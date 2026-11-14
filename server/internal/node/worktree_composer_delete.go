package node

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/proto"
)

// Preview records are deliberately ephemeral: a node restart invalidates confirmation, never authorizes a retry.
// A dispatched token is retained for read-only reconciliation and cannot dispatch a second removal.
var composerDeletions = struct {
	sync.Mutex
	previews map[string]*composerDeletion
}{previews: map[string]*composerDeletion{}}

type composerDeletion struct {
	node       *Node
	workspace  orcart.Worktree
	herdr      *herdrWorkspaceDeletion
	runtime    *runtimeWorkspaceDeletion // an engine runtime's worktree (runtimeworkspaces_manage.go)
	pathInfo   os.FileInfo
	gitInfo    os.FileInfo
	dirtyState string
	created    time.Time
	dispatched bool
}

type workspaceDeletionInput struct {
	Preview      bool   `json:"preview"`
	Reconcile    bool   `json:"reconcile"`
	Token        string `json:"preview_token"`
	Force        bool   `json:"force"`
	DeleteBranch bool   `json:"delete_branch"`
	CloseOnly    bool   `json:"close_only"`
}

func (n *Node) worktreeDelete(ctx context.Context, kind, id string, raw json.RawMessage) (any, error) {
	var in workspaceDeletionInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	if in.Preview && (in.Reconcile || in.Token != "" || in.Force || in.DeleteBranch) {
		return nil, composerError(ctx, "preview_invalid")
	}
	if kind == "herdr" {
		return n.herdrWorktreeDelete(ctx, id, in)
	}
	if _, _, runtime := n.runtimeEngine(kind); runtime && kind != "" { // Paseo's (runtimeworkspaces_manage.go)
		return n.runtimeWorktreeDelete(ctx, kind, id, in)
	}
	if in.CloseOnly {
		return nil, composerError(ctx, "close_unsupported")
	}
	var prior *composerDeletion
	if !in.Preview {
		composerDeletions.Lock()
		prior = composerDeletions.previews[in.Token]
		dispatched := prior != nil && prior.dispatched
		composerDeletions.Unlock()
		if prior == nil || prior.node != n || prior.herdr != nil || prior.runtime != nil || kind != "orca" || prior.workspace.WorktreeID != id || time.Since(prior.created) > 24*time.Hour {
			return nil, composerError(ctx, "preview_required")
		}
		if in.Reconcile {
			return n.reconcileComposerDeletion(ctx, prior), nil
		}
		if dispatched {
			return nil, composerError(ctx, "delete_dispatched")
		}
	}
	o, w, trees, err := n.orcaWorkspace(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	if prior != nil && !sameComposerWorkspace(prior.workspace, w) {
		return nil, composerError(ctx, "preview_stale")
	}
	st, err := o.c.Probe(ctx)
	if err != nil {
		return nil, orcaErr(err)
	}
	if !slices.Contains(st.Capabilities, "worktree.remove-branch-policy.v1") ||
		!slices.Contains(st.Capabilities, "worktree.archive-failure-blocking.v1") ||
		folderComposerWorkspace(w) && !slices.Contains(st.Capabilities, "folder-workspace.unique-id-mutation.v1") {
		return nil, composerError(ctx, "update_delete")
	}
	if w.InstanceID == "" || w.HostID != "local" || w.Removing {
		return nil, composerError(ctx, "identity_unavailable")
	}
	if w.IsMain == nil || *w.IsMain {
		return nil, composerError(ctx, "main_workspace")
	}
	if len(w.ChildWorktreeIDs) > 0 {
		return nil, composerError(ctx, "children")
	}
	for _, child := range trees {
		parentKey := "worktree:" + id
		if strings.HasPrefix(id, "folder:") {
			parentKey = id
		}
		if localComposerWorkspace(child) && (child.ParentWorktreeID == id || child.WorkspaceLineage != nil && child.WorkspaceLineage.ParentWorkspaceKey == parentKey) {
			return nil, composerError(ctx, "children")
		}
	}
	current, dirty, err := inspectComposerDeletion(ctx, w)
	if err != nil {
		return nil, err
	}
	current.node = n
	if in.Preview {
		key, err := rememberWorkspaceDeletion(current)
		if err != nil {
			return nil, err
		}
		return proto.WorktreeDeletePreview{WorkspaceID: id, Kind: kind, HostID: "local", InstanceID: w.InstanceID,
			Path: w.Path, Branch: strings.TrimPrefix(w.Branch, "refs/heads/"), Dirty: dirty,
			Folder: folderComposerWorkspace(w), PreviewToken: key}, nil
	}
	if time.Since(prior.created) > 10*time.Minute || !os.SameFile(prior.pathInfo, current.pathInfo) ||
		prior.gitInfo != nil && (current.gitInfo == nil || !os.SameFile(prior.gitInfo, current.gitInfo)) ||
		prior.dirtyState != current.dirtyState {
		return nil, composerError(ctx, "preview_stale")
	}
	if dirty > 0 && !in.Force {
		return nil, refusal("worktree_dirty", i18n.T(i18n.From(ctx), "worktree-composer.dirty"), map[string]any{"dirty": dirty})
	}
	if folderComposerWorkspace(w) && in.DeleteBranch {
		return nil, composerError(ctx, "folder_branch")
	}
	composerDeletions.Lock()
	if prior.dispatched {
		composerDeletions.Unlock()
		return nil, composerError(ctx, "delete_dispatched")
	}
	prior.dispatched = true
	composerDeletions.Unlock()

	var result struct {
		Removed       bool `json:"removed"`
		Removing      bool `json:"removing"`
		BranchDeleted bool `json:"branchDeleted"`
	}
	if folderComposerWorkspace(w) && strings.HasPrefix(id, "folder:") {
		err = o.c.Call(ctx, 2*time.Minute, "folderWorkspace.delete", map[string]any{
			"folderWorkspaceId": strings.TrimPrefix(id, "folder:"), "requireUniqueId": true}, &result)
	} else {
		err = o.c.Call(ctx, 2*time.Minute, "worktree.rm", map[string]any{"worktree": "id:" + id, "hostId": "local",
			"deleteBranch": in.DeleteBranch, "force": in.Force, "runHooks": true, "waitForBackgroundRemoval": true}, &result)
	}
	if err != nil {
		// A transport error after dispatch cannot establish whether the runtime mutated anything.
		var rpcErr *orcart.Error
		if errors.As(err, &rpcErr) && rpcErr.Code != "runtime_timeout" {
			return nil, orcaErr(err)
		}
		return proto.WorktreeDeleteResult{WorkspaceID: id, State: "unknown"}, nil
	}
	if result.Removing || !result.Removed {
		state := "unknown"
		if result.Removing {
			state = "removing"
		}
		// Folder unregister replies may omit removed; catalog reconciliation is authoritative for folders.
		if folderComposerWorkspace(w) {
			return n.reconcileComposerDeletion(ctx, prior), nil
		}
		return proto.WorktreeDeleteResult{WorkspaceID: id, State: state}, nil
	}
	// Even a runtime completion acknowledgement must agree with the host-owned catalog and checkout disappearance.
	completed := n.reconcileComposerDeletion(ctx, prior)
	completed.BranchDeleted = result.BranchDeleted
	_ = n.refreshHosts(ctx, o)
	return completed, nil
}

func rememberWorkspaceDeletion(entry *composerDeletion) (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	key := hex.EncodeToString(token[:])
	composerDeletions.Lock()
	defer composerDeletions.Unlock()
	for k, old := range composerDeletions.previews {
		if time.Since(old.created) > 24*time.Hour {
			delete(composerDeletions.previews, k)
		}
	}
	composerDeletions.previews[key] = entry
	return key, nil
}

func sameComposerWorkspace(a, b orcart.Worktree) bool {
	return a.WorktreeID == b.WorktreeID && a.InstanceID == b.InstanceID && a.RepoID == b.RepoID &&
		localComposerWorkspace(a) && localComposerWorkspace(b) && a.Path == b.Path && a.Branch == b.Branch &&
		a.WorkspaceKind == b.WorkspaceKind && a.ParentWorktreeID == b.ParentWorktreeID
}

func (n *Node) reconcileComposerDeletion(ctx context.Context, prior *composerDeletion) proto.WorktreeDeleteResult {
	w := prior.workspace
	res := proto.WorktreeDeleteResult{WorkspaceID: w.WorktreeID, State: "unknown"}
	h, err := n.hostOfKind("orca")
	if err != nil {
		return res
	}
	o, ok := h.(*orcaHost)
	if !ok {
		return res
	}
	trees, err := composerCatalog(ctx, o)
	if err != nil {
		return res
	}
	for _, current := range trees {
		if current.WorktreeID != w.WorktreeID || !localComposerWorkspace(current) {
			continue
		}
		current, err = classifyComposerWorkspace(ctx, o, current)
		if err != nil {
			return res
		}
		if !sameComposerWorkspace(w, current) {
			return res
		}
		res.State = "present"
		if current.Removing {
			res.State = "removing"
		}
		return res
	}
	if !folderComposerWorkspace(w) {
		if _, err := os.Lstat(w.Path); !os.IsNotExist(err) {
			return res
		}
	}
	res.Removed, res.State = true, "removed"
	return res
}

func inspectComposerDeletion(ctx context.Context, w orcart.Worktree) (*composerDeletion, int, error) {
	entry, dirty, err := inspectWorktreeDeletion(ctx, w.Path, folderComposerWorkspace(w))
	if entry != nil {
		entry.workspace = w
	}
	return entry, dirty, err
}

func inspectWorktreeDeletion(ctx context.Context, path string, folder bool) (*composerDeletion, int, error) {
	entry := &composerDeletion{created: time.Now()}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, composerError(ctx, "identity_unavailable")
	}
	entry.pathInfo = info
	if folder {
		return entry, 0, nil
	}
	gitDir, err := absGit(ctx, path, "--git-dir")
	if err != nil {
		return nil, 0, err
	}
	common, err := absGit(ctx, path, "--git-common-dir")
	if err != nil {
		return nil, 0, err
	}
	if gitDir == common {
		return nil, 0, composerError(ctx, "main_checkout")
	}
	entry.gitInfo, err = os.Stat(gitDir)
	if err != nil {
		return nil, 0, err
	}
	status, err := git(ctx, path, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return nil, 0, err
	}
	dirty := 0
	if strings.TrimSpace(status) != "" {
		dirty = len(strings.Split(strings.TrimSpace(status), "\n"))
	}
	diff, err := git(ctx, path, "diff", "--no-ext-diff", "--no-textconv", "--binary", "HEAD", "--")
	if err != nil {
		return nil, 0, err
	}
	untracked, err := git(ctx, path, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, 0, err
	}
	head, err := git(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return nil, 0, err
	}
	sum := sha256.New()
	_, _ = fmt.Fprintf(sum, "%s\x00%s\x00%s\x00", head, status, diff)
	for _, name := range strings.Split(untracked, "\x00") {
		if name == "" {
			continue
		}
		full := filepath.Join(path, name)
		stat, err := os.Lstat(full)
		if err != nil {
			return nil, 0, err
		}
		_, _ = fmt.Fprintf(sum, "%s\x00%s\x00", name, stat.Mode())
		if stat.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(full)
			if err != nil {
				return nil, 0, err
			}
			_, _ = io.WriteString(sum, target)
		} else if stat.Mode().IsRegular() {
			file, err := os.Open(full)
			if err != nil {
				return nil, 0, err
			}
			_, err = io.Copy(sum, file)
			_ = file.Close()
			if err != nil {
				return nil, 0, err
			}
		} else {
			return nil, 0, composerError(ctx, "identity_unavailable")
		}
		_, _ = io.WriteString(sum, "\x00")
	}
	entry.dirtyState = hex.EncodeToString(sum.Sum(nil))
	return entry, dirty, nil
}
