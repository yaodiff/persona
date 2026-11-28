package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/acceptloop"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// pluginSocket serves n's node socket at a short path of its own (the node's own path is the user's), and gives a
// client for it: what the Persona plugin's $.http.fetch is, from this test's process (the claude whose sessions/<pid>.json
// writeOutsideClaude writes).
func pluginSocket(t *testing.T, n *Node) *http.Client {
	t.Helper()
	dir, err := os.MkdirTemp("", "ccp")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "n.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go acceptloop.Run(ctx, ln, t.Logf, func(c net.Conn) { go n.serveSock(ctx, c) })
	t.Cleanup(func() { cancel(); _ = ln.Close(); _ = os.RemoveAll(dir) })
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}}}
}

// pluginPost is one request of the plugin's: the status and the JSON answer.
func pluginPost(t *testing.T, c *http.Client, what string, body map[string]any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	res, err := c.Post("http://persona/claude-plugin/v1/"+what, "application/json", strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return res.StatusCode, out
}

// fakePlugin is Persona's plugin in a session: it polls, and does with each message what its mode says. "submit"
// writes the prompt into the transcript as Claude Code does for a plugin's asUser prompt, then says submitted;
// "queue" writes the queue entry Claude Code writes for one submitted while a turn runs or a dialog waits, then says
// submitted; "refuse" says it did not submit; "silent" takes the message and does nothing.
type fakePlugin struct {
	t          *testing.T
	c          *http.Client
	session    string
	transcript string

	mu    sync.Mutex
	mode  string
	got   []map[string]any
	ready chan struct{} // the first authenticated HTTP poll completed
}

func startFakePlugin(t *testing.T, c *http.Client, session, transcript, mode string) *fakePlugin {
	return startFakePluginAs(t, c, session, transcript, mode, "0.1.0", nil)
}

// startFakePluginAs is startFakePlugin for a plugin of version with capabilities (0.2.0: busy_input, interrupt).
func startFakePluginAs(t *testing.T, c *http.Client, session, transcript, mode, version string, caps []string) *fakePlugin {
	p := &fakePlugin{t: t, c: c, session: session, transcript: transcript, mode: mode, ready: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		var ready sync.Once
		for ctx.Err() == nil {
			b, _ := json.Marshal(map[string]any{"session_id": session, "plugin_version": version, "engine_version": "2.1.308", "capabilities": caps, "wait_ms": 1000})
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://persona/claude-plugin/v1/poll", strings.NewReader(string(b)))
			res, err := c.Do(req)
			if err != nil {
				select {
				case <-ctx.Done():
				case <-time.After(20 * time.Millisecond):
				}
				continue
			}
			var answer struct {
				Messages []map[string]any `json:"messages"`
			}
			decodeErr := json.NewDecoder(res.Body).Decode(&answer)
			res.Body.Close()
			if res.StatusCode == http.StatusOK && decodeErr == nil {
				ready.Do(func() { close(p.ready) })
			}
			for _, m := range answer.Messages {
				p.take(m)
			}
		}
	}()
	return p
}

func (p *fakePlugin) take(m map[string]any) {
	p.mu.Lock()
	p.got = append(p.got, m)
	mode := p.mode
	p.mu.Unlock()
	text, _ := m["text"].(string)
	switch mode {
	case "submit":
		line, _ := json.Marshal(map[string]any{"type": "user", "uuid": newUUID(), "sessionId": p.session,
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "message": map[string]any{"role": "user", "content": text},
			"origin": map[string]any{"kind": "plugin", "name": "persona", "asUser": true}, "promptSource": "system"})
		f, err := os.OpenFile(p.transcript, os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.Write(append(line, '\n'))
			_ = f.Close()
		}
		pluginPost(p.t, p.c, "ack", map[string]any{"session_id": p.session, "id": m["id"], "submitted": true})
	case "queue":
		line, _ := json.Marshal(map[string]any{"type": "queue-operation", "operation": "enqueue", "sessionId": p.session,
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "content": text})
		f, err := os.OpenFile(p.transcript, os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.Write(append(line, '\n'))
			_ = f.Close()
		}
		pluginPost(p.t, p.c, "ack", map[string]any{"session_id": p.session, "id": m["id"], "submitted": true})
	case "refuse":
		pluginPost(p.t, p.c, "ack", map[string]any{"session_id": p.session, "id": m["id"], "submitted": false, "reason": "late"})
	}
}

func (p *fakePlugin) messages() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]any(nil), p.got...)
}

