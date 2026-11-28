package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// storedSupervisePending reads the durable outbox without depending on its in-memory representation.
func storedSupervisePending(t *testing.T, s *Service) map[string]superviseSignal {
	t.Helper()
	var state struct {
		Pending map[string]superviseSignal `json:"supervise_pending"`
	}
	if _, err := s.store().Setting(context.Background(), relayKey, &state); err != nil {
		t.Fatal(err)
	}
	return state.Pending
}

// simulateSupervisionRestart clears memory only after digest and restoration can no longer use the old state.
// Replacing the whole book would replace its mutexes while timers still reference them.
func simulateSupervisionRestart(t *testing.T, s *Service) {
	t.Helper()
	b := &s.supervise
	b.digestMu.Lock()
	b.pendingLoadMu.Lock()
	b.dedupMu.Lock()
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timer, b.timerAt = nil, time.Time{}
	b.pending, b.pendingLoaded, b.sent, b.dedup = nil, false, nil, nil
	b.queued, b.last, b.asks = nil, time.Time{}, nil
	b.legacyBackground, b.failSeen, b.taken = nil, nil, nil // taken is never persisted (#17838)
	b.mu.Unlock()
	b.dedupMu.Unlock()
	b.pendingLoadMu.Unlock()
	b.digestMu.Unlock()
	s.relays.mu.Lock()
	s.relays.loaded = false
	s.relays.mu.Unlock()
}

// markLeaderTurnSignalled puts a terminated turn and its untold signal on the real turn ledger.
func markLeaderTurnSignalled(t *testing.T, s *Service, g superviseSignal) {
	t.Helper()
	last := proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusIdle, UpdatedAt: time.Now().UTC()}
	if _, again := s.takeTurn(context.Background(), Todo{ID: g.Todo}, last, nil, turnClose{Key: "turn-" + g.Key}, true, time.Now().Add(-time.Hour), turnDone); again {
		t.Fatal("the terminated test turn was already taken")
	}
	if s.turnSignalled(g) {
		t.Fatal("the test turn signal was told before delivery")
	}
}

// requireLeaderTurnToldAfterRestart drops the in-memory ledger and requires the persisted told receipt to deduplicate.
func requireLeaderTurnToldAfterRestart(t *testing.T, s *Service, g superviseSignal) {
	t.Helper()
	waitFor(t, "the leader delivery's told receipt", func() bool {
		e, ok := s.turnOf(context.Background(), g.Todo)
		return ok && e.Signal != nil && e.Signal.Told != nil
	})
	b := &s.tasks.turns
	b.mu.Lock()
	b.loaded, b.doc = false, taskTurnsDoc{}
	b.mu.Unlock()
	if _, ok := s.turnOf(context.Background(), g.Todo); !ok { // the sweep loads the ledger before asking turnSignalled
		t.Fatal("the persisted turn is missing after restart")
	}
	if !s.turnSignalled(g) {
		e, ok := s.turnOf(context.Background(), g.Todo)
		t.Fatalf("the %s success path did not record the turn as told after restart: ok %v, turn %+v, signal %+v, original %+v", g.Key, ok, e, e.Signal, g)
	}
}

// A needs-user state produces no new watch signal. Its gathered notification must therefore survive a restart before
// the first digest, reach main, and leave the durable outbox only after real delivery or queue admission (#5732).
func TestNeedsUserPendingSurvivesRestartBeforeDigest(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.State = reviewNeedsUser
		return true
	}); err != nil {
		t.Fatal(err)
	}
	work := time.Now()
	if !s.noteOwnerWait(ctx, todo, work, "选择发布方式") {
		t.Fatal("could not record the owner wait")
	}
	g := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: work.UTC().Format(time.RFC3339Nano)}
	if got := storedSupervisePending(t, s)[g.id()]; got.Key != g.Key {
		t.Fatalf("the gathered needs-user signal was not persisted: %+v", got)
	}

	// A process restart loses the in-memory book and relay's loaded mark, not the shared store.
	simulateSupervisionRestart(t, s)
	s.superviseTimes = superviseTimings{gather: 1, every: 1, urgent: 1} // due immediately for the post-restart digest
	s.sendDigest(ctx)
	if got := strings.Join(f.noticesTo("conv1"), "\n"); !strings.Contains(got, todo.ID) || !strings.Contains(got, "选择发布方式") {
		t.Fatalf("the restored needs-user signal was not delivered to main:\n%s", got)
	}
	if got := storedSupervisePending(t, s); len(got) != 0 {
		t.Fatalf("the delivered signal stayed in the durable outbox: %+v", got)
	}
}

