package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// planInboxSource validates a caller-selected item. matchPlannedInbox handles source inference for plans.
func (s *Service) planInboxSource(ctx context.Context, machine, conversation string, t *PlanTask) error {
	source := strings.TrimSpace(t.InboxItem)
	if source == "none" {
		t.InboxItem, t.InboxHandoff, t.InboxOptOut = "", "", true
		return nil
	}
	t.InboxItem = source
	if t.InboxItem == "" {
		return nil
	}
	if todo, ok := s.todoNow(t.ReuseTodo); ok && todo.InboxItem != "" && todo.InboxItem != t.InboxItem {
		return fmt.Errorf("todo %s already belongs to another Inbox item", todo.ID)
	}
	if err := s.inboxReady(ctx); err != nil {
		return err
	}
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	it := b.items[t.InboxItem]
	if it == nil {
		return fmt.Errorf("inbox_item %q does not exist in this manager", t.InboxItem)
	}
	h := handoffTo(it, conversation)
	if h == nil || h.MachineID != machine || h.Op == "" {
		return fmt.Errorf("inbox_item %q was not handed to this conversation", t.InboxItem)
	}
	t.InboxHandoff = h.Op
	return b.checkTaskInbox(conversation, *t)
}

// checkTaskInbox rechecks the exact recorded handoff at admission, not a newer item or conversation. Under b.mu.
func (b *inboxBook) checkTaskInbox(conversation string, t PlanTask) error {
	it := b.items[t.InboxItem]
	if it == nil || !it.open() || !inboxTaskLinkEligible(it) {
		return fmt.Errorf("inbox_item %q is missing, filed or excluded from task links", t.InboxItem)
	}
	if t.InboxMatch != "" && it.Agent != nil && it.Agent.TodoID != "" && it.Agent.TodoID != t.ReuseTodo && it.Agent.TodoID != t.ReservedTodoID && it.Agent.TodoID != t.TodoID {
		return fmt.Errorf("inbox_item %q already belongs to another task", t.InboxItem)
	}
	if t.InboxMatch != "" {
		return b.checkSoftInbox(t, time.Now(), func(it *InboxItem) bool { return it.VisibilityExcluded })
	}
	h := handoffOp(it, t.InboxHandoff)
	if t.InboxHandoff == "" || h == nil || h.SessionID != conversation {
		return fmt.Errorf("inbox_item %q no longer has this plan's handoff", t.InboxItem)
	}
	return nil
}

func (s *Service) checkPlannedInbox(ctx context.Context, conversation string, t PlanTask) error {
	if t.InboxItem == "" {
		return nil
	}
	if todo, ok := s.todoNow(t.ReuseTodo); ok && todo.InboxItem != "" && todo.InboxItem != t.InboxItem {
		return fmt.Errorf("todo %s already belongs to another Inbox item", todo.ID)
	}
	if err := s.inboxReady(ctx); err != nil {
		return err
	}
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.checkTaskInbox(conversation, t)
}

// linkPlannedInbox also says whether a failed save left a newer item write still referring to this task.
func (s *Service) linkPlannedInbox(ctx context.Context, conversation string, t PlanTask, todo Todo) (keep bool, err error) {
	if t.InboxItem == "" {
		return false, nil
	}
	if todo.InboxItem != "" && todo.InboxItem != t.InboxItem {
		return false, fmt.Errorf("todo %s already belongs to another Inbox item", todo.ID)
	}
	changed := todo.InboxItem != t.InboxItem
	if changed {
		if _, err := s.editTodo(ctx, todo.ID, db.TodoEdit{InboxItem: &t.InboxItem}); err != nil {
			return false, err
		}
	}
	b := &s.inbox
	b.mu.Lock()
	err = b.checkTaskInbox(conversation, t)
	var w *inboxWrite
	var related []*inboxWrite
	if err == nil {
		it := b.items[t.InboxItem]
		w = beginWrite(it)
		if it.Agent == nil {
			it.Agent = &InboxAgent{State: AgentSkipped}
		}
		it.Agent.TodoID = todo.ID
		related = b.linkSoftInboxRelated(t, it)
		b.settle(w)
	}
	b.mu.Unlock()
	failed := false
	if err == nil {
		for _, bad := range s.saveWrites(ctx, append([]*inboxWrite{w}, related...), "its planned task link") {
			failed = failed || bad
		}
	}
	if err == nil && failed {
		b.mu.Lock()
		it := b.items[t.InboxItem]
		keep = it != nil && it.Agent != nil && it.Agent.TodoID == todo.ID
		b.mu.Unlock()
		// Do not remove a target the current item still references; the failed admission must not start an Agent.
		if keep {
			return true, errInboxNotPersisted
		}
		err = errInboxNotPersisted
	}
	if err != nil && changed {
		if _, undoErr := s.editTodo(ctx, todo.ID, db.TodoEdit{InboxItem: &todo.InboxItem}); undoErr != nil {
			return false, fmt.Errorf("%w (restoring the task source also failed: %v)", err, undoErr)
		}
	}
	return false, err
}
