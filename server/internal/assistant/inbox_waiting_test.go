package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Following up (docs/assistant/inbox.md "Following up", DECISIONS B-7): the processing conversation says the ball is
// on the other side (inbox_suggest's follow), the user's own reply keeps the matter open and waits, new messages clear
// the looks, and a "say something" whose turn only updates the recap goes back to where it was.

// followOf is the item's follow, under the inbox's lock (nil when it has none).
func followOf(h *inboxHub, it *InboxItem) *InboxFollow {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Follow == nil {
		return nil
	}
	f := *it.Follow
	return &f
}

// wantCheckIn says the item's check_at is about d from now.
func wantCheckIn(t *testing.T, h *inboxHub, it *InboxItem, d time.Duration, what string) {
	t.Helper()
	f := followOf(h, it)
	if f == nil || f.CheckAt == nil {
		t.Fatalf("%s: follow %+v", what, f)
	}
	if got := time.Until(*f.CheckAt); got < d-2*time.Minute || got > d+2*time.Minute {
		t.Fatalf("%s: check_at is %v from now, want about %v (at %v)", what, got, d, f.CheckAt)
	}
}

// handlingNow is the item's handling state and whether its conversation was woken, under the inbox's lock.
func handlingNow(h *inboxHub, it *InboxItem) (state string, wake bool, back string) {
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	if it.Handling != nil {
		state = it.Handling.State
	}
	if it.Agent != nil {
		wake, back = it.Agent.Wake, it.Agent.Back
	}
	return state, wake, back
}

