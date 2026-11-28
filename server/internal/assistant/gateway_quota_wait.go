package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

const gatewayQuotaWaitKey = "assistant-tasks.gateway_quota_wait"
const gatewayQuotaMaxWait = 30 * time.Minute

// GatewayQuotaPool is an explicit operator assertion about the shared account
// pool behind the named launchers in a group. Gateway IDs and provider names
// must agree with usage.gateway_accounts. Nothing is inferred from the engine.
type GatewayQuotaPool struct {
	Source   string `json:"source"`
	Provider string `json:"provider"`
}

func (p GatewayQuotaPool) valid() bool {
	return len(p.Source) <= 128 && p.Source != "" && strings.TrimSpace(p.Source) == p.Source &&
		strings.IndexFunc(p.Source, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.')
		}) < 0 && (p.Provider == "claude" || p.Provider == "codex")
}

type taskGatewayWait struct {
	Pool     GatewayQuotaPool `json:"pool"`
	Launcher string           `json:"launcher"`
	Since    time.Time        `json:"since"`
	Deadline time.Time        `json:"deadline"`
}

type gatewayQuotaWaitError struct {
	queue TaskQueue
}

func (e *gatewayQuotaWaitError) Error() string { return e.queue.Reason }

type gatewayQueueStore interface {
	UnstartedQueuedTodo(context.Context, string, string, time.Time) (bool, error)
	ClaimUnstartedTaskQueue(context.Context, string, int, string, time.Time, json.RawMessage, json.RawMessage, json.RawMessage, time.Time) (bool, error)
}

type gatewayQueueStateError struct{ cause error }

func (e *gatewayQueueStateError) Error() string { return e.cause.Error() }

var errGatewayTaskHandled = errors.New("task already handled before its quota-queued start")

func (t PlanTask) claimedGatewayStart() bool {
	return t.Queue != nil && t.Queue.Gateway != nil && t.Queue.Claimed
}

func (t PlanTask) startWithoutReplay() bool {
	return t.RequestGate != "" || t.claimedGatewayStart() || t.HandoffID != ""
}

func (s *Service) gatewayPool(ctx context.Context, launcher string) (GatewayQuotaPool, bool) {
	cfg := s.admissionSettings(ctx)
	if cfg.Enabled != nil && !*cfg.Enabled {
		return GatewayQuotaPool{}, false
	}
	if group, ok := cfg.launcherGroup(launcher); ok && group.QuotaPool != nil && group.QuotaPool.valid() {
		return *group.QuotaPool, true // an admission group's own pool wins over its channel's
	}
	return s.channelQuotaOf(ctx, launcher)
}

// deferGatewayStart runs for the first background launch only. When quota
// evidence suffices, confirm the launcher's actual fit without changing the
// selected machine, launcher or model, and before taking any admission claim.
func (s *Service) deferGatewayStart(ctx context.Context, task *PlanTask, m Device, own startPick, params map[string]any, retry bool) error {
	if s.devs == nil || retry || task.planID == "" || task.RequestGate != "" || task.ReuseTodo != "" || task.Queue != nil {
		return nil
	}
	launcher, _ := params["launcher"].(string)
	pool, ok := s.gatewayPool(ctx, launcher)
	if !ok {
		return nil
	}
	now := time.Now().UTC()
	wait := &taskGatewayWait{Pool: pool, Launcher: launcher, Since: now, Deadline: now.Add(gatewayQuotaMaxWait)}
	recovery := s.gatewayWaitRecovery(ctx, wait, now)
	if recovery.IsZero() {
		return nil
	}
	if ready, err := s.gatewayTaskReadyAt(ctx, *task, wait.Since); err != nil || !ready {
		return nil
	}
	candidate := *task
	fitted := maps.Clone(params)
	agent, err := s.fitTaskLauncher(ctx, &candidate, m, own, fitted)
	if err != nil || fitted["launcher"] != launcher || agent.snapshot != pool.Provider {
		return nil
	}
	return &gatewayQuotaWaitError{queue: s.gatewayWaiting(ctx, wait, now, now, recovery, 1)}
}

func (s *Service) gatewayWaitRecovery(ctx context.Context, wait *taskGatewayWait, now time.Time) time.Time {
	pool, ok := s.gatewayPool(ctx, wait.Launcher)
	if !ok || pool != wait.Pool || s.devs == nil || !now.Before(wait.Deadline) {
		return time.Time{}
	}
	recovery := s.devs.GatewayRetryAt(pool.Source, pool.Provider, now)
	if !recovery.After(now) || recovery.After(wait.Deadline) {
		return time.Time{}
	}
	return recovery
}

