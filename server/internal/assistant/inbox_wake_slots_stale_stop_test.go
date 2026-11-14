package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func wakeSlotsStaleStopTestLatestLink(t *testing.T, h *inboxHub, it *InboxItem) InboxChainLink {
	t.Helper()
	h.inbox.mu.Lock()
	defer h.inbox.mu.Unlock()
	link := inboxLatestAgentLink(it)
	if link == nil {
		t.Fatal("the item has no current Agent link")
	}
	return *link
}

// A stop deferred while its Agent was current becomes informational after the user confirms a newer Agent. Admitting
// that old result must neither wake the processing conversation nor hide the newer Agent from ordinary sweep timeouts.
func TestInboxWakeSlotsRound1FixStaleDeferredStopDoesNotWakeConfirmedAgent(t *testing.T) {
	ctx := context.Background()
	h, it, sessA := newInboxAgentStoppedHub(t)
	cfg := InboxAgentConfig{Enabled: true, Concurrency: 1, TimeoutMinutes: 1}
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent = cfg })
	busy := wakeSlotsAdversarialTestOccupyOnlySlot(h, "wake-slots-stale-stop-busy")
	if !h.recordInboxAgentStops(ctx, sessA) {
		t.Fatal("Agent A's stop was not recorded")
	}
	if _, err := suggestTool(t, h, `{"kind":"agent","text":"Synthetic Agent B instruction","machine":"box"}`); err != nil {
		t.Fatal(err)
	}
	h.f.mu.Lock()
	h.f.agents = 41 // keep Agent B's session distinct from the processing conversation and Agent A
	h.f.mu.Unlock()
	code, out := suggestionCall(t, h, it.ID, `{"version":1,"action":"confirm"}`)
	if code != 200 {
		t.Fatalf("confirm Agent B: %d %v", code, out)
	}
	h.deliverNotes(ctx, it) // finish the confirmation's informational send before counting Agent A's result
	linkB := wakeSlotsStaleStopTestLatestLink(t, h, it)
	if linkB.MachineID != "box" || linkB.SessionID == sessA.SessionID {
		t.Fatalf("Agent B did not become the latest Agent: %+v", linkB)
	}
	if state, _, _ := handlingNow(h, it); state != HandlingAgent {
		t.Fatalf("confirming Agent B did not retain HandlingAgent: %q", state)
	}
	if got := wakeSlotsAgentTestDeliveries(h, sessA.LastText); got != 0 {
		t.Fatalf("Agent A's deferred result was delivered %d times before admission", got)
	}
	sessB := proto.Session{MachineID: linkB.MachineID, SessionID: linkB.SessionID, Status: proto.StatusWorking, UpdatedAt: time.Now().UTC()}
	h.f.setSessions("box", sessA, sessB)

	wakeSlotsAgentTestRelease(t, h, busy)
	h.checkInboxFollowUps(ctx, cfg, time.Now())

	h.inbox.mu.Lock()
	state := it.Handling.State
	wake := it.Agent.Wake
	wakeSlot := it.Agent.WakeSlot
	running := h.inbox.inboxRunning()
	h.inbox.mu.Unlock()
	if state != HandlingAgent || wake || wakeSlot || running != 0 {
		t.Fatalf("stale Agent A consumed B's handling or a capacity slot: handling %q, wake %v, wake_slot %v, running %d", state, wake, wakeSlot, running)
	}

	h.deliverPending(ctx)
	toldConv(t, h, sessA.LastText)
	if got := wakeSlotsAgentTestDeliveries(h, sessA.LastText); got != 1 {
		t.Fatalf("Agent A's informational result was delivered %d times, want once", got)
	}
	stored := wakeSlotsAgentTestStoredItem(t, h, it.ID)
	if got := len(wakeSlotsAgentTestStopEntries(&stored)); got != 1 {
		t.Fatalf("Agent A has %d durable stop entries, want exactly one", got)
	}
	if _, _, err := h.suggest(ctx, "s1", suggestIn{Recap: "Synthetic recap while Agent B runs"}); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := handlingNow(h, it); state != HandlingAgent {
		t.Fatalf("a recap-only outcome falsely completed running Agent B: %q", state)
	}

	started := time.Now().Add(-5 * time.Minute)
	h.inbox.mu.Lock()
	it.Agent.StartedAt = &started
	h.inbox.mu.Unlock()
	h.sweepInboxAgents(ctx, cfg, time.Now())
	if state, _, _ := handlingNow(h, it); state != HandlingAgent {
		t.Fatalf("working Agent B did not retain ordinary sweep supervision: %q", state)
	}

	// A reachable node that no longer has B retains the existing four-timeout failure behavior.
	h.f.setSessions("box", sessA)
	h.sweepInboxAgents(ctx, cfg, time.Now())
	if state, _, _ := handlingNow(h, it); state != HandlingFailed {
		t.Fatalf("stale Agent A suppressed current Agent B's timeout: %q", state)
	}
}
