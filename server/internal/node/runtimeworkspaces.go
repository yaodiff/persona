package node

// The workspaces of a runtime that is one of the machine's engines (docs/integrations/runtimes.md "Workspaces",
// docs/protocol/protocol.md "Runtime ↔ node"; Paseo: docs/integrations/paseo.md "Spaces and workspaces"). A runtime that keeps
// workspaces of its own (Paseo's checkouts, worktrees and folders) says so with caps.workspaces: it pushes them
// (runtime.workspaces) and makes worktrees as workspaces of its own (workspace.prepare, workspace.create). While the
// machine enables it as an engine its workspaces are the catalog's (workspaces.go), with or without sessions in them, its
// sessions name theirs (workspace_id, runtimes_events.go), and the worktrees Persona makes there ("New worktree…", a new
// agent's own worktree, #6115, an assistant task's) are made by it, in its own folder, so that it knows them.
// What Persona promises of a new worktree stays the node's to check with git, as for a terminal host (worktree.go): an
// existing branch is checked out as it is and never one checked out elsewhere, a new branch starts at the source
// checkout's HEAD, a folder that holds something is refused, nothing is forced. Only where the folder goes is the
// runtime's.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The kinds of a runtime's workspace.
const (
	workspaceCheckout = "checkout" // a repository's main checkout
	workspaceWorktree = "worktree" // a linked worktree of a repository checked out elsewhere
	workspaceFolder   = "folder"   // a folder in no repository
)

// Bounds on what a runtime's workspace list may carry.
const (
	maxRuntimeWorkspaces = 2000
	maxWorkspacePath     = 4096
)

// Deadlines of what the node asks a runtime engine about worktrees, each inside what the hub gives the call
// (server/internal/hub/panes.go nodeActions), so the user hears how it went, or that it ran out of time, rather than the
// hub's timeout while the runtime goes on: one for the whole of a create or an add (workspace.prepare, the checks, then
// workspace.create, which runs git worktree add: Paseo gives that up to 2 minutes) inside the 150 s of worktree/create
// and worktree/add; one for "New worktree…"'s first look inside worktree/prepare's 30 s; a short one for where a new
// agent's worktree would go, which worktree.status says only when it can.
var (
	runtimeWorktreeDeadline = 145 * time.Second
	runtimePrepareDeadline  = 25 * time.Second
	runtimeStatusDeadline   = 10 * time.Second
)

// runtimeSlugMax is the longest folder name a runtime engine gives a worktree (Paseo's MAX_SLUG_LENGTH).
const runtimeSlugMax = 50

// runtimeWorkspace is one of a runtime's workspaces, as runtime.workspaces says it.
type runtimeWorkspace struct {
	// ID is the runtime's id for it ([A-Za-z0-9][A-Za-z0-9_.-]{0,127}): what its sessions name as their workspace_id.
	ID    string `json:"workspace_id"`
	Title string `json:"title,omitempty"`
	// Cwd is its folder (absolute).
	Cwd string `json:"cwd"`
	// Kind is workspaceCheckout, workspaceWorktree or workspaceFolder.
	Kind   string `json:"kind,omitempty"`
	Branch string `json:"branch,omitempty"`
	// SpaceID and SpaceName are the space its sessions are grouped in (their space_id and space_name; Paseo's project).
	SpaceID   string `json:"space_id,omitempty"`
	SpaceName string `json:"space_name,omitempty"`
	// RepoRoot is its repository's main checkout.
	RepoRoot string `json:"repo_root,omitempty"`
	// Owned: the runtime made this checkout, a worktree of its own, and removes it with the workspace.
	Owned bool `json:"owned,omitempty"`
}

