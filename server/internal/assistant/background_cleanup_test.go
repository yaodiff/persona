package assistant

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// drainFleetBackground stops new background scheduling, then lets active calls finish before a test closes its store.
// Production Run uses the same canceled runCtx to stop new naming passes and direction-tree timers.
func drainFleetBackground(t *testing.T, s *Service, cancel context.CancelFunc) {
	t.Helper()
	cancel()

	// A retry callback may still be restoring todos before it takes dedupMu. Mark timers stopped, wait for the whole
	// callback, and disarm anything left; a callback that already fired observes the stop before any I/O.
	s.stopSuperviseTimers()
	s.stopQuietTimers()

	names := &s.agentNames
	names.mu.Lock()
	if names.timer != nil {
		names.timer.Stop()
		names.timer = nil
		names.timerSeq++
	}
	names.mu.Unlock()

	tree := &s.dtree
	tree.mu.Lock()
	if tree.timer != nil && tree.timer.Stop() {
		tree.timer = nil
		tree.armed = false
		tree.runs.Done()
	}
	if tree.wakeTimer != nil {
		tree.wakeTimer.Stop()
		tree.wakeTimer, tree.wake = nil, time.Time{}
	}
	tree.mu.Unlock()

	s.todoViewsStop()
	s.todosCountStop()

	// A screen-upgrade start checks runCtx under fleet.mu; taking it here closes the Add/Wait teardown window.
	// Delayed turn completions use the same gate: stop pending timers and let an already running callback finish.
	s.fleet.mu.Lock()
	s.fleet.completionStopped = true
	for timer := range s.fleet.completionTimers {
		if timer.Stop() {
			s.fleet.completionRuns.Done()
		}
		delete(s.fleet.completionTimers, timer)
	}
	for _, p := range s.fleet.interactions {
		if p.orphaned != nil {
			p.orphaned.Stop()
			p.orphaned = nil // a queued callback checks this under fleet.mu before recording its outcome
		}
	}
	s.fleet.mu.Unlock()
	// workEvent and recordSendAsync use work.mu for the same runCtx + Add scheduling gate.
	s.work.mu.Lock()
	s.work.mu.Unlock()

	waitFleetBackground(t, "turn completions", &s.fleet.completionRuns)
	waitFleetBackground(t, "agent naming", &names.runs)
	waitFleetBackground(t, "direction tree", &tree.runs)
	waitFleetBackground(t, "to-do views", &s.tview.runs)
	waitFleetBackground(t, "to-do counts", &s.fleet.live.counts.todos.runs)
	waitFleetBackground(t, "screen upgrades", &s.fleet.upgrades)
	recWait(t, s) // the recording module's (hub transcription, questions)
	waitFleetBackground(t, "work events", &s.work.eventWrites)
}

// newRestartedService is a second manager on a test's store, as after a hub restart. Its background work (work events,
// screen upgrades) is drained before the database fixture closes, as newFleetTestOn does for the first one: otherwise
// it logs through the ended test, a data race under -race and a "Log in goroutine after ... has completed" panic.
func newRestartedService(t *testing.T, o Options) *Service {
	t.Helper()
	s := New(o)
	runCtx, cancel := context.WithCancel(context.Background())
	s.running.Store(&runCtx)
	t.Cleanup(func() {
		// Invalidate a delayed recovery retry before the fixture closes its store.
		s.approvals.mu.Lock()
		s.approvals.recoverySeq++
		s.approvals.mu.Unlock()
		drainFleetBackground(t, s, cancel)
	})
	return s
}

func waitFleetBackground(t *testing.T, name string, wg *sync.WaitGroup) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		t.Fatalf("%s did not finish before the database fixture closed", name)
	}
}

// orphanEndMem observes the real grace callback's outcome write without closing a database under a finished test.
type orphanEndMem struct {
	*fleetMem
	ended chan string
}

func (m *orphanEndMem) FinishInteraction(ctx context.Context, id, status string, r *proto.Resolution) error {
	m.ended <- id
	return m.fleetMem.FinishInteraction(ctx, id, status, r)
}

