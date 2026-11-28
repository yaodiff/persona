package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

const doctorDialTimeout = 3 * time.Second

func checkNodeSocket(ctx context.Context, path string) DoctorCheck {
	return checkNodeSocketIn(ctx, path, i18n.Default)
}

func checkNodeSocketIn(ctx context.Context, path string, lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: "node.sock"}
	if _, err := os.Stat(path); err != nil {
		c.Status, c.Detail = DoctorFail, i18n.T(lang, "node-doctor.socket_missing")
		c.Hint = i18n.T(lang, "node-doctor.node_socket_missing_hint")
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	line, err := nodeclient.DialAt(ctx, path, sockRequest{Kind: "ping"})
	if err != nil {
		c.Status = DoctorFail
		c.Detail = i18n.T(lang, "node-doctor.node_socket_unanswered", i18n.Args{"error": shorten(err.Error(), 80)})
		c.Hint = i18n.T(lang, "node-doctor.socket_stale_hint")
		return c
	}
	if !json.Valid(line) {
		c.Status, c.Detail = DoctorFail, i18n.T(lang, "node-doctor.node_socket_invalid")
		c.Hint = i18n.T(lang, "node-doctor.node_socket_invalid_hint")
		return c
	}
	c.Status, c.Detail = DoctorOK, i18n.T(lang, "node-doctor.node_socket_ok")
	return c
}

func checkRuntimeSocket(ctx context.Context, path string) DoctorCheck {
	return checkRuntimeSocketIn(ctx, path, i18n.Default)
}

func checkRuntimeSocketIn(ctx context.Context, path string, lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: "runtime.sock"}
	if _, err := os.Stat(path); err != nil {
		c.Status, c.Detail = DoctorWarn, i18n.T(lang, "node-doctor.socket_missing")
		c.Hint = i18n.T(lang, "node-doctor.runtime_socket_missing_hint")
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		c.Status = DoctorFail
		c.Detail = i18n.T(lang, "node-doctor.runtime_socket_unavailable", i18n.Args{"error": shorten(err.Error(), 80)})
		c.Hint = i18n.T(lang, "node-doctor.socket_stale_hint")
		return c
	}
	conn.Close()
	c.Status, c.Detail = DoctorOK, i18n.T(lang, "node-doctor.runtime_socket_ok")
	return c
}

// ClaudeVersionUnanswered is the claude-version check's state when no version was read but the shell did find claude
// (it ran out of time, or printed none): the assistant's readiness then says unknown, not claude_missing. The hub
// matches the literal (assistant/readiness.go); "" is every other outcome, as with nodes from before it.
const ClaudeVersionUnanswered = "unanswered"

// claudeProbe is ClaudeProbe, or ClaudeVersion read as it always was: no version is no claude.
func (o DoctorOptions) claudeProbe() func(context.Context) (string, bool) {
	if o.ClaudeProbe != nil {
		return o.ClaudeProbe
	}
	if o.ClaudeVersion == nil {
		return nil
	}
	return func(ctx context.Context) (string, bool) {
		v := o.ClaudeVersion(ctx)
		return v, v == ""
	}
}

func checkClaudeVersion(ctx context.Context, ask func(context.Context) (string, bool)) DoctorCheck {
	return checkClaudeVersionIn(ctx, ask, i18n.Default)
}

func checkClaudeVersionIn(ctx context.Context, ask func(context.Context) (string, bool), lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: "claude-version"}
	v, missing := "", true
	if ask != nil {
		v, missing = ask(ctx)
	}
	switch {
	case v == "" && !missing:
		c.Status, c.State, c.Detail = DoctorWarn, ClaudeVersionUnanswered, i18n.T(lang, "node-doctor.claude_unanswered")
		c.Hint = i18n.T(lang, "node-doctor.claude_unanswered_hint")
	case v == "":
		c.Status, c.Detail = DoctorWarn, i18n.T(lang, "node-doctor.claude_missing")
		c.Hint = i18n.T(lang, "node-doctor.claude_missing_hint")
	case !versionAtLeast(v, pluginMinVersion):
		c.Status = DoctorWarn
		min := fmt.Sprintf("%d.%d.%d", pluginMinVersion[0], pluginMinVersion[1], pluginMinVersion[2])
		c.Detail = i18n.T(lang, "node-doctor.claude_too_old", i18n.Args{"version": v, "minimum": min})
		c.Hint = i18n.T(lang, "node-doctor.claude_too_old_hint")
	default:
		c.Status, c.Detail = DoctorOK, i18n.T(lang, "node-doctor.claude_ok", i18n.Args{"version": v})
	}
	return c
}

// checkHerdr dials herdr's API socket. The engine is chosen by the hub when it connects and is not kept on this
// machine, so a socket that is not there is a warning: it only matters when herdr is this machine's engine.
func checkHerdr(ctx context.Context, path string) DoctorCheck {
	return checkHerdrIn(ctx, path, i18n.Default)
}

