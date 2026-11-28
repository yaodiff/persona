package assistant

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func checkDeliveryApproval(t *testing.T, s *Service, id, state, gate, unblock string) {
	t.Helper()
	ctx := context.Background()
	list, err := s.listedTodosOf(ctx, false, map[string]bool{id: true})
	if err != nil || len(list) != 1 {
		t.Fatalf("read task: %d rows, %v", len(list), err)
	}
	if d := list[0].Delivery; d == nil || d.State != state || d.Gate != gate || d.Unblock != unblock {
		t.Errorf("delivery in the task list = %+v, want %s / %q / %s", d, state, gate, unblock)
	}
	if gate != "" && list[0].DoneAt == nil && !strings.Contains(deliverySummary(todoNow(s, id)), gate) {
		t.Errorf("task summary is missing the current gate %q", gate)
	}
	approvals := s.snapshotApprovals(ctx, "")
	if unblock == unblockUser {
		if len(approvals) != 1 || approvals[0].ID != id || approvals[0].Gate != gate {
			t.Errorf("snapshot approvals = %+v, want %s / %q", approvals, id, gate)
		}
	} else if len(approvals) != 0 {
		t.Errorf("snapshot still asks for an approval: %+v", approvals)
	}
}

func TestDeliveryApprovalFollowsPark(t *testing.T) {
	for _, tc := range []struct{ waitingOn, kind, target, reason string }{
		{parkSomeoneElse, "person_reply", "deployment agent", "Wait for the deployment agent to verify the build"},
		{parkExternalEvent, "external_event", "release build", "Wait for the build to become available"},
		{parkDate, "date", "release window", "Wait for the scheduled release window"},
	} {
		t.Run(tc.waitingOn, func(t *testing.T) {
			s, _, task := newSupervisedService(t)
			ctx, now := context.Background(), time.Now()
			const oldGate = "Approve the release"
			s.setTaskDelivery(ctx, task.ID, TodoDelivery{State: deliveryPR, Gate: oldGate, Unblock: unblockUser, At: now})
			checkDeliveryApproval(t, s, task.ID, deliveryPR, oldGate, unblockSupervisor) // its supervisor reads it first (#13191)
			if _, err := reviewErr(s, task.ID, map[string]any{
				"verdict": reviewParked, "reason": tc.reason, "waiting_on": tc.waitingOn, "until": now.Add(time.Hour).Format(time.RFC3339),
				"evidence": testParkEvidence(tc.kind, tc.target, "https://example.com/releases/42", tc.reason),
			}); err != nil {
				t.Fatal(err)
			}
			checkDeliveryApproval(t, s, task.ID, deliveryPR, tc.reason, unblockOther)
		})
	}
}

func TestDeliveryApprovalFollowsRework(t *testing.T) {
	s, f, task := newSupervisedService(t)
	ctx := context.Background()
	waitsForReview(t, s, f, task.ID, "The revised checks are ready for review.")
	const oldGate = "Approve the revised checks"
	s.setTaskDelivery(ctx, task.ID, TodoDelivery{State: deliveryPR, Gate: oldGate, Unblock: unblockUser, At: time.Now().Add(-time.Hour)})
	checkDeliveryApproval(t, s, task.ID, deliveryPR, oldGate, unblockSupervisor) // its supervisor reads it first (#13191)

	reviewTask(t, s, task.ID, map[string]any{"verdict": reviewRework, "note": "Add coverage for delayed replies"})
	cur := todoNow(s, task.ID)
	if cur.Supervision.State != reviewRework || listedAsk(t, s, task.ID) != nil {
		t.Fatalf("expected rework with no owner decision: %+v", cur.Supervision)
	}
	checkDeliveryApproval(t, s, task.ID, deliveryPR, "", unblockAgent)
	if summary := deliverySummary(cur); strings.Contains(summary, oldGate) {
		t.Errorf("task summary still shows the previous gate: %q", summary)
	}

	// A later round can renew the same hold without changing what it asks the owner to do.
	s.setTaskDelivery(ctx, task.ID, TodoDelivery{State: deliveryPR, Gate: oldGate, Unblock: unblockUser, At: time.Now()})
	checkDeliveryApproval(t, s, task.ID, deliveryPR, oldGate, unblockSupervisor) // its supervisor reads it first (#13191)
	decision := rolloutDecision()
	reviewTask(t, s, task.ID, decision)
	checkDeliveryApproval(t, s, task.ID, deliveryPR, decision["reason"].(string), unblockUser)
	ask := listedAsk(t, s, task.ID)
	if options, _ := ask["options"].([]any); ask["outcome"] != outcomeDecide || len(options) != 2 {
		t.Fatalf("expected both owner options after rework: %v", ask)
	}
}

