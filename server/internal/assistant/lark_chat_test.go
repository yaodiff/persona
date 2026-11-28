package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
)

// The Feishu chat is the lark module's (lark_chat.go): these tests reach it as the core does, through the port, and
// read its state as it is stored (the settings row botStateKey, in the shape below).

// botStateKey is the chat's state, the settings row the module keeps it in.
const botStateKey = "bots.feishu.state"

// botState is the chat's state as it is stored (the module's chat_state.go): what these tests read and seed of it.
type botState struct {
	Current botTarget              `json:"current"`
	Threads map[string]*botThread  `json:"threads,omitempty"`
	Runs    []map[string]any       `json:"runs,omitempty"`
	Seen    []string               `json:"seen,omitempty"`
	Cards   map[string]*botCardRef `json:"cards,omitempty"`
	Pairing *string                `json:"pairing,omitempty"`
	Topics  map[string]*struct {
		MessageID string    `json:"message_id"`
		At        time.Time `json:"at"`
	} `json:"topics,omitempty"`
	Board      *botBoardRef   `json:"board,omitempty"`
	Companions []CompanionBot `json:"companions,omitempty"`
	NudgeOffAt time.Time      `json:"nudge_off_at,omitzero"`
}

// botThread is a message the bot posted for a target, as the chat's state keeps it.
type botThread struct {
	Target botTarget `json:"target"`
	Title  string    `json:"title,omitempty"`
	At     time.Time `json:"at"`
}

// botCardRef is a card kept in the chat's state.
type botCardRef = assistantwire.ChatCardRef

// setLarkNow sets the chat's clock (nil: the time now).
func (s *Service) setLarkNow(now func() time.Time) {
	s.lark.mu.Lock()
	defer s.lark.mu.Unlock()
	s.lark.now = now
}

// chatCard is the card asking interaction id in the chat's records.
func chatCard(s *Service, id string) (botCard, bool) {
	var c botCard
	ok := false
	s.larkModule().Book(context.Background(), func(b assistantwire.ChatBook) { c, ok = b.Card(id) })
	return c, ok
}

// chatCards are the cards in the chat's records.
func chatCards(s *Service) map[string]botCard {
	var cards map[string]botCard
	s.larkModule().Book(context.Background(), func(b assistantwire.ChatBook) { cards = b.Cards() })
	return cards
}

// keepChatCard records the card asking interaction id (nil forgets it).
func keepChatCard(s *Service, id string, c *botCard) {
	s.larkModule().Book(context.Background(), func(b assistantwire.ChatBook) { b.KeepCard(id, c) })
}

// updateChatCard changes a card and its persisted ref through the module's production write path.
func updateChatCard(s *Service, id string, update func(*botCard)) {
	s.larkModule().Book(context.Background(), func(b assistantwire.ChatBook) {
		card, ok := b.Card(id)
		if !ok {
			return
		}
		update(&card)
		b.KeepCard(id, &card)
	})
}

// chatCardRef is the card asking interaction id as the chat's state keeps it.
func chatCardRef(s *Service, id string) (botCardRef, bool) {
	var ref botCardRef
	ok := false
	s.larkModule().Book(context.Background(), func(b assistantwire.ChatBook) { ref, ok = b.CardRef(id) })
	return ref, ok
}

