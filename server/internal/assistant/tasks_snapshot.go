package assistant

import (
	"cmp"
	"context"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/todo/projection"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- what the assistant sees ----

// snapshotOpenTodoLimit, snapshotTaskLimit: how many rows of the open to-do index and of the task details one
// conversation's snapshot shows. The cached snapshot keeps them all, so a conversation's own come first whatever the
// others have (scopeSnapshot).
const (
	snapshotOpenTodoLimit = 100
	snapshotTaskLimit     = 12
)

// snapshotOpenTodos is the compact index that keeps every ordinary-sized open todo discoverable: all of them, newest
// first, and how many (scopeSnapshot shows a conversation the first snapshotOpenTodoLimit of its own). A failed start
// is open as long as the app's To-dos count it open (snapshotFailedStarts), and marked as one.
func (s *Service) snapshotOpenTodos(ctx context.Context, web string) ([]prompts.SnapshotOpenTodo, int) {
	if s.ensureTodosLoaded(ctx) != nil {
		return nil, 0
	}
	asked := s.askedInSnapshot(ctx)
	s.todos.mu.RLock()
	list := make([]Todo, 0, len(s.todos.todos))
	var failed []Todo
	for _, t := range s.todos.todos {
		switch {
		case asked[t.ID]:
		case t.DoneAt == nil:
			list = append(list, copyTodoLocked(t))
		case t.failedStart():
			failed = append(failed, copyTodoLocked(t))
		}
	}
	var all []listedTodo
	if len(failed) > 0 { // what a failed start is superseded by is read across every to-do, as the app's triage reads it
		all = make([]listedTodo, 0, len(s.todos.todos))
		for _, t := range s.todos.todos {
			all = append(all, listedTodo{Todo: copyTodoLocked(t)})
		}
	}
	s.todos.mu.RUnlock()
	cfg := s.settings(ctx)
	if cfg.todosV2On() && len(failed) > 0 {
		s.populateTodoActiveBlockers(ctx, all)
	}
	open := snapshotFailedStartsWithConfig(failed, all, time.Now(), s.location(ctx), cfg)
	for _, t := range failed {
		if open[t.ID] {
			list = append(list, t)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].ID < list[j].ID
		}
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})
	out := make([]prompts.SnapshotOpenTodo, 0, len(list))
	for _, t := range list {
		out = append(out, prompts.SnapshotOpenTodo{ID: t.ID, Title: clip(oneLine(t.Title), 40), Link: todoLink(web, t.ID), Owner: todoOwner(t),
			Failed: t.DoneAt != nil})
	}
	return out, len(out)
}

// snapshotFailedStarts are those of failed (failed starts) that the app's To-dos count open (triageCountsAsOpen): one
// that still waits on the owner (rule 1, younger than triageShelveAfter) and whose row's work did not finish in a related
// to-do (triageSupersededFailedStarts over all). The snapshot's open count is the app's, so a failed start no longer
// takes one off it (#10365).
func snapshotFailedStarts(failed []Todo, all []listedTodo, now time.Time) map[string]bool {
	return snapshotFailedStartsWithConfig(failed, all, now, nil, Settings{})
}

func snapshotFailedStartsWithConfig(failed []Todo, all []listedTodo, now time.Time, loc *time.Location, cfg Settings) map[string]bool {
	if len(failed) == 0 {
		return nil
	}
	if cfg.todosV2On() {
		facts := make([]projection.Facts, len(all))
		for i := range all {
			facts[i] = projectionFacts(all[i], nil, loc)
			applyTodosV2FactsConfig(&facts[i], cfg)
		}
		views := projection.Views(facts, now)
		viewByID := make(map[string]assistantwire.TodoSummary, len(all))
		for i := range all {
			viewByID[all[i].ID] = views[i]
		}
		open := make(map[string]bool, len(failed))
		for _, t := range failed {
			v, ok := viewByID[t.ID]
			if !ok {
				f := projectionFacts(listedTodo{Todo: t}, nil, loc)
				applyTodosV2FactsConfig(&f, cfg)
				v = projection.View(f, now)
			}
			if projectionViewCountsAsOpen(v) {
				open[t.ID] = true
			}
		}
		return open
	}
	superseded := triageSupersededFailedStarts(all)
	open := make(map[string]bool, len(failed))
	for _, t := range failed {
		l := listedTodo{Todo: t}
		if triageCountsAsOpen(t, triageTodo(l, nil, now, time.Time{}, nil, superseded[t.ID])) {
			open[t.ID] = true
		}
	}
	return open
}

