package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
)

// Task workspaces (#4786): a leader and its workers share a herdr workspace or tabs of an Orca worktree. The node
// records that ownership with every session it starts, so a direct resume keeps the placement and workspace.close can
// prove both IDs before closing anything. Other terminal hosts and runtimes ignore task placement as before.

type taskSpaceKey struct{}
type reopenWorktreeWorkspaceKey struct{}

type taskSpace struct {
	task, id, label string
	adopted         bool
	mu              sync.Mutex
	got             string
}

func withTaskSpace(ctx context.Context, task, id, label string) (context.Context, *taskSpace) {
	if task == "" {
		return ctx, nil
	}
	ts := &taskSpace{task: task, id: id, label: label}
	return context.WithValue(ctx, taskSpaceKey{}, ts), ts
}

func taskSpaceIn(ctx context.Context, launch host.Launcher) *taskSpace {
	ts, _ := ctx.Value(taskSpaceKey{}).(*taskSpace)
	switch launch.(type) {
	case *herdrHost, *orcaHost:
		return ts
	default:
		return nil
	}
}

func (ts *taskSpace) opened(workspace string) {
	if ts == nil || workspace == "" {
		return
	}
	ts.mu.Lock()
	ts.got = workspace
	ts.mu.Unlock()
}

func (ts *taskSpace) reply() map[string]any {
	if ts == nil {
		return nil
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.got == "" {
		return nil
	}
	return map[string]any{"task_id": ts.task, "workspace_id": ts.got, "label": ts.label}
}

func withTaskSpaceReply(res any, ts *taskSpace) any {
	m, ok := res.(map[string]any)
	if !ok {
		return res
	}
	if workspace := ts.reply(); workspace != nil {
		m["task_workspace"] = workspace
	}
	return res
}

func (ts *taskSpace) current() string {
	if ts == nil {
		return ""
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.got != "" {
		return ts.got
	}
	return ts.id
}

// putAgentState persists task ownership with every kind of terminal Agent. An adopted or moved shell does not
// transfer that ownership to its user workspace: only a tab this start actually created updates ts.got.
func (n *Node) putAgentState(ctx context.Context, launch host.Launcher, id string, state sessionState) {
	if ts := taskSpaceIn(ctx, launch); ts != nil {
		state.TaskID = ts.task
		if _, ok := launch.(*orcaHost); ok {
			state.WorkspaceHost = host.Orca
		}
		previous, hadPrevious := n.state.get(id)
		switch {
		case ts.adopted && hadPrevious && previous.WorkspaceHost == state.WorkspaceHost && previous.TaskID == state.TaskID:
			// A kept or user-picked shell does not move the task to its current user workspace.
			state.WorkspaceID, state.WorkspaceLabel = previous.WorkspaceID, previous.WorkspaceLabel
		case ts.adopted:
			state.WorkspaceID, state.WorkspaceLabel = "", ts.label
		default:
			state.WorkspaceID = ts.current()
			state.WorkspaceLabel = ts.label
			if hadPrevious && previous.WorkspaceHost == state.WorkspaceHost && previous.TaskID == state.TaskID {
				if state.WorkspaceID == "" {
					state.WorkspaceID = previous.WorkspaceID
				}
				if state.WorkspaceLabel == "" {
					state.WorkspaceLabel = previous.WorkspaceLabel
				}
			}
		}
	}
	if h, ok := launch.(*orcaHost); ok {
		state.OrcaTerminal = h.claimTerminalOwner(state.Pane, id)
	}
	n.state.put(id, state)
}

// tabSpace keeps ordinary directory placement unless a classic herdr or Orca task supplies its placement. A recorded task workspace
// is selected by ID, never by a directory basename or a mutable label. If it is gone, the same task label is recreated.
func (n *Node) tabSpace(ctx context.Context, launch host.Launcher, cwd, asked string) (string, error) {
	ts := taskSpaceIn(ctx, launch)
	if backend := backendLaunchFrom(ctx); backend != nil && (!backend.taskDispatch || ts == nil) {
		return "", nil
	}
	if ts == nil {
		if asked != "" {
			if _, ok := launch.(*herdrHost); ok {
				// An explicit workspace start brings back a stopped herdr server, as before.
				_ = n.ensureHerdr(ctx, "start a session in a workspace")
			}
			return asked, nil
		}
		return n.workspaceFor(ctx, launch, cwd)
	}
	if _, orca := launch.(*orcaHost); orca {
		// Orca resolves the first tab by checkout and all subsequent tabs by the recorded worktree ID.
		return ts.id, nil
	}
	_ = n.ensureHerdr(ctx, "start a session in a task workspace")
	if ts.id == "" {
		if ts.label == "" {
			return "", errors.New("workspace_label is required for a new task workspace")
		}
		return "", nil
	}
	spaces, err := launch.Spaces(ctx)
	if err != nil {
		return "", err
	}
	for _, space := range spaces {
		if space.ID == ts.id {
			if space.Label != "" {
				ts.label = space.Label
			}
			return space.ID, nil
		}
	}
	if ts.label == "" {
		return "", errors.New("the recorded task workspace is gone and its label is unknown")
	}
	return "", nil
}

// A classic herdr workspace disappears when its last tab closes. An allocated task worktree still exists on disk.
// Before any command was typed, retry placement by the exact checkout directory on the same herdr server.
func (n *Node) reopenWorktreeTab(ctx context.Context, h *herdrHost, staleID, cwd, tabName string) (*host.Created, error) {
	target, err := h.target(ctx, staleID)
	if err != nil {
		return nil, err
	}
	dir, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("the task's worktree path is no longer a directory")
	}
	defer n.typing.lock("herdr-worktree:" + target.session + "\x00" + dir)()
	spaces, err := target.c.Workspaces(ctx)
	if err != nil {
		return nil, err
	}
	for _, space := range spaces {
		if (space.Cwd != "" && realPath(space.Cwd) == dir) ||
			(space.Worktree != nil && space.Worktree.CheckoutPath != "" && realPath(space.Worktree.CheckoutPath) == dir) {
			id := target.qualify(space.ID)
			created, err := createStartSlot(ctx, h, id, cwd, tabLabel(tabName, n.tabLabels(ctx, h, id)))
			if err == nil && created.SpaceID == "" {
				created.SpaceID = id
			}
			return created, err
		}
	}
	c, err := target.c.WorkspaceCreate(ctx, cwd, filepath.Base(cwd), startShellEnv...)
	if err != nil {
		return nil, err
	}
	created := fromHerdrCreated(target.session, c)
	if created.SlotID != "" {
		_ = renameSlot(ctx, h, created.SlotID, tabLabel(tabName, nil))
	}
	return created, nil
}

const (
	notTaskSpace      = "not_task_workspace"
	taskSpaceBusyCode = "task_workspace_busy"
)

// closeTaskSpace closes only the exact workspace the node recorded for this task. A rename does not transfer
// ownership; a user workspace with the same label, another task's workspace, and an unrecorded ID are all refused.
func (n *Node) closeTaskSpace(ctx context.Context, task, id string) (map[string]any, error) {
	if task == "" || id == "" {
		return nil, errors.New("task_id and workspace_id are required")
	}
	h, ok := n.hosts.Get(host.Herdr).(*herdrHost)
	if session, _ := herdr.SplitID(id); !ok || session != "" {
		return nil, refusal(notTaskSpace, "only a task workspace on this computer's own herdr is closed", nil)
	}
	n.state.mu.Lock()
	owned := false
	for _, state := range n.state.Sessions {
		if state.WorkspaceHost == "" && state.TaskID == task && state.WorkspaceID == id {
			owned = true
			break
		}
	}
	n.state.mu.Unlock()
	if !owned {
		return nil, refusal(notTaskSpace, "the workspace is not the one this node recorded for the task", nil)
	}
	layout, err := h.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	found := false
	for _, space := range layout.Spaces {
		found = found || space.ID == id
	}
	if !found {
		return map[string]any{"closed": false, "gone": true}, nil
	}
	busy := false
	for _, agent := range layout.Agents {
		busy = busy || agent.WorkspaceID == id && agent.PaneID != ""
	}
	for _, pane := range layout.Panes {
		busy = busy || pane.Agent != "" && pane.WorkspaceID == id && pane.PaneID != ""
	}
	if busy {
		return nil, refusal(taskSpaceBusyCode, fmt.Sprintf("task workspace %s still has a live Agent", id), nil)
	}
	if err := h.c.WorkspaceClose(ctx, id); err != nil {
		if herdr.IsNotFound(err) {
			return map[string]any{"closed": false, "gone": true}, nil
		}
		return nil, err
	}
	_ = n.refreshHosts(ctx, h)
	return map[string]any{"closed": true}, nil
}
