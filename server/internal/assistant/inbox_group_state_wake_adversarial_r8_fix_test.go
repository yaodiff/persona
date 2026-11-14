package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/proto"
)

// A follow submitted while the current Agent runs must also survive an empty or timed-out processing turn after that
// Agent stops. Neither sweep outcome is a handling failure: both restore the same four-hour wait as a recap-only turn.
func TestAdversarialInboxGroupStateWakeR8FollowSurvivesCurrentAgentStopSweep(t *testing.T) {
	for _, tc := range []struct {
		name           string
		timeoutMinutes int
		status         string
	}{
		{name: "no_draft", timeoutMinutes: 15, status: proto.StatusIdle},
		{name: "timeout", timeoutMinutes: 1, status: proto.StatusWorking},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h := inboxGroupStateTestHub(t, true)
			h.inbox.handing.Lock()
			t.Cleanup(h.inbox.handing.Unlock)
			it := inboxGroupStateTestItem(h, "event-r8-follow-agent-stop-"+tc.name, true)
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
			if state, _, back := handlingNow(h, it); state != HandlingLooking || back != HandlingWaiting {
				t.Fatalf("the current Agent's stop left handling %q with back %q, want looking behind waiting", state, back)
			}

			started := time.Now().Add(-2 * time.Minute)
			h.inbox.mu.Lock()
			it.Agent.StartedAt = &started
			h.inbox.mu.Unlock()
			h.f.setSessions("server", proto.Session{SessionID: "session-1", Status: tc.status})
			h.sweepInboxAgents(ctx, InboxAgentConfig{Enabled: true, TimeoutMinutes: tc.timeoutMinutes}, time.Now())

			state, wake, back := handlingNow(h, it)
			if state != HandlingWaiting || wake {
				t.Fatalf("%s after the current Agent's stop discarded the follow: handling %q, wake %v, back %q; want waiting",
					tc.name, state, wake, back)
			}
			wantCheckIn(t, h, it, 4*time.Hour, tc.name+" follow restored after the current Agent stop")
			followCheckNoFailure(t, h, it)
		})
	}
}
