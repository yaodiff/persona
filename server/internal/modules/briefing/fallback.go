package briefing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/modapi"
)

// StartReport keeps a Report's ordinary model input unchanged (north-star lines only), while retaining the full fixed
// snapshot for a possible model-failure fallback. Sent attaches it without a normal feed item, Claim and Push ignore it
// on success, so a successful Report has exactly its old card/feed/push behavior.
func (r *runner) StartReport(ctx context.Context, task modapi.BriefTaskInfo, now time.Time) string {
	run := &modapi.BriefRun{Facts: r.FactsAt(ctx, now, task.ID), Label: task.Label, FallbackOnly: true}
	run.Facts.Contributions = task.Contributions
	r.SetTaskRun(task.ID, run)
	if !run.Facts.NorthStar.HasStore || r.prompts == nil {
		return ""
	}
	return r.prompts.RenderNorthStarFacts(run.Facts.NorthStarLines)
}

// IsFailure says this scheduled turn has full fixed facts whose model answer failed. Its ordinary error mirror and
// finished push are suppressed; watchScheduledRun sends the one durable fallback instead.
func (r *runner) IsFailure(session, asked string, cut bool) bool {
	if !cut || r.of(session) == nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(r.stripContext(asked)), "⏰")
}

// FallbackKey is the durable receipt key for one routine occurrence.
func FallbackKey(refID string, due time.Time) string {
	return refID + "@" + due.UTC().Format(time.RFC3339Nano)
}

// FallbackID is stable across run IDs and retries: it names the routine and the one scheduled occurrence.
func FallbackID(refID string, due time.Time) string {
	sum := sha256.Sum256([]byte(refID + "\x00" + due.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:16])
}

// Fallback is the one notification path for a failed Facts/Report turn. Retry failures and a new hub on the same store
// see the same routine+due receipt. A later successful retry still uses briefMirror/briefPush normally and does not
// retract this card.
func (r *runner) Fallback(ctx context.Context, scheduled modapi.BriefScheduleRun, task modapi.BriefTaskInfo, device modapi.BriefDevice) {
	r.fallbackRun(ctx, scheduled, task, device, r.of(scheduled.SessionID))
}

// fallbackRun delivers one retained snapshot. A started turn finds it by session; a run that failed before its
// conversation started passes the by-task snapshot directly. Both consume the same routine+due receipt.
func (r *runner) fallbackRun(ctx context.Context, scheduled modapi.BriefScheduleRun, task modapi.BriefTaskInfo, device modapi.BriefDevice, run *modapi.BriefRun) {
	if !task.Facts && !task.Report {
		return
	}
	if run == nil {
		r.logf("task %s: model failed but its briefing facts are no longer available", task.ID)
		return
	}
	if r.schedule == nil || !r.schedule.ClaimFallbackReceipt(ctx, FallbackKey(scheduled.RefID, scheduled.Due)) {
		return
	}
	header := ""
	if r.prompts != nil {
		header = r.prompts.Translate(run.Facts.Lang, "assistant-briefing.fallback_header", nil)
	}
	id := FallbackID(scheduled.RefID, scheduled.Due)

	// The app item has its own deterministic ID, so even a caller retried within this process cannot append it twice.
	body := r.Body(run.Facts, run.Label)
	body.Notice = header
	if r.feed != nil {
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if err := r.feed.AppendOnce(fctx, "feed_brief_fallback_"+id, scheduled.SessionID, modapi.FeedKindBrief, scheduled.RefID, body); err != nil {
			r.logf("briefing fallback feed %s: %v", task.ID, err)
		} else {
			r.supervisionBriefed(fctx, run.Facts)
		}
		cancel()
	}

	if r.delivery != nil {
		r.delivery.SendFallback(ctx, modapi.BriefFallbackSend{
			Run:    scheduled,
			Task:   task,
			Device: device,
			Label:  run.Label,
			Header: header,
			ID:     id,
			Facts:  run.Facts,
		})
	}
}

// NotStarted covers model resolution and conversation-start failures, where no watcher exists. The fixed facts go to
// the task's conversation, or main when this run intended to create one. A Facts/Report retry keeps the original due;
// ordinary tasks never enter this path and retain oneOffNotStarted's old failure notification and retry behavior.
func (r *runner) NotStarted(ctx context.Context, task modapi.BriefTaskInfo, failed modapi.BriefScheduleRun, retry bool) {
	scheduled := failed
	if r.schedule != nil {
		if sid, mid, mname, ok := r.schedule.ResolveFallbackTarget(ctx, task, failed); ok {
			scheduled.SessionID = sid
			scheduled.MachineID = mid
			r.fallbackRun(ctx, scheduled, task, modapi.BriefDevice{ID: mid, Name: mname}, r.ofTask(task.ID))
		} else {
			r.logf("task %s: model failed before a conversation started and no device can deliver its briefing facts", task.ID)
		}
	}
	if !retry || failed.Retried || failed.Manual || r.schedule == nil {
		return
	}
	r.schedule.RetryNotStarted(task.ID, failed)
}
