package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// wakeSlotsAgentTestBusy adds a persisted conversation that already occupies the only looking slot. Its content has
// already been escalated, so a handoff pass cannot turn it into a second, new-message wake.
func wakeSlotsAgentTestBusy(t *testing.T, h *inboxHub) *InboxItem {
	t.Helper()
	now := time.Now().UTC()
	busy := actionItem("wake-slots-busy", now)
	busy.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "wake-slots-busy-session", Of: busy.Last, StartedAt: &now}
	h.inbox.mu.Lock()
	h.inbox.items[busy.ID] = busy
	h.inbox.setHandling(busy, HandlingLooking, "", now)
	h.inbox.mu.Unlock()
	if failed := h.saveInbox(context.Background(), []*InboxItem{busy}); failed[busy.ID] {
		t.Fatal("the busy conversation was not persisted")
	}
	return busy
}

func wakeSlotsAgentTestStoredItem(t *testing.T, h *inboxHub, id string) InboxItem {
	t.Helper()
	var found *InboxItem
	err := h.st.InboxItems(context.Background(), time.Now().Add(-inboxForget), func(raw []byte) error {
		var it InboxItem
		if json.Unmarshal(raw, &it) == nil && it.ID == id {
			copy := it
			found = &copy
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found == nil {
		t.Fatalf("item %s was not persisted", id)
	}
	return *found
}

func wakeSlotsAgentTestStopEntries(it *InboxItem) []InboxChainEntry {
	var out []InboxChainEntry
	for _, entry := range it.Chain {
		if entry.Kind == ChainAgentStopped {
			out = append(out, entry)
		}
	}
	return out
}

func wakeSlotsAgentTestStopNotes(it *InboxItem) []inboxNote {
	var out []inboxNote
	if it.Agent == nil {
		return nil
	}
	for _, note := range it.Agent.Pending {
		if note.Name == prompts.NoteAgentStopped {
			out = append(out, note)
		}
	}
	return out
}

func wakeSlotsAgentTestDeliveries(h *inboxHub, detail string) int {
	n := 0
	for _, sent := range h.f.sentToAgents() {
		if strings.Contains(sent, detail) {
			n++
		}
	}
	return n
}

// wakeSlotsAgentTestAssertDeferred checks the durable record, not merely the in-memory callback result. At full
// capacity the stop and its complete notification wait for another pass; the item must not join the looking count yet.
func wakeSlotsAgentTestAssertDeferred(t *testing.T, h *inboxHub, it *InboxItem, sess proto.Session) {
	t.Helper()
	if got := wakeSlotsAgentTestDeliveries(h, sess.LastText); got != 0 {
		t.Fatalf("Agent-stop wake was delivered %d times at full capacity", got)
	}
	stored := wakeSlotsAgentTestStoredItem(t, h, it.ID)
	stops := wakeSlotsAgentTestStopEntries(&stored)
	if len(stops) != 1 {
		t.Fatalf("persisted Agent-stop entries = %d, want exactly one: %+v", len(stops), stored.Chain)
	}
	stop := stops[0]
	if stop.Detail != sess.LastText || stop.Link == nil || stop.Link.MachineID != sess.MachineID || stop.Link.SessionID != sess.SessionID {
		t.Fatalf("persisted Agent-stop entry lost its result or link: %+v", stop)
	}
	notes := wakeSlotsAgentTestStopNotes(&stored)
	if len(notes) != 1 {
		t.Fatalf("persisted Agent-stop notifications = %d, want exactly one: %+v", len(notes), stored.Agent)
	}
	note := notes[0]
	if note.Text != sess.LastText || note.Machine != sess.MachineID || note.Session != sess.SessionID {
		t.Fatalf("persisted Agent-stop notification lost its content: %+v", note)
	}
	if stored.Handling == nil || stored.Handling.State == HandlingLooking {
		t.Fatalf("deferred Agent-stop wake joined the looking conversations at full capacity: %+v", stored.Handling)
	}
}

func wakeSlotsAgentTestRelease(t *testing.T, h *inboxHub, busy *InboxItem) {
	t.Helper()
	h.inbox.mu.Lock()
	h.inbox.setHandling(busy, HandlingWaiting, "", time.Now().UTC())
	h.inbox.mu.Unlock()
	if failed := h.saveInbox(context.Background(), []*InboxItem{busy}); failed[busy.ID] {
		t.Fatal("the released conversation was not persisted")
	}
}

func wakeSlotsAgentTestAssertAdmitted(t *testing.T, h *inboxHub, it *InboxItem, sess proto.Session) {
	t.Helper()
	toldConv(t, h, sess.LastText)
	if got := wakeSlotsAgentTestDeliveries(h, sess.LastText); got != 1 {
		t.Fatalf("deferred Agent-stop notification was delivered %d times after admission, want once", got)
	}
	stored := wakeSlotsAgentTestStoredItem(t, h, it.ID)
	if stored.Handling == nil || stored.Handling.State != HandlingLooking {
		t.Fatalf("admitted Agent-stop wake did not return the item to looking: %+v", stored.Handling)
	}
	if got := len(wakeSlotsAgentTestStopEntries(&stored)); got != 1 {
		t.Fatalf("admission duplicated the persisted Agent-stop entry: %d entries", got)
	}
}

func TestWakeSlotsAgentTestAgentStopDefersAtCapacityAndNextPassAdmits(t *testing.T) {
	ctx := context.Background()
	h, it, sess := newInboxAgentStoppedHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Concurrency = 1 })
	busy := wakeSlotsAgentTestBusy(t, h)

	if !h.recordInboxAgentStops(ctx, sess) {
		t.Fatal("the Agent stop was not saved")
	}
	h.handPass(ctx) // the same capacity-full pass must leave the wake deferred
	wakeSlotsAgentTestAssertDeferred(t, h, it, sess)

	// A repeated observation of the same stopped turn is idempotent even while its wake is waiting for capacity.
	if !h.recordInboxAgentStops(ctx, sess) {
		t.Fatal("the duplicate Agent stop was not handled")
	}
	h.handPass(ctx)
	wakeSlotsAgentTestAssertDeferred(t, h, it, sess)

	wakeSlotsAgentTestRelease(t, h, busy)
	h.handPass(ctx)
	wakeSlotsAgentTestAssertAdmitted(t, h, it, sess)

	before := wakeSlotsAgentTestDeliveries(h, sess.LastText)
	if !h.recordInboxAgentStops(ctx, sess) {
		t.Fatal("the post-admission duplicate Agent stop was not handled")
	}
	h.deliverPending(ctx)
	if got := wakeSlotsAgentTestDeliveries(h, sess.LastText); got != before {
		t.Fatalf("the admitted stop was delivered again by a duplicate observation: %d sends, want %d", got, before)
	}
}

func TestWakeSlotsAgentTestAgentStopDeferredSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	h, it, sess := newInboxAgentStoppedHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Concurrency = 1 })
	busy := wakeSlotsAgentTestBusy(t, h)

	if !h.recordInboxAgentStops(ctx, sess) {
		t.Fatal("the Agent stop was not saved")
	}
	h.handPass(ctx)
	wakeSlotsAgentTestAssertDeferred(t, h, it, sess)

	// Restart with the same hub and durable inbox rows, but no in-memory inbox state or capacity counter.
	restartedService := New(Options{Hub: h.f, Store: func() Store { return h.st }, Logf: testLogf(t)})
	restarted := &inboxHub{Service: restartedService, f: h.f, in: h.in, st: h.st}
	h.in.svc = restartedService
	restarted.inbox.init()
	if err := restarted.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.inbox.mu.Lock()
	reloadedItem := restarted.inbox.items[it.ID]
	reloadedBusy := restarted.inbox.items[busy.ID]
	restarted.inbox.mu.Unlock()
	if reloadedItem == nil || reloadedBusy == nil {
		t.Fatalf("restart lost the deferred item or the busy conversation: item %p, busy %p", reloadedItem, reloadedBusy)
	}
	if reloadedBusy.Handling == nil || reloadedBusy.Handling.State != HandlingLooking {
		t.Fatalf("restart lost the busy conversation's looking state: %+v", reloadedBusy.Handling)
	}
	wakeSlotsAgentTestAssertDeferred(t, restarted, reloadedItem, sess)

	// The reloaded busy conversation still consumes the persisted slot, so another pass cannot admit the stop yet.
	restarted.handPass(ctx)
	wakeSlotsAgentTestAssertDeferred(t, restarted, reloadedItem, sess)

	wakeSlotsAgentTestRelease(t, restarted, reloadedBusy)
	restarted.handPass(ctx)
	wakeSlotsAgentTestAssertAdmitted(t, restarted, reloadedItem, sess)
}
