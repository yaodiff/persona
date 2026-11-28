//go:build !windows

package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const externalTestNative = `#!/bin/sh
printf '%s\n' "$*" >> "$CLI_FAKE_DIR/external-argv"
echo '{"type":"system","subtype":"init","model":"example-model"}'
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$CLI_FAKE_DIR/external-stdin"
  case "$line" in
    *'"type":"control_request"'*)
      id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s"}}\n' "$id" ;;
    *'"type":"user"'*)
      echo '{"type":"result","subtype":"success","model":"example-model","result":"hello"}' ;;
  esac
done
`

const externalTestAdapter = `#!/bin/sh
operation=$1
shift
case "$operation" in
  inventory) exec /bin/cat "$CLI_FAKE_DIR/external-inventory.json" ;;
  exec)
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --native-program) native=$2; shift 2 ;;
        --settings-output) output=$2; shift 2 ;;
        --) shift; break ;;
        *) shift ;;
      esac
    done
    if [ -n "$output" ]; then
      printf '%s\n' "$output" > "$CLI_FAKE_DIR/external-settings-path"
      printf '{"env":{"EXAMPLE_SECRET":"synthetic-value"}}\n' > "$output"
      chmod 600 "$output"
    fi
    exec "$native" "$@" ;;
  *) exit 125 ;;
esac
`

