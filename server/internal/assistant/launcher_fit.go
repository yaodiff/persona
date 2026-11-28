package assistant

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- a new Agent's launcher on the computer it starts on (#2640) ----
//
// Model configuration › Agents (agent.new) is the one launcher purpose with no computer, and a model profile's tiers
// have none either: their launcher may be a ~/.zshrc function or a wrapper script only some computers have. Every new
// Agent (a task, agent_start, New Agent in the apps, a message resuming an Agent started outside Persona) is held to
// what its computer can run before the start goes out, rather than the terminal answering "command not found"
// (docs/server/launcher-health.md, "A launcher the computer doesn't have").

// startPick is what a start's caller named itself, before a tier and Model configuration › Agents fill in the rest.
// ultracode is the tri-state the caller gave (nil: it said nothing, false: it explicitly asked for it off). permission
// is a task's permission mode as it stood before the launcher that runs set it (launchPick only; pickOf leaves it "").
type startPick struct {
	launcher, effort, model, speed, tier, permission string
	ultracode                                        *bool
	// The caller or preset selected omp with no model before Model Settings were applied.
	ompModelDefault bool
}

// explicitLauncher says whether the start's launcher is a choice the caller made that fitLauncher must refuse rather
// than quietly replace: the caller named the launcher itself, or an explicit tier resolved to it. A tier a
// start-defaults policy selected for this start counts too (#12032). A launcher only the agent.new default gave has
// neither field and still gives way to one the computer has.
func (p startPick) explicitLauncher() bool { return p.launcher != "" || p.tier != "" }

// launchPick is what task t holds of its launch now, as pickOf reads it from a start's params, with its ultracode.
func (t *PlanTask) launchPick() startPick {
	return startPick{launcher: strings.TrimSpace(t.Launcher), effort: strings.TrimSpace(t.Effort),
		model: strings.TrimSpace(t.Model), speed: strings.TrimSpace(t.Speed), tier: strings.TrimSpace(t.Tier), permission: strings.TrimSpace(t.Permission),
		ultracode: copyBool(t.Ultracode)}
}

// startFill is what a start-defaults policy filled in on one start try: a preset (which then decided the launcher,
// model, effort, tier, and with the agent it runs the permission mode its launcher sets), the policy's effort, its
// ultracode. A rule that fits but fills nothing leaves it empty, and the start is the one without the feature.
type startFill struct{ preset, effort, ultracode bool }

func (f startFill) any() bool { return f.preset || f.effort || f.ultracode }

// startNamed is what a start try put in place of the launch resolved for its task, which the task names from then on
// (storedLaunch): launcher, the launcher (its tier goes with it) the computer has for a default it lacks (#2661) or an
// engine runs in place of the one asked (#8140); noModel, that launcher runs its own default model, not the one asked;
// effort, the effort an engine ran in place of the one asked (#8140).
type startNamed struct{ launcher, noModel, effort bool }

// storedLaunch is the launch task t keeps in its plan: the task's own launcher, model, effort, speed and tier
// (callerPick), not what this try resolved for it from a tier, Model configuration › Agents or a start-defaults
// policy. Those are for that one start (the Agent's session shows what it runs with, a failed start's error args its
// launcher): kept with the task, a retry (task_retry, the card's Retry), a remedy or a queued start, which read the
// task back from the store, would take them for the task's own choice and stay on them after the default changed
// (#12618). The candidate a tier gave the start is recorded apart, as the hub's Dispatch: the next Agent of the task
// keeps it while the tier still lists it (candidate_keep.go, #14701), without it ever becoming the task's own launch,
// so a changed tier or profile still decides once the candidate is gone. What this try put in place of the resolved launch (startNamed) is kept, as the card
// shows it, unless a policy's preset (or, for the effort, the policy's effort) decided it. The permission mode is the
// one this try settled, except one a policy's preset brought. CallerPermission separately drives the next start.
func (t PlanTask) storedLaunch() startPick {
	kept := t.launchPick()
	if t.callerPick == nil {
		return kept
	}
	own := *t.callerPick
	ran := kept
	kept.launcher, kept.model, kept.effort, kept.speed, kept.tier = own.launcher, own.model, own.effort, own.speed, own.tier
	if t.startFilled.preset {
		kept.permission = own.permission
		if t.CallerPermission != nil && *t.CallerPermission == "" {
			kept.permission = ran.permission
		}
	}
	if named := t.startNamed; !t.startFilled.preset {
		if named.launcher {
			kept.launcher, kept.tier = ran.launcher, ""
		}
		if named.noModel {
			kept.model = ""
		}
		if named.effort && !t.startFilled.effort {
			kept.effort = ran.effort
		}
	}
	if t.startFilled.ultracode {
		kept.ultracode = copyBool(own.ultracode)
	}
	return kept
}

