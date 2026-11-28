package assistantwire

import (
	"encoding/json"
	"testing"
)

func TestStageRuntimeWireRoundTrip(t *testing.T) {
	type record struct {
		Phase TaskPhase      `json:"phase,omitempty"`
		Wait  *TaskWaitState `json:"wait_state,omitempty"`
	}
	for _, tc := range []struct {
		json  string
		valid bool
	}{
		{`{}`, false},
		{`{"phase":"waiting","wait_state":{"waiting_on":"external","wait_target":{"ref":"Publication"},"reason":"Publisher is processing it"}}`, true},
		{`{"phase":"future","wait_state":{"waiting_on":"future","wait_target":{"kind":"future","ref":"Publication"},"reason":"A newer writer recorded this"}}`, false},
	} {
		var rec record
		if err := json.Unmarshal([]byte(tc.json), &rec); err != nil {
			t.Fatal(err)
		}
		if rec.Phase.Valid() != tc.valid || rec.Wait != nil && rec.Wait.WaitingOn.Valid() != tc.valid {
			t.Fatalf("decoded an unknown value as supported: %+v", rec)
		}
		raw, err := json.Marshal(rec)
		if err != nil || string(raw) != tc.json {
			t.Fatalf("round trip = %s, %v, want %s", raw, err, tc.json)
		}
	}
}
