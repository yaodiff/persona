package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
)

// The stops of an item's Agents that the inbox_agent_stopped_test.go cases leave out: a linked session the node no
// longer knows, a link that fell off the kept chain, one stop under several triggers at once, and the detail's shape.

// oppStoppedHub is one open item whose processing conversation s1 started the Agent s-sub on box, the Agent settled.
func oppStoppedHub(t *testing.T) (*inboxHub, *InboxItem) {
	t.Helper()
	h, _, it := handledHub(t)
	h.inbox.handing.Lock()
	t.Cleanup(h.inbox.handing.Unlock)
	h.inboxAgentStarted(context.Background(), "s1", "box", "s-sub", "check the example")
	waitNeedsYouIdle(t, h.Service)
	h.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s-sub", Status: proto.StatusIdle,
		LastText: "the example is fixed", UpdatedAt: time.Now().UTC()})
	return h, it
}

func oppStops(it *InboxItem) []InboxChainEntry {
	var out []InboxChainEntry
	for _, e := range it.Chain {
		if e.Kind == ChainAgentStopped {
			out = append(out, e)
		}
	}
	return out
}

// A node that answers without the linked session knows the Agent is gone: it stopped, whatever took it away (the user
// deleted it, the node restarted, a runtime dropped the record). The item must not wait on it forever — before the
// Agent stops were recorded, the sweep failed it after four timeouts.
func TestOppInboxAgentStopWhenTheNodeNoLongerListsTheSession(t *testing.T) {
	h, it := oppStoppedHub(t)
	started := time.Now().Add(-5 * time.Hour)
	h.inbox.mu.Lock()
	it.Agent.StartedAt = &started
	h.inbox.mu.Unlock()
	h.f.setSessions("box") // the machine answers, without s-sub
	h.sweepInboxAgents(context.Background(), InboxAgentConfig{Enabled: true}, time.Now())
	h.inbox.mu.Lock()
	state, agent := it.Handling.State, it.Agent.State
	stops := len(oppStops(it))
	h.inbox.mu.Unlock()
	if state == HandlingAgent && agent != AgentFailed && stops == 0 {
		t.Fatalf("the item waits on a session the node no longer has: handling %q, agent %q, chain %+v", state, agent, it.Chain)
	}
}

// The chain keeps 50 entries: on a long matter the agent entry can fall off before the Agent stops. Nothing links the
// session any more, so no stop can be recorded; the item must still not sit in Agent forever (the sweep failed it
// after four timeouts before).
func TestOppInboxAgentStopWhenTheLinkFellOffTheChain(t *testing.T) {
	h, it := oppStoppedHub(t)
	started := time.Now().Add(-5 * time.Hour)
	h.inbox.mu.Lock()
	it.Agent.StartedAt = &started
	for range chainMax + 5 {
		h.inbox.chainAdd(it, InboxChainEntry{Kind: ChainReceived, Count: 1, Text: "more"})
	}
	h.inbox.mu.Unlock()
	if inboxLatestAgentLink(it) != nil {
		t.Fatalf("the agent link is still on the chain: %+v", it.Chain[len(it.Chain)-3:])
	}
	h.sweepInboxAgents(context.Background(), InboxAgentConfig{Enabled: true}, time.Now())
	h.inbox.mu.Lock()
	state, agent := it.Handling.State, it.Agent.State
	h.inbox.mu.Unlock()
	if state == HandlingAgent && agent != AgentFailed {
		t.Fatalf("an item whose link fell off the chain waits forever: handling %q, agent %q", state, agent)
	}
}

// The turn's end and the sweep can judge the same stop at once: one entry, one wake.
func TestOppInboxAgentStopIsRecordedOnceUnderConcurrentTriggers(t *testing.T) {
	h, it := oppStoppedHub(t)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
		}()
	}
	wg.Wait()
	h.inbox.mu.Lock()
	stops := len(oppStops(it))
	h.inbox.mu.Unlock()
	if stops != 1 {
		t.Fatalf("%d stops recorded for one stop", stops)
	}
}

// Last words are one line, at most 300 characters, and a stop without any is still a stop.
func TestOppInboxAgentStopDetailShape(t *testing.T) {
	for _, tc := range []struct {
		name, last, want string
	}{
		{"none", "", ""},
		{"several lines", "first line\nsecond line", "first line second line"},
		{"long", strings.Repeat("长", 400), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, it := oppStoppedHub(t)
			h.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s-sub", Status: proto.StatusIdle,
				LastText: tc.last, UpdatedAt: time.Now().UTC()})
			h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
			h.inbox.mu.Lock()
			stops := oppStops(it)
			h.inbox.mu.Unlock()
			if len(stops) != 1 {
				t.Fatalf("%d stops: %+v", len(stops), it.Chain)
			}
			d := stops[0].Detail
			if strings.Contains(d, "\n") || utf8.RuneCountInString(d) > 301 { // clip's 300 and its ellipsis
				t.Fatalf("detail is not one line of at most 300 characters: %q", d)
			}
			if tc.want != "" && d != tc.want {
				t.Fatalf("detail %q, want %q", d, tc.want)
			}
		})
	}
}

