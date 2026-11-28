package lark

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The agents' cards on a fake core (asks.go, ask_cards.go): what the module does with what the core says of an
// interaction (Host.AskView, AskRoute, AskSensitive, AskState). What the core says, and the cards end to end, are the
// assistant's tests (lark_asks_test.go there). All ids are placeholders.

func askQuestion(id string) proto.Interaction {
	return proto.Interaction{ID: id, Kind: proto.IxQuestion, MachineID: "server", SessionID: "a1",
		Questions: []proto.Question{{Question: "Ship it?", Options: []proto.QuestionOption{{Label: "Yes"}, {Label: "No"}}}}}
}

// A card is wanted with the cards switch on, for a conversation the chat drives; "all cards" takes the others, but not
// one that says something sensitive, and the core is asked about that only then.
func TestAskWantedFollowsTheSwitchesAndTheRoute(t *testing.T) {
	for _, c := range []struct {
		name               string
		cards, all         bool
		private, driven    bool
		direct, sensitive  bool
		want, askSensitive bool
	}{
		{name: "cards off", driven: true},
		{name: "direct approval with cards off", direct: true},
		{name: "a Just me card", cards: true, driven: true, private: true},
		{name: "a private direct approval", cards: true, direct: true, private: true},
		{name: "driven", cards: true, driven: true, sensitive: true, want: true},
		{name: "not driven", cards: true},
		{name: "direct approval", cards: true, direct: true, want: true, askSensitive: true},
		{name: "direct sensitive approval", cards: true, direct: true, sensitive: true, askSensitive: true},
		{name: "all cards", cards: true, all: true, want: true, askSensitive: true},
		{name: "all cards, sensitive", cards: true, all: true, sensitive: true, askSensitive: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeHub()
			f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
				return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0),
					Features: FeishuFeatures{Main: true, Cards: c.cards, AllCards: c.all}}, nil
			})
			s := newTestService(t, f)
			s.chat().setBotCurrent(botTarget{Kind: targetAgent, MachineID: "server", SessionID: "other"})
			asked := false
			s.ch().route = func(proto.Interaction) assistantwire.AskRoute { return assistantwire.AskRoute{Driven: c.driven} }
			s.ch().direct = func(proto.Interaction) bool { return c.direct }
			s.ch().sensitive = func(proto.Interaction) bool { asked = true; return c.sensitive }
			ix := askQuestion("ix_q")
			ix.Private = c.private
			if got := s.layer.AskWanted(context.Background(), ix); got != c.want || asked != c.askSensitive {
				t.Fatalf("wanted %v (want %v), the core asked whether it is sensitive: %v (want %v)", got, c.want, asked, c.askSensitive)
			}
		})
	}
}

// The chat's own target drives its conversation: the main conversation by what the core says is main, an agent by
// its ids.
func TestAskWantedForTheChatsTarget(t *testing.T) {
	f, _ := feishuHub("ou_a", time.Unix(100, 0))
	s := newTestService(t, f)
	ctx := context.Background()
	main := false
	s.ch().route = func(proto.Interaction) assistantwire.AskRoute { return assistantwire.AskRoute{Main: main} }
	if s.layer.AskWanted(ctx, askQuestion("ix_1")) {
		t.Fatal("a card of a conversation that is not the main one was wanted")
	}
	main = true
	if !s.layer.AskWanted(ctx, askQuestion("ix_1")) {
		t.Fatal("the main conversation's card was not wanted")
	}
	main = false
	s.chat().setBotCurrent(botTarget{Kind: targetAgent, MachineID: "server", SessionID: "a1"})
	if !s.layer.AskWanted(ctx, askQuestion("ix_1")) {
		t.Fatal("the card of the agent the chat talks to was not wanted")
	}
}

// The card goes under the run it answers: the private chat's wins and the topic is told, a topic's alone goes into
// the topic and never detours into the group.
func TestAskCardGoesWhereItsRunsAre(t *testing.T) {
	topic := &assistantwire.AskRun{ReplyTo: "om_topic", To: feishuTarget{ChatID: "oc_g", InThread: true}}
	private := &assistantwire.AskRun{ReplyTo: "om_dm"}
	for _, c := range []struct {
		name          string
		route         assistantwire.AskRoute
		direct        bool
		reply, chat   string
		noDetour      bool
		note, noteMsg string
	}{
		{name: "no run"},
		{name: "private", route: assistantwire.AskRoute{Private: private}, reply: "om_dm"},
		{name: "topic", route: assistantwire.AskRoute{Topic: topic}, reply: "om_topic", chat: "oc_g", noDetour: true},
		{name: "direct approval from a topic", route: assistantwire.AskRoute{Topic: topic}, direct: true, note: "oc_g", noteMsg: "om_topic"},
		{name: "both", route: assistantwire.AskRoute{Private: private, Topic: topic}, reply: "om_dm", note: "oc_g", noteMsg: "om_topic"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, posts := feishuHub("ou_a", time.Unix(100, 0))
			s := newTestService(t, f)
			s.ch().route = func(proto.Interaction) assistantwire.AskRoute { return c.route }
			s.ch().direct = func(proto.Interaction) bool { return c.direct }
			s.layer.AskPost(context.Background(), askQuestion("ix_q"))
			if len(*posts) != 1 {
				t.Fatalf("posts %v", *posts)
			}
			p := (*posts)[0]
			chat, _ := p["chat_id"].(string)
			noDetour, _ := p["no_detour"].(bool)
			if p["reply_to"] != c.reply || chat != c.chat || noDetour != c.noDetour {
				t.Fatalf("posted %v", p)
			}
			notes := f.appParams(AppFeishu, FeishuText)
			if c.note == "" {
				if len(notes) != 0 {
					t.Fatalf("notes %v", notes)
				}
				return
			}
			if len(notes) != 1 || notes[0]["chat_id"] != c.note || notes[0]["reply_to"] != c.noteMsg {
				t.Fatalf("notes %v", notes)
			}
		})
	}
}

