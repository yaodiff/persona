package lark

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// appendedBodies joins every appended event's content, and returns the types in order.
func appendedBodies(s *Service) (types []string, body string) {
	for _, p := range s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotAppend) {
		raw, _ := json.Marshal(p["events"])
		var batch []botcard.CotEvent
		_ = json.Unmarshal(raw, &batch)
		for _, e := range batch {
			types = append(types, e.Type)
			body += e.Content
		}
	}
	return types, body
}

func completeReasons(s *Service) []string {
	var out []string
	for _, p := range s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotComplete) {
		reason, _ := p["reason"].(string)
		out = append(out, reason)
	}
	return out
}

// A private turn with both switches writes the answer into the agent message and sends no card.
func TestFeishuUnifiedPrivateTurnAnswersInsideAgentMessage(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	cotFeatures(s, "dm_cot", "cot_answer")
	r, _ := cotRun(s)
	topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")
	finishTopicCot(t, s, r, &channel.Result{Answer: "结论在这里"})
	if len(*posts) != 0 {
		t.Fatalf("posts %v, want none", *posts)
	}
	types, body := appendedBodies(s)
	want := "REASONING_END TEXT_MESSAGE_START TEXT_MESSAGE_CONTENT TEXT_MESSAGE_END RUN_FINISHED"
	if !strings.HasSuffix(strings.Join(types, " "), want) {
		t.Fatalf("events %v", types)
	}
	if !strings.Contains(body, "结论在这里") || !strings.Contains(body, "Persona") {
		t.Fatalf("answer text missing from the agent message: %s", body)
	}
	if r.Ext["cot_answer"] != "1" || r.Ext["cot_done"] != "1" {
		t.Fatalf("ext %v", r.Ext)
	}
	if reacts := s.hub.(*fakeHub).appParams(AppFeishu, assistantwire.FeishuReact); len(reacts) == 0 {
		t.Fatal("the message was not marked done")
	}
}

// A topic turn does the same, and the agent message stays in the topic.
func TestFeishuUnifiedTopicTurnAnswersInsideAgentMessage(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	cotFeatures(s, "cot_answer")
	r, _ := topicCotRun(s)
	topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")
	finishTopicCot(t, s, r, &channel.Result{Answer: "结论在这里"})
	creates := s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotCreate)
	if len(creates) != 1 || creates[0]["chat_id"] != testChat || creates[0]["in_thread"] != true {
		t.Fatalf("creation %v", creates)
	}
	if len(*posts) != 0 {
		t.Fatal("a unified topic turn posted a card")
	}
	types, body := appendedBodies(s)
	if !strings.HasSuffix(strings.Join(types, " "), "TEXT_MESSAGE_END RUN_FINISHED") || !strings.Contains(body, "结论在这里") {
		t.Fatalf("events %v", types)
	}
}

// A turn that ends before any progress look still gets its agent message, with the answer inside and no card.
func TestFeishuUnifiedQuickTurnCreatesAtTheEnd(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	cotFeatures(s, "dm_cot", "cot_answer")
	r, _ := cotRun(s)
	finishTopicCot(t, s, r, &channel.Result{Answer: "结论在这里"})
	if creates := s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotCreate); len(creates) != 1 || len(*posts) != 0 {
		t.Fatalf("creations %v posts %v", creates, *posts)
	}
	types, _ := appendedBodies(s)
	if !strings.Contains(strings.Join(types, " "), "TEXT_MESSAGE_CONTENT") || types[len(types)-1] != "RUN_FINISHED" {
		t.Fatalf("events %v", types)
	}
}

