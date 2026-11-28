package assistant

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the supervision message ----

// superviseTool marks a queued supervision message (relayMsg.Tool).
const superviseTool = "supervise"

// sendDigest puts the due signals into their supervisors' conversations, one supervision message each: now, or queued
// behind its turn (a message still queued takes these in too). A signal goes to its task's leader, to the background
// supervisor for a task no leader supervises (made here on first use, supervise_background.go), or to the main
// conversation (digestTo, supervise_leader.go); while no main conversation is set, what would go to it goes to the
// conversation that dispatched its task (routeTo). A signal that no longer holds (answered, done, reviewed meanwhile) is
// dropped.
func (s *Service) sendDigest(ctx context.Context) {
	b := &s.supervise
	b.digestMu.Lock()
	defer b.digestMu.Unlock()
	s.sendDigestLocked(ctx)
}

func (s *Service) sendDigestLocked(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	if err := s.restoreFailedStartBatches(ctx); err != nil {
		s.logf("supervise: could not restore deferred failed starts: %v", err)
	}
	s.revalidateFailedStartDeferrals(ctx)
	s.recheckOutageHolds(ctx, time.Now())
	b := &s.supervise
	if err := s.ensureTodosLoaded(ctx); err != nil {
		b.mu.Lock()
		b.init()
		s.retryDigestLocked(s.superviseTimings().urgent)
		b.mu.Unlock()
		return
	}
	b.routingMu.RLock()
	defer b.routingMu.RUnlock()
	s.restoreSupervisePending(ctx)
	batch := s.supervisionBatchInterval(ctx)
	cooldown := s.superviseTimings().routineCooldown()
	gating := batch > 0 || cooldown > 0 // a recipient's routine signals may be held (gateDigestBatches)
	b.dedupMu.Lock()
	defer b.dedupMu.Unlock()
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
	}
	mode := s.supervisionMode(ctx)
	if mode == supervisionModeOff {
		// Switched off while this digest waited for its turn: nothing is taken out of the gather, and the withdrawal
		// empties it.
		b.mu.Unlock()
		s.syncAutoSupervision(ctx)
		return
	}
	if mode == supervisionModeOutcomes && slices.ContainsFunc(slices.Collect(maps.Values(b.pending)), func(g superviseSignal) bool {
		return !supervisionSignalAllowed(mode, g)
	}) {
		// Switched to outcomes while this digest waited: its withdrawal takes the routine signals out first.
		b.mu.Unlock()
		s.syncAutoSupervision(ctx)
		b.mu.Lock()
	}
	b.timer, b.timerAt = nil, time.Time{}
	b.syncBatchInterval(batch)
	list := make([]superviseSignal, 0, len(b.pending))
	for _, g := range b.pending {
		list = append(list, g)
	}
	now := time.Now()
	if wait, ok := digestWaitWithBatch(now, b.last, list, s.superviseTimings(), batch); !ok || wait > time.Second {
		if ok {
			s.scheduleDigestLocked(now)
		}
		b.mu.Unlock()
		return
	}
	list = slices.DeleteFunc(list, func(g superviseSignal) bool {
		return signalNotBeforePending(g, now) || gating && supervisionBatchDeferred(g, now)
	})
	if len(list) == 0 {
		s.scheduleDigestLocked(now)
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	here := s.settings(ctx)
	gone := map[string]bool{} // conversations other than the main one that cannot be told now, as this digest found them
	there := func(to string) bool {
		if _, seen := gone[to]; !seen {
			if to == here.BackgroundSupervisor {
				// One just started, or whose machine has not listed yet, is there: its message queues (backgroundThere).
				gone[to] = !s.backgroundThere(ctx, here, to)
			} else {
				c, ok := s.session(ctx, here.MachineID, to)
				gone[to] = !ok || !c.Assistant
			}
		}
		return !gone[to]
	}
	// What waits for a main conversation: everything while there is no machine (the hub's hello while the bus is down);
	// while no main conversation is set, only what the conversation that dispatched its task cannot be told either
	// (#10126): the rest goes there now.
	waits := map[string]bool{}
	for _, g := range list {
		if here.MachineID == "" || here.Main == "" && !s.toldWithoutMain(ctx, here, g, there) {
			waits[g.id()] = true
		}
	}
	if len(waits) > 0 {
		// Nowhere to tell them now. They stay pending and untold, and go out on a later try (#5420).
		// Said once when it starts and then hourly: the watch's next pass tries again, and logging each try filled
		// the hub's log with a line every 30 seconds while the main conversation was away. What went out meanwhile
		// is not counted: only what still waits keeps this going.
		b.mu.Lock()
		first := b.noMainSince.IsZero()
		if first {
			b.noMainSince = now
		}
		logIt := first || now.Sub(b.noMainLogged) >= superviseNoMainLogEvery
		if logIt {
			b.noMainLogged = now
		}
		since, waiting := now.Sub(b.noMainSince), len(waits)
		s.retryDigestLocked(s.superviseTimings().urgent)
		b.mu.Unlock()
		switch {
		case first:
			s.logf("supervise: no main conversation; %d signal(s) wait for one", waiting)
		case logIt:
			s.logf("supervise: still no main conversation after %s; %d signal(s) waiting", since.Round(time.Second), waiting)
		}
		if len(waits) == len(list) {
			return
		}
	} else {
		b.mu.Lock()
		away := b.noMainSince
		b.noMainSince, b.noMainLogged = time.Time{}, time.Time{}
		b.mu.Unlock()
		switch {
		case away.IsZero():
		case here.Main != "":
			s.logf("supervise: main conversation found after %s; telling what waited", now.Sub(away).Round(time.Second))
		default:
			s.logf("supervise: what waited %s goes to the conversations that dispatched it", now.Sub(away).Round(time.Second))
		}
	}
	b.mu.Lock()
	list = list[:0]
	held := map[string]superviseSignal{}
	for id, g := range b.pending { // as they are now: one may have come or gone meanwhile
		if waits[id] || signalNotBeforePending(g, now) || gating && supervisionBatchDeferred(g, now) {
			held[id] = g
			continue
		}
		list = append(list, g)
	}
	if len(list) == 0 {
		if len(held) > 0 {
			s.scheduleDigestLocked(now)
		}
		b.mu.Unlock()
		return
	}
	b.pending = held
	// Until the batch is marked sent below, the queue refuses its keys as taken: a signal raised again meanwhile is
	// refused as it is once this digest is done (#17838). Every return clears what it did not mark; taken is never
	// persisted, so a restart in between leaves nothing behind.
	took := slices.Clone(list)
	b.init()
	for _, g := range took {
		b.taken[g.id()] = g.Key
	}
	released := false // the marking below clears them in its own lock; only an early return takes it again
	defer func() {
		if !released {
			b.mu.Lock()
			releaseTakenLocked(b, took)
			b.mu.Unlock()
		}
	}()
	prev := b.last
	b.last = now
	b.mu.Unlock()
	if f := s.digestTakenHook; f != nil {
		f()
	}
	if gating || len(held) > 0 {
		defer s.schedulePendingSupervision() // an early return must not strand another batch or held outage signal
	}

	list = s.screenMechanicalSignals(ctx, list, now, prev)
	if len(list) == 0 {
		return
	}
	web := s.webBase(ctx)
	groups := map[string][]superviseSignal{}
	var order []string
	leaders := map[[2]string][]superviseSignal{} // leader agent (machine, session) → the signals of its workers (task_leader.go)
	var leaderOrder []TodoAgent
	var wait []superviseSignal     // with no main conversation, what nobody else can be told either (routeTo)
	s.rehomeLeaderlessWorkers(ctx) // a worker whose leader's task closed gets a supervisor that can act, before routing
	if s.backgroundNeeded(ctx, here, list) {
		// Only an explicitly addressed legacy notice selects the existing background conversation. One that is gone or
		// cannot start another clean round leaves here as it was: the gone check below moves its unread work to main.
		id, was := s.ensureBackground(ctx)
		if id != "" {
			here = s.settings(ctx)
		}
		if lost := s.backgroundLost(ctx, was, cmp.Or(id, here.Main), web); len(lost) > 0 { // what waited for the one that is gone goes out with these
			list = mergeSignals(lost, list)
		}
		if was != "" && id == "" {
			for i := range list {
				if list[i].To == was {
					list[i].To = "" // this digest's explicit legacy target is gone too: regather it for main
				}
			}
		}
	}
	var retry []superviseSignal
	ctx, list, retry = s.allStoppedBatch(ctx, list)
	if len(retry) > 0 {
		s.restoreDigest(retry, now, prev) // unknown is retried, never marked sent or told
	}
	if len(list) == 0 {
		return
	}
	b.mu.Lock()
	releaseTakenLocked(b, took)
	released = true
	for _, g := range list {
		b.sent[g.id()] = g.Key
	}
	b.last = now
	b.mu.Unlock()
	for _, g := range list {
		if next := s.currentSupervisorSignal(g); next.To != g.To {
			s.replaceSupervisePending(g, next)
			g = next
		}
		original := g
		if lead, handled, reachable := s.leaderRoute(g); handled { // never digestTo: a session is no conversation
			if reachable {
				key := [2]string{lead.MachineID, lead.SessionID}
				if _, seen := leaders[key]; !seen {
					leaderOrder = append(leaderOrder, lead)
				}
				leaders[key] = append(leaders[key], g)
				continue
			}
			if g.todoV2Wake() {
				entry, valid, retry := s.todoV2WakeDigestEntry(ctx, g, web)
				if retry {
					wait = append(wait, g)
					continue
				}
				if !valid {
					s.ackSupervisePending([]superviseSignal{g})
					continue
				}
				g.Detail = entry.Detail
			}
			// Unreachable, not dropped: it becomes a decision for the conversation above the leader (escalateLeaderSignal),
			// which has its own delivery target below.
			worker := g
			g = s.escalateLeaderSignal(ctx, g)
			if g.Todo == "" { // nobody above it: restore both the durable outbox and this process's retry (#5732)
				if _, open := s.todoNow(worker.Todo); open && here.Main == "" {
					wait = append(wait, worker) // nobody above it since the check (toldWithoutMain): it waits as that one would
				} else {
					s.restoreDigest([]superviseSignal{original}, now, prev)
				}
				continue
			}
			s.replaceSupervisePending(original, g)
		}
		to := s.routeTo(ctx, here, g, there)
		if to == "" { // no main conversation, and the one that dispatched it cannot be told either: it waits (#10126)
			wait = append(wait, g)
			continue
		}
		if to == here.BackgroundSupervisor && to != here.Main {
			to, g = s.applyBackgroundBreaker(ctx, here, to, g, now)
			if to == "" {
				continue
			}
		}
		if _, ok := groups[to]; !ok {
			order = append(order, to)
		}
		if g.Kind == sigAsk {
			groups[to] = mergeSignals(groups[to], []superviseSignal{g})
		} else {
			groups[to] = append(groups[to], g)
		}
	}
	if gating {
		order = s.gateDigestBatches(order, groups, now, batch, cooldown)
		b.mu.Lock()
		s.scheduleDigestLocked(now)
		b.mu.Unlock()
	}
	for key, list := range leaders {
		leaders[key] = s.screenFinalWakeSignals(ctx, list, now, prev)
	}
	for to, list := range groups {
		groups[to] = s.screenFinalWakeSignals(ctx, list, now, prev)
	}
	delivered := false
	for _, lead := range leaderOrder {
		delivered = s.deliverToLeaderAgent(ctx, lead, leaders[[2]string{lead.MachineID, lead.SessionID}], web, now, prev) || delivered
	}
	for _, to := range order {
		if s.deliverDigest(ctx, here, to, groups[to], web, now, prev) {
			delivered = true
			s.toldRecipient(to, now) // its next routine signals wait out the cooldown (#11614)
		}
	}
	if delivered && (len(order) > 1 || gating) {
		// One message went out: what another left undone (put the last message back to prev) does not undo it.
		b.mu.Lock()
		if b.last.Before(now) {
			b.last = now
		}
		b.mu.Unlock()
	} else if gating && !delivered {
		// A held or moot batch sent nothing. Do not invent a legacy last-message time in case batching is switched off.
		b.mu.Lock()
		if b.last.Equal(now) {
			b.last = prev
		}
		b.mu.Unlock()
	}
	if len(wait) > 0 {
		at := now // nothing went out: the last message is prev again
		if delivered {
			at = time.Time{} // what went out to the others stays the last message
		}
		s.restoreDigest(wait, at, prev)
	}
}

