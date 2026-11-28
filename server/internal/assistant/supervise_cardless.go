package assistant

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A supervised task judged finished waits in pending_review for its supervisor's review, and a supervisor's review is
// not the owner's: it never enters the owner's Needs you by itself (docs/assistant/unified-entry.md "Needs you and In
// progress"). When such a review has sat past the settle time with nothing the owner can answer — no needs_user
// conclusion, no accepted user_check, no ask_user card, no pending worker interaction — it can hide indefinitely
// (#12648). The watch catches that state here once per review round and hands the task back to its supervisor. It
// never synthesizes a question from chat text and cannot tell an owner wait from a late review, so the rendered
// message names the whole review vocabulary; the supervisor picks the verdict the work actually needs.

// sigCardlessWait: a pending_review task, or an owner wait held below Needs you, has stayed with no card the owner can
// answer past the settle time.
const sigCardlessWait = "cardless_wait"

const ownerWaitCardlessKeyPrefix = "cardless-wait@"

// cardlessSettle is how long a card-less review waits before the supervisor is told, matching decisionSettle: a card
// being written or an answer on its way is not mistaken for a silent wait.
const cardlessSettle = 10 * time.Minute

// cardlessSince is when the current pending_review round began: the ledger's PendingSince, else its last write.
func cardlessSince(t Todo) time.Time {
	v := t.Supervision
	if v == nil {
		return time.Time{}
	}
	if v.PendingSince != nil {
		return *v.PendingSince
	}
	return v.UpdatedAt
}

// cardlessWorkersStopped says none of the task's active workers still works or holds a card the owner can answer.
// It judges only the sessions it is given; callers must first prove those sessions are complete with
// cardlessAgentsReachable, because taskAgentsIn drops a machine whose session list cannot be read.
func cardlessWorkersStopped(agents []proto.Session) bool {
	for _, c := range agents {
		if c.State == proto.StateNeedsYou || c.PendingInteractions > 0 {
			return false // a real pending card stands for the owner
		}
		if c.State == proto.StateWorking || c.Starting {
			return false // the work goes on; this is not a settled review wait
		}
	}
	return true
}

// cardlessAgentsReachable reports that every machine hosting one of t's active agents answered its session list this
// pass. taskAgentsIn records a failed read in lists but drops that machine's sessions from the slice, so without this
// check a transient device/RPC failure would look like every worker having stopped and spend the round's one
// hand-back (and its CardlessToldAt stamp) on an unknown state. A failed pass simply retries on a later watch tick.
func cardlessAgentsReachable(t Todo, lists machineSessions) bool {
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		if l, ok := lists[a.MachineID]; ok && l.err != nil {
			return false
		}
	}
	return true
}

// cardlessRoundKey is stable across Postgres reloads, which retain microseconds rather than nanoseconds. Legacy
// reviews have no PendingSince: their UpdatedAt changes when stamped, so retain both the original time and the
// exact prospective stamp in the pending claim instead of changing the timestamp the UI uses.
func cardlessRoundKey(t Todo, now time.Time) string {
	since := cardlessSince(t).UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	if t.Supervision.PendingSince == nil {
		return "cardless-legacy@" + since + "@" + now.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	}
	return "cardless@" + since
}

func ownerWaitCardlessAnchor(w *OwnerWait) time.Time {
	if w == nil {
		return time.Time{}
	}
	if !w.Work.IsZero() {
		return w.Work
	}
	return w.At
}

func ownerWaitCardlessKey(w *OwnerWait) string {
	anchor := ownerWaitCardlessAnchor(w)
	if anchor.IsZero() {
		return ""
	}
	return ownerWaitCardlessKeyPrefix + anchor.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
}

func ownerWaitCardlessClaim(g superviseSignal) bool {
	return strings.HasPrefix(g.Key, ownerWaitCardlessKeyPrefix)
}

// cardlessSignal builds the signal for whichever card-less shape the task is in now. The pending-review path keeps its
// original round identity; an owner wait gets its own identity from the work stretch that stopped for the owner.
func cardlessSignal(t Todo, now time.Time) (superviseSignal, bool) {
	v := t.Supervision
	if v == nil || t.DoneAt != nil {
		return superviseSignal{}, false
	}
	if v.state(t) == reviewPending {
		if v.CardlessToldAt != nil {
			return superviseSignal{}, false
		}
		since := cardlessSince(t)
		if since.IsZero() || now.Sub(since) < cardlessSettle {
			return superviseSignal{}, false
		}
		return superviseSignal{
			Todo: t.ID, Kind: sigCardlessWait, Key: cardlessRoundKey(t, now),
			Minutes: int(now.Sub(since).Minutes()),
		}, true
	}
	w := v.Waits
	if w == nil || w.CardlessToldAt != nil || w.At.IsZero() || now.Sub(w.At) < cardlessSettle {
		return superviseSignal{}, false
	}
	if st := v.state(t); st != "" && st != reviewRework {
		return superviseSignal{}, false
	}
	return superviseSignal{
		Todo: t.ID, Kind: sigCardlessWait, Key: ownerWaitCardlessKey(w),
		Minutes: int(now.Sub(w.At).Minutes()), Detail: clip(oneLine(w.Reason), 200),
	}, ownerWaitCardlessKey(w) != ""
}

