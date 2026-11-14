package assistant

import (
	"context"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
)

// fallbackTierPutEvent appends messages through the connector and then writes the returned thing directly. The
// optional adjustment lets a case remove the connector's explicit rule tier without replacing the rest of the fixture.
func fallbackTierPutEvent(t *testing.T, h *inboxHub, g *groupLark, it *InboxItem, e plannedEvent, byModel bool, adjust func(*connector.InboxThing)) *InboxItem {
	t.Helper()
	if it.Group == nil {
		t.Fatal("the fallback-tier fixture has no conversation")
	}
	e.existing = it.ID
	thing, err := g.GroupAppend(context.Background(), connector.GroupAppend{
		Thread: it.Group.Thread, Event: it.ID, Meta: it.Meta, Messages: e.messages,
	})
	if err != nil {
		t.Fatal(err)
	}
	if adjust != nil {
		adjust(&thing)
	}
	got := h.putEvent(sourceOf(g), it.Group.Thread, e, thing, byModel)
	if got != it {
		t.Fatalf("putEvent returned %p, want the existing event %p", got, it)
	}
	return got
}

func fallbackTierKindCount(it *InboxItem, kind string) int {
	n := 0
	for _, entry := range it.Chain {
		if entry.Kind == kind {
			n++
		}
	}
	return n
}

// An ordinary fallback assignment in a group mention is not a direct-chat rewrite. Its appended thing's action rule
// raises an existing FYI event, preserves its words and triager, and records exactly one new triage decision.
func TestInboxGroupFallbackTierMentionAssignmentRaisesFYI(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, ruleTier: TierAction, fallback: connector.GroupFallbackResult{
		Assign: []connector.GroupAssign{{Message: "m1060", Event: "lark:F1:1051"}, {Message: "m1061", Event: "lark:F1:1051"}}}}
	h, _ := groupHub(t, g, nil)
	setGroupConversationKind(g, h, KindMention)
	it := h.inbox.items["lark:F1:1051"]
	it.Tier, it.Reason = TierFYI, "Original reason"
	title, summary := it.Title, it.Summary

	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.fallbacks) != 1 {
		t.Fatalf("%d fallback calls, want 1", len(g.fallbacks))
	}
	if len(g.appends) != 1 || g.appends[0].Event != it.ID ||
		!slices.Equal(g.appends[0].Messages, []string{"m1060", "m1061"}) {
		t.Fatalf("appends %+v", g.appends)
	}
	if it.Title != title || it.Summary != summary || it.Tier != TierAction || it.Reason != h.inbox.byRules() ||
		it.TriagedBy != "model" || it.Urgent || it.Count != 3 || it.Last != "1061" || it.TriagedAt != it.Last {
		t.Fatalf("mention assignment %+v", it)
	}
	if got := fallbackTierKindCount(it, ChainTriaged); got != 1 {
		t.Fatalf("%d triaged entries, want exactly 1; chain %v", got, chainKinds(it))
	}
	if len(h.inbox.items) != 2 || h.inbox.items["lark:F1:1060"] != nil {
		t.Fatalf("the ordinary assignment opened another event: %v", h.inbox.items)
	}
}

// Equal and lower rule tiers do not retriage an ordinary fallback target. Noise and an event with handled() true are
// not raised even by an action rule; all of their original triage fields survive and none gains a triaged entry.
func TestInboxGroupFallbackTierOrdinaryAppendNoRiseBranches(t *testing.T) {
	tests := []struct {
		name           string
		existing, rule string
		want           string
		urgent         bool
		handled        bool
	}{
		{name: "equal tier", existing: TierFYI, rule: TierFYI, want: TierFYI},
		{name: "higher existing tier", existing: TierAction, rule: TierFYI, want: TierAction, urgent: true},
		{name: "noise stays noise", existing: TierNoise, rule: TierAction, want: TierNoise},
		{name: "handled keeps every triage field", existing: TierFYI, rule: TierAction, want: TierFYI, urgent: true, handled: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := &groupLark{placed: map[string]bool{}, ruleTier: tc.rule}
			h, _ := groupHub(t, g, nil)
			it := h.inbox.items["lark:F1:1051"]
			it.Title, it.Summary = "Original title", "Original summary"
			it.Tier, it.Reason, it.TriagedBy, it.Urgent = tc.existing, "Original reason", "model", tc.urgent
			if tc.handled {
				it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingWaiting}
			}

			fallbackTierPutEvent(t, h, g, it, plannedEvent{messages: []string{"m1060", "m1061"}}, false, nil)

			if it.Title != "Original title" || it.Summary != "Original summary" || it.Tier != tc.want ||
				it.Reason != "Original reason" || it.TriagedBy != "model" || it.Urgent != tc.urgent ||
				it.Count != 3 || it.Last != "1061" {
				t.Fatalf("ordinary append %+v", it)
			}
			if got := fallbackTierKindCount(it, ChainTriaged); got != 0 {
				t.Fatalf("%d triaged entries without a rise; chain %v", got, chainKinds(it))
			}
		})
	}
}

