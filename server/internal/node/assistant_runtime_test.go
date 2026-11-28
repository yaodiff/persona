package node

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

// assistantRuntimeHello is a runtime whose launcher can run the assistant (caps.assistant).
func assistantRuntimeHello() map[string]any {
	return map[string]any{"runtime": "dsh", "name": "DSH", "launcher": "dsh",
		"caps": map[string]any{"start": true, "send": true, "interrupt": true, "assistant": true}}
}

type hubCalls struct {
	mu    sync.Mutex
	tools []json.RawMessage
	calls []json.RawMessage
}

// assistantHub answers what the hub answers an assistant on a runtime: the snapshot, its tools, their calls.
func assistantHub(hub *runtimeHub) *hubCalls {
	c := &hubCalls{}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	hub.answer = func(method string, params json.RawMessage) (any, error, bool) {
		c.mu.Lock()
		defer c.mu.Unlock()
		switch method {
		case "assistant.context":
			return map[string]any{"text": "- 现在：测试时间\n- 在线电脑：测试机"}, nil, true
		case "assistant.lark":
			return map[string]any{}, nil, true
		case "mcp.tools":
			c.tools = append(c.tools, params)
			var p struct {
				Assistant bool `json:"assistant"`
			}
			_ = json.Unmarshal(params, &p)
			tools := []proto.Tool{{Name: "phone_contacts", InputSchema: json.RawMessage(`{"type":"object"}`)}}
			if p.Assistant {
				tools = append(tools, proto.Tool{Name: "ask_user", InputSchema: json.RawMessage(`{"type":"object"}`)})
			}
			return map[string]any{"tools": tools}, nil, true
		case "mcp.call":
			c.calls = append(c.calls, params)
			return proto.TextResult("ok", false), nil, true
		}
		return nil, nil, false
	}
	return c
}

func startedAssistant(t *testing.T, n *Node, host *fakeRuntime) string {
	t.Helper()
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": "session-a1", "cwd": filepath.Join(n.home, ".persona", "assistant"), "status": "idle"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	raw, err := nodeCall(t, n, "assistant.send", map[string]any{"launcher": "dsh", "cwd": filepath.Join(n.home, ".persona", "assistant"), "text": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(raw, &res)
	if res.SessionID != "session-a1" {
		t.Fatalf("assistant.send %s", raw)
	}
	return res.SessionID
}

func TestRuntimeAssistantFirstMessageIsVerbatimWithoutAutomaticCommands(t *testing.T) {
	for _, engine := range []string{"agy", "cursor", "dsh"} {
		t.Run(engine, func(t *testing.T) {
			n, hub := runtimeNode(t)
			assistantHub(hub)
			hello := assistantRuntimeHello()
			hello["runtime"], hello["launcher"] = engine, engine
			host := dialRuntime(t, n, hello)
			id := engine + "-first-message"
			cwd := filepath.Join(n.home, ".persona", "assistant")
			host.answer = func(method string, _ json.RawMessage) (any, error) {
				if method == "session.start" {
					return map[string]any{"session": proto.Session{SessionID: id, Cwd: cwd, Status: proto.StatusIdle}}, nil
				}
				return map[string]bool{"ok": true}, nil
			}
			const first = "Check this provider's usage source.\nKeep the original configuration."
			if _, err := nodeCall(t, n, "assistant.send", map[string]any{"launcher": engine, "cwd": cwd, "text": first}); err != nil {
				t.Fatal(err)
			}
			// These transports have no compact-before-send implementation, even if a caller asks for it.
			if compactable, why := n.compactableRuntime(id); compactable {
				t.Fatalf("unexpected automatic compaction support: %s", why)
			}
			row := proto.Session{SessionID: id, Cwd: cwd, Status: proto.StatusIdle, Rev: 2, ContextTokens: 190000, ContextWindow: 200000}
			if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{row}}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "nearly full runtime row", func() bool {
				got, _, ok := n.runtimes.rowOf(id)
				return ok && proto.ContextNearlyFull(int(got.ContextTokens), int(got.ContextWindow))
			})
			const next = "Continue with the same request."
			if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": id, "text": next, "compact_if_full": true}); err != nil {
				t.Fatal(err)
			}
			sends := host.got("session.send")
			if len(sends) != 2 {
				t.Fatalf("unexpected command or duplicate send: %s", sends)
			}
			for i, want := range []string{first, next} {
				var sent struct {
					Text string `json:"text"`
				}
				if err := json.Unmarshal(sends[i], &sent); err != nil {
					t.Fatal(err)
				}
				if sent.Text != want || strings.Contains(sent.Text, "/compact") {
					t.Fatalf("message changed: %q, want %q", sent.Text, want)
				}
			}
			starts := host.got("session.start")
			if len(starts) != 1 || strings.Contains(string(starts[0]), `"prompt"`) || strings.Contains(string(starts[0]), "/compact") {
				t.Fatalf("allocation sent unexpected input: %s", starts)
			}
		})
	}
}

func TestRuntimeAssistantQueue(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	hello := assistantRuntimeHello()
	hello["caps"].(map[string]any)["queue_edit"] = true
	hello["caps"].(map[string]any)["queue_send_now"] = true
	host := dialRuntime(t, n, hello)
	id := startedAssistant(t, n, host)
	const child = "queue-child"
	image := proto.Image{Mime: "image/png", Data: "aW1hZ2U="}
	host.mu.Lock()
	host.answer = func(method string, params json.RawMessage) (any, error) {
		switch method {
		case "session.send":
			return map[string]bool{"queued": true}, nil
		case "session.queue.edit":
			return proto.QueueEdit{Queue: []string{"first", ""}, Images: [][]proto.Image{nil, {image}}}, nil
		case "session.queue.send_now":
			return map[string]bool{"sent": true}, nil
		}
		return nil, fmt.Errorf("unexpected %s", method)
	}
	host.mu.Unlock()
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []proto.Session{
		{SessionID: id, Status: proto.StatusWorking, Queued: []string{"first", ""}}, {SessionID: child, IsSubagent: true},
	}})
	waitFor(t, "runtime queue listed", func() bool {
		rows := listed(t, n)
		return len(rows[id].Queued) == 2 && rows[child].Capabilities != nil
	})
	for sid, want := range map[string]bool{id: true, child: false} {
		caps := listed(t, n)[sid].Capabilities
		if caps == nil || caps.QueueEdit == nil || caps.QueueSendNow == nil || *caps.QueueEdit != want || *caps.QueueSendNow != want {
			t.Fatalf("queue capabilities for %s: %+v", sid, caps)
		}
	}
	if out, err := nodeCall(t, n, "session.send", map[string]any{"session_id": id, "text": "next"}); err != nil || string(out) != `{"ok":true,"queued":true}` {
		t.Fatalf("assistant send: %s, %v", out, err)
	}
	edit := "session.queue.edit"
	sendNow := "session.queue.send_now"
	out, err := nodeCall(t, n, edit, map[string]any{"session_id": id, "keep": []int{1}})
	if err != nil || string(out) != `{"queue":["first",""],"images":[null,[{"mime":"image/png","data":"aW1hZ2U="}]]}` {
		t.Fatalf("assistant queue edit: %s, %v", out, err)
	}
	if calls := host.got(edit); len(calls) != 1 || string(calls[0]) != `{"keep":[1],"session_id":"`+id+`"}` {
		t.Fatalf("forwarded edit: %s", calls)
	}
	if out, err := nodeCall(t, n, sendNow, map[string]any{"session_id": id}); err != nil || string(out) != `{"sent":true}` {
		t.Fatalf("assistant send-now: %s, %v", out, err)
	}
	for _, method := range []string{edit, sendNow} {
		if _, err := nodeCall(t, n, method, map[string]any{"session_id": id + subagentSep + child}); rpcCode(err) != "unsupported" || len(host.got(method)) != 1 {
			t.Fatalf("%s forwarded to subagent: %v", method, err)
		}
	}
	_ = host.conn.Close()
	waitFor(t, "runtime disconnected", func() bool {
		conn, _, _ := n.runtimes.belongs(id)
		return conn == nil
	})
	for _, method := range []string{edit, sendNow} {
		if _, err := nodeCall(t, n, method, map[string]any{"session_id": id}); rpcCode(err) != "runtime_away" {
			t.Fatalf("%s offline: %v", method, err)
		}
	}
}

func TestRuntimeAssistantModeForwardsAssistant(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	host := dialRuntime(t, n, assistantRuntimeHello())
	id := startedAssistant(t, n, host)
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": id, "permission_mode": "plan"}); err != nil {
		t.Fatal(err)
	}
	calls := host.got("session.modes")
	if len(calls) != 1 {
		t.Fatalf("session.modes calls: %s", calls)
	}
	var got struct {
		SessionID string `json:"session_id"`
		Mode      string `json:"permission_mode"`
		Assistant bool   `json:"assistant"`
	}
	if err := json.Unmarshal(calls[0], &got); err != nil {
		t.Fatal(err)
	}
	if got.SessionID != id || got.Mode != "plan" || !got.Assistant {
		t.Fatalf("session.modes payload: %s", calls[0])
	}
}