// Resuming work retires the previous stretch's idle in memory and in the durable outbox. A genuinely newer idle after
// that resume remains renderable and survives its own restart (#5732).
func TestWorkingDropsStaleIdleAcrossRestart(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, _, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.State = reviewPending
		return true
	}); err != nil {
		t.Fatal(err)
	}
	old := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "old-stop", Detail: "old stretch stopped", At: time.Now()}
	s.signal(old)
	if got := storedSupervisePending(t, s)[old.id()]; got.Key != old.Key {
		t.Fatalf("the old idle was not persisted for the restart fixture: %+v", got)
	}
	if _, ok := s.digestEntry(ctx, old, testWeb); !ok {
		t.Fatal("the old idle does not render before work resumes")
	}
	pendingHas := func(g superviseSignal) bool {
		b := &s.supervise
		b.mu.Lock()
		defer b.mu.Unlock()
		_, ok := b.pending[g.id()]
		return ok
	}
	pendingText := func() string {
		b := &s.supervise
		b.mu.Lock()
		list := make([]superviseSignal, 0, len(b.pending))
		for _, g := range b.pending {
			list = append(list, g)
		}
		b.mu.Unlock()
		var entries []prompts.SuperviseEntry
		for _, g := range list {
			if e, ok := s.digestEntry(ctx, g, testWeb); ok {
				entries = append(entries, e)
			}
		}
		return prompts.RenderSupervise(prompts.Supervise{Entries: entries})
	}

	s.supervisedWorking(ctx, todoNow(s, todo.ID))
	simulateSupervisionRestart(t, s)
	s.restoreSupervisePending(ctx)
	if pendingHas(old) {
		t.Error("the previous stretch's idle was restored into memory after work resumed")
	}
	if got := storedSupervisePending(t, s)[old.id()]; got.Key == old.Key {
		t.Errorf("the previous stretch's idle stayed durable after work resumed: %+v", got)
	}
	if text := pendingText(); strings.Contains(text, old.Detail) {
		t.Errorf("the restored supervision message still contains the previous stretch's idle:\n%s", text)
	}

	newer := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "new-stop", Detail: "new stretch stopped", At: time.Now()}
	s.signal(newer)
	if got := storedSupervisePending(t, s)[newer.id()]; got.Key != newer.Key {
		t.Fatalf("the post-resume idle was not persisted: %+v", got)
	}
	if _, ok := s.digestEntry(ctx, newer, testWeb); !ok {
		t.Fatal("the post-resume idle does not render")
	}
	simulateSupervisionRestart(t, s)
	s.restoreSupervisePending(ctx)
	if !pendingHas(newer) {
		t.Error("the post-resume idle was not restored into memory")
	}
	if got := storedSupervisePending(t, s)[newer.id()]; got.Key != newer.Key {
		t.Errorf("the post-resume idle did not stay durable: %+v", got)
	}
	if text := pendingText(); !strings.Contains(text, newer.Detail) {
		t.Errorf("the restored supervision message lost the post-resume idle:\n%s", text)
	}
}

// Queue admission is another durable form of the old idle. Resuming work withdraws exactly that signal, regathers a
// mixed digest's other member, and leaves a genuinely newer post-resume idle deliverable (#5732).
func TestWorkingRetractsQueuedIdleAcrossRestart(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.State = reviewPending
		return true
	}); err != nil {
		t.Fatal(err)
	}
	old := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "queued-old-stop", Detail: "old queued idle", At: time.Now()}
	other := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "unrelated-stall", Detail: "unrelated stall", At: time.Now()}
	if !s.queueDigest("conv1", relayMsg{Tool: superviseTool, Text: old.Detail + "; " + other.Detail, At: time.Now()},
		[]superviseSignal{old, other}, time.Now(), time.Time{}) {
		t.Fatal("the old idle and its mixed-digest control were not admitted to the durable queue")
	}
	s.supervisedWorking(ctx, todoNow(s, todo.ID))
	simulateSupervisionRestart(t, s)
	for _, msg := range queuedRelays(s, "conv1") {
		if strings.Contains(msg.Text, old.Detail) {
			t.Errorf("the durable relay queue retained the previous stretch's idle: %+v", msg)
		}
	}
	idle := proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle}
	s.drainRelays("server", []proto.Session{idle})
	s.superviseTimes = superviseTimings{gather: 1, every: 1, urgent: 1}
	s.sendDigest(ctx) // the withdrawn head's other member is regathered into pending before it can go out again
	waitFor(t, "the mixed-digest control to drain", func() bool {
		return strings.Contains(strings.Join(f.noticesTo("conv1"), "\n"), other.Detail)
	})
	got := strings.Join(f.noticesTo("conv1"), "\n")
	if strings.Contains(got, old.Detail) {
		t.Errorf("the drained supervision message contains the previous stretch's idle:\n%s", got)
	}
	if !strings.Contains(got, other.Detail) {
		t.Errorf("withdrawing the old idle dropped the mixed digest's other member:\n%s", got)
	}

	newer := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "queued-new-stop", Detail: "new queued idle", At: time.Now()}
	if !s.queueDigest("conv1", relayMsg{Tool: superviseTool, Text: newer.Detail, At: time.Now()},
		[]superviseSignal{newer}, time.Now(), time.Time{}) {
		t.Fatal("the post-resume idle was not admitted to the durable queue")
	}
	s.drainRelays("server", []proto.Session{idle})
	waitFor(t, "the post-resume idle to drain", func() bool {
		return strings.Contains(strings.Join(f.noticesTo("conv1"), "\n"), newer.Detail)
	})
	if got := strings.Join(f.noticesTo("conv1"), "\n"); !strings.Contains(got, newer.Detail) {
		t.Errorf("the post-resume idle was not deliverable:\n%s", got)
	}
}

// A resume without a review or owner wait must retire the same durable idle forms. The early return in supervisedWorking
// used to leave the queued head and the turn-ledger signal untouched even though it removed the outbox entry (#5732).
func TestWorkingRetractsQueuedIdleWithoutOwnerWait(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, _, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	old := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "ordinary-old-stop", Detail: "ordinary old idle", At: time.Now()}
	other := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "ordinary-other", Detail: "ordinary other", At: time.Now()}
	markLeaderTurnSignalled(t, s, old)
	if !s.queueDigest("conv1", relayMsg{Tool: superviseTool, Text: old.Detail + "; " + other.Detail, At: time.Now()},
		[]superviseSignal{old, other}, time.Now(), time.Time{}) {
		t.Fatal("the ordinary idle and its mixed-digest control were not admitted to the durable queue")
	}
	resumed := todoNow(s, todo.ID)
	resumed.Supervision.State = ""
	resumed.Supervision.Waits = nil
	s.supervisedWorking(ctx, resumed)
	for _, msg := range queuedRelays(s, "conv1") {
		if strings.Contains(msg.Text, old.Detail) {
			t.Errorf("ordinary resumed work left the old idle in the durable relay queue: %+v", msg)
		}
	}
	if entry, ok := s.turnOf(ctx, todo.ID); !ok || entry.Signal == nil || entry.Signal.Told == nil {
		t.Errorf("ordinary resumed work did not persist the old idle's told receipt: %+v", entry)
	}
	if got := storedSupervisePending(t, s)[other.id()]; got.Key != other.Key {
		t.Errorf("ordinary idle retraction dropped the mixed-digest control: %+v", got)
	}
	fresh := old
	fresh.Key, fresh.Detail = "ordinary-new-stop", "ordinary new idle"
	s.signal(fresh)
	if got := storedSupervisePending(t, s)[fresh.id()]; got.Key != fresh.Key {
		t.Errorf("ordinary post-resume idle was incorrectly suppressed: %+v", got)
	}
}

