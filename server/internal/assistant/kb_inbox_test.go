package assistant

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kb"
)

// A knowledge base chat's Inbox items and an item's chat (kb_inbox.go, #15356). Names and texts are placeholders.

// kbInboxFixture is an assistant with a knowledge base holding one connector's chat (larkx-c1) and a Feishu chat
// (lark-f1), with an Inbox the test fills.
type kbInboxFixture struct {
	t      *testing.T
	s      *Service
	e      *kb.Engine
	at     time.Time // a minute two hours ago, the messages' time
	zone   *time.Location
	chat   *kb.Chat
	feishu *kb.Chat
}

func newKBInboxFixture(t *testing.T) *kbInboxFixture {
	t.Helper()
	s := newKBService(t, newFakeHub())
	e := kbEng(t, s)
	f := &kbInboxFixture{t: t, s: s, e: e, at: time.Now().Add(-2 * time.Hour).Truncate(time.Minute).UTC()}
	f.chat = &kb.Chat{ID: kb.ChatID("larkx", "c1"), Source: "larkx", SourceID: "c1", Connector: "larkx", Kind: "group", Name: "Release group"}
	f.feishu = &kb.Chat{ID: kb.ChatID("lark", "f1"), Source: "lark", SourceID: "f1", Kind: "group", Name: "Design group"}
	for _, c := range []*kb.Chat{f.chat, f.feishu} {
		if err := e.Store.PutChat(c); err != nil {
			t.Fatal(err)
		}
	}
	f.zone = e.Store.ChatZone(f.chat.ID)
	s.inbox.init()
	s.inbox.loaded.Store(true)
	return f
}

func (f *kbInboxFixture) add(items ...*InboxItem) {
	for _, it := range items {
		f.s.inbox.items[it.ID] = it
	}
}

func (f *kbInboxFixture) write(c *kb.Chat, lines ...kb.Line) {
	f.t.Helper()
	if err := f.e.Store.AppendMessages(c, lines); err != nil {
		f.t.Fatal(err)
	}
}

// get answers path for a device of the given role ("": the user's own app, as tests ask).
func (f *kbInboxFixture) get(path, role string) map[string]json.RawMessage {
	f.t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if role != "" {
		req.Header.Set(RoleHeader, role)
	}
	if role == roleExtension {
		req.Header.Set(ExtensionHeader, "example-extension")
	}
	rec := httptest.NewRecorder()
	f.s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		f.t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *kbInboxFixture) inbox(chat, role string) ([]kbChatInboxItem, bool) {
	f.t.Helper()
	raw, ok := f.get("/api/v1/kb/chats/"+chat, role)["inbox"]
	if !ok {
		return nil, false
	}
	var list []kbChatInboxItem
	if err := json.Unmarshal(raw, &list); err != nil {
		f.t.Fatal(err)
	}
	return list, true
}

// line is the row a recent text has for a message at at.
func (f *kbInboxFixture) line(at time.Time, sender, text string) kbChatInboxLine {
	l := kb.Line{At: at, Sender: sender, Text: text}.Format(f.zone)
	return kbChatInboxLine{Day: at.In(f.zone).Format("2006-01-02"), Line: l[:len(l)-1]}
}

func kbReceived(at time.Time, sender string, texts ...string) InboxChainEntry {
	e := InboxChainEntry{Kind: ChainReceived, At: at, Sender: sender, Count: len(texts)}
	for _, x := range texts {
		e.Messages = append(e.Messages, InboxChainMessage{Sender: sender, Text: x})
	}
	return e
}

func kbConnectorItem(id, source, thread string, at time.Time) *InboxItem {
	return &InboxItem{ID: id, Source: source, Kind: KindGroup, Title: "Release group", StartedAt: at, UpdatedAt: at,
		Connector: &InboxConnector{Thread: thread}}
}

func kbInboxIDs(list []kbChatInboxItem) []string {
	out := []string{}
	for _, x := range list {
		out = append(out, x.ID)
	}
	return out
}

