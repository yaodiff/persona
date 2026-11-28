package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A todo or assistant tool call holding an agy pane's placeholder id (agy-w1w-p1) resolves to the real companion row
// (agy-<uuid>) once the pane is gone and the companion row names the placeholder in Aliases — and vice versa while the
// pane is live. Ambiguous aliases are refused rather than guessed.
func TestAgentToolsAndSupervisionResolveAgyPlaceholderAlias(t *testing.T) {
	ctx := context.Background()
	const (
		placeholder = "agy-w1-p1"
		realID      = "agy-11111111-2222-4333-8444-555555555555"
	)

	s, f := newTaskService(t)
	realRow := proto.Session{
		SessionID:  realID,
		MachineID:  "box",
		Cwd:        "/code/app",
		Agent:      "agy",
		SourceKind: proto.SourceRuntime,
		Status:     proto.StatusDone,
		State:      proto.StateUnread,
		Title:      "Fix agy session merge",
		LastText:   "Merged and verified.",
		Capabilities: &proto.SessionCapabilities{
			Read: true, Send: true, Interrupt: true,
		},
	}
	realRow.SetAliases([]string{placeholder})
	f.setSessions("box", realRow)

	var seenItemsID, seenSendID string
	f.answerNode(DeviceSessionItems, func(_ string, raw json.RawMessage) (any, error) {
		var p struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(raw, &p)
		seenItemsID = p.SessionID
		if p.SessionID != realID {
			return map[string]any{"items": []proto.Item{}}, nil
		}
		return map[string]any{"items": []proto.Item{
			{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "Fix the bug"},
			{ID: "a1", Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "Fixed in PR #999."},
		}}, nil
	})
	f.answerNode(DeviceSessionSend, func(_ string, raw json.RawMessage) (any, error) {
		var p struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(raw, &p)
		seenSendID = p.SessionID
		return map[string]any{"ok": true}, nil
	})

	// 1. agent_read with the placeholder id resolves to the real companion session and reads its reply.
	readOut, err := s.toolAgentRead(ctx, "box", placeholder, 2, "")
	if err != nil {
		t.Fatalf("agent_read by placeholder alias: %v", err)
	}
	if seenItemsID != realID || !strings.Contains(readOut, "Fixed in PR #999.") {
		t.Fatalf("agent_read got id=%q out=%q, want id=%q with reply", seenItemsID, readOut, realID)
	}

	// 2. The alias preserves both the external-session refusal and the authorized send.
	sendArgs, _ := json.Marshal(map[string]any{"machine": "box", "session_id": placeholder, "text": "Please also check lint"})
	if _, err := s.runTool(ctx, "server", "conv1", "agent_send", sendArgs); err == nil || seenSendID != "" {
		t.Fatalf("unassigned external session: send targeted %q, error %v", seenSendID, err)
	}
	realRow.StartedByPersona = true
	f.setSessions("box", realRow)
	if _, err := s.runTool(ctx, "server", "conv1", "agent_send", sendArgs); err != nil {
		t.Fatalf("agent_send by placeholder alias: %v", err)
	}
	if seenSendID != realID {
		t.Fatalf("agent_send targeted %q, want %q", seenSendID, realID)
	}

	// 3. A todo linked to the placeholder id finds its real agent in taskAgents, taskDoneReplies, and outcomeAgents.
	todo := Todo{
		ID:     "todo_test_agy_alias",
		Title:  "Fix agy placeholder",
		Origin: "conv1",
		Agents: []TodoAgent{{MachineID: "box", SessionID: placeholder, Cwd: "/code/app", Agent: "agy"}},
	}
	putTodo(s, todo)

	agents := s.taskAgents(ctx, todo)
	if len(agents) != 1 || agents[0].SessionID != realID {
		t.Fatalf("taskAgents = %+v, want [%s]", agents, realID)
	}
	reached, ok := s.taskAgentsReached(ctx, todo)
	if !ok || len(reached) != 1 || reached[0].SessionID != realID {
		t.Fatalf("taskAgentsReached = %+v (ok=%v), want [%s]", reached, ok, realID)
	}
	seenItemsID = ""
	replies, _, _ := s.taskReplies(ctx, todo)
	if seenItemsID != realID || len(replies) != 1 || !strings.Contains(replies[0].Text, "Fixed in PR #999.") {
		t.Fatalf("taskReplies = %+v (queried %q), want reply from %s", replies, seenItemsID, realID)
	}
	seenItemsID = ""
	outcomes, _ := s.outcomeAgents(ctx, todo)
	if seenItemsID != realID || len(outcomes) != 1 || !strings.Contains(outcomes[0].Text, "Fixed in PR #999.") {
		t.Fatalf("outcomeAgents = %+v (queried %q), want outcome from %s", outcomes, seenItemsID, realID)
	}

	// 4. Reverse direction: pane is live and carries the companion id in Aliases.
	paneRow := proto.Session{
		SessionID: placeholder, MachineID: "box", Cwd: "/code/app", Agent: "agy", PaneID: "w1:p1",
		Status: proto.StatusIdle, State: proto.StateIdle, Title: "Fix agy session merge",
	}
	paneRow.SetAliases([]string{realID})
	f.setSessions("box", paneRow)
	seenItemsID = ""
	if _, err := s.toolAgentRead(ctx, "box", realID, 2, ""); err != nil {
		t.Fatalf("agent_read by companion alias while pane is live: %v", err)
	}
	if seenItemsID != placeholder {
		t.Fatalf("reverse alias agent_read targeted %q, want %q", seenItemsID, placeholder)
	}

	// 5. Ambiguous alias across two rows is refused rather than guessed.
	dup := realRow
	dup.SessionID = "agy-22222222-3333-4444-8555-666666666666"
	dup.SetAliases([]string{placeholder})
	f.setSessions("box", realRow, dup)
	if _, _, err := s.findAgent(ctx, "box", placeholder); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous alias error = %v, want ambiguous refusal", err)
	}
	if got := s.taskAgents(ctx, todo); len(got) != 0 {
		t.Fatalf("ambiguous alias taskAgents = %+v, want empty", got)
	}
}
