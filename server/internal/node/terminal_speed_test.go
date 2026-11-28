package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func speedIDs(tiers []proto.ServiceTier) []string {
	ids := make([]string, 0, len(tiers))
	for _, tier := range tiers {
		ids = append(ids, tier.ID)
	}
	return ids
}

func codexSpeedEvent(at time.Time, tier string) string {
	return fmt.Sprintf(`{"type":"event_msg","timestamp":%q,"payload":{"type":"thread_settings_applied","thread_settings":{"model":"example","reasoning_effort":"high","service_tier":%q}}}`, at.Format(time.RFC3339Nano), tier)
}

func TestCodexTerminalSpeed(t *testing.T) {
	n, f, path := codexNode(t, proto.StatusIdle, `{"type":"turn_context","timestamp":"2026-11-01T13:41:00Z","payload":{"model":"example","effort":"high","service_tier":"default"}}`)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, strings.Replace(string(body), "0.163.0", "0.169.1", 1))
	writeFile(t, filepath.Join(n.codexHome(), "models_cache.json"), `{"models":[{"slug":"example","service_tiers":[{"id":"priority","name":"Fast"},{"id":"ultrafast","name":"Ultrafast"},{"id":"flex","name":"Flex"}]}]}`)
	f.setScreen(cxPane, cxEmpty)
	f.after(func(c herdrCall) {
		if c.Method != "agent.prompt" {
			return
		}
		switch str(c.Params["text"]) {
		case "/model --speed=fast":
			appendLine(t, path, codexSpeedEvent(time.Now(), "priority"))
		case "/model --speed=ultrafast":
			appendLine(t, path, codexSpeedEvent(time.Now(), "ultrafast"))
		case "/model --speed=standard":
			appendLine(t, path, codexSpeedEvent(time.Now(), "default"))
		}
	})
	row := paneRow(t, n, cxID)
	if row.ServiceTier != "default" || !slices.Equal(speedIDs(row.ServiceTiers), []string{"default", "priority", "ultrafast", "flex"}) {
		t.Fatalf("terminal speed discovery: %q %v", row.ServiceTier, row.ServiceTiers)
	}
	for _, tier := range []string{"priority", "ultrafast", "default"} {
		if err := n.setSessionSpeed(context.Background(), cxID, tier); err != nil {
			t.Fatal(err)
		}
		if row := paneRow(t, n, cxID); row.ServiceTier != tier || row.Model != "example" {
			t.Fatalf("confirmed %q, row shows speed %q on model %q", tier, row.ServiceTier, row.Model)
		}
	}
	if err := n.setSessionSpeed(context.Background(), cxID, "unadvertised"); refusalCode(err) != "session_speed_unsupported" {
		t.Fatalf("unsupported tier: %v", err)
	}
	f.setScreen(cxPane, cxBox("a draft"))
	if err := n.setSessionSpeed(context.Background(), cxID, "ultrafast"); refusalCode(err) != "input_draft" {
		t.Fatalf("draft: %v", err)
	}
	if sent := f.trace("agent.prompt", "pane.send_keys", "pane.send_text"); len(sent) != 3 {
		t.Fatalf("expected only the three accepted changes: %v", sent)
	}
}

func TestCodexTerminalSpeedOlderVersion(t *testing.T) {
	n, f, path := codexNode(t, proto.StatusIdle, `{"type":"turn_context","payload":{"model":"example","service_tier":"priority"}}`)
	n.paths[cxID] = path
	writeFile(t, filepath.Join(n.codexHome(), "models_cache.json"), `{"models":[{"slug":"example","service_tiers":[{"id":"priority","name":"Fast"}]}]}`)
	if row := paneRow(t, n, cxID); len(row.ServiceTiers) != 0 {
		t.Fatalf("old binary offers tiers: %v", row.ServiceTiers)
	}
	if err := n.setSessionSpeed(context.Background(), cxID, "priority"); refusalCode(err) != "session_speed_unsupported" {
		t.Fatalf("old binary: %v", err)
	}
	if sent := f.trace("agent.prompt", "pane.send_keys", "pane.send_text"); len(sent) != 0 {
		t.Fatalf("typed into old binary: %v", sent)
	}
}

