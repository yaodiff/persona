package node

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
)

// Typing into Orca terminals beyond the agents the process scan knows (#17040), against the fake runtime.

// orcaWrites are the texts that went into handle so far (no closes), one per write.
func orcaWrites(o *orcarttest.Fake, handle string) []string {
	var out []string
	for _, w := range o.Writes() {
		if w.Handle == handle && !w.Close {
			out = append(out, w.Text)
		}
	}
	return out
}

// Text goes into an Orca terminal only where something reads it as typing: a program other than a shell that Orca
// names in front (pi, opencode, an editor). A shell in front, an answer Orca cannot verify and an Orca without
// terminal.inspectProcess get nothing, and keys that type nothing (Enter, Ctrl-C) still go to whatever is there.
func TestOrcaTypesIntoACLIInFront(t *testing.T) {
	ctx := context.Background()
	pane := host.PaneRef{Kind: host.Orca, ID: "tab1:leaf1"}
	for _, tc := range []struct {
		name    string
		process map[string]any // nil: an Orca without terminal.inspectProcess
		types   bool
	}{
		{"pi in front", orcarttest.Running("pi", orcaShellPID), true},
		{"opencode in front", orcarttest.Running("opencode", orcaShellPID), true},
		{"a shell at rest", orcarttest.ShellAtRest(orcaShellPID), false},
		{"a shell with a job", orcarttest.ShellWithJobs(orcaShellPID), false},
		{"a shell Orca names", orcarttest.Running("zsh", orcaShellPID), false},
		{"unverifiable", orcarttest.Unverifiable("incarnation_mismatch"), false},
		{"older Orca", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, o, _, h := orcaPlainNode(t)
			if tc.process != nil {
				o.SetProcess("term_1", tc.process)
			}
			errText := h.SendText(ctx, pane, "hello")
			errKey := h.SendKeys(ctx, pane, "y")
			if tc.types != (errText == nil) || tc.types != (errKey == nil) {
				t.Fatalf("SendText: %v, SendKeys y: %v", errText, errKey)
			}
			for _, err := range []error{errText, errKey} {
				if err != nil && !errors.Is(err, host.ErrNotDelivered) {
					t.Errorf("a refusal not marked as nothing typed: %v", err)
				}
			}
			want := []string{}
			if tc.types {
				want = []string{"hello", "y"}
			}
			if got := orcaWrites(o, "term_1"); !slices.Equal(got, want) && !(len(got) == 0 && len(want) == 0) {
				t.Fatalf("typed %q, want %q", got, want)
			}
			if err := h.SendKeys(ctx, pane, "enter"); err != nil {
				t.Errorf("enter: %v", err)
			}
			if inv := o.Invalid(); len(inv) != 0 {
				t.Errorf("params Orca would refuse: %v", inv)
			}
		})
	}
}

