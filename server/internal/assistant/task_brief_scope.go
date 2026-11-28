package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
)

// ---- scope changes and the clock (#3822) ----
//
// After a task is handed out, what done means changes in one way only: an entry in its brief's log (task_scope, or
// agent_supervise / agent_send with a task on one's own task, which go through the same code). The entry says what
// changed and why, and leaves the criteria and expected time in effect; the brief itself stays as it was written. A
// change that adds a step only the owner may allow (irreversible, or seen by other people) is refused unless the
// owner's own message started the turn that makes it, and that message is kept on the entry.
//
// The clock: a brief past max(expect_minutes × 3, a day) since it was handed out (or since its latest scope change)
// with no end raises one overdue signal to its supervisor, recorded in the log; a scope change starts the clock again.

// sigOverdue: the task's brief passed its clock with no end (briefClock).
const sigOverdue = "overdue"

// sigEstimateDue: an automatic task_plan estimate reached its review point (briefClock).
const sigEstimateDue = "estimate_due"

const (
	briefCompletionReport = "report_to_supervisor"
	briefCompletionReview = "task_review"
	briefCompletionJudge  = "judge"
)

// scopeRisk are the risky words text adds that were not part of the work as known: the words of a step only the owner
// decides (ownerOnlyWords) that text names to be done (not after a negation) and known does not, and the supervisor's
// own steps (the rest of riskyWords) that known holds off (names only after a negation).
func scopeRisk(text, known string) []string {
	text, known = strings.ToLower(text), strings.ToLower(known)
	var out []string
	for _, w := range riskyWordsIn(text) {
		if !mentionedToDo(text, w) || mentionedToDo(known, w) {
			continue
		}
		// A step that is the supervisor's (merge, deploy…) is added freely, unless the owner's words on record hold it
		// off ("do not merge; stop at a PR"): that is their decision, and only they change it.
		if !slices.Contains(ownerOnlyWords, w) && !strings.Contains(known, w) {
			continue
		}
		out = append(out, w)
	}
	return out
}

// addedDeliverySteps lets scopeRisk see new merge or deploy obligations even if the change summary names only a mode.
func (s *Service) addedDeliverySteps(ctx context.Context, before, after assistantwire.CompletionMode) string {
	if after == "" {
		return ""
	}
	next := s.deliveryGateForMode(ctx, after)
	if next == deliveryGateOff {
		return ""
	}
	previous := s.deliveryGateForMode(ctx, before)
	var steps string
	if before == "" || previous == deliveryGateOff {
		steps += "\nmerge 合并 合入" // i18n-ignore: input for scopeRisk
	}
	if next == deliveryGateLive && (before == "" || previous != deliveryGateLive) {
		steps += "\ndeploy 部署 上线" // i18n-ignore: input for scopeRisk
	}
	return steps
}

// ownerOnlyWords are the risky words (riskyWords) naming a step only the owner decides (decision rights,
// docs/designs/decision-rights-20261123.md): speaking for them to other people (3), spending their money (4), deleting
// what is theirs (5). Merging, pushing, releasing and deploying work the owner ordered belong to the supervisor: a
// deploy with a backup and an automatic rollback is reversible, so it is not the owner's, and a task's scope may add
// it without asking anyone.
var ownerOnlyWords = []string{"发消息", "回复", "评论", "comment", "send", "发送", "提审", "删除", "delete", "购买", "付费", "续费", "充值"} // i18n-ignore: matched

// ownerOnRecord is what the owner already said that bears on task t, for scopeRisk's known text: the decisions the
// owner made on it and its continuation ancestors (by hand, on a card, in its thread; not an agent's, not one pulled
// out of a reply) and the standing authorizations they granted (their actions and words). What is on record is not
// asked again, and the owner is never asked to register it once more.
func (s *Service) ownerOnRecord(ctx context.Context, t Todo) string {
	var b strings.Builder
	if st := s.todoDB(); st != nil {
		for _, id := range s.continuationTodos(t.ID) {
			rows, err := st.ListTodoDecisions(ctx, id, false)
			if err != nil {
				continue
			}
			for _, r := range rows {
				switch r.Source {
				case db.DecisionManual, db.DecisionCard, db.DecisionThread:
					b.WriteString("\n" + r.Question + "\n" + r.Choice)
				}
			}
		}
	}
	if all, err := s.standingAuthorizations(ctx); err == nil {
		for _, a := range all.Authorizations {
			b.WriteString("\n" + strings.Join(a.Actions, " ") + "\n" + a.Quote)
		}
	}
	return b.String()
}

