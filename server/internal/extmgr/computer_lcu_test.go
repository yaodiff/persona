package extmgr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/mcpwait"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestLCUCallsAreJavaScript(t *testing.T) {
	h, _ := extensionTestHub(t)
	ctx := context.Background()
	_, seen := computerMac(t, h, "mac")
	if err := h.extensions.reg.SetEnabled(ctx, cuaExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.SetConfig(ctx, cuaExtensionID, json.RawMessage(`{"machines": ["mac"], "backend": "lcu", "app_approval": false}`), "test"); err != nil {
		t.Fatal(err)
	}
	steps := `[{"click":{"app":"Calculator","role":"button","name":"7"}}]`
	res := h.callComputerTool(ctx, "conv1234-5678", "computer_run", json.RawMessage(`{"machine":"mac","steps":`+steps+`}`))
	if res.IsError {
		t.Fatalf("%+v", res)
	}
	_, calls := seen()
	if len(calls) != 1 || calls[0].Tool != "js" || !strings.Contains(calls[0].Args["code"].(string), `appNamed("Calculator")`) {
		t.Fatalf("steps become the runtime's JavaScript: %+v", calls)
	}
	res = h.callComputerTool(ctx, "conv1234-5678", "computer_state", json.RawMessage(`{"machine":"mac","app":"Calculator"}`))
	if res.IsError {
		t.Fatalf("%+v", res)
	}
	_, calls = seen()
	last := calls[len(calls)-1]
	if last.Tool != "js" || !strings.Contains(last.Args["code"].(string), `getApp("Calculator")`) || last.Session != "persona-conv1234" {
		t.Fatalf("state is one js call: %+v", last)
	}
}

func TestLCUElicitationWaitKindSemantics(t *testing.T) {
	native := json.RawMessage(`{"mode":"form","message":"Allow Calculator?","requestedSchema":{"type":"object","properties":{}},"_meta":{"codex_approval_kind":"mcp_tool_call","connector_id":"computer-use","tool_params":{"app":"com.apple.calculator"},"persist":["always","session"]}}`)
	enum := json.RawMessage(`{"mode":"form","message":"Pick one","requestedSchema":{"type":"object","properties":{"choice":{"type":"string","enum":["alpha","beta"]}},"required":["choice"]}}`)
	for _, tc := range []struct {
		name         string
		params       json.RawMessage
		wantKind     proto.MCPWaitKind
		wantOverride bool
	}{
		{name: "native permission with four choices", params: native, wantKind: proto.MCPWaitApproval, wantOverride: true},
		{name: "genuine enum question", params: enum, wantKind: proto.MCPWaitUnknown, wantOverride: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := extensionTestHub(t)
			_, _ = computerMac(t, h, "mac")
			mac := h.machineByName("mac")
			var gotKind proto.MCPWaitKind
			var gotOverride bool
			h.opts.Ask = func(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
				if ix.Kind != proto.IxQuestion || len(ix.Questions) != 1 {
					t.Fatalf("presentation changed: %+v", ix)
				}
				gotKind, gotOverride = mcpwait.InteractionKindFrom(ctx, ix.ID)
				return proto.Resolution{Dismiss: true}, nil
			}
			raw, err := json.Marshal(map[string]any{"conversation": "conv-assistant", "params": tc.params})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.answerComputerElicit(context.Background(), mac, raw); err != nil {
				t.Fatal(err)
			}
			if gotOverride != tc.wantOverride || (gotOverride && gotKind != tc.wantKind) {
				t.Fatalf("wait-kind override = %q, %v; want %q, %v", gotKind, gotOverride, tc.wantKind, tc.wantOverride)
			}
		})
	}
}

func TestLCUElicitationUsesTheExistingCard(t *testing.T) {
	h, _ := extensionTestHub(t)
	keepSessions(t, h)
	_, _ = computerMac(t, h, "mac")
	mac := h.machineByName("mac")
	listSessions(h, "mac", []proto.Session{{SessionID: "conv-assistant", Assistant: true, Title: "Persona"}})
	deskOf(h)
	raw := json.RawMessage(`{"conversation":"conv-assistant","params":{"mode":"form","message":"Allow Computer Use to use \"Calculator\"?","requestedSchema":{"type":"object","properties":{}},"_meta":{"codex_approval_kind":"mcp_tool_call","connector_id":"computer-use","tool_params":{"app":"com.apple.calculator"},"tool_params_display":[{"name":"app","value":"Calculator"}],"persist":["always","session"],"riskLevel":"high","warningSubtitle":"Allowing ChatGPT to use this app"}}}`)
	out := make(chan map[string]any, 1)
	go func() {
		res, err := h.answerComputerElicit(context.Background(), mac, raw)
		if err != nil {
			t.Errorf("%v", err)
		}
		out <- res
	}()
	// First card: accept with session persist via Answers (IxQuestion).
	select {
	case c := <-deskOf(h).ch:
		if c.ix.Kind != proto.IxQuestion || c.ix.SessionID != "conv-assistant" || c.ix.MachineID != "mac" {
			t.Fatalf("card is on the assistant conversation: %+v", c.ix)
		}
		if !strings.Contains(c.ix.Summary, "「mac」") || !strings.Contains(c.ix.Summary, "高风险") || strings.Contains(c.ix.Summary, "ChatGPT") {
			t.Fatalf("summary: %q", c.ix.Summary)
		}
		q := c.ix.Questions[0].Question
		c.answer <- proto.Resolution{Answers: map[string]string{q: "本会话允许"}}
	case <-time.After(5 * time.Second):
		t.Fatal("no card")
	}
	got := <-out
	if got["action"] != "accept" {
		t.Fatalf("%v", got)
	}
	meta, _ := got["_meta"].(map[string]any)
	if meta["persist"] != "session" {
		t.Fatalf("session, not an invented always: %v", got)
	}

	// The assistant writing the answer does not approve it.
	go func() {
		res, _ := h.answerComputerElicit(context.Background(), mac, raw)
		out <- res
	}()
	select {
	case c := <-deskOf(h).ch:
		yes := true
		q := c.ix.Questions[0].Question
		c.answer <- proto.Resolution{Allow: &yes, AnsweredBy: proto.AnsweredByAssistant, Answers: map[string]string{q: "一直允许"}}
	case <-time.After(5 * time.Second):
		t.Fatal("no second card")
	}
	if got = <-out; got["action"] != "cancel" {
		t.Fatalf("assistant answer cancels: %v", got)
	}
}
