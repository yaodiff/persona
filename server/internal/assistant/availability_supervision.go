package assistant

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// What supervision costs per accepted task (#5086, step K1 of docs/design/proposals/main-chat-availability.md;
// docs/assistant/assistant.md "Supervision by model"): per model and effort the reviewing conversation ran on, how
// many tasks it accepted, how many of those were reopened within a day and how many rework rounds they took; and per
// day and supervisor conversation, the tasks it accepted next to the output tokens it generated. Work events like the
// rest of availability.go, no new table: one per task_review, and the round event's own model, effort and output
// count.
//
// The tokens are not attributed to tasks: nothing recorded says which task a supervisor's turn was spent on (a
// supervision message gathers several tasks' signals, the token counts are per conversation or per machine), so the
// read puts a conversation's day of output next to that day's acceptances and leaves the division to the reader.
const (
	// taskReviewedEvent: a conversation or a task leader's agent gave a task_review. Detail: todo, verdict (as
	// written: a rework past its rounds is needs_user), round (the task's rework rounds so far in all, as its ledger
	// counts them), rework_rounds (those since the task was last accepted: what this acceptance took), conversation
	// (the reviewer), role (the supervisor* constants), model and effort (what the reviewer ran on, supervisorRuns;
	// absent when not known). An acceptance is recorded once it closed the task: not of a task that was closed
	// already, nor when the close failed or closed nothing (a concern).
	taskReviewedEvent = "task_reviewed"
)

// supervisionReopenWithin: a reopen this soon after an acceptance counts against it, and an acceptance younger than
// this is not settled yet. The work stats' wrong_done uses the same day for the judge's closes.
const supervisionReopenWithin = wrongDoneWithin

// Which supervisor a reviewing conversation is.
const (
	supervisorMain       = "main"
	supervisorBackground = "background"
	supervisorLeader     = "leader"
	supervisorOther      = "conversation" // a side conversation reviewing what it handed out
	supervisorTaskLeader = "task_leader"  // a task leader's agent reviewing its workers (task_leader.go)
)

// supervisorRole is which supervisor conversation id is.
func (s *Service) supervisorRole(ctx context.Context, id string) string {
	switch {
	case s.isMain(ctx, id):
		return supervisorMain
	case s.isBackground(ctx, id):
		return supervisorBackground
	case s.isLeader(ctx, id):
		return supervisorLeader
	}
	return supervisorOther
}

// supervisorRuns is the model and effort conversation id (of role) runs on: what its node lists for it, else, for the
// main conversation and the background supervisor, what their purpose is set to. Either may be "" (the launcher's
// own default, which nothing here can name).
func (s *Service) supervisorRuns(ctx context.Context, id, role string) (model, effort string) {
	if c, ok := s.nodeSession(s.settings(ctx).MachineID, id); ok {
		model, effort = c.Model, c.Effort
	}
	if model != "" && effort != "" {
		return model, effort
	}
	purpose := ""
	switch role {
	case supervisorMain:
		purpose = "assistant"
	case supervisorBackground:
		purpose = purposeSupervise
	}
	if purpose != "" {
		if b, err := s.resolveModel(ctx, purpose); err == nil {
			model, effort = cmp.Or(model, b.Model), cmp.Or(effort, b.Effort)
		}
	}
	return model, effort
}

// reworkedSinceAccepted is how many times the task was sent back since it was last accepted (by anyone: a
// conversation, a task leader, the independent reviewer), the review just written counted when it is a rework and
// passed over when it is the acceptance being recorded. Out of the ledger's reviews, of which it keeps the last 20.
func (v Supervision) reworkedSinceAccepted() int {
	n := 0
	for i := len(v.Reviews) - 1; i >= 0; i-- {
		switch r := v.Reviews[i]; {
		case r.Verdict == reviewRework:
			n++
		case r.Verdict == reviewAccepted && i < len(v.Reviews)-1:
			return n
		}
	}
	return n
}

// taskReviewed records conversation self's review of task todo: verdict as written, v the task's ledger after it.
func (s *Service) taskReviewed(ctx context.Context, self, todo, verdict string, v Supervision) {
	role := s.supervisorRole(ctx, self)
	model, effort := s.supervisorRuns(ctx, self, role)
	s.reviewRecorded(todo, verdict, v, self, role, model, effort)
}

