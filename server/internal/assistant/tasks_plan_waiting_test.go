package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestContinuationOwnerStepsRemainTyped(t *testing.T) {
	for step, want := range map[string]struct {
		source string
		kind   int
	}{
		ownerStepMoney:        {cardPlanMoney, cardCatMoney},
		ownerStepOutward:      {cardPlanOutward, cardCatOutward},
		ownerStepIrreversible: {cardPlanIrreversible, cardCatIrreversible},
	} {
		t.Run(step, func(t *testing.T) {
			e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
				SessionID: "agent01", Status: proto.StatusIdle, State: proto.StateIdle,
			})
			e.s.mainPlansWait = false
			raw, err := json.Marshal(map[string]any{"tasks": []map[string]any{{
				"title": "Continue with owner step", "prompt": "Finish the authorized work.",
				"machine": "box", "cwd": "/code/app", "launcher": "codex", "role": "worker",
				"continues": todo.ID, "continues_why": "Switch launcher", "user_requested": true, "owner_step": step,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			res, err := e.s.toolTaskPlan(context.Background(), "m1", raw)
			if err != nil || !strings.Contains(res, "nothing has started") || len(taskStartMachines(e.f)) != 0 {
				t.Fatalf("owner step bypassed: %s %v", res, err)
			}
			if pushes := e.f.pushed(); len(pushes) != 1 || pushes[0].CardID != planIDRe.FindString(res) {
				t.Fatalf("owner card not pushed once: %+v", pushes)
			}
			if c := cardsToday(t, e.s)[want.source]; c.Category != want.kind || c.N != 1 {
				t.Fatalf("owner step not typed: %+v", c)
			}
			if c := cardsToday(t, e.s)[cardPlanWaiting]; c.N != 0 {
				t.Fatalf("zero-category push: %+v", c)
			}
		})
	}
}

func TestAuthorizedContinuationHardGates(t *testing.T) {
	for _, code := range []string{"permission_escalation", "safety_refusal", "unresolved_approval"} {
		t.Run(code, func(t *testing.T) {
			sess := proto.Session{SessionID: "agent01", Status: proto.StatusIdle, State: proto.StateIdle}
			if code == "safety_refusal" {
				sess.TurnError, sess.TurnErrorKind = "Provider refused this request", proto.TurnErrorUser
				sess.ModelFallback = &proto.ModelRefusal{Subtype: "model_refusal_fallback", OriginalModel: "claude-opus", Category: proto.ModelRecoveryReasonSafeguards, Explanation: "provider safety refusal"}
			}
			if code == "unresolved_approval" {
				sess.Status, sess.State, sess.PendingInteractions = proto.StatusBlocked, proto.StateNeedsYou, 1
			}
			e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", sess)
			e.s.mainPlansWait = false
			permission := "default"
			if code == "permission_escalation" {
				permission = "bypassPermissions"
			}
			raw, err := json.Marshal(map[string]any{"tasks": []map[string]any{{
				"title": "Continue the task", "prompt": "Continue the remaining work.", "machine": "box", "cwd": "/code/app",
				"launcher": "codex", "role": "worker", "permission": permission,
				"continues": todo.ID, "continues_why": "Switch launcher", "user_requested": true,
			}}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = e.s.toolTaskPlan(context.Background(), "m1", raw)
			var blocker handoffBlockerError
			if !errors.As(err, &blocker) || blocker.code != code {
				t.Fatalf("expected %s, got %v", code, err)
			}
			if len(taskStartMachines(e.f)) != 0 || len(e.f.stopped) != 0 || len(e.f.pushed()) != 0 || todoCount(e.s) != 1 {
				t.Fatalf("hard gate had side effects: starts=%v pushes=%+v", taskStartMachines(e.f), e.f.pushed())
			}
			if h := e.s.latestTodoContextHandoff(context.Background(), todo.ID); h != nil {
				t.Fatalf("refused plan generated handoff: %+v", h)
			}
		})
	}
}

func TestNonOwnerPlanWaitsDoNotPush(t *testing.T) {
	t.Run("ordinary_conversation_without_start_authority", func(t *testing.T) {
		s, f := newQuietService(t)
		s.mainPlansWait = false
		f.known["side1"] = true
		res, err := s.toolTaskPlan(context.Background(), "side1", planArgs(false))
		if err != nil || !strings.Contains(res, "nothing has started") || len(taskStartMachines(f)) != 0 || len(f.pushed()) != 0 {
			t.Fatalf("ordinary conversation acquired authority or pushed: %s %v %+v", res, err, f.pushed())
		}
		p, err := s.loadPlan(context.Background(), planIDRe.FindString(res))
		if err != nil || p.State != planOpen {
			t.Fatalf("waiting plan was lost: %+v %v", p, err)
		}
	})

	t.Run("request_prerequisites", func(t *testing.T) {
		e := newTodoEnv(t, nil)
		root := requestTestSetup(t, e)
		e.must("PATCH", "/api/v1/todos/"+root.ID, `{"require_accepted_after":true}`)
		first := requestTestPiece(t, e.s, root, "First prerequisite")
		dependent := requestTestPiece(t, e.s, root, "Dependent work", first.ID)
		e.s.mainPlansWait = false
		plan := requestTestPlan(t, e.s, dependent)
		if plan.State != planOpen || plan.Tasks[0].RequestGate != root.ID || len(taskStartMachines(e.f)) != 0 || len(e.f.pushed()) != 0 {
			t.Fatalf("request gate bypassed or pushed: %+v %+v", plan, e.f.pushed())
		}
		if _, err := e.s.startPlan(context.Background(), plan.ID, nil, false); err == nil {
			t.Fatal("automatic start bypassed request gate")
		}
	})

	t.Run("private_track_confirmation", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01", Status: proto.StatusIdle, State: proto.StateIdle,
		})
		e.s.mainPlansWait = false
		setSettings(t, e.s, func(v *Settings) { v.Leaders = []string{"m1"} })
		e.sessions("server", proto.Session{SessionID: "m1", MachineID: "server", Assistant: true, Space: SpaceHealth, Status: proto.StatusIdle})
		raw, err := json.Marshal(map[string]any{"tasks": []map[string]any{{
			"title": "Continue private work", "prompt": "Continue the existing task.", "machine": "box", "cwd": "/code/app",
			"launcher": "codex", "role": "worker", "continues": todo.ID, "continues_why": "Switch launcher",
		}}})
		if err != nil {
			t.Fatal(err)
		}
		res, err := e.s.toolTaskPlan(context.Background(), "m1", raw)
		if err != nil || !strings.Contains(res, "nothing has started") || len(taskStartMachines(e.f)) != 0 || len(e.f.pushed()) != 0 {
			t.Fatalf("private boundary bypassed or pushed: %s %v %+v", res, err, e.f.pushed())
		}
		if h := e.s.latestTodoContextHandoff(context.Background(), todo.ID); h != nil {
			t.Fatalf("private content handed off before confirmation: %+v", h)
		}
	})
}
