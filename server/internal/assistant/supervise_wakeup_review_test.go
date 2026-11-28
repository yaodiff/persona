package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func captureWakeLogs(s *Service) func() []string {
	var mu sync.Mutex
	var lines []string
	s.logf = func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

func idleDigestSends(f *fakeHub, conversation string) []string {
	var out []string
	for _, send := range assistantSendsTo(f, conversation) {
		text, _ := send["text"].(string)
		if strings.Contains(text, "signal="+sigIdle) {
			out = append(out, text)
		}
	}
	return out
}

func idleWakeSends(f *fakeHub, machine, session, detail string) []string {
	var out []string
	for _, text := range sendsTo(f, machine, session) {
		if strings.Contains(text, detail) {
			out = append(out, text)
		}
	}
	return out
}

func queuedIdleWakeFixture(t *testing.T) (*Service, *fakeHub, Todo, superviseSignal, proto.Session) {
	t.Helper()
	return queuedIdleWakeFixtureOn(t, nil)
}

func queuedIdleWakeFixtureOn(t *testing.T, st Store) (*Service, *fakeHub, Todo, superviseSignal, proto.Session) {
	t.Helper()
	ctx := context.Background()
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	sid := todo.Agents[0].SessionID
	now := time.Now()
	agent := proto.Session{SessionID: sid, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
		State: proto.StateUnread, LastText: "synthetic turn complete", TurnError: "synthetic turn failure", UpdatedAt: now.Add(-time.Minute),
		Loop: &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}}
	f.setSessions("box", agent)
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking})
	idle := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "turn-wake", Machine: "box", Session: sid,
		Detail: "synthetic idle after a failed turn", At: now.Add(-time.Minute)}
	turns := &s.tasks.turns
	turns.mu.Lock()
	if !s.turnsLocked(ctx) {
		turns.mu.Unlock()
		t.Fatal("cannot load the turn ledger")
	}
	turns.doc.Turns[todo.ID] = taskTurn{Machine: "box", Session: sid, Key: idle.Key, State: turnDone, Seen: now,
		Signal: &turnSignal{Kind: idle.Kind, Key: idle.Key, Machine: idle.Machine, Detail: idle.Detail}}
	turns.mu.Unlock()

	s.supervise.dedupMu.Lock()
	s.updateSupervisePending(func(pending map[string]superviseSignal) { pending[idle.id()] = idle })
	accepted := s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{idle}, s.webBase(ctx), now, time.Time{})
	s.supervise.dedupMu.Unlock()
	if !accepted || s.queuedFor("conv1") != 1 {
		t.Fatalf("real digest admission did not queue the idle: accepted=%v queue=%d", accepted, s.queuedFor("conv1"))
	}
	turns.mu.Lock()
	told := turns.doc.Turns[todo.ID].Signal.Told != nil
	turns.mu.Unlock()
	if !told {
		t.Fatal("queue admission did not reserve the turn ledger")
	}
	return s, f, todo, idle, agent
}

func TestQueuedWakeDrainReleasesDeliveryReservations(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%v", restart), func(t *testing.T) {
			st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
			s, f, todo, idle, agent := queuedIdleWakeFixtureOn(t, st)
			logs := captureWakeLogs(s)
			drainQueuedDigest(t, s, f)
			if s.queuedFor("conv1") != 0 {
				t.Fatalf("future-wakeup idle was not removed from the durable source queue: queue=%d", s.queuedFor("conv1"))
			}
			wantWake := agent.Loop.NextAt.Format(time.RFC3339Nano)
			foundDrainLog := false
			for _, line := range logs() {
				if strings.Contains(line, "boundary=drain") && strings.Contains(line, "wake="+wantWake) && strings.Contains(line, "conflict=turn_error") {
					foundDrainLog = true
				}
			}
			if !foundDrainLog {
				t.Errorf("drain withholding lacks its boundary, wake and conflict: %q", logs())
			}

			if restart {
				simulateSupervisionRestart(t, s)
			}

			s.superviseTimes = superviseTimings{}
			agent.Loop.NextAt = time.Now().Add(-time.Minute)
			f.setSessions("box", agent)
			f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
			wakeRound(s)
			if got := idleDigestSends(f, "conv1"); len(got) != 1 || !strings.Contains(got[0], idle.Detail) {
				turns := &s.tasks.turns
				turns.mu.Lock()
				sig := turns.doc.Turns[todo.ID].Signal
				turns.mu.Unlock()
				s.supervise.mu.Lock()
				pending := len(s.supervise.pending)
				s.supervise.mu.Unlock()
				t.Fatalf("expired wakeup without a new turn lost the idle: sends=%q pending=%d ledger=%+v logs=%q", got, pending, sig, logs())
			}
		})
	}
}

