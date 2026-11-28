package assistant

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

// A broadcast by conversation_send (11-23): the main conversation sent the same round to 10 leader conversations three
// times in 12 minutes, and all 30 woke. A sender that wakes fanoutWide or more conversations within fanoutWindow has
// the ones past that kept for their receivers (relay_later.go) with a durable deferred wake (#14352): each goes to the
// back of the sender's wake queue and still counts against the cap when it wakes, so a broadcast stays spread out over
// time instead of firing at once. If the receiver wakes for any other reason first, it reads the kept messages then and
// the deferred wake is dropped (no double wake). Explicit wake: false messages are never woken later. A conversation it
// already woke in the window, and a quote of the owner's request (which must wake), still go in immediately.
const fanoutWide = 3

var (
	// fanoutWindow is the rolling window for per-sender conversation_send wake capping. Var so tests can shorten it.
	fanoutWindow = 10 * time.Minute
	// fanoutInFlightDelay is how long a due deferred wake waits for an in-flight delivery carrying Later to settle.
	fanoutInFlightDelay = 2 * time.Second
)

// fanoutWake is the durable payload of a delayedFanoutWake wait in assistant.delayed_waits (#14352).
type fanoutWake struct {
	Sender    string `json:"sender"`
	Target    string `json:"target"`
	MachineID string `json:"machine_id,omitempty"`
}

type fanoutTimer struct {
	t       *time.Timer
	stopCtx func() bool
}

func (ft *fanoutTimer) stop() {
	if ft == nil {
		return
	}
	if ft.t != nil {
		ft.t.Stop()
	}
	if ft.stopCtx != nil {
		ft.stopCtx()
	}
}

// sendFanout is, per sender, when it last woke each conversation by conversation_send, along with local timers for
// pending deferred wakes (and an in-memory fallback list when no durable store is configured).
//
// Lock order: delayedMu -> relays.mu -> sendFanout.mu (sendFanout.mu is always a leaf lock).
type sendFanout struct {
	mu      sync.Mutex
	woken   map[string]map[string]time.Time
	timers  map[string]*fanoutTimer
	pending map[string]delayedWait
}

// admit says whether sender may wake target at now: yes when target is one it woke within the window, or when the
// conversations it woke within the window, with target, are fewer than fanoutWide. A yes is recorded.
func (f *sendFanout) admit(sender, target string, now time.Time) bool {
	ok, _, _, _ := f.admitReturningPrev(sender, target, now)
	return ok
}

// admitReturningPrev is admit plus the prior recorded timestamp (for rollback on a failed save) and, when rejected,
// the earliest time at which sender's window has room for another distinct target.
func (f *sendFanout) admitReturningPrev(sender, target string, now time.Time) (admitted bool, prev time.Time, hadPrev bool, nextAt time.Time) {
	now = now.UTC()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.woken == nil {
		f.woken = map[string]map[string]time.Time{}
	}
	seen := f.woken[sender]
	for id, at := range seen {
		if now.Sub(at) >= fanoutWindow {
			delete(seen, id)
		}
	}
	if old, again := seen[target]; again {
		seen[target] = now
		return true, old, true, time.Time{}
	}
	if len(seen)+1 >= fanoutWide {
		return false, time.Time{}, false, earliestFanoutSlot(seen, now, now)
	}
	if seen == nil {
		seen = map[string]time.Time{}
		f.woken[sender] = seen
	}
	seen[target] = now
	return true, time.Time{}, false, time.Time{}
}

// restore undoes an admission recorded at at if no newer admission replaced it.
func (f *sendFanout) restore(sender, target string, prev time.Time, hadPrev bool, at time.Time) {
	at = at.UTC()
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := f.woken[sender]
	if cur, ok := seen[target]; !ok || !cur.Equal(at) {
		return
	}
	if hadPrev {
		seen[target] = prev
	} else {
		delete(seen, target)
		if len(seen) == 0 {
			delete(f.woken, sender)
		}
	}
}

// senderWindow returns a copy of sender's active wake timestamps within fanoutWindow at now.
func (f *sendFanout) senderWindow(sender string, now time.Time) map[string]time.Time {
	now = now.UTC()
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := f.woken[sender]
	if len(seen) == 0 {
		return nil
	}
	out := make(map[string]time.Time, len(seen))
	for id, at := range seen {
		if now.Sub(at) < fanoutWindow {
			out[id] = at
		}
	}
	return out
}

