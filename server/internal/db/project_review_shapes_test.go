package db

import (
	"encoding/json"
	"testing"
	"time"
)

// The migration's shapes and closed outcome set accept what the writers store and reject corrupted rows.
func TestProjectReviewAndInvestigationStorageShapes(t *testing.T) {
	d, ctx := open(t), t.Context()
	for _, outcome := range checkValues(t, "manager", "project_review_deliveries_outcome_check") {
		queued, err := d.RecordProjectReview(ctx, "review-"+outcome, "target", "binding",
			json.RawMessage(`{"kind":"owner_input"}`), "", time.Now())
		if err != nil || !queued {
			t.Fatalf("record outcome %q: queued=%v err=%v", outcome, queued, err)
		}
		if outcome != "" {
			if err := d.SettleProjectReview(ctx, "review-"+outcome, outcome); err != nil {
				t.Errorf("settle %q: %v", outcome, err)
			}
		}
	}
	if _, err := d.RecordProjectReview(ctx, "bad-shape", "target", "binding",
		json.RawMessage(`[]`), "", time.Now()); err == nil {
		t.Error("an array project-review event was accepted")
	}
	if _, err := d.RecordProjectReview(ctx, "bad-shape", "target", "binding",
		json.RawMessage(`{"kind":"owner_input"}`), "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := d.SettleProjectReview(ctx, "bad-shape", "not_a_recorded_outcome"); err == nil {
		t.Error("an unknown project-review outcome was accepted")
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := d.CreateTodo(ctx, TodoRow{ID: "investigation", Title: "Investigate", Origin: "main", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutTodoSupervision(ctx, TodoSupervisionRow{TodoID: "investigation", Supervisor: "main",
		InvestigationFindings: json.RawMessage(`{"version":1,"items":[]}`), UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutTodoSupervision(ctx, TodoSupervisionRow{TodoID: "investigation", Supervisor: "main",
		InvestigationFindings: json.RawMessage(`[]`), UpdatedAt: now}); err == nil {
		t.Error("an array investigation report was accepted")
	}
}
