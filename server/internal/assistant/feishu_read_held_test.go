package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A /read's answer held for the owner (the lark module's read_held.go, #14437): it goes to the owner's private chat with the bot,
// says which /read it answers, and has Post to group and Don't post; the group gets the same cards under the request
// only on Post to group, once, and never otherwise. All ids are placeholders.

// heldEnv is readEnv on a store, with Lark's posts recorded by where they went: landed keeps one message per key, as
// Lark does, and answer, when set, answers a post into the group instead.
type heldEnv struct {
	*threadEnv
	st     *memStore
	mu     sync.Mutex
	dm     []map[string]any
	group  []map[string]any
	landed map[string]bool
	answer func(n int, p map[string]any) (any, error)
	n      int
}

func newHeldEnv(t *testing.T) *heldEnv {
	t.Helper()
	needsLarkLayer(t)
	st := &memStore{}
	e := &heldEnv{threadEnv: newThreadEnv(t, st), st: st, landed: map[string]bool{}}
	e.features.ReadGroup = true
	e.publicURL = "https://hub.example.com"
	// Outside the fake's lock: a slow post into the group must not hold the service's other calls.
	e.f.answerAppBlocking(AppFeishu, FeishuPost, func(raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		e.mu.Lock()
		if p["chat_id"] == nil {
			e.dm = append(e.dm, p)
			id := fmt.Sprintf("om_dm%d", len(e.dm))
			e.mu.Unlock()
			return map[string]any{"message_id": id}, nil
		}
		e.n++
		n, answer := e.n, e.answer
		e.mu.Unlock()
		var out any = map[string]any{"message_id": "om_group"}
		var err error
		if answer != nil {
			out, err = answer(n, p)
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if err == nil || errors.Is(err, bus.ErrOffline) {
			e.land(p) // bus.ErrOffline: Lark took it, but the reply never came back
		}
		if err != nil {
			return nil, err
		}
		return out, nil
	})
	return e
}

// land is one post into the group as Lark keeps it (e.mu held).
func (e *heldEnv) land(p map[string]any) {
	key, _ := p["uuid"].(string)
	if !e.landed[key] {
		e.group = append(e.group, p)
	}
	e.landed[key] = true
}

// tries is how many posts into the group were tried.
func (e *heldEnv) tries() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.n
}

func (e *heldEnv) posts() (dm, group []map[string]any) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]map[string]any(nil), e.dm...), append([]map[string]any(nil), e.group...)
}

// ask is the owner's /read id answered with answer: the run, finished.
func (e *heldEnv) ask(id, text, thread, answer string) *channel.Run {
	e.t.Helper()
	e.read(readEvent(id, text, thread))
	run := e.readLive("fb_" + id)
	e.s.finishChannelRun(run, &channel.Result{Answer: answer})
	return run
}

var heldIDRe = regexp.MustCompile(`"a":"rd","do":"post","id":"([^"]+)"`)

// heldID is the held answer a private card's Post to group button posts, "" when it has none.
func heldID(card string) string {
	if m := heldIDRe.FindStringSubmatch(card); m != nil {
		return m[1]
	}
	return ""
}

// press presses a held answer's button (do: post or skip) on private message mid, as Lark delivers it.
func (e *heldEnv) press(id, do, mid string) *botcard.CardResponse {
	e.t.Helper()
	return pressHeld(e.t, e.s, id, do, mid)
}

// pressHeld is press on service s (the hub checked the owner pressed it, and hands the event over as it came).
func pressHeld(t testing.TB, s *Service, id, do, mid string) *botcard.CardResponse {
	t.Helper()
	var a botcard.CardAction
	a.Operator.OpenID = "ou_owner"
	a.Action.Value = map[string]any{"a": "rd", "id": id, "do": do}
	a.Context.MessageID = mid
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := s.FeishuAction(context.Background(), FeishuAction{Event: raw})
	if err != nil {
		t.Fatal(err)
	}
	if resp == nil {
		t.Fatal("the press was not answered")
	}
	return resp
}

