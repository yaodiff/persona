package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func TestTodoReopenWaitsBehindTerminalReviewAndPark(t *testing.T) {
	for _, state := range []string{reviewAccepted, reviewParked} {
		t.Run(state, func(t *testing.T) {
			e := newTodoEnv(t, nil)
			// Use a different to-do so the order is tested across the global writers.
			reopened := e.create("Reopen this").ID
			e.must("PATCH", "/api/v1/todos/"+reopened, `{"done":true}`)
			reviewed := e.create("Review this").ID
			if err := e.s.superviseTodo(t.Context(), reviewed, "conv1", nil, 0); err != nil {
				t.Fatal(err)
			}

			// Stop the terminal review or guarded park just after it takes the to-do writer.
			// A reopen must queue here without taking the ledger writer that this path needs next.
			s := e.s
			s.todos.writing.Lock()
			reopenDone := make(chan error, 1)
			go func() {
				_, err := s.editTodo(context.Background(), reopened, todoDoneEdit(false))
				reopenDone <- err
			}()
			holdingTodo := true
			defer func() {
				if holdingTodo {
					s.todos.writing.Unlock()
				}
				select {
				case err := <-reopenDone:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(5 * time.Second):
					t.Error("reopen did not finish after the to-do writer was released")
				}
			}()
			waitFor(t, "reopen at the write lock", func() bool {
				for _, stack := range goroutineStacks() {
					if strings.Contains(stack, "[sync.Mutex.Lock]") && strings.Contains(stack, "(*Service).editTodoWithCloseActivity") {
						return true
					}
				}
				return false
			})
			if !s.supervise.writeMu.TryLock() {
				t.Fatal("reopen holds the ledger writer while waiting for the to-do writer")
			}
			var closing *taskReviewClose
			if state == reviewAccepted {
				closing = &taskReviewClose{reason: "accepted", accepted: true}
			}
			ledger, changed, err := s.updateSupervisionGuardedLockedAt(t.Context(), reviewed, time.Now(), func(cur Todo) bool {
				return cur.DoneAt == nil
			}, func(v *Supervision) bool {
				v.State = state
				return true
			}, closing)
			s.supervise.writeMu.Unlock()
			s.todos.writing.Unlock()
			holdingTodo = false
			if err != nil || !changed || ledger.State != state {
				t.Fatalf("review/park did not commit while reopen was queued: state=%s changed=%t err=%v", ledger.State, changed, err)
			}
			if closing != nil {
				if !closing.closed {
					t.Fatal("terminal review left the to-do open")
				}
				closing.after()
			}
		})
	}
}

func TestTodoReopenReleasesWritersWhileWaitingForIdleClose(t *testing.T) {
	e := newTodoEnv(t, nil)
	s := e.s
	id := e.create("Reopen after idle close").ID
	done := true
	if _, err := s.editTodo(t.Context(), id, db.TodoEdit{
		Done: &done, SetResponsible: true,
		Responsible: &db.TodoResponsible{Kind: db.ResponsibleSession, MachineID: "server", SessionID: "worker"},
	}); err != nil {
		t.Fatal(err)
	}
	s.todos.writing.Lock()
	release, why, ok := s.acquireIdleCloseFenceLocked("server", []string{"worker"})
	s.todos.writing.Unlock()
	if !ok {
		t.Fatalf("idle close fence: %s", why)
	}
	defer release()

	reopened := make(chan error, 1)
	go func() {
		_, err := s.editTodo(t.Context(), id, todoDoneEdit(false))
		reopened <- err
	}()
	waitFor(t, "reopen at the idle-close fence", func() bool {
		for _, stack := range goroutineStacks() {
			if strings.Contains(stack, "[select]") && strings.Contains(stack, "(*Service).lockTodoWritingForSessions") {
				return true
			}
		}
		return false
	})
	if !s.todos.writing.TryLock() {
		t.Fatal("reopen held the to-do writer while waiting for the idle close")
	}
	s.todos.writing.Unlock()
	if !s.supervise.writeMu.TryLock() {
		t.Fatal("reopen held the ledger writer while waiting for the idle close")
	}
	s.supervise.writeMu.Unlock()
	release()
	select {
	case err := <-reopened:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reopen did not finish after the idle-close fence was released")
	}
}