// A follow-only suggestion puts the event in waiting with what it waits on and when to look again, records the
// waiting step the user reads (time in the user's zone, who said when there is one), and leaves the conversation with
// nothing that would bring the event back to another state.
func TestInboxFollowWaitsOnTheOtherSide(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"recap":"张三要报价，已经问了","follow":{"who":"张三","next":"等他给报价","check_in":"4h"}}`); err != nil {
		t.Fatal(err)
	}
	f := followOf(h, it)
	if f == nil || f.Who != "张三" || f.Next != "等他给报价" || f.Checks != 0 || f.Due {
		t.Fatalf("follow %+v", f)
	}
	wantCheckIn(t, h, it, 4*time.Hour, "check_in 4h")
	if state, wake, back := handlingNow(h, it); state != HandlingWaiting || wake || back != "" {
		t.Fatalf("handling %q, wake %v, back %q", state, wake, back)
	}
	if it.Suggestion != nil {
		t.Fatalf("a follow-only call left a suggestion: %+v", it.Suggestion)
	}
	h.inbox.mu.Lock()
	last := it.Chain[len(it.Chain)-1]
	h.inbox.mu.Unlock()
	if last.Kind != ChainWaiting || last.Via != ViaAssistant || last.Detail != "等他给报价" {
		t.Fatalf("waiting step: %+v", last)
	}
	if strings.TrimSpace(last.Text) == "" || !strings.Contains(last.Text, "张三") || strings.Contains(last.Text, "{") {
		t.Fatalf("waiting step's words: %q", last.Text)
	}
	// Listed, it waits on the other side with the same words the apps draw.
	h.inbox.mu.Lock()
	l := it.listed()
	h.inbox.mu.Unlock()
	if l.Follow == nil || l.Follow.Who != "张三" || l.Follow.CheckAt == nil {
		t.Fatalf("listed follow: %+v", l.Follow)
	}
}

// check_at wins over check_in; without either it is 24 hours; anything outside 30 minutes to 14 days is held to the
// nearer edge.
func TestInboxFollowCheckAtAndClamping(t *testing.T) {
	at := time.Now().UTC().Add(6 * time.Hour).Truncate(time.Second)
	h, _, it := handledHub(t)
	args := `{"recap":"等回话","follow":{"who":"李四","next":"等确认","check_at":"` + at.Format(time.RFC3339) + `","check_in":"2d"}}`
	if _, err := suggestTool(t, h, args); err != nil {
		t.Fatal(err)
	}
	if f := followOf(h, it); f == nil || f.CheckAt == nil || !f.CheckAt.Equal(at) {
		t.Fatalf("check_at should win: %+v", f)
	}

	for _, c := range []struct {
		name  string
		fargs string
		want  time.Duration
	}{
		{"no time given", `{"who":"李四"}`, 24 * time.Hour},
		{"30m", `{"check_in":"30m"}`, 30 * time.Minute},
		{"2d", `{"check_in":"2d"}`, 48 * time.Hour},
		{"under the floor", `{"check_in":"1m"}`, 30 * time.Minute},
		{"past", `{"check_in":"-3h"}`, 30 * time.Minute},
		{"negative days", `{"check_in":"-1d"}`, 30 * time.Minute},
		{"days and hours", `{"check_in":"2d12h"}`, 60 * time.Hour},
		{"over the ceiling", `{"check_in":"30d"}`, 14 * 24 * time.Hour},
		{"far over the ceiling", `{"check_in":"366d"}`, 14 * 24 * time.Hour},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, _, it := handledHub(t)
			if _, err := suggestTool(t, h, `{"recap":"等回话","follow":`+c.fargs+`}`); err != nil {
				t.Fatal(err)
			}
			wantCheckIn(t, h, it, c.want, c.name)
		})
	}

	// A check_at before the floor or after the ceiling is held to the edge too.
	for _, c := range []struct {
		name string
		at   time.Time
		want time.Duration
	}{
		{"check_at in the past", time.Now().UTC().Add(-time.Hour), 30 * time.Minute},
		{"check_at far ahead", time.Now().UTC().Add(60 * 24 * time.Hour), 14 * 24 * time.Hour},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, _, it := handledHub(t)
			args := `{"recap":"等回话","follow":{"check_at":"` + c.at.Format(time.RFC3339) + `"}}`
			if _, err := suggestTool(t, h, args); err != nil {
				t.Fatal(err)
			}
			wantCheckIn(t, h, it, c.want, c.name)
		})
	}
}

// who and next are one line, cut at 80 characters.
func TestInboxFollowClipsWhoAndNext(t *testing.T) {
	h, _, it := handledHub(t)
	long := strings.Repeat("长", 100)
	args := `{"recap":"等回话","follow":{"who":"` + long + `\n第二行","next":"` + long + `"}}`
	if _, err := suggestTool(t, h, args); err != nil {
		t.Fatal(err)
	}
	// clip keeps n runes and marks the cut with "…" (textfmt.Clip, as everywhere else).
	f := followOf(h, it)
	if f == nil || len([]rune(f.Who)) > 81 || len([]rune(f.Next)) > 81 || !strings.HasSuffix(f.Who, "…") || !strings.HasSuffix(f.Next, "…") ||
		strings.Contains(f.Who, "\n") || strings.Contains(f.Who, "第二行") {
		t.Fatalf("clipped: who %d next %d (%q)", len([]rune(f.Who)), len([]rune(f.Next)), f.Who)
	}
}

// A follow the tool cannot read (a check_in it does not parse, a check_at that is not RFC 3339) is refused with a
// reason the model can act on, and nothing on the item changes.
func TestInboxFollowRefusesUnreadableTime(t *testing.T) {
	for _, args := range []string{
		`{"recap":"等回话","follow":{"check_in":"4 hours"}}`,
		`{"recap":"等回话","follow":{"check_in":"soon"}}`,
		`{"recap":"等回话","follow":{"check_at":"tomorrow"}}`,
		`{"recap":"等回话","follow":{"check_at":"2026-01-02 03:04"}}`,
	} {
		t.Run(args, func(t *testing.T) {
			h, _, it := handledHub(t)
			h.inbox.mu.Lock()
			before, chain := string(itemState(it)), len(it.Chain)
			h.inbox.mu.Unlock()
			saved := h.st.saved(it.ID)
			if _, err := suggestTool(t, h, args); err == nil {
				t.Fatal("an unreadable follow time was kept")
			}
			h.inbox.mu.Lock()
			after := string(itemState(it))
			chain2 := len(it.Chain)
			h.inbox.mu.Unlock()
			if after != before || chain2 != chain || h.st.saved(it.ID) != saved {
				t.Fatalf("a refused follow left a trace: chain %d→%d, saved %v→%v\nbefore %s\nafter  %s",
					chain, chain2, saved, h.st.saved(it.ID), before, after)
			}
		})
	}
}

// follow is only for the turn that hands in nothing ("waiting on them, nothing for you to confirm"): with a kind it
// is refused, saying the two are one or the other, and nothing is written.
func TestInboxFollowWithKindRefused(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.mu.Lock()
	before, chain := string(itemState(it)), len(it.Chain)
	h.inbox.mu.Unlock()
	saved := h.st.saved(it.ID)
	_, err := suggestTool(t, h, `{"kind":"reply","text":"好的","follow":{"who":"张三","next":"等回话","check_in":"4h"}}`)
	if err == nil || !strings.Contains(err.Error(), "follow") || !strings.Contains(err.Error(), "kind") {
		t.Fatalf("kind with follow: %v", err)
	}
	h.inbox.mu.Lock()
	after := string(itemState(it))
	chain2 := len(it.Chain)
	h.inbox.mu.Unlock()
	if after != before || chain2 != chain || h.st.saved(it.ID) != saved {
		t.Fatalf("a refused follow left a trace: chain %d→%d\nbefore %s\nafter  %s", chain, chain2, before, after)
	}
}

// On a filed item a follow is refused like a kind (the model must not be told it was kept), and nothing is written.
func TestInboxFollowOnAFiledItemRefused(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.mu.Lock()
	h.inbox.file(it, time.Now(), "user")
	before, chain := string(itemState(it)), len(it.Chain)
	h.inbox.mu.Unlock()
	saved := h.st.saved(it.ID)
	_, err := suggestTool(t, h, `{"recap":"等回话","follow":{"who":"张三","next":"等回话","check_in":"4h"}}`)
	if err == nil || !strings.Contains(err.Error(), "already filed") {
		t.Fatalf("a follow on a filed item: %v", err)
	}
	h.inbox.mu.Lock()
	after := string(itemState(it))
	chain2 := len(it.Chain)
	h.inbox.mu.Unlock()
	if it.Follow != nil || after != before || chain2 != chain || h.st.saved(it.ID) != saved {
		t.Fatalf("a refused follow left a trace: follow %+v chain %d→%d", it.Follow, chain, chain2)
	}
}

// A suggestion already waiting for the user stays, and the follow is written beside it.
func TestInboxFollowBesideASuggestion(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"可以，周四给你","recap":"张三要报价"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := suggestTool(t, h, `{"recap":"张三要报价，也问了他时间","follow":{"who":"张三","next":"等他确认时间","check_in":"2h"}}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	s := it.Suggestion
	h.inbox.mu.Unlock()
	if s == nil || s.Kind != SuggestReply || s.Text != "可以，周四给你" || s.Version != 1 {
		t.Fatalf("the suggestion went away: %+v", s)
	}
	f := followOf(h, it)
	if f == nil || f.Who != "张三" || f.Next != "等他确认时间" {
		t.Fatalf("follow beside a suggestion: %+v", f)
	}
	wantCheckIn(t, h, it, 2*time.Hour, "beside a suggestion")
}

