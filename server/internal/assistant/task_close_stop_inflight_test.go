package assistant

import (
	"context"
	"testing"
	"time"
)

func TestClosedTaskRetirementCancelsBeforeStopOnOwnerActivity(t *testing.T) {
	for _, activity := range []string{"message", "view"} {
		t.Run(activity, func(t *testing.T) {
			ft, row := closeTestService(t, nil)
			s, ctx := ft.s, context.Background()
			gate := &taskRetirementBarrier{Hub: s.hub, entered: make(chan struct{}), release: make(chan struct{})}
			s.hub = gate
			todo, err := s.createTodo(ctx, "Disposable task", "Check the result", "conv1", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.linkTodoAgent(ctx, todo.ID, "mac", row.SessionID, row.Title, "/code", "claude"); err != nil {
				t.Fatal(err)
			}
			if ok, err := s.reviewDoneTodo(ctx, todo.ID, "Closed", true); err != nil || !ok {
				t.Fatalf("close: %v %v", ok, err)
			}
			s.todos.mu.Lock()
			old := time.Now().Add(-time.Hour)
			s.todos.todos[todo.ID].DoneAt = &old
			s.todos.mu.Unlock()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				s.closeDoneTaskAgents(ctx, taskCloseRefused{}, doneSeen{{"mac", row.SessionID}: {done: old, running: old}})
			}()
			defer func() {
				close(gate.release)
				<-finished
			}()
			select {
			case <-gate.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("the stop RPC did not reach the pre-stop barrier")
			}
			if activity == "view" {
				setTaskViewer(t, s, "mac", row.SessionID, true)
			} else {
				s.rememberSessionActivity(ctx, "mac", row.SessionID)
			}
			// Release the transport only after the input has arrived; an already
			// cancelled RPC must never reach the irreversible node stop.
			select {
			case gate.release <- struct{}{}:
			case <-finished:
			}
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("retirement did not finish")
			}
			if got := retiredOf(ft); len(got) != 0 {
				t.Fatalf("owner activity during the stop RPC still stopped the Agent: %v", got)
			}
			if archivedIDs(t, s)[row.SessionID] {
				t.Fatal("the still-live conversation was archived")
			}
		})
	}
}
