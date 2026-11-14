package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// ---- starting and dismissing a plan ----

var errPlanNotOpen = errors.New("this plan was already started or dismissed")

// planInvalid is a start the user's edits make impossible as they are (a model that is no model id): 400.
type planInvalid struct{ error }

// startPlan opens a todo for each task of the open plan id (edits, when given, replace the tasks' fields the user can
// change) and starts their agents in the background; remember lets this kind start without asking next time.
func (s *Service) startPlan(ctx context.Context, id string, edits []PlanTask, remember bool) (*TaskPlan, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return nil, err
	}
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.State != planOpen {
		return nil, errPlanNotOpen
	}
	for i := range p.Tasks {
		if i < len(edits) {
			applyPlanEdit(&p.Tasks[i], edits[i])
		}
		p.Tasks[i].normalizeReadonlyPermission()
		if err := p.Tasks[i].normalizeMachinePolicy(); err != nil {
			return nil, planInvalid{err}
		}
		if err := checkModelID(p.Tasks[i].Model); err != nil { // typed on the card (#1644): refused before anything starts
			return nil, planInvalid{err}
		}
		p.Tasks[i].Permission = strings.TrimSpace(p.Tasks[i].Permission)
		if m, ok := s.machineByName(ctx, p.Tasks[i].Machine); ok && p.Tasks[i].Role == roleLeader && s.isWorkMachine(m.ID) {
			return nil, planInvalid{fmt.Errorf("task %d: role \"leader\" cannot run on %s: a work-trust computer gives its agents only "+
				"the reporting tools, so it could not supervise workers. Put the task leader on a computer you own", i+1, m.Name)}
		}
		if !s.placeTask(ctx, &p.Tasks[i]) {
			return nil, i18n.Err("assistant-tasks.task_no_machine", i18n.Args{"task": p.Tasks[i].Title, "machine": strconv.Quote(p.Tasks[i].Machine)})
		}
		if err := s.resolveTaskWorkspace(ctx, &p.Tasks[i]); err != nil {
			return nil, planInvalid{err}
		}
		// A card left waiting while another plan carried on the same to-do would open a second one for the same work.
		if t := p.Tasks[i]; t.Continues != "" && t.ReuseTodo == "" {
			if succ, ok := s.openSuccessor(t.Continues); ok {
				return nil, planInvalid{fmt.Errorf("task %d: %w", i+1, errContinued(t.Continues, succ))}
			}
		}
	}
	p.State = planStarted
	for i := range p.Tasks {
		p.Tasks[i].OwnerStarted = ownerStarts(ctx)
	}
	ok, err := s.movePlan(ctx, p, planOpen)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errPlanNotOpen
	}
	for i := range p.Tasks {
		t := &p.Tasks[i]
		var todo Todo
		if t.ReuseTodo != "" {
			// The todo is already there with its own title, notes and task card in the conversation's feed (a Lark topic
			// opened it): the task runs on it, and nothing is made or shown twice.
			todo, err = s.takeTodoForTask(ctx, p.ID, p.Conversation, t.ReuseTodo)
			if err != nil {
				t.Error = err.Error()
				continue
			}
			if t.Project != "" && todo.Project != t.Project {
				if moved, err := s.editTodo(ctx, todo.ID, db.TodoEdit{Project: &t.Project}); err != nil {
					s.logf("task %s: project: %v", todo.ID, err)
				} else {
					todo = moved
				}
			}
		} else {
			title, retitle := s.taskTitle(ctx, t.Title, t.Prompt) // the assistant's own title stays when it is a real one
			todo, err = s.createTaskTodoAs(ctx, t.ReservedTodoID, title, t.Prompt, p.Conversation, p.ID, t.Project)
			if err != nil {
				t.Error = err.Error()
				continue
			}
			if retitle {
				s.nameTodoLater(ctx, todo.ID, title, t.Prompt)
			}
		}
		t.TodoID = todo.ID
		s.briefStarted(ctx, p, t, todo) // what was asked, kept from now on (task_brief_plan.go)
		s.setTaskStageKind(ctx, todo.ID, "", "", taskStageKind(t.Readonly, t.DoneWhen, todo.PRURL), false)
		// The main conversation or a leader follows up what it hands out (supervise.go); a task the main conversation
		// handed to a leader is that leader's, while it still is one (else the main conversation's again).
		if sup := p.Conversation; s.supervises(ctx, sup) {
			if t.Supervisor != "" && s.isLeader(ctx, t.Supervisor) {
				sup = t.Supervisor
				if t.ReuseTodo == "" {
					s.taskCard(ctx, sup, todo.ID, "") // the leader sees its task's card too; the plan's stays where it was made
				}
			}
			s.superviseTodo(ctx, todo.ID, sup, t.DoneWhen, t.ExpectMinutes)
		} else if sup, ok := s.defaultPlanSupervisor(ctx, p.Conversation, t.Parent); ok {
			// Any other conversation's task is its own to supervise: its signals go to the conversation that planned it,
			// which also sees its card and reviews it. No second card when that is the planning conversation itself.
			if t.ReuseTodo == "" && sup != p.Conversation {
				s.taskCard(ctx, sup, todo.ID, "")
			}
			s.superviseTodo(ctx, todo.ID, sup, t.DoneWhen, t.ExpectMinutes)
		}
		if t.Parent != "" { // a task leader's worker: supervised by its leader's agent, with no conversation to show a card in
			s.superviseTodo(ctx, todo.ID, p.Conversation, t.DoneWhen, t.ExpectMinutes)
			continue
		}
		if t.ReuseTodo != "" {
			continue
		}
		if _, err := s.appendFeed(ctx, p.Conversation, feedTask, todo.ID, nil); err != nil {
			s.logf("task %s: feed: %v", todo.ID, err)
		}
	}
	if _, err := s.movePlan(ctx, p, planStarted); err != nil { // keep the todo ids
		s.logf("plan %s: %v", p.ID, err)
	}
	if remember {
		s.trustKinds(ctx, p.Tasks)
	}
	s.event(ctx, map[string]any{"type": "task_plan", "plan": p})
	for i := range p.Tasks {
		if task := p.Tasks[i]; task.TodoID != "" {
			s.startPlanTaskAsync(p.ID, p.Conversation, i, task, false, func() {
				s.releaseTakenTodo(task.ReuseTodo) // linked or failed: the todo is no longer in the gap
			})
		}
	}
	return p, nil
}

