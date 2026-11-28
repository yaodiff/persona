package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestTodoAutoSeenUndoGuard(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)

	if err := d.CreateTodo(ctx, TodoRow{
		ID:        "todo_auto_seen_1",
		Title:     "Fix flaky build",
		Origin:    "conv_main",
		Audience:  AudienceAutonomous,
		CreatedAt: base,
		UpdatedAt: base,
		DoneAt:    &base,
	}); err != nil {
		t.Fatalf("CreateTodo: %v", err)
	}

	got, err := d.GetTodo(ctx, "todo_auto_seen_1")
	if err != nil {
		t.Fatalf("GetTodo: %v", err)
	}
	if got.SeenAt != nil || got.AutoSeenUndone {
		t.Fatalf("initial row: seen_at=%v auto_seen_undone=%t", got.SeenAt, got.AutoSeenUndone)
	}

	// AutoSeeTodo applies when auto_seen_undone is false and leaves updated_at untouched.
	autoAt := base.Add(time.Minute)
	row, applied, err := d.AutoSeeTodo(ctx, "todo_auto_seen_1", base, nil, autoAt)
	if err != nil || !applied || row == nil || row.SeenAt == nil || !row.SeenAt.Equal(autoAt) || row.AutoSeenUndone {
		t.Fatalf("AutoSeeTodo initial: row=%+v applied=%t err=%v", row, applied, err)
	}
	if !row.UpdatedAt.Equal(base) {
		t.Fatalf("AutoSeeTodo changed updated_at: got %v, want %v", row.UpdatedAt, base)
	}

	// Manual Undo (SeeTodo with nil) clears seen_at and persists auto_seen_undone = true.
	undone, err := d.SeeTodo(ctx, "todo_auto_seen_1", nil)
	if err != nil || undone == nil || undone.SeenAt != nil || !undone.AutoSeenUndone {
		t.Fatalf("SeeTodo(nil) undo: row=%+v err=%v", undone, err)
	}
	if !undone.UpdatedAt.Equal(base) {
		t.Fatalf("SeeTodo(nil) changed updated_at: got %v, want %v", undone.UpdatedAt, base)
	}

	// Subsequent AutoSeeTodo must never overwrite manual Undo.
	row, applied, err = d.AutoSeeTodo(ctx, "todo_auto_seen_1", base, nil, base.Add(2*time.Minute))
	if err != nil || applied || row != nil {
		t.Fatalf("AutoSeeTodo after undo: row=%+v applied=%t err=%v, want (nil, false, nil)", row, applied, err)
	}
	got, err = d.GetTodo(ctx, "todo_auto_seen_1")
	if err != nil || got.SeenAt != nil || !got.AutoSeenUndone {
		t.Fatalf("GetTodo after blocked AutoSeeTodo: got=%+v err=%v", got, err)
	}

	// Manual PUT /seen (SeeTodo with non-nil time) sets seen_at without clearing auto_seen_undone.
	manualAt := base.Add(3 * time.Minute)
	manual, err := d.SeeTodo(ctx, "todo_auto_seen_1", &manualAt)
	if err != nil || manual == nil || manual.SeenAt == nil || !manual.SeenAt.Equal(manualAt) || !manual.AutoSeenUndone {
		t.Fatalf("SeeTodo(manual) after undo: row=%+v err=%v", manual, err)
	}

	// Missing to-do returns ErrTodoNotFound.
	if _, _, err := d.AutoSeeTodo(ctx, "todo_missing", base, nil, autoAt); !errors.Is(err, ErrTodoNotFound) {
		t.Fatalf("AutoSeeTodo missing: err=%v, want ErrTodoNotFound", err)
	}
}

