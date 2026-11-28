package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// A card ends one of three ways as far as the user is concerned: they allowed it, they declined it, or nobody answered
// it. Only the user's own allow or decline on a device is told or kept as theirs. A card that expired, was withdrawn or
// cleared, or that someone else answered for them (the assistant, a supervisor, a default, the terminal) is never told
// to the model as the user declining it, and never kept as the user's decision.

// endOutbound ends the next confirmation card conversation self raises for tool with r, as whatever ends it would
// (its expiry, its withdrawal, someone answering for the user).
func endOutbound(t *testing.T, s *Service, self, tool string, r proto.Resolution) {
	t.Helper()
	go func() {
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); <-tick.C {
			if qs := s.pendingQuestions(); len(qs) == 1 {
				ix := qs[0]
				if ix.Kind != proto.IxPermission || ix.ToolName != tool || ix.SessionID != self {
					t.Errorf("card: %+v", ix)
				}
				raw, _ := json.Marshal(r)
				if _, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: ix.ID, Answer: raw}); err != nil {
					t.Error(err)
				}
				return
			}
		}
		t.Error("no card on the phone")
	}()
}

// unansweredEnds are the ways a card ends without the user's own answer, as the one waiting on it gets them.
func unansweredEnds() map[string]proto.Resolution {
	no := false
	return map[string]proto.Resolution{
		"expired":           {Dismiss: true, Reason: proto.DismissedExpired},
		"withdrawn":         {Dismiss: true, Reason: proto.DismissedWithdrawn},
		"cleared":           {Dismiss: true, Reason: proto.DismissedCleared},
		"denied for them":   {Allow: &no, AnsweredBy: proto.AnsweredByAssistant},
		"closed, no choice": {Dismiss: true},
	}
}

// saysNobodyAnswered: out tells the model nobody answered, never that the user declined.
func saysNobodyAnswered(out string) bool {
	return !strings.Contains(out, "declined") && strings.Contains(out, "did NOT decline")
}

// healthOutboundHub is healthHub with a coding agent in the health space and one outside it.
func healthOutboundHub() *fakeHub {
	f := healthHub()
	f.sessions["server"] = append(f.sessions["server"],
		proto.Session{SessionID: "code-0000-0000", Cwd: "/home/me/src/app", Agent: "claude", Status: proto.StatusIdle, Title: "app"},
		proto.Session{SessionID: "health-agent", Cwd: "/home/me/.persona/health/work", Agent: "claude", Status: proto.StatusIdle,
			Title: "health Agent"})
	return f
}

// The card agent_send raised in the health space expired (or was withdrawn, cleared, or answered for the user): nothing
// is sent, and the model is told nobody answered, not that the user declined. The user's own decline still says so.
func TestAgentSendExpiredCardIsNotDeclined(t *testing.T) {
	f := healthOutboundHub()
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })
	args := `{"session_id":"health-agent","text":"把我的睡眠数据画成图"}`
	for name, r := range unansweredEnds() {
		endOutbound(t, s, "sleep-000-0000", "agent_send", r)
		out, isErr := spaceTool(t, s, "sleep-000-0000", "agent_send", args)
		if isErr || !saysNobodyAnswered(out) || len(sentToAgent(f)) != 0 {
			t.Fatalf("%s: %s, sent %q", name, out, sentToAgent(f))
		}
	}
	answerOutbound(t, s, "sleep-000-0000", "agent_send", "把我的睡眠数据画成图", false)
	if out, isErr := spaceTool(t, s, "sleep-000-0000", "agent_send", args); isErr ||
		!strings.Contains(out, "The user declined on the phone: nothing was sent to that agent.") || len(sentToAgent(f)) != 0 {
		t.Fatalf("the user's decline: %s, sent %q", out, sentToAgent(f))
	}
}

// agent_start and agent_interrupt say the same: a card nobody answered started or stopped nothing, and is no decline.
func TestOutboundToolsUnansweredAreNotDeclined(t *testing.T) {
	f := healthOutboundHub()
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "main-0000-0000" })
	for name, r := range unansweredEnds() {
		endOutbound(t, s, "sleep-000-0000", "agent_start", r)
		if out, _ := spaceTool(t, s, "sleep-000-0000", "agent_start", `{"machine":"server","cwd":"/home/me/src/app","prompt":"整理体检报告"}`); !saysNobodyAnswered(out) || len(f.starts) != 0 {
			t.Fatalf("agent_start, %s: %s, starts %v", name, out, f.starts)
		}
		endOutbound(t, s, "sleep-000-0000", "agent_interrupt", r)
		out, err := s.runTool(context.Background(), "server", "sleep-000-0000", "agent_interrupt",
			json.RawMessage(`{"session_id":"health-agent","then":"先停一下"}`))
		if err != nil || !saysNobodyAnswered(out) || len(f.interrupted()) != 0 {
			t.Fatalf("agent_interrupt, %s: %s %v, interrupted %v", name, out, err, f.interrupted())
		}
	}
}

