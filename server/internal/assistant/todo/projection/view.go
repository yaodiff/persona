package projection

import (
	"cmp"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The decision table (docs/design/proposals/todos-redesign.md 6.3): the first row that holds places the to-do, except
// row 13, which only sets the Inactive mark on a to-do row 14 places. Closed work comes first (rows 6 and 7), so an
// open to-do never reads as done (6.2 invariant 1). Each function below is one row family and says which rows; the
// fields each sets, and every line key, are listed in spec/fixtures/todo-view/README.md. next_change_at is worked out
// apart from the rows (nextChange): the first moment the whole view reads differently.

// placement is a view and what ranks it within its section, worked out from the facts together with the view (the
// ranking never reads them back off it): the soonest deadline of the decisions on the row, when it wakes (a park's
// end, a usage limit's reset and its grace, the hub's next wake of a run that waits) and when it comes back from
// Someday; a zero time is none.
type placement struct {
	assistantwire.TodoSummary
	wait                 *assistantwire.TodoWait
	deadline, wake, back time.Time
}

// reading is one to-do's facts as the rows read them at now, worked out once.
type reading struct {
	f    *Facts
	now  time.Time
	last time.Time // the last activity
	runs []run
}

func read(f *Facts, now time.Time) *reading {
	return &reading{f: f, now: now, last: lastActivity(f, now), runs: activeRuns(f, now)}
}

// View is the view of to-do f at now: the first row of the decision table that holds, with the steps it carries
// rolled up (proposal 3.4 "Steps roll up") and the next moment time alone changes it. Its rank is 0: Views ranks the
// rows of a list.
func View(f Facts, now time.Time) assistantwire.TodoSummary {
	return place(&f, now).TodoSummary
}

// place is f's placement at now, next_change_at included.
func place(f *Facts, now time.Time) placement {
	p := read(f, now).place()
	p.NextChangeAt = nextChange(f, now, p.TodoSummary)
	return p
}

// place is the placement the rows and the steps give, without next_change_at.
func (r *reading) place() placement {
	p := r.withSteps(r.own())
	if p.Section != assistantwire.TodoListSectionTodo {
		return p
	}
	// A gate or blocker fills an otherwise idle row before it is ranked or rolled up to a parent.
	switch {
	case r.f.TodosV2 && r.f.RepoGate != nil:
		p.Reason = assistantwire.TodoReasonPrOpen
		p.Line = line(assistantwire.TodoLineKeyPrOpen)
		p.wait = copyWait(*r.f.RepoGate)
	case len(r.f.ActiveBlockers) > 0:
		b := r.f.ActiveBlockers[0]
		p.Reason = assistantwire.TodoReasonParked
		p.Line = line(assistantwire.TodoLineKeyParked, assistantwire.TodoLineArgReason, b.WaitingOn)
		if remaining := len(r.f.ActiveBlockers) - 1; remaining > 0 {
			p.Line = withArg(p.Line, "remaining", strconv.Itoa(remaining))
		}
		p.wait = copyWait(b)
	default:
		return p
	}
	p.Phase, p.Section, p.Inactive = assistantwire.TodoPhaseWaiting, assistantwire.TodoListSectionWaiting, time.Time{}
	return p
}

// own places the to-do by its own facts, rows 1 to 14.
func (r *reading) own() placement {
	if p, ok := r.closedWork(); ok { // rows 6, 7
		return p
	}
	if r.f.StartError != "" { // row 1, then 12 to 14
		return r.failedStart()
	}
	for _, row := range []func() (placement, bool){
		r.runAsks,           // row 2
		r.ownerDecides,      // row 3
		r.stoppedUnfinished, // row 2's unattended-stop fallback; explicit owner asks keep their own reason
		r.canWait,           // row 4
		r.working,           // row 5
		r.parked,            // row 8
		r.structuredWait,    // row 8, from a runtime report or provider observation
		r.finishedRecent,    // row 9
		r.waiting,           // row 10
		r.finishedSeen,      // row 11
		r.someday,           // row 12
	} {
		if p, ok := row(); ok {
			return p
		}
	}
	return r.rest() // rows 13, 14
}

// at is a placement in phase, section and reason, seen when the owner looked at it since its last activity (the
// closed rows compare with the close instead).
func (r *reading) at(phase, section, reason string) placement {
	return placement{TodoSummary: assistantwire.TodoSummary{Phase: phase, Section: section, Reason: reason,
		Seen: r.seenSince(r.last), CanWait: section == assistantwire.TodoListSectionCanWait}}
}

// seenSince: the owner looked at it at or after at.
func (r *reading) seenSince(at time.Time) bool {
	return r.f.SeenAt != nil && !r.f.SeenAt.Before(at)
}

// supervised: a conversation (main or a direction leader) supervises this task and handles its operational failures
// (failed starts, transient/model interruptions, supervisor-held cards) until it explicitly escalates to the owner.
func (r *reading) supervised() bool {
	return r.f.Supervision != nil && r.f.Supervision.Supervisor != ""
}

// line is a line 2 with the arguments that have a value (name, value pairs); an empty args map is left out.
func line(key string, args ...string) *assistantwire.TodoLine {
	l := &assistantwire.TodoLine{Key: key}
	for i := 0; i+1 < len(args); i += 2 {
		if args[i+1] == "" {
			continue
		}
		if l.Args == nil {
			l.Args = map[string]string{}
		}
		l.Args[args[i]] = args[i+1]
	}
	return l
}

// stamp is an absolute time for a line argument or a view field: UTC, as Go writes it.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// listed is a time as the list writes it, for an ask's id and round: sent back as it is.
func listed(t time.Time) string { return t.Format(time.RFC3339Nano) }

// DecisionAskID is the id of a to-do's own decision asked at askedAt: its ask's in the view, and the one its push
// names (assistantwire.NotifyTodo.Ask), so an answer from the notification reaches the same ask.
func DecisionAskID(askedAt time.Time) string { return "todo:decision@" + listed(askedAt) }

func utc(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC()
}

// firstLine is the first non-empty line of s, trimmed.
func firstLine(s string) string {
	for l := range strings.Lines(s) {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// ---- rows 6 and 7: closed work ----

// closedWork: closed and unseen within 24 hours is Just finished (row 6); then, or once seen, Done with how it ended
// (row 7). A failed start superseded by finished related work reads as closed (proposal 6.3, "Rule 1a").
func (r *reading) closedWork() (placement, bool) {
	f := r.f
	if !f.closed() {
		return placement{}, false
	}
	at := *f.DoneAt
	reason, l := r.outcome()
	p := r.at(assistantwire.TodoPhaseDone, assistantwire.TodoListSectionDone, reason)
	if p.Seen = r.seenSince(at); !p.Seen && r.now.Before(at.Add(justFinishedFor)) {
		p.Section, p.Reason = assistantwire.TodoListSectionJustFinished, assistantwire.TodoReasonClosedUnseen
	}
	p.Line, p.Since = l, utc(at)
	return p, true
}

// outcome is how a closed to-do ended (row 7's reasons) and its line. A cancellation is read off the ledger, as the
// web reads it: the list sends a cancelled to-do's done_by as review, and its ledger says cancelled. done_by review
// with any other ledger state is an accepting review; an owner's close of a once cancelled task is the owner's.
func (r *reading) outcome() (string, *assistantwire.TodoLine) {
	f := r.f
	cancelled := f.CancelledAt != nil || f.DoneBy == doneByCancelled ||
		f.DoneBy == doneByReview && f.Supervision != nil && f.Supervision.State == reviewCancelled
	switch {
	case f.StartError != "": // only a superseded failed start is closed with its error
		return assistantwire.TodoReasonSuperseded, line(assistantwire.TodoLineKeySuperseded)
	case cancelled:
		return assistantwire.TodoReasonCanceled, line(assistantwire.TodoLineKeyCanceled, "reason", cancelNote(f))
	case f.DoneBy == doneByReported:
		return assistantwire.TodoReasonReported, line(assistantwire.TodoLineKeyReported)
	}
	return assistantwire.TodoReasonCompleted, line(assistantwire.TodoLineKeyCompleted, "by", f.DoneBy)
}

// cancelNote is why the to-do was cancelled: the first line of the cancelling review's note, else its
// cancelled_reason. Never its done_reason, which leads with the hub's own word for a cancellation.
func cancelNote(f *Facts) string {
	if v := f.Supervision; v != nil {
		for i := len(v.Reviews) - 1; i >= 0; i-- {
			if v.Reviews[i].Verdict == reviewCancelled {
				if n := firstLine(v.Reviews[i].Note); n != "" {
					return n
				}
				break
			}
		}
	}
	return f.CancelledReason
}

// ---- row 1: a failed start ----

// failedStart: a run failed to start; it stays open and needs the owner for 48 hours after the failure, then rows 12
// to 14 place it as nothing works on it, with the Inactive mark whatever its due date or origin. Its retry is the ask,
// safe to answer anywhere, on a to-do a conversation opened: the hub starts no other again, so one with no origin only
// says why it did not start.
func (r *reading) failedStart() placement {
	f := r.f
	// The failure is done_at, which FailTodo stores (a start that fails on a closed to-do keeps the close). updated_at
	// stands in for a row without one, which FailTodo never leaves: the concerns it skips get no start error.
	failed := f.UpdatedAt
	if f.DoneAt != nil {
		failed = *f.DoneAt
	}
	if !r.now.Before(failed.Add(failedStartFor)) {
		if p, ok := r.someday(); ok {
			return p
		}
		return r.rest()
	}
	if r.supervised() {
		// A supervised task's failed start is its supervisor's to retry, remedy or give up on (the start remedies and
		// the failed signal go there): Persona works on it, and only the supervisor's needs_user brings it to the owner.
		p := r.at(assistantwire.TodoPhaseWorking, assistantwire.TodoListSectionWorking, assistantwire.TodoReasonPersonaWorking)
		p.Line, p.Since = line(assistantwire.TodoLineKeyFailedStart, "error", f.StartError), utc(failed)
		return p
	}
	p := r.at(assistantwire.TodoPhaseNeedsYou, assistantwire.TodoListSectionNeedsYou, assistantwire.TodoReasonFailedStart)
	p.Line, p.Since = line(assistantwire.TodoLineKeyFailedStart, "error", f.StartError), utc(failed)
	if f.Origin != "" {
		p.Asks = []assistantwire.OwnerAsk{withRisk(assistantwire.OwnerAsk{ID: "todo:retry@" + listed(failed),
			Kind: assistantwire.OwnerAskKindRetry, Home: assistantwire.OwnerAskHomeTodo}, assistantwire.OwnerAskRiskSafe)}
	}
	return p
}

// ---- row 2: a run asks, or was cut off ----

// runAsks: a run waits on the owner (agent_asks; every run that asks is an ask, the oldest first) or was cut off by a
// model error (interrupted). Unattended closeouts are handled separately, after explicit owner decisions.
func (r *reading) runAsks() (placement, bool) {
	var asking []run
	for _, rn := range r.runs {
		if rn.kind == runAsks {
			asking = append(asking, rn)
		}
	}
	if len(asking) > 0 {
		slices.SortStableFunc(asking, func(a, b run) int { return a.since().Compare(b.since()) })
		p := r.at(assistantwire.TodoPhaseNeedsYou, assistantwire.TodoListSectionNeedsYou, assistantwire.TodoReasonAgentAsks)
		for _, rn := range asking {
			p.Asks = append(p.Asks, runAsk(rn))
		}
		p.Line = line(assistantwire.TodoLineKeyAgentAsks, "run", asking[0].title(), "text", p.Asks[0].Text)
		p.Since = utc(asking[0].since())
		r.withOwnAsk(&p)
		return p, true
	}
	if !r.supervised() {
		if rn, ok := firstRun(r.runs, runInterrupted); ok {
			p := r.at(assistantwire.TodoPhaseNeedsYou, assistantwire.TodoListSectionNeedsYou, assistantwire.TodoReasonInterrupted)
			p.Line, p.Since = line(assistantwire.TodoLineKeyInterrupted, "run", rn.title()), utc(rn.since())
			r.withOwnAsk(&p)
			return p, true
		}
	}
	return placement{}, false
}

// stoppedTurn reads the latest evidenced stop, not a read receipt or automatic ledger update. A newer stopped
// run supersedes an older report; any actual execution keeps the task out of the unattended-stop fallback.
func (r *reading) stoppedTurn() (run, time.Time, bool) {
	var lead run
	var at time.Time
	found := false
	for _, rn := range r.runs {
		switch rn.kind {
		case runRunning, runPersona, runStarting, runBackground:
			return run{}, time.Time{}, false
		}
		if !rn.found || rn.s.Work == nil || rn.kind != runIdle && rn.kind != runUnread && rn.kind != runWaits {
			continue
		}
		if rn.reportStatus() == "" && rn.kind != runIdle {
			continue // preserve upstream's unread/wait compatibility when there is no closeout report
		}
		if rn.reportStatus() == "" && rn.s.Work.TurnAt.IsZero() {
			continue // older snapshots without stop evidence keep their compatibility reading
		}
		end := rn.since()
		if end.IsZero() && rn.link.LinkedAt != nil {
			end = *rn.link.LinkedAt
		}
		if !found || end.After(at) {
			lead, at, found = rn, end, true
		}
	}
	at = latest(at, r.f.CreatedAt)
	if sv := r.f.Supervision; sv != nil {
		at = later(at, sv.FollowedUpAt)
		for _, review := range sv.Reviews {
			if review.By == "" {
				at = latest(at, review.At)
			}
		}
	}
	return lead, at, found
}

func (r *reading) stoppedUnfinished() (placement, bool) {
	if r.f.Unreached || r.settled() || reviewState(r.f) == reviewParked || r.runtimeWait() != nil {
		return placement{}, false
	}
	rn, since, ok := r.stoppedTurn()
	if !ok || rn.kind == runWaits || rn.reportStatus() == proto.CloseoutDone || since.IsZero() || r.now.Sub(since) <= stoppedFollowupAfter {
		return placement{}, false
	}
	text := rn.s.Work.Wait
	if c := rn.s.Work.Closeout; c != nil {
		text = cmp.Or(c.Waiting, c.Missing, text)
	}
	p := r.at(assistantwire.TodoPhaseNeedsYou, assistantwire.TodoListSectionNeedsYou, assistantwire.TodoReasonStoppedUnfinished)
	p.Line, p.Since = line(assistantwire.TodoLineKeyStoppedUnfinished, "run", rn.title(), "text", text), utc(since)
	return p, true
}

// runAsk is run rn's ask, at home in its session, its id following the moment the session came to wait: what its
// pending card asks (cardKind), with the card's id to answer it by, safe to answer in place only when a notification
// may answer it too (proto.Interaction.OneTapQuestion: one question with options, no fields, not a Just me turn's or a
// health conversation's, and its asker did not say going ahead cannot be undone); a question, open, while the hub
// holds no card for it (the session list says that it waits, not on what).
func runAsk(rn run) assistantwire.OwnerAsk {
	text := rn.s.Asks
	if text == "" {
		text = rn.s.AttentionReason
	}
	a := assistantwire.OwnerAsk{
		ID:   "run:" + rn.link.MachineID + "/" + rn.link.SessionID + "@" + listed(rn.since()),
		Kind: assistantwire.OwnerAskKindQuestion, Home: assistantwire.OwnerAskHomeRun, Text: text,
		Run: &assistantwire.TodoRunRef{MachineID: rn.link.MachineID, SessionID: rn.link.SessionID, Title: rn.title()},
	}
	risk := assistantwire.OwnerAskRiskOpen
	if c := rn.card; c != nil {
		a.Kind, a.Interaction = cardKind(c), c.ID
		if _, ok := c.OneTapQuestion(); ok {
			risk = assistantwire.OwnerAskRiskSafe
		}
	}
	return withRisk(a, risk)
}

// cardKind is the kind of ask a run's pending card is: secret when it asks for a password (on the screen, or among
// the fields to fill in), form for other fields to fill in or an MCP server's form, tool for an approval, plan for a
// plan, else question (a question card, or a dialog on the screen).
func cardKind(c *proto.Interaction) string {
	switch {
	case c.Secret || slices.ContainsFunc(c.Fields, func(f proto.Field) bool { return f.Secret }):
		return assistantwire.OwnerAskKindSecret
	case len(c.Fields) > 0 || c.Kind == proto.IxElicitation:
		return assistantwire.OwnerAskKindForm
	case c.Kind == proto.IxPermission:
		return assistantwire.OwnerAskKindTool
	case c.Kind == proto.IxPlan:
		return assistantwire.OwnerAskKindPlan
	}
	return assistantwire.OwnerAskKindQuestion
}

// withRisk is ask a with its risk and the lock-screen action that follows from it (Lockscreen); a risk that is neither
// safe nor open (none said) is open.
func withRisk(a assistantwire.OwnerAsk, risk string) assistantwire.OwnerAsk {
	a.Risk = assistantwire.OwnerAskRiskOpen
	if risk == assistantwire.OwnerAskRiskSafe {
		a.Risk = assistantwire.OwnerAskRiskSafe
	}
	a.Lockscreen = Lockscreen(risk)
	return a
}

// Lockscreen is what a push about an ask of the given risk may offer on the lock screen: its answers (answer) only when
// it is safe, else nothing (open_only), a risk none said included. The view's asks and the decision push both take it
// from here.
func Lockscreen(risk string) string {
	if risk == assistantwire.OwnerAskRiskSafe {
		return assistantwire.OwnerAskLockscreenAnswer
	}
	return assistantwire.OwnerAskLockscreenOpenOnly
}

// withOwnAsk adds the to-do's own decision to p's asks when one also waits on the owner.
func (r *reading) withOwnAsk(p *placement) {
	if a, src := r.ownAsk(); src != nil {
		p.addAsk(a, src)
	}
}

// addAsk adds a, read from the to-do's own ask src, to p's asks, and its deadline to p's.
func (p *placement) addAsk(a assistantwire.OwnerAsk, src *Ask) {
	p.Asks = append(p.Asks, a)
	if src.Deadline != nil {
		p.deadline = earliest(p.deadline, *src.Deadline)
	}
}

// decisionAsk is the to-do's own open decision (a conclusion that needs the owner's decision, or says it cannot be
// done), unless the owner chose Don't handle on its round.
func (r *reading) decisionAsk() *Ask {
	a := openAsk(r.f)
	if a == nil || r.dismissed() || (a.Outcome != outcomeDecide && a.Outcome != outcomeCannot) {
		return nil
	}
	return a
}

// dismissed: the owner chose Don't handle on the decision round, so it no longer waits on them.
func (r *reading) dismissed() bool {
	return r.f.DecisionReview != nil && r.f.DecisionReview.DismissalID != ""
}

// ownAsk is the to-do's own decision as an ask at home on the to-do, its id and round the moment it was asked, its
// risk as listed, and the ask it was read from (nil when no decision waits).
func (r *reading) ownAsk() (assistantwire.OwnerAsk, *Ask) {
	a := r.decisionAsk()
	if a == nil {
		return assistantwire.OwnerAsk{}, nil
	}
	round := listed(a.AskedAt)
	out := assistantwire.OwnerAsk{ID: DecisionAskID(a.AskedAt), Kind: assistantwire.OwnerAskKindDecision,
		Home: assistantwire.OwnerAskHomeTodo, Round: round, Text: a.Question, Recommend: a.Recommend,
		Options: a.Options, CanWait: r.canWaitAsk(a)}
	if a.Default == defaultTaken && a.Deadline != nil {
		out.DefaultAt = utc(*a.Deadline)
	}
	return withRisk(out, a.Risk), a
}

// canWaitDecision: a decision that can wait for the briefing.
func canWaitDecision(a *Ask) bool { return a.Outcome == outcomeDecide && a.ForBriefing }

// canWaitAsk reports whether decision ask a stays in the Can wait fold at r.now.
// Under todos_v2, non-critical can_wait decisions age into active Needs you after the configured SLA
// (default 48h; disabled when CanWaitSLADisabled is true).
func (r *reading) canWaitAsk(a *Ask) bool {
	if !canWaitDecision(a) {
		return false
	}
	if r.f.TodosV2 && !a.AskedAt.IsZero() {
		if d, ok := canWaitSLADuration(r.f); ok && !r.now.Before(a.AskedAt.Add(d)) {
			return false
		}
	}
	return true
}

// pendingReviewWaiting reports whether f is in pending_review without an open owner ask under todos_v2.
func (r *reading) pendingReviewWaiting() bool {
	return r.f.TodosV2 && reviewState(r.f) == reviewPending && !r.settled() && openAsk(r.f) == nil
}

// ---- row 3: the owner decides ----

// ownerDecides: open, and its review waits for the owner's decision (needs_user) or its ask is a decision or says it
// cannot be done. A dismissed decision is out, and a decision that can wait is row 4's. Personal verification
// (user_check) is deliberately not placed here: #15680 (#15638) keeps that optional "to verify" layer calm and out of
// Needs you on every surface, so it falls through to its review/rest placement; clients report counts.user_check.
func (r *reading) ownerDecides() (placement, bool) {
	state := reviewState(r.f)
	a := r.decisionAsk()
	p := r.at(assistantwire.TodoPhaseNeedsYou, assistantwire.TodoListSectionNeedsYou, assistantwire.TodoReasonOwnerDecides)
	switch {
	case a == nil && (r.dismissed() || state != reviewNeedsUser):
		if d := r.f.Delivery; d != nil && d.Owner {
			p.Line, p.Since = line(assistantwire.TodoLineKeyDecision, "text", d.Gate), utc(d.At)
			return p, true
		}
		if r.pendingReviewWaiting() {
			since := pendingReviewSince(r.f)
			d, ok := pendingReviewSLADuration(r.f)
			if !ok {
				p.Line, p.Since = line(assistantwire.TodoLineKeyDecision), utc(since)
				return p, true
			}
			if !since.IsZero() && !r.now.Before(since.Add(d)) {
				p.Line, p.Since = line(assistantwire.TodoLineKeyDecision), utc(since)
				return p, true
			}
		}
		return placement{}, false
	case a != nil && r.canWaitAsk(a): // row 4
		return placement{}, false
	}
	switch {
	case a == nil: // a needs_user review with no ask listed: its question, nothing to answer here
		p.Line = line(assistantwire.TodoLineKeyDecision)
		if rv := latestReview(r.f.Supervision); rv != nil {
			p.Line, p.Since = line(assistantwire.TodoLineKeyDecision, "text", rv.Reason, "recommend", rv.Recommend), utc(rv.At)
		}
		return p, true
	case a.Outcome == outcomeCannot:
		p.Line = line(assistantwire.TodoLineKeyCannot)
	default:
		p.Line = line(assistantwire.TodoLineKeyDecision, "text", a.Question, "recommend", a.Recommend)
	}
	p.Since = utc(a.AskedAt)
	r.withOwnAsk(&p)
	return p, true
}

// ---- row 4: it can wait ----

// canWait: a decision marked for the briefing (Persona may take its recommendation at its deadline), or a run that
// ended on a plain-text question; listed under Needs you's fold, not counted.
func (r *reading) canWait() (placement, bool) {
	if a, src := r.ownAsk(); src != nil && a.CanWait {
		p := r.at(assistantwire.TodoPhaseNeedsYou, assistantwire.TodoListSectionCanWait, assistantwire.TodoReasonCanWait)
		p.Line = line(assistantwire.TodoLineKeyDecision, "text", a.Text, "recommend", a.Recommend)
		if !a.DefaultAt.IsZero() {
			p.Line = line(assistantwire.TodoLineKeyDecisionDefault, "text", a.Text, "recommend", a.Recommend, assistantwire.TodoLineArgDefaultAt, stamp(a.DefaultAt))
		}
		p.Since = utc(src.AskedAt)
		p.addAsk(a, src)
		return p, true
	}
	if rn, ok := firstRun(r.runs, runAwaitsReply); ok {
		p := r.at(assistantwire.TodoPhaseNeedsYou, assistantwire.TodoListSectionCanWait, assistantwire.TodoReasonAwaitsReply)
		p.Line, p.Since = line(assistantwire.TodoLineKeyAwaitsReply, "run", rn.title(), "text", rn.s.Asks), utc(rn.since())
		return p, true
	}
	return placement{}, false
}

// ---- row 5: someone works on it ----

// working: Persona or an Agent works on it now, else a run starts, else one works in the background or the hub
// retries its moment's model failure; within each kind the first such run in the list's order (the newest link first)
// decides. queued needs the start's admission queue, which the plan keeps and the list does not carry yet.
func (r *reading) working() (placement, bool) {
	for _, rn := range r.runs {
		if rn.kind != runRunning && rn.kind != runPersona {
			continue
		}
		text := rn.s.Activity
		if text == "" {
			text = firstLine(rn.s.LastText)
		}
		p := r.at(assistantwire.TodoPhaseWorking, assistantwire.TodoListSectionWorking, assistantwire.TodoReasonAgentRunning)
		p.Line = line(assistantwire.TodoLineKeyWorking, "run", rn.title(), "text", text)
		if rn.kind == runPersona {
			p.Reason, p.Line = assistantwire.TodoReasonPersonaWorking, line(assistantwire.TodoLineKeyPersonaWorking, "text", text)
		}
		p.Since = utc(rn.since())
		return p, true
	}
	for _, k := range []struct {
		kind   runKind
		reason string
		key    string
	}{
		{runStarting, assistantwire.TodoReasonStarting, assistantwire.TodoLineKeyStarting},
		{runBackground, assistantwire.TodoReasonBackground, assistantwire.TodoLineKeyBackground},
	} {
		if rn, ok := firstRun(r.runs, k.kind); ok {
			p := r.at(assistantwire.TodoPhaseWorking, assistantwire.TodoListSectionWorking, k.reason)
			p.Line, p.Since = line(k.key, "run", rn.title()), utc(rn.since())
			return p, true
		}
	}
	if r.pendingReviewWaiting() {
		p := r.at(assistantwire.TodoPhaseWorking, assistantwire.TodoListSectionWorking, assistantwire.TodoReasonPersonaWorking)
		p.Line, p.Since = line(assistantwire.TodoLineKeyPersonaWorking), utc(pendingReviewSince(r.f))
		return p, true
	}
	// A supervised task whose agent stopped on a model error (e.g. 503) is its supervisor's to recover or escalate,
	// just like a supervised failed start: keep it in Working / persona_working unless the task is already settled,
	// parked, or awaiting optional owner verification (user_check).
	if r.supervised() && !r.settled() && reviewState(r.f) != reviewParked && reviewState(r.f) != reviewUserCheck && r.runtimeWait() == nil {
		if rn, ok := firstRun(r.runs, runInterrupted); ok {
			p := r.at(assistantwire.TodoPhaseWorking, assistantwire.TodoListSectionWorking, assistantwire.TodoReasonPersonaWorking)
			p.Line, p.Since = line(assistantwire.TodoLineKeyInterrupted, "run", rn.title()), utc(rn.since())
			return p, true
		}
	}
	return placement{}, false
}

// ---- row 8: parked ----

// parked: its supervisor parked it until something named happens; it waits whatever its run last said. A park on
// someone else says so in its reason. The park's end passing changes nothing by itself (the hub's wake does); it is
// when the row wakes, for its rank.
func (r *reading) parked() (placement, bool) {
	sv := r.f.Supervision
	if sv == nil || sv.State != reviewParked || sv.Parked == nil {
		return placement{}, false
	}
	reason := assistantwire.TodoReasonParked
	if sv.Parked.WaitingOn == parkSomeoneElse {
		reason = assistantwire.TodoReasonSomeoneElse
	}
	p := r.at(assistantwire.TodoPhaseWaiting, assistantwire.TodoListSectionWaiting, reason)
	p.Line = line(assistantwire.TodoLineKeyParked, "reason", sv.Parked.Reason, assistantwire.TodoLineArgUntil, stamp(sv.Parked.Until))
	if rv := latestReview(sv); rv != nil && rv.Verdict == reviewParked {
		p.Since = utc(rv.At)
	}
	p.wait = buildParkedWait(sv)
	p.wake = sv.Parked.Until
	return p, true
}

func (r *reading) structuredWait() (placement, bool) {
	w := r.runtimeWait()
	if w == nil {
		return placement{}, false
	}
	p := r.at(assistantwire.TodoPhaseWaiting, assistantwire.TodoListSectionWaiting, assistantwire.TodoReasonParked)
	p.wait = buildRuntimeWait(w, r.f.RuntimeAt)
	p.Line = line(assistantwire.TodoLineKeyParked, assistantwire.TodoLineArgReason, p.wait.Reason, assistantwire.TodoLineArgUntil, stamp(p.wait.Until))
	p.Since, p.wake = p.wait.Since, p.wait.Until
	return p, true
}

// ---- rows 9 and 11: finished work ----

// finish is what says the to-do's work finished, when it did and its line: accepted (even when the hub reopened it),
// deployed, a run's turn nobody has seen, a review not done yet, or its ask saying done or please verify. An open
// concern is never accepted or deployed: only its report closes it.
func (r *reading) finish() (time.Time, *assistantwire.TodoLine, bool) {
	f := r.f
	if r.settled() {
		if acceptanceStands(f) {
			return latestReview(f.Supervision).At, line(assistantwire.TodoLineKeyAccepted), true
		}
		at := f.Delivery.At
		if at.IsZero() {
			at = r.last
		}
		return at, line(assistantwire.TodoLineKeyDeployed), true
	}
	if rn, since, ok := r.stoppedTurn(); ok {
		if rn.reportStatus() != proto.CloseoutDone {
			return time.Time{}, nil, false
		}
		at := rn.since()
		if at.IsZero() {
			at = since
		}
		return at, line(assistantwire.TodoLineKeyFinished, "run", rn.title()), true
	}
	if rn, ok := firstRun(r.runs, runUnread); ok {
		return rn.since(), line(assistantwire.TodoLineKeyFinished, "run", rn.title()), true
	}
	if reviewState(f) == reviewPending {
		at := f.Supervision.UpdatedAt
		if f.Supervision.PendingSince != nil {
			at = *f.Supervision.PendingSince
		}
		return at, line(assistantwire.TodoLineKeyFinished), true
	}
	if a := openAsk(f); a != nil && (a.Outcome == "" || a.Outcome == outcomeDone) {
		return a.AskedAt, line(assistantwire.TodoLineKeyFinished), true
	}
	return time.Time{}, nil, false
}

// settled: accepted or deployed, finished whatever its PR facts say; never an open concern.
func (r *reading) settled() bool {
	f := r.f
	return f.Kind != kindConcern && (acceptanceStands(f) || f.Delivery != nil && f.Delivery.State == deliveryLive)
}

// finishedRecent: finished, and nobody has seen it since it last moved (finished_unseen), or seen less than 24 hours
// ago (finished_recent): ready, in Just finished (row 9).
func (r *reading) finishedRecent() (placement, bool) {
	at, l, ok := r.finish()
	if !ok {
		return placement{}, false
	}
	p := r.at(assistantwire.TodoPhaseReady, assistantwire.TodoListSectionJustFinished, assistantwire.TodoReasonFinishedUnseen)
	p.Line, p.Since = l, utc(at)
	if rn, _, known := r.stoppedTurn(); known && rn.reportStatus() == proto.CloseoutDone {
		p.Seen = r.seenSince(at)
	}
	if !p.Seen {
		return p, true
	}
	if r.now.Before(r.f.SeenAt.Add(justFinishedFor)) {
		p.Reason = assistantwire.TodoReasonFinishedRecent
		return p, true
	}
	return placement{}, false
}

// finishedSeen: finished, seen more than 24 hours ago and still open: a To do to tick (row 11).
func (r *reading) finishedSeen() (placement, bool) {
	at, _, ok := r.finish()
	if !ok {
		return placement{}, false
	}
	p := r.at(assistantwire.TodoPhaseTodo, assistantwire.TodoListSectionTodo, assistantwire.TodoReasonFinishedSeen)
	p.Line, p.Since = line(assistantwire.TodoLineKeyFinishedSeen, assistantwire.TodoLineArgAt, stamp(at)), utc(at)
	if rn, _, known := r.stoppedTurn(); known && rn.reportStatus() == proto.CloseoutDone {
		p.Seen = r.seenSince(at)
	}
	return p, true
}

// ---- row 10: waiting on something named ----

// waiting: fresh facts say one of its PRs is open and waits for a merge (never accepted or deployed work), else a run
// stopped on a usage limit until it resets, else a run's turn ended blocked on another Agent or an outside event
// (someone_else; it also comes with a park, row 8), in the order 6.3 lists them. computer_offline needs Start When
// It's Back, scheduled_report a report's schedule: neither is kept yet.
func (r *reading) waiting() (placement, bool) {
	if prs := openPRs(r.f, r.now); len(prs) > 0 && !r.settled() {
		p := r.at(assistantwire.TodoPhaseWaiting, assistantwire.TodoListSectionWaiting, assistantwire.TodoReasonPrOpen)
		p.Line = line(assistantwire.TodoLineKeyPrOpen)
		p.wait = buildPRWait(prs[0])
		return p, true
	}
	if rn, ok := firstRun(r.runs, runLimited); ok {
		p := r.at(assistantwire.TodoPhaseWaiting, assistantwire.TodoListSectionWaiting, assistantwire.TodoReasonUsageLimit)
		p.Line, p.Since = line(assistantwire.TodoLineKeyUsageLimit, "run", rn.title()), utc(rn.since())
		if reset := usageLimitReset(*rn.s); reset != nil {
			p.Line = line(assistantwire.TodoLineKeyUsageLimit, "run", rn.title(), assistantwire.TodoLineArgResetsAt, stamp(*reset))
			p.wake = reset.Add(usageLimitGrace)
		}
		p.wait = buildUsageLimitWait(rn)
		return p, true
	}
	if rn, ok := firstRun(r.runs, runWaits); ok {
		p := r.at(assistantwire.TodoPhaseWaiting, assistantwire.TodoListSectionWaiting, assistantwire.TodoReasonSomeoneElse)
		p.Line, p.Since = line(assistantwire.TodoLineKeyRunWaits, "run", rn.title(), "on", waitsOn(*rn.s)), utc(rn.since())
		// When the hub looks next ranks it; that moment passing changes nothing by itself, as a park's end does not.
		if w := rn.s.Work.WakeAt; w != nil {
			p.wake = *w
		}
		p.wait = buildRunWaitsWait(rn)
		return p, true
	}
	return placement{}, false
}

// ---- row 12: Someday ----

// someday: the owner put it off, until a day or with no date; back in its place once the day comes.
func (r *reading) someday() (placement, bool) {
	until := r.f.SomedayUntil
	if until == nil || !r.now.Before(*until) {
		return placement{}, false
	}
	p := r.at(assistantwire.TodoPhaseSomeday, assistantwire.TodoListSectionSomeday, assistantwire.TodoReasonDeferred)
	p.Line = line(assistantwire.TodoLineKeySomeday)
	if until.Before(somedayNoDate) {
		p.Line, p.back = line(assistantwire.TodoLineKeySomeday, assistantwire.TodoLineArgUntil, stamp(*until)), *until
	}
	return p, true
}

// ---- rows 13 and 14: nothing works on it ----

// rest: a To do with no line (its notes are line 2). Row 13's Inactive mark: a task (an Agent was linked, or a
// conversation handed it out) with no due date ahead, never an open concern, or any failed start, that nothing moved
// for 48 hours; never while a computer one of its runs is on did not answer.
func (r *reading) rest() placement {
	f := r.f
	p := r.at(assistantwire.TodoPhaseTodo, assistantwire.TodoListSectionTodo, assistantwire.TodoReasonRest)
	failed := f.StartError != ""
	linked := slices.ContainsFunc(f.Agents, func(a Agent) bool { return a.DismissedAt == nil })
	if f.Unreached || !failed && (f.Kind == kindConcern || !linked && f.Origin == "") {
		return p
	}
	from := r.last.Add(inactiveAfter)
	if due, ok := dueEnd(f); ok && !failed && due.After(from) {
		from = due
	}
	if !r.now.Before(from) {
		p.Inactive = utc(r.last)
	}
	return p
}

// ---- steps ----

// stepOrder: the sections a step carries its parent into, the most urgent first. A step's Can wait comes after live
// work: it is listed, not counted (proposal 3.4 row 1b, 3.7), so it never pulls a parent that works or waits into the
// fold, and lifts only a parent in To do, Just finished or Someday.
var stepOrder = []string{assistantwire.TodoListSectionNeedsYou, assistantwire.TodoListSectionWorking,
	assistantwire.TodoListSectionWaiting, assistantwire.TodoListSectionCanWait}

// part is the parent's own placement (step nil) or one of its open steps'.
type part struct {
	placement
	step *Facts
	last time.Time // its last activity
}

// withSteps counts the to-do's steps and rolls them up (proposal 3.4 "Steps roll up", 6.2 invariant 6). An open
// parent takes the phase, section, reason, since, line and wake of its leading step (leadingStep), its line naming the
// step, and has no Inactive mark. A row in Needs you or Can wait lists the asks of the parent and of every open step
// there, the leading part's first, then Needs you's before Can wait's, the oldest first: a step is never a row of its
// own, so its asks are on its parent's, each id saying whose it is. seen compares with the latest activity of the parts
// on the row. A closed parent stays done.
func (r *reading) withSteps(own placement) placement {
	f := r.f
	if len(f.Steps) == 0 {
		return own
	}
	own.Steps = &assistantwire.TodoStepCount{Total: len(f.Steps)}
	var steps []part
	for i := range f.Steps {
		s := &f.Steps[i]
		if s.closed() {
			own.Steps.Done++
		} else if !f.closed() {
			step := s
			if r.f.TodosV2 {
				cp := *s
				cp.TodosV2 = true
				cp.PendingReviewSLAHours = r.f.PendingReviewSLAHours
				cp.PendingReviewSLADisabled = r.f.PendingReviewSLADisabled
				cp.CanWaitSLAHours = r.f.CanWaitSLAHours
				cp.CanWaitSLADisabled = r.f.CanWaitSLADisabled
				step = &cp
			}
			sr := read(step, r.now)
			steps = append(steps, part{placement: sr.place(), step: step, last: sr.last})
		}
	}
	// The parts on the row: the one that leads it, then, on a row that lists asks, the others that are in Needs you or
	// Can wait.
	parts := []part{{placement: own, last: r.last}}
	if i := leadingStep(own.Section, steps); i >= 0 {
		parts[0] = steps[i]
	}
	lead := parts[0]
	if holdsAsks(lead.Section) {
		for _, p := range append([]part{{placement: own, last: r.last}}, steps...) {
			if p.step != lead.step && holdsAsks(p.Section) {
				parts = append(parts, p)
			}
		}
		slices.SortStableFunc(parts[1:], func(a, b part) int {
			return cmp.Or(cmp.Compare(sectionIndex(a.Section), sectionIndex(b.Section)), a.Since.Compare(b.Since))
		})
	}
	if len(parts) == 1 && lead.step == nil {
		return own // no step is on the row
	}
	out := lead.placement
	out.Asks, out.deadline = nil, time.Time{}
	for _, pt := range parts {
		for _, a := range pt.Asks {
			if pt.step != nil {
				a.ID = "step:" + pt.step.ID + "/" + a.ID
			}
			out.Asks = append(out.Asks, a)
		}
		out.deadline = earliest(out.deadline, pt.deadline)
	}
	out.Seen = r.seenSince(latest(r.last, latestOf(parts)))
	if lead.step != nil {
		out.Line = withArg(out.Line, "step", lead.step.Title)
		if out.wait != nil {
			out.wait = cloneWait(out.wait)
			out.wait.Step = lead.step.Title
		}
		out.Inactive, out.Steps, out.back = time.Time{}, own.Steps, time.Time{}
	}
	return out
}

// withArg is l with one more argument; nil stays nil.
func withArg(l *assistantwire.TodoLine, name, value string) *assistantwire.TodoLine {
	if l == nil {
		return nil
	}
	args := map[string]string{name: value}
	for k, a := range l.Args {
		args[k] = a
	}
	return &assistantwire.TodoLine{Key: l.Key, Args: args}
}

// leadingStep is the index of the step that leads its parent's row: the first in the most urgent of stepOrder's
// sections (the oldest step on a tie), when that is ahead of the parent's own section; -1 when none is.
func leadingStep(own string, steps []part) int {
	lead, best := -1, len(stepOrder)
	for i, s := range steps {
		if k := slices.Index(stepOrder, s.Section); k >= 0 && k < best {
			lead, best = i, k
		}
	}
	switch {
	case lead < 0 || sectionIndex(steps[lead].Section) >= sectionIndex(own):
		return -1
	case steps[lead].Section == assistantwire.TodoListSectionCanWait &&
		sectionIndex(own) <= sectionIndex(assistantwire.TodoListSectionWaiting):
		return -1
	}
	return lead
}

// holdsAsks: a section whose rows list asks.
func holdsAsks(section string) bool {
	return section == assistantwire.TodoListSectionNeedsYou || section == assistantwire.TodoListSectionCanWait
}

// latestOf is the latest activity of parts.
func latestOf(parts []part) time.Time {
	var out time.Time
	for _, p := range parts {
		out = latest(out, p.last)
	}
	return out
}

// sectionIndex is the section's place in the list's order; an unknown section goes last.
func sectionIndex(section string) int {
	if i := slices.Index(assistantwire.TodoListSections, section); i >= 0 {
		return i
	}
	return len(assistantwire.TodoListSections)
}

// ---- next_change_at ----

// nextChange is next_change_at: the first moment after now at which time alone changes f's view v (its phase,
// section, reason, line, times, marks or asks); zero when none does. The view changes only where one of the windows
// the rules read ends (moments), so it is the first of those at which the view reads differently.
func nextChange(f *Facts, now time.Time, v assistantwire.TodoSummary) time.Time {
	at := moments(f, now)
	slices.SortFunc(at, time.Time.Compare)
	for _, t := range slices.CompactFunc(at, time.Time.Equal) {
		if !sameView(read(f, t).place().TodoSummary, v) {
			return utc(t)
		}
	}
	return time.Time{}
}

// moments are the ends after now of every window the rules read for f and its steps: a failed start's 48 hours,
// closed or seen work's 24 hours, a PR's freshness, a usage limit's reset and grace, a scheduled retry's wake and
// grace, Someday's return, and the 48 hours to the Inactive mark or the end of a due day.
func moments(f *Facts, now time.Time) []time.Time {
	var out []time.Time
	add := func(t time.Time) {
		if t.After(now) && t.Before(somedayNoDate) {
			out = append(out, t)
		}
	}
	if rn, since, ok := read(f, now).stoppedTurn(); ok && rn.reportStatus() != proto.CloseoutDone && !since.IsZero() {
		add(since.Add(stoppedFollowupAfter).Add(time.Nanosecond))
	}
	add(f.UpdatedAt.Add(failedStartFor))
	if f.DoneAt != nil {
		add(f.DoneAt.Add(justFinishedFor))
		add(f.DoneAt.Add(failedStartFor))
	}
	if f.SeenAt != nil {
		add(f.SeenAt.Add(justFinishedFor))
	}
	prs := f.OtherPRs
	if f.PRStatus != nil {
		prs = append([]PRStatus{*f.PRStatus}, prs...)
	}
	for _, p := range prs {
		add(p.CheckedAt)
		add(p.CheckedAt.Add(prFreshFor))
	}
	for _, s := range f.Sessions {
		if reset := usageLimitReset(s); reset != nil {
			add(reset.Add(usageLimitGrace))
		}
		if w := s.Work; w != nil && w.On == proto.WaitTransient && w.WakeAt != nil {
			add(w.WakeAt.Add(retryGrace))
		}
	}
	if u := f.SomedayUntil; u != nil {
		add(*u)
		add(u.Add(inactiveAfter))
	}
	add(lastActivity(f, now).Add(inactiveAfter))
	if due, ok := dueEnd(f); ok {
		add(due)
	}
	if f.TodosV2 {
		if reviewState(f) == reviewPending {
			if since := pendingReviewSince(f); !since.IsZero() {
				if d, ok := pendingReviewSLADuration(f); ok {
					add(since.Add(d))
				}
			}
		}
		if a := openAsk(f); a != nil && canWaitDecision(a) && !a.AskedAt.IsZero() {
			if d, ok := canWaitSLADuration(f); ok {
				add(a.AskedAt.Add(d))
			}
		}
	}
	for i := range f.Steps {
		if f.TodosV2 {
			cp := f.Steps[i]
			cp.TodosV2 = true
			cp.PendingReviewSLAHours = f.PendingReviewSLAHours
			cp.PendingReviewSLADisabled = f.PendingReviewSLADisabled
			cp.CanWaitSLAHours = f.CanWaitSLAHours
			cp.CanWaitSLADisabled = f.CanWaitSLADisabled
			out = append(out, moments(&cp, now)...)
		} else {
			out = append(out, moments(&f.Steps[i], now)...)
		}
	}
	return out
}

// sameView: a and b read the same, whatever their next_change_at and rank.
func sameView(a, b assistantwire.TodoSummary) bool {
	a.NextChangeAt, a.Rank, b.NextChangeAt, b.Rank = time.Time{}, 0, time.Time{}, 0
	return reflect.DeepEqual(a, b)
}
