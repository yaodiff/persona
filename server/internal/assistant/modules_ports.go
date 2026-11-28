package assistant

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/modapi"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/wireids"
)

// modulePorts wires the narrow core ports (modapi.Ports) this Service serves to loaded modules.
func (s *Service) modulePorts() modapi.Ports {
	return modapi.Ports{
		Tasks:    briefTaskPort{s: s},
		Todos:    briefTodoPort{s: s},
		Inbox:    briefInboxPort{s: s},
		Later:    briefLaterPort{s: s},
		Schedule: briefSchedulePort{s: s},
		Feed:     briefFeedPort{s: s},
		Delivery: briefDeliveryPort{s: s},
		DayRecap: briefDayRecapPort{s: s},
		Prompts:  briefPromptPort{},
	}
}

func toBriefBoardRows(rows []boardRow) []modapi.BriefBoardRow {
	out := make([]modapi.BriefBoardRow, len(rows))
	for i, r := range rows {
		out[i] = modapi.BriefBoardRow{
			Blocking: r.blocking, Waited: r.waited, ID: r.id, Summary: r.summary,
			From: r.from, Link: r.link, Nudged: r.nudged, Detail: r.detail, EvidenceLink: r.evidenceLink,
			Machine: r.machine, Session: r.session, Asked: r.asked,
		}
	}
	return out
}

func fromBriefBoardRow(r modapi.BriefBoardRow) boardRow {
	return boardRow{
		blocking: r.Blocking, waited: r.Waited, id: r.ID, summary: r.Summary,
		from: r.From, link: r.Link, nudged: r.Nudged,
		machine: r.Machine, session: r.Session, asked: r.Asked,
	}
}

// ---- TaskPort ----

type briefTaskPort struct{ s *Service }

func (p briefTaskPort) BotLang(ctx context.Context) string          { return string(p.s.botLang(ctx)) }
func (p briefTaskPort) WebBase(ctx context.Context) string          { return p.s.webBase(ctx) }
func (p briefTaskPort) Location(ctx context.Context) *time.Location { return p.s.location(ctx) }
func (p briefTaskPort) FleetReady() bool                            { return p.s.fleetReady() }

func (p briefTaskPort) FeishuPushesOn(ctx context.Context) bool {
	st, err := p.s.chat().Status(ctx)
	ready := err == nil && st.Ready
	p.s.larkModule().WorkbenchOn(st.Features, ready)
	return ready && st.Features.Pushes
}

func (p briefTaskPort) BoardRows(ctx context.Context, lang, web string, now time.Time) []modapi.BriefBoardRow {
	return toBriefBoardRows(p.s.boardRows(ctx, i18n.Lang(lang), web, now))
}

func (p briefTaskPort) AskCopy(todoID, lang string) (modapi.BriefAsk, bool) {
	a, ok := p.s.askCopy(todoID)
	if !ok {
		return modapi.BriefAsk{}, false
	}
	l := i18n.Lang(lang)
	var delivery *TodoDelivery
	if t, ok := p.s.todoNow(todoID); ok {
		delivery = currentDelivery(t)
	}
	return modapi.BriefAsk{
		Changed:       a.Changed,
		Conclusion:    a.Conclusion,
		Outcome:       a.Outcome,
		Recommend:     a.Recommend,
		Verdict:       verdictText(l, &a, delivery),
		RecommendText: recommendText(l, &a),
		Decide:        decideAsk(&a),
	}, true
}

func (p briefTaskPort) TodayFinished(ctx context.Context, since time.Time, loc *time.Location) []modapi.BriefDone {
	rows := p.s.todayFinished(ctx, "*", since, loc)
	out := make([]modapi.BriefDone, len(rows))
	for i, d := range rows {
		out[i] = modapi.BriefDone{ID: d.ID, Title: d.Title, At: d.At}
	}
	return out
}

func (p briefTaskPort) TodayFailures(ctx context.Context, since time.Time, loc *time.Location) ([]modapi.BriefFailure, int) {
	rows, total := p.s.todayFailures(ctx, "*", since, loc)
	out := make([]modapi.BriefFailure, len(rows))
	for i, x := range rows {
		out[i] = modapi.BriefFailure{
			Kind: x.Kind, ID: x.ID, Ref: x.Ref, Title: x.Title, At: x.At, Why: x.Why, Skipped: x.Skipped,
		}
	}
	return out, total
}