func TestAssistantOnARuntimeStartsAndSendsWithTheSnapshot(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	n.dir = filepath.Join(n.home, ".persona")
	cwd := filepath.Join(n.dir, "assistant")
	privateMemory := filepath.Join(n.dir, "profiles", "claude", "projects", memoryKey(cwd), "memory")
	writeNote(t, privateMemory, "runtime-copy.md", "A note from another profile.\n", time.Time{})
	host := dialRuntime(t, n, assistantRuntimeHello())
	if a, err := n.assistantAgent(context.Background(), "dsh"); err != nil || a != assistantOnRuntime {
		t.Fatalf("assistantAgent: %q %v", a, err)
	}
	sid := startedAssistant(t, n, host)

	starts := host.got("session.start")
	if len(starts) != 1 || !strings.Contains(string(starts[0]), `"assistant":true`) {
		t.Fatalf("session.start %s", starts)
	}
	sends := host.got("session.send")
	if len(sends) != 1 {
		t.Fatalf("%d sends", len(sends))
	}
	var p struct {
		SessionID string `json:"session_id"`
		Text      string `json:"text"`
		Context   string `json:"context"`
		Assistant bool   `json:"assistant"`
		MemoryDir string `json:"memory_dir"`
	}
	_ = json.Unmarshal(sends[0], &p)
	if p.SessionID != sid || p.Text != "hello" || !p.Assistant || p.MemoryDir == "" {
		t.Fatalf("send %s", sends[0])
	}
	if !strings.Contains(p.Context, "测试时间") || !strings.Contains(p.Context, "MEMORY.md") {
		t.Fatalf("the first message's context: %q", p.Context)
	}
	if p.MemoryDir != n.memoryDir(cwd) || !strings.Contains(p.Context, "(runtime-copy.md)") {
		t.Fatalf("the runtime's first turn missed the merged memory: %s", sends[0])
	}
	if got := readNote(t, p.MemoryDir, "runtime-copy.md"); got != "A note from another profile.\n" {
		t.Fatalf("the runtime's memory note: %q", got)
	}
	if st, ok := n.state.get(sid); !ok || st.Launcher != "dsh" || !strings.HasSuffix(st.Cwd, "/.persona/assistant") {
		t.Fatalf("state %+v", st)
	}
	if !n.isAssistantSession(sid) || !n.runtimeAssistant(sid) {
		t.Fatal("not known as an assistant conversation")
	}

	// The phone's message to it is the assistant's: the snapshot again (only what changed) and the mode.
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": sid, "permission_mode": "plan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sid, "text": "next", "client_msg_id": "m-2"}); err != nil {
		t.Fatal(err)
	}
	sends = host.got("session.send")
	if len(sends) != 2 {
		t.Fatalf("%d sends", len(sends))
	}
	var q struct {
		Text      string `json:"text"`
		Mode      string `json:"mode"`
		Assistant bool   `json:"assistant"`
		Context   string `json:"context"`
	}
	_ = json.Unmarshal(sends[1], &q)
	if q.Text != "next" || q.Mode != "plan" || !q.Assistant || strings.Contains(q.Context, "MEMORY.md") || !strings.Contains(q.Context, "测试时间") {
		t.Fatalf("second send %s", sends[1])
	}
	if s := listed(t, n)[sid]; s.PermissionMode != "plan" {
		t.Fatalf("listed mode %q", s.PermissionMode)
	}

	// A message without a launcher (the node forgot the conversation's state: a restart) still reaches its runtime.
	n.state.put(sid, sessionState{})
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{"session_id": sid, "text": "after a restart"}); err != nil {
		t.Fatalf("without a launcher: %v", err)
	}
	if got := host.got("session.send"); len(got) != 3 || !strings.Contains(string(got[2]), "after a restart") {
		t.Fatalf("sends %d", len(got))
	}

	// Stop from the phone: the runtime's interrupt.
	if _, err := nodeCall(t, n, "session.interrupt", map[string]any{"session_id": sid}); err != nil || len(host.got("session.interrupt")) != 1 {
		t.Fatalf("interrupt: %v", err)
	}
	// Another kind of agent asked for (the main conversation follows "Model settings › Main chat"): the conversation stays on its
	// runtime, the only one that can resume it.
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{"session_id": sid, "launcher": "claude", "text": "on claude"}); err != nil {
		t.Fatalf("a claude launcher for a runtime conversation: %v", err)
	}
	if got := host.got("session.send"); len(got) != 4 || !strings.Contains(string(got[3]), "on claude") {
		t.Fatalf("sends %d", len(got))
	}
}

// A public mention on a runtime-backed main chat gets the runtime's ordinary input but no owner snapshot (#11130).
func TestAssistantPublicSendOnRuntimeSkipsSnapshot(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	host := dialRuntime(t, n, assistantRuntimeHello())
	sid := startedAssistant(t, n, host)
	hub.mu.Lock()
	base := hub.answer
	contextCalls := 0
	hub.answer = func(method string, params json.RawMessage) (any, error, bool) {
		if method == "assistant.context" {
			contextCalls++
		}
		return base(method, params)
	}
	hub.mu.Unlock()
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sid, "text": "public mention", "public": true}); err != nil {
		t.Fatal(err)
	}
	hub.mu.Lock()
	if contextCalls != 0 {
		hub.mu.Unlock()
		t.Fatalf("public runtime send requested %d owner snapshots", contextCalls)
	}
	hub.mu.Unlock()
	sends := host.got("session.send")
	if len(sends) != 2 || strings.Contains(string(sends[1]), "测试时间") {
		t.Fatalf("public runtime input carries the owner snapshot: %s", sends)
	}
}

// A runtime may put the client_msg_id it was sent a message with on the message's line (the node-to-runtime
// session.send, as Paseo does). For an assistant conversation that id is the message's own: the app's, or none when it
// came without one, so the apps match their pending bubble by it, or by its words. The node used to send every message
// under an id of its own, which such a runtime put on the line: the app kept its bubble and showed the message twice
// (#5225, as #5200).
func TestAssistantOnARuntimeSendsTheMessagesOwnID(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	host := dialRuntime(t, n, assistantRuntimeHello())
	var mu sync.Mutex
	started, seq := 0, map[string]int{}
	host.answer = func(method string, params json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		switch method {
		case "session.start":
			started++
			return map[string]any{"session": map[string]any{"session_id": fmt.Sprintf("session-a%d", started), "cwd": filepath.Join(n.home, ".persona", "assistant"), "status": "idle"}}, nil
		case "session.send": // a runtime that ties its own line: the user's line carries the id it was sent with
			var p struct {
				SessionID   string `json:"session_id"`
				Text        string `json:"text"`
				ClientMsgID string `json:"client_msg_id"`
			}
			_ = json.Unmarshal(params, &p)
			seq[p.SessionID]++
			s := seq[p.SessionID]
			_ = host.peer.Notify("session.items", map[string]any{"session_id": p.SessionID, "from_seq": s - 1, "to_seq": s,
				"items": []proto.Item{{ID: fmt.Sprintf("%d#0", s), Seq: s, Role: "user", Kind: proto.KindText, Text: p.Text, ClientMsgID: p.ClientMsgID}}})
		}
		return map[string]any{"ok": true}, nil
	}
	// pushed is the client_msg_id the hub got on the line with text, once it got it.
	pushed := func(text string) string {
		t.Helper()
		var id string
		waitFor(t, "the line "+text, func() bool {
			hub.mu.Lock()
			defer hub.mu.Unlock()
			for _, raw := range hub.items {
				var b struct {
					Items []proto.Item `json:"items"`
				}
				_ = json.Unmarshal(raw, &b)
				for _, it := range b.Items {
					if it.Text == text {
						id = it.ClientMsgID
						return true
					}
				}
			}
			return false
		})
		return id
	}
	// sentID is the client_msg_id the runtime got the message with text under; absent is "", never a key with "".
	sentID := func(text string) string {
		t.Helper()
		for _, raw := range host.got("session.send") {
			var p map[string]any
			_ = json.Unmarshal(raw, &p)
			if p["text"] != text {
				continue
			}
			id, has := p["client_msg_id"]
			if !has {
				return ""
			}
			if s, _ := id.(string); s != "" {
				return s
			}
			t.Fatalf("%q went with client_msg_id %v", text, id)
		}
		t.Fatalf("%q never reached the runtime", text)
		return ""
	}
	assistantDir := filepath.Join(n.home, ".persona", "assistant")

	// A new conversation's first message as the manager sends it (no id since #5200): the line carries none, and the
	// app finds it by its words.
	raw, err := nodeCall(t, n, "assistant.send", map[string]any{"launcher": "dsh", "cwd": assistantDir, "text": "第一条"})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(raw, &res)
	sid := res.SessionID
	if sid != "session-a1" || !n.runtimeAssistant(sid) {
		t.Fatalf("assistant.send %s", raw)
	}
	if got, line := sentID("第一条"), pushed("第一条"); got != "" || line != "" {
		t.Fatalf("a first message without an id went with %q, its line carries %q", got, line)
	}

	// The app's message, with its id: the runtime gets that id, and the line carries it.
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sid, "text": "第二条", "client_msg_id": "app-2"}); err != nil {
		t.Fatal(err)
	}
	if got, line := sentID("第二条"), pushed("第二条"); got != "app-2" || line != "app-2" {
		t.Fatalf("the app's message went with %q, its line carries %q; want app-2", got, line)
	}

	// An older app's message, without one: none on the line either.
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sid, "text": "第三条"}); err != nil {
		t.Fatal(err)
	}
	if got, line := sentID("第三条"), pushed("第三条"); got != "" || line != "" {
		t.Fatalf("a message without an id went with %q, its line carries %q", got, line)
	}

	// A new conversation's first message with its id (one a person's own app opened): the line carries it.
	raw, err = nodeCall(t, n, "assistant.send", map[string]any{"launcher": "dsh", "cwd": assistantDir, "text": "新对话", "client_msg_id": "app-new"})
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &res)
	if res.SessionID != "session-a2" {
		t.Fatalf("assistant.send %s", raw)
	}
	if got, line := sentID("新对话"), pushed("新对话"); got != "app-new" || line != "app-new" {
		t.Fatalf("a first message with its id went with %q, its line carries %q; want app-new", got, line)
	}
}

