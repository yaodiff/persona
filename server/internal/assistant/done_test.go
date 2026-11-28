package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

func TestTaskDoneSourceSurvivesTranscriptParsing(t *testing.T) {
	for _, wrapped := range []bool{true, false} {
		t.Run(map[bool]string{true: "rendered envelope", false: "context stripped"}[wrapped], func(t *testing.T) {
			s, f := newTaskService(t)
			todo := startedTask(t, s, f)
			ctx := context.Background()
			s.superviseTodo(ctx, todo.ID, "conv1", []string{"Login test passes"}, 0)
			s.taskWorking(ctx, "box", "s1", startRow{})
			text := prompts.RenderSideConversation("Did you handle a separate parser problem?")
			if !wrapped {
				text = "<persona-context>Source: another conversation.</persona-context>\n<persona-side-conversation />\nDid you handle a separate parser problem?"
			}
			line, _ := json.Marshal(map[string]any{"type": "user", "uuid": "synthetic-user", "message": map[string]any{"role": "user", "content": text}})
			items := transcript.ParseLine(1, line, &transcript.Meta{})
			if len(items) != 1 || !wrapped && strings.Contains(items[0].Text, "<persona-context") {
				t.Fatalf("unexpected parsed context: %+v", items)
			}
			items = append(items, proto.Item{Kind: proto.KindText, Role: "assistant", Text: "已告知另一会话：本会话未处理该问题"}) // i18n-ignore: synthetic regression answer
			f.setItems(items...)
			f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone})
			f.judge = `{"verdict":"done","reason":"The last reply answered the question"}`
			s.taskTurnEnded(ctx, "box", "s1")
			if todoNow(s, todo.ID).Supervision.State == reviewPending {
				t.Fatal("parsed side conversation incorrectly entered pending review")
			}
		})
	}
}

func TestTaskDonePreservesTurnSourceBeforeClipping(t *testing.T) {
	s, f := newTaskService(t)
	todo := startedTask(t, s, f)
	ask := strings.Repeat("a", 1200) + `<persona-context kind="side-conversation">Another task's question</persona-context>` + strings.Repeat("b", 1200)
	f.setItems(proto.Item{Kind: proto.KindText, Role: "user", From: "peer-agent", Text: ask},
		proto.Item{Kind: proto.KindText, Role: "assistant", Text: "Another session was informed; the task was not handled here."})
	replies, blocked, _ := s.taskReplies(context.Background(), todo)
	if !blocked || len(replies) != 1 || replies[0].From != "peer-agent" || replies[0].Source != "peer" {
		t.Fatalf("source lost: blocked=%v replies=%+v", blocked, replies)
	}
	if strings.Contains(replies[0].Ask, "side-conversation") {
		t.Fatal("fixture must clip the envelope away so metadata is essential")
	}
}

func TestTaskDoneSourceAllowsBackgroundWorkButKeepsQueuedPeer(t *testing.T) {
	for _, tc := range []struct {
		items       []proto.Item
		wantBlocked bool
	}{
		{items: []proto.Item{{Kind: proto.KindNotice, Role: "system", Wake: true}}},
		{items: []proto.Item{{Kind: proto.KindNotice, Role: "system", Task: &proto.TaskNote{}}}},
		{items: []proto.Item{{Kind: proto.KindNotice, Role: "user", Task: &proto.TaskNote{Kind: "shell"}}}},
		{items: []proto.Item{{Kind: proto.KindNotice, Role: "system", Injected: &proto.Injected{Kind: proto.InjectedSystem, State: "supervise"}}}, wantBlocked: true},
		{items: []proto.Item{{Kind: proto.KindText, Role: "user", Injected: &proto.Injected{Kind: proto.InjectedSystem, State: "inbox"}}}, wantBlocked: true},
		{items: []proto.Item{{Kind: proto.KindText, Role: "user", Injected: &proto.Injected{Kind: proto.InjectedFollowup, State: "wake"}}}},
		{items: []proto.Item{{Kind: proto.KindText, Role: "user", Text: prompts.RenderLimitFallbackContinue("English")}}},
		{items: []proto.Item{{Kind: proto.KindText, Role: "user", Injected: &proto.Injected{Kind: proto.InjectedFollowup, State: "limit_continue"}}, {Kind: proto.KindText, Role: "user", From: "peer-agent", Queued: true}}, wantBlocked: true},
		{items: []proto.Item{{Kind: proto.KindText, Role: "user", Text: "Continue the login fix"}, {Kind: proto.KindText, Role: "user", From: "peer-agent", Text: "Unrelated question", Queued: true}}, wantBlocked: true},
		{items: []proto.Item{{Kind: proto.KindNotice, Role: "system", Task: &proto.TaskNote{}}, {Kind: proto.KindText, Role: "user", From: "peer-agent", Text: "Unrelated question", Queued: true}}, wantBlocked: true},
	} {
		_, source := taskTurnSource(Todo{}, tc.items)
		if got := source != ""; got != tc.wantBlocked {
			t.Fatalf("blocked=%v, want %v: %+v", got, tc.wantBlocked, tc.items)
		}
	}
}