// cleanWorkspaces keeps the workspaces a runtime may list: valid ids, once each, absolute folders, a kind the node
// knows (anything else is a folder), text clipped, the list bounded.
func cleanWorkspaces(list []runtimeWorkspace) []runtimeWorkspace {
	clip := func(s string, max int) string {
		if r := []rune(strings.TrimSpace(s)); len(r) > max {
			return string(r[:max])
		}
		return strings.TrimSpace(s)
	}
	dir := func(p string) string {
		if p = strings.TrimSpace(p); !filepath.IsAbs(p) || len(p) > maxWorkspacePath {
			return ""
		}
		return filepath.Clean(p)
	}
	out := make([]runtimeWorkspace, 0, min(len(list), maxRuntimeWorkspaces))
	seen := map[string]bool{}
	for _, w := range list {
		if len(out) == maxRuntimeWorkspaces {
			break
		}
		if w.Cwd = dir(w.Cwd); !runtimeIDRe.MatchString(w.ID) || seen[w.ID] || w.Cwd == "" {
			continue
		}
		seen[w.ID] = true
		w.Title, w.Branch = clip(w.Title, maxLauncherText), clip(w.Branch, maxBranch)
		w.SpaceID, w.SpaceName = clip(w.SpaceID, maxLauncherText), clip(w.SpaceName, maxLauncherText)
		w.RepoRoot = dir(w.RepoRoot)
		switch w.Kind {
		case workspaceCheckout, workspaceWorktree:
		default:
			w.Kind = workspaceFolder
		}
		out = append(out, w)
	}
	return out
}

// makesWorkspaces: the runtime keeps workspaces of its own (caps.workspaces) and is on this machine's socket (a
// daemon's workspaces are another machine's).
func (h *runtimeConn) makesWorkspaces() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hello.Caps.Workspaces && h.daemon == ""
}

// setWorkspaces replaces its workspaces (runtime.workspaces).
func (h *runtimeConn) setWorkspaces(list []runtimeWorkspace) {
	h.mu.Lock()
	h.workspaces, h.workspacesSaid = list, true
	h.mu.Unlock()
}

func (h *runtimeConn) workspacesCopy() []runtimeWorkspace {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.workspaces)
}

// workspaceByID is its workspace of that id.
func (h *runtimeConn) workspaceByID(id string) (runtimeWorkspace, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, w := range h.workspaces {
		if w.ID == id {
			return w, true
		}
	}
	return runtimeWorkspace{}, false
}

// keepWorkspace lists w at once (one it just made), in place of the one of its id: the runtime's next push says it too.
func (h *runtimeConn) keepWorkspace(w runtimeWorkspace) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for i := range h.workspaces {
		if h.workspaces[i].ID == w.ID {
			h.workspaces[i] = w
			return
		}
	}
	h.workspaces = append(h.workspaces, w)
}

// runtimeEngine: kind (the machine's default host when "") is an engine that is a runtime (Paseo, Cindy), not a
// terminal host, and one of the engines the machine enables; h is its connection while it is connected, nil otherwise;
// engine is the kind. A runtime that is connected but not one of the machine's engines (Paseo's bridge running while the
// machine uses herdr) is not one: it makes, lists and deletes no workspaces here.
func (n *Node) runtimeEngine(kind string) (h *runtimeConn, engine string, ok bool) {
	if kind == "" {
		kind = n.defaultKind()
	}
	id, ok := engineRuntimes[kind]
	if !ok || n.hosts.Get(kind) != nil || !n.hostEnabled(kind) {
		return nil, kind, false
	}
	if n.runtimes != nil {
		n.runtimes.mu.Lock()
		h = n.runtimes.byRuntime(id)
		n.runtimes.mu.Unlock()
	}
	return h, kind, true
}