// askedInSnapshot are the question to-dos the snapshot's own line (snapshotAsked) shows. Only those stay out of the
// roster and the task list: one whose ask record was pruned (askTodoKeep, askTodoMax), or that line no longer shows, is
// an ordinary to-do and is listed like any other, so no open to-do drops out of both.
func (s *Service) askedInSnapshot(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	for _, a := range s.snapshotAsked(ctx) {
		out[a.TodoID] = true
	}
	return out
}

// snapshotTasks are the open todos plus recent conversation tasks that have ended, the highest-priority first: all of
// them (scopeSnapshot shows a conversation the first snapshotTaskLimit of its own).
func (s *Service) snapshotTasks(ctx context.Context, web string) []prompts.SnapshotTask {
	var list []Todo
	if s.ensureTodosLoaded(ctx) != nil {
		return nil
	}
	asked := s.askedInSnapshot(ctx)
	s.todos.mu.RLock()
	for _, row := range s.todos.todos {
		t := copyTodoLocked(row)
		if asked[t.ID] {
			continue // questions have their own line in the snapshot
		}
		if t.DoneAt == nil || (t.Origin != "" && time.Since(*t.DoneAt) < 24*time.Hour && time.Since(t.UpdatedAt) < 7*24*time.Hour) {
			list = append(list, t)
		}
	}
	s.todos.mu.RUnlock()
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
	type ranked struct {
		task prompts.SnapshotTask
		rank int
		open bool
	}
	var all []ranked
	now := time.Now()
	cfg := s.settings(ctx)
	loc := s.location(ctx)
	asks := s.todoAsks(ctx)
	var blockersByTodo map[string][]todoActiveBlockerView
	if cfg.todosV2On() && len(list) > 0 {
		tmp := make([]listedTodo, len(list))
		for i := range list {
			tmp[i] = listedTodo{Todo: list[i]}
		}
		s.populateTodoActiveBlockers(ctx, tmp)
		blockersByTodo = make(map[string][]todoActiveBlockerView)
		for i := range tmp {
			if len(tmp[i].ActiveBlockers) > 0 {
				blockersByTodo[tmp[i].ID] = tmp[i].ActiveBlockers
			}
		}
	}
	var openIDs []string
	for _, t := range list {
		if t.DoneAt == nil {
			openIDs = append(openIDs, t.ID)
		}
	}
	reposByTodo := s.loadOpenTodoRepositories(ctx, openIDs)
	lists := machineSessions{} // each machine listed once for all the tasks
	for _, t := range list {
		// The hold as the apps read it (currentDelivery): a parked wait or a supervisor's unreviewed hold is not the owner's.
		t.Delivery = currentDelivery(t)
		agents := s.taskAgentsIn(ctx, t, lists)
		st := taskState(t, agents)
		if t.DoneAt == nil {
			review := ""
			if t.Supervision != nil {
				review = t.Supervision.state(t)
			}
			var ask *todoAsk
			if a, ok := asks[t.ID]; ok {
				ask = triageOpenAsk(&a)
			}
			decisionRow := []listedTodo{{Todo: t}}
			if err := s.listDecisionReviews(ctx, decisionRow); err != nil {
				decisionRow[0].DecisionReview = nil // a missing dismissal record fails closed: the owner wait stays
			}
			decision := decisionRow[0].DecisionReview
			dismissed := decision != nil && decision.DismissalID != ""
			atRest := st == "followup" || st == "unread" || st == "not_started"
			switch {
			case t.Delivery.waitsForOwner() || !dismissed && (review == reviewNeedsUser || ask != nil && (ask.Outcome == outcomeDecide || ask.Outcome == outcomeCannot)):
				st = "owner_wait"
			case atRest && (st == "unread" || review == reviewPending || deliveryAwaitsReview(t, agents) ||
				ask != nil && (ask.Outcome == "" || ask.Outcome == outcomeDone)):
				st = "awaiting_review"
			}
			// The hub snapshot follows the same owner / Accepted / Seen decision table as every client. In particular,
			// dismissing a review keeps an independent delivery approval, reopening accepted work does not erase acceptance,
			// and Seen files only an ordinary finished turn (#5707, #5732).
			switch snapshotDoneRuleWithConfig(t, agents, ask, decision, blockersByTodo[t.ID], now, loc, cfg) {
			case ruleOwnerDecides:
				st = "owner_wait"
			case ruleAccepted, ruleSeen:
				st = "done"
			}
			if st == "not_started" || st == "followup" {
				var gate *repoGate
				if bound := reposByTodo[t.ID]; len(bound) > 0 {
					gate = snapshotRepoGate(t, bound, cfg.todosV2On())
				}
				if prState := openPRSnapshotState(triageOpenPRs(t, now), gate); prState != "" {
					st = prState
				}
			}
			if st == "not_started" {
				switch {
				case review == reviewParked:
					st = "parked"
				case t.Kind == db.KindConcern:
					st = "tracking"
				}
			}
		}
		if t.DoneAt != nil && st == "followup" && time.Since(t.UpdatedAt) > 24*time.Hour {
			continue
		}
		if st == "done" && t.DoneBy == db.DoneByAuto {
			st = "auto_done"
		}
		task := snapshotTask(t, st, agents, now)
		if st == "failed" && task.Last == "" {
			task.Last = clip(oneLine(t.StartError), 80) // why it did not start, for the morning brief (#4124)
		}
		task.Link = todoLink(web, t.ID)
		task.OriginID, task.OriginTitle, task.OriginLink = s.originAttribution(ctx, t, web)
		if t.Supervision != nil && t.Supervision.Supervisor != "" && t.Supervision.Supervisor != s.settings(ctx).Main {
			task.SupervisedElsewhere = true
		}
		if t.Cancelled() {
			task.CancelledReason = clip(oneLine(t.DoneReason), 80)
			if v := t.Supervision; v != nil {
				for i := len(v.Reviews) - 1; i >= 0; i-- {
					if r := v.Reviews[i]; r.Verdict == reviewCancelled && r.Note != "" {
						task.CancelledReason = clip(oneLine(r.Note), 80)
						break
					}
				}
			}
		}
		for _, step := range s.taskLineage(t.ID) {
			if !step.Current {
				continue
			}
			task.Continues, task.ContinuesLink = step.Continues, todoLink(web, step.Continues)
			task.SupersededBy, task.SupersededByLink = step.SupersededBy, todoLink(web, step.SupersededBy)
		}
		task.Delivery = clip(oneLine(deliverySummary(t)), 80)
		var rank int
		task.Health, rank = taskHealth(t, agents, now) // supervised tasks: what needs the assistant first (supervise.go)
		if dismissed, err := s.decisionDismissed(ctx, t); err == nil && dismissed && !t.Delivery.waitsForOwner() {
			// The owner chose Don't handle on this decision: not theirs to answer now, so it is not raised again. An
			// independent delivery approval is a different owner wait and keeps both its state and its priority (#5732).
			task.Health, rank = "你选了不处理这个决定", 6 // i18n-ignore: the snapshot's words, as taskHealth has them
		}
		if v := t.Supervision; v.state(t) == reviewParked {
			task.Health = parkedHealth(v.parked(), s.location(ctx)) // until when, in the owner's zone (parked.go)
		}
		if o, ok := s.orphanOf(t.ID); ok && t.DoneAt == nil {
			task.Orphan = o.Reason   // nobody works on it any more (tasks_orphan.go)
			if task.Health == "正常" { // i18n-ignore: the snapshot's words, as taskHealth has them
				task.Health, rank = "没人在做", 2 // i18n-ignore: the snapshot's words
			}
		}
		if t.Supervision != nil {
			task.DoneWhen = len(t.Supervision.DoneWhen)
			if p := t.Supervision.Progress; p != nil && !isCodexRouteProgress(p) {
				lead := taskLead(st, agents)
				// A completed round's last reply says more than its earlier progress.
				// Explicit conclusions, outstanding questions, and progress newer than
				// the last result timestamp still have their own line.
				resultReady := st == "done" || st == "auto_done" || st == "awaiting_review" ||
					(t.Supervision.state(t) == reviewUserCheck && (st == "owner_wait" || st == "followup"))
				resultAt := lead.UpdatedAt
				if t.DoneAt != nil && t.DoneAt.After(resultAt) {
					resultAt = *t.DoneAt
				}
				progressNewer := !p.At.IsZero() && !resultAt.IsZero() && p.At.After(resultAt)
				if p.Kind != upReportProgress || task.Last == "" || !resultReady || progressNewer {
					reported(&task, p, lead, now)
				}
			}
		}
		all = append(all, ranked{task: task, rank: rank, open: t.DoneAt == nil})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].open != all[j].open {
			return all[i].open
		}
		if all[i].rank != all[j].rank {
			return all[i].rank < all[j].rank
		}
		return false // list was newest first, so ties keep that order
	})
	var out []prompts.SnapshotTask
	for _, r := range all {
		out = append(out, r.task)
	}
	return out
}

