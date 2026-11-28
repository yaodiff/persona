package node

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

func agyCompanionHello() map[string]any {
	return map[string]any{
		"runtime":  agyAgent,
		"name":     "Antigravity",
		"agent":    agyAgent,
		"launcher": agyAgent,
		"caps": map[string]any{
			"start":     true,
			"send":      true,
			"interrupt": true,
			"modes":     true,
			"readonly":  true,
			"assistant": true,
		},
		"launchers": []map[string]any{
			{"name": agyAgent, "agent": agyAgent, "models": []proto.AgentModel{{ID: "companion-model"}}},
		},
	}
}

// After a node restart (in-memory n.launches cleared), backfillResume must rebuild an agy pane's resume_argv
// with the persisted AppMode (e.g. --dangerously-skip-permissions for bypassPermissions, --mode accept-edits /
// --mode auto for explicit modes) and never escalate default, plan or legacy empty records.
func TestAgyBackfillResumePreservesPermissionMode(t *testing.T) {
	const (
		convID = "11111111-2222-4333-8444-555555555555"
		model  = "test-model"
		effort = "high"
	)
	for _, tc := range []struct {
		name     string
		appMode  string
		wantFlag string
	}{
		{"bypassPermissions", proto.ModeBypass, " --dangerously-skip-permissions"},
		{"acceptEdits", "acceptEdits", " --mode accept-edits"},
		{"auto", "auto", " --mode auto"},
		{"default", proto.ModeDefault, ""},
		{"plan_no_escalation", "plan", ""},
		{"legacy_empty_no_escalation", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home := herdrNode(t)
			agyTestFile(t, n.agyDataRoot(), convID, `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","content":"hi"}`+"\n")
			n.state.put(agyTestRow, sessionState{
				Pane:        agyTestPane,
				Launcher:    agyAgent,
				Cwd:         home,
				Model:       model,
				ModelEffort: effort,
				AppMode:     tc.appMode,
				StartedAt:   time.Now().UTC(),
			})

			// Simulate a fresh node process reconnecting to a live Herdr pane (n.launches is empty).
			f.reply("session.snapshot", snapshot(nil, nil, []any{agyTestAgent(agyTestPane, home, convID)}, nil))
			listSessions(t, n)
			waitFor(t, "agy backfill resume report", func() bool {
				return len(f.trace("pane.report_agent_session")) > 0
			})

			wantCmd := withHerdrAgent(resumeHerdrAgent(agyAgent), "agy --model "+model+" --effort "+effort+tc.wantFlag+" --conversation "+convID)
			wantArgv := inAgentEnv(n.dir, wantCmd, dquote)
			got := f.trace("pane.report_agent_session")
			if len(got) != 1 {
				t.Fatalf("backfill resume reports = %v, want 1", got)
			}
			var report struct {
				PaneID         string   `json:"pane_id"`
				Source         string   `json:"source"`
				Agent          string   `json:"agent"`
				AgentSessionID string   `json:"agent_session_id"`
				ResumeArgv     []string `json:"resume_argv"`
			}
			raw := strings.TrimPrefix(got[0], "pane.report_agent_session ")
			if err := json.Unmarshal([]byte(raw), &report); err != nil {
				t.Fatalf("unmarshal report %q: %v", got[0], err)
			}
			if report.PaneID != agyTestPane || report.Source != resumeSource || report.Agent != agyAgent ||
				report.AgentSessionID != convID || len(report.ResumeArgv) != 2 || report.ResumeArgv[0] != "eval" || report.ResumeArgv[1] != wantArgv {
				t.Fatalf("backfill resume report:\ngot:  %+v\nwant argv: %s", report, wantArgv)
			}

			// session.info on the pane must also preserve st.AppMode when recorded.
			info := agyTestCall(t, n, "session.info", agyTestRow, nil).(map[string]any)
			gotMode, _ := info["permission_mode"].(string)
			if gotMode != tc.appMode {
				t.Fatalf("pane session.info permission_mode = %q, want %q", gotMode, tc.appMode)
			}
		})
	}
}

