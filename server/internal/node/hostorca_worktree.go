package node

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/orcart"
)

// "New worktree…" on Orca (docs/integrations/terminal.md): Orca makes the checkout itself (worktree.create), so the new worktree is
// one of Orca's, listed under the workspace it came from, in Orca's sidebar and in Persona's workspaces alike.

var _ host.Worktreer = (*orcaHost)(nil)

// CanWorktree: every Orca this node speaks to has worktree.create (its CLI's `orca worktree create` is that call).
func (h *orcaHost) CanWorktree() bool { return true }

// WorktreesDir is Orca's default workspace directory: Orca puts a repository's worktrees in <dir>/<repo>/<name>. The
// runtime does not report the setting; when it was changed, Orca still uses its own and the result says where.
func (h *orcaHost) WorktreesDir(home string) string { return filepath.Join(home, "orca", "workspaces") }

// orcaWorktreeTerminalWait is how long CreateWorktree looks for the terminal Orca opens in a new worktree.
var orcaWorktreeTerminalWait = 5 * time.Second

// CreateWorktree asks Orca for a worktree of space's repository on branch, named for path's last element: branch is
// checked out when it exists locally, else made from the space's HEAD (its branch, or the commit when detached), the
// herdr way. Orca does not fail where git would: a branch checked out elsewhere, or a folder that is taken (an empty
// one too), gets a "-2" branch and folder instead (Orca 1.4.250). So both are refused here before Orca is asked, and
// an answer on another branch (taken meanwhile, or a folder setting of Orca's own that is taken) is an error. So is a
// directory-level workspace in an existing checkout, Orca's answer when the checkout is open as an Orca worktree
// (orcaDirectoryWorkspace): no worktree and no branch.
func (h *orcaHost) CreateWorktree(ctx context.Context, space, branch, path string) (*host.Created, error) {
	return h.CreateWorktreeUnder(ctx, space, branch, path, space)
}

// CreateWorktreeUnder keeps the Git source separate from the sidebar parent, which can belong to another project.
func (h *orcaHost) CreateWorktreeUnder(ctx context.Context, space, branch, path, parent string) (*host.Created, error) {
	return h.createWorktree(ctx, space, branch, path, parent, false)
}

// These seeds belong to Orca's generated creature-name pool. Orca applies its retired-name registry, branch prefix
// and collision retries; omitting branchNameOverride keeps the branch eligible for its first-work rename.
var orcaGeneratedNames = [...]string{"nautilus", "seahorse", "starfish", "coral", "narwhal", "jellyfish", "octopus", "manta",
	"dolphin", "manatee", "cuttlefish", "anemone", "urchin", "triton", "nudibranch", "coelacanth"}

func (h *orcaHost) CreateGeneratedWorktree(ctx context.Context, space, parent string) (*host.Created, error) {
	return h.createWorktree(ctx, space, "", "", parent, true)
}

