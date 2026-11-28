package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestOrcaCreateTerminalInRegisteredWorktree(t *testing.T) {
	n, _, o, _, home := orcaNode(t)
	repoDir := filepath.Join(home, "my-project")
	mkdir(t, repoDir)

	o.SetWorktrees(orcarttest.Worktree("wt-proj", repoDir, "refs/heads/main"))

	res, err := request(t, n, "session.start", map[string]any{
		"session_id": "0e000000-0000-4000-8000-000000000001",
		"cwd":        repoDir,
		"async":      true,
	})
	if err != nil {
		t.Fatalf("session.start failed: %v", err)
	}
	session, ok := res["session"].(map[string]any)
	if !ok {
		t.Fatalf("expected session map in result, got %v", res)
	}
	if sid, _ := session["session_id"].(string); sid != "0e000000-0000-4000-8000-000000000001" {
		t.Errorf("expected session ID 0e000000-0000-4000-8000-000000000001, got %v", session["session_id"])
	}

	// Verify terminal.create was called in fake Orca
	deadline := time.Now().Add(5 * time.Second)
	for len(o.Trace("terminal.create")) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for terminal.create; calls: %v", o.Trace())
		}
		time.Sleep(20 * time.Millisecond)
	}

	createCalls := o.Trace("terminal.create")
	if len(createCalls) == 0 {
		t.Fatalf("expected terminal.create call, got none")
	}
	if !strings.Contains(createCalls[0], `"worktree":"id:wt-proj"`) {
		t.Errorf("expected terminal.create for wt-proj, got %s", createCalls[0])
	}
}

func TestOrcaCreateTerminalInSubdirectory(t *testing.T) {
	n, _, o, _, home := orcaNode(t)
	repoDir := filepath.Join(home, "monorepo")
	subDir := filepath.Join(repoDir, "services", "api")
	mkdir(t, subDir)

	o.SetWorktrees(orcarttest.Worktree("wt-mono", repoDir, "refs/heads/main"))

	res, err := request(t, n, "session.start", map[string]any{
		"session_id": "0e000000-0000-4000-8000-000000000002",
		"cwd":        subDir,
		"async":      true,
	})
	if err != nil {
		t.Fatalf("session.start in subDir failed: %v", err)
	}
	session, ok := res["session"].(map[string]any)
	if !ok {
		t.Fatalf("expected session map in result, got %v", res)
	}
	if cwd, _ := session["cwd"].(string); cwd != subDir {
		t.Errorf("expected Cwd %s, got %v", subDir, session["cwd"])
	}

	deadline := time.Now().Add(5 * time.Second)
	for len(o.Trace("terminal.create")) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for terminal.create; calls: %v", o.Trace())
		}
		time.Sleep(20 * time.Millisecond)
	}

	createCalls := o.Trace("terminal.create")
	if !strings.Contains(createCalls[0], `"worktree":"id:wt-mono"`) {
		t.Errorf("expected terminal.create for wt-mono, got %s", createCalls[0])
	}

	// Verify the launched shell command executed cd -- subDir
	for len(o.Writes()) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for terminal write; writes: %v", o.Writes())
		}
		time.Sleep(20 * time.Millisecond)
	}
	w := o.Writes()[0]
	if !strings.Contains(w.Text, "cd -- "+shellQuote(subDir)) {
		t.Errorf("expected cd to %s, got %s", subDir, w.Text)
	}
}

func TestOrcaFallbackToAvailableWorktree(t *testing.T) {
	n, _, o, _, home := orcaNode(t)
	otherDir := filepath.Join(home, "unregistered")
	mkdir(t, otherDir)

	// Orca has a worktree open for another project
	wtDir := filepath.Join(home, "open-project")
	mkdir(t, wtDir)
	o.SetWorktrees(orcarttest.Worktree("wt-open", wtDir, "refs/heads/main"))

	launch, err := hostCap[host.Launcher](n)
	if err != nil {
		t.Fatalf("expected host.Launcher, got error: %v", err)
	}

	created, err := launch.CreateSpace(context.Background(), otherDir, "test")
	if err != nil {
		t.Fatalf("expected fallback to wt-open, got err: %v", err)
	}
	if created.SpaceID != "wt-open" {
		t.Errorf("expected container SpaceID wt-open, got %s", created.SpaceID)
	}
}

func TestOrcaRefusesWhenNoWorktrees(t *testing.T) {
	n, _, o, _, home := orcaNode(t)
	unregDir := filepath.Join(home, "unregistered")
	mkdir(t, unregDir)

	// No worktrees in Orca at all
	o.SetWorktrees()

	launch, err := hostCap[host.Launcher](n)
	if err != nil {
		t.Fatalf("expected host.Launcher, got error: %v", err)
	}

	_, err = launch.CreateSpace(context.Background(), unregDir, "test")
	if err == nil {
		t.Fatalf("expected refusal when no worktrees in Orca, got nil")
	}
	if !strings.Contains(err.Error(), "orca_not_registered") && !strings.Contains(err.Error(), "没有任何打开的项目") {
		t.Errorf("expected orca_not_registered refusal, got %v", err)
	}
}

