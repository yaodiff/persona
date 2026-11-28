package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The assistant's resume of a stopped agent (#8099). A stopped agent is read-only (node stopped.go, #4754): no message
// wakes it, and Persona's own follow-ups (rework, relayed answers, digests) are still never delivered to it. What the
// assistant may do is resume it on purpose, as the user's Resume on its terminal page does, when the next work needs
// that agent's context (it has just found the cause and should now fix it, rework, the user asked for "the one with
// the context"): agent_send with resume true and resume_why. The resume is
//
//   - the same node call as the user's Resume: session.start with resume_session_id and a node-side recovery guard
//     (node node.go fillResume: the session's own launcher, directory, model and permission mode; the shell it left
//     when that is still there, else a new tab), the text as its first message;
//   - never unsupervised: the agent is on an open task of this conversation already, or the call gives task (the main
//     conversation, or a direction's lead within leadTakeAllowed, #12953), which makes it one as agent_send with a
//     task does, the card marked resumed (wakeResumed);
//   - visible: the phone's confirmation and the task's card say which agent was resumed and why (resume_note);
//   - compacted before the text goes in when its context is nearly full (#11137): resumed without a first message,
//     then the text sent with compact_if_full, so the node runs the runtime's own /compact and sends once it is done
//     (node compact_before_send.go). Only a failed compaction, or a runtime Persona cannot compact, falls back to a
//     replacement, saying why;
//   - refused for the user's stops, archived Agents with no recorded non-user stop, offline computers, or the node's own
//     start refusals. An unknown stop is resumable (#11528).

// resumeRequest is what agent_send's resume asks for: why its context is needed.
type resumeRequest struct {
	why string
}

// stoppedRefusal is agent_send's answer for a stopped agent without resume: nothing was sent, and how to choose.
func stoppedRefusal(c proto.Session) error {
	return fmt.Errorf("%s has stopped: its conversation and terminal stay, and a plain message does not start it again. "+
		"Nothing was sent. Choose how to go on: when the next work depends on what this agent already knows (it has just "+
		"investigated this and should now fix it, rework of its own work, the user asked for the agent with the context), "+
		"send again with resume true and resume_why, which resumes this same session with its context on its own "+
		"launcher and permission mode (also give task, unless it is on an open task of yours). "+
		"A nearly full context is no reason to start over: the resume compacts it with the agent's own /compact before your "+
		"text goes in. For unrelated work, or when it went off track, start a replacement and give it what "+
		"it needs (read this one with agent_read): for its task, task_plan with continues (the task's todo id) and "+
		"continues_why, which keeps the task's todo and brief; for work on no task, agent_start in its directory. "+
		"When you cannot tell, ask the owner (ask_user, or task_review needs_user): reuse this agent, or start a new one", agentLabel(c))
}

