package fleetcount

import (
	"slices"

	"github.com/nwverse/persona/server/internal/agentref"
	"github.com/nwverse/persona/server/internal/domainkit/defect"
	"github.com/nwverse/persona/server/internal/domainkit/instant"
	"github.com/nwverse/persona/server/internal/domainkit/slot"
)

// rowKind is which of TallyVisitor's calls a row is.
type rowKind struct{ rowKind uint8 }

func agentRow() rowKind       { return rowKind{rowKind: 1} }
func cardRow() rowKind        { return rowKind{rowKind: 2} }
func agentByCardRow() rowKind { return rowKind{rowKind: 3} }
func cardUnderRow() rowKind   { return rowKind{rowKind: 4} }

// row is one entry of a tally: a row, or a further card of the row before it.
type row struct {
	kind  rowKind
	agent slot.Slot
	card  slot.Slot
	why   Why
	since instant.Instant
}

// Tally is the rows that need the owner on one surface, in the order the surface lists them, and the surface's number.
// The zero Tally is empty, and on no surface.
type Tally struct {
	tally []row
	total RowCount
	on    Surface
}

// TallyVisitor is told every row of a tally, then the total.
type TallyVisitor interface {
	VisitAgent(agent slot.Slot, why Why, since instant.Instant) // an agent waiting with no card standing for it
	VisitCard(card slot.Slot, why Why)                          // a card counted by itself, without an agent row
	VisitAgentByCard(agent, card slot.Slot, why Why)            // an agent its oldest standing card stands for
	VisitCardUnder(card slot.Slot)                              // a further card of the row told just before
	VisitTotal(total RowCount)
}

// Accept tells v every row, then the total.
func (t Tally) Accept(v TallyVisitor) {
	for i := range t.tally {
		r := &t.tally[i]
		switch r.kind {
		case agentRow():
			v.VisitAgent(r.agent, r.why, r.since)
		case cardRow():
			v.VisitCard(r.card, r.why)
		case agentByCardRow():
			v.VisitAgentByCard(r.agent, r.card, r.why)
		case cardUnderRow():
			v.VisitCardUnder(r.card)
		}
	}
	v.VisitTotal(t.total)
}

// On says whether the tally is a surface's rows: what renders one surface's rows refuses another's.
func (t Tally) On(surface Surface) bool { return t.on == surface && surface != (Surface{}) }

// AgentsNeedsYou is the agents and cards that need the owner on a surface, and the surface's number.
//
//   - The Agents list (SurfaceBadge) is the needs-you fixture's (spec/fixtures/needs-you): a row per waiting session,
//     its cards under it oldest first, a child's and a side session's under the row the hub folds them under, and a
//     card from no session a row of its own; rows go oldest first. Muted agents, quiet tasks' agents and sessions whose
//     only cards are questions the assistant is still deciding on are listed but not counted; a directory-hidden
//     session and a card its task's supervisor holds are in no row.
//   - The Live Activity and Persona Link list an agent by its first card that stands for it, as the cards come, then the
//     agents waiting in a terminal dialog, in the agents' order; neither folds, nor lists a card from no session. Persona
//     Link's card is the first one handed to the owner while the agent has one (its summary orders the rows newest first
//     itself). An agent whose every card its task's supervisor holds is none of theirs, and neither is a quiet task's
//     agent or a directory-hidden one (Visibility.LeftOut), with its cards.
//   - Persona counts one row per pending owner card on its conversations and quiet tasks' agents (PersonaCards).
//
// So one list of agents and cards serves every surface: the caller hands in the quiet and the hidden with the rest. The
// zero Surface is a defect.
func AgentsNeedsYou(on Surface, agents Agents, cards Cards) Tally {
	var t Tally
	switch on {
	case SurfaceBadge():
		t = badgeRows(agents.agents, cards.cards)
	case SurfaceLiveActivity(), SurfaceLink():
		t = fleetRows(on, agents.agents, cards.cards)
	case SurfacePersonaCards():
		return PersonaCards(agents, Agents{}, cards)
	default:
		defect.Raise("fleetcount.AgentsNeedsYou", "the zero Surface")
		return Tally{}
	}
	t.on = on
	return t
}

