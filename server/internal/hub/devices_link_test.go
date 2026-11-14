package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The computers are the manager's (devices_link.go): a hub whose manager connects its machine; the apps' lists, calls,
// pushes and streams go through the manager as before; while the manager is gone the machine is offline and its routes
// answer 503 assistant_offline; a new manager connects it again by itself.
func TestMachinesAreTheManagers(t *testing.T) {
	box := newMeshNode("uid-box", "box", "s1")
	box.resident = true
	mh := startTestHub(t, hubA, "Dev", []MachineConfig{{ID: "box", Name: "Box", Kind: "ssh"}}, []*meshNode{box})
	tok, _ := mh.deviceToken(t, roleFull)
	eventually(t, 10*time.Second, "box online through the manager", func() bool {
		m := mh.machines(t, tok)["box"]
		return m.Online && m.Route != nil && m.Route.Via == "ssh" && m.HubID == hubA
	})
	if m := mh.h.machineByID("box"); m == nil || !m.managed {
		t.Fatal("the hub's machine is not the manager's")
	}

	// The sessions (listed right after it connected), an item page, a send.
	var code int
	var b []byte
	eventually(t, 5*time.Second, "its sessions", func() bool {
		code, b = mh.do(t, "GET", "/api/v1/sessions", tok, "")
		return code == 200 && strings.Contains(string(b), `"session_id":"s1"`)
	})
	code, b = mh.do(t, "GET", "/api/v1/sessions/box/s1/items", tok, "")
	if code != 200 || !strings.Contains(string(b), "hi from s1") {
		t.Fatalf("items: %d %s", code, b)
	}
	code, b = mh.do(t, "POST", "/api/v1/sessions/box/s1/messages", tok, `{"text":"经过助手","client_msg_id":"m-1"}`)
	if code != 200 || !strings.Contains(strings.Join(box.sentTexts(), ","), "经过助手") {
		t.Fatalf("send: %d %s, the node got %v", code, b, box.sentTexts())
	}

	// The node's events reach a subscribed app.
	a := dialApp(t, mh, tok)
	a.send(t, map[string]any{"type": "subscribe", "machine_id": "box", "session_id": "s1"})
	a.wait(t, "the backlog", 0, hasItem("hi from s1"))
	box.addItem("s1", "live through the manager")
	a.wait(t, "a live item", 0, hasItem("live through the manager"))

	// A forwarded port: bytes both ways through the hub, the manager and the node.
	echo := echoServer(t)
	code, b = mh.do(t, "POST", "/api/v1/machines/box/ports/"+strconv.Itoa(echo)+"/open", tok, "")
	if code != 200 {
		t.Fatalf("open port: %d %s", code, b)
	}
	if got := portEcho(t, mh, tok, echo, "ping through the manager"); got != "ping through the manager" {
		t.Fatalf("port echo %q", got)
	}

	// A terminal: the app's keys reach the node, the node's frames the app (the node here echoes each key as a frame).
	box.mu.Lock()
	box.onEvent = func(c *meshNodeConn, event string, params json.RawMessage) {
		if event != "terminal.input" {
			return
		}
		var in struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(params, &in)
		go func() {
			_ = c.p.Notify("terminal.frame", map[string]any{"session_id": "s1", "epoch": "e1", "seq": 1, "full": true, "cols": 80,
				"rows": 24, "data": in.Text})
		}()
	}
	box.mu.Unlock()
	a.send(t, map[string]any{"type": "terminal_subscribe", "machine_id": "box", "session_id": "s1", "control": map[string]any{"cols": 80, "rows": 24}})
	a.wait(t, "the app drives the terminal", 0, func(m map[string]any) bool { return m["type"] == "terminal_control" && m["state"] == "active" })
	for i := range 3 {
		key := "k" + strconv.Itoa(i)
		a.send(t, map[string]any{"type": "terminal_input", "machine_id": "box", "session_id": "s1", "text": key})
		a.wait(t, "the echo of "+key, 0, func(m map[string]any) bool { return m["type"] == "terminal" && m["data"] == key })
	}

	// The manager goes: offline, saying why; its routes 503 assistant_offline; nothing reaches the node meanwhile.
	mh.mgr.stop()
	eventually(t, 10*time.Second, "box offline without its manager", func() bool {
		m := mh.machines(t, tok)["box"]
		return !m.Online && m.Error == errAssistantOffline.Error()
	})
	a.wait(t, "the terminal ends, saying why", 0, func(m map[string]any) bool {
		return m["type"] == "terminal" && m["error"] == errAssistantOffline.Error()
	})
	for _, path := range []string{"/api/v1/sessions/box/s1/items", "/api/v1/machines/box/fs?path=/"} {
		code, b = mh.do(t, "GET", path, tok, "")
		var e struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(b, &e)
		if code != http.StatusServiceUnavailable || e.Code != "assistant_offline" {
			t.Fatalf("%s without the manager: %d %s", path, code, b)
		}
	}
	code, b = mh.do(t, "POST", "/api/v1/machines/box/reconnect", tok, "")
	if code != http.StatusServiceUnavailable || !strings.Contains(string(b), "assistant_offline") {
		t.Fatalf("a route of the manager's without it: %d %s", code, b)
	}
	code, b = mh.do(t, "GET", "/api/v1/sessions", tok, "")
	if code != 200 || !strings.Contains(string(b), `"status":"unknown"`) {
		t.Fatalf("the sessions as last known: %d %s", code, b)
	}

	// A new manager connects it again, nobody doing anything.
	mh.mgr = startTestManager(t, mh.h, mh.srv, map[string]func(context.Context) (net.Conn, error){"box": box.dialer(mh.h)})
	eventually(t, 10*time.Second, "box back with the new manager", func() bool { return mh.machines(t, tok)["box"].Online })
	code, b = mh.do(t, "GET", "/api/v1/sessions/box/s1/items", tok, "")
	if code != 200 || !strings.Contains(string(b), "hi from s1") {
		t.Fatalf("items again: %d %s", code, b)
	}
	// The app watching the session still gets its items: the hub asked the new manager to watch it again.
	box.addItem("s1", "after the manager came back")
	a.wait(t, "a live item after the manager came back", 0, hasItem("after the manager came back"))
}

