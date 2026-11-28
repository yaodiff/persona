package projection

import (
	"cmp"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// What the rules read off the to-do's runs and its other facts: how each linked session stands, the last activity,
// the PR facts, the due day. Ported from the legacy triage (internal/assistant/todo_triage.go, which reads the same
// sessions the way the apps do) and kept apart from it until that copy retires.

// Windows the rules use. Each is half open: the view changes at the window's end, which next_change_at names.
const (
	failedStartFor             = 48 * time.Hour   // row 1: a failed start needs the owner this long after the failure
	justFinishedFor            = 24 * time.Hour   // rows 6 and 9: closed work unseen, or finished work after it was seen
	inactiveAfter              = 48 * time.Hour   // row 13: no activity this long gets the Inactive mark
	usageLimitGrace            = 10 * time.Minute // a usage limit still holds this long past its reset
	retryGrace                 = 5 * time.Minute  // a scheduled retry still counts this long past its wake_at (the hub's wakeGrace)
	prFreshFor                 = 15 * time.Minute // PR facts count this long after they were checked
	pendingReviewEscalateAfter = 24 * time.Hour   // todos_v2: pending_review stays in working this long, then surfaces to needs_you
	canWaitEscalateAfter       = 48 * time.Hour   // todos_v2: non-critical can_wait decisions age into active needs_you after this long
	stoppedFollowupAfter       = time.Hour        // unfinished stops need follow-up only after a full hour
)

// runKind is how one linked run stands, as far as the rules care; the order is the order runReading checks them in.
type runKind int

const (
	runIdle        runKind = iota // stopped and seen, or nothing the rules read
	runOffline                    // the hub does not list its session
	runAsks                       // waits on the owner (a question, a plan, a tool approval)
	runLimited                    // stopped on a usage limit that has not reset yet
	runInterrupted                // cut off by a model error that is not a usage limit
	runAwaitsReply                // stopped on a plain-text question nobody answered
	runUnread                     // finished a turn nobody has seen
	runWaits                      // its turn ended blocked on another Agent or a named outside event
	runBackground                 // background work or a /loop brings it back, or the hub retries a model failure
	runStarting                   // Persona is opening it
	runRunning                    // an Agent works now
	runPersona                    // one of Persona's own conversations works now
)

// run is one active linked run: its link, its session when the hub lists it (else noSession), how it stands, and the
// pending card it asks by (asksBefore; nil: none the hub holds). s and card point into the facts: a session is 1.5 KB,
// and every read of the list makes a run of each active link.
type run struct {
	link  Agent
	s     *proto.Session
	found bool
	kind  runKind
	card  *proto.Interaction
}

// noSession is the session of a run the hub does not list: the zero session, never written.
var noSession proto.Session

// title is the run's title as the session list has it, else as the link recorded it.
func (r run) title() string {
	if r.found && r.s.Title != "" {
		return r.s.Title
	}
	return r.link.Title
}

// since is when the run got where it is: its session's state_since, else its updated_at.
func (r run) since() time.Time {
	if r.s.Work != nil && r.s.Work.Phase != proto.WorkWorking && !r.s.Starting &&
		stateOf(*r.s) != proto.StateWorking && stateOf(*r.s) != proto.StateNeedsYou && !r.s.Work.TurnAt.IsZero() {
		return r.s.Work.TurnAt
	}
	if !r.s.StateSince.IsZero() {
		return r.s.StateSince
	}
	return r.s.UpdatedAt
}

func (r run) reportStatus() string {
	if r.s.Work != nil && r.s.Work.Closeout != nil {
		switch status := r.s.Work.Closeout.Status; status {
		case proto.CloseoutDone, proto.CloseoutNotDone, proto.CloseoutBlocked:
			return status
		}
	}
	return ""
}

func parkEvidenceInteractionID(f *Facts) string {
	sv := f.Supervision
	if sv == nil || sv.State != reviewParked || sv.Parked == nil || sv.Parked.Evidence == nil || sv.Parked.Evidence.Kind != parkExternalEvent {
		return ""
	}
	return sv.Parked.Evidence.InteractionID
}

func parkInteractionID(f *Facts, a Agent) string {
	if a.DismissedAt != nil || a.StartError != "" {
		return ""
	}
	return parkEvidenceInteractionID(f)
}

// cardForRun reports whether pending interaction c belongs to run a directly or via a folded child or side session.
func cardForRun(c *proto.Interaction, a Agent) bool {
	if c.MachineID == a.MachineID && c.SessionID == a.SessionID {
		return true
	}
	if c.FoldedUnder != "" && c.FoldedUnder == a.SessionID && cmp.Or(c.FoldedUnderMachineID, c.MachineID) == a.MachineID {
		return true
	}
	return c.FoldedSide != "" && c.FoldedSide == a.SessionID && c.MachineID == a.MachineID
}

// linkedParkCard is the exact pending card a current external-event park names for this worker, if it still exists.
func linkedParkCard(f *Facts, a Agent) *proto.Interaction {
	id := parkInteractionID(f, a)
	if id == "" {
		return nil
	}
	for i := range f.Interactions {
		c := &f.Interactions[i]
		if c.ID == id && cardForRun(c, a) {
			return c
		}
	}
	return nil
}

// activeRuns are the to-do's runs that still work for it (not released, not dismissed, started), in the list's order
// (the newest link first), each read off its session. A released worker with the park's exact pending card remains,
// because releasing the worker did not answer its owner request.
func activeRuns(f *Facts, now time.Time) []run {
	var out []run
	for _, a := range f.Agents {
		linked := linkedParkCard(f, a)
		if (a.ReleasedAt != nil || a.DismissedAt != nil || a.StartError != "") && linked == nil {
			continue // a run whose start failed counts as stopped
		}
		r := run{link: a, s: &noSession}
		for i := range f.Sessions {
			if s := &f.Sessions[i]; s.MachineID == a.MachineID && s.SessionID == a.SessionID {
				r.s, r.found = s, true
				break
			}
		}
		var userCard, heldCard *proto.Interaction
		for i := range f.Interactions {
			c := &f.Interactions[i]
			if !cardForRun(c, a) {
				continue
			}
			if c.Delegating {
				if heldCard == nil || asksBefore(c, heldCard) {
					heldCard = c
				}
			} else if userCard == nil || asksBefore(c, userCard) {
				userCard = c
			}
		}
		if userCard != nil {
			r.card = userCard
		} else {
			r.card = heldCard
		}
		if linked != nil {
			r.card = linked
		}
		r.kind = runReading(r, now)
		if linked != nil {
			r.kind = runAsks
		} else if userCard != nil && r.found && (userCard.SessionID != a.SessionID || userCard.MachineID != a.MachineID) {
			// A folded child/side session has an un-delegated owner card while its parent session is working or idle.
			r.kind = runAsks
		} else if (r.kind == runAsks || r.kind == runIdle) && userCard == nil && heldCard != nil {
			// Every pending card on this run (direct or folded) went to its supervising conversation (or Persona
			// decides whether to answer it): Persona's work until that conversation passes it up, not the owner's.
			r.kind = runPersona
		} else if r.card == nil && r.kind == runAsks && r.s.Work != nil && r.s.Work.InteractionID == parkInteractionID(f, a) {
			// The exact linked card is gone; this is its stale session state, not a new owner question.
			r.kind = runIdle
		}
		out = append(out, r)
	}
	return out
}

// asksBefore: card c, rather than d, is what their run asks: the session's own before the manager's card about its
// turn, as the session list reads them (a work card may stay up after the agent went on to ask something else), then
// the older, then the lesser id, so two reads of the same cards name the same one.
func asksBefore(c, d *proto.Interaction) bool {
	if cw, dw := c.ToolName == assistantwire.WorkCardTool, d.ToolName == assistantwire.WorkCardTool; cw != dw {
		return dw
	}
	if !c.CreatedAt.Equal(d.CreatedAt) {
		return c.CreatedAt.Before(d.CreatedAt)
	}
	return c.ID < d.ID
}

// runReading is how run r stands: the first that holds of offline, asks, a usage limit, interrupted, awaits a reply,
// unread, waits on another Agent or a named outside event, background, starting, running.
func runReading(r run, now time.Time) runKind {
	if !r.found {
		return runOffline
	}
	s := *r.s
	st := stateOf(s)
	switch {
	case st == proto.StateNeedsYou:
		return runAsks
	case s.Starting:
		return runStarting
	case st == proto.StateWorking && s.Assistant:
		return runPersona
	case st == proto.StateWorking && !s.BackgroundWorking:
		return runRunning
	case runningBackground(s):
		return runBackground
	case s.Work != nil && s.Work.Phase == proto.WorkWorking:
		return runRunning
	case usageLimited(s, now):
		return runLimited
	case interrupted(s, now):
		return runInterrupted
	case s.AwaitsReply && stopped(s):
		return runAwaitsReply
	case r.reportStatus() == proto.CloseoutDone:
		return runUnread
	case r.reportStatus() != "":
		if waitsOn(s) != "" {
			return runWaits
		}
		if waitsByItself(s, now) {
			return runBackground
		}
		return runIdle
	case st == proto.StateUnread:
		return runUnread
	case waitsOn(s) != "":
		return runWaits
	case waitsByItself(s, now):
		return runBackground
	}
	return runIdle
}

// A generic blocked/external hint is not execution. Keep actual background and child work distinct from it.
func runningBackground(s proto.Session) bool {
	if s.BackgroundWorking || len(s.Background) > 0 || s.Children().Working > 0 {
		return true
	}
	for _, a := range s.Subagents {
		if a.Running {
			return true
		}
	}
	for _, w := range s.Workflows {
		if w.Running {
			return true
		}
	}
	return false
}

// stateOf is a session's state as the apps read it: the hub's, else worked out from its status.
func stateOf(s proto.Session) string {
	switch {
	case s.State != "":
		return s.State
	case s.Status == proto.StatusOffline:
		return "offline"
	case s.Status == proto.StatusWorking:
		return proto.StateWorking
	case s.Status == proto.StatusBlocked || s.PendingInteractions > 0:
		return proto.StateNeedsYou
	}
	return proto.StateIdle
}

// stopped: its turn is over, unread or seen.
func stopped(s proto.Session) bool {
	if s.State == proto.StateUnread || s.State == proto.StateIdle {
		return true
	}
	if s.State != "" {
		return false
	}
	return s.Status != proto.StatusWorking && s.Status != proto.StatusOffline && s.Status != proto.StatusBlocked &&
		s.PendingInteractions == 0
}

// usageLimitReset is when the usage limit that stopped s resets; nil when it is not known.
func usageLimitReset(s proto.Session) *time.Time {
	if s.TurnErrorResetsAt != nil {
		return s.TurnErrorResetsAt
	}
	if s.Work != nil {
		return s.Work.ResetsAt
	}
	return nil
}

// usageLimited: stopped on a usage limit whose reset (with its grace) is ahead or not known.
func usageLimited(s proto.Session, now time.Time) bool {
	if s.TurnError == "" || s.TurnErrorKind != proto.TurnErrorUsageLimit || !stopped(s) {
		return false
	}
	reset := usageLimitReset(s)
	return reset == nil || now.Before(reset.Add(usageLimitGrace))
}

// interrupted: cut off by a model error that is not a usage limit, and that the hub does not retry by itself.
func interrupted(s proto.Session, now time.Time) bool {
	if s.TurnError == "" || usageLimited(s, now) || retrying(s, now) {
		return false
	}
	st := stateOf(s)
	return st == proto.StateUnread || st == proto.StateIdle
}

// blockedOn is what s's turn ended blocked on (its work's on), when it is not working, asking or unread; "" otherwise.
func blockedOn(s proto.Session) string {
	st := stateOf(s)
	if st == proto.StateWorking || st == proto.StateNeedsYou || st == proto.StateUnread || s.Work == nil ||
		s.Work.Phase != proto.WorkBlocked {
		return ""
	}
	return s.Work.On
}

// waitsOn is what s waits on when its turn ended blocked on another Agent (agent) or on an outside event it names
// (external with a wait): the hub wakes it to check, when its wake switch is on (docs/assistant/agent-work.md
// "Waiting: scheduled wakeups"); "" otherwise. A usage limit's wait is never one: the rows read the limit itself.
func waitsOn(s proto.Session) string {
	switch on := blockedOn(s); {
	case on == proto.WaitAgent:
		return on
	case on == proto.WaitExternal && !wakesItself(s) && s.Work.ResetsAt == nil:
		return on
	}
	return ""
}

// wakesItself: its turn ended blocked on external with nothing named to wait for (no wait, no agent), which the hub
// writes for background commands still running and for a /loop between its ticks: the run ends it by itself, so the
// hub sends nothing (its managerWaits and actOn).
func wakesItself(s proto.Session) bool {
	return blockedOn(s) == proto.WaitExternal && s.Work.Wait == "" && s.Work.Agent == nil
}

// retrying: the hub's "Continue" after a moment's model failure is on its way (work blocked on transient with its
// wake_at, docs/assistant/agent-work.md "Transient errors: automatic continuation"), and not so overdue that the hub
// drops it from the list (retryGrace). The session keeps its turn_error meanwhile, but the hub writes no Interrupted
// receipt until its retries run out, and neither does the view.
func retrying(s proto.Session, now time.Time) bool {
	return blockedOn(s) == proto.WaitTransient && s.Work.WakeAt != nil && now.Before(s.Work.WakeAt.Add(retryGrace))
}

// waitsByItself: something brings it back without the owner and with nothing named to wait for: background work
// still running after its turn ended, a wait it ends by itself (background commands, a /loop), or the hub sending
// "Continue" after a moment's model failure.
func waitsByItself(s proto.Session, now time.Time) bool {
	if stateOf(s) == proto.StateWorking {
		return s.BackgroundWorking
	}
	return wakesItself(s) || retrying(s, now)
}

// firstRun is the first of runs (in the list's order) of kind k.
func firstRun(runs []run, k runKind) (run, bool) {
	for _, r := range runs {
		if r.kind == k {
			return r, true
		}
	}
	return run{}, false
}

// later is the later of a and *b, a nil b being none.
func later(a time.Time, b *time.Time) time.Time {
	if b != nil && b.After(a) {
		return *b
	}
	return a
}

// latest is the later of a and b.
func latest(a, b time.Time) time.Time { return later(a, &b) }

// earliest is the earlier of a and b, a zero time being none.
func earliest(a, b time.Time) time.Time {
	if a.IsZero() || !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}

// lastActivity is the last moment at now that the to-do or any of its active runs moved: its own times, its ledger's,
// its delivery's and ask's, the owner's restore, its return from Someday once that passed, and each active run's
// session (docs/assistant/todo-status-sop.md "Principle: real signals only").
func lastActivity(f *Facts, now time.Time) time.Time {
	last := later(f.UpdatedAt, &f.CreatedAt)
	last = later(last, &f.RuntimeAt)
	last = later(last, f.ReopenedAt)
	last = later(last, f.DoneAt)
	if v := f.Supervision; v != nil {
		last = later(last, &v.UpdatedAt)
		if v.Progress != nil {
			last = later(last, &v.Progress.At)
		}
		if r := latestReview(v); r != nil {
			last = later(last, &r.At)
		}
	}
	if f.Delivery != nil {
		last = later(last, &f.Delivery.At)
	}
	if f.Ask != nil {
		last = later(last, &f.Ask.AskedAt)
	}
	last = later(last, f.RestoredAt)
	if u := f.SomedayUntil; u != nil && !now.Before(*u) {
		last = later(last, u)
	}
	for _, a := range f.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		for _, s := range f.Sessions {
			if s.MachineID == a.MachineID && s.SessionID == a.SessionID {
				last = later(last, &s.UpdatedAt)
				last = later(last, &s.StateSince)
				break
			}
		}
	}
	return last
}

