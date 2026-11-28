package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// toolFrom runs tool name as conversation session.
func toolFrom(t *testing.T, s *Service, session, name string, args any) (string, bool) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Tool(context.Background(), ToolCall{Name: name, Arguments: raw, MachineID: "server", SessionID: session})
	if len(r.Content) == 0 {
		t.Fatalf("%s: no answer", name)
	}
	return r.Content[0].Text, r.IsError
}

// The owner answered a task's decision in the chat, and the conversation already passed it on with agent_send:
// task_decided told_agent ends the decision as the card would (out of Needs you and needs_user, no default at its
// deadline), and sends nothing to the agent again. Options that are none of the decision's, a conversation that neither
// is the main one nor supervises the task, and a decision already answered are refused.
func TestTaskDecidedEndsADecisionTheOwnerAnsweredInChat(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	sent := len(sentToAgent(f))
	said := "直接全量吧，不用灰度了"
	if got, isErr := toolFrom(t, s, "conv-other", "task_decided", map[string]any{"todo_id": todo.ID, "option": "直接全量", "said": said,
		"told_agent": true}); !isErr || !strings.Contains(got, "supervised by another conversation") {
		t.Fatalf("another conversation: %v %s", isErr, got)
	}
	if got, isErr := tool(t, s, "task_decided", map[string]any{"todo_id": todo.ID, "option": "再等等", "said": said, "told_agent": true}); !isErr ||
		!strings.Contains(got, "先灰度 10%") || !strings.Contains(got, "直接全量") {
		t.Fatalf("an option that is none of them: %v %s", isErr, got)
	}
	if got, isErr := tool(t, s, "task_decided", map[string]any{"todo_id": todo.ID, "option": "直接全量"}); !isErr {
		t.Fatalf("without the owner's words: %s", got)
	}
	if listedAsk(t, s, todo.ID) == nil || todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatal("a refused call changed the decision")
	}
	if got, isErr := tool(t, s, "task_decided", map[string]any{"todo_id": todo.ID, "option": "直接全量", "said": said,
		"told_agent": true}); isErr {
		t.Fatalf("task_decided: %s", got)
	}
	if got := sentToAgent(f)[sent:]; len(got) != 0 {
		t.Fatalf("sent to the agent again: %q", got)
	}
	if a := askOf(s, todo.ID); a.AcceptedAt.IsZero() || a.ByDefault || a.Chosen != "直接全量" || listedAsk(t, s, todo.ID) != nil ||
		todoNow(s, todo.ID).Supervision.State == reviewNeedsUser {
		t.Fatalf("the decision still waits: %+v", a)
	}
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now())
	if got := sentToAgent(f)[sent:]; len(got) != 0 {
		t.Fatalf("the default was taken after the owner chose: %q", got)
	}
	if got, isErr := tool(t, s, "task_decided", map[string]any{"todo_id": todo.ID, "option": "先灰度 10%", "said": said}); !isErr ||
		!strings.Contains(got, "no decision waiting") {
		t.Fatalf("a second answer: %v %s", isErr, got)
	}
}

// Without told_agent the option goes to the task's agent as an answer on the card does.
func TestTaskDecidedSendsTheOptionToTheAgent(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "两种走法")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	sent := len(sentToAgent(f))
	if got, isErr := tool(t, s, "task_decided", map[string]any{"todo_id": todo.ID, "option": "直接全量", "said": "全量"}); isErr {
		t.Fatalf("task_decided: %s", got)
	}
	if got := sentToAgent(f)[sent:]; len(got) != 1 || !strings.Contains(got[0], "直接全量") {
		t.Fatalf("to the agent: %q", got)
	}
	if listedAsk(t, s, todo.ID) != nil || todoNow(s, todo.ID).Supervision.State == reviewNeedsUser {
		t.Fatal("the decision still waits")
	}
}

// On the store, the answer is a decision on the to-do: the option, the owner's words as its reason, and the conversation
// that heard it as its source.
func TestTaskDecidedIsADecisionOnTheTodo(t *testing.T) {
	database := todoTestDB(t)
	s, f, todo, _, _ := conclusionServiceOn(t, database, false)
	waitsForReview(t, s, f, todo.ID, "两种走法")
	reviewTask(t, s, todo.ID, rolloutDecision())
	if got, isErr := tool(t, s, "task_decided", map[string]any{"todo_id": todo.ID, "option": "先灰度 10%", "said": "按推荐的来",
		"told_agent": true}); isErr {
		t.Fatalf("task_decided: %s", got)
	}
	list, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Choice != "先灰度 10%" || list[0].Reason != "按推荐的来" || list[0].Source != db.DecisionManual ||
		list[0].SourceSession != "conv1" || list[0].Question != "先灰度还是直接全量" {
		t.Fatalf("decisions %+v", list)
	}
}

