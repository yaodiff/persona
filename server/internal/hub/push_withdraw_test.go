package hub

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apns"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// withdrawHub is relayHub with the phone registered as an app that takes pushes back, and short waits.
func withdrawHub(t *testing.T, features ...string) (*Hub, *relayAPNs, func(apns.Request) map[string]any) {
	t.Helper()
	h, f, priv := relayHub(t)
	h.push.withdrawals.gather = 60 * time.Millisecond
	h.push.withdrawals.every = 400 * time.Millisecond
	d, _ := h.push.device("phone")
	d.Features = features
	h.push.register(d)
	return h, f, func(r apns.Request) map[string]any { return opened(t, priv, r) }
}

func card(id string) proto.Interaction {
	return proto.Interaction{ID: id, Kind: proto.IxPermission, MachineID: "m1", SessionID: "s1", Summary: "ls"}
}

func backgroundPushes(f *relayAPNs) []apns.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.DeleteFunc(slices.Clone(f.got), func(r apns.Request) bool { return r.PushType != "background" })
}

// Cards answered elsewhere close together are taken back in one silent push, sealed for the relay like any other;
// a card that was never pushed is not named.
func TestWithdrawTakesCardsBackInOnePush(t *testing.T) {
	h, f, open := withdrawHub(t, "withdraw", "something-else")
	if d, _ := h.push.device("phone"); !slices.Equal(d.Features, []string{"withdraw"}) {
		t.Fatalf("features %v", d.Features)
	}
	for _, id := range []string{"ix_a", "ix_b", "ix_c"} {
		h.notifyInteraction("", card(id), "笔记本", false)
	}
	f.wait(t, 3)
	h.push.withdraw("ix_a")
	h.push.withdraw("ix_never") // muted, or before the app said it takes pushes back
	h.push.withdraw("ix_b")
	h.dropCard("", "ix_c") // gone from the hub's list for another reason: the same
	got := f.wait(t, 4)
	time.Sleep(150 * time.Millisecond)
	if bg := backgroundPushes(f); len(bg) != 1 {
		t.Fatalf("%d background pushes, want 1", len(bg))
	}
	r := got[3]
	if r.PushType != "background" || r.Priority != "5" {
		t.Fatalf("withdrawal %+v", r)
	}
	p := open(r)
	if ids, _ := p["resolved_interaction_ids"].([]any); len(ids) != 3 || ids[0] != "ix_a" || ids[1] != "ix_b" || ids[2] != "ix_c" {
		t.Fatalf("payload %v", p)
	}
	if aps, _ := p["aps"].(map[string]any); aps["content-available"] != float64(1) || aps["alert"] != nil || aps["sound"] != nil {
		t.Fatalf("aps %v", p["aps"])
	}
	// Told once: the same card over again (the full list after it) sends nothing more.
	h.push.withdraw("ix_a")
	time.Sleep(150 * time.Millisecond)
	if bg := backgroundPushes(f); len(bg) != 1 {
		t.Fatalf("%d background pushes after a repeat", len(bg))
	}
}

func TestWithdrawPayloadNamesTheResolvedOccurrence(t *testing.T) {
	h, f, open := withdrawHub(t, "withdraw")
	ix := card("ix_occurrence")
	ix.CreatedAt = time.Date(2026, 11, 25, 2, 3, 4, 123456789, time.UTC)
	h.notifyInteraction("", ix, "notebook", false)
	f.wait(t, 1)
	reportResolvedAt(t, h, ix.ID, ix.CreatedAt)
	got := f.wait(t, 2)
	p := open(got[1])
	cards, _ := p["resolved_interactions"].([]any)
	if len(cards) != 1 {
		t.Fatalf("withdrawal has no occurrence-bound entries: %v", p)
	}
	entry, _ := cards[0].(map[string]any)
	if entry["id"] != ix.ID || entry["created_at"] != ix.CreatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("withdrawal occurrence = %v, want %s", entry, ix.CreatedAt.Format(time.RFC3339Nano))
	}
}

