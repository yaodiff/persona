package node

// A new agent in a Git worktree of its own (#6115, docs/integrations/terminal.md "A new agent in a worktree"): worktree.add makes one
// of the repository a folder is in, under the directory the computer already puts this repository's checkouts in (#2431,
// worktreesDir), on a branch of its own (persona/<the agent's name>), and opens nothing: the agent's start opens its
// workspace. worktree.status tells whether a folder is such a worktree and what removing it would lose; worktree.remove
// takes one of them away when the agent is closed and the user says so. Nothing is forced unless the user confirmed it
// (force): uncommitted changes, or a branch with commits the main checkout's branch lacks, are refusals first.

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/proto"
)

// agentBranchPrefix is the branch prefix of a new agent's worktree.
const agentBranchPrefix = "persona/"

// maxSlug bounds the part of a branch taken from the agent's name.
const maxSlug = 40

// agentBranch is the branch a new agent's worktree gets from its name: persona/<slug>, "agent" when the name has no
// ASCII letters or digits.
func agentBranch(nameHint string) string {
	slug := herdr.WorktreeSlug(nameHint)
	if len(slug) > maxSlug {
		slug = strings.TrimRight(slug[:maxSlug], "-")
	}
	if slug == "" {
		slug = "agent"
	}
	return agentBranchPrefix + slug
}

// worktreeAdd makes a worktree of the repository cwd is in, for a new agent. An empty branch is the agent's own
// (agentBranch, made unique: -2, -3… while the branch or its folder is taken); a branch given is used as it is: an
// existing one checked out, a new one made from the repository's HEAD.
func (n *Node) worktreeAdd(ctx context.Context, kind, cwd, branch, nameHint string) (proto.WorktreeAdded, error) {
	var res proto.WorktreeAdded
	// On an engine runtime (Paseo) the runtime makes it, as a workspace of its own its start then finds
	// (runtimeworkspaces.go): a bare git worktree would be a folder it files under a project of its own.
	if rt, runtime, err := n.worktreeRuntime(ctx, kind); runtime {
		if err != nil {
			return res, refusal("worktree_failed", err.Error(), nil)
		}
		return n.runtimeWorktreeAdd(ctx, rt, cwd, branch, nameHint)
	}
	dir := proto.ExpandHome(strings.TrimSpace(cwd), n.home)
	if dir == "" || !filepath.IsAbs(dir) {
		return res, refusal("not_a_repository", "the folder is not in a Git repository", nil)
	}
	repo, err := repoAt(ctx, dir)
	if err != nil {
		return res, refusal("not_a_repository", "the folder is not in a Git repository", nil)
	}
	h, _ := n.hostOfKind(kind) // the host's checkouts directory; without one, herdr's
	base := n.worktreesDir(h, repo.name)
	branch = strings.TrimSpace(branch)
	candidates := []string{branch}
	if branch == "" {
		own := agentBranch(nameHint)
		candidates = []string{own}
		for i := 2; i <= 50; i++ {
			candidates = append(candidates, own+"-"+strconv.Itoa(i))
		}
	}
	for _, b := range candidates {
		dest := filepath.Join(base, herdr.WorktreeDirName(b))
		exists := branchExists(ctx, repo.root, b)
		if len(candidates) > 1 && (exists || pathTaken(dest)) {
			continue // the agent's own name is taken: the next
		}
		if err := checkBranch(ctx, repo.root, b); err != nil {
			return res, err
		}
		if dest, err = n.checkoutPath(repo.root, dest); err != nil {
			return res, err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return res, err
		}
		args := []string{"worktree", "add", "-b", b, "--", dest}
		if exists {
			args = []string{"worktree", "add", "--", dest, b}
		}
		if _, err := gitTimed(ctx, repo.root, 2*time.Minute, args...); err != nil {
			return res, refusal("worktree_failed", gitFailure(err), nil)
		}
		return proto.WorktreeAdded{Path: dest, Branch: b, Root: repo.root}, nil
	}
	return res, noAgentBranch(ctx)
}

// noAgentBranch is the refusal when the agent's own branch name and the next 49 are all taken.
func noAgentBranch(ctx context.Context) error {
	return refusal("worktree_failed", composerError(ctx, "agent_branch_exhausted").Error(), nil)
}

