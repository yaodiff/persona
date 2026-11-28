package assistant

import (
	"context"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Stopping the agents of finished tasks (docs/assistant/todos.md "Stop Agents of completed tasks"). Once a task opened from an
// assistant conversation is done — by the user or by the assistant's judge (done.go) — its agents that have stayed
// still for a while are stopped: the node quits the agent process and keeps its terminal, the shell it ran in (node
// retire.go, session.stop; #4754; the node closes that terminal itself only after its grace period, #10111). The todo keeps the agents
// (machine, session id, directory) and the node keeps how each was started (launcher, model, modes), so reading one
// goes on from its transcript and its terminal stays open on its computer.
//
// A done task's never-started agent can also be stopped at a startup screen. Agents that have run and wait for the
// user, those with background work, and those still linked to an open task are kept.

var (
	// taskCloseIdle is how long a done task's agent must have been still (and the task done) before it is closed.
	taskCloseIdle = taskUserIdle
	// taskCloseEvery is how often the assistant looks for agents to close (tests shorten them).
	taskCloseEvery = time.Minute
	// taskCloseRetry is how long a node's refusal stands for an agent that did not change: a node updated meanwhile
	// may close it now (a deploy restarts the hub first and the resident node once it is idle: the old one is asked,
	// which does not know session.retire).
	taskCloseRetry = 10 * time.Minute
)

// taskCloseRefusal is a node's refusal to close an agent: the session's updated_at then, when, and why.
type taskCloseRefusal struct {
	updated, at time.Time
	why         string
}

// taskCloseRefused: (machine, session) → its node's last refusal, so it is not asked every minute while nothing
// changed.
type taskCloseRefused map[[2]string]taskCloseRefusal

// doneSeen: (machine, session) → what the pass saw of a done task's agent since the task was done (#10311).
// Observation clocks start over at a manager restart. The stop evidence is loaded separately.
type doneSeen map[[2]string]doneSeenAgent

// doneSeenAgent is what the pass saw of one agent of a task done at done.
type doneSeenAgent struct {
	done time.Time // the task's done time it is about: a task done again later starts over
	// running is when the pass first saw it running in a pane, zero while it is stopped: its stillness counts from
	// no earlier, so an agent resumed with no message (its last step is old) is not stopped on the next pass.
	running time.Time
	stopped bool // seen stopped since the task was done, or stopped by this pass
	// lastStop is when this pass last confirmed a stop. A stale row older than it is retired for workspace cleanup;
	// newer activity is a resume and keeps the workspace open.
	lastStop time.Time
	// resumed: running again after that. Someone brought it back on purpose (the owner's Resume, a message that
	// resumed it): this pass leaves it to the idle rule (tasks_idle.go), which covers resumed agents.
	resumed bool
}

func (s *Service) taskCloseLoop(ctx context.Context) {
	t := time.NewTicker(taskCloseEvery)
	defer t.Stop()
	refused := taskCloseRefused{}
	doneAgents := doneSeen{}
	spawnedAgents := doneSeen{}
	idleSeen := idleSeen{}
	endedSeen := endedSeen{}
	var archived time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.closeDoneTaskAgents(ctx, refused, doneAgents)
		s.closeDoneSpawnedAgents(ctx, refused, spawnedAgents)
		s.closeIdleAgents(ctx, refused, doneAgents, idleSeen)
		s.closeEndedAgentTerminals(ctx, endedSeen) // Agents that quit, with close_ended_terminals on (agent_terminal_close.go)
		s.orphanWatch(ctx, time.Now())             // open to-dos whose agents are all gone (tasks_orphan.go)
		if now := time.Now(); now.Sub(archived) >= autoArchiveEvery {
			archived = now
			s.archiveClosedSessions(ctx, now) // closed sessions idle for days (agent_archive_auto.go)
		}
	}
}

// doneTaskAgent is an agent of a done task, with when the task was done.
type doneTaskAgent struct {
	todo, machine, session string
	doneAt                 time.Time
}

