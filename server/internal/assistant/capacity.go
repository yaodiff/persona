package assistant

import (
	"context"
	"fmt"

	"github.com/nwverse/persona/server/internal/proto"
)

// Account-pool capacity in launcher selection (docs/server/usage-windows.md "Account-pool gateways",
// docs/server/launcher-health.md "Capacity-aware notes"). The manager already collects, read-only, how many
// subscription accounts of each upstream provider are schedulable and when the nearest one recovers.
//
// Machines are not reordered by capacity: one launcher maps to one upstream provider, so every machine offering it
// sees the same provider-level capacity. Launchers are chosen by quota only among the candidates the user listed,
// in their order, on one tier (candidate_headroom.go, candidate_score.go, #14701): two providers are not equivalent,
// and the model configuration is never silently changed outside that list (docs/assistant/model-config.md "Tier
// candidates are not a silent change"); a switch says why on the task card. What capacity adds here is the
// "why and when" on the detour path (healthDetour, the one chokepoint the agent_start tool and task plans share):
// when a launcher is rerouted or started anyway despite a failed check, a saturated pool is named, with the nearest
// recovery time. Stale or missing data says nothing and changes nothing.

// capacitySnapshot is the current view; zero when there is no fleet or no pool data.
func (s *Service) capacitySnapshot(ctx context.Context) proto.CapacitySnapshot {
	if s.devs == nil {
		return proto.CapacitySnapshot{}
	}
	return s.devs.CapacitySnapshot(ctx)
}

// launcherProvider maps a launcher on a machine to the upstream provider id its pooled accounts are listed under
// (proto.ProviderCapacity.Provider, the "claude" / "codex" vocabulary). The health record already names the agent
// the check ran there, a Persona-managed launcher names its agent too, and so does the machine's rc-file or companion
// launcher as the hub last saw it (no node is asked; #17011: the built-in agy launcher is only there). A launcher that
// names no agent but is called after one (codex, agy…) runs that agent, as viewAgent reads it. "" when unknown.
func (s *Service) launcherProvider(ctx context.Context, machine, launcher string) string {
	if r, ok := s.launcherHealth(ctx, machine, launcher); ok && r.Agent != "" {
		return r.Agent
	}
	for _, l := range s.storedLaunchers(ctx) {
		if l.Name == launcher && l.Agent != "" {
			return l.Agent
		}
	}
	seen, _ := s.zshrcLaunchersLastSeen(ctx)
	if l, ok := launcherInSnapshot(seen, machine, launcher); ok {
		if agent := namedAgent(l.Name, l.Agent); agent != "" {
			return agent
		}
	}
	return ""
}

// namedAgent is the agent a launcher runs: its own agent field, else the agent it is named after.
func namedAgent(name, agent string) string {
	return viewAgent(LauncherView{Name: name, Agent: agent})
}

// capacityNote is a sentence about the provider's pool when it is freshly known to be saturated: "" with no data,
// stale data, or any schedulable account. The recovery time is relative (schedule.go's until).
func (s *Service) capacityNote(ctx context.Context, provider string) string {
	if provider == "" {
		return ""
	}
	p, ok := s.capacitySnapshot(ctx).Provider(provider)
	if !ok || !p.Saturated() {
		return ""
	}
	if p.EarliestRecovery != nil {
		return fmt.Sprintf("no schedulable %s account in the pool right now; the nearest one recovers %s", provider, until(*p.EarliestRecovery))
	}
	return fmt.Sprintf("no schedulable %s account in the pool right now", provider)
}

// poolSuffix renders a saturated-pool note as a detour line's sentence suffix ("; …" or "").
func poolSuffix(note string) string {
	if note == "" {
		return ""
	}
	return "; " + note
}
