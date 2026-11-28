package assistant

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Task leaders (#3656, docs/assistant/unified-entry.md "Task leaders"). A task the main conversation or a leader
// conversation hands out with task_plan role "leader" has a coding agent that leads it: it gets a restricted task_plan
// of its own and hands workers to its task, and what the workers report goes to it. Depth stops there: dot (a leader
// conversation) → task leader → worker; a worker hands out nothing (workerRefusal).
//
// What is kept, with no new column or table: the leader's todo_agents row has source "leader" (set when its agent is
// linked from the plan task's Role, which the saved plan keeps), and each worker's row has source "worker" and via the
// leader's task id. A worker's todo is a supervised task like any other, with the leader's agent session as the
// ledger's supervisor (so the API shows it, and a restart keeps it) and the task's done_when. Where a supervisor is a
// conversation everything goes to that conversation; here the same signals, the worker's reports included, are one
// message into the leader agent's session instead (deliverToLeaderAgent, called from sendDigest), and the leader closes
// its workers with a task_review of its own that reaches its direct workers only (leaderTaskReview). None of it goes
// through digestTo, which would send a session that is no conversation to the main chat. What a leader needs the user
// for goes up to the conversation that handed it its task (sigDecide).

const (
	roleLeader = "leader"
	// sourceLeader and sourceWorker are todo_agents.source of a task leader's agent and of its workers' (via: the
	// leader's task id), as sourceReview marks a reviewer.
	sourceLeader = "leader"
	sourceWorker = "worker"
	// leaderWorkersMost bounds the workers of one leader that are open at a time.
	leaderWorkersMost = 8
)

// leaderListWait bounds only legacy one-shot/direct discovery while a leader start reaches its link. Revision-aware
// watches use their cursor and heartbeat instead.
var leaderListWait = 8 * time.Second

func (a TodoAgent) active() bool { return a.ReleasedAt == nil && a.DismissedAt == nil }

// linkSource is the todo_agents source and via the agent of plan task t is linked with.
func (t PlanTask) linkSource() (source, via string) {
	switch {
	case t.Role == roleLeader:
		return sourceLeader, ""
	case t.Parent != "":
		return sourceWorker, t.Parent
	}
	return "manual", ""
}

// linkPlanAgent links the agent just started for plan task t to its todo.
func (s *Service) linkPlanAgent(ctx context.Context, t PlanTask, mid, sid string, allowDone bool) error {
	source, via := t.linkSource()
	err := s.linkTodoAgentWithSourceAllowingDone(ctx, t.TodoID, mid, sid, t.Title, t.Cwd, t.startedAgent, "", source, via, allowDone)
	if err == nil && !allowDone {
		s.estimateAdmitted(ctx, t, mid, sid)
	}
	return err
}

// leaderTask is the open task agent machine/session leads, with the agent's own row; false for any other agent.
func (s *Service) leaderTask(ctx context.Context, machine, session string) (Todo, TodoAgent, bool) {
	if session == "" {
		return Todo{}, TodoAgent{}, false
	}
	machine = s.canonical(machine)
	t, ok := s.taskOf(ctx, machine, session)
	if !ok || t.Cancelled() {
		return Todo{}, TodoAgent{}, false
	}
	for _, a := range t.Agents {
		if a.MachineID == machine && a.SessionID == session && a.active() && a.Source == sourceLeader {
			return t, a, true
		}
	}
	return Todo{}, TodoAgent{}, false
}

// taskOfAny is taskOf plus the tasks whose agent is released or dismissed: a leader whose agent was replaced still has
// its own task and its supervisor above it, so what could not be delivered can be escalated there.
func (s *Service) taskOfAny(ctx context.Context, mid, sid string) (Todo, bool) {
	if t, ok := s.taskOf(ctx, mid, sid); ok {
		return t, true
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return Todo{}, false
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for _, t := range s.todos.todos {
		if t.Origin == "" || t.DoneAt != nil {
			continue
		}
		for _, a := range t.Agents {
			if a.MachineID == mid && a.SessionID == sid {
				return copyTodoLocked(t), true
			}
		}
	}
	return Todo{}, false
}

// isLeaderSession: machine/session is a task leader's agent, whatever the state of its row. Both are needed: a session id
// alone is not unique across computers.
func (s *Service) isLeaderSession(machine, sid string) bool {
	if sid == "" {
		return false
	}
	machine = s.canonical(machine)
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for _, t := range s.todos.todos {
		for _, a := range t.Agents {
			if a.MachineID == machine && a.SessionID == sid && a.Source == sourceLeader {
				return true
			}
		}
	}
	return false
}

// agentSupervised: task t's supervisor is a task leader's agent, not a conversation.
func (s *Service) agentSupervised(t Todo) bool {
	if t.Supervision == nil || t.Supervision.Supervisor == "" {
		return false
	}
	return s.isLeaderRef(t.Supervision.Supervisor)
}

// leaderRef names a task leader's agent as a worker's ledger supervisor and to-do origin: machine and session together,
// since a session id alone is not unique across computers. It is no conversation id (conversations have none with a "/").
func leaderRef(machine, session string) string { return machine + "/" + session }

// parseLeaderRef is the machine and session a leaderRef names.
func parseLeaderRef(ref string) (machine, session string, ok bool) {
	machine, session, ok = strings.Cut(ref, "/")
	return machine, session, ok && machine != "" && session != ""
}

// isLeaderRef: ref names a task leader's agent.
func (s *Service) isLeaderRef(ref string) bool {
	m, sid, ok := parseLeaderRef(ref)
	return ok && s.isLeaderSession(m, sid)
}

// leaderSupervisor is the leader agent that supervises task t now: the session its ledger names while that is the
// agent of an open leader task, else the current agent of the leader task its worker's row says it belongs to (a leader
// started again has a new session). supervised says t's supervisor is a leader agent at all; ok that it can be reached.
func (s *Service) leaderSupervisor(t Todo) (lead TodoAgent, supervised, ok bool) {
	if !s.agentSupervised(t) {
		return TodoAgent{}, false, false
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	open := func(l *Todo) bool { return l != nil && l.DoneAt == nil && !l.Cancelled() }
	machine, supSession, _ := parseLeaderRef(t.Supervision.Supervisor)
	machine = s.canonical(machine)
	// anyMachine: the fallback by the leader task's link takes its current agent wherever it runs now (a flexible leader
	// started again on another computer); the ledger's own session is matched on its computer.
	pick := func(l *Todo, sid string, anyMachine bool) {
		for _, a := range l.Agents {
			if (anyMachine || a.MachineID == machine) && a.active() && a.Source == sourceLeader && (sid == "" || a.SessionID == sid) && (!ok || a.LinkedAt.After(lead.LinkedAt)) {
				lead, ok = a, true
			}
		}
	}
	for _, l := range s.todos.todos {
		if open(l) {
			pick(l, supSession, false)
		}
	}
	if ok {
		return lead, true, true
	}
	for _, a := range t.Agents {
		if a.Source == sourceWorker && a.Via != "" && open(s.todos.todos[a.Via]) {
			pick(s.todos.todos[a.Via], "", true)
		}
	}
	return lead, true, ok
}

// leaderPlanTool is the restricted task_plan a task leader gets, under the name the model calls (prompts/mcp/leader).
func leaderPlanTool() proto.Tool {
	t := prompts.Tool("leader_task_plan")
	return proto.Tool{Name: "task_plan", Description: t.Description, InputSchema: t.InputSchema}
}

// leaderToolList are a task leader's own tools: the restricted task_plan and task_review, under the names the model calls.
func leaderToolList() []proto.Tool {
	r := prompts.Tool("leader_task_review")
	return []proto.Tool{leaderPlanTool(), {Name: "task_review", Description: r.Description, InputSchema: r.InputSchema}}
}

// runtimeLeaderTools supplies plugin registration schemas and checks individual sessions at tool activation.
func (s *Service) runtimeLeaderTools(ctx context.Context, machine, session string, guestHint bool) []proto.Tool {
	if s.isWorkMachine(machine) || guestHint {
		return nil
	}
	if session != "" {
		if _, guest, err := s.guestOf(ctx, machine, session); err != nil || guest {
			return nil
		}
		if _, _, ok := s.leaderTask(ctx, machine, session); !ok {
			return nil
		}
	}
	return localizedMCPTools(leaderToolList(), s.toolLanguage(ctx))
}

// leaderTools are the tools only a task leader's agent gets, on top of any agent's: none for any other agent.
func (s *Service) leaderTools(ctx context.Context, machine, session string) []proto.Tool {
	if session == "" {
		return nil
	}
	if _, _, ok := s.leaderTask(ctx, machine, session); ok {
		return localizedMCPTools(leaderToolList(), s.toolLanguage(ctx))
	}
	if revision, _ := ctx.Value(revisionDiscoveryKey{}).(bool); revision {
		return nil // a revision-aware peer recovers through its cursor, never through the legacy startup wait
	}
	// A legacy one-shot peer asks before the start returns and has no cursor with which to recover later. Wait only
	// while a leader is actually starting, and only until that leader's real link grants the tools.
	if _, ok := s.taskOf(ctx, s.canonical(machine), session); ok {
		return nil
	}
	for deadline := time.Now().Add(leaderListWait); s.leaderStarts.Load() > 0 && time.Now().Before(deadline); {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(100 * time.Millisecond):
		}
		if _, _, ok := s.leaderTask(ctx, machine, session); ok {
			return localizedMCPTools(leaderToolList(), s.toolLanguage(ctx))
		}
	}
	return nil
}

// leaderTaskPlan is task_plan called by agent machine/session through its MCP server: it runs only for the leader of
// an open task, and only for workers beneath that task. Any other agent is refused as if the tool did not exist; a
// task's other agents are told why.
func (s *Service) leaderTaskPlan(ctx context.Context, machine, session string, args json.RawMessage) proto.ToolResult {
	lead, agent, ok := s.leaderTask(ctx, machine, session)
	if !ok {
		if err := s.workerRefusal(ctx, machine, session); err != nil {
			return proto.TextResult(err.Error(), true)
		}
		return proto.TextResult("unknown tool task_plan", true)
	}
	res, err := s.taskPlanFrom(ctx, agent.MachineID, leaderRef(agent.MachineID, session), args, &leaderScope{task: lead, agent: agent})
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	return proto.TextResult(res, false)
}

// leaderScope is what a task leader's task_plan is bound to: its open task and its own agent (the default place).
type leaderScope struct {
	task  Todo
	agent TodoAgent
}

// leaderTaskIn is a worker task as a task leader may plan it: nothing else (no todo_id, attachments, project,
// user_requested…) is read, an unknown field is refused. Every new worker declares role "worker".
type leaderTaskIn struct {
	Title            string             `json:"title"`
	Prompt           string             `json:"prompt"`
	Role             string             `json:"role"`
	Machine          string             `json:"machine"`
	MachinePolicy    string             `json:"machine_policy"`
	Cwd              string             `json:"cwd"`
	Launcher         string             `json:"launcher"`
	Host             string             `json:"host"`
	Model            string             `json:"model"`
	Tier             string             `json:"tier"`
	Effort           string             `json:"effort"`
	Speed            string             `json:"speed"`
	Permission       string             `json:"permission"`
	Readonly         bool               `json:"readonly"`
	MultiIssueReport bool               `json:"multi_issue_report"`
	DoneWhen         []string           `json:"done_when"`
	Goal             string             `json:"goal"`
	ExpectMinutes    int                `json:"expect_minutes"`
	Why              string             `json:"why"`
	DistinctFrom     []TaskDistinctFrom `json:"distinct_from"`
}

// workerTasks reads a leader's task_plan arguments as worker tasks beneath its task.
func (l *leaderScope) workerTasks(args json.RawMessage) ([]PlanTask, error) {
	var in struct {
		Tasks []leaderTaskIn `json:"tasks"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return nil, fmt.Errorf("a task leader's task_plan takes only the fields its schema lists; todo_id, attachments and "+
			"any other are refused, and workers can only be new tasks beneath your own task (%v)", err)
	}
	out := make([]PlanTask, 0, len(in.Tasks))
	for _, t := range in.Tasks {
		pt := PlanTask{Title: t.Title, Prompt: t.Prompt, Role: t.Role, Machine: t.Machine, MachinePolicy: t.MachinePolicy, Cwd: t.Cwd,
			Launcher: t.Launcher, Host: strings.TrimSpace(t.Host), Model: t.Model, Tier: t.Tier, Effort: t.Effort, Speed: t.Speed, Permission: t.Permission, Readonly: t.Readonly, MultiIssueReport: t.MultiIssueReport,
			DoneWhen: t.DoneWhen, Goal: t.Goal, ExpectMinutes: t.ExpectMinutes, Why: t.Why, Parent: l.task.ID, Project: l.task.Project,
			DistinctFrom: t.DistinctFrom}
		if strings.TrimSpace(pt.Machine) == "" {
			pt.Machine = l.agent.MachineID
			if strings.TrimSpace(pt.Cwd) == "" {
				pt.Cwd = l.agent.Cwd
			}
		}
		out = append(out, pt)
	}
	return out, nil
}

// checkLeaderRoom refuses a plan that would leave the leader's task with more than leaderWorkersMost open workers. The
// workers are counted by the task they belong to (the supervisor ref or a worker row via the leader's task), so a leader
// started again does not get a new allowance.
func (s *Service) checkLeaderRoom(ctx context.Context, scope *leaderScope, adding int) error {
	ref := leaderRef(scope.agent.MachineID, scope.agent.SessionID)
	_ = s.ensureTodosLoaded(ctx)
	open := 0
	s.todos.mu.RLock()
	for _, t := range s.todos.todos {
		if t.DoneAt != nil || t.Supervision == nil && !slices.ContainsFunc(t.Agents, func(a TodoAgent) bool { return a.Source == sourceWorker }) {
			continue
		}
		belongs := t.Supervision != nil && t.Supervision.Supervisor == ref
		for _, a := range t.Agents {
			belongs = belongs || a.Source == sourceWorker && a.Via == scope.task.ID && a.active()
		}
		if belongs {
			open++
		}
	}
	s.todos.mu.RUnlock()
	if open+adding > leaderWorkersMost {
		return fmt.Errorf("at most %d workers of your task are open at a time (%d now): wait for some to finish; nothing was planned",
			leaderWorkersMost, open)
	}
	return nil
}

// ---- supervision messages ----

// leaderConversation is the conversation above task leader agent lead: the one that handed it its task (its ledger's
// supervisor), and the main conversation if that is gone. A leader whose own task is closed has nobody above it (""):
// its workers were closed with it.
func (s *Service) leaderConversation(ctx context.Context, lead TodoAgent) string {
	if t, ok := s.taskOfAny(ctx, lead.MachineID, lead.SessionID); ok && t.Supervision != nil && t.Supervision.Supervisor != "" && !s.isLeaderRef(t.Supervision.Supervisor) {
		return t.Supervision.Supervisor
	}
	return s.settings(ctx).Main
}

// leaderAbove is the task whose agent supervises worker task t, with that agent's session (the ledger's), ok when t is
// leader-supervised at all. It works for a released or dismissed leader too: what could not be delivered still has a
// conversation above it to go to.
func (s *Service) leaderAbove(t Todo) (lead Todo, session string, ok bool) {
	if !s.agentSupervised(t) {
		return Todo{}, "", false
	}
	machine, sid, _ := parseLeaderRef(t.Supervision.Supervisor)
	machine = s.canonical(machine)
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for _, l := range s.todos.todos {
		for _, a := range l.Agents {
			if a.MachineID == machine && a.SessionID == sid && a.Source == sourceLeader {
				return copyTodoLocked(l), sid, true
			}
		}
	}
	return Todo{}, sid, true
}

// supervisorAbove is the conversation above worker task t's leader agent, "" when there is none. Used to escalate what
// can no longer be delivered to the leader.
func (s *Service) supervisorAbove(ctx context.Context, lead TodoAgent) string {
	if _, ok := s.taskOfAny(ctx, lead.MachineID, lead.SessionID); !ok {
		return "" // the leader's own task is closed
	}
	return s.leaderConversation(ctx, lead)
}

// supervisorAboveTask is the conversation above worker task t's leader: the conversation that supervises the leader's own
// task, else the main conversation; "" with neither. It does not need the leader's task to be open: a closed leader's
// workers go to this conversation (rehomeLeaderlessWorkers).
func (s *Service) supervisorAboveTask(ctx context.Context, t Todo) (up, leaderSession string) {
	lead, sid, ok := s.leaderAbove(t)
	if !ok {
		return "", ""
	}
	if lead.Supervision != nil && lead.Supervision.Supervisor != "" && !s.isLeaderRef(lead.Supervision.Supervisor) {
		return lead.Supervision.Supervisor, sid
	}
	return s.settings(ctx).Main, sid
}

// escalateLeaderSignal is signal g about a worker whose leader agent is unreachable, as a decision for the conversation
// above that leader: it names the worker (so that conversation can review it directly) and says the leader could not be
// reached, with what the signal said. Todo "" when there is no conversation above it, in which case the caller leaves
// the signal for another try.
func (s *Service) escalateLeaderSignal(ctx context.Context, g superviseSignal) superviseSignal {
	return s.escalateLeaderSignalAs(ctx, g, false)
}

// escalateLeaderSignalAs is escalateLeaderSignal; uncertain says the leader's computer took the message but its session
// was not seen taking it (held for approval there, dropped, typed part way), rather than that the leader was not reached.
func (s *Service) escalateLeaderSignalAs(ctx context.Context, g superviseSignal, uncertain bool) superviseSignal {
	t, ok := s.todoNow(g.Todo)
	if !ok {
		return superviseSignal{}
	}
	up, sid := s.supervisorAboveTask(ctx, t)
	if up == "" {
		s.logf("supervise: %s of task %s: its task leader %s is unreachable and nothing is above it; trying again later", g.Kind, g.Todo, short(sid))
		return superviseSignal{}
	}
	why, key := fmt.Sprintf("Its task leader %s cannot be reached, so what its worker reported could not be delivered (%s: %s).",
		short(sid), g.Kind, clip(oneLine(g.Detail), 300)), "leader-unreachable@"
	if uncertain {
		why, key = fmt.Sprintf("Its task leader %s may not have received what its worker reported: its computer took the message, but "+
			"the session was not seen taking it (it may wait for approval there, or have been dropped) (%s: %s).",
			short(sid), g.Kind, clip(oneLine(g.Detail), 300)), "leader-unconfirmed@"
	}
	return superviseSignal{Todo: g.Todo, Kind: sigDecide, To: up, Key: key + g.Todo + "@" + g.Kind + "@" + g.Key,
		Detail: why, At: g.At, Unreachable: true}
}

// leaderRoute says where signal g of a task goes when its supervisor is a leader agent: handled false for any other
// task (digestTo decides), else the leader agent it goes to, reachable false when there is none now (the signal is
// dropped: it never falls to the main conversation). A signal that names its conversation itself (g.To: a card's rung,
// a decision sent up) is not one of these.
func (s *Service) leaderRoute(g superviseSignal) (lead TodoAgent, handled, reachable bool) {
	if g.To != "" {
		return TodoAgent{}, false, false
	}
	t, ok := s.todoNow(g.Todo)
	if !ok {
		return TodoAgent{}, false, false
	}
	lead, handled, reachable = s.leaderSupervisor(t)
	return lead, handled, reachable
}

// deliverToLeaderAgent puts the signals list of its workers into leader agent lead's session as one supervision message
// (agents/worker_supervise), deduplicated as a conversation's are; now is when sendDigest marked it as the last message
// and prev the last before. True when it went in. session.send queues behind a running turn and resumes a closed agent,
// so nothing is queued here. An offline resume completes in the background.
func (s *Service) deliverToLeaderAgent(ctx context.Context, lead TodoAgent, list []superviseSignal, web string, now, prev time.Time) bool {
	var entries []prompts.SuperviseEntry
	var kept, moot, retry []superviseSignal
	// An earlier offline resume owns its in-flight signals until its real delivery callback. Keep them out of this
	// batch's acknowledgements as well as its text: a second digest may carry new signals, but must not ack the first
	// batch before that first batch reaches the leader (#5732, #9701). dedupMu is held throughout this selection.
	batch := make([]superviseSignal, 0, len(list))
	for _, g := range list {
		e, ok, deferRetry := s.digestEntryChecked(ctx, g, web)
		if deferRetry {
			retry = append(retry, g)
			continue
		}
		if !ok {
			moot = append(moot, g)
			batch = append(batch, g)
			continue
		}
		key, hash := digestFingerprint(e, g)
		if _, resuming := s.supervise.resuming[[2]string{key, hash}]; resuming {
			continue
		}
		entries, kept = append(entries, e), append(kept, g)
		batch = append(batch, g)
	}
	if len(retry) > 0 {
		at := now
		if len(entries) > 0 {
			at = time.Time{}
		}
		s.restoreDigest(retry, at, prev)
	}
	s.clearSentTodoReady(moot)
	s.clearSentPRMerged(moot)
	s.clearSentParentCloseout(moot)
	s.turnsTold(ctx, moot) // what no longer holds is not told after a restart either
	if len(entries) == 0 {
		s.ackSupervisePending(batch)
		return false
	}
	entries, kept = s.screenModelSupervision(ctx, leaderRef(lead.MachineID, lead.SessionID), entries, kept, now, prev)
	batch = append(moot, kept...)
	if len(entries) == 0 {
		s.ackSupervisePending(moot)
		return false
	}
	holds := kept
	session, listed := s.session(ctx, lead.MachineID, lead.SessionID)
	if !listed {
		s.logf("supervise: task leader %s is not listed; trying again", short(lead.SessionID))
		s.restoreDigest(kept, now, prev)
		return false
	}
	delivery := leaderDelivery
	if session.Status == proto.StatusOffline {
		delivery = leaderResumeDelivery
	}
	entries, kept, rollback, err := s.reserveSupervisionDelivery(ctx, entries, kept, now, delivery)
	if err != nil {
		s.logf("supervise: deduplication unavailable; retrying: %v", err)
		s.restoreDigest(kept, now, prev)
		return false
	}
	if len(entries) == 0 {
		s.ackSupervisePending(batch)
		s.topicFollowupsTold(ctx, holds)
		s.todoReadyTold(ctx, holds)
		s.prMergeWakeTold(ctx, holds)
		s.parentCloseoutTold(ctx, holds)
		s.turnsTold(ctx, holds) // already covered by the deduplication history
		return false
	}
	delivered := func(ctx context.Context) {
		s.supervise.dedupMu.Lock()
		defer s.supervise.dedupMu.Unlock()
		s.leaderDelivered(ctx, lead, entries, batch, kept, holds)
	}
	// Sort a copy for rendering so the retry still pairs each fingerprint with its original signal.
	text := prompts.RenderWorkerSupervise(sortEntries(slices.Clone(entries)))
	if delivery == leaderResumeDelivery {
		s.resumeLeaderForSupervision(lead, text, batch, kept, holds, func(ctx context.Context, err error) {
			s.supervise.dedupMu.Lock()
			defer s.supervise.dedupMu.Unlock()
			rollback()
			if err == nil {
				s.leaderDelivered(ctx, lead, entries, batch, kept, holds)
			}
		}, delivered)
		return false
	}
	if err := s.sendToLeader(ctx, lead, text, batch, kept, holds, delivered); err != nil {
		if relaySuppressed(err) {
			rollback() // not delivered, not retried and not escalated: the withdrawal owns what is left
			s.ackSupervisePending(batch)
			return false
		}
		var escalated leaderEscalated
		if errors.As(err, &escalated) {
			// The current process's escalation/retry owns this message. Do not restore its pending entry and escalate it
			// twice, but release dedup and leave the turn untold so a manager restarted without either can deliver it.
			rollback()
			s.logf("supervise: %d signal(s) for task leader %s escalated (stopped, or not seen taking them): %v; keeping them recoverable", len(entries), short(lead.SessionID), err)
			return false
		}
		s.logf("supervise: %d signal(s) for task leader %s: %v; trying again", len(entries), short(lead.SessionID), err)
		rollback()
		s.restoreDigest(kept, now, prev)
		return false
	}
	s.logf("supervise: %d signal(s) to task leader %s", len(entries), short(lead.SessionID))
	s.ackTopicFollowupDedup(ctx, kept)
	s.ackSupervisePending(batch)
	s.parkedDueTold(ctx, kept)
	s.topicFollowupsTold(ctx, holds)
	s.estimateDueTold(ctx, kept)
	s.todoReadyTold(ctx, holds)
	s.prMergeWakeTold(ctx, holds)
	s.parentCloseoutTold(ctx, holds)
	s.turnsTold(ctx, holds) // kept went, the rest was said the same within the deduplication window
	return true
}

// leaderUnreachable is a send to the leader's session that failed: the message did not reach it, so it must be retried
// or escalated, never lost.
type leaderUnreachable struct{ err error }

func (e leaderUnreachable) Error() string { return e.err.Error() }
func (e leaderUnreachable) Unwrap() error { return e.err }

// leaderEscalated is a refused or unconfirmed send (#9607) whose escalation and process-local retry are already running. It is not delivery:
// the original turn stays untold and its dedup reservation is released so a restarted manager can recover it.
type leaderEscalated struct{ err error }

func (e leaderEscalated) Error() string { return e.err.Error() }
func (e leaderEscalated) Unwrap() error { return e.err }

// sendToLeader sends text, the supervision message for signals sent, into the leader agent's session. It states no
// permission-mode class without evidence captured by the source node when the words were written; a digest with missing
// or mixed classes states none, and queued retries reuse that immutable evidence. A stopped or unconfirmed send escalates
// and retries (escalateLeaderMessage); nodeMayHaveTaken marks that escalation unconfirmed rather than unreachable (#9607).
// Other failures are retried by the next digest. After real delivery, delivered re-records dedup and acknowledges this
// batch, including members suppressed by delivered dedup history (#5732). sent determines the text actually carried;
// receipts includes the filtered members whose turn receipts close on delivery.
func (s *Service) sendToLeader(ctx context.Context, lead TodoAgent, text string, all, sent, receipts []superviseSignal, delivered func(context.Context)) error {
	if !supervisionSignalsAllowed(s.supervisionMode(ctx), sent) {
		return errSupervisionSuppressed // the last look before the send (supervise_switch.go)
	}
	ctx = supervisionSourceContext(ctx, sent)
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	if err := s.call(sctx, lead.MachineID, DeviceSessionSend, sendParams(lead.SessionID, text, false), nil); err != nil {
		if refusedWith(err, proto.CodeAgentStopped) {
			s.logf("task leader %s: it has stopped: %v", short(lead.SessionID), err)
			s.escalateLeaderMessage(lead, all, sent, receipts, text, false, delivered)
			return leaderEscalated{err}
		}
		if nodeMayHaveTaken(err) {
			s.logf("task leader %s: the message may have reached it, not confirmed: %v", short(lead.SessionID), err)
			s.escalateLeaderMessage(lead, all, sent, receipts, text, true, delivered)
			return leaderEscalated{err}
		}
		return leaderUnreachable{err}
	}
	return nil
}

// resumeLeaderForSupervision lets the digest continue while the offline session resumes. Complete the first attempt
// before handing a failure to the retry loop. Only confirmed delivery acknowledges the batch and its filtered receipts.
func (s *Service) resumeLeaderForSupervision(lead TodoAgent, text string, all, sent, receipts []superviseSignal, completed func(context.Context, error), delivered func(context.Context)) {
	go func() {
		ctx := context.Background()
		err := s.sendToLeaderNow(ctx, lead, text, sent)
		completed(ctx, err)
		if relaySuppressed(err) {
			return
		}
		if err != nil {
			s.logf("task leader %s: resume: %v", short(lead.SessionID), err)
			s.escalateLeaderMessage(lead, all, sent, receipts, text, nodeMayHaveTaken(err), delivered)
		}
	}()
}

// leaderDelivered records a confirmed background send or retry under dedupMu. It re-records the actual entries/signals
// pairing, then acknowledges only this batch; an older in-flight resume still owns its own excluded signals.
func (s *Service) leaderDelivered(ctx context.Context, lead TodoAgent, entries []prompts.SuperviseEntry, all, sent, receipts []superviseSignal) {
	if _, _, _, err := s.reserveSupervision(ctx, entries, sent, time.Now()); err != nil {
		s.logf("supervise: task leader %s took the message; deduplication not recorded: %v", short(lead.SessionID), err)
	}
	s.ackTopicFollowupDedup(ctx, sent)
	s.topicFollowupsTold(ctx, receipts)
	s.ackSupervisePending(all)
	s.parkedDueTold(ctx, sent)
	s.estimateDueTold(ctx, sent)
	s.todoReadyTold(ctx, receipts)
	s.prMergeWakeTold(ctx, receipts)
	s.parentCloseoutTold(ctx, receipts)
	s.turnsTold(ctx, receipts)
	s.logf("supervise: %d signal(s) to task leader %s after confirmation", len(sent), short(lead.SessionID))
}

// sendToLeaderNow is one attempt to put text into the leader's session, resuming it if closed; it waits up to
// leaderResumeTime. Each attempt has its own time, so it can be called from a goroutine that outlives any request.
func (s *Service) sendToLeaderNow(ctx context.Context, lead TodoAgent, text string, kept []superviseSignal) error {
	if !supervisionSignalsAllowed(s.supervisionMode(ctx), kept) {
		return errSupervisionSuppressed // also a retry or resume that was already running
	}
	ctx = supervisionSourceContext(ctx, kept)
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.leaderResumeTime())
	defer cancel()
	return s.call(sctx, lead.MachineID, DeviceSessionSend, sendParams(lead.SessionID, text, false), nil)
}

// escalateLeaderMessage handles supervision message text for signals sent that could not be put into the leader's
// session (a closed agent's resume failed, or the node refused it because the agent has stopped), or that its session
// was not seen taking (uncertain). Only those actually rendered into the text become decisions for the conversation
// above the leader, naming the worker and saying whether the leader was unreachable or unconfirmed. In any case the
// message is retried on its own context until taken or the leader's task closes. Real delivery re-records dedup and
// acknowledges the complete original batch, including filtered members (#5732, #9607).
func (s *Service) escalateLeaderMessage(lead TodoAgent, all, sent, receipts []superviseSignal, text string, uncertain bool, delivered func(context.Context)) {
	ctx := context.Background()
	if !supervisionSignalsAllowed(s.supervisionMode(ctx), sent) {
		return // the mode drops it (off, or only routine checks under outcomes): no escalation and no retry loop
	}
	for _, g := range sent {
		if w, ok := s.todoNow(g.Todo); ok {
			if esc := s.escalateLeaderSignalAs(ctx, g, uncertain); esc.Todo != "" {
				s.signalReplacing(&g, esc)
			} else if up, _ := s.supervisorAboveTask(ctx, w); up == "" {
				s.logf("task leader %s: nobody above it to tell about %s; retrying the leader", short(lead.SessionID), g.Todo)
			}
		}
	}
	go func() {
		wait, deadline := s.leaderResumeTime(), time.Now().Add(24*time.Hour)
		for time.Now().Before(deadline) {
			time.Sleep(wait)
			if _, ok := s.taskOfAny(ctx, lead.MachineID, lead.SessionID); !ok {
				return // the leader's task is closed: its workers were rehomed, nobody is left to tell
			}
			s.logf("task leader %s: trying the undelivered supervision message again", short(lead.SessionID))
			err := s.sendToLeaderNow(ctx, lead, text, sent)
			if relaySuppressed(err) {
				return // switched off while this retry waited
			}
			if err == nil {
				if delivered != nil {
					delivered(ctx)
				} else {
					s.ackSupervisePending(all)
					s.parkedDueTold(ctx, sent)
					s.turnsTold(ctx, receipts)
				}
				return
			}
			wait = leaderRetryBackoff(wait)
		}
	}()
}

// ---- the leader's task_review ----

// leaderTaskReview is task_review called by agent machine/session through its MCP server: only for a task leader.
func (s *Service) leaderTaskReview(ctx context.Context, machine, session string, args json.RawMessage) proto.ToolResult {
	_, agent, ok := s.leaderTask(ctx, machine, session)
	if !ok {
		return proto.TextResult("unknown tool task_review", true)
	}
	res, err := s.leaderReview(ctx, agent, args)
	if err != nil {
		return proto.TextResult(err.Error(), true)
	}
	return proto.TextResult(res, false)
}

// leaderReview is the leader agent's task_review on one of its direct workers: accepted or cancelled closes it and
// leaves its agent usable until idle after owner inactivity. Rework sends what is missing back to the worker (two
// rounds, then it goes up); needs_user sends the question to whoever handed the leader its task.
// No conclusion card, "Please verify" or other card goes to the user,
// and no other task is reachable: it is the leader's direct worker (its ledger names this leader) or nothing.
func (s *Service) leaderReview(ctx context.Context, lead TodoAgent, args json.RawMessage) (string, error) {
	var a struct {
		TodoID     string         `json:"todo_id"`
		Verdict    string         `json:"verdict"`
		ReworkKind string         `json:"rework_kind"`
		Checks     []ReviewCheck  `json:"checks"`
		Note       string         `json:"note"`
		Reason     string         `json:"reason"`
		Recommend  string         `json:"recommend"`
		Findings   *findingReview `json:"findings_review"`
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return "", fmt.Errorf("a task leader's task_review takes only the fields its schema lists (%v)", err)
	}
	a.Note = strings.TrimSpace(a.Note)
	a.ReworkKind = strings.TrimSpace(a.ReworkKind)
	switch a.Verdict {
	case reviewAccepted, reviewRework, reviewNeedsUser, reviewCancelled, reviewFindings:
	default:
		return "", errors.New("verdict must be accepted, rework, needs_user, cancelled or findings")
	}
	if a.ReworkKind != "" && a.Verdict != reviewRework {
		return "", errors.New("rework_kind applies to rework only")
	}
	if !validReworkKind(a.ReworkKind) {
		return "", errors.New("rework_kind must be external_input, substantive_new, repeated_error or low_roi_edge")
	}
	if a.Verdict == reviewRework && a.Note == "" && !slices.ContainsFunc(a.Checks, func(c ReviewCheck) bool { return !c.Met }) {
		return "", errors.New("rework: say what is missing (a check that is not met, or note)")
	}
	t, ok := s.todoNow(strings.TrimSpace(a.TodoID))
	if !ok {
		return "", fmt.Errorf("no task %q", a.TodoID)
	}
	if cur, supervised, reachable := s.leaderSupervisor(t); !supervised || !reachable || cur.MachineID != lead.MachineID || cur.SessionID != lead.SessionID {
		return "", fmt.Errorf("task %s is not one of your workers: you review the workers you handed out with task_plan, nothing else", t.ID)
	}
	if a.Verdict == reviewFindings {
		return s.reviewInvestigationFindings(ctx, t, a.Findings, a.Checks, lead.SessionID, func(cur Todo) bool {
			supervisor, supervised, reachable := s.leaderSupervisor(cur)
			return supervised && reachable && supervisor.MachineID == lead.MachineID && supervisor.SessionID == lead.SessionID
		})
	}
	if a.Findings != nil {
		return "", errors.New("record findings_review with verdict findings before changing task state")
	}
	if t.DoneAt != nil && (a.Verdict == reviewRework || a.Verdict == reviewNeedsUser) {
		return "", fmt.Errorf("task %s is done already (%s): hand out a new worker for what is left", t.ID, cmp.Or(t.DoneReason, "closed"))
	}
	if a.Verdict == reviewAccepted {
		if err := s.todoCloseDeclineError(ctx, t); err != nil {
			return "", err
		}
	}
	if a.Verdict == reviewAccepted || a.Verdict == reviewCancelled {
		if err := s.taskCloseActivityError(ctx, t, time.Now()); err != nil {
			return "", s.taskCloseToolError(ctx, s.deferTaskReview(ctx, t.ID, err))
		}
	}
	if a.Verdict == reviewAccepted && t.DoneAt == nil {
		if err := s.closeoutFollowUpError(ctx, t, ""); err != nil {
			return "", err
		}
		if _, why := s.ownerInstructionBlock(ctx, t); why != "" {
			return "", errors.New("accepted refused: " + why + ". Send the instruction back with rework, or ask the " +
				"conversation above to collect the owner's explicit confirmation")
		}
	}
	var up string // the conversation a needs_user goes to: whoever handed the leader its task
	if lt, ok := s.leaderTaskOf(ctx, lead); ok && lt.Supervision != nil {
		up = lt.Supervision.Supervisor
	}
	if a.Verdict == reviewNeedsUser && up == "" {
		return "", errors.New("your own task has nobody to hand a decision to: decide it yourself, or review the worker with the other verdicts")
	}
	checks := make([]ReviewCheck, 0, len(a.Checks))
	for _, c := range a.Checks {
		c.Criterion, c.Evidence = clip(oneLine(c.Criterion), 200), clip(strings.TrimSpace(c.Evidence), 600)
		if c.Criterion != "" {
			checks = append(checks, c)
		}
	}
	verdict, note := a.Verdict, clip(a.Note, 2000)
	over, overSafetyCap, overCount := false, false, 0
	var criteriaErr, findingErr error
	var closing *taskReviewClose
	if verdict == reviewAccepted || verdict == reviewCancelled {
		reason := cmp.Or(clip(oneLine(note), 80), "验收通过") // i18n-ignore: done_reason, as a conversation's review writes it
		if verdict == reviewCancelled {
			reason = "取消：" + cmp.Or(clip(oneLine(note), 80), "不做了") // i18n-ignore: done_reason, as a conversation's review writes it
		}
		closing = &taskReviewClose{reason: reason, accepted: verdict == reviewAccepted}
	}
	var cancelCheckedAgents []TodoAgent
	cancelWorkersGone := false
	if verdict == reviewCancelled {
		cancelCheckedAgents, cancelWorkersGone = s.checkStaleInvestigationCancelWorkers(ctx, t)
	}
	var guard func(Todo) bool
	if verdict == reviewAccepted || verdict == reviewCancelled {
		guard = func(cur Todo) bool {
			if !(verdict == reviewCancelled && staleInvestigationCancelAllowed(cur, cancelCheckedAgents, cancelWorkersGone)) {
				findingErr = s.investigationTransitionError(ctx, cur, cur.Supervision, nil)
			}
			if findingErr == nil {
				var acceptedChecks []ReviewCheck
				if verdict == reviewAccepted {
					acceptedChecks = checks
				}
				findingErr = s.receivedFindingsCloseError(cur, acceptedChecks)
			}
			return findingErr == nil
		}
	}
	v, _, err := s.updateSupervisionGuardedWithClose(ctx, t.ID, guard, func(v *Supervision) bool {
		if verdict == reviewAccepted {
			if criteriaErr = completeReviewCriteriaError(v.DoneWhen, a.Checks); criteriaErr != nil {
				return false
			}
		}
		if verdict == reviewRework {
			if blocked, safetyCap, count := v.reworkBlocked(a.ReworkKind); blocked {
				verdict, over, overSafetyCap, overCount = reviewNeedsUser, true, safetyCap, count
			}
		}
		r := TaskReview{At: time.Now().UTC(), Verdict: verdict, Checks: checks, Note: note}
		if verdict == reviewRework {
			round, stalled := v.applyReworkAccounting(a.ReworkKind)
			r.Round, r.ReworkKind, r.StalledReworkRounds = round, a.ReworkKind, stalled
		}
		if verdict == reviewAccepted && len(s.investigationReceivedFindings(t.ID)) > 0 {
			v.applyChecks(checks, r.At, "supervisor:"+lead.SessionID)
		}
		v.State = verdict
		v.Reviews = append(v.Reviews, r)
		if len(v.Reviews) > 20 {
			v.Reviews = v.Reviews[len(v.Reviews)-20:]
		}
		return true
	}, closing)
	if err != nil {
		return "", s.taskCloseToolError(ctx, s.deferTaskReview(ctx, t.ID, err))
	}
	if findingErr != nil {
		return "", findingErr
	}
	if criteriaErr != nil {
		return "", criteriaErr
	}
	if verdict == reviewNeedsUser && up == "" {
		up = s.settings(ctx).Main // reworked too often with nobody above the leader: the main conversation
	}
	if verdict != reviewAccepted { // by the leader's model and effort; an acceptance once it closed the worker (below)
		s.leaderTaskReviewed(lead, t.ID, verdict, v)
	}
	if verdict == reviewRework || verdict == reviewNeedsUser {
		s.setTaskStageKind(ctx, t.ID, "unverified", "", "", false)
	}
	b := &s.supervise
	b.mu.Lock()
	b.init()
	var keys []string
	for _, k := range []string{sigReview, sigIdle, sigUnjudged, sigOutageRecovery, sigNeedsYou, sigStalled, sigNoProgress, sigTimedOut, sigOrphaned, sigRecovered, sigEstimateDue} {
		key := t.ID + "|" + k
		delete(b.pending, key) // reviewed: what waited is answered
		keys = append(keys, key)
	}
	s.forgetSupervisePending(keys...)
	b.mu.Unlock()
	switch verdict {
	case reviewAccepted, reviewCancelled:
		if t.DoneAt == nil || t.StartError != "" {
			wasOpen := t.DoneAt == nil
			if wasOpen && closing.closed && verdict == reviewAccepted { // a failed start was closed already: no acceptance to count
				s.leaderTaskReviewed(lead, t.ID, verdict, v)
			}
		}
		if verdict == reviewAccepted {
			s.sendsAccepted(ctx, t.ID, time.Now()) // what was sent its agent before is answered: closed already, too (#7539)
		}
		if verdict == reviewCancelled {
			return fmt.Sprintf("Worker task %s is cancelled and closed; its conversation stays usable until its Agent is idle "+
				"and the user has been inactive for 10 minutes, then it is stopped and archived (its terminal stays).", t.ID), nil
		}
		return fmt.Sprintf("Worker task %s is accepted and closed; its agent is stopped once it stays still (its terminal stays).", t.ID), nil
	case reviewNeedsUser:
		why := cmp.Or(clip(oneLine(a.Reason), 200), clip(oneLine(note), 200))
		n := cmp.Or(overCount, maxReworkRounds)
		if over && a.Reason == "" {
			why = fmt.Sprintf("sent back %d times already, still short: %s", n, clip(oneLine(note), 160))
		}
		s.signal(superviseSignal{Todo: t.ID, Kind: sigDecide, To: up, Key: fmt.Sprintf("decide@%d@%s", v.Round, time.Now().UTC().Format(time.RFC3339Nano)),
			Detail: why, Ask: clip(oneLine(a.Recommend), 200)})
		if over {
			if overSafetyCap {
				return fmt.Sprintf("Worker task %s was sent back %d times already (overall rework safety cap %d): it now waits for a decision, which went up to whoever handed "+
					"you your task. Review it again once the answer reaches you.", t.ID, n, maxTotalReworkRounds), nil
			}
			return fmt.Sprintf("Worker task %s was sent back %d times already: it now waits for a decision, which went up to whoever handed "+
				"you your task. Review it again once the answer reaches you.", t.ID, n), nil
		}
		return fmt.Sprintf("Worker task %s waits for a decision, which went up to whoever handed you your task. You are told what "+
			"they decide; no card went to the user.", t.ID), nil
	}
	displayMax := maxTotalReworkRounds
	if isStalledReworkKind(a.ReworkKind) && v.Round <= maxReworkRounds {
		displayMax = maxReworkRounds
	}
	text := prompts.RenderSuperviseRework(prompts.SuperviseRework{Round: v.Round, Max: displayMax, Checks: reworkChecks(checks), Note: note})
	sent, err := s.sendToTask(ctx, t, text)
	if err != nil {
		return "", fmt.Errorf("recorded rework round %d, but sending it to the worker failed: %v", v.Round, err)
	}
	stalled := v.stalledReworkRounds()
	if isStalledReworkKind(a.ReworkKind) && v.Round == stalled {
		return fmt.Sprintf("Rework round %d of %d: %s. Its next stop comes back to you as a supervision message.", v.Round, maxReworkRounds, sent), nil
	}
	if a.ReworkKind != "" {
		return fmt.Sprintf("Rework round %d of %d (%s; stalled/low-ROI %d of %d): %s. Its next stop comes back to you as a supervision message.",
			v.Round, maxTotalReworkRounds, a.ReworkKind, stalled, maxReworkRounds, sent), nil
	}
	return fmt.Sprintf("Rework round %d of %d (stalled/low-ROI %d of %d): %s. Its next stop comes back to you as a supervision message.",
		v.Round, maxTotalReworkRounds, stalled, maxReworkRounds, sent), nil
}

// agentSupervisedSession: the open task agent machine/session works for is supervised by a task leader's agent.
func (s *Service) agentSupervisedSession(machine, session string) bool {
	t, ok := s.taskOf(context.Background(), s.canonical(machine), session)
	return ok && s.agentSupervised(t)
}

// rehomeLeaderlessWorkers gives the open workers of a leader whose own task is closed a supervisor that can act: the
// conversation above the leader (its own supervisor, else the main one), and with no conversation at all the worker is
// cancelled with the reason. It runs from the watch loop and every digest, and is idempotent: a rehomed worker is no
// longer leader-supervised.
func (s *Service) rehomeLeaderlessWorkers(ctx context.Context) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return
	}
	s.todos.mu.RLock()
	var workers []Todo
	for _, t := range s.todos.todos {
		if t.DoneAt == nil && t.Supervision != nil {
			workers = append(workers, copyTodoLocked(t))
		}
	}
	s.todos.mu.RUnlock()
	for _, t := range workers {
		s.rehomeWorker(ctx, t)
	}
}

// rehomeWorker rehomes worker task t if its leader's task is closed (see rehomeLeaderlessWorkers); it gives the task as
// it is after, and cancelled true when there was nobody to hand it to. The supervisor and the origin move together:
// the origin is where the task's notices and receipts go.
func (s *Service) rehomeWorker(ctx context.Context, t Todo) (cur Todo, cancelled bool) {
	lead, _, ok := s.leaderAbove(t)
	if !ok || lead.ID != "" && s.leaderOpen(lead) { // not leader-supervised, or its leader's task is open
		return t, false
	}
	up, _ := s.supervisorAboveTask(ctx, t)
	if up == "" || s.isLeaderRef(up) {
		if err := s.taskCloseActivityError(ctx, t, time.Now()); err != nil {
			s.logf("task %s: rehome deferred: %v", t.ID, err)
			return t, false
		}
		reason := "its task leader is gone, and there is no conversation above it to take it over" // i18n-ignore: done_reason
		closing := &taskReviewClose{reason: reason}
		if _, _, err := s.updateSupervisionWithClose(ctx, t.ID,
			func(v *Supervision) bool { v.State = reviewCancelled; return true }, closing); err != nil {
			s.logf("task %s: rehome: %v", t.ID, err)
			cur, _ = s.todoNow(t.ID)
			return cur, false
		}
		cur, _ = s.todoNow(t.ID)
		if closing.closed {
			s.logf("task %s: %s; cancelled", t.ID, reason)
		}
		return cur, closing.closed
	}
	s.logf("task %s: its task leader is gone; %s takes it over", t.ID, up)
	if _, err := s.editTodo(ctx, t.ID, db.TodoEdit{Origin: &up}); err != nil {
		s.logf("task %s: rehome: origin: %v", t.ID, err)
		return t, false // nothing changed yet: tried again at the next pass
	}
	s.todos.mu.Lock() // the stored row moved; memory follows (setTodoRow leaves the origin alone for every other writer)
	if m := s.todos.todos[t.ID]; m != nil {
		m.Origin = up
	}
	s.todos.mu.Unlock()
	s.todosChanged(ctx)
	var pending bool
	v, _, err := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
		v.Supervisor = up
		if v.State == reviewRework {
			v.State = reviewPending // what the leader asked for is with the worker: the new supervisor reviews what comes back
		}
		pending = v.state(t) == reviewPending
		return true
	})
	if err != nil {
		s.logf("task %s: rehome: %v", t.ID, err)
		return t, false
	}
	if pending {
		s.signalHandedOverReview(t.ID, up, reviewKey(&v), "它的 task leader 已经不在，这个任务交给你验收") // i18n-ignore: the supervision message is the assistant's prompt
	}
	cur, _ = s.todoNow(t.ID)
	return cur, false
}

// leaderOpen: the leader's own task is open.
func (s *Service) leaderOpen(t Todo) bool { return t.ID != "" && t.DoneAt == nil && !t.Cancelled() }

// mayReviewForLeader: conversation self supervises the task leader that supervises worker task t, so it can review the
// worker (a decision the leader sent up). The worker's supervisor stays the leader's agent: this never takes it over.
func (s *Service) mayReviewForLeader(ctx context.Context, self string, t Todo) bool {
	up, _ := s.supervisorAboveTask(ctx, t)
	if up == "" {
		return false
	}
	// The background supervisor stands in for main only when this worker really had a legacy notice for it. Being
	// configured as background never grants acceptance over a new task-leader worker (#5732). An attested round fork
	// shares only its base's already recorded notice, never a fresh acceptance grant.
	self = s.supervisionHoldKey(ctx, self)
	return up == self || s.isBackground(ctx, self) && !s.isLeader(ctx, up) && s.legacyBackgroundNotice(ctx, self, t.ID)
}

// leaderTaskOf is the open task leader agent lead leads.
func (s *Service) leaderTaskOf(ctx context.Context, lead TodoAgent) (Todo, bool) {
	t, ok := s.taskOfAny(ctx, lead.MachineID, lead.SessionID)
	return t, ok
}

// leaderResumeWait bounds how long resuming a closed leader agent may take before the message goes up to the conversation
// above it instead (Service.leaderResume shortens it in tests).
const leaderResumeWait = 3 * time.Minute

// leaderResumeTime is s's own leaderResumeWait. Tests set it on their Service, so a retry goroutine left over from one
// test (it may run for a day) never reads what another test's cleanup writes.
func (s *Service) leaderResumeTime() time.Duration {
	if s.leaderResume > 0 {
		return s.leaderResume
	}
	return leaderResumeWait
}

// leaderTaskIDOf is the task leader agent lead belongs to ("" if none).
func (s *Service) leaderTaskIDOf(lead TodoAgent) string {
	if t, ok := s.taskOfAny(context.Background(), lead.MachineID, lead.SessionID); ok {
		return t.ID
	}
	return ""
}

// leaderRetryBackoff caps how often an undelivered worker supervision message is tried again.
func leaderRetryBackoff(d time.Duration) time.Duration {
	if d < 10*time.Minute {
		return d * 2
	}
	return 30 * time.Minute
}
