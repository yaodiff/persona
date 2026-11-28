package fleetcount

import (
	"slices"

	"github.com/nwverse/persona/server/internal/agentref"
	"github.com/nwverse/persona/server/internal/domainkit/defect"
	"github.com/nwverse/persona/server/internal/domainkit/fail"
	"github.com/nwverse/persona/server/internal/domainkit/instant"
	"github.com/nwverse/persona/server/internal/domainkit/opt"
	"github.com/nwverse/persona/server/internal/domainkit/outcome"
	"github.com/nwverse/persona/server/internal/domainkit/slot"
)

// Agent is one agent as the fleet sees it, at the caller's place for it. The zero Agent is no agent; only Build makes
// one, and NewAgents raises a defect on it.
type Agent struct {
	ref        agentref.Ref
	at         slot.Slot
	state      State
	since      opt.Option[instant.Instant]
	updated    instant.Instant
	role       Role
	lineage    Lineage
	muting     Muting
	visibility Visibility
	placement  Placement
}

// AgentStateStep is the builder's first stage: the agent's state. The zero AgentStateStep is a skipped stage, and
// Build refuses what it leaves out.
type AgentStateStep struct{ agentStateStep Agent }

// AgentRoleStep is the builder's second stage: whose the agent is. The zero AgentRoleStep is a skipped stage, and
// Build refuses what it leaves out.
type AgentRoleStep struct{ agentRoleStep Agent }

// AgentPlaceStep is the builder's third stage: where the agent is shown. The zero AgentPlaceStep is a skipped stage,
// and Build refuses what it leaves out.
type AgentPlaceStep struct{ agentPlaceStep Agent }

// AgentBuilder is the builder's last stage. The zero AgentBuilder is a skipped stage, and Build refuses what it leaves
// out.
type AgentBuilder struct{ agentBuilder Agent }

// NewAgentBuilder starts an agent: the session it is and the caller's place for it.
func NewAgentBuilder(ref agentref.Ref, at slot.Slot) AgentStateStep {
	return AgentStateStep{agentStateStep: Agent{ref: ref, at: at}}
}

// State is the agent's state, since when it holds (none when the hub does not say) and when the agent last changed
// (unknown when the hub does not say either).
func (s AgentStateStep) State(state State, since opt.Option[instant.Instant], updated instant.Instant) AgentRoleStep {
	a := s.agentStateStep
	a.state, a.since, a.updated = state, since, updated
	return AgentRoleStep{agentRoleStep: a}
}

// Role is whose conversation the agent is, whether another agent started it and whether the owner muted it.
func (s AgentRoleStep) Role(role Role, lineage Lineage, muting Muting) AgentPlaceStep {
	a := s.agentRoleStep
	a.role, a.lineage, a.muting = role, lineage, muting
	return AgentPlaceStep{agentPlaceStep: a}
}

// Place is whether the lists show the agent and where its row is.
func (s AgentPlaceStep) Place(visibility Visibility, placement Placement) AgentBuilder {
	a := s.agentPlaceStep
	a.visibility, a.placement = visibility, placement
	return AgentBuilder{agentBuilder: a}
}

// Build is the agent, or every part a skipped stage or a zero value left out, in order. An unknown update time is
// not refused: the kit's zero Instant is unknown, and an agent the hub sent without one still waits.
func (b AgentBuilder) Build() (out outcome.Of[Agent, Fail]) {
	a := b.agentBuilder
	var parts []fail.Invalid
	var noSession agentref.Ref
	parts = wrong(parts, a.ref != noSession, "fleetcount.agent.no_session", "agent")
	parts = wrong(parts, a.state != (State{}), "fleetcount.agent.no_state", "state")
	parts = wrong(parts, a.role != (Role{}), "fleetcount.agent.no_role", "role")
	parts = wrong(parts, a.lineage != (Lineage{}), "fleetcount.agent.no_lineage", "lineage")
	parts = wrong(parts, a.muting != (Muting{}), "fleetcount.agent.no_muting", "muting")
	parts = wrong(parts, a.visibility != (Visibility{}), "fleetcount.agent.no_visibility", "visibility")
	parts = wrong(parts, a.placement != nil, "fleetcount.agent.no_placement", "placement")
	if len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	return out.Ok(a)
}

// Counts says whether the agent can count on a surface at all. The Live Activity and Persona Link's summary leave out
// every agent its visibility leaves out (LeftOut: a quiet task's agent and a directory-hidden one), and with it its
// cards; beyond that, the Live Activity leaves out muted agents and subtasks, whose parent's row stands for them, and
// Persona Link's summary counts, as the web app's badge does, every agent whose row says it needs you but the
// assistant's conversations, muted ones included. The Agents list leaves out the assistant's conversations,
// directory-hidden agents, and muted and quiet agents, which it lists without counting them. Persona counts cards
// from its conversations and quiet tasks' agents when the asking session is neither muted nor directory-hidden.
// The zero Agent and the zero Surface are defects.
func (a Agent) Counts(on Surface) bool {
	a.known("fleetcount.Agent.Counts")
	switch on {
	case SurfaceLiveActivity():
		return !a.visibility.LeftOut() && a.lineage == LineageRoot() && a.muting == MutingHeard()
	case SurfaceLink():
		return !a.visibility.LeftOut() && a.role == RoleWorker() && a.state == StateNeedsYou()
	case SurfaceBadge():
		return a.listed() && a.muting == MutingHeard() && a.visibility == VisibilityListed()
	case SurfacePersonaCards():
		return a.muting == MutingHeard() && a.visibility != VisibilityHiddenByDirectory() &&
			(a.role == RoleAssistant() || a.visibility == VisibilityQuiet())
	}
	defect.Raise("fleetcount.Agent.Counts", "the zero Surface")
	return false
}