// doneTaskAgents are the agents still linked to tasks that are done, except those that count for an open todo now.
func (s *Service) doneTaskAgents() []doneTaskAgent {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	var out []doneTaskAgent
	for _, t := range s.todos.todos {
		if t.Origin == "" || t.DoneAt == nil {
			continue
		}
		for _, a := range t.Agents {
			if a.ReleasedAt != nil || a.DismissedAt != nil {
				continue
			}
			if _, open := s.todos.activeBySession[[2]string{a.MachineID, a.SessionID}]; open {
				continue
			}
			out = append(out, doneTaskAgent{todo: t.ID, machine: a.MachineID, session: a.SessionID, doneAt: *t.DoneAt})
		}
	}
	return out
}

// retirable says whether s, an agent of a task done at doneAt, may be closed now: running in a pane of the engine,
// still since both the task was done and its last step at least taskCloseIdle ago, and with nothing going on.
func retirable(s proto.Session, doneAt, now time.Time) bool {
	return retirableAfter(s, doneAt, now, taskCloseIdle)
}

// retirableAfter is retirable with the time the agent must have been still.
func retirableAfter(s proto.Session, doneAt, now time.Time, idle time.Duration) bool {
	isRuntime := runtimeCanClose(s)
	switch {
	case s.Assistant, s.Starting, !s.StartedByPersona, s.Archived:
		return false
	case !isRuntime && s.SourceKind != "":
		return false
	case s.Status == proto.StatusOffline || (!isRuntime && s.PaneID == ""):
		return false // not running: nothing to close
	case s.Status == proto.StatusWorking || s.Status == proto.StatusBlocked || s.Status == proto.StatusUnknown:
		return false // working, waiting, or the engine cannot tell
	case s.State == proto.StateWorking || s.State == proto.StateNeedsYou || s.PendingInteractions > 0:
		return false
	case taskAgentHasUnfinishedWork(s):
		return false // more to come, or something it runs that the user may be using
	}
	// Only agents a node quits with Ctrl+C in their pane (node retire.go stoppable), or runtime-backed sessions with
	// an explicit close capability (session.retire close_terminal). agy quits in panes too (#11087).
	if !isRuntime && s.Agent != "" && s.Agent != "claude" && s.Agent != "codex" && s.Agent != "traex" && s.Agent != "agy" {
		return false
	}
	last := doneAt
	if s.UpdatedAt.After(last) {
		last = s.UpdatedAt
	}
	return now.Sub(last) >= idle
}

func taskAgentHasUnfinishedWork(c proto.Session) bool {
	if len(c.Queued) > 0 || len(c.Background) > 0 || c.Loop != nil || len(c.Ports) > 0 {
		return true
	}
	for _, sa := range c.Subagents {
		if sa.Running {
			return true
		}
	}
	for _, w := range c.Workflows {
		if w.Running {
			return true
		}
	}
	return false
}

