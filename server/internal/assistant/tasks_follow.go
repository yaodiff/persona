package assistant

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- following a task's agents ----

// taskAgents are the sessions of t's active agents, as the clients see them (with state).
func (s *Service) taskAgents(ctx context.Context, t Todo) []proto.Session {
	return s.taskAgentsIn(ctx, t, machineSessions{})
}

// machineSessions are the machines' session lists read so far in one pass over many tasks (taskAgentsIn), by
// machine: each machine is listed once for all of them, not once per task (#3029). A failed read is kept too.
type machineSessions map[string]machineList

type machineList struct {
	list []proto.Session
	err  error
}

// findListedSession finds the row in list with SessionID == id, else the unique row whose Aliases name id.
// Returns false when no row matches or when more than one row claims the same alias.
func findListedSession(list []proto.Session, id string) (proto.Session, bool) {
	var aliasMatch proto.Session
	aliases := 0
	for _, c := range list {
		if c.SessionID == id {
			return c, true
		}
		if slices.Contains(c.Aliases(), id) {
			aliasMatch = c
			aliases++
		}
	}
	if aliases == 1 {
		return aliasMatch, true
	}
	return proto.Session{}, false
}

// resolveTaskSession resolves a todo agent's stored session id to the session currently listed on machine, following
// proto.Session.Aliases when a pane placeholder row moved to its companion row (or vice versa).
func (s *Service) resolveTaskSession(ctx context.Context, machine, id string) (proto.Session, string, bool) {
	if c, ok := s.session(ctx, machine, id); ok {
		return c, c.SessionID, true
	}
	if list, err := s.sessions(ctx, machine); err == nil {
		if c, ok := findListedSession(list, id); ok {
			return c, c.SessionID, true
		}
	}
	return proto.Session{}, id, false
}

// taskAgentsIn is taskAgents with the lists already read in this pass; the machines it lists are added to lists.
func (s *Service) taskAgentsIn(ctx context.Context, t Todo, lists machineSessions) []proto.Session {
	var out []proto.Session
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		l, ok := lists[a.MachineID]
		if !ok {
			l.list, l.err = s.sessions(ctx, a.MachineID)
			lists[a.MachineID] = l
		}
		if l.err != nil {
			continue
		}
		if c, ok := findListedSession(l.list, a.SessionID); ok {
			out = append(out, c)
		}
	}
	return out
}

// sessionsReached lists machine's sessions, retrying a dropped or offline link a bounded number of times within
// ctx. reached is false when the node could not be asked after the retries: the caller must not read that as
// "the machine has no sessions". ErrNoDevice is deliberately different: the hub itself answers that it has no
// such machine (it was removed from the hub's configuration), a definite answer rather than a flaky node — an
// agent registered on a machine the hub no longer knows cannot still be waiting, so it answers with an empty
// list and reached true. Retrying it would spend the budget and wrongly keep a needs notice for a machine gone
// for good.
func (s *Service) sessionsReached(ctx context.Context, machine string) ([]proto.Session, bool) {
	backoff := agentReachBackoff
	for attempt := 0; ; attempt++ {
		list, err := s.sessions(ctx, machine)
		if err == nil {
			return list, true
		}
		if errors.Is(err, ErrNoDevice) {
			return nil, true // definite: the hub no longer knows this machine
		}
		if attempt+1 >= agentReachAttempts || ctx.Err() != nil {
			return nil, false
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, false
		case <-timer.C:
		}
		backoff *= 2
	}
}

// taskAgentsReached is taskAgents plus reachability. Each distinct machine of t's active agents is asked once
// within one agentReachMax deadline so retries across machines cannot stack; reached is false when one stayed
// unreachable, so callers keep "still waits" rather than read an unreachable node as an answer (ErrNoDevice: none).
func (s *Service) taskAgentsReached(ctx context.Context, t Todo) (agents []proto.Session, reached bool) {
	ctx, cancel := context.WithTimeout(ctx, agentReachMax)
	defer cancel()
	reached = true
	lists, asked := map[string][]proto.Session{}, map[string]bool{}
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil || asked[a.MachineID] {
			continue
		}
		asked[a.MachineID] = true
		list, ok := s.sessionsReached(ctx, a.MachineID)
		if !ok {
			reached = false
			continue
		}
		lists[a.MachineID] = list
	}
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil {
			continue
		}
		if c, ok := findListedSession(lists[a.MachineID], a.SessionID); ok {
			agents = append(agents, c)
		}
	}
	return s.withPersonWait(agents), reached
}

// taskNeedsRecheck freshly asks t's agents' machines whether one still waits for the user. known is false when an
// agent machine stayed unreachable after the bounded retries, so a caller can keep the notice rather than take
// an unreachable node for an answer.
func (s *Service) taskNeedsRecheck(ctx context.Context, t Todo) (waits, known bool) {
	agents, reached := s.taskAgentsReached(ctx, t)
	if !reached {
		return false, false
	}
	for _, c := range agents {
		if c.State == proto.StateNeedsYou || c.PendingInteractions > 0 {
			return true, true
		}
	}
	return false, true
}

// taskWaits: one of t's agents still waits for the user. A machine the recheck cannot reach is not an answer: it
// is taken as still waiting, so a needs notice is never dropped on a transient RPC failure.
func (s *Service) taskWaits(ctx context.Context, t Todo) bool {
	waits, known := s.taskNeedsRecheck(ctx, t)
	return waits || !known
}

const (
	ignoredClosedStart = "task %s: accepted; its agent works again, but nothing was sent for the task: left closed"
	droppedClosedEnd   = "task %s: closed; its agent %s/%s ended a turn: dropped"
)

const maxClosedTurnLogs = 256

type closedTurnLog struct {
	todo, machine, session string
	doneAt                 time.Time
	format                 string
}

// Remember the most recent diagnostics for closed tasks in this manager. Eviction or a
// restart can log an old closure again; the log history never participates in a turn decision.
func (ts *tasksState) firstClosedTurnLog(t *Todo, machine, session string, format string) bool {
	entry := closedTurnLog{todo: t.ID, machine: machine, session: session, doneAt: t.DoneAt.UTC(), format: format}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	logs := ts.closedTurnLogs
	for i := len(logs) - 1; i >= 0; i-- {
		if logs[i] == entry {
			copy(logs[i:], logs[i+1:])
			logs[len(logs)-1] = entry
			return false
		}
	}
	if len(logs) == maxClosedTurnLogs {
		logs = slices.Delete(logs, 0, 1)
	}
	ts.closedTurnLogs = append(logs, entry)
	return true
}

