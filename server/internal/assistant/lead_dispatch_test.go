package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A supervisor's identity does not override an isolated track's privacy boundary.
func TestSupervisorSensitiveHandoffStillChecksPrivacyBoundary(t *testing.T) {
	ctx := context.Background()
	s, f, _ := infoUseService(t)
	f.setHello(func(h *Hello) {
		h.Assistant.MachineID, h.Assistant.Machines = "qa-machine", []string{"qa-machine"}
	})
	setSettings(t, s, func(v *Settings) {
		v.Main, v.Leaders, v.MachineID = "qa-main-chat", []string{"qa-health-1"}, "qa-machine"
	})
	f.setSessions("qa-machine", proto.Session{SessionID: "qa-health-1", MachineID: "qa-machine", Assistant: true, Space: SpaceHealth},
		proto.Session{SessionID: "qa-main-chat", MachineID: "qa-machine", Assistant: true})
	if !s.selfPolicy(ctx, "qa-health-1").ConfirmOutbound {
		t.Fatal("the fixture did not install the isolated track policy")
	}
	answerOutboundIn(t, s, "qa-health-1", "agent_send", "summary", "", false)
	ok, _, err := s.confirmOutbound(ctx, "qa-machine", "qa-health-1", "agent_send", "qa: placeholder health data", "summary", nil)
	if err != nil || ok {
		t.Fatalf("isolated track bypassed owner boundary: %v %v", ok, err)
	}
}

// A direction lead's plan starts at once, writable work in a folder the owner never let start by itself included: no
// card, no push. An ordinary conversation's same plan still waits on its card.
func TestLeadPlanStartsAtOnce(t *testing.T) {
	s, f := newQuietService(t)
	ctx := context.Background()
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	f.known["lead1"] = true
	res, err := s.toolTaskPlan(ctx, "lead1", planArgs(false))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res, "without asking") {
		t.Fatalf("the lead's plan waits on a card: %s", res)
	}
	if !strings.Contains(res, "automatic start initiated (delegated by the direction lead)") {
		t.Fatalf("the lead's plan does not say why it started: %s", res)
	}
	waitFor(t, "the lead's task to start", func() bool { return len(onlyTodo(s).Agents) == 1 })
	if got := f.pushed(); len(got) != 0 {
		t.Fatalf("the lead's plan was pushed: %+v", got)
	}

	s2, f2 := newQuietService(t)
	if res, err := s2.toolTaskPlan(ctx, "side1", planArgs(false)); err != nil || !strings.Contains(res, "card") {
		t.Fatalf("an ordinary conversation's writable plan did not wait on its card: %s %v", res, err)
	}
	if got := f2.pushed(); len(got) != 0 {
		t.Fatalf("an ordinary conversation without authority must not push a category-zero plan: %+v", got)
	}
}