// notReusable is the refusal of a todo_id: it says why, so the assistant can plan again without it.
func notReusable(id, why string) error {
	return fmt.Errorf("todo_id %s cannot be used: %s (leave todo_id out and the task gets a todo of its own)", id, why)
}

// reusableTodo says whether a task of a plan from conversation may run on todo id (task_plan's todo_id): the todo is
// one that conversation opened or supervises (a handover after its agent stopped carries on in the same to-do, #6427),
// is not done, has no live agent on it and is not taken by a plan that started and has not linked its agent yet. An
// agent linked and never let go whose session stopped (its terminal kept, tasks_close.go), was archived or whose
// computer the hub no longer knows is no agent working on it: those links are answered as stopped, for the taker to
// let go (releaseStoppedAgents). A computer that cannot be reached is not an answer: its agent counts as working.
// taking: the plan hold is what the caller is about to claim itself, so it is not looked at (takeTodoForTask holds it).
func (s *Service) reusableTodo(ctx context.Context, conversation, id string, taking bool) (stopped []TodoAgent, err error) {
	t, ok := s.todoNow(id)
	switch {
	case !ok:
		return nil, notReusable(id, "there is no such todo")
	case conversation == "" || t.Origin != conversation && (t.Supervision == nil || t.Supervision.Supervisor != conversation):
		return nil, notReusable(id, "it was not opened from this conversation, nor is it supervised by it")
	case t.DoneAt != nil:
		return nil, notReusable(id, "it is already done")
	}
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		if !s.todoAgentStopped(ctx, a) {
			return nil, notReusable(id, "an agent is already working on it")
		}
		stopped = append(stopped, a)
	}
	if !taking {
		s.tasks.mu.Lock()
		busy := s.tasks.reusing[id]
		s.tasks.mu.Unlock()
		if busy {
			return nil, notReusable(id, "another plan is starting an agent on it")
		}
	}
	return stopped, nil
}

// todoAgentStopped: nothing runs agent a now, so a new agent may take over its to-do: its session is listed stopped
// (switchStopped) or archived, or its computer is gone from the hub. Not listed at all, or the computer not reached,
// is not known to be stopped.
func (s *Service) todoAgentStopped(ctx context.Context, a TodoAgent) bool {
	list, err := s.sessions(ctx, a.MachineID)
	if errors.Is(err, ErrNoDevice) {
		return true
	}
	if err != nil {
		return false
	}
	for _, c := range list {
		if c.SessionID == a.SessionID {
			return c.Archived || switchStopped(c)
		}
	}
	return false
}

