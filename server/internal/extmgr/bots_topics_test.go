package extmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Topic group chats (bots_topics.go, #5279): the owner opts a group in, and only the owner's messages in a topic of it
// reach the assistant. All ids are placeholders; the private chat is the botWorld's oc_p2p.

const (
	topicChat  = "oc_topic"
	topicOther = "oc_other"
)

// groupEvent is a text message in a group chat; thread "" is a message outside any topic.
func groupEvent(id, from, text, chat, thread string) []byte {
	extra := map[string]any{"chat_type": "group", "chat_id": chat}
	if thread != "" {
		extra["thread_id"] = thread
	}
	return botMessageEvent(id, from, "text", textContent(text), extra)
}

func (w *botWorld) send(t *testing.T, ev []byte) {
	t.Helper()
	if _, err := w.h.botEvent(w.ctx, ev); err != nil {
		t.Fatal(err)
	}
}

// topicsOn opts the group chats in, as the app does.
func (w *botWorld) topicsOn(t *testing.T, chats ...string) {
	t.Helper()
	var list []string
	for _, c := range chats {
		list = append(list, fmt.Sprintf(`{"chat_id":%q}`, c))
	}
	if code, out := w.do(t, "PUT", "/api/v1/bots/feishu", `{"topic_chats":[`+strings.Join(list, ",")+`]}`); code != 200 {
		t.Fatalf("topic_chats: %d %v", code, out)
	}
}

// botStarted waits for the two calls each bot start makes in the background (restartBotLocked: turning the bot on, new
// credentials, a restart; pairing and other settings changes do not start it): the read of its own identity (GET
// /bot/v3/info, #14219) and the long connection's first attempt (POST /callback/ws/endpoint, which fails in tests,
// #14986). A count of the calls the bot makes taken before they end would count them as calls a message caused; where
// the message needs the bot's id before the read is back, it reads the id itself, a second call (#16350). newBotWorld
// waits for its start.
func (w *botWorld) botStarted(t *testing.T) {
	t.Helper()
	eventually(t, 5*time.Second, "the bot's start-up calls", func() bool {
		w.h.bots.mu.Lock()
		read, conn := w.h.bots.info.OpenID != "", w.h.bots.conn
		w.h.bots.mu.Unlock()
		return read && conn != nil && !conn.State().Since.IsZero()
	})
}

func (w *botWorld) settings() FeishuBotSettings {
	_, set := w.h.botAPI()
	return set
}

// quiet says the bot did nothing for the messages sent since total was read, and that they were all taken: a marker
// message from the owner in the opted-in topic goes after them (the hub takes messages in order), and once it is handed
// over, the ones before it have been through.
func (w *botWorld) quiet(t *testing.T, total int, marker string) {
	t.Helper()
	w.send(t, groupEvent(marker, "ou_me", "marker", topicChat, "omt_1"))
	eventually(t, 5*time.Second, "the marker handed", func() bool {
		got := w.handed()
		return len(got) > 0 && got[len(got)-1] == marker
	})
	if n := w.api.total(); n != total {
		t.Fatalf("the bot made %d call(s) to Feishu for messages it should have dropped silently:\n%s", n-total, w.api.since(total))
	}
}

// 1. The owner's message in a topic of an opted-in group goes to the assistant, without an owner action (it answers no
// card), and it leaves the owner's private chat as it was (5).
func TestTopicOwnerMessageInAnOptedInGroupReachesTheAssistant(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	w.send(t, groupEvent("om_g1", "ou_me", "先发安卓", topicChat, "omt_1"))
	eventually(t, 5*time.Second, "handed", func() bool { return len(w.handed()) == 1 })
	var m assistantwire.FeishuMessage
	_ = json.Unmarshal(w.a.got(assistantwire.MethodFeishuMessage)[0], &m)
	if w.handed()[0] != "om_g1" || m.Action != "" || !strings.Contains(string(m.Event), `"thread_id":"omt_1"`) {
		t.Fatalf("handed %v, action %q, event %s", w.handed(), m.Action, m.Event)
	}
	if set := w.settings(); set.Owner.ChatID != "oc_p2p" {
		t.Fatalf("a group message moved the owner's private chat to %q", set.Owner.ChatID)
	}
}

// 2, 3, 4. Anything short of the five conditions is dropped without a word: no reply, no reaction, no "owner only"
// note, and nothing handed over.
func TestTopicMessagesThatAreNotTheOwnersInAnOptedInTopicAreDroppedSilently(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	total := w.api.total()
	for _, c := range []struct{ name, id, from, chat, thread string }{
		{"someone else in an opted-in topic", "om_x1", "ou_stranger", topicChat, "omt_1"},
		{"the owner in a group not opted in", "om_x2", "ou_me", topicOther, "omt_1"},
		{"the owner in an opted-in group, outside any topic", "om_x3", "ou_me", topicChat, ""},
		{"someone else in a group not opted in", "om_x4", "ou_stranger", topicOther, "omt_1"},
	} {
		w.send(t, groupEvent(c.id, c.from, c.name, c.chat, c.thread))
	}
	w.quiet(t, total, "om_marker")
	if got := w.handed(); len(got) != 1 {
		t.Fatalf("handed %v: only the marker should have been", got)
	}
	if set := w.settings(); set.Owner.ChatID != "oc_p2p" {
		t.Fatalf("a group message moved the owner's private chat to %q", set.Owner.ChatID)
	}
}

// Not paired: a group message is dropped too, with no pairing hint (the hint is for the private chat).
func TestTopicMessageBeforePairingIsDropped(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	time.Sleep(300 * time.Millisecond) // the bot's own start-up calls (its info) are done
	total := w.api.total()
	w.send(t, groupEvent("om_g0", "ou_me", "/topics", topicChat, "omt_1"))
	w.send(t, groupEvent("om_g00", "ou_me", "你好", topicChat, "omt_1"))
	time.Sleep(300 * time.Millisecond)
	if n := w.api.total(); n != total || len(w.handed()) != 0 || len(w.settings().TopicChats) != 0 {
		t.Fatalf("calls %d (was %d), handed %v, list %v", n, total, w.handed(), w.settings().TopicChats)
	}
}

// 6. /topics (or /话题群) in a group opts it in, but only from the owner, and says so in the group; /topics off takes
// it out; neither goes to the assistant; unpairing empties the list.
func TestTopicsCommand(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	total := w.api.total()
	w.send(t, groupEvent("om_c0", "ou_stranger", "/topics", topicChat, ""))
	w.send(t, groupEvent("om_c1", "ou_stranger", "/话题群", topicChat, "omt_1"))
	time.Sleep(300 * time.Millisecond)
	if n := w.api.total(); n != total || len(w.settings().TopicChats) != 0 {
		t.Fatalf("a stranger's command: calls %d (was %d), list %v", n, total, w.settings().TopicChats)
	}

	w.send(t, groupEvent("om_c2", "ou_me", "/topics", topicChat, ""))
	w.api.find(t, "/messages/om_c2/reply", "已开启")
	if set := w.settings(); len(set.TopicChats) != 1 || set.TopicChats[0].ChatID != topicChat || set.TopicChats[0].At.IsZero() {
		t.Fatalf("list %+v", set.TopicChats)
	}
	if set := w.settings(); set.Owner.ChatID != "oc_p2p" {
		t.Fatalf("the command moved the owner's private chat to %q", set.Owner.ChatID)
	}
	// Again: said so, not listed twice.
	w.send(t, groupEvent("om_c3", "ou_me", "/topics", topicChat, "omt_1"))
	if c := w.api.find(t, "/messages/om_c3/reply", "已经开启"); !strings.Contains(c, `"reply_in_thread":true`) { // from inside a topic: answered there
		t.Fatalf("a command in a topic was answered outside it: %s", c)
	}
	if c := w.api.find(t, "/messages/om_c2/reply", "已开启"); strings.Contains(c, "reply_in_thread") { // not in one: a plain reply
		t.Fatalf("a command outside a topic was answered in a thread: %s", c)
	}
	// The Chinese name, the full-width slash, and the @ of the bot in front of it (a group message starts with one).
	w.send(t, groupEvent("om_c4", "ou_me", "/话题群 off", topicChat, ""))
	w.api.find(t, "/messages/om_c4/reply", "已关闭")
	if len(w.settings().TopicChats) != 0 {
		t.Fatalf("not taken out: %+v", w.settings().TopicChats)
	}
	w.send(t, botMessageEvent("om_c5", "ou_me", "text", textContent("@_user_1 ／topics"), map[string]any{"chat_type": "group", "chat_id": topicOther,
		"mentions": []map[string]any{{"key": "@_user_1", "id": map[string]any{"open_id": "ou_bot"}, "name": "Persona"}}}))
	w.api.find(t, "/messages/om_c5/reply", "已开启")
	if set := w.settings(); len(set.TopicChats) != 1 || set.TopicChats[0].ChatID != topicOther {
		t.Fatalf("list %+v", set.TopicChats)
	}
	w.send(t, groupEvent("om_c6", "ou_me", "/topics off", topicOther, ""))
	w.api.find(t, "/messages/om_c6/reply", "已关闭")
	w.send(t, groupEvent("om_c7", "ou_me", "/topics off", topicOther, ""))
	w.api.find(t, "/messages/om_c7/reply", "没有开启")
	if got := w.handed(); len(got) != 0 {
		t.Fatalf("a command went to the assistant: %v", got)
	}

	// Opted out, the group's topics are not read again; opted in and then unpaired, nothing stays.
	w.send(t, groupEvent("om_c8", "ou_me", "/topics", topicChat, ""))
	w.api.find(t, "/messages/om_c8/reply", "已开启")
	if code, _ := w.do(t, "DELETE", "/api/v1/bots/feishu/owner", ""); code != 200 {
		t.Fatalf("unpair: %d", code)
	}
	if set := w.settings(); len(set.TopicChats) != 0 {
		t.Fatalf("unpaired, the list is still %+v", set.TopicChats)
	}
	var saved FeishuBotSettings
	_, _ = w.h.store.Setting(context.Background(), botSettingsKey, &saved)
	if len(saved.TopicChats) != 0 || saved.Owner != nil {
		t.Fatalf("saved %+v", saved)
	}
}

// The app's list: PUT topic_chats replaces it (and needs a paired bot), GET shows it, a bad id is refused, and it is
// the list the gate uses.
func TestTopicChatsSetting(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"topic_chats":[{"chat_id":"oc_a"}]}`); code != 400 {
		t.Fatalf("a list with no owner: %d", code)
	}
	w.pair(t)
	for _, bad := range []string{`[{"chat_id":""}]`, `[{"chat_id":"oc a"}]`, `[{"chat_id":"oc_a"},{"chat_id":"oc_a"}]`} {
		if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"topic_chats":`+bad+`}`); code != 400 {
			t.Fatalf("%s: %d", bad, code)
		}
	}
	code, out := w.do(t, "PUT", "/api/v1/bots/feishu", `{"topic_chats":[{"chat_id":"oc_a","name":"发版"},{"chat_id":"`+topicChat+`"}]}`)
	list, _ := out["topic_chats"].([]any)
	if code != 200 || len(list) != 2 || list[0].(map[string]any)["name"] != "发版" || list[0].(map[string]any)["added_at"] == nil {
		t.Fatalf("put: %d %v", code, out)
	}
	// Another change leaves it; the chats that stay keep their time.
	at := w.settings().TopicChats[0].At
	if code, out := w.do(t, "PUT", "/api/v1/bots/feishu", `{"features":{"main":true}}`); code != 200 || len(out["topic_chats"].([]any)) != 2 {
		t.Fatalf("a change of something else: %d %v", code, out)
	}
	w.do(t, "PUT", "/api/v1/bots/feishu", `{"topic_chats":[{"chat_id":"oc_a"}]}`)
	if got := w.settings().TopicChats; len(got) != 1 || !got[0].At.Equal(at) {
		t.Fatalf("%+v, want oc_a added at %v", got, at)
	}
	if _, out := w.do(t, "GET", "/api/v1/bots/feishu", ""); len(out["topic_chats"].([]any)) != 1 {
		t.Fatalf("view %v", out)
	}
	w.do(t, "PUT", "/api/v1/bots/feishu", `{"topic_chats":[]}`)
	if _, out := w.do(t, "GET", "/api/v1/bots/feishu", ""); out["topic_chats"] != nil {
		t.Fatalf("emptied, the view says %v", out["topic_chats"])
	}
	// What was added and taken out is in the audit.
	st := w.h.store.(*botStore)
	st.mu.Lock()
	var seen []string
	for _, r := range st.audit {
		seen = append(seen, r.Action)
	}
	st.mu.Unlock()
	if !strings.Contains(strings.Join(seen, " "), auditBotTopicChatAdded) || !strings.Contains(strings.Join(seen, " "), auditBotTopicChatRemoved) {
		t.Fatalf("audit: %v", seen)
	}
}

