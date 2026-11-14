package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
)

// chatTags is the tag a chat connector marks a thing with: its chat and the name people read (a group's name, the
// other person of a direct chat).
func chatTags(name string) []connector.Tag {
	return []connector.Tag{{Key: connector.TagChat, Value: "oc_chat", Name: name}}
}

// fillMigrated takes where a chat thing happens from its chat tag's name, or else its repository tag's name or value,
// on every write: a group's name or the other person of a direct chat, trimmed. A thing that names neither (a
// document), a chat tag with a blank name and no repository, and any other tag leave the item's where as it was; a
// renamed chat follows on the next move.
func TestFillMigratedWhere(t *testing.T) {
	for _, c := range []struct {
		name  string
		start string
		tags  []connector.Tag
		want  string
	}{
		{"a group", "", chatTags("Example Group"), "Example Group"},
		{"a direct chat's other person", "", chatTags("Alice"), "Alice"},
		{"trimmed", "", chatTags("  Example Group \n"), "Example Group"},
		{"renamed", "Old Name", chatTags("New Name"), "New Name"},
		{"no tags keeps it", "Example Group", nil, "Example Group"},
		{"a blank name keeps it", "Example Group", chatTags(""), "Example Group"},
		{"a spaces-only name keeps it", "Example Group", chatTags("   "), "Example Group"},
		{"a chat tag without a name keeps it", "Example Group", []connector.Tag{{Key: connector.TagChat, Value: "oc_chat"}}, "Example Group"},
		{"a repository's name", "", []connector.Tag{{Key: connector.TagRepo, Value: "example-org/example-repo", Name: "Example repository"}},
			"Example repository"},
		{"a repository's value", "", []connector.Tag{{Key: connector.TagRepo, Value: "example-org/example-repo"}}, "example-org/example-repo"},
		{"a blank chat name falls back to the repository", "Old Name", append(chatTags(" "),
			connector.Tag{Key: connector.TagRepo, Value: "example-org/example-repo"}), "example-org/example-repo"},
		{"another tag keeps it", "Example Group", []connector.Tag{{Key: connector.TagSender, Value: "alice", Name: "Alice"}}, "Example Group"},
	} {
		t.Run(c.name, func(t *testing.T) {
			it := &InboxItem{ID: "lark:10:5", Source: "lark", Where: c.start}
			fillMigrated(it, connector.InboxThing{Thread: "chat:10", Kind: KindDM, Title: "Latest message", Last: "5", Tags: c.tags,
				Extra: &connector.ThingExtra{Source: "lark", Count: 1}})
			if it.Where != c.want {
				t.Fatalf("where %q, want %q", it.Where, c.want)
			}
		})
	}
}

// A chat a plugin took over comes in with the chat's name as where; a rename follows it on the next move; a document's
// new item has none. Where is listed (an app reads it) and absent from the JSON when empty, so items from before and
// mail keep the shape they had.
func TestMigratedInboxWhere(t *testing.T) {
	ctx := context.Background()
	h := newInboxHub(t)
	if err := h.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	p := &extensionLark{}
	h.addConnector(p, false)
	now := time.Now()
	larkExtra := func(last string) *connector.ThingExtra {
		return &connector.ThingExtra{Source: "lark", Account: "feishu", AccountName: "飞书", LegacyID: "lark:10:" + last, Count: 1,
			StartedAt: now, Lark: rawJSON(InboxLark{AccountID: 1, FeedID: "10", StartPosition: last})}
	}
	chat := func(last, name string) connector.InboxThing {
		return connector.InboxThing{Thread: "chat:10", Kind: KindDM, Title: "Alice", At: now, Last: last,
			Tags: chatTags(name), Extra: larkExtra(last)}
	}
	p.things = []connector.InboxThing{chat("5", "Example Group")}
	h.collectInbox(ctx, roundPoll)
	it := h.inbox.items["lark:10:5"]
	if it == nil || it.Where != "Example Group" {
		t.Fatalf("item %+v", it)
	}
	listed, _ := json.Marshal(it.listed())
	if !strings.Contains(string(listed), `"where":"Example Group"`) {
		t.Fatalf("where is not listed: %s", listed)
	}

	// The chat is renamed in the source: the next move carries the new name.
	p.things[0] = chat("6", "Renamed Group")
	h.collectInbox(ctx, roundPoll)
	if it = h.inbox.items["lark:10:5"]; it == nil || it.Last != "6" || it.Where != "Renamed Group" {
		t.Fatalf("a rename did not follow: %+v", it)
	}

	// A document's thing names no chat: no where, and none in its JSON.
	p.things = append(p.things, connector.InboxThing{Thread: "doc:n1", Kind: KindDoc, Title: "周报", At: now, Last: "100",
		Extra: &connector.ThingExtra{Source: "lark", Account: "feishu", AccountName: "飞书", LegacyID: "doc:n1:100", Count: 1,
			StartedAt: now, Doc: rawJSON(InboxDoc{NoticeID: "n1", Type: "sheet"})}})
	h.collectInbox(ctx, roundPoll)
	doc := h.inbox.items["doc:n1:100"]
	if doc == nil || doc.Where != "" {
		t.Fatalf("doc item %+v", doc)
	}
	listed, _ = json.Marshal(doc.listed())
	if strings.Contains(string(listed), `"where"`) {
		t.Fatalf("an empty where is listed: %s", listed)
	}

	// Mail never has one (its row reads sender and subject).
	mail, _ := json.Marshal(mailItem("m1", "shop@example.com", "示例商城").listed())
	if strings.Contains(string(mail), `"where"`) {
		t.Fatalf("mail lists a where: %s", mail)
	}
}

