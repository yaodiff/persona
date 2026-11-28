package assistant

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- a new Agent whose start goes to an engine that runs only its own launchers (#8140) ----
//
// Paseo runs every session with a provider of its own: on a computer whose start host is Paseo, a launcher of the login
// shell's, a provider kept in Persona and plain claude never run. The owner's rule (docs/integrations/paseo.md "A
// launcher Paseo does not have"): a preset or task that names a launcher Paseo does not have gets one of Paseo's
// providers of the same kind of agent, chosen here and said on the task (its why, agent_start's answer and card, New
// Agent's note); with none of that kind it is refused with the reason, never run as another kind of agent or on another
// account unsaid. The engine's node and bridge hold every start to the same rule, and say what they ran instead
// (proto.StartInstead), which the task then names too (startInsteadNote).

// agentStartHost is the engine host a new Agent's start on m opens in when that is a runtime (Paseo, Cindy): the one
// the start names, else m's default. "" for a terminal host, whose launchers are the machine's own as before.
func agentStartHost(m Device, asked string) string {
	if h := cmp.Or(asked, m.Engine); proto.RuntimeEngine(h) {
		return h
	}
	return ""
}

// fitEngineLauncher is fitLauncher where m's start in host goes to an engine runtime that runs only its own launchers
// (the node's list for that host says host_runtime): a launcher it lists runs as named; one it does not have gives way
// to its provider of the same kind of agent (sameKindLauncher), which params then names, keeping the model only when
// that provider offers it and the effort the start had (the engine lowers it to one the model offers, and says so);
// with none of that kind it is refused. done false: the node did not answer so for that host (its list unreadable, an
// older node, the engine away, or one that takes any launcher), and fitLauncher goes on as for a terminal.
func (s *Service) fitEngineLauncher(ctx context.Context, m Device, host string, params map[string]any) (note string, agent startAgent, err error, done bool) {
	name, _ := params["launcher"].(string)
	list, hostRuntime, known := s.launchersHereOn(withUnhidden(ctx, name), m.ID, host)
	if !known || !hostRuntime {
		return "", startAgent{}, nil, false
	}
	list = s.launchersWithinScope(ctx, m.ID, list)
	discovered := make([]ConfiguredLauncher, 0, len(list))
	for _, l := range list {
		discovered = append(discovered, ConfiguredLauncher{Launcher: l})
	}
	overrides := s.hello(ctx).Models.Launchers[m.ID]
	configured := sessionLaunchers(discovered, overrides)
	agentOf := func(name string) startAgent {
		for _, l := range configured {
			if l.Name == name {
				a := launcherStartAgent(l)
				a.snapshot = launcherKindSnapshot(discovered, overrides, name)
				return a
			}
		}
		return startAgent{}
	}
	if name == "" { // none named: the engine's default provider, the first it lists
		if i := slices.IndexFunc(list, func(l proto.Launcher) bool { return compat.Source(l.Source) == host }); i >= 0 {
			return "", agentOf(list[i].Name), nil, true
		}
		return "", startAgent{}, nil, true
	}
	if slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == name }) {
		return "", agentOf(name), nil, true
	}
	engine := proto.EngineNames[host]
	kind := s.launcherKindOn(ctx, m.ID, name)
	to := sameKindLauncher(list, host, kind)
	if to == "" {
		var names []string
		for _, l := range list {
			if compat.Source(l.Source) == host {
				names = append(names, l.Name)
			}
		}
		return "", startAgent{}, i18n.Err("assistant-tasks.launcher_kind_missing", i18n.Args{
			"engine": engine, "machine": firstNonEmpty(m.Name, m.ID), "launcher": name, "kind": kindName(kind),
			"launchers": cmp.Or(strings.Join(names, ", "), "—"),
		}), true
	}
	model, _ := params["model"].(string)
	effort, _ := params["effort"].(string)
	for _, k := range []string{"model", "effort", "model_fingerprint"} {
		delete(params, k)
	}
	params["launcher"] = to
	dropped := ""
	if model != "" && (s.launcherModelSet(ctx, m.ID, to)[model] || s.isCustomModel(ctx, m.ID, to, model)) {
		params["model"] = model
	} else {
		dropped = model // the old launcher's: the one standing in runs its own default
	}
	if effort != "" {
		params["effort"] = effort
	}
	if err := s.applyModel(ctx, "agent.new", params); err != nil {
		return "", startAgent{}, err, true
	}
	s.logf("start on %s: %s has no %s, using %s of the same kind (%s)", m.ID, engine, name, to, kind)
	note = apierr.StartInstead(s.lang(ctx), engine, firstNonEmpty(m.Name, m.ID), &proto.StartInstead{Launcher: name, Model: dropped},
		proto.Session{Launcher: to})
	current, _ := params["launcher"].(string)
	return note, agentOf(current), nil, true
}

