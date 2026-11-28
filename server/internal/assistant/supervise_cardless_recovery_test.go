package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// cardlessClaimStore fails only the two durable writes of a card-less claim; all task setup uses real Postgres.
type cardlessClaimStore struct {
	*db.DB
	mu                sync.Mutex
	failOutbox        bool
	failMarker        bool
	commitMarkerError bool
	afterOutbox       func()
}

func (s *cardlessClaimStore) PutSetting(ctx context.Context, key string, value any) error {
	cardless := false
	if key == relayKey {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		var state relayState
		if err := json.Unmarshal(raw, &state); err != nil {
			return err
		}
		s.mu.Lock()
		fail := s.failOutbox
		s.mu.Unlock()
		for _, g := range state.SupervisePending {
			if g.Kind == sigCardlessWait {
				cardless = true
				if fail {
					return errors.New("injected cardless outbox failure")
				}
			}
		}
	}
	if err := s.DB.PutSetting(ctx, key, value); err != nil {
		return err
	}
	if cardless {
		s.mu.Lock()
		after := s.afterOutbox
		s.afterOutbox = nil
		s.mu.Unlock()
		if after != nil {
			after()
		}
	}
	return nil
}

func restartCardlessClaim(t *testing.T, store Store) (*Service, *fakeHub) {
	t.Helper()
	s, f := restartedOn(t, store)
	t.Cleanup(func() { stopCardlessManager(s) })
	holdDigest(s)
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Status: proto.StatusIdle, State: "idle"})
	if err := s.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.restoreSupervisePending(context.Background())
	return s, f
}

// restartCardlessClaimWithManualDigest stops timer scheduling before the
// initial settings publish loads the to-dos and restores the durable outbox.
func restartCardlessClaimWithManualDigest(t *testing.T, store *cardlessClaimStore) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.settings = &Settings{Main: "conv1", Timezone: "Asia/Shanghai"}
	f.known["conv1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Status: proto.StatusIdle, State: "idle"})
	s := New(Options{Hub: f, Store: func() Store { return store }, Logf: testLogf(t)})
	stopSupervisionTimers(s)
	withSettings(t, f, s)
	if err := s.ensureTodosLoaded(t.Context()); err != nil {
		t.Fatal(err)
	}
	s.restoreSupervisePending(t.Context())
	return s, f
}

// cardlessRuns holds, for each manager runCardlessManager started, what cancels its run context.
var cardlessRuns sync.Map

// runCardlessManager gives s a run context that stopCardlessManager cancels, as a manager's Run has. A digest timer
// reads the run context when it fires (sendDigest(s.runCtx())). Without one of its own, a timer that fired just
// before the stop read context.Background, waited for digestMu, and digested after the stop. The store's failure
// was cleared by then, so it spent the claim the next manager had to recover (#13631). Call it before the watch
// whose failure the restart recovers.
func runCardlessManager(t *testing.T, s *Service) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s.running.Store(&ctx)
	cardlessRuns.Store(s, cancel)
	t.Cleanup(func() {
		cardlessRuns.Delete(s)
		cancel()
	})
}

// A restart ends the old manager before the failed store recovers. Its retry timer must not race the new manager
// and turn this process-crash fixture into two writers sharing one outbox.
func stopCardlessManager(s *Service) {
	if stop, ok := cardlessRuns.LoadAndDelete(s); ok {
		stop.(context.CancelFunc)() // what a timer that already fired read
	}
	stopSupervisionTimers(s)
}

func flushCardlessClaim(s *Service) {
	s.supervise.mu.Lock()
	s.supervise.last = time.Time{}
	s.supervise.mu.Unlock()
	s.sendDigest(context.Background())
	holdDigest(s)
}

// deliverCardlessDigest supplies the caller locks that sendDigest holds around deliverDigest. Keep the real
// timer enabled in the acknowledgment tests: it must serialize with this explicit delivery through the same locks.
func deliverCardlessDigest(s *Service, list []superviseSignal) bool {
	b := &s.supervise
	b.digestMu.Lock()
	defer b.digestMu.Unlock()
	b.routingMu.RLock()
	defer b.routingMu.RUnlock()
	b.dedupMu.Lock()
	defer b.dedupMu.Unlock()
	ctx := context.Background()
	return s.deliverDigest(ctx, s.settings(ctx), "conv1", list, "", time.Now(), time.Time{})
}

