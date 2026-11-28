package node

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A runtime connector's raw arguments are part of the card: a different body or
// destination can never share the same detached grant.
func TestRuntimeGateCardKeepsTheCompleteToolInput(t *testing.T) {
	r := newLarkEditRig(t, func(ix proto.Interaction) proto.Resolution {
		return proto.Resolution{Dismiss: true, Pending: true, ApprovalID: ix.ID, Message: "Nothing was executed. End this turn now."}
	})
	cwd := t.TempDir()
	r.processIn(cwd, testThread)
	r.n.mu.Lock()
	r.n.procs[testThread] = &assistantProc{id: testThread, agent: "claude", cwd: cwd}
	g := r.n.larkCfg.Gates[0]
	g.Tool = &proto.GateTool{Name: "example_gate"}
	r.n.mu.Unlock()
	r.n.state.put(testThread, sessionState{Cwd: cwd, Launcher: "claude"})
	raw := json.RawMessage(`{"args":["POST","/conversations/F1/messages","{\"text\":\"first body\"}"]}`)
	result := r.n.assistantGateTool(context.Background(), &g, testThread, raw)
	r.hub.mu.Lock()
	created := len(r.hub.created)
	r.hub.mu.Unlock()
	if created == 0 {
		t.Fatalf("runtime gate raised no card; tool result = %+v", result)
	}
	card := r.hub.lastCreated(t)
	if string(card.ToolInput) != string(raw) || card.ExecutionCwd != cwd {
		t.Fatalf("runtime gate lost its exact retry identity: cwd=%q input=%s, want cwd=%q input=%s", card.ExecutionCwd, card.ToolInput, cwd, raw)
	}
}

// The real PreToolUse path returns promptly with an explicit non-execution
// result when the manager releases a detachable approval. No request reaches
// the gated API, and the card remains the one to continue later.
func TestLarkGatePendingResultEndsTheToolCallWithoutExecuting(t *testing.T) {
	r := newLarkEditRig(t, func(ix proto.Interaction) proto.Resolution {
		return proto.Resolution{Dismiss: true, Pending: true, ApprovalID: ix.ID,
			Message: "The approval is still pending. Nothing was executed. End this turn now; a later message will report the outcome."}
	})
	cwd := t.TempDir()
	const command = `ex POST '/conversations/F1/messages' '{"text":"wait for approval"}'`
	spec := runLarkEditHook(t, r, cwd, command, nil)
	if spec["permissionDecision"] != "deny" {
		t.Fatalf("pending hook decision = %#v, want no execution", spec)
	}
	reason, _ := spec["permissionDecisionReason"].(string)
	if !strings.Contains(reason, "End this turn now") || !strings.Contains(reason, "Nothing was executed") {
		t.Fatalf("pending hook reason = %q", reason)
	}
	card := r.hub.lastCreated(t)
	if !card.Detachable || card.ID == "" {
		t.Fatalf("command-gate card did not opt into detaching: %#v", card)
	}
	if card.ExecutionCwd != cwd || len(card.ToolInput) == 0 {
		t.Fatalf("detachable card lost its exact execution identity: cwd=%q input=%s", card.ExecutionCwd, card.ToolInput)
	}
	if got := r.api.requests(); len(got) != 0 {
		t.Fatalf("the pending command reached the gated API: %+v", got)
	}
}