func TestAgyBackfillResumeDoesNotAffectOtherAgents(t *testing.T) {
	for _, kind := range []string{"", "claude", "codex", piAgent, cursorAgent, traexAgent, ompAgent} {
		if got := agyBackfillMode(kind, proto.ModeBypass); got != "" {
			t.Errorf("agyBackfillMode(%q, bypassPermissions) = %q, want empty", kind, got)
		}
	}
	if got := agyBackfillMode(agyAgent, proto.ModeBypass); got != proto.ModeBypass {
		t.Errorf("agyBackfillMode(agy, bypassPermissions) = %q", got)
	}

	// A pi pane with AppMode in state must never receive agy's --dangerously-skip-permissions flag on backfill.
	n, f, home := herdrNode(t)
	const piID = "11111111-2222-4333-8444-555555555556"
	n.state.put(piID, sessionState{
		Pane:      "w1:p3",
		Launcher:  piAgent,
		Cwd:       home,
		Model:     "pi-model",
		AppMode:   proto.ModeBypass,
		StartedAt: time.Now().UTC(),
	})
	f.reply("session.snapshot", snapshot(nil, nil, []any{agentOf(piAgent, piID, proto.StatusIdle, "w1:p3", "w1:t1", "w1", home, piAgent)}, nil))
	listSessions(t, n)
	waitFor(t, "pi backfill resume report", func() bool {
		return len(f.trace("pane.report_agent_session")) > 0
	})
	if got := f.trace("pane.report_agent_session")[0]; strings.Contains(got, "--dangerously-skip-permissions") {
		t.Fatalf("pi backfill unexpectedly carried agy bypass flag: %s", got)
	}
}

// When a Persona-started agy pane gets a verified conversation ID and later stops or disappears on reboot,
// continuing the conversation through the companion row (agy-<uuid>) must keep the original permission_mode
// instead of falling back to local settings.json. Legacy empty-mode and user-started panes must not mirror.
func TestAgyPaneConversationMirrorsPermissionModeToCompanion(t *testing.T) {
	const (
		convBypass = "22222222-3333-4444-8555-666666666661"
		convLegacy = "22222222-3333-4444-8555-666666666662"
		convUser   = "22222222-3333-4444-8555-666666666663"
	)
	n, f, home := herdrNode(t)
	n.logger = log.New(io.Discard, "", 0)
	n.runtimeSock = filepath.Join(filepath.Dir(f.sock), "runtime.sock")
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	if _, err := n.listenRuntimeSocket(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{convBypass, convLegacy, convUser} {
		agyTestFile(t, n.agyDataRoot(), id, `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","content":"work"}`+"\n")
	}

	// Start/record one bypassPermissions pane, one legacy empty-mode pane, and one user-started pane (not in n.state).
	h := n.hosts.Get(host.Herdr).(*herdrHost)
	seedAgents(t, n, f,
		agyTestAgent("w1:p1", home, convBypass),
		agyTestAgent("w1:p2", home, convLegacy),
		agyTestAgent("w1:p3", home, convUser),
	)
	n.noteWorkAgent(ctx, h, "w1:p1", "agy-w1-p1", home, agyAgent, agyAgent, "test-model", "", proto.ModeBypass)
	n.noteWorkAgent(ctx, h, "w1:p2", "agy-w1-p2", home, agyAgent, agyAgent, "test-model", "", "")
	waitFor(t, "both Persona agy panes reported", func() bool {
		return len(f.trace("pane.report_agent_session")) >= 2
	})

	// Verify only the bypassPermissions conversation was mirrored, with no Pane bound.
	if st, ok := n.state.get(agyAgent + "-" + convBypass); !ok || st.AppMode != proto.ModeBypass || st.Pane != "" {
		t.Fatalf("mirrored bypass state = %+v (ok=%v)", st, ok)
	}
	if _, ok := n.state.get(agyAgent + "-" + convLegacy); ok {
		t.Fatal("legacy empty-AppMode pane unexpectedly created companion state")
	}
	if _, ok := n.state.get(agyAgent + "-" + convUser); ok {
		t.Fatal("user-started pane unexpectedly created companion state")
	}

	// Simulate reboot / pane stop: Herdr panes are gone and companion lists the conversations with local settings default.
	seedAgents(t, n, f)
	companion := dialRuntime(t, n, agyCompanionHello())
	companion.mu.Lock()
	companion.answer = func(method string, _ json.RawMessage) (any, error) {
		return map[string]any{"ok": true}, nil
	}
	companion.mu.Unlock()
	_ = companion.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": agyAgent + "-" + convBypass, "agent": agyAgent, "cwd": home, "status": "idle", "launcher": agyAgent},
		{"session_id": agyAgent + "-" + convLegacy, "agent": agyAgent, "cwd": home, "status": "idle", "launcher": agyAgent},
		{"session_id": agyAgent + "-" + convUser, "agent": agyAgent, "cwd": home, "status": "idle", "launcher": agyAgent},
	}})
	waitFor(t, "companion rows listed", func() bool {
		_, ok := listed(t, n)[agyAgent+"-"+convBypass]
		return ok
	})

	rows := listed(t, n)
	if got := rows[agyAgent+"-"+convBypass].PermissionMode; got != proto.ModeBypass {
		t.Fatalf("companion row mode after pane stop = %q, want %q", got, proto.ModeBypass)
	}
	if got := rows[agyAgent+"-"+convLegacy].PermissionMode; got != "" {
		t.Fatalf("legacy companion row mode = %q, want empty", got)
	}
	if got := rows[agyAgent+"-"+convUser].PermissionMode; got != "" {
		t.Fatalf("user companion row mode = %q, want empty", got)
	}

	for _, id := range []string{agyAgent + "-" + convBypass, agyAgent + "-" + convLegacy, agyAgent + "-" + convUser} {
		if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": id, "text": "continue", "client_msg_id": "m-" + id}); err != nil {
			t.Fatalf("session.send %s: %v", id, err)
		}
	}
	sends := companion.got("session.send")
	if len(sends) != 3 {
		t.Fatalf("companion sends = %d, want 3", len(sends))
	}
	gotByID := map[string]map[string]any{}
	for _, raw := range sends {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		gotByID[m["session_id"].(string)] = m
	}
	if got := gotByID[agyAgent+"-"+convBypass]["mode"]; got != proto.ModeBypass {
		t.Fatalf("bypass companion send mode = %v, want %q", got, proto.ModeBypass)
	}
	if _, has := gotByID[agyAgent+"-"+convLegacy]["mode"]; has {
		t.Fatalf("legacy companion send unexpectedly set mode: %v", gotByID[agyAgent+"-"+convLegacy])
	}
	if _, has := gotByID[agyAgent+"-"+convUser]["mode"]; has {
		t.Fatalf("user companion send unexpectedly set mode: %v", gotByID[agyAgent+"-"+convUser])
	}
}

