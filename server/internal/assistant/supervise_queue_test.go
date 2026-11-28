package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func queuedDigestFixture(t *testing.T, titles ...string) (*Service, *fakeHub, []Todo) {
	t.Helper()
	s, f := newQuietService(t)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking})
	var tasks []Todo
	var signals []superviseSignal
	for _, title := range titles {
		task, err := s.createTodo(context.Background(), title, "Check the example", "conv1", "")
		if err != nil {
			t.Fatal(err)
		}
		s.superviseTodo(context.Background(), task.ID, "conv1", []string{"Verified"}, 0)
		tasks = append(tasks, task)
		signals = append(signals, superviseSignal{Todo: task.ID, Kind: sigInterrupted, Key: "interrupted", Detail: "Capacity unavailable"}, superviseSignal{Todo: task.ID, Kind: sigUnjudged, Key: "unjudged", Detail: "Needs review"})
	}
	if !s.deliverDigest(context.Background(), s.settings(context.Background()), "conv1", signals, "", time.Now(), time.Time{}) {
		t.Fatal("digest not queued")
	}
	if s.queuedFor("conv1") != 1 {
		t.Fatal("expected one digest")
	}
	waitFor(t, "the backfill's read of the to-dos", s.spaceBackfillScanIdle)
	return s, f, tasks
}

func TestQueuedRoutineSignalsStayQuietWhenTaskWaitsForUser(t *testing.T) {
	s, f, tasks := queuedDigestFixture(t, "Waiting for owner", "Still active")
	if _, _, err := s.updateSupervision(t.Context(), tasks[0].ID, func(v *Supervision) bool {
		v.State = reviewNeedsUser
		return true
	}); err != nil {
		t.Fatal(err)
	}
	drainQueuedDigest(t, s, f)
	got := strings.Join(f.noticesTo("conv1"), "\n")
	if strings.Contains(got, "Waiting for owner") || !strings.Contains(got, "Still active") {
		t.Fatalf("queued digest did not follow the current task states: %s", got)
	}
}

func drainQueuedDigest(t *testing.T, s *Service, f *fakeHub) {
	t.Helper()
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	f.setSessions("server", idle)
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "drain completed", func() bool {
		s.relays.mu.Lock()
		defer s.relays.mu.Unlock()
		return !s.relays.draining["conv1"]
	})
}

func TestQueuedDigestDropsAcceptedTasks(t *testing.T) {
	s, f, tasks := queuedDigestFixture(t, "Accepted example")
	closeTodo(t, s, tasks[0].ID)
	drainQueuedDigest(t, s, f)
	if len(f.notices()) != 0 || s.queuedFor("conv1") != 0 {
		t.Fatalf("obsolete digest sent or retained: sends=%q queue=%d", f.notices(), s.queuedFor("conv1"))
	}
}

func TestQueuedDigestOnlySendsActionableTasks(t *testing.T) {
	s, f, tasks := queuedDigestFixture(t, "Accepted example", "Actionable example")
	closeTodo(t, s, tasks[0].ID)
	drainQueuedDigest(t, s, f)
	notices := f.notices()
	if len(notices) != 1 || strings.Contains(notices[0], "Accepted example") || !strings.Contains(notices[0], "Actionable example") {
		t.Fatalf("mixed digest: %q", notices)
	}
	if s.queuedFor("conv1") != 0 {
		t.Fatal("delivered digest remains queued")
	}
}

func TestQueuedDigestRechecksAfterRestart(t *testing.T) {
	s, f, tasks := queuedDigestFixture(t, "Accepted example")
	closeTodo(t, s, tasks[0].ID)
	s.relays.mu.Lock()
	raw, err := json.Marshal(s.relays.st)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &s.relays.st); err != nil {
		t.Fatal(err)
	}
	s.relays.mu.Unlock()
	s.supervise.mu.Lock()
	s.supervise.queued = nil
	s.supervise.mu.Unlock()
	drainQueuedDigest(t, s, f)
	if len(f.notices()) != 0 || s.queuedFor("conv1") != 0 {
		t.Fatalf("reloaded obsolete digest: %q", f.notices())
	}
}

func TestQueuedDigestUsesCurrentSupervisor(t *testing.T) {
	s, f, tasks := queuedDigestFixture(t, "Reassigned example")
	s.todos.mu.Lock()
	s.todos.todos[tasks[0].ID].Supervision.Supervisor = "conv2"
	s.todos.mu.Unlock()
	drainQueuedDigest(t, s, f)
	if len(f.notices()) != 0 || s.queuedFor("conv1") != 0 || s.queuedFor("conv2") != 1 {
		t.Fatalf("wrong routing: sends=%q old=%d new=%d", f.notices(), s.queuedFor("conv1"), s.queuedFor("conv2"))
	}
	idle := proto.Session{SessionID: "conv2", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	f.setSessions("server", idle)
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "new supervisor received digest", func() bool { return s.queuedFor("conv2") == 0 })
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sends) != 1 || f.sends[0]["session_id"] != "conv2" {
		t.Fatalf("send targets: %+v", f.sends)
	}
}

