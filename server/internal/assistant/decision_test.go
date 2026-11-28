package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Decisions the owner can answer (#3804, decision.go). All names, ids and texts are placeholders.

// rolloutDecision is a complete needs_user review: a call on priority, which can be undone.
func rolloutDecision() map[string]any {
	return map[string]any{"verdict": "needs_user", "why_owner": "priority", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%",
		"options": []map[string]string{
			{"label": "先灰度 10%", "leads_to": "今天放 10%，明天看数据再全量"},
			{"label": "直接全量", "leads_to": "今天全部用户都用上，出问题要整体回滚"},
		},
		"cost_of_waiting": "新版本每天晚一天上线"}
}

// dropTableDecision is a complete needs_user review of a step that cannot be undone.
func dropTableDecision() map[string]any {
	return map[string]any{"verdict": "needs_user", "why_owner": "irreversible", "reason": "删不删旧表", "recommend": "删掉旧表",
		"options": []map[string]string{
			{"label": "删掉旧表", "leads_to": "旧数据不能恢复，释放 40 GB"},
			{"label": "先留着", "leads_to": "继续占空间，迁移算没收尾"},
			{"label": "先导出再删", "leads_to": "多花半天，数据留一份备份"},
		},
		"cost_of_waiting": "磁盘两周后写满"}
}

func en() i18n.Lang { return i18n.Lang("en") }

// reviewErr is task_review with args as given (nothing filled in), and its error.
func reviewErr(s *Service, todo string, args map[string]any) (string, error) {
	args["todo_id"] = todo
	raw, _ := json.Marshal(args)
	return s.runTool(context.Background(), "server", "conv1", "task_review", raw)
}

// answerAsk posts body to the accept route of to-do id for the round that waits now.
func answerAsk(s *Service, id string, body map[string]string) *httptest.ResponseRecorder {
	if a := askOf(s, id); a != nil {
		body["asked_at"] = a.AskedAt.Format(timeKeyLayout)
	}
	raw, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/todos/"+id+"/accept", strings.NewReader(string(raw))))
	return rec
}

// signalled says the supervisor was or is about to be told signal kind of task id: it waits to go out, or a
// supervision message with text went.
func signalled(s *Service, f *fakeHub, id, kind, text string) bool {
	b := &s.supervise
	b.mu.Lock()
	_, waiting := b.pending[id+"|"+kind]
	b.mu.Unlock()
	return waiting || slices.ContainsFunc(f.notices(), func(n string) bool { return strings.Contains(n, text) })
}

// forgetSignals has the supervision book forget what waits and what it told, as a restart of the assistant does, once
// what waited went out: what is told "once" must not come again from the state that is kept.
func forgetSignals(t *testing.T, s *Service) {
	t.Helper()
	b := &s.supervise
	waitFor(t, "the supervision message", func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.pending) == 0
	})
	b.mu.Lock()
	b.sent = map[string]string{}
	b.mu.Unlock()
}

func pendingKinds(s *Service, id string) (out []string) {
	b := &s.supervise
	b.mu.Lock()
	defer b.mu.Unlock()
	for k := range b.pending {
		if strings.HasPrefix(k, id+"|") {
			out = append(out, strings.TrimPrefix(k, id+"|"))
		}
	}
	slices.Sort(out)
	return out
}

// needs_user is refused without each part of a decision: the refusal names what is missing and what to do when nothing
// needs the owner, and the task is left as it was.
func TestNeedsUserIsRefusedWithoutEachPartOfADecision(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	one := []map[string]string{{"label": "先灰度 10%", "leads_to": "今天放 10%"}}
	five := []map[string]string{{"label": "先灰度 10%", "leads_to": "a"}, {"label": "b", "leads_to": "b"}, {"label": "c", "leads_to": "c"},
		{"label": "d", "leads_to": "d"}, {"label": "e", "leads_to": "e"}}
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"no why_owner", func(a map[string]any) { delete(a, "why_owner") }, "why_owner"},
		{"a why_owner that is none of them", func(a map[string]any) { a["why_owner"] = "blocked" }, "why_owner"},
		{"no reason", func(a map[string]any) { delete(a, "reason") }, "reason (one sentence"},
		{"the matter only in note", func(a map[string]any) { delete(a, "reason"); a["note"] = "要你选方案" }, "reason (one sentence"},
		{"no recommend", func(a map[string]any) { delete(a, "recommend") }, "recommend (the option"},
		{"no options", func(a map[string]any) { delete(a, "options") }, "options (2 to 4"},
		{"one option", func(a map[string]any) { a["options"] = one }, "options (2 to 4"},
		{"five options", func(a map[string]any) { a["options"] = five }, "options (2 to 4"},
		{"an option that leads nowhere", func(a map[string]any) {
			a["options"] = []map[string]string{{"label": "先灰度 10%", "leads_to": "今天放 10%"}, {"label": "直接全量"}}
		}, "leads_to"},
		{"a recommendation that is no option", func(a map[string]any) { a["recommend"] = "再等等" }, "recommend must be the label"},
		{"no cost_of_waiting", func(a map[string]any) { delete(a, "cost_of_waiting") }, "cost_of_waiting"},
	} {
		args := rolloutDecision()
		tc.edit(args)
		_, err := reviewErr(s, todo.ID, args)
		if err == nil || !strings.Contains(err.Error(), "needs_user is refused") || !strings.Contains(err.Error(), tc.want) ||
			!strings.Contains(err.Error(), "decide it yourself") || !strings.Contains(err.Error(), "send the work back") ||
			!strings.Contains(err.Error(), "close it") {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	// Everything missing is named at once, so one retry is enough.
	_, err := reviewErr(s, todo.ID, map[string]any{"verdict": "needs_user", "note": "等用户恢复工具权限"})
	for _, want := range []string{"why_owner", "reason (", "recommend (", "options (", "cost_of_waiting ("} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("a bare needs_user: %v, want %q named", err, want)
		}
	}
	if v := todoNow(s, todo.ID).Supervision; v.State != reviewPending || len(v.Reviews) != 0 || askOf(s, todo.ID) != nil {
		t.Fatalf("a refused review changed the task: %+v, ask %+v", v, askOf(s, todo.ID))
	}
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatalf("a complete decision: %v", err)
	}
	// The ledger keeps what was asked (before #3804 a review kept only its note).
	v := todoNow(s, todo.ID).Supervision
	if r := v.Reviews[len(v.Reviews)-1]; v.State != reviewNeedsUser || r.WhyOwner != "priority" || r.Reason != "先灰度还是直接全量" ||
		r.Recommend != "先灰度 10%" || len(r.Options) != 2 || r.CostOfWaiting == "" {
		t.Fatalf("review %+v", r)
	}
}

