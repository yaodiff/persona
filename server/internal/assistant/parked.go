package assistant

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Parked tasks (docs/assistant/todos.md "Parked tasks"). New parks require a typed external wait and source-backed
// evidence (park_evidence.go). An owner decision belongs in needs_user, never a new owner_priority park. Existing
// records remain readable without inventing contacts. A parked task stays open: no new ask, push or conclusion card.
// A verified interaction_id instead keeps the worker's existing pending request in Needs you, without another card.
// No stalled, idle, orphaned, overdue or undecidable signal is raised for the park. At its time its
// supervisor gets a parked_due signal: decide, park again with a new reason, or close it. One the supervisor does not
// act on (no task_review follows it) is resent a few times (#14048). Its agent starting a turn
// (someone sent it something) ends the parking, and it is supervised as before. So does a decision of the owner's
// recorded on a task parked for them or for someone else (parkDecided, #8044): its supervisor gets the decision.
// Parking again and again is the old habit under a new name, so the parks in a row with no agent turn between them
// are counted, and one past maxReparks re-parks is refused. The watch reconciles legacy owner_priority parks into
// needs_user and hands missing questions back to their supervisor; it never reconstructs a question or consent.

const (
	reviewParked = "parked"
	// sigParkedDue: a parked task's time came (parkedDueSignal).
	sigParkedDue = "parked_due"
	// sigParkDecided: a decision of the owner's was recorded on a task parked for them or for someone else, which
	// ended the parking (parkDecided).
	sigParkDecided = "park_decided"
)

// What a parked task waits on (task_review waiting_on).
const (
	parkOwnerPriority = "owner_priority" // legacy reads only; new owner decisions use needs_user
	parkSomeoneElse   = "someone_else"   // another person has to do or answer something
	parkExternalEvent = "external_event" // something outside has to happen (a release, a result, a fix upstream)
	parkDate          = "date"           // nothing but the time
)

var parkWaitingOn = []string{parkSomeoneElse, parkExternalEvent, parkDate}

var (
	// parkDefault: how long a task is parked when the review gives no time; parkMin and parkMax bound the time it gives.
	parkDefault = 24 * time.Hour
	parkMin     = time.Minute
	parkMax     = 30 * 24 * time.Hour
)

// parkedDueResendAfter is how long a told parked_due waits for the supervisor's task_review before it is told again, and
// maxParkedDueResends how many times it is resent (#14048).
const (
	parkedDueResendAfter = time.Hour
	maxParkedDueResends  = 3
)

// maxReparks is how many times in a row a task is parked again with no agent turn in between; one more is refused.
const maxReparks = 3

// Parked is what a parked task waits for, as the apps get it on the to-do (supervision.parked): the reason in one
// sentence, until when, what kind of thing it waits on, and how many times in a row it was parked with no agent turn
// in between (1: parked once).
type Parked struct {
	Reason    string             `json:"reason"`
	Until     time.Time          `json:"until"`
	WaitingOn string             `json:"waiting_on"`
	Count     int                `json:"count"`
	Evidence  *ParkEvidence      `json:"evidence,omitempty"`
	Handoff   *taskHandoffTarget `json:"handoff,omitempty"`
}

// parking is what holds v parked now: its latest review, when the ledger's state is parked and that review parked it.
// nil when it is not parked.
func (v *Supervision) parking() *TaskReview {
	if v == nil || v.State != reviewParked || len(v.Reviews) == 0 {
		return nil
	}
	if r := &v.Reviews[len(v.Reviews)-1]; r.Verdict == reviewParked {
		return r
	}
	return nil
}

// activeTimeSince discounts the intersection of each park with the current period of inactivity.
// For older ledgers, the next review (or the last ledger update) is the available end of a completed park.
func (v *Supervision) activeTimeSince(since, now time.Time) time.Duration {
	active := now.Sub(since)
	cursor := since
	for i, r := range v.Reviews {
		if r.Verdict != reviewParked || r.At.IsZero() {
			continue
		}
		end := r.ParkEndedAt
		if end.IsZero() {
			end = v.UpdatedAt
			if i+1 < len(v.Reviews) {
				end = v.Reviews[i+1].At
			}
		}
		if end.After(now) {
			end = now
		}
		start := r.At
		if cursor.After(start) {
			start = cursor
		}
		if end.After(start) {
			active -= end.Sub(start)
			cursor = end
		}
	}
	return active
}

