package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
)

// larkDM is the Feishu plugin's thing for a private chat with new messages from Alice: last is the newest position,
// lines the context's message lines (as the plugin writes them).
func larkDM(feed, start, last string, count int, at time.Time, lines ...string) connector.InboxThing {
	block, _ := json.Marshal(InboxLark{AccountID: 1, FeedID: feed, StartPosition: start})
	ctx := "飞书私聊（Alice）的新消息："
	for _, l := range lines {
		ctx += "\n[11-05 10:00] Alice：" + l
	}
	return connector.InboxThing{Thread: "chat:" + feed, Kind: KindDM, Title: "Alice", Sender: "Alice", Preview: lines[len(lines)-1],
		Context: ctx, At: at, Last: last, Extra: &connector.ThingExtra{Source: "lark", Account: "feishu", AccountName: "飞书",
			Count: count, RuleTier: TierAction, Lark: block}}
}

func chainKinds(it *InboxItem) []string {
	var out []string
	for _, e := range it.Chain {
		out = append(out, e.Kind)
	}
	return out
}

func larkItem(h *inboxHub, feed string) *InboxItem {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	for _, it := range h.inbox.items {
		if it.Lark != nil && it.Lark.FeedID == feed && it.open() {
			return it
		}
	}
	return nil
}

// A Feishu DM from arrival to the reply: one received entry a batch (how many, who, the messages, where the batch
// starts), the triage with its tier, reason and urgency, the second batch, the reply sent.
func TestInboxChainFromArrivalToReply(t *testing.T) {
	ctx := context.Background()
	_, model := newTriageModel(t, "ok", `{"tier":"action","summary":"Alice 问报价","reason":"私聊直接提问","urgent":true}`)
	lark := &extensionLark{}
	h, _ := admitHub(t, model, lark)
	now := time.Now().Add(-time.Minute)
	lark.things = []connector.InboxThing{larkDM("f1", "100", "102", 3, now, "在吗", "客户催报价了", "周五前能给报价吗？")}
	collectAndAdmit(h)
	it := larkItem(h, "f1")
	if it == nil {
		t.Fatal("no item")
	}
	h.inbox.mu.Lock()
	got := it.listed()
	h.inbox.mu.Unlock()
	if len(got.Chain) != 2 || got.Chain[0].Kind != ChainReceived || got.Chain[1].Kind != ChainTriaged {
		t.Fatalf("chain: %+v", got.Chain)
	}
	r := got.Chain[0]
	if r.Count != 3 || r.Sender != "Alice" || r.Text != "收到 3 条新消息 · Alice" || r.Link == nil || r.Link.Type != LinkSource ||
		r.Link.Position != "100" || len(r.Messages) != 3 || r.Messages[2].Text != "周五前能给报价吗？" || r.ID != "c1" {
		t.Fatalf("received: %+v", r)
	}
	if tr := got.Chain[1]; tr.Text != "分拣：需要你（急）" || tr.Detail != "需要你 · 私聊直接提问" || tr.Link != nil {
		t.Fatalf("triaged: %+v", tr)
	}
	if !got.Urgent || got.Status != StatusAction {
		t.Fatalf("urgent %v, status %q", got.Urgent, got.Status)
	}

	// Two more messages: one entry for the batch, starting after what was there.
	lark.things = []connector.InboxThing{larkDM("f1", "100", "104", 5, now.Add(30*time.Second), "在吗", "客户催报价了", "周五前能给报价吗？", "顺便", "合同也看下")}
	collectAndAdmit(h)
	waitTriage(h)
	h.inbox.mu.Lock()
	kinds := chainKinds(it)
	second := it.Chain[2]
	h.inbox.mu.Unlock()
	if len(kinds) != 3 || kinds[2] != ChainReceived {
		t.Fatalf("after the second batch (same tier: no new triaged entry): %v", kinds)
	}
	if second.Count != 2 || second.Link.Position != "103" || len(second.Messages) != 2 || second.Messages[0].Text != "顺便" {
		t.Fatalf("second batch: %+v", second)
	}

	if _, err := h.replyInbox(ctx, it.ID, "周四给你"); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	last := it.Chain[len(it.Chain)-1]
	if last.Kind != ChainSent || last.Text != "发了飞书消息" || last.Link == nil || last.Link.Position != "104" || it.DoneBy != "replied" {
		t.Fatalf("sent: %+v, done by %q", last, it.DoneBy)
	}
}