func TestDeliveryApprovalFollowsReopenedDecisionRound(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx := context.Background()
	const externalGate = "Wait for the maintainer"
	s.setTaskDelivery(ctx, task.ID, TodoDelivery{State: deliveryPR, Gate: externalGate, Unblock: unblockOther, At: time.Now()})
	decision := rolloutDecision()
	reviewTask(t, s, task.ID, decision)
	checkDeliveryApproval(t, s, task.ID, deliveryPR, decision["reason"].(string), unblockUser)
	if ask := listedAsk(t, s, task.ID); ask == nil {
		t.Fatal("the first decision must be answerable")
	}

	closeAndReopen(t, s, task.ID)
	cur := todoNow(s, task.ID)
	if r := triageLatestReview(cur.Supervision); r == nil || r.Verdict != reviewNeedsUser || !r.At.Before(*cur.ReopenedAt) {
		t.Fatalf("the previous decision must stay in review history: %+v", r)
	}
	checkDeliveryApproval(t, s, task.ID, deliveryPR, externalGate, unblockOther)
	row := listedTodos(t, s)[task.ID]
	view, _ := row["view"].(map[string]any)
	if row["ask"] != nil || view == nil || view["section"] == assistantwire.TodoListSectionNeedsYou {
		t.Errorf("the reopened task still needs an answer: ask %v, view %v", row["ask"], view)
	}
	if _, rank := taskHealth(cur, nil, time.Now()); rank == 0 {
		t.Error("the briefing still treats the previous decision as an active wait")
	}

	decision["reason"] = "Choose the rollout for the revised build"
	reviewTask(t, s, task.ID, decision)
	checkDeliveryApproval(t, s, task.ID, deliveryPR, decision["reason"].(string), unblockUser)
	ask := listedAsk(t, s, task.ID)
	if options, _ := ask["options"].([]any); ask["outcome"] != outcomeDecide || len(options) != 2 {
		t.Fatalf("expected both owner options in the new round: %v", ask)
	}
}

func TestDeliveryApprovalFollowsDecisionAndAcceptance(t *testing.T) {
	s, f, task := newSupervisedService(t)
	ctx := context.Background()
	waitsForReview(t, s, f, task.ID, "The change is ready for a phased rollout.")
	s.setTaskDelivery(ctx, task.ID, TodoDelivery{State: deliveryMerged, Gate: "Approve the release", Unblock: unblockUser, At: time.Now()})
	decision := rolloutDecision()
	if _, err := reviewErr(s, task.ID, decision); err != nil {
		t.Fatal(err)
	}
	checkDeliveryApproval(t, s, task.ID, deliveryMerged, decision["reason"].(string), unblockUser)
	if rec := answerAsk(s, task.ID, map[string]string{"option": decision["recommend"].(string)}); rec.Code != http.StatusOK {
		t.Fatalf("answer: %d %s", rec.Code, rec.Body.String())
	}
	checkDeliveryApproval(t, s, task.ID, deliveryMerged, "", unblockAgent)

	// A later judgment can still ask for an approval in a new round. The supervisor's requested owner check
	// ends that hold and leaves the existing verification ask as the next step.
	s.setTaskDelivery(ctx, task.ID, TodoDelivery{State: deliveryMerged, Gate: "Approve the next release", Unblock: unblockUser, At: time.Now()})
	checkDeliveryApproval(t, s, task.ID, deliveryMerged, "Approve the next release", unblockSupervisor) // its supervisor reads it first (#13191)
	f.judge = `{"changed":"The change is ready","how":"Open the updated screen"}`
	reviewTask(t, s, task.ID, map[string]any{"verdict": reviewAccepted, "user_check": true,
		"checks": completeReviewChecks(todoNow(s, task.ID))})
	checkDeliveryApproval(t, s, task.ID, deliveryMerged, "", "")
	waitFor(t, "the verification ask", func() bool { return askOf(s, task.ID) != nil })
	if ask := askOf(s, task.ID); decideAsk(ask) || !ask.AcceptedAt.IsZero() {
		t.Fatalf("the owner's verification was lost: %+v", ask)
	}
}

