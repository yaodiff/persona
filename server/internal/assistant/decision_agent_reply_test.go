package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func decisionAgentReplyService(t *testing.T) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f, todo := newSupervisedService(t)
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	waitsForReview(t, s, f, todo.ID, "The agent needs a choice between two rollout options.")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	return s, f, todo
}

func TestUserSpokePausesPendingAgentDecision(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	sent := len(sentToAgent(f))
	s.UserSpoke(context.Background(), SessionRef{MachineID: a.MachineID, SessionID: a.SessionID})
	if got := askOf(s, todo.ID); got.AgentReply == nil || got.DueAt.IsZero() {
		t.Fatalf("the owner message left the automatic default enabled: %+v", got)
	}
	waitFor(t, "the supervisor handoff", func() bool {
		a := askOf(s, todo.ID)
		return a != nil && a.AgentReply != nil && !a.AgentReply.ToldAt.IsZero()
	})
	got := askOf(s, todo.ID)
	if got == nil || got.AgentReply == nil || !got.DueAt.After(got.AskedAt) || got.AgentReply.MachineID != a.MachineID ||
		got.AgentReply.SessionID != a.SessionID || got.AgentReply.At.IsZero() || got.AgentReply.ToldAt.IsZero() {
		t.Fatalf("reply was not recorded and paused: %+v", got)
	}
	if !got.AcceptedAt.IsZero() || got.ByDefault || got.Chosen != "" || todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatalf("the answer was guessed or the ask ended: ask %+v state %q", got, todoNow(s, todo.ID).Supervision.State)
	}
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now().Add(48 * time.Hour))
	if result, err := s.acceptTryAs(context.Background(), todo.ID, "", true); err != nil || result.taken {
		t.Fatalf("an already-collected deadline took the default: %+v %v", result, err)
	}
	if got := sentToAgent(f)[sent:]; len(got) != 0 || todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatalf("sweep chose an option or ended the ask: sent %q, state %q", got, todoNow(s, todo.ID).Supervision.State)
	}
	waitFor(t, "the rendered decision directive", func() bool {
		return strings.Contains(strings.Join(f.notices(), "\n"), "task_decided")
	})
}

func TestUserPromptSubmitPausesPendingAgentDecision(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	params, _ := json.Marshal(map[string]any{"session_id": a.SessionID, "event": "UserPromptSubmit", "prompt_source": "user"})
	if !s.NodeEvent(a.MachineID, "hook.event", params) {
		t.Fatal("hook event was not accepted")
	}
	waitFor(t, "the decision hook to pause", func() bool {
		got := askOf(s, todo.ID)
		return got != nil && got.AgentReply != nil && got.AgentReply.MachineID == a.MachineID && got.AgentReply.SessionID == a.SessionID && !got.DueAt.IsZero()
	})
	waitFor(t, "the decision reply signal", func() bool { return signalled(s, f, todo.ID, sigDecisionReply, "task_decided") })
	if todoNow(s, todo.ID).Supervision.State != reviewNeedsUser || askOf(s, todo.ID).Chosen != "" {
		t.Fatal("hook guessed an option or ended the pending decision")
	}
}

