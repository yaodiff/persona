package assistant

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The main conversation in charge of any agent (#5112, docs/assistant/unified-entry.md"Supervision"): stopping and redirecting one
// (agent_interrupt), taking one it did not hand out under its supervision (agent_supervise), and the agents' own way
// up to their supervisor (report_to_supervisor, one of the tools an agent gets, not the assistant; agentToolNames).

// agentToolNames are the tools of the agents (not the assistant's conversations) that are this manager's own; their
// descriptions and schemas are in prompts/mcp (docs/assistant/prompts.md). Each needs to know which agent calls it: the node
// names the session of the agent's pane for them (node.callerTools). The list lives in assistantwire so the node reads
// the same one (#9089).
var agentToolNames = assistantwire.AgentToolNames

// AgentTools are the agents' own tools (mcpTools for an agent).
func (s *Service) AgentTools() []proto.Tool { return toolsNamed(agentToolNames) }

func isAgentTool(name string) bool {
	for _, n := range agentToolNames {
		if n == name {
			return true
		}
	}
	return false
}

func (s *Service) externalSessionRefusal(ctx context.Context, self string, am Device, c proto.Session) error {
	if c.SourceKind != proto.SourceRuntime || c.StartedByPersona {
		return nil
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return fmt.Errorf("cannot verify the open task assignment of %s on %s (external runtime session, source_kind=%s): %w. Use agent_read instead",
			agentLabel(c), am.Name, c.SourceKind, err)
	}
	if t, ok := s.openTodoOfAgent(am.ID, c.SessionID); ok && self != "" &&
		(t.Origin == self || supervisorOf(t) == self || s.backgroundSupervises(ctx, self, t)) {
		return nil
	}
	return fmt.Errorf("%s on %s is an external runtime session (source_kind=%s) that was not started by Persona and is not assigned to an open task of this conversation. Use agent_read instead",
		agentLabel(c), am.Name, c.SourceKind)
}

// ---- agent_interrupt ----

// interruptSettleWait is how long agent_interrupt waits for a stopped turn to end before saying it has not yet;
// interruptSettleEvery how often it looks meanwhile (tests shorten both).
var (
	interruptSettleWait  = 15 * time.Second
	interruptSettleEvery = 500 * time.Millisecond
)

// toolAgentInterrupt stops an agent's turn (session.interrupt) and, with then, sends it the new instruction right
// after: steering instead of agent_send's queueing behind the turn. An agent not working is not stopped; then still
// goes to it. "Stopped" is said only once the turn has ended: the runtime saw it end (settled), or the agent no longer
// reads as working; otherwise it says the stop was asked for and the instruction is queued behind the turn (#8097).
func (s *Service) toolAgentInterrupt(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	var a struct {
		Machine   string `json:"machine"`
		SessionID string `json:"session_id"`
		Then      string `json:"then"`
	}
	_ = json.Unmarshal(args, &a)
	am, c, err := s.findAgent(ctx, a.Machine, a.SessionID)
	if err != nil {
		return "", err
	}
	if c.Assistant {
		return "", fmt.Errorf("that is an assistant conversation, not a coding agent: it is the user's to stop")
	}
	then := strings.TrimSpace(a.Then)
	if c.SourceKind != "" && (c.Capabilities == nil || !c.Capabilities.Interrupt || then != "" && !c.Capabilities.Send) {
		return "", fmt.Errorf("this agent can only be read: %s", c.ReadOnlyReason)
	}
	if err := s.externalSessionRefusal(ctx, self, am, c); err != nil {
		return "", err
	}
	if stoppedAgent(c) { // read-only (#4754): nothing runs to stop; a resume is agent_send's (#8099)
		return "", fmt.Errorf("%s has already stopped: nothing to interrupt, and nothing was sent. To go on with it and its "+
			"context, use agent_send with resume true and resume_why; otherwise start a replacement: for its task, task_plan "+
			"with continues (the task's todo id) and continues_why; for work on no task, agent_start", agentLabel(c))
	}
	if err := s.exitAllowed(ctx, self, am, c, then, 0); err != nil { // refused before the turn is stopped or the user asked
		return "", err
	}
	summary := i18n.T(s.lang(ctx), "assistant-tasks.interrupt_summary", i18n.Args{"agent": agentLabel(c), "machine": am.Name})
	if then != "" {
		summary += "\n\n" + then
	}
	if ok, outcome, err := s.confirmOutbound(ctx, machine, self, "agent_interrupt", then, summary, args); err != nil {
		return "", err
	} else if !ok {
		return outboundNotDone(outcome, "The user declined on the phone: the agent goes on as it was.", "the agent goes on as it was"), nil
	}
	// Check again after approval, since the task assignment can change while the phone is waiting.
	if err := s.externalSessionRefusal(ctx, self, am, c); err != nil {
		return "", err
	}
	if err := s.exitAllowed(ctx, self, am, c, then, 0); err != nil {
		return "", err
	}
	// Only a turn under way is stopped: an agent still starting has none yet (then is queued for it, as agent_send does).
	working := c.Status == proto.StatusWorking
	stopped := false // its turn is known to have ended
	// over: the node found its turn already ended, only its background work keeping it Working (#10828): nothing was
	// interrupted and that work goes on, so it is not said to have been stopped.
	over := false
	if working {
		var res struct {
			Settled bool `json:"settled"`
		}
		err := s.call(ctx, am.ID, "session.interrupt", map[string]any{"session_id": c.SessionID}, &res)
		var re *rpc.Error
		over = errors.As(err, &re) && re.Code == proto.CodeTurnOverBackground // its turn ended while the phone was asked
		if err != nil && !over {
			return "", fmt.Errorf("stop: %w", err)
		}
		s.logf("agent_interrupt %s on %s (from %s)", short(c.SessionID), am.ID, short(self))
		stopped = !over && (res.Settled || s.interruptLanded(ctx, am.ID, c.SessionID))
	}
	const overSaid = "%s on %s had already ended its turn: nothing was interrupted, and its background work is still running " +
		"(it can be stopped in the terminal on that computer). Do not tell the user it stopped."
	if then == "" {
		if !working {
			return fmt.Sprintf("%s on %s was not working: nothing to stop.", agentLabel(c), am.Name), nil
		}
		if over {
			return fmt.Sprintf(overSaid, agentLabel(c), am.Name), nil
		}
		if !stopped {
			return fmt.Sprintf("Asked %s on %s to stop, but its turn has not ended yet: it still reads as working after %s. "+
				"Do not tell the user it stopped; look again with agent_read before counting on it.", agentLabel(c), am.Name, interruptSettleWait), nil
		}
		return fmt.Sprintf("Stopped %s on %s: its turn was cut off, the session stays (agent_send to go on). What it did already is not undone.",
			agentLabel(c), am.Name), nil
	}
	// The stop takes a moment to land; session.send queues the text if the agent still reads as working, and a
	// closed agent is resumed first (as agent_send does), which can take minutes.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	if err := s.externalSessionRefusal(sctx, self, am, c); err != nil {
		if working && !over {
			return "", fmt.Errorf("the stop was requested, but the new instruction was withheld: %w", err)
		}
		return "", err
	}
	var sent struct {
		Queued bool `json:"queued"` // the runtime holds it behind a turn still running
	}
	note, err := s.noteAgentSend(sctx, self, am.ID, c.SessionID, "", then) // more from its task's conversation reopens an accepted task (#6726, #7539)
	if err == nil {
		err = s.call(sctx, am.ID, DeviceSessionSend, sendParams(c.SessionID, then, false), &sent)
	}
	note.sent(sctx, err) // an unknown answer counts for the agent's next turn: it may still arrive
	if err != nil {
		if working && !over {
			return "", fmt.Errorf("stopped it, but sending the new instruction failed: %w", err)
		}
		return "", err
	}
	if t, ok := s.taskOf(ctx, am.ID, c.SessionID); ok && (t.Origin == self || t.Supervision != nil && t.Supervision.Supervisor == self) {
		// The conversation shows what it told its task, as for agent_send to a task (routed); marked when it cut in, and
		// naming the run it told (#10465).
		if _, err := s.appendFeed(ctx, self, feedRouted, t.ID, map[string]any{"text": then, "interrupted": working && !over, "from": routedFromAssistant,
			"machine_id": am.ID, "session_id": c.SessionID}); err != nil {
			s.logf("task %s: routed feed: %v", t.ID, err)
		}
	}
	if !working {
		return fmt.Sprintf("%s on %s was not working: sent the new instruction.", agentLabel(c), am.Name), nil
	}
	if over {
		said := fmt.Sprintf(overSaid, agentLabel(c), am.Name)
		if sent.Queued {
			return said + " The new instruction is queued behind what it is doing now and is read when that ends.", nil
		}
		return said + " The new instruction was sent.", nil
	}
	if !stopped {
		return fmt.Sprintf("Asked %s on %s to stop, but its turn has not ended yet (it still reads as working after %s): the new "+
			"instruction is queued and is read only when that turn ends. Do not tell the user it stopped; look again with agent_read.",
			agentLabel(c), am.Name, interruptSettleWait), nil
	}
	if sent.Queued {
		return fmt.Sprintf("Stopped %s on %s; the new instruction is queued behind what it is doing now and is read when that ends. "+
			"What it did already is not undone.", agentLabel(c), am.Name), nil
	}
	return fmt.Sprintf("Stopped %s on %s and sent the new instruction. What it did already is not undone.", agentLabel(c), am.Name), nil
}