func (p briefTaskPort) NorthStar(ctx context.Context, now time.Time, loc *time.Location, lang string) modapi.BriefNorthStar {
	ns := p.s.briefNorthStarAt(ctx, now, loc)
	lines := ns.lines(i18n.Lang(lang))
	b := ns.body(lines)
	_, hasStore := p.s.store().(northStarStore)
	return modapi.BriefNorthStar{
		Lines:                  b.Lines,
		Interruptions:          b.Interruptions,
		InterruptionsYesterday: b.InterruptionsYesterday,
		Partial:                b.Partial,
		Availability:           b.Availability,
		AvailabilityYesterday:  b.AvailabilityYesterday,
		Attempts:               b.Attempts,
		OK:                     b.OK,
		HasStore:               hasStore,
	}
}

func (p briefTaskPort) Confirmations(ctx context.Context, now time.Time, loc *time.Location, lang string) modapi.BriefConfirmations {
	l := i18n.Lang(lang)
	c := p.s.briefConfirmationsAt(ctx, now, loc)
	sources := make([]modapi.BriefConfirmationCount, len(c.Sources))
	for i, row := range c.Sources {
		sources[i] = modapi.BriefConfirmationCount{
			MachineID: row.MachineID, SessionID: row.SessionID, TodoID: row.TodoID,
			Kind: row.Kind, SessionTitle: row.SessionTitle,
			UserNotified: row.UserNotified, AutoAuthorized: row.AutoAuthorized,
		}
	}
	return modapi.BriefConfirmations{
		Unknown:        c.Unknown,
		Since:          c.Since,
		UserNotified:   c.UserNotified,
		AutoAuthorized: c.AutoAuthorized,
		Sources:        sources,
		Summary:        c.summary(l),
		Lines:          p.s.confirmationLines(l, c),
	}
}

func (p briefTaskPort) Leaders(ctx context.Context, now time.Time) []modapi.BriefLeader {
	return p.s.briefLeaders(ctx, now)
}

func (p briefTaskPort) Directions(ctx context.Context, now time.Time, lang, web string) ([]modapi.BriefDirection, []modapi.BriefDisabledRoutine) {
	return p.s.briefDirections(ctx, now, i18n.Lang(lang), web)
}

func (p briefTaskPort) Captures(ctx context.Context, lang string, since time.Time) []string {
	return p.s.captureBriefLines(ctx, i18n.Lang(lang), since)
}

func (p briefTaskPort) TaskBriefDigest(ctx context.Context, now time.Time, loc *time.Location, markTold bool) (open, done []modapi.BriefTask) {
	open, done = p.s.briefDigest(ctx, now, p.s.briefDigestSince(ctx, now), loc)
	if markTold {
		p.s.briefDigestTold(ctx, now)
	}
	return open, done
}

func (p briefTaskPort) SetupFor(ctx context.Context, now time.Time, loc *time.Location, lang, web string) *modapi.BriefSetup {
	return toBriefSetup(p.s.setupFor(ctx, now, loc, i18n.Lang(lang), web))
}

func (p briefTaskPort) SetupTold(setup *modapi.BriefSetup, at time.Time) {
	p.s.setupTold(fromBriefSetup(setup), at)
}

func toBriefSetup(b *setupBlock) *modapi.BriefSetup {
	if b == nil {
		return nil
	}
	out := &modapi.BriefSetup{Week: b.Week}
	if len(b.Suggestions) > 0 {
		out.Suggestions = make([]modapi.BriefSetupSuggestion, len(b.Suggestions))
		for i, sg := range b.Suggestions {
			out.Suggestions[i] = modapi.BriefSetupSuggestion{
				ID: sg.ID, Setting: sg.Setting, Count: sg.Count, Minutes: sg.Minutes, Text: sg.Text, Link: sg.Link,
			}
		}
	}
	return out
}

func fromBriefSetup(b *modapi.BriefSetup) *setupBlock {
	if b == nil {
		return nil
	}
	out := &setupBlock{Week: b.Week}
	if len(b.Suggestions) > 0 {
		out.Suggestions = make([]setupSuggestion, len(b.Suggestions))
		for i, sg := range b.Suggestions {
			out.Suggestions[i] = setupSuggestion{
				ID: sg.ID, Setting: sg.Setting, Count: sg.Count, Minutes: sg.Minutes, Text: sg.Text, Link: sg.Link,
			}
		}
	}
	return out
}

// ---- TodoPort ----

type briefTodoPort struct{ s *Service }

