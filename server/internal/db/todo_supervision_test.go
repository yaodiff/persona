package db

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestReviewTodoCommitsLedgerAndClosureTogether(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		for _, ledger := range []bool{true, false} {
			name := "cancelled"
			if accepted {
				name = "accepted"
			}
			if ledger {
				name += "/supervised"
			} else {
				name += "/no ledger"
			}
			t.Run(name, func(t *testing.T) {
				d, ctx := open(t), context.Background()
				at := time.Now().UTC().Truncate(time.Microsecond)
				before := TodoRow{ID: "review", Title: "Check the change", Origin: "main", CreatedAt: at, UpdatedAt: at}
				if err := d.CreateTodo(ctx, before); err != nil {
					t.Fatal(err)
				}
				var next *TodoSupervisionRow
				if ledger {
					current := TodoSupervisionRow{
						TodoID: before.ID, Supervisor: "main", SupervisorSource: "explicit",
						State: "pending_review", UpdatedAt: at, CardlessToldAt: &at,
					}
					if err := d.PutTodoSupervision(ctx, current); err != nil {
						t.Fatal(err)
					}
					current.State, current.Reviews = "accepted", json.RawMessage(`[{"verdict":"accepted"}]`)
					if !accepted {
						current.State, current.Reviews = "cancelled", json.RawMessage(`[{"verdict":"cancelled"}]`)
					}
					current.CardlessToldAt = nil // a terminal review clears the pending round's hand-back receipt
					next = &current
				}
				ledgerBefore, err := d.ListTodoSupervision(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if ledger && (len(ledgerBefore) != 1 || ledgerBefore[0].SupervisorSource != "explicit" || ledgerBefore[0].CardlessToldAt == nil || !ledgerBefore[0].CardlessToldAt.Equal(at)) {
					t.Fatalf("pending review lost its supervisor source or hand-back receipt: %+v", ledgerBefore)
				}
				refused := errors.New("owner became active before commit")
				checked := false
				row, closed, err := d.ReviewTodo(ctx, before.ID, "Reviewed", accepted, at.Add(time.Second), next, func() error {
					checked = true
					return refused
				})
				if !checked || !errors.Is(err, refused) || closed || row != nil {
					t.Fatalf("refused commit = %+v %v %v, checked=%v", row, closed, err, checked)
				}
				stored, err := d.GetTodo(ctx, before.ID)
				if err != nil || stored.DoneAt != nil || stored.DoneBy != "" {
					t.Fatalf("closure escaped rollback: %+v %v", stored, err)
				}
				ledgerAfter, err := d.ListTodoSupervision(ctx)
				if err != nil || !reflect.DeepEqual(ledgerBefore, ledgerAfter) {
					t.Fatalf("ledger escaped rollback: before=%+v after=%+v err=%v", ledgerBefore, ledgerAfter, err)
				}
				row, closed, err = d.ReviewTodo(ctx, before.ID, "Reviewed", accepted, at.Add(2*time.Second), next, func() error { return nil })
				want := DoneByCancelled
				if accepted {
					want = DoneByReview
				}
				if err != nil || !closed || row == nil || row.DoneAt == nil || row.DoneBy != want {
					t.Fatalf("successful commit = %+v %v %v", row, closed, err)
				}
				ledgerAfter, err = d.ListTodoSupervision(ctx)
				if err != nil || ledger && (len(ledgerAfter) != 1 || ledgerAfter[0].State != next.State ||
					ledgerAfter[0].SupervisorSource != next.SupervisorSource || ledgerAfter[0].CardlessToldAt != nil) ||
					!ledger && len(ledgerAfter) != 0 {
					t.Fatalf("successful commit lost the ledger: %+v %v", ledgerAfter, err)
				}
			})
		}
	}
}

