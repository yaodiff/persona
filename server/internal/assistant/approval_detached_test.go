package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

type detachedTestResult struct {
	res proto.Resolution
	err error
}

type relayWriteFailStore struct {
	*fleetMem
	fail atomic.Bool
}

func (s *relayWriteFailStore) PutSetting(ctx context.Context, key string, value any) error {
	if key == relayKey && s.fail.Load() {
		return errors.New("synthetic relay queue write failure")
	}
	return s.fleetMem.PutSetting(ctx, key, value)
}

type grantWriteFailStore struct {
	*fleetMem
	fail atomic.Bool
}

func (s *grantWriteFailStore) PutSetting(ctx context.Context, key string, value any) error {
	if key == detachedGrantSetting && s.fail.Load() {
		return errors.New("synthetic grant revocation write failure")
	}
	return s.fleetMem.PutSetting(ctx, key, value)
}

type recoveryFailStore struct {
	*restoreMem
	failID string
}

func (s *recoveryFailStore) FinishInteraction(ctx context.Context, id, status string, r *proto.Resolution) error {
	if id == s.failID {
		return errors.New("synthetic recovered interaction write failure")
	}
	return s.restoreMem.FinishInteraction(ctx, id, status, r)
}

func (s *recoveryFailStore) FinishInteractionOccurrence(ctx context.Context, ix proto.Interaction, status string, r *proto.Resolution) (bool, bool, error) {
	if ix.ID == s.failID {
		return false, false, errors.New("synthetic recovered interaction write failure")
	}
	return s.restoreMem.FinishInteractionOccurrence(ctx, ix, status, r)
}

type journalReadFailStore struct {
	*restoreMem
	fail atomic.Bool
}

func (s *journalReadFailStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == approvalJournalKey && s.fail.Load() {
		return false, errors.New("synthetic approval journal read failure")
	}
	return s.restoreMem.Setting(ctx, key, out)
}

type restoreReadFailStore struct {
	*restoreMem
	id   string
	fail atomic.Bool
}

func (s *restoreReadFailStore) InteractionRecord(ctx context.Context, id string) (*db.InteractionRecord, error) {
	if id == s.id && s.fail.Load() {
		return nil, errors.New("synthetic restored interaction read failure")
	}
	return s.restoreMem.InteractionRecord(ctx, id)
}

type expireReadFailStore struct {
	*restoreMem
	fail atomic.Bool
}

func (s *expireReadFailStore) ExpireStaleInteractions(ctx context.Context) ([]proto.Interaction, []proto.Interaction, error) {
	if s.fail.Load() {
		return nil, nil, errors.New("synthetic startup expiration read failure")
	}
	return s.restoreMem.ExpireStaleInteractions(ctx)
}

type restoreInterleaveStore struct {
	*restoreMem
	snapshot     chan struct{}
	release      chan struct{}
	snapshotOnce sync.Once
	releaseOnce  sync.Once
}

func (s *restoreInterleaveStore) ExpireStaleInteractions(ctx context.Context) ([]proto.Interaction, []proto.Interaction, error) {
	resumable, cancelled, err := s.restoreMem.ExpireStaleInteractions(ctx)
	if err != nil {
		return nil, nil, err
	}
	s.snapshotOnce.Do(func() { close(s.snapshot) })
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	return resumable, cancelled, nil
}

func putTerminalOccurrenceJournal(t *testing.T, ft *fleetTest, ix proto.Interaction, status, detail string) approvalJournalEntry {
	t.Helper()
	createdAt, fingerprint, err := approvalJournalOccurrence(ix)
	if err != nil {
		t.Fatal(err)
	}
	entry := approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID,
		CreatedAt: createdAt, Fingerprint: fingerprint, Status: status,
		Result: approvalResultText(ix.ID, status, detail), Continuation: ix.Detachable && status == "approved",
		UpdatedAt: time.Now().UTC()}
	if err := ft.st.PutSetting(context.Background(), approvalJournalKey, []approvalJournalEntry{entry}); err != nil {
		t.Fatal(err)
	}
	return entry
}

