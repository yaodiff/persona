package node

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/taskworkspace"
)

type backendLaunchKey struct{}
type backendLaunch struct {
	executor     *workspaceExecutor
	use          string
	taskDispatch bool
}

func backendLaunchFrom(ctx context.Context) *backendLaunch {
	b, _ := ctx.Value(backendLaunchKey{}).(*backendLaunch)
	return b
}

// Persist the intent before the first command can reach the terminal or runtime.
// A lost response or a crashed Node cannot authorize a second command.
func (b *backendLaunch) beforeDispatch(expectedID string) error {
	if b.executor.journal.Launch != nil {
		return nil // A resume keeps the original session and use.
	}
	b.executor.journal.Launch = &taskworkspace.Launch{UseID: b.use, ExpectedSessionID: expectedID, TaskDispatch: b.taskDispatch}
	return b.executor.save()
}

func (e *workspaceExecutor) startSession(ctx context.Context, req taskworkspace.Request, params json.RawMessage) (any, error) {
	j := &e.journal
	problem := func(code string) (any, error) {
		return taskworkspace.Record{Error: taskworkspace.Failure(code, "start", j.OperationID, "", j.Plan.RootPath)}, nil
	}
	if !strings.HasPrefix(req.UseID, "launch_") || j.State != "ready" || j.Cleanup != nil || !slices.Contains(j.Uses, req.UseID) {
		return problem("not_ready")
	}
	var in struct {
		Cwd       string `json:"cwd"`
		Resume    string `json:"resume_session_id"`
		Workspace string `json:"workspace_id"`
		InPane    string `json:"in_pane"`
		Task      string `json:"task_id"`
		Launcher  string `json:"launcher"`
		Async     bool   `json:"async"`
	}
	if err := json.Unmarshal(params, &in); err != nil {
		return nil, err
	}
	if in.Cwd != j.Plan.RootPath || in.Resume != "" || in.InPane != "" || in.Async ||
		!req.TaskDispatch && (in.Workspace != "" || in.Task != "") {
		return problem("invalid_input")
	}
	if j.Launch != nil {
		if j.Launch.UseID == req.UseID {
			if observed := e.observedLaunch(ctx); observed != nil {
				return map[string]any{"session": observed}, nil
			}
		}
		return problem("operation_unknown")
	}
	if !e.owned(j.Plan.RootPath) {
		return problem("identity_mismatch")
	}
	for i := range j.Repositories {
		repo := &j.Repositories[i]
		if code := e.inspect(ctx, repo); code != "" {
			return taskworkspace.Record{Error: taskworkspace.Failure(code, "start", j.OperationID, repo.RepoID, repo.Path)}, nil
		}
	}
	if !req.TaskDispatch && (isDesktopLauncher(in.Launcher) || isCodexAppLauncher(in.Launcher)) {
		return problem("unsupported")
	}
	b := &backendLaunch{executor: e, use: req.UseID, taskDispatch: req.TaskDispatch}
	ctx = context.WithValue(ctx, backendLaunchKey{}, b)
	out, err := e.n.handleMethod(ctx, "session.start", params)
	if err != nil {
		if p, ok := errors.AsType[*taskworkspace.Problem](err); ok {
			return taskworkspace.Record{Error: p}, nil
		}
		if rpcCode(err) == "backend_workspace_unsupported" {
			return problem("unsupported")
		}
		if b.taskDispatch && j.Launch != nil && j.Launch.SessionID != "" {
			return nil, err // Desktop creation can succeed before its prompt is accepted.
		}
		if j.Launch != nil && (startFailedForSure(err) || rpcCode(err) == "backend_workspace_start_refused") {
			j.Launch = nil
			if saveErr := e.save(); saveErr != nil {
				return nil, saveErr
			}
			return problem("engine_start_failed")
		}
		if j.Launch != nil {
			if observed := e.observedLaunch(ctx); observed != nil {
				return map[string]any{"session": observed}, nil
			}
			return problem("operation_unknown")
		}
		return nil, err
	}
	reply, _ := out.(map[string]any)
	s, _ := reply["session"].(*proto.Session)
	if s == nil || s.SessionID == "" {
		return problem("operation_unknown")
	}
	if j.Launch == nil {
		return problem("operation_unknown")
	}
	if err := e.recordLaunchSession(s.SessionID); err != nil {
		return nil, err
	}
	if filepath.Clean(s.Cwd) != j.Plan.RootPath {
		return problem("result_mismatch")
	}
	if s.StartError != "" {
		return problem("engine_start_failed")
	}
	if s.Starting && !b.taskDispatch {
		return problem("operation_unknown")
	}
	return out, nil
}

func (e *workspaceExecutor) recordLaunchSession(id string) error {
	if err := e.rememberLaunch(id); err != nil {
		return err
	}
	e.journal.Launch.SessionID = id
	return e.save()
}

// Reconcile the exact identity named before dispatch or returned by the engine.
func (e *workspaceExecutor) observedLaunch(ctx context.Context) *proto.Session {
	l := e.journal.Launch
	id := l.SessionID
	if id == "" {
		id = l.ExpectedSessionID
	}
	if id == "" {
		return nil
	}
	for _, row := range e.n.sessionList(ctx, true) {
		if row.SessionID != id || row.Cwd != e.journal.Plan.RootPath || row.Starting || row.Archived || row.StartError != "" {
			continue
		}
		switch row.Status {
		case proto.StatusWorking, proto.StatusIdle, proto.StatusBlocked, proto.StatusDone:
			if l.SessionID == "" {
				if e.recordLaunchSession(id) != nil {
					return nil
				}
			}
			return &row
		}
	}
	return nil
}

// A disconnected engine or an empty inventory alone cannot release a checkout.
func (e *workspaceExecutor) launchStopped(ctx context.Context) bool {
	id := e.journal.Launch.SessionID
	if id == "" {
		return false
	}
	n := e.n
	if h, _, known := n.runtimes.belongs(id); known {
		if h == nil {
			return false
		}
		row, ok := h.row(id)
		return ok && row.Cwd == e.journal.Plan.RootPath && !row.Starting && row.Status == proto.StatusOffline && (row.Archived || row.StartError != "")
	}
	if err := n.refreshHostInventory(ctx, true); err != nil {
		return false
	}
	if _, live := n.agent(id); live {
		return false
	}
	if n.stopCause(id) != proto.StopUnknown {
		return true
	}
	for _, s := range n.sessionList(ctx, false) {
		if s.SessionID == id && !s.Starting && s.Cwd == e.journal.Plan.RootPath {
			return s.Status == proto.StatusInterrupted || s.Status == proto.StatusOffline && (s.ReadOnlyReason == stoppedReason || s.Archived)
		}
	}
	return false
}