// taskWorking: one of the task's agents started a turn. The agent of a task the hub closed meanwhile (judged done,
// accepted) works on it again — sent more by agent_send, which resumes a closed one, or by the user: the task is open
// again first, as a change asked for on its "Please verify" card reopens it (#3457), so this turn is followed
// (interrupted or done receipt, judgment) and the task shows in progress, not done (#5892). An accepted task opens
// again only for what its conversation sent (closedTurnReopens; once the node took that send, the send opened it
// already, agentSendNote.sent); any other turn leaves it closed, and its end makes nothing of it either (settleTurn
// follows open tasks only). A turn that starts while such a send is still out waits for its answer (closedTurnHeld).
func (s *Service) taskWorking(ctx context.Context, mid, sid string, row startRow) {
	s.tasks.completionMu.Lock()
	defer s.tasks.completionMu.Unlock()
	t, ok := s.taskOf(ctx, mid, sid)
	if ok && s.startBeforeTakenTurn(t.ID, mid, sid, row) {
		// Listed before the turn it began ended, and handled after that end was taken up: no new stretch, which would
		// put the judgment being made for that turn out of date and have it judged again (#13711).
		s.logf("task %s: its agent %s/%s's start came after the turn it began was taken up: no new stretch", t.ID, mid, short(sid))
		return
	}
	if !ok {
		closed, found := s.closedTaskOf(ctx, mid, sid)
		if !found {
			return
		}
		s.loadSendsOut(ctx) // the sends of more for it a manager before this one kept (tasks_sends_out.go)
		switch s.closedTurnReopens(closed, mid, sid) {
		case closedTurnHeld:
			s.logf("task %s: accepted; its agent works again while more for it is being sent: held for that send's answer", closed.ID)
			s.saveSendsOut(ctx) // the hold too: after a restart, the turn is the task's (recoverSendsOut)
			return
		case closedTurnStays:
			if s.shouldLogClosedTurn(closed, mid, sid, false) {
				s.logf(ignoredClosedStart, closed.ID)
			}
			return
		}
		if err := s.reopenTodo(ctx, closed.ID); err != nil {
			s.logf("task %s: reopen: %v", closed.ID, err)
			return
		}
		if closed.DoneBy == db.DoneByReview {
			s.sendsOpened(ctx, closed, mid, sid) // the send it was opened for is done with
		}
		if t, ok = s.taskOf(ctx, mid, sid); !ok {
			return
		}
		s.logf("task %s: reopened: its agent works again", t.ID)
	}
	s.clearDoneProgress(ctx, t.ID)
	s.taskWorks(ctx, t, row.NativeLoop)
}

// taskWorks is what follows a turn's start on open task t: the stretch of work begins now, and what was judged or
// waited for before is moot. s.tasks.completionMu is held (taskWorking, or taskStartMissed for a start nobody heard).
func (s *Service) taskWorks(ctx context.Context, t Todo, nativeLoop bool) {
	now := time.Now()
	s.tasks.mu.Lock()
	s.tasks.working[t.ID] = now
	s.tasks.mu.Unlock()
	if err := s.recordTodoCloseDecline(ctx, t.ID, time.Time{}); err != nil {
		s.logf("task %s: clearing close decline: %v", t.ID, err)
	}
	// A native loop can wake itself. Keep the park until its completed
	// transcript establishes that its turn advanced the work.
	if nativeLoop && t.Supervision.state(t) == reviewParked {
		return
	}
	s.interruptsSoon()          // an interrupted receipt of it reads resumed now (interruptions.go)
	s.clearTaskCheck(ctx, t.ID) // it judged the work before this turn (task_check.go)
	if t.hasTaskRuntime() {
		s.taskRuntimeWorking(ctx, t, now)
	} else {
		s.setTaskStage(ctx, t.ID, "develop", "")
	}
	s.taskPlanApproval()
	if t.Supervision != nil {
		s.supervisedWorking(ctx, t)
	}
}

// sendReopens: what conversation self sends the agent of accepted task t asks t for more. self is t's conversation, its
// supervisor, or the background supervisor standing in for the main conversation (backgroundSupervises: it accepts and
// reworks the main conversation's tasks while their ledger keeps the main conversation as the supervisor), for a task
// whose signals go to it: one whose ledger names the main conversation or no one (digestTo). A task another
// conversation supervises sends that conversation its review signals, so the background supervisor's send leaves it
// closed. Any other conversation's send is its own business with the agent.
func (s *Service) sendReopens(ctx context.Context, self string, t Todo) bool {
	if self == "" {
		return false
	}
	if self == t.Origin || t.Supervision != nil && self == t.Supervision.Supervisor {
		return true
	}
	if !s.backgroundSupervises(ctx, self, t) {
		return false
	}
	sup := t.Supervision.Supervisor
	return sup == "" || sup == s.settings(ctx).Main
}

// closedTurn is what a new turn of its agent does to a closed task (closedTurnReopens).
type closedTurn int

const (
	closedTurnStays closedTurn = iota // other work: the task stays closed
	closedTurnOpens                   // the task opens again, and the turn is followed
	closedTurnHeld                    // a send that may reopen it is out: its answer decides (agentSendNote.sent)
)

const closedTurnLoggedCap = 512

type closedTurnLogKey struct {
	todoID  string
	doneAt  int64
	machine string
	session string
	ended   bool
}

// shouldLogClosedTurn reports whether the ignored-turn diagnostic for closed task t and agent mid/sid has not yet been
// logged at t's current DoneAt. Returning true only on the first occurrence per (task, DoneAt, agent, start/end) keeps
// first-turn observability while stopping reused long-lived sessions from spamming identical log lines on every
// unrelated turn (#16421). Reopening and later closing the task produces a new DoneAt and logs fresh.
func (s *Service) shouldLogClosedTurn(t Todo, mid, sid string, ended bool) bool {
	var doneAt int64
	if t.DoneAt != nil {
		doneAt = t.DoneAt.UnixNano()
	}
	key := closedTurnLogKey{todoID: t.ID, doneAt: doneAt, machine: mid, session: sid, ended: ended}
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	if s.tasks.closedTurnLogged == nil {
		s.tasks.closedTurnLogged = make(map[closedTurnLogKey]struct{})
	}
	if _, seen := s.tasks.closedTurnLogged[key]; seen {
		return false
	}
	if len(s.tasks.closedTurnLogged) >= closedTurnLoggedCap {
		clear(s.tasks.closedTurnLogged)
	}
	s.tasks.closedTurnLogged[key] = struct{}{}
	return true
}

// closedTurnReopens: what a new turn of agent mid/sid does to closed task t (taskWorking). One the hub's judge closed
// on its own opens again: that close is provisional (nobody accepted it, the judge may have been early), and more for
// it comes as a turn of its agent (#5892). One its supervisor accepted (done_by review) is closed for good, whoever
// started its agent: a turn for the agent's own timers, other conversations, messages from other sessions or the
// owner's words in its own session is no word that the task wants more. Such turns reopened the finished task each
// time and woke its supervision again, "judged done, waiting for your review" (#6726 for adopted agents; #7539 for
// agents a task_plan started, which once reopened on any turn). An accepted task reopens only for what the task's
// conversation, its supervisor or the background supervisor acting for it (sendReopens) sent the agent after the
// acceptance: agent_send, agent_interrupt with then, or the owner's message on the task's card (which, when its agents
// have all stopped, reopens the task once its new agent started, routeToTaskAs). A send the node took reopens the task
// itself (agentSendNote.sent, tasks_sends_out.go). The agent's next turn after a send whose answer was unknown opens it
// here, however late (the agent may read the text much later); one that starts while a send is out is held for the
// answer. The owner can still reopen one by hand,
// and todo_reopen does too. That covers an acceptance by the judge's own spot check (supervise_sample.go), which is a
// review too.
func (s *Service) closedTurnReopens(t Todo, mid, sid string) closedTurn {
	if t.DoneBy != db.DoneByReview {
		return closedTurnOpens
	}
	if t.DoneAt == nil {
		return closedTurnStays
	}
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	o := s.tasks.sendsOut[[2]string{mid, sid}]
	switch pending, answered := o.asking(t.ID, *t.DoneAt, time.Now()); {
	case answered: // it may be the turn that send asked for: no answer to wait for
		return closedTurnOpens
	case pending:
		o.held, o.heldAt, o.ended = t.ID, *t.DoneAt, false
		return closedTurnHeld
	}
	return closedTurnStays
}

