package extmgr

// Apps the assistant never operates (#4336, docs/extensions/computer-use.md "c) Per-App approval"): System Settings
// and the system's permission and authorization prompts. macOS's privacy permissions are meant to be answered by a
// person, and one Allow on a card that says "operate System Settings" would otherwise let an Agent turn any of them on.
// On Cua Driver each computer_run goes through protectedapps.Look.Check, the same rules as the Agents' own Cua Driver
// (node/cua_gate.go), before any card and again after one: a call that names one of these apps or would reach one is
// refused; while a prompt is on the screen everything is; while System Settings is open only what could reach it is
// (its element tokens, a point where it is on top, keys while it is in front, and scripts, which cannot be checked
// beforehand), #7560. Steps naming no app run one at a time, each checked the same way just before it runs, so a
// prompt an earlier step brought up stops the rest. Reading the screen (computer_state) stays as it was: asked every
// time. LCU asks before it touches an app, and those questions are refused for these (computer_lcu.go).

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nwverse/persona/server/internal/protectedapps"
	"github.com/nwverse/persona/server/internal/proto"
)

// protectedApp: a is one of the apps never operated (protectedapps.Is), by bundle id or by name.
func protectedApp(a approvedApp) bool { return protectedapps.Is(a.BundleID, a.Name) }

// refusedText is what the model is told; it passes the gist on to the user.
func refusedText(machine, why string) string { return protectedapps.Refusal(machine, why) }

// knownApps is Cua Driver's list of m's apps, nil when it cannot be read.
func (h *Manager) knownApps(ctx context.Context, m *Machine) []protectedapps.App {
	if res, err := h.computerCall(ctx, m, "persona-apps", "", "list_apps", map[string]any{}); err == nil {
		return protectedapps.Apps(res.Output)
	}
	return nil
}

// protectedLook is a look at m for protectedapps' check, through its Cua Driver, with the app list read for this call
// (nil: read when needed).
func (h *Manager) protectedLook(ctx context.Context, m *Machine, known []protectedapps.App) *protectedapps.Look {
	return protectedapps.NewLook(func(tool string, args map[string]any) (json.RawMessage, bool) {
		res, err := h.computerCall(ctx, m, "persona-apps", "", tool, args)
		if err != nil || res == nil || len(res.Output) == 0 {
			return nil, false
		}
		return res.Output, true
	}, known)
}

// unnamedSteps: some step names no app of its own and the call names none (protectedapps.UnnamedSteps).
func unnamedSteps(app string, steps json.RawMessage) bool {
	return protectedapps.UnnamedSteps(app, steps)
}

// runStepsOneByOne runs steps one run_steps call at a time, checking each after the first (which the caller checked)
// just before it runs: check's refusal stops the rest, and stopped says so. The answer lists each step's own answer;
// the last step carries the call's observe, and its screenshot is the answer's.
func (h *Manager) runStepsOneByOne(ctx context.Context, m *Machine, session, conversation string, steps, observe json.RawMessage,
	check func(step json.RawMessage) protectedapps.Verdict) (*proto.ComputerResult, string, error) {
	var list []json.RawMessage
	if err := json.Unmarshal(steps, &list); err != nil {
		return nil, "", err
	}
	outs := make([]json.RawMessage, 0, len(list))
	var last *proto.ComputerResult
	for i, step := range list {
		if i > 0 {
			if v := check(step); v.Why != "" {
				h.logf("[%s] computer_run refused before step %d of %d (%s): %s", m.cfg.ID, i+1, len(list), v.Rule, v.App)
				return nil, fmt.Sprintf("Stopped before step %d of %d (the %d before it ran). %s", i+1, len(list), i, v.Why), nil
			}
		}
		in := map[string]any{"steps": []json.RawMessage{step}}
		if i == len(list)-1 && len(observe) > 0 {
			in["observe"] = observe
		}
		res, err := h.computerCall(ctx, m, session, conversation, "run_steps", in)
		if err != nil {
			return nil, "", err
		}
		last = res
		out := res.Output
		if len(out) == 0 {
			out, _ = json.Marshal(res.Text)
		}
		outs = append(outs, out)
		var r struct {
			OK *bool `json:"ok"`
		}
		if json.Unmarshal(res.Output, &r) == nil && r.OK != nil && !*r.OK {
			break // stopped at its first failure, as run_steps does
		}
	}
	all, _ := json.Marshal(map[string]any{"one_by_one": true, "steps": outs})
	return &proto.ComputerResult{Output: all, Image: last.Image}, "", nil
}
