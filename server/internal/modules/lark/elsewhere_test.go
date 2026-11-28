package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// Asking from any group (elsewhere.go, #6146): a request opens a topic in the home topic group and a to-do bound to it.
// The assistant's feishu_elsewhere_test.go runs it through the hub's call and the to-dos; here the entry's own rules.

const askHome = "oc_home" // the home topic group

// elsewhereHub is a paired bot with a home topic group: a new message there opens a topic (om_root1 in omt_home).
func elsewhereHub(t *testing.T) (*Service, *fakeHub) {
	f := newFakeHub()
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_owner", PairedAt: time.Unix(100, 0),
			Features: FeishuFeatures{Main: true, Agents: true, Cards: true}, Chats: []string{askHome}, HomeChat: askHome}, nil
	})
	roots := 0
	f.answerApp(AppFeishu, FeishuText, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		if m["chat_id"] == askHome && m["reply_to"] == nil {
			roots++
			return map[string]any{"message_id": fmt.Sprintf("om_root%d", roots)}, nil
		}
		return map[string]any{}, nil
	})
	f.answerApp(AppFeishu, FeishuMessages, func(p json.RawMessage) (any, error) {
		var m struct {
			MessageID string `json:"message_id"`
		}
		_ = json.Unmarshal(p, &m)
		if strings.HasPrefix(m.MessageID, "om_root") {
			return map[string]any{"items": []map[string]any{{"message_id": m.MessageID, "chat_id": askHome, "thread_id": "omt_home", "msg_type": "text"}}}, nil
		}
		return map[string]any{"items": []any{}}, nil
	})
	f.answerApp(AppFeishu, FeishuReact, func(json.RawMessage) (any, error) { return map[string]any{"reaction_id": "r1"}, nil })
	s := newTestService(t, f)
	s.setLarkTiming("topic_lookup_wait", time.Nanosecond)
	s.ch().main = "m1"
	s.ch().sessions["server"] = []proto.Session{{SessionID: "m1"}}
	return s, f
}

// reactions are the messages the bot reacted to.
func reactions(f *fakeHub) []string {
	var out []string
	for _, p := range f.appParams(AppFeishu, FeishuReact) {
		out = append(out, p["message_id"].(string))
	}
	return out
}

// The entry knows nothing of who received the request: a Source built by any channel opens the same topic, and a
// channel the report cannot go back through is refused before anything is posted.
func TestOpenFromElsewhereIsIdentityAgnostic(t *testing.T) {
	s, f := elsewhereHub(t)
	c := s.chat()
	todo, err := c.openFromElsewhere(context.Background(), Source{Channel: channel.Feishu, ChatID: "oc_owner_own_chat", MessageID: "om_own1", Text: "整理一下会议纪要", Quote: "上周的纪要见文档"})
	opened := s.ch().opened
	if err != nil || todo == "" || len(opened) != 1 || opened[0].Source == nil || opened[0].Source.ChatID != "oc_owner_own_chat" ||
		!opened[0].Topic || opened[0].ChatID != askHome {
		t.Fatalf("%q %+v %v", todo, opened, err)
	}
	if len(reactions(f)) != 1 || reactions(f)[0] != "om_root1" { // only the run's own, on the topic's first message
		t.Fatalf("reactions %v", reactions(f))
	}
	before := len(f.appParams(AppFeishu, FeishuText))
	for name, src := range map[string]Source{
		"a channel it cannot answer through": {Channel: "wechat", ChatID: "c", MessageID: "m", Text: "x"},
		"no chat":                            {Channel: channel.Feishu, MessageID: "m", Text: "x"},
		"no message":                         {Channel: channel.Feishu, ChatID: "c", Text: "x"},
	} {
		if _, err := c.openFromElsewhere(context.Background(), src); err == nil || errors.Is(err, errNoWords) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := c.openFromElsewhere(context.Background(), Source{Channel: channel.Feishu, ChatID: "c", MessageID: "m", Text: "  "}); !errors.Is(err, errNoWords) {
		t.Fatalf("no words: %v", err)
	}
	if got := len(f.appParams(AppFeishu, FeishuText)); got != before || len(s.ch().opened) != 1 {
		t.Fatalf("something was posted or made for a refused source: %d texts (was %d), %d todos", got, before, len(s.ch().opened))
	}
}
