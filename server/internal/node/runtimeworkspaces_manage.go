package node

// Renaming and deleting an engine runtime's workspaces (#6297; docs/integrations/runtimes.md "Workspaces"; Paseo:
// docs/integrations/paseo.md "Spaces and workspaces"): a workspace's title (worktree.rename), the space its sessions
// are grouped in (space.rename: Paseo's project), and a worktree the runtime made, deleted the way a terminal host's is
// (worktree.delete: a preview the user confirms with its token, then a reconciliation that trusts only the runtime's
// list and the disk), the runtime archiving it (workspace.archive). The runtime removes such a folder whatever is in
// it (Paseo forces it), so what the user confirmed is checked here first: the changes the preview showed, the sessions
// the archive ends, the checkout's identity. Its branch stays unless the user asked for it to go (and git agrees).

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// workspaceRenameTimeout bounds workspace.rename, within the 20 s the hub gives space.rename and the 30 s of
// worktree/rename.
var workspaceRenameTimeout = 15 * time.Second

// runtimeRemoveDeadline bounds a closed Agent's worktree.remove on a runtime engine, the checks and the runtime's
// archive together, inside the 90 s the hub gives worktree/remove. A confirmed worktree.delete has
// runtimeWorktreeDeadline, inside its 150 s.
var runtimeRemoveDeadline = 85 * time.Second

// dropWorkspace takes a workspace the runtime archived off its list at once: its next push says the same.
func (h *runtimeConn) dropWorkspace(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.workspaces = slices.DeleteFunc(h.workspaces, func(w runtimeWorkspace) bool { return w.ID == id })
}

// retitleWorkspace gives a workspace the title it was just renamed to, until the runtime's next push says it.
func (h *runtimeConn) retitleWorkspace(id, title string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.workspaces {
		if h.workspaces[i].ID == id {
			h.workspaces[i].Title = title
		}
	}
}

// listsWorkspaces: the runtime has said its list on this connection (runtime.workspaces), so a workspace missing from
// it is gone, not unheard of yet.
func (h *runtimeConn) listsWorkspaces() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.workspacesSaid
}

// workspaceSessions are the ids of h's sessions in workspace id, sorted.
func (h *runtimeConn) workspaceSessions(id string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, s := range h.sessions {
		if s.WorkspaceID == id {
			out = append(out, s.SessionID)
		}
	}
	slices.Sort(out)
	return out
}

// workspaceAtWork: one of h's sessions in workspace id may still be writing in it: starting, working, waiting for the
// user, or in a state the runtime does not say. Idle, done and stopped ones are not (the archive ends them).
func (h *runtimeConn) workspaceAtWork(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.ContainsFunc(h.sessions, func(s proto.Session) bool {
		return s.WorkspaceID == id && (s.Starting || !slices.Contains([]string{proto.StatusIdle, proto.StatusDone, proto.StatusOffline}, s.Status))
	})
}

// workspaceLive: one of h's sessions in workspace id is running (any state but stopped), which archiving the
// workspace would end.
func (h *runtimeConn) workspaceLive(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.ContainsFunc(h.sessions, func(s proto.Session) bool { return s.WorkspaceID == id && s.Status != proto.StatusOffline })
}

// ownedWorktreeAt is h's workspace whose folder is dir: a worktree h made (owned), which it removes with the
// workspace. h may be nil (the runtime away): none.
func (h *runtimeConn) ownedWorktreeAt(dir string) (runtimeWorkspace, bool) {
	if h == nil || !h.makesWorkspaces() {
		return runtimeWorkspace{}, false
	}
	d := realPath(filepath.Clean(dir))
	for _, w := range h.workspacesCopy() {
		if w.Kind == workspaceWorktree && w.Owned && realPath(w.Cwd) == d {
			return w, true
		}
	}
	return runtimeWorkspace{}, false
}

// runtimeRenameWorkspace is worktree.rename of runtime h's workspace: its title. Its branch, folder and sessions keep
// their names.
func (n *Node) runtimeRenameWorkspace(ctx context.Context, h *runtimeConn, id, name string) error {
	if _, ok := h.workspaceByID(id); !ok {
		return composerError(ctx, "runtime_workspace_missing", i18n.Args{"engine": h.label()})
	}
	cctx, cancel := context.WithTimeout(ctx, workspaceRenameTimeout)
	defer cancel()
	var res struct {
		Workspace *runtimeWorkspace `json:"workspace"`
	}
	if err := h.peer.Call(cctx, "workspace.rename", map[string]any{"workspace_id": id, "title": name}, &res); err != nil {
		return runtimeErr(h, err)
	}
	if res.Workspace != nil {
		if w := cleanWorkspaces([]runtimeWorkspace{*res.Workspace}); len(w) == 1 && w[0].ID == id {
			h.keepWorkspace(w[0])
			return nil
		}
	}
	h.retitleWorkspace(id, name)
	return nil
}