// The chat's page lists the items of its connector and thread, open ones first and each group newest first, with
// their tier and done state; another connector's thread of the same id, another thread, a row merged into another
// (its survivor is listed instead) and an item done longer ago than the Inbox lists stay out.
func TestKBChatInboxJoin(t *testing.T) {
	f := newKBInboxFixture(t)
	at := f.at
	now := time.Now().UTC()
	done := now.Add(-time.Hour)
	old := now.Add(-inboxKeepDone - time.Hour)

	open := kbConnectorItem("larkx:c1:2", "larkx", "c1", at)
	open.Tier, open.Summary = TierAction, "Asks for a review"
	older := kbConnectorItem("larkx:c1:1", "larkx", "c1", at.Add(-time.Hour))
	older.Tier = TierFYI
	filed := kbConnectorItem("larkx:c1:0", "larkx", "c1", at.Add(time.Minute))
	filed.Tier, filed.DoneAt, filed.DoneBy = TierNoise, &done, "user"
	gone := kbConnectorItem("larkx:c1:old", "larkx", "c1", at.Add(-2*time.Hour))
	gone.DoneAt, gone.DoneBy = &old, "user"
	member := kbConnectorItem("larkx:c1:m", "larkx", "c1", at.Add(-3*time.Hour))
	survivor := kbConnectorItem("larkx:c9:s", "larkx", "c9", at.Add(-90*time.Minute))
	survivor.Tier, survivor.MergedFrom = TierAction, []InboxMergeRef{{ID: member.ID}}
	member.MergedInto = survivor.ID
	f.add(open, older, filed, gone, member, survivor,
		kbConnectorItem("github:c1", "github", "c1", at),      // another connector, the same thread id
		kbConnectorItem("larkx:c2:1", "larkx", "c2", at),      // another thread
		kbConnectorItem("larkx:c1:x", "larkx", "chat:c1", at)) // not the chat's source id

	list, ok := f.inbox(f.chat.ID, "")
	if !ok {
		t.Fatal("no inbox on the chat's page")
	}
	if got, want := kbInboxIDs(list), []string{open.ID, older.ID, survivor.ID, filed.ID}; !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	if x := list[0]; x.Tier != TierAction || x.Status != StatusAction || x.Summary != "Asks for a review" || x.DoneAt != nil || !x.UpdatedAt.Equal(at) {
		t.Fatalf("the open item: %+v", x)
	}
	if x := list[3]; x.Tier != TierNoise || x.DoneAt == nil || !x.DoneAt.Equal(done) || x.Status != "" {
		t.Fatalf("the done item: %+v", x)
	}

	// The Feishu chat's page lists its Feishu items (the chat the AI twin reads for them), and only those.
	lark := &InboxItem{ID: "lark:f1:1", Source: "lark", Kind: KindGroup, Title: "Design group", UpdatedAt: at, Lark: &InboxLark{FeedID: "f1", IsGroup: true}}
	f.add(lark, &InboxItem{ID: "lark:f2:1", Source: "lark", Kind: KindGroup, UpdatedAt: at, Lark: &InboxLark{FeedID: "f2", IsGroup: true}})
	if list, _ := f.inbox(f.feishu.ID, ""); !slices.Equal(kbInboxIDs(list), []string{lark.ID}) {
		t.Fatalf("the Feishu chat lists %v", kbInboxIDs(list))
	}
}

// A chat with no Inbox items, and any chat asked by an extension's token (which reads the Inbox by its own scope),
// gets no inbox at all; neither does an item's page asked that way get its chat.
func TestKBChatInboxEmptyAndVisibility(t *testing.T) {
	f := newKBInboxFixture(t)
	if _, ok := f.inbox(f.chat.ID, ""); ok {
		t.Fatal("a chat with no Inbox items has an inbox")
	}
	f.add(kbConnectorItem("larkx:c2:1", "larkx", "c2", f.at))
	if _, ok := f.inbox(f.chat.ID, ""); ok {
		t.Fatal("another thread's item made an inbox")
	}
	it := kbConnectorItem("larkx:c1:1", "larkx", "c1", f.at)
	f.add(it)
	if list, ok := f.inbox(f.chat.ID, ""); !ok || len(list) != 1 {
		t.Fatalf("the user's app: %v %v", list, ok)
	}
	for _, role := range []string{roleExtension, roleAnswer} {
		if list, ok := f.inbox(f.chat.ID, role); ok {
			t.Fatalf("%s: %v", role, list)
		}
		if _, ok := f.get("/api/v1/inbox/"+it.ID, role)["kb_chat"]; ok {
			t.Fatalf("%s: the item names its chat", role)
		}
	}
}

