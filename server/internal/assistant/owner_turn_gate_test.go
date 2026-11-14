package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The owner's message always goes first (#6394): a backlog from other conversations stays queued for the whole of
// the owner's round, whatever rank its messages carry, and drains only once the round is seen over.
func TestOwnerTurnGateHoldsTheWholeQueue(t *testing.T) {
	presenceTimes(t, 150*time.Millisecond, time.Hour)
	ownerTurnTimes(t, time.Hour, time.Hour)
	s, f := newQuietService(t)
	stopHolds(t, s)
	ctx := context.Background()
	session := func(status string) proto.Session {
		return proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: status}
	}
	// A backlog: an ordinary relayed message, a decision-bearing notice and a late answer, each of a different rank.
	at := time.Now().UTC()
	s.queueRelay("conv1", relayMsg{From: "c2", FromTitle: "Research", Tool: "conversation_send", Text: "ordinary", At: at})
	s.queueRelay("conv1", relayMsg{From: "t1", Tool: noticeTool, State: "needs", Text: "a decision", At: at})
	s.queueRelay("conv1", relayMsg{From: "t2", Tool: askAnswerTool, Text: "the late answer", At: at})
	if q := s.queuedFor("conv1"); q != 3 {
		t.Fatalf("seeded %d queued messages, want 3", q)
	}
	// The owner speaks while the conversation is idle: the gate arms, and a drain delivers nothing of the queue.
	// The owner's own message never enters the relay queue in the first place (SendParams goes straight to the node).
	s.UserSpoke(ctx, SessionRef{MachineID: "server", SessionID: "conv1"})
	if params := ownerSend(t, s, "conv1", "my question"); params["interject_background"] != nil {
		t.Fatalf("the owner starts this round: no interject mark, %v", params)
	}
	s.drainRelays("server", []proto.Session{session(proto.StatusIdle)})
	if sent := f.sentTo("conv1"); sent != 0 {
		t.Fatalf("the gate let %d queued message(s) out while the owner's round was on", sent)
	}
	if q := s.queuedFor("conv1"); q != 3 {
		t.Fatalf("the gate let queued messages out: %d left, want 3", q)
	}
	// The round runs: still nothing of the queue.
	s.setSessions("server", []proto.Session{session(proto.StatusWorking)})
	s.drainRelays("server", []proto.Session{session(proto.StatusWorking)})
	if sent := f.sentTo("conv1"); sent != 0 {
		t.Fatalf("a working conversation took %d queued sends, want 0", sent)
	}
	// The round ends: the gate releases and one message drains (the late answer, first by rank). A new round is on
	// again for that message while the rest keep waiting.
	s.setSessions("server", []proto.Session{session(proto.StatusIdle)})
	// The drain pops the queue only once the send returns: wait for both, never for the moment between them (#7643).
	waitFor(t, "the first queued message after the owner's round", func() bool {
		return f.sentTo("conv1") == 1 && s.queuedFor("conv1") == 2
	})
	if n := f.notices(); len(n) != 1 || n[0] != "the late answer" {
		t.Fatalf("delivered %v, want the late answer first", n)
	}
	if q := s.queuedFor("conv1"); q != 2 {
		t.Fatalf("%d queued left, want 2", q)
	}
}

// A gate whose round no session list ever shows working releases after ownerTurnStartWait: a refused send or a
// conversation the list dropped must not stick the queue forever (#6394).
func TestOwnerTurnGateSafetyStartWait(t *testing.T) {
	presenceTimes(t, 150*time.Millisecond, time.Hour)
	ownerTurnTimes(t, 250*time.Millisecond, time.Hour)
	s, f := newQuietService(t)
	stopHolds(t, s)
	ctx := context.Background()
	session := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	s.queueRelay("conv1", relayMsg{From: "c2", FromTitle: "Research", Tool: "conversation_send", Text: "held", At: time.Now().UTC()})
	spoke := time.Now()
	s.UserSpoke(ctx, SessionRef{MachineID: "server", SessionID: "conv1"})
	s.drainRelays("server", []proto.Session{session})
	// The gate holds until at least spoke+ownerTurnStartWait: a count read before then must be 0. A machine that
	// stalled past the limit before the read proves nothing either way (#7643).
	if sent := f.sentTo("conv1"); time.Since(spoke) < ownerTurnStartWait && sent != 0 {
		t.Fatalf("the gate let a message out at once: %d", sent)
	}
	// The drain pops the queue only once the send returns: wait for both, never for the moment between them (#7643).
	waitFor(t, "the safety limit to release the gate", func() bool {
		return f.sentTo("conv1") == 1 && s.queuedFor("conv1") == 0
	})
}

// A gate whose round a list keeps showing working releases on the hard cap, and the owner speaking again refreshes
// it (#6394).
func TestOwnerTurnGateHardCapAndRefresh(t *testing.T) {
	ownerTurnTimes(t, time.Hour, 250*time.Millisecond)
	presenceTimes(t, 150*time.Millisecond, time.Hour)
	s, f := newQuietService(t)
	stopHolds(t, s)
	ctx := context.Background()
	session := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true}
	s.queueRelay("conv1", relayMsg{From: "c2", FromTitle: "Research", Tool: "conversation_send", Text: "held", At: time.Now().UTC()})
	s.UserSpoke(ctx, SessionRef{MachineID: "server", SessionID: "conv1"})
	s.setSessions("server", []proto.Session{session, {SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking}})
	// The owner speaks again mid-round: the cap starts over. Put the first turn at an exact timestamp rather than
	// sleeping to make it old enough; the second turn must still hold against its own refreshed timestamp.
	s.relays.mu.Lock()
	turn := s.relays.ownerTurns["conv1"]
	turn.at = time.Now().UTC().Add(-150 * time.Millisecond)
	s.relays.ownerTurns["conv1"] = turn
	s.relays.mu.Unlock()
	spoke := time.Now()
	s.UserSpoke(ctx, SessionRef{MachineID: "server", SessionID: "conv1"})
	s.drainRelays("server", []proto.Session{{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking}})
	// Read before spoke+ownerTurnMax, the count must be 0; past it, a stalled machine proves nothing (#7643).
	if sent := f.sentTo("conv1"); time.Since(spoke) < ownerTurnMax && sent != 0 {
		t.Fatalf("the refreshed gate let a message out: %d", sent)
	}
	// The list never shows the round stop; the hard cap releases it.
	waitFor(t, "the hard cap to release", func() bool { return f.sentTo("conv1") == 1 })
}

// The gate arms only for the conversation the owner spoke in; another conversation's queue drains as ever (#6394).
func TestOwnerTurnGateScopedToConversation(t *testing.T) {
	presenceTimes(t, 150*time.Millisecond, time.Hour)
	ownerTurnTimes(t, time.Hour, time.Hour)
	s, f := newQuietService(t)
	stopHolds(t, s)
	ctx := context.Background()
	f.known["lead1"] = true
	f.setSessions("server",
		proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	msg := relayMsg{From: "c2", FromTitle: "Research", Tool: "conversation_send", Text: "hello", At: time.Now().UTC()}
	s.UserSpoke(ctx, SessionRef{MachineID: "server", SessionID: "conv1"})
	s.relayOrQueue(ctx, "conv1", msg)
	s.relayOrQueue(ctx, "lead1", msg)
	if a, b := f.sentTo("conv1"), f.sentTo("lead1"); a != 0 || b != 1 {
		t.Fatalf("the spoken conversation sent %d and the other %d, want 0 and 1", a, b)
	}
}
