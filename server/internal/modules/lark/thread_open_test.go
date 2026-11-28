package lark

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
)

// Feishu may send a topic's first message with no root_id, or with its own id as the root: both open one. A message
// that replies into the topic (a root that is another message) and one outside any topic do not. (That such a message
// opens one to-do is the assistant's TestIsTopicRoot.)
func TestIsTopicRootShapes(t *testing.T) {
	for _, c := range []struct {
		name                   string
		thread, root, messages string
		want                   bool
	}{
		{"no root_id", "omt_a", "", "om_1", true},
		{"its own id as the root", "omt_a", "om_1", "om_1", true},
		{"a reply to the root", "omt_a", "om_0", "om_1", false},
		{"outside a topic", "", "", "om_1", false},
		{"outside a topic, own root", "", "om_1", "om_1", false},
	} {
		var ev botcard.MessageEvent
		ev.Message.ThreadID, ev.Message.RootID, ev.Message.MessageID = c.thread, c.root, c.messages
		if got := isTopicRoot(ev); got != c.want {
			t.Errorf("%s: isTopicRoot = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTopicOpeningByMode(t *testing.T) {
	for _, tc := range []struct {
		text     string
		root     bool
		want     string
		explicit bool
	}{
		{"What is the error?", true, "", false},
		{"Another question", false, "", false},
		{"/todo fix the error", true, "fix the error", true},
		{"／TODO　fix the error", false, "fix the error", true},
		{"/todo", false, "", true},
		{"/ai fix the error", false, "fix the error", true},
		{"Please make a to-do to fix the error", false, "fix the error", true},
		{"建个待办：修复错误", false, "修复错误", true},
		{"ToDoを作ってください：エラーを直す", false, "エラーを直す", true},
		{"Make a to-do list", false, "", false},
		{"/todos fix the error", false, "", false},
		{"Could you explain what 'make a to-do' means?", false, "", false},
	} {
		t.Run(tc.text, func(t *testing.T) {
			var ev botcard.MessageEvent
			ev.Message.ThreadID, ev.Message.MessageID, ev.Message.MessageType = "omt_1", "om_2", "text"
			if !tc.root {
				ev.Message.RootID = "om_1"
			}
			raw, _ := json.Marshal(map[string]string{"text": tc.text})
			ev.Message.Content = string(raw)
			content := botcard.ParseContent("text", ev.Message.Content, nil)
			for _, mode := range []assistantwire.TopicTodoMode{"", assistantwire.TopicAnswerOnly, assistantwire.TopicMainDecides, assistantwire.TopicAlwaysTodo} {
				wantText, wantOpen := tc.want, tc.explicit
				if !wantOpen && tc.root && mode == assistantwire.TopicAlwaysTodo {
					wantText, wantOpen = tc.text, true
				}
				if text, open := topicOpening(ev, content, mode); text != wantText || open != wantOpen {
					t.Errorf("%q: got (%q, %v), want (%q, %v)", mode, text, open, wantText, wantOpen)
				}
			}
		})
	}
}
