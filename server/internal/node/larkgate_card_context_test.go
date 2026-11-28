package node

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func larkTitleGate(base string, user ...proto.LarkRule) []proto.CommandGate {
	g := exampleGate(base, user...)
	g.Command = "ol"
	g.Title = "飞书"
	g.PathPrefix = "/agent/v1"
	return []proto.CommandGate{g}
}

func TestLarkSummaryShowsMessageContextNotInternalFields(t *testing.T) {
	calls := []larkCall{
		{Method: "POST", Path: "/conversations/oc_1/messages", Body: `{"text":"I will check the build","clientId":"internal-client","expectedSenderId":"internal-sender"}`},
		{Method: "POST", Path: "/messages/om_1/replies", Body: `{"text":"I will check it too","replyMode":"thread"}`},
	}
	contexts := []larkCardContext{
		{OK: true, Chat: "Release Room"},
		{OK: true, Chat: "Release Room", ReplySender: "Example User", ReplyText: "Can we ship on Friday?", InTopic: true},
	}
	s := larkSummaryWithContext(larkTitleGate(""), calls, contexts, i18n.En)
	for _, want := range []string{"Send a message to “Release Room” on Lark as you", "I will check the build", "Reply to Example User in “Release Room” on Lark as you", "Can we ship on Friday?", "(in a topic)", "I will check it too"} {
		if !strings.Contains(s, want) {
			t.Errorf("summary lacks %q:\n%s", want, s)
		}
	}
	for _, unwanted := range []string{"clientId", "expectedSenderId", "replyMode", "/conversations/oc_1/messages", "/messages/om_1/replies", "internal-client", "internal-sender"} {
		if strings.Contains(s, unwanted) {
			t.Errorf("summary exposes %q:\n%s", unwanted, s)
		}
	}
}