// Companion agy sessions started with an explicit permission_mode must preserve that mode across
// companion/node/machine restarts on sessions.list, session.info and session.send without falling back to
// local settings.json (including never escalating read-only plan or explicit default when settings.json has
// always-proceed). Settings-derived starts (empty permission_mode) keep following settings.json, session.modes
// updates persisted state for Persona-started sessions only, and non-agy runtimes are unaffected.
func TestAgyCompanionPreservesPermissionModeAcrossRestart(t *testing.T) {
	for _, tc := range []struct {
		name               string
		startMode          string
		startReturnedMode  string
		afterRestartRowMod string // what fresh companion reports from local settings.json after restart
		wantListedMode     string
		wantSendMode       string
		wantStateRecorded  bool
	}{
		{
			name:               "bypassPermissions survives ask settings",
			startMode:          proto.ModeBypass,
			startReturnedMode:  proto.ModeBypass,
			afterRestartRowMod: "",
			wantListedMode:     proto.ModeBypass,
			wantSendMode:       proto.ModeBypass,
			wantStateRecorded:  true,
		},
		{
			name:               "acceptEdits survives ask settings",
			startMode:          "acceptEdits",
			startReturnedMode:  "acceptEdits",
			afterRestartRowMod: "",
			wantListedMode:     "acceptEdits",
			wantSendMode:       "acceptEdits",
			wantStateRecorded:  true,
		},
		{
			name:               "explicit default not escalated by always-proceed settings",
			startMode:          proto.ModeDefault,
			startReturnedMode:  "",
			afterRestartRowMod: proto.ModeBypass,
			wantListedMode:     proto.ModeDefault,
			wantSendMode:       proto.ModeDefault,
			wantStateRecorded:  true,
		},
		{
			name:               "read-only plan not escalated by always-proceed settings",
			startMode:          "plan",
			startReturnedMode:  "plan",
			afterRestartRowMod: proto.ModeBypass,
			wantListedMode:     "plan",
			wantSendMode:       "plan",
			wantStateRecorded:  true,
		},
		{
			name:               "refused auto fallback persisted as bypassPermissions",
			startMode:          "auto",
			startReturnedMode:  proto.ModeBypass,
			afterRestartRowMod: "",
			wantListedMode:     proto.ModeBypass,
			wantSendMode:       proto.ModeBypass,
			wantStateRecorded:  true,
		},
		{
			name:               "unset mode stays settings-derived",
			startMode:          "",
			startReturnedMode:  "",
			afterRestartRowMod: "acceptEdits",
			wantListedMode:     "acceptEdits",
			wantSendMode:       "",
			wantStateRecorded:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, _ := runtimeNode(t)
			const sid = "agy-33333333-4444-4555-8666-777777777777"
			first := dialRuntime(t, n, agyCompanionHello())
			first.mu.Lock()
			first.answer = func(method string, _ json.RawMessage) (any, error) {
				if method == "session.start" {
					return map[string]any{"session": proto.Session{
						SessionID:      sid,
						Agent:          agyAgent,
						Cwd:            n.home,
						Status:         proto.StatusIdle,
						Launcher:       agyAgent,
						PermissionMode: tc.startReturnedMode,
					}}, nil
				}
				return map[string]any{"ok": true}, nil
			}
			first.mu.Unlock()

			startParams := map[string]any{"cwd": n.home, "launcher": agyAgent, "host": agyAgent}
			if tc.startMode != "" {
				startParams["permission_mode"] = tc.startMode
			}
			if _, err := nodeCall(t, n, "session.start", startParams); err != nil {
				t.Fatalf("session.start: %v", err)
			}
			_, recorded := n.state.get(sid)
			if recorded != tc.wantStateRecorded {
				t.Fatalf("state recorded = %v, want %v", recorded, tc.wantStateRecorded)
			}

			// Simulate companion/machine restart: first companion disconnects, a fresh companion connects with
			// empty in-memory sessionModes and reports whatever local settings.json says.
			_ = first.conn.Close()
			waitFor(t, "first companion disconnected", func() bool { return runtimeGone(n, agyAgent) })

			restarted := dialRuntime(t, n, agyCompanionHello())
			restarted.mu.Lock()
			restarted.answer = func(method string, _ json.RawMessage) (any, error) {
				return map[string]any{"ok": true}, nil
			}
			restarted.mu.Unlock()
			row := map[string]any{
				"session_id": sid, "agent": agyAgent, "cwd": n.home, "status": "idle", "launcher": agyAgent,
			}
			if tc.afterRestartRowMod != "" {
				row["permission_mode"] = tc.afterRestartRowMod
			}
			_ = restarted.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{row}})
			waitFor(t, "restarted companion listed session", func() bool {
				_, ok := listed(t, n)[sid]
				return ok
			})

			if got := listed(t, n)[sid].PermissionMode; got != tc.wantListedMode {
				t.Fatalf("sessions.list permission_mode = %q, want %q", got, tc.wantListedMode)
			}
			infoRaw, err := nodeCall(t, n, "session.info", map[string]any{"session_id": sid})
			if err != nil {
				t.Fatalf("session.info: %v", err)
			}
			var info map[string]any
			_ = json.Unmarshal(infoRaw, &info)
			if got, _ := info["permission_mode"].(string); got != tc.wantListedMode {
				t.Fatalf("session.info permission_mode = %q, want %q", got, tc.wantListedMode)
			}

			if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": sid, "text": "resume work", "client_msg_id": "m-restart"}); err != nil {
				t.Fatalf("session.send: %v", err)
			}
			sends := restarted.got("session.send")
			if len(sends) != 1 {
				t.Fatalf("session.send count = %d, want 1", len(sends))
			}
			var sent map[string]any
			_ = json.Unmarshal(sends[0], &sent)
			gotSendMode, hasSendMode := sent["mode"].(string)
			if tc.wantSendMode == "" {
				if hasSendMode {
					t.Fatalf("session.send unexpectedly included mode %q", gotSendMode)
				}
			} else if gotSendMode != tc.wantSendMode {
				t.Fatalf("session.send mode = %q, want %q", gotSendMode, tc.wantSendMode)
			}
		})
	}
}

