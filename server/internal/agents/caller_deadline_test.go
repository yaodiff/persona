package agents

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/sessionlist"
)

// ctxErrSource answers with ctx.Err() when ctx has ended, mirroring a real RPC/HTTP source
// whose caller's deadline passed before the source could answer.
type ctxErrSource struct {
	memSource
}

func (c *ctxErrSource) Sessions(ctx context.Context) ([]Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.memSource.Sessions(ctx)
}

func (c *ctxErrSource) Search(ctx context.Context, q string) ([]SearchHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return c.memSource.Search(ctx, q)
}

// A caller whose own deadline expired must get DeadlineExceeded back without poisoning the
// source's outage status, firing spurious change broadcasts, logging source failures, or
// evicting healthy rows from the shared paginated listIndex.
func TestCallerDeadlineDoesNotPoisonStatusOrDropIndexedRows(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	var changes [][]string
	logf := func(format string, args ...any) {
		mu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	onChange := func(ids []string) {
		mu.Lock()
		changes = append(changes, ids)
		mu.Unlock()
	}
	a := &ctxErrSource{memSource: memSource{name: "first", sessions: []AgentSession{{SessionKey: "a"}}}}
	b := &ctxErrSource{memSource: memSource{name: "second", sessions: []AgentSession{{SessionKey: "b"}}}}
	s := NewService(nil, onChange, logf, a, b)

	first, err := s.ListPage(context.Background(), sessionlist.Options{})
	if err != nil || first.Total != 2 {
		t.Fatalf("baseline: %+v %v", first, err)
	}
	s.cache.invalidate("second", nil)

	expired := newExpiringCtx()
	expired.expire()

	if _, err := s.ListPage(expired, sessionlist.Options{Since: first.Revision}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired ListPage: err=%v, want DeadlineExceeded", err)
	}
	if _, err := s.ListCounts(expired, sessionlist.Options{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired ListCounts: err=%v, want DeadlineExceeded", err)
	}
	if _, err := s.ListPageWithCounts(expired, sessionlist.Options{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired ListPageWithCounts: err=%v, want DeadlineExceeded", err)
	}
	if _, err := s.Sessions(expired, "", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired Sessions: err=%v, want DeadlineExceeded", err)
	}
	if _, err := s.Search(expired, "", "a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired Search: err=%v, want DeadlineExceeded", err)
	}

	mu.Lock()
	gotLogs := append([]string(nil), logs...)
	gotChanges := append([][]string(nil), changes...)
	mu.Unlock()
	if len(gotLogs) != 0 {
		t.Fatalf("caller deadline logged as source failure: %v", gotLogs)
	}
	if len(gotChanges) != 0 {
		t.Fatalf("caller deadline emitted spurious change notifications: %v", gotChanges)
	}
	for _, st := range s.Statuses(context.Background()) {
		if st.Error != "" {
			t.Fatalf("caller deadline poisoned status of %s: %+v", st.Source, st)
		}
	}

	// Healthy rows in the shared index must remain intact (no RemoveSource eviction).
	page, err := s.ListPage(context.Background(), sessionlist.Options{Since: first.Revision})
	if err != nil || page.Total != 2 || len(page.Remove) != 0 || page.Revision != first.Revision {
		t.Fatalf("indexed rows dropped on caller deadline: %+v %v (baseline rev %s)", page, err, first.Revision)
	}
}

// A waiter joining a healthy in-flight list call whose own deadline expires must not evict
// the source from the shared listIndex while the leader's fetch is still healthy.
func TestJoinedWaiterDeadlineDoesNotEvictIndexedSource(t *testing.T) {
	a := &memSource{name: "first", sessions: []AgentSession{{SessionKey: "a"}}}
	g := newGatedSource("second")
	s := NewService(nil, nil, nil, a, g)

	// Populate baseline index with both sources.
	doneBase := make(chan struct{})
	var base sessionlist.Page
	var baseErr error
	go func() {
		defer close(doneBase)
		base, baseErr = s.ListPage(context.Background(), sessionlist.Options{})
	}()
	waitStarted(t, g)
	close(g.release)
	<-doneBase
	if baseErr != nil || base.Total != 2 {
		t.Fatalf("baseline: %+v %v", base, baseErr)
	}

	// Replace release gate and invalidate second's cache so a fresh leader fetch blocks in flight.
	g.release = make(chan struct{})
	s.cache.invalidate("second", nil)

	leaderDone := make(chan error, 1)
	go func() {
		_, err := s.Sessions(context.Background(), "second", "")
		leaderDone <- err
	}()
	waitStarted(t, g)

	waiterCtx := newExpiringCtx()
	waiterDone := make(chan error, 1)
	go func() {
		_, err := s.ListPage(waiterCtx, sessionlist.Options{Since: base.Revision})
		waiterDone <- err
	}()
	waitWaiters(t, s, "second", 1)

	waiterCtx.expire()
	if err := <-waiterDone; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter ListPage: err=%v, want DeadlineExceeded", err)
	}

	// Inspect the shared index directly before releasing leader: second must NOT have been removed.
	counts, err := s.listIndex.Counts(sessionlist.Options{})
	if err != nil || counts.Total != 2 {
		t.Fatalf("waiter deadline evicted healthy source from listIndex: counts=%+v err=%v", counts, err)
	}

	close(g.release)
	if err := <-leaderDone; err != nil {
		t.Fatalf("leader Sessions: %v", err)
	}
}

// PollOnce aborted by its caller context's deadline must not mark healthy sources as failing.
func TestPollOnceCallerDeadlineDoesNotPoisonStatus(t *testing.T) {
	var changes [][]string
	a := &ctxErrSource{memSource: memSource{name: "first", sessions: []AgentSession{{SessionKey: "a"}}}}
	s := NewService(nil, func(ids []string) { changes = append(changes, ids) }, nil, a)

	s.PollOnce(context.Background())

	expired := newExpiringCtx()
	expired.expire()
	s.PollOnce(expired)

	if len(changes) != 0 {
		t.Fatalf("PollOnce with expired caller ctx emitted changes: %v", changes)
	}
	if st := s.Statuses(context.Background()); len(st) != 1 || st[0].Error != "" {
		t.Fatalf("PollOnce with expired caller ctx poisoned status: %+v", st)
	}

	// A source-internal context.Canceled while the poll context is still live must skip only that
	// source without poisoning its status or aborting polling of remaining healthy sources.
	canceledSrc := &memSource{name: "canceled", fail: context.Canceled}
	healthySrc := &memSource{name: "healthy", sessions: []AgentSession{{SessionKey: "h1", RankTimeMs: 1}}}
	s2 := NewService(nil, nil, nil, canceledSrc, healthySrc)
	s2.PollOnce(context.Background())
	if healthySrc.listN != 1 {
		t.Fatalf("PollOnce aborted before polling remaining healthy source: listN=%d, want 1", healthySrc.listN)
	}
	for _, st := range s2.Statuses(context.Background()) {
		if st.Error != "" {
			t.Fatalf("source-internal cancel poisoned status of %s: %+v", st.Source, st)
		}
	}
}
