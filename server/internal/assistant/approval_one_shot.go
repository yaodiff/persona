package assistant

import (
	"context"
	"errors"
	"fmt"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// waitOneShotPreparedCard preserves the pre-clarification contract for an
// operation without a live origin conversation. The original caller is the only
// terminal recipient: no clarification journal or conversation delivery starts.
func (s *Service) waitOneShotPreparedCard(ctx context.Context, ix proto.Interaction, op approvalops.Operation) (approvalops.Outcome, error) {
	r, err := s.ask(ctx, ix)
	if err != nil {
		return approvalops.Outcome{}, fmt.Errorf("%w: %w", approvalops.ErrNotApplied, err)
	}
	switch {
	case r.UserAllowed() && (!proto.InternalMaintenanceApproval(ix) || !r.Remembered && time.Now().Before(ix.ExpiresAt)) || proto.MaintenanceAllowed(ix, r, time.Now()):
		if err := ctx.Err(); err != nil {
			return approvalops.Outcome{Text: fmt.Sprintf("The change was approved after its caller stopped waiting: nothing changed (%v).", err)}, nil
		}
		text, err := op.Apply(ctx, r)
		switch {
		case errors.Is(err, approvalops.ErrStale):
			return approvalops.Outcome{Text: fmt.Sprintf("The setting or resource changed after this card was prepared. Nothing was applied; a fresh approval card is required. (%v)", err)}, nil
		case errors.Is(err, approvalops.ErrNotApplied):
			return approvalops.Outcome{Text: fmt.Sprintf("The approved change was not started: %v. Nothing was applied; correct the request or ask for a fresh approval before retrying.", err)}, nil
		case err != nil:
			return approvalops.Outcome{}, fmt.Errorf("the approved change's execution outcome is uncertain: %w; check the current state before retrying; do not assume it applied or that nothing changed", err)
		default:
			return approvalops.Outcome{Text: text}, nil
		}
	case r.UserDenied():
		text := op.Denied
		if text == "" {
			text = "The user declined the change: nothing changed."
		}
		return approvalops.Outcome{Text: text}, nil
	default:
		text := op.Unanswered
		if text == "" {
			text = prompts.RenderSettingUnanswered()
		}
		return approvalops.Outcome{Text: text}, nil
	}
}
