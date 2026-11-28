package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// codexSpeedStart runs the real session.start path with a managed Codex whose
// app-server catalog contains tiersJSON for the selected model. The captured
// personad launch argv and fake herdr calls are the acceptance evidence.
func codexSpeedStart(t *testing.T, tiersJSON, field, speed string) (*Node, *fakeHerdr, []string, error) {
	t.Helper()
	withCodexLaunchers(t, map[string][]string{})
	n, f, _, _ := codexStartHerdr(t, 0)
	t.Setenv("HOME", n.home)
	t.Setenv("ZDOTDIR", n.home)
	writeFile(t, filepath.Join(n.home, ".zshrc"), "")
	n.bin = filepath.Join(n.home, "bin", "personad")
	models := `{"jsonrpc":"2.0","id":2,"result":{"data":[{"model":"example-big","displayName":"Example Big","hidden":false,"defaultReasoningEffort":"high","supportedReasoningEfforts":[{"reasoningEffort":"high"}],"serviceTiers":` + tiersJSON + `}],"nextCursor":null}}`
	writeFile(t, n.bin, `#!/bin/sh
case " $* " in *" app-server "*) ;; *) echo "unexpected personad invocation: $*" >&2; exit 1 ;; esac
read line
echo '{"jsonrpc":"2.0","id":1,"result":{"userAgent":"codex"}}'
read line
read line
echo '`+models+`'
sleep 60
`)
	if err := os.Chmod(n.bin, 0o755); err != nil {
		t.Fatal(err)
	}
	n.setManaged([]proto.ManagedLauncher{{Name: "gw", Agent: "codex", BaseURL: "https://gateway.example.com", APIKey: "k", Model: "example-big"}})
	_, err := request(t, n, "session.start", map[string]any{
		"cwd": "~/proj", "launcher": "gw", "prompt": "hello", field: speed,
	})
	return n, f, slices.Clone(ran), err
}

func codexStartTier(argv []string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-c" {
			if tier, ok := strings.CutPrefix(argv[i+1], "service_tier="); ok {
				return tier
			}
		}
	}
	return ""
}

// Codex records the native tier in each turn_context. Derive the fake rollout's
// record from the argv it was actually started with, as Codex does, rather than
// adding a new session-speed readback API for this acceptance test.
func recordCodexStartTurnContext(t *testing.T, n *Node, argv []string) string {
	t.Helper()
	tier := codexStartTier(argv)
	if tier == "" {
		return ""
	}
	paths, err := filepath.Glob(filepath.Join(n.home, ".codex", "sessions", "*", "*", "*", "rollout-*-"+startThread+".jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("start rollout = %v (%v)", paths, err)
	}
	line, _ := json.Marshal(map[string]any{
		"timestamp": "2026-11-19T12:00:00Z", "type": "turn_context",
		"payload": map[string]any{"model": "example-big", "service_tier": tier},
	})
	appendLines(t, paths[0], string(line))
	return string(line)
}

func TestCodexSessionStartSpeedMapsArgvAndRollout(t *testing.T) {
	for _, tc := range []struct {
		name, speed, native string
	}{
		{name: "standard requests no override", speed: "standard"},
		{name: "fast maps to native priority", speed: "fast", native: "priority"},
		{name: "ultrafast stays ultrafast", speed: "ultrafast", native: "ultrafast"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tiers := `[{"id":"priority","name":"Fast"},{"id":"ultrafast","name":"Ultrafast"}]`
			n, _, argv, err := codexSpeedStart(t, tiers, "speed", tc.speed)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{n.bin, "launch", "gw"}
			if tc.native != "" {
				want = append(want, "-c", "service_tier="+tc.native)
			}
			if !slices.Equal(argv, want) {
				t.Errorf("Codex argv = %q, want %q", argv, want)
			}
			line := recordCodexStartTurnContext(t, n, argv)
			if tc.native == "" {
				if line != "" {
					t.Errorf("standard start recorded an override: %s", line)
				}
				return
			}
			var got struct {
				Type    string `json:"type"`
				Payload struct {
					ServiceTier string `json:"service_tier"`
				} `json:"payload"`
			}
			if json.Unmarshal([]byte(line), &got) != nil || got.Type != "turn_context" || got.Payload.ServiceTier != tc.native {
				t.Errorf("turn_context = %s, want service_tier %q", line, tc.native)
			}
		})
	}
}

func TestCodexSessionStartSpeedRequiresSelectedModelTier(t *testing.T) {
	for _, tc := range []struct {
		name, speed, tiers string
	}{
		{name: "fast missing from selected model", speed: "fast", tiers: `[{"id":"ultrafast","name":"Ultrafast"}]`},
		{name: "ultrafast missing from selected model", speed: "ultrafast", tiers: `[{"id":"priority","name":"Fast"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, f, _, err := codexSpeedStart(t, tc.tiers, "speed", tc.speed)
			if code := rpcCode(err); code != "agent_speed_model" {
				t.Errorf("refusal = %q (%v), want agent_speed_model", code, err)
			}
			if calls := f.trace("tab.create", "pane.send_input"); len(calls) != 0 {
				t.Errorf("an unadvertised tier reached startup: %v", calls)
			}
		})
	}
}

func TestCodexSessionStartServiceTier(t *testing.T) {
	const tiers = `[{"id":"priority","name":"Fast"},{"id":"ultrafast","name":"Ultrafast"}]`
	for _, tier := range []string{"priority", "ultrafast"} {
		t.Run(tier, func(t *testing.T) {
			n, _, argv, err := codexSpeedStart(t, tiers, "service_tier", tier)
			if err != nil || codexStartTier(argv) != tier {
				t.Fatalf("start tier = %q, err = %v", codexStartTier(argv), err)
			}
			catalog, err := request(t, n, "launchers.models", map[string]any{"name": "gw", "configured": true})
			if err != nil || catalog["supports_start_service_tier"] != true {
				t.Fatalf("catalog did not advertise support: %v, %v", catalog, err)
			}
			recordCodexStartTurnContext(t, n, argv)
			row := proto.Session{SessionID: startThread, Agent: "codex", Capabilities: &proto.SessionCapabilities{}}
			path := n.transcriptPath(startThread)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			n.codexPaneControls(&row, n.metaNow(path, info))
			if row.ServiceTier != tier {
				t.Fatalf("first turn reported %q, want %q", row.ServiceTier, tier)
			}
		})
	}
	t.Run("unadvertised", func(t *testing.T) {
		_, f, _, err := codexSpeedStart(t, `[{"id":"priority","name":"Fast"}]`, "service_tier", "ultrafast")
		if rpcCode(err) != "agent_speed_model" || len(f.trace("tab.create", "pane.send_input")) != 0 {
			t.Fatalf("unadvertised tier reached startup or returned the wrong error: %v", err)
		}
	})
}