// What each tool says when its card did not let it go: the user's decline only for their own decline on a device.
func TestOutboundNotDoneWording(t *testing.T) {
	if got := outboundNotDone(proto.OutcomeDeclined, "The user declined on the phone: no agent was started.", "no agent was started"); got != "The user declined on the phone: no agent was started." {
		t.Fatalf("the user's decline: %q", got)
	}
	if out, err := todoNotAllowed(proto.OutcomeDeclined, nil); err != nil || out != "The user did not allow it on the phone: nothing changed." {
		t.Fatalf("todo, the user's decline: %q %v", out, err)
	}
	boom := errors.New("boom")
	if _, err := todoNotAllowed(proto.OutcomeExpired, boom); !errors.Is(err, boom) {
		t.Fatalf("an error is the error: %v", err)
	}
	for _, outcome := range []string{proto.OutcomeExpired, proto.OutcomeWithdrawn, proto.OutcomeCleared, proto.OutcomeDismissed,
		proto.OutcomeUnanswered, proto.OutcomeByAssistant, proto.OutcomeByDefault, proto.OutcomeByTerminal} {
		if got := outboundNotDone(outcome, "The user declined on the phone: nothing was sent to that agent.", "nothing was sent to that agent"); !saysNobodyAnswered(got) ||
			!strings.Contains(got, "nothing was sent to that agent") {
			t.Errorf("agent_send, %s: %q", outcome, got)
		}
		if out, err := todoNotAllowed(outcome, nil); err != nil || !saysNobodyAnswered(out) || strings.Contains(out, "did not allow") {
			t.Errorf("todo, %s: %q %v", outcome, out, err)
		}
		if err := a2aUnanswered(outcome); errors.Is(err, errA2ADeclined) || !saysNobodyAnswered(err.Error()) {
			t.Errorf("a2a_send, %s: %v", outcome, err)
		}
	}
}

