package assistantwire_test

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestCoreAssistantBusMethodsWire(t *testing.T) {
	checkMethodWire[assistantwire.Snapshot, assistantwire.SnapshotResult](t, assistantwire.SnapshotMethod, "manager.snapshot",
		`{"epoch":"test-epoch","seq":7,"at":"2026-01-02T00:00:00Z","sessions":[],`+
			`"interactions":null,"tasks":null,"todos":null,"profile":null,"work":null,"channels":null}`,
		`{"ok":true,"seq":7}`)
	checkMethodWire[assistantwire.SessionRef, assistantwire.ToolsResult](t, assistantwire.ToolsMethod, "assistant.tools",
		`{"machine_id":"box","session_id":"conversation"}`,
		`{"tools":[{"name":"example","description":"Run an example","input_schema":{"type":"object"},"defer_loading":true}]}`)
	checkJSONRoundTrip[assistantwire.ToolsResult](t, `{"tools":null}`)
	checkJSONRoundTrip[assistantwire.ToolsResult](t, `{"tools":[]}`)
	checkMethodWire[assistantwire.ToolCall, proto.ToolResult](t, assistantwire.ToolMethod, "assistant.tool",
		`{"name":"example","arguments":{"query":"example"},"machine_id":"box","session_id":"conversation"}`,
		`{"content":[{"type":"text","text":"Try again"}],"is_error":true,"error_kind":"rate_limited","retry_after":2}`)
	checkMethodWire[assistantwire.ContextParams, assistantwire.ContextResult](t, assistantwire.ContextMethod, "assistant.context",
		`{"session_id":"conversation","machine_id":"box","query":"What changed?"}`,
		`{"text":"Live context","view_errors":"Missing summary","view_error_receipts":[{"item_id":"reply","digest":"check"}]}`)
	checkMethodWire[assistantwire.Action, assistantwire.ActionResult](t, assistantwire.ActionMethod, "manager.action",
		`{"action_id":"answer","session_id":"conversation","op":"answer","interaction_id":"prompt","interaction_version":2,"answer":{"allow":true}}`,
		`{"ok":true,"seq":9}`)
}

func TestCoreAssistantRelayBusMethodsWire(t *testing.T) {
	checkMethodWire[assistantwire.DeviceCall, json.RawMessage](t, assistantwire.DeviceMethod, "device.call",
		`{"device":"box","method":"session.items","params":{"session_id":"conversation","turns":1}}`,
		`{"items":[],"future_field":true}`)
	checkMethodWire[assistantwire.DeviceCall, json.RawMessage](t, assistantwire.DeviceRetireMethod, "device.retire",
		`{"device":"box","method":"session.stop_if_idle","params":{"session_id":"conversation","retire":true}}`,
		`{"ok":true}`)
	checkMethodWire[assistantwire.AppCall, json.RawMessage](t, assistantwire.AppMethod, "app.call",
		`{"app_id":"notify","method":"notify.quiet","params":{},"timeout_ms":1500}`,
		`{"quiet":false}`)
	const reply = `{"status":201,"header":{"Content-Type":["text/plain"]},"body":"b2s="}`
	checkMethodWire[assistantwire.HTTPRequest, assistantwire.HTTPResponse](t, assistantwire.HTTPMethod, "http.request",
		`{"method":"POST","path":"/example","query":"x=1","header":{"Content-Type":["text/plain"]},"body":"eA=="}`, reply)
	checkMethodWire[assistantwire.HTTPRequest, assistantwire.HTTPResponse](t, assistantwire.HTTPStreamedMethod, "http.request_streamed",
		`{"method":"POST","path":"/example","query":"x=1","header":{"Content-Type":["text/plain"]},"body_stream":{"id":"relay","size":1}}`, reply)
}

func TestExtensionBusMethodsWire(t *testing.T) {
	checkMethodWire(t, assistantwire.ExtensionIdentityMethod, "extensions.identity", `{}`, `{"supported":true}`)
	checkMethodWire(t, assistantwire.ModelsExtensionCompleteMethod, "models.extension_complete",
		`{"storage_id":"old-id","extension":"example","params":{"messages":[{"role":"user","content":"example"}]}}`,
		`{"text":"ok","model":"example","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1,"requests":1}}`)
	checkMethodWire(t, assistantwire.ModelsPurposeCompleteMethod, "models.purpose_complete",
		`{"purpose":"subscription.recipe","params":{"messages":[{"role":"user","content":"example"}]}}`,
		`{"text":"{}","json":{},"model":"example","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1,"requests":1}}`)
	checkMethodWire(t, assistantwire.ExtensionTrustForgetMethod, "assistant.extension_trust_forget", `{"extension":"example"}`, `{}`)
	checkMethodWire(t, assistantwire.AssistantConversationsMethod, "assistant.conversations",
		`{"since":"2026-01-02T00:00:00Z","include_archived":true,"limit":1}`, `{"items":[],"total":2,"next_offset":1}`)
	checkMethodWire(t, assistantwire.AssistantMessagesMethod, "assistant.messages",
		`{"id":"example","after_seq":3}`, `{"messages":[],"next_seq":4}`)
	checkMethodWire(t, assistantwire.AssistantMemoryMethod, "assistant.memory", `{"include_content":true}`,
		`{"entries":[{"name":"example.md","size":3,"modified":"2026-01-02T00:00:00Z","content":"abc"}]}`)
}

func TestModelEntriesAndApprovalBusMethodsWire(t *testing.T) {
	checkMethodWire(t, assistantwire.ModelsUsableMethod, "models.usable", `{"purpose":"inbox.triage"}`,
		`{"usable":true,"provider":"Example API","model":"example"}`)
	checkJSONRoundTrip[assistantwire.ModelsUsableResult](t, `{"usable":false}`)
	checkMethodWire(t, assistantwire.ModelsLaunchersMethod, "models.launchers", `{}`,
		`{"launchers":[{"name":"example","base_url":"https://api.example.com/v1"}]}`)
	checkMethodWire(t, assistantwire.AssistantEntriesListMethod, "assistant.entries.list", `{"after":"example","by":"user"}`,
		`{"entries":[],"after":"example","has_more":false}`)
	checkMethodWire(t, assistantwire.FleetApprovalApplyMethod, "fleet.approval_apply",
		`{"interaction_id":"example","resolution":{"allow":true}}`, `{"text":"applied"}`)
	checkJSONRoundTrip[assistantwire.FleetApprovalApplyResult](t, `{"text":"","stale":true}`)
	checkMethodWire(t, assistantwire.FleetApprovalDiscardMethod, "fleet.approval_discard",
		`{"interaction_id":"example","status":"denied"}`, `{"ok":true}`)
}

func checkMethodWire[P, R any](t *testing.T, m buswire.Method[P, R], name, request, reply string) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if m.Name != name {
			t.Fatalf("method = %q, want %q", m.Name, name)
		}
		checkJSONRoundTrip[P](t, request)
		checkJSONRoundTrip[R](t, reply)
	})
}

func checkJSONRoundTrip[T any](t *testing.T, want string) {
	t.Helper()
	var value T
	if err := json.Unmarshal([]byte(want), &value); err != nil {
		t.Fatal(err)
	}
	if raw, err := json.Marshal(value); err != nil || string(raw) != want {
		t.Errorf("%T wire = %s, %v, want %s", value, raw, err, want)
	}
}