func TestQueuedWakeDrainKeepOrTriedDoesNotRestore(t *testing.T) {
	for _, tc := range []struct {
		name  string
		tried bool
	}{
		{name: "queue head changed"},
		{name: "uncertain earlier delivery", tried: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, idle, _ := queuedIdleWakeFixture(t)
			s.relays.mu.Lock()
			queued := s.relays.st.Queue["conv1"][0]
			if tc.tried {
				queued.TriedAt = time.Now().Add(-time.Second)
				s.relays.st.Queue["conv1"][0] = queued
			} else {
				queued.Text += " stale copy"
			}
			s.relays.mu.Unlock()

			_, _, keep := s.recheckQueuedDigest(context.Background(), "conv1", queued)
			if wantKeep := !tc.tried; keep != wantKeep {
				t.Fatalf("queue keep = %v, want %v (tried=%v)", keep, wantKeep, tc.tried)
			}
			s.supervise.mu.Lock()
			_, restored := s.supervise.pending[idle.id()]
			s.supervise.mu.Unlock()
			if restored || s.queuedFor("conv1") != 1 {
				t.Fatalf("a kept queue entry was also restored to pending: restored=%v queue=%d", restored, s.queuedFor("conv1"))
			}
		})
	}
}

func TestQueuedWakeDrainPersistsDeferredIdleBeforeRestore(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, _, _, idle, _ := queuedIdleWakeFixtureOn(t, st)
	queued := queuedRelays(s, "conv1")
	if len(queued) != 1 {
		t.Fatalf("queue fixture has %d messages", len(queued))
	}
	ctx := context.Background()
	s.supervise.dedupMu.Lock()
	locked := true
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.recheckQueuedDigest(ctx, "conv1", queued[0])
	}()
	defer func() {
		if locked {
			s.supervise.dedupMu.Unlock()
		}
		<-done
	}()

	// Holding dedupMu stops restoreDrainedWakeSignals after the queue checkpoint. The deferred idle must already
	// have a durable outbox copy in that same checkpoint, before any in-memory restoration can run.
	waitFor(t, "the source queue rewrite before restoration", func() bool {
		var state relayState
		if _, err := st.Setting(ctx, relayKey, &state); err != nil {
			return false
		}
		return len(state.Queue["conv1"]) == 0
	})
	persisted := storedSupervisePending(t, s)
	if got := persisted[idle.id()]; got.Key != idle.Key {
		t.Fatalf("deferred idle is not durable between queue rewrite and restore: got=%+v all=%+v", got, persisted)
	}
	s.supervise.dedupMu.Unlock()
	locked = false
	waitFor(t, "the drain to finish", func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	})
}

func TestUndeliveredDecisionIdleIgnoresFutureWake(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	now := time.Now()
	sid := todo.Agents[0].SessionID
	f.setSessions("box", proto.Session{SessionID: sid, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
		State: proto.StateNeedsYou, PendingInteractions: 1, LastText: "synthetic question", UpdatedAt: now.Add(-time.Minute),
		Loop: &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}})
	res := s.decisionNotDelivered(context.Background(), todo.ID, now.Add(-time.Minute), "use the safe option", errors.New("synthetic send failure"))
	if !res.taken || res.undelivered == nil {
		t.Fatalf("decision failure was not handed off: %+v", res)
	}
	waitFor(t, "the undelivered decision to reach its supervisor despite the future wakeup", func() bool {
		return len(idleDigestSends(f, "conv1")) == 1
	})
	if got := idleDigestSends(f, "conv1"); !strings.Contains(got[0], "use the safe option") {
		t.Fatalf("the handoff lost the owner's decision: %q", got)
	}
}

