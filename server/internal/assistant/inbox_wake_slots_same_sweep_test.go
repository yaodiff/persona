package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
)

func wakeSlotsSameSweepTestDeferredStopNotes(it *InboxItem) []inboxNote {
	var out []inboxNote
	if it.Agent != nil {
		for _, note := range it.Agent.Pending {
			if note.Deferred && note.Name == prompts.NoteAgentStopped {
				out = append(out, note)
			}
		}
	}
	return out
}

// The sweep can be the first observer of a settled current Agent. If capacity defers that newly recorded stop, the
// same sweep must not mistake the item's old Agent start time for a still-running turn and discard the held result.
func TestInboxWakeSlotsRound1FixSameSweepDeferredSettledStopSurvives(t *testing.T) {
	ctx := context.Background()
	h, it, sess := newInboxAgentStoppedHub(t)
	cfg := InboxAgentConfig{Enabled: true, Concurrency: 1, TimeoutMinutes: 1}
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent = cfg })
	busy := wakeSlotsAdversarialTestOccupyOnlySlot(h, "wake-slots-same-sweep-busy")
	started := time.Now().Add(-5 * time.Minute)
	h.inbox.mu.Lock()
	it.Agent.StartedAt = &started
	it.Agent.Back = ""
	h.inbox.mu.Unlock()

	// Do not record the stop first: this sweep must discover it through the reachable node.
	h.sweepInboxAgents(ctx, cfg, time.Now())

	stored := wakeSlotsAgentTestStoredItem(t, h, it.ID)
	if stored.Agent == nil || stored.Handling == nil {
		t.Fatalf("the same sweep lost the item's handling state: %+v", stored)
	}
	stops := wakeSlotsAgentTestStopEntries(&stored)
	stopNotes := wakeSlotsAgentTestStopNotes(&stored)
	deferred := wakeSlotsSameSweepTestDeferredStopNotes(&stored)
	for _, entry := range stored.Chain {
		if entry.Kind == ChainFailed {
			t.Fatalf("a settled stop discovered by the same sweep timed out: %+v", stored.Chain)
		}
	}
	if stored.Handling.State != HandlingAgent || stored.Agent.DeferredAt == nil {
		t.Fatalf("the same sweep did not preserve the deferred stop: handling %+v, agent %+v", stored.Handling, stored.Agent)
	}
	if len(stops) != 1 || len(stopNotes) != 1 || len(deferred) != 1 {
		t.Fatalf("durable stops %d, stop notes %d, deferred stop notes %d; want one of each: %+v", len(stops), len(stopNotes), len(deferred), stored.Agent)
	}
	if deferred[0].Text != sess.LastText || deferred[0].Machine != sess.MachineID || deferred[0].Session != sess.SessionID {
		t.Fatalf("the durable deferred notification lost the Agent result: %+v", deferred[0])
	}
	if got := wakeSlotsAgentTestDeliveries(h, sess.LastText); got != 0 {
		t.Fatalf("the held stop was delivered %d times at full capacity", got)
	}

	wakeSlotsAgentTestRelease(t, h, busy)
	h.checkInboxFollowUps(ctx, cfg, time.Now())
	h.deliverPending(ctx)
	toldConv(t, h, sess.LastText)
	if got := wakeSlotsAgentTestDeliveries(h, sess.LastText); got != 1 {
		t.Fatalf("the same-sweep stop was delivered %d times after capacity freed, want once", got)
	}
}