// copyBool is a copy of an optional switch, so the copy and the original never change together.
func copyBool(b *bool) *bool {
	if b == nil {
		return nil
	}
	v := *b
	return &v
}

// pickOf is the caller's own pick in a start's params.
func pickOf(params map[string]any) startPick {
	l, _ := params["launcher"].(string)
	e, _ := params["effort"].(string)
	m, _ := params["model"].(string)
	t, _ := params["tier"].(string)
	v, _ := params["speed"].(string)
	pm, _ := params["permission_mode"].(string)
	u, _ := params["ultracode"].(*bool)
	return startPick{launcher: strings.TrimSpace(l), effort: strings.TrimSpace(e),
		model: strings.TrimSpace(m), speed: strings.TrimSpace(v), tier: strings.TrimSpace(t), permission: strings.TrimSpace(pm), ultracode: u}
}

// resolveStart fills params (a session.start's, with the caller's own launcher, model, effort and tier, if any) with
// what the new Agent runs on: the tier, then Model configuration › Agents. It answers the caller's own pick, for
// fitLauncher.
func (s *Service) resolveStart(ctx context.Context, params map[string]any, launcherDefaultModel bool) (startPick, error) {
	own := pickOf(params)
	if err := s.applyStartTier(ctx, params); err != nil {
		return own, err
	}
	selected := pickOf(params)
	own.ompModelDefault = selected.launcher == "omp" && selected.model == ""
	return own, s.applyModelBinding(ctx, "agent.new", params, launcherDefaultModel)
}

// startAgent is what a start's launcher runs on the computer it starts on (fitLauncher): the agent type (claude, codex,
// a runtime's own such as paseo; "" when the computer's list does not say), and whether that agent asks for approval
// unless its runtime's switch is on, which a start in bypassPermissions turns on (proto.Launcher.AutoAccept: Paseo's
// Auto Accept).
type startAgent struct {
	agent           string
	snapshot        string // the explicitly known kind for a to-do link; agent's compatibility defaults are not evidence
	source          string // the selected launcher's provenance, retained through fitting and health detours
	autoAccept      bool
	permissionModes proto.PermissionModeSet
}

// launcherStartAgent is what launcher l runs.
func launcherStartAgent(l ConfiguredLauncher) startAgent {
	return startAgent{agent: l.AgentType, source: l.Source, autoAccept: l.AutoAccept, permissionModes: l.PermissionModes}
}

func (a startAgent) supportsPermissionMode(mode string) bool {
	return permissionModeSupported(a.agent, a.permissionModes, mode)
}

// fitNewLauncher applies a new Agent's defaults using the launcher actually available on its destination.
// Resumes call fitLauncher directly and keep their existing execution settings.
func (s *Service) fitNewLauncher(ctx context.Context, m Device, own startPick, params map[string]any) (string, startAgent, error) {
	note, agent, err := s.fitLauncher(ctx, m, own, params)
	if err == nil {
		err = s.applyOmpStartDefaults(ctx, m, own, agent, params)
	}
	return note, agent, err
}

func ompStartModelTaskHost(m Device, params map[string]any) bool {
	host, _ := params["host"].(string)
	if host == "" {
		host = proto.EngineHostsOf(m.Engine).Default
	}
	return host == "" || host == proto.EngineHerdr || host == proto.EngineOrca
}

