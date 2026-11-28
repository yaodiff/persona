package projection

import (
	"cmp"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/wireids"
)

// Wait is the strongly-typed waiting reason of to-do f at now (#16168): non-nil exactly when the projection places f
// in TodoPhaseWaiting, and nil otherwise. Clients render it directly without deriving waiting state themselves.
func Wait(f Facts, now time.Time) *assistantwire.TodoWait {
	return cloneWait(place(&f, now).wait)
}

func cloneWait(w *assistantwire.TodoWait) *assistantwire.TodoWait {
	if w == nil {
		return nil
	}
	out := *w
	if w.Contact != nil {
		c := *w.Contact
		out.Contact = &c
	}
	if len(w.PrBlockers) > 0 {
		out.PrBlockers = append([]assistantwire.TodoWaitPRBlocker(nil), w.PrBlockers...)
	}
	return &out
}

func buildParkedWait(sv *Supervision) *assistantwire.TodoWait {
	pk := sv.Parked
	if pk == nil {
		return nil
	}
	w := &assistantwire.TodoWait{
		Kind:   parkedWaitKind(pk),
		Reason: strings.TrimSpace(pk.Reason),
		Until:  utc(pk.Until),
	}
	if w.Kind == assistantwire.TodoWaitKindDate && !pk.Until.IsZero() {
		w.WakeAt = utc(pk.Until)
	}
	if pk.Handoff != nil {
		w.WaitingOn = strings.TrimSpace(pk.Handoff.Title)
		w.WakeCondition = strings.TrimSpace(pk.Handoff.Check)
		if id := strings.TrimSpace(pk.Handoff.TodoID.String()); id != "" {
			w.HandoffTodoID = pk.Handoff.TodoID
		}
	} else if ev := pk.Evidence; ev != nil {
		w.WaitingOn = strings.TrimSpace(ev.Target)
		w.WakeCondition = strings.TrimSpace(ev.Wake)
		w.URL = strings.TrimSpace(ev.URL)
		if c := ev.Contact; c != nil {
			channel := strings.TrimSpace(c.Channel)
			if channel != "" {
				w.Contact = &assistantwire.TodoWaitContact{
					Channel: channel,
					Sender:  strings.TrimSpace(c.Sender),
					SentAt:  utc(c.SentAt),
					URL:     strings.TrimSpace(c.URL),
					Request: strings.TrimSpace(c.Request),
				}
				if w.URL == "" {
					w.URL = w.Contact.URL
				}
				if !c.SentAt.IsZero() {
					w.Since = utc(c.SentAt)
				}
			}
		}
	}
	if w.Since.IsZero() {
		if rv := latestReview(sv); rv != nil && rv.Verdict == reviewParked && !rv.At.IsZero() {
			w.Since = utc(rv.At)
		}
	}
	return w
}

func parkedWaitKind(pk *Parked) assistantwire.TodoWaitKind {
	if pk.Handoff != nil || pk.WaitingOn == parkTodoHandoff {
		return assistantwire.TodoWaitKindTodoHandoff
	}
	if pk.Evidence != nil {
		switch pk.Evidence.Kind {
		case parkEvidencePersonReply:
			return assistantwire.TodoWaitKindPersonReply
		case parkEvidenceMergeReview:
			return assistantwire.TodoWaitKindMergeReview
		case parkEvidenceExternalEvent:
			return assistantwire.TodoWaitKindExternalEvent
		case parkEvidenceDate:
			return assistantwire.TodoWaitKindDate
		}
	}
	switch pk.WaitingOn {
	case parkSomeoneElse:
		return assistantwire.TodoWaitKindPersonReply
	case parkExternalEvent:
		return assistantwire.TodoWaitKindExternalEvent
	case parkDate:
		return assistantwire.TodoWaitKindDate
	default:
		return assistantwire.TodoWaitKindParked
	}
}

