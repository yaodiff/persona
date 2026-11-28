package node

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

const testGrace = 10 * time.Minute

// zshInFront answers pane.process_info with a login zsh alone in front.
func zshInFront(f *fakeHerdr) {
	f.reply("pane.process_info", map[string]any{"process_info": map[string]any{"foreground_processes": []any{
		map[string]any{"pid": 1, "name": "zsh", "argv0": "-zsh"}}}})
}

// rounds runs closeFinishedRound at t0 + each offset and returns what each closed.
func rounds(t *testing.T, n *Node, t0 time.Time, offsets ...time.Duration) [][]string {
	t.Helper()
	var out [][]string
	for _, d := range offsets {
		out = append(out, n.closeFinishedRound(t.Context(), testGrace, t0.Add(d)))
	}
	return out
}

// A finished Agent's terminal (marked for its session, only a shell in front, nobody watching) is closed once it has
// sat unchanged for the grace period, not before; a shell the user opened beside it is never closed (#10111).
func TestFinishedTerminalClosesAfterGrace(t *testing.T) {
	n, f, cwd := keptAgent(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, []any{
		shellPane("w1:p1", "w1:t1", "w1", cwd, sidA), shellPane("w1:p2", "w1:t2", "w1", cwd, "")}))
	f.setScreen("w1:p1", "done.\n% ")
	f.setScreen("w1:p2", "% ")
	t0 := time.Now()
	got := rounds(t, n, t0, 0, testGrace-time.Second)
	if len(got[0]) != 0 || len(got[1]) != 0 {
		t.Fatalf("closed within the grace period: %q", got)
	}
	closesNothing(t, f)
	got = rounds(t, n, t0, testGrace)
	if !slices.Equal(got[0], []string{"w1:p1"}) {
		t.Fatalf("after the grace period closed %q", got[0])
	}
	if tr := f.trace("tab.close", "pane.close"); len(tr) != 1 || !strings.Contains(tr[0], "w1:t1") && !strings.Contains(tr[0], "w1:p1") {
		t.Fatalf("closed %q", tr)
	}
	// Later rounds never close the user's shell.
	f.reset()
	f.reply("session.snapshot", snapshot(nil, nil, nil, []any{shellPane("w1:p2", "w1:t2", "w1", cwd, "")}))
	zshInFront(f)
	got = rounds(t, n, t0, 2*testGrace, 5*testGrace)
	if len(got[0]) != 0 || len(got[1]) != 0 {
		t.Fatalf("closed a user's shell: %q", got)
	}
	closesNothing(t, f)
}

// Codex starts mark their tab before the thread is known ("agent"): those are Persona's too.
func TestFinishedTerminalWithoutSessionIDCloses(t *testing.T) {
	n, f, cwd := keptAgent(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, []any{shellPane("w2:p1", "w2:t1", "w2", cwd, "agent")}))
	t0 := time.Now()
	if got := rounds(t, n, t0, 0, testGrace); !slices.Equal(got[1], []string{"w2:p1"}) {
		t.Fatalf("closed %q", got)
	}
}

