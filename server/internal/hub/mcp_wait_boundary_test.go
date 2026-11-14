package hub

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	devicestate "github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type mcpWaitTestBus struct {
	events chan devicewire.NodeEvent
}

type mcpWaitTestDevices struct{ assistant.Devices }

func (mcpWaitTestDevices) Canonical(id string) string  { return id }
func (mcpWaitTestDevices) List() []devicestate.Summary { return nil }
func (mcpWaitTestDevices) Notify(devicewire.NodeEvent) {}
func (mcpWaitTestDevices) Session(machine, session string) (proto.Session, bool) {
	return proto.Session{MachineID: machine, SessionID: session, Assistant: true}, true
}

type mcpWaitNodeAnswer struct {
	value   any
	handled bool
	err     error
}

func callMCPWaitNode(manager *assistant.Service, ctx context.Context, params map[string]any) chan mcpWaitNodeAnswer {
	raw, _ := json.Marshal(params)
	done := make(chan mcpWaitNodeAnswer, 1)
	go func() {
		value, handled, err := manager.NodeRequest(ctx, "computer", "mcp.call", raw)
		done <- mcpWaitNodeAnswer{value: value, handled: handled, err: err}
	}()
	return done
}

func newMCPWaitTestBus(t *testing.T, h *Hub, onRequest rpc.Handler) *mcpWaitTestBus {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2mR, h2mW := io.Pipe()
	m2hR, m2hW := io.Pipe()
	hubPeer := rpc.New("hub", h2mW)
	managerPeer := rpc.New("manager", m2hW)
	b := &mcpWaitTestBus{events: make(chan devicewire.NodeEvent, 8)}
	managerPeer.OnEvent = func(event string, params json.RawMessage) {
		if event != devicewire.EventNotify {
			return
		}
		var notice devicewire.NodeEvent
		if json.Unmarshal(params, &notice) == nil && notice.Event == proto.EventMCPWait {
			b.events <- notice
		}
	}
	if onRequest == nil {
		onRequest = func(context.Context, string, json.RawMessage) (any, error) { return map[string]any{}, nil }
	}
	managerPeer.OnRequest = onRequest
	h.bus.current = map[string]*busConn{assistantwire.AssistantID: {
		peer: hubPeer, presence: busPresence{Capabilities: []string{assistantwire.CapabilityMCPWait}},
	}}
	done := make(chan error, 2)
	go func() { done <- hubPeer.Serve(ctx, m2hR) }()
	go func() { done <- managerPeer.Serve(ctx, h2mR) }()
	t.Cleanup(func() {
		cancel()
		_ = h2mW.Close()
		_ = m2hW.Close()
		for range 2 {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("the MCP wait test bus did not stop")
			}
		}
	})
	return b
}

func (b *mcpWaitTestBus) next(t *testing.T) devicewire.NodeEvent {
	t.Helper()
	select {
	case event := <-b.events:
		return event
	case <-time.After(5 * time.Second):
		t.Fatal("the manager did not receive the hub's mcp.wait relay")
		return devicewire.NodeEvent{}
	}
}

func requireMCPWaitBoundaryEvent(t *testing.T, event devicewire.NodeEvent, machine, callID string, kind proto.MCPWaitKind) {
	t.Helper()
	if event.MachineID != machine || event.Event != proto.EventMCPWait {
		t.Fatalf("relay envelope = %+v", event)
	}
	var exact map[string]json.RawMessage
	if err := json.Unmarshal(event.Params, &exact); err != nil || len(exact) != 2 || exact["call_id"] == nil || exact["kind"] == nil {
		t.Fatalf("wait params must contain exactly call_id and kind: %s (%v)", event.Params, err)
	}
	var update proto.MCPWaitEvent
	if err := json.Unmarshal(event.Params, &update); err != nil || update.CallID != callID || update.Kind != kind {
		t.Fatalf("wait update = %+v (%v)", update, err)
	}
}

