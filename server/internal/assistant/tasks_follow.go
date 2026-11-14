package assistant

import (
	"context"
	"errors"
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
		for _, c := range l.list {
			if c.SessionID == a.SessionID {
				out = append(out, c)
				break
			}
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
		for _, c := range lists[a.MachineID] {
			if c.SessionID == a.SessionID {
				agents = append(agents, c)
				break
			}
		}
	}
	return agents, reached
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

// taskWorking: one of the task's agents started a turn. The agent of a task the hub closed meanwhile (judged done,
// accepted) works on it again — sent more by agent_send, which resumes a closed one, or by the user: the task is open
// again first, as a change asked for on its "Please verify" card reopens it (#3457), so this turn is followed
// (interrupted or done receipt, judgment) and the task shows in progress, not done (#5892).
func (s *Service) taskWorking(ctx context.Context, mid, sid string) {
	s.tasks.completionMu.Lock()
	defer s.tasks.completionMu.Unlock()
	t, ok := s.taskOf(ctx, mid, sid)
	if !ok {
		closed, found := s.closedTaskOf(ctx, mid, sid)
		if !found {
			return
		}
		if !s.closedTurnReopens(closed, mid, sid) {
			s.logf("task %s: accepted; its agent works again, but nothing was sent for the task: left closed", closed.ID)
			return
		}
		if err := s.reopenTodo(ctx, closed.ID); err != nil {
			s.logf("task %s: reopen: %v", closed.ID, err)
			return
		}
		if t, ok = s.taskOf(ctx, mid, sid); !ok {
			return
		}
		s.logf("task %s: reopened: its agent works again", t.ID)
	}
	s.taskWorks(ctx, t)
}

// taskWorks is what follows a turn's start on open task t: the stretch of work begins now, and what was judged or
// waited for before is moot. s.tasks.completionMu is held (taskWorking, or taskStartMissed for a start nobody heard).
func (s *Service) taskWorks(ctx context.Context, t Todo) {
	s.tasks.mu.Lock()
	s.tasks.working[t.ID] = time.Now()
	s.tasks.mu.Unlock()
	s.interruptsSoon()          // an interrupted receipt of it reads resumed now (interruptions.go)
	s.clearTaskCheck(ctx, t.ID) // it judged the work before this turn (task_check.go)
	s.setTaskStage(ctx, t.ID, "develop", "")
	s.taskPlanApproval()
	if t.Supervision != nil {
		s.supervisedWorking(ctx, t)
	}
}

// agentSent is one send to an agent for a conversation: which one and when (tasksState.sentBy).
type agentSent struct {
	conversation string
	at           time.Time
}

// noteAgentSend records that conversation self is sending agent mid/sid text now (agent_send, or the owner's message on
// a task's card, routeToTask), before the send goes: its turn may start before the call returns. undo takes the record
// back when the send failed, unless a later send replaced it.
func (s *Service) noteAgentSend(self, mid, sid string) (undo func()) {
	key, sent := [2]string{mid, sid}, agentSent{conversation: self, at: time.Now()}
	s.tasks.mu.Lock()
	s.tasks.sentBy[key] = sent
	s.tasks.mu.Unlock()
	return func() {
		s.tasks.mu.Lock()
		if s.tasks.sentBy[key] == sent {
			delete(s.tasks.sentBy, key)
		}
		s.tasks.mu.Unlock()
	}
}

// closedTurnReopens: a new turn of agent mid/sid opens closed task t again (taskWorking). One the hub's judge closed,
// yes: the judge may have been early, and more for it comes as a turn of its agent. So does one its supervisor
// accepted whose agent its plan started (task_plan): that session exists for the task, and more for it (the owner's
// follow-up, a start that failed and came up late) is the task's work. A task with no plan has an agent that was often
// running before it became the task's (agent_supervise, agent_send with a task, an agent that became a task by itself,
// a link by hand; agent_start's tasks have no plan either) and is kept on for other work: turns for its own timers,
// other conversations, messages from other sessions and the owner's words in its own session reopened the finished
// task each time and woke its supervision again (#6726). Such an accepted task reopens only when the task's
// conversation (or its supervisor) sent the agent more after the acceptance: agent_send, or the owner's message on the
// task's card. The owner can still reopen one by hand.
func (s *Service) closedTurnReopens(t Todo, mid, sid string) bool {
	if t.DoneBy != db.DoneByReview || t.PlanID != "" {
		return true
	}
	s.tasks.mu.Lock()
	sent, ok := s.tasks.sentBy[[2]string{mid, sid}]
	s.tasks.mu.Unlock()
	if !ok || t.DoneAt == nil || !sent.at.After(*t.DoneAt) {
		return false
	}
	return sent.conversation == t.Origin || t.Supervision != nil && sent.conversation == t.Supervision.Supervisor
}

type receiptBody struct {
	Title   string         `json:"title"`
	State   string         `json:"state"` // done, failed, interrupted
	Summary string         `json:"summary,omitempty"`
	PR      string         `json:"pr,omitempty"`
	Machine string         `json:"machine,omitempty"`
	Review  *receiptReview `json:"review,omitempty"`
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

// silentTurnErrorIn is the interrupted receipt's detail for a turn with no output at all (silentTurn, #2349), in l.
// Not kept in a package variable: that would read the catalogs whenever personad starts, a hook too (#1308).
func silentTurnErrorIn(l i18n.Lang) string { return i18n.T(l, "assistant-tasks.silent_turn") }

var prLink = lazyre.New(`https://github\.com/[\w.-]+/[\w.-]+/pull/\d+|https://[^/\s<>"()]+/(?:[^/\s<>"()]+/)+merge_requests/\d+\b`)

// taskTurnEnded: one of the task's agents (sid on machine) ended a turn. Once none of them works or waits for the
// user any more, the conversation gets a receipt (once per stretch of work), and the todo is closed if the judge
// finds the work finished.
func (s *Service) taskTurnEnded(ctx context.Context, machine, sid string) {
	time.Sleep(s.settleDelay(ctx)) // a turn that ends to ask, or goes on at once, is not the end
	s.taskTurnSettled(ctx, machine, sid)
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
	if s.meetingTurnNow(machine, sid) {
		return settleRefused // a meeting's batch: never the task's progress, report or completion (meeting_turns.go)
	}
	t, ok := s.taskOf(ctx, machine, sid)
	if !ok {
		// An agent Persona started that is on no todo becomes a task once a turn of it did work (#3617, agent_tasks.go).
		if t, ok = s.agentTaskAtTurnEnd(ctx, machine, sid); !ok {
			return settleRefused
		}
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
	if !reached || !taskAgentsSettled(t, agents) {
		return settleLater
	}
	agents = currentTaskAgents(agents, supersededAgents(t, agents)) // not an agent it was handed over from (#6483)
	var last, cut proto.Session
	for _, c := range agents {
		if c.State == proto.StateWorking || c.State == proto.StateNeedsYou || c.PendingInteractions > 0 {
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
		if text != "" && (cut.SessionID == "" || c.UpdatedAt.After(cut.UpdatedAt)) {
			cut = c
			cut.TurnError, cut.TurnErrorKind = text, kind
		}
	}
	var items []proto.Item
	read := false
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
		if _, closed := turnEndOf(items); !closed {
			return settleLater // it went on since the sweep read it
		}
		if cut.SessionID == "" && taskTurnAwaitingAnswer(items) && !silentTurn(items) {
			return settleHeld // stopped between its tools and its answer: held as below, with no start taken for it
		}
		if !heard {
			s.logf("task %s: its agent %s/%s ended a turn with no start heard: taken up", t.ID, last.MachineID, short(last.SessionID))
			if !s.taskStartMissed(ctx, t.ID) {
				return settleRefused
			}
		}
	}
	s.tasks.mu.Lock()
	since, worked := s.tasks.working[t.ID]
	if !worked {
		since = t.CreatedAt // a hub restarted since it began: count from the start
	}
	if r, ok := s.tasks.receipted[t.ID]; ok && !r.Before(since) {
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
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	name := machine
	if m, ok := s.device(cctx, machine); ok {
		name = m.Name
	}
	if !s.taskWorkCurrent(t.ID, since) || !s.taskRuntimeSettled(cctx, t) {
		s.releaseReceipt(ctx, t.ID, machine, sid, reserved)
		return settleLater
	}
	if unheard == "" {
		// The turn as its transcript has it: read before any receipt, since its end names the turn (task_turns.go).
		items, read = s.lastTurnItems(cctx, last.MachineID, last.SessionID)
		if !s.taskWorkCurrent(t.ID, since) || !s.taskRuntimeSettled(cctx, t) {
			s.releaseReceipt(ctx, t.ID, machine, sid, reserved)
			return settleLater
		}
	}
	end, named := turnEndOf(items)
	if cut.SessionID != "" {
		if _, again := s.takeTurn(cctx, t, last, items, end, named, since, turnInterrupted); again {
			return settleTaken // this very turn was taken up before: no second receipt
		}
		// A turn cut off by the model service is no end of the work: an interrupted receipt (with "Continue" on the card),
		// and nothing to judge or close (#1516).
		s.logf("task %s: interrupted: %s", t.ID, clip(oneLine(cut.TurnError), 200))
		where := [2]string{cut.MachineID, cut.SessionID}
		body := receiptBody{Title: t.Title, State: "interrupted", Summary: i18n.T(s.lang(cctx), "assistant-tasks.interrupted"), Machine: name,
			Error: clip(strings.TrimSpace(cut.TurnError), 300)}
		s.appendInterrupted(cctx, t, body, false)
		s.taskNotice(cctx, t, name, "interrupted", "", body.Error)
		s.taskReport(t, reportInterrupted, name, where, body.Error, nil)
		return settleTaken
	}
	if read && taskTurnAwaitingAnswer(items) && !silentTurn(items) {
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
	if read && silentTurn(items) {
		if _, again := s.takeTurn(cctx, t, last, items, end, named, since, turnInterrupted); again {
			return settleTaken // this very turn was taken up before: no second receipt
		}
		// No reply and no tool call, yet no error either (the model service answered 200 with nothing in it): no end
		// of the work. Interrupted, as a cut-off turn is, with nothing to judge or close (#2349).
		s.logf("task %s: interrupted: the turn had no output", t.ID)
		where := [2]string{last.MachineID, last.SessionID}
		l := s.lang(cctx)
		body := receiptBody{Title: t.Title, State: "interrupted", Summary: i18n.T(l, "assistant-tasks.interrupted"), Machine: name,
			Error: silentTurnErrorIn(l)}
		s.appendInterrupted(cctx, t, body, true)
		s.taskNotice(cctx, t, name, "interrupted", "", body.Error)
		s.taskReport(t, reportInterrupted, name, where, body.Error, nil)
		return settleTaken
	}
	// The whole last reply, as Markdown: the session list's last_text is one line cut at 200 characters.
	text := strings.TrimSpace(last.LastText)
	if _, answer := turnExchange(items); answer != "" {
		text = answer
	}
	if current, ok := s.todoNow(t.ID); !ok || current.Cancelled() || current.AgentAborted(machine, sid) {
		s.logf("task %s: cancelled or agent %s/%s aborted: not issuing done receipt", t.ID, machine, sid)
		return settleRefused
	}
	body := receiptBody{Title: t.Title, State: "done", Summary: clipMarkdown(text, 600), Machine: name,
		Reroute: s.rerouteOfTask(cctx, t.ID, t.PlanID)}
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
	}
	if body.Summary == "" {
		body.Summary = i18n.T(s.lang(cctx), "assistant-tasks.done")
	}
	if pr := prLink.FindAllString(text, -1); len(pr) > 0 {
		body.PR = pr[len(pr)-1]
	}
	// One receipt, one judgment and one supervision signal for each turn the runtime closed, whatever tells of its end
	// and however often. The ledger says whether this turn was taken up before; its receipt has one id, so the feed
	// says it too where a manager stopped between the two (task_turns.go).
	turn, again := s.takeTurn(cctx, t, last, items, end, named, since, turnDone)
	if again && turn.State != turnDone {
		return settleTaken // it had an interrupted receipt
	}
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
	if again {
		s.turnAgain(cctx, t, turn, last, text, since, worked)
		return settleTaken
	}
	s.taskNotice(cctx, t, name, "done", "", clipMarkdown(text, 1500))
	if s.advanceTaskReply(cctx, t, last.MachineID, last.SessionID, text) {
		go s.askAcceptance(t, last.MachineID, last.SessionID, text) // verified, by the supervising assistant or the user (acceptance.go)
	}
	s.taskReport(t, reportDone, name, [2]string{last.MachineID, last.SessionID}, text, nil)
	s.autoFinishTask(t, since, worked) // the work may be finished: then the todo is done (done.go)
	return settleTaken
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

// taskTurnEndedAfter has taskTurnEnded look at sid's task again at at. What happened meanwhile decides as ever: a turn
// that went on and ended had its own receipt, one that works again has none yet. A look already set for as early is
// enough (one still waiting then sets the next): the wait's wakes (a background task reporting, a /loop) end a turn
// each, and each comes here.
func (s *Service) taskTurnEndedAfter(ctx context.Context, machine, sid string, at time.Time) {
	ref := SessionRef{MachineID: machine, SessionID: sid}
	s.tasks.mu.Lock()
	if s.tasks.limitLooks == nil {
		s.tasks.limitLooks = map[SessionRef]time.Time{}
	}
	if pending, ok := s.tasks.limitLooks[ref]; ok && !pending.After(at) { // one as early is set: it looks again if need be
		s.tasks.mu.Unlock()
		return
	}
	s.tasks.limitLooks[ref] = at
	s.tasks.mu.Unlock()
	time.AfterFunc(time.Until(at), func() {
		s.tasks.mu.Lock()
		if s.tasks.limitLooks[ref].Equal(at) {
			delete(s.tasks.limitLooks, ref)
		}
		s.tasks.mu.Unlock()
		s.taskTurnEnded(ctx, machine, sid)
	})
}

// routeToTask sends text straight to the agent of task todoID that most needs it, and keeps it in the conversation as
// the user's (no from: a task card's Message, or a decision the owner made in Lark).
func (s *Service) routeToTask(ctx context.Context, conversation, todoID, text string) (FeedItem, error) {
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
		return FeedItem{}, i18n.Err("assistant-tasks.route_no_agent")
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
			return FeedItem{}, i18n.Err("assistant-tasks.route_agents_stopped")
		}
		started, err := s.startTaskSuccessor(ctx, t, last, text)
		if err != nil {
			return FeedItem{}, err
		}
		return s.appendFeed(ctx, conversation, feedRouted, todoID, map[string]string{"text": text,
			"new_agent": started.SessionID, "new_agent_machine": started.MachineID})
	}
	c := pickTaskAgent(live)
	if _, ok := s.device(ctx, c.MachineID); !ok {
		return FeedItem{}, i18n.Err("assistant-tasks.route_offline")
	}
	// An offline row a node before #4754 lists is resumed first: that can take minutes, and goes on when the phone
	// stops waiting, as a send from the chat does.
	wait := 90 * time.Second
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
	unsent := s.noteAgentSend(conversation, c.MachineID, c.SessionID) // the owner's words to the task reopen it (#6726)
	if err := s.call(sctx, c.MachineID, DeviceSessionSend, map[string]any{"session_id": c.SessionID, "text": text}, nil); err != nil {
		if !startResultUnknown(err) {
			unsent()
		}
		return FeedItem{}, err
	}
	body := map[string]string{"text": text}
	if relay != "" {
		body["relay"] = relay
	}
	return s.appendFeed(sctx, conversation, feedRouted, todoID, body)
}
