package assistant

import (
	"cmp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// The To-dos SOP on the hub (docs/assistant/todo-status-sop.md, #3483): where each to-do shows, by the same decision
// table as the clients' copies (web/src/lib/todo-triage.ts, apple/Shared/TodoTriage.swift, Android's TodoTriage.kt).
// The hub needs the sections to tell when a leader's task is stuck (leader_reports.go). It is one more copy, not a
// second definition: it reads the to-dos and sessions as the apps get them (GET /api/v1/todos, the decorated session
// lists) and passes the clients' shared cases (spec/fixtures/triage, todo_triage_test.go).

// The SOP's sections, in the page's order: what the owner has to act on, the unfinished work, what just finished, then
// the folded ones, Done last (#5707).
const (
	triageWaiting  = "waiting"
	triageWorking  = "working"
	triageJustDone = "just_done"
	triagePR       = "pr"
	triageOther    = "other"
	triageShelved  = "shelved"
	triageDone     = "done"
)

var triageSections = []string{triageWaiting, triageWorking, triagePR, triageOther, triageJustDone, triageShelved, triageDone}

// The SOP's rules, by name.
const (
	ruleFailedStart    = "failed_start"
	ruleSuperseded     = "superseded"
	ruleAgentAsks      = "agent_asks"
	ruleOwnerDecides   = "owner_decides"
	ruleAgentRunning   = "agent_running"
	ruleClosed         = "closed"
	ruleAccepted       = "accepted"
	ruleDeployed       = "deployed"
	ruleFinishedUnseen = "finished_unseen"
	ruleSeen           = "seen"
	rulePROpen         = "pr_open"
	ruleStale          = "stale"
	ruleRest           = "rest"
)

const (
	// triageShelveAfter: no activity for this long, nothing of it running: shelved.
	triageShelveAfter = 48 * time.Hour
	// triageJustDoneFor: finished this recently, and not seen since, is Just finished; earlier, Done. Seeing it files it
	// at once (#5707); the time is the fallback.
	triageJustDoneFor = 24 * time.Hour
	// triageLimitGrace: how long past its reset a usage-limit wait still counts (limitResumeGrace, the web's GRACE_MS).
	triageLimitGrace = 10 * time.Minute
)

// todoTriage is where a to-do shows: its section, the rule that put it there, and the last moment anything of it moved
// (zero when nothing says).
type todoTriage struct {
	Section string
	Rule    string
	Last    time.Time
}

// triageLookup finds a session as the apps get it (its state worked out); false when the hub does not list it.
type triageLookup func(machine, session string) (proto.Session, bool)

// triageAgents are the agents the apps see on a to-do: dismissed ones are not sent (formatTodo).
func triageAgents(t Todo) []TodoAgent {
	out := make([]TodoAgent, 0, len(t.Agents))
	for _, a := range t.Agents {
		if a.DismissedAt == nil {
			out = append(out, a)
		}
	}
	return out
}

// triageStateOf is a session's state as the apps read it (the web's stateOf).
func triageStateOf(s proto.Session) string {
	switch {
	case s.State != "":
		return s.State
	case s.Status == proto.StatusOffline:
		return "offline"
	case s.Status == proto.StatusWorking:
		return proto.StateWorking
	case s.Status == proto.StatusBlocked || s.PendingInteractions > 0:
		return proto.StateNeedsYou
	}
	return proto.StateIdle
}

// triageStopped: stopped the way an interrupted turn is, unread or idle (the web's usage-limit.ts stopped).
func triageStopped(s proto.Session) bool {
	if s.State == proto.StateUnread || s.State == proto.StateIdle {
		return true
	}
	if s.State != "" {
		return false
	}
	return s.Status != proto.StatusWorking && s.Status != proto.StatusOffline && s.Status != proto.StatusBlocked && s.PendingInteractions == 0
}

// triageUsageLimit: stopped on a usage limit whose reset is ahead or not known (the web's usageLimitAgent).
func triageUsageLimit(s proto.Session, now time.Time) bool {
	if s.TurnError == "" || s.TurnErrorKind != proto.TurnErrorUsageLimit || !triageStopped(s) {
		return false
	}
	reset := s.TurnErrorResetsAt
	if reset == nil && s.Work != nil {
		reset = s.Work.ResetsAt
	}
	return reset == nil || !now.After(reset.Add(triageLimitGrace))
}

// triageInterrupted: cut off by a model error, not a usage limit (the web's interruptedAgent).
func triageInterrupted(s proto.Session, now time.Time) bool {
	if s.TurnError == "" || triageUsageLimit(s, now) {
		return false
	}
	st := triageStateOf(s)
	return st == proto.StateUnread || st == proto.StateIdle
}

// triageAgentWaits: its turn ended and something brings it back (the web's agentWaits, the hub's agentWaits with the
// apps' reading of the state).
func triageAgentWaits(s proto.Session) bool {
	st := triageStateOf(s)
	if st == proto.StateWorking {
		return s.BackgroundWorking
	}
	w := s.Work
	return st != proto.StateNeedsYou && st != proto.StateUnread && w != nil && w.Phase == proto.WorkBlocked &&
		(w.On == proto.WaitAgent || w.On == proto.WaitExternal || w.On == proto.WaitTransient)
}

// triageStatus is the to-do's status as the apps work it out (the web's todoStatus): failed, done, needs_you,
// interrupted, usage_limit, unread, working, waiting, follow_up, not_started.
func triageStatus(t Todo, look triageLookup, now time.Time) string {
	if t.StartError != "" {
		return "failed"
	}
	if t.DoneAt != nil {
		return "done"
	}
	var active []TodoAgent
	for _, a := range triageAgents(t) {
		if a.ReleasedAt == nil {
			active = append(active, a)
		}
	}
	if len(active) == 0 {
		return "not_started"
	}
	var interrupted, limited, unread, working, waiting bool
	for _, a := range active {
		if a.StartError != "" {
			continue // a failed start counts as stopped
		}
		var s proto.Session
		found := false
		if look != nil {
			s, found = look(a.MachineID, a.SessionID)
		}
		st := "offline"
		if found {
			st = triageStateOf(s)
		}
		if st == proto.StateNeedsYou {
			return "needs_you"
		}
		switch {
		case found && triageUsageLimit(s, now):
			limited = true
		case found && triageInterrupted(s, now):
			interrupted = true
		case st == proto.StateUnread:
			unread = true
		case found && triageAgentWaits(s):
			waiting = true
		case st == proto.StateWorking || found && s.Starting:
			working = true
		}
	}
	switch {
	case interrupted:
		return "interrupted"
	case limited:
		return "usage_limit"
	case unread:
		return "unread"
	case working:
		return "working"
	case waiting:
		return "waiting"
	}
	return "follow_up"
}

// triageLatestReview is the latest review, if any.
func triageLatestReview(v *Supervision) *TaskReview {
	if v == nil || len(v.Reviews) == 0 {
		return nil
	}
	return &v.Reviews[len(v.Reviews)-1]
}

// acceptanceStands: the latest review accepted it and none came since, even when the hub reopened it (the web's
// acceptanceStands, the SOP's "Accepted tasks").
func acceptanceStands(t Todo) bool {
	r := triageLatestReview(t.Supervision)
	return t.Supervision != nil && t.Supervision.State == reviewAccepted && r != nil && r.Verdict == reviewAccepted
}

// triageReviewState is the review state that holds now (the web's reviewState): a cancelled task reopened goes on
// again; an accepted one stays accepted while its acceptance stands.
func triageReviewState(t Todo) string {
	if t.Supervision == nil {
		return ""
	}
	st := t.Supervision.State
	if t.DoneAt == nil && (st == reviewAccepted && !acceptanceStands(t) || st == reviewCancelled) {
		return ""
	}
	return st
}

// triageOpenAsk is the ask the to-do waits on, when a whole one is there (the web's openAsk).
func triageOpenAsk(a *todoAsk) *todoAsk {
	if a == nil || a.Verdict == "" || a.AskedAt.IsZero() {
		return nil
	}
	return a
}

func later(a time.Time, b *time.Time) time.Time {
	if b != nil && b.After(a) {
		return *b
	}
	return a
}

// triageLastActivity is the last moment the to-do or any of its agents moved (the web's lastActivity); a restore from
// Shelved counts as one.
func triageLastActivity(t listedTodo, look triageLookup, restoredAt time.Time) time.Time {
	last := later(t.UpdatedAt, &t.CreatedAt)
	last = later(last, t.ReopenedAt)
	last = later(last, t.DoneAt)
	if v := t.Supervision; v != nil {
		last = later(last, &v.UpdatedAt)
		if v.Progress != nil {
			last = later(last, &v.Progress.At)
		}
		if r := triageLatestReview(v); r != nil {
			last = later(last, &r.At)
		}
	}
	if t.Delivery != nil {
		last = later(last, &t.Delivery.At)
	}
	if t.Ask != nil {
		last = later(last, &t.Ask.AskedAt)
	}
	last = later(last, &restoredAt)
	for _, a := range triageAgents(t.Todo) {
		if a.ReleasedAt != nil || look == nil {
			continue
		}
		if s, ok := look(a.MachineID, a.SessionID); ok {
			last = later(last, &s.UpdatedAt)
			last = later(last, &s.StateSince)
		}
	}
	return last
}

const triagePRStatusFreshFor = 15 * time.Minute

func triageNormalizePRURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.IndexAny(raw, "#?"); i >= 0 {
		raw = raw[:i]
	}
	return strings.ToLower(strings.TrimRight(raw, "/"))
}

