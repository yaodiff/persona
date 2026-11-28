package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Reload both the JSONB interaction and the journal into fresh manager services.
// The test node counts only execution permits returned across the approval hook.
func TestDetachedWaitingRestartPostgres(t *testing.T) {
	for _, order := range []string{"scheduler_first", "reconnect_first"} {
		for _, decision := range []string{"allow", "deny", "applying", "legacy", "clarification", "expired"} {
			t.Run(order+"/"+decision, func(t *testing.T) {
				ctx := context.Background()
				database, err := db.Open(ctx, newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(database.Close)
				ft := newFleetTestOn(t, database)
				preparedTestConversation(ft)
				ix := detachedTestCard("ix_detached_restart")
				ix.ToolInput = json.RawMessage(`{"command":"printf approved","description":"test command"}`)
				done := startDetachedTest(t, ft, ix)
				ft.s.detachPendingApprovalsOnOwnerMessage(ix.MachineID, ix.SessionID)
				first := awaitDetachedTest(t, done)
				if first.err != nil || !first.res.Pending || first.res.Allow != nil || first.res.ApprovalID != ix.ID {
					t.Fatalf("initial hook = %+v", first)
				}
				// The first process no longer owns the deadline after this point.
				ft.s.fleet.mu.Lock()
				card := ft.s.fleet.interactions[ix.ID]
				deadline := card.ix.ExpiresAt
				card.prepared.decisionTimer.Stop()
				ft.s.fleet.mu.Unlock()
				switch decision {
				case "applying":
					if err := ft.s.approvalJournalApplying(ctx, ix); err != nil {
						t.Fatal(err)
					}
				case "clarification":
					// Asking a question pauses the live coordinator. Its outstanding
					// turn cannot be reconstructed from the stored command alone.
					if err := ft.s.clarify(ctx, ix.ID, "What does this do?", "question-before-restart", nil); err != nil {
						t.Fatal(err)
					}
				case "expired":
					// The persisted waiting window ended while the manager was down.
					expired := time.Now().Add(-time.Second).UTC()
					var journal []map[string]any
					if _, err := database.Setting(ctx, approvalJournalKey, &journal); err != nil || len(journal) != 1 {
						t.Fatalf("read waiting journal: %v, entries=%d", err, len(journal))
					}
					journal[0]["waiting_until"] = expired
					if err := database.PutSetting(ctx, approvalJournalKey, journal); err != nil {
						t.Fatal(err)
					}
					if err := database.UpdateInteractionDeadline(ctx, ix.ID, ix.CreatedAt, expired); err != nil {
						t.Fatal(err)
					}
				case "legacy":
					entry := approvalJournalEntry{ID: ix.ID, SessionID: ix.SessionID, MachineID: ix.MachineID, UpdatedAt: deadline}
					if err := database.PutSetting(ctx, approvalJournalKey, []approvalJournalEntry{entry}); err != nil {
						t.Fatal(err)
					}
				}

				// The scheduler must finalize an already-approved sibling before the
				// fleet sweep can cancel its still-pending, now-expired history row.
				var sibling proto.Interaction
				if order == "scheduler_first" && decision == "allow" {
					sibling = detachedTestCard("ix_recorded_sibling")
					sibling.CreatedAt = time.Now().Add(-time.Hour).UTC()
					sibling.ExpiresAt = time.Now().Add(-time.Minute).UTC()
					sibling.ToolInput = json.RawMessage(`{"command":"printf sibling"}`)
					if err := database.InsertInteraction(ctx, sibling); err != nil {
						t.Fatal(err)
					}
					if err := ft.s.putDetachedGrant(ctx, sibling, proto.Resolution{}); err != nil {
						t.Fatal(err)
					}
					var entries []approvalJournalEntry
					if _, err := database.Setting(ctx, approvalJournalKey, &entries); err != nil {
						t.Fatal(err)
					}
					digest, err := detachedFingerprintDigest(sibling)
					if err != nil {
						t.Fatal(err)
					}
					entries = append(entries, approvalJournalEntry{ID: sibling.ID, MachineID: sibling.MachineID,
						SessionID: sibling.SessionID, CreatedAt: sibling.CreatedAt, Fingerprint: digest, Status: "approved",
						Result: approvalResultText(sibling.ID, "approved", "Retry the exact call."), Continuation: true})
					if err := database.PutSetting(ctx, approvalJournalKey, entries); err != nil {
						t.Fatal(err)
					}
				}
				firstRestart := true
				restart := func() *Service {
					s := newRestartedService(t, Options{Hub: ft.f, Devices: ft.d, Store: func() Store { return database }, Logf: testLogf(t)})
					if decision == "deny" && firstRestart {
						// The released hook's dismissal may beat both startup passes.
						s.endNodeInteraction(nodeInteractionEnd{ID: ix.ID}, "dismissed", "", nil)
						s.endNodeInteraction(nodeInteractionEnd{ID: ix.ID, CreatedAt: ix.CreatedAt}, "cancelled", "", nil)
					}
					firstRestart = false
					if order == "scheduler_first" {
						s.recoverApprovalJournals(ctx)
						s.restoreInteractions(ctx)
					} else {
						s.restoreInteractions(ctx)
						s.recoverApprovalJournals(ctx)
					}
					return s
				}
				s := restart()
				if sibling.ID != "" {
					retry := sibling
					retry.ID = "ix_recorded_sibling_retry"
					permit, found, err := s.consumeDetachedGrant(ctx, retry)
					if err != nil || !found || !permit.UserAllowed() || permit.ApprovalID != sibling.ID {
						t.Fatalf("recorded sibling Allow after startup: %+v, %t, %v", permit, found, err)
					}
				}
				pending := s.pendingInteractions()
				rec, err := database.InteractionRecord(ctx, ix.ID)
				if err != nil || rec == nil {
					t.Fatalf("reload original interaction: %+v, %v", rec, err)
				}
				var executions int
				assertNoGrant := func(input proto.Interaction) {
					t.Helper()
					r, found, err := s.consumeDetachedGrant(ctx, input)
					if r.UserAllowed() {
						executions++
					}
					if err != nil || found || r.UserAllowed() {
						t.Fatalf("unexpected grant for %s: %+v, found=%v err=%v", input.ID, r, found, err)
					}
				}
				exact := ix
				exact.ID = "ix_identical_retry"
				assertNoGrant(exact)
				if decision == "applying" || decision == "legacy" || decision == "clarification" || decision == "expired" {
					status, detail := "failed", "uncertain"
					if decision != "applying" {
						status, detail = "cancelled", "cancelled"
					}
					result := waitFakeApprovalResult(t, ft.f, ix.ID)
					if len(pending) != 0 || rec.Status != status || !strings.Contains(result, detail) {
						t.Fatalf("recovery left %d pending, status %q, result %q", len(pending), rec.Status, result)
					}
					return
				}
				if len(pending) != 1 || !sameApprovalOccurrence(pending[0], ix) || !pending[0].CreatedAt.Equal(ix.CreatedAt) ||
					!pending[0].ExpiresAt.Equal(deadline) || rec.Status != "pending" || rec.Resolution != nil {
					t.Fatalf("reloaded cards = %+v, durable outcome = %s %+v", pending, rec.Status, rec.Resolution)
				}
				replay, err := s.nodeInteraction(ctx, ix)
				if replay.UserAllowed() {
					executions++
				}
				if err != nil || !replay.Pending || replay.ApprovalID != first.res.ApprovalID || executions != 0 {
					t.Fatalf("restart returned %+v, %v; executions=%d", replay, err, executions)
				}
				allow := decision == "allow"
				answer := proto.Resolution{Allow: &allow}
				answers := make(chan error, 2)
				for range 2 {
					go func() { answers <- s.resolve(ix.ID, answer) }()
				}
				firstClick, secondClick := <-answers, <-answers
				if !((firstClick == nil && errors.Is(secondClick, errNotPending)) ||
					(secondClick == nil && errors.Is(firstClick, errNotPending))) {
					t.Fatalf("duplicate answers: %v, %v; want exactly one winner", firstClick, secondClick)
				}
				result := waitFakeApprovalResult(t, ft.f, ix.ID)
				word := "denied"
				if allow {
					word = "approved"
				}
				if !strings.Contains(result, word) {
					t.Fatalf("owner result = %q", result)
				}
				waitFor(t, "completion of approval delivery", func() bool {
					s.approvals.mu.Lock()
					defer s.approvals.mu.Unlock()
					return !s.approvals.delivering
				})
				s = restart()
				changed := exact
				changed.ToolInput = json.RawMessage(`{"command":"printf changed","description":"test command"}`)
				assertNoGrant(changed)
				if allow {
					r, err := s.nodeInteraction(ctx, exact)
					if err != nil || !r.UserAllowed() || r.ApprovalID != ix.ID {
						t.Fatalf("identical node retry = %+v, %v", r, err)
					}
					executions++
					s = restart()
				}
				assertNoGrant(exact)
				if (allow && executions != 1) || (!allow && executions != 0) {
					t.Fatalf("%s: executions=%d", decision, executions)
				}
			})
		}
	}
}

func TestUnjournaledNodeDismissalWithUnreadableHistory(t *testing.T) {
	for _, target := range []string{"original", "replacement"} {
		t.Run(target, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ft := newFleetTest(t)
				ix := detachedTestCard("ix_deferred_dismissal")
				ix.CreatedAt = time.Now().Add(-time.Hour)
				ft.s.record(ix)
				st := &restoreReadFailStore{restoreMem: &restoreMem{fleetMem: ft.st}, id: ix.ID}
				restarted := newRestartedService(t, Options{Hub: ft.f, Devices: ft.d, Store: func() Store { return st }, Logf: testLogf(t)})
				st.fail.Store(true)
				restarted.endNodeInteraction(nodeInteractionEnd{ID: ix.ID}, "dismissed", "", nil)
				elapse(time.Minute)
				want := "dismissed"
				if target == "replacement" {
					// A later occurrence from a device whose clock lags the manager.
					ix.CreatedAt = ix.CreatedAt.Add(time.Second)
					ft.s.record(ix)
					want = "pending"
				}
				st.fail.Store(false)
				elapse(time.Minute)
				got, _, err := st.InteractionOutcome(context.Background(), ix.ID)
				if err != nil || got != want {
					t.Fatalf("deferred ID-only dismissal: status=%s want=%s err=%v", got, want, err)
				}
			})
		})
	}
}

