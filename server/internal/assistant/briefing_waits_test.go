package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/modapi"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The fixed direction rows come from the same ledgers as supervision. A task that was dispatched and later stopped is
// named; one that was never dispatched is only counted. All ids and secrets here are placeholders on a fake hub.
func TestBriefDirectionsListStoppedWork(t *testing.T) {
	needsBriefingModule(t)
	ctx := context.Background()
	now := time.Now().UTC()
	e := newTodoEventEnv(t, nil)
	const main, lead = "conv_main", "conv_lead_a"
	setSettings(t, e.s, func(v *Settings) { v.Main, v.Leaders = main, []string{lead} })
	e.title(main, "Main")
	e.title(lead, "Platform")

	task := func(title string) string {
		id := e.leaderTask(title, lead)
		e.s.todos.mu.Lock()
		e.s.todos.todos[id].Origin = lead
		e.s.todos.mu.Unlock()
		return id
	}
	edit := func(id string, fn func(*Todo)) {
		t.Helper()
		e.s.todos.mu.Lock()
		fn(e.s.todos.todos[id])
		e.s.todos.mu.Unlock()
	}
	released := func(id string, at time.Time) {
		t.Helper()
		edit(id, func(v *Todo) {
			v.Agents = []TodoAgent{{MachineID: "server", SessionID: "released-" + id, LinkedAt: at.Add(-time.Hour), ReleasedAt: &at}}
		})
	}
	var sessions []proto.Session
	link := func(id, sid string, sess proto.Session) {
		t.Helper()
		if err := e.s.linkTodoAgent(ctx, id, "server", sid, todoNow(e.s, id).Title, "/code/app", "claude"); err != nil {
			t.Fatal(err)
		}
		sess.MachineID, sess.SessionID, sess.StartedByPersona = "server", sid, true
		sessions = append(sessions, sess)
	}
	card := func(id, sid, kind, title, question string, private bool) {
		t.Helper()
		e.s.fleet.mu.Lock()
		if e.s.fleet.interactions == nil {
			e.s.fleet.interactions = map[string]*pending{}
		}
		e.s.fleet.interactions[id] = &pending{ix: proto.Interaction{ID: id, MachineID: "server", SessionID: sid, Kind: kind,
			TaskTitle: title, Questions: []proto.Question{{Question: question}}, CreatedAt: now.Add(-10 * time.Minute), Private: private}}
		e.s.fleet.mu.Unlock()
	}

	undispatched := task("Never dispatched")
	active := task("Still working")
	link(active, "s-active", proto.Session{Status: proto.StatusWorking, State: proto.StateWorking, UpdatedAt: now, Activity: "editing"})

	needs := task("Owner chooses the rollout")
	released(needs, now.Add(-3*time.Hour))
	edit(needs, func(v *Todo) {
		v.Supervision.State, v.Supervision.UpdatedAt = reviewNeedsUser, now.Add(-2*time.Hour)
		v.Supervision.Reviews = []TaskReview{{At: now.Add(-2 * time.Hour), Verdict: reviewNeedsUser, Reason: "choose a rollout"}}
	})

	review := task("Supervisor reviews the patch")
	released(review, now.Add(-3*time.Hour))
	pendingSince := now.Add(-90 * time.Minute)
	edit(review, func(v *Todo) {
		v.Supervision.State, v.Supervision.UpdatedAt, v.Supervision.PendingSince = reviewPending, now.Add(-90*time.Minute), &pendingSince
	})

	question := task("Question task actual title")
	link(question, "s-question", proto.Session{Status: proto.StatusIdle, State: proto.StateIdle, PaneID: "w:q", UpdatedAt: now})
	card("ix-question", "s-question", proto.IxQuestion, "an outdated card title", "which branch?", false)

	permission := task("Permission task actual title")
	link(permission, "s-permission", proto.Session{Status: proto.StatusIdle, State: proto.StateIdle, PaneID: "w:p", UpdatedAt: now})
	card("ix-permission", "s-permission", proto.IxPermission, "permission card title", "may this command run?", false)

	privateTask := task("SECRET PRIVATE TASK TITLE")
	link(privateTask, "s-private", proto.Session{Status: proto.StatusIdle, State: proto.StateIdle, PaneID: "w:s", UpdatedAt: now})
	card("ix-private", "s-private", proto.IxQuestion, "SECRET CARD TASK TITLE", "SECRET PRIVATE QUESTION", true)

	parked := task("Waits for the vendor")
	released(parked, now.Add(-5*time.Hour))
	parkedUntil := now.Add(24 * time.Hour)
	edit(parked, func(v *Todo) {
		v.Supervision.State = reviewParked
		v.Supervision.Reviews = []TaskReview{{At: now.Add(-4 * time.Hour), Verdict: reviewParked, Until: parkedUntil,
			WaitingOn: "vendor", Reason: "upstream API", Parks: 1}}
	})

	capacity := task("No execution capacity")
	queuedAt, nextAt := now.Add(-40*time.Minute), now.Add(5*time.Minute)
	e.s.tasks.mu.Lock()
	if e.s.tasks.plans == nil {
		e.s.tasks.plans = map[string]*TaskPlan{}
	}
	e.s.tasks.plans["plan-capacity"] = &TaskPlan{ID: "plan-capacity", State: planStarted, Tasks: []PlanTask{{TodoID: capacity,
		Queue: &TaskQueue{Reason: "direction cap", QueuedAt: queuedAt, NextAt: nextAt, Attempt: 1}}}}
	e.s.tasks.mu.Unlock()

	orphaned := task("Agent closed on a quota error")
	link(orphaned, "s-orphan", proto.Session{Status: proto.StatusOffline, State: proto.StateOffline, UpdatedAt: now.Add(-time.Hour), TurnError: "quota"})
	orphanSince := now.Add(-45 * time.Minute)
	e.s.orphans.mu.Lock()
	if e.s.orphans.now == nil {
		e.s.orphans.now, e.s.orphans.seen = map[string]TodoOrphan{}, map[string]time.Time{}
	}
	e.s.orphans.now[orphaned] = TodoOrphan{Since: orphanSince}
	e.s.orphans.seen[orphaned] = orphanSince
	e.s.orphans.mu.Unlock()

	overdue := task("Past its expected finish")
	released(overdue, now.Add(-3*time.Hour))
	e.s.tbriefs.mu.Lock()
	e.s.tbriefs.init()
	e.s.tbriefs.briefs["brief-overdue"] = &taskBrief{rec: briefRecord{ID: "brief-overdue", CreatedAt: now.Add(-25 * time.Hour), ExpectMinutes: 30}}
	e.s.tbriefs.links[overdue] = taskBriefLink{Brief: "brief-overdue", At: now.Add(-25 * time.Hour)}
	e.s.tbriefs.mu.Unlock()

	liveStalled := task("Working session has not moved")
	link(liveStalled, "s-stalled", proto.Session{Status: proto.StatusWorking, State: proto.StateWorking,
		UpdatedAt: now.Add(-30 * time.Minute), Activity: "last tool call"})

	releasedTask := task("Released agent was never reopened")
	released(releasedTask, now.Add(-3*time.Hour))

	mainWait := e.leaderTask("Main direction owner decision", main)
	released(mainWait, now.Add(-3*time.Hour))
	edit(mainWait, func(v *Todo) {
		v.Origin = main
		v.Supervision.State, v.Supervision.UpdatedAt = reviewNeedsUser, now.Add(-time.Hour)
	})

	e.f.setSessions("server", sessions...)
	dirs, _ := e.s.briefDirections(ctx, now, i18n.ZhHans, testWeb)
	if len(dirs) != 2 {
		t.Fatalf("directions = %+v", dirs)
	}
	byID := map[string]prompts.BriefDirection{}
	for _, d := range dirs {
		byID[d.ID] = d
	}
	mainDir, leadDir := byID[main], byID[lead]
	if !mainDir.Owner || leadDir.Owner || len(mainDir.Waits) != 1 || mainDir.Waits[0].TodoID != mainWait {
		t.Fatalf("main direction = %+v; main task = %+v; all directions = %+v", mainDir, todoNow(e.s, mainWait), dirs)
	}
	if leadDir.Open != 13 || leadDir.Active != 1 || leadDir.Undispatched != 1 || len(leadDir.Waits) != 11 || leadDir.More != 0 {
		t.Fatalf("leader counts = %+v", leadDir)
	}
	waits := map[string]prompts.BriefWait{}
	var private *prompts.BriefWait
	for i := range leadDir.Waits {
		w := leadDir.Waits[i]
		if w.Kind == "private" {
			private = &leadDir.Waits[i]
			continue
		}
		waits[w.TodoID] = w
		if w.Title != todoNow(e.s, w.TodoID).Title || w.Since.IsZero() || w.Link != todoLink(testWeb, w.TodoID) || !strings.Contains(w.Line, w.Title) {
			t.Errorf("incomplete wait row: %+v, want task link %s", w, todoLink(testWeb, w.TodoID))
		}
	}
	for id, kind := range map[string]string{
		needs: "needs_user", review: "review", question: "question", permission: "permission", parked: "parked",
		capacity: "capacity", orphaned: "orphaned", overdue: "overdue", liveStalled: "stalled", releasedTask: "stalled",
	} {
		if got := waits[id].Kind; got != kind {
			t.Errorf("%s: kind = %q, want %q (%+v)", id, got, kind, waits[id])
		}
	}
	for _, id := range []string{active, undispatched} {
		if _, listed := waits[id]; listed {
			t.Errorf("active or never-dispatched task got a stopped row: %+v", waits[id])
		}
	}
	if got := waits[question]; got.Title != "Question task actual title" || got.TodoID != question || got.WaitingOn != "owner" {
		t.Errorf("the task-linked question lost its task identity: %+v", got)
	}
	if got := waits[parked]; !got.Until.Equal(parkedUntil) || got.WaitingOn != "vendor" {
		t.Errorf("parked row = %+v", got)
	}
	if got := waits[capacity]; !got.Since.Equal(queuedAt) || !got.Until.Equal(nextAt) || got.WaitingOn != "capacity" {
		t.Errorf("capacity row = %+v", got)
	}
	if got := waits[orphaned]; !got.Since.Equal(orphanSince) {
		t.Errorf("orphan stopped duration starts at %s, want the existing orphan mark %s", got.Since, orphanSince)
	}
	if private == nil || private.TodoID != "" || private.Title != i18n.T(i18n.ZhHans, "assistant-briefing.private_card") ||
		private.Reason != "" || private.Link == "" || strings.Contains(private.Link, privateTask) || private.WaitingOn != "owner" {
		t.Fatalf("private row = %+v", private)
	}
	raw, _ := json.Marshal(dirs)
	facts := &briefFacts{At: now, Loc: time.UTC, Lang: string(i18n.ZhHans), Directions: dirs}
	briefCardView, plain, _ := e.s.briefCard(facts, "Morning briefing", "")
	modelText := e.s.briefModelText(facts)
	for _, d := range dirs {
		for _, w := range d.Waits {
			if w.TodoID == "" {
				continue
			}
			if want := w.Line + " " + todoLink(testWeb, w.TodoID); !strings.Contains(modelText, want) {
				t.Errorf("rendered model facts omitted the stopped task's todo URL %q: %s", want, modelText)
			}
		}
	}
	rendered := string(raw) + briefCardView.JSON() + plain + modelText
	for _, secret := range []string{"SECRET PRIVATE TASK TITLE", "SECRET CARD TASK TITLE", "SECRET PRIVATE QUESTION", privateTask} {
		if strings.Contains(rendered, secret) {
			t.Errorf("private content %q leaked into the fixed facts or their rendering", secret)
		}
	}

	// First discovery of an already-closed session uses its actual last activity when no supervisor orphan mark exists.
	e.s.orphans.mu.Lock()
	delete(e.s.orphans.now, orphaned)
	delete(e.s.orphans.seen, orphaned)
	e.s.orphans.mu.Unlock()
	dirs, _ = e.s.briefDirections(ctx, now, i18n.ZhHans, testWeb)
	foundOrphan := false
	for _, d := range dirs {
		for _, w := range d.Waits {
			if w.TodoID == orphaned {
				foundOrphan = true
				if !w.Since.Equal(now.Add(-time.Hour)) {
					t.Errorf("first closed-orphan discovery starts at %s, want the session's last activity %s", w.Since, now.Add(-time.Hour))
				}
			}
		}
	}
	if !foundOrphan {
		t.Fatal("first closed-orphan discovery produced no stopped row")
	}
}

