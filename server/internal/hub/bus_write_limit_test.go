package hub

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Oversized outgoing bus requests, events, and handler replies on the hub side must fail locally
// without closing the manager WebSocket or taking its managed machines offline.
func TestHubBusWriteLimitKeepsManagerAndMachinesOnline(t *testing.T) {
	const limit = 4096
	const secretMarker = "HUB_SECRET_BUS_PAYLOAD_DO_NOT_LOG"

	h, srv := bodyHub(t)
	h.bus.maxMessage = limit
	logs := &syncBuffer{}
	h.logger.SetOutput(logs)

	tok := busToken(t, h, busAssistant, "Assistant")
	f := startService(t, srv, tok, busHello, map[string]any{"id": busAssistant})
	if err := f.c.Wait(ctxTimeout(t)); err != nil {
		t.Fatal(err)
	}
	var hello busHelloReply
	f.hello(t, &hello)

	connBefore := h.busConnOf(busAssistant)
	if connBefore == nil {
		t.Fatal("expected assistant bus connection")
	}

	// Report a managed machine online through the manager's bus connection.
	if err := f.c.Notify(devicewire.EventState, devicewire.States{
		Machines: []devicewire.State{{
			Machine:     proto.Machine{ID: "box", Name: "Box", Online: true},
			Connected:   true,
			ConnectedAt: time.Now().UTC(),
		}},
	}); err != nil {
		t.Fatal(err)
	}
	machineOnline := func() bool {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/machines", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body struct {
			Machines []proto.Machine `json:"machines"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return false
		}
		for _, m := range body.Machines {
			if m.ID == "box" && m.Online {
				return true
			}
		}
		return false
	}
	eventually(t, 5*time.Second, "managed machine box online", machineOnline)

	ctx := ctxTimeout(t)

	// 1. Oversized hub -> manager request fails locally without marking the bus offline.
	err := h.callBus(ctx, busAssistant, "echo", map[string]string{
		"secret": secretMarker,
		"pad":    strings.Repeat("a", limit),
	}, nil)
	if !errors.Is(err, rpc.ErrMessageTooLarge) || errors.Is(err, errBusOffline) {
		t.Fatalf("oversized callBus err = %v, want rpc.ErrMessageTooLarge (not errBusOffline)", err)
	}

	// 2. Oversized hub -> manager event fails locally.
	err = h.notifyBus(busAssistant, "hub.big_event", map[string]string{
		"secret": secretMarker,
		"pad":    strings.Repeat("b", limit),
	})
	if !errors.Is(err, rpc.ErrMessageTooLarge) {
		t.Fatalf("oversized notifyBus err = %v, want rpc.ErrMessageTooLarge", err)
	}

	// 3. Oversized hub -> manager handler reply (test.echo wraps params in {"from":"assistant","params":...},
	// adding 10 bytes over the request line, so a request just under limit fits inbound ReadLimit while its
	// reply exceeds the hub's WriteLimit) returns a bounded *rpc.Error without disconnecting the manager.
	replyTriggerPad := limit - 103
	err = f.c.Call(ctx, "test.echo", map[string]string{
		"secret": secretMarker,
		"pad":    strings.Repeat("c", replyTriggerPad),
	}, nil)
	var re *rpc.Error
	if !errors.As(err, &re) {
		t.Fatalf("oversized hub reply err = %T %v, want *rpc.Error", err, err)
	}
	if !strings.Contains(re.Message, "test.echo") || !strings.Contains(re.Message, "bytes") || strings.Contains(re.Message, secretMarker) {
		t.Fatalf("unexpected oversized hub reply error: %q", re.Message)
	}

	// The exact same manager connection must remain up, normal RPCs both ways must work,
	// and the managed machine must stay online.
	if got := h.busConnOf(busAssistant); got != connBefore {
		t.Fatalf("assistant bus connection replaced/dropped (%p -> %p)", connBefore, got)
	}
	if f.count.Load() != 1 {
		t.Fatalf("manager reconnected: hellos = %d, want 1", f.count.Load())
	}
	var out struct {
		Got map[string]any `json:"got"`
	}
	if err := h.callBus(ctx, busAssistant, "echo", map[string]any{"ok": true}, &out); err != nil || out.Got["ok"] != true {
		t.Fatalf("hub->manager echo after rejections failed: out=%+v err=%v", out, err)
	}
	var pong map[string]any
	if err := f.c.Call(ctx, "ping", nil, &pong); err != nil {
		t.Fatalf("manager->hub ping after rejections failed: %v", err)
	}
	if !machineOnline() {
		t.Fatal("managed machine box went offline after oversized bus operations")
	}

	// Diagnostics must identify each rejected operation and size without leaking payload contents
	// or logging a bus disconnect.
	for _, want := range []string{
		"bus: [assistant] rejected outgoing request echo:",
		"bus: [assistant] rejected outgoing event hub.big_event:",
		"bus: [assistant] rejected outgoing reply test.echo:",
	} {
		if !logs.WaitFor(want, 5*time.Second) {
			t.Errorf("missing expected diagnostic %q in logs:\n%s", want, logs.String())
		}
	}
	allLogs := logs.String()
	if strings.Contains(allLogs, secretMarker) {
		t.Fatalf("hub logs leaked payload content:\n%s", allLogs)
	}
	if strings.Contains(allLogs, "bus: [assistant] disconnected") || strings.Contains(allLogs, "the manager is gone") {
		t.Fatalf("hub unexpectedly disconnected manager:\n%s", allLogs)
	}
}
