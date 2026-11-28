package assistant

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The switch for automatic supervision messages (Settings.AutoSupervisionMode, docs/assistant/unified-entry.md).
//
// It stops only the messages the assistant sends on its own, the ones that wake the main, a leader or the background
// supervisor conversation (Tool == superviseTool, and a task leader Agent's supervision message). What it never
// touches: the task ledger and every task's state, the owner's own questions and acceptance (task_review and the
// owner's cards), permission cards and the answers to them, and the safety checks.
//
// It has three modes. all (the default) sends every supervision message. off sends none: nothing is sent, queued or
// scheduled, and what was gathered or queued is withdrawn once. outcomes sends only the one-shot wake-ups a supervisor
// has to act on (a task that finished, stopped, failed, reported done, blocked or a question, or needs a decision) and
// drops the routine checks made while a task runs (supervisionSignalAllowed); switching to it withdraws only what it
// drops. Switching back to all, or from off to either, takes only events that happen afterwards: it does not replay
// what was withdrawn.

// The modes of Settings.AutoSupervisionMode.
const (
	supervisionModeAll      = "all"
	supervisionModeOutcomes = "outcomes"
	supervisionModeOff      = "off"
)

// supervisionModes are the values Settings.AutoSupervisionMode accepts, in the settings page's order.
var supervisionModes = []string{supervisionModeAll, supervisionModeOutcomes, supervisionModeOff}

// errSupervisionSuppressed is what a delivery step answers when the mode drops the message at the moment of sending.
// It is a third outcome, neither a delivery nor a failure: nothing was sent, nothing is retried, the owner is not
// pushed and no receipt, dedup record or acceptance is written for it.
var errSupervisionSuppressed = errors.New("automatic supervision messages are off: the message was suppressed")

// relaySuppressed reports whether err is the suppressed outcome.
func relaySuppressed(err error) bool { return errors.Is(err, errSupervisionSuppressed) }

// supervisionModeOf is the mode in v. Unset (or a value this hub does not know) falls back to the older switch,
// AutoSupervisionMessages: unset or true is all, false is off, so every assistant that never saw the mode behaves as
// before.
func supervisionModeOf(v Settings) string {
	if slices.Contains(supervisionModes, v.AutoSupervisionMode) {
		return v.AutoSupervisionMode
	}
	if v.AutoSupervisionMessages != nil && !*v.AutoSupervisionMessages {
		return supervisionModeOff
	}
	return supervisionModeAll
}

// autoSupervisionOf reports whether v sends any automatic supervision message (a mode other than off); it is what the
// older boolean auto_supervision_messages reads.
func autoSupervisionOf(v Settings) bool { return supervisionModeOf(v) != supervisionModeOff }

// supervisionSignalAllowed reports whether mode sends g. all sends every signal and off none. outcomes sends a task's
// terminal handoff, stop, failure, an explicit report of done, blocked or a question, and every signal that waits on a
// decision or the owner's attention; it drops the periodic and routine checks made while a task runs (stalled, no
// progress, estimates, review nudges, the retry that started it, schedule checks, the parked summary, PR and Inbox side
// signals). A kind this switch does not list is sent: a new wake-up is never lost to a mode that predates it.
func supervisionSignalAllowed(mode string, g superviseSignal) bool {
	switch mode {
	case supervisionModeOff:
		return false
	case supervisionModeOutcomes:
	default:
		return true
	}
	switch g.Kind {
	case sigReport:
		return g.Report == upReportDone || g.Report == upReportBlocked || g.Report == upReportQuestion
	case sigCheck:
		return g.Report == checkDecidedReport
	case sigStalled, sigNoProgress, sigLeaderStuck, sigEstimateDue, sigOverdue, sigReviewNudge, sigStarted,
		sigParkedSummary, sigPRConflict, sigDraftPR, sigPREvent, sigInbox:
		return false
	}
	return true
}

// supervisionSignalsAllowed reports whether mode sends a message made of list: off none, all every one, outcomes one
// with at least one signal it sends. A message with no signals to judge (an empty list) is sent unless off.
func supervisionSignalsAllowed(mode string, list []superviseSignal) bool {
	switch mode {
	case supervisionModeOff:
		return false
	case supervisionModeOutcomes:
		return len(list) == 0 || slices.ContainsFunc(list, func(g superviseSignal) bool { return supervisionSignalAllowed(mode, g) })
	}
	return true
}