// Detached transport fixtures exercise owner-only approval; internal command
// routing has separate source and supervisor lifecycle regressions.
func detachedTestCard(id string) proto.Interaction {
	return proto.Interaction{ID: id, MachineID: "mac", SessionID: "s1", Cwd: "/home/u/.persona/assistant",
		ExecutionCwd: "/home/u/.persona/assistant", Kind: proto.IxPermission, OutwardWrite: true,
		ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"printf approved"}`), Detachable: true, Resumable: true,
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
}

func startDetachedTest(t *testing.T, ft *fleetTest, ix proto.Interaction) <-chan detachedTestResult {
	t.Helper()
	done := make(chan detachedTestResult, 1)
	go func() {
		res, err := ft.s.nodeInteraction(context.Background(), ix)
		done <- detachedTestResult{res: res, err: err}
	}()
	ft.waitPending(1)
	return done
}

func awaitDetachedTest(t *testing.T, done <-chan detachedTestResult) detachedTestResult {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("the detached command call did not return")
		return detachedTestResult{}
	}
}

func shortDetachedTimers(t *testing.T) {
	t.Helper()
	oldInitial, oldWaiting := approvalInitialWait, approvalWaitingWindow
	approvalInitialWait, approvalWaitingWindow = 20*time.Millisecond, 80*time.Millisecond
	t.Cleanup(func() { approvalInitialWait, approvalWaitingWindow = oldInitial, oldWaiting })
}

// The original decision deadline can be fifteen minutes away. The turn is
// nevertheless released at the short initial wait, and a late Allow applies once.
func TestPreparedApprovalReleasesBeforeItsDecisionDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTestNoStore(t)
		preparedTestConversation(ft)
		var applies atomic.Int32
		done := startShortPrepared(t, ft, "ix_short_initial", time.Hour, func(context.Context, proto.Resolution) (string, error) {
			applies.Add(1)
			return "applied after the owner answered", nil
		})
		elapse(30 * time.Millisecond)
		first := awaitPreparedTest(t, done)
		if first.err != nil || !first.out.Clarifying || first.out.Text != approvalops.WaitingText {
			t.Fatalf("initial result = %+v, %v; want the non-final waiting result", first.out, first.err)
		}
		if !strings.Contains(first.out.Text, "End this turn now") {
			t.Fatalf("waiting result does not release the turn explicitly: %q", first.out.Text)
		}
		if got := len(ft.s.pendingInteractions()); got != 1 {
			t.Fatalf("pending cards = %d, want the retained approval", got)
		}
		allow := true
		if err := ft.s.resolve("ix_short_initial", proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		if applies.Load() != 1 {
			t.Fatalf("Apply ran %d times, want once", applies.Load())
		}
		if result := waitApprovalResult(t, ft, "ix_short_initial"); !strings.Contains(result, "applied after the owner answered") {
			t.Fatalf("terminal result = %q", result)
		}
	})
}

// A command approved after its initial hook returned is allowed once for the
// identical retry. A second identical call and a changed input both need their
// own decision, and the original conversation gets the continuation message.
func TestDetachedCommandApprovalContinuesExactlyOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		done := startDetachedTest(t, ft, detachedTestCard("ix_detached_once"))
		elapse(30 * time.Millisecond)
		first := awaitDetachedTest(t, done)
		if first.err != nil || !first.res.Pending || !first.res.Dismiss || first.res.Allow != nil ||
			first.res.ApprovalID != "ix_detached_once" || !strings.Contains(first.res.Message, "The command was not executed") {
			t.Fatalf("initial hook result = %+v, %v", first.res, first.err)
		}
		allow := true
		if err := ft.s.resolve("ix_detached_once", proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		result := waitApprovalResult(t, ft, "ix_detached_once")
		for _, want := range []string{"ix_detached_once", "approved", "Nothing has run yet", `{"command":"printf approved"}`} {
			if !strings.Contains(result, want) {
				t.Errorf("continuation message = %q, want %q", result, want)
			}
		}

		// Replaying the original resumable request is not the requested retry and
		// must leave the unused grant available for a new tool invocation.
		replayed, err := ft.s.nodeInteraction(context.Background(), detachedTestCard("ix_detached_once"))
		if err != nil || (replayed.Allow != nil && *replayed.Allow) || !replayed.Dismiss {
			t.Fatalf("replaying the original resumable card consumed its own grant: %+v, %v", replayed, err)
		}

		retry := detachedTestCard("ix_retry_once")
		got, err := ft.s.nodeInteraction(context.Background(), retry)
		if err != nil || got.Allow == nil || !*got.Allow || got.ApprovalID != "ix_detached_once" {
			t.Fatalf("identical retry = %+v, %v; want the original one-time grant", got, err)
		}

		second := startDetachedTest(t, ft, detachedTestCard("ix_retry_second"))
		deny := false
		if err := ft.s.resolve("ix_retry_second", proto.Resolution{Allow: &deny}); err != nil {
			t.Fatal(err)
		}
		if got := awaitDetachedTest(t, second); (got.res.Allow != nil && *got.res.Allow) || got.res.Pending {
			t.Fatalf("a second identical call reused the consumed grant: %+v", got.res)
		}
	})
}

func TestDetachedCommandGrantRejectsChangedInputAndConcurrentReplay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		done := startDetachedTest(t, ft, detachedTestCard("ix_detached_replay"))
		elapse(30 * time.Millisecond)
		awaitDetachedTest(t, done)
		allow := true
		if err := ft.s.resolve("ix_detached_replay", proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		waitApprovalResult(t, ft, "ix_detached_replay")

		changed := detachedTestCard("ix_changed_input")
		changed.ToolInput = json.RawMessage(`{"command":"printf changed"}`)
		changedDone := startDetachedTest(t, ft, changed)
		deny := false
		if err := ft.s.resolve("ix_changed_input", proto.Resolution{Allow: &deny}); err != nil {
			t.Fatal(err)
		}
		awaitDetachedTest(t, changedDone)

		first := make(chan detachedTestResult, 1)
		second := make(chan detachedTestResult, 1)
		go func() {
			res, err := ft.s.nodeInteraction(context.Background(), detachedTestCard("ix_race_a"))
			first <- detachedTestResult{res, err}
		}()
		go func() {
			res, err := ft.s.nodeInteraction(context.Background(), detachedTestCard("ix_race_b"))
			second <- detachedTestResult{res, err}
		}()
		ft.waitPending(1) // the loser becomes a fresh card; the winner consumed the only grant
		if err := ft.s.resolve("ix_race_b", proto.Resolution{Allow: &deny}); err != nil {
			// The calls have distinct ids; the pending one is whichever remains.
			if err := ft.s.resolve("ix_race_a", proto.Resolution{Allow: &deny}); err != nil {
				t.Fatal(err)
			}
		}
		allowed := 0
		for _, ch := range []<-chan detachedTestResult{first, second} {
			got := awaitDetachedTest(t, ch)
			if got.err == nil && got.res.Allow != nil && *got.res.Allow {
				allowed++
			}
		}
		if allowed != 1 {
			t.Fatalf("concurrent retries allowed %d calls, want exactly one", allowed)
		}
	})
}

func TestDetachedGrantConsumptionFailureDoesNotAllow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		done := startDetachedTest(t, ft, detachedTestCard("ix_detached_durable"))
		elapse(30 * time.Millisecond)
		awaitDetachedTest(t, done)
		allow := true
		if err := ft.s.resolve("ix_detached_durable", proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		waitApprovalResult(t, ft, "ix_detached_durable")
		ft.st.setFailPut(errors.New("synthetic write failure"))
		if got, err := ft.s.nodeInteraction(context.Background(), detachedTestCard("ix_failed_consume")); err == nil || got.Allow != nil {
			t.Fatalf("failed consumption = %+v, %v; want no execution", got, err)
		}
		ft.st.setFailPut(nil)
		got, err := ft.s.nodeInteraction(context.Background(), detachedTestCard("ix_successful_consume"))
		if err != nil || got.Allow == nil || !*got.Allow {
			t.Fatalf("retry after the durable write recovered = %+v, %v", got, err)
		}

		// A fresh manager on the same store cannot consume that grant again. This
		// also proves the successful grant was persisted, not only held in memory.
		restarted := New(Options{Hub: ft.f, Store: func() Store { return ft.st }, Logf: testLogf(t), Devices: ft.d})
		pending := make(chan detachedTestResult, 1)
		go func() {
			res, err := restarted.nodeInteraction(context.Background(), detachedTestCard("ix_after_restart"))
			pending <- detachedTestResult{res, err}
		}()
		waitFor(t, "the restarted manager's fresh pending card", func() bool {
			restarted.fleet.mu.Lock()
			defer restarted.fleet.mu.Unlock()
			return len(restarted.fleet.interactions) == 1
		})
		restarted.fleet.mu.Lock()
		p := restarted.fleet.interactions["ix_after_restart"]
		restarted.fleet.mu.Unlock()
		restarted.endPrepared(p, "cancelled", "test cleanup")
		awaitDetachedTest(t, pending)
	})
}

func TestDetachedApprovalCarriesEditedTextAndTerminalOutcomes(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ix := detachedTestCard("ix_detached_edit")
		ix.EditableText = "original body"
		done := startDetachedTest(t, ft, ix)
		elapse(30 * time.Millisecond)
		awaitDetachedTest(t, done)
		allow := true
		if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &allow, EditedText: "edited body"}); err != nil {
			t.Fatal(err)
		}
		waitApprovalResult(t, ft, ix.ID)
		retry := ix
		retry.ID = "ix_detached_edit_retry"
		got, err := ft.s.nodeInteraction(context.Background(), retry)
		if err != nil || got.Allow == nil || !*got.Allow || got.EditedText != "edited body" || got.ApprovalID != ix.ID {
			t.Fatalf("edited continuation = %+v, %v", got, err)
		}

		expiring := startDetachedTest(t, ft, detachedTestCard("ix_detached_expiry"))
		elapse(30 * time.Millisecond)
		awaitDetachedTest(t, expiring)
		elapse(100 * time.Millisecond)
		ft.waitPending(0)
		if result := waitApprovalResult(t, ft, "ix_detached_expiry"); !strings.Contains(result, "expired") || !strings.Contains(result, "nothing was executed") {
			t.Fatalf("expiry result = %q", result)
		}
		fresh := startDetachedTest(t, ft, detachedTestCard("ix_after_expiry"))
		elapse(30 * time.Millisecond)
		awaitDetachedTest(t, fresh) // denial after the caller returned is journalled too
		deny := false
		if err := ft.s.resolve("ix_after_expiry", proto.Resolution{Allow: &deny}); err != nil {
			t.Fatal(err)
		}
		if result := waitApprovalResult(t, ft, "ix_after_expiry"); !strings.Contains(result, "denied") || strings.Contains(result, "Owner note:") {
			t.Fatalf("denial result = %q", result)
		}

		// Detached denial with an owner note preserves that note in the delivered journal outcome.
		withNote := startDetachedTest(t, ft, detachedTestCard("ix_detached_deny_note"))
		elapse(30 * time.Millisecond)
		awaitDetachedTest(t, withNote)
		if err := ft.s.resolve("ix_detached_deny_note", proto.Resolution{Allow: &deny, Message: "  Change tone to brief  "}); err != nil {
			t.Fatal(err)
		}
		if result := waitApprovalResult(t, ft, "ix_detached_deny_note"); !strings.Contains(result, "denied") || !strings.Contains(result, "Owner note: Change tone to brief") {
			t.Fatalf("denial-with-note result = %q, want owner note preserved", result)
		}
	})
}

func TestImmediateDetachedAllowLeavesNoSecondGrant(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	done := startDetachedTest(t, ft, detachedTestCard("ix_immediate"))
	allow := true
	if err := ft.s.resolve("ix_immediate", proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	if got := awaitDetachedTest(t, done); got.res.Allow == nil || !*got.res.Allow || got.res.Pending {
		t.Fatalf("immediate answer = %+v, %v", got.res, got.err)
	}
	second := startDetachedTest(t, ft, detachedTestCard("ix_immediate_second"))
	deny := false
	if err := ft.s.resolve("ix_immediate_second", proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	awaitDetachedTest(t, second)
}

func TestDetachedFingerprintUsesTheExecutionDirectory(t *testing.T) {
	a := detachedTestCard("ix_cwd_a")
	b := a
	b.ID, b.ExecutionCwd = "ix_cwd_b", "/home/u/.persona/assistant/subdir"
	fa, err := detachedFingerprint(a)
	if err != nil {
		t.Fatal(err)
	}
	fb, err := detachedFingerprint(b)
	if err != nil {
		t.Fatal(err)
	}
	if fa == fb {
		t.Fatal("identical command text in different execution directories has the same grant fingerprint")
	}
}

func TestDetachedReusedIDDoesNotInheritAnImmediateAllow(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	original := detachedTestCard("ix_detached_immediate_reused")
	done := startDetachedTest(t, ft, original)
	allow := true
	if err := ft.s.resolve(original.ID, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	if got := awaitDetachedTest(t, done); got.err != nil || got.res.Allow == nil || !*got.res.Allow || got.res.ApprovalID != "" {
		t.Fatalf("immediate original Allow = %+v, %v", got.res, got.err)
	}

	changed := detachedTestCard(original.ID)
	changed.CreatedAt = changed.CreatedAt.Add(time.Second)
	changed.ToolInput = json.RawMessage(`{"command":"printf different"}`)
	res, err := ft.s.nodeInteraction(context.Background(), changed)
	if !errors.Is(err, errInteractionIDReused) || (res.Allow != nil && *res.Allow) {
		t.Fatalf("different command with a reused id = %+v, %v; want occurrence rejection", res, err)
	}
}

func TestOriginalReplayDoesNotSpendGrantBeforeTheRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ix := detachedTestCard("ix_original_always")
		ix.AlwaysKeys = []string{"lark:exact-test"}
		done := startDetachedTest(t, ft, ix)
		elapse(30 * time.Millisecond)
		awaitDetachedTest(t, done)
		allow := true
		if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &allow, Always: true}); err != nil {
			t.Fatal(err)
		}
		waitApprovalResult(t, ft, ix.ID)

		replayed, err := ft.s.nodeInteraction(context.Background(), ix)
		if err != nil || (replayed.Allow != nil && *replayed.Allow) || !replayed.Dismiss {
			t.Fatalf("the original request spent its continuation through replay or Always: %+v, %v", replayed, err)
		}
		retry := ix
		retry.ID = "ix_original_always_retry"
		got, err := ft.s.nodeInteraction(context.Background(), retry)
		if err != nil || got.Allow == nil || !*got.Allow || got.ApprovalID != ix.ID {
			t.Fatalf("the new exact retry did not receive the preserved grant: %+v, %v", got, err)
		}
	})
}

func TestInterruptedApprovedGrantIsRevokedDuringRecovery(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_interrupted_apply")
	if err := ft.s.approvalJournalStarted(context.Background(), ix); err != nil {
		t.Fatal(err)
	}
	if err := ft.s.approvalJournalApplying(context.Background(), ix); err != nil {
		t.Fatal(err)
	}
	if err := ft.s.putDetachedGrant(context.Background(), ix, proto.Resolution{}); err != nil {
		t.Fatal(err)
	}

	restarted := New(Options{Hub: ft.f, Store: func() Store { return ft.st }, Logf: testLogf(t), Devices: ft.d})
	restarted.recoverApprovalJournals(context.Background())
	if got, ok, err := restarted.consumeDetachedGrant(context.Background(), ix); err != nil || ok || got.Allow != nil {
		t.Fatalf("interrupted approval left a spendable grant: %+v, %v, %v", got, ok, err)
	}
	if result := waitFakeApprovalResult(t, ft.f, ix.ID); !strings.Contains(result, "uncertain") {
		t.Fatalf("recovered outcome = %q, want the interrupted-apply warning", result)
	}
}

func TestFailedGrantRevocationKeepsRecoveryUntilAnotherRestart(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	st := &grantWriteFailStore{fleetMem: ft.st}
	ft.s.setStore(func() Store { return st })
	ix := detachedTestCard("ix_revocation_write_failure")
	if err := ft.s.approvalJournalStarted(context.Background(), ix); err != nil {
		t.Fatal(err)
	}
	if err := ft.s.approvalJournalApplying(context.Background(), ix); err != nil {
		t.Fatal(err)
	}
	if err := ft.s.putDetachedGrant(context.Background(), ix, proto.Resolution{}); err != nil {
		t.Fatal(err)
	}
	st.fail.Store(true)
	restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
	restarted.recoverApprovalJournals(context.Background())
	if got := storedApprovalJournal(t, ft); len(got) != 1 || got[0].Status != "failed" {
		t.Fatalf("failed durable revocation was forgotten: %+v", got)
	}
	freshRetry := ix
	freshRetry.ID = "ix_revocation_write_failure_retry"
	if got, ok, err := restarted.consumeDetachedGrant(context.Background(), freshRetry); err != nil || !ok || got.Allow != nil || !got.Dismiss || got.ApprovalID != ix.ID {
		t.Fatalf("a non-approved persisted grant executed through a fresh retry id: %+v ok=%v err=%v", got, ok, err)
	}

	st.fail.Store(false)
	second := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
	second.recoverApprovalJournals(context.Background())
	if got, ok, err := second.consumeDetachedGrant(context.Background(), ix); err != nil || ok || got.Allow != nil {
		t.Fatalf("revocation did not converge after the write recovered: %+v ok=%v err=%v", got, ok, err)
	}
}

func TestNonApprovedJournalCannotBeSpentThroughFreshRetry(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_nonapproved_original")
	entry := approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID,
		Status: "failed", Result: approvalResultText(ix.ID, "failed", "Execution is uncertain."), UpdatedAt: time.Now().UTC()}
	if err := ft.st.PutSetting(context.Background(), approvalJournalKey, []approvalJournalEntry{entry}); err != nil {
		t.Fatal(err)
	}
	if err := ft.s.putDetachedGrant(context.Background(), ix, proto.Resolution{}); err != nil {
		t.Fatal(err)
	}
	restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return ft.st }, Logf: testLogf(t), Devices: ft.d})
	retry := ix
	retry.ID = "ix_nonapproved_fresh_retry"
	got, ok, err := restarted.consumeDetachedGrant(context.Background(), retry)
	if err != nil || !ok || got.Allow != nil || !got.Dismiss || got.ApprovalID != ix.ID {
		t.Fatalf("fresh retry spent a non-approved grant: %+v ok=%v err=%v", got, ok, err)
	}
}

func TestDetachedWaitingRestartFinishesTheRestoredCard(t *testing.T) {
	for _, tc := range []struct {
		name         string
		recoverFirst bool
	}{{"restore then recover", false}, {"recover then restore", true}} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				shortDetachedTimers(t)
				ft := newFleetTest(t)
				preparedTestConversation(ft)
				ix := detachedTestCard("ix_detached_restart_waiting")
				done := startDetachedTest(t, ft, ix)
				elapse(30 * time.Millisecond)
				if got := awaitDetachedTest(t, done); got.err != nil || !got.res.Pending {
					t.Fatalf("initial result = %+v, %v", got.res, got.err)
				}

				// Stop the previous manager's clock when simulating its process exit.
				ft.s.fleet.mu.Lock()
				ft.s.fleet.interactions[ix.ID].prepared.decisionTimer.Stop()
				ft.s.fleet.mu.Unlock()

				st := &restoreMem{fleetMem: ft.st}
				restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
				if tc.recoverFirst {
					restarted.recoverApprovalJournals(context.Background())
					restarted.restoreInteractions(context.Background())
				} else {
					restarted.restoreInteractions(context.Background())
					restarted.recoverApprovalJournals(context.Background())
				}
				pending := restarted.pendingInteractions()
				if len(pending) != 1 || pending[0].ID != ix.ID || !pending[0].CreatedAt.Equal(ix.CreatedAt) {
					t.Fatalf("restored cards = %+v, want the original unanswered occurrence", pending)
				}
				if status, res, err := st.InteractionOutcome(context.Background(), ix.ID); err != nil || status != "pending" || res != nil {
					t.Fatalf("restored outcome = %q, %+v, %v", status, res, err)
				}
				replayed, err := restarted.nodeInteraction(context.Background(), detachedTestCard(ix.ID))
				if err != nil || !replayed.Pending || replayed.ApprovalID != ix.ID || replayed.Allow != nil {
					t.Fatalf("original replay = %+v, %v; want the same pending request without execution", replayed, err)
				}
				retry := ix
				retry.ID = "ix_unapproved_retry"
				if res, granted, err := restarted.consumeDetachedGrant(context.Background(), retry); err != nil || granted || res.UserAllowed() {
					t.Fatalf("restart issued an execution permit: %+v, %v, %v", res, granted, err)
				}
				allow := true
				if err := restarted.resolve(ix.ID, proto.Resolution{Allow: &allow}); err != nil {
					t.Fatal(err)
				}
				waitFakeApprovalResult(t, ft.f, ix.ID)
			})
		})
	}
}

func TestDetachedRestoredReplayCannotAllowBeforeRecovery(t *testing.T) {
	for _, restore := range []bool{true, false} {
		t.Run(fmt.Sprintf("restore_%v", restore), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				shortDetachedTimers(t)
				ft := newFleetTest(t)
				preparedTestConversation(ft)
				ix := detachedTestCard("ix_detached_replay_before_recovery")
				done := startDetachedTest(t, ft, ix)
				elapse(30 * time.Millisecond)
				if got := awaitDetachedTest(t, done); got.err != nil || !got.res.Pending {
					t.Fatalf("initial result = %+v, %v", got.res, got.err)
				}
				st := &restoreMem{fleetMem: ft.st}
				restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
				if restore {
					restarted.restoreInteractions(context.Background())
				}
				replayed := make(chan detachedTestResult, 1)
				go func() {
					res, err := restarted.nodeInteraction(context.Background(), detachedTestCard(ix.ID))
					replayed <- detachedTestResult{res, err}
				}()
				elapse(0)
				allow := true
				if err := restarted.resolve(ix.ID, proto.Resolution{Allow: &allow}); err != nil {
					t.Fatal(err)
				}
				got := awaitDetachedTest(t, replayed)
				if got.err != nil || !got.res.Pending || got.res.Allow != nil || got.res.ApprovalID != ix.ID {
					t.Fatalf("restored replay before recovery = %+v, %v; want the pending original", got.res, got.err)
				}
				restarted.recoverApprovalJournals(context.Background())
				if result := waitFakeApprovalResult(t, ft.f, ix.ID); !strings.Contains(result, "approved") {
					t.Fatalf("owner decision before scheduler recovery = %q", result)
				}
			})
		})
	}
}

func TestDetachedTerminalJournalClosesRestoredCard(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_detached_terminal_before_history")
	result := approvalResultText(ix.ID, "approved", "Retry the same tool now through its original approval hook.")
	entry := approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID,
		Status: "approved", Result: result, Continuation: true}
	if err := ft.s.putDetachedGrant(context.Background(), ix, proto.Resolution{}); err != nil {
		t.Fatal(err)
	}
	// Production persists the grant before the terminal journal. Keep this
	// legacy journal occurrence-free while preserving that write ordering.
	entry.UpdatedAt = time.Now().UTC()
	if err := ft.st.PutSetting(context.Background(), approvalJournalKey, []approvalJournalEntry{entry}); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after the terminal journal write but before the pending
	// interaction row and its restored live card received that outcome.
	ft.s.record(ix)
	st := &restoreMem{fleetMem: ft.st}
	restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
	restarted.restoreInteractions(context.Background())
	if got := len(restarted.pendingInteractions()); got != 0 {
		t.Fatalf("terminal journal exposed %d answerable cards", got)
	}
	restarted.recoverApprovalJournals(context.Background())
	if got := len(restarted.pendingInteractions()); got != 0 {
		t.Fatalf("terminal journal left an answerable restored card: pending=%d", got)
	}
	status, resolution, err := st.InteractionOutcome(context.Background(), ix.ID)
	if err != nil || status != "resolved" || resolution == nil || !resolution.Dismiss || (resolution.Allow != nil && *resolution.Allow) {
		t.Fatalf("recovered terminal interaction = status %q resolution %+v err %v", status, resolution, err)
	}
	if got := waitFakeApprovalResult(t, ft.f, ix.ID); got != result {
		t.Fatalf("terminal continuation = %q, want the recorded result %q", got, result)
	}
}

func TestRecoveryFinalizesWholeBatchBeforeDeliveringAnyResult(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	a := detachedTestCard("ix_recovery_batch_a")
	b := detachedTestCard("ix_recovery_batch_b")
	entries := []approvalJournalEntry{}
	for _, ix := range []proto.Interaction{a, b} {
		entries = append(entries, approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID,
			Status: "denied", Result: approvalResultText(ix.ID, "denied", "Nothing was executed."), UpdatedAt: time.Now().UTC()})
		ft.s.record(ix)
	}
	if err := ft.st.PutSetting(context.Background(), approvalJournalKey, entries); err != nil {
		t.Fatal(err)
	}
	st := &recoveryFailStore{restoreMem: &restoreMem{fleetMem: ft.st}, failID: b.ID}
	restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
	restarted.restoreInteractions(context.Background())
	restarted.recoverApprovalJournals(context.Background())
	if got := storedApprovalJournal(t, ft); len(got) != 2 {
		t.Fatalf("a recovered result was forgotten after its sibling failed: %+v", got)
	}
	if status, _, _ := st.InteractionOutcome(context.Background(), a.ID); status != "resolved" {
		t.Fatalf("first recovered interaction = %q, want resolved", status)
	}
	if status, _, _ := st.InteractionOutcome(context.Background(), b.ID); status != "pending" {
		t.Fatalf("failed recovered interaction = %q, want pending for the retry", status)
	}
	// Even an explicit concurrent drain cannot deliver A while B's interaction is
	// not durable. Otherwise a crash could leave B pending without its journal.
	restarted.deliverApprovalResults()
	ft.f.mu.Lock()
	sent := len(ft.f.sends)
	ft.f.mu.Unlock()
	if sent != 0 {
		t.Fatalf("recovery delivered %d results before the whole batch was durable", sent)
	}

	st.failID = ""
	restarted.recoverApprovalJournals(context.Background())
	for _, ix := range []proto.Interaction{a, b} {
		if status, _, _ := st.InteractionOutcome(context.Background(), ix.ID); status != "resolved" {
			t.Fatalf("retried recovered interaction %s = %q", ix.ID, status)
		}
		waitFakeApprovalResult(t, ft.f, ix.ID)
	}
}

func TestRecoveredApprovedJournalFinalizesAMissingInteractionBeforeItsGrantCanBeSpent(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_recovery_missing_history")
	putTerminalOccurrenceJournal(t, ft, ix, "approved", "Retry the exact call once.")
	if err := ft.s.putDetachedGrant(context.Background(), ix, proto.Resolution{}); err != nil {
		t.Fatal(err)
	}
	// No InsertInteraction: the original best-effort history write was lost.
	st := &restoreMem{fleetMem: ft.st}
	restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
	restarted.recoverApprovalJournals(context.Background())
	if status, _, err := st.InteractionOutcome(context.Background(), ix.ID); err != nil || status != "resolved" {
		t.Fatalf("missing history after recovery = %q, %v; want a durable terminal row", status, err)
	}
	waitFakeApprovalResult(t, ft.f, ix.ID)
	retry := ix
	retry.ID = "ix_recovery_missing_history_retry"
	got, ok, err := restarted.consumeDetachedGrant(context.Background(), retry)
	if err != nil || !ok || got.Allow == nil || !*got.Allow {
		t.Fatalf("exact retry after recovery = %+v ok=%v err=%v", got, ok, err)
	}
	replayCtx, cancelReplay := context.WithTimeout(context.Background(), 100*time.Millisecond)
	replayed, err := restarted.nodeInteraction(replayCtx, detachedTestCard(ix.ID))
	cancelReplay()
	if err == nil && replayed.Allow != nil && *replayed.Allow {
		t.Fatalf("the consumed original executed again after its missing history was recovered: %+v", replayed)
	}
}

func TestRecoveryDoesNotFinishANewerPendingOccurrence(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	first := detachedTestCard("ix_recovery_reused_id")
	putTerminalOccurrenceJournal(t, ft, first, "denied", "Nothing was executed.")
	ft.s.record(first)
	if err := ft.st.FinishInteraction(context.Background(), first.ID, "resolved", nil); err != nil {
		t.Fatal(err)
	}
	newer := first
	newer.CreatedAt = first.CreatedAt.Add(time.Second)
	newer.ToolInput = json.RawMessage(`{"command":"printf newer"}`)
	if err := ft.st.InsertInteraction(context.Background(), newer); err != nil {
		t.Fatal(err)
	}
	st := &restoreMem{fleetMem: ft.st}
	restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
	restarted.restoreInteractions(context.Background())
	if got := restarted.pendingInteractions(); len(got) != 1 || !got[0].CreatedAt.Equal(newer.CreatedAt) {
		t.Fatalf("restored occurrences = %+v, want only the newer one", got)
	}
	restarted.recoverApprovalJournals(context.Background())
	if status, _, _ := st.InteractionOutcome(context.Background(), newer.ID); status != "pending" {
		t.Fatalf("the old journal finalized the newer occurrence: %q", status)
	}
	if got := storedApprovalJournal(t, ft); len(got) != 1 || got[0].Finalized {
		t.Fatalf("old journal was released while the newer occurrence was pending: %+v", got)
	}
	ft.f.mu.Lock()
	sent := len(ft.f.sends)
	ft.f.mu.Unlock()
	if sent != 0 {
		t.Fatalf("old result delivered while the newer occurrence was pending: %d sends", sent)
	}
	deny := false
	if err := restarted.resolve(newer.ID, proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	restarted.recoverApprovalJournals(context.Background())
	waitFakeApprovalResult(t, ft.f, first.ID)
	if status, resolution, _ := st.InteractionOutcome(context.Background(), newer.ID); status != "resolved" || resolution == nil || resolution.Allow == nil || *resolution.Allow {
		t.Fatalf("newer occurrence outcome = %q %+v, want its own denial", status, resolution)
	}
}

func TestRestoreSnapshotCannotResurrectAnInteractionAfterRecovery(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_restore_recovery_interleave")
	putTerminalOccurrenceJournal(t, ft, ix, "denied", "Nothing was executed.")
	ft.s.record(ix)
	st := &restoreInterleaveStore{restoreMem: &restoreMem{fleetMem: ft.st}, snapshot: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { st.releaseOnce.Do(func() { close(st.release) }) })
	restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
	restored := make(chan struct{})
	go func() {
		restarted.restoreInteractions(context.Background())
		close(restored)
	}()
	select {
	case <-st.snapshot:
	case <-time.After(5 * time.Second):
		t.Fatal("restore did not take its pending snapshot")
	}
	restarted.recoverApprovalJournals(context.Background())
	st.releaseOnce.Do(func() { close(st.release) })
	select {
	case <-restored:
	case <-time.After(5 * time.Second):
		t.Fatal("restore did not finish")
	}
	if got := restarted.pendingInteractions(); len(got) != 0 {
		t.Fatalf("restore resurrected a recovered interaction: %+v", got)
	}
	if status, _, _ := st.InteractionOutcome(context.Background(), ix.ID); status != "resolved" {
		t.Fatalf("recovered occurrence = %q, want resolved", status)
	}
	waitFakeApprovalResult(t, ft.f, ix.ID)
}

func TestLiveFinalizerRetainsItsJournalUntilHistoryIsDurable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ix := detachedTestCard("ix_live_history_failure")
		st := &recoveryFailStore{restoreMem: &restoreMem{fleetMem: ft.st}}
		ft.s.setStore(func() Store { return st })
		done := startDetachedTest(t, ft, ix)
		elapse(30 * time.Millisecond)
		if got := awaitDetachedTest(t, done); got.err != nil || !got.res.Pending {
			t.Fatalf("initial result = %+v, %v", got.res, got.err)
		}
		st.failID = ix.ID
		allow := true
		if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		elapse(0)
		if status, _, _ := st.InteractionOutcome(context.Background(), ix.ID); status != "pending" {
			t.Fatalf("fault-injected history status = %q, want pending", status)
		}
		entries := storedApprovalJournal(t, ft)
		if len(entries) != 1 || entries[0].Finalized {
			t.Fatalf("result was finalized without durable history: %+v", entries)
		}
		ft.f.mu.Lock()
		sent := len(ft.f.sends)
		ft.f.mu.Unlock()
		if sent != 0 {
			t.Fatalf("result delivered without durable history: %d sends", sent)
		}
		st.failID = ""
		ft.s.deliverApprovalResults()
		waitFakeApprovalResult(t, ft.f, ix.ID)
		if status, _, _ := st.InteractionOutcome(context.Background(), ix.ID); status != "resolved" {
			t.Fatalf("retried history status = %q, want resolved", status)
		}
	})
}

func TestInitialJournalReadFailureRetriesRecovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ix := detachedTestCard("ix_initial_journal_read_failure")
		putTerminalOccurrenceJournal(t, ft, ix, "denied", "Nothing was executed.")
		ft.s.record(ix)
		st := &journalReadFailStore{restoreMem: &restoreMem{fleetMem: ft.st}}
		st.fail.Store(true)
		restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
		restarted.recoverApprovalJournals(context.Background())
		restarted.approvals.mu.Lock()
		recovering, done, retry := restarted.approvals.recovering, restarted.approvals.recoveryDone, restarted.approvals.recoveryRetry
		restarted.approvals.mu.Unlock()
		if !recovering || done || !retry {
			t.Fatalf("failed initial read did not retain a closed retry gate: recovering=%v done=%v retry=%v", recovering, done, retry)
		}
		st.fail.Store(false)
		elapse(time.Minute)
		waitFakeApprovalResult(t, ft.f, ix.ID)
		restarted.approvals.mu.Lock()
		recovering, done = restarted.approvals.recovering, restarted.approvals.recoveryDone
		restarted.approvals.mu.Unlock()
		if recovering || !done {
			t.Fatalf("retry did not complete recovery: recovering=%v done=%v", recovering, done)
		}
	})
}

func TestRecoveryAcceptsLiveTerminalHistoryBeforeDelivery(t *testing.T) {
	for _, tc := range []struct {
		journalStatus string
		historyStatus string
	}{{"denied", "resolved"}, {"failed", "failed"}, {"covered", "resolved"}} {
		t.Run(tc.journalStatus, func(t *testing.T) {
			ft := newFleetTest(t)
			preparedTestConversation(ft)
			ix := detachedTestCard("ix_live_terminal_before_delivery_" + tc.journalStatus)
			entry := putTerminalOccurrenceJournal(t, ft, ix, tc.journalStatus, "The live operation already ended.")
			entry.Finalized = true // the strict live history write succeeded; delivery did not run
			putApprovalJournal(t, ft, entry)
			ft.s.record(ix)
			resolution := &proto.Resolution{Dismiss: true}
			if tc.journalStatus == "denied" {
				allow := false
				resolution = &proto.Resolution{Allow: &allow}
			}
			if err := ft.st.FinishInteraction(context.Background(), ix.ID, tc.historyStatus, resolution); err != nil {
				t.Fatal(err)
			}
			st := &restoreMem{fleetMem: ft.st}
			restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
			restarted.recoverApprovalJournals(context.Background())
			waitFakeApprovalResult(t, ft.f, ix.ID)
			if status, _, _ := st.InteractionOutcome(context.Background(), ix.ID); status != tc.historyStatus {
				t.Fatalf("recovery changed the live terminal status to %q, want %q", status, tc.historyStatus)
			}
		})
	}
}

func TestRecoveryKeepsAnOwnerResolutionNewerThanItsStaleJournal(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_recovery_owner_already_resolved")
	if err := ft.s.approvalJournalStarted(context.Background(), ix); err != nil {
		t.Fatal(err)
	}
	if err := ft.s.putDetachedGrant(context.Background(), ix, proto.Resolution{}); err != nil {
		t.Fatal(err)
	}
	ft.s.record(ix)
	allow := true
	if err := ft.st.FinishInteraction(context.Background(), ix.ID, "resolved", &proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	st := &restoreMem{fleetMem: ft.st}
	restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
	restarted.recoverApprovalJournals(context.Background())
	restarted.approvals.mu.Lock()
	recovering, done := restarted.approvals.recovering, restarted.approvals.recoveryDone
	restarted.approvals.mu.Unlock()
	if recovering || !done {
		t.Fatalf("the owner's terminal outcome left recovery stuck: recovering=%v done=%v", recovering, done)
	}
	if status, _, _ := st.InteractionOutcome(context.Background(), ix.ID); status != "resolved" {
		t.Fatalf("recovery replaced the owner's outcome with %q", status)
	}
	if result := waitFakeApprovalResult(t, ft.f, ix.ID); !strings.Contains(result, "approved") || strings.Contains(result, "cancelled") {
		t.Fatalf("recovery delivered its stale journal instead of the owner outcome: %q", result)
	}
	retry := ix
	retry.ID = "ix_recovery_owner_already_resolved_retry"
	got, ok, err := restarted.consumeDetachedGrant(context.Background(), retry)
	if err != nil || !ok || got.Allow == nil || !*got.Allow {
		t.Fatalf("the recorded owner Allow did not preserve its one-time continuation: %+v ok=%v err=%v", got, ok, err)
	}
}

func TestRecoveryDoesNotFinalizeOverAnOwnerClaimInFlight(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_recovery_owner_claim_in_flight")
	entry := putTerminalOccurrenceJournal(t, ft, ix, "cancelled", "The assistant restarted.")
	ft.s.record(ix)
	restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return &restoreMem{fleetMem: ft.st} }, Logf: testLogf(t), Devices: ft.d})
	allow := true
	restarted.fleet.mu.Lock()
	restarted.fleet.interactions = map[string]*pending{ix.ID: {ix: ix, claimed: true, resolution: &proto.Resolution{Allow: &allow}}}
	restarted.fleet.mu.Unlock()
	if err := restarted.finishRecoveredApproval(entry); err == nil {
		t.Fatal("recovery finalized its stale cancellation while the owner claim was still executing")
	}
	if status, _, _ := ft.st.InteractionOutcome(context.Background(), ix.ID); status != "pending" {
		t.Fatalf("recovery wrote %q over the in-flight owner claim", status)
	}
}

func TestUnfinishedLegacyJournalKeepsItsOccurrenceCutoff(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_legacy_cutoff")
	updatedAt := time.Now().Add(-time.Hour).UTC()
	ix.CreatedAt = updatedAt.Add(time.Minute)
	putApprovalJournal(t, ft, approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID, UpdatedAt: updatedAt})
	ft.s.record(ix)
	st := &restoreMem{fleetMem: ft.st}
	restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
	restarted.restoreInteractions(context.Background())
	restarted.recoverApprovalJournals(context.Background())
	if status, _, _ := st.InteractionOutcome(context.Background(), ix.ID); status != "pending" {
		t.Fatalf("legacy recovery cancelled the newer card: %q", status)
	}
	entries := storedApprovalJournal(t, ft)
	if len(entries) != 1 || !entries[0].UpdatedAt.Equal(updatedAt) || entries[0].Finalized {
		t.Fatalf("legacy cutoff changed during recovery: %+v", entries)
	}
	deny := false
	if err := restarted.resolve(ix.ID, proto.Resolution{Allow: &deny}); err != nil {
		t.Fatal(err)
	}
	restarted.recoverApprovalJournals(context.Background())
	waitFakeApprovalResult(t, ft.f, ix.ID)
}

func TestInitialJournalRetryDoesNotCancelAnApprovalCreatedByThisProcess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		approvalWaitingWindow = time.Hour // advance past the retry, not the live card's own window
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		st := &journalReadFailStore{restoreMem: &restoreMem{fleetMem: ft.st}}
		st.fail.Store(true)
		restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
		restarted.recoverApprovalJournals(context.Background())
		st.fail.Store(false)
		live := &fleetTest{t: t, s: restarted, d: ft.d, f: ft.f, st: ft.st}
		ix := detachedTestCard("ix_created_before_initial_retry")
		done := startDetachedTest(t, live, ix)
		elapse(30 * time.Millisecond)
		if got := awaitDetachedTest(t, done); got.err != nil || !got.res.Pending {
			t.Fatalf("new live approval = %+v, %v", got.res, got.err)
		}
		elapse(time.Minute)
		if got := restarted.pendingInteractions(); len(got) != 1 || got[0].ID != ix.ID {
			t.Fatalf("startup retry cancelled a current-process approval: %+v", got)
		}
		allow := true
		if err := restarted.resolve(ix.ID, proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		waitFakeApprovalResult(t, ft.f, ix.ID)
	})
}

func TestOlderDenialDoesNotRevokeANewerOccurrenceGrant(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	first := detachedTestCard("ix_old_denial_new_grant")
	putTerminalOccurrenceJournal(t, ft, first, "denied", "Nothing was executed.")
	newer := first
	newer.CreatedAt = first.CreatedAt.Add(time.Second)
	newer.ToolInput = json.RawMessage(`{"command":"printf newer"}`)
	if err := ft.s.putDetachedGrant(context.Background(), newer, proto.Resolution{}); err != nil {
		t.Fatal(err)
	}
	st := &restoreMem{fleetMem: ft.st}
	restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
	restarted.recoverApprovalJournals(context.Background())
	waitFakeApprovalResult(t, ft.f, first.ID)
	retry := newer
	retry.ID = "ix_newer_occurrence_retry"
	got, ok, err := restarted.consumeDetachedGrant(context.Background(), retry)
	if err != nil || !ok || got.Allow == nil || !*got.Allow {
		t.Fatalf("older denial revoked the newer occurrence's grant: %+v ok=%v err=%v", got, ok, err)
	}
}

func TestLegacyDenialDoesNotRevokeANewerGrant(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	first := detachedTestCard("ix_legacy_denial_new_grant")
	legacy := approvalJournalEntry{ID: first.ID, MachineID: first.MachineID, SessionID: first.SessionID,
		Status: "denied", Result: "Nothing was executed.", UpdatedAt: time.Now().Add(-time.Hour).UTC()}
	putApprovalJournal(t, ft, legacy)
	newer := first
	newer.CreatedAt = first.CreatedAt.Add(time.Second)
	newer.ToolInput = json.RawMessage(`{"command":"printf newer legacy"}`)
	if err := ft.s.putDetachedGrant(context.Background(), newer, proto.Resolution{}); err != nil {
		t.Fatal(err)
	}
	if err := ft.s.revokeDetachedGrant(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	retry := newer
	retry.ID = "ix_legacy_denial_new_grant_retry"
	got, ok, err := ft.s.consumeDetachedGrant(context.Background(), retry)
	if err != nil || !ok || got.Allow == nil || !*got.Allow {
		t.Fatalf("fingerprintless older denial suppressed or revoked the newer grant: %+v ok=%v err=%v", got, ok, err)
	}
}

func TestRestoreRetriesATransientOccurrenceRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ix := detachedTestCard("ix_restore_transient_read")
		ft.s.record(ix)
		st := &restoreReadFailStore{restoreMem: &restoreMem{fleetMem: ft.st}, id: ix.ID}
		st.fail.Store(true)
		restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
		restarted.restoreInteractions(context.Background())
		if got := restarted.pendingInteractions(); len(got) != 0 {
			t.Fatalf("failed read installed an unvalidated card: %+v", got)
		}
		st.fail.Store(false)
		elapse(time.Minute)
		got := restarted.pendingInteractions()
		if len(got) != 1 || got[0].ID != ix.ID || !got[0].CreatedAt.Equal(ix.CreatedAt) {
			t.Fatalf("transient read permanently dropped the resumable card: %+v", got)
		}
		var published []proto.Interaction
		for _, raw := range ft.toldOf(assistantwire.EventFleetInteraction) {
			var event assistantwire.FleetInteraction
			if json.Unmarshal(raw, &event) == nil {
				published = append(published, event.Interaction)
			}
		}
		if len(published) != 1 || published[0].ID != ix.ID || !published[0].CreatedAt.Equal(ix.CreatedAt) {
			t.Fatalf("retry restored an invisible card; hub events = %+v", published)
		}
	})
}

func TestRestoreRetriesTheInitialExpirationReadWithoutCancellingLiveCards(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ix := detachedTestCard("ix_restore_initial_read")
		ft.s.record(ix)
		st := &expireReadFailStore{restoreMem: &restoreMem{fleetMem: ft.st}}
		st.fail.Store(true)
		restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
		restarted.restoreInteractions(context.Background())
		if got := restarted.pendingInteractions(); len(got) != 0 {
			t.Fatalf("failed startup read installed a card: %+v", got)
		}
		live := resumable("ix_live_during_initial_restore_retry")
		live.Resumable = false
		live.CreatedAt = time.Now().UTC()
		liveCtx, cancelLive := context.WithCancel(context.Background())
		defer cancelLive()
		liveDone := make(chan struct{})
		go func() {
			_, _ = restarted.waitInteraction(liveCtx, live)
			close(liveDone)
		}()
		waitFor(t, "the current-process card to register", func() bool { return len(restarted.pendingInteractions()) == 1 })
		st.fail.Store(false)
		elapse(time.Minute)
		got := restarted.pendingInteractions()
		if len(got) != 2 {
			t.Fatalf("startup retry restored %d cards, want the recovered and live cards: %+v", len(got), got)
		}
		seen := map[string]bool{}
		for _, pending := range got {
			seen[pending.ID] = true
		}
		if !seen[ix.ID] || !seen[live.ID] {
			t.Fatalf("startup retry lost a card: %+v", got)
		}
		if status, _, err := st.InteractionOutcome(context.Background(), live.ID); err != nil || status != "pending" {
			t.Fatalf("startup retry cancelled a current-process card in history: status %q, %v", status, err)
		}
		cancelLive()
		<-liveDone
	})
}

func TestDetachedFingerprintCanonicalizesJSONKeyOrder(t *testing.T) {
	first := detachedTestCard("ix_canonical_fingerprint")
	first.ToolInput = json.RawMessage(`{"command":"pwd","x":1,"nested":{"z":2,"a":3}}`)
	second := first
	second.ToolInput = json.RawMessage(`{"nested":{"a":3,"z":2},"x":1,"command":"pwd"}`)
	a, err := detachedFingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := detachedFingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("JSON object key order changed the detached occurrence identity")
	}
	second.ToolInput = json.RawMessage(`{"nested":{"a":3,"z":2},"x":1,"command":"printf changed"}`)
	b, err = detachedFingerprint(second)
	if err != nil || a == b {
		t.Fatalf("a changed command kept the same fingerprint: err=%v", err)
	}
}

func TestDetachedFingerprintCanonicalizesJSONBNumbers(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{"exponent", `{"command":"pwd","x":1e2}`, `{"x":100,"command":"pwd"}`},
		{"decimal scale", `{"command":"pwd","x":1.00}`, `{"x":1,"command":"pwd"}`},
		{"nested signed zero", `{"command":"pwd","x":[-0.0,0.01]}`, `{"x":[0,1e-2],"command":"pwd"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := detachedTestCard("ix_jsonb_number")
			first.ToolInput = json.RawMessage(tc.before)
			second := first
			second.ToolInput = json.RawMessage(tc.after)
			a, err := detachedFingerprint(first)
			if err != nil {
				t.Fatal(err)
			}
			b, err := detachedFingerprint(second)
			if err != nil || a != b {
				t.Fatalf("a JSONB numeric round trip changed identity: err=%v\nfirst  %s\nsecond %s", err, a, b)
			}
		})
	}
}

