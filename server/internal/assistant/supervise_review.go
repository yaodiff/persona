package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the assistant's task_review tool ----

func isFormerMain(cfg Settings, id string) bool {
	if id == "" {
		return false
	}
	for _, f := range cfg.Former {
		if f.SessionID == id {
			return true
		}
	}
	return false
}

// canReviewLedger reports whether conversation self is the single conversation currently allowed to review task t
// under ledger v. Only the task's current supervisor (or a task leader's parent / legacy background) may review it;
// main chat cannot review a task another conversation supervises.
func (s *Service) canReviewLedger(ctx context.Context, self string, t Todo, v *Supervision) bool {
	if self == "" || t.Origin == "" {
		return false
	}
	cfg := s.settings(ctx)
	isMain := s.isMain(ctx, self)
	if s.backgroundSupervises(ctx, self, t) {
		return true
	}
	if s.mayReviewForLeader(ctx, self, t) {
		return true
	}
	if v == nil || v.Supervisor == "" {
		if t.Origin != cfg.Main && s.liveSideSupervisor(ctx, t.Origin) {
			return self == t.Origin
		}
		return isMain
	}
	sup := v.Supervisor
	if s.isLeader(ctx, sup) {
		if d, _, loaded := s.directionSnapshot(ctx, sup); loaded && d.Outside != nil {
			// Legacy reporting-only assignments have no reviewer conversation; their reminders already go to main.
			return isMain
		}
	}
	// An unassigned non-leader task (such as a main-chat task after main changed) belongs to the current main
	// conversation alone.
	if !v.assigned() && !s.isLeader(ctx, sup) && !s.isLeaderRef(sup) {
		return isMain
	}
	if sup == self {
		return !isFormerMain(cfg, self)
	}
	return isMain && isFormerMain(cfg, sup)
}

