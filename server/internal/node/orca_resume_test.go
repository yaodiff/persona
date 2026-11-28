package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
)

// Resuming a stopped Codex, TRAE CLI or pi session on Orca the way herdr does (#17044): a new terminal in the Orca
// worktree holding the session's directory, `cd` there, the launcher's resume command. Never a second writer: the
// conversation's own checks keep a resume out while something still has it open.

// orcaCodexNode is an Orca node whose ~/.codex has one rollout, startThread's, in cwd (an Orca worktree), which
// Persona started with launcher and which has stopped: its last terminal still shows the shell it left.
func orcaCodexNode(t *testing.T, launcher string) (*Node, *orcarttest.Fake, *fakeProcs, string) {
	t.Helper()
	n, _, o, procs, home := orcaNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	day := time.Now()
	dir := mkdir(t, filepath.Join(home, ".codex", "sessions", day.Format("2006"), day.Format("01"), day.Format("02")))
	meta, _ := json.Marshal(map[string]any{"timestamp": day.UTC().Format(time.RFC3339Nano), "type": "session_meta",
		"payload": map[string]any{"id": startThread, "cwd": cwd, "originator": "codex-tui", "source": "cli"}})
	if err := os.WriteFile(filepath.Join(dir, "rollout-2026-11-02T10-00-00-"+startThread+".jsonl"), append(meta, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	n.codices = newCodexRegistry()
	n.codices.scan = func() []codexProcess { return nil }
	n.codices.checked = func() ([]codexProcess, bool) { return nil, true }
	fixtureCodexIndex(n.codices)
	n.configureCodex(proto.CodexConfig{})
	o.SetWorktrees(orcarttest.Worktree("wt-proj", cwd, "refs/heads/main"))
	// The terminal the stopped Codex ran in, its shell in front again. It does not stop a resume, as it stops a message
	// (notRunningRefusal); the node has no record of opening it, so the resume opens a new terminal (keptPane).
	o.SetTerminals(orcarttest.Terminal("term_old", "tabOld", "leafOld", "wt-proj", cwd, "refs/heads/main", "zsh", ""))
	n.state.put(startThread, sessionState{Launcher: launcher, Cwd: cwd, Pane: "orca:tabOld:leafOld", StartedAt: time.Now().Add(-time.Hour)})
	n.mu.Lock()
	if n.lastPanes == nil {
		n.lastPanes = map[string]string{}
	}
	n.lastPanes[startThread] = "orca:tabOld:leafOld"
	n.mu.Unlock()
	return n, o, procs, cwd
}

// orcaStartFiles has o's terminal.send read the codex-start file an Orca start's typed line sources and run what the
// file says in that terminal: a codex process. It returns the files read so far.
func orcaStartFiles(t *testing.T, o *orcarttest.Fake, procs *fakeProcs) func() []codexStartFile {
	t.Helper()
	var mu sync.Mutex
	var files []codexStartFile
	o.Handle("terminal.send", func(p map[string]any) any {
		text, _ := p["text"].(string)
		handle, _ := p["terminal"].(string)
		reply := map[string]any{"send": map[string]any{"handle": handle, "accepted": true, "bytesWritten": len(text)}}
		_, line, _ := strings.Cut(strings.TrimSuffix(text, "\r"), " && ")
		cmd, ok := startFileCommand(line)
		if !ok {
			return reply
		}
		m := startFileRE.FindStringSubmatch(cmd)
		if m == nil {
			return reply
		}
		b, err := os.ReadFile(m[1])
		if err != nil {
			return reply
		}
		var f codexStartFile
		if json.Unmarshal(b, &f) != nil {
			return reply
		}
		mu.Lock()
		files = append(files, f)
		mu.Unlock()
		if pane := orcaFakePane(handle); pane != "" {
			procs.set(pane, orcaProc{pid: 4747, agent: "codex", start: time.Now()})
		}
		return reply
	})
	return func() []codexStartFile {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(files)
	}
}

// A stopped Codex Persona started resumes in Orca from the user's Resume: a new terminal in the worktree holding its
// directory, `cd` there, its launcher with `resume <id>` through codex-start, with the terminal's own hooks (as a new
// Codex start in Orca has them). Before #17044 it was refused (not_running), and the row offered Resume all the same.
func TestOrcaResumesAStoppedCodex(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"cx": {"codex", "-m", "gpt-6"}})
	n, o, procs, cwd := orcaCodexNode(t, "cx")
	if s := byID(listSessions(t, n))[startThread]; s.Capabilities == nil || !s.Capabilities.Terminal || s.Capabilities.Send ||
		s.ReadOnlyReason != stoppedReason {
		t.Fatalf("the stopped Codex is not offered Resume: %+v (%+v)", s, s.Capabilities)
	}
	o.Reset()
	files := orcaStartFiles(t, o, procs)
	// A plain message does not bring it back on Orca either (#4754, #17809): refused, with no terminal opened or typed in.
	_, err := request(t, n, "session.send", map[string]any{"session_id": startThread, "text": "go on"})
	if code, _ := refusalOf(t, err); code != agentStopped {
		t.Fatalf("a message to the stopped Codex: %v", err)
	}
	if calls := o.Trace("terminal.create", "terminal.send"); len(calls) != 0 || len(files()) != 0 {
		t.Fatalf("a message opened or typed into a terminal: %v", calls)
	}
	o.Reset()
	res, err := request(t, n, "session.start", map[string]any{"resume_session_id": startThread})
	if err != nil {
		t.Fatalf("resume: %v (calls %v)", err, o.Trace())
	}
	if s, _ := res["session"].(map[string]any); s == nil || s["session_id"] != startThread || s["pane_id"] != "orca:tab_2:leaf_2" {
		t.Errorf("session = %v", res["session"])
	}
	if c := o.Trace("terminal.create"); len(c) != 1 || !strings.Contains(c[0], `"worktree":"id:wt-proj"`) {
		t.Errorf("terminal.create %v", c)
	}
	sends := o.Trace("terminal.send")
	if len(sends) != 1 || !strings.Contains(sends[0], "cd -- "+shellQuote(cwd)) || !strings.Contains(sends[0], "term_fake_2") {
		t.Errorf("not started in a new terminal in the session's directory: %v", sends)
	}
	got := files()
	if len(got) != 1 || strings.Join(got[0].Argv, " ") != "codex -m gpt-6 resume "+startThread || got[0].Prompt != "" ||
		!got[0].IsolateHooks || got[0].Dir != cwd {
		t.Fatalf("codex-start files %+v", got)
	}
	if st, _ := n.state.get(startThread); st.Pane != "orca:tab_2:leaf_2" || st.Launcher != "cx" || st.Cwd != cwd {
		t.Errorf("state = %+v", st)
	}
	for _, w := range o.Writes() {
		if w.Handle == "term_old" {
			t.Errorf("typed into the terminal the stopped Codex left: %+v", w)
		}
	}
	if inv := o.Invalid(); len(inv) != 0 {
		t.Errorf("params Orca would refuse: %v", inv)
	}
}