func TestUndeliveredDecisionIdleSurvivesOrdinaryIdle(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	now := time.Now()
	sid := todo.Agents[0].SessionID
	f.setSessions("box", proto.Session{SessionID: sid, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
		State: proto.StateUnread, TurnError: "synthetic turn failure", UpdatedAt: now.Add(-time.Minute),
		Loop: &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}})
	res := s.decisionNotDelivered(context.Background(), todo.ID, now.Add(-time.Minute), "use the safe option", errors.New("synthetic send failure"))
	if !res.taken || res.undelivered == nil {
		t.Fatalf("decision failure was not handed off: %+v", res)
	}
	s.signal(superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "wake-turn-2", Machine: "box", Session: sid,
		Detail: "synthetic ordinary idle", At: now})
	s.superviseTimes = superviseTimings{gather: 1, every: 1, urgent: 1}
	wakeRound(s)
	if got := idleDigestSends(f, "conv1"); len(got) != 1 || !strings.Contains(got[0], "use the safe option") {
		s.supervise.mu.Lock()
		merged := s.supervise.pending[todo.ID+"|"+sigIdle]
		s.supervise.mu.Unlock()
		t.Fatalf("an ordinary idle replaced the undelivered decision: sends=%q merged=%+v", got, merged)
	}
}

func TestMergeSignalsPreservesUndeliveredDecision(t *testing.T) {
	handoff := superviseSignal{Todo: "todo_1", Kind: sigIdle, Key: "undelivered-1", Handoff: true, Detail: "use the safe option"}
	ordinary := superviseSignal{Todo: "todo_1", Kind: sigIdle, Key: "wake-turn-2", Detail: "synthetic ordinary idle"}
	got := mergeSignals([]superviseSignal{handoff}, []superviseSignal{ordinary})
	if len(got) != 1 || !got[0].Handoff || !strings.Contains(got[0].Detail, handoff.Detail) {
		t.Fatalf("ordinary idle replaced handoff detail: %+v", got)
	}
}

func TestForgetSupervisionKeepsNewerSignalKey(t *testing.T) {
	s, _, todo := newSupervisedService(t)
	old := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "old-turn", Detail: "synthetic idle"}
	e, ok := s.digestEntry(context.Background(), old, testWeb)
	if !ok {
		t.Fatal("the idle does not render")
	}
	fingerprint, _ := digestFingerprint(e, old)
	s.supervise.dedup = map[string]superviseFingerprint{fingerprint: {Key: "new-turn"}}
	if err := s.forgetSupervision(context.Background(), []superviseSignal{old}, testWeb); err != nil {
		t.Fatal(err)
	}
	if got := s.supervise.dedup[fingerprint]; got.Key != "new-turn" {
		t.Fatalf("releasing an old idle deleted a newer signal's fingerprint: %+v", got)
	}
}

