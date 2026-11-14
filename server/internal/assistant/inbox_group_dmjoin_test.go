package assistant

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// Tests the package's own tests do not cover for A-8 (a direct chat's fallback-new messages joining the conversation's
// latest event): the model branch is untouched, an event being handled keeps its words, an ordinary group fallback append can raise
// but never lower the tier, a fallback name the hub never offered is refused, and the rule floor takes the highest
// rewritten entry and the fallback-new default when an entry carries no tier.

// Only a fallback plan is rewritten: an event the model opens in a direct chat stays its own event.
func TestInboxGroupDMModelPlanIsNotRewritten(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}}
	_, client := newGroupModel(t, `{"assign":[{"message":"m1060","event":"new:1"},{"message":"m1061","event":"new:1"}],
		"events":[{"event":"new:1","title":"示例模型新事件","summary":"示例","tier":"action","reason":"示例"}]}`)
	h, _ := groupHub(t, g, client)
	setGroupConversationKind(g, h, KindDM)
	it := h.inbox.items["lark:F1:1051"]
	it.UpdatedAt = time.Now().Add(-time.Minute)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.fallbacks) != 0 {
		t.Fatalf("the model's answer went to the fallback: %+v", g.fallbacks)
	}
	fresh := h.inbox.items["lark:F1:1060"]
	if fresh == nil || len(g.appends) != 1 || g.appends[0].Event != "" || fresh.Title != "示例模型新事件" || fresh.TriagedBy != "model" {
		t.Fatalf("new event %+v, appends %+v", fresh, g.appends)
	}
	if it.Count != 1 || it.Last != "1051" || len(h.inbox.items) != 3 {
		t.Fatalf("the model's new event joined the latest one: %+v, items %d", it, len(h.inbox.items))
	}
}

// An event a processing conversation works on keeps its tier, words and urgency when a rewritten join comes in: the
// conversation decides what the messages mean (the resolver's ruling on A-8), exactly as an ordinary fallback append does.
func TestInboxGroupDMJoinLeavesHandledEventAlone(t *testing.T) {
	g, h, it, _ := groupDMJoinFixture(t, connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierAction}},
	})
	it.Tier, it.Reason, it.Urgent = TierFYI, "示例原理由", false
	it.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingWaiting}
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.appends) != 1 || g.appends[0].Event != it.ID {
		t.Fatalf("appends %+v", g.appends)
	}
	if it.Title != "示例 A 方案评审" || it.Summary != "等你定时间" || it.Tier != TierFYI || it.Reason != "示例原理由" ||
		it.TriagedBy != "model" || it.Urgent || it.Count != 3 || it.Last != "1061" {
		t.Fatalf("handled event %+v", it)
	}
	if it.Handling == nil || it.Handling.State != HandlingWaiting {
		t.Fatalf("the join changed how the event is handled: %+v", it.Handling)
	}
	if slices.Contains(chainKinds(it), ChainTriaged) {
		t.Fatalf("the join recorded a retriage of a handled event: %v", chainKinds(it))
	}
	if len(h.inbox.items) != 2 || h.inbox.items["lark:F1:1060"] != nil {
		t.Fatalf("the rewrite opened another event: %v", h.inbox.items)
	}
}

// A group's ordinary fallback append takes the same monotonic rule floor as a direct-chat rewrite (A-12).
func TestInboxGroupFallbackAppendRaisesTheGroupTier(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, ruleTier: TierAction, fallback: connector.GroupFallbackResult{
		Assign: []connector.GroupAssign{{Message: "m1060", Event: "lark:F1:1051"}, {Message: "m1061", Event: "lark:F1:1051"}}}}
	h, _ := groupHub(t, g, nil)
	it := h.inbox.items["lark:F1:1051"]
	it.Tier, it.Reason = TierFYI, "示例原理由"
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.appends) != 1 || g.appends[0].Event != it.ID {
		t.Fatalf("appends %+v", g.appends)
	}
	if it.Title != "示例 A 方案评审" || it.Summary != "等你定时间" || it.Tier != TierAction || it.Reason != "按规则" ||
		it.TriagedBy != "model" || it.Count != 3 || it.Last != "1061" {
		t.Fatalf("ordinary append %+v", it)
	}
	if kinds := chainKinds(it); !slices.Contains(kinds, ChainTriaged) {
		t.Fatalf("the rule-tier rise was not recorded: %v", kinds)
	}
	if len(h.inbox.items) != 2 || h.inbox.items["lark:F1:1060"] != nil {
		t.Fatalf("the ordinary append opened another event: %v", h.inbox.items)
	}
}

// A fallback naming an event the hub never offered it is refused, in a direct chat as anywhere: nothing is appended
// or opened, and the batch waits for the next round.
func TestInboxGroupFallbackRejectsAnEventItWasNotGiven(t *testing.T) {
	g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
		Assign: []connector.GroupAssign{{Message: "m1060", Event: "lark:F1:777"}, {Message: "m1061", Event: "lark:F1:777"}}}}
	h, _ := groupHub(t, g, nil)
	setGroupConversationKind(g, h, KindDM)
	it := h.inbox.items["lark:F1:1051"]
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.fallbacks) != 1 {
		t.Fatalf("%d fallback call(s)", len(g.fallbacks))
	}
	if len(g.appends) != 0 {
		t.Fatalf("an event the fallback was not given was written: %+v", g.appends)
	}
	if len(h.inbox.items) != 2 || it.Count != 1 || it.Last != "1051" ||
		h.inbox.items["lark:F1:1060"] != nil || h.inbox.items["lark:F1:777"] != nil {
		t.Fatalf("items %v", h.inbox.items)
	}
	if g.batch == nil {
		t.Fatal("the refused batch was marked placed")
	}
}

