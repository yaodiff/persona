package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
)

// A follow handed in while the current Agent still runs is the wait behind that Agent, not a request to return to
// Needs you when the Agent's later stop turn has no new suggestion. The stop's recap-only fallback must resume it.
func TestAdversarialInboxGroupStateWakeR8FollowSurvivesCurrentAgentStop(t *testing.T) {
	ctx := context.Background()
	h := inboxGroupStateTestHub(t, true)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)
	it := inboxGroupStateTestItem(h, "event-r8-follow-agent-stop", true)
	h.inboxAgentStarted(ctx, "session-1", "box", "agent-session", "synthetic Agent task")

	report := inboxGroupStateReport(it, false, connector.DoneReplied)
	rest, changed := h.applyGroupStates(ctx, inboxGroupStateTestSource(), []connector.InboxThing{report})
	requireInboxGroupStateChange(t, rest, changed, it)
	h.deliverPending(ctx)
	_, _, err := h.suggest(ctx, "session-1", suggestIn{
		Recap:  "The owner replied; keep waiting while the synthetic Agent finishes",
		Follow: &suggestFollow{Who: "Example teammate", Next: "Wait for the answer", CheckIn: "4h"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if state, _, _ := handlingNow(h, it); state != HandlingAgent {
		t.Fatalf("follow replaced the running Agent with %q", state)
	}

	h.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "agent-session", Status: proto.StatusIdle,
		LastText: "Synthetic Agent result", UpdatedAt: time.Now().UTC()})
	h.inboxAgentTurnSettled(ctx, "box", "agent-session")
	h.deliverPending(ctx)
	if state, _, _ := handlingNow(h, it); state != HandlingLooking {
		t.Fatalf("the current Agent's stop left handling %q, want looking", state)
	}
	if _, _, err := h.suggest(ctx, "session-1", suggestIn{Recap: "The Agent finished; the wait is unchanged"}); err != nil {
		t.Fatal(err)
	}

	state, wake, back := handlingNow(h, it)
	if state != HandlingWaiting || wake {
		t.Fatalf("the stop's recap discarded the submitted follow: handling %q, wake %v, back %q; want waiting", state, wake, back)
	}
	wantCheckIn(t, h, it, 4*time.Hour, "follow restored after the current Agent stop")
}
