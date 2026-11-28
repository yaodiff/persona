package node

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
)

// orcaPromptScreen has o answer every terminal.read with a shell prompt, so a start types its line at once.
func orcaPromptScreen(o *orcarttest.Fake) {
	o.Handle("terminal.read", func(p map[string]any) any {
		return map[string]any{"terminal": map[string]any{"handle": p["terminal"], "tail": []string{"bash-5.2$ "}, "source": "screen"}}
	})
}

// envOfCreate is a terminal.create's env param (nil when it has none).
func envOfCreate(p map[string]any) map[string]any {
	env, _ := p["env"].(map[string]any)
	return env
}

// An Orca start's terminal is made with startShellEnv as terminal.create's env (host.EnvLauncher, #17036): the shell's
// startup files see the Agent mark and the frameworks' switches before anything is typed. The typed start line still
// unsets the switches and keeps the mark (agentCommand), and the create carries no command: the start line is typed
// through the launch grant as before.
func TestOrcaStartTerminalCarriesStartupEnv(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, _, o, _, home := orcaNode(t)
	n.dir = filepath.Join(home, ".persona")
	repoDir := mkdir(t, filepath.Join(home, "proj"))
	o.SetWorktrees(orcarttest.Worktree("wt-proj", repoDir, "refs/heads/main"))
	orcaPromptScreen(o)
	starts := orcaTypedStarts(o)
	if _, err := request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": repoDir, "async": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start line", func() bool { return len(starts()) > 0 })
	creates := o.Params("terminal.create")
	if len(creates) != 1 {
		t.Fatalf("terminal.create %v", o.Trace("terminal.create"))
	}
	wantStartEnv(t, "terminal.create", envOfCreate(creates[0]))
	if _, ok := creates[0]["command"]; ok {
		t.Errorf("a start's terminal got a startup command: %v", creates[0])
	}
	cmd := starts()[0]
	head, _, ok := strings.Cut(cmd, " && ")
	if !ok || !strings.HasPrefix(head, "unset ") {
		t.Fatalf("the start line does not unset the startup variables: %q", cmd)
	}
	names := strings.Fields(strings.TrimPrefix(head, "unset "))
	for _, k := range startupOnlyEnv {
		if !slices.Contains(names, k) {
			t.Errorf("%s is not unset: %q", k, cmd)
		}
	}
	if !strings.Contains(cmd, " "+agentMarkEnv+" ") {
		t.Errorf("the Agent mark is not kept: %q", cmd)
	}
	if bad := o.Invalid(); len(bad) != 0 {
		t.Errorf("Orca's schema refused %v", bad)
	}
}

// An Orca whose terminal.create refuses env (invalid_argument before anything is made) gets the create again without
// it, and the start goes on in that one terminal, its shell without the variables.
func TestOrcaRefusingEnvGetsTheTerminalWithout(t *testing.T) {
	withPaneShell(t, "/bin/zsh")
	n, _, o, _, home := orcaNode(t)
	repoDir := mkdir(t, filepath.Join(home, "proj"))
	o.SetWorktrees(orcarttest.Worktree("wt-proj", repoDir, "refs/heads/main"))
	o.Unknown("terminal.create", "env")
	orcaPromptScreen(o)
	if _, err := request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": repoDir, "async": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start line", func() bool { return len(o.Writes()) > 0 })
	creates := o.Params("terminal.create")
	if len(creates) != 2 || envOfCreate(creates[0]) == nil || envOfCreate(creates[1]) != nil {
		t.Fatalf("terminal.create %v", o.Trace("terminal.create"))
	}
	st, _ := n.state.get(startID)
	if st.OrcaTerminal != nil {
		t.Fatal("an old runtime that refused the startup environment created close authority")
	}
	if creates[0]["clientMutationId"] != creates[1]["clientMutationId"] {
		t.Errorf("the create without env is another request: %v", o.Trace("terminal.create"))
	}
	l, err := (&orcart.Client{MetaPath: o.MetaPath}).Terminals(context.Background(), false)
	if err != nil || len(l.Terminals) != 1 {
		t.Fatalf("terminals %v, %v", l, err)
	}
	if w := o.Writes()[0]; w.Handle != l.Terminals[0].Handle || !strings.HasPrefix(w.Text, "cd -- "+shellQuote(repoDir)+" && ") {
		t.Errorf("the start did not go on in the terminal made without env: %+v", w)
	}
}

