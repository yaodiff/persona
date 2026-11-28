package fleetcountadapt

import (
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/agentref"
	"github.com/nwverse/persona/server/internal/assistant/fleetcount"
	"github.com/nwverse/persona/server/internal/domainkit/fail"
	"github.com/nwverse/persona/server/internal/domainkit/instant"
	"github.com/nwverse/persona/server/internal/domainkit/opt"
	"github.com/nwverse/persona/server/internal/domainkit/outcome"
	"github.com/nwverse/persona/server/internal/domainkit/slot"
	"github.com/nwverse/persona/server/internal/proto"
)

// Fleet is one round's agents and cards, as the caller lists them, and the rows fleetcount decides for each surface.
// Each agent and card keeps the caller's session or interaction at its slot, which the renderers read
// the wire's text from. A caller keeps a Fleet from round to round, so the arrays it fills are reused; nothing
// fleetcount owns is. The zero Fleet is empty.
type Fleet struct {
	seats  []seat
	ixs    []card
	agents []fleetcount.Agent
	cards  []fleetcount.Card
	quiet  []fleetcount.Agent // quiet task agents supplied for the Persona count only
	core   coreValues
	live   liveRows
	link   linkRows
	counts countsRows
	why    whyWord
}

// coreValues is the round's agents and cards as fleetcount holds them, made once a round for every surface decided on
// them, so its copy of them is made once.
type coreValues struct {
	agents fleetcount.Agents
	cards  fleetcount.Cards
	made   bool
}

// seat is one agent as the caller has it: its computer's name for people, its session (read, never written) and
// whether the owner muted it.
type seat struct {
	machine string
	session *proto.Session
	muted   bool
}

// card is one pending card as the caller has it: the interaction (read, never written) and whether the owner was told
// of it.
type card struct {
	ix   *proto.Interaction
	told bool
}

// Reset empties f for a new round, keeping its arrays. The sessions and cards of the last round are let go, so a
// Fleet kept between rounds holds none of their lists.
func (f *Fleet) Reset() {
	clear(f.seats)
	clear(f.ixs)
	clear(f.agents)
	clear(f.cards)
	clear(f.quiet)
	f.seats, f.ixs, f.agents, f.cards = f.seats[:0], f.ixs[:0], f.agents[:0], f.cards[:0]
	f.quiet = f.quiet[:0]
	f.core = coreValues{}
}

// Agent adds the agent a session is: machine is its computer's name for people, muted whether the owner muted it.
// A session without a machine or a session id fails with agentref's failure and is not added; it is never filled in.
func (f *Fleet) Agent(machine string, s *proto.Session, muted bool) outcome.Of[fleetcount.Agent, fail.Fail] {
	built := Agent(s, muted, len(f.seats))
	if a, failed := outcome.Unpack(built); failed == nil {
		f.seats = append(f.seats, seat{machine: machine, session: s, muted: muted})
		f.agents = append(f.agents, a)
		f.core = coreValues{}
	}
	return built
}

// PersonaAgent adds a quiet task's agent for the Persona card count, apart from the other surfaces' agents.
func (f *Fleet) PersonaAgent(s *proto.Session, muted bool) outcome.Of[fleetcount.Agent, fail.Fail] {
	built := Agent(s, muted, len(f.quiet))
	if a, failed := outcome.Unpack(built); failed == nil {
		f.quiet = append(f.quiet, a)
	}
	return built
}

// Card adds the card an interaction is, told whether the owner was told of it. A card without an id, or one that
// names a session without its machine, fails and is not added.
func (f *Fleet) Card(ix *proto.Interaction, told bool) outcome.Of[fleetcount.Card, fail.Fail] {
	built := Card(ix, told, len(f.ixs))
	if c, failed := outcome.Unpack(built); failed == nil {
		f.ixs = append(f.ixs, card{ix: ix, told: told})
		f.cards = append(f.cards, c)
		f.core = coreValues{}
	}
	return built
}

