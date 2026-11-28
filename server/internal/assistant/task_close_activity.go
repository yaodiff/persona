package assistant

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"
)

// Ten minutes covers reading a result, running a check and sending a follow-up.
// Live app subscriptions protect an open conversation for as long as it stays open.
const taskUserIdle = 10 * time.Minute

const taskActivityKey = "assistant.task_activity"

// An explicit read protects retirement, but can also come from a passive viewer.
const taskReadActivityPrefix = "read:"

// activityUnknown is sessionActivityReason's answer when the owner's recent activity could not be read.
const activityUnknown = "the user's recent session activity could not be loaded"

type taskActivityBook struct {
	mu           sync.Mutex
	writing      sync.Mutex
	loaded       bool
	times        map[string]time.Time
	ownerExcerpt map[string]string
	retiring     map[string]map[*taskRetirement]struct{}
}

type taskRetirement struct{ cancel context.CancelFunc }

// Register before the final activity read so input cannot fall between the read
// and the cancellable RPC. No storage or fleet mutex is held across the stop.
func (s *Service) retirementContext(ctx context.Context, machine, session string) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	r := &taskRetirement{cancel: cancel}
	b, key := &s.taskActivity, runtimeListID(machine, session)
	b.mu.Lock()
	if b.retiring == nil {
		b.retiring = map[string]map[*taskRetirement]struct{}{}
	}
	if b.retiring[key] == nil {
		b.retiring[key] = map[*taskRetirement]struct{}{}
	}
	b.retiring[key][r] = struct{}{}
	b.mu.Unlock()
	if s.sessionActivityReason(ctx, machine, session, time.Now()) != "" {
		cancel()
	}
	return ctx, func() {
		cancel()
		b.mu.Lock()
		delete(b.retiring[key], r)
		if len(b.retiring[key]) == 0 {
			delete(b.retiring, key)
		}
		b.mu.Unlock()
	}
}

func (s *Service) cancelSessionRetirement(machine, session string) {
	b := &s.taskActivity
	b.mu.Lock()
	defer b.mu.Unlock()
	for r := range b.retiring[runtimeListID(machine, session)] {
		r.cancel()
	}
}

// Read receipts cannot represent input or leaving a page: that would mark a later
// answer read and suppress its notification. Keep those activity times separately.
func (s *Service) loadTaskActivity(ctx context.Context) error {
	b := &s.taskActivity
	b.mu.Lock()
	loaded := b.loaded
	b.mu.Unlock()
	if loaded {
		return nil
	}
	b.writing.Lock()
	defer b.writing.Unlock()
	b.mu.Lock()
	loaded = b.loaded
	b.mu.Unlock()
	if loaded {
		return nil
	}
	times := map[string]time.Time{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, taskActivityKey, &times); err != nil {
			return err
		}
	}
	if times == nil {
		times = map[string]time.Time{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for key, at := range b.times {
		if at.After(times[key]) {
			times[key] = at
		}
	}
	b.times, b.loaded = times, true
	return nil
}

func (s *Service) rememberSessionActivity(ctx context.Context, machine, session string) {
	s.rememberTaskActivity(ctx, machine, session, runtimeListID(machine, session))
}

func (s *Service) rememberTaskActivity(ctx context.Context, machine, session, key string) {
	b := &s.taskActivity
	b.mu.Lock()
	if b.times == nil {
		b.times = map[string]time.Time{}
	}
	now := time.Now().UTC()
	b.times[key] = now
	b.mu.Unlock()
	s.cancelSessionRetirement(machine, session)
	if err := s.loadTaskActivity(ctx); err != nil {
		s.logf("task activity: load: %v (kept in memory)", err)
		return
	}
	// Publish input before storage can block. A transaction's final guard never waits on persistence.
	b.writing.Lock()
	defer b.writing.Unlock()
	b.mu.Lock()
	for key, at := range b.times {
		if now.Sub(at) >= taskUserIdle {
			delete(b.times, key)
		}
	}
	times := maps.Clone(b.times)
	b.mu.Unlock()
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, taskActivityKey, times); err != nil {
			s.logf("task activity: save: %v (kept in memory)", err)
		}
	}
}

func (s *Service) taskUserActivityAt(machine, session string) (activity, read time.Time, loaded bool) {
	b := &s.taskActivity
	key := runtimeListID(machine, session)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.times[key], b.times[taskReadActivityPrefix+key], b.loaded
}

type taskCloseHoldError struct {
	todo, why string
	work      *taskCloseWork
}

func (e *taskCloseHoldError) Error() string {
	if e.work != nil {
		return fmt.Sprintf("task %s cannot be closed: the user has an unfinished request in Agent %q (%s)", e.todo, e.work.title, e.work.items)
	}
	return fmt.Sprintf("task %s cannot be closed: %s. Leave the task open and retry after the user leaves the "+
		"conversation and has been inactive for 10 minutes; do not stop or archive its Agent", e.todo, e.why)
}

