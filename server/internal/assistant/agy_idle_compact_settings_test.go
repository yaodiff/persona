package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgyIdleCompactSettingsDefaultsAndPut(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	ctx := context.Background()
	if !s.agyIdleAutoCompact(ctx) || s.agyIdleAutoCompactMinutes(ctx) != 10 || s.agyIdleAutoCompactTokens(ctx) != 200000 {
		t.Fatalf("defaults: enabled=%v minutes=%d tokens=%d", s.agyIdleAutoCompact(ctx), s.agyIdleAutoCompactMinutes(ctx), s.agyIdleAutoCompactTokens(ctx))
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant", nil))
	var view map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || view["agy_idle_auto_compact"] != true || view["agy_idle_auto_compact_minutes"] != float64(10) || view["agy_idle_auto_compact_tokens"] != float64(200000) {
		t.Fatalf("effective defaults in API view: %d %+v", rec.Code, view)
	}

	put := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRecorder()
		s.ServeHTTP(r, httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(body)))
		return r
	}
	for _, body := range []string{
		`{"agy_idle_auto_compact":"false"}`,
		`{"agy_idle_auto_compact_minutes":0}`, `{"agy_idle_auto_compact_minutes":1441}`,
		`{"agy_idle_auto_compact_tokens":9999}`, `{"agy_idle_auto_compact_tokens":10000001}`,
	} {
		if r := put(body); r.Code != http.StatusBadRequest {
			t.Fatalf("invalid PUT %s: %d %s", body, r.Code, r.Body.String())
		}
	}
	if r := put(`{"agy_idle_auto_compact":false,"agy_idle_auto_compact_minutes":15,"agy_idle_auto_compact_tokens":250000}`); r.Code != http.StatusOK {
		t.Fatalf("valid PUT: %d %s", r.Code, r.Body.String())
	}
	if s.agyIdleAutoCompact(ctx) || s.agyIdleAutoCompactMinutes(ctx) != 15 || s.agyIdleAutoCompactTokens(ctx) != 250000 {
		t.Fatal("PUT did not persist AGY idle compaction settings")
	}
	if r := put(`{"notes":"keep agy settings"}`); r.Code != http.StatusOK || s.agyIdleAutoCompact(ctx) || s.agyIdleAutoCompactMinutes(ctx) != 15 || s.agyIdleAutoCompactTokens(ctx) != 250000 {
		t.Fatal("partial PUT reset AGY idle compaction settings")
	}
}

func TestAgyIdleCompactSettingsToolAndNodeConfig(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t, newFakeHub().withMachine())
	tool := func(args string) (string, bool) {
		r := s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(args), MachineID: "server", SessionID: "c1"})
		return r.Content[0].Text, r.IsError
	}
	for _, c := range []struct{ key, want string }{
		{"assistant.agy_idle_auto_compact", "true"},
		{"assistant.agy_idle_auto_compact_minutes", "10"},
		{"assistant.agy_idle_auto_compact_tokens", "200000"},
	} {
		if got, isErr := tool(fmt.Sprintf(`{"action":"get","key":%q}`, c.key)); isErr || got != c.want {
			t.Fatalf("%s default: got %q err=%v want %q", c.key, got, isErr, c.want)
		}
	}
	for _, c := range []struct{ key, value string }{
		{"assistant.agy_idle_auto_compact", "null"},
		{"assistant.agy_idle_auto_compact_minutes", "0"},
		{"assistant.agy_idle_auto_compact_tokens", "9999"},
	} {
		if out, isErr := tool(fmt.Sprintf(`{"action":"set","key":%q,"value":%s}`, c.key, c.value)); !isErr || strings.TrimSpace(out) == "" {
			t.Fatalf("invalid %s=%s: %q error=%v", c.key, c.value, out, isErr)
		}
	}

	// Verify a new connection gets node.configure with agy_idle_compaction and pending_interaction_sessions.
	s.fleet.mu.Lock()
	if s.fleet.interactions == nil {
		s.fleet.interactions = map[string]*pending{}
	}
	s.fleet.interactions["ix-agy-1"] = &pending{ix: proto.Interaction{ID: "ix-agy-1", MachineID: "server", SessionID: "agy-11111111-2222-3333-4444-555555555555"}}
	s.fleet.mu.Unlock()

	calls := s.nodeConnectCalls(ctx, "server")
	var found bool
	for _, call := range calls {
		if call.Method != "node.configure" {
			continue
		}
		var payload struct {
			Policy  *assistantwire.AgyIdleCompaction `json:"agy_idle_compaction"`
			Pending []string                         `json:"pending_interaction_sessions"`
		}
		if err := json.Unmarshal(call.Params, &payload); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(call.Params), `"agy_idle_compaction"`) {
			continue // another setting's node.configure (the BotMux Owner Union ID)
		}
		if payload.Policy == nil || !payload.Policy.Enabled || payload.Policy.IdleMinutes != 10 || payload.Policy.ContextTokens != 200000 {
			t.Fatalf("unexpected agy_idle_compaction payload: %+v", payload.Policy)
		}
		if len(payload.Pending) != 1 || payload.Pending[0] != "agy-11111111-2222-3333-4444-555555555555" {
			t.Fatalf("unexpected pending_interaction_sessions: %v", payload.Pending)
		}
		found = true
	}
	if !found {
		t.Fatalf("nodeConnectCalls did not include node.configure for AGY idle compaction: %+v", calls)
	}
}
