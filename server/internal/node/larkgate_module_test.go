package node

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/moduleswitch"
	"github.com/nwverse/persona/server/internal/node/larkgateport"
	"github.com/nwverse/persona/server/internal/proto"
)

// needsLarkGate skips tests of Feishu/Lark-specific lark-cli gate rules when the build leaves out the lark module
// (-tags persona_without_lark or -tags persona_core_only).
func needsLarkGate(t testing.TB) {
	t.Helper()
	if larkgateport.Get(moduleswitch.Set{}) == nil {
		t.Skip("built without the lark module (internal/node/larkgateport)")
	}
}

// needsLarkGateForCommand skips only subtests whose command exercises lark-cli when the lark module is absent, so
// generic CommandGate and dynamic-command cases continue to run in persona_without_lark and persona_core_only builds.
func needsLarkGateForCommand(t testing.TB, command string) {
	t.Helper()
	if larkgateport.Get(moduleswitch.Set{}) != nil {
		return
	}
	for _, needle := range []string{"lark-cli", `lark\-cli`, "lark-c?i", "lark-{cli", "lark''-cli", `lark""-cli`, `lark-c\li`} {
		if strings.Contains(command, needle) {
			t.Skip("built without the lark module (internal/node/larkgateport)")
		}
	}
}

func larkCLIActor(args []string) string {
	if r := larkGateRules(); r != nil {
		return r.ActorLabel(args)
	}
	return ""
}

// TestOpenAPIIMHTTPWriteRequiresConfirmationWithoutLark keeps the OpenAPI IM write boundary in core.
// This test runs in every build tag matrix, so it also covers builds that exclude the lark module entirely.
func TestOpenAPIIMHTTPWriteRequiresConfirmationWithoutLark(t *testing.T) {
	calls := []larkCall{
		{Gate: 0, Method: "POST", Path: "/open-apis/im/v1/messages", Body: `{}`},
		{Gate: 0, Method: "POST", Path: "/im/v1/messages", Body: `{}`},
		{Gate: 0, Method: "POST", Path: "/im/messages", Body: `{}`},
		{Gate: 0, Method: "POST", Path: "/open-apis/im/v1/batch_messages", Body: `{}`},
		{Gate: 0, Method: "PATCH", Path: "/open-apis/im/v1/threads", Body: `{}`},
	}
	assertCoreIMWriteBoundary := func(t *testing.T) {
		t.Helper()
		for _, c := range calls {
			what, boundary := classifyGateAction(c.Method, c.Path)
			if what != "发消息" || boundary != larkgateport.BoundaryOutbound {
				t.Fatalf("%s %s classification = (%q, %d), want outbound message boundary", c.Method, c.Path, what, boundary)
			}
			if larkBypassSafe(c) {
				t.Fatalf("larkBypassSafe(%s %s) = true, want false", c.Method, c.Path)
			}
		}
	}

	t.Run("current build", func(t *testing.T) {
		if larkGateRules() == nil {
			t.Log("lark module is excluded by build tags")
		}
		assertCoreIMWriteBoundary(t)
	})

	t.Run("lark switched off at runtime", func(t *testing.T) {
		prev := hubModules.Load()
		t.Cleanup(func() { hubModules.Store(prev) })
		setHubModules([]string{moduleswitch.Marker})
		if got := larkGateRules(); got != nil {
			t.Fatalf("larkGateRules() = %v with lark switched off, want nil", got)
		}
		assertCoreIMWriteBoundary(t)
	})
}

// TestLarkGateDisabledDegradesCleanly verifies that with the lark module switched off (or omitted by build tags),
// generic proto.CommandGate rules and boundaries keep working identically while lark-cli calls fail closed.
func TestLarkGateDisabledDegradesCleanly(t *testing.T) {
	prev := hubModules.Load()
	t.Cleanup(func() { hubModules.Store(prev) })
	setHubModules([]string{moduleswitch.Marker}) // lark switched off

	if got := larkGateRules(); got != nil {
		t.Fatalf("larkGateRules() = %v with lark switched off, want nil", got)
	}

	// Generic proto.CommandGate (non-Lark plugin) still classifies, decides, summarizes and guards boundaries.
	cfg := olCfg()
	readCall := larkCall{Gate: 0, Method: "GET", Path: "/health"}
	if got := larkDecision(cfg, readCall); got != "allow" {
		t.Fatalf("generic GET decision = %q, want allow", got)
	}
	if !larkRead(readCall) || !larkBypassSafe(readCall) || larkCallOutwardWrite(cfg.Gates, readCall) {
		t.Fatalf("generic GET read/bypass/outward unexpected")
	}
	ordinaryWrite := larkCall{Gate: 0, Method: "POST", Path: "/notes", Body: `{}`}
	if got := larkDecision(cfg, ordinaryWrite); got != "confirm" || !larkBypassSafe(ordinaryWrite) {
		t.Fatalf("generic ordinary write decision=%q bypassSafe=%v", got, larkBypassSafe(ordinaryWrite))
	}
	sendCall := larkCall{Gate: 0, Method: "POST", Path: "/conversations/7123/messages", Body: `{"text":"hi"}`}
	if larkBypassSafe(sendCall) || !larkCallOutwardWrite(cfg.Gates, sendCall) {
		t.Fatalf("generic outbound write must stay behind confirmation")
	}
	delCall := larkCall{Gate: 0, Method: "DELETE", Path: "/files/1"}
	if larkBypassSafe(delCall) || !larkCallOutwardWrite(cfg.Gates, delCall) {
		t.Fatalf("generic delete must stay behind confirmation")
	}
	if got := callTitle(cfg.Gates, sendCall); got != "示例" {
		t.Fatalf("generic callTitle = %q, want 示例", got)
	}
	if s := larkSummary(cfg.Gates, []larkCall{sendCall}); !strings.Contains(s, "以你的身份在示例上发消息") {
		t.Fatalf("generic summary = %q", s)
	}

	// lark-cli calls fail closed when the lark module is absent/off (never bypass, never silent read, no "飞书" title).
	cliRead := larkCall{Gate: -1, CLI: true, Method: "CLI", Path: "/docs/+fetch", Risk: "read", Args: []string{"docs", "+fetch"}}
	if got := larkDecision(proto.AssistantLark{}, cliRead); got != "confirm" {
		t.Fatalf("cli read decision with lark off = %q, want confirm", got)
	}
	if larkRead(cliRead) || larkBypassSafe(cliRead) {
		t.Fatalf("cli read must fail closed with lark off")
	}
	cliBotSend := larkCall{Gate: -1, CLI: true, Method: "CLI", Path: "/im/+messages-send", Risk: "write", Args: []string{"im", "+messages-send", "--as", "bot"}}
	if larkBypassSafe(cliBotSend) || !larkCallOutwardWrite(cfg.Gates, cliBotSend) {
		t.Fatalf("cli --as bot send must fail closed with lark off")
	}
	cliAPI := larkCall{Gate: -1, CLI: true, Method: "POST", Path: "/open-apis/im/v1/messages", Args: []string{"api", "POST", "/open-apis/im/v1/messages"}}
	if larkBypassSafe(cliAPI) {
		t.Fatalf("cli api POST must fail closed with lark off")
	}
	if got := callTitle(nil, cliBotSend); got != "" {
		t.Fatalf("cli callTitle with lark off = %q, want empty", got)
	}
	if s := larkSummary(nil, []larkCall{cliBotSend}); strings.Contains(s, "飞书") || !strings.Contains(s, "lark-cli") {
		t.Fatalf("cli summary with lark off = %q", s)
	}
}
