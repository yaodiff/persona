package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// What the leaders report up (docs/design/proposals/owner-at-a-glance.md "Leaders report up", #3485): for each task a
// leader supervises, the hub tells the extensions granted leaders.read (leader.report) when it
//
//   - reached its goal: its latest review accepted it;
//   - needs the owner's decision: its latest review says needs_user;
//   - needs the owner's decision on a plan: a plan the leader handed out, or the main conversation handed to it
//     (task_plan supervisor), waits for the owner's Start (#3608; told once per plan, with its id and no to-do);
//   - is stuck: by the To-dos SOP (todo_triage.go) it sits in Waiting on you, or stopped unfinished (PR to merge, Other
//     open, Shelved), and nothing of it moved for leaderStuckAfter.
//
// They are read off the ledger (reviews, the to-dos, their agents' sessions), never asked of a model, and progress is
// never reported. Each is told once: what was told is kept in the assistant's store (assistant.leader_reports), saved
// before the report is queued, so a restart or a replay of the same ledger tells nothing again. A review is told by its
// time; a stuck task at most once per leaderStuckAfter. Only tasks of a current leader are reported: once a direction
// ended, its tasks' signals are the main conversation's again, as before.

const leaderReportsKey = "assistant.leader_reports"

const (
	// leaderStuckAfter: a task that did not move this long is stuck; it is told so at most this often.
	leaderStuckAfter = 24 * time.Hour
	// leaderReportsEvery: how often the ledger is read for stuck tasks besides after each change.
	leaderReportsEvery = 10 * time.Minute
	// leaderReportsKeep: how long a told review is remembered (a review older than this is never told again either).
	leaderReportsKeep = 30 * 24 * time.Hour
)

// leaderReportBook is what was told: review reports by key (kind, to-do, review time), and each to-do's last stuck
// report. A key the book has is never told again.
type leaderReportBook struct {
	mu     sync.Mutex
	loaded bool
	told   leaderReportsTold
	run    sync.Mutex // one sync at a time
	// plansDue: a leader's plan was just made; the next sync reads the waiting plans even when it is not the loop's.
	plansDue atomic.Bool
}

type leaderReportsTold struct {
	Reviews map[string]time.Time `json:"reviews"`
	Stuck   map[string]time.Time `json:"stuck"`
	// Since: when reports started on this hub; a review from before it is history, not news.
	Since time.Time `json:"since"`
}

func (s *Service) leaderReportsLocked(ctx context.Context) bool {
	b := &s.lreports
	if b.loaded {
		return true
	}
	var told leaderReportsTold
	found := false
	if st := s.store(); st != nil {
		var err error
		if found, err = st.Setting(ctx, leaderReportsKey, &told); err != nil {
			s.logf("load leader reports: %v", err)
			return false
		}
	}
	fresh := !found || told.Since.IsZero()
	if fresh {
		told.Since = time.Now().UTC()
	}
	if told.Reviews == nil {
		told.Reviews = map[string]time.Time{}
	}
	if told.Stuck == nil {
		told.Stuck = map[string]time.Time{}
	}
	b.told, b.loaded = told, true
	if fresh { // kept at once: from now on is news, whatever happens before the first report
		if err := s.saveLeaderReportsLocked(ctx); err != nil {
			s.logf("save leader reports: %v", err)
		}
	}
	return true
}

func (s *Service) saveLeaderReportsLocked(ctx context.Context) error {
	st := s.store()
	if st == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return st.PutSetting(ctx, leaderReportsKey, s.lreports.told)
}

// leaderReportsLoop reads the ledger every leaderReportsEvery for tasks that got stuck meanwhile (the changes of the
// to-dos are read as they come: directionsSync runs it).
func (s *Service) leaderReportsLoop(ctx context.Context) {
	t := time.NewTicker(leaderReportsEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.leaderReportsSync(ctx, time.Now(), true)
			s.directionTreeSoon() // a task of the tree may be stuck now with nothing else changing (direction_tree.go)
		}
	}
}

