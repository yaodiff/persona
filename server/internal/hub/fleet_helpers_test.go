package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// What the manager tells the hub of the nodes, as the hub's own tests play it (fleet_link.go): a machine's list as
// the apps get it, a pending interaction, one that is over.

// reportSessions is the manager's fleet.sessions for m: list as the apps get it (decorated as the test wants).
func reportSessions(h *Hub, m *Machine, list ...proto.Session) {
	for i := range list {
		list[i].MachineID = m.cfg.ID
	}
	h.takeFleetSessions(assistantwire.FleetSessions{MachineID: m.cfg.ID, Sessions: list})
}

// reportInteraction is the manager's fleet.interaction.
func reportInteraction(t *testing.T, h *Hub, ix proto.Interaction) {
	t.Helper()
	b, _ := json.Marshal(assistantwire.FleetInteraction{Interaction: ix})
	busEvents[assistantwire.EventFleetInteraction](h, nil, b)
}

// reportResolved is the manager's fleet.interaction_resolved.
func reportResolved(t *testing.T, h *Hub, id, by string) {
	t.Helper()
	b, _ := json.Marshal(assistantwire.FleetResolved{ID: id, AnsweredBy: by})
	busEvents[assistantwire.EventFleetResolved](h, nil, b)
}

// reportLive is the manager's fleet.live.
func reportLive(t *testing.T, h *Hub, st FleetState, stats FleetStats) {
	t.Helper()
	b, _ := json.Marshal(assistantwire.FleetLive{State: st, Stats: stats})
	busEvents[assistantwire.EventFleetLive](h, nil, b)
}

// applyDeviceStates is applyDeviceStatesFrom the manager that carries hub.json's machines.
func (h *Hub) applyDeviceStates(st devicewire.States) { h.applyDeviceStatesFrom(devicesOwner, st) }

// testAsks plays the manager for the hub's own cards in the hub's tests (Hub.askVia): a card is shown to the apps
// (fleet.interaction) and waits for h.resolve.
type testAsks struct {
	mu    sync.Mutex
	waits map[string]chan proto.Resolution
}

// playedAsks are the tests' hubs' played managers.
var playedAsks sync.Map // *Hub → *testAsks

// playManagerAsks makes h's own cards wait for h.resolve (newTestHub does it).
func playManagerAsks(h *Hub) {
	q := &testAsks{waits: map[string]chan proto.Resolution{}}
	playedAsks.Store(h, q)
	h.askVia = func(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
		if _, ok := testAssistants.Load(h); ok { // a manager is connected: it asks (fleet.ask)
			var r proto.Resolution
			if err := h.asst().call(ctx, assistantwire.MethodFleetAsk, map[string]any{"interaction": ix}, &r); err != nil {
				return proto.Resolution{Dismiss: true}, nil
			}
			return r, nil
		}
		if ix.ID == "" {
			ix.ID = "ix_" + randomHex(6)
		}
		if ix.CreatedAt.IsZero() {
			ix.CreatedAt = time.Now().UTC()
		}
		ch := make(chan proto.Resolution, 1)
		q.mu.Lock()
		q.waits[ix.ID] = ch
		q.mu.Unlock()
		b, _ := json.Marshal(assistantwire.FleetInteraction{Interaction: ix})
		busEvents[assistantwire.EventFleetInteraction](h, nil, b)
		defer func() {
			q.mu.Lock()
			delete(q.waits, ix.ID)
			q.mu.Unlock()
			b, _ := json.Marshal(assistantwire.FleetResolved{ID: ix.ID})
			busEvents[assistantwire.EventFleetResolved](h, nil, b)
		}()
		wait := time.Until(ix.ExpiresAt)
		if ix.ExpiresAt.IsZero() {
			wait = time.Hour
		}
		select {
		case r := <-ch:
			return r, nil
		case <-ctx.Done():
			return proto.Resolution{Dismiss: true}, nil
		case <-time.After(wait):
			return proto.Resolution{Dismiss: true}, nil
		}
	}
}