// catalogRuntimes are the runtimes whose workspaces are the machine's catalog: each engine runtime the machine enables
// that is connected and keeps workspaces. enabled: the machine enables an engine runtime that is not connected, or
// keeps workspaces: a source of workspaces, though perhaps not running now.
func (n *Node) catalogRuntimes() (list []*runtimeConn, enabled bool) {
	kinds := make([]string, 0, len(engineRuntimes))
	for kind := range engineRuntimes {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	for _, kind := range kinds {
		h, _, ok := n.runtimeEngine(kind)
		switch {
		case !ok: // not one of the machine's engines
		case h == nil:
			enabled = true // not running: none listed now, as a terminal host that is not running lists none
		case h.makesWorkspaces():
			enabled = true
			list = append(list, h)
		}
	}
	return list, enabled
}

// catalogDir is a workspace's folder as the catalog names it: its physical directory (symbolic links resolved), as
// the node names a session's working tree; as written when it cannot be resolved (gone, or in iCloud Drive).
func (n *Node) catalogDir(cwd string) string {
	if path := n.resolvePath(cwd, ""); path != "" && !inICloud(path) {
		if real, err := filepath.EvalSymlinks(path); err == nil && !inICloud(real) {
			return real
		}
	}
	return cwd
}

// runtimeWorkspaceRows are runtime h's workspaces as catalog rows (proto.Workspace): the source is the runtime (the
// host kind its sessions name), the project its space, the main checkout the one main workspace of a repository.
func (n *Node) runtimeWorkspaceRows(h *runtimeConn) []proto.Workspace {
	rt := h.id()
	list := h.workspacesCopy()
	out := make([]proto.Workspace, 0, len(list))
	for _, w := range list {
		row := proto.Workspace{ID: w.ID, Source: rt, SourceID: w.ID, Title: firstOf(w.Title, w.Branch, filepath.Base(w.Cwd)),
			Cwd: n.catalogDir(w.Cwd), Branch: w.Branch, Online: true, Available: true, ProjectID: w.SpaceID, ProjectName: w.SpaceName}
		if w.RepoRoot != "" {
			row.ProjectPath = n.catalogDir(w.RepoRoot)
		}
		switch w.Kind {
		case workspaceCheckout, workspaceWorktree:
			main := w.Kind == workspaceCheckout
			row.IsMain, row.WorkspaceKind = &main, "git"
			row.Owned = w.Kind == workspaceWorktree && w.Owned // the worktrees Delete worktree is offered for (#6297)
		default:
			row.WorkspaceKind = workspaceFolder
		}
		out = append(out, row)
	}
	return out
}

// isRuntimeWorkspace: id is a workspace of a connected runtime's, not a terminal host's (a Paseo session's
// workspace_id, which a client may send where a terminal host's workspace goes).
func (n *Node) isRuntimeWorkspace(id string) bool {
	if id == "" || n.runtimes == nil {
		return false
	}
	for _, h := range n.runtimes.ready() {
		if h.makesWorkspaces() {
			if _, ok := h.workspaceByID(id); ok {
				return true
			}
		}
	}
	return false
}

// worktreeRuntime is the engine runtime that makes the worktrees of kind (the machine's default host when ""). runtime
// is false when kind is a terminal host: worktree.go makes them. A runtime engine that cannot is an error: away (not
// connected), or keeping no workspaces (worktreeUnsupported, which the hub reads as "run an inferred task in its folder").
func (n *Node) worktreeRuntime(ctx context.Context, kind string) (h *runtimeConn, runtime bool, err error) {
	h, engine, runtime := n.runtimeEngine(kind)
	switch {
	case !runtime:
		return nil, false, nil
	case h == nil:
		return nil, true, composerError(ctx, "runtime_away", i18n.Args{"engine": engineLabel(engine)})
	case !h.makesWorkspaces():
		return nil, true, worktreeUnsupported(engine)
	}
	return h, true, nil
}

// engineLabel is what the apps call an engine.
func engineLabel(kind string) string {
	if name := proto.EngineNames[kind]; name != "" {
		return name
	}
	return kind
}

// runtimeSource is where a runtime's new worktree comes from: the workspace named (its id and folder), else a folder
// (the assistant and a new agent know folders): its repository's top level. main is the repository's main checkout.
type runtimeSource struct {
	workspace, dir, main string
	repo                 worktreeRepo
}

// runtimeWorktreeSource finds the source of a new worktree: one of h's workspaces by id, or the repository cwd is in.
func (n *Node) runtimeWorktreeSource(ctx context.Context, h *runtimeConn, workspace, cwd string) (runtimeSource, error) {
	var src runtimeSource
	if workspace != "" {
		w, ok := h.workspaceByID(workspace)
		if !ok {
			return src, composerError(ctx, "runtime_workspace_missing", i18n.Args{"engine": h.label()})
		}
		src.workspace, src.dir = w.ID, w.Cwd
	} else {
		dir := proto.ExpandHome(strings.TrimSpace(cwd), n.home)
		if dir == "" || !filepath.IsAbs(dir) {
			return src, composerError(ctx, "runtime_source_required")
		}
		src.dir = filepath.Clean(dir)
	}
	repo, err := repoAt(ctx, src.dir)
	if err != nil {
		return src, err
	}
	if src.workspace == "" {
		src.dir = repo.root // a folder's worktree is its repository's, as a terminal host's (repoOfWorkspace)
	}
	src.repo, src.main = repo, mainCheckoutRoot(ctx, repo.root)
	return src, nil
}

// mainCheckoutRoot is the main checkout of the repository dir is in, as its git common directory names it (a linked
// worktree's repository is the checkout whose .git that is; a bare repository is its own directory); physical. ""
// when git cannot say.
func mainCheckoutRoot(ctx context.Context, dir string) string {
	common, err := absGit(ctx, dir, "--git-common-dir")
	if err != nil {
		return ""
	}
	if filepath.Base(common) == ".git" {
		return filepath.Dir(common)
	}
	return common
}

// runtimeErr is a runtime's refusal as it is (its code survives the wire), anything else said as the runtime's.
func runtimeErr(h *runtimeConn, err error) error {
	if err == nil || isRPCError(err) {
		return err
	}
	return fmt.Errorf("%s: %w", h.label(), err)
}

// runtimeCheckouts is the folder runtime h puts the worktrees of src's repository in (workspace.prepare).
func (n *Node) runtimeCheckouts(ctx context.Context, h *runtimeConn, src runtimeSource) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, runtimeCallTimeout)
	defer cancel()
	var res struct {
		Checkouts string `json:"checkouts"`
	}
	if err := h.peer.Call(cctx, "workspace.prepare", map[string]any{"cwd": src.dir, "repo_root": firstOf(src.main, src.repo.root)}, &res); err != nil {
		return "", runtimeErr(h, err)
	}
	if c := strings.TrimSpace(res.Checkouts); filepath.IsAbs(c) {
		return filepath.Clean(c), nil
	}
	return "", composerError(ctx, "runtime_prepare", i18n.Args{"engine": h.label()})
}

