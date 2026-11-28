package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// conclusionService is newSupervisedService with the Lark bot on (task reports on with the execution steps, the card as
// before #3218, and only_conclusions as given): the cards it
// posts and the notes it writes are kept.
func conclusionService(t *testing.T, onlyConclusions bool) (s *Service, f *fakeHub, todo Todo, posts, notes func() []string) {
	t.Helper()
	return conclusionServiceOn(t, nil, onlyConclusions)
}

// conclusionServiceOn is conclusionService with st as its database (nil: none).
func conclusionServiceOn(t *testing.T, st Store, onlyConclusions bool) (s *Service, f *fakeHub, todo Todo, posts, notes func() []string) {
	t.Helper()
	s, f, todo = newSupervisedServiceOn(t, st)
	s.reports.mu.Lock()
	s.reports.delay = testReportDelay
	s.reports.mu.Unlock()
	var mu sync.Mutex
	var cards, said []string
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_me", PublicURL: "https://hub.example.com",
			Features: FeishuFeatures{Main: true, Cards: true, Reports: true, ReportSteps: true, OnlyConclusions: onlyConclusions}}, nil
	})
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m struct {
			Card string `json:"card"`
		}
		_ = json.Unmarshal(p, &m)
		mu.Lock()
		cards = append(cards, m.Card)
		mu.Unlock()
		return map[string]any{"message_id": "om_c"}, nil
	})
	f.answerApp(AppFeishu, FeishuUpdateCard, func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	f.answerApp(AppFeishu, FeishuText, func(p json.RawMessage) (any, error) {
		var m map[string]string
		_ = json.Unmarshal(p, &m)
		mu.Lock()
		said = append(said, m["text"])
		mu.Unlock()
		return map[string]any{}, nil
	})
	keep := func(list *[]string) func() []string {
		return func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), *list...) }
	}
	return s, f, todo, keep(&cards), keep(&said)
}

// reviewTask runs task_review on todo from the main conversation and returns what it answers.
func reviewTask(t *testing.T, s *Service, todo string, args map[string]any) string {
	t.Helper()
	args["todo_id"] = todo
	if args["verdict"] == reviewAccepted {
		if _, supplied := args["checks"]; !supplied {
			args["checks"] = completeReviewChecks(todoNow(s, todo))
		}
	}
	asDecision(args)
	raw, _ := json.Marshal(args)
	res, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw)
	if err != nil {
		t.Fatalf("task_review %v: %v", args, err)
	}
	return res
}

// asDecision fills in what a needs_user review must carry to be a decision (#3804, decision_test.go tests each part)
// where a test about something else left it out: why only the owner decides (one that cannot be undone, so nothing is
// taken by default unless the test sets a reversible deadline), two options with the recommended first, and what
// waiting costs. What the test gave is kept; a review without reason or recommend is still refused.
func asDecision(args map[string]any) {
	if args["verdict"] != "needs_user" {
		return
	}
	if _, ok := args["why_owner"]; !ok {
		args["why_owner"] = whyIrreversible
		if rev, _ := args["reversible"].(bool); rev {
			args["why_owner"] = whyPriority
		}
	}
	if _, ok := args["options"]; !ok {
		if rec, _ := args["recommend"].(string); rec != "" {
			args["options"] = []map[string]string{{"label": rec, "leads_to": "照推荐的做"}, {"label": "先不动", "leads_to": "任务停在这里"}}
		}
	}
	if _, ok := args["cost_of_waiting"]; !ok {
		args["cost_of_waiting"] = "任务停着等你"
	}
}

