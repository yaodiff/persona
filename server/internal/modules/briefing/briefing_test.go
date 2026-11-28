package briefing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/modapi"
)

func TestManifest(t *testing.T) {
	info := Module{}.Info()
	if info.ID != "briefing" || info.API != modapi.API || info.Origin != modapi.OriginBuiltin {
		t.Fatalf("manifest: %+v", info)
	}
	if len(info.Settings) != 1 || info.Settings[0] != SettingKey || len(info.Schedules) != 1 || info.Schedules[0] != modapi.ScheduleFactsName {
		t.Fatalf("manifest slots: %+v", info)
	}
}

// The value persona_settings shows is the defaults until a value is stored, and stored values come back as written.
func TestSettingValueAndCheck(t *testing.T) {
	def, _ := json.Marshal(Defaults())
	if got := string(value(nil)); got != string(def) || got != `{"stalled_minutes":20,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":15}` {
		t.Fatalf("default value %s", got)
	}
	for _, bad := range []string{`{}`, `{"stalled_minutes":20,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":0}`,
		`{"stalled_minutes":0,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":15}`,
		`{"stalled_minutes":20,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":101}`,
		`{"stalled_minutes":10081,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":15}`, `"15"`, `[`} {
		if _, err := check(json.RawMessage(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if got, err := check(json.RawMessage(`null`)); err != nil || got != nil {
		t.Fatalf("null = %s, %v; want the defaults (nil)", got, err)
	}
	raw := `{"max_rows":7,"stalled_minutes":25,"silent_minutes":150,"orphan_minutes":12,"card_minutes":4}`
	stored, err := check(json.RawMessage(raw))
	if err != nil || string(stored) != `{"stalled_minutes":25,"silent_minutes":150,"orphan_minutes":12,"card_minutes":4,"max_rows":7}` {
		t.Fatalf("stored %s, %v", stored, err)
	}
	if got := describe(value(stored)); got != "stalled=25m, silent=150m, orphan=12m, cards=4m, max_rows=7" {
		t.Fatalf("describe %q", got)
	}
}

func TestRules(t *testing.T) {
	w := waitsOf(nil)
	if w.CardAfter() != 3*time.Minute || w.OrphanAfter() != 10*time.Minute || w.StalledAfter(0) != 20*time.Minute ||
		w.SilentAfter(0) != 2*time.Hour || w.StalledAfter(40) != time.Hour || w.SilentAfter(100) != 150*time.Minute {
		t.Fatalf("rules %+v", w)
	}
	if got := waitsOf(json.RawMessage(`not json`)); got != Defaults() {
		t.Fatalf("unreadable stored value: %+v", got)
	}
}

// Owner waits come first and are never folded; the others are ordered by age, then key, and bounded by MaxRows.
func TestFoldKeepsEveryOwnerWait(t *testing.T) {
	now := time.Now()
	var rows []modapi.BriefWait
	for i := 0; i < 17; i++ {
		rows = append(rows, modapi.BriefWait{Kind: "stalled", Since: now.Add(time.Duration(i) * time.Minute), TodoID: fmt.Sprintf("other-%02d", i)})
	}
	for i := 0; i < 20; i++ {
		kind := []string{"needs_user", "question", "permission", "private"}[i%4]
		rows = append(rows, modapi.BriefWait{Kind: kind, Since: now.Add(time.Duration(i) * time.Minute), TodoID: fmt.Sprintf("owner-%02d", i)})
	}
	d := modapi.BriefDirection{Waits: rows}
	Defaults().FoldDirection(&d)
	keep, more := d.Waits, d.More
	if len(keep) != 35 || more != 2 {
		t.Fatalf("kept %d, more %d; want 35 and 2", len(keep), more)
	}
	for n, row := range keep {
		if owner := OwnerWait(row.Kind); owner != (n < 20) {
			t.Fatalf("row %d (%+v) out of place", n, row)
		}
	}
	if keep[20].TodoID != "other-00" || keep[34].TodoID != "other-14" {
		t.Fatalf("the others are not oldest first: %s … %s", keep[20].TodoID, keep[34].TodoID)
	}
	same := []modapi.BriefWait{{Kind: "stalled", Since: now, TodoID: "b"}, {Kind: "stalled", Since: now, TodoID: "a"}}
	d.Waits = same
	Defaults().FoldDirection(&d)
	if d.Waits[0].TodoID != "a" {
		t.Fatalf("ties are not broken by key: %v", d.Waits)
	}
}

// ---- fake modapi.Host & narrow ports for standalone module unit tests ----

type fakeHost struct {
	t        *testing.T
	setting  *modapi.Setting
	facts    *modapi.ScheduleFacts
	tasks    *fakeTasks
	todos    *fakeTodos
	inbox    *fakeInbox
	later    *fakeLater
	schedule *fakeSchedule
	feed     *fakeFeed
	delivery *fakeDelivery
	dayRecap *fakeDayRecap
	prompts  *fakePrompts
}

func newFakeHost(t *testing.T) *fakeHost {
	t.Helper()
	return &fakeHost{
		t:        t,
		tasks:    &fakeTasks{lang: "en", web: "https://hub.example", loc: time.UTC, fleetReady: true, feishuPushes: true},
		todos:    &fakeTodos{titles: map[string]string{}},
		inbox:    &fakeInbox{},
		later:    &fakeLater{},
		schedule: &fakeSchedule{claimed: map[string]bool{}, morningDue: true, targetOK: true, targetSession: "main", targetMachine: "m1", targetName: "Host"},
		feed:     &fakeFeed{},
		delivery: &fakeDelivery{},
		dayRecap: &fakeDayRecap{},
		prompts:  &fakePrompts{},
	}
}

func (h *fakeHost) Logf(format string, args ...any)                            { h.t.Logf(format, args...) }
func (h *fakeHost) AddSetting(s modapi.Setting) error                          { h.setting = &s; return nil }
func (h *fakeHost) AddScheduleFacts(f modapi.ScheduleFacts) error              { h.facts = &f; return nil }
func (h *fakeHost) AddHubRoute(string, http.HandlerFunc) error                 { return nil }
func (h *fakeHost) AddMCPTool(modapi.MCPTool) error                            { return nil }
func (h *fakeHost) AddBackgroundJob(string, func(context.Context) error) error { return nil }
func (h *fakeHost) Tasks() modapi.TaskPort                                     { return h.tasks }
func (h *fakeHost) Todos() modapi.TodoPort                                     { return h.todos }
func (h *fakeHost) Inbox() modapi.InboxPort                                    { return h.inbox }
func (h *fakeHost) Later() modapi.LaterPort                                    { return h.later }
func (h *fakeHost) Schedule() modapi.SchedulePort                              { return h.schedule }
func (h *fakeHost) Feed() modapi.FeedPort                                      { return h.feed }
func (h *fakeHost) Delivery() modapi.DeliveryPort                              { return h.delivery }
func (h *fakeHost) DayRecap() modapi.DayRecapPort                              { return h.dayRecap }
func (h *fakeHost) Prompts() modapi.PromptPort                                 { return h.prompts }

type fakeTasks struct {
	lang, web    string
	loc          *time.Location
	fleetReady   bool
	feishuPushes bool
	board        []modapi.BriefBoardRow
	asks         map[string]modapi.BriefAsk
	done         []modapi.BriefDone
	failures     []modapi.BriefFailure
	failTotal    int
	northStar    modapi.BriefNorthStar
	confirm      modapi.BriefConfirmations
	setup        *modapi.BriefSetup
	setupTold    int
	digestMarked bool
}

func (f *fakeTasks) BotLang(context.Context) string          { return f.lang }
func (f *fakeTasks) WebBase(context.Context) string          { return f.web }
func (f *fakeTasks) Location(context.Context) *time.Location { return f.loc }
func (f *fakeTasks) FleetReady() bool                        { return f.fleetReady }
func (f *fakeTasks) FeishuPushesOn(context.Context) bool     { return f.feishuPushes }
func (f *fakeTasks) BoardRows(context.Context, string, string, time.Time) []modapi.BriefBoardRow {
	return f.board
}
func (f *fakeTasks) AskCopy(id, _ string) (modapi.BriefAsk, bool) {
	a, ok := f.asks[id]
	return a, ok
}
func (f *fakeTasks) TodayFinished(context.Context, time.Time, *time.Location) []modapi.BriefDone {
	return f.done
}
func (f *fakeTasks) TodayFailures(context.Context, time.Time, *time.Location) ([]modapi.BriefFailure, int) {
	return f.failures, f.failTotal
}
func (f *fakeTasks) NorthStar(context.Context, time.Time, *time.Location, string) modapi.BriefNorthStar {
	return f.northStar
}
func (f *fakeTasks) Confirmations(context.Context, time.Time, *time.Location, string) modapi.BriefConfirmations {
	return f.confirm
}
func (f *fakeTasks) Leaders(context.Context, time.Time) []modapi.BriefLeader { return nil }
func (f *fakeTasks) Directions(context.Context, time.Time, string, string) ([]modapi.BriefDirection, []modapi.BriefDisabledRoutine) {
	return nil, nil
}
func (f *fakeTasks) Captures(context.Context, string, time.Time) []string { return nil }
func (f *fakeTasks) TaskBriefDigest(_ context.Context, _ time.Time, _ *time.Location, mark bool) ([]modapi.BriefTask, []modapi.BriefTask) {
	if mark {
		f.digestMarked = true
	}
	return nil, nil
}
func (f *fakeTasks) SetupFor(context.Context, time.Time, *time.Location, string, string) *modapi.BriefSetup {
	return f.setup
}
func (f *fakeTasks) SetupTold(setup *modapi.BriefSetup, _ time.Time) {
	if setup != nil {
		f.setupTold++
	}
}

type fakeTodos struct {
	open      []modapi.BriefTodoProgress
	titles    map[string]string
	followups []modapi.BriefPRFollowup
}

func (f *fakeTodos) OpenProgressTodos(context.Context) []modapi.BriefTodoProgress { return f.open }
func (f *fakeTodos) TodoTitle(id string) (string, bool)                           { v, ok := f.titles[id]; return v, ok }
func (f *fakeTodos) StalePRFollowups(context.Context, time.Time, string) []modapi.BriefPRFollowup {
	return f.followups
}

func (f *fakeTodos) DeferredSupervision(context.Context, time.Time) []modapi.BriefDeferredSupervision {
	return nil
}
func (f *fakeTodos) SupervisionBriefed(context.Context, []string, time.Time) {}

type fakeInbox struct{ signals []modapi.BriefInboxSignal }

func (f *fakeInbox) FollowingSignals(context.Context) []modapi.BriefInboxSignal { return f.signals }

type fakeLater struct {
	rows  []modapi.BriefBoardRow
	total int
}

func (f *fakeLater) Handle(context.Context, string) (bool, error) { return false, nil }
func (f *fakeLater) Rows(context.Context, string, string, time.Time, int) ([]modapi.BriefBoardRow, int) {
	return f.rows, f.total
}

type fakeSchedule struct {
	morningDue    bool
	claimed       map[string]bool
	targetOK      bool
	targetSession string
	targetMachine string
	targetName    string
	retried       []string
}

func (f *fakeSchedule) MorningFactsDue(context.Context, int) bool { return f.morningDue }
func (f *fakeSchedule) ClaimFallbackReceipt(_ context.Context, key string) bool {
	if f.claimed[key] {
		return false
	}
	f.claimed[key] = true
	return true
}
func (f *fakeSchedule) ResolveFallbackTarget(context.Context, modapi.BriefTaskInfo, modapi.BriefScheduleRun) (string, string, string, bool) {
	return f.targetSession, f.targetMachine, f.targetName, f.targetOK
}
func (f *fakeSchedule) RetryNotStarted(taskID string, _ modapi.BriefScheduleRun) {
	f.retried = append(f.retried, taskID)
}

type fakeFeedEntry struct {
	id, session, kind, ref string
	body                   any
}
type fakeFeed struct{ items []fakeFeedEntry }

func (f *fakeFeed) Append(_ context.Context, session, kind, ref string, body any) error {
	f.items = append(f.items, fakeFeedEntry{session: session, kind: kind, ref: ref, body: body})
	return nil
}
func (f *fakeFeed) AppendOnce(_ context.Context, id, session, kind, ref string, body any) error {
	f.items = append(f.items, fakeFeedEntry{id: id, session: session, kind: kind, ref: ref, body: body})
	return nil
}

type fakeDelivery struct {
	fallbacks []modapi.BriefFallbackSend
	listed    int
}

func (f *fakeDelivery) SendFallback(_ context.Context, req modapi.BriefFallbackSend) {
	f.fallbacks = append(f.fallbacks, req)
}
func (f *fakeDelivery) MarkListed(*modapi.BriefSnapshot) { f.listed++ }

type fakeDayRecap struct{ recs []modapi.BriefDayRecording }

func (f *fakeDayRecap) TodaysRecordings(context.Context, time.Time, *time.Location) []modapi.BriefDayRecording {
	return f.recs
}

type fakePrompts struct{}

func (fakePrompts) RenderBriefFacts(b modapi.BriefFacts) string {
	return fmt.Sprintf("facts:waiting=%d,done=%d,failed=%d,accept=%d,later=%d",
		b.WaitingTotal, b.DoneTotal, b.FailedTotal, b.AcceptTotal, b.LaterTotal)
}
func (fakePrompts) RenderNorthStarFacts(lines []string) string { return strings.Join(lines, "\n") }
func (fakePrompts) BriefDayRunes(d *modapi.BriefDay) int {
	n := 0
	for _, p := range d.Parts {
		n += len([]rune(p.Summary))
	}
	for _, e := range d.Timeline {
		n += len([]rune(e.Title)) + 20
	}
	for _, a := range d.Actions {
		n += len([]rune(a.Who)) + len([]rune(a.What)) + 10
	}
	return n
}
func (fakePrompts) BoardRowMarkdown(_ string, n int, r modapi.BriefBoardRow) string {
	return fmt.Sprintf("**%d** %s", n, r.Summary)
}
func (fakePrompts) OpenStageBar(_, stage string) string { return stage }
func (fakePrompts) BoardRowPlain(_ string, n int, r modapi.BriefBoardRow) string {
	return fmt.Sprintf("%d. %s", n, r.Summary)
}
func (fakePrompts) Translate(_ string, key string, args map[string]any) string {
	if args != nil {
		if n, ok := args["n"]; ok {
			return fmt.Sprintf("%s=%v", key, n)
		}
	}
	return key
}
func (fakePrompts) StripContext(s string) string { return s }

// Register wires the runner; Start/Sent/Claim/Push/Reached/Grace and Body wire JSON work purely over modapi ports.
func TestRunnerLifecycleAndWireJSON(t *testing.T) {
	h := newFakeHost(t)
	now := time.Now().UTC()
	h.tasks.board = []modapi.BriefBoardRow{
		{Blocking: true, ID: "ix_1", Summary: "Wait Q", From: "Agent", Machine: "m1", Session: "ag1", Waited: 5 * time.Minute},
		{Blocking: false, ID: "todo_1", Summary: "Verify T", From: "Agent", Asked: now.Add(-time.Hour)},
	}
	h.tasks.asks = map[string]modapi.BriefAsk{
		"todo_1": {Changed: "fixed bug", Verdict: "done", Outcome: "decide", Recommend: "ship", Decide: true},
	}
	h.tasks.done = []modapi.BriefDone{
		{ID: "todo_1", Title: "Waiting verify", At: now.Add(-time.Hour)}, // excluded from Done because in Accept
		{ID: "todo_2", Title: "Finished", At: now.Add(-2 * time.Hour)},
	}
	h.tasks.failures = []modapi.BriefFailure{
		{Kind: "routine", ID: "morning", Title: "Self retry"},
		{Kind: "routine", ID: "patrol", Title: "Other patrol", Why: "503"},
	}
	h.tasks.failTotal = 2
	h.tasks.setup = &modapi.BriefSetup{Week: "2026-W47", Suggestions: []modapi.BriefSetupSuggestion{{ID: "delegate", Setting: "x", Count: 3, Text: "Enable delegate"}}}
	h.todos.titles["todo_1"] = "Fix login"
	h.todos.open = []modapi.BriefTodoProgress{
		{ID: "todo_1", Title: "Skipped accept", CreatedAt: now.Add(-3 * time.Hour), Active: true},
		{ID: "todo_p2", Title: "Newer", Stage: "develop", CreatedAt: now.Add(-time.Hour), Active: true},
		{ID: "todo_p1", Title: "Older", Stage: "review", CreatedAt: now.Add(-2 * time.Hour), Active: true},
	}

	if err := (Module{}).Register(h); err != nil {
		t.Fatal(err)
	}
	r := h.facts.Runner
	model := r.Start(context.Background(), modapi.BriefTaskInfo{ID: "morning", Label: "Morning", Facts: true}, now)
	if !h.tasks.digestMarked || model != "facts:waiting=1,done=1,failed=1,accept=1,later=0" {
		t.Fatalf("Start: digestMarked=%v model=%q", h.tasks.digestMarked, model)
	}
	if r.TaskRunCount() != 1 {
		t.Fatalf("TaskRunCount = %d, want 1", r.TaskRunCount())
	}

	// Sent attaches the run and writes the `brief` feed item once (retried writes none).
	r.Sent(modapi.BriefScheduleRun{RefID: "morning", SessionID: "sess1", Due: now})
	if r.TaskRunCount() != 0 || !r.HasSession("sess1") || len(h.feed.items) != 1 {
		t.Fatalf("after Sent: tasks=%d has=%v feed=%d", r.TaskRunCount(), r.HasSession("sess1"), len(h.feed.items))
	}
	raw, err := json.Marshal(h.feed.items[0].body)
	if err != nil {
		t.Fatal(err)
	}
	var body modapi.BriefBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body.DoneTotal != 1 || body.FailedTotal != 1 || len(body.Accept) != 1 || body.Accept[0].Title != "Fix login" ||
		body.Accept[0].Recommend != "ship" || len(body.Progress) != 2 || body.Progress[0].ID != "todo_p1" {
		t.Fatalf("body = %+v", body)
	}
	if !strings.Contains(string(raw), `"lines":[]`) || !strings.Contains(string(raw), `"sources":[]`) {
		t.Fatalf("empty wire lists became null: %s", raw)
	}

	// Claim succeeds once for this turn, rejects duplicates with claimed=true, and Push sends counts once.
	run, claimed := r.Claim("sess1", "⏰ Morning", "turn1", false)
	if run == nil || claimed {
		t.Fatalf("first Claim = %v, %v", run, claimed)
	}
	if run2, claimed2 := r.Claim("sess1", "⏰ Morning", "turn1", false); run2 != nil || !claimed2 {
		t.Fatalf("duplicate Claim = %v, %v", run2, claimed2)
	}
	if got := r.Push("sess1", "en", true); got == "" || h.delivery.listed != 1 {
		t.Fatalf("first Push = %q, listed=%d", got, h.delivery.listed)
	}
	if got := r.Push("sess1", "en", true); got != "" {
		t.Fatalf("second Push = %q, want empty", got)
	}
	r.Reached("sess1")
	if h.tasks.setupTold != 1 {
		t.Fatalf("setupTold = %d, want 1", h.tasks.setupTold)
	}
	if g := r.Grace(context.Background(), now, 9*60); g != Grace {
		t.Fatalf("Grace = %d, want %d", g, Grace)
	}
}

func TestRunnerFallbackAndNotStarted(t *testing.T) {
	h := newFakeHost(t)
	h.tasks.northStar = modapi.BriefNorthStar{Lines: []string{"NS line"}, HasStore: true}
	if err := (Module{}).Register(h); err != nil {
		t.Fatal(err)
	}
	r := h.facts.Runner
	now := time.Now().UTC()
	due := now.Truncate(time.Minute)
	task := modapi.BriefTaskInfo{ID: "daily", Label: "Daily", Report: true}
	if got := r.StartReport(context.Background(), task, now); got != "NS line" {
		t.Fatalf("StartReport = %q, want north-star lines", got)
	}
	// Pre-delivery failure delivers from by-task snapshot and schedules one retry; duplicate occurrence is deduped.
	sched := modapi.BriefScheduleRun{ID: "run1", RefID: "daily", Due: due}
	r.NotStarted(context.Background(), task, sched, true)
	if len(h.feed.items) != 1 || !strings.HasPrefix(h.feed.items[0].id, "feed_brief_fallback_") ||
		len(h.delivery.fallbacks) != 1 || !slices.Equal(h.schedule.retried, []string{"daily"}) {
		t.Fatalf("NotStarted: feed=%+v fallbacks=%+v retried=%v", h.feed.items, h.delivery.fallbacks, h.schedule.retried)
	}
	// Second failure for the same routine+due is deduplicated by ClaimFallbackReceipt.
	r.NotStarted(context.Background(), task, sched, false)
	if len(h.delivery.fallbacks) != 1 {
		t.Fatalf("duplicate occurrence delivered %d fallbacks, want 1", len(h.delivery.fallbacks))
	}
}

func TestDayMergesAndBounds(t *testing.T) {
	h := newFakeHost(t)
	now := time.Date(2026, 11, 20, 20, 0, 0, 0, time.UTC)
	start := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	h.dayRecap.recs = []modapi.BriefDayRecording{
		{
			ID: "r1", Part: 1, StartedAt: start, Duration: 3600, Chars: 100,
			SpanStart: start, SpanEnd: now, HasCoverage: true, CoverageTo: 3600,
			Gaps: []modapi.BriefDayCoverageGap{{From: 600, To: 1500, Reason: "interrupted"}},
			Recap: &modapi.BriefDayRecap{
				GeneratedAt: start.Add(2 * time.Hour), CoversTo: 3600, Summary: "Morning summary",
				Timeline: []modapi.BriefDayTimelineEntry{{From: 60, To: 120, Meeting: true, Title: "Standup"}},
				Actions: []modapi.BriefDayRecapAction{
					{At: 100, Who: "Bo", What: "Unconfirmed"},
					{At: 120, Who: "Bo", What: "Already todo", TodoID: "t1"},
				},
			},
		},
		{ID: "r2", Part: 2, StartedAt: start.Add(2 * time.Hour), Duration: 1800, Chars: 0, SpanStart: start.Add(2 * time.Hour), SpanEnd: now},
	}
	if err := (Module{}).Register(h); err != nil {
		t.Fatal(err)
	}
	day := h.facts.Runner.Day(context.Background(), now, time.UTC)
	if day == nil || day.From != "08:00" || day.To != "20:00" || day.Transcribed != "10:30" ||
		len(day.Parts) != 2 || day.Parts[1].State != "none" || day.Parts[1].Recorded != 30 ||
		len(day.Timeline) != 1 || !day.Timeline[0].Meeting || len(day.Actions) != 1 || len(day.Gaps) != 1 {
		t.Fatalf("Day = %+v", day)
	}
}

// max_rows bounds only the non-owner tail, in main as well as every leader. Every owner-wait row remains, even when
// owner rows alone exceed the configured limit; the folded rows become one direction-level "N more" count.
func TestFoldBriefDirectionKeepsEveryOwnerWait(t *testing.T) {
	waits := Defaults()
	now := time.Now().UTC()
	for _, ownerDirection := range []bool{false, true} {
		t.Run(fmt.Sprintf("owner_direction_%v", ownerDirection), func(t *testing.T) {
			d := modapi.BriefDirection{Owner: ownerDirection}
			for i := 0; i < 20; i++ {
				d.Waits = append(d.Waits, modapi.BriefWait{TodoID: fmt.Sprintf("owner-%02d", i), Kind: "needs_user", Since: now.Add(time.Duration(i) * time.Minute)})
			}
			for i := 0; i < 17; i++ {
				d.Waits = append(d.Waits, modapi.BriefWait{TodoID: fmt.Sprintf("other-%02d", i), Kind: "stalled", Since: now.Add(time.Duration(i) * time.Minute)})
			}
			waits.FoldDirection(&d)
			if len(d.Waits) != 35 || d.More != 2 {
				t.Fatalf("kept %d rows, more %d; want 35 and 2", len(d.Waits), d.More)
			}
			for i := 0; i < 20; i++ {
				if d.Waits[i].Kind != "needs_user" {
					t.Fatalf("owner row %d was folded or sorted behind another wait: %+v", i, d.Waits[i])
				}
			}
		})
	}
}
