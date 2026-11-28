package assistant

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/fleetcount"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/domainkit/opt"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The counts view (docs/protocol/counts.md, step 7a of docs/plans/plan-derived-counts.md): the counts people see, as
// this manager works them out, told the hub (counts.view) for the clients to draw instead of counting themselves.
// liveLoop makes it in each round from the agents and cards the Live Activity's state and Persona Link's summary are
// made from, after the state is told, so the three agree and a badge is never ahead of the lists the round published.
// fleetcount decides which Agents rows and Persona cards count. fleetcountadapt writes the view's shape, row keys
// and held reasons. This file adds the view's place in this run: its epoch, rev, as_of and
// build, and when it is told. The view's to-dos count is the to-do list's own (countsTodos): the counts
// GET /api/v1/todos answers, kept from the last whole-list read.

// linkAndCounts is linkAttention and the counts view of the same agents and cards, decided on one fill of the fleet:
// liveLoop's round. The view carries the Agents badge and Persona cards (which also take quiet agents), and todos
// as the manager last worked them out (countsTodos.kept). tellCounts adds the epoch, rev, as_of and build.
func (r *fleetRound) linkAndCounts(l i18n.Lang, agents []fleetInput, ixs []proto.Interaction, told map[string]bool, quiet []fleetInput, todos opt.Option[assistantwire.TodoCounts]) (link.Attention, assistantwire.CountsView) {
	r.fillLink(agents, ixs, told, quiet)
	defer r.letLinkGo()
	needsYou := fleetcount.AgentsNeedsYou(fleetcount.SurfaceBadge(), r.link.Agents(), r.link.Cards())
	return r.link.Link(string(l), sessionTitle), r.link.View(needsYou, todos)
}

// countsTeller is what liveLoop keeps of the counts view between rounds: this run's epoch, the rev of the last view
// made, its counts and totals as JSON (to tell a new view from a repeat) and the view as told. A view goes up a rev
// only when its counts or totals change; one the hub was not given (it was away, or it connected again since) is told
// again as it was.
type countsTeller struct {
	epoch assistantwire.CountsEpoch
	rev   int
	last  []byte       // the last view's counts and totals
	next  bytes.Buffer // this round's
	enc   *json.Encoder
	part  countsPart
	view  []byte
	todos countsTodos // the to-do list's counts; the list reads keep them, under a lock of their own
}

// countsPart is the part of a view that says what it counts.
type countsPart struct {
	Counts assistantwire.CountsViewCounts  `json:"counts"`
	Totals *assistantwire.CountsViewTotals `json:"totals,omitempty"`
}

// tellCounts tells the hub this round's counts view: a new rev when its counts changed, as_of now, and the view the
// hub was last told again when it was not given it yet (liveChanged(true) after a handshake). A round whose counts did
// not change tells nothing.
func (s *Service) tellCounts(v assistantwire.CountsView) {
	l := &s.fleet.live
	t := &l.counts
	if t.enc == nil {
		t.epoch, t.enc = assistantwire.CountsEpoch(rand.Text()), json.NewEncoder(&t.next)
	}
	if t.todos.kept().IsNone() {
		s.todosCountSoon() // this run has not counted its to-dos yet, or could not: no change may push them for a while
	}
	t.next.Reset()
	t.part = countsPart{Counts: v.Counts, Totals: v.Totals}
	err := t.enc.Encode(&t.part)
	t.part = countsPart{}
	if err != nil {
		s.logf("counts: the view does not encode: %v", err)
		return
	}
	if t.view == nil || !bytes.Equal(t.next.Bytes(), t.last) {
		t.rev++
		v.Epoch, v.Rev, v.AsOf, v.Build = t.epoch, t.rev, time.Now().UnixMilli(), s.build
		b, err := json.Marshal(v)
		if err != nil {
			s.logf("counts: the view does not encode: %v", err)
			return
		}
		t.view, t.last = b, append(t.last[:0], t.next.Bytes()...)
		l.mu.Lock()
		l.countsTold = false
		l.mu.Unlock()
	}
	l.mu.Lock()
	told := l.countsTold
	l.mu.Unlock()
	if told {
		return
	}
	if s.hub.Tell(assistantwire.EventCountsView, rpc.Marshaled(t.view)) == nil { // json.Marshal's bytes, as they are
		l.mu.Lock()
		l.countsTold = true
		l.mu.Unlock()
	}
}