// waitPolling waits until the node has the plugin's poll waiting.
func waitPolling(t *testing.T, n *Node, session string) {
	t.Helper()
	waitFor(t, "the plugin's poll", func() bool {
		n.ccPlugins.mu.Lock()
		defer n.ccPlugins.mu.Unlock()
		s := n.ccPlugins.sessions[session]
		return s != nil && s.poll != nil
	})
}

func shortPluginWaits(t *testing.T) {
	oldPick, oldSubmit, oldConfirm := ccPluginPickupWait, ccPluginSubmitWait, ccPluginConfirmWait
	ccPluginPickupWait, ccPluginSubmitWait, ccPluginConfirmWait = 500*time.Millisecond, 300*time.Millisecond, 600*time.Millisecond
	t.Cleanup(func() { ccPluginPickupWait, ccPluginSubmitWait, ccPluginConfirmWait = oldPick, oldSubmit, oldConfirm })
}

// The owner's own message to a Claude at rest in the desktop app goes in through Persona's plugin, as the person's
// input: the inbox gets nothing, the answer says so, and the session's row says the plugin is there.
func TestCCPluginTakesTheOwnersMessage(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 2*time.Second)
	shortPluginWaits(t)
	p := bgTranscript(t, home, home, sidA, "plan")
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	c := pluginSocket(t, n)

	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || s.Capabilities.PluginInput {
		t.Fatalf("plugin_input before any poll: %+v", s.Capabilities)
	}
	plugin := startFakePlugin(t, c, sidA, p, "submit")
	waitPolling(t, n, sidA)
	if s := byID(listSessions(t, n))[sidA]; s.Capabilities == nil || !s.Capabilities.PluginInput || !s.Capabilities.Send {
		t.Fatalf("plugin_input with the plugin polling: %+v", s.Capabilities)
	}

	res, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "yes, go ahead and merge", "client_msg_id": "m-1", "owner_input": true})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if res["ok"] != true || res["delivered_as"] != "user_input" || res["relay_reason"] != nil {
		t.Errorf("answer %v", res)
	}
	if got := plugin.messages(); len(got) != 1 || got[0]["text"] != "yes, go ahead and merge" {
		t.Errorf("the plugin got %v", got)
	}
	if len(inbox.users()) != 0 {
		t.Errorf("the inbox got %v as well", inbox.users())
	}
}

// Everyone else's message keeps the inbox, the plugin there or not: one without owner_input never reaches the plugin
// and its answer says nothing of the way it went.
func TestCCPluginOnlyForTheOwner(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 2*time.Second)
	shortPluginWaits(t)
	p := bgTranscript(t, home, home, sidA, "plan")
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "cli", "idle", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	plugin := startFakePlugin(t, pluginSocket(t, n), sidA, p, "submit")
	waitPolling(t, n, sidA)

	res, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "the assistant says hi", "client_msg_id": "m-1"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if res["delivered_as"] != nil || len(plugin.messages()) != 0 || len(inbox.users()) != 1 {
		t.Errorf("answer %v, plugin %v, inbox %d", res, plugin.messages(), len(inbox.users()))
	}
}

// When the plugin route does not go, the owner's message goes through the inbox and the answer says why: a session
// at work whose plugin is one from before busy_input, a state the plugin does not deliver in, no plugin, a plugin that
// will not submit it, one that takes it and shows nothing before its deadline.
func TestCCPluginFallsBackToTheInbox(t *testing.T) {
	for _, c := range []struct {
		name, status, mode string // mode "": no plugin
		reason             string
		pluginGets         int
	}{
		{"busy, a plugin from before busy_input", "busy", "submit", relayOutdated, 0},
		{"behind a dialog, a plugin from before busy_input", "waiting", "submit", relayOutdated, 0},
		{"a state the plugin does not deliver in", "starting", "submit", relayBusy, 0},
		{"no plugin", "idle", "", relayNoPlugin, 0},
		{"refused", "idle", "refuse", relayRefused, 1},
		{"silent", "idle", "silent", relayUnconfirmed, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			noPaneEnv(t)
			shortPeerWait(t, 2*time.Second)
			shortPluginWaits(t)
			p := bgTranscript(t, home, home, sidA, "plan")
			inbox := newFakeInbox(t, p)
			writeOutsideClaude(t, home, sidA, "claude-desktop", c.status, inbox, nil)
			f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
			var plugin *fakePlugin
			if c.mode != "" {
				plugin = startFakePlugin(t, pluginSocket(t, n), sidA, p, c.mode)
				if c.mode == "refuse" {
					// Finish transport setup and authentication before measuring the short acknowledgement window.
					<-plugin.ready
				}
				waitPolling(t, n, sidA)
			}
			res, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "merge it", "client_msg_id": "m-1", "owner_input": true})
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if res["ok"] != true || res["delivered_as"] != "relayed" || res["relay_reason"] != c.reason {
				t.Errorf("answer %v", res)
			}
			if len(inbox.users()) != 1 {
				t.Errorf("the inbox got %d messages", len(inbox.users()))
			}
			if plugin != nil && len(plugin.messages()) != c.pluginGets {
				t.Errorf("the plugin got %v", plugin.messages())
			}
		})
	}
}