// supervisionRelayAllowed reports whether mode sends msg. A message that is not a supervision message is always sent;
// a supervision message is judged by the signals it carries, and one queued before messages carried them (legacy text
// only) is dropped only by off.
func supervisionRelayAllowed(mode string, msg relayMsg) bool {
	if msg.Tool != superviseTool {
		return true
	}
	if mode == supervisionModeOff {
		return false
	}
	signals, structured := decodeDigestSignals(msg.Signals)
	if !structured {
		return true
	}
	return supervisionSignalsAllowed(mode, signals)
}

// supervisionMode is the assistant's mode. A settings read that fails serves the last good ones (settings), and a
// Service that was never set up reads as all.
func (s *Service) supervisionMode(ctx context.Context) string {
	if s.storeAccessor() == nil {
		return supervisionModeAll // a Service that was never set up has no settings to turn anything off
	}
	return supervisionModeOf(s.settings(ctx))
}

// autoSupervisionOn reports whether any automatic supervision message is sent (the mode is not off).
func (s *Service) autoSupervisionOn(ctx context.Context) bool {
	return s.supervisionMode(ctx) != supervisionModeOff
}

// supervisionAllowed reports whether the assistant's mode sends g now.
func (s *Service) supervisionAllowed(ctx context.Context, g superviseSignal) bool {
	return supervisionSignalAllowed(s.supervisionMode(ctx), g)
}

// supervisionWithdrawn is what one withdrawal took out.
type supervisionWithdrawn struct {
	Queued   int // supervision messages taken off the relay queues (or marked withdrawn when a send is in flight)
	Pending  int // gathered signals, in memory or in the durable outbox
	Deferred int // failed starts held back by the failed-start cooldown
}

func (w supervisionWithdrawn) any() bool { return w.Queued+w.Pending+w.Deferred > 0 }

// nodeCancelEvery is how often the manager asks one node conversation again to cancel the supervision input still
// waiting in its queue while the switch is off.
const nodeCancelEvery = 20 * time.Second

// supervisionModeOff is the switch's memory-only state in superviseBook.
type supervisionOff struct {
	mu      sync.Mutex
	was     bool                 // the last sync found it off
	routine bool                 // the last sync found it in outcomes: its first withdrawal is logged
	unsave  bool                 // a withdrawal changed the relay queue but its write failed: write again
	cancel  map[string]time.Time // machine|session → when the node was last asked to cancel its waiting supervision input
}

// syncAutoSupervision applies the mode: off withdraws what automatic supervision holds, outcomes withdraws only the
// routine signals it drops (both idempotent, cheap when there is nothing), all forgets both. The saved settings change
// goes through settingsChanged, a restart and the watch's tick come here too, so a mode left on off or outcomes is
// applied on whichever happens first. It reports whether any supervision message is sent (the mode is not off).
func (s *Service) syncAutoSupervision(ctx context.Context) bool {
	off := &s.supervise.off
	switch s.supervisionMode(ctx) {
	case supervisionModeAll:
		off.mu.Lock()
		off.was, off.routine = false, false
		off.mu.Unlock()
		return true
	case supervisionModeOutcomes:
		off.mu.Lock()
		first := !off.routine
		off.was, off.routine = false, true
		off.mu.Unlock()
		w := s.withdrawRoutineSupervision(ctx)
		if first || w.any() {
			s.logf("supervise: automatic supervision messages are outcomes only: withdrew %d queued message(s), %d gathered signal(s)",
				w.Queued, w.Pending)
		}
		if w.Queued > 0 {
			s.publish(ctx)
		}
		return true
	}
	off.mu.Lock()
	first := !off.was
	off.was, off.routine = true, false
	off.mu.Unlock()
	w := s.withdrawSupervision(ctx, first)
	if first || w.any() {
		s.logf("supervise: automatic supervision messages are off: withdrew %d queued message(s), %d gathered signal(s), %d deferred failed start(s)",
			w.Queued, w.Pending, w.Deferred)
	}
	if first {
		s.cancelNodeSupervision(ctx, "", nil)
	}
	if w.Queued > 0 {
		s.publish(ctx)
	}
	return false
}