// PersonaCards is one row per pending card for the owner on a Persona conversation or a quiet task's agent (D9/D11).
// The manager can supply the quiet agents it keeps out of the other surfaces as a second list. Each card follows the
// session that asked it, including when the session folds under another row. Forms count too, and a terminal wait
// contributes only through its pending card. Sessionless cards belong to the Agents badge.
func PersonaCards(agents, quiet Agents, cards Cards) Tally {
	on := SurfacePersonaCards()
	if len(cards.cards) == 0 {
		return Tally{on: on}
	}
	eligible := make(map[agentref.Ref]struct{})
	for _, list := range [2][]Agent{agents.agents, quiet.agents} {
		for i := range list {
			a := &list[i]
			if a.Counts(on) {
				eligible[a.ref] = struct{}{}
			}
		}
	}
	if len(eligible) == 0 {
		return Tally{on: on}
	}
	var rows []row
	for i := range cards.cards {
		c := &cards.cards[i]
		if c.holding != HoldingForTheOwner() || c.standing == CardStandingDisplayCopy() {
			continue
		}
		c.asker.Match(func(ref agentref.Ref) {
			if _, ok := eligible[ref]; ok {
				rows = append(rows, row{kind: cardRow(), card: c.at, why: WhyNeedsYou()})
			}
		}, func() {})
	}
	return Tally{tally: rows, total: RowCount{rowCount: len(rows)}, on: on}
}

// mark is what the cards say of one agent that counts on the surface, as the bits below.
type mark struct{ mark uint8 }

const (
	markAsked      = 1 << iota // a card stands for it already
	markSupervised             // its task's supervisor holds one of its cards
	markOther                  // one of its cards is not the supervisor's
	markHanded                 // one of its cards was handed to the owner
)

func (m mark) has(bit uint8) bool { return m.mark&bit != 0 }

// supervisedOnly says whether every card of the agent is its task's supervisor's: its needs-you is the supervisor's.
func (m mark) supervisedOnly() bool { return m.has(markSupervised) && !m.has(markOther) }

// fleetRows is the Live Activity's and Persona Link's rows. An agent's cards are found by its session; a session
// listed twice is the later listing's, as the cards cannot tell them apart.
func fleetRows(on Surface, agents []Agent, cards []Card) Tally {
	var marks []mark
	if len(cards) > 0 {
		marks = make([]mark, len(agents))
		for i := range cards {
			c := &cards[i]
			if c.standing == CardStandingDisplayCopy() {
				continue
			}
			bits := uint8(markOther)
			if c.holding == HoldingHeldBySupervisor() {
				bits = markSupervised
			}
			if c.handed() {
				bits |= markHanded
			}
			c.asker.Match(func(ref agentref.Ref) { markAll(on, agents, marks, ref, bits) }, func() {})
		}
	}
	rows := make([]row, 0, fleetBound(agents, cards))
	for i := range cards {
		c := &cards[i]
		if c.standing == CardStandingDisplayCopy() {
			continue
		}
		c.asker.Match(func(ref agentref.Ref) {
			j := lastCounting(on, agents, ref)
			if j < 0 || marks[j].has(markAsked) || marks[j].supervisedOnly() || !stands(on, c, marks[j]) {
				return
			}
			markAll(on, agents, marks, ref, markAsked)
			rows = append(rows, row{kind: agentByCardRow(), agent: agents[j].at, card: c.at, why: fleetWhy(on, agents[j], marks[j])})
		}, func() {})
	}
	for i := range agents {
		a := &agents[i]
		var m mark
		if marks != nil {
			m = marks[i]
		}
		if a.Counts(on) && a.waitsInItsTerminal() && !m.has(markAsked) && !m.supervisedOnly() {
			rows = append(rows, row{kind: agentRow(), agent: a.at, why: fleetWhy(on, *a, m), since: a.waitingSince()})
		}
	}
	return Tally{tally: rows, total: RowCount{rowCount: len(rows)}}
}

// fleetBound is the most rows fleetRows can make: one per card with an asker and one per agent the hub says waits.
func fleetBound(agents []Agent, cards []Card) int {
	n := 0
	for i := range agents {
		if agents[i].state == StateNeedsYou() {
			n++
		}
	}
	for i := range cards {
		if !cards[i].asker.IsNone() {
			n++
		}
	}
	return n
}

// markAll sets bits on every agent of ref that counts on the surface.
func markAll(on Surface, agents []Agent, marks []mark, ref agentref.Ref, bits uint8) {
	for i := range agents {
		if agents[i].ref == ref && agents[i].Counts(on) {
			marks[i].mark |= bits
		}
	}
}