// parked is v's parking as the apps get it; nil when it is not parked.
func (v *Supervision) parked() *Parked {
	r := v.parking()
	if r == nil {
		return nil
	}
	p := &Parked{Reason: r.Reason, Until: r.Until, WaitingOn: r.WaitingOn, Count: max(1, r.Parks), Evidence: r.Evidence.clone()}
	if r.Handoff != nil {
		target := r.Handoff.Target
		p.Handoff = &target
	}
	return p
}

// parseParkUntil reads a park's time: a duration ("3d", "36h", "2d12h", "90m") or an RFC 3339 time; "" is parkDefault
// from now. It must be parkMin to parkMax ahead.
func parseParkUntil(raw string, now time.Time) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return now.Add(parkDefault).UTC(), nil
	}
	var at time.Time
	if d, ok := parkDuration(raw); ok {
		at = now.Add(d)
	} else if t, err := time.Parse(time.RFC3339, raw); err == nil {
		at = t
	} else {
		return time.Time{}, fmt.Errorf("until %q: give a duration such as 3d, 36h or 90m, or an RFC 3339 time", raw)
	}
	if d := at.Sub(now); d < parkMin || d > parkMax {
		return time.Time{}, fmt.Errorf("until %q: must be between 1 minute and 30 days from now", raw)
	}
	return at.UTC(), nil
}

// parkDuration reads a duration that may count days ("3d", "2d12h"), which time.ParseDuration does not.
func parkDuration(raw string) (time.Duration, bool) {
	days, rest, hasDays := strings.Cut(raw, "d")
	if !hasDays {
		d, err := time.ParseDuration(raw)
		return d, err == nil
	}
	n, err := strconv.Atoi(days)
	if err != nil || n < 0 || n > 365 {
		return 0, false
	}
	d := time.Duration(n) * 24 * time.Hour
	if rest != "" {
		more, err := time.ParseDuration(rest)
		if err != nil || more < 0 {
			return 0, false
		}
		d += more
	}
	return d, true
}

// parkElse is what a refused park is told to do instead.
const parkElse = "A task that waits for nothing is not parked: decide it yourself and send the agent on (agent_send), send " +
	"the work back (rework), or close it (accepted or cancelled); a decision only the user can make is needs_user."

// readPark checks the external wait and its source-backed evidence before any review is written. The error names
// everything missing at once.
func readPark(reason, until, waitingOn string, evidence *ParkEvidence, now time.Time) (Parked, error) {
	p := Parked{Reason: clip(oneLine(reason), 200), WaitingOn: strings.TrimSpace(waitingOn)}
	var missing []string
	if p.Reason == "" {
		missing = append(missing, "reason (one sentence: what the task waits for)")
	}
	if !slices.Contains(parkWaitingOn, p.WaitingOn) {
		missing = append(missing, "waiting_on (what kind of thing it waits on: "+strings.Join(parkWaitingOn, ", ")+")")
	}
	if p.WaitingOn == parkOwnerPriority {
		missing = append(missing, "owner_priority is an owner decision: use needs_user with why_owner, reason, recommend, options and cost_of_waiting")
	}
	e, fields := readParkEvidence(evidence, p.WaitingOn, until, now)
	missing = append(missing, fields...)
	at, err := parseParkUntil(until, now)
	if err != nil {
		missing = append(missing, err.Error())
	}
	if len(missing) > 0 {
		return Parked{}, fmt.Errorf("parked is refused: it does not say what the task waits for. Missing: %s. %s",
			strings.Join(missing, "; "), parkElse)
	}
	p.Until, p.Evidence = at, e
	return p, nil
}

// reparkError refuses parking task id again after it was parked parks times in a row with no agent turn in between.
func reparkError(id string, parks int) error {
	return fmt.Errorf("task %s was parked %d times in a row (re-parked %d times) and its agent did not work in between: a "+
		"task nobody moves is not waiting, it is stuck. It is not parked again. Close it (cancelled, or accepted when what "+
		"was done is enough), or state the real decision for the user: needs_user with why_owner, reason, recommend, options "+
		"and cost_of_waiting", id, parks, parks-1)
}