// queueAndDeliverCardlessDigest queues g and delivers it at once, both under digestMu. The real timer still runs.
// It cannot see g before this delivery: g's queueing arms the timer, and an immediate claim pending beside it fires the
// timer within the short gather (#13631). A signal with g's fingerprint that a digest delivered earlier still
// deduplicates g here, so a caller that needs g's first delivery keeps the digest quiet until then (#13877).
func queueAndDeliverCardlessDigest(s *Service, g superviseSignal) bool {
	b := &s.supervise
	b.digestMu.Lock()
	defer b.digestMu.Unlock()
	s.queueSignal(g)
	b.routingMu.RLock()
	defer b.routingMu.RUnlock()
	b.dedupMu.Lock()
	defer b.dedupMu.Unlock()
	ctx := context.Background()
	return s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{g}, "", time.Now(), time.Time{})
}

func cardlessNotices(f *fakeHub) int {
	count := 0
	for _, text := range f.notices() {
		if strings.Contains(text, "signal=cardless_wait") {
			count++
		}
	}
	return count
}

// Exercise the real watch, durable admission, restart and sender. The fake only cancels or fails a targeted write;
// it never fabricates a pending signal. A legacy missing PendingSince must retain its existing UI meaning.
func TestCardlessClaimRecoveredPendingDeliversOnce(t *testing.T) {
	for _, mode := range []string{"marker_failure", "cancelled_after_outbox", "legacy_marker_failure", "legacy_claim", "legacy_ambiguous_marker_write"} {
		t.Run(mode, func(t *testing.T) {
			store := &cardlessClaimStore{DB: taskRecoveryDatabase(t)}
			s, f, todo := newSupervisedServiceOn(t, store)
			t.Cleanup(func() { stopCardlessManager(s) })
			holdDigest(s)
			waitsForReview(t, s, f, todo.ID, "Ready for review.")
			legacy := strings.HasPrefix(mode, "legacy_")
			if legacy {
				s.todos.mu.Lock()
				v := s.todos.todos[todo.ID].Supervision
				v.PendingSince = nil
				v.UpdatedAt = time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
				row := v.row(todo.ID)
				s.todos.mu.Unlock()
				if err := store.DB.PutTodoSupervision(context.Background(), row); err != nil {
					t.Fatal(err)
				}
			}
			runCardlessManager(t, s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			store.mu.Lock()
			// The ambiguous mode's first marker write commits and loses its reply; every later one fails. Both are armed
			// before the watch: its claim is immediate, so the live digest timer can retry the marker as soon as the
			// watch releases writeMu, before a failure armed after the watch would apply (#16013).
			store.failMarker = strings.Contains(mode, "marker_failure") || mode == "legacy_ambiguous_marker_write"
			store.commitMarkerError = mode == "legacy_ambiguous_marker_write"
			if mode == "cancelled_after_outbox" {
				store.afterOutbox = cancel
			}
			store.mu.Unlock()
			s.superviseWatch(ctx, time.Now().Add(cardlessSettle+time.Minute))
			if mode == "legacy_ambiguous_marker_write" {
				s.superviseWatch(ctx, time.Now().Add(cardlessSettle+2*time.Minute))
			}
			if mode != "legacy_claim" && todoNow(s, todo.ID).Supervision.CardlessToldAt != nil {
				t.Fatal("the injected failure still acknowledged the marker")
			}
			if _, ok := storedSupervisePending(t, s)[todo.ID+"|"+sigCardlessWait]; !ok {
				t.Fatal("the actual watch left no durable pending claim")
			}
			stopCardlessManager(s)
			store.mu.Lock()
			store.failMarker = false
			store.mu.Unlock()
			again, nextHub := restartCardlessClaim(t, store)
			flushCardlessClaim(again)
			if got := cardlessNotices(nextHub); got != 1 {
				t.Fatalf("restored claim sent %d cardless messages, want 1; ledger=%+v, pending=%+v", got, todoNow(again, todo.ID).Supervision, storedSupervisePending(t, again))
			}
			if _, ok := storedSupervisePending(t, again)[todo.ID+"|"+sigCardlessWait]; ok {
				t.Fatal("delivered claim stayed in the durable outbox")
			}
			if legacy && todoNow(again, todo.ID).Supervision.PendingSince != nil {
				t.Fatal("claim recovery changed the legacy review's UI timestamp")
			}
			again.superviseWatch(context.Background(), time.Now().Add(48*time.Hour))
			flushCardlessClaim(again)
			if got := cardlessNotices(nextHub); got != 1 {
				t.Fatalf("a delivered round was repeated beyond the dedup window: %d", got)
			}
			stopCardlessManager(again)
			// Fingerprint expiry cannot make a completed round eligible again; the marker is the durable receipt.
			if err := store.DB.PutSetting(context.Background(), superviseDedupKey, map[string]superviseFingerprint{}); err != nil {
				t.Fatal(err)
			}
			third, thirdHub := restartCardlessClaim(t, store)
			third.superviseWatch(context.Background(), time.Now().Add(48*time.Hour))
			flushCardlessClaim(third)
			if got := cardlessNotices(thirdHub); got != 0 {
				t.Fatalf("a delivered round was repeated after another restart: %d", got)
			}
			if mode == "marker_failure" {
				if _, err := reviewErr(third, todo.ID, map[string]any{"verdict": reviewRework, "note": "Add the missing regression."}); err != nil {
					t.Fatal(err)
				}
				waitsForReview(t, third, thirdHub, todo.ID, "The regression is ready.")
				third.superviseWatch(context.Background(), time.Now().Add(cardlessSettle+time.Minute))
				flushCardlessClaim(third)
				if got := cardlessNotices(thirdHub); got != 1 {
					t.Fatalf("the new review round was not handed back once: %d", got)
				}
			}
		})
	}
}

func TestCardlessClaimRecoveryRechecksRoundUnderWriteLock(t *testing.T) {
	store := &cardlessClaimStore{DB: taskRecoveryDatabase(t)}
	s, f, todo := newSupervisedServiceOn(t, store)
	t.Cleanup(func() { stopCardlessManager(s) })
	holdDigest(s)
	waitsForReview(t, s, f, todo.ID, "Ready for review.")
	runCardlessManager(t, s)
	store.mu.Lock()
	store.failMarker = true
	store.mu.Unlock()
	s.superviseWatch(context.Background(), time.Now().Add(cardlessSettle+time.Minute))
	g := pendingCardlessSignal(t, s, todo.ID)
	stopCardlessManager(s)
	store.mu.Lock()
	store.failMarker = false
	store.mu.Unlock()
	// Start the recovery attempt below after this test holds the write lock.
	again, _ := restartCardlessClaimWithManualDigest(t, store)
	again.supervise.writeMu.Lock()
	done := make(chan [2]bool, 1)
	go func() {
		valid, retry := again.completeCardlessClaim(context.Background(), g)
		done <- [2]bool{valid, retry}
	}()
	waitForSuperviseMutex(t, "completeCardlessClaim", &again.supervise.writeMu)
	for _, state := range []string{reviewRework, reviewPending} {
		if _, _, err := again.updateSupervisionLockedAt(context.Background(), todo.ID, time.Now().Add(time.Minute), func(v *Supervision) bool {
			v.State = state
			return true
		}); err != nil {
			again.supervise.writeMu.Unlock()
			t.Fatal(err)
		}
	}
	again.supervise.writeMu.Unlock()
	if got := <-done; got != [2]bool{false, false} {
		t.Fatalf("stale claim after a new review round: valid/retry = %v", got)
	}
	if todoNow(again, todo.ID).Supervision.CardlessToldAt != nil {
		t.Fatal("the old pending claim stamped the new review round")
	}
}

func TestCardlessClaimLegacyCompletedMarker(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "marker_only"
		if pending {
			name = "old_queued_signal"
		}
		t.Run(name, func(t *testing.T) {
			store := &cardlessClaimStore{DB: taskRecoveryDatabase(t)}
			s, f, todo := newSupervisedServiceOn(t, store)
			runCardlessManager(t, s)
			t.Cleanup(func() { stopCardlessManager(s) })
			holdDigest(s)
			waitsForReview(t, s, f, todo.ID, "Ready for review.")
			// Finish the review's writes before installing the legacy row
			// and handing the outbox to the restarted manager.
			turnsQuiet(t, s, todo.ID)
			stopCardlessManager(s)
			stamp := time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
			v := todoNow(s, todo.ID).Supervision.clone()
			v.PendingSince, v.CardlessToldAt, v.UpdatedAt = nil, &stamp, stamp
			if err := store.DB.PutTodoSupervision(context.Background(), v.row(todo.ID)); err != nil {
				t.Fatal(err)
			}
			again, nextHub := restartCardlessClaimWithManualDigest(t, store)
			if pending {
				g := superviseSignal{Todo: todo.ID, Kind: sigCardlessWait, Key: "cardless@" + stamp.Add(-time.Hour).Format(time.RFC3339Nano), At: stamp}
				again.signal(g)
				if got := storedSupervisePending(t, again)[g.id()]; got.Key != g.Key {
					t.Fatalf("legacy claim did not reach the outbox: %+v", got)
				}
			}
			again.superviseWatch(context.Background(), time.Now().Add(48*time.Hour))
			flushCardlessClaim(again)
			if pending {
				waitFor(t, "the legacy claim delivery", func() bool { return cardlessNotices(nextHub) > 0 })
			}
			want := 0
			if pending {
				want = 1
			}
			if got := cardlessNotices(nextHub); got != want {
				t.Fatalf("legacy claim delivered %d times, want %d", got, want)
			}
		})
	}
}