// Unified mode holds the card through the grace, then a text-only turn becomes a card turn and stays one.
func TestFeishuUnifiedHoldsCardUntilGrace(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	cotFeatures(s, "dm_cot", "cot_answer")
	s.setLarkTiming("cot_unified_grace", 10*time.Second)
	r, prog := cotRun(s)
	prog.items, prog.steps = nil, nil
	topicCotProgress(t, s, r, time.Second, nil, "")
	if len(*posts) != 0 {
		t.Fatalf("a tick inside the grace posted %v", *posts)
	}
	topicCotProgress(t, s, r, 11*time.Second, nil, "")
	if len(*posts) != 1 || r.Ext["card"] == "" {
		t.Fatalf("past the grace: posts %v ext %v", *posts, r.Ext)
	}
	finishTopicCot(t, s, r, &channel.Result{Answer: "好的"})
	card := lastCard(t, r, *posts)
	if !strings.Contains(card, "好的") {
		t.Fatalf("answer card %s", card)
	}
	types, _ := appendedBodies(s)
	if strings.Contains(strings.Join(types, " "), "TEXT_MESSAGE") {
		t.Fatalf("the answer went into the agent message too: %v", types)
	}
}

// A refused creation posts nothing on the first look and the card, with the process, on the next.
func TestFeishuUnifiedCreationRefusedFallsBackToCards(t *testing.T) {
	s, posts, _ := cotHub(t, true)
	cotFeatures(s, "dm_cot", "cot_answer")
	r, _ := cotRun(s)
	topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")
	if len(*posts) != 0 || r.Ext["cot_off"] == "" {
		t.Fatalf("first look: posts %v ext %v", *posts, r.Ext)
	}
	topicCotProgress(t, s, r, 2*time.Second, []string{"运行 `ls`"}, "")
	if len(*posts) != 1 {
		t.Fatalf("second look posts %v", *posts)
	}
	finishTopicCot(t, s, r, &channel.Result{Answer: "结论在这里"})
	card := lastCard(t, r, *posts)
	if !strings.Contains(card, "结论在这里") || !strings.Contains(card, "collapsible_panel") {
		t.Fatalf("fallback answer %s", card)
	}
	n := 0
	for _, p := range *posts {
		if card, _ := p["card"].(string); strings.Contains(card, "结论在这里") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("answer appeared %d times", n)
	}
}

// A terminal append Feishu refuses closes the agent message as an error and the card carries the answer.
func TestFeishuUnifiedAppendErrorFallsBackToAnswerCard(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	cotFeatures(s, "dm_cot", "cot_answer")
	r, _ := cotRun(s)
	topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")
	s.hub.(*fakeHub).answerApp(AppFeishu, FeishuCotAppend, func(p json.RawMessage) (any, error) {
		if strings.Contains(string(p), "RUN_FINISHED") || strings.Contains(string(p), "TEXT_MESSAGE") {
			return nil, feishuAPIError(12345)
		}
		return map[string]any{}, nil
	})
	finishTopicCot(t, s, r, &channel.Result{Answer: "结论在这里"})
	if reasons := completeReasons(s); len(reasons) != 1 || reasons[0] != string(cotReasonError) {
		t.Fatalf("cot_complete reasons %v", reasons)
	}
	card := postedAnswer(t, *posts, "结论在这里")
	if !strings.Contains(card, "collapsible_panel") {
		t.Fatalf("answer card %s", card)
	}
	if reacts := s.hub.(*fakeHub).appParams(AppFeishu, assistantwire.FeishuReact); len(reacts) == 0 {
		t.Fatal("not marked done")
	}
}

// An append that got no reply is not counted: the card still goes out, and the agent message is closed.
func TestFeishuUnifiedAmbiguousAppendStillDeliversAnswer(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	cotFeatures(s, "dm_cot", "cot_answer")
	r, _ := cotRun(s)
	topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")
	s.hub.(*fakeHub).answerApp(AppFeishu, FeishuCotAppend, func(p json.RawMessage) (any, error) {
		if strings.Contains(string(p), "TEXT_MESSAGE") || strings.Contains(string(p), "RUN_FINISHED") {
			return nil, feishuAPIError(feishuTransport)
		}
		return map[string]any{}, nil
	})
	finishTopicCot(t, s, r, &channel.Result{Answer: "结论在这里"})
	if r.Ext["cot_answer"] != "" {
		t.Fatalf("ambiguous append counted as delivered: %v", r.Ext)
	}
	if len(completeReasons(s)) == 0 {
		t.Fatal("cot_complete was not called")
	}
	postedAnswer(t, *posts, "结论在这里")
}

