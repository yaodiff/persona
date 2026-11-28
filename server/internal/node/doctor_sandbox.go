package node

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/i18n"
)

// checkAgentSandbox is the doctor's line for the agent sandbox: whether it is on and where from, whether this machine
// can run it, and each kind of start's last status.
func checkAgentSandbox(dir string) DoctorCheck {
	return checkAgentSandboxIn(dir, i18n.Default)
}

func checkAgentSandboxIn(dir string, lang i18n.Lang) DoctorCheck {
	c := DoctorCheck{Name: "agent-sandbox", Status: DoctorOK, Items: []string{}}
	on, from := agentSandboxOn(dir)
	support := agentSandboxProbe() // macOS runs sandbox-exec true; Linux runs a real default-allow bwrap probe
	sup := sandbox.AgentSupport(support)
	supWhy := sup.WhyIn(lang)
	last := readAgentSandboxStatus(dir)
	wheres := make([]string, 0, len(last))
	for w := range last {
		wheres = append(wheres, w)
	}
	slices.Sort(wheres)
	for _, w := range wheres {
		r := last[w]
		key := "node-doctor.sandbox_status_item"
		args := i18n.Args{"where": w, "status": agentSandboxWord(lang, r.State), "at": r.At.Local().Format("01-02 15:04")}
		if r.Why != "" {
			key = "node-doctor.sandbox_status_item_why"
			args["why"] = r.WhyIn(lang)
		}
		c.Items = append(c.Items, i18n.T(lang, key, args))
	}
	switch {
	case !on:
		if from == "" {
			c.Detail = i18n.T(lang, "node-doctor.sandbox_off_default")
		} else {
			c.Detail = i18n.T(lang, "node-doctor.sandbox_off_from", i18n.Args{"from": from})
		}
		switch sup.State {
		case sandbox.AgentActive:
			c.Detail = i18n.T(lang, "node-doctor.sandbox_off_available", i18n.Args{"detail": c.Detail})
			c.Hint = i18n.T(lang, "node-doctor.sandbox_enable_hint", i18n.Args{"path": filepath.Join(dir, sandbox.AgentOnFile)})
		case sandbox.AgentUnsupported:
			c.Detail = i18n.T(lang, "node-doctor.sandbox_off_unsupported", i18n.Args{"detail": c.Detail, "why": supWhy})
		default:
			c.Detail = i18n.T(lang, "node-doctor.sandbox_off_unavailable", i18n.Args{"detail": c.Detail, "why": supWhy})
		}
	case sup.State == sandbox.AgentUnsupported:
		c.Status = DoctorWarn
		c.Detail = i18n.T(lang, "node-doctor.sandbox_on_unsupported", i18n.Args{"why": supWhy})
		c.Hint = i18n.T(lang, "node-doctor.sandbox_runs_outside_hint")
	case sup.State == sandbox.AgentUnavailable:
		c.Status = DoctorWarn
		c.Detail = i18n.T(lang, "node-doctor.sandbox_on_unavailable", i18n.Args{"why": supWhy})
		c.Hint = i18n.T(lang, "node-doctor.sandbox_runs_outside_hint")
	default:
		c.Detail = i18n.T(lang, "node-doctor.sandbox_on", i18n.Args{"from": from, "limits": agentSandboxLimitsIn(support.OS, lang)})
		c.Hint = i18n.T(lang, "node-doctor.sandbox_environment_hint", i18n.Args{"env": agentSandboxEnv})
		for _, w := range wheres {
			if s := last[w].State; s == sandbox.AgentSkipped || s == sandbox.AgentUnavailable {
				c.Status = DoctorWarn
				c.Detail = i18n.T(lang, "node-doctor.sandbox_on_skipped", i18n.Args{"detail": c.Detail})
				c.Hint = i18n.T(lang, "node-doctor.sandbox_skipped_hint")
				break
			}
		}
	}
	if len(c.Items) > 0 {
		c.Detail = i18n.T(lang, "node-doctor.sandbox_with_recent", i18n.Args{
			"detail": c.Detail, "items": strings.Join(c.Items, i18n.T(lang, "node-doctor.item_separator")),
		})
	}
	return c
}

// agentSandboxLimits is what the doctor reminds of while the sandbox is on (docs/server/agent-sandbox.md): what it does not
// stop, which agents it does not cover, and platform-specific behavior that needs a restart or stops working.
func agentSandboxLimits(goos string) string {
	return agentSandboxLimitsIn(goos, i18n.Default)
}

func agentSandboxLimitsIn(goos string, lang i18n.Lang) string {
	limits := i18n.T(lang, "node-doctor.sandbox_limits")
	switch goos {
	case "darwin":
		return i18n.T(lang, "node-doctor.sandbox_limits_darwin", i18n.Args{"limits": limits})
	case "linux":
		return i18n.T(lang, "node-doctor.sandbox_limits_linux", i18n.Args{"limits": limits})
	default:
		return limits
	}
}

// agentSandboxWord is one status in the doctor's words. Unknown future states keep the old empty word.
func agentSandboxWord(lang i18n.Lang, state string) string {
	key := map[string]string{
		sandbox.AgentOff:         "node-doctor.sandbox_state_off",
		sandbox.AgentActive:      "node-doctor.sandbox_state_active",
		sandbox.AgentSkipped:     "node-doctor.sandbox_state_skipped",
		sandbox.AgentUnsupported: "node-doctor.sandbox_state_unsupported",
		sandbox.AgentUnavailable: "node-doctor.sandbox_state_unavailable",
	}[state]
	if key == "" {
		return ""
	}
	return i18n.T(lang, key)
}
