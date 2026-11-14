package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// newInboxAgentStoppedHub has one open item, its processing conversation s1, and a linked child Agent on box.
// Handoff passes are held so a test only observes the stop it drives, not an unrelated sweep.
func newInboxAgentStoppedHub(t *testing.T) (*inboxHub, *InboxItem, proto.Session) {
	t.Helper()
	h, _, it := handledHub(t)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)
	h.inboxAgentStarted(context.Background(), "s1", "box", "s-sub", "check the example")
	waitNeedsYouIdle(t, h.Service)
	sess := proto.Session{MachineID: "box", SessionID: "s-sub", Status: proto.StatusIdle,
		LastText: "the example is fixed", UpdatedAt: time.Now().UTC()}
	h.f.setSessions("box", sess)
	return h, it, sess
}

func inboxAgentStoppedCount(it *InboxItem) int {
	n := 0
	for _, e := range it.Chain {
		if e.Kind == ChainAgentStopped {
			n++
		}
	}
	return n
}

func latestInboxAgentStop(t *testing.T, it *InboxItem) InboxChainEntry {
	t.Helper()
	for i := len(it.Chain) - 1; i >= 0; i-- {
		if it.Chain[i].Kind == ChainAgentStopped {
			return it.Chain[i]
		}
	}
	t.Fatal("no agent_stopped entry")
	return InboxChainEntry{}
}

func inboxAgentStoppedSnapshot(t *testing.T, h *inboxHub, it *InboxItem) InboxItem {
	t.Helper()
	h.inbox.mu.Lock()
	raw, err := json.Marshal(it)
	h.inbox.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var snapshot InboxItem
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// inboxAgentStopFailStore rejects only the next row carrying a new stop. A buggy timeout that follows gets a working
// store, so the rollback test can tell "no timeout was attempted" from "the timeout write failed too".
type inboxAgentStopFailStore struct {
	*inboxMemStore
	mu       sync.Mutex
	failStop bool
}

func (s *inboxAgentStopFailStore) PutInboxItem(ctx context.Context, id string, data []byte, updatedAt time.Time, doneAt *time.Time) error {
	s.mu.Lock()
	fail := s.failStop && strings.Contains(string(data), `"kind":"agent_stopped"`)
	if fail {
		s.failStop = false
	}
	s.mu.Unlock()
	if fail {
		return errMuteStoreDown
	}
	return s.inboxMemStore.PutInboxItem(ctx, id, data, updatedAt, doneAt)
}

func TestInboxAgentStoppedRecordsAndWakesOnce(t *testing.T) {
	h, it, sess := newInboxAgentStoppedHub(t)
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")

	snapshot := inboxAgentStoppedSnapshot(t, h, it)
	e := latestInboxAgentStop(t, &snapshot)
	if e.Via != ViaAgent || e.Link == nil || e.Link.MachineID != "box" || e.Link.SessionID != "s-sub" ||
		e.Text != "Agent 停了" || e.Detail != sess.LastText || e.TurnAt == nil || !e.TurnAt.Equal(sess.UpdatedAt) {
		t.Fatalf("stop entry: %+v", e)
	}
	if snapshot.Handling.State != HandlingLooking || snapshot.Agent.StartedAt == nil {
		t.Fatalf("handling did not return to the conversation: %+v %+v", snapshot.Handling, snapshot.Agent)
	}
	for i, entry := range snapshot.listed().Chain {
		if entry.TurnAt != nil {
			t.Fatalf("listed chain %d exposes node bookkeeping: %+v", i, entry)
		}
	}
	told := toldConv(t, h, "Agent 停了")
	for _, want := range []string{sess.LastText, "inbox_suggest", "s-sub"} {
		if !strings.Contains(told, want) {
			t.Errorf("notification lacks %q:\n%s", want, told)
		}
	}

	before := len(h.f.sentToAgents())
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
	if got := inboxAgentStoppedCount(it); got != 1 || len(h.f.sentToAgents()) != before {
		t.Fatalf("the same stop was reported again: stops %d, sends %d → %d", got, before, len(h.f.sentToAgents()))
	}
}

func TestInboxAgentStoppedFailureIncludesError(t *testing.T) {
	h, it, sess := newInboxAgentStoppedHub(t)
	sess.TurnError = "API Error: example failed\nwith another line"
	sess.Background = []proto.BackgroundTask{{}} // failure is a stop even while background work remains
	h.f.setSessions("box", sess)
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")

	snapshot := inboxAgentStoppedSnapshot(t, h, it)
	e := latestInboxAgentStop(t, &snapshot)
	if e.Text != "Agent 失败了" || e.Detail != "API Error: example failed with another line" {
		t.Fatalf("failure entry: %+v", e)
	}
	told := toldConv(t, h, "Agent 失败了")
	if !strings.Contains(told, e.Detail) {
		t.Fatalf("failure notice lacks the error:\n%s", told)
	}
}

func TestInboxAgentStoppedWaitsForMoreWork(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*inboxHub, *proto.Session)
	}{
		{"working", func(_ *inboxHub, s *proto.Session) { s.Status = proto.StatusWorking }},
		{"starting", func(_ *inboxHub, s *proto.Session) { s.Starting = true }},
		{"needs you", func(_ *inboxHub, s *proto.Session) { s.State = proto.StateNeedsYou }},
		{"pending interaction", func(_ *inboxHub, s *proto.Session) { s.PendingInteractions = 1 }},
		{"blocked", func(_ *inboxHub, s *proto.Session) { s.Status = proto.StatusBlocked }},
		{"queued input", func(_ *inboxHub, s *proto.Session) { s.Queued = []string{"continue"} }},
		{"queued relay", func(h *inboxHub, _ *proto.Session) { h.queueRelay("s-sub", relayMsg{From: "another"}) }},
		{"background", func(_ *inboxHub, s *proto.Session) { s.Background = []proto.BackgroundTask{{}} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, it, sess := newInboxAgentStoppedHub(t)
			tc.setup(h, &sess)
			h.f.setSessions("box", sess)
			h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
			if got := inboxAgentStoppedCount(it); got != 0 {
				t.Fatalf("recorded %d stops while more work was pending", got)
			}
			if it.Handling.State != HandlingAgent || len(h.f.sentToAgents()) != 0 {
				t.Fatalf("unexpected wake: %+v, sent %v", it.Handling, h.f.sentToAgents())
			}
		})
	}
}