// A failing read is unknown, not evidence that the pending tasks disappeared.
type unreadableDigestTasks struct {
	Store
	todoStore
}

func (unreadableDigestTasks) ListTodos(context.Context) ([]db.TodoRow, error) {
	return nil, errors.New("synthetic unavailable tasks")
}

// Setting fails too: the settings in effect are the last good ones (settingsLastGood), as with any unreadable store. The
// drain reads them for the switch for automatic supervision messages.
func (unreadableDigestTasks) Setting(context.Context, string, any) (bool, error) {
	return false, errors.New("synthetic unavailable settings")
}

func TestQueuedDigestKeepsUnreadableTasks(t *testing.T) {
	s, f, _ := queuedDigestFixture(t, "Recoverable example")
	oldStore := s.storeAccessor()
	s.setStore(func() Store { return unreadableDigestTasks{} })
	s.todos.mu.Lock()
	s.todos.loaded = false
	s.todos.mu.Unlock()
	// The relay book is already loaded; the task read is the only failed dependency.
	drainQueuedDigest(t, s, f)
	if len(f.notices()) != 0 || s.queuedFor("conv1") != 1 {
		t.Fatal("unknown task state sent or dropped")
	}
	s.setStore(oldStore)
	s.todos.mu.Lock()
	s.todos.loaded = true
	s.todos.mu.Unlock()
	drainQueuedDigest(t, s, f)
	if len(f.notices()) != 1 || s.queuedFor("conv1") != 0 {
		t.Fatal("pending work did not recover")
	}
}

func TestQueuedDigestKeepsFailedCheckpoint(t *testing.T) {
	s, f, tasks := queuedDigestFixture(t, "Accepted example", "Recoverable example")
	closeTodo(t, s, tasks[0].ID)
	st := &memStore{failPut: errors.New("synthetic checkpoint failure")}
	oldStore := s.storeAccessor()
	s.setStore(func() Store { return st })
	drainQueuedDigest(t, s, f)
	if len(f.notices()) != 0 || s.queuedFor("conv1") != 1 {
		t.Fatal("failed checkpoint sent or lost work")
	}
	s.setStore(oldStore)
	drainQueuedDigest(t, s, f)
	if len(f.notices()) != 1 || strings.Contains(f.notices()[0], "Accepted example") {
		t.Fatal("checkpoint recovery failed")
	}
}

func TestQueuedDigestKeepsFollowingOwnerMessage(t *testing.T) {
	s, f, tasks := queuedDigestFixture(t, "Accepted example")
	closeTodo(t, s, tasks[0].ID)
	s.queueRelay("conv1", relayMsg{From: "owner", Tool: askAnswerTool, Text: "Keep this answer", At: time.Now(), Owner: true})
	drainQueuedDigest(t, s, f)
	waitFor(t, "following owner message", func() bool { return len(f.notices()) == 1 })
	// The owner answer can rank ahead of the digest; drain its next idle turn too.
	drainQueuedDigest(t, s, f)
	if s.queuedFor("conv1") != 0 {
		t.Fatal("obsolete digest remains after next idle turn")
	}
	if !strings.Contains(f.notices()[0], "Keep this answer") || strings.Contains(f.notices()[0], "Accepted example") {
		t.Fatalf("following message: %q", f.notices())
	}
}

func TestQueuedDigestPersistsAcrossServiceRestart(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, newMigratedDatabase(t, db.AllSets), db.AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	s, f := newQuietServiceOn(t, database)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	busy := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking}
	f.setSessions("server", busy)
	task, err := s.createTodo(ctx, "Persisted example", "Verify restart", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, task.ID, "conv1", []string{"Verified"}, 0)
	signals := []superviseSignal{{Todo: task.ID, Kind: sigInterrupted, Key: "cut-off", Detail: "Capacity unavailable"}, {Todo: task.ID, Kind: sigUnjudged, Key: "review", Detail: "Needs review"}}
	s.updateSupervisePending(func(pending map[string]superviseSignal) {
		for _, g := range signals {
			pending[g.id()] = g
		}
	})
	if !s.deliverDigest(ctx, s.settings(ctx), "conv1", signals, "", time.Now(), time.Time{}) {
		t.Fatal("not queued")
	}
	done := true
	if _, err := database.UpdateTodo(ctx, task.ID, nil, nil, &done, time.Now()); err != nil {
		t.Fatal(err)
	}
	again, hub := newQuietServiceOn(t, database)
	again.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	drainQueuedDigest(t, again, hub)
	if len(hub.notices()) != 0 || again.queuedFor("conv1") != 0 {
		t.Fatal("persisted accepted task sent or retained")
	}
	var saved relayState
	if _, err := database.Setting(ctx, relayKey, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Queue["conv1"]) != 0 {
		t.Fatal("obsolete queue still on disk")
	}
}

