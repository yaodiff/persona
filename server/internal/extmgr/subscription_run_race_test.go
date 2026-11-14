package extmgr

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/subscriptions"
)

// toolRunClock follows real time and advances by the runner's waits without making the test wait for them.
type toolRunClock struct {
	mu      sync.Mutex
	advance time.Duration
}

func (c *toolRunClock) Now() time.Time {
	c.mu.Lock()
	advance := c.advance
	c.mu.Unlock()
	return time.Now().Add(advance)
}

func (c *toolRunClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.advance += d
	c.mu.Unlock()
	return nil
}

// toolRunService uses the same store and connector as the tool environment, with a clock and audit hook for run tests.
func toolRunService(e *toolEnv, store subscriptions.Store, audit func(action string)) *subscriptions.Service {
	return subscriptions.New(subscriptions.Options{Store: store, Connectors: toolConns{e.conn}, Clock: &toolRunClock{},
		Lang: func() i18n.Lang { return i18n.En },
		Audit: func(_ context.Context, _, action string, _ subscriptions.Actor, _ map[string]any) {
			if audit != nil {
				audit(action)
			}
		}})
}

// scheduledFirstStore finishes a scheduled run inside RunNow's initial read. Once RunNow is recorded, the next read
// releases the runner for the manual run, after taking its snapshot of the scheduled run's history.
type scheduledFirstStore struct {
	*subscriptions.MemStore
	mu              sync.Mutex
	beforeGet       func()
	releaseAfterGet chan struct{}
}

func (s *scheduledFirstStore) GetSubscription(ctx context.Context, id string) (*subscriptions.Subscription, error) {
	s.mu.Lock()
	before := s.beforeGet
	s.beforeGet = nil
	s.mu.Unlock()
	if before != nil {
		before()
	}
	sub, err := s.MemStore.GetSubscription(ctx, id)
	s.mu.Lock()
	release := s.releaseAfterGet
	s.releaseAfterGet = nil
	s.mu.Unlock()
	if release != nil {
		close(release)
	}
	return sub, err
}

func (s *scheduledFirstStore) releaseOnNextGet(release chan struct{}) {
	s.mu.Lock()
	s.releaseAfterGet = release
	s.mu.Unlock()
}

func TestSubscriptionRunWaitsForNowAfterAScheduledRunWins(t *testing.T) {
	poll := subscriptionRunPoll
	subscriptionRunPoll = time.Millisecond
	t.Cleanup(func() { subscriptionRunPoll = poll })

	e := newToolEnv(t)
	s := e.made()
	store := &scheduledFirstStore{MemStore: e.store}
	runSecond := make(chan struct{})
	e.h.subs = toolRunService(e, store, func(action string) {
		if action == "subscription_run_now" {
			// RunNow has recorded its request. The tool's first poll still reads the scheduled run, then releases this one.
			store.releaseOnNextGet(runSecond)
		}
	})
	store.beforeGet = func() { e.h.subs.RunDue(context.Background()) }

	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		select {
		case <-runSecond:
			e.h.subs.RunDue(ctx)
		case <-ctx.Done():
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-secondDone
	})

	out, isErr := e.call("subscription_run", `{"id":"`+s.ID+`","wait_seconds":10}`)
	run, _ := out["run"].(map[string]any)
	items, _ := out["new_items"].([]any)
	if isErr || out["ran"] != true || run["trigger"] != subscriptions.TriggerNow || run["new"] != float64(0) || run["count"] != float64(2) || len(items) != 0 {
		t.Fatalf("the requested run, not the scheduled run that found the items: %v", out)
	}
	_, runs, err := e.h.subs.Get(context.Background(), s.ID)
	if err != nil || len(runs) != 2 || runs[0].Trigger != subscriptions.TriggerNow || runs[0].New != 0 ||
		runs[1].Trigger != subscriptions.TriggerSchedule || runs[1].New != 2 {
		t.Fatalf("the scheduled and requested runs: %+v, %v", runs, err)
	}
}
