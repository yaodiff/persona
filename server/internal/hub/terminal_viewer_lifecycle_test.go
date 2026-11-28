package hub

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
)

func terminalViewerClient(h *Hub) *client {
	c := &client{hub: h, role: roleFull, out: make(chan []byte, 32), subs: map[[2]string]bool{{"m1", "chat"}: true}}
	h.mu.Lock()
	if h.clients == nil {
		h.clients = map[*client]struct{}{}
	}
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

func terminalViewerReport(h *Hub) []assistantwire.SessionRef {
	h.reported.mu.Lock()
	defer h.reported.mu.Unlock()
	return append([]assistantwire.SessionRef(nil), h.reported.clients.Viewing...)
}

func terminalViewerChanges(h *Hub) <-chan struct{} {
	changed := make(chan struct{}, 32)
	c := rosterConn(h, assistantwire.AssistantID, writerFunc(func(raw []byte) (int, error) {
		var event struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			return 0, err
		}
		if event.Event == assistantwire.EventClients {
			changed <- struct{}{}
		}
		return len(raw), nil
	}))
	c.presence = busPresence{ID: assistantwire.AssistantID, Role: bus.RoleManager}
	return changed
}

func waitTerminalViewerReport(t *testing.T, h *Hub, changed <-chan struct{}, want []assistantwire.SessionRef) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		if got := terminalViewerReport(h); reflect.DeepEqual(got, want) {
			return
		}
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatalf("fleet viewer report = %+v, want %+v", terminalViewerReport(h), want)
		}
	}
}

func TestTerminalWatchFailureKeepsPagesUntilTheyLeave(t *testing.T) {
	h := newTestHub(t)
	changed := terminalViewerChanges(h)
	boardNode(t, h, "m1", func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != "terminal.watch" {
			return map[string]any{}, nil
		}
		var p struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(params, &p)
		if p.SessionID == "failed" {
			return nil, errors.New("watch failed")
		}
		return map[string]any{"control": false}, nil
	})
	c := terminalViewerClient(h)
	h.terminalSubscribe(context.Background(), c, "m1", "survivor", "", 0, nil, false)
	h.terminalSubscribe(context.Background(), c, "m1", "failed", "", 0, nil, false)

	deadline := time.After(3 * time.Second)
	for {
		select {
		case raw := <-c.out:
			var msg struct {
				Type  string `json:"type"`
				Error string `json:"error"`
			}
			_ = json.Unmarshal(raw, &msg)
			if msg.Type == "terminal" && msg.Error != "" {
				goto ended
			}
		case <-deadline:
			t.Fatal("watch failure did not end the terminal while the WebSocket client remained connected")
		}
	}

ended:
	want := []assistantwire.SessionRef{
		{MachineID: "m1", SessionID: "chat"},
		{MachineID: "m1", SessionID: "failed"},
		{MachineID: "m1", SessionID: "survivor"},
	}
	waitTerminalViewerReport(t, h, changed, want)
	h.terminalUnsubscribe(c, "m1", "failed")
	waitTerminalViewerReport(t, h, changed, []assistantwire.SessionRef{
		{MachineID: "m1", SessionID: "chat"},
		{MachineID: "m1", SessionID: "survivor"},
	})
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.termSubs[[3]string{"m1", "failed", "m1"}] {
		t.Fatal("failed terminal remained in the connected client's viewer subscriptions")
	}
	if !c.termSubs[[3]string{"m1", "survivor", "m1"}] {
		t.Fatal("ending one terminal removed the client's independent terminal subscription")
	}
	if !c.subs[[2]string{"m1", "chat"}] {
		t.Fatal("ending a terminal removed the independent chat subscription")
	}
}

func TestTerminalsEndedKeepsPagesAndTheirIndependentChat(t *testing.T) {
	h := newTestHub(t)
	changed := terminalViewerChanges(h)
	boardNode(t, h, "m1", func(context.Context, string, json.RawMessage) (any, error) {
		return map[string]any{"control": false}, nil
	})
	c := terminalViewerClient(h)
	h.terminalSubscribe(context.Background(), c, "m1", "one", "", 0, nil, false)
	h.terminalSubscribe(context.Background(), c, "m1", "two", "", 0, nil, false)
	h.terminalsEnded("m1", "machine disconnected")

	want := []assistantwire.SessionRef{
		{MachineID: "m1", SessionID: "chat"},
		{MachineID: "m1", SessionID: "one"},
		{MachineID: "m1", SessionID: "two"},
	}
	waitTerminalViewerReport(t, h, changed, want)
	h.terminalDrop(c)
	waitTerminalViewerReport(t, h, changed, []assistantwire.SessionRef{{MachineID: "m1", SessionID: "chat"}})
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.termSubs) != 0 {
		t.Fatalf("machine disconnect left terminal viewer entries: %+v", c.termSubs)
	}
	if !c.subs[[2]string{"m1", "chat"}] {
		t.Fatal("machine disconnect removed the independent chat subscription")
	}
}