// With no Lark bot, a needs_user review leaves its decision on GET /api/v1/todos with everything that answers it: the
// question, the options with what each leads to, why the owner, the cost of waiting, the background the hub filled in,
// the default and its time, and the fixed answers.
func TestDecisionIsOnTheTodoWithoutTheBot(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	if _, err := s.appendFeed(ctx, "conv1", feedRouted, todo.ID, map[string]string{"text": "登录页横屏崩了，先修这个"}); err != nil {
		t.Fatal(err)
	}
	s.putWork(ctx, db.SessionWork{MachineID: "box", SessionID: "s1", Work: proto.WorkState{Line: "等灰度方案",
		Closeout: &proto.Closeout{Status: proto.CloseoutBlocked, Did: []string{"修复已合并，预发验证通过"}}}})
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	ask := listedAsk(t, s, todo.ID)
	if ask == nil {
		t.Fatal("no ask on the to-do after a needs_user review with no Lark bot")
	}
	if ask["outcome"] != "decide" || ask["question"] != "先灰度还是直接全量" || ask["recommend"] != "先灰度 10%" ||
		ask["why_owner"] != "priority" || ask["cost_of_waiting"] != "新版本每天晚一天上线" || ask["default"] != "taken" {
		t.Fatalf("ask %v", ask)
	}
	opts, _ := ask["options"].([]any)
	if first, _ := opts[0].(map[string]any); len(opts) != 2 || first["label"] != "先灰度 10%" || first["leads_to"] != "今天放 10%，明天看数据再全量" {
		t.Fatalf("options %v", opts)
	}
	if got, _ := json.Marshal(ask["responses"]); string(got) != `["decide_yourself","not_what_i_asked","explain_first"]` {
		t.Fatalf("responses %s", got)
	}
	at, _ := ask["deadline"].(string)
	if d, err := time.Parse(time.RFC3339Nano, at); err != nil || time.Until(d) < 23*time.Hour || time.Until(d) > 24*time.Hour {
		t.Fatalf("deadline %q: a decision that can be undone gets a day", at)
	}
	from, _ := ask["from"].(map[string]any)
	if from["conversation"] != "conv1" || from["main"] != true || from["message"] != "登录页横屏崩了，先修这个" || from["by_assistant"] != nil {
		t.Fatalf("from %v", from)
	}
	if ask["stands"] != "修复已合并，预发验证通过" {
		t.Fatalf("stands %v: the closeout comes before the work line", ask["stands"])
	}
	bg, _ := ask["background"].(string)
	if !strings.Contains(bg, "主对话") || !strings.Contains(bg, todo.CreatedAt.In(s.location(ctx)).Format("2006-01-02")) ||
		!strings.Contains(bg, "登录页横屏崩了，先修这个") || !strings.Contains(bg, "修复已合并，预发验证通过") {
		t.Fatalf("background %q", bg)
	}
	// The push says the same, so it stands on its own.
	pushes := decidePushes(t, f)
	if len(pushes) != 1 {
		t.Fatalf("pushes %v", pushes)
	}
	body, _ := pushes[0]["body"].(string)
	for _, want := range []string{"先灰度还是直接全量", "登录页横屏崩了，先修这个", "1. 先灰度 10%：今天放 10%，明天看数据再全量", "2. 直接全量", "新版本每天晚一天上线"} {
		if !strings.Contains(body, want) {
			t.Errorf("push body lacks %q:\n%s", want, body)
		}
	}
}

// A to-do that records no message of the owner's is told as started by the assistant on its date, not guessed; with
// no closeout the work line says where it stands, and with neither nothing does.
func TestDecisionBackgroundDoesNotGuess(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "两种走法")
	from, stands := s.decisionBackground(ctx, todoNow(s, todo.ID))
	if !from.ByAssistant || from.Message != "" || !from.At.Equal(todo.CreatedAt) || stands != "" {
		t.Fatalf("from %+v, stands %q", from, stands)
	}
	s.putWork(ctx, db.SessionWork{MachineID: "box", SessionID: "s1", Work: proto.WorkState{Line: "等灰度方案"}})
	if _, stands := s.decisionBackground(ctx, todoNow(s, todo.ID)); stands != "等灰度方案" {
		t.Fatalf("stands %q", stands)
	}
	// What Persona wrote to the agent, an answer the hub worded for the owner, and anything routed after the task's
	// first review are not the owner's message that started it.
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	if rec := answerAsk(s, todo.ID, map[string]string{"option": "直接全量"}); rec.Code != http.StatusOK {
		t.Fatalf("answer: %d %s", rec.Code, rec.Body.String())
	}
	if a := askOf(s, todo.ID); a.Chosen != "直接全量" || !strings.Contains(acceptedNote(en(), a), "直接全量") {
		t.Fatalf("the option chosen is not kept on the ask: %+v", a)
	}
	for _, body := range []map[string]string{{"text": "按 A 做", "from": routedFromAssistant}, {"text": "用户选了", "relay": routedRelay}, {"text": "后来补的一句"}} {
		if _, err := s.appendFeed(ctx, "conv1", feedRouted, todo.ID, body); err != nil {
			t.Fatal(err)
		}
	}
	if from, _ := s.decisionBackground(ctx, todoNow(s, todo.ID)); !from.ByAssistant || from.Message != "" {
		t.Fatalf("a message that is not the owner's first was taken for it: %+v", from)
	}
	at := time.Date(2026, 11, 7, 9, 30, 0, 0, time.UTC)
	en := en()
	for _, tc := range []struct {
		ask  acceptAsk
		want string
	}{
		{acceptAsk{From: &askFrom{Main: true, At: at, ByAssistant: true}}, "Started by the assistant on 2026-11-07, in the main conversation"},
		{acceptAsk{From: &askFrom{Title: "Billing", At: at, Message: "Fix the invoice total"}, Stands: "PR open, CI green"},
			"From the conversation “Billing”, 2026-11-07: “Fix the invoice total” · Now: PR open, CI green"},
		{acceptAsk{}, ""},
	} {
		if got := backgroundText(en, time.UTC, &tc.ask); got != tc.want {
			t.Errorf("background %q, want %q", got, tc.want)
		}
	}
}

// A task with a brief (#3822) comes from what its brief froze at dispatch: the owner's message and its time, or who
// started the work when it was not them; what was routed to the task later does not change that.
func TestDecisionBackgroundReadsTheBrief(t *testing.T) {
	asked := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		rec         briefRecord
		message, by string
		assistant   bool
	}{
		{briefRecord{StartedBy: briefByOwner, Goal: "修登录崩溃", Request: &BriefRequest{Text: "iPad 横屏登录会崩，\n修一下", At: asked}}, "iPad 横屏登录会崩， 修一下", "", false},
		{briefRecord{StartedBy: briefByRoutine, Goal: "修登录崩溃", Reason: "晨间巡检发现崩溃"}, "", briefByRoutine, true},
	} {
		s, _, todo := newSupervisedService(t)
		ctx := context.Background()
		// The brief the task was handed out with, as if that turn had followed this message or this routine.
		s.tbriefs.mu.Lock()
		tb := s.tbriefs.briefs[s.tbriefs.links[todo.ID].Brief]
		if tb == nil {
			s.tbriefs.mu.Unlock()
			t.Fatal("the task has no brief")
		}
		tb.rec.StartedBy, tb.rec.Request, tb.rec.Reason = tc.rec.StartedBy, tc.rec.Request, tc.rec.Reason
		s.tbriefs.mu.Unlock()
		if _, err := s.appendFeed(ctx, "conv1", feedRouted, todo.ID, map[string]string{"text": "顺手把图标也换了"}); err != nil {
			t.Fatal(err)
		}
		from, _ := s.decisionBackground(ctx, todoNow(s, todo.ID))
		if from.Message != tc.message || from.StartedBy != tc.by || from.ByAssistant != tc.assistant ||
			tc.message != "" && !from.At.Equal(asked) {
			t.Fatalf("%s: from %+v", tc.rec.StartedBy, from)
		}
	}
}

