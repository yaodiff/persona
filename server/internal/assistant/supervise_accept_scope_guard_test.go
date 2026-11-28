package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// multiScopeReviewTask creates a supervised task with a custom prompt (notes), done_when criteria, and optional
// out_of_bounds, then drives its agent through one working->done turn so it sits in pending_review.
func multiScopeReviewTask(t *testing.T, title, prompt string, doneWhen, outOfBounds []string, lastText string) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f := newQuietServiceOn(t, nil)
	f.judge = `{"verdict":"done","reason":"done"}`
	taskSpec := map[string]any{
		"title":          title,
		"prompt":         prompt,
		"machine":        "box",
		"cwd":            "/code/app",
		"readonly":       true,
		"why":            "codebase is on box",
		"done_when":      doneWhen,
		"expect_minutes": 30,
	}
	if len(outOfBounds) > 0 {
		taskSpec["out_of_bounds"] = outOfBounds
	}
	raw, err := json.Marshal(map[string]any{"tasks": []map[string]any{taskSpec}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.toolTaskPlan(context.Background(), "conv1", raw); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "agent linked", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, lastText, 0)
	waitFor(t, "pending review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	return s, f, todoNow(s, todo.ID)
}

const multiScopeEnglishBrief = `Handle the webhook incident across three scopes:

Scope:
1. Ship code fix for webhook retry storm
2. Production data cleanup for stale duplicate records
3. Alarm rule changes for retry thresholds`

const multiScopeChineseBrief = `本次处理三个范围：
1. 修复 webhook 重试风暴代码
2. 清理线上脏数据
3. 调整告警规则阈值`

// #13169 core regression: a reworded handoff deliberately avoids every wording-guard regex while leaving two scopes of
// a multi-scope brief uncovered without a card or follow-up to-do. The structural check must refuse bare accepted,
// name the uncovered scopes and all three exits, and write nothing.
func TestAcceptScopeGuardRefusesRewordedHandoffWithoutWordingMatch(t *testing.T) {
	doneWhen := []string{"Code fix for webhook retry storm shipped and verified"}
	s, _, todo := multiScopeReviewTask(
		t,
		"Webhook incident follow-through",
		multiScopeEnglishBrief,
		doneWhen,
		nil,
		"Merged PR #901 fixing the webhook retry storm; unit and integration tests are green.",
	)
	ctx := context.Background()

	checks := []ReviewCheck{{
		Criterion: doneWhen[0],
		Met:       true,
		Evidence:  "PR #901 merged and unit tests green",
	}}
	note := "Code fix shipped and verified"
	reason := "Code fix is complete and verified; production-data cleanup and alarm-rule changes were not touched this round, tell me the scope if you want them handled"

	// Prove this payload completely bypasses the wording guard (#12026); removing the structural guard breaks this test.
	evidence := s.acceptGuardEvidence(ctx, todo, note, reason, checks)
	if matched, _ := acceptGuardBlock(evidence); matched != "" {
		t.Fatalf("expected reworded handoff to avoid wording guard, matched %q", matched)
	}

	raw := reviewRaw(t, todo.ID, map[string]any{
		"verdict": "accepted",
		"note":    note,
		"reason":  reason,
		"checks":  checks,
	})
	_, err := s.runTool(ctx, "server", "conv1", "task_review", raw)
	if err == nil {
		t.Fatal("expected bare accepted to be refused for uncovered multi-scope brief")
	}
	msg := err.Error()
	for _, want := range []string{
		"bare accepted refused",
		"Production data cleanup for stale duplicate records",
		"Alarm rule changes for retry thresholds",
		"needs_user",
		"user_check",
		"follow-up to-do",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal error missing %q:\n%s", want, msg)
		}
	}
	cur := todoNow(s, todo.ID)
	if cur.DoneAt != nil || cur.Supervision.State != reviewPending || len(cur.Supervision.Reviews) != 0 {
		t.Fatalf("refused review mutated task state: done=%v sup=%+v", cur.DoneAt, cur.Supervision)
	}
}