func TestPreCanonicalDetachedGrantRemainsConsumable(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_pr1_raw_grant")
	ix.ToolInput = json.RawMessage(`{"x":1,"command":"pwd"}`)
	legacy, err := legacyDetachedFingerprint(ix)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := detachedFingerprint(ix)
	if err != nil {
		t.Fatal(err)
	}
	if legacy == canonical {
		t.Fatal("fixture did not distinguish the PR1 raw and canonical fingerprints")
	}
	if err := ft.st.PutSetting(context.Background(), detachedGrantSetting, map[string]detachedGrant{ix.ID: {
		Fingerprint: legacy, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}}); err != nil {
		t.Fatal(err)
	}
	// Repeating Apply after an upgrade is idempotent with the old on-disk form.
	if err := ft.s.putDetachedGrant(context.Background(), ix, proto.Resolution{}); err != nil {
		t.Fatalf("old grant was rejected as a different approval: %v", err)
	}
	retry := ix
	retry.ID = "ix_pr1_raw_grant_retry"
	got, ok, err := ft.s.consumeDetachedGrant(context.Background(), retry)
	if err != nil || !ok || got.Allow == nil || !*got.Allow {
		t.Fatalf("PR1 grant did not survive canonicalization: %+v ok=%v err=%v", got, ok, err)
	}
}

