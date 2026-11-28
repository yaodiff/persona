package node

import (
	"cmp"
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode"

	repository "github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
)

func (e *workspaceExecutor) sourceFetchTimeout(transport string, createdCommonDir bool) time.Duration {
	if createdCommonDir && remoteFetchKey(transport) != "" {
		return cmp.Or(e.coldFetchTimeout, taskworkspace.ColdFetchTimeout)
	}
	return taskworkspace.GitTimeout
}

// sourceGit takes any network slot before starting the Git command timeout.
func (e *workspaceExecutor) sourceGit(ctx context.Context, transport string, timeout time.Duration, dir string, args ...string) (string, error) {
	return e.n.remoteFetches.run(ctx, transport, func(ctx context.Context) (string, error) {
		return e.gitWithTimeout(ctx, timeout, dir, args...)
	})
}

func workspaceFetchErrorCode(err error, fallback string) string {
	var timeout interface{ Timeout() bool }
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return "source_fetch_timeout"
	}
	return fallback
}

func (e *workspaceExecutor) initPrivateSource(ctx context.Context, r *taskworkspace.Instance, transport, advertised string) (created bool, code string) {
	if !e.owned(r.CommonDir) {
		if err := e.claimDir(r.CommonDir); err != nil {
			return false, "path_conflict"
		}
		created = true
	}
	for _, name := range []string{"config", "objects", "refs"} {
		if workspaceNoLinks(filepath.Join(r.CommonDir, name)) != nil {
			return created, "git_layout_mismatch"
		}
	}
	objectFormat := "sha1"
	if len(advertised) == 64 {
		objectFormat = "sha256"
	}
	if _, err := e.git(ctx, r.CommonDir, "init", "--bare", "--object-format="+objectFormat, "."); err != nil {
		return created, "source_failed"
	}
	// Use the upstream URL even when the initial objects come from a mirror.
	if _, err := e.git(ctx, r.CommonDir, "config", "remote.origin.url", transport); err != nil {
		return created, "source_failed"
	}
	return created, ""
}

// The caller holds the workspace locks and created this directory on this
// attempt. The receipt and any registered worktrees keep their own lifetimes.
func (e *workspaceExecutor) removeIncompleteCommonDir(r *taskworkspace.Instance) {
	if r.State != "pending" || r.ResolvedCommit != "" || r.Input.Local != nil || r.CommonDir != e.privateCommonDir(r.RepoID) {
		return
	}
	info, err := os.Lstat(r.CommonDir)
	if err != nil || !e.owned(r.CommonDir) {
		return
	}
	parent, err := os.OpenRoot(filepath.Dir(r.CommonDir))
	if err != nil {
		return
	}
	defer parent.Close()
	name := filepath.Base(r.CommonDir)
	current, err := parent.Lstat(name)
	if err != nil || !os.SameFile(info, current) {
		return
	}
	if _, err := parent.Lstat(filepath.Join(name, "worktrees")); !errors.Is(err, os.ErrNotExist) {
		return
	}
	if err := parent.RemoveAll(name); err == nil {
		delete(e.journal.Identities, r.CommonDir)
	}
}