// An accept holds its todo's action lock across session.send. Its own UserPromptSubmit must not wait for that lock on
// the manager's read loop: the event returns now and the ordered decision work runs once the send has completed.
func TestDecisionPromptDoesNotBlockOnAcceptActionLock(t *testing.T) {
	s, _, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	params, _ := json.Marshal(map[string]any{"session_id": a.SessionID, "event": "UserPromptSubmit", "prompt_source": "user"})
	s.reviewDismissals.actions.acquire(todo.ID)
	locked := true
	defer func() {
		if locked {
			s.reviewDismissals.actions.release(todo.ID)
		}
	}()
	answered := make(chan bool, 1)
	go func() { answered <- s.NodeEvent(a.MachineID, "hook.event", params) }()
	select {
	case accepted := <-answered:
		if !accepted {
			t.Fatal("hook event was not accepted")
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("NodeEvent waited for the accept action lock on the read loop")
	}
	s.reviewDismissals.actions.release(todo.ID)
	locked = false
	waitFor(t, "the ordered decision hook", func() bool {
		got := askOf(s, todo.ID)
		return got != nil && got.AgentReply != nil && !got.DueAt.IsZero()
	})
}

func TestDecisionAgentReplyIgnoresUntrustedOrStaleEvents(t *testing.T) {
	for _, tc := range []struct {
		name, machine, session, event, source string
		spooledOld                            bool
	}{
		{name: "unrelated session", machine: "box", session: "another-session", event: "UserPromptSubmit", source: "user"},
		{name: "wrong machine", machine: "another-machine", session: "s1", event: "UserPromptSubmit", source: "user"},
		{name: "missing source", machine: "box", session: "s1", event: "UserPromptSubmit"},
		{name: "task source", machine: "box", session: "s1", event: "UserPromptSubmit", source: "task"},
		{name: "system source", machine: "box", session: "s1", event: "UserPromptSubmit", source: "system"},
		{name: "relay source", machine: "box", session: "s1", event: "UserPromptSubmit", source: "relay"},
		{name: "stop event", machine: "box", session: "s1", event: "Stop", source: "user"},
		{name: "old replay", machine: "box", session: "s1", event: "UserPromptSubmit", source: "user", spooledOld: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := decisionAgentReplyService(t)
			a := askOf(s, todo.ID)
			if tc.machine == "box" && tc.session == "s1" {
				tc.machine, tc.session = a.MachineID, a.SessionID
			}
			p := map[string]any{"session_id": tc.session, "event": tc.event}
			if tc.source != "" {
				p["prompt_source"] = tc.source
			}
			if tc.spooledOld {
				p["spooled_at"] = a.AskedAt.Add(-time.Second).UTC().Format(time.RFC3339Nano)
			}
			params, _ := json.Marshal(p)
			s.NodeEvent(tc.machine, "hook.event", params)
			if tc.name == "unrelated session" {
				s.UserSpoke(context.Background(), SessionRef{MachineID: tc.machine, SessionID: tc.session})
			}
			if tc.name == "wrong machine" {
				s.UserSpoke(context.Background(), SessionRef{MachineID: tc.machine, SessionID: a.SessionID})
			}
			if got := askOf(s, todo.ID); got == nil || !got.DueAt.IsZero() || got.AgentReply != nil ||
				strings.Contains(strings.Join(pendingKinds(s, todo.ID), ","), sigDecisionReply) ||
				signalled(s, f, todo.ID, sigDecisionReply, "task_decided") {
				t.Fatalf("ignored event changed the decision: ask %+v", got)
			}
		})
	}
}

