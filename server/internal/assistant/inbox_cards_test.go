package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

// The message cards of the AI twin's drafts about a task (docs/assistant/unified-entry.md "Message cards link to tasks"): the card is in the
// task's conversation, and its "Send" is the inbox's reply, once per card.

// cardHub has a task in conv1 and an inbox item about it (a GitHub-like review thread) whose agent is drafting.
func cardHub(t *testing.T) (*inboxHub, *replierConnector, *InboxItem) {
	t.Helper()
	s, f := newTaskService(t)
	in := newFakeInbox()
	f.inbox, in.svc = in, s
	h := &inboxHub{Service: s, f: f, in: in}
	h.inboxReady(context.Background())
	rc := &replierConnector{}
	h.addConnector(rc, false)
	todo := startedTask(t, s, f)
	it := &InboxItem{ID: "example:1", Source: "example", Last: "m1", UpdatedAt: time.Now(),
		Connector: &InboxConnector{Thread: "example-org/example-repo#12", CanReply: true},
		Agent:     &InboxAgent{State: AgentRunning, SessionID: "s1", TodoID: todo.ID}}
	h.inbox.mu.Lock()
	h.inbox.items[it.ID] = it
	h.inbox.mu.Unlock()
	return h, rc, it
}

// cards are the message cards of conv1's feed after since, as the app reads it (not its task's plan and card).
func cards(t *testing.T, h *inboxHub, since time.Time) []FeedItem {
	t.Helper()
	path := "/api/v1/assistant/conversations/conv1/feed"
	if !since.IsZero() {
		path += "?since=" + url.QueryEscape(since.Format(time.RFC3339Nano))
	}
	code, out := inboxCall(t, h.Service, "GET", path, "")
	if code != http.StatusOK {
		t.Fatalf("feed: %d", code)
	}
	raw, _ := json.Marshal(out["items"])
	var items []FeedItem
	_ = json.Unmarshal(raw, &items)
	var cards []FeedItem
	for _, it := range items {
		if it.Kind == feedMessage {
			cards = append(cards, it)
		}
	}
	return cards
}

