package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// judgedTask is a started task whose agent (s1 on box) worked and stopped saying text, the judge answering judge: the
// todo and the start of the work it judges.
func judgedTask(t *testing.T, s *Service, f *fakeHub, text, judge string) (Todo, time.Time) {
	t.Helper()
	todo := startedTask(t, s, f)
	s.taskWorking(context.Background(), "box", "s1", startRow{})
	s.tasks.mu.Lock()
	work := s.tasks.working[todo.ID]
	s.tasks.mu.Unlock()
	f.judge = judge
	if text == "" {
		f.setItems()
	} else {
		f.setItems(proto.Item{Kind: proto.KindText, Role: "assistant", Text: text})
	}
	f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s1", Status: proto.StatusDone})
	return todoNow(s, todo.ID), work
}

// keptCheck is task id's check as the stages setting keeps it (what a restarted hub reads back).
func keptCheck(s *Service, id string) *TodoCheck {
	return s.todoStages(context.Background())[id].Check
}

// Every automatic judgment is kept on the todo as its check: the verdict, its reason (the hub's own in the user's
// language), when and the work it judged. Done is delivery for review, never closure (#5732).
func TestAutoFinishKeepsTheCheck(t *testing.T) {
	for _, tc := range []struct {
		name, locale, text, judge string
		delivery                  bool
		verdict, reason           string
		closed                    bool
	}{
		{name: "done", text: "PR 已合并", judge: `{"verdict":"done","reason":"PR 已合并"}`, verdict: verdictDone, reason: "PR 已合并"},
		{name: "done without a reason", locale: "en", text: "Merged", judge: `{"verdict":"done","reason":""}`, verdict: verdictDone,
			reason: "Done"},
		{name: "not done", text: "还差两处", judge: `{"verdict":"not_done","reason":"还差两处没改"}`, verdict: verdictNotDone, reason: "还差两处没改"},
		{name: "waiting", locale: "en", text: "Waiting for CI", judge: `{"verdict":"waiting","reason":"Waiting for CI"}`, verdict: verdictWaiting,
			reason: "Waiting for CI"},
		{name: "needs the user", text: "要你选", judge: `{"verdict":"needs_user","reason":"等你选方案"}`, verdict: verdictNeedsUser, reason: "等你选方案"},
		{name: "a made-up verdict", text: "嗯", judge: `{"verdict":"maybe","reason":"看不出"}`, verdict: verdictUnsure, reason: "看不出"},
		{name: "undelivered", text: "开了 PR https://github.com/o/r/pull/7", delivery: true,
			judge:   `{"verdict":"done","reason":"开了 PR","delivery":"pr","gate":"等合并","unblock":"user"}`,
			verdict: verdictUndelivered, reason: "开了 PR"},
		{name: "no reply", locale: "ja", verdict: verdictUnsure, reason: "エージェントの返信を読めませんでした"},
		{name: "cut off", locale: "en", text: "API Error: 529 overloaded", verdict: verdictNotDone, reason: "Interrupted by a model service error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newTaskService(t)
			f.hello.Locale = tc.locale
			if tc.delivery {
				deliveryOn(t, s)
			}
			todo, work := judgedTask(t, s, f, tc.text, tc.judge)
			s.autoFinishTask(todo, work, true)
			got := todoNow(s, todo.ID)
			c := got.Check
			if c == nil || c.Verdict != tc.verdict || c.Reason != tc.reason || !c.WorkSince.Equal(work) || c.At.Before(work) {
				t.Fatalf("check = %+v, want %s %q since %v", c, tc.verdict, tc.reason, work)
			}
			if kept := keptCheck(s, todo.ID); kept == nil || *kept != *c {
				t.Fatalf("kept check = %+v, on the todo %+v", kept, c)
			}
			if closed := got.DoneAt != nil; closed != tc.closed || tc.closed && got.DoneReason != tc.reason {
				t.Fatalf("closed = %v (%q), want %v", closed, got.DoneReason, tc.closed)
			}
		})
	}
}

// No model to ask (or a model error): no verdict, nothing kept.
func TestAutoFinishWithoutAVerdictKeepsNoCheck(t *testing.T) {
	s, f := newTaskService(t)
	todo, work := judgedTask(t, s, f, "做好了", "")
	s.autoFinishTask(todo, work, true)
	if got := todoNow(s, todo.ID); got.Check != nil || got.DoneAt != nil {
		t.Fatalf("todo = %+v %+v", got, got.Check)
	}
}

