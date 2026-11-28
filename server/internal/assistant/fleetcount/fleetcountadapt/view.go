package fleetcountadapt

import (
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistant/fleetcount"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/domainkit/defect"
	"github.com/nwverse/persona/server/internal/domainkit/instant"
	"github.com/nwverse/persona/server/internal/domainkit/opt"
	"github.com/nwverse/persona/server/internal/domainkit/slot"
)

// viewRowsMax is how many rows a count's keys and held list each, at most (schema/wire/counts.schema.json).
const viewRowsMax = 200

// viewRoom is what a view takes besides its rows, at most: the epoch, rev, as_of and build the caller fills in, the
// totals, each count's n and more, and the punctuation around them. The rows get the rest of
// assistantwire.CountsViewMaxBytes.
const viewRoom = 1 << 10

// View is the counts view of the Agents list's tally of f's agents and cards (fleetcount.AgentsNeedsYou on
// fleetcount.SurfaceBadge(), docs/protocol/counts.md "agents_needs_you"), its counts and totals made in one pass over
// the tally, so its rows and its number cannot disagree: counts.agents_needs_you lists the counted rows, oldest first,
// as keys (agent:<machine>/<session> with the row's cards, card:<id> for a card on no session), the rows the count
// leaves out as held with their reason's wire word, and n is the tally's total, which totals repeat. A row for a reason
// the wire has no word for (a quiet task's) is in neither list. keys and held list at most 200 rows each, and fewer
// when the view would pass assistantwire.CountsViewMaxBytes: the counted rows keys leaves out are more. The caller
// fills in the view's epoch, rev, as_of and build. A tally of another surface, or one with places f does not hold,
// is a defect.
//
// counts.persona_cards is fleetcount.PersonaCards over the same snapshot and the quiet agents supplied for Persona.
// Each counted card has a card:<id> row, and more covers any cards beyond the row cap or the bytes left after the
// Agents count. Excluded cards have no row. A manager that works out this count always supplies its n and total.
//
// todos are the to-do list's counts, as GET /api/v1/todos answers them for the whole list (the to-do projection's
// Counts()), or none while the manager has not counted its to-dos: counts.todos_needs_you is their needs_you, with
// totals repeating it, and absent while there are none, which reads as unknown, never 0 (docs/protocol/counts.md
// "todos_needs_you"). Those counts are numbers without their rows, so the count lists no keys and its n is all more.
// The to-dos have no core yet; once they do, their counts come in as its value rather than the wire's, and this
// function stays their writer.
//
// The Inbox has no count in the view yet (docs/protocol/counts.md, "Later ids"). Until it has a core of its own, its
// count comes in as one more input of this function, today's number as package assistant works it out, so no record
// is made for it.
func (f *Fleet) View(needsYou fleetcount.Tally, todos opt.Option[assistantwire.TodoCounts]) assistantwire.CountsView {
	if !needsYou.On(fleetcount.SurfaceBadge()) {
		defect.Raise("fleetcountadapt.Fleet.View", "a tally of another surface than the Agents list's")
	}
	persona := fleetcount.PersonaCards(f.Agents(), fleetcount.NewAgents(f.quiet), f.Cards())
	room := assistantwire.CountsViewMaxBytes - viewRoom
	// Keep both counts and their totals in one allocation. The view outlives the round's scratch arrays.
	part := &struct {
		agents, persona   assistantwire.CountsViewCount
		agentsN, personaN int
		totals            assistantwire.CountsViewTotals
	}{}
	part.agents = f.counts.write(f, needsYou, &room)
	part.persona = f.counts.write(f, persona, &room)
	part.agentsN, part.personaN = part.agents.N, part.persona.N
	counts := assistantwire.CountsViewCounts{AgentsNeedsYou: &part.agents, PersonaCards: &part.persona}
	totals := &part.totals
	totals.AgentsNeedsYou, totals.PersonaCards = &part.agentsN, &part.personaN
	todos.Match(func(c assistantwire.TodoCounts) { writeTodos(&counts, totals, c) }, todosUncounted)
	return assistantwire.CountsView{
		Counts: counts,
		Totals: totals,
	}
}

// writeTodos writes the to-do list's counts as counts.todos_needs_you and its total, in one allocation: their
// needs_you, with no row listed, so all of n is more.
func writeTodos(counts *assistantwire.CountsViewCounts, totals *assistantwire.CountsViewTotals, todos assistantwire.TodoCounts) {
	part := &struct {
		count assistantwire.CountsViewCount
		n     int
	}{count: assistantwire.CountsViewCount{N: todos.NeedsYou, More: todos.NeedsYou}, n: todos.NeedsYou}
	counts.TodosNeedsYou, totals.TodosNeedsYou = &part.count, &part.n
}

