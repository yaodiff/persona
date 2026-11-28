package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// deployedForReview has supervised todo's agent say its work is deployed until its "Please verify" card is out and the
// task waits for review. The card goes to the owner as a big feature's does (ownerVerifiesAll), or as one from before
// the assistant accepted deployed work itself (#3205).
func deployedForReview(t *testing.T, s *Service, f *fakeHub, todo string) {
	t.Helper()
	s.ownerVerifiesAll = true
	f.judge = `{"changed":"飞书机器人多了任务进展开关","link":"https://hub.example.com/settings?tab=feishu","how":"打开设置看开关"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	turnWithRecordedDeployment(t, s, f)
	waitFor(t, "the 请验收 card", func() bool { a := askOf(s, todo); return a != nil && a.MessageID != "" })
	f.judge = `{"verdict":"done","reason":"已部署"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	turnWithRecordedDeployment(t, s, f)
	waitFor(t, "the review", func() bool { return todoNow(s, todo).Supervision.State == reviewPending })
}

// updatedCards keeps the cards the bot redraws.
func updatedCards(f *fakeHub) func() []string {
	var mu sync.Mutex
	var cards []string
	f.answerApp(AppFeishu, FeishuUpdateCard, func(p json.RawMessage) (any, error) {
		var m struct {
			Card string `json:"card"`
		}
		_ = json.Unmarshal(p, &m)
		mu.Lock()
		cards = append(cards, m.Card)
		mu.Unlock()
		return map[string]any{}, nil
	})
	return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), cards...) }
}

func waitingForOwner(s *Service) int {
	ctx := context.Background()
	_, total := s.snapshotUnaccepted(ctx, s.location(ctx))
	return total
}

// The supervisor's accepted, having checked the evidence, passes the deployed task's "Please verify": the task is
// verified, the card says the supervisor passed it, and nothing waits for the owner's "Pass" (#3240).
func TestSupervisorAcceptedPassesPleaseVerify(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, posts, _ := conclusionService(t, false)
	updated := updatedCards(f)
	deployedForReview(t, s, f, todo.ID)
	res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "note": "上线了",
		"checks": []ReviewCheck{
			{Criterion: "PR 合并到 master", Met: true, Evidence: "https://github.com/o/r/pull/9 merged"},
			{Criterion: "浅色 / 深色截图贴进 PR", Met: true, Evidence: "both screenshots verified"},
		}})
	if !strings.Contains(res, "marked verified by you") {
		t.Fatalf("the review's answer: %s", res)
	}
	a := askOf(s, todo.ID)
	if a == nil || a.AcceptedAt.IsZero() || !a.BySupervisor {
		t.Fatalf("ask %+v", a)
	}
	if n := waitingForOwner(s); n != 0 {
		t.Fatalf("%d waiting for the owner", n)
	}
	if got := todoNow(s, todo.ID); got.Stage != "verify" {
		t.Fatalf("stage %q", got.Stage)
	}
	if n := countAcceptCards(posts()); n != 1 {
		t.Fatalf("%d 请验收 cards", n)
	}
	if n := len(cardsWith(posts(), "结论：")); n != 0 {
		t.Fatalf("%d conclusion cards", n)
	}
	if c := cardsWith(updated(), "督办已核对证据，标为已验证"); len(c) != 1 || strings.Contains(c[0], `"a":"accept"`) {
		t.Fatalf("the card is not redrawn as passed by the supervisor: %q", updated())
	}
	// Reminded the next morning: nothing, it is passed.
	s.accept.mu.Lock()
	s.accept.asks[todo.ID].AskedAt = time.Now().Add(-30 * time.Hour)
	s.accept.mu.Unlock()
	waitReportQueue(t, s) // report cards go out on their own timer: count only after them (#14138)
	n := len(posts())
	ctx := context.Background()
	now := time.Now().In(s.location(ctx))
	s.remindAcceptance(time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, s.location(ctx)))
	if got := posts()[n:]; len(got) != 0 {
		t.Fatalf("reminded: %q", got)
	}
}

