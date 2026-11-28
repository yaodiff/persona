package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// supervisorRef is the direction conversation that supervises the decisionAgentReplyService
// fixture's to-do (newSupervisedService: settings.Main = "conv1" on the assistant machine).
func supervisorRef() SessionRef {
	return SessionRef{MachineID: "server", SessionID: "conv1"}
}

// An exact option label typed in the supervisor / direction conversation settles that
// conversation's own pending decision card and is delivered to the worker Agent, the same way
// an answer on the card or task_decided with told_agent=false is (review P1: the record-only
// path left the worker with no answer even though the card closed).
func TestSupervisorConversationExactChoiceSettles(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	// Use the non-recommended option: a recommended label is recorded with an empty Chosen by
	// the existing "OK takes the recommendation" rule, so the second option asserts Chosen too.
	want := a.Options[1].Label
	sent := len(sentToAgent(f))
	deliveredSupervisorChoice(t, s, supervisorRef(), want, "choice-1")
	got := askOf(s, todo.ID)
	if got == nil || got.AcceptedAt.IsZero() || got.Chosen != want || got.ByDefault {
		t.Fatalf("the supervisor reply did not settle the exact option: %+v", got)
	}
	if delivered := sentToAgent(f)[sent:]; len(delivered) != 1 || !strings.Contains(delivered[0], want) {
		t.Fatalf("the exact supervisor choice was not relayed to the worker: %q", delivered)
	}
	if todoNow(s, todo.ID).Supervision.State == reviewNeedsUser {
		t.Fatal("the decision stayed in needs_user")
	}
	if strings.Contains(strings.Join(pendingKinds(s, todo.ID), ","), sigDecisionReply) ||
		signalled(s, f, todo.ID, sigDecisionReply, "task_decided") {
		t.Fatal("an exact supervisor choice must not emit a hand-back signal")
	}
}

// A lone option letter in the supervisor conversation must not settle the card: that
// conversation is the owner's main chat, where a one-letter message usually answers something
// unrelated. It is treated as free text: the default pauses and the card is handed back.
func TestSupervisorConversationLoneLetterDoesNotSettle(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	s.userSpokeText(context.Background(), supervisorRef(), "B")
	got := askOf(s, todo.ID)
	if got == nil || got.Chosen != "" || !got.AcceptedAt.IsZero() {
		t.Fatalf("a lone letter settled the supervisor card: %+v", got)
	}
	if got.AgentReply == nil || got.DueAt.IsZero() {
		t.Fatalf("a lone letter was ignored instead of handed back: %+v", got)
	}
	if todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatal("the decision left needs_user on a lone letter")
	}
	waitFor(t, "the lone-letter hand-off", func() bool {
		return signalled(s, f, todo.ID, sigDecisionReply, "task_decided")
	})
	// The full label still settles afterwards.
	deliveredSupervisorChoice(t, s, supervisorRef(), askOf(s, todo.ID).Options[1].Label, "choice-2")
	if want := askOf(s, todo.ID).Options[1].Label; askOf(s, todo.ID).Chosen != want {
		t.Fatalf("the full label after the lone letter did not settle %q", want)
	}
}

// Free text in the supervisor conversation pauses the default and hands the decision back to
// that conversation's supervisor via decision_reply, without guessing an option.
func TestSupervisorConversationFreeTextHandsBack(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	s.userSpokeText(context.Background(), supervisorRef(), "let me think about the rollout risk first")
	got := askOf(s, todo.ID)
	if got == nil || got.AgentReply == nil || got.DueAt.IsZero() || got.Chosen != "" || !got.AcceptedAt.IsZero() {
		t.Fatalf("free text was guessed or settled: %+v", got)
	}
	if todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatal("free text must keep the decision in needs_user")
	}
	waitFor(t, "the supervisor hand-off", func() bool {
		return signalled(s, f, todo.ID, sigDecisionReply, "task_decided")
	})
}

