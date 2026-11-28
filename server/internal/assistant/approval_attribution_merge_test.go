package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestRecoveredOutcomeCarriesUnansweredReason(t *testing.T) {
	ft := newFleetTest(t)
	for _, tc := range []struct {
		status string
		reason string
	}{
		{status: "expired", reason: proto.DismissedExpired},
		{status: "cancelled", reason: proto.DismissedWithdrawn},
	} {
		ix := proto.Interaction{ID: "ix_recovered_" + tc.status, MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, CreatedAt: time.Now().UTC()}
		if err := ft.st.InsertInteraction(t.Context(), ix); err != nil {
			t.Fatal(err)
		}
		if err := ft.st.FinishInteraction(t.Context(), ix.ID, tc.status, nil); err != nil {
			t.Fatal(err)
		}
		status, r, ended := ft.s.outcome(ix.ID)
		if status != tc.status || !ended || !r.Dismiss || r.Reason != tc.reason {
			t.Fatalf("recovered %s outcome = status %q resolution %+v ended %v; want reason %q", tc.status, status, r, ended, tc.reason)
		}
	}
}

func TestGraceEndedReconnectCarriesWithdrawnReason(t *testing.T) {
	ft := newFleetTest(t)
	ix := proto.Interaction{ID: "ix_grace_reason", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
		Resumable: true, CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour)}
	p := &pending{ix: ix, done: make(chan proto.Resolution, 1), graceEnded: true}
	ft.s.fleet.mu.Lock()
	ft.s.ixMapLocked()[ix.ID] = p
	ft.s.fleet.mu.Unlock()
	r, err := ft.s.waitInteraction(t.Context(), ix)
	if err != nil || !r.Dismiss || r.Reason != proto.DismissedWithdrawn {
		t.Fatalf("grace-ended reconnect = %+v, %v; want withdrawn reason", r, err)
	}
}

// ownerActionAttributionStore keeps owner-action CAS and interaction history in
// their existing test implementations. Explicit setting and placement methods
// resolve the ambiguous selectors introduced by embedding both.
type ownerActionAttributionStore struct {
	*ownerActionTestStore
	*fleetMem
}

func (st *ownerActionAttributionStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	return st.ownerActionTestStore.Setting(ctx, key, out)
}

func (st *ownerActionAttributionStore) PutSetting(ctx context.Context, key string, value any) error {
	return st.ownerActionTestStore.PutSetting(ctx, key, value)
}

func (st *ownerActionAttributionStore) PutPlacement(ctx context.Context, p db.PlacementRow) error {
	return st.ownerActionTestStore.PutPlacement(ctx, p)
}

func (st *ownerActionAttributionStore) PutModelRouting(ctx context.Context, row db.ModelRoutingRow) error {
	return st.fleetMem.PutModelRouting(ctx, row)
}

func TestOwnerActionCompletionKeepsDeviceAttribution(t *testing.T) {
	f, _ := feishuHub("ou_owner", time.Unix(100, 0))
	on := true
	f.settings = &Settings{MachineID: "server", Main: "m1", OwnerReplyApproval: &on, PersistentOwnerActionCards: &on}
	st := &ownerActionAttributionStore{ownerActionTestStore: &ownerActionTestStore{}, fleetMem: newFleetMem()}
	s := newServiceOn(t, f, st)
	book, ok := newOwnerActionBook(st)
	if !ok {
		t.Fatal("owner-action test store must support CAS")
	}
	batch := createOwnerTestBatch(t, book, `{"body":"device attribution"}`)
	ix := ownerReplyBatchIX(batch)
	putOwnerReplyPending(s, ix)
	allow := true
	ctx := db.WithEndedBy(context.Background(), db.EndedByDevice)
	if err := s.resolveApprovedAt(ctx, ix.ID, proto.Resolution{Allow: &allow}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := st.endedAs(ix.ID); got != db.EndedByDevice {
		t.Fatalf("owner-action completion recorded ended_by %q, want %q", got, db.EndedByDevice)
	}
}

func TestPreparedTerminalDismissKeepsTerminalAttribution(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := proto.Interaction{ID: "ix_prepared_terminal_attribution", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission,
		ToolName: "persona_settings", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour)}
	done := startPreparedInteraction(t, ft, ix, approvalops.Operation{})
	ft.s.endNodeInteraction(nodeInteractionEnd{ID: ix.ID, CreatedAt: ix.CreatedAt}, "dismissed", "dismissed on the computer",
		&proto.Resolution{Dismiss: true, AnsweredBy: proto.AnsweredByTerminal})
	if got := ft.st.endedAs(ix.ID); got != db.EndedByTerminal {
		t.Fatalf("prepared terminal dismissal recorded ended_by %q, want %q", got, db.EndedByTerminal)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("prepared waiter did not finish after terminal dismissal")
	}
}

