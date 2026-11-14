package assistant

import (
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// bigSnapshot fills every section the snapshot can carry, in Chinese as the real one is (a character is three bytes,
// the budget counts characters), with lines as long as the sources let them be (#3203).
func bigSnapshot(now time.Time) prompts.Snapshot {
	line := strings.Repeat("构建在等评审结论", 20) // 160 characters, the agents' line limit
	agents := func(n int, machine string) []prompts.SnapshotAgent {
		out := make([]prompts.SnapshotAgent, n)
		for i := range out {
			out[i] = prompts.SnapshotAgent{Label: "assistant › 整理发布说明", Machine: machine, Session: "01a1c3f3", Line: line,
				Link: "http://hub.example:18660/s/hub-1/01a1c3f3", Plan: &prompts.SnapshotAgentPlan{Done: 2, Total: 5, Current: "跑全量测试"}}
		}
		return out
	}
	tasks := make([]prompts.SnapshotTask, 12)
	for i := range tasks {
		tasks[i] = prompts.SnapshotTask{ID: "todo_1", Title: "落一份很长的报告", State: "在做", Last: line[:80*3], Doing: line[:80*3]}
	}
	unaccepted := make([]prompts.SnapshotAccept, 8)
	for i := range unaccepted {
		unaccepted[i] = prompts.SnapshotAccept{Title: "上线了", Verdict: line[:120*3], Changed: line[:60*3], Since: now}
	}
	followups := make([]prompts.SnapshotFollowup, 8)
	for i := range followups {
		followups[i] = prompts.SnapshotFollowup{At: now.Add(time.Hour), Label: "看一眼构建结果"}
	}
	return prompts.Snapshot{
		Now: now, Zone: "Asia/Shanghai",
		Machines: []prompts.SnapshotMachine{{Name: "the hub machine", ID: "hub-1", Online: true}},
		Needs:    agents(6, "hub-1"), Unread: agents(6, "build-1"), Awaiting: agents(4, "build-1"),
		Waiting: agents(6, "build-2"), Done: agents(20, "build-3"), Muted: agents(4, "build-4"),
		Working: agents(12, "hub-1"), Workflows: []prompts.SnapshotWorkflow{{Agent: "assistant › a", Machine: "build-1", Session: "s1", Name: "audit", Phase: "Verify", PhaseIndex: 2, Phases: 2, Done: 1, Started: 2, For: "3 minutes"}},
		Chats:    &prompts.SnapshotChats{AtMe: []prompts.SnapshotChat{{Name: "发布群", Unread: 3, Preview: line[:40*3]}}, Other: []prompts.SnapshotChat{{Name: "运维群", Unread: 9}}},
		Calendar: &prompts.SnapshotCalendar{Events: []prompts.SnapshotEvent{{Start: now, End: now.Add(time.Hour), Title: "周会"}}},
		KB:       strings.Repeat("知识库条目很长很长。\n", 30),
		Tasks:    tasks, Unaccepted: unaccepted, UnacceptedTotal: 12,
		Approvals: []prompts.SnapshotApproval{{ID: "todo_2", Title: "合并", Stage: "CI 绿", Gate: "等你点头"}},
		Plans:     []prompts.SnapshotPlan{{At: now, Titles: []string{"a", "b"}, Tasks: 2}},
		Asked:     []prompts.SnapshotAsked{{TodoID: "todo_3", Question: "先发哪个？", At: now}},
		Followups: followups, FollowupsTotal: 11,
		FollowingEnabled: true,
		Following:        []prompts.SnapshotFollowing{{Title: "跟进的报价", Where: "项目群", Who: "评审", Next: "再问一次", CheckAt: now}}, FollowingTotal: 1,
		Routines: []prompts.SnapshotRoutine{{Label: "早报", Rule: "每天 09:00"}}, RoutinesTotal: 1,
		Missed: []prompts.SnapshotMiss{{At: now, Label: "清单", Why: line[:60*3]}}, MissedTotal: 1,
	}
}

var fitNow = time.Date(2026, 11, 10, 9, 30, 0, 0, time.UTC)

func runes(s string) int { return utf8.RuneCountInString(s) }

func TestFitSnapshotLeavesASmallSnapshotAlone(t *testing.T) {
	s := prompts.Snapshot{Now: fitNow, Zone: "Asia/Shanghai",
		Needs:    []prompts.SnapshotAgent{{Label: "assistant › a", Machine: "build-1", Session: "s1", Line: "在等"}},
		Routines: []prompts.SnapshotRoutine{{Label: "早报", Rule: "每天 09:00"}}, RoutinesTotal: 1}
	if got, want := fitSnapshot(s), prompts.RenderSnapshot(s); got != want {
		t.Fatalf("a snapshot under the budget changed:\n%s\nwant\n%s", got, want)
	}
}

func TestSnapshotDoneAndMutedStayCounts(t *testing.T) {
	s := prompts.Snapshot{Now: fitNow, Zone: "Asia/Shanghai",
		Needs:    []prompts.SnapshotAgent{{Label: "等你批准发布", Machine: "build-1", Session: "need-1", Line: "可以合并吗"}},
		Awaiting: []prompts.SnapshotAgent{{Label: "看过还没回复", Machine: "build-1", Session: "wait-1", Line: "选哪个方案"}},
		Done:     []prompts.SnapshotAgent{{Label: "已经修完的登录问题", Machine: "build-2", Session: "done-1", Line: "测试通过"}},
		Muted:    []prompts.SnapshotAgent{{Label: "已经屏蔽的巡检", Machine: "build-3", Session: "mute-1", Line: "没有新事"}}}
	got := fitSnapshot(s)
	for _, want := range []string{
		"  - 做完的（24 小时内）：1 个，agents_list state=done 查",
		"  - 已屏蔽（用户关了它们的通知，上面的数不算它们；别主动提，问到再说）：1 个，agents_list state=muted 查",
		"等你批准发布", "看过还没回复", "可以合并吗", "选哪个方案",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"已经修完的登录问题", "已经屏蔽的巡检", "done-1", "mute-1", "测试通过", "没有新事"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("done or muted details stayed in an under-budget snapshot (%q):\n%s", unwanted, got)
		}
	}
}

