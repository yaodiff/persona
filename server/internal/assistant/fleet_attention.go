package assistant

import (
	"cmp"
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/assistant/fleetview"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
)

// Persona Link's "Needs you" summary (internal/linkwire/attention.go, docs/operations/link.md "Needs you"): worked out
// in liveLoop from the Live Activity's inputs and handed to the devices, which send it to the computers' Persona Link
// when it changed. Both rules are the counting core's (fleetcount, its Persona Link surface), and the summary is its
// adapter's rendering (fleetcountadapt); they reuse two rules the product has, and write no third:
//
//   - What it lists and counts is the web app's "Needs you" badge (stateCounts().badge in web/src/lib/sessions.ts, the
//     count iOS shows) as it is: every agent whose row says "Needs you" but quiet tasks' agents and the assistant's
//     conversations, muted ones and ones waiting only on the assistant's decision included.
//   - Whether an item notifies (Notify) is the phone's push rule: once a card of the agent is the user's (pending.told:
//     tell rang it, when the phone is pushed; not while being recorded, nor while the assistant decides on it, nor a
//     card back from a reconnect or a restart that is not known to have rung), and never for a muted agent (mutedAs).
//     The web app's system notifications follow the same rule for agents with cards (lookingOnly, isMuted in
//     web/src/lib); for one in a terminal dialog before its card they ring at once, where the phone waits for the
//     screen card.
//
// spec/fixtures/attention holds the manager and the web app to both.
//
// The Live Activity counts otherwise (fleetcount's Live Activity surface: muted agents left out, questions being
// decided on counted) and does not change.

// linkTextMax and linkDetailMax are how long an item's title and place, and its detail, get in runes, the ellipsis
// that marks a cut included (fleetview, which the summary's rows are written with).
const (
	linkTextMax   = fleetview.LinkTextMax
	linkDetailMax = fleetview.LinkDetailMax
)

// linkAttention is the summary of the agents and pending interactions (fleetInputs), told the ids of the cards that
// are the user's; without Rev and WebURL: the devices fill them in for each computer. The same input in any order gives the same
// summary: cards and items of the same time are ordered by their ids.
func linkAttention(l i18n.Lang, agents []fleetInput, ixs []proto.Interaction, told map[string]bool) link.Attention {
	var r fleetRound
	return r.linkAttention(l, agents, ixs, told)
}

// linkAttention is the package's linkAttention over the arrays the last round kept. Which agents the summary lists and
// counts, by which card and whether each notifies is fleetcount's (fleetcount.AgentsNeedsYou, Persona Link's surface),
// and its items are fleetcountadapt's: the cards are read oldest first, those of the same time by id, so that the card
// that stands for an agent does not depend on the order they were read in.
func (r *fleetRound) linkAttention(l i18n.Lang, agents []fleetInput, ixs []proto.Interaction, told map[string]bool) link.Attention {
	r.fillLink(agents, ixs, told, nil)
	defer r.letLinkGo()
	return r.link.Link(string(l), sessionTitle)
}

// fillLink puts a round's agents and cards into the fleet Persona Link's summary is decided on, the cards oldest first
// and those of the same time by id (linkAttention), told which cards are the owner's. The counts view also takes
// quiet tasks' agents for its Persona card count.
func (r *fleetRound) fillLink(agents []fleetInput, ixs []proto.Interaction, told map[string]bool, quiet []fleetInput) {
	r.cards = append(r.cards[:0], ixs...)
	slices.SortStableFunc(r.cards, func(a, b proto.Interaction) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	r.fill(&r.link, agents, r.cards, told, quiet)
}

// letLinkGo lets the round's lists go until the next one.
func (r *fleetRound) letLinkGo() {
	r.link.Reset()
	clear(r.cards)
}

// linkCovers is whether card ix, told, is what Persona Link's summary (linkAttention, as liveLoop works it out next)
// shows for its agent and rings for: the card that stands for the agent, notifying, among the listed items, not in
// More. Only such a card's push may a Persona Link desktop take over from the phone (hub: notifyInteraction): another
// card of the same agent and one past the cap ring nowhere, and one whose row does not say "Needs you" yet is not
// rung for by the next summary.
func (s *Service) linkCovers(ix proto.Interaction) bool {
	agents, ixs, told := s.fleetInputs()
	l := s.lang(context.Background())
	s.fleet.live.roundMu.Lock()
	summary := s.fleet.live.round.linkAttention(l, agents, ixs, told)
	s.fleet.live.roundMu.Unlock()
	for _, it := range summary.Items {
		if it.InteractionID == ix.ID && it.MachineID == ix.MachineID && it.SessionID == ix.SessionID {
			return it.Notify
		}
	}
	return false
}