// closeDoneTaskAgents stops eligible agents of done tasks; refused remembers the sessions a node would not close,
// seen what each agent did since its task was done.
func (s *Service) closeDoneTaskAgents(ctx context.Context, refused taskCloseRefused, seen doneSeen) {
	if !s.todosLoaded() {
		return
	}
	if err := s.loadTaskCloseStops(ctx); err != nil {
		s.logf("completed task agents: load stop evidence: %v", err)
		return
	}
	defer s.closeDoneTaskSpaces(ctx, time.Now(), seen)
	defer s.saveTaskCloseStops(ctx)
	stops := &s.taskCloseStops
	agents := s.doneTaskAgents()
	if len(agents) == 0 {
		clear(seen)
		stops.prune(nil)
		return
	}
	now := time.Now()
	reachable := map[string]bool{}
	for _, d := range s.devices(ctx) {
		reachable[d.ID] = d.Reachable
	}
	lists := map[string]map[string]proto.Session{}
	done := make(map[[2]string]bool, len(agents))
	completions := make(map[string]time.Time, len(agents))
	for _, a := range agents {
		key := [2]string{a.machine, a.session}
		done[key] = true // an unreachable machine keeps what was seen
		id := a.stopKey()
		completions[id] = a.doneAt
		if !reachable[a.machine] {
			continue
		}
		c, ok := s.doneTaskAgentRow(ctx, lists, a)
		if !ok {
			continue
		}
		stopped := sameTaskCompletion(stops.completions[id], a.doneAt) || s.archivedStopAfterTaskDone(a)
		w := seen.see(key, a.doneAt, c, now, stopped)
		if w.stopped {
			stops.note(id, a.doneAt)
		}
		if w.resumed || w.running.IsZero() {
			continue
		}
		since := a.doneAt
		if w.running.After(since) {
			since = w.running
		}
		startupBlocked := now.Sub(a.doneAt) >= taskCloseIdle && blockedAtTaskStartup(c)
		if !startupBlocked && (!retirable(c, since, now) || (runtimeCanClose(c) && !s.closeEndedTerminals(ctx))) {
			continue
		}
		if s.sessionActivityReason(ctx, a.machine, a.session, now) != "" {
			continue
		}
		if r, ok := refused[key]; ok && r.updated.Equal(c.UpdatedAt) && now.Sub(r.at) < taskCloseRetry {
			continue
		}
		if !s.doneTaskAgentStillLinked(a) {
			continue
		}
		if startupBlocked {
			cards := s.taskStartupScreenCards(a.machine, a.session)
			if len(cards) == 0 || len(cards) < c.PendingInteractions || !s.stopTaskStartup(ctx, refused, a, c, cards, now) {
				continue
			}
		} else if !s.retireAgent(ctx, refused, "task "+a.todo, a.machine, c, now) {
			continue
		}
		// Stopped by this pass: running again before a pass sees it stopped is a resume too.
		w.stopped, w.running, w.lastStop = true, time.Time{}, now
		seen[key] = w
		stops.note(id, a.doneAt)
		s.saveTaskCloseStops(ctx)
		if !startupBlocked {
			s.archiveRetiredTaskAgent(ctx, a, c)
		}
	}
	for k := range seen {
		if !done[k] { // its task reopened, or the agent left it
			delete(seen, k)
		}
	}
	stops.prune(completions)
}

func blockedAtTaskStartup(c proto.Session) bool {
	return c.StartedByPersona && !c.Assistant && c.SourceKind == "" && !c.Starting && c.PaneID != "" &&
		c.Status == proto.StatusBlocked && c.State != proto.StateWorking && c.UpdatedAt.IsZero() &&
		!c.BackgroundWorking && !c.BackgroundWaiting && !taskAgentHasUnfinishedWork(c)
}

// doneTaskAgentStillLinked rechecks ownership after fetching the agent's row.
func (s *Service) doneTaskAgentStillLinked(a doneTaskAgent) bool {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	t := s.todos.todos[a.todo]
	_, open := s.todos.activeBySession[[2]string{a.machine, a.session}]
	if open || t == nil || t.Origin == "" || t.DoneAt == nil || !sameTaskCompletion(*t.DoneAt, a.doneAt) {
		return false
	}
	for _, link := range t.Agents {
		if link.MachineID == a.machine && link.SessionID == a.session && link.ReleasedAt == nil && link.DismissedAt == nil {
			return true
		}
	}
	return false
}

// taskStartupScreenCards requires positive evidence of a startup screen and preserves work or sensitive cards.
func (s *Service) taskStartupScreenCards(machine, session string) []string {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	var ids []string
	for id, p := range s.fleet.interactions {
		ix := p.ix
		if ix.SessionID != session || !s.sameMachine(ix.MachineID, machine) {
			continue
		}
		if ix.Kind != proto.IxScreen || ix.Private || ix.Secret || len(ix.Fields) > 0 || p.claimed || p.prepared != nil {
			return nil
		}
		ids = append(ids, id)
	}
	return ids
}