// runtimeWorktreePrepare is worktree.prepare on a runtime engine: the repository, the branch its checkout is on, and
// the folder the runtime puts its worktrees in (where the dialog's folder goes; only its name can be chosen).
func (n *Node) runtimeWorktreePrepare(ctx context.Context, h *runtimeConn, workspace, cwd string) (proto.WorktreePrepare, error) {
	ctx, cancel := context.WithTimeout(ctx, runtimePrepareDeadline)
	defer cancel()
	src, err := n.runtimeWorktreeSource(ctx, h, workspace, cwd)
	if err != nil {
		return proto.WorktreePrepare{}, err
	}
	if err = requireCommit(ctx, src.repo); err != nil {
		return proto.WorktreePrepare{}, err
	}
	checkouts, err := n.runtimeCheckouts(ctx, h, src)
	if err != nil {
		return proto.WorktreePrepare{}, err
	}
	return proto.WorktreePrepare{Root: src.repo.root, RepoName: src.repo.name, Branch: src.repo.branch, Path: checkouts,
		AutomaticName: true}, nil
}

// runtimeWorktreeCreate is worktree.create on a runtime engine: the checks a terminal host's worktree gets
// (worktreeCreate), then the runtime makes it, in its own folder: the path typed must be in that folder, its name is
// the worktree's folder name, as the runtime takes a name (runtimeFolder).
func (n *Node) runtimeWorktreeCreate(ctx context.Context, h *runtimeConn, workspace, cwd, branch, path, parent string) (proto.WorktreeCreateResult, error) {
	var res proto.WorktreeCreateResult
	ctx, cancel := context.WithTimeout(ctx, runtimeWorktreeDeadline)
	defer cancel()
	branch = strings.TrimSpace(branch)
	src, err := n.runtimeWorktreeSource(ctx, h, workspace, cwd)
	if err != nil {
		return res, err
	}
	if parent != "" && parent != src.workspace {
		return res, composerError(ctx, "runtime_parent", i18n.Args{"engine": h.label()})
	}
	if err = requireCommit(ctx, src.repo); err != nil {
		return res, err
	}
	if err = checkBranch(ctx, src.repo.root, branch); err != nil {
		return res, err
	}
	checkouts, err := n.runtimeCheckouts(ctx, h, src)
	if err != nil {
		return res, err
	}
	dest, err := n.checkoutPath(src.repo.root, path)
	if err != nil {
		return res, err
	}
	if realPath(filepath.Dir(dest)) != realPath(checkouts) {
		return res, composerError(ctx, "runtime_folder", i18n.Args{"engine": h.label(), "path": checkouts})
	}
	name, err := n.runtimeFolder(ctx, h, src.repo.root, checkouts, filepath.Base(dest))
	if err != nil {
		return res, err
	}
	w, err := n.runtimeNewWorktree(ctx, h, src, branch, name)
	if err != nil {
		return res, err
	}
	return proto.WorktreeCreateResult{WorkspaceID: w.ID, Path: n.catalogDir(w.Cwd), Branch: firstOf(w.Branch, branch)}, nil
}