func (p briefTodoPort) OpenProgressTodos(ctx context.Context) []modapi.BriefTodoProgress {
	var out []modapi.BriefTodoProgress
	lists := machineSessions{}
	for _, t := range p.s.todosOf(ctx, "*") {
		if t.DoneAt != nil {
			continue
		}
		st := taskState(t, p.s.taskAgentsIn(ctx, t, lists))
		out = append(out, modapi.BriefTodoProgress{
			ID:        t.ID,
			Title:     t.Title,
			Stage:     t.Stage,
			StageKind: t.StageKind,
			CreatedAt: t.CreatedAt,
			Active:    st == "working" || st == "waiting", // under way either way (#3274)
		})
	}
	return out
}

func (p briefTodoPort) TodoTitle(id string) (string, bool) {
	t, ok := p.s.todoNow(id)
	if !ok {
		return "", false
	}
	return t.Title, true
}

func (p briefTodoPort) DeferredSupervision(ctx context.Context, at time.Time) []modapi.BriefDeferredSupervision {
	st, ok := p.s.store().(supervisionDecisionStore)
	if !ok {
		return nil
	}
	rows, err := st.DeferredSupervision(ctx, at.Add(-24*time.Hour), at)
	if err != nil {
		p.s.logf("briefing: could not read deferred supervision: %v", err)
		return nil
	}
	var out []modapi.BriefDeferredSupervision
	for _, row := range rows {
		out = append(out, modapi.BriefDeferredSupervision{ID: modapi.BriefSupervisionDecisionID(row.ID), TodoID: wireids.TodoID(row.TodoID), Title: row.Title, Reason: row.Reason})
	}
	return out
}

func (p briefTodoPort) SupervisionBriefed(ctx context.Context, ids []string, at time.Time) {
	if st, ok := p.s.store().(supervisionDecisionStore); ok {
		if err := st.SupervisionBriefed(ctx, ids, at); err != nil {
			p.s.logf("briefing: could not acknowledge deferred supervision: %v", err)
		}
	}
}

func (p briefTodoPort) StalePRFollowups(ctx context.Context, at time.Time, lang string) []modapi.BriefPRFollowup {
	fups := p.s.stalePRFollowups(ctx, at)
	if len(fups) == 0 {
		return nil
	}
	_ = p.s.ensureTodosLoaded(ctx)
	l := i18n.Lang(lang)
	out := make([]modapi.BriefPRFollowup, 0, len(fups))
	for _, fup := range fups {
		title := fup.TodoID
		if t, ok := p.s.todoNow(fup.TodoID); ok {
			title = t.Title
		}
		row := modapi.BriefPRFollowup{
			TodoID:         fup.TodoID,
			Title:          title,
			PRURL:          fup.PRURL,
			State:          fup.State,
			WatchStatus:    fup.EffectiveWatchStatus(),
			WatchReason:    fup.WatchReason,
			LastObservedAt: fup.LastObservedAt,
			WatchUpdatedAt: fup.WatchUpdatedAt,
		}
		if fup.IsUnwatched() {
			row.Note = prFollowupUnwatchedNote(l, fup)
		}
		out = append(out, row)
	}
	return out
}

// ---- InboxPort ----

type briefInboxPort struct{ s *Service }

func (p briefInboxPort) FollowingSignals(ctx context.Context) []modapi.BriefInboxSignal {
	return toBriefInboxSignals(p.s.followingInboxItems(ctx))
}

func toBriefInboxSignals(items []InboxItem) []modapi.BriefInboxSignal {
	out := make([]modapi.BriefInboxSignal, len(items))
	for i, it := range items {
		sig := modapi.BriefInboxSignal{ID: it.ID, Title: it.Title, Now: it.Now, Open: it.open()}
		if it.Handling != nil && it.Handling.Since != nil {
			t := *it.Handling.Since
			sig.WaitingSince = &t
		}
		if it.Follow != nil {
			sig.Who, sig.Next = it.Follow.Who, it.Follow.Next
			if it.Follow.CheckAt != nil {
				t := *it.Follow.CheckAt
				sig.CheckAt = &t
			}
		}
		out[i] = sig
	}
	return out
}

// ---- LaterPort ----

type briefLaterPort struct{ s *Service }

func (p briefLaterPort) Handle(ctx context.Context, id string) (bool, error) {
	return p.s.laterHandled(ctx, id, time.Now())
}

func (p briefLaterPort) Rows(ctx context.Context, lang, web string, now time.Time, limit int) ([]modapi.BriefBoardRow, int) {
	rows, total := p.s.laterRows(ctx, i18n.Lang(lang), web, now, limit)
	return toBriefBoardRows(rows), total
}

// ---- DayRecapPort ----

type briefDayRecapPort struct{ s *Service }

