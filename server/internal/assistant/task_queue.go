package assistant

import (
	"cmp"
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The admission queue. A start the admission gate refuses because the machine is briefly full (a stale 30-second
// sample, a load spike, the direction cap) is not a failed start: the task stays open, its card reads as waiting with
// the gate's reason, and this loop retries it on a timer until it starts or the bounds below are reached. Only the
// hub retries these; no supervisor turn is involved. A full launcher group (admissionGroupFullKey) has no bounds: the
// task waits for the shared pool as long as it takes, and the bounds apply again from the moment another line holds it.

const (
	// admissionQueueSweep is how often the loop looks for due retries.
	admissionQueueSweep = time.Minute
	// admissionQueueInterval is the wait between a refusal and the next start attempt.
	admissionQueueInterval = 90 * time.Second
	// admissionQueueMaxAttempts bounds the number of automatic retries regardless of how short each wait was.
	admissionQueueMaxAttempts = 10
	// admissionQueueMaxWait bounds the total time a task may spend queued before its start fails for good.
	admissionQueueMaxWait = 30 * time.Minute
	// admissionQueueLookback bounds the recent-update side of the database scan; a serialized queue survives downtime.
	admissionQueueLookback = time.Hour
	// blockerWaitQueueKey marks an approved task waiting for prerequisites.
	blockerWaitQueueKey = "todo_blockers"
)

func (s *Service) blockerWaitQueue(ctx context.Context, refs []string) *TaskQueue {
	now := time.Now().UTC()
	reason := i18n.T(s.lang(ctx), "assistant-tasks.checking_prerequisites")
	if len(refs) > 0 {
		reason = i18n.T(s.lang(ctx), "assistant-tasks.waiting_for_prerequisites", i18n.Args{"blockers": strings.Join(refs, ", ")})
	}
	return &TaskQueue{
		Key:      blockerWaitQueueKey,
		Reason:   reason,
		QueuedAt: now,
		NextAt:   now,
		Blockers: append([]string(nil), refs...),
	}
}

// TaskQueue records a plan task waiting for an admission-gate retry. It is serialized inside the plan's task blob, so
// a hub restart resumes the wait: the loop reads NextAt back and retries when it is due.
type TaskQueue struct {
	// Reason is the last gate refusal rendered in the owner's language, shown on the task's card while it waits.
	Reason string `json:"reason"`
	// Key is the refusal's stable i18n key (admission_load, admission_stale…), empty when it cannot be resolved.
	Key string `json:"key,omitempty"`
	// Attempt is how many refusals this queueing stretch has seen.
	Attempt int `json:"attempt"`
	// QueuedAt is when the first refusal of this stretch happened.
	QueuedAt time.Time `json:"queued_at"`
	// NextAt is when the queue loop may start the task again.
	NextAt time.Time `json:"next_at"`
	// Retry: the start this record holds was itself a retry (the owner's Retry of a failed start, or task_retry), so
	// its first failure was told already. The queue loop starts it again as the same kind of start; a first start
	// that waited here and then fails is told as a first start's failure would be.
	Retry     bool               `json:"retry,omitempty"`
	Claimed   bool               `json:"claimed,omitempty"`
	Blockers  []string           `json:"blockers,omitempty"`
	Gateway   *taskGatewayWait   `json:"gateway_wait,omitempty"`
	Workspace *taskWorkspaceWait `json:"workspace_wait,omitempty"`
	Placement *taskPlacementWait `json:"placement_wait,omitempty"`
}

// queueAdmissionRefusal parks a task the admission gate refused instead of failing it. The claim held by the start
// is released in runPlanTaskStart's defer, so a queued task holds no machine or direction slot while it waits.
func (s *Service) queueAdmissionRefusal(planID string, i int, t PlanTask, err error, retry bool) {
	ctx, cancel := context.WithTimeout(context.Background(), taskFailTimeout)
	defer cancel()
	var denied *admissionDeniedError
	if !errors.As(err, &denied) {
		return // only gate refusals queue; other failures keep the ordinary failed-start path
	}
	now := time.Now().UTC()
	reason := denied.msg.Text(s.lang(ctx))
	key := ""
	if denied.reason != nil {
		key = denied.reason.Key
	}
	if t.claimedGatewayStart() {
		// A peer can finish the task and clear this claim during the start's
		// preflight. Only the exact claim may become an ordinary resource wait.
		q := admissionQueueEntry(now, reason, key, retry, t.Queue)
		if s.replaceTaskQueue(ctx, t, &q, queueQuotaStart) {
			s.logf("task %s: admission queued (attempt %d): %s", t.TodoID, q.Attempt, reason)
		}
		return
	}
	attempt := 1
	if perr := s.patchPlanTask(ctx, planID, i, func(pt *PlanTask) {
		q := admissionQueueEntry(now, reason, key, retry, pt.Queue)
		attempt = q.Attempt
		pt.Queue = &q
		pt.Placement = queuedPlacement(t, &q)
		pt.Error = ""
		pt.ErrorCode, pt.ErrorArgs = "", nil
	}); perr != nil {
		s.logf("task %s: admission queue: persist: %v", t.TodoID, perr)
	}
	s.logf("task %s: admission queued (attempt %d): %s", t.TodoID, attempt, reason)
}

func admissionQueueEntry(now time.Time, reason, key string, retry bool, before *TaskQueue) TaskQueue {
	q := TaskQueue{Reason: reason, Key: key, Attempt: 1, QueuedAt: now, NextAt: now.Add(admissionQueueInterval), Retry: retry}
	if before != nil && before.Key != requestGateKey && before.Key != workspaceQueueKey && before.Key != pendingPlacementQueueKey && before.Key != blockerWaitQueueKey {
		q.QueuedAt, q.Attempt, q.Retry, q.Gateway = before.QueuedAt, before.Attempt+1, before.Retry, before.Gateway
		// Group and quota waits have separate budgets. A new resource refusal starts a new stretch.
		if before.Key == gatewayQuotaWaitKey || before.Key == admissionGroupFullKey && key != admissionGroupFullKey {
			q.QueuedAt, q.Attempt = now, 1
		}
	}
	return q
}

// admissionQueueLoop retries queued starts until they pass the gate or exceed the queue bounds.
func (s *Service) admissionQueueLoop(ctx context.Context) {
	ticker := time.NewTicker(admissionQueueSweep)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepAdmissionQueue(ctx)
		}
	}
}