func externalAssistantFixture(t *testing.T, warm ...bool) (*cliFixture, string, modelExecution, func(string)) {
	t.Helper()
	trustRoot, err := os.MkdirTemp("", "external-assistant-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(trustRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	trustRoot, err = filepath.EvalSymlinks(trustRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(trustRoot) })
	previousRoot := externalAdapterTrustRoot
	externalAdapterTrustRoot = trustRoot
	t.Cleanup(func() { externalAdapterTrustRoot = previousRoot })
	// The fake home, working directories and adapter runtime files must share this boundary too. TMPDIR places the
	// node's os.MkdirTemp("", …) directories; t.TempDir() uses GOTMPDIR ahead of TMPDIR when it is set, as
	// scripts/test/server_test_env.py does (#17172). A test's first t.TempDir() fixes the parent of all its later
	// ones, so no test calls it before this fixture.
	t.Setenv("TMPDIR", trustRoot)
	t.Setenv("GOTMPDIR", trustRoot)
	f := cliTest(t)
	t.Setenv(agentSandboxEnv, "off")
	managedFiles, managedDirs := claudeManagedSettings, claudeManagedSettingsDirs
	claudeManagedSettings, claudeManagedSettingsDirs = nil, nil
	t.Cleanup(func() { claudeManagedSettings, claudeManagedSettingsDirs = managedFiles, managedDirs })
	root, err := filepath.EvalSymlinks(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	adapter := filepath.Join(root, "example-adapter")
	native := filepath.Join(root, "example-native")
	for path, body := range map[string]string{adapter: externalTestAdapter, native: externalTestNative} {
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	target := proto.ExternalTarget{
		ID: "claude-code_" + strings.Repeat("a", 32), Name: "Example connection", Engine: "claude-code",
		Capabilities:    proto.ExternalCapabilities{Contexts: []string{"main-chat"}, Runtimes: []string{"cli"}, Platforms: []string{"posix"}},
		CredentialState: "unknown", Models: []proto.AgentModel{{ID: "example-model", Name: "Example model", Default: true}},
	}
	writeInventory := func(revision string) {
		t.Helper()
		target.Revision = revision
		data, err := json.Marshal(proto.ExternalInventory{Protocol: 1, Targets: []proto.ExternalTarget{target}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "external-inventory.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	revision := "sha256:" + strings.Repeat("1", 64)
	writeInventory(revision)
	config, err := json.Marshal(map[string]any{"version": 1, "id": "example", "command": []string{adapter}, "native_program": native})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "adapter-config.json")
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PERSONA_LAUNCHER_ADAPTER_CONFIG", configPath)
	f.n.external, err = loadExternalAdapterRegistry()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.n.external.Close)
	launcher := proto.ExternalLauncherName("example", target.ID)
	if len(warm) == 0 || warm[0] {
		launchers := f.n.external.launchers(context.Background(), true)
		if len(launchers) != 1 || launchers[0].Name != launcher {
			t.Fatalf("external inventory: %+v", launchers)
		}
	}
	id := "11111111-2222-4333-8444-555555555555"
	execution := modelExecution{
		Model: "example-model", Fingerprint: "explicit-model-pick", IsMain: true,
		External: &proto.ExternalExecution{Scope: proto.ExternalMainChatScope, SessionID: id,
			AdapterID: "example", TargetID: target.ID, Revision: revision},
	}
	return f, launcher, execution, writeInventory
}

func requireExternalCode(t *testing.T, err error, code string) {
	t.Helper()
	var wire *rpc.Error
	if !errors.As(err, &wire) || wire.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
	var detail map[string]any
	if err := json.Unmarshal(wire.Data, &detail); err != nil || detail["delivered"] != "none" {
		t.Fatalf("pre-execution refusal lost delivery state: %s", wire.Data)
	}
}

func TestExternalAdapterPersistedPickLoadsColdInventory(t *testing.T) {
	f, launcher, execution, _ := externalAssistantFixture(t, false)
	id := execution.External.SessionID
	if _, ok := f.n.external.lookup(launcher); ok {
		t.Fatal("fixture warmed the inventory before the persisted pick")
	}
	cwd := t.TempDir()
	f.n.state.put(id, sessionState{Launcher: launcher, Cwd: cwd, Model: execution.Model, IsMain: true})
	if _, err := f.n.assistantSend(t.Context(), id, cwd, launcher, "cold main-chat pick", nil, "default", "", execution); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	p := f.proc(id)
	if p == nil || p.model.External == nil || *p.model.External != *execution.External {
		t.Fatal("cold initialization changed the persisted target or revision")
	}
	if !strings.Contains(f.file("external-stdin"), "cold main-chat pick") {
		t.Fatal("cold persisted pick did not reach the native CLI")
	}
}

func TestExternalAdapterResidentHandoverLoadsColdInventory(t *testing.T) {
	f, launcher, execution, update := externalAssistantFixture(t)
	id, cwd := execution.External.SessionID, t.TempDir()
	if _, err := f.n.assistantSend(t.Context(), id, cwd, launcher, "before handover", nil, "default", "", execution); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	p := f.proc(id)
	pid := p.cmd.Process.Pid
	state, _ := f.n.state.get(id)
	keeper := newAssistantKeeper()
	f.n.keeper = keeper
	f.n.handOverAssistants()
	f.n.external.Close()
	t.Cleanup(keeper.stopAll)
	update("sha256:" + strings.Repeat("2", 64))
	binding := *execution.External
	binding.Revision = "sha256:" + strings.Repeat("2", 64)
	execution.External = &binding

	next := codexTestNode(t)
	next.home, next.dir, next.bin = f.n.home, f.n.dir, f.n.bin
	next.keeper = keeper
	next.state.put(id, state)
	withFakeHub(t, next)
	var err error
	next.external, err = loadExternalAdapterRegistry()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(next.external.Close)
	t.Cleanup(func() { next.stopAssistant(id) })
	if _, ok := next.external.lookup(launcher); ok {
		t.Fatal("new resident connection unexpectedly inherited the old registry cache")
	}
	if adopted := keeper.adopt(next); len(adopted) != 1 || adopted[0] != p || p.ownerOr(f.n) != next {
		t.Fatal("resident handover did not adopt the existing process")
	}
	if _, err := next.assistantSend(t.Context(), id, cwd, launcher, "after handover", nil, "default", "", execution); err != nil {
		t.Fatal(err)
	}
	g := &cliFixture{n: next, dir: f.dir}
	g.idle(t, id)
	if current := g.proc(id); current != p || current.cmd.Process.Pid != pid || current.model.External.Revision == binding.Revision {
		t.Fatal("cold registry initialization restarted the resident conversation or changed its actual binding")
	}
	if !strings.Contains(f.file("external-stdin"), "after handover") {
		t.Fatal("resident conversation did not receive the next message")
	}
}

func TestExternalAdapterColdInventoryStillValidatesTarget(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*proto.ExternalInventory)
		code string
	}{
		{"missing_target", func(inventory *proto.ExternalInventory) { inventory.Targets = []proto.ExternalTarget{} }, "adapter_target_not_found"},
		{"changed_identity", func(inventory *proto.ExternalInventory) { inventory.Targets[0].ID += "-other" }, "adapter_target_not_found"},
		{"changed_capability", func(inventory *proto.ExternalInventory) {
			inventory.Targets[0].Capabilities.Runtimes = []string{"agentsdk"}
		}, "adapter_runtime_unsupported"},
		{"changed_revision", func(inventory *proto.ExternalInventory) {
			inventory.Targets[0].Revision = "sha256:" + strings.Repeat("2", 64)
		}, "adapter_revision_mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, launcher, execution, _ := externalAssistantFixture(t, false)
			path := filepath.Join(f.dir, "external-inventory.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var inventory proto.ExternalInventory
			if err := json.Unmarshal(data, &inventory); err != nil {
				t.Fatal(err)
			}
			test.edit(&inventory)
			data, err = json.Marshal(inventory)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			_, err = f.n.assistantSend(t.Context(), execution.External.SessionID, t.TempDir(), launcher, "must not be delivered", nil, "default", "", execution)
			requireExternalCode(t, err, test.code)
			if f.file("external-argv") != "" {
				t.Fatal("a refused cold binding started the native CLI")
			}
		})
	}
}

func TestExternalAdapterAssistantAdmission(t *testing.T) {
	f, launcher, execution, _ := externalAssistantFixture(t)
	id := execution.External.SessionID
	if err := f.n.checkExternalAssistant(context.Background(), id, launcher, execution, false); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		edit func(*modelExecution)
		code string
	}{
		{"missing_scope", func(m *modelExecution) { m.External = nil }, "adapter_scope_required"},
		{"not_main", func(m *modelExecution) { m.IsMain = false }, "adapter_scope_required"},
		{"wrong_session", func(m *modelExecution) { m.External.SessionID = newUUID() }, "adapter_scope_required"},
		{"wrong_target", func(m *modelExecution) { m.External.TargetID = "another-target" }, "adapter_target_not_found"},
		{"sdk", func(m *modelExecution) { m.Runtime = "agentsdk" }, "adapter_runtime_unsupported"},
		{"unknown_runtime", func(m *modelExecution) { m.Runtime = "unknown-runtime" }, "adapter_runtime_unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := execution
			binding := *execution.External
			current.External = &binding
			test.edit(&current)
			requireExternalCode(t, f.n.checkExternalAssistant(context.Background(), id, launcher, current, false), test.code)
		})
	}
	requireExternalCode(t, f.n.checkExternalAssistant(context.Background(), id, launcher, execution, true), "adapter_scope_required")
	for _, value := range []string{"on", "not-a-policy"} {
		t.Setenv(agentSandboxEnv, value)
		requireExternalCode(t, f.n.checkExternalAssistant(context.Background(), id, launcher, execution, false), "adapter_sandbox_unsupported")
	}
}