// waitsForReview has the task's agent finish a turn with reply until the judge's "done" puts the task up for review.
func waitsForReview(t *testing.T, s *Service, f *fakeHub, todo, reply string) {
	t.Helper()
	f.judge = `{"verdict":"done","reason":"调研有结论了"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, reply, 0)
	waitFor(t, "the review", func() bool { return todoNow(s, todo).Supervision.State == reviewPending })
}

func cardsWith(posts []string, want string) (out []string) {
	for _, p := range posts {
		if strings.Contains(p, want) {
			out = append(out, p)
		}
	}
	return out
}

// A supervised research task that ends with "cannot be done" is one card: that verdict, the reason, the link; its
// closing is not a line of the task report as well. The supervisor checked it, so the card says so and nothing waits
// for the owner's "OK" (#3240).
func TestSupervisedCannotBeDoneIsOneConclusionCard(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, posts, notes := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "查了，对方接口不对外开放，直连做不了。文档 https://docs.example.com/api")
	waitFor(t, "the turn's report", func() bool { return len(cardsWith(posts(), "这一轮做完")) == 1 }) // the switch is off: as before
	before := len(posts())
	res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "outcome": "cannot", "reason": "对方接口不对外开放",
		"link": "https://docs.example.com/api", "note": "调研结论：做不了"})
	if !strings.Contains(res, "conclusion card") {
		t.Fatalf("the review's answer does not say the card went out: %s", res)
	}
	time.Sleep(6 * testReportDelay) // a closing line of the task report would be out by now
	after := posts()[before:]
	if len(after) != 1 {
		t.Fatalf("%d cards after the review, want the conclusion only:\n%s", len(after), strings.Join(after, "\n---\n"))
	}
	card := after[0]
	for _, want := range []string{"结论：修登录崩溃", "⛔ 做不了：对方接口不对外开放", "https://docs.example.com/api", "督办已核对证据"} {
		if !strings.Contains(card, want) {
			t.Errorf("card lacks %q:\n%s", want, card)
		}
	}
	if strings.Contains(card, `"a":"accept"`) {
		t.Errorf("the card still asks the owner:\n%s", card)
	}
	if strings.Contains(card, "🏁") || strings.Contains(card, "改了什么") {
		t.Fatalf("not a conclusion card:\n%s", card)
	}
	a := askOf(s, todo.ID)
	if a == nil || !a.Conclusion || a.Outcome != outcomeCannot || a.MessageID == "" || a.AcceptedAt.IsZero() || !a.BySupervisor {
		t.Fatalf("ask %+v", a)
	}
	ctx := context.Background()
	if _, total := s.snapshotUnaccepted(ctx, s.location(ctx)); total != 0 {
		t.Fatalf("%d waiting for the owner", total)
	}
	_ = notes
}

// judgeConcluded closes supervised todo, a big feature (high priority, #3205), as the done judge does (judgeClose) with
// a cannot conclusion, which still waits for the owner's "OK": a big feature is the owner's to look at (#3356).
func judgeConcluded(t *testing.T, s *Service, todo string) {
	t.Helper()
	ctx := context.Background()
	s.todos.mu.Lock()
	s.todos.todos[todo].Priority = "high"
	s.todos.mu.Unlock()
	if _, _, err := s.updateSupervision(ctx, todo, func(v *Supervision) bool {
		v.State = reviewAccepted
		v.Reviews = append(v.Reviews, TaskReview{At: time.Now().UTC(), Verdict: reviewAccepted, By: reviewerJudge})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.reviewDoneTodo(ctx, todo, "判官核对通过", true); err != nil || !ok {
		t.Fatalf("close: %v %v", ok, err)
	}
	cur := todoNow(s, todo)
	s.askConclusion(ctx, cur, conclusionInput{Outcome: outcomeCannot, Reason: "对方接口不对外开放", Link: "https://docs.example.com/api",
		Verified: supervisorVerified(cur)})
}

// A conclusion with no link given takes the task's own: a web address in the review's evidence before the agent's chat.
func TestConclusionFindsItsLink(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, posts, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "做完了")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "note": "做完了",
		"checks": []ReviewCheck{
			{Criterion: "PR 合并到 master", Met: true, Evidence: "https://github.com/o/r/pull/7 merged"},
			{Criterion: "浅色 / 深色截图贴进 PR", Met: true, Evidence: "both screenshots verified"},
		}})
	cards := cardsWith(posts(), "结论：")
	if len(cards) != 1 || !strings.Contains(cards[0], "https://github.com/o/r/pull/7") || !strings.Contains(cards[0], "✅ 做完了：做完了") {
		t.Fatalf("cards %q", cards)
	}
}

// Deployed work keeps its "Please verify" card, now with the verdict line on top; the review that accepts the task then
// sends no second card.
func TestDeployedWorkKeepsPleaseVerifyWithTheVerdictOnTop(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, posts, _ := conclusionService(t, false)
	s.ownerVerifiesAll = true
	f.judge = `{"changed":"飞书机器人多了任务进展开关","link":"https://hub.example.com/settings?tab=feishu","how":"打开设置看开关"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	s.setTaskStage(t.Context(), todo.ID, "", "https://github.com/o/r/pull/9")
	agentTurn(s, f, proto.StatusDone, deployedReply+"\n交付：已上线并实测", 0)
	waitFor(t, "the 请验收 card", func() bool { a := askOf(s, todo.ID); return a != nil && a.MessageID != "" })
	card := acceptCardOf(posts())
	top, lines := strings.Index(card, "✅ 做完了：已部署，按下面验收"), strings.Index(card, "改了什么")
	if top < 0 || lines < 0 || top > lines {
		t.Fatalf("the verdict line is not on top of the three lines:\n%s", card)
	}
	f.judge = `{"verdict":"done","reason":"已部署"}`
	agentTurn(s, f, proto.StatusWorking, "", 0)
	turnWithRecordedDeployment(t, s, f)
	waitFor(t, "the review", func() bool { return todoNow(s, todo.ID).Supervision.State == reviewPending })
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "note": "上线了", "checks": completeReviewChecks(todoNow(s, todo.ID))})
	time.Sleep(6 * testReportDelay)
	if n := len(cardsWith(posts(), "结论：")); n != 0 {
		t.Fatalf("%d conclusion cards for deployed work", n)
	}
	if n := countAcceptCards(posts()); n != 1 {
		t.Fatalf("%d 请验收 cards", n)
	}
	// The supervisor checked it: its "Please verify" is passed for the owner, who is not asked to press "Pass" (#3240).
	ctx := context.Background()
	if a := askOf(s, todo.ID); a == nil || a.AcceptedAt.IsZero() || !a.BySupervisor {
		t.Fatalf("the ask is not passed by the supervisor: %+v", a)
	}
	if _, total := s.snapshotUnaccepted(ctx, s.location(ctx)); total != 0 {
		t.Fatalf("%d still waiting for the owner", total)
	}
}

// A task sent back twice and still short goes to the owner only as a decision they can answer (#3804): a third rework
// that states none is refused and changes nothing, so the supervisor states one; with one, the card asks that, not
// with the rework the agent was told.
func TestReworkOverAsksTheOwnerHowToGoOn(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, posts, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "补了一部分")
	if _, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool { v.Round = maxReworkRounds; return true }); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "rework", "note": "把深色截图补上，再跑一遍 CI"})
	_, err := s.runTool(context.Background(), "server", "conv1", "task_review", raw)
	if err == nil || !strings.Contains(err.Error(), "sent back 2 times") || !strings.Contains(err.Error(), "why_owner") {
		t.Fatalf("a third rework with no decision: %v", err)
	}
	if v := todoNow(s, todo.ID).Supervision; v.State != reviewPending || len(v.Reviews) != 0 || askOf(s, todo.ID) != nil {
		t.Fatalf("the refused rework changed the task: %+v, ask %+v", v, askOf(s, todo.ID))
	}
	res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "rework", "note": "把深色截图补上，再跑一遍 CI",
		"why_owner": "priority", "reason": "返工了 2 轮还没做完，接下来怎么办", "recommend": "再返工一轮",
		"options":         []map[string]string{{"label": "就这样收下", "leads_to": "缺深色截图，直接合并"}, {"label": "再返工一轮", "leads_to": "再等大约半小时"}},
		"cost_of_waiting": "PR 一直开着"})
	if !strings.Contains(res, "sent back 2 times") || !strings.Contains(res, "went to them in Lark") {
		t.Fatalf("answer: %s", res)
	}
	cards := cardsWith(posts(), "结论：")
	if len(cards) != 1 || !strings.Contains(cards[0], "🤔 要你拍板：返工了 2 轮还没做完，接下来怎么办") || strings.Contains(cards[0], "深色截图补上") {
		t.Fatalf("cards %q", cards)
	}
	// The recommended option is listed first whatever order the review gave them in.
	if a := askOf(s, todo.ID); a == nil || len(a.Options) != 2 || a.Options[0].Label != "再返工一轮" || todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatalf("ask %+v", a)
	}
}