func TestAssistantLauncherNeedsTheCapability(t *testing.T) {
	n, _ := runtimeNode(t)
	_ = dialRuntime(t, n, dshHello()) // a runtime without caps.assistant
	if n.assistantRuntime("dsh") != nil {
		t.Fatal("a runtime that did not declare assistant runs the assistant")
	}
	raw, _ := nodeCall(t, n, "launchers.list", map[string]any{})
	if strings.Contains(string(raw), `"assistant":true`) {
		t.Fatalf("launchers %s", raw)
	}
}

func TestAssistantToolsOnlyForTheAssistantsConversations(t *testing.T) {
	n, hub := runtimeNode(t)
	calls := assistantHub(hub)
	host := dialRuntime(t, n, assistantRuntimeHello())
	plain := dialRuntime(t, n, map[string]any{"runtime": "other", "caps": map[string]any{"send": true}})
	sid := startedAssistant(t, n, host)
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": sid, "cwd": filepath.Join(n.home, ".persona", "assistant"), "status": "idle"},
		{"session_id": "session-w", "cwd": "/w/app", "status": "idle"},
	}})
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)["session-w"]; return ok })

	toolNames := func(f *fakeRuntime, assistant bool) []string {
		var res struct {
			Tools []proto.Tool `json:"tools"`
		}
		if err := f.peer.Call(context.Background(), "mcp.tools", map[string]any{"assistant": assistant}, &res); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, x := range res.Tools {
			out = append(out, x.Name)
		}
		return out
	}
	if got := toolNames(plain, true); strings.Contains(strings.Join(got, ","), "ask_user") {
		t.Fatalf("a runtime that does not run the assistant got %v", got)
	}
	if got := strings.Join(toolNames(host, true), ","); !strings.Contains(got, "ask_user") {
		t.Fatalf("the assistant's runtime got %v", got)
	}

	call := func(f *fakeRuntime, session string) map[string]any {
		var res proto.ToolResult
		if err := f.peer.Call(context.Background(), "mcp.call", map[string]any{"name": "ask_user", "args": map[string]any{}, "session_id": session, "assistant": true}, &res); err != nil {
			t.Fatal(err)
		}
		calls.mu.Lock()
		defer calls.mu.Unlock()
		var p map[string]any
		_ = json.Unmarshal(calls.calls[len(calls.calls)-1], &p)
		return p
	}
	if p := call(host, sid); p["assistant"] != true || p["session_id"] != sid {
		t.Fatalf("the assistant's call %v", p)
	}
	if p := call(host, "session-w"); p["assistant"] == true {
		t.Fatalf("a call from another conversation passed as the assistant's: %v", p)
	}
	if p := call(plain, sid); p["assistant"] == true {
		t.Fatalf("another runtime's call passed as the assistant's: %v", p)
	}
}

func TestAssistantRuntimePIDReportsOnTheSocket(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no local peer process lookup")
	}
	previousLineage := nodeLineage
	t.Cleanup(func() { nodeLineage = previousLineage })
	nodeLineage = func() []int { return nil }
	rh := sharedHost(t)
	n, _ := runtimeNodeOn(t, rh)
	host := dialRuntime(t, n, assistantRuntimeHello())
	plain := dialRuntime(t, n, map[string]any{"runtime": "plain-pid", "caps": map[string]any{"start": true, "send": true}})
	if features, _ := host.helloR["features"].([]any); !slices.Contains(features, any("assistant_pids")) {
		t.Fatalf("assistant_pids not advertised: %v", features)
	}
	worker := exec.CommandContext(t.Context(), "sleep", "60")
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Wait() })
	const chat, other = "session-assistant-worker", "session-other-worker"
	for index, client := range []*fakeRuntime{host, plain} {
		id := []string{chat, other}[index]
		if err := client.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
			{"session_id": id, "cwd": identity.AssistantDir(n.home), "status": "idle"},
		}}); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "runtimes own their Assistant-directory rows", func() bool {
		_, _, chatListed := n.runtimes.rowOf(chat)
		_, _, otherListed := n.runtimes.rowOf(other)
		return chatListed && otherListed
	})
	report := func(t *testing.T, client *fakeRuntime, pids map[string]int) {
		t.Helper()
		if err := client.peer.Notify("runtime.assistant_pids", map[string]any{"pids": pids}); err != nil {
			t.Fatal(err)
		}
		var response any
		if err := client.peer.Call(t.Context(), "mcp.tools", map[string]any{"session_id": "not-owned"}, &response); err != nil {
			t.Fatal(err)
		}
	}
	pid := worker.Process.Pid
	report(t, plain, map[string]int{other: pid})
	report(t, host, map[string]int{other: pid, "unlisted-assistant": pid})
	if roots := n.assistantRoots(); len(roots) != 0 {
		t.Fatalf("accepted PID without assistant capability or ownership: %v", roots)
	}
	report(t, host, map[string]int{chat: pid})
	if got := n.assistantRoots()[pid]; got != chat {
		t.Fatalf("worker authorized as %q, want %q", got, chat)
	}
	report(t, host, map[string]int{})
	if got := n.assistantRoots()[pid]; got != "" {
		t.Fatalf("omitted worker still authorized as %q", got)
	}
	t.Run("hub reconnect", func(t *testing.T) {
		report(t, host, map[string]int{chat: pid})
		rh.detach(n)
		if workers := rh.reg.assistantWorkers(); len(workers) != 0 {
			t.Fatalf("hub detach kept assistant workers: %v", workers)
		}
		report(t, host, map[string]int{chat: pid})
		if workers := rh.reg.assistantWorkers(); len(workers) != 0 {
			t.Fatalf("accepted PIDs without a hub: %v", workers)
		}
		report(t, host, map[string]int{})
		reconnected, _ := runtimeNodeOn(t, rh)
		if _, _, ok := reconnected.runtimes.rowOf(chat); !ok {
			t.Fatal("assistant row did not survive hub reconnect")
		}
		if got := reconnected.assistantRoots()[pid]; got != "" {
			t.Fatalf("former worker still authorized after hub reconnect as %q", got)
		}
		report(t, host, map[string]int{chat: pid})
		if got := reconnected.assistantRoots()[pid]; got != chat {
			t.Fatalf("fresh snapshot authorized worker as %q, want %q", got, chat)
		}
	})
	report(t, host, map[string]int{chat: pid})
	if err := host.conn.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "disconnected worker cleared", func() bool { return n.assistantRoots()[pid] == "" })
}

func TestRuntimeAgentToolsRequireOwnedSessions(t *testing.T) {
	n, hub := runtimeNode(t)
	calls := assistantHub(hub)
	host := dialRuntime(t, n, assistantRuntimeHello())
	other := dialRuntime(t, n, map[string]any{"runtime": "other", "caps": map[string]any{"send": true}})
	const sid = "session-leader"
	if err := host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": sid, "cwd": "/w/app", "status": "idle"},
	}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "listed", func() bool { _, ok := listed(t, n)[sid]; return ok })
	ctx := context.Background()
	for _, runtime := range []*fakeRuntime{host, other} {
		var result struct{ Tools []proto.Tool }
		if err := runtime.peer.Call(ctx, "mcp.tools", map[string]any{"leader_only": true, "session_id": sid}, &result); err != nil {
			t.Fatal(err)
		}
		if (len(result.Tools) > 0) != (runtime == host) {
			t.Fatalf("unexpected discovery result: %+v", result)
		}
		for _, name := range []string{"task_plan", "report_to_supervisor"} {
			var res proto.ToolResult
			if err := runtime.peer.Call(ctx, "mcp.call", map[string]any{"name": name, "session_id": sid}, &res); err != nil {
				t.Fatal(err)
			}
			if res.IsError != (runtime == other) {
				t.Fatalf("%s: unexpected call result: %+v", name, res)
			}
		}
	}
	calls.mu.Lock()
	defer calls.mu.Unlock()
	if len(calls.tools) != 1 || len(calls.calls) != 2 {
		t.Fatalf("unexpected manager requests: tools %s, calls %s", calls.tools, calls.calls)
	}
	for _, raw := range append(append([]json.RawMessage(nil), calls.tools...), calls.calls...) {
		var req struct {
			SessionID string `json:"session_id"`
			Assistant bool   `json:"assistant"`
		}
		if err := json.Unmarshal(raw, &req); err != nil || req.SessionID != sid || req.Assistant {
			t.Fatalf("unexpected caller: %s (%v)", raw, err)
		}
	}
}