// runtimeFolderName is the folder name a runtime engine gives a worktree it is asked to name name (Paseo's slugify):
// lowercase, each run of anything but a-z and 0-9 one hyphen, none at either end; past runtimeSlugMax characters it is
// cut at its last hyphen beyond the middle, else at runtimeSlugMax with the hyphens that end it dropped. "" when
// nothing is left, a name the runtime refuses.
func runtimeFolderName(name string) string {
	// JavaScript lowercases U+0130 (I with a dot above) to an i and a combining dot, Go to the i alone: the dot ends the
	// run, as there.
	slug := herdr.WorktreeSlug(strings.ReplaceAll(name, "İ", "i̇"))
	if len(slug) <= runtimeSlugMax {
		return slug
	}
	cut := slug[:runtimeSlugMax]
	if i := strings.LastIndexByte(cut, '-'); i > runtimeSlugMax/2 {
		return cut[:i]
	}
	return strings.TrimRight(cut, "-")
}

// runtimeFolder is the folder name sent to runtime h for a worktree named name, in checkouts (its folder for the
// repository whose top level is root): name as the runtime takes it (runtimeFolderName), so the checks are made on the
// folder the runtime uses and the new worktree's path is known. Refused when nothing of the name is left, and when
// anything is at that path, an empty folder too: the runtime would not use it but put the worktree beside it (Paseo:
// <name>-1).
func (n *Node) runtimeFolder(ctx context.Context, h *runtimeConn, root, checkouts, name string) (string, error) {
	folder := runtimeFolderName(name)
	if folder == "" {
		return "", composerError(ctx, "runtime_folder_name", i18n.Args{"engine": h.label(), "name": name})
	}
	dest := filepath.Join(checkouts, folder)
	if _, err := os.Lstat(dest); err == nil {
		return "", composerError(ctx, "runtime_folder_taken", i18n.Args{"engine": h.label(), "path": dest})
	}
	if _, err := n.checkoutPath(root, dest); err != nil {
		return "", err
	}
	return folder, nil
}

