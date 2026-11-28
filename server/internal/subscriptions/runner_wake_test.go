package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

func waitCondition(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("timed out waiting for %s", desc)
}

type listNotifyStore struct {
	*MemStore
	listed chan struct{}
}

func (s *listNotifyStore) ListSubscriptions(ctx context.Context) ([]Subscription, error) {
	subs, err := s.MemStore.ListSubscriptions(ctx)
	select {
	case s.listed <- struct{}{}:
	default:
	}
	return subs, err
}

func TestRunRechecksBusyConnectorAfterWorkerCompletes(t *testing.T) {
	e := newEnv(t)
	_ = e.turnOn(e.create(itemsRecipe()))
	sub2 := e.turnOn(e.create(itemsRecipe()))
	// Push sub2's next scheduled run into the future so only explicit RunNow makes it due.
	future := e.clock.Now().Add(time.Hour)
	stored2, err := e.store.GetSubscription(e.ctx, sub2.ID)
	if err != nil || stored2 == nil {
		t.Fatalf("get sub2: %v", err)
	}
	prevRev := stored2.Revision
	stored2.Revision++
	stored2.NextRunAt = &future
	if ok, err := e.store.UpdateSubscription(e.ctx, *stored2, prevRev); err != nil || !ok {
		t.Fatalf("update sub2: ok=%v err=%v", ok, err)
	}

	firstInFlight := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondRan := make(chan struct{})
	var callNum atomic.Int32
	e.conn.setAnswer(func(connector.CatalogQuery) (string, error) {
		n := callNum.Add(1)
		if n == 1 {
			close(firstInFlight)
			<-releaseFirst
			return notices(1), nil
		}
		if n == 2 {
			close(secondRan)
		}
		return notices(2), nil
	})

	store := &listNotifyStore{MemStore: e.store, listed: make(chan struct{}, 16)}
	svc := New(Options{Store: store, Connectors: e.conns, Clock: e.clock})

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		svc.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
		<-runDone
	})

	select {
	case <-firstInFlight:
	case <-time.After(3 * time.Second):
		t.Fatal("first subscription did not start")
	}

	// Drain any earlier ListSubscriptions notifications, then trigger RunNow while connector is busy.
	for len(store.listed) > 0 {
		<-store.listed
	}
	if _, err := svc.RunNow(e.ctx, sub2.ID, Actor{}); err != nil {
		t.Fatal(err)
	}
	// Wait deterministically for startDueInto to record retryBusy["example"] while the first worker is still busy.
	select {
	case <-store.listed:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not wake for RunNow")
	}
	waitCondition(t, "retryBusy recorded for example", func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return svc.retryBusy["example"]
	})

	// Release the first worker; Run must immediately re-check the connector instead of waiting 30s for tickEvery.
	e.clock.advance(callGap + callJitter)
	close(releaseFirst)

	select {
	case <-secondRan:
	case <-time.After(2 * time.Second):
		t.Fatal("RunNow queued while connector was busy did not run promptly after worker completed")
	}
}

func TestRunRechecksDeferredConnectorsPastMaxRunning(t *testing.T) {
	e := newEnv(t)
	releaseFirstWave := make(chan struct{})
	var inFirstWave atomic.Int32
	fourInFlight := make(chan struct{})
	var fourOnce sync.Once
	fifthRan := make(chan struct{})
	var fifthOnce sync.Once

	for i := range maxRunning + 1 {
		cid := fmt.Sprintf("conn-%d", i+1)
		fc := &fakeConn{id: cid, sources: []connector.CatalogSource{noticesSource()}}
		// Seed initial answer for Create dry-run before switching to blocking wave handler.
		fc.setAnswer(func(connector.CatalogQuery) (string, error) { return notices(1), nil })
		e.conns.mu.Lock()
		e.conns.infos[cid] = &ConnectorInfo{ID: cid, Status: connector.StateOK, Enabled: true, Conn: fc}
		e.conns.mu.Unlock()

		r := itemsRecipe()
		sub, err := e.svc.Create(e.ctx, CreateRequest{Connector: cid, Source: "notices", Recipe: &r}, Actor{ID: "device:phone"})
		if err != nil {
			t.Fatalf("create %s: %v", cid, err)
		}
		e.turnOn(sub)
		fc.setAnswer(func(connector.CatalogQuery) (string, error) {
			n := inFirstWave.Add(1)
			if n <= maxRunning {
				if n == maxRunning {
					fourOnce.Do(func() { close(fourInFlight) })
				}
				<-releaseFirstWave
				return notices(int(n)), nil
			}
			fifthOnce.Do(func() { close(fifthRan) })
			return notices(int(n)), nil
		})
	}

	svc := New(Options{Store: e.store, Connectors: e.conns, Clock: e.clock})
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		svc.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-releaseFirstWave:
		default:
			close(releaseFirstWave)
		}
		<-runDone
	})

	select {
	case <-fourInFlight:
	case <-time.After(3 * time.Second):
		t.Fatalf("expected %d connectors running in parallel, got %d", maxRunning, inFirstWave.Load())
	}

	close(releaseFirstWave)

	select {
	case <-fifthRan:
	case <-time.After(2 * time.Second):
		t.Fatal("5th due connector deferred by maxRunning did not run promptly after a worker finished")
	}
}

