package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
)

// The reviewer's checks of the snapshot's "Following up" section (docs/assistant/inbox.md "Following up",
// DECISIONS.md B-11): the real snapshot path carries it, reading it writes nothing, and the rendered section keeps the
// snapshot's one-line-per-section shape.

func followingReviewItem(id string, follow *InboxFollow, handling *InboxHandling, tier string) *InboxItem {
	return &InboxItem{ID: id, Source: "lark", Kind: KindDM, Title: "标题 " + id, Where: "地点 " + id, Tier: tier,
		Last: "1", UpdatedAt: time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC), Handling: handling, Follow: follow}
}

// The section comes out of the snapshot the assistant is really handed: taken, fitted and rendered, with the matters it
// selects and nothing else.
func TestSnapshotFollowingThroughTheSnapshot(t *testing.T) {
	h := newInboxHub(t)
	setSettings(t, h.Service, func(v *Settings) { v.Timezone = "Asia/Shanghai" })
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	checkAt := time.Date(2026, 11, 14, 3, 30, 0, 0, time.UTC) // 11:30 in Shanghai
	waiting := followingReviewItem("lark:f1:1", &InboxFollow{Who: "张工", Next: "再问一次进度", CheckAt: &checkAt},
		&InboxHandling{State: HandlingWaiting}, TierAction)
	needs := followingReviewItem("lark:f1:2", nil, &InboxHandling{State: HandlingDone}, TierAction)
	h.inbox.mu.Lock()
	h.inbox.items[waiting.ID], h.inbox.items[needs.ID] = waiting, needs
	h.inbox.mu.Unlock()

	done := make(chan struct{})
	h.Service.takeSnapshot(done)
	<-done
	text := fitSnapshot(*h.Service.live.snap)

	if !strings.Contains(text, "- 在跟的事（1 件）：") {
		t.Fatalf("the snapshot has no Following up section:\n%s", text)
	}
	line := snapshotFollowingLine(t, text, "标题 lark:f1:1")
	for _, want := range []string{"在哪：地点 lark:f1:1", "等谁：张工", "下一步：再问一次进度", "再看：11-14 11:30"} {
		if !strings.Contains(line, want) {
			t.Errorf("following line lacks %q: %s", want, line)
		}
	}
	if strings.Contains(text, "标题 lark:f1:2") {
		t.Errorf("a matter that is not being followed is in the section:\n%s", text)
	}
	if strings.Contains(text, "- 在跟的事：没有") {
		t.Errorf("a snapshot with a followed matter still says there are none:\n%s", text)
	}
	// The section sits with the snapshot's other lines: before the scheduled follow-ups, one top-level line.
	if i, j := strings.Index(text, "- 在跟的事（1 件）："), strings.Index(text, "- 已安排的跟进："); i < 0 || j < 0 || i > j {
		t.Errorf("the section is not before the scheduled follow-ups:\n%s", text)
	}
}

// A cold start is not an empty inbox: a row only in the store is loaded by the snapshot's own read and appears in the
// section. After that, taking the snapshot again writes nothing and changes no item.
func TestSnapshotFollowingLoadsColdInboxAndWritesNothing(t *testing.T) {
	h := newInboxHub(t)
	setSettings(t, h.Service, func(v *Settings) { v.Timezone = "Asia/Shanghai" })
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	checkAt := time.Date(2026, 11, 14, 3, 30, 0, 0, time.UTC)
	it := followingReviewItem("lark:f1:1", &InboxFollow{Who: "张工", Next: "再问一次", CheckAt: &checkAt},
		&InboxHandling{State: HandlingWaiting}, TierAction)
	raw, err := json.Marshal(it)
	if err != nil {
		t.Fatal(err)
	}
	h.inbox.mu.Lock()
	delete(h.inbox.items, it.ID)
	h.inbox.mu.Unlock()
	h.inbox.loaded.Store(false)
	h.st.imu.Lock()
	h.st.rows[it.ID] = raw
	h.st.imu.Unlock()

	done := make(chan struct{})
	h.Service.takeSnapshot(done)
	<-done
	if got := h.Service.inbox.items[it.ID]; got == nil {
		t.Fatal("the snapshot's read did not load the stored item: a cold start reads as an empty inbox")
	}
	h.inbox.mu.Lock()
	before := briefFollowingTestCopy([]InboxItem{*h.inbox.items[it.ID]})[0]
	h.inbox.mu.Unlock()

	done = make(chan struct{})
	h.Service.takeSnapshot(done)
	<-done
	h.st.imu.Lock()
	writes := h.st.ids[it.ID]
	h.st.imu.Unlock()
	if writes != 0 {
		t.Fatalf("reading the snapshot wrote the item %d times", writes)
	}
	h.inbox.mu.Lock()
	after := h.inbox.items[it.ID]
	h.inbox.mu.Unlock()
	if !jsonEqual(t, after, &before) {
		t.Fatalf("reading the snapshot changed the item:\n got %+v\nwant %+v", after, before)
	}
}

