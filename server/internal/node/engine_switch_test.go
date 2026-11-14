package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Switching a running terminal Agent between Claude Code and Codex with the user's next message (engine_switch.go,
// #5054), against a fake herdr as launcher_switch_test.go does. The handoff's text is engine_handoff.go's: these tests
// look at the block it is carried in and at the user's words after it, not at its wording.

// switchedThread is the Codex thread a switch to Codex starts.
const switchedThread = "01a19a00-0000-7000-8000-000000005054"

// tellTo is the user's message from the chat (apply_switch) to session id.
func tellTo(t *testing.T, n *Node, id, text string) error {
	t.Helper()
	_, err := request(t, n, "session.send", map[string]any{"session_id": id, "text": text, "apply_switch": true})
	return err
}

// writeThread writes Codex thread id's rollout in ~/.codex: its session_meta in cwd, and first as its first message
// when not empty.
func writeThread(t *testing.T, home, id, cwd, first string) {
	t.Helper()
	day := time.Now()
	dir := mkdir(t, filepath.Join(home, ".codex", "sessions", day.Format("2006"), day.Format("01"), day.Format("02")))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	meta, _ := json.Marshal(map[string]any{"timestamp": now, "type": "session_meta", "payload": map[string]any{"id": id, "cwd": cwd,
		"originator": "codex-tui", "source": "cli"}})
	lines := string(meta) + "\n"
	if first != "" {
		user, _ := json.Marshal(map[string]any{"timestamp": now, "type": "event_msg", "payload": map[string]any{"type": "item_completed",
			"item": map[string]any{"type": "UserMessage", "id": "u1", "content": []any{map[string]any{"type": "text", "text": first}}}}})
		lines += string(user) + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-11-12T10-00-00-"+id+".jsonl"), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
}

// codexComesUp plays Codex in pane: once codex-start is typed there, it keeps what the file held (the words, message
// and directory), and, when up, the thread shows: a new one (switchedThread, its rollout beginning with the file's
// message) or the one resumed, listed in the pane from then on.
func codexComesUp(t *testing.T, f *fakeHerdr, home, pane string, up bool) func() []codexStartFile {
	t.Helper()
	var mu sync.Mutex
	var files []codexStartFile
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_input" || str(c.Params["pane_id"]) != pane {
			return
		}
		m := startFileRE.FindStringSubmatch(str(c.Params["text"]))
		if m == nil {
			return
		}
		var file codexStartFile
		if b, err := os.ReadFile(m[1]); err != nil || json.Unmarshal(b, &file) != nil {
			return
		}
		mu.Lock()
		files = append(files, file)
		mu.Unlock()
		if !up {
			return
		}
		thread := switchedThread
		if i := strings.Index(strings.Join(file.Argv, " "), " resume "); i >= 0 {
			thread = file.Argv[len(file.Argv)-1]
		} else {
			writeThread(t, home, thread, file.Dir, file.Prompt)
		}
		f.reply("session.snapshot", snapshot(nil, nil, []any{agentOf("codex", thread, proto.StatusIdle, pane, "w1:t1", "w1", file.Dir, "")}, nil))
	})
	return func() []codexStartFile {
		mu.Lock()
		defer mu.Unlock()
		return append([]codexStartFile(nil), files...)
	}
}

// claudeComesUp lists the claude a start types into pane w1:p1 as the session it starts (--session-id or --resume),
// its empty input box on the screen, when up says yes to the typed command.
func claudeComesUp(t *testing.T, f *fakeHerdr, cwd string, up func(typed string) bool) {
	t.Helper()
	plain, ansi := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
	comesUpWhen(f, "w1:p1", up)
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_input" || str(c.Params["pane_id"]) != "w1:p1" {
			return
		}
		typed := str(c.Params["text"])
		if !up(typed) {
			return
		}
		id := ""
		if m := sessionIDArg.FindStringSubmatch(typed); m != nil {
			id = m[1]
		} else if i := strings.Index(typed, "--resume "); i >= 0 {
			id = strings.Fields(typed[i+len("--resume "):])[0]
		}
		if id != "" {
			f.setScreens("w1:p1", plain, ansi)
			f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(id, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")}, nil))
		}
	})
}

// promptsTo are the texts the fake herdr was asked to prompt pane w1:p1 with.
func promptsTo(f *fakeHerdr) []string {
	var out []string
	for _, c := range f.calls() {
		if c.Method == "agent.prompt" && str(c.Params["target"]) == "w1:p1" {
			out = append(out, str(c.Params["text"]))
		}
	}
	return out
}

// checkHandoffBlock checks that prompt is the handoff block (from, to, model, resumed) followed by the user's words text.
func checkHandoffBlock(t *testing.T, prompt, from, to, model string, resumed bool, text string) {
	t.Helper()
	head := `<persona-context kind="engine-switch" title="Handoff" from="` + from + `" to="` + to + `"`
	if model != "" {
		head += ` model="` + model + `"`
	}
	if resumed {
		head += ` resumed="1"`
	}
	head += ">\n"
	if !strings.HasPrefix(prompt, head) {
		t.Fatalf("the first message does not start with the block %q:\n%s", head, prompt)
	}
	if !strings.HasSuffix(prompt, "</persona-context>\n\n"+text) {
		t.Fatalf("the user's words do not follow the block:\n%s", prompt)
	}
}

// longTurn is finishedTurn with a reply long enough for the transcript to be listed once nothing runs it (an offline
// row: a transcript under 2 KB is a probe's, scanOffline).
func longTurn(cwd string) []string {
	return []string{`{"type":"user","cwd":"` + cwd + `","message":{"role":"user","content":"hi"},"uuid":"u1"}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + strings.Repeat("hello ", 500) + `"}]},"uuid":"a1","parentUuid":"u1"}`,
		`{"type":"system","subtype":"turn_duration","uuid":"d1","parentUuid":"a1","durationMs":1000}`}
}