// A Claude session that is not running resumes in Orca as on herdr (#5548): a terminal opened in the Orca worktree
// holding its directory, cd there, its launcher with --resume. One still open elsewhere (a live claude writes it) is
// refused, nothing started: two writers would corrupt the transcript.
func TestOrcaResumesAnOfflineSession(t *testing.T) {
	n, _, o, _, home := orcaNode(t)
	n.claudeStart, n.claudeReady = 200*time.Millisecond, 200*time.Millisecond // the fake terminal never runs claude
	projDir := filepath.Join(home, "my-project")
	mkdir(t, projDir)
	o.SetWorktrees(orcarttest.Worktree("repo1::"+projDir, projDir, "refs/heads/main"))
	const id = "0e000000-0000-4000-8000-000000000003"
	writeTranscript(t, home, projDir, id, time.Now().Add(-time.Hour), `{"type":"custom-title","customTitle":"x"}`)
	starts := orcaTypedStarts(o)

	// The start then waits for claude, which the fake never runs: a timeout, not a refusal.
	_, err := request(t, n, "session.start", map[string]any{"session_id": id, "resume_session_id": id, "cwd": projDir})
	if isRefusal(err, "orca_resume") || isRefusal(err, openElsewhere) {
		t.Fatalf("resume refused: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(strings.Join(starts(), "\n"), "--resume "+id) {
		if time.Now().After(deadline) {
			t.Fatalf("no resume typed; calls: %v", o.Trace())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if c := o.Trace("terminal.create"); len(c) != 1 || !strings.Contains(c[0], `"worktree":"id:repo1::`+projDir+`"`) {
		t.Errorf("terminal.create %v", c)
	}
	if s := strings.Join(o.Trace("terminal.send"), "\n"); !strings.Contains(s, "cd -- ") || !strings.Contains(s, projDir) {
		t.Errorf("not started in the session's directory: %s", s)
	}

	const open = "0e000000-0000-4000-8000-000000000004"
	writeClaudeSession(t, home, os.Getpid(), open, "cli") // this test's own pid: a live claude has it open
	o.Reset()
	_, err = request(t, n, "session.start", map[string]any{"session_id": open, "resume_session_id": open, "cwd": projDir})
	if !isRefusal(err, openElsewhere) {
		t.Fatalf("resume of a session open elsewhere: %v", err)
	}
	if c := o.Trace("terminal.create", "terminal.send"); len(c) != 0 {
		t.Errorf("Orca was asked %v", c)
	}

	// One an Orca native chat owns (its runtime's) is refused. (Other agents' sessions resume in Orca too since
	// #17044: orca_resume_test.go.)
	if n.runtimes == nil {
		n.runtimes = newRuntimeRegistry()
	}
	n.runtimes.mu.Lock()
	n.runtimes.known[id] = knownSession{runtime: "orca", label: "Orca", at: time.Now()}
	n.runtimes.mu.Unlock()
	_, err = request(t, n, "session.start", map[string]any{"session_id": id, "resume_session_id": id, "cwd": projDir})
	if !isRefusal(err, "runtime_session") {
		t.Errorf("resume of a native chat's session: %v", err)
	}
	if c := o.Trace("terminal.create", "terminal.send"); len(c) != 0 {
		t.Errorf("Orca was asked %v", c)
	}
}

// A directory whose name holds a control character is refused before Orca opens a terminal: Run types `cd -- <cwd>`
// into the shell's line editor, which acts on a tab (completion) or a line break even inside the quotes (#2867).
func TestOrcaRefusesADirectoryItCannotType(t *testing.T) {
	fake := orcarttest.New(t)
	root := t.TempDir()
	fake.SetWorktrees(orcarttest.Worktree("wt-1", root, "refs/heads/main"))
	h := newOrcaHost(&orcart.Client{MetaPath: fake.MetaPath})
	for _, name := range []string{"a\tb", "a\nb", "a\x1bb", "a\x7fb"} {
		dir := mkdir(t, filepath.Join(root, name))
		if created, err := h.CreateSpace(context.Background(), dir, "tab1"); created != nil || !errors.Is(err, host.ErrNotDelivered) || refusalCode(err) != proto.CodeDirectoryUntypable {
			t.Errorf("CreateSpace(%q) = %v, %v", name, created, err)
		}
		if created, err := h.CreateSlot(context.Background(), "wt-1", dir, "tab1"); created != nil || !errors.Is(err, host.ErrNotDelivered) || refusalCode(err) != proto.CodeDirectoryUntypable {
			t.Errorf("CreateSlot(%q) = %v, %v", name, created, err)
		}
	}
	if c := fake.Trace("terminal.create", "terminal.send"); len(c) != 0 {
		t.Errorf("Orca was asked %v", c)
	}
	// A name that only looks unusual still opens a terminal and is typed quoted.
	dir := mkdir(t, filepath.Join(root, `it's a \dir`))
	created, err := h.CreateSpace(context.Background(), dir, "tab1")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if err := h.Run(context.Background(), created.Pane, "echo hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if w := fake.Writes(); len(w) != 1 || !strings.HasPrefix(w[0].Text, "cd -- "+shellQuote(dir)+" && echo hello") {
		t.Errorf("typed %v", w)
	}
}

func TestOrcaRunSingleUseGrant(t *testing.T) {
	fake := orcarttest.New(t)
	fake.SetWorktrees(orcarttest.Worktree("wt-1", "/tmp/repo", "refs/heads/main"))
	c := &orcart.Client{MetaPath: fake.MetaPath}
	h := newOrcaHost(c)

	created, err := h.CreateSpace(context.Background(), "/tmp/repo", "tab1")
	if err != nil {
		t.Fatalf("CreateSpace failed: %v", err)
	}

	// First Run should succeed
	if err := h.Run(context.Background(), created.Pane, "echo hello"); err != nil {
		t.Fatalf("first Run failed: %v", err)
	}

	// Second Run should be rejected (token already consumed)
	if err := h.Run(context.Background(), created.Pane, "echo twice"); err == nil {
		t.Fatalf("second Run expected error, got nil")
	}
}

// A new Orca tab has no screen for its first moments (terminal.read answers screen-unavailable): the start waits for
// the shell's prompt instead of typing into a tty whose shell is not there yet, and then types the short line that
// sources its start file, so a shell slow to take the terminal cannot cut the command at macOS's 1024-byte canonical
// line limit and leave it unrun at the prompt (#5450).
func TestOrcaStartWaitsForTheScreenAndTypesAShortLine(t *testing.T) {
	withPaneShell(t, "/bin/bash")
	n, _, o, _, home := orcaNode(t)
	watchShell(n)
	n.shellSettle, n.shellQuiet = 10*time.Second, 100*time.Millisecond
	repoDir := mkdir(t, filepath.Join(home, "proj"))
	o.SetWorktrees(orcarttest.Worktree("wt-proj", repoDir, "refs/heads/main"))
	var drawn atomic.Bool
	var reads atomic.Int32
	read := make(chan struct{}, 1)
	o.Handle("terminal.read", func(p map[string]any) any {
		reads.Add(1)
		select {
		case read <- struct{}{}:
		default:
		}
		if !drawn.Load() {
			return map[string]any{"terminal": map[string]any{"handle": p["terminal"], "tail": []string{}, "source": "screen-unavailable"}}
		}
		return map[string]any{"terminal": map[string]any{"handle": p["terminal"], "tail": []string{"bash-5.2$ "}, "source": "screen"}}
	})
	if _, err := request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": repoDir, "async": true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the new tab", func() bool { return len(o.Trace("terminal.create")) > 0 })
	for reads.Load() < 3 { // no screen across more than one settle poll
		if w := o.Writes(); len(w) != 0 {
			t.Fatalf("typed before the tab had a screen: %q", w[0].Text)
		}
		select {
		case <-read:
		case <-time.After(5 * time.Second):
			t.Fatalf("the tab was not read again: %v", o.Trace())
		}
	}
	drawn.Store(true)
	waitFor(t, "the start line", func() bool { return len(o.Writes()) > 0 })
	typed := o.Writes()[0].Text
	want := "cd -- " + shellQuote(repoDir) + " && . "
	if !strings.HasPrefix(typed, want) || !strings.HasSuffix(typed, " && true "+shellQuote(startID)+"\r") || len(typed) >= 1024 {
		t.Errorf("typed %d bytes, not the start file's line: %q", len(typed), typed)
	}
}

// On Windows Orca's terminals run Windows PowerShell by default, which reads neither Run's `cd -- <dir> &&` nor a POSIX
// start: the node opens the start's terminal in Git Bash when Orca can be told the shell, and types the start file's
// line there as on macOS. An older Orca, which would ignore the shell, and other platforms get Orca's default (#9810).
func TestOrcaStartOpensGitBashOnWindows(t *testing.T) {
	selection := []string{"terminal.prompt-delivery.v1", orcart.CapShellSelection}
	for _, tc := range []struct {
		name      string
		windows   bool
		paneShell string // herdrPaneShell(): "" on Windows, the login shell elsewhere
		caps      []string
		shell     any // terminal.create's shell; nil when not sent
		sourced   bool
	}{
		{"windows", true, "", selection, "git-bash", true},
		{"windows, older Orca", true, "", []string{"terminal.prompt-delivery.v1"}, nil, false},
		{"macOS", false, "/bin/zsh", selection, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withPaneShell(t, tc.paneShell)
			old := orcaOnWindows
			orcaOnWindows = func() bool { return tc.windows }
			t.Cleanup(func() { orcaOnWindows = old })
			n, _, o, _, home := orcaNode(t)
			o.SetStatus(map[string]any{"capabilities": tc.caps})
			repoDir := mkdir(t, filepath.Join(home, "proj"))
			o.SetWorktrees(orcarttest.Worktree("wt-proj", repoDir, "refs/heads/main"))
			o.Handle("terminal.read", func(p map[string]any) any {
				return map[string]any{"terminal": map[string]any{"handle": p["terminal"], "tail": []string{"bash-5.2$ "}, "source": "screen"}}
			})
			if _, err := request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": repoDir, "async": true}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the start line", func() bool { return len(o.Writes()) > 0 })
			if got := o.Params("terminal.create")[0]["shell"]; got != tc.shell {
				t.Errorf("terminal.create shell = %v, want %v", got, tc.shell)
			}
			typed := o.Writes()[0].Text
			if sourced := strings.HasPrefix(typed, "cd -- "+shellQuote(repoDir)+" && . "); sourced != tc.sourced {
				t.Errorf("typed %q: sources the start file = %v, want %v", typed, sourced, tc.sourced)
			}
		})
	}
}