func TestAgyCompanionSessionModesUpdatesPersistedModeAndSkipsExternalAndOtherRuntimes(t *testing.T) {
	n, _ := runtimeNode(t)
	const (
		personaSID  = "agy-44444444-5555-4666-8777-888888888881"
		externalSID = "agy-44444444-5555-4666-8777-888888888882"
		paseoSID    = "paseo-other-1"
	)

	first := dialRuntime(t, n, agyCompanionHello())
	first.mu.Lock()
	first.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": proto.Session{
				SessionID: personaSID, Agent: agyAgent, Cwd: n.home, Status: proto.StatusIdle, Launcher: agyAgent, PermissionMode: proto.ModeBypass,
			}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	first.mu.Unlock()

	if _, err := nodeCall(t, n, "session.start", map[string]any{
		"cwd": n.home, "launcher": agyAgent, "host": agyAgent, "permission_mode": proto.ModeBypass,
	}); err != nil {
		t.Fatalf("session.start: %v", err)
	}
	_ = first.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": personaSID, "agent": agyAgent, "cwd": n.home, "status": "idle", "launcher": agyAgent, "permission_mode": proto.ModeBypass},
		{"session_id": externalSID, "agent": agyAgent, "cwd": n.home, "status": "idle", "launcher": agyAgent},
	}})
	waitFor(t, "both agy sessions listed", func() bool {
		rows := listed(t, n)
		_, ok1 := rows[personaSID]
		_, ok2 := rows[externalSID]
		return ok1 && ok2
	})

	// Explicit mode change updates Persona's persisted state, but never creates state for an external session.
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": personaSID, "permission_mode": "acceptEdits"}); err != nil {
		t.Fatalf("session.modes persona: %v", err)
	}
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": externalSID, "permission_mode": "acceptEdits"}); err != nil {
		t.Fatalf("session.modes external: %v", err)
	}
	if st, ok := n.state.get(personaSID); !ok || st.AppMode != "acceptEdits" {
		t.Fatalf("persona state after session.modes = %+v (ok=%v)", st, ok)
	}
	if _, ok := n.state.get(externalSID); ok {
		t.Fatal("session.modes unexpectedly created state for external user session")
	}

	// Restart companion and verify personaSID replays acceptEdits while externalSID omits mode.
	_ = first.conn.Close()
	waitFor(t, "first companion gone", func() bool { return runtimeGone(n, agyAgent) })
	second := dialRuntime(t, n, agyCompanionHello())
	second.mu.Lock()
	second.answer = func(method string, _ json.RawMessage) (any, error) {
		return map[string]any{"ok": true}, nil
	}
	second.mu.Unlock()
	_ = second.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": personaSID, "agent": agyAgent, "cwd": n.home, "status": "idle", "launcher": agyAgent},
		{"session_id": externalSID, "agent": agyAgent, "cwd": n.home, "status": "idle", "launcher": agyAgent},
	}})
	waitFor(t, "restarted sessions listed", func() bool { _, ok := listed(t, n)[personaSID]; return ok })

	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": personaSID, "text": "go", "client_msg_id": "m1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": externalSID, "text": "go", "client_msg_id": "m2"}); err != nil {
		t.Fatal(err)
	}
	var sentPersona, sentExternal map[string]any
	for _, raw := range second.got("session.send") {
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		if m["session_id"] == personaSID {
			sentPersona = m
		} else if m["session_id"] == externalSID {
			sentExternal = m
		}
	}
	if sentPersona["mode"] != "acceptEdits" {
		t.Fatalf("persona send after mode change + restart = %v, want mode acceptEdits", sentPersona)
	}
	if _, has := sentExternal["mode"]; has {
		t.Fatalf("external send unexpectedly forced mode: %v", sentExternal)
	}

	// Non-agy runtime (Paseo) must never get a mode field injected on session.send even if n.state has AppMode.
	paseo := dialRuntime(t, n, paseoEngineHello())
	paseo.mu.Lock()
	paseo.answer = func(method string, _ json.RawMessage) (any, error) {
		return map[string]any{"ok": true}, nil
	}
	paseo.mu.Unlock()
	n.state.put(paseoSID, sessionState{Launcher: "paseo-claude", Cwd: n.home, AppMode: proto.ModeBypass, StartedAt: time.Now().UTC()})
	_ = paseo.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": paseoSID, "agent": "claude", "cwd": n.home, "status": "idle", "launcher": "paseo-claude"},
	}})
	waitFor(t, "paseo session listed", func() bool { _, ok := listed(t, n)[paseoSID]; return ok })
	if _, err := nodeCall(t, n, "session.send", map[string]any{"session_id": paseoSID, "text": "ping", "client_msg_id": "m3"}); err != nil {
		t.Fatal(err)
	}
	var sentPaseo map[string]any
	_ = json.Unmarshal(paseo.got("session.send")[0], &sentPaseo)
	if _, has := sentPaseo["mode"]; has {
		t.Fatalf("non-agy runtime send unexpectedly included mode: %v", sentPaseo)
	}
}