func TestDecisionAgentReplyDoesNotAffectAnsweredOrNextRound(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	s.UserSpoke(context.Background(), SessionRef{MachineID: a.MachineID, SessionID: a.SessionID})
	sent := len(sentToAgent(f))
	if _, isErr := tool(t, s, "task_decided", map[string]any{"todo_id": todo.ID, "option": "直接全量", "said": "Choose the full rollout.", "told_agent": true}); isErr {
		t.Fatal("explicit answer was rejected")
	}
	if got := askOf(s, todo.ID); got == nil || got.AgentReply == nil || got.AgentReply.At.IsZero() || got.AcceptedAt.IsZero() {
		t.Fatalf("answered decision was not retained: %+v", got)
	}
	s.UserSpoke(context.Background(), SessionRef{MachineID: a.MachineID, SessionID: a.SessionID})
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now())
	if got := sentToAgent(f)[sent:]; len(got) != 0 {
		t.Fatalf("explicitly told answer was sent again: %q", got)
	}

	s2, f2, todo2 := newSupervisedService(t)
	waitsForReview(t, s2, f2, todo2.ID, "The agent needs a choice between two rollout options.")
	if _, err := reviewErr(s2, todo2.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	a2 := askOf(s2, todo2.ID)
	s2.UserSpoke(context.Background(), SessionRef{MachineID: a2.MachineID, SessionID: a2.SessionID})
	if _, err := reviewErr(s2, todo2.ID, map[string]any{"verdict": "rework", "note": "Revise the rollout plan."}); err != nil {
		t.Fatal(err)
	}
	// The next independent completion is judged after owner inactivity, not while the owner's message is still recent.
	s2.taskActivity.mu.Lock()
	s2.taskActivity.times[runtimeListID(a2.MachineID, a2.SessionID)] = time.Now().Add(-taskUserIdle - time.Second)
	s2.taskActivity.mu.Unlock()
	waitsForReview(t, s2, f2, todo2.ID, "The agent needs a choice between two rollout options.")
	if _, err := reviewErr(s2, todo2.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	if got := askOf(s2, todo2.ID); got == nil || got.AgentReply != nil || !got.DueAt.IsZero() || got.AskedAt.Equal(a2.AskedAt) {
		t.Fatalf("previous round's pause affected the new round: %+v", got)
	}
	sent = len(sentToAgent(f2))
	pastDeadline(t, s2, todo2.ID)
	s2.sweepDecisionDeadlines(time.Now())
	if got := sentToAgent(f2)[sent:]; len(got) != 1 || !askOf(s2, todo2.ID).ByDefault {
		t.Fatalf("new round lost its independent default: sent %q ask %+v", got, askOf(s2, todo2.ID))
	}
}

func TestGoldenDecisionAgentReply(t *testing.T) {
	for _, language := range []struct{ locale, name string }{
		{"en", "English"}, {"zh-Hans", ""}, {"ja", "Japanese"},
	} {
		t.Run(language.locale, func(t *testing.T) {
			golden(t, "supervise.agent_reply."+language.locale, prompts.RenderSupervise(prompts.Supervise{
				Entries: []prompts.SuperviseEntry{{ID: "todo_example", Title: "Example task", Kind: sigDecisionReply,
					AgentReply: &prompts.DecisionAgentReply{Language: language.name,
						Machine: "box", Session: "agent-session"}}},
			}))
		})
	}
}

func TestDecisionAgentReplyPauseSurvivesReload(t *testing.T) {
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	database := todoTestDB(t)
	s, f, todo, _, _ := conclusionServiceOn(t, database, false)
	// This process never tells the supervisor: the pause must reach the reloaded one. With the test's 20 ms gather the
	// reply's digest could go out while UserSpoke was still running on a loaded machine, before the timer below was
	// stopped (#13670).
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	waitsForReview(t, s, f, todo.ID, "Choose a rollout option.")
	reviewTask(t, s, todo.ID, rolloutDecision())
	setSettings(t, s, func(v *Settings) { v.Main = "" })
	a := askOf(s, todo.ID)
	// Model the old process stopping after the reply is persisted, before its supervisor can deliver it.
	s.supervise.digestMu.Lock()
	s.UserSpoke(context.Background(), SessionRef{MachineID: a.MachineID, SessionID: a.SessionID})
	s.supervise.mu.Lock()
	if s.supervise.timer != nil {
		s.supervise.timer.Stop()
		s.supervise.timer, s.supervise.timerAt = nil, time.Time{}
	}
	s.supervise.pending = map[string]superviseSignal{}
	s.supervise.mu.Unlock()
	s.supervise.digestMu.Unlock()
	var stored map[string]*acceptAsk
	if _, err := database.Setting(context.Background(), acceptanceKey, &stored); err != nil {
		t.Fatal(err)
	}
	paused := stored[todo.ID]
	if paused == nil || paused.AgentReply == nil || !paused.AgentReply.ToldAt.IsZero() || paused.DueAt.IsZero() || !paused.AcceptedAt.IsZero() {
		t.Fatalf("pause not persisted: %+v", paused)
	}
	reloaded, nextHub := newQuietServiceOn(t, database)
	if err := reloaded.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	reloaded.sweepDecisionDeadlines(time.Now().Add(48 * time.Hour))
	waitFor(t, "the recovered supervisor handoff", func() bool {
		a, ok := reloaded.askCopy(todo.ID)
		return ok && a.AgentReply != nil && !a.AgentReply.ToldAt.IsZero()
	})
	again, ok := reloaded.askCopy(todo.ID)
	if !ok || again.AgentReply == nil || again.DueAt.IsZero() || !again.AcceptedAt.IsZero() || again.ByDefault {
		t.Fatalf("reloaded decision lost the pause: %+v", again)
	}
	if result, err := reloaded.acceptTryAs(context.Background(), todo.ID, "", true); err != nil || result.taken {
		t.Fatalf("reloaded default was taken: %+v %v", result, err)
	}
	if got := sentToAgent(nextHub); len(got) != 0 {
		t.Fatalf("reloaded decision sent a guessed answer: %q", got)
	}
	if decisions, err := database.ListTodoDecisions(context.Background(), todo.ID, true); err != nil || len(decisions) != 0 {
		t.Fatalf("free text was recorded as an inferred choice: %+v %v", decisions, err)
	}
}

// A side-conversation agent_send must never settle or withdraw the task's pending decision,
// even when an older node reports the hook with prompt_source=user. The hub reads the turn's
// prompt and blocks on its provenance (issue #12369).
func TestSideConversationHookDoesNotWithdrawDecision(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	now := time.Now()
	f.items = []proto.Item{
		{Seq: 1, Role: "user", Kind: proto.KindText, TS: now.Add(-time.Second), Text: prompts.RenderSideConversation("also check the flaky parser test")},
		{Seq: 2, Role: "assistant", Kind: proto.KindText, TS: now, Text: "On it."},
	}
	params, _ := json.Marshal(map[string]any{"session_id": a.SessionID, "event": "UserPromptSubmit", "prompt_source": "user"})
	if !s.NodeEvent(a.MachineID, "hook.event", params) {
		t.Fatal("hook event was not accepted")
	}
	// Wait on the ordered queue (not a fixed sleep): the hook enqueues its decision work through
	// inOrder, so this barrier closes only after that work has actually run.
	waitFleetOrder(t, s)
	if got := askOf(s, todo.ID); got == nil || got.AgentReply != nil || !got.DueAt.IsZero() ||
		got.AcceptedAt.IsZero() == false || got.Chosen != "" {
		t.Fatalf("the side conversation changed the pending decision: %+v", got)
	}
	if strings.Contains(strings.Join(pendingKinds(s, todo.ID), ","), sigDecisionReply) ||
		signalled(s, f, todo.ID, sigDecisionReply, "task_decided") {
		t.Fatal("the side conversation emitted a decision_reply withdrawal signal")
	}
	if todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatal("the pending decision left needs_user")
	}
}

