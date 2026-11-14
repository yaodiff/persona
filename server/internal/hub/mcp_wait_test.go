package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func receiveMCPWaitTest[T any](t *testing.T, ch <-chan T, description string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}

func TestMCPWaitReporterUsesCurrentManagerCapability(t *testing.T) {
	h := newTestHub(t)
	var notices bytes.Buffer
	h.bus.current = map[string]*busConn{assistantwire.AssistantID: {
		peer: rpc.New("manager", &notices), presence: busPresence{Capabilities: []string{assistantwire.CapabilityMCPWait}},
	}}
	report := h.mcpWaitReporter(assistantwire.AssistantID, "computer", "77777777-7777-4777-8777-777777777777")
	if report == nil {
		t.Fatal("a capable owning manager did not get a reporter")
	}
	report(proto.MCPWaitApproval)
	var wire struct {
		Event  string               `json:"event"`
		Params devicewire.NodeEvent `json:"params"`
	}
	if err := json.NewDecoder(&notices).Decode(&wire); err != nil {
		t.Fatal(err)
	}
	if wire.Event != devicewire.EventNotify || wire.Params.MachineID != "computer" || wire.Params.Event != proto.EventMCPWait {
		t.Fatalf("relay envelope = %+v", wire)
	}
	var exact map[string]json.RawMessage
	if err := json.Unmarshal(wire.Params.Params, &exact); err != nil || len(exact) != 2 || exact["call_id"] == nil || exact["kind"] == nil {
		t.Fatalf("wait params must contain exactly call_id and kind: %s (%v)", wire.Params.Params, err)
	}
	var update proto.MCPWaitEvent
	if err := json.Unmarshal(wire.Params.Params, &update); err != nil || update.CallID != "77777777-7777-4777-8777-777777777777" || update.Kind != proto.MCPWaitApproval {
		t.Fatalf("wait update = %+v (%v)", update, err)
	}

	for _, callID := range []string{"not-a-uuid", string(bytes.Repeat([]byte{'x'}, 1<<20))} {
		if report := h.mcpWaitReporter(assistantwire.AssistantID, "computer", callID); report != nil {
			t.Fatal("an invalid call id reached the hub's wait relay")
		}
	}

	// A manager can be replaced while the phone call is pending. Its older replacement receives no unknown event.
	h.bus.current = map[string]*busConn{assistantwire.AssistantID: {peer: rpc.New("old-manager", &notices)}}
	report(proto.MCPWaitComputer)
	var extra json.RawMessage
	if err := json.NewDecoder(&notices).Decode(&extra); err == nil {
		t.Fatalf("an older manager received %s", extra)
	}
	if old := h.mcpWaitReporter(assistantwire.AssistantID, "computer", "77777777-7777-4777-8777-777777777778"); old != nil {
		t.Fatal("an older manager must not enable phone wait updates")
	}
}

func TestMCPWaitReporterChecksMachineOwnership(t *testing.T) {
	h := newTestHub(t)
	var notices bytes.Buffer
	h.bus.current = map[string]*busConn{assistantwire.AssistantID: {
		peer: rpc.New("manager", &notices), presence: busPresence{Capabilities: []string{assistantwire.CapabilityMCPWait}},
	}}
	const callID = "99999999-9999-4999-8999-999999999999"
	h.devices.kept = map[string]string{"computer": "other-manager"}
	if report := h.mcpWaitReporter(assistantwire.AssistantID, "computer", callID); report != nil {
		t.Fatal("a manager received a reporter for another manager's machine")
	}

	h.devices.kept["computer"] = assistantwire.AssistantID
	report := h.mcpWaitReporter(assistantwire.AssistantID, "computer", callID)
	if report == nil {
		t.Fatal("the owning manager did not get a reporter")
	}
	report(proto.MCPWaitApproval)
	var first json.RawMessage
	if err := json.NewDecoder(&notices).Decode(&first); err != nil {
		t.Fatalf("the owning manager's relay is missing: %v", err)
	}

	h.devices.kept["computer"] = "other-manager"
	report(proto.MCPWaitComputer)
	var extra json.RawMessage
	if err := json.NewDecoder(&notices).Decode(&extra); err == nil {
		t.Fatalf("an existing reporter followed the machine to another manager: %s", extra)
	}
}

