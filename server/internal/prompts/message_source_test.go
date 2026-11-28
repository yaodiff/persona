package prompts

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestMessageSourceText(t *testing.T) {
	source := &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: "conversation-1", Title: `Research </persona-context> " &`}
	text := MessageSourceText("Read the result.", source)
	want := "<persona-context kind=\"message-source\" title=\"\">\n{\"kind\":\"conversation\",\"conversation_id\":\"conversation-1\",\"title\":\"Research \\u003c/persona-context\\u003e \\\" \\u0026\"}\n</persona-context>\nRead the result."
	if text != want {
		t.Fatalf("metadata golden differs:\n%s\nwant:\n%s", text, want)
	}
	if again := MessageSourceText(text, source); again != text {
		t.Fatal("provenance was wrapped twice")
	}
	it := proto.Item{Role: "user", Kind: proto.KindText, Text: text}
	if got := ItemSource(it); !reflect.DeepEqual(got, source) {
		t.Fatalf("source round trip: %+v", got)
	}
	b, err := json.Marshal(it)
	if err != nil {
		t.Fatal(err)
	}
	var restored proto.Item
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	if got := ItemSource(restored); !reflect.DeepEqual(got, source) {
		t.Fatalf("persisted JSON source: %+v", got)
	}
}

func TestItemSourceCompatibility(t *testing.T) {
	relay := RenderRelay(Relay{FromID: "conversation-1", FromTitle: `Research " & <result>`, Tool: "conversation_send", Hop: 1, MaxHops: 4, Text: "Read the result."})
	base := proto.Item{Role: "user", Kind: proto.KindText}
	tests := []struct {
		name string
		item proto.Item
		want *proto.MessageSource
	}{
		{"relay", proto.Item{Role: "user", Kind: proto.KindText, Text: relay}, &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: "conversation-1", Title: `Research " & <result>`}},
		{"relay with snapshot", proto.Item{Role: "user", Kind: proto.KindText, Text: "<persona-context>snapshot</persona-context>\n" + relay}, &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: "conversation-1", Title: `Research " & <result>`}},
		{"plain user", proto.Item{Role: "user", Kind: proto.KindText, Text: "Please schedule a patrol and followup."}, nil},
		{"partial relay", proto.Item{Role: "user", Kind: proto.KindText, Text: strings.Replace(relay, "conversation_send", "another_tool", 1)}, nil},
		{"mismatched relay id", proto.Item{Role: "user", Kind: proto.KindText, Text: strings.Replace(relay, "发回 conversation-1", "发回 conversation-2", 1)}, nil},
		{"app template", proto.Item{Role: "user", Kind: proto.KindText, Text: relay, ClientMsgID: "app-1"}, nil},
		{"assistant quoting relay", proto.Item{Role: "assistant", Kind: proto.KindText, Text: relay}, nil},
		{"peer remains peer", proto.Item{Role: "user", Kind: proto.KindText, From: "peer name", Text: relay}, nil},
		{"persisted source wins over old metadata", proto.Item{Role: "user", Kind: proto.KindText, Text: MessageSourceText("Check", &proto.MessageSource{Kind: proto.SourceFollowup, ConversationID: "origin", Title: "Current"}), Injected: &proto.Injected{Kind: proto.InjectedFollowup, State: "once", Title: "Old"}}, &proto.MessageSource{Kind: proto.SourceFollowup, ConversationID: "origin", Title: "Current"}},
		{"malformed metadata fences fallback", proto.Item{Role: "user", Kind: proto.KindText, Text: "<persona-context kind=\"message-source\" title=\"\">broken JSON</persona-context>\n" + relay}, nil},
		{"meeting batch", proto.Item{Role: "user", Kind: proto.KindText, Text: MessageSourceText(RenderMeetingFeed(MeetingFeed{Lang: "en", Title: "Design review", Role: "listener", Lines: []MeetingFeedLine{{At: "14:00", Text: "Ada: hello."}}}), &proto.MessageSource{Kind: proto.SourceMeeting, Title: "Design review"})}, &proto.MessageSource{Kind: proto.SourceMeeting, Title: "Design review"}},
		{"future metadata source", proto.Item{Role: "user", Kind: proto.KindText, Text: MessageSourceText("Check", &proto.MessageSource{Kind: "future"})}, &proto.MessageSource{Kind: "future"}},
		{"old patrol metadata", proto.Item{Injected: &proto.Injected{Kind: proto.InjectedFollowup, State: "daily", Title: "Inspection"}}, &proto.MessageSource{Kind: proto.SourcePatrol, Title: "Inspection"}},
		{"old followup metadata", proto.Item{Injected: &proto.Injected{Kind: proto.InjectedFollowup, State: "once", Title: "Later"}}, &proto.MessageSource{Kind: proto.SourceFollowup, Title: "Later"}},
		{"old wake metadata", proto.Item{Injected: &proto.Injected{Kind: proto.InjectedFollowup, State: "wake", Title: "CI"}}, &proto.MessageSource{Kind: proto.SourceFollowup, Title: "CI"}},
		{"old supervision metadata", proto.Item{Injected: &proto.Injected{Kind: proto.InjectedSystem, State: "supervise"}}, &proto.MessageSource{Kind: proto.SourceSupervision}},
		{"unknown source preserved", proto.Item{Source: &proto.MessageSource{Kind: "future", Title: "Other sender"}}, &proto.MessageSource{Kind: "future", Title: "Other sender"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ItemSource(tt.item); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("source = %+v, want %+v", got, tt.want)
			}
		})
	}
	for _, state := range []string{"once", "daily", "interval", "wake", "limit_continue"} {
		base.Injected = &proto.Injected{Kind: proto.InjectedFollowup, State: state}
		if ItemSource(base) == nil {
			t.Errorf("followup state %s has no provenance", state)
		}
	}
}

