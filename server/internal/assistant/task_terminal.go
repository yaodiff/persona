package assistant

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

type taskTerminalTarget struct {
	Machine string `json:"machine"`
	devicewire.TaskTerminal
}

func (s *Service) taskTerminalPlan(ctx context.Context, t Todo) (PlanTask, int, error) {
	if t.PlanID == "" {
		return PlanTask{}, -1, nil
	}
	p, err := s.loadPlan(ctx, t.PlanID)
	if errors.Is(err, errPlanNotFound) {
		return PlanTask{}, -1, nil
	}
	if err != nil {
		return PlanTask{}, -1, err
	}
	i := slices.IndexFunc(p.Tasks, func(pt PlanTask) bool { return pt.TodoID == t.ID })
	if i < 0 || p.State != planStarted {
		return PlanTask{}, -1, nil
	}
	return p.Tasks[i], i, nil
}

func (s *Service) taskTerminalRoute() {
	s.handle("DELETE /api/v1/todos/{id}/terminal-cleanup", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("only the owner can withdraw terminal cleanup"))
			return
		}
		ctx := r.Context()
		t, ok, err := s.todo(ctx, r.PathValue("id"))
		if err != nil || !ok {
			if err == nil {
				err = db.ErrTodoNotFound
			}
			todoErr(w, err)
			return
		}
		_, i, err := s.taskTerminalPlan(ctx, t)
		status := devicewire.TaskTerminalSkipped
		if err == nil && i >= 0 {
			err = s.patchPlanTask(ctx, t.PlanID, i, func(pt *PlanTask) {
				if pt.TodoID != t.ID {
					return
				}
				if pt.TerminalAuthorized && (pt.TerminalCleanup == devicewire.TaskTerminalPending ||
					pt.TerminalCleanup == devicewire.TaskTerminalClosed) {
					status = pt.TerminalCleanup
				} else if pt.CloseTabOnAccept {
					pt.CloseTabOnAccept = false
					pt.TerminalTarget = taskTerminalTarget{}
					pt.TerminalCleanup = status
				}
			})
		}
		if err != nil {
			todoErr(w, err)
			return
		}
		if status != devicewire.TaskTerminalSkipped {
			apierr.Write(w, http.StatusConflict, errors.New("terminal cleanup has already been dispatched"))
			return
		}
		apierr.WriteJSON(w, map[string]string{"terminal_cleanup": status})
	})
}

// goFinishTaskTerminal runs finishTaskTerminal for an acceptance the owner's Pass just recorded, off the Pass's path:
// the owner's answer does not wait for the node (counted in tasks.terminalRuns; the tests wait on them). A terminal
// that is busy, watched or no longer the task's alone at that moment is not closed (finishTaskTerminal's checks).
func (s *Service) goFinishTaskTerminal(ctx context.Context, t Todo) {
	ctx = context.WithoutCancel(ctx)
	s.tasks.terminalRuns.Add(1)
	go func() {
		defer s.tasks.terminalRuns.Done()
		// The node's final check reads when the owner last looked at the Agent; Pass itself did not load it. Unknown
		// activity keeps the terminal open.
		s.loadSeen(ctx)
		if err := s.loadTaskActivity(ctx); err != nil {
			s.logf("task %s: owner Pass: load activity for terminal cleanup: %v", t.ID, err)
		}
		if status := s.finishTaskTerminal(ctx, t, true); status != devicewire.TaskTerminalSkipped {
			s.logf("task %s: owner Pass: terminal cleanup %s", t.ID, status)
		}
	}()
}

// finishTaskTerminal claims one close attempt in the plan before it can reach
// the node. A lost reply leaves the outcome pending and cannot cause a resend.
func (s *Service) finishTaskTerminal(ctx context.Context, t Todo, accepted bool) string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentArchiveCloseTimeout)
	defer cancel()
	task, i, err := s.taskTerminalPlan(ctx, t)
	if err != nil {
		s.logf("task %s: load terminal cleanup: %v", t.ID, err)
	}
	if !task.CloseTabOnAccept || task.TerminalCleanup != "" {
		return terminalCleanupStatus(task)
	}
	target := task.TerminalTarget
	cur, ok := s.todoNow(t.ID)
	eligible := accepted && ok && cur.DoneAt != nil && target.Machine != "" && s.taskTerminalIdle(ctx, target)
	claimed := false
	status := devicewire.TaskTerminalSkipped
	err = s.patchPlanTask(ctx, t.PlanID, i, func(pt *PlanTask) {
		if pt.TodoID != t.ID {
			return
		}
		if !pt.CloseTabOnAccept || pt.TerminalCleanup != "" {
			status = terminalCleanupStatus(*pt)
			return
		}
		if eligible && pt.TerminalTarget == target && s.taskTerminalOwned(t.ID, *cur.DoneAt, target) {
			claimed = true
			status = devicewire.TaskTerminalPending
		}
		pt.TerminalCleanup = status
	})
	if err != nil {
		s.logf("task %s: claim terminal cleanup: %v", t.ID, err)
		return devicewire.TaskTerminalSkipped
	}
	if !claimed {
		return status
	}
	var out struct {
		Status string `json:"status"`
	}
	params := devicewire.TaskTerminalCloseRequest{TodoID: t.ID, AcceptedAt: *cur.DoneAt, Terminal: target.TaskTerminal}
	err = s.call(ctx, target.Machine, devicewire.MethodCloseTaskTerminal, params, &out)
	switch {
	case err == nil && (out.Status == devicewire.TaskTerminalClosed || out.Status == devicewire.TaskTerminalPending):
		status = out.Status
	case sendUnknown(err):
		status = devicewire.TaskTerminalPending
	default:
		status = devicewire.TaskTerminalSkipped
	}
	if status != devicewire.TaskTerminalPending {
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
		defer cancel()
		if err := s.patchPlanTask(saveCtx, t.PlanID, i, func(pt *PlanTask) {
			if pt.TerminalTarget == target && pt.TerminalCleanup == devicewire.TaskTerminalPending {
				if status == devicewire.TaskTerminalClosed && !pt.TerminalAuthorized {
					status = devicewire.TaskTerminalSkipped
				}
				pt.TerminalCleanup = status
			}
		}); err != nil {
			s.logf("task %s: save terminal cleanup result: %v", t.ID, err)
		}
	}
	return status
}