// parkedAnswer is what task_review answers a park of task id.
func parkedAnswer(id string, p Parked, loc *time.Location) string {
	owner := ""
	if p.WaitingOn == parkOwnerPriority {
		owner = " The user's daily digest lists it as parked, waiting for their priority call, with your reason; it is not in their Needs you."
	}
	decided := ""
	if p.WaitingOn == parkOwnerPriority || p.WaitingOn == parkSomeoneElse {
		decided = " A decision the user records on the task (an answered card, a reply in its topic, one written in the apps) ends " +
			"it too, and a supervision message brings you the decision."
	}
	left := 1 + maxReparks - p.Count
	asked := "the user is not asked"
	if p.Evidence != nil && p.Evidence.InteractionID != "" {
		asked = "no new request is created: the existing linked owner request remains actionable"
	}
	again := fmt.Sprintf("It can be parked again %d more time(s) with no agent turn in between.", left)
	if left <= 0 {
		again = "It cannot be parked again unless its agent works in between: next time, decide or close it."
	}
	return fmt.Sprintf("Task %s is parked until %s (waiting_on %s; parked %d time(s) in a row). It stays open; %s, and "+
		"no stalled, idle, orphaned or overdue signal is raised for it meanwhile.%s At that time a supervision message (parked_due) brings it "+
		"back to you: decide, park it again with a new reason, or close it. Its agent starting a turn ends the parking (anything sent "+
		"to it does that, so tell it what it needs before you park, not after).%s %s", id, p.Until.In(loc).Format(toolTimeLayout), p.WaitingOn,
		p.Count, asked, owner, decided, again)
}

// parkedDueSignal: supervised open task t is parked and its time came, and no supervision message carried that yet
// (kept on the review once one did: parkedDueTold): the signal that hands it back. Its key names the park, so the
// watch raising it again every tick tells it once.
func parkedDueSignal(t Todo, now time.Time) (superviseSignal, bool) {
	v := t.Supervision
	if v == nil || t.DoneAt != nil {
		return superviseSignal{}, false
	}
	r := v.parking()
	if r == nil || r.Handoff != nil || now.Before(r.Until) {
		return superviseSignal{}, false
	}
	// Told already: a reminder the supervisor did not act on (no task_review since, the park is still this one) comes
	// again after parkedDueResendAfter, at most maxParkedDueResends times. A supervision turn that ended on a model API
	// error (a 429) writes no review, so such a turn does not count as handled.
	if !r.DueAt.IsZero() && (r.DueResends >= maxParkedDueResends || now.Before(laterTime(r.DueAt, r.Until).Add(parkedDueResendAfter))) {
		return superviseSignal{}, false
	}
	return superviseSignal{Todo: t.ID, Kind: sigParkedDue, Key: parkDueKey(r), Detail: r.Reason,
		Minutes: int(now.Sub(r.At).Minutes())}, true
}

// laterTime is the later of a and b.
func laterTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// parkKey names one park: when the review parked it.
func parkKey(r *TaskReview) string { return "parked@" + r.At.UTC().Format(time.RFC3339Nano) }

// parkDueKey names one parked_due reminder of park r: the first carries parkKey, each resend after it adds its number,
// so a resend is news to the supervision dedup and a reminder already told is not raised under its old key again.
func parkDueKey(r *TaskReview) string {
	if r.DueAt.IsZero() {
		return parkKey(r)
	}
	return parkKey(r) + "#resend=" + strconv.Itoa(r.DueResends+1)
}

// parkedDueResend is which resend of park r's parked_due its next reminder is: 0 for the first reminder.
func parkedDueResend(r *TaskReview) int {
	if r == nil || r.DueAt.IsZero() {
		return 0
	}
	return r.DueResends + 1
}

// parkedDueTold keeps on each park in list that a supervision message carried its parked_due (sent, or queued where a
// restart keeps it), so a manager started again does not tell it a second time; until then the signal is raised again
// from the ledger, so a restart before the message went loses nothing.
func (s *Service) parkedDueTold(ctx context.Context, list []superviseSignal) {
	for _, g := range list {
		if g.Kind != sigParkedDue {
			continue
		}
		if _, _, err := s.updateSupervision(ctx, g.Todo, func(v *Supervision) bool {
			r := v.parking()
			if r == nil || parkDueKey(r) != g.Key {
				return false
			}
			if !r.DueAt.IsZero() {
				r.DueResends++
			}
			r.DueAt = time.Now().UTC()
			return true
		}); err != nil {
			s.logf("task %s: parked due: %v", g.Todo, err)
		}
	}
}

// decisionEndsPark: a decision from source is the owner's own call: written or chosen by them in the apps or on a
// card of the task's (manual), their answer to a question card of its agent (card), or their reply in its topic
// (thread). Not an agent's note, a supervisor's answer to a routed card or the hub's default at a deadline (agent),
// and not what the hub or an extension writes of a task's outcome (outcome, reported): none of those is what a task
// parked for the owner or for someone else waits for, and its supervisor is not woken for them.
func decisionEndsPark(source string) bool {
	return source == db.DecisionManual || source == db.DecisionCard || source == db.DecisionThread
}