// A stopped Codex whose terminal Persona opened for it, its shell at rest there again, resumes in that shell, as a
// Claude session does (stopped.go inKeptTerminal): no new terminal, typed once through the single-use grant Orca
// checks again right before the line goes in. A shell something else runs in front of gets a new terminal instead.
func TestOrcaResumesAStoppedCodexInItsKeptShell(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "at rest", true: "busy"}[busy], func(t *testing.T) {
			withOrcaShell(t, "-zsh")
			withCodexLaunchers(t, map[string][]string{"cx": {"codex"}})
			n, o, procs, cwd := orcaCodexNode(t, "cx")
			if err := n.refreshHosts(t.Context()); err != nil {
				t.Fatal(err)
			}
			h := n.hosts.Get(host.Orca).(*orcaHost)
			n.agentTerminalOpened(h, host.PaneRef{Kind: host.Orca, ID: "tabOld:leafOld"}, startThread)
			if busy {
				o.SetProcess("term_old", orcarttest.Running("npm", orcaShellPID))
			} else {
				o.SetProcess("term_old", orcarttest.ShellAtRest(orcaShellPID))
			}
			files := orcaStartFiles(t, o, procs)
			o.Reset()
			res, err := request(t, n, "session.start", map[string]any{"resume_session_id": startThread})
			if err != nil {
				t.Fatalf("resume: %v (calls %v)", err, o.Trace("terminal.create", "terminal.send"))
			}
			want, terminal := "orca:tabOld:leafOld", "term_old"
			if busy {
				want, terminal = "orca:tab_2:leaf_2", "term_fake_2"
			}
			if s, _ := res["session"].(map[string]any); s == nil || s["session_id"] != startThread || s["pane_id"] != want {
				t.Errorf("session = %v", res["session"])
			}
			if c := o.Trace("terminal.create"); busy != (len(c) == 1) || len(c) > 1 {
				t.Errorf("terminal.create %v", c)
			}
			sends := o.Params("terminal.send")
			if len(sends) != 1 || sends[0]["terminal"] != terminal {
				t.Fatalf("sends %v", sends)
			}
			if text, _ := sends[0]["text"].(string); !strings.HasPrefix(text, "cd -- "+shellQuote(cwd)+" && ") {
				t.Errorf("not started in the thread's folder: %q", text)
			}
			if !busy && sends[0]["expectedIncarnationId"] != "inc-term_old" {
				t.Errorf("typed without the kept shell's incarnation: %v", sends[0])
			}
			if got := files(); len(got) != 1 || strings.Join(got[0].Argv, " ") != "codex resume "+startThread || !got[0].IsolateHooks {
				t.Fatalf("codex-start files %+v", got)
			}
			if inv := o.Invalid(); len(inv) != 0 {
				t.Errorf("params Orca would refuse: %v", inv)
			}
		})
	}
}