// codexSwitchNode is switchNode after a switch to Codex: Claude Code session sidA (its transcript in ~/proj, its
// record on plain claude) is the conversation's earlier stretch, until an hour ago, and Codex thread sidB runs in pane
// w1:p1 now, Persona's on plain codex, its record naming sidA's stretch. The pane's screen is blank.
func codexSwitchNode(t *testing.T) (n *Node, f *fakeHerdr, home, cwd string, until time.Time) {
	t.Helper()
	n, f, home, cwd, _ = switchNode(t, proto.StatusIdle, longTurn)
	until = time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	n.state.put(sidB, sessionState{Launcher: "codex", Cwd: cwd, StartedAt: until,
		EngineHistory: []proto.EngineLeg{{SessionID: sidA, Agent: "claude", Until: until}}})
	// the Codex stretch's own words: what a switch back hands over (engineHandoff reads them)
	writeRollout(t, home, sidB, cwd, until, until.Add(time.Minute),
		`{"timestamp":"`+until.Add(10*time.Second).Format(time.RFC3339)+`","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"make it faster"}]}}}`,
		`{"timestamp":"`+until.Add(20*time.Second).Format(time.RFC3339)+`","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":"m1","content":[{"type":"text","text":"Cached the lookup."}]}}}`)
	f.reply("session.snapshot", snapshot(nil, nil, []any{agentOf("codex", sidB, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")}, nil))
	f.setScreens("w1:p1", "", "")
	listSessions(t, n)
	f.reset()
	return n, f, home, cwd, until
}

// Picking a launcher of the other engine keeps the switch pending, naming that engine, on the record and the row,
// with nothing done in the terminal; the session's own launcher drops it.
func TestEngineSwitchIsPendingUntilCancelled(t *testing.T) {
	n, f, _, _, persisted := switchNode(t, proto.StatusIdle, nil)
	if err := pick(t, n, map[string]any{"launcher": "cx", "from": "claude", "model": "gpt-6", "effort": "high"}); err != nil {
		t.Fatal(err)
	}
	if got := drove(f); len(got) != 0 {
		t.Fatalf("picking drove the terminal: %q", got)
	}
	ps := rowOf(t, n, sidA).PendingSwitch
	if ps == nil || ps.Launcher != "cx" || ps.Agent != "codex" || ps.Model != "gpt-6" || ps.Effort != "high" || ps.At.IsZero() {
		t.Fatalf("row's pending switch = %+v", ps)
	}
	if puts := persisted(); len(puts) == 0 || puts[len(puts)-1].PendingSwitch == nil || puts[len(puts)-1].PendingSwitch.Agent != "codex" {
		t.Fatalf("the switch was not kept with the record: %+v", puts)
	}
	if err := pick(t, n, map[string]any{"launcher": "claude", "from": "claude"}); err != nil {
		t.Fatal(err)
	}
	if ps := n.pendingSwitch(sidA); ps != nil {
		t.Fatalf("its own launcher left the switch pending: %+v", ps)
	}
	if got := drove(f); len(got) != 0 {
		t.Fatalf("cancelling drove the terminal: %q", got)
	}
}

// What a switch to the other engine is refused for when it is picked, with nothing kept and nothing typed.
func TestEngineSwitchRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, n *Node, f *fakeHerdr, cwd string)
		params map[string]any
		code   string
	}{
		{"launcher not on this computer", nil, map[string]any{"launcher": "nowhere"}, proto.CodeSwitchLauncherMissing},
		{"an assistant conversation", func(t *testing.T, n *Node, f *fakeHerdr, cwd string) {
			n.mu.Lock()
			n.procs[sidA] = &assistantProc{}
			n.mu.Unlock()
		}, map[string]any{"launcher": "cx"}, proto.CodeSwitchUnsupported},
		{"a runtime's session", func(t *testing.T, n *Node, f *fakeHerdr, cwd string) {
			if n.runtimes == nil {
				n.runtimes = newRuntimeRegistry()
			}
			n.runtimes.mu.Lock()
			if n.runtimes.known == nil {
				n.runtimes.known = map[string]knownSession{}
			}
			n.runtimes.known[sidA] = knownSession{runtime: "rt", label: "Runtime", at: time.Now()}
			n.runtimes.mu.Unlock()
		}, map[string]any{"launcher": "cx"}, proto.CodeSwitchUnsupported},
		{"a session Persona cannot tell the launcher of", func(t *testing.T, n *Node, f *fakeHerdr, cwd string) {
			n.state.mu.Lock()
			delete(n.state.Sessions, sidA)
			n.state.mu.Unlock()
		}, map[string]any{"launcher": "cx"}, proto.CodeSwitchUnsupported},
		{"a model the Codex launcher would not run", nil, map[string]any{"launcher": "cx", "model": "-rf"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, _, cwd, _ := switchNode(t, proto.StatusIdle, nil)
			if tc.setup != nil {
				tc.setup(t, n, f, cwd)
			}
			f.reset()
			err := pick(t, n, tc.params)
			if tc.code == "" {
				if err == nil || !strings.Contains(err.Error(), "invalid model") {
					t.Fatalf("refused with %v", err)
				}
			} else if code, data := refusalOf(t, err); code != tc.code || data["delivered"] != deliveredNone {
				t.Fatalf("refused with %q (%v), want %q", code, err, tc.code)
			}
			if ps := n.pendingSwitch(sidA); ps != nil {
				t.Errorf("a refused switch was kept: %+v", ps)
			}
			if got := drove(f); len(got) != 0 {
				t.Errorf("a refused switch drove the terminal: %q", got)
			}
		})
	}
}

// A Codex session picking another Codex launcher stays refused (one engine's launchers switch within Claude Code
// only, #5035); picking a Claude Code launcher is a switch to it.
func TestEngineSwitchOfACodexSession(t *testing.T) {
	n, _, _, _, _ := codexSwitchNode(t)
	err := pickFor(t, n, sidB, map[string]any{"launcher": "cx", "from": "codex"})
	if code, _ := refusalOf(t, err); code != proto.CodeSwitchUnsupported {
		t.Fatalf("another Codex launcher: %v", err)
	}
	if err := pickFor(t, n, sidB, map[string]any{"launcher": "claude", "from": "codex"}); err != nil {
		t.Fatal(err)
	}
	if ps := n.pendingSwitch(sidB); ps == nil || ps.Agent != "claude" || ps.Launcher != "claude" {
		t.Fatalf("pending switch = %+v", ps)
	}
}

// pickFor is POST …/modes with params for session id.
func pickFor(t *testing.T, n *Node, id string, params map[string]any) error {
	t.Helper()
	p := map[string]any{"session_id": id}
	for k, v := range params {
		p[k] = v
	}
	_, err := request(t, n, "session.modes", p)
	return err
}

// The user's next message moves the conversation to Codex: the claude is quit in its pane and Codex started in the
// same shell through codex-start, in the conversation's folder, with the handoff block and then the user's words as
// its first message (nothing typed a second time). The new thread's record names the Claude Code stretch it continues,
// the old session's names none, and the list shows the conversation once: the Codex row, with the conversation's
// title. The to-do the claude's pane was linked to is handed on (persona_from), and the pane Persona opened for the
// old session is marked as the new one's.
func TestEngineSwitchToCodexOnTheUsersNextMessage(t *testing.T) {
	n, f, home, cwd, _ := switchNode(t, proto.StatusIdle, longTurn)
	agent := claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")
	agent["tokens"] = map[string]any{"persona_todo_ref": "todo-1@m1/" + sidA, personaTabToken: sidA}
	f.reply("session.snapshot", snapshot(nil, nil, []any{agent}, nil))
	title := rowOf(t, n, sidA).Title
	if err := pick(t, n, map[string]any{"launcher": "cx", "model": "gpt-6"}); err != nil {
		t.Fatal(err)
	}
	f.reset()
	quitsToShell(f, cwd)
	files := codexComesUp(t, f, home, "w1:p1", true)
	before := time.Now().UTC()
	if err := tell(t, n, "hello", true); err != nil {
		t.Fatalf("send: %v\n%s", err, strings.Join(f.trace(), "\n"))
	}
	if got := f.trace("tab.create", "workspace.create", "tab.close", "pane.close"); len(got) != 0 {
		t.Errorf("a tab was opened or closed: %q", got)
	}
	if starts := typedStarts(f, "w1:p1"); len(starts) != 1 || !startFileRE.MatchString(starts[0]) {
		t.Fatalf("typed %q", starts)
	}
	if got := promptsTo(f); len(got) != 0 {
		t.Errorf("the message was typed again: %q", got)
	}
	got := files()
	if len(got) != 1 {
		t.Fatalf("codex-start files: %+v", got)
	}
	if got[0].Dir != cwd || !strings.Contains(strings.Join(got[0].Argv, " "), " launch cx --model gpt-6") {
		t.Errorf("codex-start runs %q in %q", got[0].Argv, got[0].Dir)
	}
	checkHandoffBlock(t, got[0].Prompt, "claude", "codex", "gpt-6", false, "hello")
	marks := strings.Join(f.trace("pane.report_metadata"), "\n")
	if !strings.Contains(marks, `"persona_from":"todo-1@m1/`+sidA+`"`) || !strings.Contains(marks, `"persona_tab":"`+switchedThread+`"`) {
		t.Errorf("the pane's marks:\n%s", marks)
	}
	st, ok := n.state.get(switchedThread)
	if !ok || len(st.EngineHistory) != 1 || st.EngineHistory[0].SessionID != sidA || st.EngineHistory[0].Agent != "claude" ||
		st.EngineHistory[0].Until.Before(before) || st.PendingSwitch != nil || st.Launcher != "cx" || st.Model != "gpt-6" {
		t.Fatalf("the new thread's record = %+v (%v)", st, ok)
	}
	if old, _ := n.state.get(sidA); old.EngineHistory != nil || old.PendingSwitch != nil {
		t.Errorf("the old session's record = %+v", old)
	}
	list := byID(listSessions(t, n))
	if _, listed := list[sidA]; listed {
		t.Error("the Claude Code session the conversation ran in is still listed")
	}
	row, ok := list[switchedThread]
	if !ok || len(row.EngineHistory()) != 1 || row.EngineHistory()[0].SessionID != sidA || row.Title != title {
		t.Fatalf("the conversation's row = %+v (history %+v, listed %v), want title %q", row, row.EngineHistory(), ok, title)
	}
	if n.switches().inFlight(sidA) || n.pendingSwitch(sidA) != nil {
		t.Error("the switch is left under way or pending")
	}
}

// Back to Claude Code: the conversation's earlier Claude Code session is resumed (--resume) in the same shell, with
// the block saying so (resumed) and the user's words after it; its record carries both earlier stretches, itself
// among them, and it is listed, its own earlier stretch naming it notwithstanding.
func TestEngineSwitchBackResumesTheEarlierSession(t *testing.T) {
	n, f, _, cwd, until := codexSwitchNode(t)
	// Persona joined the Codex thread on its app-server: it does not join it again once the conversation moved on.
	n.codices = newCodexRegistry()
	n.codices.scan = func() []codexProcess { return nil }
	n.codices.checked = func() ([]codexProcess, bool) { return nil, true }
	n.configureCodex(proto.CodexConfig{}) // ~/.codex is the legacy home: its thread ids are the session ids
	n.codices.mu.Lock()
	n.codices.config.Joined = map[string]string{sidB: "daemon"}
	n.codices.mu.Unlock()
	listSessions(t, n)
	f.reset()
	n.state.setPendingSwitch(sidB, &proto.PendingSwitch{Launcher: "claude", Agent: "claude", At: time.Now()})
	quitsOn(f, "w1:p1", sidB, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidB))
	claudeComesUp(t, f, cwd, func(string) bool { return true })
	if err := tellTo(t, n, sidB, "and now?"); err != nil {
		t.Fatalf("send: %v\n%s", err, strings.Join(f.trace(), "\n"))
	}
	starts := typedStarts(f, "w1:p1")
	if len(starts) != 1 || !strings.Contains(starts[0], "--resume "+sidA) {
		t.Fatalf("typed %q", starts)
	}
	got := promptsTo(f)
	if len(got) == 0 || len(got) > 1 && got[1] != got[0] { // typed again, the same, while the transcript does not show it
		t.Fatalf("prompts %q", got)
	}
	checkHandoffBlock(t, got[0], "codex", "claude", "", true, "and now?")
	for _, words := range []string{"make it faster", "Cached the lookup."} { // the Codex stretch, handed back
		if !strings.Contains(got[0], words) {
			t.Errorf("the handoff leaves out %q:\n%s", words, got[0])
		}
	}
	st, _ := n.state.get(sidA)
	if len(st.EngineHistory) != 2 || st.EngineHistory[0] != (proto.EngineLeg{SessionID: sidA, Agent: "claude", Until: until}) ||
		st.EngineHistory[1].SessionID != sidB || st.EngineHistory[1].Agent != "codex" || st.Launcher != "claude" {
		t.Fatalf("the resumed session's record = %+v", st)
	}
	if b, _ := n.state.get(sidB); b.EngineHistory != nil || b.PendingSwitch != nil {
		t.Errorf("the Codex thread's record = %+v", b)
	}
	n.codices.mu.Lock()
	_, rejoins := n.codices.config.Joined[sidB]
	n.codices.mu.Unlock()
	if rejoins {
		t.Error("the Codex thread the conversation moved on from is still joined again on a reconnect")
	}
	if row := rowOf(t, n, sidA); len(row.EngineHistory()) != 2 {
		t.Errorf("the conversation's row has history %+v", row.EngineHistory())
	}
}

