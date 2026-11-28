package assistant

import (
	"cmp"
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/agentprompt"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A task whose agents have all stopped (read-only: node stopped.go, #4754) goes on with a new agent when the user asks
// it to: a message on its card (route) or its Retry. The new agent starts on the computer, in the directory and with
// the launcher of the task's latest agent; its first message is the user's, after who worked on the task before and
// that agent's last reply (prompts agents/task_successor); it is linked to the task as the first was, and the card's
// feed line says a new agent took it. Persona's own follow-ups (relayed answers, rework, digests) are not delivered to
// a stopped agent and start nothing: errTaskAgentsStopped.

// errTaskAgentsStopped: every agent of the task has stopped; nothing was sent.
var errTaskAgentsStopped = errors.New("every agent of this task has stopped and is read-only: nothing was sent. " +
	"A message on the task's card starts a new agent for it; to go on with a stopped agent's own context, resume it on " +
	"purpose with agent_send (resume true, resume_why), or the user can with Resume on its terminal page")

// liveTaskAgents are the task's agents that have not stopped; last is the latest of all when none is left.
func liveTaskAgents(agents []proto.Session) (live []proto.Session, last proto.Session) {
	for _, c := range agents {
		if !stoppedAgent(c) {
			live = append(live, c)
		} else if last.SessionID == "" || c.UpdatedAt.After(last.UpdatedAt) {
			last = c
		}
	}
	return live, last
}

// startTaskSuccessor starts a new agent for task t after its stopped agent last, with the user's text as its first
// message, and links it to t.
func (s *Service) startTaskSuccessor(ctx context.Context, t Todo, last proto.Session, text string) (proto.Session, error) {
	m, ok := s.device(ctx, last.MachineID)
	if !ok || !m.Reachable {
		return proto.Session{}, machineOffline(cmp.Or(m.Name, last.MachineID))
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentStartTimeout)
	defer cancel()
	sctx = context.WithValue(sctx, placementTaskKey{}, t.ID)
	agent := last.Agent
	if agent == "" {
		agent = s.launcherAgent(sctx, last.MachineID, last.Launcher)
	}
	successor := prompts.TaskSuccessor{Previous: agentLabel(last), PreviousID: last.SessionID,
		LastReply: clip(last.LastText, 2000), Files: prompts.RenderTaskFiles(answerLanguage(s.hello(ctx).Locale))}
	prompt := ""
	if agent == agentprompt.Antigravity {
		if ordered, ok := agentprompt.WorkflowCommand(text); ok {
			successor.Text = ""
			context := strings.TrimSpace(prompts.RenderTaskSuccessor(successor))
			if decisions := s.todoHandoffDecisionsContext(sctx, t.ID); decisions != "" {
				context = strings.TrimSpace(context + "\n\n" + decisions)
			}
			prompt = agentprompt.WithContext(agent, ordered, context)
		}
	}
	if prompt == "" {
		successor.Text = text
		prompt = prompts.RenderTaskSuccessor(successor)
		prompt = s.withTodoDecisions(sctx, t.ID, prompt)
	}
	params := map[string]any{"cwd": last.Cwd, "launcher": last.Launcher, "model": "", "effort": "", "tab_name": t.Title,
		"prompt": prompt}
	if last.PermissionMode != "" {
		params["permission_mode"] = last.PermissionMode
	}
	if err := s.applyModel(sctx, "agent.new", params); err != nil {
		return proto.Session{}, err
	}
	dispatchAllowAll(sctx, s, m, last.Launcher, params)
	var res struct {
		Session proto.Session `json:"session"`
	}
	if err := s.call(sctx, m.ID, DeviceSessionStart, params, &res); err != nil {
		if sendUnknown(err) {
			return proto.Session{}, &uncertainTaskStart{Message: i18n.Message{
				Key: "assistant-tasks.route_start_unconfirmed", Args: i18n.Args{"reason": err},
			}, cause: err}
		}
		return proto.Session{}, err
	}
	c := res.Session
	c.MachineID = cmp.Or(c.MachineID, m.ID)
	link := s.linkTodoAgent
	if t.DoneAt != nil {
		link = s.linkTodoAgentOfDoneTask
	}
	if err := link(sctx, t.ID, m.ID, c.SessionID, t.Title, cmp.Or(c.Cwd, last.Cwd), cmp.Or(c.Agent, last.Agent)); err != nil {
		s.logf("task %s: new agent %s/%s not linked: %v", t.ID, m.ID, short(c.SessionID), err)
	}
	// The task's ledger goes on with the new agent: an open task without a supervisor gets one, as any Agent Persona
	// starts does (#4669); a done one is left as it is.
	s.ensureSupervisor(sctx, t.ID)
	s.logf("task %s: its agents had stopped; started %s/%s for the user's message", t.ID, m.ID, short(c.SessionID))
	return c, nil
}

// Keep the node's original result available to callers while giving the owner a localized explanation.
type uncertainTaskStart struct {
	i18n.Message
	cause error
}

func (e *uncertainTaskStart) Unwrap() error { return e.cause }

// taskStart is a start of a task's agent on its way (tasksState.retrying).
type taskStart struct {
	done chan struct{} // closed when it ended
	// agent is the new agent a start after stopped ones brought up (startTaskSuccessor), set before done closes; empty
	// for other starts and for one that failed.
	agent proto.Session
	// Written by the starter before done closes. A waiter must not start a second agent when the first may exist.
	uncertain bool
}

// claimTaskStart marks task id's agent as starting. ok is false when a start is on its way already: cur is that one.
func (s *Service) claimTaskStart(id string) (cur *taskStart, ok bool) {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	return s.claimTaskStartLocked(id)
}

// claimTaskStartLocked is claimTaskStart under tasks.mu.
func (s *Service) claimTaskStartLocked(id string) (cur *taskStart, ok bool) {
	if cur := s.tasks.retrying[id]; cur != nil {
		return cur, false
	}
	st := &taskStart{done: make(chan struct{})}
	s.tasks.retrying[id] = st
	return st, true
}

// endTaskStart ends task id's start st; agent is the new agent it started, if any. Ending it again does nothing.
func (s *Service) endTaskStart(id string, st *taskStart, agent proto.Session) {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	select {
	case <-st.done:
		return
	default:
	}
	if s.tasks.retrying[id] == st {
		delete(s.tasks.retrying, id)
	}
	st.agent = agent
	close(st.done)
}

// taskStartOnWay reports whether a start of task id's agent is on its way.
func (s *Service) taskStartOnWay(id string) bool {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	return s.tasks.retrying[id] != nil
}

// taskStartWait bounds how long a message for a task waits for the start of its agent already on its way: that start's
// own bound and a margin (tests shorten it).
var taskStartWait = func() time.Duration { return agentStartTimeout + time.Minute }
