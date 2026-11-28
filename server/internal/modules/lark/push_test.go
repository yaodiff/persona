package lark

import "testing"

func TestAgentDoneState(t *testing.T) {
	for _, c := range []struct {
		head   string
		asks   bool
		title  string
		color  string
		tagKey string
	}{
		{"修登录", false, "修登录", "green", "agent_tag_done"},
		{"修登录", true, "修登录", "orange", "agent_tag_asks"},
		{"⏳ 修登录", false, "修登录", "orange", "agent_tag_blocked"},
		{"⚠️ 修登录", false, "修登录", "red", "agent_tag_failed"},
	} {
		title, color, tag := agentDoneState(c.head, c.asks)
		if title != c.title || color != c.color || tag != "assistant-feishu_cards."+c.tagKey {
			t.Errorf("%q asks=%v: %q %q %q", c.head, c.asks, title, color, tag)
		}
	}
}
