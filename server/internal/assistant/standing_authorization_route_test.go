package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func routeStandingGrant() StandingAuthorization {
	return StandingAuthorization{ID: "sa_route", MachineID: "mac", Directory: "/code/app", Actions: []string{"merge"},
		Conditions: []string{}, Quote: "Merge changes in this repository without asking again.", Source: "Main conversation", GrantedAt: time.Now().UTC(), Reversible: true}
}

func TestStandingCardPathsPunctuation(t *testing.T) {
	for _, test := range []struct {
		name, separator string
		quotedAllowed   bool
	}{
		{"semicolon", ";", false}, {"comma", ",", true}, {"exclamation", "!", true}, {"question", "?", false},
		{"ideographic_period", "。", true}, {"fullwidth_semicolon", "；", true}, {"fullwidth_comma", "，", true},
		{"fullwidth_exclamation", "！", true}, {"fullwidth_question", "？", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			const first, second = "/example/first.txt", "/example/second"
			got, ok := standingCardPaths(proto.Interaction{Plan: first + test.separator + second})
			if want := []string{first, second}; !ok || !slices.Equal(got, want) {
				t.Fatalf("unquoted paths = %q, ok = %v; want %q", got, ok, want)
			}
			for _, quote := range []string{"\"", "'", "`"} {
				path := first + test.separator + "part"
				got, ok := standingCardPaths(proto.Interaction{Plan: quote + path + quote})
				if test.quotedAllowed {
					if want := []string{path}; !ok || !slices.Equal(got, want) {
						t.Errorf("quoted with %q: paths = %q, ok = %v; want %q", quote, got, ok, want)
					}
				} else if ok || len(got) != 0 {
					t.Errorf("quoted with %q: paths = %q, ok = %v; want rejection", quote, got, ok)
				}
			}
		})
	}
}