// New Terminal in a folder (panes.new with a cwd) on Orca opens the shell in that folder: terminal.create's cwd is not
// honoured, so the create carries `cd <folder>` as its command, delivered by Orca once the shell is ready. Nothing is
// typed into the terminal afterwards, the user's shell gets no launch grant and no start environment, and a terminal
// for the worktree's own folder carries no command at all.
func TestOrcaNewTerminalOpensInTheFolder(t *testing.T) {
	n, _, o, _, home := orcaNode(t)
	repoDir := mkdir(t, filepath.Join(home, "proj"))
	sub := mkdir(t, filepath.Join(repoDir, "src", "my dir"))
	o.SetWorktrees(orcarttest.Worktree("wt-proj", repoDir, "refs/heads/main"))
	if _, err := n.listPanes(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := request(t, n, "panes.new", map[string]any{"workspace_id": "wt-proj", "cwd": sub})
	if err != nil {
		t.Fatal(err)
	}
	creates := o.Params("terminal.create")
	if len(creates) != 1 {
		t.Fatalf("terminal.create %v", o.Trace("terminal.create"))
	}
	if got := creates[0]["command"]; got != "cd "+shellQuote(sub) {
		t.Errorf("command = %v, want cd into %s", got, sub)
	}
	if got := creates[0]["startupCommandDelivery"]; got != "shell-ready" {
		t.Errorf("startupCommandDelivery = %v", got)
	}
	if env := envOfCreate(creates[0]); env != nil {
		t.Errorf("the user's shell got a start's environment: %v", env)
	}
	if w := o.Writes(); len(w) != 0 {
		t.Errorf("typed into the new shell: %+v", w)
	}
	h := n.hosts.Get(host.Orca).(*orcaHost)
	pane, _ := res["pane"].(string)
	ref := host.ParsePane(pane)
	if ref.ID == "" || h.launchable(ref) {
		t.Errorf("the user's shell %q has a launch grant", pane)
	}
	if err := h.Run(context.Background(), ref, "echo hi"); err == nil {
		t.Error("Run typed into the user's shell")
	}

	o.Reset()
	if _, err := request(t, n, "panes.new", map[string]any{"workspace_id": "wt-proj"}); err != nil {
		t.Fatal(err)
	}
	if creates := o.Params("terminal.create"); len(creates) != 1 || creates[0]["command"] != nil {
		t.Errorf("a terminal at the worktree's folder: %v", o.Trace("terminal.create"))
	}
	if bad := o.Invalid(); len(bad) != 0 {
		t.Errorf("Orca's schema refused %v", bad)
	}
}

// Open Terminal Here for a folder outside every Orca worktree: the terminal goes into a worktree Orca shows as its
// container, and the shell is put in the folder the same way.
func TestOrcaNewTerminalOutsideAWorktree(t *testing.T) {
	n, _, o, _, home := orcaNode(t)
	repoDir := mkdir(t, filepath.Join(home, "proj"))
	other := mkdir(t, filepath.Join(home, "notes"))
	o.SetWorktrees(orcarttest.Worktree("wt-proj", repoDir, "refs/heads/main"))
	if _, err := n.listPanes(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := request(t, n, "panes.new", map[string]any{"cwd": other}); err != nil {
		t.Fatal(err)
	}
	creates := o.Params("terminal.create")
	if len(creates) != 1 || creates[0]["worktree"] != "id:wt-proj" || creates[0]["command"] != "cd "+shellQuote(other) {
		t.Errorf("terminal.create %v", o.Trace("terminal.create"))
	}
}

func TestOrcaCdLine(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct{ cwd, want string }{
		{root, ""},
		{root + "/", ""},
		{filepath.Join(root, "a b"), "cd '" + filepath.Join(root, "a b") + "'"},
		{"relative/dir", ""},
		{filepath.Join(root, "it's"), "cd '" + filepath.Join(root, `it'\''s`) + "'"},
	} {
		if got := orcaCdLine(tc.cwd, root); got != tc.want {
			t.Errorf("orcaCdLine(%q) = %q, want %q", tc.cwd, got, tc.want)
		}
	}
}