// When the owner verbally passes a personal verification (user_check) in chat, task_decided with an explicit pass
// option settles the open verification ask and closes the task as accepted. Non-pass options, unauthorized
// conversations, and duplicate calls are refused without modifying the waiting verification.
func TestTaskDecidedPassesOwnerUserCheckInChat(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "Ready for the owner to verify.")
	f.judge = `{"changed":"Updated the layout","link":"","how":"Open the page and check"}`
	reviewTask(t, s, todo.ID, map[string]any{
		"verdict":    "accepted",
		"note":       "Checks met; owner to verify",
		"user_check": true,
		"checks":     completeReviewChecks(todoNow(s, todo.ID)),
	})
	waitFor(t, "the user_check ask", func() bool {
		a := askOf(s, todo.ID)
		return a != nil && !a.AskedAt.IsZero()
	})
	if listedAsk(t, s, todo.ID) == nil || todoNow(s, todo.ID).Supervision.State != reviewUserCheck || todoNow(s, todo.ID).DoneAt != nil {
		t.Fatalf("expected open user_check waiting on owner: todo=%+v ask=%+v", todoNow(s, todo.ID), askOf(s, todo.ID))
	}

	// Another conversation may not settle this task's verification.
	if got, isErr := toolFrom(t, s, "conv-other", "task_decided", map[string]any{
		"todo_id": todo.ID, "option": "Pass", "said": "Looks good",
	}); !isErr || !strings.Contains(got, "supervised by another conversation") {
		t.Fatalf("another conversation: %v %s", isErr, got)
	}

	// Non-pass options must be refused so change requests go through rework instead of closing the task.
	if got, isErr := tool(t, s, "task_decided", map[string]any{
		"todo_id": todo.ID, "option": "Needs changes", "said": "Fix the header",
	}); !isErr || !strings.Contains(got, "user_check") {
		t.Fatalf("non-pass option: %v %s", isErr, got)
	}
	if todoNow(s, todo.ID).DoneAt != nil || todoNow(s, todo.ID).Supervision.State != reviewUserCheck || listedAsk(t, s, todo.ID) == nil {
		t.Fatal("refused call modified the waiting user_check")
	}

	// Explicit oral approval from the supervising conversation settles the verification and closes the task.
	if got, isErr := tool(t, s, "task_decided", map[string]any{
		"todo_id": todo.ID, "option": "通过", "said": "通过，验证没问题",
	}); isErr {
		t.Fatalf("task_decided user_check pass: %s", got)
	}
	after := todoNow(s, todo.ID)
	if after.DoneAt == nil || after.Supervision.State != reviewAccepted || listedAsk(t, s, todo.ID) != nil {
		t.Fatalf("expected closed accepted task after oral pass: done=%v state=%q ask=%+v", after.DoneAt, after.Supervision.State, listedAsk(t, s, todo.ID))
	}
	if a := askOf(s, todo.ID); a == nil || a.AcceptedAt.IsZero() {
		t.Fatalf("expected accepted ask timestamp, got %+v", a)
	}

	// A second pass attempt is refused because the task is already closed.
	if got, isErr := tool(t, s, "task_decided", map[string]any{
		"todo_id": todo.ID, "option": "Pass", "said": "Pass again",
	}); !isErr {
		t.Fatalf("second pass should fail: %s", got)
	}
}

// On the store, passing a personal verification through task_decided also records a manual audit decision.
func TestTaskDecidedUserCheckRecordsDecisionOnTodo(t *testing.T) {
	database := todoTestDB(t)
	s, f, todo, _, _ := conclusionServiceOn(t, database, false)
	waitsForReview(t, s, f, todo.ID, "Ready for the owner to verify.")
	f.judge = `{"changed":"Updated the layout","link":"","how":"Open the page and check"}`
	reviewTask(t, s, todo.ID, map[string]any{
		"verdict":    "accepted",
		"note":       "Checks met; owner to verify",
		"user_check": true,
		"checks":     completeReviewChecks(todoNow(s, todo.ID)),
	})
	waitFor(t, "the user_check ask", func() bool {
		a := askOf(s, todo.ID)
		return a != nil && !a.AskedAt.IsZero()
	})
	said := "Looks good, passed"
	if got, isErr := tool(t, s, "task_decided", map[string]any{
		"todo_id": todo.ID, "option": "Pass", "said": said,
	}); isErr {
		t.Fatalf("task_decided user_check pass: %s", got)
	}
	// The pass closes the task, so what it came to is recorded in the background as well (goRecordTaskOutcome, a
	// decision of source outcome). Wait for it rather than read the store while it may still be writing: the owner's
	// pass is the one manual decision, the completion's outcome the one outcome decision, whichever was written first
	// (#17879).
	s.tasks.outcomeRuns.Wait()
	list, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	var manual, outcome []db.TodoDecisionRow
	for _, d := range list {
		switch d.Source {
		case db.DecisionManual:
			manual = append(manual, d)
		case db.DecisionOutcome:
			outcome = append(outcome, d)
		default:
			t.Fatalf("unexpected decision %+v", d)
		}
	}
	if len(manual) != 1 || manual[0].Choice != "Pass" || manual[0].Reason != said || manual[0].SourceSession != "conv1" {
		t.Fatalf("owner decisions %+v (all %+v)", manual, list)
	}
	if len(outcome) != 1 || !strings.HasPrefix(outcome[0].SourceMessage, "outcome:"+todo.ID+":") {
		t.Fatalf("outcome decisions %+v (all %+v)", outcome, list)
	}
}