func TestLarkSummaryUsesNonLarkGateTitle(t *testing.T) {
	slackGate := []proto.CommandGate{{Command: "sl", Title: "Slack"}}
	calls := []larkCall{
		{Gate: 0, Method: "POST", Path: "/conversations/c1/messages", Body: `{"text":"deploying now"}`},
		{Gate: 0, Method: "POST", Path: "/messages/m1/replies", Body: `{"text":"done","replyMode":"direct","editorType":"simple"}`},
	}
	en := larkSummaryWithContext(slackGate, calls, []larkCardContext{{OK: true, Chat: "releases"}, {}}, i18n.En)
	for _, want := range []string{
		"Send a message to “releases” on Slack as you",
		"Reply to message m1 on Slack as you",
		"deploying now",
		"done",
	} {
		if !strings.Contains(en, want) {
			t.Errorf("en summary lacks %q:\n%s", want, en)
		}
	}
	for _, unwanted := range []string{"Lark", "飞书", "(in a topic)"} {
		if strings.Contains(en, unwanted) {
			t.Errorf("non-Lark gate mislabeled with %q:\n%s", unwanted, en)
		}
	}

	zh := larkSummaryWithContext(olGate(""), []larkCall{{Gate: 0, Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"测试"}`}}, []larkCardContext{{}}, i18n.ZhHans)
	if !strings.Contains(zh, "以你的身份在示例上给「7123456789」发消息") || strings.Contains(zh, "飞书") {
		t.Errorf("example gate mislabeled as Feishu:\n%s", zh)
	}
}

func TestLarkSummaryGatewayFallbackWithoutMetadataIsClean(t *testing.T) {
	gates := larkTitleGate("")
	t.Run("send with mentions and internal fields (zh-Hans)", func(t *testing.T) {
		calls := []larkCall{{
			Method: "POST",
			Path:   "/conversations/7123456789/messages",
			Body:   `{"text":"第一行\n第二行 @张三 请看下","mentions":[{"userId":"100002","name":"张三"},{"user_id":"all","name":"所有人"}],"clientId":"cid-1","expectedSenderId":"100001"}`,
		}}
		s := larkSummaryWithContext(gates, calls, []larkCardContext{{}}, i18n.ZhHans)
		for _, want := range []string{
			"以你的身份在飞书上给「7123456789」发消息",
			"@提及：@张三, @所有人",
			"第一行\n第二行 @张三 请看下",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("summary lacks %q:\n%s", want, s)
			}
		}
		for _, unwanted := range []string{
			"POST /conversations/7123456789/messages",
			"clientId", "cid-1", "expectedSenderId", "100001", "userId", "user_id",
		} {
			if strings.Contains(s, unwanted) {
				t.Errorf("summary exposes %q:\n%s", unwanted, s)
			}
		}
	})

	t.Run("reply without metadata shows clean reply summary (en and zh-Hans)", func(t *testing.T) {
		calls := []larkCall{{
			Method: "POST",
			Path:   "/messages/7999888777/replies",
			Body:   `{"text":"Got it, checking now","replyMode":"thread","mentions":[{"userId":"12","name":"Alice"}],"clientId":"internal"}`,
		}}
		en := larkSummaryWithContext(gates, calls, []larkCardContext{{}}, i18n.En)
		for _, want := range []string{
			"Reply to message 7999888777 on Lark as you (in a topic)",
			"Mentions: @Alice",
			"Got it, checking now",
		} {
			if !strings.Contains(en, want) {
				t.Errorf("en summary lacks %q:\n%s", want, en)
			}
		}
		for _, unwanted := range []string{"POST /messages/7999888777/replies", "clientId", "internal", "replyMode", "userId"} {
			if strings.Contains(en, unwanted) {
				t.Errorf("en summary exposes %q:\n%s", unwanted, en)
			}
		}

		zh := larkSummaryWithContext(gates, calls, []larkCardContext{{}}, i18n.ZhHans)
		for _, want := range []string{
			"以你的身份在飞书上回复消息 7999888777（话题回复）",
			"@提及：@Alice",
			"Got it, checking now",
		} {
			if !strings.Contains(zh, want) {
				t.Errorf("zh summary lacks %q:\n%s", want, zh)
			}
		}
	})

	t.Run("reply with direct mode and simple editor stays clean", func(t *testing.T) {
		calls := []larkCall{{
			Method: "POST",
			Path:   "/conversations/7123456789/messages/7999/replies",
			Body:   `{"text":"following up","replyMode":"direct","editorType":"simple"}`,
		}}
		s := larkSummaryWithContext(gates, calls, []larkCardContext{{}}, i18n.En)
		for _, want := range []string{
			"Reply in “7123456789” on Lark as you",
			"following up",
		} {
			if !strings.Contains(s, want) {
				t.Errorf("summary lacks %q:\n%s", want, s)
			}
		}
		for _, unwanted := range []string{"POST /conversations/", "replyMode", "editorType", "(in a topic)"} {
			if strings.Contains(s, unwanted) {
				t.Errorf("summary exposes %q:\n%s", unwanted, s)
			}
		}
	})
}

func TestLarkSummaryTransparentFallbackForAmbiguousOrExtraFieldCalls(t *testing.T) {
	resolved := []larkCardContext{{OK: true, Chat: "Release Room"}}
	cases := []struct {
		name  string
		call  larkCall
		wants []string
	}{
		{
			name:  "dynamic dollar in body",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"$SECRET"}`},
			wants: []string{"POST /conversations/7123456789/messages", "$SECRET"},
		},
		{
			name:  "replyToMessageId disallowed on send route",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"hi","replyToMessageId":"7999"}`},
			wants: []string{"POST /conversations/7123456789/messages", `"replyToMessageId": "7999"`},
		},
		{
			name:  "replyMode disallowed on send route",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"hi","replyMode":"thread"}`},
			wants: []string{"POST /conversations/7123456789/messages", `"replyMode": "thread"`},
		},
		{
			name:  "thread flag disallowed on send route",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"hi","thread":true}`},
			wants: []string{"POST /conversations/7123456789/messages", `"thread": true`},
		},
		{
			name:  "reply_in_thread disallowed on send route",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"hi","reply_in_thread":true}`},
			wants: []string{"POST /conversations/7123456789/messages", `"reply_in_thread": true`},
		},
		{
			name:  "conflicting synonym replyMode and thread on reply route",
			call:  larkCall{Method: "POST", Path: "/messages/7100/replies", Body: `{"text":"hi","replyMode":"direct","thread":true}`},
			wants: []string{"POST /messages/7100/replies", `"replyMode": "direct"`, `"thread": true`},
		},
		{
			name:  "conflicting synonym replyMode and reply_in_thread on reply route",
			call:  larkCall{Method: "POST", Path: "/messages/7100/replies", Body: `{"text":"hi","replyMode":"direct","reply_in_thread":true}`},
			wants: []string{"POST /messages/7100/replies", `"reply_in_thread": true`},
		},
		{
			name:  "conflicting synonym mention IDs",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"@Alice","mentions":[{"userId":"1","id":"2","name":"Alice"}]}`},
			wants: []string{"POST /conversations/7123456789/messages", `"userId": "1"`, `"id": "2"`},
		},
		{
			name:  "conflicting synonym mention names",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"@Alice","mentions":[{"userId":"1","name":"Alice","displayName":"Bob"}]}`},
			wants: []string{"POST /conversations/7123456789/messages", `"displayName": "Bob"`},
		},
		{
			name:  "lone at-sign mention name",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"@","mentions":[{"name":"@"}]}`},
			wants: []string{"POST /conversations/7123456789/messages", `"name": "@"`},
		},
		{
			name:  "usergroup mention bypass falls back to raw",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"@Alice","mentions":[{"userId":"usergroup:12345","name":"Alice"}]}`},
			wants: []string{"POST /conversations/7123456789/messages", `"userId": "usergroup:12345"`},
		},
		{
			name:  "uppercase usergroup mention bypass falls back to raw",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"@Alice","mentions":[{"open_id":"USERGROUP:999","name":"Alice"}]}`},
			wants: []string{"POST /conversations/7123456789/messages", `"open_id": "USERGROUP:999"`},
		},
		{
			name:  "non-user mention type falls back to raw",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"@Alice","mentions":[{"userId":"100002","name":"Alice","type":"all"}]}`},
			wants: []string{"POST /conversations/7123456789/messages", `"type": "all"`},
		},
		{
			name:  "unknown top-level extra field",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"hello","attachments":["/etc/hosts"]}`},
			wants: []string{"POST /conversations/7123456789/messages", `"attachments"`, `/etc/hosts`},
		},
		{
			name:  "unknown urgent flag",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"hello","urgent":true}`},
			wants: []string{"POST /conversations/7123456789/messages", `"urgent": true`},
		},
		{
			name:  "top-level account selector",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"hello","accountId":"2"}`},
			wants: []string{"POST /conversations/7123456789/messages", `"accountId": "2"`},
		},
		{
			name:  "unknown extra field inside mention item",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"@Alice","mentions":[{"name":"Alice","extra":1}]}`},
			wants: []string{"POST /conversations/7123456789/messages", `"extra": 1`},
		},
		{
			name:  "malformed mentions type",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"hi","mentions":"all"}`},
			wants: []string{"POST /conversations/7123456789/messages", `"mentions": "all"`},
		},
		{
			name:  "non-text msg_type",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"msg_type":"interactive","content":"{\"text\":\"hi\"}"}`},
			wants: []string{"POST /conversations/7123456789/messages", `"msg_type": "interactive"`},
		},
		{
			name:  "nested content with hidden extra key",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"msg_type":"text","content":"{\"text\":\"hi\",\"file_key\":\"f1\"}"}`},
			wants: []string{"POST /conversations/7123456789/messages", `file_key`},
		},
		{
			name:  "duplicate exact keys",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"benign","text":"malicious"}`},
			wants: []string{"POST /conversations/7123456789/messages"},
		},
		{
			name:  "duplicate case-folded keys",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"benign","TEXT":"malicious"}`},
			wants: []string{"POST /conversations/7123456789/messages", `"TEXT": "malicious"`},
		},
		{
			name:  "non-JSON body",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `plain text not json`},
			wants: []string{"POST /conversations/7123456789/messages", "plain text not json"},
		},
		{
			name:  "trailing JSON garbage",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"hi"}{"extra":1}`},
			wants: []string{"POST /conversations/7123456789/messages", `{"extra":1}`},
		},
		{
			name:  "non-empty query string stays visible",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Query: "ol_account=2", Body: `{"text":"hi"}`},
			wants: []string{"POST /conversations/7123456789/messages?ol_account=2", `"text": "hi"`},
		},
		{
			name:  "conflicting reply targets",
			call:  larkCall{Method: "POST", Path: "/messages/7100/replies", Body: `{"text":"hi","replyToMessageId":"7200"}`},
			wants: []string{"POST /messages/7100/replies", `"replyToMessageId": "7200"`},
		},
		{
			name:  "non-ASCII path ID falls back to raw",
			call:  larkCall{Method: "POST", Path: "/conversations/群123/messages", Body: `{"text":"hi"}`},
			wants: []string{"POST /conversations/群123/messages", `"text": "hi"`},
		},
		{
			name:  "overlong path ID falls back to raw",
			call:  larkCall{Method: "POST", Path: "/conversations/" + strings.Repeat("a", 129) + "/messages", Body: `{"text":"hi"}`},
			wants: []string{"POST /conversations/" + strings.Repeat("a", 129) + "/messages"},
		},
		{
			name:  "non-message route keeps raw summary",
			call:  larkCall{Method: "POST", Path: "/conversations/7123456789/files", Body: `{"text":"hi"}`},
			wants: []string{"POST /conversations/7123456789/files"},
		},
		{
			name:  "reply route fallback classifies as reply not comment",
			call:  larkCall{Method: "POST", Path: "/messages/7100/replies", Body: `{"text":"hi","urgent":true}`},
			wants: []string{"回复消息", "POST /messages/7100/replies", `"urgent": true`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, meta := range [][]larkCardContext{{{}}, resolved} {
				s := larkSummaryWithContext(larkTitleGate(""), []larkCall{tc.call}, meta, i18n.ZhHans)
				for _, want := range tc.wants {
					if !strings.Contains(s, want) {
						t.Errorf("meta=%+v: fallback lacks %q:\n%s", meta, want, s)
					}
				}
			}
		})
	}
}