func (s *Service) taskCloseActivityError(ctx context.Context, t Todo, now time.Time) error {
	if t.DoneAt != nil {
		return nil
	}
	_ = s.loadTaskActivity(ctx)
	return s.taskCloseSnapshotError(t, now)
}

func (s *Service) taskCloseSnapshotError(t Todo, now time.Time) error {
	return s.taskCloseSnapshotErrorExcept(t, now, SessionRef{})
}

func (s *Service) taskCloseSnapshotErrorExcept(t Todo, now time.Time, untouched SessionRef) error {
	if work := s.taskCloseWorkSnapshot(t, now); work != nil {
		return &taskCloseHoldError{todo: t.ID, work: work}
	}
	return s.taskCloseActivitySnapshotErrorExcept(t, now, untouched)
}

// taskCloseActivitySnapshotError is safe inside a database transaction: cache reads only, with no storage lock or I/O.
func (s *Service) taskCloseActivitySnapshotError(t Todo, now time.Time) error {
	return s.taskCloseActivitySnapshotErrorExcept(t, now, SessionRef{})
}

func (s *Service) taskCloseActivitySnapshotErrorExcept(t Todo, now time.Time, untouched SessionRef) error {
	if t.DoneAt != nil {
		return nil
	}
	// Revalidate the same dispatch and its current review/delivery state at the write boundary.
	if untouched.SessionID != "" && s.freshDispatchAgent(t, now) != untouched {
		untouched = SessionRef{}
	}
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil || a.SessionID == "" {
			continue
		}
		if why := s.sessionActivitySnapshotReasonExcept(a.MachineID, a.SessionID, now, untouched.SessionID == a.SessionID && untouched.MachineID == a.MachineID); why != "" {
			return &taskCloseHoldError{todo: t.ID, why: why}
		}
	}
	return nil
}

// A refused close retracts the completed turn's review, not the turn itself. The sweep can judge it again after
// inactivity. Serialize with new work so an old review cannot invalidate a later turn or undo a concurrent closure.
// Call after releasing the review's write locks.
func (s *Service) deferTaskReview(ctx context.Context, id string, err error) error {
	var hold *taskCloseHoldError
	if !errors.As(err, &hold) {
		return err
	}
	s.tasks.completionMu.Lock()
	defer s.tasks.completionMu.Unlock()
	t, ok := s.todoNow(id)
	if !ok || t.DoneAt != nil || t.Supervision == nil || t.Supervision.State != reviewPending {
		return err
	}
	e, ok := s.turnOf(ctx, id)
	if !ok || e.State != turnDone || e.Judged != nil &&
		(e.Judged.Check.Verdict != verdictDone || e.Judged.Skip != "" || e.Judged.Err != "") {
		return err
	}
	_, changed, resetErr := s.updateSupervision(ctx, id, func(v *Supervision) bool {
		if v.State != reviewPending || s.closedSince(id, e.Work) {
			return false
		}
		v.State = ""
		if v.Round > 0 {
			v.State = reviewRework
		}
		return true
	})
	if resetErr != nil {
		return errors.Join(err, fmt.Errorf("could not defer the pending review: %w", resetErr))
	}
	if changed {
		s.deferTurnJudgment(ctx, id)
		b := &s.supervise
		b.mu.Lock()
		delete(b.pending, id+"|"+sigReview)
		b.mu.Unlock()
	}
	return err
}

func (s *Service) sessionActivityReason(ctx context.Context, machine, session string, now time.Time) string {
	_ = s.loadTaskActivity(ctx)
	return s.sessionActivitySnapshotReason(machine, session, now)
}

func (s *Service) sessionActivitySnapshotReason(machine, session string, now time.Time) string {
	return s.sessionActivitySnapshotReasonExcept(machine, session, now, false)
}

func (s *Service) sessionActivitySnapshotReasonExcept(machine, session string, now time.Time, untouched bool) string {
	activity, read, activityLoaded := s.taskUserActivityAt(machine, session)
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	for _, ref := range s.fleet.clients.Viewing {
		if ref.MachineID == machine && ref.SessionID == session {
			if untouched && activityLoaded && activity.IsZero() {
				return "" // this is only a passive viewer; actual activity still wins
			}
			return fmt.Sprintf("the user is viewing Agent %s", short(session))
		}
	}
	if !activityLoaded {
		return activityUnknown
	}
	if !untouched && read.After(activity) {
		activity = read
	}
	if !activity.IsZero() && now.Sub(activity) < taskUserIdle {
		return fmt.Sprintf("the user viewed or messaged Agent %s within the last 10 minutes", short(session))
	}
	return ""
}
