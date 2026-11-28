package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A concern (#3561) closes only through ReportConcern: no edit, judge, review or failed start closes it, and the report
// and the close are one write.
func TestConcernClosesOnlyByItsReport(t *testing.T) {
	d := open(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	if err := d.CreateTodo(ctx, TodoRow{ID: "todo_concern", Title: "发版会不会拖到下周", CreatedAt: base, UpdatedAt: base,
		Origin: "main_example", Kind: KindConcern}); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateTodo(ctx, TodoRow{ID: "todo_plain", Title: "x", CreatedAt: base, UpdatedAt: base}); err != nil {
		t.Fatal(err)
	}
	if r, err := d.GetTodo(ctx, "todo_concern"); err != nil || r.Kind != KindConcern {
		t.Fatalf("kind not kept: %+v %v", r, err)
	}

	done := true
	if _, err := d.EditTodo(ctx, "todo_concern", TodoEdit{Done: &done}, base.Add(time.Minute)); !errors.Is(err, ErrConcernUnreported) {
		t.Fatalf("closed by hand: %v", err)
	}
	if _, ok, err := d.AutoDoneTodo(ctx, "todo_concern", "looks finished", base.Add(time.Minute), base, nil); err != nil || ok {
		t.Fatalf("closed by the judge: %v %v", ok, err)
	}
	for _, accepted := range []bool{true, false} {
		if _, ok, err := d.ReviewDoneTodo(ctx, "todo_concern", "accepted", accepted, base.Add(time.Minute)); err != nil || ok {
			t.Fatalf("closed by a review (accepted %v): %v %v", accepted, ok, err)
		}
	}
	if r, err := d.FailTodo(ctx, "todo_concern", "did not start", base.Add(time.Minute)); err != nil || r.DoneAt != nil || r.StartError != "" {
		t.Fatalf("closed by a failed start: %+v %v", r, err)
	}
	if _, err := d.InsertTodoDecision(ctx, TodoDecisionRow{ID: "d_sneak", TodoID: "todo_concern", Question: "q", Choice: "c",
		Source: DecisionReported, CreatedAt: base.Add(time.Minute)}); err == nil {
		t.Fatal("a report written without closing")
	}
	if r, _ := d.GetTodo(ctx, "todo_concern"); r.DoneAt != nil {
		t.Fatalf("closed before its report: %+v", r)
	}

	report := func(id string, at time.Time) (*TodoRow, bool, error) {
		return d.ReportConcern(ctx, TodoDecisionRow{ID: id, TodoID: "todo_concern", Question: "发版会不会拖到下周",
			Choice: "不会，周四发", Source: DecisionReported, SourceSession: "main_example", CreatedAt: at}, "不会，周四发")
	}
	if _, _, err := d.ReportConcern(ctx, TodoDecisionRow{ID: "d_plain", TodoID: "todo_plain", Question: "q", Choice: "c",
		Source: DecisionReported, CreatedAt: base}, "c"); !errors.Is(err, ErrNotConcern) {
		t.Fatalf("a plain to-do reported: %v", err)
	}
	r, closed, err := report("d_report_1", base.Add(2*time.Minute))
	if err != nil || !closed || r.DoneAt == nil || r.DoneBy != DoneByReported || r.DoneReason != "不会，周四发" {
		t.Fatalf("report: %+v %v %v", r, closed, err)
	}
	list, _ := d.ListTodoDecisions(ctx, "todo_concern", false)
	if len(list) != 1 || list[0].Source != DecisionReported || list[0].Choice != "不会，周四发" {
		t.Fatalf("decisions: %+v", list)
	}

	// Reopened (the owner asks again): closed again only by another report.
	undone := false
	if r, err = d.EditTodo(ctx, "todo_concern", TodoEdit{Done: &undone}, base.Add(3*time.Minute)); err != nil || r.DoneAt != nil {
		t.Fatalf("reopen: %+v %v", r, err)
	}
	if _, err := d.EditTodo(ctx, "todo_concern", TodoEdit{Done: &done}, base.Add(4*time.Minute)); !errors.Is(err, ErrConcernUnreported) {
		t.Fatalf("closed by hand after its first report: %v", err)
	}
	if r, closed, err = report("d_report_2", base.Add(5*time.Minute)); err != nil || !closed || r.DoneAt == nil {
		t.Fatalf("second report: %+v %v %v", r, closed, err)
	}
	// A report on a closed concern is kept; the close stays as it was.
	if r, closed, err = report("d_report_3", base.Add(6*time.Minute)); err != nil || closed || !r.DoneAt.Equal(base.Add(5*time.Minute)) {
		t.Fatalf("report on a closed concern: %+v %v %v", r, closed, err)
	}
	if _, _, err := d.ReportConcern(ctx, TodoDecisionRow{ID: "d_gone", TodoID: "todo_gone", Question: "q", Choice: "c",
		Source: DecisionReported, CreatedAt: base}, "c"); !errors.Is(err, ErrTodoNotFound) {
		t.Fatalf("a gone todo: %v", err)
	}
}