// The hub's own user_spoke path can carry text with provenance. A side-conversation or relay
// envelope there is rejected before the ask is touched, while ordinary owner text still pauses
// the default (issue #12369).
func TestSideConversationUserSpokeTextDoesNotWithdrawDecision(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	s.userSpokeText(context.Background(), SessionRef{MachineID: a.MachineID, SessionID: a.SessionID},
		prompts.RenderSideConversation("unrelated request from another conversation"))
	if got := askOf(s, todo.ID); got == nil || got.AgentReply != nil || !got.DueAt.IsZero() || got.Chosen != "" {
		t.Fatalf("provenance-marked user_spoke changed the pending decision: %+v", got)
	}
	if signalled(s, f, todo.ID, sigDecisionReply, "task_decided") {
		t.Fatal("provenance-marked user_spoke emitted a decision_reply signal")
	}
	// A genuine owner message on the same session still pauses and hands back as before.
	s.userSpokeText(context.Background(), SessionRef{MachineID: a.MachineID, SessionID: a.SessionID}, "let us think it over")
	waitFor(t, "the owner reply to pause the default", func() bool {
		got := askOf(s, todo.ID)
		return got != nil && got.AgentReply != nil && !got.DueAt.IsZero()
	})
}

// A multi-hop relay reaching the Agent is another conversation's input, never the owner
// answering its pending decision: neither the text path nor the hook transcript backstop may
// settle or withdraw the card (issue #12369).
func TestRelayMessageDoesNotWithdrawDecision(t *testing.T) {
	relay := prompts.RenderRelay(prompts.Relay{FromID: "conv-other", FromTitle: "Direction",
		Tool: "conversation_send", Hop: 1, MaxHops: 3, Text: "take this follow-up"})
	t.Run("authenticated user_spoke text", func(t *testing.T) {
		s, f, todo := decisionAgentReplyService(t)
		a := askOf(s, todo.ID)
		s.userSpokeText(context.Background(), SessionRef{MachineID: a.MachineID, SessionID: a.SessionID}, relay)
		if got := askOf(s, todo.ID); got == nil || got.AgentReply != nil || !got.DueAt.IsZero() || got.Chosen != "" {
			t.Fatalf("relay text changed the pending decision: %+v", got)
		}
		if signalled(s, f, todo.ID, sigDecisionReply, "task_decided") {
			t.Fatal("relay text emitted a decision_reply signal")
		}
	})
	t.Run("older node hook reported as user", func(t *testing.T) {
		s, f, todo := decisionAgentReplyService(t)
		a := askOf(s, todo.ID)
		now := time.Now()
		f.items = []proto.Item{
			{Seq: 1, Role: "user", Kind: proto.KindText, TS: now.Add(-time.Second), Text: relay},
			{Seq: 2, Role: "assistant", Kind: proto.KindText, TS: now, Text: "On it."},
		}
		params, _ := json.Marshal(map[string]any{"session_id": a.SessionID, "event": "UserPromptSubmit", "prompt_source": "user"})
		if !s.NodeEvent(a.MachineID, "hook.event", params) {
			t.Fatal("hook event was not accepted")
		}
		waitFleetOrder(t, s)
		if got := askOf(s, todo.ID); got == nil || got.AgentReply != nil || got.Chosen != "" {
			t.Fatalf("the relay hook changed the pending decision: %+v", got)
		}
		if signalled(s, f, todo.ID, sigDecisionReply, "task_decided") {
			t.Fatal("the relay hook emitted a decision_reply signal")
		}
		if todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
			t.Fatal("the pending decision left needs_user")
		}
	})
}

