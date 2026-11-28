package fleetcountadapt

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/fleetcount"
	"github.com/nwverse/persona/server/internal/assistant/fleetview"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/deeplink"
	"github.com/nwverse/persona/server/internal/domainkit/instant"
	"github.com/nwverse/persona/server/internal/domainkit/slot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/linkwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// LiveActivity writes the Live Activity's needs-you number and rows into st, by fleetcount's rows of the agents and
// cards f holds: st.NeedsYou, and the first fleetview.MaxItems of them as st.Items, which it makes with room for that
// many when there are any (the caller appends its working and finished rows after them). It appends each waiting
// agent's session to waiting, which the caller leaves out of the rows it adds. title is an agent's title for people
// before it is clipped. lang is the user's language (an i18n.Lang).
func (f *Fleet) LiveActivity(lang string, st *assistantwire.FleetState, waiting *[]*proto.Session, title func(*proto.Session) string) {
	r := &f.live
	r.f, r.lang, r.title, r.waiting, r.items = f, i18n.Lang(lang), title, *waiting, nil
	f.decide(fleetcount.SurfaceLiveActivity()).Accept(r)
	st.NeedsYou, st.Items, *waiting = r.total, r.items, r.waiting
	r.title, r.waiting, r.items = nil, nil, nil // what st and waiting hold is the caller's now
}

// liveRows fills the Live Activity's state from a tally. It keeps what it writes until LiveActivity hands it over, so
// that neither the caller's state nor its list is held by the Fleet.
type liveRows struct {
	f       *Fleet
	lang    i18n.Lang
	title   func(*proto.Session) string
	waiting []*proto.Session
	items   []assistantwire.FleetItem
	total   int
}

func (r *liveRows) VisitAgent(agent slot.Slot, _ fleetcount.Why, _ instant.Instant) {
	r.row(&r.f.seats[agent.Wire()], nil)
}

func (r *liveRows) VisitAgentByCard(agent, card slot.Slot, _ fleetcount.Why) {
	r.row(&r.f.seats[agent.Wire()], r.f.ixs[card.Wire()].ix)
}

func (r *liveRows) VisitCard(slot.Slot, fleetcount.Why) {} // the Live Activity lists no card of no session
func (r *liveRows) VisitCardUnder(slot.Slot)            {} // nor a row's further cards

func (r *liveRows) VisitTotal(total fleetcount.RowCount) { r.total = total.Wire() }

// row is one waiting agent, by the card that stands for it (nil: the dialog in its terminal).
func (r *liveRows) row(s *seat, ix *proto.Interaction) {
	r.waiting = append(r.waiting, s.session)
	if len(r.items) == fleetview.MaxItems {
		return // only the items shown are written out: their texts are most of what the state costs
	}
	if r.items == nil {
		r.items = make([]assistantwire.FleetItem, 0, fleetview.MaxItems)
	}
	r.items = append(r.items, needItem(r.lang, s, ix, r.title))
}

// needItem is the item of an agent that waits for the owner, by the card that stands for it.
func needItem(l i18n.Lang, s *seat, ix *proto.Interaction, title func(*proto.Session) string) assistantwire.FleetItem {
	sess := s.session
	item := assistantwire.FleetItem{ID: sess.MachineID + "/" + sess.SessionID, Title: fleetview.AgentTitle(title(sess), fleetview.TitleMax),
		Place: fleetview.AgentPlace(s.machine, sess, fleetview.PlaceMax), Kind: proto.IxScreen, Detail: i18n.T(l, "hub-liveactivity.screen")}
	if ix != nil {
		item.Kind, item.Detail = ix.Kind, textfmt.Clip(textfmt.OneLine(fleetview.InteractionDetail(l, *ix)), fleetview.DetailMax)
		if ix.Private { // a Just me turn's card: the Live Activity shows it without its text
			item.Detail = i18n.T(l, "push.private_card")
		}
		item.Interaction, item.Since = ix.ID, fleetview.UnixSeconds(ix.CreatedAt)
		if !ix.CreatedAt.IsZero() {
			item.InteractionCreatedAt = ix.CreatedAt.UTC().Format(time.RFC3339Nano)
		}
		if len(ix.Fields) > 0 || ix.ToolName == proto.SecretHandoffTool || !decidable(*ix) {
			// What it asks for (a password) is filled in in the app, or (a hand-off) approved on the card only
			// (hub/secret_handoff.go), or the card cannot say what approving does (#12982): no Allow / Deny on the
			// activity, a tap opens the request in the app.
			item.Interaction = ""
			item.InteractionCreatedAt = ""
		}
	}
	return item
}