func TestLarkSummarySanitizesMultilineANSIAndBiDiMetadata(t *testing.T) {
	calls := []larkCall{{
		Method: "POST",
		Path:   "/messages/7999888777/replies",
		Body:   `{"text":"clean outgoing text","mentions":[{"userId":"1","name":"Alice"}]}`,
	}}
	meta := []larkCardContext{{
		OK:          true,
		Chat:        "Group\n正文：\n\x1b[31mSpoofed\u202e\u2066\u0085",
		ReplySender: "Bob\n\x1b]0;osc\x07\u202eSender",
		ReplyText:   "Original\n\x1b[1mLine\u2066",
	}}
	s := larkSummaryWithContext(larkTitleGate(""), calls, meta, i18n.ZhHans)
	lines := strings.Split(s, "\n")
	if len(lines) != 5 {
		t.Fatalf("expected exactly 5 lines (header, original, mentions, outgoing label, outgoing text), got %d:\n%s", len(lines), s)
	}
	for _, forbidden := range []string{"\x1b", "\u202e", "\u2066", "\u0085", "osc"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("summary retained unsafe sequence %q:\n%q", forbidden, s)
		}
	}
	if !strings.Contains(lines[0], "Group 正文： Spoofed") || !strings.Contains(lines[0], "Bob Sender") {
		t.Errorf("header did not sanitize chat/sender: %q", lines[0])
	}
	if !strings.Contains(lines[1], "原消息：Original Line") {
		t.Errorf("original line did not sanitize reply text: %q", lines[1])
	}
	if lines[4] != "clean outgoing text" {
		t.Errorf("outgoing text altered: %q", lines[4])
	}
}