// resolve answers one of the hub's own cards as the manager would (tests: playManagerAsks).
func (h *Hub) resolve(id string, r proto.Resolution) error {
	var ch chan proto.Resolution
	if v, ok := playedAsks.Load(h); ok {
		q := v.(*testAsks)
		q.mu.Lock()
		ch = q.waits[id]
		q.mu.Unlock()
	}
	if ch == nil {
		return resolveAtManager(h, id, r)
	}
	select {
	case ch <- r:
		return nil
	default:
		return fmt.Errorf("interaction %s is no longer pending", id)
	}
}

// mcpTools are the tools the manager lists for a node (assistant/fleet_tools.go) from what the hub has: the phone's,
// the connectors', and for an assistant conversation Cua's, ego's and the connectors only it gets (its own tools
// left out: they are the manager's).
func (h *Hub) mcpTools(assistant bool) []proto.Tool {
	ctx := context.Background()
	agents, assistantOnly := fakeExtOf(h).AgentTools(ctx, assistant) // the extensions' (the manager's, faked)
	tools := append(h.phoneToolList(), agents...)
	if !assistant {
		for _, n := range []string{"report_to_supervisor", "todo_decisions", "todo_decision_add", "todo_bind_thread"} { // the agents' own, the manager's (assistant AgentTools)
			t := prompts.Tool(n)
			tools = append(tools, proto.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
		}
	}
	if assistant {
		tools = append(tools, h.asst().Tools(ctx)...) // the assistant's own, when one is connected
		tools = append(tools, assistantOnly...)
	}
	return tools
}

// callTool is a node's MCP call as the manager routes it to the hub (app.call hub, then phone).
func (h *Hub) callTool(ctx context.Context, machineID, name string, args json.RawMessage) proto.ToolResult {
	if r, ok := fakeExtOf(h).CallTool(ctx, machineID, "", name, args, false); ok {
		return r
	}
	b, _ := json.Marshal(map[string]any{"name": name, "args": args})
	out, ok, err := h.fleetAppCall(ctx, assistantwire.AssistantID, assistantwire.AppCall{AppID: assistantwire.AppPhone, Method: assistantwire.PhoneCall, Params: b})
	if !ok || err != nil {
		return proto.TextResult("unknown tool "+name, true)
	}
	var r proto.ToolResult
	_ = json.Unmarshal(out, &r)
	return r
}

// callAssistantTool runs one of the assistant's tools for the conversation sessionID on m, as a node's mcp.call
// reaches it in the manager.
func (h *Hub) callAssistantTool(ctx context.Context, m *Machine, sessionID, name string, args json.RawMessage) proto.ToolResult {
	return h.asst().Tool(ctx, assistantwire.ToolCall{Name: name, Arguments: args, MachineID: m.cfg.ID, SessionID: sessionID})
}

// isAssistantTool: name is one of the assistant's own tools (it lists them: assistant.tools).
func (h *Hub) isAssistantTool(ctx context.Context, name string) bool {
	for _, t := range h.asst().Tools(ctx) {
		if t.Name == name {
			return true
		}
	}
	return false
}

// resolveAtManager answers a card of the connected manager's as an app does (the route the hub relays to it).
func resolveAtManager(h *Hub, id string, r proto.Resolution) error {
	if _, ok := testAssistants.Load(h); !ok {
		return fmt.Errorf("interaction %s is no longer pending", id)
	}
	b, _ := json.Marshal(r)
	req := httptest.NewRequest("POST", "/api/v1/interactions/"+id+"/resolve", bytes.NewReader(b))
	w := httptest.NewRecorder()
	h.forwardHTTP(w, req, assistantwire.AssistantID) // as the hub relays an app's answer
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"already":true`) {
		return fmt.Errorf("resolve %s: %d %s", id, w.Code, w.Body)
	}
	settleAssistant(h)
	return nil
}

// todoLinkOf is the open todo the agent mid/sid works for, as the assistant reported it.
func (h *Hub) todoLinkOf(mid, sid string) (assistantwire.TodoLink, bool) {
	tl := &h.todoLinks
	tl.mu.RLock()
	defer tl.mu.RUnlock()
	l, ok := tl.links[[2]string{mid, sid}]
	return l, ok
}