// The live bug of #3804: a task waited for the owner (needs_user) and its to-do carried no ask. A decision was the
// task's ask until anything asked the owner to verify the same task: a turn of its agent that said "deployed", or a
// held deploy landing. That round replaced the decision and, the task being no big feature, was passed at once by the
// supervising assistant, so nothing waited any more while the review still said needs_user.
func TestDeployedTurnDoesNotReplaceADecision(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "合并了，预发验证通过；生产发布等批准。")
	if _, err := reviewErr(s, todo.ID, dropTableDecision()); err != nil {
		t.Fatal(err)
	}
	asked := askOf(s, todo.ID).AskedAt
	// Its agent says once more that the work is deployed (a wake-up, an answer to the supervisor).
	s.askAcceptance(todoNow(s, todo.ID), "box", "s1", "已经部署到预发并验证通过，PR https://github.com/o/r/pull/7 已合并")
	a := askOf(s, todo.ID)
	if a == nil || !decideAsk(a) || !a.AcceptedAt.IsZero() || !a.AskedAt.Equal(asked) || a.ByAssistant {
		t.Fatalf("the decision was replaced: %+v", a)
	}
	if ask := listedAsk(t, s, todo.ID); ask == nil || ask["outcome"] != "decide" {
		t.Fatalf("GET /api/v1/todos lost the decision: %v", ask)
	}
	// Once it is answered, the next deployed turn asks as before.
	if rec := answerAsk(s, todo.ID, map[string]string{"option": "先留着"}); rec.Code != http.StatusOK {
		t.Fatalf("answer: %d %s", rec.Code, rec.Body.String())
	}
	s.askAcceptance(todoNow(s, todo.ID), "box", "s1", "已经部署，PR https://github.com/o/r/pull/7 已合并")
	if a := askOf(s, todo.ID); a == nil || a.Conclusion || !a.ByAssistant {
		t.Fatalf("no \"Please verify\" round after the decision was answered: %+v", a)
	}
}

// The other order: the review comes while a "Please verify" for the same task is still being written (its lines come
// from a model call). The decision is kept all the same, and the "Please verify" gives way when it arrives, whether the
// owner or the assistant was to pass it; it never accepts the decision.
func TestDecisionAskedWhileAPleaseVerifyIsWritten(t *testing.T) {
	for _, owner := range []bool{false, true} {
		s, f, todo := newSupervisedService(t)
		ctx := context.Background()
		waitsForReview(t, s, f, todo.ID, "合并了，预发验证通过；生产发布等批准。")
		s.accept.mu.Lock()
		if !s.loadAcceptLocked(ctx) {
			t.Fatal("asks not loaded")
		}
		s.accept.asks[todo.ID] = &acceptAsk{} // askAcceptanceRound's claim, its model call still running
		s.accept.mu.Unlock()
		if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
			t.Fatal(err)
		}
		if ask := listedAsk(t, s, todo.ID); ask == nil || ask["outcome"] != "decide" {
			t.Fatalf("owner=%v: the decision was not kept while a \"Please verify\" was in flight: %v", owner, ask)
		}
		sent := len(sentToAgent(f))
		verify := acceptAsk{AskedAt: time.Now().UTC(), Changed: "登录页", How: "横屏打开登录页", MachineID: "box", SessionID: "s1"}
		if owner {
			if s.postAsk(ctx, todoNow(s, todo.ID), verify) {
				t.Fatal("a card went out for the round that gave way")
			}
		} else {
			s.assistantAccepts(ctx, todoNow(s, todo.ID), verify)
		}
		a := askOf(s, todo.ID)
		if a == nil || !decideAsk(a) || !a.AcceptedAt.IsZero() || len(sentToAgent(f)) != sent {
			t.Fatalf("owner=%v: the decision gave way or was taken: %+v, to the agent %q", owner, a, sentToAgent(f)[sent:])
		}
	}
}

// A session list that keeps failing leaves the task's Agents unknown: the owner's choice is still made (#17431), recorded
// and handed to the supervisor once, never sent again by the hub, and the owner is told it did not reach the Agent.
func TestSessionListFailureRecordsDecisionForSupervisor(t *testing.T) {
	reachA, reachB, reachM := agentReachAttempts, agentReachBackoff, agentReachMax
	agentReachAttempts, agentReachBackoff, agentReachMax = 2, time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { agentReachAttempts, agentReachBackoff, agentReachMax = reachA, reachB, reachM })
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	calls := f.sessionsListCalls("box")
	f.sessionsFail["box"] = 1000
	sent := len(sentToAgent(f))
	key := "undelivered|" + askOf(s, todo.ID).AskedAt.UTC().Format(time.RFC3339Nano)
	result, err := s.chooseOption(context.Background(), todo.ID, "", "直接全量")
	var routeErr *i18n.Message
	if err != nil || !result.taken || !errors.As(result.undelivered, &routeErr) || routeErr.Key != "assistant-tasks.route_offline" {
		t.Fatalf("a failed session list did not record the decision: result=%+v err=%v", result, err)
	}
	if f.sessionsListCalls("box")-calls != agentReachAttempts+1 {
		t.Fatalf("session list calls = %d, want initial lookup plus %d retries", f.sessionsListCalls("box")-calls, agentReachAttempts)
	}
	s.supervise.dedupMu.Lock()
	s.supervise.mu.Lock()
	g, pending := s.supervise.pending[todo.ID+"|"+sigIdle]
	told := s.supervise.sent[todo.ID+"|"+sigIdle] == key
	s.supervise.mu.Unlock()
	s.supervise.dedupMu.Unlock()
	if (!pending || g.Key != key || !strings.Contains(g.Detail, "直接全量")) && !told {
		t.Fatalf("supervisor not told: %+v (told %v)", g, told)
	}
	if listedAsk(t, s, todo.ID) != nil || len(sentToAgent(f)) != sent {
		t.Fatalf("the decision still waits, or was sent: %d sends", len(sentToAgent(f))-sent)
	}
	f.sessionsFail["box"] = 0
	if result, err = s.chooseOption(context.Background(), todo.ID, "", "直接全量"); err != nil || result.taken || len(sentToAgent(f)) != sent {
		t.Fatalf("the recorded decision was taken again: result=%+v err=%v", result, err)
	}
}