func (p briefDayRecapPort) TodaysRecordings(ctx context.Context, now time.Time, loc *time.Location) []modapi.BriefDayRecording {
	e, err := p.s.kbGet(ctx)
	if err != nil {
		return nil
	}
	recs := e.TodaysDayRecordings(now, loc)
	if len(recs) == 0 {
		return nil
	}
	out := make([]modapi.BriefDayRecording, len(recs))
	for i := range recs {
		rec := &recs[i]
		start, end := e.DaySpan(rec, now)
		row := modapi.BriefDayRecording{
			ID: rec.ID, Part: rec.Part, StartedAt: rec.StartedAt, Duration: rec.Duration,
			Chars: rec.Chars, RecapState: rec.RecapState, SpanStart: start, SpanEnd: end,
		}
		if rec.Coverage != nil {
			row.HasCoverage = true
			row.CoverageTo = rec.Coverage.To
			row.Gaps = make([]modapi.BriefDayCoverageGap, len(rec.Coverage.Gaps))
			for j, g := range rec.Coverage.Gaps {
				row.Gaps[j] = modapi.BriefDayCoverageGap{From: g.From, To: g.To, Reason: g.Reason}
			}
		}
		if rc, err := e.ReadDayRecap(rec.ID); err == nil {
			dr := &modapi.BriefDayRecap{
				GeneratedAt: rc.GeneratedAt, CoversTo: rc.Covers.To,
				Final: rc.Final, Partial: rc.Partial, Summary: rc.Summary,
				Timeline: make([]modapi.BriefDayTimelineEntry, len(rc.Timeline)),
				Actions:  make([]modapi.BriefDayRecapAction, len(rc.Actions)),
			}
			for j, en := range rc.Timeline {
				dr.Timeline[j] = modapi.BriefDayTimelineEntry{
					From: en.From, To: en.To, Meeting: en.Kind == kb.RecapMeeting, Title: en.Title,
				}
			}
			for j, a := range rc.Actions {
				dr.Actions[j] = modapi.BriefDayRecapAction{
					At: a.At, Who: a.Who, What: a.What, TodoID: a.TodoID,
				}
			}
			row.Recap = dr
		}
		out[i] = row
	}
	return out
}

// ---- SchedulePort ----

type briefSchedulePort struct{ s *Service }

func (p briefSchedulePort) MorningFactsDue(ctx context.Context, weekday int) bool {
	sc, err := p.s.lockedSchedule(ctx)
	if err != nil {
		return false
	}
	defer sc.mu.Unlock()
	for _, t := range sc.tasks {
		if t.ID == "morning" && t.Enabled && t.Repeat == repeatDaily && t.Facts && hhmm.MatchString(t.Time) &&
			(len(t.Weekdays) == 0 || slices.Contains(t.Weekdays, weekday)) {
			return true
		}
	}
	return false
}

// ClaimFallbackReceipt persists the receipt before any channel is asked to send. A receipt write that fails fails
// closed: no unremembered card goes out, and the next attempt can try the durable claim again. The map is separate from
// the 200-entry display history, so ordinary run trimming cannot forget it.
func (p briefSchedulePort) ClaimFallbackReceipt(ctx context.Context, key string) bool {
	s := p.s
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return false
	}
	defer sc.mu.Unlock()
	if _, ok := sc.briefFallbacks[key]; ok {
		return false
	}
	next := maps.Clone(sc.briefFallbacks)
	if next == nil {
		next = map[string]time.Time{}
	}
	next[key] = time.Now().UTC()
	if st := s.store(); st != nil {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := st.PutSetting(wctx, briefFallbacksKey, next); err != nil {
			s.logf("briefing fallback receipt %s: %v", key, err)
			return false
		}
	}
	sc.briefFallbacks = next
	return true
}

func (p briefSchedulePort) ResolveFallbackTarget(ctx context.Context, task modapi.BriefTaskInfo, run modapi.BriefScheduleRun) (sessionID, machineID, machineName string, ok bool) {
	s := p.s
	sessionID = run.SessionID
	if sessionID == "" {
		sessionID = task.SessionID
	}
	if sessionID == "" {
		sessionID = task.RunSessionID
	}
	cfg := s.settings(ctx)
	if sessionID == "" {
		sessionID = cfg.Main
	}
	machine := run.MachineID
	if machine == "" {
		machine = task.MachineID
	}
	if machine == "" {
		machine = cfg.MachineID
	}
	m, found := s.device(ctx, machine)
	if !found {
		return "", "", "", false
	}
	return sessionID, m.ID, m.Name, true
}