// relayPutHook lets a test pause one durable relay write without a production-only hook.
type relayPutHook struct {
	Store
	hook func(relayState)
}

func (st *relayPutHook) PutSetting(ctx context.Context, key string, value any) error {
	if key == relayKey && st.hook != nil {
		st.hook(value.(relayState))
	}
	return st.Store.PutSetting(ctx, key, value)
}

// A newer idle admitted during the old one's final relay write must survive working's remaining cleanup (#5732).
func TestWorkingKeepsIdleAdmittedDuringOldIdleRetraction(t *testing.T) {
	st := &relayPutHook{Store: planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}}
	s, _, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	old := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "retraction-race-old", Detail: "old idle", At: time.Now()}
	s.signal(old)
	resumed := todoNow(s, todo.ID)
	resumed.Supervision.State, resumed.Supervision.Waits = "", nil
	reached, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	st.hook = func(state relayState) {
		if current, exists := state.SupervisePending[old.id()]; !exists || current.Key != old.Key {
			once.Do(func() { close(reached); <-release })
		}
	}
	workingDone := make(chan struct{})
	go func() { s.supervisedWorking(ctx, resumed); close(workingDone) }()
	<-reached // the old exact-key retraction now holds relays.mu in its final write

	// Hold supervise.mu until the new signal queues on it. It then holds that mutex through its durable write, so the
	// old retraction cannot finish before the new generation lands in both pending forms.
	s.supervise.mu.Lock()
	fresh := old
	fresh.Key, fresh.Detail = "retraction-race-new", "new idle"
	freshDone := make(chan struct{})
	go func() { s.signal(fresh); close(freshDone) }()
	waitForSuperviseMutex(t, "signalReplacing", &s.supervise.mu)
	s.supervise.mu.Unlock()
	waitForSuperviseMutex(t, "updateSupervisePending", &s.relays.mu)
	close(release)
	<-workingDone
	<-freshDone
	if got := storedSupervisePending(t, s)[fresh.id()]; got.Key != fresh.Key {
		t.Errorf("working's old-idle cleanup erased the newer durable idle: %+v", got)
	}
	if !idlePendingForReplayTest(s, fresh) {
		t.Error("working's old-idle cleanup erased the newer in-memory idle")
	}
}

// A late acknowledgment for an older stretch cannot delete the newer one gathered under the same signal id.
func TestPendingAckKeepsNewerStretch(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, _, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	old := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "old", At: time.Now()}
	newer := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "new", At: time.Now()}
	s.signal(old)
	s.signal(newer)
	s.ackSupervisePending([]superviseSignal{old})
	if got := storedSupervisePending(t, s)[newer.id()]; got.Key != newer.Key {
		t.Fatalf("the old acknowledgment removed the newer stretch: %+v", got)
	}
}

// A task leader's direct worker delivery uses the same durable acknowledgment as a conversation digest.
func TestTaskLeaderDeliveryAcknowledgesPending(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	g := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "worker-stall", At: time.Now()}
	s.signal(g)
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking})
	lead := TodoAgent{MachineID: "box", SessionID: "s1"}
	if !s.deliverToLeaderAgent(context.Background(), lead, []superviseSignal{g}, testWeb, time.Now(), time.Time{}) {
		t.Fatal("the online task leader did not accept its worker signal")
	}
	if got := storedSupervisePending(t, s); len(got) != 0 {
		t.Fatalf("the worker signal stayed pending after delivery: %+v", got)
	}
}

// Starting an offline leader's resume is not delivery: the outbox clears only after the blocked real send completes.
func TestOfflineTaskLeaderAcknowledgesOnlyAfterSend(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	if _, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	g := superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "offline-worker-stall", At: time.Now()}
	s.signal(g)
	markLeaderTurnSignalled(t, s, g)
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusOffline})
	started, release := make(chan struct{}), make(chan struct{})
	f.answerNode(DeviceSessionSend, func(_ string, _ json.RawMessage) (any, error) {
		close(started)
		<-release
		return map[string]any{"ok": true}, nil
	})
	lead := TodoAgent{MachineID: "box", SessionID: "s1"}
	if s.deliverToLeaderAgent(context.Background(), lead, []superviseSignal{g}, testWeb, time.Now(), time.Time{}) {
		t.Fatal("an offline resume is not synchronous delivery")
	}
	<-started
	if got := storedSupervisePending(t, s)[g.id()]; got.Key != g.Key {
		t.Fatal("the offline leader was acknowledged before its real send")
	}
	close(release)
	waitFor(t, "the offline leader's real-send acknowledgment", func() bool { return len(storedSupervisePending(t, s)) == 0 })
	requireLeaderTurnToldAfterRestart(t, s, g)
}