// latestReview is the ledger's latest review; nil when there is none.
func latestReview(v *Supervision) *Review {
	if v == nil || len(v.Reviews) == 0 {
		return nil
	}
	return &v.Reviews[len(v.Reviews)-1]
}

// acceptanceStands: the latest review accepted it and none came since, even when the hub reopened it
// (docs/assistant/todo-status-sop.md "Accepted tasks").
func acceptanceStands(f *Facts) bool {
	r := latestReview(f.Supervision)
	return f.Supervision != nil && f.Supervision.State == reviewAccepted && r != nil && r.Verdict == reviewAccepted
}

// reviewState is the review state that holds now: an accepted task reopened after a newer review, or a cancelled one
// reopened, goes on as if unreviewed.
func reviewState(f *Facts) string {
	if f.Supervision == nil {
		return ""
	}
	st := f.Supervision.State
	if f.DoneAt == nil && (st == reviewAccepted && !acceptanceStands(f) || st == reviewCancelled) {
		return ""
	}
	return st
}

// pendingReviewSince is when a pending_review task began waiting for supervisor review.
func pendingReviewSince(f *Facts) time.Time {
	if f.Supervision == nil {
		return time.Time{}
	}
	if f.Supervision.PendingSince != nil {
		return *f.Supervision.PendingSince
	}
	return f.Supervision.UpdatedAt
}