func TestAgyAssistantModeOfSurvivesNodeRestart(t *testing.T) {
	n, hub := runtimeNode(t)
	assistantHub(hub)
	const sid = "agy-55555555-6666-4777-8888-999999999999"
	dir := filepath.Join(n.home, ".persona", "assistant")
	companion := dialRuntime(t, n, agyCompanionHello())
	companion.mu.Lock()
	companion.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": map[string]any{"session_id": sid, "agent": agyAgent, "cwd": dir, "status": "idle", "launcher": agyAgent}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	companion.mu.Unlock()

	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"launcher": agyAgent, "cwd": dir, "text": "first turn", "permission_mode": proto.ModeBypass,
	}); err != nil {
		t.Fatalf("initial assistant.send: %v", err)
	}

	// Simulate node restart: clear in-memory n.modes while keeping persisted n.state.
	n.mu.Lock()
	n.modes = map[string]sessionModes{}
	n.mu.Unlock()

	if got := n.assistantModeOf(sid); got != proto.ModeBypass {
		t.Fatalf("assistantModeOf after restart = %q, want %q", got, proto.ModeBypass)
	}
	_ = companion.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": sid, "agent": agyAgent, "cwd": dir, "status": "idle", "launcher": agyAgent},
	}})
	waitFor(t, "assistant row listed with restored mode", func() bool {
		return listed(t, n)[sid].PermissionMode == proto.ModeBypass
	})

	if _, err := nodeCall(t, n, "assistant.send", map[string]any{
		"session_id": sid, "launcher": agyAgent, "cwd": dir, "text": "second turn",
	}); err != nil {
		t.Fatalf("follow-up assistant.send: %v", err)
	}
	sends := companion.got("session.send")
	if len(sends) != 2 {
		t.Fatalf("assistant sends = %d, want 2", len(sends))
	}
	var second map[string]any
	_ = json.Unmarshal(sends[1], &second)
	if second["mode"] != proto.ModeBypass {
		t.Fatalf("follow-up assistant send mode = %v, want %q", second["mode"], proto.ModeBypass)
	}
}

