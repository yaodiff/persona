package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
)

// #7391: a relay hop is a request; the first message back, its answer, keeps the hop.

// relayChain is a Service whose conversations message each other as conversation_send does: checked and counted
// (relayAllowed), then delivered at once (relayDelivered). send returns the hop the message went at.
func relayChain(t *testing.T) (*Service, func(from, to string) (int, error)) {
	t.Helper()
	s := newTestService(t, newFakeHub())
	send := func(from, to string) (int, error) {
		hop, answer, err := s.relayAllowed(from, to, time.Now())
		if err == nil {
			s.relayDelivered(context.Background(), to, relayMsg{From: from, Tool: "conversation_send", Hop: hop, Answer: answer, At: time.Now().UTC()})
		}
		return hop, err
	}
	return s, send
}

// forgetRate drops the per-pair and per-sender counts, as ten minutes going by does.
func forgetRate(s *Service) {
	s.relays.mu.Lock()
	s.relays.sent = map[[2]string][]time.Time{}
	s.relays.mu.Unlock()
}

// A conversation and the one it asked go back and forth relayMaxHops times; the next request is past the limit.
func TestRelayExchangesKeepTheirHop(t *testing.T) {
	_, send := relayChain(t)
	for i, want := range []int{1, 1, 2, 2, 3, 3} {
		from, to := "hub", "worker"
		if i%2 == 1 {
			from, to = to, from
		}
		if hop, err := send(from, to); err != nil || hop != want {
			t.Fatalf("message %d, %s → %s: hop %d, %v (want hop %d)", i+1, from, to, hop, err, want)
		}
	}
	if _, err := send("hub", "worker"); err == nil || !strings.Contains(err.Error(), "hop 4") {
		t.Fatalf("a fourth request went: %v", err)
	}
}

// Two conversations that keep writing to each other are cut after 2 × relayMaxHops messages however slowly they
// write: the per-pair rate limit forgets after ten minutes, the hops do not.
func TestRelayPingPongCutAtTwiceTheHops(t *testing.T) {
	s, send := relayChain(t)
	sent := 0
	for ; sent < 100; sent++ {
		from, to := "a", "b"
		if sent%2 == 1 {
			from, to = to, from
		}
		forgetRate(s)
		if _, err := send(from, to); err != nil {
			if !strings.Contains(err.Error(), "hop") {
				t.Fatalf("cut by %v, not the hops", err)
			}
			break
		}
	}
	if sent != 2*relayMaxHops {
		t.Fatalf("%d messages went before the cut, want %d", sent, 2*relayMaxHops)
	}
}

// Only the first message back is free; a message to a third conversation is a request; the answer to a question of
// one's own leaves the answer one owes still free.
func TestRelayOnlyTheFirstAnswerIsFree(t *testing.T) {
	s, send := relayChain(t)
	for i, st := range []struct {
		from, to string
		hop      int
		why      string
	}{
		{"a", "b", 1, "a request"},
		{"b", "a", 1, "its answer"},
		{"b", "a", 2, "a second message back: a request"},
		{"a", "c", 3, "a asks a third conversation"},
		{"c", "a", 3, "c answers a"},
		{"a", "b", 3, "a still owes b its answer"},
	} {
		forgetRate(s)
		if hop, err := send(st.from, st.to); err != nil || hop != st.hop {
			t.Fatalf("step %d (%s), %s → %s: hop %d, %v (want hop %d)", i+1, st.why, st.from, st.to, hop, err, st.hop)
		}
	}
	forgetRate(s)
	if _, err := send("b", "a"); err == nil || !strings.Contains(err.Error(), "hop 4") {
		t.Fatalf("b's next request went: %v", err)
	}
}

// What the user says resets the conversation's hops as before, and with them the answer it owed.
func TestRelayUserSpokeDropsTheAnswerOwed(t *testing.T) {
	s, send := relayChain(t)
	if _, err := send("a", "b"); err != nil {
		t.Fatal(err)
	}
	s.userSpoke("b")
	if hop, err := send("b", "a"); err != nil || hop != 1 {
		t.Fatalf("after the user spoke: hop %d, %v", hop, err)
	}
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if hp := b.st.Hops["a"]; hp.From != "b" {
		t.Fatalf("b's message after the user spoke is a request a owes an answer to: %+v", hp)
	}
}

// A request older than the hop memory is forgotten with its hop: no answer is owed for it, neither when the
// conversation next writes nor once an answer to a question of its own comes in.
func TestRelayStaleRequestOwesNoAnswer(t *testing.T) {
	s, send := relayChain(t)
	set := func(id string, hp relayHop) {
		b := &s.relays
		b.mu.Lock()
		defer b.mu.Unlock()
		if !s.relayLocked(context.Background()) {
			t.Fatal("relay state")
		}
		b.st.Hops[id] = hp
	}
	stale := time.Now().Add(-relayHopMemory - time.Minute).UTC()
	set("b", relayHop{Hop: relayMaxHops, At: stale, From: "a"})
	if hop, err := send("b", "a"); err != nil || hop != 1 {
		t.Fatalf("b's message after the stale request: hop %d, %v (want a new request at hop 1)", hop, err)
	}
	// a's answer to b's question waited in b's queue while b's request from c went stale: it does not come back
	// with the answer.
	set("b", relayHop{Hop: relayMaxHops, At: stale, From: "c"})
	s.relayDelivered(context.Background(), "b", relayMsg{From: "a", Tool: "conversation_send", Hop: 1, Answer: true, At: time.Now().UTC()})
	if hop, err := send("b", "c"); err != nil || hop != 2 {
		t.Fatalf("b's message to c: hop %d, %v (want a new request at hop 2)", hop, err)
	}
}
