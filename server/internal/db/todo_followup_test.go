package db

import (
	"context"
	"testing"
	"time"
)

func TestTodoSupervisorFollowupRoundTrip(t *testing.T) {
	d, ctx := open(t), context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	if err := d.CreateTodo(ctx, TodoRow{ID: "followup", Title: "Check the release", CreatedAt: at, UpdatedAt: at, Origin: "main"}); err != nil {
		t.Fatal(err)
	}
	r := TodoSupervisionRow{TodoID: "followup", Supervisor: "main", State: "pending_review", UpdatedAt: at, FollowedUpAt: &at}
	if err := d.PutTodoSupervision(ctx, r); err != nil {
		t.Fatal(err)
	}
	rows, err := d.ListTodoSupervision(ctx)
	if err != nil || len(rows) != 1 || rows[0].FollowedUpAt == nil || !rows[0].FollowedUpAt.Equal(at) {
		t.Fatalf("persisted follow-up: %+v, %v", rows, err)
	}
	r.FollowedUpAt = nil
	if err := d.PutTodoSupervision(ctx, r); err != nil {
		t.Fatal(err)
	}
	rows, err = d.ListTodoSupervision(ctx)
	if err != nil || len(rows) != 1 || rows[0].FollowedUpAt != nil {
		t.Fatalf("cleared follow-up: %+v, %v", rows, err)
	}
}
