package assistant

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestHubSessionBusInvalidParams(t *testing.T) {
	b := &BusHub{helloed: make(chan struct{})}
	close(b.helloed)
	for _, tc := range []struct{ method, params string }{
		{assistantwire.MethodFleetAsk, `{"interaction":42}`},
		{assistantwire.MethodAgentTabName, `{"prompt":42}`},
		{assistantwire.MethodStart, `{"text":42}`},
		{assistantwire.MethodKeepUndelivered, `{"params":42}`},
	} {
		t.Run(tc.method, func(t *testing.T) {
			out, err := b.answer(t.Context(), tc.method, json.RawMessage(tc.params))
			var rpcErr *rpc.Error
			if out != nil || !errors.As(err, &rpcErr) || rpcErr.Code != bus.CodeInvalidParams ||
				!strings.HasPrefix(rpcErr.Message, tc.method+": ") {
				t.Fatalf("%s %s = %v, %v, want invalid_params prefixed with %q", tc.method, tc.params, out, err, tc.method+": ")
			}
		})
	}
}

func TestHubAgentTabNameBusReply(t *testing.T) {
	s, f := newQuietService(t)
	f.mu.Lock()
	f.tabReply = "Fix Login"
	f.mu.Unlock()
	b := &BusHub{svc: s, helloed: make(chan struct{})}
	close(b.helloed)
	p := assistantwire.AgentTabNameParams{Prompt: "Fix the login page"}
	out, err := b.answer(t.Context(), assistantwire.AgentTabNameMethod.Name, mustJSON(p))
	if got, ok := out.(assistantwire.AgentTabNameResult); err != nil || !ok || got.Name != "fix-login" {
		t.Fatalf("agent tab name = %+v (%T), %v, want fix-login", out, out, err)
	}
}

func TestHubStartBusReply(t *testing.T) {
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server"}
	b := &BusHub{svc: newTestService(t, f), helloed: make(chan struct{})}
	close(b.helloed)
	p := assistantwire.StartParams{Text: "Start a conversation", Untitled: true}
	out, err := b.answer(t.Context(), assistantwire.StartMethod.Name, mustJSON(p))
	if got, ok := out.(assistantwire.StartResult); err != nil || !ok || got.Session.SessionID == "" || got.Session.MachineID != "server" {
		t.Fatalf("start = %+v (%T), %v, want a new session on server", out, out, err)
	}
}

func TestHubKeepUndeliveredBusReply(t *testing.T) {
	s, _ := newQuietServiceOn(t, &memStore{})
	b := &BusHub{svc: s, helloed: make(chan struct{})}
	close(b.helloed)
	p := assistantwire.KeepUndeliveredParams{
		MachineID: "server", SessionID: "conv1", Human: true,
		Params: map[string]any{"client_msg_id": "keep-1", "text": "Try again"},
	}
	out, err := b.answer(t.Context(), assistantwire.KeepUndeliveredMethod.Name, mustJSON(p))
	got, ok := out.(assistantwire.KeepUndeliveredResult)
	if err != nil || !ok || got.Undelivered == nil {
		t.Fatalf("keep undelivered = %+v (%T), %v, want a kept message", out, out, err)
	}
	if kept := got.Undelivered; kept.ID != "keep-1" || kept.Text != "Try again" || kept.State != proto.UndeliveredWaiting {
		t.Fatalf("kept message = %+v, want keep-1 waiting for delivery", kept)
	}
}
