package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Agent A and then Agent B belong to the same matter. An older Agent's stop is still timeline history, but it must not
// clear a newer instruction's restoration point: a recap-only turn returns to B, which is still running.
func TestAdversarialInboxGroupStateWakeOlderAgentStopRecapKeepsNewerAgent(t *testing.T) {
	ctx := context.Background()
	h, _, it := handledHub(t)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)

	h.inboxAgentStarted(ctx, "s1", "box", "agent-a", "first synthetic Agent task")
	h.inboxAgentStarted(ctx, "s1", "box", "agent-b", "second synthetic Agent task")
	if link := inboxLatestAgentLink(it); link == nil || link.MachineID != "box" || link.SessionID != "agent-b" {
		t.Fatalf("current Agent link = %+v, want Agent B", link)
	}

	if code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"Check the newer synthetic instruction"}`); code != 200 {
		t.Fatalf("instruct: %d %v", code, out)
	}
	h.deliverPending(ctx)
	if state, _, back := handlingNow(h, it); state != HandlingLooking || back != HandlingAgent {
		t.Fatalf("instruction did not interrupt Agent B: handling %q, back %q", state, back)
	}

	h.f.setSessions("box",
		proto.Session{MachineID: "box", SessionID: "agent-a", Status: proto.StatusIdle,
			LastText: "Older synthetic Agent result", UpdatedAt: time.Now().UTC()},
		proto.Session{MachineID: "box", SessionID: "agent-b", Status: proto.StatusWorking,
			LastText: "Newer synthetic Agent still running", UpdatedAt: time.Now().UTC()})
	h.inboxAgentTurnSettled(ctx, "box", "agent-a")
	h.deliverPending(ctx)

	if got := inboxAgentStoppedCount(it); got != 1 {
		t.Fatalf("Agent A stops = %d, want one", got)
	}
	if stop := latestInboxAgentStop(t, it); stop.Link == nil || stop.Link.MachineID != "box" || stop.Link.SessionID != "agent-a" {
		t.Fatalf("recorded stop = %+v, want Agent A", stop)
	}

	if _, _, err := h.suggest(ctx, "s1", suggestIn{Recap: "Synthetic recap while Agent B continues"}); err != nil {
		t.Fatal(err)
	}
	state, wake, back := handlingNow(h, it)
	if state != HandlingAgent || wake || back != HandlingAgent {
		t.Fatalf("recap after Agent A stopped lost running Agent B: handling %q, wake %v, back %q; want agent with agent restoration", state, wake, back)
	}
	if got := inboxAgentStoppedCount(it); got != 1 {
		t.Fatalf("Agent A stops after recap = %d, want one", got)
	}
}