// runtimeSpace is the engine runtime a space id is one of: the space its sessions are grouped in (Paseo's project), or
// one of its workspaces, which names the space it is in (Android names a session's space by its workspace_id). space
// is the space's own id; h is nil when no engine runtime has it.
func (n *Node) runtimeSpace(id string) (h *runtimeConn, space string) {
	if id == "" {
		return nil, ""
	}
	runtimes, _ := n.catalogRuntimes()
	for _, h := range runtimes {
		for _, w := range h.workspacesCopy() {
			if w.SpaceID != "" && (w.SpaceID == id || w.ID == id) {
				return h, w.SpaceID
			}
		}
		h.mu.Lock()
		found := slices.ContainsFunc(h.sessions, func(s proto.Session) bool { return s.SpaceID == id })
		h.mu.Unlock()
		if found {
			return h, id
		}
	}
	return nil, ""
}

// runtimeRenameSpace is space.rename of runtime h's space: the name its sessions show, which the runtime answers
// (what the hub keys pins and to-do links by).
func (n *Node) runtimeRenameSpace(ctx context.Context, h *runtimeConn, space, name string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, workspaceRenameTimeout)
	defer cancel()
	var res struct {
		SpaceName string `json:"space_name"`
	}
	if err := h.peer.Call(cctx, "workspace.rename", map[string]any{"space_id": space, "title": name}, &res); err != nil {
		return "", runtimeErr(h, err)
	}
	return firstOf(strings.TrimSpace(res.SpaceName), name), nil
}

// runtimeWorkspaceDeletion is what a preview of deleting a runtime's worktree binds, besides the checkout's identity and
// its changes (composerDeletion's pathInfo, gitInfo and dirtyState).
type runtimeWorkspaceDeletion struct {
	kind      string // the engine (paseo)
	instance  string // the runtime process that listed it
	workspace runtimeWorkspace
	sessions  string // its sessions, which the archive ends: one started since the preview is another confirmation
	branch    string
	common    string // the repository's git common directory, where a branch the user lets go is deleted
}

// inspectRuntimeDeletion reads what deleting runtime h's workspace id would do: only a worktree the runtime made, as
// its own checkout's top level (never a main checkout, a folder, or a worktree someone else made, which the runtime
// would only archive and leave on the disk), with its changes counted, and nothing at work in it (runtimeAtWork).
func (n *Node) inspectRuntimeDeletion(ctx context.Context, h *runtimeConn, kind, id string) (*composerDeletion, int, error) {
	w, ok := h.workspaceByID(id)
	if !ok {
		return nil, 0, composerError(ctx, "preview_stale")
	}
	engine := i18n.Args{"engine": h.label()}
	switch {
	case w.Kind == workspaceCheckout:
		return nil, 0, composerError(ctx, "main_checkout")
	case w.Kind != workspaceWorktree:
		return nil, 0, composerError(ctx, "runtime_folder_kept", engine)
	case !w.Owned:
		return nil, 0, composerError(ctx, "runtime_not_owned", engine)
	}
	path := w.Cwd
	if err := runtimeWorktreeTop(ctx, h, path); err != nil {
		return nil, 0, err
	}
	if err := n.runtimeAtWork(ctx, h, w); err != nil {
		return nil, 0, err
	}
	entry, dirty, err := inspectWorktreeDeletion(ctx, path, false)
	if err != nil {
		return nil, 0, err
	}
	branch, err := git(ctx, path, "branch", "--show-current")
	if err != nil {
		return nil, 0, err
	}
	common, err := absGit(ctx, path, "--git-common-dir")
	if err != nil {
		return nil, 0, err
	}
	h.mu.Lock()
	instance := h.hello.Instance
	h.mu.Unlock()
	entry.node = n
	entry.runtime = &runtimeWorkspaceDeletion{kind: kind, instance: instance, workspace: w,
		sessions: strings.Join(h.workspaceSessions(id), "\n"), branch: strings.TrimSpace(branch), common: common}
	return entry, dirty, nil
}