func TestPreCanonicalDetachedGrantAcceptsReorderedRetry(t *testing.T) {
	for _, tc := range []struct {
		name     string
		checkPut bool
	}{{"consume reordered input", false}, {"idempotent put with reordered input", true}} {
		t.Run(tc.name, func(t *testing.T) {
			ft := newFleetTest(t)
			preparedTestConversation(ft)
			ix := detachedTestCard("ix_pr1_raw_grant_reordered")
			ix.ToolInput = json.RawMessage(`{"x":1,"command":"pwd"}`)
			legacy, err := legacyDetachedFingerprint(ix)
			if err != nil {
				t.Fatal(err)
			}
			if err := ft.st.PutSetting(context.Background(), detachedGrantSetting, map[string]detachedGrant{ix.ID: {
				Fingerprint: legacy, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC(),
			}}); err != nil {
				t.Fatal(err)
			}
			retry := ix
			retry.ToolInput = json.RawMessage(`{"command":"pwd","x":1}`)
			if tc.checkPut {
				if err := ft.s.putDetachedGrant(context.Background(), retry, proto.Resolution{}); err != nil {
					t.Fatalf("reordered input was not idempotent with the stored PR1 grant: %v", err)
				}
			}
			retry.ID = "ix_pr1_raw_grant_reordered_retry"
			got, ok, err := ft.s.consumeDetachedGrant(context.Background(), retry)
			if err != nil || !ok || got.Allow == nil || !*got.Allow {
				t.Fatalf("PR1 grant did not accept equivalent reordered input: %+v ok=%v err=%v", got, ok, err)
			}
		})
	}
}

