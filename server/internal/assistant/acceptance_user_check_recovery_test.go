package assistant

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

type userCheckPassStore struct {
	*db.DB
	closeCalls       atomic.Int32
	ledgerWrites     atomic.Int32
	acceptanceWrites atomic.Int32
	stageWrites      atomic.Int32
	failLedger       atomic.Bool
}

func TestUserCheckRecoversMissingPassAsk(t *testing.T) {
	ctx := t.Context()
	for _, scenario := range []string{"missing", "previously auto accepted"} {
		t.Run(scenario, func(t *testing.T) {
			store := todoTestDB(t)
			s, hub, todo := acceptedForUserCheckOn(t, store)
			old := askOf(s, todo.ID)
			asks := make(map[string]*acceptAsk)
			if scenario == "previously auto accepted" {
				previous := *old
				previous.AcceptedAt, previous.ByAssistant = previous.AskedAt, true
				asks[todo.ID] = &previous
			}
			if err := store.PutSetting(ctx, acceptanceKey, asks); err != nil {
				t.Fatal(err)
			}

			restarted := newServiceOn(t, hub, store)
			restarted.verifySupervisedAsks(ctx)
			got := todoNow(restarted, todo.ID)
			fresh := askOf(restarted, todo.ID)
			if got.DoneAt != nil || got.Supervision.state(got) != reviewUserCheck ||
				fresh == nil || !fresh.AcceptedAt.IsZero() || fresh.AskedAt.Equal(old.AskedAt) ||
				fresh.SessionID != old.SessionID {
				t.Fatalf("the pending verification did not recover a usable Pass: todo=%+v ask=%+v", got, fresh)
			}
			again := newServiceOn(t, hub, store)
			again.verifySupervisedAsks(ctx)
			if saved := askOf(again, todo.ID); saved == nil || !saved.AskedAt.Equal(fresh.AskedAt) {
				t.Fatalf("recovery did not keep the same persisted ask: %+v", saved)
			}
			if result, err := again.acceptReviewRound(ctx, todo.ID, old.AskedAt.Format(timeKeyLayout)); err != nil || result.taken {
				t.Fatalf("an older Pass accepted the recovered round: %+v, %v", result, err)
			}
			if result, err := again.acceptReviewRound(ctx, todo.ID, fresh.AskedAt.Format(timeKeyLayout)); err != nil || !result.taken {
				t.Fatalf("the recovered Pass could not complete the task: %+v, %v", result, err)
			}
		})
	}
}

