package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
)

func saveCompetingInboxClaim(t *testing.T, s *Service, st *db.DB, it *InboxItem, todoID string) {
	t.Helper()
	s.inbox.mu.Lock()
	it.Agent = &InboxAgent{State: AgentSkipped, TodoID: todoID}
	data, err := json.Marshal(it)
	updatedAt, doneAt := it.UpdatedAt, it.DoneAt
	s.inbox.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// The fake transport holds its mutex; persist without re-entering its Hello method.
	if err := st.PutInboxItem(t.Context(), it.ID, data, updatedAt, doneAt); err != nil {
		t.Fatal("save the competing claim", err)
	}
}

func TestAgentSendNewTaskInboxLateClaimRollsBackOnlyNewTask(t *testing.T) {
	for _, delivery := range []string{"sent", "queued"} {
		t.Run(delivery, func(t *testing.T) {
			st := todoTestDB(t)
			s, f := newQuietServiceOn(t, st)
			secretary(f)
			other, err := s.createTodo(t.Context(), "Existing claimant", "Other work", "conv1", "")
			if err != nil {
				t.Fatal(err)
			}
			it := taskInboxItem(t, s, "example:late-new-task-claim", "server", "conv1")
			newID := ""
			f.answerNode(DeviceSessionSend, func(_ string, raw json.RawMessage) (any, error) {
				var args map[string]any
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, err
				}
				f.agentSent = append(f.agentSent, args)
				made, ok := s.openTodoOfAgent("box", "sec00001")
				if !ok || made.ID == other.ID {
					t.Fatal("send did not exercise a newly created task")
				}
				newID = made.ID
				saveCompetingInboxClaim(t, s, st, it, other.ID)
				return map[string]bool{"queued": delivery == "queued"}, nil
			})
			text, err := existingInboxCall(t, s, "agent_send", map[string]any{
				"machine": "box", "session_id": "sec00001", "text": "Investigate the additional report", "inbox_item": it.ID,
				"task": map[string]any{"title": "Investigate report", "done_when": []string{"The reported case is verified"}},
			})
			if err == nil || !strings.Contains(text, "message delivered") || !strings.Contains(text, "do not resend") {
				t.Fatal("late claim did not preserve the delivery receipt", text, err)
			}
			if newID == "" || len(sentToAgent(f)) != 1 || len(f.started()) != 0 || len(f.retires()) != 0 {
				t.Fatal("late claim retried delivery or changed the running Agent", newID, sentToAgent(f), f.started(), f.retires())
			}
			if _, exists := s.todoNow(newID); exists {
				t.Fatal("the newly created task survived the conflicting Inbox claim", newID)
			}
			if !strings.Contains(text, "newly created task "+newID+" was removed") {
				t.Fatal("partial success did not explain the rolled-back task", text)
			}
			if _, linked := s.openTodoOfAgent("box", "sec00001"); linked {
				t.Fatal("the rolled-back task retained its Agent link")
			}
			rows, err := st.ListTodos(t.Context())
			if err != nil || len(rows) != 1 || rows[0].ID != other.ID {
				t.Fatal("rollback removed the claimant or left a duplicate durable task", rows, err)
			}
			next, _ := newQuietServiceOn(t, st)
			if err := next.inboxReady(t.Context()); err != nil {
				t.Fatal(err)
			}
			raw, ok := next.InboxItemJSON(t.Context(), it.ID)
			var saved InboxItem
			if !ok || json.Unmarshal(raw, &saved) != nil || saved.Agent == nil || saved.Agent.TodoID != other.ID {
				t.Fatal("rollback lost the other task's persisted Inbox claim", string(raw))
			}
		})
	}
}