func (s *Service) applyOmpStartDefaults(ctx context.Context, m Device, own startPick, agent startAgent, params map[string]any) error {
	declared, _ := params["agent_type"].(string)
	if params["launcher"] != "omp" || agent.agent != "omp" || (declared != "" && declared != "omp") || !ompStartModelTaskHost(m, params) {
		return nil
	}
	if source := compat.Source(agent.source); source != "" && source != "builtin" && !machineSource(source) {
		return nil
	}
	model, _ := params["model"].(string)
	effort, _ := params["effort"].(string)
	mode, _ := params["permission_mode"].(string)
	// An explicit launcher default uses omp's local model. Following Model Settings keeps its model.
	// Only an effort or permission mode the caller named reaches the node, which refuses it before opening the tab.
	dropModel, dropEffort, dropMode := own.ompModelDefault && model != "", own.effort == "" && effort != "", own.permission == "" && mode != ""
	if !dropModel && !dropEffort && !dropMode {
		return nil
	}
	if dropModel {
		model = ""
		delete(params, "model")
	}
	if dropEffort {
		effort = ""
		delete(params, "effort")
	}
	if dropMode {
		mode = ""
		delete(params, "permission_mode")
	}
	return s.finishModel(ctx, "agent.new", params, ModelBinding{Provider: launcherProvider("omp"), Model: model, Effort: effort, PermissionMode: mode})
}

// launcherKindSnapshot is the kind the launcher metadata actually names, before sessionLaunchers applies its Claude
// compatibility default to an arbitrary wrapper. Explicit hub configuration wins over the node's answer, as it does
// for the start itself. Recognized built-in names are evidence; a guessed wrapper type is not.
func launcherKindSnapshot(discovered, overrides []ConfiguredLauncher, name string) string {
	kind := ""
	for _, l := range discovered {
		if l.Name == name {
			kind = cmp.Or(l.AgentType, l.Agent)
			break
		}
	}
	for _, l := range overrides {
		if l.Name == name && l.AgentType != "" && l.Source != proto.ExternalLauncherSource && !proto.IsExternalLauncher(l.Name) {
			kind = l.AgentType
		}
	}
	if kind == "" {
		switch name {
		case "claude", "codex", "traex", "agy":
			kind = name
		}
	}
	return kind
}

// configuredStartAgentLookup applies hub overrides and compatibility defaults once for an inventory, then resolves
// names without another machine read. Both the real fit and candidate previews use this same metadata.
func configuredStartAgentLookup(discovered, overrides []ConfiguredLauncher) func(string) startAgent {
	configured := sessionLaunchers(discovered, overrides)
	return func(name string) startAgent {
		for _, l := range configured {
			if l.Name == name {
				a := launcherStartAgent(l)
				a.snapshot = launcherKindSnapshot(discovered, overrides, name)
				return a
			}
		}
		return startAgent{}
	}
}

// allowAll: the Agents Persona itself starts on the launcher (agent_start, a planned task, a read-only one too) run with
// Allow all, bypassPermissions: Claude Code's, and, by the owner's decision (#8141), those whose approval is a switch
// Allow all turns on (Paseo's providers with Auto Accept: Grok and its other ACP providers, OpenCode), which otherwise
// ask for every tool and leave a card the assistant never answers, and the Antigravity CLI (agy), whose worker would
// otherwise stop at its own approval prompt in the pane (--dangerously-skip-permissions, node/workagent.go). Codex keeps
// its sandbox, on Paseo as in a terminal: its tasks take the settled mode (tasks_permission.go). A start a person makes
// (New Agent) keeps the mode they chose.
func (a startAgent) allowAll() bool { return a.agent == "claude" || a.agent == "agy" || a.autoAccept }

// allowAllOver: a task's mode gives way to Allow all, always on Claude Code and an Auto Accept provider (their planned
// tasks run in it), only when it has none on agy, whose start runs in the mode it names (node/workagent.go).
func (a startAgent) allowAllOver(mode string) bool {
	return a.allowAll() && (a.agent != "agy" || mode == "")
}