// A supervised task's ledger is written whole, read back, and goes with its todo.
func TestTodoSupervision(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := d.CreateTodo(ctx, TodoRow{ID: "t1", Title: "部署", CreatedAt: now, UpdatedAt: now, Origin: "main"}); err != nil {
		t.Fatal(err)
	}
	r := TodoSupervisionRow{TodoID: "t1", Supervisor: "main", DoneWhen: []string{"PR 合并", "线上验证"}, ExpectMinutes: 30, UpdatedAt: now}
	if err := d.PutTodoSupervision(ctx, r); err != nil {
		t.Fatal(err)
	}
	r.SupervisorSource = "explicit"
	r.State, r.Round, r.Reviews = "rework", 1, json.RawMessage(`[{"verdict":"rework"}]`)
	if err := d.PutTodoSupervision(ctx, r); err != nil {
		t.Fatal(err)
	}
	list, err := d.ListTodoSupervision(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	got := list[0]
	if got.SupervisorSource != "explicit" || got.Supervisor != "main" || len(got.DoneWhen) != 2 || got.DoneWhen[1] != "线上验证" || got.ExpectMinutes != 30 ||
		got.State != "rework" || got.Round != 1 || !json.Valid(got.Reviews) || !got.UpdatedAt.Equal(now) {
		t.Fatalf("row = %+v (%s)", got, got.Reviews)
	}
	if got.AutoReview != nil {
		t.Fatalf("no review ran, auto_review = %s", got.AutoReview)
	}
	r.AutoReview = json.RawMessage(`{"id":"rv_1","state":"running","session_id":"s9"}`)
	if err := d.PutTodoSupervision(ctx, r); err != nil {
		t.Fatal(err)
	}
	if list, err = d.ListTodoSupervision(ctx); err != nil || len(list) != 1 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	var run struct{ ID, State, SessionID string }
	if err := json.Unmarshal(list[0].AutoReview, &run); err != nil || run.ID != "rv_1" || run.State != "running" {
		t.Fatalf("auto_review = %s (%v)", list[0].AutoReview, err)
	}
	r.AutoReview = nil // written without a run: NULL again
	if err := d.PutTodoSupervision(ctx, r); err != nil {
		t.Fatal(err)
	}
	if list, _ = d.ListTodoSupervision(ctx); len(list) != 1 || list[0].AutoReview != nil {
		t.Fatalf("after a write without a run: %+v", list)
	}
	// The work stopped on the owner's decision (#1277): kept, then cleared.
	r.Waits = json.RawMessage(`{"work":"2026-11-11T08:00:00Z","at":"2026-11-11T09:00:00Z","reason":"等用户挑一版"}`)
	if err := d.PutTodoSupervision(ctx, r); err != nil {
		t.Fatal(err)
	}
	var wait struct{ Reason string }
	if list, _ = d.ListTodoSupervision(ctx); len(list) != 1 || json.Unmarshal(list[0].Waits, &wait) != nil || wait.Reason != "等用户挑一版" {
		t.Fatalf("waits = %+v", list)
	}
	r.Waits = nil
	if err := d.PutTodoSupervision(ctx, r); err != nil {
		t.Fatal(err)
	}
	if list, _ = d.ListTodoSupervision(ctx); len(list) != 1 || list[0].Waits != nil {
		t.Fatalf("after the wait cleared: %+v", list)
	}
	// When its review began: kept while it waits, NULL again once it is over.
	began := now.Add(-20 * time.Minute)
	r.State, r.PendingSince = "pending_review", &began
	if err := d.PutTodoSupervision(ctx, r); err != nil {
		t.Fatal(err)
	}
	if list, _ = d.ListTodoSupervision(ctx); len(list) != 1 || list[0].PendingSince == nil || !list[0].PendingSince.Equal(began) {
		t.Fatalf("pending_since = %+v, want %v", list, began)
	}
	r.State, r.PendingSince = "rework", nil
	if err := d.PutTodoSupervision(ctx, r); err != nil {
		t.Fatal(err)
	}
	if list, _ = d.ListTodoSupervision(ctx); len(list) != 1 || list[0].PendingSince != nil {
		t.Fatalf("after the review: %+v", list)
	}
	if err := d.PutTodoSupervision(ctx, TodoSupervisionRow{TodoID: "nope", Supervisor: "main", UpdatedAt: now}); !errors.Is(err, ErrTodoNotFound) {
		t.Fatalf("missing todo: %v", err)
	}
	row, ok, err := d.ReviewDoneTodo(ctx, "t1", "验收通过", true, now)
	if err != nil || !ok || row.DoneBy != DoneByReview || row.DoneReason != "验收通过" || row.DoneAt == nil {
		t.Fatalf("ReviewDoneTodo = %+v, %v, %v", row, ok, err)
	}
	if _, ok, err := d.ReviewDoneTodo(ctx, "t1", "again", true, now); err != nil || ok {
		t.Fatalf("done twice: %v, %v", ok, err)
	}
	if _, _, err := d.ReviewDoneTodo(ctx, "nope", "x", true, now); !errors.Is(err, ErrTodoNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if err := d.DeleteTodo(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if list, err := d.ListTodoSupervision(ctx); err != nil || len(list) != 0 {
		t.Fatalf("after delete: %+v, %v", list, err)
	}
}

func TestReviewDoneTodoCancellation(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := d.CreateTodo(ctx, TodoRow{ID: "cancelled", Title: "Cancelled task", CreatedAt: now, UpdatedAt: now, Origin: "main"}); err != nil {
		t.Fatal(err)
	}
	row, closed, err := d.ReviewDoneTodo(ctx, "cancelled", "No longer needed", false, now)
	if err != nil || !closed || row.DoneBy != DoneByCancelled || row.DoneReason != "No longer needed" || row.DoneAt == nil || !row.DoneAt.Equal(now) {
		t.Fatalf("cancel: row=%+v closed=%v err=%v", row, closed, err)
	}
	// A late accepted review cannot replace a cancellation without an explicit reopen.
	if _, closed, err := d.ReviewDoneTodo(ctx, "cancelled", "Accepted later", true, now.Add(time.Minute)); err != nil || closed {
		t.Fatalf("review after cancellation: closed=%v err=%v", closed, err)
	}
	stored, err := d.GetTodo(ctx, "cancelled")
	if err != nil || stored.DoneBy != DoneByCancelled || stored.DoneReason != "No longer needed" || stored.DoneAt == nil || !stored.DoneAt.Equal(now) {
		t.Fatalf("persisted cancellation: row=%+v err=%v", stored, err)
	}
	done := false
	reopenedAt := now.Add(2 * time.Minute)
	if _, err := d.UpdateTodo(ctx, "cancelled", nil, nil, &done, reopenedAt); err != nil {
		t.Fatal(err)
	}
	stored, err = d.GetTodo(ctx, "cancelled")
	if err != nil || stored.DoneAt != nil || stored.DoneBy != "" || stored.DoneReason != "" || stored.ReopenedAt == nil || !stored.ReopenedAt.Equal(reopenedAt) {
		t.Fatalf("persisted reopen: row=%+v err=%v", stored, err)
	}
	if row, closed, err := d.ReviewDoneTodo(ctx, "cancelled", "Accepted after more work", true, reopenedAt.Add(time.Minute)); err != nil || !closed || row.DoneBy != DoneByReview {
		t.Fatalf("accept reopened task: row=%+v closed=%v err=%v", row, closed, err)
	}
	if _, _, err := d.ReviewDoneTodo(ctx, "missing", "No longer needed", false, now); !errors.Is(err, ErrTodoNotFound) {
		t.Fatalf("cancel missing task: %v", err)
	}
}

func TestReviewDoneTodoClosesAFailedStart(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	for _, tc := range []struct {
		name     string
		id       string
		accepted bool
		doneBy   string
	}{
		{name: "cancelled", id: "failed_cancelled", accepted: false, doneBy: DoneByCancelled},
		{name: "accepted", id: "failed_accepted", accepted: true, doneBy: DoneByReview},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := d.CreateTodo(ctx, TodoRow{ID: tc.id, Title: "Supervised task", CreatedAt: now, UpdatedAt: now, Origin: "main"}); err != nil {
				t.Fatal(err)
			}
			failed, err := d.FailTodo(ctx, tc.id, "synthetic start failure", now)
			if err != nil || failed.StartError == "" || failed.DoneAt == nil || failed.DoneBy != "" {
				t.Fatalf("FailTodo = %+v, %v", failed, err)
			}
			// Without a review the genuine failed start stays visible as one.
			stored, err := d.GetTodo(ctx, tc.id)
			if err != nil || stored.StartError != "synthetic start failure" || stored.DoneBy != "" {
				t.Fatalf("unreviewed failed start = %+v, %v", stored, err)
			}
			closedAt := now.Add(time.Minute)
			row, closed, err := d.ReviewDoneTodo(ctx, tc.id, "reviewed after the start failed", tc.accepted, closedAt)
			if err != nil || !closed || row == nil {
				t.Fatalf("ReviewDoneTodo = %+v, closed=%v, err=%v", row, closed, err)
			}
			if row.StartError != "" || row.DoneBy != tc.doneBy || row.DoneAt == nil || !row.DoneAt.Equal(closedAt) {
				t.Fatalf("closed failed start = %+v", row)
			}
			stored, err = d.GetTodo(ctx, tc.id)
			if err != nil || stored.StartError != row.StartError || stored.DoneBy != row.DoneBy || stored.DoneAt == nil || !stored.DoneAt.Equal(*row.DoneAt) {
				t.Fatalf("stored failed start = %+v, returned = %+v, err=%v", stored, row, err)
			}
		})
	}
}

func TestReviewDoneTodoDoesNotReplaceAnOrdinaryClose(t *testing.T) {
	d, ctx := open(t), context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := d.CreateTodo(ctx, TodoRow{ID: "ordinary_close", Title: "Ordinary task", CreatedAt: now, UpdatedAt: now, Origin: "main"}); err != nil {
		t.Fatal(err)
	}
	row, closed, err := d.ReviewDoneTodo(ctx, "ordinary_close", "accepted first", true, now)
	if err != nil || !closed || row.StartError != "" {
		t.Fatalf("first close = %+v, closed=%v, err=%v", row, closed, err)
	}
	for _, accepted := range []bool{false, true} {
		if _, closed, err := d.ReviewDoneTodo(ctx, "ordinary_close", "late review", accepted, now.Add(time.Minute)); err != nil || closed {
			t.Fatalf("late review accepted=%v: closed=%v, err=%v", accepted, closed, err)
		}
	}
	stored, err := d.GetTodo(ctx, "ordinary_close")
	if err != nil || stored.DoneBy != DoneByReview || stored.DoneReason != "accepted first" || stored.DoneAt == nil || !stored.DoneAt.Equal(now) {
		t.Fatalf("late reviews replaced the first close: %+v, %v", stored, err)
	}
}
