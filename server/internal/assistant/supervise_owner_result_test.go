package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestSupervisorReviewKeepsOwnerResultOpen(t *testing.T) {
	for _, tc := range []struct {
		name      string
		readonly  bool
		requested bool
		inboxKind string
		outcome   string
		blocked   bool
	}{
		{name: "requested investigation", readonly: true, requested: true, blocked: true},
		{name: "unavailable result", readonly: true, requested: true, outcome: outcomeCannot, blocked: true},
		{name: "external group task", inboxKind: KindMention, blocked: true},
		{name: "internal read-only inspection", readonly: true},
		{name: "requested writable task", requested: true},
		{name: "direct chat link", inboxKind: KindDM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, f := newQuietServiceOn(t, nil)
			raw, err := json.Marshal(map[string]any{"tasks": []map[string]any{{
				"title": "Check the sample", "prompt": "Compare the sample with the reference.",
				"machine": "box", "cwd": "/code/app", "readonly": tc.readonly, "user_requested": tc.requested,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.toolTaskPlan(ctx, "conv1", raw); err != nil {
				t.Fatal(err)
			}
			if plans, err := s.conversationPlans(ctx, "conv1"); err != nil || len(plans) != 1 {
				t.Fatalf("expected one plan, got %d: %v", len(plans), err)
			} else if plan := plans[0]; plan.State == planOpen {
				if _, err := s.startPlan(ctx, plan.ID, nil, false); err != nil {
					t.Fatal(err)
				}
			}
			waitFor(t, "task started", func() bool { return len(onlyTodo(s).Agents) == 1 })
			todo := onlyTodo(s)
			if tc.inboxKind != "" {
				id := "connector:sample"
				if err = s.inboxReady(ctx); err != nil {
					t.Fatal(err)
				}
				s.inbox.mu.Lock()
				s.inbox.items[id] = &InboxItem{ID: id, Kind: tc.inboxKind}
				s.inbox.mu.Unlock()
				if _, err = s.editTodo(ctx, todo.ID, db.TodoEdit{InboxItem: &id}); err != nil {
					t.Fatal(err)
				}
			}
			f.judge = `{"verdict":"done","reason":"The two inputs agree."}`
			agentTurn(s, f, proto.StatusWorking, "", 0)
			agentTurn(s, f, proto.StatusDone, "The two inputs agree.", 0)
			waitFor(t, "supervisor review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
			fields := map[string]any{"verdict": reviewAccepted, "note": "The two inputs agree.", "outcome": tc.outcome}
			_, err = s.runTool(ctx, "server", "conv1", "task_review", reviewRaw(t, todo.ID, fields))
			if !tc.blocked {
				if err != nil || todoNow(s, todo.ID).DoneAt == nil {
					t.Fatalf("ordinary review did not close: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "the owner needs this task's result") {
				t.Fatalf("bare accepted should direct the supervisor to an owner card, got %v", err)
			}
			current := todoNow(s, todo.ID)
			if current.DoneAt != nil || len(current.Supervision.Reviews) != 0 {
				t.Fatalf("refusal wrote a review or closed the task: %+v", current.Supervision)
			}

			if tc.inboxKind == KindMention {
				decision := map[string]any{
					"verdict": reviewNeedsUser, "why_owner": "priority",
					"reason": "The two inputs agree. Is a second comparison needed?", "recommend": "Keep the result",
					"options": []DecisionOption{
						{Label: "Keep the result", LeadsTo: "Finish the comparison"},
						{Label: "Compare again", LeadsTo: "Check the next sample"},
					},
					"cost_of_waiting": "The comparison stays open.",
				}
				if _, err = s.runTool(ctx, "server", "conv1", "task_review", reviewRaw(t, todo.ID, decision)); err != nil {
					t.Fatal(err)
				}
				ask, ok := s.todoAsks(ctx)[todo.ID]
				if !ok || ask.Outcome != outcomeDecide || todoNow(s, todo.ID).DoneAt != nil {
					t.Fatalf("expected an open owner decision, got %+v", ask)
				}
				result, err := s.chooseOption(ctx, todo.ID, ask.AskedAt.Format(timeKeyLayout), ask.Recommend)
				if err != nil || !result.taken {
					t.Fatalf("owner option was not taken: %+v, %v", result, err)
				}
				if _, err = s.runTool(ctx, "server", "conv1", "task_review", reviewRaw(t, todo.ID, fields)); err != nil ||
					todoNow(s, todo.ID).DoneAt == nil {
					t.Fatalf("review did not close after the owner chose: %v", err)
				}
				return
			}

			fields["user_check"] = true
			if _, err = s.runTool(ctx, "server", "conv1", "task_review", reviewRaw(t, todo.ID, fields)); err != nil {
				t.Fatal(err)
			}
			current = todoNow(s, todo.ID)
			ask, ok := s.askCopy(todo.ID)
			if current.DoneAt != nil || current.Supervision.State != reviewUserCheck || !ok || !ask.AcceptedAt.IsZero() {
				t.Fatalf("expected open owner verification: state=%s ask=%v open=%v", current.Supervision.State, ok, current.DoneAt == nil)
			}
			if _, ok := s.todoAsks(ctx)[todo.ID]; !ok {
				t.Fatal("owner verification is missing from the app projection")
			}
			if result, err := s.acceptTry(ctx, todo.ID, ask.MessageID); err != nil || !result.taken || todoNow(s, todo.ID).DoneAt == nil {
				t.Fatalf("owner Pass did not close the task: %+v, %v", result, err)
			}
		})
	}
}