func TestQueuedDigestUncertainDeliveryKeepsIdentity(t *testing.T) {
	for _, landed := range []bool{false, true} {
		t.Run(fmt.Sprint(landed), func(t *testing.T) {
			s, f, tasks := queuedDigestFixture(t, "Accepted example", "Actionable example")
			s.relays.mu.Lock()
			msg := s.relays.st.Queue["conv1"][0]
			msg.TriedAt = time.Now().Add(-time.Second)
			msg.Looks = relayLookLimit - 1
			s.relays.st.Queue["conv1"][0] = msg
			s.relays.mu.Unlock()
			if landed {
				f.mu.Lock()
				f.items = append(f.items, proto.Item{Role: "user", Kind: proto.KindText, Text: msg.Text, TS: time.Now()})
				f.mu.Unlock()
			}
			closeTodo(t, s, tasks[0].ID)
			drainQueuedDigest(t, s, f)
			if len(f.notices()) != 0 {
				t.Fatal("uncertain send repeated under a new identity")
			}
			if landed {
				if s.queuedFor("conv1") != 0 {
					t.Fatal("confirmed old delivery remains queued")
				}
			} else {
				s.relays.mu.Lock()
				defer s.relays.mu.Unlock()
				q := s.relays.st.Queue["conv1"]
				if len(q) != 1 || q[0].Looks != relayLookLimit || q[0].LookAfter.IsZero() {
					t.Fatalf("want an uncertain payload after the final check, got %+v", q)
				}
				got := q[0]
				got.Looks, got.LookAfter = msg.Looks, msg.LookAfter
				if got != msg {
					t.Fatal("uncertain payload changed")
				}
			}
		})
	}
}

func TestQueuedDigestDoesNotRememberDrainedSignals(t *testing.T) {
	s, f, _ := queuedDigestFixture(t, "Reviewed example")
	drainQueuedDigest(t, s, f)
	if len(s.queuedDigestSignals("conv1")) != 0 {
		t.Fatal("already delivered signals could be merged into a later digest")
	}
}

func TestLegacyQueuedDigestDoesNotHoldRelayWhileWaitingForSupervise(t *testing.T) {
	s, _ := newQuietService(t)
	legacy := superviseSignal{Todo: "legacy-todo", Kind: sigIdle, Key: "legacy-idle", Detail: "legacy text"}
	s.supervise.mu.Lock()
	s.supervise.queued = map[string][]superviseSignal{"conv1": {legacy}}
	s.supervise.mu.Unlock()
	s.relays.mu.Lock()
	s.relays.st.Queue = map[string][]relayMsg{"conv1": {{Tool: superviseTool, Text: "legacy digest"}}}
	s.relays.loaded = true
	s.relays.mu.Unlock()

	// The legacy fallback may wait for the supervision book, but it must not hold the relay lock meanwhile: a signal
	// holding that book needs the relay lock for its durable outbox write, so the reverse order deadlocks both paths.
	s.supervise.mu.Lock()
	result := make(chan []superviseSignal, 1)
	go func() { result <- s.queuedDigestSignals("conv1") }()
	waitForSuperviseMutex(t, "queuedDigestSignals", &s.supervise.mu)
	relayAvailable := make(chan struct{})
	go func() {
		s.relays.mu.Lock()
		s.relays.mu.Unlock()
		close(relayAvailable)
	}()
	available := false
	select {
	case <-relayAvailable:
		available = true
	case <-time.After(500 * time.Millisecond):
	}
	s.supervise.mu.Unlock()
	if !available {
		<-relayAvailable // let the deadlocked fixture unwind before failing the test
		<-result
		t.Fatal("legacy queued digest holds relays.mu while waiting for supervise.mu")
	}
	if got := <-result; len(got) != 1 || got[0].Key != legacy.Key {
		t.Fatalf("legacy queued signals = %+v, want %q", got, legacy.Key)
	}
}

func TestQueuedDigestRefreshKeepsPersistedDeliveryID(t *testing.T) {
	s, f, tasks := queuedDigestFixture(t, "Accepted example", "Actionable example")
	s.relays.mu.Lock()
	original := s.relays.st.Queue["conv1"][0]
	s.relays.mu.Unlock()
	closeTodo(t, s, tasks[0].ID)
	tries := relayTries(f, "conv1")
	f.mu.Lock()
	f.failAssistant = errors.New("synthetic definite refusal")
	f.mu.Unlock()
	drainQueuedDigest(t, s, f)
	s.relays.mu.Lock()
	raw, err := json.Marshal(s.relays.st)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &s.relays.st); err != nil {
		t.Fatal(err)
	}
	refreshed := s.relays.st.Queue["conv1"][0]
	s.relays.mu.Unlock()
	if refreshed.Text == original.Text || relayMsgID("conv1", refreshed) != relayMsgID("conv1", original) {
		t.Fatal("refresh changed the persisted once-only delivery identity")
	}
	if ids := triesOf(f, tries); len(ids) != 1 || ids[0] != relayMsgID("conv1", original) {
		t.Fatalf("delivery ids: %q", ids)
	}
}