// The owner reads parked-until values in the hub's configured zone, directions by conversation name rather than raw ID,
// and every ordinary task row with the same to-do URL in both the rich card and its complete plain-text substitute.
func TestBriefDirectionsUseHubZoneNamesAndTaskLinks(t *testing.T) {
	needsBriefingModule(t)
	ctx := context.Background()
	now := time.Date(2030, 1, 2, 20, 0, 0, 0, time.UTC)
	until := time.Date(2030, 1, 3, 1, 30, 0, 0, time.UTC)
	e := newTodoEventEnv(t, nil)
	const main, lead = "conv_main", "conv_lead_a"
	setSettings(t, e.s, func(v *Settings) { v.Main, v.Leaders, v.Timezone = main, []string{lead}, "Asia/Shanghai" })
	e.title(lead, "Platform")
	parked := e.leaderTask("Parked until tomorrow", lead)
	linkedAt, releasedAt := now.Add(-2*time.Hour), now.Add(-time.Hour)
	e.s.todos.mu.Lock()
	todo := e.s.todos.todos[parked]
	todo.Origin = lead
	todo.Agents = []TodoAgent{{MachineID: "server", SessionID: "released-parked", LinkedAt: linkedAt, ReleasedAt: &releasedAt}}
	todo.Supervision.State = reviewParked
	todo.Supervision.Reviews = []TaskReview{{At: releasedAt, Verdict: reviewParked, Until: until, WaitingOn: "vendor"}}
	e.s.todos.mu.Unlock()

	dirs, _ := e.s.briefDirections(ctx, now, i18n.ZhHans, testWeb)
	byID := map[string]prompts.BriefDirection{}
	for _, d := range dirs {
		byID[d.ID] = d
	}
	if got, want := byID[main].Name, i18n.T(i18n.ZhHans, "assistant-feishu_cards.main_chat"); got != want {
		t.Errorf("main direction name = %q, want %q (raw ID hidden)", got, want)
	}
	if got := byID[lead].Name; got != "Platform" {
		t.Errorf("leader direction name = %q, want its conversation title", got)
	}
	if len(byID[lead].Waits) != 1 {
		t.Fatalf("leader waits = %+v, want the parked task", byID[lead].Waits)
	}
	w := byID[lead].Waits[0]
	if !strings.Contains(w.Line, "2030-01-03 09:30") || strings.Contains(w.Line, "2030-01-03 01:30") {
		t.Errorf("parked-until line did not convert UTC to the hub zone: %q", w.Line)
	}
	if want := todoLink(testWeb, parked); w.Link != want {
		t.Fatalf("parked task link = %q, want %q", w.Link, want)
	}
	card, plain, _ := e.s.briefCard(&briefFacts{At: now, Lang: string(i18n.ZhHans), Loc: e.s.location(ctx), Web: testWeb, Directions: dirs}, "Evening briefing", "")
	if !strings.Contains(card.JSON(), "]("+w.Link+")") {
		t.Errorf("rich card has no clickable task link %s: %s", w.Link, card.JSON())
	}
	if !strings.Contains(plain, w.Line+" · "+w.Link) {
		t.Errorf("plain-text substitute omitted the task URL %s: %s", w.Link, plain)
	}
}

