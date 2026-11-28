package hub

import (
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

// The public HTTP start keeps the shared speed field intact for the node. A
// malformed value is rejected before the node can start anything.
func TestSessionStartSpeedHTTPContract(t *testing.T) {
	t.Run("fast reaches session start", func(t *testing.T) {
		h := newTestHub(t)
		connectAssistant(t, h)
		var got map[string]any
		calls := 0
		modelsNode(t, h, MachineConfig{ID: "box"}, func(method string, params map[string]any) (any, error) {
			if method == "session.start" {
				calls++
				got = params
				return map[string]any{"session": proto.Session{SessionID: "speed-start", Starting: true}}, nil
			}
			return map[string]any{}, nil
		})
		w := modelsRequest(h, "POST", "/api/v1/sessions", `{"machine_id":"box","cwd":"/code","launcher":"claude","async":true,"speed":"fast"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("start = %d %s", w.Code, w.Body)
		}
		if calls != 1 || got["speed"] != "fast" {
			t.Fatalf("node calls = %d, params = %v; speed was dropped", calls, got)
		}
	})

	t.Run("non-string speed is invalid before dispatch", func(t *testing.T) {
		h := newTestHub(t)
		connectAssistant(t, h)
		calls := 0
		modelsNode(t, h, MachineConfig{ID: "box"}, func(method string, _ map[string]any) (any, error) {
			calls++
			return map[string]any{}, nil
		})
		w := modelsRequest(h, "POST", "/api/v1/sessions", `{"machine_id":"box","cwd":"/code","launcher":"claude","async":true,"speed":true}`)
		if w.Code != http.StatusBadRequest || calls != 0 {
			t.Fatalf("start = %d %s, node calls = %d; want 400 before dispatch", w.Code, w.Body, calls)
		}
	})
}

func TestSessionStartServiceTierHTTPContract(t *testing.T) {
	h := newTestHub(t)
	connectAssistant(t, h)
	var got map[string]any
	modelsNode(t, h, MachineConfig{ID: "box"}, func(method string, params map[string]any) (any, error) {
		if method == "session.start" {
			got = params
			return map[string]any{"session": proto.Session{SessionID: "speed-start", ServiceTier: "ultrafast"}}, nil
		}
		return map[string]any{}, nil
	})
	const body = `{"machine_id":"box","cwd":"/code","launcher":"codex","agent_type":"codex","service_tier":"ultrafast"}`
	m := h.localMachine("box")
	m.mu.Lock()
	m.info.Protocol = nodeproto.Version
	m.info.Capabilities = []string{nodeproto.SessionStartSpeed}
	m.mu.Unlock()
	w := modelsRequest(h, "POST", "/api/v1/sessions", body)
	if w.Code != http.StatusConflict || errorCodeOf(w.Body.Bytes()) != proto.CodeNodeOutdated || got != nil {
		t.Fatalf("old node start = %d %s, node params = %v", w.Code, w.Body, got)
	}
	m.mu.Lock()
	m.info.Capabilities = append(m.info.Capabilities, nodeproto.SessionStartServiceTier)
	m.mu.Unlock()
	w = modelsRequest(h, "POST", "/api/v1/sessions", body)
	if w.Code != http.StatusOK || got["service_tier"] != "ultrafast" {
		t.Fatalf("start = %d %s, node params = %v", w.Code, w.Body, got)
	}
}