func cardBody(t *testing.T, it FeedItem) MessageCard {
	t.Helper()
	var b MessageCard
	if err := json.Unmarshal(it.Body, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// "Send" on a card: the reply goes out once, the card says so for good — on a reload, after the item opens again and
// the AI twin drafts a new card, and to the clients at once.
func TestMessageCardStaysSent(t *testing.T) {
	h, rc, it := cardHub(t)
	mux := h.routes()
	draft(t, h, "确实是 bug，谢谢指出，修好了告诉你")
	list := cards(t, h, time.Time{})
	if len(list) != 1 || cardSentAt(list[0]) != nil {
		t.Fatalf("feed = %+v", list)
	}
	card := list[0]
	loaded := card.At

	eventsBefore := len(strings.Fields(h.f.eventTypes()))
	body, _ := json.Marshal(map[string]string{"text": "确实是 bug，谢谢指出（改过）", "card": card.ID})
	if code, out := inboxCall(t, mux, "POST", "/api/v1/inbox/example:1/reply", string(body)); code != 200 || out["done_by"] != "replied" {
		t.Fatalf("reply: %d %v", code, out)
	}
	if len(rc.got) != 1 || rc.got[0].Text != "确实是 bug，谢谢指出（改过）" {
		t.Fatalf("sent = %+v", rc.got)
	}
	// The clients hear the card changed; a reload draws it sent, with what went out; so does a client that has it
	// already and asks for what came since.
	if !slices.Contains(strings.Fields(h.f.eventTypes())[eventsBefore:], "assistant_feed") {
		t.Fatal("the clients were not told the card was sent")
	}
	for _, since := range []time.Time{{}, loaded} {
		got := cards(t, h, since)
		if len(got) != 1 || cardSentAt(got[0]) == nil || cardBody(t, got[0]).Sent != "确实是 bug，谢谢指出（改过）" {
			t.Fatalf("since %v: feed = %+v", since, got)
		}
	}

	// Tapped again, from another device or after a reload with the old page: nothing more goes out.
	if code, _ := inboxCall(t, mux, "POST", "/api/v1/inbox/example:1/reply", string(body)); code != 200 || len(rc.got) != 1 {
		t.Fatalf("sent again: %d %+v", code, rc.got)
	}
	other, _ := json.Marshal(map[string]string{"text": "再说一遍", "card": card.ID})
	if code, _ := inboxCall(t, mux, "POST", "/api/v1/inbox/example:1/reply", string(other)); code != 200 || len(rc.got) != 1 {
		t.Fatalf("sent again with other text: %d %+v", code, rc.got)
	}

	// A draft that arrives after the reply went out puts no new card in.
	if _, err := h.runTool(context.Background(), "mac", "s1", "inbox_draft", json.RawMessage(`{"text":"晚到的草稿"}`)); !errors.Is(err, errSuggestFiled) {
		t.Fatalf("late inbox_draft: %v", err)
	}
	if got := cards(t, h, time.Time{}); len(got) != 1 {
		t.Fatalf("a card for a reply already out: %+v", got)
	}

	// A new message opens the item again and the AI twin drafts again: a new card to send; the old one stays sent.
	h.inbox.mu.Lock()
	it.DoneAt, it.DoneBy, it.Last = nil, "", "m2"
	h.inbox.mu.Unlock()
	time.Sleep(2 * time.Millisecond)
	draft(t, h, "已经合了")
	got := cards(t, h, time.Time{})
	if len(got) != 2 || got[0].ID != card.ID || cardSentAt(got[0]) == nil || cardSentAt(got[1]) != nil ||
		cardBody(t, got[0]).Sent != "确实是 bug，谢谢指出（改过）" || cardBody(t, got[1]).Draft != "已经合了" {
		t.Fatalf("after a new draft: %+v", got)
	}
}

// Taps that arrive together send one reply.
func TestMessageCardSendsOnce(t *testing.T) {
	h, rc, _ := cardHub(t)
	mux := h.routes()
	draft(t, h, "在修了")
	card := cards(t, h, time.Time{})[0]
	body, _ := json.Marshal(map[string]string{"text": "在修了", "card": card.ID})
	var wg sync.WaitGroup
	codes := make([]int, 5)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i], _ = inboxCall(t, mux, "POST", "/api/v1/inbox/example:1/reply", string(body))
		}()
	}
	wg.Wait()
	for _, c := range codes {
		if c != 200 {
			t.Fatalf("codes = %v", codes)
		}
	}
	if len(rc.got) != 1 {
		t.Fatalf("sent %d times", len(rc.got))
	}
}

// A recorded Persona send restores the send time even if the original write did not update the card.
func TestMessageCardSentBeforeTheRecord(t *testing.T) {
	h, _, it := cardHub(t)
	draft(t, h, "在修了")
	h.inbox.mu.Lock()
	at := time.Now().Add(time.Second)
	h.inbox.noteSent(it, at)
	it.done(at, "replied")
	h.inbox.mu.Unlock()
	got := cards(t, h, time.Time{})
	if len(got) != 1 || cardSentAt(got[0]) == nil || cardBody(t, got[0]).Sent != "" || cardBody(t, got[0]).Draft != "在修了" {
		t.Fatalf("feed = %+v", got)
	}
	h.inbox.mu.Lock()
	it.DoneAt, it.DoneBy = nil, ""
	h.inbox.mu.Unlock()
	if got := cards(t, h, time.Time{}); cardSentAt(got[0]) == nil {
		t.Fatal("the card forgot it was sent")
	}

	// Filed without a reply, or replied to before the card came: the card does not say sent (nothing went out from
	// it), but its "Send" sends nothing while the item stays filed (TestMessageCardOfAFiledItemSendsNothing).
	h2, rc2, it2 := cardHub(t)
	h2.inbox.mu.Lock()
	it2.done(time.Now().Add(-time.Minute), "replied")
	h2.inbox.mu.Unlock()
	it2.DoneBy = ""
	it2.DoneAt = nil
	draft(t, h2, "在修了")
	h2.inbox.mu.Lock()
	it2.done(time.Now(), "user")
	h2.inbox.mu.Unlock()
	got = cards(t, h2, time.Time{})
	if len(got) != 1 || cardSentAt(got[0]) != nil {
		t.Fatalf("filed, not replied: %+v", got)
	}
	body, _ := json.Marshal(map[string]string{"text": "在修了", "card": got[0].ID})
	if code, _ := inboxCall(t, h2.routes(), "POST", "/api/v1/inbox/example:1/reply", string(body)); code != http.StatusConflict || len(rc2.got) != 0 {
		t.Fatalf("filed, not replied: 发送 = %d, sent %+v", code, rc2.got)
	}
}

