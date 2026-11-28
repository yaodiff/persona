package extmgr

import (
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

// Reading a group with /read (bots_read.go, #11803): with read_group on, the owner's "@bot /read …" in any group is
// handed to the assistant marked as a read, kept as a source and audited; nothing else is, and with the switch off
// nothing changes. The bot may answer with cards under that message only. All ids are placeholders.

// readOn turns the read_group switch on, as the settings page does.
func (w *botWorld) readOn(t *testing.T) {
	t.Helper()
	if code, body := w.do(t, "PUT", "/api/v1/bots/feishu", `{"features":{"read_group":true}}`); code != 200 {
		t.Fatalf("switch on: %d %s", code, body)
	}
}

// The switch is off by default: the owner's "@bot /read …" in a group that is not opted in is dropped without a word,
// as any other command there, and in a topic of an opted-in group it is an ordinary topic message.
func TestReadGroupOffByDefault(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	if f, _ := w.feishu(t, assistantwire.FeishuStatusGet, nil)["features"].(map[string]any); f["read_group"] != false {
		t.Fatalf("read_group must be off by default: %v", f)
	}
	total := w.api.total()
	w.send(t, askEvent("om_read_off", "ou_me", "@_user_1 /read 看下怎么回事", atBot, nil))
	w.quiet(t, total, "om_marker_read_off")
	if got := w.handed(); len(got) != 1 {
		t.Fatalf("handed %v: only the marker should have been", got)
	}
	if set := w.settings(); len(set.AISources) != 0 {
		t.Fatalf("a source was kept with the switch off: %+v", set.AISources)
	}

	w.send(t, botMessageEvent("om_read_topic", "ou_me", "text", textContent("@_user_1 /read 看下"), map[string]any{
		"chat_type": "group", "chat_id": topicChat, "thread_id": "omt_1", "mentions": atBot}))
	eventually(t, 5*time.Second, "handed", func() bool { got := w.handed(); return got[len(got)-1] == "om_read_topic" })
	if id, from := w.takenFrom(t); id != "om_read_topic" || from != "" {
		t.Fatalf("handed %q from %q, want an ordinary topic message", id, from)
	}
}

// With the switch on, the owner's "@bot /read …" goes to the assistant marked as a read from any group, opted in or not,
// in a topic or not; it is kept as a source of its own kind and audited without the words. The group does not become one
// the bot sends into.
func TestReadGroupOwnerCommandIsHandedMarked(t *testing.T) {
	needsLarkModule(t)
	for _, c := range []struct{ name, chat, thread, text string }{
		{"a group that is not opted in", askChat, "", "@_user_1 /read 看下这几条告警"},
		{"no words", askChat, "", "@_user_1 /read"},
		{"the Chinese alias", askChat, "", "@_user_1 /读群 近两天的内容"},
		{"upper case, full-width slash", askChat, "", "@_user_1 ／READ 看下"},
		{"a topic of a group that is not opted in", askChat, "omt_9", "@_user_1 /read 看下"},
		{"an opted-in group, outside a topic", topicChat, "", "@_user_1 /read 看下"},
		{"an opted-in group, in a topic", topicChat, "omt_1", "@_user_1 /read 看下"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newBotWorld(t)
			w.pair(t)
			w.topicsOn(t, topicChat)
			w.readOn(t)
			extra := map[string]any{"chat_type": "group", "chat_id": c.chat, "mentions": atBot}
			if c.thread != "" {
				extra["thread_id"] = c.thread
			}
			w.send(t, botMessageEvent("om_read1", "ou_me", "text", textContent(c.text), extra))
			eventually(t, 5*time.Second, "handed", func() bool { return len(w.handed()) == 1 })
			id, from := w.takenFrom(t)
			var m assistantwire.FeishuMessage
			_ = json.Unmarshal(w.a.got(assistantwire.MethodFeishuMessage)[0], &m)
			if id != "om_read1" || from != assistantwire.FeishuFromRead || m.Action != "" {
				t.Fatalf("handed %q from %q with action %q", id, from, m.Action)
			}
			set := w.settings()
			if len(set.AISources) != 1 || set.AISources[0].ChatID != c.chat || set.AISources[0].MessageID != "om_read1" ||
				set.AISources[0].Kind != botSourceRead {
				t.Fatalf("sources %+v", set.AISources)
			}
			if c.chat == askChat && botChatAllowed(set, askChat) {
				t.Fatal("the group became a chat the bot may send into")
			}
			if set.Owner.ChatID != "oc_p2p" {
				t.Fatalf("it moved the owner's private chat to %q", set.Owner.ChatID)
			}
			w.audited(t, auditBotReadTaken, func(detail string) {
				if !strings.Contains(detail, c.chat) || strings.Contains(detail, "看下") || strings.Contains(detail, "告警") {
					t.Errorf("audit detail %s", detail)
				}
			})
		})
	}
}