type receiptBody struct {
	Title   string `json:"title"`
	State   string `json:"state"` // done, failed, interrupted
	Summary string `json:"summary,omitempty"`
	PR      string `json:"pr,omitempty"`
	Machine string `json:"machine,omitempty"`
	// Files: what the agent handed over this turn (task_files.go, #10511): name and where it is on the conversation's
	// computer, or why it could not come. Clients that do not know it ignore it.
	Files  []taskFile     `json:"files,omitempty"`
	Review *receiptReview `json:"review,omitempty"`
	// MachineID and SessionID name the run whose turn this is (#10465), so a reader of the to-do's history can give the
	// receipt to that run instead of guessing from the computer's name. Absent where no run started (a failed start)
	// and on older receipts.
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// Criteria: a done receipt's list of its brief's criteria as they stood when the agent stopped (#3822,
	// briefCriteriaNow): met or not met as the supervision ledger tracks them (#3635: the agent's reports and the
	// reviews), unchecked while pending there, or dropped by a scope change.
	// Absent without a brief or criteria. CriteriaMore: how many the list left out.
	Criteria     []BriefCriterion `json:"criteria,omitempty"`
	CriteriaMore int              `json:"criteria_more,omitempty"`
	// Reroute names the machine a flexible task was moved off and where it landed, with the health-check reason;
	// absent when it ran where it was asked (#2271).
	Reroute *PlanReroute `json:"reroute,omitempty"`
	// Error: what the model service answered when the turn was cut off (interrupted), for the card's detail.
	Error string `json:"error,omitempty"`
	// Interrupted receipts only (interruptions.go): Cause is the kind of error, Group the receipt heading the batch this
	// one joined (empty on a head), Resolution how its task was dealt with since (worked out on every read), Batch the
	// batch on its head.
	Cause      string        `json:"cause,omitempty"`
	Group      string        `json:"group,omitempty"`
	Resolution string        `json:"resolution,omitempty"`
	Batch      *receiptBatch `json:"batch,omitempty"`
}

// rerouteOfTask resolves the recorded machine reroute for the plan task that runs todoID: nil when the task has no
// plan, the plan no longer holds it, or it was never moved (#2271).
func (s *Service) rerouteOfTask(ctx context.Context, todoID, planID string) *PlanReroute {
	if planID == "" {
		return nil
	}
	p, err := s.loadPlan(ctx, planID)
	if err != nil || p == nil {
		return nil
	}
	for i := range p.Tasks {
		if p.Tasks[i].TodoID == todoID {
			if decision := p.Tasks[i].Placement; decision != nil && decision.Reroute != nil {
				return decision.Reroute
			}
			return p.Tasks[i].Reroute
		}
	}
	return nil
}

type receiptReview struct {
	State    string `json:"state"` // checking: Persona follows up; awaiting: completion already judged
	Criteria int    `json:"criteria"`
}

// interruptedSummary is an interrupted task's receipt: the error itself is no conclusion (#1516).
const interruptedSummary = assistantwire.InterruptedSummary

// quietTurnKind is what a turn, read as items, is when it had no output at all: proto.TurnErrorStopped when Persona's
// Stop interrupted it (stopped: stopFor found that Stop), proto.TurnErrorNoOutput when nothing did (silentTurn). "" for
// a turn with output, one the user stopped themselves (Esc: theirs to go on with), or one that could not be read.
func quietTurnKind(items []proto.Item, stopped bool) string {
	none, userStopped := turnWithoutOutput(items)
	switch {
	case !none:
		return ""
	case stopped:
		return proto.TurnErrorStopped // Claude Code may write the interrupted notice for Persona's Esc, or nothing
	case userStopped:
		return ""
	}
	return proto.TurnErrorNoOutput
}

// sameTurnRow: now is agent row last still, stopped: nothing was written to its transcript since (the same Rev, or the
// same time where the node gives no Rev).
func sameTurnRow(now, last proto.Session) bool {
	if now.Status == proto.StatusWorking || now.Starting {
		return false
	}
	if now.Rev != 0 || last.Rev != 0 {
		return now.Rev == last.Rev
	}
	return now.UpdatedAt.Equal(last.UpdatedAt)
}

// silentTurnErrorIn is the interrupted receipt's detail for a turn with no output at all (silentTurn, #2349), in l.
// Not kept in a package variable: that would read the catalogs whenever personad starts, a hook too (#1308).
func silentTurnErrorIn(l i18n.Lang) string { return i18n.T(l, "assistant-tasks.silent_turn") }

var prLink = lazyre.New(`https?://github\.com/[\w.-]+/[\w.-]+/pull/\d+|https?://[^/\s<>"()]+/(?:[^/\s<>"()]+/)+merge_requests/\d+\b`)

// taskTurnEnded: one of the task's agents (sid on machine) ended a turn. Once none of them works or waits for the
// user any more, the conversation gets a receipt (once per stretch of work), and the judge records delivery for review.
func (s *Service) taskTurnEnded(ctx context.Context, machine, sid string) {
	_, _ = s.taskTurnEndedOutcome(ctx, machine, sid)
}

// taskTurnEndedOutcome preserves whether this particular settlement could not reach an agent. A later probe is not a
// substitute: a transient failure may already have recovered by then, losing the reason to retain the recheck.
func (s *Service) taskTurnEndedOutcome(ctx context.Context, machine, sid string) (turnSettle, bool) {
	time.Sleep(s.settleDelay(ctx)) // a turn that ends to ask, or goes on at once, is not the end
	var unreachable bool
	result := s.settleTurnReporting(ctx, machine, sid, "", &unreachable)
	return result, unreachable
}

// taskTurnSettled is taskTurnEnded once the turn stayed still: after its sleep, or afterSettle's timer.
func (s *Service) taskTurnSettled(ctx context.Context, machine, sid string) {
	s.settleTurn(ctx, machine, sid, "")
}

// turnSettle is what settleTurn made of a turn's end.
type turnSettle int

const (
	// settleRefused: nothing is made of this end as the task stands: no task (or another one's agent), a cancelled
	// one, an agent let go, a meeting's turn. Looking at it again changes nothing.
	settleRefused turnSettle = iota
	// settleLater: not now. An agent works or waits, a usage limit is waited out, the node did not answer, another
	// look is at it, or the task changed under this one.
	settleLater
	// settleHeld: the turn stopped between its tools and its answer (the user stopped it there): no receipt until
	// something is written to its transcript.
	settleHeld
	// settleTaken: the turn has its receipt, from this look or one before it.
	settleTaken
)

