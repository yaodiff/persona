package assistant

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/i18n"
)

// ---- what the task's events say ----

// taskNotice is what happened to task t (state: done, needs, interrupted): a supervised task's supervisor gets a
// signal, any other quiet task's conversation its notice.
func (s *Service) taskNotice(ctx context.Context, t Todo, machine, state, ask, text string) {
	s.taskNoticeOf(ctx, t, machine, state, ask, text, 0, nil)
}

// taskStartFailed is taskNotice for task t's start that failed (reason) after start's retries (nil: none). Each
// attempt's failure is news of its own, even with the error the one before had: task_retry promised the supervisor
// how it ends (#6027).
func (s *Service) taskStartFailed(ctx context.Context, t Todo, machine string, start *StartRemedy, reason string) {
	attempt := 0
	if start != nil {
		attempt = start.Attempt
	}
	s.taskNoticeOfStart(ctx, t, machine, "failed", "", reason, attempt, nil, start)
}

// failedStartLauncher snapshots what actually failed, including a launcher selected by a tier or policy and recorded
// in the plan's refusal arguments. A task without those facts is not eligible for cross-task grouping.
func (s *Service) failedStartLauncher(ctx context.Context, t Todo) string {
	if _, p, i, err := s.remedyOf(ctx, t.ID); err == nil {
		if launcher := s.ranLauncher(ctx, p.Tasks[i]); launcher != "" {
			return launcher
		}
	}
	for i := len(t.Agents) - 1; i >= 0; i-- {
		if t.Agents[i].StartError != "" && t.Agents[i].Agent != "" {
			return t.Agents[i].Agent
		}
	}
	return ""
}

// taskNoticeOf is taskNotice; attempt is a failed start's retries so far, and files what a done turn handed over to
// its conversation's computer (task_files.go, #10511), which a notice lists outside its text's clip.
func (s *Service) taskNoticeOf(ctx context.Context, t Todo, machine, state, ask, text string, attempt int, files []taskFile) {
	s.taskNoticeOfStart(ctx, t, machine, state, ask, text, attempt, files, nil)
}

// taskNoticeOfStart also carries the failed start's remedy state; every other task notice passes nil.
func (s *Service) taskNoticeOfStart(ctx context.Context, t Todo, machine, state, ask, text string, attempt int, files []taskFile, start *StartRemedy) {
	// A reporting conversation keeps its own quiet notice even though main now supervises the task (#5732). Main's own
	// quiet tasks use only the supervision digest, so they are not told twice.
	if t.Quiet && t.Origin != "" {
		if _, reporting := s.reporterOf(ctx, t.Origin); reporting {
			if s.reporterStopped(ctx, t.Origin) {
				return
			}
			s.quietNotify(ctx, t, machine, state, ask, text, files)
		}
	}
	if t.Supervision == nil {
		if !t.Quiet {
			if state == "failed" && t.Origin != "" {
				s.notifyTaskFailed(ctx, t, machine, text)
				if s.settings(ctx).Main == "" {
					// Without main there is no new acceptance authority (#5732), but the dispatching conversation still
					// hears the failed start through the ordinary no-main digest fallback (#10126).
					key := text
					if attempt > 0 {
						key = fmt.Sprintf("retry %d: %s", attempt, text)
					}
					s.signal(superviseSignal{Todo: t.ID, Kind: sigFailed, Key: key, Detail: text, At: time.Now()})
				}
			}
			return
		}
		s.quietNotify(ctx, t, machine, state, ask, text, files)
		return
	}
	// A supervised task's failed start is its supervisor's business: the failed signal below carries it there, with
	// its start remedies, and only the supervisor's needs_user brings it to the owner. No push to the phone.
	g := superviseSignal{Todo: t.ID, Machine: machine, Detail: text, At: time.Now()}
	switch state {
	case "failed":
		g.Kind, g.Key = sigFailed, text
		g.Generation = g.At.UTC()
		g.Launcher = s.failedStartLauncher(ctx, t)
		g.Remedying = start != nil && start.State == remedying && t.StartError == ""
		if attempt > 0 {
			g.Key = fmt.Sprintf("retry %d: %s", attempt, text) // as the started signal names it (start_remedy.go)
		}
	case "interrupted":
		g.Kind, g.Key = sigInterrupted, time.Now().UTC().Format(time.RFC3339)
		s.signalOutageAware(ctx, t, g, nil, s.isOutageCausedInterrupted(ctx, t, g))
		return
	case "needs":
		b := &s.supervise
		b.mu.Lock()
		b.init()
		b.asks[t.ID] = ask
		b.mu.Unlock()
		if !t.Quiet {
			return // the user was pushed: the supervisor hears of it if it waits (watchSignals)
		}
		g.Kind, g.Key, g.Ask, g.Detail = sigNeedsYou, "ask@"+time.Now().UTC().Format(time.RFC3339), ask, ""
	default:
		return // done: the judge's verdict says what follows (superviseJudged)
	}
	s.signal(g)
}

