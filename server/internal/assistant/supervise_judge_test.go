package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/proto"
)

// fastJudgeRetry makes a failed judgment's second call come at once.
func fastJudgeRetry(t *testing.T) {
	t.Helper()
	old := taskDoneRetryAfter
	taskDoneRetryAfter = time.Millisecond
	t.Cleanup(func() { taskDoneRetryAfter = old })
}

func text(role, s string) proto.Item { return proto.Item{Kind: proto.KindText, Role: role, Text: s} }

const fullReport = "Super Relay 新空间加模型做完了：PR https://github.com/o/r/pull/77 已合并，已部署，接口实测返回新模型列表。"

// A replay of #3111's first case: the task's agent reported the work done; the user then asked it in its terminal
// "好了吗？" and it answered "好了，全部完成。" and stopped. The judge is given the earlier report with the short
// answer, and the task waits for review: no "stopped short" message.
func TestSupervisedDoneAfterTheUsersOwnCheck(t *testing.T) {
	fastJudgeRetry(t)
	s, f, todo := newSupervisedService(t)
	f.pageTurns = true
	f.mu.Lock()
	prompt, _ := f.starts[0]["prompt"].(string)
	f.mu.Unlock()
	// A judge that can tell it is done only from the report: the short answer alone is unsure.
	f.judgeBy = func(in string) string {
		if strings.Contains(in, "pull/77 已合并") {
			return `{"verdict":"done","reason":"已合并并部署"}`
		}
		return `{"verdict":"unsure","reason":"只说好了"}`
	}
	f.setItems(text("user", prompt), text("assistant", fullReport), text("user", "好了吗？"), text("assistant", "好了，全部完成。"))
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "好了，全部完成。", 0)
	waitFor(t, "the message", func() bool { return len(f.notices()) == 1 })

	n := entryLine(f.notices()[0])
	if !strings.Contains(n, "待逐项复核，不代表完成（已合并并部署）") || strings.Contains(n, "没做完") || strings.Contains(n, "没判断出来") {
		t.Fatalf("message:\n%s", n)
	}
	if v := todoNow(s, todo.ID).Supervision; v.State != reviewPending {
		t.Fatalf("not waiting for review: %+v", v)
	}
	f.mu.Lock()
	in := f.judgeAsked[len(f.judgeAsked)-1]
	f.mu.Unlock()
	for _, want := range []string{"它上一轮的回复（同一个会话里，这一轮之前）：\n" + fullReport, "这一轮收到的请求（不一定来自用户，不替代原任务）：\n好了吗？", "它的回复：\n好了，全部完成。"} {
		if !strings.Contains(in, want) {
			t.Errorf("judge input lacks %q:\n%s", want, in)
		}
	}
}