// authorizeTaskTerminal is the node's final check after inspecting the tab.
// It consumes the authorization once, so withdrawal or reassignment that
// wins before this write keeps the pane open.
func (s *Service) authorizeTaskTerminal(ctx context.Context, machine string, req devicewire.TaskTerminalCloseRequest) bool {
	t, ok := s.todoNow(req.TodoID)
	if !ok || t.DoneAt == nil || !t.DoneAt.Equal(req.AcceptedAt) || t.DoneBy != db.DoneByReview {
		return false
	}
	target := taskTerminalTarget{Machine: machine, TaskTerminal: req.Terminal}
	pt, i, err := s.taskTerminalPlan(ctx, t)
	if err != nil || i < 0 || !pt.CloseTabOnAccept || pt.TerminalTarget != target || pt.TerminalAuthorized ||
		pt.TerminalCleanup != devicewire.TaskTerminalPending || !s.taskTerminalIdle(ctx, target) {
		return false
	}
	authorized := false
	s.todos.writing.Lock()
	defer s.todos.writing.Unlock()
	err = s.patchPlanTask(ctx, t.PlanID, i, func(pt *PlanTask) {
		if pt.TodoID == t.ID && pt.CloseTabOnAccept && pt.TerminalCleanup == devicewire.TaskTerminalPending &&
			pt.TerminalTarget == target && !pt.TerminalAuthorized && s.taskTerminalOwned(t.ID, req.AcceptedAt, target) &&
			s.sessionActivitySnapshotReason(machine, target.SessionID, time.Now()) == "" {
			pt.TerminalAuthorized = true
			authorized = true
		}
	})
	return err == nil && authorized
}

func terminalCleanupStatus(t PlanTask) string {
	if t.TerminalCleanup != "" {
		return t.TerminalCleanup
	}
	return devicewire.TaskTerminalSkipped
}

func terminalCleanupWords(status string) string {
	result := " Terminal cleanup: " + status + "."
	if status == devicewire.TaskTerminalPending {
		result += " The close request may have reached the node. Check the terminal before trying again."
	}
	return result
}

func (s *Service) taskTerminalIdle(ctx context.Context, target taskTerminalTarget) bool {
	var row proto.Session
	var ok bool
	if s.fleetOn() {
		row, ok = s.listedSession(target.Machine, target.SessionID)
	} else {
		row, ok = s.session(ctx, target.Machine, target.SessionID)
	}
	if !ok || row.PaneID != target.PaneID || row.TabID != target.TabID || row.WorkspaceID != target.WorkspaceID ||
		!row.StartedByPersona || row.Assistant || row.Archived || row.SourceKind != "" || row.Attached || row.PendingSwitch != nil ||
		row.Undelivered != nil && len(*row.Undelivered) > 0 || time.Since(row.SeenAt) < time.Minute || agentCloseBusy(row) {
		return false
	}
	return row.Status == proto.StatusIdle || row.Status == proto.StatusDone
}

func (s *Service) taskTerminalOwned(id string, acceptedAt time.Time, target taskTerminalTarget) bool {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	t := s.todos.todos[id]
	if t == nil || t.DoneAt == nil || !t.DoneAt.Equal(acceptedAt) || t.DoneBy != db.DoneByReview ||
		s.todos.activeBySession[[2]string{target.Machine, target.SessionID}] != "" {
		return false
	}
	// A task that acquired more workers no longer has a single exclusive run.
	var linked int
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		if a.MachineID != target.Machine || a.SessionID != target.SessionID {
			return false
		}
		linked++
	}
	return linked == 1
}