// A decision on priority has a default and a date without the review setting any: after a day unanswered the
// recommendation is taken as the owner's OK would take it.
func TestPriorityDecisionIsTakenAtItsDefaultTime(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	res, err := reviewErr(s, todo.ID, rolloutDecision())
	if err != nil || !strings.Contains(res, "takes the recommendation by default") {
		t.Fatalf("task_review: %q %v", res, err)
	}
	a := askOf(s, todo.ID)
	if d := time.Until(a.Deadline); d < 23*time.Hour+59*time.Minute || d > 24*time.Hour || !a.Reversible {
		t.Fatalf("deadline in %v, reversible %v", d, a.Reversible)
	}
	sent := len(sentToAgent(f))
	s.sweepDecisionDeadlines(time.Now())
	if len(sentToAgent(f)) != sent {
		t.Fatal("taken before its time")
	}
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now())
	if got := sentToAgent(f)[sent:]; len(got) != 1 || !strings.Contains(got[0], "先灰度 10%") {
		t.Fatalf("to the agent: %q", got)
	}
	if a := askOf(s, todo.ID); !a.ByDefault || a.AcceptedAt.IsZero() || todoNow(s, todo.ID).Supervision.State == reviewNeedsUser {
		t.Fatalf("ask %+v", a)
	}
	if listedAsk(t, s, todo.ID) != nil || len(pendingKinds(s, todo.ID)) != 0 {
		t.Fatalf("still waits, or went back to its supervisor: %v", pendingKinds(s, todo.ID))
	}
	// The review's own deadline is kept.
	s2, f2, todo2 := newSupervisedService(t)
	waitsForReview(t, s2, f2, todo2.ID, "两种走法")
	own := rolloutDecision()
	own["deadline"], own["reversible"] = "2h", true
	if _, err := reviewErr(s2, todo2.ID, own); err != nil {
		t.Fatal(err)
	}
	if d := time.Until(askOf(s2, todo2.ID).Deadline); d > 2*time.Hour || d < 119*time.Minute {
		t.Fatalf("the review's deadline was not kept: in %v", d)
	}
}

// A default recommendation at its deadline is not an explicit owner action: with no Agent it keeps the existing
// rollback and reminder behavior instead of becoming a recorded-only success.
func TestDefaultRecommendationWithStoppedAgentKeepsReminder(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	a := askOf(s, todo.ID)
	c := idleAgent(time.Now().Add(-time.Hour))
	c.Status = proto.StatusInterrupted
	f.setSessions("box", c)
	sent := len(sentToAgent(f))
	pushes := len(decidePushes(t, f))
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now())
	got := askOf(s, todo.ID)
	if !got.AcceptedAt.IsZero() || got.ByDefault || got.DueAt.IsZero() ||
		todoNow(s, todo.ID).Supervision.State != reviewNeedsUser || listedAsk(t, s, todo.ID) == nil ||
		len(decidePushes(t, f)) != pushes+1 {
		t.Fatalf("automatic default changed its reminder behavior: %+v", got)
	}
	if len(sentToAgent(f)) != sent {
		t.Fatal("the stopped Agent got the default recommendation")
	}
	key := "undelivered|" + a.AskedAt.UTC().Format(time.RFC3339Nano)
	s.supervise.mu.Lock()
	g, pending := s.supervise.pending[todo.ID+"|"+sigIdle]
	told := s.supervise.sent[todo.ID+"|"+sigIdle] == key
	s.supervise.mu.Unlock()
	if pending && g.Key == key || told {
		t.Fatalf("the default became an owner decision: %+v", g)
	}
}

// A decision that cannot be undone has no default. After a day its old facts leave the owner queue; only a fresh
// supervisor review raises a current, answerable round again.
func TestIrreversibleDecisionWaitsForFreshReviewAfterRecheck(t *testing.T) {
	for _, why := range []string{whyIrreversible, whyVisible} {
		s, f, todo := newSupervisedService(t)
		waitsForReview(t, s, f, todo.ID, "要删掉旧的生产库表吗？")
		args := dropTableDecision()
		args["why_owner"] = why
		res, err := reviewErr(s, todo.ID, args)
		if err != nil || !strings.Contains(res, "never taken by default") {
			t.Fatalf("%s: task_review: %q %v", why, res, err)
		}
		a := askOf(s, todo.ID)
		if !a.Deadline.IsZero() || a.Reversible {
			t.Fatalf("%s: a default on a decision never taken by default: %+v", why, a)
		}
		if ask := listedAsk(t, s, todo.ID); ask["default"] != "never" || ask["deadline"] != nil {
			t.Fatalf("%s: ask %v", why, ask)
		}
		sent, pushes := len(sentToAgent(f)), len(decidePushes(t, f))
		now := time.Now()
		for _, after := range []time.Duration{time.Hour, 23 * time.Hour} {
			s.sweepDecisionDeadlines(now.Add(after))
		}
		if len(pendingKinds(s, todo.ID)) != 0 || len(decidePushes(t, f)) != pushes {
			t.Fatalf("%s: told before a day passed: %v", why, pendingKinds(s, todo.ID))
		}
		s.sweepDecisionDeadlines(now.Add(24*time.Hour + time.Minute))
		if !signalled(s, f, todo.ID, sigRecheck, "已交回督办") || len(decidePushes(t, f)) != pushes {
			t.Fatalf("%s: no recheck after a day, or the owner was raised it before the supervisor looked", why)
		}
		if ask := listedAsk(t, s, todo.ID); ask != nil {
			t.Fatalf("%s: the stale decision still waits for the owner: %v", why, ask)
		}
		s.sweepDecisionDeadlines(now.Add(25*time.Hour + 2*time.Minute))
		if got := len(decidePushes(t, f)); got != pushes {
			t.Fatalf("%s: the unrefreshed decision was pushed again: %d, want %d", why, got, pushes)
		}
		if a := askOf(s, todo.ID); len(sentToAgent(f)) != sent || !a.AcceptedAt.IsZero() || a.ByDefault || !a.RaisedAt.IsZero() ||
			todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
			t.Fatalf("%s: the stale round changed: %+v, to the agent %q", why, a, sentToAgent(f)[sent:])
		}

		// Re-issuing after checking the facts is what raises it to the owner again.
		fresh := dropTableDecision()
		fresh["why_owner"] = why
		if _, err := reviewErr(s, todo.ID, fresh); err != nil {
			t.Fatal(err)
		}
		if got := len(decidePushes(t, f)); got != pushes+1 {
			t.Fatalf("%s: the fresh review did not raise the decision: %d pushes", why, got)
		}
		if ask := listedAsk(t, s, todo.ID); ask == nil {
			t.Fatalf("%s: the fresh review is not answerable", why)
		}
		s.sweepDecisionDeadlines(time.Now().Add(time.Hour))
		if got := pendingKinds(s, todo.ID); len(got) != 0 {
			t.Fatalf("%s: the fresh decision inherited the recheck: %v", why, got)
		}
		if rec := answerAsk(s, todo.ID, map[string]string{"option": "删掉旧表"}); rec.Code != 200 {
			t.Fatalf("%s: answering the fresh decision: %d %s", why, rec.Code, rec.Body.String())
		}
	}
}