// With no explicit eventRuleTier, an ordinary append uses the same fallbackEventTier default as fallback-new. A
// mention therefore defaults to action, while a bot conversation defaults to noise rather than a blanket action floor.
func TestInboxGroupFallbackTierUsesFallbackEventDefault(t *testing.T) {
	tests := []struct {
		name             string
		kind             string
		want             string
		wantByRules      bool
		wantTriagedCount int
	}{
		{name: "mention default", kind: KindMention, want: TierAction, wantByRules: true, wantTriagedCount: 1},
		{name: "bot default", kind: KindBot, want: TierFYI},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := &groupLark{placed: map[string]bool{}}
			h, _ := groupHub(t, g, nil)
			it := h.inbox.items["lark:F1:1051"]
			it.Title, it.Summary = "Original title", "Original summary"
			it.Tier, it.Reason, it.TriagedBy = TierFYI, "Original reason", "model"

			fallbackTierPutEvent(t, h, g, it, plannedEvent{messages: []string{"m1060", "m1061"}}, false, func(thing *connector.InboxThing) {
				thing.Kind = tc.kind
				thing.Tier = ""
				thing.Extra.RuleTier = ""
			})

			wantReason := "Original reason"
			if tc.wantByRules {
				wantReason = h.inbox.byRules()
			}
			if it.Title != "Original title" || it.Summary != "Original summary" || it.Tier != tc.want ||
				it.Reason != wantReason || it.TriagedBy != "model" {
				t.Fatalf("default-tier append %+v", it)
			}
			if got := fallbackTierKindCount(it, ChainTriaged); got != tc.wantTriagedCount {
				t.Fatalf("%d triaged entries, want %d; chain %v", got, tc.wantTriagedCount, chainKinds(it))
			}
		})
	}
}

// The existing direct-chat fallback-new rewrite remains monotonic. Only its actual rise contributes a triaged entry;
// equal, lower and noise cases preserve the original reason and contribute none.
func TestInboxGroupFallbackTierDMJoinChainIsMonotonic(t *testing.T) {
	tests := []struct {
		name             string
		existing, rule   string
		want             string
		urgent           bool
		wantByRules      bool
		wantTriagedCount int
	}{
		{name: "rise", existing: TierFYI, rule: TierAction, want: TierAction, wantByRules: true, wantTriagedCount: 1},
		{name: "equal", existing: TierFYI, rule: TierFYI, want: TierFYI},
		{name: "lower", existing: TierAction, rule: TierFYI, want: TierAction, urgent: true},
		{name: "noise", existing: TierNoise, rule: TierAction, want: TierNoise},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, h, it, _ := groupDMJoinFixture(t, connector.GroupFallbackResult{
				New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.Tier(tc.rule)}},
			})
			it.Tier, it.Reason, it.Urgent = tc.existing, "Original reason", tc.urgent
			title, summary := it.Title, it.Summary

			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			if len(g.appends) != 1 || g.appends[0].Event != it.ID {
				t.Fatalf("appends %+v", g.appends)
			}
			wantReason := "Original reason"
			if tc.wantByRules {
				wantReason = h.inbox.byRules()
			}
			if it.Title != title || it.Summary != summary || it.Tier != tc.want || it.Reason != wantReason ||
				it.TriagedBy != "model" || it.Urgent != tc.urgent || it.Count != 3 || it.Last != "1061" {
				t.Fatalf("direct-chat join %+v", it)
			}
			if got := fallbackTierKindCount(it, ChainTriaged); got != tc.wantTriagedCount {
				t.Fatalf("%d triaged entries, want %d; chain %v", got, tc.wantTriagedCount, chainKinds(it))
			}
			if len(h.inbox.items) != 2 || h.inbox.items["lark:F1:1060"] != nil {
				t.Fatalf("the rewrite opened another event: %v", h.inbox.items)
			}
		})
	}
}