func TestLiveJournaledDetachedReconnectStillWaitsForTheOwner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ix := detachedTestCard("ix_live_journaled_reconnect")
		done := startDetachedTest(t, ft, ix)
		elapse(30 * time.Millisecond)
		if got := awaitDetachedTest(t, done); got.err != nil || !got.res.Pending {
			t.Fatalf("initial result = %+v, %v", got.res, got.err)
		}
		ft.s.approvals.mu.Lock()
		ft.s.approvals.recoveryDone = true
		ft.s.approvals.mu.Unlock()
		reconnected := startDetachedTest(t, ft, detachedTestCard(ix.ID))
		elapse(30 * time.Millisecond)
		got := awaitDetachedTest(t, reconnected)
		if got.err != nil || !got.res.Pending || !got.res.Dismiss || got.res.Allow != nil {
			t.Fatalf("live reconnect = %+v, %v; want the same pending wait", got.res, got.err)
		}
		allow := true
		if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		waitFakeApprovalResult(t, ft.f, ix.ID)
	})
}

func TestApprovalJournalPersistsOnlyTheCallDigest(t *testing.T) {
	ft := newFleetTest(t)
	ix := detachedTestCard("ix_journal_digest_only")
	ix.ToolInput = json.RawMessage(`{"command":"printf synthetic-secret-argument"}`)
	if err := ft.s.approvalJournalStarted(context.Background(), ix); err != nil {
		t.Fatal(err)
	}
	entries := storedApprovalJournal(t, ft)
	if len(entries) != 1 || len(entries[0].Fingerprint) != 64 {
		t.Fatalf("journal occurrence = %+v, want one SHA-256 digest", entries)
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "synthetic-secret-argument") || strings.Contains(string(raw), `"command"`) {
		t.Fatalf("journal persisted raw tool arguments: %s", raw)
	}
}

