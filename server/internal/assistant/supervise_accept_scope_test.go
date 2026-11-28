package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestAcceptedReviewDeferredScopeRequiresLedger(t *testing.T) {
	const scope = "Decide whether to repair existing affected work."
	if err := deferredScopeLedgerError(false, scope); err == nil || !strings.Contains(err.Error(), "review ledger") {
		t.Fatalf("deferred scope without a ledger must be refused: %v", err)
	}
	if err := deferredScopeLedgerError(true, scope); err != nil {
		t.Fatalf("recordable deferred scope was refused: %v", err)
	}
	if err := deferredScopeLedgerError(false, ""); err != nil {
		t.Fatalf("ordinary legacy acceptance was refused: %v", err)
	}
}

// The one passing check covers the shipped fix. The review's outcome decision
// leaves another part of the original request for a separate owner decision.
func TestAcceptedReviewDeferredScope(t *testing.T) {
	const deferred = "Decide whether to repair existing affected work and adjust the paging rules."
	for _, route := range []string{"bare", "unlinked", "closed", "follow-up", "after-only", "user_check", "existing_decide"} {
		t.Run(route, func(t *testing.T) {
			ctx := context.Background()
			s, f := newQuietServiceOn(t, nil)
			f.judge = `{"verdict":"done","reason":"The code fix shipped"}`
			const criterion = "Investigate and ship the code fix"
			plan := []byte(`{"tasks":[{"title":"Resolve recurring paging alerts","prompt":"Find the cause, fix new work, repair affected work, and stop recurring pages.","machine":"box","cwd":"/code/app","readonly":true,"why":"The repository is on this machine","done_when":["` + criterion + `"]}]}`)
			if _, err := s.toolTaskPlan(ctx, "conv1", plan); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the agent link", func() bool { return len(onlyTodo(s).Agents) == 1 })
			todo := onlyTodo(s)
			agentTurn(s, f, proto.StatusWorking, "", 0)
			agentTurn(s, f, proto.StatusDone, "The fix shipped. New work succeeded.", 0)
			waitFor(t, "wait for review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
			fields := map[string]any{
				"verdict":        "accepted",
				"reason":         "存量这次没动，要处理再单独批",
				"checks":         []ReviewCheck{{Criterion: criterion, Met: true, Evidence: "New work completed successfully with the deployed fix."}},
				"deferred_scope": deferred,
			}
			switch route {
			case "unlinked", "closed", "follow-up", "after-only":
				parent := todo.ID
				var after []string
				if route == "unlinked" {
					parent = ""
				}
				if route == "after-only" {
					root := createRequestAndPiece(t, s, "conv1", "Resolve the alert incident", "", "", nil)
					parent, after = root.ID, []string{todo.ID}
					if _, err := s.editTodo(ctx, todo.ID, db.TodoEdit{PartOf: &parent}); err != nil {
						t.Fatal(err)
					}
				}
				follow := createRequestAndPiece(t, s, "conv1", deferred, "", parent, after)
				if route == "closed" {
					if closed, err := s.reviewDoneTodo(ctx, follow.ID, "No longer needed", false); err != nil || !closed {
						t.Fatalf("close follow-up: %v, %v", closed, err)
					}
				}
				fields["follow_up_todo_id"] = follow.ID
			case "user_check":
				fields["user_check"] = true
			case "existing_decide":
				decision := reviewRaw(t, todo.ID, map[string]any{
					"verdict": "needs_user", "why_owner": "irreversible",
					"reason": deferred, "recommend": "Approve",
					"options": []map[string]string{
						{"label": "Approve", "leads_to": "Start the repair and update the rules"},
						{"label": "Decline", "leads_to": "Leave the existing work and rules as they are"},
					},
					"cost_of_waiting": "Existing alerts may recur",
				})
				if _, err := s.runTool(ctx, "server", "conv1", "task_review", decision); err != nil {
					t.Fatal(err)
				}
			}
			before := todoNow(s, todo.ID)
			result, err := s.runTool(ctx, "server", "conv1", "task_review", reviewRaw(t, todo.ID, fields))
			cur := todoNow(s, todo.ID)
			ask, hasAsk := s.askCopy(todo.ID)
			switch route {
			case "bare", "unlinked", "closed", "user_check":
				if err == nil {
					t.Fatalf("accepted with deferred scope and no owner action: %s", result)
				}
				for _, want := range []string{"deferred scope", "needs_user", "linked follow-up to-do"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal omits %q: %v", want, err)
					}
				}
				if cur.DoneAt != nil || cur.Supervision.State != reviewPending || len(cur.Supervision.Reviews) != 0 || hasAsk {
					t.Fatalf("refusal changed the to-do or created an ask: %+v, %+v", cur.Supervision, ask)
				}
			case "follow-up", "after-only":
				if err != nil || cur.DoneAt == nil || cur.Supervision.State != reviewAccepted {
					t.Fatalf("linked follow-up did not allow acceptance: %s, %v", result, err)
				}
				review := triageLatestReview(cur.Supervision)
				if review.DeferredScope != deferred || review.FollowUpTodoID != fields["follow_up_todo_id"] {
					t.Fatalf("acceptance did not record the hand-off: %+v", review)
				}
			case "existing_decide":
				if err != nil || cur.Supervision.State != reviewNeedsUser || len(cur.Supervision.Reviews) != len(before.Supervision.Reviews) ||
					!hasAsk || !decisionOpen(&ask, cur) {
					t.Fatalf("acceptance displaced the existing decision: %s, %v, %+v", result, err, ask)
				}
			}
		})
	}
}
