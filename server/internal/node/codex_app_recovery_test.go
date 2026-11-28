package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func writeCodexAppFinishedRollout(t *testing.T, codexHome, threadID, cwd string, age time.Duration, extraLines ...string) string {
	t.Helper()
	at := time.Now().Add(-age).UTC()
	dir := mkdir(t, filepath.Join(codexHome, "sessions", at.Format("2006"), at.Format("01"), at.Format("02")))
	meta, _ := json.Marshal(map[string]any{
		"timestamp": at.Add(-2 * time.Minute).Format(time.RFC3339Nano),
		"type":      "session_meta",
		"payload": map[string]any{
			"id":            threadID,
			"cwd":           cwd,
			"originator":    "Codex Desktop",
			"source":        "vscode",
			"thread_source": "user",
		},
	})
	userMsg, _ := json.Marshal(map[string]any{
		"timestamp": at.Add(-90 * time.Second).Format(time.RFC3339Nano),
		"type":      "event_msg",
		"payload": map[string]any{
			"type": "item_completed",
			"item": map[string]any{
				"type":    "UserMessage",
				"id":      "u1",
				"content": []any{map[string]any{"type": "text", "text": "Open a draft MR for the sync fix."}},
			},
		},
	})
	assistantMsg, _ := json.Marshal(map[string]any{
		"timestamp": at.Add(-30 * time.Second).Format(time.RFC3339Nano),
		"type":      "event_msg",
		"payload": map[string]any{
			"type": "item_completed",
			"item": map[string]any{
				"type":    "AgentMessage",
				"id":      "a1",
				"phase":   "final_answer",
				"content": []any{map[string]any{"type": "text", "text": "Draft MR is ready for follow-up."}},
			},
		},
	})
	lines := []string{
		string(meta),
		`{"timestamp":"` + at.Add(-95*time.Second).Format(time.RFC3339Nano) + `","type":"event_msg","payload":{"type":"task_started"}}`,
		string(userMsg),
		string(assistantMsg),
	}
	if len(extraLines) == 0 {
		lines = append(lines, `{"timestamp":"`+at.Format(time.RFC3339Nano)+`","type":"event_msg","payload":{"type":"task_complete"}}`)
	} else {
		lines = append(lines, extraLines...)
	}
	path := filepath.Join(dir, "rollout-"+at.Format("2006-01-02T15-04-05")+"-"+threadID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	return path
}

func codexAppRecoveryNode(t *testing.T, listed bool, extraLines ...string) (*Node, *fakeHerdr, string, string) {
	t.Helper()
	codexResumes.Delete(startThread)
	t.Cleanup(func() { codexResumes.Delete(startThread) })
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	codexHome := filepath.Join(home, ".codex")
	path := writeCodexAppFinishedRollout(t, codexHome, startThread, cwd, 4*time.Hour, extraLines...)
	if listed {
		if err := os.WriteFile(filepath.Join(codexHome, ".codex-global-state.json"),
			[]byte(`{"projectless-thread-ids":["`+startThread+`"]}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	n.codices = newCodexRegistry()
	n.codices.scan = func() []codexProcess {
		// Unrelated CLI / assistant app-servers (real live PID holding no rollout) must not block idle App thread recovery.
		return []codexProcess{{pid: os.Getpid(), home: filepath.Join(home, ".other-codex"), endpoint: "stdio://"}}
	}
	n.codices.checked = func() ([]codexProcess, bool) { return n.codices.scan(), true }
	n.configureCodex(proto.CodexConfig{})
	n.discoverCodex()
	return n, f, cwd, path
}

func TestCodexAppIdleUnownedOffersContinueTakeoverAndResume(t *testing.T) {
	for _, listed := range []bool{false, true} {
		name := "unlisted_desktop_rollout"
		if listed {
			name = "sidebar_listed_app_chat"
		}
		t.Run(name, func(t *testing.T) {
			n, f, cwd, path := codexAppRecoveryNode(t, listed)
			row, ok := byID(listSessions(t, n))[startThread]
			if !ok {
				t.Fatal("Codex App thread missing from session list")
			}
			if row.SourceKind != "codex_history" || row.Capabilities == nil || row.Capabilities.Send {
				t.Fatalf("unexpected source/send: %+v (%+v)", row, row.Capabilities)
			}
			if !row.Capabilities.Terminal || row.Capabilities.StopCause != proto.StopTaskCompleted {
				t.Fatalf("idle unowned App thread should be resumable with StopTaskCompleted: %+v", row.Capabilities)
			}
			if row.Capabilities.Takeover == nil || row.Capabilities.Takeover.Running || !row.Capabilities.Takeover.CanResume || !row.Capabilities.Takeover.AppOwned {
				t.Fatalf("takeover capability = %+v", row.Capabilities.Takeover)
			}
			if want := i18n.T(i18n.Default, codexDesktopRecoverableKey); row.ReadOnlyReason != want {
				t.Fatalf("read_only_reason = %q, want %q", row.ReadOnlyReason, want)
			}

			// Plain send still refuses with agent_stopped without launching a terminal.
			f.reset()
			if _, err := request(t, n, "session.send", map[string]any{"session_id": startThread, "text": "un-draft the MR"}); refusalCode(err) != agentStopped {
				t.Fatalf("plain send to idle App thread = %v, want %s", err, agentStopped)
			}
			if calls := f.trace("tab.create", "pane.send_input"); len(calls) != 0 {
				t.Fatalf("plain send touched terminal: %v", calls)
			}

			// 1. Continue in a new session prepares cwd, transcript path, and bounded summary without touching source.
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
			plan, err := n.prepareCodexTakeover(context.Background(), startThread)
			if err != nil {
				t.Fatal(err)
			}
			if !plan.CanResume || !plan.AppOwned || plan.Running || plan.Cwd != cwd || plan.TranscriptPath == "" {
				t.Fatalf("unexpected takeover plan: %+v", plan)
			}
			if !strings.Contains(plan.ContinuationPrompt, plan.TranscriptPath) || !strings.Contains(plan.ContinuationPrompt, "Draft MR is ready for follow-up.") {
				t.Fatalf("continuation prompt missing transcript or summary: %q", plan.ContinuationPrompt)
			}
			after, _ := os.ReadFile(path)
			if !slices.Equal(before, after) {
				t.Fatal("preparing continuation modified original rollout")
			}

			// 2. Take over requires explicit confirmation that the App will no longer own the chat.
			var sharedCalls int
			n.takeovers.shared = func(context.Context, codexSource) ([]string, error) {
				sharedCalls++
				return []string{"codex", "--remote", "unix:///shared/socket"}, nil
			}
			if _, err := n.commitCodexTakeover(context.Background(), startThread, plan.Token, false); refusalCode(err) != "codex_takeover_app_confirmation_required" || sharedCalls != 0 {
				t.Fatalf("unconfirmed App takeover = %v (sharedCalls=%d)", err, sharedCalls)
			}

			// Confirmed takeover resumes the thread in a Persona-managed terminal on the shared app-server.
			plan, err = n.prepareCodexTakeover(context.Background(), startThread)
			if err != nil {
				t.Fatal(err)
			}
			file := resumeFile(f, cwd)
			f.setScreen("w1:p2", cxReady)
			f.after(func(c herdrCall) {
				if c.Method == "pane.send_input" && strings.Contains(str(c.Params["text"]), "codex-start") {
					takeoverTestOwner(n)
				}
			})
			session, err := n.commitCodexTakeover(context.Background(), startThread, plan.Token, true)
			if err != nil {
				t.Fatalf("confirmed App takeover failed: %v", err)
			}
			if session.SessionID != startThread || sharedCalls != 1 {
				t.Fatalf("session=%+v sharedCalls=%d", session, sharedCalls)
			}
			if want := []string{"codex", "--remote", "unix:///shared/socket", "-C", cwd, "resume", startThread}; !slices.Equal(file().Argv, want) {
				t.Fatalf("argv=%v, want %v", file().Argv, want)
			}
		})
	}
}

func TestCodexAppTakeoverAndResumePreserveCustomCodexHome(t *testing.T) {
	codexResumes.Delete(startThread)
	t.Cleanup(func() { codexResumes.Delete(startThread) })
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	customHome := mkdir(t, filepath.Join(home, "custom-codex"))
	writeCodexAppFinishedRollout(t, customHome, startThread, cwd, 3*time.Hour)
	n.codices = newCodexRegistry()
	n.codices.scan = func() []codexProcess { return nil }
	n.codices.checked = func() ([]codexProcess, bool) { return nil, true }
	n.configureCodex(proto.CodexConfig{LegacyHome: customHome, Homes: []string{customHome}})
	n.discoverCodex()
	listSessions(t, n)

	canonicalCustom := canonicalCodexHome(customHome, n.home)
	n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
	var seenHome string
	n.takeovers.shared = func(_ context.Context, src codexSource) ([]string, error) {
		seenHome = src.home
		return []string{"codex", "--remote", "unix:///custom/socket"}, nil
	}
	plan, err := n.prepareCodexTakeover(context.Background(), startThread)
	if err != nil {
		t.Fatal(err)
	}
	file := resumeFile(f, cwd)
	f.setScreen("w1:p2", cxReady)
	f.after(func(c herdrCall) {
		if c.Method == "pane.send_input" && strings.Contains(str(c.Params["text"]), "codex-start") {
			takeoverTestOwner(n)
		}
	})
	if _, err := n.commitCodexTakeover(context.Background(), startThread, plan.Token, true); err != nil {
		t.Fatalf("custom CODEX_HOME takeover failed: %v", err)
	}
	if seenHome != canonicalCustom {
		t.Fatalf("shared daemon home = %q, want %q", seenHome, canonicalCustom)
	}
	if want := []string{"CODEX_HOME=" + canonicalCustom, "codex", "--remote", "unix:///custom/socket", "-C", cwd, "resume", startThread}; !slices.Equal(file().Argv, want) {
		t.Fatalf("argv=%v, want %v", file().Argv, want)
	}
}

func TestCodexAppRecoveryRefusesResumeWhenAppRunningOrLiveWriter(t *testing.T) {
	t.Run("app running without legacy bus offers continue only", func(t *testing.T) {
		n, f, cwd, _ := codexAppRecoveryNode(t, true)
		codexHome := filepath.Join(n.home, ".codex")
		n.codices.scan = func() []codexProcess {
			return []codexProcess{{pid: 5150, home: codexHome, endpoint: "stdio://", desktop: true}}
		}
		n.codices.checked = func() ([]codexProcess, bool) { return n.codices.scan(), true }
		n.discoverCodex()

		row := byID(listSessions(t, n))[startThread]
		if row.Capabilities == nil || row.Capabilities.Send || row.Capabilities.Terminal {
			t.Fatalf("running App should not advertise send or terminal resume: %+v", row.Capabilities)
		}
		if row.Capabilities.Takeover == nil || row.Capabilities.Takeover.CanResume || row.Capabilities.Takeover.Reason != "app_running" || !row.Capabilities.Takeover.AppOwned {
			t.Fatalf("running App takeover capability = %+v", row.Capabilities.Takeover)
		}
		if want := i18n.T(i18n.Default, codexDesktopContinueOnlyKey); row.ReadOnlyReason != want {
			t.Fatalf("read_only_reason = %q, want %q", row.ReadOnlyReason, want)
		}

		// Continue in a new session still prepares safely.
		n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
		plan, err := n.prepareCodexTakeover(context.Background(), startThread)
		if err != nil {
			t.Fatal(err)
		}
		if plan.CanResume || plan.Reason != "app_running" || plan.Cwd != cwd || plan.ContinuationPrompt == "" {
			t.Fatalf("running App plan = %+v", plan)
		}
		if _, err := n.commitCodexTakeover(context.Background(), startThread, plan.Token, true); refusalCode(err) != "codex_takeover_app_running" {
			t.Fatalf("commit while App running = %v", err)
		}
		if _, err := request(t, n, "session.start", map[string]any{"resume_session_id": startThread}); refusalCode(err) != "codex_read_only" {
			t.Fatalf("session.start resume while App running = %v", err)
		}
		if calls := f.trace("tab.create", "pane.send_input"); len(calls) != 0 {
			t.Fatalf("refused resume touched terminal: %v", calls)
		}
	})

	t.Run("app starts between prepare and commit", func(t *testing.T) {
		n, f, _, _ := codexAppRecoveryNode(t, false)
		codexHome := filepath.Join(n.home, ".codex")
		n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
		n.takeovers.shared = func(context.Context, codexSource) ([]string, error) {
			t.Fatal("shared daemon should not start when App is already running")
			return nil, nil
		}
		listSessions(t, n)
		plan, err := n.prepareCodexTakeover(context.Background(), startThread)
		if err != nil || !plan.CanResume {
			t.Fatalf("prepare = %+v err=%v", plan, err)
		}
		n.codices.scan = func() []codexProcess {
			return []codexProcess{{pid: 6060, home: codexHome, desktop: true}}
		}
		n.codices.checked = func() ([]codexProcess, bool) { return n.codices.scan(), true }
		if _, err := n.commitCodexTakeover(context.Background(), startThread, plan.Token, true); refusalCode(err) != "codex_takeover_app_running" {
			t.Fatalf("TOCTOU App start was not refused: %v", err)
		}
		if calls := f.trace("tab.create", "pane.send_input"); len(calls) != 0 {
			t.Fatalf("refused takeover touched terminal: %v", calls)
		}
	})

	t.Run("active legacy bus keeps App bus control and refuses terminal resume", func(t *testing.T) {
		busHome, _ := startFakeCodexBus(t, startThread)
		n, _, home := herdrNode(t)
		cwd := mkdir(t, filepath.Join(home, "proj"))
		writeCodexAppFinishedRollout(t, busHome, startThread, cwd, 2*time.Hour)
		if err := os.WriteFile(filepath.Join(busHome, ".codex-global-state.json"),
			[]byte(`{"projectless-thread-ids":["`+startThread+`"]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		n.codices = newCodexRegistry()
		n.codices.scan = func() []codexProcess { return nil }
		n.codices.checked = func() ([]codexProcess, bool) { return nil, true }
		n.configureCodex(proto.CodexConfig{LegacyHome: busHome, Homes: []string{busHome}})
		n.discoverCodex()

		row := byID(listSessions(t, n))[startThread]
		if row.Capabilities == nil || !row.Capabilities.Send || row.Capabilities.Terminal || row.Capabilities.Takeover != nil {
			t.Fatalf("live bus chat should use App bus, not terminal/takeover: %+v", row.Capabilities)
		}
		if _, err := request(t, n, "session.start", map[string]any{"resume_session_id": startThread}); refusalCode(err) != "codex_read_only" {
			t.Fatalf("live bus chat was resumed in terminal: %v", err)
		}
	})

	t.Run("working turn or live rollout refuses takeover and resume", func(t *testing.T) {
		n, _, _, _ := codexAppRecoveryNode(t, true, `{"timestamp":"`+time.Now().UTC().Format(time.RFC3339Nano)+`","type":"event_msg","payload":{"type":"task_started"}}`)
		row := byID(listSessions(t, n))[startThread]
		if row.Status != proto.StatusWorking || row.Capabilities.Terminal || row.Capabilities.Takeover != nil {
			t.Fatalf("working App chat should stay view-only: status=%q caps=%+v", row.Status, row.Capabilities)
		}
		if _, err := request(t, n, "session.start", map[string]any{"resume_session_id": startThread}); refusalCode(err) != "codex_read_only" {
			t.Fatalf("working App chat was resumed: %v", err)
		}
	})

	t.Run("incomplete process scan fails closed", func(t *testing.T) {
		n, _, _, _ := codexAppRecoveryNode(t, false)
		listSessions(t, n)
		n.codices.checked = func() ([]codexProcess, bool) { return nil, false }
		n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
		plan, err := n.prepareCodexTakeover(context.Background(), startThread)
		if err != nil {
			t.Fatal(err)
		}
		if plan.CanResume || plan.Reason != "unavailable" || plan.ContinuationPrompt == "" {
			t.Fatalf("incomplete scan should fail closed to continuation: %+v", plan)
		}
		if _, err := request(t, n, "session.start", map[string]any{"resume_session_id": startThread}); refusalCode(err) != "codex_read_only" {
			t.Fatalf("incomplete scan allowed resume: %v", err)
		}
	})

	t.Run("multiple app-server owners stay strictly view-only", func(t *testing.T) {
		n, _, _, _ := codexAppRecoveryNode(t, false)
		codexHome := canonicalCodexHome(filepath.Join(n.home, ".codex"), n.home)
		n.codices.mu.Lock()
		n.codices.remotes["r1"] = &codexRemote{n: n, ready: true, spec: proto.CodexServer{ID: "r1", Home: codexHome},
			threads: map[string]remoteThread{startThread: {id: startThread, publicID: startThread}}}
		n.codices.remotes["r2"] = &codexRemote{n: n, ready: true, spec: proto.CodexServer{ID: "r2", Home: codexHome},
			threads: map[string]remoteThread{startThread: {id: startThread, publicID: startThread}}}
		n.codices.mu.Unlock()
		row := byID(listSessions(t, n))[startThread]
		if row.Capabilities.Send || row.Capabilities.Terminal || row.Capabilities.Takeover != nil {
			t.Fatalf("ambiguous app-server owners must stay view-only: %+v", row.Capabilities)
		}
	})
}

func TestCodexAppRecoveryEncryptedContextFallsBackToContinue(t *testing.T) {
	t.Run("recorded encrypted context error", func(t *testing.T) {
		errLine := `{"timestamp":"` + time.Now().Add(-3*time.Hour).UTC().Format(time.RFC3339Nano) + `","type":"event_msg","payload":{"type":"task_complete","error":{"message":"invalid_encrypted_content"}}}`
		n, _, cwd, _ := codexAppRecoveryNode(t, false, errLine)
		row := byID(listSessions(t, n))[startThread]
		if row.Capabilities.Terminal || row.Capabilities.Takeover == nil || row.Capabilities.Takeover.CanResume ||
			row.Capabilities.Takeover.Reason != "encrypted_context" || !row.Capabilities.Takeover.AppOwned {
			t.Fatalf("encrypted App thread caps = %+v", row.Capabilities)
		}
		if want := i18n.T(i18n.Default, codexDesktopContinueOnlyKey); row.ReadOnlyReason != want {
			t.Fatalf("read_only_reason = %q, want %q", row.ReadOnlyReason, want)
		}
		n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
		plan, err := n.prepareCodexTakeover(context.Background(), startThread)
		if err != nil {
			t.Fatal(err)
		}
		if plan.CanResume || plan.Reason != "encrypted_context" || plan.Cwd != cwd || plan.ContinuationPrompt == "" {
			t.Fatalf("encrypted plan = %+v", plan)
		}
		if _, err := n.commitCodexTakeover(context.Background(), startThread, plan.Token, true); refusalCode(err) != "codex_takeover_continue_only" {
			t.Fatalf("encrypted commit = %v", err)
		}
		if _, err := request(t, n, "session.start", map[string]any{"resume_session_id": startThread}); refusalCode(err) != "codex_read_only" {
			t.Fatalf("encrypted session.start resume = %v", err)
		}
	})

	t.Run("late encrypted failure during takeover commit switches to continue only", func(t *testing.T) {
		withCodexWaits(t)
		n, f, cwd, _ := codexAppRecoveryNode(t, false)
		listSessions(t, n)
		resumeFile(f, cwd)
		f.setScreen("w1:p2", cxReady)
		n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
		n.takeovers.shared = func(context.Context, codexSource) ([]string, error) {
			return []string{"codex", "--remote", "unix:///shared/socket"}, nil
		}
		f.after(func(c herdrCall) {
			if c.Method == "pane.send_input" && strings.Contains(str(c.Params["text"]), "codex-start") {
				f.setScreen("w1:p2", codexStartError+" invalid_encrypted_content")
			}
		})
		plan, err := n.prepareCodexTakeover(context.Background(), startThread)
		if err != nil || !plan.CanResume {
			t.Fatalf("prepare = %+v err=%v", plan, err)
		}
		if _, err := n.commitCodexTakeover(context.Background(), startThread, plan.Token, true); refusalCode(err) != "codex_takeover_continue_only" {
			t.Fatalf("late encrypted failure = %v", err)
		}
		f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
		_ = n.refreshHosts(context.Background())
		row := byID(listSessions(t, n))[startThread]
		if row.Capabilities.Terminal || row.Capabilities.Takeover == nil || row.Capabilities.Takeover.CanResume || row.Capabilities.Takeover.Reason != "encrypted_context" {
			t.Fatalf("post-failure row = %+v (%+v)", row, row.Capabilities)
		}
	})
}

func TestCodexAppAutomaticResumeProvenanceAndConsumption(t *testing.T) {
	n, f, cwd, _ := codexAppRecoveryNode(t, false)
	listSessions(t, n)

	// A completed turn on an idle, unowned App thread supplies the initial stop cause.
	got := resumeFile(f, cwd)
	if _, err := request(t, n, "session.start", map[string]any{
		"resume_session_id": startThread, "prompt": "Un-draft the MR", "resume_automatic": true,
	}); err != nil {
		t.Fatalf("supervised resume of idle unowned App thread failed: %v", err)
	}
	if file := got(); !strings.Contains(strings.Join(file.Argv, " "), "resume "+startThread) || file.Prompt != "Un-draft the MR" {
		t.Fatalf("unexpected start file: %+v", file)
	}
	if state, ok := n.state.get(startThread); !ok || state.StopCause != proto.StopUnknown {
		t.Fatalf("state after successful resume: present=%t, stop cause=%s", ok, state.StopCause)
	}
	// The saved unknown stop survives the pane's exit and supersedes the completed rollout.
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	if err := n.refreshHosts(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cause := n.effectiveStopCause(startThread); cause != proto.StopUnknown {
		t.Fatalf("effective stop cause after pane exit = %s, want unknown", cause)
	}
	row := requireCodexSession(t, listSessions(t, n), startThread)
	if row.Capabilities.StopCause == proto.StopTaskCompleted {
		t.Fatal("listing restored task_completed from the rollout after resume")
	}
	n.state.stopped(startThread, proto.StopUser)
	if _, err := request(t, n, "session.start", map[string]any{
		"resume_session_id": startThread, "prompt": "After user stop", "resume_automatic": true,
	}); refusalCode(err) != "resume_not_allowed" {
		t.Fatalf("user-stopped App thread allowed automatic resume: %v", err)
	}
}

type uncertainAppResumeHost struct {
	*fakeTakeoverLauncherHost
	workspaceAttempts int
	startErr          error
}

func (h *uncertainAppResumeHost) CreateSpace(context.Context, string, string) (*host.Created, error) {
	h.workspaceAttempts++
	return nil, h.startErr
}

func TestCodexAppAutomaticResumeSettlesAfterConsumingSavedStop(t *testing.T) {
	withCodexLaunchers(t, map[string][]string{"codex": {"codex"}})
	cases := []struct {
		name      string
		savedStop bool
	}{
		// Consuming the old cause lets uncertainResumes hold the retry until the start is observed or resumeSettle expires.
		{name: "saved stop", savedStop: true},
		// After CreateSpace returns an uncertain error, resumeCodexThread records an unknown stop cause that takes precedence over the rollout.
		{name: "derived from rollout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := remoteTestNode(t)
			h := &uncertainAppResumeHost{
				fakeTakeoverLauncherHost: &fakeTakeoverLauncherHost{kind: host.Herdr},
				startErr:                 errors.New("workspace creation reply lost"),
			}
			n.hosts = host.NewRegistry(h)
			mkdir(t, n.dir)
			cwd := t.TempDir()
			thread := newUUID()
			path := writeCodexAppFinishedRollout(t, n.codexHome(), thread, cwd, 4*time.Hour)
			n.registerCodex(n.codexHome(), thread, path)
			if tc.savedStop {
				n.state.put(thread, sessionState{Cwd: cwd, Launcher: "codex", StopCause: proto.StopTaskCompleted})
			} else if _, ok := n.state.get(thread); ok {
				t.Fatal("rollout thread already has saved state before resume")
			}
			if cause := n.effectiveStopCause(thread); cause != proto.StopTaskCompleted {
				t.Fatalf("initial stop cause = %s, want task_completed", cause)
			}

			resume := map[string]any{"resume_session_id": thread, "resume_automatic": true, "prompt": "Continue the task"}
			_, err := request(t, n, "session.start", resume)
			if err == nil || !strings.Contains(err.Error(), h.startErr.Error()) || startFailedForSure(err) || h.workspaceAttempts != 1 {
				t.Fatalf("first resume = %v, workspace attempts = %d, want one uncertain start", err, h.workspaceAttempts)
			}
			if cause := n.effectiveStopCause(thread); cause != proto.StopUnknown {
				t.Errorf("stop cause after uncertain start = %s, want unknown", cause)
			}
			if _, err := request(t, n, "session.start", resume); refusalCode(err) != "resume_not_allowed" {
				t.Errorf("second resume = %v, want resume_not_allowed", err)
			}
			if h.workspaceAttempts != 1 {
				t.Errorf("second resume reached workspace creation: total attempts = %d", h.workspaceAttempts)
			}
		})
	}
}

func TestCodexDesktopProcessDetection(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantOK      bool
		wantDesktop bool
	}{
		{"macOS Codex App GUI", []string{"/Applications/Codex.app/Contents/MacOS/Codex"}, true, true},
		{"macOS Codex App bundled stdio app-server", []string{"/Applications/Codex.app/Contents/Resources/codex", "app-server"}, true, true},
		{"macOS ChatGPT bundled stdio app-server", []string{"/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex", "app-server"}, true, true},
		{"Linux codex-desktop GUI", []string{"/usr/bin/codex-desktop"}, true, true},
		{"Windows OpenAI.Codex GUI", []string{`C:\Program Files\WindowsApps\OpenAI.Codex_1.0\Codex.exe`}, true, true},
		{"unrelated CLI stdio app-server", []string{"/usr/local/bin/codex", "app-server"}, true, false},
		{"shared listening daemon", []string{"/Applications/Codex.app/Contents/Resources/codex", "app-server", "--listen", "unix:///tmp/c.sock"}, true, false},
		{"bundled CLI used in terminal", []string{"/Applications/ChatGPT.app/Contents/Resources/codex-cli/bin/codex"}, true, false},
		{"ordinary CLI", []string{"codex"}, true, false},
		{"unrelated process", []string{"/usr/bin/vim", "notes.md"}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, ok := codexProcessArgs(1234, tc.args, "")
			if ok != tc.wantOK || (ok && p.desktop != tc.wantDesktop) {
				t.Fatalf("codexProcessArgs(%v) = (%+v, %v), want ok=%v desktop=%v", tc.args, p, ok, tc.wantOK, tc.wantDesktop)
			}
		})
	}
}

func TestCodexAppRecoveryLocalizedStrings(t *testing.T) {
	keys := []string{
		codexDesktopRecoverableKey,
		codexDesktopContinueOnlyKey,
		"codex-takeover.app_running",
		"codex-takeover.app_confirmation_required",
	}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		for _, key := range keys {
			got := i18n.T(lang, key)
			if got == "" || got == key {
				t.Errorf("%s/%s missing translation: %q", lang, key, got)
			}
		}
	}
	// Verify errors.Is / refusalCode works for new refusal keys.
	if err := takeoverRefusal("app_running"); refusalCode(err) != "codex_takeover_app_running" {
		t.Fatalf("app_running refusal = %v", err)
	}
	if err := takeoverRefusal("app_confirmation_required"); refusalCode(err) != "codex_takeover_app_confirmation_required" {
		t.Fatalf("app_confirmation_required refusal = %v", err)
	}
	if errors.Is(takeoverRefusal("app_running"), errCodexNoOwner) {
		t.Fatal("unexpected sentinel match")
	}
}
