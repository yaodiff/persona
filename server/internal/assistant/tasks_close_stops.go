package assistant

import (
	"context"
	"time"
)

const taskCloseStopsKey = "assistant.completed_task_stops"

// taskCloseStopBook is owned by the task-close loop. It keeps the completion
// for which each linked Agent was observed stopped. A running row on the next
// manager can then count as a resume even if it missed the offline row.
type taskCloseStopBook struct {
	completions map[string]time.Time // (task, machine, session) → task's done_at
	dirty       bool
}

func (a doneTaskAgent) stopKey() string {
	return a.todo + "/" + runtimeListID(a.machine, a.session)
}

// A manual edit returns its Go timestamp while Postgres stores done_at to
// microsecond precision. The stop ledger may also contain old nanosecond values.
func sameTaskCompletion(a, b time.Time) bool {
	return a.Truncate(time.Microsecond).Equal(b.Truncate(time.Microsecond))
}

func (s *Service) loadTaskCloseStops(ctx context.Context) error {
	b := &s.taskCloseStops
	if b.completions != nil {
		return nil
	}
	completions := map[string]time.Time{}
	if st := s.store(); st != nil {
		ctx, cancel := context.WithTimeout(ctx, agentArchiveTimeout)
		defer cancel()
		if _, err := st.Setting(ctx, taskCloseStopsKey, &completions); err != nil {
			return err
		}
		// The archive also carries confirmed stops from before this setting existed.
		if s.agentArchives.snapshot.Load() == nil {
			if err := s.agentArchives.lock(ctx); err != nil {
				return err
			}
			err := s.loadAgentArchivesLocked(ctx)
			s.agentArchives.unlock()
			if err != nil {
				return err
			}
		}
	}
	if completions == nil {
		completions = map[string]time.Time{}
	}
	b.completions = completions
	return nil
}

func (s *Service) archivedStopAfterTaskDone(a doneTaskAgent) bool {
	archives := s.agentArchives.snapshot.Load()
	return archives != nil && (*archives)[runtimeListID(a.machine, a.session)].ClosedAt.After(a.doneAt)
}

func (b *taskCloseStopBook) note(key string, doneAt time.Time) {
	doneAt = doneAt.Truncate(time.Microsecond)
	if !b.completions[key].Equal(doneAt) {
		b.completions[key] = doneAt
		b.dirty = true
	}
}

func (b *taskCloseStopBook) prune(current map[string]time.Time) {
	for key, recorded := range b.completions {
		if !sameTaskCompletion(recorded, current[key]) {
			delete(b.completions, key)
			b.dirty = true
		}
	}
}

func (s *Service) saveTaskCloseStops(ctx context.Context) {
	b := &s.taskCloseStops
	if !b.dirty {
		return
	}
	if st := s.store(); st != nil {
		// Finish recording a confirmed stop even if this manager is shutting down.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentArchiveTimeout)
		defer cancel()
		if err := st.PutSetting(ctx, taskCloseStopsKey, b.completions); err != nil {
			s.logf("completed task agents: save stop evidence: %v", err)
			return // retry on the next pass
		}
	}
	b.dirty = false
}
