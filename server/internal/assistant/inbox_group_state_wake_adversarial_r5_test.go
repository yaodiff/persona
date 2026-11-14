package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A waiting matter's instruction starts Agent A and then Agent B. Agent A's older stop must not replace the waiting
// restoration point while B is still current; after B stops, a recap-only turn belongs back in waiting.
func TestAdversarialInboxGroupStateWakeOlderStopPreservesWaitingBehindNewerAgent(t *testing.T) {
	ctx := context.Background()
	h, _, it := handledHub(t)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)

	if _, err := suggestTool(t, h, `{"recap":"Waiting for the synthetic quote","follow":{"who":"Example teammate","next":"Wait for the quote","check_in":"4h"}}`); err != nil {
		t.Fatal(err)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"Investigate with two synthetic Agents"}`); code != 200 {
		t.Fatalf("instruct: %d %v", code, out)
	}
	h.deliverPending(ctx)
	h.inboxAgentStarted(ctx, "s1", "box", "agent-a", "first synthetic Agent task")
	h.inboxAgentStarted(ctx, "s1", "box", "agent-b", "second synthetic Agent task")
	if state, _, back := handlingNow(h, it); state != HandlingAgent || back != HandlingWaiting {
		t.Fatalf("Agent startup lost waiting: handling %q, back %q", state, back)
	}

	h.f.setSessions("box",
		proto.Session{MachineID: "box", SessionID: "agent-a", Status: proto.StatusIdle,
			LastText: "Older synthetic Agent result", UpdatedAt: time.Now().UTC()},
		proto.Session{MachineID: "box", SessionID: "agent-b", Status: proto.StatusWorking,
			LastText: "Newer synthetic Agent still running", UpdatedAt: time.Now().UTC()})
	h.inboxAgentTurnSettled(ctx, "box", "agent-a")
	h.deliverPending(ctx)
	if state, _, back := handlingNow(h, it); state != HandlingAgent || back != HandlingWaiting {
		t.Errorf("older Agent stop changed the newer Agent's restoration point: handling %q, back %q; want agent backed by waiting", state, back)
	}

	h.f.setSessions("box",
		proto.Session{MachineID: "box", SessionID: "agent-a", Status: proto.StatusIdle,
			LastText: "Older synthetic Agent result", UpdatedAt: time.Now().UTC()},
		proto.Session{MachineID: "box", SessionID: "agent-b", Status: proto.StatusIdle,
			LastText: "Newer synthetic Agent result", UpdatedAt: time.Now().UTC().Add(time.Second)})
	h.inboxAgentTurnSettled(ctx, "box", "agent-b")
	h.deliverPending(ctx)
	if _, err := suggestTool(t, h, `{"recap":"Both synthetic Agents finished; still waiting for the quote"}`); err != nil {
		t.Fatal(err)
	}
	state, wake, back := handlingNow(h, it)
	if state != HandlingWaiting || wake {
		t.Fatalf("recap after older and current Agent stops ended in handling %q, wake %v, back %q; want waiting", state, wake, back)
	}
	wantCheckIn(t, h, it, 4*time.Hour, "waiting restoration after two Agent stops")
	followCheckNoFailure(t, h, it)
}