// settleTurn is taskTurnSettled, and says what it made of the end. unheard: the todo for which the supervision sweep
// read this turn closed in the session's transcript, with the ledger not having it (recoverTurn, task_turns.go); ""
// for an end that was told. A start nobody heard is taken here then (taskStartMissed), and only once every look that
// can refuse or put off the end has passed and the transcript, read again, still shows the turn closed: an end that
// is not taken leaves the task as it was (its stage, its check, what its supervisor waits for).
func (s *Service) settleTurn(ctx context.Context, machine, sid, unheard string) turnSettle {
	result := s.settleTurnReporting(ctx, machine, sid, unheard, nil)
	return result
}

// settleTurnReporting additionally reports an unreachable agent read, the one settleLater reason that has no other
// wake guaranteed. Existing callers keep the turnSettle-only wrapper above.
func (s *Service) settleTurnReporting(ctx context.Context, machine, sid, unheard string, unreachable *bool) turnSettle {
	if s.meetingTurnNow(machine, sid) {
		return settleRefused // a meeting's batch: never the task's progress, report or completion (meeting_turns.go)
	}
	if s.endHeld(ctx, machine, sid) {
		s.logf("agent %s/%s ended a turn held for a send still out: taken up once that send is answered", machine, short(sid))
		return settleLater
	}
	t, ok := s.taskOf(ctx, machine, sid)
	if !ok {
		// A session opened manually on the Agents page is not adopted just because its turn did work (#5732).
		if closed, found := s.closedTaskOf(ctx, machine, sid); found {
			// Its closed task's agent still: a turn nobody sent for the task, or one running when it was accepted
			// (#7539). Nothing is made of its end; log once per close cycle so reused long-lived sessions do not
			// spam logs on every subsequent unrelated turn (#16421).
			if s.shouldLogClosedTurn(closed, machine, sid, true) {
				s.logf(droppedClosedEnd, closed.ID, machine, short(sid))
			}
		}
		return settleRefused
	}
	if unheard != "" && t.ID != unheard {
		return settleRefused // the session is another task's agent now: its turns are not this todo's
	}
	if current, ok := s.todoNow(t.ID); ok && (current.Cancelled() || current.AgentAborted(machine, sid)) {
		s.logf("task %s: cancelled or agent %s/%s aborted: skipping turn end", t.ID, machine, sid)
		return settleRefused
	}
	agents, reached := s.taskAgentsReached(ctx, t)
	agents = s.rowsProven(agents) // an unknown status whose transcript showed the turn closed is a stopped agent (task_turns.go)
	var inboxRecipient proto.Session
	if s.taskInboxRecipient(ctx, t.ID, machine, sid) {
		for _, c := range agents {
			if c.MachineID == machine && c.SessionID == sid {
				inboxRecipient = c
				break
			}
		}
	}
	if !reached || !taskAgentsSettled(t, agents) {
		if unreachable != nil {
			*unreachable = !reached
		}
		// An event belongs to one Agent's turn, not the aggregate task's completion.
		if taskAgentSettled(inboxRecipient) && !limitTakenUp(inboxRecipient, time.Now()) {
			items, read := s.lastTurnItems(ctx, machine, sid)
			if _, closed := turnEndOf(items); read && closed &&
				s.taskInboxTurnEnded(ctx, t, inboxRecipient, items, silentTurn(items)) {
				return settleTaken
			}
		}
		return settleLater
	}
	agents = currentTaskAgents(agents, supersededAgents(t, agents)) // not an agent it was handed over from (#6483)
	var last, cut proto.Session
	for _, c := range agents {
		if !taskAgentSettledIn(t, agents, c) { // as taskAgentsSettled read it: a done report's left-over shells do not hold it
			return settleLater
		}
		if w := c.Work; w != nil && w.Phase == proto.WorkBlocked && w.On == proto.WaitTransient && w.WakeAt != nil {
			return settleLater // "Continue" goes by itself (agents_work.go): the receipt waits for how that ends
		}
		if now := time.Now(); limitTakenUp(c, now) {
			// A usage limit stopped it until the reset, and its CLI or the manager goes on then (usage_limit.go): the
			// receipt waits for how that ends. Looked at again once the wait is over, so a turn that did not go on after
			// all (the continue cancelled) still gets its receipt.
			s.taskTurnEndedAfter(ctx, machine, sid, limitLookAgain(c, now))
			return settleLater
		}
		if c.UpdatedAt.After(last.UpdatedAt) {
			last = c
		}
		// The node's word, whatever the state now; or, once it stopped, Claude Code's last words when they are the
		// error ("API Error: …", "Not logged in · …"): the node's raw list has no turn error for those (fleet.go
		// turnError reads them for the apps).
		text, kind := strings.TrimSpace(c.TurnError), c.TurnErrorKind
		if text == "" {
			text, kind = turnError(c)
		}
		if proto.SilentTurnKind(kind) {
			text = "" // the hub's own word on a turn with no output, from a look before (silent_turns.go): its items say it again
		}
		if text != "" && (cut.SessionID == "" || c.UpdatedAt.After(cut.UpdatedAt)) {
			cut = c
			cut.TurnError, cut.TurnErrorKind = text, kind
		}
	}
	if inboxRecipient.SessionID != "" {
		last = inboxRecipient // another Agent's newer timestamp cannot choose this event's transcript
	}
	var items []proto.Item
	read := false
	missedStart := false
	if unheard != "" {
		// Nothing above refuses the end. The turn must still be closed where the node has it now (the sweep's read is
		// a moment old), no other look may be at it, and it must be one that gets a receipt, before a start nobody
		// heard is taken for it.
		heard, settling := s.turnStartHeard(t.ID)
		if settling {
			return settleLater
		}
		if items, read = s.lastTurnItems(ctx, last.MachineID, last.SessionID); !read {
			return settleLater
		}
		if _, closed := turnEndForSession(last, items); !closed {
			return settleLater // it went on since the sweep read it
		}
		_, stopped := s.stopFor(last.MachineID, last.SessionID, s.taskStretch(t))
		if cut.SessionID == "" && taskTurnAwaitingAnswer(items) && quietTurnKind(items, stopped) == "" {
			return settleHeld // stopped between its tools and its answer: held as below, with no start taken for it
		}
		if !heard {
			s.logf("task %s: its agent %s/%s ended a turn with no start heard: taken up", t.ID, last.MachineID, short(last.SessionID))
			missedStart = true
		}
	}
	s.tasks.mu.Lock()
	since, worked := s.tasks.working[t.ID]
	if !worked {
		since = t.CreatedAt // a hub restarted since it began: count from the start
	}
	if r, ok := s.tasks.receipted[t.ID]; ok && !r.Before(since) && !missedStart {
		s.tasks.mu.Unlock()
		return settleLater // this stretch has its receipt, or another look is writing it
	}
	reserved := time.Now()
	s.tasks.receipted[t.ID] = reserved
	// With the reservation, under the task lock: no look sees the one without the other (turnStartHeard), and the
	// sweep leaves this turn alone while its end is handled here. The mark is in memory: nothing here waits for the
	// ledger, whose mutex is held across its database writes (turnsState's lock order, task_turns.go).
	s.taskJudging(t.ID, 1)
	s.tasks.mu.Unlock()
	defer s.taskJudging(t.ID, -1)
	// Reserve first: resumed-work idle retraction may wait on the turn ledger, and a look must be able to observe this
	// receipt while that ledger is held. The task's other work still happens before its receipt is written.
	if missedStart {
		if !s.taskStartMissed(ctx, t.ID, last.Loop != nil || nativeLoopStart(items).NativeLoopID != "") {
			s.releaseReceipt(ctx, t.ID, machine, sid, reserved)
			return settleRefused
		}
		// taskWorks timestamps the newly observed stretch. Read it now, as the pre-reservation call used to provide it,
		// so taskWorkCurrent and autoFinishTask use that start rather than the task's creation time.
		s.tasks.mu.Lock()
		since, worked = s.tasks.working[t.ID]
		s.tasks.mu.Unlock()
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	// The computer named is the one of the run the receipt names (#10465), not of the session whose end set this off:
	// with a task's Agents on two computers the two can differ.
	run := last
	if cut.SessionID != "" {
		run = cut
	}
	name := cmp.Or(run.MachineID, machine)
	if m, ok := s.device(cctx, name); ok {
		name = m.Name
	}
	if !s.taskWorkCurrent(t.ID, since) || !s.taskRuntimeSettled(cctx, t) {
		s.releaseReceipt(ctx, t.ID, machine, sid, reserved)
		return settleLater
	}
	if unheard == "" {
		// The turn as its transcript has it: read before any receipt, since its end names the turn (task_turns.go).
		items, read = s.lastTurnItems(cctx, last.MachineID, last.SessionID)
		if !read {
			// A failed read cannot identify this turn's prompt or end. A later row or sweep may name it.
			items = nil
		}
		if !s.taskWorkCurrent(t.ID, since) || !s.taskRuntimeSettled(cctx, t) {
			s.releaseReceipt(ctx, t.ID, machine, sid, reserved)
			return settleLater
		}
	}
	end, named := turnEndForSession(last, items)
	quiet := "" // the turn had no output: proto.TurnErrorNoOutput, or proto.TurnErrorStopped (silent_turns.go)
	// The Stop that interrupted this turn, if one did: read once, it classifies the turn and is the one its receipt
	// spends, never a newer one (stopFor, forgetStop).
	stop, stopped := s.stopFor(last.MachineID, last.SessionID, since)
	if read {
		quiet = quietTurnKind(items, stopped)
	}
	if cut.SessionID != "" {
		s.forgetStop(last.MachineID, last.SessionID, stop)
		if _, again := s.takeTurn(cctx, t, last, items, end, named, since, turnInterrupted); again {
			if read && silentTurn(items) {
				s.taskInboxTurnEnded(cctx, t, last, items, true)
			}
			return settleTaken // this very turn was taken up before: no second receipt
		}
		// A turn cut off by the model service is no end of the work: an interrupted receipt (with "Continue" on the card),
		// and nothing to judge or close (#1516).
		s.logf("task %s: interrupted: %s", t.ID, clip(oneLine(cut.TurnError), 200))
		where := [2]string{cut.MachineID, cut.SessionID}
		body := receiptBody{Title: t.Title, State: "interrupted", Summary: i18n.T(s.lang(cctx), "assistant-tasks.interrupted"), Machine: name,
			Error: clip(strings.TrimSpace(cut.TurnError), 300)}
		s.appendInterrupted(cctx, t, SessionRef{MachineID: cut.MachineID, SessionID: cut.SessionID}, body, false)
		if s.closedWhileRead(t.ID) {
			return settleTaken
		}
		if read && silentTurn(items) && s.taskInboxTurnEnded(cctx, t, last, items, true) {
			return settleTaken
		}
		s.taskNotice(cctx, t, name, "interrupted", "", body.Error)
		s.taskReport(t, reportInterrupted, name, where, body.Error, nil)
		return settleTaken
	}
	if read && taskTurnAwaitingAnswer(items) && quiet == "" {
		// Do not consume this stretch's receipt while the runtime is between tools and its answer.
		s.tasks.mu.Lock()
		if !s.tasks.working[t.ID].After(since) {
			delete(s.tasks.receipted, t.ID)
			if s.tasks.awaitingAnswer == nil {
				s.tasks.awaitingAnswer = make(map[SessionRef]bool)
			}
			s.tasks.awaitingAnswer[SessionRef{MachineID: machine, SessionID: sid}] = true
		}
		s.tasks.mu.Unlock()
		// The answer's update may have arrived during the transcript read, before
		// the retry was registered. Reconcile that update instead of waiting for another tick.
		if latest, ok := s.session(cctx, last.MachineID, last.SessionID); ok &&
			(latest.Status != last.Status || latest.Rev != last.Rev || latest.LastText != last.LastText || !latest.UpdatedAt.Equal(last.UpdatedAt)) &&
			s.takeTaskAnswerRetry(machine, sid) {
			s.afterSettle(context.WithoutCancel(ctx), s.taskTurnSettled, machine, sid)
		}
		return settleLater
	}
	if quiet != "" {
		// The verdict is the turn's that was read: the row must not have moved while its transcript was (the row was
		// taken before the read). Moved, it is looked at again, the row and the transcript read together.
		if now, ok := s.session(cctx, last.MachineID, last.SessionID); !ok || !sameTurnRow(now, last) {
			s.releaseReceipt(ctx, t.ID, machine, sid, reserved)
			if !ok && quiet == proto.TurnErrorNoOutput {
				// A vanished recipient still owes its recorded source a failure notice.
				s.taskInboxTurnEnded(cctx, t, last, items, true)
			}
			return settleLater
		}
		s.forgetStop(last.MachineID, last.SessionID, stop)
		// The ledger keeps the verdict with the turn, for a manager started again (silent_turns.go).
		if e, again := s.takeTurnAs(cctx, t, last, items, end, named, since, turnInterrupted, quiet, ""); again {
			// This very turn was taken up before: no second receipt. Its verdict is on the row all the same, where the
			// ledger has it (just put there for a turn taken without it) and the row does not show it yet.
			if m, ok := s.quietMarks(last.MachineID)[last.SessionID]; e.Silent != "" && (!ok || !m.holds(last)) {
				s.markQuietTurn(last, e.Silent)
			}
			// An external event's no-output turn still has to be reconciled with its retry ledger (#10050);
			// a turn Persona's Stop interrupted neither acknowledges nor retries its event.
			if e.Silent == proto.TurnErrorNoOutput {
				s.taskInboxTurnEnded(cctx, t, last, items, true)
			}
			return settleTaken
		}
		// The Agent's row says so too, not "done" (#10187).
		s.markQuietTurn(last, quiet)
		l := s.lang(cctx)
		if quiet == proto.TurnErrorStopped {
			// The owner stopped the conversation that handed the task out, and Persona's Stop interrupted the Agent before
			// it had any output (stopReporting): stopped, not failed. A receipt that says so, with "Continue" on its card;
			// no notice or report, which that conversation does not get until the owner talks in it again.
			s.logf("task %s: stopped with its conversation before the turn had any output", t.ID)
			body := receiptBody{Title: t.Title, State: "interrupted", Summary: i18n.T(l, "assistant-tasks.stopped"), Machine: name,
				Error: quietTurnText(l, quiet), Cause: causeStopped}
			s.appendInterrupted(cctx, t, SessionRef{MachineID: last.MachineID, SessionID: last.SessionID}, body, true)
			return settleTaken
		}
		// No reply and no tool call, yet no error either (the model service answered 200 with nothing in it): no end
		// of the work. Interrupted, as a cut-off turn is, with nothing to judge or close (#2349).
		s.logf("task %s: interrupted: the turn had no output", t.ID)
		where := [2]string{last.MachineID, last.SessionID}
		body := receiptBody{Title: t.Title, State: "interrupted", Summary: i18n.T(l, "assistant-tasks.interrupted"), Machine: name,
			Error: silentTurnErrorIn(l)}
		s.appendInterrupted(cctx, t, SessionRef{MachineID: last.MachineID, SessionID: last.SessionID}, body, true)
		if s.closedWhileRead(t.ID) {
			return settleTaken
		}
		if s.taskInboxTurnEnded(cctx, t, last, items, true) {
			return settleTaken
		}
		s.taskNotice(cctx, t, name, "interrupted", "", body.Error)
		s.taskReport(t, reportInterrupted, name, where, body.Error, nil)
		return settleTaken
	}
	if read {
		s.taskInboxTurnEnded(cctx, t, last, items, false)
	}
	// The whole last reply, as Markdown: the session list's last_text is one line cut at 200 characters.
	_, answer := turnExchange(items)
	text := answer
	if text == "" && !isUnlinkedAgyPane(last, items) {
		text = strings.TrimSpace(last.LastText)
	}
	// A paged reply may have lost its tail (the file lines and the closeout are there): read the whole item.
	text = s.taskAnswer(cctx, last.MachineID, last.SessionID, items, text)
	current, ok := s.todoNow(t.ID)
	if !ok || current.Cancelled() || current.AgentAborted(machine, sid) {
		s.logf("task %s: cancelled or agent %s/%s aborted: not issuing done receipt", t.ID, machine, sid)
		return settleRefused
	}
	fire := nativeLoopStart(items)
	if evidence := nativeLoopNoopEvidence(current, fire, text); current.DoneAt == nil && named && !stopped && evidence != "" {
		s.takeTurnAs(cctx, t, last, items, end, true, since, turnLoopNoop, "", evidence)
		s.signalNativeLoopNoop(cctx, t)
		return settleTaken
	}
	if read && text != "" && !stopped && current.Supervision.state(current) == reviewParked &&
		(last.Loop != nil || fire.NativeLoopID != "") {
		began := since
		if !fire.TS.IsZero() {
			began = fire.TS
		}
		s.finishNativeLoopPark(cctx, current, began)
	}
	s.forgetStop(last.MachineID, last.SessionID, stop) // a Stop that came after its output: this turn's
	// The files it handed over (task_files.go, #10511): copied to the computer of the conversation that hears of it.
	files := s.deliverTaskFiles(cctx, t, machine, s.conversationMachine(cctx, s.taskReceiver(cctx, t), ""), text)
	body := receiptBody{Title: t.Title, State: "done", Summary: clipMarkdown(text, 600), Machine: name, Files: files,
		MachineID: last.MachineID, SessionID: last.SessionID, Reroute: s.rerouteOfTask(cctx, t.ID, t.PlanID)}
	// Persona follows up on a supervised stop through sigReview or sigIdle after judgment; the receipt need not wait.
	// needs_user waits on the user instead. Read the latest todo in case it changed while reading the reply.
	if current, ok := s.todoNow(t.ID); ok && current.DoneAt == nil {
		if v := current.Supervision; v != nil && v.state(current) != reviewNeedsUser {
			state := "checking" // receipt stays ahead of the model; a stopped turn is not a completion verdict
			if v.state(current) == reviewPending {
				state = "awaiting"
			}
			body.Review = &receiptReview{State: state, Criteria: len(v.DoneWhen)}
		}
	}
	if current, ok := s.todoNow(t.ID); ok {
		if list := s.briefCriteriaNow(current); len(list) > briefCriteriaMost {
			body.Criteria, body.CriteriaMore = list[:briefCriteriaMost], len(list)-briefCriteriaMost
		} else {
			body.Criteria = list
		}
		body.PR = s.ownedReplyPR(cctx, current, last.MachineID, last.SessionID, text, taskReplyItem(items).TS)
	}
	if body.Summary == "" {
		body.Summary = i18n.T(s.lang(cctx), "assistant-tasks.done")
	}
	// One receipt, one judgment and one supervision signal for each turn the runtime closed, whatever tells of its end
	// and however often. The ledger says whether this turn was taken up before; its receipt has one id, so the feed
	// says it too where a manager stopped between the two (task_turns.go).
	turn, again := s.takeTurn(cctx, t, last, items, end, named, since, turnDone)
	if again && turn.State != turnDone {
		return settleTaken // it had an interrupted receipt
	}
	handledDecision := read && !stopped && answer != "" && s.decisionReplyTurnEnded(cctx, t, last, items, text)
	switch id := receiptID(t.ID, turn); {
	case again && turn.Receipt:
		// Taken up before it had a name, and its receipt went out then (under an id of its own): no second one.
	case id != "" && !s.isLeaderRef(t.Origin):
		// The feed's word outranks the ledger's where it has one: a receipt that is not there yet is written (a manager
		// stopped between the two). Where the feed cannot be asked or written, the ledger's word stands: a turn it has
		// is not handled a second time over a database error.
		if fresh, err := s.appendFeedOnce(cctx, id, t.Origin, feedReceipt, t.ID, body); err != nil {
			s.logf("task %s: receipt: %v", t.ID, err)
		} else {
			again = !fresh
		}
	default:
		if _, err := s.appendFeed(cctx, t.Origin, feedReceipt, t.ID, body); err != nil {
			s.logf("task %s: receipt: %v", t.ID, err)
		} else {
			s.turnReceipted(cctx, t.ID) // no name to find it by later: the ledger says it is out
		}
	}
	if s.closedWhileRead(t.ID) {
		s.turnJudged(cctx, t.ID, taskDoneCheck{}, nil, "done") // nothing to judge: the sweep does not ask again
		return settleTaken
	}
	if handledDecision {
		s.turnJudged(cctx, t.ID, taskDoneCheck{}, nil, "agent_decision_handled")
		return settleTaken
	}
	if again {
		s.turnAgain(cctx, t, turn, last, text, since, worked)
		return settleTaken
	}
	s.taskNoticeOf(cctx, t, name, "done", "", clipMarkdown(text, 1500), 0, files)
	if s.advanceTaskReplyAt(cctx, t, last.MachineID, last.SessionID, text, last.UpdatedAt) {
		go s.askAcceptance(t, last.MachineID, last.SessionID, text) // verified, by the supervising assistant or the user (acceptance.go)
	}
	s.taskReport(t, reportDone, name, [2]string{last.MachineID, last.SessionID}, text, nil)
	s.autoFinishTask(t, since, worked) // the work may be finished: then the todo is done (done.go)
	return settleTaken
}

// closedWhileRead: task id was closed (accepted, cancelled, closed by hand) while its agent's turn end was read. The
// receipt written for that end says how the turn ended, and that is all: no notice, stage, report, judgment or
// supervision signal for a task that is over (#7539).
func (s *Service) closedWhileRead(id string) bool {
	if t, ok := s.todoNow(id); ok && t.DoneAt == nil {
		return false
	}
	s.logf("task %s: closed while its agent's turn end was read: receipt only", id)
	return true
}

// releaseReceipt gives back the receipt taskTurnSettled reserved for task id at reserved, when its second look at
// the task's agents did not find them stopped: the node did not answer that time, or one works or waits again. The
// stretch keeps its claim to a receipt, and the turn end is looked at once more after receiptRetryAfter: without
// that, a link that dropped for a moment left the stretch with no receipt and no judgment for good, since nothing
// tells of the same turn end again. A look that finds an agent at work ends there; its own turn end follows.
func (s *Service) releaseReceipt(ctx context.Context, id, machine, sid string, reserved time.Time) {
	s.tasks.mu.Lock()
	mine := s.tasks.receipted[id].Equal(reserved)
	if mine {
		delete(s.tasks.receipted, id)
	}
	s.tasks.mu.Unlock()
	if mine {
		s.taskTurnEndedAfter(context.WithoutCancel(ctx), machine, sid, time.Now().Add(receiptRetryAfter))
	}
}

// taskTurnEndedAfter persists the look before arming its process-local timer. Recovery uses the same path after a
// manager restart; a storeless service retains the previous memory-only behavior.
func (s *Service) taskTurnEndedAfter(ctx context.Context, machine, sid string, at time.Time) {
	w := s.newReceiptRecheck(ctx, machine, sid, at)
	persisted := false
	if w.Receipt.TodoID != "" && s.store() != nil {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		selected, arm, err := s.persistReceiptRecheck(pctx, w)
		cancel()
		if err != nil {
			s.logf("persist receipt recheck %s/%s: %v", machine, sid, err)
		} else {
			if !arm {
				return
			}
			w, persisted = selected, true
		}
	}
	s.armReceiptRecheck(w, persisted)
}

// successorOpens: the owner's message on the card of closed task t (judged done or accepted, not cancelled) started
// started, a new agent for it, after its agents had all stopped, or may have (the start's answer is unknown; started is
// empty then). The owner asks the task for more: it is open again, and the new agent's work is the task's from now (it
// has no send on record for closedTurnReopens, and its first turn may have started before it was linked, while the
// task was still closed). A start that definitely failed (refused, or it never went out: rpc.ErrNotSent) asked for
// nothing, and the task stays as it was (#7539).
func (s *Service) successorOpens(ctx context.Context, t Todo, started proto.Session) {
	s.tasks.completionMu.Lock()
	defer s.tasks.completionMu.Unlock()
	if cur, ok := s.todoNow(t.ID); !ok || cur.DoneAt == nil || !sameInstant(*cur.DoneAt, *t.DoneAt) {
		return // opened already (its new agent's first turn), or moved since
	}
	if err := s.reopenTodo(ctx, t.ID); err != nil {
		s.logf("task %s: reopen for the owner's message: %v", t.ID, err)
		return
	}
	if started.SessionID == "" {
		s.logf("task %s: reopened: the owner's message may have started a new agent for it", t.ID)
		return
	}
	s.logf("task %s: reopened: the owner's message started a new agent for it", t.ID)
	if cur, ok := s.taskOf(ctx, started.MachineID, started.SessionID); ok && cur.ID == t.ID {
		s.taskWorks(ctx, cur, false)
	}
}

// routeToTask sends text straight to the agent of task todoID that most needs it, and keeps it in the conversation as
// the user's (no from: a task card's Message, or a decision the owner made in Lark).
func (s *Service) routeToTask(ctx context.Context, conversation, todoID, text string) (FeedItem, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return FeedItem{}, err
	}
	s.reviewDismissals.actions.acquire(todoID)
	var err error
	if t, ok := s.todoNow(todoID); ok && t.Origin == conversation && t.Supervision.state(t) == reviewUserCheck {
		err = s.reopenTodo(ctx, todoID)
	}
	// Only the round change is serialized: other messages must still join an Agent start in flight.
	s.reviewDismissals.actions.release(todoID)
	if err != nil {
		return FeedItem{}, err
	}
	return s.routeToTaskChecked(ctx, conversation, todoID, text, nil)
}

