package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/proto"
)

// goalFeatures are the bot's switches as a new install has them: reports on, the execution steps off (#3218).
var goalFeatures = FeishuFeatures{Main: true, Cards: true, Reports: true}

// waitReportQueue waits for the scheduled report send to finish, including batches that produce no card.
func waitReportQueue(t *testing.T, s *Service) {
	t.Helper()
	waitFor(t, "the report queue", func() bool {
		s.reports.sending.Lock()
		defer s.reports.sending.Unlock()
		s.reports.mu.Lock()
		defer s.reports.mu.Unlock()
		return s.reports.timer == nil && len(s.reports.pending) == 0
	})
}

// By default a task's turns are not pushed: a finished turn (whatever the agent said, a merged PR, "收工"), an
// interruption and a closing by the judge are execution steps.
func TestGoalReportsSkipExecutionSteps(t *testing.T) {
	s, f, posts := reportServiceWith(t, goalFeatures, repositorystore.New(todoTestDB(t)))
	todo := startedTask(t, s, f)
	artifactNodeProof(f, "https://github.com/o/r/pull/901", "contributor", "contributor",
		"fix/login", "fix/login", "abc", "abc")
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "好的，收工。https://github.com/o/r/pull/901 已合并，临时 worktree 清掉了", 0)
	waitReportQueue(t, s)
	s.taskReport(todo, reportInterrupted, "box", [2]string{"box", "s1"}, "模型服务断开", nil)
	waitReportQueue(t, s)
	s.taskClosedReport(todo, "做完了")
	waitReportQueue(t, s)
	if got := posts(); len(got) != 0 {
		t.Fatalf("execution steps pushed:\n%s", strings.Join(got, "\n---\n"))
	}
	if got := onlyTodo(s); got.Stage != "merge" {
		t.Fatalf("the stage is still kept for the apps: %q", got.Stage)
	}
}

// A decision is pushed as its own answerable card, with no task report line around it.
func TestGoalReportsDecisionIsItsOwnCard(t *testing.T) {
	needsLarkLayer(t)
	s, f, posts := reportServiceWith(t, goalFeatures)
	todo := startedTask(t, s, f)
	agentTurn(s, f, proto.StatusBlocked, "", 1)
	ix := proto.Interaction{ID: "ix-g1", MachineID: "box", SessionID: "s1", Kind: proto.IxQuestion,
		Questions: []proto.Question{{Question: "用哪个分支？", Options: []proto.QuestionOption{{Label: "main"}, {Label: "dev"}}}}}
	f.answerApp(AppInteractions, InteractionsGet, func(json.RawMessage) (any, error) { return map[string]any{"ok": true, "interaction": ix}, nil })
	s.taskReport(todo, reportNeeds, "box", [2]string{"box", "s1"}, noticeAsk(ix), &ix)
	waitFor(t, "the question's card", func() bool { return len(posts()) == 1 })
	waitReportQueue(t, s)
	got := posts()
	if len(got) != 1 || !strings.Contains(got[0], `"a":"ix"`) || strings.Contains(got[0], `"content":"汇报"}`) {
		t.Fatalf("posts:\n%s", strings.Join(got, "\n---\n"))
	}
}