// orcaTypedStarts has o's terminal.send read the start file an Orca start's typed line (`cd -- <dir> && . <file>…`)
// sources, while it is still there (typedstart.go), and returns the commands read so far.
func orcaTypedStarts(o *orcarttest.Fake) func() []string {
	var mu sync.Mutex
	var cmds []string
	o.Handle("terminal.send", func(p map[string]any) any {
		text, _ := p["text"].(string)
		_, line, _ := strings.Cut(strings.TrimSuffix(text, "\r"), " && ")
		if cmd, ok := startFileCommand(line); ok {
			mu.Lock()
			cmds = append(cmds, cmd)
			mu.Unlock()
		}
		return map[string]any{"send": map[string]any{"handle": p["terminal"], "accepted": true, "bytesWritten": len(text)}}
	})
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(cmds)
	}
}

// A start whose directory is in no Orca worktree opens its terminal in the worktree Orca is showing (isActive), not
// the first one listed: Orca's UI does not adopt a terminal made in a worktree it is not showing (#5547).
func TestOrcaFallbackPrefersTheActiveWorktree(t *testing.T) {
	fake := orcarttest.New(t)
	root := t.TempDir()
	first, active := mkdir(t, filepath.Join(root, "first")), mkdir(t, filepath.Join(root, "active"))
	outside := mkdir(t, filepath.Join(root, "outside"))
	wtActive := orcarttest.Worktree("wt-active", active, "refs/heads/main")
	wtActive["isActive"] = true
	fake.SetWorktrees(orcarttest.Worktree("wt-first", first, "refs/heads/main"), wtActive)
	h := newOrcaHost(&orcart.Client{MetaPath: fake.MetaPath})
	created, err := h.CreateSpace(context.Background(), outside, "tab1")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	if created.SpaceID != "wt-active" {
		t.Errorf("terminal opened in %s, want the active worktree", created.SpaceID)
	}
	// Without an active one, the first local worktree still holds it.
	fake.SetWorktrees(orcarttest.Worktree("wt-first", first, "refs/heads/main"), orcarttest.Worktree("wt-2", active, "refs/heads/main"))
	if created, err = h.CreateSpace(context.Background(), outside, "tab2"); err != nil || created.SpaceID != "wt-first" {
		t.Errorf("CreateSpace = %+v, %v; want wt-first", created, err)
	}
}