// The node hands a session's messages only to the claude that has it open: a poll naming another session is refused,
// and a claude the node does not deliver to this way (one an SDK runs) is told to come back much later.
func TestCCPluginPollsAreTheSessionsOwn(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	p := bgTranscript(t, home, home, sidA, "plan")
	writeOutsideClaude(t, home, sidA, "cli", "idle", newFakeInbox(t, p), nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	c := pluginSocket(t, n)

	status, out := pluginPost(t, c, "poll", map[string]any{"session_id": sidB, "wait_ms": 100})
	if status != http.StatusForbidden || out["error"] != "not_this_session" {
		t.Errorf("another session's poll: %d %v", status, out)
	}
	if status, _ := pluginPost(t, c, "poll", map[string]any{"session_id": "../x", "wait_ms": 100}); status != http.StatusBadRequest {
		t.Errorf("a poll with no session id: %d", status)
	}
	if status, _ := pluginPost(t, c, "nothing", map[string]any{"session_id": sidA}); status != http.StatusNotFound {
		t.Errorf("an unknown request: %d", status)
	}
	status, out = pluginPost(t, c, "poll", map[string]any{"session_id": sidA, "wait_ms": 100})
	if status != http.StatusOK || out["retry_ms"] != nil {
		t.Errorf("its own poll: %d %v", status, out)
	}

	writeOutsideClaude(t, home, sidA, "sdk-ts", "idle", nil, nil)
	status, out = pluginPost(t, c, "poll", map[string]any{"session_id": sidA, "wait_ms": 100})
	if status != http.StatusOK || out["retry_ms"] != float64(ccPluginOtherRetry.Milliseconds()) {
		t.Errorf("an SDK's claude: %d %v", status, out)
	}
}

// Exercise the process-bound poll and both readers of session effort (the list and details).
func TestCCPluginDesktopEffortReadback(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	bgTranscript(t, home, home, sidA, "first")
	bgTranscript(t, home, home, sidB, "second")
	writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", nil, nil)
	writeClaudeProc(t, home, os.Getppid(), map[string]any{"sessionId": sidB, "cwd": home, "entrypoint": "claude-desktop", "kind": "interactive", "status": "idle"})
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	c := pluginSocket(t, n)
	poll := func(level string, at time.Time) {
		t.Helper()
		req := map[string]any{"session_id": sidA, "wait_ms": 1}
		if !at.IsZero() {
			req["observed_effort"] = map[string]any{"at_ms": at.UnixMilli(), "level": level}
		}
		if status, body := pluginPost(t, c, "poll", req); status != http.StatusOK {
			t.Fatalf("poll: %d %v", status, body)
		}
	}
	check := func(id, want string) {
		t.Helper()
		if _, effort := n.modesOf(id); effort != want {
			t.Errorf("details for %s: effort %q, want %q", id, effort, want)
		}
		for _, row := range listSessions(t, n) {
			if row.SessionID == id {
				if row.Effort != want {
					t.Errorf("row for %s: effort %q, want %q", id, row.Effort, want)
				}
				return
			}
		}
		t.Fatalf("missing session %s", id)
	}

	now := time.Now()
	check(sidA, "")
	poll("high", now)
	check(sidA, "high")
	check(sidB, "")
	poll("low", now.Add(time.Millisecond))
	check(sidA, "low")
	poll("high", now) // an overlapping poll arrives out of order
	check(sidA, "low")
	poll("", now.Add(2*time.Millisecond))
	check(sidA, "")
	poll("high", now.Add(3*time.Millisecond))
	check(sidA, "high")
	poll("", time.Time{}) // a wake poll with no readback can arrive late
	check(sidA, "high")
	reset := now.Add(4 * time.Millisecond)
	status, _ := pluginPost(t, c, "poll", map[string]any{"session_id": sidA, "wait_ms": 1,
		"observed_effort": map[string]any{"at_ms": reset.UnixMilli(), "reset": true}})
	if status != http.StatusOK {
		t.Fatalf("reset: %d", status)
	}
	poll("high", now)
	check(sidA, "")
	poll("max", now.Add(5*time.Millisecond))
	n.mu.Lock()
	n.agents[sidA] = host.Agent{Agent: "claude", PaneID: "w1:p2"}
	n.mu.Unlock()
	if _, effort := n.modesOf(sidA); effort != "" {
		t.Fatalf("Desktop effort leaked into terminal pane details: %q", effort)
	}
	n.mu.Lock()
	delete(n.agents, sidA)
	n.mu.Unlock()
	n.ccPlugins.mu.Lock()
	n.ccPlugins.sessions[sidA].effortSeen = now.Add(-ccPluginStale - time.Second)
	n.ccPlugins.mu.Unlock()
	check(sidA, "")

	// Repeating a poll must not turn old evidence into a new step after an explicit pick.
	n.noteModes(sidA, "", "medium")
	poll("low", now.Add(-time.Minute))
	check(sidA, "medium")
	poll("xhigh", time.Now().Add(time.Second))
	check(sidA, "xhigh")
	poll("auto", time.Now().Add(2*time.Second))
	check(sidA, "auto")
}

func TestCCPluginSameEffortUpdatesTimeWithoutRebuildingList(t *testing.T) {
	var r ccPluginRegistry
	poll := func(level string, at int64) bool {
		_, _, _, _, changed := r.polled(sidA, 1, "", "", ccPluginCaps{}, false, false, ccPluginEffort{Level: level, AtMS: at})
		return changed
	}
	if !poll("high", 1) || poll("high", 2) || !poll("low", 3) {
		t.Fatal("expected a list notification only for a new or changed level")
	}
	observed, _ := r.observedEffort(sidA)
	if observed.Level != "low" || observed.AtMS != 3 {
		t.Fatalf("last observation: %+v", observed)
	}
}

func TestCCPluginDesktopEffortPollWhileListBlocked(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", nil, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	c := pluginSocket(t, n)
	// Start with an established channel, then block list building while the native level switches.
	pluginPost(t, c, "poll", map[string]any{"session_id": sidA, "wait_ms": 1})
	withFakeHub(t, n)
	t.Cleanup(func() { <-n.notifySessionsSoon() })
	offlineCache.Lock()
	defer offlineCache.Unlock()
	done := make(chan int, 1)
	go func() {
		status, _ := pluginPost(t, c, "poll", map[string]any{"session_id": sidA, "wait_ms": 1,
			"observed_effort": map[string]any{"at_ms": time.Now().UnixMilli(), "level": "high"}})
		done <- status
	}()
	select {
	case status := <-done:
		if status != http.StatusOK {
			t.Fatalf("poll: %d", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("effort poll waited for the session list")
	}
}

// claude-plugin.status lists the sessions whose plugin polls, for installers and the doctor.
func TestCCPluginStatus(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	p := bgTranscript(t, home, home, sidA, "plan")
	writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", newFakeInbox(t, p), nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	c := pluginSocket(t, n)
	ask := func() map[string]any {
		dir, _ := os.MkdirTemp("", "ccs")
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		path := filepath.Join(dir, "n.sock")
		ln, err := net.Listen("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		go func() {
			if s, err := ln.Accept(); err == nil {
				n.serveSock(context.Background(), s)
			}
		}()
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		fmt.Fprintln(conn, `{"kind":"claude-plugin.status"}`)
		line, _ := bufio.NewReader(conn).ReadBytes('\n')
		out := map[string]any{}
		_ = json.Unmarshal(line, &out)
		return out
	}
	if out := ask(); out["protocol"] != float64(1) || len(out["sessions"].([]any)) != 0 {
		t.Errorf("before a poll: %v", out)
	}
	startFakePlugin(t, c, sidA, p, "submit")
	waitPolling(t, n, sidA)
	out := ask()
	list, _ := out["sessions"].([]any)
	if len(list) != 1 {
		t.Fatalf("status %v", out)
	}
	s := list[0].(map[string]any)
	if s["session_id"] != sidA || s["pid"] != float64(os.Getpid()) || s["plugin_version"] != "0.1.0" || s["engine_version"] != "2.1.308" || s["polling"] != true {
		t.Errorf("status entry %v", s)
	}
}

// Only Persona's own prompt submitted as the person's words counts as taken: not another plugin's, not one framed as
// a plugin's message, not a peer's; Claude Code's queue entry for it does.
func TestCCPluginLineSeen(t *testing.T) {
	const needle = "merge it"
	for _, c := range []struct {
		line string
		want bool
	}{
		{`{"type":"user","message":{"role":"user","content":"merge it now"},"origin":{"kind":"plugin","name":"persona","asUser":true}}`, true},
		{`{"type":"user","message":{"role":"user","content":"The persona plugin sent a message:\nmerge it"},"origin":{"kind":"plugin","name":"persona"}}`, false},
		{`{"type":"user","message":{"role":"user","content":"merge it"},"origin":{"kind":"plugin","name":"other","asUser":true}}`, false},
		{`{"type":"user","message":{"role":"user","content":"merge it"},"isMeta":true,"origin":{"kind":"peer","name":"Persona"}}`, false},
		{`{"type":"queue-operation","operation":"enqueue","content":"merge it"}`, true},
		{`{"type":"queue-operation","operation":"dequeue","content":"merge it"}`, false},
		{`{"type":"user","message":{"role":"user","content":"something else"},"origin":{"kind":"plugin","name":"persona","asUser":true}}`, false},
	} {
		if got := ccPluginLineSeen([]byte(c.line), needle); got != c.want {
			t.Errorf("%s: %v", c.line, got)
		}
	}
}

// Only an HTTP request line is the plugin's; the JSON requests keep their way.
func TestIsHTTPRequestLine(t *testing.T) {
	for line, want := range map[string]bool{
		"POST /claude-plugin/v1/poll HTTP/1.1\r\n": true,
		"GET / HTTP/1.1\r\n":                       true,
		`{"kind":"ping"}` + "\n":                   false,
		"POSTAL\n":                                 false,
	} {
		if isHTTPRequestLine([]byte(line)) != want {
			t.Errorf("%q", line)
		}
	}
}

// A 0.2.0 plugin advertising busy_input and interrupt queues owner follow-ups as user input while a turn runs (setting
// queued: true without touching the peer inbox) and aborts the active turn on OwnerInterrupt.
func TestCCPluginQueuesBusyOwnerMessageAndAbortsTurn(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 2*time.Second)
	shortPluginWaits(t)
	p := bgTranscript(t, home, home, sidA, "auto")
	inbox := newFakeInbox(t, p)
	inbox.setMode(peerModeBypass) // would reject/hold unattested peer messages in headless desktop mode
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "busy", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	c := pluginSocket(t, n)

	var mu sync.Mutex
	var queuedMsgs []string
	var interrupts []string
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			b, _ := json.Marshal(map[string]any{
				"session_id":     sidA,
				"plugin_version": "0.2.0",
				"engine_version": "2.1.326",
				"capabilities":   []string{"busy_input", "interrupt"},
				"wait_ms":        1000,
			})
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://persona/claude-plugin/v1/poll", strings.NewReader(string(b)))
			res, err := c.Do(req)
			if err != nil {
				continue
			}
			var answer struct {
				Messages  []map[string]any `json:"messages"`
				Interrupt map[string]any   `json:"interrupt"`
			}
			_ = json.NewDecoder(res.Body).Decode(&answer)
			res.Body.Close()
			if answer.Interrupt != nil {
				id, _ := answer.Interrupt["id"].(string)
				mu.Lock()
				interrupts = append(interrupts, id)
				mu.Unlock()
				pluginPost(t, c, "ack", map[string]any{"session_id": sidA, "id": id, "submitted": true})
			}
			for _, m := range answer.Messages {
				text, _ := m["text"].(string)
				mu.Lock()
				queuedMsgs = append(queuedMsgs, text)
				mu.Unlock()
				line, _ := json.Marshal(map[string]any{
					"type":      "queue-operation",
					"operation": "enqueue",
					"content":   text,
				})
				if tf, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
					_, _ = tf.Write(append(line, '\n'))
					_ = tf.Close()
				}
				pluginPost(t, c, "ack", map[string]any{"session_id": sidA, "id": m["id"], "submitted": true})
			}
		}
	}()
	waitPolling(t, n, sidA)

	res, err := request(t, n, "session.send", map[string]any{
		"session_id":    sidA,
		"text":          "keep going and check tests",
		"client_msg_id": "m-busy-1",
		"owner_input":   true,
	})
	if err != nil {
		t.Fatalf("busy owner send: %v", err)
	}
	if res["ok"] != true || res["delivered_as"] != "user_input" || res["queued"] != true {
		t.Fatalf("busy owner send result: %v", res)
	}

	waitPolling(t, n, sidA)
	if _, err := request(t, n, nodeproto.OwnerInterrupt, map[string]any{"session_id": sidA}); err != nil {
		t.Fatalf("owner interrupt: %v", err)
	}
	mu.Lock()
	gotMsgs, gotInts := append([]string(nil), queuedMsgs...), append([]string(nil), interrupts...)
	mu.Unlock()
	if len(gotMsgs) != 1 || gotMsgs[0] != "keep going and check tests" {
		t.Fatalf("queued messages: %v", gotMsgs)
	}
	if len(gotInts) != 1 {
		t.Fatalf("interrupts: %v", gotInts)
	}
	if len(inbox.users()) != 0 {
		t.Fatalf("peer inbox should not have been used: %v", inbox.users())
	}
}

