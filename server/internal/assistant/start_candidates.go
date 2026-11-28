package assistant

import (
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/proto"
)

// startCandidate is one preset a start-defaults rule may choose on a machine: whether it can start there now, and
// why not. Availability mirrors the start path exactly (a hidden launcher whose health check passes still counts,
// an absent health record leaves a preset offered), so a preview and a real start never disagree.
type startCandidate struct {
	Name      string
	Launcher  string
	Agent     string // configured metadata from the same inventory; empty means unknown, never guessed
	Available bool
	Reason    string // why it cannot start now; "" when Available
}

// startCandidateSource lists the presets a rule can pick from on a machine. The built-in source reads the active
// model profile with the machine's launcher list and health, the same data GET /api/v1/assistant/worker-presets
// serves. A dynamic candidates source implements this one interface and is set on the Service at construction:
// rule matching, priority/weight selection and the per-try retry reset in start_defaults.go stay unchanged.
type startCandidateSource interface {
	candidates(ctx context.Context, machine string, names []string) (map[string]startCandidate, error)
}

// requestedStartCandidateNames limits availability work to the rule's choices, without changing their declared order.
func requestedStartCandidateNames(choices []PresetChoice) []string {
	names := make([]string, 0, len(choices))
	seen := make(map[string]bool, len(choices))
	for _, choice := range choices {
		if choice.Name == "" || seen[choice.Name] {
			continue
		}
		seen[choice.Name] = true
		names = append(names, choice.Name)
	}
	return names
}

// candidateSource answers with the Service's source, or the model-profile one when none was injected.
func (s *Service) candidateSource() startCandidateSource {
	if s.startCandidates != nil {
		return s.startCandidates
	}
	return profileCandidateSource{s: s}
}

// profileCandidateSource is the built-in startCandidateSource: the active model profile's tiers, judged on the
// machine's own launcher list and health (launchersHere, hiddenUsable, launcherUnusable), never pickerTiers'
// filtered list, which would hide the reason a preset was passed over.
type profileCandidateSource struct{ s *Service }

// candidateAgentLookup mirrors fitLauncher's managed, last-seen and configured metadata decisions for one inventory.
// An unknown wrapper stays unknown; only real launcher metadata or a named built-in determines the agent.
func (s *Service) candidateAgentLookup(ctx context.Context, machine string, list []proto.Launcher, known bool) func(string) string {
	discovered := make([]ConfiguredLauncher, 0, len(list))
	for _, l := range list {
		discovered = append(discovered, ConfiguredLauncher{Launcher: l})
	}
	agentOf := configuredStartAgentLookup(discovered, s.hello(ctx).Models.Launchers[machine])
	return func(name string) string {
		if agent, err := s.launcherWithoutInventory(ctx, machine, name); agent.agent != "" || err != nil {
			return agent.agent
		}
		if !known {
			return s.lastSeenAgent(ctx, machine, name).agent
		}
		return agentOf(name).agent
	}
}

type hiddenCandidateMetadata struct {
	usable bool
	agent  string
}

// launcherAvailability judges launchers on one machine the way a start does: its ordinary launcher list read once,
// a hidden launcher probed once (its health check passes: it still starts, #4661), then the launcher's health
// (launcherUnusable). profileCandidateSource and pickTierCandidate share it, so a preset offered, a candidate picked
// and the start that follows agree.
type launcherAvailability struct {
	s          *Service
	machine    string
	loaded     bool
	list       []proto.Launcher
	listKnown  bool
	agentOf    func(string) string
	hidden     []string
	hiddenRows map[string]hiddenCandidateMetadata
}

func (s *Service) availabilityOn(machine string) *launcherAvailability {
	return &launcherAvailability{s: s, machine: machine, hiddenRows: map[string]hiddenCandidateMetadata{}}
}

// check says whether launcher can start on the machine now, why not, and the agent it runs (configured metadata;
// "" unknown).
func (a *launcherAvailability) check(ctx context.Context, launcher string) (available bool, reason, agent string) {
	if err := a.s.launcherScopeErr(ctx, a.machine, launcher); err != nil {
		return false, err.Error(), ""
	}
	if !a.loaded {
		a.list, a.listKnown = a.s.launchersHere(ctx, a.machine)
		a.agentOf = a.s.candidateAgentLookup(ctx, a.machine, a.list, a.listKnown)
		a.hidden = a.s.hiddenLaunchers(ctx)
		a.loaded = true
	}
	agent = a.agentOf(launcher)
	// A hidden launcher is startable when its health check passes; a plain one must be in the machine's list.
	has := !a.listKnown || slices.ContainsFunc(a.list, func(l proto.Launcher) bool { return l.Name == launcher })
	if !has && slices.Contains(a.hidden, launcher) {
		row, probed := a.hiddenRows[launcher]
		if !probed {
			hiddenList, known, usable := a.s.hiddenLauncherInventory(ctx, a.machine, launcher)
			row = hiddenCandidateMetadata{usable: usable, agent: a.s.candidateAgentLookup(ctx, a.machine, hiddenList, known)(launcher)}
			a.hiddenRows[launcher] = row
		}
		has = row.usable
		if has {
			agent = row.agent
		}
	}
	if a.listKnown && !has {
		return false, "this computer has no launcher " + launcher, agent
	}
	if _, bad := a.s.launcherUnusable(ctx, a.machine, launcher); bad {
		return false, launcher + " is not available yet", agent
	}
	return true, "", agent
}

// candidates are the presets names (or their aliases) on machine: a preset with several candidates is available when
// any is, and offers the first available one in its order; with none available it says why its first is not.
func (p profileCandidateSource) candidates(ctx context.Context, machine string, names []string) (map[string]startCandidate, error) {
	prof, _, err := p.s.activeModelProfile(ctx)
	if err != nil {
		return nil, err
	}
	if prof == nil {
		return map[string]startCandidate{}, nil
	}
	// Read the ordinary inventory once. Explicitly requested hidden launchers still start (#4661); cache each
	// unhidden probe and its metadata, including a false result, so repeated tiers never repeat its launcher-list RPC.
	avail := p.s.availabilityOn(machine)
	out := make(map[string]startCandidate, len(names))
	for _, name := range names {
		own, ok := prof.lookupTier(name)
		if !ok {
			continue
		}
		var c startCandidate
		for i, cand := range prof.Tiers[own].list() {
			available, reason, agent := avail.check(ctx, cand.Launcher)
			if i == 0 || available {
				c = startCandidate{Name: name, Launcher: cand.Launcher, Agent: agent, Available: available, Reason: reason}
			}
			if available {
				break
			}
		}
		out[name] = c
	}
	return out, nil
}