// superviseJudged: supervised task t's agents all stopped after work since work, and the judge said c (err: it could
// not). Finished: the task waits for review. Not finished: the supervisor hears it stopped short.
func (s *Service) superviseJudged(ctx context.Context, t Todo, work time.Time, c taskDoneCheck, err error) {
	if s.closedSince(t.ID, work) {
		s.logf("task %s: closed since the work judged began: judged %s, dropped", t.ID, cmp.Or(c.Verdict, "with an error"))
		return
	}
	st := t.Supervision.state(t)
	if st == reviewUserCheck {
		return // the supervisor finished its review; only the owner's Pass or feedback settles this check
	}
	key := work.UTC().Format(time.RFC3339Nano)
	if err == nil && c.Verdict == verdictDone {
		if s.autoReviewBegin(ctx, t, work, c.Reason) {
			return // the independent review first; it ends in rework or the wait for review (supervise_autoreview.go)
		}
		detail := c.Reason
		closed := false
		v, _, uerr := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
			if closed = s.closedSince(t.ID, work) || reviewClosedSince(v, work); closed {
				return false // accepted or cancelled meanwhile: with the ledger's lock, so no review is undone
			}
			if v.state(t) == reviewPending && v.Waits == nil {
				return false
			}
			v.State, v.Waits = reviewPending, nil
			return true
		})
		if uerr != nil {
			s.logf("task %s: pending review: %v", t.ID, uerr)
			return
		}
		if closed {
			s.logf("task %s: closed while judged done: no review asked", t.ID)
			return
		}
		s.logf("task %s: waits for review: %s", t.ID, detail)
		s.signal(superviseSignal{Todo: t.ID, Kind: sigReview, Key: reviewKey(&v), Detail: detail})
		return
	}
	if st == reviewNeedsUser && c.Verdict != verdictNeedsUser {
		// The owner's decision is still open, but its agent went on and ended another turn: what it found may answer
		// or replace the question (#14899). An owner wait was told as one above (noteOwnerWait).
		s.needsUserAgentTurn(ctx, t, c, err)
		return
	}
	if st == reviewNeedsUser || st == reviewParked {
		return // the supervisor knows it waits: for the user, or parked until its time (parked.go)
	}
	if v := t.Supervision; t.DoneAt == nil && (v.State == reviewAccepted || v.State == reviewCancelled) && c.Verdict != verdictNeedsUser {
		// Closed by its review, open again only because its agent was sent more (taskWorking, #5892): what it did then
		// is no task stopped short. Done, it waits for review again (above); an explicit owner action still waits for
		// the owner (below); every other outcome leaves the supervisor out of it (#3111, #5732).
		s.logf("task %s: %s before, more work since judged %s: not told", t.ID, v.State, cmp.Or(c.Verdict, "with an error"))
		return
	}
	if c.Verdict == verdictNeedsUser {
		// An explicit owner action is deterministic evidence, even when the model call carrying the rest of the
		// judgment failed (#5732). It is not an unjudged stop and must still reach the owner.
		s.noteOwnerWait(ctx, t, work, c.Reason)
		return
	}
	if err != nil {
		// The judge failed (the model service, a reply that was no verdict): no word on whether it is finished, not
		// "stopped short" (#3111). The supervisor reads the agent's reply and decides. No model set up for the judge is
		// said as such, not as a model error that a retry might mend.
		detail := "判定模型出错" // i18n-ignore: the supervision message is the assistant's prompt
		if errors.Is(err, errTaskDoneJudgeExhausted) {
			detail = fmt.Sprintf("判定服务出错（自动重判 %d 次仍失败）", taskDoneDeferredRetries) // i18n-ignore: supervisor prompt
		}
		if ai.IsNotConfigured(err) {
			detail = "判定模型未配置" // i18n-ignore: the supervision message is the assistant's prompt
		}
		g := superviseSignal{Todo: t.ID, Kind: sigUnjudged, Key: key, Detail: detail, JudgeDown: true}
		s.signalOutageAware(ctx, t, g, err, s.isOutageCausedUnjudged(ctx, t, c, err))
		return
	}
	detail := verdictWords[c.Verdict]
	if c.Reason != "" {
		detail += "：" + c.Reason
	}
	if c.Verdict == verdictUnsure { // no word on it either way: not "stopped short" (#3111)
		if len(c.unlinked) != 0 {
			detail = c.Reason
		}
		g := superviseSignal{Todo: t.ID, Kind: sigUnjudged, Key: key, Detail: detail, Unlinked: c.unlinked}
		s.signalOutageAware(ctx, t, g, nil, s.isOutageCausedUnjudged(ctx, t, c, nil))
		return
	}
	if d := c.Delivery; c.Verdict == verdictUndelivered && d.held() { // where it is held, to chase (delivery.go)
		detail += "（" + deliveryLabels[d.State]
		if d.Gate != "" {
			detail += "，卡在「" + d.Gate + "」"
		}
		detail += "）"
	}
	s.signal(superviseSignal{Todo: t.ID, Kind: sigIdle, Key: key, Detail: detail})
}

