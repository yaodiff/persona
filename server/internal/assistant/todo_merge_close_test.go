package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// A merge closes its source as it always has (#17755): done at the merge's own time, DoneAt and UpdatedAt the same
// instant, by cancellation, for "merged into <target>"; the same without the database as in its transaction, and as
// the database keeps it after a restart.
func TestTodoMergeClosesTheSourceAsBefore(t *testing.T) {
	check := func(t *testing.T, s *Service, from, to time.Time, targetID, sourceID string) {
		t.Helper()
		got, ok := s.todoNow(sourceID)
		if !ok || got.DoneAt == nil {
			t.Fatalf("the merged source is not closed: %+v (found %t)", got, ok)
		}
		if got.DoneBy != db.DoneByCancelled || got.DoneReason != "merged into "+targetID {
			t.Fatalf("source closed by %q for %q, want %q for %q", got.DoneBy, got.DoneReason, db.DoneByCancelled,
				"merged into "+targetID)
		}
		if !got.UpdatedAt.Equal(*got.DoneAt) || got.DoneAt.Before(from) || got.DoneAt.After(to) {
			t.Fatalf("source done at %v, updated at %v: want one instant within the merge [%v, %v]", *got.DoneAt,
				got.UpdatedAt, from, to)
		}
	}
	merge := func(t *testing.T, s *Service, targetID, sourceID string) (time.Time, time.Time) {
		t.Helper()
		from := time.Now().UTC().Truncate(time.Millisecond)
		if _, err := s.mergeTodos(context.Background(), targetID, todoMergeRequest{SourceID: sourceID}); err != nil {
			t.Fatalf("mergeTodos: %v", err)
		}
		return from, time.Now().UTC()
	}

	t.Run("memory", func(t *testing.T) {
		s, _ := newTopologyTestEnv(t, true)
		ctx := context.Background()
		target, err := s.createPlainTodo(ctx, "Primary goal", "", db.TodoEdit{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		source, err := s.createPlainTodo(ctx, "Duplicate goal", "", db.TodoEdit{}, nil)
		if err != nil {
			t.Fatal(err)
		}
		from, to := merge(t, s, target.ID, source.ID)
		check(t, s, from, to, target.ID, source.ID)
	})

	t.Run("postgres", func(t *testing.T) {
		e, st := topologyPostgres(t)
		target := e.create("Primary goal")
		source := e.create("Duplicate goal")
		from, to := merge(t, e.s, target.ID, source.ID)
		check(t, e.s, from, to, target.ID, source.ID)
		reloaded := newServiceOn(t, e.f, st)
		if err := reloaded.ensureTodosLoaded(context.Background()); err != nil {
			t.Fatal(err)
		}
		check(t, reloaded, from, to, target.ID, source.ID)
	})
}
