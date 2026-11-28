package node

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Agent start speed is a one-start choice, never a launcher default or a resume
// setting. Validation and coded refusals live here so every start path agrees.
func speedRefusal(code, reason string) error {
	return refusal(code, reason, map[string]any{"reason": reason})
}

func checkStartSpeedValue(speed, resume string) error {
	if !proto.ValidSpeed(speed) {
		return speedRefusal("agent_speed_invalid", fmt.Sprintf("Agent speed %q is not standard, fast or ultrafast; choose one of those or leave speed out", speed))
	}
	if proto.FasterSpeed(speed) && resume != "" {
		return speedRefusal("agent_speed_start_only", fmt.Sprintf("Speed %q applies only to a new Agent start, not a resume; leave speed out to use the launcher's own setting", speed))
	}
	return nil
}

func unsupportedStartSpeed(launcher, speed string) error {
	return speedRefusal("agent_speed_unsupported", fmt.Sprintf("Launcher %q cannot apply speed %q to a new Agent start; leave speed out or choose standard", launcher, speed))
}

// prepareSessionStartSpeed rejects requests that would otherwise reach a
// desktop app or external runtime before those dispatch paths can start anything.
func (n *Node) prepareSessionStartSpeed(ctx context.Context, speed, launcher, host, resume string) (string, error) {
	speed = strings.TrimSpace(speed)
	if err := checkStartSpeedValue(speed, resume); err != nil {
		return "", err
	}
	if !proto.FasterSpeed(speed) {
		return speed, nil
	}
	if !n.terminalStartSpeedSupported(withStartHost(ctx, host), launcher) {
		return "", unsupportedStartSpeed(launcher, speed)
	}
	return speed, nil
}

func (n *Node) terminalStartSpeedSupported(ctx context.Context, launcher string) bool {
	if proto.IsExternalLauncher(launcher) || isDesktopLauncher(launcher) || isCodexAppLauncher(launcher) {
		return false
	}
	if _, runtime := engineRuntimes[n.startKind(ctx)]; runtime {
		return false
	}
	h, _ := n.startRuntimeIn(ctx, launcher)
	return h == nil
}

func (n *Node) checkStartServiceTier(ctx context.Context, tier, speed, launcher, resume string) error {
	if tier == "" {
		return nil
	}
	if speed != "" || (tier != "priority" && tier != "ultrafast") {
		return speedRefusal("agent_speed_invalid", i18n.T(i18n.From(ctx), "rpc-errors.agent_speed_tier_value"))
	}
	if resume != "" {
		return speedRefusal("agent_speed_start_only", i18n.T(i18n.From(ctx), "rpc-errors.agent_speed_tier_new"))
	}
	if !n.terminalStartSpeedSupported(ctx, launcher) {
		return unsupportedStartSpeed(launcher, tier)
	}
	return nil
}

// applyStartSpeed resolves the actual terminal dispatch, which can differ from
// a declared agent type when the launcher is Codex.
func (m *modelExecution) applyStartSpeed(launcher, agent string, codex bool) error {
	speed := m.Speed
	if !proto.FasterSpeed(speed) {
		m.Speed = ""
		return nil
	}
	if codex && agent != traexAgent {
		agent = "codex"
	} else if agent == "codex" {
		// A name alone does not prove that this is the terminal Codex path.
		return unsupportedStartSpeed(launcher, speed)
	}
	if !proto.SpeedSupported(agent, speed) {
		return unsupportedStartSpeed(launcher, speed)
	}
	if agent == "codex" {
		m.ServiceTier = proto.CodexServiceTier(speed)
	} else {
		m.FastMode = true
	}
	return nil
}

// checkCodexStartSpeed verifies the selected model's own catalog. Codex accepts
// an unadvertised tier without complaining, so argv alone is not validation.
func (n *Node) checkCodexStartSpeed(ctx context.Context, launcher, model, tier string) error {
	catalog, err := n.launcherModels(ctx, launcher, "codex", false)
	selected := model
	if selected == "" {
		selected = n.launcherDefaultModel(launcher)
	}
	var found *proto.AgentModel
	if err == nil && catalog.Agent == "codex" {
		for i := range catalog.Models {
			m := &catalog.Models[i]
			if selected != "" && (m.ID == selected || m.Resolved == selected || slices.Contains(m.Aliases, selected)) {
				found = m
				break
			}
			if selected == "" && m.Default {
				found = m
				break
			}
		}
	}
	if found != nil && slices.ContainsFunc(found.ServiceTiers, func(t proto.ServiceTier) bool { return t.ID == tier }) {
		return nil
	}
	name := selected
	if name == "" {
		name = "the launcher's default model"
	}
	return speedRefusal("agent_speed_model", fmt.Sprintf("Codex model %q does not advertise service tier %q; choose an advertised speed or standard", name, tier))
}

// claudeFastModeSettings returns a distinct settings file for the initial
// command only. The base settings remain untouched for herdr's automatic resume.
func (n *Node) claudeFastModeSettings(ctx context.Context, launcher, settings, cwd string) (string, error) {
	fastBytes, _ := json.MarshalIndent(map[string]any{"fastMode": true}, "", "  ")
	if settings == "" {
		if managed, ok := n.managedLauncher(launcher); ok {
			ours, err := writeSettings(n.dir, "fast-mode-settings.json", fastBytes)
			if err != nil {
				return "", err
			}
			return n.mergeManagedSettings(launcher, managed, ours)
		}
		if own := n.launcherOwnSettings(ctx, launcher); own != "" {
			ours, err := writeSettings(n.dir, "fast-mode-settings.json", fastBytes)
			if err != nil {
				return "", err
			}
			return n.mergeLauncherSettings(launcher, own, ours, cwd)
		}
		if launcher != "claude" {
			// A function can append --settings after Persona's. Without seeing it, adding ours would discard the
			// launcher's own settings because Claude Code keeps only the last one.
			probe, err := n.launcherProbe(ctx, launcher)
			if err != nil {
				return "", fmt.Errorf("inspect launcher settings for fast mode: %w", err)
			}
			if settingsArg(probe.Args) != "" {
				return "", fmt.Errorf("launcher %q has --settings that cannot be merged for fast mode", launcher)
			}
		}
		return writeSettings(n.dir, "fast-mode-settings.json", fastBytes)
	}
	base, err := readLauncherSettings(settings, cwd, n.home)
	if err != nil {
		return "", err
	}
	out, err := json.MarshalIndent(mergeClaudeSettings(base, map[string]any{"fastMode": true}), "", "  ")
	if err != nil {
		return "", err
	}
	name := cmp.Or(launcher, "claude") + "-fast-" + filepath.Base(settings)
	return writeSettings(n.dir, name, out)
}