// await says why a card ended unanswered (proto.Resolution.Reason): its time ran out, or the one waiting went away. The
// history keeps who or what ended each card (interactions.ended_by): its expiry, its withdrawal, a device, or the
// assistant answering for the user.
func TestAwaitExpiryCarriesReason(t *testing.T) {
	ctx := context.Background()
	ft := newFleetTest(t)

	r, err := ft.s.waitInteraction(ctx, proto.Interaction{ID: "ix_exp", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, OutwardWrite: true,
		ExpiresAt: time.Now().Add(50 * time.Millisecond)})
	if err != nil || !r.Dismiss || r.Reason != proto.DismissedExpired || r.Outcome() != proto.OutcomeExpired {
		t.Fatalf("expired: %+v %v", r, err)
	}
	if status, _, _ := ft.st.InteractionOutcome(ctx, "ix_exp"); status != "expired" || ft.st.endedAs("ix_exp") != db.EndedByExpired {
		t.Fatalf("expired: history %q, ended by %q", status, ft.st.endedAs("ix_exp"))
	}

	cctx, cancel := context.WithCancel(ctx)
	done := make(chan proto.Resolution, 1)
	go func() {
		r, _ := ft.s.waitInteraction(cctx, proto.Interaction{ID: "ix_gone", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, OutwardWrite: true,
			ExpiresAt: time.Now().Add(time.Hour)})
		done <- r
	}()
	ft.waitPending(1)
	cancel()
	select {
	case r := <-done:
		if !r.Dismiss || r.Reason != proto.DismissedWithdrawn || r.Outcome() != proto.OutcomeWithdrawn || r.UserDenied() {
			t.Fatalf("withdrawn: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("withdrawn: waitInteraction did not return")
	}
	if status, _, _ := ft.st.InteractionOutcome(ctx, "ix_gone"); status != "cancelled" || ft.st.endedAs("ix_gone") != db.EndedByWithdrawn {
		t.Fatalf("withdrawn: history %q, ended by %q", status, ft.st.endedAs("ix_gone"))
	}

	// The user's answer on a device, through the apps' route: theirs.
	go func() {
		r, _ := ft.s.waitInteraction(ctx, proto.Interaction{ID: "ix_dev", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, OutwardWrite: true,
			ExpiresAt: time.Now().Add(time.Hour)})
		done <- r
	}()
	ft.waitPending(1)
	w := httptest.NewRecorder()
	ft.s.fleetHandler(http.NotFoundHandler()).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/interactions/ix_dev/resolve",
		strings.NewReader(`{"allow":false}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("resolve: %d %s", w.Code, w.Body)
	}
	if r := <-done; r.Outcome() != proto.OutcomeDeclined || !r.UserDenied() {
		t.Fatalf("the user's decline: %+v", r)
	}
	if by := ft.st.endedAs("ix_dev"); by != db.EndedByDevice {
		t.Fatalf("the user's decline ended by %q", by)
	}

	// The assistant answering for the user: not theirs.
	go func() {
		r, _ := ft.s.waitInteraction(ctx, proto.Interaction{ID: "ix_for", MachineID: "mac", SessionID: "s1", Kind: proto.IxPermission, OutwardWrite: true,
			ExpiresAt: time.Now().Add(time.Hour)})
		done <- r
	}()
	ft.waitPending(1)
	no := false
	if err := ft.s.resolve("ix_for", proto.Resolution{Allow: &no, AnsweredBy: proto.AnsweredByAssistant}); err != nil {
		t.Fatal(err)
	}
	if r := <-done; r.Outcome() != proto.OutcomeByAssistant || r.UserDenied() {
		t.Fatalf("answered for the user: %+v", r)
	}
	if by := ft.st.endedAs("ix_for"); by != db.EndedByAssistant {
		t.Fatalf("answered for the user, ended by %q", by)
	}
}

// A dismissal the manager says was a withdrawal is no "the user closed the password card"; nor is one it says expired,
// whenever it comes.
func TestSecretRunWithdrawnIsNotTheUsersClose(t *testing.T) {
	expires := time.Now().Add(10 * time.Minute)
	for _, reason := range []string{proto.DismissedWithdrawn, proto.DismissedCleared} {
		status, message := secretRunDismissal(proto.Resolution{Dismiss: true, Reason: reason}, time.Now(), expires)
		if status != secretExpired || strings.Contains(message, "The user closed") || !strings.Contains(message, "did NOT decline") {
			t.Errorf("%s: %s %q", reason, status, message)
		}
	}
	if status, _ := secretRunDismissal(proto.Resolution{Dismiss: true, Reason: proto.DismissedExpired}, time.Now(), expires); status != secretExpired {
		t.Errorf("expired early by another clock: %s", status)
	}
	if status, _ := secretRunDismissal(proto.Resolution{Dismiss: true}, time.Now(), expires); status != secretDeclined {
		t.Errorf("closed by the user: %s", status)
	}
}

// Only the user's own answer on a card is kept as a decision of the task: not a recommendation taken at its deadline,
// the terminal's, the assistant's, nor an answer that came after the card expired.
func TestCardDecisionsOnlyTheUsersOwnAnswer(t *testing.T) {
	database := todoTestDB(t)
	e := newRouteEnv(t, database, true)
	ctx := context.Background()
	q := askQuestion("ixq0", "worker-1")
	answer := map[string]string{"用哪个分支？": "main"}
	for i, r := range []proto.Resolution{
		{Answers: answer, AnsweredBy: proto.AnsweredByDefault, Late: true},
		{Answers: answer, AnsweredBy: proto.AnsweredByTerminal},
		{Answers: answer, AnsweredBy: proto.AnsweredByAssistant},
		{Answers: answer, Late: true},
	} {
		q.ID = "ixq" + string(rune('1'+i))
		e.s.cardDecisions(ctx, q, r, "resolved")
		if list, _ := database.ListTodoDecisions(ctx, e.todo.ID, false); len(list) != 0 {
			t.Fatalf("%+v became a decision: %+v", r, list)
		}
	}
	q.ID = "ixq9"
	e.s.cardDecisions(ctx, q, proto.Resolution{Answers: answer}, "resolved")
	if list, _ := database.ListTodoDecisions(ctx, e.todo.ID, false); len(list) != 1 || list[0].Source != db.DecisionCard || list[0].Choice != "main" {
		t.Fatalf("the user's own answer: %+v", list)
	}
}

// An escalated approval answered for the user (the assistant's interaction_answer) is not kept as the user's decision
// on the card; the user's own is.
func TestEscalatedAnswerForTheUserIsNoDecision(t *testing.T) {
	database := todoTestDB(t)
	e := newRouteEnv(t, database, true)
	ctx := context.Background()
	escalate := func(id string) proto.Interaction {
		e.s.fleet.mu.Lock()
		if e.s.askUp.escalated == nil {
			e.s.askUp.escalated = map[string]escalatedCard{}
		}
		e.s.askUp.escalated[id] = escalatedCard{todo: e.todo.ID, at: time.Now()}
		e.s.fleet.mu.Unlock()
		return askPermission(id, "worker-1")
	}
	yes := true
	e.s.escalatedResolved(ctx, escalate("ixe1"), proto.Resolution{Allow: &yes, AnsweredBy: proto.AnsweredByAssistant}, "resolved")
	if list, _ := database.ListTodoDecisions(ctx, e.todo.ID, false); len(list) != 0 {
		t.Fatalf("an answer for the user became a decision: %+v", list)
	}
	e.s.escalatedResolved(ctx, escalate("ixe2"), proto.Resolution{Allow: &yes}, "resolved")
	if list, _ := database.ListTodoDecisions(ctx, e.todo.ID, false); len(list) != 1 || list[0].Source != db.DecisionCard || list[0].SourceMessage != "ixe2" {
		t.Fatalf("the user's own approval: %+v", list)
	}
}
