package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func writeFakeClaudeTranscript(t *testing.T, n *Node, id string) {
	t.Helper()
	dir := filepath.Join(n.home, ".claude", "projects", "-assistant")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(`{"type":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAssistantCrossEngineResumeKeepsCompatibleSettingFollowingModel(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	f := cliTest(t)
	f.fakeLaunch(t)
	probedLaunchers(t, map[string]string{codexKindLauncher: "codex"})
	const launcher = "gateway_claude"
	const savedModel = "vendor/alternate-model"
	f.n.setManaged([]proto.ManagedLauncher{{
		Name:    launcher,
		BaseURL: "http://127.0.0.1:1",
		APIKey:  "test-key",
		Model:   "vendor/default-model",
		Models:  []proto.ProviderModel{{ID: savedModel}},
	}})
	writeFakeClaudeTranscript(t, f.n, claudeConversation)
	f.n.state.put(claudeConversation, sessionState{
		Launcher:         launcher,
		Cwd:              t.TempDir(),
		Model:            savedModel,
		ModelEffort:      "high",
		ModelFingerprint: "fp-saved",
		FollowsSetting:   true,
	})

	id, err := f.n.assistantSend(context.Background(), claudeConversation, "", codexKindLauncher, "follow up", nil, "", "",
		modelExecution{Model: "gpt-model", Effort: "xhigh", UseEffort: true, Fingerprint: "codex-fp", FollowsSetting: true})
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)

	argv := f.file("cli-argv")
	if !strings.Contains(argv, "--resume "+claudeConversation) ||
		!strings.Contains(argv, "--model "+savedModel) || strings.Contains(argv, "gpt-model") {
		t.Fatalf("expected resume with the original launcher's saved model, got: %q", argv)
	}
	st, ok := f.n.state.get(claudeConversation)
	if !ok || st.Launcher != launcher || st.Model != savedModel || st.ModelEffort != "high" ||
		st.ModelFingerprint != "fp-saved" || !st.FollowsSetting {
		t.Fatalf("resume changed the saved launcher or model execution: %+v (ok=%v)", st, ok)
	}
}

// Cross-engine launcher fallback clears the hub's options for the other engine and resumes on the conversation's own
// launcher. A stale or unlisted persisted st.Model (for example a residual small/haiku model) must fall back to the
// launcher default instead of trapping subsequent turns in model_not_found (#15622).
func TestAssistantCrossEngineResumeDropsStaleManagedLauncherModel(t *testing.T) {
	t.Run("managed launcher with explicit Models list drops stale small model", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		f := cliTest(t)
		f.fakeLaunch(t)
		probedLaunchers(t, map[string]string{codexKindLauncher: "codex"})
		f.n.setManaged([]proto.ManagedLauncher{{
			Name:       "gw_claude",
			BaseURL:    "http://gw.example.com",
			APIKey:     "test-key",
			Model:      "claude-opus-5-5",
			SmallModel: "claude-haiku-5-5",
			HaikuModel: "claude-haiku-5-5",
			Models: []proto.ProviderModel{
				{ID: "claude-opus-5-5"},
				{ID: "claude-sonnet-5-5"},
			},
		}})
		writeFakeClaudeTranscript(t, f.n, claudeConversation)
		f.n.state.put(claudeConversation, sessionState{
			Launcher:         "gw_claude",
			Cwd:              t.TempDir(),
			Model:            "claude-haiku-5-5",
			ModelFingerprint: "fp-old",
			FollowsSetting:   true,
		})

		id, err := f.n.assistantSend(context.Background(), claudeConversation, "", codexKindLauncher, "hello", nil, "", "",
			modelExecution{Model: "gpt-model", Fingerprint: "codex-fp", FollowsSetting: true})
		if err != nil {
			t.Fatal(err)
		}
		f.idle(t, id)

		argv := f.file("cli-argv")
		if !strings.Contains(argv, "--resume "+claudeConversation) {
			t.Fatalf("expected claude resume args, got: %q", argv)
		}
		if strings.Contains(argv, "claude-haiku-5-5") || strings.Contains(argv, "gpt-model") {
			t.Fatalf("resurrected stale or foreign model on cross-engine resume: %q", argv)
		}
		st, ok := f.n.state.get(claudeConversation)
		if !ok || st.Launcher != "gw_claude" || st.Model != "" || st.ModelFingerprint != "launcher-default" {
			t.Fatalf("unexpected persisted state after dropping stale model: %+v (ok=%v)", st, ok)
		}
	})

	t.Run("managed launcher without Models list drops residual small/haiku model", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		f := cliTest(t)
		f.fakeLaunch(t)
		probedLaunchers(t, map[string]string{codexKindLauncher: "codex"})
		f.n.setManaged([]proto.ManagedLauncher{{
			Name:       "gw_claude_nomodels",
			BaseURL:    "http://gw.example.com",
			APIKey:     "test-key",
			Model:      "claude-opus-5-5",
			SmallModel: "claude-haiku-5-5",
			HaikuModel: "claude-haiku-5-5",
		}})
		writeFakeClaudeTranscript(t, f.n, claudeConversation)
		f.n.state.put(claudeConversation, sessionState{
			Launcher:         "gw_claude_nomodels",
			Cwd:              t.TempDir(),
			Model:            "claude-haiku-5-5",
			ModelFingerprint: "fp-old",
		})

		id, err := f.n.assistantSend(context.Background(), claudeConversation, "", codexKindLauncher, "hello", nil, "", "",
			modelExecution{Model: "gpt-model", Fingerprint: "codex-fp"})
		if err != nil {
			t.Fatal(err)
		}
		f.idle(t, id)

		argv := f.file("cli-argv")
		if strings.Contains(argv, "claude-haiku-5-5") {
			t.Fatalf("resurrected residual small/haiku model on managed launcher: %q", argv)
		}
		if st, _ := f.n.state.get(claudeConversation); st.Model != "" {
			t.Fatalf("stale model remained persisted: %+v", st)
		}
	})

	t.Run("managed launcher keeps valid configured model for ordinary session", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		f := cliTest(t)
		f.fakeLaunch(t)
		probedLaunchers(t, map[string]string{codexKindLauncher: "codex"})
		f.n.setManaged([]proto.ManagedLauncher{{
			Name:    "gw_claude_valid",
			BaseURL: "http://gw.example.com",
			APIKey:  "test-key",
			Model:   "claude-opus-5-5",
			Models: []proto.ProviderModel{
				{ID: "claude-opus-5-5"},
				{ID: "claude-sonnet-5-5"},
			},
		}})
		writeFakeClaudeTranscript(t, f.n, claudeConversation)
		f.n.state.put(claudeConversation, sessionState{
			Launcher:         "gw_claude_valid",
			Cwd:              t.TempDir(),
			Model:            "claude-sonnet-5-5",
			ModelFingerprint: "fp-sonnet",
		})

		id, err := f.n.assistantSend(context.Background(), claudeConversation, "", codexKindLauncher, "hello", nil, "", "",
			modelExecution{Model: "gpt-model", Fingerprint: "codex-fp"})
		if err != nil {
			t.Fatal(err)
		}
		f.idle(t, id)

		argv := f.file("cli-argv")
		if !strings.Contains(argv, "--model claude-sonnet-5-5") {
			t.Fatalf("expected valid configured model to be kept, got: %q", argv)
		}
		if st, _ := f.n.state.get(claudeConversation); st.Model != "claude-sonnet-5-5" {
			t.Fatalf("valid persisted model was unexpectedly cleared: %+v", st)
		}
	})

	t.Run("cached launcher catalog drops unlisted stale model on cross-engine resume", func(t *testing.T) {
		assertCachedCatalogCrossEngineResumeUsesDefault(t)
	})

	t.Run("cached launcher catalog drops unlisted stale model despite host model env", func(t *testing.T) {
		t.Setenv("ANTHROPIC_MODEL", "host-gateway-model")
		t.Setenv("ANTHROPIC_DEFAULT_SONNET_MODEL", "host-sonnet")
		assertCachedCatalogCrossEngineResumeUsesDefault(t)
	})

	t.Run("cached launcher catalog drops unlisted stale model despite host managed settings", func(t *testing.T) {
		dir := t.TempDir()
		policyFile := filepath.Join(dir, "managed-settings.json")
		dropInDir := filepath.Join(dir, "managed-settings.d")
		if err := os.MkdirAll(dropInDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(policyFile, []byte(`{"model":"managed-model","env":{"ANTHROPIC_MODEL":"managed-env-model"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dropInDir, "10-model.json"), []byte(`{"env":{"ANTHROPIC_DEFAULT_OPUS_MODEL":"managed-dropin"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		prevFiles, prevDirs := claudeManagedSettings, claudeManagedSettingsDirs
		claudeManagedSettings = []string{policyFile}
		claudeManagedSettingsDirs = []string{dropInDir}
		t.Cleanup(func() {
			claudeManagedSettings, claudeManagedSettingsDirs = prevFiles, prevDirs
		})
		assertCachedCatalogCrossEngineResumeUsesDefault(t)
	})
}

func assertCachedCatalogCrossEngineResumeUsesDefault(t *testing.T) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	f := cliTest(t)
	probedLaunchers(t, map[string]string{codexKindLauncher: "codex"})
	fp := f.n.executionFingerprint("claude", modelExecution{})
	cacheKey := f.n.home + "\x00claude\x00claude\x00" + fp
	launcherModelsCache.Lock()
	launcherModelsCache.at[cacheKey] = time.Now()
	launcherModelsCache.got[cacheKey] = proto.LauncherModels{
		Models: []proto.AgentModel{
			{ID: "claude-opus-5-5", Default: true},
			{ID: "claude-sonnet-5-5"},
		},
	}
	launcherModelsCache.Unlock()
	t.Cleanup(func() {
		launcherModelsCache.Lock()
		delete(launcherModelsCache.at, cacheKey)
		delete(launcherModelsCache.got, cacheKey)
		launcherModelsCache.Unlock()
	})

	writeFakeClaudeTranscript(t, f.n, claudeConversation)
	f.n.state.put(claudeConversation, sessionState{
		Launcher:         "claude",
		Cwd:              t.TempDir(),
		Model:            "stale-haiku",
		ModelFingerprint: "fp-stale",
	})

	id, err := f.n.assistantSend(context.Background(), claudeConversation, "", codexKindLauncher, "hello", nil, "", "",
		modelExecution{Model: "gpt-model", Fingerprint: "codex-fp"})
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)

	argv := f.file("cli-argv")
	if strings.Contains(argv, "stale-haiku") {
		t.Fatalf("resurrected model absent from cached catalog: %q", argv)
	}
	if !strings.Contains(argv, "--model default") {
		t.Fatalf("expected --model default to override transcript stickiness, got: %q", argv)
	}
	if st, _ := f.n.state.get(claudeConversation); st.Model != "" {
		t.Fatalf("unlisted stale model remained persisted: %+v", st)
	}
}

func TestAssistantCrossEngineResumeDropsSettingFollowingAndRecoveryModel(t *testing.T) {
	cases := []struct {
		name            string
		askedLauncher   string
		askedExec       []modelExecution
		state           sessionState
		expectNoModelID string
	}{
		{
			name:          "setting-following conversation drops legacy st.Model on cross-engine resume",
			askedLauncher: codexKindLauncher,
			askedExec:     []modelExecution{{Model: "gpt-model", Fingerprint: "codex-fp", FollowsSetting: true}},
			state: sessionState{
				Launcher:         "claude",
				Model:            "legacy-haiku",
				ModelFingerprint: "fp-legacy",
				FollowsSetting:   true,
			},
			expectNoModelID: "legacy-haiku",
		},
		{
			name:          "main conversation drops legacy st.Model on cross-engine resume",
			askedLauncher: codexKindLauncher,
			askedExec:     []modelExecution{{Model: "gpt-model", Fingerprint: "codex-fp", IsMain: true}},
			state: sessionState{
				Launcher:         "claude",
				Model:            "legacy-haiku",
				ModelFingerprint: "fp-legacy",
				IsMain:           true,
			},
			expectNoModelID: "legacy-haiku",
		},
		{
			name:          "ModelFromRecovery drops stale model on cross-engine resume",
			askedLauncher: codexKindLauncher,
			askedExec:     []modelExecution{{Model: "gpt-model", Fingerprint: "codex-fp"}},
			state: sessionState{
				Launcher:          "claude",
				Model:             "recovered-haiku",
				ModelFingerprint:  "fp-rec",
				ModelFromRecovery: true,
			},
			expectNoModelID: "recovered-haiku",
		},
		{
			name:          "ModelFromRecovery drops stale model on same-engine empty-options resume",
			askedLauncher: "claude",
			askedExec:     nil,
			state: sessionState{
				Launcher:          "claude",
				Model:             "recovered-haiku",
				ModelFingerprint:  "fp-rec",
				ModelFromRecovery: true,
			},
			expectNoModelID: "recovered-haiku",
		},
		{
			name:          "ModelRecovery marker targeting stale model drops on cross-engine resume",
			askedLauncher: codexKindLauncher,
			askedExec:     []modelExecution{{Model: "gpt-model", Fingerprint: "codex-fp"}},
			state: sessionState{
				Launcher:         "claude",
				Model:            "recovered-haiku",
				ModelFingerprint: "fp-rec",
				ModelRecovery: &proto.ModelRecovery{
					From:           "primary-model",
					To:             "recovered-haiku",
					ChainExhausted: true,
					Tried:          []string{"primary-model", "recovered-haiku"},
				},
			},
			expectNoModelID: "recovered-haiku",
		},
		{
			name:          "ModelRecovery exhausted Tried fallback model drops on cross-engine resume",
			askedLauncher: codexKindLauncher,
			askedExec:     []modelExecution{{Model: "gpt-model", Fingerprint: "codex-fp"}},
			state: sessionState{
				Launcher:         "claude",
				Model:            "recovered-haiku",
				ModelFingerprint: "fp-rec",
				ModelRecovery: &proto.ModelRecovery{
					From:           "primary-model",
					To:             "last-fallback",
					ChainExhausted: true,
					Tried:          []string{"primary-model", "recovered-haiku", "last-fallback"},
				},
			},
			expectNoModelID: "recovered-haiku",
		},
		{
			name:          "ModelRecovery exhausted fallback without From drops on same-engine resume",
			askedLauncher: "claude",
			askedExec:     nil,
			state: sessionState{
				Launcher:         "claude",
				Model:            "recovered-haiku",
				ModelFingerprint: "fp-rec",
				ModelRecovery: &proto.ModelRecovery{
					From:           "",
					ProbeFrom:      "primary-model",
					To:             "recovered-haiku",
					ChainExhausted: true,
					Tried:          []string{"primary-model", "recovered-haiku"},
				},
			},
			expectNoModelID: "recovered-haiku",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			f := cliTest(t)
			probedLaunchers(t, map[string]string{codexKindLauncher: "codex"})
			writeFakeClaudeTranscript(t, f.n, claudeConversation)
			st := tc.state
			st.Cwd = t.TempDir()
			f.n.state.put(claudeConversation, st)

			id, err := f.n.assistantSend(context.Background(), claudeConversation, "", tc.askedLauncher, "hello", nil, "", "", tc.askedExec...)
			if err != nil {
				t.Fatal(err)
			}
			f.idle(t, id)

			argv := f.file("cli-argv")
			if strings.Contains(argv, tc.expectNoModelID) {
				t.Fatalf("resurrected %q on resume: %q", tc.expectNoModelID, argv)
			}
			got, _ := f.n.state.get(claudeConversation)
			if got.Model != "" || got.ModelFromRecovery {
				t.Fatalf("stale model remained persisted: %+v", got)
			}
		})
	}
}

// When automatic model recovery falls back to a candidate model that fails with model_not_found, that failing candidate
// must not poison sessionState.Model; a subsequent cross-engine resume must recover cleanly on the launcher default
// instead of looping forever on model_not_found (#15622).
func TestAssistantRecoveryModelNotFoundDoesNotPoisonSessionState(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	f := cliTest(t)
	probedLaunchers(t, map[string]string{codexKindLauncher: "codex"})

	prevModels := assistantRecoveryModels
	assistantRecoveryModels = func(context.Context, *Node, string) (proto.LauncherModels, error) {
		return proto.LauncherModels{
			Models: []proto.AgentModel{
				{ID: "primary-model"},
				{ID: "bad-haiku"},
			},
		}, nil
	}
	t.Cleanup(func() { assistantRecoveryModels = prevModels })

	script := fakeCLIAuthStatus + `printf '%s\n' "$*" >> "$CLI_FAKE_DIR/cli-argv"
echo '{"type":"system","subtype":"init"}'
echo '{"type":"system","subtype":"background_tasks_changed","tasks":[]}'
is_bad=0
case "$*" in *'--model bad-haiku'*) is_bad=1 ;; esac
while IFS= read -r line; do
  case "$line" in
    *'"type":"control_request"'*)
      id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      echo "{\"type\":\"control_response\",\"response\":{\"subtype\":\"success\",\"request_id\":\"$id\"}}" ;;
    *'"type":"user"'*)
      printf '%s\n' "$line" >> "$CLI_FAKE_DIR/user.jsonl"
      if [ "$is_bad" -eq 1 ]; then
        echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 404 model_not_found: bad-haiku"}'
      else
        count=$(cat "$CLI_FAKE_DIR/primary-calls" 2>/dev/null || echo 0)
        count=$((count+1))
        echo "$count" > "$CLI_FAKE_DIR/primary-calls"
        if [ "$count" -eq 1 ]; then
          echo '{"type":"stream_event","event":{"type":"message_start","message":{"model":"primary-model","usage":{"input_tokens":5}}},"parent_tool_use_id":null}'
          echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: safeguards flagged this message"}'
        else
          echo '{"type":"stream_event","event":{"type":"message_start","message":{"model":"primary-model","usage":{"input_tokens":5}}},"parent_tool_use_id":null}'
          echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"recovered ok"}},"parent_tool_use_id":null}'
          echo '{"type":"result","subtype":"success","is_error":false,"result":"recovered ok"}'
        fi
      fi ;;
  esac
