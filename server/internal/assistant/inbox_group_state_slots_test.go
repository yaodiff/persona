package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/prompts"
)

func wakeSlotsStateTestConnectorItem(h *inboxHub, id, session string) *InboxItem {
	it := inboxGroupStateTestItem(h, id, true)
	h.inbox.mu.Lock()
	it.Agent.SessionID = session
	it.Agent.Pending = nil
	it.Agent.Wake = false
	it.Agent.Back = ""
	it.Agent.StartedAt = nil
	it.Handling.SessionID = session
	h.inbox.setHandling(it, HandlingDone, "", time.Now().UTC())
	h.inbox.mu.Unlock()
	return it
}

func wakeSlotsStateTestCollect(t *testing.T, s *Service, it *InboxItem, read bool, done string) {
	t.Helper()
	src := inboxGroupStateTestSource()
	src.Things = []connector.InboxThing{inboxGroupStateReport(it, read, done)}
	if err := s.InboxCollected(context.Background(), assistantwire.InboxRound{Sources: []assistantwire.InboxSource{src}}); err != nil {
		t.Fatalf("collect the source state: %v", err)
	}
}

func wakeSlotsStateTestHandling(s *Service, it *InboxItem) (state string, wake bool, back string) {
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	if it.Handling != nil {
		state = it.Handling.State
	}
	if it.Agent != nil {
		wake, back = it.Agent.Wake, it.Agent.Back
	}
	return state, wake, back
}

func wakeSlotsStateTestPending(t *testing.T, s *Service, it *InboxItem) inboxNote {
	t.Helper()
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	if it.Agent == nil || len(it.Agent.Pending) != 1 {
		t.Fatalf("pending notes for %s = %+v, want exactly one", it.ID, it.Agent)
	}
	return it.Agent.Pending[0]
}

func wakeSlotsStateTestNoPending(t *testing.T, s *Service, it *InboxItem) {
	t.Helper()
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	if it.Agent != nil && len(it.Agent.Pending) != 0 {
		t.Fatalf("pending notes for %s after delivery = %+v, want none", it.ID, it.Agent.Pending)
	}
}

func wakeSlotsStateTestReload(t *testing.T, h *inboxHub) *Service {
	t.Helper()
	reloaded := New(Options{Hub: h.f, Store: func() Store { return h.st }, Logf: testLogf(t)})
	if err := reloaded.inboxReady(context.Background()); err != nil {
		t.Fatalf("reload the inbox: %v", err)
	}
	return reloaded
}

func wakeSlotsStateTestItem(t *testing.T, s *Service, id string) *InboxItem {
	t.Helper()
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	it := s.inbox.items[id]
	if it == nil {
		t.Fatalf("%s was lost on reload", id)
	}
	return it
}

func wakeSlotsStateTestRelease(t *testing.T, s *Service, it *InboxItem) {
	t.Helper()
	s.inbox.mu.Lock()
	s.inbox.resumeInboxWaiting(it, time.Now().UTC())
	s.inbox.mu.Unlock()
	if failed := s.saveInbox(context.Background(), []*InboxItem{it}); failed[it.ID] {
		t.Fatalf("save the released conversation for %s", it.ID)
	}
}

func wakeSlotsStateTestRequireOnlySend(t *testing.T, h *inboxHub, count int, session, text string) {
	t.Helper()
	sent := h.f.sentToAgents()
	if len(sent) != count {
		t.Fatalf("session sends = %d, want %d: %v", len(sent), count, sent)
	}
	last := sent[len(sent)-1]
	if !strings.HasPrefix(last, session+": ") {
		t.Fatalf("the latest send went to the wrong conversation: %q, want %s", last, session)
	}
	if text != "" && last != session+": "+text {
		t.Fatalf("the deferred notification changed:\nwant %s: %s\n got %s", session, text, last)
	}
}

