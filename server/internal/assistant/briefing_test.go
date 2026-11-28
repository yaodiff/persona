package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/kernel"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/modapi"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	briefDayRunes   = 4000
	briefDayReasons = 6
	briefDayRanges  = 5
	briefDayParts   = 5
	briefDaySummary = 300
	briefDayTitle   = 60
	briefDayWhat    = 100
)

const feedBrief = modapi.FeedKindBrief

type (
	briefProgress       = modapi.BriefTaskProgress
	briefBody           = modapi.BriefBody
	briefBodyRow        = modapi.BriefBodyRow
	briefBodyPRFollowup = modapi.BriefPRFollowup
)

func (s *Service) briefFactsAt(ctx context.Context, now time.Time, self string) *briefFacts {
	r, ok := s.briefingRunner()
	if !ok {
		return nil
	}
	return r.FactsAt(ctx, now, self)
}

func (s *Service) briefModelText(f *briefFacts) string {
	r, ok := s.briefingRunner()
	if !ok {
		return ""
	}
	return r.ModelText(f)
}

func (s *Service) briefBodyOf(f *briefFacts, title string) briefBody {
	r, ok := s.briefingRunner()
	if !ok {
		return briefBody{}
	}
	return r.Body(f, title)
}

func (s *Service) briefSetSessionRun(session string, run *briefRun) {
	if r, ok := s.briefingRunner(); ok {
		r.SetSessionRun(session, run)
	}
}

func (s *Service) briefSetTaskRun(taskID string, run *briefRun) {
	if r, ok := s.briefingRunner(); ok {
		r.SetTaskRun(taskID, run)
	}
}

func (s *Service) briefTaskRunCount() int {
	r, ok := s.briefingRunner()
	if !ok {
		return 0
	}
	return r.TaskRunCount()
}

func briefRunnerForTest(t *testing.T) modapi.ScheduleFactsRunner {
	t.Helper()
	needsBriefingModule(t)
	k := kernel.NewWithPorts(kernel.Builtin(), nil, modapi.Ports{Prompts: briefPromptPort{}}, t.Logf)
	f, ok := k.ScheduleFacts()
	if !ok || f.Runner == nil {
		t.Fatal("briefing module runner not registered")
	}
	return f.Runner
}

func briefFollowing(t *testing.T, items []InboxItem, now time.Time, loc *time.Location) (waiting, due []prompts.BriefFollow) {
	t.Helper()
	return briefRunnerForTest(t).Following(toBriefInboxSignals(items), now, loc)
}

func briefDayBound(t *testing.T, d *prompts.BriefDay) {
	t.Helper()
	briefRunnerForTest(t).BoundDay(d)
}

func briefFallbackID(run ScheduleRun) string {
	sum := sha256.Sum256([]byte(run.RefID + "\x00" + run.Due.UTC().Format(time.RFC3339Nano)))
	return hex.EncodeToString(sum[:16])
}

// The fixed part of the morning and evening routines (briefing.go): the facts are worked out once when the run starts,
// from the same data as the standing board and the "Today" card. All ids are placeholders; the clock is fake.

// briefEnv is a bot and an assistant as the board's tests have them, with helpers for what the briefing lists.
type briefEnv struct{ *boardEnv }

func newBriefEnv(t *testing.T) *briefEnv {
	t.Helper()
	needsBriefingModule(t)
	e := &briefEnv{newBoardEnv(t)}
	e.s.accept.loaded, e.s.accept.asks = true, map[string]*acceptAsk{}
	return e
}

// verify is a task waiting for the owner to verify it, asked age ago.
func (e *briefEnv) verify(id, title string, age time.Duration) {
	e.s.todos.todos[id] = &Todo{ID: id, Title: title, Origin: "conv1"}
	e.s.accept.asks[id] = &acceptAsk{MessageID: "om_" + id, Messages: []string{"om_" + id}, AskedAt: e.clk.Now().Add(-age),
		MachineID: "box", SessionID: "ag1", Changed: title + " changed", Link: testWeb + "/todos?id=" + id}
}

// closed is a task closed age ago (startError: it never started).
func (e *briefEnv) closed(id, title string, age time.Duration, startError string) {
	at := e.clk.Now().Add(-age)
	e.s.todos.todos[id] = &Todo{ID: id, Title: title, Origin: "conv1", DoneAt: &at, StartError: startError}
}