func TestDetachedLegacyReplayUsesStoredOccurrence(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_legacy_original")
	ft.s.record(ix)
	cutoff := ix.CreatedAt.Add(time.Second)
	putApprovalJournal(t, ft, approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID, UpdatedAt: cutoff})
	st := &restoreMem{fleetMem: ft.st}
	s := newRestartedService(t, Options{Hub: ft.f, Devices: ft.d, Store: func() Store { return st }, Logf: testLogf(t)})
	replay := ix
	replay.CreatedAt = cutoff.Add(time.Second)
	got, handled, err := s.journaledDetachedReplay(context.Background(), replay)
	if err != nil || !handled || !got.Dismiss || got.UserAllowed() {
		t.Fatalf("legacy original before Always: handled=%v resolution=%+v err=%v", handled, got, err)
	}
}

func TestDetachedRecoveryRetryUsesLatestBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ix := detachedTestCard("ix_failed_terminal_write")
		putTerminalOccurrenceJournal(t, ft, ix, "denied", "Nothing was executed.")
		ft.s.record(ix)
		st := &recoveryFailStore{restoreMem: &restoreMem{fleetMem: ft.st}, failID: ix.ID}
		s := newRestartedService(t, Options{Hub: ft.f, Devices: ft.d, Store: func() Store { return st }, Logf: testLogf(t)})
		s.recoverApprovalJournals(context.Background())
		s.recoverApprovalJournals(context.Background())
		st.failID = ""
		elapse(time.Minute)
		if result := waitFakeApprovalResult(t, ft.f, ix.ID); !strings.Contains(result, "denied") {
			t.Fatalf("latest recovery did not deliver the recorded denial: %q", result)
		}
	})
}

