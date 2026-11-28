package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A sender wakes at most two conversations within the window; a third and later are kept for their receivers, a
// conversation already woken still wakes, another sender is counted apart, and the window passing frees it again.
func TestConversationSendFanoutKeepsABroadcastQuiet(t *testing.T) {
	var f sendFanout
	now := time.Now()
	for _, c := range []struct {
		name           string
		sender, target string
		at             time.Duration
		want           bool
	}{
		{"first", "main", "lead1", 0, true},
		{"second", "main", "lead2", time.Minute, true},
		{"third", "main", "lead3", 2 * time.Minute, false},
		{"fourth", "main", "lead4", 3 * time.Minute, false},
		{"one already woken", "main", "lead1", 4 * time.Minute, true},
		{"another sender", "lead1", "lead2", 4 * time.Minute, true},
		{"the window passed for the first two", "main", "lead3", 12 * time.Minute, true},
	} {
		if got := f.admit(c.sender, c.target, now.Add(c.at)); got != c.want {
			t.Errorf("%s: admit %v, want %v", c.name, got, c.want)
		}
	}
}

func shortFanoutWindow(t *testing.T, d time.Duration) {
	t.Helper()
	oldWin, oldDelay := fanoutWindow, fanoutInFlightDelay
	fanoutWindow, fanoutInFlightDelay = d, 25*time.Millisecond
	t.Cleanup(func() {
		fanoutWindow, fanoutInFlightDelay = oldWin, oldDelay
	})
}