// closedSince: task id is closed now (accepted, cancelled, closed by hand), or a review accepted or cancelled it after
// the stretch of work begun at work (terminal task reviews commit the ledger and closure together). A judgment of that
// stretch is moot: it neither changes the ledger nor tells the supervisor (#7539). A task opened again is judged
// again by the work after its reopening, which began after that review.
func (s *Service) closedSince(id string, work time.Time) bool {
	t, ok := s.todoNow(id)
	return !ok || t.DoneAt != nil || reviewClosedSince(t.Supervision, work)
}

// reviewClosedSince: v's latest review accepted or cancelled its task after work.
func reviewClosedSince(v *Supervision, work time.Time) bool {
	if v == nil || len(v.Reviews) == 0 {
		return false
	}
	r := v.Reviews[len(v.Reviews)-1]
	return (r.Verdict == reviewAccepted || r.Verdict == reviewCancelled) && r.At.After(work)
}

var verdictWords = map[string]string{verdictNotDone: "没做完", verdictWaiting: "在等别人或别的事", verdictNeedsUser: "要用户决定",
	verdictUnsure: "看不出做没做完", verdictUndelivered: "做完了但还没上线"}

// noteOwnerWait records that the judge found a supervised task's stretch of work stopped on a decision its agent says
// is the owner's, and hands it to the task's supervisor at once. The owner is never pushed from here: an agent asking
// for the user is the supervisor's to weigh (decision rights: only goals, what only the user knows, speaking as them,
// spending, an irreversible step not already agreed, or an action only they can do are the user's). The supervisor
// passes a real one on with task_review needs_user, which makes the card and its one push; anything else it decides
// and sends to the agent. Until then the task is not the user's: no Needs you, no badge, and it cannot be accepted
// while the wait holds (ownerWaitError). The wait ends when the agent goes on (supervisedWorking).
func (s *Service) noteOwnerWait(ctx context.Context, t Todo, work time.Time, reason string) bool {
	closed := false
	_, changed, err := s.updateSupervisionGuarded(ctx, t.ID, func(current Todo) bool {
		// task_review writes its terminal ledger before closing the todo. Check both at this single write boundary:
		// otherwise a late judgment can recreate Waits after acceptance and then be closed with that wait still live.
		closed = current.DoneAt != nil || reviewClosedSince(current.Supervision, work)
		return !closed
	}, func(v *Supervision) bool {
		if v.Waits != nil && v.Waits.Work.Equal(work) {
			return false
		}
		v.Waits = &OwnerWait{Work: work.UTC(), At: time.Now().UTC(), Reason: clip(oneLine(reason), 200)}
		if v.State != reviewNeedsUser { // a decision the supervisor already passed to the owner stays theirs
			v.State = ""
			if v.Round > 0 {
				v.State = reviewRework
			}
		}
		return true
	})
	if err != nil {
		s.logf("task %s: owner wait: %v", t.ID, err)
		return false
	}
	if closed {
		s.logf("task %s: owner wait dropped: its work was reviewed or closed", t.ID)
		return false
	}
	if changed {
		s.signal(superviseSignal{Todo: t.ID, Kind: sigIdle, Key: work.UTC().Format(time.RFC3339Nano), Detail: ownerWaitDetail(reason)})
	}
	return true
}