func TestInboxAgentStoppedAfterWorkingAgain(t *testing.T) {
	h, it, sess := newInboxAgentStoppedHub(t)
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
	snapshot := inboxAgentStoppedSnapshot(t, h, it)
	first := latestInboxAgentStop(t, &snapshot)

	sess.Status = proto.StatusWorking
	sess.UpdatedAt = first.TurnAt.Add(time.Second)
	h.f.setSessions("box", sess)
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
	if got := inboxAgentStoppedCount(it); got != 1 {
		t.Fatalf("working again recorded a stop: %d", got)
	}
	sess.Status = proto.StatusIdle
	sess.LastText = "the second pass is fixed"
	h.f.setSessions("box", sess)
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
	if got := inboxAgentStoppedCount(it); got != 2 || latestInboxAgentStop(t, it).Detail != sess.LastText {
		t.Fatalf("the second stop was not recorded: %d, %+v", got, it.Chain)
	}
	toldConv(t, h, sess.LastText)
}

func TestInboxAgentStopDedupUsesOnlyNodeTime(t *testing.T) {
	turn := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	it := &InboxItem{Chain: []InboxChainEntry{
		{Kind: ChainAgent, At: turn.Add(time.Hour), Link: &InboxChainLink{Type: LinkSession, MachineID: "box", SessionID: "s-sub"}},
		{Kind: ChainAgentStopped, At: turn.Add(2 * time.Hour), TurnAt: &turn, Link: &InboxChainLink{Type: LinkSession, MachineID: "box", SessionID: "s-sub"}},
	}}
	sess := proto.Session{MachineID: "box", SessionID: "s-sub", UpdatedAt: turn}
	if linked, duplicate := inboxAgentStopDuplicate(it, sess); !linked || !duplicate {
		t.Fatalf("hub display time was mixed into the node-time comparison: linked %v, duplicate %v", linked, duplicate)
	}
	sess.UpdatedAt = turn.Add(time.Nanosecond)
	if _, duplicate := inboxAgentStopDuplicate(it, sess); duplicate {
		t.Fatal("new node activity was swallowed by the hub-side display time")
	}
	var zero time.Time
	it.Chain[1].TurnAt = &zero
	sess.UpdatedAt = zero
	if _, duplicate := inboxAgentStopDuplicate(it, sess); !duplicate {
		t.Fatal("a zero node timestamp did not deduplicate the same stop")
	}
	if got := inboxAgentStopDisplayTime(it, sess, turn.Add(time.Hour)); !got.Equal(turn.Add(2 * time.Hour)) {
		t.Fatalf("display time went behind the existing chain or followed the zero node time: %s", got)
	}
}