func TestMessageCardsFromSourceReplyAreFiled(t *testing.T) {
	h, rc, it := cardHub(t)
	draft(t, h, "First draft")
	draft(t, h, "Second draft")
	if got := cards(t, h, time.Time{}); len(got) != 2 {
		t.Fatalf("feed has %d drafts, want two", len(got))
	}
	at := time.Now().Add(time.Second).UTC()
	h.inbox.mu.Lock()
	settled := h.inbox.settleThing(it, connector.InboxThing{Done: connector.DoneReplied}, at)
	h.inbox.mu.Unlock()
	if !settled {
		t.Fatal("source reply did not file the item")
	}
	code, out := inboxCall(t, h.routes(), "GET", "/api/v1/inbox/example:1", "")
	if code != http.StatusOK || out["done_by"] != "source_replied" || out["now"] != NowDoneReplied {
		t.Fatalf("inbox detail: %d %v", code, out)
	}
	for _, card := range cards(t, h, time.Time{}) {
		if sent, filed := cardSentAt(card), cardFiledAt(t, card); sent != nil || filed == nil || !filed.Equal(at) {
			t.Fatalf("source reply: %s", card.Body)
		}
	}
	if len(rc.got) != 0 {
		t.Fatal("filing a draft sent a message")
	}
}

func TestMessageCardsKeepDoneStateAfterInboxEviction(t *testing.T) {
	ctx := t.Context()
	database := todoTestDB(t)
	s, _ := newTaskServiceOn(t, database)
	h := &inboxHub{Service: s}
	at := time.Now().UTC().Add(-inboxForget - time.Hour)
	for id, event := range map[string]string{"example:source-reply": ChainSelfReplied, "example:send": ChainSent} {
		it := InboxItem{ID: id, Source: "example", UpdatedAt: at, Chain: []InboxChainEntry{{Kind: event, At: at}}}
		it.done(at, "replied")
		raw, err := json.Marshal(it)
		if err != nil {
			t.Fatal(err)
		}
		if err := database.PutInboxItem(ctx, id, raw, at, it.DoneAt); err != nil {
			t.Fatal(err)
		}
		if _, err := s.appendFeedAt(ctx, "conv1", feedMessage, id, MessageCard{Draft: "A draft"}, at.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	held := len(h.inbox.items)
	h.inbox.mu.Unlock()
	if held != 0 {
		t.Fatalf("loaded %d inbox items into memory, want none", held)
	}
	for range 2 {
		got := cards(t, h, time.Time{})
		if len(got) != 2 {
			t.Fatalf("feed has %d message cards, want two", len(got))
		}
		for _, card := range got {
			sent, filed := cardSentAt(card), cardFiledAt(t, card)
			switch card.Ref {
			case "example:source-reply":
				if sent != nil || filed == nil || !filed.Equal(at) {
					t.Fatalf("archived source reply: %s", card.Body)
				}
			case "example:send":
				if sent == nil || !sent.Equal(at) || filed != nil || cardBody(t, card).Sent != "" {
					t.Fatalf("archived send: %s", card.Body)
				}
			default:
				t.Fatalf("unexpected card: %s", card.Ref)
			}
		}
	}
}

// The AI twin's draft goes out with one tap: one that leaves a blank to fill in is sent back to it (the assistant's
// rule, assistant.TestInboxDraftRules), and nothing changes on the item.
func TestInboxDraftRefusesPlaceholders(t *testing.T) {
	h, _, it := cardHub(t)
	if _, err := runTool(h.Service, "s1", "inbox_draft", map[string]any{"text": "修好了，【预计今晚 / 几点前】合进去"}); err == nil || !strings.Contains(err.Error(), "fill in") {
		t.Fatalf("a draft with a blank: %v", err)
	}
	if it.Agent.State != AgentRunning || it.Agent.Draft != "" || len(cards(t, h, time.Time{})) != 0 {
		t.Fatalf("a draft with a blank was taken: %+v", it.Agent)
	}
	draft(t, h, "修好了，合进去了")
}

// draft is the AI twin in conversation s1 handing in its draft (inbox_draft).
func draft(t *testing.T, h *inboxHub, text string) {
	t.Helper()
	if _, err := runTool(h.Service, "s1", "inbox_draft", map[string]any{"text": text}); err != nil {
		t.Fatalf("inbox_draft: %v", err)
	}
}

// replierConnector records the replies it gets.
type replierConnector struct {
	mu  sync.Mutex
	got []connector.InboxReply
}

func (*replierConnector) ID() string                                              { return "example" }
func (*replierConnector) Name() string                                            { return "Example" }
func (*replierConnector) Collect(context.Context) ([]connector.InboxThing, error) { return nil, nil }
func (*replierConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapInbox}}
}
func (c *replierConnector) Reply(_ context.Context, r connector.InboxReply) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, r)
	return nil
}