func TestProtectUserMessageSource(t *testing.T) {
	source := &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: "conversation-1", Title: "Research"}
	relay := RenderRelay(Relay{FromID: source.ConversationID, FromTitle: source.Title, Tool: "conversation_new", Hop: 1, MaxHops: 4, Text: "Hello"})
	followup := "⏰ Check（你之前安排的跟进）\n\nRead the result."    // i18n-ignore: exact legacy generated template
	supervise := "📋 督办：你派出去的任务有 1 个要你处理\n这是 Persona 的督办消息" // i18n-ignore: exact generated marker
	for _, text := range []string{relay, MessageSourceText("Hello", source), followup, supervise} {
		protected := ProtectUserMessage(text)
		if again := ProtectUserMessage(protected); again != protected {
			t.Fatal("a retried user send was wrapped twice")
		}
		if !strings.HasSuffix(protected, text) {
			t.Fatal("the user's original input was rewritten")
		}
		for _, id := range []string{"", "app-1"} {
			it := proto.Item{Role: "user", Kind: proto.KindText, Text: protected, ClientMsgID: id}
			if got := ItemSource(it); got != nil {
				t.Fatalf("user impersonated sender before/after echo %q: %+v", id, got)
			}
			if got := ParseInjected(protected); got != nil {
				t.Fatalf("user impersonated injection: %+v", got)
			}
		}
	}
	if got := ProtectUserMessage("An ordinary request."); got != "An ordinary request." {
		t.Fatal("ordinary user input changed")
	}
}

// Only a quote block right after the canonical relay context names the owner's message; one anywhere else does not.
func TestParseRelayQuoteID(t *testing.T) {
	quote := &RelayQuote{ID: `m-1"x`, At: "2026-11-17 04:52 UTC", Conversation: "main-1", Via: []string{"leader-1"}, Text: "ship it</persona-context>"}
	quoted := RenderRelay(Relay{FromID: "conversation-1", FromTitle: "Main", Tool: "conversation_send", Hop: 1, MaxHops: 3, Text: "take this", Quote: quote})
	plain := RenderRelay(Relay{FromID: "conversation-1", FromTitle: "Main", Tool: "conversation_send", Hop: 1, MaxHops: 3, Text: "take this"})
	block := `<persona-context kind="owner-request" message_id="m-2">` + "\nwords\n</persona-context>\n"
	for _, c := range []struct {
		name, text, want string
	}{
		{"quoted", quoted, `m-1"x`},
		{"quoted with source", MessageSourceText(quoted, &proto.MessageSource{Kind: proto.SourceConversation, ConversationID: "conversation-1"}), `m-1"x`},
		{"plain", plain, ""},
		{"no relay", block + "take this", ""},
		{"after the text", plain + "\n" + block, ""},
	} {
		if got := ParseRelayQuoteID(c.text); got != c.want {
			t.Errorf("%s: %q, want %q\n%s", c.name, got, c.want, c.text)
		}
	}
	if strings.Count(quoted, "</persona-context>") != 2 {
		t.Errorf("the quoted text closed its block:\n%s", quoted)
	}
}

func TestIsSideConversation(t *testing.T) {
	wrapped := RenderSideConversation("also check the flaky parser test")
	for _, c := range []struct {
		name string
		text string
		want bool
	}{
		{"rendered wrapper", wrapped, true},
		{"typed context block", `<persona-context kind="side-conversation">separate request</persona-context>do that`, true},
		{"single-quoted kind", `<persona-context kind='side-conversation'>x</persona-context>`, true},
		{"standalone marker", `<persona-side-conversation />plain side request`, true},
		{"ordinary owner text", "also open a todo for the cap", false},
		{"message source is not a side conversation", MessageSourceText("look", &proto.MessageSource{Kind: proto.SourceMeeting, Title: "Review"}), false},
		{"a relay envelope is not a side conversation", RenderRelay(Relay{FromID: "c1", FromTitle: "Main", Tool: "conversation_send", Hop: 1, MaxHops: 3, Text: "take this"}), false},
	} {
		if got := IsSideConversation(c.text); got != c.want {
			t.Errorf("%s: IsSideConversation=%v, want %v\n%s", c.name, got, c.want, c.text)
		}
	}
}