func TestTaskDonePromptsGolden(t *testing.T) {
	golden(t, "task_done", prompts.RenderTaskDone(prompts.TaskDone{Owner: "User"}))
	golden(t, "task_done.delivery", prompts.RenderTaskDone(prompts.TaskDone{Owner: "User", Delivery: true}))
	golden(t, "task_done.language", prompts.RenderTaskDone(prompts.TaskDone{Owner: "User", Language: "English"}))
	in := prompts.TaskDoneInput{Title: "Fix the login failure", Prompt: "Fix the login failure and test the correction.",
		Criteria: []string{"Login regression test passes"}, Replies: []prompts.TaskDoneReply{{Machine: "Computer", From: "peer-agent", Source: "peer",
			Ask: "Did this session fix the unrelated parser?", Text: "Another session was informed; this session did not handle that problem."}}}
	golden(t, "task_done.input", prompts.RenderTaskDoneInput(in))
}

func TestTaskDonePromptKeepsOriginalTaskWhenSourceWasLost(t *testing.T) {
	text := prompts.RenderTaskDoneInput(prompts.TaskDoneInput{Title: "Fix login", Prompt: "Fix and test login", Criteria: []string{"Login test passes"},
		Replies: []prompts.TaskDoneReply{{Machine: "Computer", Ask: "Answer an unrelated parser question", Text: "The question was answered."}}})
	for _, want := range []string{"Fix and test login", "Login test passes", "不替代原任务"} { // i18n-ignore: prompt contract
		if !strings.Contains(text, want) {
			t.Fatalf("input lacks %q: %s", want, text)
		}
	}
	if strings.Contains(text, "用户后来给它的要求") { // i18n-ignore: removed misleading prompt heading
		t.Fatal("an unknown source is still described as a later user requirement")
	}
	system := prompts.RenderTaskDone(prompts.TaskDone{Owner: "User"})
	if strings.Contains(system, "派活时的要求已经在之前几轮里处理过") || !strings.Contains(system, "不能推断原来的要求") { // i18n-ignore: prompt contract
		t.Fatal("new questions still implicitly complete the original task")
	}
}

func TestAutoDoneSkip(t *testing.T) {
	now := time.Now()
	reopened := now.Add(-time.Minute)
	for _, tc := range []struct {
		t      Todo
		work   time.Time
		worked bool
		want   string
	}{
		{Todo{}, now, true, "not_a_task"},
		{Todo{Origin: "c", DoneAt: &now}, now, true, "done"},
		{Todo{Origin: "c", ReopenedAt: &reopened}, now.Add(-2 * time.Minute), true, "reopened"},
		{Todo{Origin: "c", ReopenedAt: &reopened}, time.Time{}, false, "reopened"},
		{Todo{Origin: "c", ReopenedAt: &reopened}, now, true, ""},
		{Todo{Origin: "c"}, time.Time{}, false, ""},
	} {
		if got := autoDoneSkip(tc.t, tc.work, tc.worked); got != tc.want {
			t.Errorf("autoDoneSkip(%+v, worked %v) = %q, want %q", tc.t, tc.worked, got, tc.want)
		}
	}
}

func TestDoneSignals(t *testing.T) {
	for _, tc := range []struct {
		text      string
		pr, build bool
	}{
		{"PR https://github.com/o/r/pull/12 已合并", true, false},
		{"提了 https://github.com/o/r/pull/12，等 review", false, false},
		{"merged https://github.com/o/r/pull/12; TestFlight build 202611041200 is up", true, true},
		{"发了 TestFlight，构建号 1234", false, true},
		{"没有全改完", false, false},
	} {
		reply := doneReply(tc.text)
		reply.pr = "https://github.com/o/r/pull/12"
		got := doneSignals([]taskDoneReply{reply})
		if got.MergedPR != tc.pr || got.Build != tc.build {
			t.Errorf("doneSignals(%q) = %+v", tc.text, got)
		}
	}
	if s := clipMiddle(strings.Repeat("a", 10)+strings.Repeat("b", 10), 4); s != "aaaa\n…\nbbbb" {
		t.Fatalf("clipMiddle = %q", s)
	}
}