// pendingReviewSLADuration returns the effective pending_review quiet window under todos_v2=true.
// ok is false when quiet sinking is disabled (surfaces immediately to needs_you).
func pendingReviewSLADuration(f *Facts) (time.Duration, bool) {
	if f.PendingReviewSLADisabled {
		return 0, false
	}
	if f.PendingReviewSLAHours > 0 {
		return time.Duration(f.PendingReviewSLAHours) * time.Hour, true
	}
	return pendingReviewEscalateAfter, true
}

// canWaitSLADuration returns the effective can_wait auto-escalation window under todos_v2=true.
// ok is false when auto-escalation is disabled (can_wait never ages into active needs_you).
func canWaitSLADuration(f *Facts) (time.Duration, bool) {
	if f.CanWaitSLADisabled {
		return 0, false
	}
	if f.CanWaitSLAHours > 0 {
		return time.Duration(f.CanWaitSLAHours) * time.Hour, true
	}
	return canWaitEscalateAfter, true
}

// openAsk is the to-do's own ask when a whole one waits.
func openAsk(f *Facts) *Ask {
	if a := f.Ask; a != nil && a.Verdict != "" && !a.AskedAt.IsZero() {
		return a
	}
	return nil
}

// normalizePRURL is a PR's URL as two of them compare: trimmed, without a fragment or query, without a trailing
// slash, lower case.
func normalizePRURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.IndexAny(raw, "#?"); i >= 0 {
		raw = raw[:i]
	}
	return strings.ToLower(strings.TrimRight(raw, "/"))
}