// leaderReportsSync tells what the leaders' tasks reached or need since the ledger was last read, and with stuckToo
// which are stuck (that asks the agents' machines for their sessions: the loop's, not each change's).
func (s *Service) leaderReportsSync(ctx context.Context, now time.Time, stuckToo bool) {
	if !s.todoEventsOn() || !s.todosLoaded() {
		return
	}
	b := &s.lreports
	b.run.Lock()
	defer b.run.Unlock()
	b.mu.Lock()
	ok := s.leaderReportsLocked(ctx) // read (and started) before anything else, so Since is the first read's
	b.mu.Unlock()
	if !ok {
		return
	}
	cfg := s.settings(ctx)
	var leads []string
	for _, id := range cfg.Leaders {
		if id != "" && id != cfg.Main {
			leads = append(leads, id)
		}
	}
	if len(leads) == 0 {
		return
	}
	s.todos.mu.RLock()
	var tasks []Todo
	for _, t := range s.todos.todos {
		if t.Supervision != nil && slices.Contains(leads, t.Supervision.Supervisor) {
			tasks = append(tasks, copyTodoLocked(t))
		}
	}
	s.todos.mu.RUnlock()
	var plans []leaderPlan
	if stuckToo || s.lreports.plansDue.Swap(false) { // a plan waits rarely: the loop's, or right after task_plan made one
		plans = s.leaderPlans(ctx, cfg.Main, leads, now)
	}
	if len(tasks) == 0 && len(plans) == 0 {
		return
	}
	slices.SortFunc(tasks, func(a, b Todo) int { return a.CreatedAt.Compare(b.CreatedAt) })
	var asks map[string]todoAsk
	look, reached := triageLookup(nil), map[string]bool{}
	if stuckToo {
		asks = s.todoAsks(ctx)
		look, reached = s.leaderSessions(ctx, tasks)
	}
	l, loc := s.lang(ctx), s.location(ctx)
	names := s.leaderNames(ctx, leads) // before the book's lock: it reads the titles and the knowledge base

	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.leaderReportsLocked(ctx) {
		return
	}
	told := &b.told
	var out []assistantwire.LeaderReportEvent
	var stuckSigs []superviseSignal // the main conversation's one line for each (sigLeaderStuck)
	for _, t := range tasks {
		lead := t.Supervision.Supervisor
		if r := triageLatestReview(t.Supervision); r != nil && !r.At.Before(told.Since) && now.Sub(r.At) < leaderReportsKeep {
			kind := ""
			switch {
			case r.Verdict == reviewAccepted && t.Supervision.State == reviewAccepted:
				kind = assistantwire.LeaderReached
			case r.Verdict == reviewNeedsUser && t.Supervision.State == reviewNeedsUser && t.DoneAt == nil:
				kind = assistantwire.LeaderDecision
			}
			key := kind + "|" + t.ID + "|" + r.At.UTC().Format(time.RFC3339Nano)
			if _, done := told.Reviews[key]; kind != "" && !done {
				told.Reviews[key] = now
				out = append(out, leaderReport(key, lead, kind, t, leaderReportText(l, kind, t, r.Note, ""), r.At))
			}
		}
		if !reached[t.ID] {
			continue // an agent's machine could not be asked: it may be running, not stuck
		}
		lt := listedTodo{Todo: t}
		if a, ok := asks[t.ID]; ok {
			lt.Ask = &a
		}
		tr := triageTodo(lt, look, now, time.Time{}, loc, false) // a failed start is closed: never stuck
		stuck := taskStuck(t, tr, reached[t.ID], now)
		if last, ok := told.Stuck[t.ID]; stuck && (!ok || now.Sub(last) >= leaderStuckAfter) {
			told.Stuck[t.ID] = now
			key := "stuck|" + t.ID + "|" + now.UTC().Format(time.RFC3339Nano)
			text := leaderReportText(l, assistantwire.LeaderStuck, t, "", tr.Section)
			out = append(out, leaderReport(key, lead, assistantwire.LeaderStuck, t, text, now))
			stuckSigs = append(stuckSigs, superviseSignal{Todo: t.ID, Kind: sigLeaderStuck, Key: key,
				Detail: clip(names[lead]+" · "+oneLine(text), 200)})
		}
	}
	for _, lp := range plans {
		p := lp.plan
		key := "plan|" + p.ID
		if _, done := told.Reviews[key]; done || p.CreatedAt.Before(told.Since) {
			continue
		}
		told.Reviews[key] = now
		titles := make([]string, 0, len(p.Tasks))
		for _, t := range p.Tasks {
			titles = append(titles, clip(oneLine(t.Title), 60))
		}
		sum := sha256.Sum256([]byte(key))
		out = append(out, assistantwire.LeaderReportEvent{ID: "lrep_" + hex.EncodeToString(sum[:8]), Leader: lp.lead,
			Kind: assistantwire.LeaderDecision, Plan: p.ID, At: p.CreatedAt.UTC(), Text: clip(i18n.T(l, "assistant-reports.leader_plan_waiting",
				i18n.Args{"tasks": strings.Join(titles, i18n.T(l, "assistant-tasks.list_sep"))}), assistantwire.LeaderReportTextMost)})
	}
	// Forget what can no longer be told again: reviews past keeping, stuck marks of to-dos gone or no longer stuck long.
	for k, at := range told.Reviews {
		if now.Sub(at) > leaderReportsKeep {
			delete(told.Reviews, k)
		}
	}
	for id, at := range told.Stuck {
		if now.Sub(at) > leaderReportsKeep {
			delete(told.Stuck, id)
		}
	}
	if len(out) == 0 {
		return
	}
	if err := s.saveLeaderReportsLocked(ctx); err != nil {
		s.logf("save leader reports: %v (not told: they would be told again)", err)
		b.loaded = false // read the book again next time, as it is in the store
		return
	}
	for _, r := range out {
		r.Level = s.leaderReportLevel(ctx, r.Kind)
		if r.Plan != "" && r.Level == assistantwire.LevelP0 {
			r.Level = assistantwire.LevelP1 // a plan waiting for Start is P1, as its own push (tasks_plan.go)
		}
		s.todoQueue(todoNotice{assistantwire.EventLeaderReport, r})
	}
	if len(stuckSigs) > 0 {
		go func() { // not under the book's lock
			for _, g := range stuckSigs {
				s.signal(g)
			}
		}()
	}
}