// takeTodoForTask is the todo a started plan's task runs on (PlanTask.ReuseTodo), checked again now (it may have been
// closed or taken since the plan was made) and held until the task's agent is linked (releaseTakenTodo). The stopped
// agents still linked to it are let go first: the new agent is the one working on it.
func (s *Service) takeTodoForTask(ctx context.Context, planID, conversation, id string) (Todo, error) {
	s.tasks.mu.Lock()
	if s.tasks.reusing[id] {
		s.tasks.mu.Unlock()
		return Todo{}, notReusable(id, "another plan is starting an agent on it")
	}
	s.tasks.reusing[id] = true
	s.tasks.mu.Unlock()
	stopped, err := s.reusableTodo(ctx, conversation, id, true)
	if err != nil {
		s.releaseTakenTodo(id)
		return Todo{}, err
	}
	// TodoID is the execution binding; ReuseTodo preserves each historical plan's intended target.
	// Retire old bindings while this takeover holds the to-do, before the new agent can ask for approval.
	// Creation order is not start order, and a later retry can change an old plan's UpdatedAt. A supervisor taking
	// over a to-do another conversation opened retires that conversation's bindings too.
	convs := []string{conversation}
	if t, ok := s.todoNow(id); ok && t.Origin != "" && t.Origin != conversation {
		convs = append(convs, t.Origin)
	}
	for _, conv := range convs {
		plans, err := s.conversationPlans(ctx, conv)
		if err != nil {
			s.releaseTakenTodo(id)
			return Todo{}, err
		}
		for _, p := range plans {
			if p.ID == planID {
				continue
			}
			for i, task := range p.Tasks {
				if task.TodoID == id {
					if err := s.patchPlanTask(ctx, p.ID, i, func(t *PlanTask) { t.TodoID = "" }); err != nil {
						s.releaseTakenTodo(id)
						return Todo{}, err
					}
				}
			}
		}
	}
	if err := s.releaseStoppedAgents(ctx, id, stopped); err != nil {
		s.releaseTakenTodo(id)
		return Todo{}, err
	}
	t, _ := s.todoNow(id)
	return formatTodo(t), nil
}

// todoAgentReleaser is the store's letting go of one agent link (released_at only); a store without it keeps that in
// memory only.
type todoAgentReleaser interface {
	ReleaseTodoAgent(ctx context.Context, todoID, machineID, sessionID string, at time.Time) error
}

var _ todoAgentReleaser = (*db.DB)(nil)

// releaseStoppedAgents lets go of the stopped agents still linked to todo id (reusableTodo), so the agent taking it
// over is the one the to-do and its follow-up see. They stay in its history.
func (s *Service) releaseStoppedAgents(ctx context.Context, id string, stopped []TodoAgent) error {
	if len(stopped) == 0 {
		return nil
	}
	now := time.Now().UTC()
	s.todos.writing.Lock()
	if st, ok := s.todoDB().(todoAgentReleaser); ok {
		for _, a := range stopped {
			if err := st.ReleaseTodoAgent(ctx, id, a.MachineID, a.SessionID, now); err != nil {
				s.todos.writing.Unlock()
				return err
			}
		}
	}
	s.todos.mu.Lock()
	if t, ok := s.todos.todos[id]; ok {
		for _, a := range stopped {
			for i := range t.Agents {
				if t.Agents[i].MachineID == a.MachineID && t.Agents[i].SessionID == a.SessionID && t.Agents[i].ReleasedAt == nil {
					t.Agents[i].ReleasedAt = &now
				}
			}
			if key := [2]string{a.MachineID, a.SessionID}; s.todos.activeBySession[key] == id {
				delete(s.todos.activeBySession, key)
			}
		}
		t.UpdatedAt = now
	}
	s.todos.mu.Unlock()
	s.todos.writing.Unlock()
	s.logf("task %s: let go of %d stopped agent(s) for the agent taking it over", id, len(stopped))
	s.linksChanged(ctx)
	s.todosChanged(ctx)
	return nil
}

// releaseTakenTodo lets go of a todo takeTodoForTask held; id "" is nothing.
func (s *Service) releaseTakenTodo(id string) {
	if id == "" {
		return
	}
	s.tasks.mu.Lock()
	delete(s.tasks.reusing, id)
	s.tasks.mu.Unlock()
}

