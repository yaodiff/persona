package extmgr

import (
	"context"
	"encoding/json"
	"testing"
)

func TestVolcMeetingTranscriptionChoiceBusWire(t *testing.T) {
	volc := needsVolcModule(t)
	h := newTestManager(t)
	h.extensions = newExtensions(t.TempDir(), nil, t.Logf)
	h.addVolcExtension(t.Context(), h.extensions.reg, nil)
	p := h.extensions.reg.GetExact(volc.ExtensionID)
	if p == nil {
		t.Fatal("built-in extension is missing")
	}

	const (
		device = `{"extension":"","default":"device"}`
		hub    = `{"extension":"volc/volc","default":"hub"}`
	)
	calls := []struct{ params, reply string }{
		{`{}`, device},
		{`{"set":true,"use":true,"extension":"volc/volc"}`, hub},
		{`{}`, hub},
		{`{"set":true,"extension":"volc/volc"}`, device},
		{`{}`, device},
	}
	h.opts.Assistant = func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		if len(calls) == 0 {
			t.Fatalf("unexpected assistant call: %s %s", method, params)
		}
		want := calls[0]
		calls = calls[1:]
		if method != "meeting.transcription_choice" || string(params) != want.params {
			t.Fatalf("assistant call = %s %s, want meeting.transcription_choice %s", method, params, want.params)
		}
		return json.RawMessage(want.reply), nil
	}

	for _, raw := range []json.RawMessage{json.RawMessage(`{"meetings":true}`), json.RawMessage(`{"meetings":false}`)} {
		rest, changed, err := h.volcConfigWrite(t.Context(), p, raw)
		if err != nil || changed || string(rest) != "{}" {
			t.Fatalf("save %s: settings = %s, changed = %v, error = %v", raw, rest, changed, err)
		}
	}
	if on, known := h.volcMeetings(t.Context()); on || !known {
		t.Fatalf("after turning off: on = %v, known = %v", on, known)
	}
	if len(calls) != 0 {
		t.Fatalf("missing assistant calls: %+v", calls)
	}
}
