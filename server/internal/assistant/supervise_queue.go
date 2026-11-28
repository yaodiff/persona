package assistant

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

func encodeDigestSignals(list []superviseSignal) string {
	raw, _ := json.Marshal(list)
	return string(raw)
}

// decodeDigestSignals reads the comparable JSON payload. The bool distinguishes a structured empty list from a legacy
// text-only message or an invalid payload; exact retraction must not mistake the former for legacy prose.
func decodeDigestSignals(raw string) ([]superviseSignal, bool) {
	if raw == "" {
		return nil, false
	}
	var list []superviseSignal
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, false
	}
	return list, true
}

// Outside directions never appear in a session listing to drain a digest queued by an older manager.
func (s *Service) recoverOutsideDirectionDigests(ctx context.Context) {
	here := s.settings(ctx)
	if here.Main == "" {
		return
	}
	r := &s.relays
	r.mu.Lock()
	var queues []string
	if s.relayLocked(ctx) {
		queues = slices.Collect(maps.Keys(r.st.Queue))
	}
	r.mu.Unlock()
	moved := false
	for _, from := range queues {
		d, _, _ := s.directionSnapshot(ctx, from)
		if d.Outside != nil && from != here.Main && s.moveOutsideDirectionDigests(ctx, from, here.Main) {
			moved = true
		}
	}
	if moved {
		s.drainConversations(here.MachineID, []string{here.Main})
	}
}

// Move the queued messages in one checkpoint. Queue admission already acknowledged their fingerprints.
// Main's normal drain rechecks each structured signal against the current task and renders it for its recipient.
func (s *Service) moveOutsideDirectionDigests(ctx context.Context, from, main string) bool {
	r := &s.relays
	r.mu.Lock()
	defer r.mu.Unlock()
	if !s.relayLocked(ctx) || r.draining[from] {
		return false
	}
	var moved, kept []relayMsg
	for _, msg := range r.st.Queue[from] {
		signals, structured := decodeDigestSignals(msg.Signals)
		if msg.Tool != superviseTool || !structured || len(signals) == 0 || msg.Withdrawn || !msg.TriedAt.IsZero() || !msg.DoubtedAt.IsZero() {
			kept = append(kept, msg)
			continue
		}
		msg.OnceID = "" // the delivery id includes its destination
		moved = append(moved, msg)
	}
	if len(moved) == 0 {
		return false
	}
	next := r.st
	next.Queue = maps.Clone(r.st.Queue)
	if len(kept) == 0 {
		delete(next.Queue, from)
	} else {
		next.Queue[from] = kept
	}
	next.Queue[main] = append(slices.Clone(next.Queue[main]), moved...)
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, relayKey, next); err != nil {
			s.logf("supervise: could not move the outside direction's queue to main: %v", err)
			return false
		}
	}
	r.st = next
	return true
}

// Keep the same rendering for initial dispatch and a queue that has waited while tasks changed.
func (s *Service) renderQueuedDigest(ctx context.Context, to string, list []prompts.SuperviseEntry, originalFresh ...bool) string {
	here := s.settings(ctx)
	background := to != here.Main && to == here.BackgroundSupervisor
	leader := background || to != here.Main && s.isLeader(ctx, to)
	fresh := background && !backgroundKeepsContext(here)
	if len(originalFresh) > 0 {
		fresh = originalFresh[0]
	}
	level := supervisionTriage
	if len(list) > 0 && !slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool { return e.Kind != sigParkedSummary }) {
		level = supervisionInfo
	}
	list = joinTurnReports(list)
	return prompts.RenderSupervise(prompts.Supervise{Level: level, Entries: sortEntries(list), Leader: leader, Background: background,
		Fresh: fresh, Side: to != here.Main && !leader, Escalated: escalatedIn(list),
		BackgroundEscalated: slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool { return e.Kind == sigEscalated && e.Background }),
		LeaderStuck:         slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool { return e.Kind == sigLeaderStuck }),
		Overdue:             slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool { return e.Kind == sigOverdue }),
		ParkedDue:           slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool { return e.Kind == sigParkedDue }),
		ParkDecided:         slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool { return e.Kind == sigParkDecided }),
		Decisions: slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool {
			return e.Kind == sigRecheck || e.Kind == sigUndecidable || e.Kind == sigOwnerCannot || e.Kind == sigCardlessWait
		}),
		Inbox: slices.ContainsFunc(list, func(e prompts.SuperviseEntry) bool { return e.Kind == sigInbox }),
		Asked: askedIn(list), AskMinutes: int(askHoldFor.Minutes()), PushGrading: askedIn(list) && s.quietModeOn(ctx)})
}

