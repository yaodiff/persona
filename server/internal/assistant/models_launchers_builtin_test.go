package assistant

import (
	"context"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A shell wrapper does not replace the computer's built-in claude (#4660). The node's list omits plain claude when
// it finds a wrapper, while devices.list has already reported that Claude Code is installed.
func TestMachineLaunchersIncludeReportedBuiltinClaude(t *testing.T) {
	s, f, _ := modelService(t)
	ctx := context.Background()
	f.devices = []Device{
		{ID: "server", Name: "server", Online: true, Reachable: true, ClaudeVersion: "2.1.322"},
		{ID: "without-version", Name: "without-version", Online: true, Reachable: true},
	}
	f.sessions["without-version"] = nil
	f.zshrc = map[string][]proto.Launcher{
		"server":          {{Name: "work", Source: "zshrc"}},
		"without-version": {{Name: "work", Source: "zshrc"}},
	}

	got, err := s.machineLaunchers(ctx, "server")
	if err != nil {
		t.Fatal(err)
	}
	builtins := slices.DeleteFunc(slices.Clone(got), func(l proto.Launcher) bool { return l.Name != "claude" })
	if len(builtins) != 1 || builtins[0].Source != "builtin" || builtins[0].DisplayName != "Claude Code" {
		t.Fatalf("picker with a shell wrapper: %+v", got)
	}
	if !slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "work" }) {
		t.Fatalf("the shell wrapper disappeared: %+v", got)
	}
	if names := launcherNamesAt(t, s, "/api/v1/machines/server/launchers"); !slices.Contains(names, "claude") || !slices.Contains(names, "work") {
		t.Fatalf("the picker route offers %v", names)
	}

	// A version reported for another computer, or wrappers alone, are not evidence that this computer has claude.
	got, err = s.machineLaunchers(ctx, "without-version")
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "claude" }) {
		t.Fatalf("a computer without a reported Claude Code version gained one: %+v", got)
	}

	// Hide / restore keeps working while wrappers remain.
	if err := s.putSetting(ctx, launchersHiddenKey, []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.machineLaunchers(ctx, "server")
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(got, func(l proto.Launcher) bool { return l.Name == "claude" }) {
		t.Fatalf("the hidden built-in is still offered: %+v", got)
	}
	if err := s.putSetting(ctx, launchersHiddenKey, []string{}); err != nil {
		t.Fatal(err)
	}

	// Newer nodes that list claude themselves, and a hub.json override, must not create a second row.
	f.zshrc["server"] = append(f.zshrc["server"], proto.Launcher{Name: "claude"})
	got, err = s.machineLaunchers(ctx, "server")
	if err != nil {
		t.Fatal(err)
	}
	if n := len(slices.DeleteFunc(slices.Clone(got), func(l proto.Launcher) bool { return l.Name != "claude" })); n != 1 {
		t.Fatalf("node-listed claude appears %d times: %+v", n, got)
	}
	f.zshrc["server"] = f.zshrc["server"][:1]
	f.hello.Models.Launchers = map[string][]ConfiguredLauncher{
		"server": {{Launcher: proto.Launcher{Name: "claude", Model: "configured-model"}, AgentType: "claude"}},
	}
	picker, err := s.pickerLaunchers(ctx, "server")
	if err != nil {
		t.Fatal(err)
	}
	configured := slices.DeleteFunc(slices.Clone(picker), func(l ConfiguredLauncher) bool { return l.Name != "claude" })
	if len(configured) != 1 || configured[0].Model != "configured-model" || configured[0].Source != "builtin" {
		t.Fatalf("configured claude picker: %+v", picker)
	}
}