func jsonEqual(t *testing.T, got *InboxItem, want *InboxItem) bool {
	t.Helper()
	g, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	w, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	return string(g) == string(w)
}

// The rendered section is one top-level line with indented rows: the node splits the snapshot into sections by that
// shape, so a row on its own top-level line would read as a separate section.
func TestSnapshotFollowingSectionShape(t *testing.T) {
	loc := time.FixedZone("UTC+8", 8*60*60)
	at := time.Date(2026, 11, 14, 3, 30, 0, 0, time.UTC)
	s := snapshotFollowingTestService(
		snapshotFollowingTestItem("a", NowWaiting, &at),
		snapshotFollowingTestItem("b", NowFollowUpAsk, nil),
		snapshotFollowingTestItem("c", NowCheckDue, nil),
	)
	rows, total := s.snapshotFollowing(context.Background(), loc)
	text := prompts.RenderSnapshot(prompts.Snapshot{Now: at, Zone: loc.String(), FollowingEnabled: true, Following: rows, FollowingTotal: total})
	lines := strings.Split(text, "\n")
	i := 0
	for ; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "- 在跟的事") {
			break
		}
	}
	if i == len(lines) {
		t.Fatalf("no Following up section:\n%s", text)
	}
	if !strings.Contains(lines[i], "（3 件）") || !strings.HasSuffix(lines[i], "：") {
		t.Errorf("heading line = %q, want the count and a colon", lines[i])
	}
	rows_ := 0
	for _, line := range lines[i+1:] {
		if line == "" || !strings.HasPrefix(line, "  ") {
			break
		}
		if !strings.HasPrefix(line, "  - 「") {
			t.Errorf("row line without the indented bullet: %q", line)
		}
		rows_++
	}
	if rows_ != 3 {
		t.Errorf("the section has %d row lines, want 3:\n%s", rows_, text)
	}
	if strings.Contains(text, "\n\n") {
		t.Errorf("the section left a blank line:\n%q", text)
	}
}

// A returned matter is due in the briefing whatever its check time says: follow_up_ask and check_due are not limited to
// today's window, only a plain waiting matter is (DECISIONS.md B-11).
func TestBriefFollowingReturnedMattersIgnoreTheCheckTime(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	tomorrow := now.Add(36 * time.Hour)
	ask := InboxItem{ID: "ask", Title: "ask", Now: NowFollowUpAsk,
		Follow: &InboxFollow{CheckAt: &tomorrow, Checks: inboxFollowUpAskChecks}}
	dueNow := InboxItem{ID: "due", Title: "due", Now: NowCheckDue, Follow: &InboxFollow{CheckAt: &tomorrow}}
	waitingToday := InboxItem{ID: "today", Title: "today", Now: NowWaiting, Follow: &InboxFollow{CheckAt: briefFollowingTestTime(now.Add(time.Hour))}}
	waitingTomorrow := InboxItem{ID: "tomorrow", Title: "tomorrow", Now: NowWaiting, Follow: &InboxFollow{CheckAt: &tomorrow}}
	waitingNoCheck := InboxItem{ID: "no-check", Title: "no-check", Now: NowWaiting, Follow: &InboxFollow{CheckAt: &tomorrow}}

	_, due := briefFollowing([]InboxItem{ask, dueNow, waitingToday, waitingTomorrow, waitingNoCheck}, now, time.UTC)
	want := []string{"today", "ask", "due"}
	if got := briefFollowingTestTitles(due); !reflect.DeepEqual(got, want) {
		t.Fatalf("due titles = %v, want %v (the returned ones are due past today)", got, want)
	}
}