// fitLauncher holds params' launcher (resolveStart's) to what computer m can start. One the caller named, or an
// explicit tier resolved to, that m does not have is refused, naming the ones it has: both are caller choices the
// start must not quietly replace (#12032). One a default gave (only the agent.new binding or a profile) gives way to
// one m has (launcherInstead), with that launcher's own default model unless the caller named a model, and with the
// caller's effort; note says so in the
// user's language, for the task's why and the tool's answer. A list that can't be read (m offline, a timeout) counts
// as having it, as mainChatFit does: nothing is refused or replaced on a guess. A default of Claude's desktop app gives
// way the same on every start but a person's New Agent, list or not (models_desktop.go). What the launcher runs comes
// from the same list, so a caller changing permissions by it does not have to ask for it again.
func (s *Service) fitLauncher(ctx context.Context, m Device, own startPick, params map[string]any) (note string, agent startAgent, err error) {
	name, _ := params["launcher"].(string)
	if proto.IsExternalLauncher(name) {
		return "", startAgent{}, i18n.Err("external-adapter.purpose_unsupported")
	}
	withoutList, scopeErr := s.launcherWithoutInventory(ctx, m.ID, name)
	if scopeErr != nil {
		placementOf(ctx).hit(m.ID, "launcher_missing", i18n.Args{"launcher": name})
		if own.explicitLauncher() {
			return "", startAgent{}, scopeErr
		}
	}
	asked, _ := params["host"].(string)
	if host := agentStartHost(m, asked); host != "" {
		// Where the start's engine runs only its own launchers, plain claude and a managed launcher do not run either.
		if note, agent, err, done := s.fitEngineLauncher(ctx, m, host, params); done {
			return note, agent, err
		}
	}
	if withoutList.agent != "" {
		return "", withoutList, nil
	}
	// The desktop app's launcher as a default opens only a person's New Agent start in the app: any other start gives
	// way as on a computer without it, whatever its list says (models_desktop.go).
	appDefault := !own.explicitLauncher() && !personStart(ctx) && s.desktopAppProvider(ctx, launcherProvider(name))
	list, known := s.launchersHere(withUnhidden(ctx, name), m.ID) // a hidden default still runs (#4659)
	if !known && !appDefault && scopeErr == nil {
		return "", s.lastSeenAgent(ctx, m.ID, name), nil
	}
	list = s.launchersWithinScope(ctx, m.ID, list)
	discovered := make([]ConfiguredLauncher, 0, len(list))
	for _, l := range list {
		discovered = append(discovered, ConfiguredLauncher{Launcher: l})
	}
	overrides := s.hello(ctx).Models.Launchers[m.ID]
	agentOf := configuredStartAgentLookup(discovered, overrides)
	here := slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == name })
	if here && !appDefault {
		return "", agentOf(name), nil
	}
	if known && !here {
		placementOf(ctx).hit(m.ID, "launcher_missing", i18n.Args{"launcher": name})
	}
	if own.explicitLauncher() {
		return "", startAgent{}, i18n.Err("assistant-tasks.launcher_not_on_machine", i18n.Args{
			"machine": firstNonEmpty(m.Name, m.ID), "launcher": name, "launchers": strings.Join(launcherNamesOn(m, list), ", "),
		})
	}
	to := launcherInstead(m, list)
	for _, k := range []string{"model", "effort", "model_fingerprint", "speed", "permission_mode"} {
		delete(params, k)
	}
	params["launcher"] = to
	if own.model != "" {
		params["model"] = own.model
	}
	if own.effort != "" {
		params["effort"] = own.effort
	}
	if own.speed != "" {
		params["speed"] = own.speed
	}
	if own.permission != "" {
		params["permission_mode"] = own.permission
	}
	if err := s.applyModel(ctx, "agent.new", params); err != nil {
		return "", startAgent{}, err
	}
	args := i18n.Args{"launcher": name, "machine": firstNonEmpty(m.Name, m.ID), "to": to}
	if own.model != "" {
		args["model"] = own.model
	}
	key := "assistant-tasks.launcher_swapped"
	switch {
	case appDefault && own.model != "":
		key = "assistant-tasks.launcher_desktop_default_model"
	case appDefault:
		key = "assistant-tasks.launcher_desktop_default"
	case own.model != "":
		key = "assistant-tasks.launcher_swapped_model"
	}
	if appDefault {
		s.logf("start on %s: launcher %s opens only a person's New Agent start, using %s", m.ID, name, to)
	} else {
		s.logf("start on %s: launcher %s is not there, using %s", m.ID, name, to)
	}
	note = i18n.T(s.lang(ctx), key, args)
	current, _ := params["launcher"].(string)
	return note, agentOf(current), nil
}

