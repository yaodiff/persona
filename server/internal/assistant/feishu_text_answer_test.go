package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/proto"
)

// textAnswerBot pairs the Feishu bot with ixs pending and records card posts, chat notes and resolves.
func textAnswerBot(t *testing.T, resolve appAnswer, ixs ...proto.Interaction) (*Service, *fakeHub, *[]map[string]any, *[]map[string]any, *[]map[string]any) {
	t.Helper()
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.withQueue(ixs...)
	var notes, resolves []map[string]any
	f.answerApp(AppFeishu, FeishuText, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		notes = append(notes, m)
		return map[string]any{}, nil
	})
	f.answerApp(AppInteractions, InteractionsResolve, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		resolves = append(resolves, m)
		if resolve != nil {
			return resolve(p)
		}
		return map[string]any{"ok": true}, nil
	})
	s := newTestService(t, f)
	_, _ = s.chat().Status(context.Background())
	return s, f, posts, &notes, &resolves
}
func seedCard(s *Service, ix proto.Interaction) {
	keepChatCard(s, ix.ID, &botCard{MessageID: "om_" + ix.ID, Ix: ix, At: time.Unix(1000, 0)})
}
func seedThreadCard(s *Service, ix proto.Interaction, thread string, at int64) {
	keepChatCard(s, ix.ID, &botCard{MessageID: "om_" + ix.ID, Thread: thread, Ix: ix, At: time.Unix(at, 0)})
}
func qCard(id string, multi bool, opts ...string) proto.Interaction {
	qos := make([]proto.QuestionOption, len(opts))
	for i, o := range opts {
		qos[i] = proto.QuestionOption{Label: o}
	}
	return proto.Interaction{ID: id, MachineID: "server", SessionID: "m1", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "选哪个？", MultiSelect: multi, Options: qos}}}
}
func noteText(ns *[]map[string]any) string {
	var b strings.Builder
	for _, n := range *ns {
		b.WriteString(n["text"].(string))
	}
	return b.String()
}

// A typed option resolves like the press (same id, answers, one-use owner action); the latest posted card wins.
func TestTypedAnswerResolvesTheCard(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	for _, c := range []struct {
		text, want, id string
		multi          bool
	}{
		{"2", "上海", "ix_a", false}, {"广州", "广州", "ix_b", false}, {"1、3", "北京, 广州", "ix_c", true},
	} {
		ix := qCard(c.id, c.multi, "北京", "上海", "广州")
		s, f, _, _, rs := textAnswerBot(t, nil, ix)
		seedCard(s, ix)
		if !botAnswer(t, s, f, "om_r", c.text, "oa_1", "", "") || len(*rs) != 1 {
			t.Fatalf("%q not resolved: %v", c.text, *rs)
		}
		r := (*rs)[0]
		ans, _ := r["resolution"].(map[string]any)["answers"].(map[string]any)
		if r["interaction_id"] != c.id || r["action"] != "oa_1" || ans["选哪个？"] != c.want {
			t.Fatalf("%q resolve %v", c.text, r)
		}
	}
	old, latest := qCard("ix_old", false, "甲", "乙"), qCard("ix_new", false, "子", "丑")
	s, f, _, _, rs := textAnswerBot(t, nil, old, latest)
	seedCard(s, old)
	keepChatCard(s, "ix_new", &botCard{MessageID: "om_ix_new", Ix: latest, At: time.Unix(2000, 0)})
	if !botAnswer(t, s, f, "om_r", "2", "oa_1", "", "") || (*rs)[0]["interaction_id"] != "ix_new" {
		t.Fatalf("the latest card was not the one answered: %v", *rs)
	}
	if _, ok := chatCard(s, "ix_old"); !ok {
		t.Fatal("the older card was closed")
	}
	// The card says a plain reply works for one question with options, not for a free-text question.
	hint := "也可以直接在聊天里回复序号或选项名"
	if js := s.interactionCard(context.Background(), old).JSON(); !strings.Contains(js, hint) {
		t.Fatalf("no text-answer hint: %s", js)
	}
	freeQ := proto.Interaction{Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "为什么？"}}}
	if strings.Contains(s.interactionCard(context.Background(), freeQ).JSON(), hint) {
		t.Fatal("a free-text question promises a typed answer")
	}
}