func (p briefSchedulePort) RetryNotStarted(taskID string, failed modapi.BriefScheduleRun) {
	s := p.s
	delay := runRetryDelay
	if s.runRetry > 0 {
		delay = s.runRetry
	}
	run := ScheduleRun{
		ID: failed.ID, RefID: failed.RefID, SessionID: failed.SessionID, MachineID: failed.MachineID,
		Due: failed.Due, Retried: failed.Retried, Manual: failed.Manual,
	}
	go func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		<-timer.C
		rctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cur, ok := s.retryStillDue(rctx, taskID, run)
		cancel()
		if !ok {
			return
		}
		if _, err := s.runTask(cur, failed.Due, time.Now(), false, true); err != nil {
			s.logf("task %s briefing retry: %v", taskID, err)
		}
	}()
}

// ---- FeedPort ----

type briefFeedPort struct{ s *Service }

func (p briefFeedPort) Append(ctx context.Context, session, kind, ref string, body any) error {
	_, err := p.s.appendFeed(ctx, session, kind, ref, body)
	return err
}

func (p briefFeedPort) AppendOnce(ctx context.Context, id, session, kind, ref string, body any) error {
	_, err := p.s.appendFeedOnce(ctx, id, session, kind, ref, body)
	return err
}

// ---- DeliveryPort ----

type briefDeliveryPort struct{ s *Service }

func (p briefDeliveryPort) SendFallback(ctx context.Context, req modapi.BriefFallbackSend) {
	s := p.s
	l := i18n.Lang(req.Facts.Lang)
	openLabel, openURL := s.openLink(ctx, req.Device.ID, req.Run.SessionID)
	cards, plain, shown := s.scheduleCards(req.Facts, req.Label, "", req.Header, openLabel, openURL)
	st, err := s.chat().Status(ctx)
	ready := err == nil && st.Ready
	s.larkModule().WorkbenchOn(st.Features, ready)
	_, delivery := s.taskDeliveryForConversation(ctx, req.Device.ID, req.Run.SessionID)
	if ready && (st.Features.Pushes || isFeishuThreadDelivery(delivery)) {
		// The fallback has no assistant answer of its own. Track the failed turn that is actually last, so a transient
		// Feishu failure can retry, but a newer answer still makes this old occurrence too late to post.
		_, answer, turn, _ := s.channelLastTurnOf(ctx, req.Device.ID, req.Run.SessionID)
		to := s.larkMirror(req.Device.ID, req.Run.SessionID, req.Label, false, answer, turn, delivery)
		s.postBriefMirror(ctx, to, l, cards, "f"+req.ID, func() { s.briefAsked(req.Facts, shown) })
	}

	// deliver preserves this stable notification ID (push/pushResult would replace it with a random one).
	s.noteAsked(req.Run.SessionID)
	s.deliverCard(cardBriefFallback, Notification{
		ID:        "ntf_brief_fallback_" + req.ID,
		Kind:      assistantwire.NotifyDone,
		DeviceID:  req.Device.ID,
		Subtitle:  req.Device.Name,
		SessionID: req.Run.SessionID,
		Title:     req.Label,
		Body:      plain,
	})
	s.briefListed(req.Facts)
}

func (p briefDeliveryPort) MarkListed(f *modapi.BriefSnapshot) {
	p.s.briefListed(f)
}

// ---- PromptPort ----

type briefPromptPort struct{}

func (briefPromptPort) RenderBriefFacts(b modapi.BriefFacts) string {
	return prompts.RenderBriefFacts(b)
}
func (briefPromptPort) RenderNorthStarFacts(lines []string) string {
	return prompts.RenderNorthStarFacts(lines)
}
func (briefPromptPort) BriefDayRunes(day *modapi.BriefDay) int { return prompts.BriefDayRunes(day) }
func (briefPromptPort) StripContext(text string) string        { return stripContext(text) }

func (briefPromptPort) BoardRowMarkdown(lang string, n int, row modapi.BriefBoardRow) string {
	return boardRowMarkdown(i18n.Lang(lang), n, fromBriefBoardRow(row))
}

func (briefPromptPort) OpenStageBar(lang, stage string) string {
	return stageBar(i18n.Lang(lang), stage, false)
}

func (briefPromptPort) BoardRowPlain(lang string, n int, row modapi.BriefBoardRow) string {
	l := i18n.Lang(lang)
	return fmt.Sprintf("%d. %s · %s · %s", n, row.Summary, i18n.T(l, "assistant-feishu_cards.board_waited", i18n.Args{"wait": boardWait(l, row.Waited)}), row.From)
}

func (briefPromptPort) Translate(lang, key string, args map[string]any) string {
	if args == nil {
		return i18n.T(i18n.Lang(lang), key)
	}
	return i18n.T(i18n.Lang(lang), key, i18n.Args(args))
}