func TestInboxAgentStoppedDeduplicatesAfterItemReload(t *testing.T) {
	h, it, sess := newInboxAgentStoppedHub(t)
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
	toldConv(t, h, sess.LastText)
	restored := inboxAgentStoppedSnapshot(t, h, it)
	h.inbox.mu.Lock()
	h.inbox.items[restored.ID] = &restored
	h.inbox.mu.Unlock()
	before := len(h.f.sentToAgents())
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
	if got := inboxAgentStoppedCount(&restored); got != 1 || len(h.f.sentToAgents()) != before {
		t.Fatalf("reload lost the stop marker: stops %d, sends %d → %d", got, before, len(h.f.sentToAgents()))
	}
}

func TestInboxAgentStoppedIsDeduplicatedPerItem(t *testing.T) {
	h, first, sess := newInboxAgentStoppedHub(t)
	second := actionItem("second-item", time.Now())
	second.Chain = []InboxChainEntry{{Kind: ChainAgent, At: time.Now(),
		Link: &InboxChainLink{Type: LinkSession, MachineID: "box", SessionID: "s-sub"}}}
	h.inbox.mu.Lock()
	h.inbox.items[second.ID] = second
	h.inbox.mu.Unlock()
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
	if inboxAgentStoppedCount(first) != 1 || inboxAgentStoppedCount(second) != 1 {
		t.Fatalf("linked items did not each record the stop: first %+v, second %+v", first.Chain, second.Chain)
	}
	toldConv(t, h, sess.LastText)
	before := len(h.f.sentToAgents())
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
	if inboxAgentStoppedCount(first) != 1 || inboxAgentStoppedCount(second) != 1 || len(h.f.sentToAgents()) != before {
		t.Fatal("a repeated stop was not deduplicated independently on both items")
	}
}

func TestInboxAgentStoppedSkipsClosedItems(t *testing.T) {
	h, it, _ := newInboxAgentStoppedHub(t)
	it.done(time.Now(), "user")
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
	if got := inboxAgentStoppedCount(it); got != 0 || len(h.f.sentToAgents()) != 0 {
		t.Fatalf("closed item: stops %d, sent %v", got, h.f.sentToAgents())
	}
}

func TestInboxAgentStoppedWithoutAConversationOnlyRecords(t *testing.T) {
	h, it, _ := newInboxAgentStoppedHub(t)
	it.Agent, it.Handling = nil, nil
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
	if got := inboxAgentStoppedCount(it); got != 1 || it.Agent != nil || len(h.f.sentToAgents()) != 0 {
		t.Fatalf("no-conversation item: stops %d, agent %+v, sent %v", got, it.Agent, h.f.sentToAgents())
	}
}

func TestInboxAgentStoppedWithTwinOffOnlyRecords(t *testing.T) {
	h, it, _ := newInboxAgentStoppedHub(t)
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = false })
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
	if got := inboxAgentStoppedCount(it); got != 1 || it.Handling.State != HandlingAgent || len(h.f.sentToAgents()) != 0 {
		t.Fatalf("disabled twin: stops %d, handling %+v, sent %v", got, it.Handling, h.f.sentToAgents())
	}
}

