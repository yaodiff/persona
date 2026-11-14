package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// relayTries records the once_id of every assistant.send try into conversation to.
func relayTries(f *fakeHub, to string) *[]string {
	var ids []string
	f.mu.Lock()
	prev := f.onAssistantSend
	f.onAssistantSend = func(f *fakeHub, p map[string]any) {
		if prev != nil {
			prev(f, p)
		}
		if p["session_id"] == to {
			id, _ := p["once_id"].(string)
			ids = append(ids, id)
			if _, tied := p["client_msg_id"]; tied {
				ids = append(ids, "client_msg_id set") // a tied line reads as the owner's own message
			}
		}
	}
	f.mu.Unlock()
	return &ids
}

func triesOf(f *fakeHub, ids *[]string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), (*ids)...)
}

func drainingNow(s *Service, id string) bool {
	s.relays.mu.Lock()
	defer s.relays.mu.Unlock()
	return s.relays.draining[id]
}

// Every try of a queued message goes under one once_id (the node sends an id once), and no client_msg_id; another message
// has its own id.
func TestRelayTriesShareOneMessageID(t *testing.T) {
	s, f := newQuietService(t)
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	at := time.Now().UTC()
	s.queueRelay("conv1", relayMsg{From: "conv2", FromTitle: "Planning", Tool: "conversation_send", Text: "first", At: at})
	tries := relayTries(f, "conv1")
	f.mu.Lock()
	f.failAssistant = errors.New("the launcher did not start")
	f.mu.Unlock()
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "the failed try", func() bool { return len(triesOf(f, tries)) == 1 && !drainingNow(s, "conv1") })
	f.mu.Lock()
	f.failAssistant = nil
	f.mu.Unlock()
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "the second try", func() bool { return f.sentTo("conv1") == 1 && !drainingNow(s, "conv1") })
	got := triesOf(f, tries)
	if len(got) != 2 || got[0] == "" || got[0] != got[1] {
		t.Fatalf("the tries went under %q, want one id twice", got)
	}
	other := relayMsgID("conv1", relayMsg{From: "conv2", FromTitle: "Planning", Tool: "conversation_send", Text: "second", At: at})
	if other == got[0] || relayMsgID("conv3", relayMsg{From: "conv2", Tool: "conversation_send", Text: "first", At: at}) == got[0] {
		t.Fatalf("another message or conversation shares the id %q", got[0])
	}
}

// The node took the message and wrote it, but answered after the manager stopped waiting (a node slower than the
// send's 90 s): the message is in the conversation, so it is taken as delivered and never sent again. Before, it
// stayed queued and went in once more on every drain while the node stayed slow.
func TestRelayInDoubtButInTheConversationIsNotSentAgain(t *testing.T) {
	s, f := newQuietService(t)
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	msg := relayMsg{From: "conv2", FromTitle: "Planning", Tool: "conversation_send", Text: "please register the lanes", At: time.Now().UTC()}
	s.queueRelay("conv1", msg)
	tries := relayTries(f, "conv1")
	f.mu.Lock()
	prev := f.onAssistantSend
	f.onAssistantSend = func(f *fakeHub, p map[string]any) {
		prev(f, p)
		// The line as the node writes it: the text as sent, the message-source block the manager put in front kept.
		text, _ := p["text"].(string)
		f.items = append(f.items, proto.Item{Seq: len(f.items) + 1, Role: "user", Kind: proto.KindText, Text: text, TS: time.Now()})
	}
	f.failAssistant = context.DeadlineExceeded // written, then no answer in time
	f.mu.Unlock()

	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "the try in doubt to settle", func() bool { return len(triesOf(f, tries)) == 1 && !drainingNow(s, "conv1") })
	if q := s.queuedFor("conv1"); q != 0 {
		t.Fatalf("a message found in the conversation is still queued (%d)", q)
	}
	for range 3 {
		s.drainRelays("server", []proto.Session{idle})
		waitFor(t, "the repeated drain to settle", func() bool { return !drainingNow(s, "conv1") })
	}
	if got := triesOf(f, tries); len(got) != 1 {
		t.Fatalf("sent %d times, want once", len(got))
	}
}