// Silent omission (the review never mentions the other scopes at all) and Chinese multi-scope reworded handoffs are
// also refused cleanly.
func TestAcceptScopeGuardRefusesSilentOmissionAndChineseHandoff(t *testing.T) {
	ctx := context.Background()

	t.Run("silent omission", func(t *testing.T) {
		doneWhen := []string{"Code fix for webhook retry storm shipped"}
		s, _, todo := multiScopeReviewTask(t, "Webhook scopes", multiScopeEnglishBrief, doneWhen, nil, "PR #901 merged.")
		raw := reviewRaw(t, todo.ID, map[string]any{
			"verdict": "accepted",
			"note":    "PR #901 merged",
			"checks":  []ReviewCheck{{Criterion: doneWhen[0], Met: true, Evidence: "PR #901 merged"}},
		})
		_, err := s.runTool(ctx, "server", "conv1", "task_review", raw)
		if err == nil || !strings.Contains(err.Error(), "Production data cleanup") || !strings.Contains(err.Error(), "Alarm rule changes") {
			t.Fatalf("expected silent omission refusal naming uncovered scopes, got: %v", err)
		}
	})

	t.Run("chinese reworded handoff", func(t *testing.T) {
		doneWhen := []string{"webhook 重试风暴代码修复已合入"}
		s, _, todo := multiScopeReviewTask(t, "处理 webhook 故障", multiScopeChineseBrief, doneWhen, nil, "PR #902 已合入，单测全过。")
		checks := []ReviewCheck{{Criterion: doneWhen[0], Met: true, Evidence: "PR #902 已合入"}}
		reason := "代码修复已合入并验证；线上脏数据清理和告警规则阈值本轮未动，如需处理请告诉我范围"
		if matched, _ := acceptGuardBlock(s.acceptGuardEvidence(ctx, todo, "代码修复完成", reason, checks)); matched != "" {
			t.Fatalf("expected Chinese handoff to avoid wording guard, matched %q", matched)
		}
		raw := reviewRaw(t, todo.ID, map[string]any{
			"verdict": "accepted",
			"note":    "代码修复完成",
			"reason":  reason,
			"checks":  checks,
		})
		_, err := s.runTool(ctx, "server", "conv1", "task_review", raw)
		if err == nil || !strings.Contains(err.Error(), "清理线上脏数据") || !strings.Contains(err.Error(), "调整告警规则阈值") {
			t.Fatalf("expected Chinese multi-scope refusal, got: %v", err)
		}
	})

	t.Run("inline numbered brief", func(t *testing.T) {
		doneWhen := []string{"Code fix shipped"}
		inlineBrief := "Deliver: 1. Ship code fix for webhook retry; 2. Run production data cleanup; 3. Update alarm rule thresholds."
		s, _, todo := multiScopeReviewTask(t, "Inline multi-scope", inlineBrief, doneWhen, nil, "PR #903 merged.")
		raw := reviewRaw(t, todo.ID, map[string]any{
			"verdict": "accepted",
			"note":    "Code fix merged",
			"checks":  []ReviewCheck{{Criterion: doneWhen[0], Met: true, Evidence: "PR #903 merged fixing webhook retry"}},
		})
		_, err := s.runTool(ctx, "server", "conv1", "task_review", raw)
		if err == nil || !strings.Contains(err.Error(), "Run production data cleanup") || !strings.Contains(err.Error(), "Update alarm rule thresholds") {
			t.Fatalf("expected inline multi-scope refusal, got: %v", err)
		}
	})
}