func (w *botWorld) sendErr(method string, params any) error {
	raw, _ := json.Marshal(params)
	_, ok, err := w.h.AppCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppFeishu, Method: method, Params: raw})
	if !ok {
		return fmt.Errorf("%s not handled", method)
	}
	return err
}

// 7. The bot sends into an opted-in group and not into another; a reply goes where its parent is, so a parent in a
// chat the bot may not send into is refused whatever chat_id says, and nothing is sent.
func TestTopicChatsSendAndReplyChecks(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	card := botcard.NewCard("t", "green", "a").Markdown("topic card").JSON()

	if err := w.sendErr(assistantwire.FeishuText, map[string]any{"text": "到话题群", "chat_id": topicChat}); err != nil {
		t.Fatal(err)
	}
	w.api.find(t, `"receive_id":"`+topicChat+`"`, "到话题群")
	if err := w.sendErr(assistantwire.FeishuPost, map[string]any{"card": card, "chat_id": topicChat}); err != nil {
		t.Fatal(err)
	}
	w.api.find(t, `"receive_id":"`+topicChat+`"`, "topic card")
	before := w.api.total()
	for _, p := range []map[string]any{{"text": "x", "chat_id": topicOther}, {"card": card, "chat_id": topicOther, "reply_to": "om_whatever"}} {
		method := assistantwire.FeishuText
		if p["card"] != nil {
			method = assistantwire.FeishuPost
		}
		if err := w.sendErr(method, p); !errors.Is(err, errBotChatDenied) {
			t.Fatalf("%s to a chat not opted in: %v", method, err)
		}
	}
	if w.api.total() != before {
		t.Fatal("something went to Feishu for a refused chat")
	}
	// The refusal carries a code on the bus, so the assistant can tell it from a failure that may have gone out.
	var re *rpc.Error
	if err := w.sendErr(assistantwire.FeishuText, map[string]any{"text": "x", "chat_id": topicOther}); !errors.As(err, &re) || re.Code != assistantwire.CodeChatDenied {
		t.Fatalf("the refusal of a chat: %v", err)
	}

	// Parents the hub saw arrive: in the opted-in topic (fine), in a group not opted in (refused).
	w.send(t, groupEvent("om_in", "ou_me", "话题里", topicChat, "omt_1"))
	eventually(t, 5*time.Second, "the first handed", func() bool { return len(w.handed()) == 1 })
	w.api.inChat("om_out", topicOther) // a message in a group not listed: Feishu says where it is
	if err := w.sendErr(assistantwire.FeishuText, map[string]any{"text": "回复话题", "chat_id": topicChat, "reply_to": "om_in", "in_thread": true}); err != nil {
		t.Fatal(err)
	}
	w.api.find(t, "/messages/om_in/reply", "回复话题", `"reply_in_thread":true`)
	before = w.api.posts()
	for _, c := range []struct {
		method string
		p      map[string]any
	}{
		{assistantwire.FeishuText, map[string]any{"text": "不该发", "reply_to": "om_out"}},
		{assistantwire.FeishuText, map[string]any{"text": "不该发", "chat_id": topicChat, "reply_to": "om_out", "in_thread": true}},
		{assistantwire.FeishuPost, map[string]any{"card": card, "reply_to": "om_out"}},
	} {
		if err := w.sendErr(c.method, c.p); !errors.Is(err, errBotReplyChat) {
			t.Fatalf("%s %v: %v, want %v", c.method, c.p, err, errBotReplyChat)
		}
	}
	if w.api.posts() != before {
		t.Fatal("a reply into a chat not allowed was sent")
	}

	// A parent the hub did not see: Feishu is asked which chat it is in (once), and that decides.
	w.api.mu.Lock()
	w.api.read["om_old_out"] = []map[string]any{{"message_id": "om_old_out", "chat_id": topicOther}}
	w.api.read["om_old_dm"] = []map[string]any{{"message_id": "om_old_dm", "chat_id": "oc_p2p"}}
	w.api.read["om_old_nochat"] = []map[string]any{{"message_id": "om_old_nochat"}}
	w.api.readCode["om_old_err"] = 99991
	w.api.readCode["om_old_gone"] = 230011
	w.api.mu.Unlock()
	if err := w.sendErr(assistantwire.FeishuText, map[string]any{"text": "不该发", "reply_to": "om_old_out"}); !errors.Is(err, errBotReplyChat) {
		t.Fatalf("a parent Feishu says is elsewhere: %v", err)
	}
	for range 2 { // a parent in a chat the bot may use is asked about once, then remembered
		if err := w.sendErr(assistantwire.FeishuText, map[string]any{"text": "回私聊里的旧消息", "reply_to": "om_old_dm"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := w.api.count("GET /open-apis/im/v1/messages/om_old_dm"); n != 1 {
		t.Fatalf("%d lookups of the same parent, want 1", n)
	}
	w.api.find(t, "/messages/om_old_dm/reply", "回私聊里的旧消息")
	for _, id := range []string{"om_old_nochat", "om_old_err"} { // cannot be told: refused
		if err := w.sendErr(assistantwire.FeishuText, map[string]any{"text": "不该发", "reply_to": id}); err == nil || errors.Is(err, errBotReplyChat) {
			t.Fatalf("%s: %v, want a refusal that says the chat could not be told", id, err)
		}
	}
	if w.api.count("不该发") != 0 {
		t.Fatal("a reply to a parent whose chat could not be told was sent")
	}
	// A parent withdrawn cannot be replied into at all: the answer goes to the chat the call names, as before.
	if err := w.sendErr(assistantwire.FeishuText, map[string]any{"text": "撤回了的", "reply_to": "om_old_gone", "chat_id": topicChat}); err != nil {
		t.Fatal(err)
	}
	w.api.find(t, "/messages/om_old_gone/reply", "撤回了的")
}

// 9. A press on a card in a group counts only from the owner, and only in a group the owner opted in; the private
// chat's cards work as ever.
func TestTopicChatCardPressIsTheOwnersAlone(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	w.a.on(assistantwire.MethodFeishuAction, func(json.RawMessage) (any, error) {
		return map[string]any{"toast": map[string]any{"type": "success", "content": "已选：李四"}}, nil
	})
	press := func(from, chat string) string {
		ctxMap := map[string]any{"open_message_id": "om_card"}
		if chat != "" {
			ctxMap["open_chat_id"] = chat
		}
		b, _ := json.Marshal(map[string]any{"schema": "2.0", "header": map[string]any{"event_type": "card.action.trigger"},
			"event": map[string]any{"operator": map[string]any{"open_id": from},
				"action":  map[string]any{"value": map[string]any{"a": "ix", "id": "ix_q", "q": "0", "v": "李四"}, "tag": "button"},
				"context": ctxMap}})
		res, err := w.h.botEvent(w.ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(res)
		return string(raw)
	}
	pressed := func() int { return len(w.a.got(assistantwire.MethodFeishuAction)) }

	if out := press("ou_stranger", topicChat); !strings.Contains(out, "只有机器人的主人") || pressed() != 0 {
		t.Fatalf("someone else's press in the group: %s", out)
	}
	if out := press("ou_me", topicOther); !strings.Contains(out, "没有为机器人开启") || pressed() != 0 {
		t.Fatalf("the owner's press in a group not opted in: %s", out)
	}
	if out := press("ou_me", topicChat); !strings.Contains(out, "李四") || pressed() != 1 {
		t.Fatalf("the owner's press in the opted-in group: %s", out)
	}
	if out := press("ou_me", "oc_p2p"); !strings.Contains(out, "李四") || pressed() != 2 {
		t.Fatalf("the owner's press in the private chat: %s", out)
	}
	w.api.inChat("om_card", "oc_p2p") // a press that names no chat goes by where its card is (the next test)
	if out := press("ou_me", ""); !strings.Contains(out, "李四") || pressed() != 3 {
		t.Fatalf("a press that names no chat: %s", out)
	}
	// Opted out, the cards left in the group are dead.
	w.topicsOn(t)
	if out := press("ou_me", topicChat); !strings.Contains(out, "没有为机器人开启") || pressed() != 3 {
		t.Fatalf("a press after the group was taken out: %s", out)
	}
}

// A press whose callback names no chat is judged by the chat of the card's message, not let through: a card in a group
// the owner did not opt in (or took out again) is dead whether or not the callback says where it is.
func TestCardPressWithoutChatIsJudgedByItsMessage(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	w.a.on(assistantwire.MethodFeishuAction, func(json.RawMessage) (any, error) {
		return map[string]any{"toast": map[string]any{"type": "success", "content": "已选：李四"}}, nil
	})
	press := func(from, msg string) string {
		b, _ := json.Marshal(map[string]any{"schema": "2.0", "header": map[string]any{"event_type": "card.action.trigger"},
			"event": map[string]any{"operator": map[string]any{"open_id": from},
				"action":  map[string]any{"value": map[string]any{"a": "ix", "id": "ix_q", "q": "0", "v": "李四"}, "tag": "button"},
				"context": map[string]any{"open_message_id": msg}}})
		res, err := w.h.botEvent(w.ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(res)
		return string(raw)
	}
	pressed := func() int { return len(w.a.got(assistantwire.MethodFeishuAction)) }
	w.api.inChat("om_card_out", topicOther) // a card in a group that is not opted in
	w.api.inChat("om_card_dm", "oc_p2p")
	w.api.inChat("om_card_topic", topicChat)
	w.api.mu.Lock()
	w.api.readCode["om_card_err"] = 99991
	w.api.mu.Unlock()

	if out := press("ou_me", "om_card_out"); !strings.Contains(out, "没有为机器人开启") || pressed() != 0 {
		t.Fatalf("the owner's press on a card in a group not opted in, naming no chat: %s", out)
	}
	w.api.mu.Lock()
	w.api.read["om_card_other"] = []map[string]any{{"message_id": "om_somebody_else", "chat_id": "oc_p2p"}} // not about that message
	w.api.mu.Unlock()
	for _, id := range []string{"om_card_err", "om_card_other", "om_card_unseen", ""} { // the chat cannot be told
		if out := press("ou_me", id); !strings.Contains(out, "判断不出") || pressed() != 0 {
			t.Fatalf("a press whose card's chat cannot be told (%q): %s", id, out)
		}
	}
	if out := press("ou_me", "om_card_dm"); !strings.Contains(out, "李四") || pressed() != 1 {
		t.Fatalf("a press on a card in the private chat: %s", out)
	}
	if out := press("ou_me", "om_card_topic"); !strings.Contains(out, "李四") || pressed() != 2 {
		t.Fatalf("a press on a card in the opted-in group: %s", out)
	}
	// Opted out, a card the hub remembers (from the lookup above) is dead, with no callback naming its chat.
	w.topicsOn(t)
	if out := press("ou_me", "om_card_topic"); !strings.Contains(out, "没有为机器人开启") || pressed() != 2 {
		t.Fatalf("a press after the group was taken out: %s", out)
	}
	// Someone else is still turned away first.
	if out := press("ou_stranger", "om_card_dm"); !strings.Contains(out, "只有机器人的主人") || pressed() != 2 {
		t.Fatalf("someone else's press: %s", out)
	}
}

// A recall in an opted-in group goes to the assistant only for a message the hub handed over (the event does not say
// whose it was); one in a group not listed, or of a message never handed over, does not.
func TestTopicChatRecall(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	recall := func(chat, id string) {
		b, _ := json.Marshal(map[string]any{"schema": "2.0", "header": map[string]any{"event_id": "ev_r" + chat + id, "event_type": "im.message.recalled_v1"},
			"event": map[string]any{"message_id": id, "chat_id": chat, "recall_type": "message_owner"}})
		_, _ = w.h.botEvent(w.ctx, b)
	}
	w.send(t, groupEvent("om_handed", "ou_me", "在", topicChat, "omt_1"))
	eventually(t, 5*time.Second, "handed", func() bool { return len(w.handed()) == 1 })
	recall(topicOther, "om_handed")  // another group
	recall(topicChat, "om_stranger") // a message of someone else's, never handed over
	time.Sleep(200 * time.Millisecond)
	if w.a.wasTold(assistantwire.EventFeishuRecalled) {
		t.Fatal("a recall that is not of a handed message was passed on")
	}
	recall(topicChat, "om_handed")
	eventually(t, 5*time.Second, "told of the recall", func() bool { return w.a.wasTold(assistantwire.EventFeishuRecalled) })
}

func TestBotChatAllowedWithTopicChats(t *testing.T) {
	needsLarkModule(t)
	set := FeishuBotSettings{Owner: &BotOwner{OpenID: "ou_owner", ChatID: "oc_p2p"}, TopicChats: []BotChat{{ChatID: topicChat}}}
	for chat, want := range map[string]bool{"oc_p2p": true, topicChat: true, topicOther: false, "": false} {
		if got := botChatAllowed(set, chat); got != want {
			t.Errorf("botChatAllowed(%q) = %v, want %v", chat, got, want)
		}
	}
	set.Owner = nil // a list with no owner allows nothing
	if botChatAllowed(set, topicChat) {
		t.Error("a group is allowed with no owner")
	}
}

func TestTopicsCommandText(t *testing.T) {
	needsLarkModule(t)
	bot := []botcard.Mention{{Key: "@_user_1", Name: "Persona"}}
	for _, c := range []struct {
		text     string
		on, isIt bool
	}{
		{"/topics", true, true}, {"/Topics on", true, true}, {"  /topics off ", false, true}, {"／topics", true, true},
		{"/话题群", true, true}, {"/话题群 关", false, true}, {"@_user_1 /topics", true, true}, {"@Persona /topics off", false, true},
		{"/topics now", false, false}, {"/topics off now", false, false}, {"/topicsoff", false, false}, {"topics", false, false},
		{"@_user_1 请 /topics", false, false}, {"/new", false, false}, {"", false, false},
	} {
		cmd, isIt := topicsCmd(c.text, bot)
		if on := cmd == topicsOn || cmd == topicsHome; on != c.on || isIt != c.isIt { // "home" is on too
			t.Errorf("topicsCmd(%q) on = %v, %v; want %v, %v", c.text, on, isIt, c.on, c.isIt)
		}
	}
}

// An owner's topic message the assistant cannot take is answered that it could not be sent, in the topic.
func TestTopicMessageWhileTheAssistantIsAwayIsAnsweredInTheTopic(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	shortInboundWait(t)
	w.away(true)
	w.send(t, groupEvent("om_away", "ou_me", "在吗？", topicChat, "omt_1"))
	if c := w.api.find(t, "/messages/om_away/reply", "助手暂时连不上"); !strings.Contains(c, `"reply_in_thread":true`) {
		t.Fatalf("the note was not in the topic: %s", c)
	}
}

// What the hub remembers of where a message is: the owner's own, and what the bot sent. Strangers' messages do not push
// them out; a parent that is not remembered is asked about, and a lookup that blips still finds it.
func TestReplyParentLookup(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	gets := func(id string) int { return w.api.count("GET /open-apis/im/v1/messages/" + id) }
	reply := func(id string) error {
		return w.sendErr(assistantwire.FeishuText, map[string]any{"text": "回复 " + id, "reply_to": id})
	}

	// The owner's message in the DM is remembered, and so is what the bot sends: no lookup for either.
	w.send(t, botMessageEvent("om_keep", "ou_me", "text", textContent("在吗"), nil))
	eventually(t, 5*time.Second, "handed", func() bool { return len(w.handed()) == 1 })
	out := w.feishu(t, assistantwire.FeishuText, map[string]any{"text": "我在", "reply_to": "om_keep"})
	sent, _ := out["message_id"].(string)
	if sent == "" {
		t.Fatalf("no id for what was sent: %v", out)
	}
	// Strangers' messages (in a listed group too, and in their own chat with the bot) are not kept, however many.
	for i := range botMsgChatKeep + 20 {
		w.send(t, groupEvent(fmt.Sprintf("om_noise%d", i), "ou_stranger", "noise", topicChat, "omt_1"))
	}
	w.send(t, botMessageEvent("om_stranger_dm", "ou_stranger", "text", textContent("hi"), map[string]any{"chat_id": "oc_stranger_dm"}))
	w.api.find(t, "只为它的主人服务") // the one note a stranger in the DM gets
	w.quiet(t, w.api.total(), "om_marker_lookup")
	w.api.inChat("om_stranger_dm", "oc_stranger_dm")
	for i := 1; i <= 2; i++ { // a chat the bot may not use is not remembered: asked about each time
		if err := reply("om_stranger_dm"); !errors.Is(err, errBotReplyChat) || gets("om_stranger_dm") != i {
			t.Fatalf("a stranger's chat: %v, %d lookups", err, gets("om_stranger_dm"))
		}
	}
	for _, id := range []string{"om_keep", sent} {
		if err := reply(id); err != nil || gets(id) != 0 {
			t.Fatalf("reply to %s (remembered): %v, %d lookups", id, err, gets(id))
		}
	}

	// Not remembered any more (the table is full of the owner's own messages), Feishu is asked, and the answer is kept.
	for i := range botMsgChatKeep + 1 {
		w.h.rememberMessageChat(fmt.Sprintf("om_fill%d", i), "oc_p2p")
	}
	w.api.inChat("om_keep", "oc_p2p")
	for range 2 {
		if err := reply("om_keep"); err != nil {
			t.Fatal(err)
		}
	}
	if gets("om_keep") != 1 {
		t.Fatalf("%d lookups of a forgotten parent, want 1", gets("om_keep"))
	}

	// A lookup that blips once is read again by the client and succeeds; one that keeps failing is refused, and sends nothing.
	w.api.inChat("om_blip", "oc_p2p")
	w.api.mu.Lock()
	w.api.readFail["om_blip"] = 1
	w.api.readFail["om_down"] = 10
	w.api.mu.Unlock()
	if err := reply("om_blip"); err != nil {
		t.Fatalf("a lookup that failed once: %v", err)
	}
	if err := reply("om_down"); err == nil || errors.Is(err, errBotReplyChat) {
		t.Fatalf("a lookup that keeps failing: %v", err)
	}
	if w.api.count("回复 om_down") != 0 {
		t.Fatal("a reply was sent although its parent's chat could not be told")
	}

	// The answer must be about this message: another one in the list is not it.
	w.api.mu.Lock()
	w.api.read["om_mixed"] = []map[string]any{{"message_id": "om_other", "chat_id": "oc_p2p"}}
	w.api.mu.Unlock()
	if err := reply("om_mixed"); err == nil || errors.Is(err, errBotReplyChat) {
		t.Fatalf("an answer about another message: %v", err)
	}
}

// An agent message may answer in an opted-in topic, but never under a parent in a chat the bot may not use.
func TestCotCreateParentMustBeInAnAllowedChat(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	w.send(t, groupEvent("om_cot_topic", "ou_me", "在", topicChat, "omt_1"))
	eventually(t, 5*time.Second, "handed", func() bool { return len(w.handed()) == 1 })
	w.topicsOn(t) // the parent's chat is remembered, but is no longer one the bot may write in
	before := w.api.total()
	if err := w.sendErr(assistantwire.FeishuCotCreate, map[string]any{"reply_to": "om_cot_topic"}); !errors.Is(err, errBotReplyChat) {
		t.Fatalf("an agent message answering a message in a chat that is not opted in: %v", err)
	}
	if w.api.total() != before {
		t.Fatalf("something was sent:\n%s", w.api.since(before))
	}
}

// /topics is the hub's: the list is changed before the next message is let go (pass). So a message right after
// /topics off is not handed over, and one right after /topics is.
func TestTopicsCommandIsInForceForTheNextMessage(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	api := w.h.bots.api
	at := func(id, text, thread string) []string { // the list at the moment the next message is let go
		var seen []string
		ev := groupEvent(id, "ou_me", text, topicChat, thread)
		var m botcard.MessageEvent
		var env struct {
			Event json.RawMessage `json:"event"`
		}
		_ = json.Unmarshal(ev, &env)
		_ = json.Unmarshal(env.Event, &m)
		w.h.botGroupMessage(api, w.h.bots.set.copy(), m, env.Event, time.Now(), func() {
			for _, c := range w.settings().TopicChats {
				seen = append(seen, c.ChatID)
			}
		})
		return seen
	}
	if got := at("om_n1", "/topics", ""); len(got) != 1 || got[0] != topicChat {
		t.Fatalf("the next message is let go with the list %v after /topics", got)
	}
	if got := at("om_n3", "/topics off", "omt_1"); len(got) != 0 {
		t.Fatalf("the next message is let go with the list %v after /topics off", got)
	}
	// And end to end, one right after the other.
	w.send(t, groupEvent("om_e1", "ou_me", "/topics", topicChat, ""))
	w.send(t, groupEvent("om_e2", "ou_me", "紧跟着的话", topicChat, "omt_1"))
	eventually(t, 5*time.Second, "the message right after /topics handed", func() bool { return len(w.handed()) == 1 })
	w.send(t, groupEvent("om_e3", "ou_me", "/topics off", topicChat, "omt_1"))
	w.send(t, groupEvent("om_e4", "ou_me", "关了之后的话", topicChat, "omt_1"))
	w.api.find(t, "/messages/om_e3/reply", "已关闭")
	time.Sleep(300 * time.Millisecond)
	if got := w.handed(); len(got) != 1 || got[0] != "om_e2" {
		t.Fatalf("handed %v, want only om_e2", got)
	}
}

// A group message from an app (a bot) is dropped even when it carries the owner's open id; and after unpairing and
// pairing again the list starts empty and the command works again.
func TestTopicGroupMessageFromAnAppAndRePairing(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	ev := groupEvent("om_app", "ou_me", "来自机器人", topicChat, "omt_1")
	var m map[string]any
	_ = json.Unmarshal(ev, &m)
	m["event"].(map[string]any)["sender"].(map[string]any)["sender_type"] = "app"
	ev, _ = json.Marshal(m)
	w.send(t, ev)
	w.quiet(t, w.api.total(), "om_marker_app")
	if got := w.handed(); len(got) != 1 {
		t.Fatalf("handed %v: only the marker should have been", got)
	}

	if code, _ := w.do(t, "DELETE", "/api/v1/bots/feishu/owner", ""); code != 200 {
		t.Fatalf("unpair: %d", code)
	}
	_, out := w.do(t, "POST", "/api/v1/bots/feishu/pair", "")
	w.send(t, botMessageEvent("om_repair", "ou_me", "text", textContent(out["pairing"].(map[string]any)["code"].(string)), nil))
	eventually(t, 8*time.Second, "paired again", func() bool { return w.settings().Owner != nil })
	if got := w.settings().TopicChats; len(got) != 0 {
		t.Fatalf("paired again, the list is %+v", got)
	}
	n := len(w.handed())
	w.send(t, groupEvent("om_after", "ou_me", "还在吗", topicChat, "omt_1"))
	time.Sleep(300 * time.Millisecond)
	if len(w.handed()) != n {
		t.Fatal("a group that was on before the unpairing was still read")
	}
	w.send(t, groupEvent("om_again", "ou_me", "/topics", topicChat, ""))
	w.api.find(t, "/messages/om_again/reply", "已开启")
}

// A request that cannot keep its list is refused whole: a new App ID unpairs the owner, so a list sent with it is a 400
// and the App ID stays.
func TestTopicChatsWithANewAppIDChangeNothing(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"app_id":"cli_other","app_secret":"x","topic_chats":[{"chat_id":"oc_new"}]}`)
	if code != 400 {
		t.Fatalf("%d, want 400", code)
	}
	_, out := w.do(t, "GET", "/api/v1/bots/feishu", "")
	if out["app_id"] != "cli_test" || out["owner"] == nil || len(out["topic_chats"].([]any)) != 1 {
		t.Fatalf("something of the refused request was applied: %v", out)
	}
	// The same App ID is no change.
	if code, _ := w.do(t, "PUT", "/api/v1/bots/feishu", `{"app_id":"cli_test","topic_chats":[{"chat_id":"oc_new"}]}`); code != 200 {
		t.Fatalf("the same App ID with a list: %d", code)
	}
}

// feishu.status carries the chats the bot may send into, so the assistant can tell before it posts: the owner's
// private chat and the opted-in groups, nothing else, and nothing before pairing.
func TestFeishuStatusListsTheChatsTheBotMaySendInto(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	if st := w.feishu(t, assistantwire.FeishuStatusGet, nil); st["chats"] != nil {
		t.Fatalf("chats before pairing: %v", st["chats"])
	}
	w.pair(t)
	w.topicsOn(t, topicChat)
	st := w.feishu(t, assistantwire.FeishuStatusGet, nil)
	chats, _ := st["chats"].([]any)
	if len(chats) != 2 || chats[0] != "oc_p2p" || chats[1] != topicChat {
		t.Fatalf("chats %v", st["chats"])
	}
	for _, c := range chats {
		if !botChatAllowed(w.settings(), c.(string)) {
			t.Errorf("%v listed but not allowed", c)
		}
	}
}

// A line of the bot's own in an opted-in topic is not a reply (it comes with a sender that is not a user): the hub drops
// it, whatever open id it carries, so a to-do's own post into its topic can never come back as the owner's words.
func TestTopicBotsOwnPostIsDropped(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	total := w.api.total()
	for i, from := range []string{"ou_me", "ou_bot"} { // even one carrying the owner's own open id
		ev := bytes.Replace(groupEvent(fmt.Sprintf("om_own%d", i), from, "新决策：先发安卓", topicChat, "omt_1"), []byte(`"sender_type":"user"`), []byte(`"sender_type":"app"`), 1)
		if !bytes.Contains(ev, []byte(`"sender_type":"app"`)) {
			t.Fatal("event not rewritten")
		}
		w.send(t, ev)
	}
	w.quiet(t, total, "om_marker")
	if got := w.handed(); len(got) != 1 || got[0] != "om_marker" {
		t.Fatalf("handed %v: only the marker should have been", got)
	}
}
