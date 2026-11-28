package node

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
)

func TestOtherAgentStartCommand(t *testing.T) {
	const prompt = "a 'quoted' prompt; $(printf unexpected)"
	cmd := otherAgentCommand("wrapper", "00000000-0000-4000-8000-000000000001", prompt, "/bin/sh")
	// Execute only a fake shell function: a first prompt must remain a single literal argument.
	out, err := exec.Command("sh", "-c", `wrapper() { printf '%s\n' "$#" "$@"; }; `+cmd).Output()
	if err != nil || string(out) != "3\nresume\n00000000-0000-4000-8000-000000000001\n"+prompt+"\n" {
		t.Fatalf("command arguments = %q (%v)", out, err)
	}
	if strings.Contains(cmd, "--session-id") || strings.Contains(cmd, "--settings") || strings.Contains(cmd, "--mcp-config") {
		t.Fatal("a non-Claude start must not receive Claude flags")
	}
}

// Typed into a shell without start files, ^U would erase the line and a CR would then run what follows it; a tab
// would complete the quoted word, close the quote and leave the rest to the shell.
func TestOtherAgentStartCommandHasNoControls(t *testing.T) {
	const prompt = "hi\x15touch pwned\r\x03\x1b[201~\t;touch pwned"
	cmd := otherAgentCommand("wrapper", "00000000-0000-4000-8000-000000000001", prompt, "/bin/sh")
	if strings.ContainsFunc(cmd, pasteControl) || strings.Contains(cmd, "\t") {
		t.Fatalf("command %q keeps a control character or a tab", cmd)
	}
	out, err := exec.Command("sh", "-c", `wrapper() { printf '%s\n' "$#" "$@"; }; `+cmd).Output()
	if want := "3\nresume\n00000000-0000-4000-8000-000000000001\nhi<U+0015>touch pwned\r<U+0003><ESC>[201~ ;touch pwned\n"; err != nil || string(out) != want {
		t.Fatalf("command arguments = %q (%v), want %q", out, err, want)
	}
}

// A line break inside quotes continues the command in the sh family (fish too) but ends it in csh, quote open or not,
// and runs the next line on its own; a shell not known is taken for one like csh.
func TestOtherAgentStartCommandLineBreaks(t *testing.T) {
	const prompt = "one\ttwo\r\nthree\rfour\nfive"
	for _, tc := range []struct {
		shells []string
		want   string
	}{
		{[]string{"/bin/zsh"}, "traex 'one two\r\nthree\rfour\nfive'"},
		{[]string{"-fish"}, "traex 'one two\r\nthree\rfour\nfive'"},
		{[]string{"/bin/tcsh"}, "traex 'one two three four five'"},
		{[]string{"/bin/zsh", "csh"}, "traex 'one two three four five'"},
		{nil, "traex 'one two three four five'"},
	} {
		if got := otherAgentCommand(traexAgent, "", prompt, tc.shells...); got != tc.want {
			t.Errorf("%v: command %q, want %q", tc.shells, got, tc.want)
		}
	}
}

// startOtherAgent builds its command for the shell of the pane it made: a zsh pane sources it from a start file, and a
// tcsh pane (no `.`) gets it typed out with the first message on one line.
func TestOtherAgentStartTypesForThePaneShell(t *testing.T) {
	for shell, tc := range map[string]struct{ model, want string }{
		"/bin/zsh":  {"", "traex 'a b\nc'"},
		"/bin/tcsh": {"example-fast", "traex -m example-fast -- 'a b c'"},
	} {
		t.Run(shell, func(t *testing.T) {
			withPaneShell(t, shell)
			n, f, home := herdrNode(t)
			f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
			f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.after(func(c herdrCall) {
				if c.Method == "pane.send_input" {
					cancel()
				}
			})
			_, _ = n.startOtherAgent(ctx, n.hosts.Get(host.Herdr).(*herdrHost), home, traexAgent, traexAgent, "a\tb\nc", "", "w1", "", modelExecution{Model: tc.model})
			want := tc.want
			if shell == "/bin/zsh" {
				want = inAgentEnv(n.dir, want, shellQuote)
			}
			wantCall := call("pane.send_input", map[string]any{"pane_id": "w1:p2", "keys": []string{"Enter"}, "text": want})
			if got := f.trace("pane.send_input"); len(got) != 1 || got[0] != wantCall {
				t.Errorf("typed %v, want %s", got, wantCall)
			}
			for _, c := range f.calls() {
				if c.Method == "pane.send_input" && (c.Typed != "") != (shell == "/bin/zsh") {
					t.Errorf("typed %q: a start file only for zsh", c.Typed)
				}
			}
		})
	}
}

