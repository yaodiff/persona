package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The to-do waiting for the owner carries its open ask (#2056): what the briefing's `accept[]` row says of it, so the apps
// can show the recommendation and take it with POST /api/v1/todos/{id}/accept.

// listedTodos is GET /api/v1/todos as the apps read it: each to-do as JSON, by id.
func listedTodos(t *testing.T, s *Service) map[string]map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/todos", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Todos []map[string]any `json:"todos"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, x := range out.Todos {
		id, _ := x["id"].(string)
		byID[id] = x
	}
	return byID
}

// listedAsk is the `ask` of to-do id in GET /api/v1/todos (nil: none).
func listedAsk(t *testing.T, s *Service, id string) map[string]any {
	t.Helper()
	x, ok := listedTodos(t, s)[id]
	if !ok {
		t.Fatalf("to-do %s is not listed", id)
	}
	ask, _ := x["ask"].(map[string]any)
	if _, there := x["ask"]; there && ask == nil {
		t.Fatalf("to-do %s: ask %v", id, x["ask"])
	}
	return ask
}

// acceptAt presses OK for to-do id with asked_at as a client read it.
func acceptAt(s *Service, id, askedAt string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"asked_at": askedAt})
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/todos/"+id+"/accept", strings.NewReader(string(body))))
	return rec
}

// A decision with a recommended option is on its to-do while it waits: the verdict line, the option and the round, the
// same as the briefing's row; OK with that asked_at takes it, and from then on the to-do has no ask.
func TestTodoListCarriesTheDecisionWaiting(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	if ask := listedAsk(t, s, todo.ID); ask != nil {
		t.Fatalf("an ask before any conclusion: %v", ask)
	}
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	ask := listedAsk(t, s, todo.ID)
	asked := askOf(s, todo.ID).AskedAt
	if ask == nil || ask["outcome"] != "decide" || ask["verdict"] != "🤔 要你拍板：先灰度还是直接全量" || ask["recommend"] != "先灰度 10%" {
		t.Fatalf("ask %v", ask)
	}
	at, _ := ask["asked_at"].(string)
	if got, err := time.Parse(time.RFC3339Nano, at); err != nil || !got.Equal(asked) {
		t.Fatalf("asked_at %q, the round was asked at %v", at, asked)
	}
	sent := len(sentToAgent(f))
	if rec := acceptAt(s, todo.ID, at); rec.Code != http.StatusOK {
		t.Fatalf("OK: %d %s", rec.Code, rec.Body.String())
	}
	if got := sentToAgent(f)[sent:]; len(got) != 1 || !strings.Contains(got[0], "先灰度 10%") {
		t.Fatalf("to the agent: %q", got)
	}
	if ask := listedAsk(t, s, todo.ID); ask != nil {
		t.Fatalf("still an ask once taken: %v", ask)
	}
}

func TestTodoGetReportsTheCurrentPendingDecision(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "The rollout options are ready.")
	get := func() string {
		t.Helper()
		args, _ := json.Marshal(map[string]string{"id": todo.ID})
		got, err := s.runTool(t.Context(), "server", "conv1", "todo_get", args)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	const pending = "pending user decision (needs_user"
	if got := get(); strings.Contains(got, pending) {
		t.Fatalf("pending decision before task_review: %s", got)
	}
	reviewTask(t, s, todo.ID, map[string]any{
		"verdict": "needs_user", "reason": "How should the release proceed?", "recommend": "Try a staged rollout",
		"options": []DecisionOption{{Label: "Try a staged rollout", LeadsTo: "Check a small group first"}, {Label: "Wait for verification", LeadsTo: "Keep the current release"}},
	})
	ask := listedAsk(t, s, todo.ID)
	at := ask["asked_at"].(string)
	got := get()
	for _, want := range []string{pending, at, "How should the release proceed?", "recommended: Try a staged rollout", "Wait for verification: Keep the current release"} {
		if !strings.Contains(got, want) {
			t.Errorf("todo_get missing %q: %s", want, got)
		}
	}
	rec := httptest.NewRecorder()
	body, _ := json.Marshal(map[string]string{"asked_at": at, "option": "Wait for verification"})
	s.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/todos/"+todo.ID+"/accept", strings.NewReader(string(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("choose the alternate option: %d %s", rec.Code, rec.Body.String())
	}
	if got := get(); strings.Contains(got, pending) {
		t.Fatalf("still pending after the answer: %s", got)
	}
}

