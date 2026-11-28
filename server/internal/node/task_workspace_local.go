package node

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	repository "github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func init() { nodeRPC(taskworkspace.LocalSourceMethod, (*Node).workspaceLocalSource) }

func (n *Node) workspaceLocalSource(ctx context.Context, raw json.RawMessage) (any, error) {
	var in taskworkspace.LocalSourceRequest
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	fail := func(code repository.LocalSourceError) (any, error) {
		return taskworkspace.LocalSource{Error: code}, nil
	}
	cwd, err := n.realPath(filepath.Clean(in.Cwd))
	if err != nil || n.offLimitsResolved(cwd) != nil {
		return fail(repository.LocalSourceInvalidPath)
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return fail(repository.LocalSourceInvalidPath)
	}
	hasCheckout, err := workspaceHasCheckoutMarker(cwd)
	if err != nil {
		return fail(repository.LocalSourceUnavailable)
	}
	if !hasCheckout {
		return taskworkspace.LocalSource{}, nil
	}
	top, err := workspaceReadGit(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return fail(repository.LocalSourceUnavailable)
	}
	common, err := workspaceReadGit(ctx, cwd, "rev-parse", "--git-common-dir")
	if err != nil || common == "" {
		return fail(repository.LocalSourceInvalid)
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(cwd, common)
	}
	common, err = n.realPath(filepath.Clean(common))
	if err != nil || !n.workspaceLocalCommonAllowed(common) {
		return fail(repository.LocalSourceInvalid)
	}
	local := &repository.LocalCheckout{CommonDir: common}
	branch, _ := workspaceReadGit(ctx, cwd, "symbolic-ref", "--quiet", "HEAD")
	if in.TaskBranch != "" {
		if _, err := repository.NormalizeRef(repository.RequestedRef{Kind: "branch", Value: in.TaskBranch}); err != nil {
			return fail(repository.LocalSourceInvalidRef)
		}
		ref := "refs/heads/" + in.TaskBranch
		local.Commit, err = workspaceBranchTip(ctx, common, ref)
		if err != nil {
			return fail(repository.LocalSourceUnavailable)
		}
		if local.Commit != "" {
			at, err := branchCheckedOutAt(ctx, cwd, in.TaskBranch)
			if err != nil {
				return fail(repository.LocalSourceUnavailable)
			}
			if at != "" {
				return taskworkspace.LocalSource{ExistingPath: n.visiblePath(at)}, nil
			}
			branch, local.ExistingBranch = ref, true
		}
	}
	if local.Commit == "" {
		local.Commit, err = workspaceBranchTip(ctx, cwd, "HEAD^{commit}")
		if err != nil {
			return fail(repository.LocalSourceUnavailable)
		}
		if local.Commit == "" {
			return fail(repository.LocalSourceNoCommits)
		}
	}
	ref := repository.RequestedRef{Kind: "commit", Value: local.Commit}
	if name, ok := strings.CutPrefix(branch, "refs/heads/"); ok {
		ref = repository.RequestedRef{Kind: "branch", Value: name}
	}
	name := filepath.Base(top)
	if filepath.Base(common) == ".git" {
		name = filepath.Base(filepath.Dir(common))
	}
	return taskworkspace.LocalSource{Repository: &repository.PreparationRepository{
		RepoID: "local", DirectoryAlias: repository.DefaultAlias(repository.Identity{Path: name}), RequestedRef: ref, Local: local,
	}}, nil
}

// The marker can be a directory or a pointer file. Git checks its validity when
// it exists, so an unreadable checkout cannot be treated as a plain folder.
func workspaceHasCheckoutMarker(cwd string) (bool, error) {
	for dir := cwd; ; {
		_, err := os.Lstat(filepath.Join(dir, ".git"))
		if !errors.Is(err, fs.ErrNotExist) {
			return err == nil, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false, nil
		}
		dir = parent
	}
}

func (n *Node) workspaceLocalCommonAllowed(common string) bool {
	state := n.dir
	if state == "" {
		state = filepath.Join(n.home, ".persona")
	}
	state, err := n.realPath(state)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if n.offLimitsResolved(common) != nil || workspacenaming.InPrivateState(common, state) || workspaceNoLinks(common) != nil {
		return false
	}
	for _, part := range []string{"config", "objects", "refs", "worktrees"} {
		if workspaceNoLinks(filepath.Join(common, part)) != nil {
			return false
		}
	}
	return true
}

func (e *workspaceExecutor) localSource(ctx context.Context, r *taskworkspace.Instance) string {
	local := r.Input.Local
	if !e.n.workspaceLocalCommonAllowed(r.CommonDir) || r.CommonDir != local.CommonDir ||
		workspacenaming.InPrivateState(r.Path, r.CommonDir) || workspacenaming.InPrivateState(r.CommonDir, r.Path) {
		return "source_invalid"
	}
	id, err := workspaceFileID(r.CommonDir)
	if err != nil || e.journal.LocalGitIdentity != "" && e.journal.LocalGitIdentity != id {
		return "identity_mismatch"
	}
	if gitDir, err := workspaceReadGit(ctx, r.CommonDir, "rev-parse", "--git-common-dir"); err != nil ||
		gitDir != "." && filepath.Clean(gitDir) != r.CommonDir {
		return "source_invalid"
	}
	if _, err := workspaceReadGit(ctx, r.CommonDir, "cat-file", "-e", local.Commit+"^{commit}"); err != nil {
		return "source_missing"
	}
	if r.ResolvedCommit == "" {
		e.journal.LocalGitIdentity = id
		r.ResolvedRef, r.ResolvedCommit, r.State = r.Input.RequestedRef.Value, local.Commit, "source_ready"
		if r.Input.RequestedRef.Kind == "branch" {
			r.ResolvedRef = "refs/heads/" + strings.TrimPrefix(r.ResolvedRef, "refs/heads/")
		}
		if err := e.save(); err != nil {
			return "persistence_failed"
		}
	}
	return ""
}

func (e *workspaceExecutor) sameGitDir(r *taskworkspace.Instance) bool {
	if r.Input.Local == nil {
		return e.owned(r.CommonDir)
	}
	if r.CommonDir != r.Input.Local.CommonDir || e.journal.LocalGitIdentity == "" || !e.n.workspaceLocalCommonAllowed(r.CommonDir) {
		return false
	}
	id, err := workspaceFileID(r.CommonDir)
	return err == nil && id == e.journal.LocalGitIdentity
}