func cardJSON(t *testing.T, r *botcard.CardResponse) string {
	t.Helper()
	if r == nil || r.Card == nil {
		return ""
	}
	b, err := json.Marshal(r.Card.Data)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func heldText(key string, args ...i18n.Args) string {
	return i18n.T("", "assistant-feishu."+key, args...)
}

// By default the answer goes to the owner's private chat: the cards the group would get, with which /read it answers
// and the two buttons, and nothing in the group but the reaction. With no press nothing is ever posted.
func TestFeishuReadHeldGoesToTheOwnerFirst(t *testing.T) {
	e := newHeldEnv(t)
	e.ask("om_h1", "/read 看下这几条告警是不是同一个原因", "", "结论：三条告警都来自同一次发布")
	if got := handoffs(e.f); len(got) != 1 || !strings.Contains(got[0].text, "这一轮的最终回答会先由机器人私聊发给用户") ||
		strings.Contains(got[0].text, "这一轮的最终回答会由机器人回复在触发消息下面") {
		t.Fatalf("the prompt does not say the answer goes to the owner first: %+v", got)
	}
	dm, group := e.posts()
	if len(group) != 0 || len(e.f.appParams(AppFeishu, FeishuText)) != 0 {
		t.Fatalf("something went into the group: posts %v, notes %v", group, e.f.appParams(AppFeishu, FeishuText))
	}
	if len(dm) != 1 {
		t.Fatalf("private posts %v", dm)
	}
	p := dm[0]
	card, _ := p["card"].(string)
	if p["reply_to"] != "" || p["in_thread"] != nil || p["no_detour"] != nil {
		t.Fatalf("the private card went elsewhere: %v", p)
	}
	for _, want := range []string{"三条告警都来自同一次发布", heldText("read_held_from", i18n.Args{"ask": "看下这几条告警是不是同一个原因"}),
		heldText("read_held_waiting"), heldText("read_held_post"), heldText("read_held_skip"), heldText("read_held_open"),
		"applink.feishu.cn/client/chat/open?openChatId=oc_alerts", `"do":"skip"`} {
		if !strings.Contains(card, want) {
			t.Errorf("the private card lacks %q: %s", want, card)
		}
	}
	if heldID(card) == "" || strings.Contains(card, "hub.example.com") || strings.Contains(card, "lark-cli") {
		t.Fatalf("the private card: %s", card)
	}
	if r := reactions(e.f); len(r) != 2 || r[0] != "om_h1" || r[1] != "om_h1" {
		t.Fatalf("reactions %v", r)
	}
	if react := e.f.appParams(AppFeishu, FeishuReact); react[1]["emoji"] != "DONE" {
		t.Fatalf("reactions %v", react)
	}
	if r, _ := e.s.channelRunByID("fb_om_h1"); r != nil {
		t.Fatal("the run did not end once its answer was held")
	}
}

// Post to group sends the same card under the request, with no process, no web link and none of the private lines,
// once: a second press, Lark's redelivery and a press after a restart post nothing more. The private card then says it
// was posted.
func TestFeishuReadHeldPostsOnce(t *testing.T) {
	e := newHeldEnv(t)
	e.ask("om_h2", "/read 看下", "", "结论：同一次发布")
	dm, _ := e.posts()
	id := heldID(dm[0]["card"].(string))
	r := e.press(id, "post", "om_dm1")
	if r.Toast == nil || r.Toast.Content != heldText("read_held_posted") || r.Toast.Type != "success" {
		t.Fatalf("toast %+v", r.Toast)
	}
	if c := cardJSON(t, r); !strings.Contains(c, heldText("read_held_posted")) || strings.Contains(c, `"a":"rd"`) || !strings.Contains(c, "同一次发布") {
		t.Fatalf("card after the press: %s", c)
	}
	_, group := e.posts()
	if len(group) != 1 {
		t.Fatalf("group posts %v", group)
	}
	g := group[0]
	card, _ := g["card"].(string)
	if g["reply_to"] != "om_h2" || g["chat_id"] != readChat || g["no_detour"] != true || g["uuid"] == "" || g["in_thread"] != nil {
		t.Fatalf("group post %v", g)
	}
	for _, not := range []string{heldText("read_held_waiting"), heldText("read_held_from_bare"), `"a":"rd"`, "applink", "hub.example.com", "lark-cli"} {
		if strings.Contains(card, not) {
			t.Fatalf("the group's card has %q: %s", not, card)
		}
	}
	if !strings.Contains(card, "同一次发布") {
		t.Fatalf("the group's card: %s", card)
	}
	// Pressed again, and pressed after a restart (another service on the same store): nothing more goes.
	if r := e.press(id, "post", "om_dm1"); r.Toast == nil || r.Toast.Content != heldText("read_held_posted") || r.Card != nil {
		t.Fatalf("second press %+v", r)
	}
	restarted := newServiceOn(t, e.f, e.st)
	if r := pressHeld(t, restarted, id, "post", "om_dm1"); r.Toast == nil || r.Toast.Content != heldText("read_held_posted") {
		t.Fatalf("press after a restart %+v", r.Toast)
	}
	if e.tries() != 1 {
		t.Fatalf("%d posts into the group", e.tries())
	}
}

// A post Lark took but whose receipt was lost leaves the buttons: the owner is told to press again, and the press
// goes under the same key, so the group gets one card.
func TestFeishuReadHeldPostRetryLandsOnce(t *testing.T) {
	e := newHeldEnv(t)
	e.answer = func(n int, _ map[string]any) (any, error) {
		if n == 1 {
			return nil, bus.ErrOffline
		}
		return map[string]any{"message_id": "om_group"}, nil
	}
	e.ask("om_h3", "/read 看下", "", "结论")
	dm, _ := e.posts()
	id := heldID(dm[0]["card"].(string))
	r := e.press(id, "post", "om_dm1")
	if r.Card != nil || r.Toast == nil || r.Toast.Content != heldText("read_held_try_again", i18n.Args{"why": heldText("read_held_unreachable")}) {
		t.Fatalf("first press %+v (card %s)", r.Toast, cardJSON(t, r))
	}
	r = e.press(id, "post", "om_dm1")
	if r.Toast == nil || r.Toast.Content != heldText("read_held_posted") {
		t.Fatalf("second press %+v", r.Toast)
	}
	_, group := e.posts()
	if e.tries() != 2 || len(group) != 1 {
		t.Fatalf("%d tries, %d cards in the group", e.tries(), len(group))
	}
}

// Don't post closes the card for good: nothing goes into the group then, nor on a later press.
func TestFeishuReadHeldDontPost(t *testing.T) {
	e := newHeldEnv(t)
	e.ask("om_h4", "/read 看下", "", "结论")
	dm, _ := e.posts()
	id := heldID(dm[0]["card"].(string))
	r := e.press(id, "skip", "om_dm1")
	if r.Toast == nil || r.Toast.Content != heldText("read_held_declined") {
		t.Fatalf("toast %+v", r.Toast)
	}
	if c := cardJSON(t, r); !strings.Contains(c, heldText("read_held_declined")) || strings.Contains(c, `"a":"rd"`) {
		t.Fatalf("card after the press: %s", c)
	}
	if r := e.press(id, "post", "om_dm1"); r.Toast == nil || r.Toast.Content != heldText("read_held_declined") {
		t.Fatalf("post after Don't post: %+v", r.Toast)
	}
	if _, group := e.posts(); len(group) != 0 || e.tries() != 0 {
		t.Fatalf("group posts %v", group)
	}
}

// When the hub refuses the post (the source expired or used up its replies) or Lark says the request is gone, nothing
// goes into the group as a message of its own: the private card says it was not posted and why, and pressing again
// posts nothing.
func TestFeishuReadHeldSourceGone(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		why  string
	}{
		{"refused", &rpc.Error{Code: CodeChatDenied, Message: "feishu: the bot may only reply to the original message"}, "read_held_denied"},
		{"recalled", errors.New("feishu reply: 230011 the message was withdrawn"), "read_held_gone"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newHeldEnv(t)
			e.answer = func(int, map[string]any) (any, error) { return nil, c.err }
			e.ask("om_h5", "/read 看下", "", "结论")
			dm, _ := e.posts()
			id := heldID(dm[0]["card"].(string))
			r := e.press(id, "post", "om_dm1")
			want := heldText("read_held_not_posted", i18n.Args{"why": heldText(c.why)})
			if r.Toast == nil || r.Toast.Content != want || !strings.Contains(cardJSON(t, r), want) || strings.Contains(cardJSON(t, r), `"a":"rd"`) {
				t.Fatalf("press: toast %+v, card %s", r.Toast, cardJSON(t, r))
			}
			if r := e.press(id, "post", "om_dm1"); r.Toast == nil || r.Toast.Content != want {
				t.Fatalf("press again %+v", r.Toast)
			}
			if _, group := e.posts(); len(group) != 0 || e.tries() != 1 {
				t.Fatalf("%d tries, group %v", e.tries(), group)
			}
		})
	}
}