// A model closeout keeps a done event's own triage even when the appended thing carries an action rule. The new
// ordinary-fallback floor must not leak into this keepDone path.
func TestInboxGroupFallbackTierModelCloseoutHasNoFloor(t *testing.T) {
	g := &groupLark{placed: map[string]bool{}, ruleTier: TierAction}
	h, _ := groupHub(t, g, nil)
	it := h.inbox.items["lark:F1:900"]
	it.Count, it.Read = 1, true
	it.Title, it.Summary = "Original closeout title", "Original closeout summary"
	it.Tier, it.Reason, it.TriagedBy, it.Urgent = TierFYI, "Original closeout reason", "model", false
	doneAt := *it.DoneAt
	closeout := false

	fallbackTierPutEvent(t, h, g, it, plannedEvent{
		messages: []string{"m1060", "m1061"}, reopen: &closeout,
		title: "Model closeout title", summary: "Model closeout summary", tier: TierAction, reason: "Model closeout reason", urgent: true,
	}, true, nil)

	if it.DoneAt == nil || !it.DoneAt.Equal(doneAt) || it.DoneBy != "user" || !it.Read ||
		it.Title != "Original closeout title" || it.Summary != "Original closeout summary" ||
		it.Tier != TierFYI || it.Reason != "Original closeout reason" || it.TriagedBy != "model" || it.Urgent ||
		it.Count != 3 || it.Last != "1061" {
		t.Fatalf("model closeout %+v", it)
	}
	if got := fallbackTierKindCount(it, ChainTriaged); got != 0 {
		t.Fatalf("the closeout gained %d triaged entries; chain %v", got, chainKinds(it))
	}
	if got := fallbackTierKindCount(it, ChainReopened); got != 0 {
		t.Fatalf("the closeout gained %d reopened entries; chain %v", got, chainKinds(it))
	}
}

// A mute keeping a direct-chat rewrite done does not disable the rewrite's pre-existing A-8 floor. This is distinct
// from a model closeout: the event stays done and read, while its rise is recorded once and it is not reopened.
func TestInboxGroupFallbackTierMutedDMJoinKeepsA8Floor(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, ruleTier: TierAction}
	h, _ := groupHub(t, g, nil)
	setGroupConversationKind(g, h, KindDM)
	it := h.inbox.items["lark:F1:900"]
	it.Count, it.Read = 1, true
	it.Title, it.Summary = "Original muted title", "Original muted summary"
	it.Tier, it.Reason, it.TriagedBy = TierFYI, "Original muted reason", "model"
	doneAt := *it.DoneAt
	h.inbox.mutes = []InboxMute{{ID: "m1", Source: "lark", Tag: connector.TagChat, Value: "F1", Name: "Example conversation"}}

	fallbackTierPutEvent(t, h, g, it, plannedEvent{
		messages: []string{"m1060", "m1061"}, dmJoined: true, ruleFloor: TierAction,
	}, false, nil)

	if it.DoneAt == nil || !it.DoneAt.Equal(doneAt) || it.DoneBy != "user" || !it.Read ||
		it.Title != "Original muted title" || it.Summary != "Original muted summary" ||
		it.Tier != TierAction || it.Reason != h.inbox.byRules() || it.TriagedBy != "model" ||
		it.Count != 3 || it.Last != "1061" {
		t.Fatalf("muted direct-chat join %+v", it)
	}
	if got := fallbackTierKindCount(it, ChainTriaged); got != 1 {
		t.Fatalf("%d triaged entries, want exactly 1; chain %v", got, chainKinds(it))
	}
	if got := fallbackTierKindCount(it, ChainReopened); got != 0 {
		t.Fatalf("the muted event gained %d reopened entries; chain %v", got, chainKinds(it))
	}
}