func TestDetachedDisconnectKeepsResumableCardForReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ix := detachedTestCard("ix_disconnect")
		ctx, cancel := context.WithCancel(context.Background())
		first := make(chan detachedTestResult, 1)
		go func() {
			res, err := ft.s.nodeInteraction(ctx, ix)
			first <- detachedTestResult{res, err}
		}()
		ft.waitPending(1)
		cancel()
		if got := awaitDetachedTest(t, first); got.err != nil || !got.res.Dismiss {
			t.Fatalf("disconnected original = %+v, %v, want reconnectable dismissal", got.res, got.err)
		}
		elapse(30 * time.Millisecond)
		if got := len(ft.s.pendingInteractions()); got != 1 {
			t.Fatalf("the initial timer cancelled the disconnected card; pending = %d", got)
		}

		reconnected := make(chan detachedTestResult, 1)
		go func() {
			res, err := ft.s.nodeInteraction(context.Background(), ix)
			reconnected <- detachedTestResult{res, err}
		}()
		elapse(30 * time.Millisecond)
		got := awaitDetachedTest(t, reconnected)
		if got.err != nil || !got.res.Pending || got.res.ApprovalID != ix.ID {
			t.Fatalf("reconnected initial wait = %+v, %v", got.res, got.err)
		}
		deny := false
		if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &deny}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDetachedWaitingSurvivesTerminalReconcileAndNodeDismiss(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		at := time.Now()
		items := []proto.Item{
			{ID: "call#0", Seq: 10, TS: at.Add(-time.Millisecond), Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "bash-call"},
			{ID: "result#0", Seq: 11, TS: at.Add(time.Second), Role: "user", Kind: proto.KindToolResult, ToolUseID: "bash-call", Text: approvalops.WaitingText},
		}
		ft := planFleet(t, true, func(int) ([]proto.Item, bool, error) { return items, false, nil })
		preparedTestConversation(ft)
		ix := detachedTestCard("ix_detached_terminal_result")
		ix.ToolUseID, ix.CreatedAt, ix.ExpiresAt = "bash-call", at, at.Add(time.Hour)
		done := startDetachedTest(t, ft, ix)
		elapse(30 * time.Millisecond)
		if got := awaitDetachedTest(t, done); got.err != nil || !got.res.Pending {
			t.Fatalf("initial result = %+v, %v", got.res, got.err)
		}

		// The pending tool result is the turn being released, not a decision in
		// the terminal. A revision scan and a late node dismissal must both leave
		// the authoritative card available to its owner.
		ft.s.syncPlanInteractions("mac")
		elapse(0)
		ft.s.NodeEvent("mac", "interaction.dismiss", json.RawMessage(`{"id":"`+ix.ID+`"}`))
		if got := len(ft.s.pendingInteractions()); got != 1 {
			status, res, _ := ft.st.InteractionOutcome(context.Background(), ix.ID)
			t.Fatalf("waiting card was withdrawn: pending=%d status=%s resolution=%+v", got, status, res)
		}
		allow := true
		if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &allow}); err != nil {
			t.Fatal(err)
		}
		waitApprovalResult(t, ft, ix.ID)
		retry := detachedTestCard("ix_detached_terminal_result_retry")
		got, err := ft.s.nodeInteraction(context.Background(), retry)
		if err != nil || got.Allow == nil || !*got.Allow || got.ApprovalID != ix.ID {
			t.Fatalf("owner's exact continuation = %+v, %v", got, err)
		}
	})
}

func TestDetachedWaitingWinsAnInFlightTerminalReconcile(t *testing.T) {
	at := time.Now()
	started, release := make(chan struct{}), make(chan struct{})
	items := []proto.Item{
		{ID: "call#0", Seq: 10, TS: at.Add(-time.Millisecond), Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "bash-call"},
		{ID: "result#0", Seq: 11, TS: at.Add(time.Second), Role: "user", Kind: proto.KindToolResult, ToolUseID: "bash-call", Text: approvalops.WaitingText},
	}
	ft := planFleet(t, false, func(int) ([]proto.Item, bool, error) {
		close(started)
		<-release
		return items, false, nil
	})
	ix := detachedTestCard("ix_detached_reconcile_race")
	ix.ToolUseID, ix.CreatedAt, ix.ExpiresAt = "bash-call", at, at.Add(time.Hour)
	p := &pending{ix: ix, done: make(chan proto.Resolution, 1), prepared: &preparedApproval{}}
	putPending(ft.s, p)
	done := make(chan struct{})
	go func() {
		ft.s.checkPlanInteraction("mac", p)
		close(done)
	}()
	<-started
	ft.s.fleet.mu.Lock()
	p.prepared.clarified, p.prepared.waiting = true, true
	ft.s.fleet.mu.Unlock()
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("terminal reconciliation did not finish")
	}
	ft.s.fleet.mu.Lock()
	kept := ft.s.fleet.interactions[ix.ID] == p
	ft.s.fleet.mu.Unlock()
	if !kept {
		t.Fatal("a reconciliation begun before the waiting state withdrew the retained card")
	}
	select {
	case r := <-p.done:
		t.Fatalf("waiting card received a terminal dismissal: %+v", r)
	default:
	}
}

func TestConsumedContinuationDoesNotAskForAnotherRetry(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_stale_continuation")
	allow := true
	if err := ft.s.putDetachedGrant(context.Background(), ix, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	retry := ix
	retry.ID = "ix_stale_continuation_retry"
	got, err := ft.s.nodeInteraction(context.Background(), retry)
	if err != nil || got.Allow == nil || !*got.Allow {
		t.Fatalf("matching call did not consume the grant: %+v, %v", got, err)
	}
	entry := approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID, Status: "approved",
		Result: approvalResultText(ix.ID, "approved", detachedContinuationText(ix, proto.Resolution{Allow: &allow})), Continuation: true}
	if err := ft.s.deliverApprovalResult(entry); err != nil {
		t.Fatal(err)
	}
	result := waitFakeApprovalResult(t, ft.f, ix.ID)
	if !strings.Contains(result, "Do not retry") || strings.Contains(result, "Retry the same tool now") {
		t.Fatalf("consumed continuation instructed a duplicate retry: %q", result)
	}
}