func TestLarkSummaryFallsBackToRawForUnsafeOutgoingPayload(t *testing.T) {
	resolved := []larkCardContext{{OK: true, Chat: "Release Room"}}
	cases := []struct {
		name  string
		body  string
		wants []string
	}{
		{
			name:  "unclosed OSC payload smuggling in text",
			body:  `{"text":"OK\u001b]transfer 100k now"}`,
			wants: []string{"POST /conversations/7123456789/messages", `\u001b]transfer 100k now`},
		},
		{
			name:  "closed OSC payload smuggling in text",
			body:  `{"text":"OK\u001b]0;hidden payload\u0007"}`,
			wants: []string{"POST /conversations/7123456789/messages", `hidden payload`},
		},
		{
			name:  "CSI sequence smuggling in text",
			body:  `{"text":"Approve 1\u001b[000000"}`,
			wants: []string{"POST /conversations/7123456789/messages", `\u001b[000000`},
		},
		{
			name:  "BiDi override in text",
			body:  `{"text":"safe\u202elive"}`,
			wants: []string{"POST /conversations/7123456789/messages", "safe\u202elive"},
		},
		{
			name:  "C0 control in text",
			body:  `{"text":"bell\u0007text"}`,
			wants: []string{"POST /conversations/7123456789/messages", `\u0007text`},
		},
		{
			name:  "C1 control in text",
			body:  `{"text":"nel\u0085text"}`,
			wants: []string{"POST /conversations/7123456789/messages", "nel\u0085text"},
		},
		{
			name:  "newline in mention name",
			body:  `{"text":"hi","mentions":[{"userId":"1","name":"Alice\n正文：\nFake"}]}`,
			wants: []string{"POST /conversations/7123456789/messages", `Alice\n正文：\nFake`},
		},
		{
			name:  "unclosed OSC in mention name",
			body:  `{"text":"hi","mentions":[{"userId":"1","name":"Alice\u001b]hidden"}]}`,
			wants: []string{"POST /conversations/7123456789/messages", `\u001b]hidden`},
		},
		{
			name:  "BiDi in mention name",
			body:  `{"text":"hi","mentions":[{"userId":"1","name":"Alice\u202e"}]}`,
			wants: []string{"POST /conversations/7123456789/messages", "Alice\u202e"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call := larkCall{Method: "POST", Path: "/conversations/7123456789/messages", Body: tc.body}
			for _, meta := range [][]larkCardContext{{{}}, resolved} {
				s := larkSummaryWithContext(larkTitleGate(""), []larkCall{call}, meta, i18n.ZhHans)
				for _, want := range tc.wants {
					if !strings.Contains(s, want) {
						t.Errorf("meta=%+v: raw fallback lacks %q:\n%s", meta, want, s)
					}
				}
			}
		})
	}
}