// A provenance-marked message reaching the supervisor conversation is not the owner answering:
// it neither settles nor withdraws the pending card.
func TestSupervisorConversationProvenanceIgnored(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	before := askOf(s, todo.ID)
	s.userSpokeText(context.Background(), supervisorRef(),
		prompts.RenderSideConversation("an unrelated request from another conversation"))
	got := askOf(s, todo.ID)
	if got == nil || got.AgentReply != nil || !got.DueAt.Equal(before.DueAt) || got.Chosen != "" {
		t.Fatalf("a side conversation changed the supervisor's pending decision: %+v", got)
	}
	if signalled(s, f, todo.ID, sigDecisionReply, "task_decided") {
		t.Fatal("a side conversation emitted a decision_reply signal")
	}
	if todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatal("the pending decision left needs_user")
	}
	// A genuine owner message right after still settles the card.
	deliveredSupervisorChoice(t, s, supervisorRef(), askOf(s, todo.ID).Options[0].Label, "choice-after-relay")
	if askOf(s, todo.ID).AcceptedAt.IsZero() {
		t.Fatal("the genuine owner reply after provenance input did not settle")
	}
}

// A text-less spoke (a hook with no payload) cannot answer a supervisor card: there are no
// owner words to match or hand back, so the decision is left untouched.
func TestSupervisorConversationEmptyTextIgnored(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	before := askOf(s, todo.ID)
	s.userSpokeText(context.Background(), supervisorRef(), "")
	got := askOf(s, todo.ID)
	if got == nil || got.AgentReply != nil || !got.DueAt.Equal(before.DueAt) {
		t.Fatalf("an empty spoke changed the supervisor decision: %+v", got)
	}
	// The hand-back pauses synchronously, before the spoke returns (handBackOneSupervisorReply), so the checks
	// above see it; no wait is needed (fixed sleeps are a guard failure, docs/lessons/testing.md).
	if signalled(s, f, todo.ID, sigDecisionReply, "task_decided") {
		t.Fatal("an empty spoke emitted a decision_reply signal")
	}
}

// addSecondSupervisedTodo puts another needs_user to-do supervised by conv1 with its own decide
// ask next to the fixture's to-do, so one message faces two open cards at once.
func addSecondSupervisedTodo(t *testing.T, s *Service) Todo {
	t.Helper()
	now := time.Now().UTC()
	second := Todo{
		ID: "todo_supervisor_second", Title: "Choose the rollback plan", Origin: "conv1",
		Supervision: &Supervision{Supervisor: "conv1", State: reviewNeedsUser, Round: 1},
		Agents:      []TodoAgent{},
	}
	putTodo(s, second)
	s.accept.mu.Lock()
	defer s.accept.mu.Unlock()
	if !s.accept.loaded {
		t.Fatal("accept book not loaded")
	}
	s.accept.asks[second.ID] = &acceptAsk{
		Conclusion: true, Outcome: outcomeDecide, Reason: "rollback strategy",
		Recommend: "snapshot", AskedAt: now, DueAt: now.Add(time.Hour),
		Options: []DecisionOption{
			{Label: "snapshot", LeadsTo: "restore a snapshot"},
			{Label: "redeploy", LeadsTo: "ship the previous build"},
		},
	}
	return second
}

// When the same supervisor conversation holds several open cards, even a label that exactly
// matches one card is not applied silently: every open card is handed back for an explicit,
// unambiguous choice, and none is settled.
func TestSupervisorConversationMultipleCardsNotSilentlySettled(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	second := addSecondSupervisedTodo(t, s)
	first := askOf(s, todo.ID)
	// The message exactly names the first card's non-recommended option.
	msg := first.Options[1].Label
	s.userSpokeText(context.Background(), supervisorRef(), msg)
	for _, id := range []string{todo.ID, second.ID} {
		got := askOf(s, id)
		if got == nil || got.AgentReply == nil || !got.AcceptedAt.IsZero() || got.Chosen != "" {
			t.Fatalf("card %s was silently settled instead of handed back: %+v", id, got)
		}
		if todoNow(s, id).Supervision.State != reviewNeedsUser {
			t.Fatalf("card %s left needs_user", id)
		}
	}
	waitFor(t, "both hand-offs", func() bool {
		return signalled(s, f, todo.ID, sigDecisionReply, "task_decided") &&
			signalled(s, f, second.ID, sigDecisionReply, "task_decided")
	})
}

