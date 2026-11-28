package assistant

import (
	"cmp"
	"context"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Retiring finished task_spawn sub-sessions while their parent to-do stays open (#13578).
// A spawned session shares the parent's to-do (task_spawn.go) and previously stayed open until
// the parent to-do closed (tasks_close.go) or hours of quiet on the whole to-do (tasks_idle.go).
// Once a spawned session has reported done (or reached WorkDone), has no pending question,
// queued input, or background work, and stays quiet for spawnedCloseIdle, the same guarded stop
// retires its TUI while keeping its transcript and to-do link intact for resume.

// spawnedCloseIdle is how long a completed task_spawn session must stay quiet before retiring.
var spawnedCloseIdle = 45 * time.Second

// spawnedDoneBook remembers when each task_spawn session reported completion (in memory).
// Any follow-up message (agent_send, user_spoke) or non-done report clears the entry.
type spawnedDoneBook struct {
	mu   sync.Mutex
	done map[[2]string]time.Time
}

func (b *spawnedDoneBook) note(machine, session string, at time.Time) {
	if machine == "" || session == "" {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	b.mu.Lock()
	if b.done == nil {
		b.done = map[[2]string]time.Time{}
	}
	b.done[[2]string{machine, session}] = at
	b.mu.Unlock()
}

func (b *spawnedDoneBook) clear(machine, session string) {
	if machine == "" || session == "" {
		return
	}
	b.mu.Lock()
	delete(b.done, [2]string{machine, session})
	b.mu.Unlock()
}

func (b *spawnedDoneBook) get(machine, session string) (time.Time, bool) {
	b.mu.Lock()
	at, ok := b.done[[2]string{machine, session}]
	b.mu.Unlock()
	return at, ok
}

func (b *spawnedDoneBook) prune(active map[[2]string]bool) {
	b.mu.Lock()
	for k := range b.done {
		if !active[k] {
			delete(b.done, k)
		}
	}
	b.mu.Unlock()
}

func (s *Service) noteSpawnedDone(machine, session string, at time.Time) {
	s.spawnedDone.note(s.canonical(machine), session, at)
}

func (s *Service) clearSpawnedDone(machine, session string) {
	s.spawnedDone.clear(s.canonical(machine), session)
}

type spawnedTaskAgent struct {
	todo, machine, session string
}

// openSpawnedAgents returns active source="spawned" agents attached to currently open tasks.
func (s *Service) openSpawnedAgents() []spawnedTaskAgent {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	var out []spawnedTaskAgent
	for _, t := range s.todos.todos {
		if t.Origin == "" || t.DoneAt != nil {
			continue
		}
		for _, a := range t.Agents {
			if a.Source != "spawned" || a.ReleasedAt != nil || a.DismissedAt != nil {
				continue
			}
			out = append(out, spawnedTaskAgent{todo: t.ID, machine: a.MachineID, session: a.SessionID})
		}
	}
	return out
}

// spawnedTaskAgentStillEligible rechecks task ownership and completion state right before stopping.
func (s *Service) spawnedTaskAgentStillEligible(a spawnedTaskAgent, doneAt time.Time, byWork bool) bool {
	if !byWork {
		got, ok := s.spawnedDone.get(a.machine, a.session)
		if !ok || !sameTaskCompletion(got, doneAt) {
			return false
		}
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	t := s.todos.todos[a.todo]
	if t == nil || t.Origin == "" || t.DoneAt != nil {
		return false
	}
	for _, link := range t.Agents {
		if link.MachineID == a.machine && link.SessionID == a.session &&
			link.Source == "spawned" && link.ReleasedAt == nil && link.DismissedAt == nil {
			return true
		}
	}
	return false
}

// closeDoneSpawnedAgents retires completed, idle task_spawn sessions while their shared to-do stays open.
// Transcripts, launch state, and to-do links remain intact so any retired session can be resumed later.
func (s *Service) closeDoneSpawnedAgents(ctx context.Context, refused taskCloseRefused, seen doneSeen) {
	if !s.todosLoaded() {
		return
	}
	agents := s.openSpawnedAgents()
	if len(agents) == 0 {
		clear(seen)
		s.spawnedDone.prune(nil)
		return
	}
	now := time.Now()
	reachable := map[string]bool{}
	for _, d := range s.devices(ctx) {
		reachable[d.ID] = d.Reachable
	}
	lists := map[string]map[string]proto.Session{}
	active := make(map[[2]string]bool, len(agents))
	for _, a := range agents {
		key := [2]string{a.machine, a.session}
		active[key] = true
		if !reachable[a.machine] {
			continue
		}
		c, ok := s.doneTaskAgentRow(ctx, lists, doneTaskAgent{todo: a.todo, machine: a.machine, session: a.session})
		if !ok {
			continue
		}
		doneAt, reported := s.spawnedDone.get(a.machine, a.session)
		byWork := false
		if !reported && c.Work != nil && c.Work.Phase == proto.WorkDone && c.Work.RetiredAt == nil {
			doneAt = cmp.Or(c.Work.TurnAt, c.Work.At, c.UpdatedAt)
			byWork = !doneAt.IsZero()
		}
		if !reported && !byWork {
			delete(seen, key)
			continue
		}
		w := seen.see(key, doneAt, c, now, false)
		if w.resumed || w.running.IsZero() {
			continue
		}
		if c.Work != nil && (c.Work.Phase == proto.WorkBlocked || c.Work.WakeAt != nil) {
			continue
		}
		if !retirableAfter(c, doneAt, now, spawnedCloseIdle) {
			continue
		}
		if s.sessionActivityReason(ctx, a.machine, a.session, now) != "" {
			continue
		}
		if r, ok := refused[key]; ok && r.updated.Equal(c.UpdatedAt) && now.Sub(r.at) < taskCloseRetry {
			continue
		}
		if !s.spawnedTaskAgentStillEligible(a, doneAt, byWork) {
			continue
		}
		if !s.retireAgent(ctx, refused, "spawned "+a.todo, a.machine, c, now) {
			continue
		}
		w.stopped, w.running, w.lastStop = true, time.Time{}, now
		seen[key] = w
		s.clearSpawnedDone(a.machine, a.session)
	}
	for k := range seen {
		if !active[k] {
			delete(seen, k)
		}
	}
	s.spawnedDone.prune(active)
}