// ownerWaitDetail is the supervisor's message for an agent that stopped to ask for the user: the user has not been
// told, and the supervisor decides whether they should be.
func ownerWaitDetail(reason string) string {
	return "要用户决定：" + reason + "；用户还没收到。先查用户说过的话：只有方向目标、只有用户知道的事、以用户名义对外、花钱、" + // i18n-ignore: the supervision message is the assistant's prompt
		"没说过的不可逆操作、只有用户本人能做的事才交用户（task_review needs_user，写明 why_owner）；其余你来定，用 agent_send 告诉它" // i18n-ignore: the supervision message is the assistant's prompt
}

// supervisedWorking: one of supervised task t's agents started a turn: a review waiting is moot (the work goes on),
// and a parked task is parked no longer (someone sent its agent something: it is supervised as before, parked.go).
func (s *Service) supervisedWorking(ctx context.Context, t Todo) {
	s.resetBackgroundBreakerForTodo(t.ID)
	s.autoReviewWorking(ctx, t)
	// Retire the previous stop before any early return or outbox deletion. Exact-key retraction owns every idle form;
	// a later signal-id sweep could erase a newer idle admitted while the old one's final write was in flight.
	if !s.retractSupervisedIdle(ctx, t.ID) {
		s.logf("task %s: could not retire the previous stretch's idle; its durable forms stay for retry", t.ID)
	}
	b := &s.supervise
	b.mu.Lock()
	keys := []string{t.ID + "|" + sigReview, t.ID + "|" + sigReviewNudge, t.ID + "|" + sigTimedOut, t.ID + "|" + sigParkedDue, t.ID + "|" + sigEstimateDue, t.ID + "|" + sigOutageRecovery}
	for _, key := range keys {
		delete(b.pending, key)
	}
	s.forgetSupervisePending(keys...)
	b.mu.Unlock()
	if t.Supervision != nil && t.Supervision.Investigation != nil {
		// A new working turn needs its own declaration. Older findings remain in the ledger.
		if _, _, err := s.updateSupervisionGuarded(ctx, t.ID, func(cur Todo) bool { return cur.DoneAt == nil }, func(v *Supervision) bool {
			if v.Investigation == nil || v.Investigation.Stale || v.Investigation.Version == 0 {
				return false
			}
			v.Investigation.Stale = true
			return true
		}); err != nil {
			s.logf("task %s: invalidate investigation review: %v", t.ID, err)
		}
	}
	if st := t.Supervision.state(t); st != reviewPending && st != reviewParked && st != reviewTimedOut && st != reviewClosing && (t.Supervision == nil || t.Supervision.Waits == nil) {
		return
	}
	ownerWaitEnded := false
	var endedOwnerWaitKey string
	var endedPendingReview bool
	if _, _, err := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
		if v.State != reviewPending && v.State != reviewParked && v.State != reviewTimedOut && v.State != reviewClosing && v.Waits == nil {
			return false
		}
		ownerWaitEnded = v.Waits != nil
		if v.Waits != nil {
			endedOwnerWaitKey = ownerWaitCardlessKey(v.Waits)
		}
		if v.State == reviewPending {
			endedPendingReview = true
		}
		v.Waits = nil // its agent goes on: the owner's answer reached it, or the work changed (#1277)
		if v.State != reviewPending && v.State != reviewParked && v.State != reviewTimedOut && v.State != reviewClosing && v.State != reviewNeedsUser {
			return true
		}
		// A judge-written owner wait ends with this work too. A needs_user review/decision card has no Waits and was
		// returned above, so it is not cleared here (#5732).
		v.State = ""
		if v.Round > 0 {
			v.State = reviewRework
		}
		return true
	}); err != nil {
		s.logf("task %s: supervision: %v", t.ID, err)
	} else {
		// Retire queued cardless claims only after the wait/pending state write succeeds; otherwise a retry stays
		// needed if the old Waits.CardlessToldAt stamp could not be cleared (#12648).
		cardlessID := t.ID + "|" + sigCardlessWait
		b.mu.Lock()
		if p, ok := b.pending[cardlessID]; ok {
			matchesEnded := (endedOwnerWaitKey != "" && p.Key == endedOwnerWaitKey) ||
				(endedPendingReview && !ownerWaitCardlessClaim(p))
			if matchesEnded {
				delete(b.pending, cardlessID)
				s.forgetSupervisePending(cardlessID)
			}
		} else if endedOwnerWaitKey != "" || endedPendingReview {
			s.forgetSupervisePending(cardlessID)
		}
		b.mu.Unlock()
		if ownerWaitEnded {
			s.todoPushOver(t.ID) // the owner wait is over: the next one may ring
		}
	}
}

