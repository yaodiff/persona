package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// preparedApproval is the process-local continuation behind one permission
// card. Its fields are guarded by Service.fleet.mu except op and caller, which
// are immutable. The public interaction carries only CanClarify, never this
// operation.
type preparedApproval struct {
	op           approvalops.Operation
	caller       context.Context
	outcome      approvalops.Outcome
	result       proto.Resolution
	err          error
	finished     bool
	reportStatus string

	clarified  bool
	waiting    bool // its decision window ended unanswered and it was kept for the owner (keepWaiting)
	ownerSpoke bool // an owner message arrived before startPreparedTimer armed the initial wait
	release    chan struct{}
	attempt    *clarifyAttempt
	terminal   bool
	deliveries map[string]clarifyDelivery

	decisionTimer    *time.Timer
	pauseTimer       *time.Timer
	decisionGen      uint64    // bumped whenever decisionTimer is stopped or replaced
	decisionDeadline time.Time // current effective decision deadline while not paused
	remaining        time.Duration
	paused           bool
	pauseStarted     time.Time
	pausedTotal      time.Duration

	// A turn covers only questions accepted before it starts. A question
	// accepted during that turn waits for the next one, as the node queues it.
	questionSeq uint64
	acceptedSeq uint64
	turnSeq     uint64
	answeredSeq uint64
	turnActive  bool
	channelRuns map[string]bool
}

type clarifyAttempt struct {
	done        chan struct{}
	err         error
	first       bool
	clientMsgID string
	text        string
	originKey   string
	sending     bool
	turnStarted bool
	turnEnded   bool
}

type clarifyDelivery struct {
	text      string
	originKey string
}

// clarificationOrigin is the verified card callback's return destination. It
// contains no form values; only the question text travels with the owner turn.
type clarificationOrigin struct {
	Channel   string
	ChatID    string
	MessageID string
}

var (
	errApprovalClarifying = errors.New("approval is clarifying")
	errClarifyUnavailable = errors.New("this approval cannot take a question")
	// clarificationPauseBudget is cumulative per card, not per question.
	clarificationPauseBudget = 24 * time.Hour
	clarifyQuestionRunes     = 1000
	// approvalInitialWait bounds how long a prepared tool call holds its conversation's turn before returning the
	// non-final waiting result. The owner still has the much longer waiting window to answer the retained card.
	approvalInitialWait = 75 * time.Second
	// approvalWaitingWindow is how long a prepared card stays pending after the original tool call returned unanswered.
	approvalWaitingWindow = 24 * time.Hour
)

// waitPreparedCard registers an authoritative prepared approval. The original
// caller returns either its final tool result or, on the first accepted
// question, a non-final Clarifying outcome while the card stays pending.
func (s *Service) waitPreparedCard(ctx context.Context, ix proto.Interaction, op approvalops.Operation) (approvalops.Outcome, error) {
	if reservedCard(ix) || ix.Kind != proto.IxPermission || op.Apply == nil {
		return approvalops.Outcome{}, errClarifyUnavailable
	}
	if proto.InternalMaintenanceApproval(ix) {
		return s.waitOneShotPreparedCard(ctx, ix, op)
	}
	live := ix.MachineID != "" && ix.SessionID != ""
	if live {
		// Clarification needs an exact live conversation for the owner's question
		// and its answer. Unavailable origins retain the older one-shot card instead.
		// A deleted or unreadable origin stays prepared so clarify returns that exact
		// error rather than disguising it as a different fallback.
		if err := s.conversationAvailable(ctx, ix.MachineID, ix.SessionID); err == nil {
			// The latest complete session list already answers the common case;
			// ask the node only when that list does not contain the conversation.
			s.fleet.mu.Lock()
			_, listed := s.fleet.prev[ix.MachineID][ix.SessionID]
			s.fleet.mu.Unlock()
			if !listed {
				if _, ok := s.session(ctx, ix.MachineID, ix.SessionID); !ok {
					live = false
				}
			}
		}
	}
	if !live {
		return s.waitOneShotPreparedCard(ctx, ix, op)
	}
	state := &preparedApproval{op: op, caller: ctx, release: make(chan struct{}), deliveries: map[string]clarifyDelivery{}}
	ix.CanClarify = true
	_, err := s.waitInteractionPrepared(ctx, s.ownCard(ctx, ix), state)
	if errors.Is(err, errApprovalClarifying) {
		s.fleet.mu.Lock()
		waiting := state.waiting
		s.fleet.mu.Unlock()
		if waiting {
			return approvalops.Outcome{Clarifying: true, Text: approvalops.WaitingText}, nil
		}
		return approvalops.Outcome{Clarifying: true, Text: approvalops.ClarifyingText}, nil
	}
	if err != nil {
		return approvalops.Outcome{}, err
	}
	if state.err != nil {
		return approvalops.Outcome{}, state.err
	}
	return state.outcome, nil
}