func TestTakeOverInRuntimeFillsPermissionModeIndependentlyOfEffort(t *testing.T) {
	n, _ := runtimeNode(t)
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	hello := paseoEngineHello()
	hello["caps"].(map[string]any)["readonly"] = true
	host := dialRuntime(t, n, hello)
	host.mu.Lock()
	host.answer = func(method string, params json.RawMessage) (any, error) {
		var p struct {
			Resume string `json:"resume"`
		}
		_ = json.Unmarshal(params, &p)
		return map[string]any{"session": map[string]any{"session_id": p.Resume, "agent": "claude", "status": "idle", "cwd": n.home}}, nil
	}
	host.mu.Unlock()

	sdkTranscript(t, n, sdkSession, "prior work")
	n.state.put(sdkSession, sessionState{
		Launcher:  "paseo-claude",
		Cwd:       n.home,
		AppMode:   "plan",
		AppEffort: "low",
		StartedAt: time.Now().UTC(),
	})

	if _, err := nodeCall(t, n, "session.start", map[string]any{
		"cwd":               n.home,
		"resume_session_id": sdkSession,
		"effort":            "high",
	}); err != nil {
		t.Fatalf("session.start resume with explicit effort: %v", err)
	}
	starts := host.got("session.start")
	if len(starts) != 1 {
		t.Fatalf("paseo starts = %d, want 1", len(starts))
	}
	var start map[string]any
	_ = json.Unmarshal(starts[0], &start)
	if start["permission_mode"] != "plan" || start["effort"] != "high" {
		t.Fatalf("takeover start = %v, want permission_mode=plan effort=high", start)
	}
}

