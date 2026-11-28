package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

const agySwitchConv = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeee0001"

// writeAgySwitchTranscript writes a native Antigravity transcript.jsonl under ~/.gemini/antigravity-cli/brain/<conv>.
func writeAgySwitchTranscript(t *testing.T, home, conv string, lines ...map[string]any) {
	t.Helper()
	dir := mkdir(t, filepath.Join(home, ".gemini", "antigravity-cli", "brain", conv, ".system_generated", "logs"))
	var b strings.Builder
	for _, line := range lines {
		raw, err := json.Marshal(line)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "transcript.jsonl"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// withAgyLauncher registers agy in the shell launcher cache alongside any extra launchers.
func withAgyLauncher(t *testing.T) {
	t.Helper()
	launcherCache.Lock()
	oldAt, oldList := launcherCache.at, launcherCache.list
	launcherCache.at = time.Now()
	launcherCache.list = append(append([]proto.Launcher(nil), oldList...), proto.Launcher{Name: agyAgent, Agent: agyAgent})
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.at, launcherCache.list = oldAt, oldList
		launcherCache.Unlock()
	})
}

// agyAgentRow builds a fake herdr snapshot entry for a terminal agy pane, optionally reporting a native conversation UUID.
func agyAgentRow(status, pane, tab, ws, cwd, conv string) map[string]any {
	row := agentOf(agyAgent, "", status, pane, tab, ws, cwd, agyAgent)
	if conv != "" {
		row["agent_session"] = map[string]any{
			"source": "herdr:antigravity_cli",
			"agent":  agyAgent,
			"kind":   "id",
			"value":  conv,
		}
	}
	return row
}

// agyComesUp plays Antigravity CLI in pane w1:p1 when HERDR_AGENT=agy is typed into the shell.
func agyComesUp(t *testing.T, f *fakeHerdr, cwd, conv string, up bool) func() []string {
	t.Helper()
	var cmds []string
	f.after(func(c herdrCall) {
		if c.Method != "pane.send_input" || str(c.Params["pane_id"]) != "w1:p1" {
			return
		}
		typed := str(c.Params["text"])
		if !strings.Contains(typed, "HERDR_AGENT=agy") {
			return
		}
		cmds = append(cmds, typed)
		if !up {
			return
		}
		f.setScreens("w1:p1", "? for shortcuts\n", "? for shortcuts\n")
		f.reply("session.snapshot", snapshot(nil, nil, []any{agyAgentRow(proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, conv)}, nil))
	})
	return func() []string { return append([]string(nil), cmds...) }
}

func TestEngineSwitchClaudeToAgyOnNextMessage(t *testing.T) {
	n, f, _, cwd, _ := switchNode(t, proto.StatusIdle, longTurn)
	withAgyLauncher(t)
	agent := claudeAgent(sidA, proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, "")
	agent["tokens"] = map[string]any{"persona_todo_ref": "todo-agy@m1/" + sidA, personaTabToken: sidA}
	f.reply("session.snapshot", snapshot(nil, nil, []any{agent}, nil))
	title := rowOf(t, n, sidA).Title

	if err := pick(t, n, map[string]any{"launcher": "agy", "model": "gemini-4-neon-high"}); err != nil {
		t.Fatal(err)
	}
	ps := rowOf(t, n, sidA).PendingSwitch
	if ps == nil || ps.Launcher != "agy" || ps.Agent != "agy" || ps.Model != "gemini-4-neon-high" {
		t.Fatalf("pending switch = %+v", ps)
	}

	quitsOn(f, "w1:p1", sidA, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidA))
	cmds := agyComesUp(t, f, cwd, agySwitchConv, true)
	f.reply("agent.prompt", map[string]any{})
	f.reply("agent.get", map[string]any{"agent": agyAgentRow(proto.StatusWorking, "w1:p1", "w1:t1", "w1", cwd, agySwitchConv)})

	if err := tellTo(t, n, sidA, "continue on neon"); err != nil {
		t.Fatal(err)
	}
	if len(cmds()) != 1 || !strings.Contains(cmds()[0], "agy --model gemini-4-neon-high") {
		t.Fatalf("typed commands = %v", cmds())
	}
	prompts := promptsTo(f)
	if len(prompts) != 1 {
		t.Fatalf("prompts = %d, want 1", len(prompts))
	}
	checkHandoffBlock(t, prompts[0], "claude", "agy", "gemini-4-neon-high", false, "continue on neon")

	agyPaneID := otherSession(host.Agent{Agent: agyAgent, PaneID: "w1:p1"})
	rows := listSessions(t, n)
	if findRow(rows, sidA) != nil {
		t.Fatalf("old Claude session %s should be hidden by joinStretches", sidA)
	}
	got := findRow(rows, agyPaneID)
	if got == nil {
		t.Fatalf("new agy pane session %s not listed", agyPaneID)
	}
	if legs := got.EngineHistory(); len(legs) != 1 || legs[0].SessionID != sidA || legs[0].Agent != "claude" {
		t.Fatalf("EngineHistory = %+v, want claude leg %s", legs, sidA)
	}
	if got.Title != title {
		t.Fatalf("title = %q, want %q", got.Title, title)
	}
	if n.pendingSwitch(sidA) != nil {
		t.Fatalf("old pending switch was not cleared")
	}
}