// The waiting step is written by the program in the user's language, its time in the user's zone (the hub runs in
// UTC); with nobody named it says it waits for a reply.
func TestInboxWaitingStepInTheUsersLanguageAndZone(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Skip("no Asia/Shanghai on this machine")
	}
	for _, c := range []struct {
		name string
		lang i18n.Lang
		who  string
		want string
	}{
		{"zh, who", i18n.ZhHans, "张三", "等张三 · "},
		{"zh, nobody", i18n.ZhHans, "", "等对方回复 · "},
		{"en, who", i18n.En, "Zhang", "Waiting for Zhang · look again "},
		{"ja, nobody", i18n.Ja, "", "返信待ち"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, _, it := handledHub(t)
			setSettings(t, h.Service, func(v *Settings) { v.Timezone = "Asia/Shanghai" })
			h.inbox.lang = func() i18n.Lang { return c.lang }
			args := `{"recap":"等回话","follow":{"next":"等确认","check_in":"4h"}}`
			if c.who != "" {
				args = `{"recap":"等回话","follow":{"who":"` + c.who + `","next":"等确认","check_in":"4h"}}`
			}
			if _, err := suggestTool(t, h, args); err != nil {
				t.Fatal(err)
			}
			h.inbox.mu.Lock()
			last := it.Chain[len(it.Chain)-1]
			h.inbox.mu.Unlock()
			if !strings.Contains(last.Text, c.want) {
				t.Fatalf("waiting step %q, want %q in it", last.Text, c.want)
			}
			f := followOf(h, it)
			if f == nil || f.CheckAt == nil {
				t.Fatalf("follow %+v", f)
			}
			if stamp := f.CheckAt.In(loc).Format("01-02 15:04"); !strings.Contains(last.Text, stamp) {
				t.Fatalf("waiting step %q, want the time %s in the user's zone", last.Text, stamp)
			}
		})
	}
}

// Confirming a reply sends it and then waits: the default follow (24 hours, nobody named) and a waiting step the user
// made, after the sent one.
func TestInboxConfirmReplyWaitsWithTheDefaultFollow(t *testing.T) {
	h, lark, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"可以，周四给你"}`); err != nil {
		t.Fatal(err)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`); code != 200 {
		t.Fatalf("confirm: %d %v", code, out)
	}
	if len(lark.got) != 1 {
		t.Fatalf("sent: %+v", lark.got)
	}
	if state, _, _ := handlingNow(h, it); state != HandlingWaiting {
		t.Fatalf("handling after the send: %q", state)
	}
	f := followOf(h, it)
	if f == nil || f.Who != "" || f.Next != "" || f.Checks != 0 || f.Due {
		t.Fatalf("default follow: %+v", f)
	}
	wantCheckIn(t, h, it, 24*time.Hour, "confirmed reply")
	h.inbox.mu.Lock()
	kinds := chainKinds(it)
	wait := it.Chain[len(it.Chain)-1]
	h.inbox.mu.Unlock()
	if len(kinds) < 2 || kinds[len(kinds)-2] != ChainSent || kinds[len(kinds)-1] != ChainWaiting {
		t.Fatalf("chain after the send: %v", kinds)
	}
	if wait.Via != ViaUser || strings.TrimSpace(wait.Text) == "" {
		t.Fatalf("the user's waiting step: %+v", wait)
	}
}

