package node

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

const piSID = "01a19a98-f2eb-707e-a8ce-b771b9fd5ed6"

// piPaneAgent is a pi in a herdr pane as herdr lists it; with file, as herdr's pi integration reports its session.
func piPaneAgent(pane, cwd, file string) map[string]any {
	a := map[string]any{"agent": "pi", "agent_status": proto.StatusIdle, "pane_id": pane, "tab_id": "w1:t3", "workspace_id": "w1",
		"cwd": cwd, "terminal_title_stripped": "π - work", "terminal_title": "π - work"}
	if file != "" {
		a["agent_session"] = map[string]any{"agent": "pi", "kind": "path", "source": "herdr:pi", "value": file}
	}
	return a
}

// piHerdrNode is a node on a fake herdr with a runtime socket (a resident node's host) for the pi extensions.
func piHerdrNode(t *testing.T) (*Node, *fakeHerdr, string, *RuntimeHost) {
	t.Helper()
	n, f, home := herdrNode(t)
	dir, err := os.MkdirTemp("/tmp", "jpi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	rh := NewRuntimeHost("test", filepath.Join(dir, "r.sock"))
	rh.logger = log.New(io.Discard, "", 0)
	n.logger = log.New(io.Discard, "", 0)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := rh.Listen(ctx); err != nil {
		t.Fatal(err)
	}
	n.runtimeSock = rh.path
	rh.attach(n)
	return n, f, home, rh
}

// piExtension is the extension in a pi process: a runtime listing the session it runs (in pane, when in herdr).
func piExtension(t *testing.T, rh *RuntimeHost, runtime string, rows ...map[string]any) *fakeRuntime {
	t.Helper()
	rt := dialRuntimeAt(t, rh.path, map[string]any{"runtime": runtime, "agent": "pi", "name": "pi", "caps": map[string]any{"send": true, "interrupt": true, "rename": true}})
	_ = rt.peer.Notify("runtime.sessions", map[string]any{"sessions": rows})
	waitFor(t, "the extension's list", func() bool {
		for _, r := range rows {
			if rh.reg.ownerOf(r["session_id"].(string)) == nil {
				return false
			}
		}
		return true
	})
	return rt
}

func onlyRow(t *testing.T, list []proto.Session, id string) proto.Session {
	t.Helper()
	var found []proto.Session
	for _, s := range list {
		if s.SessionID == id {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d rows for %s: %+v", len(found), id, found)
	}
	return found[0]
}

// A pi in a herdr pane is one session: herdr's row (pane, space, tab, the terminal), completed by what the extension
// in it lists. The conversation and the control go to the extension; the screen and the keys to herdr.
func TestPiInHerdrIsOneHerdrSession(t *testing.T) {
	n, f, home, rh := piHerdrNode(t)
	file := filepath.Join(home, ".pi/agent/sessions/--w--/2026-11-02T04-51-17-356Z_"+piSID+".jsonl")
	rt := piExtension(t, rh, "pi-4242", map[string]any{"session_id": piSID, "status": "working", "title": "fix the parser",
		"last_text": "Reading the grammar.", "model": "claude-x", "rev": 7, "pane_id": "w1:p3", "cwd": "/w"})
	seedAgents(t, n, f, piPaneAgent("w1:p3", "/w", file))

	s := onlyRow(t, listSessions(t, n), piSID)
	if s.Agent != "pi" || s.PaneID != "w1:p3" || s.SpaceKind != "herdr" || s.SourceKind != "" {
		t.Fatalf("not herdr's row: %+v", s)
	}
	if s.Title != "fix the parser" || s.LastText != "Reading the grammar." || s.Model != "claude-x" || s.Rev != 7 || s.ReadOnlyReason != "" {
		t.Fatalf("not completed by the extension: %+v", s)
	}

	f.reset()
	if _, err := request(t, n, "session.send", map[string]any{"session_id": piSID, "text": "go on", "client_msg_id": "m-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := request(t, n, "session.items", map[string]any{"session_id": piSID, "turns": 2}); err != nil {
		t.Fatal(err)
	}
	if len(rt.got("session.send")) != 1 || len(rt.got("session.items")) != 1 {
		t.Fatalf("send %d, items %d at the extension", len(rt.got("session.send")), len(rt.got("session.items")))
	}
	for _, c := range f.trace() {
		if strings.Contains(c, "agent.prompt") || strings.Contains(c, "pane.send_text") {
			t.Fatalf("typed into the pane: %v", f.trace())
		}
	}
	f.setScreen("w1:p3", "π pi\n> ")
	if _, err := request(t, n, "session.screen", map[string]any{"session_id": piSID}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(f.trace(), "\n"), "pane.read") {
		t.Fatalf("the screen did not come from herdr: %v", f.trace())
	}
	if len(rt.got("session.screen")) != 0 {
		t.Fatal("the screen went to the extension")
	}
}

// Without herdr's pi integration (no session in the pane's report) the extension says which pane it runs in.
func TestPiInHerdrFoundByTheExtensionsPane(t *testing.T) {
	n, f, _, rh := piHerdrNode(t)
	piExtension(t, rh, "pi-4243", map[string]any{"session_id": piSID, "status": "idle", "pane_id": "w1:p4", "cwd": "/w"})
	seedAgents(t, n, f, piPaneAgent("w1:p4", "/w", ""))
	if s := onlyRow(t, listSessions(t, n), piSID); s.PaneID != "w1:p4" || s.Agent != "pi" {
		t.Fatalf("%+v", s)
	}
}

// An async request for a launcher whose agent owns its session ID starts Pi synchronously, then sends the prompt once
// through the extension's real session instead of returning a placeholder ID.
func TestSessionStartPiWithoutAgentTypeUsesItsRealSessionForTheFirstMessage(t *testing.T) {
	n, f, home, rh := piHerdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	n.shellSettled = true // Keep shell settling outside this test; it exercises session identity and first-message routing.
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": "proj"}}})
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	runtimes := make(chan *fakeRuntime, 1)
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" && strings.Contains(c.String(), "HERDR_AGENT=pi") {
			go func() {
				runtimes <- piExtension(t, rh, "pi-start", map[string]any{
					"session_id": piSID, "status": "idle", "pane_id": "w1:p2", "cwd": cwd,
				})
			}()
		}
	})

	const prompt = "Inspect the parser and report the issue."
	result, err := request(t, n, "session.start", map[string]any{
		"cwd": cwd, "workspace_id": "w1", "launcher": "pi", "prompt": prompt, "async": true,
		// The Web request omits agent_type and session_id for this launcher.
	})
	if err != nil {
		t.Fatalf("start: %v\nherdr calls:\n%s", err, strings.Join(f.trace(), "\n"))
	}
	s := decodeSessions(t, []any{result["session"]})[0]
	if s.SessionID != piSID || s.Agent != piAgent || s.Starting {
		t.Fatalf("returned session %+v, want Pi's reported session %s", s, piSID)
	}
	rt := <-runtimes
	messages := rt.got("session.send")
	if len(messages) != 1 {
		t.Fatalf("Pi received %d first-message sends, want one: %s", len(messages), messages)
	}
	var sent struct {
		SessionID string `json:"session_id"`
		Text      string `json:"text"`
	}
	if err := json.Unmarshal(messages[0], &sent); err != nil {
		t.Fatal(err)
	}
	if sent.SessionID != piSID || sent.Text != prompt {
		t.Fatalf("first message routed to %q with text %q", sent.SessionID, sent.Text)
	}
}

