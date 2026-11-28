package node

// A new Agent's own worktree on Orca (#6115, #17050; docs/integrations/terminal.md "A new agent in a worktree"): Orca
// makes it, as a child of the Orca worktree the Agent's folder is (worktree.create with parentWorktree), so Orca's
// sidebar lists it under that one and the Agent's start opens in it by its id; and Orca takes it away again
// (worktree.rm), so its sidebar learns it is gone and its archive hooks run. A plain `git worktree add` in Orca's
// folder was a checkout Orca did not know: the start registered it as a project of its own.

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/proto"
)

// orcaRemoveBranchPolicy is the status.get capability of an Orca whose worktree.rm keeps the branch unless told to
// delete it (deleteBranch). Without it worktree.remove leaves the checkout to git, as before.
const orcaRemoveBranchPolicy = "worktree.remove-branch-policy.v1"

// orcaAgentWorktreeAdd is worktree.add on Orca: the Agent's worktree (worktreeAdd's branch and folder names) made by
// Orca as a child of the Orca worktree that is repo's checkout (agentWorktreeSource), its id the answer's
// workspace_id. The first shell Orca opens in it is Persona's (markWorktreeShell): the Agent's start closes it once
// its own tab is open there. ok is false when Orca does not run or holds no worktree there: worktreeAdd's git way, as
// before.
func (n *Node) orcaAgentWorktreeAdd(ctx context.Context, o *orcaHost, repo worktreeRepo, branch, nameHint string) (proto.WorktreeAdded, bool, error) {
	var res proto.WorktreeAdded
	src, err := o.agentWorktreeSource(ctx, repo)
	if errors.Is(err, host.ErrNotRunning) || err == nil && src == nil {
		return res, false, nil
	}
	if err != nil {
		return res, true, refusal("worktree_failed", gitFailure(err), nil)
	}
	base := n.worktreesDir(o, repo.name)
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
		if len(candidates) > 1 && (branchExists(ctx, repo.root, b) || pathTaken(dest)) {
			continue // the agent's own name is taken: the next
		}
		if err := checkBranch(ctx, repo.root, b); err != nil {
			return res, true, err
		}
		created, err := o.CreateWorktreeUnder(ctx, src.WorktreeID, b, dest, src.WorktreeID)
		if err != nil {
			return res, true, refusal("worktree_failed", gitFailure(err), nil)
		}
		n.portableWorktreeLinks(created.Path)
		n.markWorktreeShell(withAgentWorktree(ctx, true), o, created)
		_ = n.refreshHosts(ctx, o) // the new worktree is a workspace the start finds
		return proto.WorktreeAdded{Path: created.Path, Branch: created.Branch, Root: repo.root, WorkspaceID: created.SpaceID}, true, nil
	}
	return res, true, noAgentBranch(ctx)
}

// agentWorktreeSource is the local Orca git worktree whose checkout is repo's (its top level): a new Agent's worktree
// is made from it and under it. A main checkout Orca does not hold yet is registered first (repo.add), under the rules
// a start in it would be (resolveWorktree: not for a backend launch, not the home, the root or a temporary folder).
// nil: Orca holds no worktree there (a linked checkout it does not know, or a folder it may not register).
func (h *orcaHost) agentWorktreeSource(ctx context.Context, repo worktreeRepo) (*orcart.Worktree, error) {
	find := func() (*orcart.Worktree, error) {
		trees, _, _, err := h.c.Worktrees(ctx, "")
		if err != nil {
			return nil, orcaErr(err)
		}
		return orcaGitWorktreeAt(trees, repo.root), nil
	}
	w, err := find()
	if err != nil || w != nil || repo.mainRoot != "" || backendLaunchFrom(ctx) != nil || !h.canAutoRegister(repo.root) {
		return w, err
	}
	if err := h.c.RepoAdd(ctx, repo.root, "git"); err != nil {
		return nil, orcaErr(err)
	}
	return find()
}

// orcaGitWorktreeAt is the local git worktree of trees whose folder is path (spelled through a link or not), nil for
// none: its id is Orca's "<repo id>::<path>". A folder workspace has no repository for worktree.create, a
// directory-level workspace in a checkout (orcaDirectoryWorkspace's "…::workspace:<uuid>") is not the checkout, and
// one on an SSH target is that machine's.
func orcaGitWorktreeAt(trees []orcart.Worktree, path string) *orcart.Worktree {
	want := realPath(filepath.Clean(path))
	for i := range trees {
		w := &trees[i]
		if !localComposerWorkspace(*w) || folderComposerWorkspace(*w) || w.Path == "" || w.Removing ||
			!strings.HasSuffix(w.WorktreeID, "::"+w.Path) {
			continue
		}
		if realPath(filepath.Clean(w.Path)) == want {
			return w
		}
	}
	return nil
}

// agentWorktreeAt is the Orca worktree a closed Agent's checkout at path is, when it is one Persona may take away
// through Orca: local, a linked worktree (not the project's main checkout), wherever Orca's workspace folder is. nil
// when Orca does not list it (or cannot be asked): worktreeStatus then judges by the checkouts folder alone, as before.
func (h *orcaHost) agentWorktreeAt(ctx context.Context, path string) *orcart.Worktree {
	trees, _, _, err := h.c.Worktrees(ctx, "")
	if err != nil {
		return nil
	}
	if w := orcaGitWorktreeAt(trees, path); w != nil && w.IsMain != nil && !*w.IsMain {
		return w
	}
	return nil
}

// orcaAgentWorktreeRemove is worktree.remove's last step for an Orca worktree (agentWorktreeAt), after its guards:
// worktree.rm, keeping the branch unless deleteBranch, forcing only what the user confirmed, with Orca's archive hooks.
// done false: this Orca cannot be told to keep the branch (orcaRemoveBranchPolicy), and git removes it as before.
func (n *Node) orcaAgentWorktreeRemove(ctx context.Context, o *orcaHost, w *orcart.Worktree, deleteBranch, force bool) (proto.WorktreeRemoved, bool, error) {
	var res proto.WorktreeRemoved
	st, err := o.c.Probe(ctx)
	if err != nil || st == nil || !slices.Contains(st.Capabilities, orcaRemoveBranchPolicy) {
		return res, false, nil
	}
	if len(w.ChildWorktreeIDs) > 0 {
		return res, true, refusal("worktree_failed", composerError(ctx, "children").Error(), nil)
	}
	var result struct {
		Removed       bool `json:"removed"`
		Removing      bool `json:"removing"`
		BranchDeleted bool `json:"branchDeleted"`
	}
	err = o.c.Call(ctx, 2*time.Minute, "worktree.rm", map[string]any{"worktree": "id:" + w.WorktreeID, "hostId": "local",
		"deleteBranch": deleteBranch, "force": force, "runHooks": true, "waitForBackgroundRemoval": true}, &result)
	if err != nil {
		return res, true, refusal("worktree_failed", orcaErr(err).Error(), nil)
	}
	_ = n.refreshHosts(ctx, o) // the worktree leaves the workspaces
	if !result.Removed {
		return res, true, refusal("worktree_failed", composerError(ctx, "deleting").Error(), nil)
	}
	res.Removed, res.BranchDeleted = true, deleteBranch && result.BranchDeleted
	return res, true, nil
}