// todosUncounted leaves counts.todos_needs_you out: the manager has not counted its to-dos, so the count is unknown,
// never 0.
func todosUncounted() {}

// countsRows gathers a tally's rows by place in f's lists, then writes them out as a count. It keeps its arrays from
// one view to the next; the count it writes holds none of them.
type countsRows struct {
	f     *Fleet
	rows  []countsRow
	cards []int32 // every row's cards, by place in f.ixs: row i's are cards[rows[i].from:rows[i].to]
	keys  []span  // where each listed row's key is in the string the keys are written into
	why   whyWord
	last  int // the row a further card goes under, -1 after a row the view leaves out
	total int
}

// countsRow is one row: its agent's place in f.seats (-1 for an individual card), its cards, why the count leaves
// it out ("" when it counts) and whether the view lists it.
type countsRow struct {
	seat     int32
	from, to int32
	held     assistantwire.CountsHeldReason
	listed   bool
}

type span struct{ from, to int }

func (r *countsRows) write(f *Fleet, tally fleetcount.Tally, room *int) assistantwire.CountsViewCount {
	r.f, r.rows, r.cards, r.last, r.total = f, r.rows[:0], r.cards[:0], -1, 0
	tally.Accept(r)
	count := r.count(room)
	r.f = nil
	return count
}

func (r *countsRows) VisitAgent(agent slot.Slot, why fleetcount.Why, _ instant.Instant) {
	r.row(r.seat(agent), -1, why)
}

func (r *countsRows) VisitAgentByCard(agent, card slot.Slot, why fleetcount.Why) {
	r.row(r.seat(agent), r.card(card), why)
}

func (r *countsRows) VisitCard(card slot.Slot, why fleetcount.Why) { r.row(-1, r.card(card), why) }

func (r *countsRows) VisitCardUnder(card slot.Slot) {
	if c := r.card(card); r.last >= 0 {
		r.cards = append(r.cards, c)
		r.rows[r.last].to++
	}
}

func (r *countsRows) VisitTotal(total fleetcount.RowCount) { r.total = total.Wire() }

// seat and card are a slot's place in f's lists; a place f does not hold is a tally of another fleet.
func (r *countsRows) seat(s slot.Slot) int32 {
	if int(s.Wire()) >= len(r.f.seats) {
		defect.Raise("fleetcountadapt.Fleet.View", "a tally of another fleet")
	}
	return s.Wire()
}

func (r *countsRows) card(s slot.Slot) int32 {
	if int(s.Wire()) >= len(r.f.ixs) {
		defect.Raise("fleetcountadapt.Fleet.View", "a tally of another fleet")
	}
	return s.Wire()
}

// row adds one row by its agent's and its first card's places (-1 for none), unless the wire has no word for why the
// count leaves it out.
func (r *countsRows) row(seat, card int32, why fleetcount.Why) {
	r.last = -1
	var held assistantwire.CountsHeldReason
	if !why.Counted() {
		why.Accept(&r.why)
		if held = assistantwire.CountsHeldReason(r.why.word); !held.Valid() {
			return // a quiet task's row: the view lists none (docs/protocol/counts.md, "Why a row is held")
		}
	}
	from := int32(len(r.cards))
	if card >= 0 {
		r.cards = append(r.cards, card)
	}
	r.rows = append(r.rows, countsRow{seat: seat, from: from, to: int32(len(r.cards)), held: held})
	r.last = len(r.rows) - 1
}