// awaitPrepared releases only the original tool call on clarification. Final
// resolution, expiry and replacement still finish the interaction normally.
// Every waiter listens to the broadcast, never the one-shot p.done: a snapshot
// waiter cannot steal the original caller's terminal result. An auxiliary
// snapshot waiter's cancellation detaches only that waiter; the original
// caller owns the prepared operation's cancellation lifecycle.
func (s *Service) awaitPrepared(ctx context.Context, p *pending, auxiliary bool) (proto.Resolution, error) {
	s.fleet.mu.Lock()
	if p.resolved == nil {
		p.resolved = make(chan struct{})
	}
	resolved := p.resolved
	s.fleet.mu.Unlock()
	terminal := func() (proto.Resolution, error) {
		s.fleet.mu.Lock()
		r := p.prepared.result
		s.fleet.mu.Unlock()
		s.finishPending(p)
		return r, nil
	}
	// Prefer an already published terminal result over a clarification release
	// left closed by an earlier question (a snapshot can attach after both).
	select {
	case <-resolved:
		return terminal()
	default:
	}
	select {
	case <-resolved:
		return terminal()
	case <-p.prepared.release:
		select {
		case <-resolved:
			return terminal()
		default:
		}
		return proto.Resolution{}, errApprovalClarifying
	case <-ctx.Done():
		s.fleet.mu.Lock()
		settled := p.claimed || p.prepared.terminal
		orphaned := false
		if !settled && !auxiliary && p.ix.Resumable && s.fleet.interactions[p.ix.ID] == p {
			orphaned = s.orphanLocked(p)
		}
		s.fleet.mu.Unlock()
		if settled {
			// A final claim made concurrently with the disconnect wins, exactly as
			// in the ordinary waiter. Wait for its outcome rather than orphaning a
			// card that has already finished.
			<-resolved
			return terminal()
		}
		if auxiliary {
			// The snapshot stopped asking for this card. Its auxiliary waiter
			// leaves, but the original operation and owner card remain pending.
			return proto.Resolution{Dismiss: true}, nil
		}
		if orphaned {
			// Match the ordinary hook waiter: a transient disconnect does not
			// cancel a resumable card while its reconnect grace is running.
			return proto.Resolution{Dismiss: true}, nil
		}
		s.endPrepared(p, "cancelled", "The approval was cancelled before the owner answered; nothing changed.")
		return proto.Resolution{}, ctx.Err()
	}
}

// startPreparedTimer is called after the card is registered and announced.
func (s *Service) startPreparedTimer(p *pending) {
	s.fleet.mu.Lock()
	a := p.prepared
	if a == nil || a.terminal || p.claimed || s.fleet.interactions[p.ix.ID] != p || a.paused || a.clarified {
		s.fleet.mu.Unlock()
		return
	}
	if persistentOwnerProposal(p.ix) {
		if a.decisionTimer != nil {
			a.decisionTimer.Stop()
			a.decisionTimer = nil
		}
		a.decisionGen++
		s.fleet.mu.Unlock()
		return
	}
	if !p.ix.ExpiresAt.IsZero() {
		a.decisionDeadline = p.ix.ExpiresAt
	}
	if a.decisionDeadline.IsZero() {
		s.fleet.mu.Unlock()
		return
	}
	if a.decisionTimer != nil {
		a.decisionTimer.Stop()
	}
	a.decisionGen++
	gen := a.decisionGen
	wait := min(max(0, time.Until(a.decisionDeadline)), approvalInitialWait)
	a.decisionTimer = time.AfterFunc(wait, func() {
		s.expirePreparedGen(p, gen)
	})
	detachNow := a.ownerSpoke && p.ix.Detachable && p.orphaned == nil && a.caller.Err() == nil
	a.ownerSpoke = false
	s.fleet.mu.Unlock()
	if detachNow {
		go s.detachPreparedGen(p, gen, false)
	}
}