// Orca re-keys a terminal its UI did not adopt: orphaned, its tabId and leafId rewritten, the same handle and
// incarnation (#5547). The start still types into it, sees claude come up in it and closes it when the start fails:
// the node keeps the pane key creation gave it, and the host finds the terminal by its handle.
func TestOrcaFindsANewTerminalOrcaReKeyed(t *testing.T) {
	n, _, o, procs, home := orcaNode(t)
	n.claudeStart, n.claudeReady = 5*time.Second, 5*time.Second
	outside := mkdir(t, filepath.Join(home, "outside"))
	o.SetWorktrees(orcarttest.Worktree("wt-1", mkdir(t, filepath.Join(home, "proj")), "refs/heads/main"))
	launch, err := hostCap[host.Launcher](n)
	if err != nil {
		t.Fatal(err)
	}
	h := launch.(*orcaHost)
	created, err := launch.CreateSpace(context.Background(), outside, "tab1")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	const handle = "term_fake_1"
	newKey := o.Orphan(handle)
	if newKey == "" || newKey == created.Pane.ID {
		t.Fatalf("not re-keyed: %q", newKey)
	}
	if err := launch.Run(context.Background(), created.Pane, "claude"); err != nil {
		t.Fatalf("Run in the re-keyed terminal: %v", err)
	}
	if w := o.Writes(); len(w) != 1 || w[0].Handle != handle || !strings.HasSuffix(w[0].Text, " && claude\r") {
		t.Fatalf("typed %+v", w)
	}

	// claude runs in it (its ORCA_PANE_KEY is the key the shell was started with) and shows its input box.
	procs.set(created.Pane.ID, orcaProc{pid: 4242, agent: "claude", cwd: outside})
	o.SetScreen(handle, "? for shortcuts\n")
	a, err := n.waitReady(context.Background(), created.Pane.String(), nil)
	if err != nil || a.Agent != "claude" {
		t.Fatalf("waitReady = %+v, %v", a, err)
	}
	// Under the key Orca gave it now, claude is found as well.
	procs.set(created.Pane.ID)
	procs.set(newKey, orcaProc{pid: 4242, agent: "claude", cwd: outside})
	h.mu.Lock()
	h.procsAt = time.Time{}
	h.mu.Unlock()
	if a, err := h.Agent(context.Background(), created.Pane); err != nil || a.Agent != "claude" {
		t.Fatalf("Agent under the new key = %+v, %v", a, err)
	}

	// Its tab is still renamed under the slot the start was given.
	if err := h.RenameSlot(context.Background(), created.SlotID, "renamed"); err != nil {
		t.Errorf("RenameSlot of the re-keyed terminal: %v", err)
	}

	n.closeNewTab(context.Background(), launch, created, "")
	if w := o.Writes(); len(w) != 2 || !w[1].Close || w[1].Handle != handle {
		t.Errorf("the re-keyed terminal was not closed: %+v", w)
	}
}