// todoOwner is whose task t is in the snapshot: the conversation that handed it out and the one that supervises it.
func todoOwner(t Todo) prompts.SnapshotOwner {
	o := prompts.SnapshotOwner{Origin: t.Origin}
	if t.Supervision != nil {
		o.Supervisor = t.Supervision.Supervisor
	}
	return o
}

// snapshotTask is one task in the snapshot, in state st: its lead agent's (taskLead) last words, the step it is on
// while it works (or has said nothing yet: an agent that only runs tools, #1583) and its running subagents, as the
// task cards show them (docs/assistant/unified-entry.md "Task cards").
func snapshotTask(t Todo, st string, agents []proto.Session, now time.Time) prompts.SnapshotTask {
	task := prompts.SnapshotTask{ID: t.ID, Title: clip(oneLine(t.Title), 60), State: taskStateLabel(st), Owner: todoOwner(t)}
	last := taskLead(st, agents)
	if last.SessionID == "" {
		return task
	}
	if st == "interrupted" && last.TurnErrorKind == proto.TurnErrorStopped {
		task.State = taskStateLabel("stopped") // Persona's Stop, not the model service (silent_turns.go, #10187)
	}
	task.Machine, task.Session = last.MachineID, short(last.SessionID)
	task.Last = clip(oneLine(last.LastText), 80)
	working := last.State == proto.StateWorking
	if doing := strings.TrimSpace(last.Activity); doing != "" && (working || task.Last == "") {
		task.Doing = clip(oneLine(doing), 80)
		if working && last.ActivityAt != nil && now.Sub(*last.ActivityAt) >= time.Minute {
			task.DoingFor = humanDuration(now.Sub(*last.ActivityAt))
		}
	}
	for _, sa := range last.Subagents {
		if sa.Running && len(task.Subagents) < 3 {
			task.Subagents = append(task.Subagents, clip(oneLine(cmp.Or(sa.Description, sa.Type, "子任务")), 40))
		}
	}
	task.Workflows = runningWorkflows(last, now)
	task.Plan = snapshotPlan(last)
	return task
}

