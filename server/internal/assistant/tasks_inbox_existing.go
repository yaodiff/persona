package assistant

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

var errInboxTaskClaimed = errors.New("Inbox item belongs to another task")

func (s *Service) planExistingTaskInbox(ctx context.Context, machine, conversation string, source *PlanTask, todo Todo) error {
	if err := s.planInboxSource(ctx, machine, conversation, source); err != nil {
		return err
	}
	if source.InboxItem == "" {
		return nil
	}
	return s.checkExistingTaskInbox(conversation, *source, todo)
}

// A follow-up may belong to an existing task with a different original Inbox
// source. Keep that source and append this item's backlink and Agent chain.
func (s *Service) checkExistingTaskInbox(conversation string, source PlanTask, todo Todo) error {
	if todo.ID != "" && !taskOpen(todo) {
		return fmt.Errorf("task %s is not open; its Inbox item was not linked", todo.ID)
	}
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkTaskInbox(conversation, source); err != nil {
		return err
	}
	it := b.items[source.InboxItem]
	if it.Agent != nil && it.Agent.TodoID != "" && it.Agent.TodoID != todo.ID {
		return fmt.Errorf("%w: inbox_item %q already links to task %s; continue that task instead", errInboxTaskClaimed, it.ID, it.Agent.TodoID)
	}
	return nil
}

// An explicitly selected handoff keeps the backlink on the origin and names an Agent on the receiving task.
func (s *Service) linkExistingTaskInbox(ctx context.Context, conversation string, source PlanTask, todoID, agentTodoID string, agents []SessionRef) error {
	todo, ok := s.todoNow(todoID)
	if !ok {
		return fmt.Errorf("task %s no longer exists", todoID)
	}
	if err := s.checkExistingTaskInbox(conversation, source, todo); err != nil {
		return err
	}
	agentTodo := todo
	if agentTodoID != todoID {
		agentTodo, ok = s.todoNow(agentTodoID)
		if !ok || !taskOpen(agentTodo) {
			return fmt.Errorf("the receiving task %s is no longer open; its Agent was not linked", agentTodoID)
		}
	}
	activeAgents := make([]SessionRef, 0, len(agents))
	for _, ref := range agents {
		for _, a := range agentTodo.Agents {
			if a.MachineID == ref.MachineID && a.SessionID == ref.SessionID && a.ReleasedAt == nil && a.DismissedAt == nil {
				activeAgents = append(activeAgents, ref)
				break
			}
		}
	}
	if len(agents) > 0 && len(activeAgents) == 0 {
		return fmt.Errorf("the Agent no longer works on task %s; its Inbox item was not linked", agentTodoID)
	}
	b := &s.inbox
	b.mu.Lock()
	if err := b.checkTaskInbox(conversation, source); err != nil {
		b.mu.Unlock()
		return err
	}
	it := b.items[source.InboxItem]
	if it.Agent != nil && it.Agent.TodoID != "" && it.Agent.TodoID != todo.ID {
		b.mu.Unlock()
		return fmt.Errorf("%w: inbox_item %q was linked to another task meanwhile", errInboxTaskClaimed, it.ID)
	}
	w := beginWrite(it)
	changed := it.Agent == nil || it.Agent.TodoID != todo.ID
	if it.Agent == nil {
		it.Agent = &InboxAgent{State: AgentSkipped}
	}
	it.Agent.TodoID = todo.ID
	for _, ref := range activeAgents {
		seen := false
		for _, entry := range it.Chain {
			if inboxChainSession(entry, ref.MachineID, ref.SessionID) {
				if entry.Kind == ChainAgent {
					seen = true
				} else if entry.Kind == ChainAgentStopped {
					seen = false
				}
			}
		}
		if !seen {
			b.chainAdd(it, InboxChainEntry{Kind: ChainAgent, At: time.Now().UTC(),
				Text: i18n.T(b.userLang(), "assistant-inbox_chain.agent", i18n.Args{"title": clip(oneLine(todo.Title), 40)}),
				Link: &InboxChainLink{Type: LinkSession, MachineID: ref.MachineID, SessionID: ref.SessionID}})
			changed = true
		}
	}
	if !changed {
		b.mu.Unlock()
		return nil
	}
	b.settle(w)
	b.mu.Unlock()
	if s.saveWrites(ctx, []*inboxWrite{w}, "its existing task and Agent links")[source.InboxItem] {
		return errInboxNotPersisted
	}
	return nil
}