// A start whose new pane is gone says so, rather than blaming a busy machine (#5547).
func TestOrcaStartSaysWhenItsPaneIsGone(t *testing.T) {
	n, _, o, _, home := orcaNode(t)
	n.claudeStart, n.claudeReady = time.Minute, time.Second
	o.SetWorktrees(orcarttest.Worktree("wt-1", mkdir(t, filepath.Join(home, "proj")), "refs/heads/main"))
	launch, err := hostCap[host.Launcher](n)
	if err != nil {
		t.Fatal(err)
	}
	created, err := launch.CreateSpace(context.Background(), filepath.Join(home, "proj"), "tab1")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	o.SetTerminals() // closed, or Orca restarted
	began := time.Now()
	_, err = n.waitReady(context.Background(), created.Pane.String(), nil)
	if err == nil || strings.Contains(err.Error(), "busy") || !strings.Contains(err.Error(), "could not be found") {
		t.Errorf("waitReady = %v", err)
	}
	if took := time.Since(began); took > 10*time.Second {
		t.Errorf("waited %s for a pane that is gone, not a few looks", took)
	}
}

// A started terminal the user moves to another tab is still in Orca's UI: it is listed where it is now, and that tab
// is renamed, not the one it was made in (#5547 keeps only an orphaned one under its first key).
func TestOrcaListsAMovedTerminalWhereItIs(t *testing.T) {
	fake := orcarttest.New(t)
	root := t.TempDir()
	fake.SetWorktrees(orcarttest.Worktree("wt-1", root, "refs/heads/main"))
	h := newOrcaHost(&orcart.Client{MetaPath: fake.MetaPath})
	h.scan = (&fakeProcs{}).scan
	created, err := h.CreateSpace(context.Background(), root, "tab1")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	fake.Move("term_fake_1", "tab_moved", "leaf_moved")
	l, err := h.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Panes) != 1 || l.Panes[0].PaneID != (host.PaneRef{Kind: host.Orca, ID: "tab_moved:leaf_moved"}).String() || l.Panes[0].TabID != "tab_moved" {
		t.Errorf("panes %+v, want the moved one under tab_moved:leaf_moved (made as %s)", l.Panes, created.Pane.ID)
	}
	if err := h.RenameSlot(context.Background(), "tab_moved", "renamed"); err != nil {
		t.Errorf("RenameSlot(tab_moved): %v", err)
	}
	if tr := fake.Trace("terminal.rename"); len(tr) != 1 || !strings.Contains(tr[0], `"terminal":"term_fake_1"`) {
		t.Errorf("renames %v", tr)
	}
}

// Run types only into the terminal this host made: one Orca orphaned without re-keying it is refused, and so is a
// new handle under the same pane key (#972: Persona trusts the handle terminal.create returned).
func TestOrcaRunRefusesAnotherTerminalUnderItsKey(t *testing.T) {
	fake := orcarttest.New(t)
	root := t.TempDir()
	fake.SetWorktrees(orcarttest.Worktree("wt-1", root, "refs/heads/main"))
	h := newOrcaHost(&orcart.Client{MetaPath: fake.MetaPath})
	created, err := h.CreateSpace(context.Background(), root, "tab1")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	fake.Update("term_fake_1", map[string]any{"orphaned": true})
	if err := h.Run(context.Background(), created.Pane, "echo hi"); err == nil || !errors.Is(err, host.ErrNotDelivered) {
		t.Errorf("Run in an orphaned terminal Orca did not re-key: %v", err)
	}

	created, err = h.CreateSpace(context.Background(), root, "tab2")
	if err != nil {
		t.Fatalf("CreateSpace: %v", err)
	}
	tab, leaf, _ := strings.Cut(created.Pane.ID, ":")
	fake.SetTerminals(orcarttest.Terminal("term_other", tab, leaf, "wt-1", root, "refs/heads/main", "tab2", ""))
	if err := h.Run(context.Background(), created.Pane, "echo hi"); err == nil || !errors.Is(err, host.ErrNotDelivered) {
		t.Errorf("Run under the same pane key with another handle: %v", err)
	}
	if w := fake.Writes(); len(w) != 0 {
		t.Errorf("typed %+v", w)
	}
}