func newFanoutTestService(t *testing.T, st Store, f *fakeHub, ids ...string) (*Service, context.Context, context.CancelFunc) {
	t.Helper()
	if f == nil {
		f = newFakeHub().withMachine()
	}
	s, _ := newQuietServiceOn(t, st)
	s.hub = f
	ctx, cancel := context.WithCancel(context.Background())
	s.running.Store(&ctx)
	t.Cleanup(func() {
		cancel()
		s.sendFanout.stopAllTimers()
	})
	f.mu.Lock()
	f.hello.Assistant.MachineID = "server"
	f.known["main"] = true
	for _, id := range ids {
		f.known[id] = true
	}
	f.mu.Unlock()
	setSettings(t, s, func(v *Settings) { v.MachineID = "server"; v.Main = "main" })
	sessions := make([]proto.Session, 0, len(ids)+1)
	sessions = append(sessions, proto.Session{SessionID: "main", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	for _, id := range ids {
		sessions = append(sessions, proto.Session{SessionID: id, MachineID: "server", Assistant: true, Status: proto.StatusIdle, Title: id})
	}
	f.setSessions("server", sessions...)
	return s, ctx, cancel
}

func fanoutWaitsOf(t *testing.T, s *Service) []delayedWait {
	t.Helper()
	doc, err := s.readDelayedWaits(context.Background())
	if err != nil {
		t.Fatalf("readDelayedWaits: %v", err)
	}
	var out []delayedWait
	for _, w := range doc.Waits {
		if w.Kind == delayedFanoutWake {
			out = append(out, w)
		}
	}
	return out
}

func TestNextFanoutSlotSpreadsBurstsAcrossWindows(t *testing.T) {
	t0 := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	actual := map[string]time.Time{
		"lead1": t0,
		"lead2": t0.Add(time.Minute),
	}
	var waits []delayedWait
	// 3rd target waits for lead1 to expire (t0 + 10m).
	at3 := nextFanoutSlot(actual, waits, "main", "", time.Time{}, t0.Add(2*time.Minute))
	if !at3.Equal(t0.Add(10 * time.Minute)) {
		t.Fatalf("lead3 at = %v, want %v", at3, t0.Add(10*time.Minute))
	}
	waits = append(waits, delayedWait{ID: "w3", Kind: delayedFanoutWake, At: at3, Fanout: &fanoutWake{Sender: "main", Target: "lead3"}})

	// 4th target waits for lead2 to expire (t0 + 11m), behind lead3.
	at4 := nextFanoutSlot(actual, waits, "main", "", time.Time{}, t0.Add(3*time.Minute))
	if !at4.Equal(t0.Add(11 * time.Minute)) {
		t.Fatalf("lead4 at = %v, want %v", at4, t0.Add(11*time.Minute))
	}
	waits = append(waits, delayedWait{ID: "w4", Kind: delayedFanoutWake, At: at4, Fanout: &fanoutWake{Sender: "main", Target: "lead4"}})

	// 5th target waits for lead3 to expire (t0 + 20m): deferred wakes count against the cap.
	at5 := nextFanoutSlot(actual, waits, "main", "", time.Time{}, t0.Add(4*time.Minute))
	if !at5.Equal(t0.Add(20 * time.Minute)) {
		t.Fatalf("lead5 at = %v, want %v", at5, t0.Add(20*time.Minute))
	}
	waits = append(waits, delayedWait{ID: "w5", Kind: delayedFanoutWake, At: at5, Fanout: &fanoutWake{Sender: "main", Target: "lead5"}})

	// 6th target waits for lead4 to expire (t0 + 21m).
	at6 := nextFanoutSlot(actual, waits, "main", "", time.Time{}, t0.Add(5*time.Minute))
	if !at6.Equal(t0.Add(21 * time.Minute)) {
		t.Fatalf("lead6 at = %v, want %v", at6, t0.Add(21*time.Minute))
	}

	// If lead3 woke early and left the queue, a new target still goes to the back of the queue (>= lead4's 11m).
	atAfterDrop := nextFanoutSlot(actual, waits[1:2], "main", "", time.Time{}, t0.Add(5*time.Minute))
	if atAfterDrop.Before(at4) {
		t.Fatalf("new target jumped ahead of lead4: got %v < %v", atAfterDrop, at4)
	}

	// Rescheduling an earlier wait (w3) due to slight jitter must not push it behind a later wave (w5).
	atJitter := nextFanoutSlot(actual, waits, "main", "w3", at3, t0.Add(9*time.Minute+59*time.Second))
	if !atJitter.Equal(t0.Add(10 * time.Minute)) {
		t.Fatalf("jitter reschedule pushed w3 to %v, want %v", atJitter, t0.Add(10*time.Minute))
	}

	// After a long outage where two deferred wakes just fired at t0+35m, overdue remaining waits still pace by window.
	outageNow := t0.Add(35 * time.Minute)
	outageActual := map[string]time.Time{"lead3": outageNow, "lead4": outageNow}
	overdue := []delayedWait{
		{ID: "w5", Kind: delayedFanoutWake, At: t0.Add(20 * time.Minute), Fanout: &fanoutWake{Sender: "main", Target: "lead5"}},
		{ID: "w6", Kind: delayedFanoutWake, At: t0.Add(21 * time.Minute), Fanout: &fanoutWake{Sender: "main", Target: "lead6"}},
	}
	at7 := nextFanoutSlot(outageActual, overdue, "main", "", time.Time{}, outageNow)
	if !at7.Equal(outageNow.Add(20 * time.Minute)) {
		t.Fatalf("post-outage lead7 at = %v, want %v", at7, outageNow.Add(20*time.Minute))
	}
}

// Sender wakes fanoutWide-1 conversations immediately; the next ones are kept with a clear tool answer, woken
// automatically after the window without any other message, exactly once, and count against the cap so bursts stay
// spread out over time.
func TestConversationSendDeferredWakeFiresOnceAndCountsAgainstCap(t *testing.T) {
	shortFanoutWindow(t, 250*time.Millisecond)
	st := withDelayedWaitCAS(&memStore{})
	f := newFakeHub().withMachine()
	s, ctx, _ := newFanoutTestService(t, st, f, "lead1", "lead2", "lead3", "lead4", "lead5", "lead6")

	type wakeEvent struct {
		text string
		at   time.Time
	}
	lead3Ch := make(chan wakeEvent, 4)
	lead4Ch := make(chan wakeEvent, 4)
	lead5Ch := make(chan wakeEvent, 4)
	f.mu.Lock()
	f.onAssistantSend = func(_ *fakeHub, p map[string]any) {
		id, _ := p["session_id"].(string)
		text, _ := p["text"].(string)
		ev := wakeEvent{text: text, at: time.Now()}
		switch id {
		case "lead3":
			lead3Ch <- ev
		case "lead4":
			lead4Ch <- ev
		case "lead5":
			lead5Ch <- ev
		}
	}
	f.mu.Unlock()

	for _, id := range []string{"lead1", "lead2"} {
		out, err := s.toolConversationSend(ctx, "main", []byte(`{"id":"`+id+`","text":"immediate `+id+`"}`))
		if err != nil {
			t.Fatalf("send %s: %v", id, err)
		}
		if !strings.Contains(out, "Sent to") {
			t.Fatalf("send %s unexpected answer: %q", id, out)
		}
	}

	// 3rd, 4th, and 5th targets exceed the cap and get deferred wakes at the back of the queue.
	out3, err := s.toolConversationSend(ctx, "main", []byte(`{"id":"lead3","text":"deferred lead3 task"}`))
	if err != nil {
		t.Fatalf("send lead3: %v", err)
	}
	if !strings.Contains(out3, "Kept for") || !strings.Contains(out3, "woken automatically in about") {
		t.Fatalf("tool answer did not state deferred wake plainly: %q", out3)
	}
	if strings.Contains(out3, "send it again after the window") || strings.Contains(out3, "No reply will come") {
		t.Fatalf("tool answer still carries obsolete wake:false copy: %q", out3)
	}
	if _, err := s.toolConversationSend(ctx, "main", []byte(`{"id":"lead4","text":"deferred lead4 task"}`)); err != nil {
		t.Fatalf("send lead4: %v", err)
	}
	if _, err := s.toolConversationSend(ctx, "main", []byte(`{"id":"lead5","text":"deferred lead5 task"}`)); err != nil {
		t.Fatalf("send lead5: %v", err)
	}

	if got := f.sentTo("lead3") + f.sentTo("lead4") + f.sentTo("lead5"); got != 0 {
		t.Fatalf("capped targets woke immediately: %d sends", got)
	}
	if waits := fanoutWaitsOf(t, s); len(waits) != 3 {
		t.Fatalf("persisted deferred wakes = %d, want 3", len(waits))
	}
	if labels, ok := s.queuedRelays(); !ok || len(labels["lead3"]) != 1 || !strings.Contains(labels["lead3"][0], "deferred lead3 task") {
		t.Fatalf("queuedRelays for lead3 = %v, %v", labels["lead3"], ok)
	}

	// First deferred wave: lead3 and lead4 wake after the first window without any other message.
	var ev3, ev4 wakeEvent
	select {
	case ev3 = <-lead3Ch:
	case <-time.After(2 * time.Second):
		t.Fatal("lead3 was not woken automatically after the window")
	}
	if !strings.Contains(ev3.text, "deferred lead3 task") || !strings.Contains(ev3.text, "第 1 跳") {
		t.Fatalf("lead3 delivered text = %q", ev3.text)
	}
	select {
	case ev4 = <-lead4Ch:
	case <-time.After(2 * time.Second):
		t.Fatal("lead4 was not woken automatically after the window")
	}
	if !strings.Contains(ev4.text, "deferred lead4 task") {
		t.Fatalf("lead4 delivered text = %q", ev4.text)
	}

	// Because lead3 and lead4 counted against main's cap, lead5 has NOT woken yet and a fresh send to lead6 is also capped.
	if got5 := f.sentTo("lead5"); got5 != 0 {
		t.Fatalf("lead5 woke in the same window as lead3/lead4 (%d sends): deferred wakes did not count against the cap", got5)
	}
	out6, err := s.toolConversationSend(ctx, "main", []byte(`{"id":"lead6","text":"deferred lead6 task"}`))
	if err != nil {
		t.Fatalf("send lead6: %v", err)
	}
	if !strings.Contains(out6, "woken automatically in about") {
		t.Fatalf("lead6 was not capped after deferred wakes filled the window: %q", out6)
	}

	// Second deferred wave: lead5 and lead6 wake after the next window, each once.
	select {
	case ev5 := <-lead5Ch:
		if !strings.Contains(ev5.text, "deferred lead5 task") {
			t.Fatalf("lead5 delivered text = %q", ev5.text)
		}
		if gap := ev5.at.Sub(ev3.at); gap < fanoutWindow-40*time.Millisecond {
			t.Fatalf("lead5 woke only %v after lead3 (window %v): burst was not spread out", gap, fanoutWindow)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lead5 was not woken automatically in the next window")
	}
	waitFor(t, "lead6 deferred wake", func() bool { return f.sentTo("lead6") == 1 })
	waitFor(t, "all deferred wakes to leave backlog", func() bool { return len(fanoutWaitsOf(t, s)) == 0 })

	// Confirm no double wake arrives for lead3 or lead5 after an extra window.
	select {
	case extra := <-lead3Ch:
		t.Fatalf("lead3 received a second wake: %q", extra.text)
	case extra := <-lead5Ch:
		t.Fatalf("lead5 received a second wake: %q", extra.text)
	case <-time.After(300 * time.Millisecond):
	}
	for _, id := range []string{"lead1", "lead2", "lead3", "lead4", "lead5", "lead6"} {
		if got := f.sentTo(id); got != 1 {
			t.Fatalf("%s received %d wakes, want exactly 1", id, got)
		}
		if left := laterFor(s, id); len(left) != 0 {
			t.Fatalf("%s still has %d Later items", id, len(left))
		}
	}
}

// If the receiver wakes for any other reason before its deferred wake fires, it reads the kept message then and the
// deferred wake is dropped (no double wake), even if an explicit wake:false message arrives afterwards.
func TestConversationSendDeferredWakeDroppedWhenReceiverWakesEarlier(t *testing.T) {
	t.Run("woken by another conversation", func(t *testing.T) {
		shortFanoutWindow(t, 160*time.Millisecond)
		st := withDelayedWaitCAS(&memStore{})
		f := newFakeHub().withMachine()
		s, ctx, _ := newFanoutTestService(t, st, f, "lead1", "lead2", "lead3")

		lead3Ch := make(chan string, 4)
		f.mu.Lock()
		f.onAssistantSend = func(_ *fakeHub, p map[string]any) {
			if id, _ := p["session_id"].(string); id == "lead3" {
				text, _ := p["text"].(string)
				lead3Ch <- text
			}
		}
		f.mu.Unlock()

		for _, id := range []string{"lead1", "lead2", "lead3"} {
			if _, err := s.toolConversationSend(ctx, "main", []byte(`{"id":"`+id+`","text":"from main to `+id+`"}`)); err != nil {
				t.Fatalf("send %s: %v", id, err)
			}
		}
		if waits := fanoutWaitsOf(t, s); len(waits) != 1 {
			t.Fatalf("deferred waits = %d, want 1", len(waits))
		}

		// lead1 wakes lead3 before the window elapses; lead3 reads main's kept message in that turn.
		if _, err := s.toolConversationSend(ctx, "lead1", []byte(`{"id":"lead3","text":"urgent ping from lead1"}`)); err != nil {
			t.Fatalf("lead1 -> lead3: %v", err)
		}
		var first string
		select {
		case first = <-lead3Ch:
		case <-time.After(2 * time.Second):
			t.Fatal("lead3 did not wake for lead1")
		}
		if !strings.Contains(first, "from main to lead3") || !strings.Contains(first, "urgent ping from lead1") {
			t.Fatalf("earlier wake did not carry kept message: %q", first)
		}
		waitFor(t, "deferred wake dropped on early wake", func() bool { return len(fanoutWaitsOf(t, s)) == 0 })

		// An explicit wake:false arriving afterwards must not trigger any wake when the original deadline passes.
		if _, err := s.toolConversationSend(ctx, "lead2", []byte(`{"id":"lead3","text":"later fyi","wake":false}`)); err != nil {
			t.Fatalf("quiet fyi: %v", err)
		}
		select {
		case extra := <-lead3Ch:
			t.Fatalf("dropped deferred wake fired a second time: %q", extra)
		case <-time.After(260 * time.Millisecond):
		}
		if got := f.sentTo("lead3"); got != 1 {
			t.Fatalf("lead3 wakes = %d, want 1", got)
		}
		if left := laterFor(s, "lead3"); len(left) != 1 || left[0].Text != "later fyi" {
			t.Fatalf("later quiet message disturbed: %+v", left)
		}
	})

	t.Run("woken by owner message", func(t *testing.T) {
		shortFanoutWindow(t, 160*time.Millisecond)
		st := withDelayedWaitCAS(&memStore{})
		f := newFakeHub().withMachine()
		s, ctx, _ := newFanoutTestService(t, st, f, "lead1", "lead2", "lead3")

		lead3Ch := make(chan string, 2)
		f.mu.Lock()
		f.onAssistantSend = func(_ *fakeHub, p map[string]any) {
			if id, _ := p["session_id"].(string); id == "lead3" {
				text, _ := p["text"].(string)
				lead3Ch <- text
			}
		}
		f.mu.Unlock()

		for _, id := range []string{"lead1", "lead2", "lead3"} {
			if _, err := s.toolConversationSend(ctx, "main", []byte(`{"id":"`+id+`","text":"kept for owner read `+id+`"}`)); err != nil {
				t.Fatalf("send %s: %v", id, err)
			}
		}
		if waits := fanoutWaitsOf(t, s); len(waits) != 1 {
			t.Fatalf("deferred waits = %d, want 1", len(waits))
		}

		// Owner sends a message to lead3, carrying the kept message and settling it on delivery confirmation.
		params := map[string]any{"text": "Owner checking in"}
		if _, err := s.withOwnerRelayLater(ctx, "server", "lead3", "owner-msg-1", params, true); err != nil {
			t.Fatalf("withOwnerRelayLater: %v", err)
		}
		if text, _ := params["text"].(string); !strings.Contains(text, "kept for owner read lead3") {
			t.Fatalf("owner send did not carry kept message: %q", text)
		}
		s.relayLaterOwnerOutcome(ctx, "lead3", "owner-msg-1", true)

		waitFor(t, "deferred wake dropped after owner send", func() bool { return len(fanoutWaitsOf(t, s)) == 0 })
		if left := laterFor(s, "lead3"); len(left) != 0 {
			t.Fatalf("Later[lead3] still has %d items after owner send", len(left))
		}
		select {
		case extra := <-lead3Ch:
			t.Fatalf("lead3 received a deferred wake after owner already woke it: %q", extra)
		case <-time.After(260 * time.Millisecond):
		}
	})
}

// Explicit wake: false messages are never woken automatically, whether under or over the fanout cap.
func TestConversationSendExplicitWakeFalseNeverWakesLater(t *testing.T) {
	shortFanoutWindow(t, 80*time.Millisecond)
	st := withDelayedWaitCAS(&memStore{})
	f := newFakeHub().withMachine()
	s, ctx, _ := newFanoutTestService(t, st, f, "lead1", "lead2", "lead3", "lead4")

	wokeCh := make(chan string, 4)
	f.mu.Lock()
	f.onAssistantSend = func(_ *fakeHub, p map[string]any) {
		if id, _ := p["session_id"].(string); id == "lead3" || id == "lead4" {
			wokeCh <- id
		}
	}
	f.mu.Unlock()

	// Explicit wake: false while under cap.
	out4, err := s.toolConversationSend(ctx, "main", []byte(`{"id":"lead4","text":"quiet under cap","wake":false}`))
	if err != nil || !strings.Contains(out4, "without waking it") {
		t.Fatalf("explicit wake:false under cap = %q, %v", out4, err)
	}
	// Fill cap, then send explicit wake: false.
	for _, id := range []string{"lead1", "lead2"} {
		if _, err := s.toolConversationSend(ctx, "main", []byte(`{"id":"`+id+`","text":"wake `+id+`"}`)); err != nil {
			t.Fatalf("send %s: %v", id, err)
		}
	}
	out3, err := s.toolConversationSend(ctx, "main", []byte(`{"id":"lead3","text":"quiet over cap","wake":false}`))
	if err != nil || !strings.Contains(out3, "without waking it") {
		t.Fatalf("explicit wake:false over cap = %q, %v", out3, err)
	}
	if waits := fanoutWaitsOf(t, s); len(waits) != 0 {
		t.Fatalf("explicit wake:false created %d deferred wakes", len(waits))
	}
	for _, id := range []string{"lead3", "lead4"} {
		left := laterFor(s, id)
		if len(left) != 1 || left[0].DeferredWake {
			t.Fatalf("Later[%s] = %+v, want 1 item with DeferredWake=false", id, left)
		}
	}
	select {
	case id := <-wokeCh:
		t.Fatalf("explicit wake:false message woke %s", id)
	case <-time.After(220 * time.Millisecond):
	}

	// Defense-in-depth: even if a stale delayedFanoutWake fires for ("main", "lead3") while only an explicit
	// wake:false message from "main" sits in Later["lead3"], it must not wake lead3 or remove that quiet message.
	stale := delayedWait{
		ID:     "fanout_stale_quiet_only",
		Kind:   delayedFanoutWake,
		At:     time.Now().Add(-time.Second).UTC(),
		Fanout: &fanoutWake{Sender: "main", Target: "lead3", MachineID: "server"},
	}
	if err := s.updateDelayedWaits(ctx, func(doc *delayedWaitDoc) (bool, error) {
		doc.Waits = append(doc.Waits, stale)
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	s.runFanoutWake(ctx, stale, true)
	if got := f.sentTo("lead3"); got != 0 {
		t.Fatalf("stale deferred wake woke explicit wake:false message (%d sends)", got)
	}
	if left := laterFor(s, "lead3"); len(left) != 1 || left[0].Text != "quiet over cap" {
		t.Fatalf("explicit wake:false message was disturbed by stale deferred wake: %+v", left)
	}
	if waits := fanoutWaitsOf(t, s); len(waits) != 0 {
		t.Fatalf("stale deferred wake was not cleaned up: %+v", waits)
	}
}

// Deferred wakes and recent fanout window timestamps survive a hub restart: the new manager keeps the original
// deadline, preserves the cap across restart, wakes the target exactly once, and does not double-wake on another
// restart or after a crash while claimed.
func TestConversationSendDeferredWakeSurvivesHubRestart(t *testing.T) {
	// This case constructs and restores two managers before exercising the persisted cap. Keep the window long
	// enough for that setup under load; the original deadline and all wake-once assertions remain unchanged.
	shortFanoutWindow(t, 2*time.Second)
	st := withDelayedWaitCAS(&memStore{})
	f := newFakeHub().withMachine()

	first, ctx1, cancel1 := newFanoutTestService(t, st, f, "lead1", "lead2", "lead3", "lead4")
	for _, id := range []string{"lead1", "lead2", "lead3"} {
		if _, err := first.toolConversationSend(ctx1, "main", []byte(`{"id":"`+id+`","text":"restart check `+id+`"}`)); err != nil {
			t.Fatalf("first send %s: %v", id, err)
		}
	}
	waits := fanoutWaitsOf(t, first)
	if len(waits) != 1 || waits[0].Fanout == nil || waits[0].Fanout.Target != "lead3" {
		t.Fatalf("persisted waits before restart = %+v", waits)
	}
	origAt := waits[0].At

	// Stop the first manager before the deadline fires (simulating a hub restart).
	cancel1()
	first.sendFanout.stopAllTimers()

	// Boot a second manager on the same durable store and restore delayed waits.
	second, ctx2, cancel2 := newFanoutTestService(t, st, f, "lead1", "lead2", "lead3", "lead4")
	defer cancel2()
	sent3 := make(chan string, 4)
	f.mu.Lock()
	f.onAssistantSend = func(_ *fakeHub, p map[string]any) {
		if id, _ := p["session_id"].(string); id == "lead3" {
			text, _ := p["text"].(string)
			sent3 <- text
		}
	}
	f.mu.Unlock()

	if !second.restoreDelayedWaits(ctx2) {
		t.Fatal("restoreDelayedWaits returned false")
	}
	restored := fanoutWaitsOf(t, second)
	if len(restored) != 1 || !restored[0].At.Equal(origAt) {
		t.Fatalf("restored wait deadline = %+v, want %v", restored, origAt)
	}

	// Sending to lead4 right after restart still sees lead1 & lead2 in the persisted fanout window and is also deferred.
	out4, err := second.toolConversationSend(ctx2, "main", []byte(`{"id":"lead4","text":"post-restart lead4"}`))
	if err != nil {
		t.Fatalf("second send lead4: %v", err)
	}
	if !strings.Contains(out4, "woken automatically in about") {
		t.Fatalf("post-restart send bypassed persisted fanout window: %q", out4)
	}
	if got := f.sentTo("lead4"); got != 0 {
		t.Fatalf("lead4 woke immediately after restart: %d sends", got)
	}

	// lead3 must wait until origAt, then wake automatically once.
	if rem := time.Until(origAt); rem > 60*time.Millisecond {
		select {
		case early := <-sent3:
			t.Fatalf("restored deferred wake fired before its deadline: %q", early)
		case <-time.After(40 * time.Millisecond):
		}
	}
	select {
	case got := <-sent3:
		if !strings.Contains(got, "restart check lead3") {
			t.Fatalf("restored wake text = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restored deferred wake did not fire after restart")
	}
	waitFor(t, "post-restart lead4 deferred wake", func() bool { return f.sentTo("lead4") == 1 })
	waitFor(t, "restored deferred wakes cleared", func() bool { return len(fanoutWaitsOf(t, second)) == 0 })

	// A third restart must not re-fire anything.
	third, ctx3, cancel3 := newFanoutTestService(t, st, f, "lead1", "lead2", "lead3", "lead4")
	defer cancel3()
	if !third.restoreDelayedWaits(ctx3) {
		t.Fatal("third restoreDelayedWaits returned false")
	}
	select {
	case extra := <-sent3:
		t.Fatalf("third restart produced duplicate wake: %q", extra)
	case <-time.After(80 * time.Millisecond):
	}
	if got := f.sentTo("lead3"); got != 1 {
		t.Fatalf("lead3 wakes across restarts = %d, want 1", got)
	}
}

// A crash while a deferred fanout wake is claimed recovers cleanly on restart: if the kept message was not yet
// promoted, it is requeued and fires once; if it was already promoted/consumed before the crash, recovery drops the
// claim without a duplicate wake.
func TestConversationSendDeferredWakeCrashWhileClaimedRecovery(t *testing.T) {
	t.Run("not yet promoted fires once", func(t *testing.T) {
		shortFanoutWindow(t, 60*time.Millisecond)
		st := withDelayedWaitCAS(&memStore{})
		f := newFakeHub().withMachine()
		first, ctx1, cancel1 := newFanoutTestService(t, st, f, "lead3")
		if _, ok := first.keepRelayLater("lead3", relayMsg{
			From: "main", FromTitle: "主对话", Tool: "conversation_send",
			Text: "claimed before crash", At: time.Now().UTC(), DeferredWake: true,
		}); !ok {
			t.Fatal("keepRelayLater failed")
		}
		if err := first.updateDelayedWaits(ctx1, func(doc *delayedWaitDoc) (bool, error) {
			doc.Waits = append(doc.Waits, delayedWait{
				ID: "fanout_crashed_1", Kind: delayedFanoutWake, At: time.Now().Add(-time.Second).UTC(),
				Claim: "dead-manager", Fanout: &fanoutWake{Sender: "main", Target: "lead3", MachineID: "server"},
			})
			return true, nil
		}); err != nil {
			t.Fatal(err)
		}
		cancel1()

		second, ctx2, cancel2 := newFanoutTestService(t, st, f, "lead3")
		defer cancel2()
		if !second.restoreDelayedWaits(ctx2) {
			t.Fatal("restoreDelayedWaits returned false")
		}
		waitFor(t, "recovered claimed wake to deliver once", func() bool {
			return f.sentTo("lead3") == 1 && len(fanoutWaitsOf(t, second)) == 0
		})
	})

	t.Run("already consumed drops without double wake", func(t *testing.T) {
		shortFanoutWindow(t, 60*time.Millisecond)
		st := withDelayedWaitCAS(&memStore{})
		f := newFakeHub().withMachine()
		first, ctx1, cancel1 := newFanoutTestService(t, st, f, "lead3")
		if err := first.updateDelayedWaits(ctx1, func(doc *delayedWaitDoc) (bool, error) {
			doc.Waits = append(doc.Waits, delayedWait{
				ID: "fanout_crashed_2", Kind: delayedFanoutWake, At: time.Now().Add(-time.Second).UTC(),
				Claim: "dead-manager", Fanout: &fanoutWake{Sender: "main", Target: "lead3", MachineID: "server"},
			})
			return true, nil
		}); err != nil {
			t.Fatal(err)
		}
		cancel1()

		second, ctx2, cancel2 := newFanoutTestService(t, st, f, "lead3")
		defer cancel2()
		if !second.restoreDelayedWaits(ctx2) {
			t.Fatal("restoreDelayedWaits returned false")
		}
		waitFor(t, "stale claimed wake to be dropped", func() bool { return len(fanoutWaitsOf(t, second)) == 0 })
		if got := f.sentTo("lead3"); got != 0 {
			t.Fatalf("already-consumed claimed wake sent %d times, want 0", got)
		}
	})
}

// Multiple capped messages from the same sender to the same receiver coalesce into one deferred wake, and any explicit
// wake: false message waiting alongside them rides along in that single turn without triggering an extra wake.
func TestConversationSendMultipleCappedToSameTargetCoalesce(t *testing.T) {
	shortFanoutWindow(t, 120*time.Millisecond)
	st := withDelayedWaitCAS(&memStore{})
	f := newFakeHub().withMachine()
	s, ctx, _ := newFanoutTestService(t, st, f, "lead1", "lead2", "lead3")

	lead3Ch := make(chan string, 4)
	f.mu.Lock()
	f.onAssistantSend = func(_ *fakeHub, p map[string]any) {
		if id, _ := p["session_id"].(string); id == "lead3" {
			text, _ := p["text"].(string)
			lead3Ch <- text
		}
	}
	f.mu.Unlock()

	for _, id := range []string{"lead1", "lead2"} {
		if _, err := s.toolConversationSend(ctx, "main", []byte(`{"id":"`+id+`","text":"immediate `+id+`"}`)); err != nil {
			t.Fatalf("send %s: %v", id, err)
		}
	}
	if _, err := s.toolConversationSend(ctx, "lead2", []byte(`{"id":"lead3","text":"quiet side note","wake":false}`)); err != nil {
		t.Fatalf("quiet note: %v", err)
	}
	if _, err := s.toolConversationSend(ctx, "main", []byte(`{"id":"lead3","text":"part one"}`)); err != nil {
		t.Fatalf("part one: %v", err)
	}
	if _, err := s.toolConversationSend(ctx, "main", []byte(`{"id":"lead3","text":"part two"}`)); err != nil {
		t.Fatalf("part two: %v", err)
	}
	if waits := fanoutWaitsOf(t, s); len(waits) != 1 {
		t.Fatalf("expected 1 coalesced deferred wake, got %d", len(waits))
	}
	var delivered string
	select {
	case delivered = <-lead3Ch:
	case <-time.After(2 * time.Second):
		t.Fatal("coalesced deferred wake did not fire")
	}
	if !strings.Contains(delivered, "quiet side note") ||
		!strings.Contains(delivered, "part one") ||
		!strings.Contains(delivered, "part two") ||
		strings.Index(delivered, "part one") > strings.Index(delivered, "part two") {
		t.Fatalf("coalesced delivery missing or out of order: %q", delivered)
	}
	waitFor(t, "deferred wake cleared", func() bool { return len(fanoutWaitsOf(t, s)) == 0 })
	select {
	case extra := <-lead3Ch:
		t.Fatalf("coalesced wake fired a second time: %q", extra)
	case <-time.After(180 * time.Millisecond):
	}
	if got := f.sentTo("lead3"); got != 1 {
		t.Fatalf("lead3 wakes = %d, want 1", got)
	}
}

// A due deferred wake waits if an owner send carrying Later[target] is currently in flight, then drops cleanly once
// that owner send confirms delivery (preventing an in-flight double-wake race).
func TestConversationSendDeferredWakeWaitsForInFlightOwnerSend(t *testing.T) {
	shortFanoutWindow(t, 80*time.Millisecond)
	st := withDelayedWaitCAS(&memStore{})
	f := newFakeHub().withMachine()
	s, ctx, _ := newFanoutTestService(t, st, f, "lead1", "lead2", "lead3")

	for _, id := range []string{"lead1", "lead2", "lead3"} {
		if _, err := s.toolConversationSend(ctx, "main", []byte(`{"id":"`+id+`","text":"msg `+id+`"}`)); err != nil {
			t.Fatalf("send %s: %v", id, err)
		}
	}
	// Start an owner send to lead3 carrying Later["lead3"], leaving it in flight while the fanout window elapses.
	params := map[string]any{"text": "Owner message in flight"}
	if _, err := s.withOwnerRelayLater(ctx, "server", "lead3", "owner-inflight-1", params, true); err != nil {
		t.Fatalf("withOwnerRelayLater: %v", err)
	}

	// Wait past fanoutWindow while owner send is still unsettled: deferred wake must defer rather than double-wake.
	select {
	case <-time.After(140 * time.Millisecond):
	}
	if got := f.sentTo("lead3"); got != 0 {
		t.Fatalf("deferred wake fired while owner send was in flight: %d sends", got)
	}

	// Confirm the owner send; the deferred wake must now drop without waking lead3 again.
	s.relayLaterOwnerOutcome(ctx, "lead3", "owner-inflight-1", true)
	waitFor(t, "deferred wake dropped after in-flight owner send settles", func() bool {
		return len(fanoutWaitsOf(t, s)) == 0
	})
	select {
	case <-time.After(120 * time.Millisecond):
	}
	if got := f.sentTo("lead3"); got != 0 {
		t.Fatalf("deferred wake fired after owner send settled: %d sends", got)
	}
}

// Storeless services (no durable Store) still fire deferred wakes once and drop them on an earlier wake.
func TestConversationSendDeferredWakeStoreless(t *testing.T) {
	shortFanoutWindow(t, 100*time.Millisecond)
	f := newFakeHub().withMachine()
	s, ctx, _ := newFanoutTestService(t, nil, f, "lead1", "lead2", "lead3")

	lead3Ch := make(chan string, 2)
	f.mu.Lock()
	f.onAssistantSend = func(_ *fakeHub, p map[string]any) {
		if id, _ := p["session_id"].(string); id == "lead3" {
			text, _ := p["text"].(string)
			lead3Ch <- text
		}
	}
	f.mu.Unlock()

	for _, id := range []string{"lead1", "lead2", "lead3"} {
		if _, err := s.toolConversationSend(ctx, "main", []byte(`{"id":"`+id+`","text":"storeless `+id+`"}`)); err != nil {
			t.Fatalf("send %s: %v", id, err)
		}
	}
	select {
	case got := <-lead3Ch:
		if !strings.Contains(got, "storeless lead3") {
			t.Fatalf("storeless wake text = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("storeless deferred wake did not fire")
	}
	select {
	case extra := <-lead3Ch:
		t.Fatalf("storeless deferred wake fired twice: %q", extra)
	case <-time.After(150 * time.Millisecond):
	}
	if got := f.sentTo("lead3"); got != 1 {
		t.Fatalf("storeless lead3 wakes = %d, want 1", got)
	}
}