// reported puts the agent's latest report p on task. A report newer than lead's plan says more than the plan's count:
// it stands alone.
func reported(task *prompts.SnapshotTask, p *TaskProgress, lead proto.Session, now time.Time) {
	task.Reported = &prompts.SnapshotReport{Kind: p.Kind, Step: p.Step, Steps: p.Steps, Text: clip(oneLine(p.Text), 160),
		Minutes: max(0, int(now.Sub(p.At).Minutes()))}
	if lead.Plan != nil && p.At.After(lead.Plan.At) {
		task.Plan = nil
	}
}

// snapshotPlan is c's own task list as the snapshot tells it, nil when it keeps none (docs/integrations/agent-goal.md "Plan").
func snapshotPlan(c proto.Session) *prompts.SnapshotAgentPlan {
	p := c.Plan
	if p == nil || p.Total == 0 {
		return nil
	}
	return &prompts.SnapshotAgentPlan{Done: p.Done, Total: p.Total, Current: clip(oneLine(p.Current), 60)}
}

// taskLead is the agent a task in state st is shown with: the latest of those that put it in that state (the one
// waiting for the user, cut off, finished unseen, working), so its session id and words are the ones the state is
// about (#4192); the latest of all when none does (a task that stopped, or is done).
func taskLead(st string, agents []proto.Session) proto.Session {
	var lead, latest proto.Session
	for _, c := range agents {
		if latest.SessionID == "" || c.UpdatedAt.After(latest.UpdatedAt) {
			latest = c
		}
		var makes bool
		switch st {
		case "needs_you":
			makes = c.State == proto.StateNeedsYou || c.PendingInteractions > 0
		case "interrupted":
			makes = c.TurnError != "" && !usageLimitWaiting(c, time.Now())
		case "usage_limit":
			makes = usageLimitWaiting(c, time.Now())
		case "unread":
			makes = c.State == proto.StateUnread
		case "working":
			makes = c.State == proto.StateWorking && !agentWaits(c)
		case "waiting":
			makes = agentWaits(c)
		}
		if makes && (lead.SessionID == "" || c.UpdatedAt.After(lead.UpdatedAt)) {
			lead = c
		}
	}
	if lead.SessionID != "" {
		return lead
	}
	return latest
}

