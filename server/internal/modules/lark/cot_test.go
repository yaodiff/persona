package lark

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// The words a turn says before a tool call go into the process; the words it ends with (its answer) do not.
func TestCotEventsHoldTheAnswerBack(t *testing.T) {
	s := newTestService(t, newFakeHub())
	var p cotProgress
	start := func(first string) []botcard.CotEvent { return botcard.CotStart("c1", "cot1", first) }
	items := []proto.Item{
		{ID: "a", Role: "assistant", Kind: proto.KindText, Text: "我先查一下"},
		{ID: "b", Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tu1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)},
		{ID: "c", Role: "user", Kind: proto.KindToolResult, ToolUseID: "tu1", Text: "a.txt"},
		{ID: "d", Role: "assistant", Kind: proto.KindText, Text: "最终结论"},
	}
	ev, para := s.sink().s.cotEvents("", &p, items, "", start, false)
	types := []string{}
	all := ""
	for _, e := range ev {
		types = append(types, e.Type)
		all += e.Content
	}
	want := "RUN_STARTED REASONING_START REASONING_MESSAGE_START REASONING_MESSAGE_CONTENT REASONING_MESSAGE_END TOOL_CALL_START TOOL_CALL_END TOOL_CALL_RESULT"
	if strings.Join(types, " ") != want {
		t.Fatalf("events %v", types)
	}
	if para != "p-a" || !strings.Contains(all, "我先查一下") || strings.Contains(all, "最终结论") || !strings.Contains(all, `"parentMessageId":"p-a"`) || !strings.Contains(all, "运行 · ls") {
		t.Fatalf("para %q, events %s", para, all)
	}
	// More comes: the held words go in once a call follows them, and the run is not started again.
	more := []proto.Item{{ID: "e", Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tu2", ToolName: "Read", ToolInput: json.RawMessage(`{"file_path":"/x/y.go"}`)}}
	ev, para = s.sink().s.cotEvents("", &p, more, para, start, false)
	if para != "p-d" || ev[0].Type != "REASONING_MESSAGE_START" || !strings.Contains(ev[1].Content, "最终结论") {
		t.Fatalf("second batch %v (para %q)", ev, para)
	}
	// A result whose call it did not see is left out.
	if ev, _ := s.sink().s.cotEvents("", &p, []proto.Item{{Kind: proto.KindToolResult, ToolUseID: "other"}}, para, start, false); len(ev) != 0 {
		t.Fatalf("stray result %v", ev)
	}
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
		var workers []*cotWorker
		for _, p := range s.chans.live {
			if p.native() != nil {
				workers = append(workers, p.native())
			}
		}
		s.chans.mu.Unlock()
		for _, w := range workers {
			w.abort()
		}
		for _, w := range workers {
			select {
			case <-w.done:
			case <-time.After(2 * time.Second):
				t.Error("native worker did not stop during cleanup")
			}
		}
	})
	return s, posts, &calls
}

func cotRun(s *Service) (*channel.Run, *chanLive) {
	r := &channel.Run{ID: "fb_m1", Channel: channel.Feishu, MachineID: "server", SessionID: "c1", ReplyTo: "om_m1", Label: "Persona", Started: time.Now()}
	prog := newLive()
	prog.items, prog.steps = []proto.Item{
		{ID: "a", Role: "assistant", Kind: proto.KindThinking, Text: "想一想"},
		{ID: "b", Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tu1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)},
		{ID: "c", Role: "user", Kind: proto.KindToolResult, ToolUseID: "tu1"},
	}, []string{"运行 `ls`"}
	s.chans.mu.Lock()
	s.chans.st.Runs = append(s.chans.st.Runs, r)
	s.chans.live = map[string]*chanLive{r.ID: prog}
	s.chans.mu.Unlock()
	return r, prog
}