done
`
	if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
		t.Fatal(err)
	}

	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "first turn", nil, "", "", modelExecution{Fingerprint: "fp-default"})
	if err != nil {
		t.Fatal(err)
	}
	cliWait(t, "recovery to attempt bad-haiku", func() bool {
		return strings.Contains(f.file("cli-argv"), "--model bad-haiku")
	})
	f.idle(t, id)

	st, ok := f.n.state.get(id)
	if !ok {
		t.Fatalf("session state missing for %s", id)
	}
	if st.Model == "bad-haiku" {
		t.Fatalf("failed recovery fallback model poisoned sessionState.Model: %+v", st)
	}
	if !f.n.isModelUnavailable("claude", "bad-haiku") {
		t.Fatalf("expected bad-haiku to be marked unavailable on claude")
	}

	// Simulate a cross-engine resume after the failed recovery.
	writeFakeClaudeTranscript(t, f.n, id)
	if err := os.WriteFile(filepath.Join(f.dir, "cli-argv"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.n.assistantSend(context.Background(), id, "", codexKindLauncher, "follow up", nil, "", "",
		modelExecution{Model: "gpt-model", Fingerprint: "codex-fp"}); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)

	turn2Argv := f.file("cli-argv")
	if strings.Contains(turn2Argv, "bad-haiku") || strings.Contains(turn2Argv, "gpt-model") {
		t.Fatalf("cross-engine resume passed poisoned or foreign model: %q", turn2Argv)
	}
	p := f.proc(id)
	if p == nil {
		t.Fatal("missing process after follow-up turn")
	}
	p.mu.Lock()
	turnErr, replyModel := p.turnError, p.replyModel
	p.mu.Unlock()
	if turnErr != "" || replyModel != "primary-model" {
		t.Fatalf("cross-engine resume did not recover cleanly: turnErr=%q replyModel=%q", turnErr, replyModel)
	}
}

// When a legacy persisted st.Model (not explicitly chosen on this turn) fails with model_not_found or a gateway
// model_route error, the node clears the stale st.Model and marks it unavailable so the next turn automatically falls
// back to the launcher default and succeeds (#15622).
func TestAssistantPersistedModelRouteFailureClearsStaleModelAndRecovers(t *testing.T) {
	cases := []struct {
		name    string
		errText string
	}{
		{
			name:    "model_not_found",
			errText: "There is an issue with the selected model (stale-model). It may not exist or you may not have access to it.",
		},
		{
			name:    "model_route",
			errText: "API Error: 400 bare claude model name cannot be routed",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			f := cliTest(t)
			t.Setenv("CLI_FAKE_STALE_ERR", tc.errText)

			script := fakeCLIAuthStatus + `printf '%s\n' "$*" >> "$CLI_FAKE_DIR/cli-argv"
