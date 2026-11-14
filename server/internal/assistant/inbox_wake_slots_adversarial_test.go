package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
)

func wakeSlotsAdversarialTestOccupyOnlySlot(h *inboxHub, id string) *InboxItem {
	now := time.Now().UTC()
	busy := actionItem(id, now)
	busy.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: id + "-session", Of: busy.Last}
	h.inbox.mu.Lock()
	h.inbox.items[busy.ID] = busy
	h.inbox.setHandling(busy, HandlingLooking, "", now)
	h.inbox.mu.Unlock()
	return busy
}

// A user's instruction is deliberately uncapped, but overtaking a held stop must not make the already-stopped Agent
// the restoration point of the combined turn. The immediate pre-cap path cleared that restoration point first.
func TestWakeSlotsAdversarialTestInstructAfterDeferredStopDoesNotRestoreAgent(t *testing.T) {
	ctx := context.Background()
	h, it, sess := newInboxAgentStoppedHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled, c.Agent.Concurrency = true, 1 })
	wakeSlotsAdversarialTestOccupyOnlySlot(h, "wake-slots-adversarial-instruct-busy")
	if !h.recordInboxAgentStops(ctx, sess) {
		t.Fatal("the Agent stop was not recorded")
	}
	h.inbox.mu.Lock()
	deferred := it.Agent.DeferredAt != nil
	h.inbox.mu.Unlock()
	if !deferred {
		t.Fatal("the test stop did not wait for the occupied slot")
	}

	code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"Investigate after the stop"}`)
	if code != 200 {
		t.Fatalf("uncapped instruction at full capacity: %d %v", code, out)
	}
	toldConv(t, h, "Investigate after the stop") // the user's note and the held stop are both delivered immediately
	if _, _, err := h.suggest(ctx, "s1", suggestIn{Recap: "Recap after the instruction and Agent result"}); err != nil {
		t.Fatal(err)
	}
	state, _, back := handlingNow(h, it)
	if state != HandlingDone || back == HandlingAgent {
		t.Fatalf("the combined turn restored the stopped Agent: handling %q, back %q; want done", state, back)
	}
}

// When a stop and then a source reply wait for the same slot, admitting both must preserve the reply's waiting
// restoration point. Otherwise an idle processing turn is treated as a failure instead of returning to its follow.
func TestWakeSlotsAdversarialTestStopThenReplyAdmissionRestoresWaiting(t *testing.T) {
	ctx := context.Background()
	h := inboxGroupStateTestHub(t, true)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled, c.Agent.Concurrency = true, 1 })
	it := inboxGroupStateTestItem(h, "wake-slots-adversarial-stop-reply", true)
	h.inboxAgentStarted(ctx, "session-1", "box", "agent-session", "synthetic Agent task")
	busy := wakeSlotsAdversarialTestOccupyOnlySlot(h, "wake-slots-adversarial-stop-reply-busy")
	sess := proto.Session{MachineID: "box", SessionID: "agent-session", Status: proto.StatusIdle,
		LastText: "Synthetic Agent result", UpdatedAt: time.Now().UTC()}
	if !h.recordInboxAgentStops(ctx, sess) {
		t.Fatal("the Agent stop was not recorded")
	}
	report := inboxGroupStateReport(it, false, connector.DoneReplied)
	rest, changed := h.applyGroupStates(ctx, inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateChange(t, rest, changed, it)

	h.inbox.mu.Lock()
	h.inbox.setHandling(busy, HandlingWaiting, "", time.Now().UTC())
	h.inbox.mu.Unlock()
	h.checkInboxFollowUps(ctx, InboxAgentConfig{Enabled: true, Concurrency: 1}, time.Now())
	h.deliverPending(ctx)
	if state, _, _ := handlingNow(h, it); state != HandlingLooking {
		t.Fatalf("the held notifications were not admitted after capacity freed: handling %q", state)
	}

	started := time.Now().Add(-2 * time.Minute)
	h.inbox.mu.Lock()
	it.Agent.StartedAt = &started
	h.inbox.mu.Unlock()
	h.f.setSessions("server", proto.Session{SessionID: "session-1", Status: proto.StatusIdle})
	h.sweepInboxAgents(ctx, InboxAgentConfig{Enabled: true, Concurrency: 1}, time.Now())
	state, _, back := handlingNow(h, it)
	if state != HandlingWaiting {
		t.Fatalf("an empty combined stop/reply turn lost its follow: handling %q, back %q; want waiting", state, back)
	}
	followCheckNoFailure(t, h, it)
}
