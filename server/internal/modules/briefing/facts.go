package briefing

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/modapi"
)

// The fixed part of the morning and evening routines (docs/extensions/bots.md "Morning and evening briefings"): with a
// task's `facts` on, Persona works out what waits for the owner, what happened in the last Window and what is to be
// verified when the run starts, once. The Lark card, the push text and the facts the model is given all come from that
// one computation, so their numbers agree; the model only writes a sentence or two on top.

const (
	// Window is how far back "overnight" looks from the start of the run.
	Window = 12 * time.Hour
	// RowsMax is the most rows one section lists; the rest are counted ("and N more").
	RowsMax = 10
	// ModelRows is the most rows of a list the model is given (the counts still say how many there are).
	ModelRows = 30
	// ProgressMax is the most tasks "in progress" lists.
	ProgressMax = 8
	// FollowingMax is the most Inbox matters in each model-only follow-up list.
	FollowingMax = 5
	// Keep is how long after its start the facts of a run are still said: the turn of a routine takes minutes, and a
	// message the owner sends into its conversation hours later is no briefing.
	Keep = 3 * time.Hour
	// Grace is how long (minutes) after the morning briefing's time the acceptance reminders wait when that briefing
	// lists the tasks to verify itself: if it has not gone out by then they are sent one by one as before.
	Grace = 60

	// Inbox follow-up states (matching internal/assistant/inbox_follow.go).
	inboxNowWaiting     = "waiting"
	inboxNowCheckDue    = "check_due"
	inboxNowFollowUpAsk = "follow_up_ask"
)

// FleetWait is how many seconds a run waits for the pending cards to be read after a restart (FleetUp) before it works
// out its facts without them.
var FleetWait = 20

// book holds the facts of the runs under way: by task from the start of the run, by conversation once it is sent.
type book struct {
	mu        sync.Mutex
	byTask    map[string]*modapi.BriefRun
	bySession map[string]*modapi.BriefRun
}

// runner owns one kernel instance's briefing fact gathering, feed writing, fallback and run retention.
type runner struct {
	logf     func(format string, args ...any)
	tasks    modapi.TaskPort
	todos    modapi.TodoPort
	inbox    modapi.InboxPort
	later    modapi.LaterPort
	schedule modapi.SchedulePort
	feed     modapi.FeedPort
	delivery modapi.DeliveryPort
	dayRecap modapi.DayRecapPort
	prompts  modapi.PromptPort
	book     book
}

var _ modapi.ScheduleFactsRunner = (*runner)(nil)

func newRunner(h modapi.Host) *runner {
	r := &runner{
		logf: func(string, ...any) {},
	}
	if h != nil {
		r.logf = h.Logf
		r.tasks = h.Tasks()
		r.todos = h.Todos()
		r.inbox = h.Inbox()
		r.later = h.Later()
		r.schedule = h.Schedule()
		r.feed = h.Feed()
		r.delivery = h.Delivery()
		r.dayRecap = h.DayRecap()
		r.prompts = h.Prompts()
	}
	return r
}

