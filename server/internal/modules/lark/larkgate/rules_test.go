package larkgate

import (
	"strings"
	"testing"
)

func TestCLIRead(t *testing.T) {
	g := New()
	for _, tc := range []struct {
		method, risk string
		want         bool
	}{
		{"CLI", "read", true},
		{"CLI", "missing", true},
		{"CLI", "write", false},
		{"CLI", "high-risk-write", false},
		{"CLI", "", false},
		{"GET", "", true},
		{"HEAD", "", true},
		{"POST", "", false},
		{"DELETE", "", false},
	} {
		if got := g.CLIRead(tc.method, tc.risk); got != tc.want {
			t.Errorf("CLIRead(%q, %q) = %v, want %v", tc.method, tc.risk, got, tc.want)
		}
	}
}

func TestClassifyAction(t *testing.T) {
	g := New()
	for _, tc := range []struct {
		method, path string
		what         string
		boundary     int
		matched      bool
	}{
		{"CLI", "/im/messages/delete", "撤回消息", boundaryDeletion, true},
		{"CLI", "/drive/files/delete", "删除", boundaryDeletion, true},
		{"CLI", "/docs/+remove-member", "删除", boundaryDeletion, true},
		{"CLI", "/drive/files/comments/create", "发表评论", boundaryOutbound, true},
		{"CLI", "/mail/user_mailboxes/messages/send", "发邮件", boundaryOutbound, true},
		{"CLI", "/mail/+messages-send", "发邮件", boundaryOutbound, true},
		{"CLI", "/im/messages/create", "发消息", boundaryOutbound, true},
		{"CLI", "/im/+messages-send", "发消息", boundaryOutbound, true},
		{"CLI", "/im/+messages-reply", "回复消息", boundaryOutbound, true},
		{"CLI", "/im/reactions/create", "表情回复", boundaryOutbound, true},
		{"CLI", "/im/messages/forward", "转发消息", boundaryOutbound, true},
		{"CLI", "/im/messages/urgent", "加急", boundaryOutbound, true},
		{"CLI", "/im/+chat-update", "改群 / 会话", boundaryOrdinary, true},
		{"CLI", "/markdown/+write", "写文档", boundaryOrdinary, true},
		{"CLI", "/task/+create", "改待办", boundaryOrdinary, true},
		{"CLI", "/approval/+approve", "处理审批", boundaryOrdinary, true},
		{"CLI", "/calendar/+create", "建 / 改日程", boundaryOrdinary, true},
		{"CLI", "/docs/+create", "写文档", boundaryOrdinary, true},
		{"CLI", "/wiki/+move", "写文档", boundaryOrdinary, true},
		{"CLI", "/mail/folders/patch", "发 / 改邮件", boundaryOrdinary, true},
		// HTTP routes, including OpenAPI IM, are left to core so they remain guarded without this module.
		{"DELETE", "/files/1", "", boundaryOrdinary, false},
		{"POST", "/conversations/1/messages", "", boundaryOrdinary, false},
		{"POST", "/open-apis/im/v1/messages", "", boundaryOrdinary, false},
		{"POST", "/im/v1/messages", "", boundaryOrdinary, false},
		{"POST", "/im/messages", "", boundaryOrdinary, false},
		{"POST", "/open-apis/im/v1/batch_messages", "", boundaryOrdinary, false},
		{"PATCH", "/open-apis/im/v1/threads", "", boundaryOrdinary, false},
		{"POST", "/notes", "", boundaryOrdinary, false},
	} {
		what, boundary, matched := g.ClassifyAction(tc.method, tc.path)
		if what != tc.what || boundary != tc.boundary || matched != tc.matched {
			t.Errorf("ClassifyAction(%q, %q) = (%q, %d, %v), want (%q, %d, %v)",
				tc.method, tc.path, what, boundary, matched, tc.what, tc.boundary, tc.matched)
		}
	}
}