// A decision older than a day is handed back to its supervisor once: the message carries the decision as it was asked
// as task facts. Handling stays in the static system policy. It is not told again after a restart.
func TestOldDecisionGoesBackToItsSupervisorOnce(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "要删掉旧的生产库表吗？")
	if _, err := reviewErr(s, todo.ID, dropTableDecision()); err != nil {
		t.Fatal(err)
	}
	before := len(f.notices())
	day := time.Now().Add(decisionRecheckAfter + time.Minute)
	s.sweepDecisionAges(day)
	waitFor(t, "the recheck message", func() bool { return len(f.notices()) > before })
	n := f.notices()[before]
	// Check the actual DeviceAssistantSend payload contains no decision-handling manual.
	// i18n-ignore: exact instructions in the runtime-bound supervision prompt.
	for _, instruction := range []string{
		"- 决定交回你核对的：", "- 用户那边没有能回答的东西的：",
		"- 用户回「你来定」的：", "- 用户回「这不是我要的」的：", "- 用户回「先讲清背景」的：",
		"todo_decision_add",
	} {
		if got := strings.Count(n, instruction); got != 0 {
			t.Errorf("runtime-bound event repeats instruction %q %d times", instruction, got)
		}
	}
	for _, want := range []string{"等了超过一天", "已交回督办", "why_owner=irreversible，不会默认执行", "删不删旧表", "选项「先导出再删」：多花半天，数据留一份备份",
		"推荐：删掉旧表", "等待的代价：磁盘两周后写满", "signal=recheck"} {
		if !strings.Contains(n, want) {
			t.Errorf("recheck message lacks %q:\n%s", want, n)
		}
	}
	if askOf(s, todo.ID).RecheckAt.IsZero() {
		t.Fatal("the recheck was not kept on the ask")
	}
	forgetSignals(t, s)
	s.sweepDecisionAges(day.Add(time.Minute))
	s.sweepDecisionAges(day.Add(72 * time.Hour))
	if got := pendingKinds(s, todo.ID); slices.Contains(got, sigRecheck) {
		t.Fatalf("handed back twice: %v", got)
	}
	// Re-issued, it is a new decision with a day of its own.
	if _, err := reviewErr(s, todo.ID, dropTableDecision()); err != nil {
		t.Fatal(err)
	}
	if a := askOf(s, todo.ID); !a.RecheckAt.IsZero() || !a.RaisedAt.IsZero() {
		t.Fatalf("the re-issued decision starts old: %+v", a)
	}
	s.sweepDecisionAges(time.Now().Add(time.Hour))
	if got := pendingKinds(s, todo.ID); len(got) != 0 {
		t.Fatalf("a fresh decision went back: %v", got)
	}
}

// The backlog: a task left needs_user with nothing the owner can answer (no ask at all, or one that recommends
// nothing) is handed back to its supervisor once to be restated; a task with an answerable decision is not.
func TestUndecidableTaskGoesBackToItsSupervisorOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		ask  bool
	}{{"no ask", false}, {"an ask that recommends nothing", true}} {
		s, f, todo := newSupervisedService(t)
		ctx := context.Background()
		waitsForReview(t, s, f, todo.ID, "做完了，等你看。")
		// As a hub from before #3804 left it: needs_user, the whole matter in the note.
		old := time.Now().Add(-92 * time.Hour).UTC()
		if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
			v.State = reviewNeedsUser
			v.Reviews = append(v.Reviews, TaskReview{At: old, Verdict: reviewNeedsUser, Note: "草稿改动等用户看了再决定合不合"})
			return true
		}); err != nil {
			t.Fatal(err)
		}
		if tc.ask {
			s.askConclusion(ctx, todoNow(s, todo.ID), conclusionInput{Outcome: outcomeDecide, Note: "草稿改动等用户看了再决定合不合"})
			if a := askOf(s, todo.ID); a == nil || a.Recommend != "" {
				t.Fatalf("%s: ask %+v", tc.name, a)
			}
		}
		before := len(f.notices())
		s.superviseWatch(ctx, time.Now())
		waitFor(t, "the undecidable message", func() bool { return len(f.notices()) > before })
		n := f.notices()[before]
		for _, want := range []string{"标着等用户决定，但用户那边没有能回答的东西", "草稿改动等用户看了再决定合不合", "signal=undecidable"} {
			if !strings.Contains(n, want) {
				t.Errorf("%s: message lacks %q:\n%s", tc.name, want, n)
			}
		}
		v := todoNow(s, todo.ID).Supervision
		if v.Reviews[len(v.Reviews)-1].RestateAt.IsZero() {
			t.Fatalf("%s: not kept on the review", tc.name)
		}
		forgetSignals(t, s)
		s.superviseWatch(ctx, time.Now().Add(time.Hour))
		s.superviseWatch(ctx, time.Now().Add(48*time.Hour))
		if got := pendingKinds(s, todo.ID); slices.Contains(got, sigUndecidable) {
			t.Fatalf("%s: handed back twice: %v", tc.name, got)
		}
	}
	// An answerable decision is not undecidable, however old its review; nor is one whose review is a moment old.
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "两种走法")
	if _, err := reviewErr(s, todo.ID, dropTableDecision()); err != nil {
		t.Fatal(err)
	}
	s.superviseWatch(context.Background(), time.Now().Add(72*time.Hour))
	if got := pendingKinds(s, todo.ID); slices.Contains(got, sigUndecidable) {
		t.Fatalf("an answerable decision was handed back as undecidable: %v", got)
	}
}