// A judgment overtaken by a new turn while the model was asked keeps nothing.
func TestAutoFinishKeepsNoStaleCheck(t *testing.T) {
	s, f := newTaskService(t)
	todo, work := judgedTask(t, s, f, "还没做完", `{"verdict":"not_done","reason":"还差测试"}`)
	turned := false
	f.hookReads(receiptHooks{before: func(int) {
		if !turned {
			turned = true
			s.taskWorking(context.Background(), "box", "s1", startRow{})
		}
	}})
	s.autoFinishTask(todo, work, true)
	if !turned {
		t.Fatal("the new turn never started")
	}
	if got := todoNow(s, todo.ID); got.Check != nil || keptCheck(s, todo.ID) != nil {
		t.Fatalf("stale check kept: %+v", got.Check)
	}
}

// A new turn of one of its agents, or a reopen, takes the check off: it judged the work before.
func TestTaskCheckClearedByANewTurnAndAReopen(t *testing.T) {
	ctx := context.Background()
	s, f := newTaskService(t)
	todo, work := judgedTask(t, s, f, "还没做完", `{"verdict":"waiting","reason":"等 CI"}`)
	s.autoFinishTask(todo, work, true)
	if todoNow(s, todo.ID).Check == nil {
		t.Fatal("no check")
	}
	s.taskWorking(ctx, "box", "s1", startRow{})
	if got := todoNow(s, todo.ID); got.Check != nil || keptCheck(s, todo.ID) != nil || got.Stage != "develop" {
		t.Fatalf("after a new turn: %+v %+v", got.Check, keptCheck(s, todo.ID))
	}

	s2, f2 := newTaskService(t)
	todo, work = judgedTask(t, s2, f2, "PR 已合并", `{"verdict":"done","reason":"PR 已合并"}`)
	s2.autoFinishTask(todo, work, true)
	if got := todoNow(s2, todo.ID); got.DoneAt != nil || got.Check == nil || got.Check.Verdict != verdictDone {
		t.Fatalf("delivered: %+v %+v", got, got.Check)
	}
	if w := serve(s2, "PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`); w.Code != 200 {
		t.Fatalf("close before reopen: %d %s", w.Code, w.Body)
	}
	if got := todoNow(s2, todo.ID); got.DoneAt == nil {
		t.Fatal("user completion did not close the task")
	}
	if err := s2.reopenTodo(ctx, todo.ID); err != nil {
		t.Fatal(err)
	}
	if got := todoNow(s2, todo.ID); got.DoneAt != nil || got.Check != nil || keptCheck(s2, todo.ID) != nil {
		t.Fatalf("after a reopen: %+v %+v", got, got.Check)
	}
}

// A supervised task gets its check too, before its supervisor hears of it; done still waits for review.
func TestSupervisedTaskKeepsTheCheck(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	f.judge = `{"verdict":"waiting","reason":"等 App 审核"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "已提交审核", 0)
	waitFor(t, "the check", func() bool { return todoNow(s, todo.ID).Check != nil })
	if c := todoNow(s, todo.ID).Check; c.Verdict != verdictWaiting || c.Reason != "等 App 审核" {
		t.Fatalf("check = %+v", c)
	}
	waitFor(t, "the supervisor's message", func() bool { return len(f.notices()) == 1 })
	if n := f.notices()[0]; !strings.Contains(n, "在等别人或别的事：等 App 审核") {
		t.Fatalf("message:\n%s", n)
	}

	f.judge = `{"verdict":"done","reason":"PR 已合并"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	if c := todoNow(s, todo.ID).Check; c != nil {
		t.Fatalf("a new turn kept the check: %+v", c)
	}
	agentTurn(s, f, proto.StatusDone, "PR 已合并", 0)
	waitFor(t, "the review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	if got := todoNow(s, todo.ID); got.DoneAt != nil || got.Check == nil || got.Check.Verdict != verdictDone {
		t.Fatalf("todo = %+v %+v", got, got.Check)
	}
}

// The judge is asked for the reason in the user's language (nothing added for Chinese, the prompt's own).
func TestTaskDoneJudgeAsksInTheUsersLanguage(t *testing.T) {
	for _, tc := range []struct{ locale, want string }{{"en", "用English写"}, {"ja", "用" + i18n.Lang("ja").Name() + "写"}, {"zh-Hans", ""}} {
		m := &cannedJSON{answer: `{"verdict":"waiting","reason":"Waiting for CI"}`}
		in := taskDoneInput{Owner: "User", Language: answerLanguage(tc.locale), Title: "Fix login", Replies: []taskDoneReply{doneReply("PR open")}}
		if _, err := judgeTaskDoneWith(context.Background(), m, in); err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(m.system, "写，说结果"); got != (tc.want != "") || tc.want != "" && !strings.Contains(m.system, tc.want) {
			t.Fatalf("%s: system prompt:\n%s", tc.locale, m.system)
		}
	}
}