func TestDeliveryApprovalFollowsPRStatus(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx, now := context.Background(), time.Now()
	const url = "https://github.com/example/app/pull/42"
	s.setTaskStage(ctx, task.ID, "review", url)
	observe := func(state string, draft bool, at time.Time) {
		t.Helper()
		if err := s.applyPRStatus(ctx, todoNow(s, task.ID), TodoPRStatus{URL: url, State: state, Draft: draft, CheckedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	const readyGate = "Approve marking the PR ready for review"
	s.setTaskDelivery(ctx, task.ID, TodoDelivery{State: deliveryPR, Gate: readyGate, Unblock: unblockUser, At: now})
	observe("open", true, now.Add(time.Second))
	checkDeliveryApproval(t, s, task.ID, deliveryPR, readyGate, unblockSupervisor) // its supervisor reads it first (#13191)
	observe("open", false, now.Add(2*time.Second))
	checkDeliveryApproval(t, s, task.ID, deliveryPR, "", unblockOther)

	const mergeGate = "Approve the merge"
	s.setTaskDelivery(ctx, task.ID, TodoDelivery{State: deliveryPR, Gate: mergeGate, Unblock: unblockUser, At: now.Add(3 * time.Second)})
	observe("open", false, now.Add(4*time.Second))
	checkDeliveryApproval(t, s, task.ID, deliveryPR, mergeGate, unblockSupervisor) // its supervisor reads it first (#13191)
	observe("merged", false, now.Add(5*time.Second))
	checkDeliveryApproval(t, s, task.ID, deliveryMerged, "", unblockAgent)
	if got := todoNow(s, task.ID); got.Stage != "merge" || got.DoneAt != nil {
		t.Fatalf("the merged PR needs deployment and verification: stage %q, done %v", got.Stage, got.DoneAt)
	}
}

func TestDeliveryApprovalPreservesIndependentDecisionOnMerge(t *testing.T) {
	s, _, task := newSupervisedService(t)
	ctx := context.Background()
	const url = "https://github.com/example/app/pull/42"
	s.setTaskStage(ctx, task.ID, "review", url)
	s.setTaskDelivery(ctx, task.ID, TodoDelivery{State: deliveryPR, Gate: "Wait for the maintainer to merge", Unblock: unblockOther, At: time.Now()})
	decision := rolloutDecision()
	if _, err := reviewErr(s, task.ID, decision); err != nil {
		t.Fatal(err)
	}
	if err := s.applyPRStatus(ctx, todoNow(s, task.ID), TodoPRStatus{URL: url, State: "merged", CheckedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	checkDeliveryApproval(t, s, task.ID, deliveryMerged, decision["reason"].(string), unblockUser)
}

func TestDeliveryApprovalReadsLegacyExternalActor(t *testing.T) {
	s, _, task := newSupervisedService(t)
	const gate = "等部署集成 Agent 实测"
	s.setTaskDelivery(context.Background(), task.ID, TodoDelivery{State: deliveryMerged, Gate: gate, Unblock: unblockUser, At: time.Now()})
	checkDeliveryApproval(t, s, task.ID, deliveryMerged, gate, unblockOther)
}

func TestJudgedDeliveryUsesExplicitExternalWait(t *testing.T) {
	for _, gate := range []string{"等部署集成 Agent 实测", "等维护者合并", "待评审人回复", "Wait for the deployment agent", "Awaiting maintainer review"} {
		if got := judgedDelivery(deliveryPR, gate, unblockUser, nil); got.Gate != gate || got.Unblock != unblockOther {
			t.Errorf("judged external wait %q = %+v", gate, got)
		}
	}
	const reply = `<closeout status="blocked" merged="no" deployed="no">
artifact: pr https://github.com/example/app/pull/42
waiting: Waiting for the reviewer to respond
</closeout>`
	if got := judgedDelivery(deliveryPR, "", unblockUser, []taskDoneReply{doneReply(reply)}); got.Unblock != unblockOther {
		t.Errorf("judged closeout = %+v", got)
	}
	if got := judgedDelivery(deliveryPR, "Waiting for your approval to contact the reviewer", unblockUser, nil); got.Unblock != unblockUser {
		t.Errorf("explicit owner approval = %+v", got)
	}
}