func TestClaudeTerminalSpeed(t *testing.T) {
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "project"))
	path := writeTranscript(t, home, cwd, sidA, time.Now(), `{"type":"assistant","timestamp":"2026-01-01T00:00:00Z","version":"2.1.331","message":{"model":"claude-opus-5-5","content":[{"type":"text","text":"Done"}],"usage":{"speed":"standard"}}}`)
	n.paths[sidA] = path
	seedAgents(t, n, f, claudeAgent(sidA, proto.StatusDone, cxPane, "w1:t1", "w1", cwd, ""))
	ultraScreens(t, f, cxPane)
	f.after(func(c herdrCall) {
		if c.Method != "agent.prompt" {
			return
		}
		switch cmd := str(c.Params["text"]); cmd {
		case "/fast on", "/fast off":
			outcome := "Fast mode OFF"
			if cmd == "/fast on" {
				outcome = `\u001b[38;5;208m↯\u001b[39m Fast mode ON · $8/$40 per Mtok`
			}
			appendLine(t, path, fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":"<local-command-stdout>%s</local-command-stdout>"}}`, time.Now().UTC().Format(time.RFC3339Nano), outcome))
		default:
			t.Errorf("unexpected Claude command %q", cmd)
		}
	})
	if row := paneRow(t, n, sidA); row.ServiceTier != "default" || !slices.Equal(speedIDs(row.ServiceTiers), []string{"default", "priority"}) {
		t.Fatalf("Claude speed discovery: %q %v", row.ServiceTier, row.ServiceTiers)
	}
	for _, tier := range []string{"priority", "default"} {
		if err := n.setSessionSpeed(context.Background(), sidA, tier); err != nil {
			t.Fatal(err)
		}
		if row := paneRow(t, n, sidA); row.ServiceTier != tier || row.Model != "claude-opus-5-5" {
			t.Fatalf("confirmed %q, row shows speed %q on model %q", tier, row.ServiceTier, row.Model)
		}
	}
	if err := n.setSessionSpeed(context.Background(), sidA, "ultrafast"); refusalCode(err) != "session_speed_unsupported" {
		t.Fatalf("Claude Ultrafast: %v", err)
	}
	n.noteModel(sidA, "claude-sonnet-5", "claude-opus-5-5")
	if err := n.setSessionSpeed(context.Background(), sidA, "priority"); refusalCode(err) != "session_speed_unsupported" {
		t.Fatalf("unsupported model: %v", err)
	}
	if sent := f.trace("agent.prompt"); len(sent) != 2 {
		t.Fatalf("only enable and disable should go to Claude: %v", sent)
	}
}

func TestTerminalSpeedConfirmation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "speed.jsonl")
	started := time.Now()
	writeFile(t, path, codexSpeedEvent(started.Add(-time.Second), "priority")+"\n"+
		fmt.Sprintf(`{"type":"turn_context","timestamp":%q,"payload":{"service_tier":"priority"}}`, started.Format(time.RFC3339Nano))+"\n")
	st, _ := os.Stat(path)
	if terminalSpeedRecorded(path, st.Size(), "priority", started) {
		t.Fatal("old setting or turn context confirmed a new command")
	}
	refused := "■ Speed 'ultrafast' is unavailable for example. Available: standard, priority."
	target := codexTerminalSettings{tier: "ultrafast"}
	if codexTerminalOutcome(refused, refused, target) != nil ||
		codexTerminalOutcome(refused, refused+"\n"+refused, target) == nil ||
		claudeTerminalSpeedRefusal("", "⎿ Fast mode requires usage credits") == "" {
		t.Fatal("did not distinguish current runtime refusals from old screen text")
	}
}
