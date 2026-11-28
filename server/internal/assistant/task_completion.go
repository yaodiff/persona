package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

var (
	loopInspectionRequest = lazyre.New(`^/loop(?:\s|$)`)
)

// A runtime's user role also carries peer messages and automated injections. Those
// exchanges cannot hand over this task merely because their answer sounds final.
// Persona and the recorded task supervisor may legitimately advance the task.
// Native background completion and own-task continuation are not side conversations.
func taskTurnSource(t Todo, items []proto.Item) (from, source string) {
	for _, it := range items {
		if it.Kind != proto.KindText && it.Kind != proto.KindNotice {
			continue
		}
		if it.Role != "user" && !(it.Role == "system" && it.Kind == proto.KindNotice) {
			continue
		}
		if f := strings.TrimSpace(it.From); f != "" {
			from = f
			if f != "Persona" && (t.Supervision == nil || f != t.Supervision.Supervisor) {
				return f, "peer"
			}
		}
		if source := prompts.ItemSource(it); source != nil && source.Kind == proto.SourceConversation {
			return source.ConversationID, "side-conversation"
		} else if source != nil && source.Kind == proto.SourceMeeting {
			return from, "meeting" // other people's words: the turn is never the task's (meeting_turns.go)
		}
		text := strings.TrimSpace(it.Text)
		if it.Role == "user" && it.Kind == proto.KindText && loopInspectionRequest.MatchString(text) {
			return from, "inspection"
		}
		if prompts.IsSideConversation(text) {
			return from, "side-conversation"
		}
		if strings.Contains(text, "<cross-session-message") || strings.Contains(text, "<teammate-message") {
			return from, "peer"
		}
		injected := it.Injected
		if injected == nil {
			injected = prompts.ParseInjected(text)
		}
		if injected != nil {
			// These resume this session's work, rather than introduce another task.
			// Continue scanning: a queued peer message still makes the turn a side exchange.
			if injected.Kind == proto.InjectedFollowup && prompts.ContinuesOwnWork(injected) {
				continue
			}
			return from, "injected"
		}
	}
	return from, ""
}

// A side turn can still submit the task's actual completion report. Require a
// successful task-scoped tool call in this turn, the corresponding live progress
// record, and evidence naming this task or one of its current acceptance criteria.
// Missing timestamps or evidence fail closed; an older done report is not a handoff.
func freshTaskDoneReport(t Todo, items []proto.Item, since time.Time) (text string, evidence []string, ok bool) {
	if t.Supervision == nil || t.Supervision.Progress == nil {
		return "", nil, false
	}
	v, p := t.Supervision, t.Supervision.Progress
	turnKnown := !since.IsZero()
	if t.CreatedAt.After(since) {
		since = t.CreatedAt
	}
	if t.ReopenedAt != nil && t.ReopenedAt.After(since) {
		since = *t.ReopenedAt
	}
	if v.UpdatedAt.After(since) {
		since = v.UpdatedAt
	}
	for _, it := range items {
		if (it.Role == "user" || it.Wake || it.Task != nil) && (it.Kind == proto.KindText || it.Kind == proto.KindNotice) && !it.TS.IsZero() {
			turnKnown = true
			if it.TS.After(since) {
				since = it.TS
			}
		}
	}
	if !turnKnown || p.Kind != upReportDone || p.At.IsZero() || p.At.Before(since) {
		return "", nil, false
	}
	for i, it := range items {
		if it.Kind != proto.KindToolUse || it.Role != "assistant" || it.ToolUseID == "" || it.TS.IsZero() || it.TS.Before(since) {
			continue
		}
		if it.ToolName != "report_to_supervisor" && !slices.Contains(compat.MCPToolNames([]string{"report_to_supervisor"}), it.ToolName) {
			continue
		}
		var report struct {
			Kind     string   `json:"kind"`
			Text     string   `json:"text"`
			Evidence []string `json:"evidence"`
		}
		if json.Unmarshal(it.ToolInput, &report) != nil || report.Kind != upReportDone || strings.TrimSpace(report.Text) == "" ||
			p.Text != clip(oneLine(clip(strings.TrimSpace(report.Text), 2000)), 200) {
			continue
		}
		related := false
		for _, e := range report.Evidence {
			for _, criterion := range append([]string{t.Title, t.Notes}, v.DoneWhen...) {
				if c := strings.TrimSpace(criterion); c != "" && strings.Contains(e, c) {
					related = true
				}
			}
		}
		if !related {
			continue
		}
		for _, result := range items[i+1:] {
			if result.Kind == proto.KindToolResult && result.ToolUseID == it.ToolUseID {
				if !result.IsError {
					return report.Text, report.Evidence, true
				}
				break
			}
		}
	}
	return "", nil, false
}