// routeTo is the conversation signal g goes to: where digestTo sends it, the main conversation instead of one that
// cannot be told now (there), and while no main conversation is set, the conversation that dispatched its task
// (dispatcherOf, #10126). "" when none of them can be told: it waits for a main conversation (#5420). With a main
// conversation set it is never "", and nothing goes anywhere else than before.
func (s *Service) routeTo(ctx context.Context, here Settings, g superviseSignal, there func(string) bool) string {
	to := s.digestTarget(ctx, here, g)
	if t, ok := s.todoNow(g.Todo); ok && t.Supervision != nil && t.Supervision.assigned() &&
		(to == t.Supervision.Supervisor || to == "") {
		return to // an unavailable explicit recipient keeps its queue; it never silently falls back to main
	}
	if to != here.Main && !there(to) {
		to = here.Main
	}
	if to == "" {
		to = s.dispatcherOf(g.Todo, there)
	}
	return to
}

// toldWithoutMain: signal g can be told now while no main conversation is set: its task leader's agent takes it
// (leaderRoute: never the main conversation's); with that agent unreachable, the conversation above the leader does
// (supervisorAboveTask, the decision escalateLeaderSignal makes, routed as sendDigest routes it); or a conversation does
// (routeTo). A leader's signal with nobody above the leader to tell waits, as every signal did before (#5420).
func (s *Service) toldWithoutMain(ctx context.Context, here Settings, g superviseSignal, there func(string) bool) bool {
	if _, handled, reachable := s.leaderRoute(g); handled {
		if reachable {
			return true
		}
		t, ok := s.todoNow(g.Todo)
		if !ok {
			return true // its task went meanwhile: nothing is told of it
		}
		up, _ := s.supervisorAboveTask(ctx, t)
		return up != "" && s.routeTo(ctx, here, superviseSignal{Todo: g.Todo, Kind: sigDecide, To: up}, there) != ""
	}
	return s.routeTo(ctx, here, g, there) != ""
}

// releaseTakenLocked clears the taken marks of took that still stand: what a handover or a put-back cleared, or a later
// digest took again, is left as it is. s.supervise.mu is held.
func releaseTakenLocked(b *superviseBook, took []superviseSignal) {
	for _, g := range took {
		if b.taken[g.id()] == g.Key {
			delete(b.taken, g.id())
		}
	}
}

// dispatcherOf is the conversation that dispatched task id, while it can be told now (there): the one its ledger names
// as its supervisor (the side conversation or leader that handed it out with task_plan), else the one it was planned
// in (Todo.Origin). "" when neither can, and for a ledger that names a task leader's agent (leaderRef), which is no
// conversation.
func (s *Service) dispatcherOf(id string, there func(string) bool) string {
	t, ok := s.todoNow(id)
	if !ok {
		return ""
	}
	var sup string
	if t.Supervision != nil {
		sup = t.Supervision.Supervisor
	}
	for _, c := range []string{sup, t.Origin} {
		if _, _, ref := parseLeaderRef(c); c != "" && !ref && there(c) {
			return c
		}
	}
	return ""
}