// A conclusion that only tells (done, cannot be done) is an ask too, without a recommendation; the to-dos nobody waits on
// have none.
func TestTodoListCarriesAConclusionWithoutRecommendation(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	other, err := s.createPlainTodo(t.Context(), "买牛奶", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	waitsForReview(t, s, f, todo.ID, "查了，做不了")
	judgeConcluded(t, s, todo.ID)
	ask := listedAsk(t, s, todo.ID)
	if ask == nil || ask["outcome"] != "cannot" || ask["verdict"] != "⛔ 做不了：对方接口不对外开放" || ask["asked_at"] == nil {
		t.Fatalf("ask %v", ask)
	}
	if _, there := ask["recommend"]; there {
		t.Fatalf("a recommendation on a conclusion that only tells: %v", ask)
	}
	if ask := listedAsk(t, s, other.ID); ask != nil {
		t.Fatalf("a plain to-do has an ask: %v", ask)
	}
}

// A decision the supervisor moved on from (it sent the work back) is no longer on the to-do.
func TestTodoAskEndsWhenTheReviewMovesOn(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	if listedAsk(t, s, todo.ID) == nil {
		t.Fatal("no ask while the decision waits")
	}
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "rework", "note": "用户在对话里说了直接全量，按全量做"})
	if ask := listedAsk(t, s, todo.ID); ask != nil {
		t.Fatalf("still an ask after rework: %v", ask)
	}
}

