package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ownerActionTestStore has the same revision-checked replacement semantics as
// db.CompareSetting. The mutex covers the read/compare/write sequence, so two
// independent books exercise cross-manager arbitration rather than a book lock.
type ownerActionTestStore struct {
	noPlans
	mu                   sync.Mutex
	values               map[string][]byte
	casErr               error
	casAfterCommitErr    error
	casFailuresRemaining int
	casCall              atomic.Int32
	casEntered           chan struct{}
	casRelease           chan struct{}
}

func (st *ownerActionTestStore) Setting(_ context.Context, key string, out any) (bool, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	raw, ok := st.values[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, out)
}

func (st *ownerActionTestStore) PutSetting(_ context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.values == nil {
		st.values = map[string][]byte{}
	}
	st.values[key] = raw
	return nil
}

func (st *ownerActionTestStore) CompareSetting(_ context.Context, key string, revision int64, value any) (bool, error) {
	st.casCall.Add(1)
	if st.casEntered != nil {
		st.casEntered <- struct{}{}
	}
	if st.casRelease != nil {
		<-st.casRelease
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.casFailuresRemaining > 0 {
		st.casFailuresRemaining--
		return false, errors.New("transient compare-and-swap failure")
	}
	if st.casErr != nil {
		return false, st.casErr
	}
	var current struct {
		Revision int64 `json:"revision"`
	}
	if len(st.values[key]) > 0 {
		if err := json.Unmarshal(st.values[key], &current); err != nil {
			return false, err
		}
	}
	if current.Revision != revision {
		return false, nil
	}
	if st.values == nil {
		st.values = map[string][]byte{}
	}
	st.values[key] = raw
	if st.casAfterCommitErr != nil {
		return false, st.casAfterCommitErr
	}
	return true, nil
}

func newOwnerActionTestBook(t *testing.T) (*ownerActionBook, *ownerActionTestStore) {
	t.Helper()
	st := &ownerActionTestStore{}
	book, ok := newOwnerActionBook(st)
	if !ok {
		t.Fatal("a revision-aware store must support owner-action grants")
	}
	return book, st
}

func validOwnerProof(batch, item string) ownerReplyProof {
	return ownerReplyProof{
		AuthenticatedOwner: true,
		DirectParent:       true,
		ExactPhrase:        true,
		EventID:            "event-1",
		BatchID:            batch,
		ItemID:             item,
	}
}

func createOwnerTestBatch(t *testing.T, book *ownerActionBook, inputs ...string) ownerActionBatch {
	t.Helper()
	drafts := make([]ownerActionDraft, len(inputs))
	for i, input := range inputs {
		drafts[i] = ownerActionDraft{Tool: "mail_send", Input: json.RawMessage(input)}
	}
	batch, err := book.create(context.Background(), "machine-1", "session-1", drafts)
	if err != nil {
		t.Fatalf("create batch: %v", err)
	}
	return batch
}

func TestOwnerActionCanonicalPayload(t *testing.T) {
	a, err := canonicalOwnerAction(json.RawMessage(` {"subject":"hello","to":[{"email":"a@example.test"}],"n":1} `))
	if err != nil {
		t.Fatal(err)
	}
	b, err := canonicalOwnerAction(json.RawMessage(`{"n":1,"to":[{"email":"a@example.test"}],"subject":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("equivalent objects must have one canonical form:\n%s\n%s", a, b)
	}
	changed, err := canonicalOwnerAction(json.RawMessage(`{"n":1,"to":[{"email":"b@example.test"}],"subject":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) == string(changed) {
		t.Fatal("a changed recipient must change the canonical payload")
	}
	if _, err := canonicalOwnerAction(json.RawMessage(`{"to":"a@example.test","to":"b@example.test"}`)); err == nil {
		t.Fatal("duplicate object keys are ambiguous and must be rejected")
	}
}

func TestOwnerActionBatchApproveConsumesEachItemOnce(t *testing.T) {
	book, _ := newOwnerActionTestBook(t)
	batch := createOwnerTestBatch(t, book,
		`{"to":[{"email":"a@example.test"}],"subject":"one","body":"first"}`,
		`{"to":[{"email":"b@example.test"}],"subject":"two","body":"second"}`,
		`{"to":[{"email":"c@example.test"}],"subject":"three","body":"third"}`,
	)
	decision, err := book.decide(context.Background(), validOwnerProof(batch.ID, ""), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.ItemIDs) != 3 {
		t.Fatalf("batch approval granted %d items, want 3", len(decision.ItemIDs))
	}
	// A duplicate reply is idempotent and creates no additional grant.
	decision, err = book.decide(context.Background(), validOwnerProof(batch.ID, ""), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.ItemIDs) != 0 {
		t.Fatalf("duplicate reply newly granted %v", decision.ItemIDs)
	}
	for _, item := range batch.Items {
		granted, err := book.consume(context.Background(), batch.MachineID, batch.SessionID, item.Tool, json.RawMessage(item.Canonical))
		if err != nil || !granted {
			t.Fatalf("first exact execution for %s: granted=%v err=%v", item.ID, granted, err)
		}
		granted, err = book.consume(context.Background(), batch.MachineID, batch.SessionID, item.Tool, json.RawMessage(item.Canonical))
		if err != nil {
			t.Fatal(err)
		}
		if granted {
			t.Fatalf("item %s executed more than once", item.ID)
		}
	}
}

func TestOwnerActionIdenticalItemsAreIndependentOneShotGrants(t *testing.T) {
	book, _ := newOwnerActionTestBook(t)
	const identical = `{"to":[{"email":"a@example.test"}],"subject":"same","body":"same"}`
	batch := createOwnerTestBatch(t, book, identical, identical)
	if batch.Items[0].ID == batch.Items[1].ID {
		t.Fatal("intentional duplicate operations collapsed into one proposal")
	}
	if _, err := book.decide(context.Background(), validOwnerProof(batch.ID, ""), true); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		granted, err := book.consume(context.Background(), batch.MachineID, batch.SessionID, "mail_send", json.RawMessage(identical))
		if err != nil || !granted {
			t.Fatalf("identical execution %d: granted=%v err=%v", i+1, granted, err)
		}
	}
	granted, err := book.consume(context.Background(), batch.MachineID, batch.SessionID, "mail_send", json.RawMessage(identical))
	if err != nil || granted {
		t.Fatalf("third identical execution: granted=%v err=%v", granted, err)
	}
}

func TestOwnerActionItemReplyApprovesOnlyThatItem(t *testing.T) {
	book, _ := newOwnerActionTestBook(t)
	batch := createOwnerTestBatch(t, book, `{"body":"one"}`, `{"body":"two"}`)
	proof := validOwnerProof(batch.ID, batch.Items[0].ID)
	if _, err := book.decide(context.Background(), proof, true); err != nil {
		t.Fatal(err)
	}
	granted, err := book.consume(context.Background(), batch.MachineID, batch.SessionID, "mail_send", json.RawMessage(batch.Items[1].Canonical))
	if err != nil {
		t.Fatal(err)
	}
	if granted {
		t.Fatal("approving one item must not approve its sibling")
	}
}

func TestOwnerActionDecisionProofFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ownerReplyProof)
	}{
		{"unauthenticated", func(p *ownerReplyProof) { p.AuthenticatedOwner = false }},
		{"wrong location", func(p *ownerReplyProof) { p.DirectParent = false }},
		{"relayed or inexact words", func(p *ownerReplyProof) { p.ExactPhrase = false }},
		{"missing event", func(p *ownerReplyProof) { p.EventID = "" }},
		{"another item", func(p *ownerReplyProof) { p.ItemID = "missing-item" }},
		{"another batch", func(p *ownerReplyProof) { p.BatchID = "missing-batch" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			book, _ := newOwnerActionTestBook(t)
			batch := createOwnerTestBatch(t, book, `{"body":"original"}`)
			proof := validOwnerProof(batch.ID, "")
			tt.mutate(&proof)
			if _, err := book.decide(context.Background(), proof, true); err == nil {
				t.Fatal("invalid approval proof must be rejected")
			}
			granted, err := book.consume(context.Background(), batch.MachineID, batch.SessionID, "mail_send", json.RawMessage(batch.Items[0].Canonical))
			if err != nil {
				t.Fatal(err)
			}
			if granted {
				t.Fatal("invalid approval proof granted execution")
			}
		})
	}
}