// An earlier session that does not come up gives way to a fresh one, once: the block then says the conversation goes
// on from the summary (not resumed), and the earlier session's record is put back as it was.
func TestEngineSwitchFallsBackToAFreshSession(t *testing.T) {
	n, f, _, cwd, _ := codexSwitchNode(t)
	n.claudeStart, n.claudeReady = time.Second, time.Second
	before, _ := n.state.get(sidA)
	n.state.setPendingSwitch(sidB, &proto.PendingSwitch{Launcher: "claude", Agent: "claude", At: time.Now()})
	quitsOn(f, "w1:p1", sidB, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidB))
	claudeComesUp(t, f, cwd, func(typed string) bool { return !strings.Contains(typed, "--resume") })
	if err := tellTo(t, n, sidB, "and now?"); err != nil {
		t.Fatalf("send: %v\n%s", err, strings.Join(f.trace(), "\n"))
	}
	starts := typedStarts(f, "w1:p1")
	if len(starts) != 2 || !strings.Contains(starts[0], "--resume "+sidA) || !sessionIDArg.MatchString(starts[1]) {
		t.Fatalf("typed %q", starts)
	}
	got := promptsTo(f)
	if len(got) == 0 || len(got) > 1 && got[1] != got[0] {
		t.Fatalf("prompts %q", got)
	}
	checkHandoffBlock(t, got[0], "codex", "claude", "", false, "and now?")
	for _, words := range []string{"make it faster", "Cached the lookup."} { // the Codex stretch, handed back
		if !strings.Contains(got[0], words) {
			t.Errorf("the handoff leaves out %q:\n%s", words, got[0])
		}
	}
	fresh := sessionIDArg.FindStringSubmatch(starts[1])[1]
	if st, _ := n.state.get(fresh); len(st.EngineHistory) != 2 || st.EngineHistory[1].SessionID != sidB {
		t.Errorf("the fresh session's record = %+v", st)
	}
	if st, _ := n.state.get(sidA); st.EngineHistory != nil || st.Cwd != before.Cwd || st.Launcher != before.Launcher {
		t.Errorf("the earlier session's record = %+v, was %+v", st, before)
	}
}

// Codex that does not come up rolls the switch back: the pane (the user's terminal, adopted) stays open, the claude is
// resumed on its own launcher in the same shell, the message is refused with nothing typed (switch_failed), and the
// switch is dropped.
func TestEngineSwitchFailureRollsBack(t *testing.T) {
	old := codexStartWait
	codexStartWait = time.Second
	t.Cleanup(func() { codexStartWait = old })
	n, f, home, cwd, _ := switchNode(t, proto.StatusIdle, nil)
	n.state.setPendingSwitch(sidA, &proto.PendingSwitch{Launcher: "cx", Agent: "codex", At: time.Now()})
	f.reset()
	quitsToShell(f, cwd)
	codexComesUp(t, f, home, "w1:p1", false)
	claudeComesUp(t, f, cwd, func(typed string) bool { return strings.Contains(typed, "--resume "+sidA) })
	err := tell(t, n, "hello", true)
	if code, data := refusalOf(t, err); code != proto.CodeSwitchFailed || data["delivered"] != deliveredNone {
		t.Fatalf("refused with %q (%v)", code, err)
	}
	if got := f.trace("tab.close", "pane.close"); len(got) != 0 {
		t.Errorf("closed the terminal: %q", got)
	}
	starts := typedStarts(f, "w1:p1")
	if len(starts) != 2 || !startFileRE.MatchString(starts[0]) || !strings.Contains(starts[1], "--resume "+sidA) {
		t.Fatalf("typed %q", starts)
	}
	if got := promptsTo(f); len(got) != 0 {
		t.Errorf("the message was typed: %q", got)
	}
	if st, _ := n.state.get(sidA); st.Launcher != "claude" || st.PendingSwitch != nil || st.EngineHistory != nil {
		t.Errorf("record after the failed switch = %+v", st)
	}
	if n.pendingSwitch(sidA) != nil || n.switches().inFlight(sidA) {
		t.Error("the failed switch still waits, or is left under way")
	}
}

