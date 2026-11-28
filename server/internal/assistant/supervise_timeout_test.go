package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// queuedSideOrphan admits an orphan digest to a working side supervisor. The caller can then make that conversation
// disappear and age the durable queue without replacing the production queue-admission path with a test fixture.
func queuedSideOrphan(t *testing.T) (*Service, *fakeHub, Todo) {
	t.Helper()
	return queuedSideOrphanOn(t, nil)
}

func queuedSideOrphanOn(t *testing.T, st Store) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f := newQuietServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	f.mu.Lock()
	f.known["conv2"] = true
	f.mu.Unlock()
	main := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	side := proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusWorking}
	f.setSessions("server", main, side)
	task, err := s.createTodo(ctx, "Worker disappeared", "Verify supervisor escalation", "conv2", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, task.ID, "conv2", []string{"Verified"}, 0); err != nil {
		t.Fatal(err)
	}
	s.orphans.mu.Lock()
	if s.orphans.now == nil {
		s.orphans.now = map[string]TodoOrphan{}
	}
	s.orphans.now[task.ID] = TodoOrphan{Reason: orphanClosed, key: "worker-gone"}
	s.orphans.mu.Unlock()
	g := superviseSignal{Todo: task.ID, Kind: sigOrphaned, Key: "worker-gone", Orphan: orphanClosed, Detail: "worker closed", At: time.Now()}
	s.updateSupervisePending(func(pending map[string]superviseSignal) { pending[g.id()] = g })
	if !s.deliverDigest(ctx, s.settings(ctx), "conv2", []superviseSignal{g}, "", time.Now(), time.Time{}) {
		t.Fatal("orphan digest was not admitted to the side supervisor's queue")
	}
	if s.queuedFor("conv2") != 1 {
		t.Fatal("expected one durable side-supervisor digest")
	}
	return s, f, todoNow(s, task.ID)
}

func ageQueuedSideDigest(t *testing.T, s *Service, age time.Duration) {
	t.Helper()
	s.relays.mu.Lock()
	defer s.relays.mu.Unlock()
	q := s.relays.st.Queue["conv2"]
	if len(q) != 1 {
		t.Fatalf("side queue has %d messages", len(q))
	}
	q[0].QueuedAt = time.Now().Add(-age).UTC()
	s.relays.st.Queue["conv2"] = q
	if err := s.saveRelayLocked(); err != nil {
		t.Fatal(err)
	}
}

// A physically missing supervisor never appears in the session list that drives ordinary queue drains. The periodic
// held-relay recovery must therefore recheck its aged queue explicitly, escalate the still-valid orphan to main as an
// unreachable-supervisor decision, and leave task ownership unchanged.
func TestMissingSupervisorEscalatesQueuedOrphanAfterTimeout(t *testing.T) {
	s, f, todo := queuedSideOrphan(t)
	ageQueuedSideDigest(t, s, 31*time.Minute)
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})

	s.drainHeldRelays(context.Background(), time.Now())

	if s.queuedFor("conv1") == 0 && len(f.notices()) == 0 {
		t.Fatalf("missing supervisor kept the only copy: side=%d main=%d notices=%q", s.queuedFor("conv2"), s.queuedFor("conv1"), f.notices())
	}
	if s.queuedFor("conv1") > 0 {
		drainQueuedDigest(t, s, f)
	}
	notices := f.notices()
	if len(notices) != 1 || !strings.Contains(notices[0], todo.ID) || !strings.Contains(notices[0], "signal=decide") ||
		!strings.Contains(notices[0], "orphaned") || !strings.Contains(notices[0], "主管对话") {
		t.Fatalf("main did not get the supervisor-unreachable orphan decision: %q", notices)
	}
	if s.queuedFor("conv2") != 0 || len(f.noticesTo("conv2")) != 0 {
		t.Fatal("the original supervisor retained or received a duplicate")
	}
	if got := todoNow(s, todo.ID).Supervision.Supervisor; got != "conv2" {
		t.Fatalf("notification escalation changed task ownership to %q", got)
	}
}

func TestSupervisorTimeoutKeepsFreshWorkingUnknownAndNoMainQueues(t *testing.T) {
	for _, tc := range []struct {
		name    string
		age     time.Duration
		working bool
		unknown bool
		noMain  bool
	}{
		{name: "fresh queue", age: time.Minute},
		{name: "working supervisor", age: 31 * time.Minute, working: true},
		{name: "unknown session inventory", age: 31 * time.Minute, unknown: true},
		{name: "no main conversation", age: 31 * time.Minute, noMain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _ := queuedSideOrphan(t)
			ageQueuedSideDigest(t, s, tc.age)
			main := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
			list := []proto.Session{main}
			if tc.working {
				list = append(list, proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusWorking})
			}
			f.setSessions("server", list...)
			if tc.unknown {
				f.answerNode(DeviceSessionsList, func(_ string, _ json.RawMessage) (any, error) {
					return nil, errors.New("synthetic unavailable session inventory")
				})
			}
			if tc.noMain {
				setSettings(t, s, func(v *Settings) { v.Main = "" })
			}

			s.drainHeldRelays(context.Background(), time.Now())

			if s.queuedFor("conv2") != 1 || s.queuedFor("conv1") != 0 || len(f.notices()) != 0 {
				t.Fatalf("queue changed without a proven timeout: side=%d main=%d notices=%q", s.queuedFor("conv2"), s.queuedFor("conv1"), f.notices())
			}
		})
	}
}