// An item's page names its knowledge base chat, by its connector's thread or by the chat kbChatOf names, with the
// chat page's path; an item whose chat the knowledge base doesn't have names none.
func TestInboxItemKBChat(t *testing.T) {
	f := newKBInboxFixture(t)
	it := kbConnectorItem("larkx:c1:1", "larkx", "c1", f.at)
	lark := &InboxItem{ID: "lark:f1:1", Source: "lark", Kind: KindGroup, UpdatedAt: f.at, Lark: &InboxLark{FeedID: "f1", IsGroup: true}}
	none := kbConnectorItem("larkx:c3:1", "larkx", "c3", f.at)
	other := kbConnectorItem("github:c1", "github", "c1", f.at) // the chat larkx-c1 is not its connector's
	f.add(it, lark, none, other)
	chat := func(id string) *inboxKBChat {
		t.Helper()
		raw, ok := f.get("/api/v1/inbox/"+id, "")["kb_chat"]
		if !ok {
			return nil
		}
		var c inboxKBChat
		if err := json.Unmarshal(raw, &c); err != nil {
			t.Fatal(err)
		}
		return &c
	}
	if c := chat(it.ID); c == nil || c.ID != "larkx-c1" || c.Path != "/kb/chats/larkx-c1" {
		t.Fatalf("the connector's item: %+v", c)
	}
	if c := chat(lark.ID); c == nil || c.ID != "lark-f1" || c.Path != "/kb/chats/lark-f1" {
		t.Fatalf("the Feishu item: %+v", c)
	}
	for _, id := range []string{none.ID, other.ID} {
		if c := chat(id); c != nil {
			t.Fatalf("%s names %+v", id, c)
		}
	}
}

// A chat its connector files under another name (an archive source of its manifest, connectorwire.ChatBatch.Source)
// is still its: the chat's page lists the connector's items of that thread, and such an item names the chat. A chat
// of the same thread another connector wrote, under the same name, is neither; nor is a chat of another id, since
// nothing in the connector protocol relates an Inbox thread to an archive chat of another id (only kbChatOf does, for
// the sources it knows).
func TestKBChatInboxArchiveSourceAlias(t *testing.T) {
	f := newKBInboxFixture(t)
	alias := &kb.Chat{ID: kb.ChatID("larkx-posts", "p9"), Source: "larkx-posts", SourceID: "p9", Connector: "larkx", Kind: "group", Name: "Posts"}
	decoy := &kb.Chat{ID: kb.ChatID("feeds", "q7"), Source: "feeds", SourceID: "q7", Connector: "feedsx", Kind: "group", Name: "Feed"}
	elsewhere := &kb.Chat{ID: kb.ChatID("larkx", "room-r5"), Source: "larkx", SourceID: "room-r5", Connector: "larkx", Kind: "group", Name: "Room"}
	for _, c := range []*kb.Chat{alias, decoy, elsewhere} {
		if err := f.e.Store.PutChat(c); err != nil {
			t.Fatal(err)
		}
	}
	it := kbConnectorItem("larkx:p9:1", "larkx", "p9", f.at)
	notDecoy := kbConnectorItem("larkx:q7:1", "larkx", "q7", f.at) // feeds-q7 is feedsx's
	notElsewhere := kbConnectorItem("larkx:r5:1", "larkx", "r5", f.at)
	f.add(it, notDecoy, notElsewhere)
	if list, _ := f.inbox(alias.ID, ""); !slices.Equal(kbInboxIDs(list), []string{it.ID}) {
		t.Fatalf("the alias chat's page lists %v", kbInboxIDs(list))
	}
	for _, c := range []*kb.Chat{decoy, elsewhere} {
		if list, _ := f.inbox(c.ID, ""); len(list) != 0 {
			t.Fatalf("%s lists %v", c.ID, kbInboxIDs(list))
		}
	}
	raw, ok := f.get("/api/v1/inbox/"+it.ID, "")["kb_chat"]
	var c inboxKBChat
	if !ok || json.Unmarshal(raw, &c) != nil || c.ID != alias.ID || c.Path != "/kb/chats/"+alias.ID {
		t.Fatalf("the item names %s", raw)
	}
	for _, x := range []*InboxItem{notDecoy, notElsewhere} {
		if raw, ok := f.get("/api/v1/inbox/"+x.ID, "")["kb_chat"]; ok {
			t.Fatalf("%s names %s", x.ID, raw)
		}
	}
}

