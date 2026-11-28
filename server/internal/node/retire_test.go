package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Retiring a finished task's agent and resuming it on the next message (retire.go).

// withoutAgentReports drops herdr's agent reports from a trace. The node reports a pane's agent — its state, and which
// session runs in it — in the background (herdrevents.go, herdrresume.go), so one can land between any two calls of
// whatever is being driven; a test that lists the calls it expects should not depend on where it fell (#1940).
func withoutAgentReports(trace []string) []string {
	return slices.DeleteFunc(slices.Clone(trace), func(c string) bool {
		return strings.HasPrefix(c, "pane.report_agent") || strings.HasPrefix(c, "pane.clear_agent_authority")
	})
}

// session.retire (what a hub before #4754 asks for a finished or idle agent) stops a still Claude and keeps its pane,
// with what it was started with, and refuses one that works or waits; one not running is no error.
func TestHerdrRetire(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	for _, id := range []string{sidA, sidB, sidC} {
		// A finished turn: a transcript with a prompt and nothing after it, written just now, is a turn under way
		// whatever herdr says (claudeStatus, #2069).
		writeTranscript(t, home, cwd, id, time.Now(), `{"type":"user","cwd":"`+cwd+`","message":{"role":"user","content":"hi"},"uuid":"u1"}`,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]},"uuid":"a1","parentUuid":"u1"}`,
			`{"type":"system","subtype":"turn_duration","uuid":"d1","parentUuid":"a1","durationMs":1000}`)
	}
	n.state.put(sidA, sessionState{Launcher: "cc", Cwd: cwd, Model: "opus"})
	f.reply("session.snapshot", snapshot(nil, nil, []any{
		claudeAgent(sidA, proto.StatusDone, "w1:p1", "w1:t1", "w1", cwd, ""),
		claudeAgent(sidB, proto.StatusWorking, "w1:p2", "w1:t2", "w1", cwd, ""),
		claudeAgent(sidC, proto.StatusBlocked, "w1:p3", "w1:t3", "w1", cwd, ""),
		claudeAgent(sidD, proto.StatusIdle, "w1:p4", "w1:t4", "w1", cwd, ""), // no transcript
	}, nil))
	listSessions(t, n)
	// sidA was started by Persona: the refresh gives herdr its resume command in the background (backfillResume),
	// once. Wait for it, or it lands among retire's calls.
	waitCalls(t, f, "pane.report_agent_session", 1)
	f.reset()

	for _, id := range []string{sidB, sidC} {
		_, err := request(t, n, "session.retire", map[string]any{"session_id": id})
		if code, _ := refusalOf(t, err); code != notRetirable {
			t.Errorf("%s: code %q", id, code)
		}
	}
	quitsOn(f, "w1:p1", sidA, []any{
		claudeAgent(sidB, proto.StatusWorking, "w1:p2", "w1:t2", "w1", cwd, ""),
		claudeAgent(sidC, proto.StatusBlocked, "w1:p3", "w1:t3", "w1", cwd, ""),
		claudeAgent(sidD, proto.StatusIdle, "w1:p4", "w1:t4", "w1", cwd, ""),
	}, shellPane("w1:p1", "w1:t1", "w1", cwd, sidA))
	res, err := request(t, n, "session.retire", map[string]any{"session_id": sidA})
	if err != nil || res["closed"] != true {
		t.Fatalf("retire = %v, %v", res, err)
	}
	if st, _ := n.state.get(sidA); st.Launcher != "cc" || st.Cwd != cwd || st.Model != "opus" {
		t.Errorf("state after retire = %+v", st)
	}
	assertTrace(t, withoutAgentReports(f.trace("pane.send_keys", "tab.close", "pane.close")), []string{
		sendKeys("w1:p1", "ctrl+c"), sendKeys("w1:p1", "ctrl+c"),
	})

	f.reset()
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	const gone = "0d000000-0000-4000-8000-0000000000ee"
	res, err = request(t, n, "session.retire", map[string]any{"session_id": gone})
	if err != nil || res["closed"] != false {
		t.Fatalf("not running: %v, %v", res, err)
	}
}

// session.retire stops an idle agy in its pane with the same two Ctrl+C presses and keeps the pane (#11087); a working
// one is refused, and a work agent the node cannot quit that way (cursor) is never stopped.
func TestHerdrRetireAgy(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	cwd := mkdir(t, filepath.Join(home, "proj"))
	working := agyTestAgent("w1:p2", cwd, agyOtherTestID)
	working["agent_status"] = proto.StatusWorking
	working["tab_id"] = "w1:t2"
	cursor := agentOf(cursorAgent, "", proto.StatusIdle, "w1:p3", "w1:t3", "w1", cwd, "cursor")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, cwd, agyTestID), working, cursor)
	idOf := func(pane string) string {
		n.mu.Lock()
		defer n.mu.Unlock()
		for id, a := range n.agents {
			if a.PaneID == pane {
				return id
			}
		}
		t.Fatalf("no agent in %s", pane)
		return ""
	}
	idle, busy, other := idOf(agyTestPane), idOf("w1:p2"), idOf("w1:p3")

	// The agy rows are pane-derived (otherSession), never a hex session id: still accepted while agy runs in the pane.
	if idle != otherSession(host.Agent{Agent: agyAgent, PaneID: agyTestPane}) {
		t.Fatalf("agy row id = %q", idle)
	}
	_, err := request(t, n, "session.retire", map[string]any{"session_id": busy})
	if code, _ := refusalOf(t, err); code != notRetirable {
		t.Errorf("working agy: code %q", code)
	}
	if _, err := request(t, n, "session.retire", map[string]any{"session_id": other}); err == nil {
		t.Errorf("cursor: retired")
	}
	if got := withoutAgentReports(f.trace("pane.send_keys")); len(got) != 0 {
		t.Fatalf("keys sent before the idle agy's retire: %v", got)
	}
	quitsOn(f, agyTestPane, idle, []any{working, cursor}, shellPane(agyTestPane, "w1:t1", "w1", cwd, idle))
	res, err := request(t, n, "session.retire", map[string]any{"session_id": idle})
	if err != nil || res["closed"] != true {
		t.Fatalf("retire = %v, %v", res, err)
	}
	assertTrace(t, withoutAgentReports(f.trace("pane.send_keys", "tab.close", "pane.close")), []string{
		sendKeys(agyTestPane, "ctrl+c"), sendKeys(agyTestPane, "ctrl+c"),
	})
}

// A message to a Codex Persona started that is no longer running is refused (#4754: a stopped agent takes no
// messages). The user's Resume (session.start) brings it back in a new tab of its directory's workspace, named after
// its title: its launcher's words with the model it ran with, `resume <id>`, and the start's prompt (nothing typed).
func TestHerdrResumeBringsBackAStoppedCodex(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"cx": {"codex", "-m", "gpt-6"}})
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	day := time.Now()
	dir := mkdir(t, filepath.Join(home, ".codex", "sessions", day.Format("2006"), day.Format("01"), day.Format("02")))
	now := day.UTC().Format(time.RFC3339Nano)
	meta, _ := json.Marshal(map[string]any{"timestamp": now, "type": "session_meta", "payload": map[string]any{"id": startThread, "cwd": cwd}})
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-11-02T10-00-00-"+startThread+".jsonl"), append(meta, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := request(t, n, "session.send", map[string]any{"session_id": startThread, "text": "go on"})
	if code, _ := refusalOf(t, err); code != agentStopped {
		t.Fatalf("a message to a stopped Codex: %v", err)
	}

	n.state.put(startThread, sessionState{Launcher: "cx", Cwd: cwd, Model: "gpt-7"})
	f.reset()
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": "proj"}}})
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	var mu sync.Mutex
	var file codexStartFile
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_input" {
			return
		}
		if m := startFileRE.FindStringSubmatch(str(c.Params["text"])); m != nil {
			b, _ := os.ReadFile(m[1])
			mu.Lock()
			_ = json.Unmarshal(b, &file)
			mu.Unlock()
			f.reply("session.snapshot", snapshot(nil, nil, []any{agentOf("codex", "", proto.StatusWorking, "w1:p2", "w1:t2", "w1", cwd, "")}, nil))
		}
	})
	if _, err := request(t, n, "session.start", map[string]any{"resume_session_id": startThread, "prompt": "go on"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"codex", "--model", "gpt-7", "resume", startThread}; strings.Join(file.Argv, " ") != strings.Join(want, " ") ||
		file.Prompt != "go on" || file.Agent != "codex" {
		t.Errorf("ran %q with %q (HERDR_AGENT %q)", file.Argv, file.Prompt, file.Agent)
	}
	if st, _ := n.state.get(startThread); st.Pane != "w1:p2" || st.Launcher != "cx" || st.Model != "gpt-7" {
		t.Errorf("state = %+v", st)
	}
	if a, ok := n.agent(startThread); !ok || a.PaneID != "w1:p2" {
		t.Errorf("the resumed Codex is not its session: %+v %v", a, ok)
	}
	if got := f.trace("tab.create", "pane.send_keys", "agent.prompt", "pane.send_text"); len(got) != 1 ||
		!strings.Contains(got[0], `"workspace_id":"w1"`) {
		t.Errorf("herdr calls %q", got)
	}
}

// On Orca too, retire stops a still agent with Ctrl+C and never closes its terminal, a Codex as well as a Claude: the
// terminal stays, so whether Orca could continue the session does not matter any more (#4754).
func TestOrcaRetireKeepsTheTerminal(t *testing.T) {
	withStop(t, 1, orcaProcsMaxAge+time.Second) // the node reads Orca's processes at most this often
	pid, exit := idleTerminalTestProcess(t)
	cn, co, procs := orcaSessionWithPID(t, pid)
	go func() { // claude quits once Ctrl+C has been pressed twice: its process leaves the terminal
		tick := time.NewTicker(10 * time.Millisecond) // the fake's trace signals nothing: looked at 200 times at most
		defer tick.Stop()
		for range 200 {
			if len(co.Trace("terminal.send")) >= 2 {
				exit()
				procs.set("tab1:leaf1")
				return
			}
			<-tick.C
		}
	}()
	res, err := request(t, cn, "session.retire", map[string]any{"session_id": sidA})
	if err != nil || res["closed"] != true {
		t.Fatalf("claude on Orca: %v, %v (calls %v)", res, err, co.Trace())
	}
	if c := co.Trace("terminal.close"); len(c) != 0 {
		t.Errorf("closed: %v", c)
	}
}

func TestRetireUsesSessionHostWithRuntimeDefault(t *testing.T) {
	for _, closeTerminal := range []bool{false, true} {
		action := "stop"
		if closeTerminal {
			action = "close"
		}
		t.Run(action, func(t *testing.T) {
			for _, def := range []string{proto.EnginePaseo, proto.EngineHerdr} {
				t.Run(def, func(t *testing.T) {
					withStop(t, 1, 50*time.Millisecond)
					n, f, home := herdrNode(t)
					choice := proto.EngineChoice{Hosts: []string{proto.EngineHerdr, proto.EnginePaseo}, Default: def}
					n.eng.name, n.eng.set = proto.LegacyEngine(choice), &choice
					cwd := mkdir(t, filepath.Join(home, "proj"))
					writeTranscript(t, home, cwd, sidA, time.Now(), `{"type":"assistant","message":{"role":"assistant","content":"done"},"uuid":"a1"}`)
					n.agents[sidA] = host.Agent{Agent: "claude", PaneID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1", AgentStatus: proto.StatusDone}
					n.state.put(sidA, sessionState{Pane: "w1:p1", Launcher: "cc", Cwd: cwd})
					n.runtimes = newRuntimeRegistry()
					n.runtimes.known[sidB] = knownSession{runtime: proto.EnginePaseo, label: "Paseo", at: time.Now()}
					n.agents[sidB] = host.Agent{Agent: "claude", PaneID: "w1:p9", AgentStatus: proto.StatusDone}
					n.state.put(sidB, sessionState{Pane: "w1:p9"}) // stale terminal state cannot override runtime ownership
					if !closeTerminal {
						quitsOn(f, "w1:p1", sidA, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidA))
					}
					res, err := request(t, n, "session.retire", map[string]any{"session_id": sidA, "close_terminal": closeTerminal})
					if err != nil || res["closed"] != true {
						t.Fatalf("herdr session: %v, %v", res, err)
					}
					if closeTerminal {
						if got := f.trace("tab.close"); len(got) != 1 || !strings.Contains(got[0], `"tab_id":"w1:t1"`) {
							t.Fatalf("herdr close calls: %v", got)
						}
					} else {
						closesNothing(t, f)
					}
					f.reset()
					_, err = request(t, n, "session.retire", map[string]any{"session_id": sidB, "close_terminal": closeTerminal})
					if code, _ := refusalOf(t, err); code != notRetirable {
						t.Fatalf("Paseo session: %v", err)
					}
					if got := f.trace("pane.send_keys", "tab.close"); len(got) != 0 {
						t.Fatalf("Paseo retirement touched terminals: %v", got)
					}
				})
			}
		})
	}
}

func TestRetireHerdrCoreWithRuntimeDefault(t *testing.T) {
	for _, closeTerminal := range []bool{false, true} {
		action := "stop"
		if closeTerminal {
			action = "close"
		}
		t.Run(action, func(t *testing.T) {
			withStop(t, 1, 50*time.Millisecond)
			pid, exit := idleTerminalTestProcess(t)
			n, hc := hcSessionWithPID(t, pid)
			choice := proto.EngineChoice{Hosts: []string{proto.EngineHerdrCore, proto.EnginePaseo}, Default: proto.EnginePaseo}
			n.eng.name, n.eng.set = proto.LegacyEngine(choice), &choice
			hc.handle("terminal.keys", func(map[string]any) any {
				exit()
				hc.setTerminals(hcTerminal("t1", "", "", 4243, "zsh", hcCwd, "", "", ""))
				return map[string]any{}
			})
			hc.handle("terminal.kill", func(map[string]any) any {
				k := hcTerminal("t1", "", "", 0, "", hcCwd, "", "", "")
				k["state"] = "finished"
				return k
			})
			res, err := request(t, n, "session.retire", map[string]any{"session_id": sidA, "close_terminal": closeTerminal})
			if err != nil || res["closed"] != true {
				t.Fatalf("herdr-core retirement: %v, %v", res, err)
			}
			if got := hc.trace("terminal.keys"); len(got) != 2 {
				t.Fatalf("interrupt calls: %v", got)
			}
			kills := hc.trace("terminal.kill")
			if closeTerminal {
				if len(kills) != 1 || !strings.Contains(kills[0], `"terminal":"t1"`) {
					t.Fatalf("close calls: %v", kills)
				}
			} else {
				if len(kills) != 0 {
					t.Fatalf("stopping closed terminal: %v", kills)
				}
				if _, live := n.agent(sidA); live {
					t.Fatal("agent still live after stop")
				}
			}
		})
	}
}

func TestHerdrRetireGuardedActivityVersion(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	updatedAt := time.Date(2026, 11, 26, 0, 10, 0, 0, time.UTC)
	transcriptPath := writeTranscript(t, home, cwd, sidA, updatedAt,
		`{"type":"user","cwd":"`+cwd+`","message":{"role":"user","content":"hi"},"uuid":"u1"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]},"uuid":"a1","parentUuid":"u1"}`,
		`{"type":"system","subtype":"turn_duration","uuid":"d1","parentUuid":"a1","durationMs":1000}`)
	n.state.put(sidA, sessionState{Launcher: "cc", Cwd: cwd, Model: "opus"})
	f.reply("session.snapshot", snapshot(nil, nil, []any{
		claudeAgent(sidA, proto.StatusDone, "w1:p1", "w1:t1", "w1", cwd, ""),
	}, nil))
	list := listSessions(t, n)
	var row proto.Session
	for _, s := range list {
		if s.SessionID == sidA {
			row = s
			break
		}
	}
	if row.Rev == 0 || row.UpdatedAt.IsZero() {
		t.Fatalf("expected populated rev and updated_at, got %+v", row)
	}
	waitCalls(t, f, "pane.report_agent_session", 1)
	f.reset()

	// 0. Guarded retire requires close_terminal=true.
	_, err := request(t, n, "session.retire_guarded", map[string]any{
		"session_id":          sidA,
		"close_terminal":      false,
		"expected_rev":        row.Rev,
		"has_expected_rev":    true,
		"expected_updated_at": row.UpdatedAt,
		"expected_pane_id":    "w1:p1",
	})
	if code, _ := refusalOf(t, err); code != notRetirable {
		t.Fatalf("close_terminal=false: got %v, want not_retirable", err)
	}

	// 1. Mismatched expected pane refuses without touching terminal.
	_, err = request(t, n, "session.retire_guarded", map[string]any{
		"session_id":          sidA,
		"close_terminal":      true,
		"expected_rev":        row.Rev,
		"has_expected_rev":    true,
		"expected_updated_at": row.UpdatedAt,
		"expected_pane_id":    "w1:p99",
	})
	if code, _ := refusalOf(t, err); code != notRetirable {
		t.Fatalf("pane mismatch: got %v, want not_retirable", err)
	}
	if got := withoutAgentReports(f.trace("pane.send_keys", "tab.close", "pane.close")); len(got) != 0 {
		t.Fatalf("pane mismatch touched terminal: %v", got)
	}

	// 2. Concurrent status change to working (observed via strict host refresh) refuses without touching terminal.
	f.reply("session.snapshot", snapshot(nil, nil, []any{
		claudeAgent(sidA, proto.StatusWorking, "w1:p1", "w1:t1", "w1", cwd, ""),
	}, nil))
	_, err = request(t, n, "session.retire_guarded", map[string]any{
		"session_id":          sidA,
		"close_terminal":      true,
		"expected_rev":        row.Rev,
		"has_expected_rev":    true,
		"expected_updated_at": row.UpdatedAt,
		"expected_pane_id":    "w1:p1",
	})
	if code, _ := refusalOf(t, err); code != notRetirable {
		t.Fatalf("working transition: got %v, want not_retirable", err)
	}
	if got := withoutAgentReports(f.trace("pane.send_keys", "tab.close", "pane.close")); len(got) != 0 {
		t.Fatalf("working transition touched terminal: %v", got)
	}

	// Restore idle status in host snapshot, then simulate concurrent transcript write (activity version change).
	f.reply("session.snapshot", snapshot(nil, nil, []any{
		claudeAgent(sidA, proto.StatusDone, "w1:p1", "w1:t1", "w1", cwd, ""),
	}, nil))
	listSessions(t, n)
	f.reset()

	newerAt := updatedAt.Add(time.Minute)
	writeTranscript(t, home, cwd, sidA, newerAt,
		`{"type":"user","cwd":"`+cwd+`","message":{"role":"user","content":"hi"},"uuid":"u1"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]},"uuid":"a1","parentUuid":"u1"}`,
		`{"type":"user","cwd":"`+cwd+`","message":{"role":"user","content":"new work"},"uuid":"u2"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]},"uuid":"a2","parentUuid":"u2"}`,
		`{"type":"system","subtype":"turn_duration","uuid":"d2","parentUuid":"a2","durationMs":1000}`)
	_, err = request(t, n, "session.retire_guarded", map[string]any{
		"session_id":          sidA,
		"close_terminal":      true,
		"expected_rev":        row.Rev,
		"has_expected_rev":    true,
		"expected_updated_at": row.UpdatedAt,
		"expected_pane_id":    "w1:p1",
	})
	if code, _ := refusalOf(t, err); code != notRetirable {
		t.Fatalf("concurrent transcript rev change: got %v, want not_retirable", err)
	}
	if got := withoutAgentReports(f.trace("pane.send_keys", "tab.close", "pane.close")); len(got) != 0 {
		t.Fatalf("rev change touched terminal: %v", got)
	}

	// 3. Matching fresh rev, updated_at, and pane closes cleanly and confirms guard_verified.
	st, statErr := os.Stat(transcriptPath)
	if statErr != nil {
		t.Fatal(statErr)
	}
	res, err := request(t, n, "session.retire_guarded", map[string]any{
		"session_id":          sidA,
		"close_terminal":      true,
		"expected_rev":        st.Size(),
		"has_expected_rev":    true,
		"expected_updated_at": newerAt,
		"expected_pane_id":    "w1:p1",
	})
	if err != nil || res["closed"] != true || res["guard_verified"] != true {
		t.Fatalf("matching guarded retire: %v, %v", res, err)
	}
	if got := f.trace("tab.close"); len(got) != 1 || !strings.Contains(got[0], `"tab_id":"w1:t1"`) {
		t.Fatalf("expected tab.close after matching guard, got %v", got)
	}
}

func TestHerdrRetireGuardedNamedSessionRefusesCachedFallback(t *testing.T) {
	oldWait := namedWait
	namedWait = 80 * time.Millisecond
	t.Cleanup(func() { namedWait = oldWait })

	n, f, home := herdrNode(t)
	h, fs := namedHerdr(t, f)
	n.hosts = host.NewRegistry(h)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	updatedAt := time.Date(2026, 11, 26, 0, 10, 0, 0, time.UTC)
	writeTranscript(t, home, cwd, sidB, updatedAt,
		`{"type":"user","cwd":"`+cwd+`","message":{"role":"user","content":"hi"},"uuid":"u1"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]},"uuid":"a1","parentUuid":"u1"}`,
		`{"type":"system","subtype":"turn_duration","uuid":"d1","parentUuid":"a1","durationMs":1000}`)
	n.state.put(sidB, sessionState{Launcher: "cc", Cwd: cwd, Model: "opus"})

	namedSnap := func(status string) map[string]any {
		return snapshot(
			[]any{map[string]any{"workspace_id": "w4", "label": "app", "number": 1}},
			[]any{map[string]any{"tab_id": "w4:t1", "workspace_id": "w4", "label": "main", "number": 1}},
			[]any{claudeAgent(sidB, status, "w4:p1", "w4:t1", "w4", cwd, "")},
			nil,
		)
	}
	f.reply("session.snapshot", sessionSnapshot(sidA))
	fs.build.reply("session.snapshot", namedSnap(proto.StatusDone))
	fs.review.reply("session.snapshot", snapshot(nil, nil, nil, nil))

	// Populate initial listing so build's lastLayout caches sidB as idle/done.
	m := byID(listSessions(t, n))
	row, ok := m[sidB]
	if !ok || row.PaneID != "@build:w4:p1" || row.Rev == 0 || row.UpdatedAt.IsZero() {
		t.Fatalf("unexpected initial named session row: %+v (ok=%v)", row, ok)
	}
	waitCalls(t, fs.build, "pane.report_agent_session", 1)
	f.reset()
	fs.build.reset()

	guardReq := map[string]any{
		"session_id":          sidB,
		"close_terminal":      true,
		"expected_rev":        row.Rev,
		"has_expected_rev":    true,
		"expected_updated_at": row.UpdatedAt,
		"expected_pane_id":    "@build:w4:p1",
	}

	// 1. Named session returns snapshot error while default server succeeds: must NOT fall back to lastLayout.
	fs.build.reply("session.snapshot", herdrFail{Code: "unavailable", Message: "named snapshot failed"})
	if _, err := request(t, n, "session.retire_guarded", guardReq); err == nil {
		t.Fatal("named session snapshot error unexpectedly allowed guarded retire via lastLayout")
	} else if code, _ := refusalOf(t, err); code != notRetirable {
		t.Fatalf("named session snapshot error: got %v, want not_retirable", err)
	}
	if got := withoutAgentReports(fs.build.trace("pane.send_keys", "tab.close", "pane.close")); len(got) != 0 {
		t.Fatalf("named session error touched terminal: %v", got)
	}

	// 2. Named session hangs past namedWait (timeout) while default server succeeds: must refuse without falling back to lastLayout.
	blocked, release := context.WithCancel(context.Background())
	var hungWG sync.WaitGroup
	hungWG.Add(1)
	fs.build.handle("session.snapshot", func(map[string]any) any {
		defer hungWG.Done()
		<-blocked.Done()
		return namedSnap(proto.StatusDone)
	})
	if _, err := request(t, n, "session.retire_guarded", guardReq); err == nil {
		release()
		hungWG.Wait()
		t.Fatal("named session timeout unexpectedly allowed guarded retire via lastLayout")
	} else if code, _ := refusalOf(t, err); code != notRetirable {
		release()
		hungWG.Wait()
		t.Fatalf("named session timeout: got %v, want not_retirable", err)
	}
	release()
	hungWG.Wait()
	if got := withoutAgentReports(fs.build.trace("pane.send_keys", "tab.close", "pane.close")); len(got) != 0 {
		t.Fatalf("named session timeout touched terminal: %v", got)
	}

	// 3. Named session busy (in-flight query already holds ns.busy) while default server succeeds: must refuse without falling back to lastLayout.
	fs.build.reply("session.snapshot", namedSnap(proto.StatusDone))
	ns := h.named.lookup(context.Background(), "build")
	if ns == nil {
		t.Fatal("expected named session build in registry")
	}
	ns.busy.Store(true)
	_, err := request(t, n, "session.retire_guarded", guardReq)
	ns.busy.Store(false)
	if code, _ := refusalOf(t, err); code != notRetirable {
		t.Fatalf("named session busy: got %v, want not_retirable", err)
	}
	if got := withoutAgentReports(fs.build.trace("pane.send_keys", "tab.close", "pane.close")); len(got) != 0 {
		t.Fatalf("named session busy touched terminal: %v", got)
	}

	// 4. Named session freshly reports StatusWorking even though transcript Rev did not change yet: must refuse.
	fs.build.reply("session.snapshot", namedSnap(proto.StatusWorking))
	if _, err := request(t, n, "session.retire_guarded", guardReq); err == nil {
		t.Fatal("named session resumed working unexpectedly allowed guarded retire")
	} else if code, _ := refusalOf(t, err); code != notRetirable {
		t.Fatalf("named session working: got %v, want not_retirable", err)
	}
	if got := withoutAgentReports(fs.build.trace("pane.send_keys", "tab.close", "pane.close")); len(got) != 0 {
		t.Fatalf("named session working touched terminal: %v", got)
	}

	// 5. Named session returns to StatusDone and freshly responds with matching rev/updated_at/pane: closes @build:w4:p1 cleanly.
	fs.build.reply("session.snapshot", namedSnap(proto.StatusDone))
	listSessions(t, n)
	f.reset()
	fs.build.reset()
	res, err := request(t, n, "session.retire_guarded", guardReq)
	if err != nil || res["closed"] != true || res["guard_verified"] != true {
		t.Fatalf("fresh matching named session retire: %v, %v", res, err)
	}
	if got := fs.build.trace("tab.close"); len(got) != 1 || !strings.Contains(got[0], `"tab_id":"w4:t1"`) {
		t.Fatalf("expected tab.close on build session, got %v", got)
	}
	if got := f.trace("tab.close", "pane.close"); len(got) != 0 {
		t.Fatalf("default server terminal unexpectedly touched: %v", got)
	}
}

// A runtime's session that no runtime lists any more (its companion restarted, the conversation still on disk) is not
// running here: session.stop and session.retire answer so, instead of refusing its id for not being a hex one, so a
// continuation that stops its to-do's old Agent first can go on (#16376). An id of another shape is still refused.
func TestStopOfAnUnlistedRuntimeSessionIsNotRunning(t *testing.T) {
	n, f, _ := herdrNode(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	const unlisted = "agy-b-a489e5d4-d97e-4a1e-a56b-7e2262c8ddea"
	res, err := request(t, n, "session.stop", map[string]any{"session_id": unlisted})
	if err != nil || res["stopped"] != false {
		t.Fatalf("stop = %v, %v; want not running", res, err)
	}
	res, err = request(t, n, "session.retire", map[string]any{"session_id": unlisted})
	if err != nil || res["closed"] != false {
		t.Fatalf("retire = %v, %v; want not running", res, err)
	}
	if got := withoutAgentReports(f.trace("pane.send_keys", "tab.close", "pane.close")); len(got) != 0 {
		t.Fatalf("keys or closes sent for a session nothing runs: %v", got)
	}
	if _, err := request(t, n, "session.stop", map[string]any{"session_id": "../etc/passwd"}); err == nil || !strings.Contains(err.Error(), "invalid session id") {
		t.Fatalf("stop of a path-shaped id = %v; want refused", err)
	}
}