// A transient judgment failure is asked again inline. A valid unsure verdict still tells the supervisor the hub could
// not tell, never that the agent stopped short.
func TestSupervisedUnjudgedIsNotStoppedShort(t *testing.T) {
	fastJudgeRetry(t)
	s, f, todo := newSupervisedService(t)
	f.pageTurns = true
	f.setItems(text("assistant", fullReport))

	f.judgeFails = 1 // one hiccup: the second call says done
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, fullReport, 0)
	waitFor(t, "the review message", func() bool { return len(f.notices()) == 1 })
	if n := f.notices()[0]; !strings.Contains(n, "待逐项复核，不代表完成") {
		t.Fatalf("after one failure:\n%s", n)
	}
	if v := todoNow(s, todo.ID).Supervision; v.State != reviewPending {
		t.Fatalf("not waiting for review: %+v", v)
	}

	// More work, and this time the judge returns a valid unsure verdict.
	f.judge = `{"verdict":"unsure","reason":"证据不足"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "又改了一处", 0)
	waitFor(t, "the second message", func() bool { return len(f.notices()) == 2 })
	n := entryLine(f.notices()[1])
	if !strings.Contains(n, "Agent 停下了，hub 没判断出来是不是做完了（不是说没做完）（看不出做没做完：证据不足）") || strings.Contains(n, "停下了，但没做完") {
		t.Fatalf("after an unsure judgment:\n%s", n)
	}
}

// With no model set up for the judge, the supervisor hears that the judge has no model, not that the model erred: a
// retry will not mend it, and the supervisor reads the reply itself.
func TestSupervisedUnjudgedSaysTheJudgeHasNoModel(t *testing.T) {
	fastJudgeRetry(t)
	s, f, _ := newSupervisedService(t)
	f.pageTurns = true
	f.mu.Lock()
	f.hello.Models.AI, f.hello.Models.TriageAI = ai.Config{}, nil
	f.mu.Unlock()
	s.settingsChanged(context.Background())
	if _, err := s.purposeModel("inbox.judge").Complete(context.Background(), "", "reply", 16); !ai.IsNotConfigured(err) {
		t.Fatalf("the judge has a model: %v", err)
	}
	f.setItems(text("assistant", fullReport))
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, fullReport, 0)
	waitFor(t, "the supervisor's message", func() bool { return len(f.notices()) == 1 })
	n := entryLine(f.notices()[0])
	if !strings.Contains(n, "signal=unjudged") || !strings.Contains(n, "（判定模型未配置）") || strings.Contains(n, "判定模型出错") {
		t.Fatalf("no model for the judge:\n%s", n)
	}
}

// #3111's second case: a task accepted by its review is opened again when its agent is sent more; that work, not
// judged done, tells the supervisor nothing ("stopped short" was wrong); judged done, it waits for review again.
func TestAcceptedTaskFollowUpIsNotStoppedShort(t *testing.T) {
	fastJudgeRetry(t)
	s, f, todo := newSupervisedService(t)
	f.pageTurns = true
	ctx := context.Background()
	f.setItems(text("assistant", fullReport))
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, fullReport, 0)
	waitFor(t, "the review message", func() bool { return len(f.notices()) == 1 })
	raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "accepted", "note": "可以", "checks": completeReviewChecks(todoNow(s, todo.ID))})
	if _, err := s.runTool(ctx, "server", "conv1", "task_review", raw); err != nil {
		t.Fatal(err)
	}
	if got := todoNow(s, todo.ID); got.DoneAt == nil {
		t.Fatalf("not closed: %+v", got)
	}

	// Its agent is sent more (the owner's message on its card: a turn nobody sent leaves it closed, #7539): the task
	// is open again.
	if _, err := s.routeToTask(ctx, "conv1", todo.ID, "顺手把 README 也改一下"); err != nil {
		t.Fatal(err)
	}
	f.setItems(text("assistant", fullReport), text("user", "顺手把 README 也改一下"), text("assistant", "README 改了一半"))
	agentTurn(s, f, proto.StatusWorking, "", 0)
	reopened := todoNow(s, todo.ID)
	if reopened.DoneAt != nil || reopened.Supervision.State != reviewAccepted {
		t.Fatalf("not reopened by its agent's work: %+v %+v", reopened, reopened.Supervision)
	}
	// That work judged not finished, unsure or not at all: the supervisor is not told (signal is synchronous).
	for _, j := range []struct {
		c   taskDoneCheck
		err error
	}{
		{taskDoneCheck{Verdict: verdictNotDone, Reason: "还差一处"}, nil},
		{taskDoneCheck{Verdict: verdictUnsure, Reason: "看不出"}, nil},
		{taskDoneCheck{}, errors.New("judge: 400")},
	} {
		s.superviseJudged(ctx, reopened, time.Now(), j.c, j.err)
		if k := stopSignals(s, todo.ID); k != "" {
			t.Fatalf("%+v %v: told %s", j.c, j.err, k)
		}
	}
	// The same stop of a task never reviewed is told (the guard is only for one closed by review).
	open := reopened
	sup := *open.Supervision
	sup.State = ""
	open.Supervision = &sup
	s.superviseJudged(ctx, open, time.Now(), taskDoneCheck{Verdict: verdictNotDone, Reason: "还差一处"}, nil)
	if k := stopSignals(s, todo.ID); k != sigIdle {
		t.Fatalf("an open task's stop: %q", k)
	}
	s.supervise.mu.Lock()
	delete(s.supervise.pending, todo.ID+"|"+sigIdle)
	s.supervise.mu.Unlock()

	f.judge = `{"verdict":"done","reason":"README 也改好了"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "README 也改好了", 0)
	waitFor(t, "the review again", func() bool { return len(f.notices()) == 2 })
	if n := f.notices()[1]; !strings.Contains(n, "待逐项复核，不代表完成（README 也改好了）") {
		t.Fatalf("message:\n%s", n)
	}
}

// entryLine is a supervision message's first task line (what it says of the task, without the instructions).
func entryLine(msg string) string {
	for _, l := range strings.Split(msg, "\n") {
		if strings.HasPrefix(l, "1. 任务") {
			return l
		}
	}
	return ""
}

// stopSignals names the stopped-short or unjudged signal waiting or told for todo id ("" none).
func stopSignals(s *Service, id string) string {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, k := range []string{sigIdle, sigUnjudged} {
		if _, ok := b.pending[id+"|"+k]; ok {
			return k
		}
		if _, ok := b.sent[id+"|"+k]; ok {
			return k
		}
	}
	return ""
}

func TestSplitLastTurn(t *testing.T) {
	q := text("user", "排队的")
	q.Queued = true
	items := []proto.Item{text("user", "做 A"), text("assistant", "A 做完了"), text("user", "好了吗？"), q, text("assistant", "好了")}
	earlier, last := splitLastTurn(items)
	if len(earlier) != 2 || len(last) != 3 {
		t.Fatalf("earlier %d, last %d", len(earlier), len(last))
	}
	if earlier, last := splitLastTurn(items[:2]); earlier != nil || len(last) != 2 {
		t.Fatalf("one turn: %v / %v", earlier, last)
	}
}

// A side turn (a peer's message, another conversation) never brings the earlier report along: only the user's own
// follow-up in the task's session does (#3111).
func TestEarlierReportNotForSideTurns(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	f.pageTurns = true
	peer := text("user", "好了吗？")
	peer.From = "another-agent"
	f.setItems(text("assistant", fullReport), peer, text("assistant", "好了，全部完成。"))
	replies, _, _ := s.taskReplies(context.Background(), todoNow(s, todo.ID))
	if len(replies) != 1 || replies[0].Source != "peer" || replies[0].Earlier != "" {
		t.Fatalf("replies = %+v", replies)
	}
	f.setItems(text("assistant", fullReport), text("user", "好了吗？"), text("assistant", "好了，全部完成。"))
	replies, _, _ = s.taskReplies(context.Background(), todoNow(s, todo.ID))
	if len(replies) != 1 || replies[0].Source != "" || replies[0].Earlier != fullReport {
		t.Fatalf("own follow-up: %+v", replies)
	}
}
