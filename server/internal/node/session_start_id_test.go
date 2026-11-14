package node

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A hub that names the session id on a synchronous session.start gets exactly that session: the node types Claude's
// --session-id with it and answers the session under it. The hub relies on this to reconcile an unanswered start by
// exact id (#6908). Older hubs send no session_id and keep the node-generated one.
func TestSyncStartUsesTheHubNamedSessionID(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	named := "0e000000-0000-4000-8000-0000000000bb"
	f.reply("workspace.list", map[string]any{"workspaces": []any{
		map[string]any{"workspace_id": "w1", "label": "proj"}}})
	startHerdr(f, "w1:p2", proto.StatusIdle)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	f.screenSteps("w1:p2", isKeys(), screenFixture(t, "claude-2.1.312-trust-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.txt"))
	promptLands(t, f, home, cwd)

	res, err := request(t, n, "session.start", map[string]any{"session_id": named, "cwd": "~/proj", "prompt": "fix the build"})
	if err != nil {
		t.Fatal(err)
	}
	s := decodeSessions(t, []any{res["session"]})[0]
	if s.SessionID != named {
		t.Fatalf("answered session id %q, want the hub-named %q", s.SessionID, named)
	}
	typed := f.trace("pane.send_input")
	want := startCmd("claude", "--session-id "+named, personaDir(home))
	if !slices.Contains(typed, call("pane.send_input", map[string]any{"pane_id": "w1:p2", "keys": []string{"Enter"}, "text": want})) {
		t.Fatalf("typed %v, want a start under %s", typed, named)
	}
}

// Without a hub-named id the node still generates its own, as before.
func TestSyncStartWithoutANamedIDGeneratesOne(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	f.reply("workspace.list", map[string]any{"workspaces": []any{
		map[string]any{"workspace_id": "w1", "label": "proj"}}})
	startHerdr(f, "w1:p2", proto.StatusIdle)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t2"}, "root_pane": map[string]any{"pane_id": "w1:p2"}})
	f.screenSteps("w1:p2", isKeys(), screenFixture(t, "claude-2.1.312-trust-162.txt"), screenFixture(t, "claude-2.1.312-empty-162.txt"))
	promptLands(t, f, home, cwd)

	res, err := request(t, n, "session.start", map[string]any{"cwd": "~/proj", "prompt": "fix the build"})
	if err != nil {
		t.Fatal(err)
	}
	s := decodeSessions(t, []any{res["session"]})[0]
	if !validID(s.SessionID) {
		t.Fatalf("the node generated no valid session id: %q", s.SessionID)
	}
}
