package assistant

import (
	"context"
	"errors"
	"fmt"
)

var errOwnerResultNeedsReview = errors.New("bare accepted refused: the owner needs this task's result; use accepted with user_check: true to show the result, or needs_user if the owner has a decision to make")

// A read-only launch is also used for internal work. Require a recorded owner request or a group Inbox source
// before holding the result for the owner. The review's own note is not evidence that the owner saw it.
func (s *Service) checkOwnerResultBeforeAccept(ctx context.Context, t Todo) error {
	// A selected option in the current review is an owner action. An expired, superseded or previous
	// decision does not establish that the owner settled the current result.
	if r := triageLatestReview(t.Supervision); r != nil && r.Verdict == reviewNeedsUser {
		if a, ok := s.askCopy(t.ID); ok && decideAsk(&a) && a.OutsideID == "" && !a.ByDefault &&
			!a.Superseded && !a.AcceptedAt.IsZero() && !a.AskedAt.Before(r.At) && !acceptAskStale(&a, t) &&
			(a.OptionTaken || a.Chosen == "" && a.Response == "" && a.AgentReply == nil) {
			return nil
		}
	}
	if t.PlanID != "" {
		p, i, err := s.currentPlanAssignment(ctx, t)
		if err != nil && !errors.Is(err, errTaskNotInPlan) {
			return fmt.Errorf("read task assignment before accepting: %w", err)
		}
		if err == nil && p.Tasks[i].Readonly && p.Tasks[i].UserRequested {
			return errOwnerResultNeedsReview
		}
	}
	group, err := s.inboxItemIsGroup(ctx, t.InboxItem)
	if err != nil {
		return err
	}
	if group {
		return errOwnerResultNeedsReview
	}
	return nil
}
