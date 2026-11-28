package assistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/proto"
)

// A detached command approval does not make the manager a second shell. Its
// only continuation is a durable, one-time grant for the exact call the node
// already classified. The originating conversation retries that call after the
// result message arrives, and the node executes it through its ordinary hook.
const detachedGrantSetting = "assistant.detached_approval_grants"

var (
	detachedGrantTTL  = 24 * time.Hour
	detachedGrantKeep = 128
)

type detachedGrant struct {
	Fingerprint string    `json:"fingerprint"`
	EditedText  string    `json:"edited_text,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type detachedGrantBook struct {
	mu     sync.Mutex
	loaded bool
	grants map[string]detachedGrant // authoritative interaction id -> one unused approval
}

// detachedFingerprint compares the complete canonical input value, not a parsed
// command or a route. Object key order and formatting are not identity: a
// PostgreSQL JSONB round trip may change both. The command and every other value
// still must match, so a changed call stays behind a fresh card.
func detachedFingerprint(ix proto.Interaction) (string, error) {
	input, err := canonicalDetachedInput(ix.ToolInput)
	if err != nil {
		return "", err
	}
	return detachedFingerprintInput(ix, input)
}

// legacyDetachedFingerprint is the PR1 on-disk grant representation. Its raw
// JSON bytes remain accepted so an upgrade cannot strand an already approved
// one-time continuation. New grants use detachedFingerprint above.
func legacyDetachedFingerprint(ix proto.Interaction) (string, error) {
	return detachedFingerprintInput(ix, ix.ToolInput)
}

func detachedFingerprintInput(ix proto.Interaction, input json.RawMessage) (string, error) {
	b, err := json.Marshal(struct {
		Machine string          `json:"machine"`
		Session string          `json:"session"`
		Cwd     string          `json:"cwd"`
		Kind    string          `json:"kind"`
		Tool    string          `json:"tool"`
		Input   json.RawMessage `json:"input"`
	}{ix.MachineID, ix.SessionID, ix.ExecutionCwd, ix.Kind, ix.ToolName, input})
	return string(b), err
}

// canonicalStoredDetachedFingerprint upgrades PR1's raw-JSON envelope without
// trusting malformed, trailing or unknown data. The destination, tool and call
// kind remain exact; only the input value is canonicalized.
func canonicalStoredDetachedFingerprint(raw string) (string, error) {
	var envelope struct {
		Machine string          `json:"machine"`
		Session string          `json:"session"`
		Cwd     string          `json:"cwd"`
		Kind    string          `json:"kind"`
		Tool    string          `json:"tool"`
		Input   json.RawMessage `json:"input"`
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(&envelope); err != nil {
		return "", err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("multiple fingerprint values")
		}
		return "", err
	}
	input, err := canonicalDetachedInput(envelope.Input)
	if err != nil {
		return "", err
	}
	return detachedFingerprintInput(proto.Interaction{MachineID: envelope.Machine, SessionID: envelope.Session,
		ExecutionCwd: envelope.Cwd, Kind: envelope.Kind, ToolName: envelope.Tool}, input)
}

func detachedFingerprintMatches(stored, canonical string) bool {
	if stored == canonical {
		return true
	}
	upgraded, err := canonicalStoredDetachedFingerprint(stored)
	return err == nil && upgraded == canonical
}

func detachedStoredFingerprintDigest(fingerprint string) (string, bool) {
	canonical, err := canonicalStoredDetachedFingerprint(fingerprint)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:]), true
}

func canonicalDetachedInput(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		// A journal tombstone intentionally omits the executable call. Treat its
		// absent input as JSON null so two identical tombstones can be finalized;
		// live detachable requests still require non-empty input at ingress.
		return json.RawMessage("null"), nil
	}
	canonical, err := proto.CanonicalJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("canonicalize the exact tool input: %w", err)
	}
	return canonical, nil
}

// detachedFingerprintDigest binds a journal without persisting the command or
// other tool arguments that the fingerprint contains.
func detachedFingerprintDigest(ix proto.Interaction) (string, error) {
	fingerprint, err := detachedFingerprint(ix)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(fingerprint))
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) loadDetachedGrantsLocked(ctx context.Context) error {
	b := &s.detached
	if b.loaded {
		return nil
	}
	var grants map[string]detachedGrant
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, detachedGrantSetting, &grants); err != nil {
			return fmt.Errorf("read detached approval grants: %w", err)
		}
	}
	if grants == nil {
		grants = map[string]detachedGrant{}
	}
	b.grants, b.loaded = grants, true
	return nil
}

func (s *Service) saveDetachedGrantsLocked(ctx context.Context, grants map[string]detachedGrant) error {
	if st := s.store(); st != nil {
		return st.PutSetting(ctx, detachedGrantSetting, grants)
	}
	return nil
}

func cloneDetachedGrants(grants map[string]detachedGrant) map[string]detachedGrant {
	out := make(map[string]detachedGrant, len(grants))
	for id, grant := range grants {
		out[id] = grant
	}
	return out
}

// putDetachedGrant persists the grant before the approved result is journalled.
// A repeated Apply of the same card is idempotent; a different grant under the
// same card id is refused rather than replacing an unused owner decision.
func (s *Service) putDetachedGrant(ctx context.Context, ix proto.Interaction, r proto.Resolution) error {
	fingerprint, err := detachedFingerprint(ix)
	if err != nil {
		return err
	}
	b := &s.detached
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.loadDetachedGrantsLocked(ctx); err != nil {
		return err
	}
	if old, ok := b.grants[ix.ID]; ok {
		if detachedFingerprintMatches(old.Fingerprint, fingerprint) && old.EditedText == r.EditedText {
			return nil
		}
		return errors.New("the approval already has a different continuation grant")
	}
	now := time.Now().UTC()
	grants := cloneDetachedGrants(b.grants)
	for id, grant := range grants {
		if !grant.ExpiresAt.After(now) {
			delete(grants, id)
		}
	}
	for len(grants) >= detachedGrantKeep {
		oldest := ""
		for id, grant := range grants {
			if oldest == "" || grant.CreatedAt.Before(grants[oldest].CreatedAt) {
				oldest = id
			}
		}
		delete(grants, oldest)
	}
	grants[ix.ID] = detachedGrant{Fingerprint: fingerprint, EditedText: r.EditedText,
		CreatedAt: now, ExpiresAt: now.Add(detachedGrantTTL)}
	if err := s.saveDetachedGrantsLocked(ctx, grants); err != nil {
		return fmt.Errorf("save detached approval grant: %w", err)
	}
	b.grants = grants
	return nil
}

// consumeDetachedGrant removes and persists one matching grant before allowing
// the retry. If that write fails, the in-memory grant remains and the command
// is not allowed; a later attempt cannot execute twice on a partially consumed
// approval. A changed input or a second retry creates no Allow.
func (s *Service) consumeDetachedGrant(ctx context.Context, ix proto.Interaction) (proto.Resolution, bool, error) {
	fingerprint, err := detachedFingerprint(ix)
	if err != nil {
		return proto.Resolution{}, false, err
	}
	b := &s.detached
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.loadDetachedGrantsLocked(ctx); err != nil {
		return proto.Resolution{}, false, err
	}
	ids := make([]string, 0, len(b.grants))
	for id := range b.grants {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, c := b.grants[ids[i]].CreatedAt, b.grants[ids[j]].CreatedAt
		if a.Equal(c) {
			return ids[i] < ids[j]
		}
		return a.Before(c)
	})
	now := time.Now().UTC()
	for _, id := range ids {
		grant := b.grants[id]
		if !detachedFingerprintMatches(grant.Fingerprint, fingerprint) || !grant.ExpiresAt.After(now) {
			continue
		}
		// A normal retry has a fresh interaction ID. Check the candidate grant's
		// own journal, not the new request's ID, before allowing its exact call.
		entry, journaled, err := s.approvalJournalGrantRecord(ctx, id, grant)
		if err != nil {
			return proto.Resolution{}, false, fmt.Errorf("check detached approval journal: %w", err)
		}
		if journaled && (entry.Status != "approved" || entry.Result == "") {
			return proto.Resolution{Dismiss: true, ApprovalID: id,
				Message: "This approval did not complete with an approved outcome; its continuation cannot execute."}, true, nil
		}
		grants := cloneDetachedGrants(b.grants)
		delete(grants, id)
		if err := s.saveDetachedGrantsLocked(ctx, grants); err != nil {
			return proto.Resolution{}, false, fmt.Errorf("consume detached approval grant: %w", err)
		}
		b.grants = grants
		allow := true
		return proto.Resolution{Allow: &allow, ApprovalID: id, EditedText: grant.EditedText,
			Message: "Allowed once by the earlier approval of this exact call."}, true, nil
	}
	return proto.Resolution{}, false, nil
}

// Older entries have only an update-time cutoff. The original node can replay
// with a fresh display timestamp; use the creation time of its exact stored call.
func (s *Service) detachedReplayJournal(ctx context.Context, ix proto.Interaction) (approvalJournalEntry, bool, error) {
	entry, found, err := s.approvalJournalRecord(ctx, ix)
	if found || err != nil {
		return entry, found, err
	}
	legacy, found, err := s.findApprovalJournal(ctx, func(a approvalJournalEntry) bool {
		return a.ID == ix.ID && a.MachineID == ix.MachineID && a.SessionID == ix.SessionID &&
			a.CreatedAt.IsZero() && a.Fingerprint == ""
	})
	if !found || err != nil {
		return approvalJournalEntry{}, false, err
	}
	st, ok := s.store().(interactionRecorder)
	if !ok {
		return approvalJournalEntry{}, false, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	rec, err := st.InteractionRecord(readCtx, ix.ID)
	if err != nil || rec == nil || !sameApprovalOccurrence(ix, rec.Interaction) {
		return approvalJournalEntry{}, false, err
	}
	return legacy, approvalJournalMatches(legacy, rec.Interaction), nil
}

// journaledDetachedReplay handles a recovered original before it can reach an
// Always rule or register a fresh executable operation. An active process-local
// prepared approval is not recovered: its reconnecting hook keeps the ordinary
// prepared waiter. The returned resolution is always non-executing, even if an
// older terminal database row contained an Allow.
func (s *Service) journaledDetachedReplay(ctx context.Context, ix proto.Interaction) (proto.Resolution, bool, error) {
	entry, found, err := s.detachedReplayJournal(ctx, ix)
	if err != nil || !found {
		return proto.Resolution{}, false, err
	}
	// An original hook may reconnect before either startup pass. It has
	// already released its turn and must never spend the continuation itself.
	if !entry.live && entry.unansweredDetached() {
		waiting, err := s.retainDetachedWaiting(ctx, entry)
		if err == nil && waiting && !s.ownsDetachedWaiting(entry) {
			// An original hook can arrive before either startup pass. Complete
			// journal finalization before asking fleet restore to expose the card.
			b := &s.approvals
			b.mu.Lock()
			ready := b.recoveryDone
			b.mu.Unlock()
			if ready {
				s.restoreInteractions(ctx)
			} else {
				s.recoverManagerApprovalJournals()
			}
			// Restoration may have taken the card past its original deadline or
			// observed a terminal row while finalizing the batch.
			waiting, err = s.retainDetachedWaiting(ctx, entry)
		}
		if err != nil {
			s.logf("defer restoring detached approval %s: %v", ix.ID, err)
			s.approvals.mu.Lock()
			s.retryApprovalRecoveryLocked()
			s.approvals.mu.Unlock()
		}
		if err != nil || waiting {
			if s.ownsDetachedWaiting(entry) {
				return detachedWaitingResolution(ix.ID), true, nil
			}
			return proto.Resolution{Dismiss: true, Pending: true, ApprovalID: ix.ID,
				Message: "The approval is being restored. The command has not executed. Wait for the owner’s decision."}, true, nil
		}
	}
	if err := s.checkRecordedInteractionOccurrence(ctx, ix); err != nil {
		return proto.Resolution{}, true, err
	}
	s.fleet.mu.Lock()
	p := s.fleet.interactions[ix.ID]
	local := p != nil && (p.local != nil || p.prepared != nil && !p.prepared.terminal)
	s.fleet.mu.Unlock()
	if local {
		return proto.Resolution{}, false, nil
	}
	b := &s.approvals
	b.mu.Lock()
	done := b.recoveryDone
	if !done {
		// Recovery will release delivery after its complete batch is final. Until
		// then, finalizing this replay must not start a drain of its siblings.
		b.recovering = true
	}
	b.mu.Unlock()
	if !done {
		if err := s.finishRecoveredApproval(entry); err != nil {
			return proto.Resolution{}, true, err
		}
	}
	_, r, ended := s.outcome(ix.ID)
	if !ended {
		r = proto.Resolution{Reason: "assistant_restarted"}
	}
	r.Allow, r.Approve, r.Pending = nil, nil, false
	r.Dismiss, r.ApprovalID = true, ix.ID
	return r, true, nil
}

// detachedOriginalReplay identifies the original resumable request whose card has
// a late grant or any recorded terminal outcome. The unused grant is enough
// while the outcome write is still in flight; history then prevents an Always
// rule from replacing the original Allow, Deny, expiry or failed outcome.
func (s *Service) detachedOriginalReplay(ctx context.Context, ix proto.Interaction) (bool, error) {
	if !ix.Resumable {
		return false, nil
	}
	b := &s.detached
	b.mu.Lock()
	if err := s.loadDetachedGrantsLocked(ctx); err != nil {
		b.mu.Unlock()
		return false, err
	}
	_, unused := b.grants[ix.ID]
	b.mu.Unlock()
	if unused {
		return true, nil
	}
	_, journaled, err := s.approvalJournalRecord(ctx, ix)
	if err != nil {
		return false, err
	}
	if journaled {
		return true, nil
	}
	st := s.fleetStore()
	if st == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	status, _, err := st.InteractionOutcome(ctx, ix.ID)
	if err != nil {
		return false, fmt.Errorf("read the original detached approval outcome: %w", err)
	}
	return status != "" && status != "pending", nil
}

// revokeDetachedGrant makes an interrupted or otherwise unsuccessful approval
// unusable. It re-reads the durable book on every attempt so a previous failed
// delete is really retried, not hidden by the in-memory cache. A newer occurrence
// may reuse the interaction ID with a different call fingerprint; an older
// journal must not revoke that newer grant. Legacy denied/expired/cancelled or
// covered entries cannot have created a grant after their last update, while a
// failed or unfinished entry still fails closed across the whole ID.
func detachedGrantMatchesJournal(grant detachedGrant, entry approvalJournalEntry) bool {
	if entry.Fingerprint == "" {
		if !grant.CreatedAt.After(entry.UpdatedAt) {
			return true
		}
		switch entry.Status {
		case "denied", "expired", "cancelled", "covered":
			return false
		default:
			return true
		}
	}
	digest, ok := detachedStoredFingerprintDigest(grant.Fingerprint)
	return ok && digest == entry.Fingerprint
}

func (s *Service) revokeDetachedGrant(ctx context.Context, entry approvalJournalEntry) error {
	b := &s.detached
	b.mu.Lock()
	defer b.mu.Unlock()
	id := entry.ID
	var grants map[string]detachedGrant
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, detachedGrantSetting, &grants); err != nil {
			if b.loaded {
				if grant, ok := b.grants[id]; ok && detachedGrantMatchesJournal(grant, entry) {
					delete(b.grants, id)
				}
			}
			return fmt.Errorf("read detached approval grant for revocation: %w", err)
		}
	} else {
		if err := s.loadDetachedGrantsLocked(ctx); err != nil {
			return err
		}
		grants = cloneDetachedGrants(b.grants)
	}
	if grants == nil {
		grants = map[string]detachedGrant{}
	}
	grant, ok := grants[id]
	if !ok {
		if b.loaded {
			delete(b.grants, id)
		}
		return nil
	}
	if !detachedGrantMatchesJournal(grant, entry) {
		return nil
	}
	delete(grants, id)
	if err := s.saveDetachedGrantsLocked(ctx, grants); err != nil {
		if b.loaded {
			if cached, exists := b.grants[id]; exists && detachedGrantMatchesJournal(cached, entry) {
				delete(b.grants, id)
			}
		}
		return fmt.Errorf("revoke detached approval grant: %w", err)
	}
	if b.loaded {
		if cached, exists := b.grants[id]; exists && detachedGrantMatchesJournal(cached, entry) {
			delete(b.grants, id)
		}
	}
	return nil
}

// detachedContinuationPendingLocked reports whether the exact retry is still
// available. s.detached.mu is held across the result handoff too, so a matching
// call either consumes first (and the message becomes a no-retry notice) or the
// continuation reaches the conversation first. New relays carry the journal's
// canonical digest; legacy relays use their result timestamp as a conservative
// cutoff instead of accepting any grant that later reused the ID.
func (s *Service) detachedContinuationPendingLocked(ctx context.Context, id, digest string, cutoff time.Time) (bool, error) {
	if err := s.loadDetachedGrantsLocked(ctx); err != nil {
		return false, err
	}
	grant, ok := s.detached.grants[id]
	if !ok || !grant.ExpiresAt.After(time.Now()) {
		return false, nil
	}
	if digest != "" {
		candidate, ok := detachedStoredFingerprintDigest(grant.Fingerprint)
		return ok && candidate == digest, nil
	}
	if cutoff.IsZero() {
		return false, nil
	}
	return !grant.CreatedAt.After(cutoff), nil
}

// A late release acknowledgement can arrive while the manager still has only
// the durable card. Read the journal only for a pending detachable command.
func (s *Service) detachedNodeEndAfterRelease(ctx context.Context, ix proto.Interaction) (bool, error) {
	if !ix.Detachable || !ix.Resumable {
		return false, nil
	}
	entry, found, err := s.approvalJournalRecord(ctx, ix)
	return found && entry.matchesStored(ix) &&
		(entry.canResumeDetached(ix) || entry.Status == "applying" || entry.Result != ""), err
}

// prepareRestoredDetached runs before the validated pending card is visible.
// Journaled commands use their retained deadline and late-decision path. Leave
// terminal and unreconstructable journal entries to the startup finalizer.
func (s *Service) prepareRestoredDetached(p *pending, stored proto.Interaction, entry approvalJournalEntry) bool {
	if !p.ix.Detachable || entry.ID == "" {
		return true
	}
	if !entry.matchesStored(stored) {
		return true // another pending occurrence reused the runtime ID
	}
	if !p.ix.CreatedAt.Equal(stored.CreatedAt) || !entry.canResumeDetached(stored) || !entry.WaitingUntil.After(time.Now()) {
		return false
	}
	p.ix.ExpiresAt = stored.ExpiresAt
	runCtx := s.runCtx()
	a := s.detachedApproval(runCtx, p.ix)
	a.clarified, a.waiting = true, true
	a.decisionDeadline = p.ix.ExpiresAt
	close(a.release)
	p.prepared = a
	a.decisionTimer = time.AfterFunc(time.Until(a.decisionDeadline), func() {
		if runCtx.Err() == nil {
			s.expirePreparedGen(p, 0)
		}
	})
	return true
}

func (s *Service) detachedApproval(ctx context.Context, ix proto.Interaction) *preparedApproval {
	state := &preparedApproval{caller: ctx, release: make(chan struct{}), deliveries: map[string]clarifyDelivery{}}
	state.op = approvalops.Operation{
		Denied:     "The owner denied this command. Nothing was executed. Do not retry it or create another approval for the same call.",
		Unanswered: "The approval ended without an owner Allow. Nothing was executed; do not retry unless the owner asks.",
		Apply: func(applyCtx context.Context, r proto.Resolution) (string, error) {
			s.fleet.mu.Lock()
			continued := state.clarified
			s.fleet.mu.Unlock()
			if !continued {
				if r.Remembered {
					return "A newer Always allow covers this exact command through the original hook.", nil
				}
				return "The owner allowed this exact command through the original hook.", nil
			}
			if r.Remembered {
				// A newer explicit Always decision covers this sibling card. Its
				// late card must not mint another one-time grant or ask the model to
				// repeat a call that a later owner turn may already have run.
				state.reportStatus = "covered"
				return "A newer Always allow covers this exact call. This older card executed nothing and requests no automatic retry; check whether the call already ran before making a later explicit call.", nil
			}
			if err := s.putDetachedGrant(applyCtx, ix, r); err != nil {
				return "", fmt.Errorf("%w: %v", approvalops.ErrNotApplied, err)
			}
			state.reportStatus = "approved"
			return detachedContinuationText(ix, r), nil
		},
	}
	return state
}

// waitDetachedCard uses the prepared-approval coordinator for its first-answer-
// wins claim, clarification, durable result delivery and short initial wait.
// Apply creates a continuation grant only after the original caller was
// released. An immediate Allow returns through the original hook and leaves no
// unused second execution behind.
func (s *Service) waitDetachedCard(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
	state := s.detachedApproval(ctx, ix)
	res, err := s.waitInteractionPrepared(ctx, ix, state)
	if errors.Is(err, errApprovalClarifying) {
		s.fleet.mu.Lock()
		waiting := state.waiting
		s.fleet.mu.Unlock()
		if waiting {
			return detachedWaitingResolution(ix.ID), nil
		}
		text := approvalops.ClarifyingText
		return proto.Resolution{Dismiss: true, Pending: true, ApprovalID: ix.ID, Message: text}, nil
	}
	if err != nil {
		return proto.Resolution{}, err
	}
	if state.err != nil {
		return proto.Resolution{}, state.err
	}
	if res.ApprovalID != "" && res.Allow != nil && *res.Allow {
		// A detached Allow is a continuation fact, never an execution permit. The
		// only Allow for a late-approved call comes from consumeDetachedGrant.
		// This also closes a resumable interaction.create replay after its grant
		// was consumed; an immediate Allow has no ApprovalID and keeps the ordinary
		// resumable-hook behavior.
		return proto.Resolution{Dismiss: true, ApprovalID: res.ApprovalID,
			Message: "The one-time continuation for this approval was already consumed or must be consumed by its exact retry; do not replay the original approval."}, nil
	}
	return res, nil
}

func detachedWaitingResolution(id string) proto.Resolution {
	return proto.Resolution{Dismiss: true, Pending: true, ApprovalID: id, Message: approvalops.WaitingText + " The command was not executed."}
}

func detachedContinuationText(ix proto.Interaction, r proto.Resolution) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The owner approved one retry of this exact tool call. Nothing has run yet. Retry the same tool now with exactly this input: %s. The one-time grant is consumed by that retry; do not repeat the call after it.", ix.ToolInput)
	if r.EditedText != "" {
		fmt.Fprintf(&b, " The owner replaced the editable text with: %s. Use the normal edited-answer path for that retry.", r.EditedText)
	}
	return b.String()
}