// resumeAllowed checks a resume before anything is asked or started: a reason, the computer reachable, room left in
// the agent's context, and a task of self's it will be on (one it is on, or task, which the send's checks go on with).
func (s *Service) resumeAllowed(ctx context.Context, self string, am Device, c proto.Session, why string, task *sendTask, review resumeReview) error {
	cause := proto.StopUnknown
	if c.Capabilities != nil {
		cause = c.Capabilities.StopCause
	}
	if c.Archived {
		// Archive snapshots may predate the stop or a later user close. Only the node's saved cause authorizes resume.
		var info struct {
			StopCause proto.StopCause `json:"stop_cause"`
		}
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := s.call(cctx, am.ID, "session.info", map[string]any{"session_id": c.SessionID}, &info)
		cancel()
		if err != nil {
			return resumeFailed(c, am, fmt.Errorf("could not verify the node's stop cause: %w", err))
		}
		cause = info.StopCause
	}
	// Every stop but the user's may be resumed (#11528): an unknown cause too, an Agent that quit by itself or that a
	// restart of its machine cut off. An archived one only with a cause the node recorded as not the user's: archiving
	// it was the user's (or the assistant's own close, which records its cause).
	oldNode := c.Capabilities == nil && !c.Archived
	ownerOnlyStop := c.Archived && !cause.AllowsSupervisorResume() || !cause.SupervisorMayResume()
	// An archive lookup failure above leaves the last verified cause intact. Background forwarding never
	// contributes evidence to a supervisor's decision.
	if review.reviewer != "" {
		s.todos.noteResumeCheck(self, review, am.ID, c, oldNode || ownerOnlyStop)
	}
	if oldNode {
		return fmt.Errorf("%s cannot be resumed by the assistant: its computer's node reports no stop cause (too old). "+
			"The user's Resume on its terminal page can. Nothing was started or sent", agentLabel(c))
	}
	if ownerOnlyStop {
		return fmt.Errorf("%s cannot be resumed by the assistant: stop cause is %s. %s require "+
			"the user's Resume on its terminal page. Give that instruction separately from the choices on a decision card. Nothing was started or sent",
			agentLabel(c), cause, userResumeOnly(c.Archived, cause))
	}
	if why == "" {
		return fmt.Errorf("resume_why is required: say why this agent's own context is needed (the user sees it). Nothing was started or sent")
	}
	if d, ok := s.device(ctx, am.ID); !ok || !d.Reachable {
		return resumeFailed(c, am, machineOffline(am.Name))
	}
	if contextFull(c) && !compactable(c) {
		return resumeFailed(c, am, fmt.Errorf("its context is nearly full (%d of %d tokens) and %s, so it cannot be compacted "+
			"before your text goes in", c.ContextTokens, c.ContextWindow, notCompactable(c)))
	}
	if task != nil {
		return nil // sendTaskAllowed checks it next, as for any hand-off
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	t, ok := s.openTodoOfAgent(am.ID, c.SessionID)
	if !ok {
		if !s.supervises(ctx, self) {
			return fmt.Errorf("%s is on no open task of yours: a resumed agent must be supervised, and only the main "+
				"conversation and a direction's lead supervise agents. Nothing was started or sent", agentLabel(c))
		}
		return fmt.Errorf("%s is on no open task of yours: a resumed agent must be supervised. Give task to make it your "+
			"task as you resume it. Nothing was started or sent", agentLabel(c))
	}
	if err := othersTask(c, t, self); err != nil {
		return fmt.Errorf("%w; it is not yours to resume. Nothing was started or sent", err)
	}
	return nil
}

// resumeTodoForAgent prepares a same-session resume of stopped agent c back onto open to-do id: the to-do must be one
// self opened or supervises, still open, with no active agent, and c its most recently released (not dismissed) agent.
// The historical link is reactivated (released_at cleared) before resumeAllowed runs, so the resume keeps the original
// to-do and its brief instead of adopting the agent onto a new supervised task (#13192). Nothing starts here: the
// caller still goes through resumeAllowed and the node's session.start.
func (s *Service) resumeTodoForAgent(ctx context.Context, self string, am Device, c proto.Session, id string) (Todo, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Todo{}, errors.New("todo_id is empty")
	}
	if c.State == proto.StateNeedsYou || c.PendingInteractions > 0 {
		return Todo{}, fmt.Errorf("%s is waiting for the user (a question, plan, permission or dialog): it is theirs to answer first", agentLabel(c))
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return Todo{}, err
	}
	t, ok := s.todoNow(id)
	if !ok {
		return Todo{}, fmt.Errorf("todo_id %s: there is no such to-do", id)
	}
	if t.Origin != self && (t.Supervision == nil || t.Supervision.Supervisor != self) {
		return Todo{}, fmt.Errorf("todo_id %s: it was not opened from this conversation, nor is it supervised by it", id)
	}
	if t.DoneAt != nil {
		return Todo{}, fmt.Errorf("todo_id %s: it is already done; reopen it before resuming its Agent", id)
	}
	for _, a := range t.Agents {
		if a.active() {
			return Todo{}, fmt.Errorf("todo_id %s: an agent is already working on it; send that Agent instead of resuming another", id)
		}
	}
	// Reject before the re-link when the session still serves another open to-do: writeTodoAgentLink would release that
	// other active link, and rollbackResumeTodo only releases this to-do's link, so a later refusal/failed start would
	// leave the other to-do with no Agent even though nothing was sent (#13225 QA).
	if other, ok := s.openTodoOfAgent(am.ID, c.SessionID); ok && other.ID != t.ID {
		return Todo{}, fmt.Errorf("todo_id %s: this Agent is still on to-do %s; resume it there, or hand that to-do over first", id, other.ID)
	}
	last := lastReleasedAgent(t)
	if last == nil || last.MachineID != am.ID || last.SessionID != c.SessionID {
		return Todo{}, fmt.Errorf("todo_id %s: this Agent is not the to-do's latest released Agent; resume onto a to-do only with the Agent that last worked on it", id)
	}
	agentName := c.Agent
	if agentName == "" {
		agentName = last.Agent
	}
	// The upsert clears released_at on the same (todo, machine, session) link and keeps its title/cwd history
	// (db.linkTodoAgentWithSource); any active link elsewhere would be released first, which the checks above rule out.
	if err := s.linkTodoAgentWithSource(ctx, t.ID, am.ID, c.SessionID, last.Title, last.Cwd, agentName,
		"", "manual", "resume"); err != nil {
		return Todo{}, fmt.Errorf("resume it on its to-do: %w", err)
	}
	relinked, _ := s.todoNow(t.ID)
	s.logf("resume todo %s: re-linked agent %s/%s", t.ID, am.ID, short(c.SessionID))
	return formatTodo(relinked), nil
}