// decidable: a card the Live Activity offers buttons for (a permission: Allow / Deny, a plan: Approve) says what
// approving does (#12982): a permission its summary (a bare tool name says nothing about what runs), a plan its title,
// and neither is private (a "Just me" card shows without its text). Other kinds never get buttons and keep their
// interaction, which names the card.
func decidable(ix proto.Interaction) bool {
	switch ix.Kind {
	case proto.IxPermission:
		return !ix.Private && strings.TrimSpace(ix.Summary) != ""
	case proto.IxPlan:
		return !ix.Private && strings.TrimSpace(textfmt.PlanTitle(ix.Plan)) != ""
	}
	return true
}

// Link is Persona Link's Needs you summary of the agents and cards f holds, without Rev and WebURL (the devices fill
// them in for each computer): fleetcount's rows newest first (past linkwire.AttentionMaxItems the oldest go to More, so
// an agent that just started waiting is always listed), those of the same time by machine and session. An item
// notifies once a card of its agent is the owner's, and never for a muted agent. title is an agent's title for people
// before it is clipped. lang is the user's language (an i18n.Lang).
func (f *Fleet) Link(lang string, title func(*proto.Session) string) linkwire.Attention {
	r := &f.link
	r.f, r.rows = f, r.rows[:0]
	f.decide(fleetcount.SurfaceLink()).Accept(r)
	slices.SortStableFunc(r.rows, func(a, b linkRow) int {
		x, y := f.seats[a.seat].session, f.seats[b.seat].session
		return cmp.Or(cmp.Compare(b.since, a.since), cmp.Compare(x.MachineID, y.MachineID), cmp.Compare(x.SessionID, y.SessionID))
	})
	out := linkwire.Attention{NeedsYou: r.total, Items: make([]linkwire.AttentionItem, 0, min(len(r.rows), linkwire.AttentionMaxItems))}
	for i := range r.rows {
		if i == linkwire.AttentionMaxItems {
			out.More = len(r.rows) - i
			break
		}
		out.Items = append(out.Items, f.linkItem(i18n.Lang(lang), &r.rows[i], title))
	}
	return out
}

// linkRows gathers Persona Link's rows from a tally, to be ordered before they are written out.
type linkRows struct {
	f     *Fleet
	rows  []linkRow
	total int
}

// linkRow is one waiting agent: its seat, the card that stands for it (-1: the dialog in its terminal), whether its
// summary rings for it, and since when it waits (UTC nanoseconds, 0 when unknown).
type linkRow struct {
	seat, card int
	rings      bool
	since      int64
}

func (r *linkRows) VisitAgent(agent slot.Slot, why fleetcount.Why, since instant.Instant) {
	r.rows = append(r.rows, linkRow{seat: int(agent.Wire()), card: -1, rings: r.rings(why), since: since.Wire()})
}

func (r *linkRows) VisitAgentByCard(agent, card slot.Slot, why fleetcount.Why) {
	at := wireTime(r.f.ixs[card.Wire()].ix.CreatedAt)
	r.rows = append(r.rows, linkRow{seat: int(agent.Wire()), card: int(card.Wire()), rings: r.rings(why), since: at.Wire()})
}

func (r *linkRows) VisitCard(slot.Slot, fleetcount.Why) {} // Persona Link lists no card of no session
func (r *linkRows) VisitCardUnder(slot.Slot)            {} // nor a row's further cards

func (r *linkRows) VisitTotal(total fleetcount.RowCount) { r.total = total.Wire() }

func (r *linkRows) rings(why fleetcount.Why) bool {
	why.Accept(&r.f.why)
	return r.f.why.rings
}

// linkItem is one agent of the summary, by the card that stands for it.
func (f *Fleet) linkItem(l i18n.Lang, row *linkRow, title func(*proto.Session) string) linkwire.AttentionItem {
	s := &f.seats[row.seat]
	sess := s.session
	it := linkwire.AttentionItem{MachineID: sess.MachineID, SessionID: sess.SessionID,
		Title: fleetview.Fit(fleetview.AgentTitle(title(sess), fleetview.LinkTextMax), fleetview.LinkTextMax),
		Place: fleetview.Fit(fleetview.AgentPlace(s.machine, sess, fleetview.LinkTextMax), fleetview.LinkTextMax),
		Kind:  proto.IxScreen, Detail: i18n.T(l, "hub-liveactivity.screen"), Muted: s.muted, Notify: row.rings,
		Route: deeplink.Session(sess.MachineID, sess.SessionID)}
	if row.card >= 0 {
		ix := f.ixs[row.card].ix
		it.Kind, it.Detail, it.InteractionID = ix.Kind, fleetview.Fit(textfmt.OneLine(fleetview.InteractionDetail(l, *ix)), fleetview.LinkDetailMax), ix.ID
	}
	if row.since != 0 {
		it.Since = time.Unix(0, row.since).UTC().Truncate(time.Second).Format(time.RFC3339)
	}
	return it
}
