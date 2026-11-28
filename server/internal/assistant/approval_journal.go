package assistant

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	approvalJournalKey           = "assistant.approval_clarifications"
	approvalRestartCancelledText = "The assistant restarted, so nothing was applied and the retained operation was cancelled without executing. Ask for a fresh approval card if the change is still wanted."
	invalidationSummaryRunes     = 200
	invalidationLabelRunes       = 80
)

var (
	errApprovalConversationGone = errors.New("the originating conversation no longer exists")
	// errApprovalExecutionUncertain distinguishes a durable interrupted/unknown
	// apply from an operation that is known not to have started. A restored owner
	// claim must never turn the former into a confident cancellation or run it again.
	errApprovalExecutionUncertain = errors.New("the earlier approval execution outcome is uncertain")
)

// approvalJournalEntry contains delivery state and a minimal safe summary only.
// In particular it has no executable callback, raw tool arguments, secret text,
// Resolution.Values or approval fields.
type approvalJournalEntry struct {
	ID          string    `json:"id"`
	MachineID   string    `json:"machine_id"`
	SessionID   string    `json:"session_id"`
	CreatedAt   time.Time `json:"created_at,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	Kind        string    `json:"kind,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	Status      string    `json:"status,omitempty"`
	Result      string    `json:"result,omitempty"`
	// Finalized means this occurrence has a durable terminal interaction record,
	// or a different occurrence under its ID is already terminal and therefore
	// also prevents the original from replaying. Results never deliver without it.
	Finalized bool `json:"finalized,omitempty"`
	Private   bool `json:"private,omitempty"`
	// Continuation marks a detached command result whose retry instruction is
	// valid only while its one-time grant remains unused.
	// WaitingUntil is written with the transition out of the original hook.
	// It identifies the retained decision window in the interaction row.
	WaitingUntil time.Time `json:"waiting_until,omitzero"`
	Continuation bool      `json:"continuation,omitempty"`
	Accepted     bool      `json:"accepted,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
	// live is process-local and never persisted. A delayed startup retry must not
	// mistake an approval created by this process for an interrupted old one.
	live bool
}

// safeInteractionSummary extracts only the minimum safe human-readable context
// needed for a terminal or invalidation notice. It never reads ToolInput,
// EditableText, Screen, full Plan text, or secret prompt/field content.
func safeInteractionSummary(ix proto.Interaction) string {
	if ix.Secret || hasSecretField(ix) {
		return clip(oneLine(cmp.Or(ix.ToolTitle, ix.ToolName, "secret confirmation")), invalidationLabelRunes)
	}
	if ix.Kind == proto.IxQuestion && len(ix.Questions) > 0 {
		if line := strings.TrimSpace(questionLine(ix)); line != "" {
			return clip(oneLine(line), invalidationSummaryRunes)
		}
	}
	if summary := strings.TrimSpace(ix.Summary); summary != "" {
		return clip(oneLine(summary), invalidationSummaryRunes)
	}
	if ix.Kind == proto.IxPlan && strings.TrimSpace(ix.Plan) != "" {
		if title := strings.TrimSpace(planTitle(ix.Plan)); title != "" {
			return clip(oneLine(title), invalidationSummaryRunes)
		}
	}
	if ix.Kind == proto.IxElicitation && ix.Elicitation != nil && strings.TrimSpace(ix.Elicitation.Message) != "" {
		return clip(oneLine(ix.Elicitation.Message), invalidationSummaryRunes)
	}
	return clip(oneLine(cmp.Or(ix.ToolTitle, ix.ToolName)), invalidationLabelRunes)
}

func (s *Service) loadApprovalJournalLocked(ctx context.Context) bool {
	b := &s.approvals
	if b.loaded {
		return true
	}
	var list []approvalJournalEntry
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, approvalJournalKey, &list); err != nil {
			s.logf("load approval clarification journal: %v", err)
			return false
		}
	}
	b.list, b.loaded = list, true
	// Reconnect may load the journal before the scheduler. Gate result delivery
	// before an owner can claim a restored command from this batch.
	b.recovering = b.recovering || slices.ContainsFunc(list, approvalJournalEntry.unansweredDetached)
	return true
}

func (s *Service) approvalJournalRecord(ctx context.Context, ix proto.Interaction) (approvalJournalEntry, bool, error) {
	return s.findApprovalJournal(ctx, func(a approvalJournalEntry) bool { return approvalJournalMatches(a, ix) })
}

func (s *Service) findApprovalJournal(ctx context.Context, matches func(approvalJournalEntry) bool) (approvalJournalEntry, bool, error) {
	b := &s.approvals
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadApprovalJournalLocked(ctx) {
		return approvalJournalEntry{}, false, errors.New("the clarification journal is unavailable")
	}
	for i := len(b.list) - 1; i >= 0; i-- {
		if matches(b.list[i]) {
			return b.list[i], true, nil
		}
	}
	return approvalJournalEntry{}, false, nil
}

func (s *Service) approvalJournalGrantRecord(ctx context.Context, id string, grant detachedGrant) (approvalJournalEntry, bool, error) {
	b := &s.approvals
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadApprovalJournalLocked(ctx) {
		return approvalJournalEntry{}, false, errors.New("the clarification journal is unavailable")
	}
	digest, _ := detachedStoredFingerprintDigest(grant.Fingerprint)
	for i := len(b.list) - 1; i >= 0; i-- {
		a := b.list[i]
		if a.ID == id && (a.Fingerprint != "" && digest != "" && a.Fingerprint == digest ||
			a.Fingerprint == "" && detachedGrantMatchesJournal(grant, a)) {
			return a, true, nil
		}
	}
	return approvalJournalEntry{}, false, nil
}

func (s *Service) saveApprovalJournalLocked(ctx context.Context) error {
	if st := s.store(); st != nil {
		list := s.approvals.list
		if list == nil {
			list = []approvalJournalEntry{}
		}
		return st.PutSetting(ctx, approvalJournalKey, list)
	}
	return nil
}

func approvalJournalOccurrence(ix proto.Interaction) (time.Time, string, error) {
	if ix.Detachable {
		fingerprint, err := detachedFingerprintDigest(ix)
		return ix.CreatedAt, fingerprint, err
	}
	return ix.CreatedAt, "", nil
}

func (s *Service) approvalJournalStarted(ctx context.Context, ix proto.Interaction) error {
	return s.startApprovalJournal(ctx, ix, time.Time{})
}

func (s *Service) startApprovalJournal(ctx context.Context, ix proto.Interaction, waitingUntil time.Time) error {
	createdAt, fingerprint, err := approvalJournalOccurrence(ix)
	if err != nil {
		return err
	}
	b := &s.approvals
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadApprovalJournalLocked(ctx) {
		return errors.New("the clarification journal is unavailable")
	}
	summary := safeInteractionSummary(ix)
	if i := slices.IndexFunc(b.list, func(a approvalJournalEntry) bool { return approvalJournalMatches(a, ix) }); i >= 0 {
		old := b.list[i]
		b.list[i].WaitingUntil = waitingUntil
		b.list[i].live = true
		if b.list[i].Summary == "" {
			b.list[i].Summary = summary
		}
		if b.list[i].Kind == "" {
			b.list[i].Kind = ix.Kind
		}
		if err := s.saveApprovalJournalLocked(ctx); err != nil {
			b.list[i] = old
			return fmt.Errorf("keep the clarification result journal: %w", err)
		}
		return nil
	}
	b.list = append(b.list, approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID,
		CreatedAt: createdAt, Fingerprint: fingerprint, Kind: ix.Kind, Summary: summary, Private: ix.Private, WaitingUntil: waitingUntil,
		UpdatedAt: time.Now().UTC(), live: true})
	if err := s.saveApprovalJournalLocked(ctx); err != nil {
		b.list = slices.DeleteFunc(b.list, func(a approvalJournalEntry) bool {
			return approvalJournalMatches(a, ix) && a.Result == ""
		})
		return fmt.Errorf("keep the clarification result journal: %w", err)
	}
	return nil
}

// approvalJournalApplying is persisted immediately before the retained
// operation executes. A restart in this state has an uncertain outcome, never a
// fabricated cancellation or success.
func (s *Service) approvalJournalApplying(ctx context.Context, ix proto.Interaction) error {
	createdAt, fingerprint, err := approvalJournalOccurrence(ix)
	if err != nil {
		return err
	}
	b := &s.approvals
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadApprovalJournalLocked(ctx) {
		return errors.New("the clarification journal is unavailable")
	}
	summary := safeInteractionSummary(ix)
	i := slices.IndexFunc(b.list, func(a approvalJournalEntry) bool { return approvalJournalMatches(a, ix) })
	if i < 0 {
		b.list = append(b.list, approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID,
			CreatedAt: createdAt, Fingerprint: fingerprint, Kind: ix.Kind, Summary: summary, Private: ix.Private, live: true})
		i = len(b.list) - 1
	} else {
		if ix.Private {
			b.list[i].Private = true
		}
		if b.list[i].Summary == "" && summary != "" {
			b.list[i].Summary = summary
		}
		if b.list[i].Kind == "" && ix.Kind != "" {
			b.list[i].Kind = ix.Kind
		}
		if b.list[i].CreatedAt.IsZero() {
			b.list[i].CreatedAt = createdAt
		}
		if b.list[i].Fingerprint == "" {
			b.list[i].Fingerprint = fingerprint
		}
	}
	b.list[i].live = true
	if b.list[i].Result != "" {
		if b.list[i].Status == "failed" {
			return fmt.Errorf("%w: %s", errApprovalExecutionUncertain, b.list[i].Result)
		}
		return errors.New("the approval already has a terminal result")
	}
	b.list[i].Status = "applying"
	b.list[i].UpdatedAt = time.Now().UTC()
	if err := s.saveApprovalJournalLocked(ctx); err != nil {
		return fmt.Errorf("save approval execution state: %w", err)
	}
	return nil
}

func (s *Service) approvalJournalAborted(ctx context.Context, ix proto.Interaction) {
	b := &s.approvals
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadApprovalJournalLocked(ctx) {
		return
	}
	b.list = slices.DeleteFunc(b.list, func(a approvalJournalEntry) bool {
		return approvalJournalMatches(a, ix) && a.Status == "" && a.Result == "" && !a.Accepted
	})
	if err := s.saveApprovalJournalLocked(ctx); err != nil {
		s.logf("abort approval clarification journal: %v", err)
	}
}

// approvalResultText always names the literal interaction ID and says that the
// original card is no longer waiting, including when execution is uncertain.
func approvalResultText(id, status, text string) string {
	return approvalResultTextWithSummary(id, status, "", text)
}

func approvalResultTextWithSummary(id, status, summary, text string) string {
	word := map[string]string{
		"resolved": "applied", "approved": "approved", "covered": "covered by a newer Always allow", "denied": "denied", "expired": "expired",
		"replaced": "replaced", "stale": "stale and cancelled", "cancelled": "cancelled", "dismissed": "dismissed",
		"failed": "left with an uncertain execution outcome",
	}[status]
	if word == "" {
		word = "finished"
	}
	if summary != "" {
		return fmt.Sprintf("Approval %s is %s and is no longer pending. Original request: %s. %s", id, word, summary, text)
	}
	return fmt.Sprintf("Approval %s is %s and is no longer pending. %s", id, word, text)
}

func questionInvalidationResultText(id, summary string) string {
	if summary != "" {
		return fmt.Sprintf("Question %s was cancelled by a restart or upgrade and is no longer pending. Original question: %s. Nothing was applied. Create a fresh ask_user card if you still need the user's answer.", id, summary)
	}
	return fmt.Sprintf("Question %s was cancelled by a restart or upgrade and is no longer pending. Nothing was applied. Create a fresh ask_user card if you still need the user's answer.", id)
}

func (s *Service) approvalJournalFinished(ctx context.Context, ix proto.Interaction, status, text string, finalized bool) approvalJournalEntry {
	createdAt, fingerprint, err := approvalJournalOccurrence(ix)
	if err != nil {
		finalized = false
		s.logf("record approval occurrence %s: %v", ix.ID, err)
	}
	b := &s.approvals
	b.mu.Lock()
	if !s.loadApprovalJournalLocked(ctx) {
		b.mu.Unlock()
		return approvalJournalEntry{}
	}
	summary := safeInteractionSummary(ix)
	prevSummary := ""
	i := slices.IndexFunc(b.list, func(a approvalJournalEntry) bool { return approvalJournalMatches(a, ix) })
	if i < 0 {
		b.list = append(b.list, approvalJournalEntry{ID: ix.ID, MachineID: ix.MachineID, SessionID: ix.SessionID,
			CreatedAt: createdAt, Fingerprint: fingerprint, Kind: ix.Kind, Summary: summary, Private: ix.Private})
		i = len(b.list) - 1
	} else {
		prevSummary = b.list[i].Summary
		if ix.Private {
			// Upgrade a legacy or recovered entry before its terminal delivery.
			b.list[i].Private = true
		}
		if b.list[i].Summary == "" && summary != "" {
			b.list[i].Summary = summary
		}
		if b.list[i].Kind == "" && ix.Kind != "" {
			b.list[i].Kind = ix.Kind
		}
		if b.list[i].CreatedAt.IsZero() {
			b.list[i].CreatedAt = createdAt
		}
		if b.list[i].Fingerprint == "" {
			b.list[i].Fingerprint = fingerprint
		}
	}
	if !b.list[i].Finalized {
		// The one live claim's honest execution result supersedes an unfinished
		// journal's restart-generated cancellation. Without this, recovery could
		// keep and deliver "cancelled" after the owner's operation already ran.
		if b.list[i].CreatedAt.IsZero() {
			b.list[i].CreatedAt = createdAt
		}
		if b.list[i].Fingerprint == "" {
			b.list[i].Fingerprint = fingerprint
		}
		preserveRecorded := b.list[i].Result != "" && b.list[i].Status == status &&
			b.list[i].Result == approvalResultTextWithSummary(ix.ID, status, prevSummary, text)
		b.list[i].Status = status
		if !preserveRecorded {
			b.list[i].Result = approvalResultTextWithSummary(ix.ID, status, b.list[i].Summary, text)
		}
		b.list[i].Continuation = ix.Detachable && status == "approved"
		b.list[i].UpdatedAt = time.Now().UTC()
	}
	b.list[i].Finalized = b.list[i].Finalized || finalized
	if b.handled == nil {
		b.handled = map[string]bool{}
	}
	b.handled[ix.ID] = true
	if err := s.saveApprovalJournalLocked(ctx); err != nil {
		s.logf("save approval result %s: %v", ix.ID, err)
	}
	entry := b.list[i]
	deliver := !b.recovering
	b.mu.Unlock()
	if deliver {
		go s.deliverApprovalResults()
	}
	return entry
}

func sameApprovalJournalOccurrence(a, b approvalJournalEntry) bool {
	if a.ID != b.ID || a.MachineID != b.MachineID || a.SessionID != b.SessionID {
		return false
	}
	if a.Fingerprint != "" || b.Fingerprint != "" {
		return a.Fingerprint == b.Fingerprint
	}
	return a.CreatedAt.Equal(b.CreatedAt)
}

func (s *Service) currentApprovalJournalEntry(ctx context.Context, entry approvalJournalEntry) (approvalJournalEntry, bool, error) {
	b := &s.approvals
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadApprovalJournalLocked(ctx) {
		return approvalJournalEntry{}, false, errors.New("the clarification journal is unavailable")
	}
	i := slices.IndexFunc(b.list, func(a approvalJournalEntry) bool { return sameApprovalJournalOccurrence(a, entry) })
	if i < 0 {
		return approvalJournalEntry{}, false, nil
	}
	return b.list[i], true, nil
}

func (s *Service) approvalJournalFinalized(ctx context.Context, entry approvalJournalEntry) error {
	b := &s.approvals
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadApprovalJournalLocked(ctx) {
		return errors.New("the clarification journal is unavailable")
	}
	i := slices.IndexFunc(b.list, func(a approvalJournalEntry) bool { return sameApprovalJournalOccurrence(a, entry) })
	if i < 0 || b.list[i].Finalized {
		return nil
	}
	b.list[i].Finalized = true
	if err := s.saveApprovalJournalLocked(ctx); err != nil {
		b.list[i].Finalized = false
		return fmt.Errorf("persist finalized approval occurrence: %w", err)
	}
	return nil
}

func recoveredTerminalJournalStatus(ix proto.Interaction, status string, resolution *proto.Resolution) string {
	if status != "resolved" {
		return status
	}
	if resolution != nil {
		switch {
		case ix.Detachable && resolution.UserAllowed() && resolution.Remembered:
			return "covered"
		case ix.Detachable && resolution.UserAllowed():
			return "approved"
		case resolution.UserDenied():
			return "denied"
		}
	}
	return "resolved"
}

// adoptRecoveredTerminal reconciles a stale restart-delivery entry with the
// terminal outcome already durable for the same occurrence. The caller holds
// fleet.mu and has no matching live claimed card, so a live finalizer cannot be
// between its history and journal writes. In particular an owner Allow keeps
// its continuation grant instead of being revoked as a restart cancellation.
func (s *Service) adoptRecoveredTerminal(ctx context.Context, entry approvalJournalEntry, ix proto.Interaction, status string, resolution *proto.Resolution) (approvalJournalEntry, error) {
	journalStatus := recoveredTerminalJournalStatus(ix, status, resolution)
	text := "The terminal outcome already recorded for this exact approval was preserved; restart recovery did not replace it."
	b := &s.approvals
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadApprovalJournalLocked(ctx) {
		return approvalJournalEntry{}, errors.New("the clarification journal is unavailable")
	}
	i := slices.IndexFunc(b.list, func(a approvalJournalEntry) bool { return sameApprovalJournalOccurrence(a, entry) })
	if i < 0 {
		return approvalJournalEntry{}, errors.New("the recovered approval journal entry disappeared")
	}
	b.list[i].Status = journalStatus
	b.list[i].Result = approvalResultText(ix.ID, journalStatus, text)
	b.list[i].Continuation = ix.Detachable && journalStatus == "approved"
	b.list[i].UpdatedAt = time.Now().UTC()
	if err := s.saveApprovalJournalLocked(ctx); err != nil {
		return approvalJournalEntry{}, fmt.Errorf("adopt the durable approval outcome: %w", err)
	}
	return b.list[i], nil
}

// deliverApprovalResults hands each result to the exact conversation or its
// durable relay queue. There is deliberately no fallback to today's main chat.
// A single drain prevents concurrent accepts; a result added during a drain
// triggers one more pass. Transient failures wait for the retry timer; a result
// handed to the conversation or durable queue is removed. A result is
// permanently undeliverable only when the deletion ledger names its original
// conversation; an uncertain execution outcome stays durable even then.
func (s *Service) deliverApprovalResults() {
	b := &s.approvals
	b.mu.Lock()
	if !s.loadApprovalJournalLocked(context.Background()) {
		b.mu.Unlock()
		return
	}
	if b.recovering {
		b.again = true
		b.mu.Unlock()
		return
	}
	if b.delivering {
		b.again = true
		b.mu.Unlock()
		return
	}
	b.delivering = true
	for {
		b.again = false
		var due []approvalJournalEntry
		kept := b.list[:0]
		cleaned := false
		for _, a := range b.list {
			if a.Accepted {
				// Versions before result cleanup retained handed-off entries forever.
				cleaned = true
				continue
			}
			kept = append(kept, a)
			if a.Result != "" {
				due = append(due, a)
			}
		}
		b.list = kept
		if cleaned {
			if err := s.saveApprovalJournalLocked(context.Background()); err != nil {
				s.logf("clean handed-off approval results: %v", err)
			}
		}
		b.mu.Unlock()
		failed := false
		for _, a := range due {
			if !a.Finalized {
				// A live finalizer and startup recovery share this boundary. Do not
				// hand off or delete a result until its occurrence is durably terminal.
				if err := s.finishRecoveredApproval(a); err != nil {
					failed = true
					s.logf("finalize approval result %s before delivery: %v", a.ID, err)
					continue
				}
				current, found, err := s.currentApprovalJournalEntry(context.Background(), a)
				if err != nil || !found {
					failed = true
					s.logf("reload finalized approval result %s before delivery: found=%v err=%v", a.ID, found, err)
					continue
				}
				a = current
			}
			err := s.deliverApprovalResult(a)
			gone := errors.Is(err, errApprovalConversationGone)
			if err != nil && !gone {
				failed = true
				s.logf("deliver approval result %s: %v", a.ID, err)
				continue
			}
			if gone {
				s.logf("discard approval result %s: %v", a.ID, err)
			}
			if a.Status != "approved" {
				// Never discard the last recovery record while a failed durable
				// revocation could still resurrect an execution grant on restart.
				if err := s.revokeDetachedGrant(context.Background(), a); err != nil {
					failed = true
					s.logf("retain approval result %s until grant revocation persists: %v", a.ID, err)
					continue
				}
			}
			if gone && a.Status == "failed" {
				// A deleted destination cannot receive the warning, but its durable
				// record is the last evidence that execution may have happened. The
				// revocation above has still run; only its failure schedules a retry.
				s.logf("retain uncertain approval result %s for recovery: %v", a.ID, err)
				continue
			}
			b.mu.Lock()
			before := len(b.list)
			b.list = slices.DeleteFunc(b.list, func(x approvalJournalEntry) bool {
				return x.ID == a.ID && x.CreatedAt.Equal(a.CreatedAt) && x.Fingerprint == a.Fingerprint &&
					x.UpdatedAt.Equal(a.UpdatedAt) && x.Result == a.Result
			})
			if len(b.list) != before {
				if err := s.saveApprovalJournalLocked(context.Background()); err != nil {
					s.logf("remove handed-off approval result %s: %v", a.ID, err)
				}
			}
			b.mu.Unlock()
		}
		b.mu.Lock()
		if failed {
			s.scheduleApprovalResultRetryLocked()
		}
		if !b.again {
			b.delivering = false
			b.mu.Unlock()
			return
		}
	}
}

// approvalResultRetryDelay is how long a transient delivery failure waits before the next pass
// (approvalBook.retryDelay shortens it for one manager in tests).
const approvalResultRetryDelay = time.Minute

// scheduleApprovalResultRetryLocked arms one retry pass. It belongs to the run that armed it: once that run has ended
// (the manager stopped, its store and bus closed), the pass does nothing and arms no other; the next manager's Run
// delivers what is left from the durable journal (recoverApprovalJournals, #16045). Before Run, with no run context,
// it always retries.
func (s *Service) scheduleApprovalResultRetryLocked() {
	b := &s.approvals
	if b.retry {
		return
	}
	b.retry = true
	run := s.runCtx()
	time.AfterFunc(cmp.Or(b.retryDelay, approvalResultRetryDelay), func() {
		b.mu.Lock()
		b.retry = false
		b.mu.Unlock()
		if run.Err() != nil {
			s.logf("approval results: retry skipped, the manager's run ended; the next run delivers them from the journal")
			return
		}
		s.deliverApprovalResults()
	})
}

func (s *Service) deliverApprovalResult(a approvalJournalEntry) error {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if a.Continuation {
		// Serialize with grant consumption through the actual conversation/queue
		// handoff. If another call consumed first, the late result must not tell
		// the model to execute that same call again (especially under Always).
		s.detached.mu.Lock()
		defer s.detached.mu.Unlock()
		pending, err := s.detachedContinuationPendingLocked(ctx, a.ID, a.Fingerprint, a.UpdatedAt)
		if err != nil {
			return fmt.Errorf("check detached continuation before delivery: %w", err)
		}
		if !pending {
			a.Result = consumedContinuationText(a.ID)
		}
	}
	if _, deleted, err := s.deletedConversation(ctx, a.MachineID, a.SessionID); err != nil {
		return fmt.Errorf("check the originating conversation's deletion record: %w", err)
	} else if deleted {
		return errApprovalConversationGone
	}
	var res struct {
		OK bool `json:"ok"`
	}
	if err := s.call(ctx, a.MachineID, DeviceSessionGet, map[string]any{"session_id": a.SessionID}, &res); err != nil {
		return err
	}
	if !res.OK {
		// session.get reads the manager's current cache. A restart, an offline
		// node, or a list that has not arrived yet says nothing about deletion.
		return errors.New("the originating conversation is not currently listed")
	}
	title := "Approval result"
	if a.Kind == proto.IxQuestion {
		title = "Question result"
	}
	msg := relayMsg{From: approvalResultFrom + a.ID, FromTitle: title, Tool: askAnswerTool,
		Text: a.Result, At: a.UpdatedAt, Private: a.Private, Continuation: a.Continuation, ContinuationDigest: a.Fingerprint}
	if msg.At.IsZero() {
		msg.At = time.Now().UTC()
	}
	if msg.Continuation && msg.OnceID == "" {
		msg.OnceID = relayMsgID(a.SessionID, msg)
	}
	working, placeholder := false, false
	if c, ok := s.session(ctx, a.MachineID, a.SessionID); ok {
		working = c.Status == proto.StatusWorking || c.Starting
		placeholder = c.LogicalPlaceholder || (c.Assistant && c.Cwd == "" && c.Space == "" && s.isLogicalOfflineRow(c))
	}
	if !working {
		if err := s.tryDirectRelay(ctx, a.MachineID, a.SessionID, &msg); err == nil {
			return nil
		} else if s.refusedDirect(ctx, a.MachineID, a.SessionID, msg, err) {
			return nil // a runtime conversation that cannot take it: the owner was pushed (relayRefused)
		} else if !placeholder && relayFailureOf(err) == relayFailedGone {
			return errApprovalConversationGone
		} else if !relayInDoubt(err) && !refusedForGood(err) {
			// A transient send refusal may still be retried through the durable
			// queue; an actually missing conversation was rejected above.
			s.logf("approval result %s direct send: %v; queued", a.ID, err)
		}
	}
	return s.queueApprovalResult(a.MachineID, a.SessionID, msg)
}

func consumedContinuationText(id string) string {
	return fmt.Sprintf("Approval %s is no longer pending. Its one-time continuation was already consumed or is no longer available. Do not retry it automatically; check whether the exact call already ran.", id)
}

// tryQueuedApprovalRelay rechecks a detached continuation at the last possible
// boundary. Grant consumption stays excluded through the actual send, including
// when an Always rule would let the requested retry execute without a prompt.
func (s *Service) tryQueuedApprovalRelay(ctx context.Context, machine, to string, original *relayMsg) (relayMsg, time.Duration, error) {
	if !original.Continuation {
		return s.tryQueuedRelay(ctx, machine, to, original)
	}
	msg := *original
	s.detached.mu.Lock()
	defer s.detached.mu.Unlock()
	id := strings.TrimPrefix(msg.From, approvalResultFrom)
	pending, err := s.detachedContinuationPendingLocked(ctx, id, msg.ContinuationDigest, msg.At)
	if err != nil {
		return msg, time.Minute, fmt.Errorf("recheck queued detached continuation: %w", err)
	}
	if !pending {
		msg.Text = consumedContinuationText(id)
	}
	return s.tryQueuedRelayText(ctx, machine, to, original, msg)
}

// queueApprovalResult deduplicates by the stable once_id before persisting.
func (s *Service) queueApprovalResult(machine, to string, msg relayMsg) error {
	if msg.Continuation && msg.OnceID == "" {
		msg.OnceID = relayMsgID(to, msg)
	}
	b := &s.relays
	b.mu.Lock()
	if !s.relayLocked(context.Background()) {
		b.mu.Unlock()
		return errors.New("the relay queue is unavailable")
	}
	id := relayMsgID(to, msg)
	for _, old := range b.st.Queue[to] {
		if relayMsgID(to, old) == id || sameLegacyContinuationRelay(old, msg) {
			b.mu.Unlock()
			return nil
		}
	}
	msg.QueuedAt, msg.Depth = time.Now().UTC(), len(b.st.Queue[to])+1
	b.st.Queue[to] = append(b.st.Queue[to], msg)
	if err := s.saveRelayLocked(); err != nil {
		// The journal still owns this result. Do not acknowledge an in-memory-only
		// queue entry that a restart could lose; let the durable journal retry it.
		q := b.st.Queue[to]
		if len(q) == 1 {
			delete(b.st.Queue, to)
		} else {
			b.st.Queue[to] = q[:len(q)-1]
		}
		b.mu.Unlock()
		return fmt.Errorf("persist approval result relay: %w", err)
	}
	b.mu.Unlock()
	if c, ok := s.session(context.Background(), machine, to); ok {
		s.drainRelays(machine, []proto.Session{c})
	}
	return nil
}

// lookupInteractionSummary backfills a safe summary from the interaction record
// for entries persisted by an older binary before Summary was recorded.
func (s *Service) lookupInteractionSummary(ctx context.Context, id string) string {
	st, ok := s.store().(interactionRecorder)
	if !ok {
		return ""
	}
	readCtx, cancel := context.WithTimeout(ctx, historyTimeout)
	rec, err := func() (rec *db.InteractionRecord, err error) {
		defer guard(&err)
		return st.InteractionRecord(readCtx, id)
	}()
	cancel()
	if err != nil || rec == nil {
		return ""
	}
	return safeInteractionSummary(rec.Interaction)
}

// shouldNotifyRestartInvalidated reports whether a non-resumable interaction
// cancelled by startup reconciliation belongs to an assistant conversation that
// needs a safe invalidation notice to issue a fresh card. Resumable cards keep
// their existing restart behavior, and coding-agent / one-shot cards are untouched.
func shouldNotifyRestartInvalidated(ix proto.Interaction) bool {
	if ix.Resumable || ix.MachineID == "" || ix.SessionID == "" {
		return false
	}
	return ix.ToolName == "ask_user" || ix.CanClarify
}

// recordRestartInvalidation queues one contextual invalidation notice on the
// originating conversation's durable route for a non-resumable card cancelled
// during startup reconciliation. It never duplicates an entry already handled by
// approvalJournal and never overwrites an interrupted applying outcome.
func (s *Service) recordRestartInvalidation(ctx context.Context, ix proto.Interaction) {
	if !shouldNotifyRestartInvalidated(ix) {
		return
	}
	summary := safeInteractionSummary(ix)
	b := &s.approvals
	b.mu.Lock()
	if !s.loadApprovalJournalLocked(ctx) {
		b.mu.Unlock()
		return
	}
	if b.handled[ix.ID] {
		b.mu.Unlock()
		return
	}
	if i := slices.IndexFunc(b.list, func(a approvalJournalEntry) bool { return a.ID == ix.ID }); i >= 0 {
		// Already tracked in the approval journal (e.g. clarifying or applying):
		// backfill a missing summary for older pre-upgrade entries, and let
		// recoverApprovalJournals decide the exact terminal state.
		if b.list[i].Summary == "" && summary != "" {
			b.list[i].Summary = summary
			if err := s.saveApprovalJournalLocked(ctx); err != nil {
				s.logf("save recovered approval summary %s: %v", ix.ID, err)
			}
		}
		b.mu.Unlock()
		return
	}
	var result string
	if ix.Kind == proto.IxQuestion {
		result = questionInvalidationResultText(ix.ID, summary)
	} else {
		result = approvalResultTextWithSummary(ix.ID, "cancelled", summary, approvalRestartCancelledText)
	}
	b.list = append(b.list, approvalJournalEntry{
		ID:        ix.ID,
		MachineID: ix.MachineID,
		SessionID: ix.SessionID,
		Kind:      ix.Kind,
		Summary:   summary,
		Status:    "cancelled",
		Result:    result,
		Private:   ix.Private,
		UpdatedAt: time.Now().UTC(),
	})
	if b.handled == nil {
		b.handled = map[string]bool{}
	}
	b.handled[ix.ID] = true
	if err := s.saveApprovalJournalLocked(ctx); err != nil {
		s.logf("save restart invalidation %s: %v", ix.ID, err)
	}
	b.mu.Unlock()
	go s.deliverApprovalResults()
}

// Only a complete persisted command can rebuild the one-time continuation.
func (a approvalJournalEntry) unansweredDetached() bool {
	return a.Status == "" && a.Result == "" && !a.Finalized && !a.Accepted && a.Fingerprint != "" && !a.WaitingUntil.IsZero()
}

// Unlike the original node hook, which may refresh its display timestamp, the
// stored row and waiting journal carry the manager's canonical creation time.
func (a approvalJournalEntry) matchesStored(ix proto.Interaction) bool {
	return (a.WaitingUntil.IsZero() || ix.CreatedAt.Equal(a.CreatedAt)) && approvalJournalMatches(a, ix)
}

func (a approvalJournalEntry) canResumeDetached(ix proto.Interaction) bool {
	return a.unansweredDetached() && ix.Detachable && ix.Resumable && ix.Kind == proto.IxPermission &&
		ix.MachineID != "" && ix.SessionID != "" && ix.ToolName != "" && ix.ExecutionCwd != "" && len(ix.ToolInput) > 0 &&
		ix.ExpiresAt.Equal(a.WaitingUntil) && a.matchesStored(ix)
}

// Journal reconciliation classifies the original durable row without starting
// the fleet expiration sweep. Terminal siblings must finalize in this batch
// before a reconnecting hook can initiate that sweep.
func (s *Service) retainDetachedWaiting(ctx context.Context, entry approvalJournalEntry) (bool, error) {
	if s.ownsDetachedWaiting(entry) {
		return true, nil
	}
	recorder, ok := s.store().(interactionRecorder)
	if !ok {
		return false, nil
	}
	readCtx, cancel := context.WithTimeout(ctx, historyTimeout)
	defer cancel()
	rec, err := recorder.InteractionRecord(readCtx, entry.ID)
	if err != nil {
		return false, err
	}
	if rec != nil && rec.Status == "pending" && entry.canResumeDetached(rec.Interaction) && entry.WaitingUntil.After(time.Now()) {
		return true, nil
	}
	// A reconnect can install the card and claim a timely Allow during the read.
	return s.ownsDetachedWaiting(entry), nil
}

func (s *Service) ownsDetachedWaiting(entry approvalJournalEntry) bool {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	p := s.fleet.interactions[entry.ID]
	return p != nil && p.prepared != nil && p.prepared.waiting && entry.matchesStored(p.ix)
}

// Startup gates delivery until its terminal siblings are durable. A later
// retry of a missing waiting card must leave their results deliverable.
func (s *Service) retryApprovalRecoveryLocked() {
	b := &s.approvals
	b.recovering = !b.recoveryDone
	s.scheduleApprovalRecoveryLocked()
}

func (s *Service) scheduleApprovalRecoveryLocked() {
	b := &s.approvals
	if !b.recoveryRetry {
		b.recoveryRetry = true
		ctx := s.runCtx()
		time.AfterFunc(time.Minute, func() {
			b.mu.Lock()
			b.recoveryRetry = false
			b.mu.Unlock()
			if ctx.Err() == nil {
				s.recoverApprovalJournals(ctx)
			}
		})
	}
}

// Startup and a reconnect can both start this batch. Give it its own manager
// deadline independent of the hook and of preceding scheduler recovery work.
func (s *Service) recoverManagerApprovalJournals() {
	ctx, cancel := context.WithTimeout(s.runCtx(), taskFailTimeout)
	defer cancel()
	s.recoverApprovalJournals(ctx)
}

// recoverApprovalJournals leaves complete unanswered detachable cards for
// interaction restore. An interrupted apply remains uncertain.
func (s *Service) recoverApprovalJournals(ctx context.Context) {
	b := &s.approvals
	b.mu.Lock()
	if b.recoverySeq != 0 && ctx.Err() != nil {
		b.mu.Unlock()
		return
	}
	if !s.loadApprovalJournalLocked(ctx) {
		s.retryApprovalRecoveryLocked()
		b.mu.Unlock()
		return
	}
	b.recoverySeq++
	seq := b.recoverySeq
	b.recovering = !b.recoveryDone
	var recovered []approvalJournalEntry
	changed := false
	var needSummary []string
	var candidates []approvalJournalEntry
	for i := range b.list {
		if a := b.list[i]; !a.live && a.unansweredDetached() {
			candidates = append(candidates, a)
		}
		if b.list[i].Result == "" && !b.list[i].live && b.list[i].Summary == "" {
			needSummary = append(needSummary, b.list[i].ID)
		}
	}
	b.mu.Unlock()

	backfilled := map[string]string{}
	for _, id := range needSummary {
		if summary := s.lookupInteractionSummary(ctx, id); summary != "" {
			backfilled[id] = summary
		}
	}

	waiting := make(map[approvalJournalEntry]bool, len(candidates))
	for _, a := range candidates {
		keep, err := s.retainDetachedWaiting(ctx, a)
		if err != nil {
			s.logf("read waiting detached approval %s: %v", a.ID, err)
			b.mu.Lock()
			if b.recoverySeq == seq {
				s.retryApprovalRecoveryLocked()
			}
			b.mu.Unlock()
			return
		}
		waiting[a] = keep
	}

	b.mu.Lock()
	if b.recoverySeq != seq {
		b.mu.Unlock()
		return
	}
	for i := range b.list {
		if waiting[b.list[i]] || (b.list[i].Result == "" && b.list[i].live) {
			// Restored continuations and approvals created during a startup
			// retry still belong to an active owner decision.
			continue
		}
		if b.handled == nil {
			b.handled = map[string]bool{}
		}
		b.handled[b.list[i].ID] = true
		if b.list[i].Result == "" {
			if b.list[i].Summary == "" {
				b.list[i].Summary = backfilled[b.list[i].ID]
			}
			if b.list[i].Status == "applying" {
				b.list[i].Status = "failed"
				b.list[i].Result = approvalResultTextWithSummary(b.list[i].ID, "failed", b.list[i].Summary, "The assistant restarted while the approved operation was executing. Its execution outcome is uncertain: check current state before retrying; do not assume it applied or that nothing changed.")
			} else {
				b.list[i].Status = "cancelled"
				b.list[i].Result = approvalResultTextWithSummary(b.list[i].ID, "cancelled", b.list[i].Summary, approvalRestartCancelledText)
			}
			if !b.list[i].CreatedAt.IsZero() || b.list[i].Fingerprint != "" {
				b.list[i].UpdatedAt = time.Now().UTC()
			}
			// A legacy entry has no occurrence fields. Its original UpdatedAt is
			// also its occurrence cutoff; leave it unchanged so this recovery result
			// cannot make a later same-ID card look like the old occurrence.
			changed = true
		}
		// Startup must finalize the whole batch before delivery. Once delivery
		// is open, its single worker owns terminal writes, including retries.
		if !b.recoveryDone {
			recovered = append(recovered, b.list[i])
		}
	}
	if changed {
		if err := s.saveApprovalJournalLocked(ctx); err != nil {
			s.logf("save restarted approval outcomes: %v", err)
		}
	}
	b.mu.Unlock()
	s.finalizeRecoveredApprovals(ctx, seq, recovered)
}

// finalizeRecoveredApprovals closes the whole recovered batch before releasing
// result delivery. A failed terminal interaction write keeps recovery active and
// reloads the batch on retry; no journal can be delivered and forgotten while its
// restored interaction is still pending. The sequence prevents an older retry
// from completing over a newer recovery pass.
func (s *Service) finalizeRecoveredApprovals(ctx context.Context, seq uint64, recovered []approvalJournalEntry) {
	b := &s.approvals
	b.mu.Lock()
	if b.recoverySeq != seq {
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	failed := false
	for _, a := range recovered {
		if err := s.finishRecoveredApproval(a); err != nil {
			failed = true
			s.logf("finish recovered approval %s: %v", a.ID, err)
			continue
		}
		current, found, err := s.currentApprovalJournalEntry(ctx, a)
		if err != nil {
			failed = true
			s.logf("reload recovered approval %s: %v", a.ID, err)
			continue
		}
		if found && current.Status != "approved" {
			// A continuation grant is valid only with its completed approved result.
			// Reload after finalization: a live owner Allow may have replaced the
			// restart-generated cancellation and must keep its one-time grant.
			if err := s.revokeDetachedGrant(ctx, current); err != nil {
				failed = true
				s.logf("revoke interrupted approval %s: %v", current.ID, err)
			}
		}
	}
	b.mu.Lock()
	if b.recoverySeq != seq {
		b.mu.Unlock()
		return
	}
	if failed {
		// Re-read the current batch when the timer fires. Another replay may
		// already have superseded this sequence while the write was failing.
		s.retryApprovalRecoveryLocked()
		b.mu.Unlock()
		return
	}
	b.recovering, b.recoveryDone = false, true
	var waiting []approvalJournalEntry
	for _, a := range b.list {
		if !a.live && a.unansweredDetached() {
			waiting = append(waiting, a)
		}
	}
	b.mu.Unlock()
	if len(waiting) != 0 {
		// The terminal siblings are durable. The fleet can now run its normal
		// startup sweep and install the waiting cards before accepting answers.
		s.restoreInteractions(ctx)
		for _, a := range waiting {
			if !s.ownsDetachedWaiting(a) {
				// Expiry or a transient restore failure may have left the journal
				// without a live timer. Reconcile it, keeping delivery available.
				b.mu.Lock()
				s.scheduleApprovalRecoveryLocked()
				b.mu.Unlock()
				break
			}
		}
	}
	s.deliverApprovalResults()
}

// recoveredApprovalOutcome closes a restored card consistently with its durable
// journal. An unfinished apply is uncertain, another unfinished entry is
// cancelled, and an already recorded outcome is retained without executing it.
func recoveredApprovalOutcome(a approvalJournalEntry) (historyStatus, journalStatus, text string, restartCancelled bool) {
	journalStatus, text = a.Status, a.Result
	if text == "" {
		if journalStatus == "applying" {
			journalStatus = "failed"
			text = "The assistant restarted while the approved operation was executing. Its execution outcome is uncertain: check current state before retrying; do not assume it applied or that nothing changed."
		} else {
			journalStatus = "cancelled"
			text = approvalRestartCancelledText
		}
	} else {
		// finishPrepared adds the standard result prefix when it refreshes the
		// journal. A terminal recovered entry already contains that prefix; strip
		// exactly this occurrence's own prefix so delivery reproduces it once.
		text = strings.TrimPrefix(text, approvalResultTextWithSummary(a.ID, journalStatus, a.Summary, ""))
	}
	switch journalStatus {
	case "approved", "resolved", "denied", "covered":
		// resolvePreparedAnswer uses endStatus for history: an owner Allow=false
		// and an Always-covered Allow are both "resolved" there.
		historyStatus = "resolved"
	case "failed":
		// Preserve the live uncertain-execution status instead of conflicting with
		// it by manufacturing "cancelled" during recovery.
		historyStatus = "failed"
	case "dismissed":
		historyStatus = "dismissed"
	case "expired":
		historyStatus = "expired"
	default:
		historyStatus = "cancelled"
	}
	// A retry of our own restart cancellation keeps Cleared even after the
	// journal has its terminal delivery text. Other retained outcomes keep the
	// attribution implied by their actual terminal status; a restart must not
	// relabel an earlier expiry or owner decision as a local clear.
	restartCancelled = journalStatus == "cancelled" && text == approvalRestartCancelledText
	return historyStatus, journalStatus, text, restartCancelled
}

func approvalJournalInteraction(entry approvalJournalEntry) proto.Interaction {
	return proto.Interaction{ID: entry.ID, MachineID: entry.MachineID, SessionID: entry.SessionID,
		Kind: proto.IxPermission, CreatedAt: entry.CreatedAt, Detachable: entry.Fingerprint != ""}
}

func approvalJournalMatches(entry approvalJournalEntry, ix proto.Interaction) bool {
	if entry.ID != ix.ID || entry.MachineID != ix.MachineID || entry.SessionID != ix.SessionID {
		return false
	}
	if entry.Fingerprint != "" {
		fingerprint, err := detachedFingerprintDigest(ix)
		return err == nil && fingerprint == entry.Fingerprint
	}
	if entry.CreatedAt.IsZero() {
		// A legacy journal has no explicit occurrence. Never match a card created
		// after that journal's last update; older unknown occurrences fail closed.
		return !ix.CreatedAt.After(entry.UpdatedAt)
	}
	return entry.CreatedAt.Equal(ix.CreatedAt)
}

// finishRecoveredApproval closes only the journal's occurrence. A different
// live occurrence is never claimed. With no matching live card, the fleet mutex
// stays held through the strict database write so restoreInteractions cannot
// validate a pending snapshot and then reinstall it after recovery closes it.
// A live owner claim wins over restart-generated delivery text; recovery then
// retries after that claim's honest outcome is durable instead of writing a
// competing cancellation or blocking the complete delivery batch forever.
func (s *Service) finishRecoveredApproval(entry approvalJournalEntry) error {
	current, found, err := s.currentApprovalJournalEntry(context.Background(), entry)
	if err != nil || !found {
		return err
	}
	if current.Finalized {
		return nil
	}
	entry = current
	id := entry.ID
	historyStatus, journalStatus, text, restartCancelled := recoveredApprovalOutcome(entry)
	r := proto.Resolution{Dismiss: true, Reason: "assistant_restarted"}
	if journalStatus == "denied" {
		allow := false
		r = proto.Resolution{Allow: &allow, Reason: "assistant_restarted"}
	}
	if historyStatus == "expired" {
		// Match live finishPrepared expiry semantics. Persisting the synthetic
		// restart dismissal here would replay an expired card as merely dismissed.
		r = proto.Resolution{Dismiss: true, Reason: proto.DismissedExpired}
	}
	by := endedByOf(historyStatus, &r)
	if restartCancelled {
		by = db.EndedByCleared
	}
	s.fleet.mu.Lock()
	p := s.fleet.interactions[id]
	if p != nil && !entry.matchesStored(p.ix) {
		p = nil
	}
	if p == nil {
		candidate := approvalJournalInteraction(entry)
		if recorder, ok := s.store().(interactionRecorder); ok {
			readCtx, cancel := context.WithTimeout(context.Background(), historyTimeout)
			rec, readErr := recorder.InteractionRecord(readCtx, entry.ID)
			cancel()
			if readErr != nil {
				s.fleet.mu.Unlock()
				return readErr
			}
			if rec != nil && entry.matchesStored(rec.Interaction) {
				// Use the complete stored call when it is the same occurrence. A
				// tombstone deliberately has no tool arguments and must not be
				// mistaken for a different fingerprint.
				candidate = rec.Interaction
				if rec.Status != "pending" {
					matches := rec.Status == historyStatus
					if rec.Status == "resolved" {
						matches = recoveredTerminalJournalStatus(rec.Interaction, rec.Status, rec.Resolution) == journalStatus
					}
					if !matches && !(journalStatus == "failed" && rec.Status == "cancelled") {
						entry, err = s.adoptRecoveredTerminal(context.Background(), entry, rec.Interaction, rec.Status, rec.Resolution)
						if err != nil {
							s.fleet.mu.Unlock()
							return err
						}
					}
					s.fleet.mu.Unlock()
					return s.approvalJournalFinalized(context.Background(), entry)
				}
			}
		}
		err := s.recordEndOccurrenceStrictBy(candidate, historyStatus, by, &r)
		s.fleet.mu.Unlock()
		if err != nil {
			return err
		}
		return s.approvalJournalFinalized(context.Background(), entry)
	}
	s.fleet.mu.Unlock()
	s.fleet.mu.Lock()
	if s.fleet.interactions[id] != p {
		s.fleet.mu.Unlock()
		return fmt.Errorf("interaction %s changed occurrence during recovery", id)
	}
	if p.claimed {
		s.fleet.mu.Unlock()
		return fmt.Errorf("interaction %s has an owner claim still finalizing", id)
	}
	if p.prepared != nil {
		s.fleet.mu.Unlock()
		if !s.endPreparedIfBy(p, historyStatus, text, nil, by) {
			return fmt.Errorf("interaction %s has an owner claim still finalizing", id)
		}
	} else {
		a := &preparedApproval{terminal: true, clarified: true, result: r,
			outcome: approvalops.Outcome{Text: text}, reportStatus: journalStatus}
		p.prepared, p.claimed, p.resolution = a, true, &r
		p.endedBy = by
		p.expired = historyStatus == "expired"
		if p.resolved == nil {
			p.resolved = make(chan struct{})
		}
		s.fleet.mu.Unlock()
		_ = s.finishPrepared(p, historyStatus)
	}
	if err := s.recordEndOccurrenceStrictBy(p.ix, historyStatus, by, &r); err != nil {
		return err
	}
	return s.approvalJournalFinalized(context.Background(), entry)
}