// The stock codex launcher resumes on Orca with Orca's own Codex launch arguments, as a new start of it there runs
// them (codex_orca_launch_defaults.go); Orca preferences it cannot follow refuse before a terminal opens.
func TestOrcaResumesAStockCodexWithOrcasArguments(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{}) // the stock launcher: no configured alias of that name
	n, o, procs, _ := orcaCodexNode(t, "codex")
	o.Handle("settings.getAgentLaunch", func(map[string]any) any {
		return orcaCodexPreferenceAnswer(orcaCodexPreferenceSettings("--sandbox workspace-write"))
	})
	files := orcaStartFiles(t, o, procs)
	if _, err := request(t, n, "session.start", map[string]any{"resume_session_id": startThread}); err != nil {
		t.Fatalf("resume: %v (calls %v)", err, o.Trace())
	}
	if got := files(); len(got) != 1 || strings.Join(got[0].Argv, " ") != "codex --sandbox workspace-write resume "+startThread {
		t.Fatalf("codex-start files %+v", got)
	}
	assertNoOrcaSettingsMutations(t, o)

	n2, o2, procs2, _ := orcaCodexNode(t, "codex")
	o2.Handle("settings.getAgentLaunch", func(map[string]any) any {
		return orcaCodexPreferenceAnswer(map[string]any{"agentCmdOverrides": map[string]string{"codex": "my-codex"}})
	})
	files2 := orcaStartFiles(t, o2, procs2)
	_, err := request(t, n2, "session.start", map[string]any{"resume_session_id": startThread})
	if code, _ := refusalOf(t, err); code != "codex_launch_preferences" {
		t.Fatalf("custom Orca command: %v", err)
	}
	if c := o2.Trace("terminal.create", "terminal.send"); len(c) != 0 || len(files2()) != 0 {
		t.Errorf("Orca was asked %v", c)
	}
}

