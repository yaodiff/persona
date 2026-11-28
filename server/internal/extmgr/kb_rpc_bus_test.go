package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestExtensionKBRPCBusWire(t *testing.T) {
	h := newTestManager(t)
	spec := kbExtensionSpec(t, h, "example", extension.Permissions{Hub: []string{extension.ScopeKBRead, extension.ScopeKBWrite}})
	for _, tc := range []struct {
		method, assistantMethod, params, reply string
	}{
		{"kb.list", "kb.list", `{"limit":3,"kind":"projects"}`, `{"items":[],"tags":[],"total":0,"future_field":true}`},
		{"kb.recent", "kb.recent", `{"since":"2026-01-02T00:00:00Z"}`, `{"items":[],"total":0,"future_field":true}`},
		{"kb.get", "kb.get", `{"kind":"projects","id":"example","recent_chars":0}`, `{"detail":null,"future_field":true}`},
		{"kb.search", "kb.search", `{"query":"example","messages":false}`, `{"hits":[],"future_field":true}`},
		{"kb.update", "kb.extension_update", `{"kind":"projects","id":"example","summary":"Updated"}`, `{"id":"example","future_field":true}`},
		{"kb.put_recording", "kb.extension_put_recording",
			`{"id":"55555555-5555-4555-8555-555555555555","started_at":"2026-01-02T00:00:00Z","minutes":{"markdown":"Example"}}`,
			`{"id":"55555555-5555-4555-8555-555555555555","revision":1,"created":true,"future_field":true}`},
	} {
		t.Run(tc.method, func(t *testing.T) {
			wantParams := tc.params
			if tc.method != tc.assistantMethod {
				wantParams = `{"extension":"example","params":` + tc.params + `}`
			}
			var calls int
			h.opts.Assistant = func(_ context.Context, method string, raw json.RawMessage) (json.RawMessage, error) {
				calls++
				if method != tc.assistantMethod || string(raw) != wantParams {
					t.Fatalf("assistant call = %s %s, want %s %s", method, raw, tc.assistantMethod, wantParams)
				}
				return json.RawMessage(tc.reply), nil
			}
			out, err := spec.Handle(t.Context(), tc.method, json.RawMessage(tc.params))
			if reply, ok := out.(json.RawMessage); err != nil || !ok || string(reply) != tc.reply || calls != 1 {
				t.Fatalf("reply = %s (%T), error = %v, calls = %d", reply, out, err, calls)
			}
		})
	}
}

func TestExtensionKBRPCBusDayFallbackStripsSentOrigin(t *testing.T) {
	h := newTestManager(t)
	spec := kbExtensionSpec(t, h, "curator", extension.Permissions{Hub: []string{extension.ScopeKBRead, extension.ScopeSentRead}})
	const params = `{"day":"2026-01-02","id":"example-chat"}`
	var calls []string
	h.opts.Assistant = func(_ context.Context, method string, raw json.RawMessage) (json.RawMessage, error) {
		calls = append(calls, method)
		if string(raw) != params {
			t.Fatalf("%s params = %s, want %s", method, raw, params)
		}
		if method == "kb.extension_day" {
			return nil, &rpc.Error{Code: extension.CodeUnknownMethod, Message: "unknown method"}
		}
		return json.RawMessage(kbMarkedDay), nil
	}
	out, err := spec.Handle(t.Context(), "kb.day", json.RawMessage(params))
	day, ok := out.(assistantwire.KBDayResult)
	if err != nil || !ok || day.SentOrigin != "unavailable" || !slices.Equal(calls, []string{"kb.extension_day", "kb.day"}) {
		t.Fatalf("fallback = %+v, error = %v, calls = %v", out, err, calls)
	}
	if len(day.Messages) != 2 || day.Messages[0].Text != "20:00 me: assistant words" {
		t.Fatalf("fallback lost archived messages: %+v", day.Messages)
	}
	for _, message := range day.Messages {
		if message.SentBy != "" || message.SentLedgerID != 0 || message.RecalledAt != nil {
			t.Fatalf("fallback exposed sent provenance: %+v", message)
		}
	}
}

func TestExtensionKBRPCBusPreservesStrictErrors(t *testing.T) {
	h := newTestManager(t)
	spec := kbExtensionSpec(t, h, "curator", extension.Permissions{Hub: []string{extension.ScopeKBRead, extension.ScopeKBWrite, extension.ScopeSentRead}})
	for _, tc := range []struct {
		method, assistantMethod, params, forwarded, message string
	}{
		{"kb.day", "kb.extension_day", `{"id":"example-chat","day":"2026-01-02","unexpected":true}`,
			`{"id":"example-chat","day":"2026-01-02","unexpected":true}`, `kb.day: json: unknown field "unexpected"`},
		{"kb.update", "kb.extension_update", `{"kind":"projects","id":"example","summmary":"Misspelled"}`,
			`{"extension":"curator","params":{"kind":"projects","id":"example","summmary":"Misspelled"}}`, `kb.update: json: unknown field "summmary"`},
	} {
		t.Run(tc.method, func(t *testing.T) {
			h.opts.Assistant = func(_ context.Context, method string, raw json.RawMessage) (json.RawMessage, error) {
				if method != tc.assistantMethod || string(raw) != tc.forwarded {
					t.Fatalf("assistant call = %s %s, want %s %s", method, raw, tc.assistantMethod, tc.forwarded)
				}
				return nil, &rpc.Error{Code: bus.CodeInvalidParams, Message: tc.message}
			}
			out, err := spec.Handle(t.Context(), tc.method, json.RawMessage(tc.params))
			var re *rpc.Error
			if out != nil || !errors.As(err, &re) || re.Code != bus.CodeInvalidParams || re.Message != tc.message {
				t.Fatalf("reply = %v, error = %v, want invalid_params: %s", out, err, tc.message)
			}
		})
	}
}

func TestExtensionKBRPCBusAssistantOffline(t *testing.T) {
	h := newTestManager(t)
	spec := kbExtensionSpec(t, h, "curator", extension.Permissions{Hub: []string{extension.ScopeKBRead, extension.ScopeKBWrite, extension.ScopeSentRead}})
	h.opts.Assistant = nil
	for _, method := range []string{"kb.list", "kb.day", "kb.update", "kb.put_recording"} {
		out, err := spec.Handle(t.Context(), method, json.RawMessage(`{}`))
		var re *rpc.Error
		if out != nil || !errors.As(err, &re) || re.Code != assistantwire.CodeAssistantOffline || re.Message != errAssistantOffline.Error() {
			t.Errorf("%s: reply = %v, error = %v, want %s", method, out, err, assistantwire.CodeAssistantOffline)
		}
	}
}
