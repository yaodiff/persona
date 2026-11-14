package assistant

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// The complete fallback path must offer and reopen a recent high-count direct-chat event. Against the pre-fix
// predicate this fails before the append: the done event is withheld from group.fallback and the remaining open event
// becomes joinDMFallback's target instead.
func TestInboxGroupDMFallbackReviewReopensDoneAtAndAboveCountCap(t *testing.T) {
	for _, count := range []int{groupDoneCountMax, groupDoneCountMax + 25} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			g, h, open, done := groupDMJoinFixture(t, connector.GroupFallbackResult{New: []connector.GroupNew{{
				Key: "follow-up", Messages: []string{"m1060", "m1061"}, RuleTier: connector.TierAction,
			}}})
			now := time.Now()
			open.UpdatedAt = now.Add(-time.Hour)
			done.UpdatedAt = now.Add(-30 * time.Minute)
			doneAt := done.UpdatedAt
			done.DoneAt, done.DoneBy, done.Count, done.Read = &doneAt, "user", count, true

			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			if len(g.fallbacks) != 1 {
				t.Fatalf("fallback calls = %d, want 1", len(g.fallbacks))
			}
			offered := false
			for _, event := range g.fallbacks[0].Events {
				offered = offered || event.ID == done.ID && event.Done
			}
			if !offered {
				t.Errorf("done direct-chat event %s was not offered to fallback: %+v", done.ID, g.fallbacks[0].Events)
			}
			if len(g.appends) != 1 || g.appends[0].Event != done.ID ||
				!slices.Equal(g.appends[0].Messages, []string{"m1060", "m1061"}) {
				t.Fatalf("appends = %+v, want both messages appended to %s", g.appends, done.ID)
			}
			if !done.open() || done.DoneBy != "" || done.Read || done.Count != count+2 || done.Last != "1061" ||
				done.Tier != TierAction {
				t.Fatalf("reopened done event = %+v", done)
			}
			if open.Count != 1 || open.Last != "1051" || len(h.inbox.items) != 2 || h.inbox.items["lark:F1:1060"] != nil {
				t.Fatalf("fallback touched another event: open = %+v, items = %v", open, h.inbox.items)
			}
			if kinds := chainKinds(done); len(kinds) < 2 || kinds[0] != ChainReceived || kinds[1] != ChainReopened {
				t.Fatalf("done event chain = %v", kinds)
			}
		})
	}
}