// runtimeWorktreeCreateAutomatic is worktree.create with automatic naming on a runtime engine: the runtime names the
// branch and the folder, the branch made from the source checkout's HEAD.
func (n *Node) runtimeWorktreeCreateAutomatic(ctx context.Context, h *runtimeConn, workspace, cwd, parent string) (proto.WorktreeCreateResult, error) {
	var res proto.WorktreeCreateResult
	ctx, cancel := context.WithTimeout(ctx, runtimeWorktreeDeadline)
	defer cancel()
	src, err := n.runtimeWorktreeSource(ctx, h, workspace, cwd)
	if err != nil {
		return res, err
	}
	if parent != "" && parent != src.workspace {
		return res, composerError(ctx, "runtime_parent", i18n.Args{"engine": h.label()})
	}
	if err = requireCommit(ctx, src.repo); err != nil {
		return res, err
	}
	w, err := n.runtimeNewWorktree(ctx, h, src, "", "")
	if err != nil {
		return res, err
	}
	return proto.WorktreeCreateResult{WorkspaceID: w.ID, Path: n.catalogDir(w.Cwd), Branch: w.Branch}, nil
}

// runtimeNewWorktree has runtime h make a worktree of src's repository (workspace.create) on branch, in a folder named
// name (runtimeFolder): a branch that exists is checked out as it is, never one checked out elsewhere (git would
// refuse, and Paseo would make another branch of it instead); a new one starts at src's HEAD, as a terminal host's
// does; no branch and no name: the runtime names both. The workspace it made is listed at once. ctx carries the
// caller's deadline (runtimeWorktreeDeadline), which the runtime's work shares with the checks before it.
func (n *Node) runtimeNewWorktree(ctx context.Context, h *runtimeConn, src runtimeSource, branch, name string) (runtimeWorkspace, error) {
	var key [12]byte
	if _, err := rand.Read(key[:]); err != nil {
		return runtimeWorkspace{}, err
	}
	params := map[string]any{"cwd": src.dir, "key": "persona-" + hex.EncodeToString(key[:])}
	if src.workspace != "" {
		params["workspace_id"] = src.workspace
	}
	create := true
	if branch != "" {
		params["branch"] = branch
		if branchExists(ctx, src.repo.root, branch) {
			at, err := branchCheckedOutAt(ctx, src.repo.root, branch)
			if err != nil {
				return runtimeWorkspace{}, errors.New(gitFailure(err))
			}
			if at != "" {
				return runtimeWorkspace{}, composerError(ctx, "branch_taken", i18n.Args{"branch": branch, "path": at})
			}
			create = false
		}
	}
	params["create_branch"] = create
	if create {
		base, err := headOf(ctx, src.repo)
		if err != nil {
			return runtimeWorkspace{}, err
		}
		params["base"] = base
	}
	if name != "" {
		params["name"] = name
	}
	var res struct {
		Workspace runtimeWorkspace `json:"workspace"`
	}
	if err := h.peer.Call(ctx, "workspace.create", params, &res); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return runtimeWorkspace{}, composerError(ctx, "runtime_slow", i18n.Args{"engine": h.label()})
		}
		return runtimeWorkspace{}, runtimeErr(h, err)
	}
	made := cleanWorkspaces([]runtimeWorkspace{res.Workspace})
	if len(made) == 0 {
		return runtimeWorkspace{}, composerError(ctx, "runtime_unconfirmed", i18n.Args{"engine": h.label()})
	}
	h.keepWorkspace(made[0])
	return made[0], nil
}