func TestStandingAuthorizationCards(t *testing.T) {
	for _, test := range []struct {
		name, kind, text, action string
		reversible, approved     bool
		directory                string
	}{
		{"question", proto.IxQuestion, "Merge?", "merge", true, true, "/code/app"},
		{"plan", proto.IxPlan, "Merge changes.", "merge", true, true, "/code/app"},
		{"permission", proto.IxPermission, "Merge?", "merge", true, false, "/code/app"},
		{"other_repository", proto.IxQuestion, "Merge?", "merge", true, false, "/code/other"},
		{"same_prefix_other_repository", proto.IxQuestion, "Merge?", "merge", true, false, "/code/application"},
		{"unavailable_nested_repository", proto.IxQuestion, "Merge?", "merge", true, false, "/code/app/vendor/other"},
		{"production_outside_scope", proto.IxQuestion, "deploy-production?", "merge", true, false, "/code/app"},
		{"send_outside_scope", proto.IxQuestion, "Send message?", "merge", true, false, "/code/app"},
		{"delete_outside_scope", proto.IxQuestion, "Delete files?", "merge", true, false, "/code/app"},
		{"spend_outside_scope", proto.IxQuestion, "Spend money?", "merge", true, false, "/code/app"},
		{"production_irreversible", proto.IxQuestion, "deploy-production?", "deploy-production", false, false, "/code/app"},
		{"send_irreversible", proto.IxQuestion, "Send message?", "send", false, false, "/code/app"},
		{"delete_irreversible", proto.IxQuestion, "Delete files?", "delete", false, false, "/code/app"},
		{"spend_irreversible", proto.IxQuestion, "Spend money?", "spend", false, false, "/code/app"},
		{"explicit_reversible_production", proto.IxQuestion, "deploy-production?", "deploy-production", true, true, "/code/app"},
		{"explicit_reversible_send", proto.IxQuestion, "Send message?", "send", true, true, "/code/app"},
		{"explicit_reversible_delete", proto.IxQuestion, "Delete files?", "delete", true, true, "/code/app"},
		{"explicit_reversible_spend", proto.IxQuestion, "Spend money?", "spend", true, true, "/code/app"},
		{"compound_plan", proto.IxPlan, "Merge changes and deploy to production.", "merge", true, false, "/code/app"},
		{"unclassified_plan", proto.IxPlan, "Merge changes; then format the disk.", "merge", true, false, "/code/app"},
	} {
		t.Run(test.name, func(t *testing.T) {
			database := taskRecoveryDatabase(t)
			e := newRouteEnv(t, database, true)
			t.Cleanup(func() { settleService(t, e.s) })
			grant := routeStandingGrant()
			grant.Actions = []string{test.action}
			grant.Reversible = test.reversible
			saveDispatchAuthorizations(t, database, grant)
			agents := append([]proto.Session(nil), e.agents...)
			agents[0].Cwd = test.directory
			e.sessions("mac", agents...)
			e.f.setSessions("mac", agents...)
			ix := askQuestion("standing-card", "worker-1")
			ix.Kind = test.kind
			if ix.Kind == proto.IxPlan {
				ix.Questions = nil
				ix.Plan = test.text
				ix.ToolName = "ExitPlanMode"
				ix.ToolInput, _ = json.Marshal(map[string]any{"plan": ix.Plan})
			} else if ix.Kind == proto.IxQuestion {
				ix.Questions[0].Question = test.text
				ix.Questions[0].Options = []proto.QuestionOption{{Label: "Continue"}, {Label: "Not now"}}
				ix.ToolInput, _ = json.Marshal(map[string]any{"questions": ix.Questions})
			}
			p := &pending{ix: ix, done: make(chan proto.Resolution, 1)}
			putPending(e.s, p)
			e.s.record(ix)
			e.s.announce(ix)
			record, err := database.InteractionRecord(t.Context(), ix.ID)
			if err != nil {
				t.Fatal(err)
			}
			if test.approved {
				select {
				case answer := <-p.done:
					if answer.AnsweredBy != proto.AnsweredByAssistant || !strings.Contains(answer.Reason, grant.ID) || !strings.Contains(answer.Reason, grant.Quote) {
						t.Fatalf("answer lacks authority: %+v", answer)
					}
					if ix.Kind == proto.IxPlan && (answer.Approve == nil || !*answer.Approve || answer.Mode != "default") {
						t.Fatal("plan not approved")
					}
				default:
					t.Fatal("hook did not receive automatic answer")
				}
				if record.Status != "resolved" || record.Resolution == nil {
					t.Fatalf("outcome: %+v", record)
				}
				decisions := decisionsOf(t, database, e.todo.ID)
				if len(decisions) != 1 || decisions[0].Source != db.DecisionAgent || !strings.Contains(decisions[0].Reason, grant.ID) {
					t.Fatalf("decision: %+v", decisions)
				}
				for _, shown := range e.toldOf("fleet.interaction") {
					var event struct {
						Interaction proto.Interaction `json:"interaction"`
					}
					_ = json.Unmarshal(shown, &event)
					if event.Interaction.ID == ix.ID {
						t.Fatalf("automatic card shown: %s", shown)
					}
				}
				if e.pushesOf(ix.ID) != 0 || len(e.s.heldAsks(e.todo.ID, "lead1")) != 0 {
					t.Fatal("automatic card reached user or supervisor")
				}
				counts := e.s.briefConfirmationsAt(t.Context(), time.Now().Add(time.Second), time.UTC)
				if counts.AutoAuthorized != 1 || counts.UserNotified != 0 {
					t.Fatalf("counts: %+v", counts)
				}
			} else {
				if record.Status != "pending" || len(decisionsOf(t, database, e.todo.ID)) != 0 {
					t.Fatalf("unapproved card changed: %+v", record)
				}
				if _, ok := e.s.pendingIx(ix.ID); !ok {
					t.Fatal("manual card disappeared")
				}
			}
		})
	}
}

type failingStandingStore struct{ *db.DB }

func (f failingStandingStore) FinishStandingInteraction(context.Context, string, string, proto.Interaction, proto.Resolution, db.TodoDecisionRow, time.Time) error {
	return errors.New("audit unavailable")
}