// Panes that are not a finished Agent's at rest are never closed, however long they stay: one with an Agent in it
// (working, idle or waiting for an answer), a marked one with another program in front, and one a client is watching.
func TestFinishedTerminalLeavesBusyPanes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(n *Node, f *fakeHerdr, cwd string)
	}{
		{"working agent", func(n *Node, f *fakeHerdr, cwd string) {
			f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusWorking, "w1:p1", "w1:t1", "w1", cwd, "")},
				[]any{claudePane(sidA, "w1:p1", "w1:t1", "w1", cwd)}))
		}},
		{"waiting for an answer", func(n *Node, f *fakeHerdr, cwd string) {
			f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusBlocked, "w1:p1", "w1:t1", "w1", cwd, "")},
				[]any{claudePane(sidA, "w1:p1", "w1:t1", "w1", cwd)}))
		}},
		{"idle agent", func(n *Node, f *fakeHerdr, cwd string) {
			f.reply("session.snapshot", snapshot(nil, nil, []any{claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")},
				[]any{claudePane(sidA, "w1:p1", "w1:t1", "w1", cwd)}))
		}},
		{"program in front", func(n *Node, f *fakeHerdr, cwd string) {
			f.reply("pane.process_info", map[string]any{"process_info": map[string]any{"foreground_processes": []any{
				map[string]any{"pid": 1, "name": "zsh", "argv0": "-zsh"}, map[string]any{"pid": 2, "name": "vim", "argv0": "vim"}}}})
		}},
		{"watched", func(n *Node, f *fakeHerdr, cwd string) {
			n.setTermStream(panePrefix+"w1:p1", &termStream{cancel: func() {}})
		}},
		{"watched by session", func(n *Node, f *fakeHerdr, cwd string) {
			n.setTermStream(sidA, &termStream{cancel: func() {}})
		}},
		{"kept for a retry", func(n *Node, f *fakeHerdr, cwd string) {
			n.mu.Lock()
			n.keptStarts = map[string]keptStart{"w1:p1": {id: sidA}}
			n.mu.Unlock()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, cwd := keptAgent(t)
			tc.setup(n, f, cwd)
			t0 := time.Now()
			for _, got := range rounds(t, n, t0, 0, testGrace, 3*testGrace) {
				if len(got) != 0 {
					t.Fatalf("closed %q", got)
				}
			}
			closesNothing(t, f)
		})
	}
}

// New output on the screen starts the grace period again.
func TestFinishedTerminalScreenChangeRestartsClock(t *testing.T) {
	n, f, _ := keptAgent(t)
	f.setScreen("w1:p1", "% ")
	t0 := time.Now()
	if got := rounds(t, n, t0, 0); len(got[0]) != 0 {
		t.Fatal(got)
	}
	f.setScreen("w1:p1", "% ls\nREADME.md\n% ")
	if got := rounds(t, n, t0, testGrace); len(got[0]) != 0 {
		t.Fatalf("closed right after new output: %q", got)
	}
	if got := rounds(t, n, t0, 2*testGrace-time.Second); len(got[0]) != 0 {
		t.Fatalf("closed before a whole grace period after the change: %q", got)
	}
	if got := rounds(t, n, t0, 2*testGrace); !slices.Equal(got[0], []string{"w1:p1"}) {
		t.Fatalf("closed %q", got)
	}
}

// A close that fails is tried again on a later round; nothing is lost meanwhile.
func TestFinishedTerminalCloseFailureRetries(t *testing.T) {
	n, f, _ := keptAgent(t)
	fail := herdrFail{Code: "internal_error", Message: "busy"}
	f.reply("tab.close", fail)
	f.reply("pane.close", fail)
	t0 := time.Now()
	if got := rounds(t, n, t0, 0, testGrace); len(got[1]) != 0 {
		t.Fatalf("reported a failed close as closed: %q", got)
	}
	f.handle("tab.close", func(map[string]any) any { return map[string]any{"type": "ok"} })
	f.handle("pane.close", func(map[string]any) any { return map[string]any{"type": "ok"} })
	if got := rounds(t, n, t0, testGrace+time.Minute); !slices.Equal(got[0], []string{"w1:p1"}) {
		t.Fatalf("retry closed %q", got)
	}
}

// An engine that does not answer gets nothing closed, and its clocks are not advanced past what was seen.
func TestFinishedTerminalEngineDownClosesNothing(t *testing.T) {
	n, f, _ := keptAgent(t)
	t0 := time.Now()
	rounds(t, n, t0, 0)
	f.reply("session.snapshot", herdrFail{Code: "internal_error", Message: "down"})
	f.reply("session.list", herdrFail{Code: "internal_error", Message: "down"})
	f.reply("agent.list", herdrFail{Code: "internal_error", Message: "down"})
	for _, got := range rounds(t, n, t0, testGrace, 2*testGrace) {
		if len(got) != 0 {
			t.Fatalf("closed while the engine was down: %q", got)
		}
	}
	closesNothing(t, f)
}

