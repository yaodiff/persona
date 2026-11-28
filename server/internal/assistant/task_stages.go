package assistant

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// taskPlanApproval uses pending plan cards, not the runtime's working status or words. Snapshot all
// active agents: answering one plan does not clear another agent's plan on the same task.
func (s *Service) taskPlanApproval() {
	s.tasks.planApprovalMu.Lock()
	defer s.tasks.planApprovalMu.Unlock()
	plans := map[SessionRef]bool{}
	s.fleet.mu.Lock()
	for _, p := range s.fleet.interactions {
		if !p.claimed && p.ix.Kind == proto.IxPlan {
			plans[SessionRef{MachineID: p.ix.MachineID, SessionID: p.ix.SessionID}] = true
		}
	}
	s.fleet.mu.Unlock()
	s.todos.mu.Lock()
	changed := false
	for _, t := range s.todos.todos {
		pending := false
		for _, a := range t.Agents {
			if a.ReleasedAt == nil && a.DismissedAt == nil && plans[SessionRef{MachineID: a.MachineID, SessionID: a.SessionID}] {
				pending = true
				break
			}
		}
		if t.planPending != pending {
			t.planPending, changed = pending, true
		}
	}
	s.todos.mu.Unlock()
	if changed {
		s.todosChanged(context.Background())
	}
}

// taskStageItems also sees full progress replies when a client or a channel follows a session.
// Tool results, user text and deltas are not completion evidence.
func (s *Service) taskStageItems(ctx context.Context, p SessionItems) {
	if !s.todosLoaded() {
		return
	}
	t, ok := s.taskOf(ctx, p.MachineID, p.SessionID)
	if !ok {
		return
	}
	for _, it := range p.Items {
		if current, found := s.todoNow(t.ID); found {
			t = current
		}
		if it.Role == "assistant" && it.Kind == proto.KindText && it.FullSize == 0 && strings.TrimSpace(it.Text) != "" {
			s.advanceTaskReplyAt(ctx, t, p.MachineID, p.SessionID, it.Text, it.TS)
		}
	}
}

// taskStageSessions sees replies even when no client follows the task's transcript.
func (s *Service) taskStageSessions(ctx context.Context, d DeviceSessions) {
	if !s.todosLoaded() {
		return
	}
	s.taskPlanApproval()
	seen := make(map[SessionRef]bool, len(d.Sessions))
	for _, c := range d.Sessions {
		ref := SessionRef{MachineID: d.MachineID, SessionID: c.SessionID}
		seen[ref] = true
		t, ok := s.taskOf(ctx, d.MachineID, c.SessionID)
		if !ok {
			continue
		}
		s.tasks.mu.Lock()
		if s.tasks.stageReplies == nil {
			s.tasks.stageReplies = map[SessionRef][3]string{}
		}
		key := [3]string{t.ID, c.LastText, c.UpdatedAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)}
		if s.tasks.stageReplies[ref] == key {
			s.tasks.mu.Unlock()
			continue
		}
		s.tasks.stageReplies[ref] = key
		s.tasks.mu.Unlock()
		if t.StageKind == "" {
			s.restage(ctx, t.ID, nil)
		}
		if strings.TrimSpace(c.LastText) == "" {
			continue
		}
		if strings.HasSuffix(c.LastText, "…") {
			continue // a clipped suffix can hide a negation or the change link
		}
		s.advanceTaskReplyAt(ctx, t, d.MachineID, c.SessionID, c.LastText, c.UpdatedAt)
	}
	s.tasks.mu.Lock()
	for ref := range s.tasks.stageReplies {
		if ref.MachineID == d.MachineID && !seen[ref] {
			delete(s.tasks.stageReplies, ref)
		}
	}
	s.tasks.mu.Unlock()
}