// taskStartTimeout bounds one start of a task's agent: a busy machine's shell can take minutes to run claude
// (node's claudeStartWait, #1396).
var taskStartTimeout = 5 * time.Minute

// taskFailTimeout bounds recording a failed start (the todo, the plan, the receipt), after the start's own time.
const taskFailTimeout = 30 * time.Second

// startPlanTask starts one task's agent and links it to the task's todo. A failed start ends the task: the todo is
// done with why (so nothing shows it as going on, #1077), the plan keeps the error, and the conversation gets a
// receipt — one per task: a retry that fails again (retry) updates the error the receipt shows (clients read it off
// the todo). A task the user finished while its agent was still starting (#2954) is not a failed start: the agent
// that did start stays in the task and the close loop retires it (tasks_close.go). Use startPlanTaskAsync to
// schedule it: go startPlanTask would register too late for the manager's idle check.
func (s *Service) startPlanTask(planID, conversation string, i int, t PlanTask, retry bool) {
	s.taskStarting.Add(1)
	defer s.taskStarting.Add(-1)
	s.runPlanTaskStart(planID, conversation, i, t, retry)
}

// startPlanTaskAsync reserves the start before launching its goroutine: the request may return before the
// goroutine is scheduled, and the manager must not see that gap as idle. Keep the reservation through cleanup.
func (s *Service) startPlanTaskAsync(planID, conversation string, i int, t PlanTask, retry bool, after func()) {
	s.taskStarting.Add(1)
	go func() {
		defer s.taskStarting.Add(-1)
		if after != nil {
			defer after()
		}
		s.runPlanTaskStart(planID, conversation, i, t, retry)
	}()
}