// A reply the user makes in Persona (POST /reply) on an item with a processing conversation does not file it: it
// waits on the other side with the default follow, records the send and the waiting step, and tells the conversation
// what was sent without waking it.
func TestInboxReplyWithAConversationWaits(t *testing.T) {
	h, lark, it := handledHub(t)
	h.inbox.mu.Lock()
	started := it.Agent.StartedAt
	h.inbox.mu.Unlock()
	code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/reply", `{"text":"好的，周四给你"}`)
	if code != 200 || out["done_at"] != nil {
		t.Fatalf("reply: %d %v", code, out)
	}
	if len(lark.got) != 1 || lark.got[0].Text != "好的，周四给你" {
		t.Fatalf("sent: %+v", lark.got)
	}
	state, wake, back := handlingNow(h, it)
	if state != HandlingWaiting || wake || back != "" {
		t.Fatalf("handling %q, wake %v, back %q", state, wake, back)
	}
	f := followOf(h, it)
	if f == nil || f.Who != "" || f.Next != "" || f.Checks != 0 || f.Due {
		t.Fatalf("default follow: %+v", f)
	}
	wantCheckIn(t, h, it, 24*time.Hour, "the user's reply")
	h.inbox.mu.Lock()
	kinds := chainKinds(it)
	wait := it.Chain[len(it.Chain)-1]
	h.inbox.mu.Unlock()
	if len(kinds) < 2 || kinds[len(kinds)-2] != ChainSent || kinds[len(kinds)-1] != ChainWaiting || wait.Via != ViaUser {
		t.Fatalf("chain after the user's reply: %v", kinds)
	}
	// The conversation is told what was sent, as a notice that does not start a turn: delivered (Pending drained),
	// handling still waiting, no turn started.
	told := toldConv(t, h, "好的，周四给你")
	if strings.Contains(told, "有了新消息") {
		t.Fatalf("the conversation was told new messages: %s", told)
	}
	// Delivery empties Pending after the send (keepToldSince), so wait for that rather than read it at once.
	deadline := time.Now().Add(5 * time.Second)
	poll := time.NewTicker(5 * time.Millisecond)
	defer poll.Stop()
	for {
		h.inbox.mu.Lock()
		pending, startedNow := len(it.Agent.Pending), it.Agent.StartedAt
		handling := it.Handling.State
		h.inbox.mu.Unlock()
		if pending == 0 {
			if handling != HandlingWaiting {
				t.Fatalf("after delivery: handling %q", handling)
			}
			if (started == nil) != (startedNow == nil) || started != nil && !started.Equal(*startedNow) {
				t.Fatalf("the notice started a turn: started_at %v → %v", started, startedNow)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the notice was not delivered: %d notes pending", pending)
		}
		<-poll.C
	}
	// Listed, it is in progress waiting, not filed.
	h.inbox.mu.Lock()
	l := it.listed()
	h.inbox.mu.Unlock()
	if l.Status != StatusWorking || l.DoneAt != nil {
		t.Fatalf("listed: status %q done %v", l.Status, l.DoneAt)
	}
}

// A handback suggestion goes back to the conversation to do the work; when that turn only updates the recap, the
// item returns to waiting and keeps what it waits on.
func TestInboxHandbackRecapOnlyKeepsWaiting(t *testing.T) {
	h, _, it := handledHub(t)
	// Hold the item's delivery off: tellSoon sends its pending notes (and clears the wake) on a goroutine once a request
	// has answered, which the test would race with.
	delivery, _ := h.inbox.telling.LoadOrStore(it.ID, &sync.Mutex{})
	delivery.(*sync.Mutex).Lock()
	t.Cleanup(delivery.(*sync.Mutex).Unlock)
	if _, err := suggestTool(t, h, `{"recap":"先问张三","follow":{"who":"张三","next":"等他给口径","check_in":"4h"}}`); err != nil {
		t.Fatal(err)
	}
	before := followOf(h, it)
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"先问张三再回客户","handback":true}`); err != nil {
		t.Fatal(err)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`); code != 200 {
		t.Fatalf("confirm the handback: %d %v", code, out)
	}
	if state, wake, _ := handlingNow(h, it); state != HandlingLooking || !wake {
		t.Fatalf("after the handback: %q wake %v", state, wake)
	}
	if _, err := suggestTool(t, h, `{"recap":"问过了，等张三回口径"}`); err != nil {
		t.Fatal(err)
	}
	// A handback comes after a suggestion, which had left the item at done (waiting for the user): a recap-only turn
	// goes back there, not to waiting. What must survive is the follow it waits on.
	if state, _, _ := handlingNow(h, it); state == HandlingLooking {
		t.Fatalf("a recap-only turn after the handback left it looking: %q", state)
	}
	f := followOf(h, it)
	if f == nil || before == nil || f.Who != before.Who || f.Next != before.Next || !f.CheckAt.Equal(*before.CheckAt) {
		t.Fatalf("the handback dropped the follow: %+v → %+v", before, f)
	}
}