// runtimeWorktreeTop says why path, the folder of a worktree runtime h lists, is not a worktree's top level to delete
// (nil when it is): the folder is gone, git no longer knows it as a worktree (its record in the repository pruned), or
// it is a folder inside one.
func runtimeWorktreeTop(ctx context.Context, h *runtimeConn, path string) error {
	args := i18n.Args{"engine": h.label(), "path": path}
	if _, err := os.Stat(path); err != nil {
		return composerError(ctx, "runtime_folder_gone", args)
	}
	top, err := git(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return composerError(ctx, "runtime_git_gone", args)
	}
	if realPath(strings.TrimSpace(top)) != realPath(path) {
		return composerError(ctx, "runtime_not_toplevel", args)
	}
	return nil
}

// runtimeAtWork refuses deleting runtime workspace w while work in it could be lost: one of its sessions still at
// work (workspaceAtWork), or an Agent of any engine running in its folder (a herdr one that went there).
func (n *Node) runtimeAtWork(ctx context.Context, h *runtimeConn, w runtimeWorkspace) error {
	if h.workspaceAtWork(w.ID) || n.agentRunsIn(w.Cwd) {
		return composerError(ctx, "runtime_busy", i18n.Args{"engine": h.label()})
	}
	return nil
}

// sameRuntimeDeletion: nothing a preview showed has changed since: the same runtime process lists the same workspace
// (its identity: a new title or a moved branch name is still it) with the same sessions, and the checkout is the same
// one, on the same branch, with the same changes.
func sameRuntimeDeletion(a, b *composerDeletion) bool {
	x, y := a.runtime, b.runtime
	return x != nil && y != nil && x.kind == y.kind && x.instance == y.instance && sameRuntimeWorkspace(x.workspace, y.workspace) &&
		x.sessions == y.sessions && x.branch == y.branch && x.common == y.common &&
		os.SameFile(a.pathInfo, b.pathInfo) && os.SameFile(a.gitInfo, b.gitInfo) && a.dirtyState == b.dirtyState
}

// sameRuntimeWorkspace: a and b are the same workspace of the runtime's: id, folder, kind, the runtime's ownership and
// repository. Its title, its space's name and the branch the runtime last said are labels.
func sameRuntimeWorkspace(a, b runtimeWorkspace) bool {
	return a.ID == b.ID && a.Cwd == b.Cwd && a.Kind == b.Kind && a.Owned == b.Owned && a.RepoRoot == b.RepoRoot
}

// runtimeWorktreeDelete is worktree.delete of an engine runtime's worktree, the way a terminal host's is: a preview
// (its token), a confirmation that has to match it (and force for changes it showed), then the runtime archives the
// workspace, which removes the worktree; reconcile only reads. Closing (close_only: archiving a workspace and keeping
// its folder) is left to the runtime's own app.
func (n *Node) runtimeWorktreeDelete(ctx context.Context, kind, id string, in workspaceDeletionInput) (any, error) {
	if in.CloseOnly {
		return nil, composerError(ctx, "close_unsupported")
	}
	var prior *composerDeletion
	if !in.Preview {
		composerDeletions.Lock()
		prior = composerDeletions.previews[in.Token]
		dispatched := prior != nil && prior.dispatched
		composerDeletions.Unlock()
		if prior == nil || prior.node != n || prior.runtime == nil || prior.runtime.kind != kind || prior.runtime.workspace.ID != id ||
			time.Since(prior.created) > 24*time.Hour {
			return nil, composerError(ctx, "preview_required")
		}
		if in.Reconcile { // read only, whether or not the runtime is connected now
			return n.reconcileRuntimeDeletion(prior), nil
		}
		if dispatched {
			return nil, composerError(ctx, "delete_dispatched")
		}
	}
	h, _, err := n.worktreeRuntime(ctx, kind)
	if err != nil {
		return nil, err
	}
	// One deadline for the checks and the runtime's archive together, inside the hub's 150 s.
	ctx, cancel := context.WithTimeout(ctx, runtimeWorktreeDeadline)
	defer cancel()
	current, dirty, err := n.inspectRuntimeDeletion(ctx, h, kind, id)
	if err != nil {
		return nil, err
	}
	if in.Preview {
		token, err := rememberWorkspaceDeletion(current)
		if err != nil {
			return nil, err
		}
		return proto.WorktreeDeletePreview{WorkspaceID: id, Kind: kind, HostID: "local", InstanceID: current.runtime.instance,
			Path: n.catalogDir(current.runtime.workspace.Cwd), Branch: current.runtime.branch, Dirty: dirty, PreviewToken: token}, nil
	}
	if time.Since(prior.created) > 10*time.Minute || !sameRuntimeDeletion(prior, current) {
		return nil, composerError(ctx, "preview_stale")
	}
	if dirty > 0 && !in.Force {
		return nil, composerError(ctx, "dirty")
	}
	composerDeletions.Lock()
	if prior.dispatched {
		composerDeletions.Unlock()
		return nil, composerError(ctx, "delete_dispatched")
	}
	prior.dispatched = true
	composerDeletions.Unlock()

	err = h.peer.Call(ctx, "workspace.archive", map[string]any{"workspace_id": id}, nil)
	if err != nil {
		if result := n.reconcileRuntimeDeletion(prior); result.Removed {
			return result, nil
		}
		if !isRPCError(err) || ctx.Err() != nil {
			// No answer: whether the runtime archived anything is not known; reconcile tells.
			return proto.WorktreeDeleteResult{WorkspaceID: id, State: "unknown"}, nil
		}
		return nil, runtimeErr(h, err)
	}
	h.dropWorkspace(id)
	result := n.reconcileRuntimeDeletion(prior)
	if result.Removed && in.DeleteBranch && prior.runtime.branch != "" {
		_, err = git(ctx, prior.runtime.common, "branch", "-d", prior.runtime.branch)
		result.BranchDeleted = err == nil
	}
	return result, nil
}

