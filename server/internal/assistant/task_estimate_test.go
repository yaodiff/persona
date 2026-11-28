package assistant

import (
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func firstPlanTask(t *testing.T, s *Service) PlanTask {
	t.Helper()
	s.tasks.mu.Lock()
	defer s.tasks.mu.Unlock()
	if len(s.tasks.plans) != 1 {
		t.Fatalf("plans = %d, want 1", len(s.tasks.plans))
	}
	for _, p := range s.tasks.plans {
		if len(p.Tasks) != 1 {
			t.Fatalf("plan tasks = %d, want 1", len(p.Tasks))
		}
		return p.Tasks[0]
	}
	return PlanTask{}
}

func briefEventsOf(b TaskBrief, kind string) []BriefEvent {
	var out []BriefEvent
	for _, e := range b.Log {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

func startEstimateTask(t *testing.T, st Store, over map[string]any) (*Service, *fakeHub, Todo, TaskBrief) {
	t.Helper()
	s, f, _ := briefServiceOn(t, st, "please fix the login crash before Friday")
	if got, isErr := tool(t, s, "task_plan", briefTask(over)); isErr {
		t.Fatalf("task_plan: %s", got)
	}
	waitFor(t, "the agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	brief, ok := s.briefOfTodo(todo.ID)
	if !ok {
		t.Fatal("the started task has no brief")
	}
	return s, f, todo, brief
}

func TestTaskPlanAutomaticEstimateSource(t *testing.T) {
	cases := []struct {
		name       string
		over       map[string]any
		want       int
		wantSource string
	}{
		{name: "omitted uses documented default", over: map[string]any{"expect_minutes": nil}, want: defaultTaskEstimateMinutes, wantSource: briefEstimateSystemDefault},
		{name: "zero uses documented default", over: map[string]any{"expect_minutes": 0}, want: defaultTaskEstimateMinutes, wantSource: briefEstimateSystemDefault},
		{name: "same number supplied explicitly is an override", over: map[string]any{"expect_minutes": defaultTaskEstimateMinutes}, want: defaultTaskEstimateMinutes, wantSource: briefEstimateOverride},
		{name: "positive value is an override", over: map[string]any{"expect_minutes": 45}, want: 45, wantSource: briefEstimateOverride},
		{name: "overrides cap at one day", over: map[string]any{"expect_minutes": 24*60 + 1}, want: 24 * 60, wantSource: briefEstimateOverride},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := briefService(t, "please fix the login crash")
			if got, isErr := tool(t, s, "task_plan", briefTask(tc.over)); isErr {
				t.Fatalf("task_plan: %s", got)
			}
			planTask := firstPlanTask(t, s)
			if planTask.ExpectMinutes != tc.want || planTask.EstimateSource != tc.wantSource {
				t.Fatalf("plan estimate = %d/%q, want %d/%q", planTask.ExpectMinutes, planTask.EstimateSource, tc.want, tc.wantSource)
			}
			if planTask.Brief == nil || planTask.Brief.ExpectMinutes != tc.want || planTask.Brief.EstimateSource != tc.wantSource {
				t.Fatalf("brief estimate = %+v", planTask.Brief)
			}
		})
	}
	t.Run("negative override is refused before planning", func(t *testing.T) {
		s, _, _ := briefService(t, "please fix the login crash")
		if got, isErr := tool(t, s, "task_plan", briefTask(map[string]any{"expect_minutes": -1})); !isErr ||
			!strings.Contains(got, "expect_minutes") {
			t.Fatalf("negative estimate: %s, error=%v", got, isErr)
		}
		if len(s.tasks.plans) != 0 {
			t.Fatalf("a plan was created for an invalid estimate: %d", len(s.tasks.plans))
		}
	})
}

func TestAutomaticEstimateTracksPlanEdits(t *testing.T) {
	cases := []struct {
		name        string
		minutes     int
		source      string
		wantMinutes int
		wantSource  string
	}{
		{name: "default changed becomes override", minutes: 90, source: briefEstimateSystemDefault, wantMinutes: 90, wantSource: briefEstimateOverride},
		{name: "cleared override returns to default", minutes: 0, source: briefEstimateOverride, wantMinutes: defaultTaskEstimateMinutes, wantSource: briefEstimateSystemDefault},
		{name: "explicitly choosing default stays override", minutes: defaultTaskEstimateMinutes, source: briefEstimateOverride, wantMinutes: defaultTaskEstimateMinutes, wantSource: briefEstimateOverride},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			minutes, source := automaticEstimate(tc.minutes, tc.source)
			if minutes != tc.wantMinutes || source != tc.wantSource {
				t.Fatalf("automaticEstimate = %d/%q, want %d/%q", minutes, source, tc.wantMinutes, tc.wantSource)
			}
		})
	}
}