// routeToTaskChecked lets automatic decisions recheck their task after slow session/device lookups.
// Direct user messages retain routeToTask's ability to reuse a completed task's agent.
func (s *Service) routeToTaskChecked(ctx context.Context, conversation, todoID, text string, guard func(Todo) bool) (FeedItem, error) {
	return s.routeToTaskAs(ctx, conversation, todoID, text, "", guard)
}

// routedRelay marks a routed item the hub wrote for the owner (`relay`): an answer of theirs it put into words for the
// task's agent (a recommendation taken, an option chosen, a reply to a card), as opposed to what they typed to the
// task themselves. The apps draw both as the owner's; where a task came from reads only the latter (decision.go).
const routedRelay = "answer"

// agentUnavailableError marks a routing refusal that no Agent can receive. The wrapper keeps the localized route error;
// a lower-level send failure with the same text is not marked and stays retryable.
type agentUnavailableError struct{ error }

func (e *agentUnavailableError) Unwrap() error { return e.error }
func (e *agentUnavailableError) Text(l i18n.Lang) string {
	return i18n.Localize(e.error, l)
}

func agentUnavailable(key string) error {
	return &agentUnavailableError{error: i18n.Err(key)}
}

// noAgentCanTake reports whether nothing can receive a decision now: the router found no live Agent or computer, or the
// node explicitly refused a stopped Agent. Other send failures can be transient and must not take the decision.
func noAgentCanTake(err error) bool {
	var unavailable *agentUnavailableError
	return errors.As(err, &unavailable) || refusedWith(err, proto.CodeAgentStopped)
}