// An answer that lays out as two cards stays in cards; the agent message ends without the text.
func TestFeishuUnifiedLongAnswerUsesCards(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	cotFeatures(s, "dm_cot", "cot_answer")
	r, _ := cotRun(s)
	topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")
	finishTopicCot(t, s, r, &channel.Result{Answer: strings.Repeat("答", 7000)})
	types, _ := appendedBodies(s)
	joined := strings.Join(types, " ")
	if strings.Contains(joined, "TEXT_MESSAGE") || !strings.HasSuffix(joined, "RUN_FINISHED") {
		t.Fatalf("events %v", types)
	}
	if len(*posts) < 2 {
		t.Fatalf("posts %d, want both cards", len(*posts))
	}
	first, _ := (*posts)[0]["card"].(string)
	if strings.Contains(first, "collapsible_panel") {
		t.Fatal("the first card still has the process panel")
	}
}

// /stop closes the agent message with cot_complete, not a finished run, and the end card says it stopped.
func TestFeishuUnifiedStopClosesAgentMessageWithComplete(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	cotFeatures(s, "dm_cot", "cot_answer")
	r, _ := cotRun(s)
	topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")
	finishTopicCot(t, s, r, &channel.Result{Stopped: "已中断", End: channel.EndStopped})
	types, _ := appendedBodies(s)
	joined := strings.Join(types, " ")
	if !strings.Contains(joined, "REASONING_END") || strings.Contains(joined, "RUN_FINISHED") || strings.Contains(joined, "TEXT_MESSAGE") {
		t.Fatalf("events %v", types)
	}
	if reasons := completeReasons(s); len(reasons) != 1 || reasons[0] != string(cotReasonDone) {
		t.Fatalf("reasons %v", reasons)
	}
	n := 0
	for _, p := range *posts {
		if card, _ := p["card"].(string); strings.Contains(card, "已中断") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("stopped cards %d in %v", n, *posts)
	}
}

// A topic that is not in unified mode closes the same way when it is stopped.
func TestFeishuTopicStopClosesAgentMessageWithComplete(t *testing.T) {
	s, _, _ := cotHub(t, false)
	r, _ := topicCotRun(s)
	topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")
	finishTopicCot(t, s, r, &channel.Result{End: channel.EndStopped})
	types, _ := appendedBodies(s)
	joined := strings.Join(types, " ")
	if strings.Contains(joined, "RUN_FINISHED") || !strings.Contains(joined, "REASONING_END") {
		t.Fatalf("events %v", types)
	}
	if reasons := completeReasons(s); len(reasons) != 1 || reasons[0] != "done" {
		t.Fatalf("reasons %v", reasons)
	}
}

func TestCotEndReason(t *testing.T) {
	for _, tc := range []struct {
		end  channel.End
		want string
	}{
		{channel.EndDone, cotReasonDone},
		{channel.EndStopped, cotReasonDone},
		{channel.EndRecalled, cotReasonDone},
		{channel.EndUnrelated, cotReasonDone},
		{channel.EndError, cotReasonError},
		{channel.EndGaveUp, cotReasonError},
		{channel.EndGone, cotReasonError},
		{channel.EndOffline, cotReasonError},
	} {
		if got := cotEndReason(tc.end); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.end, got, tc.want)
		}
	}
}