func TestGatedToolGoesThroughTheGate(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	skills := filepath.Join(n.home, ".persona", "skills")
	t.Setenv("PERSONA_SKILLS_DIR", skills)
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	n.dir = filepath.Join(n.home, ".persona")
	script := filepath.Join(skills, "example", "scripts", "ex.sh")
	if err := os.MkdirAll(filepath.Dir(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skills, "example", "SKILL.md"), []byte("---\nname: example\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(n.home, "ran")
	if err := os.WriteFile(script, []byte("#!/bin/bash\necho \"$*\" >> "+shellQuote(ran)+"\necho done \"$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// The hub prepares its assistant directory before sending. User-selected task directories keep their own skills.
	prepareAssistantForTest(t, n, ".persona/assistant", nil, nil)
	g := exampleGate("")
	g.Tool = &proto.GateTool{Name: "example_api", Description: "Run ex."}
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt = proto.AssistantLark{Gates: []proto.CommandGate{g}}, time.Now().Add(time.Hour)
	n.mu.Unlock()
	const gatedTool = "example_api"
	host := dialRuntime(t, n, assistantRuntimeHello())
	sid := startedAssistant(t, n, host)

	var tools struct {
		Tools []proto.Tool `json:"tools"`
	}
	if err := host.peer.Call(context.Background(), "mcp.tools", map[string]any{"assistant": true}, &tools); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(toolList(tools.Tools), gatedTool) {
		t.Fatalf("tools %v", toolList(tools.Tools))
	}
	run := func(args ...string) proto.ToolResult {
		var res proto.ToolResult
		if err := host.peer.Call(context.Background(), "mcp.call", map[string]any{"name": gatedTool, "args": map[string]any{"args": args}, "session_id": sid, "assistant": true}, &res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := run("GET", "/me"); res.IsError || !strings.Contains(res.Content[0].Text, "done GET /me") {
		t.Fatalf("a read: %+v", res)
	}
	hub.mu.Lock()
	hub.resolve = func(ix proto.Interaction) proto.Resolution {
		no := false
		return proto.Resolution{Allow: &no, Message: "not now"}
	}
	hub.mu.Unlock()
	if res := run("POST", "/conversations/oc_test/messages", `{"text":"hi"}`); !res.IsError || !strings.Contains(res.Content[0].Text, "not now") {
		t.Fatalf("a refused write: %+v", res)
	}
	hub.mu.Lock()
	asked := len(hub.asked)
	hub.mu.Unlock()
	if asked != 1 {
		t.Fatalf("%d cards", asked)
	}
	b, _ := os.ReadFile(ran)
	if got := strings.TrimSpace(string(b)); got != "GET /me" {
		t.Fatalf("ol ran %q", got)
	}
	// Not the assistant's conversation: the gated tool is not run here (it goes to the hub, which has no such tool).
	var res proto.ToolResult
	if err := host.peer.Call(context.Background(), "mcp.call", map[string]any{"name": gatedTool, "args": map[string]any{"args": []string{"GET", "/other"}}, "session_id": "session-other", "assistant": true}, &res); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(ran); strings.Contains(string(b), "/other") {
		t.Fatal("the gated tool ran for a conversation that is not the assistant's")
	}
}

func toolList(ts []proto.Tool) string {
	var out []string
	for _, x := range ts {
		out = append(out, x.Name)
	}
	return strings.Join(out, ",")
}

// A message that is only a slash command goes to the runtime on its own. With the <persona-context> block in front, an
// agent that runs its own commands (Antigravity's /compact) no longer sees a command: it records the line as plain text,
// `/compact/compact`, and compacts nothing. The snapshot is not lost: it stays unsent and rides with the next ordinary
// message, and after a /compact the conversation is told everything again, as after Persona's own compaction.
func TestAssistantOnARuntimeSendsASlashCommandOnItsOwn(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	host := dialRuntime(t, n, assistantRuntimeHello())
	sid := startedAssistant(t, n, host)
	sentText := func(i int) (text, context string) {
		t.Helper()
		sends := host.got("session.send")
		if len(sends) <= i {
			t.Fatalf("%d sends, want more than %d", len(sends), i)
		}
		var p struct {
			Text    string `json:"text"`
			Context string `json:"context"`
		}
		_ = json.Unmarshal(sends[i], &p)
		return p.Text, p.Context
	}
	seen := func() map[string]string {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.ctxSent[sid]
	}
	if seen() == nil {
		t.Fatal("the first message did not record the snapshot as sent")
	}

	// /status: no context in front, and what the conversation has seen stays as it was.
	before := seen()
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sid, "text": "/status", "client_msg_id": "m-status"}); err != nil {
		t.Fatal(err)
	}
	if text, ctx := sentText(1); text != "/status" || ctx != "" {
		t.Fatalf("/status went as %q with context %q", text, ctx)
	}
	if got := seen(); len(got) != len(before) {
		t.Fatalf("/status changed what the conversation has seen: %v -> %v", before, got)
	}

	// /compact: on its own too, and the conversation has to be told everything again afterwards.
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sid, "text": "/compact\n", "client_msg_id": "m-compact"}); err != nil {
		t.Fatal(err)
	}
	if text, ctx := sentText(2); strings.TrimSpace(text) != "/compact" || ctx != "" {
		t.Fatalf("/compact went as %q with context %q", text, ctx)
	}
	if got := seen(); got != nil {
		t.Fatalf("after /compact the conversation still counts the snapshot as told: %v", got)
	}

	// The next ordinary message carries the whole snapshot again.
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sid, "text": "next", "client_msg_id": "m-next"}); err != nil {
		t.Fatal(err)
	}
	if text, ctx := sentText(3); text != "next" || !strings.Contains(ctx, "测试时间") {
		t.Fatalf("the message after /compact went as %q with context %q", text, ctx)
	}
	if seen() == nil {
		t.Fatal("the message after /compact did not record the snapshot as sent")
	}

	// Words that merely begin with a slash, a path or a command with a sentence, are messages: they carry the context.
	for i, text := range []string{"/tmp/report.md is ready\nplease read it", "/data/logs"} {
		if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sid, "text": text, "client_msg_id": "m-words-" + string(rune('a'+i))}); err != nil {
			t.Fatal(err)
		}
		if got, ctx := sentText(4 + i); got != text || !strings.Contains(ctx, "测试时间") {
			t.Fatalf("%q went as %q with context %q", text, got, ctx)
		}
	}
	// A picture makes it a message, whatever the words.
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sid, "text": "/status", "client_msg_id": "m-pic", "images": []map[string]any{{"mime": "image/png", "data": "AA=="}}}); err != nil {
		t.Fatal(err)
	}
	if _, ctx := sentText(6); !strings.Contains(ctx, "测试时间") {
		t.Fatalf("a /status with a picture went without context: %q", ctx)
	}
}

func TestAssistantOnAgyKeepsWorkflowCommandFirstAndContextLast(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	hello := assistantRuntimeHello()
	hello["runtime"], hello["launcher"] = "agy", "agy"
	host := dialRuntime(t, n, hello)
	cwd := filepath.Join(n.home, ".persona", "assistant")
	host.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": proto.Session{SessionID: "agy-a1", Cwd: cwd, Status: proto.StatusIdle}}, nil
		}
		return map[string]bool{"ok": true}, nil
	}
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{"launcher": "agy", "cwd": cwd, "text": "warm up"}); err != nil {
		t.Fatal(err)
	}
	send := func(text string) (sent struct {
		Text    string `json:"text"`
		Context string `json:"context"`
	}) {
		t.Helper()
		if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": "agy-a1", "text": text}); err != nil {
			t.Fatal(err)
		}
		all := host.got("session.send")
		if err := json.Unmarshal(all[len(all)-1], &sent); err != nil {
			t.Fatal(err)
		}
		return sent
	}
	if got := send(" \t/boost fix the race\nkeep the body"); got.Text != "/boost fix the race\nkeep the body" ||
		!strings.Contains(got.Context, "测试时间") {
		t.Fatalf("workflow prompt = %+v", got)
	}
	if got := send("/status"); got.Text != "/status" || got.Context != "" {
		t.Fatalf("/status prompt = %+v", got)
	}
	if got := send("/tmp/report.md"); got.Text != "/tmp/report.md" || !strings.Contains(got.Context, "测试时间") {
		t.Fatalf("path prompt = %+v", got)
	}
}

func TestAssistantRuntimeStartPropagatesPermissionMode(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	host := dialRuntime(t, n, assistantRuntimeHello())
	cwd := t.TempDir()
	host.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": "session-perm-1", "cwd": cwd, "status": "idle"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"cwd":             cwd,
		"launcher":        "dsh",
		"text":            "hello",
		"permission_mode": "acceptEdits",
	}); err != nil {
		t.Fatal(err)
	}
	starts := host.got("session.start")
	if len(starts) != 1 {
		t.Fatalf("starts = %d, want 1", len(starts))
	}
	var start struct {
		PermissionMode string `json:"permission_mode"`
	}
	if err := json.Unmarshal(starts[0], &start); err != nil {
		t.Fatal(err)
	}
	if start.PermissionMode != "acceptEdits" {
		t.Fatalf("session.start permission_mode = %q, want acceptEdits (%s)", start.PermissionMode, starts[0])
	}
}