// lastReleasedAgent is the to-do's most recently linked agent that was let go (released, not dismissed); nil when the
// to-do never had one. A dismissed link is an explicit removal, not a stop the assistant may undo.
func lastReleasedAgent(t Todo) *TodoAgent {
	var last *TodoAgent
	for i := range t.Agents {
		a := &t.Agents[i]
		if a.ReleasedAt == nil || a.DismissedAt != nil {
			continue
		}
		if last == nil || a.LinkedAt.After(last.LinkedAt) {
			last = a
		}
	}
	return last
}

// rollbackResumeTodo undoes a resumeTodoForAgent re-link when the send does not happen (a refusal after the link, a
// declined confirmation, or a node start that failed for a known reason): the link is released again so the to-do is
// unassigned as before. A start whose result is unknown keeps the link, as the session may still come up. Best
// effort: a failure here only logs, the caller's original error stands.
func (s *Service) rollbackResumeTodo(ctx context.Context, todoID, mid, sid string) {
	now := time.Now().UTC()
	s.todos.writing.Lock()
	if st, ok := s.todoDB().(todoAgentReleaser); ok {
		if err := st.ReleaseTodoAgent(ctx, todoID, mid, sid, now); err != nil {
			s.logf("resume todo %s: rollback release: %v", todoID, err)
		}
	}
	s.todos.mu.Lock()
	if t, ok := s.todos.todos[todoID]; ok {
		for i := range t.Agents {
			if t.Agents[i].MachineID == mid && t.Agents[i].SessionID == sid && t.Agents[i].ReleasedAt == nil {
				t.Agents[i].ReleasedAt = &now
				t.UpdatedAt = now
			}
		}
	}
	if key := [2]string{mid, sid}; s.todos.activeBySession[key] == todoID {
		delete(s.todos.activeBySession, key)
	}
	s.todos.mu.Unlock()
	s.todos.writing.Unlock()
	s.linksChanged(ctx)
	s.todosChanged(ctx)
	s.logf("resume todo %s: rolled back the re-link of %s/%s", todoID, mid, short(sid))
}

// eventMayResume: an event Persona routes on its own (a PR's, an Inbox item's) may resume c only after a stop the node
// recorded as not the user's. An unknown stop or a supervision timeout stop is left to the supervisor's deliberate
// agent_send resume; an event does not decide that on its own (#11528). An archived one is left to resumeAllowed,
// which reads the node's own cause.
func eventMayResume(c proto.Session) error {
	if c.Capabilities != nil && c.Capabilities.StopCause == proto.StopTimeout {
		return fmt.Errorf("%s stopped on supervision timeout: an event does not resume it; its supervisor may", agentLabel(c))
	}
	if c.Archived || c.Capabilities != nil && c.Capabilities.StopCause.AllowsSupervisorResume() {
		return nil
	}
	return fmt.Errorf("%s has no recorded non-user stop: an event does not resume it; its supervisor may", agentLabel(c))
}