// runPlanTaskStart is the shared start body; its caller owns the idle reservation through every return path.
func (s *Service) runPlanTaskStart(planID, conversation string, i int, t PlanTask, retry bool) {
	t.planID, t.planIndex = planID, i
	defer s.releaseTaskWorkspace(t.TodoID)
	// The admission claim (if the gate took one) is released when this start body ends either way: on success the
	// agent is linked into the todo by then so the ledger's direction count carries it, and on failure nothing runs.
	defer func() {
		if t.admissionRelease != nil {
			t.admissionRelease()
			t.admissionRelease = nil
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), taskStartTimeout)
	defer cancel()
	placed, placedWhy, placedPermission := t.Machine, t.Why, t.Permission
	if t.Role == roleLeader { // its tools are listed once it is linked: a list asked for meanwhile waits (task_leader.go)
		s.leaderStarts.Add(1)
		defer s.leaderStarts.Add(-1)
	}
	t.Permission = strings.TrimSpace(t.Permission)
	mid, sid, err := s.startTaskAgent(ctx, &t, retry)
	var lost startLinkLost
	if errors.As(err, &lost) && lost.back {
		// The link took the start's answer and the machine, back, does not show the agent: start it once more, with
		// a whole start's time of its own (the first may have spent its own on the dead link). Not counted against
		// the launcher (#7511).
		s.logf("task %s: start: %v; the link to %s dropped and came back without the agent; starting once more", t.TodoID, err, t.Machine)
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), taskStartTimeout)
		defer rcancel()
		ctx = rctx
		mid, sid, err = s.startTaskAgent(ctx, &t, retry)
	} else if err != nil && ctx.Err() == nil && !startStalled(err) {
		// The machine's link dropped (a node restarting, a tunnel blinking): try once more after a moment. Any other
		// launcher failure may move a flexible task, after verifying its directory on the destination.
		// An unknown result (the wait ran out or the link dropped while the answer was out) already went through
		// adoption inside startTaskAgent: neither a blind retry nor a reroute may run, since the node can still be
		// creating that first session and either one would open a second agent (#6540).
		unknown := startResultUnknown(err)
		shouldRetry := transientStartErr(err) && !unknown
		if !shouldRetry && t.MachinePolicy == machineFlexible && !unknown {
			shouldRetry = s.healthyPlace(ctx, &t)
		}
		if shouldRetry && (errors.Is(err, errMachineOffline) || errors.Is(err, apierr.ErrOffline)) {
			// Not sent: no connection to the machine. The retry waits a moment and then for the link to come back
			// (#7511), not only a fixed moment that a reconnect taking a minute outlasts; still down when the wait
			// ends, it fails as offline at once without reaching the machine.
			s.logf("task %s: start: %v; retrying once %s is back", t.TodoID, err, t.Machine)
			select {
			case <-time.After(s.taskStartRetry()):
			case <-ctx.Done():
			}
			if ctx.Err() == nil {
				s.waitMachineBack(ctx, t.Machine)
			}
			if ctx.Err() == nil {
				mid, sid, err = s.startTaskAgent(ctx, &t, retry)
			}
		} else if shouldRetry {
			s.logf("task %s: start: %v; retrying", t.TodoID, err)
			select {
			case <-time.After(s.taskStartRetry()):
				mid, sid, err = s.startTaskAgent(ctx, &t, retry)
			case <-ctx.Done():
			}
		}
	}
	if t.Machine != placed || t.Why != placedWhy || t.Permission != placedPermission { // workspace admission already persisted the actual checkout
		s.updatePlanTask(ctx, planID, i, t)
	}
	if err == nil {
		// The start's own context can be spent already: an unanswered start whose session was just reconciled
		// (#6540) reaches here after taskStartTimeout, and a database write on that context fails at once, which
		// would fail the task although its agent is running. The link gets its own time, like the bookkeeping below.
		linkCtx, linkCancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
		err = s.linkPlanAgent(linkCtx, t, mid, sid, false)
		linkCancel()
		if errors.Is(err, db.ErrTodoCompleted) {
			// The user tapped "Done" while the agent was starting (a busy machine takes minutes, #1396). The task stays
			// done and its agent stays in it: nothing reads failed, and the close loop still finds the agent to retire.
			retryCtx, retryCancel := context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
			err = s.linkPlanAgent(retryCtx, t, mid, sid, true)
			retryCancel()
			if err != nil {
				s.logf("task %s: link to the finished task: %v", t.TodoID, err)
			}
		}
	}
	if err == nil {
		if t.Queue != nil { // a queued retry made it through the gate and linked: its waiting record is done
			s.clearAdmissionQueue(context.WithoutCancel(ctx), planID, i, t.TodoID)
		}
		s.remedyStarted(context.WithoutCancel(ctx), planID, conversation, i, t) // after an assistant's retry (start_remedy.go)
		return
	}
	// An admission-gate refusal means the machine or direction is briefly full, not that the start failed: park the
	// task in the admission queue (no failTodo, no failure receipt) and let the queue loop retry it (task_queue.go).
	if isAdmissionDenied(err) {
		s.queueAdmissionRefusal(planID, i, t, err)
		return
	}
	s.logf("task %s: start: %v", t.TodoID, err)
	// The start may have used up ctx (a busy machine's shell taking all of taskStartTimeout): recording the failure
	// on it failed too, and the task stayed "starting" with no agent for ever (#4510). The bookkeeping gets its own time.
	ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), taskFailTimeout)
	defer cancel()
	limit := 200
	if code, _ := codedStartRefusal(err); code != "" {
		limit = proto.CodedStartErrorMax // the hub's sentence, whose remedy comes last (#1642): kept whole
	}
	reason := clip(oneLine(s.startFailureText(ctx, t.Machine, err)), limit)
	// The new tab's shell waits on a question, or the agent's screen stopped, and the computer keeps the tab: a
	// supervised task's start is remedied first, not failed yet (start_remedy.go, #2570).
	code, args := codedStartRefusal(err)
	if s.beginRemedy(ctx, planID, i, t, err, code, args, reason) {
		return
	}
	// A retry the assistant asked for that failed so its tab is gone: the start fails for good, and the supervisor and
	// the user hear of it as of a first failure.
	remedied := t.Start != nil && t.Start.State == remedying
	if ferr := s.failTodo(ctx, t.TodoID, reason); ferr != nil {
		s.logf("task %s: fail: %v", t.TodoID, ferr)
	}
	if perr := s.patchPlanTask(ctx, planID, i, func(pt *PlanTask) {
		pt.Error = err.Error()
		pt.ErrorCode, pt.ErrorArgs = code, args
		if remedied && pt.Start != nil {
			pt.Start.State, pt.Start.Pane, pt.Start.At = remedyFailed, "", time.Now().UTC()
		}
	}); perr != nil {
		s.logf("plan %s: task %d's start error: %v", planID, i, perr)
	}
	s.tasks.mu.Lock()
	s.tasks.receipted[t.TodoID] = time.Now()
	s.tasks.mu.Unlock()
	if remedied {
		s.remedyStep(ctx, conversation, t.TodoID, startRemedyBody{Action: "gave_up", By: "hub", Attempt: t.Start.Attempt, Reason: reason})
	}
	if retry && !remedied {
		return
	}
	_, _ = s.appendFeed(ctx, conversation, feedReceipt, t.TodoID, receiptBody{Title: t.Title, State: "failed",
		Summary: i18n.T(s.lang(ctx), "assistant-tasks.failed_start", i18n.Args{"reason": reason})})
	if todo, ok, err := s.todo(ctx, t.TodoID); err == nil && ok {
		s.taskNotice(ctx, todo, t.Machine, "failed", "", reason)
		s.taskReport(todo, reportFailed, t.Machine, [2]string{}, reason, nil)
	}
}