echo '{"type":"system","subtype":"init"}'
is_stale=0
case "$*" in *'--model stale-model'*) is_stale=1 ;; esac
while IFS= read -r line; do
  case "$line" in
    *'"type":"control_request"'*)
      id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      echo "{\"type\":\"control_response\",\"response\":{\"subtype\":\"success\",\"request_id\":\"$id\"}}" ;;
    *'"type":"user"'*)
      if [ "$is_stale" -eq 1 ]; then
        printf '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"%s"}\n' "$CLI_FAKE_STALE_ERR"
      else
        echo '{"type":"stream_event","event":{"type":"message_start","message":{"model":"default-model","usage":{"input_tokens":5}}},"parent_tool_use_id":null}'
        echo '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"ok on default"}},"parent_tool_use_id":null}'
        echo '{"type":"result","subtype":"success","is_error":false,"result":"ok on default"}'
      fi ;;
  esac
done
`
			if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
				t.Fatal(err)
			}

			writeFakeClaudeTranscript(t, f.n, claudeConversation)
			f.n.state.put(claudeConversation, sessionState{
				Launcher:         "claude",
				Cwd:              t.TempDir(),
				Model:            "stale-model",
				ModelFingerprint: "fp-stale",
			})

			// Turn 1: empty options resurrects legacy st.Model with PersistedModel=true and hits model_not_found/model_route.
			if _, err := f.n.assistantSend(context.Background(), claudeConversation, "", "claude", "turn 1", nil, "", ""); err != nil {
				t.Fatal(err)
			}
			f.idle(t, claudeConversation)

			st, ok := f.n.state.get(claudeConversation)
			if !ok || st.Model != "" || st.ModelFingerprint != "launcher-default" {
				t.Fatalf("expected stale st.Model to be cleared after %s, got: %+v (ok=%v)", tc.name, st, ok)
			}
			if !f.n.isModelUnavailable("claude", "stale-model") {
				t.Fatalf("expected stale-model to be marked unavailable on claude")
			}

			// Turn 2: empty options restarts the idle process on the launcher default and succeeds.
			if err := os.WriteFile(filepath.Join(f.dir, "cli-argv"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := f.n.assistantSend(context.Background(), claudeConversation, "", "claude", "turn 2", nil, "", ""); err != nil {
				t.Fatal(err)
			}
			f.idle(t, claudeConversation)

			argv2 := f.file("cli-argv")
			if strings.Contains(argv2, "stale-model") {
				t.Fatalf("turn 2 still passed cleared stale-model: %q", argv2)
			}
			p := f.proc(claudeConversation)
			if p == nil {
				t.Fatal("missing process after turn 2")
			}
			p.mu.Lock()
			turnErr, replyModel := p.turnError, p.replyModel
			p.mu.Unlock()
			if turnErr != "" || replyModel != "default-model" {
				t.Fatalf("turn 2 did not recover on launcher default: turnErr=%q replyModel=%q", turnErr, replyModel)
			}
		})
	}
}

func TestAssistantCodexPersistedModelNotFoundClearsSessionState(t *testing.T) {
	t.Run("persisted codex model cleared on model_not_found", func(t *testing.T) {
		n := codexTestNode(t)
		p, c, _ := newFakeCodex(t, n, t.TempDir())
		p.id, c.threadID = testThread, testThread
		p.launcher = "codex"
		p.model = modelExecution{Model: "stale-codex", PersistedModel: true}
		p.busy = true
		n.procs[testThread] = p
		n.state.put(testThread, sessionState{
			Launcher:         "codex",
			Cwd:              p.cwd,
			Model:            "stale-codex",
			ModelFingerprint: "fp-old",
		})

		params, err := json.Marshal(map[string]any{
			"threadId": testThread,
			"turn": map[string]any{
				"id":     "turn-1",
				"status": "failed",
				"error":  map[string]any{"message": "model_not_found: stale-codex"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		c.notification("turn/completed", params)

		st, _ := n.state.get(testThread)
		if st.Model != "" || st.ModelFingerprint != "launcher-default" {
			t.Fatalf("expected persisted codex model to be cleared, got: %+v", st)
		}
		if !n.isModelUnavailable("codex", "stale-codex") {
			t.Fatalf("expected stale-codex to be marked unavailable on codex")
		}
	})

	t.Run("explicit codex model pick is not cleared on model_not_found", func(t *testing.T) {
		n := codexTestNode(t)
		p, c, _ := newFakeCodex(t, n, t.TempDir())
		p.id, c.threadID = testThread, testThread
		p.launcher = "codex"
		p.model = modelExecution{Model: "explicit-codex", Fingerprint: "fp-pick", PersistedModel: false, FromRecovery: false}
		p.busy = true
		n.procs[testThread] = p
		n.state.put(testThread, sessionState{
			Launcher:         "codex",
			Cwd:              p.cwd,
			Model:            "explicit-codex",
			ModelFingerprint: "fp-pick",
		})

		params, err := json.Marshal(map[string]any{
			"threadId": testThread,
			"turn": map[string]any{
				"id":     "turn-1",
				"status": "failed",
				"error":  map[string]any{"message": "model_not_found: explicit-codex"},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		c.notification("turn/completed", params)

		st, _ := n.state.get(testThread)
		if st.Model != "explicit-codex" {
			t.Fatalf("explicit codex pick was unexpectedly cleared: %+v", st)
		}
		if n.isModelUnavailable("codex", "explicit-codex") {
			t.Fatalf("explicit codex pick should not be marked unavailable globally")
		}
	})
}

func TestAssistantSameEngineFollowUpAndExplicitModelPickUnaffected(t *testing.T) {
	t.Run("same-engine follow-up preserves valid persisted model", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		f := cliTest(t)
		writeFakeClaudeTranscript(t, f.n, claudeConversation)
		f.n.state.put(claudeConversation, sessionState{
			Launcher:         "claude",
			Cwd:              t.TempDir(),
			Model:            "kept-sonnet",
			ModelFingerprint: "fp-sonnet",
		})

		if _, err := f.n.assistantSend(context.Background(), claudeConversation, "", "claude", "first follow-up", nil, "", ""); err != nil {
			t.Fatal(err)
		}
		f.idle(t, claudeConversation)
		firstProc := f.proc(claudeConversation)

		if _, err := f.n.assistantSend(context.Background(), claudeConversation, "", "claude", "second follow-up", nil, "", ""); err != nil {
			t.Fatal(err)
		}
		f.idle(t, claudeConversation)

		if argv := f.file("cli-argv"); !strings.Contains(argv, "--model kept-sonnet") {
			t.Fatalf("same-engine follow-up did not keep persisted model: %q", argv)
		}
		if f.proc(claudeConversation) != firstProc {
			t.Fatal("same-engine follow-up restarted an already-matching process")
		}
		if st, _ := f.n.state.get(claudeConversation); st.Model != "kept-sonnet" {
			t.Fatalf("valid persisted model was modified: %+v", st)
		}
	})

	t.Run("explicit per-turn model pick failing with model_not_found is preserved", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		f := cliTest(t)
		script := fakeCLIAuthStatus + `printf '%s\n' "$*" >> "$CLI_FAKE_DIR/cli-argv"
