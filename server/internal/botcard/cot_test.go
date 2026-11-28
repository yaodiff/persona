package botcard

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// The answer's text goes in before the run is finished, so Feishu shows it as the run's own text.
func TestCotFinishWithAnswerOrdersTextBeforeRunFinished(t *testing.T) {
	events := CotFinishWithAnswer("c1", "run1", "p-last", "a-1", "the answer")
	var types []string
	var messageID string
	for _, e := range events {
		types = append(types, e.Type)
		var body map[string]string
		_ = json.Unmarshal([]byte(e.Content), &body)
		switch e.Type {
		case "TEXT_MESSAGE_START":
			if body["role"] != "assistant" {
				t.Fatalf("START role = %q", body["role"])
			}
			messageID = body["messageId"]
		case "TEXT_MESSAGE_CONTENT", "TEXT_MESSAGE_END":
			if body["messageId"] != messageID {
				t.Fatalf("%s message id %q, START had %q", e.Type, body["messageId"], messageID)
			}
		case "RUN_FINISHED":
			if body["status"] != "done" || body["threadId"] != "c1" || body["runId"] != "run1" {
				t.Fatalf("RUN_FINISHED = %v", body)
			}
		}
	}
	want := "REASONING_END TEXT_MESSAGE_START TEXT_MESSAGE_CONTENT TEXT_MESSAGE_END RUN_FINISHED"
	if strings.Join(types, " ") != want {
		t.Fatalf("events %v, want %s", types, want)
	}
	if messageID != "a-1" {
		t.Fatalf("message id %q", messageID)
	}
}

// A long answer is cut on rune boundaries, so a CJK character is never split, and an empty one sends nothing.
func TestCotTextSplitsLongAnswersOnRuneBoundaries(t *testing.T) {
	if CotText("a-1", "") != nil {
		t.Fatal("an empty answer still produced events")
	}
	text := strings.Repeat("答", 2*cotTextDelta+1)
	events := CotText("a-1", text)
	var got string
	contents := 0
	for _, e := range events {
		if e.Type != "TEXT_MESSAGE_CONTENT" {
			continue
		}
		var body map[string]string
		_ = json.Unmarshal([]byte(e.Content), &body)
		delta := body["delta"]
		if !utf8.ValidString(delta) || len([]rune(delta)) > cotTextDelta {
			t.Fatalf("delta is not valid UTF-8 within the bound: %d runes, valid %v", len([]rune(delta)), utf8.ValidString(delta))
		}
		got += delta
		contents++
	}
	if contents != 3 || got != text {
		t.Fatalf("%d content events, joined equal %v", contents, got == text)
	}
}

// CotFinish stays what it was: the process ends, then the run, with the same bodies.
func TestCotFinishIsUnchanged(t *testing.T) {
	events := CotFinish("c1", "run1", "p-last")
	if len(events) != 2 || events[0].Type != "REASONING_END" || events[1].Type != "RUN_FINISHED" {
		t.Fatalf("events %v", events)
	}
	var end, finished map[string]string
	_ = json.Unmarshal([]byte(events[0].Content), &end)
	_ = json.Unmarshal([]byte(events[1].Content), &finished)
	if end["messageId"] != "p-last" {
		t.Fatalf("REASONING_END = %v", end)
	}
	if finished["threadId"] != "c1" || finished["runId"] != "run1" || finished["status"] != "done" || len(finished) != 3 {
		t.Fatalf("RUN_FINISHED = %v", finished)
	}
}