func branchExists(ctx context.Context, root, branch string) bool {
	_, err := git(ctx, root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// pathTaken: something is there (an empty directory is not something).
func pathTaken(p string) bool {
	entries, err := os.ReadDir(p)
	if err != nil {
		_, statErr := os.Stat(p)
		return statErr == nil // a file
	}
	return len(entries) > 0
}

// worktreeStatus says what cwd is: a linked worktree or not, Persona's or not, and what removing it would lose; and,
// for any folder in a repository, where worktree.add would put a new agent's worktree. A folder outside any repository
// is simply not a worktree.
func (n *Node) worktreeStatus(ctx context.Context, kind, cwd string) (proto.WorktreeStatus, error) {
	var st proto.WorktreeStatus
	dir := proto.ExpandHome(strings.TrimSpace(cwd), n.home)
	if dir == "" || !filepath.IsAbs(dir) {
		return st, nil
	}
	top, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(top) == "" {
		return st, nil
	}
	st.Path = strings.TrimSpace(top)
	repo, err := repoAt(ctx, st.Path)
	if err != nil {
		return st, nil
	}
	st.Branch = repo.branch
	rt, runtime, _ := n.worktreeRuntime(ctx, kind)
	if runtime {
		st.Checkouts = n.runtimeCheckoutsOf(ctx, rt, st.Path) // the runtime's folder ("" when it cannot say)
	} else {
		h, _ := n.hostOfKind(kind)
		st.Checkouts = n.worktreesDir(h, repo.name)
	}
	gitDir, err1 := absGit(ctx, st.Path, "--git-dir")
	common, err2 := absGit(ctx, st.Path, "--git-common-dir")
	if err1 != nil || err2 != nil || gitDir == common {
		return st, nil // the main checkout, not a linked worktree
	}
	st.Worktree = true
	// A runtime's worktree is its workspace too: Persona's to remove only when it is the folder of a worktree workspace
	// the runtime made, which worktree.remove then has the runtime archive (runtimeAgentWorktreeRemove); removing only
	// the checkout would leave the runtime a workspace in a folder that is gone.
	if runtime {
		_, st.Ours = rt.ownedWorktreeAt(st.Path)
	} else {
		st.Ours = within(realPath(st.Path), realPath(st.Checkouts))
	}
	if out, err := git(ctx, st.Path, "status", "--porcelain"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if strings.TrimSpace(line) != "" {
				st.Dirty++
			}
		}
	}
	// the main checkout: the first worktree git lists
	if out, err := git(ctx, st.Path, "worktree", "list", "--porcelain"); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if p, ok := strings.CutPrefix(line, "worktree "); ok {
				st.Root = strings.TrimSpace(p)
				break
			}
		}
	}
	if st.Root != "" {
		if b, err := git(ctx, st.Root, "branch", "--show-current"); err == nil {
			st.MainBranch = strings.TrimSpace(b)
		}
	}
	if st.Branch != "" && st.MainBranch != "" && st.Branch != st.MainBranch {
		if out, err := git(ctx, st.Path, "rev-list", "--count", st.MainBranch+".."+st.Branch); err == nil {
			st.Unmerged, _ = strconv.Atoi(strings.TrimSpace(out))
		}
	}
	return st, nil
}

// absGit is a rev-parse path option's answer as an absolute, clean path (git before 2.31 has no --path-format).
func absGit(ctx context.Context, dir, option string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", option)
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(out)
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return realPath(filepath.Clean(p)), nil
}

// worktreeRemove takes away one of Persona's worktrees, and its branch when asked. Refused: a folder that is not
// one of them (the worktree's own folder, not one inside it), one an agent still runs in, uncommitted changes, and
// deleting a branch with commits the main checkout's branch lacks, unless the user confirmed it (force).
func (n *Node) worktreeRemove(ctx context.Context, kind, path string, deleteBranch, force bool) (proto.WorktreeRemoved, error) {
	var res proto.WorktreeRemoved
	if _, runtime, _ := n.worktreeRuntime(ctx, kind); runtime { // the checks and the runtime's archive, inside the hub's 90 s
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, runtimeRemoveDeadline)
		defer cancel()
	}
	p := proto.ExpandHome(strings.TrimSpace(path), n.home)
	st, err := n.worktreeStatus(ctx, kind, p)
	if err != nil {
		return res, err
	}
	if !st.Worktree || !st.Ours || st.Root == "" || realPath(filepath.Clean(p)) != realPath(st.Path) {
		return res, refusal("not_our_worktree", "this folder is not a worktree Persona made", nil)
	}
	if n.agentRunsIn(st.Path) {
		return res, refusal("worktree_in_use", "an agent still runs in this worktree", nil)
	}
	if st.Dirty > 0 && !force {
		return res, refusal("worktree_dirty", "the worktree has uncommitted changes", map[string]any{"dirty": st.Dirty})
	}
	if deleteBranch && st.Unmerged > 0 && !force {
		return res, refusal("worktree_unmerged", "the branch has commits that are not on the main branch",
			map[string]any{"unmerged": st.Unmerged, "main_branch": st.MainBranch})
	}
	if rt, runtime, _ := n.worktreeRuntime(ctx, kind); runtime { // ours: a worktree workspace the runtime made
		return n.runtimeAgentWorktreeRemove(ctx, rt, st, deleteBranch, force)
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	if _, err := gitTimed(ctx, st.Root, time.Minute, append(args, "--", st.Path)...); err != nil {
		return res, refusal("worktree_failed", gitFailure(err), nil)
	}
	res.Removed = true
	if deleteBranch && st.Branch != "" {
		flag := "-d"
		if force {
			flag = "-D"
		}
		if _, err := git(ctx, st.Root, "branch", flag, st.Branch); err != nil {
			return res, refusal("worktree_failed", gitFailure(err), map[string]any{"removed": true})
		}
		res.BranchDeleted = true
	}
	return res, nil
}

// agentRunsIn: a live agent's folder is dir or inside it.
func (n *Node) agentRunsIn(dir string) bool {
	d := realPath(filepath.Clean(dir))
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, a := range n.agents {
		if a.Cwd != "" && within(realPath(filepath.Clean(a.Cwd)), d) && a.AgentStatus != proto.StatusOffline {
			return true
		}
	}
	return false
}
