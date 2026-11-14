package assistant

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Ten minutes covers reading a result, running a check and sending a follow-up.
// Live app subscriptions protect an open conversation for as long as it stays open.
const taskUserIdle = 10 * time.Minute

const taskActivityKey = "assistant.task_activity"

// activityUnknown is sessionActivityReason's answer when the owner's recent activity could not be read.
const activityUnknown = "the user's recent session activity could not be loaded"

type taskActivityBook struct {
	mu     sync.Mutex
	loaded bool
	times  map[string]time.Time
}

// Read receipts cannot represent input or leaving a page: that would mark a later
// answer read and suppress its notification. Keep those activity times separately.
func (s *Service) loadTaskActivityLocked(ctx context.Context) error {
	b := &s.taskActivity
	if b.loaded {
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
	for key, at := range b.times {
		if at.After(times[key]) {
			times[key] = at
		}
	}
	b.times, b.loaded = times, true
	return nil
}

func (s *Service) rememberSessionActivity(ctx context.Context, machine, session string) time.Time {
	b := &s.taskActivity
	b.mu.Lock()
	defer b.mu.Unlock()
	err := s.loadTaskActivityLocked(ctx)
	if b.times == nil {
		b.times = map[string]time.Time{}
	}
	now := time.Now().UTC()
	b.times[runtimeListID(machine, session)] = now
	if err != nil {
		s.logf("task activity: load: %v (kept in memory)", err)
		return now
	}
	for key, at := range b.times {
		if now.Sub(at) >= taskUserIdle {
			delete(b.times, key)
		}
	}
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, taskActivityKey, b.times); err != nil {
			s.logf("task activity: save: %v (kept in memory)", err)
		}
	}
	return now
}

func (s *Service) taskUserActivityAt(ctx context.Context, machine, session string) (time.Time, error) {
	b := &s.taskActivity
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.loadTaskActivityLocked(ctx); err != nil {
		return time.Time{}, err
	}
	return b.times[runtimeListID(machine, session)], nil
}

func (s *Service) taskCloseActivityError(ctx context.Context, t Todo, now time.Time) error {
	if t.DoneAt != nil {
		return nil
	}
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil || a.SessionID == "" {
			continue
		}
		if why := s.sessionActivityReason(ctx, a.MachineID, a.SessionID, now); why != "" {
			return fmt.Errorf("task %s cannot be closed: %s. Leave the task open and retry after the user leaves the "+
				"conversation and has been inactive for 10 minutes; do not stop or archive its Agent", t.ID, why)
		}
	}
	return nil
}

func (s *Service) sessionActivityReason(ctx context.Context, machine, session string, now time.Time) string {
	s.loadSeen(ctx)
	persistent := s.fleetStore() != nil
	activity, activityErr := s.taskUserActivityAt(ctx, machine, session)
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	for _, ref := range s.fleet.clients.Viewing {
		if ref.MachineID == machine && ref.SessionID == session {
			return fmt.Sprintf("the user is viewing Agent %s", short(session))
		}
	}
	if persistent && !s.fleet.seenLoaded || activityErr != nil {
		return activityUnknown
	}
	at := s.fleet.seen[[2]string{machine, session}]
	if activity.After(at) {
		at = activity
	}
	if !at.IsZero() && now.Sub(at) < taskUserIdle {
		return fmt.Sprintf("the user viewed or messaged Agent %s within the last 10 minutes", short(session))
	}
	return ""
}

// ownerOnSession: the owner is viewing agent machine/session, or messaged, read or left it within taskUserIdle. Activity
// that could not be read counts as absent here: the caller keeps its usual behavior (closedTurnReopens).
func (s *Service) ownerOnSession(ctx context.Context, machine, session string, now time.Time) bool {
	why := s.sessionActivityReason(ctx, machine, session, now)
	return why != "" && why != activityUnknown
}