func TestUndeliveredDecisionQueuedDrainIgnoresFutureWake(t *testing.T) {
	ctx := context.Background()
	s, f, todo := newSupervisedService(t)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	now := time.Now()
	sid := todo.Agents[0].SessionID
	f.setSessions("box", proto.Session{SessionID: sid, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
		State: proto.StateNeedsYou, PendingInteractions: 1, LastText: "synthetic question", UpdatedAt: now.Add(-time.Minute),
		Loop: &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}})
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking})
	s.decisionNotDelivered(ctx, todo.ID, now.Add(-time.Minute), "use the safe option", errors.New("synthetic send failure"))
	s.supervise.mu.Lock()
	var handoff superviseSignal
	for _, g := range s.supervise.pending {
		if g.Todo == todo.ID && g.Kind == sigIdle {
			handoff = g
		}
	}
	s.supervise.mu.Unlock()
	if handoff.Todo == "" {
		t.Fatal("the failed decision did not queue its handoff")
	}
	s.supervise.dedupMu.Lock()
	accepted := s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{handoff}, s.webBase(ctx), now, time.Time{})
	s.supervise.dedupMu.Unlock()
	if !accepted || s.queuedFor("conv1") != 1 {
		t.Fatalf("the handoff was not admitted to the real queue: accepted=%v queue=%d", accepted, s.queuedFor("conv1"))
	}
	drainQueuedDigest(t, s, f)
	if got := idleDigestSends(f, "conv1"); len(got) != 1 || !strings.Contains(got[0], "use the safe option") {
		t.Fatalf("queued handoff was suppressed by the future wakeup: sends=%q queue=%d", got, s.queuedFor("conv1"))
	}
}

func TestUndeliveredDecisionIdleRestartIgnoresFutureWake(t *testing.T) {
	ctx := context.Background()
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	// The handoff skips the gather (#17099), so the hour above does not keep it until the simulated restart: hold the
	// digest timer, as if the manager stopped before its digest ran. The explicit wakeRound below still delivers.
	s.stopSuperviseTimers()
	now := time.Now()
	sid := todo.Agents[0].SessionID
	f.setSessions("box", proto.Session{SessionID: sid, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
		State: proto.StateNeedsYou, PendingInteractions: 1, LastText: "synthetic question", UpdatedAt: now.Add(-time.Minute),
		Loop: &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}})
	turns := &s.tasks.turns
	turns.mu.Lock()
	if !s.turnsLocked(ctx) {
		turns.mu.Unlock()
		t.Fatal("cannot load the turn ledger")
	}
	turns.doc.Turns[todo.ID] = taskTurn{Machine: "box", Session: sid, Key: "decision-turn", State: turnDone, Seen: now}
	turns.mu.Unlock()
	askedAt := now.Add(-time.Minute)
	s.decisionNotDelivered(ctx, todo.ID, askedAt, "use the safe option", errors.New("synthetic send failure"))
	id := todo.ID + "|" + sigIdle
	s.supervise.mu.Lock()
	handoff := s.supervise.pending[id]
	s.supervise.mu.Unlock()
	if handoff.Key == "" || storedSupervisePending(t, s)[id].Key != handoff.Key {
		t.Fatalf("the handoff was not durable before restart: memory=%+v stored=%+v", handoff, storedSupervisePending(t, s))
	}
	simulateSupervisionRestart(t, s)
	s.restoreSupervisePending(ctx)
	s.supervise.mu.Lock()
	restored := s.supervise.pending[id]
	s.supervise.mu.Unlock()
	if restored.Key != handoff.Key || !restored.Handoff {
		t.Fatalf("the real outbox reload lost the handoff: restored=%+v stored=%+v", restored, storedSupervisePending(t, s))
	}
	s.superviseTimes = superviseTimings{gather: 1, every: 1, urgent: 1}
	logs := captureWakeLogs(s)
	wakeRound(s)
	if got := idleDigestSends(f, "conv1"); len(got) != 1 || !strings.Contains(got[0], "use the safe option") {
		s.supervise.mu.Lock()
		pending := s.supervise.pending[id]
		last := s.supervise.last
		s.supervise.mu.Unlock()
		t.Fatalf("restarted handoff was not delivered: sends=%q restored=%+v pending=%+v last=%v stored=%+v logs=%q", got, restored, pending, last, storedSupervisePending(t, s), logs())
	}
}

