package node

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"time"
)

func TestModelAliasSettingsArePrivateAndIsolated(t *testing.T) {
	home := t.TempDir()
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	base := filepath.Join(home, "base.json")
	writeFile(t, base, `{"model":"main","env":{"ANTHROPIC_DEFAULT_SONNET_MODEL":"original","ANTHROPIC_DEFAULT_HAIKU_MODEL":"default-fast","EXAMPLE_AUTH":"test-value"}}`)
	start := func(model string) string {
		t.Helper()
		path, selected, err := n.claudeModelAliasSettings(t.Context(), "claude", home, base, proto.ModelAliases{"sonnet_model": model}, "")
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("settings must be private: %v", err)
		}
		settings := readSettings(t, path)
		env, _ := settings["env"].(map[string]any)
		if selected != "main" || settings["model"] != "main" || env["ANTHROPIC_DEFAULT_SONNET_MODEL"] != model ||
			env["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "default-fast" || env["EXAMPLE_AUTH"] != "test-value" {
			t.Fatal("override lost inherited settings or modified the main model")
		}
		return path
	}
	if start("vendor/one") == start("vendor/two") || readSettings(t, base)["env"].(map[string]any)["ANTHROPIC_DEFAULT_SONNET_MODEL"] != "original" {
		t.Fatal("a start overwrote shared settings")
	}
}

func TestDispatchClaudeEnvNeverInheritsNativeAliases(t *testing.T) {
	env := map[string]any{
		"ANTHROPIC_MODEL":                      "claude-opus-5-5",
		"ANTHROPIC_DEFAULT_SONNET_MODEL":       "vendor/claude-sonnet-5",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":        "haiku",
		"ANTHROPIC_DEFAULT_FABLE_MODEL":        "anthropic.claude-fable-5",
		"CLAUDE_CODE_SUBAGENT_MODEL":           "claude-haiku-4-5",
		"CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK": "0",
	}
	if model := dispatchClaudeEnv(env, proto.ModelAliases{"opus_model": "claude-opus-5-5"}, "vendor/main"); model != "vendor/main" {
		t.Fatal("dispatch inherited a native default")
	}
	for _, key := range []string{"ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL", "CLAUDE_CODE_WORKFLOW_SUBAGENT_MODEL"} {
		if env[key] != "vendor/main" {
			t.Fatalf("unexpected automatic model in %s", key)
		}
	}
	if env["CLAUDE_CODE_DISABLE_REFUSAL_FALLBACK"] != "1" || env["ANTHROPIC_DEFAULT_OPUS_MODEL"] != "claude-opus-5-5" {
		t.Fatal("dispatch did not disable native fallback or preserve the explicit opt-in")
	}
}

// An alias's own --settings is found as the shell would pass it: either form, the last of several, ~ and $HOME
// left for the node to expand. One without, or a function, has none.
func TestAliasOwnSettings(t *testing.T) {
	defs := `alias work="claude --settings ~/.claude/settings.work.json --dangerously-skip-permissions"
alias eqs='claude --settings=$HOME/s.json'
alias two="claude --settings a.json --settings b.json --model m"
alias plain="claude --model m"
alias other="ls --settings x"
`
	_, _, settings, _ := mergeLaunchers(asNode(t, defs, launcherScript))
	want := map[string]string{"work": "~/.claude/settings.work.json", "eqs": "$HOME/s.json", "two": "b.json"}
	if len(settings) != len(want) {
		t.Fatalf("settings %v, want %v", settings, want)
	}
	for k, v := range want {
		if settings[k] != v {
			t.Errorf("%s: %q, want %q", k, settings[k], v)
		}
	}
}

// withLauncherSettings is the launcher list's --settings for the test, put back after it.
func withLauncherSettings(t *testing.T, settings map[string]string) {
	t.Helper()
	launcherCache.Lock()
	old, oldAt := launcherCache.settings, launcherCache.at
	launcherCache.settings, launcherCache.at = settings, time.Now()
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.settings, launcherCache.at = old, oldAt
		launcherCache.Unlock()
	})
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func hookCommands(t *testing.T, s map[string]any, event string) []string {
	t.Helper()
	hooks, _ := s["hooks"].(map[string]any)
	list, _ := hooks[event].([]any)
	var out []string
	for _, m := range list {
		for _, h := range m.(map[string]any)["hooks"].([]any) {
			out = append(out, h.(map[string]any)["command"].(string))
		}
	}
	return out
}