func orphanTimerFixture(t *testing.T) (*Service, *orphanEndMem, context.CancelFunc) {
	t.Helper()
	st := &orphanEndMem{fleetMem: newFleetMem(), ended: make(chan string, 1)}
	s := New(Options{Hub: newFakeHub(), Store: func() Store { return st }, Logf: testLogf(t), Devices: newFleetDevs()})
	runCtx, cancel := context.WithCancel(context.Background())
	s.running.Store(&runCtx)
	p := &pending{ix: resumable("ix_drain"), done: make(chan proto.Resolution)}
	p.ix.ExpiresAt = time.Now().Add(30 * time.Millisecond)
	s.fleet.mu.Lock()
	s.fleet.interactions = map[string]*pending{p.ix.ID: p}
	if !s.orphanLocked(p) {
		t.Fatal("the short grace did not arm")
	}
	s.fleet.mu.Unlock()
	return s, st, cancel
}

func TestOrphanTimerRecordsCancellationBeforeDrain(t *testing.T) {
	s, st, cancel := orphanTimerFixture(t)
	select {
	case id := <-st.ended:
		if id != "ix_drain" {
			t.Fatalf("ended %q", id)
		}
	case <-time.After(time.Second):
		t.Fatal("the ordinary grace callback did not record its cancellation")
	}
	drainFleetBackground(t, s, cancel)
}

func TestDrainFleetBackgroundStopsOrphanedInteractions(t *testing.T) {
	s, st, cancel := orphanTimerFixture(t)
	drainFleetBackground(t, s, cancel)
	select {
	case id := <-st.ended:
		t.Fatalf("orphan timer %q recorded an outcome after the service was drained", id)
	case <-time.After(100 * time.Millisecond):
	}
}

// delayedBackgroundStore lets a background database call finish only after its test has returned. A fixture that drains
// the call before closing the database sees no error; without that drain, the closed-pool error logs through the ended T.
type delayedBackgroundStore struct {
	*db.DB
	release chan struct{}
}

func newDelayedBackgroundStore(t *testing.T) *delayedBackgroundStore {
	t.Helper()
	st := &delayedBackgroundStore{DB: taskRecoveryDatabase(t), release: make(chan struct{})}
	time.AfterFunc(100*time.Millisecond, func() { close(st.release) })
	return st
}