// An ask left waiting by a hub from before #3240 (the supervisor accepted, the owner was still asked) is passed by the
// sweep; so is one whose card went out after the review.
func TestSupervisedBacklogIsPassedBySweep(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, _, _ := conclusionService(t, false)
	deployedForReview(t, s, f, todo.ID)
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "note": "上线了", "checks": completeReviewChecks(todoNow(s, todo.ID))})
	s.accept.mu.Lock()
	s.accept.asks[todo.ID].AcceptedAt, s.accept.asks[todo.ID].BySupervisor = time.Time{}, false // as the old hub left it
	s.accept.mu.Unlock()
	if n := waitingForOwner(s); n != 1 {
		t.Fatalf("%d waiting before the sweep", n)
	}
	s.verifySupervisedAsks(context.Background())
	if a := askOf(s, todo.ID); a == nil || a.AcceptedAt.IsZero() || !a.BySupervisor {
		t.Fatalf("ask %+v", a)
	}
	if n := waitingForOwner(s); n != 0 {
		t.Fatalf("%d waiting after the sweep", n)
	}
}

// user_check: the supervisor accepted, but the owner is to look themselves: the "Please verify" keeps waiting for them;
// with none up yet, one is asked.
func TestSupervisorUserCheckStillAsksTheOwner(t *testing.T) {
	needsLarkLayer(t)
	t.Run("deployed", func(t *testing.T) {
		s, f, todo, posts, _ := conclusionService(t, false)
		deployedForReview(t, s, f, todo.ID)
		res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "note": "界面改好了", "user_check": true, "checks": completeReviewChecks(todoNow(s, todo.ID))})
		if !strings.Contains(res, "user_check") {
			t.Fatalf("the review's answer: %s", res)
		}
		s.verifySupervisedAsks(context.Background())
		if a := askOf(s, todo.ID); a == nil || !a.AcceptedAt.IsZero() || a.BySupervisor {
			t.Fatalf("ask %+v", a)
		}
		if n := waitingForOwner(s); n != 1 {
			t.Fatalf("%d waiting for the owner", n)
		}
		if n := countAcceptCards(posts()); n != 1 {
			t.Fatalf("%d 请验收 cards", n)
		}
		mid := askOf(s, todo.ID).MessageID
		if !s.acceptReply(context.Background(), "om_ok", "通过", false, mid) || askOf(s, todo.ID).AcceptedAt.IsZero() {
			t.Fatal("the owner's Pass did not pass it")
		}
	})
	t.Run("deployed, passed by the assistant", func(t *testing.T) {
		s, f, todo, posts, _ := conclusionService(t, false)
		f.judge = `{"changed":"飞书机器人多了任务进展开关","link":"https://hub.example.com/settings?tab=feishu","how":"打开设置看开关"}`
		agentTurn(s, f, proto.StatusWorking, "", 0)
		turnWithRecordedDeployment(t, s, f)
		waitFor(t, "the assistant's acceptance", func() bool { a := askOf(s, todo.ID); return a != nil && !a.AcceptedAt.IsZero() })
		if n := countAcceptCards(posts()); n != 0 {
			t.Fatalf("%d 请验收 cards before the review", n)
		}
		f.judge = `{"verdict":"done","reason":"已部署"}`
		agentTurn(s, f, proto.StatusWorking, "", 0)
		turnWithRecordedDeployment(t, s, f)
		waitFor(t, "the review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
		f.judge = `{"changed":"飞书机器人多了任务进展开关","link":"https://hub.example.com/settings?tab=feishu","how":"打开设置看开关"}`
		reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "note": "界面改好了", "user_check": true, "checks": completeReviewChecks(todoNow(s, todo.ID))})
		waitFor(t, "the 请验收 card", func() bool { a := askOf(s, todo.ID); return a != nil && a.MessageID != "" })
		if a := askOf(s, todo.ID); !a.AcceptedAt.IsZero() || a.ByAssistant || a.BySupervisor {
			t.Fatalf("ask %+v", a)
		}
		if n := waitingForOwner(s); n != 1 {
			t.Fatalf("%d waiting for the owner", n)
		}
	})
	t.Run("not deployed", func(t *testing.T) {
		s, f, todo, posts, _ := conclusionService(t, false)
		waitsForReview(t, s, f, todo.ID, "改好了，截图贴在 PR 里")
		f.judge = `{"changed":"登录页横屏不再崩溃","link":"","how":"iPad 横屏打开登录页"}`
		reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "note": "界面改好了", "user_check": true, "checks": completeReviewChecks(todoNow(s, todo.ID))})
		waitFor(t, "the 请验收 card", func() bool { a := askOf(s, todo.ID); return a != nil && a.MessageID != "" })
		if n := countAcceptCards(posts()); n != 1 {
			t.Fatalf("%d 请验收 cards", n)
		}
		if n := len(cardsWith(posts(), "结论：")); n != 0 {
			t.Fatalf("%d conclusion cards besides it", n)
		}
		if n := waitingForOwner(s); n != 1 {
			t.Fatalf("%d waiting for the owner", n)
		}
	})
	t.Run("only with accepted", func(t *testing.T) {
		s, f, todo, _, _ := conclusionService(t, false)
		waitsForReview(t, s, f, todo.ID, "做了一半")
		raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "rework", "note": "补测试", "user_check": true})
		if _, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw); err == nil {
			t.Fatal("user_check with rework was taken")
		}
	})
}