// "Needs your decision" carries the recommended option: "OK" takes it, which goes into the task's agent; the card is
// over then.
func TestDecisionCarriesTheRecommendationAndOKTakesIt(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, posts, notes := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%",
		"note": "要用户定上线方式"})
	if !strings.Contains(res, `"OK" takes the recommendation`) {
		t.Fatalf("answer: %s", res)
	}
	cards := cardsWith(posts(), "结论：")
	if len(cards) != 1 {
		t.Fatalf("%d conclusion cards", len(cards))
	}
	for _, want := range []string{"🤔 要你拍板：先灰度还是直接全量", "推荐：先灰度 10%", "按推荐来", "回「OK」就按推荐来"} {
		if !strings.Contains(cards[0], want) {
			t.Errorf("card lacks %q:\n%s", want, cards[0])
		}
	}
	mid := askOf(s, todo.ID).MessageID
	sent := len(sentToAgent(f))
	if !s.acceptReply(context.Background(), "om_ok", "OK", false, mid) {
		t.Fatal("not taken")
	}
	got := sentToAgent(f)[sent:]
	if len(got) != 1 || !strings.Contains(got[0], "先灰度 10%") {
		t.Fatalf("to the agent: %q", got)
	}
	if n := notes(); len(n) == 0 || !strings.Contains(n[len(n)-1], "按推荐来") {
		t.Fatalf("notes %q", n)
	}
	// Decided: the task no longer waits for the owner, so the agent's next stop goes back to the supervisor.
	if st := todoNow(s, todo.ID).Supervision.State; st == reviewNeedsUser {
		t.Fatalf("still waits for the owner: %q", st)
	}
	// Taken once: a second "OK" takes nothing more.
	s.acceptReply(context.Background(), "om_ok2", "OK", false, mid)
	if n := len(sentToAgent(f)); n != sent+1 {
		t.Fatalf("%d sends after a second OK", n-sent)
	}
}

// With only_conclusions on, a task's turn ending (an intermediate state) is not pushed; its conclusion still is.
func TestOnlyConclusionsPushesNoProgress(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, posts, _ := conclusionService(t, true)
	waitsForReview(t, s, f, todo.ID, "查完了，可以做，方案见 https://docs.example.com/plan")
	time.Sleep(6 * testReportDelay) // a report of the turn would be out by now
	if p := posts(); len(p) != 0 {
		t.Fatalf("pushed before the conclusion:\n%s", strings.Join(p, "\n---\n"))
	}
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "accepted", "reason": "能做，方案已写好", "link": "https://docs.example.com/plan",
		"checks": completeReviewChecks(todoNow(s, todo.ID))})
	time.Sleep(6 * testReportDelay)
	if p := posts(); len(p) != 1 || !strings.Contains(p[0], "✅ 做完了：能做，方案已写好") {
		t.Fatalf("posts:\n%s", strings.Join(p, "\n---\n"))
	}
}

// The web briefing, the briefing's Lark card and the board say the conclusion in the same words as its own card.
func TestBriefingSaysTheSameVerdictAsTheCard(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, posts, _ := conclusionService(t, false)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "查了，做不了")
	judgeConcluded(t, s, todo.ID)
	const verdict = "⛔ 做不了：对方接口不对外开放"
	if len(cardsWith(posts(), botcard.EscapeMarkdown(verdict))) != 1 {
		t.Fatalf("the card does not say %q", verdict)
	}
	if s.briefingOn() {
		facts := s.briefFactsAt(ctx, time.Now(), "")
		body := s.briefBodyOf(facts, "早报")
		var row *briefBodyRow
		for i := range body.Accept {
			if body.Accept[i].ID == todo.ID {
				row = &body.Accept[i]
			}
		}
		if row == nil || row.Verdict != verdict || row.Outcome != outcomeCannot {
			t.Fatalf("the brief's row %+v", row)
		}
		card, _, _ := s.briefCard(facts, "早报", "")
		if js := card.JSON(); !strings.Contains(js, botcard.EscapeMarkdown(verdict)) || !strings.Contains(js, "知道了") {
			t.Fatalf("the briefing card does not say it:\n%s", js)
		}
	}
	var onBoard bool
	for _, r := range s.boardRows(ctx, s.botLang(ctx), s.webBase(ctx), time.Now()) {
		onBoard = onBoard || r.id == todo.ID && strings.Contains(r.summary, verdict)
	}
	if !onBoard {
		t.Fatal("the board does not say it")
	}
}