func (s *delayedBackgroundStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == settingsKey {
		select {
		case <-s.release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return s.DB.Setting(ctx, key, out)
}

func (s *delayedBackgroundStore) AddWorkEvent(ctx context.Context, e db.WorkEvent) error {
	<-s.release
	return s.DB.AddWorkEvent(ctx, e)
}

// acceptedSendStore pauses an accepted send record while it reads the conversation roles. The test can then stop new
// scheduling before releasing the record, proving that the rest of that accepted operation still writes its event.
type acceptedSendStore struct {
	*db.DB
	rolesStarted chan struct{}
	releaseRoles chan struct{}
	startedOnce  sync.Once
}

func (s *acceptedSendStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == settingsKey {
		s.startedOnce.Do(func() { close(s.rolesStarted) })
		select {
		case <-s.releaseRoles:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return s.DB.Setting(ctx, key, out)
}

// digestLoadStore holds a digest in its todo restoration, before it takes the delivery lock. Draining still has to
// wait for that callback; otherwise the database fixture can close while the callback is only getting started.
type digestLoadStore struct {
	*db.DB
	started chan struct{}
	release chan struct{}
	loaded  chan struct{}
	once    sync.Once
}

func (s *digestLoadStore) ListTodos(context.Context) ([]db.TodoRow, error) {
	s.once.Do(func() { close(s.started) })
	<-s.release
	close(s.loaded)
	return nil, nil
}

func (s *digestLoadStore) ListAllTodoSpaces(context.Context) ([]db.TodoSpaceRow, error) {
	return nil, nil
}

func (s *digestLoadStore) ListAllTodoAgents(context.Context) ([]db.TodoAgentRow, error) {
	return nil, nil
}

func TestDrainFleetBackgroundDigestLifecycle(t *testing.T) {
	t.Run("stops_retry", func(t *testing.T) {
		s := New(Options{Hub: newFakeHub(), Store: func() Store { return &memStore{} }, Logf: t.Logf, Devices: newFleetDevs()})
		runCtx, cancel := context.WithCancel(context.Background())
		s.running.Store(&runCtx)
		b := &s.supervise
		b.mu.Lock()
		b.init()
		s.retryDigestLocked(time.Hour)
		b.mu.Unlock()

		drainFleetBackground(t, s, cancel)
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.timer != nil {
			t.Fatal("the digest retry remained armed after the service was drained")
		}
	})

	t.Run("waits_for_callback_before_delivery_lock", func(t *testing.T) {
		st := &digestLoadStore{DB: taskRecoveryDatabase(t), started: make(chan struct{}), release: make(chan struct{}), loaded: make(chan struct{})}
		s := New(Options{Hub: newFakeHub(), Store: func() Store { return st }, Logf: t.Logf, Devices: newFleetDevs()})
		runCtx, cancel := context.WithCancel(context.Background())
		s.running.Store(&runCtx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.sendDigest(context.Background())
		}()
		select {
		case <-st.started:
		case <-time.After(5 * time.Second):
			t.Fatal("the digest did not reach its todo restoration")
		}
		time.AfterFunc(50*time.Millisecond, func() { close(st.release) })

		drainFleetBackground(t, s, cancel)
		early := false
		select {
		case <-st.loaded:
		default:
			early = true
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the digest did not finish after its restoration was released")
		}
		if early {
			t.Fatal("drain returned while the digest was still restoring its todos")
		}
	})
}

// A database-backed fleet fixture must drain the service work it started before the test's database closes. Each case
// returns immediately: cleanup, not a sleep or an assertion inside the case, is what has to finish the background work.
func TestFleetDatabaseFixtureDrainsBackgroundWork(t *testing.T) {
	t.Run("direction_tree", func(t *testing.T) {
		ft := newFleetTestOn(t, taskRecoveryDatabase(t))
		if err := ft.s.ensureTodosLoaded(context.Background()); err != nil {
			t.Fatal(err)
		}
		ft.s.directionTreeSoon()
	})

	t.Run("agent_naming", func(t *testing.T) {
		ft := newFleetTestOn(t, newDelayedBackgroundStore(t))
		ft.s.agentNamesChanged(DeviceSessions{})
	})

	t.Run("work_events", func(t *testing.T) {
		ft := newFleetTestOn(t, newDelayedBackgroundStore(t))
		ft.s.workEvent("mac", "s1", "turn_finished", nil)
	})

	t.Run("work_events_after_stop", func(t *testing.T) {
		ft := newFleetTestOn(t, newDelayedBackgroundStore(t))
		time.AfterFunc(50*time.Millisecond, func() { ft.s.workEvent("mac", "s1", "turn_finished", nil) })
	})

	t.Run("accepted_send_after_stop", func(t *testing.T) {
		ctx := context.Background()
		database := taskRecoveryDatabase(t)
		if err := database.PutSetting(ctx, settingsKey, Settings{Leaders: []string{"s1"}}); err != nil {
			t.Fatal(err)
		}
		st := &acceptedSendStore{DB: database, rolesStarted: make(chan struct{}), releaseRoles: make(chan struct{})}
		ft := newFleetTestOn(t, st)
		ft.s.northStarOn.Store(true)
		ft.s.recordSendAsync("mac", "s1", "resume", originManager, false, outcomeOK, "", "")
		select {
		case <-st.rolesStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("accepted send did not reach its role lookup")
		}
		ft.cancel()
		close(st.releaseRoles)
		waitFleetBackground(t, "accepted send", &ft.s.work.eventWrites)
		events, err := database.WorkEventsBetween(ctx, time.Now().Add(-time.Minute), time.Now().Add(time.Minute), []string{sendEvent})
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 {
			t.Fatalf("accepted send events after stop = %d, want 1", len(events))
		}
	})

	t.Run("synchronous_send_after_stop", func(t *testing.T) {
		ctx := context.Background()
		st := &workMemStore{}
		session := t.Name()
		if err := st.PutSetting(ctx, settingsKey, Settings{Leaders: []string{session}}); err != nil {
			t.Fatal(err)
		}
		ft := newFleetTestOn(t, st)
		ft.s.northStarOn.Store(true)
		ft.cancel()
		if ft.s.runCtx().Err() == nil {
			t.Fatal("the service context is not canceled")
		}
		// The hub's synchronous delivery report is already on its caller's stack, not a new background operation.
		ft.s.ConversationSent(ctx, ConversationSent{MachineID: "mac", SessionID: session, Outcome: assistantwire.ConversationDelivered})
		// A new asynchronous manager send remains stopped.
		ft.s.recordSendAsync("mac", session, "resume", originManager, false, outcomeOK, "", "")
		waitFleetBackground(t, "post-stop async send", &ft.s.work.eventWrites)
		st.wmu.Lock()
		got := len(st.events)
		st.wmu.Unlock()
		if got != 1 {
			t.Fatalf("synchronous send events after stop = %d, want 1", got)
		}
	})
}