// deliverDigest puts the signals list into conversation to (the main conversation, a leader or the background
// supervisor) as one supervision message; now is when sendDigest marked it as the last message and prev the last
// message before. True when it went in or was queued.
func (s *Service) deliverDigest(ctx context.Context, here Settings, to string, list []superviseSignal, web string, now, prev time.Time) bool {
	b := &s.supervise
	list = s.gateFailedStartBatches(ctx, to, list, now, prev)
	if len(list) == 0 {
		return false
	}
	var entries []prompts.SuperviseEntry
	var kept, moot, retry []superviseSignal
	s.remedyGoesTo(to, list) // a failed start is ended by this conversation's turn, no other's (start_remedy.go)
	for _, g := range list {
		if e, ok, deferRetry := s.digestEntryChecked(ctx, g, web); ok {
			entries = append(entries, e)
			if g.Kind == sigAsk {
				g.Cards = e.Asks // receipt names exactly the cards this rendered delivery contains
			}
			kept = append(kept, g)
		} else if deferRetry {
			retry = append(retry, g)
		} else {
			moot = append(moot, g)
		}
	}
	if len(retry) > 0 {
		at := now
		if len(entries) > 0 {
			at = time.Time{} // sibling signals still deliver: keep b.last as now
		}
		s.restoreDigest(retry, at, prev)
	}
	s.clearSentTodoReady(moot)
	s.clearSentPRMerged(moot)
	s.clearSentParentCloseout(moot)
	s.turnsTold(ctx, moot) // what no longer holds is not told after a restart either (task_turns.go)
	s.discardFailedStartBatches(ctx, moot, now)
	if len(entries) == 0 {
		s.ackSupervisePending(moot)
		return false
	}
	for i := range kept {
		if s.noMainOriginFallback(here, to, kept[i]) {
			kept[i].OriginOnly = true // persist the route's lack of acceptance authority with the signal itself
		}
	}
	willInterject := slices.ContainsFunc(kept, func(g superviseSignal) bool { return g.Kind == sigDecisionReply })
	c, listed := s.session(ctx, here.MachineID, to)
	directBlocked := !listed || c.Starting || s.relayReplacing(to) || s.relayHasUnresolvedSupervision(to) ||
		!willInterject && (c.Status == proto.StatusWorking || s.relayWaits(to, relayMsg{Tool: superviseTool, Urgent: anyDecision(kept)}))
	if directBlocked && slices.ContainsFunc(kept, supervisionSignalNeedsRevalidation) {
		var queueableEntries []prompts.SuperviseEntry
		var queueable, reval []superviseSignal
		for i, g := range kept {
			if supervisionSignalNeedsRevalidation(g) {
				reval = append(reval, g)
			} else {
				queueableEntries = append(queueableEntries, entries[i])
				queueable = append(queueable, g)
			}
		}
		if len(queueable) > 0 {
			// Keep revalidation-only signals pending without reserving dedup so ordinary signals can still queue or
			// merge into one digest instead of looping in SupervisePending behind them.
			s.restoreDigest(reval, time.Time{}, prev)
			entries, kept = queueableEntries, queueable
		}
	}
	entries, kept = s.screenModelSupervision(ctx, to, entries, kept, now, prev)
	ackable := append(slices.Clone(moot), kept...) // retry entries stay in the outbox
	if len(entries) == 0 {
		s.ackSupervisePending(moot)
		return false
	}
	holds := kept
	entries, kept, rollback, err := s.reserveSupervision(ctx, entries, kept, now)
	if err != nil {
		s.logf("supervise: deduplication unavailable; retrying: %v", err)
		s.releaseFailedStartBatches(ctx, kept)
		s.restoreDigest(kept, now, prev)
		return false
	}
	if len(entries) == 0 {
		s.automaticParksTold(ctx, holds)
		s.ackSupervisePending(ackable) // already covered by the deduplication history
		s.agentDecisionRepliesTold(ctx, holds)
		s.handoffResultsTold(ctx, holds)
		s.todoReadyTold(ctx, holds)
		s.prMergeWakeTold(ctx, holds)
		s.parentCloseoutTold(ctx, holds)
		s.topicFollowupsTold(ctx, holds)
		s.turnsTold(ctx, holds) // said the same within the window: told
		s.finishFailedStartBatches(ctx, to, nil, holds, now)
		b.mu.Lock()
		if b.last.Equal(now) {
			b.last = prev
		}
		b.mu.Unlock()
		return false
	}
	background := to != here.Main && to == here.BackgroundSupervisor // old work only; also scopes its closeout grant
	delivered := kept
	ackKeys := map[[2]string]string{}
	for i, g := range kept {
		key, _ := digestFingerprint(entries[i], g)
		ackKeys[[2]string{g.id(), g.Key}] = key
	}
	accepted := false
	acceptedIDs := map[[2]string]bool{}
	for _, g := range kept {
		acceptedIDs[[2]string{g.id(), g.Key}] = true
	}
	var settled []superviseSignal
	for _, g := range holds {
		if !acceptedIDs[[2]string{g.id(), g.Key}] {
			settled = append(settled, g)
		}
	}
	defer func() {
		if !accepted {
			rollback()
			s.releaseFailedStartBatches(ctx, kept)
			return
		}
		if background {
			// Exclude only the no-main Origin fallback: it tells the dispatcher without making it the acceptance
			// authority. Older automatic background routes can have an empty To too and keep their grant (#5732).
			legacy := make([]superviseSignal, 0, len(kept))
			for _, g := range kept {
				if !g.OriginOnly && !s.noMainOriginFallback(here, to, g) {
					legacy = append(legacy, g)
				}
			}
			s.rememberLegacyBackground(to, legacy)
		}
		s.ackSupervisePending(ackable)
		s.ackSupervisionDedupLocked(ctx, delivered, ackKeys)
		s.parkedDueTold(ctx, kept) // told for good: not again after a restart (parked.go)
		s.automaticParksTold(ctx, kept)
		s.estimateDueTold(ctx, kept)
		s.parkDecidedTold(ctx, kept)
		s.agentDecisionRepliesTold(ctx, kept)
		s.handoffResultsTold(ctx, kept)
		s.todoReadyTold(ctx, kept)
		s.prMergeWakeTold(ctx, kept)
		s.parentCloseoutTold(ctx, kept)
		s.topicFollowupsTold(ctx, holds)
		s.turnsTold(ctx, holds) // and how a turn ended: kept went, the rest of them was said the same within the window
		s.finishFailedStartBatches(ctx, to, kept, settled, now)
	}()
	if !s.supervisePendingDurably(kept) {
		// A daily parked summary is derived from the durable parking ledger, so reconstruction can present its next day
		// before that replacement reached the outbox. Persist it before sending, without letting an older summary replace
		// a newer pending one. The second check below still blocks the send if any outbox write failed (#5732).
		r := &s.relays
		r.mu.Lock()
		if s.relayLocked(ctx) {
			pending := maps.Clone(r.st.SupervisePending)
			r.mu.Unlock()
			for _, g := range kept {
				if g.Kind != sigParkedSummary {
					continue
				}
				if cur, ok := pending[g.id()]; ok && cur.At.After(g.At) {
					continue
				}
				s.queueSignal(g)
			}
		} else {
			r.mu.Unlock()
		}
	}
	if !s.supervisePendingDurably(kept) {
		// The initial outbox write failed or the exact stretch was replaced before this delivery. Restore the in-memory
		// retry and let restoreDigest retry persistence first; neither a direct send nor queue admission may be acknowledged.
		s.restoreDigest(kept, now, prev)
		return false
	}
	render := func(list []prompts.SuperviseEntry) string { return s.renderQueuedDigest(ctx, to, list) }
	queued := s.queuedDigestSignals(to)
	msg := relayMsg{From: superviseTool, FromTitle: "督办", Tool: superviseTool, At: now.UTC()}
	if len(queued) > 0 && !slices.ContainsFunc(kept, supervisionSignalNeedsRevalidation) {
		// The last message may still wait in the queue: this one takes its place with both.
		all := mergeSignals(queued, kept)
		var allEntries []prompts.SuperviseEntry
		for i, g := range all {
			if e, ok := s.digestEntry(ctx, g, web); ok {
				if g.Kind == sigAsk {
					all[i].Cards = e.Asks
				}
				key, _ := digestFingerprint(e, all[i])
				ackKeys[[2]string{g.id(), g.Key}] = key
				allEntries = append(allEntries, e)
			}
		}
		msg.Text, msg.Urgent = render(allEntries), anyDecision(all)
		msg.Signals = encodeDigestSignals(all)
		if s.replaceQueuedSupervise(to, msg) {
			delivered = all
			accepted = true
			b.mu.Lock()
			b.setQueuedLocked(to, all)
			b.mu.Unlock()
			s.publish(ctx)
			return true
		}
	}
	msg.Text, msg.Urgent = render(entries), anyDecision(kept)
	msg.Signals = encodeDigestSignals(kept)
	msg.Interject = slices.ContainsFunc(kept, func(g superviseSignal) bool { return g.Kind == sigDecisionReply })
	if background {
		s.logf("supervise: %d signal(s) to the background supervisor %s", len(entries), short(to))
	} else if to != here.Main {
		s.logf("supervise: %d signal(s) to conversation %s", len(entries), short(to))
	} else {
		s.logf("supervise: %d signal(s) to the main conversation", len(entries))
	}
	c, ok := s.session(ctx, here.MachineID, to)
	if !ok || c.Starting || s.relayReplacing(to) || s.relayHasUnresolvedSupervision(to) || !msg.Interject && (c.Status == proto.StatusWorking || s.relayWaits(to, msg)) {
		if !s.queueDigest(to, msg, kept, now, prev) {
			return false
		}
		accepted = true
		s.publish(ctx)
		if ok && c.Status != proto.StatusWorking && !c.Starting {
			s.drainRelays(here.MachineID, []proto.Session{c})
		}
		return true
	}
	if err := s.tryDirectRelay(ctx, here.MachineID, to, &msg); err != nil {
		if relaySuppressed(err) {
			// Switched off since the digest was gathered: not delivered, not queued, not retried. The reservation is
			// released (the deferred rollback) and what is left of the outbox goes with the withdrawal.
			s.ackSupervisePending(list)
			return false
		}
		if s.refusedDirect(ctx, here.MachineID, to, msg, err) {
			accepted = true // retire this delivery while keeping the task available for review
			return true
		}
		s.logf("supervise: %v; queued", err)
		if s.queueDigest(to, msg, kept, now, prev) {
			accepted = true
			s.publish(ctx)
			if !msg.LookAfter.IsZero() {
				s.drainAfter(here.MachineID, to, max(time.Until(msg.LookAfter), 0))
			}
		}
		return accepted
	}
	accepted = true
	if !msg.Interject { // an in-turn decision reply can pass an older supervision relay; keep that relay tracked
		b.mu.Lock()
		b.setQueuedLocked(to, nil)
		b.mu.Unlock()
	}
	return true
}