// headOf is what a new branch of repo starts from: the branch its checkout is on, else (a detached HEAD) its commit.
func headOf(ctx context.Context, repo worktreeRepo) (string, error) {
	if repo.branch != "" {
		return repo.branch, nil
	}
	out, err := git(ctx, repo.root, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", errors.New(gitFailure(err))
	}
	return strings.TrimSpace(out), nil
}

// runtimeWorktreeAdd is worktree.add (a new agent's own worktree, #6115) on a runtime engine: the same branch, made
// unique the same way, in a folder of the runtime's named as a terminal host's would be, as the runtime takes a name
// (runtimeFolder); the runtime makes it. A name whose folder is there already, empty or not, is taken; the -2, -3… of
// a name too long for the runtime's folders shorten the name, not the number, so that each is a folder of its own.
func (n *Node) runtimeWorktreeAdd(ctx context.Context, h *runtimeConn, cwd, branch, nameHint string) (proto.WorktreeAdded, error) {
	var res proto.WorktreeAdded
	ctx, cancel := context.WithTimeout(ctx, runtimeWorktreeDeadline)
	defer cancel()
	dir := proto.ExpandHome(strings.TrimSpace(cwd), n.home)
	if dir == "" || !filepath.IsAbs(dir) {
		return res, refusal("not_a_repository", "the folder is not in a Git repository", nil)
	}
	repo, err := repoAt(ctx, dir)
	if err != nil {
		return res, refusal("not_a_repository", "the folder is not in a Git repository", nil)
	}
	src := runtimeSource{dir: repo.root, repo: repo, main: mainCheckoutRoot(ctx, repo.root)}
	base, err := n.runtimeCheckouts(ctx, h, src)
	if err != nil {
		return res, refusal("worktree_failed", err.Error(), nil)
	}
	type candidate struct{ branch, folder string }
	branch = strings.TrimSpace(branch)
	candidates := []candidate{{branch, runtimeFolderName(herdr.WorktreeDirName(branch))}}
	if branch == "" {
		own := agentBranch(nameHint)
		folder := runtimeFolderName(herdr.WorktreeDirName(own))
		candidates = []candidate{{own, folder}}
		for i := 2; i <= 50; i++ {
			suffix := fmt.Sprintf("-%d", i)
			candidates = append(candidates, candidate{own + suffix, strings.TrimRight(folder[:min(len(folder), runtimeSlugMax-len(suffix))], "-") + suffix})
		}
	}
	for _, c := range candidates {
		b, name := c.branch, c.folder
		if _, err := os.Lstat(filepath.Join(base, name)); len(candidates) > 1 && (branchExists(ctx, repo.root, b) || err == nil) {
			continue // the agent's own name is taken: the next
		}
		if err := checkBranch(ctx, repo.root, b); err != nil {
			return res, err
		}
		if name, err = n.runtimeFolder(ctx, h, repo.root, base, name); err != nil {
			return res, refusal("worktree_failed", err.Error(), nil)
		}
		w, err := n.runtimeNewWorktree(ctx, h, src, b, name)
		if err != nil {
			return res, refusal("worktree_failed", err.Error(), nil)
		}
		return proto.WorktreeAdded{Path: n.catalogDir(w.Cwd), Branch: firstOf(w.Branch, b), Root: repo.root, WorkspaceID: w.ID}, nil
	}
	return res, noAgentBranch(ctx)
}

// runtimeCheckoutsOf is where runtime h puts the worktrees of the repository dir is the top level of, "" when it
// cannot say (for worktree.status, which says it when it can).
func (n *Node) runtimeCheckoutsOf(ctx context.Context, h *runtimeConn, dir string) string {
	if h == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, runtimeStatusDeadline)
	defer cancel()
	c, err := n.runtimeCheckouts(ctx, h, runtimeSource{dir: dir, main: mainCheckoutRoot(ctx, dir), repo: worktreeRepo{root: dir}})
	if err != nil {
		return ""
	}
	return c
}