func TestContendedLegacyNodeEndPinsLiveCardWithoutTimestamp(t *testing.T) {
	for _, target := range []string{"original", "replacement"} {
		t.Run(target, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ft := newFleetTest(t)
				ix := permission("ix_legacy_no_created_at")
				original := &pending{ix: ix, done: make(chan proto.Resolution, 1)}
				ft.s.record(ix)
				release := ft.s.lockInteractionAdmission(ix.ID)
				ft.s.fleet.mu.Lock()
				ft.s.ixMapLocked()[ix.ID] = original
				ft.s.fleet.mu.Unlock()
				ft.s.endNodeInteraction(nodeInteractionEnd{ID: ix.ID}, "dismissed", "", nil)
				want := 0
				if target == "replacement" {
					ft.s.fleet.mu.Lock()
					ft.s.fleet.interactions[ix.ID] = &pending{ix: ix, done: make(chan proto.Resolution, 1)}
					ft.s.fleet.mu.Unlock()
					want = 1
				}
				release()
				synctest.Wait()
				if got := len(ft.s.pendingInteractions()); got != want {
					t.Fatalf("pending = %d, want %d", got, want)
				}
			})
		})
	}
}

func TestLegacyStoredNodeEndNeverRetriesAgainstANewCard(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTest(t)
		ix := detachedTestCard("ix_legacy_stored_end")
		ix.CreatedAt = time.Time{}
		ft.s.record(ix)
		st := &journalReadFailStore{restoreMem: &restoreMem{fleetMem: ft.st}}
		st.fail.Store(true)
		s := newRestartedService(t, Options{Hub: ft.f, Devices: ft.d, Store: func() Store { return st }, Logf: testLogf(t)})
		s.endNodeInteraction(nodeInteractionEnd{ID: ix.ID}, "dismissed", "", nil)
		ix.CreatedAt = time.Now()
		ft.s.record(ix)
		st.fail.Store(false)
		elapse(time.Minute)
		status, _, err := st.InteractionOutcome(context.Background(), ix.ID)
		if err != nil || status != "pending" {
			t.Fatalf("a deferred ID-only end withdrew the replacement: status=%s err=%v", status, err)
		}
	})
}

