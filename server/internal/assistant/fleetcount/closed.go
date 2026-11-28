package fleetcount

import (
	"github.com/nwverse/persona/server/internal/agentref"
	"github.com/nwverse/persona/server/internal/domainkit/defect"
)

// State is where an agent's turn stands. The zero State is no state; Build refuses it.
type State struct{ state uint8 }

// StateWorking is an agent at work.
func StateWorking() State { return State{state: 1} }

// StateNeedsYou is an agent waiting for the owner: the hub says so, from a card or a dialog its terminal shows.
func StateNeedsYou() State { return State{state: 2} }

// StateFinished is an agent whose turn ended and whose result the owner has not seen.
func StateFinished() State { return State{state: 3} }

// StateIdle is an agent with no turn: idle, offline, or in a state a newer peer sent.
func StateIdle() State { return State{state: 4} }

// StateVisitor is told which state it is.
type StateVisitor interface {
	VisitWorking()
	VisitNeedsYou()
	VisitFinished()
	VisitIdle()
}

// Accept tells v which state it is; the zero State is a defect.
func (s State) Accept(v StateVisitor) {
	switch s {
	case StateWorking():
		v.VisitWorking()
	case StateNeedsYou():
		v.VisitNeedsYou()
	case StateFinished():
		v.VisitFinished()
	case StateIdle():
		v.VisitIdle()
	default:
		defect.Raise("fleetcount.State.Accept", "the zero State")
	}
}

// Role is whose conversation an agent is. The zero Role is no role; Build refuses it.
type Role struct{ role uint8 }

// RoleWorker is an agent doing a task.
func RoleWorker() Role { return Role{role: 1} }

// RoleAssistant is one of the assistant's own conversations, the Persona tab's.
func RoleAssistant() Role { return Role{role: 2} }

// RoleVisitor is told which role it is.
type RoleVisitor interface {
	VisitWorker()
	VisitAssistant()
}

// Accept tells v which role it is; the zero Role is a defect.
func (r Role) Accept(v RoleVisitor) {
	switch r {
	case RoleWorker():
		v.VisitWorker()
	case RoleAssistant():
		v.VisitAssistant()
	default:
		defect.Raise("fleetcount.Role.Accept", "the zero Role")
	}
}

// Lineage says whether another agent started this one. The zero Lineage is none; Build refuses it.
type Lineage struct{ lineage uint8 }

// LineageRoot is an agent nobody started: it has a row of its own.
func LineageRoot() Lineage { return Lineage{lineage: 1} }

// LineageSubtask is an agent another one started (a Codex sub-agent, a runtime's session another created), which its
// parent's row stands for on the Live Activity.
func LineageSubtask() Lineage { return Lineage{lineage: 2} }

// LineageVisitor is told which lineage it is.
type LineageVisitor interface {
	VisitRoot()
	VisitSubtask()
}

// Accept tells v which lineage it is; the zero Lineage is a defect.
func (l Lineage) Accept(v LineageVisitor) {
	switch l {
	case LineageRoot():
		v.VisitRoot()
	case LineageSubtask():
		v.VisitSubtask()
	default:
		defect.Raise("fleetcount.Lineage.Accept", "the zero Lineage")
	}
}

// Muting says whether the owner muted an agent (itself, or its space). The zero Muting is none; Build refuses it.
type Muting struct{ muting uint8 }

// MutingHeard is an agent the owner hears from.
func MutingHeard() Muting { return Muting{muting: 1} }

// MutingMuted is an agent the owner muted.
func MutingMuted() Muting { return Muting{muting: 2} }

// MutingVisitor is told which muting it is.
type MutingVisitor interface {
	VisitHeard()
	VisitMuted()
}

// Accept tells v which muting it is; the zero Muting is a defect.
func (m Muting) Accept(v MutingVisitor) {
	switch m {
	case MutingHeard():
		v.VisitHeard()
	case MutingMuted():
		v.VisitMuted()
	default:
		defect.Raise("fleetcount.Muting.Accept", "the zero Muting")
	}
}

// Visibility is whether the owner's lists show an agent. The zero Visibility is none; Build refuses it.
type Visibility struct{ visibility uint8 }

// VisibilityListed is an agent the lists show.
func VisibilityListed() Visibility { return Visibility{visibility: 1} }

// VisibilityQuiet is a quiet task's agent: listed, while its task's main conversation tells the owner about it
// (#1731).
func VisibilityQuiet() Visibility { return Visibility{visibility: 2} }

// VisibilityHiddenByDirectory is an agent an Agent-folder hide rule matches: no list shows it, and its cards count
// nowhere.
func VisibilityHiddenByDirectory() Visibility { return Visibility{visibility: 3} }

// LeftOut says whether the fleet's Live Activity and Persona Link's summary leave the agent out: a quiet task's agent
// and a directory-hidden one are none of theirs, nor are their cards. Agent.Counts asks it for both surfaces, so an
// agent it leaves out counts on neither whoever hands it in; a caller that drops such an agent before it builds one asks
// the same. The zero Visibility is a defect.
func (v Visibility) LeftOut() bool {
	if v == (Visibility{}) {
		defect.Raise("fleetcount.Visibility.LeftOut", "the zero Visibility")
	}
	return v != VisibilityListed()
}