// The rows of the chat's recent messages an item received link to it: the same sender and text (a cut one by its
// start) within the minutes the item's chain says they came in; another sender, another text and a time outside
// stay unlinked.
func TestKBChatInboxMessageRows(t *testing.T) {
	f := newKBInboxFixture(t)
	t0, t1 := f.at.Add(-time.Minute), f.at
	long := "A long message that goes on well past what the Inbox keeps of one, so its chain holds its start"
	f.write(f.chat,
		kb.Line{At: t0, Sender: "Alice", Text: "First of two"},
		kb.Line{At: t1, Sender: "Alice", Text: "Second of two\nwith a second line"},
		kb.Line{At: t1, Sender: "Bob", Text: "Second of two"},                        // another sender
		kb.Line{At: t1, Sender: "Alice", Text: "Something else"},                     // another text
		kb.Line{At: t1.Add(10 * time.Minute), Sender: "Alice", Text: "First of two"}, // after the batch
		kb.Line{At: t1.Add(20 * time.Minute), Sender: "Cara", Text: long},
		kb.Line{At: t1.Add(30 * time.Minute), Sender: "Dan", Text: "At its own time"},
	)
	it := kbConnectorItem("larkx:c1:1", "larkx", "c1", t0)
	it.Chain = []InboxChainEntry{kbReceived(t1, "Alice", "First of two", "Second of two with a second line")}
	cut := kbConnectorItem("larkx:c1:2", "larkx", "c1", t1.Add(20*time.Minute))
	cut.Chain = []InboxChainEntry{kbReceived(t1.Add(25*time.Minute), "Cara", clip(long, 40))}
	own := t1.Add(30 * time.Minute)
	timed := kbConnectorItem("larkx:c1:3", "larkx", "c1", t1)
	timed.Chain = []InboxChainEntry{{Kind: ChainReceived, At: own.Add(time.Hour), Messages: []InboxChainMessage{
		{Sender: "Dan", Text: "At its own time", At: &own}, {Sender: "Dan", Text: "Never archived"}}}}
	f.add(it, cut, timed)

	list, _ := f.inbox(f.chat.ID, "")
	got := map[string][]kbChatInboxLine{}
	for _, x := range list {
		got[x.ID] = x.Messages
	}
	want := map[string][]kbChatInboxLine{
		it.ID:    {f.line(t0, "Alice", "First of two"), f.line(t1, "Alice", "Second of two\nwith a second line")},
		cut.ID:   {f.line(t1.Add(20*time.Minute), "Cara", long)},
		timed.ID: {f.line(own, "Dan", "At its own time")},
	}
	for id, w := range want {
		if !slices.Equal(got[id], w) {
			t.Errorf("%s: rows %q, want %q", id, got[id], w)
		}
	}
	if t.Failed() {
		t.Logf("recent:\n%s", f.e.Store.Since(f.chat.ID, time.Now().AddDate(0, 0, -7), 20000))
	}
}

// A row merged into a survivor of another chat: each chat's page lists the survivor, and its rows link to it by what
// the row of that chat received (a member keeps its own chain, and its source keeps collecting into it after the
// merge, followMerged), so the member's chat links the member's rows, before and after the merge, and the survivor's
// chat its own.
func TestKBChatInboxMergedRows(t *testing.T) {
	f := newKBInboxFixture(t)
	other := &kb.Chat{ID: kb.ChatID("larkx", "c9"), Source: "larkx", SourceID: "c9", Connector: "larkx", Kind: "group", Name: "Other group"}
	if err := f.e.Store.PutChat(other); err != nil {
		t.Fatal(err)
	}
	t0, t1, t2 := f.at.Add(-20*time.Minute), f.at.Add(-10*time.Minute), f.at
	f.write(f.chat, kb.Line{At: t0, Sender: "Alice", Text: "Before the merge"}, kb.Line{At: t2, Sender: "Alice", Text: "After the merge"})
	f.write(other, kb.Line{At: t1, Sender: "Bob", Text: "In the other chat"})
	member := kbConnectorItem("larkx:c1:m", "larkx", "c1", t0)
	member.Chain = []InboxChainEntry{kbReceived(t0, "Alice", "Before the merge"), kbReceived(t2, "Alice", "After the merge")}
	survivor := kbConnectorItem("larkx:c9:s", "larkx", "c9", t1)
	// merge copies the member's entries into the survivor's chain; followMerged adds a count without messages.
	survivor.Chain = []InboxChainEntry{kbReceived(t0, "Alice", "Before the merge"), kbReceived(t1, "Bob", "In the other chat"),
		{Kind: ChainReceived, At: t2, Count: 1, Sender: "Alice"}}
	survivor.MergedFrom, member.MergedInto = []InboxMergeRef{{ID: member.ID}}, survivor.ID
	f.add(member, survivor)

	for _, c := range []struct {
		chat *kb.Chat
		want []kbChatInboxLine
	}{
		{f.chat, []kbChatInboxLine{f.line(t0, "Alice", "Before the merge"), f.line(t2, "Alice", "After the merge")}},
		{other, []kbChatInboxLine{f.line(t1, "Bob", "In the other chat")}},
	} {
		list, _ := f.inbox(c.chat.ID, "")
		if len(list) != 1 || list[0].ID != survivor.ID || !slices.Equal(list[0].Messages, c.want) {
			t.Errorf("%s: %+v, want the survivor with rows %q", c.chat.ID, list, c.want)
		}
	}
}