func TestLegacyDismissBeforeApprovalRecoveryPostgres(t *testing.T) {
	for _, status := range []string{"applying", "approved"} {
		t.Run(status, func(t *testing.T) {
			ctx := t.Context()
			database, err := db.Open(ctx, newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(database.Close)
			ft := newFleetTestOn(t, database)
			preparedTestConversation(ft)
			ix := detachedTestCard("ix_legacy_dismiss")
			if err := database.InsertInteraction(ctx, ix); err != nil {
				t.Fatal(err)
			}
			entry := approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID,
				Status: status, UpdatedAt: time.Now().UTC()}
			if err := ft.s.putDetachedGrant(ctx, ix, proto.Resolution{}); err != nil {
				t.Fatal(err)
			}
			if status == "approved" {
				entry.UpdatedAt = time.Now().UTC()
				entry.Result = approvalResultText(ix.ID, status, "Retry the exact call.")
				entry.Continuation = true
			}
			if err := database.PutSetting(ctx, approvalJournalKey, []approvalJournalEntry{entry}); err != nil {
				t.Fatal(err)
			}
			s := newRestartedService(t, Options{Hub: ft.f, Devices: ft.d, Store: func() Store { return database }, Logf: testLogf(t)})
			s.endNodeInteraction(nodeInteractionEnd{ID: ix.ID}, "dismissed", "", &proto.Resolution{Dismiss: true, AnsweredBy: proto.AnsweredByTerminal})
			s.recoverApprovalJournals(ctx)
			rec, err := database.InteractionRecord(ctx, ix.ID)
			if err != nil || rec == nil {
				t.Fatalf("reload history: %+v, %v", rec, err)
			}
			wantHistory, wantResult := "failed", "uncertain"
			if status == "approved" {
				wantHistory, wantResult = "resolved", "approved"
			}
			if rec.Status != wantHistory {
				t.Errorf("history = %q, want %q", rec.Status, wantHistory)
			}
			if result := waitFakeApprovalResult(t, ft.f, ix.ID); !strings.Contains(result, wantResult) {
				t.Errorf("delivered %q, want %q", result, wantResult)
			}
			retry := ix
			retry.ID = "ix_retry"
			for attempt := range 2 {
				permit, found, err := s.consumeDetachedGrant(ctx, retry)
				want := status == "approved" && attempt == 0
				if err != nil || found != want || permit.UserAllowed() != want {
					t.Errorf("retry %d: permit=%+v found=%v err=%v, want allow=%v", attempt+1, permit, found, err, want)
				}
			}
		})
	}
}