func TestExternalAdapterRefusesCodingProbeAndSDKFallback(t *testing.T) {
	f, launcher, execution, _ := externalAssistantFixture(t)
	id := execution.External.SessionID
	execution.Runtime = "agentsdk"
	_, err := f.n.spawnClaude(id, t.TempDir(), launcher, false, false, execution)
	requireExternalCode(t, err, "adapter_runtime_unsupported")
	if f.file("start.json") != "" || f.file("external-argv") != "" {
		t.Fatal("unsupported SDK selection started a bridge or silently fell back to CLI")
	}
	_, err = f.n.startAsync(id, f.n.home, launcher, "hello", "", "default", "", "")
	requireExternalCode(t, err, "adapter_purpose_unsupported")
	_, err = f.n.start(context.Background(), f.n.home, launcher, "hello", "", "", "default", "", "", "", "")
	requireExternalCode(t, err, "adapter_purpose_unsupported")
	_, err = f.n.launcherProbe(context.Background(), launcher)
	requireExternalCode(t, err, "adapter_probe_unsupported")
	if f.n.launcherCommand(launcher) != "false" || f.n.launcherCommandIn(launcher, "", shellQuote) != "false" {
		t.Fatal("an external identity became a shell command")
	}
	if f.n.recoverAssistantResult(&assistantProc{launcher: launcher}, true, "API Error: overloaded") {
		t.Fatal("an explicit external pick entered automatic model recovery")
	}
}