// Never a second writer: a Codex that still holds the thread's rollout (suspended in the terminal it ran in, or
// opened on the computer since the list was read) keeps the resume out with nothing opened, as on herdr.
func TestOrcaCodexResumeRefusesWhileAWriterHoldsIt(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"cx": {"codex"}})
	n, o, procs, _ := orcaCodexNode(t, "cx")
	listSessions(t, n)
	path := n.codexPath(startThread)
	n.codices.mu.Lock()
	n.codices.checked = func() ([]codexProcess, bool) {
		return []codexProcess{{pid: os.Getpid(), orcaPane: "tabOld:leafOld"}}, true
	}
	n.codices.mu.Unlock()
	// This test process holds the rollout open: "a Codex" suspended in the terminal the agent ran in.
	h, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	o.Reset()
	files := orcaStartFiles(t, o, procs)
	_, err = request(t, n, "session.start", map[string]any{"resume_session_id": startThread})
	if code, _ := refusalOf(t, err); code != codexResumeRefused {
		t.Fatalf("resume of a held thread: %v", err)
	}
	if c := o.Trace("terminal.create", "terminal.send"); len(c) != 0 || len(files()) != 0 {
		t.Errorf("Orca was asked %v", c)
	}
}

// The assistant's explicit resume (session.start with resume_automatic, assistant agent_resume.go) of a Codex the task closer stopped
// (task_completed) goes the same way on Orca: a new terminal, the launcher's resume command with the text.
func TestOrcaSupervisorResumesAStoppedCodex(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"cx": {"codex"}})
	n, o, procs, cwd := orcaCodexNode(t, "cx")
	if err := n.saveStopCause(t.Context(), startThread, proto.StopTaskCompleted); err != nil {
		t.Fatal(err)
	}
	n.state.stopped(startThread, proto.StopTaskCompleted)
	files := orcaStartFiles(t, o, procs)
	if _, err := request(t, n, "session.start", map[string]any{
		"resume_session_id": startThread, "resume_automatic": true, "prompt": "Fix what the review found",
	}); err != nil {
		t.Fatalf("supervisor resume: %v (calls %v)", err, o.Trace())
	}
	got := files()
	if len(got) != 1 || strings.Join(got[0].Argv, " ") != "codex resume "+startThread || got[0].Prompt != "Fix what the review found" || got[0].Dir != cwd {
		t.Fatalf("codex-start files %+v", got)
	}
}

// Codex takeover goes to Orca when it is the start host: an Orca-only computer used to have "no terminal engine"
// for it (#17044).
func TestCodexTakeoverHostIncludesOrca(t *testing.T) {
	n, _, _, _ := orcaCodexNode(t, "cx")
	ctx, err := n.takeoverHost(t.Context())
	if err != nil || n.startKind(ctx) != proto.EngineOrca {
		t.Fatalf("takeoverHost = (%q, %v), want orca", n.startKind(ctx), err)
	}
}

// orcaFakePane is the pane key of the fake runtime's terminal handle (term_fake_<n>: tab_<n>:leaf_<n>), "" for another.
func orcaFakePane(handle string) string {
	if handle == "term_old" { // orcaCodexNode's terminal of the stopped Codex
		return "tabOld:leafOld"
	}
	var idx int
	if _, err := fmt.Sscanf(handle, "term_fake_%d", &idx); err != nil {
		return ""
	}
	return fmt.Sprintf("tab_%d:leaf_%d", idx, idx)
}

