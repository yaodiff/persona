package assistant

import (
	"context"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
)

func fallbackTierAdversarialAppend(t *testing.T, g *groupLark, it *InboxItem, messages ...string) connector.InboxThing {
	t.Helper()
	thing, err := g.GroupAppend(context.Background(), connector.GroupAppend{
		Thread: it.Group.Thread, Event: it.ID, Meta: it.Meta, Messages: messages,
	})
	if err != nil {
		t.Fatal(err)
	}
	return thing
}

func fallbackTierAdversarialKindCount(it *InboxItem, kind string) int {
	n := 0
	for _, entry := range it.Chain {
		if entry.Kind == kind {
			n++
		}
	}
	return n
}

// Reopening a done event does not stop handled() from being true. Its processing conversation still owns triage,
// so an ordinary fallback append must not use the reopen branch to bypass the handled-event guard.
func TestInboxGroupFallbackTierAdversarialDoneHandledReopenKeepsTriage(t *testing.T) {
	for _, tc := range []struct {
		name string
		mark func(*InboxItem)
	}{
		{name: "handling", mark: func(it *InboxItem) {
			it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingWaiting}
		}},
		{name: "suggestion", mark: func(it *InboxItem) {
			it.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestReply, Text: "Original suggestion"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &groupLark{placed: map[string]bool{}, ruleTier: TierAction}
			h, _ := groupHub(t, g, nil)
			it := h.inbox.items["lark:F1:900"]
			it.Count, it.Read = 1, true
			it.Title, it.Summary = "Original title", "Original summary"
			it.Tier, it.Reason, it.TriagedBy, it.Urgent = TierFYI, "Original reason", "model", false
			tc.mark(it)
			if !it.handled() {
				t.Fatal("the fixture must satisfy handled()")
			}

			thing := fallbackTierAdversarialAppend(t, g, it, "m1060", "m1061")
			planned := plannedEvent{existing: it.ID, messages: []string{"m1060", "m1061"}}
			if got := h.putEvent(sourceOf(g), it.Group.Thread, planned, thing, false); got != it {
				t.Fatalf("putEvent returned %p, want %p", got, it)
			}

			if !it.open() || it.DoneBy != "" || it.Read || !it.handled() {
				t.Fatalf("the fallback assignment did not follow the ordinary reopen path: %+v", it)
			}
			if it.Title != "Original title" || it.Summary != "Original summary" || it.Tier != TierFYI ||
				it.Reason != "Original reason" || it.TriagedBy != "model" || it.Urgent ||
				it.Count != 3 || it.Last != "1061" {
				t.Fatalf("a handled event was retriaged while reopening: %+v", it)
			}
			if got := fallbackTierAdversarialKindCount(it, ChainTriaged); got != 0 {
				t.Fatalf("a handled event gained %d triaged entries; chain %v", got, chainKinds(it))
			}
		})
	}
}

// A direct assignment and a fallback-new rewrite can share the rewritten append. The rewrite keeps its own noise
// floor, but that must not hide the ordinary assignment's action rule from the returned thing: the event takes the
// highest floor contributed by either kind of fallback assignment.
func TestInboxGroupFallbackTierAdversarialMixedDMAndOrdinaryFloors(t *testing.T) {
	g, h, it, _ := groupDMJoinFixture(t, connector.GroupFallbackResult{
		Assign: []connector.GroupAssign{{Message: "m1060", Event: "lark:F1:1051"}},
		New:    []connector.GroupNew{{Key: "k1", Messages: []string{"m1061"}, RuleTier: connector.TierNoise}},
	})
	g.ruleTier = TierAction
	it.Tier, it.Reason, it.TriagedBy = TierFYI, "Original reason", "model"
	title, summary := it.Title, it.Summary

	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.appends) != 1 || g.appends[0].Event != it.ID ||
		!slices.Equal(g.appends[0].Messages, []string{"m1060", "m1061"}) {
		t.Fatalf("appends %+v", g.appends)
	}
	if it.Title != title || it.Summary != summary || it.Tier != TierAction || it.Reason != h.inbox.byRules() ||
		it.TriagedBy != "model" || it.Count != 3 || it.Last != "1061" {
		t.Fatalf("the ordinary assignment's higher floor was lost in the mixed append: %+v", it)
	}
	if got := fallbackTierAdversarialKindCount(it, ChainTriaged); got != 1 {
		t.Fatalf("%d triaged entries, want exactly 1; chain %v", got, chainKinds(it))
	}
}