// An answer split into cards goes to the owner as those cards, the buttons on the last; one press posts them all, in
// order, each under its own key.
func TestFeishuReadHeldManyCards(t *testing.T) {
	e := newHeldEnv(t)
	long := strings.Repeat("第一段的内容很多。\n\n", 700) + strings.Repeat("第二段的内容很多。\n\n", 700) + "最后一句"
	e.ask("om_h6", "/read 全部读一遍", "", long)
	dm, _ := e.posts()
	if len(dm) < 2 {
		t.Fatalf("%d private cards", len(dm))
	}
	for i, p := range dm {
		card := p["card"].(string)
		if (heldID(card) != "") != (i == len(dm)-1) || strings.Contains(card, heldText("read_held_from", i18n.Args{"ask": "全部读一遍"})) != (i == 0) {
			t.Fatalf("private card %d of %d: %s", i+1, len(dm), card)
		}
	}
	id := heldID(dm[len(dm)-1]["card"].(string))
	if r := e.press(id, "post", "om_dm9"); r.Toast == nil || r.Toast.Content != heldText("read_held_posted") {
		t.Fatalf("press %+v", r.Toast)
	}
	_, group := e.posts()
	if len(group) != len(dm) {
		t.Fatalf("%d cards in the group for %d private ones", len(group), len(dm))
	}
	keys := map[string]bool{}
	for i, g := range group {
		keys[g["uuid"].(string)] = true
		if g["reply_to"] != "om_h6" || !strings.Contains(g["card"].(string), map[bool]string{true: "最后一句", false: "第一段"}[i == len(group)-1]) {
			t.Fatalf("group card %d: %v", i+1, g)
		}
	}
	if len(keys) != len(group) {
		t.Fatalf("keys %v", keys)
	}
}