// A stopped TRAE CLI session resumes in Orca as on herdr (#17044): a new terminal in the worktree holding its
// directory, `cd` there, `<launcher> resume <id>`; the start waits for that TRAE CLI to show the session in the
// terminal. Before #17044 it was refused (orca_resume).
func TestOrcaResumesAStoppedTraex(t *testing.T) {
	n, _, o, procs, home := orcaNode(t)
	n.shellSettled = true
	cwd := mkdir(t, filepath.Join(home, "proj"))
	o.SetWorktrees(orcarttest.Worktree("wt-proj", cwd, "refs/heads/main"))
	const id, pid = "01a00000-0000-7000-8000-0000000000e1", 5151
	writeMetaRollout(t, n.traexHome(), id, `{"id":"`+id+`","cwd":"`+cwd+`"}`, time.Now().Add(-time.Hour))
	n.state.put(id, sessionState{Launcher: traexAgent, Cwd: cwd, Pane: "orca:tabOld:leafOld", StartedAt: time.Now().Add(-time.Hour)})
	withProcessArgvStub(t, func(p int) ([]string, error) {
		if p == pid {
			return []string{traexAgent, "resume", id}, nil
		}
		return nil, os.ErrNotExist
	})
	var mu sync.Mutex
	var typed []string
	o.Handle("terminal.send", func(p map[string]any) any {
		text, _ := p["text"].(string)
		handle, _ := p["terminal"].(string)
		_, line, _ := strings.Cut(strings.TrimSuffix(text, "\r"), " && ")
		if cmd, ok := startFileCommand(line); ok {
			mu.Lock()
			typed = append(typed, cmd)
			mu.Unlock()
			if pane := orcaFakePane(handle); pane != "" && strings.Contains(cmd, "resume") {
				procs.set(pane, orcaProc{pid: pid, agent: traexAgent, start: time.Now()})
			}
		}
		return map[string]any{"send": map[string]any{"handle": handle, "accepted": true, "bytesWritten": len(text)}}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	res, err := requestCtx(t, ctx, n, "session.start", map[string]any{"resume_session_id": id})
	if err != nil {
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("resume: %v (typed %q, calls %v)", err, typed, o.Trace("terminal.create", "terminal.send"))
	}
	if s, _ := res["session"].(map[string]any); s == nil || s["session_id"] != id || s["pane_id"] != "orca:tab_1:leaf_1" {
		t.Errorf("session = %v", res["session"])
	}
	if c := o.Trace("terminal.create"); len(c) != 1 || !strings.Contains(c[0], `"worktree":"id:wt-proj"`) {
		t.Errorf("terminal.create %v", c)
	}
	if s := strings.Join(o.Trace("terminal.send"), "\n"); !strings.Contains(s, "cd -- "+shellQuote(cwd)) {
		t.Errorf("not started in the session's directory: %s", s)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(typed) != 1 || !strings.Contains(typed[0], traexAgent+" resume "+shellQuote(id)) {
		t.Errorf("typed %q", typed)
	}
	if st, _ := n.state.get(id); st.Pane != "orca:tab_1:leaf_1" || st.Launcher != traexAgent || st.Cwd != cwd {
		t.Errorf("state = %+v", st)
	}
}

// A pi session that ran in a terminal and stopped is resumed in Orca when Orca is the engine (#17044): a send opens a
// terminal in the worktree holding its directory, runs the runtime's resume command there through tab-start, waits
// for the pi it starts to list the session, and sends there. Before #17044 it was refused (orca_resume).
func TestPiResumedInOrca(t *testing.T) {
	n, _, home, rh := piHerdrNode(t)
	o := orcarttest.New(t)
	h := newOrcaHost(&orcart.Client{MetaPath: o.MetaPath})
	h.scan = (&fakeProcs{}).scan
	n.hosts = host.NewRegistry(h)
	n.eng.name = proto.EngineOrca
	t.Cleanup(n.starts.close)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	o.SetWorktrees(orcarttest.Worktree("wt-proj", cwd, "refs/heads/main"))
	file := filepath.Join(home, ".pi/agent/sessions/x_"+piSID+".jsonl")
	files := piExtension(t, rh, "pi-files", map[string]any{"session_id": piSID, "status": "offline", "cwd": cwd,
		"resume_argv": []string{"pi", "--session", file}})
	cmdRe := regexp.MustCompile(` tab-start '?([^' \r]+)'?\r?$`)
	started := make(chan codexStartFile, 1)
	fresh := make(chan *fakeRuntime, 1)
	o.Handle("terminal.send", func(p map[string]any) any {
		text, _ := p["text"].(string)
		if m := cmdRe.FindStringSubmatch(text); m != nil {
			var sf codexStartFile
			b, _ := os.ReadFile(m[1])
			_ = json.Unmarshal(b, &sf)
			started <- sf
			// The pi it starts loads the extension, which lists the session running.
			go func() {
				fresh <- piExtension(t, rh, "pi-5555", map[string]any{"session_id": piSID, "status": "idle", "cwd": cwd})
			}()
		}
		return map[string]any{"send": map[string]any{"handle": p["terminal"], "accepted": true, "bytesWritten": len(text)}}
	})
	old := piResumeWait
	piResumeWait = 10 * time.Second
	t.Cleanup(func() { piResumeWait = old })

	if _, err := request(t, n, "session.send", map[string]any{"session_id": piSID, "text": "and the tests", "client_msg_id": "m-3"}); err != nil {
		t.Fatalf("%v\norca: %v", err, o.Trace())
	}
	if sf := <-started; strings.Join(sf.Argv, " ") != "pi --session "+file {
		t.Fatalf("ran %v", sf.Argv)
	}
	if c := o.Trace("terminal.create"); len(c) != 1 || !strings.Contains(c[0], `"worktree":"id:wt-proj"`) {
		t.Errorf("terminal.create %v", c)
	}
	sends := strings.Join(o.Trace("terminal.send"), "\n")
	if !strings.Contains(sends, "cd -- "+shellQuote(cwd)) || strings.Contains(sends, "and the tests") {
		t.Errorf("typed %s", sends)
	}
	if len(files.got("session.send")) != 0 {
		t.Fatal("sent to the runtime with the session on disk")
	}
	if rt := <-fresh; len(rt.got("session.send")) != 1 || !strings.Contains(string(rt.got("session.send")[0]), "and the tests") {
		t.Fatal("the message did not reach the pi started in Orca")
	}
}

// What Orca still does not resume from the phone is refused before a terminal opens, in words from the catalogs
// (rpc-errors.*): an omp session (#13597), and a work agent's session through a launcher (as on every engine).
func TestOrcaRefusesWhatItDoesNotResume(t *testing.T) {
	n, _, o, _, home := orcaNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	o.SetWorktrees(orcarttest.Worktree("wt-proj", cwd, "refs/heads/main"))
	const id = "0e000000-0000-4000-8000-0000000000e2"
	n.state.put(id, sessionState{Launcher: ompAgent, Cwd: cwd})
	_, err := request(t, n, "session.start", map[string]any{"resume_session_id": id, "launcher": ompAgent, "cwd": cwd})
	if !isRefusal(err, "orca_resume") || !strings.Contains(err.Error(), i18n.T(i18n.Default, "rpc-errors.orca_resume")) {
		t.Errorf("omp resume on Orca: %v", err)
	}
	// A message to a session Orca has nothing of to continue: not_running naming the engine, for the hub to word.
	const unknown = "0e000000-0000-4000-8000-0000000000e3"
	_, err = request(t, n, "session.send", map[string]any{"session_id": unknown, "text": "hello"})
	if code, data := refusalOf(t, err); code != notRunning || data["reason"] != proto.NotRunningEngine || data["engine"] != "Orca" ||
		data["delivered"] != "none" || err.Error() != i18n.T(i18n.Default, "rpc-errors.not_running_engine", i18n.Args{"engine": "Orca"}) {
		t.Errorf("message to an unknown session: %v %v", err, data)
	}
	for _, w := range []string{"agy", "cursor"} {
		_, err = n.start(t.Context(), cwd, w, "", id, "", "", "", "", "", w)
		if !isRefusal(err, "work_agent_resume") || !strings.Contains(err.Error(), i18n.T(i18n.Default, "rpc-errors.work_agent_resume")) {
			t.Errorf("%s resume: %v", w, err)
		}
	}
	if c := o.Trace("terminal.create", "terminal.send"); len(c) != 0 {
		t.Errorf("Orca was asked %v", c)
	}
}
