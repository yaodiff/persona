package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	ownerActionPrepareTool = "owner_action_prepare"
	ownerActionBatchTool   = "owner_action_batch"
	// A proposal must fit in one Feishu card without safeCard replacing its
	// contents. Larger operations retain the ordinary approval flow.
	ownerActionMaxDisplay = 12000
)

func (s *Service) ownerActions() (*ownerActionBook, error) {
	book, ok := newOwnerActionBook(s.store())
	if !ok {
		return nil, errors.New("exact owner-action approval needs a persistent revision-aware database")
	}
	return book, nil
}

func ownerActionInteractionID(batchID, itemID string) string {
	if itemID != "" {
		return "ix_owner_item_" + strings.TrimPrefix(itemID, "oai_")
	}
	return "ix_owner_batch_" + strings.TrimPrefix(batchID, "oab_")
}

func ownerActionProposal(ix proto.Interaction) bool {
	if !ix.OwnerProposal || ix.Kind != proto.IxPermission || ix.ApprovalBatchID == "" {
		return false
	}
	if ix.ApprovalItemID != "" {
		return ix.ID == ownerActionInteractionID(ix.ApprovalBatchID, ix.ApprovalItemID) && ix.ToolName != ownerActionBatchTool
	}
	return ix.ID == ownerActionInteractionID(ix.ApprovalBatchID, "") && ix.ToolName == ownerActionBatchTool
}

func persistentOwnerProposal(ix proto.Interaction) bool {
	return ownerActionProposal(ix) && ix.Resumable && ix.NoExpiry
}

func ownerActionDisplay(item ownerActionItem) (string, error) {
	if len(item.Canonical) > ownerActionMaxDisplay {
		return "", errors.New("owner-action item is too large to display in full; use its ordinary approval card")
	}
	// RedactArgs is the existing channel privacy boundary. Secret values still
	// participate in the fingerprint, so changing one requires fresh approval,
	// but they are not copied into a chat card.
	redacted := proto.RedactArgs(json.RawMessage(item.Canonical))
	if redacted == "" {
		return "", errors.New("owner-action input cannot be displayed")
	}
	var out bytes.Buffer
	if err := json.Indent(&out, []byte(redacted), "", "  "); err != nil {
		return "", err
	}
	if out.Len() > ownerActionMaxDisplay {
		return "", errors.New("owner-action item is too large to display in full; use its ordinary approval card")
	}
	return out.String(), nil
}

// registerOwnerAction waits only for registration, not for the proposal's
// decision. Both a joined pending card and an early failure release the caller;
// cancelling the tool context also releases it without abandoning the durable
// proposal's background timeout cleanup.
func (s *Service) registerOwnerAction(ctx context.Context, ix proto.Interaction) error {
	ready := make(chan error, 1)
	go func() { _, _ = s.waitInteractionReadyResult(context.Background(), ix, nil, ready) }()
	select {
	case err := <-ready:
		return err
	case <-ctx.Done():
		// Announcement precedes the readiness notification. If cancellation
		// races that notification, an already pending matching occurrence is
		// registered and the persistent proposal must report its pending result.
		if current, pending := s.pendingIx(ix.ID); pending && sameApprovalOccurrence(ix, current) {
			return nil
		}
		select {
		case err := <-ready:
			return err
		default:
		}
		return ctx.Err()
	}
}

func ownerActionItemDisplay(item ownerActionItem) (string, error) {
	arguments, err := ownerActionDisplay(item)
	if err != nil {
		return "", err
	}
	if item.Display == "" {
		return arguments, nil
	}
	text := item.Display + "\n\nCanonical arguments:\n" + arguments
	if len(text) > ownerActionMaxDisplay {
		return "", errors.New("built-in before/after and canonical arguments are too large to display in full")
	}
	return text, nil
}