// A connector report collected during a handoff pass competes with that pass's due follow-up for the same slot. Both
// replied and read retain their source-state records and their complete notification while deferred, including on reload.
func TestInboxWakeSlotsStateConnectorAndScheduledCheckCompete(t *testing.T) {
	for _, tc := range []struct {
		name          string
		read          bool
		done          string
		note          string
		deferredState string
	}{
		{name: "replied", done: connector.DoneReplied, note: prompts.NoteSelfReplied, deferredState: HandlingWaiting},
		{name: "read", read: true, note: prompts.NoteSelfRead, deferredState: HandlingDone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			h, due, _ := followCheckHub(t, 0)
			h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled, c.Agent.Concurrency = true, 1 })
			reported := wakeSlotsStateTestConnectorItem(h, "state-"+tc.name, "state-session-"+tc.name)
			beforeChain := len(reported.Chain)
			if failed := h.saveInbox(ctx, []*InboxItem{due, reported}); len(failed) != 0 {
				t.Fatalf("save the fixtures: %v", failed)
			}

			h.inbox.passHook = func() {
				h.inbox.passHook = nil
				wakeSlotsStateTestCollect(t, h.Service, reported, tc.read, tc.done)
			}
			h.handPass(ctx)

			if f := followOf(h, due); f == nil || f.Checks != 1 || f.CheckAt != nil {
				t.Fatalf("the due follow-up did not take the pass's slot: %+v", f)
			}
			state, _, _ := wakeSlotsStateTestHandling(h.Service, reported)
			if state != tc.deferredState {
				t.Fatalf("connector wake at full capacity has handling %q, want deferred %q", state, tc.deferredState)
			}
			note := wakeSlotsStateTestPending(t, h.Service, reported)
			wantText := ""
			if tc.done == connector.DoneReplied {
				wantText = inboxGroupStateAt.In(h.location(ctx)).Format("2006-01-02 15:04")
				entries := inboxGroupStateSelfReplies(reported)
				if len(entries) != 1 || entries[0].Link == nil || entries[0].Link.Position != reported.Last {
					t.Fatalf("deferred reply chain = %+v, want one source-position record", entries)
				}
			} else {
				if !reported.Read || len(reported.Chain) != beforeChain {
					t.Fatalf("deferred read changed the chain or was not recorded: read=%v chain=%+v", reported.Read, reported.Chain)
				}
			}
			if note.Name != tc.note || note.Text != wantText {
				t.Fatalf("deferred notification = %+v, want %s with text %q", note, tc.note, wantText)
			}
			if got := h.f.sentToAgents(); len(got) != 1 || !strings.HasPrefix(got[0], "s1: ") {
				t.Fatalf("a connector wake shared the slot instead of waiting: %v", got)
			}
			if !h.st.saved(reported.ID) {
				t.Fatal("the deferred connector state was not persisted")
			}

			// Repeating an undelivered report must not duplicate its chain record or notification.
			wakeSlotsStateTestCollect(t, h.Service, reported, tc.read, tc.done)
			if again := wakeSlotsStateTestPending(t, h.Service, reported); again != note {
				t.Fatalf("repeated report changed the pending notification: before %+v, after %+v", note, again)
			}
			if len(reported.Chain) != beforeChain && tc.read || len(inboxGroupStateSelfReplies(reported)) != 1 && !tc.read {
				t.Fatalf("repeated report duplicated the chain: %+v", reported.Chain)
			}

			reloaded := wakeSlotsStateTestReload(t, h)
			dueReloaded := wakeSlotsStateTestItem(t, reloaded, due.ID)
			reportedReloaded := wakeSlotsStateTestItem(t, reloaded, reported.ID)
			state, _, _ = wakeSlotsStateTestHandling(reloaded, reportedReloaded)
			if state != tc.deferredState {
				t.Fatalf("deferred handling after reload = %q, want %q", state, tc.deferredState)
			}
			if kept := wakeSlotsStateTestPending(t, reloaded, reportedReloaded); kept != note {
				t.Fatalf("notification changed on reload: before %+v, after %+v", note, kept)
			}

			wakeSlotsStateTestRelease(t, reloaded, dueReloaded)
			wantNotification := renderNote(note, reloaded.owner(ctx))
			reloaded.handPass(ctx)

			state, wake, _ := wakeSlotsStateTestHandling(reloaded, reportedReloaded)
			if state != HandlingLooking || wake {
				t.Fatalf("admitted connector wake after reload: handling %q, wake %v", state, wake)
			}
			wakeSlotsStateTestNoPending(t, reloaded, reportedReloaded)
			wakeSlotsStateTestRequireOnlySend(t, h, 2, reported.Agent.SessionID, wantNotification)
			if tc.read && len(reportedReloaded.Chain) != beforeChain {
				t.Fatalf("read admission added a chain entry: %+v", reportedReloaded.Chain)
			}
			if !tc.read && len(inboxGroupStateSelfReplies(reportedReloaded)) != 1 {
				t.Fatalf("reply admission duplicated its chain entry: %+v", reportedReloaded.Chain)
			}

			// The source may repeat its report after delivery too; it is still the same read or reply.
			wakeSlotsStateTestCollect(t, reloaded, reportedReloaded, tc.read, tc.done)
			reloaded.handPass(ctx)
			if got := len(h.f.sentToAgents()); got != 2 {
				t.Fatalf("the delivered source state woke the conversation again: %d sends", got)
			}
		})
	}
}

