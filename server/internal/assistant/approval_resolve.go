package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// resolvePreparedAnswer claims once, rechecks and applies through the retained
// owner operation, then records the honest terminal outcome. In particular it
// never treats clarification, a proxy answer or a remembered grant as Allow.
func (s *Service) resolvePreparedAnswer(ctx context.Context, p *pending, r proto.Resolution, remember bool, authorization *standingResolution) error {
	return s.resolvePreparedAnswerAt(ctx, p, r, remember, authorization, nil)
}

func (s *Service) resolvePreparedAnswerAt(ctx context.Context, p *pending, r proto.Resolution, remember bool, authorization *standingResolution, expected *interactionExpectation) error {
	if authorization != nil || r.Remembered {
		// Other prepared operations still require a fresh owner decision. A
		// detachable command is the narrow exception: before release its original
		// hook executes once under the new Always rule; after release Apply only
		// records a no-retry covered outcome and creates no grant.
		if authorization != nil || !p.ix.Detachable {
			return errors.New("a standing or remembered authorization cannot approve a prepared card")
		}
	}
	if !editedAnswerAllowed(p.ix, r) {
		return errInvalidEditedAnswer
	}
	for _, f := range p.ix.Fields {
		if r.Allow != nil && *r.Allow && !f.Optional && strings.TrimSpace(r.Values[f.Key]) == "" {
			return i18n.Err("hub-hub.fill_in", i18n.Args{"field": f.Label})
		}
	}
	r.Values = declaredValues(p.ix, r.Values)
	r = honest(p.ix, r)

	s.fleet.mu.Lock()
	if !expected.matches(p.ix) {
		s.fleet.mu.Unlock()
		return fmt.Errorf("interaction %s is %w", p.ix.ID, errNotPending)
	}
	if s.fleet.interactions[p.ix.ID] != p || p.prepared.terminal || p.claimed {
		kept := r
		if p.resolution != nil {
			kept = *p.resolution
		}
		first := &answeredJustNow{id: p.ix.ID, status: endStatus(kept), kept: redacted(p.ix, kept)}
		s.fleet.mu.Unlock()
		return first
	}
	if p.prepared.attempt != nil {
		a := p.prepared.attempt
		s.fleet.mu.Unlock()
		<-a.done
		return s.resolvePreparedAnswerAt(ctx, p, r, remember, authorization, expected)
	}
	if detachedWaitElapsedLocked(p) {
		// The original deadline can elapse while a stored row is being read.
		// A delayed timer must not extend the owner's execution authority.
		gen := p.prepared.decisionGen
		s.fleet.mu.Unlock()
		s.expirePreparedGen(p, gen)
		return fmt.Errorf("interaction %s is %w", p.ix.ID, errNotPending)
	}
	p.claimed = true
	p.resolution = &r
	p.endedBy = cmpOr(endedByOf("", &r), db.EndedBy(ctx)) // for the history: who answered (recordEndBy)
	if p.resolved == nil {
		p.resolved = make(chan struct{})
	}
	a := p.prepared
	a.terminal = true
	clarified := a.clarified
	caller := a.caller
	if a.decisionTimer != nil {
		a.decisionTimer.Stop()
	}
	if a.pauseTimer != nil {
		a.pauseTimer.Stop()
	}
	s.fleet.mu.Unlock()

	status := endStatus(r)
	a.reportStatus = status
	text := cmpOr(a.op.Unanswered, prompts.RenderSettingUnanswered())
	var applyErr error
	switch {
	case r.UserDenied():
		a.reportStatus = "denied"
		text = cmpOr(a.op.Denied, "The user declined the change: nothing changed.")
		if note := strings.TrimSpace(r.Message); note != "" {
			text += " Owner note: " + note
		}
	case r.UserAllowed():
		a.reportStatus = "resolved"
		applyCtx := ctx
		if !clarified {
			// Before clarification the original caller still owns the apply, as
			// before this coordinator existed. Its cancellation must keep the old
			// no-caller/no-change behavior; after clarification that caller has
			// legitimately returned, so the final owner action supplies the ctx.
			applyCtx = caller
			if err := caller.Err(); err != nil {
				status = "cancelled"
				a.reportStatus = "cancelled"
				r = proto.Resolution{Dismiss: true, Reason: "caller_gone"}
				text = fmt.Sprintf("The change was approved after its caller stopped waiting: nothing changed (%v).", err)
				break
			}
		}
		if clarified {
			if err := s.approvalJournalApplying(ctx, p.ix); err != nil {
				if errors.Is(err, errApprovalExecutionUncertain) {
					// The earlier apply may already have executed. Preserve its honest
					// failure and keep applyErr non-nil so no grant or standing rule is
					// created; the ordinary ErrNotApplied cancellation must not rewrite it.
					applyErr = err
				} else {
					applyErr = fmt.Errorf("%w: the approval journal could not record execution: %v", approvalops.ErrNotApplied, err)
				}
			}
		}
		if applyErr == nil {
			// UserAllowed is checked by the coordinator, and the owner operation
			// rechecks current state immediately before its one apply.
			text, applyErr = a.op.Apply(applyCtx, r)
		}
		if errors.Is(applyErr, approvalops.ErrStale) {
			status = "cancelled"
			a.reportStatus = "stale"
			r = proto.Resolution{Dismiss: true, Reason: "stale"}
			text = fmt.Sprintf("The setting or resource changed after this card was prepared. Nothing was applied; a fresh approval card is required. (%v)", applyErr)
			applyErr = nil
		} else if errors.Is(applyErr, approvalops.ErrNotApplied) {
			status = "cancelled"
			a.reportStatus = "cancelled"
			r = proto.Resolution{Dismiss: true, Reason: "not_applied"}
			text = fmt.Sprintf("The approved change was not started: %v. Nothing was applied; correct the request or ask for a fresh approval before retrying.", applyErr)
			applyErr = nil
		} else if applyErr != nil {
			status = "failed"
			a.reportStatus = "failed"
			text = fmt.Sprintf("The owner allowed the change, but its execution outcome is uncertain: %v. Check the current state before retrying; do not assume it applied or that nothing changed.", applyErr)
		}
	case r.Allow != nil:
		// An assistant/supervisor-attributed Allow is neither an owner Allow nor
		// an owner Deny. It ends without executing the retained operation.
		a.reportStatus = "cancelled"
		text = cmpOr(a.op.Unanswered, "The approval ended without a fresh owner Allow; nothing changed.")
	}

	s.fleet.mu.Lock()
	if p.ix.Detachable && clarified && r.UserAllowed() && applyErr == nil {
		// A late detached Allow records a continuation grant, not permission to
		// replay the original hook. Only grant consumption may return this call's
		// execution Allow (approval_detached.go enforces that distinction).
		r.ApprovalID = p.ix.ID
	}
	a.outcome = approvalops.Outcome{Text: text}
	a.err = applyErr
	a.result = r
	p.resolution = &r
	p.answeredBy, p.answeredReason = r.AnsweredBy, r.Reason
	p.expired = false
	s.fleet.mu.Unlock()
	s.finishPrepared(p, status)
	if remember && applyErr == nil && r.UserAllowed() {
		s.keepAlways(p.ix, r)
	}
	return nil
}