// Only the three state keys enter, from the item's own state: a folded matter, and one whose current suggestion puts a
// confirmation key on it, are not being followed in the snapshot or the briefing.
func TestFollowingSelectionIsByStateKey(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	checkAt := now.Add(time.Hour)
	since := now.Add(-48 * time.Hour)
	folded := followingReviewItem("lark:f1:folded", &InboxFollow{CheckAt: &checkAt},
		&InboxHandling{State: HandlingWaiting, Since: &since}, TierNoise)
	confirm := followingReviewItem("lark:f1:confirm", &InboxFollow{CheckAt: &checkAt},
		&InboxHandling{State: HandlingWaiting, Since: &since}, TierAction)
	confirm.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestReply, Text: "草稿"}
	waiting := followingReviewItem("lark:f1:waiting", &InboxFollow{CheckAt: &checkAt},
		&InboxHandling{State: HandlingWaiting, Since: &since}, TierAction)

	s := snapshotFollowingTestService(folded, confirm, waiting)
	rows, total := s.snapshotFollowing(context.Background(), now.Location())
	if total != 1 || len(rows) != 1 || rows[0].Title != "标题 lark:f1:waiting" {
		t.Fatalf("snapshot rows = %+v, total %d; want the waiting matter alone", rows, total)
	}
	items := []InboxItem{}
	for _, it := range []*InboxItem{folded, confirm, waiting} {
		items = append(items, it.listed())
	}
	wantIDs, dueIDs := []string{}, []string{}
	for _, it := range items {
		waitingRows, dueRows := briefFollowing([]InboxItem{it}, now, time.UTC)
		if len(waitingRows) > 0 {
			wantIDs = append(wantIDs, it.ID)
		}
		if len(dueRows) > 0 {
			dueIDs = append(dueIDs, it.ID)
		}
	}
	if !reflect.DeepEqual(wantIDs, []string{"lark:f1:waiting"}) || !reflect.DeepEqual(dueIDs, []string{"lark:f1:waiting"}) {
		t.Fatalf("briefing waiting %v and due %v, want the waiting matter alone in both", wantIDs, dueIDs)
	}
}

func TestSnapshotFollowingRowWithoutFollow(t *testing.T) {
	s := snapshotFollowingTestService(&InboxItem{ID: "bare", Title: "没有 follow 的事件", Tier: TierAction,
		Handling: &InboxHandling{State: HandlingWaiting}})
	rows, total := s.snapshotFollowing(context.Background(), time.UTC)
	if total != 1 || len(rows) != 1 {
		t.Fatalf("rows = %+v, total %d", rows, total)
	}
	line := snapshotFollowingLine(t, prompts.RenderSnapshot(prompts.Snapshot{Now: time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC), Zone: "UTC", FollowingEnabled: true, Following: rows, FollowingTotal: total}), "没有 follow")
	for _, want := range []string{"在哪：没写", "等谁：没写", "下一步：没写", "再看：没定时间"} {
		if !strings.Contains(line, want) {
			t.Errorf("following line lacks %q: %s", want, line)
		}
	}
}

// While Inbox is enabled and available, the section never goes missing: once the last followed matter ends, the
// snapshot says there are none, so a message carrying only changed sections tells the model the rows are gone instead
// of leaving the old ones standing.
func TestSnapshotFollowingSaysNoneWhenTheLastMatterEnds(t *testing.T) {
	h := newInboxHub(t)
	setSettings(t, h.Service, func(v *Settings) { v.Timezone = "Asia/Shanghai" })
	h.setConfig(func(c *assistantwire.InboxConfig) { c.Agent.Enabled = true })
	at := time.Date(2026, 11, 14, 3, 30, 0, 0, time.UTC)
	it := followingReviewItem("lark:f1:1", &InboxFollow{Who: "张工", Next: "再问一次", CheckAt: &at},
		&InboxHandling{State: HandlingWaiting}, TierAction)
	h.inbox.mu.Lock()
	h.inbox.items[it.ID] = it
	h.inbox.mu.Unlock()

	done := make(chan struct{})
	h.Service.takeSnapshot(done)
	<-done
	if text := fitSnapshot(*h.Service.live.snap); !strings.Contains(text, "- 在跟的事（1 件）：") {
		t.Fatalf("the followed matter is not in the section:\n%s", text)
	}

	filedAt := time.Now()
	h.inbox.mu.Lock()
	it.DoneAt, it.DoneBy = &filedAt, "user"
	h.inbox.mu.Unlock()
	done = make(chan struct{})
	h.Service.takeSnapshot(done)
	<-done
	text := fitSnapshot(*h.Service.live.snap)
	if !strings.Contains(text, "- 在跟的事：没有") {
		t.Fatalf("an empty section is missing once the matter ended:\n%s", text)
	}
	if strings.Contains(text, "标题 lark:f1:1") || strings.Contains(text, "在跟的事（0 件）") {
		t.Fatalf("the ended matter is still in the section, or the empty state has a count:\n%s", text)
	}
}