func TestBriefTaskWaitUsesLatestAgentActivity(t *testing.T) {
	s := newStorelessService(t, newFakeHub().withMachine())
	waits := defaultTestWaits(t, s)
	now := time.Now().UTC()
	agents := []proto.Session{
		{SessionID: "old", State: proto.StateWorking, UpdatedAt: now.Add(-3 * time.Hour), Activity: "old stopped tool"},
		{SessionID: "current", State: proto.StateIdle, UpdatedAt: now.Add(-30 * time.Minute), Activity: "recent answer"},
	}
	if w, stopped := s.briefTaskWait(context.Background(), Todo{ID: "todo-live", Title: "Still active"}, agents, nil, nil,
		now, waits, nil, nil, nil); stopped {
		t.Fatalf("an old agent hid the newer agent's activity: %+v", w)
	}
	agents[1].UpdatedAt = now.Add(-2 * time.Hour)
	w, stopped := s.briefTaskWait(context.Background(), Todo{ID: "todo-stopped", Title: "Both stopped"}, agents, nil, nil,
		now, waits, nil, nil, nil)
	if !stopped || w.Kind != "stalled" || !w.Since.Equal(agents[1].UpdatedAt) || w.Reason != "recent answer" {
		t.Fatalf("the latest stopped session did not supply the wait row: %+v, stopped %v", w, stopped)
	}
}

