package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/mcpwait"
	"github.com/nwverse/persona/server/internal/proto"
)

type mcpWaitDevices struct {
	*fleetDevs
	events chan devicewire.NodeEvent
}

type mcpWaitElicitExtension struct {
	ExtensionTools
	answer func(context.Context) (map[string]any, error)
}

func (e *mcpWaitElicitExtension) AnswerComputerElicit(ctx context.Context, _ string, _ json.RawMessage) (map[string]any, error) {
	return e.answer(ctx)
}

func (d *mcpWaitDevices) Notify(event devicewire.NodeEvent) { d.events <- event }

func newMCPWaitTest(t *testing.T) (*fleetTest, chan devicewire.NodeEvent) {
	t.Helper()
	f := newFakeHub()
	base := newFleetDevs()
	base.add("mac", "Computer")
	d := &mcpWaitDevices{fleetDevs: base, events: make(chan devicewire.NodeEvent, 16)}
	s := New(Options{Hub: f, Logf: testLogf(t), Devices: d})
	return &fleetTest{t: t, s: s, d: base, f: f}, d.events
}

func nextMCPWaitEvent(t *testing.T, events chan devicewire.NodeEvent, callID string, want proto.MCPWaitKind) {
	t.Helper()
	select {
	case event := <-events:
		if event.MachineID != "mac" || event.Event != proto.EventMCPWait {
			t.Fatalf("event envelope = %+v", event)
		}
		var exact map[string]json.RawMessage
		if err := json.Unmarshal(event.Params, &exact); err != nil || len(exact) != 2 || exact["call_id"] == nil || exact["kind"] == nil {
			t.Fatalf("wait params must contain exactly call_id and kind: %s (%v)", event.Params, err)
		}
		var update proto.MCPWaitEvent
		if err := json.Unmarshal(event.Params, &update); err != nil || update.CallID != callID || update.Kind != want {
			t.Fatalf("wait update = %+v, want call %q kind %q (%v)", update, callID, want, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("wait update %q did not arrive", want)
	}
}

func requireMCPWaitTestDone(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the MCP wait interaction did not return")
	}
}

func TestMCPWaitBusHelloCapability(t *testing.T) {
	b := NewBusHub(BusOptions{Hub: "http://[fd00::20]:18660", Token: "t"})
	p, _ := b.client.HelloParams().(map[string]any)
	caps, _ := p["capabilities"].([]string)
	if !slices.Contains(caps, assistantwire.CapabilityMCPWait) {
		t.Fatalf("manager does not advertise MCP wait updates: %v", caps)
	}
}

func TestMCPWaitPermissionPublishesApprovalThenComputer(t *testing.T) {
	ft, events := newMCPWaitTest(t)
	ctx := ft.s.mcpWaitContext(context.Background(), "mac", "10000000-0000-4000-8000-000000000001", true)
	nextMCPWaitEvent(t, events, "10000000-0000-4000-8000-000000000001", proto.MCPWaitComputer)
	ix := proto.Interaction{ID: "ix_permission", MachineID: "mac", SessionID: "session", Kind: proto.IxPermission,
		ToolName: "example", Summary: "permission text must not be relayed", ExpiresAt: time.Now().Add(time.Hour)}
	done := make(chan error, 1)
	go func() {
		_, err := ft.s.askHub(ctx, ix)
		done <- err
	}()
	nextMCPWaitEvent(t, events, "10000000-0000-4000-8000-000000000001", proto.MCPWaitApproval)
	ft.waitPending(1)
	allow := true
	if err := ft.s.resolve(ix.ID, proto.Resolution{Allow: &allow}); err != nil {
		t.Fatal(err)
	}
	nextMCPWaitEvent(t, events, "10000000-0000-4000-8000-000000000001", proto.MCPWaitComputer)
	requireMCPWaitTestDone(t, done)
	select {
	case event := <-events:
		t.Fatalf("unexpected event after the permission resolved: %s", event.Params)
	default:
	}
}

func TestMCPWaitDirectPermissionPublishesApprovalThenComputer(t *testing.T) {
	ft, events := newMCPWaitTest(t)
	ctx := ft.s.mcpWaitContext(context.Background(), "mac", "10000000-0000-4000-8000-000000000002", true)
	nextMCPWaitEvent(t, events, "10000000-0000-4000-8000-000000000002", proto.MCPWaitComputer)
	ix := proto.Interaction{ID: "ix_direct_permission", MachineID: "mac", SessionID: "session", Kind: proto.IxPermission,
		ToolName: "todo_delete", Summary: "permission text must not be relayed", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	done := make(chan error, 1)
	go func() {
		_, err := ft.s.ask(ctx, ix)
		done <- err
	}()
	nextMCPWaitEvent(t, events, "10000000-0000-4000-8000-000000000002", proto.MCPWaitApproval)
	var answer chan proto.Resolution
	waitFor(t, "the direct permission", func() bool {
		ft.s.questions.mu.Lock()
		defer ft.s.questions.mu.Unlock()
		if q := ft.s.questions.m[ix.ID]; q != nil {
			answer = q.answer
			return true
		}
		return false
	})
	allow := true
	answer <- proto.Resolution{Allow: &allow}
	nextMCPWaitEvent(t, events, "10000000-0000-4000-8000-000000000002", proto.MCPWaitComputer)
	requireMCPWaitTestDone(t, done)
}

func TestMCPWaitQuestionPublishesAnswerThenComputer(t *testing.T) {
	ft, events := newMCPWaitTest(t)
	ctx := ft.s.mcpWaitContext(context.Background(), "mac", "10000000-0000-4000-8000-000000000003", true)
	nextMCPWaitEvent(t, events, "10000000-0000-4000-8000-000000000003", proto.MCPWaitComputer)
	ix := proto.Interaction{ID: "ix_question", MachineID: "mac", SessionID: "session", Kind: proto.IxQuestion,
		ToolName: "ask_user", Questions: []proto.Question{{Question: "question text must not be relayed"}},
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	done := make(chan error, 1)
	go func() {
		_, err := ft.s.ask(ctx, ix)
		done <- err
	}()
	nextMCPWaitEvent(t, events, "10000000-0000-4000-8000-000000000003", proto.MCPWaitAnswer)
	var answer chan proto.Resolution
	waitFor(t, "the ask_user question", func() bool {
		ft.s.questions.mu.Lock()
		defer ft.s.questions.mu.Unlock()
		if q := ft.s.questions.m[ix.ID]; q != nil {
			answer = q.answer
			return true
		}
		return false
	})
	answer <- proto.Resolution{Answers: map[string]string{ix.Questions[0].Question: "private answer"}}
	nextMCPWaitEvent(t, events, "10000000-0000-4000-8000-000000000003", proto.MCPWaitComputer)
	requireMCPWaitTestDone(t, done)
	select {
	case event := <-events:
		t.Fatalf("unexpected event after the question resolved: %s", event.Params)
	default:
	}
}

func TestMCPWaitInteractionKindOverride(t *testing.T) {
	ft, events := newMCPWaitTest(t)
	const callID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	outer := ft.s.mcpWaitContext(context.Background(), "mac", callID, true)
	defer ft.s.endMCPWait(outer)
	nextMCPWaitEvent(t, events, callID, proto.MCPWaitComputer)
	ctx := mcpwait.WithInteractionKind(outer, "ix_native", proto.MCPWaitApproval)
	resumeNative := pushMCPInteraction(ctx, proto.Interaction{ID: "ix_native", Kind: proto.IxQuestion})
	nextMCPWaitEvent(t, events, callID, proto.MCPWaitApproval)
	resumeNative()
	nextMCPWaitEvent(t, events, callID, proto.MCPWaitComputer)
	resumeEnum := pushMCPInteraction(ctx, proto.Interaction{ID: "ix_enum", Kind: proto.IxQuestion})
	nextMCPWaitEvent(t, events, callID, proto.MCPWaitAnswer)
	resumeEnum()
	nextMCPWaitEvent(t, events, callID, proto.MCPWaitComputer)
}

func TestMCPWaitParamsPreserveFunctionalMachine(t *testing.T) {
	const callID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	r := &mcpWaitReporter{machine: "origin", callID: callID}
	ctx := withMCPWaitReporter(context.Background(), r)
	for _, tc := range []struct {
		name     string
		params   map[string]any
		machine  string
		wantCall bool
	}{
		{name: "matching machine", params: map[string]any{"machine_id": "origin"}, machine: "origin", wantCall: true},
		{name: "different machine", params: map[string]any{"machine_id": "target"}, machine: "target", wantCall: false},
		{name: "phone call without machine", params: map[string]any{}, machine: "origin", wantCall: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mcpWaitParams(ctx, tc.params)
			if got := tc.params["machine_id"]; got != tc.machine {
				t.Fatalf("machine_id = %v, want %q", got, tc.machine)
			}
			gotCall, present := tc.params["call_id"]
			if present != tc.wantCall || present && gotCall != callID {
				t.Fatalf("call_id = %v, present %v; want present %v", gotCall, present, tc.wantCall)
			}
		})
	}
}

func TestMCPWaitNodeRequestRequiresOptIn(t *testing.T) {
	ft, events := newMCPWaitTest(t)
	params := json.RawMessage(`{"name":"unknown","args":{},"wait_updates":true,"call_id":"10000000-0000-4000-8000-000000000004"}`)
	if _, handled, err := ft.s.NodeRequest(context.Background(), "mac", "mcp.call", params); !handled || err != nil {
		t.Fatalf("NodeRequest handled = %v, err = %v", handled, err)
	}
	nextMCPWaitEvent(t, events, "10000000-0000-4000-8000-000000000004", proto.MCPWaitComputer)
	ft.s.mcpWaitMu.Lock()
	_, stillLive := ft.s.mcpWaits["10000000-0000-4000-8000-000000000004"]
	ft.s.mcpWaitMu.Unlock()
	if stillLive {
		t.Fatal("the live call registration survived its tool result")
	}
	params = json.RawMessage(`{"name":"unknown","args":{},"call_id":"10000000-0000-4000-8000-000000000005"}`)
	if _, handled, err := ft.s.NodeRequest(context.Background(), "mac", "mcp.call", params); !handled || err != nil {
		t.Fatalf("old NodeRequest handled = %v, err = %v", handled, err)
	}
	select {
	case event := <-events:
		t.Fatalf("a call without wait_updates published %s", event.Params)
	default:
	}
}

func TestMCPWaitNodeRequestRejectsInvalidAndDuplicateIDs(t *testing.T) {
	ft, events := newMCPWaitTest(t)
	for _, callID := range []any{"not-a-uuid", strings.Repeat("x", 1<<20), 17, map[string]any{"not": "a string"}} {
		params, err := json.Marshal(map[string]any{"name": "unknown", "args": map[string]any{}, "wait_updates": true, "call_id": callID})
		if err != nil {
			t.Fatal(err)
		}
		if result, handled, err := ft.s.NodeRequest(context.Background(), "mac", "mcp.call", params); !handled || err != nil || result == nil {
			t.Fatalf("an invalid id must not fail the tool call: result = %v, handled = %v, err = %v", result, handled, err)
		}
		select {
		case event := <-events:
			t.Fatalf("an invalid mcp.call id published %d bytes", len(event.Params))
		default:
		}
	}

	const liveID = "33333333-3333-4333-8333-333333333333"
	registered := ft.s.mcpWaitContext(context.Background(), "mac", liveID, true)
	defer ft.s.endMCPWait(registered)
	nextMCPWaitEvent(t, events, liveID, proto.MCPWaitComputer)
	params := json.RawMessage(`{"name":"unknown","args":{},"wait_updates":true,"call_id":"` + liveID + `"}`)
	if _, handled, err := ft.s.NodeRequest(context.Background(), "other", "mcp.call", params); !handled || err != nil {
		t.Fatalf("a duplicate id must not fail the tool call: handled = %v, err = %v", handled, err)
	}
	select {
	case event := <-events:
		t.Fatalf("a duplicate live id replaced or reused the origin at %q", event.MachineID)
	default:
	}
}

func TestMCPWaitComputerElicitRequiresLiveCall(t *testing.T) {
	ft, events := newMCPWaitTest(t)
	ft.s.SetExtensions(&mcpWaitElicitExtension{answer: func(ctx context.Context) (map[string]any, error) {
		if mcpWaitFrom(ctx) != nil {
			t.Error("an elicitation without a live registered call received a reporter")
		}
		return map[string]any{"action": "cancel"}, nil
	}})
	for name, callID := range map[string]any{
		"missing":    "",
		"unknown":    "11111111-1111-4111-8111-111111111111",
		"malformed":  "not-a-uuid",
		"oversized":  strings.Repeat("x", 1<<20),
		"non-string": 17,
	} {
		t.Run(name, func(t *testing.T) {
			params, err := json.Marshal(map[string]any{"conversation": "session", "params": map[string]any{},
				"call_id": callID, "wait_machine": "mac"})
			if err != nil {
				t.Fatal(err)
			}
			if _, handled, err := ft.s.NodeRequest(context.Background(), "target", "computer.elicit", params); !handled || err != nil {
				t.Fatalf("the elicitation itself must proceed without reporting: handled = %v, err = %v", handled, err)
			}
			select {
			case event := <-events:
				t.Fatalf("an elicitation without a live valid call published to %q: %d bytes", event.MachineID, len(event.Params))
			default:
			}
		})
	}
}

func TestMCPWaitComputerElicitUsesRegisteredOrigin(t *testing.T) {
	ft, events := newMCPWaitTest(t)
	const callID = "22222222-2222-4222-8222-222222222222"
	outer := ft.s.mcpWaitContext(context.Background(), "mac", callID, true)
	defer ft.s.endMCPWait(outer)
	nextMCPWaitEvent(t, events, callID, proto.MCPWaitComputer)
	resumeOuter := pushMCPWait(outer, proto.MCPWaitApproval)
	nextMCPWaitEvent(t, events, callID, proto.MCPWaitApproval)
	ft.s.SetExtensions(&mcpWaitElicitExtension{answer: func(ctx context.Context) (map[string]any, error) {
		resume := pushMCPWait(ctx, proto.MCPWaitAnswer)
		resume()
		return map[string]any{"action": "cancel"}, nil
	}})
	params := json.RawMessage(`{"conversation":"session","params":{},"call_id":"` + callID + `","wait_machine":"other"}`)
	if _, handled, err := ft.s.NodeRequest(context.Background(), "target", "computer.elicit", params); !handled || err != nil {
		t.Fatalf("computer.elicit handled = %v, err = %v", handled, err)
	}
	nextMCPWaitEvent(t, events, callID, proto.MCPWaitAnswer)
	nextMCPWaitEvent(t, events, callID, proto.MCPWaitApproval)
	resumeOuter()
	nextMCPWaitEvent(t, events, callID, proto.MCPWaitComputer)
}

func TestMCPWaitRestoreOutOfOrder(t *testing.T) {
	ft, events := newMCPWaitTest(t)
	const callID = "88888888-8888-4888-8888-888888888888"
	ctx := ft.s.mcpWaitContext(context.Background(), "mac", callID, true)
	defer ft.s.endMCPWait(ctx)
	nextMCPWaitEvent(t, events, callID, proto.MCPWaitComputer)
	resumeApproval := pushMCPWait(ctx, proto.MCPWaitApproval)
	nextMCPWaitEvent(t, events, callID, proto.MCPWaitApproval)
	resumeAnswer := pushMCPWait(ctx, proto.MCPWaitAnswer)
	nextMCPWaitEvent(t, events, callID, proto.MCPWaitAnswer)

	resumeApproval() // the permission closed; only the newer question is still open
	nextMCPWaitEvent(t, events, callID, proto.MCPWaitAnswer)
	resumeAnswer()
	nextMCPWaitEvent(t, events, callID, proto.MCPWaitComputer)

	const sameKindID = "88888888-8888-4888-8888-888888888889"
	sameKindCtx := ft.s.mcpWaitContext(context.Background(), "mac", sameKindID, true)
	defer ft.s.endMCPWait(sameKindCtx)
	nextMCPWaitEvent(t, events, sameKindID, proto.MCPWaitComputer)
	first := pushMCPWait(sameKindCtx, proto.MCPWaitApproval)
	nextMCPWaitEvent(t, events, sameKindID, proto.MCPWaitApproval)
	second := pushMCPWait(sameKindCtx, proto.MCPWaitApproval)
	nextMCPWaitEvent(t, events, sameKindID, proto.MCPWaitApproval)
	first()
	nextMCPWaitEvent(t, events, sameKindID, proto.MCPWaitApproval)
	second()
	nextMCPWaitEvent(t, events, sameKindID, proto.MCPWaitComputer)
}

func TestMCPWaitEndBlocksHeldElicitReporter(t *testing.T) {
	ft, events := newMCPWaitTest(t)
	const callID = "99999999-9999-4999-8999-999999999999"
	outer := ft.s.mcpWaitContext(context.Background(), "mac", callID, true)
	nextMCPWaitEvent(t, events, callID, proto.MCPWaitComputer)
	held := ft.s.mcpWaitElicitContext(context.Background(), callID)
	ft.s.endMCPWait(outer)
	resume := pushMCPWait(held, proto.MCPWaitApproval)
	resume()
	select {
	case event := <-events:
		t.Fatalf("a held elicitation reporter published after its outer call returned: %s", event.Params)
	default:
	}
	ft.s.mcpWaitMu.Lock()
	_, live := ft.s.mcpWaits[callID]
	ft.s.mcpWaitMu.Unlock()
	if live {
		t.Fatal("the completed call stayed registered")
	}
}

func TestMCPWaitNestedWaitRestoresPreviousKind(t *testing.T) {
	ft, events := newMCPWaitTest(t)
	ctx := ft.s.mcpWaitContext(context.Background(), "mac", "10000000-0000-4000-8000-000000000008", true)
	defer ft.s.endMCPWait(ctx)
	nextMCPWaitEvent(t, events, "10000000-0000-4000-8000-000000000008", proto.MCPWaitComputer)
	resumeApproval := pushMCPWait(ctx, proto.MCPWaitApproval)
	nextMCPWaitEvent(t, events, "10000000-0000-4000-8000-000000000008", proto.MCPWaitApproval)
	resumeAnswer := pushMCPWait(ctx, proto.MCPWaitAnswer)
	nextMCPWaitEvent(t, events, "10000000-0000-4000-8000-000000000008", proto.MCPWaitAnswer)
	resumeAnswer()
	nextMCPWaitEvent(t, events, "10000000-0000-4000-8000-000000000008", proto.MCPWaitApproval)
	resumeApproval()
	nextMCPWaitEvent(t, events, "10000000-0000-4000-8000-000000000008", proto.MCPWaitComputer)

	plain := ft.s.mcpWaitContext(context.Background(), "mac", "10000000-0000-4000-8000-000000000005", false)
	if mcpWaitFrom(plain) != nil {
		t.Fatal("a call without wait_updates must not publish live state")
	}
}