// Over the budget, sections are left out until it fits, and what waits for the user stays.
func TestFitSnapshotStaysInBudget(t *testing.T) {
	s := bigSnapshot(fitNow)
	if n := runes(prompts.RenderSnapshot(s)); n <= snapshotBudget {
		t.Fatalf("the fixture has to be over the budget to prove anything (%d characters)", n)
	}
	got := fitSnapshot(s)
	if n := runes(got); n > snapshotBudget {
		t.Fatalf("the fitted snapshot has %d characters, the budget is %d", n, snapshotBudget)
	}
	if strings.Contains(got, strings.TrimSpace(prompts.SnapshotCut())) {
		t.Fatalf("leaving sections out should have been enough, it was cut:\n%s", got)
	}
	for _, want := range []string{"  - 需要你：", "  - 在问用户、用户看过还没回的", "后台 workflow 在跑", "等用户点「开始」的计划", "没成功的定时任务",
		"@我：", "周会"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q, which is never left out, is missing:\n%s", want, got)
		}
	}
	if strings.Count(got, "session_id=01a1c3f3") < 6+4 {
		t.Errorf("an agent waiting for the user went missing:\n%s", got)
	}
}

// A section left out keeps its heading and its count, and says where the rest is: never "none", never a count of 0,
// and never gone (the node sends only what changed, so a missing section would read as unchanged).
func TestFitSnapshotKeepsCountsOfWhatItLeavesOut(t *testing.T) {
	got := fitSnapshot(bigSnapshot(fitNow))
	for _, want := range []string{
		"- Agents：需要你 6 个，完成未看 6 个，工作中 12 个",
		"  - 做完的（24 小时内）：20 个，agents_list state=done 查",
		"  - 已屏蔽（用户关了它们的通知，上面的数不算它们；别主动提，问到再说）：4 个，agents_list state=muted 查",
		"- 已安排的跟进：11 个，这份快照放不下，没列出来，要看用 list_followups",
		"- 知识库（kb_get 看详情）：这份快照放不下",
		"- 飞书未读会话 2 个",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	for _, wrong := range []string{"已安排的跟进：没有", "飞书：没有未读", "工作中 0 个", "完成未看 0 个"} {
		if strings.Contains(got, wrong) {
			t.Errorf("a section left out reads as empty (%q):\n%s", wrong, got)
		}
	}
}

// Every section the template can leave out is in the order fitSnapshot goes by, and leaving it out shows: a new
// SnapshotOmit field the order forgets, or the template ignores, fails here.
func TestFitSnapshotCoversEverySection(t *testing.T) {
	full := bigSnapshot(fitNow)
	var all prompts.SnapshotOmit
	for _, omit := range snapshotOmits {
		before := all
		omit(&all)
		if all == before {
			t.Error("an entry of snapshotOmits leaves nothing out")
		}
		one := prompts.SnapshotOmit{}
		omit(&one)
		s := full
		s.Omit = one
		if prompts.RenderSnapshot(s) == prompts.RenderSnapshot(full) {
			t.Errorf("the template ignores %+v (or the fixture does not reach its section)", one)
		}
	}
	v := reflect.ValueOf(all)
	for i := range v.NumField() {
		if !v.Field(i).Bool() {
			t.Errorf("SnapshotOmit.%s is in no entry of snapshotOmits", v.Type().Field(i).Name)
		}
	}
}

// The last section left out is the one that makes it fit: it renders, it does not fall through to the cut (#3203:
// the cut sliced past the end of a short block and panicked).
func TestFitSnapshotWhenTheLastOmitFits(t *testing.T) {
	approvals := make([]prompts.SnapshotApproval, 40)
	for i := range approvals {
		approvals[i] = prompts.SnapshotApproval{ID: "todo_9", Title: strings.Repeat("上线新版发布流程", 25), Stage: "CI 绿", Gate: "等你点头"}
	}
	s := prompts.Snapshot{Now: fitNow, Zone: "UTC", Approvals: approvals}
	got := fitSnapshot(s)
	if n := runes(got); n > snapshotBudget {
		t.Fatalf("%d characters, the budget is %d", n, snapshotBudget)
	}
	if !strings.Contains(got, "- 等用户点头才能上线的（40 件；合到一起一次列给用户批，别一件件问）：这份快照放不下") {
		t.Fatalf("the approvals were not left out with their count:\n%s", got)
	}
	if strings.Contains(got, strings.TrimSpace(prompts.SnapshotCut())) {
		t.Fatalf("it fits once the approvals are out, but it was cut:\n%s", got)
	}
}

// What no section can be left out of is cut, within the budget with the line that says so, at a rune boundary; the
// agents waiting for the user, which come first, stay.
func TestFitSnapshotCutsWhatCannotBeLeftOut(t *testing.T) {
	long := strings.Repeat("等你决定先发哪个版本", 800) // 8000 characters
	for name, s := range map[string]prompts.Snapshot{
		"needs you": {Now: fitNow, Zone: "UTC",
			Needs:    []prompts.SnapshotAgent{{Label: "assistant › a", Machine: "m", Session: "s1", Line: "在等你"}},
			Awaiting: []prompts.SnapshotAgent{{Label: "assistant › b", Machine: "m", Session: "s2", Line: long}}},
		"one line": {Now: fitNow, Zone: "UTC", Needs: []prompts.SnapshotAgent{{Label: "assistant › a", Machine: "m", Session: "s1", Line: long}}},
	} {
		got := fitSnapshot(s)
		if n := runes(got); n > snapshotBudget {
			t.Errorf("%s: %d characters, the budget is %d", name, n, snapshotBudget)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: cut inside a character", name)
		}
		if !strings.HasSuffix(got, "\n"+prompts.SnapshotCut()) {
			t.Errorf("%s: does not end with the line saying it was cut:\n%s", name, got)
		}
		if !strings.Contains(got, "session_id=s1") {
			t.Errorf("%s: the agent waiting for the user was cut:\n%s", name, got)
		}
	}
}

// fitSnapshot gets the cached snapshot's lists, which every message renders at the same time: it changes none of
// them (run with -race).
func TestFitSnapshotLeavesTheCachedSnapshotAlone(t *testing.T) {
	cached := bigSnapshot(fitNow)
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { fitSnapshot(cached) })
	}
	wg.Wait()
	if !reflect.DeepEqual(cached, bigSnapshot(fitNow)) {
		t.Fatal("fitting changed the cached snapshot")
	}
}