// Anything short of "the owner, @-ing the bot, starting with /read" does nothing and says nothing, with the switch on.
func TestReadGroupEverythingElseIsDroppedSilently(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	w.readOn(t)
	total := w.api.total()
	otherAt := []map[string]any{{"key": "@_user_1", "id": map[string]any{"open_id": "ou_someone"}, "name": "Someone"}}
	for _, c := range []struct {
		name     string
		from     string
		text     string
		mentions []map[string]any
		typ      string
	}{
		{"someone else's /read that @s the bot", "ou_stranger", "@_user_1 /read 看下", atBot, ""},
		{"the owner's /read with no @", "ou_me", "/read 看下", nil, ""},
		{"the owner's /read @-ing someone else", "ou_me", "@_user_1 /read 看下", otherAt, ""},
		{"the owner's /reader", "ou_me", "@_user_1 /reader 看下", atBot, ""},
		{"/read not at the start", "ou_me", "@_user_1 请 /read 看下", atBot, ""},
		{"the owner's /read as a merged forward", "ou_me", "@_user_1 /read 看下", atBot, "merge_forward"},
		{"the owner's /read as a picture", "ou_me", "@_user_1 /read 看下", atBot, "image"},
	} {
		typ := c.typ
		if typ == "" {
			typ = "text"
		}
		content := textContent(c.text)
		if typ == "merge_forward" || typ == "image" {
			content = `{}`
		}
		extra := map[string]any{"chat_type": "group", "chat_id": askChat}
		if c.mentions != nil {
			extra["mentions"] = c.mentions
		}
		w.send(t, botMessageEvent("om_r_"+strings.ReplaceAll(c.name, " ", "_"), c.from, typ, content, extra))
	}
	w.quiet(t, total, "om_marker_read")
	if got := w.handed(); len(got) != 1 {
		t.Fatalf("handed %v: only the marker should have been", got)
	}
	if set := w.settings(); len(set.AISources) != 0 {
		t.Fatalf("sources kept for messages that were dropped: %+v", set.AISources)
	}
}

