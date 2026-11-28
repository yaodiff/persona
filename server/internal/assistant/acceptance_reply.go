package assistant

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larkhost"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// passWords accepts bare approval, optionally prefixed with the Chinese word for "verify" and followed by one or two
// Chinese completion particles (as in "ok" plus a particle) or a stop. Question particles stay out, and
// both full-width and ASCII question marks are neither particles nor stops, so questions still go to the agent, and
// negations ("not passed", "can't pass", "no good") never match because the regex anchors on the approval word.
var passWords = lazyre.New(`(?i)^\s*(验收)?(通过|没问题|可以|好的?|ok|okay|lgtm|pass(ed)?|approved?|👍)(?:[了啦呀咯喽呗哒啊]{1,2})?\s*[。.!！]?\s*$`)

// conclusionQuestion handles free text on a conclusion before acceptance can take it as an answer. A displayed
// option or bare confirmation continues through the existing reply path. Other text is a conversation with the
// supervisor, so the card, its deadline and the task's decisions are untouched.
func (s *Service) conclusionQuestion(ctx context.Context, ev *botcard.MessageEvent, content *botcard.Content, features FeishuFeatures) bool {
	msg := ev.Message
	id, earlier := s.acceptFor(msg.ParentID, msg.RootID)
	if id == "" || earlier {
		return false
	}
	ask, _ := s.askCopy(id)
	text := strings.TrimSpace(content.Text)
	if !ask.Conclusion || text == "" {
		return false
	}
	todo, ok := s.todoNow(id)
	literal := unquoteOption(text)
	question := strings.ContainsAny(text, "?？") // i18n-ignore: question marks in the owner's reply, never shown
	if ask.manualTerminalAnswer(text, todo.Agents) && (!question || literal == ask.Recommend || slices.ContainsFunc(ask.Options, func(o DecisionOption) bool { return o.Label == literal })) {
		return false // acceptReply refuses the option; a question must repeat the displayed label verbatim
	}
	if !question {
		if passWords.MatchString(text) && (!decideAsk(&ask) || ask.Recommend != "") {
			return false
		}
		if decideAsk(&ask) {
			if option := exactDecisionOption(text, ask.Options); option != "" {
				content.Text = option
				return false
			}
		}
	}
	if !ok {
		return false
	}
	cfg := s.settings(ctx)
	target := botTarget{Kind: targetMain, TodoID: id}
	supervisor := cfg.Main
	if todo.Supervision != nil && todo.Supervision.Supervisor != "" {
		supervisor = todo.Supervision.Supervisor
	}
	if supervisor == cfg.Main {
		supervisor = s.ownSupervisor(cfg)
	}
	if supervisor != "" && supervisor != cfg.Main {
		if c, present := s.session(ctx, cfg.MachineID, supervisor); present && c.Assistant {
			target.Kind, target.SessionID = targetPersona, supervisor
		}
	}
	l := s.botLang(ctx)
	if err := botAllowed(l, features, target); err != nil {
		s.chat().Note(ctx, larkhost.Target{}, msg.MessageID, err.Error())
	} else if err := s.larkModule().Send(ctx, ev, *content, target); err != nil {
		s.logf("bots: conclusion question for task %s: %v", id, err)
		s.chat().Note(ctx, larkhost.Target{}, msg.MessageID, i18n.T(l, "assistant-feishu.not_sent", i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
	}
	return true
}

// acceptReply answers a reply to a "Please verify" card or a conclusion's (true: it was one): "Pass" marks it verified (a
// decision: takes the option recommended), anything else is the user's change (a decision: their choice), written into the
// todo's decisions and sent to its agent. media: the reply also carried pictures or files; only the words go on, and the
// answer says the rest did not (#5107).
func (s *Service) acceptReply(ctx context.Context, messageID, text string, media bool, ids ...string) bool {
	id, earlier := s.acceptFor(ids...)
	if id == "" {
		return false
	}
	l := s.botLang(ctx)
	text = strings.TrimSpace(text)
	if text == "" {
		if media {
			s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "assistant-reports.accept_media_dropped"))
		}
		return true
	}
	if earlier { // a card of a round before the one asking now: answered, it neither accepts nor changes this round
		s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "assistant-feishu_cards.already_handled"))
		return true
	}
	t, ok := s.todoNow(id)
	ask, _ := s.askCopy(id)
	card := slices.IndexFunc(ids, func(mid string) bool { return mid != "" && slices.Contains(ask.Messages, mid) })
	if card < 0 {
		s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "assistant-feishu_cards.already_handled"))
		return true
	}
	if decideAsk(&ask) && !ask.AcceptedAt.IsZero() { // taken, or its review moved on: over
		s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "assistant-feishu_cards.already_handled"))
		return true
	}
	if decideAsk(&ask) && (!ask.RecheckAt.IsZero() || ok && ownerDecisionReturned(t.Supervision)) {
		s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "assistant-feishu_cards.already_handled"))
		return true
	}
	// "OK" to a decision with nothing recommended takes nothing: it is their answer, and goes on as one.
	if passWords.MatchString(text) && !(decideAsk(&ask) && ask.Recommend == "") {
		// Approval on a pre-reopen card is answered but neither accepts nor reopens; a gone todo keeps old behavior.
		if ok {
			if a, _ := s.acceptAskLocked(id); a != nil && acceptAskStale(a, t) { // acceptFor read the asks
				s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "assistant-feishu_cards.already_handled"))
				return true
			}
		}
		if result, err := s.acceptTry(ctx, id, ids[card]); errors.Is(err, errAcceptStore) {
			s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "http-status.unavailable"))
		} else if note := inDoubtNote(l, err); note != "" { // it may be with the agent already: look before saying it again
			s.chat().Note(ctx, larkhost.Target{}, messageID, note)
		} else if err != nil { // the recommendation did not reach the agent: nothing recorded, "OK" can be said again
			s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "assistant-reports.accept_not_taken", i18n.Args{"error": clip(oneLine(i18n.Localize(err, l)), 120)}))
		} else if result.undelivered != nil {
			why := decisionNotDeliveredReason(result.undelivered, l)
			s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "assistant-reports.accept_decided_not_sent", i18n.Args{"error": why}))
		} else if result.taken {
			s.chat().Note(ctx, larkhost.Target{}, messageID, acceptedNote(l, &ask))
		} else { // not this round that waits: answered as its button answers (the lark module's card_actions.go), not swallowed
			s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "assistant-feishu_cards.already_handled"))
		}
		return true
	}
	// Outside decision replies take the task's action lock in answerDecisionText.
	if decideAsk(&ask) && ask.OutsideID != "" {
		result, err := s.answerDecisionText(ctx, id, ask.AskedAt.UTC().Format(timeKeyLayout), text)
		switch {
		case errors.Is(err, errAcceptStore):
			s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "http-status.unavailable"))
		case err != nil:
			s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "assistant-reports.accept_not_taken", i18n.Args{"error": clip(oneLine(i18n.Localize(err, l)), 120)}))
		case !result.taken:
			s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "assistant-feishu_cards.already_handled"))
		default:
			s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "assistant-reports.outside_answered"))
		}
		return true
	}
	s.reviewDismissals.actions.acquire(id)
	t, ok = s.todoNow(id)
	if !ok {
		s.reviewDismissals.actions.release(id)
		s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "assistant-reports.accept_gone"))
		return true
	}
	current, present := s.askCopy(id)
	if !present || !current.AskedAt.Equal(ask.AskedAt) || acceptAskStale(&current, t) ||
		decideAsk(&current) && (!current.AcceptedAt.IsZero() || acceptAskEnded(&current, t) || !current.RecheckAt.IsZero() || ownerDecisionReturned(t.Supervision)) {
		s.reviewDismissals.actions.release(id)
		s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "assistant-feishu_cards.already_handled"))
		return true
	}
	if current.manualTerminalAnswer(text, t.Agents) {
		s.reviewDismissals.actions.release(id)
		s.chat().Note(ctx, larkhost.Target{}, messageID, i18n.T(l, "assistant-reports.manual_terminal_resume"))
		return true
	}
	// Feedback starts its work round before delivery, which may wait for an offline Agent to resume.
	var reopenErr error
	if t.StartError == "" {
		reopenErr = s.reopenTodo(ctx, id)
	}
	s.reviewDismissals.actions.release(id)
	if reopenErr != nil {
		s.logf("task %s: acceptance reopen: %v", id, reopenErr)
		s.chat().Note(ctx, larkhost.Target{}, messageID, withMediaDropped(l, i18n.T(l, "assistant-reports.accept_not_sent",
			i18n.Args{"error": clip(oneLine(reopenErr.Error()), 120)}), media))
		return true
	}
	question, msg := i18n.T(l, "assistant-reports.accept_question"), i18n.T(l, "assistant-reports.accept_to_agent", i18n.Args{"feedback": text})
	switch {
	case decideAsk(&ask):
		question = cmp.Or(ask.Reason, i18n.T(l, "assistant-reports.verdict_decide"))
		msg = i18n.T(l, "assistant-reports.verdict_decided_to_agent", i18n.Args{"decision": text})
	case ask.Conclusion:
		msg = i18n.T(l, "assistant-reports.verdict_to_agent", i18n.Args{"feedback": text})
	}
	if decideAsk(&ask) {
		s.deadlineAnswered(ctx, id, ask.AskedAt) // the owner answered: no default at its deadline, even if the send fails
	}
	if st := s.todoDB(); st != nil {
		at := decisionNow()
		if ok, err := s.insertDecision(ctx, st, db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: id,
			Question: question, Choice: decisionText(text), Source: db.DecisionManual,
			SourceMessage: "feishu-" + messageID, CreatedAt: at}); err != nil {
			s.logf("task %s: acceptance decision: %v", id, err)
		} else if ok {
			s.decisionsChanged(ctx, id, at)
		}
	}
	if _, err := s.relayToTask(ctx, t.Origin, id, msg, nil); err != nil {
		s.logf("task %s: acceptance to agent: %v", id, err)
		why := clip(oneLine(err.Error()), 120)
		if !decideAsk(&ask) {
			s.chat().Note(ctx, larkhost.Target{}, messageID, withMediaDropped(l, i18n.T(l, "assistant-reports.accept_not_sent", i18n.Args{"error": why}), media))
			return true
		}
		// A decision answered in words is made even when its agent cannot take it now: recorded above, its ask is answered,
		// and its supervisor is told to pass it on. Never resent by the hub, so the agent cannot get it twice.
		s.answeredInWords(ctx, id, ask.AskedAt)
		s.decisionNotDelivered(ctx, id, ask.AskedAt, text, err)
		s.chat().Note(ctx, larkhost.Target{}, messageID, withMediaDropped(l, i18n.T(l, "assistant-reports.accept_decided_not_sent", i18n.Args{"error": why}), media))
		return true
	}
	if decideAsk(&ask) {
		s.userDecided(ctx, id) // decided: its agent's next stop goes back to the supervisor
	}
	s.answeredInWords(ctx, id, ask.AskedAt)
	s.chat().Note(ctx, larkhost.Target{}, messageID, withMediaDropped(l, i18n.T(l, "assistant-reports.accept_sent"), media))
	return true
}