// An item the user filed while its AI twin worked ("Done", "Ignore", read and filed elsewhere) needs nothing more: the
// draft that comes in after puts no card with "Send" in the task's conversation.
func TestMessageCardNotForAFiledItem(t *testing.T) {
	for _, by := range []string{"user", "dismissed", "read"} {
		h, rc, it := cardHub(t)
		h.inbox.mu.Lock()
		it.done(time.Now(), by)
		h.inbox.mu.Unlock()
		if _, err := h.runTool(context.Background(), "mac", "s1", "inbox_draft", json.RawMessage(`{"text":"在修了"}`)); !errors.Is(err, errSuggestFiled) {
			t.Fatalf("filed by %s: inbox_draft: %v", by, err)
		}
		if got := cards(t, h, time.Time{}); len(got) != 0 {
			t.Fatalf("filed by %s: a card to send: %+v", by, got)
		}
		if len(rc.got) != 0 {
			t.Fatalf("filed by %s: sent %+v", by, rc.got)
		}
	}
}

// A card put in before its item was filed ("Done", "Ignore", read and filed elsewhere): its "Send" sends nothing, and the
// item stays filed as it was. Moved back to the inbox, the item is open again and the card sends.
func TestMessageCardOfAFiledItemSendsNothing(t *testing.T) {
	for _, by := range []string{"user", "dismissed", "read"} {
		h, rc, it := cardHub(t)
		mux := h.routes()
		draft(t, h, "在修了")
		list := cards(t, h, time.Time{})
		if len(list) != 1 {
			t.Fatalf("filed by %s: feed = %+v", by, list)
		}
		h.inbox.mu.Lock()
		it.done(time.Now(), by)
		h.inbox.mu.Unlock()
		body, _ := json.Marshal(map[string]string{"text": "在修了", "card": list[0].ID})
		code, out := inboxCall(t, mux, "POST", "/api/v1/inbox/example:1/reply", string(body))
		if code != http.StatusConflict || out["code"] != "inbox_item_done" || len(rc.got) != 0 {
			t.Fatalf("filed by %s: 发送 = %d %v, sent %+v", by, code, out, rc.got)
		}
		h.inbox.mu.Lock()
		doneBy, open := it.DoneBy, it.open()
		h.inbox.mu.Unlock()
		if open || doneBy != by {
			t.Fatalf("filed by %s: the item is now open=%v by %q", by, open, doneBy)
		}
		if got := cards(t, h, time.Time{}); cardSentAt(got[0]) != nil {
			t.Fatalf("filed by %s: the card says sent: %+v", by, got)
		}

		h.inbox.mu.Lock()
		it.DoneAt, it.DoneBy = nil, ""
		h.inbox.mu.Unlock()
		if code, _ := inboxCall(t, mux, "POST", "/api/v1/inbox/example:1/reply", string(body)); code != 200 || len(rc.got) != 1 {
			t.Fatalf("filed by %s, moved back: 发送 = %d, sent %+v", by, code, rc.got)
		}
	}
}