func TestAgyCompanionModeHelpersFallbackAware(t *testing.T) {
	for _, tc := range []struct {
		name      string
		rowMode   string
		savedMode string
		wantShown string
		wantSend  string
	}{
		{"live auto-to-bypass fallback omits send mode", proto.ModeBypass, "auto", proto.ModeBypass, ""},
		{"normal auto with auto row replays auto", "auto", "auto", "auto", "auto"},
		{"normal auto survives empty row after restart", "", "auto", "auto", "auto"},
		{"normal auto survives default row after restart", proto.ModeDefault, "auto", "auto", "auto"},
		{"normal auto survives acceptEdits row after restart", "acceptEdits", "auto", "auto", "auto"},
		{"normal auto survives plan row after restart", "plan", "auto", "auto", "auto"},
		{"saved bypassPermissions always replays", "", proto.ModeBypass, proto.ModeBypass, proto.ModeBypass},
		{"saved bypassPermissions with bypass row replays", proto.ModeBypass, proto.ModeBypass, proto.ModeBypass, proto.ModeBypass},
		{"saved acceptEdits never escalated by bypass row", proto.ModeBypass, "acceptEdits", "acceptEdits", "acceptEdits"},
		{"saved plan never escalated by bypass row", proto.ModeBypass, "plan", "plan", "plan"},
		{"saved default never escalated by bypass row", proto.ModeBypass, proto.ModeDefault, proto.ModeDefault, proto.ModeDefault},
		{"unset saved mode omits send mode", proto.ModeBypass, "", proto.ModeBypass, ""},
		{"unset saved mode with empty row", "", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := agyShownMode(tc.rowMode, tc.savedMode); got != tc.wantShown {
				t.Errorf("agyShownMode(%q, %q) = %q, want %q", tc.rowMode, tc.savedMode, got, tc.wantShown)
			}
			if got := agySendMode(tc.rowMode, tc.savedMode); got != tc.wantSend {
				t.Errorf("agySendMode(%q, %q) = %q, want %q", tc.rowMode, tc.savedMode, got, tc.wantSend)
			}
		})
	}
}

