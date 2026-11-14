package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// Only direct chats are exempt from the done-event count cap. Every other candidate filter still applies.
func TestInboxGroupEventsDoneDMCountCap(t *testing.T) {
	tests := []struct {
		name   string
		kind   string
		tier   string
		count  int
		done   bool
		merged bool
		want   bool
	}{
		{name: "done direct at cap", kind: KindDM, tier: TierFYI, count: groupDoneCountMax, done: true, want: true},
		{name: "done direct above cap", kind: KindDM, tier: TierFYI, count: groupDoneCountMax + 25, done: true, want: true},
		{name: "done group at cap", kind: KindGroup, tier: TierFYI, count: groupDoneCountMax, done: true},
		{name: "done mention above cap", kind: KindMention, tier: TierFYI, count: groupDoneCountMax + 25, done: true},
		{name: "done bot above cap", kind: KindBot, tier: TierFYI, count: groupDoneCountMax + 25, done: true},
		{name: "done unspecified kind above cap", tier: TierFYI, count: groupDoneCountMax + 25, done: true},
		{name: "done direct noise above cap", kind: KindDM, tier: TierNoise, count: groupDoneCountMax + 25, done: true},
		{name: "open direct above cap", kind: KindDM, tier: TierFYI, count: groupDoneCountMax + 25, want: true},
		{name: "merged done direct above cap", kind: KindDM, tier: TierFYI, count: groupDoneCountMax + 25, done: true, merged: true},
		{name: "merged open direct above cap", kind: KindDM, tier: TierFYI, count: groupDoneCountMax + 25, merged: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := admitHub(t, nil)
			addGroupCandidate(h, "candidate", "Example", time.Now().Add(-time.Hour), tc.done)
			it := h.inbox.items["candidate"]
			it.Kind, it.Tier, it.Count = tc.kind, tc.tier, tc.count
			if tc.merged {
				it.MergedInto = "survivor"
			}

			ids := groupCandidateIDs(h.groupEvents("lark", "chat:F1", ""))
			if got := slices.Contains(ids, "candidate"); got != tc.want {
				t.Fatalf("candidate listed = %v, want %v; candidates %v", got, tc.want, ids)
			}
		})
	}
}

// High-count direct-chat events fill the existing independent done quota, without extending its window or size.
func TestInboxGroupEventsDoneDMCountCapKeepsLimits(t *testing.T) {
	h, _ := admitHub(t, nil)
	now := time.Now()
	want := make([]string, 0, groupOpenMax+groupDoneMax)
	for i := 0; i < groupOpenMax; i++ {
		id := fmt.Sprintf("open-%02d", i)
		addGroupCandidate(h, id, "Example", now.Add(-time.Duration(i)*time.Minute), false)
		it := h.inbox.items[id]
		it.Kind, it.Count = KindDM, groupDoneCountMax+i
		want = append(want, id)
	}
	for i := 0; i < groupDoneMax+1; i++ {
		id := fmt.Sprintf("done-%02d", i)
		at := now.Add(-time.Hour - time.Duration(i)*time.Minute)
		addGroupCandidate(h, id, "Example", at, true)
		it := h.inbox.items[id]
		it.Kind, it.Count = KindDM, groupDoneCountMax+i
		if i < groupDoneMax {
			want = append(want, id)
		}
	}
	addGroupCandidate(h, "stale", "Example", now, true)
	stale := h.inbox.items["stale"]
	stale.Kind, stale.Count = KindDM, groupDoneCountMax
	stale.UpdatedAt = now.Add(time.Minute) // Activity cannot retain an event whose filing is outside the window.
	doneAt := now.Add(-groupDoneFor - time.Hour)
	stale.DoneAt = &doneAt

	got := groupCandidateIDs(h.groupEvents("lark", "chat:F1", ""))
	if !slices.Equal(got, want) {
		t.Fatalf("candidates %v, want %v", got, want)
	}
}