var (
	errTaskNotRetryable = i18n.Err("assistant-tasks.not_retryable")
	errTaskNotInPlan    = i18n.Err("assistant-tasks.not_in_plan")
	errMachineOffline   = errors.New("machine offline") // matched with errors.Is; the text is machineOffline's
)

// machineOffline is errMachineOffline for machine name, said in the request's language.
func machineOffline(name string) error {
	return offlineError{i18n.Err("assistant-tasks.machine_offline", i18n.Args{"name": name})}
}

type offlineError struct{ msg error }

func (e offlineError) Error() string        { return e.msg.Error() }
func (e offlineError) Unwrap() error        { return e.msg }
func (e offlineError) Is(target error) bool { return target == errMachineOffline }

// retryTask starts again the agent of a task that did not start (#1396): the same task as its plan has it (machine,
// directory, prompt, launcher, model, permission, title), in the same todo — reopened, its error and the plan's
// cleared — so the task shows as starting where it was, not as a second one. A task already
// starting again, or started since, is answered as it is: a double tap starts one agent. A machine that is offline is
// refused before anything changes. A task that is open is not started again: one whose agent the model service cut
// off is told to go on (continueTask).
// The todo it answers is as the apps get it.
func (s *Service) retryTask(ctx context.Context, todoID string) (json.RawMessage, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return nil, err
	}
	cur, ok, err := s.todo(ctx, todoID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, db.ErrTodoNotFound
	}
	if cur.PlanID == "" || cur.Origin == "" {
		return nil, errTaskNotRetryable
	}
	if cur.DoneAt == nil {
		return s.continueTask(ctx, cur)
	}
	s.tasks.mu.Lock()
	busy := s.tasks.retrying[todoID]
	if !busy && cur.DoneAt != nil && cur.StartError != "" {
		s.tasks.retrying[todoID] = true
	}
	s.tasks.mu.Unlock()
	if busy || cur.DoneAt == nil || cur.StartError == "" {
		if !busy && cur.DoneAt != nil {
			return nil, errTaskNotRetryable // done by hand or by the hub, not a failed start
		}
		return s.todoView(ctx, todoID)
	}
	started := false
	defer func() {
		if !started {
			s.tasks.mu.Lock()
			delete(s.tasks.retrying, todoID)
			s.tasks.mu.Unlock()
		}
	}()
	p, err := s.loadPlan(ctx, cur.PlanID)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(p.Tasks, func(t PlanTask) bool { return t.TodoID == todoID })
	if i < 0 {
		return nil, errTaskNotInPlan
	}
	p.Tasks[i].normalizeReadonlyPermission()
	t := p.Tasks[i]
	t.Permission = strings.TrimSpace(t.Permission)
	m, ok := s.machineByName(ctx, t.Machine)
	if !ok {
		return nil, i18n.Err("assistant-tasks.no_machine", i18n.Args{"machine": strconv.Quote(t.Machine)})
	}
	if err := s.leaderOnWorkMachine(&t, m); err != nil { // a retry of a leader plan saved on a computer that is work-trust now
		return nil, fmt.Errorf("%w; nothing was started", err)
	}
	if !m.Reachable {
		return nil, machineOffline(m.Name)
	}
	reopened, ok, err := s.reopenFailedTodo(ctx, todoID)
	if err != nil {
		return nil, err
	}
	view, err := json.Marshal(reopened)
	if err != nil {
		return nil, err
	}
	if !ok { // another assistant sharing the store reopened it first
		return view, nil
	}
	// Task i is kept as this retry starts it (t), its error cleared. The plan is read again under its lock, so what its
	// other tasks recorded since p was read stays.
	if err := s.patchPlanTask(ctx, p.ID, i, func(pt *PlanTask) {
		*pt = t
		pt.Error, pt.ErrorCode, pt.ErrorArgs = "", "", nil
	}); err != nil {
		s.logf("plan %s: %v", p.ID, err)
	}
	s.logf("task %s: retrying its start on %s", todoID, m.ID)
	started = true
	s.startPlanTaskAsync(p.ID, p.Conversation, i, t, true, func() {
		s.tasks.mu.Lock()
		delete(s.tasks.retrying, todoID)
		s.tasks.mu.Unlock()
	})
	return view, nil
}