// A handling conclusion can move action to FYI with a concluded entry but no FYI triage entry. When a later fallback
// append raises FYI to action, the older action triage text must not deduplicate this new decision out of the chain.
func TestInboxGroupFallbackTierAdversarialRiseAfterEarlierActionTriageAddsEntry(t *testing.T) {
	g := &groupLark{placed: map[string]bool{}, ruleTier: TierAction}
	h, _ := groupHub(t, g, nil)
	it := h.inbox.items["lark:F1:1051"]
	it.Tier, it.Reason = TierAction, "Original action reason"
	it.Chain = []InboxChainEntry{{ID: "c1", Kind: ChainReceived, Text: "Received"}}
	it.ChainSeq = 1
	h.inbox.noteTriaged(it)
	it.Tier, it.Reason = TierFYI, "Conversation moved this to FYI"
	it.Chain = append(it.Chain, InboxChainEntry{Kind: ChainConcluded, Text: "Concluded"})
	before := fallbackTierAdversarialKindCount(it, ChainTriaged)
	if before != 1 {
		t.Fatalf("fixture has %d earlier triaged entries, want 1", before)
	}
	thing := fallbackTierAdversarialAppend(t, g, it, "m1060", "m1061")
	planned := plannedEvent{existing: it.ID, messages: []string{"m1060", "m1061"}}

	if got := h.putEvent(sourceOf(g), it.Group.Thread, planned, thing, false); got != it {
		t.Fatalf("putEvent returned %p, want %p", got, it)
	}
	if it.Tier != TierAction || it.Reason != h.inbox.byRules() {
		t.Fatalf("the fallback append did not rise: %+v", it)
	}
	if got := fallbackTierAdversarialKindCount(it, ChainTriaged); got != before+1 {
		t.Fatalf("the actual rise added %d triaged entries, want 1; chain %v", got-before, chainKinds(it))
	}
}

// A repeated append response is not another triage decision: the first delivery rises once, and the same version
// changes neither the count nor the chain when delivered again.
func TestInboxGroupFallbackTierAdversarialRepeatedRiseIsIdempotent(t *testing.T) {
	g := &groupLark{placed: map[string]bool{}, ruleTier: TierAction}
	h, _ := groupHub(t, g, nil)
	it := h.inbox.items["lark:F1:1051"]
	it.Tier, it.Reason, it.TriagedBy = TierFYI, "Original reason", "model"
	thing := fallbackTierAdversarialAppend(t, g, it, "m1060", "m1061")
	planned := plannedEvent{existing: it.ID, messages: []string{"m1060", "m1061"}}

	if got := h.putEvent(sourceOf(g), it.Group.Thread, planned, thing, false); got != it {
		t.Fatalf("first putEvent returned %p, want %p", got, it)
	}
	chain := append([]InboxChainEntry(nil), it.Chain...)
	if it.Tier != TierAction || fallbackTierAdversarialKindCount(it, ChainTriaged) != 1 || it.Count != 3 {
		t.Fatalf("first delivery %+v; chain %v", it, chainKinds(it))
	}
	if got := h.putEvent(sourceOf(g), it.Group.Thread, planned, thing, false); got != nil {
		t.Fatalf("repeated putEvent returned %p, want nil", got)
	}
	if it.Count != 3 || it.Last != "1061" || !slices.Equal(chainKinds(it), chainKinds(&InboxItem{Chain: chain})) {
		t.Fatalf("repeated delivery changed the event: %+v", it)
	}
}

// Ordinary fallback assignments use the same done-preserving mute path as direct-chat rewrites. An actual rise is
// recorded once even though the event stays done and read; no reopened entry is added.
func TestInboxGroupFallbackTierAdversarialOrdinaryMutedDoneRise(t *testing.T) {
	g := &groupLark{placed: map[string]bool{}, ruleTier: TierAction}
	h, _ := groupHub(t, g, nil)
	it := h.inbox.items["lark:F1:900"]
	it.Count, it.Read = 1, true
	it.Title, it.Summary = "Original title", "Original summary"
	it.Tier, it.Reason, it.TriagedBy = TierFYI, "Original reason", "model"
	doneAt := *it.DoneAt
	h.inbox.mutes = []InboxMute{{ID: "m1", Source: "lark", Tag: connector.TagChat, Value: "F1", Name: "Example conversation"}}
	thing := fallbackTierAdversarialAppend(t, g, it, "m1060", "m1061")
	planned := plannedEvent{existing: it.ID, messages: []string{"m1060", "m1061"}}

	if got := h.putEvent(sourceOf(g), it.Group.Thread, planned, thing, false); got != it {
		t.Fatalf("putEvent returned %p, want %p", got, it)
	}
	if it.DoneAt == nil || !it.DoneAt.Equal(doneAt) || it.DoneBy != "user" || !it.Read ||
		it.Title != "Original title" || it.Summary != "Original summary" || it.Tier != TierAction ||
		it.Reason != h.inbox.byRules() || it.TriagedBy != "model" || it.Count != 3 || it.Last != "1061" {
		t.Fatalf("muted ordinary append %+v", it)
	}
	if got := fallbackTierAdversarialKindCount(it, ChainTriaged); got != 1 {
		t.Fatalf("%d triaged entries, want exactly 1; chain %v", got, chainKinds(it))
	}
	if got := fallbackTierAdversarialKindCount(it, ChainReopened); got != 0 {
		t.Fatalf("the muted event gained %d reopened entries; chain %v", got, chainKinds(it))
	}
}