// A recommendation that cannot reach the task's agent is not taken: the button answers why (not "already handled"), the
// reply is told it did not go, nothing is recorded as decided, and the next press takes it once the agent is reachable.
func TestRecommendationThatCannotReachTheAgentIsNotTaken(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, _, notes := conclusionService(t, false)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	a := askOf(s, todo.ID)
	f.mu.Lock()
	f.failSend = errors.New("node down")
	f.mu.Unlock()
	if ok, err := s.acceptReviewRound(ctx, todo.ID, a.AskedAt.UTC().Format(timeKeyLayout)); ok.taken || err == nil {
		t.Fatalf("taken while the agent cannot be reached: %v %v", ok, err)
	}
	// The web button: 502 with the reason, not 409 "already handled" (the web app toasts it and the button works again).
	asked, _ := json.Marshal(a.AskedAt)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/todos/"+todo.ID+"/accept", strings.NewReader(`{"asked_at":`+string(asked)+`}`)))
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "node down") {
		t.Fatalf("the web button: %d %s", rec.Code, rec.Body.String())
	}
	// The Lark button: an error toast with the reason, saying nothing was taken (not "kept in the todo").
	notTaken := i18n.T(s.botLang(ctx), "assistant-reports.accept_not_taken", i18n.Args{"error": "node down"})
	press := &botcard.CardAction{}
	press.Action.Value = map[string]any{"a": "accept", "id": todo.ID}
	if r := s.larkModule().Action(ctx, press, ""); r == nil || r.Toast == nil ||
		r.Toast.Type != "error" || r.Toast.Content != notTaken {
		t.Fatalf("the Lark button: %+v", r)
	}
	s.acceptReply(ctx, "om_ok", "OK", false, a.MessageID)
	if n := notes(); len(n) == 0 || n[len(n)-1] != notTaken {
		t.Fatalf("the reply is not told it did not go: %q", n)
	}
	if got := askOf(s, todo.ID); !got.AcceptedAt.IsZero() || todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatalf("taken after all: %+v", got)
	}
	f.mu.Lock()
	f.failSend = nil
	f.mu.Unlock()
	if ok, err := s.acceptTry(ctx, todo.ID, ""); !ok.taken || err != nil {
		t.Fatalf("not taken once the agent is reachable: %v %v", ok, err)
	}
	if got := sentToAgent(f); len(got) == 0 || !strings.Contains(got[len(got)-1], "先灰度 10%") {
		t.Fatalf("to the agent: %q", got)
	}
}

// Hold the automatic send's session lookup while the user completes the task.
// The request context marks only that lookup, not completion's publication or outcome reads.
type conclusionLookupKey struct{}
type conclusionLookupHub struct {
	*fakeHub
	entered, release chan struct{}
}

func (h *conclusionLookupHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method == DeviceSessionsList && ctx.Value(conclusionLookupKey{}) != nil {
		close(h.entered)
		select {
		case <-h.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return h.fakeHub.DeviceCall(ctx, p)
}

func TestLateTaskDecisionDoesNotSendAfterCompletion(t *testing.T) {
	for _, action := range []string{"recommendation", "rework"} {
		for _, reopen := range []bool{false, true} {
			name := action + map[bool]string{false: "/done", true: "/reopened"}[reopen]
			t.Run(name, func(t *testing.T) {
				s, f, todo := newSupervisedService(t)
				f.setSessions("box", idleAgent(time.Now().Add(-time.Hour)))
				reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "Choose a rollout", "recommend": "Use staged rollout"})
				a := askOf(s, todo.ID)
				h := &conclusionLookupHub{fakeHub: f, entered: make(chan struct{}), release: make(chan struct{})}
				setHub(s, h)
				var release sync.Once
				defer release.Do(func() { close(h.release) })
				ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), conclusionLookupKey{}, true), 5*time.Second)
				defer cancel()
				done := make(chan error, 1)
				sent := len(sentToAgent(f))
				go func() {
					var err error
					if action == "recommendation" {
						_, err = s.acceptTry(ctx, todo.ID, a.MessageID)
					} else {
						raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "verdict": "rework", "note": "Run one more test"})
						_, err = s.toolTaskReview(ctx, "conv1", raw)
					}
					done <- err
				}()
				select {
				case <-h.entered:
				case <-ctx.Done():
					t.Fatal("automatic send did not reach session lookup")
				}
				e := &todoEnv{t: t, s: s, f: f}
				e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`)
				if reopen {
					e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":false}`)
				}
				release.Do(func() { close(h.release) })
				select {
				case err := <-done:
					if err == nil {
						t.Fatal("stale automatic send was not refused")
					}
				case <-ctx.Done():
					t.Fatal("automatic send did not finish after lookup was released")
				}
				if got := len(sentToAgent(f)); got != sent {
					t.Fatalf("stale %s sent %d messages after completion", action, got-sent)
				}
				if listedAsk(t, s, todo.ID) != nil {
					t.Fatal("the old decision is still pending")
				}
				if !reopen {
					s.taskWorking(context.Background(), "box", "s1", startRow{})
					if got := todoNow(s, todo.ID); got.DoneAt == nil || got.DoneBy != "" {
						t.Fatalf("late supervisor work changed the user's completion: %+v", got)
					}
				}
			})
		}
	}
}

