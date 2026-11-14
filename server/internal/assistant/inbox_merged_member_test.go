package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// addMergedGroupMember adds a hidden member whose survivor is the open event in groupHub. Like a row merged by hand,
// the member has not been filed, so its DoneAt is nil even though open is false.
func addMergedGroupMember(h *inboxHub) *InboxItem {
	survivor := h.inbox.items["lark:F1:1051"]
	now := time.Now()
	member := &InboxItem{
		ID: "lark:F1:1040", Source: "lark", Kind: KindGroup, Title: "Merged member", Summary: "Hidden member summary",
		Tier: TierAction, Reason: "Example", Last: "1040", TriagedAt: "1040", TriagedBy: "model", Count: 1,
		StartedAt: now.Add(-2 * time.Hour), UpdatedAt: now, Context: "Merged member context",
		Meta: json.RawMessage(`{"messages":["m1040"]}`), Group: &InboxGroup{Connector: "lark", Thread: "chat:F1"},
		Lark: &InboxLark{AccountID: 1, FeedID: "F1", IsGroup: true, StartPosition: "1040"}, MergedInto: survivor.ID,
	}
	survivor.MergedFrom = append(survivor.MergedFrom, InboxMergeRef{ID: member.ID, Source: member.Source, Title: member.Title})
	h.inbox.items[member.ID] = member
	return member
}

// A merged member is hidden from both grouping paths. In particular, its nil DoneAt is never used as a done event's
// timestamp, and neither the model nor the connector can assign new messages to it as a listed candidate.
func TestInboxGroupSkipsMergedMember(t *testing.T) {
	for _, tc := range []struct {
		name      string
		modelDown bool
	}{
		{name: "model"},
		{name: "fallback", modelDown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &groupLark{batch: groupBatch(), placed: map[string]bool{}, fallback: connector.GroupFallbackResult{
				Assign: []connector.GroupAssign{
					{Message: "m1060", Event: "lark:F1:1051"},
					{Message: "m1061", Event: "lark:F1:1051"},
				},
			}}
			model, client := newGroupModel(t, groupAnswerOK)
			model.down = tc.modelDown
			h, _ := groupHub(t, g, client)
			member := addMergedGroupMember(h)

			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			for _, event := range h.groupEvents("lark", "chat:F1", "") {
				if event.ID == member.ID {
					t.Fatalf("merged member is a candidate: %+v", event)
				}
			}
			if tc.modelDown {
				if len(g.fallbacks) != 1 {
					t.Fatalf("%d fallback calls, want 1", len(g.fallbacks))
				}
				for _, event := range g.fallbacks[0].Events {
					if event.ID == member.ID {
						t.Fatalf("merged member was supplied to the fallback: %+v", event)
					}
				}
			} else {
				calls := model.asked()
				if len(calls) != 1 {
					t.Fatalf("%d model calls, want 1", len(calls))
				}
				if strings.Contains(calls[0].User, member.ID) {
					t.Fatalf("merged member was supplied to the model:\n%s", calls[0].User)
				}
				if len(g.fallbacks) != 0 {
					t.Fatalf("model path used the fallback: %+v", g.fallbacks)
				}
			}
			for _, a := range g.appends {
				if a.Event == member.ID {
					t.Fatalf("grouping appended to the merged member: %+v", a)
				}
			}
			if member.DoneAt != nil || member.MergedInto != "lark:F1:1051" {
				t.Fatalf("merged member changed while grouping another event: %+v", member)
			}
		})
	}
}

// If an append already aimed at a member arrives, putEvent does not treat its nil DoneAt as a filing to reopen.
// writeGroup's followMerged step instead gives the activity to the survivor and reopens that row.
func TestInboxGroupPutEventDoesNotReopenMergedMember(t *testing.T) {
	g := &groupLark{placed: map[string]bool{}}
	h, _ := groupHub(t, g, nil)
	member := addMergedGroupMember(h)
	survivor := h.inbox.items["lark:F1:1051"]
	survivor.done(time.Now().Add(-time.Minute), "user")
	batch := groupBatch()
	batch.Group.New = []string{"m1060"}
	plan := &groupPlan{events: []plannedEvent{{existing: member.ID, messages: []string{"m1060"}}}}

	h.writeGroup(context.Background(), sourceOf(g), *batch, plan)

	if len(g.appends) != 1 || g.appends[0].Event != member.ID {
		t.Fatalf("appends %+v, want the forced member append", g.appends)
	}
	if member.DoneAt != nil || slices.Contains(chainKinds(member), ChainReopened) {
		t.Fatalf("merged member was reopened: %+v, chain %v", member, chainKinds(member))
	}
	if !survivor.open() || survivor.DoneBy != "" {
		t.Fatalf("survivor did not take the member's activity: %+v", survivor)
	}
}

// A merged member has no filing timestamp of its own. Asking for message-card filing times must not dereference its
// nil DoneAt; genuinely filed rows still report their timestamp.
func TestInboxFiledAtSkipsMergedMember(t *testing.T) {
	h, _ := admitHub(t, nil)
	h.collectInbox(context.Background(), roundPoll)
	doneAt := time.Now().Add(-time.Hour)
	h.inbox.mu.Lock()
	for _, item := range []*InboxItem{
		{ID: "survivor"},
		{ID: "member", MergedInto: "survivor"},
		{ID: "done", DoneAt: &doneAt},
		{ID: "open"},
	} {
		h.inbox.items[item.ID] = item
	}
	h.inbox.mu.Unlock()

	got := h.inboxFiledAt(context.Background(), []string{"member", "done", "open", "missing"})
	if len(got) != 1 || !got["done"].Equal(doneAt) {
		t.Fatalf("filed times %v, want only done at %v", got, doneAt)
	}
}
