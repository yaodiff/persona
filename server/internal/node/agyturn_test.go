package node

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgyPaneListUsesCurrentNativeTurnEvidence(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seed := func(status string) {
		a := agyTestAgent(agyTestPane, home, agyTestID)
		a["agent_status"] = status
		seedAgents(t, n, f, a)
	}
	report := func(event string, extra map[string]any) {
		t.Helper()
		extra["conversationId"] = agyTestID
		extra["lastUserInput"] = "Read the example"
		raw, _ := json.Marshal(extra)
		rep, ok := nodeclient.ParseAgyReport(event, raw)
		if !ok {
			t.Fatalf("native %s payload refused", event)
		}
		input, _ := json.Marshal(rep)
		if got := n.agyHookKind(sockRequest{Pane: agyTestPane, Input: input}); got["ok"] != true {
			t.Fatalf("node refused the %s report", event)
		}
	}
	assert := func(status string, verified bool, lastText string) {
		t.Helper()
		for _, row := range listSessions(t, n) {
			if row.SessionID != agyTestRow {
				continue
			}
			data, _ := json.Marshal(row)
			var wire struct {
				Verified bool `json:"native_turn_verified"`
			}
			_ = json.Unmarshal(data, &wire)
			if row.Status != status || wire.Verified != verified || row.LastText != lastText {
				t.Fatalf("got status %q, verified %v, reply %q; want %q, %v, %q",
					row.Status, wire.Verified, row.LastText, status, verified, lastText)
			}
			return
		}
		t.Fatal("missing AGY pane row")
	}
	seed(proto.StatusWorking)
	assert(proto.StatusWorking, false, "")
	const input = `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2025-01-01T00:00:00Z","content":"<USER_REQUEST>\nRead the example\n</USER_REQUEST>"}` + "\n"
	const reply = `{"step_index":1,"type":"PLANNER_RESPONSE","status":"DONE","created_at":"2025-01-01T00:00:01Z","content":"Example verified."}` + "\n"
	file := agyTestFile(t, n.agyDataRoot(), agyTestID, input)
	report("PreInvocation", map[string]any{"initialNumSteps": 1})
	assert(proto.StatusWorking, true, "")
	agyTestAppend(t, file, reply)
	seed(proto.StatusDone)
	assert(proto.StatusDone, false, "Example verified.")
	report("Stop", map[string]any{"fullyIdle": false, "terminationReason": "NO_TOOL_CALL"})
	assert(proto.StatusDone, false, "Example verified.")
	report("Stop", map[string]any{"fullyIdle": true, "terminationReason": "error"})
	assert(proto.StatusDone, false, "Example verified.")
	report("Stop", map[string]any{"fullyIdle": true, "terminationReason": "NO_TOOL_CALL"})
	assert(proto.StatusDone, true, "Example verified.")

	// Typing the same prompt again cannot reuse a Stop from the previous turn.
	agyTestAppend(t, file, `{"step_index":2,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2025-01-01T00:00:02Z","content":"Read the example"}`+"\n")
	assert(proto.StatusDone, false, "")
}