// sweepAdmissionQueue runs one pass: due retries start again, over-bounds waits fail for good, and tasks whose todo
// ended while queued simply drop their queue.
func (s *Service) sweepAdmissionQueue(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, taskFailTimeout)
	defer cancel()
	if err := s.ensureTodosLoaded(cctx); err != nil {
		s.logf("admission queue: load todos: %v", err)
		return
	}
	now := time.Now().UTC()
	for _, p := range s.queueCandidatePlans(cctx) {
		if p.State != planStarted {
			continue
		}
		for i := range p.Tasks {
			pt := p.Tasks[i]
			q := pt.Queue
			if q == nil {
				continue
			}
			if q.Key == workspaceQueueKey || q.Key == pendingPlacementQueueKey {
				continue
			}
			if q.Gateway != nil {
				s.retryGatewayWait(cctx, p, i, pt, now)
				continue
			}
			if q.Claimed || q.NextAt.After(now) {
				continue
			}
			todo, ok := s.todoNow(pt.TodoID)
			if !ok || todo.DoneAt != nil || todo.Cancelled() || pt.RequestGate != "" && todo.ReopenedAt != nil && todo.ReopenedAt.After(q.QueuedAt) {
				if err := s.patchPlanTask(cctx, p.ID, i, func(t *PlanTask) { t.Queue = nil }); err != nil {
					s.logf("task %s: admission queue: clear ended task: %v", pt.TodoID, err)
				}
				continue
			}
			readiness, err := s.taskTodoReadiness(cctx, todo, pt)
			if err != nil {
				s.logf("task %s: admission queue: read prerequisites: %v", pt.TodoID, err)
				continue
			}
			if len(readiness.missingPRs) > 0 {
				s.saveApprovedPRBlockers(cctx, todo.ID, readiness.missingPRs, false)
				continue
			}
			if pt.RequestGate != "" {
				s.startPlanTaskAsync(p.ID, p.Conversation, i, pt, q.Retry, nil)
				continue
			}
			if !readiness.ready() {
				continue
			}
			if queueExpired(q, now) {
				s.failQueuedStart(cctx, p, i, pt, q)
				continue
			}
			s.retryQueuedStart(cctx, p, i, pt)
		}
	}
	s.sweepWorkspaceQueues(cctx)
}