// A second digest may deliver new work while an older offline resume is still blocked. It must not acknowledge the
// older batch's pending signal or turn receipt before that older real send completes.
func TestOfflineLeaderSecondBatchDoesNotAcknowledgeInflightSignal(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	second := todo
	second.ID = "todo_second_worker"
	second.Title = "Second worker"
	putTodo(s, second)
	for _, id := range []string{todo.ID, second.ID} {
		if _, _, err := s.updateSupervision(context.Background(), id, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	first := superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "first-offline-send", At: time.Now()}
	newer := superviseSignal{Todo: second.ID, Kind: sigReview, Key: "second-offline-send", At: time.Now()}
	s.signal(first)
	s.signal(newer)
	markLeaderTurnSignalled(t, s, first)
	markLeaderTurnSignalled(t, s, newer)
	firstEntry, ok := s.digestEntry(ctx, first, testWeb)
	if !ok {
		t.Fatal("the first signal is not renderable")
	}
	_, _, rollback, err := s.reserveSupervisionDelivery(ctx, []prompts.SuperviseEntry{firstEntry}, []superviseSignal{first}, time.Now(), leaderResumeDelivery)
	if err != nil {
		t.Fatal(err)
	}
	// The first offline send is still in flight. A second online digest carries only the new signal; its synchronous
	// success must leave the first signal's pending entry and receipt for the first delivery callback.
	lead := TodoAgent{MachineID: "box", SessionID: "s1"}
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking})
	if !s.deliverToLeaderAgent(ctx, lead, []superviseSignal{first, newer}, testWeb, time.Now(), time.Time{}) {
		t.Fatal("the second online batch did not deliver")
	}
	text := strings.Join(deliveredTo(f, "s1"), "\n")
	if strings.Contains(text, first.Todo) || !strings.Contains(text, newer.Todo) {
		t.Fatalf("the second batch did not filter exactly the in-flight entry:\n%s", text)
	}
	if got := storedSupervisePending(t, s)[first.id()]; got.Key != first.Key {
		t.Fatal("the second batch acknowledged the first in-flight signal")
	}
	if got := storedSupervisePending(t, s)[newer.id()]; got.Key != "" {
		t.Fatalf("the second batch's own signal stayed pending: %+v", got)
	}
	if s.turnSignalled(first) {
		t.Fatal("the second batch closed the first in-flight signal's turn receipt")
	}
	if !s.turnSignalled(newer) {
		t.Fatal("the second batch's own turn receipt was not closed")
	}
	s.supervise.dedupMu.Lock()
	rollback()
	s.leaderDelivered(ctx, lead, []prompts.SuperviseEntry{firstEntry}, []superviseSignal{first}, []superviseSignal{first}, []superviseSignal{first})
	s.supervise.dedupMu.Unlock()
	if got := storedSupervisePending(t, s); len(got) != 0 {
		t.Fatalf("the first real delivery left its signal pending: %+v", got)
	}
	requireLeaderTurnToldAfterRestart(t, s, first)
	requireLeaderTurnToldAfterRestart(t, s, newer)
}

// A mixed batch can contain a signal whose task closed before delivery. Once another member's real send succeeds, the
// filtered member is obsolete too and must not be restored by the next restart, on either asynchronous success path.
func TestOfflineLeaderSuccessAcknowledgesFilteredBatch(t *testing.T) {
	for _, tc := range []struct {
		name       string
		failResume bool
	}{
		{name: "resume"},
		{name: "retry", failResume: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
			s, f, todo := newSupervisedServiceOn(t, st)
			s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
			closedAt := time.Now().UTC()
			closed := Todo{ID: "closed-before-leader-delivery", Title: "已关闭", Origin: "conv1", DoneAt: &closedAt,
				Supervision: &Supervision{Supervisor: "box/s1", State: reviewPending}}
			putTodo(s, closed)
			stale := superviseSignal{Todo: closed.ID, Kind: sigReview, Key: "closed-before-delivery", At: time.Now()}
			live := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "live-offline-batch", At: time.Now()}
			s.signal(stale)
			s.signal(live)
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusOffline})
			calls := 0
			f.answerNode(DeviceSessionSend, func(_ string, _ json.RawMessage) (any, error) {
				calls++
				if tc.failResume && calls == 1 {
					return nil, errors.New("resume failed")
				}
				return map[string]any{"ok": true}, nil
			})
			if tc.failResume {
				s.leaderResume = time.Millisecond
			}
			lead := TodoAgent{MachineID: "box", SessionID: "s1"}
			list := []superviseSignal{stale, live}
			if s.deliverToLeaderAgent(context.Background(), lead, list, testWeb, time.Now(), time.Time{}) {
				t.Fatal("an offline resume is not synchronous delivery")
			}
			waitFor(t, "the live member's real-send acknowledgment", func() bool {
				return storedSupervisePending(t, s)[live.id()].Key == ""
			})
			if got := storedSupervisePending(t, s); len(got) != 0 {
				t.Fatalf("a successful real send left filtered members in the durable outbox: %+v", got)
			}
		})
	}
}

// An unreachable leader's signal is delivered as a decision above it; the original worker signal must not replay too.
func TestUnreachableLeaderEscalationReplacesPending(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: 1, every: 1, urgent: 1}
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.Supervisor = "box/s1"
		return true
	}); err != nil {
		t.Fatal(err)
	}
	released := time.Now()
	putTodo(s, Todo{ID: "leader-task", Origin: "conv1", Supervision: &Supervision{Supervisor: "conv1"},
		Agents: []TodoAgent{{MachineID: "box", SessionID: "s1", Source: sourceLeader, ReleasedAt: &released}}})
	f.setSessions("box")
	g := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "unreachable-leader", At: time.Now()}
	s.signal(g)
	s.sendDigest(ctx)
	if got := strings.Join(f.noticesTo("conv1"), "\n"); !strings.Contains(got, todo.ID) || !strings.Contains(got, "cannot be reached") {
		t.Fatalf("the leader's decision did not reach main:\n%s", got)
	}
	if got := storedSupervisePending(t, s); len(got) != 0 {
		t.Fatalf("the escalated worker signal remained for a duplicate restart delivery: %+v", got)
	}
}