func waitTriage(h *inboxHub) {
	h.inbox.triaging.Lock()
	h.inbox.triaging.Unlock() //nolint:staticcheck // waiting for the pass
}

// A reply the user wrote in Feishu itself files the item and says so; reading elsewhere or filing records "Done".
func TestInboxChainSelfReplyAndDone(t *testing.T) {
	_, model := newTriageModel(t, "ok", `{"tier":"action","summary":"Alice 问","reason":"私聊"}`)
	lark := &extensionLark{}
	h, _ := admitHub(t, model, lark)
	now := time.Now().Add(-time.Minute)
	lark.things = []connector.InboxThing{larkDM("f2", "1", "1", 1, now, "在吗")}
	collectAndAdmit(h)
	it := larkItem(h, "f2")
	done := larkDM("f2", "1", "1", 1, now, "在吗")
	done.Done = connector.DoneReplied
	lark.things = []connector.InboxThing{done}
	collectAndAdmit(h)
	h.inbox.mu.Lock()
	kinds, by, text := chainKinds(it), it.DoneBy, it.Chain[len(it.Chain)-1].Text
	h.inbox.mu.Unlock()
	if by != "replied" || kinds[len(kinds)-1] != ChainSelfReplied || text != "你在飞书里回复了" {
		t.Fatalf("self reply: by %q, %v %q", by, kinds, text)
	}

	lark.things = []connector.InboxThing{larkDM("f3", "1", "1", 1, now, "在吗")}
	collectAndAdmit(h)
	other := larkItem(h, "f3")
	if code, _ := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+other.ID+"/done", `{"done":true}`); code != 200 {
		t.Fatal(code)
	}
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if e := other.Chain[len(other.Chain)-1]; e.Kind != ChainDone || e.Text != "你标了已处理" {
		t.Fatalf("done: %+v", e)
	}
}

// Items stored before chains list received and triaged from their fields, and keep them when something is added.
func TestInboxChainSynthesizedForOldItems(t *testing.T) {
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	old := &InboxItem{ID: "mail:a:t:7", Source: "mail", Kind: KindMail, Title: "合同", Sender: "法务", Count: 2, Last: "8",
		StartedAt: at, UpdatedAt: at.Add(time.Hour), Tier: TierAction, Reason: "要你确认", Mail: &InboxMail{AccountID: "a", UIDs: []uint32{7, 8}}}
	c := old.listed()
	if len(c.Chain) != 2 || c.Chain[0].Text != "收到 2 封邮件 · 法务" || c.Chain[0].Link.UID != 7 || c.Chain[1].Detail != "需要你 · 要你确认" ||
		c.Status != StatusAction {
		t.Fatalf("synthesized: %+v status %q", c.Chain, c.Status)
	}
	b := newInbox()
	b.items[old.ID] = old
	b.noteSent(old, at.Add(2*time.Hour))
	if k := chainKinds(old); len(k) != 3 || k[0] != ChainReceived || k[1] != ChainTriaged || k[2] != ChainSent || old.Chain[2].ID != "c3" {
		t.Fatalf("kept: %v %+v", k, old.Chain)
	}
	if old.Chain[2].Link.UID != 8 {
		t.Fatalf("a mail reply answers the newest: %+v", old.Chain[2].Link)
	}
}

// A mail batch after triage: the new mails only, linked at the first new one.
func TestInboxChainMail(t *testing.T) {
	now := time.Now()
	date := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	a := assistantwire.InboxMailAccount{ID: "a1", Email: "me@example.com"}
	b := newInbox()
	b.applyMail(a, []assistantwire.InboxMailSummary{{UID: 3, Subject: "周报", From: MailAddress{Name: "李四"}, Snippet: "第一封", Date: date(-2 * time.Hour)}}, now)
	var it *InboxItem
	for _, x := range b.items {
		it = x
		b.noteTriaged(x)
	}
	b.applyMail(a, []assistantwire.InboxMailSummary{
		{UID: 3, Subject: "周报", From: MailAddress{Name: "李四"}, Snippet: "第一封", Date: date(-2 * time.Hour)},
		{UID: 5, Subject: "Re: 周报", From: MailAddress{Email: "wang@example.com"}, Snippet: "补充", Date: date(-time.Hour)},
	}, now)
	if len(it.Chain) != 3 {
		t.Fatalf("chain: %+v", it.Chain)
	}
	e := it.Chain[2]
	if e.Count != 1 || e.Link.UID != 5 || len(e.Messages) != 1 || e.Messages[0].Sender != "wang@example.com" || e.Messages[0].Text != "补充" ||
		e.Text != "收到 1 封邮件 · wang@example.com" {
		t.Fatalf("second batch: %+v", e)
	}
}

