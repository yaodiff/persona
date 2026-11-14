package assistant

import (
	"context"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/prompts"
)

func snapshotFollowingTestItem(id, state string, checkAt *time.Time) *InboxItem {
	it := &InboxItem{
		ID: id, Title: "标题 " + id, Where: "地点 " + id, Tier: TierAction,
		Handling: &InboxHandling{State: HandlingDone},
		Follow:   &InboxFollow{Who: "对方 " + id, Next: "继续 " + id, CheckAt: checkAt},
		Last:     "new-" + id, ArrivedOf: "old-" + id,
	}
	switch state {
	case NowWaiting:
		it.Handling.State = HandlingWaiting
	case NowFollowUpAsk:
		it.Follow.Checks = inboxFollowUpAskChecks
	case NowCheckDue:
		it.Follow.Due = true
	}
	return it
}

func snapshotFollowingTestService(items ...*InboxItem) *Service {
	s := &Service{}
	s.inbox.init()
	s.inbox.loaded.Store(true)
	for _, it := range items {
		s.inbox.items[it.ID] = it
	}
	return s
}

func snapshotFollowingFilterItems() []*InboxItem {
	at := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	doneAt := at.Add(-time.Hour)
	return []*InboxItem{
		snapshotFollowingTestItem("waiting", NowWaiting, &at),
		snapshotFollowingTestItem("ask", NowFollowUpAsk, nil),
		snapshotFollowingTestItem("due", NowCheckDue, nil),
		{
			ID: "waiting-suggestion", Title: "等待中带着建议", Tier: TierAction,
			Handling: &InboxHandling{State: HandlingWaiting}, Suggestion: &InboxSuggestion{Kind: SuggestReply},
			Follow: &InboxFollow{CheckAt: &at},
		},
		{
			ID: "folded-waiting", Title: "已折叠", Tier: TierNoise,
			Handling: &InboxHandling{State: HandlingWaiting}, Follow: &InboxFollow{CheckAt: &at},
		},
		{
			ID: "done", Title: "已经了结", Tier: TierAction, DoneAt: &doneAt, DoneBy: "user",
			Handling: &InboxHandling{State: HandlingWaiting},
		},
		{
			ID: "looking", Title: "正在处理", Tier: TierAction,
			Handling: &InboxHandling{State: HandlingLooking},
		},
		{
			ID: "needs", Title: "普通需要你", Tier: TierAction,
			Handling: &InboxHandling{State: HandlingDone},
		},
	}
}

// Only the three follow-up states enter the snapshot, and reading them never stamps or otherwise changes an item.
func TestFollowingInboxItemsFiltersAndDoesNotMutate(t *testing.T) {
	before := map[string]*InboxItem{}
	for _, it := range snapshotFollowingFilterItems() {
		before[it.ID] = it
	}
	s := snapshotFollowingTestService(snapshotFollowingFilterItems()...)

	got := s.followingInboxItems(context.Background())
	states := map[string]string{}
	for _, it := range got {
		states[it.ID] = it.Now
	}
	want := map[string]string{"waiting": NowWaiting, "ask": NowFollowUpAsk, "due": NowCheckDue}
	if !reflect.DeepEqual(states, want) {
		t.Fatalf("following items = %v, want %v", states, want)
	}
	rows, total := s.snapshotFollowing(context.Background(), time.UTC)
	if len(rows) != 3 || total != 3 {
		t.Fatalf("snapshot has %d rows and total %d, want 3 and 3", len(rows), total)
	}
	if !reflect.DeepEqual(s.inbox.items, before) {
		t.Fatal("reading the following snapshot changed the inbox items")
	}

	for i := range got {
		if got[i].ID == "waiting" {
			got[i].Follow.Who = "changed in the returned copy"
		}
	}
	if s.inbox.items["waiting"].Follow.Who == "changed in the returned copy" {
		t.Fatal("the returned item shares its follow data with the inbox")
	}
}