// The user answers the matter themselves while a suggestion waits: their reply supersedes it, so it is gone from the
// item and confirming it afterwards is stale (nothing more goes out).
func TestInboxDirectReplySupersedesASuggestion(t *testing.T) {
	h, lark, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"可以，周四给你"}`); err != nil {
		t.Fatal(err)
	}
	if code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/reply", `{"text":"我自己回了"}`); code != 200 {
		t.Fatalf("direct reply: %d %v", code, out)
	}
	if len(lark.got) != 1 || lark.got[0].Text != "我自己回了" {
		t.Fatalf("sent: %+v", lark.got)
	}
	h.inbox.mu.Lock()
	s := it.Suggestion
	h.inbox.mu.Unlock()
	if s != nil {
		t.Fatalf("the user's own reply left the suggestion: %+v", s)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`); code != 409 || out["code"] != "suggestion_stale" {
		t.Fatalf("confirming the superseded suggestion: %d %v", code, out)
	}
	if len(lark.got) != 1 {
		t.Fatalf("the stale confirmation sent again: %+v", lark.got)
	}
	if state, _, _ := handlingNow(h, it); state != HandlingWaiting {
		t.Fatalf("handling: %q", state)
	}
}

// The same direct reply asked again (a timed-out request retried) is one send: the item keeps one sent and one waiting
// step, and the wait is not pushed further out.
func TestInboxDirectReplyRepeatRecordsOnce(t *testing.T) {
	h, lark, it := handledHub(t)
	for i := range 2 {
		if code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/reply", `{"text":"好的，周四给你","client_id":"c-1"}`); code != 200 {
			t.Fatalf("reply %d: %d %v", i, code, out)
		}
	}
	if len(lark.got) != 1 {
		t.Fatalf("a repeated reply went out %d times", len(lark.got))
	}
	h.inbox.mu.Lock()
	kinds := chainKinds(it)
	h.inbox.mu.Unlock()
	if strings.Count(strings.Join(kinds, ","), ChainSent) != 1 || strings.Count(strings.Join(kinds, ","), ChainWaiting) != 1 {
		t.Fatalf("chain after the repeat: %v", kinds)
	}
}

// A mail item that waits: the same reply asked again after an answer was lost is one send, and it leaves one sent and
// one waiting step (the hub says it went out already; this manager still writes the state it never wrote).
func TestInboxMailWaitingRepeatRecordsOnce(t *testing.T) {
	h := newInboxHub(t)
	now := time.Now()
	it := &InboxItem{ID: "mail:acc1:t:9", Source: "mail", Kind: KindMail, Title: "季度汇报", Last: "9", Count: 1,
		StartedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute), Tier: TierAction, TriagedAt: "9",
		Mail:  &InboxMail{AccountID: "acc1", Mailbox: "INBOX", UIDs: []uint32{9}, Thread: "季度汇报"},
		Agent: &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "s1"}}
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	h.in.mail = &fakeMail{}
	h.inbox.mu.Lock()
	h.inbox.items[it.ID] = it
	h.inbox.setHandling(it, HandlingLooking, "", now)
	h.inbox.mu.Unlock()
	h.f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking})

	for i := range 2 {
		code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/reply", `{"text":"收到","client_id":"m-1"}`)
		if code != 200 || out["done_at"] != nil {
			t.Fatalf("mail reply %d: %d %v", i, code, out)
		}
	}
	if len(h.in.mail.sent) != 1 {
		t.Fatalf("the mail reply went out %d times", len(h.in.mail.sent))
	}
	h.inbox.mu.Lock()
	kinds := chainKinds(it)
	open := it.open()
	follow := it.Follow
	state := it.Handling.State
	h.inbox.mu.Unlock()
	if !open || state != HandlingWaiting || follow == nil {
		t.Fatalf("the mail item after its reply: open %v, %q, follow %+v", open, state, follow)
	}
	joined := strings.Join(kinds, ",")
	if strings.Count(joined, ChainSent) != 1 || strings.Count(joined, ChainWaiting) != 1 {
		t.Fatalf("chain after the repeat: %v", kinds)
	}
}