// runtimeWait reads the structured wait of an open task. A leftover provider deploy wait from before the
// completion mode was honored has already met a landed contract once its delivery is merged.
func (r *reading) runtimeWait() *assistantwire.TaskWaitState {
	f, w := r.f, r.f.WaitState
	if f.Phase != assistantwire.TaskPhaseWaiting || w == nil || !w.WaitingOn.Valid() ||
		acceptanceStands(f) || reviewState(f) == reviewPending || reviewState(f) == reviewUserCheck {
		return nil
	}
	if f.ObservedWait && f.CompletionModeNow == assistantwire.CompletionModeLanded &&
		w.WaitingOn == assistantwire.WaitExternal && w.Target.Kind == assistantwire.WaitTargetPR &&
		f.Delivery != nil && f.Delivery.State == deliveryMerged {
		return nil
	}
	return w
}

func buildRuntimeWait(w *assistantwire.TaskWaitState, at time.Time) *assistantwire.TodoWait {
	ref := strings.TrimSpace(w.Target.Ref)
	out := &assistantwire.TodoWait{
		Kind:      assistantwire.TodoWaitKindExternalEvent,
		WaitingOn: cmp.Or(firstLine(w.Target.Summary), ref),
		Reason:    firstLine(w.Reason),
		Until:     utc(w.Until),
		Since:     utc(at),
	}
	switch w.WaitingOn {
	case assistantwire.WaitUser:
		out.Kind = assistantwire.TodoWaitKindPersonReply
	case assistantwire.WaitDate:
		out.Kind = assistantwire.TodoWaitKindDate
		out.WakeAt = out.Until
	case assistantwire.WaitDependency:
		if w.Target.Kind == assistantwire.WaitTargetTodo {
			out.Kind = assistantwire.TodoWaitKindTodoHandoff
			out.HandoffTodoID = wireids.TodoID(ref)
			out.WaitingOn = firstLine(w.Target.Summary)
		}
	}
	switch w.Target.Kind {
	case assistantwire.WaitTargetPR, assistantwire.WaitTargetIssue, assistantwire.WaitTargetChat, assistantwire.WaitTargetURL:
		if u, err := url.Parse(ref); err == nil && u.Host != "" && u.User == nil && (u.Scheme == "https" || u.Scheme == "http") {
			out.URL = ref
		}
	}
	if w.Target.Kind == assistantwire.WaitTargetPR {
		out.WaitingOn = cmp.Or(firstLine(w.Target.Summary), shortPRLabel(out.URL), ref)
	}
	return out
}

// RepoGateWait is Facts.RepoGate for the open delivery PR at prURL holding repository's gate, blocked by blocker ("" when
// it names none).
func RepoGateWait(repository, prURL string, blocker assistantwire.TodoWaitPRBlocker) *assistantwire.TodoWait {
	w := &assistantwire.TodoWait{
		Kind:      assistantwire.TodoWaitKindPrMerge,
		WaitingOn: cmp.Or(shortPRLabel(prURL), repository),
		URL:       prURL,
	}
	if blocker != "" {
		w.PrBlockers = []assistantwire.TodoWaitPRBlocker{blocker}
	}
	return w
}

// copyWait is a row's own copy of a wait the facts carry, so the row never shares the facts' value.
func copyWait(w assistantwire.TodoWait) *assistantwire.TodoWait {
	w.PrBlockers = slices.Clone(w.PrBlockers)
	return &w
}