// leaderRef is an independent direction conversation distinct from the main chat (conv1).
func leaderDirectionRef() SessionRef {
	return SessionRef{MachineID: "server", SessionID: "lead1"}
}

// putLeaderDecisionTodo registers a needs_user to-do supervised by lead1 with its own decide
// ask, mirroring a card an independent direction raised through task_plan/task_review.
func putLeaderDecisionTodo(t *testing.T, s *Service) Todo {
	t.Helper()
	now := time.Now().UTC()
	leader := Todo{
		ID: "todo_leader_direction", Title: "Choose the rollback plan", Origin: "lead1",
		Supervision: &Supervision{Supervisor: "lead1", State: reviewNeedsUser, Round: 1},
		Agents:      []TodoAgent{},
	}
	putTodo(s, leader)
	s.accept.mu.Lock()
	defer s.accept.mu.Unlock()
	if !s.accept.loaded {
		t.Fatal("accept book not loaded")
	}
	s.accept.asks[leader.ID] = &acceptAsk{
		Conclusion: true, Outcome: outcomeDecide, Reason: "rollback strategy",
		Recommend: "snapshot", AskedAt: now, DueAt: now.Add(time.Hour),
		Options: []DecisionOption{
			{Label: "snapshot", LeadsTo: "restore a snapshot"},
			{Label: "redeploy", LeadsTo: "ship the previous build"},
		},
	}
	return leader
}

// decisionReplyTarget reads the queued decision_reply signal's explicit To and the conversation
// digestTo would deliver it to, so a regression cannot hide behind the pending-key alone.
func decisionReplyTarget(t *testing.T, s *Service, id string) (to, routed string) {
	t.Helper()
	b := &s.supervise
	b.mu.Lock()
	g, ok := b.pending[id+"|"+sigDecisionReply]
	b.mu.Unlock()
	if !ok {
		t.Fatalf("no pending decision_reply for %s", id)
	}
	return g.To, s.digestTo(context.Background(), s.settings(context.Background()), g)
}

// A free-text reply in an independent direction conversation must hand the pending card back to
// that direction, not the main conversation: the decision_reply signal names the leader
// explicitly, so digestTo no longer falls back to Main for its needs_user card (#12430 retest).
func TestLeaderDirectionFreeTextHandsBackToLeader(t *testing.T) {
	s, f, _ := decisionAgentReplyService(t)
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	leader := putLeaderDecisionTodo(t, s)
	s.userSpokeText(context.Background(), leaderDirectionRef(), "let me think about the rollback risk first")
	waitFor(t, "the leader hand-off", func() bool {
		return signalled(s, f, leader.ID, sigDecisionReply, "task_decided")
	})
	to, routed := decisionReplyTarget(t, s, leader.ID)
	if to != "lead1" || routed != "lead1" {
		t.Fatalf("free text routed to %q (To %q), want the lead1 direction", routed, to)
	}
	if got := askOf(s, leader.ID); got == nil || got.AgentReply == nil || got.Chosen != "" || !got.AcceptedAt.IsZero() {
		t.Fatalf("free text was guessed or settled: %+v", got)
	}
	if todoNow(s, leader.ID).Supervision.State != reviewNeedsUser {
		t.Fatal("free text must keep the decision in needs_user")
	}
}