// leaderPlan is a plan waiting for the owner's Start and the leader it is of.
type leaderPlan struct {
	plan *TaskPlan
	lead string
}

// leaderPlans are the plans of the last 7 days still waiting for the owner's Start that are a current leader's: made in
// a leader's own conversation, or in the main conversation with a leader as their tasks' supervisor (task_plan
// supervisor). Read from the plans themselves, not the feed (which pages from its oldest item). A conversation whose
// plans cannot be read is left out this time.
func (s *Service) leaderPlans(ctx context.Context, main string, leads []string, now time.Time) []leaderPlan {
	var out []leaderPlan
	for _, conv := range append(slices.Clone(leads), main) {
		if conv == "" {
			continue
		}
		for _, p := range s.openPlans(ctx, conv, now.Add(-7*24*time.Hour)) {
			lead := conv
			if conv == main {
				lead = p.Tasks[0].Supervisor
			}
			if slices.Contains(leads, lead) {
				out = append(out, leaderPlan{plan: p, lead: lead})
			}
		}
	}
	return out
}

// openPlans are conversation's plans made since then that still wait for Start and have tasks, oldest first.
func (s *Service) openPlans(ctx context.Context, conversation string, since time.Time) []*TaskPlan {
	var all []*TaskPlan
	if st := s.store(); st == nil {
		s.tasks.mu.Lock()
		for _, p := range s.tasks.plans {
			if p.Conversation == conversation {
				cp := *p
				cp.Tasks = slices.Clone(p.Tasks)
				all = append(all, &cp)
			}
		}
		s.tasks.mu.Unlock()
	} else {
		rows, err := st.TaskPlans(ctx, conversation)
		if err != nil {
			return nil
		}
		for _, r := range rows {
			all = append(all, planFromRow(r))
		}
	}
	var out []*TaskPlan
	for _, p := range all {
		if p.State == planOpen && len(p.Tasks) > 0 && p.CreatedAt.After(since) {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b *TaskPlan) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

// taskStuck is the one stuck rule (#3485, #4352): by the To-dos SOP it sits in Waiting on you, or stopped unfinished
// (PR to merge, Other open, Shelved), and nothing of it moved for leaderStuckAfter. reached: every machine its agents are
// on answered (one that did not may be running it). leader.report kind stuck and the direction tree's stuck both read it.
// A to-do no agent has worked on yet (notStarted) is stuck only in Waiting on you: elsewhere it has not started, it is
// not stopped (#5846).
func taskStuck(t Todo, tr todoTriage, reached bool, now time.Time) bool {
	if notStarted(t) && tr.Section != triageWaiting {
		return false
	}
	return reached && t.DoneAt == nil && !tr.Last.IsZero() && now.Sub(tr.Last) >= leaderStuckAfter &&
		slices.Contains([]string{triageWaiting, triagePR, triageOther, triageShelved}, tr.Section)
}

// notStarted: no agent has ever worked on to-do t (none linked, not even one let go since), and it is not a concern
// (#5846): a piece of a direction a leader recorded with todo_create and has not handed out, a Lark topic's to-do, a
// to-do the user made. The direction tree shows it as planned, and it is stuck only while it waits on the owner
// (taskStuck). A concern waits for the main conversation's answer (concerns.go) and keeps the rules it had.
func notStarted(t Todo) bool {
	return len(t.Agents) == 0 && !t.isConcern()
}

// leaderSessions finds the sessions of the tasks' agents as the apps get them, asking each machine once; reached says,
// by to-do id, whether every machine its agents are on answered.
func (s *Service) leaderSessions(ctx context.Context, tasks []Todo) (triageLookup, map[string]bool) {
	lists, reached := s.agentSessionLists(ctx, tasks)
	return func(machine, id string) (proto.Session, bool) {
		for _, c := range lists[machine] {
			if c.SessionID == id {
				return c, true
			}
		}
		return proto.Session{}, false
	}, reached
}

// agentSessionLists are the session lists, by machine, of the machines the tasks' active agents are on, each asked
// once; reached says, by to-do id, whether every machine its agents are on answered.
func (s *Service) agentSessionLists(ctx context.Context, tasks []Todo) (map[string][]proto.Session, map[string]bool) {
	lists := map[string][]proto.Session{}
	failed := map[string]bool{}
	reached := map[string]bool{}
	for _, t := range tasks {
		ok := true
		for _, a := range t.Agents {
			if a.ReleasedAt != nil || a.DismissedAt != nil {
				continue
			}
			if _, asked := lists[a.MachineID]; !asked && !failed[a.MachineID] {
				list, err := s.sessions(ctx, a.MachineID)
				if err != nil {
					failed[a.MachineID] = true
				} else {
					lists[a.MachineID] = list
				}
			}
			ok = ok && !failed[a.MachineID]
		}
		reached[t.ID] = ok
	}
	return lists, reached
}

// leaderReport is one report, named by its key.
func leaderReport(key, lead, kind string, t Todo, text string, at time.Time) assistantwire.LeaderReportEvent {
	sum := sha256.Sum256([]byte(key))
	return assistantwire.LeaderReportEvent{ID: "lrep_" + hex.EncodeToString(sum[:8]), Leader: lead, Kind: kind, Todo: t.ID,
		Text: clip(text, assistantwire.LeaderReportTextMost), Link: t.PRURL, At: at.UTC()}
}

// leaderReportText is a report's line in the owner's language: the task, and why (the review's note for a decision;
// where it is stuck).
func leaderReportText(l i18n.Lang, kind string, t Todo, note, section string) string {
	task := clip(oneLine(t.Title), 120)
	switch kind {
	case assistantwire.LeaderReached:
		return i18n.T(l, "assistant-reports.leader_reached", i18n.Args{"task": task})
	case assistantwire.LeaderDecision:
		if note = clip(oneLine(note), 160); note != "" {
			return i18n.T(l, "assistant-reports.leader_decision_why", i18n.Args{"task": task, "why": note})
		}
		return i18n.T(l, "assistant-reports.leader_decision", i18n.Args{"task": task})
	}
	if section == triageWaiting {
		return i18n.T(l, "assistant-reports.leader_stuck_waiting", i18n.Args{"task": task})
	}
	return i18n.T(l, "assistant-reports.leader_stuck_stopped", i18n.Args{"task": task})
}