// Agents is the agents f holds, as fleetcount takes them (fleetcount.AgentsNeedsYou). Every surface decided on them in
// a round shares the one copy fleetcount makes.
func (f *Fleet) Agents() fleetcount.Agents {
	f.made()
	return f.core.agents
}

// Cards is the cards f holds, as fleetcount takes them, shared as Agents is.
func (f *Fleet) Cards() fleetcount.Cards {
	f.made()
	return f.core.cards
}

func (f *Fleet) made() {
	if !f.core.made {
		f.core = coreValues{agents: fleetcount.NewAgents(f.agents), cards: fleetcount.NewCards(f.cards), made: true}
	}
}

// decide is fleetcount's rows of f's agents and cards on a surface.
func (f *Fleet) decide(on fleetcount.Surface) fleetcount.Tally {
	return fleetcount.AgentsNeedsYou(on, f.Agents(), f.Cards())
}

// Visibility is whether the owner's lists show a session's agent: hidden by an Agent-folder rule first, else a quiet
// task's, else listed.
func Visibility(s *proto.Session) fleetcount.Visibility {
	switch {
	case s.HiddenByDirectory():
		return fleetcount.VisibilityHiddenByDirectory()
	case s.Quiet:
		return fleetcount.VisibilityQuiet()
	}
	return fleetcount.VisibilityListed()
}

// Agent is the agent a session is, at place at of the caller's list (muted: the owner muted it): a session without a
// machine or a session id fails with agentref's failure, one the builder refuses with fleetcount's. It is never
// filled in.
func Agent(s *proto.Session, muted bool, at int) outcome.Of[fleetcount.Agent, fail.Fail] {
	return outcome.Then(agentref.ParseRef(s.MachineID, s.SessionID), func(ref agentref.Ref) outcome.Of[fleetcount.Agent, fleetcount.Fail] {
		since := opt.None[instant.Instant]()
		if at := wireTime(s.StateSince); at.Known() {
			since = opt.Some(at)
		}
		role, lineage, muting := fleetcount.RoleWorker(), fleetcount.LineageRoot(), fleetcount.MutingHeard()
		if s.Assistant {
			role = fleetcount.RoleAssistant()
		}
		if s.IsSubtask() {
			lineage = fleetcount.LineageSubtask()
		}
		if muted {
			muting = fleetcount.MutingMuted()
		}
		return fleetcount.NewAgentBuilder(ref, slot.NewSlot(at)).
			State(state(s.State), since, wireTime(s.UpdatedAt)).
			Role(role, lineage, muting).
			Place(Visibility(s), placement(s, ref)).
			Build()
	})
}

// wireTime is a time the wire carries as an instant: unknown for the zero time, and for one the kit cannot hold
// (outside the years 1678 to 2262), which the wire's decoding lets through.
func wireTime(t time.Time) instant.Instant {
	if t.IsZero() || !time.Unix(0, t.UnixNano()).Equal(t) {
		var unknown instant.Instant
		return unknown
	}
	return instant.At(t)
}

// state is a wire state as fleetcount knows it; one from a newer peer is idle.
func state(wire string) fleetcount.State {
	switch wire {
	case proto.StateWorking:
		return fleetcount.StateWorking()
	case proto.StateNeedsYou:
		return fleetcount.StateNeedsYou()
	case proto.StateUnread:
		return fleetcount.StateFinished()
	}
	return fleetcount.StateIdle()
}

// placement is where the hub folds a session: under the row folded_under names (on the computer folded_under_machine_id
// names when it is another), else a row of its own.
func placement(s *proto.Session, ref agentref.Ref) fleetcount.Placement {
	under := s.FoldedUnder()
	if under == "" {
		return fleetcount.PlacementOwnRow()
	}
	machine := s.FoldedUnderMachineID()
	if machine == "" {
		machine = s.MachineID
	}
	parent, f := outcome.Unpack(agentref.ParseRef(machine, under))
	if f != nil || parent.IsSame(ref) {
		return fleetcount.PlacementOwnRow() // a fold that names no session, or the session itself, folds nothing
	}
	return fleetcount.PlacementUnder(parent)
}

