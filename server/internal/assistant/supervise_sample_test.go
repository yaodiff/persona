package assistant

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Spot-check reviews (#3128, supervise_sample.go): below 100%, the judge records its own checks as delivery evidence.
// Every finished supervised task still waits for the main conversation's task_review; a judge never accepts or closes
// one, including a task outside the sampled share (#5732).

// The judge's answer for newSupervisedService's task (its two criteria), each check met or not, with the delivery.
func judgeWithChecks(delivery string, met1, met2 bool) string {
	return fmt.Sprintf(`{"verdict":"done","reason":"PR 已合并并上线","delivery":%q,"checks":[`+
		`{"criterion":"PR 合并到 master","checkable":true,"met":%v,"evidence":"https://github.com/example/app/pull/7 merged"},`+
		`{"criterion":"浅色 / 深色截图贴进 PR","checkable":true,"met":%v,"evidence":"两张截图在 PR 描述里"}]}`, delivery, met1, met2)
}

// finishTurn has the supervised task's agent finish a stretch of work, the judge answering judge.
func finishTurn(t *testing.T, s *Service, f *fakeHub, judge string) {
	t.Helper()
	f.mu.Lock()
	f.judge = judge
	f.mu.Unlock()
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了，证据见 PR\n交付：已上线并实测；卡点：无；谁来解：无", 0)
}

func reviewSignal(s *Service, id string) (superviseSignal, bool) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	g, ok := b.pending[id+"|"+sigReview]
	return g, ok
}

func sampleAt(t *testing.T, s *Service, pct int) {
	t.Helper()
	setSettings(t, s, func(v *Settings) { v.ReviewSamplePercent = &pct })
}

