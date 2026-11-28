package assistant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The logical input queue has its own lifetime and may deliver on a different physical session.
func TestAgyUnlinkedLogicalSwitchQueuesOnlyShortReason(t *testing.T) {
	s, f, st, _ := logicalPair(t, "codex")
	ctx := t.Context()
	const machine, root, target = "server", "side-chat", "target-chat"
	setSettings(t, s, func(v *Settings) { v.Main = root })
	linked := time.Now().UTC().Add(-time.Hour)
	pane := proto.Session{SessionID: "agy-pane", Agent: "agy", PaneID: "w1:p1", Cwd: "/code/app"}
	candidate := proto.Session{SessionID: "agy-private-conversation", Agent: "agy", SourceKind: proto.SourceRuntime,
		SourceInstanceID: "agy", Cwd: pane.Cwd, UpdatedAt: time.Now()}
	f.setSessions("box", pane, candidate)
	todo := Todo{ID: "unlinked", Title: "Check the app", Origin: root, CreatedAt: linked,
		Supervision: &Supervision{Supervisor: root, SupervisorSource: "explicit"},
		Agents:      []TodoAgent{{MachineID: "box", SessionID: pane.SessionID, LinkedAt: linked, Cwd: pane.Cwd, Agent: pane.Agent}}}
	putTodo(s, todo)
	reason := i18n.T(s.lang(ctx), checkReasonAgyUnlinked)
	g := superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Detail: reason,
		Unlinked: []unlinkedPane{{MachineID: "box", SessionID: pane.SessionID, LinkedAt: linked}}}
	const otherContext = `<persona-context kind="context" title="another notice">Original context.</persona-context>`
	msg := relayMsg{From: superviseTool, Tool: superviseTool, Text: otherContext + "\n" + reason, Signals: encodeDigestSignals([]superviseSignal{g}), At: time.Now()}
	if rendered := s.agyRelayText(ctx, machine, root, msg); !strings.Contains(rendered, candidate.SessionID) {
		t.Fatalf("fixture did not resolve the candidate for the original recipient: %s", rendered)
	}
	if _, _, err := s.sendRelay(ctx, machine, root, msg); err != nil {
		t.Fatal(err)
	}
	// Hide the candidate before switching and delivering the already accepted queue entry.
	candidate.SetHiddenByDirectory(true)
	f.setSessions("box", pane, candidate)
	c := activatePair(t, st)
	queued, err := st.ClaimConversationInput(ctx, machine, root, c.Epoch)
	if err != nil || queued == nil {
		t.Fatalf("claim queued public input: %+v, %v", queued, err)
	}
	var input logicalInput
	if err := json.Unmarshal(queued.Payload, &input); err != nil {
		t.Fatal(err)
	}
	if text, _ := input.Params["text"].(string); !strings.Contains(text, otherContext) || !strings.Contains(text, reason) {
		t.Fatalf("queue discarded preexisting context or the reason: %q", text)
	}
	if strings.Contains(string(queued.Payload), candidate.SessionID) {
		t.Errorf("durable logical payload contains a private candidate: %s", queued.Payload)
	}
	if err := st.RequeueConversationInput(ctx, machine, root, queued.ClientMsgID, c.Epoch, ""); err != nil {
		t.Fatal(err)
	}
	var received string
	f.answerNode(DeviceAssistantSend, func(_ string, raw json.RawMessage) (any, error) {
		var p struct {
			SessionID string `json:"session_id"`
			Text      string `json:"text"`
		}
		if err := json.Unmarshal(raw, &p); err != nil || p.SessionID != target {
			t.Fatalf("wrong physical delivery: %s, %v", raw, err)
		}
		received = p.Text
		return map[string]bool{"ok": true}, nil
	})
	if err := s.drainLogicalInput(ctx, st, c); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(received, reason) || !strings.Contains(received, otherContext) || strings.Contains(received, candidate.SessionID) {
		t.Fatalf("switched recipient received a stale candidate or lost the reason: %q", received)
	}

	// A later direct relay also targets the new physical session, even though the public id is stable.
	candidate.SetHiddenByDirectory(false)
	f.setSessions("box", pane, candidate)
	msg.At = msg.At.Add(time.Minute)
	received = ""
	if _, _, err := s.sendRelay(ctx, machine, root, msg); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(received, reason) || !strings.Contains(received, otherContext) || strings.Contains(received, candidate.SessionID) {
		t.Fatalf("redirected send reused the public root's candidate: %q", received)
	}
}