// In a topic the answer is still held, and posted into the topic.
func TestFeishuReadHeldInATopic(t *testing.T) {
	e := newHeldEnv(t)
	e.ask("om_h7", "/读群 近两天的", "omt_9", "好的")
	dm, group := e.posts()
	if len(dm) != 1 || len(group) != 0 || dm[0]["in_thread"] != nil {
		t.Fatalf("private %v, group %v", dm, group)
	}
	if r := e.press(heldID(dm[0]["card"].(string)), "post", "om_dm1"); r.Toast == nil || r.Toast.Content != heldText("read_held_posted") {
		t.Fatalf("press %+v", r.Toast)
	}
	if _, group = e.posts(); len(group) != 1 || group[0]["in_thread"] != true || group[0]["chat_id"] != readChat || group[0]["reply_to"] != "om_h7" {
		t.Fatalf("group %v", group)
	}
}

// A post that takes longer than Lark waits for the press says it is under way; the card says it was posted once it was.
func TestFeishuReadHeldSlowPost(t *testing.T) {
	e := newHeldEnv(t)
	release := make(chan struct{})
	e.ask("om_h8", "/read 看下", "", "结论")
	e.answer = func(int, map[string]any) (any, error) {
		<-release
		return map[string]any{"message_id": "om_group"}, nil
	}
	e.s.setLarkTiming("card_answer_wait", 20*time.Millisecond)
	dm, _ := e.posts()
	id := heldID(dm[0]["card"].(string))
	if r := e.press(id, "post", "om_dm1"); r.Toast == nil || r.Toast.Content != heldText("read_held_posting") || r.Card != nil {
		t.Fatalf("press %+v", r)
	}
	if r := e.press(id, "post", "om_dm1"); r.Toast == nil || r.Toast.Content != heldText("read_held_posting") {
		t.Fatalf("a press meanwhile %+v", r.Toast)
	}
	close(release)
	posted := func() bool {
		var updated string
		for _, u := range e.f.appParams(AppFeishu, FeishuUpdateCard) {
			if u["message_id"] == "om_dm1" {
				updated, _ = u["card"].(string)
			}
		}
		return strings.Contains(updated, heldText("read_held_posted"))
	}
	waitFor(t, "the card to say it was posted", posted)
	if _, group := e.posts(); len(group) != 1 || e.tries() != 1 {
		t.Fatalf("%d tries, group %v", e.tries(), group)
	}
}