// When a desktop Claude process starts after an earlier seeded transcript mode (e.g. "auto") with explicit
// --permission-mode=bypassPermissions on its command line, peerSessionClass honors the live process flag.
func TestPeerSessionClassHonorsNewerProcessPermissionMode(t *testing.T) {
	n, _, home := herdrNode(t)
	noPaneEnv(t)
	oldTime := time.Date(2026, 11, 20, 4, 0, 0, 0, time.UTC)
	p := writeTranscript(t, home, home, sidA, oldTime,
		`{"type":"user","permissionMode":"auto","uuid":"u1","timestamp":"2026-11-20T04:00:00Z","cwd":"`+home+`","sessionId":"`+sidA+`","message":{"role":"user","content":"seed"}}`)
	_ = p

	oldArgv := processArgv
	processArgv = func(pid int) ([]string, error) {
		return []string{"claude", "--resume", sidA, "--permission-mode=bypassPermissions"}, nil
	}
	t.Cleanup(func() { processArgv = oldArgv })

	proc := claudeProc{
		pid:        os.Getpid(),
		SessionID:  sidA,
		StartedAt:  oldTime.Add(5 * time.Minute).UnixMilli(),
		Entrypoint: "claude-desktop-3p",
		dir:        filepath.Join(home, ".claude", "sessions"),
	}
	if got := peerSessionClass(n, proc, sidA); got != peerModeBypass {
		t.Fatalf("peerSessionClass = %q, want %q", got, peerModeBypass)
	}
}