// taskState follows docs/assistant/todos.md "State calculation rules"(clients compute the same). One more for the
// assistant: an agent a usage limit stopped until its reset (usage_limit.go) is not interrupted for the user to
// continue. It waits for the reset (its CLI or the manager goes on then), and a "Continue" before it is refused at once.
func taskState(t Todo, agents []proto.Session) string {
	if t.failedStart() {
		return "failed"
	}
	// A cancelled task is closed but did not finish: it reads as "cancelled", never "done", however its agents settle.
	// Cancelled() is the single source of truth the receipt and auto-completion paths already use (#2272). Reopen
	// rebuilds the todo row without DoneAt/DoneBy, and Supervision.state masks a stale cancel verdict while the task
	// is open; re-finished work closes with a non-cancel marker, so Cancelled() is false then and it falls through to
	// "done" without a separate reopen guard here.
	if t.DoneAt != nil && t.Cancelled() {
		return "cancelled"
	}
	if t.DoneAt != nil {
		return "done"
	}
	if t.Supervision.state(t) == reviewTimedOut {
		return reviewTimedOut
	}
	has := map[string]bool{}
	interrupted, limited, working, waiting := false, false, false, false
	now := time.Now()
	for _, c := range agents {
		has[c.State] = true
		if agentWaits(c) {
			waiting = true
		} else if c.State == proto.StateWorking {
			working = true
		}
		if usageLimitWaiting(c, now) {
			limited = true
		} else {
			interrupted = interrupted || c.TurnError != ""
		}
	}
	switch {
	case has[proto.StateNeedsYou]:
		return "needs_you"
	case interrupted: // stopped on a model API error: not done, waiting for "Continue" (#1516)
		return "interrupted"
	case limited:
		return "usage_limit"
	case has[proto.StateUnread]:
		return "unread"
	case working:
		return "working"
	case waiting:
		return "waiting"
	case len(agents) > 0:
		return "followup"
	}
	return "not_started"
}

// snapshotDoneRule reuses the clients' triage decision table for owner decisions and the two open-at-rest Done
// transitions the snapshot needs: accepted and ordinarily Seen. Working, concern, open-PR and acceptance-required rows
// keep their existing snapshot projection instead of getting a second, divergent copy of those rules here.
func snapshotDoneRule(t Todo, agents []proto.Session, ask *todoAsk, decision *decisionReview, now time.Time, loc *time.Location) string {
	return snapshotDoneRuleWithConfig(t, agents, ask, decision, nil, now, loc, Settings{})
}