// A try in doubt whose line is not in the conversation stays queued, and the message behind it is not held up by it:
// the next drain sends that one first, then tries the doubtful one again under the same id.
func TestRelayInDoubtNotFoundStaysQueuedWithoutBlocking(t *testing.T) {
	s, f := newQuietService(t)
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	at := time.Now().UTC()
	s.queueRelay("conv1", relayMsg{From: "conv2", Tool: "conversation_send", Text: "first", At: at})
	s.queueRelay("conv1", relayMsg{From: "conv3", Tool: "conversation_send", Text: "second", At: at.Add(time.Second)})
	tries := relayTries(f, "conv1")
	f.mu.Lock()
	f.failAssistant = context.DeadlineExceeded // nothing written: the conversation has no such line
	f.mu.Unlock()
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "the try in doubt to settle", func() bool { return len(triesOf(f, tries)) == 1 && !drainingNow(s, "conv1") })
	if q := s.queuedFor("conv1"); q != 2 {
		t.Fatalf("after a try in doubt not found: %d queued, want 2", q)
	}
	f.mu.Lock()
	f.failAssistant = nil
	f.mu.Unlock()
	for i := range 2 {
		s.drainRelays("server", []proto.Session{idle})
		waitFor(t, fmt.Sprintf("delivery %d", i+1), func() bool { return f.sentTo("conv1") == i+1 && !drainingNow(s, "conv1") })
	}
	n := f.notices()
	if len(n) != 2 || !strings.Contains(n[0], "second") || !strings.Contains(n[1], "first") {
		t.Fatalf("went in as %q, want the one behind first, then the doubtful one", n)
	}
	got := triesOf(f, tries)
	if len(got) != 3 || got[0] != got[2] || got[0] == got[1] {
		t.Fatalf("tries went under %q, want first, second, then first's id again", got)
	}
}

// A refusal is no doubt: the conversation is not read, and the message stays queued.
func TestRelayRefusalIsNotLookedFor(t *testing.T) {
	if relayInDoubt(errors.New("conversation conv1 is gone")) || relayInDoubt(errors.New("the launcher did not start")) {
		t.Fatal("a refusal taken as a send in doubt")
	}
	if !relayInDoubt(context.DeadlineExceeded) || !relayInDoubt(fmt.Errorf("relay: %w", context.DeadlineExceeded)) {
		t.Fatal("a send whose time ran out is not in doubt")
	}
}

// An older line with the same words is another message: a try in doubt stays queued when only such a line is there.
func TestRelayInDoubtOlderLineWithTheSameWordsIsNotIt(t *testing.T) {
	s, f := newQuietService(t)
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	msg := relayMsg{From: "conv2", FromTitle: "Planning", Tool: "conversation_send", Text: "please register the lanes", At: time.Now().UTC()}
	s.queueRelay("conv1", msg)
	tries := relayTries(f, "conv1")
	f.mu.Lock()
	prev := f.onAssistantSend
	f.onAssistantSend = func(f *fakeHub, p map[string]any) {
		prev(f, p)
		text, _ := p["text"].(string)
		f.items = append(f.items, proto.Item{Seq: len(f.items) + 1, Role: "user", Kind: proto.KindText, Text: text, TS: time.Now().Add(-10 * time.Minute)})
	}
	f.failAssistant = context.DeadlineExceeded
	f.mu.Unlock()
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "the try in doubt to settle", func() bool { return len(triesOf(f, tries)) == 1 && !drainingNow(s, "conv1") })
	if q := s.queuedFor("conv1"); q != 1 {
		t.Fatalf("an older line with its words was taken as the message: %d queued, want 1", q)
	}
}

