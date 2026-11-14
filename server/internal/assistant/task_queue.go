package assistant

import (
	"context"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The admission queue. A start the admission gate refuses because the machine is briefly full (a stale 30-second
// sample, a load spike, the direction cap) is not a failed start: the task stays open, its card reads as waiting with
// the gate's reason, and this loop retries it on a timer until it starts or the bounds below are reached. Only the
// hub retries these; no supervisor turn is involved.

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
)

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
}

// queueAdmissionRefusal parks a task the admission gate refused instead of failing it. The claim held by the start
// is released in runPlanTaskStart's defer, so a queued task holds no machine or direction slot while it waits.
func (s *Service) queueAdmissionRefusal(planID string, i int, t PlanTask, err error) {
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
	if perr := s.patchPlanTask(ctx, planID, i, func(pt *PlanTask) {
		q := &TaskQueue{Reason: reason, Key: key, Attempt: 1, QueuedAt: now, NextAt: now.Add(admissionQueueInterval)}
		if pt.Queue != nil { // refused again after a queued retry: keep the first QueuedAt and count on
			q.QueuedAt = pt.Queue.QueuedAt
			q.Attempt = pt.Queue.Attempt + 1
		}
		pt.Queue = q
		pt.Error = ""
		pt.ErrorCode, pt.ErrorArgs = "", nil
	}); perr != nil {
		s.logf("task %s: admission queue: persist: %v", t.TodoID, perr)
	}
	s.logf("task %s: admission queued (attempt %d): %s", t.TodoID, queueAttempt(t), reason)
}

func queueAttempt(t PlanTask) int {
	if t.Queue != nil {
		return t.Queue.Attempt
	}
	return 1
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
			if q == nil || q.NextAt.After(now) {
				continue
			}
			todo, ok := s.todoNow(pt.TodoID)
			if !ok || todo.DoneAt != nil || todo.Cancelled() {
				if err := s.patchPlanTask(cctx, p.ID, i, func(t *PlanTask) { t.Queue = nil }); err != nil {
					s.logf("task %s: admission queue: clear ended task: %v", pt.TodoID, err)
				}
				continue
			}
			if q.Attempt >= admissionQueueMaxAttempts || now.Sub(q.QueuedAt) >= admissionQueueMaxWait {
				s.failQueuedStart(cctx, p, i, pt, q)
				continue
			}
			s.retryQueuedStart(cctx, p, i, pt)
		}
	}
}

// retryQueuedStart starts the task again through the ordinary async start path, which re-runs the gate and either
// links the agent or parks the task again. The queue record is kept (not cleared) with NextAt pushed past the max wait
// so the sweep cannot start it twice while this attempt is in flight: success clears it in runPlanTaskStart, another
// refusal parks it again with the attempt counted on.
func (s *Service) retryQueuedStart(ctx context.Context, p *TaskPlan, i int, pt PlanTask) {
	if err := s.patchPlanTask(ctx, p.ID, i, func(t *PlanTask) {
		if t.Queue != nil {
			t.Queue.NextAt = time.Now().Add(admissionQueueMaxWait)
		}
	}); err != nil {
		s.logf("task %s: admission queue: mark retry in flight: %v", pt.TodoID, err)
		return
	}
	s.logf("task %s: admission queue: retrying start on %s", pt.TodoID, pt.Machine)
	s.startPlanTaskAsync(p.ID, p.Conversation, i, pt, true, nil)
}

// clearAdmissionQueue drops a task's queue marker once a retry has started its agent for good.
func (s *Service) clearAdmissionQueue(ctx context.Context, planID string, i int, todoID string) {
	if err := s.patchPlanTask(ctx, planID, i, func(t *PlanTask) {
		if t.Queue != nil {
			t.Queue = nil
		}
	}); err != nil {
		s.logf("task %s: admission queue: clear after start: %v", todoID, err)
	}
}

// failQueuedStart ends a task that waited past the queue bounds: the todo fails and the ordinary failure receipt goes
// out once, naming the last gate reason so the owner knows what was full.
func (s *Service) failQueuedStart(ctx context.Context, p *TaskPlan, i int, pt PlanTask, q *TaskQueue) {
	reason := i18n.T(s.lang(ctx), "assistant-tasks.admission_queue_timeout", i18n.Args{
		"limit": admissionQueueMaxWait.Round(time.Minute).String(), "reason": q.Reason})
	if err := s.failTodo(ctx, pt.TodoID, reason); err != nil {
		s.logf("task %s: admission queue: fail: %v", pt.TodoID, err)
	}
	if err := s.patchPlanTask(ctx, p.ID, i, func(t *PlanTask) {
		t.Queue = nil
		t.Error = reason
	}); err != nil {
		s.logf("plan %s: task %d's queue timeout: %v", p.ID, i, err)
	}
	s.tasks.mu.Lock()
	s.tasks.receipted[pt.TodoID] = time.Now()
	s.tasks.mu.Unlock()
	if _, err := s.appendFeed(ctx, p.Conversation, feedReceipt, pt.TodoID, receiptBody{Title: pt.Title, State: "failed",
		Summary: i18n.T(s.lang(ctx), "assistant-tasks.failed_start", i18n.Args{"reason": reason})}); err != nil {
		s.logf("task %s: admission queue: receipt: %v", pt.TodoID, err)
	}
	if todo, ok := s.todoNow(pt.TodoID); ok {
		s.taskNotice(ctx, todo, pt.Machine, "failed", "", reason)
		s.taskReport(todo, reportFailed, pt.Machine, [2]string{}, reason, nil)
	}
	s.logf("task %s: admission queue: gave up after %d attempt(s)", pt.TodoID, q.Attempt)
}

// taskQueueStore lists recently updated or durably queued plans whose task blobs may hold admission queue records.
type taskQueueStore interface {
	TaskPlansQueued(ctx context.Context, since time.Time) ([]db.TaskPlanRow, error)
}

var _ taskQueueStore = (*db.DB)(nil)

// queueCandidatePlans returns recently updated or durably queued started plans, from the database or the in-memory
// store, without returning unrelated historical plans. Queue writes refresh updated_at; created_at may be older.
func (s *Service) queueCandidatePlans(ctx context.Context) []*TaskPlan {
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
		return out
	}
	ev, ok := st.(taskQueueStore)
	if !ok {
		return nil
	}
	rows, err := ev.TaskPlansQueued(ctx, time.Now().Add(-admissionQueueLookback))
	if err != nil {
		s.logf("admission queue: list plans: %v", err)
		return nil
	}
	plans := make([]*TaskPlan, 0, len(rows))
	for _, row := range rows {
		plans = append(plans, planFromRow(row))
	}
	return plans
}