// With the grace period off the terminals stay, as before #10111.
func TestFinishedTerminalGraceOff(t *testing.T) {
	t.Setenv(agentTerminalGraceEnv, "off")
	if g := agentTerminalGrace(); g != 0 {
		t.Fatalf("grace = %s", g)
	}
	n, f, _ := keptAgent(t)
	if got := n.closeFinishedRound(t.Context(), 0, time.Now().Add(time.Hour)); len(got) != 0 {
		t.Fatal(got)
	}
	closesNothing(t, f)
	for v, want := range map[string]time.Duration{"": defaultAgentTerminalGrace, "45m": 45 * time.Minute, "0": 0, "nonsense": defaultAgentTerminalGrace} {
		t.Setenv(agentTerminalGraceEnv, v)
		if got := agentTerminalGrace(); got != want {
			t.Errorf("%q: grace = %s, want %s", v, got, want)
		}
	}
}

// After its terminal is closed, the user's Resume brings the Agent back with its conversation in a new tab, which
// starts marked as an Agent's terminal.
func TestResumeAfterFinishedTerminalClosedOpensNewTab(t *testing.T) {
	n, f, cwd := keptAgent(t)
	f.after(func(c herdrCall) { // herdr lists the pane no more once it is closed
		if c.Method == "tab.close" || c.Method == "pane.close" {
			f.reply("session.snapshot", snapshot([]any{map[string]any{"workspace_id": "w1", "label": "proj", "cwd": cwd}}, nil, nil, nil))
		}
	})
	t0 := time.Now()
	if got := rounds(t, n, t0, 0, testGrace); !slices.Equal(got[1], []string{"w1:p1"}) {
		t.Fatalf("closed %q", got)
	}
	n.claudeStart, n.claudeReady = 200*time.Millisecond, 200*time.Millisecond // the fake shell never runs claude
	_, _ = request(t, n, "session.start", map[string]any{"resume_session_id": sidA})
	var created []herdrCall
	for _, c := range f.calls() {
		if c.Method == "tab.create" || c.Method == "workspace.create" {
			created = append(created, c)
		}
	}
	if len(created) == 0 {
		t.Fatalf("no new tab: %q", f.trace())
	}
	env, _ := created[0].Params["env"].(map[string]any)
	if env["PERSONA_AGENT"] != "1" {
		t.Errorf("the new tab's shell has no PERSONA_AGENT: %v", created[0].Params)
	}
	var typed string
	for _, c := range f.calls() {
		if c.Method == "pane.send_input" {
			typed += str(c.Params["text"])
		}
	}
	if !strings.Contains(typed, "--resume "+sidA) || !strings.Contains(typed, agentMarkEnv+" ") {
		t.Errorf("typed %q", typed)
	}
}

// fakeOrca is an Orca-like host for the in-memory ownership: no metadata marks, no foreground names; whether a pane
// holds its shell at rest (restingShell) by pane, unknown (an error) for the others. An agent process (procs) is no
// answer either way: the grace close never asks it (#17039).
type fakeOrca struct {
	host.Host
	panes  []host.Agent
	procs  map[string][]int
	rest   map[string]bool
	closed []string
}

func (h *fakeOrca) Kind() string { return host.Orca }
func (h *fakeOrca) Snapshot(context.Context) (*host.Layout, error) {
	return &host.Layout{Panes: slices.Clone(h.panes)}, nil
}
func (h *fakeOrca) Agents(context.Context) ([]host.Agent, error) { return nil, nil }
func (h *fakeOrca) Read(context.Context, host.PaneRef, host.ReadOpts) (string, error) {
	return "% ", nil
}
func (h *fakeOrca) PaneProcesses(_ context.Context, p host.PaneRef) ([]int, error) {
	return h.procs[p.String()], nil
}
func (h *fakeOrca) PaneShellAtRest(_ context.Context, p host.PaneRef) (bool, error) {
	rest, ok := h.rest[p.String()]
	if !ok {
		return false, errors.New("cannot tell")
	}
	return rest, nil
}
func (h *fakeOrca) RenameSpace(context.Context, string, string) error { return nil }
func (h *fakeOrca) RenameSlot(context.Context, string, string) error  { return nil }
func (h *fakeOrca) Close(_ context.Context, p host.PaneRef, _, _ string) error {
	h.closed = append(h.closed, p.String())
	return nil
}
func (h *fakeOrca) CreateSpace(context.Context, string, string) (*host.Created, error) {
	return nil, nil
}
func (h *fakeOrca) CreateSlot(context.Context, string, string, string) (*host.Created, error) {
	return nil, nil
}
func (h *fakeOrca) Spaces(context.Context) ([]host.Space, error)          { return nil, nil }
func (h *fakeOrca) Run(context.Context, host.PaneRef, string) error       { return nil }
func (h *fakeOrca) NameAgent(context.Context, host.PaneRef, string) error { return nil }

