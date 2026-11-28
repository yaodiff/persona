package assistant

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

const (
	externalPRObservationTTL = time.Minute
	externalPRRetryMin       = 30 * time.Second
	externalPRRetryMax       = 10 * time.Minute
	externalPRWorkers        = 4
)

type todoBlockerReconciler struct {
	mu    sync.Mutex // serializes storage reconciliation in this Service
	sweep atomic.Bool
	prs   externalPRObservations
}

type externalPRObservations struct {
	mu    sync.Mutex
	byURL map[string]externalPRCacheEntry
}

type externalPRCacheEntry struct {
	fact      todoBlockerPRFact
	startedAt time.Time
	nextAt    time.Time
	backoff   time.Duration
}

func externalPRObservationKey(ref string) string {
	return strings.ToLower(normalizeTodoBlockerPRRef(ref))
}

func (c *externalPRObservations) due(targets map[string]string, now time.Time) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.byURL {
		if _, active := targets[key]; !active {
			delete(c.byURL, key)
		}
	}
	var due []string
	for key, ref := range targets {
		if !now.Before(c.byURL[key].nextAt) {
			due = append(due, ref)
		}
	}
	slices.Sort(due)
	return due
}

func (c *externalPRObservations) lookup(ref string, now time.Time) (todoBlockerPRFact, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.byURL[externalPRObservationKey(ref)]
	return entry.fact, entry.fact.URL != "" && now.Before(entry.nextAt)
}

func (c *externalPRObservations) record(ref string, fact todoBlockerPRFact, ok bool, started, finished time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byURL == nil {
		c.byURL = make(map[string]externalPRCacheEntry)
	}
	key := externalPRObservationKey(ref)
	prev := c.byURL[key]
	if started.Before(prev.startedAt) {
		return false
	}
	entry := externalPRCacheEntry{startedAt: started}
	if ok {
		fact.URL = normalizeTodoBlockerPRRef(ref)
		entry.fact = fact
		entry.nextAt = finished.Add(externalPRObservationTTL)
	} else {
		entry.backoff = min(externalPRRetryMax, max(externalPRRetryMin, prev.backoff*2))
		entry.nextAt = finished.Add(entry.backoff)
	}
	c.byURL[key] = entry
	return ok
}

func terminalBlockerPR(fact todoBlockerPRFact) bool {
	return fact.Merged || strings.EqualFold(fact.State, "merged") || strings.EqualFold(fact.State, "closed")
}

// queueTodoBlockerSweep leaves the supervision tick free to process other work. A slow
// forge sweep can span several ticks, with at most one sweep and a bounded number of requests.
func (s *Service) queueTodoBlockerSweep(ctx context.Context, now time.Time) {
	if s.todoBlockerDB() == nil || !s.blockerReconcile.sweep.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer s.blockerReconcile.sweep.Store(false)
		if err := s.refreshTodoBlockers(ctx, now); err != nil {
			s.logf("todo blocker sweep: %v", err)
		}
	}()
}

func (s *Service) refreshTodoBlockers(ctx context.Context, now time.Time) error {
	sweepStarted := time.Now()
	_, reconcileErr := s.ReconcileTodoBlockers(ctx, "")
	st := s.todoBlockerDB()
	if st == nil {
		return reconcileErr
	}
	rows, err := st.ListActiveTodoBlockers(ctx)
	if err != nil {
		return errors.Join(reconcileErr, err)
	}
	if !s.settings(ctx).todosV2On() {
		rows = s.approvedExternalPRWaits(ctx, rows)
	}
	targets := make(map[string]string)
	for _, row := range rows {
		if row.Kind != todoBlockerKindExternalPR {
			continue
		}
		ref := normalizeTodoBlockerPRRef(row.TargetRef)
		if ref != "" {
			key := externalPRObservationKey(ref)
			if _, seen := targets[key]; !seen {
				targets[key] = ref
			}
		}
	}
	due := s.blockerReconcile.prs.due(targets, now)
	var workers sync.WaitGroup
	for i := range min(externalPRWorkers, len(due)) {
		workers.Go(func() {
			for j := i; j < len(due) && ctx.Err() == nil; j += externalPRWorkers {
				ref := due[j]
				started := time.Now()
				fact, ok := s.fetchBlockerPRFact(ctx, ref)
				if ctx.Err() != nil {
					return
				}
				if s.blockerReconcile.prs.record(ref, fact, ok, started, now.Add(time.Since(sweepStarted))) && terminalBlockerPR(fact) {
					if _, err := s.ReconcileTodoBlockers(ctx, ref); err != nil {
						s.logf("todo blockers: apply observed PR %s: %v", ref, err)
					}
				}
			}
		})
	}
	workers.Wait()
	return reconcileErr
}

// approvedExternalPRWaits keeps work already approved under Labs recoverable when Labs is disabled.
func (s *Service) approvedExternalPRWaits(ctx context.Context, rows []db.TodoBlockerRow) []db.TodoBlockerRow {
	if !slices.ContainsFunc(rows, func(row db.TodoBlockerRow) bool { return row.Kind == todoBlockerKindExternalPR }) {
		return nil
	}
	approved, err := s.queuedTodoTasks(ctx)
	if err != nil {
		s.logf("todo blockers: read approved PR waits: %v", err)
		return nil
	}
	var waiting []db.TodoBlockerRow
	for _, row := range rows {
		if row.Kind != todoBlockerKindExternalPR || approved[row.TodoID] == nil {
			continue
		}
		if todo, ok := s.todoNow(row.TodoID); ok && todo.DoneAt == nil && !todo.Cancelled() {
			waiting = append(waiting, row)
		}
	}
	return waiting
}