// A big feature (high priority) is the owner's to try (#3205): its "Please verify" waits for them after the supervisor
// accepted too; its conclusion would not, but it has none (the card is that).
func TestSupervisorAcceptedLeavesABigFeatureToTheOwner(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, _, _ := conclusionService(t, false)
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].Priority = "high"
	s.todos.mu.Unlock()
	deployedForReview(t, s, f, todo.ID)
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "note": "上线了", "checks": completeReviewChecks(todoNow(s, todo.ID))})
	s.verifySupervisedAsks(context.Background())
	if a := askOf(s, todo.ID); a == nil || !a.AcceptedAt.IsZero() {
		t.Fatalf("ask %+v", a)
	}
	if n := waitingForOwner(s); n != 1 {
		t.Fatalf("%d waiting for the owner", n)
	}
	ctx := context.Background()
	if got := s.todoStages(ctx)[todo.ID]; got.Stage != "deploy" || got.Verified {
		t.Fatalf("waiting for owner: %+v", got)
	}
	if ok, err := s.acceptTry(ctx, todo.ID, ""); err != nil || !ok.taken {
		t.Fatalf("owner acceptance: %v %v", ok, err)
	}
	if got := s.todoStages(ctx)[todo.ID]; got.Stage != "verify" || !got.Verified {
		t.Fatalf("accepted by owner: %+v", got)
	}
}

// A task with no supervisor keeps asking the owner: closed by the auto judge, its "Please verify" waits for their "Pass".
func TestUnsupervisedTaskStillAsksTheOwner(t *testing.T) {
	needsLarkLayer(t)
	s, f, posts := acceptService(t) // its deployed work asks the owner (ownerVerifiesAll), as a big feature's does
	todo := startedTask(t, s, f)
	if todo.Supervision != nil && todo.Supervision.Supervisor != "" {
		t.Fatalf("the task has a supervisor: %+v", todo.Supervision)
	}
	agentTurn(s, f, proto.StatusWorking, "", 0)
	turnWithRecordedDeployment(t, s, f)
	waitAcceptCard(t, s, todo.ID, posts)
	ctx := context.Background()
	if ok, err := s.autoDoneTodo(ctx, todo.ID, "做完了", time.Now().Add(-time.Minute)); err != nil || !ok {
		t.Fatalf("auto done: %v %v", ok, err)
	}
	s.verifySupervisedAsks(ctx)
	if a := askOf(s, todo.ID); a == nil || !a.AcceptedAt.IsZero() || a.BySupervisor {
		t.Fatalf("ask %+v", a)
	}
	if n := waitingForOwner(s); n != 1 {
		t.Fatalf("%d waiting for the owner", n)
	}
	if n := countAcceptCards(posts()); n != 1 {
		t.Fatalf("%d 请验收 cards", n)
	}
}