// queueBounded says the generic attempt and time bounds apply. Group, prerequisite, quota, workspace, placement and blocker waits manage their own.
func queueBounded(q *TaskQueue) bool {
	return q.Key != admissionGroupFullKey && q.Key != requestGateKey && q.Key != gatewayQuotaWaitKey && q.Key != workspaceQueueKey && q.Key != pendingPlacementQueueKey && q.Key != blockerWaitQueueKey
}

func queueExpired(q *TaskQueue, now time.Time) bool {
	return queueBounded(q) && (q.Attempt >= admissionQueueMaxAttempts || now.Sub(q.QueuedAt) >= admissionQueueMaxWait)
}

// retryQueuedStart starts the task again through the ordinary async start path, which re-runs the gate and either
// links the agent or parks the task again. The queue record is kept (not cleared) with NextAt pushed past the max wait
// so the sweep cannot start it twice while this attempt is in flight: success clears it in runPlanTaskStart, another
// refusal parks it again with the attempt counted on.
func (s *Service) retryQueuedStart(ctx context.Context, p *TaskPlan, i int, pt PlanTask) {
	pt.planID, pt.planIndex = p.ID, i
	next := *pt.Queue
	next.NextAt = time.Now().Add(admissionQueueMaxWait)
	if !s.replaceTaskQueue(ctx, pt, &next, queueCAS) {
		return
	}
	pt.Queue = &next
	s.logf("task %s: admission queue: retrying start on %s", pt.TodoID, pt.Machine)
	// The same kind of start the gate refused: a first start that now fails past the gate is told as one (#8092).
	s.startPlanTaskAsync(p.ID, p.Conversation, i, pt, pt.Queue != nil && pt.Queue.Retry, nil)
}

// clearAdmissionQueue drops a task's queue marker once a retry got past the gate: its agent started for good, or its
// start failed for another reason.
func (s *Service) clearAdmissionQueue(ctx context.Context, planID string, i int, todoID string) {
	if err := s.patchPlanTask(ctx, planID, i, func(t *PlanTask) {
		if t.Queue != nil {
			t.Queue = nil
		}
	}); err != nil {
		s.logf("task %s: admission queue: clear past the gate: %v", todoID, err)
	}
}

