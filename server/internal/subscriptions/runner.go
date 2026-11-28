package subscriptions

import (
	"context"
	"math/rand/v2"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
)

// The runner's limits (docs/extensions/subscriptions.md "Running").
const (
	tickEvery        = 30 * time.Second
	startJitter      = 60 * time.Second
	callGap          = 20 * time.Second // between two calls to one connector
	callJitter       = 5 * time.Second
	defaultPerHour   = 120
	maxRunning       = 4 // connectors running at once
	backoffMax       = 6 * time.Hour
	connFailsToPause = 3           // calls in a row that failed before the whole connector backs off
	connBackoffStart = time.Minute // doubling up to backoffMax
	shapeMissesMax   = 2           // runs in a row whose items cannot be read before the recipe needs redoing
)

// gate is one connector's limits, shared by all its subscriptions: one call at a time and at least callGap apart, an
// hourly budget, and a pause after rate_limited or repeated failures. It is kept in memory; a pause is also on each
// subscription (PausedUntil), from which a restarted manager takes it back.
type gate struct {
	calls  []time.Time // in the last hour
	last   time.Time
	paused time.Time
	fails  int
}

func (g *gate) prune(now time.Time) {
	g.calls = slices.DeleteFunc(g.calls, func(t time.Time) bool { return now.Sub(t) >= time.Hour })
}

func (s *Service) gateOf(id string) *gate {
	g := s.gates[id]
	if g == nil {
		g = &gate{}
		s.gates[id] = g
	}
	return g
}

// poke has the loop look for due work now.
func (s *Service) poke() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run schedules and runs subscriptions until ctx is done: it waits a random part of a minute first (a fleet of
// restarted managers does not call every source at once), then looks every 30 seconds and whenever asked.
func (s *Service) Run(ctx context.Context) {
	if s.o.Clock.Sleep(ctx, rand.N(startJitter)) != nil {
		return
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		if ctx.Err() != nil {
			return
		}
		s.startDueInto(ctx, &wg)
		t := time.NewTimer(tickEvery)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.wake:
		case <-t.C:
		}
		t.Stop()
	}
}

// RunDue starts what is due and waits for it (tests, and a manual pass).
func (s *Service) RunDue(ctx context.Context) {
	s.startDue(ctx).Wait()
}

// startDue starts one worker for each connector with due subscriptions, at most maxRunning at once, each running its
// connector's due subscriptions one by one.
func (s *Service) startDue(ctx context.Context) *sync.WaitGroup {
	var wg sync.WaitGroup
	s.startDueInto(ctx, &wg)
	return &wg
}

func (s *Service) startDueInto(ctx context.Context, wg *sync.WaitGroup) {
	all, err := s.o.Store.ListSubscriptions(ctx)
	if err != nil {
		s.o.Logf("subscriptions: list: %v", err)
		return
	}
	now := s.now()
	due := map[string][]Subscription{}
	s.mu.Lock()
	for _, sub := range all {
		if p := sub.PausedUntil; p != nil && p.After(now) && sub.RunState == StateRateLimited {
			if g := s.gateOf(sub.Connector); g.paused.Before(*p) {
				g.paused = *p // as it was before a restart
			}
		}
		if IsPush(sub) {
			continue // filled by its device, never pulled
		}
		asked := s.askedDue(sub.ID, now) || s.kickDue(sub.ID, now) && pullScheduled(sub)
		if !asked && (!pullScheduled(sub) || sub.NextRunAt == nil || sub.NextRunAt.After(now)) {
			continue
		}
		if sub.LeaseUntil != nil && sub.LeaseUntil.After(now) {
			continue
		}
		due[sub.Connector] = append(due[sub.Connector], sub)
	}
	ids := make([]string, 0, len(due))
	for id := range due {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if s.busy[id] {
			if s.retryBusy == nil {
				s.retryBusy = map[string]bool{}
			}
			s.retryBusy[id] = true
			continue
		}
		if s.running >= maxRunning {
			s.retryCap = true
			continue
		}
		subs := due[id]
		sort.SliceStable(subs, func(i, j int) bool {
			ai, aj := s.askedDue(subs[i].ID, now), s.askedDue(subs[j].ID, now)
			if ai != aj {
				return ai
			}
			return before(subs[i].NextRunAt, subs[j].NextRunAt)
		})
		delete(s.retryBusy, id)
		s.busy[id] = true
		s.running++
		wg.Add(1)
		go func() {
			defer wg.Done()
			progressed := false
			defer func() {
				s.mu.Lock()
				delete(s.busy, id)
				s.running--
				recheck := false
				if progressed {
					recheck = s.retryBusy[id] || s.retryCap
					s.retryCap = false
				}
				delete(s.retryBusy, id)
				s.mu.Unlock()
				if recheck && ctx.Err() == nil {
					s.poke()
				}
			}()
			for _, sub := range subs {
				if ctx.Err() != nil {
					return
				}
				if s.runOne(ctx, sub.ID) {
					progressed = true
				}
			}
		}()
	}
	s.mu.Unlock()
}