func TestSnapshotFollowingRendersEmptyState(t *testing.T) {
	s := snapshotFollowingTestService()
	rows, total := s.snapshotFollowing(context.Background(), time.UTC)
	if len(rows) != 0 || total != 0 {
		t.Fatalf("empty inbox gave %d rows and total %d", len(rows), total)
	}
	out := prompts.RenderSnapshot(prompts.Snapshot{Now: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC), Zone: "UTC", FollowingEnabled: true})
	if !strings.Contains(out, "- 在跟的事：没有\n") || strings.Contains(out, "在跟的事（0 件）") {
		t.Fatalf("the empty following state is missing or has an empty count:\n%s", out)
	}
}

func snapshotFollowingLine(t *testing.T, text, title string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, title) {
			return line
		}
	}
	t.Fatalf("no line contains %q:\n%s", title, text)
	return ""
}

// Check times order the rows, ids break ties, missing times come last, and the total survives the five-row bound.
func TestSnapshotFollowingSortsCapsAndUsesOwnerZone(t *testing.T) {
	early := time.Date(2026, 11, 13, 0, 30, 0, 0, time.UTC)
	tie := early.Add(time.Hour)
	late := early.Add(2 * time.Hour)
	s := snapshotFollowingTestService(
		snapshotFollowingTestItem("nil-b", NowCheckDue, nil),
		snapshotFollowingTestItem("tie-b", NowWaiting, &tie),
		snapshotFollowingTestItem("late", NowWaiting, &late),
		snapshotFollowingTestItem("nil-a", NowFollowUpAsk, nil),
		snapshotFollowingTestItem("early", NowWaiting, &early),
		snapshotFollowingTestItem("tie-a", NowWaiting, &tie),
		snapshotFollowingTestItem("nil-c", NowCheckDue, nil),
	)
	loc := time.FixedZone("UTC+8", 8*60*60)
	rows, total := s.snapshotFollowing(context.Background(), loc)
	if total != 7 || len(rows) != 5 {
		t.Fatalf("got total %d and %d rows, want 7 and 5", total, len(rows))
	}
	wantTitles := []string{"标题 early", "标题 tie-a", "标题 tie-b", "标题 late", "标题 nil-a"}
	for i, want := range wantTitles {
		if rows[i].Title != want {
			t.Errorf("row %d title = %q, want %q", i, rows[i].Title, want)
		}
	}
	if rows[0].CheckAt.Location() != loc {
		t.Errorf("check time is in %v, want %v", rows[0].CheckAt.Location(), loc)
	}
	if got := rows[0].CheckAt.Format("01-02 15:04"); got != "11-13 08:30" {
		t.Errorf("check time = %q, want 11-13 08:30", got)
	}
	if !rows[4].CheckAt.IsZero() {
		t.Errorf("a missing check time did not sort last: %v", rows[4].CheckAt)
	}

	snap := prompts.Snapshot{Now: early, Zone: loc.String(), FollowingEnabled: true, Following: rows, FollowingTotal: total}
	out := prompts.RenderSnapshot(snap)
	if !strings.Contains(out, "还有 2 件") {
		t.Errorf("the rows beyond the bound are not counted:\n%s", out)
	}
	line := snapshotFollowingLine(t, out, "标题 early")
	for _, want := range []string{"在哪：地点 early", "等谁：对方 early", "下一步：继续 early", "再看：11-13 08:30"} {
		if !strings.Contains(line, want) {
			t.Errorf("following line lacks %q: %s", want, line)
		}
	}
}

func TestSnapshotFollowingStateNotes(t *testing.T) {
	s := snapshotFollowingTestService(
		snapshotFollowingTestItem("ask", NowFollowUpAsk, nil),
		snapshotFollowingTestItem("due", NowCheckDue, nil),
	)
	rows, total := s.snapshotFollowing(context.Background(), time.UTC)
	out := prompts.RenderSnapshot(prompts.Snapshot{Now: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC), Zone: "UTC", FollowingEnabled: true, Following: rows, FollowingTotal: total})
	ask := snapshotFollowingLine(t, out, "标题 ask")
	if !strings.Contains(ask, "看过 3 次没动静") {
		t.Errorf("follow-up ask has no three-check note: %s", ask)
	}
	due := snapshotFollowingLine(t, out, "标题 due")
	if !strings.Contains(due, "再看：到了再看的时间") {
		t.Errorf("due check has no due-time note: %s", due)
	}
}