// ownerStepRoute is where conversation by takes a step only the owner may allow: a direction lead hands it to the main
// conversation (which batches what the owner must decide), never to the owner directly; the main conversation asks
// the owner, with what they said before, what is new and its recommendation.
func (s *Service) ownerStepRoute(ctx context.Context, by string) string {
	if s.isMain(ctx, by) {
		return "Ask the owner (ask_user, or task_review needs_user), with what they said before, what is new and what you " +
			"recommend, and call task_scope again in the turn their answer starts"
	}
	return "Hand it to the main conversation (conversation_send, or task_review needs_user) with the owner's words, what is " +
		"new and what you recommend; do not ask the owner yourself"
}

// scopeChange changes the fields it supplies. Approval is the owner's message that started the turn making the change.
type scopeChange struct {
	taskContract
	By, Change, Why string
	DoneWhen        []string
	Expect          int
	Approval        *BriefRequest
	eventID         string // in-memory identity of an aborted continuation's own event
	afterCommit     func() // test synchronization after the brief commit and before ledger propagation; nil in production
}

// errScopeNothing: the change changes nothing (the same criteria and time, no words).
var errScopeNothing = errors.New("nothing to change")

// applyScope appends c to the log of to-do t's brief (reconstructed first when it has none) and applies the criteria
// and expected time it leaves to the ledger of every open to-do on that brief. The brief as it is then.
func (s *Service) applyScope(ctx context.Context, t Todo, c scopeChange) (TaskBrief, error) {
	if t.DoneAt != nil {
		return TaskBrief{}, fmt.Errorf("task %s is done already: its brief is closed; hand out a new task for what is left", t.ID)
	}
	// A scope's brief commit and its ledger propagation are one ordered transition. Otherwise an older writer paused
	// after its commit can write older criteria after a newer scope has already reached the ledgers.
	b := &s.tbriefs
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	brief, err := s.ensureTaskBrief(ctx, t)
	if err != nil {
		return TaskBrief{}, err
	}
	if brief.ID == "" {
		return TaskBrief{}, fmt.Errorf("task %s has no brief", t.ID)
	}
	contract := c.taskContract.changesFrom(brief)
	if c.WorkMode.Valid() && !brief.workModeDeclared() {
		contract.WorkMode = c.WorkMode // a declaration also makes an already-effective default explicit
	}
	next := brief.Criteria
	if c.DoneWhen != nil {
		next = cleanDoneWhen(c.DoneWhen)
	}
	expect := max(0, min(c.Expect, 24*60))
	var added, dropped []string
	for _, x := range next {
		if !slices.Contains(brief.Criteria, x) {
			added = append(added, x)
		}
	}
	for _, x := range brief.Criteria {
		if !slices.Contains(next, x) {
			dropped = append(dropped, x)
		}
	}
	criteriaChanged := len(added)+len(dropped) > 0
	contractChanged := contract != (taskContract{})
	if !criteriaChanged && !contractChanged && (expect == 0 || expect == brief.ExpectNow) && strings.TrimSpace(c.Change) == "" {
		return brief, errScopeNothing
	}
	known := strings.Join(append(append([]string{brief.Goal, t.Title, brief.Handover}, brief.DoneWhen...), brief.Criteria...), "\n")
	if brief.Request != nil {
		known += "\n" + brief.Request.Text
	}
	known += s.ownerOnRecord(ctx, t)
	kind := briefEventScope
	var risk []string
	if expect > 0 && c.DoneWhen == nil && !contractChanged {
		// Estimate explanations are audit data and never extend the authorized work.
		kind = briefEventEstimate
	} else {
		steps := s.addedDeliverySteps(ctx, brief.CompletionModeNow, contract.CompletionMode)
		risk = scopeRisk(c.Change+"\n"+strings.Join(added, "\n")+steps, known)
	}
	if words := risk; len(words) > 0 && c.Approval == nil {
		return TaskBrief{}, fmt.Errorf("this change adds a step only the owner may allow (%s: speaking for them to others, "+
			"spending their money or deleting what is theirs), and neither the brief, the owner's request nor their recorded "+
			"decisions and standing authorizations say it: it was not applied. %s", strings.Join(words, ", "), s.ownerStepRoute(ctx, c.By))
	}
	scopes := 0
	for _, e := range brief.Log {
		if e.Kind == briefEventScope || e.Kind == briefEventEstimate {
			scopes++
		}
	}
	if scopes >= briefLogMax {
		return TaskBrief{}, fmt.Errorf("task %s's scope changed %d times already: this is other work by now; close it (task_review) and hand out a new task", t.ID, scopes)
	}
	estimateSource := ""
	if expect > 0 && brief.EstimateSourceNow != "" {
		estimateSource = briefEstimateOverride
	}
	ev := BriefEvent{taskContract: contract, At: time.Now().UTC(), Kind: kind, Todo: t.ID, By: c.By, Change: clip(oneLine(c.Change), 300),
		Why: clip(oneLine(c.Why), 300), Added: added, Dropped: dropped, ExpectMinutes: expect, EstimateSource: estimateSource, eventID: c.eventID}
	if criteriaChanged {
		ev.DoneWhen = next
		if ev.DoneWhen == nil {
			ev.DoneWhen = []string{}
		}
	}
	if len(risk) > 0 { // the owner's message that allowed it stays on the entry
		ev.Approval = c.Approval
	}
	brief, _, err = s.changeBriefLocked(ctx, brief.ID, func(tb *taskBrief) bool {
		tb.st.Log = append(tb.st.Log, ev)
		return true
	})
	if err != nil {
		return TaskBrief{}, err
	}
	if c.afterCommit != nil {
		c.afterCommit()
	}
	for _, id := range s.briefTodos(brief.ID) {
		o, ok := s.todoNow(id)
		if !ok || o.DoneAt != nil {
			continue
		}
		if ev.WorkMode != "" {
			s.restage(ctx, id, nil)
		}
		// A task with no ledger gets none here: a ledger moves its notices into supervision messages (#4101).
		if o.Supervision == nil {
			continue
		}
		if _, _, err := s.updateSupervision(ctx, id, func(v *Supervision) bool {
			v.DoneWhen, v.ExpectMinutes = slices.Clone(brief.Criteria), brief.ExpectNow
			if criteriaChanged {
				v.recordAssignments(o.Agents, ev.At)
			}
			if (criteriaChanged || contractChanged) && (v.State == reviewAccepted || v.State == reviewCancelled) {
				// The terminal review covered the old scope (#6880).
				v.State = ""
				if v.Round > 0 {
					v.State = reviewRework
				}
			}
			return true
		}); err != nil {
			s.logf("task %s: scope: ledger: %v", id, err)
		}
	}
	s.todosChanged(ctx)
	return brief, nil
}