// Direct completion is the existing user PATCH, not acceptance of the recommendation.
// It ends a decision even when its agent is already closed, and keeps normal idle cleanup.
func TestDirectCompletionEndsDecisionWithoutSending(t *testing.T) {
	for _, tc := range []struct {
		name, recommend string
		closed          bool
	}{
		{"idle with recommendation", "Use staged rollout", false},
		{"closed with recommendation", "Use staged rollout", true},
		{"closed without recommendation", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			todoStores(t, func(t *testing.T, e *todoEnv, database *db.DB) {
				ctx := context.Background()
				setSettings(t, e.s, func(v *Settings) { v.Main = "conv1" })
				todo, err := e.s.createTodo(ctx, "Choose rollout", "Review rollout options", "conv1", "")
				if err != nil {
					t.Fatal(err)
				}
				c := idleAgent(time.Now().Add(-time.Hour))
				if tc.closed {
					c.Status, c.PaneID = proto.StatusOffline, ""
				}
				e.f.setSessions("box", c)
				if err := e.s.linkTodoAgent(ctx, todo.ID, "box", "s1", "Rollout", "/code/app", "claude"); err != nil {
					t.Fatal(err)
				}
				e.s.superviseTodo(ctx, todo.ID, "conv1", nil, 0)
				if tc.recommend != "" {
					reviewTask(t, e.s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "Choose a rollout", "recommend": tc.recommend})
				} else {
					legacyDecision(t, e.s, todo.ID, "Choose a rollout")
				}
				ask := listedAsk(t, e.s, todo.ID)
				if ask == nil || ask["outcome"] != outcomeDecide {
					t.Fatalf("no decision waiting: %v", ask)
				}
				at := ask["asked_at"].(string)
				sent := len(sentToAgent(e.f))
				var response struct{ Todo Todo }
				if err := json.Unmarshal(e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`).Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if got := response.Todo; got.DoneAt == nil || got.DoneBy != "" || got.DoneReason != "" {
					t.Fatalf("not a user completion: %+v", got)
				}
				if listedAsk(t, e.s, todo.ID) != nil || len(e.s.awaitingAcceptance(ctx)) != 0 {
					t.Fatal("the completed decision still waits in todos/briefing")
				}
				for _, row := range e.s.boardRows(ctx, e.s.botLang(ctx), "", time.Now()) {
					if row.id == todo.ID {
						t.Fatal("the completed task is still on the pending board")
					}
				}
				todosTold(t, e.s)
				if e.todoOf("box", "s1") != "" || !strings.Contains(e.f.eventTypes(), "todos") {
					t.Fatal("completion did not clear session decoration and notify clients")
				}
				if rec := acceptAt(e.s, todo.ID, at); rec.Code != http.StatusConflict {
					t.Fatalf("late acceptance: %d %s", rec.Code, rec.Body.String())
				}
				if database != nil {
					reloadTodos(t, e.s)
					if mem, stored := todoDoneBoth(t, e, database, todo.ID); !mem || !stored {
						t.Fatal("completion did not survive reload")
					}
					decisions, err := database.ListTodoDecisions(ctx, todo.ID, true)
					if err != nil {
						t.Fatal(err)
					}
					for _, d := range decisions {
						if d.Source == db.DecisionManual {
							t.Fatalf("completion took a recommendation: %+v", d)
						}
					}
				}
				// A later working report must not undo the user's choice, even with a needs_user ledger.
				e.s.taskWorking(ctx, "box", "s1", startRow{})
				if todoNow(e.s, todo.ID).DoneAt == nil {
					t.Fatal("agent activity reopened the user-completed task")
				}
				e.s.closeDoneTaskAgents(ctx, taskCloseRefused{}, doneSeen{})
				if len(e.f.retires()) != 0 {
					t.Fatal("completion bypassed the idle grace period")
				}
				old := taskCloseIdle
				taskCloseIdle = 0
				t.Cleanup(func() { taskCloseIdle = old })
				e.s.closeDoneTaskAgents(ctx, taskCloseRefused{}, doneSeen{})
				if got := e.f.retires(); len(got) != map[bool]int{true: 0, false: 1}[tc.closed] {
					t.Fatalf("idle cleanup: %v", got)
				}
				if got := len(sentToAgent(e.f)); got != sent {
					t.Fatalf("completion/late acceptance sent %d messages (would resume a closed agent)", got-sent)
				}
				e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":false}`)
				if listedAsk(t, e.s, todo.ID) != nil {
					t.Fatal("reopening revived the old decision")
				}
				if rec := acceptAt(e.s, todo.ID, at); rec.Code != http.StatusConflict {
					t.Fatalf("acceptance after reopen: %d %s", rec.Code, rec.Body.String())
				}
			})
		})
	}
}

// The apps are told to read the to-dos again when an ask comes up and when it is taken, also when nothing else of the
// to-do changes with it (a review's own change of the ledger is told before its ask is kept).
func TestTodoAskChangesTellTheApps(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "查了，做不了")
	todosEvents := func() int {
		todosTold(t, s)
		f.mu.Lock()
		defer f.mu.Unlock()
		n := 0
		for _, e := range f.events {
			if e == "todos" {
				n++
			}
		}
		return n
	}
	before := todosEvents()
	s.askConclusion(t.Context(), todoNow(s, todo.ID), conclusionInput{Outcome: outcomeCannot, Reason: "对方接口不对外开放"})
	if todosEvents() == before {
		t.Fatal("no todos event once the ask is kept")
	}
	at, _ := listedAsk(t, s, todo.ID)["asked_at"].(string)
	before = todosEvents()
	if rec := acceptAt(s, todo.ID, at); rec.Code != http.StatusOK {
		t.Fatalf("OK: %d %s", rec.Code, rec.Body.String())
	}
	if todosEvents() == before {
		t.Fatal("no todos event once the ask is taken")
	}
}

// decidePushes are the "decide" pushes asked of the hub, as JSON (what goes over the bus).
func decidePushes(t *testing.T, f *fakeHub) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, n := range f.pushed() {
		if n.Kind != "decide" {
			continue
		}
		raw, _ := json.Marshal(n)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		out = append(out, m)
	}
	return out
}

