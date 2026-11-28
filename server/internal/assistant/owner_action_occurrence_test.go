package assistant

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestOwnerActionRegistrationJoinNotifies(t *testing.T) {
	s, _, _, book := ownerReplyTestService(t)
	batch := createOwnerTestBatch(t, book, `{"body":"registration"}`)
	ix := ownerReplyBatchIX(batch)
	putOwnerReplyPending(s, ix)
	ready := make(chan error, 1)
	returned := make(chan error, 1)
	go func() {
		_, err := s.waitInteractionReadyResult(context.Background(), ix, nil, ready)
		returned <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(150 * time.Millisecond):
		_ = s.resolve(ix.ID, proto.Resolution{Dismiss: true})
		select {
		case <-returned:
		case <-time.After(time.Second):
		}
		t.Fatal("an existing-pending registration never released its ready waiter")
	}
	_ = s.resolve(ix.ID, proto.Resolution{Dismiss: true})
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("registered waiter did not return after dismissal")
	}
}

type ownerActionRecordErrorStore struct {
	*ownerActionTestStore
	err error
}

func (st *ownerActionRecordErrorStore) InteractionRecord(context.Context, string) (*db.InteractionRecord, error) {
	return nil, st.err
}

func TestOwnerActionRegistrationErrorNotifies(t *testing.T) {
	f, _ := feishuHub("ou_owner", time.Unix(100, 0))
	on := true
	f.settings = &Settings{MachineID: "server", Main: "m1", OwnerReplyApproval: &on, PersistentOwnerActionCards: &on}
	st := &ownerActionRecordErrorStore{ownerActionTestStore: &ownerActionTestStore{}, err: errors.New("transient interaction read")}
	s := newServiceOn(t, f, st)
	book, ok := newOwnerActionBook(st)
	if !ok {
		t.Fatal("owner-action test store must support CAS")
	}
	batch := createOwnerTestBatch(t, book, `{"body":"registration error"}`)
	ix := ownerReplyBatchIX(batch)
	ready := make(chan error, 1)
	returned := make(chan error, 1)
	go func() {
		_, err := s.waitInteractionReadyResult(context.Background(), ix, nil, ready)
		returned <- err
	}()
	select {
	case err := <-ready:
		if err == nil {
			t.Fatal("registration reported success after the store read failed")
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("an early registration error never released its ready waiter")
	}
	select {
	case err := <-returned:
		if err == nil {
			t.Fatal("registration returned nil after the store read failed")
		}
	case <-time.After(time.Second):
		t.Fatal("registration error did not return")
	}
}

func TestOwnerActionRegistrationDoesNotReportSuccessBeforePendingEntry(t *testing.T) {
	s, _, _, _ := ownerReplyTestService(t)
	ix := proto.Interaction{ID: "ix_elicitation_before_registration", MachineID: "machine-1", SessionID: "session-1",
		Kind: proto.IxElicitation, CreatedAt: time.Now().UTC()}
	ready := make(chan error, 1)
	returned := make(chan error, 1)
	go func() {
		_, err := s.waitInteractionReadyResult(context.Background(), ix, nil, ready)
		returned <- err
	}()
	select {
	case err := <-ready:
		if err == nil {
			t.Fatal("an early nil return was reported as a successful registration")
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatal("an early nil return never released its ready waiter")
	}
	if err := <-returned; err != nil {
		t.Fatal(err)
	}
}

func TestOwnerActionPendingDurableOriginDoesNotGetApprovalReceipt(t *testing.T) {
	s, _, _, book := ownerReplyTestService(t)
	batch := createOwnerTestBatch(t, book, `{"body":"still pending"}`)
	ix := ownerReplyBatchIX(batch)
	out, err := s.SendParams(context.Background(), SendParams{MachineID: batch.MachineID, SessionID: batch.SessionID, Human: true,
		Params: map[string]any{"text": "发", "client_msg_id": "pending-restart-reply", "approval_reply_to": ix.ID}})
	if err == nil {
		t.Fatalf("an unanswered durable proposal was acknowledged: %+v", out)
	}
	if out[ownerApprovalHandledParam] != nil {
		t.Fatalf("pending proposal returned an approval receipt: %+v", out)
	}
	current, _, readErr := book.batch(context.Background(), batch.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if current.Items[0].State != ownerActionPending {
		t.Fatalf("unanswered proposal changed to %s", current.Items[0].State)
	}
}

func TestOwnerActionResolveRejectsStaleOccurrence(t *testing.T) {
	s, _, _, book := ownerReplyTestService(t)
	batch := createOwnerTestBatch(t, book, `{"body":"occurrence"}`)
	ix := ownerReplyBatchIX(batch)
	putOwnerReplyPending(s, ix)
	stale := ix.CreatedAt.Add(-time.Nanosecond)
	allow := true
	if err := s.resolveApprovedAt(context.Background(), ix.ID, proto.Resolution{Allow: &allow}, nil, &stale); err == nil {
		t.Fatal("a stale occurrence approved the live proposal")
	}
	current, _, err := book.batch(context.Background(), batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Items[0].State != ownerActionPending {
		t.Fatalf("stale occurrence changed item state to %s", current.Items[0].State)
	}
}

func TestOwnerActionReplyEnvelopeRejectsStaleOccurrence(t *testing.T) {
	s, _, _, book := ownerReplyTestService(t)
	batch := createOwnerTestBatch(t, book, `{"body":"envelope occurrence"}`)
	ix := ownerReplyBatchIX(batch)
	putOwnerReplyPending(s, ix)
	stale := ix.CreatedAt.Add(-time.Nanosecond).Format(time.RFC3339Nano)
	out, err := s.SendParams(context.Background(), SendParams{MachineID: batch.MachineID, SessionID: batch.SessionID, Human: true,
		Params: map[string]any{"text": "发", "client_msg_id": "stale-envelope", "approval_reply_to": ix.ID, "approval_reply_created_at": stale}})
	if err == nil {
		t.Fatalf("stale exact-reply envelope was acknowledged: %+v", out)
	}
	if out[ownerApprovalHandledParam] != nil {
		t.Fatalf("stale exact-reply envelope returned a receipt: %+v", out)
	}
	current, _, readErr := book.batch(context.Background(), batch.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if current.Items[0].State != ownerActionPending {
		t.Fatalf("stale exact-reply envelope changed item state to %s", current.Items[0].State)
	}
}

func TestOwnerActionFeishuRejectsStaleCardOccurrence(t *testing.T) {
	needsLarkLayer(t)
	s, f, _, book := ownerReplyTestService(t)
	batch := createOwnerTestBatch(t, book, `{"body":"feishu occurrence"}`)
	ix := ownerReplyBatchIX(batch)
	keepOwnerReplyCard(s, ix, "om_stale")
	updateChatCard(s, ix.ID, func(card *botCard) {
		card.Ix.CreatedAt = ix.CreatedAt.Add(-time.Nanosecond)
	})
	calls := 0
	answerOwnerProof(f, "oa_reply", ix.ID, &calls, nil)
	if !s.botApproveOwnerAction(context.Background(), ownerReplyEvent("om_stale"), "发", "oa_reply") {
		t.Fatal("stale exact reply was not consumed as a handled message")
	}
	if calls != 0 {
		t.Fatalf("stale card consumed %d hub owner-action proofs, want zero", calls)
	}
	current, _, err := book.batch(context.Background(), batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Items[0].State != ownerActionPending {
		t.Fatalf("stale Feishu occurrence changed item state to %s", current.Items[0].State)
	}
}

func TestOwnerActionDecisionClaimsBeforeExpiry(t *testing.T) {
	s, _, st, book := ownerReplyTestService(t)
	batch := createOwnerTestBatch(t, book, `{"body":"expiry race"}`)
	ix := ownerReplyBatchIX(batch)
	ix.NoExpiry = false
	ix.ExpiresAt = time.Now().Add(60 * time.Millisecond)
	putOwnerReplyPending(s, ix)
	s.fleet.mu.Lock()
	p := s.fleet.interactions[ix.ID]
	s.fleet.mu.Unlock()
	waited := make(chan proto.Resolution, 1)
	waitErr := make(chan error, 1)
	go func() {
		r, err := s.await(context.Background(), p)
		waited <- r
		waitErr <- err
	}()
	if !ownerActionPoll(t, time.Second, func() bool {
		s.fleet.mu.Lock()
		defer s.fleet.mu.Unlock()
		return p.resolved != nil
	}) {
		t.Fatal("owner-action waiter did not register")
	}
	st.casEntered = make(chan struct{}, 1)
	st.casRelease = make(chan struct{})
	resolved := make(chan error, 1)
	allow := true
	go func() {
		resolved <- s.resolveOwnerProposalAt(context.Background(), ix, proto.Resolution{Allow: &allow},
			ownerActionCardProof(ix, "expiry-test"), nil)
	}()
	select {
	case <-st.casEntered:
	case <-time.After(time.Second):
		close(st.casRelease)
		t.Fatal("owner decision did not reach its durable compare-and-swap")
	}
	var early *proto.Resolution
	select {
	case r := <-waited:
		early = &r
	case <-time.After(100 * time.Millisecond):
	}
	close(st.casRelease)
	select {
	case err := <-resolved:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("owner decision did not finish")
	}
	var r proto.Resolution
	if early != nil {
		r = *early
	} else {
		select {
		case r = <-waited:
		case <-time.After(time.Second):
			t.Fatal("the claimed owner decision did not reach its original waiter")
		}
	}
	if err := <-waitErr; err != nil {
		t.Fatal(err)
	}
	if r.Allow == nil || !*r.Allow || r.Dismiss {
		t.Fatalf("expiry beat an owner decision already in flight: %+v", r)
	}
}

func TestOwnerActionRejectedDecisionReleasesClaimForWaiter(t *testing.T) {
	s, _, _, book := ownerReplyTestService(t)
	batch := createOwnerTestBatch(t, book, `{"body":"rejected decision"}`)
	ix := ownerReplyBatchIX(batch)
	ix.NoExpiry = false
	ix.ExpiresAt = time.Now().Add(40 * time.Millisecond)
	putOwnerReplyPending(s, ix)
	s.fleet.mu.Lock()
	p := s.fleet.interactions[ix.ID]
	s.fleet.mu.Unlock()
	waited := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.await(context.Background(), p)
		waited <- r
	}()
	allow := true
	if err := s.resolveOwnerProposalAt(context.Background(), ix, proto.Resolution{Allow: &allow}, ownerReplyProof{}, nil); !errors.Is(err, errOwnerActionProof) {
		t.Fatalf("invalid owner proof returned %v", err)
	}
	select {
	case r := <-waited:
		if !r.Dismiss {
			t.Fatalf("rejected decision produced an answer: %+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("rejected decision left the original waiter claimed forever")
	}
}

func TestOwnerActionAmbiguousCommitErrorReconcilesGrant(t *testing.T) {
	s, _, st, book := ownerReplyTestService(t)
	batch := createOwnerTestBatch(t, book, `{"body":"commit outcome unknown"}`)
	ix := ownerReplyBatchIX(batch)
	ix.NoExpiry = false
	ix.ExpiresAt = time.Now().Add(80 * time.Millisecond)
	putOwnerReplyPending(s, ix)
	s.fleet.mu.Lock()
	p := s.fleet.interactions[ix.ID]
	s.fleet.mu.Unlock()
	waited := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.await(context.Background(), p)
		waited <- r
	}()
	st.casAfterCommitErr = errors.New("connection lost after the durable write")
	allow := true
	resolveErr := s.resolveOwnerProposalAt(context.Background(), ix, proto.Resolution{Allow: &allow},
		ownerActionCardProof(ix, "ambiguous-commit-test"), nil)
	var r proto.Resolution
	select {
	case r = <-waited:
	case <-time.After(time.Second):
		t.Fatal("uncertain durable decision left the original waiter blocked")
	}
	if r.Allow == nil || !*r.Allow || r.Dismiss {
		t.Fatalf("a committed grant was rolled back to expiry after an ambiguous storage error: %+v (resolve error %v)", r, resolveErr)
	}
	if resolveErr != nil {
		t.Fatalf("the reconciled durable decision was still reported as failed: %v", resolveErr)
	}
}

func TestOwnerActionReconciliationContinuesPastInitialRetryBudget(t *testing.T) {
	s, _, st, book := ownerReplyTestService(t)
	batch := createOwnerTestBatch(t, book, `{"body":"recovery after retry budget"}`)
	ix := ownerReplyBatchIX(batch)
	ix.NoExpiry = false
	ix.ExpiresAt = time.Now().Add(60 * time.Millisecond)
	putOwnerReplyPending(s, ix)
	s.fleet.mu.Lock()
	p := s.fleet.interactions[ix.ID]
	s.fleet.mu.Unlock()
	waited := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.await(context.Background(), p)
		waited <- r
	}()
	// Two immediate attempts plus eight background attempts used to exhaust the
	// retry budget. Recovery must still deliver the decision on a later attempt.
	st.casFailuresRemaining = 11
	allow := true
	if err := s.resolveOwnerProposalAt(context.Background(), ix, proto.Resolution{Allow: &allow},
		ownerActionCardProof(ix, "extended-reconciliation-test"), nil); err == nil {
		t.Fatal("the injected storage outage was not surfaced to the caller")
	}
	select {
	case r := <-waited:
		if r.Allow == nil || !*r.Allow || r.Dismiss {
			t.Fatalf("recovered decision produced the wrong answer: %+v", r)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("proposal stayed claimed after the database recovered beyond the old retry budget")
	}
}

func TestOwnerActionShutdownWakesPersistentWaiter(t *testing.T) {
	s, _, _, book := ownerReplyTestService(t)
	batch := createOwnerTestBatch(t, book, `{"body":"persistent shutdown"}`)
	ix := ownerReplyBatchIX(batch)
	putOwnerReplyPending(s, ix)
	runCtx, cancel := context.WithCancel(context.Background())
	s.running.Store(&runCtx)
	allow := true
	p, err := s.claimOwnerProposal(ix, proto.Resolution{Allow: &allow}, nil)
	if err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		r   proto.Resolution
		err error
	}
	waited := make(chan outcome, 1)
	go func() {
		r, err := s.await(context.Background(), p)
		waited <- outcome{r: r, err: err}
	}()
	// The early persistenceErr check makes either waiter scheduling safe.
	cancel()
	s.failOwnerProposalClaim(p, runCtx.Err())
	select {
	case got := <-waited:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("persistent shutdown returned resolution %+v and error %v", got.r, got.err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("persistent proposal waiter stayed blocked after service shutdown")
	}
}

func TestOwnerActionResolutionUsesTheFirstDurableAnswer(t *testing.T) {
	s, _, _, book := ownerReplyTestService(t)
	batch := createOwnerTestBatch(t, book, `{"body":"first durable answer"}`)
	ix := ownerReplyBatchIX(batch)
	putOwnerReplyPending(s, ix)
	denyProof := ownerActionCardProof(ix, "first-denial")
	if _, err := book.decide(context.Background(), denyProof, false); err != nil {
		t.Fatal(err)
	}
	s.fleet.mu.Lock()
	p := s.fleet.interactions[ix.ID]
	s.fleet.mu.Unlock()
	waited := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.await(context.Background(), p)
		waited <- r
	}()
	allow := true
	allowProof := ownerActionCardProof(ix, "later-allow")
	if err := s.resolveOwnerProposalAt(context.Background(), ix, proto.Resolution{Allow: &allow}, allowProof, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-waited:
		if r.Allow == nil || *r.Allow || r.Dismiss {
			t.Fatalf("later Allow replaced the first durable Deny: %+v", r)
		}
	case <-time.After(time.Second):
		t.Fatal("first durable answer did not reach the original waiter")
	}
}

func TestOwnerActionSyncDoesNotResolveReplacement(t *testing.T) {
	s, _, _, book := ownerReplyTestService(t)
	batch := createOwnerTestBatch(t, book, `{"body":"projection"}`)
	allow := true
	if _, err := book.decide(context.Background(), ownerReplyProof{AuthenticatedOwner: true, DirectParent: true, ExactPhrase: true,
		EventID: "projection-decision", BatchID: batch.ID}, allow); err != nil {
		t.Fatal(err)
	}
	replacement := ownerReplyBatchIX(batch)
	replacement.CreatedAt = replacement.CreatedAt.Add(time.Second)
	putOwnerReplyPending(s, replacement)
	if err := s.syncOwnerActionBatch(context.Background(), batch.ID); err != nil {
		t.Fatal(err)
	}
	if _, pending := s.pendingIx(replacement.ID); !pending {
		t.Fatal("delayed batch projection withdrew a newer same-ID replacement")
	}
}