func (e *workspaceExecutor) source(ctx context.Context, r *taskworkspace.Instance) string {
	if r.Input.Local != nil {
		return e.localSource(ctx, r)
	}
	if r.ResolvedCommit != "" {
		if !e.owned(r.CommonDir) {
			return "identity_mismatch"
		}
		if _, err := workspaceReadGit(ctx, r.CommonDir, "cat-file", "-e", r.ResolvedCommit+"^{commit}"); err != nil {
			return "source_missing"
		}
		return ""
	}
	if !e.n.workspaceSourceAllowed(r.Input) {
		return "source_invalid"
	}
	transport := r.Input.SourceSnapshot.CloneURL
	ref, err := repository.NormalizeRef(r.Input.RequestedRef)
	if err != nil {
		return "ref_invalid"
	}
	if slices.Contains(e.mirrorRepos, r.RepoID) {
		if used, code := e.sourceFromMirror(ctx, r, transport, ref); used {
			return code
		}
	}
	// Resolve against what the source advertises now. An earlier failed
	// preparation can leave tracking branches and tags in this private store.
	requested := ref.Value
	switch ref.Kind {
	case "default", "commit":
		requested = "HEAD"
	case "branch":
		requested = "refs/heads/" + strings.TrimPrefix(requested, "refs/heads/")
	case "tag":
		requested = "refs/tags/" + strings.TrimPrefix(requested, "refs/tags/")
	}
	listing, err := e.sourceGit(ctx, transport, taskworkspace.GitTimeout, e.journal.Plan.RootPath, "ls-remote", "--symref", "--", transport, requested)
	if err != nil {
		return workspaceFetchErrorCode(err, "source_unavailable")
	}
	resolvedRef := requested
	var advertised string
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == requested {
			advertised = fields[0]
		}
		if ref.Kind == "default" && len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD" && strings.HasPrefix(fields[1], "refs/heads/") {
			resolvedRef = fields[1]
		}
	}
	if ref.Kind == "default" && resolvedRef == "HEAD" {
		if listing == "" {
			heads, err := e.sourceGit(ctx, transport, taskworkspace.GitTimeout, e.journal.Plan.RootPath, "ls-remote", "--heads", "--", transport)
			if err != nil {
				return workspaceFetchErrorCode(err, "source_unavailable")
			}
			if heads == "" {
				return "no_commits"
			}
		}
		return "default_branch_unresolved"
	}
	if ref.Kind == "commit" {
		resolvedRef, advertised = ref.Value, ref.Value
	}
	if advertised == "" {
		return "ref_not_found"
	}
	createdCommonDir, code := e.initPrivateSource(ctx, r, transport, advertised)
	if createdCommonDir {
		defer e.removeIncompleteCommonDir(r)
	}
	if code != "" {
		return code
	}
	// Fetch the selected ref explicitly; FETCH_HEAD then identifies its actual
	// remote object even if other refs in this store are stale.
	timeout := e.sourceFetchTimeout(transport, createdCommonDir)
	if _, err = e.sourceGit(ctx, transport, timeout, r.CommonDir, "fetch", "--no-recurse-submodules", "--tags", "origin", resolvedRef, "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		if ref.Kind == "commit" {
			return workspaceFetchErrorCode(err, "ref_not_found")
		}
		return workspaceFetchErrorCode(err, "source_fetch_failed")
	}
	r.ResolvedRef = resolvedRef
	commit, err := workspaceReadGit(ctx, r.CommonDir, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	if err != nil {
		return "ref_not_found"
	}
	normalized, err := repository.NormalizeRef(repository.RequestedRef{Kind: "commit", Value: commit})
	if err != nil {
		return "source_failed"
	}
	// Pin the actual object, independently of later remote movement and GC.
	if _, err = e.git(ctx, r.CommonDir, "update-ref", "refs/persona/base", normalized.Value); err != nil {
		return "source_failed"
	}
	r.ResolvedCommit = normalized.Value
	r.State = "source_ready"
	if err = e.save(); err != nil {
		return "persistence_failed"
	}
	return ""
}