// feishuSay hands ev to the chat as the hub does (feishu.message), action being the owner's action for what it
// answers ("" none), and returns once the chat has handled it. With st the bot's status is st from then on (the
// hub's feishu.status); nil keeps the one the fake answers.
func feishuSay(t testing.TB, s *Service, f *fakeHub, st *FeishuStatus, ev botcard.MessageEvent, action string) {
	t.Helper()
	if st != nil {
		cur := *st
		f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) { return cur, nil })
		s.larkModule().ForgetStatus()
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FeishuMessage(context.Background(), FeishuMessage{Event: raw, Action: action}); err != nil {
		t.Fatalf("feishu.message %s: %v", ev.Message.MessageID, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.larkModule().Wait(ctx); err != nil {
		t.Fatalf("the chat did not handle %s: %v", ev.Message.MessageID, err)
	}
}

// botSay is the owner's text message mid in the bot's private chat, replying to parent in the thread rooted at root,
// handed over with the bot's switches ff (the status's owner and pairing kept): feishuSay.
func botSay(t testing.TB, s *Service, f *fakeHub, ff FeishuFeatures, mid, text, root, parent string) {
	t.Helper()
	feishuSay(t, s, f, withFeatures(f, ff), p2pText(mid, text, root, parent), "")
}

// withFeatures is the status the fake answers now, with the bot's switches ff.
func withFeatures(f *fakeHub, ff FeishuFeatures) *FeishuStatus {
	var st FeishuStatus
	f.mu.Lock()
	answer := f.apps[AppFeishu+" "+FeishuStatusGet]
	f.mu.Unlock()
	if answer != nil {
		if v, err := answer(nil); err == nil {
			raw, _ := json.Marshal(v)
			_ = json.Unmarshal(raw, &st)
		}
	}
	st.Features = ff
	return &st
}

// p2pText is a text message mid in the bot's private chat, replying to parent in the thread rooted at root.
func p2pText(mid, text, root, parent string) botcard.MessageEvent {
	var ev botcard.MessageEvent
	ev.Message.MessageID, ev.Message.ChatType, ev.Message.MessageType = mid, "p2p", "text"
	ev.Message.RootID, ev.Message.ParentID = root, parent
	content, _ := json.Marshal(map[string]string{"text": text})
	ev.Message.Content = string(content)
	ev.Sender.SenderType = "user"
	return ev
}

// botAnswer hands the owner's text message mid (replying to parent in the thread rooted at root, with action) to the
// chat and says whether the chat took it as the answer to a card: it did not go on into a conversation.
func botAnswer(t testing.TB, s *Service, f *fakeHub, mid, text, action, root, parent string) bool {
	t.Helper()
	before := len(sendOnces(f))
	feishuSay(t, s, f, nil, p2pText(mid, text, root, parent), action)
	return len(sendOnces(f)) == before
}

// seedTopicAnchor makes message id the latest the owner said in the topic (chat, thread), as the hub hands one over:
// the lines the bot says into the topic reply under it. The topic is bound to no to-do yet, so the chat tells it so (a
// hint under the message); that hint is taken out of what the fake recorded, and the test sees only its own lines.
func seedTopicAnchor(t testing.TB, s *Service, f *fakeHub, chat, thread, id string) {
	t.Helper()
	st := withFeatures(f, FeishuFeatures{})
	var ev botcard.MessageEvent
	ev.Message.MessageID, ev.Message.ChatID, ev.Message.ChatType, ev.Message.ThreadID = id, chat, "group", thread
	ev.Message.RootID, ev.Message.ParentID = "om_root", "om_root"
	ev.Message.MessageType, ev.Message.Content = "text", `{"text":"seed"}`
	ev.Sender.SenderType, ev.Sender.SenderID.OpenID = "user", st.Owner
	feishuSay(t, s, f, nil, ev, "")
	if got := s.larkModule().TopicAnchor(context.Background(), chat, thread); got != id {
		t.Fatalf("the topic's anchor is %q, want %q", got, id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appCalls = slices.DeleteFunc(f.appCalls, func(c AppCall) bool {
		var p map[string]any
		_ = json.Unmarshal(c.Params, &p)
		return c.AppID == AppFeishu && c.Method == FeishuText && p["reply_to"] == id
	})
}

// botCommandCard is the card a command (text, message mid) posts in the private chat with the bot's switches ff (nil:
// the status the fake answers, as it is): its JSON and the text it goes as when refused.
func botCommandCard(t testing.TB, s *Service, f *fakeHub, ff *FeishuFeatures, mid, text string) (card, fallback string) {
	t.Helper()
	before := len(f.appParams(AppFeishu, FeishuPost))
	if ff != nil {
		botSay(t, s, f, *ff, mid, text, "", "")
	} else {
		feishuSay(t, s, f, nil, p2pText(mid, text, "", ""), "")
	}
	posts := f.appParams(AppFeishu, FeishuPost)
	if len(posts) != before+1 {
		t.Fatalf("%s posted %d cards, want 1", text, len(posts)-before)
	}
	card, _ = posts[len(posts)-1]["card"].(string)
	fallback, _ = posts[len(posts)-1]["fallback"].(string)
	return card, fallback
}

// feishuHand hands ev over as the hub does a message it marked as from (assistantwire.FeishuFrom*: a /read, an /ai
// from elsewhere), and returns once the chat handled it.
func feishuHand(t testing.TB, s *Service, f *fakeHub, ev botcard.MessageEvent, from string) {
	t.Helper()
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FeishuMessage(context.Background(), FeishuMessage{Event: raw, From: from}); err != nil {
		t.Fatalf("feishu.message %s: %v", ev.Message.MessageID, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.larkModule().Wait(ctx); err != nil {
		t.Fatalf("the chat did not handle %s: %v", ev.Message.MessageID, err)
	}
}

// messagesSaid numbers the messages the tests hand the chat (nextMessageID): each is a message of its own, also for a
// chat that restarted on the same store (it remembers the messages it took).
var messagesSaid atomic.Int64

// nextMessageID is a new message id starting with prefix.
func nextMessageID(prefix string) string { return fmt.Sprintf("%s_%d", prefix, messagesSaid.Add(1)) }