// A task-linked ordinary card keeps the task's name, not a stale title denormalized onto the card. A Private card keeps
// neither identity nor question text. This calls the collector directly so withTask cannot repair a bad fixture first.
func TestBriefTaskCardWaitIdentityAndPrivacy(t *testing.T) {
	s := newStorelessService(t, newFakeHub().withMachine())
	waits := defaultTestWaits(t, s)
	now := time.Now().UTC()
	todo := Todo{ID: "todo-real", Title: "The real task title"}
	for _, tc := range []struct {
		name    string
		private bool
		kind    string
	}{
		{name: "question", kind: "question"},
		{name: "permission", kind: "permission"},
		{name: "private", kind: "private", private: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ix := proto.Interaction{ID: "ix-card", MachineID: "server", SessionID: "s-card", Kind: proto.IxQuestion,
				TaskTitle: "stale card title", Questions: []proto.Question{{Question: "secret question"}}, CreatedAt: now.Add(-time.Hour), Private: tc.private}
			if tc.kind == "permission" {
				ix.Kind = proto.IxPermission
			}
			w, stopped := s.briefTaskWait(context.Background(), todo, nil, []proto.Interaction{ix}, nil, now, waits, nil, nil, nil)
			if !stopped || w.Kind != tc.kind || w.InteractionID != ix.ID {
				t.Fatalf("wait = %+v, stopped %v", w, stopped)
			}
			if tc.private {
				if w.TodoID != "" || w.Title == todo.Title || w.Reason != "" || strings.Contains(w.Title, "secret") {
					t.Fatalf("private card kept content: %+v", w)
				}
			} else if w.TodoID != todo.ID || w.Title != todo.Title {
				t.Fatalf("ordinary card lost its task identity: %+v", w)
			}
		})
	}
}