// A session waiting on a permission prompt or a plan to approve takes the owner's message through a busy_input plugin
// too: Claude Code queues the prompt behind the dialog (measured with 2.1.329), so it goes in as the person's words
// once the dialog is answered, and the answer says it is queued and waits on the computer. Before, it went through the
// inbox, which a bypass session can drop without a trace.
func TestCCPluginQueuesBehindADialog(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 2*time.Second)
	shortPluginWaits(t)
	p := bgTranscript(t, home, home, sidA, "plan")
	inbox := newFakeInbox(t, p)
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "waiting", inbox, map[string]any{"waitingFor": "permission prompt"})
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	plugin := startFakePluginAs(t, pluginSocket(t, n), sidA, p, "queue", "0.2.0", []string{"busy_input", "interrupt"})
	waitPolling(t, n, sidA)

	res, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "use the staging bucket", "client_msg_id": "m-1", "owner_input": true})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if res["ok"] != true || res["delivered_as"] != "user_input" || res["queued"] != true || res["waits_on_computer"] != true {
		t.Errorf("answer %v", res)
	}
	if got := plugin.messages(); len(got) != 1 || got[0]["text"] != "use the staging bucket" {
		t.Errorf("the plugin got %v", got)
	}
	if len(inbox.users()) != 0 {
		t.Errorf("the inbox got %v as well", inbox.users())
	}
}