func TestOwnerActionEditedPayloadNeedsFreshApproval(t *testing.T) {
	book, _ := newOwnerActionTestBook(t)
	batch := createOwnerTestBatch(t, book, `{"to":[{"email":"a@example.test"}],"body":"original"}`)
	if _, err := book.decide(context.Background(), validOwnerProof(batch.ID, ""), true); err != nil {
		t.Fatal(err)
	}
	for _, edited := range []string{
		`{"to":[{"email":"a@example.test"}],"body":"edited"}`,
		`{"to":[{"email":"b@example.test"}],"body":"original"}`,
	} {
		granted, err := book.consume(context.Background(), batch.MachineID, batch.SessionID, "mail_send", json.RawMessage(edited))
		if err != nil {
			t.Fatal(err)
		}
		if granted {
			t.Fatalf("edited payload %s inherited the grant", edited)
		}
	}
}

func TestOwnerActionCanonicalBytesRemainAuthorityWhenFingerprintMatches(t *testing.T) {
	book, _ := newOwnerActionTestBook(t)
	const original = `{"to":"a@example.test","body":"original"}`
	const edited = `{"to":"a@example.test","body":"edited"}`
	batch := createOwnerTestBatch(t, book, original)
	if _, err := book.decide(context.Background(), validOwnerProof(batch.ID, ""), true); err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalOwnerAction(json.RawMessage(edited))
	if err != nil {
		t.Fatal(err)
	}
	stored, found, err := book.batch(context.Background(), batch.ID)
	if err != nil || !found || len(stored.Items) != 1 {
		t.Fatalf("read granted batch: found=%v batch=%+v err=%v", found, stored, err)
	}
	if stored.Items[0].State != ownerActionGranted {
		t.Fatalf("stored item state = %s, want granted", stored.Items[0].State)
	}
	// Simulate a digest collision at the storage boundary. The fingerprint is
	// only a lookup key; different complete bytes must still fail closed.
	stored.Items[0].Fingerprint = ownerActionDigest(canonical)
	if err := book.putForTest(context.Background(), stored); err != nil {
		t.Fatal(err)
	}
	granted, err := book.consume(context.Background(), batch.MachineID, batch.SessionID, "mail_send", json.RawMessage(edited))
	if err != nil || granted {
		t.Fatalf("same digest authorized different canonical bytes: granted=%v err=%v", granted, err)
	}
}