// lastCounting is the place of the last agent of ref that counts on the surface, -1 when none does.
func lastCounting(on Surface, agents []Agent, ref agentref.Ref) int {
	for i := len(agents) - 1; i >= 0; i-- {
		if agents[i].ref == ref && agents[i].Counts(on) {
			return i
		}
	}
	return -1
}

// stands says whether a card may stand for its agent on the Live Activity or in Persona Link's summary: never a form,
// and in Persona Link's only one handed to the owner while the agent has one.
func stands(on Surface, c *Card, m mark) bool {
	if c.standing != CardStandingStands() {
		return false
	}
	return on != SurfaceLink() || c.handed() || !m.has(markHanded)
}

// fleetWhy is a Live Activity or Persona Link row's reason. The Live Activity's rows all wait for the owner (it counts
// the questions the assistant is still deciding on, and lists no muted agent); Persona Link's are muted, or held while
// none of the agent's cards was handed to the owner, which its summary does not ring for.
func fleetWhy(on Surface, a Agent, m mark) Why {
	switch {
	case on == SurfaceLiveActivity():
		return WhyNeedsYou()
	case a.muting == MutingMuted():
		return WhyMuted()
	case !m.has(markHanded):
		return WhyHeld()
	}
	return WhyNeedsYou()
}

// badgeRows is the Agents list's rows (see AgentsNeedsYou). Each row is put together in parallel lists by its place
// among the rows: its agent (-1 for a card of no session), its first and last card (places in cards, -1 for none; the
// cards between follow the first through next), whether a card or a dialog of it is the owner's, whether an agent of
// it waits in its terminal and since when. The lists are cut from three arrays made to the most rows there can be
// (one per card and one per agent waiting in its terminal), so a round makes four arrays whatever it holds.
func badgeRows(agents []Agent, cards []Card) Tally {
	bound := len(cards)
	for i := range agents {
		if agents[i].waitsInItsTerminal() {
			bound++
		}
	}
	if bound == 0 { // no card and no dialog: no row, and nothing to put one together with
		return Tally{}
	}
	ints := make([]int, 2*len(agents)+2*len(cards)+4*bound)
	cutInts := func(n int) []int { part := ints[:n:n]; ints = ints[n:]; return part }
	flags := make([]bool, 2*len(agents)+2*bound)
	cutFlags := func(n int) []bool { part := flags[:n:n]; flags = flags[n:]; return part }
	times := make([]instant.Instant, 2*bound)
	cutTimes := func(n int) []instant.Instant { part := times[:n:n]; times = times[n:]; return part }

	host := cutInts(len(agents)) // the agent whose row each agent's cards and dialog go to, -1 for none
	for i := range agents {
		host[i] = hostOf(agents, i)
	}
	order, next := cutInts(len(cards)), cutInts(len(cards)) // the cards, oldest first; each row's card after this one
	for i := range order {
		order[i], next[i] = i, -1
	}
	slices.SortStableFunc(order, func(x, y int) int { return compareInstants(cards[x].asked, cards[y].asked) })
	carded := cutFlags(len(agents))     // the agent has a card in the list
	supervised := cutFlags(len(agents)) // the agent has a card its task's supervisor holds
	agentOf, first, last := cutInts(bound)[:0], cutInts(bound)[:0], cutInts(bound)[:0]
	own, waits, waiting := cutFlags(bound)[:0], cutFlags(bound)[:0], cutTimes(bound)[:0]
	var unknown instant.Instant
	rowOf := cutInts(len(agents)) // each row agent's place among the rows, plus one; 0 for none yet
	join := func(h int) int {
		if rowOf[h] == 0 {
			agentOf, first, last = append(agentOf, h), append(first, -1), append(last, -1)
			own, waits, waiting = append(own, false), append(waits, false), append(waiting, unknown)
			rowOf[h] = len(agentOf)
		}
		return rowOf[h] - 1
	}
	for _, ci := range order {
		switch h := badgeHost(agents, host, &cards[ci], carded, supervised); {
		case h == -1:
			agentOf, first, last = append(agentOf, -1), append(first, ci), append(last, ci)
			own, waits, waiting = append(own, cards[ci].holding == HoldingForTheOwner()), append(waits, false), append(waiting, unknown)
		case h >= 0:
			g := join(h)
			if first[g] < 0 {
				first[g] = ci
			} else {
				next[last[g]] = ci
			}
			last[g] = ci
			own[g] = own[g] || cards[ci].holding == HoldingForTheOwner()
		}
	}
	for i := range agents {
		if host[i] < 0 || !agents[i].waitsInItsTerminal() || carded[i] || supervised[i] {
			continue
		}
		g := join(host[i])
		if since := agents[i].waitingSince(); !waits[g] || since.Before(waiting[g]) {
			waiting[g] = since
		}
		waits[g], own[g] = true, true
	}
	since := cutTimes(bound)[:len(agentOf)] // what each row is ordered by: its oldest card, else its dialog
	rows := cutInts(bound)[:len(agentOf)]
	for g := range agentOf {
		since[g], rows[g] = waiting[g], g
		if first[g] >= 0 {
			since[g] = cards[first[g]].asked
		}
	}
	slices.SortStableFunc(rows, func(x, y int) int { return compareInstants(since[x], since[y]) })
	out := make([]row, 0, len(cards)+len(rows))
	counted := 0
	for _, g := range rows {
		why := badgeWhy(agents, agentOf[g], own[g])
		if why.Counted() {
			counted++
		}
		switch {
		case agentOf[g] < 0:
			out = append(out, row{kind: cardRow(), card: cards[first[g]].at, why: why})
		case first[g] < 0:
			out = append(out, row{kind: agentRow(), agent: agents[agentOf[g]].at, why: why, since: since[g]})
		default:
			out = append(out, row{kind: agentByCardRow(), agent: agents[agentOf[g]].at, card: cards[first[g]].at, why: why})
			for ci := next[first[g]]; ci >= 0; ci = next[ci] {
				out = append(out, row{kind: cardUnderRow(), card: cards[ci].at})
			}
		}
	}
	return Tally{tally: out, total: RowCount{rowCount: counted}}
}