func TestEngineSwitchClaudePlanToAgyDoesNotEscalateBypass(t *testing.T) {
	n, f, _, cwd, _ := switchNode(t, proto.StatusIdle, longTurn)
	withAgyLauncher(t)
	n.noteModes(sidA, "plan", "")

	if err := pick(t, n, map[string]any{"launcher": "agy", "model": "gemini-4-neon-high"}); err != nil {
		t.Fatal(err)
	}
	quitsOn(f, "w1:p1", sidA, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidA))
	cmds := agyComesUp(t, f, cwd, agySwitchConv, true)
	f.reply("agent.prompt", map[string]any{})
	f.reply("agent.get", map[string]any{"agent": agyAgentRow(proto.StatusWorking, "w1:p1", "w1:t1", "w1", cwd, agySwitchConv)})

	if err := tellTo(t, n, sidA, "review only"); err != nil {
		t.Fatal(err)
	}
	if len(cmds()) != 1 {
		t.Fatalf("typed commands = %v", cmds())
	}
	if strings.Contains(cmds()[0], "--dangerously-skip-permissions") {
		t.Fatalf("plan-mode Claude escalated to agy bypass: %s", cmds()[0])
	}
}

func TestEngineSwitchAgyToClaudeReadsTranscriptAndServesParkedItems(t *testing.T) {
	n, f, home, cwd, _ := switchNode(t, proto.StatusIdle, longTurn)
	withAgyLauncher(t)

	agyPaneID := otherSession(host.Agent{Agent: agyAgent, PaneID: "w1:p1"})
	now := time.Now().UTC().Truncate(time.Second)
	writeAgySwitchTranscript(t, home, agySwitchConv,
		map[string]any{
			"step_index": 0,
			"type":       "USER_INPUT",
			"source":     "USER_EXPLICIT",
			"status":     "DONE",
			"created_at": now.Add(-2 * time.Minute).Format(time.RFC3339),
			"content":    "fix the table border color",
		},
		map[string]any{
			"step_index": 1,
			"type":       "PLANNER_RESPONSE",
			"source":     "MODEL",
			"status":     "DONE",
			"created_at": now.Add(-1 * time.Minute).Format(time.RFC3339),
			"content":    "Updated border propagation in layout.ts.",
		},
	)
	n.state.put(agyPaneID, sessionState{Launcher: "agy", Cwd: cwd, Model: "gemini-4-neon-high", StartedAt: now.Add(-3 * time.Minute)})
	f.reply("session.snapshot", snapshot(nil, nil, []any{agyAgentRow(proto.StatusIdle, "w1:p1", "w1:t1", "w1", cwd, agySwitchConv)}, nil))
	f.setScreens("w1:p1", "? for shortcuts\n", "? for shortcuts\n")
	listSessions(t, n)
	f.reset()

	if err := pickFor(t, n, agyPaneID, map[string]any{"launcher": "claude", "from": "agy", "model": "opus"}); err != nil {
		t.Fatal(err)
	}
	ps := n.pendingSwitch(agyPaneID)
	if ps == nil || ps.Launcher != "claude" || ps.Agent != "claude" || ps.Model != "opus" {
		t.Fatalf("pending switch = %+v", ps)
	}

	quitsOn(f, "w1:p1", agyPaneID, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, agyPaneID))
	claudeComesUp(t, f, cwd, func(typed string) bool { return strings.Contains(typed, "claude") })
	f.reply("agent.prompt", map[string]any{})

	if err := tellTo(t, n, agyPaneID, "finish with claude"); err != nil {
		t.Fatal(err)
	}
	prompts := promptsTo(f)
	if len(prompts) != 1 {
		t.Fatalf("prompts = %d, want 1", len(prompts))
	}
	checkHandoffBlock(t, prompts[0], "agy", "claude", "opus", false, "finish with claude")
	if !strings.Contains(prompts[0], "fix the table border color") || !strings.Contains(prompts[0], "Updated border propagation in layout.ts.") {
		t.Fatalf("handoff did not carry agy transcript items:\n%s", prompts[0])
	}

	// Verify the new Claude row carries the durable agy-<uuid> leg in EngineHistory.
	rows := listSessions(t, n)
	var claudeRow *proto.Session
	for i := range rows {
		if rows[i].Agent == "claude" && rows[i].PaneID == "w1:p1" {
			claudeRow = &rows[i]
			break
		}
	}
	if claudeRow == nil {
		t.Fatalf("switched Claude session not listed in pane w1:p1")
	}
	wantLegID := agyAgent + "-" + agySwitchConv
	if legs := claudeRow.EngineHistory(); len(legs) != 1 || legs[0].SessionID != wantLegID || legs[0].Agent != agyAgent {
		t.Fatalf("EngineHistory = %+v, want leg %s", legs, wantLegID)
	}

	// Verify session.items for parked agy-<uuid> reads directly from disk even without a companion runtime.
	res, err := request(t, n, "session.items", map[string]any{"session_id": wantLegID})
	if err != nil {
		t.Fatalf("session.items(%s): %v", wantLegID, err)
	}
	raw, _ := json.Marshal(res)
	if !strings.Contains(string(raw), "Updated border propagation in layout.ts.") {
		t.Fatalf("parked agy session.items missing content: %s", raw)
	}
}