func TestSnapshotFollowingClipsFreeText(t *testing.T) {
	at := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	it := snapshotFollowingTestItem("long", NowWaiting, &at)
	it.Title = "标题\n" + strings.Repeat("长", 80)
	it.Where = "地点\n" + strings.Repeat("远", 60)
	it.Follow.Who = "对方\n" + strings.Repeat("人", 60)
	it.Follow.Next = "下一步\n" + strings.Repeat("做", 100)
	rows, _ := snapshotFollowingTestService(it).snapshotFollowing(context.Background(), time.UTC)
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	checks := map[string]struct {
		value string
		max   int
	}{
		"title": {rows[0].Title, 61},
		"where": {rows[0].Where, 41},
		"who":   {rows[0].Who, 41},
		"next":  {rows[0].Next, 81},
	}
	for name, check := range checks {
		if strings.ContainsAny(check.value, "\r\n") {
			t.Errorf("%s still spans lines: %q", name, check.value)
		}
		if got := utf8.RuneCountInString(check.value); got > check.max {
			t.Errorf("%s has %d characters, limit including the ellipsis is %d", name, got, check.max)
		}
	}
}

// The inbox section has its own omission step before scheduled follow-ups: its heading and total stay within budget.
func TestFitSnapshotFollowingOmitsBeforeFollowups(t *testing.T) {
	var omitted prompts.SnapshotOmit
	snapshotOmits[2](&omitted)
	if !omitted.Following || omitted.Followups {
		t.Fatalf("the third omission = %+v, want Following alone", omitted)
	}
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	following := make([]prompts.SnapshotFollowing, 5)
	for i := range following {
		following[i].Title = "不应出现的跟进标题"
	}
	followups := make([]prompts.SnapshotFollowup, 80)
	for i := range followups {
		followups[i] = prompts.SnapshotFollowup{At: now, Label: strings.Repeat("定时跟进", 20)}
	}
	s := prompts.Snapshot{
		Now: now, Zone: "UTC", FollowingEnabled: true, Following: following, FollowingTotal: 7,
		Followups: followups, FollowupsTotal: len(followups),
	}
	if got := utf8.RuneCountInString(prompts.RenderSnapshot(s)); got <= snapshotBudget {
		t.Fatalf("fixture has only %d characters, want more than %d", got, snapshotBudget)
	}
	out := fitSnapshot(s)
	if got := utf8.RuneCountInString(out); got > snapshotBudget {
		t.Fatalf("fitted snapshot has %d characters, budget is %d", got, snapshotBudget)
	}
	want := "- 在跟的事（7 件）：这份快照放不下，没列出来，收件箱在 App 里看"
	if !strings.Contains(out, want) {
		t.Errorf("omitted following section lost its total or explanation %q:\n%s", want, out)
	}
	if strings.Contains(out, "不应出现的跟进标题") {
		t.Errorf("omitted following details remained:\n%s", out)
	}
	if strings.Contains(out, strings.TrimSpace(prompts.SnapshotCut())) {
		t.Errorf("the follow-up omissions should make this fixture fit without cutting:\n%s", out)
	}
}

func TestSnapshotCutNamesOnlyRegisteredTools(t *testing.T) {
	cut := prompts.SnapshotCut()
	if regexp.MustCompile(`\binbox\b`).MatchString(cut) {
		t.Errorf("cut text names a standalone inbox tool: %s", cut)
	}
	names := regexp.MustCompile(`[a-z][a-z0-9]*(?:_[a-z0-9]+)+`).FindAllString(cut, -1)
	if len(names) == 0 {
		t.Fatalf("no underscored tool names found in %q", cut)
	}
	declared := map[string]bool{}
	for _, name := range toolNames {
		declared[name] = true
	}
	offered := map[string]bool{}
	for _, tool := range (&Service{}).Tools(context.Background()) {
		offered[tool.Name] = true
	}
	for _, name := range names {
		if !declared[name] || !offered[name] {
			t.Errorf("cut text names unregistered tool %q (declared %v, offered %v)", name, declared[name], offered[name])
		}
	}
}