// All three valid exits succeed for a multi-scope brief with leftover work:
// 1. needs_user keeps the task open with an owner decision ask;
// 2. accepted + user_check keeps the task open with an owner verification ask;
// 3. a linked follow-up to-do (or a linked piece carrying an open needs_user ask) allows bare accepted to close it.
func TestAcceptScopeGuardAllowsValidExits(t *testing.T) {
	ctx := context.Background()
	doneWhen := []string{"Code fix for webhook retry storm shipped and verified"}
	reason := "Code fix is complete and verified; production-data cleanup and alarm-rule changes were not touched this round, tell me the scope if you want them handled"
	checks := []ReviewCheck{{Criterion: doneWhen[0], Met: true, Evidence: "PR #901 merged and verified"}}

	t.Run("needs_user exit", func(t *testing.T) {
		s, _, todo := multiScopeReviewTask(t, "Webhook incident", multiScopeEnglishBrief, doneWhen, nil, "PR #901 merged.")
		raw := reviewRaw(t, todo.ID, map[string]any{
			"verdict":   "needs_user",
			"why_owner": "priority",
			"reason":    "Code fix is merged. Should we proceed with production data cleanup and alarm rule changes?",
			"recommend": "Proceed with both",
			"options": []map[string]string{
				{"label": "Proceed with both", "leads_to": "Run data cleanup and update alarm rules"},
				{"label": "Close after code fix", "leads_to": "Skip cleanup and alarm updates"},
			},
			"cost_of_waiting": "Stale records and noisy alarms remain until decided",
		})
		res, err := s.runTool(ctx, "server", "conv1", "task_review", raw)
		if err != nil {
			t.Fatalf("needs_user refused: %v", err)
		}
		if !strings.Contains(res, "waits for the user") {
			t.Fatalf("unexpected needs_user result: %s", res)
		}
		if cur := todoNow(s, todo.ID); cur.DoneAt != nil || cur.Supervision.State != reviewNeedsUser {
			t.Fatalf("needs_user did not keep task open: done=%v sup=%+v", cur.DoneAt, cur.Supervision)
		}
	})

	t.Run("user_check exit", func(t *testing.T) {
		s, _, todo := multiScopeReviewTask(t, "Webhook incident", multiScopeEnglishBrief, doneWhen, nil, "PR #901 merged.")
		raw := reviewRaw(t, todo.ID, map[string]any{
			"verdict":    "accepted",
			"user_check": true,
			"note":       "Code fix shipped",
			"reason":     reason,
			"checks":     checks,
		})
		res, err := s.runTool(ctx, "server", "conv1", "task_review", raw)
		if err != nil {
			t.Fatalf("accepted with user_check refused: %v", err)
		}
		if !strings.Contains(res, "user_check") {
			t.Fatalf("unexpected user_check result: %s", res)
		}
		if cur := todoNow(s, todo.ID); cur.DoneAt != nil || cur.Supervision.State != reviewUserCheck {
			t.Fatalf("user_check did not keep task open: done=%v sup=%+v", cur.DoneAt, cur.Supervision)
		}
		if ask, ok := s.askCopy(todo.ID); !ok || !ask.AcceptedAt.IsZero() {
			t.Fatalf("expected open owner ask after user_check, ok=%v ask=%+v", ok, ask)
		}
	})

	t.Run("linked follow-up todo covers leftover scopes", func(t *testing.T) {
		s, _, todo := multiScopeReviewTask(t, "Webhook incident", multiScopeEnglishBrief, doneWhen, nil, "PR #901 merged.")

		// A follow-up covering only ONE leftover scope still refuses the remaining uncovered scope.
		createPartial, _ := json.Marshal(map[string]any{
			"title":   "Production data cleanup for stale duplicate records",
			"part_of": todo.ID,
		})
		if _, err := s.runTool(ctx, "server", "conv1", "todo_create", createPartial); err != nil {
			t.Fatalf("create partial follow-up: %v", err)
		}
		acceptRaw := reviewRaw(t, todo.ID, map[string]any{
			"verdict": "accepted",
			"note":    "Code fix shipped",
			"reason":  reason,
			"checks":  checks,
		})
		_, err := s.runTool(ctx, "server", "conv1", "task_review", acceptRaw)
		if err == nil || !strings.Contains(err.Error(), "Alarm rule changes") || strings.Contains(err.Error(), "Production data cleanup") {
			t.Fatalf("expected refusal naming only alarm rule changes, got: %v", err)
		}

		// Adding a second linked follow-up for the alarm rules allows bare accepted to succeed.
		createAlarm, _ := json.Marshal(map[string]any{
			"title":   "Alarm rule changes for retry thresholds",
			"part_of": todo.ID,
		})
		if _, err := s.runTool(ctx, "server", "conv1", "todo_create", createAlarm); err != nil {
			t.Fatalf("create alarm follow-up: %v", err)
		}
		res, err := s.runTool(ctx, "server", "conv1", "task_review", acceptRaw)
		if err != nil {
			t.Fatalf("bare accept with linked follow-ups refused: %v", err)
		}
		if !strings.Contains(res, "accepted and closed") {
			t.Fatalf("unexpected accept result: %s", res)
		}
		if cur := todoNow(s, todo.ID); cur.DoneAt == nil || cur.Supervision.State != reviewAccepted {
			t.Fatalf("expected task accepted and closed: done=%v sup=%+v", cur.DoneAt, cur.Supervision)
		}
	})

	t.Run("linked piece with open needs_user ask covers leftover scopes", func(t *testing.T) {
		s, _, todo := multiScopeReviewTask(t, "Webhook incident", multiScopeEnglishBrief, doneWhen, nil, "PR #901 merged.")

		// A backing child piece with an open owner decision ask (the shape outside_decisions and child needs_user use).
		child := Todo{
			ID:        "todo_child_ask",
			Title:     "Owner confirmation for remaining webhook follow-ups",
			PartOf:    todo.ID,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		s.todos.mu.Lock()
		s.todos.todos[child.ID] = &child
		s.todos.mu.Unlock()
		s.accept.mu.Lock()
		s.accept.loaded = true
		if s.accept.asks == nil {
			s.accept.asks = map[string]*acceptAsk{}
		}
		s.accept.asks[child.ID] = &acceptAsk{
			Conclusion: true,
			Outcome:    outcomeDecide,
			Reason:     "Confirm whether to run production data cleanup for stale duplicate records and update alarm rule thresholds",
			Recommend:  "Run both",
			AskedAt:    time.Now().UTC(),
		}
		s.accept.mu.Unlock()

		acceptRaw := reviewRaw(t, todo.ID, map[string]any{
			"verdict": "accepted",
			"note":    "Code fix shipped",
			"reason":  reason,
			"checks":  checks,
		})
		res, err := s.runTool(ctx, "server", "conv1", "task_review", acceptRaw)
		if err != nil {
			t.Fatalf("bare accept with linked open needs_user ask refused: %v", err)
		}
		if !strings.Contains(res, "accepted and closed") {
			t.Fatalf("unexpected accept result: %s", res)
		}
	})

	t.Run("cancelled follow-up does not satisfy uncovered scope", func(t *testing.T) {
		s, _, todo := multiScopeReviewTask(t, "Webhook incident", multiScopeEnglishBrief, doneWhen, nil, "PR #901 merged.")
		now := time.Now().UTC()
		s.todos.mu.Lock()
		s.todos.todos["todo_cancelled_child"] = &Todo{
			ID:          "todo_cancelled_child",
			Title:       "Production data cleanup and alarm rule changes",
			PartOf:      todo.ID,
			DoneAt:      &now,
			DoneBy:      db.DoneByCancelled,
			CancelledAt: &now,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		s.todos.mu.Unlock()
		acceptRaw := reviewRaw(t, todo.ID, map[string]any{
			"verdict": "accepted",
			"note":    "Code fix shipped",
			"checks":  checks,
		})
		if _, err := s.runTool(ctx, "server", "conv1", "task_review", acceptRaw); err == nil || !strings.Contains(err.Error(), "bare accepted refused") {
			t.Fatalf("cancelled follow-up should not cover remaining scope, got: %v", err)
		}
	})
}

// Single-scope tasks, full-coverage multi-scope accepts, dropped scopes via task_scope, and unstructured briefs all
// succeed with no false positives.
func TestAcceptScopeGuardNoFalsePositives(t *testing.T) {
	ctx := context.Background()

	t.Run("full coverage multi-scope accept succeeds", func(t *testing.T) {
		doneWhen := []string{
			"Code fix for webhook retry storm merged",
			"Production data cleanup completed",
			"Alarm rule thresholds updated",
		}
		s, _, todo := multiScopeReviewTask(t, "Webhook incident", multiScopeEnglishBrief, doneWhen, nil, "All three scopes finished.")
		raw := reviewRaw(t, todo.ID, map[string]any{
			"verdict": "accepted",
			"note":    "All three scopes completed and verified",
			"checks": []ReviewCheck{
				{Criterion: doneWhen[0], Met: true, Evidence: "PR #901 merged"},
				{Criterion: doneWhen[1], Met: true, Evidence: "Cleanup script removed 42 stale duplicate records"},
				{Criterion: doneWhen[2], Met: true, Evidence: "Alarm rules updated in Terraform PR #902"},
			},
		})
		res, err := s.runTool(ctx, "server", "conv1", "task_review", raw)
		if err != nil {
			t.Fatalf("full-coverage multi-scope accept refused: %v", err)
		}
		if !strings.Contains(res, "accepted and closed") {
			t.Fatalf("unexpected result: %s", res)
		}
	})

	t.Run("single criterion whose evidence covers all brief scopes succeeds", func(t *testing.T) {
		doneWhen := []string{"All incident items completed"}
		s, _, todo := multiScopeReviewTask(t, "Webhook incident", multiScopeEnglishBrief, doneWhen, nil, "All done.")
		raw := reviewRaw(t, todo.ID, map[string]any{
			"verdict": "accepted",
			"note":    "Done",
			"checks": []ReviewCheck{{
				Criterion: doneWhen[0],
				Met:       true,
				Evidence:  "PR #901 shipped webhook retry code fix; cleaned up stale duplicate production data records; updated alarm rule thresholds",
			}},
		})
		if _, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err != nil {
			t.Fatalf("umbrella check covering all scopes refused: %v", err)
		}
	})

	t.Run("single-scope task succeeds", func(t *testing.T) {
		doneWhen := []string{"Fix merged"}
		s, _, todo := multiScopeReviewTask(t, "Single scope", "Scope:\n1. Fix login crash on iPad landscape", doneWhen, nil, "PR #901 merged.")
		raw := reviewRaw(t, todo.ID, map[string]any{
			"verdict": "accepted",
			"note":    "PR #901 merged",
			"checks":  []ReviewCheck{{Criterion: doneWhen[0], Met: true, Evidence: "PR #901 merged"}},
		})
		if _, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err != nil {
			t.Fatalf("single-scope accept refused: %v", err)
		}
	})

	t.Run("unstructured brief fails open while wording guard still runs", func(t *testing.T) {
		doneWhen := []string{"PR merged"}
		proseBrief := "Investigate the retry storm in webhook handler, patch the issue, and double check metrics afterwards."
		s, _, todo := multiScopeReviewTask(t, "Prose task", proseBrief, doneWhen, nil, "PR #901 merged.")

		// Wording guard still catches inconclusive phrasing on an unstructured brief.
		badRaw := reviewRaw(t, todo.ID, map[string]any{
			"verdict": "accepted",
			"note":    "Results are inconclusive and partially done",
			"checks":  []ReviewCheck{{Criterion: doneWhen[0], Met: true, Evidence: "PR #901 merged"}},
		})
		if _, err := s.runTool(ctx, "server", "conv1", "task_review", badRaw); err == nil || !strings.Contains(err.Error(), "bare accepted refused") {
			t.Fatalf("wording guard should still refuse inconclusive accept on unstructured brief, got: %v", err)
		}

		// Clean review fails open on unstructured brief.
		okRaw := reviewRaw(t, todo.ID, map[string]any{
			"verdict": "accepted",
			"note":    "PR #901 merged",
			"checks":  []ReviewCheck{{Criterion: doneWhen[0], Met: true, Evidence: "PR #901 merged"}},
		})
		if _, err := s.runTool(ctx, "server", "conv1", "task_review", okRaw); err != nil {
			t.Fatalf("unstructured brief should fail open on clean accept, got: %v", err)
		}
	})

	t.Run("bullets under ignored headings and metadata lines do not trip guard", func(t *testing.T) {
		doneWhen := []string{"Fix merged"}
		briefWithContext := "Fix login crash on iPad landscape.\n\n" +
			"Metadata:\n- Repo: nwverse/persona\n- Branch: main\n\n" +
			"Constraints:\n- Do not change the API schema\n- Keep backward compatibility\n\n" +
			"Out of bounds:\n- Production data cleanup\n- Alarm rule changes\n\n" +
			"Steps to reproduce:\n1. Open app in landscape\n2. Tap Sign In\n3. Observe crash"
		s, _, todo := multiScopeReviewTask(t, "Context bullets", briefWithContext, doneWhen, nil, "PR #901 merged.")
		raw := reviewRaw(t, todo.ID, map[string]any{
			"verdict": "accepted",
			"note":    "PR #901 merged",
			"checks":  []ReviewCheck{{Criterion: doneWhen[0], Met: true, Evidence: "PR #901 merged"}},
		})
		if _, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err != nil {
			t.Fatalf("non-scope bullets tripped guard: %v", err)
		}
	})

	t.Run("scope dropped via task_scope is not required", func(t *testing.T) {
		doneWhen := []string{
			"Ship code fix for webhook retry storm",
			"Alarm rule changes for retry thresholds",
		}
		brief := "Scope:\n1. Ship code fix for webhook retry storm\n2. Alarm rule changes for retry thresholds"
		s, _, todo := multiScopeReviewTask(t, "Scoped down", brief, doneWhen, nil, "PR #901 merged.")

		// Drop the second criterion through task_scope.
		scopeRaw, _ := json.Marshal(map[string]any{
			"todo_id":   todo.ID,
			"change":    "Narrowed to code fix only",
			"why":       "Owner deferred alarm rules",
			"done_when": []string{doneWhen[0]},
		})
		if _, err := s.runTool(ctx, "server", "conv1", "task_scope", scopeRaw); err != nil {
			t.Fatalf("task_scope failed: %v", err)
		}
		raw := reviewRaw(t, todo.ID, map[string]any{
			"verdict": "accepted",
			"note":    "Code fix merged",
			"checks":  []ReviewCheck{{Criterion: doneWhen[0], Met: true, Evidence: "PR #901 merged"}},
		})
		if _, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err != nil {
			t.Fatalf("dropped scope should not block accept: %v", err)
		}
	})
}

// Explicit out-of-scope list recorded while closing refuses bare accepted even when the brief itself was prose,
// and succeeds once a linked follow-up to-do carries the deferred items.
func TestAcceptScopeGuardRefusesClosingDeferredList(t *testing.T) {
	ctx := context.Background()
	doneWhen := []string{"Code fix shipped"}
	s, _, todo := multiScopeReviewTask(t, "Webhook incident", "Handle the webhook retry incident.", doneWhen, nil, "PR #901 merged.")

	reason := "Code fix shipped in PR #901.\nNot touched:\n- Production data cleanup\n- Alarm rule threshold updates"
	raw := reviewRaw(t, todo.ID, map[string]any{
		"verdict": "accepted",
		"note":    "Code fix shipped",
		"reason":  reason,
		"checks":  []ReviewCheck{{Criterion: doneWhen[0], Met: true, Evidence: "PR #901 merged"}},
	})
	_, err := s.runTool(ctx, "server", "conv1", "task_review", raw)
	if err == nil || !strings.Contains(err.Error(), "Production data cleanup") || !strings.Contains(err.Error(), "Alarm rule threshold updates") {
		t.Fatalf("expected refusal naming deferred closing list, got: %v", err)
	}

	createFollowup, _ := json.Marshal(map[string]any{
		"title":   "Production data cleanup and alarm rule threshold updates",
		"part_of": todo.ID,
	})
	if _, err := s.runTool(ctx, "server", "conv1", "todo_create", createFollowup); err != nil {
		t.Fatalf("todo_create: %v", err)
	}
	if _, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err != nil {
		t.Fatalf("expected accept to succeed once linked follow-up covers deferred list, got: %v", err)
	}
}

type scopeOutcomeStore struct {
	*phaseOutcomeStore
}

func (s *scopeOutcomeStore) InsertTodoDecision(context.Context, db.TodoDecisionRow) (bool, error) {
	return true, nil
}

// TaskPlansMadeSince lists the plans this store holds. A review that accepts reads the task's current plan assignment
// first (checkOwnerResultBeforeAccept, #15566), and a store swapped in under a planned task has to answer that read
// as the database does: the task's plan stayed with the store it was made in, so the task is in no plan here.
func (s *scopeOutcomeStore) TaskPlansMadeSince(context.Context, time.Time) ([]db.TaskPlanRow, error) {
	s.planMu.Lock()
	defer s.planMu.Unlock()
	rows := make([]db.TaskPlanRow, 0, len(s.plans))
	for _, p := range s.plans {
		rows = append(rows, p)
	}
	return rows, nil
}

// #13169 design note 3: an effective outcome decision whose text records an owner-confirmation condition counts as an
// open wait for bare accepted instead of settled history.
func TestAcceptScopeGuardTreatsOwnerConfirmOutcomeDecisionAsOpenWait(t *testing.T) {
	ctx := context.Background()
	doneWhen := []string{"Code fix shipped"}
	s, _, todo := multiScopeReviewTask(t, "Webhook fix", "Fix webhook retry bug.", doneWhen, nil, "PR #901 merged.")

	st := &scopeOutcomeStore{
		phaseOutcomeStore: &phaseOutcomeStore{
			ctxAwareLinkStore: newCtxAwareLinkStore(),
			decisions: []db.TodoDecisionRow{{
				ID:        "dec_outcome_1",
				TodoID:    todo.ID,
				Source:    db.DecisionOutcome,
				Question:  "Production data cleanup",
				Choice:    "Not touched this round; tell me the scope if you want it handled",
				CreatedAt: time.Now().UTC(),
			}},
		},
	}
	s.setStore(func() Store { return st })
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	t.Cleanup(func() { s.tasks.outcomeRuns.Wait() })

	raw := reviewRaw(t, todo.ID, map[string]any{
		"verdict": "accepted",
		"note":    "Code fix merged",
		"checks":  []ReviewCheck{{Criterion: doneWhen[0], Met: true, Evidence: "PR #901 merged"}},
	})
	_, err := s.runTool(ctx, "server", "conv1", "task_review", raw)
	if err == nil || !strings.Contains(err.Error(), "bare accepted refused") || !strings.Contains(err.Error(), "Production data cleanup") {
		t.Fatalf("expected unsettled outcome decision to refuse bare accepted, got: %v", err)
	}

	// Once a linked follow-up to-do carries that scope, bare accepted succeeds.
	createFollowup, _ := json.Marshal(map[string]any{
		"title":   "Production data cleanup",
		"part_of": todo.ID,
	})
	if _, err := s.runTool(ctx, "server", "conv1", "todo_create", createFollowup); err != nil {
		t.Fatalf("todo_create: %v", err)
	}
	if _, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err != nil {
		t.Fatalf("expected accept to succeed once follow-up covers outcome wait, got: %v", err)
	}
}

func TestAcceptScopeGuardPromptAndToolGuidance(t *testing.T) {
	instructions := prompts.RenderSupervisionInstructions()
	if !strings.Contains(instructions, "uncovered brief scope") || !strings.Contains(instructions, "linked follow-up to-do") {
		t.Fatalf("supervision_instructions.md should state uncovered brief scope card-or-follow-up rule")
	}
	desc := prompts.Tool("task_review", "en").Description
	if !strings.Contains(desc, "uncovered scope") || !strings.Contains(desc, "linked follow-up to-do") {
		t.Fatalf("task_review.md should state uncovered scope card-or-follow-up rule:\n%s", desc)
	}
}