// The free text a snapshot carries is cut where it is taken, so one long line cannot crowd the rest out (clip adds
// its ellipsis after the limit).
func TestSnapshotSourcesClipFreeText(t *testing.T) {
	long := strings.Repeat("一段很长的话\n", 100)
	if p := snapshotPlan(proto.Session{Plan: &proto.PlanInfo{Done: 1, Total: 3, Current: long}}); runes(p.Current) > 61 || strings.Contains(p.Current, "\n") {
		t.Errorf("plan step kept %d characters: %q", runes(p.Current), p.Current)
	}
	var task prompts.SnapshotTask
	reported(&task, &TaskProgress{Kind: "progress", Text: long, At: fitNow}, proto.Session{}, fitNow)
	if runes(task.Reported.Text) > 161 || strings.Contains(task.Reported.Text, "\n") {
		t.Errorf("report kept %d characters: %q", runes(task.Reported.Text), task.Reported.Text)
	}
	got := snapshotTask(Todo{ID: "todo_1", Title: long}, "working", []proto.Session{{SessionID: "s1", State: proto.StateWorking, Activity: long}}, fitNow)
	if runes(got.Title) > 61 || runes(got.Doing) > 81 {
		t.Errorf("task kept a %d-character title and a %d-character step", runes(got.Title), runes(got.Doing))
	}
}