func TestExistingTaskInboxHandoffLateClaimKeepsExistingTask(t *testing.T) {
	st := todoTestDB(t)
	s, f := newQuietServiceOn(t, st)
	todo := agentTask(t, s, f)
	other, err := s.createTodo(t.Context(), "Existing claimant", "Other work", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	it := taskInboxItem(t, s, "example:existing-task-late-claim", "server", "conv1")
	before := len(sentToAgent(f))
	f.answerNode(DeviceSessionSend, func(_ string, raw json.RawMessage) (any, error) {
		var args map[string]any
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, err
		}
		f.agentSent = append(f.agentSent, args)
		saveCompetingInboxClaim(t, s, st, it, other.ID)
		return map[string]bool{"queued": false}, nil
	})
	text, err := existingInboxCall(t, s, "agent_send", existingInboxArgs("agent_send", todo, it.ID))
	if err == nil || !strings.Contains(text, "do not resend") {
		t.Fatal("late claim did not report partial delivery", text, err)
	}
	got, ok := s.todoNow(todo.ID)
	if !ok || got.DoneAt != nil || len(got.Agents) != 1 {
		t.Fatal("late claim removed or closed the pre-existing task", got)
	}
	active, ok := s.openTodoOfAgent(todo.Agents[0].MachineID, todo.Agents[0].SessionID)
	if !ok || active.ID != todo.ID || len(sentToAgent(f)) != before+1 || len(f.retires()) != 0 {
		t.Fatal("late claim changed the existing Agent or retried delivery", active)
	}
	rows, err := st.ListTodos(t.Context())
	if err != nil || len(rows) != 2 {
		t.Fatal("late claim removed an existing durable task", rows, err)
	}
}

type inboxClaimRollbackStore struct {
	*db.DB
}

func (st *inboxClaimRollbackStore) DeleteTodo(context.Context, string) error {
	return errors.New("rollback temporarily unavailable")
}

func TestAgentSendNewTaskInboxFailedRollbackNamesRemainingTask(t *testing.T) {
	st := &inboxClaimRollbackStore{DB: todoTestDB(t)}
	s, f := newQuietServiceOn(t, st)
	secretary(f)
	other, err := s.createTodo(t.Context(), "Existing claimant", "Other work", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	it := taskInboxItem(t, s, "example:failed-task-rollback", "server", "conv1")
	newID := ""
	f.answerNode(DeviceSessionSend, func(_ string, raw json.RawMessage) (any, error) {
		var args map[string]any
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, err
		}
		f.agentSent = append(f.agentSent, args)
		made, ok := s.openTodoOfAgent("box", "sec00001")
		if !ok {
			t.Fatal("new task is missing before the conflicting claim")
		}
		newID = made.ID
		saveCompetingInboxClaim(t, s, st.DB, it, other.ID)
		return map[string]bool{"queued": false}, nil
	})
	text, err := existingInboxCall(t, s, "agent_send", map[string]any{
		"machine": "box", "session_id": "sec00001", "text": "Investigate the additional report", "inbox_item": it.ID,
		"task": map[string]any{"title": "Investigate report"},
	})
	if err == nil || newID == "" || !strings.Contains(text, "newly created task "+newID+" remains because rollback failed") ||
		!strings.Contains(text, "do not resend") {
		t.Fatal("failed rollback hid the actual remaining task", text, err, newID)
	}
	if _, ok := s.todoNow(newID); !ok {
		t.Fatal("failed rollback was reported as a successful removal")
	}
	if len(sentToAgent(f)) != 1 || len(f.retires()) != 0 {
		t.Fatal("failed rollback resent the message or stopped the Agent")
	}
}

func TestAgentSendNewTaskInboxRejectsExistingClaimBeforeDelivery(t *testing.T) {
	s, f := newQuietServiceOn(t, todoTestDB(t))
	secretary(f)
	other, err := s.createTodo(t.Context(), "Existing claimant", "Other work", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	it := taskInboxItem(t, s, "example:existing-new-task-claim", "server", "conv1")
	s.inbox.mu.Lock()
	it.Agent = &InboxAgent{State: AgentSkipped, TodoID: other.ID}
	s.inbox.mu.Unlock()
	text, err := existingInboxCall(t, s, "agent_send", map[string]any{
		"machine": "box", "session_id": "sec00001", "text": "Investigate the additional report", "inbox_item": it.ID,
		"task": map[string]any{"title": "Investigate report"},
	})
	if err == nil || !strings.Contains(text, "already links to task "+other.ID) || len(sentToAgent(f)) != 0 {
		t.Fatal("a pre-existing claim was not refused before delivery", text, err, sentToAgent(f))
	}
	if _, linked := s.openTodoOfAgent("box", "sec00001"); linked {
		t.Fatal("a refused handoff adopted the Agent")
	}
	if got, ok := s.todoNow(other.ID); !ok || got.DoneAt != nil || got.DoneBy == db.DoneByReview {
		t.Fatal("refusing the new task changed the existing claimant")
	}
}