// Orca reports no shell, so the login shell is the guess: one that sources a file gets the start file, which keeps the
// first message's line break as herdr's does (#5450); for csh, where a typed line break would run what follows it,
// the command is typed with the message on one line.
func TestOtherAgentStartFlattensForAGuessedShell(t *testing.T) {
	for shell, want := range map[string]string{
		"/bin/zsh":  "traex -m example-fast -c 'model_reasoning_effort=xhigh' -- 'a\nb'",
		"/bin/tcsh": "traex -m example-fast -c 'model_reasoning_effort=xhigh' -- 'a b'",
	} {
		t.Run(shell, func(t *testing.T) {
			withPaneShell(t, shell)
			n, _, o, _, home := orcaNode(t)
			projDir := filepath.Join(home, "my-project")
			mkdir(t, projDir)
			o.SetWorktrees(orcarttest.Worktree("repo1::"+projDir, projDir, "refs/heads/main"))
			starts := orcaTypedStarts(o)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second) // until the agent would show up
			defer cancel()
			_, _ = n.start(ctx, projDir, traexAgent, "a\nb", "", "", "", "xhigh", "", "", "", modelExecution{Model: "example-fast"})
			ran := starts()
			for _, p := range o.Params("terminal.send") {
				if text, _ := p["text"].(string); strings.Contains(text, traexAgent+" ") {
					if strings.Contains(text, "\n") {
						t.Errorf("a line break typed: %q", text)
					}
					ran = append(ran, strings.TrimSuffix(text, "\r"))
				}
			}
			if len(ran) != 1 || !strings.HasSuffix(ran[0], " "+want) {
				t.Errorf("ran %q, want one command ending %q", ran, want)
			}
		})
	}
}

// Resuming a non-Claude terminal session outside its space keeps the kept record's folder inside that space (#12216).
func TestOtherAgentResumeKeepsRecordFolderInSpace(t *testing.T) {
	const run = "0a1b2c3d-7777-4f60-8a7b-9c0d1e2f3a44"
	n, f, home := herdrNode(t)
	charts := mkdir(t, filepath.Join(home, ".persona", "health", "charts"))
	elsewhere := mkdir(t, filepath.Join(home, "elsewhere"))
	n.state.put(run, sessionState{Launcher: "codex-wrap", Cwd: charts, Model: "m1", ModelEffort: "high"})

	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	f.reply("session.snapshot", snapshot(nil, nil, []any{map[string]any{
		"agent":         "codex",
		"agent_status":  "idle",
		"pane_id":       "w1:p2",
		"tab_id":        "w1:t2",
		"workspace_id":  "w1",
		"cwd":           elsewhere,
		"agent_session": map[string]any{"kind": "id", "value": run},
	}}, nil))

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := n.startOtherAgent(ctx, n.hosts.Get(host.Herdr).(*herdrHost), elsewhere, "codex-wrap", "codex", "", run, "w1", ""); err != nil {
		t.Fatalf("startOtherAgent resume: %v", err)
	}
	st, ok := n.state.get(run)
	if !ok {
		t.Fatal("missing session state after resume")
	}
	if st.Cwd != charts {
		t.Fatalf("record Cwd = %q, want %q (stay in health space)", st.Cwd, charts)
	}
}