// On Orca, which keeps no marks, only the terminals the node itself opened for Agents are closed, and only when Orca
// proves the shell at rest in them: not one running something else (busy), not one Orca cannot read (quiet: no agent
// process, but a quiet dev server or an editor looks the same to the scan), and never an Orca terminal the user
// opened.
func TestFinishedOrcaTerminalUsesNodeRecord(t *testing.T) {
	n, home := testNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	agentPane, userPane, busyPane, quietPane := "orca:wt1:a", "orca:wt1:u", "orca:wt1:b", "orca:wt1:q"
	o := &fakeOrca{rest: map[string]bool{agentPane: true, userPane: true, busyPane: false}}
	for _, p := range []string{agentPane, userPane, busyPane, quietPane} {
		o.panes = append(o.panes, host.Agent{PaneID: p, TabID: p + "-tab", WorkspaceID: "wt1", Cwd: cwd})
	}
	n.hosts = host.NewRegistry(o)
	n.panes = map[string]string{}
	for _, p := range []string{agentPane, busyPane, quietPane} {
		n.agentTerminalOpened(o, host.ParsePane(p), sidA)
	}
	t0 := time.Now()
	got := rounds(t, n, t0, 0, testGrace, 3*testGrace)
	if !slices.Equal(got[1], []string{agentPane}) || len(got[2]) != 0 || !slices.Equal(o.closed, []string{agentPane}) {
		t.Fatalf("closed %q (host %q)", got, o.closed)
	}
	// A restarted node has no record: nothing is guessed.
	n.mu.Lock()
	n.agentTerms = nil
	n.mu.Unlock()
	o.rest = map[string]bool{busyPane: true, quietPane: true, userPane: true}
	if got := rounds(t, n, t0, 4*testGrace, 6*testGrace); len(got[0]) != 0 || len(got[1]) != 0 {
		t.Fatalf("closed an unrecorded terminal: %q", got)
	}
}

// herdr keeps the mark itself: the node records nothing for it (a pane id herdr reuses after a restart must not
// inherit the record).
func TestAgentTerminalOpenedSkipsMarkingHosts(t *testing.T) {
	n, _, _ := herdrNode(t)
	n.agentTerminalOpened(n.hosts.Get(host.Herdr).(host.Launcher), host.PaneRef{Kind: host.Herdr, ID: "w1:p1"}, sidA)
	if len(n.agentTerms) != 0 {
		t.Fatalf("recorded %v", n.agentTerms)
	}
}

// claudePane is the pane entry of a Claude running in a pane Persona marked for its session.
func claudePane(session, pane, tab, ws, cwd string) map[string]any {
	p := shellPane(pane, tab, ws, cwd, session)
	p["agent"] = "claude"
	return p
}

// A Codex start marks its tab "agent" before the thread is known; once it is, the mark names the thread, as a Claude
// start's names its session (#10111). A mark naming another session is not overwritten.
func TestNamePersonaTabUpgradesAgentMark(t *testing.T) {
	n, f, cwd := keptAgent(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, []any{
		shellPane("w2:p1", "w2:t1", "w2", cwd, "agent"), shellPane("w1:p1", "w1:t1", "w1", cwd, sidA)}))
	listSessions(t, n)
	f.reset()
	n.namePersonaTab(t.Context(), "w2:p1", sidB)
	n.namePersonaTab(t.Context(), "w1:p1", sidB)
	got := f.trace("pane.report_metadata")
	if len(got) != 1 || !strings.Contains(got[0], `"pane_id":"w2:p1"`) || !strings.Contains(got[0], sidB) {
		t.Fatalf("report_metadata %q", got)
	}
}