func TestTerminalEndedKeepsConnectedPagesUntilTheirLastDeparture(t *testing.T) {
	h := newTestHub(t)
	changed := terminalViewerChanges(h)
	boardNode(t, h, "m1", func(context.Context, string, json.RawMessage) (any, error) {
		return map[string]any{"control": false}, nil
	})
	clients := []*client{terminalViewerClient(h), terminalViewerClient(h)}
	for _, c := range clients {
		h.terminalSubscribe(context.Background(), c, "m1", "ended", "", 0, nil, false)
	}
	waitTerminalViewerReport(t, h, changed, []assistantwire.SessionRef{
		{MachineID: "m1", SessionID: "chat"},
		{MachineID: "m1", SessionID: "ended"},
	})
	h.terminalEnded("m1", "ended", "stream ended", nil)
	want := []assistantwire.SessionRef{
		{MachineID: "m1", SessionID: "chat"},
		{MachineID: "m1", SessionID: "ended"},
	}
	waitTerminalViewerReport(t, h, changed, want)
	for _, c := range clients {
		c.mu.Lock()
		remaining := len(c.termSubs)
		c.mu.Unlock()
		if remaining != 1 {
			t.Fatal("ending the stream removed the still-connected terminal page")
		}
		h.mu.Lock()
		_, connected := h.clients[c]
		h.mu.Unlock()
		if !connected {
			t.Fatal("fixture disconnected the WebSocket instead of clearing its terminal")
		}
	}
	h.terminalUnsubscribe(clients[0], "m1", "ended")
	waitTerminalViewerReport(t, h, changed, want)
	h.terminalDrop(clients[1])
	waitTerminalViewerReport(t, h, changed, []assistantwire.SessionRef{{MachineID: "m1", SessionID: "chat"}})
}

func TestUnsupportedTerminalStreamKeepsItsTerminalOnlyViewer(t *testing.T) {
	h := newTestHub(t)
	changed := terminalViewerChanges(h)
	boardNode(t, h, "m1", func(context.Context, string, json.RawMessage) (any, error) {
		return nil, errors.New("unknown method: terminal.watch")
	})
	c := terminalViewerClient(h)
	c.mu.Lock()
	c.subs = nil
	c.mu.Unlock()
	h.terminalSubscribe(context.Background(), c, "m1", "polling", "", 0, nil, false)

	deadline := time.After(3 * time.Second)
	for {
		select {
		case raw := <-c.out:
			var msg struct {
				Error       string `json:"error"`
				Unsupported bool   `json:"unsupported"`
			}
			_ = json.Unmarshal(raw, &msg)
			if msg.Unsupported && msg.Error != "" {
				goto polling
			}
		case <-deadline:
			t.Fatal("the connected terminal page was not told to poll")
		}
	}

polling:
	h.terms.mu.Lock()
	relay := h.terms.relays[[2]string{"m1", "polling"}]
	h.terms.mu.Unlock()
	if relay != nil {
		t.Fatal("unsupported terminal left its stream running")
	}
	waitTerminalViewerReport(t, h, changed, []assistantwire.SessionRef{{MachineID: "m1", SessionID: "polling"}})
	h.terminalUnsubscribe(c, "m1", "polling")
	waitTerminalViewerReport(t, h, changed, nil)
}

func TestTerminalDropCleansViewerWhenRelayIsAlreadyGone(t *testing.T) {
	h := newTestHub(t)
	changed := terminalViewerChanges(h)
	c := terminalViewerClient(h)
	c.termSubs = map[[3]string]bool{
		{"m1", "ended", "m1"}:          true,
		{"m1", "alias-ended", "alias"}: true,
	}
	// The relay has already been removed by terminalEnded; terminalDrop must still use the client's records.
	h.terminalDrop(c)
	want := []assistantwire.SessionRef{{MachineID: "m1", SessionID: "chat"}}
	waitTerminalViewerReport(t, h, changed, want)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.termSubs) != 0 {
		t.Fatalf("terminalDrop left stale viewer entries without relays: %+v", c.termSubs)
	}
	if !c.subs[[2]string{"m1", "chat"}] {
		t.Fatal("terminalDrop removed the independent chat subscription")
	}
}
