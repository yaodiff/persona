package assistant

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func TestTodoBlockerPRWatchDoesNotWaitForForge(t *testing.T) {
	st := newMemBlockerStore()
	s, _ := newQuietServiceOn(t, st)
	setBlockersTodosV2Flag(t, s, true, false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const slowPR, fastPR = "https://github.com/example/repo/pull/103", "https://github.com/example/repo/pull/104"
	for _, pr := range []string{slowPR, fastPR} {
		item, err := s.createTodo(ctx, "Wait for upstream", "", "conv1", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AddTodoBlocker(ctx, item.ID, todoBlockerKindExternalPR, pr, ""); err != nil {
			t.Fatal(err)
		}
	}
	slowStarted, releaseSlow, watchReturned := make(chan struct{}), make(chan struct{}), make(chan struct{})
	s.setTodoBlockerPRFetcher(func(ctx context.Context, ref string) (todoBlockerPRFact, bool) {
		if ref == slowPR {
			close(slowStarted)
			select {
			case <-releaseSlow:
			case <-ctx.Done():
				return todoBlockerPRFact{}, false
			}
		}
		return todoBlockerPRFact{URL: ref, State: "merged"}, true
	})
	go func() {
		s.superviseWatch(ctx, time.Now())
		close(watchReturned)
	}()
	for _, done := range []<-chan struct{}{slowStarted, watchReturned} {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("supervision waited for the slow external PR")
		}
	}
	waitForCondition(t, 3*time.Second, "independent PR to clear its blocker", func() bool {
		rows, _ := st.ListActiveTodoBlockersByTarget(ctx, fastPR)
		return len(rows) == 0
	})
	close(releaseSlow)
	waitForCondition(t, 3*time.Second, "observer to finish", func() bool { return !s.blockerReconcile.sweep.Load() })
	if active, err := st.ListActiveTodoBlockers(ctx); err != nil || len(active) != 0 {
		t.Fatalf("terminal PR facts left blockers active: %+v, %v", active, err)
	}
}

func TestTodoBlockerReconcilePostgresConcurrentObservationsWakeOnce(t *testing.T) {
	st := todoTestDB(t)
	s, _ := newQuietServiceOn(t, st)
	setBlockersTodosV2Flag(t, s, true, false)
	ctx := t.Context()
	const pr = "https://github.com/example/repo/pull/105"
	todo, err := s.createTodo(ctx, "Wait for upstream", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.AddTodoBlocker(ctx, todo.ID, todoBlockerKindExternalPR, pr, "")
	if err != nil {
		t.Fatal(err)
	}
	var wakes, resolved atomic.Int32
	s.setTodoBlockerUnblockedHook(func(context.Context, string) { wakes.Add(1) })
	var observers sync.WaitGroup
	for range 8 {
		observers.Go(func() {
			sum, err := s.reconcileTodoBlockersForPR(ctx, TodoPRStatus{URL: pr, State: "merged"})
			if err != nil {
				t.Error(err)
			}
			resolved.Add(int32(len(sum.ResolvedIDs)))
		})
	}
	observers.Wait()
	got, err := st.ListTodoBlockersByTodo(ctx, todo.ID)
	if err != nil || len(got) != 1 || got[0].ID != row.ID || got[0].State != db.TodoBlockerStateResolved || resolved.Load() != 1 || wakes.Load() != 1 {
		t.Fatalf("expected one committed resolution and one wake: rows=%+v resolved=%d wakes=%d err=%v", got, resolved.Load(), wakes.Load(), err)
	}
}