// setQueuedLocked keeps list as the signals of the supervision message still queued for conversation to (none: nil);
// b.mu is held.
func (b *superviseBook) setQueuedLocked(to string, list []superviseSignal) {
	if len(list) == 0 {
		delete(b.queued, to)
		return
	}
	if b.queued == nil {
		b.queued = map[string][]superviseSignal{}
	}
	b.queued[to] = list
}

// supervisionSignalNeedsRevalidation reports whether an untried signal must stay in pending for delivery-time
// revalidation rather than being persisted into a rendered queued relay.
func supervisionSignalNeedsRevalidation(g superviseSignal) bool {
	return g.Kind == sigPRConflict || g.Kind == sigDraftPR || g.Kind == sigPREvent || g.Kind == sigDecisionReply || g.Kind == sigTopicFollowup ||
		g.Kind == sigNeedsUserExternalInfo && g.PREventID != "" ||
		g.Kind == sigInbox && g.Inbox != nil && g.Inbox.ToAgent == "unhandled"
}

// queueDigest queues msg, the message of signals kept, behind conversation to's turn; at is when sendDigest
// marked it as the last message and prev the last message before. When the relay queue can't be read nothing is
// queued (false): the signals wait again, no longer marked told (a newer one of the same task and kind keeps its
// place), the message still queued for it (supervise.queued) stays as it was, the last message is prev again (nothing went
// out), and the digest is tried again in the urgent gap, or later when the gap after prev has not passed yet.
func (s *Service) queueDigest(to string, msg relayMsg, kept []superviseSignal, at, prev time.Time) bool {
	tried := !msg.TriedAt.IsZero() || !msg.DoubtedAt.IsZero()
	if !tried && slices.ContainsFunc(kept, supervisionSignalNeedsRevalidation) {
		// Rendered relays cannot recheck PR facts, a source acknowledgment or a decision round. Keep untried signals
		// pending so the next dispatch revalidates them; deliverDigest releases the unaccepted dedup reservation.
		// Once tryDirectRelay has attempted the send, retain the exact rendered payload, OnceID, and attempt state in
		// the durable queue so reconciliation settles or retries under the same delivery identity (#12567).
		s.restoreDigest(kept, at, prev)
		return false
	}
	wasInterject := msg.Interject
	msg.Interject = false // interject is never persisted in the relay queue
	b := &s.supervise
	msg.Signals = encodeDigestSignals(kept)
	n, err := s.queueRelayDurable(to, msg)
	if err == nil && n > 0 {
		b.mu.Lock()
		if !wasInterject {
			b.setQueuedLocked(to, kept)
		}
		b.mu.Unlock()
		return true
	}
	if relaySuppressed(err) {
		return false // switched off since the digest was gathered: nothing waits, and nothing is retried
	}
	retry := s.superviseTimings().urgent
	s.logf("supervise: the relay queue can't be read; %d signal(s) wait, trying again in %s", len(kept), retry)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.init()
	if b.last.Equal(at) { // no later message went out meanwhile
		b.last = prev
	}
	for _, g := range kept {
		id := g.id()
		if b.sent[id] != g.Key {
			continue // a newer one of its kind went out meanwhile
		}
		delete(b.sent, id)
		if cur, ok := b.pending[id]; !ok {
			b.pending[id] = g
		} else if g.Kind == sigReport && cur.Key != g.Key {
			b.pending[id] = joinReports(g, cur) // both reports are told, this one first
		} else if g.Kind == sigInbox && cur.Key != g.Key {
			b.pending[id] = joinInbox(g, cur) // the newer item, with the messages of both
		} else if g.Kind == sigOutageRecovery {
			b.pending[id] = joinOutageRecovery(g, cur)
		}
	}
	s.updateSupervisePending(func(pending map[string]superviseSignal) {
		for _, g := range kept {
			if cur, ok := b.pending[g.id()]; ok {
				pending[g.id()] = cur
			}
		}
	})
	s.retryDigestLocked(retry)
	return false
}

// mergeSignals is a with b's signals over its own (one per task and kind), in a's order then b's.
func mergeSignals(a, b []superviseSignal) []superviseSignal {
	out := slices.Clone(a)
	for _, g := range b {
		if i := slices.IndexFunc(out, func(x superviseSignal) bool { return x.id() == g.id() }); i >= 0 {
			if g.Kind == sigReport && out[i].Key != g.Key {
				g = joinReports(out[i], g)
			}
			if g.Kind == sigInbox && out[i].Key != g.Key {
				g = joinInbox(out[i], g)
			}
			if g.Kind == sigOutageRecovery {
				g = joinOutageRecovery(out[i], g)
			}
			if g.Recipient == "" && out[i].Recipient != "" && out[i].Key == g.Key &&
				(g.To == "" || g.To == out[i].Recipient) &&
				(out[i].To == "" || g.To == "" || out[i].To == g.To) {
				g.Recipient = out[i].Recipient
			}
			g = preserveIdleHandoff(out[i], g)
			out[i] = g
		} else {
			out = append(out, g)
		}
	}
	return out
}

// replaceQueuedSupervise: a supervision message still waits in the queue: msg takes its place (true).
func (s *Service) replaceQueuedSupervise(to string, msg relayMsg) bool {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return false
	}
	if !s.autoSupervisionOn(context.Background()) {
		return false
	}
	previousQueue := b.st.Queue
	previousPending := maps.Clone(b.st.SupervisePending)
	q := slices.Clone(previousQueue[to])
	// A withdrawn, tried, or doubtful supervision relay still owns the outcome of its original send. Let drainRelays
	// reconcile and remove it before a replacement can replay any of the same signals under a new message id (#5732).
	if slices.ContainsFunc(q, func(m relayMsg) bool {
		return m.unresolvedSupervision() || m.Tool == superviseTool && !b.doubted[relayMsgID(to, m)].IsZero()
	}) {
		return false
	}
	for i := range q {
		if q[i].Tool == superviseTool && (i > 0 || !b.draining[to]) {
			q[i] = msg.replacing(q[i], time.Now()) // it waited since then (relay_presence.go)
			if signals, structured := decodeDigestSignals(msg.Signals); structured {
				ackSupervisePendingLocked(b.st.SupervisePending, signals)
				if len(b.st.SupervisePending) == 0 {
					b.st.SupervisePending = nil
				}
			}
			b.st.Queue = maps.Clone(previousQueue)
			b.st.Queue[to] = q
			if err := s.saveRelayLocked(); err != nil {
				// Restore the old queue and every outbox predecessor together. The caller's fallback must restore its
				// reservation instead of finalizing new signals that were never admitted (#5732).
				b.st.Queue = previousQueue
				b.st.SupervisePending = previousPending
				return false
			}
			return true
		}
	}
	return false
}

// digestEntryChecked is digestEntry, additionally reporting retry=true when delivery-time revalidation hit a
// transient store failure so callers restore the signal instead of treating it as permanently moot.
func (s *Service) digestEntryChecked(ctx context.Context, g superviseSignal, web string) (prompts.SuperviseEntry, bool, bool) {
	if g.todoV2Wake() {
		return s.todoV2WakeDigestEntry(ctx, g, web)
	}
	switch g.Kind {
	case sigTodoReady:
		return s.todoReadyDigestEntry(ctx, g, web)
	case sigPRMerged:
		return s.prMergedDigestEntry(ctx, g, web)
	case sigParentCloseout:
		return s.parentCloseoutDigestEntry(ctx, g, web)
	case sigTopicFollowup:
		return s.topicFollowupDigestEntry(ctx, g, web)
	case sigCardlessWait:
		// P1-1: an unreadable acceptance book is not evidence of no card — restore and retry before any claim logic.
		if s.cardlessDigestShouldRetry(g) {
			return prompts.SuperviseEntry{}, false, true // acceptance book unreadable: restore and retry, never render on an unknown ledger
		}
		if valid, retry := s.completeCardlessClaim(ctx, g); !valid {
			return prompts.SuperviseEntry{}, false, retry
		}
	}
	e, ok := s.digestEntry(ctx, g, web)
	return e, ok, false
}

