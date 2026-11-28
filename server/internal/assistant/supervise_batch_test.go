package assistant

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

func TestSupervisionBatchImmediateSignals(t *testing.T) {
	immediate := []string{sigNeedsYou, sigAsk, sigEscalated, sigDecide, sigDecisionReply, sigParkDecided, sigOwnerCannot, sigRecheck, sigUndecidable, sigFailed, sigParkedDue}
	immediate = append(immediate, sigBlockersUnblocked, sigMergeTrainTurn, sigWriteConflict)
	for _, kind := range immediate {
		if !supervisionBatchImmediate(superviseSignal{Kind: kind}) {
			t.Errorf("%s must bypass routine batching", kind)
		}
	}
	// The legacy urgent set is deliberately not the new allowlist: these retain their old urgency only when batching is off.
	for _, kind := range []string{sigReview, sigIdle, sigUnjudged, sigStalled, sigNoProgress, sigAllStopped, sigInterrupted, sigStarted, sigPRConflict, sigDraftPR, sigReport, sigOrphaned} {
		if supervisionBatchImmediate(superviseSignal{Kind: kind, Report: upReportDone}) {
			t.Errorf("%s unexpectedly bypasses routine batching", kind)
		}
	}
}

func TestDigestWaitWithBatchDefaultMatchesLegacy(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	p := shortSuperviseTimings
	cases := [][]superviseSignal{
		nil,
		{{Kind: sigReview, At: now}},
		{{Kind: sigNeedsYou, At: now.Add(-time.Minute)}},
		{{Kind: sigFailed, At: now}, {Kind: sigStalled, At: now.Add(-time.Second)}},
	}
	for _, last := range []time.Time{{}, now.Add(-time.Hour), now} {
		for _, pending := range cases {
			want, wantOK := digestWait(now, last, pending, p)
			got, gotOK := digestWaitWithBatch(now, last, pending, p, 0)
			if got != want || gotOK != wantOK {
				t.Errorf("last=%v pending=%v: got (%v, %v), legacy (%v, %v)", last, pending, got, gotOK, want, wantOK)
			}
		}
	}
}

func TestSupervisionBatchAnchorsFirstRoutineSignal(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	s := &Service{}
	s.supervise.init()
	first := superviseSignal{Todo: "first", Kind: sigStalled, Key: "one", At: now.Add(-2 * time.Minute)}
	second := superviseSignal{Todo: "second", Kind: sigInterrupted, Key: "two", At: now.Add(-time.Minute)}
	groups := map[string][]superviseSignal{"recipient": {first, second}}
	if order := s.gateDigestBatches([]string{"recipient"}, groups, now, 45*time.Minute, 0); len(order) != 0 {
		t.Fatalf("routine batch released early: %v", order)
	}
	want := first.At.Add(45 * time.Minute)
	if got := s.supervise.batch["recipient"]; !got.Equal(want) {
		t.Fatalf("deadline = %v, want first signal anchor %v", got, want)
	}
	if len(s.supervise.pending) != 2 {
		t.Fatalf("held %d signals, want 2", len(s.supervise.pending))
	}
	for _, g := range s.supervise.pending {
		if g.batchTo != "recipient" || !g.batchUntil.Equal(want) {
			t.Errorf("held signal lost its recipient deadline: %+v", g)
		}
	}
}

func TestSupervisionBatchRecipientsHaveIndependentDeadlines(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	s := &Service{}
	s.supervise.init()
	s.supervise.batch["due"] = now.Add(-time.Second)
	s.supervise.batch["later"] = now.Add(time.Hour)
	groups := map[string][]superviseSignal{
		"due":   {{Todo: "due-task", Kind: sigStalled, Key: "due", At: now.Add(-time.Hour)}},
		"later": {{Todo: "later-task", Kind: sigStalled, Key: "later", At: now.Add(-time.Hour)}},
	}
	order := s.gateDigestBatches([]string{"due", "later"}, groups, now, time.Hour, 0)
	if !slices.Equal(order, []string{"due"}) {
		t.Fatalf("released recipients = %v, want only due", order)
	}
	if len(groups["due"]) != 1 || len(s.supervise.pending) != 1 {
		t.Fatalf("wrong release/hold: groups=%v pending=%v", groups, s.supervise.pending)
	}
	held := s.supervise.pending["later-task|"+sigStalled]
	if held.batchTo != "later" || !held.batchUntil.Equal(now.Add(time.Hour)) {
		t.Fatalf("the other recipient's deadline changed: %+v", held)
	}
}