func legacyCardlessClaim(g superviseSignal) (since, stamp time.Time, ok bool) {
	raw, legacy := strings.CutPrefix(g.Key, "cardless-legacy@")
	if !legacy {
		return
	}
	before, after, separated := strings.Cut(raw, "@")
	since, firstErr := time.Parse(time.RFC3339Nano, before)
	stamp, secondErr := time.Parse(time.RFC3339Nano, after)
	return since, stamp, separated && firstErr == nil && secondErr == nil && !stamp.Before(since)
}

func cardlessClaimMatches(t Todo, g superviseSignal) bool {
	v := t.Supervision
	if v == nil || t.DoneAt != nil {
		return false
	}
	if ownerWaitCardlessClaim(g) {
		st := v.state(t)
		return v.Waits != nil && (st == "" || st == reviewRework) && g.Key == ownerWaitCardlessKey(v.Waits)
	}
	if v.state(t) != reviewPending {
		return false
	}
	if strings.HasPrefix(g.Key, "cardless-legacy@") {
		since, stamp, ok := legacyCardlessClaim(g)
		if !ok || v.PendingSince != nil {
			return false
		}
		if told := v.CardlessToldAt; told != nil {
			return told.Truncate(time.Microsecond).Equal(stamp.Truncate(time.Microsecond))
		}
		return since.Truncate(time.Microsecond).Equal(v.UpdatedAt.Truncate(time.Microsecond))
	}
	// Also accept an older queued key's nanoseconds, without weakening the review-round check.
	at, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(g.Key, "cardless@"))
	if err != nil || !strings.HasPrefix(g.Key, "cardless@") {
		return false
	}
	if v.PendingSince == nil && v.CardlessToldAt != nil {
		// A pending signal from before recoverable claims used the round's pre-stamp UpdatedAt. Keep its existing
		// marker semantics only for that legacy round; every subsequent round has a non-nil PendingSince.
		return !at.After(*v.CardlessToldAt)
	}
	return at.Truncate(time.Microsecond).Equal(cardlessSince(t).Truncate(time.Microsecond))
}

// cardlessClaimToldAt returns the marker belonging to this claim's shape. The two markers deliberately live on
// different state: a pending-review hand-back belongs to its round, while an owner wait belongs to that work stretch.
func cardlessClaimToldAt(v *Supervision, g superviseSignal) (*time.Time, bool) {
	if ownerWaitCardlessClaim(g) {
		if v == nil || v.Waits == nil {
			return nil, false
		}
		return v.Waits.CardlessToldAt, true
	}
	if strings.HasPrefix(g.Key, "cardless@") || strings.HasPrefix(g.Key, "cardless-legacy@") {
		if v == nil {
			return nil, false
		}
		return v.CardlessToldAt, true
	}
	return nil, false
}

func (s *Service) stampCardlessClaimLocked(ctx context.Context, g superviseSignal, stamp time.Time) (Supervision, error) {
	v, _, err := s.updateSupervisionLockedAt(ctx, g.Todo, time.Now().UTC(), func(v *Supervision) bool {
		told, ok := cardlessClaimToldAt(v, g)
		if !ok || told != nil {
			return false
		}
		at := stamp.UTC()
		if ownerWaitCardlessClaim(g) {
			v.Waits.CardlessToldAt = &at
			return true
		}
		if _, claimStamp, legacy := legacyCardlessClaim(g); legacy {
			at = claimStamp
		}
		v.CardlessToldAt = &at
		return true
	})
	return v, err
}

func (s *Service) cardlessAskStops(t Todo, readableRequired bool) (stops, retry bool) {
	if readableRequired {
		a, readable, ok := s.askCopyReadable(t.ID)
		if !readable {
			return false, true
		}
		return ok && !acceptAskStale(&a, t) && a.AcceptedAt.IsZero(), false
	}
	a, ok := s.askCopy(t.ID)
	return ok && !acceptAskStale(&a, t) && a.AcceptedAt.IsZero(), false
}