// A model may assign messages to a listed high-count direct-chat event; the ordinary write path reopens it.
func TestInboxGroupModelReopensDoneDMAtOrAboveCountCap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
	}{
		{name: "at cap", count: groupDoneCountMax},
		{name: "above cap", count: groupDoneCountMax + 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, h, open, done := groupDMJoinFixture(t, connector.GroupFallbackResult{})
			now := time.Now()
			open.UpdatedAt = now.Add(-time.Hour)
			done.UpdatedAt = now.Add(-30 * time.Minute)
			doneAt := done.UpdatedAt
			done.DoneAt, done.Count, done.Read = &doneAt, tc.count, true

			seen := map[string]groupEvent{}
			for _, event := range h.groupEvents("lark", "chat:F1", "") {
				seen[event.ID] = event
			}
			if _, ok := seen[done.ID]; !ok {
				t.Fatalf("done direct-chat candidate %s is missing: %+v", done.ID, seen)
			}
			var answer groupAnswer
			if err := json.Unmarshal([]byte(`{"assign":[{"message":"m1060","event":"lark:F1:900"},{"message":"m1061","event":"lark:F1:900"}],
				"events":[{"event":"lark:F1:900","title":"Release follow-up","summary":"New timing details","tier":"action","reason":"Needs a reply"}]}`), &answer); err != nil {
				t.Fatal(err)
			}
			plan, err := checkGroupAnswer(answer, g.batch.Group.New, seen)
			if err != nil {
				t.Fatalf("model answer rejected: %v", err)
			}
			h.writeGroup(context.Background(), sourceOf(g), *g.batch, plan)

			if len(g.appends) != 1 || g.appends[0].Event != done.ID || g.appends[0].Continues != "" ||
				!slices.Equal(g.appends[0].Messages, []string{"m1060", "m1061"}) {
				t.Fatalf("appends %+v", g.appends)
			}
			if !done.open() || done.DoneBy != "" || done.Read || done.Title != "Release follow-up" ||
				done.Summary != "New timing details" || done.Tier != TierAction || done.Reason != "Needs a reply" ||
				done.Count != tc.count+2 || done.Last != "1061" {
				t.Fatalf("reopened event %+v", done)
			}
			kinds := chainKinds(done)
			if len(kinds) < 2 || kinds[0] != ChainReceived || kinds[1] != ChainReopened {
				t.Fatalf("chain %v", kinds)
			}
			if len(h.inbox.items) != 2 || h.inbox.items["lark:F1:1060"] != nil {
				t.Fatalf("the model answer opened another event: %v", h.inbox.items)
			}
		})
	}
}

// The direct-chat fallback rewrite uses the same candidates and reopens a recent done event at the count cap.
func TestInboxGroupDMFallbackNewReopensDoneAtCountCap(t *testing.T) {
	g, h, open, done := groupDMJoinFixture(t, connector.GroupFallbackResult{})
	now := time.Now()
	open.UpdatedAt = now.Add(-time.Hour)
	done.UpdatedAt = now.Add(-30 * time.Minute)
	doneAt := done.UpdatedAt
	done.DoneAt, done.Count, done.Read = &doneAt, groupDoneCountMax, true
	done.Summary, done.Reason, done.TriagedBy = "Original summary", "Original reason", "model"
	title := done.Title

	candidates := groupCandidateIDs(h.groupEvents("lark", "chat:F1", ""))
	if !slices.Contains(candidates, done.ID) {
		t.Fatalf("done direct-chat candidate %s is missing: %v", done.ID, candidates)
	}
	plan := &groupPlan{fallbackCandidates: candidates, events: []plannedEvent{{
		messages: []string{"m1060", "m1061"}, title: "Structural title", tier: TierAction,
	}}}
	h.writeGroup(context.Background(), sourceOf(g), *g.batch, plan)

	if len(plan.events) != 1 || plan.events[0].existing != done.ID || !plan.events[0].dmJoined {
		t.Fatalf("rewritten plan %+v", plan)
	}
	if len(g.appends) != 1 || g.appends[0].Event != done.ID ||
		!slices.Equal(g.appends[0].Messages, []string{"m1060", "m1061"}) {
		t.Fatalf("appends %+v", g.appends)
	}
	if !done.open() || done.DoneBy != "" || done.Read || done.Title != title || done.Summary != "Original summary" ||
		done.Tier != TierAction || done.TriagedBy != "model" || done.Count != groupDoneCountMax+2 || done.Last != "1061" {
		t.Fatalf("reopened event %+v", done)
	}
	kinds := chainKinds(done)
	if len(kinds) < 2 || kinds[0] != ChainReceived || kinds[1] != ChainReopened {
		t.Fatalf("chain %v", kinds)
	}
	if open.Count != 1 || open.Last != "1051" || len(h.inbox.items) != 2 || h.inbox.items["lark:F1:1060"] != nil {
		t.Fatalf("the fallback rewrite touched another event: open %+v, items %v", open, h.inbox.items)
	}
}