// A promptless Pi start returns the extension's session ID; repeated sends with one client_msg_id reach the extension once.
func TestSessionStartPiWithoutPromptReturnsItsRealSessionBeforeTheFirstSend(t *testing.T) {
	n, f, home, rh := piHerdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	n.shellSettled = true // Keep shell settling outside this test; it exercises the two-stage Web flow.
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": "proj"}}})
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	runtimes := make(chan *fakeRuntime, 1)
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" && strings.Contains(c.String(), "HERDR_AGENT=pi") {
			go func() {
				runtimes <- piExtension(t, rh, "pi-promptless", map[string]any{
					"session_id": piSID, "status": "idle", "pane_id": "w1:p2", "cwd": cwd,
				})
			}()
		}
	})

	result, err := request(t, n, "session.start", map[string]any{
		"cwd": cwd, "workspace_id": "w1", "launcher": "pi", "async": true,
		// This mirrors the first, promptless Web creation request: no agent_type, prompt or session_id.
	})
	if err != nil {
		t.Fatalf("start: %v\nherdr calls:\n%s", err, strings.Join(f.trace(), "\n"))
	}
	s := decodeSessions(t, []any{result["session"]})[0]
	if s.SessionID != piSID || s.Agent != piAgent || s.Starting {
		t.Fatalf("returned session %+v, want ready Pi session %s", s, piSID)
	}
	rt := <-runtimes
	if sends := rt.got("session.send"); len(sends) != 0 {
		t.Fatalf("promptless start sent a message: %s", sends)
	}

	const prompt, clientMsgID = "Send the first task once.", "pi-first-message"
	for range 2 {
		if _, err := request(t, n, "session.send", map[string]any{
			"session_id": piSID, "text": prompt, "client_msg_id": clientMsgID,
		}); err != nil {
			t.Fatalf("first send/retry: %v", err)
		}
	}
	sends := rt.got("session.send")
	if len(sends) != 1 {
		t.Fatalf("Pi received %d sends for one client_msg_id, want one: %s", len(sends), sends)
	}
	var sent struct {
		SessionID string `json:"session_id"`
		Text      string `json:"text"`
	}
	if err := json.Unmarshal(sends[0], &sent); err != nil {
		t.Fatal(err)
	}
	if sent.SessionID != piSID || sent.Text != prompt {
		t.Fatalf("first message routed to %q with text %q", sent.SessionID, sent.Text)
	}
}