func (s *Service) gatewayWaiting(ctx context.Context, wait *taskGatewayWait, queuedAt, now, recovery time.Time, attempt int) TaskQueue {
	next := recovery
	if check := now.Add(admissionQueueInterval); check.Before(next) {
		next = check // availability, evidence or settings can change before the estimate
	}
	return TaskQueue{
		Key: gatewayQuotaWaitKey, Gateway: wait, QueuedAt: queuedAt, NextAt: next, Attempt: attempt,
		Reason: i18n.T(s.lang(ctx), gatewayQuotaWaitKey, i18n.Args{
			"time": recovery.In(s.location(ctx)).Format("Jan 2 15:04 MST"),
		}),
	}
}

// persistGatewayWait parks an initial refusal before there is any launch to
// reconcile. The ordinary started-plan update also delivers its waiting card.
func (s *Service) persistGatewayWait(ctx context.Context, task PlanTask, wait TaskQueue) error {
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
	defer cancel()
	return s.patchPlanTask(pctx, task.planID, task.planIndex, func(current *PlanTask) {
		if current.TodoID == task.TodoID && current.Queue == nil {
			current.Queue = &wait
			current.Placement = queuedPlacement(task, &wait)
			current.Error, current.ErrorCode, current.ErrorArgs = "", "", nil
		}
	})
}

// retryGatewayWait rechecks only the bound cache. It claims a release exactly
// once before handing it to the existing start path. A crash after that claim
// can leave an unconfirmed launch, which is never started again automatically.
func (s *Service) retryGatewayWait(ctx context.Context, plan *TaskPlan, i int, task PlanTask, now time.Time) {
	task.planID, task.planIndex = plan.ID, i
	queue := *task.Queue
	if queue.Gateway == nil {
		return
	}
	ready, err := s.gatewayTaskReadyAt(ctx, task, queue.Gateway.Since)
	if err != nil {
		s.logf("task %s: check queued quota launch: %v", task.TodoID, err)
		return
	}
	if !ready {
		s.replaceTaskQueue(ctx, task, nil, queueCAS)
		return
	}
	if queue.Claimed || queue.NextAt.After(now) {
		return
	}
	if queue.Key == gatewayQuotaWaitKey {
		if recovery := s.gatewayWaitRecovery(ctx, queue.Gateway, now); !recovery.IsZero() {
			next := s.gatewayWaiting(ctx, queue.Gateway, queue.QueuedAt, now, recovery, queue.Attempt+1)
			s.replaceTaskQueue(ctx, task, &next, queueCAS)
			return
		}
	}
	todo, _ := s.todoNow(task.TodoID)
	readiness, err := s.taskTodoReadiness(ctx, todo, task)
	if err != nil {
		s.logf("task %s: quota queue: read prerequisites: %v", task.TodoID, err)
		return
	}
	if !readiness.ready() {
		q := s.blockerWaitQueue(ctx, readiness.refs())
		q.Retry = queue.Retry
		s.replaceTaskQueue(ctx, task, q, queueCAS)
		return
	}
	queue.Claimed, queue.NextAt = true, now
	queue.Reason = i18n.T(s.lang(ctx), "assistant-tasks.request_unconfirmed")
	if !s.replaceTaskQueue(ctx, task, &queue, queueQuotaStart) {
		return
	}
	original := task.Queue
	task.Queue = &queue
	if queueExpired(original, now) {
		if ready, err := s.gatewayTaskReadyAt(ctx, task, queue.Gateway.Since); err == nil && ready {
			s.failQueuedStart(ctx, plan, i, task, original)
		}
		return
	}
	s.startPlanTaskAsync(plan.ID, plan.Conversation, i, task, queue.Retry, nil)
}

func (s *Service) gatewayTaskReadyAt(ctx context.Context, task PlanTask, since time.Time) (bool, error) {
	if st := s.store(); st != nil {
		if store, ok := st.(gatewayQueueStore); ok {
			return store.UnstartedQueuedTodo(ctx, task.TodoID, task.planID, since)
		}
		return false, errors.New("task store cannot verify the queued start")
	}
	todo, ok := s.todoNow(task.TodoID)
	return ok && unstartedTodo(todo) && todo.PlanID == task.planID &&
		(todo.ReopenedAt == nil || !todo.ReopenedAt.After(since)), nil
}

func (s *Service) checkClaimedGatewayStart(ctx context.Context, task PlanTask) error {
	if !task.claimedGatewayStart() {
		return nil
	}
	ready, err := s.gatewayTaskReadyAt(ctx, task, task.Queue.Gateway.Since)
	if err != nil {
		return &gatewayQueueStateError{cause: err}
	}
	if !ready {
		return errGatewayTaskHandled
	}
	return nil
}