// snapshot returns a deep copy of all active wake timestamps within fanoutWindow at now, for durable relayState saves.
func (f *sendFanout) snapshot(now time.Time) map[string]map[string]time.Time {
	now = now.UTC()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out map[string]map[string]time.Time
	for sender, seen := range f.woken {
		for id, at := range seen {
			if now.Sub(at) >= fanoutWindow {
				delete(seen, id)
				continue
			}
			if out == nil {
				out = map[string]map[string]time.Time{}
			}
			if out[sender] == nil {
				out[sender] = map[string]time.Time{}
			}
			out[sender][id] = at
		}
		if len(seen) == 0 {
			delete(f.woken, sender)
		}
	}
	return out
}

// merge hydrates f.woken with persisted wake timestamps within fanoutWindow at now, keeping whichever timestamp is newer.
func (f *sendFanout) merge(saved map[string]map[string]time.Time, now time.Time) {
	if len(saved) == 0 {
		return
	}
	now = now.UTC()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.woken == nil {
		f.woken = map[string]map[string]time.Time{}
	}
	for sender, seen := range saved {
		for id, at := range seen {
			at = at.UTC()
			if now.Sub(at) >= fanoutWindow {
				continue
			}
			cur := f.woken[sender]
			if cur == nil {
				cur = map[string]time.Time{}
				f.woken[sender] = cur
			}
			if prev, ok := cur[id]; !ok || at.After(prev) {
				cur[id] = at
			}
		}
	}
}

func (f *sendFanout) clearTimer(id string, entry *fanoutTimer) {
	f.mu.Lock()
	if f.timers[id] == entry {
		delete(f.timers, id)
	}
	stopCtx := entry.stopCtx
	f.mu.Unlock()
	if stopCtx != nil {
		stopCtx()
	}
}

func (f *sendFanout) stopTimer(id string) {
	f.mu.Lock()
	entry := f.timers[id]
	delete(f.timers, id)
	f.mu.Unlock()
	entry.stop()
}

func (f *sendFanout) stopAllTimers() {
	f.mu.Lock()
	timers := f.timers
	f.timers = nil
	f.mu.Unlock()
	for _, entry := range timers {
		entry.stop()
	}
}

// earliestFanoutSlot returns the earliest time >= max(start, now) when fewer than fanoutWide-1 entries in active remain
// within fanoutWindow.
func earliestFanoutSlot(active map[string]time.Time, start, now time.Time) time.Time {
	if start.Before(now) {
		start = now
	}
	capLimit := fanoutWide - 1
	if capLimit <= 0 {
		return start.Add(fanoutWindow)
	}
	var ts []time.Time
	for _, at := range active {
		if start.Sub(at) < fanoutWindow {
			ts = append(ts, at)
		}
	}
	if len(ts) < capLimit {
		return start
	}
	slices.SortFunc(ts, time.Time.Compare)
	t := ts[len(ts)-capLimit].Add(fanoutWindow)
	if t.Before(start) {
		return start
	}
	return t
}

// nextFanoutSlot computes the earliest time >= now (and at or after every considered pending deferred wake for sender,
// so new wakes go to the back of the queue) when sender's rolling fanoutWindow has room for another distinct target.
// excludeID omits a wait that is itself being rescheduled; when beforeAt is non-zero, only waits ahead of (beforeAt,
// excludeID) are counted so a slight timer jitter never pushes an earlier wait behind a later wave.
func nextFanoutSlot(actual map[string]time.Time, waits []delayedWait, sender, excludeID string, beforeAt, now time.Time) time.Time {
	now = now.UTC()
	type queuedWake struct {
		target string
		at     time.Time
	}
	var queued []queuedWake
	for _, w := range waits {
		if w.ID == excludeID || w.Kind != delayedFanoutWake || w.Fanout == nil || w.Fanout.Sender != sender {
			continue
		}
		if !beforeAt.IsZero() && (w.At.After(beforeAt) || (w.At.Equal(beforeAt) && w.ID > excludeID)) {
			continue
		}
		// A claimed wait whose target is already recorded in actual has already consumed its slot.
		if w.Claim != "" {
			if _, inActual := actual[w.Fanout.Target]; inActual {
				continue
			}
		}
		queued = append(queued, queuedWake{target: w.Fanout.Target, at: w.At.UTC()})
	}
	slices.SortStableFunc(queued, func(a, b queuedWake) int { return a.at.Compare(b.at) })

	active := make(map[string]time.Time, len(actual)+len(queued))
	for id, at := range actual {
		if now.Sub(at) < fanoutWindow {
			active[id] = at.UTC()
		}
	}
	start := now
	for _, q := range queued {
		at := q.at
		if at.Before(start) {
			at = start
		}
		t := earliestFanoutSlot(active, at, now)
		for id, seenAt := range active {
			if t.Sub(seenAt) >= fanoutWindow {
				delete(active, id)
			}
		}
		active[q.target] = t
		start = t
	}
	return earliestFanoutSlot(active, start, now)
}