// While the agent is busy the user's message goes to it as always and the switch waits; a message with photos is
// refused rather than its photos lost, the switch still waiting.
func TestEngineSwitchWaitsWhileBusy(t *testing.T) {
	n, f, _, _, _ := switchNode(t, proto.StatusWorking, nil)
	n.state.setPendingSwitch(sidA, &proto.PendingSwitch{Launcher: "cx", Agent: "codex", At: time.Now()})
	f.reset()
	if err := tell(t, n, "hello", true); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := f.trace("pane.send_keys", "pane.send_input"); len(got) != 0 {
		t.Errorf("quit or restarted the agent: %q", got)
	}
	if got := promptsTo(f); len(got) != 1 || got[0] != "hello" {
		t.Errorf("the message did not go to the agent: %q", got)
	}
	if ps := n.pendingSwitch(sidA); ps == nil || ps.Agent != "codex" {
		t.Errorf("the switch no longer waits: %+v", ps)
	}

	n, f, _, _, _ = switchNode(t, proto.StatusIdle, nil)
	n.state.setPendingSwitch(sidA, &proto.PendingSwitch{Launcher: "cx", Agent: "codex", At: time.Now()})
	f.reset()
	_, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "look", "apply_switch": true,
		"images": []any{map[string]any{"mime": "image/png", "data": "aGk="}}})
	if code, data := refusalOf(t, err); code != proto.CodeSwitchUnsupported || data["delivered"] != deliveredNone {
		t.Fatalf("a message with photos: %q (%v)", code, err)
	}
	if got := drove(f); len(got) != 0 {
		t.Errorf("drove the terminal: %q", got)
	}
	if n.pendingSwitch(sidA) == nil {
		t.Error("the switch no longer waits")
	}
}

// The list leaves out every session named in another listed row's history, never the row itself, and of two records
// that still name each other the one switched away from last wins; the row shows its first stretch's title unless its
// own is manual.
func TestJoinStretches(t *testing.T) {
	t1 := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	off := proto.StatusOffline
	list := []proto.Session{{SessionID: sidA, Title: "first", Status: off}, {SessionID: sidB, Title: "codex", Status: off},
		{SessionID: sidC, Title: "other", Status: off}}
	chains := map[string][]proto.EngineLeg{
		sidA: {{SessionID: sidA, Agent: "claude", Until: t1}, {SessionID: sidB, Agent: "codex", Until: t2}},
		sidB: {{SessionID: sidA, Agent: "claude", Until: t1}}, // written before the switch back, not cleared since
	}
	hidden := joinStretches(list, chains)
	if !hidden[sidB] || hidden[sidA] || hidden[sidC] {
		t.Fatalf("hidden = %v", hidden)
	}
	if list[0].Title != "first" {
		t.Errorf("the conversation's own first stretch changed its title: %q", list[0].Title)
	}
	list = []proto.Session{{SessionID: sidA, Title: "first", Status: off}, {SessionID: sidB, Title: "codex", Status: off},
		{SessionID: sidC, Title: "mine", TitleManual: true}}
	chains = map[string][]proto.EngineLeg{sidB: {{SessionID: sidA, Agent: "claude", Until: t1}},
		sidC: {{SessionID: sidA, Agent: "claude", Until: t1}}}
	joinStretches(list, chains)
	if list[1].Title != "first" || list[2].Title != "mine" {
		t.Errorf("titles = %q, %q", list[1].Title, list[2].Title)
	}
	// Only a parked row goes (idle, done, offline, cut off, with no pane): not one that runs (a pane, a claude process,
	// working, unknown) or waits for the user (blocked); the row of a switch moving on (starting, a switch to the other
	// engine pending) does.
	switching := &proto.PendingSwitch{Launcher: "cx", Agent: "codex"}
	for _, tc := range []struct {
		name string
		row  proto.Session
		hide bool
	}{
		{"offline", proto.Session{Status: off}, true},
		{"interrupted", proto.Session{Status: proto.StatusInterrupted}, true},
		// the Codex thread an app-server keeps loaded after a switch to Claude Code: a message to it would fork the conversation
		{"loaded on an app-server", proto.Session{Status: proto.StatusDone, SourceKind: "codex_app_server",
			Capabilities: &proto.SessionCapabilities{Read: true, Send: true}}, true},
		{"idle with no pane", proto.Session{Status: proto.StatusIdle}, true},
		{"in a pane", proto.Session{Status: proto.StatusIdle, PaneID: "w1:p2"}, false},
		{"offline, but a pane's", proto.Session{Status: off, PaneID: "w1:p2"}, false},
		{"a claude in another terminal", proto.Session{Status: proto.StatusIdle, SourceKind: proto.SourceClaudeTerminal}, false},
		{"working elsewhere", proto.Session{Status: proto.StatusWorking}, false},
		{"blocked", proto.Session{Status: proto.StatusBlocked}, false},
		{"unknown", proto.Session{Status: proto.StatusUnknown}, false},
		{"quit as the conversation left it", proto.Session{Status: off, UpdatedAt: t1.Add(10 * time.Second)}, true},
		// resumed outside Persona's panes (an external Codex CLI, the Codex App) after the conversation moved on
		{"used since the conversation left it", proto.Session{Status: proto.StatusDone, SourceKind: "codex_history",
			UpdatedAt: t1.Add(10 * time.Minute)}, false},
		{"switching on", proto.Session{Status: proto.StatusIdle, Starting: true, PendingSwitch: switching}, true},
		{"starting", proto.Session{Status: proto.StatusIdle, Starting: true}, false},
	} {
		tc.row.SessionID = sidA
		list := []proto.Session{tc.row, {SessionID: sidB, Status: proto.StatusIdle, PaneID: "w1:p1"}}
		hidden := joinStretches(list, map[string][]proto.EngineLeg{sidB: {{SessionID: sidA, Agent: "claude", Until: t1}}})
		if hidden[sidA] != tc.hide || hidden[sidB] {
			t.Errorf("%s: hidden = %v", tc.name, hidden)
		}
	}
}

// codex-start runs the words in the file's directory (Dir), whatever the directory it is started in, and says so in
// PWD.
func TestCodexStartRunsInItsDirectory(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir, tmp := t.TempDir(), t.TempDir()
	want, _ := filepath.EvalSymlinks(dir)
	file, out := filepath.Join(tmp, "start.json"), filepath.Join(tmp, "out")
	b, _ := json.Marshal(codexStartFile{Argv: []string{"OUT=" + out, sh, "-c", `printf '%s|%s' "$(pwd -P)" "$PWD" > "$OUT"`}, Dir: dir})
	if err := os.WriteFile(file, b, 0o600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal([]string{file})
	cmd := exec.Command(os.Args[0], "-test.run=^TestCodexStartHelper$")
	cmd.Dir = tmp
	cmd.Env = append(os.Environ(), "PWD="+tmp, "PERSONA_CODEX_START_HELPER=1", "PERSONA_CODEX_START_ARGS="+string(args))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if real, pwd, _ := strings.Cut(string(got), "|"); real != want || pwd != dir {
		t.Fatalf("ran in %q (PWD %q), want %q (%q)", real, pwd, want, dir)
	}
}

// A switch to the other engine whose connection went away hands on the record of the session it started too (its
// last stretch names the switch's session), as it does its own session's; any other record is not a switch's.
func TestSwitchShareKeepsTheRecordOfTheSessionAnEngineSwitchStarts(t *testing.T) {
	old, _ := testNode(t)
	share := old.switches()
	share.begin(sidA, proto.Session{SessionID: sidA, Starting: true})
	defer share.end(sidA)
	started := sessionState{Launcher: "cx", Cwd: "/w", EngineHistory: []proto.EngineLeg{{SessionID: sidA, Agent: "claude", Until: time.Now()}}}
	if !share.keep(old, switchedThread, started) {
		t.Fatal("the started session's record was not kept")
	}
	if share.keep(old, sidC, sessionState{Launcher: "claude", Cwd: "/w"}) {
		t.Error("another session's record was kept")
	}
	next, _ := testNode(t)
	next.share.Store(share)
	share.receive(next)
	if st, ok := next.state.get(switchedThread); !ok || len(st.EngineHistory) != 1 || st.EngineHistory[0].SessionID != sidA {
		t.Fatalf("the next Node has %+v (%v)", st, ok)
	}
}

// The model a launcher names for itself goes into the handoff block for the apps to show: the list's placeholder for a
// launcher that names none is not a model, so the block names none then.
func TestLauncherDefaultModelSkipsThePlaceholder(t *testing.T) {
	n, _ := testNode(t)
	launcherCache.Lock()
	oldAt, oldList := launcherCache.at, launcherCache.list
	launcherCache.at = time.Now()
	launcherCache.list = []proto.Launcher{{Name: "codex", Model: "默认", Agent: "codex"}, {Name: "cx", Model: "gpt-6", Agent: "codex"}} // i18n-ignore: the node's own placeholder
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.at, launcherCache.list = oldAt, oldList
		launcherCache.Unlock()
	})
	if got := n.launcherDefaultModel("codex"); got != "" {
		t.Errorf("the placeholder came back as a model: %q", got)
	}
	if got := n.launcherDefaultModel("cx"); got != "gpt-6" {
		t.Errorf("cx's own model = %q, want gpt-6", got)
	}
	if got := n.launcherDefaultModel("missing"); got != "" {
		t.Errorf("a launcher not in the list named %q", got)
	}
}