// A retry after the first resume failure is a real delivery too, and acknowledges the worker's durable signal.
func TestLeaderRetrySuccessAcknowledgesPending(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	g := superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "retry-leader", At: time.Now()}
	s.signal(g)
	markLeaderTurnSignalled(t, s, g)
	setSettings(t, s, func(v *Settings) { v.Main = "" }) // nobody above: only the leader retry can deliver it
	calls := 0
	f.answerNode(DeviceSessionSend, func(_ string, _ json.RawMessage) (any, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("resume failed")
		}
		return map[string]any{"ok": true}, nil
	})
	s.leaderResume = time.Millisecond
	lead := TodoAgent{MachineID: "box", SessionID: "s1"}
	s.escalateLeaderMessage(lead, []superviseSignal{g}, []superviseSignal{g}, []superviseSignal{g}, "worker supervision", false, nil)
	waitFor(t, "the successful leader retry's acknowledgment", func() bool { return len(storedSupervisePending(t, s)) == 0 })
	requireLeaderTurnToldAfterRestart(t, s, g)
}

// With no conversation above an unreachable leader, the current-process retry must keep both the durable signal and a
// real delivery attempt; removing it from pending and marking it sent would otherwise suppress delivery until restart.
func TestUnreachableLeaderWithoutUpperRetriesInCurrentProcess(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: 1, every: 1, urgent: 5 * time.Millisecond}
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.Supervisor = "box/s1"
		return true
	}); err != nil {
		t.Fatal(err)
	}
	released := time.Now()
	leader := Todo{ID: "leader-task", Origin: "conv1", Supervision: &Supervision{},
		Agents: []TodoAgent{{MachineID: "box", SessionID: "s1", Source: sourceLeader, ReleasedAt: &released}}}
	putTodo(s, leader)
	f.setSessions("box") // the released leader has no reachable agent

	// sendDigest reads main at entry, then webBase before routing. Remove main between those reads so the real escalation
	// branch has nobody above it despite passing the entry guard. Restore main only after this attempt, before its retry.
	setMain := func(main string) {
		s.prefs.mu.Lock()
		v := *s.prefs.cached
		v.Main = main
		s.prefs.cached, s.prefs.at = &v, time.Now()
		s.prefs.gen++
		s.prefs.mu.Unlock()
	}
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		setMain("")
		return FeishuStatus{}, nil
	})
	g := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "retry-without-upper", At: time.Now()}
	if lead, handled, reachable := s.leaderRoute(g); !handled || reachable {
		t.Fatalf("test leader route = %+v, handled %v, reachable %v; want handled and unreachable", lead, handled, reachable)
	}
	s.signal(g)
	s.sendDigest(ctx)
	if got := s.settings(ctx).Main; got != "" {
		t.Fatalf("the test did not clear main during routing: %q", got)
	}
	if got := todoNow(s, todo.ID).Supervision.Supervisor; got != "box/s1" {
		t.Fatalf("the test worker was rehomed before escalation: supervisor %q", got)
	}
	kept := storedSupervisePending(t, s)[g.id()].Key == g.Key

	// putTodo stored a copy of leader that shares this Agents array, and the digest timer reads it: change a copy.
	leader.Agents = slices.Clone(leader.Agents)
	leader.Agents[0].ReleasedAt = nil // the same leader task has an active agent again; no hub restart
	putTodo(s, leader)
	setMain("conv1")
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking})
	delivered := false
	waitFor(t, "the current-process retry after the leader returns", func() bool {
		delivered = strings.Contains(strings.Join(deliveredTo(f, "s1"), "\n"), todo.ID)
		return delivered
	})
	waitFor(t, "the retried delivery's durable acknowledgment", func() bool { return len(storedSupervisePending(t, s)) == 0 })
	if !kept || !delivered {
		t.Fatalf("unreachable leader retry: durable signal kept %v, delivered %v, pending %+v", kept, delivered, storedSupervisePending(t, s))
	}
}

// Resumed work makes an earlier idle stop moot even when no review or owner wait is stored. The durable-only form
// left by a restart must be forgotten before restoration; a genuinely new stop remains the control (#5732).
func TestSupervisedWorkingForgetsDurableIdleBeforeRestore(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	old := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "idle-before-resume", Detail: "obsolete idle stop", At: time.Now()}
	s.signal(old)
	if got := storedSupervisePending(t, s)[old.id()]; got.Key != old.Key {
		t.Fatalf("the idle outbox fixture was not persisted: %+v", got)
	}
	simulateSupervisionRestart(t, s)
	resumed := todo
	resumed.Supervision = &Supervision{Supervisor: "conv1", State: reviewRework, Round: 1}
	s.supervisedWorking(ctx, resumed)
	if got := storedSupervisePending(t, s)[old.id()]; got.Key == old.Key {
		t.Error("resumed work left the old idle stop in the durable outbox")
	}
	s.superviseTimes = superviseTimings{gather: 1, every: 1, urgent: 1}
	s.sendDigest(ctx)
	// The fresh control only needs durable admission. Do not leave its 1ns timer running into the shared retry knobs'
	// cleanup after this test returns.
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	if text := strings.Join(f.noticesTo("conv1"), "\n"); strings.Contains(text, "obsolete idle stop") {
		t.Fatalf("the old idle stop was restored and delivered after work resumed: %s", text)
	}
	fresh := old
	fresh.Key, fresh.Detail, fresh.At = "idle-after-resume", "new idle stop", time.Now()
	s.signal(fresh)
	if got := storedSupervisePending(t, s)[fresh.id()]; got.Key != fresh.Key {
		t.Errorf("a genuinely new idle stop was incorrectly suppressed: %+v", got)
	}
}

// relayWriteFailure keeps relay persistence down while independent turn/dedup writes continue to succeed.
type relayWriteFailure struct {
	Store
	mu   sync.Mutex
	fail bool
}

func (st *relayWriteFailure) setFail(fail bool) {
	st.mu.Lock()
	st.fail = fail
	st.mu.Unlock()
}

func (st *relayWriteFailure) PutSetting(ctx context.Context, key string, value any) error {
	st.mu.Lock()
	fail := st.fail && key == relayKey
	st.mu.Unlock()
	if fail {
		return errors.New("injected relay write failure")
	}
	return st.Store.PutSetting(ctx, key, value)
}