// Every linked agent must be observed. A missing session or disconnected node is
// not evidence that its work ended; raw runtime status also outranks a stale UI state.
// A link another agent took over from (supersededAgents) is no longer the task's agent and is not waited for.
func taskAgentsSettled(t Todo, agents []proto.Session) bool {
	gone := supersededAgents(t, agents)
	expected := 0
	for _, a := range t.Agents {
		if a.ReleasedAt == nil && a.DismissedAt == nil && !gone[[2]string{a.MachineID, a.SessionID}] {
			expected++
		}
	}
	agents = currentTaskAgents(agents, gone)
	if expected == 0 || len(agents) != expected {
		return false
	}
	for _, c := range agents {
		if !taskAgentSettledIn(t, agents, c) {
			return false
		}
	}
	return true
}

// taskAgentSettledIn is taskAgentSettled for c, one of task t's current agents (agents, without the superseded ones):
// an agent that reported t done in this round also settles with background commands started before that report that
// only wait, were moved from the foreground or stalled (#16418). A report is tied to the machine and session that sent
// it, so another agent linked to t cannot inherit it.
func taskAgentSettledIn(t Todo, agents []proto.Session, c proto.Session) bool {
	reportAt, ok := taskReportedDone(t, c)
	wait := len(agents) == 1 && t.Phase == assistantwire.TaskPhaseWaiting && t.WaitState != nil &&
		t.WaitState.WaitingOn.Valid() && (c.BackgroundWorking || c.State != proto.StateWorking)
	return taskAgentSettledReported(c, ok && len(agents) > 0, reportAt, wait)
}

// taskAgentSettled: c's turn is over and nothing of it goes on: no question or card waits, no background work runs
// (stalled background commands alone do not count, #16418), and no "Continue" is due by itself.
func taskAgentSettled(c proto.Session) bool {
	return taskAgentSettledReported(c, false, time.Time{}, false)
}

// taskAgentSettledReported is taskAgentSettled; reportedDone/reportAt: c reported its task done in this round at
// reportAt, so background commands left behind from before that report that only wait, were moved from the foreground
// or stalled do not hold it (backgroundLeftOver). A turn that stopped on a person also settles with its background
// work left running (#14899). A structured wait on a single-worker task serves the same purpose.
func taskAgentSettledReported(c proto.Session, reportedDone bool, reportAt time.Time, reportedWait bool) bool {
	if c.Status != proto.StatusDone && c.Status != proto.StatusIdle || c.State == proto.StateNeedsYou || c.PendingInteractions > 0 {
		return false
	}
	if (c.State == proto.StateWorking || backgroundRunning(c)) &&
		!backgroundLeftOver(c, reportedDone, reportAt) && !reportedWait && !stoppedOnPerson(c) {
		return false
	}
	w := c.Work
	return w == nil || w.Phase != proto.WorkBlocked || w.On != proto.WaitTransient || w.WakeAt == nil
}

// stoppedOnPerson: c's current turn, as its work state has it, ended with a closeout that says it stopped on a person.
// Background work it left running then does not keep that turn from settling: its end is no stretch still going on but
// a wait its task's supervisor or owner must hear of (#14899).
func stoppedOnPerson(c proto.Session) bool {
	w := c.Work
	return w != nil && w.Closeout != nil && !w.TurnAt.IsZero() && sameTurn(c, w.TurnAt) && closeoutWaitsOnPerson(*w.Closeout)
}

