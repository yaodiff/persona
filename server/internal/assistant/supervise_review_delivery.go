package assistant

import (
	"cmp"
	"context"
	"fmt"
	"strconv"
	"time"
)

// A supervisor's accepted closes a task, so with assistant.done_needs_delivery on ("a finished task closes only once
// live") it is held to what the judge's finish is held to (done.go): a task whose work is a GitHub PR is accepted only
// once the running hub contains the PR's merge (the deploy check, deploy_check.go), or, where the running hub cannot be
// checked against (another repository, a hub that does not say its commit), once the PR is merged. A check that fails
// refuses too: it never says live. "cannot" (nothing was delivered) and tasks with no PR are not held.

// acceptedDeliveryError refuses accepting task t while its PR (link, when it names one, else the task's) is not live;
// nil when nothing holds it.
func (s *Service) acceptedDeliveryError(ctx context.Context, t Todo, link, outcome string) error {
	if outcome == outcomeCannot || t.DoneAt != nil || !s.doneNeedsDelivery(ctx) {
		return nil
	}
	pr := t.PRURL
	if ciPRURL.MatchString(link) {
		pr = link
	}
	m := ciPRURL.FindStringSubmatch(pr)
	if m == nil {
		return nil
	}
	r := s.checkDeployed(ctx, pr)
	if r.State == deployUnverified {
		// Nothing to check it against: it is at least merged.
		number, _ := strconv.Atoi(m[2])
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		merged, _, err := s.forge().PRMerge(cctx, m[1], number)
		cancel()
		switch {
		case err != nil:
			r = deployResult{State: deployUnknown, Err: err}
		case !merged:
			r = deployResult{State: deployUnmerged}
		default:
			return nil
		}
	}
	if r.Err != nil {
		number, _ := strconv.Atoi(m[2])
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		isPR, err := s.forge().IsPR(cctx, m[1], number)
		cancel()
		if err == nil && !isPR {
			replacement, err := s.reportedTaskPR(ctx, t, pr)
			if err != nil {
				return err
			}
			if err := s.forgetTaskPR(ctx, t.ID, pr, replacement); err != nil {
				return err
			}
			current, ok := s.todoNow(t.ID)
			if !ok {
				return fmt.Errorf("task %s no longer exists", t.ID)
			}
			if current.PRURL == pr {
				return fmt.Errorf("task %s: its non-PR record changed during verification; retry the review", t.ID)
			}
			// The removed link may have hidden an earlier, real PR. Apply the same delivery gate to that one.
			return s.acceptedDeliveryError(ctx, current, "", outcome)
		}
	}
	if !r.held() {
		return nil
	}
	why := map[string]string{
		deployUnmerged: "the PR is not merged",
		deployOut:      "the PR is merged but the running hub does not contain it yet",
		deployNotIn:    "the running hub's deployed commits do not name the PR",
		deployUnknown:  "whether the PR is merged and live could not be checked",
	}[r.State]
	if r.Err != nil {
		why += " (" + r.Err.Error() + ")"
	}
	s.logf("task %s: accepted refused: %s: %s", t.ID, pr, cmp.Or(r.State, "held"))
	return fmt.Errorf("task %s is not accepted: %s is not live: %s, and the user's to-dos close only once their work is "+
		"live (done_needs_delivery). Keep it open: send what is missing to its agent (rework, or agent_send to merge and "+
		"deploy), park it with waiting_on external_event until the merge or deploy, or cancel it if the work is dropped. "+
		"If nothing could be delivered at all, accept it with outcome cannot", t.ID, pr, why)
}