func TestLarkEditDetachedRetryUsesOriginalApprovalGrant(t *testing.T) {
	const original = "original"
	const edited = "edited after detach"
	const body = `{"text":"original"}`
	answers := 0
	originalID := ""
	r := newLarkEditRig(t, func(ix proto.Interaction) proto.Resolution {
		answers++
		if answers == 1 {
			originalID = ix.ID
			return proto.Resolution{Dismiss: true, Pending: true, ApprovalID: ix.ID,
				Message: "Nothing was executed. End this turn now."}
		}
		return proto.Resolution{Allow: boolp(true), EditedText: edited, ApprovalID: originalID}
	})
	cwd := t.TempDir()
	base := r.processIn(cwd, testThread)
	command := `ex POST /conversations/F1/messages '{"text":"original"}'`

	first := runLarkEditHookForToolUse(t, r, cwd, command, "toolu_lark_detached_original", nil)
	if first["permissionDecision"] != "deny" || first["updatedInput"] != nil {
		t.Fatalf("detached original = %#v, want a short non-execution result", first)
	}
	originalCard := r.hub.lastCreated(t)
	if !originalCard.Detachable || originalCard.EditableText != original {
		t.Fatalf("detached original is not the editable approval: %#v", originalCard)
	}

	retry := runLarkEditHookForToolUse(t, r, cwd, command, "toolu_lark_detached_retry", nil)
	retryCard := r.hub.lastCreated(t)
	if retryCard.ID == originalCard.ID {
		t.Fatalf("retry reused interaction %q; the fixture requires a fresh interaction", retryCard.ID)
	}
	if got := r.cards(); got != 2 {
		t.Fatalf("detached retry created %d cards, want the original and one fresh retry", got)
	}
	if retry["permissionDecision"] != "allow" {
		t.Fatalf("edited retry with original ApprovalID %q and fresh interaction %q = %#v, want allow", originalCard.ID, retryCard.ID, retry)
	}
	updated, _ := retry["updatedInput"].(map[string]any)
	updatedCommand, _ := updated["command"].(string)
	v := classifyLark(updatedCommand, r.n.larkCfg.Gates, cwd, r.n.larkCLIRisk(r.n.larkCfg))
	if len(v.calls) != 1 || v.other || v.unparsed || v.unknown || v.unsure || v.deny != "" {
		t.Fatalf("edited retry is not the same standalone call: %q\n%+v", updatedCommand, v)
	}
	call := v.calls[0]
	_, text, ok := editableTextFields(call.Body)
	if !ok || text != edited {
		t.Fatalf("edited retry body = %q, text %q, parsed %v", call.Body, text, ok)
	}

	url := base + "/api/v1/conversations/F1/messages"
	if code, response := send(t, http.MethodPost, url, body); code != http.StatusForbidden {
		t.Errorf("original body after the detached edited Allow = %d %s, want its original grant retired", code, response)
	}
	tampered := strings.Replace(call.Body, edited, "tampered", 1)
	if code, response := send(t, http.MethodPost, url, tampered); code != http.StatusForbidden {
		t.Errorf("different text after the detached edited Allow = %d %s, want 403", code, response)
	}
	if got := r.api.requests(); len(got) != 0 {
		t.Fatalf("an unapproved detached retry reached the API: %+v", got)
	}
	if code, response := send(t, http.MethodPost, url, call.Body); code != http.StatusOK {
		t.Fatalf("the exact edited retry = %d %s, want 200", code, response)
	}
	if code, _ := send(t, http.MethodPost, url, call.Body); code != http.StatusForbidden {
		t.Errorf("the exact edited retry repeated = %d, want 403", code)
	}
	got := r.api.requests()
	if len(got) != 1 || got[0].Body != call.Body {
		t.Fatalf("upstream requests = %+v, want exactly the edited body once", got)
	}
	waitFor(t, "the detached edited sent-as-user report", func() bool {
		r.hub.mu.Lock()
		defer r.hub.mu.Unlock()
		return len(r.hub.larkSent) == 1
	})
	r.hub.mu.Lock()
	report := r.hub.larkSent[0]
	r.hub.mu.Unlock()
	if report.Text != edited || report.LetThrough.InteractionID != originalCard.ID ||
		report.LetThrough.EditedFrom != original || report.LetThrough.Kind != proto.LarkThroughCard {
		t.Fatalf("detached edited sent report = %#v; original approval %q, retry %q", report, originalCard.ID, retryCard.ID)
	}
}
