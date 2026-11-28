package orcart_test

import (
	"context"
	"testing"

	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
)

// The bots, their chats and their items as Orca 1.4.274 answers bots.roster, bots.chats and bots.items.list: the client
// asks for this computer's bots without their chats, picks the open Home Chat (never a closed earlier one) and keeps an
// item's question with its decisions and quick replies.
func TestBotsReadsRosterChatsAndItems(t *testing.T) {
	f := orcarttest.New(t)
	f.Handle("bots.roster", func(map[string]any) any {
		return map[string]any{"bots": []any{
			map[string]any{"id": "bot-1", "name": "Helper", "agent": "claude", "paused": false, "linked": []any{},
				"specialty": "Tidies the workspace."},
			map[string]any{"id": "", "name": "no id"},
		}}
	})
	f.Handle("bots.chats", func(map[string]any) any {
		return map[string]any{"bot": "Helper", "chats": []any{
			map[string]any{"id": "chat-old", "title": "Home", "lastActivityAt": 100, "home": true, "closed": true},
			map[string]any{"id": "chat-side", "title": "Side", "lastActivityAt": 150, "home": false, "closed": false},
			map[string]any{"id": "chat-home", "title": "Home", "lastActivityAt": 200, "home": true, "closed": false},
		}}
	})
	f.Handle("bots.items.list", func(map[string]any) any {
		return map[string]any{"botId": "bot-1", "botName": "Helper", "counts": map[string]any{"needs_you": 1}, "items": []any{
			map[string]any{"uid": "item-1", "number": 1, "title": "Review the report", "status": "needs_you",
				"chat": map[string]any{"id": "chat-home", "kind": "home", "title": "Home"}, "lastActivityAt": 300,
				"question": map[string]any{"eventId": "e4", "gist": "Done.", "text": "Done.\n\nWhich first?",
					"decisions": []any{map[string]any{"question": "Which first?", "multi": true, "replies": []any{
						map[string]any{"label": "Front end", "detail": "items 1 and 2", "recommended": true},
						map[string]any{"label": "Not now", "recommended": false},
					}}}}},
		}}
	})
	c := &orcart.Client{MetaPath: f.MetaPath}
	ctx := context.Background()

	bots, err := c.Bots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(bots) != 1 || bots[0].BotID != "bot-1" || bots[0].Name != "Helper" || bots[0].Agent != "claude" ||
		bots[0].Specialty != "Tidies the workspace." {
		t.Fatalf("bots %+v", bots)
	}
	if got := f.Trace("bots.roster"); len(got) != 1 || got[0] != `bots.roster {"withoutChats":true}` {
		t.Fatalf("roster call %v", got)
	}

	open, err := c.BotChats(ctx, &bots[0])
	if err != nil {
		t.Fatal(err)
	}
	if bots[0].HomeSessionID != "chat-home" || bots[0].HomeTitle != "Home" || bots[0].LastActiveMs != 200 {
		t.Fatalf("home chat %+v", bots[0])
	}
	if len(open) != 2 || open[0] != "chat-side" || open[1] != "chat-home" {
		t.Fatalf("open chats %v (a closed earlier Home Chat must not count)", open)
	}
	if got := f.Trace("bots.chats"); len(got) != 1 || got[0] != `bots.chats {"botId":"bot-1"}` {
		t.Fatalf("chats call %v", got)
	}

	items, err := c.BotItems(ctx, "bot-1", proto.OrcaItemNeedsYou)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Trace("bots.items.list"); len(got) != 1 || got[0] != `bots.items.list {"botId":"bot-1","limit":50,"statuses":["needs_you"]}` {
		t.Fatalf("items call %v", got)
	}
	if items.BotID != "bot-1" || items.Name != "Helper" || len(items.Items) != 1 {
		t.Fatalf("items %+v", items)
	}
	it := items.Items[0]
	if it.ItemID != "item-1" || it.Status != proto.OrcaItemNeedsYou || !it.Status.Valid() || it.ChatSessionID != "chat-home" ||
		it.LastActiveMs != 300 || it.Question == nil || it.Question.EventID != "e4" || len(it.Question.Decisions) != 1 {
		t.Fatalf("item %+v", it)
	}
	d := it.Question.Decisions[0]
	if d.Question != "Which first?" || !d.Multi || len(d.Replies) != 2 || d.Replies[0].Label != "Front end" ||
		d.Replies[0].Detail != "items 1 and 2" || !d.Replies[0].Recommended || d.Replies[1].Recommended {
		t.Fatalf("decision %+v", d)
	}
}

// An answer goes to the item as its owner, naming the question it answers, with a request id that makes a retry land
// once; nothing is sent for an empty answer.
func TestAnswerBotItemPostsTheOwnersComment(t *testing.T) {
	f := orcarttest.New(t)
	f.Handle("bots.items.owner.comment", func(map[string]any) any { return map[string]any{"eventId": "e5"} })
	c := &orcart.Client{MetaPath: f.MetaPath}
	if err := c.AnswerBotItem(context.Background(), "item-1", "e4", "Front end", "orcaitem_abc"); err != nil {
		t.Fatal(err)
	}
	if err := c.AnswerBotItem(context.Background(), "item-1", "e4", "  ", "orcaitem_abc"); err == nil {
		t.Fatal("an empty answer was accepted")
	}
	got := f.Trace("bots.items.owner.comment")
	if len(got) != 1 || got[0] != `bots.items.owner.comment {"item":"item-1","questionId":"e4","requestId":"orcaitem_abc","text":"Front end"}` {
		t.Fatalf("comment calls %v", got)
	}
}

// A read-only client (the live check against the user's own Orca) may read the bots but never answer an item.
func TestReadOnlyClientReadsBotsButNeverAnswers(t *testing.T) {
	f := orcarttest.New(t)
	f.Handle("bots.roster", func(map[string]any) any { return map[string]any{"bots": []any{}} })
	c := &orcart.Client{MetaPath: f.MetaPath, ReadOnly: true}
	if _, err := c.Bots(context.Background()); err != nil {
		t.Fatalf("read-only roster: %v", err)
	}
	if err := c.AnswerBotItem(context.Background(), "item-1", "e4", "yes", "r"); err == nil {
		t.Fatal("read-only client answered an item")
	}
	if f.Count("bots.items.owner.comment") != 0 {
		t.Fatal("a read-only client wrote to Orca")
	}
}