// A decision with a recommendation is pushed once as a "decide" push: the to-do, the round's asked_at (what OK sends to
// the accept route) and the option, with the card's words; it opens the conversation the task came from.
func TestDecisionPushCarriesTheTodoAndItsRound(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	pushes := decidePushes(t, f)
	if len(pushes) != 1 {
		t.Fatalf("%d decide pushes: %v", len(pushes), f.pushed())
	}
	p := pushes[0]
	td, _ := p["todo"].(map[string]any)
	at, _ := td["asked_at"].(string)
	got, err := time.Parse(time.RFC3339Nano, at)
	if td == nil || td["id"] != todo.ID || td["recommend"] != "先灰度 10%" || err != nil || !got.Equal(askOf(s, todo.ID).AskedAt) {
		t.Fatalf("push's todo %v (the round: %v)", td, askOf(s, todo.ID).AskedAt)
	}
	if p["session_id"] != todo.Origin {
		t.Fatalf("opens %v, the task came from %s", p["session_id"], todo.Origin)
	}
	title, _ := p["title"].(string)
	body, _ := p["body"].(string)
	if !strings.Contains(title, "修登录崩溃") || !strings.Contains(body, "🤔 要你拍板：先灰度还是直接全量") || !strings.Contains(body, "推荐：先灰度 10%") {
		t.Fatalf("push says %q / %q", title, body)
	}
	if rec := acceptAt(s, todo.ID, at); rec.Code != http.StatusOK {
		t.Fatalf("the push's OK: %d %s", rec.Code, rec.Body.String())
	}
}

// A decision's push says how it may be answered, as the to-do's view says it (#10465): its ask is the view's own
// decision, and its lock-screen rule the same, open_only for a step that cannot be undone, answer for a call on
// priority, with the options the recommended first.
func TestDecisionPushSaysItsLockscreen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		review map[string]any
		want   string
	}{
		{"a call on priority", rolloutDecision(), assistantwire.OwnerAskLockscreenAnswer},
		{"a step that cannot be undone", dropTableDecision(), assistantwire.OwnerAskLockscreenOpenOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
			if _, err := reviewErr(s, todo.ID, tc.review); err != nil {
				t.Fatal(err)
			}
			pushes := decidePushes(t, f)
			if len(pushes) != 1 {
				t.Fatalf("%d decide pushes", len(pushes))
			}
			td, _ := pushes[0]["todo"].(map[string]any)
			var own map[string]any
			view, _ := listedTodos(t, s)[todo.ID]["view"].(map[string]any)
			asks, _ := view["asks"].([]any)
			for _, a := range asks {
				if m, _ := a.(map[string]any); m["kind"] == assistantwire.OwnerAskKindDecision {
					own = m
				}
			}
			if own == nil || td["ask"] != own["id"] || td["lockscreen"] != tc.want || own["lockscreen"] != tc.want {
				t.Fatalf("push %v, the view's decision %v", td, own)
			}
			var labels []any
			for _, o := range tc.review["options"].([]map[string]string) {
				labels = append(labels, o["label"])
			}
			if labels[0] != tc.review["recommend"] || !reflect.DeepEqual(td["options"], labels) {
				t.Fatalf("push options %v, want %v", td["options"], labels)
			}
		})
	}
}

