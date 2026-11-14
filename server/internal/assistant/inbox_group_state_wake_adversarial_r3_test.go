package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
)

// A waiting matter was interrupted by an instruction that started an Agent. A source reply during that Agent's work
// must not erase the waiting restoration point: after the Agent stops, a recap-only turn still returns to waiting.
func TestAdversarialInboxGroupStateWakeReplyDuringWaitingBackedAgentRestoresWaiting(t *testing.T) {
	ctx := context.Background()
	h, _, it := handledHub(t)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)
	h.inbox.mu.Lock()
	it.Source = inboxGroupStateSource
	it.Group = &InboxGroup{Connector: inboxGroupStateSource, Thread: inboxGroupStateThread}
	h.inbox.mu.Unlock()

	if _, err := suggestTool(t, h, `{"recap":"Waiting for the synthetic quote","follow":{"who":"Example teammate","next":"Wait for the quote","check_in":"4h"}}`); err != nil {
		t.Fatal(err)
	}
	if code, out := suggestionCall(t, h, it.ID, `{"version":0,"action":"instruct","text":"Investigate the synthetic request"}`); code != 200 {
		t.Fatalf("instruct: %d %v", code, out)
	}
	h.deliverPending(ctx)
	h.inboxAgentStarted(ctx, "s1", "box", "s-sub", "Investigate the synthetic request")
	if state, _, back := handlingNow(h, it); state != HandlingAgent || back != HandlingWaiting {
		t.Fatalf("Agent startup did not preserve waiting: handling %q, back %q", state, back)
	}

	report := inboxGroupStateReport(it, false, connector.DoneReplied)
	rest, changed := h.applyGroupStates(ctx, inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateChange(t, rest, changed, it)
	if state, _, _ := handlingNow(h, it); state != HandlingAgent {
		t.Fatalf("source reply changed Agent handling to %q", state)
	}
	wantCheckIn(t, h, it, inboxFollowDefault, "source reply during waiting-backed Agent work")
	h.deliverPending(ctx)

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
		t.Fatalf("recap after the source reply and Agent stop lost waiting: handling %q, wake %v, back %q", state, wake, back)
	}
	f := followOf(h, it)
	if f == nil || f.Who != "Example teammate" || f.Next != "Wait for the quote" || f.Checks != 0 || f.Due {
		t.Fatalf("follow after the source reply and Agent stop = %+v", f)
	}
	wantCheckIn(t, h, it, inboxFollowDefault, "waiting restoration after source reply and Agent stop")
	followCheckNoFailure(t, h, it)
}
