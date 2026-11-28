package assistant

import (
	"context"
	"time"
)

// Only a confirmed send from this task's supervisor resets its unattended-stop clock.
func (s *Service) noteTaskFollowup(ctx context.Context, self, mid, sid string) {
	t, ok := s.taskOf(ctx, mid, sid)
	if !ok || t.Supervision == nil || t.Supervision.Supervisor != self {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	s.supervise.writeMu.Lock()
	defer s.supervise.writeMu.Unlock()
	t, ok = s.todoNow(t.ID)
	if !ok || t.DoneAt != nil || t.Supervision == nil || t.Supervision.Supervisor != self {
		return
	}
	now := time.Now().UTC()
	v := t.Supervision.clone()
	v.FollowedUpAt = &now
	// UpdatedAt identifies the review round. A send is follow-up, not a new review.
	if st := s.supervisionDB(); st != nil && s.todoDB() != nil {
		if err := st.PutTodoSupervision(ctx, v.row(t.ID)); err != nil {
			s.logf("task %s: record supervisor follow-up: %v", t.ID, err)
			return
		}
	}
	s.todos.mu.Lock()
	if cur := s.todos.todos[t.ID]; cur != nil && cur.Supervision != nil {
		next := cur.Supervision.clone()
		next.FollowedUpAt = &now
		cur.Supervision = next
	}
	s.todos.mu.Unlock()
	s.todosChanged(ctx)
}
