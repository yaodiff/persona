package assistant

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Under todos_v2 (#14620), pending_review tasks stay in Working (not Needs you) for up to 24 hours so routine
// supervisor reviews do not interrupt the user. To prevent silent starvation before the 24-hour escalation to
// Needs you, the supervision watch sends the task's supervisor one lightweight reminder per review round once the
// wait passes 2 hours. No user push is emitted and no extra database table is used; deduplication reuses the
// supervision signal key (review_nudge@<pending_since>) in superviseBook and the durable relay fingerprint.

const (
	sigReviewNudge             = "review_nudge"
	pendingReviewNudgeAfter    = 2 * time.Hour
	pendingReviewEscalateAfter = 24 * time.Hour
)

func pendingReviewNudgeKey(since time.Time) string {
	return "review_nudge@" + since.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
}

func (s *Service) signalPendingReviewNudge(ctx context.Context, t Todo, agents []proto.Session, lists machineSessions, now time.Time) {
	v := t.Supervision
	if ctx.Err() != nil || v == nil || t.DoneAt != nil || v.state(t) != reviewPending {
		return
	}
	slaHours := s.settings(ctx).todosV2PendingReviewSLAHours()
	if slaHours <= 0 {
		return
	}
	escalateAfter := time.Duration(slaHours) * time.Hour
	nudgeAfter := min(pendingReviewNudgeAfter, escalateAfter/2)
	since := cardlessSince(t)
	if since.IsZero() {
		return
	}
	age := now.Sub(since)
	if age < nudgeAfter || age >= escalateAfter {
		return
	}
	if a, readable, ok := s.askCopyReadable(t.ID); !readable {
		return
	} else if ok && !acceptAskStale(&a, t) && a.AcceptedAt.IsZero() {
		return
	}
	if !cardlessAgentsReachable(t, lists) || !cardlessWorkersStopped(agents) {
		return
	}
	if s.reporterStopped(ctx, t.Origin) {
		return
	}
	s.signal(superviseSignal{
		Todo:    t.ID,
		Kind:    sigReviewNudge,
		Key:     pendingReviewNudgeKey(since),
		Minutes: int(age.Minutes()),
	})
}