// A message to a CLI the process scan does not know goes in the way herdr's does for an agent it does not detect: the
// Orca host says no agent (host.ErrNoAgent) while Orca names another program in front, and the node pastes the text
// and presses Enter itself (send.go prompt, typeable). With a shell in front it is refused, nothing typed.
func TestOrcaPromptToACLIInFront(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name    string
		process map[string]any
		types   bool
	}{
		{"pi in front", orcarttest.Running("pi", orcaShellPID), true},
		{"a shell in front", orcarttest.ShellAtRest(orcaShellPID), false},
		{"unverifiable", orcarttest.Unverifiable("process_table_unreadable"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withOrcaShell(t, "-zsh")
			n, o, _, _ := orcaPlainNode(t)
			o.SetProcess("term_1", tc.process)
			err := n.prompt(ctx, "orca:tab1:leaf1", "fix the build\nand test it")
			got := orcaWrites(o, "term_1")
			if !tc.types {
				if err == nil || !errors.Is(err, host.ErrNotDelivered) || len(got) != 0 {
					t.Fatalf("prompt: %v, typed %q", err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("prompt: %v", err)
			}
			want := []string{pasteStart + "fix the build\nand test it" + pasteEnd, "\r"}
			if !slices.Equal(got, want) {
				t.Fatalf("typed %q, want %q", got, want)
			}
		})
	}
}

// A failed start's kept tab on Orca takes the owner's answer to its shell's question (start.keys / start.continue,
// withStartTyping), letters included, though no agent runs there: only in a terminal this host opened that still runs
// the same process, and only for typing that names that tab. Any other pane, or the same pane typed into without the
// start's answer, gets nothing.
func TestOrcaStartTypingOnlyInItsOwnTab(t *testing.T) {
	ctx := context.Background()
	_, o, _, h := orcaPlainNode(t)
	o.SetWorktrees(orcarttest.Worktree("repo::"+orcaCwd, orcaCwd, "refs/heads/feature-x"))
	created, err := h.CreateSlot(ctx, "repo::"+orcaCwd, orcaCwd, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.terminals(ctx); err != nil {
		t.Fatal(err)
	}
	handle := "term_fake_2" // the fake's second terminal (term_1 is listed already)
	o.SetProcess(handle, orcarttest.ShellAtRest(orcaShellPID))
	o.SetProcess("term_1", orcarttest.ShellAtRest(orcaShellPID))
	own := withStartTyping(ctx, created.Pane.String())
	if err := h.SendKeys(ctx, created.Pane, "n"); err == nil {
		t.Fatal("a shell got a letter without the start's answer")
	}
	if err := h.SendKeys(own, created.Pane, "n"); err != nil {
		t.Fatalf("the start's answer: %v", err)
	}
	if err := h.SendText(own, created.Pane, "no"); err != nil {
		t.Fatalf("the start's typed answer: %v", err)
	}
	if got := orcaWrites(o, handle); !slices.Equal(got, []string{"n", "no"}) {
		t.Fatalf("typed %q", got)
	}
	// The start's answer names its own tab: a terminal the user opened in Orca gets nothing from it.
	if err := h.SendKeys(withStartTyping(ctx, "orca:tab1:leaf1"), host.PaneRef{Kind: host.Orca, ID: "tab1:leaf1"}, "n"); err == nil {
		t.Fatal("typed into a terminal this host did not open")
	}
	// The same tab under another process: nothing.
	o.Update(handle, map[string]any{"incarnationId": "inc-other"})
	if _, _, err := h.terminals(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.SendKeys(own, created.Pane, "n"); err == nil || !errors.Is(err, host.ErrNotDelivered) {
		t.Fatalf("typed into a new process: %v", err)
	}
	if got := orcaWrites(o, "term_1"); len(got) != 0 {
		t.Fatalf("typed %q into the user's terminal", got)
	}
	if got := orcaWrites(o, handle); len(got) != 2 {
		t.Fatalf("typed %q", got)
	}
}

// Run Agent Here on Orca (panes.agent): an idle shell takes the launcher's start, typed into that terminal with a cd
// to its folder through the guarded single-use grant, and no new terminal opens. A terminal where something else
// runs, one Orca cannot read and an Orca without terminal.inspectProcess are refused with nothing typed (#17040).
func TestOrcaRunAgentHere(t *testing.T) {
	for _, tc := range []struct {
		name    string
		process map[string]any // nil: an Orca without terminal.inspectProcess
		starts  bool
	}{
		{"idle shell", orcarttest.ShellAtRest(orcaShellPID), true},
		{"vi in front", orcarttest.Running("vi", orcaShellPID), false},
		{"unverifiable", orcarttest.Unverifiable("incarnation_mismatch"), false},
		{"older Orca", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withOrcaShell(t, "-zsh")
			n, _, o, _, home := orcaNode(t)
			withStartWaits(n, 300*time.Millisecond, 300*time.Millisecond) // the fake terminal never runs claude
			dir := mkdir(t, filepath.Join(home, "proj"))
			o.SetTerminals(orcarttest.Terminal("term_1", "tab1", "leaf1", "repo::"+dir, dir, "refs/heads/main", "zsh", ""))
			o.SetWorktrees(orcarttest.Worktree("repo::"+dir, dir, "refs/heads/main"))
			if err := n.refreshHosts(t.Context()); err != nil {
				t.Fatal(err)
			}
			if tc.process != nil {
				o.SetProcess("term_1", tc.process)
			}
			starts := orcaTypedStarts(o)
			o.Reset()
			_, err := request(t, n, "panes.agent", map[string]any{"pane": "orca:tab1:leaf1", "launcher": "claude"})
			sends := o.Params("terminal.send")
			if !tc.starts {
				if err == nil || len(sends) != 0 {
					t.Fatalf("panes.agent: %v, sends %v", err, sends)
				}
				return
			}
			// claude never comes up in the fake: the start ends with an error, the pane stays as it is.
			if len(sends) != 1 || sends[0]["terminal"] != "term_1" || sends[0]["expectedIncarnationId"] != "inc-term_1" {
				t.Fatalf("sends %v (err %v)", sends, err)
			}
			if text, _ := sends[0]["text"].(string); !strings.HasPrefix(text, "cd -- "+shellQuote(dir)+" && ") {
				t.Errorf("typed %q", text)
			}
			if got := starts(); len(got) != 1 || !strings.Contains(got[0], "claude --session-id ") {
				t.Errorf("started %q", got)
			}
			if c := o.Trace("terminal.create", "terminal.close"); len(c) != 0 {
				t.Errorf("Orca was asked %v", c)
			}
		})
	}
}

// A launcher switch (#5035) on an Orca Agent: with the user's next message the claude is quit with Ctrl-C, Orca reads
// its terminal with only the shell in front, and the session starts again on the picked launcher in the same
// terminal (no new one) through the guarded single-use grant, then gets the message. Before #17040 Orca refused it
// (switch_unsupported).
func TestOrcaLauncherSwitchRestartsInTheSameTerminal(t *testing.T) {
	withOrcaShell(t, "-zsh")
	n, _, o, procs, home := orcaNode(t)
	withLoginShellConfigDir(t, "", true)
	withShellLaunchers(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	writeTranscript(t, home, cwd, sidA, time.Now().Add(-time.Hour), finishedTurn(cwd)...)
	n.state.put(sidA, sessionState{Launcher: "claude", Cwd: cwd, StartedAt: time.Now().Add(-2 * time.Hour).UTC()})
	knownProbes(t, "work")
	n.setManaged([]proto.ManagedLauncher{{Name: "work", BaseURL: "https://gateway.example.com", Model: "glm-5"}})
	const oldPID, newPID = 4242, 4343
	writeClaudePID(t, home, oldPID, sidA)
	o.SetTerminals(orcarttest.Terminal("term_1", "tab1", "leaf1", "repo::"+cwd, cwd, "refs/heads/main", "claude", "claude"))
	o.SetWorktrees(orcarttest.Worktree("repo::"+cwd, cwd, "refs/heads/main", orcarttest.Agent("tab1:leaf1", "claude", "done")))
	procs.set("tab1:leaf1", orcaProc{pid: oldPID, agent: "claude"})
	o.SetScreen("term_1", orcaClaudeScreen(orcaManualFooter))
	if m := byID(listSessions(t, n)); m[sidA].PaneID != "orca:tab1:leaf1" {
		t.Fatalf("not listed: %v", m)
	}
	if err := pick(t, n, map[string]any{"launcher": "work", "model": "opus"}); err != nil {
		t.Fatal(err)
	}
	o.Reset()
	var mu sync.Mutex
	ctrlC := 0
	var startLine string
	o.Handle("terminal.send", func(p map[string]any) any {
		text, _ := p["text"].(string)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case text == "\x03":
			if ctrlC++; ctrlC == 2 { // claude quits to its shell
				procs.set("tab1:leaf1")
				o.SetProcess("term_1", orcarttest.ShellAtRest(orcaShellPID))
				o.SetScreen("term_1", "% ")
			}
		case strings.HasPrefix(text, "cd -- "): // the start line: claude comes up on the new launcher
			startLine = text
			_, line, _ := strings.Cut(strings.TrimSuffix(text, "\r"), " && ")
			if cmd, ok := startFileCommand(line); ok {
				startLine = cmd
			}
			writeClaudePID(t, home, newPID, sidA)
			procs.set("tab1:leaf1", orcaProc{pid: newPID, agent: "claude"})
			o.SetProcess("term_1", orcarttest.Running("claude", orcaShellPID))
			o.SetScreen("term_1", orcaClaudeScreen(orcaManualFooter))
		}
		return map[string]any{"send": map[string]any{"handle": p["terminal"], "accepted": true, "bytesWritten": len(text)}}
	})
	if err := tell(t, n, "hello", true); err != nil {
		t.Fatalf("send: %v\n%s", err, strings.Join(o.Trace(), "\n"))
	}
	if c := o.Trace("terminal.create", "terminal.close"); len(c) != 0 {
		t.Errorf("a terminal was opened or closed: %v", c)
	}
	mu.Lock()
	started := startLine
	mu.Unlock()
	if !strings.Contains(started, " launch work ") || !strings.Contains(started, "--resume "+sidA) {
		t.Fatalf("started %q", started)
	}
	writes := orcaWrites(o, "term_1")
	quit, start, prompt := -1, -1, -1
	for i, w := range writes {
		switch {
		case w == "\x03" && quit < 0:
			quit = i
		case strings.HasPrefix(w, "cd -- ") && start < 0:
			start = i
		case strings.Contains(w, "hello") && prompt < 0:
			prompt = i
		}
	}
	if quit < 0 || start < quit || prompt < start {
		t.Fatalf("not quit, started, then prompted: %q", writes)
	}
	for _, p := range o.Params("terminal.send") {
		if p["terminal"] != "term_1" || p["expectedIncarnationId"] != "inc-term_1" {
			t.Errorf("a write outside the agent's terminal process: %v", p)
		}
	}
	if st, _ := n.state.get(sidA); st.Launcher != "work" || st.PendingSwitch != nil {
		t.Errorf("record after the switch = %+v", st)
	}
}

// An async start on Orca stopped on oh-my-zsh's question (shell_waiting) keeps its tab; the owner's "n" from the app
// (start.continue) goes into that tab, letters and all, though no agent runs there, and the start runs again in the
// same tab under the same id: no new terminal, its start line typed once the shell is at rest (#17040, #10365).
func TestOrcaStartContinueAnswersInTheKeptTab(t *testing.T) {
	n, _, o, _, home := orcaNode(t)
	watchShell(n)
	n.shellAsk, n.shellQuiet = 300*time.Millisecond, 200*time.Millisecond
	withStartWaits(n, 300*time.Millisecond, 300*time.Millisecond) // the fake terminal never runs claude
	o.SetWorktrees(orcarttest.Worktree("repo::"+home, home, "refs/heads/main"))
	question := "[oh-my-zsh] Would you like to update? [Y/n]"
	if _, err := request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": home, "launcher": "claude",
		"async": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the new terminal", func() bool { return o.Count("terminal.create") == 1 })
	handle := "term_fake_1"
	o.SetScreen(handle, "Last login: today\n"+question+" ")
	if err := n.starts.wait(t.Context(), startID); rpcCode(err) != proto.CodeShellWaiting {
		t.Fatalf("start: %v", err)
	}
	_, s := n.starts.failed(startID)
	if s.StartErrorCode != proto.CodeShellWaiting || s.StartErrorArgs["line"] != question || s.StartErrorArgs["choices"] != "y,n,enter" {
		t.Fatalf("listed %+v", s)
	}
	if got := orcaWrites(o, handle); len(got) != 0 {
		t.Fatalf("typed %q before the answer", got)
	}
	o.Reset()
	answered := make(chan struct{})
	o.Handle("terminal.send", func(p map[string]any) any {
		text, _ := p["text"].(string)
		if text == "n" {
			o.SetScreen(handle, "Last login: today\n"+question+" \n% ")
			close(answered)
		}
		return map[string]any{"send": map[string]any{"handle": p["terminal"], "accepted": true, "bytesWritten": len(text)}}
	})
	res, err := request(t, n, "start.continue", map[string]any{"session_id": startID, "choice": "n"})
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeSessions(t, []any{res["session"]})[0]; got.SessionID != startID || !got.Starting {
		t.Errorf("answered %+v", got)
	}
	select {
	case <-answered:
	case <-time.After(5 * time.Second):
		t.Fatalf("no answer typed: %v", o.Trace())
	}
	waitFor(t, "the start line in the kept tab", func() bool { return len(o.Params("terminal.send")) > 1 })
	_ = n.starts.wait(t.Context(), startID) // claude never comes up in the fake: the start ends, the answer stays typed
	sends := o.Params("terminal.send")
	if len(sends) < 2 || sends[0]["text"] != "n" {
		t.Fatalf("sends %v", sends)
	}
	for _, p := range sends {
		if p["terminal"] != handle || p["expectedIncarnationId"] != "inc_1" {
			t.Errorf("a write outside the kept tab's process: %v", p)
		}
	}
	if text, _ := sends[1]["text"].(string); !strings.HasPrefix(text, "cd -- ") {
		t.Errorf("the start line: %q", text)
	}
	if c := o.Count("terminal.create"); c != 0 {
		t.Errorf("opened %d new terminals", c)
	}
	if inv := o.Invalid(); len(inv) != 0 {
		t.Errorf("params Orca would refuse: %v", inv)
	}
}