// codexReadsAndRuns plays a Codex that codex-start started in pane w1:p1 but that never shows a thread (a dialog it
// waits on, a thread not seen in time): the line's file is claimed (as codex-start does), and from then on the pane
// runs codex in front of its shell. It returns what the files held.
func codexReadsAndRuns(f *fakeHerdr) func() []codexStartFile {
	var mu sync.Mutex
	running := false
	var files []codexStartFile
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_input" || str(c.Params["pane_id"]) != "w1:p1" {
			return
		}
		if m := startFileRE.FindStringSubmatch(str(c.Params["text"])); m != nil {
			b, err := claimCodexStartFile(m[1])
			var file codexStartFile
			mu.Lock()
			if err == nil && json.Unmarshal(b, &file) == nil {
				files = append(files, file)
			}
			running = true
			mu.Unlock()
		}
	})
	f.handle("pane.process_info", func(map[string]any) any {
		mu.Lock()
		name := "zsh"
		if running {
			name = "codex"
		}
		mu.Unlock()
		return map[string]any{"process_info": map[string]any{"foreground_processes": []any{map[string]any{"pid": 1, "name": name, "argv0": name}}}}
	})
	return func() []codexStartFile {
		mu.Lock()
		defer mu.Unlock()
		return append([]codexStartFile(nil), files...)
	}
}

// withShortCodexWaits shortens how long a Codex start waits for its thread and how long a switch looks for a late one.
func withShortCodexWaits(t *testing.T, start, late time.Duration) {
	t.Helper()
	oldStart, oldLate := codexStartWait, lateCodexWait
	codexStartWait, lateCodexWait = start, late
	t.Cleanup(func() { codexStartWait, lateCodexWait = oldStart, oldLate })
}