// Card is the card an interaction is, at place at of the caller's list (told: the owner was told of it): a card
// without an id, or one that names a session without its machine, fails.
func Card(ix *proto.Interaction, told bool, at int) outcome.Of[fleetcount.Card, fail.Fail] {
	return outcome.Then(agentref.ParseInteractionID(ix.ID), func(id agentref.InteractionID) outcome.Of[fleetcount.Card, fail.Fail] {
		return outcome.Then(asker(ix), func(by opt.Option[agentref.Ref]) outcome.Of[fleetcount.Card, fleetcount.Fail] {
			standing := fleetcount.CardStandingStands()
			switch {
			case ix.MirrorOf != "":
				standing = fleetcount.CardStandingDisplayCopy()
			case ix.Kind == proto.IxElicitation:
				standing = fleetcount.CardStandingForm()
			}
			telling := fleetcount.TellingUntold()
			if told {
				telling = fleetcount.TellingTold()
			}
			return fleetcount.NewCardBuilder(id, slot.NewSlot(at)).
				Asker(by).
				Standing(standing, holding(ix), telling, wireTime(ix.CreatedAt)).
				Build()
		})
	})
}

// asker is the session that asked a card, or none for a card from no Agent (an empty session_id).
func asker(ix *proto.Interaction) outcome.Of[opt.Option[agentref.Ref], agentref.Fail] {
	if ix.SessionID == "" {
		var out outcome.Of[opt.Option[agentref.Ref], agentref.Fail]
		return out.Ok(opt.None[agentref.Ref]())
	}
	return outcome.Map(agentref.ParseRef(ix.MachineID, ix.SessionID), opt.Some[agentref.Ref])
}

// holding is whose a card is to answer first: a delegating question is the assistant's to decide on, any other
// delegating card its task's supervisor's (ask_route.go), the rest the owner's.
func holding(ix *proto.Interaction) fleetcount.Holding {
	switch {
	case !ix.Delegating:
		return fleetcount.HoldingForTheOwner()
	case ix.Kind == proto.IxQuestion:
		return fleetcount.HoldingHeldByPersona()
	}
	return fleetcount.HoldingHeldBySupervisor()
}

// Log is a failure Agent or Card returned, for a log line, and only for one: "invalid agentref.machine.empty
// machine", every part of a list of invalid parts joined by "; ".
func Log(f fail.Fail) string {
	var d described
	f.Accept(&d)
	return d.text.String()
}

// described writes a failure's kind, code and part as a log line.
type described struct{ text strings.Builder }

func (d *described) line(words ...string) {
	if d.text.Len() > 0 {
		d.text.WriteString("; ")
	}
	d.text.WriteString(strings.Join(words, " "))
}

func (d *described) VisitInvalid(code fail.Code, part fail.Part) {
	d.line("invalid", code.Wire(), part.Wire())
}
func (d *described) VisitRefused(code fail.Code)  { d.line("refused", code.Wire()) }
func (d *described) VisitMissing(code fail.Code)  { d.line("missing", code.Wire()) }
func (d *described) VisitConflict(code fail.Code) { d.line("conflict", code.Wire()) }

// whyWord is a reason as the wire spells it, and whether Persona Link rings for it.
type whyWord struct {
	word  string
	rings bool
}

func (w *whyWord) VisitNeedsYou() { w.word, w.rings = "needs_you", true }
func (w *whyWord) VisitHeld()     { w.word, w.rings = "held", false }
func (w *whyWord) VisitMuted()    { w.word, w.rings = "muted", false }
func (w *whyWord) VisitQuiet()    { w.word, w.rings = "quiet", false }