func TestManualAndLegacyTasksStayOutsideAutomaticEstimate(t *testing.T) {
	s, f := newQuietService(t)
	ctx := t.Context()
	f.setSessions("box", proto.Session{SessionID: "s1abcdef", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: time.Now()})
	if _, err := runJSON(t, s, "conv1", "agent_supervise", map[string]any{"machine": "box", "session_id": "s1abcdef",
		"done_when": []string{"manual work is done"}, "expect_minutes": 30}); err != nil {
		t.Fatal(err)
	}
	manual := onlyTodo(s)
	brief, ok := s.briefOfTodo(manual.ID)
	if !ok || brief.EstimateSourceNow != "" || !brief.EstimateClockAt.IsZero() || !brief.EstimateDueAt.IsZero() {
		t.Fatalf("manual brief was admitted to automatic estimates: %+v", brief)
	}
	if _, err := s.reportNow(ctx, manual, "box", "s1abcdef", upReportDone, "finished manually", nil, 0, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := briefEventsOf(s.mustBrief(t, manual.ID), briefEventCompletion); len(got) != 0 {
		t.Fatalf("manual work produced calibration history: %+v", got)
	}
	if got, err := runJSON(t, s, "conv1", "agent_supervise", map[string]any{"machine": "box", "session_id": "s1abcdef",
		"done_when": []string{"manual work is done", "extra check"}, "expect_minutes": 45}); err != nil {
		t.Fatalf("manual rescope: %s (%v)", got, err)
	}
	if got := s.mustBrief(t, manual.ID); got.EstimateSourceNow != "" || !got.EstimateClockAt.IsZero() {
		t.Fatalf("a manual rescope promoted the task: %+v", got)
	}

	// A new task_plan continuation is an automatic dispatch, even when it carries on a manual/legacy brief.
	tasks := []PlanTask{{Continues: manual.ID, ContinuesWhy: "take it over through task_plan", Readonly: true}}
	if err := s.planBriefs(ctx, "", "conv1", tasks); err != nil {
		t.Fatal(err)
	}
	pt := &tasks[0]
	if pt.ExpectMinutes != defaultTaskEstimateMinutes || pt.EstimateSource != briefEstimateSystemDefault {
		t.Fatalf("continuation estimate = %d/%q", pt.ExpectMinutes, pt.EstimateSource)
	}
	s.briefStarted(ctx, &TaskPlan{ID: "plan_continuation", Conversation: "conv1"}, pt, manual)
	brief = s.mustBrief(t, manual.ID)
	if brief.EstimateSourceNow != briefEstimateSystemDefault || brief.ExpectNow != defaultTaskEstimateMinutes ||
		len(briefEventsOf(brief, briefEventDispatch)) != 1 || brief.EstimateDueAt != brief.EstimateClockAt.Add(defaultTaskEstimateMinutes*time.Minute) {
		t.Fatalf("task_plan continuation did not arm the automatic clock: %+v", brief)
	}
}

func TestEstimateDueComesOnceRearmsOnlyForEstimateChange(t *testing.T) {
	s, _, todo, initial := startEstimateTask(t, nil, map[string]any{"expect_minutes": 30})
	ctx := t.Context()
	dueAt := initial.EstimateDueAt
	pending := func() (superviseSignal, bool) {
		s.supervise.mu.Lock()
		defer s.supervise.mu.Unlock()
		g, ok := s.supervise.pending[todo.ID+"|"+sigEstimateDue]
		return g, ok
	}
	s.briefClock(ctx, todo, dueAt.Add(-time.Nanosecond))
	if _, ok := pending(); ok {
		t.Fatal("estimate_due fired before its deadline")
	}
	s.briefClock(ctx, todo, dueAt)
	g, ok := pending()
	if !ok || !g.urgent() || g.Key != estimateDueKey(initial) {
		t.Fatalf("estimate_due signal = %+v, %v", g, ok)
	}
	if e, ok := s.digestEntry(ctx, g, ""); !ok || e.Kind != sigEstimateDue {
		t.Fatalf("digest entry = %+v, %v", e, ok)
	}

	// A manager restart before accepted delivery loses only memory, not the durable brief: it raises the cue again.
	s.clearEstimateDue(todo.ID)
	s.briefClock(ctx, todo, dueAt.Add(time.Minute))
	g, ok = pending()
	if !ok || g.Key != estimateDueKey(initial) {
		t.Fatalf("an untold estimate cue did not survive restart: %+v %v", g, ok)
	}
	s.estimateDueTold(ctx, []superviseSignal{g})
	brief := s.mustBrief(t, todo.ID)
	if brief.EstimateDueToldAt == nil {
		t.Fatal("accepted delivery did not persist the told marker")
	}
	s.clearEstimateDue(todo.ID)
	s.briefClock(ctx, todo, dueAt.Add(2*time.Minute))
	if _, ok := pending(); ok {
		t.Fatal("the same estimate clock raised estimate_due twice")
	}

	// Changing only criteria leaves the estimate clock and its told marker alone.
	criteriaOnly := brief.Criteria
	if _, err := s.applyScope(ctx, todo, scopeChange{By: "conv1", Change: "clarify criteria only", Why: "owner asked", DoneWhen: criteriaOnly}); err != nil {
		t.Fatal(err)
	}
	brief = s.mustBrief(t, todo.ID)
	if !brief.EstimateClockAt.Equal(initial.EstimateClockAt) || brief.EstimateDueToldAt == nil {
		t.Fatalf("a criteria-only scope changed the estimate clock: %+v", brief)
	}
	s.briefClock(ctx, todo, brief.EstimateDueAt.Add(time.Minute))
	if _, ok := pending(); ok {
		t.Fatal("criteria-only scope rearmed estimate_due")
	}

	// A different estimate starts a new review clock and source becomes override.
	if _, err := s.applyScope(ctx, todo, scopeChange{By: "conv1", Change: "take more time for CI", Why: "CI queue is slow", Expect: 45}); err != nil {
		t.Fatal(err)
	}
	brief = s.mustBrief(t, todo.ID)
	if brief.ExpectNow != 45 || brief.EstimateSourceNow != briefEstimateOverride || brief.EstimateDueToldAt != nil ||
		brief.EstimateDueAt != brief.EstimateClockAt.Add(45*time.Minute) {
		t.Fatalf("estimate revision did not rearm the clock: %+v", brief)
	}
	s.briefClock(ctx, todo, brief.EstimateDueAt.Add(-time.Nanosecond))
	if _, ok := pending(); ok {
		t.Fatal("the revised estimate fired early")
	}
	s.briefClock(ctx, todo, brief.EstimateDueAt)
	g, ok = pending()
	if !ok || g.Key != estimateDueKey(brief) {
		t.Fatalf("revised estimate_due = %+v, %v", g, ok)
	}
}

func TestDoneReportSuppressesQueuedEstimateDueAndRecordsActualTime(t *testing.T) {
	s, _, todo, brief := startEstimateTask(t, nil, map[string]any{"expect_minutes": 30})
	ctx := t.Context()
	reportedAt := brief.EstimateClockAt.Add(29 * time.Minute)
	if _, err := s.reportNow(ctx, todo, todo.Agents[0].MachineID, todo.Agents[0].SessionID, upReportDone, "finished", nil, 0, 0, reportedAt); err != nil {
		t.Fatal(err)
	}
	brief = s.mustBrief(t, todo.ID)
	if brief.CompletionReported == nil || brief.ActualMinutes != 29 {
		t.Fatalf("completion = %+v actual=%d", brief.CompletionReported, brief.ActualMinutes)
	}
	if got := briefEventsOf(brief, briefEventCompletion); len(got) != 1 || got[0].Source != briefCompletionReport {
		t.Fatalf("completion events = %+v", got)
	}
	s.briefClock(ctx, todo, brief.EstimateDueAt)
	if g, ok := func() (superviseSignal, bool) {
		s.supervise.mu.Lock()
		defer s.supervise.mu.Unlock()
		g, ok := s.supervise.pending[todo.ID+"|"+sigEstimateDue]
		return g, ok
	}(); ok {
		t.Fatalf("the deadline still queued after a done report: %+v", g)
	}
	g := superviseSignal{Todo: todo.ID, Kind: sigEstimateDue, Key: estimateDueKey(brief), At: brief.EstimateDueAt}
	if _, ok := s.digestEntry(ctx, g, ""); ok {
		t.Fatal("a queued estimate_due was not revalidated against the completion event")
	}

	// The judge can run later; the earlier active report remains the single duration sample.
	if r := s.recordBriefResult(ctx, todo, nil, nil, "", reviewAccepted); r == nil {
		t.Fatal("the supervisor review did not record a brief result")
	}
	brief = s.mustBrief(t, todo.ID)
	if got := briefEventsOf(brief, briefEventCompletion); len(got) != 1 || got[0].Source != briefCompletionReport {
		t.Fatalf("a later review duplicated completion: %+v", got)
	}
}

func TestCompletionRoundsAndReviewFallback(t *testing.T) {
	t.Run("duplicate and fractional reports", func(t *testing.T) {
		s, _, todo, brief := startEstimateTask(t, nil, map[string]any{"expect_minutes": 30})
		ctx := t.Context()
		first := brief.EstimateClockAt.Add(61 * time.Second)
		if _, err := s.reportNow(ctx, todo, todo.Agents[0].MachineID, todo.Agents[0].SessionID, upReportDone, "finished", nil, 0, 0, first); err != nil {
			t.Fatal(err)
		}
		if got := s.mustBrief(t, todo.ID).ActualMinutes; got != 2 {
			t.Fatalf("actual minutes = %d, want 2", got)
		}
		if _, err := s.reportNow(ctx, todo, todo.Agents[0].MachineID, todo.Agents[0].SessionID, upReportDone, "again", nil, 0, 0, first.Add(5*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if got := briefEventsOf(s.mustBrief(t, todo.ID), briefEventCompletion); len(got) != 1 {
			t.Fatalf("duplicate completion events = %+v", got)
		}
	})
	t.Run("review and judge fallbacks each record once", func(t *testing.T) {
		for _, tc := range []struct{ name, by string }{
			{name: "supervisor review", by: ""},
			{name: "judge review", by: reviewerJudge},
		} {
			t.Run(tc.name, func(t *testing.T) {
				s, _, todo, _ := startEstimateTask(t, nil, nil)
				ctx := t.Context()
				r := s.recordBriefResult(ctx, todo, nil, nil, tc.by, reviewAccepted)
				if r == nil {
					t.Fatal("recordBriefResult returned nil")
				}
				events := briefEventsOf(s.mustBrief(t, todo.ID), briefEventCompletion)
				if len(events) != 1 || events[0].ActualMinutes < 1 {
					t.Fatalf("fallback completion = %+v", events)
				}
				wantSource := briefCompletionReview
				if tc.by == reviewerJudge {
					wantSource = briefCompletionJudge
				}
				if events[0].Source != wantSource {
					t.Fatalf("source = %q, want %q", events[0].Source, wantSource)
				}
				s.recordBriefCompletion(ctx, todo, briefCompletionReview, time.Now())
				if got := briefEventsOf(s.mustBrief(t, todo.ID), briefEventCompletion); len(got) != 1 {
					t.Fatalf("the fallback was overwritten: %+v", got)
				}
			})
		}
	})
	t.Run("reopen starts a new duration round excluding closed time", func(t *testing.T) {
		s, _, todo, brief := startEstimateTask(t, nil, nil)
		ctx := t.Context()
		firstEnd := brief.EstimateClockAt.Add(15 * time.Minute)
		s.recordBriefCompletion(ctx, todo, briefCompletionReview, firstEnd)
		reopened := firstEnd.Add(time.Hour)
		todo.ReopenedAt = &reopened
		s.recordBriefCompletion(ctx, todo, briefCompletionReview, reopened.Add(10*time.Minute))
		events := briefEventsOf(s.mustBrief(t, todo.ID), briefEventCompletion)
		if len(events) != 2 || events[0].ActualMinutes != 15 || events[1].ActualMinutes != 10 {
			t.Fatalf("reopen duration events = %+v", events)
		}
	})
}

func TestEstimateDuePromptLanguages(t *testing.T) {
	s, f, todo, brief := startEstimateTask(t, nil, map[string]any{"expect_minutes": 30})
	ctx := t.Context()
	g := superviseSignal{Todo: todo.ID, Kind: sigEstimateDue, Key: estimateDueKey(brief), At: brief.EstimateDueAt, Minutes: 30}
	for _, tc := range []struct{ locale, want string }{
		{locale: "en", want: "automatic estimate reached"},
		{locale: "zh-Hans", want: "到了系统预估"},
		{locale: "ja", want: "自動見積もりのレビュー時点"},
	} {
		t.Run(tc.locale, func(t *testing.T) {
			f.hello.Locale = tc.locale
			e, ok := s.digestEntry(ctx, g, "")
			if !ok {
				t.Fatal("estimate_due did not render")
			}
			e.ID, e.Title = "todo_example", "Example task"
			got := prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{e}})
			if !strings.Contains(got, "signal=estimate_due") || !strings.Contains(got, tc.want) {
				t.Fatalf("localized estimate_due = %s", got)
			}
			golden(t, "estimate_due."+tc.locale, got)
		})
	}
}

func TestBriefEstimateHistorySurvivesPostgresReload(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s, _, todo, brief := startEstimateTask(t, database, map[string]any{"expect_minutes": nil})
	ctx := t.Context()
	g := superviseSignal{Todo: todo.ID, Kind: sigEstimateDue, Key: estimateDueKey(brief), At: brief.EstimateDueAt, Minutes: defaultTaskEstimateMinutes}
	s.briefClock(ctx, todo, brief.EstimateDueAt)
	if _, ok := s.supervise.pending[todo.ID+"|"+sigEstimateDue]; !ok {
		t.Fatal("the estimate cue did not enter pending")
	}
	s.estimateDueTold(ctx, []superviseSignal{g})
	s.recordBriefCompletion(ctx, todo, briefCompletionReport, brief.EstimateClockAt.Add(15*time.Minute))

	rows, links, err := database.ListTaskBriefs(ctx)
	if err != nil || len(rows) != 1 || len(links) != 1 {
		t.Fatalf("stored briefs = %d links=%d err=%v", len(rows), len(links), err)
	}
	raw := string(rows[0].State)
	for _, want := range []string{briefEventDispatch, briefEventEstimateDue, briefEventCompletion, `"actual_minutes": 15`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("brief state %q lacks %q", raw, want)
		}
	}

	s.tbriefs.mu.Lock()
	s.tbriefs.briefs, s.tbriefs.links = nil, nil
	s.tbriefs.mu.Unlock()
	if err := s.loadTaskBriefs(ctx); err != nil {
		t.Fatal(err)
	}
	reloaded, ok := s.briefOfTodo(todo.ID)
	if !ok || reloaded.EstimateSourceNow != briefEstimateSystemDefault || reloaded.EstimateDueToldAt == nil || reloaded.ActualMinutes != 15 {
		t.Fatalf("reloaded brief = %+v ok=%v", reloaded, ok)
	}
	if got := briefEventsOf(reloaded, briefEventDispatch); len(got) != 1 {
		t.Fatalf("reloaded dispatch events = %+v", got)
	}
	s.clearEstimateDue(todo.ID)
	s.briefClock(ctx, todo, reloaded.EstimateDueAt.Add(time.Minute))
	if _, ok := s.supervise.pending[todo.ID+"|"+sigEstimateDue]; ok {
		t.Fatal("the persisted marker did not stop a duplicate after reload")
	}
}

func (s *Service) mustBrief(t *testing.T, todoID string) TaskBrief {
	t.Helper()
	b, ok := s.briefOfTodo(todoID)
	if !ok {
		t.Fatalf("no brief for %s", todoID)
	}
	return b
}