// A Codex start that may have the message (codex-start claimed it, and a Codex runs in the pane) is not followed by
// another start or a rollback, which would run the message twice or have the user send it again, whatever codex-start
// printed (a line of its on the screen is no proof it failed: the Codex may run after it): the switch stops where it
// is, nothing more is pressed or typed, the switch is dropped, and the message is answered as not known to have gone.
// The pane no longer stands for the session switched away from (its row would show the Codex, and a message to it go
// there), and the thread, showing up late, takes the conversation.
func TestEngineSwitchStopsWhenTheStartMayHaveTheMessage(t *testing.T) {
	withShortCodexWaits(t, time.Second, 20*time.Second)
	n, f, home, cwd, _ := switchNode(t, proto.StatusIdle, nil)
	n.state.setPendingSwitch(sidA, &proto.PendingSwitch{Launcher: "cx", Agent: "codex", At: time.Now()})
	f.reset()
	quitsToShell(f, cwd)
	files := codexReadsAndRuns(f)
	f.setScreen("w1:p1", codexStartError+" agent sandbox: codex-start not wrapped (off)\n")
	claudeComesUp(t, f, cwd, func(string) bool { return true }) // a rollback would bring the claude back
	err := tell(t, n, "hello", true)
	if code, data := refusalOf(t, err); code != "" || data["delivered"] != deliveredUnknown {
		t.Fatalf("answered %q %v (%v), want delivered unknown", code, data, err)
	}
	starts := typedStarts(f, "w1:p1")
	if len(starts) != 1 || !startFileRE.MatchString(starts[0]) {
		t.Fatalf("typed %q", starts)
	}
	trace := strings.Join(f.trace("pane.send_keys", "pane.send_input"), "\n")
	if i := strings.Index(trace, "codex-start"); strings.Contains(trace[i:], "pane.send_keys") {
		t.Errorf("keys were pressed in the pane after the start:\n%s", trace)
	}
	if got := promptsTo(f); len(got) != 0 {
		t.Errorf("the message was typed: %q", got)
	}
	if n.pendingSwitch(sidA) != nil || n.switches().inFlight(sidA) {
		t.Error("the switch still waits, or is left under way")
	}
	n.mu.Lock()
	bound, guarded := n.panes["w1:p1"], n.codexStarting["w1:p1"]
	n.mu.Unlock()
	if st, _ := n.state.get(sidA); bound == sidA || st.Pane == "w1:p1" || !guarded {
		t.Fatalf("the pane is still the old session's (bound %q, record %q) or open to a guess (%v)", bound, st.Pane, guarded)
	}
	// The thread comes up after all.
	got := files()
	if len(got) != 1 {
		t.Fatalf("codex-start files %+v", got)
	}
	writeThread(t, home, switchedThread, cwd, got[0].Prompt)
	f.reply("session.snapshot", snapshot(nil, nil, []any{agentOf("codex", switchedThread, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")}, nil))
	waitWithin(t, 15*time.Second, "the late thread to take the conversation", func() bool {
		st, ok := n.state.get(switchedThread)
		return ok && len(st.EngineHistory) == 1 && st.EngineHistory[0].SessionID == sidA && st.Pane == "w1:p1"
	})
	if st, _ := n.state.get(sidA); st.EngineHistory != nil {
		t.Errorf("the old session still carries the history: %+v", st)
	}
}

// The same for a claude the switch's first prompt was typed to without herdr saying it went (another error than one of
// nothing typed): the earlier session it resumed is not given up for a fresh one with the message again; it takes the
// conversation, as far as the records go.
func TestEngineSwitchStopsWhenTheFirstPromptIsUnconfirmed(t *testing.T) {
	n, f, _, cwd, _ := codexSwitchNode(t)
	n.claudeStart, n.claudeReady = 2*time.Second, 2*time.Second
	n.state.setPendingSwitch(sidB, &proto.PendingSwitch{Launcher: "claude", Agent: "claude", At: time.Now()})
	quitsOn(f, "w1:p1", sidB, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidB))
	claudeComesUp(t, f, cwd, func(string) bool { return true })
	f.handle("agent.prompt", func(map[string]any) any { return herdrFail{Code: "server_error", Message: "the connection went away"} })
	err := tellTo(t, n, sidB, "and now?")
	if code, data := refusalOf(t, err); code != "" || data["delivered"] != deliveredUnknown {
		t.Fatalf("answered %q %v (%v), want delivered unknown", code, data, err)
	}
	if starts := typedStarts(f, "w1:p1"); len(starts) != 1 || !strings.Contains(starts[0], "--resume "+sidA) {
		t.Fatalf("typed %q", starts)
	}
	if st, _ := n.state.get(sidA); len(st.EngineHistory) != 2 || st.EngineHistory[1].SessionID != sidB || st.PendingSwitch != nil {
		t.Errorf("the resumed session's record = %+v", st)
	}
	if st, _ := n.state.get(sidB); st.EngineHistory != nil || st.PendingSwitch != nil {
		t.Errorf("the Codex thread's record = %+v", st)
	}
}

// The user's Resume of the earlier session while the switch takes it up (from the first Ctrl+C on, as its --resume
// is typed) waits for the switch and finds it running: no second claude --resume of it is typed, here or in a new tab.
func TestEngineSwitchHoldsTheEarlierSessionWhileItResumesIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		when func(c herdrCall) bool
	}{
		{"at the first Ctrl+C", func(c herdrCall) bool { return c.Method == "pane.send_keys" }},
		{"as its --resume is typed", func(c herdrCall) bool {
			return c.Method == "pane.send_input" && strings.Contains(str(c.Params["text"]), "--resume "+sidA)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, _, cwd, _ := codexSwitchNode(t)
			n.claudeStart, n.claudeReady = 10*time.Second, 10*time.Second
			f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": "proj"}}})
			f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
			n.state.setPendingSwitch(sidB, &proto.PendingSwitch{Launcher: "claude", Agent: "claude", At: time.Now()})
			quitsOn(f, "w1:p1", sidB, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidB))
			// The switch's claude --resume comes up after the start has read its terminal; the user's Resume goes in before.
			plain, ansi := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
			var mu sync.Mutex
			up, asked, starting := false, false, false
			readyRead := make(chan struct{}, 1)
			resumed := make(chan error, 1)
			f.after(func(c herdrCall) {
				if str(c.Params["pane_id"]) != "w1:p1" {
					return
				}
				mu.Lock()
				if c.Method == "pane.read" && starting {
					select {
					case readyRead <- struct{}{}:
					default:
					}
				}
				ask := !asked && tc.when(c)
				asked = asked || ask
				mu.Unlock()
				if ask {
					go func() {
						_, err := request(t, n, "session.start", map[string]any{"resume_session_id": sidA})
						resumed <- err
					}()
				}
				if c.Method == "pane.send_input" && strings.Contains(str(c.Params["text"]), "--resume "+sidA) {
					mu.Lock()
					starting = true
					mu.Unlock()
					go func() {
						<-readyRead
						f.setScreens("w1:p1", plain, ansi)
						f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")}, nil))
						mu.Lock()
						up = true
						mu.Unlock()
					}()
				}
			})
			f.handle("agent.get", func(p map[string]any) any {
				mu.Lock()
				defer mu.Unlock()
				if !up || str(p["target"]) != "w1:p1" {
					return map[string]any{"agent": nil}
				}
				return map[string]any{"agent": map[string]any{"agent": "claude", "agent_status": proto.StatusIdle, "pane_id": "w1:p1",
					"tab_id": "w1:t1", "workspace_id": "w1"}}
			})
			if err := tellTo(t, n, sidB, "and now?"); err != nil {
				t.Fatalf("send: %v\n%s", err, strings.Join(f.trace(), "\n"))
			}
			select {
			case err := <-resumed:
				if err != nil {
					t.Errorf("the Resume: %v", err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("the Resume did not end")
			}
			var again []string
			for _, c := range f.calls() {
				if c.Method == "pane.send_input" && strings.Contains(str(c.Params["text"]), "--resume "+sidA) {
					again = append(again, str(c.Params["pane_id"]))
				}
			}
			if len(again) != 1 || len(f.trace("tab.create", "workspace.create")) != 0 {
				t.Fatalf("claude --resume typed in %q, tabs %q", again, f.trace("tab.create", "workspace.create"))
			}
		})
	}
}

// A switch does not resume an earlier session the user's Resume is starting right now: a fresh session takes the
// conversation, and the Resume's start is the only one of that session.
func TestEngineSwitchLeavesAnEarlierSessionBeingResumed(t *testing.T) {
	n, f, _, cwd, _ := codexSwitchNode(t)
	n.state.setPendingSwitch(sidB, &proto.PendingSwitch{Launcher: "claude", Agent: "claude", At: time.Now()})
	quitsOn(f, "w1:p1", sidB, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidB))
	claudeComesUp(t, f, cwd, func(string) bool { return true })
	done, ok := n.switches().resuming(sidA) // a Resume of it, between its wait and its start
	if !ok {
		t.Fatal("no Resume could be registered")
	}
	defer done()
	if err := tellTo(t, n, sidB, "and now?"); err != nil {
		t.Fatalf("send: %v", err)
	}
	starts := typedStarts(f, "w1:p1")
	if len(starts) != 1 || !sessionIDArg.MatchString(starts[0]) || strings.Contains(starts[0], "--resume") {
		t.Fatalf("typed %q", starts)
	}
	if got := promptsTo(f); len(got) == 0 || !strings.Contains(got[0], `from="codex" to="claude"`) || strings.Contains(got[0], `resumed="1"`) {
		t.Fatalf("prompts %q", got)
	}
}

// An earlier stretch of a conversation that runs again (resumed by hand in another terminal) is listed as its own row,
// next to the conversation's.
func TestEngineSwitchListsAnEarlierSessionThatRunsAgain(t *testing.T) {
	n, f, _, cwd, _ := codexSwitchNode(t)
	if _, listed := byID(listSessions(t, n))[sidA]; listed {
		t.Fatal("the earlier stretch is listed while it is parked")
	}
	f.reply("session.snapshot", snapshot(nil, nil, []any{agentOf("codex", sidB, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, ""),
		claudeAgent(sidA, proto.StatusIdle, "w1:p2", "w1:t2", "w1", cwd, "")}, nil))
	list := byID(listSessions(t, n))
	if _, listed := list[sidA]; !listed {
		t.Fatal("the earlier stretch, running again, is not listed")
	}
	if _, listed := list[sidB]; !listed {
		t.Fatal("the conversation's row is gone")
	}
}

// codex-start's notes (the agent sandbox not wrapping the start) are never read as it failing: the Codex after them
// has the message.
func TestCodexStartNotesAreNoFailure(t *testing.T) {
	var b strings.Builder
	codexStartNotef(&b, "agent sandbox: %s not wrapped (%s): %s", "codex-start", "off", "why")
	if strings.Contains(b.String(), codexStartError) {
		t.Fatalf("a note reads as codex-start failing: %q", b.String())
	}
}

// codex-start claims its file by renaming it away before it reads it, and a node that withdrew it first leaves it
// nothing: exactly one of them has the message.
func TestCodexStartClaimsItsFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "start.json")
	if err := os.WriteFile(file, []byte(`{"argv":["codex"],"prompt":"hi"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := claimCodexStartFile(file)
	if err != nil || !strings.Contains(string(b), `"hi"`) {
		t.Fatalf("claimed %q, %v", b, err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("left %v", left)
	}
	if os.Rename(file, file+".withdrawn") == nil {
		t.Error("the node could still withdraw a claimed file")
	}
	if err := os.WriteFile(file, []byte(`{"argv":["codex"],"prompt":"hi"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file, file+".withdrawn"); err != nil { // the node withdraws it first
		t.Fatal(err)
	}
	if b, err := claimCodexStartFile(file); err == nil {
		t.Fatalf("codex-start claimed a withdrawn file: %q", b)
	}
}

// A Codex thread Persona has no record of (one started elsewhere), the conversation's earlier stretch, that a switch
// back resumes and that may have the message without showing it gets a record all the same: the conversation's history
// goes on it, and the list shows one row.
func TestEngineSwitchToAnUnrecordedCodexThread(t *testing.T) {
	const thread = "01a19a00-0000-7000-8000-00000000507a"
	withShortCodexWaits(t, time.Second, time.Second)
	t.Cleanup(func() { codexResumes.Delete(thread) })
	n, f, home, cwd, _ := switchNode(t, proto.StatusIdle, longTurn)
	until := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	writeThread(t, home, thread, cwd, "earlier, in Codex")
	n.codices = newCodexRegistry()
	n.codices.scan = func() []codexProcess { return nil }
	n.codices.checked = func() ([]codexProcess, bool) { return nil, true }
	n.configureCodex(proto.CodexConfig{})
	n.state.update(sidA, func(v *sessionState) bool {
		v.EngineHistory = []proto.EngineLeg{{SessionID: thread, Agent: "codex", Until: until}}
		return true
	})
	listSessions(t, n)
	n.state.setPendingSwitch(sidA, &proto.PendingSwitch{Launcher: "cx", Agent: "codex", At: time.Now()})
	f.reset()
	quitsToShell(f, cwd)
	files := codexReadsAndRuns(f)
	err := tell(t, n, "hello", true)
	if code, data := refusalOf(t, err); code != "" || data["delivered"] != deliveredUnknown {
		t.Fatalf("answered %q %v (%v), want delivered unknown", code, data, err)
	}
	if got := files(); len(got) != 1 || !strings.HasSuffix(strings.Join(got[0].Argv, " "), " resume "+thread) {
		t.Fatalf("codex-start ran %+v", got)
	}
	st, ok := n.state.get(thread)
	if !ok || len(st.EngineHistory) != 2 || st.EngineHistory[1].SessionID != sidA || st.Pane != "w1:p1" || st.Launcher != "cx" {
		t.Fatalf("the thread's record = %+v (%v)", st, ok)
	}
	if old, _ := n.state.get(sidA); old.EngineHistory != nil {
		t.Errorf("the session switched away from still carries the history: %+v", old)
	}
}

// A switch whose new claude came up behind a dialog of its own did not type its first message: the handoff waits on
// the session's record and goes in front of the next message sent to it, whoever sends it, once.
func TestEngineSwitchKeepsTheHandoffForTheNextMessage(t *testing.T) {
	n, f, _, cwd, _ := codexSwitchNode(t)
	n.state.setPendingSwitch(sidB, &proto.PendingSwitch{Launcher: "claude", Agent: "claude", At: time.Now()})
	quitsOn(f, "w1:p1", sidB, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidB))
	plain, ansi := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
	var mu sync.Mutex
	status := ""
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" && strings.Contains(str(c.Params["text"]), "--resume "+sidA) {
			mu.Lock()
			status = proto.StatusBlocked // the resume's own dialog (a large session's prompt) holds the keyboard
			mu.Unlock()
			f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusBlocked, "w1:p1", "w1:t1", "w1", cwd, "")}, nil))
		}
	})
	f.handle("agent.get", func(p map[string]any) any {
		mu.Lock()
		defer mu.Unlock()
		if status == "" {
			return map[string]any{"agent": nil}
		}
		return map[string]any{"agent": map[string]any{"agent": "claude", "agent_status": status, "pane_id": "w1:p1",
			"tab_id": "w1:t1", "workspace_id": "w1"}}
	})
	if err := tellTo(t, n, sidB, "and now?"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := promptsTo(f); len(got) != 0 {
		t.Fatalf("typed into the dialog: %q", got)
	}
	if st, _ := n.state.get(sidA); st.PendingHandoff == nil || !st.PendingHandoff.Resumed {
		t.Fatalf("the handoff is not kept: %+v", st.PendingHandoff)
	}
	// The user answers the dialog; their message (sent again) carries the handoff.
	mu.Lock()
	status = proto.StatusIdle
	mu.Unlock()
	f.setScreens("w1:p1", plain, ansi)
	f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")}, nil))
	listSessions(t, n)
	_, _ = request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "and now?"})
	got := promptsTo(f)
	if len(got) == 0 {
		t.Fatal("the message was not typed")
	}
	checkHandoffBlock(t, got[0], "codex", "claude", "", true, "and now?")
	if st, _ := n.state.get(sidA); st.PendingHandoff != nil {
		t.Errorf("the handoff still waits after going in: %+v", st.PendingHandoff)
	}
}