// FactsAt works out the facts for a run that starts at now. self is the routine that is running: its own earlier failed
// attempt (this run is its retry) is not news.
func (r *runner) FactsAt(ctx context.Context, now time.Time, self string) *modapi.BriefSnapshot {
	if r.tasks == nil {
		return &modapi.BriefSnapshot{At: now, Loc: time.UTC, Changed: map[string]string{}, Asks: map[string]modapi.BriefAsk{}}
	}
	l, web, loc := r.tasks.BotLang(ctx), r.tasks.WebBase(ctx), r.tasks.Location(ctx)
	if loc == nil {
		loc = time.UTC
	}
	f := &modapi.BriefSnapshot{
		At:             now,
		Lang:           l,
		Web:            web,
		Loc:            loc,
		WaitingUnknown: !r.tasks.FleetReady(),
		Changed:        map[string]string{},
		Asks:           map[string]modapi.BriefAsk{},
	}
	if r.tasks.FeishuPushesOn(ctx) {
		f.Card = true
	}
	waiting := map[string]bool{}
	for _, row := range r.tasks.BoardRows(ctx, l, web, now) {
		if row.Blocking {
			if !f.WaitingUnknown {
				f.Waiting = append(f.Waiting, row)
			}
		} else {
			f.Accept = append(f.Accept, row)
			waiting[row.ID] = true
			if a, ok := r.tasks.AskCopy(row.ID, l); ok {
				f.Changed[row.ID], f.Asks[row.ID] = a.Changed, a
			}
		}
	}
	if r.later != nil {
		f.Later, f.LaterTotal = r.later.Rows(ctx, l, web, now, RowsMax)
	}
	since := now.Add(-Window)
	for _, d := range r.tasks.TodayFinished(ctx, since, loc) {
		if !waiting[d.ID] {
			f.Done = append(f.Done, d)
		}
	}
	f.DoneTotal = len(f.Done)
	if len(f.Done) > RowsMax {
		f.Done = f.Done[:RowsMax]
	}
	failed, total := r.tasks.TodayFailures(ctx, since, loc)
	for _, x := range failed {
		if x.Kind == "routine" && x.ID == self {
			total--
			continue
		}
		f.Failed = append(f.Failed, x)
	}
	f.FailedTotal = total
	f.Progress, f.ProgressTotal = r.progress(ctx, waiting)
	f.NorthStar = r.tasks.NorthStar(ctx, now, loc, l)
	f.NorthStarLines = f.NorthStar.Lines
	f.Confirmations = r.tasks.Confirmations(ctx, now, loc, l)
	f.ConfirmationLines = f.Confirmations.Lines
	f.Leaders = r.tasks.Leaders(ctx, now)
	f.Directions, f.DisabledRoutines = r.tasks.Directions(ctx, now, l, web)
	if r.inbox != nil {
		f.InboxWaiting, f.InboxDue = r.Following(r.inbox.FollowingSignals(ctx), now, loc)
	}
	if self == "evening" { // the day's recap was written for it, 30 minutes before (kb_day.go)
		f.Day = r.Day(ctx, now, loc)
	}
	if r.todos != nil && (self == "morning" || self == "evening") {
		f.DeferredSupervision = r.todos.DeferredSupervision(ctx, now)
	}
	f.Captures = r.tasks.Captures(ctx, l, since)
	return f
}

// Following makes the model's two read-only Inbox follow-up lists. waiting is the longest-waiting matters over a day
// old; due is today's checks in the owner's zone, plus matters already handed back for a check or a follow-up call. One
// matter may belong in both lists. Neither the input's order nor any item is changed.
func (r *runner) Following(items []modapi.BriefInboxSignal, now time.Time, loc *time.Location) (waiting, due []modapi.BriefFollow) {
	return Following(items, now, loc)
}