// The send rule for a /read source: cards and short lines under that message go, at most botReadReplies, into its own
// chat and no other; a send into the group, a card under any other message, a reply naming another chat and a full
// answer are refused; a reply whose original is gone is not turned into a message of the group.
func TestReadGroupAnswerGoesUnderTheMessageOnly(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.topicsOn(t, topicChat)
	w.readOn(t)
	w.send(t, askEvent("om_rsrc", "ou_me", "@_user_1 /read 看下", atBot, nil))
	eventually(t, 5*time.Second, "taken", func() bool { return len(w.handed()) == 1 })
	w.api.inChat("om_rsrc", askChat)
	w.api.inChat("om_other", askChat)
	card := botcard.NewCard("t", "green", "a").Markdown("a card").JSON()

	before := w.api.total()
	for name, c := range map[string]struct {
		method string
		params map[string]any
	}{
		"a card into the group":                 {assistantwire.FeishuPost, map[string]any{"card": card, "chat_id": askChat}},
		"a card under another message of it":    {assistantwire.FeishuPost, map[string]any{"card": card, "chat_id": askChat, "reply_to": "om_other"}},
		"a card under the source, another chat": {assistantwire.FeishuPost, map[string]any{"card": card, "chat_id": topicChat, "reply_to": "om_rsrc"}},
		"a text into the group":                 {assistantwire.FeishuText, map[string]any{"text": "你好", "chat_id": askChat}},
		"a full answer under the source":        {assistantwire.FeishuText, map[string]any{"text": "你好", "reply_to": "om_rsrc", "full_answer": true}},
	} {
		err := w.sendErr(c.method, c.params)
		var re *rpc.Error
		if err == nil || !errors.As(err, &re) || re.Code != assistantwire.CodeChatDenied {
			t.Errorf("%s: %v, want a refusal of the chat", name, err)
		}
	}
	if sent := w.api.since(before); strings.Contains(sent, "POST /open-apis/im/v1/messages") {
		t.Fatalf("something was sent for a refused call:\n%s", sent)
	}

	// Cards and a line under the source go, with or without its chat named, in its thread when asked.
	if err := w.sendErr(assistantwire.FeishuPost, map[string]any{"card": card, "reply_to": "om_rsrc"}); err != nil {
		t.Fatalf("a card under the source: %v", err)
	}
	if err := w.sendErr(assistantwire.FeishuPost, map[string]any{"card": card, "reply_to": "om_rsrc", "chat_id": askChat, "in_thread": true}); err != nil {
		t.Fatalf("a card under the source, its chat named: %v", err)
	}
	if err := w.sendErr(assistantwire.FeishuText, map[string]any{"text": "有两个文件太大，没下载", "reply_to": "om_rsrc"}); err != nil {
		t.Fatalf("a line under the source: %v", err)
	}
	w.api.find(t, "POST /open-apis/im/v1/messages/om_rsrc/reply", "a card")
	w.api.find(t, "POST /open-apis/im/v1/messages/om_rsrc/reply", `"reply_in_thread":true`)
	w.api.find(t, "POST /open-apis/im/v1/messages/om_rsrc/reply", "没下载")

	// Bounded: botReadReplies in all, then refused.
	for i := 3; i < botReadReplies; i++ {
		if err := w.sendErr(assistantwire.FeishuPost, map[string]any{"card": card, "reply_to": "om_rsrc", "uuid": fmt.Sprintf("piece-%d", i)}); err != nil {
			t.Fatalf("reply %d: %v", i+1, err)
		}
	}
	if err := w.sendErr(assistantwire.FeishuPost, map[string]any{"card": card, "reply_to": "om_rsrc", "uuid": "one-too-many"}); err == nil {
		t.Fatal("a reply past the limit went")
	}
	if botChatAllowed(w.settings(), askChat) {
		t.Fatal("the group is a chat the bot may send into")
	}

	// The original gone: the card is lost, not sent into the group in place of it.
	w.send(t, askEvent("om_rsrc_gone", "ou_me", "@_user_1 /read 另一件", atBot, nil))
	eventually(t, 5*time.Second, "taken", func() bool { return len(w.handed()) == 2 })
	w.api.mu.Lock()
	w.api.rejectReply = 230011
	w.api.mu.Unlock()
	before = w.api.total()
	if err := w.sendErr(assistantwire.FeishuPost, map[string]any{"card": card, "reply_to": "om_rsrc_gone"}); !botcard.MessageGone(err) {
		t.Fatalf("a card whose original is gone: %v", err)
	}
	if sent := w.api.since(before); strings.Contains(sent, "receive_id_type=chat_id") {
		t.Fatalf("it was sent into the group instead:\n%s", sent)
	}
}