func before(a, b *time.Time) bool {
	switch {
	case a == nil:
		return false
	case b == nil:
		return true
	}
	return a.Before(*b)
}

// askedDue says whether a RunNow request for subscription id is due by now; under s.mu.
func (s *Service) askedDue(id string, now time.Time) bool {
	at, ok := s.asked[id]
	return ok && !now.Before(at)
}

// kickDue says whether a changed event has made subscription id due by now (ConnectorChanged); under s.mu.
func (s *Service) kickDue(id string, now time.Time) bool {
	at, ok := s.kicked[id]
	return ok && !now.Before(at)
}

// runOne runs a subscription once, if it is still due and no other run holds it.
// It returns false when storage or cancellation prevented making progress, so a storage outage
// does not trigger a tight re-wake loop.
func (s *Service) runOne(ctx context.Context, id string) bool {
	sub, err := s.o.Store.GetSubscription(ctx, id)
	if err != nil {
		return false
	}
	if sub == nil {
		return true
	}
	s.mu.Lock()
	g := s.gateOf(sub.Connector)
	now := s.now()
	asked := s.askedDue(id, now)
	kicked := s.kickDue(id, now) && pullScheduled(*sub) // still on: a changed event brought its run forward
	g.prune(now)
	perHour := defaultPerHour
	if sub.MaxCallsPerHour > 0 {
		perHour = sub.MaxCallsPerHour
	}
	paused, full := g.paused.After(now), len(g.calls) >= perHour
	wait := time.Duration(0)
	if !g.last.IsZero() {
		wait = g.last.Add(callGap + rand.N(callJitter)).Sub(now)
	}
	if _, switched := MigratedFrom(*sub); switched {
		// A built-in's feed now: its runs replace the built-in's Collect calls one for one (#6381), outside the
		// subscriptions' budget and gap; a pause the connector asked for still holds.
		full, wait = false, 0
	}
	if !asked && !kicked && (!pullScheduled(*sub) || sub.NextRunAt == nil || sub.NextRunAt.After(now)) {
		if !pullScheduled(*sub) {
			delete(s.kicked, id) // switched off since (a kick not due yet stays)
		}
		s.mu.Unlock()
		return true
	}
	if paused || full {
		var resume, pausedUntil time.Time
		if paused {
			pausedUntil = g.paused
			resume = pausedUntil
		}
		if full {
			budgetUntil := g.calls[0].Add(time.Hour)
			if budgetUntil.After(resume) {
				resume = budgetUntil
			}
		}
		if asked {
			if cur, ok := s.asked[id]; ok && !resume.Before(cur) {
				s.asked[id] = resume // keep RunNow pending until the connector's limits allow it
			}
		}
		delete(s.kicked, id) // held back by the connector's limits: the schedule runs it
		s.mu.Unlock()
		// Not a run: the connector's limits hold it back. A pause shows; the hourly budget only moves the run.
		next := *sub
		next.NextRunAt = &resume
		if paused {
			next.RunState, next.PausedUntil = StateRateLimited, &pausedUntil
		} else {
			next.PausedUntil = nil
			if next.RunState == StateRateLimited {
				if next.FailCount > 0 {
					next.RunState = StateFailing
				} else {
					next.RunState = StateOK
				}
				if next.Error != nil && next.Error.Code == CodeRateLimited {
					next.Error = nil
				}
			}
		}
		_, ok := s.commit(ctx, *sub, next, nil, nil, now, false)
		return ok
	}
	s.mu.Unlock()
	if wait > 0 && s.o.Clock.Sleep(ctx, wait) != nil {
		return false
	}
	now = s.now()
	ok, err := s.o.Store.LeaseSubscription(ctx, id, now, now.Add(leaseLength))
	if err != nil {
		return false
	}
	if !ok {
		return true
	}
	s.mu.Lock()
	if asked {
		delete(s.asked, id) // a scheduled run must not consume a RunNow made after the snapshot above
	} else if cur, ok := s.asked[id]; ok && !cur.IsZero() {
		delete(s.asked, id) // this run satisfies an earlier rate-limit-deferred RunNow
	}
	delete(s.kicked, id)
	s.mu.Unlock()
	return s.execute(ctx, *sub, asked, now)
}