// triageWaitingForMerge requires a fresh observation of the same open, non-draft PR. PR facts are display signals on
// the existing todo lifecycle: active Agents and genuine owner decisions retain priority, while stale or legacy
// delivery/stage hints cannot claim that a PR is waiting for merge.
func triageWaitingForMerge(t listedTodo, status string, now time.Time) bool {
	facts := t.PRStatus
	if t.DoneAt != nil || facts == nil || t.PRURL == "" || triageNormalizePRURL(t.PRURL) != triageNormalizePRURL(facts.URL) || facts.State != "open" || facts.Draft || facts.CheckedAt.IsZero() {
		return false
	}
	age := now.Sub(facts.CheckedAt)
	if age < 0 || age > triagePRStatusFreshFor {
		return false
	}
	if status == "failed" || status == "needs_you" || status == "interrupted" || status == "working" || status == "waiting" {
		return false
	}
	if triageReviewState(t.Todo) == reviewNeedsUser || t.Ask != nil && (t.Ask.Outcome == outcomeDecide || t.Ask.Outcome == outcomeCannot) {
		return false
	}
	return true
}

// triageDueBy is the moment a to-do is due by: its due_at, or the end of its due day in loc; false without either.
func triageDueBy(t Todo, loc *time.Location) (time.Time, bool) {
	if t.DueAt != nil {
		return *t.DueAt, true
	}
	if loc == nil {
		loc = time.UTC
	}
	day, err := time.ParseInLocation("2006-01-02", t.DueDate, loc)
	if err != nil {
		return time.Time{}, false
	}
	return day.AddDate(0, 0, 1).Add(-time.Second), true
}