// joinTurnReports makes a task's report and the end of the same Agent turn one entry (reportStopHold): the stop entry,
// which carries how the hub judged the turn and the Agent's last answer, also says what the Agent reported, its words,
// evidence and step. Only the rendered message changes: each signal keeps its own delivery and deduplication.
func joinTurnReports(list []prompts.SuperviseEntry) []prompts.SuperviseEntry {
	stops := map[string]int{}
	for i, e := range list {
		if e.Kind == sigReview || e.Kind == sigIdle || e.Kind == sigUnjudged || e.Kind == sigInterrupted {
			if _, seen := stops[e.ID]; !seen {
				stops[e.ID] = i
			}
		}
	}
	if len(stops) == 0 {
		return list
	}
	out := make([]prompts.SuperviseEntry, 0, len(list))
	joined := map[int]prompts.SuperviseEntry{}
	taken := map[int]bool{}
	for i, e := range list {
		j, ok := stops[e.ID]
		if e.Kind != sigReport || !ok || e.Session != "" && list[j].Session != "" && e.Session != list[j].Session {
			continue
		}
		stop, had := joined[j]
		if !had {
			stop = list[j]
			stop.Evidence = slices.Clone(stop.Evidence)
		}
		if stop.Reported == "" || reportRank(e.Report) < reportRank(stop.Reported) {
			stop.Reported = e.Report
		}
		if stop.Said != "" && e.Said != "" && stop.Said != e.Said {
			stop.Said += "\n—\n" + e.Said
		} else if e.Said != "" {
			stop.Said = e.Said
		}
		for _, x := range e.Evidence {
			if !slices.Contains(stop.Evidence, x) {
				stop.Evidence = append(stop.Evidence, x)
			}
		}
		if e.Step > 0 {
			stop.Step, stop.Steps = e.Step, e.Steps
		}
		joined[j] = stop
		taken[i] = true // told in the stop entry; the caller's list is left as it is
	}
	for i, e := range list {
		if taken[i] {
			continue
		}
		if stop, ok := joined[i]; ok {
			e = stop
		}
		out = append(out, e)
	}
	return out
}

// reportRank orders a report's kinds by what waits on the supervisor most: a question, then blocked, then done.
func reportRank(kind string) int {
	switch kind {
	case upReportQuestion:
		return 0
	case upReportBlocked:
		return 1
	case upReportDone:
		return 2
	}
	return 3
}

// recheckDigestTo honors explicit moves through the same policy as immediate dispatch. Otherwise it preserves
// a queued legacy supervisor: a default-policy change alone is not permission to rehome an existing relay.
func (s *Service) recheckDigestTo(ctx context.Context, cfg Settings, g superviseSignal) string {
	if t, ok := s.todoNow(g.Todo); ok && t.Supervision != nil && t.Supervision.assigned() {
		return s.digestTo(ctx, cfg, g)
	}
	if g.To != "" {
		return s.digestTo(ctx, cfg, g)
	}
	switch g.Kind {
	case sigEscalated, sigFailed, sigStarted, sigLeaderStuck:
		return cfg.Main
	}
	t, ok := s.todoNow(g.Todo)
	if !ok || t.Supervision == nil {
		return cfg.Main
	}
	lead := t.Supervision.Supervisor
	if lead == "" || lead == cfg.Main {
		return cfg.Main
	}
	if t.Supervision.state(t) == reviewNeedsUser && g.Kind != sigAllStopped {
		return cfg.Main
	}
	return lead
}

// recheckDigestTarget resolves the delivery conversation for the supervisor preserved by recheckDigestTo.
func (s *Service) recheckDigestTarget(ctx context.Context, cfg Settings, g superviseSignal) string {
	return s.digestConversation(ctx, cfg, g, s.recheckDigestTo(ctx, cfg, g))
}

// Recheck structured queue entries at the last delivery boundary. Unknown task state keeps the original queue.
// Legacy text-only entries retain their behavior; their task facts cannot safely be reconstructed from prose.
func (s *Service) recheckQueuedDigest(ctx context.Context, to string, msg relayMsg) (relayMsg, bool, bool) {
	next, stale, keep, _ := s.recheckQueuedDigestAtDetailed(ctx, to, msg, false, nil)
	return next, stale, keep
}

// recheckQueuedDigestDetailed is recheckQueuedDigest, additionally reporting triedMismatch when a tried/doubted
// supervision relay cannot be rewritten because its rendered text or target set diverged from the original send.
func (s *Service) recheckQueuedDigestDetailed(ctx context.Context, to string, msg relayMsg) (relayMsg, bool, bool, bool) {
	return s.recheckQueuedDigestAtDetailed(ctx, to, msg, false, nil)
}