// execute is one run of sub, holding its lease: check the connector and the tool, call it, keep what is new.
func (s *Service) execute(ctx context.Context, sub Subscription, asked bool, start time.Time) bool {
	next := sub
	next.PausedUntil = nil
	trigger := TriggerSchedule
	switch {
	case asked:
		trigger = TriggerNow
	case sub.FailCount > 0:
		trigger = TriggerRetry
	}
	if !s.readAllowed(ctx, sub) {
		// Provider capability is a per-subscription authorization boundary. Do not turn one unsupported source into a
		// global connector outage, and do not reach its catalog or body pull.
		next.RunState, next.Error = StateNotConnected, &Problem{CodeNotConnected, "The source is paused by private-chat visibility."}
		n := s.nextRun(next, start, false)
		next.NextRunAt = &n
		_, ok := s.commit(ctx, sub, next, nil, nil, start, false)
		return ok
	}
	c, ok := s.o.Connectors.Find(ctx, sub.Connector)
	if !ok || !c.ready() {
		// Not a failure and not a run: it resumes by itself once the connector can be called.
		next.RunState, next.Error = StateNotConnected, &Problem{CodeNotConnected, "The connector cannot be reached now."}
		n := s.nextRun(next, start, false)
		next.NextRunAt = &n
		_, ok := s.commit(ctx, sub, next, nil, nil, start, false)
		return ok
	}
	sources, err := s.catalog(ctx, c, catalogFresh)
	if err != nil {
		p := catalogProblem(err)
		if p.Code == CodeNotConnected {
			next.RunState, next.Error = StateNotConnected, p
			n := s.nextRun(next, start, false)
			next.NextRunAt = &n
			_, ok := s.commit(ctx, sub, next, nil, nil, start, false)
			return ok
		}
		return s.failed(ctx, sub, next, p, trigger, start, 0)
	}
	src, tool, refused := admit(sources, sub.Source, sub.Tool)
	if refused != nil {
		return s.needsRecipe(ctx, sub, next, refused, trigger, start)
	}
	next.SourceName, next.Icon, next.Service = src.Name, src.Icon, src.Service
	next.SourceMin, next.MaxCallsPerHour = intOr(src.MinIntervalSeconds), intOr(src.MaxCallsPerHour)
	if fp := fingerprint(tool); fp != sub.ToolFingerprint {
		// The tool changed: carry on only while the recipe's arguments still fit it.
		args, err := fillArgs(sub.Recipe.Args, vars(start, s.o.Location()))
		if err == nil {
			err = checkArgs(tool.InputSchema, args)
		}
		if err != nil {
			return s.needsRecipe(ctx, sub, next, &Problem{CodeToolChanged, "The tool changed and no longer takes the recipe's arguments."}, trigger, start)
		}
		s.o.Logf("subscription %s: its tool's description changed; the recipe still fits", sub.ID)
		next.ToolFingerprint = fp
	}
	since, until := start.Add(-firstBack), start
	if sub.SinceAt != nil {
		since = sub.SinceAt.Add(-overlap)
	}
	if current := sub.Continuation; current != nil {
		since, until = current.WindowSince, current.WindowEnd
	}
	s.mu.Lock()
	g := s.gateOf(sub.Connector)
	if _, switched := MigratedFrom(sub); !switched { // a built-in's feed counts no subscription call (#6381)
		g.last = start
		g.calls = append(g.calls, start)
	}
	s.mu.Unlock()
	pl := pull(ctx, withInboxThings(c.Conn), sub.Source, sub.Recipe, since, until, s.o.Location(), sub.Label, sub.Continuation, tool.InputSchema)
	ms := s.now().Sub(start).Milliseconds()
	if pl.problem != nil {
		// A page that fails is asked for again once (one transient error must not re-read every committed page); when
		// the run before failed too, its position is dropped, as an expired page token is refused every time (#6310).
		restart := sub.Continuation != nil && sub.FailCount > 0
		switch {
		case pl.problem.Code == CodeRateLimited:
			until := s.now().Add(pl.retry)
			s.mu.Lock()
			if g.paused.Before(until) {
				g.paused = until
			}
			s.mu.Unlock()
			next.RunState, next.Error, next.PausedUntil, next.NextRunAt = StateRateLimited, pl.problem, &until, &until
			_, ok := s.commit(ctx, sub, next, nil, &Run{At: start, MS: ms, Trigger: trigger, Error: pl.problem}, start, true)
			return ok
		case pl.connError:
			next.RunState, next.Error = StateNotConnected, pl.problem
			n := s.nextRun(next, start, false)
			next.NextRunAt = &n
			_, ok := s.commit(ctx, sub, next, nil, nil, start, false)
			return ok
		case pl.problem.Code == CodeNoList && restart:
			// Not counted as a shape miss: a refused page says nothing of the recipe; the fresh pull will.
			return s.failed(ctx, sub, s.restarted(next), pl.problem, trigger, start, ms)
		case pl.problem.Code == CodeNoList:
			return s.shapeMiss(ctx, sub, next, pl.problem, trigger, start, ms)
		case pl.problem.Code == CodeNotReadOnly || pl.problem.Code == CodePagination:
			return s.needsRecipe(ctx, sub, next, pl.problem, trigger, start)
		default:
			if restart {
				next = s.restarted(next)
			}
			s.connFailed(sub.Connector)
			return s.failed(ctx, sub, next, pl.problem, trigger, start, ms)
		}
	}
	s.mu.Lock()
	g.fails = 0
	s.mu.Unlock()
	if pl.noTitle {
		return s.shapeMiss(ctx, sub, next, &Problem{CodeShapeChanged, "The items no longer have a title where the recipe reads it."}, trigger, start, ms)
	}
	run := Run{At: start, MS: ms, Trigger: trigger}
	var seen []Seen
	var items []Item
	var read []string // held items this pull reads as seen now (#6176)
	// A recipe changed with existing baseline records every page as seen and keeps none of it. Its mark clears only
	// with the terminal page, so a restart cannot deliver an earlier page as new.
	baseline := sub.Baseline
	if pl.snapshot != nil {
		run.Count = 1
		if pl.snapHash != sub.SnapshotHash {
			it := *pl.snapshot
			it.SubscriptionID = sub.ID
			// SnapshotHash is a snapshot's dedup, and the lease already serializes pulls: a seen key would make the
			// commit's claim drop a snapshot that returns to an earlier value (#13122).
			items = append(items, it)
			next.SnapshotHash = pl.snapHash
		}
	} else {
		run.Count = len(pl.items)
		known, err := s.o.Store.SeenKeys(ctx, sub.ID, uids(pl.items))
		if err != nil {
			s.o.Logf("subscription %s: seen keys: %v", sub.ID, err)
			return s.failed(ctx, sub, next, &Problem{CodeCallFailed, "The subscription's seen items could not be read."}, trigger, start, ms)
		}
		newest := sub.SinceAt
		sawItems, sawTimed := false, false
		if current := sub.Continuation; current != nil {
			newest, sawItems, sawTimed = current.NewestAt, current.SawItems, current.SawTimed
		}
		for _, m := range newOnes(pl.items, known) {
			it := m.item
			it.SubscriptionID = sub.ID
			items, seen = append(items, it), append(seen, Seen{UID: it.UID, ContentHash: m.hash})
		}
		for _, m := range pl.items {
			if m.item.Seen && known[m.item.UID] && !slices.Contains(read, m.item.UID) {
				read = append(read, m.item.UID) // read in the source since it was stored: the held item is seen too
			}
		}
		for _, m := range pl.items {
			sawItems = true
			if t := m.item.Time; t != nil {
				sawTimed = true
				if (newest == nil || t.After(*newest)) && !t.After(until) {
					tt := *t
					newest = &tt
				}
			}
		}
		if pl.continuation != nil {
			pl.continuation.Manual = asked && !on(sub) || sub.Continuation != nil && sub.Continuation.Manual
			pl.continuation.NewestAt, pl.continuation.SawItems, pl.continuation.SawTimed = newest, sawItems, sawTimed
			next.Continuation = pl.continuation
		} else {
			if newest == nil || sawItems && !sawTimed {
				newest = &until // items without times: the next run looks back from the original window's end
			}
			next.SinceAt, next.Continuation = newest, nil
		}
		next.WeakDedup = pl.weak || sub.Continuation != nil && sub.WeakDedup
		if sub.Recipe.Mode == wire.ConnectorSubscriptionModeThings {
			next.Answer, next.AnswerAt = answerOf(pl.items), &start // the Inbox gets these rows as they are (#13637)
		}
	}
	var baselineCommit *time.Time
	if baseline != nil {
		run.Baseline, items = len(items), nil
		next.Answer, next.AnswerAt = nil, nil // its new rows are not kept: each thread's newest stored, as before
		if pl.continuation == nil {
			next.Baseline, baselineCommit = nil, baseline
		}
	}
	run.New = len(items)
	next.Shape, next.ShapeMisses = pl.shape, 0
	next.RunState, next.Error, next.FailCount, next.FailingSince = StateOK, nil, 0, nil
	next.LastOKAt, next.LastNewCount = &start, len(items)
	// Only the terminal page ends the Inbox's catch-up: it must include every page of the original window.
	caughtUp := false
	if pl.continuation == nil {
		if from := inboxFrom(next); from != nil && (next.InboxCatchup == nil || next.InboxCatchup.Before(*from)) {
			next.InboxCatchup, caughtUp = &start, true
		}
	}
	next = markRows(next, caughtUp, start)
	if pl.continuation != nil {
		next.NextRunAt = &start // continuation is due now; connector gaps, budgets and backoff still apply
	} else {
		n := s.nextRun(next, start, false)
		next.NextRunAt = &n
	}
	committed, ok := s.commit(ctx, sub, next, &commitData{seen: seen, items: items, read: read, baseline: baselineCommit}, &run, start, true)
	if !ok {
		return false
	}
	next, items = committed.Sub, committed.Items
	if len(next.Runs) > 0 {
		run = next.Runs[len(next.Runs)-1]
	}
	if pl.continuation != nil {
		s.poke()
	}
	if len(items) > 0 {
		s.o.Logf("subscription %s: %d new of %d", sub.ID, len(items), run.Count)
	}
	if baseline != nil {
		s.o.Logf("subscription %s: baseline after a recipe change: %d of %d recorded as seen", sub.ID, run.Baseline, run.Count)
	}
	if len(items) > 0 || caughtUp {
		s.o.Delivered(next)
	}
	s.prune(ctx, next, start)
	return true
}

