package larksyncwire

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMessageEventItemTypeJSONRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 26, 11, 0, 0, 0, time.UTC)
	ev := MessagesEvent{
		Binding: "primary~feishu:tenant_example:ou_owner",
		Page:    1,
		Pages:   1,
		Total:   3,
		Messages: []MessageEventItem{
			{
				ChatID:       "oc_chat_1",
				MessageID:    "om_msg_1",
				SenderOpenID: "ou_sender_1",
				At:           at,
				Type:         MessageTypeText,
			},
			{
				ChatID:    "oc_chat_1",
				MessageID: "om_msg_2",
				At:        at.Add(time.Second),
				Type:      MessageType("sticker"),
			},
			{
				ChatID:    "oc_chat_1",
				MessageID: "om_msg_3",
				At:        at.Add(2 * time.Second),
			},
		},
	}

	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal MessagesEvent: %v", err)
	}
	wire := string(raw)
	for _, want := range []string{`"type":"text"`, `"type":"sticker"`} {
		if !strings.Contains(wire, want) {
			t.Fatalf("marshaled JSON missing %s: %s", want, wire)
		}
	}

	var decodedRaw struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(raw, &decodedRaw); err != nil {
		t.Fatalf("unmarshal raw map: %v", err)
	}
	if len(decodedRaw.Messages) != 3 {
		t.Fatalf("decodedRaw messages = %d, want 3", len(decodedRaw.Messages))
	}
	if _, hasType := decodedRaw.Messages[2]["type"]; hasType {
		t.Fatalf("empty MessageEventItem.Type should be omitted by omitempty: %v", decodedRaw.Messages[2])
	}

	var roundTrip MessagesEvent
	if err := json.Unmarshal(raw, &roundTrip); err != nil {
		t.Fatalf("unmarshal MessagesEvent: %v", err)
	}
	if roundTrip.Messages[0].Type != MessageTypeText {
		t.Fatalf("roundTrip[0].Type = %q, want %q", roundTrip.Messages[0].Type, MessageTypeText)
	}
	if roundTrip.Messages[1].Type != MessageType("sticker") {
		t.Fatalf("roundTrip[1].Type = %q, want %q", roundTrip.Messages[1].Type, "sticker")
	}
	if roundTrip.Messages[2].Type != "" {
		t.Fatalf("roundTrip[2].Type = %q, want empty", roundTrip.Messages[2].Type)
	}
}

func TestMessageTypeConstants(t *testing.T) {
	want := map[MessageType]string{
		MessageTypeText:        "text",
		MessageTypePost:        "post",
		MessageTypeImage:       "image",
		MessageTypeFile:        "file",
		MessageTypeMedia:       "media",
		MessageTypeAudio:       "audio",
		MessageTypeInteractive: "interactive",
		MessageTypeSystem:      "system",
	}
	for got, expected := range want {
		if string(got) != expected {
			t.Fatalf("constant %q != %q", got, expected)
		}
	}
}