// withPersonWait is agents with the work state the triage put for an agent's current turn when that turn, left with
// background work, stopped on a person (stoppedOnPerson) and its listed row does not carry that state yet: the lists
// take the triage's state only with the next publish (publishSoon), and the triage settles such a turn at once
// (triageTurn, #14899). Every other row stays as listed.
func (s *Service) withPersonWait(agents []proto.Session) []proto.Session {
	for i, c := range agents {
		if !backgroundRunning(c) || stoppedOnPerson(c) {
			continue
		}
		if e, ok := s.workEntry([2]string{c.MachineID, c.SessionID}); ok && sameTurn(c, e.TurnAt) {
			w := e.Work
			if w.TurnAt.IsZero() {
				w.TurnAt = e.TurnAt
			}
			if c.Work = &w; stoppedOnPerson(c) {
				agents[i] = c
			}
		}
	}
	return agents
}

// backgroundLeftOver (#16418): c's turn is over and all it still has running are background commands nothing waits
// for: the node found them stalled (backgroundStalledShellOnly), or, after a done report (reportedDone at reportAt),
// each started no later than reportAt and only waits, was moved from the foreground by the timeout, or stalled. Never
// while its own turn is working (state working without background_working), nor with a subagent, a workflow or a Monitor.
func backgroundLeftOver(c proto.Session, reportedDone bool, reportAt time.Time) bool {
	if c.State == proto.StateWorking && !c.BackgroundWorking {
		return false
	}
	if backgroundStalledShellOnly(c) {
		return true
	}
	if !reportedDone || len(c.Background) == 0 || backgroundIsWork(c) {
		return false
	}
	return !slices.ContainsFunc(c.Background, func(b proto.BackgroundTask) bool {
		if b.Kind != "shell" {
			return true
		}
		if !reportAt.IsZero() && !b.StartedAt.IsZero() && b.StartedAt.After(reportAt) {
			return true
		}
		return !b.MovedFromForeground && !c.BackgroundWaiting && c.BackgroundStalledSince == 0
	})
}

// taskReportedDone: agent c reported task t done (report_to_supervisor kind done) in the round under way, after the
// supervision last changed (a review, a rework, the task opened again), as freshTaskDoneReport counts a round.
func taskReportedDone(t Todo, c proto.Session) (time.Time, bool) {
	v := t.Supervision
	if t.Phase == assistantwire.TaskPhaseWaiting && t.WaitState != nil {
		return time.Time{}, false
	}
	if v == nil || v.Progress == nil || v.Progress.Kind != upReportDone || v.Progress.At.IsZero() || v.Progress.At.Before(v.UpdatedAt) {
		return time.Time{}, false
	}
	if v.Progress.machine == "" || v.Progress.session == "" || v.Progress.machine != c.MachineID || v.Progress.session != c.SessionID {
		return time.Time{}, false
	}
	if t.ReopenedAt != nil && v.Progress.At.Before(*t.ReopenedAt) {
		return time.Time{}, false
	}
	return v.Progress.At, true
}

// supersededAgents are the links of t another agent took over from (#6483): an unreleased link whose session is
// offline or no longer listed in agents, while a link made after it is listed and not offline. A takeover that did not
// release the earlier link (made before takeovers released it, or by a path that does not) would otherwise keep the
// task from ever settling, so the current agent's stop had no receipt and no judgment, and keep raising stalled from
// the old agent's last update. A lone offline agent, with no later one, is no takeover: it stays the task's agent, and
// the orphan and stall signals speak of it as before. Keyed by machine and session.
func supersededAgents(t Todo, agents []proto.Session) map[[2]string]bool {
	listed := make(map[[2]string]proto.Session, len(agents))
	for _, c := range agents {
		listed[[2]string{c.MachineID, c.SessionID}] = c
	}
	live := func(a TodoAgent) bool {
		c, ok := listed[[2]string{a.MachineID, a.SessionID}]
		return ok && c.Status != proto.StatusOffline && c.State != proto.StateOffline
	}
	var latest time.Time // when the newest link with a live session was made
	for _, a := range t.Agents {
		if a.ReleasedAt == nil && a.DismissedAt == nil && live(a) && a.LinkedAt.After(latest) {
			latest = a.LinkedAt
		}
	}
	var gone map[[2]string]bool
	for _, a := range t.Agents {
		if a.ReleasedAt != nil || a.DismissedAt != nil || live(a) || !a.LinkedAt.Before(latest) {
			continue
		}
		if gone == nil {
			gone = map[[2]string]bool{}
		}
		gone[[2]string{a.MachineID, a.SessionID}] = true
	}
	return gone
}