func TestOwnerActionConcurrentConsumersAndRestart(t *testing.T) {
	book, st := newOwnerActionTestBook(t)
	batch := createOwnerTestBatch(t, book, `{"body":"original"}`)
	if _, err := book.decide(context.Background(), validOwnerProof(batch.ID, ""), true); err != nil {
		t.Fatal(err)
	}
	// A new book has no process-local state, as after a manager restart.
	restarted, ok := newOwnerActionBook(st)
	if !ok {
		t.Fatal("restart lost CAS support")
	}
	const callers = 24
	var successes atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			granted, err := restarted.consume(context.Background(), batch.MachineID, batch.SessionID, "mail_send", json.RawMessage(batch.Items[0].Canonical))
			if err != nil {
				errs <- err
				return
			}
			if granted {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent consume: %v", err)
	}
	if got := successes.Load(); got != 1 {
		t.Fatalf("concurrent exact calls consumed %d grants, want exactly 1", got)
	}
}

func TestOwnerActionDenyAndStorageFailureExecuteNothing(t *testing.T) {
	book, st := newOwnerActionTestBook(t)
	batch := createOwnerTestBatch(t, book, `{"body":"original"}`)
	if _, err := book.decide(context.Background(), validOwnerProof(batch.ID, ""), false); err != nil {
		t.Fatal(err)
	}
	granted, err := book.consume(context.Background(), batch.MachineID, batch.SessionID, "mail_send", json.RawMessage(batch.Items[0].Canonical))
	if err != nil || granted {
		t.Fatalf("denied item: granted=%v err=%v", granted, err)
	}

	approved := createOwnerTestBatch(t, book, `{"body":"other"}`)
	approval := validOwnerProof(approved.ID, "")
	approval.EventID = "event-2"
	if _, err := book.decide(context.Background(), approval, true); err != nil {
		t.Fatal(err)
	}
	st.mu.Lock()
	st.casErr = errors.New("database unavailable")
	st.mu.Unlock()
	granted, err = book.consume(context.Background(), approved.MachineID, approved.SessionID, "mail_send", json.RawMessage(approved.Items[0].Canonical))
	if err == nil || granted {
		t.Fatalf("failed one-shot persistence must fail closed: granted=%v err=%v", granted, err)
	}
}