// keptOrcaStart is n with an Orca tab kept by a failed start in the worktree wt-proj at root; used: the start typed its
// line there, so the tab's one launch is gone (agent_start_stalled). Its terminal is term_fake_1.
func keptOrcaStart(t *testing.T, used bool) (*Node, *orcaHost, *orcarttest.Fake, *host.Created, string) {
	t.Helper()
	withPaneShell(t, "/bin/bash")
	n, _, o, _, home := orcaNode(t)
	root := mkdir(t, filepath.Join(home, "proj"))
	o.SetWorktrees(orcarttest.Worktree("wt-proj", root, "refs/heads/main"))
	h := n.hosts.Get(host.Orca).(*orcaHost)
	c, err := h.CreateSpace(context.Background(), root, "kept")
	if err != nil {
		t.Fatal(err)
	}
	if used {
		if err := h.Run(context.Background(), c.Pane, "claude"); err != nil {
			t.Fatal(err)
		}
	}
	n.keepStartTab(h, c, "", "")
	withStartWaits(n, 100*time.Millisecond, 100*time.Millisecond)
	return n, h, o, c, root
}

const keptOrcaHandle = "term_fake_1"

// isKept: the tab on pane is kept and no start holds it; until is its expiry.
func isKept(n *Node, pane string) (until time.Time, kept bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	k, ok := n.keptStarts[pane]
	return k.until, ok && !k.taken
}

// startLineIn is the terminal a start's line for startID went to, "" when none did.
func startLineIn(o *orcarttest.Fake) string {
	for _, w := range o.Writes() {
		if !w.Close && strings.Contains(w.Text, " && true "+shellQuote(startID)) {
			return w.Handle
		}
	}
	return ""
}

// closedBeforeCreate: the kept terminal was closed once, before the new one was made.
func closedBeforeCreate(t *testing.T, o *orcarttest.Fake) {
	t.Helper()
	closeAt, createAt, closes := -1, -1, 0
	for i, call := range o.Trace() {
		if strings.HasPrefix(call, "terminal.close ") && strings.Contains(call, `"`+keptOrcaHandle+`"`) {
			closeAt, closes = i, closes+1
		}
		if strings.HasPrefix(call, "terminal.create ") {
			createAt = i
		}
	}
	if closes != 1 || o.Count("terminal.create") != 2 || closeAt >= createAt {
		t.Errorf("want the kept tab closed once, then one new tab: closes %d, creates %d, trace %v", closes, o.Count("terminal.create"), o.Trace())
	}
}

// waitAsyncStart waits for the background start of startID (async) and returns how it ended.
func waitAsyncStart(t *testing.T, n *Node) error {
	t.Helper()
	rec := n.starts.get(startID)
	if rec == nil {
		t.Fatal("no async start")
	}
	select {
	case <-rec.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the async start did not end")
	}
	n.starts.mu.Lock()
	defer n.starts.mu.Unlock()
	return rec.err
}

// An Orca terminal takes one launch (orcaHost.Run). A retry named to the kept tab of a start whose claude stalled
// (agent_start_stalled: its line was typed there) closes that tab and starts in a new one, instead of failing with
// "shell launch is only allowed once" (#9534); the sync and the async start alike.
func TestOrcaRetryAfterAStalledStartOpensANewTab(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async=%v", async), func(t *testing.T) {
			n, _, o, c, root := keptOrcaStart(t, true)
			_, err := request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": root, "in_pane": c.Pane.String(), "async": async})
			if async && err == nil {
				err = waitAsyncStart(t, n)
			}
			if err != nil && strings.Contains(err.Error(), "only allowed once") { // no claude comes up here: what matters is where the line went
				t.Fatalf("the retry failed in the stalled tab: %v", err)
			}
			closedBeforeCreate(t, o)
			if got := startLineIn(o); got == "" || got == keptOrcaHandle {
				t.Errorf("the start line went to %q, want a new tab", got)
			}
			if _, kept := isKept(n, c.Pane.String()); kept {
				t.Error("the stalled tab is still kept")
			}
		})
	}
}