func buildPRWait(pr PRStatus) *assistantwire.TodoWait {
	target := shortPRLabel(pr.URL)
	title := strings.TrimSpace(pr.Title)
	reason := ""
	if target == "" {
		target = title
	} else if title != "" && title != target {
		reason = title
	}
	var blockers []assistantwire.TodoWaitPRBlocker
	if pr.Conflicts {
		blockers = append(blockers, assistantwire.TodoWaitPRBlockerConflicts)
	}
	switch pr.Review {
	case "changes_requested":
		blockers = append(blockers, assistantwire.TodoWaitPRBlockerChangesRequested)
	case "required", "review_required":
		blockers = append(blockers, assistantwire.TodoWaitPRBlockerReviewRequired)
	}
	switch pr.Checks {
	case "failure", "failing":
		blockers = append(blockers, assistantwire.TodoWaitPRBlockerChecksFailing)
	case "pending":
		blockers = append(blockers, assistantwire.TodoWaitPRBlockerChecksPending)
	}
	return &assistantwire.TodoWait{
		Kind:       assistantwire.TodoWaitKindPrMerge,
		WaitingOn:  target,
		Reason:     reason,
		URL:        strings.TrimSpace(pr.URL),
		PrBlockers: blockers,
	}
}

func shortPRLabel(raw string) string {
	clean := strings.TrimSpace(raw)
	if idx := strings.IndexAny(clean, "#?"); idx >= 0 {
		clean = clean[:idx]
	}
	sep := strings.Index(clean, "://")
	if sep < 0 {
		return ""
	}
	rest := clean[sep+3:]
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return ""
	}
	path := strings.Trim(rest[slash:], "/")
	if path == "" {
		return ""
	}
	segs := strings.Split(path, "/")
	if len(segs) < 4 {
		return ""
	}
	n, err := strconv.Atoi(segs[len(segs)-1])
	if err != nil || n <= 0 {
		return ""
	}
	if segs[len(segs)-2] == "pull" {
		return fmt.Sprintf("PR #%d", n)
	}
	if len(segs) >= 5 && segs[len(segs)-3] == "-" && segs[len(segs)-2] == "merge_requests" {
		return fmt.Sprintf("MR !%d", n)
	}
	return ""
}

func buildUsageLimitWait(rn run) *assistantwire.TodoWait {
	w := &assistantwire.TodoWait{
		Kind:      assistantwire.TodoWaitKindUsageLimit,
		WaitingOn: strings.TrimSpace(rn.title()),
		Since:     utc(rn.since()),
	}
	if rn.s.Work != nil && rn.s.Work.WakeAt != nil && !rn.s.Work.WakeAt.IsZero() {
		w.WakeAt = utc(*rn.s.Work.WakeAt)
	} else if reset := usageLimitReset(*rn.s); reset != nil && !reset.IsZero() {
		w.WakeAt = utc(*reset)
	}
	return w
}

func buildRunWaitsWait(rn run) *assistantwire.TodoWait {
	on := waitsOn(*rn.s)
	work := rn.s.Work
	kind := assistantwire.TodoWaitKindExternalEvent
	if on == proto.WaitAgent {
		kind = assistantwire.TodoWaitKindAgent
	}
	w := &assistantwire.TodoWait{
		Kind:  kind,
		Since: utc(rn.since()),
	}
	if work != nil {
		if on == proto.WaitAgent && work.Agent != nil {
			w.WaitingOn = strings.TrimSpace(work.Agent.Label)
		}
		if w.WaitingOn == "" && work.CI != nil && work.CI.Number > 0 {
			w.WaitingOn = fmt.Sprintf("PR #%d", work.CI.Number)
		}
		w.Reason = strings.TrimSpace(work.Wait)
		if w.Reason == "" {
			w.Reason = strings.TrimSpace(work.Line)
		}
		w.WakeCondition = strings.TrimSpace(work.Then)
		if work.WakeAt != nil && !work.WakeAt.IsZero() {
			w.WakeAt = utc(*work.WakeAt)
		}
		if work.CI != nil {
			if !work.CI.FallbackAt.IsZero() {
				w.Until = utc(work.CI.FallbackAt)
			}
			if repo := strings.Trim(strings.TrimSpace(work.CI.Repo), "/"); repo != "" && work.CI.Number > 0 {
				w.URL = fmt.Sprintf("https://github.com/%s/pull/%d", repo, work.CI.Number)
			}
		}
	}
	return w
}
