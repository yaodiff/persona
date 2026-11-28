package db

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestAutoDoneTodoFinalGuardRollsBackClosure(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	before := TodoRow{ID: "guarded-auto-close", Title: "Check the result", CreatedAt: now, UpdatedAt: now}
	if err := d.CreateTodo(ctx, before); err != nil {
		t.Fatal(err)
	}
	storedBefore, err := d.GetTodo(ctx, before.ID)
	if err != nil {
		t.Fatal(err)
	}
	hold := errors.New("the user became active")
	called := false
	row, closed, err := d.AutoDoneTodo(ctx, before.ID, "Checked", now.Add(time.Minute), now, func() error {
		called = true
		return hold
	})
	if !called || row != nil || closed || !errors.Is(err, hold) {
		t.Fatalf("final guard did not refuse closure: called=%v row=%+v closed=%v err=%v", called, row, closed, err)
	}
	stored, err := d.GetTodo(ctx, before.ID)
	if err != nil || !reflect.DeepEqual(stored, storedBefore) {
		t.Fatalf("refused automatic close escaped rollback: before=%+v after=%+v err=%v", storedBefore, stored, err)
	}
	if row, closed, err := d.AutoDoneTodo(ctx, before.ID, "Checked after inactivity", now.Add(2*time.Minute), now, nil); err != nil || !closed || row.DoneAt == nil {
		t.Fatalf("automatic close could not retry: row=%+v closed=%v err=%v", row, closed, err)
	}
}