// sourceFromMirror uses a managed bare mirror (~/.persona/repos/<repo_id>.git) to
// populate the workspace's self-contained private bare repository locally (no objects/info/alternates,
// keeping remote.origin.url pointing at the real upstream transport). Returns used=false whenever
// the mirror is unavailable, mismatched, or lacks the requested ref so the caller falls back cleanly
// to direct remote resolution.
func (e *workspaceExecutor) sourceFromMirror(ctx context.Context, r *taskworkspace.Instance, transport string, ref repository.RequestedRef) (used bool, code string) {
	if !repository.ValidateMirrorRepoID(r.RepoID) {
		return false, ""
	}
	// Guard against reusing a mirror that belongs to a different repository identity.
	if _, _, metaPath, _, _, err := e.n.repoMirrorPaths(r.RepoID); err == nil {
		if existingMeta, ok := readRepoMirrorMeta(metaPath); ok && existingMeta.Identity != (repository.Identity{}) && existingMeta.Identity != r.Input.SourceSnapshot.Identity {
			return false, ""
		}
	}
	ensureReq := repository.MirrorEnsureRequest{
		RepoID:     r.RepoID,
		Identity:   r.Input.SourceSnapshot.Identity,
		CloneURL:   transport,
		TTLSeconds: e.mirrorTTLSeconds,
	}
	info, err := e.n.ensureRepoMirror(ctx, ensureReq)
	var problem *taskworkspace.Problem
	if errors.As(err, &problem) {
		return true, problem.Code
	}
	if err != nil || !info.Exists || info.Path == "" {
		return false, ""
	}
	resolvedRef, advertised, ok := resolveRefInMirror(ctx, info.Path, ref)
	if !ok && !info.Fetched {
		// Mirror skipped fetch due to TTL; force-refresh once in case the requested ref was pushed recently.
		ensureReq.Force = true
		info, err = e.n.ensureRepoMirror(ctx, ensureReq)
		if errors.As(err, &problem) {
			return true, problem.Code
		}
		if err != nil || !info.Exists || info.Path == "" {
			return false, ""
		}
		resolvedRef, advertised, ok = resolveRefInMirror(ctx, info.Path, ref)
	}
	if !ok || advertised == "" {
		return false, ""
	}
	createdCommonDir, code := e.initPrivateSource(ctx, r, transport, advertised)
	if createdCommonDir {
		defer e.removeIncompleteCommonDir(r)
	}
	if code != "" {
		return true, code
	}
	var commit string
	if ref.Kind == "commit" {
		if _, err := e.git(ctx, r.CommonDir, "fetch", "--no-recurse-submodules", "--tags", info.Path, "+refs/heads/*:refs/remotes/origin/*"); err != nil {
			return true, workspaceFetchErrorCode(err, "source_fetch_failed")
		}
		commit, err = workspaceReadGit(ctx, r.CommonDir, "rev-parse", "--verify", advertised+"^{commit}")
		if err != nil {
			if _, fetchErr := e.git(ctx, r.CommonDir, "fetch", "--no-recurse-submodules", info.Path, advertised); fetchErr != nil {
				return true, workspaceFetchErrorCode(fetchErr, "ref_not_found")
			}
			commit, err = workspaceReadGit(ctx, r.CommonDir, "rev-parse", "--verify", advertised+"^{commit}")
			if err != nil {
				return true, "ref_not_found"
			}
		}
	} else {
		if _, err := e.git(ctx, r.CommonDir, "fetch", "--no-recurse-submodules", "--tags", info.Path, resolvedRef, "+refs/heads/*:refs/remotes/origin/*"); err != nil {
			return true, workspaceFetchErrorCode(err, "source_fetch_failed")
		}
		commit, err = workspaceReadGit(ctx, r.CommonDir, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
		if err != nil {
			return true, "ref_not_found"
		}
	}
	r.ResolvedRef = resolvedRef
	normalized, err := repository.NormalizeRef(repository.RequestedRef{Kind: "commit", Value: commit})
	if err != nil {
		return true, "source_failed"
	}
	if _, err := e.git(ctx, r.CommonDir, "update-ref", "refs/persona/base", normalized.Value); err != nil {
		return true, "source_failed"
	}
	r.ResolvedCommit = normalized.Value
	r.State = "source_ready"
	if err := e.save(); err != nil {
		return true, "persistence_failed"
	}
	return true, ""
}

func resolveRefInMirror(ctx context.Context, mirrorDir string, ref repository.RequestedRef) (resolvedRef, commit string, ok bool) {
	switch ref.Kind {
	case "default":
		sym, err := workspaceReadGit(ctx, mirrorDir, "symbolic-ref", "--quiet", "HEAD")
		if err != nil || !strings.HasPrefix(sym, "refs/heads/") {
			return "", "", false
		}
		sha, err := workspaceReadGit(ctx, mirrorDir, "rev-parse", "--verify", sym+"^{commit}")
		if err != nil || sha == "" {
			return "", "", false
		}
		return sym, sha, true
	case "branch":
		full := "refs/heads/" + strings.TrimPrefix(ref.Value, "refs/heads/")
		sha, err := workspaceReadGit(ctx, mirrorDir, "rev-parse", "--verify", full+"^{commit}")
		if err != nil || sha == "" {
			return "", "", false
		}
		return full, sha, true
	case "tag":
		full := "refs/tags/" + strings.TrimPrefix(ref.Value, "refs/tags/")
		sha, err := workspaceReadGit(ctx, mirrorDir, "rev-parse", "--verify", full+"^{commit}")
		if err != nil || sha == "" {
			return "", "", false
		}
		return full, sha, true
	case "ref":
		sha, err := workspaceReadGit(ctx, mirrorDir, "rev-parse", "--verify", ref.Value+"^{commit}")
		if err != nil || sha == "" {
			return "", "", false
		}
		return ref.Value, sha, true
	case "commit":
		sha, err := workspaceReadGit(ctx, mirrorDir, "rev-parse", "--verify", ref.Value+"^{commit}")
		if err != nil || !strings.EqualFold(sha, ref.Value) {
			return "", "", false
		}
		return ref.Value, sha, true
	default:
		return "", "", false
	}
}
func (e *workspaceExecutor) materialize(ctx context.Context, r *taskworkspace.Instance) string {
	if !e.owned(e.journal.Plan.RootPath) || !e.sameGitDir(r) {
		return "identity_mismatch"
	}
	// A completed command whose receipt was lost is adopted only through its
	// exact private Git registration, not because a directory happens to exist.
	if r.State == "materializing" {
		_ = e.clearBareWorktreeConfig(ctx, r)
		if e.checkRegistration(ctx, r) == "" {
			id, err := workspaceFileID(r.Path)
			if err != nil {
				return "identity_mismatch"
			}
			e.journal.Identities[r.Path] = id
			r.State = "ready"
			return e.inspect(ctx, r)
		}
	}
	if r.Path != e.journal.Plan.RootPath {
		if _, err := os.Lstat(r.Path); !errors.Is(err, os.ErrNotExist) {
			return "path_conflict"
		}
	}
	args := []string{"worktree", "add", "-b", r.TaskBranch, "--", r.Path, r.ResolvedCommit}
	if local := r.Input.Local; local != nil && local.ExistingBranch {
		tip, err := workspaceBranchTip(ctx, r.CommonDir, "refs/heads/"+r.TaskBranch)
		if err != nil || tip != r.ResolvedCommit {
			return "branch_conflict"
		}
		args = []string{"worktree", "add", "--", r.Path, r.TaskBranch}
	} else if tip, err := workspaceReadGit(ctx, r.CommonDir, "rev-parse", "--verify", "refs/heads/"+r.TaskBranch); err == nil {
		if r.Input.Local != nil || r.State != "materializing" || tip != r.ResolvedCommit {
			return "branch_conflict"
		}
		fields, err := workspaceWorktreeList(ctx, r.CommonDir)
		if err != nil || slices.Contains(fields, "branch refs/heads/"+r.TaskBranch) {
			return "branch_conflict"
		}
		// Git may create the branch before checkout fails. Resume that exact
		// unmodified branch only after proving no worktree has it checked out.
		args = []string{"worktree", "add", "--", r.Path, r.TaskBranch}
	}
	r.State = "materializing"
	if err := e.save(); err != nil {
		return "persistence_failed"
	}
	if _, err := e.git(ctx, r.CommonDir, args...); err != nil {
		return "worktree_failed"
	}
	id, err := workspaceFileID(r.Path)
	if err != nil {
		return "identity_mismatch"
	}
	e.journal.Identities[r.Path] = id
	if code := e.clearBareWorktreeConfig(ctx, r); code != "" {
		return code
	}
	if code := e.checkRegistration(ctx, r); code != "" {
		return code
	}
	head, err := workspaceReadGit(ctx, r.Path, "rev-parse", "HEAD")
	if err != nil || head != r.ResolvedCommit {
		return "git_layout_mismatch"
	}
	r.State = "ready"
	if err = e.save(); err != nil {
		return "persistence_failed"
	}
	return e.inspect(ctx, r)
}

// When a local bare repository enables extensions.worktreeConfig while keeping
// core.bare=true in its shared config, `git -C <bare> worktree add` creates a
// linked worktree without a config.worktree override, so Git treats the new
// checkout itself as bare and refuses working-tree commands (--show-toplevel,
// status, commit). Set core.bare=false only in this worktree's config.worktree
// after verifying registration under r.CommonDir without symlinks.
func (e *workspaceExecutor) clearBareWorktreeConfig(ctx context.Context, r *taskworkspace.Instance) string {
	if r.Input.Local == nil || !e.sameGitDir(r) || !e.owned(r.Path) {
		return ""
	}
	if workspaceNoLinks(r.Path) != nil || workspaceNoLinks(filepath.Join(r.Path, ".git")) != nil {
		return "git_layout_mismatch"
	}
	bare, err := workspaceReadGit(ctx, r.Path, "rev-parse", "--is-bare-repository")
	if err != nil || bare != "true" {
		return ""
	}
	wtConfig, err := workspaceReadGit(ctx, r.CommonDir, "config", "--bool", "--get", "extensions.worktreeConfig")
	if err != nil || wtConfig != "true" {
		return ""
	}
	common, err := workspaceReadGit(ctx, r.Path, "rev-parse", "--git-common-dir")
	if err != nil {
		return "git_layout_mismatch"
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(r.Path, common)
	}
	if filepath.Clean(common) != r.CommonDir {
		return "git_layout_mismatch"
	}
	dir, err := workspaceReadGit(ctx, r.Path, "rev-parse", "--git-dir")
	if err != nil {
		return "git_layout_mismatch"
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(r.Path, dir)
	}
	dir = filepath.Clean(dir)
	rel, err := filepath.Rel(filepath.Join(r.CommonDir, "worktrees"), dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) ||
		workspaceNoLinks(dir) != nil || workspaceNoLinks(filepath.Join(dir, "config.worktree")) != nil {
		return "git_layout_mismatch"
	}
	registered, err := workspaceWorktreeList(ctx, r.CommonDir)
	if err != nil || !slices.ContainsFunc(registered, func(field string) bool { return strings.TrimSpace(field) == "worktree "+r.Path }) {
		return "git_layout_mismatch"
	}
	if _, err := e.git(ctx, r.Path, "config", "--worktree", "core.bare", "false"); err != nil {
		return "git_layout_mismatch"
	}
	return ""
}
func (e *workspaceExecutor) checkRegistration(ctx context.Context, r *taskworkspace.Instance) string {
	if workspaceNoLinks(r.Path) != nil || !e.sameGitDir(r) || r.Input.Local != nil && !e.owned(r.Path) {
		return "identity_mismatch"
	}
	for _, path := range []string{filepath.Join(r.Path, ".git"), filepath.Join(r.CommonDir, "config"), filepath.Join(r.CommonDir, "objects"), filepath.Join(r.CommonDir, "refs"), filepath.Join(r.CommonDir, "worktrees")} {
		if workspaceNoLinks(path) != nil {
			return "git_layout_mismatch"
		}
	}
	common, err := workspaceReadGit(ctx, r.Path, "rev-parse", "--git-common-dir")
	if err != nil {
		return "workspace_drift"
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(r.Path, common)
	}
	if filepath.Clean(common) != r.CommonDir {
		return "git_layout_mismatch"
	}
	dir, err := workspaceReadGit(ctx, r.Path, "rev-parse", "--git-dir")
	if err != nil {
		return "git_layout_mismatch"
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(r.Path, dir)
	}
	dir = filepath.Clean(dir)
	rel, err := filepath.Rel(filepath.Join(r.CommonDir, "worktrees"), dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) ||
		workspaceNoLinks(dir) != nil || workspaceNoLinks(filepath.Join(dir, "config.worktree")) != nil {
		return "git_layout_mismatch"
	}
	top, err := workspaceReadGit(ctx, r.Path, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(top) != r.Path {
		return "git_layout_mismatch"
	}
	registered, err := workspaceWorktreeList(ctx, r.CommonDir)
	if err != nil {
		return "git_layout_mismatch"
	}
	found := false
	for _, field := range registered {
		if strings.TrimSpace(field) == "worktree "+r.Path {
			found = true
		}
	}
	if !found {
		return "git_layout_mismatch"
	}
	if r.Input.Local == nil {
		for _, path := range []string{filepath.Join(r.CommonDir, "objects", "info", "alternates"), filepath.Join(r.CommonDir, "shallow")} {
			data, err := os.ReadFile(path)
			if err != nil && !errors.Is(err, os.ErrNotExist) || len(data) > 0 {
				return "git_layout_mismatch"
			}
		}
	}
	return ""
}
func (e *workspaceExecutor) inspect(ctx context.Context, r *taskworkspace.Instance) string {
	if !e.owned(e.journal.Plan.RootPath) || !e.owned(r.Path) {
		return "identity_mismatch"
	}
	if code := e.checkRegistration(ctx, r); code != "" {
		return code
	}
	branch, err := workspaceReadGit(ctx, r.Path, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || branch != r.TaskBranch {
		return "workspace_drift"
	}
	head, err := workspaceReadGit(ctx, r.Path, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "workspace_drift"
	}
	status, err := workspaceReadGit(ctx, r.Path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return "inspection_failed"
	}
	r.CurrentHead = head
	r.Dirty = status != ""
	r.Error = nil
	return ""
}

// A quiet lookup returns status 1 for a missing ref. A command failure must
// never be reported as a completed deletion when resuming a partial cleanup.
func workspaceBranchTip(ctx context.Context, dir, ref string) (string, error) {
	tip, err := workspaceReadGit(ctx, dir, "rev-parse", "--verify", "--quiet", ref)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return "", nil
	}
	return tip, err
}

// workspaceWorktreeList is `git worktree list --porcelain` split into its fields. Git before 2.36 (Debian 11 ships
// 2.30) refuses -z as a usage error (exit 129); there the newline-separated listing is read instead, which differs only
// for a path with a newline in it.
func workspaceWorktreeList(ctx context.Context, common string) ([]string, error) {
	listing, err := workspaceReadGit(ctx, common, "worktree", "list", "--porcelain", "-z")
	if err == nil {
		return strings.Split(listing, "\x00"), nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 129 {
		return nil, err
	}
	if listing, err = workspaceReadGit(ctx, common, "worktree", "list", "--porcelain"); err != nil {
		return nil, err
	}
	return strings.Split(listing, "\n"), nil
}

// A detached worktree may still reserve a branch while rebasing or bisecting.
// Git keeps these names in the linked worktree's private administration directory.
func workspaceBranchInUse(ctx context.Context, common, ref string) (bool, error) {
	fields, err := workspaceWorktreeList(ctx, common)
	if err != nil {
		return false, err
	}
	if slices.Contains(fields, "branch "+ref) {
		return true, nil
	}
	admin := filepath.Join(common, "worktrees")
	dirs, err := os.ReadDir(admin)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, dir := range dirs {
		for _, name := range []string{"rebase-merge/head-name", "rebase-apply/head-name", "rebase-merge/update-refs", "BISECT_START"} {
			path := filepath.Join(admin, dir.Name(), name)
			if err := workspaceNoLinks(path); err != nil {
				return false, err
			}
			data, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return false, err
			}
			for line := range strings.SplitSeq(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == ref || name == "BISECT_START" && "refs/heads/"+line == ref {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (e *workspaceExecutor) cleanup(ctx context.Context, req taskworkspace.Request) taskworkspace.Record {
	j := &e.journal
	if j.State == "removed" {
		return j.Record
	}
	if len(j.Uses) > 0 || e.n.agentRunsIn(j.Plan.RootPath) {
		result := j.Record
		result.Error = taskworkspace.Failure("worktree_in_use", "cleanup", j.OperationID, "", j.Plan.RootPath)
		return result
	}
	if !e.owned(j.Plan.RootPath) {
		if _, err := os.Lstat(j.Plan.RootPath); !errors.Is(err, os.ErrNotExist) || j.Cleanup == nil && !e.hasNoResources(j.Plan.RootPath) {
			return e.fail("identity_mismatch", "cleanup", "", j.Plan.RootPath)
		}
	}
	if j.Cleanup != nil {
		for i := range j.Repositories {
			r := &j.Repositories[i]
			if !r.Removed && r.State == "ready" {
				if _, err := os.Lstat(r.Path); errors.Is(err, os.ErrNotExist) && e.sameGitDir(r) {
					fields, err := workspaceWorktreeList(ctx, r.CommonDir)
					if err == nil && !slices.Contains(fields, "worktree "+r.Path) {
						r.Removed = true
					}
				}
			}
		}
	}
	for i := range j.Repositories {
		r := &j.Repositories[i]
		if r.State == "materializing" && e.checkRegistration(ctx, r) == "" {
			id, err := workspaceFileID(r.Path)
			if err != nil {
				return e.fail("identity_mismatch", "cleanup", r.RepoID, r.Path)
			}
			j.Identities[r.Path] = id
			r.State = "ready"
		}
	}
	// Inspect all selected instances before removing any. Additional worktrees
	// remain registered and are never recursively removed with their object store.
	branchTips := make(map[string]string)
	for i := range j.Repositories {
		r := &j.Repositories[i]
		if !r.Removed && r.State != "ready" {
			continue
		}
		if !r.Removed {
			if code := e.inspect(ctx, r); code != "" {
				return e.fail(code, "cleanup", r.RepoID, r.Path)
			}
			if r.Dirty && !req.Force {
				return e.fail("worktree_dirty", "cleanup", r.RepoID, r.Path)
			}
		}
		if req.DeleteBranch && canDeleteTaskBranch(r) {
			// A partial cleanup may have removed the worktree already. Its branch
			// still needs the same non-force protection on every retry/recovery.
			if !e.sameGitDir(r) {
				return e.fail("identity_mismatch", "cleanup", r.RepoID, r.CommonDir)
			}
			tip, err := workspaceBranchTip(ctx, r.CommonDir, "refs/heads/"+r.TaskBranch)
			if err != nil {
				return e.fail("inspection_failed", "cleanup", r.RepoID, r.Path)
			}
			branchTips[r.RepoID] = tip
			if !req.Force && tip != "" {
				if _, err := workspaceReadGit(ctx, r.CommonDir, "merge-base", "--is-ancestor", tip, r.ResolvedCommit); err != nil {
					return e.fail("worktree_unmerged", "cleanup", r.RepoID, r.Path)
				}
			}
		}
	}
	j.Cleanup = &taskworkspace.CleanupResult{RetainedPaths: []string{}}
	for i := range j.Repositories {
		r := &j.Repositories[i]
		if !e.sameGitDir(r) {
			if j.Identities[r.CommonDir] != "" || r.Input.Local != nil && j.LocalGitIdentity != "" {
				return e.fail("identity_mismatch", "cleanup", r.RepoID, r.CommonDir)
			}
			continue
		}
		if !r.Removed && r.State == "ready" {
			args := []string{"worktree", "remove"}
			if req.Force {
				args = append(args, "--force")
			}
			if _, err := e.git(ctx, r.CommonDir, append(args, "--", r.Path)...); err != nil {
				return e.fail("cleanup_failed", "cleanup", r.RepoID, r.Path)
			}
			if _, err := os.Lstat(r.Path); !errors.Is(err, os.ErrNotExist) {
				return e.fail("cleanup_failed", "cleanup", r.RepoID, r.Path)
			}
			r.Removed = true
			if err := e.save(); err != nil {
				return e.fail("persistence_failed", "cleanup", r.RepoID, r.Path)
			}
		}
		if req.DeleteBranch && r.Removed && canDeleteTaskBranch(r) {
			ref := "refs/heads/" + r.TaskBranch
			tip, err := workspaceBranchTip(ctx, r.CommonDir, ref)
			if err != nil {
				return e.fail("inspection_failed", "branch", r.RepoID, r.Path)
			}
			if tip != "" {
				args := []string{"branch", "-D", "--", r.TaskBranch}
				if !req.Force {
					if tip != branchTips[r.RepoID] {
						return e.fail("worktree_unmerged", "branch", r.RepoID, r.Path)
					}
					// update-ref checks the inspected tip atomically. Unlike branch
					// -D it does not check worktree occupancy itself.
					busy, err := workspaceBranchInUse(ctx, r.CommonDir, ref)
					if err != nil {
						return e.fail("inspection_failed", "branch", r.RepoID, r.Path)
					}
					if busy {
						return e.fail("worktree_in_use", "branch", r.RepoID, r.Path)
					}
					args = []string{"update-ref", "--no-deref", "-d", ref, tip}
				}
				if _, err := e.git(ctx, r.CommonDir, args...); err != nil {
					return e.fail("cleanup_failed", "branch", r.RepoID, r.Path)
				}
			}
			r.BranchDeleted = true
		}
		j.Cleanup.RetainedPaths = append(j.Cleanup.RetainedPaths, r.CommonDir)
	}
	// Only remove an empty container. Unknown files survive even with force,
	// which has the same worktree/branch scope as the existing product command.
	if _, err := os.Lstat(j.Plan.RootPath); err == nil {
		for _, r := range j.Repositories {
			if r.Path == j.Plan.RootPath && r.Removed {
				return e.fail("cleanup_retained", "cleanup", "", j.Plan.RootPath)
			}
		}
		if !e.owned(j.Plan.RootPath) {
			return e.fail("identity_mismatch", "cleanup", "", j.Plan.RootPath)
		}
		if err = syscall.Rmdir(j.Plan.RootPath); err != nil {
			return e.fail("cleanup_retained", "cleanup", "", j.Plan.RootPath)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return e.fail("cleanup_failed", "cleanup", "", j.Plan.RootPath)
	}
	j.State = "removed"
	j.Error = nil
	j.Cleanup.Removed = true
	return j.Record
}

func canDeleteTaskBranch(r *taskworkspace.Instance) bool {
	return !r.BranchDeleted && (r.Input.Local == nil || !r.Input.Local.ExistingBranch)
}

func (n *Node) workspaceSourceAllowed(input repository.PreparationRepository) bool {
	transport := input.SourceSnapshot.CloneURL
	if transport == "" || len(transport) > 4096 || strings.ContainsFunc(transport, unicode.IsControl) {
		return false
	}
	source := input.SourceSnapshot.Identity
	if repository.IsExtensionProvider(source.Provider) {
		// The Manager resolved the provider before freezing this source. Recheck its shape locally; preparation
		// does not need the provider to remain installed and never calls it.
		u, err := url.Parse(source.InstanceURL)
		if err != nil || u.Hostname() == "" {
			return false
		}
		checked, err := repository.CheckParseAnswer(repository.ProviderInfo{ID: source.Provider, Hosts: []string{u.Hostname()}},
			repository.ParseAnswer{InstanceURL: source.InstanceURL, Path: source.Path, CanonicalURL: source.CanonicalURL})
		if err != nil || checked.Identity != source {
			return false
		}
	} else {
		identity, err := repository.Parse(source.CanonicalURL, source.Provider)
		if err != nil || identity != source {
			return false
		}
	}
	local := transport
	if strings.HasPrefix(transport, "file://") {
		u, err := url.Parse(transport)
		if err != nil || u.Host != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return false
		}
		local = u.Path
	}
	if filepath.IsAbs(local) {
		local = n.resolveHomeAlias(local)
		return n.offLimits(local) == nil && workspaceNoLinks(local) == nil
	}
	_, err := repository.CloneTransport(transport, input.SourceSnapshot.Identity)
	return err == nil
}