func (s *Service) expirePrepared(p *pending) {
	s.fleet.mu.Lock()
	var gen uint64
	if p != nil && p.prepared != nil {
		gen = p.prepared.decisionGen
	}
	s.fleet.mu.Unlock()
	s.expirePreparedGen(p, gen)
}

func (s *Service) expirePreparedGen(p *pending, gen uint64) {
	if p == nil {
		return
	}
	s.fleet.mu.Lock()
	a := p.prepared
	if a == nil || a.decisionGen != gen {
		s.fleet.mu.Unlock()
		return
	}
	orphaned := false
	if p.ix.Resumable && !a.clarified && !p.claimed &&
		s.fleet.interactions[p.ix.ID] == p && a.caller.Err() != nil {
		// The disconnect and initial-wait callbacks can race. Start the same
		// reconnect grace here when the caller has gone but awaitPrepared has not
		// yet observed it; a clarified card no longer has a caller to reconnect.
		orphaned = s.orphanLocked(p)
	}
	s.fleet.mu.Unlock()
	if orphaned {
		// The initial-wait and reconnect-grace timers share the card. While the
		// hook is detached, only the orphan timer may end it; a reconnect updates
		// prepared.caller and lets a later initial wait complete normally.
		return
	}
	if s.keepWaitingGen(p, gen) {
		return
	}
	s.endPreparedIf(p, "expired", "", func(a *preparedApproval) bool {
		return !a.paused && a.decisionGen == gen
	})
}

// keepWaitingGen is the end of an unanswered card's first decision window. The card is not taken off the apps: the
// owner may have had no notification at all (no working push, the app in the background) and only sees it when they
// open the Persona app or the web. Its original tool call returns a non-final result (approvalops.WaitingText), the
// card stays pending for approvalWaitingWindow more with its new deadline told to the apps (no second push), and its
// final outcome reaches the conversation through the approval journal, as after a question. It keeps nothing waiting
// when the caller already went away, once the card has clarified (its own timing applies), superseded, or ended. False: expire.
func (s *Service) keepWaitingGen(p *pending, gen uint64) bool {
	return s.detachPreparedGen(p, gen, true)
}