// runtimeAgentWorktreeRemove is worktree.remove of a closed Agent's own worktree (#6115) on a runtime engine, after
// worktreeRemove's checks (one of Persona's, no Agent in its folder, changes and an unmerged branch only with force):
// the worktree workspace the runtime made at st.Path, archived through the runtime, which removes the folder. As Delete
// worktree, never while a session in the workspace is running (the archive would end it; the closed Agent leaves the
// runtime's list a moment after, and the clients try again meanwhile). Its branch goes after the folder, when asked.
func (n *Node) runtimeAgentWorktreeRemove(ctx context.Context, h *runtimeConn, st proto.WorktreeStatus, deleteBranch, force bool) (proto.WorktreeRemoved, error) {
	var res proto.WorktreeRemoved
	w, ok := h.ownedWorktreeAt(st.Path)
	if !ok {
		return res, refusal("not_our_worktree", "this folder is not a worktree Persona made", nil)
	}
	if h.workspaceLive(w.ID) || n.agentRunsIn(st.Path) {
		return res, refusal("worktree_in_use", "an agent still runs in this worktree", nil)
	}
	if err := h.peer.Call(ctx, "workspace.archive", map[string]any{"workspace_id": w.ID}, nil); err != nil {
		if isRPCError(err) && ctx.Err() == nil {
			return res, refusal("worktree_failed", runtimeErr(h, err).Error(), nil) // the runtime's own words
		}
		// No answer: whether the runtime archived it is not known.
		return res, refusal("worktree_failed", composerError(ctx, "runtime_remove_unanswered", i18n.Args{"engine": h.label()}).Error(), nil)
	}
	h.dropWorkspace(w.ID)
	if _, err := os.Lstat(st.Path); !os.IsNotExist(err) { // the runtime archived it but kept the folder
		return res, refusal("worktree_failed", composerError(ctx, "runtime_folder_left",
			i18n.Args{"engine": h.label(), "path": st.Path}).Error(), nil)
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

// reconcileRuntimeDeletion says what became of a runtime worktree a confirmed deletion was sent for: removed once the
// runtime connected now no longer lists it (having said its list) and its folder is gone; present while it lists it;
// unknown otherwise (the runtime away, or archived with its folder left on the disk).
func (n *Node) reconcileRuntimeDeletion(prior *composerDeletion) proto.WorktreeDeleteResult {
	target := prior.runtime
	result := proto.WorktreeDeleteResult{WorkspaceID: target.workspace.ID, State: "unknown"}
	h, _, ok := n.runtimeEngine(target.kind)
	if !ok || h == nil || !h.makesWorkspaces() || !h.listsWorkspaces() {
		return result
	}
	if _, listed := h.workspaceByID(target.workspace.ID); listed {
		result.State = "present"
		return result
	}
	if _, err := os.Lstat(filepath.Clean(target.workspace.Cwd)); !os.IsNotExist(err) {
		return result
	}
	result.Removed, result.State = true, "removed"
	return result
}