// VisibilityVisitor is told which visibility it is.
type VisibilityVisitor interface {
	VisitListed()
	VisitQuiet()
	VisitHiddenByDirectory()
}

// Accept tells v which visibility it is; the zero Visibility is a defect.
func (v Visibility) Accept(visitor VisibilityVisitor) {
	switch v {
	case VisibilityListed():
		visitor.VisitListed()
	case VisibilityQuiet():
		visitor.VisitQuiet()
	case VisibilityHiddenByDirectory():
		visitor.VisitHiddenByDirectory()
	default:
		defect.Raise("fleetcount.Visibility.Accept", "the zero Visibility")
	}
}

// Placement is where an agent's row is in the Agents list. The zero Placement is nil; Build refuses it.
type Placement interface {
	Accept(v PlacementVisitor)
	placement() // sealed: only this package makes one
}

// PlacementVisitor is told which placement it is, with the agent whose row it is folded under.
type PlacementVisitor interface {
	VisitOwnRow()
	VisitUnder(under agentref.Ref)
}

// placementOwnRow is a row of the agent's own.
type placementOwnRow struct{}

// placementUnder is folded under another agent's row, the one the hub names in folded_under.
type placementUnder struct{ under agentref.Ref }

// PlacementOwnRow is an agent with a row of its own.
func PlacementOwnRow() Placement { return placementOwnRow{} }

// PlacementUnder is an agent the hub folds under another one's row (a child of a task, or a side session): its cards
// sit under that row, which waits for it. The zero Ref is a defect.
func PlacementUnder(under agentref.Ref) Placement {
	var noSession agentref.Ref
	if under == noSession {
		defect.Raise("fleetcount.PlacementUnder", "the zero Ref")
	}
	return placementUnder{under: under}
}

func (placementOwnRow) Accept(v PlacementVisitor)  { v.VisitOwnRow() }
func (p placementUnder) Accept(v PlacementVisitor) { v.VisitUnder(p.under) }
func (placementOwnRow) placement()                 {}
func (placementUnder) placement()                  {}

// CardStanding is whether a card can stand for the agent that asked it. The zero CardStanding is none; Build refuses
// it.
type CardStanding struct{ cardStanding uint8 }

// CardStandingStands is a card that stands for its agent wherever the agent counts: a permission, a plan, a question
// or a terminal dialog's card.
func CardStandingStands() CardStanding { return CardStanding{cardStanding: 1} }

// CardStandingForm is an MCP server's form (an elicitation): it makes its agent wait in the Agents list, but the Live
// Activity and Persona Link show the agent by its terminal dialog instead, as the legacy clients that share the Fleet
// draw it.
func CardStandingForm() CardStanding { return CardStanding{cardStanding: 2} }

// CardStandingDisplayCopy is a display mirror of another card. It contributes no pending action of its own.
func CardStandingDisplayCopy() CardStanding { return CardStanding{cardStanding: 3} }

// CardStandingVisitor is told whether the card can stand for its agent.
type CardStandingVisitor interface {
	VisitStands()
	VisitForm()
	VisitDisplayCopy()
}

// Accept tells v whether the card can stand for its agent; the zero CardStanding is a defect.
func (c CardStanding) Accept(v CardStandingVisitor) {
	switch c {
	case CardStandingStands():
		v.VisitStands()
	case CardStandingForm():
		v.VisitForm()
	case CardStandingDisplayCopy():
		v.VisitDisplayCopy()
	default:
		defect.Raise("fleetcount.CardStanding.Accept", "the zero CardStanding")
	}
}

// Telling is whether the owner was told of a card: the push rang for it. The zero Telling is none; Build refuses it.
type Telling struct{ telling uint8 }

// TellingTold is a card the owner was told of.
func TellingTold() Telling { return Telling{telling: 1} }

// TellingUntold is a card the owner was not told of yet: not announced, being recorded, or back from a reconnect that
// is not known to have rung.
func TellingUntold() Telling { return Telling{telling: 2} }

// TellingVisitor is told whether the owner was told of the card.
type TellingVisitor interface {
	VisitTold()
	VisitUntold()
}

// Accept tells v whether the owner was told of the card; the zero Telling is a defect.
func (t Telling) Accept(v TellingVisitor) {
	switch t {
	case TellingTold():
		v.VisitTold()
	case TellingUntold():
		v.VisitUntold()
	default:
		defect.Raise("fleetcount.Telling.Accept", "the zero Telling")
	}
}

// Holding is whose a card is to answer first (D11 of docs/plans/plan-derived-counts.md). The zero Holding is none;
// Build refuses it.
type Holding struct{ holding uint8 }

// HoldingForTheOwner is a card that is the owner's to answer.
func HoldingForTheOwner() Holding { return Holding{holding: 1} }