// /read switched off since: Post to group posts nothing, and the buttons stay for when it is on again.
func TestFeishuReadHeldReadOff(t *testing.T) {
	e := newHeldEnv(t)
	e.ask("om_h9", "/read 看下", "", "结论")
	dm, _ := e.posts()
	id := heldID(dm[0]["card"].(string))
	e.features.ReadGroup = false
	if r := e.press(id, "post", "om_dm1"); r.Toast == nil || r.Toast.Content != heldText("read_held_read_off") || r.Card != nil {
		t.Fatalf("press %+v", r)
	}
	e.features.ReadGroup = true
	if r := e.press(id, "post", "om_dm1"); r.Toast == nil || r.Toast.Content != heldText("read_held_posted") {
		t.Fatalf("press %+v", r.Toast)
	}
	if e.tries() != 1 {
		t.Fatalf("%d posts into the group", e.tries())
	}
}

// An answer that could not be kept for the owner (the store refused it) is not lost: the run waits and tries again,
// and nothing went to the owner or the group meanwhile.
func TestFeishuReadHeldStoreFailureTriesAgain(t *testing.T) {
	e := newHeldEnv(t)
	e.read(readEvent("om_h10", "/read 看下", ""))
	run := e.readLive("fb_om_h10")
	e.st.mu.Lock()
	e.st.failPut = errors.New("store down")
	e.st.mu.Unlock()
	e.s.finishChannelRun(run, &channel.Result{Answer: "结论"})
	if dm, group := e.posts(); len(dm) != 0 || len(group) != 0 {
		t.Fatalf("posted while the answer could not be kept: %v %v", dm, group)
	}
	if r, _ := e.s.channelRunByID(run.ID); r == nil {
		t.Fatal("the run ended without its answer")
	}
	e.st.mu.Lock()
	e.st.failPut = nil
	e.st.mu.Unlock()
	e.s.finishChannelRun(run, &channel.Result{Answer: "结论"})
	if dm, _ := e.posts(); len(dm) != 1 || heldID(dm[0]["card"].(string)) == "" {
		t.Fatalf("private posts %v", dm)
	}
}

// What keeps a held /read from going is told to the owner privately, never in the group.
func TestFeishuReadHeldRefusalIsPrivate(t *testing.T) {
	e := newHeldEnv(t)
	e.features.Main = false
	e.read(readEvent("om_h11", "/read 看下", ""))
	dm, group := e.posts()
	if len(group) != 0 || len(e.f.appParams(AppFeishu, FeishuText)) != 0 || len(handoffs(e.f)) != 0 {
		t.Fatalf("group %v, notes %v", group, e.f.appParams(AppFeishu, FeishuText))
	}
	if len(dm) != 1 || !strings.Contains(dm[0]["card"].(string), heldText("read_held_from", i18n.Args{"ask": "看下"})) || heldID(dm[0]["card"].(string)) != "" {
		t.Fatalf("private %v", dm)
	}
}

// A press of a held answer nobody knows (pruned, or from another hub) posts nothing.
func TestFeishuReadHeldUnknownPress(t *testing.T) {
	e := newHeldEnv(t)
	if r := e.press("rd-unknown", "post", "om_dm1"); r.Toast == nil || r.Toast.Content != heldText("read_held_expired") {
		t.Fatalf("press %+v", r.Toast)
	}
	if e.tries() != 0 {
		t.Fatal("posted for an unknown answer")
	}
}