// A legacy task joins main supervision before its judgment is marked complete. If that ledger write fails, leaving the
// turn unjudged lets the same completed work retry and reach review after the store recovers instead of stranding it.
func TestLegacyDoneRetriesSupervisionAttachAfterStoreRecovery(t *testing.T) {
	st := &ledgerDown{DB: todoTestDB(t)}
	st.down.Store(true)
	s, _ := newQuietServiceOn(t, st)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Legacy delivery", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	work := time.Now().UTC()
	if _, again := s.takeTurn(ctx, todo, turnRow(proto.StatusDone, 1, "delivered"), nil,
		turnClose{Key: "legacy-turn"}, true, work, turnDone); again {
		t.Fatal("the terminated test turn was already taken")
	}
	check := taskDoneCheck{Verdict: verdictDone, Reason: "delivered"}
	s.applyTaskDone(ctx, todo, work, true, check, nil)
	if e, ok := s.turnOf(ctx, todo.ID); !ok || e.Judged != nil {
		t.Fatalf("the turn was marked judged without its supervision ledger: %+v, %v", e, ok)
	}
	if cur := todoNow(s, todo.ID); cur.DoneAt != nil || cur.Supervision != nil {
		t.Fatalf("the failed attach changed the legacy task: %+v", cur)
	}

	st.down.Store(false)
	s.applyTaskDone(ctx, todo, work, true, check, nil)
	cur := todoNow(s, todo.ID)
	if cur.DoneAt != nil || cur.Supervision == nil || cur.Supervision.Supervisor != "conv1" || cur.Supervision.State != reviewPending {
		t.Fatalf("the recovered attach did not reach main review: %+v", cur)
	}
	if e, ok := s.turnOf(ctx, todo.ID); !ok || e.Judged == nil {
		t.Fatalf("the successful retry did not record its judgment: %+v, %v", e, ok)
	}
}

func TestLegacyDoneWaitsForMainBeforeJudgment(t *testing.T) {
	s, _ := newQuietService(t)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Legacy delivery before main", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	work := time.Now().UTC()
	if _, again := s.takeTurn(ctx, todo, turnRow(proto.StatusDone, 1, "delivered"), nil,
		turnClose{Key: "legacy-turn-before-main"}, true, work, turnDone); again {
		t.Fatal("the terminated test turn was already taken")
	}
	setSettings(t, s, func(v *Settings) { v.Main = "" })
	check := taskDoneCheck{Verdict: verdictDone, Reason: "delivered"}
	s.applyTaskDone(ctx, todo, work, true, check, nil)
	if e, ok := s.turnOf(ctx, todo.ID); !ok || e.Judged != nil {
		t.Fatalf("the turn was marked judged without a main reviewer: %+v, %v", e, ok)
	}
	if cur := todoNow(s, todo.ID); cur.DoneAt != nil || cur.Supervision != nil {
		t.Fatalf("the missing main changed the legacy task: %+v", cur)
	}

	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	s.applyTaskDone(ctx, todo, work, true, check, nil)
	cur := todoNow(s, todo.ID)
	if cur.DoneAt != nil || cur.Supervision == nil || cur.Supervision.Supervisor != "conv1" || cur.Supervision.State != reviewPending {
		t.Fatalf("the configured main did not receive the legacy review: %+v", cur)
	}
	if e, ok := s.turnOf(ctx, todo.ID); !ok || e.Judged == nil {
		t.Fatalf("the main-supervised retry did not record its judgment: %+v, %v", e, ok)
	}
}

func TestSameRequest(t *testing.T) {
	if !sameRequest("<persona-context kind=\"context\">x</persona-context>\niPad 横屏时登录页崩溃，修好并补测试", "iPad 横屏时登录页崩溃，修好并补测试") ||
		sameRequest("再把「全部 N」挪到最左边", "iPad 横屏时登录页崩溃，修好并补测试") {
		t.Fatal("sameRequest")
	}
}