func TestStandingAuthorizationFailureAndRevocation(t *testing.T) {
	for _, test := range []string{"audit_failure", "revoked", "explicit_task_limit", "no_task", "local_card", "unknown_condition", "task_decision_limit", "different_task_repository"} {
		t.Run(test, func(t *testing.T) {
			database := taskRecoveryDatabase(t)
			e := newRouteEnv(t, database, true)
			t.Cleanup(func() { settleService(t, e.s) })
			grant := routeStandingGrant()
			saveDispatchAuthorizations(t, database, grant)
			switch test {
			case "different_task_repository":
				e.s.todos.mu.Lock()
				e.s.todos.todos[e.todo.ID].Agents[0].Cwd = "/code/unrelated"
				e.s.todos.mu.Unlock()
			case "task_decision_limit":
				_, err := database.InsertTodoDecision(t.Context(), db.TodoDecisionRow{ID: "user-stop", TodoID: e.todo.ID, Question: "Delivery", Choice: "Do not merge; stop at a PR.", Source: db.DecisionManual, CreatedAt: decisionNow()})
				if err != nil {
					t.Fatal(err)
				}
			case "audit_failure":
				e.s.setStore(func() Store { return failingStandingStore{database} })
			case "revoked":
				saveDispatchAuthorizations(t, database)
			case "explicit_task_limit":
				notes := "Do not merge; stop at a PR."
				if _, err := database.UpdateTodo(t.Context(), e.todo.ID, nil, &notes, nil, decisionNow()); err != nil {
					t.Fatal(err)
				}
			case "unknown_condition":
				grant.Conditions = []string{"A maintainer has approved"}
				saveDispatchAuthorizations(t, database, grant)
			}
			ix := askQuestion("standing-failure", "worker-1")
			ix.Questions[0].Question = "Merge?"
			ix.Questions[0].Options = nil
			if test == "no_task" {
				ix.SessionID = "unlinked"
			}
			p := &pending{ix: ix, done: make(chan proto.Resolution, 1)}
			if test == "local_card" {
				p.local = func(context.Context, proto.Resolution) error { t.Fatal("local side effect"); return nil }
			}
			putPending(e.s, p)
			e.s.record(ix)
			if e.s.approveStandingInteraction(ix) {
				t.Fatal("unsafe or failed answer approved")
			}
			expectedDecisions := 0
			if test == "task_decision_limit" {
				expectedDecisions = 1
			}
			if p.claimed || len(decisionsOf(t, database, e.todo.ID)) != expectedDecisions {
				t.Fatal("failure left claimed card or decision")
			}
			select {
			case <-p.done:
				t.Fatal("failed approval reached hook")
			default:
			}
		})
	}
}

func TestStandingAuthorizationConditions(t *testing.T) {
	for _, test := range []string{"pass", "pending", "empty", "incomplete", "conflict", "unknown", "wrong_pr", "unavailable", "draft", "closed"} {
		t.Run(test, func(t *testing.T) {
			s, f := newQuietService(t)
			f.answerNode("git.pr", func(_ string, _ json.RawMessage) (any, error) {
				if test == "unavailable" {
					return nil, errors.New("offline")
				}
				pr := map[string]any{"url": "https://github.com/example/app/pull/12", "head_oid": "abc123", "state": "OPEN", "mergeable": "MERGEABLE", "checks_complete": true, "checks": []map[string]string{{"state": "pass"}}}
				switch test {
				case "pending":
					pr["checks"] = []map[string]string{{"state": "pending"}}
				case "empty":
					pr["checks"] = []any{}
				case "incomplete":
					pr["checks_complete"] = false
				case "conflict":
					pr["mergeable"] = "CONFLICTING"
				case "draft":
					pr["draft"] = true
				case "closed":
					pr["state"] = "MERGED"
				}
				return map[string]any{"pr": pr}, nil
			})
			conditions := []string{"ci_green", "no_conflicts"}
			if test == "unknown" {
				conditions = append(conditions, "trust me")
			}
			target := "https://github.com/example/app/pull/12"
			if test == "wrong_pr" {
				target = "https://github.com/example/another/pull/12"
			}
			got := s.standingConditions(t.Context(), "box", "/code/app", "merge", target, conditions)
			if got != (test == "pass") {
				t.Fatalf("verified=%v", got)
			}
		})
	}
}