// An item the source visibility boundary quarantined, and a survivor of a quarantined member, are listed with their
// rows as the Inbox lists them to the owner: that boundary keeps content from the AI, not from the owner
// (docs/assistant/inbox.md "One item = one row"), and the page asks no model.
func TestKBChatInboxQuarantined(t *testing.T) {
	f := newKBInboxFixture(t)
	t0, t1 := f.at.Add(-time.Minute), f.at
	f.write(f.chat, kb.Line{At: t0, Sender: "Alice", Text: "Kept from the AI"}, kb.Line{At: t1, Sender: "Bob", Text: "Also kept"})
	quarantined := kbConnectorItem("larkx:c1:q", "larkx", "c1", t0)
	quarantined.VisibilityExcluded = true
	quarantined.Chain = []InboxChainEntry{kbReceived(t0, "Alice", "Kept from the AI")}
	member := kbConnectorItem("larkx:c1:m", "larkx", "c1", t1)
	member.VisibilityExcluded = true
	member.Chain = []InboxChainEntry{kbReceived(t1, "Bob", "Also kept")}
	survivor := kbConnectorItem("larkx:c2:s", "larkx", "c2", t1.Add(time.Minute))
	survivor.MergedFrom, member.MergedInto = []InboxMergeRef{{ID: member.ID}}, survivor.ID
	f.add(quarantined, member, survivor)

	listed := []string{}
	for _, it := range f.s.inboxList(true) {
		listed = append(listed, it.ID)
	}
	list, _ := f.inbox(f.chat.ID, "")
	if got := kbInboxIDs(list); !slices.Equal(got, []string{survivor.ID, quarantined.ID}) || !slices.Equal(listed, got) {
		t.Fatalf("the chat lists %v, the Inbox %v", got, listed)
	}
	if !slices.Equal(list[0].Messages, []kbChatInboxLine{f.line(t1, "Bob", "Also kept")}) ||
		!slices.Equal(list[1].Messages, []kbChatInboxLine{f.line(t0, "Alice", "Kept from the AI")}) {
		t.Fatalf("rows: %+v", list)
	}
}

// The recent text is split into rows as the apps split it: a row's further lines stay with it, a heading that names no
// day keeps the day before, and "…" (a cut text), a line of its own and a row without a day, a time or a sender are no
// row of an item's.
func TestKBRecentRows(t *testing.T) {
	zone := time.UTC
	rows := kbRecentRows("…\n  cut off\n- 09:00 Nobody: before any day\n## 2026-11-20\n- 10:15 Alice: hello\n  more\n\n- 10:16 Bob: hi\nloose line\n## not a day\n- 11:00 Cara: still the day before\n- 11:01 no sender\n## 2026-11-21 \n- 08:00 Dan: x: y\n", zone)
	type row struct{ day, raw, sender, text string }
	var got []row
	for _, r := range rows {
		got = append(got, row{r.day, r.raw, r.sender, r.text})
		if want := r.day + " " + r.raw[2:7]; r.at.Format("2006-01-02 15:04") != want {
			t.Errorf("%q at %v", r.raw, r.at)
		}
	}
	want := []row{
		{"2026-11-20", "- 10:15 Alice: hello\n  more", "Alice", "hello\n  more"},
		{"2026-11-20", "- 10:16 Bob: hi", "Bob", "hi"},
		{"2026-11-20", "- 11:00 Cara: still the day before", "Cara", "still the day before"},
		{"2026-11-21", "- 08:00 Dan: x: y", "Dan", "x: y"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("rows %q\nwant %q", got, want)
	}
}