func TestHubApprovalWaitReportsApprovalThenComputer(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	release := make(chan struct{})
	h.askVia = func(ctx context.Context, _ proto.Interaction) (proto.Resolution, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return proto.Resolution{}, ctx.Err()
		}
		allow := true
		return proto.Resolution{Allow: &allow}, nil
	}
	kinds := make(chan proto.MCPWaitKind, 2)
	done := make(chan proto.Resolution, 1)
	go func() {
		res, _ := h.waitMCPApproval(ctx, proto.Interaction{ID: "ix_settings", Kind: proto.IxPermission},
			func(kind proto.MCPWaitKind) { kinds <- kind })
		done <- res
	}()
	receiveMCPWaitTest(t, started, "the settings approval to start")
	if kind := receiveMCPWaitTest(t, kinds, "the pending settings kind"); kind != proto.MCPWaitApproval {
		t.Fatalf("pending settings card kind = %q", kind)
	}
	close(release)
	if kind := receiveMCPWaitTest(t, kinds, "the restored settings kind"); kind != proto.MCPWaitComputer {
		t.Fatalf("resolved settings card kind = %q", kind)
	}
	if res := receiveMCPWaitTest(t, done, "the settings result"); !res.UserAllowed() {
		t.Fatalf("settings resolution = %+v", res)
	}
}

func TestCallPhoneReportsApprovalThenComputer(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone := &client{hub: h, device: "credential-ios", role: roleFull, out: make(chan []byte, 2)}
	addTestClient(h, phone)
	h.onClientMessage(ctx, phone, []byte(`{"type":"register_phone","tools":[{"name":"phone_reminders_create"}]}`))
	kinds := make(chan proto.MCPWaitKind, 2)
	report := func(kind proto.MCPWaitKind) { kinds <- kind }
	done := make(chan proto.ToolResult, 1)
	go func() { done <- h.callPhoneWithWait(ctx, "phone_reminders_create", json.RawMessage(`{}`), report) }()
	var call struct {
		CallID string `json:"call_id"`
	}
	select {
	case data := <-phone.out:
		if err := json.Unmarshal(data, &call); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("phone call was not sent")
	}
	select {
	case kind := <-kinds:
		if kind != proto.MCPWaitApproval {
			t.Fatalf("first kind = %q", kind)
		}
	case <-ctx.Done():
		t.Fatal("approval was not reported")
	}
	reply, _ := json.Marshal(map[string]any{"type": "tool_result", "call_id": call.CallID,
		"content": []proto.Content{{Type: "text", Text: "saved"}}})
	h.onClientMessage(ctx, phone, reply)
	select {
	case kind := <-kinds:
		if kind != proto.MCPWaitComputer {
			t.Fatalf("resolved kind = %q", kind)
		}
	case <-ctx.Done():
		t.Fatal("computer was not reported when the phone wait ended")
	}
	select {
	case result := <-done:
		if result.IsError || textOf(result) != "saved" {
			t.Fatalf("result = %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("phone result did not return")
	}
}

func TestCallPhoneDoesNotReportApprovalForUnconfirmedTool(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	phone := &client{hub: h, device: "credential-ios", role: roleFull, out: make(chan []byte, 1)}
	addTestClient(h, phone)
	h.onClientMessage(ctx, phone, []byte(`{"type":"register_phone","tools":[{"name":"phone_device_info"}]}`))
	kinds := make(chan proto.MCPWaitKind, 2)
	done := make(chan proto.ToolResult, 1)
	go func() {
		done <- h.callPhoneWithWait(ctx, "phone_device_info", json.RawMessage(`{}`), func(kind proto.MCPWaitKind) { kinds <- kind })
	}()
	var call struct {
		CallID string `json:"call_id"`
	}
	select {
	case data := <-phone.out:
		if err := json.Unmarshal(data, &call); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("the unconfirmed phone call was not sent")
	}
	reply, _ := json.Marshal(map[string]any{"type": "tool_result", "call_id": call.CallID,
		"content": []proto.Content{{Type: "text", Text: "ok"}}})
	h.onClientMessage(ctx, phone, reply)
	select {
	case result := <-done:
		if result.IsError {
			t.Fatalf("unconfirmed phone result = %+v", result)
		}
	case <-ctx.Done():
		t.Fatal("the unconfirmed phone call did not return")
	}
	select {
	case kind := <-kinds:
		t.Fatalf("an unconfirmed phone tool reported %q", kind)
	default:
	}
}