// userResumeOnly names the stops that need the user's Resume, for resumeAllowed's refusal of one with cause.
func userResumeOnly(archived bool, cause proto.StopCause) string {
	switch {
	case cause.UserStopped():
		return "Stops by the user (Stop, Close, Archive and close, /exit)"
	case archived:
		return "Archived Agents without a recorded non-user stop"
	}
	return "Stop causes this hub does not know"
}

// contextFull: what the model saw in c's latest reply fills proto.ContextNearlyFullShare of its window (both known).
func contextFull(c proto.Session) bool {
	return proto.ContextNearlyFull(int(c.ContextTokens), int(c.ContextWindow))
}

// compactable: c runs on a runtime whose own compaction the node can run before a message (#11137): Claude Code in one
// of Persona's terminals, and Codex wherever Persona reaches it to resume or send: its terminal, a thread nothing runs
// that the node resumes there with codex resume (codex_history), or its app-server (codex_app_server). Not the Codex
// App's chats, a Claude outside Persona's terminals (claude_bg, claude_desktop, claude_cli: its inbox runs no command)
// or a runtime's session (Paseo and the like).
func compactable(c proto.Session) bool {
	switch c.Agent {
	case "", "claude":
		return c.SourceKind == ""
	case "codex":
		if c.SpaceKind == codexAppSpace {
			// An idle, unowned Codex App thread explicitly advertised as resumable into a Persona terminal can be
			// compacted there like any terminal Codex; live or non-takeover Codex App chats stay non-compactable.
			return stoppedAgent(c) && c.Capabilities != nil && c.Capabilities.Takeover != nil && c.Capabilities.Takeover.CanResume
		}
		return c.SourceKind == "" || c.SourceKind == "codex_history" || c.SourceKind == "codex_app_server"
	}
	return false
}

// codexAppSpace is the space_kind of the Codex App's chats (node codex_desktop.go).
const codexAppSpace = "codex_app"

// notCompactable says why c cannot be compacted from Persona, and what then.
func notCompactable(c proto.Session) string {
	switch {
	case c.Agent == "codex" && c.SpaceKind == codexAppSpace:
		return "it is a Codex App chat, which takes no /compact from Persona (the App compacts on its own)"
	case c.SourceKind == proto.SourceClaudeBg || c.SourceKind == proto.SourceClaudeDesktop || c.SourceKind == proto.SourceClaudeTerminal:
		return "it is a Claude outside Persona's terminals, whose inbox runs no /compact"
	case c.SourceKind != "" && (c.Agent == "" || c.Agent == "claude" || c.Agent == "codex"):
		return "it is a " + c.SourceKind + " session, which Persona cannot compact"
	case c.SourceKind != "":
		return "it runs in " + c.SourceKind + ", which has no compaction Persona can run"
	}
	return c.Agent + " has no compaction Persona can run"
}

// compactFailed is a send whose compaction of a nearly full context failed (the node's compact_failed): nothing was
// sent; where the agent stands and what to do instead. resumed: the agent was resumed for it. The node says when the
// agent quit meanwhile (agent_quit): then it is stopped again, recorded as compact_failed, which the assistant may
// resume (#11441); the receipt never says it rests when it does not run.
func compactFailed(c proto.Session, am Device, err error, resumed bool) error {
	quit := agentQuit(err)
	var state string
	switch {
	case resumed && quit:
		state = "It was resumed, but its process quit again (see what its terminal shows above); it is stopped as " +
			"compact_failed, so you may resume it again once that is dealt with"
	case resumed:
		state = "It was resumed and runs with its context as it was"
	case quit:
		state = "Its process quit (see what its terminal shows above); it is stopped as compact_failed, so you may " +
			"resume it once that is dealt with"
	default:
		state = "It keeps running with its context as it was"
	}
	return fmt.Errorf("could not compact %s on %s before your text: %w. %s; nothing was sent. Send again to try once "+
		"more, or, if it cannot be compacted, start a replacement (read this one with agent_read and give it what it "+
		"needs): for its task, task_plan with continues (the task's todo id) and continues_why; for work on no task, "+
		"agent_start in its directory", agentLabel(c), am.Name, err, state)
}