// legacyDecision leaves task id as a hub from before #3804 could: waiting for the owner (needs_user) on a decision that
// recommends nothing. task_review refuses to make one now; the ones in the store are still listed and can be ended.
func legacyDecision(t *testing.T, s *Service, id, reason string) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := s.updateSupervision(ctx, id, func(v *Supervision) bool {
		v.State = reviewNeedsUser
		v.Reviews = append(v.Reviews, TaskReview{At: time.Now().UTC(), Verdict: reviewNeedsUser, Note: reason})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	s.askConclusion(ctx, todoNow(s, id), conclusionInput{Outcome: outcomeDecide, Reason: reason})
}

// A decision without a recommended option (one from before #3804) gets no decide push: there is nothing for OK to take.
func TestDecisionWithoutRecommendationIsNotPushedForOK(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	legacyDecision(t, s, todo.ID, "先灰度还是直接全量")
	if s.pushDecision(context.Background(), todo.ID) {
		t.Fatal("a decision that recommends nothing was pushed")
	}
	if pushes := decidePushes(t, f); len(pushes) != 0 {
		t.Fatalf("decide pushes %v", pushes)
	}
}

// An OK from an earlier round (the decision was asked again since) takes nothing: 409, and the agent hears nothing; the
// current round's OK takes the current recommendation.
func TestOKOfAnEarlierRoundTakesNothing(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	pushes := decidePushes(t, f)
	if len(pushes) != 1 {
		t.Fatalf("%d decide pushes", len(pushes))
	}
	first, _ := pushes[0]["todo"].(map[string]any)
	firstAt := askOf(s, todo.ID).AskedAt
	waitFor(t, "the clock to advance past the first decision", func() bool {
		return time.Now().UTC().After(firstAt)
	})
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "灰度比例定多少", "recommend": "灰度 5%"})
	pushes = decidePushes(t, f)
	if len(pushes) != 2 {
		t.Fatalf("%d decide pushes", len(pushes))
	}
	second, _ := pushes[1]["todo"].(map[string]any)
	if first["asked_at"] == second["asked_at"] {
		t.Fatalf("two rounds, one asked_at %v", first["asked_at"])
	}
	sent := len(sentToAgent(f))
	if rec := acceptAt(s, todo.ID, first["asked_at"].(string)); rec.Code != http.StatusConflict {
		t.Fatalf("the earlier round's OK: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(sentToAgent(f)); n != sent {
		t.Fatalf("the earlier round's OK sent %d to the agent", n-sent)
	}
	if rec := acceptAt(s, todo.ID, second["asked_at"].(string)); rec.Code != http.StatusOK {
		t.Fatalf("this round's OK: %d %s", rec.Code, rec.Body.String())
	}
	if got := sentToAgent(f)[sent:]; len(got) != 1 || !strings.Contains(got[0], "灰度 5%") {
		t.Fatalf("to the agent: %q", got)
	}
}

// With the Lark bot off the decision is pushed all the same. The hub taking a push does not say any phone shows its OK,
// so the supervisor still asks them as before, and is told OK on the push may take it first.
func TestDecisionWithoutTheBotIsPushedAndTheSupervisorStillAsks(t *testing.T) {
	s, f, todo := newSupervisedService(t)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	if len(decidePushes(t, f)) != 1 {
		t.Fatalf("no decide push with the bot off: %v", f.pushed())
	}
	if !strings.Contains(res, "Ask them") || !strings.Contains(res, "review it again once they decide") ||
		!strings.Contains(res, "A push with your recommendation also went to their phone") || strings.Contains(res, "Don't ask them again") {
		t.Fatalf("answer: %s", res)
	}
}

// With the bot on, the card and the push both go, once each, and the supervisor hears of both.
func TestDecisionWithTheBotIsCardedAndPushedOnce(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, posts, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	res := reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	if n := len(cardsWith(posts(), "结论：")); n != 1 {
		t.Fatalf("%d conclusion cards", n)
	}
	if n := len(decidePushes(t, f)); n != 1 {
		t.Fatalf("%d decide pushes", n)
	}
	if !strings.Contains(res, "Don't ask them again") || !strings.Contains(res, "went to their phone") {
		t.Fatalf("answer: %s", res)
	}
}

// A task from a muted conversation gets no decision push (its ask is still on the to-do).
func TestDecisionOfAMutedConversationIsNotPushed(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	setMutes(s, Pins{Sessions: []string{"server/" + todo.Origin}})
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"})
	if pushes := decidePushes(t, f); len(pushes) != 0 {
		t.Fatalf("decide pushes for a muted conversation: %v", pushes)
	}
	if listedAsk(t, s, todo.ID) == nil {
		t.Fatal("no ask on the to-do")
	}
}