// HoldingHeldByPersona is a question the assistant is still deciding whether it may answer for the owner: listed and
// answerable, and counted nowhere in the Agents list until it is handed to the owner.
func HoldingHeldByPersona() Holding { return Holding{holding: 2} }

// HoldingHeldBySupervisor is a card its task's supervisor holds (a permission, a plan or a routed terminal question)
// until it passes it up: the supervisor's, in no row of the owner's Agents list.
func HoldingHeldBySupervisor() Holding { return Holding{holding: 3} }

// HoldingVisitor is told whose the card is to answer first.
type HoldingVisitor interface {
	VisitForTheOwner()
	VisitHeldByPersona()
	VisitHeldBySupervisor()
}

// Accept tells v whose the card is to answer first; the zero Holding is a defect.
func (h Holding) Accept(v HoldingVisitor) {
	switch h {
	case HoldingForTheOwner():
		v.VisitForTheOwner()
	case HoldingHeldByPersona():
		v.VisitHeldByPersona()
	case HoldingHeldBySupervisor():
		v.VisitHeldBySupervisor()
	default:
		defect.Raise("fleetcount.Holding.Accept", "the zero Holding")
	}
}

// Surface is where a Needs you number and its rows are shown. The zero Surface is none; AgentsNeedsYou raises a
// defect on it.
type Surface struct{ surface uint8 }

// SurfaceBadge is the Agents list's Needs you: its rows, its header, its filter and the tab's badge, as the needs-you
// fixture (spec/fixtures/needs-you) defines them.
func SurfaceBadge() Surface { return Surface{surface: 1} }

// SurfaceLiveActivity is the Fleet Live Activity on the lock screen and in the Dynamic Island.
func SurfaceLiveActivity() Surface { return Surface{surface: 2} }

// SurfaceLink is Persona Link's Needs you summary on the owner's computers, which counts as the web app's badge does.
func SurfaceLink() Surface { return Surface{surface: 3} }

// SurfacePersonaCards counts the pending cards the Persona conversation shows the owner.
func SurfacePersonaCards() Surface { return Surface{surface: 4} }

// SurfaceVisitor is told which surface it is.
type SurfaceVisitor interface {
	VisitBadge()
	VisitLiveActivity()
	VisitLink()
	VisitPersonaCards()
}

// Accept tells v which surface it is; the zero Surface is a defect.
func (s Surface) Accept(v SurfaceVisitor) {
	switch s {
	case SurfaceBadge():
		v.VisitBadge()
	case SurfaceLiveActivity():
		v.VisitLiveActivity()
	case SurfaceLink():
		v.VisitLink()
	case SurfacePersonaCards():
		v.VisitPersonaCards()
	default:
		defect.Raise("fleetcount.Surface.Accept", "the zero Surface")
	}
}

// Why is why a row is listed. The zero Why is none; a Tally never holds it.
type Why struct{ why uint8 }

// WhyNeedsYou is a row that waits for the owner.
func WhyNeedsYou() Why { return Why{why: 1} }

// WhyHeld is a row whose cards are not the owner's yet: in the Agents list, questions the assistant is still deciding
// on (D11); in Persona Link's summary, an agent none of whose cards was handed to the owner, which it does not ring
// for.
func WhyHeld() Why { return Why{why: 2} }

// WhyMuted is a row of an agent the owner muted.
func WhyMuted() Why { return Why{why: 3} }

// WhyQuiet is a row of a quiet task's agent, which its task's main conversation tells the owner about.
func WhyQuiet() Why { return Why{why: 4} }

// Counted says whether a row for this reason counts in the Agents list's Needs you number. The Live Activity and
// Persona Link's summary still count by rules of their own (docs/plans/plan-derived-counts.md, section 1), so each
// surface's number is the total its Tally tells. The zero Why is a defect.
func (w Why) Counted() bool {
	if w == (Why{}) {
		defect.Raise("fleetcount.Why.Counted", "the zero Why")
	}
	return w == WhyNeedsYou()
}

// WhyVisitor is told which reason a row has.
type WhyVisitor interface {
	VisitNeedsYou()
	VisitHeld()
	VisitMuted()
	VisitQuiet()
}

// Accept tells v the reason; the zero Why is a defect.
func (w Why) Accept(v WhyVisitor) {
	switch w {
	case WhyNeedsYou():
		v.VisitNeedsYou()
	case WhyHeld():
		v.VisitHeld()
	case WhyMuted():
		v.VisitMuted()
	case WhyQuiet():
		v.VisitQuiet()
	default:
		defect.Raise("fleetcount.Why.Accept", "the zero Why")
	}
}

// RowCount is how many rows of a tally count. The zero RowCount is no rows.
type RowCount struct{ rowCount int }

// ParseRowCount is n rows; a negative n is a defect, since only a tally counts rows.
func ParseRowCount(n int) RowCount {
	if n < 0 {
		defect.Raise("fleetcount.ParseRowCount", "a negative count")
	}
	return RowCount{rowCount: n}
}

// Wire is the count as the wire carries it. Adapters only.
func (r RowCount) Wire() int { return r.rowCount }