// answeredInWords: the owner's words answered task id's ask. Changed again: the next deploy asks again. A decision is answered:
// its card stays known, and says so to a reply.
func (s *Service) answeredInWords(ctx context.Context, id string, askedAt time.Time) {
	b := &s.accept
	b.mu.Lock()
	a := b.asks[id]
	if a == nil || !a.AskedAt.Equal(askedAt) {
		b.mu.Unlock()
		return
	}
	decision := decideAsk(a)
	if decision {
		a.AcceptedAt = time.Now().UTC()
	} else {
		delete(b.asks, id)
	}
	s.saveAcceptLocked(ctx)
	b.mu.Unlock()
	if decision {
		s.todoPushOver(id) // answered: its next push may ring
	}
}

// withMediaDropped is note with, when the reply carried pictures or files, the line saying they did not go to the agent.
func withMediaDropped(l i18n.Lang, note string, media bool) string {
	if !media {
		return note
	}
	return note + "\n" + i18n.T(l, "assistant-reports.accept_media_dropped")
}

// acceptResult is what came of one accept operation. The zero value means the round was not taken. undelivered is
// non-nil only for a decision that was recorded and handed to its supervisor because no Agent could receive it.
type acceptResult struct {
	taken       bool
	undelivered error
}

func acceptSucceeded() acceptResult { return acceptResult{taken: true} }