// startInsteadNote is what a start's answer says the engine ran in place of what was asked (proto.StartInstead: the
// node's word, as the bridge gave it) in the user's language, for a task's why and agent_start's answer; "" when it
// ran as asked.
func (s *Service) startInsteadNote(ctx context.Context, m Device, in *proto.StartInstead, ran proto.Session) string {
	if in == nil {
		return ""
	}
	host := cmp.Or(agentStartHost(m, ran.Host), m.Engine)
	return apierr.StartInstead(s.lang(ctx), cmp.Or(proto.EngineNames[host], host), firstNonEmpty(m.Name, m.ID), in, ran)
}

// ranLauncher is the launcher a start ran: the session's when the engine says it ran another in place of the one asked
// for (proto.StartInstead), else asked. Its health and use are recorded under it.
func ranLauncher(asked string, in *proto.StartInstead, ran proto.Session) string {
	if in != nil && in.Launcher != "" && ran.Launcher != "" {
		return ran.Launcher
	}
	return asked
}

// launchersHereOn is launchersHere for a new Agent's start in host (machineLaunchersOn), and whether the node answered
// that every start there goes to an engine runtime that runs only its own launchers.
func (s *Service) launchersHereOn(ctx context.Context, machine, host string) (list []proto.Launcher, hostRuntime, known bool) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, hostRuntime, _, err := s.machineLaunchersOn(cctx, machine, host)
	if err != nil || !slices.ContainsFunc(list, func(l proto.Launcher) bool {
		source := compat.Source(l.Source)
		return source != "persona" && source != "builtin"
	}) {
		return nil, false, false
	}
	if !hostRuntime {
		for _, c := range s.hello(ctx).Models.Launchers[machine] {
			if c.Name != "" && !slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == c.Name }) {
				list = append(list, c.Launcher)
			}
		}
	}
	return list, hostRuntime, true
}

// launcherKindOn is the kind of agent launcher name starts for a start on machine (claude, codex, traex…): a provider
// kept in Persona's agent, hub.json's word for it there, the machine's last list's, else what its name says (codex,
// traex and omp by name, else Claude Code).
func (s *Service) launcherKindOn(ctx context.Context, machine, name string) string {
	if m, ok := s.managedNamed(ctx, name); ok {
		return m.AgentType()
	}
	if l, ok := s.configuredLauncher(ctx, machine, name); ok && l.AgentType != "" {
		return l.AgentType
	}
	if a := s.lastSeenAgent(ctx, machine, name).agent; a != "" {
		return a
	}
	switch name {
	case "codex", "traex", "omp", "agy":
		return name
	}
	return "claude"
}

// sameKindLauncher is the provider of engine host that stands in for a launcher of agent kind kind it does not have,
// as the engine's bridge picks it (integrations/paseo sameKindProvider): among its own launchers (Source host), in its
// order (its default first), its built-in of that kind (named kind) first, else the first of that kind. "" when it has
// none: nothing of another kind stands in.
func sameKindLauncher(list []proto.Launcher, host, kind string) string {
	if kind == "" {
		return ""
	}
	first := ""
	for _, l := range list {
		if compat.Source(l.Source) != host || entryKind(l, host) != kind {
			continue
		}
		if l.Name == kind {
			return l.Name
		}
		if first == "" {
			first = l.Name
		}
	}
	return first
}

// entryKind is the kind of agent an engine's launcher starts, as its list says it: Claude Code ("" or claude), Codex,
// or another the apps know; the engine's catch-all (its own id: Paseo's agents the apps do not know) names no kind.
func entryKind(l proto.Launcher, host string) string {
	switch l.Agent {
	case "", "claude":
		return "claude"
	case host:
		return ""
	}
	return l.Agent
}

// kindName is how a refusal names a kind of agent: the harness's name, or the kind itself for one Persona has no name
// for (traex).
func kindName(kind string) string {
	switch kind {
	case "claude", "codex", "agy", "omp":
		return harnessName(kind)
	}
	return kind
}