func TestEngineSwitchBackToParkedAgyResumesConversationWithDelta(t *testing.T) {
	n, f, home, cwd, _ := switchNode(t, proto.StatusIdle, longTurn)
	withAgyLauncher(t)

	until := time.Now().Add(-30 * time.Minute).UTC().Truncate(time.Second)
	writeAgySwitchTranscript(t, home, agySwitchConv,
		map[string]any{
			"step_index": 0,
			"type":       "USER_INPUT",
			"source":     "USER_EXPLICIT",
			"status":     "DONE",
			"created_at": until.Add(-5 * time.Minute).Format(time.RFC3339),
			"content":    "initial agy turn",
		},
	)
	parkedID := agyAgent + "-" + agySwitchConv
	n.state.put(parkedID, sessionState{Launcher: "agy", Cwd: cwd, StartedAt: until.Add(-5 * time.Minute)})
	n.state.setEngineHistory(sidA, []proto.EngineLeg{{SessionID: parkedID, Agent: agyAgent, Until: until}})

	if err := pick(t, n, map[string]any{"launcher": "agy", "model": "gemini-4-neon-high"}); err != nil {
		t.Fatal(err)
	}
	quitsOn(f, "w1:p1", sidA, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidA))
	cmds := agyComesUp(t, f, cwd, agySwitchConv, true)
	f.reply("agent.prompt", map[string]any{})
	f.reply("agent.get", map[string]any{"agent": agyAgentRow(proto.StatusWorking, "w1:p1", "w1:t1", "w1", cwd, agySwitchConv)})

	if err := tellTo(t, n, sidA, "back to agy"); err != nil {
		t.Fatal(err)
	}
	if len(cmds()) != 1 || !strings.Contains(cmds()[0], "--conversation "+agySwitchConv) {
		t.Fatalf("expected --conversation %s in %v", agySwitchConv, cmds())
	}
	prompts := promptsTo(f)
	if len(prompts) != 1 {
		t.Fatalf("prompts = %d, want 1", len(prompts))
	}
	checkHandoffBlock(t, prompts[0], "claude", "agy", "gemini-4-neon-high", true, "back to agy")
}

func TestEngineSwitchAgyFailureRollsBackToClaude(t *testing.T) {
	n, f, _, cwd, _ := switchNode(t, proto.StatusIdle, longTurn)
	withAgyLauncher(t)
	withStop(t, 1, time.Millisecond)

	if err := pick(t, n, map[string]any{"launcher": "agy", "model": "gemini-4-neon-high"}); err != nil {
		t.Fatal(err)
	}
	quitsOn(f, "w1:p1", sidA, nil, shellPane("w1:p1", "w1:t1", "w1", cwd, sidA))
	// Make agy start fail fast by returning a missing executable message on screen / never coming up while Claude comes back.
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" && str(c.Params["pane_id"]) == "w1:p1" && strings.Contains(str(c.Params["text"]), "HERDR_AGENT=agy") {
			f.setScreen("w1:p1", "CLI error: authentication required\n$ ")
		}
	})
	claudeComesUp(t, f, cwd, func(typed string) bool { return strings.Contains(typed, "--resume "+sidA) })

	err := tellTo(t, n, sidA, "should fail and roll back")
	if code, _ := refusalOf(t, err); code != proto.CodeSwitchFailed {
		t.Fatalf("err = %v, want %s", err, proto.CodeSwitchFailed)
	}
	if n.pendingSwitch(sidA) != nil {
		t.Fatalf("failed switch was not dropped")
	}
	if len(promptsTo(f)) != 0 {
		t.Fatalf("message should not be typed on failed switch: %v", promptsTo(f))
	}
}

func findRow(rows []proto.Session, id string) *proto.Session {
	for i := range rows {
		if rows[i].SessionID == id {
			return &rows[i]
		}
	}
	return nil
}
