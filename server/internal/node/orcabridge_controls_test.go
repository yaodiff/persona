package node

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/proto"
)

// orcaControlsCaps are the status.get capabilities of an Orca whose chats take every control (1.4.277).
var orcaControlsCaps = []string{"terminal.prompt-delivery.v1", orcart.CapChatRename, orcart.CapChatRewind, orcart.CapAttentionAck}

// orcaControlsNode is orcaBridgeNode on an Orca with every control, whose agentSession.options answer with current and
// two models' efforts, and say the chat can be rewound.
func orcaControlsNode(t *testing.T, current map[string]any) (*Node, *runtimeHub, *orcaChats) {
	t.Helper()
	o := newOrcaChats(t)
	o.f.SetStatus(map[string]any{"capabilities": orcaControlsCaps})
	o.f.Handle("agentSession.options", func(map[string]any) any {
		o.mu.Lock()
		defer o.mu.Unlock()
		cur := map[string]any{}
		for k, v := range current {
			cur[k] = v
		}
		return map[string]any{"current": cur, "rewind": map[string]any{"supported": true},
			"models": []any{map[string]any{"id": "opus", "label": "Opus", "efforts": []any{"low", "high", "max"}},
				map[string]any{"id": "gpt-6.2-sol", "label": "Sol", "efforts": []any{map[string]any{"value": "low"},
					map[string]any{"value": "xhigh"}, map[string]any{"value": "ultra"}}}}}
	})
	o.f.Handle("agentSession.setOption", func(p map[string]any) any { // Orca keeps what it was set to
		o.mu.Lock()
		defer o.mu.Unlock()
		if k, _ := p["key"].(string); k != "" {
			current[k] = p["value"]
		}
		return map[string]any{"ok": true, "value": map[string]any{}}
	})
	n, hub := orcaBridgeNodeOn(t, o)
	waitFor(t, "the chat listed", func() bool { _, ok := listed(t, n)[orcaChatUUID]; return ok })
	return n, hub, o
}

// Plan mode is Orca's interaction mode plan; leaving it for another permission mode sets the interaction mode back
// first. A Claude chat offers full access at any time (bypass_available), and its row shows the mode Orca reports, plan
// included.
func TestOrcaBridgePlanModeAndFullAccess(t *testing.T) {
	current := map[string]any{"permissionMode": "ask", "interactionMode": "default", "effort": "high", "model": "opus"}
	n, _, o := orcaControlsNode(t, current)
	if s := listed(t, n)[orcaChatUUID]; !s.BypassAvailable || s.PermissionMode != "default" {
		t.Fatalf("row %+v", s)
	}
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": orcaChatUUID, "permission_mode": "plan"}); err != nil {
		t.Fatal(err)
	}
	set := o.writes("agentSession.setOption")
	if len(set) != 1 || set[0]["key"] != "interactionMode" || set[0]["value"] != "plan" {
		t.Fatalf("options %v", set)
	}
	waitFor(t, "plan on the row", func() bool { return listed(t, n)[orcaChatUUID].PermissionMode == "plan" })
	o.f.Reset()
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": orcaChatUUID, "permission_mode": proto.ModeBypass}); err != nil {
		t.Fatal(err)
	}
	set = o.writes("agentSession.setOption")
	if len(set) != 2 || set[0]["key"] != "interactionMode" || set[0]["value"] != "default" ||
		set[1]["key"] != "permissionMode" || set[1]["value"] != "full-access" {
		t.Fatalf("options %v", set)
	}
	if bad := o.f.Invalid(); len(bad) > 0 {
		t.Fatalf("Orca's schema refused %v", bad)
	}
}

// A model change is Orca's setOption model (rows offer it, caps.model), before the effort chosen with it.
func TestOrcaBridgeModelSwitch(t *testing.T) {
	n, _, o := orcaControlsNode(t, map[string]any{"permissionMode": "ask", "model": "opus", "effort": "high"})
	if s := listed(t, n)[orcaChatUUID]; s.Capabilities == nil || s.Capabilities.Model == nil || !*s.Capabilities.Model {
		t.Fatalf("no model switch on %+v", s.Capabilities)
	}
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": orcaChatUUID, "model": "sonnet", "effort": "max"}); err != nil {
		t.Fatal(err)
	}
	set := o.writes("agentSession.setOption")
	if len(set) != 2 || set[0]["key"] != "model" || set[0]["value"] != "sonnet" || set[1]["key"] != "effort" || set[1]["value"] != "max" {
		t.Fatalf("options %v", set)
	}
}