func TestQueuedConsumedContinuationDoesNotAskForAnotherRetry(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_queued_consumed_continuation")
	allow := true
	if err := ft.s.putDetachedGrant(context.Background(), ix, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	working := proto.Session{MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude", Status: proto.StatusWorking, State: proto.StateWorking}
	ft.f.setSessions("mac", working)
	ft.d.setSessions("mac", []proto.Session{working})
	entry := approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID, Status: "approved",
		Result: approvalResultText(ix.ID, "approved", detachedContinuationText(ix, proto.Resolution{Allow: &allow})), Continuation: true}
	if err := ft.s.deliverApprovalResult(entry); err != nil {
		t.Fatal(err)
	}
	retry := ix
	retry.ID = "ix_queued_consumed_continuation_retry"
	got, err := ft.s.nodeInteraction(context.Background(), retry)
	if err != nil || got.Allow == nil || !*got.Allow {
		t.Fatalf("matching call did not consume the grant while the result queued: %+v, %v", got, err)
	}
	idle := working
	idle.Status, idle.State = proto.StatusIdle, proto.StateIdle
	ft.f.setSessions("mac", idle)
	ft.d.setSessions("mac", []proto.Session{idle})
	ft.s.drainRelays("mac", []proto.Session{idle})
	result := waitFakeApprovalResult(t, ft.f, ix.ID)
	if !strings.Contains(result, "Do not retry") || strings.Contains(result, "Retry the same tool now") {
		t.Fatalf("queued consumed continuation instructed a duplicate retry: %q", result)
	}
	waitFor(t, "the delivered continuation to leave the durable queue", func() bool { return ft.s.queuedFor("s1") == 0 })
}

func TestContinuationRelayIdentitySurvivesTextRewrite(t *testing.T) {
	at := time.Now().UTC()
	msg := relayMsg{From: approvalResultFrom + "ix_continuation_identity", Tool: askAnswerTool,
		Text: "Retry the same tool now", At: at, Continuation: true}
	rewritten := msg
	rewritten.Text = consumedContinuationText("ix_continuation_identity")
	if relayMsgID("s1", msg) != relayMsgID("s1", rewritten) {
		t.Fatal("a continuation text rewrite changed its durable delivery identity")
	}

	ft := newFleetTest(t)
	preparedTestConversation(ft)
	working := proto.Session{MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude", Status: proto.StatusWorking, State: proto.StateWorking}
	ft.f.setSessions("mac", working)
	ft.d.setSessions("mac", []proto.Session{working})
	if err := ft.s.queueApprovalResult("mac", "s1", msg); err != nil {
		t.Fatal(err)
	}
	if err := ft.s.queueApprovalResult("mac", "s1", rewritten); err != nil {
		t.Fatal(err)
	}
	if got := ft.s.queuedFor("s1"); got != 1 {
		t.Fatalf("the original and rewritten continuation queued %d deliveries, want one", got)
	}
}

func TestQueuedLegacyContinuationKeepsItsSentIdentity(t *testing.T) {
	ft := newFleetTest(t)
	at := time.Date(2026, 11, 21, 2, 3, 4, 5, time.UTC)
	msg := relayMsg{From: approvalResultFrom + "ix_legacy_continuation", Tool: askAnswerTool,
		Text: "Retry the same tool now", At: at, Continuation: true}
	want := legacyRelayMsgID("s1", msg)
	if err := ft.s.store().PutSetting(t.Context(), relayKey, relayState{Queue: map[string][]relayMsg{"s1": {msg}}}); err != nil {
		t.Fatal(err)
	}
	ft.s.relays.mu.Lock()
	ft.s.relays.loaded = false
	loaded := ft.s.relayLocked(t.Context())
	queued := ft.s.relays.st.Queue["s1"]
	ft.s.relays.mu.Unlock()
	if !loaded || len(queued) != 1 {
		t.Fatalf("legacy continuation did not load: %v, %+v", loaded, queued)
	}
	if got := relayMsgID("s1", queued[0]); got != want {
		t.Fatalf("upgrade changed the already-sent continuation ID: got %q want %q", got, want)
	}
	rewritten := queued[0]
	rewritten.Text = consumedContinuationText("ix_legacy_continuation")
	if got := relayMsgID("s1", rewritten); got != want {
		t.Fatalf("rewriting the upgraded continuation changed its ID: got %q want %q", got, want)
	}
}

func TestLegacyQueuedContinuationDeduplicatesJournalRedelivery(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	at := time.Date(2026, 11, 21, 2, 3, 4, 5, time.UTC)
	msg := relayMsg{From: approvalResultFrom + "ix_legacy_redelivered_continuation", Tool: askAnswerTool,
		Text: "Retry the same tool now", At: at, Continuation: true}
	if err := ft.s.store().PutSetting(t.Context(), relayKey, relayState{Queue: map[string][]relayMsg{"s1": {msg}}}); err != nil {
		t.Fatal(err)
	}
	ft.s.relays.mu.Lock()
	ft.s.relays.loaded = false
	loaded := ft.s.relayLocked(t.Context())
	ft.s.relays.mu.Unlock()
	if !loaded {
		t.Fatal("the legacy continuation queue did not load")
	}
	working := proto.Session{MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude", Status: proto.StatusWorking, State: proto.StateWorking}
	ft.f.setSessions("mac", working)
	ft.d.setSessions("mac", []proto.Session{working})
	entry := approvalJournalEntry{ID: "ix_legacy_redelivered_continuation", MachineID: "mac", SessionID: "s1",
		Status: "approved", Result: msg.Text, Continuation: true, UpdatedAt: at}
	if err := ft.s.deliverApprovalResult(entry); err != nil {
		t.Fatal(err)
	}
	if got := ft.s.queuedFor("s1"); got != 1 {
		t.Fatalf("journal redelivery duplicated its pre-upgrade continuation: queue depth %d, want 1", got)
	}
}

func TestLegacyDifferentGrantDoesNotKeepConsumedContinuationRetry(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_legacy_replaced_continuation_grant")
	allow := true
	if err := ft.s.putDetachedGrant(context.Background(), ix, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	retry := ix
	retry.ID = "ix_legacy_replaced_continuation_grant_retry"
	got, ok, err := ft.s.consumeDetachedGrant(context.Background(), retry)
	if err != nil || !ok || got.Allow == nil || !*got.Allow {
		t.Fatalf("legacy first grant was not consumed: %+v ok=%v err=%v", got, ok, err)
	}
	entry := approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID,
		Status: "approved", UpdatedAt: time.Now().UTC(),
		Result: approvalResultText(ix.ID, "approved", detachedContinuationText(ix, proto.Resolution{Allow: &allow})), Continuation: true}
	replacement := ix
	replacement.CreatedAt = ix.CreatedAt.Add(time.Second)
	replacement.ToolInput = json.RawMessage(`{"command":"printf legacy replacement"}`)
	if err := ft.s.putDetachedGrant(context.Background(), replacement, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	if err := ft.s.deliverApprovalResult(entry); err != nil {
		t.Fatal(err)
	}
	result := waitFakeApprovalResult(t, ft.f, ix.ID)
	if !strings.Contains(result, "Do not retry") || strings.Contains(result, "Retry the same tool now") {
		t.Fatalf("a different grant kept the legacy retry instruction alive: %q", result)
	}
}

func TestDifferentGrantDoesNotKeepConsumedContinuationRetry(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_replaced_continuation_grant")
	allow := true
	if err := ft.s.putDetachedGrant(context.Background(), ix, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	retry := ix
	retry.ID = "ix_replaced_continuation_grant_retry"
	got, ok, err := ft.s.consumeDetachedGrant(context.Background(), retry)
	if err != nil || !ok || got.Allow == nil || !*got.Allow {
		t.Fatalf("first grant was not consumed: %+v ok=%v err=%v", got, ok, err)
	}
	replacement := ix
	replacement.CreatedAt = ix.CreatedAt.Add(time.Second)
	replacement.ToolInput = json.RawMessage(`{"command":"printf replacement"}`)
	if err := ft.s.putDetachedGrant(context.Background(), replacement, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	createdAt, fingerprint, err := approvalJournalOccurrence(ix)
	if err != nil {
		t.Fatal(err)
	}
	entry := approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID,
		CreatedAt: createdAt, Fingerprint: fingerprint, Status: "approved", UpdatedAt: time.Now().UTC(),
		Result: approvalResultText(ix.ID, "approved", detachedContinuationText(ix, proto.Resolution{Allow: &allow})), Continuation: true}
	if err := ft.s.deliverApprovalResult(entry); err != nil {
		t.Fatal(err)
	}
	result := waitFakeApprovalResult(t, ft.f, ix.ID)
	if !strings.Contains(result, "Do not retry") || strings.Contains(result, "Retry the same tool now") {
		t.Fatalf("a different grant kept the old retry instruction alive: %q", result)
	}
}

func TestQueuedDifferentGrantDoesNotKeepConsumedContinuationRetry(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_queued_replaced_continuation_grant")
	allow := true
	if err := ft.s.putDetachedGrant(context.Background(), ix, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	working := proto.Session{MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude", Status: proto.StatusWorking, State: proto.StateWorking}
	ft.f.setSessions("mac", working)
	ft.d.setSessions("mac", []proto.Session{working})
	createdAt, fingerprint, err := approvalJournalOccurrence(ix)
	if err != nil {
		t.Fatal(err)
	}
	entry := approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID,
		CreatedAt: createdAt, Fingerprint: fingerprint, Status: "approved", UpdatedAt: time.Now().UTC(),
		Result: approvalResultText(ix.ID, "approved", detachedContinuationText(ix, proto.Resolution{Allow: &allow})), Continuation: true}
	if err := ft.s.deliverApprovalResult(entry); err != nil {
		t.Fatal(err)
	}
	retry := ix
	retry.ID = "ix_queued_replaced_continuation_grant_retry"
	got, ok, err := ft.s.consumeDetachedGrant(context.Background(), retry)
	if err != nil || !ok || got.Allow == nil || !*got.Allow {
		t.Fatalf("queued first grant was not consumed: %+v ok=%v err=%v", got, ok, err)
	}
	replacement := ix
	replacement.CreatedAt = ix.CreatedAt.Add(time.Second)
	replacement.ToolInput = json.RawMessage(`{"command":"printf queued replacement"}`)
	if err := ft.s.putDetachedGrant(context.Background(), replacement, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	idle := working
	idle.Status, idle.State = proto.StatusIdle, proto.StateIdle
	ft.f.setSessions("mac", idle)
	ft.d.setSessions("mac", []proto.Session{idle})
	ft.s.drainRelays("mac", []proto.Session{idle})
	result := waitFakeApprovalResult(t, ft.f, ix.ID)
	if !strings.Contains(result, "Do not retry") || strings.Contains(result, "Retry the same tool now") {
		t.Fatalf("queued different grant kept the old retry instruction alive: %q", result)
	}
	waitFor(t, "the replaced continuation to leave the durable queue", func() bool { return ft.s.queuedFor("s1") == 0 })
}

func TestFailedRelayWriteKeepsContinuationForRestart(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	st := &relayWriteFailStore{fleetMem: ft.st}
	st.fail.Store(true)
	ft.s.setStore(func() Store { return st })
	ix := detachedTestCard("ix_relay_write_failure")
	working := proto.Session{MachineID: "mac", SessionID: "s1", Assistant: true, Agent: "claude", Status: proto.StatusWorking, State: proto.StateWorking}
	ft.f.setSessions("mac", working)
	ft.d.setSessions("mac", []proto.Session{working})
	putApprovalJournal(t, ft, approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID, Status: "approved",
		Result: approvalResultText(ix.ID, "approved", detachedContinuationText(ix, proto.Resolution{}))})
	ft.s.deliverApprovalResults()
	if got := storedApprovalJournal(t, ft); len(got) != 1 || got[0].Result == "" {
		t.Fatalf("a failed relay write discarded the only durable continuation: %+v", got)
	}

	st.fail.Store(false)
	idle := working
	idle.Status, idle.State = proto.StatusIdle, proto.StateIdle
	ft.f.setSessions("mac", idle)
	ft.d.setSessions("mac", []proto.Session{idle})
	restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
	restarted.deliverApprovalResults()
	if result := waitFakeApprovalResult(t, ft.f, ix.ID); !strings.Contains(result, "Retry the same tool now") {
		t.Fatalf("continuation after the relay writer recovered = %q", result)
	}
}

func TestAlwaysAllowClosesLateSiblingWithoutAnotherContinuation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		source := detachedTestCard("ix_always_source")
		source.AlwaysKeys = []string{"lark:sibling-test"}
		sourceDone := startDetachedTest(t, ft, source)
		sibling := detachedTestCard("ix_always_sibling")
		sibling.AlwaysKeys = []string{"lark:sibling-test"}
		siblingDone := make(chan detachedTestResult, 1)
		go func() {
			res, err := ft.s.nodeInteraction(context.Background(), sibling)
			siblingDone <- detachedTestResult{res, err}
		}()
		ft.waitPending(2)
		elapse(30 * time.Millisecond)
		awaitDetachedTest(t, sourceDone)
		awaitDetachedTest(t, siblingDone)

		allow := true
		if err := ft.s.resolve(source.ID, proto.Resolution{Allow: &allow, Always: true}); err != nil {
			t.Fatal(err)
		}
		if result := waitApprovalResult(t, ft, sibling.ID); !strings.Contains(result, "newer Always allow") || !strings.Contains(result, "no automatic retry") {
			t.Fatalf("covered sibling result = %q", result)
		}
		if got := len(ft.s.pendingInteractions()); got != 0 {
			t.Fatalf("the covered sibling stayed actionable; pending = %d", got)
		}
		ft.s.detached.mu.Lock()
		_, siblingGrant := ft.s.detached.grants[sibling.ID]
		_, sourceGrant := ft.s.detached.grants[source.ID]
		ft.s.detached.mu.Unlock()
		if siblingGrant || !sourceGrant {
			t.Fatalf("grants after Always: sibling=%v source=%v, want no sibling grant and the source's one retry", siblingGrant, sourceGrant)
		}
	})
}

// #12918: when the initial wait ends unanswered, the hook returns WaitingText into the transcript as an error
// tool_result with the original tool_use_id. Transcript reconciliation must not mistake that release message (or later
// assistant turns) for a terminal answer and dismiss the retained 24-hour approval card.
func TestDetachedCommandApprovalSurvivesTranscriptWaitingResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ix := detachedTestCard("ix_detached_wait")
		ix.ToolUseID = "call_detached_wait"
		done := startDetachedTest(t, ft, ix)
		elapse(30 * time.Millisecond)
		first := awaitDetachedTest(t, done)
		if first.err != nil || !first.res.Pending || !first.res.Dismiss || !strings.Contains(first.res.Message, approvalops.WaitingText) {
			t.Fatalf("initial hook result = %+v, %v", first.res, first.err)
		}

		var itemsCalls atomic.Int32
		ft.d.mu.Lock()
		ft.d.machines["mac"].call = func(method string, _ json.RawMessage) (any, error) {
			if method == "session.items" {
				itemsCalls.Add(1)
				return map[string]any{
					"items": []proto.Item{
						{ID: "call#0", Seq: 10, TS: ix.CreatedAt, Role: "assistant", Kind: proto.KindToolUse,
							ToolName: "Bash", ToolUseID: ix.ToolUseID, ToolInput: ix.ToolInput},
						{ID: "result#0", Seq: 11, TS: time.Now().UTC(), Role: "user", Kind: proto.KindToolResult,
							ToolUseID: ix.ToolUseID, IsError: true, Text: "PreToolUse:Bash hook error: " + first.res.Message},
						{ID: "text#0", Seq: 12, TS: time.Now().Add(time.Millisecond).UTC(), Role: "assistant",
							Kind: proto.KindText, Text: "Waiting for your confirmation in Persona."},
					},
					"has_more": false,
				}, nil
			}
			return nil, errors.New("unexpected node call " + method)
		}
		ft.d.mu.Unlock()

		ft.s.syncPlanInteractions("mac")
		synctest.Wait()
		if itemsCalls.Load() != 0 {
			t.Fatalf("prepared approval triggered %d transcript reconciliation reads, want 0", itemsCalls.Load())
		}
		if pending := ft.s.pendingInteractions(); len(pending) != 1 || pending[0].ID != ix.ID {
			t.Fatalf("pending cards after transcript revs = %+v, want [%s]", pending, ix.ID)
		}
		if status, res, err := ft.st.InteractionOutcome(context.Background(), ix.ID); err != nil || status != "pending" || res != nil {
			t.Fatalf("stored interaction status = %q (%+v, %v), want pending", status, res, err)
		}

		for _, dev := range []string{DeviceAll, "mac"} {
			raw, handled, err := ft.s.localFleetDeviceCall(context.Background(), DeviceCall{Device: dev, Method: DeviceInteractionsList})
			if err != nil || !handled {
				t.Fatalf("localFleetDeviceCall(%q): handled=%v err=%v", dev, handled, err)
			}
			var listed struct {
				Interactions []proto.Interaction `json:"interactions"`
			}
			if err := json.Unmarshal(raw, &listed); err != nil || len(listed.Interactions) != 1 || listed.Interactions[0].ID != ix.ID {
				t.Fatalf("localFleetDeviceCall(%q) = %s (%v), want %s", dev, raw, err, ix.ID)
			}
		}

		allow := true
		if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &allow}); err != nil {
			t.Fatalf("late owner Allow after transcript revs failed: %v", err)
		}
		if result := waitApprovalResult(t, ft, ix.ID); !strings.Contains(result, "approved") {
			t.Fatalf("continuation result = %q", result)
		}
		retry := ix
		retry.ID = "ix_detached_wait_retry"
		got, err := ft.s.nodeInteraction(context.Background(), retry)
		if err != nil || got.Allow == nil || !*got.Allow || got.ApprovalID != ix.ID {
			t.Fatalf("approved retry = %+v, %v", got, err)
		}
	})
}