// A digest can read the first pending claim before a later watch retries its failed marker. Keep that claim's
// identity stable, and recheck the exact outbox member after the worker read, before acknowledging it.
func TestCardlessClaimLegacyRetryDuringRecovery(t *testing.T) {
	store := &cardlessClaimStore{DB: taskRecoveryDatabase(t)}
	s, f, todo := newSupervisedServiceOn(t, store)
	t.Cleanup(func() { stopCardlessManager(s) })
	holdDigest(s)
	waitsForReview(t, s, f, todo.ID, "Ready for review.")
	stopCardlessManager(s) // this test drives watch and digest explicitly, with no timer racing the RPC gate
	s.todos.mu.Lock()
	v := s.todos.todos[todo.ID].Supervision
	v.PendingSince = nil
	v.UpdatedAt = time.Now().Add(-time.Hour).UTC().Truncate(time.Microsecond)
	row := v.row(todo.ID)
	s.todos.mu.Unlock()
	if err := store.DB.PutTodoSupervision(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	store.failMarker = true
	store.mu.Unlock()
	now := time.Now()
	s.superviseWatch(context.Background(), now)
	g := pendingCardlessSignal(t, s, todo.ID)
	watchEntered, watchResume := make(chan struct{}), make(chan struct{})
	digestEntered, digestResume := make(chan struct{}), make(chan struct{})
	var watchOnce, digestOnce sync.Once
	releaseWatch := func() { watchOnce.Do(func() { close(watchResume) }) }
	releaseDigest := func() { digestOnce.Do(func() { close(digestResume) }) }
	defer releaseWatch()
	defer releaseDigest()
	reads := 0 // accessed only with the fake hub's mutex, as nodeAnswer requires
	f.answerNode(DeviceSessionsList, func(machine string, _ json.RawMessage) (any, error) {
		if machine == "box" {
			reads++
			var entered, resume chan struct{}
			switch reads {
			case 1:
				entered, resume = watchEntered, watchResume
			case 2:
				entered, resume = digestEntered, digestResume
			}
			if entered != nil {
				f.mu.Unlock()
				close(entered)
				<-resume
				f.mu.Lock()
			}
		}
		return map[string]any{"sessions": f.sessions[machine]}, nil
	})
	// Let the second watch pass pruneAllStoppedDedup before the old digest acquires dedupMu. Their worker reads
	// then overlap without bypassing either production caller's locks or changing the pending-review fixture.
	watchDone := make(chan struct{})
	go func() {
		s.superviseWatch(context.Background(), now.Add(time.Minute))
		close(watchDone)
	}()
	select {
	case <-watchEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the second watch never reached its worker read")
	}
	done := make(chan bool, 1)
	go func() {
		done <- deliverCardlessDigest(s, []superviseSignal{g})
	}()
	select {
	case <-digestEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the old digest never reached its worker read")
	}
	releaseWatch()
	select {
	case <-watchDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the second watch did not finish after its worker read resumed")
	}
	store.mu.Lock()
	store.failMarker = false
	store.mu.Unlock()
	releaseDigest()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the old digest did not finish after the worker read resumed")
	}
	flushCardlessClaim(s)
	if got := cardlessNotices(f); got != 1 {
		t.Fatalf("a watch retry racing recovery delivered %d hand-backs, want 1; ledger=%+v pending=%+v", got, todoNow(s, todo.ID).Supervision, storedSupervisePending(t, s))
	}
}