func (h *orcaHost) createWorktree(ctx context.Context, space, branch, path, parent string, automatic bool) (*host.Created, error) {
	trees, _, _, err := h.c.Worktrees(ctx, "")
	if err != nil {
		return nil, orcaErr(err)
	}
	var src *orcart.Worktree
	for i := range trees {
		if w := &trees[i]; w.WorktreeID == space && (w.HostID == "" || w.HostID == "local") {
			src = w
			break
		}
	}
	if src == nil {
		return nil, composerError(ctx, "workspace_missing")
	}
	parentFound := false
	for _, w := range trees {
		if w.WorktreeID == parent && (w.HostID == "" || w.HostID == "local") {
			parentFound = true
			break
		}
	}
	if !parentFound {
		return nil, composerError(ctx, "parent_missing")
	}
	repoID, _, ok := strings.Cut(space, "::") // Orca's worktree ids are <repo id>::<path>
	if !ok || repoID == "" {
		return nil, composerError(ctx, "project_missing")
	}
	if !automatic {
		if at, err := branchCheckedOutAt(ctx, src.Path, branch); err != nil {
			return nil, composerError(ctx, "worktree_list_unreadable", i18n.Args{"reason": err.Error()})
		} else if at != "" {
			return nil, composerError(ctx, "branch_taken", i18n.Args{"branch": branch, "path": at})
		}
		if _, err := os.Lstat(path); err == nil {
			return nil, composerError(ctx, "orca_path_exists")
		}
	}
	req := orcart.WorktreeCreate{Repo: "id:" + repoID, Name: filepath.Base(path), Branch: branch, Parent: "id:" + parent}
	if automatic {
		var seed [1]byte
		if _, err := rand.Read(seed[:]); err != nil {
			return nil, err
		}
		req.Name, req.Generated = orcaGeneratedNames[int(seed[0])%len(orcaGeneratedNames)], true
	}
	_, branchErr := git(ctx, src.Path, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if automatic || branchErr != nil {
		base, err := git(ctx, src.Path, "branch", "--show-current")
		if base = strings.TrimSpace(base); err != nil || base == "" {
			if base, err = git(ctx, src.Path, "rev-parse", "HEAD"); err != nil {
				return nil, composerError(ctx, "head_unreadable", i18n.Args{"reason": err.Error()})
			}
		}
		req.Base = strings.TrimSpace(base)
	}
	wt, err := h.c.CreateWorktree(ctx, req)
	if err != nil {
		return nil, orcaErr(err)
	}
	if orcaDirectoryWorkspace(wt, src.Path) {
		// No worktree at all: when the checkout is itself open as an Orca worktree, Orca answers with a directory-level
		// workspace in an existing checkout, without git worktree add or a branch (#13363). No branch or folder was
		// taken, so the error says what Orca did, and that the task can run without a checkout of its own. Nothing
		// falls back to that by itself: a task that asked for its own checkout does not quietly share one.
		return nil, composerError(ctx, "orca_directory_workspace", i18n.Args{"path": wt.Path})
	}
	got := strings.TrimPrefix(wt.Branch, "refs/heads/")
	if automatic && got == "" {
		return nil, composerError(ctx, "orca_branch_missing")
	}
	if !automatic && got != branch {
		// A real worktree on another branch, made all the same (nothing is deleted): the user sees what Orca did
		// instead. A directory-level workspace, with no branch, is told apart above.
		return nil, composerError(ctx, "orca_branch_changed", i18n.Args{"branch": branch, "got": got, "path": wt.Path})
	}
	c := &host.Created{SpaceID: wt.ID, Path: wt.Path, Branch: got}
	if t := h.terminalIn(ctx, wt.ID); t != nil {
		h.mu.Lock()
		h.track(*t) // found by its handle if Orca re-keys it (#5547)
		h.mu.Unlock()
		c.SlotID, c.Pane = t.TabID, host.PaneRef{Kind: host.Orca, ID: t.PaneKey()}
	}
	return c, nil
}

// orcaDirectoryWorkspace: Orca's answer to worktree.create is a directory-level workspace, not a new checkout. Orca
// answers ok with one when the repository's checkout is itself open as an Orca worktree (#13363): its id is
// "<repo id>::<path>::workspace:<uuid>", its path that existing checkout's, its branch and head empty. Either sign
// is enough: the id in that shape around the path Orca returned (a real worktree's id is "<repo id>::<path>", and its
// path may hold "::workspace:" itself), or the source's own checkout back as the path (a new worktree is never there),
// spelled through a link or not.
func orcaDirectoryWorkspace(wt *orcart.CreatedWorktree, source string) bool {
	if wt.Path == "" {
		return false
	}
	marker := "::" + wt.Path + "::workspace:"
	if i := strings.LastIndex(wt.ID, marker); i > 0 && isUUID(wt.ID[i+len(marker):]) {
		return true
	}
	return source != "" && (filepath.Clean(wt.Path) == filepath.Clean(source) || sameFile(wt.Path, source))
}

// isUUID: s is a UUID's text form, 8-4-4-4-12 hex digits.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// branchCheckedOutAt is the worktree of dir's repository that has branch checked out, "" when none does.
func branchCheckedOutAt(ctx context.Context, dir, branch string) (string, error) {
	out, err := git(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	var at string
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			at = p
		} else if line == "branch refs/heads/"+branch {
			return at, nil
		}
	}
	return "", nil
}

// terminalIn is the local terminal Orca opened in worktree, waiting up to orcaWorktreeTerminalWait for it; nil when
// none shows up (the worktree is there all the same).
func (h *orcaHost) terminalIn(ctx context.Context, worktree string) *orcart.Terminal {
	deadline := time.Now().Add(orcaWorktreeTerminalWait)
	for {
		if l, err := h.c.Terminals(ctx, false); err == nil {
			for _, t := range l.Terminals {
				if t.WorktreeID == worktree && t.IsLocal() && !t.Orphaned && t.TabID != "" {
					return &t
				}
			}
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
}