// An owner pasting the side-conversation template into their own message keeps ownership:
// ProtectUserMessage's author=user block must take precedence over the raw side-conversation
// markers, so the reply pauses the default instead of being silently ignored (semantic review
// P2 on #12383).
func TestProtectedSideConversationPasteStillCountsAsOwner(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	relay := prompts.RenderRelay(prompts.Relay{FromID: "conv-other", FromTitle: "Direction",
		Tool: "conversation_send", Hop: 1, MaxHops: 3, Text: "take this follow-up"})
	pasted := prompts.ProtectUserMessage(relay) + "\n" + prompts.RenderSideConversation("pasted side template")
	s.userSpokeText(context.Background(), SessionRef{MachineID: a.MachineID, SessionID: a.SessionID}, pasted)
	waitFor(t, "the protected owner reply to pause the default", func() bool {
		got := askOf(s, todo.ID)
		return got != nil && got.AgentReply != nil && !got.DueAt.IsZero()
	})
	if got := askOf(s, todo.ID); got.Chosen != "" || !got.AcceptedAt.IsZero() {
		t.Fatalf("protected paste was treated as a decision choice: %+v", got)
	}
	waitFor(t, "the protected owner hand-off", func() bool {
		return signalled(s, f, todo.ID, sigDecisionReply, "task_decided")
	})
}

// An injected system notice with no MessageSource kind (the answer_again re-prompt) must not
// settle or withdraw a pending decision through the text path (semantic review P2 on #12383).
func TestInjectedNoticeDoesNotWithdrawDecision(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	notice := prompts.RenderAssistantAnswerAgain(prompts.AssistantAnswerAgain{Language: "English"}) + " please reply"
	if prompts.ItemSource(proto.Item{Role: "user", Kind: proto.KindText, Text: notice}) != nil {
		t.Fatal("the fixture notice gained a MessageSource kind; pick one without it")
	}
	s.userSpokeText(context.Background(), SessionRef{MachineID: a.MachineID, SessionID: a.SessionID}, notice)
	if got := askOf(s, todo.ID); got == nil || got.AgentReply != nil || !got.DueAt.IsZero() || got.Chosen != "" {
		t.Fatalf("an injected notice changed the pending decision: %+v", got)
	}
	if signalled(s, f, todo.ID, sigDecisionReply, "task_decided") {
		t.Fatal("an injected notice emitted a decision_reply signal")
	}
	if todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatal("the pending decision left needs_user")
	}
}

// UserPromptSubmit fires before the prompt lands in the transcript, so turns:1 can still show
// the previous turn. A side conversation sitting in that older turn must not block the owner's
// genuine reply: when every visible prompt is older than the hook, the node's prompt_source
// classification stands and the default is paused (CaC033 review on #12383).
func TestStaleTranscriptSideConversationDoesNotBlockOwnerHook(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	hookAt := time.Now()
	f.items = []proto.Item{
		{Seq: 1, Role: "user", Kind: proto.KindText, TS: hookAt.Add(-2 * time.Minute),
			Text: prompts.RenderSideConversation("also check the flaky parser test")},
		{Seq: 2, Role: "assistant", Kind: proto.KindText, TS: hookAt.Add(-90 * time.Second), Text: "On it."},
	}
	params, _ := json.Marshal(map[string]any{
		"session_id": a.SessionID, "event": "UserPromptSubmit", "prompt_source": "user",
		"spooled_at": hookAt.UTC().Format(time.RFC3339Nano),
	})
	if !s.NodeEvent(a.MachineID, "hook.event", params) {
		t.Fatal("hook event was not accepted")
	}
	waitFor(t, "the owner's own reply to pause the default", func() bool {
		got := askOf(s, todo.ID)
		return got != nil && got.AgentReply != nil && !got.DueAt.IsZero()
	})
	if got := askOf(s, todo.ID); got.Chosen != "" || !got.AcceptedAt.IsZero() {
		t.Fatalf("the stale transcript settled the decision: %+v", got)
	}
	if !signalled(s, f, todo.ID, sigDecisionReply, "task_decided") {
		t.Fatal("the owner's hook reply did not hand back to the supervisor")
	}
}