// A chat of another provider (Codex) lists the permission modes Orca takes for it (its approval as default, auto, full
// access, and plan since it takes Orca's interaction mode) and the efforts its model has that Persona can send; a mode
// Orca does not take for it is refused, nothing changed.
func TestOrcaBridgeCodexModesAndEfforts(t *testing.T) {
	n, _, o := orcaControlsNode(t, map[string]any{"permissionMode": "full-access", "interactionMode": "default",
		"model": "gpt-6.2-sol", "effort": "low"})
	waitFor(t, "the Codex chat's efforts", func() bool { return len(listed(t, n)[orcaCodexID].Efforts) > 0 })
	s := listed(t, n)[orcaCodexID]
	if got := strings.Join(s.Capabilities.PermissionModes.List(), ","); got != "default,plan,auto,bypassPermissions" ||
		strings.Join(s.Efforts, ",") != "low,xhigh" || s.PermissionMode != proto.ModeBypass || s.BypassAvailable ||
		s.Capabilities.Modes == nil || *s.Capabilities.Modes {
		t.Fatalf("row %+v caps %+v (modes %s)", s, s.Capabilities, got)
	}
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": orcaCodexID, "permission_mode": "default"}); err != nil {
		t.Fatal(err)
	}
	if set := o.writes("agentSession.setOption"); len(set) != 1 || set[0]["key"] != "permissionMode" || set[0]["value"] != "ask" {
		t.Fatalf("options %v", set)
	}
	o.f.Reset()
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": orcaCodexID, "permission_mode": "plan"}); err != nil {
		t.Fatal(err)
	}
	if set := o.writes("agentSession.setOption"); len(set) != 1 || set[0]["key"] != "interactionMode" || set[0]["value"] != "plan" {
		t.Fatalf("options %v", set)
	}
	o.f.Reset()
	_, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": orcaCodexID, "permission_mode": "acceptEdits"})
	if err == nil || !strings.Contains(err.Error(), "acceptEdits") {
		t.Fatalf("accept-edits on Codex: %v", err)
	}
	if set := o.writes("agentSession.setOption"); len(set) != 0 {
		t.Fatalf("options %v", set)
	}
}

// A chat whose provider takes no interaction mode (its options report none) is not offered plan and refuses it; a
// provider Orca's table has no permission modes for is offered none.
func TestOrcaBridgeModesOrcaRefuses(t *testing.T) {
	n, _, o := orcaControlsNode(t, map[string]any{"permissionMode": "full-access", "model": "gpt-6.2-sol"})
	waitFor(t, "the Codex chat's options", func() bool { return listed(t, n)[orcaCodexID].PermissionMode == proto.ModeBypass })
	if got := strings.Join(listed(t, n)[orcaCodexID].Capabilities.PermissionModes.List(), ","); got != "default,auto,bypassPermissions" {
		t.Fatalf("modes %s", got)
	}
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": orcaCodexID, "permission_mode": "plan"}); err == nil {
		t.Fatal("plan on a chat without Orca's interaction mode")
	}
	if set := o.writes("agentSession.setOption"); len(set) != 0 {
		t.Fatalf("options %v", set)
	}
	row := map[string]any{}
	orcaControls(row, "pi", &orcart.Options{InteractionMode: ""}, nil)
	if caps := row["capabilities"].(map[string]any); caps["permission_modes"] != nil {
		t.Fatalf("pi offered %v", caps)
	}
}

// Rename is agentSession.rename; an Orca without it refuses in the user's words.
func TestOrcaBridgeRename(t *testing.T) {
	n, _, o := orcaControlsNode(t, map[string]any{"permissionMode": "ask"})
	o.f.Handle("agentSession.rename", func(p map[string]any) any { return map[string]any{"name": p["name"], "applied": true} })
	if _, err := nodeCall(t, n, "session.rename", map[string]any{"session_id": orcaChatUUID, "title": "Parser work"}); err != nil {
		t.Fatal(err)
	}
	got := o.writes("agentSession.rename")
	if len(got) != 1 || got[0]["sessionId"] != orcaChatID || got[0]["name"] != "Parser work" {
		t.Fatalf("renames %v", got)
	}

	old, _, _ := orcaBridgeNode(t) // an Orca without the method (the fake's default capabilities)
	if _, err := nodeCall(t, old, "session.rename", map[string]any{"session_id": orcaChatUUID, "title": "Parser work"}); err == nil {
		t.Fatal("renamed on an Orca without agentSession.rename")
	}
}