// agentQuit: a node's compact_failed says the agent quit while it was compacted (refusal data agent_quit).
func agentQuit(err error) bool {
	var re *rpc.Error
	if !errors.As(err, &re) || len(re.Data) == 0 {
		return false
	}
	var d struct {
		AgentQuit bool `json:"agent_quit"`
	}
	return json.Unmarshal(re.Data, &d) == nil && d.AgentQuit
}

// compactSendWait bounds a send that may compact first: a compaction takes minutes (node compactBeforeSendDeadline).
const compactSendWait = 15 * time.Minute

// resumeFailed is a resume that did not happen, with why and what to do instead.
func resumeFailed(c proto.Session, am Device, err error) error {
	return fmt.Errorf("could not resume %s on %s: %w. Nothing was sent; start a replacement instead (read this one with "+
		"agent_read and give it what it needs): for its task, task_plan with continues (the task's todo id) and continues_why; "+
		"for work on no task, agent_start in its directory", agentLabel(c), am.Name, err)
}

// resumeParams are the node's session.start params for resuming c with text as its first message: the user's Resume
// (the node fills in what the session ran with) plus the prompt and an eligibility recheck on that node.
func resumeParams(c proto.Session, text string) map[string]any {
	return map[string]any{"resume_session_id": c.SessionID, "prompt": text, "resume_automatic": true}
}