// cardlessDigestShouldRetry reports that a stamped cardless claim's delivery-time revalidation could not run because
// the acceptance book could not be read. It covers both a pending review and an owner wait. Every other outcome (a
// card/worker now answers, or the claim still holds) is left to digestEntry, which re-runs the checks against a
// readable book.
func (s *Service) cardlessDigestShouldRetry(g superviseSignal) bool {
	t, ok := s.todoNow(g.Todo)
	if !ok || t.Supervision == nil {
		return false
	}
	if !cardlessClaimMatches(t, g) {
		return false // reviewed, replaced, or handed back meanwhile: moot, not a transient retry
	}
	told, known := cardlessClaimToldAt(t.Supervision, g)
	if !known || told == nil {
		return false // the marker is not committed yet; completeCardlessClaim owns that step
	}
	_, readable, _ := s.askCopyReadable(t.ID)
	return !readable
}

// todoReadyDigestEntry builds the supervision entry for a sigTodoReady signal, returning retry=true on transient
// repository-store read errors so the wake stays retryable after recovery.
func (s *Service) todoReadyDigestEntry(ctx context.Context, g superviseSignal, web string) (prompts.SuperviseEntry, bool, bool) {
	t, ok := s.todoNow(g.Todo)
	if !ok || t.DoneAt != nil {
		return prompts.SuperviseEntry{}, false, false
	}
	prereqs, ready, err := s.checkTodoAfterReady(ctx, t)
	if err != nil {
		return prompts.SuperviseEntry{}, false, true
	}
	if !ready || todoReadyKey(t) != g.Key {
		return prompts.SuperviseEntry{}, false, false
	}
	var doneWhen []string
	if t.Supervision != nil {
		doneWhen = t.Supervision.DoneWhen
	}
	e := prompts.SuperviseEntry{
		ID:       t.ID,
		Title:    t.Title,
		Kind:     sigTodoReady,
		DoneWhen: doneWhen,
		Link:     todoLink(web, t.ID),
		Quiet:    t.Quiet,
		TodoReady: &prompts.SuperviseTodoReady{
			PartOf:    t.PartOf,
			Notes:     clip(strings.TrimSpace(t.Notes), 2000),
			Satisfied: prereqs,
		},
	}
	s.setEntryOrigin(ctx, t, web, &e)
	return e, true, false
}

// digestEntry is signal g as the message lists it; false when it no longer holds.
func (s *Service) digestEntry(ctx context.Context, g superviseSignal, web string) (prompts.SuperviseEntry, bool) {
	return s.digestEntryRecheck(ctx, g, web, false)
}