// Held items are kept past their destinations: the Inbox's window and a day more, and while the knowledge base takes
// them, until kbKeep (a knowledge base that stopped reading for longer misses them).
const (
	inboxKeep = 8 * 24 * time.Hour
	kbKeep    = 30 * 24 * time.Hour
)

// prune drops sub's held items its destinations are past, but for its things recipe's latest answer's rows, which the
// Inbox gets while they are in it however old they are (#13637).
func (s *Service) prune(ctx context.Context, sub Subscription, now time.Time) {
	keep := inboxKeep
	if sub.ToKB {
		keep = kbKeep
	}
	if err := s.o.Store.PruneItems(ctx, sub.ID, now.Add(-keep), sub.Answer); err != nil {
		s.o.Logf("subscription %s: prune its items: %v", sub.ID, err)
	}
}

// connFailed counts a failed call against the connector: after connFailsToPause in a row, all its subscriptions wait,
// a minute first and doubling up to backoffMax.
func (s *Service) connFailed(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.gateOf(id)
	g.fails++
	if g.fails >= connFailsToPause {
		d := min(connBackoffStart<<min(g.fails-connFailsToPause, 20), backoffMax)
		g.paused = s.now().Add(d)
		s.o.Logf("subscriptions: connector %s failed %d calls in a row: paused %s", id, g.fails, d)
	}
}

