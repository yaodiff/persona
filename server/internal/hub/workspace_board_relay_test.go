package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/rpc"
)

// boardNode is a machine of the hub's whose node answers with fn, connected over a pipe.
func boardNode(t *testing.T, h *Hub, id string, fn rpc.Handler) *Machine {
	t.Helper()
	m := newMachine(h, MachineConfig{ID: id, Name: id})
	registerMachine(h, m)
	hi, no := io.Pipe()
	ni, ho := io.Pipe()
	hp, np := rpc.New("h", ho), rpc.New("n", no)
	np.OnRequest = fn
	ctx, cancel := context.WithCancel(context.Background())
	go hp.Serve(ctx, hi)
	go np.Serve(ctx, ni)
	t.Cleanup(func() { cancel(); hp.Close(io.EOF); np.Close(io.EOF); hi.Close(); ni.Close() })
	m.peer, m.online = hp, true
	return m
}

// The workspace board is the manager's (assistant/workspace_board.go): the hub only relays its routes, 503
// assistant_offline while the manager is away, and what the manager answers once it is back.
func TestWorkspaceBoardRoutesAreRelayed(t *testing.T) {
	h := newTestHub(t)
	routes := [][2]string{
		{"GET", "/api/v1/workspace-board"},
		{"GET", "/api/v1/workspace-board/activity"},
		{"GET", "/api/v1/workspace-board/activity/1"},
		{"POST", "/api/v1/workspace-board"},
		{"GET", "/api/v1/workspaces"},
		{"GET", "/api/v1/workspace-board/settings"},
		{"PUT", "/api/v1/workspace-board/settings"},
	}
	for _, r := range routes {
		w := hubCall(h, r[0], r[1], `{}`)
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "assistant_offline") {
			t.Fatalf("%s %s without the manager: %d %s", r[0], r[1], w.Code, w.Body)
		}
	}
	connectAssistant(t, h)
	// The manager's own answer comes back unchanged: its test store keeps no board.
	w := hubCall(h, "GET", "/api/v1/workspace-board", "")
	var body struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusServiceUnavailable || body.Code != "not_supported" {
		t.Fatalf("GET board with the manager: %d %s", w.Code, w.Body)
	}
}