// rescope is what agent_supervise and agent_send with a task do to a task of conversation self that exists already:
// self authors the scope change, an existing ledger keeps its supervisor (supervisor is the default for one without
// a ledger), and new criteria or a new expected time (none: kept) go through the brief's log as a scope change made
// through via. Its ledger as it is then.
func (s *Service) rescope(ctx context.Context, t Todo, self, supervisor, via string, doneWhen []string, expect int) (Supervision, error) {
	return s.rescopeWithEvent(ctx, t, self, supervisor, via, doneWhen, expect, "")
}

// rescopeWithEvent marks a continuation's scope event with an in-memory identity for its exact rollback.
func (s *Service) rescopeWithEvent(ctx context.Context, t Todo, self, supervisor, via string, doneWhen []string, expect int, eventID string) (Supervision, error) {
	if len(doneWhen) > 0 || expect > 0 {
		c := scopeChange{By: self, Why: "set through " + via, Expect: expect, eventID: eventID} // the tool is named in why, which is not read for risky words
		if len(doneWhen) > 0 {
			c.DoneWhen = doneWhen
			c.Change = "done_when replaced"
		}
		if o := s.callerOrigin(ctx, self); o.Request != nil && len(o.Request.Via) == 0 { // relayed words are no approval
			c.Approval = o.Request
		}
		if _, err := s.applyScope(ctx, t, c); err != nil && !errors.Is(err, errScopeNothing) {
			return Supervision{}, err
		}
	}
	// The dispatching conversation need not be its supervisor: a repeated side hand-off leaves main in charge (#5732).
	brief, briefed := s.briefOfTodo(t.ID)
	v, _, err := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
		before := v.Supervisor
		if before == "" {
			v.Supervisor = supervisor
		}
		same := before == v.Supervisor
		if briefed && (!slices.Equal(v.DoneWhen, brief.Criteria) || v.ExpectMinutes != brief.ExpectNow) {
			v.DoneWhen, v.ExpectMinutes, same = slices.Clone(brief.Criteria), brief.ExpectNow, false
		}
		return !same
	})
	return v, err
}