func TestTodoAutoSeenRejectsStaleCompletionRound(t *testing.T) {
	d := open(t)
	ctx := t.Context()
	firstDone := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	const id = "todo_seen_round"
	if err := d.CreateTodo(ctx, TodoRow{
		ID: id, Title: "Review the result", CreatedAt: firstDone, UpdatedAt: firstDone, DoneAt: &firstDone,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.SeeTodo(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	reopenedAt := firstDone.Add(time.Minute)
	open := false
	reopened, err := d.EditTodo(ctx, id, TodoEdit{Done: &open}, reopenedAt)
	if err != nil || reopened.DoneAt != nil || reopened.AutoSeenUndone || reopened.SeenAt != nil {
		t.Fatalf("reopen should clear completion marks: row=%+v err=%v", reopened, err)
	}
	seenAt := reopenedAt.Add(time.Minute)
	if row, applied, err := d.AutoSeeTodo(ctx, id, firstDone, nil, seenAt); err != nil || applied || row != nil {
		t.Fatalf("stale completion matched an open to-do: row=%+v applied=%t err=%v", row, applied, err)
	}

	done := true
	if _, err := d.EditTodo(ctx, id, TodoEdit{Done: &done}, firstDone); err != nil {
		t.Fatal(err)
	}
	// Reuse the first completion timestamp to exercise both parts of the round guard. The stale auto-seen
	// statement must wait on the uncommitted row and then evaluate the predicate against the new round.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stale := make(chan error, 1)
	nextRound := reopenedAt.Add(time.Minute)
	err = d.Transaction(ctx, func(tx pgx.Tx) error {
		if _, err := EditTodoTx(ctx, tx, id, TodoEdit{Done: &open}, nextRound); err != nil {
			return err
		}
		if _, err := EditTodoTx(ctx, tx, id, TodoEdit{Done: &done}, firstDone); err != nil {
			return err
		}
		go func() {
			row, applied, err := d.AutoSeeTodo(ctx, id, firstDone, &reopenedAt, seenAt)
			if err == nil && (applied || row != nil) {
				err = errors.New("stale completion was marked seen")
			}
			stale <- err
		}()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			var waiting bool
			err := d.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname = current_database()
				AND pid <> pg_backend_pid() AND wait_event_type = 'Lock' AND query LIKE 'UPDATE todos SET seen_at = %')`).Scan(&waiting)
			if err != nil || waiting {
				return err
			}
			select {
			case err := <-stale:
				return errors.Join(errors.New("auto-seen finished before the row lock was released"), err)
			case <-tick.C:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-stale:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if row, applied, err := d.AutoSeeTodo(ctx, id, firstDone, &nextRound, seenAt); err != nil || !applied || row.SeenAt == nil {
		t.Fatalf("current round did not become seen: row=%+v applied=%t err=%v", row, applied, err)
	}
}

func TestTodoAutoSeenMigrationUpgrade(t *testing.T) {
	ctx := context.Background()
	url := freshDatabase(t)

	// Migrate up to right before 20261124091500_todo_auto_seen_undone.sql.
	before := buildOf(t, func(v int64) bool { return v < 20261124091500 }, nil)
	d, err := Attach(ctx, url, ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.migrateFrom(ctx, before); err != nil {
		t.Fatalf("migrate before auto_seen_undone: %v", err)
	}

	base := time.Date(2026, 11, 24, 9, 0, 0, 0, time.UTC)
	if _, err := d.pool.Exec(ctx, `INSERT INTO todos (id, title, notes, created_at, updated_at, origin, audience)
		VALUES ($1, $2, $3, $4, $4, $5, $6)`, "todo_pre_auto_seen", "Existing task", "keep", base, "conv_legacy", AudienceAutonomous); err != nil {
		t.Fatalf("insert pre-migration row: %v", err)
	}

	if err := d.migrateFrom(ctx, withMigrations(t, nil)); err != nil {
		t.Fatalf("migrate with auto_seen_undone: %v", err)
	}

	got, err := d.GetTodo(ctx, "todo_pre_auto_seen")
	if err != nil {
		t.Fatalf("GetTodo after upgrade: %v", err)
	}
	if got.Title != "Existing task" || got.Audience != AudienceAutonomous || got.AutoSeenUndone {
		t.Fatalf("upgraded row = %+v", got)
	}
}
