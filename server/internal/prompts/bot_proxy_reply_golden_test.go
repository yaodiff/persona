package prompts

import (
	"os"
	"strings"
	"testing"
)

// The proxy reply is pinned for the three languages, with and without context, confirming and direct. Regenerate with
// UPDATE_GOLDEN=1 and read the diff.
func TestBotProxyReplyGolden(t *testing.T) {
	cases := []struct {
		name string
		m    BotProxyReply
	}{
		{"en_confirm_context", BotProxyReply{Language: "English", Owner: "Ada", Chat: "Team", Sender: "Bo", Question: "How do I call this?", Context: "earlier line", Max: 400, Confirm: true}},
		{"en_direct", BotProxyReply{Language: "English", Question: "What time is it?", Max: 1800}},
		{"zh_confirm", BotProxyReply{Owner: "Ada", Chat: "Team", Sender: "Bo", Question: "How do I call this?", Max: 400, Confirm: true}},
		{"zh_direct_context", BotProxyReply{Question: "What time is it?", Context: "earlier line", Max: 1800}},
		{"ja_confirm_context", BotProxyReply{Language: "日本語", Owner: "Ada", Chat: "Team", Sender: "Bo", Question: "How do I call this?", Context: "earlier line", Max: 400, Confirm: true}},
		{"ja_direct", BotProxyReply{Language: "日本語", Question: "What time is it?", Max: 1800}},
	}
	var b strings.Builder
	for _, c := range cases {
		b.WriteString("===== " + c.name + " =====\n")
		b.WriteString(RenderBotProxyReply(c.m))
		b.WriteString("\n")
	}
	got := b.String()
	const path = "testdata/bot_proxy_reply.golden"
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("proxy reply golden differs; set UPDATE_GOLDEN=1 and read the diff")
	}
}