// recheckQueuedDigestAt is recheckQueuedDigest, optionally for a queued message behind the head. The timeout sweep
// uses anywhere only while the source conversation is not draining; the ordinary drain always rechecks the head.
// change runs after the original signal's validity recheck, so an escalation cannot revive a resolved worker signal.
func (s *Service) recheckQueuedDigestAt(ctx context.Context, to string, msg relayMsg, anywhere bool,
	change func(superviseSignal) (superviseSignal, bool)) (relayMsg, bool, bool) {
	next, stale, keep, _ := s.recheckQueuedDigestAtDetailed(ctx, to, msg, anywhere, change)
	return next, stale, keep
}

func (s *Service) recheckQueuedDigestAtDetailed(ctx context.Context, to string, msg relayMsg, anywhere bool,
	change func(superviseSignal) (superviseSignal, bool)) (relayMsg, bool, bool, bool) {
	var signals []superviseSignal
	if msg.Signals == "" {
		return msg, false, false, false
	}
	if err := json.Unmarshal([]byte(msg.Signals), &signals); err != nil || len(signals) == 0 {
		return msg, false, true, false
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return msg, false, true, false
	}
	now := time.Now()
	tried := !msg.TriedAt.IsZero() || !msg.DoubtedAt.IsZero()
	var deferred []superviseSignal
	if !tried {
		before := len(signals)
		signals = slices.DeleteFunc(signals, func(g superviseSignal) bool { return s.routineSignalSuppressed(g, now) })
		if dropped := before - len(signals); dropped > 0 {
			s.logf("supervise: %d queued routine signal(s) suppressed by task state or recent owner input", dropped)
		}
		signals, deferred = s.partitionFinalWakeSignals(ctx, signals, now, "drain")
	}
	// A tried message cannot change its text or identity; do not let wake partitioning strand it at the queue head.
	var retry []superviseSignal
	ctx, signals, retry = s.allStoppedBatch(ctx, signals)
	if len(retry) > 0 {
		return msg, false, true, false
	}
	here, web := s.settings(ctx), s.webBase(ctx)
	groups := map[string][]superviseSignal{}
	entries := map[string][]prompts.SuperviseEntry{}
	for i, g := range signals {
		signals[i] = s.currentSupervisorSignal(g)
	}
	signals = mergeSignals(nil, signals)
	for _, g := range signals {
		if t, ok := s.todoNow(g.Todo); ok && t.DoneAt == nil && g.Kind == sigNeedsYou {
			if _, known := s.taskNeedsRecheck(ctx, t); !known {
				return msg, false, true, false
			}
		}
		if g.Kind == sigTodoReady {
			if t, ok := s.todoNow(g.Todo); ok && t.DoneAt == nil {
				if _, _, err := s.checkTodoAfterReady(ctx, t); err != nil {
					return msg, false, true, false
				}
			}
		}
		if g.Kind == sigPRMerged {
			if t, ok := s.todoNow(g.Todo); ok && t.DoneAt == nil {
				if _, _, err := s.checkUnparkedPRMerged(ctx, t, g.PRURL); err != nil {
					return msg, false, true, false
				}
			}
		}
		if g.Kind == sigParentCloseout {
			if t, ok := s.todoNow(g.Todo); ok && t.DoneAt == nil {
				if _, _, _, err := s.checkParentCloseoutReady(ctx, t); err != nil {
					return msg, false, true, false
				}
			}
		}
		var e prompts.SuperviseEntry
		var ok bool
		if g.todoV2Wake() {
			var retry bool
			e, ok, retry = s.todoV2WakeDigestEntry(ctx, g, web)
			if retry {
				return msg, false, true, false
			}
		} else {
			if g.Kind == sigCardlessWait && s.cardlessDigestShouldRetry(g) {
				return msg, false, true, false
			}
			e, ok = s.digestEntryRecheck(ctx, g, web, tried)
		}
		if ok {
			if g.Kind == sigAsk {
				g.Cards = e.Asks
			}
			// A worker reassigned to an Agent needs the ordinary dispatcher, not assistant.send to its old conversation.
			if _, handled, _ := s.leaderRoute(g); handled {
				return msg, false, true, false
			}
			if change != nil {
				if changed, replace := change(g); replace {
					g = changed
					if e, ok = s.digestEntryRecheck(ctx, g, web, tried); !ok {
						continue
					}
				}
			}
			target := s.recheckDigestTarget(ctx, here, g)
			if target == "" {
				return msg, false, true, false
			}
			groups[target] = append(groups[target], g)
			entries[target] = append(entries[target], e)
		}
	}
	if len(groups) == 0 && len(deferred) == 0 {
		return msg, true, false, false
	}
	next := msg
	next.OnceID = relayMsgID(to, msg)
	next.Signals = encodeDigestSignals(groups[to])
	if len(groups[to]) > 0 {
		next.Text = s.renderQueuedDigest(ctx, to, entries[to], prompts.IsSuperviseRound(msg.Text))
		next.Urgent = anyDecision(groups[to])
	}
	// Render before taking the queue lock: rendering may read other service state.
	moved := map[string]relayMsg{}
	for target, list := range groups {
		if target == to {
			continue
		}
		m := msg
		m.OnceID = "" // another destination is a separate delivery
		m.Text = s.renderQueuedDigest(ctx, target, entries[target])
		m.Signals, m.Urgent = encodeDigestSignals(list), anyDecision(list)
		moved[target] = m
	}
	b := &s.relays
	b.mu.Lock()
	q := b.st.Queue[to]
	index := 0
	if anywhere {
		index = slices.Index(q, msg)
		if index < 0 || b.draining[to] || b.replacing[to] {
			b.mu.Unlock()
			return msg, false, true, false
		}
	} else if len(q) == 0 || q[0] != msg {
		b.mu.Unlock()
		return msg, false, true, false
	}
	// Do not change the identity or destination of a send that may already have landed.
	if tried && (next.Text != msg.Text || len(groups) > 1 || len(groups[to]) == 0) {
		b.mu.Unlock()
		return msg, false, true, true
	}
	state := b.st
	state.Queue = maps.Clone(b.st.Queue)
	for target, m := range moved {
		state.Queue[target] = append(slices.Clone(state.Queue[target]), m)
	}
	if len(groups[to]) > 0 {
		state.Queue[to] = slices.Clone(q)
		state.Queue[to][index] = next
	} else {
		state.Queue[to] = slices.Delete(slices.Clone(q), index, index+1)
		if len(state.Queue[to]) == 0 {
			delete(state.Queue, to)
		}
	}
	// Keep a durable copy of every deferred idle in the queue rewrite itself. If the process stops before the
	// in-memory restoration below, startup reloads this outbox entry instead of losing the only remaining copy.
	if len(deferred) > 0 {
		state.SupervisePending = maps.Clone(state.SupervisePending)
		if state.SupervisePending == nil {
			state.SupervisePending = map[string]superviseSignal{}
		}
		for _, g := range deferred {
			if _, newer := state.SupervisePending[g.id()]; !newer {
				state.SupervisePending[g.id()] = g
			}
		}
	}
	// Save moves and the corresponding source removal together, before anything can send the new payload.
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, relayKey, state); err != nil {
			b.mu.Unlock()
			return msg, false, true, false
		}
	}
	b.st = state
	b.mu.Unlock()
	s.restoreDrainedWakeSignals(ctx, deferred, web, now)
	return next, len(groups[to]) == 0, false, false
}