// A message the inbox read and the session did not take still says how it went (delivered_as relayed, relay_reason):
// the app can tell the owner the session's plugin is one from before busy_input, which only a new start of the
// session replaces, instead of only "not sure it arrived".
func TestCCPluginUnconfirmedRelaySaysWhy(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 300*time.Millisecond)
	shortPluginWaits(t)
	p := bgTranscript(t, home, home, sidA, "plan")
	inbox := newFakeInbox(t, p)
	inbox.setMode("discard") // read, then nothing: dropped, refused or expired there
	writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "busy", inbox, nil)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	startFakePlugin(t, pluginSocket(t, n), sidA, p, "submit")
	waitPolling(t, n, sidA)

	_, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "dispatch the measurements", "client_msg_id": "m-1", "owner_input": true})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != peerUnconfirmed {
		t.Fatalf("send: %v", err)
	}
	data := map[string]any{}
	_ = json.Unmarshal(re.Data, &data)
	if data["delivered"] != deliveredUnknown || data["delivered_as"] != deliveredRelayed || data["relay_reason"] != relayOutdated {
		t.Errorf("error data %s", re.Data)
	}
	// Anyone else's message says nothing of a way it went.
	_, err = request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "a note from the assistant", "client_msg_id": "m-2"})
	if !errors.As(err, &re) || re.Code != peerUnconfirmed || strings.Contains(string(re.Data), "relay_reason") {
		t.Errorf("a message that is not the owner's: %v %s", err, re.Data)
	}
}