// relayToTask is routeToTaskChecked for what the hub words on the owner's behalf (routedRelay).
func (s *Service) relayToTask(ctx context.Context, conversation, todoID, text string, guard func(Todo) bool) (FeedItem, error) {
	return s.routeToTaskAs(ctx, conversation, todoID, text, routedRelay, guard)
}

// routeToTaskAs is routeToTaskChecked; relay marks the routed item it leaves ("": the owner's own words).
func (s *Service) routeToTaskAs(ctx context.Context, conversation, todoID, text, relay string, guard func(Todo) bool) (FeedItem, error) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return FeedItem{}, err
	}
	t, ok, err := s.todo(ctx, todoID)
	if err != nil {
		return FeedItem{}, err
	}
	if !ok || t.Origin != conversation {
		return FeedItem{}, i18n.Err("assistant-tasks.route_no_task")
	}
	agents := s.taskAgents(ctx, t)
	if len(agents) == 0 {
		// A failed session list is not an answer. Retry with reachability before deciding that the task genuinely has
		// no Agent, or a transient lookup could end a decision as if the Agent were gone.
		reachedAgents, reached := s.taskAgentsReached(ctx, t)
		if !reached {
			return FeedItem{}, i18n.Err("assistant-tasks.route_offline")
		}
		agents = reachedAgents
	}
	if len(agents) == 0 {
		return FeedItem{}, agentUnavailable("assistant-tasks.route_no_agent")
	}
	live, last := liveTaskAgents(agents)
	if len(live) == 0 { // every agent has stopped (read-only, #4754): the user's message starts a new one (tasks_successor.go)
		if guard != nil {
			cur, ok := s.todoNow(todoID)
			if !ok || !guard(cur) {
				return FeedItem{}, db.ErrTodoCompleted
			}
		}
		if relay != "" {
			return FeedItem{}, agentUnavailable("assistant-tasks.route_agents_stopped")
		}
		// One new agent per task at a time: a second message while it starts (the owner's next line, or the same one
		// sent again after the app stopped waiting) goes to the agent that start brings up, not to a second one.
		st, ok := s.claimTaskStart(todoID)
		if !ok {
			return s.routeAfterStart(ctx, conversation, todoID, text, guard, st)
		}
		// A start that ended between the look at the agents and the claim linked its agent: the text is that one's.
		if c, ok := s.newlyLinkedAgent(t, todoID); ok {
			s.endTaskStart(todoID, st, proto.Session{})
			return s.sendToTaskAgent(ctx, conversation, todoID, c, text, relay, guard)
		}
		if exitText(text) { // what it would close is closed: no new agent, and a closed task stays closed (#7539)
			s.endTaskStart(todoID, st, proto.Session{})
			return FeedItem{}, i18n.Err("assistant-tasks.route_agents_exited")
		}
		started, err := s.startTaskSuccessor(ctx, t, last, text)
		_, st.uncertain = errors.AsType[*uncertainTaskStart](err)
		if hubClosedTask(&t) && (err == nil || st.uncertain) { // a start that never went out asked for nothing
			s.successorOpens(ctx, t, started) // before a message that waited for the start goes to its agent
		}
		s.endTaskStart(todoID, st, started)
		if err != nil {
			return FeedItem{}, err
		}
		body := map[string]string{"text": text, "new_agent": started.SessionID, "new_agent_machine": started.MachineID}
		if started.SessionID != "" { // the run it reached (#10465); a start that named no session leaves it unknown
			body["machine_id"], body["session_id"] = started.MachineID, started.SessionID
		}
		it, err := s.appendFeed(ctx, conversation, feedRouted, todoID, body)
		if err != nil { // the new agent has the text as its first message
			return FeedItem{}, wordsSent{err}
		}
		return it, nil
	}
	return s.sendToTaskAgent(ctx, conversation, todoID, pickTaskAgent(live), text, relay, guard)
}

