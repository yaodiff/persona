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
	"github.com/nwverse/persona/server/internal/rpc"
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
	gap := relayRetryGap
	relayRetryGap = 0 // the second try right away; the gap has its own test
	t.Cleanup(func() { relayRetryGap = gap })
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
// the next drain sends that one first. The doubtful one is retried under the same id after the look budget.
func TestRelayInDoubtNotFoundStaysQueuedWithoutBlocking(t *testing.T) {
	s, f := newQuietService(t)
	t.Cleanup(func() { stopRelayTimer(s, "conv1") })
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
	drainRelayTest(t, s, idle)
	for look := 1; look < relayLookLimit; look++ {
		if got := f.sentTo("conv1"); got != 1 {
			t.Fatalf("after %d checks, %d sends succeeded, want one", look, got)
		}
		advanceRelayLook(t, s, "conv1", look)
		drainRelayTest(t, s, idle)
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

// A refusal or explicit not-sent / not-written error is no doubt: the conversation is not read, and the message stays queued.
func TestRelayRefusalIsNotLookedFor(t *testing.T) {
	for _, err := range []error{
		errors.New("conversation conv1 is gone"),
		errors.New("the launcher did not start"),
		rpc.NotSent(context.DeadlineExceeded),
		rpc.NotSent(rpc.ErrClosed),
		errors.Join(rpc.ErrNotWritten, context.DeadlineExceeded),
		fmt.Errorf("send: %w", rpc.NotSent(rpc.ErrClosed)),
	} {
		if relayInDoubt(err) {
			t.Fatalf("expected relayInDoubt(%v) = false", err)
		}
	}
	for _, err := range []error{
		context.DeadlineExceeded,
		fmt.Errorf("relay: %w", context.DeadlineExceeded),
		rpc.ErrClosed,
		rpc.ErrDropped,
	} {
		if !relayInDoubt(err) {
			t.Fatalf("expected relayInDoubt(%v) = true", err)
		}
	}
}

// An older line with the same words is another message: a try in doubt stays queued when only such a line is there.
func TestRelayInDoubtOlderLineWithTheSameWordsIsNotIt(t *testing.T) {
	s, f := newQuietService(t)
	t.Cleanup(func() { stopRelayTimer(s, "conv1") })
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
			t.Cleanup(func() { stopRelayTimer(s, "conv1") })
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
	t.Cleanup(func() { stopRelayTimer(s, "conv1") })
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
	t.Cleanup(func() { stopRelayTimer(s, "conv1") })
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
	advanceRelayLook(t, s, "conv1", 1)
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "the next drain", func() bool { return s.queuedFor("conv1") == 0 && !drainingNow(s, "conv1") })
	if got := triesOf(f, tries); len(got) != 1 {
		t.Fatalf("sent %d times, want once: the late line was not looked for first", len(got))
	}
}

func drainRelayTest(t *testing.T, s *Service, c proto.Session) {
	t.Helper()
	s.drainRelays(c.MachineID, []proto.Session{c})
	waitFor(t, "the relay drain", func() bool { return !drainingNow(s, c.SessionID) })
}

func stopRelayTimer(s *Service, to string) {
	s.relays.mu.Lock()
	defer s.relays.mu.Unlock()
	if timer := s.relays.holds[to]; timer != nil {
		timer.Stop()
		delete(s.relays.holds, to)
	}
}

func advanceRelayLook(t *testing.T, s *Service, to string, looks int) {
	t.Helper()
	s.relays.mu.Lock()
	defer s.relays.mu.Unlock()
	q := s.relays.st.Queue[to]
	if len(q) == 0 || q[0].Looks != looks || q[0].TriedAt.IsZero() || q[0].LookAfter.IsZero() {
		t.Fatalf("want a queued relay waiting after %d checks, got %+v", looks, q)
	}
	q[0].LookAfter = time.Now().Add(-time.Second)
}

func savedRelay(t *testing.T, store *memStore, to string) relayMsg {
	t.Helper()
	var state relayState
	if ok, err := store.Setting(t.Context(), relayKey, &state); !ok || err != nil || len(state.Queue[to]) != 1 {
		t.Fatalf("read saved relay to %s: found %v, err %v, state %+v", to, ok, err, state)
	}
	return state.Queue[to][0]
}

func TestRelayInDoubtSurvivesManagerAndNodeRestart(t *testing.T) {
	for _, name := range []string{"node restarted", "once_id expired"} {
		t.Run(name, func(t *testing.T) {
			store := &memStore{}
			s, f := newQuietServiceOn(t, store)
			idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
			s.queueRelay("conv1", relayMsg{From: "conv2", Tool: "conversation_send", Text: "check the schedule", At: time.Now().UTC()})
			tries := relayTries(f, "conv1")
			f.mu.Lock()
			f.failAssistant = context.DeadlineExceeded
			f.mu.Unlock()
			drainRelayTest(t, s, idle)
			stopRelayTimer(s, "conv1")
			msg := savedRelay(t, store, "conv1")
			if got := triesOf(f, tries); len(got) != 1 || msg.TriedAt.IsZero() || msg.Looks != 1 || msg.LookAfter.IsZero() {
				t.Fatalf("want one send and its saved attempt and check schedule, got %v and %+v", got, msg)
			}
			written := msg.TriedAt.Add(time.Second)
			if name == "once_id expired" {
				// Advance the age of the saved attempt and the transcript together past the node's TTL.
				const elapsed = 11 * time.Minute
				msg.TriedAt = msg.TriedAt.Add(-elapsed)
				msg.LookAfter = msg.LookAfter.Add(-elapsed)
				written = written.Add(-elapsed)
				if err := store.PutSetting(t.Context(), relayKey, relayState{Queue: map[string][]relayMsg{"conv1": {msg}}}); err != nil {
					t.Fatal(err)
				}
			}
			restarted, node := newQuietServiceOn(t, store)
			t.Cleanup(func() { stopRelayTimer(restarted, "conv1") })
			node.mu.Lock()
			node.items = []proto.Item{{Role: "user", Kind: proto.KindText, Text: relayDeliveryText(msg), TS: written}}
			node.mu.Unlock()
			if name == "node restarted" {
				drainRelayTest(t, restarted, idle)
				if got := savedRelay(t, store, "conv1"); got != msg {
					t.Fatalf("restart changed a relay before its scheduled check: %+v", got)
				}
				advanceRelayLook(t, restarted, "conv1", 1)
			}
			// This fake node would accept a resend. The transcript must settle the relay first.
			drainRelayTest(t, restarted, idle)
			if sent, queued := node.sentTo("conv1"), restarted.queuedFor("conv1"); sent != 0 || queued != 0 {
				t.Fatalf("after restart: %d new sends and %d queued, want neither", sent, queued)
			}
		})
	}
}

func TestRelayAttemptIsSavedBeforeSend(t *testing.T) {
	store := &memStore{}
	s, f := newQuietServiceOn(t, store)
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	s.queueRelay("conv1", relayMsg{From: "conv2", Tool: "conversation_send", Text: "check the schedule", At: time.Now().UTC()})
	entered := make(chan string, 1)
	finish := make(chan struct{})
	f.mu.Lock()
	f.onAssistantSend = func(_ *fakeHub, p map[string]any) {
		entered <- p["text"].(string)
		<-finish
	}
	f.mu.Unlock()
	s.drainRelays("server", []proto.Session{idle})
	var line string
	select {
	case line = <-entered:
	case <-time.After(5 * time.Second):
		close(finish)
		t.Fatal("the send did not start")
	}
	var atStop relayState
	ok, err := store.Setting(t.Context(), relayKey, &atStop)
	close(finish)
	waitFor(t, "the old manager's send to finish", func() bool { return !drainingNow(s, "conv1") })
	q := atStop.Queue["conv1"]
	if !ok || err != nil || len(q) != 1 || q[0].TriedAt.IsZero() || q[0].Looks != 0 {
		t.Fatalf("send started without a saved pre-send attempt: found %v, err %v, queue %+v", ok, err, q)
	}
	// Start from the saved state at the interruption, without any of the old manager's later writes.
	afterStop := &memStore{}
	if err := afterStop.PutSetting(t.Context(), relayKey, atStop); err != nil {
		t.Fatal(err)
	}
	restarted, node := newQuietServiceOn(t, afterStop)
	node.mu.Lock()
	node.items = []proto.Item{{Role: "user", Kind: proto.KindText, Text: line, TS: q[0].TriedAt}}
	node.mu.Unlock()
	drainRelayTest(t, restarted, idle)
	if sent, queued := node.sentTo("conv1"), restarted.queuedFor("conv1"); sent != 0 || queued != 0 {
		t.Fatalf("after stopping mid-send: %d new sends and %d queued, want neither", sent, queued)
	}
}

func TestRelayUnsavedAttemptDoesNotSend(t *testing.T) {
	store := &memStore{}
	s, f := newQuietServiceOn(t, store)
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	t.Cleanup(func() { stopRelayTimer(s, "conv1") })
	s.queueRelay("conv1", relayMsg{Tool: superviseTool, Text: "review the schedule", At: time.Now().UTC()})
	tries := relayTries(f, "conv1")
	store.mu.Lock()
	store.failPut = errors.New("settings write failed")
	store.mu.Unlock()
	drainRelayTest(t, s, idle)
	if got := triesOf(f, tries); len(got) != 0 || !savedRelay(t, store, "conv1").TriedAt.IsZero() {
		t.Fatalf("an unsaved attempt was made: %v", got)
	}
	store.mu.Lock()
	store.failPut = nil
	store.mu.Unlock()
	drainRelayTest(t, s, idle)
	if got := triesOf(f, tries); len(got) != 1 || s.queuedFor("conv1") != 0 {
		t.Fatalf("relay did not go after settings recovered: tries %v, queue depth %d", got, s.queuedFor("conv1"))
	}
}

func TestRelayLookDistinguishesReadFailureFromAbsence(t *testing.T) {
	s, f := newQuietService(t)
	msg := relayMsg{Tool: superviseTool, Text: "review the schedule", TriedAt: time.Now()}
	f.mu.Lock()
	f.itemsFail = 1
	f.mu.Unlock()
	if found, err := s.relayArrived("server", "conv1", msg); found || err == nil {
		t.Fatalf("unreadable transcript: got found=%v, err=%v, want a read error", found, err)
	}
	if found, err := s.relayArrived("server", "conv1", msg); found || err != nil {
		t.Fatalf("empty transcript: got found=%v, err=%v, want a successful read with no match", found, err)
	}
}

func TestRelayFailedReadWaitsForLateTranscript(t *testing.T) {
	s, f := newQuietService(t)
	t.Cleanup(func() { stopRelayTimer(s, "conv1") })
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	msg := relayMsg{From: "conv2", Tool: "conversation_send", Text: "check the schedule", At: time.Now().UTC()}
	s.queueRelay("conv1", msg)
	tries := relayTries(f, "conv1")
	f.mu.Lock()
	f.failAssistant = context.DeadlineExceeded
	f.itemsFail = 1
	f.mu.Unlock()
	drainRelayTest(t, s, idle)

	// An early session list must neither read again ahead of schedule nor resend.
	f.mu.Lock()
	f.failAssistant = nil
	f.mu.Unlock()
	drainRelayTest(t, s, idle)
	f.mu.Lock()
	reads := f.itemReads
	f.mu.Unlock()
	if got := triesOf(f, tries); len(got) != 1 || reads != 1 {
		t.Fatalf("before the scheduled check: tries %v, transcript reads %d, want one of each", got, reads)
	}
	advanceRelayLook(t, s, "conv1", 1)
	drainRelayTest(t, s, idle) // readable now, but the line has not been written yet
	if got := triesOf(f, tries); len(got) != 1 {
		t.Fatalf("retried after an early transcript miss: %v", got)
	}
	f.mu.Lock()
	f.items = []proto.Item{{Role: "user", Kind: proto.KindText, Text: relayDeliveryText(msg), TS: time.Now()}}
	f.mu.Unlock()
	advanceRelayLook(t, s, "conv1", 2)
	drainRelayTest(t, s, idle)
	if got := triesOf(f, tries); len(got) != 1 || s.queuedFor("conv1") != 0 {
		t.Fatalf("late line did not settle the relay: tries %v, queue depth %d", got, s.queuedFor("conv1"))
	}
}

func TestRelayRetriesAfterBoundedUnreadableTranscript(t *testing.T) {
	s, f := newQuietService(t)
	t.Cleanup(func() { stopRelayTimer(s, "conv1") })
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	s.queueRelay("conv1", relayMsg{Tool: superviseTool, Text: "review the schedule", At: time.Now().UTC()})
	tries := relayTries(f, "conv1")
	f.mu.Lock()
	f.failAssistant = context.DeadlineExceeded
	f.itemsFail = relayLookLimit
	f.mu.Unlock()
	drainRelayTest(t, s, idle)
	f.mu.Lock()
	f.failAssistant = nil
	f.mu.Unlock()
	for look := 1; look < relayLookLimit; look++ {
		if got := triesOf(f, tries); len(got) != 1 {
			t.Fatalf("retried before the look budget expired: %v", got)
		}
		advanceRelayLook(t, s, "conv1", look)
		drainRelayTest(t, s, idle)
	}
	if got := triesOf(f, tries); len(got) != 2 || got[0] == "" || got[0] != got[1] || s.queuedFor("conv1") != 0 {
		t.Fatalf("fallback should send once under the same id: tries %v, queue depth %d", got, s.queuedFor("conv1"))
	}
}

func TestRelayContextOnlyMatchesItsOwnPayload(t *testing.T) {
	s, f := newQuietService(t)
	t.Cleanup(func() { stopRelayTimer(s, "conv1") })
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	msg := relayMsg{From: "conv2", Tool: "conversation_send", At: time.Now().UTC(),
		Text: `<persona-context kind="context">The relay's only content.</persona-context>`}
	other := msg
	other.Text = `<persona-context kind="context">An unrelated message from the same chat.</persona-context>`
	s.queueRelay("conv1", msg)
	tries := relayTries(f, "conv1")
	f.mu.Lock()
	f.failAssistant = context.DeadlineExceeded
	f.items = []proto.Item{{Role: "user", Kind: proto.KindText, Text: relayDeliveryText(other), TS: time.Now()}}
	f.mu.Unlock()
	drainRelayTest(t, s, idle)
	advanceRelayLook(t, s, "conv1", 1) // another context-only line from the same sender did not match
	f.mu.Lock()
	line := prompts.MessageSourceText(relayDeliveryText(msg), msg.source())
	f.items = append(f.items, proto.Item{Role: "user", Kind: proto.KindText, Text: line, TS: time.Now()})
	f.mu.Unlock()
	drainRelayTest(t, s, idle)
	if got := triesOf(f, tries); len(got) != 1 || s.queuedFor("conv1") != 0 {
		t.Fatalf("context-only line did not settle the relay: tries %v, queue depth %d", got, s.queuedFor("conv1"))
	}
}

func TestRelayContextOnlySurvivesTranscriptParsing(t *testing.T) {
	for _, codex := range []bool{false, true} {
		t.Run(fmt.Sprintf("codex=%v", codex), func(t *testing.T) {
			s, f := newQuietService(t)
			t.Cleanup(func() { stopRelayTimer(s, "conv1") })
			idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
			const body = "The only content was in this block."
			msg := relayMsg{From: "conv2", Tool: "conversation_send", At: time.Now().UTC(),
				Text: "<persona-context>" + body + "</persona-context>"}
			s.queueRelay("conv1", msg)
			tries := relayTries(f, "conv1")
			var line string
			f.mu.Lock()
			prev := f.onAssistantSend
			f.onAssistantSend = func(f *fakeHub, p map[string]any) {
				prev(f, p)
				line = p["text"].(string)
			}
			f.failAssistant = context.DeadlineExceeded
			f.mu.Unlock()
			drainRelayTest(t, s, idle)

			chat := newTranscriptChat(t, codex)
			chat.user(line)
			items, _, _ := chat.file.PageTurns(0, 3, 2000)
			if len(items) != 1 || strings.Contains(items[0].Text, body) {
				t.Fatalf("expected one transcript line with the bare context removed, got %+v", items)
			}
			items[0].TS = time.Now() // the transcript fixture uses a fixed timestamp
			f.mu.Lock()
			f.items = items
			f.failAssistant = nil
			f.mu.Unlock()
			advanceRelayLook(t, s, "conv1", 1)
			drainRelayTest(t, s, idle)
			if got := triesOf(f, tries); len(got) != 1 || s.queuedFor("conv1") != 0 {
				t.Fatalf("parsed transcript did not settle the relay: tries %v, queue depth %d", got, s.queuedFor("conv1"))
			}
		})
	}
}

func TestRelayImmediateTryCarriesMarkIntoQueue(t *testing.T) {
	store := &memStore{}
	s, f := newQuietServiceOn(t, store)
	t.Cleanup(func() { stopRelayTimer(s, "conv1") })
	f.mu.Lock()
	f.failAssistant = context.DeadlineExceeded
	f.known["conv2"] = true
	f.mu.Unlock()
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	if _, err := s.toolConversationSend(t.Context(), "conv2", []byte(`{"id":"conv1","text":"check the schedule"}`)); err != nil {
		t.Fatal(err)
	}
	if msg := savedRelay(t, store, "conv1"); msg.TriedAt.IsZero() {
		t.Fatal("the immediate try's start was lost on queueing")
	}
}

// A second exact confirmation can rewrite a withdrawn head while its earlier copy is being drained. The old copy must
// not restore signals from a head it did not actually remove. The head's durable doubt time must also survive restart
// and a landed check that could not remove that changed head, or a later drain replays delivered signals (#5732).
func TestWithdrawnRelayReplayRequiresRemovingSameHead(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	start := superviseSignal{Todo: "todo-b", Kind: sigUnjudged, Key: "start-unknown@todo-b", At: time.Now()}
	other := superviseSignal{Todo: "todo-c", Kind: sigStalled, Key: "still-pending", At: time.Now()}
	oldSignals := []superviseSignal{start, other}
	msg := relayMsg{Tool: superviseTool, Text: "mixed supervision", At: time.Now(), Withdrawn: true, DoubtedAt: time.Now().Add(-time.Second), Signals: encodeDigestSignals(oldSignals)}
	if s.queueRelay("conv1", msg) != 1 {
		t.Fatal("the withdrawn head was not queued")
	}
	f.mu.Lock()
	f.items = append(f.items, proto.Item{Seq: 1, Role: "user", Kind: proto.KindText, Text: msg.Text, TS: time.Now()})
	f.mu.Unlock()
	newSignals := []superviseSignal{other}
	updated := msg
	updated.Signals = encodeDigestSignals(newSignals) // the second confirmation removed start from the durable head, retaining its doubt time
	r := &s.relays
	r.mu.Lock()
	r.st.Queue["conv1"][0] = updated
	r.doubted = nil // only the durable head survives a real restart
	r.mu.Unlock()
	if !s.relayLanded("server", "conv1", msg) {
		t.Fatal("the persisted in-doubt original was not recognized after restart")
	}
	r.mu.Lock()
	replay := s.takeRelayHeadLocked("conv1", msg, true)
	queued := len(r.st.Queue["conv1"])
	restored := r.st.SupervisePending[start.id()]
	r.mu.Unlock()
	if len(replay) != 0 || restored.Key != "" {
		t.Fatalf("a stale head copy replayed confirmed signals: replay=%+v restored=%+v", replay, restored)
	}
	if queued != 1 {
		t.Fatalf("the changed queue head was removed by an older copy: %d queued", queued)
	}
	if !s.relayLanded("server", "conv1", updated) {
		t.Fatal("the changed-head check discarded the durable delivery evidence before removal")
	}
	r.mu.Lock()
	replay = s.takeRelayHeadLocked("conv1", updated, false)
	queued = len(r.st.Queue["conv1"])
	r.mu.Unlock()
	if len(replay) != 0 || queued != 0 {
		t.Fatalf("the landed updated head was replayed or retained: replay=%+v queued=%d", replay, queued)
	}
}

// A successful send may finish after another writer replaced the queue head. Only the exact sent head may leave;
// a different head is not delivery evidence and must remain queued (#5732).
func TestSuccessfulRelayRemovalRequiresSameHead(t *testing.T) {
	s, _ := newQuietServiceOn(t, &memStore{})
	if s.queueRelay("conv1", relayMsg{Tool: "conversation_send", Text: "current head", At: time.Now()}) != 1 {
		t.Fatal("the current head was not queued")
	}
	r := &s.relays
	r.mu.Lock()
	head := r.st.Queue["conv1"][0]
	older := head
	older.Text = "an older send"
	replay := s.takeRelayHeadLocked("conv1", older, true)
	queued := len(r.st.Queue["conv1"])
	r.mu.Unlock()
	if len(replay) != 0 || queued != 1 {
		t.Fatalf("a successful older send removed the current head: replay=%+v queued=%d", replay, queued)
	}
	r.mu.Lock()
	replay = s.takeRelayHeadLocked("conv1", head, true)
	queued = len(r.st.Queue["conv1"])
	r.mu.Unlock()
	if len(replay) != 0 || queued != 0 {
		t.Fatalf("the exact successful head was not removed: replay=%+v queued=%d", replay, queued)
	}
}

// The queue's own pre-send write adds attempt metadata to the head. That is still the exact message being sent,
// not a concurrent replacement; a successful send must remove it (#5732).
func TestSuccessfulRelayRemovalAllowsOwnAttemptMetadata(t *testing.T) {
	s, _ := newQuietServiceOn(t, &memStore{})
	if s.queueRelay("conv1", relayMsg{Tool: superviseTool, Text: "current head", At: time.Now()}) != 1 {
		t.Fatal("the current head was not queued")
	}
	r := &s.relays
	r.mu.Lock()
	sent := r.st.Queue["conv1"][0]
	r.st.Queue["conv1"][0].TriedAt = time.Now().UTC()
	r.st.Queue["conv1"][0].Looks = 2
	r.st.Queue["conv1"][0].LookAfter = time.Now().Add(time.Second).UTC()
	r.st.Queue["conv1"][0].DoubtedAt = time.Now().UTC()
	replay := s.takeRelayHeadLocked("conv1", sent, true)
	queued := len(r.st.Queue["conv1"])
	r.mu.Unlock()
	if len(replay) != 0 || queued != 0 {
		t.Fatalf("the sender's own attempt metadata blocked exact-head removal: replay=%+v queued=%d", replay, queued)
	}
}

// A successful original digest covers only signals it actually carried. A concurrently retained new signal must keep
// both its queue head and durable outbox entry even when the narrowed head is withdrawn (#5732).
func TestSuccessfulRelayRemovalRejectsUncoveredSupervisionSignals(t *testing.T) {
	s, _ := newQuietServiceOn(t, &memStore{})
	start := superviseSignal{Todo: "todo-a", Kind: sigUnjudged, Key: "start", At: time.Now()}
	other := superviseSignal{Todo: "todo-b", Kind: sigStalled, Key: "other", At: time.Now()}
	sent := relayMsg{Tool: superviseTool, Text: "mixed supervision", At: time.Now(), Signals: encodeDigestSignals([]superviseSignal{start, other})}
	if s.queueRelay("conv1", sent) != 1 {
		t.Fatal("the mixed digest was not queued")
	}
	newSignal := superviseSignal{Todo: "todo-c", Kind: sigReview, Key: "new-after-send", At: time.Now()}
	r := &s.relays
	r.mu.Lock()
	r.st.Queue["conv1"][0].Withdrawn = true
	r.st.Queue["conv1"][0].Signals = encodeDigestSignals([]superviseSignal{newSignal})
	r.st.SupervisePending = map[string]superviseSignal{newSignal.id(): newSignal}
	replay := s.takeRelayHeadLocked("conv1", sent, true)
	queued := len(r.st.Queue["conv1"])
	retained := r.st.SupervisePending[newSignal.id()]
	r.mu.Unlock()
	if len(replay) != 0 || queued != 1 || retained.Key != newSignal.Key {
		t.Fatalf("an uncovered new signal was acknowledged: replay=%+v queued=%d retained=%+v", replay, queued, retained)
	}
}

// A manager can stop after the original send lands but before it removes a withdrawn durable head. That head has no
// in-doubt marker, yet its original line is already in the conversation: the remaining signals must not be replayed.
func TestWithdrawnRelayLandedBeforeRestartDoesNotReplayWithoutDoubt(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	start := superviseSignal{Todo: "todo-b", Kind: sigUnjudged, Key: "start-unknown@todo-b", At: time.Now()}
	other := superviseSignal{Todo: "todo-c", Kind: sigStalled, Key: "already-delivered", At: time.Now()}
	signals := []superviseSignal{start, other}
	msg := relayMsg{Tool: superviseTool, Text: "mixed supervision", At: time.Now(), Withdrawn: true, Signals: encodeDigestSignals(signals)}
	if s.queueRelay("conv1", msg) != 1 {
		t.Fatal("the withdrawn head was not queued")
	}
	f.mu.Lock()
	f.items = append(f.items, proto.Item{Seq: 1, Role: "user", Kind: proto.KindText, Text: msg.Text, TS: time.Now()})
	f.mu.Unlock()
	s.drainRelays("server", []proto.Session{{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}})
	waitFor(t, "the landed withdrawn head to leave the queue", func() bool { return s.queuedFor("conv1") == 0 })
	if got := storedSupervisePending(t, s); len(got) != 0 {
		t.Fatalf("the landed original left signals for a duplicate after restart: %+v", got)
	}
}

// Selective /stop owns the same doubtful-head boundary as start confirmation. If the original mixed text did not land,
// its stopped task must not be sent again; the remaining task is replayed without the old text (#5732).
func TestStopDoubtfulMixedDigestDoesNotResendStoppedTask(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	stopped := superviseSignal{Todo: "stopped-task", Kind: sigReview, Key: "stopped-review", At: time.Now()}
	other := superviseSignal{Todo: "other-task", Kind: sigReview, Key: "other-review", At: time.Now()}
	signals := []superviseSignal{stopped, other}
	msg := relayMsg{Tool: superviseTool, Text: "stopped task\nremaining task", At: time.Now(), DoubtedAt: time.Now().Add(-time.Second), Signals: encodeDigestSignals(signals)}
	s.queueRelay("conv1", msg)
	n, taken := s.dropQueuedSupervisionTasks("conv1", map[string]bool{stopped.Todo: true}, nil)
	if n != 0 || len(taken) != 1 || taken[0].Key != stopped.Key {
		t.Fatalf("selective stop of a doubtful head = %d, %+v", n, taken)
	}
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "the doubtful stopped head to settle", func() bool { return s.queuedFor("conv1") == 0 })
	if got := strings.Join(f.noticesTo("conv1"), "\n"); strings.Contains(got, "stopped task") {
		t.Fatalf("the failed original resent a stopped task: %q", got)
	}
	if got := storedSupervisePending(t, s)[other.id()]; got.Key != other.Key {
		t.Fatalf("the other mixed signal was not replayed after the doubtful head failed: %+v", got)
	}
}