func TestRemoteAgentMemoryToolsAndPlanMode(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)

	remoteHello := map[string]any{
		"runtime": "pac", "name": "PAC", "launcher": "pac",
		"caps": map[string]any{"start": true, "send": true, "interrupt": true, "assistant": true, "remote_agent": true},
	}
	remoteHost := dialRuntime(t, n, remoteHello)
	localHost := dialRuntime(t, n, assistantRuntimeHello())

	cwd := identity.AssistantDir(n.home)
	remoteHost.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": "pac-remote-1", "cwd": cwd, "status": "idle"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	localHost.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": "dsh-local-1", "cwd": cwd, "status": "idle"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}

	if _, err := nodeCall(t, n, "assistant.send", map[string]any{"launcher": "pac", "cwd": cwd, "text": "hello remote"}); err != nil {
		t.Fatal(err)
	}
	sends := remoteHost.got("session.send")
	if len(sends) != 1 || !strings.Contains(string(sends[0]), "memory_read") || !strings.Contains(string(sends[0]), "memory_write") {
		t.Fatalf("expected initial context to instruct memory_read/memory_write, got: %s", sends)
	}

	var remoteTools struct {
		Tools []proto.Tool `json:"tools"`
	}
	if err := remoteHost.peer.Call(context.Background(), "mcp.tools", map[string]any{"assistant": true, "session_id": "pac-remote-1"}, &remoteTools); err != nil {
		t.Fatal(err)
	}
	hasTool := func(tools []proto.Tool, name string) bool {
		return slices.ContainsFunc(tools, func(tool proto.Tool) bool { return tool.Name == name })
	}
	if !hasTool(remoteTools.Tools, "memory_read") || !hasTool(remoteTools.Tools, "memory_write") {
		t.Fatalf("remote runtime missing memory tools: %+v", remoteTools.Tools)
	}

	if _, err := nodeCall(t, n, "assistant.send", map[string]any{"launcher": "dsh", "cwd": cwd, "text": "hello local"}); err != nil {
		t.Fatal(err)
	}
	var localTools struct {
		Tools []proto.Tool `json:"tools"`
	}
	if err := localHost.peer.Call(context.Background(), "mcp.tools", map[string]any{"assistant": true, "session_id": "dsh-local-1"}, &localTools); err != nil {
		t.Fatal(err)
	}
	if hasTool(localTools.Tools, "memory_read") || hasTool(localTools.Tools, "memory_write") {
		t.Fatalf("non-remote runtime should not receive memory tools: %+v", localTools.Tools)
	}

	callMemory := func(tool string, args map[string]any) proto.ToolResult {
		t.Helper()
		rawArgs, _ := json.Marshal(args)
		var res proto.ToolResult
		if err := remoteHost.peer.Call(context.Background(), "mcp.call", map[string]any{
			"name": tool, "args": json.RawMessage(rawArgs), "session_id": "pac-remote-1", "assistant": true,
		}, &res); err != nil {
			t.Fatalf("mcp.call %s: %v", tool, err)
		}
		return res
	}

	// Reading before the memory directory exists returns an empty note rather than an error, without creating the directory.
	if _, err := os.Stat(n.memoryDir(cwd)); !os.IsNotExist(err) {
		t.Fatalf("expected memory directory to not exist yet, stat err = %v", err)
	}
	if res := callMemory("memory_read", map[string]any{"name": "MEMORY.md"}); res.IsError || len(res.Content) != 1 || res.Content[0].Text != "" {
		t.Fatalf("expected empty note before memory dir exists, got: %+v", res)
	}
	if _, err := os.Stat(n.memoryDir(cwd)); !os.IsNotExist(err) {
		t.Fatalf("expected memory_read not to create directory, stat err = %v", err)
	}

	if res := callMemory("memory_write", map[string]any{"name": "MEMORY.md", "content": "# Notes\nhello\n"}); res.IsError {
		t.Fatalf("memory_write failed: %+v", res)
	}
	if res := callMemory("memory_read", map[string]any{"name": "MEMORY.md"}); res.IsError || len(res.Content) == 0 || res.Content[0].Text != "# Notes\nhello\n" {
		t.Fatalf("memory_read mismatch: %+v", res)
	}
	if res := callMemory("memory_read", map[string]any{"name": "missing.md"}); res.IsError || len(res.Content) != 1 || res.Content[0].Text != "" {
		t.Fatalf("expected empty note for absent file, got: %+v", res)
	}

	// Invalid names, unknown args, and oversized content must be rejected.
	for _, name := range []string{"../escape.md", "sub/file.md", "notes.txt", ""} {
		if res := callMemory("memory_read", map[string]any{"name": name}); !res.IsError {
			t.Fatalf("expected invalid filename %q to fail, got: %+v", name, res)
		}
	}
	if res := callMemory("memory_read", map[string]any{"name": "MEMORY.md", "extra": 1}); !res.IsError {
		t.Fatalf("expected unknown argument to fail: %+v", res)
	}
	if res := callMemory("memory_write", map[string]any{"name": "big.md", "content": strings.Repeat("a", 65*1024)}); !res.IsError {
		t.Fatalf("expected oversized write to fail: %+v", res)
	}

	// Symlinks and directories inside the memory directory must be refused.
	memDir := n.memoryDir(cwd)
	symPath := filepath.Join(memDir, "link.md")
	_ = os.WriteFile(filepath.Join(t.TempDir(), "outside.md"), []byte("secret"), 0o600)
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside.md"), symPath); err == nil {
		if res := callMemory("memory_read", map[string]any{"name": "link.md"}); !res.IsError {
			t.Fatalf("expected symlink read to fail: %+v", res)
		}
		if res := callMemory("memory_write", map[string]any{"name": "link.md", "content": "nope"}); !res.IsError {
			t.Fatalf("expected symlink write to fail: %+v", res)
		}
	}
	dirTarget := filepath.Join(memDir, "dir.md")
	if err := os.MkdirAll(dirTarget, 0o700); err != nil {
		t.Fatal(err)
	}
	if res := callMemory("memory_read", map[string]any{"name": "dir.md"}); !res.IsError || len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "regular file") {
		t.Fatalf("expected directory read to fail with regular file error: %+v", res)
	}
	if res := callMemory("memory_write", map[string]any{"name": "dir.md", "content": "nope"}); !res.IsError || len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "regular file") {
		t.Fatalf("expected directory write to fail with regular file error: %+v", res)
	}

	// Plan mode permits memory_read and refuses memory_write.
	n.noteModes("pac-remote-1", proto.ModePlan, "")
	if res := callMemory("memory_write", map[string]any{"name": "plan.md", "content": "denied"}); !res.IsError {
		t.Fatalf("expected memory_write in plan mode to fail: %+v", res)
	}
	if res := callMemory("memory_read", map[string]any{"name": "MEMORY.md"}); res.IsError {
		t.Fatalf("expected memory_read in plan mode to succeed: %+v", res)
	}

	// Guest sessions neither see nor can invoke memory tools.
	n.noteModes("pac-remote-1", "default", "")
	st, _ := n.state.get("pac-remote-1")
	st.Guest = []string{"read_only"}
	n.state.put("pac-remote-1", st)

	var guestTools struct {
		Tools []proto.Tool `json:"tools"`
	}
	if err := remoteHost.peer.Call(context.Background(), "mcp.tools", map[string]any{"assistant": true, "session_id": "pac-remote-1"}, &guestTools); err != nil {
		t.Fatal(err)
	}
	if hasTool(guestTools.Tools, "memory_read") || hasTool(guestTools.Tools, "memory_write") {
		t.Fatalf("guest session should not see memory tools: %+v", guestTools.Tools)
	}
	if res := callMemory("memory_read", map[string]any{"name": "MEMORY.md"}); !res.IsError {
		t.Fatalf("expected guest memory_read to fail: %+v", res)
	}
	if res := callMemory("memory_write", map[string]any{"name": "MEMORY.md", "content": "x"}); !res.IsError {
		t.Fatalf("expected guest memory_write to fail: %+v", res)
	}
}