// cotFeatures answers the bot's status with the usual switches plus the given ids on (dm_cot, cot_answer).
func cotFeatures(s *Service, on ...string) {
	extra := map[string]bool{}
	for _, id := range on {
		extra[id] = true
	}
	s.hub.(*fakeHub).answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0),
			Features: FeishuFeatures{Main: true, Cards: true, TopicCot: true, DMCot: extra["dm_cot"], CotAnswer: extra["cot_answer"]}}, nil
	})
	s.sink().s.feishuForgetStatus() // a status read moments ago would hide the switch for three seconds
}

// withCot gives r an agent message, as a run made before new turns went to the progress card has.
func withCot(s *Service, r *channel.Run) {
	s.setRunExt(r, "cot", "cot1")
	s.setRunExt(r, "cot_msg", "om_cot")
}

// After a restart mid-turn the calls already shown get their output; one whose output never came is settled at the end.
func TestCotCallsShownBeforeARestartAreSettled(t *testing.T) {
	s, _, _ := cotHub(t, false)
	r, prog := cotRun(s)
	withCot(s, r)
	prog.items = prog.items[:2] // thinking, a call without its output yet
	var events []string
	s.hub.(*fakeHub).answerApp(AppFeishu, FeishuCotAppend, func(p json.RawMessage) (any, error) {
		var m struct {
			Events []botcard.CotEvent `json:"events"`
		}
		_ = json.Unmarshal(p, &m)
		for _, e := range m.Events {
			events = append(events, e.Type)
		}
		return map[string]any{}, nil
	})
	s.sink().progress(context.Background(), r, time.Second, nil, "")
	if r.Ext["cot_calls"] != "tu1" {
		t.Fatalf("open calls %q", r.Ext["cot_calls"])
	}
	prog.sink.(*runLive).cot = cotProgress{} // the assistant restarted: only Ext is left
	prog.items = []proto.Item{{ID: "c", Role: "user", Kind: proto.KindToolResult, ToolUseID: "tu1", Text: "a.txt"},
		{ID: "d", Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tu2", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"pwd"}`)}}
	events = nil
	s.finishChannelRun(r, &channel.Result{Answer: "结论"})
	if got := strings.Join(events, " "); got != "TOOL_CALL_RESULT TOOL_CALL_START TOOL_CALL_END TOOL_CALL_RESULT REASONING_END RUN_FINISHED" {
		t.Fatalf("after the restart: %s", got)
	}
}

// Words, then thinking, then a tool call: the process shows them in the conversation's order (the words are not
// held back behind the thinking that came after them); thinking after the turn's last words goes in as it ends.
func TestCotThinkingWaitsForTheWordsBeforeIt(t *testing.T) {
	s := newTestService(t, newFakeHub())
	var p cotProgress
	start := func(first string) []botcard.CotEvent { return botcard.CotStart("c1", "cot1", first) }
	paras := func(ev []botcard.CotEvent) string {
		var out []string
		for _, e := range ev {
			if e.Type == "REASONING_MESSAGE_CONTENT" {
				var m struct {
					Delta string `json:"delta"`
				}
				_ = json.Unmarshal([]byte(e.Content), &m)
				out = append(out, m.Delta)
			}
		}
		return strings.Join(out, "|")
	}
	ev, para := s.sink().s.cotEvents("", &p, []proto.Item{
		{ID: "a", Role: "assistant", Kind: proto.KindText, Text: "我先查一下"},
		{ID: "b", Role: "assistant", Kind: proto.KindThinking, Text: "想一想"},
	}, "", start, false)
	if len(ev) != 0 {
		t.Fatalf("shown before a call follows: %s", paras(ev))
	}
	ev, para = s.sink().s.cotEvents("", &p, []proto.Item{
		{ID: "c", Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tu1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)},
		{ID: "d", Role: "assistant", Kind: proto.KindText, Text: "结论"},
		{ID: "e", Role: "assistant", Kind: proto.KindThinking, Text: "再核对"},
	}, para, start, false)
	if got := paras(ev); got != "我先查一下|想一想" || para != "p-b" {
		t.Fatalf("order %q (para %q)", got, para)
	}
	ev, _ = s.sink().s.cotEvents("", &p, nil, para, start, true)
	if got := paras(ev); got != "再核对" {
		t.Fatalf("at the end %q", got)
	}
}