func planArgsWithOverrides(t *testing.T, overrides map[string]any) json.RawMessage {
	t.Helper()
	task := map[string]any{
		"title":      "Fix login crash",
		"prompt":     "Fix the crash and add a regression test",
		"machine":    "box",
		"cwd":        "/code/app",
		"role":       "worker",
		"readonly":   false,
		"why":        "repository is on box",
		"permission": "bypassPermissions",
	}
	for k, v := range overrides {
		task[k] = v
	}
	raw, err := json.Marshal(map[string]any{"tasks": []map[string]any{task}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func setupScheduledTurn(f *fakeHub, s *Service, machine, session, sourceKind string, noteActiveRound bool) {
	now := time.Now().UTC()
	src := &proto.MessageSource{Kind: sourceKind, ConversationID: session, Title: "Scheduled check-in"}
	item := proto.Item{
		Seq:  1,
		Role: "user",
		Kind: proto.KindText,
		Text: prompts.MessageSourceText("Check pending work and dispatch tasks", src),
		TS:   now,
	}
	f.answerNode(DeviceSessionItems, func(string, json.RawMessage) (any, error) {
		return map[string]any{"items": []proto.Item{item}, "has_more": false}, nil
	})
	if noteActiveRound {
		s.noteRound(machine, session, "fu_checkin", "", now)
	}
}

// A direction lead or main conversation running its own scheduled follow-up or patrol round still orchestrates on the
// owner's behalf: its task_plan starts automatically whether user_requested is false or true, including after a hub
// restart when only the transcript identifies the scheduled turn. Ordinary unauthorized conversations still wait for
// Start even if they pass user_requested=true.
func TestSupervisorPlanStartsAtOnceInScheduledFollowup(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name            string
		session         string
		isLeader        bool
		mainPlansWait   bool
		sourceKind      string
		noteActiveRound bool
		userRequested   bool
		wantAutoStart   bool
		wantReason      string
	}{
		{
			name:            "lead followup user_requested=false",
			session:         "lead1",
			isLeader:        true,
			sourceKind:      proto.SourceFollowup,
			noteActiveRound: true,
			userRequested:   false,
			wantAutoStart:   true,
			wantReason:      "automatic start initiated (delegated by the direction lead)",
		},
		{
			name:            "lead followup user_requested=true",
			session:         "lead1",
			isLeader:        true,
			sourceKind:      proto.SourceFollowup,
			noteActiveRound: true,
			userRequested:   true,
			wantAutoStart:   true,
			wantReason:      "automatic start initiated (delegated by the direction lead)",
		},
		{
			name:            "lead followup after restart (transcript only)",
			session:         "lead1",
			isLeader:        true,
			sourceKind:      proto.SourceFollowup,
			noteActiveRound: false,
			userRequested:   false,
			wantAutoStart:   true,
			wantReason:      "automatic start initiated (delegated by the direction lead)",
		},
		{
			name:            "lead recurring patrol in own conversation",
			session:         "lead1",
			isLeader:        true,
			sourceKind:      proto.SourcePatrol,
			noteActiveRound: true,
			userRequested:   false,
			wantAutoStart:   true,
			wantReason:      "automatic start initiated (delegated by the direction lead)",
		},
		{
			name:            "main conversation followup round",
			session:         "conv1",
			isLeader:        false,
			mainPlansWait:   false,
			sourceKind:      proto.SourceFollowup,
			noteActiveRound: true,
			userRequested:   false,
			wantAutoStart:   true,
			wantReason:      "automatic start initiated (released by you as their supervisor)",
		},
		{
			name:            "ordinary conversation followup user_requested=false still waits",
			session:         "side1",
			isLeader:        false,
			sourceKind:      proto.SourceFollowup,
			noteActiveRound: true,
			userRequested:   false,
			wantAutoStart:   false,
		},
		{
			name:            "ordinary conversation followup user_requested=true still waits",
			session:         "side1",
			isLeader:        false,
			sourceKind:      proto.SourceFollowup,
			noteActiveRound: true,
			userRequested:   true,
			wantAutoStart:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}
			s, f := newQuietServiceOn(t, st)
			s.mainPlansWait = tc.mainPlansWait
			if tc.isLeader {
				setSettings(t, s, func(v *Settings) { v.Leaders = []string{tc.session} })
			}
			f.mu.Lock()
			f.known[tc.session] = true
			f.mu.Unlock()
			setupScheduledTurn(f, s, "server", tc.session, tc.sourceKind, tc.noteActiveRound)
			if !s.inScheduledTurn(ctx, "server", tc.session) {
				t.Fatalf("expected %s to be recognized as inScheduledTurn", tc.session)
			}

			args := planArgsWithOverrides(t, map[string]any{"user_requested": tc.userRequested})
			res, err := s.toolTaskPlanFrom(ctx, "server", tc.session, args)
			if err != nil {
				t.Fatalf("task_plan: %v", err)
			}
			planID := planIDPattern.FindString(res)
			if planID == "" {
				t.Fatalf("missing plan id in output: %s", res)
			}
			saved, err := s.loadPlan(ctx, planID)
			if err != nil {
				t.Fatalf("loadPlan: %v", err)
			}
			if saved.Tasks[0].UserRequested {
				t.Fatalf("scheduled turn must clear user_requested on saved plan: %+v", saved.Tasks[0])
			}

			if tc.wantAutoStart {
				if !strings.Contains(res, "without asking") || strings.Contains(res, "needs the user's Start") {
					t.Fatalf("expected automatic start, got: %s", res)
				}
				if !strings.Contains(res, tc.wantReason) {
					t.Fatalf("expected reason %q, got: %s", tc.wantReason, res)
				}
				waitFor(t, "scheduled supervisor task to start", func() bool { return len(onlyTodo(s).Agents) == 1 })
				if got := f.pushed(); len(got) != 0 {
					t.Fatalf("auto-started plan should not push: %+v", got)
				}
				return
			}

			if strings.Contains(res, "without asking") || !strings.Contains(res, "needs the user's Start") {
				t.Fatalf("expected Start card for unauthorized conversation, got: %s", res)
			}
			s.todos.mu.RLock()
			started := len(s.todos.todos)
			s.todos.mu.RUnlock()
			if started != 0 {
				t.Fatalf("unauthorized conversation started %d task(s) without Start", started)
			}
			if got := f.pushed(); len(got) != 0 {
				t.Fatalf("missing auto-start authority must not push a category-zero plan: %+v", got)
			}
		})
	}
}