// Replaying saved "auto" on session.send while the companion row already reports bypassPermissions would
// re-trigger the companion's auto-to-bypass fallback on every turn. Omit mode in that live fallback state
// while continuing to replay "auto" for normal auto sessions and continuing to replay non-auto modes even
// when a fresh companion reports bypassPermissions from local settings.json.
func TestAgyCompanionSendOmitsModeOnLiveAutoBypassFallback(t *testing.T) {
	n, _ := runtimeNode(t)
	const sid = "agy-66666666-7777-4888-8999-aaaaaaaaaaaa"

	companion := dialRuntime(t, n, agyCompanionHello())
	companion.mu.Lock()
	companion.answer = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.start" {
			return map[string]any{"session": proto.Session{
				SessionID:      sid,
				Agent:          agyAgent,
				Cwd:            n.home,
				Status:         proto.StatusIdle,
				Launcher:       agyAgent,
				PermissionMode: "auto",
			}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	companion.mu.Unlock()

	if _, err := nodeCall(t, n, "session.start", map[string]any{
		"cwd": n.home, "launcher": agyAgent, "host": agyAgent, "permission_mode": "auto",
	}); err != nil {
		t.Fatalf("session.start: %v", err)
	}
	if st, ok := n.state.get(sid); !ok || st.AppMode != "auto" {
		t.Fatalf("persisted state after auto start = %+v (ok=%v), want AppMode=auto", st, ok)
	}

	publishRow := func(rowMode string) {
		t.Helper()
		row := map[string]any{"session_id": sid, "agent": agyAgent, "cwd": n.home, "status": "idle", "launcher": agyAgent}
		if rowMode != "" {
			row["permission_mode"] = rowMode
		}
		_ = companion.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{row}})
		waitFor(t, "companion row mode "+rowMode, func() bool {
			row, _, ok := n.runtimes.rowOf(sid)
			return ok && row.PermissionMode == rowMode
		})
	}
	sendAndGetLast := func(clientMsgID string) map[string]any {
		t.Helper()
		before := len(companion.got("session.send"))
		if _, err := nodeCall(t, n, "session.send", map[string]any{
			"session_id": sid, "text": "turn " + clientMsgID, "client_msg_id": clientMsgID,
		}); err != nil {
			t.Fatalf("session.send %s: %v", clientMsgID, err)
		}
		sends := companion.got("session.send")
		if len(sends) != before+1 {
			t.Fatalf("session.send count = %d, want %d", len(sends), before+1)
		}
		var sent map[string]any
		_ = json.Unmarshal(sends[len(sends)-1], &sent)
		return sent
	}
	sessionInfoMode := func() string {
		t.Helper()
		raw, err := nodeCall(t, n, "session.info", map[string]any{"session_id": sid})
		if err != nil {
			t.Fatalf("session.info: %v", err)
		}
		var info map[string]any
		_ = json.Unmarshal(raw, &info)
		mode, _ := info["permission_mode"].(string)
		return mode
	}

	// 1. Normal auto session (row reports empty/default after restart or "auto"): replays mode "auto".
	publishRow("")
	if got := listed(t, n)[sid].PermissionMode; got != "auto" {
		t.Fatalf("sessions.list with empty row = %q, want auto", got)
	}
	if got := sessionInfoMode(); got != "auto" {
		t.Fatalf("session.info with empty row = %q, want auto", got)
	}
	if sent := sendAndGetLast("m-auto-empty-row"); sent["mode"] != "auto" {
		t.Fatalf("session.send with empty row = %v, want mode=auto", sent)
	}

	publishRow("auto")
	if sent := sendAndGetLast("m-auto-live"); sent["mode"] != "auto" {
		t.Fatalf("session.send with auto row = %v, want mode=auto", sent)
	}

	// 2. Companion falls back from auto to bypassPermissions: list/info show bypassPermissions and send omits mode.
	publishRow(proto.ModeBypass)
	if got := listed(t, n)[sid].PermissionMode; got != proto.ModeBypass {
		t.Fatalf("sessions.list during auto fallback = %q, want %q", got, proto.ModeBypass)
	}
	if got := sessionInfoMode(); got != proto.ModeBypass {
		t.Fatalf("session.info during auto fallback = %q, want %q", got, proto.ModeBypass)
	}
	sentFallback := sendAndGetLast("m-auto-fallback")
	if _, has := sentFallback["mode"]; has {
		t.Fatalf("session.send during auto-to-bypass fallback unexpectedly replayed mode: %v", sentFallback)
	}
	if st, ok := n.state.get(sid); !ok || st.AppMode != "auto" {
		t.Fatalf("persisted state after fallback send = %+v (ok=%v), want AppMode=auto", st, ok)
	}

	// 3. Switching to a non-auto mode (plan) still replays plan even if the companion row reports bypassPermissions.
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": sid, "permission_mode": "plan"}); err != nil {
		t.Fatalf("session.modes plan: %v", err)
	}
	if sent := sendAndGetLast("m-plan-over-bypass-row"); sent["mode"] != "plan" {
		t.Fatalf("session.send after switching to plan = %v, want mode=plan", sent)
	}

	// 4. Switching back to auto via session.modes while companion reports bypassPermissions fallback omits mode.
	if _, err := nodeCall(t, n, "session.modes", map[string]any{"session_id": sid, "permission_mode": "auto"}); err != nil {
		t.Fatalf("session.modes auto: %v", err)
	}
	sentModesFallback := sendAndGetLast("m-modes-auto-fallback")
	if _, has := sentModesFallback["mode"]; has {
		t.Fatalf("session.send after session.modes(auto) fallback unexpectedly replayed mode: %v", sentModesFallback)
	}
}
