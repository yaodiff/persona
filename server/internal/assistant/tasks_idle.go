package assistant

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Stopping agents that have been idle for a long time (docs/assistant/todos.md "Idle Agents stop too"). The pass in
// tasks_close.go only knows agents of finished tasks; agents started directly (agent_start), agents of tasks that
// stall or are never accepted, and stopped agents a message resumed (agent_send, the chat) would run for good.
// An agent Persona started that has been still for idle_close_hours with nothing going on (retirable) uses the same
// guarded stop as finished tasks. Its terminal stays unless close_ended_terminals is on; its transcript and launch
// state stay either way.
//
// Left running besides what retirable keeps: an agent with a wake due (Work.WakeAt) or blocked on anything (Work.Phase
// blocked: the user, another agent, CI or a deploy, a transient error), and one of an open task unless that task has
// itself been quiet for idleStalledFactor times the idle time.

const (
	// defaultIdleCloseHours is idle_close_hours when unset; 0 turns the pass off.
	defaultIdleCloseHours = 12
	maxIdleCloseHours     = 24 * 365
	// idleStalledFactor: an open task untouched for this many idle times no longer protects its agents.
	idleStalledFactor = 3
)

// idleCloseAfter is how long an agent must be idle before it is closed; 0 when the pass is off.
func (s *Service) idleCloseAfter(ctx context.Context) time.Duration {
	h := defaultIdleCloseHours
	if v := s.settings(ctx).IdleCloseHours; v != nil && *v >= 0 && *v <= maxIdleCloseHours {
		h = *v
	}
	return time.Duration(h) * time.Hour
}

// idleSeen: (machine, session) → when the pass first saw it running in a pane. A resumed agent's last activity can be
// old until its first step lands, so the idle time counts from the later of the two. The map lives in memory only: the
// first list of a machine after the hub starts (marked by idlePrimed) counts from each agent's own last activity
// instead, or every hub restart would start the clock again and an agent would never reach idle_close_hours. Agents
// that start running after that (resumed, started) count from when they are seen.
type idleSeen map[[2]string]time.Time

// idlePrimed is the mark of a machine whose running agents the pass has listed once since the hub started.
func idlePrimed(machine string) [2]string { return [2]string{machine, ""} }

// openTaskOf reports key's open task, its last activity, and whether personal verification still protects it.
func (s *Service) openTaskOf(key [2]string) (open bool, touched time.Time, userCheck bool) {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	id, ok := s.todos.activeBySession[key]
	if !ok {
		return false, time.Time{}, false
	}
	t := s.todos.todos[id]
	if t == nil {
		return true, time.Time{}, false
	}
	return true, t.UpdatedAt, t.Supervision.state(*t) == reviewUserCheck
}

// idleCloseable says whether c, first seen running at since, may be closed for being idle.
func idleCloseable(c proto.Session, since, now time.Time, idle time.Duration, openTask bool, taskTouched time.Time) bool {
	if c.Work != nil && (c.Work.Phase == proto.WorkBlocked || c.Work.WakeAt != nil) {
		return false
	}
	if openTask && now.Sub(taskTouched) < idle*idleStalledFactor {
		return false
	}
	return retirableAfter(c, since, now, idle)
}

// closeIdleAgents retires the agents on reachable machines that have been idle for too long (idleCloseable).
func (s *Service) closeIdleAgents(ctx context.Context, refused taskCloseRefused, completed doneSeen, seen idleSeen) {
	idle := s.idleCloseAfter(ctx)
	if idle <= 0 || !s.todosLoaded() {
		return
	}
	now := time.Now()
	running := map[[2]string]bool{}
	closedTasks := map[[2]string]bool{}
	for _, a := range s.doneTaskAgents() {
		key := [2]string{a.machine, a.session}
		if w := completed[key]; !w.resumed || !sameTaskCompletion(w.done, a.doneAt) {
			closedTasks[key] = true
		}
	}
	for _, d := range s.devices(ctx) {
		if !d.Reachable {
			for k := range seen { // an unreachable machine keeps what was seen
				if k[0] == d.ID {
					running[k] = true
				}
			}
			continue
		}
		list, err := s.listedSessions(ctx, d.ID) // an archive row is offline: never closed, never running
		if err != nil {
			for k := range seen {
				if k[0] == d.ID {
					running[k] = true
				}
			}
			continue
		}
		_, primed := seen[idlePrimed(d.ID)]
		seen[idlePrimed(d.ID)] = now
		running[idlePrimed(d.ID)] = true
		for _, c := range list {
			if c.Status == proto.StatusOffline || c.PaneID == "" {
				continue
			}
			key := [2]string{d.ID, c.SessionID}
			if closedTasks[key] {
				continue // the task's own grace period and archive belong to closeDoneTaskAgents
			}
			running[key] = true
			since, ok := seen[key]
			if !ok {
				since = now
				if !primed && !c.UpdatedAt.IsZero() && c.UpdatedAt.Before(now) {
					since = c.UpdatedAt // running since before the hub started: its own last activity
				}
				seen[key] = since
			}
			if w := completed[key]; w.resumed && w.running.After(since) {
				since = w.running // a new run can leave the previous run's idle timestamp behind
			}
			seen[key] = since
			open, touched, userCheck := s.openTaskOf(key)
			if userCheck || !idleCloseable(c, since, now, idle, open, touched) {
				continue
			}
			if s.sessionActivityReason(ctx, d.ID, c.SessionID, now) != "" {
				continue
			}
			if r, ok := refused[key]; ok && r.updated.Equal(c.UpdatedAt) && now.Sub(r.at) < taskCloseRetry {
				continue
			}
			if s.retireAgent(ctx, refused, "idle", d.ID, c, now) {
				delete(seen, key)
			}
		}
	}
	for k := range seen {
		if !running[k] {
			delete(seen, k)
		}
	}
}
