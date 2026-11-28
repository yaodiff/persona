package node

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
)

// A new Agent's own worktree on Orca (#17050): Orca makes it as a child of the worktree the folder is, Orca takes it
// away again, and an SSH target's worktrees get no local actions. A fake Orca runtime (orcaWorktreeNode's, whose
// worktree.create runs a real `git worktree add`) and a real temp repository; no test here talks to a real Orca.

// orcaListed makes the fake list the source as the project's main checkout and the others as its children, as
// Orca 1.4.277's worktree.ps does.
func (w *orcaWorktreeFixture) orcaListed(children ...string) {
	main := orcarttest.Worktree(w.src, w.repo, "refs/heads/dev")
	main["isMainWorktree"], main["hostId"] = true, "local"
	trees := []map[string]any{main}
	for _, path := range children {
		c := orcarttest.Worktree("repo1::"+path, path, "refs/heads/"+branchOfPath(path))
		c["isMainWorktree"], c["hostId"], c["parentWorktreeId"] = false, "local", w.src
		trees = append(trees, c)
	}
	w.o.SetWorktrees(trees...)
}

// branchOfPath is the branch the fake checked out at path (orcaWorktreeNode's worktree.create).
func branchOfPath(path string) string {
	out, err := git(context.Background(), path, "branch", "--show-current")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// worktree.add on Orca asks Orca for a child of the folder's worktree, on persona/<the agent's name> from the
// worktree's branch; the answer names the new worktree, and the first shell Orca opened in it is Persona's to close.
func TestOrcaAgentWorktreeIsOrcasChild(t *testing.T) {
	w := orcaWorktreeNode(t)
	ctx := context.Background()
	a, err := w.n.worktreeAdd(ctx, "", w.repo, "", "Fix the login crash!")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(w.dir, "persona-fix-the-login-crash")
	if a.Branch != "persona/fix-the-login-crash" || a.Path != path || a.WorkspaceID != "repo1::"+path || resolved(t, a.Root) != resolved(t, w.repo) {
		t.Fatalf("added %+v", a)
	}
	got := w.o.Params("worktree.create")
	if len(got) != 1 || got[0]["repo"] != "id:repo1" || got[0]["name"] != "persona-fix-the-login-crash" ||
		got[0]["branchNameOverride"] != "persona/fix-the-login-crash" || got[0]["baseBranch"] != "dev" ||
		got[0]["parentWorktree"] != "id:"+w.src || got[0]["activate"] != false {
		t.Fatalf("worktree.create %v", got)
	}
	if branchOf(t, a.Path) != a.Branch || runPullGit(t, a.Path, "rev-parse", "HEAD") != w.devHead {
		t.Fatal("not on its own branch from the worktree's HEAD")
	}
	w.n.mu.Lock()
	marked := w.n.agentRoots[host.PaneRef{Kind: host.Orca, ID: "tab-new:leaf-new"}.String()]
	w.n.mu.Unlock()
	if !marked {
		t.Fatal("the shell Orca opened in the worktree is not Persona's to close")
	}
	if inv := w.o.Invalid(); len(inv) != 0 {
		t.Fatalf("params Orca would refuse: %v", inv)
	}

	// the same name again: the next free one, again Orca's
	w.orcaListed(path)
	b, err := w.n.worktreeAdd(ctx, "", w.repo, "", "Fix the login crash")
	if err != nil || b.Branch != "persona/fix-the-login-crash-2" || w.o.Count("worktree.create") != 2 {
		t.Fatalf("again: %+v %v", b, err)
	}
	// a branch given is checked out as it is
	w.orcaListed(path, b.Path)
	c, err := w.n.worktreeAdd(ctx, "", w.repo, "spare", "")
	if err != nil || c.Branch != "spare" || branchOf(t, c.Path) != "spare" {
		t.Fatalf("an existing branch: %+v %v", c, err)
	}
}

// The Agent's start in its new worktree, by folder (a client that sends no workspace id) as by id, opens its tab there
// and closes the empty first shell Orca opened in it; a shell elsewhere stays.
func TestOrcaAgentTabReplacesTheWorktreesFirstShell(t *testing.T) {
	for _, byID := range []bool{false, true} {
		t.Run(map[bool]string{false: "by-folder", true: "by-id"}[byID], func(t *testing.T) {
			w := orcaWorktreeNode(t)
			ctx := context.Background()
			a, err := w.n.worktreeAdd(ctx, "", w.repo, "", "fix login")
			if err != nil {
				t.Fatal(err)
			}
			w.orcaListed(a.Path)
			w.o.SetTerminals(
				orcarttest.Terminal("term_new", "tab-new", "leaf-new", a.WorkspaceID, a.Path, "refs/heads/"+a.Branch, "zsh", ""),
				orcarttest.Terminal("term_src", "tab-src", "leaf-src", w.src, w.repo, "refs/heads/dev", "zsh", ""))
			if err := w.n.refreshHosts(ctx); err != nil {
				t.Fatal(err)
			}
			ws := ""
			if byID {
				ws = a.WorkspaceID
			}
			created, err := w.n.newTab(ctx, w.n.hosts.Get(host.Orca).(host.Launcher), a.Path, ws, "fix-login", "")
			if err != nil || created.SpaceID != a.WorkspaceID {
				t.Fatalf("the Agent's tab: %+v %v", created, err)
			}
			var closed []string
			for _, wr := range w.o.Writes() {
				if wr.Close {
					closed = append(closed, wr.Handle)
				}
			}
			if !slices.Equal(closed, []string{"term_new"}) {
				t.Fatalf("closed %v, want only the worktree's first shell", closed)
			}
		})
	}
}

// A folder Orca holds no worktree for (and may not register: a temporary folder), or an Orca that is not running,
// keeps the plain git worktree of before.
func TestOrcaAgentWorktreeFallsBackToGit(t *testing.T) {
	for _, name := range []string{"not-held", "not-running"} {
		t.Run(name, func(t *testing.T) {
			w := orcaWorktreeNode(t)
			w.o.SetWorktrees()
			if name == "not-running" {
				w.o.Stop()
			}
			a, err := w.n.worktreeAdd(context.Background(), "", w.repo, "", "by git")
			if err != nil || a.WorkspaceID != "" || a.Path != filepath.Join(w.dir, "persona-by-git") || branchOf(t, a.Path) != "persona/by-git" {
				t.Fatalf("git fallback: %+v %v", a, err)
			}
			if name == "not-held" && (w.o.Count("worktree.create") != 0 || w.o.Count("repo.add") != 0) {
				t.Fatal("Orca was asked to make or register it")
			}
		})
	}
}

// Closing the Agent: an Orca worktree is Persona's to remove wherever Orca's workspace folder is, and Orca removes it
// (worktree.rm), keeping the branch unless asked, after the usual guards; an Orca that cannot be told to keep the branch
// leaves it to git, as before.
func TestOrcaAgentWorktreeRemovedThroughOrca(t *testing.T) {
	w := orcaWorktreeNode(t)
	ctx := context.Background()
	w.dir = filepath.Join(w.home, "elsewhere", "proj") // Orca's workspace folder is not its default
	a, err := w.n.worktreeAdd(ctx, "", w.repo, "", "remove me")
	if err != nil || filepath.Dir(a.Path) != w.dir {
		t.Fatalf("added %+v %v", a, err)
	}
	w.orcaListed(a.Path)
	st, err := w.n.worktreeStatus(ctx, "", a.Path)
	if err != nil || !st.Worktree || !st.Ours || st.Branch != a.Branch {
		t.Fatalf("status of Orca's worktree in its own folder: %+v %v", st, err)
	}
	if st, _ := w.n.worktreeStatus(ctx, "", w.repo); st.Worktree || st.Ours {
		t.Fatalf("the main checkout: %+v", st)
	}
	var removed []map[string]any
	w.o.Handle("worktree.rm", func(p map[string]any) any {
		removed = append(removed, p)
		if out, err := gitRun(t.Context(), w.repo, gitOpTimeout, "worktree", "remove", "--", a.Path); err != nil {
			return orcarttest.Fail{Code: "git: " + out}
		}
		w.orcaListed()
		return map[string]any{"removed": true}
	})
	w.o.SetStatus(map[string]any{"capabilities": []string{"terminal.prompt-delivery.v1", orcaRemoveBranchPolicy}})

	// a change: refused before Orca is asked
	if err := os.WriteFile(filepath.Join(a.Path, "scratch.txt"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.n.worktreeRemove(ctx, "", a.Path, false, false); codeOf(err) != "worktree_dirty" || len(removed) != 0 {
		t.Fatalf("with a change: %v (%d removals)", err, len(removed))
	}
	if err := os.Remove(filepath.Join(a.Path, "scratch.txt")); err != nil {
		t.Fatal(err)
	}
	// an agent still in it
	w.n.mu.Lock()
	w.n.agents["live"] = host.Agent{Agent: "claude", Cwd: a.Path, AgentStatus: proto.StatusWorking}
	w.n.mu.Unlock()
	if _, err := w.n.worktreeRemove(ctx, "", a.Path, false, false); codeOf(err) != "worktree_in_use" || len(removed) != 0 {
		t.Fatalf("in use: %v", err)
	}
	w.n.mu.Lock()
	delete(w.n.agents, "live")
	w.n.mu.Unlock()

	r, err := w.n.worktreeRemove(ctx, "", a.Path, false, false)
	if err != nil || !r.Removed || r.BranchDeleted || len(removed) != 1 {
		t.Fatalf("remove: %+v %v (%d removals)", r, err, len(removed))
	}
	p := removed[0]
	if p["worktree"] != "id:repo1::"+a.Path || p["hostId"] != "local" || p["deleteBranch"] != false || p["force"] != false ||
		p["runHooks"] != true || p["waitForBackgroundRemoval"] != true {
		t.Fatalf("worktree.rm %v", p)
	}
	if _, err := os.Stat(a.Path); !os.IsNotExist(err) || !branchExists(ctx, w.repo, a.Branch) {
		t.Fatalf("after: %v, branch kept %v", err, branchExists(ctx, w.repo, a.Branch))
	}
	if inv := w.o.Invalid(); len(inv) != 0 {
		t.Fatalf("params Orca would refuse: %v", inv)
	}

	// an older Orca: git removes it, Orca is not asked
	w.o.SetStatus(map[string]any{"capabilities": []string{"terminal.prompt-delivery.v1"}})
	b, err := w.n.worktreeAdd(ctx, "", w.repo, "", "older orca")
	if err != nil {
		t.Fatal(err)
	}
	w.orcaListed(b.Path)
	if r, err := w.n.worktreeRemove(ctx, "", b.Path, false, false); err != nil || !r.Removed || len(removed) != 1 {
		t.Fatalf("older Orca: %+v %v (%d removals)", r, err, len(removed))
	}
}

// A worktree on one of Orca's SSH targets is that computer's: it is neither a space of this one's layout nor in its
// workspace catalog, so nothing offers a local action on it.
func TestOrcaLeavesSSHTargetWorktreesOut(t *testing.T) {
	n, _, o, _, _ := orcaNode(t)
	local := orcarttest.Worktree("repo::"+orcaCwd, orcaCwd, "refs/heads/feature-x")
	local["hostId"] = "local"
	remote := orcarttest.Worktree("repo::/srv/remote", "/srv/remote", "refs/heads/main")
	remote["hostId"] = "ssh-test-host"
	o.SetWorktrees(local, remote)
	h := n.hosts.Get(host.Orca)
	l, err := h.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := func(spaces []host.Space) []string {
		var out []string
		for _, s := range spaces {
			out = append(out, s.ID)
		}
		return out
	}
	if got := ids(l.Spaces); !slices.Equal(got, []string{"repo::" + orcaCwd}) {
		t.Fatalf("layout spaces %v", got)
	}
	spaces, err := h.(host.SpaceLister).Spaces(context.Background())
	if err != nil || !slices.Equal(ids(spaces), []string{"repo::" + orcaCwd}) {
		t.Fatalf("catalog spaces %v %v", ids(spaces), err)
	}
}