// continueText is what goes on a cut-off turn: what the chat's "Retry" on an API error card sends.
const continueText = "继续"

// continueHold bounds how long a sent "Continue" keeps further taps from sending another while the agent takes it up.
var continueHold = 30 * time.Second

// continueTask tells the agent of a task whose latest turn the model service cut off (#1516) to go on, as the chat's
// "Retry" on the error card does; the task shows as in progress again once the agent works. One "Continue" per cut: taps while
// one is on its way, or before the agent took it up, answer the todo as it is, as does a task with no agent cut off.
// The agent's machine offline is refused.
func (s *Service) continueTask(ctx context.Context, cur Todo) (json.RawMessage, error) {
	var cut proto.Session
	for _, c := range s.taskAgents(ctx, cur) {
		if c.TurnError != "" && (cut.SessionID == "" || c.UpdatedAt.After(cut.UpdatedAt)) {
			cut = c
		}
	}
	if cut.SessionID == "" {
		return s.todoView(ctx, cur.ID)
	}
	m, ok := s.device(ctx, cut.MachineID)
	if !ok || !m.Reachable {
		name := cut.MachineID
		if ok {
			name = m.Name
		}
		return nil, machineOffline(name)
	}
	s.tasks.mu.Lock()
	busy := s.tasks.retrying[cur.ID]
	s.tasks.retrying[cur.ID] = true
	s.tasks.mu.Unlock()
	if busy {
		return s.todoView(ctx, cur.ID)
	}
	release := func() {
		s.tasks.mu.Lock()
		delete(s.tasks.retrying, cur.ID)
		s.tasks.mu.Unlock()
	}
	if stoppedAgent(cut) { // it stopped since (read-only, #4754): a new agent goes on with the task (tasks_successor.go)
		defer release()
		if live, _ := liveTaskAgents(s.taskAgents(ctx, cur)); len(live) > 0 {
			return s.todoView(ctx, cur.ID) // another agent of the task still runs: it is that one's
		}
		if _, err := s.startTaskSuccessor(ctx, cur, cut, continueText); err != nil {
			return nil, err
		}
		return s.todoView(ctx, cur.ID)
	}
	if err := s.call(ctx, cut.MachineID, DeviceSessionSend, map[string]any{"session_id": cut.SessionID, "text": continueText}, nil); err != nil {
		release()
		return nil, err
	}
	s.logf("task %s: told %s/%s to go on after: %s", cur.ID, cut.MachineID, cut.SessionID, clip(oneLine(cut.TurnError), 120))
	go func() {
		defer release()
		ctx := context.WithoutCancel(ctx)
		for end := time.Now().Add(continueHold); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
			// Still cut off by the same error: not taken up yet. The session is as the node lists it: Claude Code's error
			// is its last words, a Codex turn's the node's own TurnError (no words of its answer, #2185).
			c, ok := s.session(ctx, cut.MachineID, cut.SessionID)
			if !ok || c.Status == proto.StatusWorking || (strings.TrimSpace(c.LastText) != cut.TurnError && c.TurnError != cut.TurnError) {
				return // it took it up (or is gone): a later cut may be continued again
			}
		}
	}()
	return s.todoView(ctx, cur.ID)
}

// taskStartRetryDelay is how long a task waits before its one retry after a start the new tab's terminal was not
// ready for, or a flexible task's move (Service.taskRetry shortens it in tests). A start that found no connection to
// its machine waits for the link instead (waitMachineBack, tasks_start_link.go).
const taskStartRetryDelay = 3 * time.Second

// taskStartRetry is the wait before a failed task start's one retry: taskStartRetryDelay unless tests set taskRetry.
func (s *Service) taskStartRetry() time.Duration {
	if s.taskRetry > 0 {
		return s.taskRetry
	}
	return taskStartRetryDelay
}