func TestSupervisionBatchImmediateReleasesWholeRecipientDigest(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	shortSupervise(t, s)
	setSettings(t, s, func(v *Settings) { v.SupervisionBatchMinutes = ptr(60) })
	routine := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "routine-batch", Detail: "routine detail", Minutes: 50}
	s.queueSignal(routine)
	waitFor(t, "the routine signal to be held", func() bool {
		s.supervise.mu.Lock()
		defer s.supervise.mu.Unlock()
		return !s.supervise.pending[routine.id()].batchUntil.IsZero()
	})
	if got := len(assistantSendsTo(f, "conv1")); got != 0 {
		t.Fatalf("routine signal was not batched: %d sends", got)
	}
	s.queueSignal(superviseSignal{Todo: todo.ID, Kind: sigFailed, Key: "immediate-failed", Detail: "start failed"})
	waitFor(t, "the mixed immediate digest", func() bool { return len(assistantSendsTo(f, "conv1")) == 1 })
	send := lastSendTo(f, "conv1")
	text, _ := send["text"].(string)
	for _, want := range []string{"signal=" + sigStalled, "signal=" + sigFailed, "routine detail", "start failed"} {
		if !strings.Contains(text, want) {
			t.Errorf("mixed digest lacks %q:\n%s", want, text)
		}
	}
	s.supervise.mu.Lock()
	defer s.supervise.mu.Unlock()
	if len(s.supervise.pending) != 0 || !s.supervise.batch["conv1"].IsZero() {
		t.Fatalf("released recipient still has batch state: pending=%v batch=%v", s.supervise.pending, s.supervise.batch)
	}
}

func TestSupervisionBatchImmediateRescreensReleasedIdleWake(t *testing.T) {
	for _, tc := range []struct {
		name     string
		next     time.Duration
		stop     bool
		needsYou bool
		wantIdle bool
	}{
		{name: "future wakeup suppresses idle"},
		{name: "future wakeup suppresses idle beside needs you", needsYou: true},
		{name: "expired wakeup still sends idle", next: -time.Minute, wantIdle: true},
		{name: "stop true still sends idle", stop: true, wantIdle: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			shortSupervise(t, s)
			s.superviseTimes.cooldown = time.Hour
			sid := todo.Agents[0].SessionID
			now := time.Now()
			s.toldRecipient("conv1", now)
			sess := proto.Session{SessionID: sid, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
				State: proto.StateUnread, LastText: "synthetic turn complete", UpdatedAt: now.Add(-time.Minute)}
			f.setSessions("box", sess)
			idle := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "turn-wake", Detail: "synthetic idle", At: now.Add(-time.Minute)}
			s.queueSignal(idle)
			waitFor(t, "the idle signal to be held", func() bool {
				s.supervise.mu.Lock()
				defer s.supervise.mu.Unlock()
				return !s.supervise.pending[idle.id()].batchUntil.IsZero()
			})
			if got := len(assistantSendsTo(f, "conv1")); got != 0 {
				t.Fatalf("idle signal was not batched: %d sends", got)
			}

			// The wake is scheduled after the idle was held, as happens while a digest waits for its recipient's deadline.
			sess.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}
			if tc.next != 0 {
				sess.Loop.NextAt = now.Add(tc.next)
			}
			if tc.stop {
				meta := transcript.Meta{Loop: sess.Loop}
				transcript.ParseLine(1, []byte(`{"type":"assistant","timestamp":"2026-11-22T10:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"wake-stop","name":"ScheduleWakeup","input":{"stop":true}}]}}`), &meta)
				sess.Loop = meta.Loop
			}
			immediate := superviseSignal{Todo: todo.ID, Kind: sigReport, Report: upReportQuestion,
				Key: "question", Detail: "synthetic question", At: now}
			if tc.needsYou {
				sess.State = proto.StateNeedsYou
				sess.PendingInteractions = 1
				immediate.Kind, immediate.Report = sigNeedsYou, ""
				immediate.Key, immediate.Detail = "needs-you", "synthetic answer needed"
			}
			f.setSessions("box", sess)
			s.queueSignal(immediate)
			waitFor(t, "the immediate digest", func() bool { return len(assistantSendsTo(f, "conv1")) == 1 })
			text, _ := lastSendTo(f, "conv1")["text"].(string)
			if !strings.Contains(text, "signal="+immediate.Kind) && !strings.Contains(text, "+"+immediate.Kind) { // a report joins its turn's stop (joinTurnReports)
				t.Fatalf("immediate signal was not delivered: %s", text)
			}
			if got := strings.Contains(text, "signal="+sigIdle); got != tc.wantIdle {
				t.Fatalf("idle delivered = %v, want %v: %s", got, tc.wantIdle, text)
			}
			if !tc.wantIdle {
				s.supervise.mu.Lock()
				deferred, ok := s.supervise.pending[idle.id()]
				s.supervise.mu.Unlock()
				if !ok || deferred.batchTo != "" || !deferred.batchUntil.IsZero() {
					t.Fatalf("deferred idle retained its released batch: pending=%v signal=%+v", ok, deferred)
				}
			}
		})
	}
	t.Run("queued drain rechecks future wake", func(t *testing.T) {
		s, f, todo := newSupervisedService(t)
		shortSupervise(t, s)
		sid := todo.Agents[0].SessionID
		now := time.Now()
		f.setSessions("box", proto.Session{SessionID: sid, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
			State: proto.StateUnread, LastText: "synthetic turn complete", UpdatedAt: now.Add(-time.Minute),
			Loop: &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}})
		idle := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "turn-wake", Detail: "synthetic idle", At: now.Add(-time.Minute)}
		msg := relayMsg{From: superviseTool, Tool: superviseTool, Text: "synthetic supervision", Signals: encodeDigestSignals([]superviseSignal{idle}), At: now}
		if n, err := s.queueRelayDurable("conv1", msg); err != nil || n != 1 {
			t.Fatalf("queue synthetic supervision: n=%d err=%v", n, err)
		}
		s.relays.mu.Lock()
		queued := s.relays.st.Queue["conv1"][0]
		s.relays.mu.Unlock()
		s.supervise.mu.Lock()
		s.supervise.init()
		s.supervise.mu.Unlock()

		_, stale, keep := s.recheckQueuedDigest(context.Background(), "conv1", queued)
		if keep || !stale {
			t.Fatalf("future-wakeup idle remained in the drained digest: stale=%v keep=%v", stale, keep)
		}
		s.supervise.mu.Lock()
		_, deferred := s.supervise.pending[idle.id()]
		s.supervise.mu.Unlock()
		if !deferred {
			t.Fatal("the queued future-wakeup idle was not restored for rescreening")
		}
	})
}