// openPRs are the to-do's PRs with a fresh observation saying open and not a draft: its current PR, then its earlier
// ones (#7935). Stale, absent or mismatched facts count for nothing.
func openPRs(f *Facts, now time.Time) []PRStatus {
	fresh := func(p PRStatus) bool {
		age := now.Sub(p.CheckedAt)
		return p.State == prOpen && !p.Draft && !p.CheckedAt.IsZero() && age >= 0 && age < prFreshFor
	}
	var out []PRStatus
	if p := f.PRStatus; p != nil && f.PRURL != "" && normalizePRURL(f.PRURL) == normalizePRURL(p.URL) && fresh(*p) {
		out = append(out, *p)
	}
	for _, p := range f.OtherPRs {
		if normalizePRURL(p.URL) != normalizePRURL(f.PRURL) && fresh(p) {
			out = append(out, p)
		}
	}
	return out
}

// dueEnd is the moment the to-do stops being due ahead: its due_at, or the midnight that ends its due day in loc;
// false without either.
func dueEnd(f *Facts) (time.Time, bool) {
	if f.DueAt != nil {
		return *f.DueAt, true
	}
	if f.DueDate == "" { // most to-dos have no date: a failed parse would allocate its error on every read
		return time.Time{}, false
	}
	loc := f.Loc
	if loc == nil {
		loc = time.UTC
	}
	day, err := time.ParseInLocation("2006-01-02", f.DueDate, loc)
	if err != nil {
		return time.Time{}, false
	}
	return day.AddDate(0, 0, 1), true
}