// Following is the pure Inbox follow-up filter/sorter used by the briefing module.
func Following(items []modapi.BriefInboxSignal, now time.Time, loc *time.Location) (waiting, due []modapi.BriefFollow) {
	if loc == nil {
		loc = time.UTC
	}
	day := dayStart(now.In(loc))
	nextDay := dayStart(day.AddDate(0, 0, 1))
	waitedBefore := now.Add(-24 * time.Hour)
	var waitingItems, dueItems []modapi.BriefInboxSignal
	for _, it := range items {
		if !it.Open {
			continue
		}
		if it.Now == inboxNowWaiting && it.WaitingSince != nil && it.WaitingSince.Before(waitedBefore) {
			waitingItems = append(waitingItems, it)
		}
		isDue := it.Now == inboxNowCheckDue || it.Now == inboxNowFollowUpAsk
		if it.Now == inboxNowWaiting && it.CheckAt != nil {
			at := it.CheckAt
			isDue = isDue || !at.Before(day) && at.Before(nextDay)
		}
		if isDue {
			dueItems = append(dueItems, it)
		}
	}
	slices.SortStableFunc(waitingItems, func(a, b modapi.BriefInboxSignal) int {
		if c := a.WaitingSince.Compare(*b.WaitingSince); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	slices.SortStableFunc(dueItems, func(a, b modapi.BriefInboxSignal) int {
		aAt, bAt := a.CheckAt, b.CheckAt
		switch {
		case aAt == nil && bAt == nil:
			return strings.Compare(a.ID, b.ID)
		case aAt == nil:
			return 1
		case bAt == nil:
			return -1
		}
		if c := aAt.Compare(*bAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	row := func(it modapi.BriefInboxSignal) modapi.BriefFollow {
		return modapi.BriefFollow{
			Title: clip(oneLine(it.Title), 60),
			Who:   clip(oneLine(it.Who), 80),
			Next:  clip(oneLine(it.Next), 80),
		}
	}
	for i, it := range waitingItems {
		if i >= FollowingMax {
			break
		}
		waiting = append(waiting, row(it))
	}
	for i, it := range dueItems {
		if i >= FollowingMax {
			break
		}
		due = append(due, row(it))
	}
	return waiting, due
}

// progress are the open tasks whose Agents are working, the longest open first (those waiting to be verified are listed
// there, not here), cut at ProgressMax, and how many there are.
func (r *runner) progress(ctx context.Context, skip map[string]bool) ([]modapi.BriefTaskProgress, int) {
	if r.todos == nil {
		return nil, 0
	}
	var open []modapi.BriefTodoProgress
	for _, t := range r.todos.OpenProgressTodos(ctx) {
		if !skip[t.ID] {
			open = append(open, t)
		}
	}
	slices.SortFunc(open, func(a, b modapi.BriefTodoProgress) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	var out []modapi.BriefTaskProgress
	total := 0
	for _, t := range open {
		if !t.Active {
			continue
		}
		total++
		if len(out) < ProgressMax {
			out = append(out, modapi.BriefTaskProgress{ID: t.ID, Title: t.Title, Stage: t.Stage, StageKind: t.StageKind})
		}
	}
	return out, total
}

// Start works out the facts for the run of task that is starting, and returns them as the model is given them.
func (r *runner) Start(ctx context.Context, task modapi.BriefTaskInfo, now time.Time) string {
	if r.tasks != nil {
		for i := 0; i < FleetWait && !r.tasks.FleetReady(); i++ { // just after a restart the cards waiting are not read yet
			select {
			case <-ctx.Done():
				i = FleetWait
			case <-time.After(time.Second):
			}
		}
	}
	run := &modapi.BriefRun{Facts: r.FactsAt(ctx, now, task.ID), Label: task.Label}
	run.Facts.Contributions = task.Contributions
	if r.tasks != nil {
		// What the briefs say since the last briefing, told once: only a real run moves the mark, not a look at the facts.
		run.Facts.Briefs, run.Facts.BriefsDone = r.tasks.TaskBriefDigest(ctx, now, run.Facts.Loc, true)
		// The week's block, on its first briefing; it is marked told when the run ends normally (Reached).
		run.Facts.Setup = r.tasks.SetupFor(ctx, now, run.Facts.Loc, run.Facts.Lang, run.Facts.Web)
	}
	r.SetTaskRun(task.ID, run)
	return r.ModelText(run.Facts)
}

// Sent: the run of task id went into conversation session; its facts are said when that conversation's turn ends, and
// are written into the conversation as a `brief` item at once (feed.go), whether or not a Lark card follows. The retry
// of a run that failed (retried) adds no item: the conversation has the first one, and the retry's facts are the card's
// and the model's.
func (r *runner) Sent(scheduled modapi.BriefScheduleRun) {
	run := r.attach(scheduled.RefID, scheduled.SessionID)
	if run == nil {
		return
	}
	if !scheduled.Retried && !run.FallbackOnly {
		r.writeFeed(scheduled, run.Label, run.Facts)
	}
}

// attach moves the run of task id from the tasks to conversation session, nil when there is none.
func (r *runner) attach(id, session string) *modapi.BriefRun {
	b := &r.book
	b.mu.Lock()
	defer b.mu.Unlock()
	run := b.byTask[id]
	if run == nil || session == "" {
		return nil
	}
	delete(b.byTask, id)
	if b.bySession == nil {
		b.bySession = map[string]*modapi.BriefRun{}
	}
	for k, existing := range b.bySession { // the old ones go
		if time.Since(existing.Facts.At) > Keep {
			delete(b.bySession, k)
		}
	}
	b.bySession[session] = run
	return run
}

// of is the facts of the run that conversation session is on, nil when it is on none (or the run is too old).
func (r *runner) of(session string) *modapi.BriefRun {
	b := &r.book
	b.mu.Lock()
	defer b.mu.Unlock()
	run := b.bySession[session]
	if run == nil || run.Facts == nil || time.Since(run.Facts.At) > Keep {
		return nil
	}
	return run
}

// HasSession says whether session currently holds an unexpired briefing run.
func (r *runner) HasSession(session string) bool { return r.of(session) != nil }

// SessionRun returns the unexpired retained run for session, if any.
func (r *runner) SessionRun(session string) *modapi.BriefRun { return r.of(session) }

// SetSessionRun stores run under session (used by integration tests).
func (r *runner) SetSessionRun(session string, run *modapi.BriefRun) {
	b := &r.book
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.bySession == nil {
		b.bySession = map[string]*modapi.BriefRun{}
	}
	if run == nil {
		delete(b.bySession, session)
		return
	}
	b.bySession[session] = run
}

// SetTaskRun stores run under taskID.
func (r *runner) SetTaskRun(taskID string, run *modapi.BriefRun) {
	b := &r.book
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.byTask == nil {
		b.byTask = map[string]*modapi.BriefRun{}
	}
	if run == nil {
		delete(b.byTask, taskID)
		return
	}
	b.byTask[taskID] = run
}

// TaskRunCount reports how many pending by-task snapshots are retained.
func (r *runner) TaskRunCount() int {
	b := &r.book
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.byTask)
}

// ofTask is the snapshot after Start/StartReport but before Sent attaches it to a conversation. Model resolution and
// conversation-start failures use it: there is no new session to look up yet.
func (r *runner) ofTask(id string) *modapi.BriefRun {
	b := &r.book
	b.mu.Lock()
	defer b.mu.Unlock()
	run := b.byTask[id]
	if run == nil || run.Facts == nil || time.Since(run.Facts.At) > Keep {
		return nil
	}
	return run
}

func (r *runner) stripContext(s string) string {
	if r.prompts != nil {
		return r.prompts.StripContext(s)
	}
	return s
}

// Claim takes the facts for the routine's own successful turn once. The second result says this retained turn already
// owns its briefing send, so a repeated completion must not fall back to an ordinary mirrored card. Other turns, failed
// runs and expired unclaimed facts keep the ordinary answer path. Report snapshots are fallback-only.
func (r *runner) Claim(session, asked, turn string, cut bool) (*modapi.BriefRun, bool) {
	b := &r.book
	b.mu.Lock()
	defer b.mu.Unlock()
	run := b.bySession[session]
	if run == nil {
		return nil, false
	}
	if run.MirrorTurn != "" {
		return nil, run.MirrorTurn == turn
	}
	if run.FallbackOnly || cut || run.Facts == nil || time.Since(run.Facts.At) > Keep ||
		!strings.HasPrefix(strings.TrimSpace(r.stripContext(asked)), "⏰") {
		return nil, false
	}
	run.MirrorTurn = turn
	return run, false
}

// Grace is the minutes remindAcceptance waits after briefingClock (start, in minutes of the day) on the day of local:
// Grace when the morning routine has facts on, is due that day and its card can go out (the Lark bot is on with its
// pushes), else none; never past the end of the day.
func (r *runner) Grace(ctx context.Context, local time.Time, start int) int {
	if r.tasks == nil || r.schedule == nil || !r.tasks.FeishuPushesOn(ctx) {
		return 0
	}
	day := int(local.Weekday()) // the weekdays of a task are 1 = Monday … 7 = Sunday
	if day == 0 {
		day = 7
	}
	if !r.schedule.MorningFactsDue(ctx, day) {
		return 0
	}
	return max(0, min(Grace, 24*60-1-start))
}

// ModelText is the facts as the model is given them (prompts assistant/routine_facts): the card's rows plus the
// model-only follow-ups, briefs and optional day recap.
func (r *runner) ModelText(f *modapi.BriefSnapshot) string {
	if f == nil || r.prompts == nil {
		return ""
	}
	b := modapi.BriefFacts{
		Hours:          int(Window / time.Hour),
		WaitingTotal:   len(f.Waiting),
		DoneTotal:      f.DoneTotal,
		FailedTotal:    f.FailedTotal,
		AcceptTotal:    len(f.Accept),
		LaterTotal:     f.LaterTotal,
		WaitingUnknown: f.WaitingUnknown,
		Card:           f.Card,
	}
	for _, row := range f.Waiting {
		if len(b.Waiting) < ModelRows {
			b.Waiting = append(b.Waiting, r.prompts.BoardRowPlain(f.Lang, len(b.Waiting)+1, row))
		}
	}
	for _, d := range f.Done {
		b.Done = append(b.Done, clip(oneLine(d.Title), 60))
	}
	for _, x := range f.Failed {
		b.Failed = append(b.Failed, modapi.BriefFailed{Title: clip(oneLine(x.Title), 60), Why: x.Why, Run: x.Kind == "routine", Skipped: x.Skipped})
	}
	b.NorthStar = f.NorthStarLines
	b.ConfirmationSummary = r.confirmationSummary(f.Lang, f.Confirmations)
	b.Confirmations = f.ConfirmationLines
	b.ProgressTotal, b.Leaders = f.ProgressTotal, f.Leaders
	b.Directions, b.DisabledRoutines = f.Directions, f.DisabledRoutines
	b.InboxWaiting, b.InboxDue = f.InboxWaiting, f.InboxDue
	b.Briefs, b.BriefsDone, b.Day = f.Briefs, f.BriefsDone, f.Day
	b.Setup = f.Setup.Lines()
	b.Captures = f.Captures
	if len(f.DeferredSupervision) > 0 {
		b.DeferredSupervisionHeading = r.prompts.Translate(f.Lang, "assistant-briefing.supervision_deferred", nil)
		for _, row := range f.DeferredSupervision {
			b.DeferredSupervision = append(b.DeferredSupervision, row.Title+" — "+row.Reason)
		}
	}
	for _, p := range f.Progress {
		b.Progress = append(b.Progress, modapi.BriefProgress{Title: clip(oneLine(p.Title), 60), Stage: p.Stage})
	}
	for _, row := range f.Accept {
		if len(b.Accept) < ModelRows {
			b.Accept = append(b.Accept, modapi.BriefAccept{Line: r.prompts.BoardRowPlain(f.Lang, len(b.Accept)+1, row), Changed: f.Changed[row.ID]})
		}
	}
	return r.prompts.RenderBriefFacts(b)
}

func (r *runner) confirmationSummary(lang string, c modapi.BriefConfirmations) string {
	if r.prompts == nil {
		return c.Summary
	}
	if c.Unknown {
		return r.prompts.Translate(lang, "assistant-confirmations.unknown", nil)
	}
	return r.prompts.Translate(lang, "assistant-confirmations.summary", map[string]any{"user": c.UserNotified, "auto": c.AutoAuthorized})
}

// CountsIn is the counts the push and the chat list say, in lang: "waiting for you 3 · not through 1 · to verify 2".
func (r *runner) CountsIn(f *modapi.BriefSnapshot, lang string) string {
	if f == nil || r.prompts == nil {
		return ""
	}
	var parts []string
	add := func(key string, n int) {
		if n > 0 {
			parts = append(parts, r.prompts.Translate(lang, "assistant-briefing."+key, map[string]any{"n": n}))
		}
	}
	if !f.WaitingUnknown {
		add("count_waiting", len(f.Waiting))
	}
	add("count_failed", f.FailedTotal)
	add("count_done", f.DoneTotal)
	add("count_accept", len(f.Accept))
	add("count_later", f.LaterTotal)
	if len(parts) == 0 {
		return r.prompts.Translate(lang, "assistant-briefing.count_none", nil)
	}
	return strings.Join(parts, r.prompts.Translate(lang, "assistant-briefing.count_sep", nil))
}

// Push is the text of the finished push of the routine run that conversation session is on: the counts Persona worked
// out, "" when it is on none or its counts were said already. report: the turn is the one a scheduled run started, not
// a message the owner sent into the conversation later. With that push the briefing has reached the owner: the
// decisions that waited for one are no longer held (delivery.MarkListed).
func (r *runner) Push(session, lang string, report bool) string {
	run := r.of(session)
	b := &r.book
	b.mu.Lock()
	if !report || run == nil || run.FallbackOnly || run.Pushed {
		b.mu.Unlock()
		return ""
	}
	run.Pushed = true
	b.mu.Unlock()
	if r.delivery != nil {
		r.delivery.MarkListed(run.Facts)
	}
	return r.CountsIn(run.Facts, lang)
}

// Reached: the turn of the routine run that conversation session is on ended normally (watchScheduledRun). Its `brief`
// item, written when the run was sent, is in the conversation with the week's setup block, and the model was given the
// block's lines: the block is told (setupTold).
func (r *runner) Reached(session string) {
	if run := r.of(session); run != nil && r.tasks != nil {
		r.tasks.SetupTold(run.Facts.Setup, run.Facts.At)
	}
}

// ---- pure text and calendar helpers (matching internal/textfmt and assistant/today.go) ----

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i] + "…"
		}
		count++
	}
	if utf8.RuneCountInString(s) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

func dayStart(now time.Time) time.Time {
	y, m, d := now.Date()
	t := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	if t.Day() != d {
		_, end := t.ZoneBounds()
		return end
	}
	return t
}