// routeAfterStart waits for task todoID's agent start st, then sends text to the new agent it started. Only a start
// known to have started none lets the message route again, which may start one. The wait goes
// on when the app stops waiting, as the start does, so the text is not lost; it is bounded by taskStartWait.
func (s *Service) routeAfterStart(ctx context.Context, conversation, todoID, text string, guard func(Todo) bool, st *taskStart) (FeedItem, error) {
	timer := time.NewTimer(taskStartWait())
	defer timer.Stop()
	select {
	case <-st.done:
	case <-timer.C:
		return FeedItem{}, i18n.Err("assistant-tasks.route_starting")
	}
	if st.uncertain {
		return FeedItem{}, i18n.Err("assistant-tasks.route_waited_unconfirmed")
	}
	s.tasks.mu.Lock()
	c := st.agent
	s.tasks.mu.Unlock()
	if c.SessionID == "" {
		return s.routeToTaskAs(context.WithoutCancel(ctx), conversation, todoID, text, "", guard)
	}
	s.logf("task %s: a message waited for its new agent %s/%s", todoID, c.MachineID, short(c.SessionID))
	return s.sendToTaskAgent(context.WithoutCancel(ctx), conversation, todoID, c, text, "", guard)
}

// newlyLinkedAgent is an agent linked to task todoID since t was read, still active: one a start that ended meanwhile
// brought up, which its machine may not list yet.
func (s *Service) newlyLinkedAgent(t Todo, todoID string) (proto.Session, bool) {
	cur, ok := s.todoNow(todoID)
	if !ok {
		return proto.Session{}, false
	}
	seen := map[SessionRef]bool{}
	for _, a := range t.Agents {
		seen[SessionRef{MachineID: a.MachineID, SessionID: a.SessionID}] = true
	}
	for _, a := range cur.Agents {
		if a.ReleasedAt == nil && a.DismissedAt == nil && a.StartError == "" && !seen[SessionRef{MachineID: a.MachineID, SessionID: a.SessionID}] {
			return proto.Session{MachineID: a.MachineID, SessionID: a.SessionID, Cwd: a.Cwd, Agent: a.Agent}, true
		}
	}
	return proto.Session{}, false
}