// briefingWaitsJSON is the briefing module's setting as persona_settings shows it (internal/modules/briefing Waits).
type briefingWaitsJSON struct {
	StalledMinutes int `json:"stalled_minutes"`
	SilentMinutes  int `json:"silent_minutes"`
	OrphanMinutes  int `json:"orphan_minutes"`
	CardMinutes    int `json:"card_minutes"`
	MaxRows        int `json:"max_rows"`
}

// defaultTestWaits are the briefing module's wait rules with nothing set.
func defaultTestWaits(t *testing.T, s *Service) modapi.WaitRules {
	t.Helper()
	needsBriefingModule(t)
	w, ok := s.briefingWaits(Settings{})
	if !ok {
		t.Fatal("the briefing module is not loaded")
	}
	return w
}

func TestBriefingWaitsSettingUsesSupervisionDefaultsAndApprovalChange(t *testing.T) {
	needsBriefingModule(t)
	ctx := context.Background()
	s := newTestService(t, newFakeHub().withMachine())
	var registered *ownSetting
	for i := range s.ownSettings() {
		o := s.ownSettings()[i]
		if o.Key == "assistant.briefing_waits" {
			registered = &o
			break
		}
	}
	if registered == nil || settingTitles["assistant.briefing_waits"] != "title_briefing_waits" {
		t.Fatal("assistant.briefing_waits is not registered in persona_settings")
	}
	inForce := func() briefingWaitsJSON {
		t.Helper()
		got, err := registered.get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var decoded briefingWaitsJSON
		if err := json.Unmarshal([]byte(got), &decoded); err != nil {
			t.Fatalf("get = %s: %v", got, err)
		}
		return decoded
	}
	defaults := inForce()
	if defaults.StalledMinutes != int(superviseStallMin/time.Minute) || defaults.SilentMinutes != int(superviseSilentMin/time.Minute) ||
		defaults.OrphanMinutes != int(orphanOfflineAfter/time.Minute) || defaults.CardMinutes != int(superviseNeedsAfter/time.Minute) || defaults.MaxRows != 15 {
		t.Fatalf("defaults no longer come from supervision: %+v", defaults)
	}
	w := defaultTestWaits(t, s)
	if w.StalledAfter(0) != superviseStallMin || w.SilentAfter(0) != superviseSilentMin || w.OrphanAfter() != orphanOfflineAfter ||
		w.CardAfter() != superviseNeedsAfter || w.StalledAfter(40) != stallAfter(40) || w.SilentAfter(100) != silentAfter(100) {
		t.Fatalf("the default wait rules differ from supervision's floors")
	}
	for _, source := range []string{"superviseStallMin", "superviseSilentMin", "orphanOfflineAfter", "superviseNeedsAfter"} {
		if !strings.Contains(registered.About, source) {
			t.Errorf("setting documentation does not name %s: %s", source, registered.About)
		}
	}
	for _, bad := range []string{`{}`, `{"stalled_minutes":20,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":0}`,
		`{"stalled_minutes":0,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":15}`,
		`{"stalled_minutes":20,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":101}`, `"15"`} {
		if _, err := registered.set(ctx, json.RawMessage(bad)); err == nil {
			t.Errorf("accepted invalid value %s", bad)
		}
	}
	custom := briefingWaitsJSON{StalledMinutes: 25, SilentMinutes: 150, OrphanMinutes: 12, CardMinutes: 4, MaxRows: 7}
	raw, _ := json.Marshal(custom)
	change, err := registered.set(ctx, raw)
	if err != nil || change == nil || change.apply == nil || change.cardText == nil {
		t.Fatalf("set did not prepare an approval-backed change: %+v, %v", change, err)
	}
	if card := change.card(i18n.ZhHans); !strings.Contains(card, "25m") || !strings.Contains(card, "max_rows=7") {
		t.Fatalf("approval card omits before/after values: %s", card)
	}
	if _, err := change.apply(ctx); err != nil {
		t.Fatal(err)
	}
	if got := inForce(); got != custom {
		t.Fatalf("applied = %+v, want %+v", got, custom)
	}
	if string(s.settings(ctx).BriefingWaits) != string(raw) {
		t.Fatalf("stored %s, want %s", s.settings(ctx).BriefingWaits, raw)
	}
	if again, err := registered.set(ctx, raw); err != nil || again.apply != nil {
		t.Fatalf("unchanged value prepared another approval: %+v, %v", again, err)
	}
	reset, err := registered.set(ctx, json.RawMessage(`null`))
	if err != nil || reset.apply == nil {
		t.Fatalf("null did not prepare a defaults reset: %+v, %v", reset, err)
	}
	if _, err := reset.apply(ctx); err != nil {
		t.Fatal(err)
	}
	if got := inForce(); got != defaults || s.settings(ctx).BriefingWaits != nil {
		t.Fatalf("null reset = %+v, saved %s", got, s.settings(ctx).BriefingWaits)
	}
}