func TestSupervisorTimeoutLeavesUnresolvedDeliveries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*relayMsg)
	}{
		{name: "tried", mutate: func(m *relayMsg) { m.TriedAt = time.Now().Add(-time.Minute) }},
		{name: "doubted", mutate: func(m *relayMsg) { m.DoubtedAt = time.Now().Add(-time.Minute) }},
		{name: "withdrawn", mutate: func(m *relayMsg) { m.Withdrawn = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _ := queuedSideOrphan(t)
			ageQueuedSideDigest(t, s, 31*time.Minute)
			s.relays.mu.Lock()
			before := s.relays.st.Queue["conv2"][0]
			tc.mutate(&before)
			s.relays.st.Queue["conv2"][0] = before
			if err := s.saveRelayLocked(); err != nil {
				s.relays.mu.Unlock()
				t.Fatal(err)
			}
			s.relays.mu.Unlock()
			f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})

			s.drainHeldRelays(context.Background(), time.Now())

			s.relays.mu.Lock()
			queue := s.relays.st.Queue["conv2"]
			unchanged := len(queue) == 1 && queue[0] == before
			s.relays.mu.Unlock()
			if !unchanged || s.queuedFor("conv1") != 0 || len(f.notices()) != 0 {
				t.Fatalf("unresolved delivery was rerouted: side=%+v main=%d notices=%q", queue, s.queuedFor("conv1"), f.notices())
			}
		})
	}
}

func TestSupervisorTimeoutMovesOnlyAgedStructuredMessage(t *testing.T) {
	s, f, _ := queuedSideOrphan(t)
	ageQueuedSideDigest(t, s, 31*time.Minute)
	freshSignals := []superviseSignal{{Todo: onlyTodo(s).ID, Kind: sigInterrupted, Key: "fresh", Detail: "fresh interruption", At: time.Now()}}
	if _, err := s.queueRelayDurable("conv2", relayMsg{From: "supervise", Tool: superviseTool, Text: "fresh", Signals: encodeDigestSignals(freshSignals), At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// Put an unrelated relay ahead of the aged digest: timeout recovery must not require it to be the queue head.
	s.relays.mu.Lock()
	prefix := relayMsg{From: "owner", Tool: askAnswerTool, Text: "keep this answer", At: time.Now(), QueuedAt: time.Now(), Owner: true}
	s.relays.st.Queue["conv2"] = append([]relayMsg{prefix}, s.relays.st.Queue["conv2"]...)
	if err := s.saveRelayLocked(); err != nil {
		s.relays.mu.Unlock()
		t.Fatal(err)
	}
	s.relays.mu.Unlock()
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})

	s.drainHeldRelays(context.Background(), time.Now())
	if s.queuedFor("conv1") > 0 {
		drainQueuedDigest(t, s, f)
	}

	s.relays.mu.Lock()
	side := append([]relayMsg(nil), s.relays.st.Queue["conv2"]...)
	s.relays.mu.Unlock()
	if len(side) != 2 || side[0] != prefix || side[1].Text != "fresh" {
		t.Fatalf("unrelated queue entries changed: %+v", side)
	}
	if got := strings.Join(f.notices(), "\n"); !strings.Contains(got, "orphaned") || strings.Contains(got, "fresh interruption") {
		t.Fatalf("wrong digest escalated: %q", got)
	}
}