// acceptedAfterJudgeDelivery has the judge deliver a checked live result, proves it did not close the task, then has
// main accept it. user_check keeps the owner's own verification requirement where asked (#5732).
func acceptedAfterJudgeDelivery(t *testing.T, s *Service, f *fakeHub, todo string, userCheck bool) {
	t.Helper()
	sampleAt(t, s, 0)
	// The judge says it is live; the reply itself does not, so no deployed "Please verify" stands in for the conclusion.
	f.mu.Lock()
	f.judge = judgeWithChecks("live", true, true)
	f.mu.Unlock()
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了，两条都核对过，结论见上面的报告", 0)
	waitFor(t, "delivered for review", func() bool { return todoNow(s, todo).Supervision.State == reviewPending })
	if todoNow(s, todo).DoneAt != nil {
		t.Fatal("the judge closed the task")
	}
	reviewTask(t, s, todo, map[string]any{"verdict": "accepted", "note": "验收通过", "user_check": userCheck,
		"checks": completeReviewChecks(todoNow(s, todo))})
	if userCheck {
		// main's user_check keeps the task open for the owner's own verification rather than closing it.
		waitFor(t, "the owner's verification", func() bool { return todoNow(s, todo).Supervision.State == reviewUserCheck })
		if todoNow(s, todo).DoneAt != nil {
			t.Fatal("user_check closed the task before the owner verified it")
		}
	} else {
		waitFor(t, "closed by main", func() bool { return todoNow(s, todo).DoneAt != nil })
	}
	waitFor(t, "its ask", func() bool { a := askOf(s, todo); return a != nil && !a.AskedAt.IsZero() })
}

// Main's acceptance after a judge delivery passes the owner's conclusion: nothing waits for them (#5732).
func TestAcceptedAfterJudgeDeliveryWaitsForNobody(t *testing.T) {
	s, f, todo, posts, _ := conclusionService(t, false)
	acceptedAfterJudgeDelivery(t, s, f, todo.ID, false)
	a := askOf(s, todo.ID)
	if !a.Conclusion || a.AcceptedAt.IsZero() || !a.BySupervisor {
		t.Fatalf("ask %+v", a)
	}
	if n := waitingForOwner(s); n != 0 {
		t.Fatalf("%d waiting for the owner", n)
	}
	if ask := listedAsk(t, s, todo.ID); ask != nil {
		t.Fatalf("still on the to-do: %v", ask)
	}
	for _, c := range posts() {
		if strings.Contains(c, `"a":"accept"`) {
			t.Fatalf("a card still asks the owner:\n%s", c)
		}
	}
	s.verifySupervisedAsks(context.Background())
	if n := waitingForOwner(s); n != 0 {
		t.Fatalf("%d waiting after the sweep", n)
	}
}

// A big feature accepted with user_check still asks the owner to look themselves.
func TestAcceptedBigFeatureWithUserCheckWaitsForTheOwner(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].Priority = "high"
	s.todos.mu.Unlock()
	acceptedAfterJudgeDelivery(t, s, f, todo.ID, true)
	s.verifySupervisedAsks(context.Background())
	if a := askOf(s, todo.ID); a == nil || a.Conclusion || !a.AcceptedAt.IsZero() {
		t.Fatalf("ask %+v", a)
	}
	if n := waitingForOwner(s); n != 1 {
		t.Fatalf("%d waiting for the owner", n)
	}
}