// notifyTaskFailed pushes a failure notification to the user's phone when a task started from a side conversation
// (neither supervised nor quiet) could not start, so the user is informed with next steps (#5081).
// Muted conversations send nothing; health conversations omit the task title.
func (s *Service) notifyTaskFailed(ctx context.Context, t Todo, machine, reason string) {
	here := s.settings(ctx).MachineID
	if here == "" {
		here = machine
	}
	if t.Origin == "" || s.muted(here, t.Origin) {
		return
	}
	l := s.lang(ctx)
	title := i18n.T(l, "assistant-tasks.task_failed_title", i18n.Args{"name": t.Title})
	// the lock screen does not name a task whose push says something sensitive (usage.go, scene 4)
	if space, _ := s.selfSpace(ctx, t.Origin); s.exitPolicy(ctx, t.Title+"\n"+reason, t.Origin, space).Redact {
		title = i18n.T(l, "assistant-tasks.task_failed_title_health")
	}
	r := strings.TrimRight(strings.TrimSpace(reason), ".。") // i18n-ignore: trailing full stops trimmed, not text
	body := i18n.T(l, "assistant-tasks.task_failed_body_no_reason")
	if r != "" {
		body = i18n.T(l, "assistant-tasks.task_failed_body", i18n.Args{"reason": r})
	}
	sub := s.machineName(machine)
	if sub == "" {
		sub = "Persona"
	}
	s.push(withCard(ctx, cardTaskFailed), Notification{
		CardID:    taskFailedCardID(t.ID), // started again (reopenFailedTodo): the push leaves the phone
		Subtitle:  sub,
		DeviceID:  here,
		SessionID: t.Origin,
		Title:     title,
		Body:      body,
	})
}