// currentTaskAgents is agents without the superseded ones in gone (supersededAgents).
func currentTaskAgents(agents []proto.Session, gone map[[2]string]bool) []proto.Session {
	if len(gone) == 0 {
		return agents
	}
	out := make([]proto.Session, 0, len(agents))
	for _, c := range agents {
		if !gone[[2]string{c.MachineID, c.SessionID}] {
			out = append(out, c)
		}
	}
	return out
}

// A status tick can briefly say idle between tool output and the next model
// response. Earlier assistant words cannot finish that still-open exchange.
func taskTurnAwaitingAnswer(items []proto.Item) bool {
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		switch it.Kind {
		case "turn_duration":
			return false
		case proto.KindText:
			return it.Role != "assistant"
		case proto.KindToolUse, proto.KindToolResult, proto.KindThinking:
			return true
		}
	}
	return false
}

// A final answer can arrive after an idle tick without another working tick.
// Retry only a receipt held for that answer, rather than notifying the turn twice.
func (s *Service) takeTaskAnswerRetry(machine, sid string) bool {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	ref := SessionRef{MachineID: machine, SessionID: sid}
	if !s.tasks.awaitingAnswer[ref] {
		return false
	}
	delete(s.tasks.awaitingAnswer, ref)
	return true
}

func (s *Service) taskWorkCurrent(id string, work time.Time) bool {
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	return !s.tasks.working[id].After(work)
}

// Model calls and transcript reads can outlive a turn. Check both the observed
// turn and fresh runtime state before applying their result, including review state.
func (s *Service) taskCompletionCurrent(ctx context.Context, t Todo, work time.Time) bool {
	current, ok := s.todoNow(t.ID)
	if !ok || current.DoneAt != nil || current.Title != t.Title || current.Notes != t.Notes || autoDoneSkip(current, work, true) != "" || !s.taskWorkCurrent(t.ID, work) {
		return false
	}
	if (current.Supervision == nil) != (t.Supervision == nil) {
		return false
	}
	if current.Supervision != nil && (!slices.Equal(current.Supervision.DoneWhen, t.Supervision.DoneWhen) || current.Supervision.Round != t.Supervision.Round) {
		return false
	}
	return s.taskRuntimeSettled(ctx, current) && s.taskWorkCurrent(t.ID, work)
}

func (s *Service) taskRuntimeSettled(ctx context.Context, t Todo) bool {
	agents, reached := s.taskAgentsReached(ctx, t)
	if reached {
		if c, missing := s.missingPendingTurnAgent(ctx, t, agents); missing {
			items, read := s.lastTurnItems(ctx, c.MachineID, c.SessionID)
			end, closed := turnEndOf(items)
			e, known := s.turnOf(ctx, t.ID)
			if read && closed && known && e.of(c, end) {
				agents = append(agents, c) // fresh transcript proof, never absence alone
			}
		}
	}
	return reached && taskAgentsSettled(t, s.rowsProven(agents))
}

// A turn that starts before this lock invalidates the judgment. One that starts
// afterwards reopens the automatically closed task through taskWorking.
func (s *Service) finishCurrentTask(ctx context.Context, id, reason string, work time.Time) (bool, error) {
	s.tasks.completionMu.Lock()
	defer s.tasks.completionMu.Unlock()
	if !s.taskWorkCurrent(id, work) {
		return false, nil
	}
	return s.autoDoneTodo(ctx, id, reason, work)
}
