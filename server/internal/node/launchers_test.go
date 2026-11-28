package node

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// fixedBins are the bin directories launcherScript looks for codex, traex and omp in besides the PATH and
// ~/.local/bin. They are this machine's, not the test's: asNode leaves them out (#5547).
const fixedBins = ":/opt/homebrew/bin:/usr/local/bin"

// asNode runs script the way the node does (loginShell: `zsh -ic`), with defs as the ~/.zshrc it sources first
// (aliases only expand in a script parsed after they were defined), in a minimal environment: nothing of the test
// runner's leaks into the output, nor what is installed on this machine.
func asNode(t *testing.T, defs, script string) string {
	t.Helper()
	return asNodeIn(t, t.TempDir(), defs, script)
}

// asNodeIn is asNode with dir as the HOME, for a test that puts something in it first.
func asNodeIn(t *testing.T, dir, defs, script string) string {
	t.Helper()
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("no zsh")
	}
	if err := os.WriteFile(filepath.Join(dir, ".zshrc"), []byte(defs), 0o600); err != nil {
		t.Fatal(err)
	}
	script = strings.ReplaceAll(script, fixedBins, "")
	cmd := exec.CommandContext(context.Background(), zsh, "-ic", script)
	cmd.Env = []string{"HOME=" + dir, "ZDOTDIR=" + dir, "PATH=/usr/bin:/bin", "USER=tester", "TERM=dumb"}
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		t.Fatalf("zsh: %v", err)
	}
	return string(out)
}

// listFrom is the node's launcher list for a ~/.zshrc made of defs.
func listFrom(t *testing.T, defs string) []proto.Launcher {
	t.Helper()
	list, _, _, _ := mergeLaunchers(asNode(t, defs, launcherScript))
	return withoutInstalledWorkAgents(list)
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func byName(list []proto.Launcher) map[string]proto.Launcher {
	m := map[string]proto.Launcher{}
	for _, l := range list {
		m[l.Name] = l
	}
	return m
}

// Aliases that start claude are launchers, with the gateway and model they set; the others are not. An alias that
// runs codex is a Codex launcher, after Claude's (codex_start.go).
func TestAliasLaunchers(t *testing.T) {
	list := listFrom(t, fixture(t, "aliases.zsh"))
	var names []string
	for _, l := range list {
		names = append(names, l.Name)
	}
	want := []string{"dmcc", "envcc", "expcc", "kcc", "mycc", "ncc", "pathcc", "spaced", "ncodex"}
	if !slices.Equal(names, want) {
		t.Fatalf("launchers %v, want %v (Claude's sorted, then Codex's; no alias named claude, no ls / git)", names, want)
	}
	if l := list[len(list)-1]; l.Agent != "codex" {
		t.Errorf("ncodex: %+v", l)
	}
	m := byName(list)
	for name, w := range map[string][2]string{
		"kcc":    {"https://gw-a.example.com/anthropic", "model-a[1m]"},
		"dmcc":   {"https://gw-b.example.com", ""},
		"spaced": {"https://gw-c.example.com", "model with space"},
		"mycc":   {"", "model-default"},
		"ncc":    {"", ""},
		"envcc":  {"https://gw-d.example.com", ""},
		"pathcc": {"https://gw-e.example.com", "flag-model"},
		"expcc":  {"https://gw-f.example.com", "eq-model"},
	} {
		if l := m[name]; l.BaseURL != w[0] || l.Model != w[1] || l.Source != "zshrc" {
			t.Errorf("%s: base %q model %q source %q, want %q %q zshrc", name, l.BaseURL, l.Model, l.Source, w[0], w[1])
		}
	}
	for _, l := range list {
		if strings.Contains(l.BaseURL+l.Model, "fake") {
			t.Errorf("%s carries a key: %+v", l.Name, l)
		}
	}
}

// A ~/.zshrc with the `_claude_launchers` helper keeps its list first; aliases it names are not repeated.
func TestHelperAndAliasesTogether(t *testing.T) {
	helper := `_claude_launchers() {
  printf '%-26s%-40s%s\n' kcc https://helper.example.com helper-model
  printf '%-26s%-40s%s\n' cc_fn https://fn.example.com fn-model
}`
	list := listFrom(t, helper+"\n"+fixture(t, "aliases.zsh"))
	if list[0].Name != "kcc" || list[1].Name != "cc_fn" {
		t.Fatalf("helper's launchers not first: %v", list[:2])
	}
	m := byName(list)
	if m["kcc"].BaseURL != "https://helper.example.com" {
		t.Errorf("kcc from the alias overrode the helper's: %+v", m["kcc"])
	}
	n := 0
	for _, l := range list {
		if l.Name == "kcc" {
			n++
		}
	}
	if n != 1 || m["dmcc"].Name == "" {
		t.Errorf("kcc listed %d times, dmcc %+v", n, m["dmcc"])
	}
}

// A leftover shell wrapper does not make the built-in Claude launcher available without its executable.
func TestBuiltinClaudeRequiresExecutable(t *testing.T) {
	for _, sh := range []struct {
		name string
		run  func(*testing.T, string, string, string) string
	}{{"zsh", asNodeIn}, {"bash", asBashIn}} {
		t.Run(sh.name, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, "bin")
			defs := `export PATH="$HOME/bin:$PATH"
claude() { command claude "$@"; }`
			list, _, _, _ := mergeLaunchers(sh.run(t, home, defs, launcherScript))
			if _, ok := byName(list)["claude"]; ok {
				t.Fatalf("advertised Claude with only a shell wrapper: %v", list)
			}
			writeScript(t, filepath.Join(bin, "claude"), "#!/bin/sh\nexit 1\n", 0o755)
			list, _, _, _ = mergeLaunchers(sh.run(t, home, defs, launcherScript))
			if _, ok := byName(list)["claude"]; !ok {
				t.Fatalf("missing installed Claude: %v", list)
			}
		})
	}
}