// digestEntryRecheck is digestEntry, allowing an already-tried queued sigDecisionReply whose queue admission set
// ToldAt to be rechecked for open-round validity before retrying under its original OnceID (#12567).
func (s *Service) digestEntryRecheck(ctx context.Context, g superviseSignal, web string, triedQueued bool) (prompts.SuperviseEntry, bool) {
	if g.todoV2Wake() {
		e, ok, _ := s.todoV2WakeDigestEntry(ctx, g, web)
		return e, ok
	}
	switch g.Kind {
	case sigTopicFollowup:
		e, ok, _ := s.topicFollowupDigestEntry(ctx, g, web)
		return e, ok
	case sigTodoReady:
		e, ok, _ := s.todoReadyDigestEntry(ctx, g, web)
		return e, ok
	case sigPRMerged:
		e, ok, _ := s.prMergedDigestEntry(ctx, g, web)
		return e, ok
	case sigParentCloseout:
		e, ok, _ := s.parentCloseoutDigestEntry(ctx, g, web)
		return e, ok
	}
	t, ok := s.todoNow(g.Todo)
	acceptedFeedback := g.Kind == sigInbox && g.Inbox != nil && g.Inbox.AfterAcceptance
	if !ok || t.Supervision == nil && g.Kind != sigFailed && g.Kind != sigReport ||
		t.DoneAt != nil && g.Kind != sigFailed && !acceptedFeedback {
		return prompts.SuperviseEntry{}, false
	}
	v := t.Supervision
	if v == nil {
		if g.Kind != sigFailed && g.Kind != sigReport {
			return prompts.SuperviseEntry{}, false
		}
		v = &Supervision{} // a failed start or reporting conversation has no ledger and grants no acceptance authority
	}
	if !triedQueued && routineSupervisionSuppressed(t, g, time.Now()) {
		return prompts.SuperviseEntry{}, false
	}
	st := v.state(t)
	var replyAsk acceptAsk
	var draft *TodoPRStatus
	var estimateBrief *TaskBrief
	var outageRecoveryText string
	switch g.Kind {
	case sigParkedSummary:
		r := v.parking()
		wantTo := s.automaticParkSummaryTo(ctx, s.settings(ctx), t)
		if r == nil || r.Automatic == nil || g.Key != g.At.Format(time.DateOnly)+"|"+r.Automatic.Dependency || wantTo != g.To {
			return prompts.SuperviseEntry{}, false
		}
	case sigHandoffResult:
		h := v.handoffResult()
		if h == nil || h.ID != g.Key {
			return prompts.SuperviseEntry{}, false
		}
		// Render through the existing review entry, with this receipt as the evidence.
		e := prompts.SuperviseEntry{ID: t.ID, Title: t.Title, Kind: sigReview, Detail: handoffResultDetail(h),
			DoneWhen: v.DoneWhen, Round: v.Round, Link: todoLink(web, t.ID), Quiet: t.Quiet}
		s.setEntryOrigin(ctx, t, web, &e)
		return e, true
	case sigFailed:
		if g.Remedying {
			// Notification batching never holds the remedy itself. If that remedy already succeeded or became a
			// final failure while this signal waited, its old remediable notification is moot; the final attempt,
			// when there is one, has its own generation and must not be discarded here.
			if t.DoneAt != nil || t.StartError != "" {
				return prompts.SuperviseEntry{}, false
			}
			if _, p, i, err := s.remedyOf(ctx, t.ID); err == nil {
				if r := p.Tasks[i].Start; r == nil || r.State != remedying {
					return prompts.SuperviseEntry{}, false
				}
			}
		}
	case sigDraftPR:
		if t.PRURL != g.PRURL && !slices.Contains(t.PRs, g.PRURL) {
			return prompts.SuperviseEntry{}, false
		}
		draft = prFacts(t, g.PRURL)
		if draft == nil || draft.State != "open" || !draft.Draft || !draft.Own || draft.HeadOID == "" ||
			g.Key != draft.URL+"@"+draft.HeadOID || draft.CheckedAt.IsZero() || !s.stageFollows(t.ID, draft.URL) {
			return prompts.SuperviseEntry{}, false
		}
		if age := time.Since(draft.CheckedAt); age < 0 || age > triagePRStatusFreshFor {
			return prompts.SuperviseEntry{}, false
		}
	case sigDecisionReply:
		a, ok := s.askCopy(t.ID)
		if !ok || a.AgentReply == nil || !triedQueued && !a.AgentReply.ToldAt.IsZero() || agentDecisionReplyKey(a) != g.Key || acceptAskStale(&a, t) {
			return prompts.SuperviseEntry{}, false
		}
		if a.AgentReply.Handled {
			if r := triageLatestReview(v); st != reviewPending || r == nil || r.At.After(a.AskedAt) {
				return prompts.SuperviseEntry{}, false
			}
		} else if !a.AcceptedAt.IsZero() || acceptAskEnded(&a, t) {
			return prompts.SuperviseEntry{}, false
		}
		replyAsk = a
	case sigPRConflict:
		prURL := g.prConflictURL()
		if prURL == "" || t.PRURL != prURL && !slices.Contains(t.PRs, prURL) {
			return prompts.SuperviseEntry{}, false
		}
		p := prFacts(t, prURL)
		if p == nil || p.URL != prURL || p.State != "open" || !p.Conflicts || p.HeadOID == "" ||
			g.Key != p.URL+"@"+p.HeadOID || !s.stageFollows(t.ID, p.URL) || s.isReferencePRLink(ctx, t.ID, p.URL) ||
			v.externalPRConflictWoke(p.URL, p.HeadOID) {
			return prompts.SuperviseEntry{}, false // resolved, closed, unfollowed, reference-only, or already woken by external PR park
		}
	case sigPREvent:
		if !s.prEventSignalStillHolds(t, g) {
			return prompts.SuperviseEntry{}, false
		}
	case sigTimedOut:
		if st != reviewTimedOut {
			return prompts.SuperviseEntry{}, false
		}
	case sigReview:
		if st != reviewPending || v.handoffResult() != nil || g.Recipient != "" && g.Recipient != v.Supervisor {
			return prompts.SuperviseEntry{}, false
		}
		if g.externalPRReview() {
			r := v.externalPRWake()
			if r == nil || g.Key != reviewKey(v) {
				return prompts.SuperviseEntry{}, false
			}
			g.Detail = strings.TrimSpace(g.Detail + " " + r.ExternalPR.detail(r))
		}
	case sigReviewNudge:
		if !s.settings(ctx).todosV2On() || st != reviewPending || v.handoffResult() != nil {
			return prompts.SuperviseEntry{}, false
		}
	case sigNeedsYou:
		if !s.taskWaits(ctx, t) {
			return prompts.SuperviseEntry{}, false // answered meanwhile
		}
	case sigDecide:
		if g.Unreachable {
			// The leader could not be reached: the worker's own state is not what decides this (the leader's task is still open).
		} else if st != reviewNeedsUser {
			return prompts.SuperviseEntry{}, false // decided meanwhile
		}
	case sigEscalated:
		if st != reviewNeedsUser && len(g.Cards) == 0 { // a leader's escalated cards are told as they were
			return prompts.SuperviseEntry{}, false // decided meanwhile
		}
	case sigOverdue:
		if b, ok := s.briefOfTodo(t.ID); !ok || b.Overdue == nil || st == reviewNeedsUser || st == reviewParked {
			return prompts.SuperviseEntry{}, false // re-scoped, with the owner, or parked meanwhile
		}
	case sigEstimateDue:
		b, ok := s.briefOfTodo(t.ID)
		if !ok || !s.estimateDueCurrent(t, b, g.Key) {
			return prompts.SuperviseEntry{}, false // completed, reviewed, re-scoped or already told meanwhile
		}
		estimateBrief = &b
	case sigAllStopped:
		fresh, holds := s.checkedAllStopped(ctx, t, g)
		if !holds {
			return prompts.SuperviseEntry{}, false // a member resumed or gained a wake/card while queued
		}
		g.Detail, g.Minutes = fresh.Detail, fresh.Minutes
		if note := breakerNote(g, answerLanguage(string(s.botLang(ctx)))); note != "" {
			if g.Detail != "" {
				g.Detail = note + "\n" + g.Detail
			} else {
				g.Detail = note
			}
		}
	case sigLoopNoop:
		if current, ok := s.nativeLoopNoopSignal(ctx, t); !ok || current.Key != g.Key {
			return prompts.SuperviseEntry{}, false
		}
	case sigNoProgress:
		if st != "" || !s.noProgressHolds(g) {
			return prompts.SuperviseEntry{}, false // progress came, or it was parked or reviewed, meanwhile
		}
	case sigIdle, sigUnjudged, sigStalled:
		if st == reviewParked || st == reviewClosing || st == reviewTimedOut || v.handoffResult() != nil {
			return prompts.SuperviseEntry{}, false // a hold or its handoff result superseded the old stop
		}
	case sigOutageRecovery:
		current, agents, valid := s.outageHoldStillValid(ctx, g)
		if !valid {
			return prompts.SuperviseEntry{}, false // closed, reviewed, resumed, or recovered while held (#13158)
		}
		outageRecoveryText = s.renderOutageRecovery(ctx, current, agents, g.Outage)
	case sigCheck:
		if g.Report == checkNoProgressReport && st == reviewParked {
			return prompts.SuperviseEntry{}, false // parked meanwhile: it waits on purpose (parked.go)
		}
	case sigParkedDue:
		if r := v.parking(); st != reviewParked || r == nil || parkDueKey(r) != g.Key {
			return prompts.SuperviseEntry{}, false // its agent works again, or it was reviewed again, meanwhile
		}
	case sigParkDecided:
		if r := v.decidedPark(); r == nil || parkDecidedKey(r) != g.Key {
			return prompts.SuperviseEntry{}, false // told by another message meanwhile (a later decision's)
		}
	case sigOrphaned:
		if g.Orphan == orphanQueued {
			p, i, err := s.currentPlanAssignment(ctx, t)
			if err != nil || queuedStartWaits(t) || s.taskStartOnWay(t.ID) {
				return prompts.SuperviseEntry{}, false
			}
			if q, ok := queuedTaskOrphan(p.ID, p.Tasks[i], time.Now()); !ok || q.key != g.Key {
				return prompts.SuperviseEntry{}, false
			}
		}
		if g.WorkerSet != "" {
			if st != "" && st != reviewRework {
				return prompts.SuperviseEntry{}, false
			}
			s.todos.mu.RLock()
			group := completedWorkerSets(s.todos.todos)[t.ID]
			s.todos.mu.RUnlock()
			if group.key() != g.WorkerSet {
				return prompts.SuperviseEntry{}, false
			}
		}
		if p := v.parking(); p != nil && p.Handoff != nil || v.handoffResult() != nil {
			return prompts.SuperviseEntry{}, false // an acknowledged handoff already explains the stop
		}
		if !strings.HasPrefix(g.Key, nodeRecoveryKeyPrefix) {
			if o, ok := s.orphanOf(t.ID); !ok || o.key != g.Key {
				return prompts.SuperviseEntry{}, false // an agent is back at it, or it is another stretch by now
			}
		}
	case sigRecovered:
		if st == reviewClosing || st == reviewTimedOut || st == reviewUserCheck || st == reviewPending {
			return prompts.SuperviseEntry{}, false // closed, timed out, or already waiting for review meanwhile
		}
	case sigUndecidable:
		if st != reviewNeedsUser {
			return prompts.SuperviseEntry{}, false // decided or reviewed again meanwhile
		}
	case sigCardlessWait:
		// The pending claim is durable before its marker. Compare identities at Postgres precision so a restored
		// signal cannot acknowledge a newer review or owner wait, or be rejected merely for losing nanoseconds.
		told, known := cardlessClaimToldAt(v, g)
		if !cardlessClaimMatches(t, g) || !known || told == nil {
			return prompts.SuperviseEntry{}, false // reviewed, replaced, or not handed back yet, meanwhile
		}
		// Re-run the conditions that made the signal valid against current data: a card or active worker can have
		// appeared while the entry waited in a queued/batched digest, in which case the "no card" claim is stale and
		// must not be rendered (#12648 semantic review).
		if a, ok := s.askCopy(t.ID); ok && !acceptAskStale(&a, t) && a.AcceptedAt.IsZero() {
			return prompts.SuperviseEntry{}, false // an owner answer now stands
		}
		// taskAgents, like taskAgentsIn, omits a machine whose session list cannot be read; a machine that went
		// unreachable between the signal and delivery therefore still reads as stopped and the entry is delivered,
		// not dropped. That is the conservative direction (a nudge to review, never a false owner card), and the
		// watch-side cardlessAgentsReachable gate is what withholds the stamp while a machine is already unknown.
		if !cardlessWorkersStopped(s.taskAgents(ctx, t)) {
			return prompts.SuperviseEntry{}, false // a worker holds a card or went back to work meanwhile
		}
	case sigDeliveryHold:
		if !deliveryHoldStands(t, g) {
			return prompts.SuperviseEntry{}, false // reviewed, parked, delivered or held anew meanwhile
		}
	case sigRecheck:
		if a, ok := s.askCopy(t.ID); st != reviewNeedsUser || !ok || !a.AcceptedAt.IsZero() || a.AgentReply != nil ||
			"recheck@"+a.AskedAt.UTC().Format(time.RFC3339Nano) != g.Key {
			return prompts.SuperviseEntry{}, false // answered, or asked again, meanwhile
		}
	case sigNeedsUserExternalInfo:
		if !s.needsUserExternalInfoStillHolds(t, g) {
			return prompts.SuperviseEntry{}, false // answered, superseded, or closed meanwhile
		}
	case sigOwnerPresent:
		if !s.ownerPresentStillHolds(t, g) {
			return prompts.SuperviseEntry{}, false // closed, card already active, or no longer waiting on owner
		}
	case sigInbox:
		if g.Inbox == nil || !s.inboxSignalsOn(ctx) {
			return prompts.SuperviseEntry{}, false // switch went off meanwhile
		}
		if !s.inboxSignalHolds(t) && (!acceptedFeedback || !s.acceptedFeedbackHolds(t, g.Inbox) || s.agentSupervised(t)) {
			if needsUserWakeEligible(t) && !s.agentSupervised(t) && g.Inbox.ToAgent != "unhandled" {
				// Task entered reviewNeedsUser while this inbox signal waited: deliver it as non-destructive external info
				// alongside the pending needs_user summary rather than silently dropping it (#13112).
				source, summary := formatInboxExternalInfo(*g.Inbox)
				e := prompts.SuperviseEntry{
					ID:           t.ID,
					Title:        t.Title,
					Kind:         sigNeedsUserExternalInfo,
					DoneWhen:     v.DoneWhen,
					Round:        v.Round,
					Link:         todoLink(web, t.ID),
					Quiet:        t.Quiet,
					ExternalInfo: s.needsUserExternalInfoEntry(ctx, t, source, summary),
				}
				e.LastReview = s.lastReviewEntry(ctx, v)
				s.setEntryOrigin(ctx, t, web, &e)
				s.briefEntry(ctx, t.ID, &e)
				return e, true
			}
			return prompts.SuperviseEntry{}, false // waits for the owner's decision (returned) or no longer eligible
		}
		if g.Inbox.ToAgent == "unhandled" && !s.taskInboxUnhandled(ctx, g.Key) {
			return prompts.SuperviseEntry{}, false // the source event was answered while its notice waited
		}
	}
	var asks []prompts.SuperviseAsk
	if g.Kind == sigAsk {
		if asks = s.heldAsks(t.ID, g.To); len(asks) == 0 {
			return prompts.SuperviseEntry{}, false // answered, or gone up a rung, meanwhile
		}
	}
	e := prompts.SuperviseEntry{ID: t.ID, Title: t.Title, Kind: g.Kind, Detail: g.Detail, Minutes: g.Minutes, DoneWhen: v.DoneWhen,
		Round: v.Round, Link: todoLink(web, t.ID), Quiet: t.Quiet}
	if g.Kind == sigOutageRecovery {
		e.Detail = ""
		e.OutageRecovery = outageRecoveryText
	}
	s.setEntryOrigin(ctx, t, web, &e)
	if note := breakerNote(g, answerLanguage(string(s.botLang(ctx)))); note != "" && g.Kind != sigAllStopped {
		if e.Detail != "" {
			e.Detail = note + "\n" + e.Detail
		} else {
			e.Detail = note
		}
	}
	if draft != nil {
		e.Detail = prompts.RenderDraftPR(prompts.DraftPR{Language: answerLanguage(string(s.botLang(ctx))),
			URL: draft.URL, Head: draft.HeadOID})
	}
	if estimateBrief != nil {
		e.Detail = prompts.RenderEstimateDue(prompts.EstimateDue{Language: answerLanguage(string(s.botLang(ctx))),
			Expected: estimateBrief.ExpectNow, Elapsed: g.Minutes})
	}
	if g.Kind == sigNeedsUserExternalInfo {
		e.Detail = ""
		e.ExternalInfo = s.needsUserExternalInfoEntry(ctx, t, g.ExternalSource, g.Detail)
		if g.PREventID != "" {
			// Keep the direct link and the complete, already bounded untrusted-content envelope.
			e.ExternalInfo.Source = g.ExternalSource
			e.ExternalInfo.Summary = g.Detail
		}
		e.LastReview = s.lastReviewEntry(ctx, v)
		s.briefEntry(ctx, t.ID, &e)
		return e, true
	}
	// The last review, shown only in a Fresh message (supervise_round.go): a decision reply's round confirms the decision
	// that review put to the owner.
	e.LastReview = s.lastReviewEntry(ctx, v)
	if g.Kind == sigDecisionReply {
		e.AgentReply = &prompts.DecisionAgentReply{Language: answerLanguage(string(s.botLang(ctx))),
			Machine: replyAsk.AgentReply.MachineID, Session: replyAsk.AgentReply.SessionID, Handled: replyAsk.AgentReply.Handled,
			Text: replyAsk.AgentReply.Text, Closeout: replyAsk.AgentReply.Closeout}
		return e, true // the reply's exact session is in AgentReply; no arbitrary last Agent is evidence
	}
	if a := v.AutoReview; g.Kind == sigReview && a != nil && a.Round == v.Round && (a.State == autoClean || a.State == autoBlocking || a.State == autoFailed) {
		e.Review = &prompts.SuperviseReviewed{State: a.State, Count: a.Count, Blocking: a.Blocking, Findings: a.Findings, Error: a.Error}
	}
	s.briefEntry(ctx, t.ID, &e) // what was asked, in the owner's words (task_brief_digest.go)
	if g.Kind == sigEscalated {
		e.Recommend = g.Ask // the leader's recommendation (escalate)
		e.Asks = g.Cards
		e.Background = !s.isLeader(ctx, v.Supervisor) && !s.sideSupervisor(ctx, s.settings(ctx), v.Supervisor) // only the background supervisor escalates a task neither a leader nor a side conversation supervises
	}
	if g.Kind == sigDecide {
		e.Recommend = g.Ask // the task leader's recommendation
		e.LeaderGone = g.Unreachable && g.Recipient == ""
		e.SupervisorGone = g.Unreachable && g.Recipient != ""
	}
	if g.Kind == sigAsk {
		e.Asks, e.Decisions = asks, s.todoDecisionsContext(ctx, t.ID) // what was settled already, not to be gone against
	}
	if g.Kind == sigReport { // what it said is the report itself, not a reason in brackets
		e.Detail, e.Report, e.Said, e.Evidence, e.Step, e.Steps = "", g.Report, g.Detail, g.Evidence, g.Step, g.Steps
	}
	// Progress reports stay quiet. Their persisted evidence accompanies the next
	// real supervision event, even after restart or a report of a different kind.
	for i, c := range v.Criteria {
		if c.At != nil {
			e.Evidence = append(e.Evidence, fmt.Sprintf("criterion %d [%s] %s — %s (%s at %s)",
				i+1, c.State, c.Criterion, c.Evidence, c.By, c.At.UTC().Format(time.RFC3339)))
		}
	}
	if g.Kind == sigOrphaned {
		e.Orphan = g.Orphan
	}
	if g.Kind == sigNoProgress {
		e.Calls, e.Waits = g.Calls, g.Waits
	}
	if g.Kind == sigRecheck || g.Kind == sigOwnerCannot {
		e.Decision = g.Decision
	}
	if g.Kind == sigParkedDue || g.Kind == sigParkedSummary {
		e.Detail, e.Parked = "", s.parkedEntry(ctx, v.parking())
	}
	if g.Kind == sigParkedDue && e.Parked != nil {
		e.Parked.Resent = parkedDueResend(v.parking())
	}
	if g.Kind == sigParkDecided && st == reviewParked { // parked again before this went: what it waits for now
		e.Parked = s.parkedEntry(ctx, v.parking())
	}
	if g.Kind == sigParkedDue || g.Kind == sigParkDecided {
		// What was decided on the task meanwhile: the answer it waited for may be there (#8044).
		e.Decisions = s.todoDecisionsContext(ctx, t.ID)
	}
	if g.Kind == sigOwnerCannot { // the owner's answer is the signal, not a reason in brackets
		e.Detail, e.Response = "", g.Detail
	}
	if g.Kind == sigCheck {
		e.Report = g.Report
		if g.Report == checkDecidedReport { // the owner's option, the decision as asked, whether the Agent has it
			e.Detail, e.Response, e.Decision, e.Told = "", g.Detail, g.Decision, g.Told
		}
	}
	if g.Kind == sigFailed {
		s.remedyEntry(ctx, t, &e) // its kept tab, and the turn that hears of it (start_remedy.go)
	}
	if g.Kind == sigInbox {
		in := *g.Inbox
		e.Inbox = &in
	}
	b := &s.supervise
	b.mu.Lock()
	e.Ask = cmp.Or(g.Ask, b.asks[t.ID])
	b.mu.Unlock()
	if g.Kind != sigNeedsYou {
		e.Ask = ""
	}
	if g.Kind == sigAllStopped || len(g.Unlinked) > 0 {
		// An unlinked terminal has no verified reply: its listed preview can be from an earlier binding.
		return e, true
	}
	if g.Kind == sigInbox && g.Inbox.ToAgent == "unhandled" {
		// This failure belongs to the delivered turn, not a successor's current reply.
		e.AgentID, e.Session, e.Machine = g.Session, short(g.Session), g.Machine
		if m, ok := s.device(ctx, g.Machine); ok {
			e.Machine = m.Name
		}
		return e, true
	}
	// Where its agent is and what it said last: the whole last answer when it stopped, a line while it works.
	agents := s.taskAgents(ctx, t)
	var last proto.Session
	for _, c := range agents {
		if g.Session != "" && c.SessionID == g.Session || g.Session == "" && c.UpdatedAt.After(last.UpdatedAt) {
			last = c
		}
	}
	if last.SessionID != "" {
		e.Session = short(last.SessionID)
		e.AgentID = last.SessionID
		if m, ok := s.device(ctx, last.MachineID); ok {
			e.Machine = m.Name
		}
		text := strings.TrimSpace(last.LastText)
		if g.Kind == sigReport {
			text = "" // it said what it had to in the report
		} else if g.Kind == sigReview || g.Kind == sigIdle || g.Kind == sigUnjudged || g.Kind == sigOutageRecovery {
			if answer := s.lastTurnAnswer(ctx, last.MachineID, last.SessionID); answer != "" {
				text = answer
			}
		}
		// The files it handed over, copied to this conversation's computer (task_files.go, #10511); the receipt path
		// usually did it already, and a copy here only fills in after a restart.
		s.deliverTaskFiles(ctx, t, last.MachineID, s.conversationMachine(ctx, s.taskReceiver(ctx, t), ""), text)
		e.Files = taskFilePrompts(s.taskFilesNow(t.ID))
		if c, arts, ok := closeout.Parse(text); ok {
			e.Closeout = &prompts.SuperviseCloseout{Status: c.Status, Waiting: c.Waiting, Merged: c.Merged, Deployed: c.Deployed,
				Did: c.Did, Verified: c.Verified, Missing: c.Missing}
			if s.checkCloseout(ctx, &c, arts); c.Checked != nil {
				e.Closeout.CheckedMerged, e.Closeout.CheckedDeployed = c.Checked.Merged, c.Checked.Deployed
				e.Closeout.LocalDeployment = c.Checked.LocalDeployment
			}
			for _, a := range arts {
				e.Closeout.Artifacts = append(e.Closeout.Artifacts, a.Kind+" "+a.Ref)
			}
			text = closeout.Strip(text) // told above, in a form that cannot be cut in the middle
		}
		e.Last = clipMiddle(text, 600)
	} else if g.Machine != "" {
		e.Machine = g.Machine
	}
	return e, true
}

