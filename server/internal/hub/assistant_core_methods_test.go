package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestAssistantSnapshotBusRoundTrip(t *testing.T) {
	h := newTestHub(t)
	a := connectAssistant(t, h)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	var epoch string
	eventually(t, 5*time.Second, "the first snapshot", func() bool {
		h.assistant.mu.RLock()
		defer h.assistant.mu.RUnlock()
		epoch = h.assistant.epoch
		return h.assistant.got
	})
	if _, err := a.bh.Snapshot(ctx, assistantwire.Snapshot{Epoch: epoch}); !errors.Is(err, assistantwire.ErrStaleSnapshot) {
		t.Fatalf("stale snapshot error = %v, want %v", err, assistantwire.ErrStaleSnapshot)
	}
	snapshot := assistantwire.Snapshot{Epoch: "next-epoch", Seq: 7}
	want := assistantwire.SnapshotResult{OK: true, Seq: snapshot.Seq}
	if got, err := a.bh.Snapshot(ctx, snapshot); err != nil || got != want {
		t.Fatalf("snapshot = %+v, %v, want %+v", got, err, want)
	}
	err := a.bh.Client().Call(ctx, assistantwire.MethodSnapshot, json.RawMessage(`{"seq":"bad"}`), nil)
	checkCoreBusInvalidParams(t, assistantwire.MethodSnapshot, err)
}

func TestAssistantToolsBusAcceptsLegacyParams(t *testing.T) {
	h := newTestHub(t)
	connectAssistant(t, h)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	const id = assistantwire.AssistantID
	result, err := buswire.Call(ctx, h.managerCaller(id), assistantwire.ToolsMethod, assistantwire.SessionRef{})
	if err != nil || !slices.ContainsFunc(result.Tools, func(tool proto.Tool) bool { return tool.Name == "ask_user" }) {
		t.Fatalf("typed tool list has %d tools and error %v, want ask_user", len(result.Tools), err)
	}
	want, err := rpc.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, params := range []string{`null`, `{}`, `{"machine_id":"","session_id":""}`} {
		var got json.RawMessage
		if err := h.callBus(ctx, id, assistantwire.MethodTools, json.RawMessage(params), &got); err != nil {
			t.Fatalf("assistant.tools with %s: %v", params, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("assistant.tools with %s returned a different tool catalog than the typed call", params)
		}
	}
}

func TestAssistantToolContextAndActionBusRoundTrip(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "Box", Kind: "local"})
	h.machines, h.byID["box"] = []*Machine{m}, m
	connectAssistant(t, h)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	const id = assistantwire.AssistantID
	a := h.asstOf(id)
	params := assistantwire.ContextParams{MachineID: "box", SessionID: "conversation"}
	if got, err := a.Context(ctx, params); err != nil || got.Text == "" || got.CompactInstructions == "" {
		t.Fatalf("context = %+v, %v, want live context and compaction instructions", got, err)
	}

	call := assistantwire.ToolCall{Name: "ask_user", MachineID: "box", SessionID: "conversation",
		Arguments: json.RawMessage(`{"why_owner":"priority","questions":[{"question":"Continue?","options":[{"label":"Yes"},{"label":"No"}]}]}`)}
	done := make(chan proto.ToolResult, 1)
	go func() { done <- a.Tool(ctx, call) }()
	var question proto.Interaction
	eventually(t, 5*time.Second, "the question from assistant.tool", func() bool {
		for _, ix := range h.pendingInteractions() {
			if ix.SessionID == call.SessionID && ix.Kind == proto.IxQuestion {
				question = ix
				return true
			}
		}
		return false
	})
	answer := assistantwire.Action{ActionID: "answer", Op: "answer", InteractionID: question.ID,
		Answer: json.RawMessage(`{"answers":{"Continue?":"Yes"}}`)}
	if got, err := a.Action(ctx, answer); err != nil || !got.OK {
		t.Fatalf("action = %+v, %v, want acknowledgment", got, err)
	}
	select {
	case result := <-done:
		if result.IsError || !strings.Contains(textOf(result), "Continue? → Yes") {
			t.Fatalf("tool result = %+v, want the answer", result)
		}
	case <-ctx.Done():
		t.Fatal("answer did not reach the tool call")
	}

	var rpcErr *rpc.Error
	if _, err := a.Action(ctx, assistantwire.Action{Op: "unknown"}); !errors.As(err, &rpcErr) || rpcErr.Message != `unknown op "unknown"` {
		t.Fatalf("action error = %v, want the manager's refusal", err)
	}
	offline := h.asstOf("offline-manager")
	if got := offline.Tool(ctx, call); !got.IsError || textOf(got) != errAssistantOffline.Error() {
		t.Fatalf("offline tool = %+v, want the offline error as text", got)
	}

	for _, method := range []string{assistantwire.MethodTools, assistantwire.MethodTool, assistantwire.MethodContext, assistantwire.MethodAction} {
		err := h.callBus(ctx, id, method, json.RawMessage(`[]`), nil)
		checkCoreBusInvalidParams(t, method, err)
	}
}

func TestAssistantDeviceAndAppBusRoundTrip(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "box", Name: "Box"})
	registerMachine(h, m)
	a := connectAssistant(t, h)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	list := assistantwire.DeviceCall{Device: assistantwire.DeviceAll, Method: assistantwire.DeviceInteractionsList}
	if raw, err := a.bh.DeviceCall(ctx, list); err != nil || string(raw) != `{"interactions":[]}` {
		t.Fatalf("device list = %s, %v, want the raw interactions reply", raw, err)
	}
	retire := assistantwire.DeviceCall{Device: m.cfg.ID, Method: nodeproto.StopIfIdle}
	if _, err := a.bh.DeviceCall(ctx, retire); !errors.Is(err, apierr.ErrOffline) {
		t.Fatalf("retirement on an offline machine = %v, want the relayed offline error", err)
	}

	call := assistantwire.AppCall{AppID: assistantwire.AppNotify, Method: assistantwire.NotifyQuietGet}
	if raw, err := a.bh.AppCall(ctx, call); err != nil || string(raw) != `{"quiet":false}` {
		t.Fatalf("app call = %s, %v, want the raw quiet-hours reply", raw, err)
	}

	err := a.bh.Client().Call(ctx, assistantwire.MethodDeviceRetire, list, nil)
	var coded *rpc.Error
	if !errors.As(err, &coded) || coded.Code != bus.CodeInvalidParams || coded.Message != "device.retire requires session.stop_if_idle" {
		t.Fatalf("non-retirement call = %v, want invalid_params requiring session.stop_if_idle", err)
	}
	for _, method := range []string{assistantwire.MethodDevice, assistantwire.MethodDeviceRetire, assistantwire.MethodApp} {
		err := a.bh.Client().Call(ctx, method, json.RawMessage(`[]`), nil)
		checkCoreBusInvalidParams(t, method, err)
	}
}

func checkCoreBusInvalidParams(t *testing.T, method string, err error) {
	t.Helper()
	var rpcErr *rpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != bus.CodeInvalidParams || !strings.HasPrefix(rpcErr.Message, method+": ") {
		t.Fatalf("%s error = %v, want invalid_params with the method prefix", method, err)
	}
}