// The initial outbox write is the only durable copy of an unreplayable decision. A direct send must wait for that
// write; recovery then persists it before a restart and delivery, without asking the watch to reconstruct it (#5732).
func TestInitialSupervisionOutboxFailureBlocksDeliveryUntilDurable(t *testing.T) {
	st := &relayWriteFailure{Store: planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	presenceTimes(t, 0, 0)
	ownerTurnTimes(t, 0, 0)
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	g := superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "unreplayable-owner-decision", Detail: "only the owner can choose", At: time.Now()}
	markLeaderTurnSignalled(t, s, g)
	st.setFail(true)
	s.signal(g)
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	accepted := s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{g}, testWeb, time.Now(), time.Time{})
	if accepted {
		t.Fatal("a signal with no durable outbox entry was accepted for direct delivery")
	}
	if got := len(f.noticesTo("conv1")); got != 0 {
		t.Fatalf("a non-durable decision was sent: %d messages", got)
	}
	entry, ok := s.turnOf(ctx, todo.ID)
	if ok && entry.Signal != nil && entry.Signal.Told != nil {
		t.Fatalf("the non-durable decision was marked told: %+v", entry.Signal)
	}
	st.setFail(false)
	s.superviseTimes = superviseTimings{gather: 1, every: 1, urgent: 1}
	s.sendDigest(ctx) // first recovery persists the pending signal without delivering it
	if got := storedSupervisePending(t, s)[g.id()]; got.Key != g.Key {
		t.Fatalf("recovery did not durably persist the decision: %+v", got)
	}
	simulateSupervisionRestart(t, s)
	s.sendDigest(ctx)
	waitFor(t, "the recovered durable decision", func() bool {
		return strings.Contains(strings.Join(f.noticesTo("conv1"), "\n"), g.Detail)
	})
}

// A failed queue/outbox write is not durable admission. In particular, the independent turn-told write must not make
// restart recovery suppress the only remaining evidence of the notification (#5732).
func TestFailedSupervisionQueueWriteDoesNotMarkTurnTold(t *testing.T) {
	st := &relayWriteFailure{Store: planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	g := superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "queue-write-failure", Detail: "durable admission", At: time.Now()}
	markLeaderTurnSignalled(t, s, g)
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking})
	st.setFail(true)
	s.signal(g) // its initial outbox write fails too, as in the reported failure sequence
	accepted := s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{g}, testWeb, time.Now(), time.Time{})
	if accepted {
		t.Error("a failed durable queue write was reported as accepted delivery")
	}

	if got := s.queuedFor("conv1"); got != 0 {
		t.Errorf("a relay write failure left %d messages apparently admitted", got)
	}
	entry, ok := s.turnOf(ctx, todo.ID)
	told := ok && entry.Signal != nil && entry.Signal.Told != nil
	if told {
		t.Errorf("the failed queue write finalized the turn receipt: %+v", entry.Signal)
		return
	}
	s.supervise.mu.Lock()
	_, pending := s.supervise.pending[g.id()]
	s.supervise.mu.Unlock()
	if !pending {
		t.Fatal("the failed queue write did not restore the in-memory signal")
	}

	simulateSupervisionRestart(t, s)
	st.setFail(false)
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	s.superviseTimes = superviseTimings{gather: 1, every: 1, urgent: 1}
	s.signal(g)
	waitFor(t, "the untold turn after relay recovery", func() bool {
		return strings.Contains(strings.Join(f.noticesTo("conv1"), "\n"), g.Detail)
	})
}

func TestFailedQueuedSupervisionReplacementDoesNotMarkTurnTold(t *testing.T) {
	st := &relayWriteFailure{Store: planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	old := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "old-queued", Detail: "old queued text", At: time.Now()}
	if n := s.queueRelay("conv1", relayMsg{Tool: superviseTool, Text: old.Detail, Signals: encodeDigestSignals([]superviseSignal{old}), At: time.Now()}); n != 1 {
		t.Fatalf("queue the original supervision message: %d", n)
	}
	s.relays.mu.Lock()
	before := s.relays.st.Queue["conv1"][0]
	s.relays.mu.Unlock()

	g := superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "replacement-write-failure", Detail: "replacement admission", At: time.Now()}
	markLeaderTurnSignalled(t, s, g)
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusWorking})
	st.setFail(true)
	s.signal(g)
	accepted := s.deliverDigest(ctx, s.settings(ctx), "conv1", []superviseSignal{g}, testWeb, time.Now(), time.Time{})
	if accepted {
		t.Error("a failed queued-message replacement was reported as accepted delivery")
	}
	s.relays.mu.Lock()
	after := s.relays.st.Queue["conv1"][0]
	s.relays.mu.Unlock()
	if after.Text != before.Text || after.Signals != before.Signals {
		t.Errorf("the failed replacement changed the original queue entry: before=%+v after=%+v", before, after)
	}
	entry, ok := s.turnOf(ctx, todo.ID)
	if ok && entry.Signal != nil && entry.Signal.Told != nil {
		t.Errorf("the failed replacement finalized the new turn receipt: %+v", entry.Signal)
	}
}

// Queue admission and outbox acknowledgment are one relay-state write. An older crash state with both is reconciled.
func TestQueuedSupervisionAndOutboxAreAtomic(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, _, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	g := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "queued-once", At: time.Now()}
	s.signal(g)
	signals := []superviseSignal{g}
	if s.queueRelay("conv1", relayMsg{Tool: superviseTool, Text: "queued", Signals: encodeDigestSignals(signals), At: time.Now()}) != 1 {
		t.Fatal("the supervision message was not queued")
	}
	if got := storedSupervisePending(t, s); len(got) != 0 {
		t.Fatalf("queue admission left the same signal in the outbox: %+v", got)
	}
	// Reproduce the pre-fix crash window: both the durable queue and outbox contain the exact stretch.
	s.updateSupervisePending(func(pending map[string]superviseSignal) { pending[g.id()] = g })
	simulateSupervisionRestart(t, s)
	s.restoreSupervisePending(context.Background())
	if got := storedSupervisePending(t, s); len(got) != 0 {
		t.Fatalf("the queued signal was not reconciled from the outbox: %+v", got)
	}
	s.supervise.mu.Lock()
	_, restored := s.supervise.pending[g.id()]
	s.supervise.mu.Unlock()
	if restored {
		t.Fatal("the queued signal was restored for a second delivery")
	}
}