// Guardrails remain intact during a supervisor's scheduled follow-up round: owner_step tasks and health-track leads
// still wait for the owner's Start, while read-only launcher refusals and unavailable machines or launchers are
// refused before creating a plan.
func TestSupervisorFollowupPlanPreservesGuardrails(t *testing.T) {
	ctx := context.Background()

	t.Run("owner_step still waits for Start", func(t *testing.T) {
		for step, want := range map[string]struct {
			source string
			kind   int
		}{
			ownerStepMoney:        {cardPlanMoney, cardCatMoney},
			ownerStepOutward:      {cardPlanOutward, cardCatOutward},
			ownerStepIrreversible: {cardPlanIrreversible, cardCatIrreversible},
		} {
			for _, self := range []string{"lead1", "conv1"} {
				s, f := newQuietService(t)
				s.mainPlansWait = false
				setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
				f.mu.Lock()
				f.known["lead1"] = true
				f.mu.Unlock()
				setupScheduledTurn(f, s, "server", self, proto.SourceFollowup, true)
				if !s.inScheduledTurn(ctx, "server", self) {
					t.Fatalf("%s %s: expected inScheduledTurn", self, step)
				}

				args := planArgsWithOverrides(t, map[string]any{"owner_step": step})
				res, err := s.toolTaskPlanFrom(ctx, "server", self, args)
				if err != nil {
					t.Fatalf("%s %s: %v", self, step, err)
				}
				if strings.Contains(res, "without asking") || !strings.Contains(res, "needs the user's Start") {
					t.Fatalf("%s %s: expected Start card, got: %s", self, step, res)
				}
				s.todos.mu.RLock()
				started := len(s.todos.todos)
				s.todos.mu.RUnlock()
				if started != 0 {
					t.Fatalf("%s %s: started %d task(s) despite owner_step", self, step, started)
				}
				if got := f.pushed(); len(got) != 1 {
					t.Fatalf("%s %s: expected one push, got %+v", self, step, got)
				}
				if c := cardsToday(t, s)[want.source]; c.Category != want.kind || c.N != 1 {
					t.Errorf("%s %s: counted %+v, want %s kind %d once", self, step, c, want.source, want.kind)
				}
			}
		}
	})

	t.Run("health track leader still waits for Start", func(t *testing.T) {
		s, f := newQuietService(t)
		setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead-health"} })
		f.setSessions("server",
			proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
			proto.Session{SessionID: "lead-health", MachineID: "server", Assistant: true, Space: SpaceHealth, Status: proto.StatusIdle},
		)
		f.mu.Lock()
		f.known["lead-health"] = true
		f.mu.Unlock()
		setupScheduledTurn(f, s, "server", "lead-health", proto.SourceFollowup, true)

		res, err := s.toolTaskPlanFrom(ctx, "server", "lead-health", planArgsWithOverrides(t, nil))
		if err != nil {
			t.Fatalf("health leader followup task_plan: %v", err)
		}
		if strings.Contains(res, "without asking") || !strings.Contains(res, "needs the user's Start") {
			t.Fatalf("health leader followup must wait for Start, got: %s", res)
		}
		s.todos.mu.RLock()
		started := len(s.todos.todos)
		s.todos.mu.RUnlock()
		if started != 0 {
			t.Fatalf("health leader started %d task(s) without Start", started)
		}
	})

	t.Run("readonly unsupported launcher refused", func(t *testing.T) {
		s, f := newQuietService(t)
		setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
		f.mu.Lock()
		f.known["lead1"] = true
		f.launcherList = []proto.Launcher{{Name: "pi", Agent: "pi"}}
		f.mu.Unlock()
		setupScheduledTurn(f, s, "server", "lead1", proto.SourceFollowup, true)

		args := planArgsWithOverrides(t, map[string]any{
			"readonly":   true,
			"launcher":   "pi",
			"permission": "",
			"goal":       "inspect repository",
		})
		_, err := s.toolTaskPlanFrom(ctx, "server", "lead1", args)
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != "readonly_unsupported" {
			t.Fatalf("expected readonly_unsupported refusal, got: %v", err)
		}
		s.todos.mu.RLock()
		started := len(s.todos.todos)
		s.todos.mu.RUnlock()
		if started != 0 {
			t.Fatalf("refused readonly plan started %d task(s)", started)
		}
	})

	t.Run("unavailable machine and launcher refused", func(t *testing.T) {
		s, f := newQuietService(t)
		setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
		f.mu.Lock()
		f.known["lead1"] = true
		f.launcherList = []proto.Launcher{{Name: "claude", Agent: "claude"}}
		f.mu.Unlock()
		setupScheduledTurn(f, s, "server", "lead1", proto.SourceFollowup, true)

		if _, err := s.toolTaskPlanFrom(ctx, "server", "lead1", planArgsWithOverrides(t, map[string]any{"machine": "missing-box"})); err == nil || !strings.Contains(err.Error(), "unknown machine") {
			t.Fatalf("expected unknown machine error, got: %v", err)
		}
		if _, err := s.toolTaskPlanFrom(ctx, "server", "lead1", planArgsWithOverrides(t, map[string]any{"launcher": "missing-launcher"})); err == nil {
			t.Fatal("expected unavailable launcher error, got nil")
		}
		s.todos.mu.RLock()
		started := len(s.todos.todos)
		s.todos.mu.RUnlock()
		if started != 0 {
			t.Fatalf("refused plans started %d task(s)", started)
		}
	})
}
