package db

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNodeRuntimeAssistantIsPersistedAndLatched(t *testing.T) {
	var state NodeSession
	if err := json.Unmarshal([]byte(`{"launcher":"agy","cwd":"/work/project","runtime_assistant":true}`), &state); err != nil {
		t.Fatal(err)
	}
	d, ctx := open(t), context.Background()
	if err := d.PutNodeSession(ctx, "machine", "assistant", state); err != nil {
		t.Fatal(err)
	}
	// An older node omits the role on a subsequent state.put.
	legacy := NodeSession{Launcher: state.Launcher, Cwd: state.Cwd}
	if err := d.PutNodeSession(ctx, "machine", "assistant", legacy); err != nil {
		t.Fatal(err)
	}
	if err := d.PutNodeSession(ctx, "machine", "agent", legacy); err != nil {
		t.Fatal(err)
	}
	sessions, err := d.NodeSessions(ctx, "machine")
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"assistant": true, "agent": false} {
		session, ok := sessions[id]
		if !ok {
			t.Fatalf("state.load omitted %s", id)
		}
		raw, err := json.Marshal(session)
		if err != nil {
			t.Fatal(err)
		}
		var restored struct {
			RuntimeAssistant bool `json:"runtime_assistant"`
		}
		if err := json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		if restored.RuntimeAssistant != want {
			t.Errorf("state.load for %s = %s, want runtime_assistant %t", id, raw, want)
		}
	}
}