// A lone letter in an independent direction is free text there (the P1 fix), and its hand-back
// must also reach the direction that owns the card rather than the main conversation.
func TestLeaderDirectionLoneLetterHandsBackToLeader(t *testing.T) {
	s, f, _ := decisionAgentReplyService(t)
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	leader := putLeaderDecisionTodo(t, s)
	s.userSpokeText(context.Background(), leaderDirectionRef(), "B")
	waitFor(t, "the lone-letter leader hand-off", func() bool {
		return signalled(s, f, leader.ID, sigDecisionReply, "task_decided")
	})
	to, routed := decisionReplyTarget(t, s, leader.ID)
	if to != "lead1" || routed != "lead1" {
		t.Fatalf("lone letter routed to %q (To %q), want the lead1 direction", routed, to)
	}
	if got := askOf(s, leader.ID); got == nil || got.Chosen != "" || got.AgentReply == nil || !got.AcceptedAt.IsZero() {
		t.Fatalf("a lone letter settled the leader card: %+v", got)
	}
}

// A card supervised by the main conversation keeps the legacy no-To routing, so an active
// background supervisor still receives its hand-back the way it did before.
func TestMainConversationDecisionReplyKeepsLegacyRouting(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t) // settings.Main = conv1, the fixture's supervisor
	s.userSpokeText(context.Background(), supervisorRef(), "let me think it over")
	waitFor(t, "the main-conversation hand-off", func() bool {
		return signalled(s, f, todo.ID, sigDecisionReply, "task_decided")
	})
	to, _ := decisionReplyTarget(t, s, todo.ID)
	if to != "" {
		t.Fatalf("main-conversation task got explicit To %q; legacy no-To routing changed", to)
	}
}

// An exact choice in an independent direction settles the card and reaches the worker Agent,
// while the direction keeps no spurious hand-back signal. It reuses the standard fixture, whose
// to-do has a live worker session, and moves its supervision to a leader first.
func TestLeaderDirectionExactChoiceDeliversToWorker(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].Supervision.Supervisor = "lead1"
	s.todos.mu.Unlock()
	want := askOf(s, todo.ID).Options[1].Label
	sent := len(sentToAgent(f))
	deliveredSupervisorChoice(t, s, leaderDirectionRef(), want, "leader-choice-1")
	if delivered := sentToAgent(f)[sent:]; len(delivered) != 1 || !strings.Contains(delivered[0], want) {
		t.Fatalf("the leader's exact choice was not relayed to the worker: %q", delivered)
	}
	if got := askOf(s, todo.ID); got == nil || got.AcceptedAt.IsZero() || got.Chosen != want {
		t.Fatalf("the leader exact choice did not settle: %+v", got)
	}
	if todoNow(s, todo.ID).Supervision.State == reviewNeedsUser {
		t.Fatal("the leader decision stayed in needs_user")
	}
	if signalled(s, f, todo.ID, sigDecisionReply, "task_decided") {
		t.Fatal("an exact leader choice emitted a hand-back signal")
	}
}

// An owner reply in the task's own Agent chat keeps the shared, legacy routing even when an
// independent leader supervises the task: its decision_reply carries no explicit To, so
// digestTo's needs_user escalation to the main conversation and digestTarget's runtime-policy
// handling still apply (review P1: the supervisor-conversation fix must not reroute this path).
func TestAgentChatReplyOnLeaderTaskKeepsLegacyRouting(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].Supervision.Supervisor = "lead1"
	s.todos.mu.Unlock()
	a := askOf(s, todo.ID)
	s.userSpokeText(context.Background(), SessionRef{MachineID: a.MachineID, SessionID: a.SessionID},
		"let me think about the rollout risk first")
	waitFor(t, "the Agent-chat hand-off", func() bool {
		return signalled(s, f, todo.ID, sigDecisionReply, "task_decided")
	})
	b := &s.supervise
	b.mu.Lock()
	g, ok := b.pending[todo.ID+"|"+sigDecisionReply]
	b.mu.Unlock()
	if !ok {
		t.Fatal("no pending decision_reply for the Agent-chat reply")
	}
	if g.To != "" {
		t.Fatalf("the Agent-chat reply was rerouted to %q; legacy no-To escalation changed", g.To)
	}
}