// codex found by the script is a launcher of its own: here one in ~/.local/bin, where the node looks besides the PATH.
// The script's other fixed directories are this machine's (fixedBins): asNode leaves them out, and a change to them
// is told here.
func TestCodexOnTheMachineIsALauncher(t *testing.T) {
	if !strings.Contains(launcherScript, `PATH="$PATH:$HOME/.local/bin`+fixedBins+`"`) {
		t.Fatal("launcherScript's directories changed: update fixedBins")
	}
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	list, codex, _, _ := mergeLaunchers(asNodeIn(t, home, `alias ll="ls -l"`, launcherScript))
	list = withoutInstalledWorkAgents(list)
	if len(list) != 1 || list[0].Name != "codex" || list[0].Agent != "codex" || len(codex["codex"]) != 1 {
		t.Fatalf("got %v, codex %v", list, codex)
	}
}

func TestCodexLauncherAliasProvenance(t *testing.T) {
	plain := launcherBegin + "\x1d\x1d/usr/local/bin/codex\n" + launcherEnd
	if names := codexAliasNames(plain); len(names) != 0 {
		t.Fatalf("plain Codex aliases = %v, want none", names)
	}
	noOpAlias := launcherBegin + "\x1dcodex\x1fcodex\x1f\x1e\x1d/usr/local/bin/codex\n" + launcherEnd
	if names := codexAliasNames(noOpAlias); !names["codex"] {
		t.Fatalf("configured no-op Codex alias lost its provenance: %v", names)
	}
}

// The parser on its own: separators, env flags, a wrapper with nothing after it.
func TestParseAliasLaunchers(t *testing.T) {
	rec := func(words ...string) string { return strings.Join(words, "\x1f") + "\x1f\x1e" }
	out := rec("a", "env", "-i", "ANTHROPIC_MODEL=m", "claude") +
		rec("b", "cd", "/tmp", "&&", "ANTHROPIC_BASE_URL=https://x.example.com", "claude") +
		rec("c", "command") +
		rec("d", "ANTHROPIC_BASE_URL=https://y.example.com", "traex") +
		rec("bad name", "claude") +
		"junk-without-separators"
	got, _ := parseAliasLaunchers(out)
	if len(got) != 2 || got[0].Name != "a" || got[0].Model != "m" || got[1].Name != "b" || got[1].BaseURL != "https://x.example.com" {
		t.Fatalf("got %+v", got)
	}
}

// The launcher page's probe works on aliases too: the variables in front of claude reach it.
func TestProbeOfAnAlias(t *testing.T) {
	out := asNode(t, fixture(t, "aliases.zsh"), probeScript("dmcc"))
	p, ok := parseProbe(out)
	if !ok {
		t.Fatalf("no probe output (%d lines, agent line: %v)", strings.Count(out, "\n"), strings.Contains(out, probeMarker+" agent"))
	}
	d := LauncherDetail(p)
	if d.Agent != "claude" || d.BaseURL != "https://gw-b.example.com" || d.Auth != "ANTHROPIC_AUTH_TOKEN" {
		t.Fatalf("%+v", d)
	}
	if !slices.Contains(d.Args, "--dangerously-skip-permissions") {
		t.Errorf("args %v", d.Args)
	}
	for _, e := range d.Env {
		if strings.Contains(e.Value, "fake") {
			t.Errorf("a key in the detail: %s=%s", e.Key, e.Value)
		}
	}
}