func TestTypedAnswerUsesOnlyTheDisplayedOccurrence(t *testing.T) {
	needsLarkLayer(t)
	first := qCard("ix_occurrence", false, "first yes", "first no")
	first.CreatedAt = time.Date(2026, 11, 24, 10, 0, 0, 123456789, time.UTC)
	replacement := qCard("ix_occurrence", false, "replacement yes", "replacement no")
	replacement.CreatedAt = first.CreatedAt.Add(time.Second)

	s, f, _, _, resolves := textAnswerBot(t, nil, replacement)
	seedCard(s, first)
	if !botAnswer(t, s, f, "om_stale", "2", "oa_stale", "", "") {
		t.Fatal("the typed answer was not consumed")
	}
	if len(*resolves) != 0 {
		t.Fatalf("an answer constructed from the first question resolved its replacement: %+v", *resolves)
	}

	s, f, _, _, resolves = textAnswerBot(t, nil, first)
	seedCard(s, first)
	if !botAnswer(t, s, f, "om_current", "2", "oa_current", "", "") || len(*resolves) != 1 {
		t.Fatalf("the current occurrence was not answered: %+v", *resolves)
	}
	if got := (*resolves)[0]["created_at"]; got != first.CreatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("typed resolve occurrence = %v, want %s", got, first.CreatedAt.Format(time.RFC3339Nano))
	}
}

func TestTypedAnswerEndedLookupKeepsReplacementLedger(t *testing.T) {
	needsLarkLayer(t)
	first := qCard("ix_ended_occurrence", false, "first yes", "first no")
	first.CreatedAt = time.Date(2026, 11, 25, 2, 0, 0, 123456789, time.UTC)
	replacement := qCard(first.ID, false, "replacement yes", "replacement no")
	replacement.CreatedAt = first.CreatedAt.Add(time.Second)

	s, f, _, _, _ := textAnswerBot(t, nil)
	seedCard(s, first)
	f.answerAppBlocking(AppInteractions, InteractionsGet, func(json.RawMessage) (any, error) {
		// The old occurrence's lookup returns ended, but its replacement arrives
		// before the old answer's optimistic ledger cleanup runs.
		seedCard(s, replacement)
		return map[string]any{"ok": false, "status": "cancelled"}, nil
	})
	if !botAnswer(t, s, f, "om_ended_stale", "2", "oa_ended_stale", "", "") {
		t.Fatal("the old occurrence's typed answer was not consumed")
	}
	got, ok := chatCard(s, first.ID)
	if !ok || got.MessageID != "om_"+first.ID || !got.Ix.CreatedAt.Equal(replacement.CreatedAt) {
		t.Fatalf("the old occurrence's ended lookup removed its replacement: ok=%v card=%+v", ok, got)
	}
}

// Non-options/no-action/plan/form messages stay for the agent; over-elsewhere handled and swallowed; failed resolve keeps waiting.
func TestTypedAnswerGuards(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	free, plan := qCard("ix_q", false, "北京", "上海"), proto.Interaction{ID: "ix_plan", Kind: proto.IxPlan}
	form := proto.Interaction{ID: "ix_form", Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "一"}, {Question: "二"}}}
	for _, c := range []struct {
		name, text, action, note string
		card                     proto.Interaction
		pending                  bool
		resolve                  appAnswer
		taken, closed            bool
		resolves                 int
	}{
		{"free text", "帮我查天气", "oa_1", "", free, true, nil, false, false, 0},
		{"no action", "2", "", "", free, true, nil, false, false, 0},
		{"plan", "2", "oa_1", "", plan, true, nil, false, false, 0},
		{"form", "1", "oa_1", "", form, true, nil, false, false, 0},
		{"ended elsewhere", "2", "oa_1", "已经在别处处理过了", free, false, nil, true, true, 0},
		{"resolve error", "2", "oa_1", "没交上去", free, true, func(json.RawMessage) (any, error) { return nil, errors.New("hub restarted") }, true, false, 1},
		{"resolve race", "2", "oa_1", "已经在别处处理过了", free, true, func(json.RawMessage) (any, error) { return map[string]any{"ok": false}, nil }, true, true, 1},
	} {
		s, f, posts, notes, rs := textAnswerBot(t, c.resolve, map[bool][]proto.Interaction{true: {free}}[c.pending]...)
		seedCard(s, c.card)
		if got := botAnswer(t, s, f, "om_r", c.text, c.action, "", ""); got != c.taken || len(*rs) != c.resolves {
			t.Fatalf("%s: taken %v, resolves %v", c.name, got, *rs)
		}
		_, waits := chatCard(s, c.card.ID)
		if waits == c.closed || (c.note != "" && !strings.Contains(noteText(notes), c.note)) || c.closed != (len(*posts) == 1) {
			t.Fatalf("%s: waits %v posts %d notes %q", c.name, waits, len(*posts), noteText(notes))
		}
	}
}