// One withdrawal push a device a minute (here 400 ms): what comes between waits and goes together.
func TestWithdrawAtMostOnceAMinute(t *testing.T) {
	h, f, open := withdrawHub(t, "withdraw")
	for _, id := range []string{"ix_a", "ix_b", "ix_c"} {
		h.notifyInteraction("", card(id), "笔记本", false)
	}
	f.wait(t, 3)
	h.push.withdraw("ix_a")
	first := f.wait(t, 4)[3]
	sent := time.Now()
	h.push.withdraw("ix_b")
	time.Sleep(100 * time.Millisecond)
	h.push.withdraw("ix_c")
	time.Sleep(150 * time.Millisecond)
	if bg := backgroundPushes(f); len(bg) != 1 {
		t.Fatalf("%d background pushes inside the minute", len(bg))
	}
	second := f.wait(t, 5)[4]
	if since := time.Since(sent); since < 350*time.Millisecond {
		t.Fatalf("second withdrawal after %v", since)
	}
	if ids, _ := open(first)["resolved_interaction_ids"].([]any); len(ids) != 1 || ids[0] != "ix_a" {
		t.Fatalf("first %v", ids)
	}
	if ids, _ := open(second)["resolved_interaction_ids"].([]any); len(ids) != 2 || ids[0] != "ix_b" || ids[1] != "ix_c" {
		t.Fatalf("second %v", ids)
	}
}

// Runtime ids may be 128 characters: forty of them do not fit one push (the relay seals about 2.7 KB, APNs takes 4 KB),
// so they go in several, each within the limit, and every card is named once.
func TestWithdrawSplitsLongIDsBySize(t *testing.T) {
	h, f, open := withdrawHub(t, "withdraw")
	var ids []string
	for i := range withdrawMost {
		id := fmt.Sprintf("ix_%03d_", i) + strings.Repeat("a", 120)
		ids = append(ids, id)
		h.push.cardPushedTo(id, "phone") // as sendCard notes it; forty card pushes would hit the relay's burst limit
	}
	for _, id := range ids {
		h.push.withdraw(id)
	}
	var got []string
	for n := 1; len(got) < len(ids); n++ {
		r := f.wait(t, n)[n-1]
		if r.PushType != "background" || len(r.Body) > apnsMaxAlert {
			t.Fatalf("withdrawal %d: %s push of %d bytes", n, r.PushType, len(r.Body))
		}
		batch, _ := open(r)["resolved_interaction_ids"].([]any)
		if len(batch) == 0 || len(batch) == len(ids) {
			t.Fatalf("withdrawal %d named %d cards", n, len(batch))
		}
		for _, id := range batch {
			got = append(got, id.(string))
		}
	}
	if !slices.Equal(got, ids) {
		t.Fatalf("withdrawn %d cards, want the %d in order", len(got), len(ids))
	}
	if r, _ := h.push.routeOfID("phone"); r.Reason != "" {
		t.Fatalf("route %+v", r)
	}
}

// An app that did not say it takes pushes back (an older one) gets no background push for it.
func TestWithdrawOnlyToAppsThatTakeItBack(t *testing.T) {
	h, f, _ := withdrawHub(t)
	h.notifyInteraction("", card("ix_a"), "笔记本", false)
	f.wait(t, 1)
	h.push.withdraw("ix_a")
	time.Sleep(200 * time.Millisecond)
	if bg := backgroundPushes(f); len(bg) != 0 {
		t.Fatalf("an older app got %d background pushes", len(bg))
	}
}