func TestUserCheckReopenRollsBackWithLedgerFailure(t *testing.T) {
	for _, path := range []string{"edit", "reopen"} {
		t.Run(path, func(t *testing.T) {
			ctx := t.Context()
			dsn := newMigratedDatabase(t, db.ManagerSet)
			store, err := db.Open(ctx, dsn, db.ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(store.Close)
			s, hub, todo := acceptedForUserCheckOn(t, store)
			old := askOf(s, todo.ID)
			done, open := true, false
			if _, err := s.editTodo(ctx, todo.ID, db.TodoEdit{Done: &done}); err != nil {
				t.Fatal(err)
			}
			execIn(t, dsn, `CREATE FUNCTION reject_user_check_reset() RETURNS trigger LANGUAGE plpgsql AS $$
				BEGIN RAISE EXCEPTION 'verification reset rejected'; END $$;
				CREATE TRIGGER reject_user_check_reset BEFORE UPDATE ON todo_supervision FOR EACH ROW
				WHEN (OLD.state = 'user_check' AND NEW.state = '') EXECUTE FUNCTION reject_user_check_reset()`)
			reopen := func(service *Service) error {
				if path == "reopen" {
					return service.reopenTodo(ctx, todo.ID)
				}
				_, err := service.editTodo(ctx, todo.ID, db.TodoEdit{Done: &open})
				return err
			}
			if err := reopen(s); err == nil {
				t.Fatal("the failed ledger reset was reported as a successful reopen")
			}
			restarted := newServiceOn(t, hub, store)
			if err := restarted.ensureTodosLoaded(ctx); err != nil {
				t.Fatal(err)
			}
			for _, service := range []*Service{s, restarted} {
				got := todoNow(service, todo.ID)
				if got.DoneAt == nil || got.ReopenedAt != nil || got.Supervision.State != reviewUserCheck {
					t.Fatalf("the failed reset partially reopened the task: %+v", got)
				}
			}
			execIn(t, dsn, `DROP TRIGGER reject_user_check_reset ON todo_supervision`)
			if err := reopen(restarted); err != nil {
				t.Fatal(err)
			}
			reloaded := newServiceOn(t, hub, store)
			reloaded.verifySupervisedAsks(ctx)
			got := todoNow(reloaded, todo.ID)
			if got.DoneAt != nil || got.ReopenedAt == nil || got.Supervision.State != "" ||
				len(got.Supervision.Reviews) != len(todo.Supervision.Reviews) {
				t.Fatalf("the retry did not persist the reopened work and keep its review history: %+v", got)
			}
			if result, err := reloaded.acceptTry(ctx, todo.ID, old.MessageID); err != nil || result.taken {
				t.Fatalf("the old Pass accepted reopened work: %+v, %v", result, err)
			}
			ledgers, err := store.ListTodoSupervision(ctx)
			if err != nil || len(ledgers) != 1 || ledgers[0].State != "" {
				t.Fatalf("the reopened ledger did not commit: %+v, %v", ledgers, err)
			}
		})
	}
}

func TestUserCheckAlreadyReopenedKeepsWorkingAfterRestart(t *testing.T) {
	ctx := t.Context()
	dsn := newMigratedDatabase(t, db.ManagerSet)
	store, err := db.Open(ctx, dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	s, hub, todo := acceptedForUserCheckOn(t, store)
	old := askOf(s, todo.ID)
	// An earlier manager committed only the todo half of the reopen.
	execIn(t, dsn, `UPDATE todos SET reopened_at = now()`)

	restarted := newServiceOn(t, hub, store)
	restarted.verifySupervisedAsks(ctx)
	got := todoNow(restarted, todo.ID)
	if got.DoneAt != nil || got.Supervision.State != "" || got.ReopenedAt == nil {
		t.Fatalf("a stale ledger resurrected personal verification: %+v", got)
	}
	if a := askOf(restarted, todo.ID); a != nil && !a.AskedAt.Equal(old.AskedAt) {
		t.Fatalf("a stale ledger created a fresh Pass: %+v", a)
	}
}

func TestUserCheckPassWaitsForItsOwnRound(t *testing.T) {
	needsLarkLayer(t)
	for _, source := range []string{"web button", "card reply", "recovery sweep"} {
		t.Run(source, func(t *testing.T) {
			ctx := t.Context()
			s, _, todo := acceptedForUserCheck(t)
			old := askOf(s, todo.ID)
			actions := &s.reviewDismissals.actions
			actions.acquire(todo.ID)
			locked := true
			defer func() {
				if locked {
					actions.release(todo.ID)
				}
			}()
			if source == "recovery sweep" {
				s.accept.mu.Lock()
				s.accept.asks[todo.ID].AcceptedAt = time.Now().UTC()
				s.accept.mu.Unlock()
			}
			finished := make(chan error, 1)
			go func() {
				switch source {
				case "recovery sweep":
					s.verifySupervisedAsks(ctx)
					finished <- nil
				case "card reply":
					s.acceptReply(ctx, "om_reply", "Pass", false, old.MessageID)
					finished <- nil
				default:
					_, err := s.acceptReviewRound(ctx, todo.ID, old.AskedAt.Format(timeKeyLayout))
					finished <- err
				}
			}()
			waitFor(t, "the old Pass queued behind the task action", func() bool {
				actions.mu.Lock()
				defer actions.mu.Unlock()
				return actions.m[todo.ID].n == 2
			})
			fresh := &acceptAsk{AskedAt: time.Now().UTC(), MessageID: "om_fresh", Messages: []string{"om_fresh"}}
			s.accept.mu.Lock()
			s.accept.asks[todo.ID] = fresh
			s.accept.mu.Unlock()
			actions.release(todo.ID)
			locked = false
			select {
			case err := <-finished:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the old Pass did not return")
			}
			got, current := todoNow(s, todo.ID), askOf(s, todo.ID)
			if got.DoneAt != nil || current == nil || !current.AcceptedAt.IsZero() {
				t.Fatalf("a previous Pass affected the new verification round: done=%v ask=%+v", got.DoneAt, current)
			}
		})
	}
}

func (d *userCheckPassStore) ReviewTodo(ctx context.Context, id, reason string, accepted bool, at time.Time, ledger *db.TodoSupervisionRow, guard func() error) (*db.TodoRow, bool, error) {
	d.closeCalls.Add(1)
	return d.DB.ReviewTodo(ctx, id, reason, accepted, at, ledger, guard)
}

func (d *userCheckPassStore) PutTodoSupervision(ctx context.Context, row db.TodoSupervisionRow) error {
	if row.State == reviewAccepted {
		d.ledgerWrites.Add(1)
		if d.failLedger.Load() {
			return errors.New("supervision store unavailable")
		}
	}
	return d.DB.PutTodoSupervision(ctx, row)
}

func (d *userCheckPassStore) PutSetting(ctx context.Context, key string, value any) error {
	switch key {
	case acceptanceKey:
		d.acceptanceWrites.Add(1)
	case reportStagesKey:
		d.stageWrites.Add(1)
	}
	return d.DB.PutSetting(ctx, key, value)
}

func TestUserCheckRecoversEveryPersistedPassStep(t *testing.T) {
	needsLarkLayer(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name             string
		done, ledgerDone bool
		verified         bool
	}{
		{name: "before completion"},
		{name: "after completion", done: true},
		{name: "after ledger update", done: true, ledgerDone: true},
		{name: "verified with interrupted ledger write", done: true, verified: true},
		{name: "already finished", done: true, ledgerDone: true, verified: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &userCheckPassStore{DB: todoTestDB(t)}
			s, f, todo := acceptedForUserCheckOn(t, store)
			s.accept.mu.Lock()
			s.accept.asks[todo.ID].AcceptedAt = time.Now().UTC()
			err := s.saveAcceptErrLocked(ctx)
			s.accept.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			acceptedAt := askOf(s, todo.ID).AcceptedAt
			if tc.done {
				if _, err := s.reviewDoneTodo(ctx, todo.ID, "Owner passed", true); err != nil {
					t.Fatal(err)
				}
			}
			if tc.ledgerDone {
				if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
					v.State = reviewAccepted
					return true
				}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.verified {
				s.setTaskStageKind(ctx, todo.ID, "verify", "", "", true)
			}
			doneAt := todoNow(s, todo.ID).DoneAt
			updated := updatedCards(f)
			store.closeCalls.Store(0)
			store.ledgerWrites.Store(0)
			store.acceptanceWrites.Store(0)
			store.stageWrites.Store(0)

			restarted := newServiceOn(t, f, store)
			restarted.verifySupervisedAsks(ctx)
			restarted.verifySupervisedAsks(ctx)
			if got := todoNow(restarted, todo.ID); got.DoneAt == nil || got.Supervision.State != reviewAccepted || !got.stageVerified {
				t.Fatalf("persisted Pass not fully recovered: done=%v ledger=%q stage=%q verified=%v", got.DoneAt, got.Supervision.State, got.Stage, got.stageVerified)
			}
			row, err := store.GetTodo(ctx, todo.ID)
			if err != nil || row.DoneAt == nil || row.DoneBy != db.DoneByReview {
				t.Fatalf("completion not durable: %+v, %v", row, err)
			}
			if doneAt != nil && !row.DoneAt.Equal(*doneAt) {
				t.Fatalf("recovery repeated completion: %v -> %v", doneAt, row.DoneAt)
			}
			if got := askOf(restarted, todo.ID); got == nil || !got.AcceptedAt.Equal(acceptedAt) {
				t.Fatalf("recovery replaced the original owner Pass: %+v", got)
			}
			wantClose, wantLedger, wantVerify := int32(0), int32(0), int32(0)
			if !tc.done {
				wantClose = 1
			}
			if !tc.ledgerDone {
				wantLedger = 1
			}
			if !tc.verified {
				wantVerify = 1
			}
			if got := store.closeCalls.Load(); got != wantClose {
				t.Fatalf("completion calls=%d, want %d", got, wantClose)
			}
			if got := store.ledgerWrites.Load(); got != wantLedger {
				t.Fatalf("ledger writes=%d, want %d", got, wantLedger)
			}
			if got := store.acceptanceWrites.Load(); got != 0 {
				t.Fatalf("rewrote the already-durable owner Pass %d times", got)
			}
			if got := store.stageWrites.Load(); got != wantVerify {
				t.Fatalf("stage writes=%d, want %d", got, wantVerify)
			}
			if got := len(updated()); got != int(wantVerify) {
				t.Fatalf("card updates=%d, want %d", got, wantVerify)
			}
			if result, err := restarted.acceptTry(ctx, todo.ID, ""); err != nil || result.taken {
				t.Fatalf("completed Pass taken twice: %+v, %v", result, err)
			}
			again := newServiceOn(t, f, store)
			again.verifySupervisedAsks(ctx)
			if got := todoNow(again, todo.ID); got.Supervision.State != reviewAccepted || got.Stage != "verify" || !got.stageVerified {
				t.Fatalf("recovered verification was not durable: %+v", got)
			}
			if store.closeCalls.Load() != wantClose || store.ledgerWrites.Load() != wantLedger ||
				store.acceptanceWrites.Load() != 0 || store.stageWrites.Load() != wantVerify || len(updated()) != int(wantVerify) {
				t.Fatal("another restart repeated completed Pass work")
			}
		})
	}
}

func TestUserCheckRetriesFailedPassLedgerBeforeVerification(t *testing.T) {
	needsLarkLayer(t)
	ctx := context.Background()
	store := &userCheckPassStore{DB: todoTestDB(t)}
	s, f, todo := acceptedForUserCheckOn(t, store)
	updated := updatedCards(f)
	store.failLedger.Store(true)
	if result, err := s.acceptTry(ctx, todo.ID, ""); result.taken || !errors.Is(err, errAcceptStore) {
		t.Fatalf("failed ledger update reported a completed Pass: %+v, %v", result, err)
	}
	done := todoNow(s, todo.ID)
	if done.DoneAt == nil || done.Supervision.State != reviewUserCheck || done.stageVerified {
		t.Fatalf("failed ledger update skipped ahead to verified: %+v", done)
	}
	a := askOf(s, todo.ID)
	if a == nil || a.AcceptedAt.IsZero() || len(updated()) != 0 {
		t.Fatalf("durable Pass lost or prematurely redrawn: ask=%+v, cards=%d", a, len(updated()))
	}
	store.closeCalls.Store(0)
	store.acceptanceWrites.Store(0)
	store.stageWrites.Store(0)
	restarted := newServiceOn(t, f, store)
	restarted.verifySupervisedAsks(ctx)
	if got := todoNow(restarted, todo.ID); got.Supervision.State != reviewUserCheck || got.stageVerified {
		t.Fatalf("failed restart skipped the pending ledger write: %+v", got)
	}
	store.failLedger.Store(false)
	restarted.verifySupervisedAsks(ctx)
	got := todoNow(restarted, todo.ID)
	if got.Supervision.State != reviewAccepted || !got.stageVerified || !got.DoneAt.Equal(*done.DoneAt) {
		t.Fatalf("ledger recovery did not finish the original Pass: %+v", got)
	}
	if store.closeCalls.Load() != 0 || store.acceptanceWrites.Load() != 0 || store.stageWrites.Load() != 1 || len(updated()) != 1 {
		t.Fatalf("recovery repeated durable steps: closes=%d asks=%d stages=%d cards=%d",
			store.closeCalls.Load(), store.acceptanceWrites.Load(), store.stageWrites.Load(), len(updated()))
	}
}