// Right after the node starts, a session whose claude can have the plugin gets a moment for the plugin's first poll
// (its backoff after the node went away) before the owner's message goes through the inbox as no_plugin.
func TestCCPluginWaitsForARestartedNodesPlugin(t *testing.T) {
	for _, c := range []struct {
		name               string
		installed, fresh   bool
		as, reason         string
		pluginGets, inboxN int
	}{
		{"plugin installed, node just started", true, true, deliveredUserInput, "", 1, 0},
		{"plugin not installed there", false, true, deliveredRelayed, relayNoPlugin, 0, 1},
		{"node running for a while", true, false, deliveredRelayed, relayNoPlugin, 0, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			noPaneEnv(t)
			shortPeerWait(t, 2*time.Second)
			shortPluginWaits(t)
			oldWait, oldInstalled := ccPluginRejoinWait, ccPluginInstalledFor
			ccPluginRejoinWait = 3 * time.Second
			entered := make(chan struct{})
			var installed sync.Once
			ccPluginInstalledFor = func(_ *Node, dir string) bool {
				present := c.installed && sameDir(dir, filepath.Join(home, ".claude"))
				if present {
					installed.Do(func() { close(entered) })
				}
				return present
			}
			t.Cleanup(func() { ccPluginRejoinWait, ccPluginInstalledFor = oldWait, oldInstalled })
			n.bootAt = time.Now()
			if !c.fresh {
				n.bootAt = time.Now().Add(-time.Hour)
			}
			p := bgTranscript(t, home, home, sidA, "plan")
			inbox := newFakeInbox(t, p)
			writeOutsideClaude(t, home, sidA, "claude-desktop-3p", "idle", inbox, nil)
			f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
			client := pluginSocket(t, n)
			var plugin *fakePlugin
			params := map[string]any{"session_id": sidA, "text": "carry on", "client_msg_id": "m-1", "owner_input": true}
			var res map[string]any
			var err error
			if c.fresh && c.installed {
				type result struct {
					res map[string]any
					err error
				}
				finished := make(chan result, 1)
				go func() {
					answer, sendErr := request(t, n, "session.send", params)
					finished <- result{answer, sendErr}
				}()
				select {
				case <-entered:
				case early := <-finished:
					t.Fatalf("send finished before waiting for the returning plugin: %v, %v", early.res, early.err)
				}
				plugin = startFakePlugin(t, client, sidA, p, "submit")
				answer := <-finished
				res, err = answer.res, answer.err
			} else {
				res, err = request(t, n, "session.send", params)
				// A plugin arriving after a non-rejoining send must not receive that earlier message.
				plugin = startFakePlugin(t, client, sidA, p, "submit")
				waitPolling(t, n, sidA)
			}
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if res["delivered_as"] != c.as || (c.reason != "" && res["relay_reason"] != c.reason) {
				t.Errorf("answer %v", res)
			}
			if len(plugin.messages()) != c.pluginGets || len(inbox.users()) != c.inboxN {
				t.Errorf("plugin %v, inbox %d", plugin.messages(), len(inbox.users()))
			}
		})
	}
}
