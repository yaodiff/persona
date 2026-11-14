package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// "New worktree…" against real temp Git repositories and a fake herdr (docs/integrations/terminal.md "New worktree").

// setWorktreeSupport decides whether the fake herdr "has" the worktree command, for this test only.
func setWorktreeSupport(t *testing.T, ok bool) {
	t.Helper()
	reset := func() {
		worktreeSupport.once = sync.Once{}
		worktreeSupport.ok = false
	}
	reset()
	worktreeSupport.once.Do(func() { worktreeSupport.ok = ok })
	t.Cleanup(reset)
}

type worktreeFixture struct {
	n    *Node
	f    *fakeHerdr
	home string
	repo string // the repository of workspace w1
}

func TestAutomaticWorktreeNamesAlsoWorkWithHerdr(t *testing.T) {
	w := worktreeNode(t)
	prep, err := request(t, w.n, "worktree.prepare", map[string]any{"workspace_id": "w1"})
	if err != nil || prep["automatic_name"] != true || prep["choose_parent"] == true {
		t.Fatalf("herdr naming capabilities: %v, %v", prep, err)
	}
	seen := map[string]bool{}
	for range 2 {
		res, err := request(t, w.n, "worktree.create", map[string]any{"workspace_id": "w1", "auto_name": true})
		if err != nil {
			t.Fatal(err)
		}
		branch, path := str(res["branch"]), str(res["path"])
		if !strings.HasPrefix(branch, "persona/worktree-") || seen[branch] || path == "" {
			t.Fatalf("must return a unique generated branch and checkout: %v", res)
		}
		seen[branch] = true
		if head := runPullGit(t, path, "rev-parse", "HEAD"); head != runPullGit(t, w.repo, "rev-parse", "HEAD") {
			t.Fatal("generated worktree does not start at the source HEAD")
		}
	}
}