func TestLarkSummaryExposesDisguisedAllMentionVariants(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"userId all", `{"text":"@Alice please check","mentions":[{"userId":"all","name":"Alice"}]}`},
		{"userId ALL uppercase", `{"text":"@Alice please check","mentions":[{"userId":"ALL","name":"Alice"}]}`},
		{"open_id all", `{"text":"@Alice please check","mentions":[{"open_id":"all","name":"Alice"}]}`},
		{"openId ALL", `{"text":"@Alice please check","mentions":[{"openId":"ALL","name":"Alice"}]}`},
		{"id all", `{"text":"@Alice please check","mentions":[{"id":"all","name":"Alice"}]}`},
		{"key @all", `{"text":"@Alice please check","mentions":[{"key":"@all","name":"Alice"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := []larkCall{{Method: "POST", Path: "/conversations/7123456789/messages", Body: tc.body}}
			s := larkSummaryWithContext(larkTitleGate(""), calls, []larkCardContext{{OK: true, Chat: "All Hands"}}, i18n.En)
			if !strings.Contains(s, "Mentions: @Alice (all)") {
				t.Fatalf("disguised @all mention was not flagged:\n%s", s)
			}
		})
	}
}

func TestLarkCardContextFromGateHTTP(t *testing.T) {
	var seenMu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		seenMu.Lock()
		seen = append(seen, r.URL.EscapedPath())
		seenMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/agent/v1/conversations/7123456789":
			_, _ = w.Write([]byte(`{"item":{"id":"7123456789","name":"研发发布群"}}`))
		case "/agent/v1/conversations/chat[1]":
			_, _ = w.Write([]byte(`{"item":{"id":"chat[1]","name":"Bracket Chat"}}`))
		case "/agent/v1/messages/7999888777":
			// Real gateway GET /messages/:id schema: flat senderName + textPreview + threadMessageType.
			_, _ = w.Write([]byte(`{"item":{"id":"7999888777","feedId":"7123456789","threadMessageType":"reply","senderName":"李四","textPreview":"发版前再确认一下单测"}}`))
		case "/agent/v1/messages/7999888778":
			// Real gateway GET /messages/:id schema with parts only.
			_, _ = w.Write([]byte(`{"item":{"id":"7999888778","feedId":"7123456789","sender":{"name":"王五"},"parts":[{"kind":"mention","text":"@李四"},{"kind":"text","text":"帮忙看下 PR"}]}}`))
		case "/agent/v1/messages/7999888779":
			// Recalled message: preview must be skipped, chat name still resolved.
			_, _ = w.Write([]byte(`{"item":{"id":"7999888779","feedId":"7123456789","isRecalled":true,"sender":{"name":"赵六"},"textPreview":"已撤回的敏感内容"}}`))
		case "/agent/v1/messages/7888000111":
			// Partial gateway message payload carrying only feedId (e.g. image/card message).
			_, _ = w.Write([]byte(`{"item":{"id":"7888000111","feedId":"7123456789","isMine":false,"localState":"synced"}}`))
		case "/agent/v1/messages/7777000111":
			// Cross-chat message: belongs to a different feedId than route chatID.
			_, _ = w.Write([]byte(`{"item":{"id":"7777000111","feedId":"9999999999","chatName":"Spoofed Group","sender":{"name":"Victim"},"textPreview":"spoofed original"}}`))
		case "/agent/v1/messages/7777000222":
			// Message omits feedId but supplies an unverified chatName.
			_, _ = w.Write([]byte(`{"item":{"id":"7777000222","chatName":"Unverified Name","sender":{"name":"李四"},"textPreview":"hello"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	gate := proxiedGate(srv.URL + "/agent/v1")
	gate.Command = "ol"
	gate.Title = "飞书"
	gate.PathPrefix = "agent/v1" // intentionally missing leading slash to verify normalization
	cfg := proto.AssistantLark{Gates: []proto.CommandGate{gate}}
	n := &Node{}

	calls := []larkCall{
		{Gate: 0, Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"单测全绿","mentions":[{"userId":"2","name":"李四"}],"clientId":"c1"}`},
		{Gate: 0, Method: "POST", Path: "/messages/7999888777/replies", Body: `{"text":"已经全绿了"}`},
		{Gate: 0, Method: "POST", Path: "/messages/7999888778/replies", Body: `{"text":"看完了"}`},
		{Gate: 0, Method: "POST", Path: "/messages/7999888779/replies", Body: `{"text":"收到"}`},
		{Gate: 0, Method: "POST", Path: "/messages/7888000111/replies", Body: `{"text":"收到"}`},
		{Gate: 0, Method: "POST", Path: "/conversations/chat[1]/messages", Body: `{"text":"no double encode"}`},
		{Gate: 0, Method: "POST", Path: "/conversations/7123456789/messages/7777000111/replies", Body: `{"text":"cross chat"}`},
		{Gate: 0, Method: "POST", Path: "/conversations/7123456789/messages/7777000222/replies", Body: `{"text":"no unverified chatName"}`},
	}
	got := n.larkCardContexts(context.Background(), cfg, calls)
	seenMu.Lock()
	gotPaths := append([]string(nil), seen...)
	seenMu.Unlock()
	wantPaths := []string{
		"/agent/v1/conversations/7123456789",
		"/agent/v1/messages/7999888777",
		"/agent/v1/conversations/7123456789",
		"/agent/v1/messages/7999888778",
		"/agent/v1/conversations/7123456789",
		"/agent/v1/messages/7999888779",
		"/agent/v1/conversations/7123456789",
		"/agent/v1/messages/7888000111",
		"/agent/v1/conversations/7123456789",
		"/agent/v1/conversations/chat%5B1%5D",
		"/agent/v1/messages/7777000111",
		"/agent/v1/messages/7777000222",
		"/agent/v1/conversations/7123456789",
	}
	if !slices.Equal(gotPaths, wantPaths) {
		t.Fatalf("gate lookup escaped paths = %v, want %v", gotPaths, wantPaths)
	}
	if !got[0].OK || got[0].Chat != "研发发布群" {
		t.Fatalf("send context = %+v, want Chat=研发发布群", got[0])
	}
	if !got[1].OK || got[1].Chat != "研发发布群" || got[1].ReplySender != "李四" || got[1].ReplyText != "发版前再确认一下单测" || !got[1].InTopic {
		t.Fatalf("textPreview reply context = %+v", got[1])
	}
	if !got[2].OK || got[2].Chat != "研发发布群" || got[2].ReplySender != "王五" || got[2].ReplyText != "@李四 帮忙看下 PR" {
		t.Fatalf("parts reply context = %+v", got[2])
	}
	if !got[3].OK || got[3].Chat != "研发发布群" || got[3].ReplyText != "" {
		t.Fatalf("recalled reply context leaked preview: %+v", got[3])
	}
	if !got[4].OK || got[4].Chat != "研发发布群" {
		t.Fatalf("partial reply context = %+v, want Chat=研发发布群", got[4])
	}
	if !got[5].OK || got[5].Chat != "Bracket Chat" {
		t.Fatalf("bracket ID context = %+v, want Chat=Bracket Chat", got[5])
	}
	if got[6].OK || got[6].Chat != "" || got[6].ReplySender != "" || got[6].ReplyText != "" {
		t.Fatalf("cross-chat reply unexpectedly adopted mismatched message: %+v", got[6])
	}
	if !got[7].OK || got[7].Chat != "研发发布群" {
		t.Fatalf("unverified chatName overwrote route chatID: %+v", got[7])
	}

	s := larkSummaryWithContext(cfg.Gates, calls[:2], got[:2], i18n.ZhHans)
	for _, want := range []string{
		"以你的身份在飞书上给「研发发布群」发消息",
		"@提及：@李四",
		"单测全绿",
		"以你的身份在飞书上回复「研发发布群」里 李四 的消息（话题回复）",
		"原消息：发版前再确认一下单测",
		"已经全绿了",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("summary lacks %q:\n%s", want, s)
		}
	}
	for _, unwanted := range []string{"POST /conversations/", "POST /messages/", "clientId"} {
		if strings.Contains(s, unwanted) {
			t.Errorf("summary exposes %q:\n%s", unwanted, s)
		}
	}
}

func TestLarkCardContextFromGateRelay(t *testing.T) {
	var relayMu sync.Mutex
	var relayed []proto.GateRelayParams
	n, hub := runtimeNode(t)
	hub.mu.Lock()
	hub.answer = func(method string, params json.RawMessage) (any, error, bool) {
		if method != proto.MethodGateRelay {
			return nil, nil, false
		}
		var req proto.GateRelayParams
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err, true
		}
		relayMu.Lock()
		relayed = append(relayed, req)
		relayMu.Unlock()
		if req.Method == http.MethodGet && req.Path == "/agent/v1/conversations/7123456789" {
			return proto.GateRelayAnswer{
				Status: http.StatusOK,
				Body:   []byte(`{"item":{"id":"7123456789","name":"Relayed Group"}}`),
			}, nil, true
		}
		return proto.GateRelayAnswer{Status: http.StatusNotFound}, nil, true
	}
	hub.mu.Unlock()

	gate := proto.CommandGate{
		Command:    "ol",
		Title:      "飞书",
		PathPrefix: "/agent/v1",
		Relay:      true,
		Rules:      exampleRules,
	}
	cfg := proto.AssistantLark{Gates: []proto.CommandGate{gate}}
	calls := []larkCall{{Gate: 0, Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"hello"}`}}
	got := n.larkCardContexts(context.Background(), cfg, calls)[0]
	relayMu.Lock()
	gotRelayed := append([]proto.GateRelayParams(nil), relayed...)
	relayMu.Unlock()
	if !got.OK || got.Chat != "Relayed Group" {
		t.Fatalf("relayed context = %+v, relayed=%+v", got, gotRelayed)
	}
	if len(gotRelayed) != 1 || gotRelayed[0].Command != "ol" || gotRelayed[0].Method != http.MethodGet || gotRelayed[0].Path != "/agent/v1/conversations/7123456789" {
		t.Fatalf("unexpected relay params: %+v", gotRelayed)
	}
}

func TestLarkCardContextGateSafetyBoundaries(t *testing.T) {
	t.Run("does not follow redirects or leak credentials", func(t *testing.T) {
		var leakedAuth atomic.Bool
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				leakedAuth.Store(true)
			}
			_, _ = w.Write([]byte(`{"item":{"name":"Redirected"}}`))
		}))
		defer target.Close()

		front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/steal", http.StatusFound)
		}))
		defer front.Close()

		cfg := proto.AssistantLark{Gates: []proto.CommandGate{proxiedGate(front.URL)}}
		got := (&Node{}).larkCardContexts(context.Background(), cfg, []larkCall{{
			Gate: 0, Method: "POST", Path: "/conversations/7123/messages", Body: `{"text":"hi"}`,
		}})[0]
		if got.OK {
			t.Fatalf("redirect unexpectedly succeeded: %+v", got)
		}
		if leakedAuth.Load() {
			t.Fatal("redirect leaked Authorization header to another host")
		}
	})

	t.Run("respects explicit deny rule on metadata GET", func(t *testing.T) {
		var hit atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hit.Add(1)
			_, _ = w.Write([]byte(`{"item":{"name":"Secret"}}`))
		}))
		defer srv.Close()

		cfg := proto.AssistantLark{Gates: []proto.CommandGate{proxiedGate(srv.URL, proto.LarkRule{
			Method: "GET", Path: "/conversations/*", Action: "deny",
		})}}
		got := (&Node{}).larkCardContexts(context.Background(), cfg, []larkCall{{
			Gate: 0, Method: "POST", Path: "/conversations/7123/messages", Body: `{"text":"hi"}`,
		}})[0]
		if got.OK || hit.Load() != 0 {
			t.Fatalf("denied GET was still executed: got=%+v hits=%d", got, hit.Load())
		}
	})

	t.Run("respects confirm rule and unallowed GET rules", func(t *testing.T) {
		var hit atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hit.Add(1)
			_, _ = w.Write([]byte(`{"item":{"name":"NeedsConfirm"}}`))
		}))
		defer srv.Close()

		confirmGate := proxiedGate(srv.URL, proto.LarkRule{Method: "GET", Path: "/conversations/*", Action: "confirm"})
		noRulesGate := proxiedGate(srv.URL)
		noRulesGate.Rules = nil
		for _, g := range []proto.CommandGate{confirmGate, noRulesGate} {
			got := (&Node{}).larkCardContexts(context.Background(), proto.AssistantLark{Gates: []proto.CommandGate{g}}, []larkCall{{
				Gate: 0, Method: "POST", Path: "/conversations/7123/messages", Body: `{"text":"hi"}`,
			}})[0]
			if got.OK || hit.Load() != 0 {
				t.Fatalf("unallowed/confirm GET was executed: got=%+v hits=%d", got, hit.Load())
			}
		}
	})

	t.Run("fails open quickly on hanging gate server", func(t *testing.T) {
		origTimeout := larkCardContextTimeout
		larkCardContextTimeout = 80 * time.Millisecond
		t.Cleanup(func() { larkCardContextTimeout = origTimeout })

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer srv.Close()

		gate := proxiedGate(srv.URL)
		gate.Title = "飞书"
		cfg := proto.AssistantLark{Gates: []proto.CommandGate{gate}}
		calls := []larkCall{{Gate: 0, Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"still readable"}`}}
		got := (&Node{}).larkCardContexts(context.Background(), cfg, calls)
		if got[0].OK {
			t.Fatalf("timed out server returned OK: %+v", got[0])
		}
		s := larkSummaryWithContext(cfg.Gates, calls, got, i18n.En)
		if !strings.Contains(s, "Send a message to “7123456789” on Lark as you") || !strings.Contains(s, "still readable") || strings.Contains(s, "POST /conversations/") {
			t.Fatalf("timeout fallback was not clean: %q", s)
		}
	})

	t.Run("numeric gateway ID does not spawn lark-cli", func(t *testing.T) {
		dir := t.TempDir()
		marker := filepath.Join(dir, "invoked")
		bin := filepath.Join(dir, "lark-cli")
		script := "#!/bin/sh\ntouch " + shellQuote(marker) + "\nexit 1\n"
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := proto.AssistantLark{CLI: &proto.LarkCLI{Bin: bin}}
		got := (&Node{}).larkCardContexts(context.Background(), cfg, []larkCall{{
			Gate: -1, Method: "POST", Path: "/conversations/7123456789/messages", Body: `{"text":"hi"}`,
		}})[0]
		if got.OK {
			t.Fatalf("unexpected context: %+v", got)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("lark-cli was invoked for numeric gateway ID")
		}
	})

	t.Run("rejects unsafe chat_id returned by lark-cli before second lookup", func(t *testing.T) {
		origTimeout := larkCardContextTimeout
		larkCardContextTimeout = 10 * time.Second
		t.Cleanup(func() { larkCardContextTimeout = origTimeout })

		dir := t.TempDir()
		secondHit := filepath.Join(dir, "second_hit")
		bin := filepath.Join(dir, "lark-cli")
		script := "#!/bin/sh\ncase \"$*\" in\n" +
			"  *\"/messages/om_1\"*) printf '%s\\n' '{\"message\":{\"chat_id\":\"../secret/chat\",\"sender\":{\"name\":\"Alice\"},\"body\":{\"content\":\"{\\\"text\\\":\\\"hi\\\"}\"}}}' ;;\n" +
			"  *\"/chats/\"*) touch " + shellQuote(secondHit) + "; exit 1 ;;\n" +
			"  *) exit 1 ;;\n" +
			"esac\n"
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := proto.AssistantLark{CLI: &proto.LarkCLI{Bin: bin}}
		got := (&Node{}).larkCardContexts(context.Background(), cfg, []larkCall{{
			Gate: -1, Method: "POST", Path: "/messages/om_1/replies", Body: `{"text":"ok"}`,
		}})[0]
		if got.OK {
			t.Fatalf("unsafe CLI chat_id unexpectedly succeeded: %+v", got)
		}
		if _, err := os.Stat(secondHit); err == nil {
			t.Fatal("lark-cli performed chat lookup with unsafe traversal chat_id")
		}
	})
}

func TestLarkCardContextFromReadOnlyCLI(t *testing.T) {
	// Spawning two fresh shell scripts in t.TempDir() under -race can exceed the default 1.5s production budget.
	origTimeout := larkCardContextTimeout
	larkCardContextTimeout = 10 * time.Second
	t.Cleanup(func() { larkCardContextTimeout = origTimeout })

	dir := t.TempDir()
	bin := filepath.Join(dir, "lark-cli")
	script := `#!/bin/sh
case "$*" in
  *"/chats/oc_1"*) printf '%s\n' '{"chat":{"name":"Release Room"}}' ;;
  *"/messages/om_1"*) printf '%s\n' '{"message":{"chat_id":"oc_1","thread_id":"omt_1","sender":{"name":"Example User"},"body":{"content":"{\"text\":\"Can we ship on Friday?\"}"}}}' ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	n := &Node{}
	cfg := proto.AssistantLark{CLI: &proto.LarkCLI{Bin: bin}}
	calls := []larkCall{{Method: "POST", Path: "/messages/om_1/replies", Body: `{"text":"yes"}`}}
	got := n.larkCardContexts(context.Background(), cfg, calls)[0]
	if !got.OK || got.Chat != "Release Room" || got.ReplySender != "Example User" || got.ReplyText != "Can we ship on Friday?" || !got.InTopic {
		t.Fatalf("unexpected context: %+v", got)
	}
	summary, ok := larkMessageSummary(nil, larkCall{Method: "POST", Path: "/messages/om_1/replies", Body: `{"text":"yes"}`}, got, i18n.En)
	if !ok || !strings.Contains(summary, "Release Room") || !strings.Contains(summary, "Can we ship on Friday?") {
		t.Fatalf("summary: %q", summary)
	}
}