// A supervisor-conversation hand-back must keep routing to its owning direction across a manager
// restart. The persisted reply records its supervisor origin; the recovery sweep rebuilds the
// explicit leader destination instead of the shared no-To signal that would escalate a
// needs_user card back to Main.
//
// Both managers hold their digest (heldSuperviseTimings): the restart comes while the hand-back
// is still untold, and the test reads what the sweep queued. With a 20 ms gather, a loaded
// machine let the first manager's digest tell it before the restart, so the sweep rightly had
// nothing to recover, or let the new manager's digest take the queued signal first (#17829).
func TestSupervisorHandBackRoutingSurvivesRestart(t *testing.T) {
	st := &memStore{}
	first, f := newQuietServiceTimed(t, st, heldSuperviseTimings())
	setSettings(t, first, func(v *Settings) { v.Leaders = []string{"lead1"} })
	first.accept.mu.Lock()
	if !first.loadAcceptLocked(context.Background()) {
		first.accept.mu.Unlock()
		t.Fatal("the accept book could not be loaded")
	}
	first.accept.mu.Unlock()
	leader := putLeaderDecisionTodo(t, first)
	first.userSpokeText(context.Background(), leaderDirectionRef(), "let me think about the rollout risk first")
	waitFor(t, "the leader hand-off", func() bool {
		return signalled(first, f, leader.ID, sigDecisionReply, "task_decided")
	})
	if got := askOf(first, leader.ID); got == nil || got.AgentReply == nil || !got.AgentReply.FromSupervisor {
		t.Fatalf("the supervisor origin was not recorded on the persisted reply: %+v", got)
	}
	// A brand-new manager over the same store: nothing is carried in memory.
	reloaded, _ := newQuietServiceTimed(t, st, heldSuperviseTimings())
	setSettings(t, reloaded, func(v *Settings) { v.Leaders = []string{"lead1"} })
	putTodo(reloaded, Todo{ID: leader.ID, Title: leader.Title, Origin: "lead1",
		Supervision: &Supervision{Supervisor: "lead1", State: reviewNeedsUser, Round: 1}})
	if err := reloaded.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	reloaded.sweepAgentDecisionReplies()
	to, routed := decisionReplyTarget(t, reloaded, leader.ID)
	if to != "lead1" || routed != "lead1" {
		t.Fatalf("after restart the hand-back routed to %q (To %q), want the lead1 direction", routed, to)
	}
}

// A can_wait decision held for a briefing has no card and the owner has never seen this round.
// Any message in its supervisor conversation must leave the hold, the card and the ask untouched:
// neither release it for hand-back nor settle an option label.
func TestHeldForBriefingDecisionIgnoresSupervisorConversation(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	s.accept.mu.Lock()
	a := s.accept.asks[todo.ID]
	if a == nil {
		s.accept.mu.Unlock()
		t.Fatal("no decide ask to hold for a briefing")
	}
	a.ForBriefing, a.NoCard = true, true
	s.accept.mu.Unlock()
	if !s.waitsForBriefing(todo.ID) {
		t.Fatal("the fixture ask is not held for a briefing")
	}
	before := askOf(s, todo.ID)
	// An exact option label would settle an ordinary card once delivered; here it must do nothing.
	deliveredSupervisorChoice(t, s, supervisorRef(), before.Options[1].Label, "briefing-choice-1")
	got := askOf(s, todo.ID)
	if got == nil || !got.ForBriefing || got.AgentReply != nil || got.Chosen != "" || !got.AcceptedAt.IsZero() {
		t.Fatalf("a briefing-held decision was released or settled: %+v", got)
	}
	if s.supervisorSettlePending(todo.ID, time.Now()) {
		t.Fatal("a briefing-held decision registered a pending supervisor settlement")
	}
	if signalled(s, f, todo.ID, sigDecisionReply, "task_decided") {
		t.Fatal("a briefing-held decision emitted a hand-back signal")
	}
	if todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatal("the held decision left needs_user")
	}
}