// interruptLanded waits up to interruptSettleWait for the agent's session to stop reading as working, after it was asked
// to stop: true once it does (or the session is gone), false when it still works at the end.
func (s *Service) interruptLanded(ctx context.Context, machine, id string) bool {
	deadline := time.Now().Add(interruptSettleWait)
	for {
		if c, ok := s.session(ctx, machine, id); !ok || c.Status != proto.StatusWorking {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(interruptSettleEvery):
		}
	}
}

// ---- agent_supervise ----

// toolAgentSupervise takes agent machine/session under the supervision of conversation self, the main conversation or a
// direction's lead (within leadTakeAllowed, #12953): a task of it already
// gets the new done_when / expect_minutes; an agent of no task becomes one (a todo of this conversation with the
// agent on it, as an answered agent_start makes); another conversation's task, or the user's own todo, stays theirs.
func (s *Service) toolAgentSupervise(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var a struct {
		taskContractInput
		Machine       string   `json:"machine"`
		SessionID     string   `json:"session_id"`
		Title         string   `json:"title"`
		Role          string   `json:"role"`
		DoneWhen      []string `json:"done_when"`
		ExpectMinutes int      `json:"expect_minutes"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	contract, err := a.validatedContract()
	if err != nil {
		return "", err
	}
	if !s.supervises(ctx, self) && !s.liveSideSupervisor(ctx, self) {
		return "", fmt.Errorf("only the main conversation and a direction's lead supervise agents: from here, send it without a task")
	}
	if a.Role != "" && a.Role != roleLeader {
		return "", errors.New(`role must be "leader" or omitted for a worker`)
	}
	am, c, err := s.findAgent(ctx, a.Machine, a.SessionID)
	if err != nil {
		return "", err
	}
	if c.Assistant {
		return "", fmt.Errorf("that is an assistant conversation, not a coding agent")
	}
	if err := s.externalSessionRefusal(ctx, self, am, c); err != nil {
		return "", err
	}
	if !s.supervises(ctx, self) && !s.sideOwnsAgent(ctx, self, am.ID, c.SessionID) {
		return "", fmt.Errorf("only the main conversation and a direction's lead supervise agents other than those this conversation started or supervises: from here, send it without a task")
	}
	if err := s.leadTakeAllowed(ctx, self, am, c); err != nil {
		return "", fmt.Errorf("%w. Nothing changed", err)
	}
	if err := s.leaderOnWorkMachine(&PlanTask{Role: a.Role}, am); err != nil {
		return "", err
	}
	todo, made, err := s.adoptAgentFor(ctx, self, self, am, c, a.Title, "", i18n.T(s.lang(ctx), "assistant-tasks.adopt_notes",
		i18n.Args{"agent": agentLabel(c), "machine": am.Name, "cwd": c.Cwd}), a.DoneWhen, a.ExpectMinutes,
		adoptionOptions{update: true, role: a.Role, contract: &contract})
	if err != nil {
		return "", err
	}
	if made {
		s.taskCard(ctx, self, todo.ID, wakeActive)
	}
	if a.Role == roleLeader {
		entry := s.adoptedTaskEntry(ctx, am.ID, c)
		note := strings.Join([]string{prompts.RenderTaskHandoff(entry), prompts.RenderTaskLeader(prompts.TaskLeader{Terminal: entry.Terminal()})}, "\n\n")
		params := map[string]any{"session_id": c.SessionID, "text": note, "client_msg_id": "leader-note:" + todo.ID}
		if err := s.call(ctx, am.ID, DeviceSessionSendOnce, params, nil); err != nil {
			return "", fmt.Errorf("task %s has its leader, but delivery of the leader instructions was not confirmed: %w. Retry agent_supervise with the same role", todo.ID, err)
		}
		return fmt.Sprintf("%s leads task %s (%q), supervised by %s. Its leader instructions were sent. done_when %s.", agentLabel(c), todo.ID, todo.Title, s.conversationWords(ctx, todo.Supervision.Supervisor), criteriaWords(nil, todo.Supervision)), nil
	}
	if !made {
		return fmt.Sprintf("%s already works for your task %s (%q): you supervise it, done_when %s.", agentLabel(c), todo.ID, todo.Title,
			criteriaWords(nil, todo.Supervision)), nil
	}
	sup := self
	if todo.Supervision != nil && todo.Supervision.Supervisor != "" {
		sup = todo.Supervision.Supervisor
	}
	return fmt.Sprintf("%s on %s is now your task %s (%q, its card is in the conversation): %s. done_when %s. The agent was not told: "+
		"if it should work to these criteria or report to its supervisor (report_to_supervisor), tell it with agent_send.",
		agentLabel(c), am.Name, todo.ID, todo.Title, s.followUpWords(ctx, self, sup), criteriaWords(nil, todo.Supervision)), nil
}

// sideOwnsAgent reports whether side conversation self owns agent machineID/sessionID: it currently supervises its open
// task, dispatched an unsupervised open task for it, or started/supervised its most recent closed task.
func (s *Service) sideOwnsAgent(ctx context.Context, self, machineID, sessionID string) bool {
	if self == "" || s.ensureTodosLoaded(ctx) != nil {
		return false
	}
	if t, ok := s.openTodoOfAgent(machineID, sessionID); ok {
		if t.Supervision != nil && t.Supervision.Supervisor != "" {
			return t.Supervision.Supervisor == self
		}
		return t.Origin == self
	}
	if prev, ok := s.closedTodoOfAgent(machineID, sessionID); ok {
		return prev.Origin == self || prev.Supervision != nil && prev.Supervision.Supervisor == self
	}
	return false
}

// adoptAgent puts agent c of machine am under conversation self's supervision (agent_supervise, agent_send with a
// task): a task of self already gets the new done_when / expect_minutes (none: kept; update false: left as it is, for
// the caller to write later), an agent of no task becomes one
// (a todo with notes and the agent linked, named by adoptedTitle from title and prompt: made true; the caller puts its
// card in the feed, taskCard); another
// conversation's task, or the user's own todo, stays theirs (an error). If the agent cannot be linked to the todo just
// made, that todo is closed (no empty task stays).
func (s *Service) adoptAgent(ctx context.Context, self string, am Device, c proto.Session, title, prompt, notes string, doneWhen []string,
	expect int, update bool) (Todo, bool, error) {
	return s.adoptAgentAs(ctx, self, am, c, title, prompt, notes, doneWhen, expect, update, false)
}

// adoptAgentAs is adoptAgent; with quiet the todo it makes is quiet whatever self's own tasks are (an agent that became
// a task by itself, agent_tasks.go). An assistant's conversation is refused whatever its row's Assistant flag says
// (assistantsConversation, #10801).
func (s *Service) adoptAgentAs(ctx context.Context, self string, am Device, c proto.Session, title, prompt, notes string, doneWhen []string,
	expect int, update, quiet bool) (Todo, bool, error) {
	return s.adoptAgentFor(ctx, self, self, am, c, title, prompt, notes, doneWhen, expect, adoptionOptions{update: update, quiet: quiet})
}

type adoptionOptions struct {
	update, quiet bool
	role          string
	contract      *taskContract // set by agent_supervise, including when both options were omitted
}

// adoptAgentFor is adoptAgentAs with the todo's origin and supervisor kept separate (#5732). A side conversation
// keeps its dispatch origin while the main conversation supervises the new task.
func (s *Service) adoptAgentFor(ctx context.Context, self, supervisor string, am Device, c proto.Session, title, prompt, notes string, doneWhen []string,
	expect int, opts adoptionOptions) (Todo, bool, error) {
	if s.assistantsConversation(ctx, am.ID, c) {
		return Todo{}, false, errors.New("that is an assistant conversation, not a coding agent")
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return Todo{}, false, err
	}
	// One adoption at a time: two at once would both find the agent on no task and make two.
	s.adoptMu.Lock()
	defer s.adoptMu.Unlock()
	doneWhen, expect = cleanDoneWhen(doneWhen), max(0, min(expect, 24*60))
	if t, ok := s.openTodoOfAgent(am.ID, c.SessionID); ok {
		if err := othersTask(c, t, self); err != nil {
			return Todo{}, false, err
		}
		if opts.role == roleLeader {
			if _, _, leads := s.leaderTask(ctx, am.ID, c.SessionID); !leads {
				return Todo{}, false, fmt.Errorf("the Agent already works on open task %s as a worker; the leader role is assigned when adopting an Agent on no open task", t.ID)
			}
		}
		if !opts.update {
			return t, false, nil
		}
		if opts.contract != nil {
			brief, _ := s.briefOfTodo(t.ID)
			if opts.contract.changesFrom(brief) != (taskContract{}) {
				return Todo{}, false, errors.New("this to-do already has a task contract. Use task_scope to change work_mode or completion_mode")
			}
		}
		// Its criteria change only through the brief's log (task_brief_scope.go): the same rules as task_scope.
		v, err := s.rescope(ctx, t, self, supervisor, "agent_supervise", doneWhen, expect)
		if err != nil {
			return Todo{}, false, err
		}
		t.Supervision = &v
		s.restage(ctx, t.ID, nil) // by its new criteria; a read-only start still holds
		return t, false, nil
	}
	// Adoption does not start the Agent with a read-only constraint.
	if opts.role == roleLeader && len(doneWhen) == 0 {
		return Todo{}, false, errors.New("a writable task leader needs done_when with at least one checkable result")
	}
	previous, continuing := s.closedTodoOfAgent(am.ID, c.SessionID)
	// Only an explicit adoption/takeover continues the closed card's brief. A newly titled agent_send task is new work,
	// even when it reuses the same Agent; it must not rewrite the old card's scope or lineage (#5732).
	continuing = continuing && opts.update
	if continuing {
		if succ, ok := s.openSuccessor(previous.ID); ok {
			return Todo{}, false, errContinued(previous.ID, succ)
		}
		if opts.contract != nil {
			brief, _ := s.briefOfTodo(previous.ID)
			if opts.contract.changesFrom(brief) != (taskContract{}) {
				return Todo{}, false, errors.New("a takeover keeps the previous task contract. Adopt the Agent first, then use task_scope to change work_mode or completion_mode")
			}
		}
	}
	title, retitle := s.adoptedTitle(ctx, c, title, prompt)
	todo, err := s.newTodo(ctx, title, notes, self, "", opts.quiet || s.quietTask(ctx, self))
	if err != nil {
		return Todo{}, false, err
	}
	if retitle {
		s.nameTodoLater(ctx, todo.ID, title, cmp.Or(strings.TrimSpace(prompt), title))
	}
	var v Supervision
	var rollback *continuationRollback
	if continuing {
		// A takeover inherits the brief and changes its scope before any ledger or Agent link is committed. A rejected
		// risky criterion therefore aborts the whole adoption instead of surviving in an unapproved open card (#5732).
		v, rollback, err = s.continueBriefFor(ctx, self, supervisor, previous, todo, notes, doneWhen, expect)
		if err != nil {
			s.dropContinuation(ctx, todo.ID, rollback, err)
			return Todo{}, false, fmt.Errorf("continue the previous task: %w", err)
		}
	} else {
		v, _, err = s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
			v.Supervisor, v.DoneWhen, v.ExpectMinutes = supervisor, doneWhen, expect
			if s.isLeader(ctx, supervisor) || s.liveSideSupervisor(ctx, supervisor) {
				v.SupervisorSource = "plan"
			}
			return true
		})
		if err != nil {
			s.dropAdoption(ctx, todo.ID, err)
			return Todo{}, false, fmt.Errorf("supervise it: %w", err)
		}
		origin := s.callerOrigin(ctx, self)
		if opts.contract != nil {
			origin.taskContract = opts.contract.withDefaultWorkMode(false)
		}
		if err := s.briefFor(ctx, origin, todo, doneWhen, expect); err != nil && opts.contract != nil {
			s.dropAdoption(ctx, todo.ID, err)
			return Todo{}, false, fmt.Errorf("keep the task contract: %w", err)
		}
	}
	s.restage(ctx, todo.ID, nil)
	todo.Supervision = &v
	source, via := (PlanTask{Role: opts.role}).linkSource()
	if err := s.linkTodoAgentWithSource(ctx, todo.ID, am.ID, c.SessionID, title, c.Cwd, c.Agent, "", source, via); err != nil {
		if continuing {
			s.dropContinuation(ctx, todo.ID, rollback, err)
		} else {
			s.dropAdoption(ctx, todo.ID, err)
		}
		return Todo{}, false, fmt.Errorf("link the agent: %w", err)
	}
	s.logf("adopt %s on %s → task %s", short(c.SessionID), am.ID, todo.ID)
	return todo, true, nil
}

// othersTask refuses an open todo t of agent c that is not conversation self's to supervise: the user's own todo, or
// another conversation's task; nil when it is self's.
func othersTask(c proto.Session, t Todo, self string) error {
	switch {
	case t.Origin == "":
		return fmt.Errorf("%s is on the user's own todo %q (%s): it stays theirs", agentLabel(c), t.Title, t.ID)
	case t.Origin != self && (t.Supervision == nil || t.Supervision.Supervisor != self):
		return fmt.Errorf("%s works for task %q (%s) of another conversation: it stays that conversation's", agentLabel(c), t.Title, t.ID)
	}
	return nil
}

// leadTakeAllowed is the boundary within which conversation self, a direction's lead, takes agent c of machine am
// under its own supervision (agent_send with a task, also when resuming it, and agent_supervise; #12953), so a lead
// does its own work without the main conversation sending for it. The main conversation is not limited here.
//
//   - An agent on an open task stays that task's: another conversation's or the user's own (named, with whose it is);
//     one on self's own task goes on under it, as from the main conversation.
//   - An agent on no open task (its earlier task, of any direction, is closed) becomes a new task of self; the closed
//     to-do and its supervisor are left as they are. Moving an open task between supervisors stays agent_transfer's.
//   - An agent waiting for the user (a question, plan, permission or dialog) is the user's to answer first.
//   - An agent busy on work no task tracks that Persona did not start (a terminal the user opened) may be the user's
//     own run: not taken until it is idle. One Persona started (the lead's own agent_start) is taken as it runs.
func (s *Service) leadTakeAllowed(ctx context.Context, self string, am Device, c proto.Session) error {
	if s.isMain(ctx, self) || !s.isLeader(ctx, self) {
		return nil
	}
	if c.State == proto.StateNeedsYou || c.PendingInteractions > 0 {
		return fmt.Errorf("%s is waiting for the user (a question, plan, permission or dialog): it is theirs to answer first", agentLabel(c))
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	if t, ok := s.openTodoOfAgent(am.ID, c.SessionID); ok {
		if t.Origin == "" {
			return othersTask(c, t, self)
		}
		if sup := cmp.Or(supervisorOf(t), t.Origin); sup != self && t.Origin != self {
			return fmt.Errorf("%s works for task %q (%s), supervised by %s: it stays theirs until that task is closed or "+
				"its supervisor hands it over", agentLabel(c), t.Title, t.ID, s.conversationWords(ctx, sup))
		}
		return nil
	}
	if !stoppedAgent(c) && c.Status == proto.StatusWorking && !c.StartedByPersona {
		return fmt.Errorf("%s is busy on work no task tracks that Persona did not start (it may be the user's own run): send "+
			"it without task, or wait until it is idle", agentLabel(c))
	}
	return nil
}

// conversationWords names conversation id in a refusal: the main conversation, a direction's lead, or the conversation.
func (s *Service) conversationWords(ctx context.Context, id string) string {
	switch {
	case s.isMain(ctx, id):
		return "the main conversation (" + id + ")"
	case s.isLeader(ctx, id):
		return "the direction lead " + id
	}
	return "conversation " + id
}

// ---- agent_send with a task ----

// sendTask is agent_send's task: new work handed to an agent that runs already becomes a supervised task of the main
// conversation, as agent_supervise makes one, so it is tracked like a task_plan task instead of only said.
type sendTask struct {
	Title         string   `json:"title"`
	DoneWhen      []string `json:"done_when"`
	ExpectMinutes int      `json:"expect_minutes"`
}

// givenTask is agent_send's task as sent, nil for a reply (#3386): left out, null, or an object of only title,
// done_when and expect_minutes that says nothing (sendTask.none), which runtimes that fill in every field of a tool's
// schema send for a reply. Anything else stays a task, as before: another type, a field of the wrong type or a key the
// task has not (a misspelled title) must not turn a hand-off into an untracked reply.
func givenTask(raw json.RawMessage) *sendTask {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var t sendTask
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&t); err == nil && t.none() {
		return nil
	}
	return &t
}

// none: task has only blank fields (no title, no non-blank done_when, no expect_minutes above 0); any real value in one
// of them makes it a hand-off.
func (t sendTask) none() bool {
	return strings.TrimSpace(t.Title) == "" && len(cleanDoneWhen(t.DoneWhen)) == 0 && t.ExpectMinutes <= 0
}

// handedTask is the task agent_send with a task put the agent on: made now, or self's own that it worked for already
// (then task's criteria are written once the text went: a send that fails changes nothing).
type handedTask struct {
	todo       Todo
	made       bool
	self       string
	supervisor string
	task       sendTask
	// wake: its card's (taskCard), wakeResumed when the agent was closed and the message resumes it
	wake string
	run  SessionRef // the agent the text goes to, which its routed line names (#10465)
}

// sendTaskAllowed refuses agent_send's task before anything is sent: an agent working for the user's own todo or
// another conversation's task stays theirs; one on a task of self's with another title would put other work under
// that task; and /exit is no work. Any conversation may hand work out, with main supervising it (#5732).
func (s *Service) sendTaskAllowed(ctx context.Context, self string, am Device, c proto.Session, task sendTask, text string) error {
	if exitText(text) {
		return fmt.Errorf("%s closes the agent, it hands it no work: send it without task", strings.TrimSpace(text))
	}
	if err := s.leadTakeAllowed(ctx, self, am, c); err != nil {
		return fmt.Errorf("%w; nothing was sent", err)
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	t, ok := s.openTodoOfAgent(am.ID, c.SessionID)
	if !ok {
		return nil
	}
	if err := othersTask(c, t, self); err != nil {
		return fmt.Errorf("%w; nothing was sent (send it without task to only send it)", err)
	}
	return otherWork(c, t, task)
}

// exitText: text is only the agents' own command to quit, which the node runs by closing the agent (node exitCommand).
func exitText(text string) bool {
	t := strings.TrimSpace(text)
	return t == "/exit" || t == "/quit"
}

// supervisedElsewhere is the task agent c works for when a conversation other than self supervises it: a reply to it
// goes as a side conversation, and /exit to it is refused (exitAllowed). Ownership that cannot be read is an error, so
// a reply never goes without its provenance. The background supervisor speaks to the tasks no leader supervises as
// their supervisor (supervise_background.go).
func (s *Service) supervisedElsewhere(ctx context.Context, self string, am Device, c proto.Session) (Todo, bool, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return Todo{}, false, err
	}
	t, ok := s.taskOf(ctx, am.ID, c.SessionID)
	return t, ok && t.Supervision != nil && t.Supervision.Supervisor != self && !s.backgroundSupervises(ctx, self, t), nil
}

// exitAllowed refuses /exit or /quit to an agent on a task another conversation supervises (#3537): a plain
// agent_send of it, or agent_interrupt's then, before anything is stopped, sent or confirmed on the phone. The node
// closes an agent only for the bare command without attachments (node send.go), so wrapped as a side conversation it
// would be typed in as a prompt while the tool said it went; and closing the agent that conversation's task runs on is
// not this conversation's to do. With attachments the text is no command, and goes as any reply does.
func (s *Service) exitAllowed(ctx context.Context, self string, am Device, c proto.Session, text string, attachments int) error {
	if !exitText(text) || attachments > 0 {
		return nil
	}
	t, theirs, err := s.supervisedElsewhere(ctx, self, am, c)
	if err != nil || !theirs {
		return err
	}
	return exitRefused(c, t, text, attachments)
}

// exitRefused is exitAllowed's answer once c is known to work for t, a task another conversation supervises: the
// refusal for a bare /exit or /quit, nil for any other text. A tool that asked the phone first reads the task again
// after the answer and checks with it, since the agent may have become that task's while the user was asked.
func exitRefused(c proto.Session, t Todo, text string, attachments int) error {
	if !exitText(text) || attachments > 0 {
		return nil
	}
	return fmt.Errorf("%s works for task %q (%s) of another conversation: %s asks to close the agent that task runs on, which is "+
		"not this conversation's to do; nothing was sent or stopped. The user can close it in the app, or with agent_archive "+
		"and close once it is idle and they confirm on the phone", agentLabel(c), t.Title, t.ID, strings.TrimSpace(text))
}

// otherWork refuses task for agent c on self's own task t when its title is another (other work, not to be filed under
// t); an empty title is more of t.
func otherWork(c proto.Session, t Todo, task sendTask) error {
	if title := clip(oneLine(task.Title), 40); title != "" && title != clip(oneLine(t.Title), 40) {
		return fmt.Errorf("%s already works for your task %s (%q), not %q; nothing was sent. More of that task: send it with that "+
			"title, or without task. Other work: close that task first (task_review), or hand the work to another agent",
			agentLabel(c), t.ID, t.Title, title)
	}
	return nil
}

// adoptedTitle names the todo of an agent adopted as a task (#3020), as task_plan names its tasks (taskTitle): the
// assistant's title when it is a real one, else it or the instruction (prompt) cleaned now, and retitle for the model to
// name it later. The session's title or label only when neither leaves anything.
func (s *Service) adoptedTitle(ctx context.Context, c proto.Session, given, prompt string) (title string, retitle bool) {
	if strings.TrimSpace(given) != "" || strings.TrimSpace(prompt) != "" {
		if title, retitle = s.taskTitle(ctx, given, prompt); title != "" {
			return title, retitle
		}
	}
	return cutRunes(oneLine(cmp.Or(strings.TrimSpace(c.Title), agentLabel(c))), todoTitleMax(s.hello(ctx).Locale)), false
}

// handTask puts the agent under self's supervision for task before text is sent to it (adoptAgent): text is the new
// todo's notes, and its title when task has none. An agent on a task of self's stays on it, unchanged until the send.
func (s *Service) handTask(ctx context.Context, self string, am Device, c proto.Session, task sendTask, text string) (*handedTask, error) {
	// Decided under adoptMu (adoptAgent): another hand-off may have put the agent on a task since sendTaskAllowed.
	// A direction lead owns its hand-off, and a live side conversation owns agents it started or already supervises;
	// otherwise the main conversation supervises.
	supervisor := s.settings(ctx).Main
	if s.isLeader(ctx, self) || s.liveSideSupervisor(ctx, self) && s.sideOwnsAgent(ctx, self, am.ID, c.SessionID) {
		supervisor = self
	}
	todo, made, err := s.adoptAgentFor(ctx, self, supervisor, am, c, task.Title, text, text, task.DoneWhen, task.ExpectMinutes, adoptionOptions{})
	if err != nil {
		return nil, fmt.Errorf("%w; nothing was sent", err)
	}
	if made {
		wake := wakeActive
		if c.Status == proto.StatusOffline || stoppedAgent(c) { // closed: the send (or its resume, #8099) starts it first
			wake = wakeResumed
		}
		return &handedTask{todo: todo, made: true, self: self, supervisor: supervisor, wake: wake, run: SessionRef{MachineID: am.ID, SessionID: c.SessionID}}, nil
	}
	if err := otherWork(c, todo, task); err != nil {
		return nil, err
	}
	return &handedTask{todo: todo, self: self, supervisor: supervisor, task: task, run: SessionRef{MachineID: am.ID, SessionID: c.SessionID}}, nil
}

// sent: the text went. A task made for it gets its card in the conversation; one it was on already gets task's
// criteria; either way what it was told goes in under the task (routed, as a message on the task's card does). The
// tool's answer says which; nothing without a task.
func (h *handedTask) sent(ctx context.Context, s *Service, text string) string {
	if h == nil {
		return ""
	}
	refused := ""
	if h.made {
		s.taskCard(ctx, h.self, h.todo.ID, h.wake)
		if h.todo.Supervision != nil && h.todo.Supervision.Supervisor != "" && h.todo.Supervision.Supervisor != h.self {
			s.taskCard(ctx, h.todo.Supervision.Supervisor, h.todo.ID, h.wake)
		}
	} else {
		doneWhen, expect := cleanDoneWhen(h.task.DoneWhen), max(0, min(h.task.ExpectMinutes, 24*60))
		s.releaseHold(ctx, h.todo.ID)                                                          // the supervisor sent its agent on: follow-ups go again (decision.go)
		v, err := s.rescope(ctx, h.todo, h.self, h.supervisor, "agent_send", doneWhen, expect) // through the brief's log, as task_scope
		if err != nil {
			s.logf("task %s: supervision: %v", h.todo.ID, err)
			refused = " Its done_when was NOT changed: " + err.Error()
		} else {
			h.todo.Supervision = &v
		}
	}
	if _, err := s.appendFeed(ctx, h.self, feedRouted, h.todo.ID, map[string]string{"text": text, "from": routedFromAssistant,
		"machine_id": h.run.MachineID, "session_id": h.run.SessionID}); err != nil {
		s.logf("task %s: routed feed: %v", h.todo.ID, err)
	}
	if !h.made {
		return fmt.Sprintf(" It already works for your task %s (%q): no new task was made, done_when %s.", h.todo.ID, h.todo.Title,
			criteriaWords(nil, h.todo.Supervision)) + refused
	}
	sup := h.supervisor
	if h.todo.Supervision != nil && h.todo.Supervision.Supervisor != "" {
		sup = h.todo.Supervision.Supervisor
	}
	return fmt.Sprintf(" It is now your task %s (%q, its card is in the conversation): %s. done_when %s.", h.todo.ID, h.todo.Title,
		s.followUpWords(ctx, h.self, sup), criteriaWords(nil, h.todo.Supervision))
}

// sendFailed: the send did not go, so a task made for it is taken back (deleted, its link with it: a closed task's
// agent would be closed after a while, tasks_close.go). When it may have gone through, keep its task and tell the
// caller to check the agent before sending the same words again.
func (h *handedTask) sendFailed(ctx context.Context, s *Service, err error) error {
	if sendUnknown(err) {
		if h != nil && h.made {
			s.taskCard(ctx, h.self, h.todo.ID, h.wake)
			return fmt.Errorf("%w (task %s stays: the message may have arrived or may still arrive; check with agent_read before sending again)", err, h.todo.ID)
		}
		return fmt.Errorf("%w (the message may have arrived or may still arrive; check with agent_read before sending again)", err)
	}
	if h == nil || !h.made {
		return err
	}
	if rerr := s.removeTodo(context.WithoutCancel(ctx), h.todo.ID); rerr != nil {
		s.logf("agent_send: take back task %s: %v", h.todo.ID, rerr)
		s.dropAdoption(ctx, h.todo.ID, err)
		return fmt.Errorf("%w (task %s was closed again)", err, h.todo.ID)
	}
	return fmt.Errorf("%w (no task was made)", err)
}

// taskCard puts task id's card into conversation self. wake says how the task came to its Agent when it did not start
// it (#5096, the clients' hand-off badge): wakeActive for an Agent that was running already (agent_supervise,
// agent_send with a task, an Agent that became a task at its turn's end, a task moved to another conversation),
// wakeResumed for a closed one the hand-off's message resumed. "": the task started its Agent, as task_plan and
// agent_start do, and the card has no body.
func (s *Service) taskCard(ctx context.Context, self, id, wake string) {
	var body any
	if wake != "" {
		body = taskCardBody{Wake: wake}
	}
	if _, err := s.appendFeed(ctx, self, feedTask, id, body); err != nil {
		s.logf("task %s: feed: %v", id, err)
	}
}

// A task card's wake (taskCard).
const (
	wakeActive  = "active"
	wakeResumed = "resumed"
)

// taskCardBody is a task card's body: none for a task that started its Agent.
type taskCardBody struct {
	Wake string `json:"wake,omitempty"`
}

// dropContinuation closes an aborted takeover and removes only its own brief link and scope event. Every current or
// snapshot member follows the latest restored brief; a revision recheck prevents a stale brief read from overwriting a
// newer scope. Review fields are restored only when the before/after/current evidence attributes them to this abort.
func (s *Service) dropContinuation(ctx context.Context, id string, rollback *continuationRollback, why error) {
	ctx = context.WithoutCancel(ctx) // the caller may have left exactly when its Agent link failed; cleanup still has to commit
	if rollback == nil {
		s.dropAdoption(ctx, id, why)
		return
	}
	if err := s.unlinkTaskBrief(ctx, id, rollback.eventID); err != nil {
		why = errors.Join(why, fmt.Errorf("take back the continuation: %w", err))
	}
	// A member added after the before snapshot is included too. A member without a ledger still gets none: its later
	// dispatch reads the restored brief instead of having a ledger invented by cleanup.
	targets := map[string]bool{}
	for todoID := range rollback.before {
		targets[todoID] = true
	}
	for _, todoID := range s.briefTodos(rollback.brief) {
		targets[todoID] = true
	}
	for todoID := range targets {
		for {
			brief, revision, ok := s.briefSnapshotByID(rollback.brief)
			if !ok {
				break
			}
			_, _, err := s.updateSupervisionGuarded(ctx, todoID, func(t Todo) bool {
				return t.DoneAt == nil && t.Supervision != nil
			}, func(v *Supervision) bool {
				desired := rollback.restoredLedger(todoID, v, brief, s.supervise.roundEpochs[todoID])
				if reflect.DeepEqual(v, desired) {
					return false
				}
				*v = *desired
				return true
			})
			if err != nil {
				why = errors.Join(why, fmt.Errorf("restore task %s after the continuation: %w", todoID, err))
				break
			}
			_, currentRevision, ok := s.briefSnapshotByID(rollback.brief)
			if !ok || currentRevision == revision {
				break
			}
		}
	}
	s.dropAdoption(ctx, id, why)
}

// dropAdoption closes the todo an adoption made when the agent could not be put on it (why), so no empty task stays.
func (s *Service) dropAdoption(ctx context.Context, id string, why error) {
	s.logf("adopt: task %s: %v", id, why)
	if err := s.failTodo(ctx, id, i18n.T(s.lang(ctx), "assistant-tasks.adopt_failed", i18n.Args{"reason": why.Error()})); err != nil {
		s.logf("adopt: close task %s: %v", id, err)
	}
}

// openTodoOfAgent is the open todo agent mid/sid is active on, whoever opened it (taskOf: only an assistant
// conversation's).
func (s *Service) openTodoOfAgent(mid, sid string) (Todo, bool) {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	t := s.todos.todos[s.todos.activeBySession[[2]string{mid, sid}]]
	if t == nil || t.DoneAt != nil {
		return Todo{}, false
	}
	return copyTodoLocked(t), true
}

// closedTodoOfAgent is the latest assistant task this agent carried before its new card. It supplies the source of a
// take-over; a user's manual to-do is never adopted into assistant history (#5732).
func (s *Service) closedTodoOfAgent(mid, sid string) (Todo, bool) {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	var best Todo
	found := false
	for _, t := range s.todos.todos {
		if t.DoneAt == nil || t.Origin == "" {
			continue
		}
		for _, a := range t.Agents {
			if a.MachineID != mid || a.SessionID != sid {
				continue
			}
			if !found || t.DoneAt.After(*best.DoneAt) || t.DoneAt.Equal(*best.DoneAt) && t.UpdatedAt.After(best.UpdatedAt) {
				best, found = copyTodoLocked(t), true
			}
			break
		}
	}
	return best, found
}

// criteriaWords is done_when as the tool's answer says it: the new list, else the ledger's, else that there is none.
func criteriaWords(list []string, v *Supervision) string {
	if len(list) == 0 && v != nil {
		list = v.DoneWhen
	}
	if len(list) == 0 {
		return "not set (judge by what the task is for; set it with task_scope)"
	}
	return strconv.Quote(strings.Join(list, "; "))
}

// reportBody is a report feed item's body (feedReport).
type reportBody struct {
	Kind     string   `json:"kind"`
	Text     string   `json:"text"`
	Evidence []string `json:"evidence,omitempty"`
	Step     int      `json:"step,omitempty"`
	Steps    int      `json:"steps,omitempty"`
	// Later: a question deferred without waking the supervisor (the main briefing queue or a reporting conversation's feed).
	Later bool `json:"later,omitempty"`
}

// ---- report_to_supervisor ----

// Report kinds an agent sends its supervisor.
const (
	upReportProgress = "progress"
	upReportQuestion = "question"
	upReportBlocked  = "blocked"
	upReportDone     = "done"
)

// reportLimit / reportWindow bound how often one agent reports (each report can start a turn of the main
// conversation: it is not to be flooded).
const (
	reportLimit  = 10
	reportWindow = 10 * time.Minute
)

// reportRate counts each session's recent reports.
type reportRate struct {
	mu   sync.Mutex
	seen map[[2]string][]time.Time
}

// allow records a report of mid/sid at now, false when it has reportLimit within reportWindow already.
func (r *reportRate) allow(mid, sid string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = map[[2]string][]time.Time{}
	}
	k := [2]string{mid, sid}
	kept := r.seen[k][:0]
	for _, at := range r.seen[k] {
		if now.Sub(at) < reportWindow {
			kept = append(kept, at)
		}
	}
	if len(kept) >= reportLimit {
		r.seen[k] = kept
		return false
	}
	r.seen[k] = append(kept, now)
	for other, list := range r.seen { // sessions that reported long ago are forgotten
		if other != k && (len(list) == 0 || now.Sub(list[len(list)-1]) >= reportWindow) {
			delete(r.seen, other)
		}
	}
	return true
}

// joinReports is two reports of the same kind waiting together (cur first, then g): what both said and showed, so
// a second question does not push the first one out.
func joinReports(cur, g superviseSignal) superviseSignal {
	g.At = cur.At
	// A call's permission observation cannot vouch for a report made by another call.
	g.SourcePermission = proto.SourcePermission{}
	if cur.Detail != "" && cur.Detail != g.Detail {
		g.Detail = clipMiddle(cur.Detail+"\n—\n"+g.Detail, 3000)
	}
	ev := slices.Clone(cur.Evidence)
	for _, e := range g.Evidence {
		if !slices.Contains(ev, e) && len(ev) < 12 {
			ev = append(ev, e)
		}
	}
	g.Evidence = ev
	if g.Step == 0 {
		g.Step, g.Steps = cur.Step, cur.Steps
	}
	return g
}

var errNoSupervisor = errors.New("no supervisor")

// agentTool runs one of the agents' own tools for agent session on machine.
func (s *Service) agentTool(ctx context.Context, machine, session, name string, args json.RawMessage) proto.ToolResult {
	switch name {
	case "report_to_supervisor":
		res, err := s.reportToSupervisor(ctx, machine, session, args, time.Now())
		if errors.Is(err, errNoSupervisor) {
			return proto.TextResult(res, false) // not an error: there is nobody to tell, and the agent goes on
		}
		if err != nil {
			return proto.TextResult(err.Error(), true)
		}
		return proto.TextResult(res, false)
	case "todo_decisions", "todo_decision_add": // todo_decisions.go
		res, err := s.agentDecisionTool(ctx, machine, session, name, args, time.Now())
		if err != nil {
			return proto.TextResult(err.Error(), true)
		}
		return proto.TextResult(res, false)
	case "todo_repositories_get", "todo_repositories_set", "todo_repository_links_set", "todo_repository_links_add":
		res, err := s.agentRepositoryTool(ctx, machine, session, name, args)
		if err != nil {
			return proto.TextResult(err.Error(), true)
		}
		return proto.TextResult(res, false)
	case "todo_bind_thread": // todo_thread.go
		res, err := s.agentThreadTool(ctx, machine, session, args)
		if err != nil {
			return proto.TextResult(err.Error(), true)
		}
		return proto.TextResult(res, false)
	}
	return proto.TextResult("unknown tool "+name, true)
}

// reportToSupervisor turns agent machine/session's report into a signal for the supervisor of the task it works
// for: in the next Supervision message (a question, a block or a finish soon: they are urgent). A question with urgent
// false is no signal: it goes into the queue of questions that can wait (later.go) when a briefing will list it.
func (s *Service) reportToSupervisor(ctx context.Context, machine, session string, args json.RawMessage, now time.Time) (string, error) {
	var a struct {
		Kind     string                       `json:"kind"`
		Text     string                       `json:"text"`
		Evidence []string                     `json:"evidence"`
		Step     int                          `json:"step"`
		Steps    int                          `json:"steps"`
		Urgent   *bool                        `json:"urgent"`
		Criteria []criterionReport            `json:"criteria"`
		Handoff  *taskHandoffReceipt          `json:"handoff_receipt"`
		Findings *[]investigationFinding      `json:"findings"`
		Phase    *assistantwire.TaskPhase     `json:"phase"`
		Wait     *assistantwire.TaskWaitState `json:"wait_state"`
		PR       string                       `json:"delivery_pr"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	switch a.Kind {
	case upReportProgress, upReportQuestion, upReportBlocked, upReportDone:
	default:
		return "", fmt.Errorf("kind must be progress, question, blocked or done")
	}
	phase, err := readTaskRuntimeReport(a.Phase, a.Wait)
	if err != nil {
		return "", err
	}
	a.PR = strings.TrimSpace(a.PR)
	text := clip(strings.TrimSpace(a.Text), 2000)
	if text == "" {
		return "", fmt.Errorf("text is empty")
	}
	if session == "" {
		return "Nobody receives this: the call came from outside an agent session.", errNoSupervisor
	}
	if a.Findings != nil && (a.Handoff != nil || a.Kind != upReportDone && a.Kind != upReportBlocked) {
		return "", errors.New("investigation findings are for the task’s done or blocked report")
	}
	if a.Handoff != nil {
		if phase != "" || a.PR != "" {
			return "", errors.New("task runtime fields belong to the task report, outside a handoff receipt")
		}
		res, err := s.reportTaskHandoff(ctx, machine, session, *a.Handoff, a.Kind, text, a.Evidence, now)
		if err == nil {
			if a.Kind == upReportDone {
				s.noteSpawnedDone(machine, session, now)
			} else {
				s.clearSpawnedDone(machine, session)
			}
		}
		return res, err
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", err
	}
	t, ok := s.taskOf(ctx, machine, session)
	if !ok || t.Supervision == nil || t.Supervision.Supervisor == "" {
		// Any open todo's agent has a receiver while agent tasks are on (#3617, agent_tasks.go).
		t, ok = s.superviseOpenTodoOf(ctx, machine, session)
	}
	if !ok {
		return "Nobody receives this: your work is not a task Persona supervises. Ask the user what you need the usual way.", errNoSupervisor
	}
	if s.agentSupervised(t) {
		// A leader whose task closed leaves the worker to the conversation above it: rehome it now, so this report is
		// told to that conversation instead of being lost until the next watch (task_leader.go). With nobody above, the
		// worker is cancelled and says so.
		if lead, _, _ := s.leaderAbove(t); lead.ID == "" || !s.leaderOpen(lead) {
			var cancelled bool
			if t, cancelled = s.rehomeWorker(ctx, t); cancelled {
				return "Your task leader is gone and nobody is above it, so this task was cancelled. Stop here; the user is not asked.", errNoSupervisor
			}
		}
	}
	reports, err := cleanCriterionReports(a.Criteria, t.Supervision.DoneWhen)
	if err != nil {
		return "", err
	}
	if !s.upReports.allow(machine, session, now) {
		return "", fmt.Errorf("too many reports: at most %d in %d minutes; put what is left into one report later", reportLimit, int(reportWindow.Minutes()))
	}
	if a.Findings != nil {
		findings, err := cleanInvestigationFindings(*a.Findings)
		if err != nil {
			return "", err
		}
		version, err := s.recordInvestigationFindings(ctx, t, SessionRef{MachineID: machine, SessionID: session}, findings, now)
		if err != nil {
			return "", err
		}
		text = fmt.Sprintf("Investigation findings version %d; review the list on the to-do before parking or closing.\n%s", version, clip(text, 1850))
	}
	if err := s.recordCriterionReports(ctx, t, machine, session, reports, now.UTC()); err != nil {
		return "", err
	}
	if err := s.recordTaskRuntimeReport(ctx, t, machine, session, a.Kind, phase, a.Wait, a.PR, now); err != nil {
		return "", err
	}
	evidence := make([]string, 0, len(a.Evidence))
	for _, e := range a.Evidence {
		if e = clip(strings.TrimSpace(e), 300); e != "" && len(evidence) < 8 {
			evidence = append(evidence, e)
		}
	}
	steps := max(0, min(a.Steps, 1000))
	step := max(0, min(a.Step, 1000))
	if steps > 0 {
		step = min(step, steps)
	}
	// A task leader's worker has no owner briefing to wait for: its report goes to the leader at once, urgent or not.
	// Immersive attention mode takes a question that set no urgent and is not about the matter in focus as one that
	// can wait (attention.go); an explicit urgent:true always goes at once.
	saidLater := a.Urgent != nil && !*a.Urgent
	byMode := a.Urgent == nil && a.Kind == upReportQuestion && s.attentionDefersQuestion(ctx, t, machine, session)
	notUrgent := (saidLater || byMode) && !s.agentSupervised(t)
	if notUrgent && a.Kind == upReportQuestion {
		answer, queued := s.reportLater(ctx, t, machine, session, text, evidence, byMode, now)
		if queued {
			s.clearSpawnedDone(machine, session)
			return answer, nil
		}
		prefix := answer // it goes as an urgent one: the answer says so first
		res, err := s.reportNow(ctx, t, machine, session, a.Kind, text, evidence, step, steps, now)
		if err == nil {
			s.clearSpawnedDone(machine, session)
		}
		return prefix + res, err
	}
	res, err := s.reportNow(ctx, t, machine, session, a.Kind, text, evidence, step, steps, now)
	if err == nil {
		if a.Kind == upReportDone {
			s.noteSpawnedDone(machine, session, now)
		} else {
			s.clearSpawnedDone(machine, session)
		}
	}
	if notUrgent {
		res = "urgent only applies to a question (kind question); this was sent as usual. " + res
	}
	return res, err
}

// reportLater keeps a question the agent said can wait without waking its supervisor. An active reporting conversation
// gets the deferred report in its own feed, never the main briefing queue. Otherwise the question waits in the queue of
// questions for the next briefing (later.go). queued false: it could not be kept (no morning or evening briefing would
// list it, or the queue is full), and the answer says why; the report then goes as an urgent one. byMode: the agent
// set no urgent and immersive attention mode deferred it, so the answers speak of the mode, not of urgent:false, and
// tell the agent to ask again with urgent:true when it is blocked.
func (s *Service) reportLater(ctx context.Context, t Todo, machine, session, text string, evidence []string, byMode bool, now time.Time) (answer string, queued bool) {
	// notKept is the prefix when the question could not be kept: it goes as an urgent one after all.
	notKept := func(why string) string {
		if byMode {
			return "" // it goes as usual: the agent asked nothing special, so there is nothing to explain
		}
		return "urgent:false was ignored: " + why + ", so it is sent as an urgent question. "
	}
	retry := "If you cannot go on without the answer, ask it again without urgent:false."
	if byMode {
		retry = "The user is in a focus block on other work. If you cannot go on without the answer, ask it again with urgent:true."
	}
	if t.Supervision != nil && t.Supervision.Supervisor == t.Origin {
		if _, reporting := s.reporterOf(ctx, t.Origin); reporting {
			if s.reporterStopped(ctx, t.Origin) {
				return "Nobody receives this: your reporting conversation was stopped. Ask the user what you need the usual way.", true
			}
			body := reportBody{Kind: upReportQuestion, Text: text, Evidence: evidence, Later: true}
			if _, err := s.appendFeed(ctx, t.Origin, feedReport, t.ID, body); err != nil {
				s.logf("task %s: reporting question feed: %v", t.ID, err)
				return notKept("the reporting conversation could not keep the question"), false
			}
			s.logf("task %s: %s reports a question deferred in its reporting conversation", t.ID, short(session))
			return "Queued without waking your reporting conversation; it can answer there when it next runs. Go on with other work. " +
				retry, true
		}
	}
	if !s.laterBriefed(ctx) {
		return notKept("no morning or evening briefing is switched on to list it"), false
	}
	_, ok, again := s.laterQueue(ctx, t.ID, machine, session, text, now)
	if !ok {
		return notKept("the queue of questions that can wait could not take it"), false
	}
	if again {
		return "Already queued: the owner sees this question in the next morning or evening briefing. Go on with other work.", true
	}
	body := reportBody{Kind: upReportQuestion, Text: text, Evidence: evidence, Later: true}
	if _, err := s.appendFeed(ctx, t.Supervision.Supervisor, feedReport, t.ID, body); err != nil {
		s.logf("task %s: report feed: %v", t.ID, err)
	}
	s.logf("task %s: %s reports a question that can wait", t.ID, short(session))
	return "Queued: the owner sees it in the next morning or evening briefing, under \"Can wait\", and answers it in your chat when they get to it. " +
		"Nobody is woken and nobody answers right away: go on with other work. " + retry, true
}

// reportNow sends a report to the supervisor of task t as a signal, and shows it in its conversation.
func (s *Service) reportNow(ctx context.Context, t Todo, machine, session, kind, text string, evidence []string, step, steps int, now time.Time) (string, error) {
	current, _ := s.todoNow(t.ID)
	done := kind == upReportDone && (current.Phase != assistantwire.TaskPhaseWaiting || current.WaitState == nil)
	if done {
		s.recordBriefCompletion(ctx, t, briefCompletionReport, now)
	}
	s.progressReported(machine, session, len(evidence), step, now) // evidence or a later step is progress (supervise_progress.go)
	g := superviseSignal{Todo: t.ID, Kind: sigReport, Key: now.UTC().Format(time.RFC3339Nano), At: now, Machine: machine,
		Session: session, Report: kind, Detail: text, Evidence: evidence, Step: step, Steps: steps, SourcePermission: devicewire.SourcePermissionFrom(ctx)}
	if kind != upReportProgress && s.reportsMidTurn(ctx, machine, session) {
		g.NotBefore = now.Add(reportStopHold) // it waits for its Agent's stop, to go out with it (releaseReportsLocked)
	}
	if !s.deliverReportingReport(ctx, t, g) {
		s.signal(g)
	}
	progress := TaskProgress{Kind: kind, Step: step, Steps: steps, Text: clip(oneLine(text), 200), At: now.UTC(),
		machine: machine, session: session}
	if text != progress.Text {
		progress.Detail = text
	}
	if s.noteProgress(ctx, t.ID, progress) {
		s.goReportLine(t, progress.At, kind, text) // off the tool call: it answers right away
	}
	if !current.hasTaskRuntime() {
		s.advanceTaskReply(ctx, t, machine, session, strings.Join(append([]string{text}, evidence...), "\n"))
	}
	// What it asks, what blocks it and its finish show in the supervisor's conversation too, so the user can follow
	// the handoff; progress reports only update the card (the stream stays quiet).
	if kind != upReportProgress {
		body := reportBody{Kind: kind, Text: text, Evidence: evidence, Step: step, Steps: steps}
		if _, err := s.appendFeed(ctx, t.Supervision.Supervisor, feedReport, t.ID, body); err != nil {
			s.logf("task %s: report feed: %v", t.ID, err)
		}
	}
	s.logf("task %s: %s reports %s", t.ID, short(session), kind)
	if done {
		s.goRecordWorkerReport(ctx, t, machine, session, text, evidence, now)
	}
	if kind == upReportProgress {
		return "Noted for your supervisor: it reads it with its next supervision message. Go on with the work.", nil
	}
	return "Sent to your supervisor: it reads it within minutes and answers you by message. Don't wait idle for it if there is " +
		"other work in the task; stop when you cannot go on.", nil
}

// reportsMidTurn: the Agent that reports is in the middle of a turn, so the turn's end signal follows its report
// (reportStopHold). One that is not working, or not known, has no stop coming: its report goes out as it always did.
func (s *Service) reportsMidTurn(ctx context.Context, machine, session string) bool {
	if machine == "" || session == "" || s.noReportHold {
		return false
	}
	c, ok := s.session(ctx, machine, session)
	return ok && c.State == proto.StateWorking
}

// deliverReportingReport sends g to an active reporting conversation through the same notice path as its quiet tasks:
// its own machine, no supervision-policy requirement, and notices removed when it is stopped. t names the reporter only
// in the returned copy from superviseOpenTodoOf; no ledger is written. false selects the ordinary supervision route
// when this is not a reporting report or its relay cannot be saved.
func (s *Service) deliverReportingReport(ctx context.Context, t Todo, g superviseSignal) bool {
	machine, reporting := s.reporterOf(ctx, t.Origin)
	if !reporting || t.Supervision == nil || t.Supervision.Supervisor != t.Origin {
		return false
	}
	if s.reporterStopped(ctx, t.Origin) {
		return true
	}
	if g.level() == supervisionInfo {
		return true // as signal does, progress stays on the card and wakes nobody
	}
	entry, ok := s.digestEntry(ctx, g, s.webBase(ctx))
	if !ok {
		return true
	}
	msg := relayMsg{From: t.ID, FromTitle: i18n.T(s.lang(ctx), "assistant-conversations.supervision_source"), Tool: noticeTool, At: g.At.UTC(),
		Text: s.renderQueuedDigest(ctx, t.Origin, []prompts.SuperviseEntry{entry})}
	queue := func() bool {
		if _, err := s.queueRelayDurable(t.Origin, msg); err != nil {
			s.logf("task %s: reporting report: %v", t.ID, err)
			return false
		}
		s.publish(ctx)
		return true
	}
	c, ok := s.session(ctx, machine, t.Origin)
	if !ok || c.Starting || c.Status == proto.StatusWorking || s.relayWaits(t.Origin, msg) {
		if !queue() {
			return false
		}
		if ok && !c.Starting && c.Status != proto.StatusWorking {
			s.drainRelays(machine, []proto.Session{c})
		}
		return true
	}
	if err := s.tryDirectRelay(ctx, machine, t.Origin, &msg); err != nil {
		if s.refusedDirect(ctx, machine, t.Origin, msg, err) {
			return true
		}
		s.logf("task %s: reporting report: %v; queued", t.ID, err)
		return queue()
	}
	return true
}

// stoppedAgent: c is an agent that has stopped and is read-only (node stopped.go, #4754): its terminal engine keeps its
// conversation and terminal, and a message to it is refused (agent_stopped).
func stoppedAgent(c proto.Session) bool {
	if c.SourceKind == "codex_history" {
		return c.Capabilities != nil && !c.Capabilities.Send && c.Capabilities.Read && c.Capabilities.Terminal && !c.IsSubagent
	}
	if c.Status != proto.StatusOffline && c.Status != proto.StatusInterrupted {
		return false
	}
	if c.SourceKind == "" {
		return c.Capabilities == nil || !c.Capabilities.Send
	}
	return false
}