// Each delivery exit must leave the failed marker's pending record intact: all-retry, a sibling that sends, and a
// sibling whose already-delivered fingerprint suppresses it. The next manager must recover and really send the claim.
func TestCardlessClaimRetrySurvivesDigestAcknowledgment(t *testing.T) {
	for _, mode := range []string{"all_retry", "delivered_sibling", "deduplicated_sibling"} {
		t.Run(mode, func(t *testing.T) {
			store := &cardlessClaimStore{DB: taskRecoveryDatabase(t)}
			s, f, todo := newSupervisedServiceOn(t, store)
			t.Cleanup(func() { stopCardlessManager(s) })
			if mode == "deduplicated_sibling" {
				// The sibling's first delivery below must be the first of its fingerprint, which the review that
				// waitsForReview leaves pending shares (a review's key is not part of it). The claim is immediate, so
				// the timer could deliver that review first; no digest runs until the sibling went (#13877).
				quietDigest(s)
			}
			holdDigest(s)
			waitsForReview(t, s, f, todo.ID, "Ready for review.")
			runCardlessManager(t, s)
			store.mu.Lock()
			store.failMarker = true
			store.mu.Unlock()
			s.superviseWatch(context.Background(), time.Now().Add(cardlessSettle+time.Minute))
			g := pendingCardlessSignal(t, s, todo.ID)
			list := []superviseSignal{g}
			if mode != "all_retry" {
				sibling := superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "review-sibling", At: time.Now()}
				if mode == "deduplicated_sibling" {
					if !queueAndDeliverCardlessDigest(s, sibling) {
						t.Fatal("the sibling's first real delivery failed")
					}
					rearmDigest(s) // the real timer races the delivery under test, as in the other modes
				}
				s.queueSignal(sibling)
				list = append(list, sibling)
			}
			deliverCardlessDigest(s, list)
			if _, ok := storedSupervisePending(t, s)[g.id()]; !ok {
				t.Fatal("digest acknowledged a marker failure that it should retry")
			}
			if cardlessNotices(f) != 0 || todoNow(s, todo.ID).Supervision.CardlessToldAt != nil {
				t.Fatal("a failed marker was treated as a delivered claim")
			}
			stopCardlessManager(s)
			store.mu.Lock()
			store.failMarker = false
			store.mu.Unlock()
			again, nextHub := restartCardlessClaim(t, store)
			flushCardlessClaim(again)
			if got := cardlessNotices(nextHub); got != 1 {
				t.Fatalf("retry survived but did not deliver after restart: %d", got)
			}
		})
	}
}