// A message inside an agent's topic only answers a card waiting in that topic; a card elsewhere keeps waiting and the
// words go to that agent. In the root chat the newest card anywhere is still answered.
func TestTypedAnswerStaysInItsTopic(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	rootCard := qCard("ix_root", false, "甲", "乙")
	topicCard := qCard("ix_topic", false, "子", "丑")
	otherCard := qCard("ix_other", false, "寅", "卯")

	// Typed in agent B's topic, where no card waits: cards in the root chat and in other topics are not touched.
	s, f, _, _, rs := textAnswerBot(t, nil, rootCard, topicCard, otherCard)
	seedCard(s, rootCard)
	seedThreadCard(s, topicCard, "om_topic_root", 2000)
	seedThreadCard(s, otherCard, "om_other_root", 3000) // newest overall, but in a different topic
	if botAnswer(t, s, f, "om_r", "2", "oa_1", "om_b_root", "") {
		t.Fatal("a topic with no card there must not take the words")
	}
	if len(*rs) != 0 {
		t.Fatalf("nothing may be resolved: %v", *rs)
	}
	for _, id := range []string{"ix_root", "ix_topic", "ix_other"} {
		if _, ok := chatCard(s, id); !ok {
			t.Fatalf("card %s was closed", id)
		}
	}

	// A card inside that topic is the one answered, even though a card elsewhere is newer.
	s, f, _, _, rs = textAnswerBot(t, nil, topicCard, otherCard)
	seedThreadCard(s, topicCard, "om_topic_root", 1000)
	seedThreadCard(s, otherCard, "om_other_root", 2000)
	if !botAnswer(t, s, f, "om_r", "1", "oa_1", "om_topic_root", "om_topic_root") ||
		len(*rs) != 1 || (*rs)[0]["interaction_id"] != "ix_topic" {
		t.Fatalf("the topic's own card was not answered: %v", *rs)
	}
	if _, ok := chatCard(s, "ix_other"); !ok {
		t.Fatal("the other topic's card was closed")
	}

	// Replying directly to a card posted as a topic root answers it (root id is the card's own message).
	s, f, _, _, rs = textAnswerBot(t, nil, rootCard)
	seedCard(s, rootCard)
	if !botAnswer(t, s, f, "om_r", "2", "oa_1", "om_ix_root", "") ||
		len(*rs) != 1 || (*rs)[0]["interaction_id"] != "ix_root" {
		t.Fatalf("the reply to the card itself was not taken: %v", *rs)
	}
}

// End to end through the handler: the owner's text answers the waiting card and starts no conversation.
func TestFeishuMessageTypedAnswer(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	ix := qCard("ix_q", false, "北京", "上海", "广州")
	s, f, _, _, rs := textAnswerBot(t, nil, ix)
	seedCard(s, ix)
	var ev botcard.MessageEvent
	ev.Message.ChatType = "p2p"
	ev.Message.MessageID, ev.Message.MessageType, ev.Message.Content = "om_in", "text", `{"text":"第二个"}`
	feishuSay(t, s, f, nil, ev, "oa_1")
	if len(*rs) != 1 || (*rs)[0]["interaction_id"] != "ix_q" || sendOnceText(f) != "" {
		t.Fatalf("resolves %v, leaked into chat %q", *rs, sendOnceText(f))
	}
}

