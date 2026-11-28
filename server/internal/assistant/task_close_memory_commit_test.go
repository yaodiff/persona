package assistant

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMemoryReviewLateActivityLeavesClosureAndLedgerUnchanged(t *testing.T) {
	for _, verdict := range []string{reviewAccepted, reviewCancelled} {
		for _, activity := range []string{"message", "view"} {
			t.Run(verdict+"/"+activity, func(t *testing.T) {
				ctx := context.Background()
				s, _, todo := newSupervisedService(t)
				s.loadSeen(ctx)
				if err := s.loadTaskActivity(ctx); err != nil {
					t.Fatal(err)
				}
				before := todoNow(s, todo.ID)
				feed := feedKinds(s, todo.Origin)
				ledger := before.Supervision.clone()
				ledger.State = verdict
				ledger.Round++

				// Stop the first guard's todo read, then its activity read, then the next todo read.
				s.todos.mu.Lock()
				unlockTodo := sync.OnceFunc(s.todos.mu.Unlock)
				unlockFleet := func() {}
				done := make(chan struct{})
				var closed bool
				var reviewErr error
				go func() {
					closed, reviewErr = s.reviewDoneTodoWithSupervision(ctx, todo.ID, "Checked", verdict == reviewAccepted, ledger, false)
					close(done)
				}()
				defer func() {
					unlockFleet()
					unlockTodo()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("memory review did not finish after releasing its locks")
					}
				}()
				waitAt := func(method string) {
					t.Helper()
					waitFor(t, "memory review at "+method, func() bool {
						for _, stack := range goroutineStacks() {
							if (strings.Contains(stack, "[sync.Mutex.Lock]") || strings.Contains(stack, "[sync.RWMutex.RLock]")) &&
								strings.Contains(stack, "(*Service)."+method) &&
								strings.Contains(stack, "(*Service).reviewDoneTodoWithSupervision") {
								return true
							}
						}
						return false
					})
				}
				waitAt("todoNow")
				s.fleet.mu.Lock()
				unlockFleet = sync.OnceFunc(s.fleet.mu.Unlock)
				unlockTodo()
				waitAt("sessionActivitySnapshotReason")
				s.todos.mu.Lock()
				unlockTodo = sync.OnceFunc(s.todos.mu.Unlock)
				unlockFleet()
				waitAt("todoNow")
				agent := todo.Agents[0]
				if activity == "view" {
					setTaskViewer(t, s, agent.MachineID, agent.SessionID, true)
				} else {
					s.rememberSessionActivity(ctx, agent.MachineID, agent.SessionID)
				}
				unlockTodo()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("memory review did not return")
				}
				var hold *taskCloseHoldError
				if closed || !errors.As(reviewErr, &hold) {
					t.Fatalf("late activity did not refuse the memory review: closed=%v err=%v", closed, reviewErr)
				}
				after := todoNow(s, todo.ID)
				if after.DoneAt != nil || !reflect.DeepEqual(after.Supervision, before.Supervision) ||
					feedKinds(s, todo.Origin) != feed || !reflect.DeepEqual(after.Agents, before.Agents) {
					t.Fatalf("refused memory review changed closure, ledger, feed or Agents: before=%+v after=%+v", before, after)
				}
			})
		}
	}
}