func (s *Service) detachPreparedGen(p *pending, gen uint64, expireOnError bool) bool {
	// A scheduled task's own conversation has nobody present to continue the turn
	// between rounds. Let its detachable card expire at the initial window instead
	// of retaining a needs-you session that makes every later round skip as busy.
	// Classification reads settings and the schedule book, so it runs without mu.
	if p.ix.Detachable && s.dispatchConversationClass(context.Background(), p.ix.MachineID, p.ix.SessionID) == dispatchClassScheduled {
		return false
	}
	for {
		s.fleet.mu.Lock()
		a := p.prepared
		if a == nil || a.terminal || p.claimed || s.fleet.interactions[p.ix.ID] != p || a.clarified || a.paused ||
			a.decisionGen != gen || a.caller.Err() != nil {
			s.fleet.mu.Unlock()
			return false
		}
		if attempt := a.attempt; attempt != nil {
			s.fleet.mu.Unlock()
			<-attempt.done
			continue
		}
		// Taken like a first question: no other attempt starts until this one ends.
		attempt := &clarifyAttempt{done: make(chan struct{}), first: true}
		a.attempt = attempt
		ix := p.ix
		s.fleet.mu.Unlock()

		deadline := time.Now().Add(approvalWaitingWindow).UTC()
		var waitingUntil time.Time
		if ix.Detachable && ix.Resumable {
			waitingUntil = deadline
		}
		err := s.startApprovalJournal(context.Background(), ix, waitingUntil)
		if err == nil {
			err = s.updateInteractionDeadline(context.Background(), ix.ID, ix.CreatedAt, deadline)
			if err != nil {
				// approvalJournalStarted succeeded, so balance its new entry before
				// this card expires. A failed start already removed that entry.
				s.approvalJournalAborted(context.Background(), ix)
			}
		}
		s.fleet.mu.Lock()
		ended := a.terminal || p.claimed || s.fleet.interactions[p.ix.ID] != p
		stale := a.decisionGen != gen
		if err != nil || ended || stale || a.caller.Err() != nil {
			if err != nil {
				a.attempt = nil
				close(attempt.done)
				s.fleet.mu.Unlock()
				if expireOnError {
					s.logf("approval %s: could not keep the unanswered card waiting, it expires: %v", ix.ID, err)
				} else {
					s.logf("approval %s: could not detach waiting card on owner message, keeping initial wait: %v", ix.ID, err)
				}
				return false
			}
			// Keep a.attempt held while rolling back the unused journal entry so a
			// waiting resolvePreparedAnswer cannot race approvalJournalApplying with
			// approvalJournalAborted.
			s.fleet.mu.Unlock()
			s.approvalJournalAborted(context.Background(), ix)
			s.fleet.mu.Lock()
			a.attempt = nil
			close(attempt.done)
			s.fleet.mu.Unlock()
			return ended || stale // ended/superseded meanwhile: nothing more to do; its caller gone: it expires as before
		}
		a.attempt = nil
		close(attempt.done)
		a.clarified, a.waiting = true, true
		p.ix.ExpiresAt = deadline
		a.decisionDeadline = p.ix.ExpiresAt
		ix = p.ix
		if a.decisionTimer != nil {
			a.decisionTimer.Stop()
		}
		a.decisionGen++
		nextGen := a.decisionGen
		a.decisionTimer = time.AfterFunc(approvalWaitingWindow, func() { s.expirePreparedGen(p, nextGen) })
		s.fleet.mu.Unlock()
		s.show(ix) // the apps keep the card, with its new deadline
		s.armPendingReminder(ix)
		close(a.release)
		return true
	}
}

// Both new questions and decisions claim this window under fleet.mu. A late
// timer cannot extend a released command's deadline by accepting either one.
func detachedWaitElapsedLocked(p *pending) bool {
	a := p.prepared
	return p.ix.Detachable && a.waiting && !a.paused && !a.decisionDeadline.After(time.Now())
}

type clarifyingCardKey struct{}

func withClarifyingCard(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, clarifyingCardKey{}, id)
}

func clarifyingCardOf(ctx context.Context) string {
	id, _ := ctx.Value(clarifyingCardKey{}).(string)
	return id
}

// detachPendingApprovalsOnOwnerMessage moves any unanswered Detachable approval
// card for (machine, session) out of its initial synchronous wait into the
// retained approvalWaitingWindow state so the blocked tool call releases the
// turn promptly for the owner's new message. The card stays pending in the UI
// and is never cancelled; non-detachable cards and scheduled conversations are
// untouched. Runs asynchronously so owner sends never block or fail on storage.
func (s *Service) detachPendingApprovalsOnOwnerMessage(machine, session string) {
	if machine == "" || session == "" {
		return
	}
	type candidate struct {
		p         *pending
		machineID string
		gen       uint64
	}
	s.fleet.mu.Lock()
	var candidates []candidate
	for _, p := range s.fleet.interactions {
		a := p.prepared
		if a == nil || !p.ix.Detachable || p.ix.SessionID != session {
			continue
		}
		if a.terminal || p.claimed || a.clarified || a.paused || p.orphaned != nil || a.caller.Err() != nil {
			continue
		}
		candidates = append(candidates, candidate{p: p, machineID: p.ix.MachineID, gen: a.decisionGen})
	}
	s.fleet.mu.Unlock()
	if len(candidates) == 0 {
		return
	}
	var ready []candidate
	for _, c := range candidates {
		if c.machineID != machine && !s.sameMachine(c.machineID, machine) {
			continue
		}
		if c.gen == 0 {
			// Registration (record/announce) has not reached startPreparedTimer yet.
			// Re-check under fleet.mu so startPreparedTimer detaches once recorded.
			s.fleet.mu.Lock()
			a := c.p.prepared
			if a != nil && s.fleet.interactions[c.p.ix.ID] == c.p &&
				!a.terminal && !c.p.claimed && !a.clarified && !a.paused &&
				c.p.orphaned == nil && a.caller.Err() == nil {
				if a.decisionGen == 0 {
					a.ownerSpoke = true
				} else {
					c.gen = a.decisionGen
					ready = append(ready, c)
				}
			}
			s.fleet.mu.Unlock()
			continue
		}
		ready = append(ready, c)
	}
	if len(ready) == 0 {
		return
	}
	for _, c := range ready {
		go s.detachPreparedGen(c.p, c.gen, false)
	}
}