// A real send succeeded but its ACK was lost. On restart an offline dedup hit retries the real send instead of stranding it.
func TestOfflineLeaderDedupHitRetriesPending(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	g := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "dedup-after-restart", At: time.Now()}
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking})
	s.signal(g)
	lead := TodoAgent{MachineID: "box", SessionID: "s1"}
	if !s.deliverToLeaderAgent(context.Background(), lead, []superviseSignal{g}, testWeb, time.Now(), time.Time{}) {
		t.Fatal("the initial real send failed")
	}
	s.updateSupervisePending(func(pending map[string]superviseSignal) { pending[g.id()] = g }) // ACK lost after that send
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusOffline})
	simulateSupervisionRestart(t, s)
	s.restoreSupervisePending(context.Background())
	s.supervise.mu.Lock()
	s.supervise.pending = map[string]superviseSignal{} // sendDigest took the stretch out for this delivery attempt
	s.supervise.mu.Unlock()
	if s.deliverToLeaderAgent(context.Background(), lead, []superviseSignal{g}, testWeb, time.Now(), time.Time{}) {
		t.Fatal("the deduplicated offline retry is not synchronous delivery")
	}
	waitFor(t, "the offline dedup retry's real-send acknowledgment", func() bool { return len(storedSupervisePending(t, s)) == 0 })
}

// The crash window after a real send's outbox ACK but before its turn receipt is recovered by the next dedup hit.
// Signals filtered as moot need the same receipt; neither path may wait for the dedup window to expire and replay.
func TestLeaderEmptyDeliveryMarksTurnToldAfterRestart(t *testing.T) {
	for _, mode := range []string{"dedup hit", "moot"} {
		t.Run(mode, func(t *testing.T) {
			st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
			s, f, todo := newSupervisedServiceOn(t, st)
			s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
			ctx := context.Background()
			if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
				t.Fatal(err)
			}
			g := superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "leader-empty-" + strings.ReplaceAll(mode, " ", "-"), At: time.Now()}
			s.signal(g)
			markLeaderTurnSignalled(t, s, g)
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking})
			if mode == "dedup hit" {
				reserveUndelivered(t, s, g)
				s.ackSupervisePending([]superviseSignal{g}) // the real send's ACK landed; its turn receipt did not
			} else {
				closed := todoNow(s, todo.ID)
				now := time.Now().UTC()
				closed.DoneAt = &now
				putTodo(s, closed)
			}
			lead := TodoAgent{MachineID: "box", SessionID: "s1"}
			if s.deliverToLeaderAgent(ctx, lead, []superviseSignal{g}, testWeb, time.Now(), time.Time{}) {
				t.Fatal("an empty dedup/moot batch must not report a new send")
			}
			requireLeaderTurnToldAfterRestart(t, s, g)
		})
	}
}

// reserveUndelivered simulates a crash after dedup was reserved but before any delivery or queue admission.
func reserveUndelivered(t *testing.T, s *Service, g superviseSignal) {
	t.Helper()
	entry, ok := s.digestEntry(context.Background(), g, testWeb)
	if !ok {
		t.Fatal("the test signal is not renderable")
	}
	s.supervise.dedupMu.Lock()
	defer s.supervise.dedupMu.Unlock()
	// sendDigest takes the stretch out of the in-memory gather before reserving it; only the durable outbox then
	// records that it has not been delivered or admitted to the relay queue.
	s.supervise.mu.Lock()
	delete(s.supervise.pending, g.id())
	s.supervise.sent[g.id()] = g.Key
	s.supervise.mu.Unlock()
	_, _, _, err := s.reserveSupervision(context.Background(), []prompts.SuperviseEntry{entry}, []superviseSignal{g}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
}

// A dedup reservation is not delivery evidence: a still-pending needs-user signal must be delivered after restart.
func TestConversationDedupReservationDoesNotErasePending(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	g := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "reserved-not-delivered", Detail: "要用户决定：选择发布方式", At: time.Now()}
	s.signal(g)
	reserveUndelivered(t, s, g)
	simulateSupervisionRestart(t, s)
	s.superviseTimes = superviseTimings{gather: 1, every: 1, urgent: 1}
	s.sendDigest(context.Background())
	if got := strings.Join(f.noticesTo("conv1"), "\n"); !strings.Contains(got, "选择发布方式") {
		t.Fatalf("a dedup reservation erased the undelivered signal:\n%s", got)
	}
	if got := storedSupervisePending(t, s); len(got) != 0 {
		t.Fatalf("the delivered signal stayed pending: %+v", got)
	}
}

// A newer stretch can replace the outbox after an older reservation but before delivery. Its different Key must not
// make that unproven reservation look like a successful send after a restart.
func TestNewerPendingAfterUndeliveredReservationIsNotDeduplicated(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	first := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "reserved-old", Detail: "要用户决定：选择发布方式", At: time.Now()}
	s.signal(first)
	reserveUndelivered(t, s, first)
	newer := first
	newer.Key = "pending-new"
	s.signal(newer)
	simulateSupervisionRestart(t, s)
	s.superviseTimes = superviseTimings{gather: 1, every: 1, urgent: 1}
	s.sendDigest(context.Background())
	if got := strings.Join(f.noticesTo("conv1"), "\n"); !strings.Contains(got, newer.Detail) {
		t.Fatalf("the older undelivered reservation hid its newer pending replacement:\n%s", got)
	}
	if got := storedSupervisePending(t, s); len(got) != 0 {
		t.Fatalf("the delivered replacement stayed pending: %+v", got)
	}
}