// fanoutKeptAnswer formats conversation_send's reply when a wake-intended message is held by the per-sender wake cap.
func fanoutKeptAnswer(name string, kept int, wait time.Duration) string {
	mins := max(1, int(math.Ceil(wait.Minutes())))
	unit := "minutes"
	if mins == 1 {
		unit = "minute"
	}
	winMins := max(1, int(math.Ceil(fanoutWindow.Minutes())))
	return fmt.Sprintf("Kept for %q (%d kept there); it will be woken automatically in about %d %s (or sooner if another message wakes it first) — no need to send it again. "+
		// i18n-ignore: a tool answer to the model; the Chinese is how leaders name that rule, kept beside the English.
		"This conversation reached its wake cap (%d conversations in %d minutes); a rule for every leader belongs in LEAD-COMMON (规矩请写进 LEAD-COMMON), not in a message to each; "+
		"to wake it immediately, quote the owner's request.",
		name, kept, mins, unit, fanoutWide-1, winMins)
}

// ensureFanoutLoaded hydrates sendFanout from durable relayState on first use after startup.
func (s *Service) ensureFanoutLoaded(ctx context.Context) {
	b := &s.relays
	b.mu.Lock()
	if !b.loaded {
		s.relayLocked(ctx)
	}
	b.mu.Unlock()
}

// admitFanout checks the per-sender wake cap after ensuring durable fanout state is loaded.
func (s *Service) admitFanout(ctx context.Context, sender, target string, now time.Time) (bool, time.Time, bool) {
	s.ensureFanoutLoaded(ctx)
	ok, prev, hadPrev, _ := s.sendFanout.admitReturningPrev(sender, target, now)
	return ok, prev, hadPrev
}

// scheduleFanoutWake persists (when a store is configured) and arms a deferred wake for (sender, target), returning the
// scheduled wake time. If an unclaimed deferred wake already exists for the same (sender, target), it is reused.
func (s *Service) scheduleFanoutWake(ctx context.Context, sender, target, machine string, now time.Time) time.Time {
	now = now.UTC()
	if s.store() != nil {
		w, err := s.persistFanoutWake(ctx, sender, target, machine, now)
		if err == nil {
			s.armFanoutWake(w, true)
			return w.At
		}
		s.logf("persist deferred fanout wake %s -> %s: %v", short(sender), short(target), err)
		at := nextFanoutSlot(s.sendFanout.senderWindow(sender, now), nil, sender, "", time.Time{}, now)
		fallback := delayedWait{
			ID:     "fanout_" + randomHex(12),
			Kind:   delayedFanoutWake,
			At:     at.UTC(),
			Fanout: &fanoutWake{Sender: sender, Target: target, MachineID: machine},
		}
		s.armFanoutWake(fallback, false)
		return fallback.At
	}
	// Storeless fallback (in-memory tests without a Store).
	f := &s.sendFanout
	f.mu.Lock()
	if f.pending == nil {
		f.pending = map[string]delayedWait{}
	}
	for _, existing := range f.pending {
		if existing.Fanout != nil && existing.Fanout.Sender == sender && existing.Fanout.Target == target {
			f.mu.Unlock()
			s.armFanoutWake(existing, false)
			return existing.At
		}
	}
	waits := make([]delayedWait, 0, len(f.pending))
	for _, existing := range f.pending {
		waits = append(waits, existing)
	}
	actual := map[string]time.Time{}
	for id, at := range f.woken[sender] {
		if now.Sub(at) < fanoutWindow {
			actual[id] = at
		}
	}
	at := nextFanoutSlot(actual, waits, sender, "", time.Time{}, now)
	w := delayedWait{
		ID:     "fanout_" + randomHex(12),
		Kind:   delayedFanoutWake,
		At:     at.UTC(),
		Fanout: &fanoutWake{Sender: sender, Target: target, MachineID: machine},
	}
	f.pending[w.ID] = w
	f.mu.Unlock()
	s.armFanoutWake(w, false)
	return w.At
}