// The hook transcript backstop must reject an injected notice with no MessageSource kind the
// same way the user_spoke text path does. The item is fresh, so the stale-turn fail-open does
// not apply: only the ParseInjected guard distinguishes it from an owner reply (CaC033
// re-verification on #12383: this hook-path guard had no test).
func TestInjectedNoticeHookDoesNotWithdrawDecision(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	notice := prompts.RenderAssistantAnswerAgain(prompts.AssistantAnswerAgain{Language: "English"}) + " please reply"
	if prompts.ItemSource(proto.Item{Role: "user", Kind: proto.KindText, Text: notice}) != nil {
		t.Fatal("the fixture notice gained a MessageSource kind; pick one without it")
	}
	now := time.Now()
	f.items = []proto.Item{
		{Seq: 1, Role: "user", Kind: proto.KindText, TS: now.Add(-time.Second), Text: notice},
		{Seq: 2, Role: "assistant", Kind: proto.KindText, TS: now, Text: "On it."},
	}
	params, _ := json.Marshal(map[string]any{"session_id": a.SessionID, "event": "UserPromptSubmit", "prompt_source": "user"})
	if !s.NodeEvent(a.MachineID, "hook.event", params) {
		t.Fatal("hook event was not accepted")
	}
	waitFleetOrder(t, s)
	if got := askOf(s, todo.ID); got == nil || got.AgentReply != nil || !got.DueAt.IsZero() || got.Chosen != "" {
		t.Fatalf("an injected notice hook changed the pending decision: %+v", got)
	}
	if signalled(s, f, todo.ID, sigDecisionReply, "task_decided") {
		t.Fatal("an injected notice hook emitted a decision_reply signal")
	}
	if todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatal("the pending decision left needs_user")
	}
}

// On the hook path too, an owner pasting the side-conversation template inside a
// ProtectUserMessage block keeps ownership: the UserAuthoredContext early return must win over
// the raw markers, so the reply pauses the default and hands back instead of being dropped as a
// side conversation (CaC033 re-verification on #12383: this hook-path guard had no test).
func TestProtectedSideConversationPasteHookStillCountsAsOwner(t *testing.T) {
	s, f, todo := decisionAgentReplyService(t)
	a := askOf(s, todo.ID)
	relay := prompts.RenderRelay(prompts.Relay{FromID: "conv-other", FromTitle: "Direction",
		Tool: "conversation_send", Hop: 1, MaxHops: 3, Text: "take this follow-up"})
	pasted := prompts.ProtectUserMessage(relay) + "\n" + prompts.RenderSideConversation("pasted side template")
	now := time.Now()
	f.items = []proto.Item{
		{Seq: 1, Role: "user", Kind: proto.KindText, TS: now.Add(-time.Second), Text: pasted},
		{Seq: 2, Role: "assistant", Kind: proto.KindText, TS: now, Text: "On it."},
	}
	params, _ := json.Marshal(map[string]any{"session_id": a.SessionID, "event": "UserPromptSubmit", "prompt_source": "user"})
	if !s.NodeEvent(a.MachineID, "hook.event", params) {
		t.Fatal("hook event was not accepted")
	}
	waitFor(t, "the protected owner hook reply to pause the default", func() bool {
		got := askOf(s, todo.ID)
		return got != nil && got.AgentReply != nil && !got.DueAt.IsZero()
	})
	if got := askOf(s, todo.ID); got.Chosen != "" || !got.AcceptedAt.IsZero() {
		t.Fatalf("the protected hook paste was treated as a decision choice: %+v", got)
	}
	waitFor(t, "the protected owner hook hand-off", func() bool {
		return signalled(s, f, todo.ID, sigDecisionReply, "task_decided")
	})
}