// Bounds of the bounded retry before a needs recheck treats a node as unreachable: agentReachBackoff before the
// second try and doubled before the third; agentReachMax bounds one whole recheck, machines included (tests shorten).
var (
	agentReachAttempts = 3
	agentReachBackoff  = 300 * time.Millisecond
	agentReachMax      = 2 * time.Second
)

// transientStartErr: the agent did not start because a link on the way was down or dropped (this hub's to the
// machine, or one further on that answered with the same words: a member hub, a connector), or the new tab's
// terminal was not ready for the start command (proto.CodeTerminalNotReady), not because the node refused or the
// launcher failed; worth one retry, and never counted against the launcher (launcher_health.go).
func transientStartErr(err error) bool {
	var re *rpc.Error
	return errors.Is(err, rpc.ErrClosed) || errors.Is(err, apierr.ErrOffline) ||
		errors.As(err, &re) && (re.Message == rpc.ErrClosed.Error() || re.Code == proto.CodeTerminalNotReady)
}

// taskPlanWithdrawnEvent: a conversation withdrew a plan card of its own (task_plan_withdraw). Detail: plan,
// conversation, reason, tasks (their titles).
const taskPlanWithdrawnEvent = "task_plan_withdrawn"

// withoutWithdrawn is plans less those their own conversation withdrew (task_plan_withdrawn in events): dismissed as
// the owner's Dismiss leaves them, but the owner did not answer them, so they say nothing about which kinds of task the
// owner starts or turns down (rulePlanRuns, the setup suggestions).
func withoutWithdrawn(plans []*TaskPlan, events []db.WorkEvent) []*TaskPlan {
	withdrawn := map[string]bool{}
	for _, e := range events {
		if id, _ := e.Detail["plan"].(string); e.Kind == taskPlanWithdrawnEvent && id != "" {
			withdrawn[id] = true
		}
	}
	if len(withdrawn) == 0 {
		return plans
	}
	out := make([]*TaskPlan, 0, len(plans))
	for _, p := range plans {
		if !withdrawn[p.ID] {
			out = append(out, p)
		}
	}
	return out
}

// toolTaskPlanWithdraw is task_plan_withdraw: the conversation that put up plan card plan_id takes it down while nothing
// of it has started, as the owner's Dismiss does (dismissPlan), its reason kept as a task_plan_withdrawn work event
// (#6739). A card whose work was done elsewhere, or that the owner dropped, otherwise waited for "Start" in every list
// of what is the owner's to handle until they dismissed it themselves. Another conversation's plan, and one that
// started or was dismissed, are refused, and nothing changes.
func (s *Service) toolTaskPlanWithdraw(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		PlanID string `json:"plan_id"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	reason, err := remedyReason(a.Reason)
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(a.PlanID)
	if id == "" {
		return "", errors.New("plan_id is required: the plan's id, as task_plan's answer or the snapshot's plans waiting to start give it")
	}
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		return "", fmt.Errorf("plan %s: %w", id, err)
	}
	if p.Conversation != self {
		return "", fmt.Errorf("plan %s was put up by another conversation: only that one withdraws it (or the user dismisses it); nothing was changed", id)
	}
	if p.State != planOpen {
		return "", fmt.Errorf("plan %s is %s, no longer waiting for Start: nothing was withdrawn", id, p.State)
	}
	if p, err = s.dismissPlan(ctx, id); err != nil {
		return "", fmt.Errorf("plan %s: %w; nothing was withdrawn", id, err)
	}
	titles := make([]string, 0, len(p.Tasks))
	for _, t := range p.Tasks {
		titles = append(titles, t.Title)
	}
	s.workEvent("", self, taskPlanWithdrawnEvent, map[string]any{"plan": id, "conversation": self, "reason": reason, "tasks": titles})
	s.logf("plan %s: withdrawn by its conversation %s: %s", id, short(self), reason)
	return fmt.Sprintf("Withdrew plan %s (%d task(s): %s): its card no longer waits for Start. Reason kept: %s", id, len(titles),
		strings.Join(titles, "; "), reason), nil
}

func (s *Service) dismissPlan(ctx context.Context, id string) (*TaskPlan, error) {
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.State != planOpen {
		return nil, errPlanNotOpen
	}
	p.State = planDismissed
	ok, err := s.movePlan(ctx, p, planOpen)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errPlanNotOpen
	}
	s.event(ctx, map[string]any{"type": "task_plan", "plan": p})
	return p, nil
}