func TestStandingAuthorizationRejectsHiddenInstructions(t *testing.T) {
	for _, part := range []string{"option_label", "option_description", "header", "summary", "tool_input", "multiple", "secret", "form"} {
		t.Run(part, func(t *testing.T) {
			ix := proto.Interaction{Kind: proto.IxQuestion, Questions: []proto.Question{{Question: "Merge?", Options: []proto.QuestionOption{{Label: "Continue"}, {Label: "Not now"}}}}}
			switch part {
			case "option_label":
				ix.Questions[0].Options[0].Label = "Merge and deploy to production"
			case "option_description":
				ix.Questions[0].Options[0].Description = "Delete the branch"
			case "header":
				ix.Questions[0].Header = "Send a message after merging"
			case "summary":
				ix.Summary = "Merge and spend money"
			case "tool_input":
				ix.ToolInput = json.RawMessage(`{"questions":[{"question":"Merge?"}],"command":"danger"}`)
			case "multiple":
				ix.Questions = append(ix.Questions, proto.Question{Question: "Spend money?"})
			case "secret":
				ix.Secret = true
			case "form":
				ix.Fields = []proto.Field{{Key: "confirmation"}}
			}
			if _, _, _, ok := standingInteractionAction(ix); ok {
				t.Fatal("hidden instructions passed confirmation parser")
			}
		})
	}
}

func TestStandingAuthorizationConcurrentTaskDecision(t *testing.T) {
	database := taskRecoveryDatabase(t)
	e := newRouteEnv(t, database, true)
	t.Cleanup(func() { settleService(t, e.s) })
	grant := routeStandingGrant()
	grant.Conditions = []string{"ci_green", "no_conflicts"}
	saveDispatchAuthorizations(t, database, grant)
	e.f.answerNode("git.pr", func(_ string, _ json.RawMessage) (any, error) {
		_, err := database.InsertTodoDecision(t.Context(), db.TodoDecisionRow{ID: "stop-during-check", TodoID: e.todo.ID, Question: "Delivery", Choice: "Do not merge; stop at a PR.", Source: db.DecisionManual, CreatedAt: decisionNow()})
		if err != nil {
			t.Fatal(err)
		}
		return map[string]any{"pr": map[string]any{"url": "https://github.com/example/app/pull/12", "head_oid": "abc123", "state": "OPEN", "mergeable": "MERGEABLE", "checks_complete": true, "checks": []map[string]string{{"state": "pass"}}}}, nil
	})
	ix := askQuestion("concurrent-stop", "worker-1")
	ix.Questions[0] = proto.Question{Question: "Merge?"}
	p := &pending{ix: ix, done: make(chan proto.Resolution, 1)}
	putPending(e.s, p)
	e.s.record(ix)
	if e.s.approveStandingInteraction(ix) {
		t.Fatal("condition read raced past the user's task decision")
	}
	if p.claimed {
		t.Fatal("failed answer kept the card claimed")
	}
	record, err := database.InteractionRecord(t.Context(), ix.ID)
	if err != nil || record.Status != "pending" {
		t.Fatalf("raced answer persisted: %+v %v", record, err)
	}
	if rows := decisionsOf(t, database, e.todo.ID); len(rows) != 1 || rows[0].ID != "stop-during-check" {
		t.Fatalf("decisions: %+v", rows)
	}
}