// #12918: if the manager restarts while a detachable hook is still blocked in its initial wait, restoreInteractions
// rebuilds the pending card with p.prepared == nil. When the still-live hook reconnects and replays interaction.create,
// waitInteractionReady must adopt its prepared coordinator so keepWaiting and late Allow still mint the grant.
func TestDetachedCommandApprovalRestoredReconnectAdoptsPreparedCoordinator(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		shortDetachedTimers(t)
		ft := newFleetTest(t)
		preparedTestConversation(ft)
		ix := detachedTestCard("ix_restored_reconnect")
		ctx, cancel := context.WithCancel(context.Background())
		first := make(chan detachedTestResult, 1)
		go func() {
			res, err := ft.s.nodeInteraction(ctx, ix)
			first <- detachedTestResult{res: res, err: err}
		}()
		ft.waitPending(1)
		cancel()
		awaitDetachedTest(t, first)

		restarted := New(Options{Hub: ft.f, Store: func() Store { return ft.st }, Logf: testLogf(t), Devices: ft.d})
		restarted.restoreInteractions(context.Background())
		replayed := make(chan detachedTestResult, 1)
		go func() {
			res, err := restarted.nodeInteraction(context.Background(), ix)
			replayed <- detachedTestResult{res: res, err: err}
		}()
		elapse(30 * time.Millisecond)
		got := awaitDetachedTest(t, replayed)
		if got.err != nil || !got.res.Pending || !got.res.Dismiss || got.res.ApprovalID != ix.ID {
			t.Fatalf("reconnected restored hook = %+v, %v, want non-final waiting release", got.res, got.err)
		}

		allow := true
		if err := restarted.resolve(ix.ID, proto.Resolution{Allow: &allow}); err != nil {
			t.Fatalf("late Allow on restored detached card failed: %v", err)
		}
		if result := waitFakeApprovalResult(t, ft.f, ix.ID); !strings.Contains(result, "approved") {
			t.Fatalf("continuation result = %q", result)
		}
		retry := ix
		retry.ID = "ix_restored_reconnect_retry"
		consumed, err := restarted.nodeInteraction(context.Background(), retry)
		if err != nil || consumed.Allow == nil || !*consumed.Allow || consumed.ApprovalID != ix.ID {
			t.Fatalf("retry after restored reconnect = %+v, %v", consumed, err)
		}
	})
}

func TestDetachedCommandClarifyPreservesDecisionDeadlineAfterAnswer(t *testing.T) {
	for _, tc := range []struct {
		name        string
		zeroExpires bool
	}{
		{name: "explicit_expires_at", zeroExpires: false},
		{name: "default_ttl_expires_at", zeroExpires: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ft := newFleetTest(t)
				preparedTestConversation(ft)
				ix := detachedTestCard("ix_detached_clarify_" + tc.name)
				if tc.zeroExpires {
					ix.ExpiresAt = time.Time{}
				}
				done := startDetachedTest(t, ft, ix)
				elapse(10 * time.Millisecond)

				if err := ft.s.clarify(context.Background(), ix.ID, "Why run this command?", "owner-q-"+tc.name, nil); err != nil {
					t.Fatalf("clarify: %v", err)
				}
				first := awaitDetachedTest(t, done)
				if first.err != nil || !first.res.Pending || !first.res.Dismiss || first.res.ApprovalID != ix.ID {
					t.Fatalf("initial hook result after clarify = %+v, %v", first.res, first.err)
				}

				elapse(200 * time.Millisecond)
				ref := SessionRef{MachineID: "mac", SessionID: "s1"}
				ft.s.approvalAnswerTurnStarted(ref)
				ft.s.approvalAnswerTurnEnded(ref)

				// Remaining decision time must be preserved after the answer (minutes/hour, never 0ms).
				elapse(time.Minute)
				pending := ft.s.pendingInteractions()
				if len(pending) != 1 || pending[0].ID != ix.ID {
					t.Fatalf("detached card expired right after clarification answer: pending=%+v", pending)
				}
				if time.Until(pending[0].ExpiresAt) <= 0 {
					t.Fatalf("resumed ExpiresAt is not in the future: %v", pending[0].ExpiresAt)
				}

				allow := true
				if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &allow}); err != nil {
					t.Fatalf("Allow after clarification answer failed: %v", err)
				}
				if result := waitApprovalResult(t, ft, ix.ID); !strings.Contains(result, "approved") {
					t.Fatalf("continuation result = %q", result)
				}
				retry := detachedTestCard("ix_detached_clarify_retry_" + tc.name)
				got, err := ft.s.nodeInteraction(context.Background(), retry)
				if err != nil || got.Allow == nil || !*got.Allow || got.ApprovalID != ix.ID {
					t.Fatalf("retry after clarified Allow = %+v, %v", got, err)
				}
			})
		})
	}
}