// A mail item that waits, and its first send's answer was lost (in doubt): the retry carries the same send id, the hub
// says it went out already, and this manager still writes the state it never wrote — one sent and one waiting step,
// open and waiting, not left as it was.
func TestInboxMailWaitingInDoubtRetryWritesTheWait(t *testing.T) {
	h := newInboxHub(t)
	now := time.Now()
	it := &InboxItem{ID: "mail:acc1:t:9", Source: "mail", Kind: KindMail, Title: "季度汇报", Last: "9", Count: 1,
		StartedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute), Tier: TierAction, TriagedAt: "9",
		Mail:  &InboxMail{AccountID: "acc1", Mailbox: "INBOX", UIDs: []uint32{9}, Thread: "季度汇报"},
		Agent: &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "s1"}}
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	h.in.mail = &fakeMail{}
	h.inbox.mu.Lock()
	h.inbox.items[it.ID] = it
	h.inbox.setHandling(it, HandlingLooking, "", now)
	h.inbox.mu.Unlock()
	h.f.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking})
	h.in.inDoubt = true

	code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/reply", `{"text":"收到","client_id":"m-1"}`)
	if code != 502 || out["code"] != assistantwire.CodeAssistantInDoubt {
		t.Fatalf("the cut-off send: %d %v", code, out)
	}
	h.inbox.mu.Lock()
	stateBefore, followBefore := it.Handling.State, it.Follow
	h.inbox.mu.Unlock()
	if stateBefore != HandlingLooking || followBefore != nil {
		t.Fatalf("the cut-off send wrote a state: %q %+v", stateBefore, followBefore)
	}

	code, out = inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/reply", `{"text":"收到","client_id":"m-1"}`)
	if code != 200 || out["done_at"] != nil {
		t.Fatalf("the retry: %d %v", code, out)
	}
	if len(h.in.mail.sent) != 1 {
		t.Fatalf("the retry sent again: %d", len(h.in.mail.sent))
	}
	h.inbox.mu.Lock()
	kinds := chainKinds(it)
	open, follow, state := it.open(), it.Follow, it.Handling.State
	h.inbox.mu.Unlock()
	joined := strings.Join(kinds, ",")
	if !open || state != HandlingWaiting || follow == nil {
		t.Fatalf("after the retry: open %v, %q, follow %+v", open, state, follow)
	}
	if strings.Count(joined, ChainSent) != 1 || strings.Count(joined, ChainWaiting) != 1 {
		t.Fatalf("chain after the retry: %v", kinds)
	}
}

// The user replies twice on an item that waits: the second reply is a new send (its own send id), and the item keeps
// one waiting step per reply, with the follow's time moved to the newer one.
func TestInboxSecondDirectReplyRefreshesTheWait(t *testing.T) {
	h, lark, it := handledHub(t)
	for _, text := range []string{"好的，周四给你", "再补一句，周五也行"} {
		if code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/reply", `{"text":"`+text+`"}`); code != 200 {
			t.Fatalf("reply %q: %d %v", text, code, out)
		}
	}
	if len(lark.got) != 2 {
		t.Fatalf("sent: %+v", lark.got)
	}
	h.inbox.mu.Lock()
	kinds := chainKinds(it)
	h.inbox.mu.Unlock()
	if strings.Count(strings.Join(kinds, ","), ChainWaiting) != 2 || kinds[len(kinds)-1] != ChainWaiting {
		t.Fatalf("chain after two replies: %v", kinds)
	}
	if state, _, _ := handlingNow(h, it); state != HandlingWaiting {
		t.Fatalf("handling: %q", state)
	}
}

// The old apps' "Send" on a message card (fromCard) and a handed-off suggestion keep filing the item, as before: the
// waiting entry points are the direct /reply, a confirmed suggestion and the tool's follow.
func TestInboxCardSendStillFiles(t *testing.T) {
	h, lark, it := handledHub(t)
	out, err := h.replyInboxDo(context.Background(), it.ID, "好的，周四给你", "", true, true)
	if err != nil || out.DoneAt == nil {
		t.Fatalf("card send: %v %+v", err, out.DoneBy)
	}
	if len(lark.got) != 1 {
		t.Fatalf("sent: %+v", lark.got)
	}
	if state, _, _ := handlingNow(h, it); state != HandlingDone {
		t.Fatalf("handling after a card send: %q", state)
	}
	if f := followOf(h, it); f != nil {
		t.Fatalf("a card send left a follow: %+v", f)
	}
}

