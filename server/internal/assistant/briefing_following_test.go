package assistant

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// The model-only Inbox facts in a routine briefing (briefing.go). All items and times are synthetic.
func briefFollowingTestTime(at time.Time) *time.Time { return &at }

func briefFollowingTestItem(id, state string, checkAt *time.Time) InboxItem {
	it := InboxItem{ID: id, Title: id, Now: state}
	if checkAt != nil {
		it.Follow = &InboxFollow{CheckAt: checkAt}
	}
	return it
}

func briefFollowingTestTitles(rows []prompts.BriefFollow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Title
	}
	return out
}

// briefFollowingTestCopy copies every field these tests can ask the pure function to read, including pointed-to state.
func briefFollowingTestCopy(items []InboxItem) []InboxItem {
	out := append([]InboxItem(nil), items...)
	for i := range out {
		if items[i].DoneAt != nil {
			at := *items[i].DoneAt
			out[i].DoneAt = &at
		}
		if items[i].Follow != nil {
			f := *items[i].Follow
			if f.CheckAt != nil {
				at := *f.CheckAt
				f.CheckAt = &at
			}
			out[i].Follow = &f
		}
		if items[i].Handling != nil {
			h := *items[i].Handling
			if h.Since != nil {
				at := *h.Since
				h.Since = &at
			}
			out[i].Handling = &h
		}
	}
	return out
}

// Exactly 24 hours is not over a day. Done matters, other states and incomplete handling records never enter either list.
func TestBriefFollowingWaitingAgeThreshold(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	today := now
	items := []InboxItem{
		{ID: "23h", Title: "23h", Now: NowWaiting, Handling: &InboxHandling{Since: briefFollowingTestTime(now.Add(-23 * time.Hour))}},
		{ID: "24h", Title: "24h", Now: NowWaiting, Handling: &InboxHandling{Since: briefFollowingTestTime(now.Add(-24 * time.Hour))}},
		{ID: "25h", Title: "25h", Now: NowWaiting, Handling: &InboxHandling{Since: briefFollowingTestTime(now.Add(-25 * time.Hour))}},
		{ID: "done", Title: "done", Now: NowWaiting, DoneAt: &now, Handling: &InboxHandling{Since: briefFollowingTestTime(now.Add(-48 * time.Hour))}, Follow: &InboxFollow{CheckAt: &today}},
		{ID: "looking", Title: "looking", Now: NowLooking, Handling: &InboxHandling{Since: briefFollowingTestTime(now.Add(-48 * time.Hour))}, Follow: &InboxFollow{CheckAt: &today}},
		{ID: "folded", Title: "folded", Now: NowFolded, Handling: &InboxHandling{State: HandlingWaiting, Since: briefFollowingTestTime(now.Add(-48 * time.Hour))}, Follow: &InboxFollow{CheckAt: &today}},
		{ID: "confirm", Title: "confirm", Now: NowConfirmReply, Handling: &InboxHandling{State: HandlingWaiting, Since: briefFollowingTestTime(now.Add(-48 * time.Hour))}, Follow: &InboxFollow{CheckAt: &today}},
		{ID: "nil-handling", Title: "nil-handling", Now: NowWaiting},
		{ID: "nil-since", Title: "nil-since", Now: NowWaiting, Handling: &InboxHandling{}},
	}
	waiting, due := briefFollowing(items, now, time.UTC)
	want := []prompts.BriefFollow{{Title: "25h"}}
	if !reflect.DeepEqual(waiting, want) {
		t.Fatalf("waiting = %+v, want %+v", waiting, want)
	}
	if len(due) != 0 {
		t.Fatalf("due = %+v, want no rows", due)
	}
}

// At 18:00 UTC it is already 02:00 the next day in Shanghai. The whole local day belongs in due, not the UTC day.
func TestBriefFollowingTodayUsesOwnerZoneAcrossUTCDate(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 13, 18, 0, 0, 0, time.UTC)     // 2026-11-14 02:00 in loc.
	day := time.Date(2026, 11, 13, 16, 0, 0, 0, time.UTC)     // 2026-11-14 00:00 in loc.
	nextDay := time.Date(2026, 11, 14, 16, 0, 0, 0, time.UTC) // 2026-11-15 00:00 in loc.
	items := []InboxItem{
		briefFollowingTestItem("later-today", NowWaiting, briefFollowingTestTime(now.Add(2*time.Hour))),
		briefFollowingTestItem("tomorrow", NowWaiting, briefFollowingTestTime(nextDay)),
		briefFollowingTestItem("yesterday", NowWaiting, briefFollowingTestTime(day.Add(-time.Nanosecond))),
		briefFollowingTestItem("already-due", NowWaiting, briefFollowingTestTime(now.Add(-time.Hour))),
		briefFollowingTestItem("local-start", NowWaiting, briefFollowingTestTime(day)),
	}
	waiting, due := briefFollowing(items, now, loc)
	if len(waiting) != 0 {
		t.Fatalf("waiting = %+v, want no rows", waiting)
	}
	want := []string{"local-start", "already-due", "later-today"}
	if got := briefFollowingTestTitles(due); !reflect.DeepEqual(got, want) {
		t.Fatalf("due titles = %v, want %v", got, want)
	}
}