// Intercept a history read between the journal snapshot and its reconciliation.
type waitingRecoveryStore struct {
	*expireReadFailStore
	beforeRead func(context.Context, string) error
}

func (st *waitingRecoveryStore) InteractionRecord(ctx context.Context, id string) (*db.InteractionRecord, error) {
	if err := st.beforeRead(ctx, id); err != nil {
		return nil, err
	}
	return st.restoreMem.InteractionRecord(ctx, id)
}

func newWaitingRecoveryTest(t *testing.T, cards ...proto.Interaction) (*Service, *waitingRecoveryStore) {
	t.Helper()
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	var entries []approvalJournalEntry
	for _, ix := range cards {
		ft.s.record(ix)
		digest, err := detachedFingerprintDigest(ix)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID,
			CreatedAt: ix.CreatedAt, Fingerprint: digest, WaitingUntil: ix.ExpiresAt, Summary: ix.ToolName})
	}
	if err := ft.st.PutSetting(t.Context(), approvalJournalKey, entries); err != nil {
		t.Fatal(err)
	}
	st := &waitingRecoveryStore{expireReadFailStore: &expireReadFailStore{restoreMem: &restoreMem{fleetMem: ft.st}}}
	s := newRestartedService(t, Options{Hub: ft.f, Devices: ft.d, Store: func() Store { return st }, Logf: testLogf(t)})
	return s, st
}

func assertRecoveryDeliveryOpen(t *testing.T, s *Service) {
	t.Helper()
	s.approvals.mu.Lock()
	defer s.approvals.mu.Unlock()
	if s.approvals.recovering || !s.approvals.recoveryDone {
		t.Fatalf("recovery blocked delivery: recovering=%v done=%v", s.approvals.recovering, s.approvals.recoveryDone)
	}
}