// armFanoutWake arms a local timer for w at w.At, stopping any earlier timer for the same wait ID.
func (s *Service) armFanoutWake(w delayedWait, persisted bool) {
	if w.Fanout == nil {
		return
	}
	ctx := s.runCtx()
	if ctx.Err() != nil {
		return
	}
	wait := max(time.Until(w.At), 0)
	f := &s.sendFanout
	f.mu.Lock()
	if prev, ok := f.timers[w.ID]; ok {
		prev.stop()
		delete(f.timers, w.ID)
	}
	if f.timers == nil {
		f.timers = map[string]*fanoutTimer{}
	}
	entry := &fanoutTimer{}
	entry.t = time.AfterFunc(wait, func() {
		f.clearTimer(w.ID, entry)
		s.runFanoutWake(s.runCtx(), w, persisted)
	})
	if ctx.Done() != nil {
		entry.stopCtx = context.AfterFunc(ctx, func() {
			f.stopTimer(w.ID)
		})
	}
	f.timers[w.ID] = entry
	f.mu.Unlock()
}

// runFanoutWake fires at w.At: claims the durable wait, checks that a wake-intended kept message from sender still waits
// for target, enforces the per-sender cap, promotes the message(s) to the back of target's relay queue, and drains it.
func (s *Service) runFanoutWake(ctx context.Context, w delayedWait, persisted bool) {
	if ctx.Err() != nil || w.Fanout == nil {
		return
	}
	durable := persisted || s.store() != nil
	if durable && !persisted {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		saved, err := s.persistFanoutWakeExisting(pctx, w)
		cancel()
		if err != nil {
			s.rearmFanoutWake(ctx, w, false, err)
			return
		}
		w, persisted = saved, true
		if w.At.After(time.Now()) {
			s.armFanoutWake(w, true)
			return
		}
	}
	token := ""
	if durable {
		token = randomHex(16)
		claimed, ok, err := s.claimDelayedWait(ctx, w.ID, token, time.Now())
		if err != nil {
			s.rearmFanoutWake(ctx, w, true, err)
			return
		}
		if !ok {
			// Guard against sub-millisecond wall-clock jitter or a concurrent reschedule moving At later.
			if claimed.ID == w.ID && claimed.Claim == "" && !claimed.Unknown && claimed.At.After(time.Now()) {
				s.armFanoutWake(claimed, true)
			}
			return
		}
		w = claimed
	} else {
		f := &s.sendFanout
		f.mu.Lock()
		_, stillPending := f.pending[w.ID]
		f.mu.Unlock()
		if !stillPending {
			return
		}
	}

	complete := func() {
		if durable {
			if err := s.completeDelayedWait(context.Background(), w.ID, token); err != nil {
				s.logf("finish deferred fanout wake %s: %v", w.ID, err)
			}
			return
		}
		f := &s.sendFanout
		f.mu.Lock()
		delete(f.pending, w.ID)
		f.mu.Unlock()
	}

	retryAt := func(minAt time.Time, useQueue bool) {
		if !minAt.After(time.Now()) {
			minAt = time.Now().Add(time.Millisecond)
		}
		if durable {
			s.releaseFanoutWake(ctx, w, token, minAt, useQueue)
			return
		}
		f := &s.sendFanout
		f.mu.Lock()
		if _, ok := f.pending[w.ID]; !ok {
			f.mu.Unlock()
			return
		}
		nextAt := minAt
		if useQueue {
			waits := make([]delayedWait, 0, len(f.pending))
			for _, existing := range f.pending {
				waits = append(waits, existing)
			}
			actual := map[string]time.Time{}
			now := time.Now().UTC()
			for id, at := range f.woken[w.Fanout.Sender] {
				if now.Sub(at) < fanoutWindow {
					actual[id] = at
				}
			}
			if slot := nextFanoutSlot(actual, waits, w.Fanout.Sender, w.ID, w.At, now); slot.After(nextAt) {
				nextAt = slot
			}
		}
		w.At = nextAt.UTC()
		f.pending[w.ID] = w
		f.mu.Unlock()
		s.armFanoutWake(w, false)
	}

	sender, target := w.Fanout.Sender, w.Fanout.Target
	if err := s.conversationIDAvailable(ctx, target); err != nil {
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == "conversation_deleted" {
			complete()
			return
		}
		retryAt(delayedWaitRetryAt(), false)
		return
	}

	promoted, waitUntil, useQueue, transient := s.promoteFanoutWake(ctx, sender, target, time.Now())
	if transient {
		retryAt(waitUntil, useQueue)
		return
	}
	complete()
	if !promoted {
		return
	}
	machine := w.Fanout.MachineID
	if machine == "" {
		machine = s.settings(ctx).MachineID
	}
	if machine != "" {
		s.unarchiveOnSend(machine, target)
	}
	s.publish(context.WithoutCancel(ctx))
	go s.releaseHeldAsks(target)
	if machine != "" && !s.drainConversations(machine, []string{target}) {
		s.drainAfter(machine, target, relayRetryGap)
	}
}