// parkWaitsForDecision: a task parked waiting on on waits for an answer (the owner's call, or someone else's), which
// a decision of the owner's recorded on it may be.
func parkWaitsForDecision(on string) bool {
	return on == parkOwnerPriority || on == parkSomeoneElse || on == parkTodoHandoff
}

// decidedPark is the latest park of v that a recorded decision ended (parkDecided), whatever was reviewed since: a
// review that followed it, parking it again included, does not mean its supervisor heard of the decision, so the
// decision is told until a supervision message carried it (DecidedTold). nil when no park of v was ended so.
func (v *Supervision) decidedPark() *TaskReview {
	if v == nil {
		return nil
	}
	for i := len(v.Reviews) - 1; i >= 0; i-- {
		if r := &v.Reviews[i]; r.Verdict == reviewParked && r.Decided != "" {
			return r
		}
	}
	return nil
}

// parkDecidedKey names one park a decision ended: the decision.
func parkDecidedKey(r *TaskReview) string { return "park_decided@" + r.Decided }

// parkDecided: decision row was just stored. When its to-do is an open supervised task parked for the owner's call or
// for someone else (waiting_on owner_priority, someone_else) and the decision is the owner's own (decisionEndsPark),
// what the task waited for may have come: the parking ends as when its agent starts a turn (supervisedWorking), a
// parked_due still waiting is dropped, and its supervisor is told with the to-do's decisions (parkDecidedSignal), so it
// sends the agent on, reviews the task or closes it. A task parked for something outside or for a date waits on: no
// decision is what it waits for. Every writer of decisions passes here (insertDecision).
//
// The decision is stored by then, so nothing here fails the request that recorded it: a ledger write that fails is
// logged, and the watch ends the parking from the stored decision on its next look (parkDecidedRetry).
func (s *Service) parkDecided(ctx context.Context, row db.TodoDecisionRow) {
	if err := s.endParkOnDecision(ctx, row); err != nil {
		s.logf("task %s: decision %s on a parked task: %v (the watch ends the parking from the stored decision)", row.TodoID, row.ID, err)
	}
}

// endParkOnDecision is parkDecided's work: an error only when the ledger could not be written.
func (s *Service) endParkOnDecision(ctx context.Context, row db.TodoDecisionRow) error {
	if !decisionEndsPark(row.Source) {
		return nil
	}
	t, ok := s.todoNow(row.TodoID)
	if !ok || t.DoneAt != nil || t.Supervision.decisionPark() == nil {
		return nil
	}
	v, changed, err := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
		r := v.decisionPark()
		if r == nil || !parkWaitsForDecision(r.WaitingOn) ||
			!row.CreatedAt.IsZero() && row.CreatedAt.Before(r.At.Truncate(time.Microsecond)) {
			return false // not parked for an answer, or parked again after this decision (parkDecidedRetry)
		}
		r.Decided = row.ID
		v.State = ""
		if v.Round > 0 {
			v.State = reviewRework
		}
		return true
	})
	if err != nil || !changed {
		return err
	}
	b := &s.supervise
	b.mu.Lock()
	delete(b.pending, t.ID+"|"+sigParkedDue)
	delete(b.pending, t.ID+"|"+sigUndecidable)
	delete(b.pending, t.ID+"|"+sigEstimateDue)
	s.forgetSupervisePending(t.ID+"|"+sigParkedDue, t.ID+"|"+sigUndecidable, t.ID+"|"+sigEstimateDue)
	b.mu.Unlock()
	s.logf("task %s: decision %s was recorded while it was parked: the parking ends, its supervisor is told", t.ID, row.ID)
	t.Supervision = &v
	if g, ok := parkDecidedSignal(t); ok {
		s.signal(g)
	}
	return nil
}

