package node

import (
	"context"
	"slices"
	"strings"

	"github.com/anmitsu/go-shlex"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/orcart"
)

// stockOrcaCodexArgv distinguishes the built-in stock launcher from a configured no-op alias
// with the same words. Discovery records alias provenance beside the shared launcher argv map.
func stockOrcaCodexArgv(launcher string, argv []string) bool {
	if launcher != "codex" || len(argv) != 1 || argv[0] != "codex" {
		return false
	}
	launcherCache.Lock()
	configured := launcherCache.codexAliases["codex"]
	if launcherCache.codexAliases == nil {
		// Test fixtures and a start before the first discovery carry no provenance map.
		_, configured = launcherCache.codex["codex"]
	}
	launcherCache.Unlock()
	return !configured
}

// codexOrcaLaunchDefaults follows Orca's stock Codex launch profile. Configured launchers and explicit
// Persona modes do not call this: their own complete launch configuration stays authoritative.
func codexOrcaLaunchDefaults(ctx context.Context, launch host.Launcher, argv []string) ([]string, error) {
	h, ok := launch.(*orcaHost)
	if !ok || len(argv) != 1 || argv[0] != "codex" {
		return argv, nil
	}
	var res struct {
		Settings *struct {
			Commands map[string]string   `json:"agentCmdOverrides"`
			Args     map[string]string   `json:"agentDefaultArgs"`
			Env      map[string][]string `json:"environmentNames"`
		} `json:"settings"`
	}
	if err := h.c.Call(ctx, orcart.CallTimeout, "settings.getAgentLaunch", nil, &res); err != nil || res.Settings == nil {
		// No fallback: an old or unavailable host cannot confirm which preferences it would use.
		return nil, launchPreferencesRefusal("unreadable")
	}
	settings := res.Settings
	if command := settings.Commands["codex"]; (command != "" && command != "codex") || len(settings.Env["codex"]) > 0 {
		return nil, launchPreferencesRefusal("custom")
	}
	raw := settings.Args["codex"]
	if strings.ContainsAny(raw, "\n\r") {
		return nil, launchPreferencesRefusal("shell")
	}
	if _, err := shlex.Split(raw, true); err != nil {
		return nil, launchPreferencesRefusal("quoting")
	}
	args := shellWords(raw)
	// Execute literal argv, never evaluate shell operators, substitutions or variables.
	for _, arg := range args {
		if strings.ContainsAny(arg, "$`<>\x00~*?[") || slices.Contains([]string{";", "&", "&&", "|", "||", ">", "<"}, arg) {
			return nil, launchPreferencesRefusal("shell")
		}
	}
	// A cached launcher's slice belongs to every later start; never append to it directly.
	return append(slices.Clone(argv), args...), nil
}

// launchPreferencesRefusal refuses a stock Codex start whose Orca launch settings Persona cannot follow, reason saying
// why: they cannot be read (unreadable), Orca's launcher has a command or environment of its own (custom), its
// arguments need a shell (shell) or leave a quote open (quoting). The hub words it by its reason (apierr's reasonKeys,
// #17057); the words here stay English, as they were for a hub that does not.
func launchPreferencesRefusal(reason string) error {
	return refusal("codex_launch_preferences", i18n.T(i18n.En, "rpc-errors.codex_launch_preferences_"+reason), map[string]any{"reason": reason})
}