// restarted is next without its continuation, whose page failed twice in a row: the next run pulls afresh from the
// original window's start, and the dedup keys of the pages kept so far hold back what they delivered (#6310).
func (s *Service) restarted(next Subscription) Subscription {
	c := next.Continuation
	s.o.Logf("subscription %s: page %d of a paginated pull failed twice in a row; pulling again from the start", next.ID, c.Pages+1)
	if next.SinceAt == nil {
		since := c.WindowSince.Add(overlap) // a first pull: the fresh one still reaches back to where it began
		next.SinceAt = &since
	}
	next.Continuation = nil
	return next
}

// failed records a failed run: retried after interval × 2^failures, at most backoffMax, never stopped.
func (s *Service) failed(ctx context.Context, sub, next Subscription, p *Problem, trigger string, start time.Time, ms int64) bool {
	next.RunState, next.Error = StateFailing, p
	next.FailCount = sub.FailCount + 1
	if sub.FailingSince == nil {
		next.FailingSince = &start
	}
	n := start.Add(backoff(time.Duration(max(sub.IntervalSeconds, minInterval))*time.Second, next.FailCount))
	next.NextRunAt = &n
	s.o.Logf("subscription %s: run failed (%s), %d in a row", sub.ID, p.Code, next.FailCount)
	_, ok := s.commit(ctx, sub, next, nil, &Run{At: start, MS: ms, Trigger: trigger, Error: p}, start, true)
	return ok
}