// What "View definition" shows for an alias (whence -f prints its text): keys masked, a token starting with "-" too.
func TestRedactAliasDefinition(t *testing.T) {
	def := `CLAUDE_CODE_NO_FLICKER=1 ANTHROPIC_BASE_URL=https://gw-b.example.com ANTHROPIC_AUTH_TOKEN=-fakeTokenWithALeadingDash000000000000 claude --dangerously-skip-permissions`
	got := redactSecrets(def)
	if strings.Contains(got, "fakeToken") || !strings.Contains(got, "https://gw-b.example.com") || !strings.Contains(got, "--dangerously-skip-permissions") {
		t.Fatalf("redacted: %s", got)
	}
	if got := redactSecrets(`ANTHROPIC_AUTH_TOKEN=sk-fake-aaaaaaaaaaaaaaaaaaaa claude`); strings.Contains(got, "aaaaaaaa") {
		t.Fatalf("redacted: %s", got)
	}
}

// withoutInstalledWorkAgents drops pi, cursor and agy. The launcher script lists them whenever the program is
// installed, and this machine may have one; the assertions here are about the rc file, not those programs.
func withoutInstalledWorkAgents(list []proto.Launcher) []proto.Launcher {
	out := make([]proto.Launcher, 0, len(list))
	for _, l := range list {
		if l.Name == "pi" || l.Name == "cursor" || l.Name == "agy" {
			continue
		}
		out = append(out, l)
	}
	return out
}

// pi, the Cursor CLI and the Antigravity CLI on PATH are launchers of their own, after the Claude and Codex ones.
func TestWorkAgentsOnPathAreLaunchers(t *testing.T) {
	parts := []string{"", "", "", "", "", "/usr/bin/pi", "", "/usr/local/bin/agy", "", ""}
	list, _, _, _ := mergeLaunchers(launcherBegin + strings.Join(parts, "\x1d") + launcherEnd)
	var got []string
	for _, l := range list {
		got = append(got, l.Name+"="+l.Agent)
	}
	want := []string{"pi=pi", "agy=agy"}
	if !slices.Equal(got, want) {
		t.Fatalf("launchers %v, want %v", got, want)
	}
}

// A launcher that names no model has no model on the wire either. The Chinese display placeholder older nodes sent is
// not a model ID; an explicit Codex alias model stays exactly as the alias wrote it.
func TestMergeLaunchersLeavesLauncherDefaultModelsEmpty(t *testing.T) {
	rec := func(words ...string) string { return strings.Join(words, "\x1f") + "\x1f\x1e" }
	parts := []string{"", rec("cx-plain", "codex") + rec("cx-explicit", "codex", "--model", "gpt-explicit"),
		"/usr/bin/codex", "/usr/bin/traex", "/usr/bin/omp", "/usr/bin/pi", "/usr/bin/cursor-agent", "/usr/bin/agy", "", "", "/usr/bin/claude"}
	list, _, _, _ := mergeLaunchers(launcherBegin + strings.Join(parts, "\x1d") + launcherEnd)
	models := byName(list)
	for _, name := range []string{"claude", "codex", "cx-plain", "traex", "omp", "pi", "cursor", "agy"} {
		if l := models[name]; l.Name != name || l.Model != "" {
			t.Errorf("%s: model %q, want no explicit model (launcher %+v)", name, l.Model, l)
		}
	}
	if got := models["cx-explicit"].Model; got != "gpt-explicit" {
		t.Errorf("explicit model = %q, want gpt-explicit", got)
	}
	if l := models[traexAgent]; l.Agent != traexAgent || l.DisplayName != "TRAE CLI" {
		t.Errorf("TRAE CLI launcher kind or display name changed: %+v", l)
	}
	wantDisplay := map[string]string{
		"claude":   "Claude Code",
		"codex":    "Codex",
		"omp":      "Oh My Pi",
		"pi":       "",
		"cursor":   "Cursor",
		"agy":      "Antigravity CLI",
		"cx-plain": "",
	}
	for name, want := range wantDisplay {
		if got := models[name].DisplayName; got != want {
			t.Errorf("%s display name = %q, want %q", name, got, want)
		}
	}
}

func TestAliasLauncherEnvironmentReset(t *testing.T) {
	words := []string{"export", "ANTHROPIC_BASE_URL=https://old.example.com", ";",
		"ANTHROPIC_MODEL=old", "env", "-i", "claude", "--model", "explicit"}
	got, _, ok := aliasLauncher(words)
	if !ok || got.BaseURL != "" || got.Model != "explicit" {
		t.Errorf("launcher after environment reset: %+v, found=%t", got, ok)
	}
}