// sendToTaskAgent sends text to c, an agent of task todoID, and keeps it in conversation's feed (routeToTaskAs).
func (s *Service) sendToTaskAgent(ctx context.Context, conversation, todoID string, c proto.Session, text, relay string, guard func(Todo) bool) (FeedItem, error) {
	if _, ok := s.device(ctx, c.MachineID); !ok {
		return FeedItem{}, agentUnavailable("assistant-tasks.route_offline")
	}
	// An offline row a node before #4754 lists is resumed first: that can take minutes, and goes on when the phone
	// stops waiting, as a send from the chat does.
	wait := liveSendWait
	if c.Status == proto.StatusOffline {
		wait = 5 * time.Minute
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), wait)
	defer cancel()
	if guard != nil {
		cur, ok := s.todoNow(todoID)
		if !ok {
			return FeedItem{}, db.ErrTodoNotFound
		}
		if !guard(cur) {
			return FeedItem{}, db.ErrTodoCompleted
		}
	}
	// The owner's words to the task reopen it (#6726, #7539): this task only, should its agent have gone to another one
	// since its agents were read (routeToTaskAs).
	note, err := s.noteAgentSend(sctx, conversation, c.MachineID, c.SessionID, todoID, text)
	if err != nil { // not sent: it would not reopen the task after a manager restart
		s.logf("task %s: card message: %v", todoID, err)
		return FeedItem{}, i18n.Err("assistant-tasks.route_not_kept")
	}
	err = s.call(sctx, c.MachineID, DeviceSessionSend, map[string]any{"session_id": c.SessionID, "text": text}, nil)
	note.sent(sctx, err)
	if err != nil {
		return FeedItem{}, s.dialogRefusal(ctx, c, err) // a dialog_open refusal names the agent and the dialog (#7343)
	}
	// The run it went to (#10465), so a client can say which run the message reached.
	body := map[string]string{"text": text, "machine_id": c.MachineID, "session_id": c.SessionID}
	if relay != "" {
		body["relay"] = relay
	}
	it, err := s.appendFeed(sctx, conversation, feedRouted, todoID, body)
	if err != nil { // its agent has the text
		return FeedItem{}, wordsSent{err}
	}
	return it, nil
}