// A claude start of a launcher with its own settings passes one file that is both: the launcher's env (its token)
// and keys, and Persona's hooks after its own. Claude Code keeps only the last --settings: before, the launcher's
// was dropped and claude was "Not logged in".
func TestClaudeSettingsMergesTheLaunchers(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	n := &Node{home: home, dir: filepath.Join(home, ".persona"), logger: log.New(io.Discard, "", 0)}
	ours := filepath.Join(n.dir, "claude-settings.json")
	writeFile(t, ours, `{"hooks":{"PreToolUse":[{"matcher":"AskUserQuestion","hooks":[{"type":"command","command":"personad hook"}]}],
		"Stop":[{"hooks":[{"type":"command","command":"personad hook"}]}]}}`)
	writeFile(t, filepath.Join(home, ".claude/settings.work.json"), `{"env":{"ANTHROPIC_AUTH_TOKEN":"tok","ANTHROPIC_BASE_URL":"https://gw.example.com"},
		"model":"m1","hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"their-hook"}]}]}}`)
	writeFile(t, filepath.Join(cwd, "rel.json"), `{"model":"rel"}`)
	withLauncherSettings(t, map[string]string{
		"work": "~/.claude/settings.work.json", "home": "$HOME/.claude/settings.work.json", "rel": "rel.json",
		"inline": `{"model":"inline"}`, "gone": "~/missing.json", "broken": "{not json",
	})
	ctx := context.Background()

	got := n.claudeSettings(ctx, "work", ours, cwd)
	if got != filepath.Join(n.dir, "launcher-settings", "work-claude-settings.json") {
		t.Fatalf("path %s", got)
	}
	s := readSettings(t, got)
	env, _ := s["env"].(map[string]any)
	if env["ANTHROPIC_AUTH_TOKEN"] != "tok" || env["ANTHROPIC_BASE_URL"] != "https://gw.example.com" || s["model"] != "m1" {
		t.Errorf("the launcher's settings are not all there: %v", s)
	}
	if c := hookCommands(t, s, "PreToolUse"); len(c) != 2 || c[0] != "their-hook" || c[1] != "personad hook" {
		t.Errorf("PreToolUse %v, want its own then Persona's", c)
	}
	if c := hookCommands(t, s, "Stop"); len(c) != 1 || c[0] != "personad hook" {
		t.Errorf("Stop %v", c)
	}
	if fi, err := os.Stat(got); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v (%v): it holds the launcher's token", fi.Mode(), err)
	}

	for launcher, model := range map[string]string{"home": "m1", "rel": "rel", "inline": "inline"} {
		if s := readSettings(t, n.claudeSettings(ctx, launcher, ours, cwd)); s["model"] != model || len(hookCommands(t, s, "Stop")) != 1 {
			t.Errorf("%s: %v", launcher, s)
		}
	}
	// Nothing to merge, or nothing that can be: Persona's file, as before.
	for _, launcher := range []string{"claude", "plain", "gone", "broken"} {
		if got := n.claudeSettings(ctx, launcher, ours, cwd); got != ours {
			t.Errorf("%s: %s, want %s", launcher, got, ours)
		}
	}
}

// A managed launcher's gateway must be in the settings file: settings env wins over the process env, so the user's
// global settings (another gateway) would otherwise replace it.
func TestManagedSettingsCarryTheLaunchersEnv(t *testing.T) {
	home := t.TempDir()
	n := &Node{home: home, dir: filepath.Join(home, ".persona"), logger: log.New(io.Discard, "", 0)}
	ours := filepath.Join(n.dir, "claude-settings.json")
	writeFile(t, ours, `{"env":{"ANTHROPIC_AUTH_TOKEN":"global"},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"personad hook"}]}]}}`)
	n.setManaged([]proto.ManagedLauncher{{Name: "m", BaseURL: "https://gw.example.com", Model: "m1", APIKey: "k"}})
	got := n.claudeSettings(context.Background(), "m", ours, home)
	if got == ours {
		t.Fatal("a managed launcher used Persona's bare settings")
	}
	s := readSettings(t, got)
	env, _ := s["env"].(map[string]any)
	if env["ANTHROPIC_BASE_URL"] != "https://gw.example.com" || env["ANTHROPIC_API_KEY"] != "k" {
		t.Errorf("the launcher's gateway is missing: %v", env)
	}
	if len(hookCommands(t, s, "Stop")) != 1 {
		t.Errorf("Persona's hooks are missing: %v", s)
	}
	if s["enableArtifact"] != false || env["CLAUDE_CODE_DISABLE_ARTIFACT"] != "1" {
		t.Errorf("Artifacts are not off for a gateway: enableArtifact %v, env %v", s["enableArtifact"], env["CLAUDE_CODE_DISABLE_ARTIFACT"])
	}
	if s["apiKeyHelper"] != "" {
		t.Errorf("the user-global apiKeyHelper must be disabled for a managed launcher: %v", s["apiKeyHelper"])
	}
}