echo '{"type":"system","subtype":"init"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"control_request"'*)
      id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      echo "{\"type\":\"control_response\",\"response\":{\"subtype\":\"success\",\"request_id\":\"$id\"}}" ;;
    *'"type":"user"'*)
      echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"API Error: 404 model_not_found: explicit-missing"}' ;;
  esac
done
`
		if err := os.WriteFile(filepath.Join(os.Getenv("HOME"), "bin", "claude"), []byte("#!/bin/sh\n"+script), 0o700); err != nil {
			t.Fatal(err)
		}

		id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "explicit pick", nil, "", "",
			modelExecution{Model: "explicit-missing", Fingerprint: "fp-pick"})
		if err != nil {
			t.Fatal(err)
		}
		f.idle(t, id)

		st, _ := f.n.state.get(id)
		if st.Model != "explicit-missing" {
			t.Fatalf("explicit model pick was cleared on failure: %+v", st)
		}
		if f.n.isModelUnavailable("claude", "explicit-missing") {
			t.Fatalf("explicit model pick was marked unavailable globally")
		}
	})

	t.Run("non-route errors on persisted model do not clear st.Model", func(t *testing.T) {
		for _, errText := range []string{
			"API Error: 503 no eligible upstream account is currently available",
			"API Error: 429 Too many requests",
			"API Error: 401 unauthorized",
			"API Error: 400 invalid_request_error: tool_search_tool is not supported",
		} {
			n := assistantNode(t)
			const id = "11111111-2222-3333-4444-555555555555"
			n.state.put(id, sessionState{
				Launcher:         "claude",
				Cwd:              t.TempDir(),
				Model:            "kept-model",
				ModelFingerprint: "fp-kept",
			})
			p := &assistantProc{
				id:       id,
				agent:    "claude",
				launcher: "claude",
				model:    modelExecution{Model: "kept-model", PersistedModel: true},
			}
			n.handleModelRouteFailure(p, errText)
			if st, _ := n.state.get(id); st.Model != "kept-model" {
				t.Fatalf("%q unexpectedly cleared persisted model: %+v", errText, st)
			}
			if n.isModelUnavailable("claude", "kept-model") {
				t.Fatalf("%q unexpectedly marked model unavailable", errText)
			}
		}
	})

	t.Run("same-engine resume preserves persisted original model with exhausted recovery marker", func(t *testing.T) {
		cases := []struct {
			name           string
			followsSetting bool
			marker         *proto.ModelRecovery
		}{
			{
				name: "exhausted switched chain with original model in Tried",
				marker: &proto.ModelRecovery{
					From:           "primary-model",
					To:             "fallback-model",
					ChainExhausted: true,
					Tried:          []string{"primary-model", "fallback-model"},
				},
			},
			{
				name:           "protected setting-following session with exhausted switched chain",
				followsSetting: true,
				marker: &proto.ModelRecovery{
					From:           "primary-model",
					To:             "fallback-model",
					ChainExhausted: true,
					Tried:          []string{"primary-model", "fallback-model"},
				},
			},
			{
				name: "exhausted chain with From equal to To and original model in Tried",
				marker: &proto.ModelRecovery{
					From:           "primary-model",
					To:             "primary-model",
					ChainExhausted: true,
					Tried:          []string{"fallback-model", "primary-model"},
				},
			},
			{
				name: "non-exhausted marker with From equal to To",
				marker: &proto.ModelRecovery{
					From: "primary-model",
					To:   "primary-model",
				},
			},
			{
				name: "no-switch exhausted chain with original model in Tried",
				marker: &proto.ModelRecovery{
					ChainExhausted: true,
					Tried:          []string{"primary-model"},
				},
			},
			{
				name: "exhausted launcher-default chain with ProbeFrom matching persisted model",
				marker: &proto.ModelRecovery{
					From:           "",
					ProbeFrom:      "primary-model",
					To:             "fallback-model",
					ChainExhausted: true,
					Tried:          []string{"primary-model", "fallback-model"},
				},
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Setenv("CLAUDE_CONFIG_DIR", "")
				f := cliTest(t)
				writeFakeClaudeTranscript(t, f.n, claudeConversation)
				f.n.state.put(claudeConversation, sessionState{
					Launcher:         "claude",
					Cwd:              t.TempDir(),
					Model:            "primary-model",
					ModelEffort:      "high",
					ModelFingerprint: "fp-primary",
					FollowsSetting:   tc.followsSetting,
					ModelRecovery:    tc.marker,
				})

				if _, err := f.n.assistantSend(context.Background(), claudeConversation, "", "claude", "follow up", nil, "", ""); err != nil {
					t.Fatal(err)
				}
				f.idle(t, claudeConversation)

				argv := f.file("cli-argv")
				if !strings.Contains(argv, "--model primary-model") {
					t.Fatalf("same-engine resume dropped original configured model: %q", argv)
				}
				if in := f.file("stdin.jsonl"); !strings.Contains(in, `"subtype":"apply_flag_settings"`) || !strings.Contains(in, `"effortLevel":"high"`) {
					t.Fatalf("same-engine resume did not apply persisted effort: %s", in)
				}
				p := f.proc(claudeConversation)
				if p == nil {
					t.Fatal("missing process after same-engine resume")
				}
				p.mu.Lock()
				gotExec := p.model
				p.mu.Unlock()
				if gotExec.Model != "primary-model" || gotExec.Effort != "high" || !gotExec.UseEffort || gotExec.Fingerprint != "fp-primary" {
					t.Fatalf("unexpected process execution after same-engine resume: %+v", gotExec)
				}
				st, ok := f.n.state.get(claudeConversation)
				if !ok || st.Model != "primary-model" || st.ModelEffort != "high" || st.ModelFingerprint != "fp-primary" {
					t.Fatalf("persisted original model/effort/fingerprint was modified: %+v (ok=%v)", st, ok)
				}
			})
		}
	})

	t.Run("cross-engine resume still drops setting-following session when exhausted marker matches r.From", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		f := cliTest(t)
		probedLaunchers(t, map[string]string{codexKindLauncher: "codex"})
		writeFakeClaudeTranscript(t, f.n, claudeConversation)
		f.n.state.put(claudeConversation, sessionState{
			Launcher:         "claude",
			Cwd:              t.TempDir(),
			Model:            "primary-model",
			ModelEffort:      "high",
			ModelFingerprint: "fp-primary",
			FollowsSetting:   true,
			ModelRecovery: &proto.ModelRecovery{
				From:           "primary-model",
				To:             "primary-model",
				ChainExhausted: true,
				Tried:          []string{"fallback-model", "primary-model"},
			},
		})

		if _, err := f.n.assistantSend(context.Background(), claudeConversation, "", codexKindLauncher, "follow up", nil, "", "",
			modelExecution{Model: "gpt-model", Fingerprint: "codex-fp", FollowsSetting: true}); err != nil {
			t.Fatal(err)
		}
		f.idle(t, claudeConversation)

		argv := f.file("cli-argv")
		if strings.Contains(argv, "primary-model") || strings.Contains(argv, "gpt-model") {
			t.Fatalf("cross-engine resume did not drop setting-following model: %q", argv)
		}
		if st, _ := f.n.state.get(claudeConversation); st.Model != "" || st.ModelFingerprint != "launcher-default" {
			t.Fatalf("expected cleared model state on cross-engine resume, got: %+v", st)
		}
	})

	t.Run("same-engine resume restores From when exhausted marker targets a fallback model", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		f := cliTest(t)
		writeFakeClaudeTranscript(t, f.n, claudeConversation)
		f.n.state.put(claudeConversation, sessionState{
			Launcher:         "claude",
			Cwd:              t.TempDir(),
			Model:            "recovered-haiku",
			ModelFingerprint: "fp-rec",
			ModelRecovery: &proto.ModelRecovery{
				From:           "primary-model",
				To:             "last-fallback",
				ChainExhausted: true,
				Tried:          []string{"primary-model", "recovered-haiku", "last-fallback"},
			},
		})

		if _, err := f.n.assistantSend(context.Background(), claudeConversation, "", "claude", "follow up", nil, "", ""); err != nil {
			t.Fatal(err)
		}
		f.idle(t, claudeConversation)

		argv := f.file("cli-argv")
		if strings.Contains(argv, "recovered-haiku") || !strings.Contains(argv, "--model primary-model") {
			t.Fatalf("expected same-engine resume to restore r.From, got: %q", argv)
		}
		if st, _ := f.n.state.get(claudeConversation); st.Model != "primary-model" {
			t.Fatalf("expected restored original model to be persisted, got: %+v", st)
		}
	})

	t.Run("same-engine resume drops fallback model when r.From is unavailable", func(t *testing.T) {
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		f := cliTest(t)
		writeFakeClaudeTranscript(t, f.n, claudeConversation)
		f.n.mu.Lock()
		f.n.noteUnavailable("claude", "primary-model")
		f.n.mu.Unlock()
		f.n.state.put(claudeConversation, sessionState{
			Launcher:         "claude",
			Cwd:              t.TempDir(),
			Model:            "recovered-haiku",
			ModelFingerprint: "fp-rec",
			ModelRecovery: &proto.ModelRecovery{
				From:           "primary-model",
				To:             "recovered-haiku",
				ChainExhausted: true,
				Tried:          []string{"primary-model", "recovered-haiku"},
			},
		})

		if _, err := f.n.assistantSend(context.Background(), claudeConversation, "", "claude", "follow up", nil, "", ""); err != nil {
			t.Fatal(err)
		}
		f.idle(t, claudeConversation)

		argv := f.file("cli-argv")
		if strings.Contains(argv, "recovered-haiku") || strings.Contains(argv, "primary-model") {
			t.Fatalf("expected fallback model to drop to launcher default, got: %q", argv)
		}
		if st, _ := f.n.state.get(claudeConversation); st.Model != "" || st.ModelFingerprint != "launcher-default" {
			t.Fatalf("expected cleared model state, got: %+v", st)
		}
	})

	t.Run("explicit pick matching From equal to To is not treated as stale recovery", func(t *testing.T) {
		n := assistantNode(t)
		const id = "11111111-2222-3333-4444-555555555555"
		n.state.put(id, sessionState{
			Launcher:         "claude",
			Cwd:              t.TempDir(),
			Model:            "primary-model",
			ModelFingerprint: "fp-primary",
			ModelRecovery: &proto.ModelRecovery{
				From:           "primary-model",
				To:             "primary-model",
				ChainExhausted: true,
				Tried:          []string{"fallback-model", "primary-model"},
			},
		})
		p := &assistantProc{
			id:       id,
			agent:    "claude",
			launcher: "claude",
			model:    modelExecution{Model: "primary-model", PersistedModel: false, FromRecovery: false},
		}
		n.handleModelRouteFailure(p, "API Error: 404 model_not_found: primary-model")
		if st, _ := n.state.get(id); st.Model != "primary-model" {
			t.Fatalf("explicit pick matching r.From == r.To was unexpectedly cleared: %+v", st)
		}
		if n.isModelUnavailable("claude", "primary-model") {
			t.Fatalf("explicit pick matching r.From == r.To was unexpectedly marked unavailable")
		}
	})
}