// A matter already handed back is due even without a check time. Done and unrelated states stay out.
func TestBriefFollowingAlwaysListsReturnedFollowUps(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	items := []InboxItem{
		{ID: "follow-up", Title: "follow-up", Now: NowFollowUpAsk, Follow: &InboxFollow{Who: "Owner", Next: "Decide whether to continue"}},
		briefFollowingTestItem("check-due", NowCheckDue, nil),
		{ID: "done-check", Title: "done-check", Now: NowCheckDue, DoneAt: &now},
		briefFollowingTestItem("needs-you", NowNeedsYou, briefFollowingTestTime(now)),
		briefFollowingTestItem("ordinary-waiting", NowWaiting, nil),
	}
	waiting, due := briefFollowing(items, now, time.UTC)
	if len(waiting) != 0 {
		t.Fatalf("waiting = %+v, want no rows", waiting)
	}
	want := []prompts.BriefFollow{
		{Title: "check-due"},
		{Title: "follow-up", Who: "Owner", Next: "Decide whether to continue"},
	}
	if !reflect.DeepEqual(due, want) {
		t.Fatalf("due = %+v, want %+v", due, want)
	}
}

// Both lists are deterministic and capped at five: age and check time first, ids breaking ties, missing check times last.
func TestBriefFollowingSortsAndLimits(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	waitingItems := []InboxItem{
		{ID: "w-new", Title: "w-new", Now: NowWaiting, Handling: &InboxHandling{Since: briefFollowingTestTime(now.Add(-26 * time.Hour))}},
		{ID: "w-tie-b", Title: "w-tie-b", Now: NowWaiting, Handling: &InboxHandling{Since: briefFollowingTestTime(now.Add(-40 * time.Hour))}},
		{ID: "w-old", Title: "w-old", Now: NowWaiting, Handling: &InboxHandling{Since: briefFollowingTestTime(now.Add(-50 * time.Hour))}},
		{ID: "w-extra", Title: "w-extra", Now: NowWaiting, Handling: &InboxHandling{Since: briefFollowingTestTime(now.Add(-25 * time.Hour))}},
		{ID: "w-tie-a", Title: "w-tie-a", Now: NowWaiting, Handling: &InboxHandling{Since: briefFollowingTestTime(now.Add(-40 * time.Hour))}},
		{ID: "w-mid", Title: "w-mid", Now: NowWaiting, Handling: &InboxHandling{Since: briefFollowingTestTime(now.Add(-30 * time.Hour))}},
	}
	waiting, _ := briefFollowing(waitingItems, now, time.UTC)
	wantWaiting := []string{"w-old", "w-tie-a", "w-tie-b", "w-mid", "w-new"}
	if got := briefFollowingTestTitles(waiting); !reflect.DeepEqual(got, wantWaiting) {
		t.Fatalf("waiting titles = %v, want %v", got, wantWaiting)
	}

	day := dayStart(now)
	tie := day.Add(2 * time.Hour)
	dueItems := []InboxItem{
		briefFollowingTestItem("d-nil-b", NowFollowUpAsk, nil),
		briefFollowingTestItem("d-later", NowWaiting, briefFollowingTestTime(day.Add(5*time.Hour))),
		briefFollowingTestItem("d-tie-b", NowWaiting, briefFollowingTestTime(tie)),
		briefFollowingTestItem("d-first", NowWaiting, briefFollowingTestTime(day.Add(time.Hour))),
		briefFollowingTestItem("d-nil-a", NowCheckDue, nil),
		briefFollowingTestItem("d-tie-a", NowWaiting, briefFollowingTestTime(tie)),
	}
	_, due := briefFollowing(dueItems, now, time.UTC)
	wantDue := []string{"d-first", "d-tie-a", "d-tie-b", "d-later", "d-nil-a"}
	if got := briefFollowingTestTitles(due); !reflect.DeepEqual(got, wantDue) {
		t.Fatalf("due titles = %v, want %v", got, wantDue)
	}
}

