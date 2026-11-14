package assistant

import (
	"context"
	"testing"

	"github.com/nwverse/persona/server/internal/connector"
)

// A-7 (4b) on the whole write path: a fallback-opened event whose title repeats the conversation's name as "<where>："
// loses that prefix when something is left; the conversation name reaches where from this batch's chat tag, which is
// why the strip happens on the same write. Only the full-width colon counts, the title is trimmed first, and a match
// that leaves nothing keeps the title as it was.
func TestInboxGroupFallbackTitleDropsTheConversationPrefix(t *testing.T) {
	for _, tc := range []struct {
		name, where, title, want string
	}{
		{name: "stripped", where: "示例群", title: "示例群：周四评审改期", want: "周四评审改期"},
		{name: "only the conversation's name", where: "示例群", title: "示例群", want: "示例群"},
		{name: "nothing after the colon", where: "示例群", title: "示例群：", want: "示例群："},
		{name: "blanks after the colon", where: "示例群", title: "示例群： \n ", want: "示例群："},
		{name: "title trimmed", where: "示例群", title: "  示例群：周四评审改期 ", want: "周四评审改期"},
		{name: "blanks after the colon are dropped with the prefix", where: "示例群", title: "示例群：  周四评审改期", want: "周四评审改期"},
		{name: "halfwidth colon", where: "示例群", title: "示例群: 周四评审改期", want: "示例群: 周四评审改期"},
		{name: "space before the full-width colon", where: "示例群", title: "示例群 ：周四评审改期", want: "示例群 ：周四评审改期"},
		{name: "another conversation's name", where: "示例群", title: "别的群：周四评审改期", want: "别的群：周四评审改期"},
		{name: "the name inside, not leading", where: "示例群", title: "群公告 示例群：周四评审改期", want: "群公告 示例群：周四评审改期"},
		{name: "no conversation", title: "示例群：周四评审改期", want: "示例群：周四评审改期"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			batch := chatBatch()
			batch.Kind = KindGroup
			if tc.where != "" {
				batch.Tags = chatTags(tc.where)
			}
			g := &groupChat{batch: batch, fallback: connector.GroupFallbackResult{
				New: []connector.GroupNew{{Key: "k1", Messages: []string{"m2", "m3"}, Title: tc.title, RuleTier: connector.TierFYI}}}}
			h := chatHub(t, g, nil)
			h.collectInbox(context.Background(), roundPoll)
			h.waitGrouping()

			fresh := h.inbox.items["examplechat:chat:C1#m2"]
			if fresh == nil {
				t.Fatalf("the fallback opened no event: %v", h.inbox.items)
			}
			if fresh.Title != tc.want {
				t.Fatalf("title %q, want %q (event %+v)", fresh.Title, tc.want, fresh)
			}
			if fresh.Where != tc.where {
				t.Fatalf("where %q, want %q", fresh.Where, tc.where)
			}
		})
	}
}

// A fallback event with no title of its own keeps the connector's title, prefix and all: the strip is the fallback
// title's, not every title the write touches.
func TestInboxGroupFallbackTitleWithoutItsOwnTitle(t *testing.T) {
	batch := chatBatch()
	batch.Kind = KindGroup
	batch.Title = "示例群：最新一句"
	batch.Tags = chatTags("示例群")
	g := &groupChat{batch: batch, fallback: connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m2", "m3"}, RuleTier: connector.TierFYI}}}}
	h := chatHub(t, g, nil)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if fresh := h.inbox.items["examplechat:chat:C1#m2"]; fresh == nil || fresh.Title != "示例群：最新一句" ||
		fresh.Where != "示例群" || fresh.TitleBy != TitleByRules {
		t.Fatalf("new event %+v", fresh)
	}
}

// Once an event exists, a later fallback write with a prefixed title does not touch its title (the strip is only for an
// event the fallback opens).
func TestInboxGroupFallbackTitleKeptOnLaterWrites(t *testing.T) {
	batch := chatBatch()
	batch.Kind = KindGroup
	batch.Tags = chatTags("示例群")
	g := &groupChat{batch: batch, fallback: connector.GroupFallbackResult{
		New: []connector.GroupNew{{Key: "k1", Messages: []string{"m2", "m3"}, Title: "示例群：周四评审改期", RuleTier: connector.TierFYI}}}}
	h := chatHub(t, g, nil)
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()
	fresh := h.inbox.items["examplechat:chat:C1#m2"]
	if fresh == nil || fresh.Title != "周四评审改期" {
		t.Fatalf("new event %+v", fresh)
	}

	// A second batch that the fallback assigns to that event takes only its messages.
	next := chatBatch()
	next.Kind = KindGroup
	next.Tags = chatTags("示例群")
	next.Group = &connector.ThingGroup{Batch: "chat:C1@4", New: []string{"m4"}}
	next.Last = "4"
	next.Context = "示例群\n[10:07] 丁：示例 C 的事 (id=m4)"
	g.mu.Lock()
	g.batch, g.fallback = next, connector.GroupFallbackResult{
		Assign: []connector.GroupAssign{{Message: "m4", Event: "examplechat:chat:C1#m2"}}}
	g.mu.Unlock()
	h.collectInbox(context.Background(), roundPoll)
	h.waitGrouping()

	if fresh.Title != "周四评审改期" || fresh.Count != 3 || fresh.Last != "4" {
		t.Fatalf("event after a later write %+v", fresh)
	}
}