// A rewind finds the user's message in Orca's journal by its text, its occurrence from the end and how many of the
// user's messages came after it, and goes back to before it (agentSession.rewind, by its item and the journal's epoch).
// One that does not match is refused with nothing changed, and so is one whose journal moved on meanwhile.
func TestOrcaBridgeRewind(t *testing.T) {
	n, _, o := orcaControlsNode(t, map[string]any{"permissionMode": "ask", "model": "gpt-6.2-sol"})
	user := func(id string, seq int, text string) any {
		return map[string]any{"itemId": id, "revision": 1, "sequence": seq, "observedAt": 1793850000000 + seq,
			"body": map[string]any{"kind": "message", "role": "user", "blocks": []any{map[string]any{"type": "text", "text": text}}}}
	}
	o.with(func(o *orcaChats) {
		o.journal = []any{user("u-1", 2, "run the tests"), user("u-2", 5, "fix it"), user("u-3", 8, "run the tests")}
	})
	waitFor(t, "the Codex chat, rewindable", func() bool {
		s, ok := listed(t, n)[orcaCodexID]
		return ok && s.Capabilities != nil && s.Capabilities.Rewind != nil && *s.Capabilities.Rewind
	})
	rewind := func(prompt string, occurrence, after int) error {
		_, err := nodeCall(t, n, "session.rewind", map[string]any{"session_id": orcaCodexID, "prompt": prompt, "occurrence": occurrence,
			"prompts_after": after, "restore": "conversation"})
		return err
	}
	if err := rewind("run the tests", 2, 1); rpcCode(err) != rewindMismatch {
		t.Fatalf("mismatch: %v", err)
	}
	if got := o.writes("agentSession.rewind"); len(got) != 0 {
		t.Fatalf("rewound on a mismatch: %v", got)
	}
	o.f.Handle("agentSession.rewind", func(map[string]any) any { return map[string]any{"ok": true, "value": map[string]any{}} })
	if err := rewind("run the tests", 2, 2); err != nil {
		t.Fatal(err)
	}
	got := o.writes("agentSession.rewind")
	if len(got) != 1 || got[0]["itemId"] != "u-1" || got[0]["expectedEpoch"] != "ep-1" {
		t.Fatalf("rewinds %v", got)
	}
	o.f.Handle("agentSession.rewind", func(map[string]any) any {
		return map[string]any{"ok": false, "refusal": map[string]any{"code": "agent_session_operation_invalid",
			"message": "agent_session_rewind:stale-epoch"}}
	})
	if err := rewind("fix it", 1, 1); rpcCode(err) != rewindMismatch {
		t.Fatalf("stale epoch: %v", err)
	}
	if bad := o.f.Invalid(); len(bad) > 0 {
		t.Fatalf("Orca's schema refused %v", bad)
	}
}

// Seen in Persona clears the chat's unread mark in Orca (agentSession.acknowledgeAttention), up to its journal's end.
func TestOrcaBridgeSeen(t *testing.T) {
	n, _, o := orcaControlsNode(t, map[string]any{"permissionMode": "ask"})
	o.f.Handle("agentSession.acknowledgeAttention", func(map[string]any) any { return map[string]any{"acknowledged": true} })
	if _, err := nodeCall(t, n, "session.seen", map[string]any{"session_id": orcaChatUUID}); err != nil {
		t.Fatal(err)
	}
	got := o.writes("agentSession.acknowledgeAttention")
	if len(got) != 1 {
		t.Fatalf("acknowledged %v", got)
	}
	cursor, _ := got[0]["observedCursor"].(map[string]any)
	if got[0]["sessionId"] != orcaChatID || cursor["epoch"] != "ep-1" {
		t.Fatalf("seen %v", got)
	}
	if bad := o.f.Invalid(); len(bad) > 0 {
		t.Fatalf("Orca's schema refused %v", bad)
	}
}