// An active "Don't handle" dismissal hides the round from the supervisor reverse-lookup: neither
// an exact option label nor free text may reopen, hand back or settle it. Undoing the dismissal
// restores normal answering.
func TestDismissedDecisionIgnoresSupervisorConversation(t *testing.T) {
	// A real store is required: with no store, chooseOption fails on errAcceptStore before it can
	// deliver or record, which would mask whether the dismissal gate actually blocks settlement.
	// The fixture is built by hand because memStore has no task_plans table for the plan flow.
	st := &memStore{}
	s, f := newQuietServiceOn(t, st)
	now := time.Now().UTC()
	todo := Todo{
		ID: "todo_supervisor_dismissed", Title: "Choose the rollback plan", Origin: "conv1",
		Supervision: &Supervision{Supervisor: "conv1", State: reviewNeedsUser, Round: 1,
			Reviews: []TaskReview{{At: now, Verdict: reviewNeedsUser, Note: "needs an owner choice"}}},
	}
	putTodo(s, todo)
	s.accept.mu.Lock()
	if !s.loadAcceptLocked(context.Background()) {
		s.accept.mu.Unlock()
		t.Fatal("the accept book could not be loaded")
	}
	s.accept.asks[todo.ID] = &acceptAsk{
		Conclusion: true, Outcome: outcomeDecide, Reason: "rollback strategy",
		Recommend: "snapshot", AskedAt: now, DueAt: now.Add(time.Hour),
		Options: []DecisionOption{
			{Label: "snapshot", LeadsTo: "restore a snapshot"},
			{Label: "redeploy", LeadsTo: "ship the previous build"},
		},
	}
	s.accept.mu.Unlock()
	review := triageLatestReview(todoNow(s, todo.ID).Supervision)
	if review == nil {
		t.Fatal("the fixture has no needs_user review")
	}
	dismiss := func(undone bool) {
		s.reviewDismissals.mu.Lock()
		s.reviewDismissals.loaded = true
		s.reviewDismissals.byTodo = map[string]taskReviewDismissal{
			todo.ID: {decisionReview: decisionReview{ReviewAt: review.At, DismissalID: "dismiss-1"}, Undone: undone},
		}
		s.reviewDismissals.mu.Unlock()
	}
	dismiss(false)
	want := "redeploy" // the non-recommended option, so chooseOption takes its full delivery path
	deliveredSupervisorChoice(t, s, supervisorRef(), want, "dismissed-choice-1")
	if got := askOf(s, todo.ID); got == nil || got.Chosen != "" || !got.AcceptedAt.IsZero() || got.AgentReply != nil {
		t.Fatalf("an exact label reopened a dismissed decision: %+v", got)
	}
	if s.supervisorSettlePending(todo.ID, time.Now()) {
		t.Fatal("a dismissed decision registered a pending supervisor settlement")
	}
	s.userSpokeText(context.Background(), supervisorRef(), "let me think about the rollout risk first")
	if got := askOf(s, todo.ID); got == nil || got.AgentReply != nil || got.Chosen != "" {
		t.Fatalf("free text handed back a dismissed decision: %+v", got)
	}
	if signalled(s, f, todo.ID, sigDecisionReply, "task_decided") {
		t.Fatal("a dismissed decision emitted a hand-back signal")
	}
	if todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatal("the dismissed decision left needs_user")
	}
	// Undo restores the ordinary path: the same exact label now settles the round once delivered.
	dismiss(true)
	deliveredSupervisorChoice(t, s, supervisorRef(), want, "dismissed-choice-2")
	if got := askOf(s, todo.ID); got == nil || got.AcceptedAt.IsZero() || got.Chosen != want {
		t.Fatalf("the decision did not answer after Undo: %+v", got)
	}
}