// cardlessConditions applies the current card and worker gates shared by admission and crash recovery.
func (s *Service) cardlessConditions(t Todo, agents []proto.Session, lists machineSessions, readableAsk bool) (stale, retry bool) {
	if stops, retry := s.cardlessAskStops(t, readableAsk); stops || retry {
		return stops && !retry, retry
	}
	// An unreadable worker is not a stopped worker: hold the claim until its machine answers, rather than treat an
	// unknown state as settled.
	if !cardlessAgentsReachable(t, lists) {
		return false, true
	}
	if !cardlessWorkersStopped(agents) {
		return true, false
	}
	return false, false
}

// commitCardlessClaim is the one write boundary for both card-less shapes. It rechecks the claim after the worker read,
// proves the outbox entry durable, and only then stamps the marker that prevents another delivery.
func (s *Service) commitCardlessClaim(ctx context.Context, g superviseSignal, stamp time.Time, admit bool) (valid, retry bool) {
	s.supervise.writeMu.Lock()
	defer s.supervise.writeMu.Unlock()
	current, ok := s.todoNow(g.Todo)
	if ctx.Err() != nil {
		return false, true
	}
	if !ok || !cardlessClaimMatches(current, g) {
		return false, false
	}
	told, ok := cardlessClaimToldAt(current.Supervision, g)
	if !ok {
		return false, false
	}
	if told != nil {
		return true, false
	}
	if admit {
		// A failed or ambiguously committed marker write retries the same claim, including its prospective legacy
		// stamp. Replacing that identity could strand an earlier committed stamp with a different pending signal.
		r := &s.relays
		r.mu.Lock()
		if s.relayLocked(ctx) {
			if pending, exists := r.st.SupervisePending[g.id()]; exists && cardlessClaimMatches(current, pending) {
				g.Key = pending.Key
			}
		}
		r.mu.Unlock()
		s.queueSignal(g)
	}
	if ctx.Err() != nil || !s.supervisePendingDurably([]superviseSignal{g}) {
		return false, true // no stamp until the pending signal has survived its durable write
	}
	v, err := s.stampCardlessClaimLocked(ctx, g, stamp)
	if err != nil {
		s.logf("task %s: card-less claim: %v (retried from the pending signal)", g.Todo, err)
		return false, true
	}
	told, ok = cardlessClaimToldAt(&v, g)
	return ok && told != nil, false
}

// signalCardlessWait persists the pending hand-back before acknowledging its round or owner wait. A crash or failed
// marker write leaves the outbox able to finish the claim; an old completed stamp without an outbox entry is never
// reconstructed.
func (s *Service) signalCardlessWait(ctx context.Context, t Todo, agents []proto.Session, lists machineSessions, now time.Time) {
	g, ok := cardlessSignal(t, now)
	if ctx.Err() != nil || !ok {
		return
	}
	// The acceptance book is three-state: "no card found" is not "no card exists". Hold off without stamping so a
	// later pass retries once the ledger recovers.
	if stale, retry := s.cardlessConditions(t, agents, lists, true); stale || retry {
		return
	}
	// Match signalReplacing's stop gate. Restore before taking writeMu: restoration can acquire dedupMu, while the
	// digest holds dedupMu when it completes a recovered claim under writeMu.
	if s.reporterStopped(ctx, t.Origin) {
		return
	}
	s.restoreSupervisePending(ctx)
	s.commitCardlessClaim(ctx, g, now, true)
}

// completeCardlessClaim finishes only an existing pending claim. It never guesses whether an old marker without an
// outbox entry was delivered. False/retry keeps transient failures pending instead of acknowledging them as moot.
func (s *Service) completeCardlessClaim(ctx context.Context, g superviseSignal) (valid, retry bool) {
	t, ok := s.todoNow(g.Todo)
	if !ok || !cardlessClaimMatches(t, g) {
		return false, false
	}
	told, known := cardlessClaimToldAt(t.Supervision, g)
	if !known {
		return false, false
	}
	if told != nil {
		return true, false
	}
	if ctx.Err() != nil || !s.supervisePendingDurably([]superviseSignal{g}) {
		return false, true
	}
	if s.reporterStopped(ctx, t.Origin) {
		return false, true
	}
	lists := machineSessions{}
	agents := s.taskAgentsIn(ctx, t, lists)
	stale, retry := s.cardlessConditions(t, agents, lists, ownerWaitCardlessClaim(g))
	if stale || retry {
		return false, retry
	}
	return s.commitCardlessClaim(ctx, g, time.Now().UTC(), false)
}