// The mute menu's chat entry is named by where the item happens (the chat's name), the title only when there is none:
// what it mutes by (tag and value) does not change.
func TestMutesOfWhere(t *testing.T) {
	it := &InboxItem{Source: "lark", Title: "Latest message", Where: "Example Group", Lark: &InboxLark{FeedID: "f1"}}
	if got := fmt.Sprint(mutesOf(it)); got != "[{chat f1 Example Group}]" {
		t.Fatalf("mutes: %s", got)
	}
	it.Where = ""
	if got := fmt.Sprint(mutesOf(it)); got != "[{chat f1 Latest message}]" {
		t.Fatalf("without a where the title names the chat: %s", got)
	}
}

// GET /api/v1/inbox lists where, and the mute menu it sends names the chat by where, not by the newest message's title.
// Muting by that entry still mutes the chat's id, as before.
func TestInboxWhereListed(t *testing.T) {
	h := muteHub(t)
	it := &InboxItem{ID: "lark:F1:5", Source: "lark", Kind: KindGroup, Title: "示例 A 周四下午可以", Where: "Example Group",
		UpdatedAt: time.Now(), Last: "5", Count: 1, Lark: &InboxLark{FeedID: "F1", IsGroup: true, SenderID: "u1"}, Sender: "丙"}
	h.inbox.mu.Lock()
	h.inbox.items[it.ID] = it
	h.inbox.mu.Unlock()
	mux := h.routes()

	code, out := inboxCall(t, mux, "GET", "/api/v1/inbox", "")
	if code != 200 {
		t.Fatalf("GET /api/v1/inbox: %d", code)
	}
	raw, _ := json.Marshal(out["items"])
	for _, want := range []string{`"where":"Example Group"`, `"name":"Example Group","tag":"chat","value":"F1"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("the list does not carry %s: %s", want, raw)
		}
	}
	if code, _ := inboxCall(t, mux, "POST", "/api/v1/inbox/mutes", `{"item_id":"`+it.ID+`","tag":"chat"}`); code != 200 {
		t.Fatalf("muting the chat entry: %d", code)
	}
	if _, out := inboxCall(t, mux, "GET", "/api/v1/inbox/mutes", ""); !strings.Contains(fmt.Sprint(out["mutes"]), "Example Group") {
		t.Fatalf("the mute kept another name: %v", out["mutes"])
	}
}

// A chat renamed in the source is listed under its new name as soon as its next thing arrives — including a thing that
// only moves its read position (the same last message, a new read marker).
func TestMigratedRenameOnABlockOnlyWrite(t *testing.T) {
	ctx := context.Background()
	h := newInboxHub(t)
	if err := h.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	p := &extensionLark{}
	h.addConnector(p, false)
	now := time.Now()
	thing := func(name, replyTo string) connector.InboxThing {
		return connector.InboxThing{Thread: "chat:10", Kind: KindDM, Title: "Alice", At: now, Last: "5", Tags: chatTags(name),
			Extra: &connector.ThingExtra{Source: "lark", Account: "feishu", AccountName: "飞书", LegacyID: "lark:10:5", Count: 1,
				StartedAt: now, Lark: rawJSON(InboxLark{AccountID: 1, FeedID: "10", StartPosition: "5", ReplyTo: replyTo})}}
	}
	p.things = []connector.InboxThing{thing("Example Group", "")}
	h.collectInbox(ctx, roundPoll)
	if it := h.inbox.items["lark:10:5"]; it == nil || it.Where != "Example Group" {
		t.Fatalf("first write: %+v", it)
	}
	p.things[0] = thing("Renamed Group", "m5") // same last message, another block (read further in the chat)
	h.collectInbox(ctx, roundPoll)
	if it := h.inbox.items["lark:10:5"]; it == nil || it.Lark == nil || it.Lark.ReplyTo != "m5" || it.Where != "Renamed Group" {
		t.Fatalf("a rename on a blocks-only write did not follow: where %q, block %+v", it.Where, it.Lark)
	}
}

// A generic connector's item takes where from its tags as it comes in and as it moves on (spec/fixtures/inbox/row.json
// "repo_where": a repository's name, or its id when it has no name), and lists it; a thing that drops its tags keeps
// the place it had.
func TestConnectorItemWhere(t *testing.T) {
	h := newInboxHub(t)
	now := time.Now()
	apply := func(tags []connector.Tag, last string) *InboxItem {
		h.inbox.mu.Lock()
		defer h.inbox.mu.Unlock()
		th := thing(last, now)
		th.Tags = tags
		h.inbox.applyConnector("example", "Example", []connector.InboxThing{th}, now)
		return openItems(h)[0]
	}
	repo := func(name string) []connector.Tag {
		return []connector.Tag{{Key: connector.TagRepo, Value: "example-org/example-repo", Name: name}}
	}

	it := apply(repo(""), "a")
	if it.Where != "example-org/example-repo" {
		t.Fatalf("a repository's id: where %q", it.Where)
	}
	listed, _ := json.Marshal(it.listed())
	if !strings.Contains(string(listed), `"where":"example-org/example-repo"`) {
		t.Fatalf("the list does not carry where: %s", listed)
	}
	if it = apply(repo("Example repository"), "b"); it.Where != "Example repository" {
		t.Fatalf("a repository's name: where %q", it.Where)
	}
	if it = apply(nil, "c"); it.Where != "Example repository" {
		t.Fatalf("a move without tags dropped it: where %q", it.Where)
	}
}

// Grouped events take where on the model, fallback and done-event writes too; fallback title preservation does not
// restore an old chat name, and the listed JSON carries the current one.
func TestPutEventWhere(t *testing.T) {
	h := newInboxHub(t)
	source := assistantwire.InboxSource{ID: "lark", Name: "Chat"}
	thing := connector.InboxThing{ID: "lark:10:5", Thread: "chat:10", Kind: KindGroup, At: time.Now(), Tags: chatTags("Example Group"),
		Extra: &connector.ThingExtra{Source: "lark", Count: 1, Lark: rawJSON(InboxLark{FeedID: "10", IsGroup: true, StartPosition: "5"})}}
	existing := ""
	write := func(last, name, title string, byModel bool) *InboxItem {
		thing.Last, thing.Title, thing.Tags = last, title, chatTags(name)
		it := h.putEvent(source, thing.Thread, plannedEvent{existing: existing, messages: []string{last}, title: title, tier: TierAction}, thing, byModel)
		existing = thing.ID
		return it
	}
	it := write("5", "Example Group", "Model title", true)
	if it == nil || it.Where != "Example Group" {
		t.Fatalf("model event: %+v", it)
	}
	listed, _ := json.Marshal(it.listed())
	if !strings.Contains(string(listed), `"where":"Example Group"`) {
		t.Fatalf("grouped event is not listed with where: %s", listed)
	}
	it = write("6", "Renamed Group", "Fallback title", false)
	if it == nil || it.Where != "Renamed Group" || it.Title != "Model title" {
		t.Fatalf("fallback event: %+v", it)
	}
	done := time.Now()
	it.DoneAt, it.DoneBy = &done, "user"
	it = write("7", "Latest Group", "Reopened title", true)
	if it == nil || it.Where != "Latest Group" {
		t.Fatalf("done event's next write: %+v", it)
	}
}

// A generic connector names its chat, or else its repository by name or id; chat wins and no usable tag keeps where.
func TestFillConnectorItemWhere(t *testing.T) {
	repo := []connector.Tag{{Key: connector.TagRepo, Value: "example-org/example-repo", Name: "Example repository"}}
	for _, tc := range []struct {
		name, start, want string
		tags              []connector.Tag
	}{
		{"chat", "", "Example Group", chatTags("Example Group")},
		{"repository name", "", "Example repository", repo},
		{"repository value", "", "example-org/example-repo", []connector.Tag{{Key: connector.TagRepo, Value: "example-org/example-repo"}}},
		{"chat before repository", "", "Example Group", append(chatTags("Example Group"), repo...)},
		{"none keeps it", "Existing place", "Existing place", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			it := &InboxItem{Where: tc.start}
			fillConnectorItem(it, connector.InboxThing{Tags: tc.tags})
			if it.Where != tc.want {
				t.Fatalf("where = %q, want %q", it.Where, tc.want)
			}
		})
	}
}