func TestInboxAgentSweepSessionStates(t *testing.T) {
	for _, tc := range []struct {
		name      string
		missing   bool
		reachable bool
		wantFail  bool
	}{
		{"working waits past the old deadline", false, true, false},
		{"unreachable uses the bounded timeout", true, false, true},
		{"definitively missing uses the bounded timeout", true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, it, sess := newInboxAgentStoppedHub(t)
			started := time.Now().Add(-2 * time.Hour)
			it.Agent.StartedAt = &started
			sess.Status = proto.StatusWorking
			if tc.missing {
				if tc.reachable {
					h.f.setSessions("box")
				} else {
					h.f.mu.Lock()
					delete(h.f.sessions, "box")
					h.f.mu.Unlock()
				}
			} else {
				h.f.setSessions("box", sess)
			}
			h.sweepInboxAgents(context.Background(), InboxAgentConfig{Enabled: true}, time.Now())
			if tc.wantFail {
				if it.Handling.State != HandlingFailed || it.Agent.State != AgentFailed || inboxAgentStoppedCount(it) != 0 {
					t.Fatalf("missing session did not get the bounded timeout: handling %+v, agent %+v, chain %+v", it.Handling, it.Agent, it.Chain)
				}
			} else if it.Handling.State != HandlingAgent || it.Agent.State == AgentFailed || inboxAgentStoppedCount(it) != 0 {
				t.Fatalf("sweep settled a working session: handling %+v, agent %+v, chain %+v", it.Handling, it.Agent, it.Chain)
			}
		})
	}
}

func TestInboxAgentSweepEvictedLinkUsesTheBoundedTimeout(t *testing.T) {
	h, it, _ := newInboxAgentStoppedHub(t)
	started := time.Now().Add(-2 * time.Hour)
	it.Agent.StartedAt = &started
	it.Chain = nil // the old Agent link was evicted by the chain cap
	h.sweepInboxAgents(context.Background(), InboxAgentConfig{Enabled: true}, time.Now())
	if it.Handling.State != HandlingFailed || it.Agent.State != AgentFailed {
		t.Fatalf("an evicted link left the item stuck: handling %+v, agent %+v", it.Handling, it.Agent)
	}
}

func TestInboxAgentSweepRepairsAMissedStop(t *testing.T) {
	h, it, sess := newInboxAgentStoppedHub(t)
	started := time.Now().Add(-2 * time.Hour)
	it.Agent.StartedAt = &started
	h.sweepInboxAgents(context.Background(), InboxAgentConfig{Enabled: true}, time.Now())
	snapshot := inboxAgentStoppedSnapshot(t, h, it)
	if got := inboxAgentStoppedCount(&snapshot); got != 1 || snapshot.Handling.State != HandlingLooking {
		t.Fatalf("sweep did not repair the stop: stops %d, handling %+v", got, snapshot.Handling)
	}
	toldConv(t, h, sess.LastText)
}

func TestInboxAgentSweepRetriesAnUndoneStop(t *testing.T) {
	h, it, sess := newInboxAgentStoppedHub(t)
	started := time.Now().Add(-2 * time.Hour)
	it.Agent.StartedAt = &started
	store := &inboxAgentStopFailStore{inboxMemStore: h.st, failStop: true}
	previousStore := h.Service.store
	h.Service.store = func() Store { return store }
	t.Cleanup(func() {
		h.deliverNotes(context.Background(), it) // finish tellSoon's save before restoring the store it reads
		h.Service.store = previousStore
	})
	h.sweepInboxAgents(context.Background(), InboxAgentConfig{Enabled: true}, time.Now())
	if it.Handling.State != HandlingAgent || it.Agent.State == AgentFailed || inboxAgentStoppedCount(it) != 0 || len(h.f.sentToAgents()) != 0 {
		t.Fatalf("an undone stop became a timeout: handling %+v, agent %+v, chain %+v", it.Handling, it.Agent, it.Chain)
	}
	h.sweepInboxAgents(context.Background(), InboxAgentConfig{Enabled: true}, time.Now())
	snapshot := inboxAgentStoppedSnapshot(t, h, it)
	if got := inboxAgentStoppedCount(&snapshot); got != 1 || snapshot.Handling.State != HandlingLooking {
		t.Fatalf("the next sweep did not retry the stop: stops %d, handling %+v", got, snapshot.Handling)
	}
	toldConv(t, h, sess.LastText)
}

func TestGoldenInboxAgentStoppedNotes(t *testing.T) {
	n := inboxNote{Name: prompts.NoteAgentStopped, Machine: "box", Session: "s-sub", Text: "the example is fixed"}
	golden(t, "inbox_handling.agent_stopped", renderNote(n, "Example user"))
	n.Failed, n.Text = true, "API Error: example failed"
	golden(t, "inbox_handling.agent_stopped_failed", renderNote(n, "Example user"))
}