// An item filed after "Send" on its card looked (between sendFromCard's check and the reply taking its copy) is not
// answered either: nothing goes out and the item keeps the mark the user gave it (#4871).
func TestMessageCardOfAnItemFiledWhileSendingSendsNothing(t *testing.T) {
	for _, by := range []string{"user", "dismissed", "read"} {
		h, rc, it := cardHub(t)
		draft(t, h, "在修了")
		list := cards(t, h, time.Time{})
		if len(list) != 1 {
			t.Fatalf("filed by %s: feed = %+v", by, list)
		}
		send := func() (InboxItem, error) {
			h.inbox.mu.Lock()
			it.done(time.Now(), by) // filed after sendFromCard's check, before the reply
			h.inbox.mu.Unlock()
			return h.replyInboxChecked(context.Background(), it.ID, "在修了", "", true, nil, nil)
		}
		if _, _, err := h.sendFromCard(context.Background(), list[0].ID, it.ID, send); !errors.Is(err, errInboxItemDone) {
			t.Fatalf("filed by %s: 发送 = %v", by, err)
		}
		h.inbox.mu.Lock()
		doneBy, open := it.DoneBy, it.open()
		h.inbox.mu.Unlock()
		if len(rc.got) != 0 || open || doneBy != by {
			t.Fatalf("filed by %s: sent %+v, the item is now open=%v by %q", by, rc.got, open, doneBy)
		}
		if got := cards(t, h, time.Time{}); cardSentAt(got[0]) != nil {
			t.Fatalf("filed by %s: the card says sent: %+v", by, got)
		}
	}
}

// A reply that is not from a card answers a filed item as before (it is how the user replies from the item itself).
func TestReplyNotFromACardAnswersAFiledItem(t *testing.T) {
	h, rc, it := cardHub(t)
	h.inbox.mu.Lock()
	it.done(time.Now(), "user")
	h.inbox.mu.Unlock()
	if _, err := h.replyInboxChecked(context.Background(), it.ID, "在修了", "", false, nil, nil); err != nil || len(rc.got) != 1 {
		t.Fatalf("reply = %v, sent %+v", err, rc.got)
	}
}

// Another reply of the item may be out when Send on its card comes (the phone's reply, a repeat): the card's send waits
// for it on the item's reply lock, past the check #4899 made. An item filed meanwhile (that reply went out, or
// Done on another device) is not answered again, and keeps its mark.
func TestMessageCardOfAnItemFiledWhileAnotherReplyIsOutSendsNothing(t *testing.T) {
	for _, by := range []string{"replied", "user", "dismissed"} {
		h, rc, it := cardHub(t)
		draft(t, h, "在修了")
		list := cards(t, h, time.Time{})
		if len(list) != 1 {
			t.Fatalf("filed by %s: feed = %+v", by, list)
		}
		r, _ := h.inbox.replies.LoadOrStore(it.ID, &inboxReply{})
		other := r.(*inboxReply)
		other.mu.Lock() // the other reply is out
		sent := make(chan error, 1)
		go func() {
			send := func() (InboxItem, error) {
				return h.replyInboxChecked(context.Background(), it.ID, "在修了", "", true, nil, nil)
			}
			_, _, err := h.sendFromCard(context.Background(), list[0].ID, it.ID, send)
			sent <- err
		}()
		time.Sleep(200 * time.Millisecond) // the card's send is past its checks, waiting on the reply lock
		h.inbox.mu.Lock()
		it.done(time.Now(), by)
		h.inbox.mu.Unlock()
		other.mu.Unlock()
		if err := <-sent; !errors.Is(err, errInboxItemDone) {
			t.Fatalf("filed by %s: 发送 = %v", by, err)
		}
		h.inbox.mu.Lock()
		doneBy := it.DoneBy
		h.inbox.mu.Unlock()
		if len(rc.got) != 0 || doneBy != by {
			t.Fatalf("filed by %s: sent %+v, the item is now by %q", by, rc.got, doneBy)
		}
	}
}

// cardFiledAt is the filed_at a message card says (#4797); nil when it does not.
func cardFiledAt(t *testing.T, it FeedItem) *time.Time {
	t.Helper()
	var b struct {
		FiledAt *time.Time `json:"filed_at"`
	}
	if err := json.Unmarshal(it.Body, &b); err != nil {
		t.Fatal(err)
	}
	return b.FiledAt
}