// parkDecidedRetry: open task t is parked for an answer, and a decision of the owner's was stored after it was parked
// (the latest such one in effect): the parking ends on it, as parkDecided would have when it was stored. The watch
// asks this on every look, so a ledger write that failed then (or a manager that stopped between the decision and the
// ledger) is made good from what is stored, never lost. True means owner evidence was found or its lookup failed:
// legacy reconciliation must not run ahead of an answer that is stored, pending or unknown.
func (s *Service) parkDecidedRetry(ctx context.Context, t Todo) bool {
	r := t.Supervision.decisionPark()
	if t.DoneAt != nil || r == nil || !parkWaitsForDecision(r.WaitingOn) {
		return false
	}
	st := s.todoDB()
	if st == nil {
		return false
	}
	list, err := st.ListTodoDecisions(ctx, t.ID, false)
	if err != nil {
		s.logf("task %s: the decisions of a parked task: %v", t.ID, err)
		return true
	}
	for i := len(list) - 1; i >= 0; i-- {
		if d := list[i]; decisionEndsPark(d.Source) && !d.CreatedAt.Before(r.At.Truncate(time.Microsecond)) {
			if err := s.endParkOnDecision(ctx, d); err != nil {
				s.logf("task %s: decision %s on a parked task: %v (tried again on the next look)", t.ID, d.ID, err)
			}
			return true
		}
	}
	return false
}

// parkDecidedSignal: a recorded decision ended a parking of open task t (parkDecided) and no supervision message
// carried that yet (kept on the review once one did: parkDecidedTold): the signal that tells its supervisor, even when
// it was reviewed or parked again since. Its key names the decision, so the watch raising it again every tick tells it
// once, and a manager restarted before the message went raises it again from the ledger.
func parkDecidedSignal(t Todo) (superviseSignal, bool) {
	v := t.Supervision
	if v == nil || t.DoneAt != nil {
		return superviseSignal{}, false
	}
	r := v.decidedPark()
	if r == nil || !r.DecidedTold.IsZero() {
		return superviseSignal{}, false
	}
	return superviseSignal{Todo: t.ID, Kind: sigParkDecided, Key: parkDecidedKey(r), Detail: "搁置时在等：" + r.Reason}, true // i18n-ignore: the supervision message is the assistant's prompt
}

// parkDecidedTold keeps on each park in list that a supervision message carried its park_decided (sent, or queued
// where a restart keeps it), as parkedDueTold does for parked_due. An earlier park a decision ended that was not told
// yet is marked too: the message carried every decision of the to-do.
func (s *Service) parkDecidedTold(ctx context.Context, list []superviseSignal) {
	for _, g := range list {
		if g.Kind != sigParkDecided {
			continue
		}
		if _, _, err := s.updateSupervision(ctx, g.Todo, func(v *Supervision) bool {
			r := v.decidedPark()
			if r == nil || parkDecidedKey(r) != g.Key || !r.DecidedTold.IsZero() {
				return false
			}
			now := time.Now().UTC()
			for i := range v.Reviews {
				if p := &v.Reviews[i]; p.Verdict == reviewParked && p.Decided != "" && p.DecidedTold.IsZero() {
					p.DecidedTold = now
				}
			}
			return true
		}); err != nil {
			s.logf("task %s: park decided: %v", g.Todo, err)
		}
	}
}

// parkedSession: the open task this session works for is parked: the manager sends its agent nothing on its own
// (workGuard), since any turn of its agent ends the parking.
func (s *Service) parkedSession(machine, session string) bool {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	t := s.todos.todos[s.todos.activeBySession[[2]string{machine, session}]]
	return t != nil && t.DoneAt == nil && t.Supervision.parking() != nil
}

// parkedEntry is park r as a parked_due signal's entry carries it.
func (s *Service) parkedEntry(ctx context.Context, r *TaskReview) *prompts.SuperviseParked {
	return &prompts.SuperviseParked{Reason: r.Reason, WaitingOn: r.WaitingOn, Count: max(1, r.Parks),
		Until: r.Until.In(s.location(ctx)).Format(toolTimeLayout), Left: max(0, 1+maxReparks-max(1, r.Parks))}
}

// parkedHealth is a parked task's supervision in the snapshot's words: until when and why.
func parkedHealth(p *Parked, loc *time.Location) string {
	on := map[string]string{parkOwnerPriority: "等用户定优先级", parkSomeoneElse: "等别人", parkExternalEvent: "等外部的事", // i18n-ignore: the snapshot is the assistant's prompt
		parkDate: "等到时间", parkTodoHandoff: "等另一条待办回传结果"}[p.WaitingOn] // i18n-ignore: the snapshot is the assistant's prompt
	return fmt.Sprintf("已搁置到 %s（%s）：%s", p.Until.In(loc).Format("01-02 15:04"), on, p.Reason) // i18n-ignore: the snapshot is the assistant's prompt
}