func checkHerdrIn(ctx context.Context, path string, lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: "herdr"}
	if path == "" {
		c.Status, c.Detail = DoctorWarn, i18n.T(lang, "node-doctor.herdr_no_path")
		c.Hint = i18n.T(lang, "node-doctor.herdr_no_path_hint")
		return c
	}
	ctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	conn, err := herdr.Dial(ctx, path)
	if err != nil {
		c.Status, c.Detail = DoctorWarn, i18n.T(lang, "node-doctor.herdr_unavailable")
		c.Hint = i18n.T(lang, "node-doctor.herdr_unavailable_hint")
		return c
	}
	conn.Close()
	c.Status, c.Detail = DoctorOK, i18n.T(lang, "node-doctor.herdr_ok")
	return c
}

// doctorOrca is what this machine has of Orca, for the doctor: node.info's row (orcaEngine).
func doctorOrca(ctx context.Context) proto.Engine { return orcaEngine(ctx, orcaClient()) }

// checkOrcaIn is the doctor's orca line (#17037): Orca running with a protocol Persona speaks, installed and not
// running, or running an incompatible version. A machine without Orca gets no line (ok false): the engine is chosen
// on the hub, and most machines never use Orca. A stopped or incompatible Orca is a warning, like a herdr that does
// not answer: it matters only when Orca is in this machine's engine set.
func checkOrcaIn(ctx context.Context, orca func(context.Context) proto.Engine, lang i18n.Lang) (DoctorCheck, bool) {
	ctx, cancel := context.WithTimeout(ctx, doctorDialTimeout)
	defer cancel()
	e := orca(ctx)
	c := DoctorCheck{Name: "orca"}
	args := i18n.Args{"version": e.Version}
	switch e.State.Known() {
	case proto.EngineRunning:
		c.Status, c.Detail = DoctorOK, i18n.T(lang, "node-doctor.orca_ok", args)
	case proto.EngineStopped:
		c.Status, c.Detail = DoctorWarn, i18n.T(lang, "node-doctor.orca_stopped")
		c.Hint = i18n.T(lang, "node-doctor.orca_stopped_hint")
	case proto.EngineIncompatible:
		c.Status, c.Detail = DoctorWarn, i18n.T(lang, "node-doctor.orca_incompatible", args)
		c.Hint = i18n.T(lang, "node-doctor.orca_incompatible_hint")
	default:
		return c, false
	}
	return c, true
}

// checkGlobalPermissionHooks looks at the user's own settings.json for synchronous PermissionRequest hooks (other
// tools put them there) and says how many; it never changes the file. One whose program is in a package of an
// extension installed on this computer (dataDir's node-plugins, extensionHookOwner) is that extension's, named and
// not warned about (#11973).
func checkGlobalPermissionHooks(configDir, dataDir string) DoctorCheck {
	return checkGlobalPermissionHooksIn(configDir, dataDir, i18n.Default)
}

func checkGlobalPermissionHooksIn(configDir, dataDir string, lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: "global-permission-hooks", Items: []string{}}
	settingsPath := filepath.Join(configDir, "settings.json")
	b, err := os.ReadFile(settingsPath)
	if errors.Is(err, os.ErrNotExist) {
		c.Status, c.Detail = DoctorOK, i18n.T(lang, "node-doctor.global_hooks_no_file")
		return c
	}
	if err != nil {
		c.Status = DoctorWarn
		c.Detail = i18n.T(lang, "node-doctor.global_hooks_unreadable", i18n.Args{"error": shorten(err.Error(), 60)})
		return c
	}
	var f struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string   `json:"type"`
				Command string   `json:"command"`
				Args    []string `json:"args"`
				Async   bool     `json:"async"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(b, &f) != nil {
		c.Status, c.Detail = DoctorWarn, i18n.T(lang, "node-doctor.global_hooks_invalid")
		c.Hint = i18n.T(lang, "node-doctor.global_hooks_invalid_hint")
		return c
	}
	var foreign, extensions []string
	fromExtensions := 0
	for _, m := range f.Hooks["PermissionRequest"] {
		for _, h := range m.Hooks {
			if h.Async || h.Type != "command" && h.Type != "" {
				continue
			}
			c.Items = append(c.Items, shorten(h.Command, 80))
			if name := extensionHookOwner(dataDir, hookProgram(h.Command, h.Args)); name != "" {
				fromExtensions++
				if !slices.Contains(extensions, name) {
					extensions = append(extensions, name)
				}
				continue
			}
			foreign = append(foreign, shorten(h.Command, 80))
		}
	}
	names := strings.Join(extensions, i18n.T(lang, "node-doctor.list_separator"))
	switch {
	case len(c.Items) == 0:
		c.Status, c.Detail = DoctorOK, i18n.T(lang, "node-doctor.global_hooks_none")
	case len(foreign) == 0: // all the user's own choice: installed extensions'
		c.Status = DoctorOK
		c.Detail = i18n.T(lang, "node-doctor.global_hooks_extensions", i18n.Args{"count": fromExtensions, "extensions": names})
	default:
		c.Status = DoctorWarn
		c.Detail = i18n.T(lang, "node-doctor.global_hooks_found", i18n.Args{"count": len(foreign), "hook": foreign[0]})
		if fromExtensions > 0 {
			c.Detail = i18n.T(lang, "node-doctor.global_hooks_found_with_extensions", i18n.Args{
				"count": len(foreign), "hook": foreign[0], "extension_count": fromExtensions, "extensions": names,
			})
		}
		c.Hint = i18n.T(lang, "node-doctor.global_hooks_found_hint", i18n.Args{"path": settingsPath})
	}
	return c
}
