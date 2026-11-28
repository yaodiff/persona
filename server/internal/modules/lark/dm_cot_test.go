package lark

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// appendedTypes are the event types sent to the agent message, in order.
func appendedTypes(s *Service) []string {
	var types []string
	for _, p := range s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotAppend) {
		raw, _ := json.Marshal(p["events"])
		var batch []struct {
			Type string `json:"event_type"`
		}
		_ = json.Unmarshal(raw, &batch)
		for _, e := range batch {
			types = append(types, e.Type)
		}
	}
	return types
}

// With both switches off, a private run stays a card: no agent message, the process folded into the answer.
func TestFeishuPrivateRunKeepsCardOnlyByDefault(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	cotFeatures(s) // topic_cot on, dm_cot off: the default
	r, _ := cotRun(s)
	topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")
	if creates := s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotCreate); len(creates) != 0 || len(*posts) != 1 {
		t.Fatalf("default private run: creations %v, posts %v", creates, *posts)
	}
	finishTopicCot(t, s, r, &channel.Result{Answer: "结论在这里"})
	card := lastCard(t, r, *posts)
	if !strings.Contains(card, "结论在这里") || !strings.Contains(card, "collapsible_panel") {
		t.Fatalf("default private answer = %s", card)
	}
}

// dm_cot opens an agent message under the owner's message, with no chat and no thread, and the progress card stays.
func TestFeishuPrivateRunOpensAgentMessageWithDMCot(t *testing.T) {
	s, posts, _ := cotHub(t, false)
	cotFeatures(s, "dm_cot")
	r, _ := cotRun(s)
	topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")
	creates := s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotCreate)
	if len(creates) != 1 {
		t.Fatalf("creations %v", creates)
	}
	got := creates[0]
	if got["reply_to"] != "om_m1" || got["chat_id"] != nil || got["in_thread"] != nil {
		t.Fatalf("creation = %v, want reply_to om_m1 and no chat or thread", got)
	}
	if len(*posts) != 1 {
		t.Fatalf("progress posts = %v, want the card beside the agent message", *posts)
	}
	finishTopicCot(t, s, r, &channel.Result{Answer: "结论在这里"})
	card := lastCard(t, r, *posts)
	if !strings.Contains(card, "结论在这里") || strings.Contains(card, "collapsible_panel") {
		t.Fatalf("answer card = %s", card)
	}
	types := appendedTypes(s)
	if types[len(types)-1] != "RUN_FINISHED" || strings.Contains(strings.Join(types, " "), "TEXT_MESSAGE") {
		t.Fatalf("events %v", types)
	}
}

// A turn with nothing but text gets no agent message, with or without unified mode, and answers in one card.
func TestFeishuPrivateTextOnlyTurnCreatesNoAgentMessage(t *testing.T) {
	for _, on := range [][]string{{"dm_cot"}, {"dm_cot", "cot_answer"}} {
		t.Run(strings.Join(on, "+"), func(t *testing.T) {
			s, posts, _ := cotHub(t, false)
			cotFeatures(s, on...)
			r, prog := cotRun(s)
			prog.items, prog.steps = []proto.Item{{ID: "a", Role: "assistant", Kind: proto.KindText, Text: "好的"}}, nil
			topicCotProgress(t, s, r, time.Second, nil, "")
			finishTopicCot(t, s, r, &channel.Result{Answer: "好的"})
			if creates := s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotCreate); len(creates) != 0 {
				t.Fatalf("a text-only turn created an agent message: %v", creates)
			}
			card := postedAnswer(t, *posts, "好的")
			if strings.Contains(card, "collapsible_panel") {
				t.Fatalf("answer card = %s", card)
			}
		})
	}
}

// A refused creation marks the run and leaves the card, process panel included, to carry the answer.
func TestFeishuPrivateAgentMessageRefusalFallsBack(t *testing.T) {
	s, posts, _ := cotHub(t, true)
	cotFeatures(s, "dm_cot")
	r, _ := cotRun(s)
	topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")
	if r.Ext["cot_off"] == "" || len(*posts) != 1 {
		t.Fatalf("refusal not recorded: ext %v, posts %d", r.Ext, len(*posts))
	}
	finishTopicCot(t, s, r, &channel.Result{Answer: "结论在这里"})
	card := lastCard(t, r, *posts)
	if !strings.Contains(card, "结论在这里") || !strings.Contains(card, "collapsible_panel") {
		t.Fatalf("fallback answer = %s", card)
	}
}

// A /read run never gets an agent message, whichever switches are on: its process stays out of the group.
func TestFeishuReadRunNeverGetsAgentMessage(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run("direct="+boolName(direct), func(t *testing.T) {
			s, _, _ := cotHub(t, false)
			cotFeatures(s, "dm_cot", "cot_answer")
			r, _ := cotRun(s)
			s.setRunExt(r, readChatKey, testChat)
			if direct {
				s.setRunExt(r, readDirectKey, "1")
			}
			topicCotProgress(t, s, r, time.Second, []string{"运行 `ls`"}, "")
			finishTopicCot(t, s, r, &channel.Result{Answer: "结论在这里"})
			if creates := s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotCreate); len(creates) != 0 {
				t.Fatalf("a /read run created an agent message: %v", creates)
			}
		})
	}
}

func boolName(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// Switching dm_cot off mid-turn keeps the run on its worker: the next events are appended once, not started again.
func TestFeishuPrivateRouteStaysWithWorkerWhenSwitchedOff(t *testing.T) {
	s, _, _ := cotHub(t, false)
	cotFeatures(s, "dm_cot")
	r, prog := cotRun(s)
	prog.items = prog.items[:1] // thinking only: the agent message opens
	topicCotProgress(t, s, r, time.Second, nil, "")
	if creates := s.hub.(*fakeHub).appParams(AppFeishu, FeishuCotCreate); len(creates) != 1 {
		t.Fatalf("creations %v", creates)
	}
	cotFeatures(s) // dm_cot off now
	prog.items = append(prog.items, proto.Item{ID: "b", Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tu1", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"ls"}`)})
	topicCotProgress(t, s, r, 2*time.Second, []string{"运行 `ls`"}, "")
	types := appendedTypes(s)
	if strings.Count(strings.Join(types, " "), "RUN_STARTED") != 1 || !strings.Contains(strings.Join(types, " "), "TOOL_CALL_START") {
		t.Fatalf("events after the switch went off: %v", types)
	}
}

// A private run that already had an agent message, with dm_cot off, still appends to it and posts no progress card.
func TestFeishuLegacyPrivateAgentMessageUnchanged(t *testing.T) {
	s, posts, calls := cotHub(t, false)
	r, _ := cotRun(s)
	withCot(s, r)
	topicCotProgress(t, s, r, time.Second, nil, "")
	if len(*posts) != 0 || len(*calls) == 0 || (*calls)[0] != "RUN_STARTED" {
		t.Fatalf("progress: posts %v, calls %v", *posts, *calls)
	}
	finishTopicCot(t, s, r, &channel.Result{Answer: "结论在这里"})
	if len(*posts) != 1 || strings.Contains((*posts)[0]["card"].(string), "collapsible_panel") {
		t.Fatalf("answer posts %v", *posts)
	}
	if got := strings.Join(*calls, " "); !strings.HasSuffix(got, "REASONING_END RUN_FINISHED") || strings.Contains(got, "create") {
		t.Fatalf("calls %s", got)
	}
}