// A replacement gathered after the predecessor's real send but before its ACK is an ordinary duplicate. The ACK must
// remove that predecessor's retry evidence without acknowledging the newer stretch itself.
func TestDeliveredPredecessorDoesNotWakeReplacement(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	first := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "sent-old", Detail: "要用户决定：选择发布方式", At: time.Now()}
	s.signal(first)
	reserveUndelivered(t, s, first)
	newer := first
	newer.Key = "replacement-after-send"
	s.signal(newer)
	s.ackSupervisePending([]superviseSignal{first}) // the real send succeeded; only its ACK was delayed
	s.superviseTimes = superviseTimings{gather: 1, every: 1, urgent: 1}
	s.sendDigest(context.Background())
	if got := strings.Join(f.noticesTo("conv1"), "\n"); got != "" {
		t.Fatalf("the delivered predecessor's replacement woke main again:\n%s", got)
	}
	if got := storedSupervisePending(t, s); len(got) != 0 {
		t.Fatalf("the deduplicated replacement stayed pending: %+v", got)
	}
}

// One member's dedup hit cannot hide it in a mixed batch nor let another member's delivery acknowledge it.
func TestMixedDedupBatchDeliversEveryPending(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	old := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "mixed-old", Detail: "旧的用户决定", At: time.Now()}
	newer := superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "mixed-new", Detail: "新的未判断停止", At: time.Now()}
	s.signal(old)
	reserveUndelivered(t, s, old)
	s.signal(newer)
	// The first stretch was already extracted for its undelivered attempt; a restart restores both durable members.
	simulateSupervisionRestart(t, s)
	s.superviseTimes = superviseTimings{gather: 1, every: 1, urgent: 1}
	s.sendDigest(context.Background())
	got := strings.Join(f.noticesTo("conv1"), "\n")
	if !strings.Contains(got, old.Detail) || !strings.Contains(got, newer.Detail) {
		t.Fatalf("the mixed batch dropped a pending member:\n%s", got)
	}
	if got := storedSupervisePending(t, s); len(got) != 0 {
		t.Fatalf("the mixed batch left delivered members pending: %+v", got)
	}
}

// Task leaders use the same rule for mixed batches, including an older dedup-reserved member.
func TestTaskLeaderMixedDedupBatchDeliversEveryPending(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking})
	old := superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "leader-mixed-old", Detail: "旧的 worker 决定", At: time.Now()}
	newer := superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "leader-mixed-new", Detail: "新的 worker 停止", At: time.Now()}
	s.signal(old)
	reserveUndelivered(t, s, old)
	s.signal(newer)
	lead := TodoAgent{MachineID: "box", SessionID: "s1"}
	if !s.deliverToLeaderAgent(context.Background(), lead, []superviseSignal{old, newer}, testWeb, time.Now(), time.Time{}) {
		t.Fatal("the mixed leader batch was not delivered")
	}
	messages := deliveredTo(f, "s1")
	if len(messages) == 0 || !strings.Contains(messages[len(messages)-1], old.Detail) || !strings.Contains(messages[len(messages)-1], newer.Detail) {
		t.Fatalf("the leader's mixed batch dropped a member: %q", messages)
	}
	if got := storedSupervisePending(t, s); len(got) != 0 {
		t.Fatalf("the leader's mixed batch left delivered members pending: %+v", got)
	}
}

// A mixed real send also covers the members suppressed by dedup. Their pre-reservation ledger receipts must land with
// the delivered member's receipt, including on the leader path's asynchronous recovery.
func TestLeaderMixedDeliveryMarksDedupHitTurnTold(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool { v.State = reviewPending; return true }); err != nil {
		t.Fatal(err)
	}
	other := Todo{ID: "other-worker", Title: "Other worker", Origin: "conv1", Supervision: &Supervision{Supervisor: "box/s1", State: reviewPending}}
	putTodo(s, other)
	old := superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "leader-mixed-acknowledged", At: time.Now()}
	newer := superviseSignal{Todo: other.ID, Kind: sigReview, Key: "leader-mixed-new-send", At: time.Now()}
	s.signal(old)
	markLeaderTurnSignalled(t, s, old)
	reserveUndelivered(t, s, old)
	s.ackSupervisePending([]superviseSignal{old})
	s.signal(newer)
	markLeaderTurnSignalled(t, s, newer)
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking})
	lead := TodoAgent{MachineID: "box", SessionID: "s1"}
	if !s.deliverToLeaderAgent(ctx, lead, []superviseSignal{old, newer}, testWeb, time.Now(), time.Time{}) {
		t.Fatal("the mixed leader batch's new member was not delivered")
	}
	requireLeaderTurnToldAfterRestart(t, s, old)
	requireLeaderTurnToldAfterRestart(t, s, newer)
}

// /stop silences a reporting task's gathered notices even when the manager restarts before the stop (#3308, #5732).
func TestStopReportingDropsPersistedPending(t *testing.T) {
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f := newQuietServiceOn(t, st)
	ctx := context.Background()
	f.known["lark1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lark1", MachineID: "server", Assistant: true, Status: proto.StatusWorking})
	s.markReporter(ctx, "server", "lark1")
	if _, err := s.toolTaskPlan(ctx, "lark1", planArgs(true)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the reporting task's agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
	agentTurn(s, f, proto.StatusWorking, "", 0)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	todo := onlyTodo(s)
	g := superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "before-stop", At: time.Now()}
	s.signal(g)
	if got := storedSupervisePending(t, s)[g.id()]; got.Key != g.Key {
		t.Fatal("the test signal was not persisted")
	}
	simulateSupervisionRestart(t, s)
	if w := serve(s, "POST", "/api/v1/assistant/conversations/lark1/stop", ""); w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := storedSupervisePending(t, s); len(got) != 0 {
		t.Fatalf("the stopped task's persisted signal survived: %+v", got)
	}
}