// A task that did not start, or whose turns were cut off reportStuckAfter times in a row, is stuck: one card saying the
// goal, where it is and the evidence with a link — no stage bar, no agent's words. It is told once; a finished turn
// starts the count again.
func TestGoalReportsStuckSaysGoalNowEvidence(t *testing.T) {
	needsLarkLayer(t)
	s, f, posts := reportServiceWith(t, goalFeatures)
	todo := startedTask(t, s, f)
	s.taskReport(todo, reportFailed, "box", [2]string{}, "那台电脑离线", nil)
	waitFor(t, "the stuck card", func() bool { return len(posts()) == 1 })
	card := posts()[0]
	for _, want := range []string{"[Persona] 修登录崩溃 · 1 件事的目标卡住了", "**目标**：修登录崩溃", "**现在**：卡住了，没开起来",
		"**证据**：那台电脑离线 [去看看](https://hub.example.com/todos/" + todo.ID + ")"} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
	for _, not := range []string{`"content":"汇报"}`, "✅开发", "⚪部署", "这一轮做完"} {
		if strings.Contains(card, not) {
			t.Errorf("card has %q:\n%s", not, card)
		}
	}

	for i := 0; i < reportStuckAfter-1; i++ {
		s.taskReport(todo, reportInterrupted, "box", [2]string{"box", "s1"}, "模型服务断开", nil)
	}
	waitReportQueue(t, s)
	if n := len(posts()); n != 1 {
		t.Fatalf("%d posts before the %dth cut", n, reportStuckAfter)
	}
	s.taskReport(todo, reportInterrupted, "box", [2]string{"box", "s1"}, "模型服务断开", nil)
	waitFor(t, "the stuck card of the cuts", func() bool { return len(posts()) == 2 })
	card = posts()[1]
	for _, want := range []string{"连续 3 次被打断", "模型服务断开", "https://hub.example.com/s/box/s1"} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
	s.taskReport(todo, reportInterrupted, "box", [2]string{"box", "s1"}, "模型服务断开", nil) // the 4th: told already
	waitReportQueue(t, s)
	s.taskReport(todo, reportDone, "box", [2]string{"box", "s1"}, "好了", nil) // resets the count
	for i := 0; i < reportStuckAfter-1; i++ {
		s.taskReport(todo, reportInterrupted, "box", [2]string{"box", "s1"}, "模型服务断开", nil)
	}
	waitReportQueue(t, s)
	if n := len(posts()); n != 2 {
		t.Fatalf("%d posts, want 2:\n%s", n, strings.Join(posts(), "\n---\n"))
	}
}

// report_steps brings the card back as it was: a finished turn with the stage bar and what the agent said.
func TestReportStepsSwitchBringsBackTheOldCard(t *testing.T) {
	needsLarkLayer(t)
	feats := goalFeatures
	feats.ReportSteps = true
	s, f, posts := reportServiceWith(t, feats)
	startedTask(t, s, f)
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "好的，收工。临时 worktree 清掉了", 0)
	waitFor(t, "the report", func() bool { return len(posts()) == 1 })
	if card := posts()[0]; !strings.Contains(card, `"content":"汇报"}`) || !strings.Contains(card, "这一轮做完</text_tag>") || !strings.Contains(card, "收工") {
		t.Fatalf("card:\n%s", card)
	}
}

// The conclusion card says the goal, where it is now — with how far the work went out — and the evidence's link.
func TestConclusionCardSaysGoalNowEvidence(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, posts, _ := conclusionService(t, true)
	waitsForReview(t, s, f, todo.ID, "查完了，见 https://docs.example.com/plan")
	s.setTaskStage(context.Background(), todo.ID, "verify", "")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "reason": "登录不再崩溃", "link": "https://hub.example.com/s/box/s1",
		"checks": completeReviewChecks(todoNow(s, todo.ID))})
	waitFor(t, "the conclusion card", func() bool { return len(cardsWith(posts(), "结论：")) == 1 })
	card := cardsWith(posts(), "结论：")[0]
	for _, want := range []string{"**目标**：修登录崩溃", "**现在**：✅ 做完了：登录不再崩溃 · 已上线并实测", "**证据**：[https://hub.example.com/s/box/s1]"} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
}

// Cuts that come quickly — the one making the task stuck replaced by a later one before the card went out — still tell it.
func TestGoalReportsStuckSurvivesQuickCuts(t *testing.T) {
	needsLarkLayer(t)
	s, f, posts := reportServiceWith(t, goalFeatures)
	todo := startedTask(t, s, f)
	for i := 0; i < reportStuckAfter+2; i++ {
		s.taskReport(todo, reportInterrupted, "box", [2]string{"box", "s1"}, "模型服务断开", nil)
	}
	waitFor(t, "the stuck card", func() bool { return len(posts()) == 1 })
	if card := posts()[0]; !strings.Contains(card, "连续 5 次被打断") {
		t.Fatalf("card:\n%s", card)
	}
}