// Only an agent entry makes the item an Agent's matter: the processing conversation's own turn ending (a handling
// entry links it) is not a stop, and records nothing.
func TestOppInboxAgentStopIgnoresTheHandlingConversation(t *testing.T) {
	h, it := oppStoppedHub(t)
	h.inboxAgentTurnSettled(context.Background(), "server", "s1")
	h.inbox.mu.Lock()
	stops := len(oppStops(it))
	h.inbox.mu.Unlock()
	if stops != 0 {
		t.Fatalf("the handling conversation's own turn was taken for an Agent stop: %+v", it.Chain)
	}
}

// An Agent waiting for the user (a question, a plan, a permission prompt) is not settled: the processing conversation
// must not be told it stopped, nor offered a next suggestion from half-done work. tasks_follow.go treats the same
// state as not settled for a task.
func TestOppInboxAgentStopWaitsForAnAgentAwaitingTheUser(t *testing.T) {
	h, it := oppStoppedHub(t)
	h.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s-sub", Status: proto.StatusBlocked, State: proto.StateNeedsYou,
		PendingInteractions: 1, LastText: "May I run the deployment?", UpdatedAt: time.Now().UTC()})
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
	h.inbox.mu.Lock()
	stops := len(oppStops(it))
	h.inbox.mu.Unlock()
	if stops != 0 {
		t.Fatalf("an Agent waiting for the user was reported as stopped: %+v", it.Chain)
	}
}

// The chain keeps 50 entries: a busy matter can push both the agent entry and the stop marker off before the next
// judgement. Then nothing links the session any more, so the callback must not record or wake anything again (the
// sweep's bounded timeout is what settles the item); it must not resurrect the old stop as a new one.
func TestOppInboxAgentStopMarkerEvictedByTheChainCapDoesNotRepeat(t *testing.T) {
	h, it := oppStoppedHub(t)
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
	toldConv(t, h, "Agent 停了") // the first stop's own wake, before what this test counts
	h.inbox.mu.Lock()
	first := len(oppStops(it))
	for range chainMax {
		h.inbox.chainAdd(it, InboxChainEntry{Kind: ChainCheck, Text: "looked again"})
	}
	h.inbox.mu.Unlock()
	if first != 1 {
		t.Fatalf("the first stop was not recorded: %d", first)
	}
	before := len(h.f.sentToAgents())
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
	if got := len(h.f.sentToAgents()); got != before {
		t.Fatalf("a stop whose chain entries were evicted woke the conversation again: %d sends → %d", before, got)
	}
}

// The sweep calls the same recording path on every pass, without waiting for any timeout: an Agent parked on a
// permission or plan prompt (herdr's blocked, needs_you) has not stopped, and must neither be reported as stopped nor
// woken while the user has not answered it.
func TestOppInboxAgentSweepLeavesAnAgentAwaitingTheUserAlone(t *testing.T) {
	h, it := oppStoppedHub(t)
	h.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s-sub", Status: proto.StatusBlocked, State: proto.StateNeedsYou,
		PendingInteractions: 1, LastText: "May I run the deployment?", UpdatedAt: time.Now().UTC()})
	h.sweepInboxAgents(context.Background(), InboxAgentConfig{Enabled: true}, time.Now())
	h.inbox.mu.Lock()
	state, stops := it.Handling.State, len(oppStops(it))
	h.inbox.mu.Unlock()
	if stops != 0 || state != HandlingAgent {
		t.Fatalf("the sweep took a parked Agent for a stopped one: handling %q, stops %d, chain %+v", state, stops, it.Chain)
	}
}

// The node's own queued input is input the user typed while it worked: it starts a new turn, so the current one is not
// the Agent's stop yet.
func TestOppInboxAgentStopWaitsForTheNodesQueuedInput(t *testing.T) {
	h, it := oppStoppedHub(t)
	h.f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s-sub", Status: proto.StatusIdle,
		Queued: []string{"also check the tests"}, LastText: "the example is fixed", UpdatedAt: time.Now().UTC()})
	h.inboxAgentTurnSettled(context.Background(), "box", "s-sub")
	h.inbox.mu.Lock()
	stops := len(oppStops(it))
	h.inbox.mu.Unlock()
	if stops != 0 {
		t.Fatalf("queued input was taken for a stop: %+v", it.Chain)
	}
}