// A card that ended elsewhere (answered in the app, withdrawn) is gone from the chat's cards once interaction.ended
// closes it; an option typed for it afterwards is still read as a late answer: the owner is told it was handled
// elsewhere and the words do not start a turn. Free words, and anything after them, go on as ordinary messages.
func TestTypedAnswerToACardEndedElsewhereIsNotForwarded(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	ix := qCard("ix_q", false, "北京", "上海", "广州")
	s, f, _, notes, rs := textAnswerBot(t, nil)
	seedCard(s, ix)
	s.feishuInteractionDone(ix, "已在 App 中回答")
	var ev botcard.MessageEvent
	ev.Message.ChatType = "p2p"
	ev.Message.MessageID, ev.Message.MessageType, ev.Message.Content = "om_in", "text", `{"text":"上海"}`
	feishuSay(t, s, f, nil, ev, "oa_1")
	if got := sendOnceText(f); got != "" || len(*rs) != 0 {
		t.Fatalf("the late answer went into the conversation: %q, resolves %v", got, *rs)
	}
	if !strings.Contains(noteText(notes), "已经在别处处理过了") {
		t.Fatalf("the owner was not told: %q", noteText(notes))
	}
	if botAnswer(t, s, f, "om_r2", "2", "oa_1", "", "") {
		t.Fatal("a second reply is not about the ended card any more")
	}

	// Free words are a new message and forget the ended card: a number after them is ordinary too.
	s, f, _, _, _ = textAnswerBot(t, nil)
	seedCard(s, ix)
	s.feishuInteractionDone(ix, "已在 App 中回答")
	if botAnswer(t, s, f, "om_r", "帮我查天气", "oa_1", "", "") {
		t.Fatal("free words were swallowed")
	}
	if botAnswer(t, s, f, "om_r2", "2", "oa_1", "", "") {
		t.Fatal("a number after a new message was read as a late answer")
	}

	// A card that ended in another agent's topic does not take words written in this one, nor does one past its time.
	s, f, _, _, _ = textAnswerBot(t, nil)
	seedThreadCard(s, ix, "om_other_root", 1000)
	s.feishuInteractionDone(ix, "已在 App 中回答")
	if botAnswer(t, s, f, "om_r", "2", "oa_1", "om_b_root", "") {
		t.Fatal("a card that ended in another topic took the words")
	}
	s, f, _, _, _ = textAnswerBot(t, nil)
	now := time.Unix(5000, 0)
	s.setLarkNow(func() time.Time { return now })
	seedCard(s, ix)
	s.feishuInteractionDone(ix, "已在 App 中回答")
	now = now.Add(endedAskFor + time.Second)
	if botAnswer(t, s, f, "om_r", "2", "oa_1", "", "") {
		t.Fatal("a card that ended long ago took the words")
	}

	// A newer card still waiting is the one a typed option answers, not an older card that ended.
	waiting := qCard("ix_w", false, "子", "丑")
	s, f, _, _, rs = textAnswerBot(t, nil, waiting)
	seedCard(s, ix)
	s.feishuInteractionDone(ix, "已在 App 中回答")
	keepChatCard(s, "ix_w", &botCard{MessageID: "om_ix_w", Ix: waiting, At: time.Unix(2000, 0)})
	if !botAnswer(t, s, f, "om_r", "2", "oa_1", "", "") || len(*rs) != 1 || (*rs)[0]["interaction_id"] != "ix_w" {
		t.Fatalf("the waiting card was not answered: %v", *rs)
	}
}