// A card whose interaction ended before it was posted is not posted, and one that ended while it was being posted is
// closed at once.
func TestAskCardThatEndedIsNotLeftOpen(t *testing.T) {
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	s := newTestService(t, f)
	ended := true
	s.ch().state = func(string) (bool, bool) { return ended, !ended }
	s.layer.AskPost(context.Background(), askQuestion("ix_q"))
	if len(*posts) != 0 {
		t.Fatalf("an ended question was asked: %v", *posts)
	}
	ended = false
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		*posts = append(*posts, m)
		ended = true // answered while it is being posted
		return map[string]any{"message_id": "om_card"}, nil
	})
	s.layer.AskPost(context.Background(), askQuestion("ix_q2"))
	if len(*posts) != 2 || (*posts)[1]["update"] != true || (*posts)[1]["message_id"] != "om_card" {
		t.Fatalf("posts %v", *posts)
	}
	if s.layer.CardsWaiting() != 0 {
		t.Fatal("the ended card is still kept")
	}
}

// The card is laid out from what the core says of the interaction: the task before the agent in the header, the
// agent's place under it, the conversation's page as the button, and the tool by the core's name for it.
func TestAskCardIsLaidOutFromTheView(t *testing.T) {
	ix := proto.Interaction{ID: "ix_p", Kind: proto.IxPermission, MachineID: "mac", SessionID: "a1", ToolName: "Bash", Summary: "ls"}
	v := assistantwire.AskView{Agent: "worker", Machine: "Mac", Secondary: "tab", Task: "[Docs] Fix the guide", Tool: "Shell",
		OpenLabel: "Open the conversation", OpenURL: "https://hub.example/s/mac/a1", OpenInline: true}
	js := interactionCard(i18n.Lang("en"), ix, v).JSON()
	for _, want := range []string{"[Docs] Fix the guide", "worker · Mac · tab", "**Shell**", "https://hub.example/s/mac/a1"} {
		if !strings.Contains(js, want) {
			t.Errorf("the card lacks %q: %s", want, js)
		}
	}
	same := v
	same.Task, same.TaskSame = "[Docs] ", true
	if title, sub := ixWho(same); title != "[Docs] worker" || sub != "Mac · tab" {
		t.Errorf("an agent named after its task: %q / %q", title, sub)
	}
	if where := ixWhere(v); where != "[Docs] Fix the guide · worker · Mac" {
		t.Errorf("where %q", where)
	}
	done := answeredCardOf(i18n.Lang("en"), ix, v, "Allowed").JSON()
	if !strings.Contains(done, "](https://hub.example/s/mac/a1)") || !strings.Contains(done, "Shell ls") || strings.Contains(done, `"callback"`) {
		t.Errorf("the answered card: %s", done)
	}
	v.OpenInline = false
	if done := answeredCardOf(i18n.Lang("en"), ix, v, "Allowed").JSON(); strings.Contains(done, "](https://hub.example/s/mac/a1)") {
		t.Errorf("an address a Markdown link cannot hold is linked: %s", done)
	}
	v.OpenLabel, v.OpenURL = "", ""
	if js := interactionCard(i18n.Lang("en"), ix, v).JSON(); strings.Contains(js, "hub.example") || openPage(v) != nil {
		t.Errorf("a card without a page has a link: %s", js)
	}
}

// The link an elicitation opens is the one given, exactly; one that reads as one site and opens another is none.
func TestElicitationTargetIsExact(t *testing.T) {
	if lk, host := elicitationTarget(" https://example.com/a?b=c%20d "); lk != "https://example.com/a?b=c%20d" || host != "example.com" {
		t.Fatalf("target %q %q", lk, host)
	}
	for _, bad := range []string{"https://github.com@evil.example/login", "https://user:pw@example.com/", "https:///example.com", "https:example.com", "javascript:alert(1)"} {
		if lk, host := elicitationTarget(bad); lk != "" || host != "" {
			t.Errorf("%q: %q %q", bad, lk, host)
		}
	}
}