// approvalNudgeDeadline returns the live effective deadline for a card the Lark
// sweep is about to nudge. A question pauses that deadline; completed pauses have
// already moved the published deadline by their cumulative duration. Reading the
// prepared interaction here also follows an implementation that extends its window.
func (s *Service) approvalNudgeDeadline(ix proto.Interaction) (time.Time, bool) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	p := s.fleet.interactions[ix.ID]
	if p == nil {
		// The hub lookup immediately before this check is authoritative for an
		// ordinary interaction that has no process-local prepared approval.
		return ix.ExpiresAt, true
	}
	if p.claimed || p.resolution != nil {
		return time.Time{}, false
	}
	a := p.prepared
	if a == nil {
		return p.ix.ExpiresAt, true
	}
	if a.terminal || a.paused {
		return time.Time{}, false
	}
	return p.ix.ExpiresAt, true
}

// pausePreparedLocked preserves the remaining decision time and publishes the
// actual cumulative-pause deadline. Repeated questions while paused change neither.
func (s *Service) pausePreparedLocked(p *pending) bool {
	a := p.prepared
	if a.paused || a.terminal {
		return false
	}
	now := time.Now()
	if a.decisionDeadline.IsZero() && !p.ix.ExpiresAt.IsZero() {
		a.decisionDeadline = p.ix.ExpiresAt
	}
	a.remaining = max(0, a.decisionDeadline.Sub(now))
	if a.decisionTimer != nil {
		a.decisionTimer.Stop()
		a.decisionTimer = nil
	}
	a.decisionGen++
	a.paused = true
	a.pauseStarted = now
	p.ix.ExpiresAt = now.Add(clarificationPauseBudget - a.pausedTotal).UTC()
	s.armPauseBudgetLocked(p)
	return true
}

func (s *Service) armPauseBudgetLocked(p *pending) {
	a := p.prepared
	left := clarificationPauseBudget - a.pausedTotal
	if a.paused {
		left -= time.Since(a.pauseStarted)
	}
	if a.pauseTimer != nil {
		a.pauseTimer.Stop()
	}
	a.pauseTimer = time.AfterFunc(max(0, left), func() {
		s.endPreparedIf(p, "expired", "", func(a *preparedApproval) bool {
			return a.paused && a.pausedTotal+time.Since(a.pauseStarted) >= clarificationPauseBudget
		})
	})
}

// resumePreparedLocked is called when the turn covering every accepted
// question has committed its answer (and every required channel run has
// finished). A stale timer callback cannot resume a new pause. The returned card
// tells clients the resumed decision deadline without another arrival or push.
func (s *Service) resumePreparedLocked(p *pending) (proto.Interaction, bool) {
	a := p.prepared
	if !a.paused || a.terminal || p.claimed || s.fleet.interactions[p.ix.ID] != p || a.attempt != nil || a.questionSeq != a.acceptedSeq || a.acceptedSeq != a.answeredSeq || len(a.channelRuns) != 0 {
		return proto.Interaction{}, false
	}
	now := time.Now()
	a.pausedTotal += now.Sub(a.pauseStarted)
	a.paused = false
	if a.pauseTimer != nil {
		a.pauseTimer.Stop()
		a.pauseTimer = nil
	}
	if a.decisionTimer != nil {
		a.decisionTimer.Stop()
	}
	a.decisionGen++
	gen := a.decisionGen
	a.decisionDeadline = now.Add(a.remaining).UTC()
	p.ix.ExpiresAt = a.decisionDeadline
	a.decisionTimer = time.AfterFunc(a.remaining, func() { s.expirePreparedGen(p, gen) })
	return p.ix, true
}