// endPreparedByID routes a lifecycle ending through the prepared card's one
// terminal boundary. False means the card is not prepared and keeps its old path.
func (s *Service) endPreparedByID(id, status, text string) bool {
	return s.endPreparedByIDWithBy(id, status, text, "")
}

func (s *Service) endPreparedByIDWithBy(id, status, text, by string) bool {
	s.fleet.mu.Lock()
	p := s.fleet.interactions[id]
	prepared := p != nil && p.prepared != nil
	s.fleet.mu.Unlock()
	if !prepared {
		return false
	}
	s.endPreparedIfBy(p, status, text, nil, by)
	return true
}

// endPrepared is expiry/cancellation outside resolve. It loses to a final claim
// already made, and reports the terminal result only if the card had clarified.
// The result reports whether this call actually won and finished the card.
func (s *Service) endPrepared(p *pending, status, text string) bool {
	return s.endPreparedIf(p, status, text, nil)
}

// endPreparedIf arbitrates with an in-flight question and rechecks guard after
// that attempt finishes. A stale decision/pause timer therefore cannot end a
// card whose state changed while its callback was waiting.
func (s *Service) endPreparedIf(p *pending, status, text string, guard func(*preparedApproval) bool) bool {
	return s.endPreparedIfBy(p, status, text, guard, "")
}