// decisionNotDelivered finishes a recorded decision whose Agent could not take it. The supervisor gets one handoff per
// ask round; the hub does not resend it. The caller owns the decision row and the ask's answered marker.
func (s *Service) decisionNotDelivered(ctx context.Context, id string, askedAt time.Time, choice string, cause error) acceptResult {
	l := s.botLang(ctx)
	s.userDecided(ctx, id)
	s.signal(superviseSignal{Todo: id, Kind: sigIdle, Key: "undelivered|" + askedAt.UTC().Format(time.RFC3339Nano), Handoff: true,
		Detail: i18n.T(l, "assistant-reports.decision_not_delivered", i18n.Args{"decision": clip(oneLine(choice), 200),
			"error": decisionNotDeliveredReason(cause, l)})})
	return acceptResult{taken: true, undelivered: cause}
}

// acceptTry marks task id verified; result.taken is false when already handled, never asked, or asked before the latest
// card-less ask (NoCard) can be accepted too, from the web app. card is the message pressed ("" from a reply, which
// acceptFor already matched, from the web app, or when Feishu does not say): a card that is not this round's (an
// earlier round's, once the next one has its own) accepts nothing (#4853). A decision (a conclusion that needs the
// owner) is accepted by taking the option it recommends, which goes to the task's agent (takeRecommendation); one with
// nothing recommended, or no longer waiting, takes nothing. The error is a decision's whose recommendation did not
// reach the task's agent, or errAcceptStore when the asks cannot be read or the acceptance cannot be saved: either way
// nothing was taken, the ask still waits, and the owner can try again.
func (s *Service) acceptTry(ctx context.Context, id, card string) (acceptResult, error) {
	return s.acceptTryAs(ctx, id, card, false)
}