// sortEntries puts what waits on a decision first.
func sortEntries(list []prompts.SuperviseEntry) []prompts.SuperviseEntry {
	rank := map[string]int{sigNeedsYou: 0, sigAsk: 0, sigEscalated: 0, sigDecide: 0, sigOwnerCannot: 0, sigUndecidable: 1, sigDeliveryHold: 1, sigRecheck: 1, sigCardlessWait: 1, sigNeedsUserExternalInfo: 1, sigEstimateDue: 2, sigParkedDue: 2, sigParkDecided: 2, sigIdle: 2, sigUnjudged: 2, sigAllStopped: 2, sigFailed: 3, sigStarted: 3, sigTodoReady: 3, sigPRMerged: 3, sigParentCloseout: 3, sigInterrupted: 4, sigOutageRecovery: 4, sigOrphaned: 4, sigRecovered: 4, sigTimedOut: 4, sigReview: 5, sigReviewNudge: 5, sigStalled: 6, sigNoProgress: 6, sigLoopNoop: 6, sigLeaderStuck: 6, sigOverdue: 6, sigInbox: 6, sigReport: 7}
	rank[sigBlockersUnblocked], rank[sigMergeTrainTurn], rank[sigWriteConflict] = 3, 3, 3
	at := func(e prompts.SuperviseEntry) int {
		switch {
		case e.Kind == sigCheck && e.Report == checkDecidedReport: // the owner's answer
			return rank[sigOwnerCannot]
		case e.Kind == sigCheck:
			return rank[sigStalled]
		case e.Kind != sigReport:
		case e.Report == upReportQuestion || e.Report == upReportBlocked: // waits on a decision too
			return 1
		case e.Report == upReportDone: // with the work waiting for review
			return rank[sigReview]
		}
		return rank[e.Kind]
	}
	sort.SliceStable(list, func(i, j int) bool { return at(list[i]) < at(list[j]) })
	return list
}