// lastSeenAgent is what launcher name runs on machine by the last launcher list the hub saw there (the persisted
// snapshot, zshrcLaunchersLastSeen), with hub.json's launchers applied, for a start whose live list could not be read
// (a slow or busy computer). Without it a Claude wrapper such as a relay script counted as an unknown agent: its task
// fell back to the computer's Claude default mode, and when that read timed out too, to "default", so the Agent
// stopped on its first Bash approval. Nothing when the snapshot does not name it: nothing is guessed.
func (s *Service) lastSeenAgent(ctx context.Context, machine, name string) startAgent {
	if name == "" {
		return startAgent{}
	}
	seen, _ := s.zshrcLaunchersLastSeen(ctx)
	overrides := s.hello(ctx).Models.Launchers[machine]
	for _, z := range seen {
		if z.MachineID != machine {
			continue
		}
		var configured []ConfiguredLauncher
		for _, l := range z.Launchers {
			if l.Name == name {
				configured = append(configured, ConfiguredLauncher{Launcher: l})
			}
		}
		if len(configured) == 0 {
			break
		}
		for _, l := range sessionLaunchers(configured, overrides) {
			if l.Name == name {
				a := launcherStartAgent(l)
				a.snapshot = launcherKindSnapshot(configured, overrides, name)
				return a
			}
		}
	}
	if name == "agy" {
		return startAgent{agent: "agy", snapshot: "agy"}
	}
	return startAgent{}
}

// launchersHere is what machine can start: machineLaunchers (its own launchers but the hidden ones, and the managed
// ones) and those hub.json names for it (a wrapper the node may not list, configuredLauncher). known false when its
// list can't be read, or the answer names none of its own: a node that read its shell lists plain claude at least
// (node mergeLaunchers), so such an answer is no list. The hub-added built-in is not evidence of a readable list.
func (s *Service) launchersHere(ctx context.Context, machine string) (list []proto.Launcher, known bool) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, err := s.machineLaunchers(cctx, machine)
	if err != nil || !slices.ContainsFunc(list, func(l proto.Launcher) bool {
		source := compat.Source(l.Source)
		return source != "persona" && source != "builtin"
	}) {
		return nil, false
	}
	for _, c := range s.hello(ctx).Models.Launchers[machine] {
		if c.Name != "" && !proto.IsExternalLauncher(c.Name) && c.Source != proto.ExternalLauncherSource &&
			!slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == c.Name }) {
			list = append(list, c.Launcher)
		}
	}
	return list, true
}

// launcherWithoutInventory recognizes built-ins and managed launchers whose scope permits the destination.
// Other launchers need the destination's inventory to establish what they run.
func (s *Service) launcherWithoutInventory(ctx context.Context, machine, name string) (startAgent, error) {
	if name == "" || name == "claude" {
		return startAgent{agent: "claude", snapshot: "claude"}, nil
	}
	if proto.IsExternalLauncher(name) {
		return startAgent{}, nil
	}
	l, ok := s.managedNamed(ctx, name)
	if !ok {
		return startAgent{}, nil
	}
	if err := s.managedLauncherScopeErr(ctx, machine, l); err != nil {
		return startAgent{}, err
	}
	agent := l.AgentType()
	return startAgent{agent: agent, snapshot: agent, source: "persona"}, nil
}

// launchersWithinScope removes stale or colliding inventory names that a start would refuse.
func (s *Service) launchersWithinScope(ctx context.Context, machine string, list []proto.Launcher) []proto.Launcher {
	ctx = withManagedSnapshot(ctx, s.storedLaunchers(ctx))
	return slices.DeleteFunc(list, func(l proto.Launcher) bool {
		return s.launcherScopeErr(ctx, machine, l.Name) != nil
	})
}

// launcherInstead is the launcher a default m does not have gives way to: plain claude when m has Claude Code (its
// node said a version, or its list names claude), else the first in its list that starts an agent with a prompt (not a
// runtime's), else claude all the same.
func launcherInstead(m Device, list []proto.Launcher) string {
	if m.ClaudeVersion != "" || slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == "claude" }) {
		return "claude"
	}
	if i := slices.IndexFunc(list, startableLauncher); i >= 0 {
		return list[i].Name
	}
	return "claude"
}

// startableLauncher: l starts an agent with a prompt: one of the machine's own, or a managed one (healthLaunchers).
func startableLauncher(l proto.Launcher) bool {
	return l.Name != "" && !proto.IsExternalLauncher(l.Name) && l.Source != proto.ExternalLauncherSource &&
		(l.Source == "" || machineSource(l.Source) || compat.Source(l.Source) == "persona")
}

// launcherNamesOn are the names a refusal lists: plain claude first when m has Claude Code, then its list's.
func launcherNamesOn(m Device, list []proto.Launcher) []string {
	var names []string
	if m.ClaudeVersion != "" {
		names = append(names, "claude")
	}
	for _, l := range list {
		if l.Name != "" && !slices.Contains(names, l.Name) {
			names = append(names, l.Name)
		}
	}
	if len(names) == 0 {
		names = append(names, "claude")
	}
	return names
}