func (s *Service) toolTaskReview(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		TodoID     string        `json:"todo_id"`
		Verdict    string        `json:"verdict"`
		ReworkKind string        `json:"rework_kind"`
		Checks     []ReviewCheck `json:"checks"`
		Note       string        `json:"note"`
		// The conclusion card's (conclusion.go): accepted's outcome (done, or cannot be done), its one sentence of why and
		// its one link; needs_user's recommended option.
		Outcome   string `json:"outcome"`
		Reason    string `json:"reason"`
		Link      string `json:"link"`
		Recommend string `json:"recommend"`
		// needs_user's deadline and whether its recommendation can be undone (#3127, decision_deadline.go).
		Deadline   string `json:"deadline"`
		Reversible *bool  `json:"reversible"`
		// accepted: the owner is still to look at the result themselves (a UI change someone has to see); without it the
		// supervisor's acceptance passes the task's "Please verify" (#3240).
		UserCheck bool `json:"user_check"`
		// needs_user's decision (#3804, decision.go): why only the owner can decide, the options (the recommended first,
		// each with what it leads to) and what waiting costs.
		WhyOwner      string           `json:"why_owner"`
		Options       []DecisionOption `json:"options"`
		CostOfWaiting string           `json:"cost_of_waiting"`
		QuestionQuote string           `json:"question_quote"`
		// needs_user: the decision can wait for the next daily briefing instead of a push now (#4930, decision_wait.go).
		CanWait bool `json:"can_wait"`
		// accepted: what was done beyond the brief (#3822, task_brief_result.go).
		AlsoDone []string `json:"also_done"`
		// accepted: work from the original brief left for a later decision or to-do.
		DeferredScope  string `json:"deferred_scope"`
		FollowUpTodoID string `json:"follow_up_todo_id"`
		// accepted: the supervisor takes back the needs_user it raised and accepts the task itself, quietly: no
		// conclusion card or push, its decision card closed as superseded (never recorded as the owner's choice).
		WithdrawNeedsUser bool `json:"withdraw_needs_user"`
		// parked (parked.go): until when it waits (a duration or a time; reason says for what), and on what kind of thing.
		Until     string         `json:"until"`
		WaitingOn string         `json:"waiting_on"`
		Evidence  *ParkEvidence  `json:"evidence"`
		Findings  *findingReview `json:"findings_review"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	a.Note = strings.TrimSpace(a.Note)
	a.ReworkKind = strings.TrimSpace(a.ReworkKind)
	a.DeferredScope = clip(oneLine(a.DeferredScope), 600)
	a.FollowUpTodoID = strings.TrimSpace(a.FollowUpTodoID)
	switch a.Verdict {
	case reviewAccepted, reviewRework, reviewNeedsUser, reviewParked, reviewCancelled:
	case reviewFindings:
	default:
		return "", fmt.Errorf("verdict must be accepted, rework, needs_user, parked, cancelled or findings")
	}
	if a.ReworkKind != "" && a.Verdict != reviewRework {
		return "", fmt.Errorf("rework_kind applies to rework only")
	}
	withdraw := a.WithdrawNeedsUser
	if withdraw && (a.Verdict != reviewAccepted || a.UserCheck) {
		return "", fmt.Errorf("withdraw_needs_user applies to accepted only (without user_check)")
	}
	if withdraw && a.Note == "" && strings.TrimSpace(a.Reason) == "" {
		return "", fmt.Errorf("withdraw_needs_user: say in note why the user is not needed after all (the decision card is closed with it)")
	}
	if !validReworkKind(a.ReworkKind) {
		return "", fmt.Errorf("rework_kind must be external_input, substantive_new, repeated_error or low_roi_edge")
	}
	if (a.DeferredScope != "" || a.FollowUpTodoID != "") && a.Verdict != reviewAccepted {
		return "", fmt.Errorf("deferred_scope and follow_up_todo_id apply to accepted only")
	}
	if a.FollowUpTodoID != "" && a.DeferredScope == "" {
		return "", fmt.Errorf("follow_up_todo_id requires deferred_scope")
	}
	switch a.Outcome = strings.TrimSpace(a.Outcome); a.Outcome {
	case "", outcomeDone, outcomeCannot:
	default:
		return "", fmt.Errorf("outcome must be done or cannot")
	}
	if a.Verdict == reviewRework && a.Note == "" && !slices.ContainsFunc(a.Checks, func(c ReviewCheck) bool { return !c.Met }) {
		return "", fmt.Errorf("rework: say what is missing (a check that is not met, or note)")
	}
	// A needs_user is a decision the owner can answer, or it is refused (#3804). A rework past its rounds becomes one
	// too, so it may carry the decision; whether it has to is known once the task is read (below).
	var decision reviewDecision
	decided := false
	if a.Verdict == reviewNeedsUser || a.Verdict == reviewRework && (a.WhyOwner != "" || len(a.Options) > 0) {
		d, err := readDecision(a.WhyOwner, a.Reason, a.Recommend, a.CostOfWaiting, a.Options)
		if err != nil {
			return "", err
		}
		decision, decided = d, true
	}
	if a.CanWait && !decided {
		return "", fmt.Errorf("can_wait applies to needs_user only")
	}
	// A parked task says what it waits for, on what kind of thing, and until when, or it is refused (parked.go).
	var park Parked
	if a.Verdict == reviewParked {
		p, err := readPark(a.Reason, a.Until, a.WaitingOn, a.Evidence, time.Now())
		if err != nil {
			return "", err
		}
		if err := verifyParkTarget(strings.TrimSpace(a.TodoID), p.Evidence); err != nil {
			return "", err
		}
		park = p
	} else if strings.TrimSpace(a.Until) != "" || strings.TrimSpace(a.WaitingOn) != "" || a.Evidence != nil {
		return "", fmt.Errorf("until, waiting_on and evidence apply to parked only")
	}
	deadline, err := parseDeadline(a.Deadline, time.Now())
	switch {
	case err != nil:
		return "", err
	case deadline.IsZero():
	case !decided:
		return "", fmt.Errorf("deadline applies to needs_user only")
	case a.Reversible == nil:
		return "", fmt.Errorf("deadline: say whether the recommendation can be undone (reversible); deleting, sending to other people, spending money and production changes cannot")
	case *a.Reversible && neverDefault(decision.WhyOwner):
		return "", fmt.Errorf("deadline: a decision whose why_owner is %s is never taken by default: reversible must be false (the user is only reminded at the deadline)", decision.WhyOwner)
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", err
	}
	a.TodoID = strings.TrimSpace(a.TodoID)
	if a.Verdict == reviewAccepted || a.Verdict == reviewCancelled {
		s.tasks.reviews.acquire(a.TodoID)
		defer s.tasks.reviews.release(a.TodoID)
	}
	t, ok := s.todoNow(a.TodoID)
	if !ok {
		return "", fmt.Errorf("no task %q", a.TodoID)
	}
	s.reviewDismissals.actions.acquire(t.ID)
	defer s.reviewDismissals.actions.release(t.ID)
	t, _ = s.todoNow(t.ID)
	// Exactly one conversation can review a task at a time: the task's current supervisor (main, direction lead, or
	// side conversation), a task leader's parent conversation, or the legacy background with durable evidence.
	isMain := s.isMain(ctx, self)
	background := s.backgroundSupervises(ctx, self, t)
	if !s.canReviewLedger(ctx, self, t, t.Supervision) {
		return "", fmt.Errorf("task %s was not handed out from this conversation", t.ID)
	}
	if a.Verdict == reviewFindings {
		return s.reviewInvestigationFindings(ctx, t, a.Findings, a.Checks, self, func(cur Todo) bool {
			return s.canReviewLedger(ctx, self, cur, cur.Supervision)
		})
	}
	if a.Findings != nil {
		return "", errors.New("record findings_review with verdict findings before changing task state")
	}
	if withdraw {
		if err := withdrawNeedsUserError(t, self, isMain); err != nil {
			return "", err
		}
	}
	if t.DoneAt != nil && (a.Verdict == reviewRework || a.Verdict == reviewNeedsUser || a.Verdict == reviewParked) {
		return "", fmt.Errorf("task %s is done already (%s): hand out a new task for what is left", t.ID, cmp.Or(t.DoneReason, "closed"))
	}
	if a.DeferredScope != "" && a.FollowUpTodoID == "" {
		if ask, ok := s.askCopy(t.ID); !a.UserCheck && ok && t.Supervision.state(t) == reviewNeedsUser && decisionOpen(&ask, t) {
			// Closing this to-do would retire its decide card. Keep the original decision answerable.
			return fmt.Sprintf("Task %s remains open with its existing owner decision. No acceptance was recorded.", t.ID), nil
		}
		return "", deferredScopeError("")
	}
	if t.DoneAt != nil && t.DoneBy == db.DoneByReview && a.Verdict == reviewAccepted && !a.UserCheck {
		plan, _, _ := s.taskTerminalPlan(ctx, t)
		if plan.CloseTabOnAccept || plan.TerminalCleanup != "" {
			s.sendsAccepted(ctx, t.ID, time.Now())
			return fmt.Sprintf("Task %s is already accepted and closed.", t.ID) + terminalCleanupWords(terminalCleanupStatus(plan)), nil
		}
	}
	if decided && slices.ContainsFunc(decision.Options, func(o DecisionOption) bool { return terminalResumeLabel(o.Label, t.Agents) }) {
		return "", errors.New("Resume on a terminal page is an action only the owner can perform. Put that instruction in the reason and give options the supervisor can carry out, such as starting a replacement or waiting for the owner. A decision button cannot resume an Agent")
	}
	if err := restated(t, a.Verdict, decision); err != nil {
		return "", err
	}
	if a.Verdict == reviewRework && !decided {
		if blocked, safetyCap, count := t.Supervision.reworkBlocked(a.ReworkKind); blocked {
			return "", reworkCapError(t.ID, safetyCap, count)
		}
	}
	checks := make([]ReviewCheck, 0, len(a.Checks))
	for _, c := range a.Checks {
		c.Criterion, c.Evidence = clip(oneLine(c.Criterion), 200), clip(strings.TrimSpace(c.Evidence), 600)
		if c.Criterion != "" {
			checks = append(checks, c)
		}
	}
	verdict, note := a.Verdict, clip(a.Note, 2000)
	if a.UserCheck && verdict != reviewAccepted {
		return "", fmt.Errorf("user_check applies to accepted only")
	}
	closesTask := verdict == reviewCancelled || verdict == reviewAccepted && !a.UserCheck
	var untouched SessionRef
	if closesTask {
		if activityErr := s.taskCloseActivityError(ctx, t, time.Now()); activityErr != nil {
			if verdict == reviewCancelled && !background {
				untouched = s.untouchedDispatch(ctx, t)
			}
			if untouched.SessionID == "" || s.taskCloseSnapshotErrorExcept(t, time.Now(), untouched) != nil {
				return "", s.taskCloseToolError(ctx, s.deferTaskReview(ctx, t.ID, activityErr))
			}
		}
	}
	if verdict == reviewAccepted {
		if err := s.todoCloseDeclineError(ctx, t); err != nil {
			return "", err
		}
	}
	restartUserCheck := t.Supervision.state(t) == reviewUserCheck && !closesTask && verdict != reviewAccepted
	deliveryNote := ""
	if verdict == reviewAccepted { // delivery_gate: owner wait & criteria first, then PR/issue/deploy gate (supervise_review_delivery.go)
		ctx = withFollowupWatchHints(ctx)
		if t.DoneAt == nil && t.Supervision != nil && t.Supervision.Waits != nil && !withdraw {
			w := *t.Supervision.Waits
			return "", ownerWaitError(t.ID, &w)
		}
		if a.Outcome != outcomeCannot && t.Supervision != nil {
			if err := completeReviewCriteriaError(t.Supervision.DoneWhen, a.Checks); err != nil {
				return "", err
			}
		}
		if t.DoneAt == nil && !a.UserCheck {
			if err := s.checkOwnerResultBeforeAccept(ctx, t); err != nil {
				return "", err
			}
		}
		if deliveryNote, err = s.acceptedDeliveryError(ctx, t, strings.TrimSpace(a.Link), a.Outcome); err != nil {
			return "", err
		}
	}
	// A bare accept still needs to settle any outstanding owner instruction or request. A declared contract with
	// checked criteria supplies the scope of a bare review; owner verification still checks the review evidence.
	// An outcome of cannot and an explicit needs_user withdrawal have their own closure rules.
	if verdict == reviewAccepted && t.DoneAt == nil && !a.UserCheck {
		if _, why := s.ownerInstructionBlock(ctx, t); why != "" {
			return "", errors.New("bare accepted refused: " + why + ". Ask the Agent to address it and report, revise " +
				"done_when with task_scope and re-review, or use accepted with user_check so the owner explicitly confirms the result")
		}
	}
	if verdict == reviewAccepted && a.Outcome != outcomeCannot && !withdraw {
		reason := strings.TrimSpace(a.Reason)
		guard := s.bareAcceptGuardError
		if a.UserCheck {
			guard = s.acceptEvidenceGuardError
		}
		if err := guard(ctx, t, note, reason, checks, a.AlsoDone); err != nil {
			return "", err
		}
		scope := ""
		if a.FollowUpTodoID != "" {
			scope = a.DeferredScope // the linked to-do is checked inside the write boundary below
		}
		if err := s.closeoutFollowUpError(ctx, t, scope); err != nil {
			return "", err
		}
	}
	if verdict == reviewAccepted && a.UserCheck && t.DoneAt != nil {
		if t.Supervision != nil && a.Outcome != outcomeCannot {
			if err := completeReviewCriteriaError(t.Supervision.DoneWhen, a.Checks); err != nil {
				return "", err
			}
		}
		if err := s.reopenTodo(ctx, t.ID); err != nil {
			return "", err
		}
	}
	// An explicit main acceptance records its verdict even for a legacy task without a ledger: accepted prerequisites
	// use that durable receipt to release their dependents. user_check likewise persists the owner's personal verification.
	// Dispatch-time topology is decided where the task is created, and nonterminal reviews still require an existing
	// supervised task. Cancellation needs no such receipt ledger.
	decisionPiece := verdict == reviewNeedsUser && t.Supervision == nil && s.investigationDecisionPiece(ctx, self, t, decision.Reason)
	ledger := t.Supervision != nil || isMain && verdict == reviewAccepted || a.UserCheck || decisionPiece
	if err := deferredScopeLedgerError(ledger, a.DeferredScope); err != nil {
		return "", err
	}
	if t.Supervision == nil && (verdict == reviewRework || verdict == reviewNeedsUser && !decisionPiece || verdict == reviewParked) {
		return "", fmt.Errorf("task %s is not supervised: send what is missing to its agent with agent_send, or ask the user in your answer", t.ID)
	}
	if verdict == reviewParked && s.agentSupervised(t) {
		// Its supervisor is a task leader's agent, whose supervision messages do not know parked_due (task_leader.go).
		return "", fmt.Errorf("task %s is supervised by a task leader agent: parking applies to tasks a conversation supervises", t.ID)
	}
	if verdict == reviewParked {
		if err := s.verifyParkInteraction(t, park.Evidence); err != nil {
			return "", err
		}
	}
	var priorReply *decisionAgentReply
	reworkOverPre, _, _ := t.Supervision.reworkBlocked(a.ReworkKind)
	mayRaiseDecision := verdict == reviewNeedsUser || verdict == reviewRework && decided && reworkOverPre
	if mayRaiseDecision && scheduledCheckOf(ctx) == "" {
		priorReply = s.priorDecisionReply(ctx, t, cmp.Or(strings.TrimSpace(a.QuestionQuote), decision.Reason))
	}
	if priorReply != nil {
		cur, ok := s.todoNow(t.ID)
		if !ok || cur.DoneAt != nil || !cur.UpdatedAt.Equal(t.UpdatedAt) ||
			s.activeTodoOf(priorReply.MachineID, priorReply.SessionID) != t.ID {
			return "", fmt.Errorf("the task changed while checking the Agent conversation: review it again")
		}
	}
	over, overSafetyCap, overCount := false, false, 0
	refused, refusedSafetyCap, refusedCount := false, false, 0
	parks := 0
	var waiting *OwnerWait // accepted while the work stopped on the owner's decision: refused, nothing written (#1277)
	judgedDone := false    // it was waiting for review: the judge had found it done
	wasNeedsUser := false  // transitioning out of (or updating) a pending needs_user decision (#13112)
	var supersededReason string
	var criteriaErr error
	unauthorized := false
	var v Supervision
	var closing *taskReviewClose
	if closesTask {
		reason := cmp.Or(clip(oneLine(note), 80), "验收通过")
		if verdict == reviewCancelled {
			reason = "取消：" + cmp.Or(clip(oneLine(note), 80), "不做了")
		}
		closing = &taskReviewClose{reason: reason, accepted: verdict == reviewAccepted, untouched: untouched}
	}
	var findingErr error
	var reviewedAt time.Time
	withdrawGone := false // withdraw_needs_user, but the task stopped waiting for the user meanwhile: nothing is written
	var cancelCheckedAgents []TodoAgent
	cancelWorkersGone := false
	if ledger && verdict == reviewCancelled {
		cancelCheckedAgents, cancelWorkersGone = s.checkStaleInvestigationCancelWorkers(ctx, t)
	}
	if ledger {
		var followUpInvalid bool
		var guard func(Todo) bool
		if a.FollowUpTodoID != "" || verdict == reviewParked || verdict == reviewAccepted || verdict == reviewCancelled {
			guard = func(cur Todo) bool {
				if a.FollowUpTodoID != "" {
					// Read the linked to-do inside the manager's write boundary.
					followUp, ok := s.todoNow(a.FollowUpTodoID)
					followUpInvalid = !ok || followUp.ID == cur.ID || followUp.DoneAt != nil ||
						(followUp.PartOf != cur.ID && !slices.Contains(followUp.After, cur.ID))
					if followUpInvalid {
						return false
					}
				}
				if verdict != reviewParked && verdict != reviewAccepted && verdict != reviewCancelled {
					return true
				}
				if !s.canReviewLedger(ctx, self, cur, cur.Supervision) {
					unauthorized = true
					return false
				}
				if cur.DoneAt != nil {
					return true
				}
				if !(verdict == reviewCancelled && staleInvestigationCancelAllowed(cur, cancelCheckedAgents, cancelWorkersGone)) {
					findingErr = s.investigationTransitionError(ctx, cur, cur.Supervision, park.Evidence)
				}
				if findingErr == nil && verdict != reviewParked {
					var acceptedChecks []ReviewCheck
					if verdict == reviewAccepted && a.Outcome != outcomeCannot {
						acceptedChecks = checks
					}
					findingErr = s.receivedFindingsCloseError(cur, acceptedChecks)
				}
				return findingErr == nil
			}
		}
		v, _, err = s.updateSupervisionGuardedWithClose(ctx, t.ID, guard, func(v *Supervision) bool {
			if !s.canReviewLedger(ctx, self, t, v) {
				unauthorized = true
				return false
			}
			if withdraw && v.State != reviewNeedsUser && v.Waits == nil {
				withdrawGone = true
				return false
			}
			if verdict == reviewAccepted && t.DoneAt == nil && v.Waits != nil && !withdraw {
				w := *v.Waits
				waiting = &w
				return false
			}
			if verdict == reviewAccepted && a.Outcome != outcomeCannot {
				if criteriaErr = completeReviewCriteriaError(v.DoneWhen, a.Checks); criteriaErr != nil {
					return false
				}
			}
			judgedDone = v.State == reviewPending
			wasNeedsUser = v.state(t) == reviewNeedsUser
			if isMain && (v.Supervisor == "" || isFormerMain(s.settings(ctx), v.Supervisor) ||
				!v.assigned() && !s.isLeader(ctx, v.Supervisor) && !s.isLeaderRef(v.Supervisor)) {
				v.Supervisor = self // the main conversation takes over an old main conversation's task, not another conversation's
			} else if v.Supervisor == "" {
				v.Supervisor = t.Origin
			}
			if verdict == reviewRework {
				if blocked, safetyCap, count := v.reworkBlocked(a.ReworkKind); blocked {
					if !decided {
						refused, refusedSafetyCap, refusedCount = true, safetyCap, count // another rework took the last round meanwhile: nothing is written
						return false
					}
					verdict, over, overSafetyCap, overCount = reviewNeedsUser, true, safetyCap, count
				}
			}
			if verdict == reviewParked {
				// Parked again while it still is (its agent's turn would have ended that): one more in a row.
				park.Count = 1
				if prev := v.parking(); prev != nil {
					if prev.Parks > maxReparks {
						parks = prev.Parks // refused: nothing is written
						return false
					}
					park.Count = max(1, prev.Parks) + 1
				}
			}
			r := TaskReview{At: time.Now().UTC(), Verdict: verdict, Checks: checks, Note: note, UserCheck: a.UserCheck,
				DeferredScope: a.DeferredScope, FollowUpTodoID: a.FollowUpTodoID, WithdrewNeedsUser: withdraw}
			if r.UserCheck {
				r.Reason, r.Link = strings.TrimSpace(a.Reason), validWebURL(a.Link)
			}
			reviewedAt = r.At
			v.applyChecks(checks, r.At, "supervisor:"+self)
			if verdict == reviewNeedsUser {
				r.WhyOwner, r.Reason, r.Recommend = decision.WhyOwner, decision.Reason, decision.Recommend
				r.Options, r.CostOfWaiting = decision.Options, decision.CostOfWaiting
				if !deadline.IsZero() {
					at := deadline.UTC()
					r.Deadline = &at
				}
				if a.Reversible != nil { // as the supervisor said it; absent when it did not say
					rev := *a.Reversible
					r.Reversible = &rev
				}
			}
			if verdict == reviewRework {
				round, stalled := v.applyReworkAccounting(a.ReworkKind)
				r.Round, r.ReworkKind, r.StalledReworkRounds = round, a.ReworkKind, stalled
			}
			if verdict == reviewParked {
				r.Reason, r.Until, r.WaitingOn, r.Parks = park.Reason, park.Until, park.WaitingOn, park.Count
				r.Evidence = park.Evidence.clone()
				r.Evidence.RecordedBy = self
			}
			if wasNeedsUser {
				supersededReason = reviewSupersededReason(verdict, a.Reason, note, decision.Reason, checks)
				r.SupersededReason = supersededReason
			}
			v.State, v.Waits = verdict, nil // reviewed: the supervisor answered what the judge found
			v.FollowedUpAt = &r.At
			if verdict == reviewAccepted && a.UserCheck {
				v.State = reviewUserCheck
			}
			if priorReply != nil && verdict == reviewNeedsUser {
				v.State = reviewPending
			}
			v.Reviews = append(v.Reviews, r)
			if len(v.Reviews) > 20 {
				v.Reviews = v.Reviews[len(v.Reviews)-20:]
			}
			return true
		}, closing)
		if followUpInvalid {
			return "", deferredScopeError(a.FollowUpTodoID)
		}
	} else if closing != nil {
		closing.row, err = s.commitReviewedTodo(ctx, t.ID, closing.reason, closing.accepted, nil, false)
		closing.closed = closing.row != nil
	}
	if err != nil {
		return "", s.taskCloseToolError(ctx, s.deferTaskReview(ctx, t.ID, err))
	}
	if findingErr != nil {
		return "", findingErr
	}
	if unauthorized {
		return "", fmt.Errorf("task %s was not handed out from this conversation", t.ID)
	}
	if criteriaErr != nil {
		return "", criteriaErr
	}
	if refused {
		return "", reworkCapError(t.ID, refusedSafetyCap, refusedCount)
	}
	if parks > 0 {
		return "", reparkError(t.ID, parks)
	}
	if waiting != nil {
		return "", ownerWaitError(t.ID, waiting)
	}
	if withdrawGone {
		return "", fmt.Errorf("task %s no longer waits for the user (needs_user): nothing to withdraw; review it as usual", t.ID)
	}
	if restartUserCheck {
		open := false
		if _, err := s.editTodo(ctx, t.ID, db.TodoEdit{Done: &open, RestartWork: true}); err != nil {
			return "", err
		}
		t, _ = s.todoNow(t.ID)
	}
	if a.Verdict == reviewRework { // after the judge's done, a wrong "done" in the work stats (agents_work_wrong_done.go)
		s.workEvent("", "", reviewReworkEvent, map[string]any{"todo": t.ID, "judged_done": judgedDone})
	}
	if verdict != reviewAccepted { // by the reviewer's model and effort; an acceptance once it closed the task (below)
		s.taskReviewed(ctx, self, t.ID, verdict, v)
	}
	if verdict == reviewRework || verdict == reviewNeedsUser || a.UserCheck || a.Outcome == outcomeCannot {
		s.setTaskStageKind(ctx, t.ID, "unverified", "", "", false)
	}
	b := &s.supervise
	b.mu.Lock()
	b.init()
	var keys []string
	for _, k := range []string{sigReview, sigReviewNudge, sigIdle, sigUnjudged, sigOutageRecovery, sigNeedsYou, sigStalled, sigNoProgress, sigAllStopped, sigTimedOut, sigOrphaned, sigRecovered, sigOverdue, sigEstimateDue, sigRecheck, sigUndecidable, sigDeliveryHold, sigOwnerCannot, sigParkedDue, sigCardlessWait} {
		key := t.ID + "|" + k
		delete(b.pending, key) // reviewed: what waited is answered
		keys = append(keys, key)
	}
	if verdict == reviewAccepted || verdict == reviewCancelled {
		// Closed: a decision that ended a parking has nothing left to move. A review that leaves the task open, parking
		// it again included, does not say its supervisor heard of the decision: that waits for its message (decidedPark).
		key := t.ID + "|" + sigParkDecided
		delete(b.pending, key)
		keys = append(keys, key)
	}
	extPrefix := t.ID + "|" + sigNeedsUserExternalInfo + "@"
	for k := range b.pending {
		if strings.HasPrefix(k, extPrefix) {
			delete(b.pending, k)
		}
	}
	s.forgetSupervisePending(keys...)
	s.forgetSupervisePendingPrefix(extPrefix)
	b.mu.Unlock()
	s.resetBackgroundBreakerForTodo(t.ID)
	// How it ended against what was asked (task_brief_result.go): each criterion of the brief, as scope changes left them.
	var result *BriefResult
	if verdict == reviewAccepted || verdict == reviewCancelled {
		result = s.recordBriefResult(ctx, t, checks, a.AlsoDone, "", verdict)
	}
	body := reviewFeedBody{Title: t.Title, Verdict: verdict, Round: v.Round, Note: note, Checks: checks, Result: result, SupersededReason: supersededReason}
	if verdict == reviewRework {
		body.ReworkKind = a.ReworkKind
	}
	if verdict == reviewParked { // what it waits for is what the line says; an older app shows it as the note
		body.Note, body.Until, body.WaitingOn = cmp.Or(note, park.Reason), park.Until, park.WaitingOn
	}
	if background { // it took no turn of t.Origin: the apps may draw this entry there as the only word of it
		body.By = reviewByBackground
	}
	_, _ = s.appendFeed(ctx, t.Origin, feedReview, t.ID, body)
	if wasNeedsUser && !(verdict == reviewNeedsUser && priorReply != nil) {
		// Supersede and clear any previous pending needs_user card across clients before posting any replacement card (#13112).
		s.supersedeDecision(ctx, t.ID, supersededReason)
	} else if verdict != reviewNeedsUser && verdict != reviewAccepted {
		s.supersedeDecision(ctx, t.ID, supersededReason) // the review moved on: a decision still waiting on the owner is over (conclusion.go)
	}
	conclusion := conclusionInput{Outcome: cmp.Or(a.Outcome, outcomeDone), Reason: a.Reason, Link: a.Link, Note: note, Checks: checks}
	switch verdict {
	case reviewAccepted, reviewCancelled:
		if verdict == reviewAccepted && a.UserCheck {
			s.dropTaskReport(t.ID)
			s.dropDecision(ctx, t.ID)
			s.sendsAccepted(ctx, t.ID, time.Now())
			if cur, ok := s.todoNow(t.ID); ok {
				ag := lastAgent(cur)
				s.askAcceptanceRound(cur, ag.MachineID, ag.SessionID, reviewReply(note, checks), true)
			}
			return fmt.Sprintf("Task %s is accepted by you and remains open awaiting the owner's verification (user_check). "+
				"Its Agent stays available. The owner's \"Pass\" completes it; change feedback returns it to ongoing work. "+
				"They can find the verification under Needs you in Persona. Don't ask them again. Terminal cleanup: skipped.", t.ID) + result.words() + deliveryNote, nil
		}
		reason, closed := closing.reason, closing.closed
		carded := false
		acceptedHere := false
		if t.DoneAt == nil || t.StartError != "" {
			if t.DoneAt == nil {
				acceptedHere = closed && verdict == reviewAccepted
				if closed && verdict == reviewAccepted { // accepted and closed (availability_supervision.go)
					s.taskReviewed(ctx, self, t.ID, verdict, v)
				}
				// A failed start was closed already: its review clears the error without inventing completion effects.
				switch {
				case closed && withdraw:
					// The supervisor took its needs_user back: no conclusion card, no push, no closed report (the owner
					// was never asked by anyone but the supervisor, and nothing of this is theirs to read).
					s.dropTaskReport(t.ID)
				case closed && ledger && verdict == reviewAccepted:
					// The conclusion is the one push about it: its card, not a line in the task report (#1226).
					s.dropTaskReport(t.ID)
					if cur, ok := s.todoNow(t.ID); ok {
						conclusion.Verified = supervisorVerified(cur)
						carded = s.askConclusion(ctx, cur, conclusion)
					}
				case closed:
					s.taskClosedReport(t, reason) // the bot's task report says it is closed, as for the judge's (reports.go)
				}
			}
		}
		if verdict == reviewAccepted {
			s.sendsAccepted(ctx, t.ID, time.Now()) // what was sent its agent before is answered: closed already, too (#7539)
		}
		if verdict == reviewCancelled {
			return fmt.Sprintf("Task %s is cancelled and closed; its Agent conversations stay usable. They will be stopped and "+
				"archived only when idle and the user has been inactive for 10 minutes (their terminals stay). "+
				"Tell the user only if they need to know.", t.ID), nil
		}
		completed := completionOf(t)
		if closing.row != nil {
			completed = committedCompletion(t.ID, *closing.row)
		}
		s.autoSeeAcceptedTodo(ctx, completed, conclusion.Outcome, a.UserCheck)
		authorized := isMain || background || t.Supervision != nil && t.Supervision.Supervisor == self ||
			t.Supervision == nil && t.Origin == self
		cleanup := s.finishTaskTerminal(ctx, t, acceptedHere && authorized && a.Outcome != outcomeCannot && !a.UserCheck)
		against := terminalCleanupWords(cleanup) + result.words() + deliveryNote
		if gaps := result.unsettled(); len(gaps) > 0 {
			against += " You accepted it with criteria of its brief not shown met: if one no longer applies, say so with task_scope before accepting next time."
		}
		if withdraw {
			s.todoPushOver(t.ID) // the next push about the to-do may ring again; nothing new goes out now
			return fmt.Sprintf("Task %s: you withdrew its needs_user and accepted it yourself; it is closed. Its decision card "+
				"is closed as superseded (not recorded as the user's choice), it left Needs you, and no conclusion card or "+
				"push went out. Don't tell the user unless they asked about it.", t.ID) + against, nil
		}
		if ledger && verdict == reviewAccepted {
			// Checked by the supervisor: its "Please verify" is passed, not left for the owner to press (#3240).
			if ok, err := s.verifyBySupervisor(ctx, t.ID); err != nil {
				s.logf("task %s: supervisor verify: %v", t.ID, err)
			} else if ok {
				return fmt.Sprintf("Task %s is accepted and closed, and marked verified by you: its \"Please verify\" card now says so and the "+
					"user is not asked to pass it. Tell the user the result in a line only if "+
					"they are waiting for it, and hand out or schedule anything that follows (TestFlight, closing an issue).", t.ID) + against, nil
			}
		}
		if carded {
			return fmt.Sprintf("Task %s is accepted and closed. Its conclusion card (the verdict, "+
				"why in a sentence, the link) went to the user in Lark, marked checked by you (nothing waits for them): don't push the result again. Hand out or schedule anything that "+
				"follows (deploy, TestFlight, closing an issue).", t.ID) + against, nil
		}
		return fmt.Sprintf("Task %s is accepted and closed. Tell the user the result in a line or two "+
			"if they are waiting for it, and hand out or schedule anything that follows (deploy, TestFlight, closing an issue).", t.ID) + against, nil
	case reviewNeedsUser:
		if priorReply != nil {
			choice, err := s.keepPriorDecisionReply(ctx, t, decision, priorReply, reviewedAt)
			if err != nil {
				return "", err
			}
			if choice != "" {
				return fmt.Sprintf("Recorded the owner's exact choice %q from the Agent conversation. No card was sent. Review the task's result.", choice), nil
			}
			return "The owner already replied in the Agent conversation. The reply was handed to you for review and no card was sent. Verify the result or clarify the question.", nil
		}
		conclusion.Outcome, conclusion.Reason, conclusion.Recommend, conclusion.Decision = outcomeDecide, decision.Reason, decision.Recommend, decision
		conclusion.Reviewer = self
		conclusion.Check = scheduledCheckOf(ctx) // raised by a recurring check (escalateScheduledTodo), never by a model's call
		reversible := false
		switch {
		case !deadline.IsZero():
			reversible = *a.Reversible
		case !neverDefault(decision.WhyOwner):
			// A default and a date (#3804): one that can be undone takes its recommendation after a day unless the review
			// set its own deadline. An explicit reversible=false keeps the date as a reminder only.
			deadline = time.Now().Add(decisionDefaultAfter).UTC()
			reversible = a.Reversible == nil || *a.Reversible
		}
		if !deadline.IsZero() {
			conclusion.Deadline, conclusion.Reversible = deadline, reversible
		}
		if over { // the note is the rework asked for, written to the agent: not the owner's question
			conclusion.Note = ""
		}
		// One that can wait is held for the next briefing: no card and no push now (#4930, decision_wait.go).
		held, notHeld := false, ""
		escalates := !isMain && (background || s.isLeader(ctx, v.Supervisor) || s.sideSupervisor(ctx, s.settings(ctx), v.Supervisor)) // the main conversation hears of it too
		if a.CanWait {
			held, notHeld = s.decisionWaits(ctx, decision.WhyOwner, deadline, reversible, time.Now())
			conclusion.ForBriefing = held
			if held && escalates {
				conclusion.RaisedBy = self // told when it is pushed, whoever the task's ledger names by then
			}
		}
		carded := s.askConclusion(ctx, t, conclusion)
		if held && !s.waitsForBriefing(t.ID) { // the ask was not kept as held: nothing would push it later
			held, notHeld = false, waitNotKept
		}
		// A leader's or the background supervisor's: the main conversation hears of it too (supervise_leader.go); of one
		// that is held, when it is pushed.
		if !held && escalates {
			s.escalateToMain(t, v.Round, cmp.Or(conclusion.Reason, note), decision.Recommend)
		}
		// A recommendation is also pushed to their phone, whose OK takes it (todo_ask.go), with the bot on or off. The hub
		// taking the push does not say a device got it, nor that the app shows its OK: without a card the supervisor still
		// asks, and is told OK may come first.
		pushed := ""
		switch {
		case deadline.IsZero():
			pushed = " It is never taken by default (" + decision.WhyOwner + "): the hub raises it to the user again once a day, and hands it " +
				"back to you after a day to confirm its facts still hold."
		case reversible:
			pushed = fmt.Sprintf(" If the user has not answered by %s, the hub takes the recommendation by default (it goes to the agent and is "+
				"recorded on the to-do as taken at the deadline). Their answer on the card, the push or the to-do ends that; if they answer you "+
				"anywhere else (in this chat), review the task with their decision (task_review) before then, or the recommendation is still taken.",
				deadline.In(s.location(ctx)).Format(toolTimeLayout))
		default:
			pushed = fmt.Sprintf(" It cannot be undone: at the deadline (%s) the user is only reminded again, nothing is taken.",
				deadline.In(s.location(ctx)).Format(toolTimeLayout))
		}
		overLabel := func() string {
			n := cmp.Or(overCount, maxReworkRounds)
			if overSafetyCap {
				return fmt.Sprintf(" was sent back %d times already (overall rework safety cap %d): it now", n, maxTotalReworkRounds)
			}
			return fmt.Sprintf(" was sent back %d times already: it now", n)
		}
		if held {
			was := ""
			if over {
				was = overLabel()
			}
			where := "the next daily briefing lists it with your recommendation, where they answer it; if no briefing reaches them " +
				"with it within 24 hours, or by 11 hours before its deadline, the hub pushes it after all"
			if s.briefingDue(ctx, time.Now()).IsZero() { // no briefing on: Needs you only (decision_wait.go)
				where = "no daily briefing is on, so it is never pushed: they answer it there when they look, and a reversible default " +
					"is taken at its deadline"
			}
			return fmt.Sprintf("Task %s%s waits for the user (needs_user), and its decision can wait (can_wait): no push and no card went out. "+
				"It is in their Needs you now, and %s. Don't ask them or push it yourself; review it once the agent stops.%s",
				t.ID, was, where, pushed), nil
		}
		if notHeld != "" {
			pushed += " can_wait was ignored (" + notHeld + "): it is pushed as usual."
		}
		phone := s.pushDecision(ctx, t.ID)
		if phone {
			pushed += " A push with your recommendation also went to their phone; its \"OK\" takes the recommendation and sends it " +
				"to the task's agent, which ends the decision too (drop a question of yours about it then)."
		}
		if carded {
			said := "a card with what they are to decide went to them in Lark, and their answer goes to the task's agent"
			if conclusion.Recommend != "" {
				said = "a card with what they are to decide and your recommendation went to them in Lark; \"OK\" takes the " +
					"recommendation, and their answer goes to the task's agent"
			}
			if over {
				return fmt.Sprintf("Task %s%s waits for the user (needs_user): %s. Don't ask them "+
					"again; review it once the agent stops.%s", t.ID, overLabel(), said, pushed), nil
			}
			return fmt.Sprintf("Task %s waits for the user: %s. Don't ask them again; review it once the agent stops.%s", t.ID, said, pushed), nil
		}
		if background {
			// No card went out, and the main conversation was told (escalateToMain above). A round lists no ask_user
			// (supervise_round.go roundToolsWithheld), and the owner does not read the background supervisor's folded
			// turns: what is to decide reaches them by the hub's push, or by its notify_user when there was none.
			was := ""
			if over {
				was = overLabel()
			}
			tell := "Push what is to decide, with your recommendation, to the user with notify_user (they do not read this conversation)"
			if phone {
				tell = "Don't push it again"
			}
			return fmt.Sprintf("Task %s%s waits for the user (needs_user), and the main conversation was told. %s; review it again once "+
				"they decide (that also ends the decision the briefing lists for it).%s", t.ID, was, tell, pushed), nil
		}
		if over {
			return fmt.Sprintf("Task %s%s waits for the user (needs_user). Tell the user what is "+
				"still missing and ask how to go on (ask_user or in your answer); review it again once they decide (that also ends the decision "+
				"the briefing lists for it).%s", t.ID, overLabel(), pushed), nil
		}
		return fmt.Sprintf("Task %s waits for the user. Ask them (ask_user, or in your answer, pushed with notify_user if they are away); "+
			"review it again once they decide (that also ends the decision the briefing lists for it).%s", t.ID, pushed), nil
	case reviewParked:
		return parkedAnswer(t.ID, park, s.location(ctx)), nil
	}
	displayMax := maxTotalReworkRounds
	if isStalledReworkKind(a.ReworkKind) && v.Round <= maxReworkRounds {
		displayMax = maxReworkRounds
	}
	text := prompts.RenderSuperviseRework(prompts.SuperviseRework{Round: v.Round, Max: displayMax, Checks: reworkChecks(checks), Note: note})
	// The task's origin still owns delivery. Attribute an owner-only refusal to the supervisor who tried this rework.
	sent, err := s.sendToTask(context.WithValue(ctx, reworkReviewerKey{}, beginResumeReview(self)), t, text)
	if err != nil {
		return "", fmt.Errorf("recorded rework round %d, but sending it to the agent failed: %v (send it with agent_send)", v.Round, err)
	}
	stalled := v.stalledReworkRounds()
	if isStalledReworkKind(a.ReworkKind) && v.Round == stalled {
		return fmt.Sprintf("Rework round %d of %d is %s. ", v.Round, maxReworkRounds, sent) + reviewAgainNote, nil
	}
	if a.ReworkKind != "" {
		return fmt.Sprintf("Rework round %d of %d (%s; stalled/low-ROI %d of %d) is %s. ", v.Round,
			maxTotalReworkRounds, a.ReworkKind, stalled, maxReworkRounds, sent) + reviewAgainNote, nil
	}
	return fmt.Sprintf("Rework round %d of %d (stalled/low-ROI %d of %d) is %s. ", v.Round,
		maxTotalReworkRounds, stalled, maxReworkRounds, sent) + reviewAgainNote, nil
}

// withdrawNeedsUserError refuses task_review withdraw_needs_user on task t from conversation self unless self
// supervises t (its ledger names it, or it is the main conversation) and t still waits for the user (needs_user).
func withdrawNeedsUserError(t Todo, self string, isMain bool) error {
	if t.Supervision == nil || !isMain && t.Supervision.Supervisor != self {
		return fmt.Errorf("task %s: only its supervisor or the main conversation withdraws its needs_user", t.ID)
	}
	// A judge's owner wait the supervisor holds (noteOwnerWait) is withdrawn the same way: the supervisor found it is
	// not the user's and the work is done.
	if t.DoneAt != nil || t.Supervision.state(t) != reviewNeedsUser && t.Supervision.Waits == nil {
		return fmt.Errorf("task %s does not wait for the user (needs_user): nothing to withdraw; review it as usual", t.ID)
	}
	return nil
}

// reviewAgainNote ends every rework reply: the supervisor is told where its next look comes from.
const reviewAgainNote = "When the agent stops again you get a 督办 message to review it again."

// reviewReply is what a review says of the work, as the reply a "Please verify" card's three lines are read from.
func reviewReply(note string, checks []ReviewCheck) string {
	lines := []string{note}
	for _, c := range checks {
		lines = append(lines, c.Criterion+": "+c.Evidence)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func deferredScopeLedgerError(ledger bool, deferredScope string) error {
	if !ledger && deferredScope != "" {
		return errors.New("accepted: deferred scope requires a review ledger. Ask the main conversation to review it or use accepted with user_check: true. Nothing was written.")
	}
	return nil
}

func deferredScopeError(followUpTodoID string) error {
	detail := ""
	if followUpTodoID != "" {
		detail = fmt.Sprintf(" Follow-up %q must be open and linked through part_of or after.", followUpTodoID)
	}
	return fmt.Errorf("accepted: deferred scope has no owner action.%s Use needs_user for a decision or "+
		"name an open linked follow-up to-do in follow_up_todo_id. Nothing was written.", detail)
}

func reworkChecks(list []ReviewCheck) []prompts.SuperviseCheck {
	out := make([]prompts.SuperviseCheck, 0, len(list))
	for _, c := range list {
		out = append(out, prompts.SuperviseCheck{Criterion: c.Criterion, Met: c.Met, Evidence: c.Evidence})
	}
	return out
}

// feedReview is a review in the conversation's feed: ref the todo id, body reviewFeedBody.
const feedReview = "review"

// reviewByBackground is reviewFeedBody.By of a review the background supervisor wrote.
const reviewByBackground = "background"

type reviewFeedBody struct {
	Title      string        `json:"title"`
	Verdict    string        `json:"verdict"`
	Round      int           `json:"round,omitempty"`
	ReworkKind string        `json:"rework_kind,omitempty"`
	Note       string        `json:"note,omitempty"`
	Checks     []ReviewCheck `json:"checks,omitempty"`
	// By: reviewByBackground when the background supervisor wrote it (supervise_background.go); "" for the review of
	// the conversation that handed the task out, a leader's, the main conversation's and a sampled acceptance.
	By string `json:"by,omitempty"`
	// A parked review's (parked.go): until when it waits and on what kind of thing; Note says for what.
	Until     time.Time `json:"until,omitzero"`
	WaitingOn string    `json:"waiting_on,omitempty"`
	// Result: an accepted or cancelled review against the task's brief (task_brief_result.go): each criterion met,
	// not_met, unchecked or dropped, then what was done beyond it.
	Result *BriefResult `json:"result,omitempty"`
	// SupersededReason records why this review superseded an earlier pending needs_user decision (#13112).
	SupersededReason string `json:"superseded_reason,omitempty"`
}

// reviewSupersededReason extracts the supervisor's natural-language reason when task_review supersedes a pending needs_user.
func reviewSupersededReason(verdict, reason, note, decisionReason string, checks []ReviewCheck) string {
	reason = strings.TrimSpace(reason)
	switch verdict {
	case reviewRework:
		if note != "" {
			return clip(oneLine(note), 300)
		}
		for _, c := range checks {
			if !c.Met {
				if c.Evidence != "" {
					return clip(oneLine(c.Criterion+": "+c.Evidence), 300)
				}
				return clip(oneLine(c.Criterion), 300)
			}
		}
	case reviewNeedsUser:
		if s := cmp.Or(note, decisionReason, reason); s != "" {
			return clip(oneLine(s), 300)
		}
	default:
		if s := cmp.Or(reason, note); s != "" {
			return clip(oneLine(s), 300)
		}
	}
	return ""
}

// updateSupervisionWithCloseGuarded applies the current-todo admission and the live-work close guard in one write
// boundary. The investigation guard must run before either durable write; the close guard runs again before commit.
func (s *Service) updateSupervisionWithCloseGuarded(ctx context.Context, id string, guard func(Todo) bool, change func(*Supervision) bool, closing *taskReviewClose) (Supervision, bool, error) {
	if closing == nil {
		return s.updateSupervisionGuarded(ctx, id, guard, change)
	}
	s.loadSeen(ctx)
	_ = s.loadTaskActivity(ctx)
	// Match guarded supervision writes: the to-do writer must be acquired before the ledger writer.
	s.todos.writing.Lock()
	s.supervise.writeMu.Lock()
	v, changed, err := s.updateSupervisionGuardedLockedAt(ctx, id, time.Now().UTC(), guard, change, closing)
	s.supervise.writeMu.Unlock()
	s.todos.writing.Unlock()
	if closing.after != nil {
		closing.after()
	}
	return v, changed, err
}

// reviewDoneTodo closes todo id after its review (done_by review or cancelled, with reason); false when it was done already. An
// accepted task's outcome is recorded, as when it closes by itself or the user closes it (todo_outcome.go, #2820); a
// cancelled one came to nothing and records none.
func (s *Service) reviewDoneTodo(ctx context.Context, id, reason string, accepted bool) (bool, error) {
	return s.reviewDoneTodoWithSupervision(ctx, id, reason, accepted, nil, false)
}

func (s *Service) reviewDoneTodoWithSupervision(ctx context.Context, id, reason string, accepted bool, ledger *Supervision, ownerPass bool) (bool, error) {
	row, err := s.commitReviewedTodo(ctx, id, reason, accepted, ledger, ownerPass)
	return row != nil, err
}

func (s *Service) commitReviewedTodo(ctx context.Context, id, reason string, accepted bool, ledger *Supervision, ownerPass bool) (*db.TodoRow, error) {
	if !ownerPass {
		s.loadSeen(ctx)
		_ = s.loadTaskActivity(ctx)
	}
	s.todos.writing.Lock()
	if current, ok := s.todoNow(id); ok && current.DoneAt == nil {
		if err := s.reviewInvestigationCloseError(ctx, current, ledger == nil && !accepted); err != nil {
			s.todos.writing.Unlock()
			return nil, err
		}
	}
	row, after, err := s.reviewDoneTodoLocked(ctx, id, reason, accepted, ledger, ownerPass, SessionRef{})
	s.todos.writing.Unlock()
	if after != nil {
		after()
	}
	return row, err
}

func (s *Service) reviewInvestigationCloseError(ctx context.Context, t Todo, allowStaleCancellation bool) error {
	if allowStaleCancellation {
		checked, gone := s.checkStaleInvestigationCancelWorkers(ctx, t)
		if staleInvestigationCancelAllowed(t, checked, gone) {
			return s.receivedFindingsCloseError(t, nil)
		}
	}
	return s.investigationCloseError(ctx, t)
}

// reviewDoneTodoLocked commits the closure while todos.writing is held and returns its post-commit effects.
func (s *Service) reviewDoneTodoLocked(ctx context.Context, id, reason string, accepted bool, ledger *Supervision, ownerPass bool, untouched SessionRef) (*db.TodoRow, func(), error) {
	guard := func() error {
		if t, ok := s.todoNow(id); ok {
			if !ownerPass {
				return s.taskCloseSnapshotErrorExcept(t, time.Now(), untouched)
			}
			if work := s.unconfirmedTaskClose(ctx, t); work != nil {
				return &taskCloseHoldError{todo: t.ID, work: work}
			}
		}
		return nil
	}
	if err := guard(); err != nil {
		return nil, nil, err
	}
	if t, ok := s.todoNow(id); ok && t.isConcern() {
		return nil, nil, nil // only its report closes a concern (concerns.go)
	}
	now := time.Now().UTC()
	var row db.TodoRow
	st := s.supervisionDB()
	persisted := st != nil && s.todoDB() != nil
	if persisted {
		var review *db.TodoSupervisionRow
		if ledger != nil {
			r := ledger.row(id)
			review = &r
		}
		r, ok, err := st.ReviewTodo(ctx, id, reason, accepted, now, review, guard)
		if err != nil || !ok {
			return nil, nil, err
		}
		row = *r
	} else {
		t, ok := s.todoNow(id)
		if !ok {
			return nil, nil, db.ErrTodoNotFound
		}
		row = db.TodoRow{ID: t.ID, Title: t.Title, Notes: t.Notes, CreatedAt: t.CreatedAt, Origin: t.Origin, PlanID: t.PlanID,
			ReopenedAt: t.ReopenedAt, Quiet: t.Quiet}
		reviewClose(&row, reason, accepted, now)
		t.keepPlan(&row)
	}
	var before Todo
	var activityErr error
	_, doneKeys, ok := s.setTodoRow(id, row, func(t *Todo) bool {
		if !persisted && !ownerPass {
			if activityErr = s.taskCloseSnapshotErrorExcept(*t, time.Now(), untouched); activityErr != nil {
				return false
			}
		}
		if t.DoneAt == nil || t.StartError != "" || s.supervisionDB() != nil {
			before = copyTodoLocked(t) // its agents as they worked on it, before done releases them
			if ledger != nil {
				next := ledger.clone()
				if t.Supervision != nil {
					next.Progress = t.Supervision.clone().Progress
				}
				t.Supervision = next
			}
			return true
		}
		return false
	})
	if activityErr != nil {
		return nil, nil, activityErr
	}
	if !ok {
		return nil, nil, nil
	}
	return &row, func() {
		s.todoRowSet(ctx, doneKeys)
		if before.failedStart() {
			if p, _, err := s.currentPlanAssignment(ctx, before); err == nil {
				s.tasks.planEdits.acquire(p.ID)
				if current, err := s.loadPlan(ctx, p.ID); err == nil {
					s.publishTaskPlan(ctx, current)
				}
				s.tasks.planEdits.release(p.ID)
			}
		}
		s.afterReviewClose(ctx, before, row, accepted)
	}, nil
}

// reviewClose is a review's close of row without the database, the one place it is written: done at at for reason,
// by the review when it accepted the work and cancelled when not. It only sets those four fields; storing the row and
// what follows a close stay with its callers (reviewDoneTodoLocked, and mergeTodos closing its source).
func reviewClose(row *db.TodoRow, reason string, accepted bool, at time.Time) {
	row.DoneAt, row.DoneBy, row.DoneReason, row.UpdatedAt = &at, db.DoneByReview, reason, at
	if !accepted {
		row.DoneBy = db.DoneByCancelled
	}
}

func (s *Service) afterReviewClose(ctx context.Context, before Todo, row db.TodoRow, accepted bool) {
	id, reason := row.ID, row.DoneReason
	// Recording an outcome and telling the planning conversation belong to a newly closed open task, not a failed start.
	if before.DoneAt == nil {
		s.todoPushOver(id) // closed: nothing about it waits on the owner any more
		if accepted && row.DoneAt != nil {
			s.goRecordTaskOutcome(before, *row.DoneAt)
			s.learnAcceptedEstimate(ctx, id)
			s.goLearnTaskLessons(ctx, before, *row.DoneAt) // what it taught its project's next Agents (task_lessons.go)
			// Post-closure PR lifecycle: keep watching this to-do's own still-open delivery PRs without reopening it (#12371).
			if cur, ok := s.todoNow(id); ok {
				s.createPRFollowups(ctx, cur)
			}
		}
		s.onTodoWorkspaceReleased(ctx, before, accepted)
		if accepted && row.DoneAt != nil {
			s.evaluateTodoAfterDependents(ctx, id)
			s.evaluateParentCloseoutForChild(ctx, id)
		}
		if s.settings(ctx).todosV2On() {
			if recErr := s.ResetSupervisedTaskRecovery(ctx, id); recErr != nil {
				s.logf("task %s: reset todos_v2 restart recovery: %v", id, recErr)
			}
			if accepted && row.DoneAt != nil {
				if _, blkErr := s.ReconcileTodoBlockers(ctx, id); blkErr != nil {
					s.logf("task %s: reconcile todos_v2 blockers: %v", id, blkErr)
				}
			}
		}
		if t, ok := s.todoNow(id); ok {
			// A side conversation's task the main conversation supervised: the planning conversation hears it ended (#4669).
			go s.taskEndedNotice(context.WithoutCancel(ctx), t, accepted, reason)
		}
		s.evaluateTodoAfterDependents(ctx, id)
	}
}

// pickTaskAgent is the agent of a task that most needs what is sent to it: one waiting for the user, then working,
// then done unseen, then the latest.
func pickTaskAgent(agents []proto.Session) proto.Session {
	rank := map[string]int{proto.StateNeedsYou: 0, proto.StateWorking: 1, proto.StateUnread: 2}
	list := slices.Clone(agents)
	sort.SliceStable(list, func(i, j int) bool {
		ri, ok := rank[list[i].State]
		if !ok {
			ri = 3
		}
		rj, ok := rank[list[j].State]
		if !ok {
			rj = 3
		}
		if ri != rj {
			return ri < rj
		}
		return list[i].UpdatedAt.After(list[j].UpdatedAt)
	})
	return list[0]
}

// sendToTask sends text to task t's agent (pickTaskAgent) and keeps it in the conversation's feed as Persona's
// (routedFromAssistant); an agent closed after a stretch of work is resumed first (that can take minutes: it goes on in
// the background). What it says about it: "sent to the agent" or "being sent".
func (s *Service) sendToTask(ctx context.Context, t Todo, text string) (string, error) {
	return s.sendTaskInput(ctx, t, text, false, nil, true, nil)
}

// sendToTaskAs is sendToTask; with wait, a closed agent's resume and send is waited for too (up to 5 minutes), so the
// caller learns whether the text reached it (inboxToAgent, #7346). It is a relay, not rework, and never resumes a stopped Agent.
func (s *Service) sendToTaskAs(ctx context.Context, t Todo, text string, wait bool) (string, error) {
	return s.sendTaskInput(ctx, t, text, wait, nil, false, nil)
}

func (s *Service) sendTaskInput(ctx context.Context, t Todo, text string, wait bool, event *taskInboxEvent, rework bool, beforeSend func(context.Context) error) (string, error) {
	agents := s.taskAgents(ctx, t)
	if len(agents) == 0 {
		return "", errors.New("the task has no agent")
	}
	if event != nil && event.Retried {
		agents = slices.DeleteFunc(agents, func(c proto.Session) bool {
			return c.MachineID != event.Machine || c.SessionID != event.Session
		})
		if len(agents) == 0 {
			return "", errors.New("the event's agent is no longer available")
		}
	}
	live, _ := liveTaskAgents(agents)
	resumeStopped := false
	if len(live) == 0 {
		if event != nil && event.Retried {
			return "", errors.New("the event's agent is stopped and read-only")
		}
		if !rework {
			return "", errTaskAgentsStopped // relays, digests and external events never resume a stopped Agent
		}
		if _, ok := s.taskSpaceOf(ctx, t.ID); !ok {
			return "", errTaskAgentsStopped // other stopped tasks' follow-ups still start nothing (tasks_successor.go)
		}
		resumeStopped = true
	}
	c := pickTaskAgent(agents)
	if len(live) > 0 {
		c = pickTaskAgent(live)
	}
	if _, ok := s.device(ctx, c.MachineID); !ok {
		return "", errors.New("its machine is offline")
	}
	if resumeStopped {
		return s.resumeStoppedTaskAgent(ctx, t, c, text)
	}
	send := func(ctx context.Context) error {
		// The user may complete or reopen the task during session lookup or before an offline send starts.
		cur, ok := s.todoNow(t.ID)
		if !ok {
			return db.ErrTodoNotFound
		}
		if cur.DoneAt != nil || cur.ReopenedAt != nil && (t.ReopenedAt == nil || !cur.ReopenedAt.Equal(*t.ReopenedAt)) {
			return db.ErrTodoCompleted
		}
		if beforeSend != nil {
			if err := beforeSend(ctx); err != nil {
				return err
			}
		}
		if event != nil {
			if !event.Retried {
				event.Machine, event.Session = c.MachineID, c.SessionID
			}
			duplicate, err := s.recordTaskInboxEvent(ctx, *event)
			if err != nil {
				return err
			}
			if duplicate {
				return nil
			}
		}
		if err := s.call(ctx, c.MachineID, DeviceSessionSend, map[string]any{"session_id": c.SessionID, "text": text}, nil); err != nil {
			return err
		}
		_, err := s.appendFeed(ctx, t.Origin, feedRouted, t.ID, map[string]string{"text": text, "from": routedFromAssistant,
			"machine_id": c.MachineID, "session_id": c.SessionID}) // the run it told (#10465)
		if err != nil && event != nil {
			// The node already accepted this event; a feed failure cannot revoke delivery.
			s.logf("task %s: external-event delivery feed: %v", t.ID, err)
			return nil
		}
		return err
	}
	if beforeSend != nil {
		// A bounded source sweep must keep its deadline even if the session needs to resume.
		if err := send(ctx); err != nil {
			return "", err
		}
		return "sent to the agent", nil
	}
	if c.Status == proto.StatusOffline && wait {
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		if err := send(sctx); err != nil {
			return "", err
		}
		return "sent to the agent after resuming it", nil
	}
	if c.Status == proto.StatusOffline {
		go func() {
			sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
			defer cancel()
			if err := send(sctx); err != nil {
				s.logf("task %s: rework: %v", t.ID, err)
			}
		}()
		return "being sent: its agent was closed and is resuming first", nil
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	if err := send(sctx); err != nil {
		return "", err
	}
	return "sent to the agent", nil
}

// resumeStoppedTaskAgent resumes only an explicit rework of a recorded herdr leader task. The ordinary guarded
// resume path supplies context compaction, stop provenance, the task workspace, and the resume note on the card.
func (s *Service) resumeStoppedTaskAgent(ctx context.Context, t Todo, c proto.Session, text string) (string, error) {
	am, ok := s.device(ctx, c.MachineID)
	if !ok {
		return "", errors.New("its machine is offline")
	}
	reviewer, _ := ctx.Value(reworkReviewerKey{}).(resumeReview)
	if err := s.resumeAllowed(ctx, t.Origin, am, c, "rework of this task", nil, reviewer); err != nil {
		return "", err
	}
	return s.resumeAndSend(ctx, t.Origin, am, c, map[string]any{"text": text}, "rework of this task", text, "", nil)
}

// ownerWaitError refuses accepting task id while its latest stretch of work stopped on a decision only the owner can
// make (OwnerWait, #1277): closing it would drop the decision, the way a task whose drafts wait for the owner to pick one
// was once accepted as done.
func ownerWaitError(id string, w *OwnerWait) error {
	why := ""
	if w.Reason != "" {
		why = fmt.Sprintf(" (the done judge: %q)", w.Reason)
	}
	return fmt.Errorf("task %s stopped on a decision its agent says only the owner can make%s: accepting it would close "+
		"the task and lose that decision. If it is the owner's (goals, what only they know, speaking as them, spending, "+
		"an irreversible step they have not agreed to, an action only they can do), review it needs_user as a decision "+
		"they can answer (why_owner, reason, recommend, options, cost_of_waiting); otherwise decide it yourself, or if "+
		"the owner already decided, send the answer to its agent with agent_send and review the work it does next; or "+
		"cancelled when it is no longer wanted", id, why)
}

// reworkCapError refuses a rework past its cap (either the strict stalled/low-ROI cap or the overall
// safety valve) that states no decision: the task goes to the owner only as a decision they can answer (#3804).
func reworkCapError(id string, safetyCap bool, count int) error {
	if safetyCap {
		return fmt.Errorf("task %s was sent back %d times already (overall rework safety cap %d): further rework is refused regardless of rework_kind. "+
			"Review it needs_user with why_owner, reason (what is to be decided: how it should go on), recommend, options "+
			"(2 to 4, each {label, leads_to}: one more round, taking it as it is, dropping it) and cost_of_waiting; or accept it as it "+
			"is (accepted), or end it (cancelled)", id, count, maxTotalReworkRounds)
	}
	return fmt.Errorf("task %s was sent back %d times already (stalled/low-ROI cap %d): a third stalled/low-ROI rework goes to the user, and only as a decision they can "+
		"answer. Review it needs_user with why_owner, reason (what is to be decided: how it should go on), recommend, options "+
		"(2 to 4, each {label, leads_to}: one more round, taking it as it is, dropping it) and cost_of_waiting; or accept it as it "+
		"is (accepted; prefer accepted with a brief note over low-ROI edge cases), or end it (cancelled)", id, maxReworkRounds, maxReworkRounds)
}

// restated checks a needs_user against what the owner answered the task's decision before (decision.go): after "You
// decide" the same question is not asked again, and one that cannot be undone or that others will see comes back only
// as a plain approval (two options: go ahead or not).
func restated(t Todo, verdict string, d reviewDecision) error {
	v := t.Supervision
	if verdict != reviewNeedsUser || v == nil || len(v.Reviews) == 0 {
		return nil
	}
	prev := v.Reviews[len(v.Reviews)-1]
	if prev.Verdict != reviewNeedsUser || prev.OwnerResponse != respondDecideYourself {
		return nil
	}
	if neverDefault(prev.WhyOwner) {
		if len(d.Options) != 2 {
			return fmt.Errorf("the user answered this decision \"You decide\", and it cannot be taken for them (%s): restate it as a "+
				"plain approval with exactly two options (go ahead, or not), each leads_to spelling out the consequence", prev.WhyOwner)
		}
		return nil
	}
	if strings.EqualFold(d.Reason, prev.Reason) {
		return errors.New("the user answered this decision \"You decide\": don't ask them the same thing again. Decide it, tell the " +
			"task's agent (agent_send) and say in a line what you assumed. " + decisionElse)
	}
	return nil
}