func TestLarkCardContextFailureFallsBack(t *testing.T) {
	n := &Node{}
	cfg := proto.AssistantLark{CLI: &proto.LarkCLI{Bin: filepath.Join(t.TempDir(), "missing-lark-cli")}}
	calls := []larkCall{{Method: "POST", Path: "/conversations/oc_1/messages", Body: `{"text":"hello"}`}}
	got := n.larkCardContexts(context.Background(), cfg, calls)[0]
	if got.OK {
		t.Fatalf("missing CLI unexpectedly produced metadata: %+v", got)
	}
}

func TestLarkCheckProducesCleanGatewayCardSummary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/agent/v1/conversations/7123456789":
			_, _ = w.Write([]byte(`{"item":{"id":"7123456789","name":"架构讨论群"}}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	gate := proxiedGate(srv.URL + "/agent/v1")
	gate.Command = "ol"
	gate.Title = "飞书"
	gate.PathPrefix = "/agent/v1"

	n := codexTestNode(t)
	fh := withFakeHub(t, n)
	fh.mu.Lock()
	fh.answer = func(proto.Interaction) proto.Resolution { return proto.Resolution{Allow: boolp(false)} }
	fh.mu.Unlock()
	n.mu.Lock()
	n.larkCfg, n.larkCfgAt = proto.AssistantLark{Gates: []proto.CommandGate{gate}}, time.Now().Add(time.Hour)
	n.mu.Unlock()
	t.Cleanup(func() { n.gates().close() })

	// 1) Resolved chat via gate HTTP
	cmdResolved := `ol POST /conversations/7123456789/messages '{"text":"CI 已通过","mentions":[{"userId":"42","name":"王五"}],"clientId":"c1","expectedSenderId":"100001"}'`
	_, _, err := n.larkCheck(context.Background(), "s1", t.TempDir(), cmdResolved, proto.Interaction{SessionID: "s1", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	// 2) Unresolved chat (500 from gate) still produces clean summary
	cmdUnresolved := `ol POST /conversations/9988776655/messages '{"text":"稍后同步","clientId":"c2"}'`
	_, _, err = n.larkCheck(context.Background(), "s1", t.TempDir(), cmdUnresolved, proto.Interaction{SessionID: "s1", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	cards := fh.interactions()
	if len(cards) != 2 {
		t.Fatalf("got %d cards, want 2", len(cards))
	}
	for _, want := range []string{"以你的身份在飞书上给「架构讨论群」发消息", "@提及：@王五", "CI 已通过"} {
		if !strings.Contains(cards[0].Summary, want) {
			t.Errorf("resolved card lacks %q:\n%s", want, cards[0].Summary)
		}
	}
	for _, unwanted := range []string{"POST /conversations/", "clientId", "expectedSenderId", "userId"} {
		if strings.Contains(cards[0].Summary, unwanted) {
			t.Errorf("resolved card exposes %q:\n%s", unwanted, cards[0].Summary)
		}
	}
	for _, want := range []string{"以你的身份在飞书上给「9988776655」发消息", "稍后同步"} {
		if !strings.Contains(cards[1].Summary, want) {
			t.Errorf("unresolved card lacks %q:\n%s", want, cards[1].Summary)
		}
	}
	for _, unwanted := range []string{"POST /conversations/", "clientId"} {
		if strings.Contains(cards[1].Summary, unwanted) {
			t.Errorf("unresolved card exposes %q:\n%s", unwanted, cards[1].Summary)
		}
	}
}