func TestInboxStatus(t *testing.T) {
	done := time.Now()
	cases := []struct {
		name string
		it   InboxItem
		want string
	}{
		{"untriaged", InboxItem{}, StatusAction},
		{"fyi", InboxItem{Tier: TierFYI}, StatusFYI},
		{"noise", InboxItem{Tier: TierNoise, Handling: &InboxHandling{State: HandlingLooking}}, StatusNoise},
		{"looking", InboxItem{Tier: TierAction, Handling: &InboxHandling{State: HandlingLooking}}, StatusWorking},
		{"urgent looking", InboxItem{Tier: TierAction, Urgent: true, Handling: &InboxHandling{State: HandlingLooking}}, StatusAction},
		{"agent", InboxItem{Tier: TierAction, Handling: &InboxHandling{State: HandlingAgent}}, StatusWorking},
		{"waiting", InboxItem{Tier: TierAction, Urgent: true, Handling: &InboxHandling{State: HandlingWaiting}}, StatusWorking},
		{"failed", InboxItem{Tier: TierAction, Handling: &InboxHandling{State: HandlingFailed}}, StatusAction},
		{"suggestion", InboxItem{Tier: TierAction, Handling: &InboxHandling{State: HandlingDone}, Suggestion: &InboxSuggestion{Kind: SuggestReply}}, StatusAction},
		{"conclusion", InboxItem{Tier: TierFYI, Handling: &InboxHandling{State: HandlingDone}, Suggestion: &InboxSuggestion{Kind: SuggestConclusion}}, StatusFYI},
		{"done", InboxItem{Tier: TierAction, DoneAt: &done}, ""},
	}
	for _, c := range cases {
		if got := c.it.status(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// Noise is one event per atom: the noise a batch opens joins the conversation's open noise event (one event for it
// all when there is none), and a noise event never takes what is sorted higher.
func TestInboxNoiseOneEventPerAtom(t *testing.T) {
	b := newInbox()
	b.items["n1"] = &InboxItem{ID: "n1", Tier: TierNoise, Group: &InboxGroup{Connector: "lark", Thread: "chat:g"}}
	plan := &groupPlan{model: true, events: []plannedEvent{
		{messages: []string{"m1"}, tier: TierNoise, title: "广告"},
		{messages: []string{"m2"}, tier: TierNoise, title: "又一条广告"},
		{existing: "n1", messages: []string{"m3"}, tier: TierAction, title: "有人问你"},
		{messages: []string{"m4"}, tier: TierFYI, title: "进展"},
	}}
	b.foldNoise("lark", "chat:g", plan)
	if len(plan.events) != 3 {
		t.Fatalf("events: %+v", plan.events)
	}
	if e := plan.events[0]; e.existing != "n1" || len(e.messages) != 2 {
		t.Fatalf("noise into the open noise event: %+v", e)
	}
	if e := plan.events[1]; e.existing != "" || e.tier != TierAction {
		t.Fatalf("not escalated into the noise event: %+v", e)
	}
	// Another conversation has no noise event yet: its noise is one new event.
	plan = &groupPlan{model: true, events: []plannedEvent{{messages: []string{"a"}, tier: TierNoise}, {messages: []string{"b"}, tier: TierNoise}}}
	b.foldNoise("lark", "chat:other", plan)
	if len(plan.events) != 1 || plan.events[0].existing != "" || len(plan.events[0].messages) != 2 {
		t.Fatalf("one new noise event: %+v", plan.events)
	}
}

// Messages that join an event being handled follow it: its tier and words stay whatever the model says of the batch.
func TestInboxMergeFollowsHandledEvent(t *testing.T) {
	h := newInboxHub(t)
	if err := h.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	ev := &InboxItem{ID: "lark:ev1", Source: "lark", Title: "报价", Summary: "客户要报价", Tier: TierAction, Last: "m1", Count: 1,
		Handling: &InboxHandling{Owner: HandlingOwner, State: HandlingWaiting}, Group: &InboxGroup{Connector: "lark", Thread: "chat:g"}}
	h.inbox.items[ev.ID] = ev
	c := assistantwire.InboxSource{ID: "lark", Name: "飞书"}
	thing := connector.InboxThing{ID: ev.ID, Thread: "chat:g", Kind: KindGroup, Title: "x", Last: "m2", At: time.Now()}
	h.putEvent(c, "chat:g", plannedEvent{existing: ev.ID, messages: []string{"m2"}, title: "闲聊", summary: "无关", tier: TierNoise}, thing, true)
	if ev.Tier != TierAction || ev.Title != "报价" || ev.Summary != "客户要报价" || ev.Count != 2 {
		t.Fatalf("the event moved: %+v", ev)
	}
	if k := chainKinds(ev); k[len(k)-1] != ChainReceived {
		t.Fatalf("chain: %v", k)
	}
}

// A connector's item that grew out of another source's item relates the two, both ways.
func TestInboxRelatedAcrossAtoms(t *testing.T) {
	b := newInbox()
	mail := &InboxItem{ID: "mail:1", Source: "mail", Title: "评审材料", Mail: &InboxMail{AccountID: "a", Thread: "t"}}
	chat := &InboxItem{ID: "lark:1", Source: "lark", Title: "王五", Lark: &InboxLark{FeedID: "f"}}
	same := &InboxItem{ID: "lark:2", Source: "lark", Title: "王五", Lark: &InboxLark{FeedID: "f"}}
	b.items[mail.ID], b.items[chat.ID], b.items[same.ID] = mail, chat, same
	b.offerParent(chat, &InboxParent{Kind: ParentInbox, ID: mail.ID, By: ParentByConnector})
	if len(chat.Related) != 1 || chat.Related[0] != (InboxRelated{ID: "mail:1", Title: "评审材料", Source: "mail"}) || len(mail.Related) != 1 {
		t.Fatalf("related: %+v / %+v", chat.Related, mail.Related)
	}
	b.offerParent(same, &InboxParent{Kind: ParentInbox, ID: chat.ID, By: ParentByContinues})
	if len(same.Related) != 0 {
		t.Fatalf("the same atom is not related: %+v", same.Related)
	}
}

// The chain keeps the newest chainMax entries, their ids going on.
func TestInboxChainKeepsTheNewest(t *testing.T) {
	b := newInbox()
	it := &InboxItem{ID: "x", Lark: &InboxLark{FeedID: "f"}, Last: "1"}
	b.noteReceived(it, inboxSeen{}, nil)
	for range chainMax + 5 {
		b.noteSent(it, time.Now())
	}
	if len(it.Chain) != chainMax || it.Chain[len(it.Chain)-1].ID != "c56" {
		t.Fatalf("%d entries, last %s", len(it.Chain), it.Chain[len(it.Chain)-1].ID)
	}
	if i18n.T(i18n.En, "assistant-inbox_chain.received_from", i18n.Args{"count": 1, "sender": "A"}) != "1 new message · A" {
		t.Fatal("en plural")
	}
}

// A context line ends in the notes the plugin gives the AI ("(id=…)", with the message it replies to and its topic):
// the chain's messages are for people and leave them out; text in parentheses of its own stays.
func TestContextMessagesDropTheIDNote(t *testing.T) {
	ctx := "飞书群「Team」里的一件事：\n" +
		"[11-05 10:00] Alice：[Sticker] (id=om_1)\n" +
		"[11-05 10:01] Bob（@了我）：can you check this (today) (id=om_2, 回复 om_1)\n" +
		"[11-05 10:02] Alice：thread reply (id=om_3, 回复 om_2, 话题 om_0)\n" +
		"[11-05 10:03] Bob：no notes here (just text)"
	got := contextMessages(ctx, 4)
	want := []InboxChainMessage{
		{Sender: "Alice", Text: "[Sticker]"},
		{Sender: "Bob", Text: "can you check this (today)"},
		{Sender: "Alice", Text: "thread reply"},
		{Sender: "Bob", Text: "no notes here (just text)"},
	}
	if len(got) != len(want) {
		t.Fatalf("messages: %+v", got)
	}
	for i := range want {
		if got[i].Sender != want[i].Sender || got[i].Text != want[i].Text {
			t.Fatalf("message %d: %+v, want %+v", i, got[i], want[i])
		}
	}
}