// nudge marks the bot as having nudged under the card of ix.
func (e *briefEnv) nudge(ix proto.Interaction) {
	keepChatCard(e.s, ix.ID, &botCard{MessageID: "om_" + ix.ID, Ix: ix, At: ix.CreatedAt, NudgedAt: e.clk.Now()})
}

// The two sections of what waits are the standing board's rows, in the board's order: the cards that hold an Agent up
// first, the tasks to verify after them; a card the bot nudged under is marked as on the board.
func TestBriefSectionsAreTheBoardsRows(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	e := newBriefEnv(t)
	old := e.card("ix_old", "box", "ag1", "旧的问题", 90*time.Minute)
	e.card("ix_new", "server", "asm", "新的问题", 5*time.Minute)
	e.nudge(old)
	e.verify("todo_v1", "修登录崩溃", 30*time.Hour)
	e.verify("todo_v2", "改设置页", 2*time.Hour)

	now := e.clk.Now()
	f := e.s.briefFactsAt(context.Background(), now, "")
	board := e.s.boardRows(context.Background(), e.s.botLang(context.Background()), testWeb, now)
	if len(board) != 4 {
		t.Fatalf("the board has %d rows, want 4", len(board))
	}
	if got := append(append([]modapi.BriefBoardRow{}, f.Waiting...), f.Accept...); !reflect.DeepEqual(got, toBriefBoardRows(board)) {
		t.Fatalf("the briefing's rows are not the board's:\n got %+v\nwant %+v", got, toBriefBoardRows(board))
	}
	if len(f.Waiting) != 2 || f.Waiting[0].ID != "ix_old" || !f.Waiting[0].Nudged || f.Waiting[1].Nudged {
		t.Fatalf("waiting: %+v (only the old card was nudged)", f.Waiting)
	}
	if len(f.Accept) != 2 || f.Accept[0].ID != "todo_v1" || f.Accept[0].Asked.IsZero() {
		t.Fatalf("to verify: %+v (the longest waiting first, each with when it was asked)", f.Accept)
	}
}

// "Overnight" is the twelve hours before the run starts: a task closed 11 hours 59 minutes before is in it, one closed 12 hours
// 1 minute before is not (the same for a task that did not start and for a scheduled run that failed).
func TestBriefWindowIsTwelveHours(t *testing.T) {
	e := newBriefEnv(t)
	e.closed("todo_in", "刚好在窗口里", 12*time.Hour-time.Minute, "")
	e.closed("todo_out", "刚好出窗口", 12*time.Hour+time.Minute, "")
	e.closed("todo_fail_in", "没开起来在窗口里", 12*time.Hour-time.Minute, "machine offline")
	e.closed("todo_fail_out", "没开起来出窗口", 12*time.Hour+time.Minute, "machine offline")
	sc := e.s.loadSchedule(context.Background())
	sc.mu.Lock()
	sc.runs = []ScheduleRun{
		{ID: "run_in", RefID: "r_in", Title: "窗口里的巡检", At: e.clk.Now().Add(-12*time.Hour + time.Minute), Status: runFailed, Error: "503"},
		{ID: "run_out", RefID: "r_out", Title: "窗口外的巡检", At: e.clk.Now().Add(-12*time.Hour - time.Minute), Status: runFailed, Error: "503"},
	}
	sc.mu.Unlock()

	f := e.s.briefFactsAt(context.Background(), e.clk.Now(), "")
	if len(f.Done) != 1 || f.Done[0].ID != "todo_in" {
		t.Errorf("done: %+v, want only the task closed 11 h 59 min before", f.Done)
	}
	got := map[string]bool{}
	for _, x := range f.Failed {
		got[x.ID] = true
	}
	if len(f.Failed) != 2 || !got["todo_fail_in"] || !got["r_in"] {
		t.Errorf("failed: %+v, want the task and the run inside the window only", f.Failed)
	}
}