// A pi pane no extension serves: listed with what is missing, an empty conversation saying so, no sending (nothing
// typed into pi blind), Esc to interrupt.
func TestPiInHerdrWithoutTheExtension(t *testing.T) {
	n, f, home, _ := piHerdrNode(t)
	file := filepath.Join(home, "2026-11-02T04-51-17-356Z_"+piSID+".jsonl")
	seedAgents(t, n, f, piPaneAgent("w1:p3", "/w", file))
	s := onlyRow(t, listSessions(t, n), piSID)
	if s.ReadOnlyReason != piNoExtension || s.PaneID != "w1:p3" {
		t.Fatalf("%+v", s)
	}
	res, err := request(t, n, "session.items", map[string]any{"session_id": piSID, "turns": 2})
	if err != nil || !strings.Contains(string(mustJSON(res)), "Persona 的扩展") {
		t.Fatalf("items %v %v", res, err)
	}
	f.reset()
	_, err = request(t, n, "session.send", map[string]any{"session_id": piSID, "text": "hi", "client_msg_id": "m-2"})
	if !isRefusal(err, "pi_no_extension") {
		t.Fatalf("send: %v", err)
	}
	if _, err := request(t, n, "session.interrupt", map[string]any{"session_id": piSID}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.trace(), "\n"); strings.Contains(got, "agent.prompt") || !strings.Contains(got, "escape") {
		t.Fatalf("herdr calls: %v", f.trace())
	}
}