// promoteFanoutWake atomically verifies that at least one DeferredWake message from sender still waits in Later[target],
// checks the per-sender fanout cap, moves sender's DeferredWake message(s) out of Later[target] into the back of
// Queue[target], and saves relayState in one write.
func (s *Service) promoteFanoutWake(ctx context.Context, sender, target string, now time.Time) (promoted bool, retryAt time.Time, useQueue bool, transient bool) {
	now = now.UTC()
	route := s.relayRouteFor(ctx, sender)
	route.queue = true

	b := &s.relays
	b.mu.Lock()
	if !s.relayLocked(ctx) {
		b.mu.Unlock()
		return false, delayedWaitRetryAt(), false, true
	}
	s.pruneRelayLaterLocked(now)
	q := b.st.Later[target]
	var matched []relayMsg
	var rest []relayMsg
	for _, m := range q {
		if m.DeferredWake && m.From == sender {
			matched = append(matched, m)
		} else {
			rest = append(rest, m)
		}
	}
	if len(matched) == 0 {
		b.mu.Unlock()
		return false, time.Time{}, false, false
	}
	// If another delivery into target is in flight right now, it may already have reserved Later[target]. Wait for it
	// to settle before deciding whether a wake is still needed so we never double-wake.
	if b.draining[target] || s.hasInFlightOwnerLaterLocked(target, now) {
		b.mu.Unlock()
		return false, now.Add(fanoutInFlightDelay), false, true
	}
	admitted, prevFanout, hadPrevFanout, nextSlot := s.sendFanout.admitReturningPrev(sender, target, now)
	if !admitted {
		b.mu.Unlock()
		return false, nextSlot, true, true
	}
	// Check relay hops, round trips, and per-pair/per-sender rate while holding b.mu so rollback is atomic on error.
	prevHop, hadHop := b.st.Hops[sender]
	sentKey := [2]string{sender, target}
	prevSent := slices.Clone(b.sent[sentKey])
	adm, err := s.relayAdmitLocked(route, sender, target, now, false)
	if err != nil {
		s.sendFanout.restore(sender, target, prevFanout, hadPrevFanout, now)
		if strings.Contains(err.Error(), "already wait for the rate") {
			b.mu.Unlock()
			return false, delayedWaitRetryAt(), false, true
		}
		// Permanent relay refusal (hop cap or round-trip cut): keep the messages quietly in Later without retrying
		// deferred wakes forever.
		for i := range q {
			if q[i].DeferredWake && q[i].From == sender {
				q[i].DeferredWake = false
			}
		}
		b.st.Later[target] = q
		if saveErr := s.saveRelayLocked(); saveErr != nil {
			b.loaded = false
		}
		b.mu.Unlock()
		s.logf("deferred fanout wake %s -> %s refused by relay limits: %v", short(sender), short(target), err)
		return false, time.Time{}, false, false
	}

	// Combine any multiple wake-intended kept messages from the same sender into one relay in chronological order.
	parts := make([]string, 0, len(matched))
	title := matched[0].FromTitle
	for _, m := range matched {
		if m.FromTitle != "" {
			title = m.FromTitle
		}
		parts = append(parts, m.Text)
	}
	msg := relayMsg{
		From:      sender,
		FromTitle: title,
		Tool:      "conversation_send",
		Text:      strings.Join(parts, "\n\n"),
		Hop:       adm.hop,
		Answer:    adm.answer,
		At:        matched[0].At,
		QueuedAt:  now,
		Depth:     len(b.st.Queue[target]) + 1,
	}
	if !adm.due.IsZero() {
		msg.Due = adm.due.UTC()
	}

	prevLater := b.st.Later[target]
	prevQueue, hadQueue := b.st.Queue[target]
	if len(rest) == 0 {
		delete(b.st.Later, target)
	} else {
		b.st.Later[target] = rest
	}
	b.st.Queue[target] = append(slices.Clone(prevQueue), msg)
	if err := s.saveRelayLocked(); err != nil {
		// Roll back every in-memory mutation so a later retry sees the untouched state.
		b.st.Later[target] = prevLater
		if hadQueue {
			b.st.Queue[target] = prevQueue
		} else {
			delete(b.st.Queue, target)
		}
		if hadHop {
			b.st.Hops[sender] = prevHop
		} else {
			delete(b.st.Hops, sender)
		}
		if len(prevSent) == 0 {
			delete(b.sent, sentKey)
		} else {
			b.sent[sentKey] = prevSent
		}
		s.sendFanout.restore(sender, target, prevFanout, hadPrevFanout, now)
		b.loaded = false
		b.mu.Unlock()
		return false, delayedWaitRetryAt(), false, true
	}
	b.mu.Unlock()
	return true, time.Time{}, false, false
}