func (s *cardlessClaimStore) PutTodoSupervision(ctx context.Context, row db.TodoSupervisionRow) error {
	s.mu.Lock()
	fail := s.failMarker
	ambiguous := s.commitMarkerError && row.CardlessToldAt != nil
	if ambiguous {
		s.commitMarkerError = false
	}
	s.mu.Unlock()
	if ambiguous { // the one lost reply comes before a standing failMarker
		if err := s.DB.PutTodoSupervision(ctx, row); err != nil {
			return err
		}
		return errors.New("injected lost reply after marker commit")
	}
	told := row.CardlessToldAt != nil
	if !told && len(row.Waits) > 0 {
		var wait OwnerWait
		told = json.Unmarshal(row.Waits, &wait) == nil && wait.CardlessToldAt != nil
	}
	if fail && told {
		return errors.New("injected cardless marker failure")
	}
	return s.DB.PutTodoSupervision(ctx, row)
}

func TestCardlessClaimOutboxFailureRetriesAfterRestart(t *testing.T) {
	store := &cardlessClaimStore{DB: taskRecoveryDatabase(t)}
	s, f, todo := newSupervisedServiceOn(t, store)
	runCardlessManager(t, s)
	t.Cleanup(func() { stopCardlessManager(s) })
	holdDigest(s)
	waitsForReview(t, s, f, todo.ID, "Ready for review.")
	// Drive admission and recovery explicitly. An immediate cardless signal
	// can otherwise be taken from pending before either assertion.
	turnsQuiet(t, s, todo.ID)
	stopCardlessManager(s)
	store.mu.Lock()
	store.failOutbox = true
	store.mu.Unlock()
	now := time.Now().Add(cardlessSettle + time.Minute)
	s.superviseWatch(context.Background(), now)
	if !slices.Contains(pendingKinds(s, todo.ID), sigCardlessWait) {
		t.Fatal("the watch did not attempt to queue the cardless claim")
	}
	if _, ok := storedSupervisePending(t, s)[todo.ID+"|"+sigCardlessWait]; ok || todoNow(s, todo.ID).Supervision.CardlessToldAt != nil {
		t.Fatal("the failed write left a durable claim or acknowledged the review round")
	}
	store.mu.Lock()
	store.failOutbox = false
	store.mu.Unlock()
	again, _ := restartCardlessClaimWithManualDigest(t, store)
	again.superviseWatch(context.Background(), now.Add(time.Minute))
	if !slices.Contains(pendingKinds(again, todo.ID), sigCardlessWait) {
		t.Fatalf("the failed outbox write permanently spent the review round across restart: ledger=%+v pending=%+v", todoNow(again, todo.ID).Supervision, storedSupervisePending(t, again))
	}
}