// A kept Orca tab where nothing was typed (shell_waiting, the question answered since) still takes the retry: same
// terminal, no new one.
func TestOrcaRetryInAnAnsweredTab(t *testing.T) {
	n, h, o, c, root := keptOrcaStart(t, false)
	if err := h.SendKeys(context.Background(), c.Pane, "enter"); err != nil { // the question answered
		t.Fatal(err)
	}
	o.SetScreen(keptOrcaHandle, "Would you like to update? [Y/n] n\nbash-5.2$ ")
	_, _ = request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": root, "in_pane": c.Pane.String()})
	if got := o.Count("terminal.create"); got != 1 {
		t.Errorf("terminals created: %d, want the kept one only", got)
	}
	if got := startLineIn(o); got != keptOrcaHandle {
		t.Errorf("the start line went to %q, want the kept tab", got)
	}
}

// The same from a real shell_waiting start: its kept tab, answered, takes the retry.
func TestOrcaShellWaitingStartRetriesInItsTab(t *testing.T) {
	withPaneShell(t, "/bin/bash")
	n, _, o, _, home := orcaNode(t)
	watchShell(n)
	n.shellAsk = 100 * time.Millisecond
	root := mkdir(t, filepath.Join(home, "proj"))
	o.SetWorktrees(orcarttest.Worktree("wt-proj", root, "refs/heads/main"))
	screen := func(lines ...string) {
		o.Handle("terminal.read", func(p map[string]any) any {
			return map[string]any{"terminal": map[string]any{"handle": p["terminal"], "tail": lines, "source": "screen"}}
		})
	}
	screen("Would you like to update? [Y/n] ")
	if _, err := request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": root}); rpcCode(err) != proto.CodeShellWaiting {
		t.Fatalf("first start: %v", err)
	}
	n.mu.Lock()
	var pane string
	for p := range n.keptStarts {
		pane = p
	}
	n.mu.Unlock()
	if pane == "" {
		t.Fatal("the question kept no tab")
	}
	screen("Would you like to update? [Y/n] n", "bash-5.2$ ")
	n.shellQuiet = 100 * time.Millisecond
	withStartWaits(n, 100*time.Millisecond, 100*time.Millisecond)
	_, err := request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": root, "in_pane": pane})
	if o.Count("terminal.create") != 1 || err != nil && strings.Contains(err.Error(), "only allowed once") {
		t.Fatalf("the answered tab was not started in: creates %d, %v", o.Count("terminal.create"), err)
	}
}

// Orca re-keys a terminal its UI did not adopt (#5547): the kept tab is still found by the launch it has or has used.
func TestOrcaRekeyedKeptTab(t *testing.T) {
	for _, used := range []bool{false, true} {
		t.Run(fmt.Sprintf("used=%v", used), func(t *testing.T) {
			n, h, o, c, root := keptOrcaStart(t, used)
			if key := o.Orphan(keptOrcaHandle); key == "" || key == c.Pane.ID {
				t.Fatal("not re-keyed")
			}
			claim, err := n.claimKeptTab(c.Pane.String())
			if err != nil || claim == nil {
				t.Fatalf("claim: %v", err)
			}
			got, err := n.newTab(context.WithValue(context.Background(), keptClaimKey{}, claim), h, root, "wt-proj", "retry", startID)
			if err != nil {
				t.Fatal(err)
			}
			if err := h.Run(context.Background(), got.Pane, "claude"); err != nil {
				t.Fatal(err)
			}
			if reused := got == c; reused == used || o.Count("terminal.close") != map[bool]int{false: 0, true: 1}[used] {
				t.Errorf("reused %v, closes %d", reused, o.Count("terminal.close"))
			}
		})
	}
}

// A retry the node refuses before it gets a tab (a bad effort, a directory that is gone) leaves the stalled start's
// kept tab as it was, sync or async.
func TestOrcaRefusedRetryKeepsTheStalledTab(t *testing.T) {
	for name, params := range map[string]map[string]any{
		"bad effort":         {"effort": "not-an-effort"},
		"missing dir":        {"cwd": "removed-workspace"},
		"bad effort, async":  {"effort": "not-an-effort", "async": true},
		"missing dir, async": {"cwd": "removed-workspace", "async": true},
	} {
		t.Run(name, func(t *testing.T) {
			n, _, o, c, root := keptOrcaStart(t, true)
			req := map[string]any{"session_id": startID, "cwd": root, "in_pane": c.Pane.String()}
			for k, v := range params {
				req[k] = v
			}
			if dir, ok := params["cwd"].(string); ok {
				req["cwd"] = filepath.Join(root, dir)
			}
			if _, err := request(t, n, "session.start", req); err == nil {
				t.Fatal("the retry was accepted")
			}
			if got := o.Count("terminal.close"); got != 0 {
				t.Errorf("a refused retry closed the kept tab (%d closes)", got)
			}
			if _, kept := isKept(n, c.Pane.String()); !kept {
				t.Error("a refused retry took the kept tab")
			}
		})
	}
}