func TestSupervisionBatchRealRecipientsDoNotHoldEachOther(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	shortSupervise(t, s)
	bg := withBackgroundBG1(t, s, f)
	setSettings(t, s, func(v *Settings) { v.SupervisionBatchMinutes = ptr(1) })
	now := time.Now()
	wakeRound(s,
		superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "main-due", To: "conv1", At: now.Add(-2 * time.Minute), Minutes: 50},
		superviseSignal{Todo: todo.ID, Kind: sigInterrupted, Key: "background-later", To: bg, At: now.Add(-58 * time.Second)},
	)
	if got := len(assistantSendsTo(f, "conv1")); got != 1 {
		t.Fatalf("due main recipient got %d messages, want 1", got)
	}
	if got := len(assistantSendsTo(f, bg)); got != 0 {
		t.Fatalf("the other recipient's deadline was released early: %d messages", got)
	}
	waitFor(t, "the background recipient's own deadline", func() bool { return len(assistantSendsTo(f, bg)) == 1 })
	if got := len(assistantSendsTo(f, "conv1")); got != 1 {
		t.Fatalf("the background batch woke the main recipient again: %d messages", got)
	}
}

func TestSupervisionBatchDueDigestsUseExistingBusyQueue(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	shortSupervise(t, s)
	bg := withBackgroundBG1(t, s, f)
	setSettings(t, s, func(v *Settings) { v.SupervisionBatchMinutes = ptr(1) })
	listRound(f, bg, proto.StatusWorking, false)
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "batch-one", To: bg, At: time.Now().Add(-2 * time.Minute), Minutes: 50})
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigInterrupted, Key: "batch-two", To: bg, At: time.Now().Add(-2 * time.Minute)})
	if n := s.queuedFor(bg); n != 1 || len(assistantSendsTo(f, bg)) != 0 {
		t.Fatalf("two due batches made %d queued messages and %d sends; want one queued, no sends", n, len(assistantSendsTo(f, bg)))
	}
	s.drainRelays("server", listRound(f, bg, proto.StatusIdle, false))
	waitFor(t, "the one merged supervision round", func() bool { return len(assistantSendsTo(f, bg)) == 1 })
	settleService(t, s)
	send := lastSendTo(f, bg)
	text, _ := send["text"].(string)
	for _, want := range []string{"signal=" + sigStalled, "signal=" + sigInterrupted} {
		if !strings.Contains(text, want) {
			t.Errorf("merged queued digest lacks %q:\n%s", want, text)
		}
	}
	if got := len(assistantSendsTo(f, bg)); got != 1 {
		t.Fatalf("busy recipient received %d wakeups, want exactly 1", got)
	}
}

func TestSupervisionBatchIntervalValidation(t *testing.T) {
	for _, test := range []struct {
		minutes int
		want    time.Duration
	}{{0, 0}, {45, 45 * time.Minute}, {1440, 1440 * time.Minute}, {-1, 0}, {1441, 0}} {
		v := Settings{SupervisionBatchMinutes: &test.minutes}
		if got := time.Duration(supervisionBatchMinutesOf(v)) * time.Minute; got != test.want {
			t.Errorf("%d minutes: got %v, want %v", test.minutes, got, test.want)
		}
	}
	if got := supervisionBatchMinutesOf(Settings{}); got != 0 {
		t.Fatalf("unset default = %d, want 0", got)
	}
}
