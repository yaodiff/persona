package assistant

import (
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/assistant/todo/mergetrain"
	"github.com/nwverse/persona/server/internal/prompts"
)

// todoV2WakeDigestEntry revalidates the event at each delivery boundary. A failed store read leaves it retryable.
func (s *Service) todoV2WakeDigestEntry(ctx context.Context, g superviseSignal, web string) (prompts.SuperviseEntry, bool, bool) {
	t, ok := s.todoNow(g.Todo)
	if !ok || t.DoneAt != nil || t.Cancelled() || !s.settings(ctx).todosV2On() || g.Key == "" {
		return prompts.SuperviseEntry{}, false, false
	}
	wake := prompts.SuperviseTodoV2Wakeup{Kind: g.Kind}
	switch g.Kind {
	case sigBlockersUnblocked:
		unblocked, err := s.blockerUnblockStillHolds(ctx, g)
		if !unblocked {
			return prompts.SuperviseEntry{}, false, err != nil
		}
		wake.BlockerPRs = g.BlockerPRs
	case sigMergeTrainTurn:
		head, ok := s.mergeTrainHeadForWake(g)
		if !ok {
			return prompts.SuperviseEntry{}, false, false
		}
		wake.PR, wake.PreviousPR = head.PRURL, g.MergeTrainTurn.PreviousPRURL
		wake.PreviousClosed = g.MergeTrainTurn.PreviousState == "closed"
		wake.NeedsApproval = head.Policy == mergetrain.PolicyHumanReviewRequired && !head.Approved
	case sigWriteConflict:
		pair, peer, err := s.recheckWriteConflict(ctx, g)
		if peer.ID == "" {
			return prompts.SuperviseEntry{}, false, err != nil
		}
		wake.PeerID, wake.PeerTitle = peer.ID, peer.Title
		wake.Overlaps = slices.Concat(pair.StaticOverlaps, pair.RuntimeFiles)
	default:
		return prompts.SuperviseEntry{}, false, false
	}
	wake.Lang = string(s.lang(ctx))
	e := prompts.SuperviseEntry{ID: t.ID, Title: t.Title, Kind: g.Kind, Link: todoLink(web, t.ID), Quiet: t.Quiet,
		Detail: prompts.RenderSuperviseTodoV2Wakeup(wake)}
	if t.Supervision != nil {
		e.DoneWhen = t.Supervision.DoneWhen
	}
	s.setEntryOrigin(ctx, t, web, &e)
	return e, true, false
}