// resumeAndSend resumes stopped agent c of am with params' text as its first message (resumeParams) and says so in
// conversation self: on task's card when the send made or found one, else on the open task of self's it is on.
// handed is what the receipt says of the attachments. The text is noted before it goes, as any agent_send's
// (noteAgentSend, #7539).
func (s *Service) resumeAndSend(ctx context.Context, self string, am Device, c proto.Session, params map[string]any, why, told, handed string,
	task *handedTask) (string, error) {
	text, _ := params["text"].(string)
	wait := 5 * time.Minute // a resume can take minutes
	if contextFull(c) {
		wait += compactSendWait // and then a compaction (#11137)
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wait)
	defer cancel()
	sendNote, err := s.noteAgentSend(sctx, self, am.ID, c.SessionID, "", text) // more from its task's conversation reopens an accepted task (#6726, #7539)
	if err != nil {
		return "", task.sendFailed(sctx, s, err)
	}
	var res struct {
		Session proto.Session `json:"session"`
	}
	full := contextFull(c) // resumeAllowed let only a compactable one through
	startParams := resumeParams(c, text)
	if full {
		// Resumed without a first message, then the text sent with compact_if_full: the node compacts it with its own
		// /compact first and sends once that is done (#11137).
		delete(startParams, "prompt")
	} else if id, ok := params["client_msg_id"]; ok {
		startParams["client_msg_id"] = id
	}
	if task != nil {
		sctx = context.WithValue(sctx, placementTaskKey{}, task.todo.ID)
	}
	err = s.call(sctx, am.ID, DeviceSessionStart, startParams, &res)
	if err != nil {
		if full && sendUnknown(err) {
			// This start carried no prompt. Keep supervision of the possibly resumed agent, but no turn is
			// attributed to text that the second call never sent.
			sendNote.sent(sctx, rpc.NotSent(err))
			if task != nil && task.made {
				s.taskCard(sctx, task.self, task.todo.ID, task.wake)
			}
			return "", fmt.Errorf("%w (the agent may have resumed without a first message; check with agent_read before resuming again)", err)
		}
		sendNote.sent(sctx, err)
		if sendUnknown(err) {
			return "", task.sendFailed(sctx, s, err)
		}
		return "", task.sendFailed(sctx, s, resumeFailed(c, am, err))
	}
	compacted := ""
	if full {
		send := maps.Clone(params)
		send["compact_if_full"] = true
		var sent struct {
			Compacted bool `json:"compacted"`
		}
		err = s.call(sctx, am.ID, DeviceSessionSend, send, &sent)
		sendNote.sent(sctx, err)
		if err != nil {
			if sendUnknown(err) {
				return "", task.sendFailed(sctx, s, err)
			}
			if errCode(err) == proto.CodeCompactFailed {
				return "", task.sendFailed(sctx, s, compactFailed(c, am, err, true))
			}
			return "", task.sendFailed(sctx, s, fmt.Errorf("resumed %s on %s, but the text did not go in: %w", agentLabel(c), am.Name, err))
		}
		s.logf("resume %s on %s: context %d of %d, compacted before the text: %v", short(c.SessionID), am.ID, c.ContextTokens, c.ContextWindow, sent.Compacted)
		if sent.Compacted {
			compacted = fmt.Sprintf(" Its context was nearly full (%d of %d tokens), so it compacted it with its own /compact first.", c.ContextTokens, c.ContextWindow)
		}
	} else {
		sendNote.sent(sctx, err)
	}
	s.progressMade(am.ID, c.SessionID, time.Now())
	s.logf("resume %s on %s for %s: %s", short(c.SessionID), am.ID, self, why)
	note := s.resumeNote(sctx, c, am, why)
	said := ""
	if task != nil {
		said = task.sent(sctx, s, told)
		s.noteResume(sctx, self, task.todo.ID, am.ID, c.SessionID, note)
		s.ensureSupervisor(sctx, task.todo.ID) // made or kept by adoptAgent; never left without a supervisor
		if err := s.clearTimedOut(sctx, task.todo.ID); err != nil {
			s.logf("task %s: clear timed_out on resume: %v", task.todo.ID, err)
		}
	} else if t, ok := s.openTodoOfAgent(am.ID, c.SessionID); ok {
		s.ensureSupervisor(sctx, t.ID) // its task goes on with a supervisor, as a successor's does (tasks_successor.go)
		s.releaseHold(sctx, t.ID)      // its supervisor sent it on: follow-ups go again (decision.go)
		if t.Supervision != nil && t.Supervision.state(t) == reviewTimedOut {
			if err := s.clearTimedOut(sctx, t.ID); err != nil {
				s.logf("task %s: clear timed_out on resume: %v", t.ID, err)
			}
		}
		if _, err := s.appendFeed(sctx, self, feedRouted, t.ID, map[string]string{"text": told, "from": routedFromAssistant,
			"machine_id": am.ID, "session_id": c.SessionID}); err != nil {
			s.logf("task %s: routed feed: %v", t.ID, err)
		}
		s.noteResume(sctx, self, t.ID, am.ID, c.SessionID, note)
		said = fmt.Sprintf(" It works for your task %s (%q).", t.ID, t.Title)
	}
	return fmt.Sprintf("Resumed %s on %s (same session, with its context, on its own launcher and permission mode) and sent.",
		agentLabel(c), am.Name) + compacted + handed + said, nil
}

// resumeNote is the line the user reads about a resume: which agent, where, and why.
func (s *Service) resumeNote(ctx context.Context, c proto.Session, am Device, why string) string {
	return i18n.T(s.lang(ctx), "assistant-tasks.resume_note", i18n.Args{"agent": agentLabel(c), "machine": am.Name, "why": why})
}

// noteResume puts the resume's note on task id's card in conversation self, as Persona's, naming the run it resumed
// (machine, sid; #10465).
func (s *Service) noteResume(ctx context.Context, self, id, machine, sid, note string) {
	if _, err := s.appendFeed(ctx, self, feedRouted, id, map[string]string{"text": note, "from": routedFromAssistant,
		"machine_id": machine, "session_id": sid}); err != nil {
		s.logf("task %s: resume feed: %v", id, err)
	}
}

// cleanWhy is resume_why as kept: one line, clipped.
func cleanWhy(why string) string {
	return clip(oneLine(strings.TrimSpace(why)), 300)
}