// acceptTryAs is acceptTry; byDefault: nobody answered decision id by its deadline and the hub takes its recommendation
// (sweepTaskDeadlines), which only a reversible decision past its deadline allows.
func (s *Service) acceptTryAs(ctx context.Context, id, card string, byDefault bool) (acceptResult, error) {
	s.reviewDismissals.actions.acquire(id)
	defer s.reviewDismissals.actions.release(id)
	return s.acceptTryAsDismissalLocked(ctx, id, card, time.Time{}, byDefault)
}

// ownerPassPending follows this round's durable owner Pass through completion, ledger and verification writes.
func ownerPassPending(a *acceptAsk, t Todo) bool {
	return a != nil && !a.AskedAt.IsZero() && !a.AcceptedAt.IsZero() &&
		!decideAsk(a) && !a.Conclusion && !a.ByAssistant && !a.BySupervisor &&
		userCheckAsked(t) && !acceptAskStale(a, t) &&
		(t.Supervision.State == reviewUserCheck ||
			t.Supervision.State == reviewAccepted && t.DoneAt != nil && !t.stageVerified)
}

// acceptTryAsDismissalLocked holds only this task's action lock across its send, not the book mutex.
func (s *Service) acceptTryAsDismissalLocked(ctx context.Context, id, card string, askedAt time.Time, byDefault bool) (acceptResult, error) {
	t, todoOK := s.todoNow(id)
	if todoOK && byDefault { // Hidden is not answered: no default is taken for a dismissed round
		if dismissed, err := s.decisionDismissed(ctx, t); err != nil || dismissed {
			return acceptResult{}, err
		}
	}
	b := &s.accept
	b.mu.Lock()
	if !s.loadAcceptLocked(ctx) {
		b.mu.Unlock()
		return acceptResult{}, errAcceptStore
	}
	a, ok := b.asks[id]
	resumeOwnerPass := ok && todoOK && ownerPassPending(a, t)
	ownerCheck := todoOK && t.Supervision != nil && (t.Supervision.State == reviewUserCheck || resumeOwnerPass)
	if !ok || a.AskedAt.IsZero() || !askedAt.IsZero() && !a.AskedAt.Equal(askedAt) ||
		!a.AcceptedAt.IsZero() && !resumeOwnerPass || (todoOK && acceptAskStale(a, t)) || (card != "" && card != a.MessageID && !slices.Contains(a.Messages, card)) ||
		decideAsk(a) && (a.Recommend == "" || todoOK && acceptAskEnded(a, t) || !a.RecheckAt.IsZero() || todoOK && ownerDecisionReturned(t.Supervision)) ||
		byDefault && !(decideAsk(a) && a.Reversible && !a.Deadline.IsZero() && a.DueAt.IsZero() && !time.Now().Before(a.Deadline)) {
		b.mu.Unlock()
		return acceptResult{}, nil
	}
	if ownerCheck && !decideAsk(a) && !a.Conclusion {
		if err := s.investigationCloseError(ctx, t); err != nil {
			b.mu.Unlock()
			return acceptResult{}, err
		}
	}
	completeDismissal := func() {}
	if a.OutsideID != "" {
		asked := a.AskedAt.UTC().Format(timeKeyLayout)
		recommend := a.Recommend
		b.mu.Unlock()
		return s.answerOutside(ctx, id, asked, outsideAnswer{Option: recommend}, byDefault)
	}
	if a.manualTerminalRecommendation(t.Agents) {
		b.mu.Unlock()
		return acceptResult{}, i18n.Err("assistant-reports.manual_terminal_resume")
	}
	if ownerCheck && !resumeOwnerPass {
		if work := s.unconfirmedTaskClose(ctx, t); work != nil {
			b.mu.Unlock()
			return acceptResult{}, errCloseNotConfirmed
		}
	}
	if resumeOwnerPass {
		// The accepted ask already records the owner's approval. Recovery may
		// finish its partial write without issuing a second confirmation.
		ctx = context.WithValue(ctx, ownerCloseKey{}, ownerCloseConfirmation{todo: t.ID, reopened: t.ReopenedAt, recovering: true})
	}
	if todoOK && !byDefault {
		var err error
		completeDismissal, err = s.prepareDismissalForAnswer(ctx, t)
		if err != nil {
			b.mu.Unlock()
			return acceptResult{}, err
		}
	}
	previousDueAt, previousAcceptedAt := a.DueAt, a.AcceptedAt
	if a.AcceptedAt.IsZero() {
		a.AcceptedAt = time.Now().UTC()
	}
	if byDefault {
		a.ByDefault, a.DueAt = true, a.AcceptedAt
	}
	if !resumeOwnerPass {
		if err := s.saveAcceptErrLocked(ctx); err != nil {
			// Kept only in memory, it would wait again after a restart: not accepted, and nothing that follows one runs.
			a.AcceptedAt, a.ByDefault, a.DueAt = previousAcceptedAt, false, previousDueAt
			b.mu.Unlock()
			s.logf("task %s: acceptance: save: %v", id, err)
			return acceptResult{}, fmt.Errorf("%w: %w", errAcceptStore, err)
		}
	}
	cp := *a
	b.mu.Unlock()
	s.askChanged(ctx) // it no longer waits
	if !decideAsk(&cp) && !cp.Conclusion {
		if ownerCheck {
			closed := false
			if t.DoneAt == nil {
				var err error
				closed, err = s.reviewDoneTodoWithSupervision(ctx, id, acceptedNote(s.botLang(ctx), &cp), true, nil, true)
				if err != nil {
					b.mu.Lock()
					if a := b.asks[id]; a != nil && a.AskedAt.Equal(cp.AskedAt) {
						a.AcceptedAt = previousAcceptedAt
						s.saveAcceptLocked(ctx)
					}
					b.mu.Unlock()
					s.askChanged(ctx)
					if isInvestigationGuardError(err) {
						return acceptResult{}, err
					}
					return acceptResult{}, fmt.Errorf("%w: %w", errAcceptStore, err)
				}
			} else if cur, ok := s.todoNow(id); ok {
				if err := s.investigationCloseError(ctx, cur); err != nil {
					if !resumeOwnerPass {
						b.mu.Lock()
						if a := b.asks[id]; a != nil && a.AskedAt.Equal(cp.AskedAt) {
							a.AcceptedAt = previousAcceptedAt
							s.saveAcceptLocked(ctx)
						}
						b.mu.Unlock()
						s.askChanged(ctx)
					}
					return acceptResult{}, err
				}
			}
			v, changed, err := s.updateSupervision(ctx, id, func(v *Supervision) bool {
				if v.State != reviewUserCheck {
					return false
				}
				v.State = reviewAccepted
				return true
			})
			if err != nil {
				s.logf("task %s: owner verification: %v", id, err)
				return acceptResult{}, fmt.Errorf("%w: %w", errAcceptStore, err)
			}
			if closed || changed {
				s.taskReviewed(ctx, v.Supervisor, id, reviewAccepted, v)
				// The owner's Pass is the acceptance task_review(user_check) deferred: the terminal cleanup the owner
				// opted into at Start follows it, under the same checks as a direct acceptance (#16948).
				s.goFinishTaskTerminal(ctx, t)
			}
		}
		completeDismissal()
		if !ownerCheck || !t.stageVerified {
			s.taskVerified(ctx, id, cp)
		}
		return acceptSucceeded(), nil
	}
	result := acceptSucceeded()
	switch {
	case decideAsk(&cp):
		var err error
		result, err = s.takeRecommendation(ctx, id, cp)
		if err != nil {
			// Not taken after all: the owner can take it again, and is told why it did not go.
			b.mu.Lock()
			if a := b.asks[id]; a != nil && a.AskedAt.Equal(cp.AskedAt) {
				// A default that did not go is not tried again (it may have reached the agent after all): its deadline stays
				// handled (DueAt) and the sweep reminds the owner instead. So is the owner's own press whose recommendation
				// may be with the agent: no default at its deadline sends it a second time.
				a.AcceptedAt, a.ByDefault = time.Time{}, false
				if !byDefault && !a.Deadline.IsZero() && a.DueAt.IsZero() && writeDelivered(err) != "none" {
					a.DueAt = time.Now().UTC()
				}
				s.saveAcceptLocked(ctx)
			}
			b.mu.Unlock()
			s.askChanged(ctx)
			return acceptResult{}, err
		}
	case cp.Conclusion:
		// Seen: the review already closed the task and told where it came from; "verified" is for deployed work.
	}
	if decideAsk(&cp) {
		s.todoPushOver(id) // taken: its next push may ring
	}
	completeDismissal()
	s.updateAskCards(ctx, id, cp)
	return result, nil
}

// taskVerified is what follows deployed task id's "Please verify" a passed (by the owner or its supervisor): the task is
// verified, its todo's chat thread and where the request came from hear it, and its cards say so.
func (s *Service) taskVerified(ctx context.Context, id string, a acceptAsk) {
	s.setTaskStageKind(ctx, id, "verify", "", "", true)
	s.chat().ThreadPost(id, larkhost.ThreadEvent{Kind: larkhost.ThreadTodoAccepted}) // and the todo's chat thread
	s.sourceReportSoon(id, sourceReportWait)                                         // and where the request came from (todo_source.go)
	s.updateAskCards(ctx, id, a)
}

// updateAskCards redraws every card of task id's ask a as it is now.
func (s *Service) updateAskCards(ctx context.Context, id string, a acceptAsk) {
	t, ok := s.todoNow(id)
	if !ok {
		return
	}
	for _, mid := range a.Messages {
		if err := s.chat().UpdateCard(ctx, mid, s.acceptCard(ctx, t, a, false)); err != nil {
			s.logf("task %s: acceptance card: %v", id, err)
		}
	}
}
