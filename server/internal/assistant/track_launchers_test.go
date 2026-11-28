package assistant

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// GET /api/v1/assistant/tracks/launchers exposes healthy assistant launchers and defaults to extensions with the
// spaces scope, without failing when the machine is unavailable.
func TestTrackLaunchersForExtension(t *testing.T) {
	f := healthHub()
	f.devices[0].Online = true
	f.zshrc = map[string][]proto.Launcher{
		"server": {
			{Name: "claude", DisplayName: "Claude Code", Model: "claude-sonnet-4-6"},
			{Name: "codex", DisplayName: "Codex", Model: "gpt-5.4"},
		},
	}
	s := newServiceOn(t, f, &memStore{})
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Launcher = "server", "claude" })

	w := serveExtension(s, "space-conversations", "/api/v1/assistant/tracks/launchers")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var out assistantwire.TrackLaunchers
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Launchers) == 0 {
		t.Fatalf("expected launchers, got %+v", out)
	}
	foundClaude := false
	for _, l := range out.Launchers {
		if l.Name == "claude" {
			foundClaude = true
			if l.DefaultModel != "claude-sonnet-4-6" {
				t.Errorf("claude default_model = %q, want claude-sonnet-4-6", l.DefaultModel)
			}
		}
	}
	if !foundClaude {
		t.Fatalf("expected claude in launchers, got %+v", out.Launchers)
	}
}