// withdrawRoutineSupervision is outcomes' withdrawal: it takes out only the signals outcomes drops, from the gathered
// signals, the durable outbox and the supervision messages in the relay queues. A queued message made only of them
// leaves the queue (a send in flight keeps its head, marked withdrawn with nothing to replay, as withdrawSupervision
// does); a message that carries an outcome too stays as it is, so no outcome wake-up is lost, and a withdrawn head keeps
// only the outcome signals it would replay. Failed starts held for the cooldown are outcomes and stay, and the nodes
// are not asked to cancel anything: what they hold may be an outcome.
func (s *Service) withdrawRoutineSupervision(ctx context.Context) supervisionWithdrawn {
	var out supervisionWithdrawn
	routine := func(g superviseSignal) bool { return !supervisionSignalAllowed(supervisionModeOutcomes, g) }
	b := &s.supervise
	b.mu.Lock()
	b.init()
	for id, g := range b.pending {
		if routine(g) {
			delete(b.pending, id)
			out.Pending++
		}
	}
	b.mu.Unlock()

	var emptied []string // conversations with no supervision message left in their queue
	r := &s.relays
	r.mu.Lock()
	if s.relayLocked(ctx) {
		changed := false
		for id, g := range r.st.SupervisePending {
			if routine(g) {
				delete(r.st.SupervisePending, id)
				out.Pending++
				changed = true
			}
		}
		if changed && len(r.st.SupervisePending) == 0 {
			r.st.SupervisePending = nil
		}
		for to, queue := range r.st.Queue {
			kept := make([]relayMsg, 0, len(queue))
			removed, left := false, false
			for i, m := range queue {
				if m.Tool != superviseTool {
					kept = append(kept, m)
					continue
				}
				signals, structured := decodeDigestSignals(m.Signals)
				if m.Withdrawn {
					// Nothing of it is sent again; only what it would replay if the original never landed is checked.
					if structured && slices.ContainsFunc(signals, routine) {
						m.Signals = encodeDigestSignals(slices.DeleteFunc(signals, routine))
						changed = true
					}
					kept, left = append(kept, m), true
					continue
				}
				if !structured || len(signals) == 0 || slices.ContainsFunc(signals, func(g superviseSignal) bool { return !routine(g) }) {
					kept, left = append(kept, m), true // legacy text, nothing to judge, or an outcome among its signals
					continue
				}
				if i == 0 && r.draining[to] || !m.DoubtedAt.IsZero() {
					// A send in flight (or whose answer was lost) still owns its outcome: keep the head, nothing to replay.
					m.Withdrawn, m.Signals = true, encodeDigestSignals([]superviseSignal{})
					kept, left = append(kept, m), true
					changed = true
					continue
				}
				out.Queued++
				removed, changed = true, true
			}
			if removed && !left {
				emptied = append(emptied, to)
			}
			if len(kept) == 0 {
				delete(r.st.Queue, to)
			} else {
				r.st.Queue[to] = kept
			}
		}
		off := &s.supervise.off
		off.mu.Lock()
		unsaved := off.unsave
		off.mu.Unlock()
		if changed || unsaved {
			err := s.saveRelayLocked()
			off.mu.Lock()
			off.unsave = err != nil
			off.mu.Unlock()
		}
	}
	r.mu.Unlock()

	if len(emptied) > 0 {
		b.mu.Lock()
		for _, to := range emptied {
			b.setQueuedLocked(to, nil)
		}
		b.mu.Unlock()
	}
	return out
}

