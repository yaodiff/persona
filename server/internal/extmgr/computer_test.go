package extmgr

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/mcpwait"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// fakeNoLock and fakeCursor are what each fake Mac (computerMac) was told by computer.nolock and computer.cursor, by
// machine id.
var (
	fakeNoLockMu sync.Mutex
	fakeNoLock   = map[string][]bool{}
	fakeCursor   = map[string][]bool{}
	// fakeWindows and fakeApps answer list_windows and list_apps on a fake Mac when set (#4336): fakeWindows is
	// given how many run_steps calls that Mac has had, so a window can come up partway through a call.
	fakeWindows = map[string]func(runs int) string{}
	fakeApps    = map[string]string{}
	// fakeEnded are the session labels Cua Driver ended on a fake Mac (#7590): every call under one is refused as Cua
	// Driver refuses it, until start_session.
	fakeEnded = map[string]map[string]bool{}
)

// computerCall is one computer.call a fake node got.
type computerCallSeen struct {
	Session     string         `json:"session"`
	Tool        string         `json:"tool"`
	Args        map[string]any `json:"args"`
	KeepAwake   bool           `json:"keep_awake"`
	ClickSound  string         `json:"click_sound"`
	CallID      string         `json:"call_id"`
	WaitMachine string         `json:"wait_machine"`
}

// computerMac is a Mac whose node answers computer.status and computer.call as a node with Cua Driver set up does, and
// says which it was asked.
func computerMac(t *testing.T, h *Manager, id string) (*Machine, func() (statuses int, calls []computerCallSeen)) {
	t.Helper()
	builtin := fakeNodesBuiltin
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide, nodeSide := rpc.New("h", h2nW), rpc.New("n", n2hW)
	var mu sync.Mutex
	statuses, calls := 0, []computerCallSeen{}
	var job *proto.ComputerJob // the node's install or grant, as computer.status says it
	board := ""                // its clipboard
	nodeSide.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		method, params = fakeBuiltinRoute(t, builtin, method, params) // either way the hub asks, the same answers (#1830 step 5)
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case "computer.status":
			statuses++
			return proto.Computer{Installed: true, Version: "0.38.0", Daemon: true, Accessibility: "granted", ScreenRecording: "granted",
				Desktop: "active", Ready: true, Job: job, Checked: time.Now()}, nil
		case "computer.clipboard": // a clipboard of its own
			var p struct {
				Set *string `json:"set"`
			}
			_ = json.Unmarshal(params, &p)
			if p.Set != nil {
				board = *p.Set
				return map[string]any{"ok": true}, nil
			}
			return map[string]any{"text": board}, nil
		case "computer.nolock": // kept apart (fakeNoLock): the plugin's changes send it to every Mac
			var p struct {
				On bool `json:"on"`
			}
			_ = json.Unmarshal(params, &p)
			fakeNoLockMu.Lock()
			fakeNoLock[id] = append(fakeNoLock[id], p.On)
			fakeNoLockMu.Unlock()
			return map[string]any{"held": p.On}, nil
		case "computer.cursor": // kept apart too (fakeCursor)
			var p struct {
				On bool `json:"on"`
			}
			_ = json.Unmarshal(params, &p)
			fakeNoLockMu.Lock()
			fakeCursor[id] = append(fakeCursor[id], p.On)
			fakeNoLockMu.Unlock()
			return map[string]any{"restarted": false}, nil
		case "computer.lock":
			calls = append(calls, computerCallSeen{Tool: method})
			return proto.Computer{Installed: true, Daemon: true, Desktop: "locked", Checked: time.Now()}, nil
		case "computer.install", "computer.grant", "computer.cancel":
			calls = append(calls, computerCallSeen{Tool: method})
			job = &proto.ComputerJob{Kind: strings.TrimPrefix(method, "computer."), State: "running", Step: "下载安装器"}
			if method == "computer.cancel" {
				job = &proto.ComputerJob{Kind: "install", State: "cancelled"}
			}
			return proto.Computer{Job: job, Checked: time.Now()}, nil
		case "computer.call":
			var c computerCallSeen
			_ = json.Unmarshal(params, &c)
			calls = append(calls, c)
			fakeNoLockMu.Lock()
			windows, apps := fakeWindows[id], fakeApps[id]
			ended := fakeEnded[id][c.Session]
			if ended && c.Tool == "start_session" {
				delete(fakeEnded[id], c.Session)
			}
			fakeNoLockMu.Unlock()
			switch {
			case ended && c.Tool == "start_session":
				return proto.ComputerResult{Output: json.RawMessage(`{"active": true, "revived": true, "session": "` + c.Session + `"}`)}, nil
			case ended:
				return nil, &rpc.Error{Message: "cua-driver " + c.Tool + ": session has ended; tool call '" + c.Tool + "' was rejected. " +
					"Call start_session with session '" + c.Session + "' to start it again, or use a new session label."}
			}
			switch c.Tool {
			case "list_apps":
				if apps != "" {
					return proto.ComputerResult{Output: json.RawMessage(apps)}, nil
				}
				return proto.ComputerResult{Output: json.RawMessage(`{"apps": [{"name": "Calculator", "bundle_id": "com.apple.calculator"}, {"name": "Terminal", "bundle_id": "com.apple.Terminal"}]}`)}, nil
			case "list_windows":
				if windows != nil {
					runs := 0
					for _, seen := range calls {
						if seen.Tool == "run_steps" {
							runs++
						}
					}
					return proto.ComputerResult{Output: json.RawMessage(windows(runs))}, nil
				}
				return proto.ComputerResult{Output: json.RawMessage(`{"windows": []}`)}, nil
			case "run_script":
				return proto.ComputerResult{Output: json.RawMessage(`{"ok": true, "value": {"pid": 63, "window_id": 2318}}`)}, nil
			case "get_window_state", "get_desktop_state":
				if since, _ := c.Args["since"].(string); since != "" { // tracing an element token (#7560)
					status := "other_window"
					if since == "s00000070" { // a snapshot of System Settings' window
						status = "no_change"
					}
					return proto.ComputerResult{Output: json.RawMessage(`{"since_status": "` + status + `"}`)}, nil
				}
				return proto.ComputerResult{Output: json.RawMessage(`{"snapshot_id": "s1", "tree_markdown": "- [5] AXButton (7)"}`),
					Image: &proto.Image{Mime: "image/jpeg", Data: "AAAA", Width: 230, Height: 408}}, nil
			}
			return proto.ComputerResult{Output: json.RawMessage(`{"ok": true, "executed": 1}`)}, nil
		}
		return nil, &rpc.Error{Message: "unknown method " + method}
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })
	m := newMachine(h, MachineConfig{ID: id, Name: id})
	m.peer, m.online, m.info = hubSide, true, proto.NodeInfo{Protocol: nodeproto.Version, Capabilities: []string{nodeproto.MCPWait}, OS: "darwin", BuiltinPrograms: fakeBuiltinPrograms(builtin)}
	h.fleetMu.Lock()
	h.machines, h.byID[id] = append(h.machines, m), m
	h.fleetMu.Unlock()
	return m, func() (int, []computerCallSeen) {
		mu.Lock()
		defer mu.Unlock()
		return statuses, append([]computerCallSeen(nil), calls...)
	}
}

