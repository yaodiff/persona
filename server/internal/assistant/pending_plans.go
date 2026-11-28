package assistant

import (
	"context"
	"errors"
	"slices"

	"github.com/nwverse/persona/server/internal/db"
)

type openTaskPlanStore interface {
	OpenTaskPlans(context.Context) ([]db.TaskPlanRow, error)
}

// openTaskPlans uses only this manager's store; managers have independent databases.
func (s *Service) openTaskPlans(ctx context.Context) ([]*TaskPlan, error) {
	if st := s.store(); st != nil {
		reader, ok := st.(openTaskPlanStore)
		if !ok {
			return nil, errors.New("pending plans: store does not support open-plan reads")
		}
		rows, err := reader.OpenTaskPlans(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]*TaskPlan, 0, len(rows))
		for _, row := range rows {
			out = append(out, planFromRow(row))
		}
		return out, nil
	}
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	var out []*TaskPlan
	for _, p := range s.tasks.plans {
		if p.State != planOpen {
			continue
		}
		cp := *p
		cp.Tasks = append([]PlanTask(nil), p.Tasks...)
		out = append(out, &cp)
	}
	return out, nil
}

// Main's Needs you has every open plan but Health's: Health keeps its own decisions (what they say stays in the health
// space); a domain space's plan waits on the owner like any other conversation's and is decided from Main too.
func (s *Service) mainPendingPlans(ctx context.Context) ([]*TaskPlan, error) {
	plans, err := s.openTaskPlans(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(plans))
	for _, p := range plans {
		ids = append(ids, p.Conversation)
	}
	spaces := s.conversationSpaces(ctx, ids)
	archived, ready := s.archivedSet()
	if !ready {
		return nil, errors.New("pending plans: archive state unavailable")
	}
	out := make([]*TaskPlan, 0, len(plans))
	for _, p := range plans {
		if space := spaces[p.Conversation]; space.known && space.space == SpaceHealth {
			continue
		}
		if _, hidden := archived[p.Conversation]; hidden {
			continue
		}
		if err := s.conversationIDAvailable(ctx, p.Conversation); err != nil {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *Service) planWithSpace(ctx context.Context, p *TaskPlan) *TaskPlan {
	if p == nil {
		return nil
	}
	cp := *p
	copied := false
	s.todos.mu.RLock()
	for i, task := range p.Tasks {
		if task.TodoID == "" || task.Error == "" && task.ErrorCode == "" && task.WorkspaceError == nil && task.Start == nil {
			continue
		}
		todo := s.todos.todos[task.TodoID]
		if todo == nil || todo.DoneAt == nil || todo.failedStart() {
			continue
		}
		task.Error, task.ErrorCode, task.ErrorArgs = "", "", nil
		task.WorkspaceError, task.RetryWith, task.Start = nil, nil, nil
		if !copied {
			cp.Tasks = slices.Clone(p.Tasks)
			copied = true
		}
		cp.Tasks[i] = task
	}
	s.todos.mu.RUnlock()
	if space, known := s.conversationSpace(ctx, p.Conversation); known {
		cp.Space = space
	}
	return &cp
}