func (s *Service) stopTaskStartup(ctx context.Context, refused taskCloseRefused, a doneTaskAgent, c proto.Session, cards []string, now time.Time) bool {
	cctx, cancel := s.retirementContext(ctx, a.machine, a.session)
	defer cancel()
	if cctx.Err() != nil {
		return false
	}
	params := map[string]any{"session_id": a.session}
	if err := s.call(cctx, a.machine, deviceSessionStop, params, nil); err != nil {
		s.noteTaskCloseError(refused, "task "+a.todo, a.machine, c, now, err)
		return false
	}
	delete(refused, [2]string{a.machine, a.session})
	s.endSessionCards(a.machine, a.session, cards...)
	s.logf("task %s: stopped agent %s/%s at its startup screen", a.todo, a.machine, a.session)
	return true
}

// see notes c, the row of an agent of a task done at done, and returns what has been seen of it by now.
func (seen doneSeen) see(key [2]string, done time.Time, c proto.Session, now time.Time, stopped bool) doneSeenAgent {
	w := seen[key]
	if !sameTaskCompletion(w.done, done) {
		w = doneSeenAgent{done: done}
	}
	w.stopped = w.stopped || stopped
	switch {
	case c.Starting: // still launching (the task was done before it started, #2954): neither stopped nor running yet
	case c.Status == proto.StatusOffline || (!runtimeCanClose(c) && c.PaneID == ""):
		w.stopped, w.running = true, time.Time{}
	default:
		if w.stopped {
			w.resumed = true
		}
		if w.running.IsZero() {
			w.running = now
		}
	}
	seen[key] = w
	return w
}

// doneTaskAgentRow is a's session as the apps get it (the state and the cards waiting count). With the fleet in this
// process it is the row a's node lists, decorated alone (listedSession): one only the archive keeps is offline, never
// retirable, so a task done long ago whose agent's session is gone costs a lookup each pass, not its machine's whole
// list (#3197). Otherwise each machine is listed once a pass, into lists.
func (s *Service) doneTaskAgentRow(ctx context.Context, lists map[string]map[string]proto.Session, a doneTaskAgent) (proto.Session, bool) {
	if s.fleetOn() && a.machine != DeviceAll {
		m, ok := s.fleetMachine(a.machine)
		if !ok {
			return proto.Session{}, false // sessions.list: no such machine, no sessions
		}
		return s.listedSession(m.ID, a.session)
	}
	list, ok := lists[a.machine]
	if !ok {
		list = map[string]proto.Session{}
		got, _ := s.sessions(ctx, a.machine)
		for _, c := range got {
			list[c.SessionID] = c
		}
		lists[a.machine] = list
	}
	c, ok := list[a.session]
	return c, ok
}

// retireAgent asks c's node for a guarded automatic stop and remembers a
// refusal; who names the reason in the log. True when the node confirmed the agent is no longer running.
func (s *Service) retireAgent(ctx context.Context, refused taskCloseRefused, who, machine string, c proto.Session, now time.Time) bool {
	key := [2]string{machine, c.SessionID}
	var res struct {
		Stopped bool `json:"stopped"`
		Closed  bool `json:"closed"`
	}
	cctx, cancel := s.retirementContext(ctx, machine, c.SessionID)
	defer cancel()
	cause := proto.StopTaskCompleted
	if who == "idle" {
		cause = proto.StopIdle
	}
	if cctx.Err() != nil {
		return false
	}
	if runtimeCanClose(c) {
		if agentCloseBusy(c) || !s.closeEndedTerminals(ctx) {
			return false
		}
		closed, err := s.closeEndedTerminal(cctx, machine, c.SessionID, cause)
		if err != nil {
			s.noteTaskCloseError(refused, who, machine, c, now, err)
			return false
		}
		if !closed {
			return false
		}
		delete(refused, key)
		s.logf("%s: closed runtime agent %s/%s via session.retire (close_ended_terminals)", who, machine, c.SessionID)
		return true
	}
	// Both task and long-idle retirement honor close-on-end (#10355) through the guarded stop RPC.
	closeIt := !agentCloseBusy(c) && s.closeEndedTerminals(ctx)
	params := map[string]any{"session_id": c.SessionID, "retire": true, "stop_cause": cause}
	if closeIt {
		params["close_terminal"] = true
	}
	if err := s.call(cctx, machine, nodeproto.StopIfIdle, params, &res); err != nil {
		s.noteTaskCloseError(refused, who, machine, c, now, err)
		return false
	}
	delete(refused, key)
	switch {
	case res.Closed:
		s.logf("%s: closed agent %s/%s and its terminal (close_ended_terminals)", who, machine, c.SessionID)
	case res.Stopped:
		s.logf("%s: stopped agent %s/%s (its terminal stays)", who, machine, c.SessionID)
	}
	return true
}