// A switch's first message a dialog held back goes in by itself once the dialog is answered (firstAfterDialog): it
// already carries the handoff, so the copy waiting for the next message is dropped and does not come twice.
func TestEngineSwitchHeldFirstMessageCarriesTheHandoffOnce(t *testing.T) {
	withFirstAfterDialog(t, 10*time.Second, 20*time.Millisecond)
	n, f, home, cwd, _ := codexSwitchNode(t)
	n.state.setPendingSwitch(sidB, &proto.PendingSwitch{Launcher: "claude", Agent: "claude", At: time.Now()})
	quitsOn(f, "w1:p1", sidB, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidB))
	plain, ansi := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
	var mu sync.Mutex
	status := ""
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" && strings.Contains(str(c.Params["text"]), "--resume "+sidA) {
			mu.Lock()
			status = proto.StatusBlocked
			mu.Unlock()
			f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusBlocked, "w1:p1", "w1:t1", "w1", cwd, "")}, nil))
		}
		if c.Method == "agent.prompt" {
			line, _ := json.Marshal(map[string]any{"type": "user", "timestamp": time.Now().UTC().Format(time.RFC3339Nano),
				"cwd": cwd, "message": map[string]any{"role": "user", "content": str(c.Params["text"])}})
			appendTranscript(t, home, cwd, sidA, string(line)) // the held message reaches the transcript
		}
	})
	f.handle("agent.get", func(p map[string]any) any {
		mu.Lock()
		defer mu.Unlock()
		if status == "" {
			return map[string]any{"agent": nil}
		}
		return map[string]any{"agent": map[string]any{"agent": "claude", "agent_status": status, "pane_id": "w1:p1",
			"tab_id": "w1:t1", "workspace_id": "w1"}}
	})
	if err := tellTo(t, n, sidB, "and now?"); err != nil {
		t.Fatalf("send: %v", err)
	}
	if st, _ := n.state.get(sidA); st.PendingHandoff == nil {
		t.Fatal("the handoff is not kept while the first message is held")
	}
	// Answered at the computer: the box is back and the held message goes in without anything sent again.
	f.setScreens("w1:p1", plain, ansi)
	f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")}, nil))
	mu.Lock()
	status = proto.StatusIdle
	mu.Unlock()
	n.dialogWait.Wait()
	got := promptsTo(f)
	if len(got) != 1 {
		t.Fatalf("typed %q, want the held message once", got)
	}
	checkHandoffBlock(t, got[0], "codex", "claude", "", true, "and now?")
	if st, _ := n.state.get(sidA); st.PendingHandoff != nil {
		t.Errorf("the handoff still waits after going in with the held message: %+v", st.PendingHandoff)
	}
}

// The handoff a held-back first message left is not put in front of a message once the session got one since (typed
// at the computer, or sent before without its arrival confirmed): it would come twice, or after the conversation went on.
func TestEngineSwitchDropsAHandoffTheSessionWentOnWithout(t *testing.T) {
	n, f, home, cwd := guideNode(t, true)
	at := time.Now().Add(-time.Minute).UTC()
	n.state.put(sidA, sessionState{Launcher: "claude", Cwd: cwd, StartedAt: at,
		PendingHandoff: &pendingHandoff{Text: "what happened", From: "codex", To: "claude", At: at}})
	appendTranscript(t, home, cwd, sidA, promptLine(cwd, "typed at the computer", 0))
	guidePane(t, f, home, cwd, func(int) string { return "" }, 1)
	if _, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "go on"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := promptsTo(f); len(got) != 1 || got[0] != "go on" {
		t.Fatalf("typed %q", got)
	}
	if st, _ := n.state.get(sidA); st.PendingHandoff != nil {
		t.Errorf("the handoff still waits: %+v", st.PendingHandoff)
	}
}

