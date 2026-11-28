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
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
)

// orcaShellPID is the shell process the fake Orca names in its evidence; withOrcaShell has processArgv name it.
const orcaShellPID = 7001

func withOrcaShell(t *testing.T, argv0 string) {
	t.Helper()
	old := processArgv
	processArgv = func(pid int) ([]string, error) {
		if pid == orcaShellPID {
			return []string{argv0}, nil
		}
		return nil, errors.New("no such process")
	}
	t.Cleanup(func() { processArgv = old })
}

// orcaPlainNode is an Orca node with one plain terminal, term_1 (pane tab1:leaf1), listed once.
func orcaPlainNode(t *testing.T) (*Node, *orcarttest.Fake, *fakeProcs, *orcaHost) {
	t.Helper()
	n, _, o, procs, _ := orcaNode(t)
	o.SetTerminals(orcaTerm("term_1", "tab1", "leaf1", ""))
	o.SetWorktrees(orcarttest.Worktree("repo::"+orcaCwd, orcaCwd, "refs/heads/feature-x"))
	if err := n.refreshHosts(t.Context()); err != nil {
		t.Fatal(err)
	}
	o.Reset()
	return n, o, procs, n.hosts.Get(host.Orca).(*orcaHost)
}

// The Orca host names what runs in a terminal with terminal.inspectProcess, fenced to the terminal's incarnation: the
// program in front, the terminal's shell when nothing is, and nothing at all (an error) when Orca marks its answer
// unverifiable or names nothing although something other than the shell is in front.
func TestOrcaPaneForeground(t *testing.T) {
	withOrcaShell(t, "-zsh")
	_, o, _, h := orcaPlainNode(t)
	pane := host.PaneRef{Kind: host.Orca, ID: "tab1:leaf1"}
	for _, tc := range []struct {
		name    string
		process map[string]any
		want    []string
	}{
		{"shell at rest", orcarttest.ShellAtRest(orcaShellPID), []string{"-zsh"}},
		{"shell with a job behind it", orcarttest.ShellWithJobs(orcaShellPID), []string{"-zsh"}},
		{"a program in front", orcarttest.Running("vi", orcaShellPID), []string{"vi"}},
		{"unverifiable", orcarttest.Unverifiable("incarnation_mismatch"), nil},
		{"unnamed child", map[string]any{"foregroundProcess": nil, "hasChildProcesses": true,
			"foregroundProcessEvidence": map[string]any{"verdict": "live"}}, nil},
		{"evidence without a verdict", map[string]any{"foregroundProcess": nil, "hasChildProcesses": false,
			"foregroundProcessEvidence": map[string]any{}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o.SetProcess("term_1", tc.process)
			got, err := h.PaneForeground(t.Context(), pane)
			if tc.want == nil {
				if err == nil {
					t.Fatalf("named %q, want an error", got)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("PaneForeground = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	for _, p := range o.Params("terminal.inspectProcess") {
		if p["terminal"] != "term_1" || p["expectedIncarnationId"] != "inc-term_1" {
			t.Errorf("inspectProcess %v: not fenced to the terminal's incarnation", p)
		}
	}
	if inv := o.Invalid(); len(inv) != 0 {
		t.Errorf("params Orca would refuse: %v", inv)
	}
	// A shell whose process cannot be read is the user's shell, which Orca opens by default.
	withOrcaShell(t, "")
	o.SetProcess("term_1", orcarttest.ShellAtRest(orcaShellPID+1))
	if got, err := h.PaneForeground(t.Context(), pane); err != nil || !slices.Equal(got, []string{herdrPaneShell()}) {
		t.Errorf("unreadable shell: %q, %v", got, err)
	}
}

// An Orca without terminal.inspectProcess: the process scan names a supported agent in front, and nothing else can be
// told (a shell and a dev server look the same to it), so the rest is an error, never "only a shell".
func TestOrcaPaneForegroundWithoutInspect(t *testing.T) {
	_, o, procs, h := orcaPlainNode(t)
	pane := host.PaneRef{Kind: host.Orca, ID: "tab1:leaf1"}
	if got, err := h.PaneForeground(t.Context(), pane); err == nil || !errors.Is(err, host.ErrUnsupported) {
		t.Errorf("no agent, no inspectProcess: %q, %v", got, err)
	}
	if rest, err := h.PaneShellAtRest(t.Context(), pane); rest || !errors.Is(err, host.ErrUnsupported) {
		t.Errorf("at rest without inspectProcess: %v, %v", rest, err)
	}
	procs.set("tab1:leaf1", orcaProc{pid: 4242, agent: "claude"})
	h.procsAt = time.Time{}
	if got, err := h.PaneForeground(t.Context(), pane); err != nil || !slices.Equal(got, []string{"claude"}) {
		t.Errorf("claude in front: %q, %v", got, err)
	}
	if c := o.Count("terminal.inspectProcess"); c == 0 {
		t.Error("inspectProcess not asked first")
	}
}

// Only Orca's proof of an idle shell is "at rest": a job behind the shell, a program in front or an answer Orca cannot
// verify is not.
func TestOrcaPaneShellAtRest(t *testing.T) {
	_, o, _, h := orcaPlainNode(t)
	pane := host.PaneRef{Kind: host.Orca, ID: "tab1:leaf1"}
	for _, tc := range []struct {
		name    string
		process map[string]any
		rest    bool
		err     bool
	}{
		{"idle shell", orcarttest.ShellAtRest(orcaShellPID), true, false},
		{"job behind the shell", orcarttest.ShellWithJobs(orcaShellPID), false, false},
		{"program in front", orcarttest.Running("npm", orcaShellPID), false, false},
		{"unverifiable", orcarttest.Unverifiable("process_table_unreadable"), false, true},
	} {
		o.SetProcess("term_1", tc.process)
		rest, err := h.PaneShellAtRest(t.Context(), pane)
		if rest != tc.rest || (err != nil) != tc.err {
			t.Errorf("%s: %v, %v", tc.name, rest, err)
		}
	}
}

// The grace close (#10111) on Orca closes a terminal Persona opened for an Agent only once Orca proves its shell at
// rest; a long-running program in it (a dev server, an editor, a CLI Persona does not know), a job behind the shell,
// an answer Orca cannot verify and an Orca without terminal.inspectProcess keep it, however long (#17039).
func TestOrcaGraceCloseOnlyAnIdleShell(t *testing.T) {
	for _, tc := range []struct {
		name    string
		process map[string]any // nil: an Orca without terminal.inspectProcess
		closes  bool
	}{
		{"idle shell", orcarttest.ShellAtRest(orcaShellPID), true},
		{"dev server", orcarttest.Running("node", orcaShellPID), false},
		{"pi in front", orcarttest.Running("pi", orcaShellPID), false},
		{"job behind the shell", orcarttest.ShellWithJobs(orcaShellPID), false},
		{"unverifiable", orcarttest.Unverifiable("incarnation_mismatch"), false},
		{"older Orca", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, o, _, h := orcaPlainNode(t)
			if tc.process != nil {
				o.SetProcess("term_1", tc.process)
			}
			n.agentTerminalOpened(h, host.PaneRef{Kind: host.Orca, ID: "tab1:leaf1"}, sidA)
			o.SetScreen("term_1", "done.\n% ")
			got := rounds(t, n, time.Now(), 0, testGrace, 3*testGrace)
			closed := slices.Concat(got...)
			if tc.closes != (len(closed) == 1 && closed[0] == "orca:tab1:leaf1") || len(closed) > 1 {
				t.Fatalf("closed %q", got)
			}
			if closes := slices.ContainsFunc(o.Writes(), func(w orcarttest.Write) bool { return w.Close }); closes != tc.closes {
				t.Fatalf("terminal.close sent: %v (writes %v)", closes, o.Writes())
			}
			if inv := o.Invalid(); len(inv) != 0 {
				t.Errorf("params Orca would refuse: %v", inv)
			}
		})
	}
}

// The terminal list labels a terminal a finished Agent left on Orca with its session and names the program in front
// of each plain terminal; Close finished terminals lists it and closes it only after checking again that only a shell
// is in front, and a terminal the user opened is neither labelled nor listed.
func TestOrcaFinishedTerminalsByHand(t *testing.T) {
	withOrcaShell(t, "-zsh")
	n, _, o, _, _ := orcaNode(t)
	o.SetTerminals(orcaTerm("term_1", "tab1", "leaf1", ""), orcaTerm("term_2", "tab2", "leaf2", ""))
	o.SetWorktrees(orcarttest.Worktree("repo::"+orcaCwd, orcaCwd, "refs/heads/feature-x"))
	if err := n.refreshHosts(t.Context()); err != nil {
		t.Fatal(err)
	}
	h := n.hosts.Get(host.Orca).(*orcaHost)
	n.agentTerminalOpened(h, host.PaneRef{Kind: host.Orca, ID: "tab1:leaf1"}, sidA)
	o.SetProcess("term_1", orcarttest.Running("vi", orcaShellPID))
	o.SetProcess("term_2", orcarttest.Running("htop", orcaShellPID))

	res, err := n.listPanes(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	n.paneForegrounds(t.Context(), &res)
	items := map[string]proto.PaneItem{}
	for _, hp := range res.Hosts {
		for _, p := range hp.Panes {
			items[p.Pane] = p
		}
	}
	if p := items["orca:tab1:leaf1"]; p.AgentSession != sidA || p.Foreground != "vi" {
		t.Errorf("Agent's terminal: %+v", p)
	}
	if p := items["orca:tab2:leaf2"]; p.AgentSession != "" || p.Foreground != "htop" {
		t.Errorf("user's terminal: %+v", p)
	}

	// Something runs in it: listed as kept, not closed.
	out, err := n.closeFinished(t.Context(), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if closed, kept := out["closed"].([]finishedTerminal), out["kept"].([]finishedTerminal); len(closed) != 0 || len(kept) != 1 ||
		kept[0].Pane != "orca:tab1:leaf1" || kept[0].SessionID != sidA {
		t.Fatalf("busy: %+v", out)
	}
	// Its shell at rest: closed, the user's terminal untouched.
	o.SetProcess("term_1", orcarttest.ShellAtRest(orcaShellPID))
	o.SetProcess("term_2", orcarttest.ShellAtRest(orcaShellPID))
	out, err = n.closeFinished(t.Context(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	if closed := out["closed"].([]finishedTerminal); len(closed) != 1 || closed[0].Pane != "orca:tab1:leaf1" {
		t.Fatalf("preview: %+v", out)
	}
	if w := o.Writes(); len(w) != 0 {
		t.Fatalf("the preview closed %v", w)
	}
	if _, err := n.closeFinished(t.Context(), "", false); err != nil {
		t.Fatal(err)
	}
	if w := o.Writes(); len(w) != 1 || !w[0].Close || w[0].Handle != "term_1" {
		t.Fatalf("closed %v", w)
	}
}

// A Resume within the grace period runs in the shell the Agent left on Orca, as on herdr: no new terminal, the
// session's launcher with --resume typed into the kept shell after Orca read it again with only its shell in front.
// Something running there instead keeps it out: a new terminal opens and the kept one gets nothing.
func TestOrcaResumeInTheKeptShell(t *testing.T) {
	for _, busy := range []bool{false, true} {
		t.Run(map[bool]string{false: "at rest", true: "busy"}[busy], func(t *testing.T) {
			withOrcaShell(t, "-zsh")
			n, _, o, _, home := orcaNode(t)
			n.claudeStart, n.claudeReady = 200*time.Millisecond, 200*time.Millisecond // the fake terminal never runs claude
			projDir := mkdir(t, filepath.Join(home, "my-project"))
			sub := mkdir(t, filepath.Join(projDir, "svc"))
			o.SetWorktrees(orcarttest.Worktree("repo1::"+projDir, projDir, "refs/heads/main"))
			kept := orcarttest.Terminal("term_k", "tabK", "leafK", "repo1::"+projDir, projDir, "refs/heads/main", "zsh", "claude")
			o.SetTerminals(kept)
			const id = "0e000000-0000-4000-8000-000000000017"
			writeTranscript(t, home, sub, id, time.Now().Add(-time.Hour), `{"type":"custom-title","customTitle":"x"}`)
			if err := n.refreshHosts(t.Context()); err != nil {
				t.Fatal(err)
			}
			h := n.hosts.Get(host.Orca).(*orcaHost)
			n.agentTerminalOpened(h, host.PaneRef{Kind: host.Orca, ID: "tabK:leafK"}, id)
			if busy {
				o.SetProcess("term_k", orcarttest.Running("npm", orcaShellPID))
			} else {
				o.SetProcess("term_k", orcarttest.ShellAtRest(orcaShellPID))
			}
			starts := orcaTypedStarts(o)
			o.Reset()
			_, _ = request(t, n, "session.start", map[string]any{"session_id": id, "resume_session_id": id, "cwd": sub})
			waitFor(t, "the resume typed", func() bool { return strings.Contains(strings.Join(starts(), "\n"), "--resume "+id) })
			sends := o.Params("terminal.send")
			if busy {
				if c := o.Trace("terminal.create"); len(c) != 1 {
					t.Fatalf("busy kept shell: terminal.create %v", c)
				}
				for _, p := range sends {
					if p["terminal"] == "term_k" {
						t.Fatalf("typed into the busy kept shell: %v", p)
					}
				}
				return
			}
			if c := o.Trace("terminal.create"); len(c) != 0 {
				t.Fatalf("opened a new terminal: %v", c)
			}
			if len(sends) == 0 || sends[0]["terminal"] != "term_k" || sends[0]["expectedIncarnationId"] != "inc-term_k" {
				t.Fatalf("sends %v", sends)
			}
			if text, _ := sends[0]["text"].(string); !strings.HasPrefix(text, "cd -- "+shellQuote(sub)+" && ") {
				t.Errorf("not started in the session's folder: %q", text)
			}
			if inv := o.Invalid(); len(inv) != 0 {
				t.Errorf("params Orca would refuse: %v", inv)
			}
		})
	}
}

// An adopted terminal (Run Agent Here, the kept shell) takes one command, and only while Orca reads the same process
// with just its shell in front: a program in front, an answer Orca cannot verify, an older Orca and a second Run are
// refused with nothing typed.
func TestOrcaAdoptedRunIsGuarded(t *testing.T) {
	ctx := context.Background()
	pane := host.PaneRef{Kind: host.Orca, ID: "tab1:leaf1"}
	for _, tc := range []struct {
		name    string
		process map[string]any
		types   bool
	}{
		{"shell at rest", orcarttest.ShellAtRest(orcaShellPID), true},
		{"shell with a job", orcarttest.ShellWithJobs(orcaShellPID), true},
		{"program in front", orcarttest.Running("vi", orcaShellPID), false},
		{"unverifiable", orcarttest.Unverifiable("incarnation_mismatch"), false},
		{"older Orca", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, o, _, h := orcaPlainNode(t)
			if tc.process != nil {
				o.SetProcess("term_1", tc.process)
			}
			if err := h.AdoptPane(ctx, pane, orcaCwd); err != nil {
				t.Fatal(err)
			}
			if h.launchable(pane) {
				t.Error("an adopted terminal counts as a fresh one's unused launch")
			}
			err := h.Run(ctx, pane, "claude")
			if tc.types != (err == nil) || err != nil && !errors.Is(err, host.ErrNotDelivered) {
				t.Fatalf("Run: %v", err)
			}
			want := 0
			if tc.types {
				want = 1
			}
			if w := o.Writes(); len(w) != want || want == 1 && w[0].Text != "cd -- "+shellQuote(orcaCwd)+" && claude\r" {
				t.Fatalf("typed %v", w)
			}
			if err := h.Run(ctx, pane, "claude"); err == nil || len(o.Writes()) != want {
				t.Fatalf("a second Run: %v, %v", err, o.Writes())
			}
		})
	}
	// A terminal that is not writable, or runs another process now, gets no grant / no command.
	_, o, _, h := orcaPlainNode(t)
	o.SetProcess("term_1", orcarttest.ShellAtRest(orcaShellPID))
	if err := h.AdoptPane(ctx, pane, orcaCwd); err != nil {
		t.Fatal(err)
	}
	o.Update("term_1", map[string]any{"incarnationId": "inc-other"})
	if err := h.Run(ctx, pane, "claude"); err == nil || len(o.Writes()) != 0 {
		t.Fatalf("new incarnation: %v, %v", err, o.Writes())
	}
	o.Update("term_1", map[string]any{"writable": false})
	if err := h.AdoptPane(ctx, pane, orcaCwd); err == nil || !errors.Is(err, host.ErrNotDelivered) {
		t.Fatalf("not writable: %v", err)
	}
	if err := h.AdoptPane(ctx, pane, orcaCwd+"/a\tb"); refusalCode(err) != proto.CodeDirectoryUntypable {
		t.Fatalf("untypable directory: %v", err)
	}
}