// failQueuedStart ends a task that waited past the queue bounds: the todo fails and the ordinary failure receipt goes
// out once, naming the last gate reason so the owner knows what was full. A task_retry that waited here is given up
// as the other failed starts give it up: its start reads failed, so a later task_retry reopens the task in a new tab.
func (s *Service) failQueuedStart(ctx context.Context, p *TaskPlan, i int, pt PlanTask, q *TaskQueue) {
	reason := i18n.T(s.lang(ctx), "assistant-tasks.admission_queue_timeout", i18n.Args{
		"limit": admissionQueueMaxWait.Round(time.Minute).String(), "reason": q.Reason})
	remedied := pt.Start != nil && pt.Start.State == remedying
	if err := s.failTodo(ctx, pt.TodoID, reason); err != nil {
		s.logf("task %s: admission queue: fail: %v", pt.TodoID, err)
	}
	if err := s.patchPlanTask(ctx, p.ID, i, func(t *PlanTask) {
		t.Queue = nil
		t.Error = reason
		if remedied && t.Start != nil {
			t.Start.State, t.Start.Pane, t.Start.At = remedyFailed, "", time.Now().UTC()
		}
	}); err != nil {
		s.logf("plan %s: task %d's queue timeout: %v", p.ID, i, err)
	}
	s.tasks.mu.Lock()
	s.tasks.receipted[pt.TodoID] = time.Now()
	s.tasks.mu.Unlock()
	if remedied {
		s.remedyStep(ctx, p.Conversation, pt.TodoID, startRemedyBody{Action: "gave_up", By: "hub", Attempt: pt.Start.Attempt, Reason: reason})
	}
	if _, err := s.appendFeed(ctx, p.Conversation, feedReceipt, pt.TodoID, receiptBody{Title: pt.Title, State: "failed",
		Summary: i18n.T(s.lang(ctx), "assistant-tasks.failed_start", i18n.Args{"reason": reason})}); err != nil {
		s.logf("task %s: admission queue: receipt: %v", pt.TodoID, err)
	}
	if todo, ok := s.todoNow(pt.TodoID); ok {
		s.taskStartFailed(ctx, todo, pt.Machine, pt.Start, reason) // a retry's timeout is news even with the same reason (#6027)
		s.taskReport(todo, reportFailed, pt.Machine, [2]string{}, reason, nil)
	}
	s.logf("task %s: admission queue: gave up after %d attempt(s) (queue=%s): %s", pt.TodoID, q.Attempt, cmp.Or(q.Key, "admission"), q.Reason)
}

// taskQueueStore lists recently updated or durably queued plans whose task blobs may hold admission queue records.
type taskQueueStore interface {
	TaskPlansQueued(ctx context.Context, since time.Time) ([]db.TaskPlanRow, error)
}

var _ taskQueueStore = (*db.DB)(nil)

// queueCandidatePlans returns recently updated or durably queued started plans, from the database or the in-memory
// store, without returning unrelated historical plans. Queue writes refresh updated_at; created_at may be older.
func (s *Service) queueCandidatePlans(ctx context.Context) []*TaskPlan {
	plans, err := s.readQueueCandidatePlans(ctx)
	if err != nil {
		s.logf("admission queue: list plans: %v", err)
	}
	return plans
}

func (s *Service) readQueueCandidatePlans(ctx context.Context) ([]*TaskPlan, error) {
	st := s.store()
	if st == nil {
		s.tasks.mu.Lock()
		out := make([]*TaskPlan, 0, len(s.tasks.plans))
		for _, p := range s.tasks.plans {
			cp := *p
			cp.Tasks = append([]PlanTask(nil), p.Tasks...)
			out = append(out, &cp)
		}
		s.tasks.mu.Unlock()
		return out, nil
	}
	ev, ok := st.(taskQueueStore)
	if !ok {
		return nil, nil
	}
	rows, err := ev.TaskPlansQueued(ctx, time.Now().Add(-admissionQueueLookback))
	if err != nil {
		return nil, err
	}
	plans := make([]*TaskPlan, 0, len(rows))
	for _, row := range rows {
		plans = append(plans, planFromRow(row))
	}
	return plans, nil
}

// queuedTodoTasks uses the execution binding, including reused to-dos whose PlanID is empty
// or historical. A to-do with ambiguous bindings has no unique queued assignment.
func (s *Service) queuedTodoTasks(ctx context.Context) (map[string]*PlanTask, error) {
	plans, err := s.readQueueCandidatePlans(ctx)
	if err != nil {
		return nil, err
	}
	tasks := make(map[string]*PlanTask)
	for _, plan := range plans {
		if plan.State != planStarted {
			continue
		}
		for i := range plan.Tasks {
			task := &plan.Tasks[i]
			if task.TodoID == "" || task.Queue == nil {
				continue
			}
			if _, found := tasks[task.TodoID]; found {
				tasks[task.TodoID] = nil
			} else {
				tasks[task.TodoID] = task
			}
		}
	}
	return tasks, nil
}
