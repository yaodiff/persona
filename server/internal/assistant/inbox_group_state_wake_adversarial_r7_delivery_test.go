package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Agent A's older stop is still delivered to the processing conversation while current Agent B continues. Delivery is
// informational only: it must not wake the conversation or replace B's handling state, restoration point or turn clock.
func TestAdversarialInboxGroupStateWakeR7OlderAgentStopDeliveryPreservesCurrentAgent(t *testing.T) {
	ctx := context.Background()
	h, _, it := handledHub(t)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)

	h.inboxAgentStarted(ctx, "s1", "box", "agent-a", "first synthetic Agent task")
	h.inboxAgentStarted(ctx, "s1", "box", "agent-b", "second synthetic Agent task")
	started := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	h.inbox.mu.Lock()
	it.Agent.StartedAt = &started
	h.inbox.mu.Unlock()
	before := inboxAgentStoppedSnapshot(t, h, it)
	if before.Handling == nil || before.Agent == nil || before.Handling.State != HandlingAgent || before.Agent.StartedAt == nil {
		t.Fatalf("Agent B setup: handling %+v, agent %+v", before.Handling, before.Agent)
	}

	const olderResult = "Older synthetic Agent result"
	h.f.setSessions("box",
		proto.Session{MachineID: "box", SessionID: "agent-a", Status: proto.StatusIdle,
			LastText: olderResult, UpdatedAt: time.Now().UTC()},
		proto.Session{MachineID: "box", SessionID: "agent-b", Status: proto.StatusWorking,
			LastText: "Newer synthetic Agent still running", UpdatedAt: time.Now().UTC()})
	h.inboxAgentTurnSettled(ctx, "box", "agent-a")

	// Do not call deliverPending: the stop itself must schedule delivery of its non-waking note.
	told := toldConv(t, h, "Agent 停了")
	for _, want := range []string{olderResult, "agent-a"} {
		if !strings.Contains(told, want) {
			t.Errorf("Agent A's stop notice lacks %q:\n%s", want, told)
		}
	}

	after := inboxAgentStoppedSnapshot(t, h, it)
	if got := inboxAgentStoppedCount(&after); got != 1 {
		t.Fatalf("Agent stops = %d, want only Agent A's stop", got)
	}
	if stop := latestInboxAgentStop(t, &after); stop.Link == nil || stop.Link.MachineID != "box" ||
		stop.Link.SessionID != "agent-a" || stop.Detail != olderResult {
		t.Fatalf("recorded stop = %+v, want Agent A", stop)
	}
	if after.Handling.State != before.Handling.State || after.Agent.Back != before.Agent.Back ||
		after.Agent.StartedAt == nil || !after.Agent.StartedAt.Equal(*before.Agent.StartedAt) {
		t.Fatalf("Agent A's delivery changed current Agent B: before handling %q, back %q, started %v; after handling %q, back %q, started %v",
			before.Handling.State, before.Agent.Back, before.Agent.StartedAt,
			after.Handling.State, after.Agent.Back, after.Agent.StartedAt)
	}
	if link := inboxLatestAgentLink(&after); link == nil || link.MachineID != "box" || link.SessionID != "agent-b" {
		t.Fatalf("current Agent link = %+v, want Agent B", link)
	}
}
