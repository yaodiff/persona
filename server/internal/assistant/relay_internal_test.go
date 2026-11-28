package assistant

import (
	"strings"
	"testing"
	"time"
)

// A relay meant only for the assistant is still kept and delivered, but the client queue does not expose it above
// the owner's composer.
func TestInternalRelaysStayOutOfTheComposerQueue(t *testing.T) {
	s, _ := newQuietService(t)
	at := time.Now().UTC()
	for _, msg := range []relayMsg{
		{FromTitle: "Card ledger", Text: "internal ledger", At: at, Internal: true},
		{FromTitle: "Lead", Text: "visible later note", At: at},
	} {
		if _, ok := s.keepRelayLater("conv1", msg); !ok {
			t.Fatalf("could not keep %q", msg.FromTitle)
		}
	}
	b := &s.relays
	b.mu.Lock()
	b.st.Queue["conv1"] = []relayMsg{
		{From: "agent_question", FromTitle: "agent_question", Tool: "agent_question", Text: "internal routing note", At: at, Internal: true},
		{From: "conv2", FromTitle: "Lead", Tool: "conversation_send", Text: "visible queue note", At: at},
	}
	b.mu.Unlock()

	queue, ok := s.queuedRelays()
	if !ok {
		t.Fatal("queue state could not be read")
	}
	lines := queue["conv1"]
	if len(lines) != 2 {
		t.Fatalf("visible queue lines = %v, want two", lines)
	}
	all := strings.Join(lines, "\n")
	if strings.Contains(all, "internal") || strings.Contains(all, "Card ledger") {
		t.Fatalf("internal relay leaked into the composer queue: %v", lines)
	}
	if !strings.Contains(all, "visible later note") || !strings.Contains(all, "visible queue note") {
		t.Fatalf("user-facing relays missing from the queue: %v", lines)
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.st.Later["conv1"]) != 2 || len(b.st.Queue["conv1"]) != 2 {
		t.Fatalf("internal relays were dropped from delivery state: later=%d queue=%d", len(b.st.Later["conv1"]), len(b.st.Queue["conv1"]))
	}
}