// The handoff is cleared only once the message it went with is known to be in: one whose arrival is not confirmed (a
// guide of Claude Code's took it) keeps it, and the next message carries it only while the transcript shows nothing
// since (here it then shows the earlier one: the handoff is dropped, not put in front twice).
func TestEngineSwitchKeepsTheHandoffUntilTheMessageIsIn(t *testing.T) {
	n, f, home, cwd := guideNode(t, false)
	at := time.Now().Add(-time.Minute).UTC()
	h := &pendingHandoff{Text: "what happened", From: "codex", To: "claude", At: at}
	n.state.put(sidA, sessionState{Launcher: "claude", Cwd: cwd, StartedAt: at, PendingHandoff: h})
	teach := screenFixture(t, "claude-2.1.318-teach-auto-mode-184.txt")
	guidePane(t, f, home, cwd, func(int) string { return teach })
	_, err := request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "go on"})
	if _, data := refusalOf(t, err); data["delivered"] != deliveredUnknown {
		t.Fatalf("answered %v", err)
	}
	if st, _ := n.state.get(sidA); st.PendingHandoff == nil {
		t.Fatal("the handoff was cleared with a message not known to be in")
	}
	got := promptsTo(f)
	if len(got) == 0 || !strings.HasPrefix(got[0], `<persona-context kind="engine-switch"`) {
		t.Fatalf("typed %q", got)
	}
	// It went in after all: the next message does not carry the handoff again.
	appendTranscript(t, home, cwd, sidA, promptLine(cwd, "go on", 0))
	f.reset()
	empty, emptyANSI := screenFixture(t, "claude-2.1.312-empty-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.ansi")
	f.setScreens("w1:p1", empty, emptyANSI)
	_, _ = request(t, n, "session.send", map[string]any{"session_id": sidA, "text": "and then?"})
	if got := promptsTo(f); len(got) == 0 || got[0] != "and then?" {
		t.Fatalf("typed %q", got)
	}
}

// A switch whose codex-start said it could not run the launcher falls back, even when the shell it failed back to
// comes in front only a moment after the start gave up.
func TestEngineSwitchFallsBackWhenCodexStartFailedLate(t *testing.T) {
	withShortCodexWaits(t, time.Second, time.Second)
	n, f, _, cwd, _ := switchNode(t, proto.StatusIdle, nil)
	n.state.setPendingSwitch(sidA, &proto.PendingSwitch{Launcher: "cx", Agent: "codex", At: time.Now()})
	f.reset()
	quitsToShell(f, cwd)
	var mu sync.Mutex
	var failedAt time.Time
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_input" || str(c.Params["pane_id"]) != "w1:p1" {
			return
		}
		if m := startFileRE.FindStringSubmatch(str(c.Params["text"])); m != nil {
			_, _ = claimCodexStartFile(m[1]) // codex-start claimed it, then could not run the launcher
			f.setScreen("w1:p1", codexStartError+" codex not found in PATH\n% ")
			mu.Lock()
			failedAt = time.Now()
			mu.Unlock()
		}
	})
	f.handle("pane.process_info", func(map[string]any) any {
		mu.Lock()
		name := "zsh"
		// the process codex-start was still in front while the start gave up; the shell comes back 1.5 s later
		if !failedAt.IsZero() && time.Since(failedAt) < codexStartWait+1500*time.Millisecond {
			name = "personad"
		}
		mu.Unlock()
		return map[string]any{"process_info": map[string]any{"foreground_processes": []any{map[string]any{"pid": 1, "name": name, "argv0": name}}}}
	})
	claudeComesUp(t, f, cwd, func(typed string) bool { return strings.Contains(typed, "--resume "+sidA) })
	err := tell(t, n, "hello", true)
	if code, data := refusalOf(t, err); code != proto.CodeSwitchFailed || data["delivered"] != deliveredNone {
		t.Fatalf("answered %q %v (%v), want switch_failed", code, data, err)
	}
}

// codex-start tries a claim again that another program holds up for a moment (a scanner on Windows), and gives up on a
// file that is gone.
func TestCodexStartClaimWaitsForABusyFile(t *testing.T) {
	busy := errors.New("the file is in use")
	oldRename, oldRetry := claimRename, claimRetryable
	t.Cleanup(func() { claimRename, claimRetryable = oldRename, oldRetry })
	tries := 0
	claimRename = func(from, to string) error {
		if tries++; tries < 3 {
			return busy
		}
		return os.Rename(from, to)
	}
	claimRetryable = func(err error) bool { return errors.Is(err, busy) }
	file := filepath.Join(t.TempDir(), "start.json")
	if err := os.WriteFile(file, []byte(`{"prompt":"hi"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, err := claimCodexStartFile(file); err != nil || !strings.Contains(string(b), "hi") || tries != 3 {
		t.Fatalf("claimed %q, %v after %d tries", b, err, tries)
	}
	if _, err := claimCodexStartFile(file); err == nil {
		t.Fatal("claimed a file that is gone")
	}
}

// The user's Resume of an id that is no session's waits for nothing, even with a switch's flight under that name.
func TestResumeTurnOfAnInvalidID(t *testing.T) {
	n, _ := testNode(t)
	n.switches().begin("not a session", proto.Session{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		n.resumeTurn(context.Background(), "not a session")()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the Resume of an invalid id did not return")
	}
}

// A late Codex thread leaves the session switched away from as it is when the user took that one up again meanwhile
// (a switch picked for it since).
func TestTookUpAgain(t *testing.T) {
	n, _, _, _, _ := switchNode(t, proto.StatusIdle, nil)
	left := time.Now().Add(-time.Minute)
	n.state.setPendingSwitch(sidA, &proto.PendingSwitch{Launcher: "cx", Agent: "codex", At: time.Now()})
	if !n.tookUpAgain(sidA, left) {
		t.Error("a session that runs, with a switch picked since, counts as not taken up")
	}
	n.mu.Lock()
	delete(n.agents, sidA)
	n.mu.Unlock()
	if !n.tookUpAgain(sidA, left) {
		t.Error("a switch picked since does not count")
	}
	n.state.setPendingSwitch(sidA, &proto.PendingSwitch{Launcher: "cx", Agent: "codex", At: left.Add(-time.Hour)})
	if n.tookUpAgain(sidA, left) {
		t.Error("a switch picked before the session was left counts")
	}
}

// An earlier session resumed elsewhere after the switch was planned (here while a message to it holds its typing lock)
// is looked at again as soon as the switch holds it, before the first Ctrl+C: a fresh session takes the conversation at
// once, without waiting for that lock.
func TestEngineSwitchLooksAgainBeforeTheQuit(t *testing.T) {
	n, f, _, cwd, _ := codexSwitchNode(t)
	unlock := n.typing.lock(sidA) // a message to it going in
	t.Cleanup(unlock)
	n.state.setPendingSwitch(sidB, &proto.PendingSwitch{Launcher: "claude", Agent: "claude", At: time.Now()})
	// The last look before the quit reads the pane a second time: by then sidA runs in another pane.
	var mu sync.Mutex
	reads := 0
	f.after(func(c herdrCall) {
		if c.Method != "pane.read" || str(c.Params["pane_id"]) != "w1:p1" {
			return
		}
		mu.Lock()
		reads++
		second := reads == 2
		mu.Unlock()
		if second {
			f.reply("session.snapshot", snapshot(nil, nil, []any{agentOf("codex", sidB, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, ""),
				claudeAgent(sidA, proto.StatusIdle, "w1:p2", "w1:t2", "w1", cwd, "")}, nil))
		}
	})
	quitsOn(f, "w1:p1", sidB, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidB))
	claudeComesUp(t, f, cwd, func(string) bool { return true })
	sent := make(chan error, 1)
	go func() { sent <- tellTo(t, n, sidB, "and now?") }()
	select {
	case err := <-sent:
		if err != nil {
			t.Fatalf("send: %v", err)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("the switch waited for the earlier session's typing lock")
	}
	if starts := typedStarts(f, "w1:p1"); len(starts) != 1 || !sessionIDArg.MatchString(starts[0]) {
		t.Fatalf("typed %q", starts)
	}
}