// A pi session that ran in herdr and stopped, listed from disk with how to resume it: a send opens a tab in herdr,
// runs the runtime's command there through tab-start (never typed), waits for the pi it starts to list the session,
// and sends there — not to the runtime that had it on disk (pi-web's companion).
func TestPiResumedInHerdr(t *testing.T) {
	n, f, home, rh := piHerdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	file := filepath.Join(home, ".pi/agent/sessions/x_"+piSID+".jsonl")
	files := piExtension(t, rh, "pi-files", map[string]any{"session_id": piSID, "status": "offline", "cwd": cwd,
		"resume_argv": []string{"pi", "--session", file}})
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": "proj"}}})
	f.reply("session.snapshot", snapshot(nil, []any{map[string]any{"tab_id": "w1:t1", "workspace_id": "w1", "label": "agent-1"}}, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	cmdRe := regexp.MustCompile(` tab-start '?([^' ]+)'?$`)
	started := make(chan codexStartFile, 1)
	fresh := make(chan *fakeRuntime, 1)
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_input" {
			return
		}
		m := cmdRe.FindStringSubmatch(str(c.Params["text"]))
		if m == nil {
			return
		}
		var sf codexStartFile
		b, _ := os.ReadFile(m[1])
		_ = json.Unmarshal(b, &sf)
		started <- sf
		// The pi it starts loads the extension, which lists the session running in that pane.
		go func() {
			fresh <- piExtension(t, rh, "pi-5555", map[string]any{"session_id": piSID, "status": "idle", "pane_id": "w1:p2", "cwd": cwd})
		}()
	})
	old := piResumeWait
	piResumeWait = 10 * time.Second
	t.Cleanup(func() { piResumeWait = old })

	if _, err := request(t, n, "session.send", map[string]any{"session_id": piSID, "text": "and the tests", "client_msg_id": "m-3"}); err != nil {
		t.Fatalf("%v\nherdr: %v", err, f.trace())
	}
	// herdr's own pi detection, not HERDR_AGENT: tab-start runs the runtime's command as it is.
	if sf := <-started; strings.Join(sf.Argv, " ") != "pi --session "+file || sf.Agent != "" {
		t.Fatalf("ran %v (HERDR_AGENT %q)", sf.Argv, sf.Agent)
	}
	if len(files.got("session.send")) != 0 {
		t.Fatal("sent to the runtime with the session on disk")
	}
	trace := strings.Join(f.trace(), "\n")
	if !strings.Contains(trace, "tab.create ") || !strings.Contains(trace, `"workspace_id":"w1"`) {
		t.Fatalf("no tab in the session's space: %v", f.trace())
	}
	if strings.Contains(trace, "and the tests") {
		t.Fatal("the message was typed into the pane")
	}
	if rt := <-fresh; len(rt.got("session.send")) != 1 || !strings.Contains(string(rt.got("session.send")[0]), "and the tests") {
		t.Fatal("the message did not reach the pi started in herdr")
	}
}

// No engine to start it in: refused with nothing sent, not handed to the runtime that has it on disk.
func TestPiResumeWithoutAnEngineIsRefused(t *testing.T) {
	n, _ := runtimeNode(t) // herdr unreachable
	cwd := t.TempDir()
	files := dialRuntime(t, n, map[string]any{"runtime": "pi-files", "agent": "pi", "caps": map[string]any{"send": true}})
	_ = files.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{{"session_id": piSID, "status": "offline",
		"cwd": cwd, "resume_argv": []string{"pi", "--session", "/x_" + piSID + ".jsonl"}}}})
	waitFor(t, "listed", func() bool { return n.runtimes.ownerOf(piSID) != nil })
	_, err := nodeCall(t, n, "session.send", map[string]any{"session_id": piSID, "text": "hi", "client_msg_id": "m-4"})
	if err == nil || !strings.Contains(err.Error(), "没在运行") {
		t.Fatalf("send: %v", err)
	}
	if len(files.got("session.send")) != 0 {
		t.Fatal("handed to the runtime with it on disk")
	}
}

// herdr's pi integration reports the session's file: its id is the file's uuid.
func TestPiSessionIDFromTheReportedFile(t *testing.T) {
	n, f, _ := herdrNode(t)
	seedAgents(t, n, f, piPaneAgent("w1:p9", "/w", "/home/u/.pi/agent/sessions/--w--/2026-11-02T04-51-17-356Z_"+piSID+".jsonl"))
	if _, ok := n.agent(piSID); !ok {
		t.Fatal("not found by the reported file")
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