func TestCLIBypassSafeAndBotActor(t *testing.T) {
	g := New()
	for _, tc := range []struct {
		name string
		path string
		risk string
		args []string
		safe bool
		bot  bool
	}{
		{"read", "/docs/+fetch", "read", []string{"docs", "+fetch"}, true, false},
		{"missing subcommand", "/im/messages/get", "missing", []string{"im", "messages", "get"}, true, false},
		{"ordinary write", "/docs/+create", "write", []string{"docs", "+create"}, true, false},
		{"unknown risk write", "/docs/+update", "", []string{"docs", "+update"}, false, false},
		{"user send", "/im/+messages-send", "write", []string{"im", "+messages-send", "--as", "user", "--text", "hi"}, false, false},
		{"bot send", "/im/+messages-send", "write", []string{"im", "+messages-send", "--as", "bot", "--text", "hi"}, true, true},
		{"bot reply", "/im/+messages-reply", "write", []string{"im", "+messages-reply", "--as=bot", "--text", "ok"}, true, true},
		{"last actor user wins", "/im/+messages-send", "write", []string{"im", "+messages-send", "--as", "bot", "--as", "user"}, false, false},
		{"last actor bot wins", "/im/+messages-send", "write", []string{"im", "+messages-send", "--as", "user", "--as=bot"}, true, true},
		{"actor flag is text value", "/im/+messages-send", "write", []string{"im", "+messages-send", "--text", "--as", "bot"}, false, false},
		{"unknown flag arity clears bot", "/im/+messages-reply", "write", []string{"im", "+messages-reply", "--as", "bot", "--future-flag", "--as", "user"}, false, false},
		{"bot deletion never exempted", "/drive/files/delete", "high-risk-write", []string{"drive", "files", "delete", "--as", "bot"}, false, true},
		{"bot mail never exempted", "/mail/messages/send", "write", []string{"mail", "messages", "send", "--as", "bot"}, false, true},
		{"bot comment never exempted", "/docs/comments/create", "write", []string{"docs", "comments", "create", "--as", "bot"}, false, true},
		{"bot native create never exempted", "/im/messages/create", "write", []string{"im", "messages", "create", "--as", "bot"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := g.CLIBypassSafe(tc.path, tc.risk, tc.args); got != tc.safe {
				t.Errorf("CLIBypassSafe = %v, want %v", got, tc.safe)
			}
			if got := g.CLIBotActor(tc.args); got != tc.bot {
				t.Errorf("CLIBotActor = %v, want %v", got, tc.bot)
			}
		})
	}
}

func TestCLISummaryAndTitle(t *testing.T) {
	g := New()
	if got := g.DefaultTitle(); got != "飞书" {
		t.Fatalf("DefaultTitle() = %q, want 飞书", got)
	}
	send := g.CLISummary("CLI", "/im/+messages-send", "write", []string{"im", "+messages-send", "--chat-id", "oc_x", "--text", "下午三点开会"}, "发消息")
	if !strings.Contains(send, "以你的身份在飞书上发消息\nlark-cli im +messages-send --chat-id oc_x --text 下午三点开会") {
		t.Errorf("send summary = %q", send)
	}
	del := g.CLISummary("CLI", "/im/messages/delete", "high-risk-write", []string{"im", "messages", "delete", "--params", `{"message_id":"om_x"}`}, "撤回消息")
	if !strings.Contains(del, "撤回消息（lark-cli 标为高风险）") {
		t.Errorf("delete summary = %q", del)
	}
	unknown := g.CLISummary("CLI", "/wiki/+move", "", []string{"wiki", "+move", "--node", "x"}, "写文档")
	if !strings.Contains(unknown, "写文档（lark-cli 没给出风险等级）\nlark-cli wiki +move --node x") {
		t.Errorf("unknown summary = %q", unknown)
	}
	bot := g.CLISummary("CLI", "/im/+messages-reply", "write", []string{"im", "+messages-reply", "--as", "bot", "--message-id", "om_y", "--text", "收到"}, "回复消息")
	if !strings.Contains(bot, "以机器人身份在飞书上回复消息\nlark-cli im +messages-reply --as bot --message-id om_y --text 收到") {
		t.Errorf("bot summary = %q", bot)
	}
}