func (s *Service) toolOwnerActionPrepare(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	cfg := s.settings(ctx)
	persistent := persistentOwnerActionCardsOn(cfg)
	if !ownerReplyApprovalOn(cfg) {
		return "", errors.New("exact owner-reply approval is disabled in Settings")
	}
	var in struct {
		Items []struct {
			Tool  string          `json:"tool"`
			Input json.RawMessage `json:"input"`
		} `json:"items"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return "", err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return "", errors.New("owner_action_prepare accepts exactly one JSON object")
	}
	drafts := make([]ownerActionDraft, 0, len(in.Items))
	ctx = withOwnerProfilePlan(ctx)
	for _, item := range in.Items {
		canonical, err := canonicalOwnerAction(item.Input)
		if err != nil {
			return "", err
		}
		if len(canonical) > ownerActionMaxDisplay {
			return "", errors.New("an item is too large to display in full; call it directly for its ordinary approval card")
		}
		draft := ownerActionDraft{Tool: item.Tool, Input: canonical}
		if ownerBuiltinWrite(item.Tool) {
			prepared, err := s.prepareOwnerBuiltin(ctx, machine, self, item.Tool, canonical)
			if err != nil {
				return "", err
			}
			draft.Display = prepared.display
			if prepared.revision != nil {
				expected := *prepared.revision
				draft.Revision = &expected
			}
			if err := advanceOwnerProfilePlan(ctx, prepared); err != nil {
				return "", err
			}
		} else {
			if s.ext == nil {
				return "", errors.New("connector tools are unavailable")
			}
			known, readOnly := s.ext.ToolAccess(item.Tool)
			if !known || readOnly || !s.ext.ConnectorTool(item.Tool) {
				return "", fmt.Errorf("%s is not a known side-effecting connector or supported built-in write", item.Tool)
			}
		}
		drafts = append(drafts, draft)
	}
	book, err := s.ownerActions()
	if err != nil {
		return "", err
	}
	batch, err := book.createPersistent(ctx, machine, self, drafts, persistent)
	if err != nil {
		return "", err
	}
	for _, item := range batch.Items {
		if _, err := ownerActionItemDisplay(item); err != nil {
			_ = book.cancelPending(context.Background(), batch.ID)
			return "", err
		}
	}
	phrases, err := ownerApprovalPhrases(cfg)
	if err != nil {
		_ = book.cancelPending(context.Background(), batch.ID)
		return "", err
	}
	batchSummary := i18n.T(s.lang(ctx), "assistant-owner_actions.batch", i18n.Args{"count": len(batch.Items), "phrases": strings.Join(phrases, " / "), "id": batch.ID})
	var builtinDetails strings.Builder
	for i, item := range batch.Items {
		if item.Display != "" {
			fmt.Fprintf(&builtinDetails, "\n\nItem %d/%d · %s\n%s", i+1, len(batch.Items), item.Tool, item.Display)
		}
	}
	batchSummary += builtinDetails.String()
	if len(batchSummary) > ownerActionMaxDisplay {
		_ = book.cancelPending(context.Background(), batch.ID)
		return "", errors.New("the batch's complete built-in before/after display is too large; split it into smaller batches")
	}
	for _, item := range batch.Items {
		display, _ := ownerActionItemDisplay(item)
		ix := proto.Interaction{
			ID:                   ownerActionInteractionID(batch.ID, item.ID),
			MachineID:            machine,
			SessionID:            self,
			Kind:                 proto.IxPermission,
			CreatedAt:            batch.CreatedAt,
			ToolName:             item.Tool,
			ToolInput:            json.RawMessage(item.Canonical),
			Summary:              display,
			ApprovalBatchID:      batch.ID,
			ApprovalItemID:       item.ID,
			OwnerProposal:        true,
			OwnerApprovalPhrases: phrases,
			Resumable:            true,
			NoExpiry:             persistent,
		}
		notKept := false
		ix.AlwaysHonored = &notKept
		// Item proposals have no process-local Apply closure. Their waiter only
		// owns timeout cleanup; approval and execution live in the durable book.
		if err := s.registerOwnerAction(ctx, ix); err != nil {
			if !persistent {
				_ = book.cancelPending(context.Background(), batch.ID)
			}
			return "", err
		}
	}
	batchIX := proto.Interaction{
		ID:                   ownerActionInteractionID(batch.ID, ""),
		MachineID:            machine,
		SessionID:            self,
		Kind:                 proto.IxPermission,
		CreatedAt:            batch.CreatedAt,
		ToolName:             ownerActionBatchTool,
		Summary:              batchSummary,
		ApprovalBatchID:      batch.ID,
		OwnerProposal:        true,
		OwnerApprovalPhrases: phrases,
		Resumable:            true,
		NoExpiry:             persistent,
	}
	notKept := false
	batchIX.AlwaysHonored = &notKept
	decided := false
	defer func() {
		if !decided && !persistent {
			_ = book.cancelPending(context.Background(), batch.ID)
			_ = s.syncOwnerActionBatch(context.Background(), batch.ID)
		}
	}()
	syncCtx := context.WithoutCancel(ctx)
	resolution, err := s.waitOwnerActionBatch(ctx, batchIX)
	if err == nil && !resolution.Dismiss {
		if err := s.syncOwnerActionBatch(syncCtx, batch.ID); err != nil && !errors.Is(err, errNotPending) {
			return "", err
		}
	}
	if err != nil {
		return "", err
	}
	if resolution.Dismiss {
		if persistent {
			return "The exact approval batch is still pending in the original conversation and channels. Do not execute any item until its approval arrives.", nil
		}
		return "The exact approval batch closed unanswered. Nothing was approved or executed.", nil
	}
	if resolution.Allow == nil {
		return "", errors.New("exact owner-action proposals accept only Allow or Deny")
	}
	decided = true
	current, found, err := book.batch(ctx, batch.ID)
	if err != nil || !found {
		return "", errors.New("the approved batch could not be read back")
	}
	if !*resolution.Allow {
		return "The owner denied this exact batch. Do not execute any of its items.", nil
	}
	var out strings.Builder
	out.WriteString("The owner approved the following exact operations. Call each listed tool exactly once in item order with the canonical arguments below, without edits, and stop at the first error. Built-in calls consume their one-shot grants and report their item position and partial result. Any other call needs its own approval. Do not retry an item after an uncertain external result.\n")
	approved := 0
	for index, item := range current.Items {
		if item.State != ownerActionGranted && item.State != ownerActionConsumed {
			continue
		}
		approved++
		fmt.Fprintf(&out, "\nItem %d/%d · %s\nTool: %s\nArguments: %s\n", index+1, len(current.Items), item.ID, item.Tool, item.Canonical)
	}
	if approved == 0 {
		return "The batch has no remaining approved items. Execute nothing.", nil
	}
	return out.String(), nil
}

// Both waiters observe the same published card. A quick reply can retire it
// before the original tool caller resumes; publishing its ID again would lose that reply.
func (s *Service) waitOwnerActionBatch(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
	type result struct {
		resolution proto.Resolution
		err        error
	}
	registered := make(chan *pending, 1)
	finished := make(chan result, 1)
	go func() {
		r, err := s.waitRegisteredInteraction(context.Background(), ix, nil, func(p *pending) { registered <- p })
		finished <- result{r, err}
	}()
	select {
	case outcome := <-finished:
		return outcome.resolution, outcome.err
	case p := <-registered:
		// An item decision may already be durable when the batch card is first published.
		err := s.syncOwnerActionBatch(context.WithoutCancel(ctx), ix.ApprovalBatchID)
		if err != nil && !errors.Is(err, errNotPending) {
			return proto.Resolution{}, err
		}
		return s.await(ctx, p)
	}
}

func ownerActionCardProof(ix proto.Interaction, source string) ownerReplyProof {
	return ownerReplyProof{
		AuthenticatedOwner: true,
		DirectParent:       true, // a press is bound to this exact card, stronger than a text parent
		ExactPhrase:        true, // Allow/Deny is an explicit typed answer
		EventID:            "card:" + source + ":" + ix.ID,
		BatchID:            ix.ApprovalBatchID,
		ItemID:             ix.ApprovalItemID,
	}
}

func (s *Service) resolveOwnerProposal(ctx context.Context, ix proto.Interaction, r proto.Resolution, proof ownerReplyProof) error {
	return s.resolveOwnerProposalAt(ctx, ix, r, proof, nil)
}

// claimOwnerProposal arbitrates the live card against timeout before the
// durable decision can commit. The persisting marker makes a timeout wait for
// the decision rather than recording a competing expiry.
func (s *Service) claimOwnerProposal(ix proto.Interaction, r proto.Resolution, expected *interactionExpectation) (*pending, error) {
	return s.claimOwnerProposalBy(ix, r, expected, "")
}

func (s *Service) claimOwnerProposalBy(ix proto.Interaction, r proto.Resolution, expected *interactionExpectation, by string) (*pending, error) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	p := s.fleet.interactions[ix.ID]
	occurrence := &interactionExpectation{occurrence: &ix}
	if p == nil || !ownerActionProposal(p.ix) || !occurrence.matches(p.ix) || !expected.matches(p.ix) || p.claimed || p.persisting != nil {
		return nil, errNotPending
	}
	p.claimed = true
	p.resolution = &r
	p.endedBy = by
	p.persisting = &questionPersistence{done: make(chan struct{})}
	p.persistenceErr = nil
	if p.resolved == nil {
		p.resolved = make(chan struct{})
	}
	p.answeredBy, p.answeredReason = r.AnsweredBy, r.Reason
	if r.AnsweredBy != "" {
		p.answeredWith = r.Answers
	}
	signalPersistenceWakeLocked(p)
	return p, nil
}

// releaseOwnerProposalClaim wakes timeout and duplicate answers after the
// durable decision. A rejected decision restores the unclaimed pending state;
// awaitOnce turns retry into a fresh arbitration instead of a false answer.
func (s *Service) releaseOwnerProposalClaim(p *pending, decisionErr error, retry bool) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	attempt := p.persisting
	if attempt == nil {
		return
	}
	if retry {
		p.claimed, p.resolution = false, nil
		p.answeredBy, p.answeredReason, p.answeredWith = "", "", nil
		p.endedBy = ""
		attempt.retry = true
	}
	attempt.err = decisionErr
	p.persisting = nil
	close(attempt.done)
	signalPersistenceWakeLocked(p)
}

// failOwnerProposalClaim wakes local waiters on service shutdown without
// exposing the provisional answer or allowing expiry to rewrite an uncertain
// durable outcome. The claimed card remains for restart reconciliation.
func (s *Service) failOwnerProposalClaim(p *pending, claimErr error) {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	attempt := p.persisting
	if attempt == nil {
		return
	}
	p.resolution = nil
	p.answeredBy, p.answeredReason, p.answeredWith = "", "", nil
	p.persistenceErr = claimErr
	attempt.err = claimErr
	p.persisting = nil
	close(attempt.done)
	signalPersistenceWakeLocked(p)
}

func (s *Service) completeOwnerProposalClaim(p *pending, r proto.Resolution) {
	s.fleet.mu.Lock()
	p.resolution = &r
	p.answeredBy, p.answeredReason = r.AnsweredBy, r.Reason
	p.answeredWith = r.Answers
	by := p.endedBy
	s.fleet.mu.Unlock()
	status := endStatus(r)
	told := redacted(p.ix, r)
	if err := s.recordEndOccurrenceStrictBy(p.ix, status, by, &told); err != nil {
		s.logf("record owner-action proposal outcome: %v", err)
	}
	s.channelsEnded(p.ix, &told)
	go s.InteractionResolved(context.Background(), InteractionResolved{Interaction: p.ix, Resolution: told, Status: status})
	s.releaseOwnerProposalClaim(p, nil, false)
	close(p.resolved)
	select {
	case p.done <- r:
	default:
	}
	s.finishPending(p)
}

func ownerProposalDefinitiveError(err error) bool {
	return errors.Is(err, errOwnerActionProof) || errors.Is(err, errOwnerActionConflict)
}

// decideOwnerProposal reads the effective target state after every decision,
// including success. Another manager's first decision may have won, and a
// storage error does not prove that its write was not applied.
func (s *Service) decideOwnerProposal(ctx context.Context, book *ownerActionBook, ix proto.Interaction, proof ownerReplyProof, allow bool) (proto.Resolution, bool, error) {
	_, decisionErr := book.decide(ctx, proof, allow)
	effective, settled, outcomeErr := book.proposalOutcome(ctx, ix.ApprovalBatchID, ix.ApprovalItemID)
	if outcomeErr == nil && settled {
		r := proto.Resolution{Allow: &effective, Reason: "exact owner-action decision " + ix.ApprovalBatchID}
		return honest(ix, r), true, nil
	}
	if decisionErr != nil {
		return proto.Resolution{}, false, decisionErr
	}
	if outcomeErr != nil {
		return proto.Resolution{}, false, outcomeErr
	}
	return proto.Resolution{}, false, errors.New("owner-action decision left its target pending")
}

// reconcileOwnerProposalClaim keeps an uncertain storage outcome fail-closed.
// Only a definitive rejection with a still-pending durable target releases the
// claim for normal expiry/cancellation arbitration.
func (s *Service) reconcileOwnerProposalClaim(ix proto.Interaction, p *pending, proof ownerReplyProof, allow bool) {
	unlockAdmission := s.lockInteractionAdmission(ix.ID)
	defer unlockAdmission()
	runCtx := s.runCtx()
	defer func() {
		if runCtx.Err() != nil {
			s.failOwnerProposalClaim(p, runCtx.Err())
		}
	}()
	for {
		s.fleet.mu.Lock()
		current := s.fleet.interactions[ix.ID] == p && p.claimed
		s.fleet.mu.Unlock()
		if !current {
			return
		}
		book, reconcileErr := s.ownerActions()
		var r proto.Resolution
		settled := false
		if reconcileErr == nil {
			ctx, cancel := context.WithTimeout(runCtx, historyTimeout)
			r, settled, reconcileErr = s.decideOwnerProposal(ctx, book, ix, proof, allow)
			cancel()
		}
		if settled {
			s.completeOwnerProposalClaim(p, r)
			if err := s.syncOwnerActionBatch(runCtx, ix.ApprovalBatchID); err != nil && runCtx.Err() == nil {
				s.logf("project reconciled owner-action decision: %v", err)
			}
			return
		}
		if ownerProposalDefinitiveError(reconcileErr) {
			s.releaseOwnerProposalClaim(p, reconcileErr, true)
			return
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-runCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
	}
}

// resolveOwnerProposalAt binds the durable decision to the exact live
// occurrence. Admission excludes a replacement while the captured card is
// claimed and the book decides; projection then uses its own occurrence fence.
func (s *Service) resolveOwnerProposalAt(ctx context.Context, ix proto.Interaction, r proto.Resolution, proof ownerReplyProof, expected *interactionExpectation) error {
	if !ownerActionProposal(ix) || r.Allow == nil || r.Always || r.EditedText != "" || len(r.Values) > 0 {
		return errOwnerActionProof
	}
	proof.BatchID, proof.ItemID = ix.ApprovalBatchID, ix.ApprovalItemID
	book, err := s.ownerActions()
	if err != nil {
		return err
	}
	unlockAdmission := s.lockInteractionAdmission(ix.ID)
	admissionHeld := true
	defer func() {
		if admissionHeld {
			unlockAdmission()
		}
	}()
	r.ModeActor = nil // provenance is never taken from an answer body
	r = honest(ix, r)
	if r.Reason == "" {
		r.Reason = "exact owner-action decision " + ix.ApprovalBatchID
	}
	p, err := s.claimOwnerProposalBy(ix, r, expected, cmpOr(endedByOf("", &r), db.EndedBy(ctx)))
	if err != nil {
		return err
	}
	effective, settled, decisionErr := s.decideOwnerProposal(ctx, book, ix, proof, *r.Allow)
	if !settled && !ownerProposalDefinitiveError(decisionErr) {
		// One immediate idempotent retry normally resolves a transient store
		// error without making the owner wait for background reconciliation.
		retryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), historyTimeout)
		effective, settled, decisionErr = s.decideOwnerProposal(retryCtx, book, ix, proof, *r.Allow)
		cancel()
	}
	if settled {
		s.completeOwnerProposalClaim(p, effective)
		admissionHeld = false
		unlockAdmission()
		return s.syncOwnerActionBatch(context.WithoutCancel(ctx), ix.ApprovalBatchID)
	}
	if ownerProposalDefinitiveError(decisionErr) {
		s.releaseOwnerProposalClaim(p, decisionErr, true)
		return decisionErr
	}
	go s.reconcileOwnerProposalClaim(ix, p, proof, *r.Allow)
	return decisionErr
}

// syncOwnerActionBatch projects durable item decisions onto the original
// cards. It never executes an operation and is safe to call after a restart or
// a duplicate reply.
func (s *Service) syncOwnerActionBatch(ctx context.Context, batchID string) error {
	book, err := s.ownerActions()
	if err != nil {
		return err
	}
	batch, found, err := book.batch(ctx, batchID)
	if err != nil {
		return err
	}
	if !found {
		return errOwnerActionProof
	}
	result := ownerActionDecisionResult{}
	ownerActionFillResult(batch, &result)
	proposal := func(itemID, tool string) proto.Interaction {
		return proto.Interaction{ID: ownerActionInteractionID(batch.ID, itemID), MachineID: batch.MachineID, SessionID: batch.SessionID,
			Kind: proto.IxPermission, ToolName: tool, CreatedAt: batch.CreatedAt, ApprovalBatchID: batch.ID, ApprovalItemID: itemID,
			OwnerProposal: true, Resumable: true, NoExpiry: batch.Persistent}
	}
	project := func(ix proto.Interaction, r proto.Resolution) error {
		expectation := &interactionExpectation{occurrence: &ix}
		current, pending := s.pendingIx(ix.ID)
		if !pending || !ownerActionProposal(current) || !expectation.matches(current) {
			return nil // a replacement is not the durable proposal's card
		}
		if err := s.resolveAnswerAt(ctx, ix.ID, r, true, nil, nil, expectation); err != nil && !errors.Is(err, errNotPending) {
			return err
		}
		return nil
	}
	for _, item := range batch.Items {
		if item.State == ownerActionPending {
			continue
		}
		allow := item.State == ownerActionGranted || item.State == ownerActionConsumed
		r := proto.Resolution{Allow: &allow, Reason: "exact owner-action decision " + batch.ID}
		if err := project(proposal(item.ID, item.Tool), r); err != nil {
			return err
		}
	}
	if result.Complete {
		r := proto.Resolution{Allow: &result.Allow, Reason: "exact owner-action decision " + batch.ID}
		if err := project(proposal("", ownerActionBatchTool), r); err != nil {
			return err
		}
	}
	return nil
}
