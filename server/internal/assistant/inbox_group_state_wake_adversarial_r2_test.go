package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// An instruction interrupted a waiting matter, and its turn started an Agent. The Agent's stop must not erase the
// waiting state to which a recap-only outcome of that same instruction returns (the existing B-7 restoration rule).
func TestAdversarialInboxGroupStateWakeAgentStopPreservesWaitingBack(t *testing.T) {
	ctx := context.Background()
	h, _, it := handledHub(t)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)

	if _, err := suggestTool(t, h, `{"recap":"Waiting for the synthetic quote","follow":{"who":"Example teammate","next":"Wait for the quote","check_in":"4h"}}`); err != nil {
		t.Fatal(err)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"Investigate the synthetic request"}`); code != 200 {
		t.Fatalf("instruct: %d %v", code, out)
	}
	if state, _, back := handlingNow(h, it); state != HandlingLooking || back != HandlingWaiting {
		t.Fatalf("instruction did not remember waiting: handling %q, back %q", state, back)
	}
	h.deliverPending(ctx)

	h.inboxAgentStarted(ctx, "s1", "box", "s-sub", "Investigate the synthetic request")
	if state, _, back := handlingNow(h, it); state != HandlingAgent || back != HandlingWaiting {
		t.Fatalf("Agent startup changed the instruction's restoration state: handling %q, back %q", state, back)
	}
	h.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s-sub", Status: proto.StatusIdle,
		LastText: "Synthetic Agent result", UpdatedAt: time.Now().UTC()})
	h.inboxAgentTurnSettled(ctx, "box", "s-sub")
	h.deliverPending(ctx)
	if state, _, _ := handlingNow(h, it); state != HandlingLooking || inboxAgentStoppedCount(it) != 1 {
		t.Fatalf("Agent stop did not return to the processing conversation: handling %q, stops %d", state, inboxAgentStoppedCount(it))
	}

	if _, err := suggestTool(t, h, `{"recap":"The Agent finished; still waiting for the synthetic quote"}`); err != nil {
		t.Fatal(err)
	}
	state, wake, back := handlingNow(h, it)
	if state != HandlingWaiting || wake || back != "" {
		t.Fatalf("recap after the instructed Agent stopped lost the original wait: handling %q, wake %v, back %q; want waiting", state, wake, back)
	}
	wantCheckIn(t, h, it, 4*time.Hour, "waiting restoration after Agent stop")
}

func TestAdversarialInboxGroupStateWakeAgentStopWhileLookingClearsAgentBack(t *testing.T) {
	ctx := context.Background()
	h := inboxGroupStateTestHub(t, true)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)
	it := inboxGroupStateTestItem(h, "event-adversarial-stop-looking", true)
	h.inboxAgentStarted(ctx, "session-1", "server", "agent-session", "synthetic Agent task")

	// A newer instruction has already interrupted Agent work, so the stop arrives while handling is looking.
	h.inbox.mu.Lock()
	h.inbox.tell(it, inboxNote{Name: prompts.NoteInstructed}, true, time.Now())
	h.inbox.setHandling(it, HandlingLooking, "", time.Now())
	h.inbox.mu.Unlock()
	if state, _, back := handlingNow(h, it); state != HandlingLooking || back != HandlingAgent {
		t.Fatalf("instruction did not interrupt Agent work: handling %q, back %q", state, back)
	}
	h.f.setSessions("server",
		proto.Session{SessionID: "session-1", Status: proto.StatusIdle},
		proto.Session{MachineID: "server", SessionID: "agent-session", Status: proto.StatusIdle,
			LastText: "Synthetic Agent result", UpdatedAt: time.Now().UTC()})
	h.inboxAgentTurnSettled(ctx, "server", "agent-session")
	h.deliverPending(ctx)

	if _, _, err := h.suggest(ctx, "session-1", suggestIn{Recap: "Synthetic recap after the looking stop"}); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := handlingNow(h, it); state != HandlingDone {
		t.Fatalf("recap after a stop while looking restored the stopped Agent or another state: %q, want done", state)
	}
}

func TestAdversarialInboxGroupStateWakeAgentStopSweepPreservesWaitingBack(t *testing.T) {
	for _, tc := range []struct {
		name           string
		timeoutMinutes int
		status         string
	}{
		{name: "no draft", timeoutMinutes: 15, status: proto.StatusIdle},
		{name: "timeout", timeoutMinutes: 1, status: proto.StatusWorking},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h, _, it := handledHub(t)
			h.inbox.handing.Lock()
			t.Cleanup(h.inbox.handing.Unlock)
			if _, err := suggestTool(t, h, `{"recap":"Waiting for the synthetic quote","follow":{"who":"Example teammate","next":"Wait for the quote","check_in":"4h"}}`); err != nil {
				t.Fatal(err)
			}
			if code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"Investigate the synthetic request"}`); code != 200 {
				t.Fatalf("instruct: %d %v", code, out)
			}
			h.deliverPending(ctx)
			h.inboxAgentStarted(ctx, "s1", "box", "s-sub", "Investigate the synthetic request")
			h.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s-sub", Status: proto.StatusIdle,
				LastText: "Synthetic Agent result", UpdatedAt: time.Now().UTC()})
			h.inboxAgentTurnSettled(ctx, "box", "s-sub")
			h.deliverPending(ctx)
			if state, _, back := handlingNow(h, it); state != HandlingLooking || back != HandlingWaiting {
				t.Fatalf("Agent stop lost waiting before the sweep: handling %q, back %q", state, back)
			}

			started := time.Now().Add(-2 * time.Minute)
			h.inbox.mu.Lock()
			it.Agent.StartedAt = &started
			h.inbox.mu.Unlock()
			h.f.setSessions("server", proto.Session{SessionID: "s1", Status: tc.status})
			h.sweepInboxAgents(ctx, InboxAgentConfig{Enabled: true, TimeoutMinutes: tc.timeoutMinutes}, time.Now())

			if state, _, _ := handlingNow(h, it); state != HandlingWaiting {
				t.Fatalf("%s after the waiting-backed Agent stop ended in %q, want waiting", tc.name, state)
			}
			wantCheckIn(t, h, it, 4*time.Hour, tc.name+" waiting restoration after Agent stop")
			followCheckNoFailure(t, h, it)
		})
	}
}