// leaderTaskReviewed records task leader agent lead's review of its worker task todo, by what the agent's node lists
// it running on.
func (s *Service) leaderTaskReviewed(lead TodoAgent, todo, verdict string, v Supervision) {
	var model, effort string
	if c, ok := s.nodeSession(lead.MachineID, lead.SessionID); ok {
		model, effort = c.Model, c.Effort
	}
	s.reviewRecorded(todo, verdict, v, lead.SessionID, supervisorTaskLeader, model, effort)
}

func (s *Service) reviewRecorded(todo, verdict string, v Supervision, reviewer, role, model, effort string) {
	detail := map[string]any{"todo": todo, "verdict": verdict, "round": v.Round, "rework_rounds": v.reworkedSinceAccepted(),
		"conversation": reviewer, "role": role}
	if model != "" {
		detail["model"] = model
	}
	if effort != "" {
		detail["effort"] = effort
	}
	s.workEvent("", "", taskReviewedEvent, detail)
}

// roundRan adds to a round event's detail what machine's conversation session ran on as the round ended, as its node
// lists it: model, effort, and output_tokens, the output it has generated so far in all (Session.OutputTokensValue; the
// read takes the difference between two rounds). Each is left out while not known.
func (s *Service) roundRan(machine, session string, detail map[string]any) map[string]any {
	c, ok := s.nodeSession(machine, session)
	if !ok {
		return detail
	}
	if c.Model != "" {
		detail["model"] = c.Model
	}
	if c.Effort != "" {
		detail["effort"] = c.Effort
	}
	if c.OutputTokensValue() > 0 {
		detail["output_tokens"] = int(c.OutputTokensValue())
	}
	return detail
}

// supervisionStats is the availability read's supervision.
type supervisionStats struct {
	// ByModel: per model and effort the reviewing conversation ran on, most acceptances first.
	ByModel []supervisorModel `json:"by_model"`
	// Days: per day (the owner's time zone) and conversation that reviewed a task in the window; newest day first.
	Days []supervisorDay `json:"days"`
}

type supervisorModel struct {
	// Model and Effort: what the reviewer ran on when it accepted; "" when not known (the launcher's own default).
	Model  string `json:"model"`
	Effort string `json:"effort"`
	// Accepted: the acceptances in the window: a task_review that accepted a task and closed it. A task accepted,
	// reopened and accepted again is two.
	Accepted int `json:"accepted"`
	// Settled: those of them made at least supervisionReopenWithin before the read, whose day to be reopened in is
	// over. Reopened: the settled ones whose task was reopened within supervisionReopenWithin of the acceptance,
	// whichever way. Reopened over Settled is the same rule for every row, however long ago its model was in use.
	Settled  int `json:"settled"`
	Reopened int `json:"reopened"`
	// ReworkRounds: the times the accepted tasks were sent back between their previous acceptance (or their start)
	// and this one, whoever sent them back; ReworkRoundsPerTask is that over Accepted.
	ReworkRounds        int     `json:"rework_rounds"`
	ReworkRoundsPerTask float64 `json:"rework_rounds_per_task"`
}

type supervisorDay struct {
	Day          string `json:"day"`
	Conversation string `json:"conversation"`
	// Role: main, background, leader or conversation (a side conversation reviewing what it handed out). A task
	// leader's agent has no day: its rounds are not an assistant conversation's, so there is no output to put here.
	Role string `json:"role"`
	// Model and Effort: what the conversation ran on at the day's last round, else at its last review; "" when not
	// known.
	Model  string `json:"model"`
	Effort string `json:"effort"`
	// Accepted: the tasks the conversation accepted that day.
	Accepted int `json:"accepted"`
	// OutputTokens: the output tokens the conversation generated in the rounds that ended that day, all of them: its
	// talk with the owner too, and no input or cached tokens. Not a cost of the day's accepted tasks alone.
	OutputTokens int `json:"output_tokens"`
}

