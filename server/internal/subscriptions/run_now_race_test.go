package subscriptions

import (
	"context"
	"sync"
	"testing"
	"time"
)

// scheduledLeaseGate holds the first scheduled run after it has read the request flag and taken its lease.
type scheduledLeaseGate struct {
	*MemStore
	leased  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *scheduledLeaseGate) LeaseSubscription(ctx context.Context, id string, now, until time.Time) (bool, error) {
	ok, err := s.MemStore.LeaseSubscription(ctx, id, now, until)
	if err != nil || !ok {
		return ok, err
	}
	first := false
	s.once.Do(func() {
		first = true
		close(s.leased)
	})
	if first {
		select {
		case <-s.release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return true, nil
}

func TestRunNowArrivingDuringScheduledRunStaysQueued(t *testing.T) {
	e := newEnv(t)
	sub := e.turnOn(e.create(itemsRecipe()))
	store := &scheduledLeaseGate{MemStore: e.store, leased: make(chan struct{}), release: make(chan struct{})}
	svc := New(Options{Store: store, Connectors: e.conns, Clock: e.clock})
	ctx, cancel := context.WithTimeout(e.ctx, 5*time.Second)
	defer cancel()
	scheduledDone := make(chan struct{})
	go func() {
		svc.RunDue(ctx)
		close(scheduledDone)
	}()
	select {
	case <-store.leased:
	case <-ctx.Done():
		t.Fatalf("waiting for the scheduled run's lease: %v", ctx.Err())
	}
	if _, err := svc.RunNow(e.ctx, sub.ID, Actor{}); err != nil {
		close(store.release)
		t.Fatal(err)
	}
	close(store.release)
	select {
	case <-scheduledDone:
	case <-ctx.Done():
		t.Fatalf("waiting for the scheduled run: %v", ctx.Err())
	}

	e.clock.advance(callGap + callJitter) // past the connector gap, but not the next scheduled run
	svc.RunDue(e.ctx)
	_, runs, err := svc.Get(e.ctx, sub.ID)
	if err != nil || len(runs) != 2 || runs[0].Trigger != TriggerNow || runs[0].New != 0 ||
		runs[1].Trigger != TriggerSchedule || runs[1].New != 3 {
		t.Fatalf("the manual request made during the scheduled run: %+v, %v", runs, err)
	}
}