// Deferred programmatic wakes retain their arrival order rather than map or item-id order. A restart does not lose
// their records or notifications, and each newly free slot admits exactly the oldest waiting wake.
func TestInboxWakeSlotsStateDeferredWakesPersistFIFO(t *testing.T) {
	ctx := context.Background()
	h := agentHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled, c.Agent.Concurrency = true, 1 })
	now := time.Now().UTC()
	busy := actionItem("busy-looking", now)
	busy.Agent = &InboxAgent{State: AgentRunning, MachineID: "server", SessionID: "busy-session", Of: busy.Last}
	h.inbox.mu.Lock()
	h.inbox.items[busy.ID] = busy
	h.inbox.setHandling(busy, HandlingLooking, "", now)
	h.inbox.mu.Unlock()
	// The lexically later item arrives first, so sorting deferred items by id cannot accidentally satisfy FIFO.
	first := wakeSlotsStateTestConnectorItem(h, "z-first-replied", "z-first-session")
	second := wakeSlotsStateTestConnectorItem(h, "a-second-read", "a-second-session")
	if failed := h.saveInbox(ctx, []*InboxItem{busy, first, second}); len(failed) != 0 {
		t.Fatalf("save the fixtures: %v", failed)
	}

	wakeSlotsStateTestCollect(t, h.Service, first, false, connector.DoneReplied)
	// Keep the persisted wake timestamps distinct as well as the collection order: the second wake is collected only
	// once the clock has passed the first one's, which is at once where the clock reads finer than a collection.
	h.inbox.mu.Lock()
	firstAt := first.Agent.DeferredAt
	h.inbox.mu.Unlock()
	if firstAt == nil {
		t.Fatal("the first wake was not deferred at full capacity")
	}
	waitUntil(t, func() bool { return time.Now().After(*firstAt) })
	wakeSlotsStateTestCollect(t, h.Service, second, true, "")
	h.handPass(ctx)

	if got := h.f.sentToAgents(); len(got) != 0 {
		t.Fatalf("deferred wakes were delivered at full capacity: %v", got)
	}
	firstNote := wakeSlotsStateTestPending(t, h.Service, first)
	secondNote := wakeSlotsStateTestPending(t, h.Service, second)
	if firstNote.Name != prompts.NoteSelfReplied || secondNote.Name != prompts.NoteSelfRead {
		t.Fatalf("deferred notes = %+v / %+v", firstNote, secondNote)
	}
	if state, _, _ := wakeSlotsStateTestHandling(h.Service, first); state != HandlingWaiting {
		t.Fatalf("first deferred reply has handling %q, want waiting", state)
	}
	if state, _, _ := wakeSlotsStateTestHandling(h.Service, second); state != HandlingDone {
		t.Fatalf("second deferred read has handling %q, want done", state)
	}
	if len(inboxGroupStateSelfReplies(first)) != 1 || !second.Read {
		t.Fatalf("deferred source records were lost: first chain %+v, second read %v", first.Chain, second.Read)
	}
	secondChain := len(second.Chain)

	// Polls keep repeating both states while they wait. Reverse their order to prove idempotence is per item.
	wakeSlotsStateTestCollect(t, h.Service, second, true, "")
	wakeSlotsStateTestCollect(t, h.Service, first, false, connector.DoneReplied)
	if got := wakeSlotsStateTestPending(t, h.Service, first); got != firstNote {
		t.Fatalf("first pending note changed on a repeated report: %+v", got)
	}
	if got := wakeSlotsStateTestPending(t, h.Service, second); got != secondNote {
		t.Fatalf("second pending note changed on a repeated report: %+v", got)
	}
	if len(inboxGroupStateSelfReplies(first)) != 1 || len(second.Chain) != secondChain {
		t.Fatalf("repeated deferred reports duplicated chain records: %+v / %+v", first.Chain, second.Chain)
	}

	reloaded := wakeSlotsStateTestReload(t, h)
	busyReloaded := wakeSlotsStateTestItem(t, reloaded, busy.ID)
	firstReloaded := wakeSlotsStateTestItem(t, reloaded, first.ID)
	secondReloaded := wakeSlotsStateTestItem(t, reloaded, second.ID)
	if got := wakeSlotsStateTestPending(t, reloaded, firstReloaded); got != firstNote {
		t.Fatalf("first notification changed on reload: %+v", got)
	}
	if got := wakeSlotsStateTestPending(t, reloaded, secondReloaded); got != secondNote {
		t.Fatalf("second notification changed on reload: %+v", got)
	}

	wakeSlotsStateTestRelease(t, reloaded, busyReloaded)
	firstNotification := renderNote(firstNote, reloaded.owner(ctx))
	reloaded.handPass(ctx)
	if state, wake, _ := wakeSlotsStateTestHandling(reloaded, firstReloaded); state != HandlingLooking || wake {
		t.Fatalf("oldest deferred wake after reload: handling %q, wake %v", state, wake)
	}
	if state, _, _ := wakeSlotsStateTestHandling(reloaded, secondReloaded); state != HandlingDone {
		t.Fatalf("younger wake was admitted beside the oldest: handling %q", state)
	}
	wakeSlotsStateTestNoPending(t, reloaded, firstReloaded)
	wakeSlotsStateTestRequireOnlySend(t, h, 1, first.Agent.SessionID, firstNotification)
	if got := wakeSlotsStateTestPending(t, reloaded, secondReloaded); got != secondNote {
		t.Fatalf("younger notification was lost while waiting: %+v", got)
	}

	wakeSlotsStateTestRelease(t, reloaded, firstReloaded)
	secondNotification := renderNote(secondNote, reloaded.owner(ctx))
	reloaded.handPass(ctx)
	if state, wake, _ := wakeSlotsStateTestHandling(reloaded, secondReloaded); state != HandlingLooking || wake {
		t.Fatalf("second deferred wake: handling %q, wake %v", state, wake)
	}
	wakeSlotsStateTestNoPending(t, reloaded, secondReloaded)
	wakeSlotsStateTestRequireOnlySend(t, h, 2, second.Agent.SessionID, secondNotification)
	if len(inboxGroupStateSelfReplies(firstReloaded)) != 1 || len(secondReloaded.Chain) != secondChain {
		t.Fatalf("admission duplicated source records: %+v / %+v", firstReloaded.Chain, secondReloaded.Chain)
	}

	wakeSlotsStateTestCollect(t, reloaded, firstReloaded, false, connector.DoneReplied)
	wakeSlotsStateTestCollect(t, reloaded, secondReloaded, true, "")
	reloaded.handPass(ctx)
	if got := len(h.f.sentToAgents()); got != 2 {
		t.Fatalf("repeated reports duplicated delivered wakes: %d sends", got)
	}
}