// A card closed because its node went away asks again, in the same message, when the same interaction is back within
// the core's grace; past it, or never detached, it stays as it is.
func TestAskDetachedCardComesBack(t *testing.T) {
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	s := newTestService(t, f)
	now := time.Unix(1000, 0)
	s.ch().now = func() time.Time { return now }
	ix := askQuestion("ix_q")
	s.layer.AskPost(context.Background(), ix)
	s.layer.AskDetached([]string{"ix_q", "ix_unknown"}, time.Minute)
	updates := func() []map[string]any {
		var out []map[string]any
		for _, p := range f.appParams(AppFeishu, FeishuUpdateCard) {
			out = append(out, p)
		}
		return out
	}
	waitFor(t, "the card to close", func() bool { return len(updates()) == 1 })
	if card, _ := updates()[0]["card"].(string); strings.Contains(card, `"callback"`) || s.layer.CardsWaiting() != 0 {
		t.Fatalf("the detached card still asks: %s", card)
	}
	s.layer.AskBack(ix, time.Minute)
	if got := updates(); len(got) != 2 || got[1]["message_id"] != "om_card" || !strings.Contains(got[1]["card"].(string), `"callback"`) {
		t.Fatalf("the card did not ask again: %v", got)
	}
	if s.layer.CardsWaiting() != 1 || len(*posts) != 3 {
		t.Fatalf("cards %d, posts %v", s.layer.CardsWaiting(), *posts)
	}
	// Away past the grace: the card stays closed.
	s.layer.AskDetached([]string{"ix_q"}, time.Minute)
	waitFor(t, "the card to close again", func() bool { return len(updates()) == 3 })
	now = now.Add(2 * time.Minute)
	s.layer.AskBack(ix, time.Minute)
	if len(updates()) != 3 || s.layer.CardsWaiting() != 0 {
		t.Fatalf("a card away past the grace asked again: %v", updates())
	}
}

// A card whose node went away while Feishu had its post but had not answered the message id yet is closed once the id
// comes, and comes back only for the same interaction still up: a card back for another session or an earlier
// generation, or one no longer up, does not take the message (the core's TestFeishuReconnectWhileFirstCardPostAwaitsAcknowledgement drives
// the same path through the fleet).
func TestAskDetachedWhilePostingComesBackForTheSameGeneration(t *testing.T) {
	for _, c := range []struct {
		name string
		up   bool // the interaction is still up when the card comes back
	}{{"still up", true}, {"no longer up", false}} {
		t.Run(c.name, func(t *testing.T) {
			f, _ := feishuHub("ou_a", time.Unix(100, 0))
			received, release := make(chan struct{}), make(chan struct{})
			f.answerAppBlocking(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) {
				close(received)
				<-release
				return map[string]any{"message_id": "om_card"}, nil
			})
			s := newTestService(t, f)
			now := time.Unix(1000, 0)
			s.ch().now = func() time.Time { return now }
			first := askQuestion("ix_q")
			first.CreatedAt = time.Unix(900, 0)
			next := first
			next.CreatedAt = time.Unix(950, 0) // the same card asked again once the node is back
			other := next
			other.SessionID = "a2" // the same id up for another session
			up := other
			s.ch().pendingIx = func(string) (proto.Interaction, bool) { return up, c.up }
			posted := make(chan struct{})
			go func() {
				defer close(posted)
				s.layer.AskPost(context.Background(), first)
			}()
			<-received
			s.layer.AskDetached([]string{"ix_q"}, time.Minute)
			close(release)
			<-posted
			updates := func() []map[string]any { return f.appParams(AppFeishu, FeishuUpdateCard) }
			waitFor(t, "the card to close", func() bool { return len(updates()) == 1 })
			if got := updates()[0]; got["message_id"] != "om_card" || strings.Contains(got["card"].(string), `"callback"`) {
				t.Fatalf("the card was not closed: %v", got)
			}
			s.layer.AskBack(other, time.Minute)
			if len(updates()) != 1 || s.layer.CardsWaiting() != 0 {
				t.Fatalf("another session took the card: %v", updates())
			}
			s.layer.AskBack(next, time.Minute) // while the id is up for the other session
			if len(updates()) != 1 || s.layer.CardsWaiting() != 0 {
				t.Fatalf("the card came back while its id is up for another session: %v", updates())
			}
			up = next
			s.layer.AskBack(first, time.Minute) // an earlier generation's
			if len(updates()) != 1 || s.layer.CardsWaiting() != 0 {
				t.Fatalf("an earlier generation took the card: %v", updates())
			}
			s.layer.AskBack(next, time.Minute)
			want := 1
			if c.up {
				want = 2
			}
			if got := updates(); len(got) != want || (c.up && !strings.Contains(got[1]["card"].(string), `"callback"`)) {
				t.Fatalf("updates %v, want %d", got, want)
			}
			if posts := f.appParams(AppFeishu, FeishuPost); len(posts) != 1 {
				t.Fatalf("posted %d cards", len(posts))
			}
		})
	}
}