func TestMCPWaitPhoneFieldsCrossManagerHubBoundary(t *testing.T) {
	h := newTestHub(t)
	bus := newMCPWaitTestBus(t, h, nil)
	phone := &client{hub: h, device: "credential-ios", role: roleFull, out: make(chan []byte, 2)}
	addTestClient(h, phone)
	h.onClientMessage(context.Background(), phone, []byte(`{"type":"register_phone","tools":[{"name":"phone_reminders_create"}]}`))
	manager := assistant.New(assistant.Options{
		Hub: permissionTestHub{assistantLink{h}}, Devices: mcpWaitTestDevices{},
	})
	const callID = "77777777-7777-4777-8777-777777777777"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := callMCPWaitNode(manager, ctx, map[string]any{
		"name": "phone_reminders_create", "args": json.RawMessage(`{}`), "wait_updates": true, "call_id": callID,
	})

	var phoneCall struct {
		CallID string `json:"call_id"`
	}
	select {
	case data := <-phone.out:
		if err := json.Unmarshal(data, &phoneCall); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("the phone call was not sent")
	case result := <-done:
		t.Fatalf("the tool returned before reaching the phone: %+v", result)
	}
	requireMCPWaitBoundaryEvent(t, bus.next(t), "computer", callID, proto.MCPWaitApproval)
	reply, _ := json.Marshal(map[string]any{"type": "tool_result", "call_id": phoneCall.CallID,
		"content": []proto.Content{{Type: "text", Text: "saved"}}})
	h.onClientMessage(ctx, phone, reply)
	requireMCPWaitBoundaryEvent(t, bus.next(t), "computer", callID, proto.MCPWaitComputer)
	select {
	case answer := <-done:
		result, ok := answer.value.(proto.ToolResult)
		if answer.err != nil || !answer.handled || !ok || result.IsError {
			t.Fatalf("phone answer = %+v", answer)
		}
	case <-ctx.Done():
		t.Fatal("the phone call did not return")
	}
}

func TestMCPWaitSettingsFieldsCrossManagerHubBoundary(t *testing.T) {
	h := newTestHub(t)
	h.store = &settingsStore{vals: map[string][]byte{}}
	registerMachine(h, newMachine(h, MachineConfig{ID: "computer", Name: "Computer"}))
	bus := newMCPWaitTestBus(t, h, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != assistantwire.MethodHTTP {
			return map[string]any{}, nil
		}
		var req assistantwire.HTTPRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		if req.Method == "GET" && req.Path == "/api/v1/machines/computer/claude-dirs" {
			return assistantwire.HTTPResponse{Status: 200, Body: json.RawMessage(`{"blocked":[]}`)}, nil
		}
		return assistantwire.HTTPResponse{Status: 404, Body: json.RawMessage(`{"error":"unexpected test route"}`)}, nil
	})
	manager := assistant.New(assistant.Options{
		Hub: permissionTestHub{assistantLink{h}}, Devices: mcpWaitTestDevices{},
	})
	if owner := h.machineOwner("computer"); owner != assistantwire.AssistantID {
		t.Fatalf("machine owner = %q", owner)
	}
	if !h.managerCan(assistantwire.AssistantID, assistantwire.CapabilityMCPWait) {
		t.Fatal("the test manager is not MCP wait capable")
	}
	const callID = "88888888-8888-4888-8888-888888888888"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := callMCPWaitNode(manager, ctx, map[string]any{
		"name": "persona_settings", "args": json.RawMessage(`{"action":"add","key":"agents.blocked_claude_dirs","value":{"machine":"computer","pattern":"~/.example/*"}}`),
		"session_id": "s1", "assistant": true, "wait_updates": true, "call_id": callID,
	})

	select {
	case event := <-bus.events:
		requireMCPWaitBoundaryEvent(t, event, "computer", callID, proto.MCPWaitApproval)
	case result := <-done:
		t.Fatalf("the settings tool returned before its approval: %+v", result)
	case <-time.After(5 * time.Second):
		t.Fatal("the manager did not receive the settings approval relay")
	}
	cancel() // do not apply the setting; the field contract is the pending-card relay and its restoration
	requireMCPWaitBoundaryEvent(t, bus.next(t), "computer", callID, proto.MCPWaitComputer)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled settings call did not return")
	}
}