// A handed-off conversation's confirmed reply keeps filing the item (no waiting), as it did before the waiting entry
// points: only the item's own processing conversation leaves the matter open on the user's reply.
func TestInboxHandedReplyStillFiles(t *testing.T) {
	h, lark, it := handoffHub(t)
	handOff(t, h, it)
	if _, err := mainSuggest(h, "m1", `{"item":"`+it.ID+`","kind":"reply","text":"好的，周四给你","recap":"等你定"}`); err != nil {
		t.Fatal(err)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`); code != 200 {
		t.Fatalf("confirm: %d %v", code, out)
	}
	if len(lark.got) != 1 {
		t.Fatalf("sent: %+v", lark.got)
	}
	h.inbox.mu.Lock()
	open, by := it.open(), it.DoneBy
	h.inbox.mu.Unlock()
	if open || by != "replied" {
		t.Fatalf("a handed-off reply: open %v, by %q", open, by)
	}
	if f := followOf(h, it); f != nil {
		t.Fatalf("a handed-off reply left a follow: %+v", f)
	}
}

// An item with no processing conversation still files on the user's reply, as before.
func TestInboxReplyWithoutAConversationStillFiles(t *testing.T) {
	ctx := context.Background()
	h := newInboxHub(t)
	now := time.Now()
	c := &replyRacingConnector{inboxConnector: inboxConnector{things: []connector.InboxThing{thing("a", now.Add(-time.Minute))}}}
	h.addConnector(c, false)
	h.collectInbox(ctx, roundPoll)
	it := openItem(t, h)
	h.inbox.mu.Lock()
	hasAgent := it.Agent != nil && it.Agent.SessionID != ""
	h.inbox.mu.Unlock()
	if hasAgent {
		t.Fatal("the test's item has a conversation")
	}
	if _, err := h.replyInboxAs(ctx, it.ID, "看过了", ""); err != nil {
		t.Fatal(err)
	}
	if open, _, by := stillOpen(h, it); open || by != "replied" {
		t.Fatalf("an item without a conversation: open %v, by %q", open, by)
	}
	if f := followOf(h, it); f != nil {
		t.Fatalf("a filed item was given a follow: %+v", f)
	}
}

// Rewriting the wait from the conversation keeps how many looks found nothing and clears the "nobody to look" mark,
// and takes the new who, next and time. Confirmed and direct replies start that count over.
func TestInboxRewritingTheWaitKeepsTheLooks(t *testing.T) {
	h, _, it := handledHub(t)
	old := time.Now().UTC().Add(time.Hour)
	h.inbox.mu.Lock()
	it.Follow = &InboxFollow{Who: "张三", Next: "等他回话", CheckAt: &old, Checks: 2, Due: true}
	h.inbox.setHandling(it, HandlingWaiting, "", time.Now())
	h.inbox.mu.Unlock()

	if _, err := suggestTool(t, h, `{"recap":"又问了一次","follow":{"who":"张三的同事","next":"等他给新时间","check_in":"4h"}}`); err != nil {
		t.Fatal(err)
	}
	f := followOf(h, it)
	if f == nil || f.Checks != 2 || f.Due {
		t.Fatalf("after the conversation rewrote the wait: %+v", f)
	}
	if f.Who != "张三的同事" || f.Next != "等他给新时间" {
		t.Fatalf("the new wait was not taken: %+v", f)
	}
	wantCheckIn(t, h, it, 4*time.Hour, "rewritten from the conversation")

	// A confirmed reply's default wait starts them over.
	if _, err := suggestTool(t, h, `{"kind":"reply","text":"可以，周四给你"}`); err != nil {
		t.Fatal(err)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`); code != 200 {
		t.Fatalf("confirm: %d %v", code, out)
	}
	if f := followOf(h, it); f == nil || f.Checks != 0 || f.Due {
		t.Fatalf("after a confirmed reply: %+v", f)
	}
	wantCheckIn(t, h, it, 24*time.Hour, "confirmed reply after looks")

	// A direct reply starts them over too.
	h.inbox.mu.Lock()
	it.Follow.Checks, it.Follow.Due = 2, true
	h.inbox.mu.Unlock()
	if code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/"+it.ID+"/reply", `{"text":"再补一句"}`); code != 200 {
		t.Fatalf("direct reply: %d %v", code, out)
	}
	if f := followOf(h, it); f == nil || f.Checks != 0 || f.Due {
		t.Fatalf("after a direct reply: %+v", f)
	}

	// New messages start the count over too.
	h.inbox.mu.Lock()
	it.Last, it.Context, it.UpdatedAt = "102", "飞书私聊（张三）的新消息：\n[11-05 12:00] 张三：好的", time.Now()
	h.inbox.mu.Unlock()
	h.inbox.escalate(time.Now().Add(-time.Hour), time.Now())
	if f := followOf(h, it); f == nil || f.Checks != 0 || f.Due {
		t.Fatalf("after new messages: %+v", f)
	}
}

