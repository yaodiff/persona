package assistant

import (
	"bytes"
	"context"
	"strings"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- plans and permissions answered in the terminal ----

// A plan or a tool's permission answered in the terminal can outlive its PermissionRequest hook: Claude Code does not
// end the hook when its own dialog is answered, so the card stayed on the phone until the hook's timeout (#3563).
// Reconcile the pending card with the completed tool call, not the agent's status or words: working is not proof
// that anyone approved anything.
func (s *Service) syncPlanInteractions(machine string) {
	now := time.Now()
	s.fleet.mu.Lock()
	var check []*pending
	for _, p := range s.fleet.interactions {
		if p.ix.MachineID != machine || !terminalReconciled(p) || p.checking || now.Sub(p.checkedAt) < 2*time.Second {
			continue
		}
		p.checking, p.checkedAt = true, now
		check = append(check, p)
	}
	s.fleet.mu.Unlock()
	for _, p := range check {
		go s.checkPlanInteraction(machine, p)
	}
}

// terminalReconciled: p is a hook's card whose tool call's end the transcript shows: a plan, or a Claude Code hook's
// permission. Prepared approvals (detached command gates and clarifying cards) belong to their own coordinator: their
// hooks return non-final WaitingText / ClarifyingText results into the transcript while the card stays pending for
// approvalWaitingWindow (#12918). A permission without an id needs its exact tool input to identify a unique call;
// Codex's and a runtime's cards end through their own sources. Callers hold s.fleet.mu.
func terminalReconciled(p *pending) bool {
	if p.local != nil || p.prepared != nil || p.claimed {
		return false
	}
	switch p.ix.Kind {
	case proto.IxPlan:
		return p.ix.ToolName == "ExitPlanMode"
	case proto.IxPermission:
		return (p.ix.ToolUseID != "" || len(p.ix.ToolInput) > 0) && p.ix.Agent == "" && p.ix.Resumable
	}
	return false
}

func (s *Service) checkPlanInteraction(machine string, p *pending) {
	defer func() {
		s.fleet.mu.Lock()
		p.checking = false
		s.fleet.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var items []proto.Item
	before := 0
	// No transcript-wide scan: most plans are in the latest two turns. A few older pages cover a user continuing
	// after answering in the terminal; pages have the node's item and parse budgets too.
	for page := 0; page < 3; page++ {
		var res struct {
			Items   []proto.Item `json:"items"`
			HasMore bool         `json:"has_more"`
		}
		if err := s.nodeCall(ctx, machine, "session.items", map[string]any{"session_id": p.ix.SessionID, "turns": 2,
			"before_seq": before, "lazy_images": true}, &res); err != nil || len(res.Items) == 0 {
			return
		}
		items = append(items, res.Items...)
		if planCallFinished(p.ix, items) {
			s.finishExternalPlan(p)
			return
		}
		next := res.Items[0].Seq
		if !res.HasMore || next <= 0 || (before > 0 && next >= before) {
			return
		}
		before = next
	}
}

// planCallFinished: items hold the result of the tool call ix asks about, or real assistant activity after a permission
// call whose hook was left behind. PermissionRequest in some Claude Code versions omits tool_use_id; then only a unique,
// recent call with the same tool name and canonically identical input identifies the call. Ambiguous calls, missing
// timestamps and earlier results fail closed. Ending or passing the call never invents an approval.
func planCallFinished(ix proto.Interaction, items []proto.Item) bool {
	id, callSeq := ix.ToolUseID, 0
	if id == "" {
		if ix.CreatedAt.IsZero() {
			return false
		}
		if ix.Kind == proto.IxPermission {
			id, callSeq = permissionCall(ix, items)
		} else {
			for _, it := range items {
				if it.Role != "assistant" || it.Kind != proto.KindToolUse || it.ToolName != "ExitPlanMode" || it.ToolUseID == "" ||
					it.TS.IsZero() || it.TS.After(ix.CreatedAt) || ix.CreatedAt.Sub(it.TS) > 30*time.Second {
					continue
				}
				if id != "" && id != it.ToolUseID {
					return false // two candidate plans: neither can be safely attributed to this hook
				}
				id = it.ToolUseID
			}
		}
	}
	if id == "" {
		return false
	}
	// First pass: check every loaded page for the call's own tool_result. checkPlanInteraction appends pages newest
	// first, so a single-pass loop would inspect newer-page assistant text before an older page's non-final WaitingText
	// or ClarifyingText result (#12918).
	for _, it := range items {
		if it.Role == "user" && it.Kind == proto.KindToolResult && it.ToolUseID == id &&
			!it.TS.IsZero() && !it.TS.Before(ix.CreatedAt) {
			return !nonFinalApprovalResult(it)
		}
	}
	if ix.Kind != proto.IxPermission || ix.ToolUseID != "" {
		return false
	}
	for _, it := range items {
		if it.Seq <= callSeq || it.Role != "assistant" || !it.TS.After(ix.CreatedAt) {
			continue
		}
		switch it.Kind {
		case proto.KindText, proto.KindThinking:
			if it.Text != "" {
				return true
			}
		case proto.KindToolUse:
			if it.ToolUseID != "" {
				return true
			}
		}
	}
	return false
}

// nonFinalApprovalResult reports whether a tool_result is a prepared hook's non-final waiting or clarification release
// rather than genuine command output: either the exact release message, or a PreToolUse hook error wrapping it (#12918).
func nonFinalApprovalResult(it proto.Item) bool {
	text := strings.TrimSpace(it.Text)
	if strings.HasPrefix(text, "PreToolUse:") {
		const marker = "hook error:"
		i := strings.Index(text, marker)
		if !it.IsError || i < 0 {
			return false
		}
		text = strings.TrimSpace(text[i+len(marker):])
	}
	return text == approvalops.WaitingText ||
		strings.HasPrefix(text, approvalops.WaitingText+" ") ||
		text == approvalops.ClarifyingText ||
		strings.HasPrefix(text, approvalops.ClarifyingText+" ")
}

// permissionCall finds the only call that can be this hook's request. Tool input, not timing alone, supplies the identity
// PermissionRequest omitted. A repeated transcript entry with the same id is one call; two different ids are ambiguous.
func permissionCall(ix proto.Interaction, items []proto.Item) (string, int) {
	want, err := canonicalOwnerAction(ix.ToolInput)
	if err != nil {
		return "", 0
	}
	id, seq := "", 0
	for _, it := range items {
		if it.Role != "assistant" || it.Kind != proto.KindToolUse || it.ToolName != ix.ToolName || it.ToolUseID == "" ||
			it.TS.IsZero() || it.TS.After(ix.CreatedAt) || ix.CreatedAt.Sub(it.TS) > 30*time.Second {
			continue
		}
		got, err := canonicalOwnerAction(it.ToolInput)
		if err != nil || !bytes.Equal(got, want) {
			continue
		}
		if id != "" && id != it.ToolUseID {
			return "", 0
		}
		if id == "" || it.Seq > seq {
			id, seq = it.ToolUseID, it.Seq
		}
	}
	return id, seq
}

func (s *Service) finishExternalPlan(p *pending) {
	s.fleet.mu.Lock()
	if s.fleet.interactions[p.ix.ID] != p || p.prepared != nil || p.claimed {
		s.fleet.mu.Unlock()
		return // the phone answered, a newer card replaced this one, or a prepared coordinator owns it
	}
	delete(s.fleet.interactions, p.ix.ID)
	if p.orphaned != nil {
		p.orphaned.Stop()
	}
	p.stopReminderLocked()
	s.fleet.mu.Unlock()
	s.recordEnd(p.ix.ID, "dismissed", &proto.Resolution{Dismiss: true, AnsweredBy: proto.AnsweredByTerminal})
	select {
	case p.done <- proto.Resolution{Dismiss: true}:
	default:
	}
	s.finished(p)
}