func TestSupersededDetachedRecoveryDiscardsHistoryReads(t *testing.T) {
	for _, read := range []string{"error", "success"} {
		t.Run(read, func(t *testing.T) {
			ctx := t.Context()
			a, b := detachedTestCard("ix_first"), detachedTestCard("ix_second")
			s, st := newWaitingRecoveryTest(t, a, b)
			// Pass one reads A before its waiting deadline becomes durable.
			if err := st.UpdateInteractionDeadline(ctx, a.ID, a.CreatedAt, a.CreatedAt); err != nil {
				t.Fatal(err)
			}
			entered, resume := make(chan struct{}), make(chan struct{})
			var blocked atomic.Bool
			st.beforeRead = func(ctx context.Context, id string) error {
				if id == b.ID && blocked.CompareAndSwap(false, true) {
					close(entered)
					select {
					case <-resume:
					case <-ctx.Done():
						return ctx.Err()
					}
					if read == "error" {
						return errors.New("stale read failed")
					}
				}
				return nil
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				s.recoverApprovalJournals(ctx)
			}()
			select {
			case <-entered:
			case <-done:
				t.Fatal("first recovery finished before reading the second card")
			}
			err := st.UpdateInteractionDeadline(ctx, a.ID, a.CreatedAt, a.ExpiresAt)
			s.recoverApprovalJournals(ctx)
			close(resume)
			<-done
			if err != nil {
				t.Fatal(err)
			}
			assertRecoveryDeliveryOpen(t, s)
			var journal []approvalJournalEntry
			if _, err := st.Setting(ctx, approvalJournalKey, &journal); err != nil {
				t.Fatal(err)
			}
			if len(journal) != 2 || !journal[0].unansweredDetached() || !journal[1].unansweredDetached() {
				t.Fatalf("older pass rewrote the newer waiting journal: %+v", journal)
			}
		})
	}
}

func TestCancelledDetachedReplayKeepsRecoveryDeliveryOpen(t *testing.T) {
	for _, phase := range []string{"during_recovery", "after_recovery"} {
		t.Run(phase, func(t *testing.T) {
			ix := detachedTestCard("ix_cancelled_hook")
			s, st := newWaitingRecoveryTest(t, ix)
			hookCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var reads atomic.Int32
			st.beforeRead = func(ctx context.Context, _ string) error {
				if phase == "during_recovery" && reads.Add(1) == 2 {
					cancel() // The reconnect started journal recovery and then disconnected.
				}
				return ctx.Err()
			}
			if phase == "after_recovery" {
				// The batch is complete, but fleet still owes a snapshot retry.
				st.fail.Store(true)
				s.recoverApprovalJournals(t.Context())
				assertRecoveryDeliveryOpen(t, s)
				cancel()
			}
			r, handled, err := s.journaledDetachedReplay(hookCtx, ix)
			if hookCtx.Err() == nil || err != nil || !handled || !r.Pending || r.UserAllowed() {
				t.Fatalf("cancelled replay: handled=%v res=%+v err=%v ctx=%v", handled, r, err, hookCtx.Err())
			}
			assertRecoveryDeliveryOpen(t, s)
			if phase == "during_recovery" && len(s.pendingInteractions()) != 1 {
				t.Fatal("disconnected hook left the waiting card unrestored")
			}
		})
	}
}

func TestDetachedSnapshotRetryDeliversSiblingResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, b := detachedTestCard("ix_unreadable"), detachedTestCard("ix_answerable")
		s, st := newWaitingRecoveryTest(t, a, b)
		var reads atomic.Int32
		st.beforeRead = func(ctx context.Context, id string) error {
			if id == a.ID && reads.Add(1) > 1 {
				return errors.New("snapshot row remains unavailable")
			}
			return nil
		}
		s.recoverApprovalJournals(t.Context())
		assertRecoveryDeliveryOpen(t, s)
		elapse(time.Minute)
		allow := false
		if err := s.resolve(b.ID, proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		assertRecoveryDeliveryOpen(t, s)
		if result := waitFakeApprovalResult(t, s.hub.(*fakeHub), b.ID); !strings.Contains(result, "denied") {
			t.Fatalf("sibling result = %q", result)
		}
	})
}