// How risky answering an ask is (askRisk): open when it cannot be undone or other people will see it, or when Persona
// would take it at a deadline it cannot undo, also once that deadline was handled; else safe, a Please verify too.
func TestAskRisk(t *testing.T) {
	deadline := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		a    acceptAsk
		want string
	}{
		{"a call on priority", acceptAsk{Outcome: outcomeDecide, WhyOwner: whyPriority}, assistantwire.OwnerAskRiskSafe},
		{"taken at a deadline it can undo", acceptAsk{Outcome: outcomeDecide, WhyOwner: whyAcceptance, Deadline: deadline,
			Reversible: true}, assistantwire.OwnerAskRiskSafe},
		{"cannot be undone", acceptAsk{Outcome: outcomeDecide, WhyOwner: whyIrreversible}, assistantwire.OwnerAskRiskOpen},
		{"others will see it", acceptAsk{Outcome: outcomeDecide, WhyOwner: whyVisible, Deadline: deadline, Reversible: true},
			assistantwire.OwnerAskRiskOpen},
		{"taken at a deadline it cannot undo", acceptAsk{Outcome: outcomeDecide, WhyOwner: whyPriority, Deadline: deadline},
			assistantwire.OwnerAskRiskOpen},
		{"that deadline handled", acceptAsk{Outcome: outcomeDecide, WhyOwner: whyPriority, Deadline: deadline,
			DueAt: deadline}, assistantwire.OwnerAskRiskOpen},
		{"outside source can take its own recommendation", acceptAsk{OutsideID: "source", Conclusion: true, Outcome: outcomeDecide,
			Recommend: legacyTerminalOption, Reversible: true}, assistantwire.OwnerAskRiskSafe},
		{"a Please verify", acceptAsk{}, assistantwire.OwnerAskRiskSafe},
	} {
		if got := askRisk(&tc.a, nil); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

// The list's ask carries its risk, and the view's own decision reads it, with the lock-screen action that follows: a
// call on priority is safe, a step that cannot be undone open, and so is one Persona would take at a deadline it
// cannot undo, also once that deadline was handled and the list no longer shows it.
func TestTodoListCarriesTheAskRisk(t *testing.T) {
	atDeadline := func() map[string]any {
		a := rolloutDecision()
		a["deadline"], a["reversible"] = "2h", false
		return a
	}
	for _, tc := range []struct {
		name    string
		review  map[string]any
		handled bool
		want    string
	}{
		{"a call on priority", rolloutDecision(), false, assistantwire.OwnerAskRiskSafe},
		{"a step that cannot be undone", dropTableDecision(), false, assistantwire.OwnerAskRiskOpen},
		{"taken at a deadline it cannot undo", atDeadline(), false, assistantwire.OwnerAskRiskOpen},
		{"that deadline handled", atDeadline(), true, assistantwire.OwnerAskRiskOpen},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")
			if _, err := reviewErr(s, todo.ID, tc.review); err != nil {
				t.Fatal(err)
			}
			if tc.handled {
				s.accept.mu.Lock()
				s.accept.asks[todo.ID].DueAt = time.Now()
				s.accept.mu.Unlock()
			}
			row := listedTodos(t, s)[todo.ID]
			ask, _ := row["ask"].(map[string]any)
			if ask == nil || ask["risk"] != tc.want || tc.handled && ask["deadline"] != nil {
				t.Fatalf("ask %v", ask)
			}
			lock := assistantwire.OwnerAskLockscreenAnswer
			if tc.want == assistantwire.OwnerAskRiskOpen {
				lock = assistantwire.OwnerAskLockscreenOpenOnly
			}
			view, _ := row["view"].(map[string]any)
			asks, _ := view["asks"].([]any)
			var own map[string]any
			for _, a := range asks {
				if m, _ := a.(map[string]any); m["kind"] == assistantwire.OwnerAskKindDecision {
					own = m
				}
			}
			if own == nil || own["risk"] != tc.want || own["lockscreen"] != lock {
				t.Fatalf("the view's decision %v, of %v", own, view)
			}
		})
	}
}

