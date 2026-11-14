package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
)

// The self_replied note explicitly asks a matter that is waiting on the other side for recap + follow. While its
// current Agent is still running, that prompted outcome must not replace HandlingAgent with waiting: the Agent's stop
// still has to return the matter to its processing conversation as it did before B-13.
func TestAdversarialInboxGroupStateWakeR7FollowKeepsCurrentAgent(t *testing.T) {
	ctx := context.Background()
	h := inboxGroupStateTestHub(t, true)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)
	it := inboxGroupStateTestItem(h, "event-r7-agent-follow", true)
	h.inboxAgentStarted(ctx, "session-1", "box", "agent-session", "synthetic Agent task")

	report := inboxGroupStateReport(it, false, connector.DoneReplied)
	rest, changed := h.applyGroupStates(ctx, inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateChange(t, rest, changed, it)
	h.deliverPending(ctx)

	_, _, err := h.suggest(ctx, "session-1", suggestIn{
		Recap: "The owner replied; still waiting for the synthetic answer",
		Follow: &suggestFollow{Who: "Example teammate", Next: "Wait for the answer", CheckIn: "4h"},
	})
	if err != nil {
		t.Fatal(err)
	}
	state, _, _ := handlingNow(h, it)
	if state != HandlingAgent {
		t.Errorf("the prompted recap + follow replaced the running Agent with %q, want %q", state, HandlingAgent)
	}

	// Even if a follow outcome were allowed to wait, the current Agent's later stop must still hand the matter back
	// to the processing conversation. Staying waiting here strands the stop notice outside the usual #6927 lifecycle.
	h.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "agent-session", Status: proto.StatusIdle,
		LastText: "Synthetic Agent result", UpdatedAt: time.Now().UTC()})
	h.inboxAgentTurnSettled(ctx, "box", "agent-session")
	h.deliverPending(ctx)
	if got := inboxAgentStoppedCount(it); got != 1 {
		t.Fatalf("Agent stops = %d, want one", got)
	}
	state, wake, back := handlingNow(h, it)
	if state != HandlingLooking {
		t.Errorf("the current Agent's stop left handling %q (wake %v, back %q), want looking", state, wake, back)
	}
}

// A source reply wakes the processing conversation but is not activity by the current Agent. Reusing the processing
// wake's StartedAt as the Agent's timeout clock lets an already expired, unreachable Agent wait for four more timeouts.
func TestAdversarialInboxGroupStateWakeR7ReplyDoesNotRefreshCurrentAgentTimeout(t *testing.T) {
	ctx := context.Background()
	h := inboxGroupStateTestHub(t, true)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)
	it := inboxGroupStateTestItem(h, "event-r7-agent-timeout", true)
	h.inboxAgentStarted(ctx, "session-1", "box", "agent-session", "synthetic Agent task")

	cfg := InboxAgentConfig{Enabled: true, TimeoutMinutes: 1}
	started := time.Now().UTC().Add(-4*agentTimeout(cfg) - time.Second)
	h.inbox.mu.Lock()
	it.Agent.StartedAt = &started
	h.inbox.mu.Unlock()

	report := inboxGroupStateReport(it, false, connector.DoneReplied)
	rest, changed := h.applyGroupStates(ctx, inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateChange(t, rest, changed, it)
	h.deliverPending(ctx)

	// The current Agent's node is unreachable, so only the existing four-timeout bound can settle it.
	h.f.mu.Lock()
	delete(h.f.sessions, "box")
	h.f.mu.Unlock()
	h.sweepInboxAgents(ctx, cfg, time.Now().UTC())

	snapshot := inboxAgentStoppedSnapshot(t, h, it)
	if snapshot.Handling == nil || snapshot.Handling.State != HandlingFailed || snapshot.Agent == nil || snapshot.Agent.State != AgentFailed {
		t.Fatalf("source reply refreshed the expired Agent's timeout: handling %+v, agent %+v", snapshot.Handling, snapshot.Agent)
	}
}
