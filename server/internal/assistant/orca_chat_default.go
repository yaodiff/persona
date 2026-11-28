package assistant

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- "Start new Agents as Orca chats" (#17650) ----
//
// A computer's switch, off by default, kept by the devices (devices/orca_chat_starts.go) and shown in its Engine
// section only while Orca is in its engine set. With it on, a start a person makes in New Agent that keeps New Agent's
// default (no launcher, preset, model or host other than Orca picked) and whose default launcher is plain Claude Code
// or Codex opens as the computer's Orca chat of that provider (orca-claude / orca-codex, node/orcabridge_start.go)
// instead of a terminal. Everything else stays where it went (docs/integrations/engines.md, "Orca chats as New Agent's
// default"):
//
//   - the assistant's own starts (agent_start, dispatch_start, task_plan, reviewers, follow-ups) never come through
//     agents.start_params, and an Orca chat carries none of Persona's launcher, hooks or MCP config a supervised task
//     needs; resumes keep the session where it is;
//   - a plan-mode (read-only) start: the bridge promises no read-only start;
//   - a launcher that is not the plain claude / codex CLI (a wrapper, a provider kept in Persona): Orca's chat runs the
//     plain CLI, so the start would answer on another model or account;
//   - options only a terminal takes (a faster speed, ultracode, a goal, model aliases), a permission mode the chat does
//     not take, and an engine runtime (Paseo, Cindy) as the start's host;
//   - a computer whose Orca does not offer that chat launcher now (Orca not running, too old to start chats, the
//     provider not installed, the launcher hidden or out of the computer's scope).
//
// The chat runs the start's model when Orca's chats of that provider list it (and its effort when that model lists
// it); otherwise Orca's own, and the launcher note says so. New Agent's form hears where its default start goes from
// the same rule (default_opens on GET /machines/{mid}/launchers).

// OrcaChats is what the manager reads of the computers' switches: the devices'.
type OrcaChats interface {
	// OrcaChatStarts: the switch is on for machine id and Orca is in its engine set.
	OrcaChatStarts(ctx context.Context, id string) bool
}

// orcaChatLauncherPrefix starts each Orca chat launcher's name (node/orcabridge_start.go); the rest is the provider.
const orcaChatLauncherPrefix = "orca-"

// orcaChatModelsTimeout bounds the read of an Orca chat launcher's models (the node answers from the bridge's last
// advertisement, without asking Orca).
const orcaChatModelsTimeout = 3 * time.Second

// orcaChatRoute is where a start goes instead of its launcher: the Orca chat launcher, the model and effort that go
// with it ("": Orca's own), and what of the start does not (dropped: the model or effort Orca's chats don't offer).
type orcaChatRoute struct {
	launcher      proto.Launcher
	model, effort string
	dropped       string
}

// orcaChatFor is the Orca chat a person's new start on m with params (what the start resolved: the default's launcher,
// model, effort and permission mode, and what the person picked, own) opens as; false when it stays where it goes.
// Read by AgentStartParams for the start and by the launchers route for the form, so both say the same.
func (s *Service) orcaChatFor(ctx context.Context, m Device, own startPick, params map[string]any) (orcaChatRoute, bool) {
	if s.orcaChats == nil || own.explicitLauncher() || own.model != "" {
		return orcaChatRoute{}, false
	}
	str := func(k string) string { v, _ := params[k].(string); return strings.TrimSpace(v) }
	if host := str("host"); host != "" && host != proto.EngineOrca || agentStartHost(m, str("host")) != "" {
		return orcaChatRoute{}, false // another host, or an engine runtime that runs only its own launchers
	}
	if str("resume_session_id") != "" || str("permission_mode") == proto.ModePlan || params["work_session"] == true {
		return orcaChatRoute{}, false
	}
	if proto.FasterSpeed(str("speed")) || str("service_tier") != "" || str("goal") != "" || params["model_aliases"] != nil {
		return orcaChatRoute{}, false
	}
	if u, ok := params["ultracode"].(bool); ok && u {
		return orcaChatRoute{}, false
	}
	provider := str("launcher")
	if provider != "claude" && provider != "codex" || !s.orcaChats.OrcaChatStarts(ctx, m.ID) {
		return orcaChatRoute{}, false
	}
	if slices.ContainsFunc(s.storedLaunchers(ctx), func(l storedLauncher) bool { return l.Name == provider }) {
		return orcaChatRoute{}, false // a provider kept in Persona under that name: not the plain CLI
	}
	list, known := s.launchersHere(ctx, m.ID) // the hidden ones left out: a hidden Orca chat launcher is not routed to
	if !known {
		return orcaChatRoute{}, false // nothing is routed on a guess
	}
	i := slices.IndexFunc(list, func(l proto.Launcher) bool {
		return l.Name == orcaChatLauncherPrefix+provider && l.Source == proto.EngineOrca
	})
	if i < 0 || s.launcherScopeErr(ctx, m.ID, list[i].Name) != nil {
		return orcaChatRoute{}, false
	}
	chat := list[i]
	if mode := str("permission_mode"); !permissionModeSupported(cmp.Or(chat.Agent, "claude"), chat.PermissionModes, mode) {
		return orcaChatRoute{}, false
	}
	route := orcaChatRoute{launcher: chat}
	model, effort := str("model"), str("effort")
	if model == "" && effort == "" {
		return route, true
	}
	cctx, cancel := context.WithTimeout(ctx, orcaChatModelsTimeout)
	defer cancel()
	var row *proto.AgentModel
	if res, err := s.launcherModelRows(cctx, m.ID, chat.Name, false); err == nil {
		for j := range res.Models {
			if model != "" && res.Models[j].ID == model || model == "" && res.Models[j].Default {
				row = &res.Models[j]
				break
			}
		}
	}
	switch {
	case row == nil && model != "":
		route.dropped = model
	case row == nil:
		route.dropped = effort
	default:
		route.model = model
		if effort != "" && slices.Contains(row.Efforts, effort) {
			route.effort = effort
		} else if effort != "" {
			route.dropped = effort
		}
	}
	return route, true
}

// routeOrcaChat sends a person's New Agent start to its Orca chat (orcaChatFor) by rewriting params: the chat
// launcher, its provider as the agent, the model and effort that go with it; the terminal launcher's fingerprint goes.
// agent is what the start runs from then on; note says what of the start did not go with it ("" none).
func (s *Service) routeOrcaChat(ctx context.Context, m Device, own startPick, params map[string]any, agent startAgent) (startAgent, string) {
	route, ok := s.orcaChatFor(ctx, m, own, params)
	if !ok {
		return agent, ""
	}
	provider := strings.TrimPrefix(route.launcher.Name, orcaChatLauncherPrefix)
	for _, k := range []string{"model", "effort", "model_fingerprint", "explicit_model", "speed"} {
		delete(params, k)
	}
	params["launcher"] = route.launcher.Name
	params["agent_type"] = provider
	if route.model != "" {
		params["model"] = route.model
	}
	if route.effort != "" {
		params["effort"] = route.effort
	}
	s.logf("start on %s: New Agent's default %s opens as an Orca chat (%s)", m.ID, provider, route.launcher.Name)
	note := ""
	if route.dropped != "" {
		note = i18n.T(s.lang(ctx), "assistant-tasks.orca_chat_option_dropped", i18n.Args{"to": orcaChatName(route.launcher),
			"option": route.dropped})
	}
	return startAgent{agent: provider, source: route.launcher.Source, permissionModes: route.launcher.PermissionModes}, note
}

// orcaChatName is what the apps call an Orca chat launcher ("Orca chat · Claude Code").
func orcaChatName(l proto.Launcher) string { return cmp.Or(l.DisplayName, l.Name) }

// orcaChatDefault is default_opens on GET /machines/{mid}/launchers (New Agent's form): the Orca chat a start that
// keeps the default (def, the launcher, model, effort and permission mode it runs) opens as on m, asked for host: its
// launcher and name, the permission modes with which it does (another mode the person picks keeps it in a terminal),
// the model and effort that go with it, and dropped when the default's model or effort does not (Orca's own runs);
// nil when it stays where it goes.
func (s *Service) orcaChatDefault(ctx context.Context, m Device, def map[string]any, host string) map[string]any {
	if def == nil || s.orcaChats == nil {
		return nil
	}
	params := map[string]any{"host": host}
	for k, to := range map[string]string{"provider": "launcher", "model": "model", "effort": "effort", "permission_mode": "permission_mode"} {
		if v, _ := def[k].(string); v != "" {
			params[to] = v
		}
	}
	route, ok := s.orcaChatFor(ctx, m, startPick{}, params)
	if !ok {
		return nil
	}
	var modes []string
	for _, mode := range proto.PermissionModes {
		if mode != proto.ModePlan && permissionModeSupported(cmp.Or(route.launcher.Agent, "claude"), route.launcher.PermissionModes, mode) {
			modes = append(modes, mode)
		}
	}
	out := map[string]any{"launcher": route.launcher.Name, "display_name": orcaChatName(route.launcher), "permission_modes": modes}
	if route.model != "" {
		out["model"] = route.model
	}
	if route.effort != "" {
		out["effort"] = route.effort
	}
	if route.dropped != "" {
		out["dropped"] = true // the default's model or effort does not go with it: Orca's own runs
	}
	return out
}
