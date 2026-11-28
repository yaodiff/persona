package assistantwire_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
)

func TestAssistantBusMethodsPreserveWireJSON(t *testing.T) {
	t.Run("outbox", func(t *testing.T) {
		checkAssistantBusWire(t, assistantwire.OutboxMethod, "manager.outbox",
			`{"items":[{"id":"item-1","at":"2026-01-02T03:04:05Z","message":{"type":"todos"}}]}`,
			`{"done":["item-1"]}`)
	})
	t.Run("call ring", func(t *testing.T) {
		checkAssistantBusWire(t, assistantwire.CallRingMethod, "call.ring",
			`{"call":{"id":"call-1","title":"Reminder","conversation_id":"conv-1","attempt":1}}`,
			`{"devices":0,"reason":"no_phone"}`)
	})
	t.Run("project review owner input", func(t *testing.T) {
		checkAssistantBusWire(t, assistantwire.ProjectReviewOwnerInputMethod, "project_review.owner_input",
			`{"machine_id":"box","session_id":"s1","message_id":"msg-1","at":"2026-01-02T03:04:05Z","unscoped":true}`,
			`{}`)
	})
	t.Run("todo handoff", func(t *testing.T) {
		checkAssistantBusWire(t, assistantwire.TodoHandoffMethod, "assistant.todo_handoff",
			`{"todo_id":"todo-1","text":"Prepare the release","new":true,"machine_id":"box","agent":"codex"}`,
			`{"found":true,"title":"Release","text":"Prepare the release"}`)
	})
	t.Run("todo link", func(t *testing.T) {
		checkAssistantBusWire(t, assistantwire.TodoLinkMethod, "assistant.todo_link",
			`{"todo_id":"todo-1","machine_id":"box","session_id":"s1","prompt":"Prepare the release"}`,
			`{"title":"Release"}`)
	})
}

type legacyBusCaller func(context.Context, string, any, any) error

func (f legacyBusCaller) Call(ctx context.Context, method string, params, out any) error {
	return f(ctx, method, params, out)
}

func checkAssistantBusWire[P, R any](t *testing.T, method buswire.Method[P, R], name, request, reply string) {
	t.Helper()
	withFutureField := func(object string) []byte {
		return []byte(strings.TrimSuffix(object, "}") + `,"future_field":true}`)
	}
	var params P
	if err := json.Unmarshal(withFutureField(request), &params); err != nil {
		t.Fatal(err)
	}
	legacy := legacyBusCaller(func(_ context.Context, gotName string, gotParams, out any) error {
		raw, err := json.Marshal(gotParams)
		if gotName != name || err != nil || string(raw) != request {
			t.Fatalf("call = %s %s (%v), want %s %s", gotName, raw, err, name, request)
		}
		if reply == `{}` {
			if out != nil {
				t.Fatalf("empty acknowledgment has decode destination %T", out)
			}
			return nil
		}
		return json.Unmarshal(withFutureField(reply), out)
	})
	result, err := buswire.Call(t.Context(), legacy, method, params)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := json.Marshal(result); err != nil || string(raw) != reply {
		t.Fatalf("reply = %s (%v), want %s", raw, err, reply)
	}
}