// summarizeSupervision reads supervisionStats out of events (oldest first; those before since only give a
// conversation's output count to take the window's first difference from, and are otherwise not counted). now is the
// time of the read: an acceptance is settled supervisionReopenWithin before it.
func summarizeSupervision(since, now time.Time, events []db.WorkEvent, loc *time.Location) supervisionStats {
	out := supervisionStats{ByModel: []supervisorModel{}, Days: []supervisorDay{}}
	type accepted struct {
		at       time.Time
		key      [2]string // model, effort
		settled  bool
		reopened bool
	}
	models := map[[2]string]*supervisorModel{}
	byTodo := map[string][]*accepted{}
	days := map[[2]string]*supervisorDay{} // day, conversation
	roles := map[string]string{}           // the conversations that reviewed in the window → their role at the last review
	lastOut := map[[2]string]float64{}     // machine, session → the output count at its last round
	day := func(e db.WorkEvent, conversation string) *supervisorDay {
		k := [2]string{e.At.In(loc).Format(time.DateOnly), conversation}
		d := days[k]
		if d == nil {
			d = &supervisorDay{Day: k[0], Conversation: conversation}
			days[k] = d
		}
		return d
	}
	for _, e := range events {
		str := func(k string) string { v, _ := e.Detail[k].(string); return v }
		num := func(k string) (float64, bool) {
			switch v := e.Detail[k].(type) {
			case float64:
				return v, true
			case int: // as recorded, before the store's JSON
				return float64(v), true
			}
			return 0, false
		}
		switch e.Kind {
		case roundEvent:
			total, ok := num("output_tokens")
			k := [2]string{e.MachineID, e.SessionID}
			prev, seen := lastOut[k]
			if ok {
				lastOut[k] = total
			}
			if e.At.Before(since) {
				continue
			}
			d := day(e, e.SessionID)
			if m, f := str("model"), str("effort"); m != "" || f != "" {
				d.Model, d.Effort = m, f
			}
			switch {
			case !ok || !seen: // no count, or nothing to take the difference from: the first recorded round counts nothing
			case total >= prev:
				d.OutputTokens += int(total - prev)
			default: // a new transcript: it counts from zero again
				d.OutputTokens += int(total)
			}
		case taskReviewedEvent:
			if e.At.Before(since) {
				continue
			}
			isAccepted := str("verdict") == reviewAccepted
			if role := str("role"); role != supervisorTaskLeader { // an agent: by_model only
				conversation := str("conversation")
				roles[conversation] = role
				d := day(e, conversation)
				if d.Model == "" && d.Effort == "" {
					d.Model, d.Effort = str("model"), str("effort")
				}
				if isAccepted {
					d.Accepted++
				}
			}
			if !isAccepted {
				continue
			}
			k := [2]string{str("model"), str("effort")}
			m := models[k]
			if m == nil {
				m = &supervisorModel{Model: k[0], Effort: k[1]}
				models[k] = m
			}
			m.Accepted++
			rounds, _ := num("rework_rounds")
			m.ReworkRounds += int(rounds)
			a := &accepted{at: e.At, key: k, settled: !now.Before(e.At.Add(supervisionReopenWithin))}
			if a.settled {
				m.Settled++
			}
			byTodo[str("todo")] = append(byTodo[str("todo")], a)
		case todoReopenedEvent:
			// The acceptance it undoes: the task's latest one before it, once, when it is settled and the reopen came
			// within its day.
			list := byTodo[str("todo")]
			if n := len(list); n > 0 && list[n-1].settled && !list[n-1].reopened && !e.At.Before(list[n-1].at) &&
				e.At.Before(list[n-1].at.Add(supervisionReopenWithin)) {
				list[n-1].reopened = true
				models[list[n-1].key].Reopened++
			}
		}
	}
	for _, m := range models {
		m.ReworkRoundsPerTask = float64(m.ReworkRounds) / float64(m.Accepted)
		out.ByModel = append(out.ByModel, *m)
	}
	slices.SortFunc(out.ByModel, func(a, b supervisorModel) int {
		return cmp.Or(cmp.Compare(b.Accepted, a.Accepted), cmp.Compare(a.Model, b.Model), cmp.Compare(a.Effort, b.Effort))
	})
	for _, d := range days {
		if _, reviewed := roles[d.Conversation]; !reviewed || d.Accepted == 0 && d.OutputTokens == 0 {
			continue // not a supervisor in the window, or a day with nothing to put side by side
		}
		out.Days = append(out.Days, *d)
	}
	for i := range out.Days { // a day it only ran rounds on has the role of its reviews
		out.Days[i].Role = roles[out.Days[i].Conversation]
	}
	slices.SortFunc(out.Days, func(a, b supervisorDay) int {
		return cmp.Or(cmp.Compare(b.Day, a.Day), cmp.Compare(a.Conversation, b.Conversation))
	})
	return out
}