// What the window holds is sorted into the right section: a closed task is done, one whose agent never started and a failed
// run are failed, a skipped run is failed with its mark; a run that went fine, a task still open and a task waiting to be
// verified (it is listed to verify, not twice) are not in the finished list.
func TestBriefSortsDoneFailedAndSkipped(t *testing.T) {
	e := newBriefEnv(t)
	e.closed("todo_done", "做完的", 3*time.Hour, "")
	e.closed("todo_nostart", "没开起来的", 2*time.Hour, "machine offline")
	e.verify("todo_verify", "等验收的", time.Hour)
	at := e.clk.Now().Add(-time.Hour)
	e.s.todos.todos["todo_verify"].DoneAt, e.s.todos.todos["todo_verify"].DoneBy = &at, db.DoneByAuto // closed by the judge, the owner still verifies
	e.s.todos.todos["todo_open"] = &Todo{ID: "todo_open", Title: "还在做的", Origin: "conv1"}
	sc := e.s.loadSchedule(context.Background())
	sc.mu.Lock()
	sc.runs = []ScheduleRun{
		{ID: "run_ok", RefID: "r_ok", Title: "好的", At: e.clk.Now().Add(-4 * time.Hour), Status: runOK},
		{ID: "run_failed", RefID: "r_failed", Title: "坏的", At: e.clk.Now().Add(-5 * time.Hour), Status: runFailed, Error: "503"},
		{ID: "run_skipped", RefID: "r_skipped", Title: "错过的", At: e.clk.Now().Add(-6 * time.Hour), Status: runSkipped, Error: "Persona was down"},
	}
	sc.mu.Unlock()

	f := e.s.briefFactsAt(context.Background(), e.clk.Now(), "")
	if len(f.Done) != 1 || f.Done[0].ID != "todo_done" || f.DoneTotal != 1 {
		t.Errorf("done: %+v (%d)", f.Done, f.DoneTotal)
	}
	want := map[string]modapi.BriefFailure{}
	for _, x := range f.Failed {
		want[x.ID] = x
	}
	if len(f.Failed) != 3 || f.FailedTotal != 3 || want["todo_nostart"].Kind != "task" || want["r_failed"].Skipped || !want["r_skipped"].Skipped {
		t.Errorf("failed: %+v", f.Failed)
	}
	if len(f.Accept) != 1 || f.Accept[0].ID != "todo_verify" {
		t.Errorf("to verify: %+v", f.Accept)
	}
}

// The routine that is running is not told its own earlier failed attempt: this run is its retry, and the failure was
// pushed when it happened. Another routine's failure in the window is still told.
func TestBriefLeavesOutTheRoutinesOwnFailedAttempt(t *testing.T) {
	e := newBriefEnv(t)
	sc := e.s.loadSchedule(context.Background())
	sc.mu.Lock()
	sc.runs = []ScheduleRun{
		{ID: "run_own", RefID: "morning", Title: "早报", At: e.clk.Now().Add(-4 * time.Minute), Status: runFailed, Error: "503"},
		{ID: "run_other", RefID: "patrol", Title: "巡检", At: e.clk.Now().Add(-time.Hour), Status: runFailed, Error: "503"},
	}
	sc.mu.Unlock()

	f := e.s.briefFactsAt(context.Background(), e.clk.Now(), "morning")
	if len(f.Failed) != 1 || f.Failed[0].ID != "patrol" || f.FailedTotal != 1 {
		t.Fatalf("failed: %+v (%d), want only the other routine's", f.Failed, f.FailedTotal)
	}
	if f := e.s.briefFactsAt(context.Background(), e.clk.Now(), "evening"); f.FailedTotal != 2 {
		t.Fatalf("another routine's run is told both: %d", f.FailedTotal)
	}
}

// Right after a restart the cards waiting for an answer are not read yet: the briefing does not say that nothing waits,
// it says they were not read. What does not depend on them (the tasks to verify) is still told.
func TestBriefDoesNotSayNothingWaitsBeforeTheCardsAreRead(t *testing.T) {
	e := newBriefEnv(t)
	e.card("ix_old", "box", "ag1", "旧的问题", 90*time.Minute)
	e.verify("todo_v1", "修登录崩溃", 30*time.Hour)
	e.s.fleetUp.Store(false)

	f := e.s.briefFactsAt(context.Background(), e.clk.Now(), "")
	if !f.WaitingUnknown || len(f.Waiting) != 0 || len(f.Accept) != 1 {
		t.Fatalf("waiting unknown %v: %+v / %+v", f.WaitingUnknown, f.Waiting, f.Accept)
	}
	card, _, _ := e.s.briefCard(f, "早报", "总述")
	js := card.JSON()
	if !strings.Contains(js, "还没读到等你处理的卡片") || strings.Contains(js, "等你处理（") {
		t.Errorf("the card:\n%s", js)
	}
	counts := briefRunnerForTest(t).CountsIn(f, string(e.s.botLang(context.Background())))
	if strings.Contains(counts, "等你") || !strings.Contains(counts, "待验收 1") {
		t.Errorf("counts: %q", counts)
	}
	if text := e.s.briefModelText(f); !strings.Contains(text, "还没读到，不要说「没有」") {
		t.Errorf("the model is told: %s", text)
	}
}