// clarificationTurnStarted associates the new turn with questions accepted
// before it started. The original approval turn does not count.
func (s *Service) clarificationTurnStarted(machine, session string) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	for _, p := range s.fleet.interactions {
		a := p.prepared
		if a == nil || a.terminal || p.ix.MachineID != machine || p.ix.SessionID != session {
			continue
		}
		if a.attempt != nil && a.attempt.sending && a.answeredSeq == a.acceptedSeq {
			a.attempt.turnStarted = true
			a.attempt.turnEnded = false
		}
		if !a.clarified || a.acceptedSeq == a.answeredSeq {
			continue
		}
		a.turnActive = true
		a.turnSeq = a.acceptedSeq
	}
}

func (s *Service) clarificationTurnEndedSync(machine, session string) []proto.Interaction {
	s.fleet.mu.Lock()
	var tells []proto.Interaction
	for _, p := range s.fleet.interactions {
		a := p.prepared
		if a == nil || a.terminal || p.ix.MachineID != machine || p.ix.SessionID != session {
			continue
		}
		if a.attempt != nil && a.attempt.sending && a.attempt.turnStarted {
			a.attempt.turnEnded = true
		}
		if !a.clarified || !a.turnActive {
			continue
		}
		a.turnActive = false
		a.answeredSeq = max(a.answeredSeq, a.turnSeq)
		if ix, ok := s.resumePreparedLocked(p); ok {
			tells = append(tells, ix)
		}
	}
	s.fleet.mu.Unlock()
	return tells
}

func (s *Service) clarificationTurnEnded(machine, session string) {
	for _, ix := range s.clarificationTurnEndedSync(machine, session) {
		s.show(ix)
	}
}

func clarificationOriginKey(origin *clarificationOrigin) string {
	if origin == nil {
		return ""
	}
	return origin.Channel + "\x00" + origin.ChatID + "\x00" + origin.MessageID
}

func validateClarifyQuestion(text string) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return errors.New("question must not be blank")
	}
	if utf8.RuneCountInString(trimmed) > clarifyQuestionRunes {
		return fmt.Errorf("question is too long (at most %d characters)", clarifyQuestionRunes)
	}
	return nil
}

// clarify delivers verbatim owner text to the exact originating conversation.
// Validation has no side effects. A final answer racing delivery waits for the
// attempt, so exactly one of clarification and that answer claims the next
// state. Different questions submitted concurrently are processed in order;
// only a replay of the same delivery is deduplicated.
func (s *Service) clarify(ctx context.Context, id, text, clientMsgID string, origin *clarificationOrigin) error {
	return s.clarifyAt(ctx, id, text, clientMsgID, origin, nil)
}