func TestExpiredSchedulerPassCannotSupersedeDetachedReplay(t *testing.T) {
	ix := detachedTestCard("ix_recover_before_scheduler")
	s, st := newWaitingRecoveryTest(t, ix)
	expired, cancel := context.WithCancel(t.Context())
	cancel()
	var reads atomic.Int32
	st.beforeRead = func(ctx context.Context, _ string) error {
		if reads.Add(1) == 2 {
			// The hook's recovery owns this read when the scheduler arrives.
			s.recoverApprovalJournals(expired)
		}
		return ctx.Err()
	}
	r, handled, err := s.journaledDetachedReplay(t.Context(), ix)
	if err != nil || !handled || !r.Pending || r.UserAllowed() {
		t.Fatalf("replay = %+v, handled=%v err=%v", r, handled, err)
	}
	assertRecoveryDeliveryOpen(t, s)
	if len(s.pendingInteractions()) != 1 {
		t.Fatal("the healthy recovery did not restore the waiting card")
	}
}

func TestOrdinaryJournalDoesNotBlockIDOnlyDismissal(t *testing.T) {
	for _, format := range []string{"dated_permission", "undated_question", "finalized_legacy"} {
		t.Run(format, func(t *testing.T) {
			ft := newFleetTest(t)
			ix := permission("ix_reused_ordinary")
			ft.s.record(ix)
			old := approvalJournalEntry{ID: ix.ID, Kind: proto.IxPermission, Status: "denied", Result: "Denied.",
				CreatedAt: ix.CreatedAt.Add(-time.Hour), UpdatedAt: ix.CreatedAt.Add(-time.Minute)}
			if format == "undated_question" {
				old.Kind, old.CreatedAt = proto.IxQuestion, time.Time{}
			} else if format == "finalized_legacy" {
				old.CreatedAt, old.Finalized = time.Time{}, true
			}
			putApprovalJournal(t, ft, old)
			st := &restoreReadFailStore{restoreMem: &restoreMem{fleetMem: ft.st}, id: ix.ID}
			st.fail.Store(true)
			s := newRestartedService(t, Options{Hub: ft.f, Devices: ft.d, Store: func() Store { return st }, Logf: testLogf(t)})
			s.endNodeInteraction(nodeInteractionEnd{ID: ix.ID}, "dismissed", "", nil)
			status, _, err := st.InteractionOutcome(t.Context(), ix.ID)
			if err != nil || status != "dismissed" {
				t.Fatalf("ordinary card after ID-only dismissal: status=%q err=%v", status, err)
			}
		})
	}
}

func TestPostStartupRecoveryLeavesApprovedHistoryToDelivery(t *testing.T) {
	s, st := newWaitingRecoveryTest(t)
	s.recoverApprovalJournals(t.Context())
	ix := detachedTestCard("ix_unfinalized_approved")
	s.record(ix)
	if err := s.putDetachedGrant(t.Context(), ix, proto.Resolution{}); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var reads atomic.Int32
	st.beforeRead = func(ctx context.Context, id string) error {
		if id == ix.ID && reads.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	}
	// The owner Allow's history write failed. Delivery has claimed that write
	// when the timer requests another pass for missing cards.
	s.approvalJournalFinished(t.Context(), ix, "approved", "Retry the exact call.", false)
	<-started
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.recoverApprovalJournals(t.Context())
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("post-startup recovery waited on the active delivery finalizer")
	}
	close(release)
	<-done
	if result := waitFakeApprovalResult(t, s.hub.(*fakeHub), ix.ID); !strings.Contains(result, "approved") {
		t.Errorf("delivered result = %q", result)
	}
	if n := reads.Load(); n != 1 {
		t.Errorf("history reads for the unfinalized approval = %d, want one", n)
	}
	retry := ix
	retry.ID = "ix_exact_retry"
	permit, found, err := s.consumeDetachedGrant(t.Context(), retry)
	if err != nil || !found || !permit.UserAllowed() {
		t.Fatalf("approved grant after concurrent finalization: %+v, found=%v, %v", permit, found, err)
	}
}