// An "/ai" source keeps its own rule: a card under it is still refused, the read switch on or off.
func TestReadGroupLeavesAISourcesAsTheyWere(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.readOn(t)
	w.send(t, askEvent("om_ai", "ou_me", "@_user_1 /ai 查一下", atBot, nil))
	eventually(t, 5*time.Second, "taken", func() bool { return len(w.handed()) == 1 })
	if _, from := w.takenFrom(t); from != assistantwire.FeishuFromElsewhere {
		t.Fatalf("an /ai was handed as %q", from)
	}
	w.api.inChat("om_ai", askChat)
	card := botcard.NewCard("t", "green", "a").Markdown("a card").JSON()
	if err := w.sendErr(assistantwire.FeishuPost, map[string]any{"card": card, "reply_to": "om_ai"}); err == nil {
		t.Fatal("a card under an /ai source went")
	}
}

// read_group_direct (#14437) is off by default, so a /read's answer goes to the owner first; the settings page turns it
// on and off like any other switch, and the channel lists it.
func TestReadGroupDirectSwitch(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	if f, _ := w.feishu(t, assistantwire.FeishuStatusGet, nil)["features"].(map[string]any); f["read_group_direct"] != false {
		t.Fatalf("read_group_direct must be off by default: %v", f)
	}
	for _, on := range []bool{true, false} {
		if code, body := w.do(t, "PUT", "/api/v1/bots/feishu", fmt.Sprintf(`{"features":{"read_group_direct":%t}}`, on)); code != 200 {
			t.Fatalf("switch %t: %d %s", on, code, body)
		}
		if f, _ := w.feishu(t, assistantwire.FeishuStatusGet, nil)["features"].(map[string]any); f["read_group_direct"] != on {
			t.Fatalf("read_group_direct after switching it %t: %v", on, f)
		}
	}
	if err := w.h.configureFeishu(w.ctx, nil, map[string]bool{"read_group_direct": true}); err != nil {
		t.Fatalf("the assistant's switch: %v", err)
	}
	found := false
	for _, f := range w.h.feishuChannel(w.ctx).Features {
		if f.ID == "read_group_direct" {
			found = f.On && f.Title != "" && f.Title != "hub-channels.feature_read_group_direct" && f.Description != ""
		}
	}
	if !found {
		t.Fatalf("the channel's features: %+v", w.h.feishuChannel(w.ctx).Features)
	}
}

// The buttons of a /read answer held for the owner (Post to group, Don't post) go through the same card press as any
// other: only the owner's press reaches the assistant, and it carries no owner action (it answers no interaction).
func TestReadHeldPressIsTheOwnersOnly(t *testing.T) {
	needsLarkModule(t)
	w := newBotWorld(t)
	w.pair(t)
	w.readOn(t)
	w.a.on(assistantwire.MethodFeishuAction, func(json.RawMessage) (any, error) {
		return map[string]any{"toast": map[string]any{"type": "success", "content": "posted"}}, nil
	})
	press := func(from string) string {
		b, _ := json.Marshal(map[string]any{"schema": "2.0", "header": map[string]any{"event_type": "card.action.trigger"},
			"event": map[string]any{"operator": map[string]any{"open_id": from},
				"action":  map[string]any{"value": map[string]any{"a": "rd", "id": "rd-example", "do": "post"}, "tag": "button"},
				"context": map[string]any{"open_message_id": "om_held", "open_chat_id": "oc_p2p"}}})
		res, err := w.h.botEvent(w.ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(res)
		return string(raw)
	}
	if out := press("ou_stranger"); !strings.Contains(out, "只有机器人的主人") || len(w.a.got(assistantwire.MethodFeishuAction)) != 0 {
		t.Fatalf("a stranger's press: %s", out)
	}
	if out := press("ou_me"); !strings.Contains(out, "posted") {
		t.Fatalf("the owner's press: %s", out)
	}
	got := w.a.got(assistantwire.MethodFeishuAction)
	var a assistantwire.FeishuAction
	if len(got) != 1 || json.Unmarshal(got[0], &a) != nil || a.Action != "" || !strings.Contains(string(a.Event), "rd-example") {
		t.Fatalf("handed %s", got)
	}
}