// Only the devices the card's push went to are told; a card pushed too long ago is forgotten.
func TestWithdrawOnlyWhereTheCardWent(t *testing.T) {
	p := newPusher(t.TempDir(), nil, t.Logf)
	now := time.Now()
	p.now = func() time.Time { return now }
	p.cardPushedTo("ix_a", "phone")
	p.cardPushedTo("ix_old", "phone")
	p.ownerCardPushedTo("plan_old", "phone") // an assistant card's push: remembered for days
	now = now.Add(withdrawKept + time.Second)
	p.cardPushedTo("ix_b", "ipad")
	p.withdrawals.mu.Lock()
	_, kept := p.withdrawals.pushed["ix_old"]
	_, planKept := p.withdrawals.pushed["plan_old"]
	p.withdrawals.mu.Unlock()
	if kept {
		t.Fatal("a card pushed half an hour ago is still remembered")
	}
	if !planKept {
		t.Fatal("an assistant card's push was forgotten after half an hour")
	}
	p.withdrawals.gather = time.Hour // queued, not sent: what is waiting is what is checked
	p.withdraw("ix_b")
	p.withdrawals.mu.Lock()
	defer p.withdrawals.mu.Unlock()
	if q := p.withdrawals.queue["ipad"]; q == nil || !slices.Equal(q.cards, []withdrawCard{{ID: "ix_b"}}) {
		t.Fatalf("ipad queue %+v", q)
	}
	if q := p.withdrawals.queue["phone"]; q != nil {
		t.Fatalf("phone was told about a card it never got: %+v", q.cards)
	}
	for _, q := range p.withdrawals.queue {
		q.timer.Stop()
	}
}

// An assistant card's push is remembered for ownerCardKept, then forgotten like any other.
func TestAnAssistantCardsPushIsRememberedForDays(t *testing.T) {
	p := newPusher(t.TempDir(), nil, t.Logf)
	now := time.Now()
	p.now = func() time.Time { return now }
	p.ownerCardPushedTo("plan_old", "phone")
	remembered := func() bool {
		p.withdrawals.mu.Lock()
		defer p.withdrawals.mu.Unlock()
		_, ok := p.withdrawals.pushed["plan_old"]
		return ok
	}
	now = now.Add(ownerCardKept - time.Minute)
	p.cardPushedTo("ix_a", "ipad") // a push prunes what is forgotten
	if !remembered() {
		t.Fatal("an assistant card's push was forgotten before ownerCardKept")
	}
	now = now.Add(2 * time.Minute)
	p.cardPushedTo("ix_b", "ipad")
	if remembered() {
		t.Fatal("an assistant card's push is remembered past ownerCardKept")
	}
}

// A manager's push that stands for a card (a plan's, assistantwire.Notification.CardID) names the card like an
// interaction's push, and the card's end takes it back the same way.
func TestWithdrawTakesBackAPushStandingForACard(t *testing.T) {
	h, f, open := withdrawHub(t, "withdraw")
	h.notifyDoneUnder("", "assistant", "m1", "笔记本", "s1", "A plan waits for Start", "修登录崩溃", pushWants{}, managerPushCollapseID("", "plan:plan_x"), "",
		pushPolicy{level: "P0", cardID: "plan_x"})
	got := f.wait(t, 1)
	if p := open(got[0]); p["interaction_id"] != "plan_x" || p["card_id"] != "plan_x" {
		t.Fatalf("the push does not name its card: %v", p)
	}
	h.push.withdraw("plan_x")
	got = f.wait(t, 2)
	if p := open(got[1]); got[1].PushType != "background" || fmt.Sprint(p["resolved_interaction_ids"]) != "[plan_x]" {
		t.Fatalf("withdrawal %+v %v", got[1], p)
	}
}

// A decision's push names its card (TodoAskCardID) as card_id, never as interaction_id, and once the to-do no longer
// waits on the owner the hub takes it back.
func TestADecisionsPushIsTakenBackOnceItIsOver(t *testing.T) {
	h, f, open := withdrawHub(t, "withdraw")
	todo := assistantwire.NotifyTodo{ID: "t1", AskedAt: time.Now(), Recommend: "a"}
	h.notifyDecideKeyed("", "m1", "笔记本", "s1", "Pick a vendor", "A or B", todo, assistantwire.TodoPushKey("t1", assistantwire.TodoPushDecide))
	got := f.wait(t, 1)
	card := assistantwire.TodoAskCardID("t1")
	if p := open(got[0]); p["card_id"] != card || p["interaction_id"] != nil {
		t.Fatalf("the decision's push does not name its card as card_id only: %v", p)
	}
	h.push.withdraw(card)
	got = f.wait(t, 2)
	if p := open(got[1]); got[1].PushType != "background" || fmt.Sprint(p["resolved_interaction_ids"]) != "["+card+"]" {
		t.Fatalf("withdrawal %+v %v", got[1], p)
	}
}