// Only one-line, clipped title / who / next cross into the model. A matter can be in both lists; the input is untouched.
func TestBriefFollowingFormatsTextWithoutChangingInput(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	checkAt := now.Add(time.Hour)
	items := []InboxItem{
		{
			ID: "both", Title: "  Weekly report\nneeds a reply  ", Now: NowWaiting,
			Handling: &InboxHandling{State: HandlingWaiting, Since: briefFollowingTestTime(now.Add(-25 * time.Hour))},
			Follow:   &InboxFollow{Who: "  Reviewer\n team ", Next: " Ask\nfor an update ", CheckAt: &checkAt, Checks: 2},
		},
		{
			ID: "long", Title: strings.Repeat("t", 61), Now: NowCheckDue,
			Follow: &InboxFollow{Who: strings.Repeat("w", 81), Next: strings.Repeat("n", 81)},
		},
		{ID: "nil-follow", Title: "No follow", Now: NowFollowUpAsk},
	}
	before := briefFollowingTestCopy(items)
	waiting, due := briefFollowing(items, now, time.UTC)
	plain := prompts.BriefFollow{Title: "Weekly report needs a reply", Who: "Reviewer team", Next: "Ask for an update"}
	if want := []prompts.BriefFollow{plain}; !reflect.DeepEqual(waiting, want) {
		t.Fatalf("waiting = %+v, want %+v", waiting, want)
	}
	wantDue := []prompts.BriefFollow{
		plain,
		{Title: strings.Repeat("t", 60) + "…", Who: strings.Repeat("w", 80) + "…", Next: strings.Repeat("n", 80) + "…"},
		{Title: "No follow"},
	}
	if !reflect.DeepEqual(due, wantDue) {
		t.Fatalf("due = %+v, want %+v", due, wantDue)
	}
	if !reflect.DeepEqual(items, before) {
		t.Fatalf("the input changed:\n got %+v\nwant %+v", items, before)
	}
}

// A cold-start briefing loads the Inbox, then carries both lists to the model even when a card carries the other facts.
func TestBriefFactsIncludeInboxFollowUps(t *testing.T) {
	e := newBriefEnv(t)
	setSettings(t, e.s, func(v *Settings) { v.Timezone = "UTC" })
	waitNeedsYouIdle(t, e.s)
	e.setFeatures(FeishuFeatures{Main: true, Board: true, Pushes: true})
	e.s.inbox.init()
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	since, checkAt := now.Add(-25*time.Hour), now.Add(time.Hour)
	item := &InboxItem{
		ID: "inbox-follow", Title: "Inbox reply", Handling: &InboxHandling{State: HandlingWaiting, Since: &since},
		Follow: &InboxFollow{Who: "Reviewer", Next: "Ask for a reply", CheckAt: &checkAt, Checks: 2},
	}
	before := briefFollowingTestCopy([]InboxItem{*item})[0]
	e.s.inbox.mu.Lock()
	e.s.inbox.items[item.ID] = item
	e.s.inbox.mu.Unlock()

	facts := e.s.briefFactsAt(context.Background(), now, "")
	want := []prompts.BriefFollow{{Title: "Inbox reply", Who: "Reviewer", Next: "Ask for a reply"}}
	if !reflect.DeepEqual(facts.InboxWaiting, want) {
		t.Fatalf("InboxWaiting = %+v, want %+v", facts.InboxWaiting, want)
	}
	if !reflect.DeepEqual(facts.InboxDue, want) {
		t.Fatalf("InboxDue = %+v, want %+v", facts.InboxDue, want)
	}
	text := facts.modelText()
	if !facts.Card || !e.s.inbox.loaded.Load() {
		t.Fatalf("Card = %v, inbox loaded = %v; want a card and a completed cold-start load", facts.Card, e.s.inbox.loaded.Load())
	}
	if strings.Count(text, item.Title) < 2 || !strings.Contains(text, item.Follow.Who) || !strings.Contains(text, item.Follow.Next) ||
		!strings.Contains(text, "即使上面有卡片") {
		t.Fatalf("the model did not receive both follow-up facts with the card exception:\n%s", text)
	}
	if !reflect.DeepEqual(*item, before) {
		t.Fatalf("the briefing changed the item:\n got %+v\nwant %+v", *item, before)
	}
}