func TestExternalAdapterMainChatLifecycleAndRevision(t *testing.T) {
	f, launcher, execution, update := externalAssistantFixture(t)
	id := execution.External.SessionID
	cwd := t.TempDir()
	if _, err := f.n.assistantSend(context.Background(), id, cwd, launcher, "hello", nil, "default", "", execution); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	first := f.proc(id)
	if first == nil || first.cmd.Process == nil {
		t.Fatal("native CLI process was not registered")
	}
	pid := first.cmd.Process.Pid
	settingsPath := strings.TrimSpace(f.file("external-settings-path"))
	if settingsPath == "" {
		t.Fatal("the node did not assign a private settings output path")
	}
	update("sha256:" + strings.Repeat("2", 64))
	f.n.external.launchers(context.Background(), true)
	binding := *execution.External
	binding.Revision = "sha256:" + strings.Repeat("2", 64)
	execution.External = &binding
	if _, err := f.n.assistantSend(context.Background(), id, cwd, launcher, "again", nil, "default", "", execution); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	if current := f.proc(id); current == nil || current.cmd.Process.Pid != pid || current.model.External.Revision == binding.Revision {
		t.Fatal("a catalog-only revision update replaced the active process or its actual binding")
	}
	f.n.stopAssistant(id)
	select {
	case <-first.exited:
	case <-time.After(5 * time.Second):
		t.Fatal("external process did not stop")
	}
	if _, err := os.Stat(settingsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private settings survived process cleanup: %v", err)
	}
	controlTranscript(t, f, id)
	if _, err := f.n.assistantSend(context.Background(), id, cwd, launcher, "resume", nil, "default", "", execution); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	if current := f.proc(id); current == nil || current.model.External.Revision != binding.Revision {
		t.Fatal("new native process did not adopt the current revision")
	}
	if !strings.Contains(f.file("external-argv"), "--resume "+id) {
		t.Fatalf("native resume arguments were lost: %s", f.file("external-argv"))
	}
}

func TestExternalAdapterRejectsManagedPolicyPresence(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(map[bool]string{false: "managed_file", true: "managed_directory"}[directory], func(t *testing.T) {
			f, _, _, _ := externalAssistantFixture(t)
			path := filepath.Join(t.TempDir(), "managed-policy")
			if directory {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				claudeManagedSettingsDirs = []string{path}
			} else {
				if err := os.WriteFile(path, []byte(`{"env":{"CLAUDE_CODE_USE_BEDROCK":"1"}}`), 0o600); err != nil {
					t.Fatal(err)
				}
				claudeManagedSettings = []string{path}
			}
			requireExternalCode(t, f.n.externalSettingsModeCheck(t.TempDir(), nil, nil), "adapter_config_conflict")
		})
	}
}

func TestExternalAdapterPolicyPathsIncludeEveryNativePlatform(t *testing.T) {
	profile := t.TempDir()
	programFiles := t.TempDir()
	t.Setenv("ProgramFiles", programFiles)
	paths := claudePolicyPaths(profile)
	for _, required := range []string{
		filepath.Join(profile, "remote-settings.json"),
		"/etc/claude-code/managed-settings.json", "/etc/claude-code/managed-settings.d",
		filepath.Join(programFiles, "ClaudeCode", "managed-settings.json"),
		filepath.Join(programFiles, "ClaudeCode", "managed-settings.d"),
	} {
		found := false
		for _, path := range paths {
			found = found || path == required
		}
		if !found {
			t.Fatalf("native policy location is not checked: %s", required)
		}
	}
}