func (s *Service) clarifyAt(ctx context.Context, id, text, clientMsgID string, origin *clarificationOrigin, expected *time.Time) error {
	if err := validateClarifyQuestion(text); err != nil {
		return err
	}
	if clientMsgID == "" {
		clientMsgID = "approval-question-" + id + "-" + randomHex(6)
	}
	originKey := clarificationOriginKey(origin)

	s.fleet.mu.Lock()
	p := s.fleet.interactions[id]
	if p != nil && expected != nil && !p.ix.CreatedAt.Equal(*expected) {
		s.fleet.mu.Unlock()
		return errClarifyUnavailable
	}
	if p == nil || p.prepared == nil || !p.ix.CanClarify {
		s.fleet.mu.Unlock()
		return errClarifyUnavailable
	}
	a := p.prepared
	if old, ok := a.deliveries[clientMsgID]; ok {
		s.fleet.mu.Unlock()
		if old.text != text || old.originKey != originKey {
			return errors.New("client_msg_id was already used for a different question")
		}
		return nil
	}
	if a.terminal || p.claimed {
		s.fleet.mu.Unlock()
		return errClarifyUnavailable
	}
	if running := a.attempt; running != nil {
		s.fleet.mu.Unlock()
		<-running.done
		if running.clientMsgID == clientMsgID {
			if running.text != text || running.originKey != originKey {
				return errors.New("client_msg_id was already used for a different question")
			}
			return running.err
		}
		return s.clarifyAt(ctx, id, text, clientMsgID, origin, expected)
	}
	if detachedWaitElapsedLocked(p) {
		gen := a.decisionGen
		s.fleet.mu.Unlock()
		s.expirePreparedGen(p, gen)
		return errClarifyUnavailable
	}
	attempt := &clarifyAttempt{done: make(chan struct{}), first: !a.clarified,
		clientMsgID: clientMsgID, text: text, originKey: originKey}
	var waitingUntil time.Time
	if p.ix.Detachable && a.waiting && a.questionSeq == 0 {
		waitingUntil = a.decisionDeadline
	}
	a.attempt = attempt
	s.fleet.mu.Unlock()

	if attempt.first {
		if err := s.approvalJournalStarted(ctx, p.ix); err != nil {
			return s.finishClarifyAttempt(p, attempt, nil, err)
		}
	}

	// Validate the exact destination before changing expiry or releasing the
	// tool. SendParams rejects a missing/ended conversation; no main-chat fallback.
	questionParams := map[string]any{"session_id": p.ix.SessionID, "text": text, "client_msg_id": clientMsgID}
	if token := assistantwire.ClarificationProofOf(ctx); token != "" {
		questionParams[assistantwire.ClarificationProofParam] = token
		questionParams[assistantwire.ClarificationInteractionParam] = id
	}
	params, err := s.SendParams(withClarifyingCard(ctx, id), SendParams{MachineID: p.ix.MachineID, SessionID: p.ix.SessionID, Params: questionParams, VerbatimText: true})
	if err != nil {
		return s.finishClarifyAttempt(p, attempt, nil, err)
	}
	var run *channel.Run
	if origin != nil && origin.Channel == channel.Feishu {
		// Base is captured before the owner's message, as for every channel run:
		// the watcher must read this question's answer, not an older turn.
		run = &channel.Run{ID: "approval-" + id + "-" + randomHex(4), Channel: channel.Feishu,
			MachineID: p.ix.MachineID, SessionID: p.ix.SessionID, TargetKind: channel.TargetConversation,
			ReplyTo: origin.MessageID, Started: time.Now().UTC(), Base: s.lastSeq(ctx, p.ix.MachineID, p.ix.SessionID),
			MsgIDs: []string{clientMsgID}, Ext: map[string]string{"clarify": id, "chat": origin.ChatID}}
	}
	// A clarification needs process-local turn and pause state. Clear the wait
	// marker before sending it, and restore it after a definite rejection.
	if !waitingUntil.IsZero() {
		if err := s.approvalJournalStarted(ctx, p.ix); err != nil {
			return s.finishClarifyAttempt(p, attempt, nil, err)
		}
	}
	s.UserSpoke(ctx, SessionRef{MachineID: p.ix.MachineID, SessionID: p.ix.SessionID})
	if p.ix.Private {
		// The owner's question starts a new turn after the original Just me turn
		// may have ended. Mark that answer turn before dispatch, not at its end.
		s.justMeSent(p.ix.MachineID, p.ix.SessionID)
	}
	s.fleet.mu.Lock()
	attempt.sending = true
	s.fleet.mu.Unlock()
	err = s.sendOnce(ctx, p.ix.MachineID, params)
	if err != nil && !relayInDoubt(err) {
		if !waitingUntil.IsZero() {
			err = errors.Join(err, s.startApprovalJournal(context.Background(), p.ix, waitingUntil))
		}
		return s.finishClarifyAttempt(p, attempt, nil, err)
	}
	if err != nil {
		// The request may already have been accepted. Rolling back into a blocked
		// tool call could strand that owner message behind it, so the card moves
		// fail-closed into Clarifying and the stable delivery ID deduplicates a
		// retry instead.
		s.logf("approval %s: question delivery outcome uncertain; retaining the pending card: %v", id, err)
		err = nil
	}
	return s.finishClarifyAttempt(p, attempt, run, nil)
}