// The rule floor is the highest rule tier among the rewritten new entries, not the first or the last one.
func TestInboxGroupDMJoinTierFloorIsTheHighestNewEntry(t *testing.T) {
	g, h, it, _ := groupDMJoinFixture(t, connector.GroupFallbackResult{New: []connector.GroupNew{
		{Key: "k1", Messages: []string{"m1060"}, RuleTier: connector.TierNoise},
		{Key: "k2", Messages: []string{"m1061"}, RuleTier: connector.TierAction},
	}})
	it.Tier, it.Reason = TierFYI, "示例原理由"
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.appends) != 1 || g.appends[0].Event != it.ID || !slices.Equal(g.appends[0].Messages, []string{"m1060", "m1061"}) {
		t.Fatalf("appends %+v", g.appends)
	}
	if it.Tier != TierAction || it.Reason != "按规则" || it.Title != "示例 A 方案评审" || it.Summary != "等你定时间" || it.Count != 3 {
		t.Fatalf("joined event %+v", it)
	}
}

// A rewritten new entry without a rule tier takes the same fallback-new default a new event would (from the appended
// thing's rule tier, then the conversation's rules, then Needs you): here the connector's thing says action.
func TestInboxGroupDMJoinDefaultFloorWithoutARuleTier(t *testing.T) {
	g, h, it, _ := groupDMJoinFixture(t, connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}}},
	})
	g.ruleTier, g.tier = TierAction, TierAction
	it.Tier, it.Reason = TierFYI, "示例原理由"
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.appends) != 1 || g.appends[0].Event != it.ID {
		t.Fatalf("appends %+v", g.appends)
	}
	if it.Tier != TierAction || it.Reason != "按规则" || it.TriagedBy != "model" || it.Count != 3 {
		t.Fatalf("joined event %+v", it)
	}
}

// The rewrite joins only the new entries: a fallback assignment to another candidate is still its own append, and a
// done one reopens.
func TestInboxGroupDMJoinKeepsAnotherExistingAssignment(t *testing.T) {
	g, h, it, done := groupDMJoinFixture(t, connector.GroupFallbackResult{New: []connector.GroupNew{
		{Key: "k1", Messages: []string{"m1061"}, RuleTier: connector.TierAction},
	}, Assign: []connector.GroupAssign{{Message: "m1060", Event: "lark:F1:900"}}})
	done.Count = 1
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	events := map[string][]string{}
	for _, a := range g.appends {
		events[a.Event] = append(events[a.Event], a.Messages...)
	}
	if len(g.appends) != 2 || !slices.Equal(events[it.ID], []string{"m1061"}) || !slices.Equal(events[done.ID], []string{"m1060"}) {
		t.Fatalf("appends %+v", g.appends)
	}
	if it.Count != 2 || it.Last != "1061" || !done.open() || done.Count != 2 || done.Last != "1060" {
		t.Fatalf("open %+v, done %+v", it, done)
	}
	if k := chainKinds(done); len(k) < 2 || k[0] != ChainReceived || k[1] != ChainReopened {
		t.Fatalf("done chain %v", k)
	}
	if len(h.inbox.items) != 2 {
		t.Fatalf("items %v", h.inbox.items)
	}
}

// The two-hour window counts the latest candidate's own last message. Just inside it joins, just outside it opens its
// own event.
func TestInboxGroupDMJoinWindowBoundary(t *testing.T) {
	for _, c := range []struct {
		name   string
		age    time.Duration
		joined bool
	}{
		{name: "inside", age: 110 * time.Minute, joined: true},
		{name: "outside", age: 130 * time.Minute, joined: false},
	} {
		t.Run(c.name, func(t *testing.T) {
			g, h, it, done := groupDMJoinFixture(t, connector.GroupFallbackResult{
				New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierAction}},
			})
			now := time.Now()
			it.UpdatedAt = now.Add(-c.age)
			done.UpdatedAt = now.Add(-24 * time.Hour)
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			if c.joined {
				if len(g.appends) != 1 || g.appends[0].Event != it.ID || it.Count != 3 || h.inbox.items["lark:F1:1060"] != nil {
					t.Fatalf("appends %+v, items %v", g.appends, h.inbox.items)
				}
				return
			}
			if len(g.appends) != 1 || g.appends[0].Event != "" || it.Count != 1 || h.inbox.items["lark:F1:1060"] == nil {
				t.Fatalf("appends %+v, items %v", g.appends, h.inbox.items)
			}
		})
	}
}

// A rewritten join keeps the event's triage chain honest: the raise is recorded as a triage entry, and the batch's
// messages keep the event's own conversation as their group.
func TestInboxGroupDMJoinRecordsTheRaise(t *testing.T) {
	g, h, it, _ := groupDMJoinFixture(t, connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierAction}},
	})
	it.Tier, it.Reason = TierFYI, "示例原理由"
	it.TriagedAt = it.Last
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if len(g.appends) != 1 || g.appends[0].Thread != "chat:F1" {
		t.Fatalf("appends %+v", g.appends)
	}
	if it.Group == nil || it.Group.Connector != "lark" || it.Group.Thread != "chat:F1" {
		t.Fatalf("the joined event moved: %+v", it.Group)
	}
	if kinds := chainKinds(it); !slices.Contains(kinds, ChainTriaged) {
		t.Fatalf("the raised tier was not recorded: %v", kinds)
	}
	if it.TriagedAt != it.Last {
		t.Fatalf("triaged_at %q, last %q", it.TriagedAt, it.Last)
	}
	if string(it.Meta) != `{"messages":["m1051","m1060","m1061"]}` {
		t.Fatalf("meta %s", it.Meta)
	}
}