// A passed judge check is delivery evidence, never acceptance. Even when sampling would skip review, the task stays
// open for the main conversation with its conclusion (#5732).
func TestJudgeCheckDeliversWithoutClosing(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	sampleAt(t, s, 0)
	finishTurn(t, s, f, judgeWithChecks("live", true, true))
	waitFor(t, "delivered for review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	cur := todoNow(s, todo.ID)
	if cur.DoneAt != nil || cur.Check == nil || cur.Check.Verdict != verdictDone {
		t.Fatalf("delivered task %+v", cur)
	}
	v := cur.Supervision
	if len(v.Reviews) != 0 {
		t.Fatalf("judge accepted without the supervisor: %+v", v)
	}
	if g, ok := reviewSignal(s, todo.ID); !ok || g.Detail != "PR 已合并并上线" {
		t.Fatalf("review signal %+v (%v)", g, ok)
	}
}

// A sampled task that passed still waits for task_review, its supervision message saying it is a spot check.
func TestSampledTaskStillWaitsForReview(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	sampleAt(t, s, 20)
	finishTurn(t, s, f, judgeWithChecks("live", true, true))
	waitFor(t, "the review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	if todoNow(s, todo.ID).DoneAt != nil {
		t.Fatal("a sampled task was closed")
	}
	g, ok := reviewSignal(s, todo.ID)
	if !ok || g.Detail != "PR 已合并并上线" {
		t.Fatalf("review signal %+v (%v)", g, ok)
	}
}

// A task that fails the check — a criterion not met, or not live — waits for task_review whatever the share.
func TestFailedCheckWaitsForReview(t *testing.T) {
	for _, tc := range []struct {
		name, judge string
	}{
		{"a criterion not met", judgeWithChecks("live", true, false)},
		{"not live", judgeWithChecks("merged", true, true)},
		{"not checkable", strings.Replace(judgeWithChecks("live", true, true), `"checkable":true`, `"checkable":false`, 1)},
		{"no checks given", `{"verdict":"done","reason":"PR 已合并","delivery":"live"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			sampleAt(t, s, 0)
			finishTurn(t, s, f, tc.judge)
			waitFor(t, "the review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
			if todoNow(s, todo.ID).DoneAt != nil {
				t.Fatal("closed though the check failed")
			}
			if g, ok := reviewSignal(s, todo.ID); !ok || strings.HasPrefix(g.Detail, "抽查") {
				t.Fatalf("review signal %+v (%v)", g, ok)
			}
		})
	}
}

// The default (100%) keeps every finished task waiting for task_review, even when everything would pass.
func TestDefaultReviewsEveryTask(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	if got := s.reviewSamplePercent(context.Background()); got != 100 {
		t.Fatalf("default share %d", got)
	}
	finishTurn(t, s, f, judgeWithChecks("live", true, true))
	waitFor(t, "the review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	if cur := todoNow(s, todo.ID); cur.DoneAt != nil || len(cur.Supervision.Reviews) != 0 {
		t.Fatalf("closed or reviewed at the default: %+v", cur)
	}
	if g, ok := reviewSignal(s, todo.ID); !ok || g.Detail != "PR 已合并并上线" {
		t.Fatalf("review signal %+v (%v)", g, ok)
	}
}

// The judge is asked for the checks only while spot-checking is on, and its answer is matched to the ledger's criteria.
func TestJudgeAsksForChecksOnlyWhenSampling(t *testing.T) {
	in := taskDoneInput{Owner: "Alex", Title: "Fix the login retry", Criteria: []string{"PR merged", "deployed"},
		Replies: []taskDoneReply{doneReply("Deployed and verified.")}}
	m := &cannedJSON{answer: `{"verdict":"done","reason":"ok","delivery":"live","checks":[{"criterion":"x","checkable":true,"met":true,"evidence":"e"}]}`}
	c, err := judgeTaskDoneWith(context.Background(), m, in)
	if err != nil {
		t.Fatal(err)
	}
	if c.AutoCheck != nil || strings.Contains(m.system, `"checks"`) || c.Delivery != nil {
		t.Fatalf("checks without sampling: %+v\n%s", c.AutoCheck, m.system)
	}
	in.Checks = true
	c, err = judgeTaskDoneWith(context.Background(), m, in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.system, `"checks"`) || m.system != prompts.RenderTaskDone(prompts.TaskDone{Owner: "Alex", Checks: true}) {
		t.Fatal("the judge was not asked for the checks")
	}
	ac := c.AutoCheck
	if ac == nil || ac.Delivery != deliveryLive || len(ac.Checks) != 2 || ac.Checks[0].Criterion != "PR merged" || ac.Checks[0].Met ||
		ac.Checks[1].Met || ac.passed() { // one check for two criteria: none counts
		t.Fatalf("auto check %+v", ac)
	}
	if c.Delivery != nil || c.Verdict != verdictDone {
		t.Fatalf("checks changed the delivery gate's verdict: %+v", c)
	}
}

// PUT /api/v1/assistant takes the share (0–100) and GET says the one in effect.
func TestReviewSamplePercentSetting(t *testing.T) {
	s, _, _ := newSupervisedService(t)
	if status, _ := serveJSON(t, s, "PUT", "/api/v1/assistant", "en", `{"review_sample_percent":101}`); status != http.StatusBadRequest {
		t.Fatalf("101%%: %d", status)
	}
	if status, body := serveJSON(t, s, "PUT", "/api/v1/assistant", "en", `{"review_sample_percent":30}`); status != http.StatusOK {
		t.Fatalf("30%%: %d %v", status, body)
	}
	if got := s.reviewSamplePercent(context.Background()); got != 30 {
		t.Fatalf("share %d", got)
	}
	status, body := serveJSON(t, s, "GET", "/api/v1/assistant", "en", "")
	if status != http.StatusOK || body["review_sample_percent"] != float64(30) {
		t.Fatalf("GET: %d %v", status, body["review_sample_percent"])
	}
}

func TestGoldenTaskDoneChecks(t *testing.T) {
	golden(t, "task_done.checks", prompts.RenderTaskDone(prompts.TaskDone{Owner: "User", Checks: true}))
}

// A rework round waits for the review the supervisor was promised, however well it passes the judge's check.
func TestJudgeLeavesAReworkRoundToTheSupervisor(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	sampleAt(t, s, 0)
	finishTurn(t, s, f, judgeWithChecks("live", true, false))
	waitFor(t, "the review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "rework", "note": "截图没贴，另外把按钮文案改短"})
	finishTurn(t, s, f, judgeWithChecks("live", true, true))
	waitFor(t, "the review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	if todoNow(s, todo.ID).DoneAt != nil {
		t.Fatal("the judge closed a rework round")
	}
}

// A task reopened after supervisor acceptance is delivered for review again, never closed by the judge (#5732).
func TestJudgeLeavesAReopenedTaskToTheSupervisor(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	sampleAt(t, s, 0)
	finishTurn(t, s, f, judgeWithChecks("live", true, true))
	waitFor(t, "the first review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "note": "验收通过",
		"checks": completeReviewChecks(todoNow(s, todo.ID))})
	if todoNow(s, todo.ID).DoneAt == nil {
		t.Fatal("supervisor acceptance did not close the task")
	}
	if err := s.reopenTodo(context.Background(), todo.ID); err != nil {
		t.Fatal(err)
	}
	finishTurn(t, s, f, judgeWithChecks("live", true, true))
	waitFor(t, "the review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	if todoNow(s, todo.ID).DoneAt != nil {
		t.Fatal("the judge closed a reopened task again")
	}
}

// Checks that are not one per criterion, each copied in its place, count as not met.
func TestMisalignedChecksDoNotPass(t *testing.T) {
	criteria := []string{"PR merged", "deployed"}
	ok := func(c string) autoCheckItem {
		return autoCheckItem{ReviewCheck: ReviewCheck{Criterion: c, Met: true, Evidence: "e"}, Checkable: true}
	}
	for name, got := range map[string][]autoCheckItem{
		"one more":  {ok("something else"), ok("PR merged"), ok("deployed")},
		"one less":  {ok("PR merged")},
		"swapped":   {ok("deployed"), ok("PR merged")},
		"rephrased": {ok("the PR is merged"), ok("deployed")},
	} {
		if autoCheckOf(criteria, got, deliveryLive).passed() {
			t.Errorf("%s: passed", name)
		}
	}
	if !autoCheckOf(criteria, []autoCheckItem{ok(" PR  merged"), ok("deployed")}, deliveryLive).passed() {
		t.Error("the same criteria, spacing aside, do not pass")
	}
}

// Live counts only on the judge's own word: the agent's delivery line alone does not let it close.
func TestAutoCheckLiveIsTheJudgesOwn(t *testing.T) {
	in := taskDoneInput{Title: "t", Criteria: []string{"PR merged"}, Checks: true,
		Replies: []taskDoneReply{doneReply("done\n交付：已上线并实测；卡点：无；谁来解：无")}}
	m := &cannedJSON{answer: `{"verdict":"done","reason":"ok","checks":[{"criterion":"PR merged","checkable":true,"met":true,"evidence":"e"}]}`}
	c, err := judgeTaskDoneWith(context.Background(), m, in)
	if err != nil {
		t.Fatal(err)
	}
	if c.AutoCheck.passed() {
		t.Fatalf("passed on the agent's line alone: %+v", c.AutoCheck)
	}
}
