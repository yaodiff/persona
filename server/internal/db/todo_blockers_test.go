package db

import (
	"context"
	"testing"
	"time"
)

func TestTodoBlockersCRUDLifecycleAndCascade(t *testing.T) {
	d := open(t)
	ctx := context.Background()

	if _, err := d.pool.Exec(ctx, `INSERT INTO todos (id, title) VALUES ('todo_b1', 'Wait on PR'), ('todo_b2', 'Wait on same PR')`); err != nil {
		t.Fatal(err)
	}

	t1 := time.Date(2026, 11, 25, 16, 0, 0, 0, time.UTC)
	prTarget := "https://github.com/example/repo/pull/42"

	b1, err := d.UpsertTodoBlocker(ctx, TodoBlockerRow{
		ID:            "blk_1",
		TodoID:        "todo_b1",
		Kind:          "external_pr",
		TargetRef:     prTarget,
		ConditionJSON: `{"require_merged":true}`,
		CreatedAt:     t1,
		UpdatedAt:     t1,
	})
	if err != nil {
		t.Fatalf("UpsertTodoBlocker b1: %v", err)
	}
	if b1 == nil || b1.State != TodoBlockerStateActive || b1.Attempts != 0 || b1.ResolvedAt != nil {
		t.Fatalf("unexpected b1: %+v", b1)
	}

	b2, err := d.UpsertTodoBlocker(ctx, TodoBlockerRow{
		ID:        "blk_2",
		TodoID:    "todo_b2",
		Kind:      "external_pr",
		TargetRef: prTarget,
		CreatedAt: t1.Add(time.Second),
		UpdatedAt: t1.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("UpsertTodoBlocker b2: %v", err)
	}
	if b2.ConditionJSON != "{}" {
		t.Fatalf("expected default ConditionJSON '{}', got %q", b2.ConditionJSON)
	}

	b3, err := d.UpsertTodoBlocker(ctx, TodoBlockerRow{
		ID:        "blk_3",
		TodoID:    "todo_b1",
		Kind:      "merge_train",
		TargetRef: "github.com/example/repo",
		CreatedAt: t1.Add(2 * time.Second),
		UpdatedAt: t1.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatalf("UpsertTodoBlocker b3: %v", err)
	}
	if b3.ID != "blk_3" {
		t.Fatalf("unexpected b3: %+v", b3)
	}

	// Upsert updates existing row by id.
	b3Updated, err := d.UpsertTodoBlocker(ctx, TodoBlockerRow{
		ID:            "blk_3",
		TodoID:        "todo_b1",
		Kind:          "merge_train",
		TargetRef:     "github.com/example/repo",
		ConditionJSON: `{"max_attempts":2}`,
		State:         TodoBlockerStateActive,
		Attempts:      1,
		CreatedAt:     t1.Add(2 * time.Second),
		UpdatedAt:     t1.Add(3 * time.Second),
	})
	if err != nil || b3Updated.Attempts != 1 || b3Updated.ConditionJSON != `{"max_attempts":2}` {
		t.Fatalf("UpsertTodoBlocker update mismatch: row=%+v err=%v", b3Updated, err)
	}

	active, err := d.ListActiveTodoBlockers(ctx)
	if err != nil {
		t.Fatalf("ListActiveTodoBlockers: %v", err)
	}
	if len(active) != 3 || active[0].ID != "blk_1" || active[1].ID != "blk_2" || active[2].ID != "blk_3" {
		t.Fatalf("unexpected active list: %+v", active)
	}

	byTarget, err := d.ListActiveTodoBlockersByTarget(ctx, prTarget)
	if err != nil {
		t.Fatalf("ListActiveTodoBlockersByTarget: %v", err)
	}
	if len(byTarget) != 2 || byTarget[0].ID != "blk_1" || byTarget[1].ID != "blk_2" {
		t.Fatalf("unexpected byTarget list: %+v", byTarget)
	}

	byTargetMixed, err := d.ListActiveTodoBlockersByTarget(ctx, "HTTPS://GitHub.COM/Example/Repo/pull/42")
	if err != nil {
		t.Fatalf("ListActiveTodoBlockersByTarget mixed-case: %v", err)
	}
	if len(byTargetMixed) != 2 || byTargetMixed[0].ID != "blk_1" || byTargetMixed[1].ID != "blk_2" {
		t.Fatalf("unexpected mixed-case byTarget list: %+v", byTargetMixed)
	}

	// Increment attempts persists across reads.
	inc, err := d.IncrementTodoBlockerAttempts(ctx, "blk_3", t1.Add(4*time.Second))
	if err != nil || inc == nil || inc.Attempts != 2 {
		t.Fatalf("IncrementTodoBlockerAttempts: row=%+v err=%v", inc, err)
	}

	// Resolve blk_1 removes it from active lists while retaining it on ListTodoBlockersByTodo.
	resolvedAt := t1.Add(5 * time.Second)
	res, err := d.ResolveTodoBlocker(ctx, "blk_1", resolvedAt)
	if err != nil || res == nil || res.State != TodoBlockerStateResolved || res.ResolvedAt == nil {
		t.Fatalf("ResolveTodoBlocker: row=%+v err=%v", res, err)
	}

	// Cancel blk_3 transitions it to cancelled.
	cancelledAt := t1.Add(6 * time.Second)
	canc, err := d.CancelTodoBlocker(ctx, "blk_3", cancelledAt)
	if err != nil || canc == nil || canc.State != TodoBlockerStateCancelled || canc.ResolvedAt == nil {
		t.Fatalf("CancelTodoBlocker: row=%+v err=%v", canc, err)
	}
	if row, err := d.CancelTodoBlocker(ctx, res.ID, cancelledAt); err != nil || row != nil {
		t.Fatalf("cancel overwrote a resolved row: %+v, %v", row, err)
	}
	if row, err := d.ResolveTodoBlocker(ctx, canc.ID, resolvedAt); err != nil || row != nil {
		t.Fatalf("resolve overwrote a cancelled row: %+v, %v", row, err)
	}
	if row, err := d.IncrementTodoBlockerAttempts(ctx, canc.ID, cancelledAt); err != nil || row != nil {
		t.Fatalf("increment overwrote a cancelled row: %+v, %v", row, err)
	}

	// Missing id returns nil, nil.
	if missing, err := d.ResolveTodoBlocker(ctx, "blk_missing", resolvedAt); err != nil || missing != nil {
		t.Fatalf("ResolveTodoBlocker missing: row=%+v err=%v", missing, err)
	}
	if missing, err := d.CancelTodoBlocker(ctx, "blk_missing", cancelledAt); err != nil || missing != nil {
		t.Fatalf("CancelTodoBlocker missing: row=%+v err=%v", missing, err)
	}
	if missing, err := d.IncrementTodoBlockerAttempts(ctx, "blk_missing", cancelledAt); err != nil || missing != nil {
		t.Fatalf("IncrementTodoBlockerAttempts missing: row=%+v err=%v", missing, err)
	}

	activeAfter, err := d.ListActiveTodoBlockers(ctx)
	if err != nil || len(activeAfter) != 1 || activeAfter[0].ID != "blk_2" {
		t.Fatalf("expected only blk_2 active, got %+v err=%v", activeAfter, err)
	}

	byTodo1, err := d.ListTodoBlockersByTodo(ctx, "todo_b1")
	if err != nil || len(byTodo1) != 2 || byTodo1[0].State != TodoBlockerStateResolved || byTodo1[1].State != TodoBlockerStateCancelled {
		t.Fatalf("unexpected byTodo1: %+v err=%v", byTodo1, err)
	}

	// Deleting parent todo cascades to todo_blockers.
	if _, err := d.pool.Exec(ctx, `DELETE FROM todos WHERE id = 'todo_b1'`); err != nil {
		t.Fatal(err)
	}
	afterCascade, err := d.ListTodoBlockersByTodo(ctx, "todo_b1")
	if err != nil || len(afterCascade) != 0 {
		t.Fatalf("expected cascade delete to clear todo_b1 blockers, got %+v err=%v", afterCascade, err)
	}
}