// count writes the gathered rows out: the counted ones as keys, the others as held, and the counted rows keys leaves
// out as more. The counted rows are listed first; each list stops at the first row past viewRowsMax or past the
// room the rows have. All the keys are written into one string.
func (r *countsRows) count(room *int) assistantwire.CountsViewCount {
	counted, listed, cards, keyBytes := r.choose(true, room)
	_, held, _, heldKeys := r.choose(false, room) // a held row lists no cards
	if counted != r.total {
		defect.Raise("fleetcountadapt.Fleet.View", "a tally whose total is not its counted rows")
	}
	keyBytes += heldKeys
	out := assistantwire.CountsViewCount{N: r.total, More: r.total - listed}
	if listed == 0 && held == 0 {
		return out
	}
	var b strings.Builder
	b.Grow(keyBytes)
	r.keys = r.keys[:0]
	for i := range r.rows {
		if row := &r.rows[i]; row.listed {
			from := b.Len()
			r.writeKey(&b, row)
			r.keys = append(r.keys, span{from, b.Len()})
		}
	}
	all := b.String()
	var ids []assistantwire.CountsCardID
	if cards > 0 {
		ids = make([]assistantwire.CountsCardID, 0, cards)
	}
	if listed > 0 {
		out.Keys = make([]assistantwire.CountsRow, 0, listed)
	}
	if held > 0 {
		out.Held = make([]assistantwire.CountsHeldRow, 0, held)
	}
	k := 0
	for i := range r.rows {
		row := &r.rows[i]
		if !row.listed {
			continue
		}
		id := assistantwire.CountsRowKey(all[r.keys[k].from:r.keys[k].to])
		k++
		if row.held != "" {
			out.Held = append(out.Held, assistantwire.CountsHeldRow{ID: id, Why: row.held})
			continue
		}
		key := assistantwire.CountsRow{ID: id}
		if row.to > row.from {
			first := len(ids)
			for _, c := range r.cards[row.from:row.to] {
				ids = append(ids, assistantwire.CountsCardID(r.f.ixs[c].ix.ID))
			}
			key.Cards = ids[first:len(ids):len(ids)]
		}
		out.Keys = append(out.Keys, key)
	}
	return out
}

// choose marks which of the counted rows (or of the held ones) the view lists: the first ones, in order, until the
// next would pass viewRowsMax or the room left, which it takes from room. It says how many rows of the kind there
// are, how many it lists, and how many cards and key bytes the listed ones hold.
func (r *countsRows) choose(counted bool, room *int) (rows, listed, cards, keyBytes int) {
	full := false
	for i := range r.rows {
		row := &r.rows[i]
		if (row.held == "") != counted {
			continue
		}
		rows++
		row.listed = false
		if full {
			continue
		}
		size := r.size(row)
		if listed == viewRowsMax || size > *room {
			full = true
			continue
		}
		*room -= size
		row.listed = true
		listed++
		cards += int(row.to - row.from)
		keyBytes += r.keyLen(row)
	}
	return rows, listed, cards, keyBytes
}

// size is at most what a row takes in the view's JSON, the comma after it included: a held row its key and reason,
// a counted one its key and cards.
func (r *countsRows) size(row *countsRow) int {
	n := len(`{"id":""},`) + r.keyLen(row) + r.escaped(row)
	if row.held != "" {
		return n + len(`,"why":""`) + len(row.held) // the reasons are plain words
	}
	if row.to > row.from {
		n += len(`,"cards":[]`)
		for _, c := range r.cards[row.from:row.to] {
			id := r.f.ixs[c].ix.ID
			n += len(`"",`) + len(id) + escapes(id)
		}
	}
	return n
}

// keyLen is how many bytes a row's key takes; escaped, how many more it may take as JSON.
func (r *countsRows) keyLen(row *countsRow) int {
	if row.seat < 0 {
		return len("card:") + len(r.f.ixs[r.cards[row.from]].ix.ID)
	}
	s := r.f.seats[row.seat].session
	return len("agent:/") + len(s.MachineID) + len(s.SessionID)
}

func (r *countsRows) escaped(row *countsRow) int {
	if row.seat < 0 {
		return escapes(r.f.ixs[r.cards[row.from]].ix.ID)
	}
	s := r.f.seats[row.seat].session
	return escapes(s.MachineID) + escapes(s.SessionID)
}

// writeKey writes an agent's row as agent:<machine>/<session>, or an individual card's row as card:<id>.
func (r *countsRows) writeKey(b *strings.Builder, row *countsRow) {
	if row.seat < 0 {
		b.WriteString("card:")
		b.WriteString(r.f.ixs[r.cards[row.from]].ix.ID)
		return
	}
	s := r.f.seats[row.seat].session
	b.WriteString("agent:")
	b.WriteString(s.MachineID)
	b.WriteByte('/')
	b.WriteString(s.SessionID)
}

// escapes is at most how many bytes more encoding/json takes to write s than s has: a quote, a backslash or a line
// break one more, any other control byte, <, > or & five more (\u0001, \u003c), U+2028 and U+2029 three more,
// and a byte that is not UTF-8 five more, the most any Go release writes for it (\ufffd).
func escapes(s string) int {
	n := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			switch {
			case c == '"' || c == '\\' || c == '\n' || c == '\r' || c == '\t':
				n++
			case c < 0x20 || c == '<' || c == '>' || c == '&':
				n += 5
			}
			i++
			continue
		}
		rn, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case rn == utf8.RuneError && size == 1:
			n += 5
		case rn == '\u2028' || rn == '\u2029':
			n += 3
		}
		i += size
	}
	return n
}