// triageShelvable: a task (an agent was linked, or a conversation handed it out) with no due date ahead. An open concern
// never is (docs/assistant/todos.md "Concerns", #3582): it waits for the secretary's report, however long, and sits in
// Working while an agent of it runs, else in Other open (triageTodo keeps it out of Done too). The clients get the same
// rule with W1 / I1 / A1, and the shared fixtures follow then; so does whether a concern may share a row with a related
// to-do (triageTodos still merges it like any to-do).
func triageShelvable(t Todo, now time.Time, loc *time.Location) bool {
	if t.Kind == db.KindConcern {
		return false
	}
	if due, ok := triageDueBy(t, loc); ok && !due.Before(now) {
		return false
	}
	return len(triageAgents(t)) > 0 || t.Origin != ""
}

// triageTodo places one to-do by the SOP's decision table, first match wins (docs/assistant/todo-status-sop.md). A to-do
// the owner has seen (Got it, or opening it: todo_seen.go, #5707) since the moment that put it in Just finished is
// filed under Done.
// restoredAt is a restore from Shelved (zero on the hub: restores are kept on each device); loc is the user's zone, for
// a due day; superseded: a failed start whose row's work really finished (triageSupersededFailedStarts, #3713).
func triageTodo(t listedTodo, look triageLookup, now, restoredAt time.Time, loc *time.Location, superseded bool) todoTriage {
	last := triageLastActivity(t, look, restoredAt)
	status := triageStatus(t.Todo, look, now)
	// seen: the owner looked at it at or after at; fresh: it got to Just finished at at, less than 24 hours ago, and is
	// not seen since
	seen := func(at time.Time) bool { return t.SeenAt != nil && !t.SeenAt.Before(at) }
	fresh := func(at time.Time) bool { return now.Sub(at) < triageJustDoneFor && !seen(at) }
	ask := triageOpenAsk(t.Ask)
	at := func(section, rule string) todoTriage { return todoTriage{Section: section, Rule: rule, Last: last} }
	review := triageReviewState(t.Todo)
	// An open concern closes only with the secretary's report: nothing else files it with the finished ones.
	concern := t.Kind == db.KindConcern && t.DoneAt == nil
	// rule 8's signals: a turn nobody has seen, a review not done yet, an ask that says done / please verify
	finished := status == "unread" || review == reviewPending || ask != nil && (ask.Outcome == "" || ask.Outcome == outcomeDone)

	switch {
	case status == "failed":
		if superseded { // the same piece of work finished in a related to-do: history, nothing left to retry
			return at(cond(fresh(cmp.Or(derefTime(t.DoneAt), last)), triageJustDone, triageDone), ruleSuperseded)
		}
		if now.Sub(last) < triageShelveAfter {
			return at(triageWaiting, ruleFailedStart)
		}
		if concern {
			return at(triageOther, ruleRest)
		}
		return at(triageDone, ruleFailedStart)
	case status == "needs_you" || status == "interrupted":
		return at(triageWaiting, ruleAgentAsks)
	case t.DoneAt == nil && (review == reviewNeedsUser || ask != nil && (ask.Outcome == outcomeDecide || ask.Outcome == outcomeCannot)):
		return at(triageWaiting, ruleOwnerDecides)
	case !(finished && !seen(last) && review != reviewParked) && triageWaitingForMerge(t, status, now):
		// rule 9 after rule 8: a turn or review nobody has looked at stays Just finished even with its PR open. A parked
		// hold is its supervisor's reading of the latest turn, so an agent session left unread does not hide its PR.
		return at(triagePR, rulePROpen)
	case status == "working" || status == "waiting":
		return at(triageWorking, ruleAgentRunning)
	case t.DoneAt != nil:
		return at(cond(fresh(*t.DoneAt), triageJustDone, triageDone), ruleClosed)
	case !concern && acceptanceStands(t.Todo):
		return at(cond(fresh(triageLatestReview(t.Supervision).At), triageJustDone, triageDone), ruleAccepted)
	case !concern && t.Delivery != nil && t.Delivery.State == deliveryLive:
		return at(cond(fresh(cmp.Or(t.Delivery.At, last)), triageJustDone, triageDone), ruleDeployed)
	case finished && !seen(last):
		return at(triageJustDone, ruleFinishedUnseen)
	case finished && !concern: // seen since it last moved, and no PR waits on the owner (a concern is never Done open)
		return at(triageDone, ruleSeen)
	case triageShelvable(t.Todo, now, loc) && now.Sub(last) >= triageShelveAfter:
		return at(triageShelved, ruleStale)
	}
	return at(triageOther, ruleRest)
}