func (s *Service) releaseFanoutWake(ctx context.Context, w delayedWait, token string, minAt time.Time, useQueue bool) {
	if ctx.Err() != nil {
		return
	}
	nextAt, released, err := s.releaseFanoutWakeWait(ctx, w, token, minAt, useQueue)
	if err != nil {
		s.logf("retain deferred fanout wake %s: %v", w.ID, err)
		time.AfterFunc(delayedWaitRecoveryDelay, func() { s.releaseFanoutWake(s.runCtx(), w, token, minAt, useQueue) })
		return
	}
	if released {
		w.Claim, w.At = "", nextAt
		s.armFanoutWake(w, true)
	}
}

func (s *Service) rearmFanoutWake(ctx context.Context, w delayedWait, persisted bool, why error) {
	if ctx.Err() != nil {
		return
	}
	s.logf("deferred fanout wake %s: %v", w.ID, why)
	nextAt := delayedWaitRetryAt()
	if persisted || s.store() != nil {
		rescheduled, err := s.rescheduleDelayedWait(ctx, w, nextAt, !persisted)
		if err != nil {
			s.logf("rearm deferred fanout wake %s: %v", w.ID, err)
			time.AfterFunc(delayedWaitRecoveryDelay, func() { s.rearmFanoutWake(s.runCtx(), w, persisted, why) })
			return
		}
		if !rescheduled {
			return
		}
		persisted = true
	}
	w.At = nextAt
	s.armFanoutWake(w, persisted)
}

// hasDeferredWakeLater reports whether target still has an unread DeferredWake message from sender in relayState.Later.
// Lock order: safe to call while holding s.delayedMu (acquires s.relays.mu).
func (s *Service) hasDeferredWakeLater(ctx context.Context, target, sender string) bool {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(ctx) {
		return true // unreadable state: conservative, do not drop a pending wake
	}
	return slices.ContainsFunc(b.st.Later[target], func(m relayMsg) bool {
		return m.DeferredWake && m.From == sender
	})
}

// cancelFanoutWakes drops pending deferred wakes for (sender, target) once an earlier delivery has already consumed
// sender's DeferredWake messages for target. Rechecks Later under delayedMu so a concurrent new send is never dropped.
func (s *Service) cancelFanoutWakes(ctx context.Context, target string, senders []string) {
	if target == "" || len(senders) == 0 {
		return
	}
	if s.store() != nil {
		for _, sender := range senders {
			removed, err := s.cancelFanoutWakeWait(ctx, sender, target, func() bool {
				return s.hasDeferredWakeLater(ctx, target, sender)
			})
			if err != nil {
				s.logf("cancel deferred fanout wake %s -> %s: %v", short(sender), short(target), err)
				continue
			}
			for _, id := range removed {
				s.sendFanout.stopTimer(id)
			}
		}
		return
	}
	// Storeless fallback.
	for _, sender := range senders {
		if s.hasDeferredWakeLater(ctx, target, sender) {
			continue
		}
		var removed []string
		f := &s.sendFanout
		f.mu.Lock()
		for id, w := range f.pending {
			if w.Fanout != nil && w.Fanout.Sender == sender && w.Fanout.Target == target {
				delete(f.pending, id)
				removed = append(removed, id)
			}
		}
		f.mu.Unlock()
		for _, id := range removed {
			f.stopTimer(id)
		}
	}
}