func TestRemoteAgentGateWriteRequiresConfirmEvenInBypass(t *testing.T) {
	needsLarkGate(t)
	n, hub := runtimeNode(t)
	assistantHub(hub)

	n.mu.Lock()
	n.larkCfg = olCfg()
	n.larkCfgAt = time.Now().Add(time.Hour)
	n.larkCfg.CLI = &proto.LarkCLI{Bin: "/nonexistent/lark-cli"}
	n.larkRisks = map[string]string{"\x00docs +create": "write"}
	n.mu.Unlock()

	hub.mu.Lock()
	hub.resolve = func(proto.Interaction) proto.Resolution {
		no := false
		return proto.Resolution{Allow: &no}
	}
	hub.mu.Unlock()

	remoteHost := dialRuntime(t, n, map[string]any{
		"runtime": "pac", "name": "PAC", "launcher": "pac",
		"caps": map[string]any{"start": true, "send": true, "interrupt": true, "assistant": true, "remote_agent": true},
	})
	localHost := dialRuntime(t, n, assistantRuntimeHello())

	cwd := identity.AssistantDir(n.home)
	remoteHost.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": "pac-bypass-1", "cwd": cwd, "status": "idle"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	localHost.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": "dsh-bypass-1", "cwd": cwd, "status": "idle"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}

	if _, err := nodeCall(t, n, "assistant.send", map[string]any{"launcher": "pac", "cwd": cwd, "text": "init remote", "permission_mode": proto.ModeBypass}); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{"launcher": "dsh", "cwd": cwd, "text": "init local", "permission_mode": proto.ModeBypass}); err != nil {
		t.Fatal(err)
	}
	n.noteModes("pac-bypass-1", proto.ModeBypass, "")
	n.noteModes("dsh-bypass-1", proto.ModeBypass, "")

	const cmd = `lark-cli docs +create --title notes --markdown hi`

	// Non-remote assistant session in bypassPermissions executes ordinary write without a card.
	decLocal, _, err := n.larkCheck(context.Background(), "dsh-bypass-1", cwd, cmd,
		proto.Interaction{SessionID: "dsh-bypass-1", Cwd: cwd, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("local larkCheck: %v", err)
	}
	if decLocal == "deny" {
		t.Fatalf("expected non-remote bypass session to allow docs +create without denial")
	}
	hub.mu.Lock()
	localCards := len(hub.asked)
	hub.mu.Unlock()
	if localCards != 0 {
		t.Fatalf("expected 0 confirmation cards for non-remote bypass session, got %d", localCards)
	}

	// Remote runtime session in bypassPermissions still raises a confirmation card with no AlwaysKeys.
	decRemote, _, err := n.larkCheck(context.Background(), "pac-bypass-1", cwd, cmd,
		proto.Interaction{SessionID: "pac-bypass-1", Cwd: cwd, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatalf("remote larkCheck: %v", err)
	}
	if decRemote != "deny" {
		t.Fatalf("expected remote larkCheck to confirm (and deny on fake hub's allow=false), got %q", decRemote)
	}
	hub.mu.Lock()
	asked := len(hub.asked)
	var firstAlways []string
	if asked > 0 {
		firstAlways = append([]string(nil), hub.asked[0].AlwaysKeys...)
	}
	hub.mu.Unlock()
	if asked != 1 {
		t.Fatalf("expected 1 confirmation card for remote bypass session, got %d", asked)
	}
	if len(firstAlways) != 0 {
		t.Fatalf("expected AlwaysKeys to be empty on remote confirm card, got %v", firstAlways)
	}

	// Configured allow rules still allow reads for remote sessions, but non-read writes must still confirm.
	n.mu.Lock()
	n.larkCfg.Gates[0].Rules = []proto.LarkRule{
		{Method: "GET", Path: "/me", Action: "allow"},
		{Method: "POST", Path: "/events", Action: "allow"},
	}
	n.mu.Unlock()

	decRead, _, err := n.larkCheck(context.Background(), "pac-bypass-1", cwd, `ex GET /me`,
		proto.Interaction{SessionID: "pac-bypass-1", Cwd: cwd, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil || decRead != "allow" {
		t.Fatalf("remote allowed read: dec=%q err=%v, want allow", decRead, err)
	}
	decRuleWrite, _, err := n.larkCheck(context.Background(), "pac-bypass-1", cwd, `ex POST /events '{}'`,
		proto.Interaction{SessionID: "pac-bypass-1", Cwd: cwd, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil || decRuleWrite != "deny" {
		t.Fatalf("remote rule-allowed write must still confirm (denied by fake hub): dec=%q err=%v", decRuleWrite, err)
	}

	// Approving a rule-matched write with Always=true must still confirm on subsequent calls and never populate AlwaysKeys.
	hub.mu.Lock()
	hub.resolve = func(proto.Interaction) proto.Resolution {
		yes := true
		return proto.Resolution{Allow: &yes, Always: true}
	}
	hub.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		dec, _, err := n.larkCheck(context.Background(), "pac-bypass-1", cwd, `ex POST /events '{}'`,
			proto.Interaction{SessionID: "pac-bypass-1", Cwd: cwd, ExpiresAt: time.Now().Add(time.Minute)})
		if err != nil || dec != "allow" {
			t.Fatalf("remote rule-matched write attempt %d: dec=%q err=%v", attempt+1, dec, err)
		}
	}
	hub.mu.Lock()
	if len(hub.asked) != 4 {
		hub.mu.Unlock()
		t.Fatalf("expected 4 cards before non-assistant checks, got %d", len(hub.asked))
	}
	for i := 1; i < 4; i++ {
		if len(hub.asked[i].AlwaysKeys) != 0 {
			hub.mu.Unlock()
			t.Fatalf("expected AlwaysKeys empty on card %d, got %v", i, hub.asked[i].AlwaysKeys)
		}
	}
	hub.mu.Unlock()

	// Remote non-assistant sessions also ignore larkAlways and require fresh confirmation on writes,
	// whereas a local non-assistant control session honors larkAlways.
	nonAssistCwd := t.TempDir()
	if err := remoteHost.peer.Notify("runtime.sessions", map[string]any{
		"sessions": []map[string]any{{"session_id": "pac-nonassist-1", "cwd": nonAssistCwd, "status": "idle"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := localHost.peer.Notify("runtime.sessions", map[string]any{
		"sessions": []map[string]any{{"session_id": "dsh-nonassist-1", "cwd": nonAssistCwd, "status": "idle"}},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "non-assistant runtime sessions owned", func() bool {
		_, _, remoteOK := n.runtimes.rowOf("pac-nonassist-1")
		_, _, localOK := n.runtimes.rowOf("dsh-nonassist-1")
		return remoteOK && localOK
	})
	n.rememberLarkAlways("pac-nonassist-1", "CLI /docs/+create")
	n.rememberLarkAlways("dsh-nonassist-1", "CLI /docs/+create")
	decLocalAlways, _, err := n.larkCheck(context.Background(), "dsh-nonassist-1", nonAssistCwd, cmd,
		proto.Interaction{SessionID: "dsh-nonassist-1", Cwd: nonAssistCwd, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil || decLocalAlways != "allow" {
		t.Fatalf("local non-assistant write with larkAlways: dec=%q err=%v, want allow", decLocalAlways, err)
	}
	hub.mu.Lock()
	afterLocalAlways := len(hub.asked)
	hub.mu.Unlock()
	if afterLocalAlways != 4 {
		t.Fatalf("expected local non-assistant larkAlways to suppress card, got %d cards", afterLocalAlways)
	}
	decAlways, _, err := n.larkCheck(context.Background(), "pac-nonassist-1", nonAssistCwd, cmd,
		proto.Interaction{SessionID: "pac-nonassist-1", Cwd: nonAssistCwd, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil || decAlways != "allow" {
		t.Fatalf("remote non-assistant write must still confirm (allowed by hub): dec=%q err=%v", decAlways, err)
	}
	hub.mu.Lock()
	totalCards := len(hub.asked)
	var lastAlways []string
	if totalCards > 0 {
		lastAlways = append([]string(nil), hub.asked[totalCards-1].AlwaysKeys...)
	}
	hub.mu.Unlock()
	if totalCards != 5 {
		t.Fatalf("expected 5 confirmation cards total, got %d", totalCards)
	}
	if len(lastAlways) != 0 {
		t.Fatalf("expected AlwaysKeys empty on remote non-assistant confirm card, got %v", lastAlways)
	}
}

func TestRemoteAgentMemoryToolsSymlinkDirAndWorkspaceScoping(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)

	remoteHost := dialRuntime(t, n, map[string]any{
		"runtime": "pac", "name": "PAC", "launcher": "pac",
		"caps": map[string]any{"start": true, "send": true, "interrupt": true, "assistant": true, "remote_agent": true},
	})

	assistantDir := identity.AssistantDir(n.home)
	nonAssistantDir := t.TempDir()

	// 1. Symlinked memory directory (~/.claude/projects/<key>/memory -> dotfiles) works, while symlinked note targets are still rejected.
	realMemDir := t.TempDir()
	memLink := n.memoryDir(assistantDir)
	if err := os.MkdirAll(filepath.Dir(memLink), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realMemDir, memLink); err == nil {
		remoteHost.answer = func(method string, _ json.RawMessage) (any, error) {
			if method == "session.start" {
				return map[string]any{"session": map[string]any{"session_id": "pac-sym-1", "cwd": assistantDir, "status": "idle"}}, nil
			}
			return map[string]any{"ok": true}, nil
		}
		if _, err := nodeCall(t, n, "assistant.send", map[string]any{"launcher": "pac", "cwd": assistantDir, "text": "hello symlink"}); err != nil {
			t.Fatal(err)
		}
		rawWrite, _ := json.Marshal(map[string]any{"name": "MEMORY.md", "content": "# Symlinked\nworks\n"})
		var wRes proto.ToolResult
		if err := remoteHost.peer.Call(context.Background(), "mcp.call", map[string]any{
			"name": "memory_write", "args": json.RawMessage(rawWrite), "session_id": "pac-sym-1", "assistant": true,
		}, &wRes); err != nil {
			t.Fatal(err)
		}
		if wRes.IsError {
			t.Fatalf("memory_write through symlinked memory dir failed: %+v", wRes)
		}
		rawRead, _ := json.Marshal(map[string]any{"name": "MEMORY.md"})
		var rRes proto.ToolResult
		if err := remoteHost.peer.Call(context.Background(), "mcp.call", map[string]any{
			"name": "memory_read", "args": json.RawMessage(rawRead), "session_id": "pac-sym-1", "assistant": true,
		}, &rRes); err != nil {
			t.Fatal(err)
		}
		if rRes.IsError || len(rRes.Content) == 0 || rRes.Content[0].Text != "# Symlinked\nworks\n" {
			t.Fatalf("memory_read through symlinked memory dir mismatch: %+v", rRes)
		}
		outside := filepath.Join(t.TempDir(), "outside.md")
		_ = os.WriteFile(outside, []byte("secret"), 0o600)
		if err := os.Symlink(outside, filepath.Join(realMemDir, "escape.md")); err == nil {
			badArgs, _ := json.Marshal(map[string]any{"name": "escape.md", "content": "nope"})
			var badWrite proto.ToolResult
			if err := remoteHost.peer.Call(context.Background(), "mcp.call", map[string]any{
				"name": "memory_write", "args": json.RawMessage(badArgs), "session_id": "pac-sym-1", "assistant": true,
			}, &badWrite); err != nil {
				t.Fatal(err)
			}
			if !badWrite.IsError {
				t.Fatalf("expected symlinked note file write inside symlinked memory dir to fail: %+v", badWrite)
			}
		}
	}

	hasTool := func(tools []proto.Tool, name string) bool {
		return slices.ContainsFunc(tools, func(tool proto.Tool) bool { return tool.Name == name })
	}

	// 2. Non-assistant workspace: neither instructions nor mcp.tools advertise memory tools, and mcp.call rejects them.
	remoteHost.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": "pac-track-1", "cwd": nonAssistantDir, "status": "idle"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	beforeSends := len(remoteHost.got("session.send"))
	if _, err := nodeCall(t, n, "assistant.send", map[string]any{"launcher": "pac", "cwd": nonAssistantDir, "text": "hello track"}); err != nil {
		t.Fatal(err)
	}
	sends := remoteHost.got("session.send")
	if len(sends) != beforeSends+1 {
		t.Fatalf("expected new session.send, got %d", len(sends))
	}
	if strings.Contains(string(sends[len(sends)-1]), "memory_read") || strings.Contains(string(sends[len(sends)-1]), "memory_write") {
		t.Fatalf("non-assistant session instructions unexpectedly mentioned memory tools: %s", sends[len(sends)-1])
	}
	var trackTools struct {
		Tools []proto.Tool `json:"tools"`
	}
	if err := remoteHost.peer.Call(context.Background(), "mcp.tools", map[string]any{"assistant": true, "session_id": "pac-track-1"}, &trackTools); err != nil {
		t.Fatal(err)
	}
	if hasTool(trackTools.Tools, "memory_read") || hasTool(trackTools.Tools, "memory_write") {
		t.Fatalf("non-assistant session should not be advertised memory tools: %+v", trackTools.Tools)
	}
	rawRead, _ := json.Marshal(map[string]any{"name": "MEMORY.md"})
	var trackCall proto.ToolResult
	if err := remoteHost.peer.Call(context.Background(), "mcp.call", map[string]any{
		"name": "memory_read", "args": json.RawMessage(rawRead), "session_id": "pac-track-1", "assistant": true,
	}, &trackCall); err != nil {
		t.Fatal(err)
	}
	if !trackCall.IsError {
		t.Fatalf("expected non-assistant memory_read call to fail closed: %+v", trackCall)
	}

	// 3. Session with no recorded cwd fails closed instead of falling back to assistantDir.
	if err := remoteHost.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": "pac-empty-cwd", "status": "idle"}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "empty-cwd runtime session owned", func() bool {
		_, _, ok := n.runtimes.rowOf("pac-empty-cwd")
		return ok
	})
	var emptyTools struct {
		Tools []proto.Tool `json:"tools"`
	}
	if err := remoteHost.peer.Call(context.Background(), "mcp.tools", map[string]any{"assistant": true, "session_id": "pac-empty-cwd"}, &emptyTools); err != nil {
		t.Fatal(err)
	}
	if hasTool(emptyTools.Tools, "memory_read") || hasTool(emptyTools.Tools, "memory_write") {
		t.Fatalf("session with empty cwd should not be advertised memory tools: %+v", emptyTools.Tools)
	}
	res := n.assistantMemoryTool(context.Background(), "memory_read", "pac-empty-cwd", rawRead)
	if !res.IsError || len(res.Content) == 0 || !strings.Contains(res.Content[0].Text, "assistant workspace") {
		t.Fatalf("expected empty cwd to fail closed without falling back to assistant dir, got: %+v", res)
	}
}

func TestRemoteAgentConfirmWritesPersistsAcrossDisconnectOwnershipChangeAndRestart(t *testing.T) {
	t.Setenv("PERSONA_SKILLS_DIR", t.TempDir())
	t.Setenv("PERSONA_PLUGIN_SKILLS_DIR", t.TempDir())
	api := newFakeAPI(t)

	setupNode := func() (*Node, *runtimeHub, string) {
		n, hub := runtimeNode(t)
		assistantHub(hub)
		n.dir = filepath.Join(n.home, ".persona")
		allowWrite := proto.LarkRule{Method: "POST", Path: "/events", Action: "allow"}
		n.mu.Lock()
		n.larkCfg = proto.AssistantLark{Gates: []proto.CommandGate{proxiedGate(api.URL, allowWrite)}}
		n.larkCfgAt = time.Now().Add(time.Hour)
		n.larkCfg.CLI = &proto.LarkCLI{Bin: "/nonexistent/lark-cli"}
		n.larkRisks = map[string]string{"\x00docs +create": "write"}
		n.mu.Unlock()
		hub.mu.Lock()
		hub.resolve = func(proto.Interaction) proto.Resolution {
			no := false
			return proto.Resolution{Allow: &no}
		}
		hub.mu.Unlock()
		cwd := identity.AssistantDir(n.home)
		skillDir := filepath.Join(cwd, ".claude", "skills", "example", "scripts")
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "ex.sh"), []byte("#!/bin/bash\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { n.gates().close() })
		return n, hub, cwd
	}

	n, hub, cwd := setupNode()

	remoteHost := dialRuntime(t, n, map[string]any{
		"runtime": "pac", "name": "PAC", "launcher": "pac",
		"caps": map[string]any{"start": true, "send": true, "interrupt": true, "assistant": true, "remote_agent": true},
	})
	localHost := dialRuntime(t, n, assistantRuntimeHello())

	remoteHost.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": "pac-bypass-1", "cwd": cwd, "status": "idle"}}, nil
		}
		return map[string]any{"ok": true}, nil
	}

	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"launcher": "pac", "cwd": cwd, "text": "init remote", "permission_mode": proto.ModeBypass,
	}); err != nil {
		t.Fatal(err)
	}
	n.noteModes("pac-bypass-1", proto.ModeBypass, "")

	nonAssistCwd := t.TempDir()
	if err := remoteHost.peer.Notify("runtime.sessions", map[string]any{
		"sessions": []map[string]any{{"session_id": "pac-nonassist-1", "cwd": nonAssistCwd, "status": "idle"}},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "remote non-assistant owned", func() bool {
		_, _, ok := n.runtimes.rowOf("pac-nonassist-1")
		return ok
	})
	n.rememberLarkAlways("pac-nonassist-1", "CLI /docs/+create")

	env, release := n.assistantLarkEnv(cwd, "pac-bypass-1")
	t.Cleanup(release)
	proxyBase, ok := envOf(env, "EXAMPLE_BASE")
	if !ok {
		t.Fatalf("missing EXAMPLE_BASE in env: %v", env)
	}

	const bypassCmd = `lark-cli docs +create --title notes --markdown hi`
	const ruleWriteCmd = `ex POST /events '{}'`

	assertPosture := func(phase string, targetNode *Node, targetHub *runtimeHub, baseURL string) {
		t.Helper()
		if !targetNode.isRemoteRuntimeSession("pac-bypass-1") {
			t.Fatalf("%s: expected pac-bypass-1 to retain remote posture", phase)
		}
		if decBypass, _, err := targetNode.larkCheck(context.Background(), "pac-bypass-1", cwd, bypassCmd,
			proto.Interaction{SessionID: "pac-bypass-1", Cwd: cwd, ExpiresAt: time.Now().Add(time.Minute)}); err != nil || decBypass != "deny" {
			t.Fatalf("%s: expected bypass write to require confirmation (denied by hub): dec=%q err=%v", phase, decBypass, err)
		}
		targetHub.mu.Lock()
		before := len(targetHub.asked)
		targetHub.mu.Unlock()
		decRule, _, err := targetNode.larkCheck(context.Background(), "pac-bypass-1", cwd, ruleWriteCmd,
			proto.Interaction{SessionID: "pac-bypass-1", Cwd: cwd, ExpiresAt: time.Now().Add(time.Minute)})
		if err != nil || decRule != "deny" {
			t.Fatalf("%s: expected rule-matched write to require confirmation (denied by hub): dec=%q err=%v", phase, decRule, err)
		}
		targetHub.mu.Lock()
		after := len(targetHub.asked)
		var lastAlways []string
		if after > 0 {
			lastAlways = append([]string(nil), targetHub.asked[after-1].AlwaysKeys...)
		}
		targetHub.mu.Unlock()
		if after != before+1 || len(lastAlways) != 0 {
			t.Fatalf("%s: expected fresh card with empty AlwaysKeys (%d -> %d), got AlwaysKeys=%v", phase, before, after, lastAlways)
		}
		beforeAPI := len(api.requests())
		code, _ := send(t, http.MethodPost, baseURL+"/api/v1/events", `{"k":1}`)
		if code != http.StatusForbidden {
			t.Fatalf("%s: gateProxy allowed rule-matched write without card: HTTP %d", phase, code)
		}
		if len(api.requests()) != beforeAPI {
			t.Fatalf("%s: gateProxy forwarded unconfirmed write to upstream API", phase)
		}
	}

	// 1. Disconnect the remote runtime: belongs() now returns (nil, label, true).
	_ = remoteHost.conn.Close()
	waitFor(t, "remote runtime disconnected", func() bool {
		conn, _, known := n.runtimes.belongs("pac-bypass-1")
		return conn == nil && known
	})
	assertPosture("after disconnect", n, hub, proxyBase)
	hub.mu.Lock()
	cardsBeforeNonAssist := len(hub.asked)
	hub.mu.Unlock()
	if dec, _, err := n.larkCheck(context.Background(), "pac-nonassist-1", nonAssistCwd, bypassCmd,
		proto.Interaction{SessionID: "pac-nonassist-1", Cwd: nonAssistCwd, ExpiresAt: time.Now().Add(time.Minute)}); err != nil || dec != "deny" {
		t.Fatalf("after disconnect non-assistant write must still confirm: dec=%q err=%v", dec, err)
	}
	hub.mu.Lock()
	if len(hub.asked) != cardsBeforeNonAssist+1 {
		hub.mu.Unlock()
		t.Fatalf("after disconnect expected confirmation card for pac-nonassist-1")
	}
	hub.mu.Unlock()

	// 2. A non-remote runtime claims/lists the same session IDs: confirm-writes remains latched.
	if err := localHost.peer.Notify("runtime.sessions", map[string]any{
		"sessions": []map[string]any{
			{"session_id": "pac-bypass-1", "cwd": cwd, "status": "idle"},
			{"session_id": "pac-nonassist-1", "cwd": nonAssistCwd, "status": "idle"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "local runtime owns sessions", func() bool {
		c1, _, _ := n.runtimes.belongs("pac-bypass-1")
		c2, _, _ := n.runtimes.belongs("pac-nonassist-1")
		return c1 != nil && c1.id() == "dsh" && !c1.remoteAgent() && c2 != nil && c2.id() == "dsh" && !c2.remoteAgent()
	})
	assertPosture("after non-remote takeover", n, hub, proxyBase)
	hub.mu.Lock()
	cardsBeforeLocalNonAssist := len(hub.asked)
	hub.mu.Unlock()
	if dec, _, err := n.larkCheck(context.Background(), "pac-nonassist-1", nonAssistCwd, bypassCmd,
		proto.Interaction{SessionID: "pac-nonassist-1", Cwd: nonAssistCwd, ExpiresAt: time.Now().Add(time.Minute)}); err != nil || dec != "deny" {
		t.Fatalf("after non-remote takeover non-assistant write must still confirm: dec=%q err=%v", dec, err)
	}
	hub.mu.Lock()
	if len(hub.asked) != cardsBeforeLocalNonAssist+1 {
		hub.mu.Unlock()
		t.Fatalf("after non-remote takeover expected confirmation card for pac-nonassist-1")
	}
	hub.mu.Unlock()

	// 3. stateFile.put preserves the RemoteAgent latch even if a subsequent write omits it.
	n.state.put("pac-bypass-1", sessionState{Launcher: "pac", Cwd: cwd})
	if st, ok := n.state.get("pac-bypass-1"); !ok || !st.RemoteAgent {
		t.Fatalf("expected RemoteAgent latch to survive stateFile.put, got %+v (ok=%v)", st, ok)
	}

	// 4. Fresh Node (simulating restart) loading persisted state enforces confirm-writes before any runtime connects.
	n2, hub2, cwd2 := setupNode()
	n2.state.merge(map[string]sessionState{
		"pac-bypass-1": {Launcher: "pac", Cwd: cwd2, RemoteAgent: true, AppMode: proto.ModeBypass},
	})
	n2.noteModes("pac-bypass-1", proto.ModeBypass, "")
	env2, release2 := n2.assistantLarkEnv(cwd2, "pac-bypass-1")
	t.Cleanup(release2)
	proxyBase2, ok := envOf(env2, "EXAMPLE_BASE")
	if !ok {
		t.Fatalf("missing EXAMPLE_BASE on restarted node: %v", env2)
	}
	cwd = cwd2
	assertPosture("after node restart", n2, hub2, proxyBase2)
}

func TestAgyAssistantDegradedAutoModePersistsAndReturnsSettledMode(t *testing.T) {
	t.Run("runtime degrades auto on start and session.modes", func(t *testing.T) {
		n, hub := runtimeNode(t)
		assistantHub(hub)
		hello := assistantRuntimeHello()
		hello["runtime"], hello["name"], hello["launcher"], hello["agent"] = "agy", "Antigravity", "agy", "agy"
		hello["caps"].(map[string]any)["modes"] = true
		host := dialRuntime(t, n, hello)
		cwd := identity.AssistantDir(n.home)
		host.answer = func(method string, _ json.RawMessage) (any, error) {
			switch method {
			case "session.start":
				return map[string]any{"session": map[string]any{
					"session_id": "agy-degraded-1", "cwd": cwd, "status": "idle", "agent": "agy",
					"permission_mode": proto.ModeBypass,
					"capabilities": map[string]any{
						"rewind":           false,
						"permission_modes": []string{proto.ModeDefault, "acceptEdits", proto.ModePlan, proto.ModeBypass},
					},
				}}, nil
			case "session.modes":
				return map[string]any{"permission_mode": proto.ModeBypass}, nil
			default:
				return map[string]any{"ok": true}, nil
			}
		}

		raw, err := nodeCall(t, n, "assistant.send", map[string]any{
			"cwd": cwd, "launcher": "agy", "text": "hello",
			"permission_mode": "auto", "permission_mode_explicit": true,
		})
		if err != nil {
			t.Fatalf("assistant.send: %v", err)
		}
		var res struct {
			SessionID      string `json:"session_id"`
			PermissionMode string `json:"permission_mode"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatal(err)
		}
		if res.SessionID != "agy-degraded-1" || res.PermissionMode != proto.ModeBypass {
			t.Fatalf("assistant.send response = %s, want permission_mode=%q", raw, proto.ModeBypass)
		}
		sends := host.got("session.send")
		if len(sends) != 1 {
			t.Fatalf("session.send calls = %d, want 1", len(sends))
		}
		var sent struct {
			Mode string `json:"mode"`
		}
		_ = json.Unmarshal(sends[0], &sent)
		if sent.Mode != proto.ModeBypass {
			t.Fatalf("session.send mode = %q, want %q; raw=%s", sent.Mode, proto.ModeBypass, sends[0])
		}
		if got := n.assistantModeOf("agy-degraded-1"); got != proto.ModeBypass {
			t.Fatalf("assistantModeOf = %q, want %q", got, proto.ModeBypass)
		}
		if st, _ := n.state.get("agy-degraded-1"); st.AppMode != proto.ModeBypass {
			t.Fatalf("persisted AppMode = %q, want %q", st.AppMode, proto.ModeBypass)
		}

		if _, err := nodeCall(t, n, "session.modes", map[string]any{
			"session_id": "agy-degraded-1", "permission_mode": "auto",
		}); err != nil {
			t.Fatalf("session.modes: %v", err)
		}
		if got := n.assistantModeOf("agy-degraded-1"); got != proto.ModeBypass {
			t.Fatalf("after session.modes assistantModeOf = %q, want %q", got, proto.ModeBypass)
		}
		if st, _ := n.state.get("agy-degraded-1"); st.AppMode != proto.ModeBypass {
			t.Fatalf("after session.modes persisted AppMode = %q, want %q", st.AppMode, proto.ModeBypass)
		}
	})

	t.Run("rolling upgrade fallback when older agy runtime rejects auto on start", func(t *testing.T) {
		n, hub := runtimeNode(t)
		assistantHub(hub)
		hello := assistantRuntimeHello()
		hello["runtime"], hello["name"], hello["launcher"], hello["agent"] = "agy", "Antigravity", "agy", "agy"
		hello["caps"].(map[string]any)["modes"] = true
		host := dialRuntime(t, n, hello)
		cwd := identity.AssistantDir(n.home)
		host.answer = func(method string, params json.RawMessage) (any, error) {
			if method == "session.start" {
				var p struct {
					PermissionMode string `json:"permission_mode"`
				}
				_ = json.Unmarshal(params, &p)
				if p.PermissionMode == "auto" {
					return nil, fmt.Errorf("Auto mode is not available: it needs the OS terminal sandbox and may not be rolled out to you yet")
				}
				return map[string]any{"session": map[string]any{
					"session_id": "agy-legacy-1", "cwd": cwd, "status": "idle", "agent": "agy",
					"permission_mode": p.PermissionMode,
				}}, nil
			}
			return map[string]any{"ok": true}, nil
		}

		raw, err := nodeCall(t, n, "assistant.send", map[string]any{
			"cwd": cwd, "launcher": "agy", "text": "hello",
			"permission_mode": "auto", "permission_mode_explicit": true,
		})
		if err != nil {
			t.Fatalf("assistant.send legacy fallback failed: %v", err)
		}
		var res struct {
			SessionID      string `json:"session_id"`
			PermissionMode string `json:"permission_mode"`
		}
		_ = json.Unmarshal(raw, &res)
		if res.SessionID != "agy-legacy-1" || res.PermissionMode != proto.ModeBypass {
			t.Fatalf("legacy fallback response = %s, want permission_mode=%q", raw, proto.ModeBypass)
		}
	})

	t.Run("agyAutoModeUnavailable tracks runtime launcher permission_modes", func(t *testing.T) {
		n, _ := runtimeNode(t)
		hello := assistantRuntimeHello()
		hello["runtime"], hello["name"], hello["launcher"], hello["agent"] = "agy", "Antigravity", "agy", "agy"
		hello["launchers"] = []map[string]any{{"name": "agy", "agent": "agy"}}
		h := dialRuntime(t, n, hello)
		if n.agyAutoModeUnavailable("agy") {
			t.Fatal("unrestricted agy launcher reported auto unavailable")
		}
		if err := h.peer.Notify("runtime.launchers", map[string]any{"launchers": []map[string]any{
			{"name": "agy", "agent": "agy", "permission_modes": []string{proto.ModeDefault, "acceptEdits", proto.ModePlan, proto.ModeBypass}},
		}}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "restricted agy launcher recorded", func() bool { return n.agyAutoModeUnavailable("agy") })
	})
}
