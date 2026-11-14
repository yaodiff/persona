package assistant

import (
	"context"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
)

// Fallback rises must bypass noteTriaged's same-text dedup, but model-driven reopens retain their existing chain
// semantics. In particular, a model reopen that returns to the tier of the latest triaged entry does not add a duplicate.
func TestInboxGroupFallbackTierRound2ModelReopenKeepsTriageDedup(t *testing.T) {
	g := &groupLark{placed: map[string]bool{}, ruleTier: TierFYI}
	h, _ := groupHub(t, g, nil)
	it := h.inbox.items["lark:F1:900"]
	it.Count, it.Read = 1, true
	it.Tier, it.Reason = TierAction, "Earlier model action"
	it.Chain = []InboxChainEntry{{ID: "c1", Kind: ChainReceived, Text: "Received"}}
	it.ChainSeq = 1
	h.inbox.noteTriaged(it)
	it.Tier, it.Reason = TierFYI, "Conversation moved this to FYI"
	it.Chain = append(it.Chain, InboxChainEntry{Kind: ChainConcluded, Text: "Concluded"})
	before := fallbackTierKindCount(it, ChainTriaged)
	if before != 1 {
		t.Fatalf("fixture has %d earlier triaged entries, want 1", before)
	}

	messages := []string{"m1060", "m1061"}
	thing, err := g.GroupAppend(context.Background(), connector.GroupAppend{
		Thread: it.Group.Thread, Event: it.ID, Meta: it.Meta, Messages: messages,
	})
	if err != nil {
		t.Fatal(err)
	}
	planned := plannedEvent{
		existing: it.ID, messages: messages,
		title: "Model reopen title", summary: "Model reopen summary", tier: TierAction, reason: "Model reopen reason",
	}
	if got := h.putEvent(sourceOf(g), it.Group.Thread, planned, thing, true); got != it {
		t.Fatalf("putEvent returned %p, want the existing event %p", got, it)
	}

	if !it.open() || it.Tier != TierAction || it.Reason != "Model reopen reason" || it.TriagedBy != "model" {
		t.Fatalf("model reopen did not apply its triage: %+v", it)
	}
	if got := fallbackTierKindCount(it, ChainTriaged); got != before {
		t.Fatalf("model reopen added %d duplicate triaged entries; chain %v", got-before, chainKinds(it))
	}
}

// The broadened handled-reopen guard must not change legacy triager provenance. Before it routed fallback reopens
// through the handled branch, this path restored an empty triaged_by from old data instead of backfilling "model".
func TestInboxGroupFallbackTierRound2DoneHandledReopenKeepsEmptyTriager(t *testing.T) {
	g := &groupLark{placed: map[string]bool{}, ruleTier: TierAction}
	h, _ := groupHub(t, g, nil)
	it := h.inbox.items["lark:F1:900"]
	it.Count, it.Read = 1, true
	it.Tier, it.Reason, it.TriagedBy = TierFYI, "Original reason", ""
	it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingWaiting}
	messages := []string{"m1060", "m1061"}
	thing := fallbackTierAdversarialAppend(t, g, it, messages...)
	planned := plannedEvent{existing: it.ID, messages: messages}

	if got := h.putEvent(sourceOf(g), it.Group.Thread, planned, thing, false); got != it {
		t.Fatalf("putEvent returned %p, want the existing event %p", got, it)
	}
	if !it.open() || !it.handled() || it.Tier != TierFYI || it.Reason != "Original reason" {
		t.Fatalf("handled fallback reopen did not preserve triage: %+v", it)
	}
	if it.TriagedBy != "" {
		t.Fatalf("legacy empty triaged_by became %q during fallback reopen", it.TriagedBy)
	}
}