func TestExternalAdapterRejectsRemotePolicyProviderMode(t *testing.T) {
	f, _, _, _ := externalAssistantFixture(t)
	profile := t.TempDir()
	path := filepath.Join(profile, "remote-settings.json")
	if err := os.WriteFile(path, []byte(`{"env":{"CLAUDE_CODE_USE_VERTEX":"1"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := f.n.externalSettingsModeCheck(t.TempDir(), nil, []string{"CLAUDE_CONFIG_DIR=" + profile})
	requireExternalCode(t, err, "adapter_config_conflict")
}

func TestExternalAdapterChecksLowerSettingsWithoutChangingNetworkPolicy(t *testing.T) {
	f, _, _, _ := externalAssistantFixture(t)
	cwd := t.TempDir()
	configDir := filepath.Join(cwd, ".claude")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configDir, "settings.local.json")
	for _, field := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		body, _ := json.Marshal(map[string]any{"env": map[string]string{field: "0"}})
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		requireExternalCode(t, f.n.externalSettingsModeCheck(cwd, nil, nil), "adapter_config_conflict")
	}
	body := `{"env":{"HTTPS_PROXY":"http://proxy.example.com","NO_PROXY":"models.example.com"},"permissions":{"defaultMode":"default"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"HTTPS_PROXY=http://proxy.example.com", "NO_PROXY=models.example.com"}
	if err := f.n.externalSettingsModeCheck(cwd, nil, env); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != body || env[0] != "HTTPS_PROXY=http://proxy.example.com" {
		t.Fatal("validating a connection changed the user's network or permission policy")
	}
	if err := os.WriteFile(path, []byte(`{"env":`), 0o600); err != nil {
		t.Fatal(err)
	}
	requireExternalCode(t, f.n.externalSettingsModeCheck(cwd, nil, nil), "adapter_config_conflict")
}

func TestExternalAdapterBusySelectionDoesNotInheritOldAuthority(t *testing.T) {
	f, launcher, execution, _ := externalAssistantFixture(t)
	id, cwd := execution.External.SessionID, t.TempDir()
	if _, err := f.n.assistantSend(context.Background(), id, cwd, launcher, "first", nil, "default", "", execution); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	p := f.proc(id)
	p.mu.Lock()
	p.busy = true
	p.mu.Unlock()
	before := f.file("external-stdin")
	ordinary := modelExecution{IsMain: true, Fingerprint: "ordinary-default"}
	_, err := f.n.assistantSend(context.Background(), id, cwd, "claude", "must not reach the old connection", nil, "default", "", ordinary)
	requireExternalCode(t, err, "adapter_busy")
	if f.file("external-stdin") != before {
		t.Fatal("changing away from an external pick sent new input to its old busy process")
	}
	if !p.alive() || f.proc(id) != p {
		t.Fatal("a refused selection stopped the ongoing turn")
	}
}

func TestExternalAdapterConfiguredProgram(t *testing.T) {
	encoded := os.Getenv("PERSONA_TEST_EXTERNAL_ADAPTER_COMMAND")
	if encoded == "" {
		t.Skip("set PERSONA_TEST_EXTERNAL_ADAPTER_COMMAND to exercise an installed protocol implementation")
	}
	var command []string
	if err := json.Unmarshal([]byte(encoded), &command); err != nil || len(command) == 0 {
		t.Fatal("PERSONA_TEST_EXTERNAL_ADAPTER_COMMAND must be a nonempty JSON argument array")
	}
	f, _, execution, _ := externalAssistantFixture(t)
	f.n.external.Close()
	root, err := filepath.EvalSymlinks(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{
		"version": 1, "id": "example", "command": command,
		"native_program": filepath.Join(root, "example-native"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "adapter-config.json"), config, 0o600); err != nil {
		t.Fatal(err)
	}
	f.n.external, err = loadExternalAdapterRegistry()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.n.external.Close)
	launchers := f.n.external.launchers(context.Background(), true)
	if len(launchers) == 0 {
		t.Fatal("configured adapter returned no targets")
	}
	chosen := launchers[0]
	if chosen.External == nil || len(chosen.External.Models) == 0 {
		t.Fatal("configured adapter returned no models")
	}
	execution.Model = chosen.External.Models[0].ID
	execution.External = &proto.ExternalExecution{
		Scope: proto.ExternalMainChatScope, SessionID: execution.External.SessionID,
		AdapterID: chosen.External.AdapterID, TargetID: chosen.External.ID, Revision: chosen.External.Revision,
	}
	id := execution.External.SessionID
	if _, err := f.n.assistantSend(context.Background(), id, t.TempDir(), chosen.Name, "synthetic adapter check", nil, "default", "", execution); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	if !strings.Contains(f.file("external-stdin"), "synthetic adapter check") {
		t.Fatal("configured adapter did not deliver the message to the fake native CLI")
	}
	f.n.stopAssistant(id)
}

func TestExternalAdapterStderrIsSanitized(t *testing.T) {
	p := &assistantProc{id: "11111111-2222-4333-8444-555555555555", launcher: proto.ExternalLauncherName("example", "target"),
		born: time.Now(), stderrDone: make(chan struct{})}
	buf := &syncBuffer{}
	f := cliTest(t)
	f.n.logger.SetOutput(buf)
	watchAssistantStderr(f.n.logger, p, io.NopCloser(strings.NewReader("error: synthetic-private-credential\nadapter_source_options_ignored\nadapter_revision_mismatch\n")))
	if strings.Contains(buf.String(), "synthetic-private-credential") || strings.Contains(strings.Join(p.stderrTail, " "), "synthetic-private-credential") {
		t.Fatal("adapter stderr leaked a credential to logs or session state")
	}
	if len(p.stderrTail) != 2 || p.stderrTail[1] != "adapter_revision_mismatch" {
		t.Fatalf("safe failure code was not retained: %v", p.stderrTail)
	}
}