func TestCodexScreenReplacementRecordsCleared(t *testing.T) {
	ft := newFleetTest(t)
	old := proto.Interaction{ID: "ix_screen_replaced", MachineID: "mac", SessionID: "s1", Kind: proto.IxScreen, CreatedAt: time.Now().UTC()}
	if err := ft.st.InsertInteraction(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	ft.s.fleet.mu.Lock()
	ft.s.ixMapLocked()[old.ID] = &pending{ix: old, done: make(chan proto.Resolution, 1)}
	ft.s.fleet.mu.Unlock()
	replacement := proto.Interaction{ID: "ix_codex_replacement", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, OutwardWrite: true, CreatedAt: time.Now().UTC()}
	raw, err := json.Marshal(map[string]any{"interactions": []proto.Interaction{replacement}})
	if err != nil {
		t.Fatal(err)
	}
	ft.s.syncCodexInteractions("mac", raw)
	if got := ft.st.endedAs(old.ID); got != db.EndedByCleared {
		t.Fatalf("Codex screen replacement recorded ended_by %q, want %q", got, db.EndedByCleared)
	}
}

func TestReplacementOccurrenceClearsFleetMemAttribution(t *testing.T) {
	st := newFleetMem()
	ix := proto.Interaction{ID: "ix_mem_replacement_attribution", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, CreatedAt: time.Now().UTC()}
	if err := st.InsertInteraction(t.Context(), ix); err != nil {
		t.Fatal(err)
	}
	allow := true
	if err := st.FinishInteraction(db.WithEndedBy(t.Context(), db.EndedByDevice), ix.ID, "resolved", &proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	later := ix
	later.CreatedAt = later.CreatedAt.Add(time.Second)
	if err := st.InsertInteraction(t.Context(), later); err != nil {
		t.Fatal(err)
	}
	if got := st.endedAs(ix.ID); got != "" {
		t.Fatalf("pending replacement retained earlier ended_by %q", got)
	}
}

func TestRecoveredApprovalWithoutLiveCardRecordsCleared(t *testing.T) {
	ft := newFleetTest(t)
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_recovery_no_live_attribution")
	ft.s.record(ix)
	entry := putTerminalOccurrenceJournal(t, ft, ix, "cancelled", approvalRestartCancelledText)
	st := &restoreMem{fleetMem: ft.st}
	restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
	if err := restarted.finishRecoveredApproval(entry); err != nil {
		t.Fatal(err)
	}
	if got := st.endedAs(ix.ID); got != db.EndedByCleared {
		t.Fatalf("no-live recovery recorded ended_by %q, want %q", got, db.EndedByCleared)
	}
}

func TestRecoveredTerminalApprovalKeepsOutcomeAttribution(t *testing.T) {
	for _, tc := range []struct {
		status      string
		wantBy      string
		wantOutcome string
	}{
		{status: "expired", wantBy: db.EndedByExpired, wantOutcome: proto.OutcomeExpired},
		{status: "resolved", wantBy: ""},
	} {
		t.Run(tc.status, func(t *testing.T) {
			ft := newFleetTest(t)
			preparedTestConversation(ft)
			ix := detachedTestCard("ix_recovery_retained_" + tc.status)
			ft.s.record(ix)
			entry := putTerminalOccurrenceJournal(t, ft, ix, tc.status, "The retained terminal outcome is delivered after restart.")
			st := &restoreMem{fleetMem: ft.st}
			restarted := newRestartedService(t, Options{Hub: ft.f, Store: func() Store { return st }, Logf: testLogf(t), Devices: ft.d})
			if err := restarted.finishRecoveredApproval(entry); err != nil {
				t.Fatal(err)
			}
			if got := st.endedAs(ix.ID); got != tc.wantBy {
				t.Fatalf("retained %s recovery recorded ended_by %q, want %q", tc.status, got, tc.wantBy)
			}
			if tc.wantOutcome != "" {
				status, r, ended := restarted.outcome(ix.ID)
				if status != tc.status || !ended || r.Outcome() != tc.wantOutcome {
					t.Fatalf("retained %s replay = status %q outcome %q ended %v; want outcome %q", tc.status, status, r.Outcome(), ended, tc.wantOutcome)
				}
			}
		})
	}
}

type preparedAttributionStore struct {
	*fleetMem
	finishBy     []string
	occurrenceBy []string
}

func (st *preparedAttributionStore) FinishInteraction(ctx context.Context, id, status string, r *proto.Resolution) error {
	st.finishBy = append(st.finishBy, db.EndedBy(ctx))
	return st.fleetMem.FinishInteraction(ctx, id, status, r)
}

func (st *preparedAttributionStore) FinishInteractionOccurrence(ctx context.Context, ix proto.Interaction, status string, r *proto.Resolution) (bool, bool, error) {
	st.occurrenceBy = append(st.occurrenceBy, db.EndedBy(ctx))
	return st.fleetMem.FinishInteractionOccurrence(ctx, ix, status, r)
}

func TestRecoveredLivePreparedApprovalRecordsCleared(t *testing.T) {
	ft := newFleetTest(t)
	st := &preparedAttributionStore{fleetMem: ft.st}
	ft.s.setStore(func() Store { return st })
	preparedTestConversation(ft)
	ix := detachedTestCard("ix_recovery_live_prepared_attribution")
	done := startPreparedInteraction(t, ft, ix, approvalops.Operation{})
	ft.s.fleet.mu.Lock()
	p := ft.s.fleet.interactions[ix.ID]
	if p != nil {
		ix = p.ix
	}
	ft.s.fleet.mu.Unlock()
	if p == nil {
		t.Fatal("live prepared card was not registered")
	}
	if err := ft.s.approvalJournalStarted(context.Background(), ix); err != nil {
		t.Fatal(err)
	}
	// Use the unfinished journal written by the real prepared-card entry point.
	// Writing a second entry directly to the store after that entry point has
	// loaded the journal cache would make recovery return "not found" without
	// exercising either the live card or its terminal history write.
	entry, found, err := ft.s.approvalJournalRecord(context.Background(), ix)
	if err != nil || !found {
		t.Fatalf("live prepared journal = found %v, %v", found, err)
	}
	if err := ft.s.finishRecoveredApproval(entry); err != nil {
		t.Fatal(err)
	}
	if got := st.endedAs(ix.ID); got != db.EndedByCleared {
		t.Fatalf("live prepared recovery recorded ended_by %q, want %q; finish calls %q, occurrence calls %q", got, db.EndedByCleared, st.finishBy, st.occurrenceBy)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("recovered prepared waiter did not finish")
	}
}

func TestRecoveredOutcomePreservesExistingDismissReason(t *testing.T) {
	ft := newFleetTest(t)
	ix := proto.Interaction{ID: "ix_recovered_replaced_reason", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, CreatedAt: time.Now().UTC()}
	if err := ft.st.InsertInteraction(t.Context(), ix); err != nil {
		t.Fatal(err)
	}
	if err := ft.st.FinishInteraction(t.Context(), ix.ID, "cancelled", &proto.Resolution{Dismiss: true, Reason: "replaced"}); err != nil {
		t.Fatal(err)
	}
	status, r, ended := ft.s.outcome(ix.ID)
	if status != "cancelled" || !ended || !r.Dismiss || r.Reason != "replaced" {
		t.Fatalf("recovered replacement outcome = status %q resolution %+v ended %v", status, r, ended)
	}
}

type codexAttributionStore struct {
	*fleetMem
	occurrenceEndedBy []string
}

func (st *codexAttributionStore) FinishInteractionOccurrence(ctx context.Context, ix proto.Interaction, status string, r *proto.Resolution) (bool, bool, error) {
	st.occurrenceEndedBy = append(st.occurrenceEndedBy, db.EndedBy(ctx))
	return st.fleetMem.FinishInteractionOccurrence(ctx, ix, status, r)
}

func TestCodexSameIDReplacementRecordsCleared(t *testing.T) {
	ft := newFleetTest(t)
	st := &codexAttributionStore{fleetMem: ft.st}
	ft.s.setStore(func() Store { return st })
	first := proto.Interaction{ID: "ix_codex_same_id", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, OutwardWrite: true, CreatedAt: time.Now().UTC()}
	raw, err := json.Marshal(map[string]any{"interactions": []proto.Interaction{first}})
	if err != nil {
		t.Fatal(err)
	}
	ft.s.syncCodexInteractions("mac", raw)
	second := first
	second.CreatedAt = second.CreatedAt.Add(time.Second)
	raw, err = json.Marshal(map[string]any{"interactions": []proto.Interaction{second}})
	if err != nil {
		t.Fatal(err)
	}
	ft.s.syncCodexInteractions("mac", raw)
	if len(st.occurrenceEndedBy) != 1 || st.occurrenceEndedBy[0] != db.EndedByCleared {
		t.Fatalf("same-ID Codex replacement recorded ended_by %v, want [%s]", st.occurrenceEndedBy, db.EndedByCleared)
	}
}