// withdrawSupervision takes out of every place the switch must empty: the gathered signals and their timer, the durable
// outbox, the supervision messages in the relay queues and (deferred) the failed starts held for the cooldown. A send already in
// flight is not cut off; its head stays marked withdrawn, so the drain that sent it neither retries it nor replays it
// (sendRelay refuses a send that has not gone yet). Nothing it takes out is sent later.
func (s *Service) withdrawSupervision(ctx context.Context, deferred bool) supervisionWithdrawn {
	var out supervisionWithdrawn
	b := &s.supervise
	b.mu.Lock()
	b.init()
	out.Pending += len(b.pending)
	clear(b.pending)
	clear(b.batch)
	clear(b.queued)
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timer, b.timerAt = nil, time.Time{}
	b.noMainSince, b.noMainLogged = time.Time{}, time.Time{}
	b.mu.Unlock()

	r := &s.relays
	r.mu.Lock()
	if s.relayLocked(ctx) {
		out.Pending += len(r.st.SupervisePending)
		changed := r.st.SupervisePending != nil
		r.st.SupervisePending = nil
		for to, queue := range r.st.Queue {
			kept := make([]relayMsg, 0, len(queue))
			for i, m := range queue {
				switch {
				case m.Tool != superviseTool:
					kept = append(kept, m)
				case i == 0 && r.draining[to] || m.Withdrawn || !m.DoubtedAt.IsZero():
					// A send in flight (or whose answer was lost) still owns its outcome: keep the head so a
					// late-visible line is recognised, but with nothing left to replay.
					if empty := encodeDigestSignals([]superviseSignal{}); !m.Withdrawn || m.Signals != empty {
						m.Withdrawn, m.Signals = true, empty
						changed = true
					}
					kept = append(kept, m)
				default:
					out.Queued++
					changed = true
				}
			}
			if len(kept) == 0 {
				delete(r.st.Queue, to)
			} else {
				r.st.Queue[to] = kept
			}
		}
		off := &s.supervise.off
		off.mu.Lock()
		unsaved := off.unsave
		off.mu.Unlock()
		if changed || unsaved {
			err := s.saveRelayLocked()
			off.mu.Lock()
			off.unsave = err != nil
			off.mu.Unlock()
		}
	}
	r.mu.Unlock()

	// Failed starts held for a cooldown would come back on the first digest after the switch is turned on again. Only
	// the first withdrawal reads that document: nothing adds to it while the switch is off.
	if !deferred {
		return out
	}
	if err := s.updateFailedStartBatches(ctx, func(doc *failedStartBatchDoc) (bool, error) {
		changed := false
		for _, batch := range doc.Batches {
			if n := len(batch.Deferred); n > 0 {
				out.Deferred += n
				batch.Deferred = nil
				changed = true
			}
		}
		return changed, nil
	}); err != nil {
		s.logf("supervise: could not clear the deferred failed starts: %v", err)
	}
	return out
}

// cancelNodeSupervision asks the nodes to cancel the supervision input their conversations still hold unstarted
// (assistant.cancel_supervision). Given a machine and its session list, it covers those conversations; with none, every
// machine the assistant uses. It only reaches claude conversations with a waiting queue, and a turn that has started is
// never taken back.
func (s *Service) cancelNodeSupervision(ctx context.Context, machine string, list []proto.Session) {
	if machine != "" {
		s.cancelNodeSupervisionOn(ctx, machine, list)
		return
	}
	v := s.settings(ctx)
	seen := map[string]bool{}
	for _, m := range []string{v.MachineID, v.MainMachineID, v.BackgroundSupervisorMachineID} {
		if m == "" || seen[m] {
			continue
		}
		seen[m] = true
		sessions, err := s.sessions(ctx, m)
		if err != nil {
			continue
		}
		s.cancelNodeSupervisionOn(ctx, m, sessions)
	}
}

func (s *Service) cancelNodeSupervisionOn(ctx context.Context, machine string, list []proto.Session) {
	off := &s.supervise.off
	now := time.Now()
	for _, c := range list {
		if !c.Assistant || len(c.Queued) == 0 {
			continue
		}
		key := machine + "|" + c.SessionID
		off.mu.Lock()
		if last, ok := off.cancel[key]; ok && now.Sub(last) < nodeCancelEvery {
			off.mu.Unlock()
			continue
		}
		if off.cancel == nil {
			off.cancel = map[string]time.Time{}
		}
		off.cancel[key] = now
		off.mu.Unlock()
		var res struct {
			Cancelled int `json:"cancelled"`
		}
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		err := s.call(cctx, machine, deviceAssistantCancelSupervision, map[string]any{"session_id": c.SessionID}, &res)
		cancel()
		switch {
		case err != nil:
			// An older node does not know the method: its waiting input then goes in as it always did.
			s.logf("supervise: could not cancel the waiting supervision input of %s: %v", short(c.SessionID), err)
		case res.Cancelled > 0:
			s.logf("supervise: cancelled %d waiting supervision input(s) in %s", res.Cancelled, short(c.SessionID))
		}
	}
}

// deviceAssistantCancelSupervision is the node method that cancels a conversation's supervision input that waits
// unstarted in its queue, by its structured message source (docs/protocol/protocol.md).
const deviceAssistantCancelSupervision = "assistant.cancel_supervision"