func TestRunWaitsForInFlightWorkersOnCancel(t *testing.T) {
	e := newEnv(t)
	e.turnOn(e.create(itemsRecipe()))

	inFlight := make(chan struct{})
	release := make(chan struct{})
	e.conn.setAnswer(func(connector.CatalogQuery) (string, error) {
		close(inFlight)
		<-release
		return notices(1), nil
	})

	svc := New(Options{Store: e.store, Connectors: e.conns, Clock: e.clock})
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		svc.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-release:
		default:
			close(release)
		}
		<-runDone
	})

	select {
	case <-inFlight:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not enter CatalogCall")
	}

	cancel()
	select {
	case <-runDone:
		t.Fatal("Run returned before in-flight connector worker finished")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case <-runDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after in-flight worker finished")
	}
}

type failingLeaseStore struct {
	*MemStore
	listCalls  atomic.Int32
	leaseCalls atomic.Int32
	secondList chan struct{}
	secondOnce sync.Once
}

func (s *failingLeaseStore) ListSubscriptions(ctx context.Context) ([]Subscription, error) {
	if s.listCalls.Add(1) == 2 {
		s.secondOnce.Do(func() { close(s.secondList) })
	}
	return s.MemStore.ListSubscriptions(ctx)
}

func (s *failingLeaseStore) LeaseSubscription(context.Context, string, time.Time, time.Time) (bool, error) {
	s.leaseCalls.Add(1)
	return false, errors.New("storage read-only")
}

// When > maxRunning connectors are due during a storage write outage (LeaseSubscription error),
// workers making no progress must not re-poke Run in a tight loop.
func TestRunDoesNotSpinWhenLeaseFailsPastMaxRunning(t *testing.T) {
	e := newEnv(t)
	for i := range maxRunning + 1 {
		cid := fmt.Sprintf("ro-conn-%d", i+1)
		fc := &fakeConn{id: cid, sources: []connector.CatalogSource{noticesSource()}}
		fc.setAnswer(func(connector.CatalogQuery) (string, error) { return notices(1), nil })
		e.conns.mu.Lock()
		e.conns.infos[cid] = &ConnectorInfo{ID: cid, Status: connector.StateOK, Enabled: true, Conn: fc}
		e.conns.mu.Unlock()

		r := itemsRecipe()
		sub, err := e.svc.Create(e.ctx, CreateRequest{Connector: cid, Source: "notices", Recipe: &r}, Actor{ID: "device:phone"})
		if err != nil {
			t.Fatalf("create %s: %v", cid, err)
		}
		e.turnOn(sub)
	}

	store := &failingLeaseStore{MemStore: e.store, secondList: make(chan struct{})}
	svc := New(Options{Store: store, Connectors: e.conns, Clock: e.clock})

	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		svc.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-runDone
	})

	// Wait for all maxRunning initial workers to fail LeaseSubscription and exit.
	waitCondition(t, "initial workers to fail lease and exit", func() bool {
		svc.mu.Lock()
		running := svc.running
		svc.mu.Unlock()
		return store.leaseCalls.Load() >= maxRunning && running == 0
	})

	// Confirm Run does NOT spin on s.poke() after failed leases.
	select {
	case <-store.secondList:
		t.Fatalf("Run spun after failed leases: ListSubscriptions called %d times, want 1", store.listCalls.Load())
	case <-time.After(150 * time.Millisecond):
	}
	if got := store.listCalls.Load(); got != 1 {
		t.Fatalf("ListSubscriptions calls = %d, want 1", got)
	}
}
