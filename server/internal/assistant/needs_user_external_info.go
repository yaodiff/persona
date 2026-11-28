package assistant

import (
	"cmp"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// Non-destructive supervision wakes during reviewNeedsUser (#13112).
//
// When external information linked to an open task arrives while the task is waiting on an owner decision
// (Supervision.State == reviewNeedsUser), engineering delivers the signal to the task's supervisor without
// mutating or dismissing the pending needs_user card upfront. Supervision evaluates in natural language whether
// the new facts resolve or supersede the pending ask:
//   - If still needed by the owner: supervision_finish(visibility="silent") leaves the original card untouched.
//   - If resolved or superseded: task_review transitions the task, records the natural-language supersession reason,
//     and automatically clears the stale needs_user card across clients (never fabricating an owner click).

const sigNeedsUserExternalInfo = "needs_user_external_info"

// needsUserWakeEligible reports whether t is an open supervised task currently waiting on an owner needs_user decision
// (not already returned to supervision for recheck/restate).
func needsUserWakeEligible(t Todo) bool {
	if !taskOpen(t) || t.Supervision == nil {
		return false
	}
	return t.Supervision.state(t) == reviewNeedsUser && !ownerDecisionReturned(t.Supervision)
}

// needsUserRoundKey returns a stable identifier for t's currently open needs_user round, truncated to microsecond
// precision so keys stay identical across Postgres round-trips and restarts.
func (s *Service) needsUserRoundKey(t Todo) string {
	if a, ok := s.askCopy(t.ID); ok && decideAsk(&a) && a.AcceptedAt.IsZero() && !a.AskedAt.IsZero() && !acceptAskStale(&a, t) {
		return a.AskedAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	}
	if r := triageLatestReview(t.Supervision); r != nil && r.Verdict == reviewNeedsUser && !r.At.IsZero() {
		return r.At.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	}
	if t.Supervision != nil && t.Supervision.Waits != nil && !t.Supervision.Waits.Work.IsZero() {
		return t.Supervision.Waits.Work.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	}
	if t.Supervision != nil && !t.Supervision.UpdatedAt.IsZero() {
		return t.Supervision.UpdatedAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
	}
	return "open"
}

// pendingNeedsUserSummary returns a concise read-only summary (question, recommendation) of t's open needs_user ask.
func (s *Service) pendingNeedsUserSummary(t Todo) (question, recommend string) {
	if a, ok := s.askCopy(t.ID); ok && decideAsk(&a) && a.AcceptedAt.IsZero() && !acceptAskStale(&a, t) {
		question = clip(oneLine(cmp.Or(a.Reason, a.Changed)), 240)
		recommend = clip(oneLine(a.Recommend), 120)
	}
	if question == "" {
		if r := triageLatestReview(t.Supervision); r != nil && r.Verdict == reviewNeedsUser {
			question = clip(oneLine(cmp.Or(r.Reason, r.Note)), 240)
			if recommend == "" {
				recommend = clip(oneLine(r.Recommend), 120)
			}
		}
	}
	if question == "" && t.Supervision != nil && t.Supervision.Waits != nil {
		question = clip(oneLine(t.Supervision.Waits.Reason), 240)
	}
	if question == "" {
		question = clip(oneLine(t.Title), 120)
	}
	return question, recommend
}

// emitNeedsUserExternalInfo emits an idempotent, non-destructive wake to task t's supervisor when external
// information arrives while t is in reviewNeedsUser. It never mutates the task's supervision state or ask card.
func (s *Service) emitNeedsUserExternalInfo(ctx context.Context, t Todo, source, summary, signalKey string, panes ...unlinkedPane) bool {
	if !needsUserWakeEligible(t) {
		return false
	}
	g := superviseSignal{
		Key:            strings.TrimSpace(signalKey),
		Unlinked:       panes,
		ExternalSource: clip(oneLine(strings.TrimSpace(source)), 160),
		Detail:         clip(oneLine(strings.TrimSpace(summary)), 500),
	}
	return s.enqueueNeedsUserExternalInfo(ctx, t, g, s.needsUserRoundKey(t))
}

func (s *Service) maybeWakeNeedsUserForPRFeedback(ctx context.Context, t Todo, ev prRoutedEvent, round string) bool {
	return s.enqueueNeedsUserExternalInfo(ctx, t, superviseSignal{
		Key:            ev.supervisorKey(),
		PRURL:          ev.PRURL,
		PREventID:      ev.EventID,
		Report:         ev.Kind,
		ExternalSource: ev.EventURL,
		Detail:         prEventNotice + "\nEvent: " + ev.Kind + "\n" + ev.feedbackContent(),
	}, round)
}

func (s *Service) enqueueNeedsUserExternalInfo(ctx context.Context, t Todo, g superviseSignal, round string) bool {
	if g.Detail == "" || g.Key == "" {
		return false
	}
	if s.agentSupervised(t) {
		// A coding task leader works without permission prompts and does not hold the owner's needs_user card;
		// route external info to the supervising conversation above it.
		up, _ := s.supervisorAboveTask(ctx, t)
		if up == "" {
			return false
		}
		g.To = up
	}
	g.Todo, g.Kind = t.ID, sigNeedsUserExternalInfo
	g.Key += "@round:" + round
	return s.signalReplacing(nil, g)
}

// needsUserExternalInfoStillHolds revalidates a queued external-info wake right before delivery: the task must still
// be open in the same pending needs_user round.
func (s *Service) needsUserExternalInfoStillHolds(t Todo, g superviseSignal) bool {
	if !needsUserWakeEligible(t) {
		return false
	}
	round := "@round:" + s.needsUserRoundKey(t)
	if g.PREventID == "" {
		return strings.HasSuffix(g.Key, round)
	}
	p := s.prFeedbackBinding(t, g.PRURL)
	if p == nil {
		return false
	}
	ev := prRoutedEvent{PRURL: p.URL, HeadSHA: p.HeadOID, Kind: g.Report, EventID: g.PREventID}
	return g.Key == ev.supervisorKey()+round
}

// needsUserExternalInfoEntry builds the localized prompt payload for a needs_user_external_info supervision entry.
func (s *Service) needsUserExternalInfoEntry(ctx context.Context, t Todo, source, summary string) *prompts.NeedsUserExternalInfo {
	question, recommend := s.pendingNeedsUserSummary(t)
	return &prompts.NeedsUserExternalInfo{
		Language:         answerLanguage(string(s.botLang(ctx))),
		Source:           clip(oneLine(strings.TrimSpace(source)), 160),
		Summary:          clip(oneLine(strings.TrimSpace(summary)), 500),
		PendingQuestion:  question,
		PendingRecommend: recommend,
	}
}

// hasOpenNeedsUserAsk reports whether t currently has an unanswered owner decision card.
func (s *Service) hasOpenNeedsUserAsk(t Todo) bool {
	a, ok := s.askCopy(t.ID)
	return ok && decideAsk(&a) && a.AcceptedAt.IsZero() && !acceptAskStale(&a, t)
}

// maybeWakeNeedsUserForPR checks newly observed PR status facts for an open task in reviewNeedsUser and emits
// idempotent wakes for meaningful external updates (merged, closed, approved, changes_requested, CI transitions,
// conflicts) without touching the pending needs_user card. Returns true when a merged PR was owned by an open
// needs_user ask card so applyPRStatus does not double-emit sigPRMerged for the same merge (#13100, #13112).
func (s *Service) maybeWakeNeedsUserForPR(ctx context.Context, t Todo, prev *TodoPRStatus, status TodoPRStatus) bool {
	if !needsUserWakeEligible(t) {
		return false
	}
	normURL := cmp.Or(triageNormalizePRURL(status.URL), strings.TrimSpace(status.URL))
	if normURL == "" {
		return false
	}
	headPart := cmp.Or(strings.TrimSpace(status.HeadOID), "nohead")

	switch status.State {
	case "merged":
		if !s.hasOpenNeedsUserAsk(t) {
			return false
		}
		if prev == nil || prev.State != "merged" {
			summary := fmt.Sprintf("PR %s was merged.", status.URL)
			s.emitNeedsUserExternalInfo(ctx, t, status.URL, summary, "pr:"+normURL+":merged")
		}
		return true
	case "closed":
		if prev == nil || prev.State != "closed" {
			summary := fmt.Sprintf("PR %s was closed without merging.", status.URL)
			s.emitNeedsUserExternalInfo(ctx, t, status.URL, summary, "pr:"+normURL+":closed")
		}
		return false
	}

	if status.State != "open" {
		return false
	}

	if status.Review == "approved" {
		reviewChanged := prev == nil ||
			prev.Review != "approved" ||
			(status.HeadOID != "" && prev.HeadOID != status.HeadOID) ||
			(status.submittedReview.ID > 0 && status.submittedReview.ID != prev.submittedReview.ID)
		if reviewChanged {
			key := "pr:" + normURL + ":approved@" + headPart
			if status.submittedReview.ID > 0 {
				key += fmt.Sprintf(":rev:%d", status.submittedReview.ID)
			}
			summary := fmt.Sprintf("PR %s was approved (checks: %s).", status.URL, cmp.Or(status.Checks, "none"))
			s.emitNeedsUserExternalInfo(ctx, t, status.URL, summary, key)
		}
	}

	if status.Review == "changes_requested" {
		reviewChanged := prev == nil ||
			prev.Review != "changes_requested" ||
			(status.HeadOID != "" && prev.HeadOID != status.HeadOID) ||
			(status.ChangesRequestedAt != nil && (prev.ChangesRequestedAt == nil || !status.ChangesRequestedAt.Equal(*prev.ChangesRequestedAt))) ||
			(status.actionableReview.ID > 0 && status.actionableReview.ID != prev.actionableReview.ID)
		if reviewChanged {
			key := "pr:" + normURL + ":changes_requested@" + headPart
			switch {
			case status.actionableReview.ID > 0:
				key += fmt.Sprintf(":rev:%d", status.actionableReview.ID)
			case status.ChangesRequestedAt != nil:
				key += ":" + status.ChangesRequestedAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
			}
			summary := fmt.Sprintf("PR %s has changes requested", status.URL)
			if status.actionableReview.Author != "" {
				summary += " by " + status.actionableReview.Author
			}
			if status.actionableReview.Body != "" {
				summary += ": " + clip(oneLine(status.actionableReview.Body), 240)
			} else {
				summary += "."
			}
			s.emitNeedsUserExternalInfo(ctx, t, status.URL, summary, key)
		}
	}

	if status.Conflicts && status.HeadOID != "" && (prev == nil || !prev.Conflicts || prev.HeadOID != status.HeadOID) {
		summary := fmt.Sprintf("PR %s at head %s now has merge conflicts.", status.URL, shortSHA(status.HeadOID))
		s.emitNeedsUserExternalInfo(ctx, t, status.URL, summary, "pr:"+normURL+":conflict@"+status.HeadOID)
	}

	if prev != nil && status.Checks != prev.Checks && (status.Checks == "success" || status.Checks == "failure") {
		summary := fmt.Sprintf("PR %s CI checks changed from %s to %s.", status.URL, cmp.Or(prev.Checks, "none"), status.Checks)
		s.emitNeedsUserExternalInfo(ctx, t, status.URL, summary, "pr:"+normURL+":checks:"+status.Checks+"@"+headPart)
	}
	return false
}

// formatInboxExternalInfo builds a concise (source, summary) pair for an Inbox item waking a needs_user task.
func formatInboxExternalInfo(in prompts.SuperviseInbox) (source, summary string) {
	source = in.Item
	if in.Title != "" {
		source = fmt.Sprintf("%s (%s)", in.Title, in.Item)
	}
	var parts []string
	if in.Sender != "" {
		if in.Count > 1 {
			parts = append(parts, fmt.Sprintf("%s (%d new messages)", in.Sender, in.Count))
		} else {
			parts = append(parts, in.Sender)
		}
	} else if in.Count > 1 {
		parts = append(parts, fmt.Sprintf("%d new messages", in.Count))
	}
	if in.Preview != "" {
		parts = append(parts, in.Preview)
	}
	if in.URL != "" {
		parts = append(parts, in.URL)
	}
	summary = strings.Join(parts, ": ")
	if summary == "" {
		summary = cmp.Or(in.Title, in.Item)
	}
	return source, summary
}

// needsUserAgentTurn wakes t's supervisor when one of t's agents ended a turn while t still waits on an open owner
// decision (#14899): the agent may have gone on (the supervisor sent it more, or it went on by itself) and found what
// answers or replaces the question. The turn that raised the decision is no news, nor is one its review came after.
// Nothing is changed on the task or its card; the supervisor decides as for any external information (#13112).
func (s *Service) needsUserAgentTurn(ctx context.Context, t Todo, c taskDoneCheck, err error) {
	e, ok := s.turnOf(ctx, t.ID)
	if !ok || e.Session == "" || e.At.IsZero() {
		return
	}
	if r := triageLatestReview(t.Supervision); r != nil && r.Verdict == reviewNeedsUser && !e.At.After(r.At) {
		return
	}
	summary := c.Reason
	if err != nil && summary == "" {
		summary = fmt.Sprintf("agent %s/%s ended a turn; it could not be judged: %s", e.Machine, short(e.Session), clip(err.Error(), 120))
	}
	if summary == "" {
		summary = fmt.Sprintf("agent %s/%s ended a turn (%s)", e.Machine, short(e.Session), cmp.Or(c.Verdict, "unjudged"))
	}
	source := fmt.Sprintf("agent turn %s/%s", e.Machine, short(e.Session))
	key := fmt.Sprintf("agent_turn:%s/%s@%s", e.Machine, e.Session, e.At.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano))
	if len(c.unlinked) > 0 {
		source = "agent turn"
	}
	if s.emitNeedsUserExternalInfo(ctx, t, source, summary, key, c.unlinked...) {
		s.logf("task %s: agent turn while the owner decides: supervisor told", t.ID)
	}
}
