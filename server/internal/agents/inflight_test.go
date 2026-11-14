package agents

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/sessionlist"
)

// gatedSource answers Sessions only when released (or when the asker goes away), counting the asks.
type gatedSource struct {
	memSource
	calls   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func newGatedSource(name string) *gatedSource {
	return &gatedSource{memSource: memSource{name: name}, started: make(chan struct{}, 64), release: make(chan struct{})}
}

func (g *gatedSource) Sessions(ctx context.Context) ([]Session, error) {
	g.calls.Add(1)
	g.started <- struct{}{}
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []Session{Wrap(g.name, AgentSession{SessionKey: "s1", Provider: "grok_bot"}, "")}, nil
}

func waitStarted(t *testing.T, g *gatedSource) {
	t.Helper()
	select {
	case <-g.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the source was not asked")
	}
}

// waitWaiters waits until n callers joined the ask of source that is under way.
func waitWaiters(t *testing.T, s *Service, source string, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		s.flights.mu.Lock()
		got := 0
		for k, f := range s.flights.m {
			if k.source == source {
				got += f.waiters
			}
		}
		s.flights.mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d callers joined the ask, want %d", got, n)
		}
		runtime.Gosched() // no fixed sleep: the joins happen within the bound or the test fails
	}
}

// The Agents page's sections, groups and counts, and every other open app, read at once after a change drops the
// cache: the source is asked once, and they all get its answer.
func TestListsMissingTheCacheTogetherAskTheSourceOnce(t *testing.T) {
	g := newGatedSource("grokbot")
	s := NewService(&memKV{m: map[string][]byte{}}, nil, nil, g)
	ctx := context.Background()
	const readers = 8
	var wg sync.WaitGroup
	errs := make([]error, readers)
	sizes := make([]int, readers)
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 3 {
			case 0:
				l, err := s.Sessions(ctx, "", "")
				errs[i], sizes[i] = err, len(l)
			case 1:
				p, err := s.ListPage(ctx, sessionlist.Options{})
				errs[i], sizes[i] = err, len(p.Sessions)
			default:
				c, err := s.ListCounts(ctx, sessionlist.Options{})
				errs[i], sizes[i] = err, c.Total
			}
		}(i)
	}
	waitStarted(t, g)
	waitWaiters(t, s, "grokbot", readers-1)
	close(g.release)
	wg.Wait()
	for i := range errs {
		if errs[i] != nil || sizes[i] != 1 {
			t.Fatalf("reader %d: %d rows, %v", i, sizes[i], errs[i])
		}
	}
	if n := g.calls.Load(); n != 1 {
		t.Fatalf("the source was asked %d times for %d readers, want once", n, readers)
	}
	if _, err := s.Sessions(ctx, "", ""); err != nil || g.calls.Load() != 1 {
		t.Fatalf("the shared answer is cached: %d asks, %v", g.calls.Load(), err)
	}
}

// expiringCtx is a context whose deadline passes when the test says so, so a deadline is not a race with the joins.
type expiringCtx struct {
	context.Context
	done chan struct{}
}

func newExpiringCtx() *expiringCtx {
	return &expiringCtx{Context: context.Background(), done: make(chan struct{})}
}

func (c *expiringCtx) Done() <-chan struct{} { return c.done }
func (c *expiringCtx) expire()               { close(c.done) }
func (c *expiringCtx) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

// The caller whose ask the others joined goes away, cancelled or past its deadline: they ask again themselves, not
// answer with its cancellation or timeout.
func TestAJoinedListAsksAgainWhenItsAskerGoesAway(t *testing.T) {
	for _, tc := range []struct {
		name  string
		asker func() (context.Context, func())
	}{
		{"cancelled", func() (context.Context, func()) { return context.WithCancel(context.Background()) }},
		{"deadline", func() (context.Context, func()) { c := newExpiringCtx(); return c, c.expire }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGatedSource("grokbot")
			s := NewService(&memKV{m: map[string][]byte{}}, nil, nil, g)
			first, end := tc.asker()
			firstErr := make(chan error, 1)
			go func() { _, err := s.Sessions(first, "grokbot", ""); firstErr <- err }()
			waitStarted(t, g)

			type result struct {
				list []Session
				err  error
			}
			second := make(chan result, 1)
			go func() { l, err := s.Sessions(context.Background(), "grokbot", ""); second <- result{l, err} }()
			waitWaiters(t, s, "grokbot", 1)
			end()
			if err := <-firstErr; err == nil {
				t.Fatal("the caller that went away got an answer")
			}
			waitStarted(t, g) // the one left asks again
			close(g.release)
			r := <-second
			if r.err != nil || len(r.list) != 1 {
				t.Fatalf("the caller left waiting: %v %v", r.list, r.err)
			}
			if n := g.calls.Load(); n != 2 {
				t.Fatalf("asked %d times, want 2", n)
			}
		})
	}
}

// timingOutSource fails its ask with its own timeout once released, while the asker is still there.
type timingOutSource struct{ *gatedSource }

func (g timingOutSource) Sessions(ctx context.Context) ([]Session, error) {
	if _, err := g.gatedSource.Sessions(ctx); err != nil {
		return nil, err
	}
	return nil, context.DeadlineExceeded
}

// The source itself times out while its asker is still there: that failure is shared, not asked again by each waiter.
func TestAJoinedListSharesTheSourcesOwnTimeout(t *testing.T) {
	g := timingOutSource{newGatedSource("grokbot")}
	s := NewService(&memKV{m: map[string][]byte{}}, nil, nil, g)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := s.Sessions(context.Background(), "grokbot", ""); errs <- err }()
	}
	waitStarted(t, g.gatedSource)
	waitWaiters(t, s, "grokbot", 1)
	close(g.release)
	for i := 0; i < 2; i++ {
		if err := <-errs; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("caller %d: %v, want the source's timeout", i, err)
		}
	}
	if n := g.calls.Load(); n != 1 {
		t.Fatalf("asked %d times, want once", n)
	}
}

// An ask that began before a change is not shared with a caller after it: that caller asks for the new list.
func TestAListAfterAnInvalidateDoesNotJoinAnEarlierAsk(t *testing.T) {
	g := newGatedSource("grokbot")
	s := NewService(&memKV{m: map[string][]byte{}}, nil, nil, g)
	ctx := context.Background()
	done := make(chan error, 2)
	go func() { _, err := s.Sessions(ctx, "grokbot", ""); done <- err }()
	waitStarted(t, g)
	s.Updated("grokbot", nil)
	go func() { _, err := s.Sessions(ctx, "grokbot", ""); done <- err }()
	waitStarted(t, g)
	close(g.release)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if n := g.calls.Load(); n != 2 {
		t.Fatalf("asked %d times, want 2 (one per generation)", n)
	}
}