// A card whose item was filed (Done, Dismiss, read elsewhere) says filed_at, when it was filed, so the apps hide its
// Send (which is refused); moved back to the inbox, it does not. A card that was sent never says it (#4797).
func TestMessageCardSaysFiled(t *testing.T) {
	for _, by := range []string{"user", "dismissed", "read"} {
		h, _, it := cardHub(t)
		draft(t, h, "在修了")
		if got := cards(t, h, time.Time{}); len(got) != 1 || cardFiledAt(t, got[0]) != nil {
			t.Fatalf("open, by %s: feed = %+v", by, got)
		}
		at := time.Now().Add(time.Second).UTC().Truncate(time.Second)
		h.inbox.mu.Lock()
		it.done(at, by)
		h.inbox.mu.Unlock()
		got := cards(t, h, time.Time{})
		if len(got) != 1 || cardFiledAt(t, got[0]) == nil || !cardFiledAt(t, got[0]).Equal(at) || cardSentAt(got[0]) != nil {
			t.Fatalf("filed by %s: feed = %s", by, got[0].Body)
		}
		h.inbox.mu.Lock()
		it.DoneAt, it.DoneBy = nil, ""
		h.inbox.mu.Unlock()
		if got := cards(t, h, time.Time{}); cardFiledAt(t, got[0]) != nil {
			t.Fatalf("filed by %s, moved back: feed = %s", by, got[0].Body)
		}
	}

	// Sent from the card: it says sent_at, not filed_at, though its item is filed (replied).
	h, rc, _ := cardHub(t)
	draft(t, h, "在修了")
	list := cards(t, h, time.Time{})
	body, _ := json.Marshal(map[string]string{"text": "在修了", "card": list[0].ID})
	if code, _ := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/example:1/reply", string(body)); code != 200 || len(rc.got) != 1 {
		t.Fatalf("发送 = %d, sent %+v", code, rc.got)
	}
	if got := cards(t, h, time.Time{}); cardSentAt(got[0]) == nil || cardFiledAt(t, got[0]) != nil {
		t.Fatalf("sent: feed = %s", got[0].Body)
	}
}

// cardLockCount is how many message cards have a "Send" lock kept (sendFromCard).
func cardLockCount(h *inboxHub) int {
	return h.inbox.cardSends.len()
}

// A card's "Send" lock is let go once its send is over: the long-running assistant keeps none for the cards it
// sent.
func TestCardSendLockReleased(t *testing.T) {
	h, rc, _ := cardHub(t)
	draft(t, h, "在修了")
	card := cards(t, h, time.Time{})[0]
	body, _ := json.Marshal(map[string]string{"text": "在修了", "card": card.ID})
	if code, _ := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/example:1/reply", string(body)); code != 200 || len(rc.got) != 1 {
		t.Fatalf("send: %d %+v", code, rc.got)
	}
	if n := cardLockCount(h); n != 0 {
		t.Fatalf("%d card locks kept after the send", n)
	}
}

// Letting a card's lock go does not let a second send through: a tap waiting on the first send, and one that comes
// after it is over, both find the card sent. One reply goes out and no lock is kept.
func TestCardSendLockReleasedSendsOnce(t *testing.T) {
	h, rc, it := cardHub(t)
	draft(t, h, "在修了")
	card := cards(t, h, time.Time{})[0]
	release := make(chan struct{})
	entered := make(chan struct{}, 3)
	send := func(wait bool) func() (InboxItem, error) {
		return func() (InboxItem, error) {
			entered <- struct{}{}
			if wait {
				<-release
			}
			return h.replyInboxChecked(context.Background(), it.ID, "在修了", "", true, nil, nil)
		}
	}
	type result struct {
		already bool
		err     error
	}
	first, second := make(chan result, 1), make(chan result, 1)
	go func() {
		_, already, err := h.sendFromCard(context.Background(), card.ID, it.ID, send(true))
		first <- result{already, err}
	}()
	<-entered // the first send holds the card's lock
	go func() {
		_, already, err := h.sendFromCard(context.Background(), card.ID, it.ID, send(false))
		second <- result{already, err}
	}()
	for h.inbox.cardSends.waiting(card.ID) < 2 { // the second tap waits on the card's lock
		time.Sleep(time.Millisecond)
	}
	close(release)
	if r := <-first; r.err != nil || r.already {
		t.Fatalf("first send: already=%v err=%v", r.already, r.err)
	}
	_, already, err := h.sendFromCard(context.Background(), card.ID, it.ID, send(false))
	if err != nil || !already {
		t.Fatalf("a tap after the send: already=%v err=%v", already, err)
	}
	if r := <-second; r.err != nil || !r.already {
		t.Fatalf("a tap waiting on the send: already=%v err=%v", r.already, r.err)
	}
	if len(entered) != 0 || len(rc.got) != 1 {
		t.Fatalf("sent %d times (%d extra sends tried)", len(rc.got), len(entered))
	}
	if n := cardLockCount(h); n != 0 {
		t.Fatalf("%d card locks kept after the sends", n)
	}
}
