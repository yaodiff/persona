package hub

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestSessionStartModelAliasesHTTPContract(t *testing.T) {
	h := newTestHub(t)
	connectAssistant(t, h)
	var got map[string]any
	calls := 0
	modelsNode(t, h, MachineConfig{ID: "box"}, func(method string, params map[string]any) (any, error) {
		if method == "session.start" {
			calls++
			got = params
			return map[string]any{"session": proto.Session{SessionID: "aliases-start"}}, nil
		}
		return map[string]any{}, nil
	})
	const body = `{"machine_id":"box","cwd":"/code","launcher":"claude","async":true,"prompt":"x","model_aliases":{"sonnet_model":"vendor/standard"}}`

	m := h.localMachine("box")
	m.mu.Lock()
	m.info.Protocol = nodeproto.Version
	m.mu.Unlock()
	w := modelsRequest(h, "POST", "/api/v1/sessions", body)
	if w.Code != http.StatusConflict || errorCodeOf(w.Body.Bytes()) != proto.CodeNodeOutdated || calls != 0 || got != nil {
		t.Fatalf("old node start = %d %s, calls = %d, params = %v", w.Code, w.Body, calls, got)
	}

	m.mu.Lock()
	m.info.Capabilities = []string{nodeproto.SessionStartModelAliases}
	m.mu.Unlock()
	w = modelsRequest(h, "POST", "/api/v1/sessions", body)
	if w.Code != http.StatusOK {
		t.Fatalf("start = %d %s", w.Code, w.Body)
	}
	aliases, _ := got["model_aliases"].(map[string]any)
	if calls != 1 || aliases["sonnet_model"] != "vendor/standard" {
		t.Fatalf("node calls = %d, params = %v; model aliases were not forwarded", calls, got)
	}
}

func TestSessionStartRejectsInvalidModelAliasesBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		aliases map[string]string
	}{
		{"unknown key", map[string]string{"tiny_model": "vendor/tiny"}},
		{"native alias", map[string]string{"sonnet_model": "sonnet"}},
		{"empty value", map[string]string{"haiku_model": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			connectAssistant(t, h)
			calls := 0
			modelsNode(t, h, MachineConfig{ID: "box"}, func(method string, _ map[string]any) (any, error) {
				calls++
				return map[string]any{}, nil
			})
			body, _ := json.Marshal(map[string]any{
				"machine_id": "box", "cwd": "/code", "launcher": "claude", "prompt": "x", "model_aliases": tc.aliases,
			})
			w := modelsRequest(h, "POST", "/api/v1/sessions", string(body))
			if w.Code != http.StatusBadRequest || calls != 0 || !strings.Contains(w.Body.String(), "model_aliases") {
				t.Fatalf("start = %d %s, node calls = %d; want 400 before dispatch", w.Code, w.Body, calls)
			}
		})
	}
}