// listed says whether the Agents list may show a row for the agent: not one of the assistant's conversations, and
// not one a hide rule matches.
func (a Agent) listed() bool {
	return a.role == RoleWorker() && a.visibility != VisibilityHiddenByDirectory()
}

// waitsInItsTerminal says whether the hub says the agent needs the owner, a dialog its terminal shows when no card
// stands for it; the assistant's own dialogs never count.
func (a Agent) waitsInItsTerminal() bool { return a.state == StateNeedsYou() && a.role == RoleWorker() }

// waitingSince is when the agent started waiting: when its state began, else when it last changed (unknown when
// neither is known).
func (a Agent) waitingSince() instant.Instant {
	since := a.updated
	a.since.Match(func(t instant.Instant) { since = t }, func() {})
	return since
}

// known raises a defect when a is the zero Agent, which no function takes; op names the function it was handed to.
func (a Agent) known(op string) {
	var noSession agentref.Ref
	if a.ref == noSession {
		defect.Raise(op, "the zero Agent")
	}
}

// Card is one pending card, at the caller's place for it. The zero Card is no card; only Build makes one, and NewCards
// raises a defect on it.
type Card struct {
	id       agentref.InteractionID
	at       slot.Slot
	asker    opt.Option[agentref.Ref]
	standing CardStanding
	holding  Holding
	telling  Telling
	asked    instant.Instant
}

// CardAskerStep is the card builder's first stage: who asked. The zero CardAskerStep is a skipped stage, and Build
// refuses what it leaves out.
type CardAskerStep struct{ cardAskerStep Card }

// CardStandingStep is the card builder's second stage: what the card stands for and whose it is.
// The zero CardStandingStep is a skipped stage, and Build refuses what it leaves out.
type CardStandingStep struct{ cardStandingStep Card }

// CardBuilder is the card builder's last stage. The zero CardBuilder is a skipped stage, and Build refuses what it
// leaves out.
type CardBuilder struct{ cardBuilder Card }

// NewCardBuilder starts a card: its id and the caller's place for it.
func NewCardBuilder(id agentref.InteractionID, at slot.Slot) CardAskerStep {
	return CardAskerStep{cardAskerStep: Card{id: id, at: at}}
}

// Asker is the session that asked, or none: a card from no Agent (a script's ask, the assistant's own question) is a
// row of its own in the Agents list.
func (s CardAskerStep) Asker(asker opt.Option[agentref.Ref]) CardStandingStep {
	c := s.cardAskerStep
	c.asker = asker
	return CardStandingStep{cardStandingStep: c}
}

// Standing is what the card may stand for, whose it is to answer, whether the owner was told of it and when it was
// asked (unknown when the hub does not say).
func (s CardStandingStep) Standing(standing CardStanding, holding Holding, telling Telling, asked instant.Instant) CardBuilder {
	c := s.cardStandingStep
	c.standing, c.holding, c.telling, c.asked = standing, holding, telling, asked
	return CardBuilder{cardBuilder: c}
}

// Build is the card, or every part a skipped stage or a zero value left out, in order.
func (b CardBuilder) Build() (out outcome.Of[Card, Fail]) {
	c := b.cardBuilder
	var parts []fail.Invalid
	var noCard agentref.InteractionID
	parts = wrong(parts, c.id != noCard, "fleetcount.card.no_id", "card")
	parts = wrong(parts, c.standing != (CardStanding{}), "fleetcount.card.no_standing", "standing")
	parts = wrong(parts, c.holding != (Holding{}), "fleetcount.card.no_holding", "holding")
	parts = wrong(parts, c.telling != (Telling{}), "fleetcount.card.no_telling", "telling")
	if len(parts) > 0 {
		return out.Err(invalidParts(parts))
	}
	return out.Ok(c)
}

// handed says whether the card is the owner's to answer and the owner was told of it.
func (c Card) handed() bool { return c.telling == TellingTold() && c.holding == HoldingForTheOwner() }

// Agents is the fleet's agents, in the caller's order. The zero Agents is none.
type Agents struct{ agents []Agent }

// NewAgents is the agents in list. It copies list, so the core owns its array and the caller may reuse its slice; a
// zero Agent in it is a defect.
func NewAgents(list []Agent) Agents {
	for i := range list {
		list[i].known("fleetcount.NewAgents")
	}
	return Agents{agents: slices.Clone(list)}
}

// Cards is the pending cards, oldest first, as the caller lists them. The zero Cards is none.
type Cards struct{ cards []Card }

// NewCards is the cards in list, oldest first. It copies list, so the core owns its array and the caller may reuse
// its slice; a zero Card in it is a defect.
func NewCards(list []Card) Cards {
	var noCard agentref.InteractionID
	for i := range list {
		if list[i].id == noCard {
			defect.Raise("fleetcount.NewCards", "the zero Card")
		}
	}
	return Cards{cards: slices.Clone(list)}
}