func TestOwnerActionTerminalCleanupNeverEvictsPendingOrGranted(t *testing.T) {
	book, _ := newOwnerActionTestBook(t)
	batch := createOwnerTestBatch(t, book, `{"body":"original"}`)
	batch.CreatedAt = time.Now().Add(-365 * 24 * time.Hour)
	if err := book.putForTest(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	_ = createOwnerTestBatch(t, book, `{"body":"cleanup-pending"}`) // runs terminal cleanup
	if _, err := book.decide(context.Background(), validOwnerProof(batch.ID, ""), true); err != nil {
		t.Fatal(err)
	}
	_ = createOwnerTestBatch(t, book, `{"body":"cleanup-granted"}`) // an unused grant still has no TTL
	granted, err := book.consume(context.Background(), batch.MachineID, batch.SessionID, "mail_send", json.RawMessage(batch.Items[0].Canonical))
	if err != nil || !granted {
		t.Fatalf("old granted item was evicted: granted=%v err=%v", granted, err)
	}
}

func TestOwnerActionRestartCancelsOnlyNonpersistentPending(t *testing.T) {
	book, _ := newOwnerActionTestBook(t)
	ctx := context.Background()
	stale := createOwnerTestBatch(t, book, `{"body":"stale"}`)
	persistent, err := book.createPersistent(ctx, "machine-1", "session-1", []ownerActionDraft{
		{Tool: "mail_send", Input: json.RawMessage(`{"body":"persistent"}`)},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	granted := createOwnerTestBatch(t, book, `{"body":"granted"}`)
	approval := validOwnerProof(granted.ID, "")
	approval.EventID = "before-restart"
	if _, err := book.decide(ctx, approval, true); err != nil {
		t.Fatal(err)
	}

	if err := book.cancelNonpersistentPending(ctx); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id       string
		state    string
		active   bool
		persist  bool
		consumed bool
	}{
		{id: stale.ID, state: ownerActionCancelled},
		{id: persistent.ID, state: ownerActionPending, active: true, persist: true},
		{id: granted.ID, state: ownerActionGranted, active: true, consumed: true},
	} {
		batch, found, err := book.batch(ctx, tc.id)
		if err != nil || !found || len(batch.Items) != 1 {
			t.Fatalf("read %s: found=%v items=%d err=%v", tc.id, found, len(batch.Items), err)
		}
		if batch.Persistent != tc.persist || batch.Items[0].State != tc.state || ownerActionBatchActive(batch) != tc.active {
			t.Errorf("batch %s after restart: persistent=%v state=%s active=%v", tc.id, batch.Persistent, batch.Items[0].State, ownerActionBatchActive(batch))
		}
		if tc.consumed {
			ok, err := book.consume(ctx, batch.MachineID, batch.SessionID, batch.Items[0].Tool, json.RawMessage(batch.Items[0].Canonical))
			if err != nil || !ok {
				t.Errorf("restart revoked an existing grant: ok=%v err=%v", ok, err)
			}
		}
	}
	proof := validOwnerProof(persistent.ID, "")
	proof.EventID = "after-restart"
	if _, err := book.decide(ctx, proof, true); err != nil {
		t.Fatalf("persistent proposal did not survive restart: %v", err)
	}
	proof = validOwnerProof(stale.ID, "")
	proof.EventID = "too-late"
	decision, err := book.decide(ctx, proof, true)
	if err != nil || len(decision.ItemIDs) != 0 {
		t.Fatalf("cancelled nonpersistent proposal accepted a late approval: newly granted=%v err=%v", decision.ItemIDs, err)
	}
	current, found, err := book.batch(ctx, stale.ID)
	if err != nil || !found || current.Items[0].State != ownerActionCancelled {
		t.Fatalf("late approval changed cancelled state: found=%v batch=%+v err=%v", found, current, err)
	}
}

func (book *ownerActionBook) putForTest(ctx context.Context, batch ownerActionBatch) error {
	for range 10 {
		doc, err := book.read(ctx)
		if err != nil {
			return err
		}
		found := false
		for i := range doc.Batches {
			if doc.Batches[i].ID == batch.ID {
				doc.Batches[i] = batch
				found = true
				break
			}
		}
		if !found {
			return errors.New("test batch not found")
		}
		doc.Revision++
		saved, err := book.cas.CompareSetting(ctx, ownerActionBookKey, doc.Revision-1, doc)
		if err != nil {
			return err
		}
		if saved {
			return nil
		}
	}
	return errors.New("test batch CAS conflict")
}
