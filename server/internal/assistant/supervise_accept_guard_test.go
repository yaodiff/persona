package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// A supervised task waiting for review, after its agent stopped with lastText as its last reply. The done judge says
// it finished, so the ledger is in pending_review; the guard scans the review fields and that reply.
func guardedReviewTask(t *testing.T, lastText string) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f, todo := newSupervisedService(t)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, lastText, 0)
	waitFor(t, "wait for review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	return s, f, todo
}

func reviewRaw(t *testing.T, todoID string, fields map[string]any) json.RawMessage {
	t.Helper()
	args := map[string]any{"todo_id": todoID}
	for k, v := range fields {
		args[k] = v
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// metChecks are the task's two done_when criteria, each met with evidence, so a bare accept passes the criteria check.
func metChecks() []ReviewCheck {
	return []ReviewCheck{
		{Criterion: "PR 合并到 master", Met: true, Evidence: "pull/901 merged"},
		{Criterion: "浅色 / 深色截图贴进 PR", Met: true, Evidence: "both attached"},
	}
}

// #12026 case 1: the agent's result is blocked on information only the owner has; a bare accepted is refused and the
// error names needs_user, and nothing is written to the task.
func TestAcceptGuardRefusesOwnerOnlyBlocker(t *testing.T) {
	s, _, todo := guardedReviewTask(t, "Session not found: that id looks like a ChatGPT workspace id, not a session id. "+
		"Please provide the session title or time so I can find the right one.")
	ctx := context.Background()

	raw := reviewRaw(t, todo.ID, map[string]any{"verdict": "accepted", "note": "agent asked the owner for the right id",
		"checks": metChecks()})
	_, err := s.runTool(ctx, "server", "conv1", "task_review", raw)
	if err == nil || !strings.Contains(err.Error(), "bare accepted refused") ||
		!strings.Contains(err.Error(), "needs_user") || !strings.Contains(err.Error(), "only_owner_knows") {
		t.Fatalf("owner-only blocker accepted: %v", err)
	}
	cur := todoNow(s, todo.ID)
	if cur.DoneAt != nil || cur.Supervision.State != reviewPending || len(cur.Supervision.Reviews) != 0 {
		t.Fatalf("a refused review changed the task: %+v", cur.Supervision)
	}

	// The Chinese wording fires the same signal.
	raw = reviewRaw(t, todo.ID, map[string]any{"verdict": "accepted",
		"note": "只有用户知道正确的会话 ID，等用户提供", "checks": metChecks()})
	if _, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err == nil ||
		!strings.Contains(err.Error(), "bare accepted refused") {
		t.Fatalf("Chinese owner-only wording accepted: %v", err)
	}
}

// #12026 case 2: a read-only evaluation came back inconclusive / partly effective; a bare accepted is refused and the
// error names user_check, needs_user and a follow-up to-do.
func TestAcceptGuardRefusesInconclusiveResult(t *testing.T) {
	s, _, todo := guardedReviewTask(t, "Eval finished: Inconclusive. One question still retrieves the wrong page, and most "+
		"of the skill changes showed no effect or the opposite effect.")
	ctx := context.Background()

	raw := reviewRaw(t, todo.ID, map[string]any{"verdict": "accepted",
		"note":   "the before/after eval is Inconclusive and partially done; the owner should decide what is next",
		"checks": metChecks()})
	_, err := s.runTool(ctx, "server", "conv1", "task_review", raw)
	if err == nil || !strings.Contains(err.Error(), "bare accepted refused") ||
		!strings.Contains(err.Error(), "user_check") || !strings.Contains(err.Error(), "follow-up to-do") {
		t.Fatalf("inconclusive result accepted: %v", err)
	}
	if cur := todoNow(s, todo.ID); cur.DoneAt != nil || cur.Supervision.State != reviewPending {
		t.Fatalf("a refused review changed the task: %+v", cur.Supervision)
	}
}

func testContractReviewTask(t *testing.T, mode, value, evidence string, v2 bool) (*Service, Todo, []ReviewCheck) {
	t.Helper()
	s, f := newQuietService(t)
	if v2 {
		setSettings(t, s, func(v *Settings) { v.TodosV2 = new(true) })
	}
	f.judge = `{"verdict":"done","reason":"The analysis is complete."}`
	const criterion = "Evaluate the hypothesis and record the observations"
	task := map[string]any{
		"title": "Evaluate the hypothesis", "prompt": criterion, "machine": "box", "cwd": "/code/app",
		"readonly": true, "role": "worker", "why": "The data is on this machine", "done_when": []string{criterion},
	}
	if mode != "" {
		task[mode] = value
	}
	if _, err := runJSON(t, s, "conv1", "task_plan", map[string]any{"tasks": []any{task}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the agent link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, evidence, 0)
	waitFor(t, "wait for review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	return s, todo, []ReviewCheck{{Criterion: criterion, Met: true, Evidence: evidence}}
}

func TestAcceptGuardAcceptsStructuredResearch(t *testing.T) {
	for _, tc := range []struct {
		name, mode, value, evidence string
		v2                          bool
	}{
		{"inconclusive research", "work_mode", "research", "The first experiment was inconclusive. The repeat showed the alternative hypothesis does not hold (不成立).", false},
		{"quoted scope wording", "completion_mode", "ready", "The report documents the sample's existing status.\n## 未处理\n- Pending sample records were counted.", true},
		{"research declared in scope", "", "", "The comparison was inconclusive.", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, todo, checks := testContractReviewTask(t, tc.mode, tc.value, tc.evidence, tc.v2)
			if tc.mode == "" {
				if _, err := runJSON(t, s, "conv1", "task_scope", map[string]any{
					"todo_id": todo.ID, "work_mode": "research",
					"change": "Declare the current work as research", "why": "The evaluation is the deliverable",
				}); err != nil {
					t.Fatal(err)
				}
			}
			review := func(fields map[string]any) (string, error) {
				fields["verdict"] = reviewAccepted
				return s.runTool(t.Context(), "server", "conv1", "task_review", reviewRaw(t, todo.ID, fields))
			}
			if _, err := review(map[string]any{}); err == nil || !strings.Contains(err.Error(), "has no check") {
				t.Fatalf("accepted without required checks: %v", err)
			}
			if tc.mode != "completion_mode" {
				res, err := review(map[string]any{"checks": checks})
				if cur := todoNow(s, todo.ID); err != nil || cur.DoneAt == nil || cur.Supervision.State != reviewAccepted {
					t.Fatalf("structured research acceptance: %s, %v", res, err)
				}
				return
			}
			const deferred = "Revisit the sample when the next data set is available."
			if _, err := review(map[string]any{"checks": checks, "deferred_scope": deferred}); err == nil ||
				!strings.Contains(err.Error(), "deferred scope has no owner action") {
				t.Fatalf("accepted with unhandled deferred scope: %v", err)
			}
			unrelated := createRequestAndPiece(t, s, "conv1", deferred, "", "", nil)
			if _, err := review(map[string]any{"checks": checks, "deferred_scope": deferred, "follow_up_todo_id": unrelated.ID}); err == nil ||
				!strings.Contains(err.Error(), "must be open and linked") {
				t.Fatalf("accepted with an unrelated follow-up: %v", err)
			}
			followUp := createRequestAndPiece(t, s, "conv1", deferred, "", todo.ID, nil)
			res, err := review(map[string]any{"checks": checks, "deferred_scope": deferred, "follow_up_todo_id": followUp.ID})
			cur := todoNow(s, todo.ID)
			if err != nil || cur.DoneAt == nil || cur.Supervision.State != reviewAccepted {
				t.Fatalf("structured research acceptance: %s, %v", res, err)
			}
			if saved := triageLatestReview(cur.Supervision); saved.DeferredScope != deferred || saved.FollowUpTodoID != followUp.ID {
				t.Fatalf("accepted review lost its linked handoff: %+v", saved)
			}
		})
	}
}

func TestAcceptGuardStructuredContractKeepsOpenDecision(t *testing.T) {
	s, todo, checks := testContractReviewTask(t, "work_mode", "research", "The available samples were analyzed.", false)
	wait := reviewRaw(t, todo.ID, map[string]any{
		"verdict": reviewNeedsUser, "why_owner": "only_owner_knows",
		"reason": "Is there another sample?", "recommend": "Use the current samples",
		"options": []DecisionOption{
			{Label: "Use the current samples", LeadsTo: "Keep the report"},
			{Label: "Add another sample", LeadsTo: "Include it in the analysis"},
		},
		"cost_of_waiting": "The report remains open.",
	})
	if _, err := s.runTool(t.Context(), "server", "conv1", "task_review", wait); err != nil {
		t.Fatal(err)
	}
	if ask, ok := s.askCopy(todo.ID); !ok || !decisionOpen(&ask, todoNow(s, todo.ID)) {
		t.Fatalf("review did not raise an open owner decision: %+v", ask)
	}
	raw := reviewRaw(t, todo.ID, map[string]any{"verdict": reviewAccepted, "checks": checks})
	if _, err := s.runTool(t.Context(), "server", "conv1", "task_review", raw); err == nil ||
		!strings.Contains(err.Error(), "owner request is still open") {
		t.Fatalf("accepted with an open owner request: %v", err)
	}
	cur := todoNow(s, todo.ID)
	if ask, ok := s.askCopy(todo.ID); !ok || !decisionOpen(&ask, cur) {
		t.Fatalf("owner decision did not remain open: %+v, %+v", cur.Supervision, ask)
	}
}

// An explicit non-done closeout status in the agent's report refuses the bare accept, while a report whose status is
// done (or that has no status line) does not.
func TestAcceptGuardRefusesNonDoneCloseout(t *testing.T) {
	s, _, todo := guardedReviewTask(t, "### Persona 工作报告\n- Status: not_done\n- Did: reproduced the crash, no fix yet")
	ctx := context.Background()
	raw := reviewRaw(t, todo.ID, map[string]any{"verdict": "accepted", "note": "closing it", "checks": metChecks()})
	_, err := s.runTool(ctx, "server", "conv1", "task_review", raw)
	if err == nil || !strings.Contains(err.Error(), "closeout") || !strings.Contains(err.Error(), "rework") {
		t.Fatalf("a not_done closeout was accepted: %v", err)
	}

	if matched, _ := acceptGuardBlock("### Persona 工作报告\n- Status: done\n- Did: fixed and merged"); matched != "" {
		t.Fatalf("a done closeout tripped the guard: %s", matched)
	}
}

// A clean, finished result with no bad-result signals accepts and closes exactly as before.
func TestAcceptGuardPassesCleanDone(t *testing.T) {
	s, _, todo := guardedReviewTask(t, "Fixed the crash and merged the PR: https://github.com/o/r/pull/901, light and dark screenshots attached.")
	ctx := context.Background()
	raw := reviewRaw(t, todo.ID, map[string]any{"verdict": "accepted", "note": "PR 已合并，截图齐了", "checks": metChecks()})
	res, err := s.runTool(ctx, "server", "conv1", "task_review", raw)
	if err != nil {
		t.Fatalf("a clean accept was refused: %v", err)
	}
	if !strings.Contains(res, "accepted and closed") {
		t.Fatalf("accept result: %s", res)
	}
	cur := todoNow(s, todo.ID)
	if cur.DoneAt == nil || cur.Supervision.State != reviewAccepted {
		t.Fatalf("the clean task did not close: %+v", cur.Supervision)
	}
}

// The two ways out of a refused accept both work: needs_user keeps the task in the owner's queue as a decision, and
// accepted with user_check keeps it there with a "Please verify" card.
func TestAcceptGuardAllowsNeedsUserAndUserCheck(t *testing.T) {
	s, _, todo := guardedReviewTask(t, "Inconclusive: the change had no measurable effect on retrieval; one question still fails.")
	ctx := context.Background()

	// needs_user with a decision the owner can answer.
	raw := reviewRaw(t, todo.ID, map[string]any{
		"verdict": "needs_user", "why_owner": "only_owner_knows",
		"reason": "评测没有定论，下一步选哪个方向", "recommend": "再做一轮",
		"options": []map[string]string{
			{"label": "再做一轮", "leads_to": "Agent 收紧检索后重跑评测"},
			{"label": "放弃这个改法", "leads_to": "回滚改动并关掉任务"},
		},
		"cost_of_waiting": "问题继续答错",
	})
	res, err := s.runTool(ctx, "server", "conv1", "task_review", raw)
	if err != nil {
		t.Fatalf("needs_user refused: %v", err)
	}
	if !strings.Contains(res, "waits for the user") {
		t.Fatalf("needs_user result: %s", res)
	}
	if cur := todoNow(s, todo.ID); cur.DoneAt != nil || cur.Supervision.State != reviewNeedsUser {
		t.Fatalf("needs_user did not keep the task open: %+v", cur.Supervision)
	}
}

// accepted with user_check: true is the guard's named way out for an inconclusive read-only result. It accepts the
// review and keeps the to-do open with a "Please verify" ask for the owner.
func TestAcceptGuardAllowsUserCheck(t *testing.T) {
	s, _, todo := guardedReviewTask(t, "Inconclusive: the change had no measurable effect; one question still retrieves the wrong page.")
	ctx := context.Background()
	raw := reviewRaw(t, todo.ID, map[string]any{"verdict": "accepted", "user_check": true,
		"note": "评测结论不确定，留给用户看结果再定", "checks": metChecks()})
	res, err := s.runTool(ctx, "server", "conv1", "task_review", raw)
	if err != nil {
		t.Fatalf("accepted with user_check refused: %v", err)
	}
	if !strings.Contains(res, "user_check") {
		t.Fatalf("user_check result: %s", res)
	}
	if cur := todoNow(s, todo.ID); cur.DoneAt != nil || cur.Supervision.State != reviewUserCheck {
		t.Fatalf("user_check did not keep the task open awaiting verification: %+v", cur.Supervision)
	}
	ask, ok := s.askCopy(todo.ID)
	if !ok || !ask.AcceptedAt.IsZero() {
		t.Fatalf("no open owner ask after user_check: ok=%v ask=%+v", ok, ask)
	}
}