// The shared To-dos fixtures (spec/fixtures/todo-view, spec/fixtures/todo-counts) list each ask's risk as the manager
// would (askRisk): the view reads it as listed, so a case could otherwise state a risk the manager never gives and
// still pass. Every ask a case's input lists, steps included, is checked wherever what it lists tells the answer.
func TestTodoFixturesListTheManagersRisk(t *testing.T) {
	type listed struct {
		Outcome   string     `json:"outcome"`
		WhyOwner  string     `json:"why_owner"`
		Recommend string     `json:"recommend"`
		Deadline  *time.Time `json:"deadline"`
		Default   string     `json:"default"`
		Risk      string     `json:"risk"`
	}
	// fixtureAsk is an acceptance ask todoAsks lists as l, and whether l says enough to tell its risk: a listed
	// decision with a deadline and a recommendation is open when the manager says its default is never taken.
	// With neither a deadline nor a default it had none. A deadline listed without its default (a
	// fixture's shorthand), or a default of never alone (a handled deadline, or no recommendation), could be either.
	fixtureAsk := func(l listed) (acceptAsk, bool) {
		a := acceptAsk{Outcome: l.Outcome, WhyOwner: l.WhyOwner}
		switch {
		case neverDefault(l.WhyOwner):
		case l.Deadline != nil && l.Default == defaultTaken:
			a.Deadline, a.Reversible = *l.Deadline, true
		case l.Deadline != nil && l.Default == defaultNever && l.Recommend != "":
			a.Deadline = *l.Deadline
		case l.Deadline == nil && l.Default == "":
		default:
			return a, false
		}
		return a, true
	}
	checked := map[string]int{}
	for _, file := range []string{"todo-view/todo_view.json", "todo-counts/todo_counts.json"} {
		raw, err := os.ReadFile("../../../spec/fixtures/" + file)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Cases []struct {
				Name  string          `json:"name"`
				Input json.RawMessage `json:"input"`
			} `json:"cases"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		for _, c := range doc.Cases {
			var in struct {
				Todo  *struct{ Ask *listed }  `json:"todo"`
				Steps []struct{ Ask *listed } `json:"steps"`
				Todos []struct{ Ask *listed } `json:"todos"`
			}
			if err := json.Unmarshal(c.Input, &in); err != nil {
				t.Fatalf("%s %s: %v", file, c.Name, err)
			}
			asks := []*listed{}
			if in.Todo != nil {
				asks = append(asks, in.Todo.Ask)
			}
			for _, r := range append(in.Steps, in.Todos...) {
				asks = append(asks, r.Ask)
			}
			for _, l := range asks {
				if l == nil {
					continue
				}
				a, told := fixtureAsk(*l)
				if !told {
					continue
				}
				if want := askRisk(&a, nil); l.Risk != want {
					t.Errorf("%s %s: the ask lists risk %q, the manager gives %q", file, c.Name, l.Risk, want)
				}
				checked[c.Name]++
			}
		}
	}
	for _, name := range []string{"decision_safe_choice", "decision_irreversible_open", "decision_visible_to_others_open",
		"ranked_list"} {
		if checked[name] == 0 {
			t.Errorf("%s: no listed ask was checked", name)
		}
	}
}

// GET /api/v1/todos exposes ask.link for valid http(s) evidence links (PRs, MRs, docs, sessions), while omitting
// invalid schemes and redundant self-links to the to-do's own /todos/<id> page (#14659).
func TestTodoListExposesValidAskLinkAndFiltersSelfAndUnsafeLinks(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "Ready for MR review.")
	const mrURL = "https://gitlab.example.com/team/app/-/merge_requests/1468"
	reviewTask(t, s, todo.ID, map[string]any{
		"verdict":   "needs_user",
		"reason":    "Approve assigning reviewers on MR !1468?",
		"recommend": "Assign reviewers",
		"link":      mrURL,
	})
	ask := listedAsk(t, s, todo.ID)
	if ask == nil || ask["link"] != mrURL {
		t.Fatalf("ask.link = %v, want %q", ask["link"], mrURL)
	}

	// Self-link to the to-do's own page and invalid/unsafe schemes are omitted from GET /api/v1/todos.
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"self link with webBase", "https://hub.example.com/todos/" + todo.ID, ""},
		{"self link with trailing slash", "https://hub.example.com/todos/" + todo.ID + "/", ""},
		{"self link on another host", "https://old.example.com/todos/" + todo.ID, ""},
		{"other todo link kept", "https://hub.example.com/todos/todo_other", "https://hub.example.com/todos/todo_other"},
		{"agent session link kept", "https://hub.example.com/s/box/s1", "https://hub.example.com/s/box/s1"},
		{"github pr kept", "https://github.com/nwverse/persona/pull/14659", "https://github.com/nwverse/persona/pull/14659"},
		{"javascript scheme rejected", "javascript:alert(1)", ""},
		{"file scheme rejected", "file:///etc/passwd", ""},
		{"bare scheme rejected", "https://", ""},
		{"plain text rejected", "not a url", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s.accept.mu.Lock()
			s.accept.asks[todo.ID].Link = tc.raw
			s.accept.mu.Unlock()
			gotAsk := listedAsk(t, s, todo.ID)
			got, present := gotAsk["link"]
			if tc.want == "" {
				if present {
					t.Fatalf("link %q should be omitted, got %v", tc.raw, got)
				}
			} else if got != tc.want {
				t.Fatalf("link %q: got %v, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

func TestUserCheckSummaryAndEvidenceOnListAndBrief(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "The itinerary is ready.")
	const summary = "Both train connections leave time for transfers."
	const link = "https://docs.example.com/itinerary"
	reviewTask(t, s, todo.ID, map[string]any{
		"verdict": "accepted", "user_check": true, "reason": "  " + summary + "  ",
		"note": "Reviewed the final itinerary.", "link": link, "checks": completeReviewChecks(todoNow(s, todo.ID)),
	})
	ask := listedAsk(t, s, todo.ID)
	if ask["summary"] != summary || ask["link"] != link || ask["outcome"] != nil {
		t.Fatalf("verification ask = %+v", ask)
	}
	needsBriefingModule(t)
	rows := s.briefBodyOf(s.briefFactsAt(t.Context(), time.Now(), ""), "Review").Accept
	if len(rows) != 1 || rows[0].Detail != summary || rows[0].EvidenceLink != link || rows[0].Verdict != ask["verdict"] {
		t.Fatalf("briefing acceptance = %+v", rows)
	}
}

func TestVerificationDetailsFromOlderAsks(t *testing.T) {
	const web = "https://hub.example.com"
	const verdict = "Done: please verify the result"
	w := awaitingAccept{todo: "trip", machine: "node", session: "worker",
		review: &TaskReview{Verdict: reviewAccepted, Note: "  The route is ready. "}}
	for _, tc := range []struct {
		reason, link, wantSummary, wantLink string
	}{
		{"", web + "/s/node/worker?tab=chat", "The route is ready.", ""},
		{" Checked both routes. ", web + "/todos/trip", "Checked both routes.", ""},
		{verdict, "https://example.com/team/trips/pull/7", "", "https://example.com/team/trips/pull/7"},
	} {
		w.ask = acceptAsk{Reason: tc.reason, Link: tc.link}
		if summary, link := w.verificationDetails(web, verdict); summary != tc.wantSummary || link != tc.wantLink {
			t.Errorf("details = %q, %q; want %q, %q", summary, link, tc.wantSummary, tc.wantLink)
		}
	}
	s, _, todo := acceptedForUserCheck(t)
	before := listedAsk(t, s, todo.ID)
	s.accept.mu.Lock()
	s.accept.asks[todo.ID].Reason = before["verdict"].(string)
	s.accept.mu.Unlock()
	ask := listedAsk(t, s, todo.ID)
	if ask["summary"] != nil || ask["link"] != nil || ask["asked_at"] != before["asked_at"] {
		t.Errorf("legacy ask with duplicate verdict: %+v", ask)
	}
}