// countsTodos is the to-do list's counts the view carries as todos_needs_you (docs/protocol/counts.md): the Counts() of
// the whole list's projection, the call GET /api/v1/todos answers its counts with (todoProjection.counts), as the
// manager last worked it out. Every whole-list read keeps its counts (todosCounted). Every {"type":"todos"} has each
// app read the list again; once todosCountSettle passed after one that no such read followed, the manager reads the
// list itself (todosCountSoon), so the number follows the list while no app is open too. None until the to-dos are
// first counted: the view then leaves the count out, which reads as unknown, never 0, and each round asks for a count
// until one is kept.
type countsTodos struct {
	mu      sync.Mutex
	counts  opt.Option[assistantwire.TodoCounts]
	gen     uint64 // the {"type":"todos"} gone out when the kept counts' to-dos were read (todoViewGen)
	failing bool   // the last recount could not read the list: its failure is logged once, until a count is kept

	armed bool                            // a recount waits for todosCountSettle or reads the list: one at a time
	timer *time.Timer                     // its real-clock timer, while it waits
	runs  sync.WaitGroup                  // the recount scheduled or running: the tests' drain waits for it
	after func(d time.Duration, f func()) // tests: a fake clock's timers
}

// todosCountSettle is how long the manager waits after a {"type":"todos"} for an app's read of the list, which counts
// it, before it reads the list itself.
const todosCountSettle = time.Second

// kept is the to-do list's counts the view carries: the last kept, or none before the to-dos were first counted.
func (t *countsTodos) kept() opt.Option[assistantwire.TodoCounts] {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counts
}

// todosCounted keeps the counts of p, the whole list's projection of to-dos read while gen {"type":"todos"} had gone
// out (taken before they were read, todoViewGen), for the counts view, and has liveLoop make the view again (one whose
// counts did not change tells nothing). Counts of to-dos read before the kept ones' last push are older and change
// nothing.
func (s *Service) todosCounted(gen uint64, p todoProjection) {
	counts := p.counts()
	t := &s.fleet.live.counts.todos
	t.mu.Lock()
	older := gen < t.gen
	if !older {
		t.counts, t.gen, t.failing = opt.Some(counts), gen, false
	}
	t.mu.Unlock()
	if !older {
		s.liveChanged(false)
	}
}

// todosCountSoon has the manager count the to-dos itself once todosCountSettle passed (todosRecount), unless a recount
// waits or reads already. Only a running manager that carries the fleet counts them: one without it tells no view, and
// before Run (a test on the real clock) nothing would stop the timer.
func (s *Service) todosCountSoon() {
	if !s.fleetOn() {
		return
	}
	t := &s.fleet.live.counts.todos
	t.mu.Lock()
	if t.armed || s.runCtx().Err() != nil || s.running.Load() == nil && t.after == nil {
		t.mu.Unlock()
		return
	}
	t.armed = true
	if after := t.after; after != nil {
		t.mu.Unlock()
		after(todosCountSettle, s.todosRecount)
		return
	}
	t.runs.Add(1)
	t.timer = time.AfterFunc(todosCountSettle, func() {
		defer t.runs.Done()
		s.todosRecount()
	})
	t.mu.Unlock()
}

// todosRecount reads the whole to-do list as GET /api/v1/todos does (todosViewed), which keeps its counts, unless a
// read of the to-dos since the last {"type":"todos"} counted them already or the manager stopped. It stays armed until
// its read ends: on a slow store that read takes up to its 30 s, and a round or a {"type":"todos"} meanwhile starts no
// second one. A {"type":"todos"} that went out while it waited or read found it armed, so it arms the next recount
// itself once it is done; a round that asked meanwhile asks again the next round.
func (s *Service) todosRecount() {
	gen := s.todoViewGen()
	t := &s.fleet.live.counts.todos
	t.mu.Lock()
	t.timer = nil
	counted := !t.counts.IsNone() && t.gen >= gen
	t.mu.Unlock()
	if !counted && s.runCtx().Err() == nil {
		s.todosRead()
	}
	t.mu.Lock()
	t.armed = false
	t.mu.Unlock()
	if s.todoViewGen() > gen { // read once armed is let go: a push from here on arms its own recount
		s.todosCountSoon()
	}
}

// todosRead is a recount's read of the whole list; its failure is logged once, until a count is kept.
func (s *Service) todosRead() {
	ctx, cancel := context.WithTimeout(s.runCtx(), 30*time.Second)
	defer cancel()
	_, _, err := s.todosViewed(ctx, false)
	if err == nil {
		return
	}
	t := &s.fleet.live.counts.todos
	t.mu.Lock()
	logged := t.failing
	t.failing = true
	t.mu.Unlock()
	if !logged {
		s.logf("counts: the to-dos could not be counted: %v", err)
	}
}

// todosCountStop stops a recount's timer once Run's context ended (the tests' drain); a recount already running
// finishes, and one that fires later finds the context ended.
func (s *Service) todosCountStop() {
	t := &s.fleet.live.counts.todos
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.timer != nil && t.timer.Stop() {
		t.runs.Done()
		t.armed = false
	}
	t.timer = nil
}