// derefTime is *at, or the zero time.
func derefTime(at *time.Time) time.Time {
	if at == nil {
		return time.Time{}
	}
	return *at
}

func cond[T any](c bool, a, b T) T {
	if c {
		return a
	}
	return b
}

// triageCountsAsOpen: the header's open count takes it (the web's countsAsOpen): not closed, not accepted or deployed,
// not shelved; a failed start counts while it waits on the owner.
func triageCountsAsOpen(t Todo, tr todoTriage) bool {
	if tr.Rule == ruleFailedStart {
		return tr.Section == triageWaiting
	}
	if t.DoneAt != nil || tr.Section == triageShelved || tr.Section == triageDone {
		return false
	}
	return tr.Rule != ruleAccepted && tr.Rule != ruleDeployed
}

// ---- related to-dos ----

var (
	triageIssueRef  = lazyre.New(`#(\d{2,7})\b`)
	triagePRTail    = lazyre.New(`[#?].*$`)
	triageTitleDrop = lazyre.New(`[\s\p{Z}\p{P}\p{S}]+`)
)

// triageRelatedKeys are the keys that make two to-dos the same piece of work (the web's relatedKeys): the same PR, the
// same issue number in the title, the same title without case, spaces and punctuation (six characters or more), the
// same plan.
func triageRelatedKeys(t Todo) []string {
	var keys []string
	pr := strings.ToLower(strings.TrimRight(triagePRTail.ReplaceAllString(strings.TrimSpace(t.PRURL), ""), "/"))
	if pr != "" {
		keys = append(keys, "pr:"+pr)
	}
	if m := triageIssueRef.FindStringSubmatch(t.Title); m != nil {
		keys = append(keys, "issue:"+m[1])
	}
	if title := triageTitleDrop.ReplaceAllString(strings.ToLower(t.Title), ""); utf8.RuneCountInString(title) >= 6 {
		keys = append(keys, "title:"+title)
	}
	if t.PlanID != "" {
		keys = append(keys, "plan:"+t.PlanID)
	}
	return keys
}

// triageGroup is one row: related to-dos led by the most urgent.
type triageGroup struct {
	Section string
	Lead    listedTodo
	Members []listedTodo // the lead first, then by section and recency
	Last    time.Time
	Triage  map[string]todoTriage
}

// triageResult is every to-do placed: rows by section, and the header's counts.
type triageResult struct {
	Sections                map[string][]triageGroup
	Open, Shelved, Finished int
}