// A task-card message still goes through, but an unknown stop needs a successor, not an implicit resume.
func TestCompletedTaskDirectMessageStillReachesAgent(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	c := idleAgent(time.Now().Add(-time.Hour))
	c.Status, c.PaneID = proto.StatusOffline, ""
	f.setSessions("box", c)
	e := &todoEnv{t: t, s: s, f: f}
	e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`)
	sent := len(sentToAgent(f))
	started := len(startCalls(f))
	if _, err := s.routeToTask(context.Background(), todo.Origin, todo.ID, "Check one more case"); err != nil {
		t.Fatal(err)
	}
	starts := startCalls(f)[started:]
	prompt := ""
	if len(starts) == 1 {
		prompt, _ = starts[0]["prompt"].(string)
	}
	if len(sentToAgent(f)) != sent || len(starts) != 1 || starts[0]["resume_session_id"] != nil ||
		!strings.HasSuffix(prompt, "Check one more case") {
		t.Fatalf("unknown stop was resumed or lost the user's message: %v", starts)
	}
	s.taskWorking(context.Background(), "box", "s1", startRow{})
	if todoNow(s, todo.ID).DoneAt == nil {
		t.Fatal("direct user input reopened a user-completed task")
	}
}

// A recommendation is recorded in the to-do's decisions only once it reached the agent: a send that fails records nothing,
// and the retry that goes through records it once.
func TestRecommendationIsRecordedOnlyOnceItReachedTheAgent(t *testing.T) {
	database := todoTestDB(t)
	reachA, reachB, reachM := agentReachAttempts, agentReachBackoff, agentReachMax
	agentReachAttempts, agentReachBackoff, agentReachMax = 2, time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { agentReachAttempts, agentReachBackoff, agentReachMax = reachA, reachB, reachM })
	f := newFakeHub().withMachine()
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Title: "上线", Cwd: "/code/app", Agent: "claude", Status: proto.StatusIdle})
	s := newKBServiceOn(t, f, database)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "上线方式", "定上线方式", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "s1", "上线", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	decisions := func() []db.TodoDecisionRow {
		list, err := database.ListTodoDecisions(ctx, todo.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	a := acceptAsk{Conclusion: true, Outcome: outcomeDecide, Reason: "先灰度还是直接全量", Recommend: "先灰度 10%"}
	f.mu.Lock()
	f.failSend = errors.New("node down")
	f.mu.Unlock()
	if _, err := s.takeRecommendation(ctx, todo.ID, a); err == nil {
		t.Fatal("no error while the agent cannot be reached")
	}
	if list := decisions(); len(list) != 0 {
		t.Fatalf("recorded though it did not reach the agent: %+v", list)
	}
	f.mu.Lock()
	f.failSend = nil
	f.mu.Unlock()
	if _, err := s.takeRecommendation(ctx, todo.ID, a); err != nil {
		t.Fatal(err)
	}
	if list := decisions(); len(list) != 1 || list[0].Choice != "先灰度 10%" {
		t.Fatalf("decisions after the retry: %+v", list)
	}
}

// The web button's 502 for a recommendation that did not reach the agent says nothing was taken: it used to say the
// choice was kept in the to-do, while no decision was recorded and the ask still waits.
func TestAcceptRouteSaysTheRecommendationWasNotTaken(t *testing.T) {
	database := todoTestDB(t)
	s, f, todo, _, _ := conclusionServiceOn(t, database, false)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	decisions := func() int {
		list, err := database.ListTodoDecisions(ctx, todo.ID, true)
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}
	before := decisions()
	a := askOf(s, todo.ID)
	f.mu.Lock()
	f.failSend = errors.New("node down")
	f.mu.Unlock()
	asked, _ := json.Marshal(a.AskedAt)
	status, body := serveJSON(t, s, "POST", "/api/v1/todos/"+todo.ID+"/accept", "zh-Hans", `{"asked_at":`+string(asked)+`}`)
	want := i18n.T(i18n.ZhHans, "assistant-reports.accept_not_taken", i18n.Args{"error": "node down"})
	if status != http.StatusBadGateway || body["error"] != want {
		t.Fatalf("%d %v, want %q", status, body, want)
	}
	if n := decisions(); n != before {
		t.Fatalf("decisions %d → %d though nothing was taken", before, n)
	}
	if got := askOf(s, todo.ID); !got.AcceptedAt.IsZero() {
		t.Fatalf("taken after all: %+v", got)
	}
}

// The web button's answer says it in the request's language (whatever the bot's): a 502's error, and the reason a
// recorded decision did not reach its Agent (an offline computer, which the hub words itself, #17431), not the
// default language.
func TestAcceptRouteReasonReadsInTheRequestLanguage(t *testing.T) {
	for _, tc := range []struct {
		bot, request string
		want         i18n.Lang
	}{
		{"en", "zh-Hans", i18n.ZhHans},
		{"zh-Hans", "en", i18n.En},
	} {
		for _, offline := range []bool{true, false} {
			s, f, todo, _, _ := conclusionService(t, false)
			f.mu.Lock()
			f.hello.Locale = tc.bot
			f.mu.Unlock()
			waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
			reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
			a := askOf(s, todo.ID)
			f.mu.Lock()
			f.failSend = errors.New("node down")
			if offline {
				f.failSend = i18n.Err("assistant-tasks.route_offline")
			}
			f.mu.Unlock()
			asked, _ := json.Marshal(a.AskedAt)
			status, body := serveJSON(t, s, "POST", "/api/v1/todos/"+todo.ID+"/accept", tc.request, `{"asked_at":`+string(asked)+`}`)
			if offline {
				if want := i18n.T(tc.want, "assistant-tasks.route_offline"); status != http.StatusOK || body["delivered"] != "none" || body["reason"] != want {
					t.Fatalf("bot %s, request %s: %d %v, want reason %q", tc.bot, tc.request, status, body, want)
				}
				continue
			}
			if want := i18n.T(tc.want, "assistant-reports.accept_not_taken", i18n.Args{"error": "node down"}); status != http.StatusBadGateway || body["error"] != want {
				t.Fatalf("bot %s, request %s: %d %v, want %q", tc.bot, tc.request, status, body, want)
			}
		}
	}
}

// A decision answered in words on Lark is made even when its agent cannot take it now: recorded, out of needs_user,
// its ask answered, and its supervisor told what to pass on. The hub does not resend it, so a later reply to the same
// card finds it answered and nothing reaches the agent twice.
func TestDecisionAnsweredInWordsStaysDecidedWhenTheSendFails(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, _, notes := conclusionService(t, false)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	a := askOf(s, todo.ID)
	f.mu.Lock()
	f.failSend = errors.New("a dialog is open")
	f.mu.Unlock()
	sent := len(sentToAgent(f))
	if !s.acceptReply(ctx, "om_words", "直接全量吧", false, a.MessageID) {
		t.Fatal("reply not taken")
	}
	if got := todoNow(s, todo.ID).Supervision.State; got == reviewNeedsUser {
		t.Fatalf("still waits on the owner: %q", got)
	}
	if got := askOf(s, todo.ID); got.AcceptedAt.IsZero() {
		t.Fatalf("ask not answered: %+v", got)
	}
	want := i18n.T(s.botLang(ctx), "assistant-reports.accept_decided_not_sent", i18n.Args{"error": "a dialog is open"})
	if n := notes(); len(n) == 0 || n[len(n)-1] != want {
		t.Fatalf("reply note %q", n)
	}
	key := "undelivered|" + a.AskedAt.UTC().Format(time.RFC3339Nano)
	// digestMu: a digest that took the signal out of pending has marked it sent by the time it lets go (#17838).
	s.supervise.digestMu.Lock()
	s.supervise.mu.Lock()
	g, pending := s.supervise.pending[todo.ID+"|"+sigIdle]
	told := s.supervise.sent[todo.ID+"|"+sigIdle] == key
	s.supervise.mu.Unlock()
	s.supervise.digestMu.Unlock()
	if !(pending && g.Key == key && strings.Contains(g.Detail, "直接全量") && strings.Contains(g.Detail, "a dialog is open")) && !told {
		t.Fatalf("supervisor not told: %+v (told %v)", g, told)
	}
	f.mu.Lock()
	f.failSend = nil
	f.mu.Unlock()
	s.acceptReply(ctx, "om_again", "直接全量吧", false, a.MessageID)
	if n := len(sentToAgent(f)); n != sent {
		t.Fatalf("%d sends after the failed one: the hub resent it", n-sent)
	}
}

func TestFeishuConclusionQuestionGoesToSupervisorWithoutAnswering(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "Two options: start with ten percent or defer the rollout.")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "Which rollout do you want?", "recommend": "Start with ten percent"})
	a := askOf(s, todo.ID)
	before := len(sentToAgent(f))
	var ev botcard.MessageEvent
	ev.Message.ChatType, ev.Message.ParentID, ev.Message.MessageType = "p2p", a.MessageID, "text"
	ev.Message.MessageID, ev.Message.Content = "om_background", `{"text":"能补充一下这个决定的背景吗"}`
	feishuSay(t, s, f, nil, ev, "")
	task := todoNow(s, todo.ID)
	if got := askOf(s, todo.ID); got == nil || !got.AcceptedAt.IsZero() || task.Supervision.State != reviewNeedsUser ||
		task.DoneAt != nil || len(sentToAgent(f)) != before {
		t.Fatalf("the question changed the decision or sent to the Agent: ask=%+v task=%+v sends=%v", got, task.Supervision, sentToAgent(f))
	}
	text := sendOnceText(f)
	if !strings.Contains(text, "能补充一下这个决定的背景吗") || !strings.Contains(text, todo.ID) {
		t.Fatalf("question and task context did not reach supervisor: %q", text)
	}
	// The existing option still answers this exact card after the clarification.
	ev.Message.MessageID, ev.Message.Content = "om_choice", `{"text":"B"}`
	feishuSay(t, s, f, nil, ev, "")
	if got := askOf(s, todo.ID); got == nil || got.AcceptedAt.IsZero() {
		t.Fatalf("explicit option did not answer: %+v", got)
	}
	sends := sentToAgent(f)[before:]
	if len(sends) != 1 || !strings.Contains(sends[0], "先不动") {
		t.Fatalf("agent got %v, want just the selected option", sends)
	}
}

// Verification asks (!a.Conclusion) reflect the to-do's effective delivery state across locales and every surface
// (Lark card, GET /todos ask, briefing feed, briefing card, snapshot), while preserving empty outcome (#14539).
func TestConclusionVerificationCopyFollowsDeliveryState(t *testing.T) {
	verifyAsk := &acceptAsk{}
	for _, tc := range []struct {
		name     string
		lang     i18n.Lang
		delivery *TodoDelivery
		want     string
	}{
		{"zh live", i18n.ZhHans, &TodoDelivery{State: deliveryLive}, "✅ 做完了：已部署，按下面验收"},
		{"zh pr", i18n.ZhHans, &TodoDelivery{State: deliveryPR}, "✅ 做完了：停在 PR，未部署，按下面验收"},
		{"zh merged", i18n.ZhHans, &TodoDelivery{State: deliveryMerged}, "✅ 做完了：已合未部署，按下面验收"},
		{"zh off", i18n.ZhHans, &TodoDelivery{State: deliveryOff, Gate: "Feature disabled by default"}, "✅ 做完了：已部署但默认关着，按下面验收"},
		{"zh none", i18n.ZhHans, &TodoDelivery{State: deliveryNone}, "✅ 做完了：请验收结果"},
		{"zh nil", i18n.ZhHans, nil, "✅ 做完了：请验收结果"},
		{"en live", i18n.En, &TodoDelivery{State: deliveryLive}, "✅ Done: deployed, check it below"},
		{"en pr", i18n.En, &TodoDelivery{State: deliveryPR}, "✅ Done: PR open, not deployed yet; check it below"},
		{"en merged", i18n.En, &TodoDelivery{State: deliveryMerged}, "✅ Done: merged, not deployed yet; check it below"},
		{"en off", i18n.En, &TodoDelivery{State: deliveryOff, Gate: "Feature disabled by default"}, "✅ Done: deployed, off by default; check it below"},
		{"en none", i18n.En, &TodoDelivery{State: deliveryNone}, "✅ Done: please verify the result below"},
		{"en nil", i18n.En, nil, "✅ Done: please verify the result below"},
		{"ja live", i18n.Ja, &TodoDelivery{State: deliveryLive}, "✅ 完了：デプロイ済み。下の手順で確認してください"},
		{"ja pr", i18n.Ja, &TodoDelivery{State: deliveryPR}, "✅ 完了：PR で停止中・未デプロイ。下の手順で確認してください"},
		{"ja merged", i18n.Ja, &TodoDelivery{State: deliveryMerged}, "✅ 完了：マージ済み・未デプロイ。下の手順で確認してください"},
		{"ja off", i18n.Ja, &TodoDelivery{State: deliveryOff, Gate: "Feature disabled by default"}, "✅ 完了：デプロイ済み・既定でオフ。下の手順で確認してください"},
		{"ja none", i18n.Ja, &TodoDelivery{State: deliveryNone}, "✅ 完了：結果を確認してください"},
		{"ja nil", i18n.Ja, nil, "✅ 完了：結果を確認してください"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := verdictText(tc.lang, verifyAsk, tc.delivery); got != tc.want {
				t.Fatalf("verdictText(%q, %+v) = %q, want %q", tc.lang, tc.delivery, got, tc.want)
			}
		})
	}
	// A conclusion keeps its own reason regardless of delivery state.
	concl := &acceptAsk{Conclusion: true, Outcome: outcomeDone, Reason: "已完成方案"}
	if got := verdictText(i18n.ZhHans, concl, &TodoDelivery{State: deliveryMerged}); got != "✅ 做完了：已完成方案" {
		t.Fatalf("conclusion verdictText = %q", got)
	}

	// End-to-end: accepted + user_check with delivery.state=merged under default delivery_gate=merged must never
	// claim "已部署，按下面验收" on any surface, and must keep Outcome == "".
	needsLarkLayer(t)
	s, f, todo, posts, _ := conclusionService(t, false)
	ctx := t.Context()
	s.setTaskDelivery(ctx, todo.ID, TodoDelivery{State: deliveryMerged, Gate: "等待部署", Unblock: unblockOther, At: time.Now().UTC()})
	waitsForReview(t, s, f, todo.ID, "PR 已合并，等待部署。")
	reviewTask(t, s, todo.ID, map[string]any{
		"verdict":    "accepted",
		"user_check": true,
		"note":       "合并完成，请验收",
		"checks":     completeReviewChecks(todoNow(s, todo.ID)),
	})
	waitFor(t, "user_check accept card", func() bool { a := askOf(s, todo.ID); return a != nil && a.MessageID != "" })

	const wantVerdict = "✅ 做完了：已合未部署，按下面验收"
	const forbidden = "已部署，按下面验收"

	// 1. Lark accept card
	card := acceptCardOf(posts())
	if !strings.Contains(card, wantVerdict) || strings.Contains(card, forbidden) {
		t.Fatalf("accept card verdict mismatch:\n%s", card)
	}

	// 2. todoAsks (GET /api/v1/todos) preserves empty Outcome and AskedAt
	asks := s.todoAsks(ctx)
	gotAsk, ok := asks[todo.ID]
	if !ok {
		t.Fatalf("todoAsks missing %s", todo.ID)
	}
	if gotAsk.Verdict != wantVerdict || gotAsk.Outcome != "" || gotAsk.AskedAt.IsZero() {
		t.Fatalf("todoAsks[%s] = %+v, want verdict=%q outcome=\"\" non-zero asked_at", todo.ID, gotAsk, wantVerdict)
	}

	// 3. Briefing feed and briefing card
	t.Run("briefing", func(t *testing.T) {
		needsBriefingModule(t)
		facts := s.briefFactsAt(ctx, time.Now(), "")
		body := s.briefBodyOf(facts, "早报")
		var row *briefBodyRow
		for i := range body.Accept {
			if body.Accept[i].ID == todo.ID {
				row = &body.Accept[i]
			}
		}
		if row == nil || row.Verdict != wantVerdict || row.Outcome != "" {
			t.Fatalf("brief body row = %+v, want verdict=%q outcome=\"\"", row, wantVerdict)
		}
		briefCard, _, _ := s.briefCard(facts, "早报", "")
		if js := briefCard.JSON(); !strings.Contains(js, botcard.EscapeMarkdown(wantVerdict)) || strings.Contains(js, forbidden) {
			t.Fatalf("brief card verdict mismatch:\n%s", js)
		}
	})

	// 4. Snapshot
	snap, total := s.snapshotUnaccepted(ctx, s.location(ctx))
	if total != 1 || len(snap) != 1 || snap[0].Verdict != wantVerdict {
		t.Fatalf("snapshotUnaccepted = (%+v, %d), want verdict=%q", snap, total, wantVerdict)
	}

	// 5. Effective delivery (currentDelivery): a recorded pr hold promoted to merged by polled PR facts uses merged copy.
	prURL := "https://github.com/o/r/pull/42"
	now := time.Now().UTC()
	s.setTaskStage(ctx, todo.ID, "review", prURL)
	s.setTaskDelivery(ctx, todo.ID, TodoDelivery{State: deliveryPR, Gate: "等待合入", Unblock: unblockOther, At: now.Add(-time.Minute)})
	if err := s.applyPRStatus(ctx, todoNow(s, todo.ID), TodoPRStatus{URL: prURL, State: "merged", CheckedAt: now}); err != nil {
		t.Fatal(err)
	}
	if got := s.todoAsks(ctx)[todo.ID].Verdict; got != wantVerdict {
		t.Fatalf("currentDelivery-promoted verdict = %q, want %q", got, wantVerdict)
	}
}

// askConclusion preserves a needs_user decision's Reason up to readDecision's 200-rune limit instead of re-clipping it
// to 120 runes (#14659), while still capping values beyond 200 runes at 200.
func TestAskConclusionPreservesDecisionReasonUpTo200Runes(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "MR is ready and waiting on reviewer assignment.")
	reason180 := strings.Repeat("a", 180)
	reviewTask(t, s, todo.ID, map[string]any{
		"verdict":   "needs_user",
		"reason":    reason180,
		"recommend": "Assign reviewers now",
	})
	a := askOf(s, todo.ID)
	if a == nil || a.Reason != reason180 {
		t.Fatalf("decision reason clipped early: len=%d got=%q", utf8.RuneCountInString(a.Reason), a.Reason)
	}
	ask := listedAsk(t, s, todo.ID)
	if ask == nil || ask["question"] != reason180 {
		t.Fatalf("listed ask.question = %v, want 180-rune reason", ask["question"])
	}

	reason240 := strings.Repeat("b", 240)
	reviewTask(t, s, todo.ID, map[string]any{
		"verdict":   "needs_user",
		"reason":    reason240,
		"recommend": "Assign reviewers now",
	})
	a = askOf(s, todo.ID)
	wantClipped := clip(reason240, decisionReasonMaxRunes)
	if a == nil || a.Reason != wantClipped {
		t.Fatalf("over-limit decision reason = %q (len=%d), want %q (len=%d)", a.Reason, utf8.RuneCountInString(a.Reason), wantClipped, utf8.RuneCountInString(wantClipped))
	}
}

// conclusionLink follows the documented fallback order: explicit link -> stage PR / PRURL -> canonical saved
// repository delivery PR/MR (skipping reference links) -> note/checks URL -> agent session -> to-do page (#14659).
func TestConclusionLinkFallbackOrderIncludesRepositoryDeliveryPRs(t *testing.T) {
	const (
		explicitLink = "https://docs.example.com/decision/1"
		stagePR      = "https://github.com/example/app/pull/42"
		repoMR       = "https://gitlab.example.com/team/app/-/merge_requests/1468"
		refPR        = "https://github.com/example/upstream/pull/99"
		noteURL      = "https://docs.example.com/from-note"
	)
	st := &fakeTodoRepoStore{
		planMemStore: &planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}},
		repos:        map[string][]repository.TaskRepository{},
	}
	f := newFakeHub().withMachine()
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_me", PublicURL: "https://hub.example.com"}, nil
	})
	s := newServiceOn(t, f, st)
	ctx := t.Context()
	todo, err := s.createTodo(ctx, "Review MR", "Review delivery MR", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "s1", "Review MR", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}

	// Reference-only repo links do not override a URL in note/checks.
	st.repos[todo.ID] = []repository.TaskRepository{{
		RepoID: "repo_1",
		Links: []repository.TaskLink{
			{Kind: "issue", Number: "10", CanonicalURL: "https://github.com/example/app/issues/10", Role: repository.LinkRoleDelivery},
			{Kind: "pull_request", Number: "99", CanonicalURL: refPR, Role: repository.LinkRoleReference},
		},
	}}
	if got := s.conclusionLink(ctx, todoNow(s, todo.ID), conclusionInput{Note: "See " + noteURL + "."}); got != noteURL {
		t.Fatalf("reference PR should be skipped in favor of note URL: got %q, want %q", got, noteURL)
	}

	// Saved canonical repository delivery MR wins over note/checks URL and agent session link.
	st.repos[todo.ID] = []repository.TaskRepository{{
		RepoID: "repo_1",
		Links: []repository.TaskLink{
			{Kind: "pull_request", Number: "99", CanonicalURL: refPR, Role: repository.LinkRoleReference},
			{Kind: "pull_request", Number: "1468", CanonicalURL: repoMR, Role: repository.LinkRoleDelivery},
		},
	}}
	if got := s.conclusionLink(ctx, todoNow(s, todo.ID), conclusionInput{Note: "See " + noteURL}); got != repoMR {
		t.Fatalf("delivery repository MR fallback: got %q, want %q", got, repoMR)
	}

	// Empty role defaults to delivery (legacy saved links).
	st.repos[todo.ID][0].Links[1].Role = ""
	if got := s.conclusionLink(ctx, todoNow(s, todo.ID), conclusionInput{Note: "See " + noteURL}); got != repoMR {
		t.Fatalf("empty-role delivery repository MR fallback: got %q, want %q", got, repoMR)
	}

	// Stage PR takes precedence over repository delivery MR.
	s.setTaskStage(ctx, todo.ID, "review", stagePR)
	if got := s.conclusionLink(ctx, todoNow(s, todo.ID), conclusionInput{Note: "See " + noteURL}); got != stagePR {
		t.Fatalf("stage PR precedence: got %q, want %q", got, stagePR)
	}

	// Explicit in.Link takes precedence over stage PR.
	if got := s.conclusionLink(ctx, todoNow(s, todo.ID), conclusionInput{Link: explicitLink, Note: "See " + noteURL}); got != explicitLink {
		t.Fatalf("explicit link precedence: got %q, want %q", got, explicitLink)
	}

	// Invalid explicit link (non-http scheme) is ignored and falls back cleanly.
	if got := s.conclusionLink(ctx, todoNow(s, todo.ID), conclusionInput{Link: "javascript:alert(1)"}); got != stagePR {
		t.Fatalf("invalid explicit link fallback: got %q, want %q", got, stagePR)
	}

	// With no PR, repo delivery link, or note URL: falls back to agent session link, then to-do page when no agent.
	plainTodo, err := s.createTodo(ctx, "Plain task", "No PR yet", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, plainTodo.ID, "box", "s1", "Plain task", "/code/app", "claude"); err != nil {
		t.Fatal(err)
	}
	cur := todoNow(s, plainTodo.ID)
	if got := s.conclusionLink(ctx, cur, conclusionInput{}); got != "https://hub.example.com/s/box/s1" {
		t.Fatalf("agent session fallback: got %q", got)
	}
	cur.Agents = nil
	if got := s.conclusionLink(ctx, cur, conclusionInput{}); got != "https://hub.example.com/todos/"+plainTodo.ID {
		t.Fatalf("todo page fallback: got %q", got)
	}
}