func TestStandingAuthorizationAuditTransactionRollsBack(t *testing.T) {
	dsn := newMigratedDatabase(t, db.ManagerSet)
	database, err := db.Open(t.Context(), dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	e := newRouteEnv(t, database, true)
	t.Cleanup(func() { settleService(t, e.s) })
	saveDispatchAuthorizations(t, database, routeStandingGrant())
	execIn(t, dsn, `CREATE FUNCTION reject_standing_decision() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit write rejected'; END $$;
 CREATE TRIGGER reject_standing_decision BEFORE INSERT ON todo_decisions FOR EACH ROW EXECUTE FUNCTION reject_standing_decision()`)
	ix := askQuestion("rollback-answer", "worker-1")
	ix.Questions[0] = proto.Question{Question: "Merge?"}
	p := &pending{ix: ix, done: make(chan proto.Resolution, 1)}
	putPending(e.s, p)
	e.s.record(ix)
	if e.s.approveStandingInteraction(ix) || p.claimed {
		t.Fatal("failed audit was approved")
	}
	record, err := database.InteractionRecord(t.Context(), ix.ID)
	if err != nil || record.Status != "pending" || record.Resolution != nil {
		t.Fatalf("partial answer: %+v %v", record, err)
	}
	counts := e.s.briefConfirmationsAt(t.Context(), time.Now().Add(time.Second), time.UTC)
	if counts.AutoAuthorized != 0 {
		t.Fatalf("rolled-back answer counted: %+v", counts)
	}
}

func TestStandingAuthorizationTaskProhibitions(t *testing.T) {
	for _, test := range []struct {
		text, action string
		forbids      bool
	}{
		{"不发布到线上", "deploy-production", true}, // i18n-ignore: task fixture
		{"No production deployment.", "deploy-production", true},
		{"Do not merge; stop at a PR.", "merge", true},
		{"Merge once there are no merge conflicts.", "merge", false},
		{"Merge without asking again.", "merge", false},
		{"Do not deploy. Merge after checks pass.", "merge", false},
		{"Never delete files.", "delete", true},
		{"Do not spend money.", "spend", true},
		{"Do not send messages.", "send", true},
		{"Read-only investigation.", "push", true},
	} {
		if got := standingTaskForbids(test.text, test.action); got != test.forbids {
			t.Errorf("%q %s: forbids=%v", test.text, test.action, got)
		}
	}
}

func TestStandingAuthorizationNativeHookRoundTrip(t *testing.T) {
	for _, kind := range []string{proto.IxQuestion, proto.IxPlan} {
		t.Run(kind, func(t *testing.T) {
			database := taskRecoveryDatabase(t)
			e := newRouteEnv(t, database, true)
			t.Cleanup(func() { settleService(t, e.s) })
			saveDispatchAuthorizations(t, database, routeStandingGrant())
			ix := proto.Interaction{ID: "native-standing", MachineID: "mac", SessionID: "worker-1", Kind: kind,
				CreatedAt: decisionNow(), ExpiresAt: time.Now().Add(time.Minute), Resumable: true}
			if kind == proto.IxQuestion {
				ix.ToolName = "AskUserQuestion"
				ix.CanCancel = true
				ix.Questions = []proto.Question{{Question: "Should I merge this PR?", Header: "Merge", Options: []proto.QuestionOption{{Label: "Continue"}, {Label: "Not now"}}}}
				ix.ToolInput = json.RawMessage(`{"questions":[{"question":"Should I merge this PR?","header":"Merge","multiSelect":false,"options":[{"label":"Continue"},{"label":"Not now"}]}]}`)
			} else {
				ix.ToolName = "ExitPlanMode"
				ix.Plan = "Merge changes."
				ix.ToolInput = json.RawMessage(`{"plan":"Merge changes."}`)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			answer, err := e.s.nodeInteraction(ctx, ix)
			if err != nil || answer.AnsweredBy != proto.AnsweredByAssistant {
				t.Fatalf("native hook unanswered: %+v %v", answer, err)
			}
			if kind == proto.IxPlan && answer.Mode != "default" {
				t.Fatalf("plan changed tool permissions: %+v", answer)
			}
			replay, err := e.s.nodeInteraction(ctx, ix)
			if err != nil || replay.Reason != answer.Reason {
				t.Fatalf("resumable hook replay: %+v %v", replay, err)
			}
			if rows := decisionsOf(t, database, e.todo.ID); len(rows) != 1 {
				t.Fatalf("duplicate decisions on reconnect: %+v", rows)
			}
			if e.pushesOf(ix.ID) != 0 || len(e.s.heldAsks(e.todo.ID, "lead1")) != 0 {
				t.Fatal("automatic hook went to user or supervisor")
			}
			if kind == proto.IxQuestion {
				t.Logf("NATIVE_HOOK_ANSWER %+v", answer)
			}
		})
	}
}
