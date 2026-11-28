package node

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestCodexDesktopAsyncQuestionWithoutPane(t *testing.T) {
	n, id, bus, rollout := appEffortChat(t)
	ctx := context.Background()

	appendLines(t, rollout, cxToday(cxAsyncItemLine))
	row := appChatRow(t, n)
	if row.PaneID != "" || row.Capabilities == nil || !row.Capabilities.Send ||
		!row.Capabilities.AnswerQuestions || row.Capabilities.Approve {
		t.Fatalf("expected a writable App chat with question support only: %+v", row.Capabilities)
	}
	wire, err := json.Marshal(row.Capabilities)
	if err != nil || !strings.Contains(string(wire), `"answer_questions":true`) {
		t.Fatalf("question capability missing from the wire: %s (%v)", wire, err)
	}
	cards := n.codexCardsNow(ctx, "")
	if len(cards) != 1 || len(cards[0].ix.Questions) != 1 || len(cards[0].ix.Questions[0].Options) != 3 {
		t.Fatalf("expected the App question and all of its options, got %+v", cards)
	}
	card := cards[0]
	answer := proto.Resolution{Answers: map[string]string{"Which color should the banner be?": "Blue"}}
	if err := n.codexAnswer(ctx, id, card.ix.ID, answer); err != nil {
		t.Fatal(err)
	}
	select {
	case params := <-bus.turns:
		request := params["turnStart"].(map[string]any)["request"].(map[string]any)
		text := request["input"].([]any)[0].(map[string]any)["text"].(string)
		if !strings.Contains(text, "send_user_message_question_reply") || !strings.Contains(text, "call_a1") {
			t.Fatalf("expected the reply envelope, got %q", text)
		}
	default:
		t.Fatal("the desktop App did not receive the answer")
	}
	if err := n.codexAnswer(ctx, id, card.ix.ID, answer); err == nil {
		t.Fatal("a second submission before the answer is observed was accepted")
	}
	appendLine(t, rollout, cxUserLine("Blue"))
	if cards := n.codexCardsNow(ctx, ""); len(cards) != 0 {
		t.Fatalf("the answered card remains open: %+v", cards)
	}
}

func TestCodexAppHistoryKeepsAsyncQuestionFields(t *testing.T) {
	item := codexRolloutItem("agentMessage", []byte(`{"id":"example-ask","text":"Choose a color","delivery":"async","questions":[{"title":"Choose a color","options":["Blue","Green"]}]}`))
	if item["delivery"] != "async" || item["questions"] == nil {
		t.Fatalf("history conversion lost the structured question: %+v", item)
	}
}
