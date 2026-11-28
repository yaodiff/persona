package assistant

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Serial workspace queueing and deterministic FIFO auto-start (#13142).
//
// When a task targeting an existing workspace opts into queueing (workspace.queue == true) or names the occupying
// task in its after prerequisites, occupancy at admission parks the task in a durable TaskQueue entry instead of
// failing into a start-error card. When the occupying task completes, is accepted/cancelled, or otherwise releases
// the workspace (and any after prerequisites are satisfied), the hub deterministically dequeues and starts the next
// FIFO waiter for that (machine, workspace) without timers or polling.

const workspaceQueueKey = "workspace_occupied"

// taskWorkspaceWait identifies the occupied (machine, path) a workspace-queued task waits on and the occupying task
// last observed holding it.
type taskWorkspaceWait struct {
	Machine    string `json:"machine"`
	Path       string `json:"path"`
	OwnerID    string `json:"owner_id,omitempty"`
	OwnerTitle string `json:"owner_title,omitempty"`
}

// workspaceTarget is one canonical (machine, path) workspace key to inspect for queued tasks.
type workspaceTarget struct {
	machine string
	path    string
}

// workspaceBusyQueueError is returned by admitTaskWorkspace when an existing workspace is occupied by another open
// task and the caller opted into serial workspace queueing instead of failing immediately.
type workspaceBusyQueueError struct {
	machine    string
	path       string
	ownerID    string
	ownerTitle string
}

func (e *workspaceBusyQueueError) Error() string {
	return fmt.Sprintf("workspace %s on %s is occupied by task %s (%s); queued", e.path, e.machine, e.ownerID, e.ownerTitle)
}

// shouldQueueExistingWorkspace reports whether task t should enter the serial workspace queue rather than failing
// immediately when its existing workspace is occupied by owner.
func (s *Service) shouldQueueExistingWorkspace(ctx context.Context, t PlanTask, owner *Todo) bool {
	if owner == nil || t.RequestGate != "" || t.Workspace == nil || t.Workspace.Mode != workspaceExisting {
		return false
	}
	if t.Workspace.Queue {
		return true
	}
	if t.WorkspaceInferred {
		return false
	}
	return s.taskWaitsAfter(t, owner.ID)
}

// taskWaitsAfter reports whether task t (or its bound to-do) directly or transitively lists targetID in its after
// prerequisites.
func (s *Service) taskWaitsAfter(t PlanTask, targetID string) bool {
	targetID = strings.TrimSpace(targetID)
	if targetID == "" {
		return false
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	queue := cleanIDs(t.After)
	if todoID := firstNonEmpty(t.TodoID, t.ReuseTodo); todoID != "" {
		if td := s.todos.todos[todoID]; td != nil {
			queue = cleanIDs(slices.Concat(queue, td.After))
		}
	}
	seen := map[string]bool{}
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if id == targetID {
			return true
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if dep := s.todos.todos[id]; dep != nil {
			queue = append(queue, dep.After...)
		}
	}
	return false
}

// nextWorkspaceQueueTime returns a strictly increasing UTC timestamp at microsecond precision so FIFO ordering
// survives Postgres timestamptz round-trips and frozen clocks in synctest.
func (s *Service) nextWorkspaceQueueTime() time.Time {
	now := time.Now().UTC().Truncate(time.Microsecond)
	s.tasks.mu.Lock()
	if !now.After(s.tasks.lastWorkspaceQueueAt) {
		now = s.tasks.lastWorkspaceQueueAt.Add(time.Microsecond)
	}
	s.tasks.lastWorkspaceQueueAt = now
	s.tasks.mu.Unlock()
	return now
}

func (s *Service) workspaceWaitReason(ctx context.Context, machine, cwd, ownerID, ownerTitle string) string {
	return i18n.T(s.lang(ctx), "assistant-tasks_workspace.queued", i18n.Args{
		"id":      ownerID,
		"title":   ownerTitle,
		"machine": machine,
		"path":    cwd,
	})
}

// workspaceQueueEntry builds a durable TaskQueue entry for a task waiting behind owner on (machine, cwd).
// If before is already a workspace queue entry, its original QueuedAt is preserved so FIFO order never resets.
func (s *Service) workspaceQueueEntry(ctx context.Context, machine, cwd, ownerID, ownerTitle string, retry bool, before *TaskQueue) TaskQueue {
	now := s.nextWorkspaceQueueTime()
	queuedAt, attempt := now, 1
	if before != nil && before.Key == workspaceQueueKey {
		queuedAt = before.QueuedAt
		attempt = before.Attempt + 1
		retry = before.Retry
	}

	return TaskQueue{
		Key:      workspaceQueueKey,
		Reason:   s.workspaceWaitReason(ctx, machine, cwd, ownerID, ownerTitle),
		Attempt:  attempt,
		QueuedAt: queuedAt,
		NextAt:   now,
		Retry:    retry,
		Workspace: &taskWorkspaceWait{
			Machine:    machine,
			Path:       cwd,
			OwnerID:    ownerID,
			OwnerTitle: ownerTitle,
		},
	}
}

// workspaceAfterWaitEntry builds a TaskQueue entry for a workspace-queued task whose target workspace is now free
// while one or more after prerequisites are still unsatisfied.
func (s *Service) workspaceAfterWaitEntry(ctx context.Context, machine, cwd string, blockers []string, before *TaskQueue) TaskQueue {
	now := s.nextWorkspaceQueueTime()
	queuedAt, attempt, retry := now, 1, false
	if before != nil && before.Key == workspaceQueueKey {
		queuedAt, attempt, retry = before.QueuedAt, max(1, before.Attempt), before.Retry
	}
	reason := i18n.T(s.lang(ctx), "assistant-tasks_workspace.queued_after", i18n.Args{
		"blockers": strings.Join(blockers, ", "),
		"machine":  machine,
		"path":     cwd,
	})
	return TaskQueue{
		Key:      workspaceQueueKey,
		Reason:   reason,
		Attempt:  attempt,
		QueuedAt: queuedAt,
		NextAt:   now,
		Retry:    retry,
		Blockers: blockers,
		Workspace: &taskWorkspaceWait{
			Machine: machine,
			Path:    cwd,
		},
	}
}

// workspaceQueuePending reports whether todo currently has a pending workspace-queued start in its bound plan.
func (s *Service) workspaceQueuePending(ctx context.Context, todo Todo) bool {
	plan, index, err := s.currentPlanAssignment(ctx, todo)
	return err == nil && plan.Tasks[index].Queue != nil && plan.Tasks[index].Queue.Key == workspaceQueueKey
}

func (s *Service) isWorkspaceDispatching(todoID string) bool {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	return s.tasks.workspaceDispatching[todoID]
}

func (s *Service) setWorkspaceDispatching(todoID string, active bool) {
	s.tasks.mu.Lock()
	if active {
		if s.tasks.workspaceDispatching == nil {
			s.tasks.workspaceDispatching = map[string]bool{}
		}
		s.tasks.workspaceDispatching[todoID] = true
	} else {
		delete(s.tasks.workspaceDispatching, todoID)
	}
	s.tasks.mu.Unlock()
}

// preclaimWorkspaceLocked reserves (machine, cwd) in workspaceClaims for todoID if no other task occupies it.
func (s *Service) preclaimWorkspace(ctx context.Context, machine, cwd, todoID string) bool {
	s.tasks.workspaceMu.Lock()
	defer s.tasks.workspaceMu.Unlock()
	if owner := s.taskWorkspaceOwner(ctx, machine, cwd, todoID, false); owner != nil {
		return false
	}
	claim := s.effectiveBoardMachine(machine) + "\x00" + s.taskDirKey(ctx, machine)(cwd)
	s.tasks.mu.Lock()
	if s.tasks.workspaceClaims == nil {
		s.tasks.workspaceClaims = map[string]string{}
	}
	s.tasks.workspaceClaims[claim] = todoID
	s.tasks.mu.Unlock()
	return true
}

// queueWorkspaceBusy parks task (planID, i) in the durable workspace queue when admission observes occupancy.
// After persisting, it immediately re-checks whether the workspace became free during the handoff so a concurrent
// completion of the occupying task can never be missed.
func (s *Service) queueWorkspaceBusy(planID string, i int, t PlanTask, busy *workspaceBusyQueueError, retry bool) {
	ctx, cancel := context.WithTimeout(context.Background(), taskFailTimeout)
	defer cancel()
	if todo, ok := s.todoNow(t.TodoID); !ok || todo.DoneAt != nil || todo.Cancelled() || todo.StartError != "" {
		s.clearAdmissionQueue(ctx, planID, i, t.TodoID)
		return
	}
	if err := s.patchPlanTask(ctx, planID, i, func(pt *PlanTask) {
		if pt.TodoID != t.TodoID {
			return
		}
		q := s.workspaceQueueEntry(ctx, busy.machine, busy.path, busy.ownerID, busy.ownerTitle, retry, pt.Queue)
		pt.Queue = &q
		pt.Placement = queuedPlacement(t, &q)
		pt.Error, pt.ErrorCode, pt.ErrorArgs = "", "", nil
	}); err != nil {
		s.logf("task %s: workspace queue: persist: %v", t.TodoID, err)
		return
	}
	s.logf("task %s: queued for workspace %s on %s behind %s (%s)", t.TodoID, busy.path, busy.machine, busy.ownerID, busy.ownerTitle)
	// Re-check after persisting in case the occupying task finished between admission and persistence.
	if s.taskWorkspaceOwner(ctx, busy.machine, busy.path, "", false) == nil {
		s.drainWorkspaceQueue(ctx, busy.machine, busy.path)
	}
}

// clearWorkspaceQueueEntry removes a workspace queue entry from a started plan task when its to-do has ended or
// been cancelled, removing the JSON key so TaskPlansQueued stops scanning the plan.
func (s *Service) clearWorkspaceQueueEntry(ctx context.Context, planID string, index int, todoID string) {
	if err := s.patchPlanTask(ctx, planID, index, func(pt *PlanTask) {
		if pt.TodoID == todoID && pt.Queue != nil && pt.Queue.Key == workspaceQueueKey {
			pt.Queue = nil
		}
	}); err != nil {
		s.logf("task %s: workspace queue: clear: %v", todoID, err)
	}
}

type workspaceQueueCandidate struct {
	plan  *TaskPlan
	index int
	task  PlanTask
	todo  Todo
}

// drainWorkspaceQueue deterministically dequeues and auto-starts at most one eligible FIFO task waiting on
// (machine, cwd) once the workspace is free and that task's after prerequisites are satisfied.
func (s *Service) drainWorkspaceQueue(ctx context.Context, machine, cwd string) {
	if strings.TrimSpace(machine) == "" || strings.TrimSpace(cwd) == "" {
		return
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
	defer cancel()
	if err := s.ensureTodosLoaded(dctx); err != nil {
		s.logf("workspace queue: load todos: %v", err)
		return
	}

	s.tasks.workspaceQueueMu.Lock()
	defer s.tasks.workspaceQueueMu.Unlock()

	effMachine := s.effectiveBoardMachine(machine)
	dirKeyFn := s.taskDirKey(dctx, machine)
	targetDirKey := dirKeyFn(cwd)

	var waiters []workspaceQueueCandidate
	for _, p := range s.queueCandidatePlans(dctx) {
		if p.State != planStarted {
			continue
		}
		for i := range p.Tasks {
			pt := p.Tasks[i]
			q := pt.Queue
			if pt.TodoID == "" || pt.Error != "" || q == nil || q.Key != workspaceQueueKey || q.Workspace == nil {
				continue
			}
			if s.effectiveBoardMachine(q.Workspace.Machine) != effMachine ||
				s.taskDirKey(dctx, q.Workspace.Machine)(q.Workspace.Path) != targetDirKey {
				continue
			}
			todo, ok := s.todoNow(pt.TodoID)
			if !ok || todo.DoneAt != nil || todo.Cancelled() || todo.StartError != "" || todoHasActiveAgent(todo) {
				s.clearWorkspaceQueueEntry(dctx, p.ID, i, pt.TodoID)
				continue
			}
			if q.Claimed {
				if s.isWorkspaceDispatching(pt.TodoID) {
					return // an auto-start for this workspace is already in flight in this process
				}
				// Claimed by an earlier process before a crash or by a peer hub: never replay automatically.
				continue
			}
			pt.planID, pt.planIndex = p.ID, i
			waiters = append(waiters, workspaceQueueCandidate{plan: p, index: i, task: pt, todo: todo})
		}
	}
	if len(waiters) == 0 {
		return
	}

	if owner := s.taskWorkspaceOwner(dctx, effMachine, cwd, "", false); owner != nil {
		// Refresh waiters whose displayed owner is stale (e.g. a predecessor handed off to owner).
		for _, w := range waiters {
			if w.task.Queue.Workspace.OwnerID != owner.ID || len(w.task.Queue.Blockers) > 0 {
				updated := s.workspaceQueueEntry(dctx, effMachine, w.task.Queue.Workspace.Path, owner.ID, owner.Title, w.task.Queue.Retry, w.task.Queue)
				updated.Attempt = w.task.Queue.Attempt // display refresh is not a new refusal attempt
				s.replaceTaskQueue(dctx, w.task, &updated, queueCAS)
			}
		}
		return
	}

	sort.Slice(waiters, func(i, j int) bool {
		a, b := waiters[i], waiters[j]
		if !a.task.Queue.QueuedAt.Equal(b.task.Queue.QueuedAt) {
			return a.task.Queue.QueuedAt.Before(b.task.Queue.QueuedAt)
		}
		if !a.plan.CreatedAt.Equal(b.plan.CreatedAt) {
			return a.plan.CreatedAt.Before(b.plan.CreatedAt)
		}
		if a.plan.ID != b.plan.ID {
			return a.plan.ID < b.plan.ID
		}
		if a.index != b.index {
			return a.index < b.index
		}
		return a.task.TodoID < b.task.TodoID
	})

	for idx, w := range waiters {
		readiness, err := s.taskTodoReadiness(dctx, w.todo, w.task)
		if err != nil {
			s.logf("task %s: workspace queue: read prerequisites: %v", w.task.TodoID, err)
			continue
		}
		if !readiness.ready() {
			s.saveApprovedPRBlockers(dctx, w.todo.ID, readiness.missingPRs, false)
			blockers := readiness.refs()
			if len(readiness.after) == 0 {
				q := s.blockerWaitQueue(dctx, blockers)
				q.Retry = w.task.Queue.Retry
				s.replaceTaskQueue(dctx, w.task, q, queueCAS)
				continue
			}
			if w.task.Queue.Workspace.OwnerID != "" || !slices.Equal(w.task.Queue.Blockers, blockers) {
				updated := s.workspaceAfterWaitEntry(dctx, effMachine, w.task.Queue.Workspace.Path, blockers, w.task.Queue)
				s.replaceTaskQueue(dctx, w.task, &updated, queueCAS)
			}
			continue
		}
		if !s.preclaimWorkspace(dctx, effMachine, w.task.Queue.Workspace.Path, w.task.TodoID) {
			return // workspace was claimed concurrently
		}
		claimed := *w.task.Queue
		claimed.Claimed = true
		claimed.Blockers = nil
		claimed.NextAt = time.Now().UTC()
		if !s.replaceTaskQueue(dctx, w.task, &claimed, queueCAS) {
			s.releaseWorkspaceClaimsOnly(w.task.TodoID)
			return
		}
		w.task.Queue = &claimed
		s.setWorkspaceDispatching(w.task.TodoID, true)
		s.logf("task %s: workspace %s on %s is free; dequeuing FIFO waiter", w.task.TodoID, w.task.Queue.Workspace.Path, effMachine)
		// Update remaining waiters on the same workspace so their status points to the newly started task.
		for _, rest := range waiters[idx+1:] {
			updated := s.workspaceQueueEntry(dctx, effMachine, rest.task.Queue.Workspace.Path, w.task.TodoID, w.todo.Title, rest.task.Queue.Retry, rest.task.Queue)
			updated.Attempt = rest.task.Queue.Attempt
			s.replaceTaskQueue(dctx, rest.task, &updated, queueCAS)
		}
		todoID := w.task.TodoID
		s.startPlanTaskAsync(w.plan.ID, w.plan.Conversation, w.index, w.task, claimed.Retry, func() {
			s.setWorkspaceDispatching(todoID, false)
		})
		return
	}
}

// onTodoWorkspaceReleased handles a to-do that completed, was accepted/cancelled, failed, or was deleted:
//   - clears its own workspace queue entry if it was cancelled/closed before starting,
//   - drains the FIFO workspace queue for every workspace it occupied, and
//   - wakes any workspace-queued task waiting on it as an after prerequisite when maySatisfyAfter is true.
func (s *Service) onTodoWorkspaceReleased(ctx context.Context, before Todo, maySatisfyAfter bool) {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
	defer cancel()

	// Release any in-memory admission claim held by this task without re-entering drain.
	claimed := s.releaseWorkspaceClaimsOnly(before.ID)

	plans := s.queueCandidatePlans(rctx)
	var hasWorkspaceQueue bool
	for _, p := range plans {
		if p.State != planStarted {
			continue
		}
		for i, pt := range p.Tasks {
			if pt.Queue == nil || pt.Queue.Key != workspaceQueueKey {
				continue
			}
			hasWorkspaceQueue = true
			if pt.TodoID == before.ID {
				s.clearWorkspaceQueueEntry(rctx, p.ID, i, before.ID)
			}
		}
	}
	if !hasWorkspaceQueue {
		return
	}

	seen := map[string]bool{}
	var targets []workspaceTarget
	addTarget := func(machine, cwd string) {
		if strings.TrimSpace(machine) == "" || strings.TrimSpace(cwd) == "" {
			return
		}
		eff := s.effectiveBoardMachine(machine)
		key := eff + "\x00" + s.taskDirKey(rctx, machine)(cwd)
		if seen[key] {
			return
		}
		seen[key] = true
		targets = append(targets, workspaceTarget{machine: eff, path: cwd})
	}
	for _, a := range before.Agents {
		if a.ReleasedAt == nil && a.DismissedAt == nil {
			addTarget(a.MachineID, a.Cwd)
		}
	}
	for _, c := range claimed {
		addTarget(c.machine, c.path)
	}
	if maySatisfyAfter && before.ID != "" {
		for _, p := range plans {
			if p.State != planStarted {
				continue
			}
			for _, pt := range p.Tasks {
				if pt.Queue == nil || pt.Queue.Key != workspaceQueueKey || pt.Queue.Workspace == nil || pt.TodoID == before.ID {
					continue
				}
				after := cleanIDs(pt.After)
				if td, ok := s.todoNow(pt.TodoID); ok {
					after = cleanIDs(slices.Concat(after, td.After))
				}
				if slices.Contains(after, before.ID) {
					addTarget(pt.Queue.Workspace.Machine, pt.Queue.Workspace.Path)
				}
			}
		}
	}
	for _, target := range targets {
		s.drainWorkspaceQueue(rctx, target.machine, target.path)
	}
}

// drainWorkspaceQueuesForPrereq checks workspace-queued tasks whose after list names prereqID (e.g. after a
// prerequisite PR transitions to MERGED).
func (s *Service) drainWorkspaceQueuesForPrereq(ctx context.Context, prereqID string) {
	prereqID = strings.TrimSpace(prereqID)
	if prereqID == "" {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
	defer cancel()
	seen := map[string]bool{}
	var targets []workspaceTarget
	for _, p := range s.queueCandidatePlans(rctx) {
		if p.State != planStarted {
			continue
		}
		for _, pt := range p.Tasks {
			if pt.Queue == nil || pt.Queue.Key != workspaceQueueKey || pt.Queue.Workspace == nil || pt.TodoID == "" {
				continue
			}
			after := cleanIDs(pt.After)
			if td, ok := s.todoNow(pt.TodoID); ok {
				after = cleanIDs(slices.Concat(after, td.After))
			}
			if !slices.Contains(after, prereqID) {
				continue
			}
			eff := s.effectiveBoardMachine(pt.Queue.Workspace.Machine)
			key := eff + "\x00" + s.taskDirKey(rctx, pt.Queue.Workspace.Machine)(pt.Queue.Workspace.Path)
			if seen[key] {
				continue
			}
			seen[key] = true
			targets = append(targets, workspaceTarget{machine: eff, path: pt.Queue.Workspace.Path})
		}
	}
	for _, target := range targets {
		s.drainWorkspaceQueue(rctx, target.machine, target.path)
	}
}

// sweepWorkspaceQueues reconciles all persisted workspace-queued tasks (cleaning up cancelled/ended ones and
// starting ready FIFO waiters after a hub restart).
func (s *Service) sweepWorkspaceQueues(ctx context.Context) {
	seen := map[string]bool{}
	var targets []workspaceTarget
	for _, p := range s.queueCandidatePlans(ctx) {
		if p.State != planStarted {
			continue
		}
		for i, pt := range p.Tasks {
			if pt.Queue == nil || pt.Queue.Key != workspaceQueueKey {
				continue
			}
			if pt.Queue.Workspace == nil {
				s.clearWorkspaceQueueEntry(ctx, p.ID, i, pt.TodoID)
				continue
			}
			eff := s.effectiveBoardMachine(pt.Queue.Workspace.Machine)
			key := eff + "\x00" + s.taskDirKey(ctx, pt.Queue.Workspace.Machine)(pt.Queue.Workspace.Path)
			if seen[key] {
				continue
			}
			seen[key] = true
			targets = append(targets, workspaceTarget{machine: eff, path: pt.Queue.Workspace.Path})
		}
	}
	for _, target := range targets {
		s.drainWorkspaceQueue(ctx, target.machine, target.path)
	}
}

// drainReleasedWorkspaces drains any workspace queues for keys just released from workspaceClaims.
func (s *Service) drainReleasedWorkspaces(released []workspaceTarget) {
	if len(released) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), taskFailTimeout)
	defer cancel()
	for _, target := range released {
		s.drainWorkspaceQueue(ctx, target.machine, target.path)
	}
}