// Choosing an option on the row: the recommended one is taken as OK takes it; another goes to the task's agent as the
// owner's choice, with what it leads to, and ends the decision; one that is none of them is refused.
func TestOptionChosenGoesToTheAgent(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	sent := len(sentToAgent(f))
	if rec := answerAsk(s, todo.ID, map[string]string{"option": "再等等"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("an option that is none of them: %d %s", rec.Code, rec.Body.String())
	}
	if rec := answerAsk(s, todo.ID, map[string]string{"option": "直接全量", "response": "explain_first"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("an option and a response: %d", rec.Code)
	}
	if rec := answerAsk(s, todo.ID, map[string]string{"response": "later"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown response: %d", rec.Code)
	}
	if len(sentToAgent(f)) != sent || listedAsk(t, s, todo.ID) == nil {
		t.Fatal("a refused answer changed the decision")
	}
	if rec := answerAsk(s, todo.ID, map[string]string{"option": "直接全量"}); rec.Code != http.StatusOK {
		t.Fatalf("option: %d %s", rec.Code, rec.Body.String())
	}
	got := sentToAgent(f)[sent:]
	if len(got) != 1 || !strings.Contains(got[0], "直接全量") || !strings.Contains(got[0], "出问题要整体回滚") {
		t.Fatalf("to the agent: %q", got)
	}
	if a := askOf(s, todo.ID); a.AcceptedAt.IsZero() || a.ByDefault || listedAsk(t, s, todo.ID) != nil ||
		todoNow(s, todo.ID).Supervision.State == reviewNeedsUser {
		t.Fatalf("the decision still waits: %+v", a)
	}
	if rec := answerAsk(s, todo.ID, map[string]string{"option": "先灰度 10%"}); rec.Code != http.StatusConflict {
		t.Fatalf("a second answer: %d", rec.Code)
	}
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now())
	if len(sentToAgent(f)) != sent+1 {
		t.Fatalf("the default was taken after the owner chose: %q", sentToAgent(f)[sent:])
	}

	// The recommended option is the plain OK.
	s2, f2, todo2 := newSupervisedService(t)
	waitsForReview(t, s2, f2, todo2.ID, "两种走法")
	if _, err := reviewErr(s2, todo2.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	sent = len(sentToAgent(f2))
	if rec := answerAsk(s2, todo2.ID, map[string]string{"option": "先灰度 10%"}); rec.Code != http.StatusOK {
		t.Fatalf("recommended option: %d %s", rec.Code, rec.Body.String())
	}
	want := i18n.T(s2.botLang(context.Background()), "assistant-reports.verdict_take_to_agent", i18n.Args{"option": "先灰度 10%"})
	if got := sentToAgent(f2)[sent:]; len(got) != 1 || got[0] != want {
		t.Fatalf("to the agent: %q", got)
	}
}

// A stopped, absent or offline Agent cannot take the owner's answer, but the answer is still made: it is recorded,
// the ask ends, and the supervisor gets the choice and the delivery failure. So is one whose send failed before
// anything was typed (the node's "Authentication required", a request that never went out, #17431): the reply says
// so, with the reason. A failure the hub cannot tell stays retryable.
func TestDecisionWithoutLiveAgentIsRecordedForSupervisor(t *testing.T) {
	for _, tc := range []struct {
		name string
		stop func(*fakeHub)
		want int
	}{
		{"stopped", func(f *fakeHub) {
			c := idleAgent(time.Now().Add(-time.Hour))
			c.Status = proto.StatusInterrupted
			f.setSessions("box", c)
		}, http.StatusOK},
		{"stopped refusal", func(f *fakeHub) {
			f.mu.Lock()
			f.failSend = &rpc.Error{Code: proto.CodeAgentStopped, Message: "the agent has stopped"}
			f.mu.Unlock()
		}, http.StatusOK},
		{"no agent", func(f *fakeHub) { f.setSessions("box") }, http.StatusOK},
		{"offline", func(f *fakeHub) {
			f.mu.Lock()
			f.devices = []Device{}
			f.mu.Unlock()
		}, http.StatusOK},
		{"authentication failure", func(f *fakeHub) {
			f.mu.Lock()
			f.failSend = &rpc.Error{Message: "Authentication required"}
			f.mu.Unlock()
		}, http.StatusOK},
		{"never sent", func(f *fakeHub) {
			f.mu.Lock()
			f.failSend = rpc.NotSent(context.Canceled)
			f.mu.Unlock()
		}, http.StatusOK},
		{"transient send failure", func(f *fakeHub) {
			f.mu.Lock()
			f.failSend = errors.New("temporary network break")
			f.mu.Unlock()
		}, http.StatusBadGateway},
	} {
		for _, option := range []string{"直接全量", "先灰度 10%"} {
			t.Run(tc.name+"/"+option, func(t *testing.T) {
				database := todoTestDB(t)
				s, f, todo := newSupervisedServiceOn(t, database)
				waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
				if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
					t.Fatal(err)
				}
				a := askOf(s, todo.ID)
				tc.stop(f)
				sent := len(sentToAgent(f))
				rec := answerAsk(s, todo.ID, map[string]string{"option": option})
				if rec.Code != tc.want {
					t.Fatalf("accept: %d %s, want %d", rec.Code, rec.Body.String(), tc.want)
				}
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body["code"] != "accept_not_taken" && tc.want == http.StatusBadGateway {
					t.Fatalf("error code %v", body["code"])
				}
				if got := len(sentToAgent(f)); got != sent {
					t.Fatalf("%d sends after the failed one", got-sent)
				}
				decisions, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
				if err != nil {
					t.Fatal(err)
				}
				key := "undelivered|" + a.AskedAt.UTC().Format(time.RFC3339Nano)
				// A digest removes pending signals before marking them sent. Take the snapshot
				// outside that interval, after any in-flight digest has finished.
				s.supervise.dedupMu.Lock()
				s.supervise.mu.Lock()
				g, pending := s.supervise.pending[todo.ID+"|"+sigIdle]
				told := s.supervise.sent[todo.ID+"|"+sigIdle] == key
				s.supervise.mu.Unlock()
				s.supervise.dedupMu.Unlock()
				if tc.want == http.StatusBadGateway {
					if len(decisions) != 0 || askOf(s, todo.ID).AcceptedAt.IsZero() == false ||
						todoNow(s, todo.ID).Supervision.State != reviewNeedsUser || pending && g.Key == key || told {
						t.Fatalf("retryable failure took the decision: decisions=%+v ask=%+v signal=%+v", decisions, askOf(s, todo.ID), g)
					}
					return
				}
				if reason, _ := body["reason"].(string); body["ok"] != true || body["delivered"] != "none" || reason == "" {
					t.Fatalf("recorded response %v", body)
				}
				if tc.name == "authentication failure" && body["reason"] != "Authentication required" {
					t.Fatalf("reason %v", body["reason"])
				}
				if len(decisions) != 1 || decisions[0].Choice != option {
					t.Fatalf("decisions %+v", decisions)
				}
				if got := askOf(s, todo.ID); got.AcceptedAt.IsZero() || listedAsk(t, s, todo.ID) != nil ||
					todoNow(s, todo.ID).Supervision.State == reviewNeedsUser {
					t.Fatalf("the decision still waits: %+v", got)
				}
				if (!pending || g.Key != key || !strings.Contains(g.Detail, option)) && !told {
					t.Fatalf("supervisor not told: %+v (told %v)", g, told)
				}
				s.waitWorkEvents() // do not let the next database fixture truncate an event still in flight
			})
		}
	}
}

// "You decide" on a decision that can be undone: nothing is taken for the owner, the task no longer waits for them, the
// supervisor is told once with the decision, and it may not put the same question to them again.
func TestYouDecideHandsADecisionBackToTheSupervisor(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	sent, before := len(sentToAgent(f)), len(f.notices())
	if rec := answerAsk(s, todo.ID, map[string]string{"response": "decide_yourself"}); rec.Code != http.StatusOK {
		t.Fatalf("answer: %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, "the owner_cannot_answer message", func() bool { return len(f.notices()) > before })
	n := f.notices()[before]
	for _, want := range []string{"用户回答不了你发的决定，回的是「你来定」", "why_owner=priority）：先灰度还是直接全量", "选项「直接全量」",
		"signal=owner_cannot_answer"} {
		if !strings.Contains(n, want) {
			t.Errorf("message lacks %q:\n%s", want, n)
		}
	}
	v := todoNow(s, todo.ID).Supervision
	if a := askOf(s, todo.ID); len(sentToAgent(f)) != sent || a.AcceptedAt.IsZero() || a.Response != "decide_yourself" || a.ByDefault ||
		v.State == reviewNeedsUser || v.Reviews[len(v.Reviews)-1].OwnerResponse != "decide_yourself" || listedAsk(t, s, todo.ID) != nil {
		t.Fatalf("after \"You decide\": ask %+v, supervision %+v, to the agent %q", a, v, sentToAgent(f)[sent:])
	}
	pastDeadline(t, s, todo.ID)
	s.sweepDecisionDeadlines(time.Now())
	if len(sentToAgent(f)) != sent {
		t.Fatalf("its default was taken after \"You decide\": %q", sentToAgent(f)[sent:])
	}
	if rec := answerAsk(s, todo.ID, map[string]string{"response": "decide_yourself"}); rec.Code != http.StatusConflict {
		t.Fatalf("a second answer: %d", rec.Code)
	}
	forgetSignals(t, s)
	s.superviseWatch(context.Background(), time.Now().Add(time.Hour))
	if got := pendingKinds(s, todo.ID); slices.Contains(got, sigUndecidable) || slices.Contains(got, sigOwnerCannot) {
		t.Fatalf("told again: %v", got)
	}
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err == nil || !strings.Contains(err.Error(), "don't ask them the same thing again") {
		t.Fatalf("the same decision again: %v", err)
	}
	// Counted: a work event, summed by the conversation that asked.
	s.work.mu.Lock()
	events := append([]db.WorkEvent(nil), s.work.testEvents...)
	s.work.mu.Unlock()
	st := summarizeWork(24, events)
	if got := st.OwnerCannotAnswer["conv1"]["decide_yourself"]; got != 1 || len(st.OwnerCannotAnswer) != 1 {
		t.Fatalf("stats %v", st.OwnerCannotAnswer)
	}
}

// "You decide" on a decision that cannot be undone takes nothing: the task still waits for the owner, and the
// supervisor can only restate it as a plain approval with two options.
func TestYouDecideNeverTakesAnIrreversibleDecision(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "要删掉旧的生产库表吗？")
	if _, err := reviewErr(s, todo.ID, dropTableDecision()); err != nil {
		t.Fatal(err)
	}
	sent, before := len(sentToAgent(f)), len(f.notices())
	if rec := answerAsk(s, todo.ID, map[string]string{"response": "decide_yourself"}); rec.Code != http.StatusOK {
		t.Fatalf("answer: %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, "the owner_cannot_answer message", func() bool { return len(f.notices()) > before })
	if n := f.notices()[before]; !strings.Contains(n, "不会默认执行") || !strings.Contains(n, "signal=owner_cannot_answer") {
		t.Fatalf("message:\n%s", n)
	}
	s.sweepDecisionDeadlines(time.Now().Add(72 * time.Hour))
	if a := askOf(s, todo.ID); len(sentToAgent(f)) != sent || a.ByDefault || todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatalf("an irreversible decision answered \"You decide\" was taken: %+v, to the agent %q", a, sentToAgent(f)[sent:])
	}
	if _, err := reviewErr(s, todo.ID, dropTableDecision()); err == nil || !strings.Contains(err.Error(), "exactly two options") {
		t.Fatalf("restated with three options: %v", err)
	}
	yesNo := dropTableDecision()
	yesNo["reason"], yesNo["recommend"] = "批准删除旧表吗", "批准删除"
	yesNo["options"] = []map[string]string{{"label": "批准删除", "leads_to": "旧数据不能恢复，释放 40 GB"}, {"label": "不删", "leads_to": "旧表留着，磁盘两周后写满"}}
	if _, err := reviewErr(s, todo.ID, yesNo); err != nil {
		t.Fatalf("restated as a plain approval: %v", err)
	}
	if ask := listedAsk(t, s, todo.ID); ask == nil || ask["question"] != "批准删除旧表吗" || ask["default"] != "never" {
		t.Fatalf("ask %v", ask)
	}
}

// "This isn't what I asked for": nothing is taken, the supervisor is told, and the task's agent gets no automatic
// follow-up until the supervisor reviews the task again.
func TestNotWhatIAskedHoldsTheAgentsFollowUps(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	if s.followUpsHeld("box", "s1") {
		t.Fatal("held before the owner said anything")
	}
	sent, before := len(sentToAgent(f)), len(f.notices())
	if rec := answerAsk(s, todo.ID, map[string]string{"response": "not_what_i_asked"}); rec.Code != http.StatusOK {
		t.Fatalf("answer: %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, "the owner_cannot_answer message", func() bool { return len(f.notices()) > before })
	if n := f.notices()[before]; !strings.Contains(n, "回的是「这不是我要的」") || !strings.Contains(n, "signal=owner_cannot_answer") ||
		!strings.Contains(n, "任务来历（hub 记的）") {
		t.Fatalf("message:\n%s", n)
	}
	if len(sentToAgent(f)) != sent || listedAsk(t, s, todo.ID) != nil || !s.followUpsHeld("box", "s1") {
		t.Fatalf("after \"not what I asked\": to the agent %q, held %v", sentToAgent(f)[sent:], s.followUpsHeld("box", "s1"))
	}
	// The work manager's guard refuses to act on the agent meanwhile.
	f.setSessions("box", proto.Session{SessionID: "s1", MachineID: "box", Cwd: "/code/app", Agent: "claude", Status: proto.StatusDone,
		State: proto.StateUnread, UpdatedAt: time.Now()})
	e := db.SessionWork{MachineID: "box", SessionID: "s1"}
	if _, why := s.workGuard(s.newWorkView(context.Background()), e, false); !strings.Contains(why, "not what they asked") {
		t.Fatalf("workGuard: %q", why)
	}
	// The supervisor compared and re-scoped it: the hold is over.
	if _, err := reviewErr(s, todo.ID, map[string]any{"verdict": "rework", "note": "只修登录页，别动别的页面"}); err != nil {
		t.Fatal(err)
	}
	if s.followUpsHeld("box", "s1") {
		t.Fatal("still held after the supervisor's review")
	}
}

// "Tell me the background first": the decision stays as it is and is no longer taken by default; the supervisor is told
// to re-issue it with the background.
func TestExplainFirstKeepsTheDecision(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	sent, before := len(sentToAgent(f)), len(f.notices())
	if rec := answerAsk(s, todo.ID, map[string]string{"response": "explain_first"}); rec.Code != http.StatusOK {
		t.Fatalf("answer: %d %s", rec.Code, rec.Body.String())
	}
	waitFor(t, "the owner_cannot_answer message", func() bool { return len(f.notices()) > before })
	if n := f.notices()[before]; !strings.Contains(n, "回的是「先讲清背景」") || !strings.Contains(n, "signal=owner_cannot_answer") {
		t.Fatalf("message:\n%s", n)
	}
	ask := listedAsk(t, s, todo.ID)
	if ask == nil || ask["response"] != "explain_first" || ask["default"] != "never" || ask["deadline"] != nil ||
		todoNow(s, todo.ID).Supervision.State != reviewNeedsUser {
		t.Fatalf("ask %v", ask)
	}
	s.accept.mu.Lock()
	s.accept.asks[todo.ID].Deadline = time.Now().Add(-time.Minute)
	s.accept.mu.Unlock()
	s.sweepDecisionDeadlines(time.Now())
	if len(sentToAgent(f)) != sent || !askOf(s, todo.ID).AcceptedAt.IsZero() {
		t.Fatalf("taken by default while the owner waits for the background: %q", sentToAgent(f)[sent:])
	}
	// Re-issued with the background, it is a new round with its own default.
	again := rolloutDecision()
	again["reason"] = "登录页修复已合并；先灰度还是直接全量"
	if _, err := reviewErr(s, todo.ID, again); err != nil {
		t.Fatal(err)
	}
	if ask := listedAsk(t, s, todo.ID); ask["response"] != nil || ask["default"] != "taken" {
		t.Fatalf("re-issued ask %v", ask)
	}
}

// The owner answers a decision twice within half an hour: first "Tell me the background first", the supervisor
// re-issues it, then "You decide", or the background again. The hub takes the second answer and ends the round, so
// the supervisor has to hear it, though its stopped agent said nothing new in between (#6027).
func TestTheOwnersSecondAnswerReachesTheSupervisor(t *testing.T) {
	for response, says := range map[string]string{respondDecideYourself: "回的是「你来定」", respondExplainFirst: "回的是「先讲清背景」"} {
		t.Run(response, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
			if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
				t.Fatal(err)
			}
			before := len(f.notices())
			if rec := answerAsk(s, todo.ID, map[string]string{"response": respondExplainFirst}); rec.Code != http.StatusOK {
				t.Fatalf("first answer: %d %s", rec.Code, rec.Body.String())
			}
			waitFor(t, "the first answer told", func() bool {
				return slices.ContainsFunc(f.notices()[before:], func(n string) bool { return strings.Contains(n, "回的是「先讲清背景」") })
			})
			again := rolloutDecision()
			again["reason"] = "登录页修复已合并；先灰度还是直接全量"
			if _, err := reviewErr(s, todo.ID, again); err != nil {
				t.Fatal(err)
			}
			before = len(f.notices())
			if rec := answerAsk(s, todo.ID, map[string]string{"response": response}); rec.Code != http.StatusOK {
				t.Fatalf("second answer: %d %s", rec.Code, rec.Body.String())
			}
			if v := todoNow(s, todo.ID).Supervision; v.Reviews[len(v.Reviews)-1].OwnerResponse != response {
				t.Fatalf("the second answer was not recorded: %+v", v.Reviews[len(v.Reviews)-1])
			}
			waitFor(t, "the second answer told", func() bool {
				return slices.ContainsFunc(f.notices()[before:], func(n string) bool { return strings.Contains(n, says) })
			})
		})
	}
}

// The supervision message for the three decision signals, as the supervisor reads it.
func TestGoldenSuperviseDecisions(t *testing.T) {
	d := &prompts.SuperviseDecision{WhyOwner: "irreversible", Question: "Drop the old table?", Recommend: "Drop it", NeverDefault: true,
		Cost: "The disk fills up in two weeks", Background: "From the main conversation, 2026-11-07: “Finish the migration” · Now: migrated, old table still there",
		Options: []prompts.SuperviseOption{{Label: "Drop it", LeadsTo: "the old rows cannot be restored"}, {Label: "Keep it", LeadsTo: "40 GB stay in use"}}}
	golden(t, "supervise.decisions", prompts.RenderSupervise(prompts.Supervise{Decisions: true, Entries: sortEntries([]prompts.SuperviseEntry{
		{ID: "todo_1", Title: "Finish the migration", Kind: sigRecheck, Minutes: 1500, Decision: d, DoneWhen: []string{"old table gone"}},
		{ID: "todo_2", Title: "Tidy the docs", Kind: sigUndecidable, Detail: "waits for the user to look"},
		{ID: "todo_3", Title: "Finish the migration", Kind: sigOwnerCannot, Response: respondDecideYourself, Decision: d},
		{ID: "todo_4", Title: "Fix the login crash", Kind: sigOwnerCannot, Response: respondNotAsked,
			Decision: &prompts.SuperviseDecision{WhyOwner: "priority", Question: "Canary or everyone?", Recommend: "Canary",
				Options: []prompts.SuperviseOption{{Label: "Canary", LeadsTo: "10% today"}, {Label: "Everyone", LeadsTo: "all users today"}}}},
		{ID: "todo_5", Title: "Fix the login crash", Kind: sigOwnerCannot, Response: respondExplainFirst},
	})}))
}

// An undecidable hand-back that comes due while its conversation is stopped (/stop, #3308) is not kept on the review
// as told, since signal drops it then: once the user speaks again it reaches the supervisor (#13415).
func TestUndecidableUnderStopWaitsForTheUser(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	ctx := context.Background()
	waitsForReview(t, s, f, todo.ID, "做完了，等你看。")
	old := time.Now().Add(-92 * time.Hour).UTC()
	if _, _, err := s.updateSupervision(ctx, todo.ID, func(v *Supervision) bool {
		v.State = reviewNeedsUser
		v.Reviews = append(v.Reviews, TaskReview{At: old, Verdict: reviewNeedsUser, Note: "草稿改动等用户看了再决定合不合"})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	s.markReporter(ctx, "box", todo.Origin)
	s.setReporterStopped(ctx, todo.Origin, "app")
	s.superviseWatch(ctx, time.Now())
	v := todoNow(s, todo.ID).Supervision
	if !v.Reviews[len(v.Reviews)-1].RestateAt.IsZero() {
		t.Fatal("kept as told while stopped, though nothing was queued")
	}
	s.setReporterStopped(ctx, todo.Origin, "")
	before := len(f.notices())
	s.superviseWatch(ctx, time.Now().Add(time.Hour))
	waitFor(t, "the undecidable message after /stop is lifted", func() bool {
		for _, n := range f.notices()[before:] {
			if strings.Contains(n, "signal=undecidable") {
				return true
			}
		}
		return false
	})
}

func TestUndeliveredDecisionBypassesSupervisionBatchAndCooldown(t *testing.T) {
	ctx := t.Context()
	s, _ := newQuietService(t)
	todo := orphanTask(t, s)
	s.supervise.mu.Lock()
	s.supervise.stopped = true // inspect the scheduled signal without racing its timer
	s.supervise.mu.Unlock()
	now := time.Now()
	reply := s.decisionNotDelivered(ctx, todo.ID, now, "Use the agreed option", errors.New("no Agent"))
	if !reply.taken || reply.undelivered == nil {
		t.Fatalf("decision handoff = %+v", reply)
	}
	s.supervise.mu.Lock()
	g := s.supervise.pending[todo.ID+"|"+sigIdle]
	s.supervise.mu.Unlock()
	if !g.Handoff || !strings.Contains(g.Detail, "Use the agreed option") {
		t.Fatalf("decision was not routed to supervision: %+v", g)
	}
	for _, batch := range []time.Duration{0, time.Hour} {
		if wait, ok := digestWaitWithBatch(g.At, g.At, []superviseSignal{g}, s.superviseTimings(), batch); !ok || wait != 0 {
			t.Fatalf("decision delayed by %s with batch %s", wait, batch)
		}
	}
	s.supervise.mu.Lock()
	s.supervise.told = map[string]time.Time{"conv1": g.At}
	s.supervise.mu.Unlock()
	groups := map[string][]superviseSignal{"conv1": {g}}
	if got := s.gateDigestBatches([]string{"conv1"}, groups, g.At, time.Hour, time.Hour); len(got) != 1 || got[0] != "conv1" {
		t.Fatalf("the recipient's digest still waits: %v", got)
	}
}