// New messages for an item waiting on the other side clear how many looks found nothing and the "nobody to look"
// mark, and keep who, the next step and the time.
func TestInboxNewMessagesClearTheLooks(t *testing.T) {
	h, _, it := handledHub(t)
	at := time.Now().UTC().Add(time.Hour)
	h.inbox.mu.Lock()
	it.Follow = &InboxFollow{Who: "张三", Next: "等他回话", CheckAt: &at, Checks: 2, Due: true}
	h.inbox.setHandling(it, HandlingWaiting, "", time.Now())
	it.Last, it.Context, it.UpdatedAt = "101", "飞书私聊（张三）的新消息：\n[11-05 11:00] 张三：在吗", time.Now()
	h.inbox.mu.Unlock()

	h.inbox.escalate(time.Now().Add(-time.Hour), time.Now())

	f := followOf(h, it)
	if f == nil || f.Checks != 0 || f.Due {
		t.Fatalf("follow after new messages: %+v", f)
	}
	if f.Who != "张三" || f.Next != "等他回话" || f.CheckAt == nil || !f.CheckAt.Equal(at) {
		t.Fatalf("new messages dropped what it waits on: %+v", f)
	}
	if state, wake, _ := handlingNow(h, it); state != HandlingLooking || !wake {
		t.Fatalf("new messages did not start a turn: %q wake %v", state, wake)
	}
}

// The user says something to an item waiting on the other side, and that turn only updates the recap: the item goes
// back to waiting, not to done.
func TestInboxInstructRecapOnlyReturnsToWaiting(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"recap":"张三要报价","follow":{"who":"张三","next":"等他给报价","check_in":"4h"}}`); err != nil {
		t.Fatal(err)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"先问问他预算"}`); code != 200 {
		t.Fatalf("instruct: %d %v", code, out)
	}
	if state, _, _ := handlingNow(h, it); state != HandlingLooking {
		t.Fatalf("after the instruction: %q", state)
	}
	if _, err := suggestTool(t, h, `{"recap":"问过了，等他回预算"}`); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := handlingNow(h, it); state != HandlingWaiting {
		t.Fatalf("a recap-only turn after the instruction: %q", state)
	}
	// And once more: a stale "back" left on the item must not send a later recap-only turn elsewhere.
	if _, err := suggestTool(t, h, `{"recap":"还是在等他回预算"}`); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := handlingNow(h, it); state != HandlingWaiting {
		t.Fatalf("a second recap-only turn: %q", state)
	}
	if f := followOf(h, it); f == nil || f.Who != "张三" || f.Checks != 0 {
		t.Fatalf("the follow went away with the instruction: %+v", f)
	}
}

// The same while an Agent it started is working.
func TestInboxInstructRecapOnlyReturnsToAgent(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.mu.Lock()
	h.inbox.setHandling(it, HandlingAgent, "", time.Now())
	h.inbox.mu.Unlock()
	if code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"顺便看看文档"}`); code != 200 {
		t.Fatalf("instruct: %d %v", code, out)
	}
	if _, err := suggestTool(t, h, `{"recap":"交给它去查了"}`); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := handlingNow(h, it); state != HandlingAgent {
		t.Fatalf("a recap-only turn after the instruction: %q", state)
	}
}

// Rejecting a suggestion on an item that waits on the other side, with a turn that only updates the recap, also
// goes back to waiting.
func TestInboxRejectRecapOnlyReturnsToWaiting(t *testing.T) {
	h, _, it := handledHub(t)
	h.inbox.mu.Lock()
	it.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestReply, Text: "可以，周四给你", At: time.Now().UTC()}
	it.SuggestSeq = 1
	h.inbox.setHandling(it, HandlingWaiting, "", time.Now())
	h.inbox.mu.Unlock()
	if code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"reject"}`); code != 200 {
		t.Fatalf("reject: %d %v", code, out)
	}
	if state, _, _ := handlingNow(h, it); state != HandlingLooking {
		t.Fatalf("after the rejection: %q", state)
	}
	if _, err := suggestTool(t, h, `{"recap":"他不要这个说法，等新口径"}`); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := handlingNow(h, it); state != HandlingWaiting {
		t.Fatalf("a recap-only turn after the rejection: %q", state)
	}
}

// A listed item waiting on the other side exposes the shared state key from the real nowKey.
func TestInboxWaitingListedAsWaiting(t *testing.T) {
	h, _, it := handledHub(t)
	if _, err := suggestTool(t, h, `{"recap":"等张三给报价","follow":{"who":"张三","next":"等他给报价","check_in":"4h"}}`); err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	listed := it.listed()
	h.inbox.mu.Unlock()
	if listed.Now != NowWaiting {
		t.Fatalf("listed now = %q, want %q", listed.Now, NowWaiting)
	}
}