func toolNames(tools []proto.Tool) string {
	var names []string
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return strings.Join(names, " ")
}

// The plugin is off until the user turns it on, then machine by machine: until then no node is asked, the assistant
// has no tools for it and the machines say nothing of it.
func TestComputerExtensionIsOffUntilTurnedOnForAMachine(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	mux := h.routes()
	mac, seen := computerMac(t, h, "mac")
	box := newMachine(h, MachineConfig{ID: "box", Name: "box"})
	box.online, box.info = true, proto.NodeInfo{OS: "linux"}
	h.machines, h.byID["box"] = append(h.machines, box), box

	if p := h.extensions.reg.Get(cuaExtensionID); p == nil || p.Record.Enabled {
		t.Fatalf("the built-in plugin is there and off: %+v", p)
	}
	if shownComputer(t, h, mac) != nil || strings.Contains(toolNames(toolsForAgents(t, h, true)), "computer_") {
		t.Fatal("off: no Computer, no tools")
	}
	h.refreshComputer(ctx, mac)
	if code, _ := call(t, mux, "GET", "/api/v1/machines/mac/computer", h.cfg.Token, ""); code != 404 {
		t.Fatalf("off: %d", code)
	}
	if n, _ := seen(); n != 0 {
		t.Fatal("the node is not asked while the plugin is off")
	}

	if err := h.extensions.reg.SetEnabled(ctx, cuaExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if c := shownComputer(t, h, mac); c == nil || c.Enabled {
		t.Fatalf("on, not yet for this Mac: %+v", c)
	}
	if shownComputer(t, h, box) != nil {
		t.Fatal("not a Mac")
	}
	if tools := toolsForAgents(t, h, true); strings.Contains(toolNames(tools), "computer_") {
		t.Fatal("no tools before some machine is on")
	}
	if code, _ := call(t, mux, "PUT", "/api/v1/machines/box/computer", h.cfg.Token, `{"enabled": true}`); code != 404 {
		t.Fatalf("a Linux machine: %d", code)
	}

	code, out := call(t, mux, "PUT", "/api/v1/machines/mac/computer", h.cfg.Token, `{"enabled": true}`)
	if code != 200 || out["enabled"] != true || out["ready"] != true || out["version"] != "0.38.0" {
		t.Fatalf("turned on for the Mac: %d %v", code, out)
	}
	if got := toolNames(toolsForAgents(t, h, true)); !strings.Contains(got, "computer_state") || !strings.Contains(got, "computer_run") {
		t.Fatalf("the assistant's tools: %s", got)
	}
	if strings.Contains(toolNames(toolsForAgents(t, h, false)), "computer_") {
		t.Fatal("the assistant's only: agents use Cua Driver's own MCP on their machine")
	}
	if c := shownComputer(t, h, mac); c == nil || !c.Enabled || !c.Ready {
		t.Fatalf("the machine list says it: %+v", c)
	}

	if !h.portAllowed(mac, screenPort) || h.portAllowed(box, screenPort) || h.portAllowed(mac, 5901) {
		t.Fatal("the live screen's port is open for a Mac it is on for, and only that port")
	}

	code, out = call(t, mux, "PUT", "/api/v1/machines/mac/computer", h.cfg.Token, `{"enabled": false}`)
	if code != 200 || out["enabled"] != false || h.computerTools() != nil {
		t.Fatalf("off again: %d %v", code, out)
	}
	if err := h.extensions.reg.SetEnabled(ctx, cuaExtensionID, false, "test"); err != nil {
		t.Fatal(err)
	}
	if shownComputer(t, h, mac) != nil {
		t.Fatal("the plugin off: nothing of it")
	}
	if h.portAllowed(mac, screenPort) {
		t.Fatal("the plugin off: no live screen")
	}
}

// computer_state and computer_run reach the node as Cua Driver calls under the conversation's session label; the
// screenshot comes back as an image beside the JSON.
func TestComputerToolsCallTheNode(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	_, seen := computerMac(t, h, "mac")
	if err := h.extensions.reg.SetEnabled(ctx, cuaExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if res := h.callComputerTool(ctx, "conv1234-5678", "computer_state", json.RawMessage(`{"machine": "mac"}`)); !res.IsError ||
		!strings.Contains(res.Content[0].Text, "not turned on") {
		t.Fatalf("not on for the Mac yet: %+v", res)
	}
	if err := h.extensions.reg.SetConfig(ctx, cuaExtensionID, json.RawMessage(`{"machines": ["mac"], "app_approval": false}`), "test"); err != nil {
		t.Fatal(err)
	}

	res := h.callComputerTool(ctx, "conv1234-5678", "computer_state", json.RawMessage(`{"machine": "mac", "app": "Calculator", "query": "7"}`))
	if res.IsError || len(res.Content) != 2 || !strings.Contains(res.Content[0].Text, "AXButton (7)") ||
		res.Content[1].Type != "image" || res.Content[1].MimeType != "image/jpeg" {
		t.Fatalf("a window's tree and screenshot: %+v", res)
	}
	_, calls := seen()
	// One process of the app (none listed here): the window is Cua Driver's own lookup's (computer_window.go).
	if len(calls) != 3 || calls[0].Tool != "list_windows" || calls[0].Args["app"] != "Calculator" || calls[1].Tool != "run_script" ||
		!strings.Contains(calls[1].Args["script"].(string), `cua.getApp("Calculator")`) {
		t.Fatalf("the window is found by Cua Driver's own lookup: %+v", calls)
	}
	if w := calls[2]; w.Tool != "get_window_state" || w.Args["window_id"] != float64(2318) || w.Args["query"] != "7" || w.Session != "persona-conv1234" {
		t.Fatalf("then read under the conversation's session: %+v", w)
	}

	steps := `[{"click": {"app": "Calculator", "role": "button", "name": "6"}}, {"tool": "press_key", "args": {"key": "return"}}]`
	res = h.callComputerTool(ctx, "conv1234-5678", "computer_run", json.RawMessage(`{"machine": "mac", "app": "Calculator", "steps": `+steps+`}`))
	if res.IsError || !strings.Contains(res.Content[0].Text, `"executed":1`) {
		t.Fatalf("%+v", res)
	}
	_, calls = seen()
	if run := calls[len(calls)-1]; run.Tool != "run_steps" || len(run.Args["steps"].([]any)) != 2 || run.Session != "persona-conv1234" {
		t.Fatalf("steps go to run_steps: %+v", run)
	}
	for _, c := range calls {
		if !c.KeepAwake {
			t.Fatalf("「操作或开着 VNC 时保持唤醒」 is on unless turned off: %+v", c)
		}
		if c.CallID != "" {
			t.Fatalf("a non-opted-in computer call carried an MCP correlator: %+v", c)
		}
	}
	if err := h.extensions.reg.SetConfig(ctx, cuaExtensionID, json.RawMessage(`{"machines": ["mac"], "keep_awake": false, "app_approval": false}`), "test"); err != nil {
		t.Fatal(err)
	}
	h.callComputerTool(ctx, "conv1234-5678", "computer_run", json.RawMessage(`{"machine": "mac", "steps": `+steps+`}`))
	if _, calls = seen(); calls[len(calls)-1].KeepAwake {
		t.Fatal("turned off: the node keeps nothing awake")
	}
	const callID = "44444444-4444-4444-8444-444444444444"
	res = h.callComputerTool(mcpwait.WithCall(ctx, callID), "conv1234-5678", "computer_run",
		json.RawMessage(`{"machine": "mac", "steps": `+steps+`}`))
	if _, calls = seen(); res.IsError || calls[len(calls)-1].CallID != callID || calls[len(calls)-1].WaitMachine != "" {
		t.Fatalf("an opted-in computer call carried the wrong correlation fields: %+v, %+v", calls[len(calls)-1], res)
	}
	res = h.callComputerTool(mcpwait.WithCall(ctx, "not-a-uuid"), "conv1234-5678", "computer_run",
		json.RawMessage(`{"machine": "mac", "steps": `+steps+`}`))
	if _, calls = seen(); res.IsError || calls[len(calls)-1].CallID != "" {
		t.Fatalf("an invalid id must disable reporting without failing the tool: %+v, %+v", calls[len(calls)-1], res)
	}
	if got := stepActions(json.RawMessage(steps)); strings.Join(got, " ") != "click press_key" {
		t.Fatalf("the log names the actions only: %v", got)
	}
	if res := h.callComputerTool(ctx, "c", "computer_run", json.RawMessage(`{"machine": "mac"}`)); !res.IsError {
		t.Fatal("neither script nor steps")
	}

	// Asking first, when the user set it: unanswered (or declined), nothing runs.
	if err := h.extensions.reg.SetConfig(ctx, cuaExtensionID, json.RawMessage(`{"machines": ["mac"], "confirm": true, "app_approval": false}`), "test"); err != nil {
		t.Fatal(err)
	}
	_, before := seen()
	deskOf(h) // nobody answers on the phone
	short, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	res = h.callComputerTool(short, "c", "computer_run", json.RawMessage(`{"machine": "mac", "script": "const a = await cua.getApp(\"Calculator\"); return 1"}`))
	_, after := seen()
	if !res.IsError || !strings.Contains(res.Content[0].Text, "declined") {
		t.Fatalf("not confirmed: %+v", res)
	}
	for _, c := range after[len(before):] { // only the look at its apps and windows (#4336)
		if c.Tool != "list_apps" && c.Tool != "list_windows" {
			t.Fatalf("not confirmed, yet it ran: %+v", c)
		}
	}
}

func TestMCPWaitComputerTargetCapability(t *testing.T) {
	h, _ := extensionTestHub(t)
	m, seen := computerMac(t, h, "mac")
	m.mu.Lock()
	m.info.Protocol, m.info.Capabilities = 0, nil // an old target cannot correlate a nested elicitation
	m.mu.Unlock()
	const callID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	kinds := make(chan proto.MCPWaitKind, 4)
	ctx := mcpwait.WithCall(context.Background(), callID)
	ctx = mcpwait.WithPush(ctx, func(kind proto.MCPWaitKind) func() {
		kinds <- kind
		return func() { kinds <- proto.MCPWaitApproval } // the enclosing manager-owned card is still open
	})
	if _, err := h.computerCall(ctx, m, "session", "conversation", "run_script", map[string]any{"script": "return 1"}); err != nil {
		t.Fatal(err)
	}
	_, calls := seen()
	if calls[len(calls)-1].CallID != "" {
		t.Errorf("an incapable target received call_id %q", calls[len(calls)-1].CallID)
	}
	if len(kinds) != 2 {
		t.Errorf("old-target wait transitions = %d, want unknown then the enclosing approval", len(kinds))
	} else {
		for _, want := range []proto.MCPWaitKind{proto.MCPWaitUnknown, proto.MCPWaitApproval} {
			select {
			case got := <-kinds:
				if got != want {
					t.Errorf("old-target kind = %q, want %q", got, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("old-target kind %q did not arrive", want)
			}
		}
	}

	m.mu.Lock()
	m.info.Protocol, m.info.Capabilities = 0, []string{nodeproto.MCPWait}
	m.mu.Unlock()
	if m.mcpWaitCapable() {
		t.Error("a capability without a handshake protocol must not enable call_id forwarding")
	}

	m.mu.Lock()
	m.info.Protocol, m.info.Capabilities = nodeproto.Version, []string{nodeproto.MCPWait}
	m.mu.Unlock()
	if _, err := h.computerCall(ctx, m, "session", "conversation", "run_script", map[string]any{"script": "return 1"}); err != nil {
		t.Fatal(err)
	}
	_, calls = seen()
	if calls[len(calls)-1].CallID != callID || calls[len(calls)-1].WaitMachine != "" {
		t.Errorf("capable-target correlation = %+v", calls[len(calls)-1])
	}
	if len(kinds) != 0 {
		t.Errorf("a capable target caused %d fallback transitions", len(kinds))
	}
}

// A long tree is cut where a character starts, saying how much is left.
func TestComputerResultCutsLongOutput(t *testing.T) {
	long := `"` + strings.Repeat("界", computerMost) + `"`
	res := computerResult(&proto.ComputerResult{Output: json.RawMessage(long)})
	text := res.Content[0].Text
	if len(text) > computerMost+100 || !strings.Contains(text, "more bytes") || !strings.HasPrefix(text, `"界`) {
		t.Fatalf("%d %q", len(text), text[len(text)-60:])
	}
	if strings.ContainsRune(text, '�') {
		t.Fatal("cut inside a character")
	}
}

// The machine page's "Install Cua Driver", "Grant access" and "Cancel" start and stop the node's job; its state comes
// back on the machine.
func TestComputerJobs(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	mux := h.routes()
	mac, seen := computerMac(t, h, "mac")
	if code, _ := call(t, mux, "POST", "/api/v1/machines/mac/computer/install", h.cfg.Token, ""); code != 404 {
		t.Fatalf("the plugin off: %d", code)
	}
	if err := h.extensions.reg.SetEnabled(ctx, cuaExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/machines/mac/computer/install", h.cfg.Token, ""); code != 404 {
		t.Fatalf("not on for this Mac: %d", code)
	}
	if err := h.extensions.reg.SetConfig(ctx, cuaExtensionID, json.RawMessage(`{"machines": ["mac"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(t, mux, "POST", "/api/v1/machines/mac/computer/reboot", h.cfg.Token, ""); code != 404 {
		t.Fatalf("only the three: %d", code)
	}
	code, out := call(t, mux, "POST", "/api/v1/machines/mac/computer/install", h.cfg.Token, "")
	job, _ := out["job"].(map[string]any)
	if code != 200 || out["enabled"] != true || job["kind"] != "install" || job["state"] != "running" || job["step"] != "下载安装器" {
		t.Fatalf("%d %v", code, out)
	}
	if c := shownComputer(t, h, mac); c == nil || c.Job == nil || c.Job.State != "running" {
		t.Fatalf("the machine list says it: %+v", c)
	}
	if _, out = call(t, mux, "POST", "/api/v1/machines/mac/computer/cancel", h.cfg.Token, ""); out["job"].(map[string]any)["state"] != "cancelled" {
		t.Fatalf("%v", out)
	}
	call(t, mux, "POST", "/api/v1/machines/mac/computer/grant", h.cfg.Token, "")
	// "Lock" and the app's word that it unlocks it over the live screen (#1507 a-2): the second only goes to the log.
	if code, out := call(t, mux, "POST", "/api/v1/machines/mac/computer/unlocking", h.cfg.Token, ""); code != 200 || out["enabled"] != true {
		t.Fatalf("unlocking: %d %v", code, out)
	}
	if code, out := call(t, mux, "POST", "/api/v1/machines/mac/computer/lock", h.cfg.Token, ""); code != 200 || out["desktop"] != "locked" {
		t.Fatalf("锁上: %d %v", code, out)
	}
	var asked []string
	_, calls := seen()
	for _, c := range calls {
		asked = append(asked, c.Tool)
	}
	if strings.Join(asked, " ") != "computer.install computer.cancel computer.grant computer.lock" {
		t.Fatalf("%v", asked)
	}
	same := proto.Computer{Job: &proto.ComputerJob{Kind: "install", State: "running"}}
	again := proto.Computer{Job: &proto.ComputerJob{Kind: "install", State: "running"}}
	if !sameComputer(same, again) || sameComputer(same, proto.Computer{}) {
		t.Fatal("jobs compare by value")
	}
	two := func() []proto.Display {
		return []proto.Display{{ID: 1, Width: 1512, Height: 982, Main: true}, {ID: 2, X: 1512, Width: 1920, Height: 1080}}
	}
	if !sameComputer(proto.Computer{Displays: two()}, proto.Computer{Displays: two()}) || sameComputer(proto.Computer{Displays: two()}, proto.Computer{}) ||
		!sameBrowser(proto.Browser{Displays: two()}, proto.Browser{Displays: two()}) || sameBrowser(proto.Browser{Displays: two()}, proto.Browser{Displays: two()[:1]}) {
		t.Fatal("displays compare by value: a display plugged in or out goes out to the apps")
	}
	mac.mu.Lock()
	mac.online = false
	mac.mu.Unlock()
	if code, _ := call(t, mux, "POST", "/api/v1/machines/mac/computer/install", h.cfg.Token, ""); code != 409 {
		t.Fatalf("offline: %d", code)
	}
}

// An answer to an older question never replaces a newer one: a page's refresh can land after the install it raced.
func TestKeepComputerKeepsTheNewest(t *testing.T) {
	h := newTestManager(t)
	m := newMachine(h, MachineConfig{ID: "mac", Name: "mac"})
	early, late := time.Now(), time.Now().Add(time.Second)
	running := proto.Computer{Job: &proto.ComputerJob{Kind: "install", State: "running"}}
	cancelled := proto.Computer{Job: &proto.ComputerJob{Kind: "install", State: "cancelled"}}
	if !h.keepComputer(m, cancelled, late) || h.keepComputer(m, running, early) {
		t.Fatal("the late answer to the early question is dropped")
	}
	if m.computer.Job.State != "cancelled" {
		t.Fatalf("%+v", m.computer.Job)
	}
	if h.keepComputer(m, cancelled, late.Add(time.Second)) {
		t.Fatal("the same again is no change")
	}

	// The node's own time decides when it says one: a status asked after an install but answered before it.
	m2 := newMachine(h, MachineConfig{ID: "mac2", Name: "mac2"})
	installing := proto.Computer{Job: &proto.ComputerJob{Kind: "install", State: "running"}, Checked: early.Add(2 * time.Second)}
	before := proto.Computer{Checked: early.Add(time.Second)}
	if !h.keepComputer(m2, installing, early) || h.keepComputer(m2, before, late) {
		t.Fatal("the answer the node gave first is the older one, however late it was asked")
	}
	if m2.computer.Job == nil {
		t.Fatal("the install's answer is kept")
	}
}

// cua and ego turned on and off while tells and refreshes read their settings: machineExtension reads a snapshot taken
// under the registry lock, so `go test -race` reports nothing.
func TestMachineExtensionToggledWhileRead(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	stop := make(chan struct{})
	var readers sync.WaitGroup
	for _, id := range []string{cuaExtensionID, egoExtensionID} {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = h.machineExtension(id)
				}
			}
		}()
	}
	for i := range 50 {
		for _, id := range []string{cuaExtensionID, egoExtensionID} {
			if err := h.extensions.reg.SetEnabled(ctx, id, i%2 == 0, "test"); err != nil {
				t.Fatal(err)
			}
		}
	}
	close(stop)
	readers.Wait()
	if err := h.extensions.reg.SetEnabled(ctx, cuaExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if !h.cua().On {
		t.Fatal("cua on after the toggles")
	}
}