// Disabled routines retain the actual transition time. A legacy disabled routine has no such record: its line says so,
// even when LastRun is much newer, and one-off reminders are not mislabeled as daily reports or patrols.
func TestBriefDisabledRoutinesUseDisableTimeNotLastRun(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	s := newTestService(t, newFakeHub().withMachine())
	disabledReport, disabledPatrol := now.Add(-2*time.Hour), now.Add(-3*time.Hour)
	setTasks(s,
		Task{ID: "morning-off", Title: "Morning report", Repeat: repeatDaily, Time: "09:00", Facts: true, SessionID: "main",
			RunMachineID: "server", RunSessionID: "main", DisabledAt: disabledReport, LastRun: now.Add(-10 * time.Minute)},
		Task{ID: "legacy-off", Title: "Legacy evening report", Repeat: repeatDaily, Time: "21:00", Report: true, SessionID: "main",
			RunMachineID: "server", RunSessionID: "main", LastRun: now.Add(-5 * time.Minute)},
		Task{ID: "patrol-off", Title: "Deploy patrol", Repeat: repeatInterval, EveryMinutes: 30, SessionID: "lead",
			RunMachineID: "server", RunSessionID: "lead", DisabledAt: disabledPatrol},
		Task{ID: "cron-off", Title: "Quota patrol", Repeat: repeatCron, Cron: "*/30 * * * *", SessionID: "lead", DisabledAt: disabledPatrol},
		Task{ID: "once-off", Title: "One-off reminder", Repeat: repeatOnce, DisabledAt: disabledReport},
		Task{ID: "ordinary-daily-off", Title: "Daily reminder", Repeat: repeatDaily, Time: "12:00", DisabledAt: disabledReport},
		Task{ID: "enabled", Title: "Enabled patrol", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
	got := s.briefDisabledRoutines(ctx, now, i18n.ZhHans, map[string]string{"main": "主对话", "lead": "平台"})
	if len(got) != 4 {
		t.Fatalf("disabled routines = %+v", got)
	}
	byID := map[string]prompts.BriefDisabledRoutine{}
	for _, r := range got {
		byID[r.ID] = r
		if !strings.Contains(r.Line, r.Title) || !strings.Contains(r.Line, r.Direction) {
			t.Errorf("incomplete disabled row: %+v", r)
		}
	}
	if r := byID["morning-off"]; r.Kind != "report" || r.DisabledAt == nil || !r.DisabledAt.Equal(disabledReport) || !strings.Contains(r.Line, "2 小时") {
		t.Errorf("recorded disable duration = %+v", r)
	}
	if r := byID["legacy-off"]; r.DisabledAt != nil || !strings.Contains(r.Line, "关闭时间未记录") || strings.Contains(r.Line, "5 分钟") {
		t.Errorf("legacy row substituted LastRun or omitted the unknown marker: %+v", r)
	}
	if r := byID["patrol-off"]; r.Kind != "patrol" || r.DisabledAt == nil || !r.DisabledAt.Equal(disabledPatrol) {
		t.Errorf("patrol = %+v", r)
	}
}

func TestRoutineDisabledAtTracksTransitionsAndExpiry(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct {
		name string
		old  Task
		next Task
		want time.Time
	}{
		{name: "created disabled", old: Task{}, next: Task{ID: "r"}, want: now},
		{name: "POST created disabled", old: Task{ID: "r", CreatedAt: now}, next: Task{ID: "r", CreatedAt: now}, want: now},
		{name: "enabled to disabled", old: Task{ID: "r", Enabled: true}, next: Task{ID: "r"}, want: now},
		{name: "legacy stays unknown", old: Task{ID: "r", CreatedAt: now.Add(-24 * time.Hour)}, next: Task{ID: "r", CreatedAt: now.Add(-24 * time.Hour)}},
		{name: "reenable clears", old: Task{ID: "r", DisabledAt: now.Add(-time.Hour)}, next: Task{ID: "r", Enabled: true, DisabledAt: now.Add(-time.Hour)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rearm(tc.old, &tc.next, now)
			if !tc.next.DisabledAt.Equal(tc.want) {
				t.Fatalf("DisabledAt = %s, want %s", tc.next.DisabledAt, tc.want)
			}
		})
	}

	s := newStorelessService(t, newFakeHub().withMachine())
	setTasks(s, Task{ID: "expiring", Title: "Temporary patrol", Prompt: "check", Repeat: repeatInterval, EveryMinutes: 30,
		Enabled: true, At: now.Add(-time.Hour), Until: now.Add(-time.Minute)})
	s.runDue(context.Background(), now)
	s.sched.inflight.Wait()
	got, ok := taskByID(s, "expiring")
	if !ok || got.Enabled || !got.DisabledAt.Equal(now) {
		t.Fatalf("Until expiry did not record its disable time: %+v", got)
	}
}