func TestSupervisorTimeoutLeavesInformationalSignalsWithSupervisor(t *testing.T) {
	s, f, todo := queuedSideOrphan(t)
	ageQueuedSideDigest(t, s, 31*time.Minute)
	progress := superviseSignal{Todo: todo.ID, Kind: sigReport, Key: "progress", Report: upReportProgress, Detail: "informational progress", At: time.Now()}
	s.relays.mu.Lock()
	msg := s.relays.st.Queue["conv2"][0]
	signals, _ := decodeDigestSignals(msg.Signals)
	msg.Signals = encodeDigestSignals(append(signals, progress))
	s.relays.st.Queue["conv2"][0] = msg
	if err := s.saveRelayLocked(); err != nil {
		s.relays.mu.Unlock()
		t.Fatal(err)
	}
	s.relays.mu.Unlock()
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})

	s.drainHeldRelays(context.Background(), time.Now())
	if s.queuedFor("conv1") > 0 {
		drainQueuedDigest(t, s, f)
	}

	if s.queuedFor("conv2") != 1 {
		t.Fatal("the informational signal did not stay with its assigned supervisor")
	}
	s.relays.mu.Lock()
	kept, _ := decodeDigestSignals(s.relays.st.Queue["conv2"][0].Signals)
	s.relays.mu.Unlock()
	if len(kept) != 1 || kept[0].Kind != sigReport || kept[0].Report != upReportProgress {
		t.Fatalf("remaining source signals = %+v", kept)
	}
	if got := strings.Join(f.notices(), "\n"); !strings.Contains(got, "orphaned") || strings.Contains(got, "informational progress") {
		t.Fatalf("informational signal leaked into main's decision: %q", got)
	}
	// Once the actionable member has moved, the aged informational remainder is not repeatedly rewritten or escalated.
	s.drainHeldRelays(context.Background(), time.Now())
	if s.queuedFor("conv2") != 1 {
		t.Fatal("informational-only remainder became a timeout candidate")
	}
}

func TestSupervisorTimeoutEscalationSurvivesRestartExactlyOnce(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, newMigratedDatabase(t, db.AllSets), db.AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, f, todo := queuedSideOrphanOn(t, database)
	ageQueuedSideDigest(t, s, 31*time.Minute)
	// Keep main busy so the test can restart between the atomic queue move and its first possible delivery.
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking})
	s.drainHeldRelays(ctx, time.Now())
	if s.queuedFor("conv2") != 0 || s.queuedFor("conv1") != 1 || len(f.notices()) != 0 {
		t.Fatalf("move was not held for restart: side=%d main=%d notices=%q", s.queuedFor("conv2"), s.queuedFor("conv1"), f.notices())
	}
	var saved relayState
	if _, err := database.Setting(ctx, relayKey, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Queue["conv2"]) != 0 || len(saved.Queue["conv1"]) != 1 {
		t.Fatalf("durable queues after move: %+v", saved.Queue)
	}
	signals, structured := decodeDigestSignals(saved.Queue["conv1"][0].Signals)
	if !structured || len(signals) != 1 || signals[0].Kind != sigDecide || !signals[0].Unreachable || signals[0].Recipient != "conv2" {
		t.Fatalf("persisted escalation = %+v", signals)
	}

	again, hub := newQuietServiceOn(t, database)
	again.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	drainQueuedDigest(t, again, hub)
	drainQueuedDigest(t, again, hub)
	if len(hub.notices()) != 1 || !strings.Contains(hub.notices()[0], todo.ID) || !strings.Contains(hub.notices()[0], "signal=decide") {
		t.Fatalf("restart delivery was missing or duplicated: %q", hub.notices())
	}
	if again.queuedFor("conv2") != 0 || again.queuedFor("conv1") != 0 {
		t.Fatal("delivered escalation remains queued")
	}
	saved = relayState{} // an omitted empty queue must not leave the previous decode's map in this struct
	if _, err := database.Setting(ctx, relayKey, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Queue["conv2"]) != 0 || len(saved.Queue["conv1"]) != 0 {
		t.Fatalf("delivered escalation persisted: %+v", saved.Queue)
	}
}

func TestSupervisorTimeoutKeepsSourceWhenCheckpointFails(t *testing.T) {
	s, f, _ := queuedSideOrphan(t)
	ageQueuedSideDigest(t, s, 31*time.Minute)
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	oldStore := s.storeAccessor()
	if oldStore == nil {
		t.Fatal("queuedSideOrphan has no store accessor")
	}
	t.Cleanup(func() { s.setStore(oldStore) })
	failed := &memStore{}
	if err := failed.PutSetting(context.Background(), settingsKey, s.settings(context.Background())); err != nil {
		t.Fatal(err)
	}
	failed.failPut = errors.New("synthetic checkpoint failure")
	s.setStore(func() Store { return failed })
	s.drainHeldRelays(context.Background(), time.Now())
	if s.queuedFor("conv2") != 1 || s.queuedFor("conv1") != 0 || len(f.notices()) != 0 {
		t.Fatal("a failed atomic move lost the source or exposed a destination copy")
	}
	s.setStore(oldStore)
	s.drainHeldRelays(context.Background(), time.Now())
	if s.queuedFor("conv2") != 0 {
		t.Fatalf("the durable source did not recover after the checkpoint failure: side=%d main=%d notices=%q", s.queuedFor("conv2"), s.queuedFor("conv1"), f.notices())
	}
	if s.queuedFor("conv1") > 0 {
		drainQueuedDigest(t, s, f)
	}
	if len(f.notices()) != 1 {
		t.Fatalf("recovered escalation was not delivered exactly once: %q", f.notices())
	}
}