// originDisplayName returns the human-readable title of conversation id (live session title, direction name, saved
// title, or short id fallback).
func (s *Service) originDisplayName(ctx context.Context, cfg Settings, id string) string {
	if id == "" {
		return ""
	}
	if c, ok := s.session(ctx, cfg.MachineID, id); ok {
		if title := strings.TrimSpace(c.Title); title != "" {
			return title
		}
	}
	if d := s.directionOf(id); d != nil {
		if name := strings.TrimSpace(d.Name); name != "" {
			return name
		}
	}
	if titles, ok := s.conversationTitles(); ok {
		if title := strings.TrimSpace(titles[id].title); title != "" {
			return title
		}
	}
	if c, ok := s.session(ctx, cfg.MachineID, id); ok {
		if name := strings.TrimSpace(conversationName(c, cfg.Main)); name != "" {
			return name
		}
	}
	return short(id)
}

// originAttribution returns the origin conversation's id, title, and link for a task dispatched from a non-main
// conversation. Main-chat tasks and task-leader agent refs return empty strings.
func (s *Service) originAttribution(ctx context.Context, t Todo, web string) (id, title, link string) {
	cfg := s.settings(ctx)
	if t.Origin == "" || t.Origin == cfg.Main || s.isLeaderRef(t.Origin) {
		return "", "", ""
	}
	return t.Origin, s.originDisplayName(ctx, cfg, t.Origin), sessionLink(web, cfg.MachineID, t.Origin, true)
}

// setEntryOrigin populates e's origin conversation attribution from t.
func (s *Service) setEntryOrigin(ctx context.Context, t Todo, web string, e *prompts.SuperviseEntry) {
	e.OriginID, e.OriginTitle, e.OriginLink = s.originAttribution(ctx, t, web)
}

// ---- the main conversation's pushes ----

// shouldHoldSupervisionPushes reports whether a supervision relay to conversation to should hold the turn's finish
// push. Main chat, direction leads, and the background supervisor always hold pushes; a side conversation holds
// info-level and routine monitoring rounds, and lets user-level supervision rounds push the owner.
func (s *Service) shouldHoldSupervisionPushes(ctx context.Context, to string, msg relayMsg) bool {
	if msg.Tool != superviseTool {
		return false
	}
	cfg := s.settings(ctx)
	if !s.sideSupervisor(ctx, cfg, to) {
		return true
	}
	signals, ok := decodeDigestSignals(msg.Signals)
	if !ok || len(signals) == 0 {
		return true
	}
	return !slices.ContainsFunc(signals, func(g superviseSignal) bool { return g.userLevel() })
}

// noteSideSupervisionPush records the task and conversation links to attach when a side conversation's user-level
// supervision round finishes and pushes the owner.
func (s *Service) noteSideSupervisionPush(ctx context.Context, to string, msg relayMsg) error {
	if err := s.releasePushes(ctx, to); err != nil {
		return err
	}
	cfg := s.settings(ctx)
	web := s.webBase(ctx)
	var links []string
	seen := map[string]bool{}
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			links = append(links, u)
		}
	}
	if signals, ok := decodeDigestSignals(msg.Signals); ok {
		for _, g := range signals {
			if g.userLevel() && g.Todo != "" {
				add(todoLink(web, g.Todo))
			}
		}
	}
	add(sessionLink(web, cfg.MachineID, to, true))
	b := &s.supervise
	b.mu.Lock()
	b.init()
	b.sideLinks[to] = links
	b.mu.Unlock()
	return nil
}

// consumeSideSupervisionLinks returns and clears any pending task/conversation links recorded for conversation id's
// user-level supervision push.
func (s *Service) consumeSideSupervisionLinks(id string) []string {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	links := b.sideLinks[id]
	delete(b.sideLinks, id)
	return links
}

// holdPushesParam marks an assistant.send whose supervision turn holds the conversation's pushes (holdPushes).
// Any other send releases them (call); scheduled inputs carry their own persisted push mode.
const holdPushesParam = "hold_pushes"