// A managed launcher authenticates with its own gateway key: an apiKeyHelper anywhere in Persona's settings would still
// run and present another machine's token to the gateway, which rejects it with 401. The merged settings pin it off.
func TestManagedSettingsDisableTheAPIKeyHelper(t *testing.T) {
	home := t.TempDir()
	n := &Node{home: home, dir: filepath.Join(home, ".persona"), logger: log.New(io.Discard, "", 0)}
	ours := filepath.Join(n.dir, "claude-settings.json")
	writeFile(t, ours, `{"apiKeyHelper":"/home/me/.cpa/get-key","env":{"ANTHROPIC_API_KEY":"global"}}`)
	n.setManaged([]proto.ManagedLauncher{{Name: "m", BaseURL: "https://gw.example.com", Model: "m1", APIKey: "k"}})
	if s := readSettings(t, n.claudeSettings(context.Background(), "m", ours, home)); s["apiKeyHelper"] != "" {
		t.Errorf("apiKeyHelper %v, want the helper disabled", s["apiKeyHelper"])
	}
}

// Persona's own settings not written yet (a test's node; a write that failed): the managed launcher's gateway still
// goes in, alone, and nothing is logged (#1364: a node without a logger crashed here).
func TestManagedSettingsWithoutPersonasFile(t *testing.T) {
	home := t.TempDir()
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	ours := filepath.Join(n.dir, "claude-settings.json")
	n.setManaged([]proto.ManagedLauncher{{Name: "m", BaseURL: "https://gw.example.com", Model: "m1", APIKey: "k"}})
	got := n.claudeSettings(context.Background(), "m", ours, home)
	if got == ours {
		t.Fatal("the bare settings, which are not there")
	}
	if env, _ := readSettings(t, got)["env"].(map[string]any); env["ANTHROPIC_BASE_URL"] != "https://gw.example.com" {
		t.Errorf("the launcher's gateway is missing: %v", env)
	}
	if readSettings(t, got)["enableArtifact"] != false {
		t.Error("Artifacts are not off in the env-only settings (the hooks-as-plugin start)")
	}
	if readSettings(t, got)["apiKeyHelper"] != "" {
		t.Error("the apiKeyHelper is not disabled in the env-only settings (the hooks-as-plugin start)")
	}
	writeFile(t, ours, `null`)
	if env, _ := readSettings(t, n.claudeSettings(context.Background(), "m", ours, home))["env"].(map[string]any); env["ANTHROPIC_API_KEY"] != "k" {
		t.Errorf("settings that say null: %v", env)
	}
}

// A managed launcher's start gets its own settings file (#17035): the launcher's gateway and the model this start
// runs, with the per-start aliases on top. The shared "<launcher>-env-settings.json" is never changed by a start, so
// one conversation's alias cannot reach another's, and two starts never share a file.
func TestModelAliasSettingsForAManagedLauncher(t *testing.T) {
	home := t.TempDir()
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	n.setManaged([]proto.ManagedLauncher{{Name: "m", BaseURL: "https://gw.example.com", Model: "gateway-model", APIKey: "secret"}})
	l, ok := n.managedLauncher("m")
	if !ok {
		t.Fatal("the managed launcher is gone")
	}
	shared, err := n.mergeManagedSettings("m", l, "", home)
	if err != nil {
		t.Fatal(err)
	}
	// The start that brings no alias of its own still runs on the launcher's gateway and model: it cannot use the
	// shared file itself, since another start's alias must not be in it.
	provided, selected, err := n.claudeModelAliasSettings(t.Context(), "m", home, shared, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if selected != "gateway-model" {
		t.Fatalf("selected %q, want the gateway's model", selected)
	}
	s := readSettings(t, provided)
	env, _ := s["env"].(map[string]any)
	if s["model"] != "gateway-model" || env["ANTHROPIC_MODEL"] != "gateway-model" ||
		env["ANTHROPIC_BASE_URL"] != "https://gw.example.com" || env["ANTHROPIC_API_KEY"] != "secret" {
		t.Fatalf("the launcher's gateway and model are missing: %v", s)
	}
	// A second start with its own alias gets another private file; neither it nor the shared file changes what the
	// first start wrote.
	aliased, selected, err := n.claudeModelAliasSettings(t.Context(), "m", home, shared,
		proto.ModelAliases{"sonnet_model": "vendor/sonnet"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if selected != "gateway-model" || aliased == provided {
		t.Fatalf("second start: settings %q (first %q), selected %q; want its own file", aliased, provided, selected)
	}
	if env, _ := readSettings(t, aliased)["env"].(map[string]any); env["ANTHROPIC_DEFAULT_SONNET_MODEL"] != "vendor/sonnet" {
		t.Fatalf("the per-start alias is missing: %v", env)
	}
	for name, path := range map[string]string{"the shared settings": shared, "the first start": provided} {
		if env, _ := readSettings(t, path)["env"].(map[string]any); env["ANTHROPIC_DEFAULT_SONNET_MODEL"] == "vendor/sonnet" {
			t.Errorf("a per-start alias reached %s: %v", name, env)
		}
	}
}