func backoff(base time.Duration, failures int) time.Duration {
	d := base
	for i := 0; i < failures && d < backoffMax; i++ {
		d *= 2
	}
	return min(d, backoffMax)
}

// shapeMiss is a run whose result the recipe can no longer read: a failure, and after shapeMissesMax in a row the
// recipe needs redoing.
func (s *Service) shapeMiss(ctx context.Context, sub, next Subscription, p *Problem, trigger string, start time.Time, ms int64) bool {
	next.ShapeMisses = sub.ShapeMisses + 1
	if next.ShapeMisses >= shapeMissesMax {
		return s.needsRecipe(ctx, sub, next, &Problem{CodeShapeChanged, p.Message}, trigger, start)
	}
	return s.failed(ctx, sub, next, p, trigger, start, ms)
}

// needsRecipe pauses sub until its recipe is changed: it is not run again by itself.
func (s *Service) needsRecipe(ctx context.Context, sub, next Subscription, p *Problem, trigger string, start time.Time) bool {
	next.RunState, next.Error, next.NextRunAt = StateNeedsRecipe, p, nil
	s.o.Logf("subscription %s: needs a new recipe (%s)", sub.ID, p.Code)
	_, ok := s.commit(ctx, sub, next, nil, &Run{At: start, Trigger: trigger, Error: p}, start, true)
	return ok
}

type commitData struct {
	seen     []Seen
	items    []Item
	read     []string   // uids of held items now seen
	baseline *time.Time // the baseline mark this run took
}

// commit writes a run's outcome (a run with a Run, a skipped round without), returns what its transaction actually
// claimed, and tells the apps; false when it could not be kept.
func (s *Service) commit(ctx context.Context, sub, next Subscription, d *commitData, run *Run, now time.Time, ran bool) (RunCommit, bool) {
	if ran {
		next.LastRunAt = &now
	}
	if run != nil {
		next.Runs = append(slices.Clone(sub.Runs), *run)
		if len(next.Runs) > RunsKept {
			next.Runs = next.Runs[len(next.Runs)-RunsKept:]
		}
	}
	if !on(next) && next.RunState != StateNeedsRecipe && (next.Continuation == nil || !next.Continuation.Manual) {
		next.NextRunAt = nil // off: run only when asked, apart from a manual continuation already underway
	}
	c := RunCommit{Sub: next, Revision: sub.Revision, Now: now}
	if d != nil {
		c.Seen, c.Items, c.Read, c.Baseline = d.seen, d.items, d.read, d.baseline
	}
	committed, err := s.o.Store.CommitRun(ctx, c)
	if err != nil {
		// Nothing of the run is kept; its lease runs out and the run is made again, deduplicated.
		s.o.Logf("subscription %s: keep run: %v", sub.ID, err)
		return RunCommit{}, false
	}
	if cur, err := s.o.Store.GetSubscription(ctx, sub.ID); err == nil && cur != nil {
		s.o.Changed(shown(*cur), false)
	}
	return committed, true
}

// nextRun is when sub runs next after a run at now: at its daily time, or after its interval with up to a tenth
// more at random so subscriptions made together drift apart.
func (s *Service) nextRun(sub Subscription, now time.Time, _ bool) time.Time {
	if sub.DailyAt != "" && sub.Recipe.Mode == wire.ConnectorSubscriptionModeSnapshot {
		return nextDaily(sub.DailyAt, now, s.o.Location())
	}
	iv := time.Duration(max(sub.IntervalSeconds, floorOf(sub.Recipe.Mode, sub.SourceMin))) * time.Second
	return now.Add(iv + rand.N(iv/10+1))
}

// nextDaily is the next hh:mm in loc after now.
func nextDaily(hhmm string, now time.Time, loc *time.Location) time.Time {
	h, _ := strconv.Atoi(hhmm[:2])
	m, _ := strconv.Atoi(hhmm[3:])
	l := now.In(loc)
	t := time.Date(l.Year(), l.Month(), l.Day(), h, m, 0, 0, loc)
	if !t.After(now) {
		t = time.Date(l.Year(), l.Month(), l.Day()+1, h, m, 0, 0, loc)
	}
	return t.UTC()
}