func TestCardlessClaimMarkerFailureKeepsRecoveryRecord(t *testing.T) {
	store := &cardlessClaimStore{DB: taskRecoveryDatabase(t)}
	s, f, todo := newSupervisedServiceOn(t, store)
	runCardlessManager(t, s)
	t.Cleanup(func() { stopCardlessManager(s) })
	holdDigest(s)
	waitsForReview(t, s, f, todo.ID, "Ready for review.")
	turnsQuiet(t, s, todo.ID)
	stopCardlessManager(s)
	store.mu.Lock()
	store.failMarker = true
	store.mu.Unlock()
	s.superviseWatch(context.Background(), time.Now().Add(cardlessSettle+time.Minute))
	if todoNow(s, todo.ID).Supervision.CardlessToldAt != nil {
		t.Fatal("the failed marker write acknowledged the review round")
	}
	store.mu.Lock()
	store.failMarker = false
	store.mu.Unlock()
	again, _ := restartCardlessClaimWithManualDigest(t, store)
	if !slices.Contains(pendingKinds(again, todo.ID), sigCardlessWait) {
		t.Fatal("the failed marker write left no durable claim for restart to recover")
	}
	if _, ok := storedSupervisePending(t, again)[todo.ID+"|"+sigCardlessWait]; !ok || cardlessNotices(f) != 0 {
		t.Fatal("recovery requires a durable claim with no earlier hand-back")
	}
	// This fresh claim is still inside the production gather window. Let the manual digest deliver it now.
	setSuperviseTimings(again, shortSuperviseTimings)
	flushCardlessClaim(again)
	if got := cardlessNotices(again.hub.(*fakeHub)); got != 1 || todoNow(again, todo.ID).Supervision.CardlessToldAt == nil {
		t.Fatalf("restored claim produced %d notices, want one notice with a persisted marker", got)
	}
	if _, ok := storedSupervisePending(t, again)[todo.ID+"|"+sigCardlessWait]; ok {
		t.Fatal("the delivered claim remained in the durable outbox")
	}
}

func TestOwnerWaitCardlessClaimRecoversAfterMarkerFailure(t *testing.T) {
	store := &cardlessClaimStore{DB: taskRecoveryDatabase(t)}
	s, f, todo := newSupervisedServiceOn(t, store)
	t.Cleanup(func() { stopCardlessManager(s) })
	holdDigest(s)
	ctx := context.Background()
	stopOnOwner(t, s, f, todo.ID)
	runCardlessManager(t, s)
	store.mu.Lock()
	store.failMarker = true
	store.mu.Unlock()
	wait := todoNow(s, todo.ID).Supervision.Waits
	s.superviseWatch(ctx, wait.At.Add(cardlessSettle+time.Minute))
	g := pendingCardlessSignal(t, s, todo.ID)
	if w := todoNow(s, todo.ID).Supervision.Waits; w == nil || w.CardlessToldAt != nil {
		t.Fatalf("the failed marker write still acknowledged the owner wait: %+v", w)
	}
	if _, ok := storedSupervisePending(t, s)[g.id()]; !ok {
		t.Fatal("the failed marker write left no durable owner-wait claim")
	}
	stopCardlessManager(s)
	store.mu.Lock()
	store.failMarker = false
	store.mu.Unlock()
	again, nextHub := restartCardlessClaimWithManualDigest(t, store)
	if valid, retry := again.completeCardlessClaim(ctx, g); !valid || retry {
		t.Fatalf("owner-wait claim completion = valid %v retry %v", valid, retry)
	}
	if w := todoNow(again, todo.ID).Supervision.Waits; w == nil || w.CardlessToldAt == nil {
		t.Fatalf("the recovered owner wait was not stamped: %+v", w)
	}
	setSuperviseTimings(again, shortSuperviseTimings)
	flushCardlessClaim(again)
	if got := cardlessNotices(nextHub); got != 1 {
		t.Fatalf("the recovered owner-wait claim delivered %d messages, want 1", got)
	}
}