// restoreDrainedWakeSignals runs only after the durable queue rewrite and outbox restoration have committed together.
// Queue admission reserved the signals' fingerprints and turn receipts; release those matching reservations before the
// in-memory retry, while the durable outbox independently covers a restart before this step runs.
func (s *Service) restoreDrainedWakeSignals(ctx context.Context, deferred []superviseSignal, web string, now time.Time) {
	if len(deferred) == 0 {
		return
	}
	s.supervise.dedupMu.Lock()
	s.forgetSupervision(ctx, deferred, web)
	s.turnsUntold(ctx, deferred)
	s.supervise.dedupMu.Unlock()
	for i := range deferred {
		deferred[i].batchTo, deferred[i].batchUntil = "", time.Time{}
	}
	s.restoreDigest(deferred, now, time.Time{})
}

// Read the payload that is actually queued, rather than the last in-memory digest (which may have drained).
func (s *Service) queuedDigestSignals(to string) []superviseSignal {
	// A pre-signals relay has no structured payload, so retain the legacy book's fallback without nesting the locks in
	// the opposite order from signalReplacing (supervise.mu → relays.mu). The queue is still checked under relays.mu;
	// if it changed meanwhile, its current structured payload wins and this snapshot is not used.
	s.supervise.mu.Lock()
	legacy := slices.Clone(s.supervise.queued[to])
	s.supervise.mu.Unlock()
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return nil
	}
	for _, msg := range b.st.Queue[to] {
		if msg.Tool != superviseTool {
			continue
		}
		var list []superviseSignal
		if msg.Signals != "" {
			_ = json.Unmarshal([]byte(msg.Signals), &list)
			return list
		}
		return legacy
	}
	return nil
}
