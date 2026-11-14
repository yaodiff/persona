package assistant

import (
	"context"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// unstartedTodo uses durable state, not the old process's retrying, reusing or receipt maps. Even a released
// agent means this task has run before; a user-completed or cancelled task must stay closed.
func unstartedTodo(t Todo) bool {
	return t.Origin != "" && t.DoneAt == nil && !t.Cancelled() && t.StartError == "" && len(t.Agents) == 0
}

// recoverTaskStarts resolves recent starts left without a result by the previous process. The node may still
// finish an agent.start after its caller disconnects, and neither cwd nor title identifies that start uniquely.
// Without a persisted session or idempotency key, retrying here could start a second agent. Use the ordinary
// failed-start path and ask the supervisor/user to check the computer before retrying instead.
func (s *Service) recoverTaskStarts(ctx context.Context, before time.Time) error {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	// Read plans before changing any: writing one task's error updates the whole plan's UpdatedAt, including
	// its other interrupted starts. Old plans are left alone rather than reviving historical work on deployment.
	plans := map[string]*TaskPlan{}
	conversations := map[string]bool{}
	for _, t := range s.todosOf(ctx, "*") {
		if !unstartedTodo(t) {
			continue
		}
		if t.PlanID == "" {
			// A reused topic to-do intentionally has no PlanID; its plan holds ReuseTodo instead.
			conversations[t.Origin] = true
			continue
		}
		if _, ok := plans[t.PlanID]; ok {
			continue
		}
		p, err := s.loadPlan(ctx, t.PlanID)
		if errors.Is(err, errPlanNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		plans[t.PlanID] = p
	}
	for conversation := range conversations {
		if st := s.store(); st != nil {
			rows, err := st.TaskPlans(ctx, conversation)
			if err != nil {
				return err
			}
			for _, row := range rows {
				plans[row.ID] = planFromRow(row)
			}
		} else {
			s.tasks.mu.Lock()
			for id, p := range s.tasks.plans {
				if p.Conversation == conversation {
					cp := *p
					cp.Tasks = append([]PlanTask(nil), p.Tasks...)
					plans[id] = &cp
				}
			}
			s.tasks.mu.Unlock()
		}
	}
	reason := i18n.T(s.lang(ctx), "assistant-tasks.start_interrupted")
	var failures []error
	for _, p := range plans {
		if p.State != planStarted || p.UpdatedAt.Before(before.Add(-taskStartTimeout)) || p.UpdatedAt.After(before) {
			continue
		}
		for i, pt := range p.Tasks {
			// A queued task is deliberately waiting for the admission queue loop: leave it for that loop, do not fail
			// it as an interrupted start (#6909).
			if pt.TodoID == "" || pt.Error != "" || pt.Start != nil || pt.Queue != nil {
				continue
			}
			failed, err := s.failTodoIf(ctx, pt.TodoID, reason, func(t Todo) bool {
				return unstartedTodo(t) && (t.PlanID == p.ID || pt.ReuseTodo == t.ID) && t.Origin == p.Conversation
			})
			if err != nil {
				failures = append(failures, err)
			}
			if !failed {
				continue
			}
			if err := s.patchPlanTask(ctx, p.ID, i, func(t *PlanTask) { t.Error = reason }); err != nil {
				failures = append(failures, err)
			}
			s.tasks.mu.Lock()
			s.tasks.receipted[pt.TodoID] = time.Now()
			s.tasks.mu.Unlock()
			if _, err := s.appendFeed(ctx, p.Conversation, feedReceipt, pt.TodoID, receiptBody{Title: pt.Title, State: "failed",
				Summary: i18n.T(s.lang(ctx), "assistant-tasks.failed_start", i18n.Args{"reason": reason})}); err != nil {
				failures = append(failures, err)
			}
			if todo, ok := s.todoNow(pt.TodoID); ok {
				s.taskNotice(ctx, todo, pt.Machine, "failed", "", reason)
				s.taskReport(todo, reportFailed, pt.Machine, [2]string{}, reason, nil)
			}
		}
	}
	return errors.Join(failures...)
}