// compareInstants orders two instants, an unknown one first.
func compareInstants(a, b instant.Instant) int {
	switch {
	case a.Before(b):
		return -1
	case b.Before(a):
		return 1
	}
	return 0
}

// hostOf is the agent whose row agent i's cards and dialog go to: its own, the one the hub folds it under while that
// one is listed (following a fold of a fold), or -1 for an agent the list does not show.
func hostOf(agents []Agent, i int) int {
	if !agents[i].listed() {
		return -1
	}
	for range len(agents) { // a fold that comes back to itself stops at the agent it started from
		under, ok := agents[i].placement.(placementUnder)
		if !ok {
			return i
		}
		j := lastListed(agents, under.under)
		if j < 0 || j == i {
			return i
		}
		i = j
	}
	return i
}

// lastListed is the place of the last agent of ref the Agents list shows, -1 when none.
func lastListed(agents []Agent, ref agentref.Ref) int {
	for i := len(agents) - 1; i >= 0; i-- {
		if agents[i].ref == ref && agents[i].listed() {
			return i
		}
	}
	return -1
}

// badgeHost is the agent whose row a card is under (-1: a row of its own, -2: none), and it marks the asking agent
// as having a card in the list, or one its task's supervisor holds.
func badgeHost(agents []Agent, host []int, c *Card, carded, supervised []bool) int {
	h := -2
	if c.standing == CardStandingDisplayCopy() {
		return h
	}
	c.asker.Match(func(ref agentref.Ref) {
		j := -1
		for i := len(agents) - 1; i >= 0; i-- {
			if agents[i].ref == ref {
				j = i
				break
			}
		}
		if j < 0 || host[j] < 0 {
			return // an agent the list does not show: the card counts nowhere
		}
		if c.holding == HoldingHeldBySupervisor() {
			supervised[j] = true
			return
		}
		carded[j], h = true, host[j]
	}, func() {
		if c.holding != HoldingHeldBySupervisor() {
			h = -1
		}
	})
	return h
}

// badgeWhy is an Agents list row's reason: its agent (-1 for none) muted or a quiet task's, else waiting while a card
// or a dialog of it is the owner's (own), else held (only questions the assistant is still deciding on).
func badgeWhy(agents []Agent, agent int, own bool) Why {
	if agent >= 0 {
		switch a := agents[agent]; {
		case a.muting == MutingMuted():
			return WhyMuted()
		case a.visibility == VisibilityQuiet():
			return WhyQuiet()
		}
	}
	if own {
		return WhyNeedsYou()
	}
	return WhyHeld()
}