// A failed turn closes the agent message as an error.
func TestFeishuErrorEndClosesAgentMessageAsError(t *testing.T) {
	s, _, _ := cotHub(t, false)
	cotFeatures(s, "dm_cot")
	r, _ := cotRun(s)
	topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")
	finishTopicCot(t, s, r, &channel.Result{End: channel.EndError})
	if reasons := completeReasons(s); len(reasons) != 1 || reasons[0] != string(cotReasonError) {
		t.Fatalf("reasons %v", reasons)
	}
}

// Answering again after the answer was confirmed inside the agent message sends nothing more.
func TestFeishuUnifiedAnswerRetryDoesNotRepeat(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	cotFeatures(s, "dm_cot", "cot_answer")
	r, _ := cotRun(s)
	finishTopicCot(t, s, r, &channel.Result{Answer: "结论在这里"})
	before := len(s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotAppend))
	// The run has ended; a retry finds the same worker, already done, and reads the stored outcome.
	s.chans.mu.Lock()
	s.chans.st.Runs = append(s.chans.st.Runs, r)
	s.chans.live[r.ID] = newLive()
	s.chans.mu.Unlock()
	if err := s.sink().answer(t.Context(), r, "结论在这里", "", time.Second, true, false, channel.EndDone); err != nil {
		t.Fatal(err)
	}
	if got := len(s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotAppend)); got != before || len(*posts) != 0 {
		t.Fatalf("retry appended (%d to %d) or posted %v", before, got, *posts)
	}
}

// The answer does not wait forever for a stuck append: the card goes out, and the worker still ends.
func TestFeishuUnifiedAnswerWaitTimeoutSendsCard(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	cotFeatures(s, "dm_cot", "cot_answer")
	s.setLarkTiming("cot_answer_wait", 30*time.Millisecond)
	r, prog := cotRun(s)
	release := make(chan struct{})
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	s.hub.(*fakeHub).answerAppBlocking(AppFeishu, FeishuCotAppend, func(json.RawMessage) (any, error) {
		<-release
		return map[string]any{}, nil
	})
	done := make(chan error, 1)
	go func() {
		done <- s.sink().answer(t.Context(), r, "结论在这里", "", time.Second, true, false, channel.EndDone)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the answer waited on the stuck append")
	}
	if len(*posts) == 0 {
		t.Fatal("no card went out")
	}
	once.Do(func() { close(release) })
	s.chans.mu.Lock()
	w := prog.native()
	s.chans.mu.Unlock()
	if w != nil {
		select {
		case <-w.done:
		case <-time.After(2 * time.Second):
			t.Fatal("the worker did not end")
		}
	}
}

// A held run's next turn gets its own agent message; the first turn's cot_answer does not linger.
func TestFeishuUnifiedHeldRunGetsOwnAgentMessagePerTurn(t *testing.T) {
	s, _, _ := cotHub(t, false)
	cotFeatures(s, "dm_cot", "cot_answer")
	r, prog := cotRun(s)
	finishTopicCot(t, s, r, &channel.Result{Answer: "先答一部分", Held: true})
	for _, k := range []string{"cot", "cot_msg", "cot_done", "cot_answer"} {
		if r.Ext[k] != "" {
			t.Fatalf("%s kept: %v", k, r.Ext)
		}
	}
	prog.items = []proto.Item{{ID: "x", Role: "assistant", Kind: proto.KindThinking, Text: "再想"}}
	topicCotProgress(t, s, r, time.Second, nil, "")
	if creates := s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotCreate); len(creates) != 2 {
		t.Fatalf("creations %d, want a second agent message", len(creates))
	}
}

// postedAnswer is the card that carried the answer: an update of the progress card, or a card of its own when unified
// mode never posted one.
func postedAnswer(t *testing.T, posts []map[string]any, text string) string {
	t.Helper()
	for i := len(posts) - 1; i >= 0; i-- {
		card, _ := posts[i]["card"].(string)
		if strings.Contains(card, text) {
			return card
		}
	}
	t.Fatalf("no card carried %q: %v", text, posts)
	return ""
}