// A supervision message in doubt keeps the conversation's pushes held while its line is found (its turn is a side
// exchange), and lets them go when it is not.
func TestSupervisionRelayInDoubtKeepsItsHoldOnlyWhenItWentIn(t *testing.T) {
	for _, wentIn := range []bool{true, false} {
		t.Run(fmt.Sprint("went in ", wentIn), func(t *testing.T) {
			s, f := newQuietService(t)
			idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
			s.queueRelay("conv1", relayMsg{Tool: superviseTool, Text: "a task waits for your review", At: time.Now().UTC()})
			tries := relayTries(f, "conv1")
			f.mu.Lock()
			prev := f.onAssistantSend
			f.onAssistantSend = func(f *fakeHub, p map[string]any) {
				prev(f, p)
				if wentIn {
					text, _ := p["text"].(string)
					f.items = append(f.items, proto.Item{Seq: len(f.items) + 1, Role: "user", Kind: proto.KindText, Text: text, TS: time.Now()})
				}
			}
			f.failAssistant = context.DeadlineExceeded
			f.mu.Unlock()
			s.drainRelays("server", []proto.Session{idle})
			waitFor(t, "the try in doubt to settle", func() bool { return len(triesOf(f, tries)) == 1 && !drainingNow(s, "conv1") })
			if got := s.supervising("conv1"); got != wentIn {
				t.Fatalf("pushes held %v, want %v", got, wentIn)
			}
		})
	}
}

// The same words from another conversation are not this relay's line: a try in doubt stays queued.
func TestRelayInDoubtSameWordsFromAnotherConversationIsNotIt(t *testing.T) {
	s, f := newQuietService(t)
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	msg := relayMsg{From: "conv2", FromTitle: "Planning", Tool: "conversation_send", Text: "please register the lanes", At: time.Now().UTC()}
	other := msg
	other.From, other.FromTitle = "conv9", "Other"
	s.queueRelay("conv1", msg)
	tries := relayTries(f, "conv1")
	f.mu.Lock()
	prev := f.onAssistantSend
	f.onAssistantSend = func(f *fakeHub, p map[string]any) {
		prev(f, p)
		f.items = append(f.items, proto.Item{Seq: len(f.items) + 1, Role: "user", Kind: proto.KindText,
			Text: prompts.MessageSourceText(relayText(other), other.source()), TS: time.Now()})
	}
	f.failAssistant = context.DeadlineExceeded
	f.mu.Unlock()
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "the try in doubt to settle", func() bool { return len(triesOf(f, tries)) == 1 && !drainingNow(s, "conv1") })
	if q := s.queuedFor("conv1"); q != 1 {
		t.Fatalf("another conversation's line with the same words was taken as the message: %d queued, want 1", q)
	}
}

// A try in doubt whose line came only after the look is not sent again: the next drain looks first (a node restarted in
// between no longer knows the once_id, so its guard would not hold).
func TestRelayInDoubtLooksAgainBeforeSendingAgain(t *testing.T) {
	s, f := newQuietService(t)
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	msg := relayMsg{From: "conv2", FromTitle: "Planning", Tool: "conversation_send", Text: "please register the lanes", At: time.Now().UTC()}
	s.queueRelay("conv1", msg)
	tries := relayTries(f, "conv1")
	var sent string
	f.mu.Lock()
	prev := f.onAssistantSend
	f.onAssistantSend = func(f *fakeHub, p map[string]any) {
		prev(f, p)
		sent, _ = p["text"].(string) // written, but its line is not in the transcript yet
	}
	f.failAssistant = context.DeadlineExceeded
	f.mu.Unlock()
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "the first try in doubt", func() bool { return len(triesOf(f, tries)) == 1 && !drainingNow(s, "conv1") })
	if q := s.queuedFor("conv1"); q != 1 {
		t.Fatalf("not found yet, it stays queued: %d queued, want 1", q)
	}
	f.mu.Lock()
	f.items = append(f.items, proto.Item{Seq: 1, Role: "user", Kind: proto.KindText, Text: sent, TS: time.Now()}) // the line lands late
	f.failAssistant = nil
	f.mu.Unlock()
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "the next drain", func() bool { return s.queuedFor("conv1") == 0 && !drainingNow(s, "conv1") })
	if got := triesOf(f, tries); len(got) != 1 {
		t.Fatalf("sent %d times, want once: the late line was not looked for first", len(got))
	}
}