// triageRelatedRoots is the row each to-do is in (the web's relatedRoots): the root of its related to-dos, open and
// closed ones kept apart.
func triageRelatedRoots(list []listedTodo) func(string) string {
	parent := map[string]string{}
	for _, t := range list {
		parent[t.ID] = t.ID
	}
	var find func(string) string
	find = func(id string) string {
		r := id
		for parent[r] != r {
			r = parent[r]
		}
		parent[id] = r
		return r
	}
	owner := map[string]string{}
	for _, t := range list {
		side := cond(t.DoneAt != nil, "c", "o")
		for _, k := range triageRelatedKeys(t.Todo) {
			key := side + "|" + k
			if other, ok := owner[key]; ok {
				parent[find(t.ID)] = find(other)
			} else {
				owner[key] = t.ID
			}
		}
	}
	return find
}

// triageSupersededFailedStarts are the failed starts (rule 1) whose row has a to-do that really finished: closed
// without a start error (the web's supersededFailedStarts, #3713). The work got done another way; such a failed start
// goes to Just finished / Done and is not counted open.
func triageSupersededFailedStarts(list []listedTodo) map[string]bool {
	find := triageRelatedRoots(list)
	finished := map[string]bool{}
	for _, t := range list {
		if t.DoneAt != nil && t.StartError == "" {
			finished[find(t.ID)] = true
		}
	}
	out := map[string]bool{}
	for _, t := range list {
		if t.StartError != "" && finished[find(t.ID)] {
			out[t.ID] = true
		}
	}
	return out
}

// triagePlace places each to-do of list by id, as triageTodos does (a failed start superseded by its row's finished
// work included); the direction tree reads the same places (direction_tree.go).
func triagePlace(list []listedTodo, look triageLookup, now time.Time, loc *time.Location) map[string]todoTriage {
	superseded := triageSupersededFailedStarts(list)
	placed := make(map[string]todoTriage, len(list))
	for _, t := range list {
		placed[t.ID] = triageTodo(t, look, now, time.Time{}, loc, superseded[t.ID])
	}
	return placed
}

// triageTodos places every to-do and merges related ones into rows (the web's triageTodos): closed and open to-dos never
// share a row; a row takes its lead's section (a superseded failed start never leads); rows go most recently active
// first. includeQuiet false leaves quiet tasks out, as the sidebar's badge does.
func triageTodos(list []listedTodo, look triageLookup, now time.Time, loc *time.Location, includeQuiet bool) triageResult {
	if !includeQuiet {
		list = slices.DeleteFunc(slices.Clone(list), func(t listedTodo) bool { return t.Quiet })
	}
	placed := triagePlace(list, look, now, loc)
	find := triageRelatedRoots(list)
	var roots []string
	byRoot := map[string][]listedTodo{}
	for _, t := range list {
		r := find(t.ID)
		if _, ok := byRoot[r]; !ok {
			roots = append(roots, r)
		}
		byRoot[r] = append(byRoot[r], t)
	}
	res := triageResult{Sections: map[string][]triageGroup{}}
	for _, t := range list {
		tr := placed[t.ID]
		if triageCountsAsOpen(t.Todo, tr) {
			res.Open++
		}
		if tr.Section == triageShelved {
			res.Shelved++
		}
	}
	res.Finished = len(list) - res.Open - res.Shelved
	order := func(s string) int { return slices.Index(triageSections, s) }
	for _, r := range roots {
		members := byRoot[r]
		slices.SortStableFunc(members, func(a, b listedTodo) int {
			ta, tb := placed[a.ID], placed[b.ID]
			behind := func(t todoTriage) int { return cond(t.Rule == ruleSuperseded, 1, 0) }
			return cmp.Or(cmp.Compare(order(ta.Section), order(tb.Section)), cmp.Compare(behind(ta), behind(tb)),
				tb.Last.Compare(ta.Last), strings.Compare(a.ID, b.ID))
		})
		g := triageGroup{Lead: members[0], Members: members, Section: placed[members[0].ID].Section, Triage: map[string]todoTriage{}}
		for _, m := range members {
			g.Triage[m.ID] = placed[m.ID]
			last := placed[m.ID].Last
			g.Last = later(g.Last, &last)
		}
		res.Sections[g.Section] = append(res.Sections[g.Section], g)
	}
	for s, gs := range res.Sections {
		slices.SortStableFunc(gs, func(a, b triageGroup) int {
			return cmp.Or(b.Last.Compare(a.Last), strings.Compare(a.Lead.ID, b.Lead.ID))
		})
		res.Sections[s] = gs
	}
	return res
}