// When Orca will not close the stalled start's tab, the retry fails with why and opens no second tab beside a claude
// that may still run there, sync or async; the tab stays kept with its expiry, and a later retry closes it then.
func TestOrcaRetryStopsWhenTheStalledTabWillNotClose(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprintf("async=%v", async), func(t *testing.T) {
			n, _, o, c, root := keptOrcaStart(t, true)
			until, _ := isKept(n, c.Pane.String())
			o.Handle("terminal.close", func(map[string]any) any { return orcarttest.Fail{Code: "permission_denied"} })
			_, err := request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": root, "in_pane": c.Pane.String(), "async": async})
			if async && err == nil {
				err = waitAsyncStart(t, n)
			}
			if err == nil || !strings.Contains(err.Error(), "permission_denied") {
				t.Errorf("retry error: %v", err)
			}
			if got := o.Count("terminal.create"); got != 1 {
				t.Errorf("terminals created: %d, want only the kept one", got)
			}
			if after, kept := isKept(n, c.Pane.String()); !kept || !after.Equal(until) {
				t.Fatalf("not kept as before: kept %v, expiry %v, was %v", kept, after, until)
			}
			o.Handle("terminal.close", nil)
			_, _ = request(t, n, "session.start", map[string]any{"session_id": sidA, "cwd": root, "in_pane": c.Pane.String()})
			if got := o.Count("terminal.create"); got != 2 {
				t.Errorf("the next retry did not replace the tab: creates %d", got)
			}
		})
	}
}

// Retries named to the same kept tab at once: one holds it and closes it once; the others are refused rather than
// opening tabs beside it, also while that close is still going and then fails.
func TestOrcaConcurrentRetriesOfTheStalledTab(t *testing.T) {
	t.Run("all at once", func(t *testing.T) {
		n, _, o, c, root := keptOrcaStart(t, true)
		var wg sync.WaitGroup
		for range 16 {
			wg.Go(func() {
				_, err := request(t, n, "session.start", map[string]any{"session_id": newUUID(), "cwd": root, "in_pane": c.Pane.String()})
				if err != nil && strings.Contains(err.Error(), "only allowed once") {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		closes := 0
		for _, call := range o.Trace("terminal.close") {
			if strings.Contains(call, `"`+keptOrcaHandle+`"`) {
				closes++
			}
		}
		if closes != 1 {
			t.Errorf("the stalled tab was closed %d times", closes)
		}
	})
	t.Run("while the close is pending", func(t *testing.T) {
		n, _, o, c, root := keptOrcaStart(t, true)
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		o.Handle("terminal.close", func(map[string]any) any {
			once.Do(func() { close(entered) })
			<-release
			return orcarttest.Fail{Code: "permission_denied"}
		})
		first := make(chan error, 1)
		go func() {
			_, err := request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": root, "in_pane": c.Pane.String()})
			first <- err
		}()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the close did not begin")
		}
		_, second := request(t, n, "session.start", map[string]any{"session_id": sidA, "cwd": root, "in_pane": c.Pane.String()})
		created := o.Count("terminal.create")
		close(release)
		if err := <-first; err == nil || !strings.Contains(err.Error(), "permission_denied") {
			t.Errorf("first retry: %v", err)
		}
		if created != 1 || rpcCode(second) != "start_tab_taken" {
			t.Errorf("a second retry opened a tab while the first held the kept one: creates %d, %v", created, second)
		}
		if _, kept := isKept(n, c.Pane.String()); !kept {
			t.Error("the tab that did not close is no longer kept")
		}
	})
}

// The stalled agent comes up in its kept tab while the retry is still being checked (its SessionStart hook binds the
// pane, here during the workspace lookup): the retry leaves that tab open and opens a new one rather than close the
// agent it now holds.
func TestOrcaRetryLeavesATabWhoseAgentCameUp(t *testing.T) {
	n, _, o, c, root := keptOrcaStart(t, true)
	var bound atomic.Bool
	o.Handle("worktree.ps", func(map[string]any) any {
		n.bindPane(c.Pane.String(), sidA)
		bound.Store(true)
		return map[string]any{"worktrees": []any{orcarttest.Worktree("wt-proj", root, "refs/heads/main")}, "totalCount": 1, "truncated": false}
	})
	_, _ = request(t, n, "session.start", map[string]any{"session_id": startID, "cwd": root, "in_pane": c.Pane.String()})
	if !bound.Load() {
		t.Fatal("the pane was not bound during the start's checks")
	}
	for _, w := range o.Writes() {
		if w.Close && w.Handle == keptOrcaHandle {
			t.Fatal("the retry closed the tab whose agent came up")
		}
	}
	if got := startLineIn(o); got == "" || got == keptOrcaHandle {
		t.Errorf("the start line went to %q, want a new tab", got)
	}
	n.mu.Lock()
	k, ok := n.keptStarts[c.Pane.String()]
	n.mu.Unlock()
	if !ok || k.taken {
		t.Errorf("the tab is not left kept (its expiry leaves a bound tab open): %v %+v", ok, k)
	}
}