func TestAutomaticReviewUnsentReworkIdleIgnoresFutureWake(t *testing.T) {
	s, f, todo, n := supervisedReview(t)
	a := autoReviewOf(s, todo.ID)
	now := time.Now()
	sid := todo.Agents[0].SessionID
	f.setSessions("box",
		proto.Session{SessionID: sid, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
			State: proto.StateNeedsYou, PendingInteractions: 1, LastText: "synthetic review question", UpdatedAt: now.Add(-time.Minute),
			Loop: &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}},
		proto.Session{SessionID: a.SessionID, MachineID: "box", Cwd: reviewCopy, Agent: "claude", Status: proto.StatusDone, UpdatedAt: now})
	f.mu.Lock()
	f.failSend = errors.New("synthetic rework delivery failure")
	f.mu.Unlock()
	n.answer(f, a.SessionID, "1. P0 login/view.swift:11 fix the blocking regression\n<review-findings count=\"1\" blocking=\"1\"/>")
	s.autoReviewTurnEnded(context.Background(), "box", a.SessionID)
	waitFor(t, "the unsent automatic-review rework to reach its supervisor despite the future wakeup", func() bool {
		return len(idleDigestSends(f, "conv1")) == 1
	})
}

func TestFutureNativeWakeFailOpenBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*Todo, *proto.Session, *proto.Session, time.Time)
	}{
		{name: "another current agent has no wakeup", setup: func(todo *Todo, c, other *proto.Session, now time.Time) {
			c.TurnError = "synthetic turn failure"
			c.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}
			todo.Agents = append(todo.Agents, TodoAgent{MachineID: other.MachineID, SessionID: other.SessionID, Cwd: other.Cwd, Source: "manual"})
		}},
		{name: "offline agent", setup: func(_ *Todo, c, _ *proto.Session, now time.Time) {
			c.Status = proto.StatusOffline
			c.State = proto.StateNeedsYou
			c.PendingInteractions = 1
			c.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}
		}},
		{name: "read only agent", setup: func(_ *Todo, c, _ *proto.Session, now time.Time) {
			c.State = proto.StateNeedsYou
			c.PendingInteractions = 1
			c.ReadOnlyReason = "synthetic read-only session"
			c.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}
		}},
		{name: "queued input", setup: func(_ *Todo, c, _ *proto.Session, now time.Time) {
			c.Queued = []string{"synthetic queued input"}
			c.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}
		}},
		{name: "queued input and turn error", setup: func(_ *Todo, c, _ *proto.Session, now time.Time) {
			c.Queued = []string{"synthetic queued input"}
			c.TurnError = "synthetic turn failure"
			c.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}
		}},
		{name: "queued input and needs you", setup: func(_ *Todo, c, _ *proto.Session, now time.Time) {
			c.Queued = []string{"synthetic queued input"}
			c.State = proto.StateNeedsYou
			c.PendingInteractions = 1
			c.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo, c, now := mechanicalFixture(t)
			todo.Delivery = nil
			other := c
			other.SessionID = "mech-agent-2"
			tc.setup(&todo, &c, &other, now)
			f.setSessions("box", c, other)
			s.todos.mu.Lock()
			copy := copyTodoLocked(&todo)
			s.todos.todos[todo.ID] = &copy
			s.todos.mu.Unlock()
			g := superviseSignal{Todo: todo.ID, Kind: sigIdle, Machine: c.MachineID, Session: c.SessionID, Key: "turn-1"}
			handled, retry := s.mechanicalSignal(context.Background(), g, now)
			if handled || !retry.IsZero() {
				t.Fatalf("a future wakeup suppressed a fail-open idle: handled=%v retry=%v", handled, retry)
			}
		})
	}
}