// worktreeNode is a node on a fake herdr whose workspace w1 is a real repository (branch main, one commit, a second
// local branch "spare"). The fake's worktree.create does what herdr does: a real `git worktree add`.
func worktreeNode(t *testing.T) worktreeFixture {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Sample Dev")
	t.Setenv("GIT_AUTHOR_EMAIL", "dev@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Sample Dev")
	t.Setenv("GIT_COMMITTER_EMAIL", "dev@example.com")
	setWorktreeSupport(t, true)
	streamingHerdr()
	n, f, home := herdrNode(t)
	repo := mkdir(t, filepath.Join(home, "proj"))
	runPullGit(t, repo, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repo, "app.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPullGit(t, repo, "add", "app.txt")
	runPullGit(t, repo, "commit", "-m", "first")
	runPullGit(t, repo, "branch", "spare")
	f.reply("session.snapshot", snapshot(
		[]any{map[string]any{"workspace_id": "w1", "label": "proj", "number": 1, "branch": "main"}},
		[]any{map[string]any{"tab_id": "w1:t1", "workspace_id": "w1", "label": "1", "number": 1}},
		nil, []any{map[string]any{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "cwd": repo}},
	))
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": "proj", "number": 1}}})
	f.reply("pane.list", map[string]any{"panes": []any{map[string]any{"workspace_id": "w1", "pane_id": "w1:p1", "cwd": repo}}})
	f.handle("worktree.create", func(p map[string]any) any {
		path, branch := str(p["path"]), str(p["branch"])
		args := []string{"worktree", "add", "-b", branch, "--", path}
		if _, err := git(t.Context(), repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
			args = []string{"worktree", "add", "--", path, branch}
		} else if base := str(p["base"]); base != "" {
			args = append(args, base)
		}
		if out, err := gitRun(t.Context(), repo, gitOpTimeout, args...); err != nil {
			return herdrFail{Code: "worktree_create_failed", Message: "Preparing worktree\n" + out}
		}
		return map[string]any{
			"workspace": map[string]any{"workspace_id": "w2", "label": "proj", "number": 2},
			"tab":       map[string]any{"tab_id": "w2:t1"}, "root_pane": map[string]any{"pane_id": "w2:p1"},
			"worktree": map[string]any{"path": path, "branch": branch},
		}
	})
	f.handle("workspace.create", func(p map[string]any) any {
		return map[string]any{
			"workspace": map[string]any{"workspace_id": "w3", "label": str(p["label"]), "number": 3},
			"tab":       map[string]any{"tab_id": "w3:t1"}, "root_pane": map[string]any{"pane_id": "w3:p1"},
		}
	})
	listSessions(t, n)
	f.reset()
	return worktreeFixture{n: n, f: f, home: home, repo: repo}
}

// snapshotWorktreeHost deliberately exposes no directorySpaces capability, so these regressions exercise
// the full-layout path used by the other terminal hosts.
type snapshotWorktreeHost struct {
	host.Host
	host.Launcher
}

func snapshotWorktreeNode(t *testing.T) worktreeFixture {
	t.Helper()
	w := worktreeNode(t)
	h := w.n.hosts.Get(host.Herdr)
	w.n.hosts.Set(&snapshotWorktreeHost{Host: h, Launcher: h.(host.Launcher)})
	return w
}

func branchOf(t *testing.T, dir string) string { return runPullGit(t, dir, "branch", "--show-current") }

// resolved is dir as git names it: its symlinks resolved (on macOS the temporary directories are under /var, a link
// to /private/var, #2650).
func resolved(t *testing.T, dir string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestWorktreePrepareReadsTheRepository(t *testing.T) {
	w := worktreeNode(t)
	res, err := request(t, w.n, "worktree.prepare", map[string]any{"workspace_id": "w1"})
	if err != nil {
		t.Fatal(err)
	}
	if res["root"] != resolved(t, w.repo) || res["repoName"] != "proj" || res["branch"] != "main" || res["path"] != filepath.Join(w.home, ".herdr", "worktrees", "proj") {
		t.Errorf("prepare = %v", res)
	}

	// herdr's own setting for where checkouts go
	cfg := mkdir(t, filepath.Join(w.home, ".config", "herdr"))
	if err := os.WriteFile(filepath.Join(cfg, "config.toml"), []byte("[worktrees]\ndirectory = \"~/trees\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, _ = request(t, w.n, "worktree.prepare", map[string]any{"workspace_id": "w1"}); res["path"] != filepath.Join(w.home, "trees", "proj") {
		t.Errorf("a configured directory: %v", res["path"])
	}

	// a detached HEAD has no branch to offer
	runPullGit(t, w.repo, "checkout", "--detach")
	if res, _ = request(t, w.n, "worktree.prepare", map[string]any{"workspace_id": "w1"}); res["branch"] != "" || res["repoName"] != "proj" {
		t.Errorf("detached: %v", res)
	}
	if got := w.f.trace("worktree.create", "workspace.create"); len(got) != 0 {
		t.Errorf("prepare changed something: %v", got)
	}
}

func TestWorktreePrepareUsesSnapshotDirectoryForCwd(t *testing.T) {
	w := snapshotWorktreeNode(t)
	w.f.reply("workspace.list", map[string]any{"workspaces": []any{
		map[string]any{"workspace_id": "w1", "label": "proj", "number": 1},
	}})
	w.f.reply("session.snapshot", snapshot(
		[]any{map[string]any{"workspace_id": "w1", "label": "proj", "number": 1}},
		[]any{map[string]any{"tab_id": "w1:t1", "workspace_id": "w1", "label": "1", "number": 1}},
		nil, []any{map[string]any{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "cwd": w.repo}},
	))
	listSessions(t, w.n)
	w.f.reset()

	res, err := request(t, w.n, "worktree.prepare", map[string]any{"cwd": w.repo})
	if err != nil {
		t.Fatal(err)
	}
	if res["root"] != resolved(t, w.repo) || res["path"] != filepath.Join(w.home, ".herdr", "worktrees", "proj") {
		t.Fatalf("prepare from pane directories = %v", res)
	}
	if got := w.f.trace("session.snapshot"); len(got) != 1 {
		t.Fatalf("snapshot calls = %v", got)
	}

}

func TestSpaceHoldingFallsBackToWorkspaceList(t *testing.T) {
	w := snapshotWorktreeNode(t)
	w.f.reply("session.snapshot", herdrFail{Code: "unavailable", Message: "snapshot unavailable"})
	w.f.reply("workspace.list", map[string]any{"workspaces": []any{
		map[string]any{"workspace_id": "w1", "label": "proj", "number": 1, "cwd": w.repo},
	}})
	h, err := w.n.hostOfKind(host.Herdr)
	if err != nil {
		t.Fatal(err)
	}
	w.f.reset()

	workspace, err := spaceHolding(context.Background(), h, w.repo)
	if err != nil {
		t.Fatal(err)
	}
	if workspace != "w1" {
		t.Fatalf("spaceHolding from workspace.list = %q", workspace)
	}
	if got := w.f.trace("workspace.list"); len(got) != 1 {
		t.Fatalf("workspace.list calls = %v", got)
	}
	if got := w.f.trace("session.snapshot"); len(got) != 1 {
		t.Fatalf("snapshot calls = %v", got)
	}

}

func TestSpaceHoldingEmptySnapshotPreservesWorkspaceListError(t *testing.T) {
	w := snapshotWorktreeNode(t)
	w.f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	w.f.reply("workspace.list", herdrFail{Code: "unavailable", Message: "workspace list unavailable"})
	h, err := w.n.hostOfKind(host.Herdr)
	if err != nil {
		t.Fatal(err)
	}
	w.f.reset()

	_, err = spaceHolding(context.Background(), h, w.repo)
	if err == nil || !strings.Contains(err.Error(), "workspace list unavailable") || strings.Contains(err.Error(), "不在这台电脑的任何工作区里") {
		t.Fatalf("spaceHolding with an empty snapshot: %v", err)
	}
	if got := w.f.trace("workspace.list"); len(got) != 1 {
		t.Fatalf("workspace.list calls = %v", got)
	}
	if got := w.f.trace("session.snapshot"); len(got) != 1 {
		t.Fatalf("snapshot calls = %v", got)
	}

}

func TestWorktreePrepareDoesNotGuessFromConflictingPaneDirectories(t *testing.T) {
	w := worktreeNode(t)
	w.f.reply("workspace.list", map[string]any{"workspaces": []any{
		map[string]any{"workspace_id": "w1", "label": "proj", "number": 1},
	}})
	panes := []any{
		map[string]any{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "cwd": w.repo},
		map[string]any{"pane_id": "w1:p2", "tab_id": "w1:t1", "workspace_id": "w1", "cwd": filepath.Join(w.home, "other")},
	}
	w.f.reply("pane.list", map[string]any{"panes": panes})
	w.f.reply("session.snapshot", snapshot(
		[]any{map[string]any{"workspace_id": "w1", "label": "proj", "number": 1}},
		[]any{map[string]any{"tab_id": "w1:t1", "workspace_id": "w1", "label": "1", "number": 1}},
		nil, panes,
	))
	listSessions(t, w.n)
	w.f.reset()

	h := w.n.hosts.Get(host.Herdr)
	if workspace, err := spaceHolding(t.Context(), h, w.repo); workspace != "" || !errors.Is(err, errNoHoldingSpace) {
		t.Fatalf("conflicting pane directories must not hold the source: %q, %v", workspace, err)
	}

	res, err := request(t, w.n, "worktree.prepare", map[string]any{"cwd": w.repo})
	if err != nil {
		t.Fatal(err)
	}
	if res["root"] != resolved(t, w.repo) || res["path"] != filepath.Join(w.home, ".herdr", "worktrees", "proj") {
		t.Fatalf("prepare with an explicit cwd and conflicting pane directories = %v", res)
	}
	if got := w.f.trace("worktree.create", "workspace.create"); len(got) != 0 {
		t.Errorf("prepare changed something: %v", got)
	}
}

func TestWorktreePrepareNamesTheRepositoryOfALinkedCheckout(t *testing.T) {
	w := worktreeNode(t)
	linked := filepath.Join(w.home, "elsewhere", "feature-checkout")
	runPullGit(t, w.repo, "worktree", "add", "-b", "feature", linked)
	w.f.reply("session.snapshot", snapshot(
		[]any{map[string]any{"workspace_id": "w1", "label": "linked", "number": 1}}, nil, nil,
		[]any{map[string]any{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "cwd": linked}},
	))
	listSessions(t, w.n)
	res, err := request(t, w.n, "worktree.prepare", map[string]any{"workspace_id": "w1"})
	if err != nil {
		t.Fatal(err)
	}
	if res["repoName"] != "proj" || res["branch"] != "feature" || res["root"] != resolved(t, linked) {
		t.Errorf("prepare in a linked checkout = %v", res)
	}
}

func TestWorktreePrepareRefusals(t *testing.T) {
	w := worktreeNode(t)
	if _, err := request(t, w.n, "worktree.prepare", map[string]any{"workspace_id": "w9"}); err == nil {
		t.Error("prepared a workspace the host does not list")
	}
	if _, err := request(t, w.n, "worktree.prepare", map[string]any{}); err == nil {
		t.Error("prepared no workspace")
	}
	plain := mkdir(t, filepath.Join(w.home, "notes"))
	w.f.reply("session.snapshot", snapshot([]any{map[string]any{"workspace_id": "w1", "label": "notes", "number": 1}}, nil, nil,
		[]any{map[string]any{"workspace_id": "w1", "pane_id": "w1:p1", "cwd": plain}}))
	listSessions(t, w.n)
	if _, err := request(t, w.n, "worktree.prepare", map[string]any{"workspace_id": "w1"}); err == nil || !strings.Contains(err.Error(), "Git") {
		t.Errorf("a directory that is not a repository: %v", err)
	}
}

// A new branch from the workspace's HEAD and an existing local branch both go through herdr, with the typed path.
func TestWorktreeCreateThroughHerdr(t *testing.T) {
	w := worktreeNode(t)
	dest := filepath.Join(w.home, ".herdr", "worktrees", "proj", "feat-x")
	res, err := request(t, w.n, "worktree.create", map[string]any{"workspace_id": "w1", "branch": " feat/x ", "path": dest})
	if err != nil {
		t.Fatal(err)
	}
	if res["workspace_id"] != "w2" || res["path"] != dest || res["branch"] != "feat/x" || res["terminal"] != "pane:w2:p1" {
		t.Errorf("create = %v", res)
	}
	assertTrace(t, w.f.trace("worktree.create", "workspace.create"), []string{
		call("worktree.create", map[string]any{"workspace_id": "w1", "branch": "feat/x", "path": dest, "focus": false}),
	})
	if got := branchOf(t, dest); got != "feat/x" {
		t.Errorf("the checkout is on %q", got)
	}

	w.f.reset()
	spare := filepath.Join(w.home, "trees", "spare")
	if _, err := request(t, w.n, "worktree.create", map[string]any{"workspace_id": "w1", "branch": "spare", "path": spare}); err != nil {
		t.Fatal(err)
	}
	if got := branchOf(t, spare); got != "spare" {
		t.Errorf("an existing branch checked out as %q", got)
	}
}

func TestWorktreeCreateFromLinkedCheckoutUsesMainRepository(t *testing.T) {
	for _, tc := range []struct {
		name          string
		mainWorkspace string
	}{
		{name: "main checkout workspace is open", mainWorkspace: "checkout"},
		{name: "only an ancestor workspace is open", mainWorkspace: "ancestor"},
		{name: "main checkout workspace is not open"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := worktreeNode(t)
			linked := filepath.Join(w.home, "elsewhere", "feature-checkout")
			runPullGit(t, w.repo, "worktree", "add", "-b", "feature", linked)
			if err := os.WriteFile(filepath.Join(linked, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runPullGit(t, linked, "add", "feature.txt")
			runPullGit(t, linked, "commit", "-m", "feature")
			base := runPullGit(t, linked, "rev-parse", "HEAD")
			if base == runPullGit(t, w.repo, "rev-parse", "HEAD") {
				t.Fatal("the fixture does not distinguish the linked and main HEADs")
			}
			spaces := []any{
				map[string]any{"workspace_id": "w-linked", "label": "linked", "number": 2, "cwd": linked},
			}
			panes := []any{
				map[string]any{"pane_id": "w-linked:p1", "tab_id": "w-linked:t1", "workspace_id": "w-linked", "cwd": linked},
			}
			mainDir := ""
			switch tc.mainWorkspace {
			case "checkout":
				mainDir = w.repo
			case "ancestor":
				mainDir = filepath.Dir(w.repo)
			}
			if mainDir != "" {
				spaces = append(spaces, map[string]any{"workspace_id": "w-main", "label": "proj", "number": 1, "cwd": mainDir})
				panes = append(panes, map[string]any{"pane_id": "w-main:p1", "tab_id": "w-main:t1", "workspace_id": "w-main", "cwd": mainDir})
			}
			w.f.reply("session.snapshot", snapshot(spaces, nil, nil, panes))
			listSessions(t, w.n)
			w.f.reset()

			dest := filepath.Join(w.home, "trees", "from-linked")
			_, err := request(t, w.n, "worktree.create", map[string]any{
				"cwd": linked, "branch": "task/from-linked", "path": dest,
			})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"branch": "task/from-linked", "path": dest, "focus": false, "base": base}
			if tc.mainWorkspace == "checkout" {
				want["workspace_id"] = "w-main"
			} else {
				want["cwd"] = resolved(t, w.repo)
			}
			assertTrace(t, w.f.trace("worktree.create"), []string{call("worktree.create", want)})
			if got := runPullGit(t, dest, "rev-parse", "HEAD"); got != base {
				t.Fatalf("the new worktree started at %s, want linked HEAD %s", got, base)
			}

			w.f.reset()
			runPullGit(t, linked, "checkout", "--orphan", "unborn")
			existingDest := filepath.Join(w.home, "trees", "existing-branch")
			if _, err := request(t, w.n, "worktree.create", map[string]any{
				"cwd": linked, "branch": "spare", "path": existingDest,
			}); err != nil {
				t.Fatal(err)
			}
			existingWant := map[string]any{"branch": "spare", "path": existingDest, "focus": false}
			if tc.mainWorkspace == "checkout" {
				existingWant["workspace_id"] = "w-main"
			} else {
				existingWant["cwd"] = resolved(t, w.repo)
			}
			assertTrace(t, w.f.trace("worktree.create"), []string{call("worktree.create", existingWant)})
		})
	}
}

// An empty directory is a place git fills; anything else is refused before herdr hears of it, and so are names and
// paths that could be read as something else.
func TestWorktreeCreateRefusals(t *testing.T) {
	w := worktreeNode(t)
	full := mkdir(t, filepath.Join(w.home, "full"))
	if err := os.WriteFile(filepath.Join(full, "keep.txt"), []byte("mine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(w.home, "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok := filepath.Join(w.home, "ok")
	// The repository through a link of the user's: .git is the same .git however the path reaches it (#2650).
	link := filepath.Join(w.home, "repo-link")
	if err := os.Symlink(w.repo, link); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]map[string]any{
		"a path holding something":    {"branch": "b1", "path": full},
		"a path that is a file":       {"branch": "b2", "path": file},
		"a relative path":             {"branch": "b3", "path": "trees/b3"},
		"no path":                     {"branch": "b4", "path": "  "},
		"inside .git":                 {"branch": "b5", "path": filepath.Join(w.repo, ".git", "wt")},
		"inside .git through a link":  {"branch": "b6", "path": filepath.Join(link, ".git", "wt")},
		"deep in .git through a link": {"branch": "b7", "path": filepath.Join(link, ".git", "new", "wt")},
		"no branch":                   {"branch": " ", "path": ok},
		"an option as a branch":       {"branch": "--detach", "path": ok},
		"a bad ref name":              {"branch": "a..b", "path": ok},
		"a space in the name":         {"branch": "my branch", "path": ok},
		"a revision expression":       {"branch": "@{-1}", "path": ok},
		"a trailing slash":            {"branch": "feat/", "path": ok},
	} {
		p["workspace_id"] = "w1"
		if _, err := request(t, w.n, "worktree.create", p); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
	if _, err := request(t, w.n, "worktree.create", map[string]any{"workspace_id": "w9", "branch": "b", "path": ok}); err == nil {
		t.Error("created from a workspace the host does not list")
	}
	if got := w.f.trace("worktree.create", "workspace.create"); len(got) != 0 {
		t.Errorf("herdr heard of a refused request: %v", got)
	}
	if b, err := os.ReadFile(filepath.Join(full, "keep.txt")); err != nil || string(b) != "mine\n" {
		t.Errorf("a directory with files was touched: %v", err)
	}

	empty := mkdir(t, filepath.Join(w.home, "empty"))
	if _, err := request(t, w.n, "worktree.create", map[string]any{"workspace_id": "w1", "branch": "b6", "path": empty}); err != nil {
		t.Errorf("an empty directory refused: %v", err)
	}
}

// What git says is what the user sees, nothing is forced, and nothing is left half-made.
func TestWorktreeCreateReportsGitsReason(t *testing.T) {
	w := worktreeNode(t)
	// main is checked out in the repository itself
	dest := filepath.Join(w.home, "trees", "main")
	_, err := request(t, w.n, "worktree.create", map[string]any{"workspace_id": "w1", "branch": "main", "path": dest})
	if err == nil || !strings.Contains(err.Error(), "already") || strings.Contains(err.Error(), "Preparing") || strings.Contains(err.Error(), "fatal:") {
		t.Fatalf("a branch checked out elsewhere: %v", err)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("a failed create left a directory")
	}
	for _, c := range w.f.calls() {
		if _, forced := c.Params["force"]; forced {
			t.Errorf("a forcing parameter was sent: %v", c)
		}
	}
}

// A herdr without `worktree`: git adds the checkout, herdr opens a workspace in it.
func TestWorktreeCreateWithoutHerdrsWorktree(t *testing.T) {
	w := worktreeNode(t)
	setWorktreeSupport(t, false)
	dest := filepath.Join(w.home, "trees", "fresh")
	res, err := request(t, w.n, "worktree.create", map[string]any{"workspace_id": "w1", "branch": "fresh", "path": dest})
	if err != nil {
		t.Fatal(err)
	}
	if res["workspace_id"] != "w3" || res["path"] != dest {
		t.Errorf("create = %v", res)
	}
	assertTrace(t, w.f.trace("worktree.create", "workspace.create"), []string{
		call("workspace.create", map[string]any{"cwd": dest, "label": "proj", "focus": false}),
	})
	if got := branchOf(t, dest); got != "fresh" {
		t.Errorf("new branch = %q", got)
	}
	if head, spare := runPullGit(t, dest, "rev-parse", "HEAD"), runPullGit(t, w.repo, "rev-parse", "main"); head != spare {
		t.Errorf("the new branch did not start at the repository's HEAD")
	}

	spare := filepath.Join(w.home, "trees", "spare")
	if _, err := request(t, w.n, "worktree.create", map[string]any{"workspace_id": "w1", "branch": "spare", "path": spare}); err != nil {
		t.Fatal(err)
	}
	if got := branchOf(t, spare); got != "spare" {
		t.Errorf("existing branch = %q", got)
	}

	// git refuses a branch checked out elsewhere; no workspace is opened for it
	w.f.reset()
	clash := filepath.Join(w.home, "trees", "clash")
	if _, err := request(t, w.n, "worktree.create", map[string]any{"workspace_id": "w1", "branch": "spare", "path": clash}); err == nil {
		t.Error("checked a branch out twice")
	}
	if got := w.f.trace("workspace.create"); len(got) != 0 {
		t.Errorf("a workspace was opened for a failed checkout: %v", got)
	}
}

// herdrCoreWorktreeNode is a task source checkout known to Herdr App, on a core
// with terminal and layout APIs but no native worktree API.
func herdrCoreWorktreeNode(t *testing.T) (worktreeFixture, *fakeHC) {
	t.Helper()
	w := worktreeNode(t)
	hc := newFakeHC(t)
	w.n.hosts = host.NewRegistry(hcHost(hc))
	w.n.eng.name = proto.EngineHerdrCore
	hc.doc = appDoc(w.repo)
	hc.setTerminals(hcTerminal("t1", "", "", 50, "zsh", w.repo, "S1", "T1", "proj / 1"))
	return w, hc
}

// A writable task's prepare/create/start sequence keeps its engine: Persona
// adds the checkout with git, then herdr-core starts the agent in that cwd.
func TestHerdrCoreTaskWorktreeFallsBackToGitAndStartsThere(t *testing.T) {
	w, hc := herdrCoreWorktreeNode(t)
	w.n.claudeStart, w.n.claudeReady = 200*time.Millisecond, 200*time.Millisecond
	prep, err := request(t, w.n, "worktree.prepare", map[string]any{"cwd": w.repo})
	if err != nil {
		t.Fatal(err)
	}
	if calls := hc.trace("terminal.run", "doc.set"); len(calls) != 0 {
		t.Fatalf("prepare changed the engine: %v", calls)
	}
	dest := filepath.Join(str(prep["path"]), "task-test")
	created, err := request(t, w.n, "worktree.create", map[string]any{
		"cwd": w.repo, "branch": "task/test", "path": dest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created["path"] != dest || created["branch"] != "task/test" || created["workspace_id"] == "" || dest == w.repo {
		t.Fatalf("create = %v", created)
	}
	panes, err := w.n.listPanes(t.Context())
	if err != nil || len(panes.Hosts) != 1 || !panes.Hosts[0].Worktree {
		t.Fatalf("herdr-core does not advertise its worktree fallback: hosts=%+v err=%v", panes.Hosts, err)
	}
	if canWorktree(&ternHost{}) {
		t.Fatal("the herdr-core fallback enabled an unrelated launcher")
	}
	if got := branchOf(t, dest); got != "task/test" {
		t.Fatalf("checkout branch = %q", got)
	}
	if got := branchOf(t, w.repo); got != "main" {
		t.Fatalf("source checkout changed to %q", got)
	}
	if head, source := runPullGit(t, dest, "rev-parse", "HEAD"), runPullGit(t, w.repo, "rev-parse", "HEAD"); head != source {
		t.Fatal("the task checkout did not start at the source HEAD")
	}

	// The fake shell never runs claude. Reaching its terminal.run and typed start
	// proves the created cwd entered the real herdr-core launch flow.
	hc.reset()
	_, err = request(t, w.n, "session.start", map[string]any{
		"session_id": "0e000000-0000-4000-8000-000000001112", "cwd": dest,
		"workspace_id": created["workspace_id"], "prompt": "write code", "permission_mode": "bypassPermissions",
	})
	if err == nil {
		t.Fatal("the fake shell unexpectedly started claude")
	}
	runs := hc.trace("terminal.run")
	if len(runs) != 1 || !strings.Contains(runs[0], `"cwd":"`+dest+`"`) {
		t.Fatalf("agent did not enter startup in the new checkout: runs=%v err=%v", runs, err)
	}
	if sends := hc.trace("terminal.send"); len(sends) != 1 {
		t.Fatalf("agent launch command was not typed: %v", sends)
	}
	if calls := w.f.trace("worktree.create", "workspace.create"); len(calls) != 0 {
		t.Fatalf("task fell back to classic herdr: %v", calls)
	}
}

func TestHerdrCoreTaskWorktreeDoesNotOverwriteExistingDirectory(t *testing.T) {
	w, hc := herdrCoreWorktreeNode(t)
	dest := mkdir(t, filepath.Join(w.home, "existing-worktree"))
	keep := filepath.Join(dest, "keep.txt")
	if err := os.WriteFile(keep, []byte("existing work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := request(t, w.n, "worktree.create", map[string]any{
		"cwd": w.repo, "branch": "task/test", "path": dest,
	})
	if err == nil || !strings.Contains(err.Error(), "不是空目录") {
		t.Fatalf("non-empty destination was not refused: %v", err)
	}
	if b, readErr := os.ReadFile(keep); readErr != nil || string(b) != "existing work\n" {
		t.Fatalf("existing directory changed: %q, %v", b, readErr)
	}
	if calls := hc.trace("terminal.run", "doc.set"); len(calls) != 0 {
		t.Fatalf("failed preparation opened or placed a terminal: %v", calls)
	}
}

func TestHerdrCoreTaskWorktreeOpenFailureKeepsCheckout(t *testing.T) {
	w, hc := herdrCoreWorktreeNode(t)
	dest := filepath.Join(w.home, "trees", "task-test")
	hc.handle("terminal.run", func(map[string]any) any { return hcFail{Kind: "unavailable"} })
	params := map[string]any{"cwd": w.repo, "branch": "task/test", "path": dest}
	created, err := request(t, w.n, "worktree.create", params)
	if err == nil || !strings.Contains(err.Error(), "检出已创建，但没能打开工作区") {
		t.Fatalf("workspace open failure = %v", err)
	}
	if created["workspace_id"] != "" || created["path"] != "" || branchOf(t, dest) != "task/test" || branchOf(t, w.repo) != "main" {
		t.Fatalf("partial checkout was lost or misreported: created=%v", created)
	}
	if kills := hc.trace("terminal.kill"); len(kills) != 0 {
		t.Fatalf("an existing terminal was closed: %v", kills)
	}

	// A blind retry cannot adopt or overwrite the partial checkout.
	hc.reset()
	if _, err = request(t, w.n, "worktree.create", params); err == nil || !strings.Contains(err.Error(), "不是空目录") {
		t.Fatalf("retry did not preserve the partial checkout: %v", err)
	}
	if calls := hc.trace("terminal.run", "doc.set"); len(calls) != 0 {
		t.Fatalf("retry touched the engine: %v", calls)
	}
}

// The terminal browser learns whether a host can make worktrees from the host list; a host that cannot do either is
// not offered it.
func TestPanesListAdvertisesWorktrees(t *testing.T) {
	w := worktreeNode(t)
	res, err := w.n.listPanes(t.Context())
	if err != nil || len(res.Hosts) != 1 || !res.Hosts[0].Worktree {
		t.Fatalf("hosts = %+v %v", res.Hosts, err)
	}
	if !canWorktree(w.n.hosts.Get(host.Herdr)) {
		t.Error("herdr with worktree cannot")
	}
	setWorktreeSupport(t, false)
	if !canWorktree(w.n.hosts.Get(host.Herdr)) {
		t.Error("an older herdr gets the git way")
	}
}

func TestWorktreeFromPaneDirectory(t *testing.T) {
	w := worktreeNode(t)
	res, err := request(t, w.n, "worktree.prepare", map[string]any{"cwd": w.repo})
	if err != nil || res["root"] != resolved(t, w.repo) {
		t.Fatalf("prepare: %v, %v", res, err)
	}
	dest := filepath.Join(w.home, "pane-checkout")
	res, err = request(t, w.n, "worktree.create", map[string]any{"cwd": w.repo, "branch": "pane-source", "path": dest})
	if err != nil || res["workspace_id"] != "w2" {
		t.Fatalf("create: %v, %v", res, err)
	}
	if branchOf(t, dest) != "pane-source" {
		t.Fatal("wrong branch")
	}
}

func TestWorktreeWithoutHoldingSpaceKeepsIsolation(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "automatic"}[automatic], func(t *testing.T) {
			w := worktreeNode(t)
			w.f.reply("workspace.list", map[string]any{"workspaces": []any{}})
			res, err := request(t, w.n, "worktree.prepare", map[string]any{"cwd": w.repo})
			if err != nil || res["root"] != resolved(t, w.repo) {
				t.Fatalf("prepare fallback: %v, %v", res, err)
			}
			dest := filepath.Join(w.home, "isolated")
			args := map[string]any{"cwd": w.repo, "branch": "isolated", "path": dest}
			if automatic {
				args = map[string]any{"cwd": w.repo, "auto_name": true}
			}
			res, err = request(t, w.n, "worktree.create", args)
			if err != nil {
				t.Fatal(err)
			}
			path := str(res["path"])
			if path == "" || path == w.repo || branchOf(t, w.repo) != "main" || branchOf(t, path) != str(res["branch"]) {
				t.Fatalf("lost isolation: %v", res)
			}
			if len(w.f.trace("worktree.create")) != 0 || len(w.f.trace("workspace.create")) != 1 {
				t.Fatal("must use Git and open a plain workspace")
			}
			plain := mkdir(t, filepath.Join(w.home, "not-git"))
			if _, err := request(t, w.n, "worktree.prepare", map[string]any{"cwd": plain}); err == nil {
				t.Fatal("accepted non-Git directory")
			}
		})
	}
}

func TestWorktreeFromSecondRepositoryPaneKeepsItsSource(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "automatic"}[automatic], func(t *testing.T) {
			w := worktreeNode(t)
			other := mkdir(t, filepath.Join(w.home, "other"))
			runPullGit(t, other, "init", "--initial-branch=main")
			if err := os.WriteFile(filepath.Join(other, "other.txt"), []byte("other repository\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runPullGit(t, other, "add", "other.txt")
			runPullGit(t, other, "commit", "-m", "other repository")
			w.f.reply("workspace.list", map[string]any{"workspaces": []any{
				map[string]any{"workspace_id": "w1", "label": "other", "number": 1},
			}})
			panes := []any{
				map[string]any{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1", "cwd": other},
				map[string]any{"pane_id": "w1:p2", "tab_id": "w1:t1", "workspace_id": "w1", "cwd": w.repo},
			}
			w.f.reply("pane.list", map[string]any{"panes": panes})
			w.f.reply("session.snapshot", snapshot(
				[]any{map[string]any{"workspace_id": "w1", "label": "other", "number": 1}},
				[]any{map[string]any{"tab_id": "w1:t1", "workspace_id": "w1", "label": "1", "number": 1}}, nil, panes,
			))
			// Model herdr's source selection: the workspace's first pane belongs to the other repository.
			w.f.handle("worktree.create", func(p map[string]any) any {
				runPullGit(t, other, "worktree", "add", "-b", str(p["branch"]), "--", str(p["path"]))
				return map[string]any{
					"workspace": map[string]any{"workspace_id": "w2"},
					"tab":       map[string]any{"tab_id": "w2:t1"}, "root_pane": map[string]any{"pane_id": "w2:p1"},
					"worktree": map[string]any{"path": p["path"], "branch": p["branch"]},
				}
			})
			listSessions(t, w.n)
			w.f.reset()
			prep, err := request(t, w.n, "worktree.prepare", map[string]any{"cwd": w.repo})
			if err != nil || prep["root"] != resolved(t, w.repo) {
				t.Fatalf("prepare the requested repository: %v, %v", prep, err)
			}
			args := map[string]any{"cwd": w.repo, "branch": "feature/requested", "path": filepath.Join(w.home, "requested-checkout")}
			if automatic {
				args = map[string]any{"cwd": w.repo, "auto_name": true}
			}
			res, err := request(t, w.n, "worktree.create", args)
			if err != nil {
				t.Fatal(err)
			}
			path := str(res["path"])
			if got, want := runPullGit(t, path, "rev-parse", "HEAD"), runPullGit(t, w.repo, "rev-parse", "HEAD"); got != want {
				t.Fatalf("created from another repository: HEAD %q, want %q", got, want)
			}
			if b, err := os.ReadFile(filepath.Join(path, "app.txt")); err != nil || string(b) != "one\n" {
				t.Fatalf("requested repository contents: %q, %v", b, err)
			}
			if _, err := os.Stat(filepath.Join(path, "other.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("included the other repository: %v", err)
			}
			if len(w.f.trace("worktree.create")) != 0 || len(w.f.trace("workspace.create")) != 1 {
				t.Fatal("ambiguous workspace must use Git and open a plain workspace")
			}
			if branchOf(t, w.repo) != "main" || branchOf(t, other) != "main" {
				t.Fatal("changed a source checkout")
			}
		})
	}
}

func TestWorktreeCwdDoesNotUseAnAncestorWorkspaceSource(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		repository bool
		membership bool
	}{
		{name: "plain ancestor"},
		{name: "ancestor repository", repository: true},
		{name: "ancestor membership with a project pane", repository: true, membership: true},
	} {
		for _, automatic := range []bool{false, true} {
			t.Run(scenario.name+map[bool]string{false: "/explicit", true: "/automatic"}[automatic], func(t *testing.T) {
				w := worktreeNode(t)
				if scenario.repository {
					runPullGit(t, w.home, "init", "--initial-branch=main")
					if err := os.WriteFile(filepath.Join(w.home, ".bashrc"), []byte("dotfiles repository\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					runPullGit(t, w.home, "add", ".bashrc")
					runPullGit(t, w.home, "commit", "-m", "dotfiles")
				}
				workspace := map[string]any{"workspace_id": "w1", "label": "home", "number": 1}
				paneDir := w.home
				if scenario.membership {
					workspace["worktree"] = map[string]any{"repo_root": w.home, "checkout_path": w.home}
					paneDir = w.repo
				}
				panes := []any{map[string]any{"workspace_id": "w1", "tab_id": "w1:t1", "pane_id": "w1:p1", "cwd": paneDir}}
				w.f.reply("workspace.list", map[string]any{"workspaces": []any{workspace}})
				w.f.reply("pane.list", map[string]any{"panes": panes})
				w.f.reply("session.snapshot", snapshot([]any{workspace}, []any{map[string]any{"workspace_id": "w1", "tab_id": "w1:t1"}}, nil, panes))
				// herdr creates from the workspace's source, not from the requested nested repository.
				w.f.handle("worktree.create", func(p map[string]any) any {
					if !scenario.repository {
						return herdrFail{Code: "not_git_worktree", Message: "workspace is not a Git worktree"}
					}
					runPullGit(t, w.home, "worktree", "add", "-b", str(p["branch"]), "--", str(p["path"]))
					return map[string]any{
						"workspace": map[string]any{"workspace_id": "w2"},
						"tab":       map[string]any{"tab_id": "w2:t1"}, "root_pane": map[string]any{"pane_id": "w2:p1"},
						"worktree": map[string]any{"path": p["path"], "branch": p["branch"]},
					}
				})
				listSessions(t, w.n)
				w.f.reset()
				args := map[string]any{"cwd": w.repo, "branch": "feature/project", "path": filepath.Join(w.home, "project-checkout")}
				if automatic {
					args = map[string]any{"cwd": w.repo, "auto_name": true}
				}
				res, err := request(t, w.n, "worktree.create", args)
				if err != nil {
					t.Fatal(err)
				}
				path := str(res["path"])
				if got, want := runPullGit(t, path, "rev-parse", "HEAD"), runPullGit(t, w.repo, "rev-parse", "HEAD"); got != want {
					t.Fatalf("created the ancestor repository: HEAD %q, want project HEAD %q", got, want)
				}
				if b, err := os.ReadFile(filepath.Join(path, "app.txt")); err != nil || string(b) != "one\n" {
					t.Fatalf("project contents: %q, %v", b, err)
				}
				if _, err := os.Stat(filepath.Join(path, ".bashrc")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("included ancestor dotfiles: %v", err)
				}
				if len(w.f.trace("worktree.create")) != 0 || len(w.f.trace("workspace.create")) != 1 {
					t.Fatal("an ancestor source must use Git and open a plain workspace")
				}
				if branchOf(t, w.repo) != "main" {
					t.Fatal("changed the source checkout")
				}
			})
		}
	}
}

func TestWorktreeCwdInTheSameRepositoryKeepsNativeGrouping(t *testing.T) {
	w := worktreeNode(t)
	subdir := mkdir(t, filepath.Join(w.repo, "src"))
	w.f.reply("pane.list", map[string]any{"panes": []any{map[string]any{"workspace_id": "w1", "cwd": subdir}}})
	res, err := request(t, w.n, "worktree.create", map[string]any{
		"cwd": mkdir(t, filepath.Join(subdir, "nested")), "branch": "feature/grouped", "path": filepath.Join(w.home, "grouped"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res["workspace_id"] != "w2" || len(w.f.trace("worktree.create")) != 1 || len(w.f.trace("workspace.create")) != 0 {
		t.Fatalf("same-repository creation lost native grouping: %v", res)
	}
}

func TestSpaceHoldingPaneCandidates(t *testing.T) {
	w := worktreeNode(t)
	h, err := w.n.hostOfKind("")
	if err != nil {
		t.Fatal(err)
	}
	child := mkdir(t, filepath.Join(w.repo, "child"))
	alias := filepath.Join(w.home, "alias")
	if err := os.Symlink(w.repo, alias); err != nil {
		t.Fatal(err)
	}
	w.f.reply("workspace.list", map[string]any{"workspaces": []any{
		map[string]any{"workspace_id": "w1"}, map[string]any{"workspace_id": "w2"},
	}})
	w.f.handle("pane.list", func(p map[string]any) any {
		dir := w.repo
		if str(p["workspace_id"]) == "w2" {
			dir = child
		}
		return map[string]any{"panes": []any{map[string]any{"workspace_id": p["workspace_id"], "cwd": dir, "foreground_cwd": child}}}
	})
	got, err := spaceHolding(t.Context(), h, filepath.Join(alias, "child"))
	if err != nil || got != "w2" {
		t.Fatalf("deep canonical match: %q, %v", got, err)
	}
	if got, err := spaceHolding(t.Context(), h, w.repo); err != nil || got != "w1" {
		t.Fatalf("pane cwd must beat deeper foreground directory: %q, %v", got, err)
	}
	w.f.reply("pane.list", map[string]any{"panes": []any{map[string]any{"foreground_cwd": w.repo}}})
	if got, err := spaceHolding(t.Context(), h, w.repo); err != nil || got == "" {
		t.Fatalf("foreground fallback: %q, %v", got, err)
	}
	other := mkdir(t, filepath.Join(w.home, "foreground-other"))
	w.f.reply("pane.list", map[string]any{"panes": []any{map[string]any{"cwd": w.repo, "foreground_cwd": other}}})
	if got, err := spaceHolding(t.Context(), h, other); got != "" || !errors.Is(err, errNoHoldingSpace) {
		t.Fatalf("foreground directory must not override the workspace's stable source: %q, %v", got, err)
	}
	w.f.reply("pane.list", map[string]any{"panes": []any{}})
	w.f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w3", "worktree": map[string]any{"repo_root": w.repo, "checkout_path": child}}}})
	if got, err := spaceHolding(t.Context(), h, child); err != nil || got != "w3" {
		t.Fatalf("metadata: %q, %v", got, err)
	}
	if got, err := spaceHolding(t.Context(), h, w.repo); err != nil || got != "w3" {
		t.Fatalf("a parent workspace holds its repository: %q, %v", got, err)
	}
	w.f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w3", "worktree": map[string]any{"repo_root": w.repo, "checkout_path": child, "is_linked_worktree": true}}}})
	if got, err := spaceHolding(t.Context(), h, w.repo); got != "" || !errors.Is(err, errNoHoldingSpace) {
		t.Fatalf("a linked checkout must not hold its main repository: %q, %v", got, err)
	}
	if got, err := spaceHolding(t.Context(), h, child); err != nil || got != "w3" {
		t.Fatalf("a linked workspace still holds its own checkout: %q, %v", got, err)
	}
}

func TestWorktreeWithOnlyLinkedWorkspaceUsesGit(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit", true: "automatic"}[automatic], func(t *testing.T) {
			w := worktreeNode(t)
			linked := filepath.Join(w.home, "linked")
			runPullGit(t, w.repo, "worktree", "add", "-b", "feature/linked", linked)
			w.f.reply("workspace.list", map[string]any{"workspaces": []any{
				map[string]any{"workspace_id": "w2", "worktree": map[string]any{
					"repo_root": w.repo, "checkout_path": linked, "is_linked_worktree": true,
				}},
			}})
			w.f.reply("pane.list", map[string]any{"panes": []any{
				map[string]any{"workspace_id": "w2", "pane_id": "w2:p1", "cwd": linked},
			}})
			// herdr refuses a linked workspace as the source even when its repo_root matches.
			w.f.reply("worktree.create", herdrFail{Code: "linked_worktree_source", Message: "start from the repo parent workspace"})
			w.f.reset()
			prep, err := request(t, w.n, "worktree.prepare", map[string]any{"cwd": w.repo})
			if err != nil || prep["root"] != resolved(t, w.repo) {
				t.Fatalf("prepare the main repository: %v, %v", prep, err)
			}
			args := map[string]any{"cwd": w.repo, "branch": "feature/requested", "path": filepath.Join(w.home, "requested-checkout")}
			if automatic {
				args = map[string]any{"cwd": w.repo, "auto_name": true}
			}
			res, err := request(t, w.n, "worktree.create", args)
			if err != nil {
				t.Fatal(err)
			}
			path := str(res["path"])
			if got, want := runPullGit(t, path, "rev-parse", "HEAD"), runPullGit(t, w.repo, "rev-parse", "HEAD"); got != want {
				t.Fatalf("created HEAD %q, want %q", got, want)
			}
			if b, err := os.ReadFile(filepath.Join(path, "app.txt")); err != nil || string(b) != "one\n" {
				t.Fatalf("main repository contents: %q, %v", b, err)
			}
			if len(w.f.trace("worktree.create")) != 0 || len(w.f.trace("workspace.create")) != 1 {
				t.Fatal("a main repository with only a linked workspace must use Git and open a plain workspace")
			}
			if branchOf(t, w.repo) != "main" || branchOf(t, linked) != "feature/linked" {
				t.Fatal("changed a source checkout")
			}
		})
	}
}

func TestSpaceHoldingNamedSessionDirectories(t *testing.T) {
	h, fs := namedHerdr(t, nil)
	dir := t.TempDir()
	for _, f := range []*fakeHerdr{fs.own, fs.build, fs.review} {
		f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1"}}})
		f.reply("pane.list", map[string]any{"panes": []any{}})
	}
	fs.build.reply("pane.list", map[string]any{"panes": []any{map[string]any{"workspace_id": "w1", "cwd": dir}}})
	if got, err := spaceHolding(t.Context(), h, dir); err != nil || got != "@build:w1" {
		t.Fatalf("named lookup: %q, %v", got, err)
	}
}