// toolTaskScope is the assistant's task_scope: the supervisor of a task changes what it is to do or what done means,
// as an entry in its brief's log.
func (s *Service) toolTaskScope(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	var a struct {
		taskContractInput
		TodoID        string    `json:"todo_id"`
		Change        string    `json:"change"`
		Why           string    `json:"why"`
		DoneWhen      *[]string `json:"done_when"`
		ExpectMinutes int       `json:"expect_minutes"`
		InboxItem     string    `json:"inbox_item"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	contract, err := a.validatedContract()
	if err != nil {
		return "", err
	}
	a.Change, a.Why = strings.TrimSpace(a.Change), strings.TrimSpace(a.Why)
	if a.Change == "" || a.Why == "" {
		return "", errors.New("task_scope needs change (what changes, in a sentence the owner can read) and why")
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", err
	}
	t, ok := s.todoNow(strings.TrimSpace(a.TodoID))
	if !ok {
		return "", fmt.Errorf("no task %q", a.TodoID)
	}
	if t.Origin == "" {
		return "", fmt.Errorf("%s is the user's own to-do, not a task that was handed out", t.ID)
	}
	supervisor := t.Origin
	if t.Supervision != nil && t.Supervision.Supervisor != "" {
		supervisor = t.Supervision.Supervisor
	}
	// Its supervisor only; the main conversation also for a task an earlier main conversation supervised (as task_review),
	// and the background supervisor for the tasks no leader supervises (supervise_background.go).
	if supervisor != self && !(s.isMain(ctx, self) && !s.isLeader(ctx, supervisor)) && !s.backgroundSupervises(ctx, self, t) {
		return "", fmt.Errorf("task %s is supervised by another conversation: only its supervisor changes its scope", t.ID)
	}
	// A card-less owner wait must not be pushed further out by an estimate extension or scope change (#12648); the
	// refusal happens before the brief is written.
	if err := s.cardlessOwnerWaitGuard(ctx, t); err != nil {
		return "", err
	}
	source := PlanTask{InboxItem: a.InboxItem}
	if err := s.planExistingTaskInbox(ctx, machine, self, &source, t); err != nil {
		return "", err
	}
	c := scopeChange{taskContract: contract, By: self, Change: a.Change, Why: a.Why, Expect: a.ExpectMinutes}
	if a.DoneWhen != nil {
		c.DoneWhen = cleanDoneWhen(*a.DoneWhen)
		if len(c.DoneWhen) == 0 {
			return "", errors.New("done_when: give the criteria as they are after the change (the whole list); a task is never left with none")
		}
	}
	if o := s.callerOrigin(ctx, self); o.Request != nil && len(o.Request.Via) == 0 { // relayed words are no approval
		c.Approval = o.Request
	}
	brief, err := s.applyScope(ctx, t, c)
	if errors.Is(err, errScopeNothing) {
		return "", errors.New("nothing changes: done_when and expect_minutes are as they were")
	}
	if err != nil {
		return "", err
	}
	if source.InboxItem != "" {
		var agents []SessionRef
		for _, a := range t.Agents {
			if a.ReleasedAt == nil && a.DismissedAt == nil {
				agents = append(agents, SessionRef{MachineID: a.MachineID, SessionID: a.SessionID})
			}
		}
		if err := s.linkExistingTaskInbox(ctx, self, source, t.ID, t.ID, agents); err != nil {
			return "", fmt.Errorf("scope updated on task %s, but its Inbox link was not saved: %w", t.ID, err)
		}
	}
	s.clearOverdue(t.ID)
	var b strings.Builder
	fmt.Fprintf(&b, "Logged on task %s's brief (the owner sees it in the next digest). Scope revision: %s. "+
		"Original brief goal: %q. Original criteria: %s. Effective criteria: %s. Dropped criteria: ",
		t.ID, brief.scopeRevision().Format(time.RFC3339Nano), brief.Goal, criteriaWords(brief.DoneWhen, nil), criteriaWords(brief.Criteria, nil))
	dropped := (&taskBrief{rec: brief.briefRecord, st: briefState{Log: brief.Log}}).dropped()
	for i, criterion := range dropped {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%q", criterion.Criterion)
	}
	if len(dropped) == 0 {
		b.WriteString("none")
	}
	fmt.Fprintf(&b, ". expect_minutes %d; the clock restarts and runs to %s.", brief.ExpectNow,
		brief.DueAt.In(s.location(ctx)).Format("2006-01-02 15:04"))
	if contract.WorkMode != "" {
		fmt.Fprintf(&b, " Effective work_mode is %s.", brief.WorkModeNow)
	}
	if contract.CompletionMode != "" {
		fmt.Fprintf(&b, " Effective completion_mode is %s.", brief.CompletionModeNow)
	}
	b.WriteString(" To replace the agent against this scope, pass scope_revision to task_plan with continues. To notify the same live agent, send the change with agent_send.")
	return b.String(), nil
}

// briefFromView rebuilds the mutable brief helpers without changing the stored record.
func briefFromView(v TaskBrief) *taskBrief {
	return &taskBrief{rec: v.briefRecord, st: briefState{Log: v.Log, Result: v.Result}}
}

// recordBriefCompletion keeps the first active completion of the current task_plan round. report_to_supervisor is
// normally earliest; recordBriefResult covers supervisor acceptance and judgeClose when no such report arrived.
func (s *Service) recordBriefCompletion(ctx context.Context, t Todo, source string, now time.Time) {
	if t.Supervision == nil || t.StartError != "" {
		return
	}
	brief, ok := s.briefOfTodo(t.ID)
	if !ok {
		return
	}
	tb := briefFromView(brief)
	if _, estimateSource, _ := tb.estimateState(); estimateSource == "" {
		return // manual and pre-feature work has no automatic estimate history
	}
	if tip := s.briefTip(brief.ID); tip != "" && tip != t.ID {
		return // a late report from a superseded leg is not this brief's completion
	}
	roundID, start, ok := tb.roundFor(t)
	if !ok || tb.completionFor(roundID, t.ID) != nil {
		return
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	actual := 1 // sub-minute work and an equal timestamp both remain a real, nonzero sample
	if elapsed := now.Sub(start); elapsed > 0 {
		actual = int((elapsed + time.Minute - 1) / time.Minute)
	}
	ev := BriefEvent{At: now.UTC(), Kind: briefEventCompletion, Todo: t.ID, Source: source, RoundID: roundID, ActualMinutes: actual}
	_, changed, err := s.changeBrief(ctx, brief.ID, func(current *taskBrief) bool {
		if current.completionFor(roundID, t.ID) != nil {
			return false
		}
		current.st.Log = append(current.st.Log, ev)
		return true
	})
	if err != nil {
		s.logf("task %s: completion duration: %v", t.ID, err)
		return
	}
	if changed {
		s.clearEstimateDue(t.ID) // a queued deadline must not arrive after the active done report
		s.todosChanged(ctx)
		s.learnAcceptedEstimate(ctx, t.ID)
	}
}

// estimateDueKey names one automatic estimate revision: the brief and its dispatch / estimate-change timestamp.
func estimateDueKey(brief TaskBrief) string {
	if brief.EstimateClockAt.IsZero() {
		return ""
	}
	return brief.ID + "@" + brief.EstimateClockAt.UTC().Format(time.RFC3339Nano)
}

// estimateDueCurrent says whether key still names an eligible, unanswered review deadline on t.
func (s *Service) estimateDueCurrent(t Todo, brief TaskBrief, key string) bool {
	if t.Supervision == nil || t.DoneAt != nil || brief.EstimateSourceNow == "" || brief.EstimateDueAt.IsZero() ||
		brief.EstimateDueToldAt != nil || key == "" || key != estimateDueKey(brief) {
		return false
	}
	if tip := s.briefTip(brief.ID); tip != "" && tip != t.ID {
		return false
	}
	switch st := t.Supervision.state(t); st {
	case reviewPending, reviewNeedsUser, reviewParked, reviewTimedOut, reviewClosing:
		return false
	case "":
		tb := briefFromView(brief)
		if roundID, _, ok := tb.roundFor(t); ok && tb.completionFor(roundID, t.ID) != nil {
			return false // the done report already asked for review; the judge need not have run yet
		}
	}
	return true
}

// estimateDueSignal raises the one-shot review cue when the current estimate is due. Unlike overdue, no durable mark
// is written here: an untold cue is re-derived after a restart, then estimateDueTold marks accepted delivery.
func (s *Service) estimateDueSignal(ctx context.Context, t Todo, now time.Time) {
	if t.Supervision == nil || t.DoneAt != nil {
		return
	}
	brief, ok := s.briefOfTodo(t.ID)
	if !ok || now.Before(brief.EstimateDueAt) {
		return
	}
	key := estimateDueKey(brief)
	if !s.estimateDueCurrent(t, brief, key) {
		return
	}
	s.signal(superviseSignal{Todo: t.ID, Kind: sigEstimateDue, Key: key, At: now,
		Minutes: int(now.Sub(brief.EstimateClockAt).Minutes())})
}

// estimateDueTold persists accepted delivery for every supervisor route (conversation, direct leader, resumed leader).
func (s *Service) estimateDueTold(ctx context.Context, list []superviseSignal) {
	for _, g := range list {
		if g.Kind != sigEstimateDue {
			continue
		}
		brief, ok := s.briefOfTodo(g.Todo)
		if !ok || brief.EstimateSourceNow == "" || g.Key != estimateDueKey(brief) {
			continue
		}
		if tip := s.briefTip(brief.ID); tip != "" && tip != g.Todo {
			continue
		}
		tb := briefFromView(brief)
		if tb.estimateDueToldAt() != nil {
			continue
		}
		_, changed, err := s.changeBrief(ctx, brief.ID, func(current *taskBrief) bool {
			if current.estimateDueToldAt() != nil {
				return false
			}
			current.st.Log = append(current.st.Log, BriefEvent{At: time.Now().UTC(), Kind: briefEventEstimateDue, Todo: g.Todo})
			return true
		})
		if err != nil {
			s.logf("task %s: estimate due delivery: %v", g.Todo, err)
		}
		if changed {
			s.todosChanged(ctx)
		}
	}
}

// clearEstimateDue forgets a waiting review-due signal after completion, review or re-scope.
func (s *Service) clearEstimateDue(todoID string) {
	b := &s.supervise
	b.mu.Lock()
	b.init()
	delete(b.pending, todoID+"|"+sigEstimateDue)
	b.mu.Unlock()
}

// clearOverdue forgets waiting overdue and estimate_due signals after re-scope or closure.
func (s *Service) clearOverdue(todoID string) {
	b := &s.supervise
	b.mu.Lock()
	b.init()
	delete(b.pending, todoID+"|"+sigOverdue)
	s.forgetSupervisePending(todoID + "|" + sigOverdue)
	b.mu.Unlock()
	s.clearEstimateDue(todoID)
}

// briefClock raises estimate_due at the automatic estimate and overdue at the older brief clock. The first is marked
// only after accepted delivery; the second keeps its existing mark-before-signal behavior. Both run only on the brief tip.
func (s *Service) briefClock(ctx context.Context, t Todo, now time.Time) {
	s.estimateDueSignal(ctx, t, now)
	v := t.Supervision
	if v == nil || t.DoneAt != nil {
		return
	}
	if st := v.state(t); st == reviewNeedsUser || st == reviewPending || st == reviewParked || st == reviewTimedOut || st == reviewClosing {
		return
	}
	brief, ok := s.briefOfTodo(t.ID)
	if !ok || brief.Overdue != nil || now.Before(brief.DueAt) {
		return
	}
	if s.briefTip(brief.ID) != t.ID {
		return // superseded: the to-do that took over carries the clock
	}
	clock := brief.ClockAt
	_, marked, err := s.changeBrief(ctx, brief.ID, func(tb *taskBrief) bool {
		if tb.overdueAt() != nil {
			return false
		}
		tb.st.Log = append(tb.st.Log, BriefEvent{At: now.UTC(), Kind: briefEventOverdue, Todo: t.ID})
		return true
	})
	if err != nil || !marked {
		if err != nil {
			s.logf("task %s: overdue: %v", t.ID, err)
		}
		return
	}
	s.todosChanged(ctx)
	s.signal(superviseSignal{Todo: t.ID, Kind: sigOverdue, Key: brief.ID + "@" + clock.UTC().Format(time.RFC3339),
		Minutes: int(now.Sub(clock).Minutes()), At: now})
}