func TestLeaderAgentFinalWakeScreen(t *testing.T) {
	s, f := newQuietService(t)
	startTaskLeader(t, s, f)
	worker := planWorker(t, s, f, nil)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	now := time.Now()
	sid := worker.Agents[0].SessionID
	leader := proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle, UpdatedAt: now}
	agent := proto.Session{SessionID: sid, MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
		State: proto.StateUnread, TurnError: "synthetic turn failure", LastText: "synthetic worker reply", UpdatedAt: now.Add(-time.Minute)}
	f.setSessions("box", leader, agent)
	first := true
	f.mu.Lock()
	f.onList = func(f *fakeHub, machine string) {
		if machine != "box" {
			return
		}
		if first {
			first = false
			return
		}
		rows := append([]proto.Session(nil), f.sessions["box"]...)
		for i := range rows {
			if rows[i].SessionID == sid {
				rows[i].Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}
			}
		}
		f.sessions["box"] = rows
	}
	f.mu.Unlock()
	idle := superviseSignal{Todo: worker.ID, Kind: sigIdle, Key: "leader-turn-wake", Machine: "box", Session: sid,
		Detail: "synthetic worker idle", At: now.Add(-time.Minute)}
	s.superviseTimes = superviseTimings{}
	wakeRound(s, idle)
	f.mu.Lock()
	f.onList = nil
	f.mu.Unlock()
	if got := idleWakeSends(f, "box", "s1", idle.Detail); len(got) != 0 {
		t.Fatalf("the leader-agent final boundary released an idle beside a future wakeup: %q", got)
	}
	s.supervise.mu.Lock()
	_, pending := s.supervise.pending[idle.id()]
	s.supervise.mu.Unlock()
	if !pending {
		t.Fatal("the deferred leader idle was not restored")
	}

	agent.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(-time.Minute)}
	f.setSessions("box", leader, agent)
	wakeRound(s)
	if got := idleWakeSends(f, "box", "s1", idle.Detail); len(got) != 1 || !strings.Contains(got[0], idle.Detail) {
		t.Fatalf("the leader did not receive the idle after its wakeup expired: %q", got)
	}
}

func TestWakeWithholdingLogsBoundaryWakeAndConflict(t *testing.T) {
	for _, tc := range []struct {
		name     string
		boundary string
		conflict string
		final    bool
		mutate   func(*proto.Session, time.Time)
	}{
		{name: "first turn error", boundary: "first", conflict: "turn_error", mutate: func(c *proto.Session, _ time.Time) {
			c.TurnError = "synthetic turn failure"
		}},
		{name: "final needs you", boundary: "final", conflict: "needs_you", final: true, mutate: func(c *proto.Session, _ time.Time) {
			c.State = proto.StateNeedsYou
			c.PendingInteractions = 1
		}},
		{name: "final clean stop", boundary: "final", conflict: "none", final: true, mutate: func(_ *proto.Session, _ time.Time) {}},
		{name: "first running wakeup", boundary: "first", conflict: "none", mutate: func(c *proto.Session, _ time.Time) {
			c.Status, c.State = proto.StatusWorking, proto.StateWorking
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo, c, now := mechanicalFixture(t)
			s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
			todo.Delivery = nil
			next := now.Add(time.Hour)
			tc.mutate(&c, now)
			c.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: next}
			f.setSessions("box", c)
			s.todos.mu.Lock()
			copy := copyTodoLocked(&todo)
			s.todos.todos[todo.ID] = &copy
			s.todos.mu.Unlock()
			s.supervise.mu.Lock()
			s.supervise.init()
			s.supervise.mu.Unlock()
			logs := captureWakeLogs(s)
			idle := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "turn-1", Machine: c.MachineID, Session: c.SessionID}
			var kept []superviseSignal
			if tc.final {
				kept = s.screenFinalWakeSignals(context.Background(), []superviseSignal{idle}, now, now.Add(-time.Minute))
			} else {
				kept = s.screenMechanicalSignals(context.Background(), []superviseSignal{idle}, now, now.Add(-time.Minute))
			}
			if len(kept) != 0 {
				t.Fatalf("the fixture did not withhold its idle: %+v", kept)
			}
			var matching []string
			for _, line := range logs() {
				if strings.Contains(line, "supervise: idle withheld") {
					matching = append(matching, line)
				}
			}
			if len(matching) != 1 {
				t.Fatalf("withheld idle log lines = %q, all logs = %q", matching, logs())
			}
			for _, want := range []string{"boundary=" + tc.boundary, "wake=" + next.Format(time.RFC3339Nano), "conflict=" + tc.conflict} {
				if !strings.Contains(matching[0], want) {
					t.Errorf("withholding log lacks %q: %s", want, matching[0])
				}
			}
		})
	}
}
