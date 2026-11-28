package assistant

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/todo/projection"
	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The To-dos view stays fresh (#10226; docs/assistant/todos.md "Live WebSocket pushes"). A client files each row by
// its view, and what files a row can change with no write to its to-do: one of its runs starts asking, stops or
// finishes, a turn is seen, or a window of the view ends (Just finished's 24 hours, the 48 hours to the Inactive
// mark). The manager then sends the settled {"type":"todos"} a write sends (todosPushSoon), so every client reads the
// list again; an old client, which works the rows out itself, just reads more often when it matters. The manager
// names todo_view in every hello (bus.go), so this always runs.
//
// What is compared is a row's classification (todoClass), never its line's arguments, since, next_change_at or rank:
// a working run's latest line and activity change on every step, and every client reading the whole list on each was
// the storm #5995 ended. The list's bytes still follow them whenever a client reads. seen is compared, so the first
// step after the owner looked at a working row, which clears its seen mark, pushes once.
//
// told is each to-do's classification as first worked out after the last {"type":"todos"} went out: what every client
// reads from then on until it changes. Three things work views out and hold them against it: every list read (the
// whole list), a change of the runs on a computer (todoRunsRecheck: the to-dos with a run there) and the clock
// (todoViewWake: the to-dos whose next_change_at passed). The last two read only the rows they are about and what
// those rows' views read (todosAroundLocked, todoSuperseded), never a copy of every to-do. A view that reads
// differently sends the push: at once from the recheck and the clock, which waited already; from a list read after
// todoViewSettle, as the client that read has the row and changes coming one after another then go out as one. The
// push clears told, and the first views worked out after it fill it again; views from to-dos read before it (an older
// gen) change nothing. What the apps were told is kept in memory only, so the first views worked out after Run starts
// are a change too: a run may have come to ask while the manager was away.

// todoViewSettle is how long a change of the runs, or a difference a list read found, waits for the rest before the
// views it touches are worked out or the apps told: the lists of several computers, and the card that comes with a
// run's question, land within it, and one push goes out.
const todoViewSettle = time.Second

// The clock's check of the views never runs more often than todoViewWakeEvery, nor sleeps longer than todoViewWakeCap:
// a far next_change_at (a Someday a year ahead) or a computer that slept never leaves it behind.
const (
	todoViewWakeEvery = time.Minute
	todoViewWakeCap   = time.Hour
)

// todoClass is what a client files a to-do's row under: its phase, section and reason, its asks by id and the card
// each is answered by (interaction: a card that lands after the run came to ask changes how its row answers it), its
// marks (Inactive, Can wait, seen) and its steps' count. Comparable: two rows filed alike are ==.
type todoClass struct {
	phase, section, reason string
	asks                   string // the ids, each with its card's, in order, one per line
	inactive               int64  // the Inactive mark's time in Unix nanoseconds; 0 without one
	canWait, seen          bool
	steps                  assistantwire.TodoStepCount
}

// todoClassOf is the classification of view v.
func todoClassOf(v assistantwire.TodoSummary) todoClass {
	c := todoClass{phase: v.Phase, section: v.Section, reason: v.Reason, canWait: v.CanWait, seen: v.Seen}
	if !v.Inactive.IsZero() {
		c.inactive = v.Inactive.UnixNano()
	}
	if v.Steps != nil {
		c.steps = *v.Steps
	}
	if len(v.Asks) > 0 {
		ids := make([]string, len(v.Asks))
		for i, a := range v.Asks {
			ids[i] = a.ID + " " + a.Interaction
		}
		c.asks = strings.Join(ids, "\n")
	}
	return c
}

// todoViewBook is what the apps were told of the views and when the views are looked at next.
type todoViewBook struct {
	mu     sync.Mutex
	gen    uint64               // the {"type":"todos"} gone out so far
	told   map[string]todoClass // by to-do id, since the last of them
	listed map[string]todoClass // the latest stable classes, kept across pushes for todoRevisionCurrent
	next   map[string]time.Time // each to-do's next_change_at, as last worked out

	machines   map[string]bool // the computers whose runs changed, waiting for todoViewSettle
	differs    bool            // a list read found a row that reads differently, waiting for todoViewSettle
	armed      bool            // todoViewSettled waits for either
	settle     *time.Timer     // its real-clock timer
	rechecking bool            // a todoRunsRecheck runs: one at a time
	again      bool            // todoViewSettle passed while it ran: it checks the machines once more

	wakeAt  time.Time   // when the clock's check is due; zero: none waits
	wakeSeq uint64      // the waiting check's: one replaced does nothing
	wake    *time.Timer // its real-clock timer
	woke    time.Time   // when the clock's check last worked the views out

	runs  sync.WaitGroup                  // real-clock callbacks scheduled or running: fixtures wait before closing
	now   func() time.Time                // tests: a fake clock
	after func(d time.Duration, f func()) // tests: a fake clock's timers
}

// clock is the views' now.
func (b *todoViewBook) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// scheduleLocked runs f after d: on the tests' clock, else on a real timer, which it answers (b.mu held).
func (b *todoViewBook) scheduleLocked(d time.Duration, f func()) *time.Timer {
	if b.after != nil {
		b.after(d, f)
		return nil
	}
	b.runs.Add(1)
	return time.AfterFunc(d, func() {
		defer b.runs.Done()
		f()
	})
}

// stopLocked stops both real-clock timers; a callback already running finishes (b.mu held).
func (b *todoViewBook) stopLocked() {
	if b.settle != nil && b.settle.Stop() {
		b.runs.Done()
		b.armed = false
	}
	b.settle = nil
	b.stopWakeLocked()
}

// stopWakeLocked drops the clock's waiting check (b.mu held).
func (b *todoViewBook) stopWakeLocked() {
	if b.wake != nil && b.wake.Stop() {
		b.runs.Done()
	}
	b.wake, b.wakeAt = nil, time.Time{}
	b.wakeSeq++
}

// todoViewsStop stops the views' timers once Run's context ended (and in the tests' drain); a callback already running
// finishes, and one that fires later finds the context ended.
func (s *Service) todoViewsStop() {
	b := &s.tview
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopLocked()
}

// todoViewNow is the views' now: the list reads and the checks below work them out at it.
func (s *Service) todoViewNow() time.Time { return s.tview.clock() }

// todoViewGen is the number of {"type":"todos"} gone out so far: taken before the to-dos are read, it says whether
// views worked out from them are still held against what the apps were told.
func (s *Service) todoViewGen() uint64 {
	b := &s.tview
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.gen
}

// todoViewPushed: a {"type":"todos"} goes out, so every app reads the list again; what each row is filed under is
// told again by the first views worked out from here, and a difference a list read found is told by this push.
func (s *Service) todoViewPushed() {
	b := &s.tview
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gen++
	b.told, b.differs = nil, false
	s.todoViewArmLocked()
}

// todoViewsSeen holds the views of p, worked out from to-dos read while gen pushes had gone out, against what the
// apps were told, and says whether one reads differently: the caller tells them again. ids are the to-dos p was worked
// out for, whose next_change_at replace those kept (one p no longer has is dropped); nil: p is every to-do, and its
// replace them all. Views from an older gen change nothing: the push since makes every app read the list, and that
// read's views count.
func (s *Service) todoViewsSeen(gen uint64, p todoProjection, ids map[string]bool) bool {
	b := &s.tview
	b.mu.Lock()
	defer b.mu.Unlock()
	if gen != b.gen {
		s.todoViewArmLocked()
		return false
	}
	// What the apps were told before the manager started is not known: the first views after Run started tell them.
	changed := gen == 0 && b.told == nil && s.running.Load() != nil
	if b.told == nil {
		b.told = make(map[string]todoClass, len(p.facts))
	}
	if ids == nil {
		b.listed = make(map[string]todoClass, len(p.facts))
	} else {
		if b.listed == nil {
			b.listed = make(map[string]todoClass, len(p.facts))
		}
		for id := range ids {
			delete(b.listed, id)
		}
	}
	for i := range p.facts {
		c := todoClassOf(p.views[i])
		if was, ok := b.told[p.facts[i].ID]; ok && was != c {
			changed = true
		}
		b.told[p.facts[i].ID] = c
		b.listed[p.facts[i].ID] = c
	}
	if ids == nil || b.next == nil {
		b.next = make(map[string]time.Time, len(p.facts))
	}
	for id := range ids {
		delete(b.next, id)
	}
	for i := range p.facts {
		if at := p.views[i].NextChangeAt; !at.IsZero() {
			b.next[p.facts[i].ID] = at
		}
	}
	s.todoViewArmLocked()
	return changed
}

// todoViewDiffers: a list read found a row that reads differently. The apps are told once todoViewSettle passed
// (todoViewSettled), with whatever else changed by then; a push before that tells them, and this one is dropped.
func (s *Service) todoViewDiffers() {
	b := &s.tview
	b.mu.Lock()
	defer b.mu.Unlock()
	b.differs = true
	s.todoViewSettleLocked()
}

// todoViewSettleLocked has todoViewSettled run once todoViewSettle passed, unless it waits already or the service
// stopped (b.mu held).
func (s *Service) todoViewSettleLocked() {
	b := &s.tview
	if b.armed || s.runCtx().Err() != nil {
		return
	}
	b.armed = true
	b.settle = b.scheduleLocked(todoViewSettle, s.todoViewSettled)
}

// todoViewSettled: todoViewSettle passed. A difference a list read found is told, and the views of the to-dos with a
// run on the computers that changed are worked out again (todoRunsRecheck).
func (s *Service) todoViewSettled() {
	b := &s.tview
	b.mu.Lock()
	differs := b.differs
	b.armed, b.settle, b.differs = false, nil, false
	b.mu.Unlock()
	if s.runCtx().Err() != nil {
		return
	}
	if differs {
		s.todosPushSoon()
	}
	s.todoRunsRecheck()
}

// ---- the runs ----

// todoRunsChanged: what the apps get of machine's sessions may have changed (its node's list, a card, a turn seen).
// When an open to-do has a run there, the views of those with one are worked out again once todoViewSettle passed
// (todoRunsRecheck).
func (s *Service) todoRunsChanged(machine string) {
	if s.runCtx().Err() != nil || !s.todoRunsOn(machine) { // before b.mu: the to-dos' lock is never taken under it
		return
	}
	b := &s.tview
	b.mu.Lock()
	if b.machines == nil {
		b.machines = map[string]bool{}
	}
	b.machines[machine] = true
	s.todoViewSettleLocked()
	b.mu.Unlock()
	s.directionTreeSoon()
}

// todoRunsOn: an open to-do has an active run on machine (activeBySession, as the direction tree reads it).
func (s *Service) todoRunsOn(machine string) bool {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for k := range s.todos.activeBySession {
		if k[0] == machine {
			return true
		}
	}
	return false
}

// todoRunsRecheck works out again the views of the to-dos with a run on the computers that changed (todosOnRuns),
// each run's session read as the list reads it, and tells the apps when one reads differently. One runs at a time: a
// settle that passes meanwhile has it check the computers that changed since once more when it is done, so an older
// check never lands after a newer one.
func (s *Service) todoRunsRecheck() {
	b := &s.tview
	b.mu.Lock()
	if b.rechecking {
		b.again = true
		b.mu.Unlock()
		return
	}
	b.rechecking = true
	for {
		machines, gen := b.machines, b.gen
		b.machines, b.again = nil, false
		b.mu.Unlock()
		if len(machines) > 0 && s.runCtx().Err() == nil && s.todosLoaded() {
			if ids := s.todosOnRuns(machines); len(ids) > 0 {
				_ = s.todoViewsCheck(gen, ids, b.clock()) // unread, they wait for the next change or list read
			}
		}
		b.mu.Lock()
		if !b.again {
			break
		}
	}
	b.rechecking = false
	b.mu.Unlock()
}

// todosOnRuns are the to-dos whose views a change of the runs on machines can change: the open ones with an active
// run there (activeBySession; a failed start's runs never ran, and it holds none), with what their views read
// (todosAroundLocked). The to-dos are let go before any session is read.
func (s *Service) todosOnRuns(machines map[string]bool) map[string]bool {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	heads := map[string]bool{}
	for k, id := range s.todos.activeBySession {
		if machines[k[0]] {
			heads[id] = true
		}
	}
	return s.todosAroundLocked(heads)
}

// todosAroundLocked are heads with the to-dos their views read: the to-do each is a step of, and every step of those
// and of heads (a parent's view rolls its steps up; steps are one level deep, todo_pieces.go). nil without heads
// (s.todos.mu held).
func (s *Service) todosAroundLocked(heads map[string]bool) map[string]bool {
	if len(heads) == 0 {
		return nil
	}
	ids := make(map[string]bool, len(heads))
	for id := range heads {
		ids[id] = true
		if t := s.todos.todos[id]; t != nil && t.PartOf != "" && t.PartOf != id && s.todos.todos[t.PartOf] != nil {
			ids[t.PartOf] = true
		}
	}
	var steps []string
	for id, t := range s.todos.todos {
		if t.PartOf != "" && t.PartOf != id && ids[t.PartOf] {
			steps = append(steps, id)
		}
	}
	for _, id := range steps {
		ids[id] = true
	}
	return ids
}

// todoViewsCheck works out the views of ids (todoViewsOf) at now from to-dos read while gen pushes had gone out, and
// tells the apps when one reads differently. A to-do of ids that is gone has its next_change_at dropped.
func (s *Service) todoViewsCheck(gen uint64, ids map[string]bool, now time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := s.todoViewsOf(ctx, ids, now)
	if err != nil {
		return err
	}
	if s.todoViewsSeen(gen, p, ids) {
		s.todosPushSoon()
	}
	return nil
}

// todoViewsOf are the views of the to-dos ids at now as a list read works them out; ids hold every to-do their views
// read (todosAroundLocked). Only their rows and their runs' sessions are read, and a failed start among them reads as
// superseded when the whole list has it so (todoSuperseded).
func (s *Service) todoViewsOf(ctx context.Context, ids map[string]bool, now time.Time) (todoProjection, error) {
	list, err := s.listedTodosOf(ctx, false, ids)
	if err != nil {
		return todoProjection{}, err
	}
	var superseded map[string]bool
	if slices.ContainsFunc(list, func(row listedTodo) bool { return row.StartError != "" }) {
		superseded = s.todoSuperseded()
	}
	return s.projectTodoRows(ctx, list, now, superseded), nil
}

// todoSuperseded are the failed starts whose work finished in a related to-do anywhere in the list
// (projection.Superseded), read from each to-do's title, PR, plan, kind and close alone: a related to-do can be any
// of them, and this is what makes a part of the list read them as the whole does without copying every to-do.
func (s *Service) todoSuperseded() map[string]bool {
	s.todos.mu.RLock()
	keys := make([]projection.Facts, 0, len(s.todos.todos))
	for _, t := range s.todos.todos {
		f := projection.Facts{ID: t.ID, Title: t.Title, PRURL: t.PRURL, PlanID: t.PlanID, StartError: t.StartError,
			Kind: t.Kind}
		if t.DoneAt != nil {
			done := *t.DoneAt
			f.DoneAt = &done
		}
		keys = append(keys, f)
	}
	s.todos.mu.RUnlock()
	return projection.Superseded(keys)
}

// ---- the clock ----

// todoViewArmLocked sets the clock's check at the earliest next_change_at kept, no sooner than todoViewWakeEvery
// after it last worked the views out and no later than todoViewWakeCap from now; a check already due sooner stays
// (it finds nothing due and sets the next). Called after every push, list read and check (b.mu held). Like Run's
// loops it runs only while the service does: before Run (a test that never starts it, on the real clock) nothing
// would ever stop the checks, each setting the next.
func (s *Service) todoViewArmLocked() {
	b := &s.tview
	due := b.earliestLocked()
	if due.IsZero() || s.runCtx().Err() != nil || s.running.Load() == nil && b.after == nil {
		b.stopWakeLocked()
		return
	}
	now := b.clock()
	at := due
	if !b.woke.IsZero() && at.Before(b.woke.Add(todoViewWakeEvery)) {
		at = b.woke.Add(todoViewWakeEvery)
	}
	if limit := now.Add(todoViewWakeCap); at.After(limit) {
		at = limit
	}
	if !b.wakeAt.IsZero() && !at.Before(b.wakeAt) {
		return
	}
	b.stopWakeLocked()
	seq := b.wakeSeq
	b.wakeAt = at
	b.wake = b.scheduleLocked(max(at.Sub(now), 0), func() { s.todoViewWake(seq) })
}

// earliestLocked is the earliest next_change_at kept; zero when none is (b.mu held).
func (b *todoViewBook) earliestLocked() time.Time {
	var due time.Time
	for _, at := range b.next {
		if due.IsZero() || at.Before(due) {
			due = at
		}
	}
	return due
}

// dueLocked are the to-dos whose next_change_at kept is not after now (b.mu held).
func (b *todoViewBook) dueLocked(now time.Time) map[string]bool {
	var due map[string]bool
	for id, at := range b.next {
		if !now.Before(at) {
			if due == nil {
				due = map[string]bool{}
			}
			due[id] = true
		}
	}
	return due
}

// todoViewWake is the clock's check seq: the views of the to-dos whose next_change_at passed are worked out again with
// what they read (todosAroundLocked) and held against what the apps were told; with none due it only sets the next
// check. A check that cannot read the to-dos tries again a minute later (todoViewWakeEvery).
func (s *Service) todoViewWake(seq uint64) {
	if s.runCtx().Err() != nil {
		return
	}
	loaded := s.todosLoaded() // before b.mu: the to-dos' lock is never taken under it
	b := &s.tview
	b.mu.Lock()
	if seq != b.wakeSeq {
		b.mu.Unlock()
		return
	}
	b.wake, b.wakeAt = nil, time.Time{}
	now := b.clock()
	due := b.dueLocked(now)
	if len(due) == 0 || !loaded {
		if len(due) > 0 {
			b.woke = now
		}
		s.todoViewArmLocked()
		b.mu.Unlock()
		return
	}
	b.woke = now
	gen := b.gen
	b.mu.Unlock()
	s.todos.mu.RLock()
	ids := s.todosAroundLocked(due) // the due ones too, so one that is gone is dropped
	s.todos.mu.RUnlock()
	if s.todoViewsCheck(gen, ids, now) != nil {
		b.mu.Lock()
		s.todoViewArmLocked()
		b.mu.Unlock()
	}
}