func (s *Service) finishClarifyAttempt(p *pending, attempt *clarifyAttempt, run *channel.Run, err error) error {
	s.fleet.mu.Lock()
	a := p.prepared
	attempt.err = err
	release := false
	var tell proto.Interaction
	tellDeadline := false
	if err == nil && !a.terminal && !p.claimed && s.fleet.interactions[p.ix.ID] == p {
		prevAccepted := a.acceptedSeq
		release = attempt.first && !a.clarified
		a.clarified = true
		a.questionSeq++
		a.acceptedSeq = a.questionSeq
		prev := s.fleet.prev[p.ix.MachineID][p.ix.SessionID]
		working := prev.Status == proto.StatusWorking || prev.WorkingBeforeUnknown
		if attempt.turnEnded {
			a.turnActive = false
			a.answeredSeq = max(a.answeredSeq, a.acceptedSeq)
		} else if attempt.turnStarted {
			a.turnActive = true
			a.turnSeq = a.acceptedSeq
		} else if working && !a.turnActive && a.answeredSeq == prevAccepted {
			// Headless assistant sessions remain StatusWorking through waitPreparedCard/waitDetachedCard
			// and either steer the owner's question into the running turn (Codex) or coalesce the
			// tool-turn end and answer-turn start inside notifySessionsSoon (Claude), emitting only
			// StatusWorking -> StatusDone with no intermediate stopped edge.
			a.turnActive = true
			a.turnSeq = a.acceptedSeq
		}
		a.deliveries[attempt.clientMsgID] = clarifyDelivery{text: attempt.text, originKey: attempt.originKey}
		if run != nil {
			if a.channelRuns == nil {
				a.channelRuns = map[string]bool{}
			}
			a.channelRuns[run.ID] = true
		}
		tellDeadline = s.pausePreparedLocked(p)
		tell = p.ix
	} else if err == nil {
		err = errClarifyUnavailable
		attempt.err = err
	}
	finalErr := attempt.err
	a.attempt = nil
	if !a.terminal && !p.claimed && s.fleet.interactions[p.ix.ID] == p {
		if ix, ok := s.resumePreparedLocked(p); ok {
			tell, tellDeadline = ix, true
		}
	}
	close(attempt.done)
	s.fleet.mu.Unlock()

	if tellDeadline {
		s.show(tell)
	}
	if finalErr != nil && attempt.first {
		s.approvalJournalAborted(context.Background(), p.ix)
	}
	if run != nil && finalErr == nil {
		s.startChannelRun(run)
	}
	if release {
		close(a.release)
	}
	if finalErr == nil {
		s.detachPendingApprovalsOnOwnerMessage(p.ix.MachineID, p.ix.SessionID)
	}
	return finalErr
}

// clarificationChannelEnded resumes expiry after this Lark answer has either
// been delivered or its failure has been reported. Other overlapping answers
// keep their own entries and continue to hold the pause.
func (s *Service) clarificationChannelEnded(id, runID string) {
	s.fleet.mu.Lock()
	p := s.fleet.interactions[id]
	if p == nil || p.prepared == nil {
		s.fleet.mu.Unlock()
		return
	}
	delete(p.prepared.channelRuns, runID)
	ix, tell := s.resumePreparedLocked(p)
	s.fleet.mu.Unlock()
	if tell {
		s.show(ix)
	}
}

type approvalEndHandledKey struct{}

// approvalAnswerTurnStarted/Ended are small indirections used by Service's turn
// events and tests.
func (s *Service) approvalAnswerTurnStarted(r SessionRef) {
	s.clarificationTurnStarted(r.MachineID, r.SessionID)
}
func (s *Service) approvalAnswerTurnEndedSync(r SessionRef) []proto.Interaction {
	return s.clarificationTurnEndedSync(r.MachineID, r.SessionID)
}
func (s *Service) approvalAnswerTurnEnded(r SessionRef) {
	s.clarificationTurnEnded(r.MachineID, r.SessionID)
}

// approvalBook is defined here; its durable journal implementation is in
// approval_journal.go.
type approvalBook struct {
	mu            sync.Mutex
	loaded        bool
	list          []approvalJournalEntry
	handled       map[string]bool
	delivering    bool
	again         bool
	retry         bool
	retryDelay    time.Duration // 0: approvalResultRetryDelay; tests shorten it
	recovering    bool
	recoveryDone  bool
	recoveryRetry bool
	recoverySeq   uint64
}
