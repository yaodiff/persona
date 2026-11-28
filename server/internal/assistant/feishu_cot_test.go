package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// feishuAPIError is Feishu's error code as the hub puts it on the bus.
func feishuAPIError(code int) error {
	data, _ := json.Marshal(map[string]int{"code": code})
	return &rpc.Error{Code: CodeFeishuAPI, Message: fmt.Sprintf("feishu: code %d", code), Data: data}
}

// cotHub is the Feishu fake with agent messages: refused when refuse is set.
func cotHub(t *testing.T, refuse bool) (*Service, *[]map[string]any, *[]string) {
	f, posts := feishuHub("ou_a", time.Unix(100, 0))
	var calls []string
	f.answerApp(AppFeishu, FeishuCotCreate, func(json.RawMessage) (any, error) {
		calls = append(calls, "create")
		if refuse {
			return nil, feishuAPIError(99991672)
		}
		return map[string]any{"cot_id": "cot1", "message_id": "om_cot"}, nil
	})
	f.answerApp(AppFeishu, FeishuCotAppend, func(p json.RawMessage) (any, error) {
		var m struct {
			Events []botcard.CotEvent `json:"events"`
		}
		_ = json.Unmarshal(p, &m)
		for _, e := range m.Events {
			calls = append(calls, e.Type)
		}
		return map[string]any{}, nil
	})
	f.answerApp(AppFeishu, FeishuCotComplete, func(json.RawMessage) (any, error) {
		calls = append(calls, "complete")
		return map[string]any{}, nil
	})
	f.answerApp(AppFeishu, FeishuReact, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	f.answerApp(AppFeishu, FeishuUnreact, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	s := newTestService(t, f)
	t.Cleanup(func() {
		s.chans.mu.Lock()
		var states []any
		for _, p := range s.chans.live {
			states = append(states, p.sink)
		}
		s.chans.mu.Unlock()
		for _, st := range states {
			if a, ok := st.(interface{ Abort() }); ok {
				a.Abort()
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := s.larkModule().Wait(ctx); err != nil {
			t.Error("native worker did not stop during cleanup")
		}
	})
	return s, posts, &calls
}

func cotRun(s *Service) (*channel.Run, *chanLive) {
	r := &channel.Run{ID: "fb_m1", Channel: channel.Feishu, MachineID: "server", SessionID: "c1", ReplyTo: "om_m1", Label: "Persona", Started: time.Now()}
	prog := &chanLive{mu: newChanMutex(), checking: newChanMutex(), done: make(chan struct{}), items: []proto.Item{
		{ID: "a", Role: "assistant", Kind: proto.KindThinking, Text: "想一想"},
		{ID: "b", Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tu1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)},
		{ID: "c", Role: "user", Kind: proto.KindToolResult, ToolUseID: "tu1"},
	}, steps: []string{"运行 `ls`"}}
	s.chans.mu.Lock()
	s.loadChannelStateLocked(context.Background())
	s.chans.st.Runs = append(s.chans.st.Runs, r)
	s.chans.live = map[string]*chanLive{r.ID: prog}
	s.chans.mu.Unlock()
	return r, prog
}

// withCot gives r an agent message, as a run made before new turns went to the progress card has.
func withCot(s *Service, r *channel.Run) {
	s.setRunExt(r, "cot", "cot1")
	s.setRunExt(r, "cot_msg", "om_cot")
}

// A new turn shows in a progress card, not an agent message: the answer goes into the card with the process folded.
func TestFeishuNewTurnIsAProgressCard(t *testing.T) {
	needsLarkLayer(t)
	s, posts, calls := cotHub(t, false)
	r, _ := cotRun(s)
	sink := feishuSink{s}
	sink.progress(context.Background(), r, time.Second, []string{"运行 `ls`"}, "")
	if len(*calls) != 0 || len(*posts) != 1 || r.Ext["card"] == "" || r.Ext["cot"] != "" {
		t.Fatalf("progress: posts %v, calls %v, ext %v", *posts, *calls, r.Ext)
	}
	s.finishChannelRun(r, &channel.Result{Answer: "结论在这里"})
	last := (*posts)[len(*posts)-1]
	card, _ := last["card"].(string)
	if len(*calls) != 0 || last["update"] != true || !strings.Contains(card, "collapsible_panel") || !strings.Contains(card, "已调用 1 次工具") || !strings.Contains(card, "结论在这里") {
		t.Fatalf("answer card %v (calls %v)", last, *calls)
	}
}

// A run that already had an agent message (from before) still adds to it and ends it; the answer is a card below.
func TestFeishuExistingAgentMessageStillFinishes(t *testing.T) {
	needsLarkLayer(t)
	s, posts, calls := cotHub(t, false)
	r, _ := cotRun(s)
	withCot(s, r)
	sink := feishuSink{s}
	sink.progress(context.Background(), r, time.Second, nil, "")
	if len(*posts) != 0 || len(*calls) == 0 || (*calls)[0] != "RUN_STARTED" {
		t.Fatalf("progress: posts %v, calls %v", *posts, *calls)
	}
	s.finishChannelRun(r, &channel.Result{Answer: "结论在这里"})
	got := strings.Join(*calls, " ")
	if !strings.HasSuffix(got, "REASONING_END RUN_FINISHED") || strings.Contains(got, "create") || strings.Contains(got, "complete") {
		t.Fatalf("calls %s", got)
	}
	if len(*posts) != 1 {
		t.Fatalf("posts %v", *posts)
	}
	card, _ := (*posts)[0]["card"].(string)
	if !strings.Contains(card, "结论在这里") || strings.Contains(card, "collapsible_panel") || (*posts)[0]["reply_to"] != "om_m1" {
		t.Fatalf("answer card %v", (*posts)[0])
	}
}

// A turn with nothing but its answer gets no agent message.
func TestFeishuPlainAnswerHasNoAgentMessage(t *testing.T) {
	needsLarkLayer(t)
	s, posts, calls := cotHub(t, false)
	r, prog := cotRun(s)
	prog.items, prog.steps = nil, nil
	s.finishChannelRun(r, &channel.Result{Answer: "好的"})
	if len(*calls) != 0 || len(*posts) != 1 || strings.Contains((*posts)[0]["card"].(string), "collapsible_panel") {
		t.Fatalf("calls %v, posts %v", *calls, *posts)
	}
}

// An append that did not reach the hub is made again from the same items: once, not twice.
func TestCotAppendTriedAgainSendsItOnce(t *testing.T) {
	needsLarkLayer(t)
	s, _, calls := cotHub(t, false)
	r, _ := cotRun(s)
	withCot(s, r)
	sink := feishuSink{s}
	s.hub.(*fakeHub).answerApp(AppFeishu, FeishuCotAppend, func(json.RawMessage) (any, error) { return nil, rpc.ErrClosed })
	sink.progress(context.Background(), r, time.Second, nil, "")
	var got []string
	s.hub.(*fakeHub).answerApp(AppFeishu, FeishuCotAppend, func(p json.RawMessage) (any, error) {
		var m struct {
			Events []botcard.CotEvent `json:"events"`
		}
		_ = json.Unmarshal(p, &m)
		for _, e := range m.Events {
			got = append(got, e.Type)
		}
		return map[string]any{}, nil
	})
	sink.progress(context.Background(), r, time.Second, nil, "")
	if strings.Count(strings.Join(got, " "), "RUN_STARTED") != 1 || strings.Count(strings.Join(got, " "), "TOOL_CALL_RESULT") != 1 || strings.Count(strings.Join(*calls, " "), "create") != 0 {
		t.Fatalf("again: %v (calls %v)", got, *calls)
	}
}

// A held run's next turn drops the agent message it had and shows in a progress card.
func TestHeldRunsNextTurnIsAProgressCard(t *testing.T) {
	needsLarkLayer(t)
	s, posts, calls := cotHub(t, false)
	r, prog := cotRun(s)
	withCot(s, r)
	feishuSink{s}.progress(context.Background(), r, time.Second, nil, "")
	s.setRunExt(r, "cot_answer", "1")
	s.finishChannelRun(r, &channel.Result{Answer: "先答一部分", Held: true})
	for _, k := range []string{"cot", "cot_msg", "cot_para", "cot_done", "cot_answer"} {
		if r.Ext[k] != "" {
			t.Fatalf("%s kept: %v", k, r.Ext)
		}
	}
	*calls, *posts = nil, nil
	prog.items = []proto.Item{{ID: "x", Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tu9", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"pwd"}`)}}
	feishuSink{s}.progress(context.Background(), r, time.Second, []string{"运行 `pwd`"}, "")
	if len(*calls) != 0 || len(*posts) != 1 {
		t.Fatalf("next turn: calls %v, posts %v", *calls, *posts)
	}
}

// A turn that starts with a tool call hangs it under a paragraph that says so (botmux's shape: a whole paragraph
// before the call), not "Working…".
func TestCotFirstToolCallHasItsParagraph(t *testing.T) {
	needsLarkLayer(t)
	s, _, _ := cotHub(t, false)
	r, prog := cotRun(s)
	withCot(s, r)
	prog.items = prog.items[1:] // straight into the tool call
	var events []botcard.CotEvent
	s.hub.(*fakeHub).answerApp(AppFeishu, FeishuCotAppend, func(p json.RawMessage) (any, error) {
		var m struct {
			Events []botcard.CotEvent `json:"events"`
		}
		_ = json.Unmarshal(p, &m)
		events = append(events, m.Events...)
		return map[string]any{}, nil
	})
	feishuSink{s}.progress(context.Background(), r, time.Second, nil, "")
	s.finishChannelRun(r, &channel.Result{Answer: "结论"})
	var seq []string
	all := ""
	for _, e := range events {
		seq = append(seq, e.Type)
		all += e.Content
	}
	want := "RUN_STARTED REASONING_START REASONING_MESSAGE_START REASONING_MESSAGE_CONTENT REASONING_MESSAGE_END TOOL_CALL_START TOOL_CALL_END TOOL_CALL_RESULT REASONING_END RUN_FINISHED"
	if strings.Join(seq, " ") != want || strings.Contains(all, "处理中") || !strings.Contains(all, "调用工具") {
		t.Fatalf("events %v\n%s", seq, all)
	}
}

// The card comes a second after the message, before the conversation is seen working (queued, starting), and says
// so; then its elapsed time moves every few seconds without new steps. A held run gets none until it works again.
func TestFeishuCardFromTheStartAndAlwaysMoving(t *testing.T) {
	needsLarkLayer(t)
	s, posts, _ := cotHub(t, false)
	r, prog := cotRun(s)
	prog.items, prog.steps = nil, nil
	now := time.Unix(1000, 0)
	r.Started = now
	s.chans.now = func() time.Time { return now }
	s.channelProgressTick(r.ID)
	if len(*posts) != 0 {
		t.Fatalf("at once: %v", *posts)
	}
	now = now.Add(time.Second) // the lark module's progress_after
	s.channelProgressTick(r.ID)
	if len(*posts) != 1 || !strings.Contains((*posts)[0]["card"].(string), "准备中") {
		t.Fatalf("first card: %v", *posts)
	}
	now = now.Add(2 * time.Second) // progress_idle less a second
	s.channelProgressTick(r.ID)
	if len(*posts) != 1 {
		t.Fatalf("too soon: %v", *posts)
	}
	s.chans.mu.Lock()
	r.Seen = true // the conversation works, but on another turn: this message waits behind it
	s.chans.mu.Unlock()
	now = now.Add(time.Second)
	s.channelProgressTick(r.ID)
	if len(*posts) != 2 || (*posts)[1]["update"] != true || !strings.Contains((*posts)[1]["card"].(string), "排队中</text_tag> <font color='grey'>Persona · 4 秒") {
		t.Fatalf("queued: %v", *posts)
	}
	// Its own message shows in the conversation: from there on it works, with only its own steps.
	s.chans.mu.Lock()
	r.Probe = "我的问题"
	noteRunItems("", r, prog, []proto.Item{
		{Seq: 1, ID: "x", Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"earlier turn"}`)},
		{Seq: 2, ID: "m", Role: "user", Kind: proto.KindText, Text: "我的问题"},
		{Seq: 3, ID: "y", Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"mine"}`)},
	}, now)
	s.chans.mu.Unlock()
	now = now.Add(3 * time.Second) // progress_idle
	s.channelProgressTick(r.ID)
	if card := (*posts)[len(*posts)-1]["card"].(string); !strings.Contains(card, "处理中</text_tag> <font color='grey'>Persona · 7 秒") || !strings.Contains(card, "mine") || strings.Contains(card, "earlier turn") {
		t.Fatalf("working: %v", *posts)
	}
	// A held run waiting on its background work (not working): no new card.
	*posts = nil
	s.chans.mu.Lock()
	r.Held = true
	delete(r.Ext, "card")
	s.chans.mu.Unlock()
	now = now.Add(time.Minute)
	s.channelProgressTick(r.ID)
	if len(*posts) != 0 {
		t.Fatalf("held run: %v", *posts)
	}
}

// A turn that stopped without words (an API error) tells the chat why, not just that there is no answer.
func TestFeishuEmptyTurnSaysWhyItStopped(t *testing.T) {
	needsLarkLayer(t)
	s, posts, _ := cotHub(t, false)
	r, _ := cotRun(s)
	s.finishChannelRun(r, &channel.Result{Stopped: "API 错误：overloaded"})
	if len(*posts) != 1 {
		t.Fatalf("posts %v", *posts)
	}
	card, _ := (*posts)[0]["card"].(string)
	if !strings.Contains(card, "这一轮没有文字回答就停了") || !strings.Contains(card, "overloaded") {
		t.Fatalf("answer card %s", card)
	}
}

// The progress card is not sent again when it is the same; after Feishu's rate limit updates wait (5 s, then
// longer) and the next due one sends the latest; a withdrawn card is forgotten and the next tick posts a new one.
func TestFeishuProgressCardThrottled(t *testing.T) {
	needsLarkLayer(t)
	s, posts, _ := cotHub(t, false)
	r, _ := cotRun(s)
	now := time.Unix(1000, 0)
	s.chans.now = func() time.Time { return now }
	sink := feishuSink{s}
	ctx := context.Background()
	steps := []string{"运行 `ls`"}
	sink.progress(ctx, r, 4*time.Second, steps, "")
	sink.progress(ctx, r, 4*time.Second, steps, "")
	if len(*posts) != 1 {
		t.Fatalf("same card sent again: %v", *posts)
	}
	f := s.hub.(*fakeHub)
	var reply error
	f.answerApp(AppFeishu, FeishuUpdateCard, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		m["update"] = true
		*posts = append(*posts, m)
		return map[string]any{}, reply
	})
	reply = &botcard.APIError{Op: "update card", Code: 230020, Msg: "This operation triggers the frequency limit."}
	sink.progress(ctx, r, 5*time.Second, steps, "")
	if len(*posts) != 2 {
		t.Fatalf("limited update not tried: %v", *posts)
	}
	reply = nil
	now = now.Add(4 * time.Second)
	sink.progress(ctx, r, 9*time.Second, steps, "")
	if len(*posts) != 2 {
		t.Fatalf("sent while backing off: %v", *posts)
	}
	now = now.Add(time.Second)
	sink.progress(ctx, r, 10*time.Second, steps, "")
	if len(*posts) != 3 || !strings.Contains((*posts)[2]["card"].(string), "10 秒") {
		t.Fatalf("latest after the backoff: %v", *posts)
	}
	reply = &botcard.APIError{Op: "update card", Code: 230011, Msg: "The message is recalled."}
	sink.progress(ctx, r, 11*time.Second, steps, "")
	if r.Ext["card"] != "" {
		t.Fatalf("withdrawn card kept: %v", r.Ext)
	}
	sink.progress(ctx, r, 11*time.Second, steps, "")
	if len(*posts) != 5 || (*posts)[4]["update"] == true || r.Ext["card"] != "om_card" {
		t.Fatalf("no new card: %v", *posts)
	}
}

// An append of more than 50 events that fails part way goes on from the chunk that failed; nothing is sent twice.
func TestCotAppendGoesOnFromTheFailedChunk(t *testing.T) {
	needsLarkLayer(t)
	s, _, _ := cotHub(t, false)
	r, prog := cotRun(s)
	withCot(s, r)
	var items []proto.Item
	for i := 0; i < 30; i++ {
		id := fmt.Sprint("tu", i)
		items = append(items, proto.Item{ID: "u" + id, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: id, ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)},
			proto.Item{ID: "r" + id, Role: "user", Kind: proto.KindToolResult, ToolUseID: id, Text: "ok"})
	}
	prog.items = items
	var got []botcard.CotEvent
	n := 0
	s.hub.(*fakeHub).answerApp(AppFeishu, FeishuCotAppend, func(p json.RawMessage) (any, error) {
		n++
		if n == 2 {
			return nil, feishuAPIError(99991400)
		}
		var m struct {
			Events []botcard.CotEvent `json:"events"`
		}
		_ = json.Unmarshal(p, &m)
		got = append(got, m.Events...)
		return map[string]any{}, nil
	})
	sink := feishuSink{s}
	sink.progress(context.Background(), r, time.Second, nil, "")
	sink.progress(context.Background(), r, 4*time.Second, nil, "")
	seen := map[string]int{}
	for _, e := range got {
		seen[e.Type+e.Content]++
	}
	for k, c := range seen {
		if c > 1 && !strings.HasPrefix(k, "TOOL_CALL_END") {
			t.Fatalf("sent %d times: %s", c, k)
		}
	}
	if len(got) != 2+3+30*3 { // run + section, the placeholder paragraph, start/end/result of each call
		t.Fatalf("%d events", len(got))
	}
}

// The end of the turn failing for now (a rate limit, no answer from Feishu): the answer still goes out and the agent
// message is ended, not left spinning.
func TestCotEndFailingForNowStillEndsIt(t *testing.T) {
	needsLarkLayer(t)
	for _, code := range []int{99991400, feishuTransport} {
		s, posts, calls := cotHub(t, false)
		r, _ := cotRun(s)
		withCot(s, r)
		feishuSink{s}.progress(context.Background(), r, time.Second, nil, "")
		s.hub.(*fakeHub).answerApp(AppFeishu, FeishuCotAppend, func(json.RawMessage) (any, error) { return nil, feishuAPIError(code) })
		s.finishChannelRun(r, &channel.Result{Answer: "结论"})
		if got := strings.Join(*calls, " "); !strings.HasSuffix(got, "complete") {
			t.Fatalf("code %d: not ended: %s", code, got)
		}
		if len(*posts) != 1 || r.Ext["cot_off"] != "" {
			t.Fatalf("code %d: answer %v, ext %v", code, *posts, r.Ext)
		}
	}
}

// An append that went out and got no reply (it may have been applied) is not sent again: the rest goes on after it,
// and the end is made sure of with cot_complete.
func TestCotAppendWithoutReplyIsNotSentAgain(t *testing.T) {
	needsLarkLayer(t)
	for _, fail := range []error{rpc.ErrDropped, feishuAPIError(feishuTransport)} {
		s, posts, calls := cotHub(t, false)
		r, _ := cotRun(s)
		withCot(s, r)
		n := 0
		s.hub.(*fakeHub).answerApp(AppFeishu, FeishuCotAppend, func(p json.RawMessage) (any, error) {
			n++
			var m struct {
				Events []botcard.CotEvent `json:"events"`
			}
			_ = json.Unmarshal(p, &m)
			for _, e := range m.Events {
				*calls = append(*calls, e.Type)
			}
			if n == 1 {
				return nil, fail
			}
			return map[string]any{}, nil
		})
		feishuSink{s}.progress(context.Background(), r, time.Second, nil, "")
		s.finishChannelRun(r, &channel.Result{Answer: "结论"})
		got := strings.Join(*calls, " ")
		if strings.Count(got, "RUN_STARTED") != 1 || strings.Count(got, "TOOL_CALL_START") != 1 {
			t.Fatalf("%v: sent again: %s", fail, got)
		}
		if !strings.HasSuffix(got, "REASONING_END RUN_FINISHED") || len(*posts) != 1 || r.Ext["cot_off"] != "" {
			t.Fatalf("%v: end %s, posts %v, ext %v", fail, got, *posts, r.Ext)
		}
	}
}

// The end of the turn failing, then cot_complete failing for a moment too: it is tried again, and only counts as ended
// once it went through.
func TestCotEndIsTriedAgain(t *testing.T) {
	needsLarkLayer(t)
	for _, failure := range []error{feishuAPIError(99991400), rpc.NotSent(context.DeadlineExceeded), errors.Join(rpc.ErrNotWritten, context.Canceled)} {
		t.Run(failure.Error(), func(t *testing.T) {
			s, posts, calls := cotHub(t, false)
			s.setLarkTimingList("cot_end_tries", []time.Duration{0, 0, 0})
			r, _ := cotRun(s)
			withCot(s, r)
			feishuSink{s}.progress(context.Background(), r, time.Second, nil, "")
			s.hub.(*fakeHub).answerApp(AppFeishu, FeishuCotAppend, func(json.RawMessage) (any, error) { return nil, feishuAPIError(99991400) })
			n := 0
			s.hub.(*fakeHub).answerApp(AppFeishu, FeishuCotComplete, func(json.RawMessage) (any, error) {
				n++
				if n < 3 {
					return nil, failure
				}
				*calls = append(*calls, "complete")
				return map[string]any{}, nil
			})
			s.finishChannelRun(r, &channel.Result{Answer: "结论"})
			if n != 3 || !strings.HasSuffix(strings.Join(*calls, " "), "complete") || len(*posts) != 1 {
				t.Fatalf("tries %d, calls %v, posts %v", n, *calls, *posts)
			}
		})
	}
}

// feishuTransport is the code the hub gives a Feishu call that got no answer from Feishu (a timeout, a dropped
// connection, a reply that is not Feishu's JSON).
const feishuTransport = -1
