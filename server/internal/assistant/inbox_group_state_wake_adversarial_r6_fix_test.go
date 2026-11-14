package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Agent A and then Agent B belong to the same matter. A's older stop is still delivered to the processing conversation,
// but its wake must not restart B's bounded timeout when B's node is unreachable.
func TestAdversarialInboxGroupStateWakeOlderAgentStopDoesNotRefreshNewerAgentTimeout(t *testing.T) {
	ctx := context.Background()
	h, _, it := handledHub(t)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)

	h.inboxAgentStarted(ctx, "s1", "box", "agent-a", "first synthetic Agent task")
	h.inboxAgentStarted(ctx, "s1", "box", "agent-b", "second synthetic Agent task")
	if link := inboxLatestAgentLink(it); link == nil || link.MachineID != "box" || link.SessionID != "agent-b" {
		t.Fatalf("current Agent link = %+v, want Agent B", link)
	}

	cfg := InboxAgentConfig{Enabled: true, TimeoutMinutes: 1}
	started := time.Now().UTC().Add(-4*agentTimeout(cfg) - time.Second)
	h.inbox.mu.Lock()
	it.Agent.StartedAt = &started
	h.inbox.mu.Unlock()
	if state, _, _ := handlingNow(h, it); state != HandlingAgent {
		t.Fatalf("handling before the older stop = %q, want agent", state)
	}

	h.f.setSessions("box",
		proto.Session{MachineID: "box", SessionID: "agent-a", Status: proto.StatusIdle,
			LastText: "Older synthetic Agent result", UpdatedAt: time.Now().UTC()},
		proto.Session{MachineID: "box", SessionID: "agent-b", Status: proto.StatusWorking,
			LastText: "Newer synthetic Agent still running", UpdatedAt: time.Now().UTC()})
	h.inboxAgentTurnSettled(ctx, "box", "agent-a")
	h.deliverPending(ctx)
	if state, wake, _ := handlingNow(h, it); state != HandlingAgent || wake {
		t.Fatalf("older Agent stop was not delivered behind Agent B: handling %q, wake %v", state, wake)
	}
	if got := inboxAgentStoppedCount(it); got != 1 {
		t.Fatalf("Agent stops after A's stop = %d, want one", got)
	}
	if stop := latestInboxAgentStop(t, it); stop.Link == nil || stop.Link.MachineID != "box" || stop.Link.SessionID != "agent-a" {
		t.Fatalf("recorded stop = %+v, want Agent A", stop)
	}

	// An unreachable current Agent has only the four-timeout bound; there is no settled-session answer to wait for.
	h.f.mu.Lock()
	delete(h.f.sessions, "box")
	h.f.mu.Unlock()
	h.sweepInboxAgents(ctx, cfg, time.Now().UTC())

	snapshot := inboxAgentStoppedSnapshot(t, h, it)
	if snapshot.Handling == nil || snapshot.Handling.State != HandlingFailed || snapshot.Agent == nil || snapshot.Agent.State != AgentFailed {
		t.Fatalf("older Agent wake kept the expired current Agent alive: handling %+v, agent %+v", snapshot.Handling, snapshot.Agent)
	}
	if got := inboxAgentStoppedCount(&snapshot); got != 1 {
		t.Fatalf("Agent stops after the sweep = %d, want only Agent A's stop", got)
	}
	if stop := latestInboxAgentStop(t, &snapshot); stop.Link == nil || stop.Link.MachineID != "box" || stop.Link.SessionID != "agent-a" {
		t.Fatalf("stop retained after the sweep = %+v, want Agent A", stop)
	}
}
