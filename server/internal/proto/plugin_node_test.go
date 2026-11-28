package proto

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestPluginNodeHubCallParamsJSON(t *testing.T) {
	in := PluginNodeHubCall{
		Revision: 7,
		ID:       PluginNodeExtensionID("example-helper"),
		Method:   "kb.put_recording",
		Params:   PluginNodeHubCallParams(`{"id":"55555555-5555-4555-8555-555555555555"}`),
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"params":{"id":"55555555-5555-4555-8555-555555555555"}`) {
		t.Fatalf("expected raw JSON params, got %s", raw)
	}
	var out PluginNodeHubCall
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Revision != in.Revision || out.ID != in.ID || out.Method != in.Method || !bytes.Equal(out.Params, in.Params) {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", out, in)
	}

	omitted, err := json.Marshal(PluginNodeHubCall{ID: "example-helper", Method: "kb.owner"})
	if err != nil {
		t.Fatalf("marshal omitted: %v", err)
	}
	if strings.Contains(string(omitted), `"params"`) {
		t.Fatalf("expected empty params to be omitted, got %s", omitted)
	}
	emptyOmitted, err := json.Marshal(PluginNodeHubCall{ID: "example-helper", Method: "kb.owner", Params: PluginNodeHubCallParams{}})
	if err != nil {
		t.Fatalf("marshal empty slice: %v", err)
	}
	if strings.Contains(string(emptyOmitted), `"params"`) {
		t.Fatalf("expected zero-length params to be omitted, got %s", emptyOmitted)
	}
}