func (s *Service) endPreparedIfBy(p *pending, status, text string, guard func(*preparedApproval) bool, by string) bool {
	for {
		s.fleet.mu.Lock()
		if p.prepared == nil || p.prepared.terminal || p.claimed || s.fleet.interactions[p.ix.ID] != p {
			s.fleet.mu.Unlock()
			return false
		}
		if attempt := p.prepared.attempt; attempt != nil {
			s.fleet.mu.Unlock()
			<-attempt.done
			continue
		}
		a := p.prepared
		if guard != nil && !guard(a) {
			s.fleet.mu.Unlock()
			return false
		}
		if text == "" {
			if status == "expired" {
				text = "The approval window ended before the owner answered; nothing was executed."
			} else {
				text = cmpOr(a.op.Unanswered, prompts.RenderSettingUnanswered())
			}
		}
		if by != "" {
			// Only the lifecycle call that wins the terminal claim may replace an
			// already-claimed answer's attribution.
			p.endedBy = by
		}
		a.terminal = true
		p.claimed = true
		p.expired = status == "expired"
		if a.decisionTimer != nil {
			a.decisionTimer.Stop()
		}
		if a.pauseTimer != nil {
			a.pauseTimer.Stop()
		}
		r := proto.Resolution{Dismiss: true}
		a.result = r
		p.resolution = &r
		a.outcome = approvalops.Outcome{Text: text}
		if p.resolved == nil {
			p.resolved = make(chan struct{})
		}
		s.fleet.mu.Unlock()
		s.finishPrepared(p, status)
		return true
	}
}

// finishPrepared publishes one terminal state and hands the original caller its
// result if it is still waiting. The durable result journal is updated before
// the card disappears, so a restart cannot silently lose the outcome.
func (s *Service) finishPrepared(p *pending, status string) error {
	s.fleet.mu.Lock()
	a := p.prepared
	if a == nil || a.finished {
		s.fleet.mu.Unlock()
		return nil
	}
	a.finished = true
	r := a.result
	clarified := a.clarified
	if status == "expired" && !clarified {
		// The original tool call is still waiting, so its result is the one place
		// to identify the expired interaction. A clarified card instead uses its
		// existing durable journal below; starting another delivery would duplicate it.
		a.outcome.Text = approvalResultTextWithSummary(p.ix.ID, status, safeInteractionSummary(p.ix), a.outcome.Text)
	}
	text := a.outcome.Text
	journalStatus := a.reportStatus
	if journalStatus == "" {
		journalStatus = status
	}
	by := p.endedBy
	s.fleet.mu.Unlock()

	if a.op.Discard != nil {
		go a.op.Discard(context.Background(), journalStatus)
	}
	var kept *proto.Resolution
	if status != "expired" {
		told := redacted(p.ix, r)
		kept = &told
	}
	if status == "expired" || by == "" {
		by = endedByOf(status, kept)
	}
	var historyErr error
	if clarified {
		historyErr = s.recordEndOccurrenceStrictBy(p.ix, status, by, kept)
		if historyErr != nil {
			s.logf("record approval occurrence outcome: %v", historyErr)
		}
	} else {
		s.recordEndBy(p.ix.ID, status, by, kept)
	}
	if clarified {
		// The delivery drain retries an unfinalized occurrence before it can hand
		// off or remove this result. A failed history write therefore cannot leave
		// a pending row behind without its recovery journal.
		s.approvalJournalFinished(context.Background(), p.ix, journalStatus, text, historyErr == nil)
	}
	if kept != nil {
		s.channelsEnded(p.ix, kept)
		s.interactionResolvedSoon(context.Background(), InteractionResolved{Interaction: p.ix, Resolution: *kept, Status: status})
	}
	if p.resolved != nil {
		close(p.resolved)
	}
	select {
	case p.done <- r:
	default:
	}
	s.finishPending(p)
	return historyErr
}