// Words that name no option answer, inside the card's topic, the free-text question itself, and an option question
// takes them as its Other: both resolve once through the card input box's own path and never reach the conversation.
// In the root chat the words still go to the conversation. A card that is no longer pending (interactions.get !ok)
// swallows them with the handled note, while a card closed into b.ended still lets free words start a turn (#5461).
func TestTypedFreeAnswerInTopic(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	free := proto.Interaction{ID: "ix_free", MachineID: "server", SessionID: "m1", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "为什么？"}}}
	opts := qCard("ix_opts", false, "北京", "上海")

	// A question without options: the words are the answer itself, the card closes, one resolve with the owner action.
	s, f, _, _, rs := textAnswerBot(t, nil, free)
	seedThreadCard(s, free, "om_topic", 1000)
	if !botAnswer(t, s, f, "om_r", "因为我想试试", "oa_1", "om_topic", "") || len(*rs) != 1 {
		t.Fatalf("free answer not resolved: %v", *rs)
	}
	r := (*rs)[0]
	ans, _ := r["resolution"].(map[string]any)["answers"].(map[string]any)
	if r["interaction_id"] != "ix_free" || r["action"] != "oa_1" || ans["为什么？"] != "因为我想试试" {
		t.Fatalf("free resolve %v", r)
	}
	if _, waits := chatCard(s, "ix_free"); waits {
		t.Fatal("the free-text card was not closed")
	}

	// An option question's words that name no option are its Other, submitted as the raw text (the card's free=1 form).
	s, f, _, _, rs = textAnswerBot(t, nil, opts)
	seedThreadCard(s, opts, "om_topic", 1000)
	if !botAnswer(t, s, f, "om_r", "都不选，去杭州", "oa_1", "om_topic", "") || len(*rs) != 1 {
		t.Fatalf("Other answer not resolved: %v", *rs)
	}
	ans, _ = (*rs)[0]["resolution"].(map[string]any)["answers"].(map[string]any)
	if (*rs)[0]["interaction_id"] != "ix_opts" || ans["选哪个？"] != "都不选，去杭州" {
		t.Fatalf("Other resolve %v", (*rs)[0])
	}

	// End to end: the topic message resolves the free-text question and starts no conversation.
	s, f, _, _, rs = textAnswerBot(t, nil, free)
	seedThreadCard(s, free, "om_topic", 1000)
	var ev botcard.MessageEvent
	ev.Message.ChatType = "p2p"
	ev.Message.MessageID, ev.Message.RootID, ev.Message.MessageType, ev.Message.Content =
		"om_in", "om_topic", "text", `{"text":"想换个方案"}`
	feishuSay(t, s, f, nil, ev, "oa_1")
	if len(*rs) != 1 || (*rs)[0]["interaction_id"] != "ix_free" || sendOnceText(f) != "" {
		t.Fatalf("resolves %v, leaked into chat %q", *rs, sendOnceText(f))
	}

	// The same words in the root chat are not an answer there: the card keeps waiting and the message is sent on.
	s, f, _, _, rs = textAnswerBot(t, nil, free)
	seedCard(s, free)
	ev = botcard.MessageEvent{}
	ev.Message.ChatType = "p2p"
	ev.Message.MessageID, ev.Message.MessageType, ev.Message.Content = "om_in2", "text", `{"text":"帮我查天气"}`
	feishuSay(t, s, f, nil, ev, "oa_1")
	if len(*rs) != 0 || !strings.Contains(sendOnceText(f), "帮我查天气") {
		t.Fatalf("root words taken as an answer: resolves %v, sent %q", *rs, sendOnceText(f))
	}
	if _, waits := chatCard(s, "ix_free"); !waits {
		t.Fatal("the root chat's card was closed")
	}

	// A card still shown but no longer pending in the hub (interactions.get !ok) swallows the topic's late free words
	// with the handled note, posts the closed card, resolves nothing — the same as a late option.
	s, f, posts, notes, rs := textAnswerBot(t, nil) // nothing pending in the queue
	seedThreadCard(s, free, "om_topic", 1000)
	if !botAnswer(t, s, f, "om_r", "迟到的回答", "oa_1", "om_topic", "") || len(*rs) != 0 {
		t.Fatalf("ended card answered: %v", *rs)
	}
	if _, waits := chatCard(s, "ix_free"); waits || len(*posts) != 1 ||
		!strings.Contains(noteText(notes), "已经在别处处理过了") {
		t.Fatalf("ended card not closed: posts %d notes %q", len(*posts), noteText(notes))
	}

	// A card closed into b.ended in the topic (#5461) keeps its own rule for free words even with this change: they
	// are not taken as a free answer, they start a turn and nothing is resolved; the ended card is read once.
	s, f, _, _, rs = textAnswerBot(t, nil)
	seedThreadCard(s, free, "om_topic", 1000)
	s.feishuInteractionDone(free, "已在 App 中回答")
	ev = botcard.MessageEvent{}
	ev.Message.ChatType = "p2p"
	ev.Message.MessageID, ev.Message.RootID, ev.Message.MessageType, ev.Message.Content =
		"om_in3", "om_topic", "text", `{"text":"帮我看看别的"}`
	feishuSay(t, s, f, nil, ev, "oa_1")
	if len(*rs) != 0 || !strings.Contains(sendOnceText(f), "帮我看看别的") {
		t.Fatalf("free words to an ended topic card were swallowed: resolves %v, sent %q", *rs, sendOnceText(f))
	}
}