func snapshotDoneRuleWithConfig(t Todo, agents []proto.Session, ask *todoAsk, decision *decisionReview, activeBlockers []todoActiveBlockerView, now time.Time, loc *time.Location, cfg Settings) string {
	lookup := func(machine, session string) (proto.Session, bool) {
		for _, c := range agents {
			if c.MachineID == machine && c.SessionID == session {
				return c, true
			}
		}
		return proto.Session{}, false
	}
	lt := listedTodo{Todo: t, Ask: ask, DecisionReview: decision, ActiveBlockers: activeBlockers}
	if cfg.todosV2On() {
		sessions := make(map[[2]string]proto.Session, len(agents))
		for _, c := range agents {
			sessions[[2]string{c.MachineID, c.SessionID}] = c
		}
		f := projectionFacts(lt, sessions, loc)
		applyTodosV2FactsConfig(&f, cfg)
		return todoTriageFromProjectionView(lt, projection.View(f, now), lookup).Rule
	}
	return triageTodo(lt, lookup, now, time.Time{}, loc, false).Rule
}

// deliveryAwaitsReview is the snapshot's unaccepted live state at rest. A new working stretch is still work in
// progress, not a second delivery; an open concern instead waits for its own report (#3582). taskHealth uses the same
// predicate so State and Health cannot disagree (#5732).
func deliveryAwaitsReview(t Todo, agents []proto.Session) bool {
	if t.DoneAt != nil || t.Kind == db.KindConcern || t.Delivery == nil || t.Delivery.State != deliveryLive {
		return false
	}
	switch taskState(t, agents) {
	case "followup", "unread", "not_started":
		return true
	}
	return false
}

// agentWaits: c's turn ended and the hub or its own background work brings it back, so its task waits rather than
// stopped (#3274): only background commands, subagents, Monitors or workflows still run and will report back
// (BackgroundWorking), or the hub waits out another agent, a deploy / CI / a time, or a moment's model failure for it
// and wakes it (Work blocked on anything but the user).
func agentWaits(c proto.Session) bool {
	if c.State == proto.StateWorking {
		return c.BackgroundWorking
	}
	w := c.Work
	return c.State != proto.StateNeedsYou && c.State != proto.StateUnread && w != nil && w.Phase == proto.WorkBlocked &&
		(w.On == proto.WaitAgent || w.On == proto.WaitExternal || w.On == proto.WaitTransient)
}

func taskStateLabel(st string) string {
	return map[string]string{"done": "已完成", "auto_done": "已完成（hub 判断做完了，自动关的）", "failed": "没开起来", "needs_you": "等用户",
		"owner_wait": "等你处理", "awaiting_review": "已交付·待看", // i18n-ignore: the model reads these snapshot labels
		"interrupted": "模型服务出错，中断了（没做完，等用户让它继续）", "unread": "已交付·待看", "working": "在做",
		"followup": "停着", "not_started": "还没开始", "timed_out": "Timed out; Continue resumes the retained session",
		// "cancelled" shares the ignored final line: every label here is model-read prompt text, and this keeps the
		// hardcoded-Chinese baseline count unchanged instead of raising it.
		// i18n-ignore: the model reads it; "stopped" is "interrupted" when Persona's Stop did it (snapshotTask)
		"stopped":      "随对话一起被停下了（没做完：用户停了交出它的对话，它还没有任何输出；等用户让它继续）",
		"pr_conflicts": "PR 有冲突", "pr_changes_requested": "PR 待修改", "pr_checks_failed": "PR 检查失败", // i18n-ignore: the model reads these snapshot labels
		"pr_in_review": "PR 评审中", "pr_checks_pending": "PR 检查中", "pr_waiting_merge": "等合入", // i18n-ignore: the model reads these snapshot labels
		"parked": "已搁置", "tracking": "跟进中", // i18n-ignore: the model reads these snapshot labels
		"cancelled": "已取消", "waiting": "在等（后台命令、子 Agent 或外部条件，hub 会叫醒它，不用管）", "usage_limit": "订阅的用量额度用完了，等额度重置（没做完；重置前发「继续」也会被拒）"}[st] // i18n-ignore: the model reads it
}