// A hub that just restarted shows the machines as the manager handed them over, until the manager reports; a call
// meanwhile waits for it instead of failing.
func TestTheHubShowsTheHandOverUntilTheManagerReports(t *testing.T) {
	h := newTestHub(t)
	st := newMeshStore()
	h.store = st
	h.cfg.Machines = []MachineConfig{{ID: "box", Name: "Box", Kind: "ssh"}}
	box := newMeshNode("uid-box", "box", "s1")
	box.resident = true
	_ = st.PutSetting(context.Background(), devicesWarmKey, map[string]any{"saved_at": time.Now(), "machines": map[string]any{
		"box": map[string]any{"info": proto.NodeInfo{Hostname: "box", Version: "test"}, "sessions": []proto.Session{{SessionID: "s1", Title: "旧的", Status: proto.StatusIdle}}}},
		"engines": map[string]string{"box": proto.EngineHerdrCore}})
	h.startDevices(context.Background())
	m := h.machineByID("box")
	if s := m.snapshot(); !s.Online || s.Hostname != "box" || len(m.sessionList()) != 1 || s.Engine != proto.EngineHerdrCore {
		t.Fatalf("before the manager: %+v %v", s, m.sessionList())
	}
	var got struct {
		Path string `json:"path"`
	}
	res := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		res <- m.call(ctx, "fs.list", map[string]any{"path": "/"}, &got)
	}()
	select {
	case err := <-res:
		t.Fatalf("the call did not wait for the manager: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	srv := newHubServer(t, h)
	startTestManager(t, h, srv, map[string]func(context.Context) (net.Conn, error){"box": box.dialer(h)})
	select {
	case err := <-res:
		if err != nil || got.Path != "/home/user" {
			t.Fatalf("the call once the manager is there: %v %+v", err, got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the call never went")
	}
	st.mu.Lock()
	_, left := st.settings[devicesWarmKey]
	st.mu.Unlock()
	if left {
		t.Fatal("the hand-over was read, not taken")
	}
	// Stopping, the hub hands the next process what the manager reported (its own settings, not the manager's).
	waitFor(t, "the manager reports box", func() bool { s := m.snapshot(); return s.Online && len(m.sessionList()) == 1 })
	h.saveDevicesWarm(context.Background())
	next := newTestHub(t)
	next.store = st
	next.cfg.Machines = h.cfg.Machines
	next.startDevices(context.Background())
	if s := next.machineByID("box").snapshot(); !s.Online || s.Hostname != "box" || len(next.machineByID("box").sessionList()) != 1 {
		t.Fatalf("the next hub process: %+v", s)
	}
	// It stops again before its manager reported: what it still shows from the hand-over is handed on.
	next.saveDevicesWarm(context.Background())
	third := newTestHub(t)
	third.store = st
	third.cfg.Machines = h.cfg.Machines
	third.startDevices(context.Background())
	if s := third.machineByID("box").snapshot(); !s.Online || len(third.machineByID("box").sessionList()) != 1 {
		t.Fatalf("a second quick restart: %+v", s)
	}
	// ... with the first hand-over's deadline, not a new one: restarts in a row do not keep it shown for ever.
	nm, tm := next.machineByID("box"), third.machineByID("box")
	nm.mu.Lock()
	first := nm.warmUntil
	nm.mu.Unlock()
	tm.mu.Lock()
	again := tm.warmUntil
	tm.mu.Unlock()
	if again.After(first) {
		t.Fatalf("the deadline moved on: %v, first %v", again, first)
	}
}

// echoServer is a TCP server on this machine that writes back what it reads: a port an agent listens on.
func echoServer(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// portEcho sends text through a forwarded port's WebSocket and returns what came back.
func portEcho(t *testing.T, mh *testHub, tok string, port int, text string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(mh.srv.URL, "https")+"/api/v1/machines/box/ports/"+strconv.Itoa(port)+"/connect",
		&websocket.DialOptions{HTTPClient: mh.srv.Client(), HTTPHeader: http.Header{"Authorization": {"Bearer " + tok}}})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer c.CloseNow()
	if err := c.Write(ctx, websocket.MessageBinary, []byte(text)); err != nil {
		t.Fatal(err)
	}
	var out []byte
	for len(out) < len(text) {
		_, b, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v (got %q)", err, out)
		}
		out = append(out, b...)
	}
	return string(out)
}

// persona_settings (the hub's own tool) blocks a Claude config directory on a machine through the manager, whose setting
// it is now, only after the phone approves, and removes it the same way.
func TestSelfSettingsBlocksClaudeDirsThroughTheManager(t *testing.T) {
	box := newMeshNode("uid-box", "box", "s1")
	box.resident = true
	mh := startTestHub(t, hubA, "Dev", []MachineConfig{{ID: "box", Name: "Box", Kind: "ssh"}}, []*meshNode{box})
	h := mh.h
	eventually(t, 10*time.Second, "box online", func() bool { m := h.machineByID("box"); return m != nil && m.isOnline() })
	m := h.machineByID("box")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run := func(args string) string {
		t.Helper()
		done := make(chan string, 1)
		go func() {
			out, err := h.toolPersonaSettings(ctx, assistantwire.AssistantID, m, "s1", json.RawMessage(args), nil)
			if err != nil {
				out = "error: " + err.Error()
			}
			done <- out
		}()
		for ctx.Err() == nil {
			if pending := h.pendingInteractions(); len(pending) == 1 {
				allow := true
				_ = h.resolve(pending[0].ID, proto.Resolution{Allow: &allow})
				break
			}
			select {
			case out := <-done:
				return out
			case <-time.After(5 * time.Millisecond):
			}
		}
		return <-done
	}
	if out := run(`{"action":"get","key":"agents.blocked_claude_dirs"}`); !strings.Contains(out, "/home/user/.automation/claude-a (listed") {
		t.Fatalf("get: %s", out)
	}
	if out := run(`{"action":"add","key":"agents.blocked_claude_dirs","value":{"machine":"box","pattern":"~/.automation/*"}}`); !strings.HasPrefix(out, "Blocked") {
		t.Fatalf("add: %s", out)
	}
	if got := m.claudeDirsState(ctx).Blocked; len(got) != 1 || got[0] != "~/.automation/*" {
		t.Fatalf("the manager keeps %v", got)
	}
	if out := run(`{"action":"remove","key":"agents.blocked_claude_dirs","id":"box:~/.automation/*"}`); !strings.HasPrefix(out, "Unblocked") {
		t.Fatalf("remove: %s", out)
	}
	if got := m.claudeDirsState(ctx).Blocked; len(got) != 0 {
		t.Fatalf("still blocked: %v", got)
	}
}

// A send the node is still on when the time runs out or the manager's connection drops may have been typed: its
// outcome is unknown, never "not sent" — the same message id is not sent again.
func TestASendCutShortIsUnknown(t *testing.T) {
	box := newMeshNode("uid-box", "box", "s1")
	box.resident = true
	typing := make(chan struct{})
	box.hold = map[string]chan struct{}{"session.send": typing}
	mh := startTestHub(t, hubA, "Dev", []MachineConfig{{ID: "box", Name: "Box", Kind: "ssh"}}, []*meshNode{box})
	t.Cleanup(func() { close(typing) })
	tok, _ := mh.deviceToken(t, roleFull)
	eventually(t, 10*time.Second, "box online", func() bool { return mh.machines(t, tok)["box"].Online })
	m := mh.h.machineByID("box")

	// The time runs out: the manager gives up a moment before the hub and says so (devicewire.CodeDeadline).
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	err := m.call(ctx, "session.send", map[string]any{"session_id": "s1", "text": "slow"}, nil)
	cancel()
	var ne *nodeError
	if !errors.As(err, &ne) || !errors.Is(err, context.DeadlineExceeded) || sendOutcome(err) != sendUnknown {
		t.Fatalf("a send that ran out of time: %v (outcome %q)", err, sendOutcome(err))
	}

	// The manager's connection drops with the send out.
	res := make(chan string, 1)
	go func() {
		code, b := mh.do(t, "POST", "/api/v1/sessions/box/s1/messages", tok, `{"text":"cut","client_msg_id":"m-cut"}`)
		res <- strconv.Itoa(code) + " " + string(b)
	}()
	eventually(t, 5*time.Second, "the node has the send", func() bool { return box.heldCalls("session.send") == 2 })
	mh.mgr.stop()
	var got string
	select {
	case got = <-res:
	case <-time.After(10 * time.Second):
		t.Fatal("no answer after the manager went")
	}
	if !strings.Contains(got, `"delivered":"unknown"`) {
		t.Fatalf("a send cut short by the manager going: %s", got)
	}
	code, b := mh.do(t, "POST", "/api/v1/sessions/box/s1/messages", tok, `{"text":"cut","client_msg_id":"m-cut"}`)
	if !strings.Contains(string(b), `"delivered":"unknown"`) || box.heldCalls("session.send") != 2 {
		t.Fatalf("the same message again: %d %s, the node had %d", code, b, box.heldCalls("session.send"))
	}
}

// The manager's coded errors come back as the hub's own; what may have reached the node never reads as offline.
func TestDeviceErrorsKeepWhatMayHaveBeenDone(t *testing.T) {
	for _, c := range []struct {
		name    string
		err     error
		outcome string
		offline bool
	}{
		{"the bus dropped with the call out", fmt.Errorf("assistant: %w", errBusDropped), sendUnknown, false},
		{"no bus", fmt.Errorf("assistant: %w", errBusOffline), "", true},
		{"its node's connection dropped", &rpc.Error{Code: devicewire.CodeNodeClosed, Message: "dropped"}, sendUnknown, false},
		{"the manager's time ran out", &rpc.Error{Code: devicewire.CodeDeadline, Message: "deadline"}, sendUnknown, false},
		{"the machine offline", &rpc.Error{Code: devicewire.CodeMachineOffline, Message: "offline"}, "", true},
		{"the node's refusal", &rpc.Error{Code: "input_draft", Message: "draft"}, "", false},
	} {
		err := deviceError("box", c.err)
		if got := sendOutcome(err); got != c.outcome || errors.Is(err, errOffline) != c.offline {
			t.Errorf("%s: %v reads as outcome %q offline %v", c.name, err, got, errors.Is(err, errOffline))
		}
	}
	if !errors.Is(deviceError("box", fmt.Errorf("assistant: %w", errBusOffline)), errManagerOffline) {
		t.Error("no bus is the manager offline")
	}
	// The bus dropping with the call out: the app is told the manager is gone (503 assistant_offline), a send that
	// was out is unknown (above).
	if code, b := callErrBytes("", deviceError("box", fmt.Errorf("assistant: %w", errBusDropped))); code != http.StatusServiceUnavailable ||
		!strings.Contains(string(b), "assistant_offline") {
		t.Errorf("a read cut short by the manager going: %d %s", code, b)
	}
	for left, want := range map[time.Duration]time.Duration{30 * time.Second: 29500 * time.Millisecond, time.Second: 900 * time.Millisecond,
		0: time.Millisecond} {
		if got := managerTimeout(left); got != want {
			t.Errorf("managerTimeout(%v) = %v, want %v", left, got, want)
		}
	}
}

// A machine the manager shows as before its restart (warm) is online for the apps, but not connected: the hub's
// self-update waits for its node to be back, and only then has the nodes updated.
func TestAWarmMachineIsNotConnectedYet(t *testing.T) {
	h := newTestHub(t)
	report := func(s devicewire.State) {
		s.Machine.ID, s.Machine.Name, s.Machine.Kind = "box", "Box", "ssh"
		h.applyDeviceStates(devicewire.States{Machines: []devicewire.State{s}})
	}
	report(devicewire.State{Machine: proto.Machine{Online: true}, WarmUntil: time.Now().Add(time.Minute)})
	m := h.machineByID("box")
	if !m.snapshot().Online {
		t.Fatal("the apps see a warm machine as before the restart")
	}
	if got := h.upd.offline([]string{"box"}); len(got) != 1 {
		t.Fatalf("a warm machine counts as back: %v", got)
	}
	report(devicewire.State{Machine: proto.Machine{Online: true}, Connected: true, ConnectedAt: time.Now()})
	if got := h.upd.offline([]string{"box"}); len(got) != 0 || !m.snapshot().Online {
		t.Fatalf("its node back: offline %v", got)
	}
	report(devicewire.State{Machine: proto.Machine{Online: false}})
	if m.snapshot().Online || len(h.upd.offline([]string{"box"})) != 1 {
		t.Fatal("its node gone")
	}
}

// #6372: a chat subscribed through a node restart keeps getting its items. A node watches per connection, so the
// manager tells the new connection to watch again.
func TestLiveItemsAfterTheNodeReconnects(t *testing.T) {
	box := newMeshNode("uid-box", "box", "s1")
	box.resident, box.pushWatchedOnly = true, true
	mh := startTestHub(t, hubA, "Dev", []MachineConfig{{ID: "box", Name: "Box", Kind: "ssh"}}, []*meshNode{box})
	tok, _ := mh.deviceToken(t, roleFull)
	eventually(t, 10*time.Second, "online", func() bool { return mh.machines(t, tok)["box"].Online })
	a := dialApp(t, mh, tok)
	a.send(t, map[string]any{"type": "subscribe", "machine_id": "box", "session_id": "s1"})
	a.wait(t, "backlog", 0, hasItem("hi from s1"))
	box.addItem("s1", "before the restart")
	a.wait(t, "live before the restart", 0, hasItem("before the restart"))
	box.mu.Lock()
	first := box.connects
	box.mu.Unlock()
	_ = box.current().c.Close() // the node restarts
	eventually(t, 15*time.Second, "watched on the new connection", func() bool {
		box.mu.Lock()
		defer box.mu.Unlock()
		return box.connects > first && len(box.conns) == 1 && box.conns[0].watching["s1"]
	})
	box.addItem("s1", "after the restart")
	a.wait(t, "live after the restart", 0, hasItem("after the restart"))
}

// … and when the restarted node refuses the first watch (still starting), the chat still gets its items.
func TestLiveItemsAfterTheRestartedNodeRefusesTheFirstWatch(t *testing.T) {
	box := newMeshNode("uid-box", "box", "s1")
	box.resident, box.pushWatchedOnly = true, true
	mh := startTestHub(t, hubA, "Dev", []MachineConfig{{ID: "box", Name: "Box", Kind: "ssh"}}, []*meshNode{box})
	tok, _ := mh.deviceToken(t, roleFull)
	eventually(t, 10*time.Second, "online", func() bool { return mh.machines(t, tok)["box"].Online })
	a := dialApp(t, mh, tok)
	a.send(t, map[string]any{"type": "subscribe", "machine_id": "box", "session_id": "s1"})
	a.wait(t, "backlog", 0, hasItem("hi from s1"))
	box.mu.Lock()
	first := box.connects
	box.failWatch = map[string]int{"s1": 1}
	box.mu.Unlock()
	_ = box.current().c.Close()
	eventually(t, 15*time.Second, "connected again", func() bool {
		box.mu.Lock()
		defer box.mu.Unlock()
		return box.connects > first && len(box.conns) == 1
	})
	eventually(t, 15*time.Second, "watched on the new connection after the refusal", func() bool {
		box.mu.Lock()
		defer box.mu.Unlock()
		return len(box.conns) == 1 && box.conns[0].watching["s1"]
	})
	box.addItem("s1", "after the refusal")
	a.wait(t, "live after the refusal", 0, hasItem("after the refusal"))
}

// A manager that is new is asked to watch again what the apps watch, a few at a time: a computer slow to answer does
// not hold up the others' items.
func TestWatchedAgainAtOnceWithANewManager(t *testing.T) {
	box, lap := newMeshNode("uid-box", "box", "s1"), newMeshNode("uid-lap", "lap", "s2")
	box.resident, lap.resident = true, true
	mh := startTestHub(t, hubA, "Dev", []MachineConfig{{ID: "box", Name: "Box", Kind: "ssh"}, {ID: "lap", Name: "Lap", Kind: "ssh"}},
		[]*meshNode{box, lap})
	tok, _ := mh.deviceToken(t, roleFull)
	eventually(t, 10*time.Second, "both online", func() bool { ms := mh.machines(t, tok); return ms["box"].Online && ms["lap"].Online })
	a := dialApp(t, mh, tok)
	a.send(t, map[string]any{"type": "subscribe", "machine_id": "box", "session_id": "s1"})
	a.wait(t, "box's backlog", 0, hasItem("hi from s1"))
	a.send(t, map[string]any{"type": "subscribe", "machine_id": "lap", "session_id": "s2"})
	a.wait(t, "lap's backlog", 0, hasItem("hi from s2"))
	slow := make(chan struct{})
	box.mu.Lock()
	box.hold = map[string]chan struct{}{"session.watch": slow}
	box.mu.Unlock()
	t.Cleanup(func() { close(slow) })
	watched := func() int { lap.mu.Lock(); defer lap.mu.Unlock(); return lap.watched["s2"] }
	before := watched()
	mh.mgr.stop()
	mh.mgr = startTestManager(t, mh.h, mh.srv, map[string]func(context.Context) (net.Conn, error){"box": box.dialer(mh.h), "lap": lap.dialer(mh.h)})
	eventually(t, 5*time.Second, "lap watched again while box is slow to answer", func() bool { return watched() > before })
	lap.addItem("s2", "after the new manager")
	a.wait(t, "lap's live item", 0, hasItem("after the new manager"))
}