func (s *Service) noteTaskCloseError(refused taskCloseRefused, who, machine string, c proto.Session, now time.Time, err error) {
	var re *rpc.Error
	if errors.As(err, &re) { // a refusal, including an old peer without guarded stop: not again for a while
		key := [2]string{machine, c.SessionID}
		if prev, ok := refused[key]; !ok || prev.why != err.Error() {
			s.logf("%s: agent %s/%s left open: %v", who, machine, c.SessionID, err)
		}
		refused[key] = taskCloseRefusal{updated: c.UpdatedAt, at: now, why: err.Error()}
	} else {
		s.logf("%s: close agent %s/%s: %v", who, machine, c.SessionID, err)
	}
}

func (s *Service) archiveRetiredTaskAgent(ctx context.Context, a doneTaskAgent, c proto.Session) {
	// The inventory row predates session.stop. Read the node's saved result before
	// native archival removes that row; neither the request nor task closure proves a stop cause.
	var info struct {
		StopCause proto.StopCause `json:"stop_cause"`
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err := s.call(cctx, a.machine, "session.info", map[string]any{"session_id": a.session}, &info)
	cancel()
	caps := &proto.SessionCapabilities{Read: true}
	if c.Capabilities != nil {
		caps.Terminal = c.Capabilities.Terminal
	}
	if err == nil {
		caps.StopCause = info.StopCause
	} else {
		s.logf("closed task agent %s/%s: stop provenance unavailable: %v", a.machine, short(a.session), err)
	}
	c.Capabilities = caps

	// session.stop can take seconds; new ownership or owner activity wins over
	// archiving the session the earlier snapshot said was safe to retire, including during the provenance read.
	t, ok := s.todoNow(a.todo)
	if !ok || t.DoneAt == nil || !sameTaskCompletion(*t.DoneAt, a.doneAt) {
		return
	}
	if open, _, _ := s.openTaskOf([2]string{a.machine, a.session}); open {
		return
	}
	if s.sessionActivityReason(ctx, a.machine, a.session, time.Now()) != "" {
		return
	}
	c.ReadOnlyReason = i18n.T(s.lang(ctx), "hub-agent_archives.task_closed")
	changedAt, err := s.noteAgentArchiveAt(ctx, c, true)
	if err != nil {
		s.logf("closed task agent %s/%s: archive: %v", c.MachineID, short(c.SessionID), err)
		return
	}
	c.ArchiveChangedAt = archiveChangedAt(changedAt)
	publishMark, err := s.noteAgentArchiveClosed(ctx, c, time.Now().UTC(), false)
	if err != nil {
		s.logf("closed task agent %s/%s: archive close reason: %v", c.MachineID, short(c.SessionID), err)
		s.publishList(c.MachineID) // the archive committed even when recording its close metadata failed
	} else if publishMark != 0 {
		s.publishListAtArchiveMark(c.MachineID, publishMark)
	}
}
