package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A to-do's own asks answered through one route (todo_asks_routes.go, #10465): the shared cases of
// spec/fixtures/todo-asks replayed against the route, then what one call cannot show. All names and texts are made up.

// The made-up values of spec/fixtures/todo-asks that a replay puts the hub's own in place of (its README, "Format").
const (
	fixtureRound   = "2026-11-10T09:00:00Z"
	fixtureStep    = "todo_step"
	fixtureReceipt = "dismiss_receipt"
)

// todoAskCase is a case of spec/fixtures/todo-asks.
type todoAskCase struct {
	Name  string
	Input struct {
		State  string
		Ask    string
		Answer json.RawMessage
	}
	Expected struct {
		Status   int
		Code     string
		Answered *assistantwire.TodoAskAnswered
		Row      bool
		Sent     *bool
		Started  *bool
		Waits    *bool
	}
}

func todoAskCases(t *testing.T) []todoAskCase {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "fixtures", "todo-asks", "answers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version int
		Cases   []todoAskCase
	}
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Version != 1 || len(doc.Cases) == 0 {
		t.Fatalf("spec/fixtures/todo-asks/answers.json: version %d, %d cases, %v", doc.Version, len(doc.Cases), err)
	}
	seen := map[string]bool{}
	for _, c := range doc.Cases {
		if c.Name == "" || seen[c.Name] {
			t.Fatalf("case name %q is empty or not unique", c.Name)
		}
		seen[c.Name] = true
	}
	return doc.Cases
}

// askScene is a to-do on which what a state names waits.
type askScene struct {
	s      *Service
	f      *fakeHub
	row    string            // the to-do whose row the path names
	values map[string]string // the fixture's made-up values → the hub's
	own    string            // the id of the ask that waits, as the row lists it
}

// put is text with the scene's values in place of the fixture's made-up ones.
func (sc askScene) put(text string) string {
	for from, to := range sc.values {
		text = strings.ReplaceAll(text, from, to)
	}
	return text
}

// ryokanDecision is a complete needs_user review, the fixtures' "Which ryokan?", which can be undone.
func ryokanDecision() map[string]any {
	return map[string]any{"verdict": "needs_user", "why_owner": "priority", "reason": "Which ryokan?", "recommend": "Ryokan Hanami",
		"options": []map[string]string{
			{"label": "Ryokan Hanami", "leads_to": "Four minutes from Gion, with a late check-in"},
			{"label": "Kaede", "leads_to": "The cheapest, twenty minutes out"},
			{"label": "Tsuki", "leads_to": "The quietest, at the top of the budget"},
		},
		"cost_of_waiting": "Rooms for that weekend sell out"}
}

// viewRound is the time in the id of the ask to-do id's row lists with prefix: the round the replay answers.
func viewRound(t *testing.T, s *Service, id, prefix string) string {
	t.Helper()
	for _, ask := range rowAsks(t, s, id) {
		if round, ok := strings.CutPrefix(ask, prefix); ok {
			return round
		}
	}
	t.Fatalf("to-do %s lists no %s ask: %v", id, prefix, rowAsks(t, s, id))
	return ""
}

// todoAskScene sets up what state names (spec/fixtures/todo-asks/README.md "States").
func todoAskScene(t *testing.T, state string) askScene {
	t.Helper()
	ctx := context.Background()
	if state == "failed_start" {
		s, f := launcherRetryHub(t)
		todo, _ := failOnWork(t, s, f, errDiskFull)
		sc := askScene{s: s, f: f, row: todo.ID, values: map[string]string{fixtureRound: viewRound(t, s, todo.ID, askRetryPrefix)}}
		sc.own = askRetryPrefix + sc.values[fixtureRound]
		return sc
	}
	s, f, todo := newDecisionAnswerServiceOn(t, todoTestDB(t))
	sc := askScene{s: s, f: f, row: todo.ID, values: map[string]string{}}
	if state == "cannot" {
		if _, err := s.acceptAskLocked(todo.ID); err != nil { // the book is read before it is written
			t.Fatal(err)
		}
		asked := time.Now().UTC()
		s.accept.mu.Lock()
		s.accept.asks[todo.ID] = &acceptAsk{Conclusion: true, Outcome: outcomeCannot, NoCard: true, AskedAt: asked,
			Reason: "The ryokan takes no bookings from abroad"}
		s.saveAcceptLocked(ctx)
		s.accept.mu.Unlock()
		sc.values[fixtureRound] = viewRound(t, s, todo.ID, askDecisionPrefix)
		sc.own = askDecisionPrefix + sc.values[fixtureRound]
		return sc
	}
	waitsForReview(t, s, f, todo.ID, "Three ryokan fit the dates.")
	if _, err := reviewErr(s, todo.ID, ryokanDecision()); err != nil {
		t.Fatal(err)
	}
	sc.values[fixtureRound] = viewRound(t, s, todo.ID, askDecisionPrefix)
	sc.own = askDecisionPrefix + sc.values[fixtureRound]
	switch state {
	case "decision":
	case "decision_no_agent":
		f.setSessions("box")
	case "decision_no_origin", "decision_no_origin_no_agent":
		s.todos.mu.Lock()
		s.todos.todos[todo.ID].Origin = ""
		s.todos.mu.Unlock()
		if state == "decision_no_origin_no_agent" {
			f.setSessions("box")
		}
	case "decision_declined":
		r := currentDecisionReview(todoNow(s, todo.ID))
		review, err := s.changeDecisionDismissal(ctx, todo.ID, r.ReviewAt, "", false)
		if err != nil {
			t.Fatal(err)
		}
		sc.values[fixtureReceipt] = review.DismissalID
	case "step_decision":
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/todos", strings.NewReader(`{"title":"Trip to Kyoto"}`)))
		var made struct{ Todo Todo }
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &made) != nil || made.Todo.ID == "" {
			t.Fatalf("the to-do: %d %s", rec.Code, rec.Body)
		}
		s.todos.mu.Lock()
		s.todos.todos[todo.ID].PartOf = made.Todo.ID
		s.todos.mu.Unlock()
		sc.row, sc.values[fixtureStep] = made.Todo.ID, todo.ID
		sc.own = askStepPrefix + todo.ID + "/" + sc.own
	default:
		t.Fatalf("no state %q", state)
	}
	return sc
}

// answerTodoAsk posts body to the answer route of ask (one path segment, encoded) on to-do row's row.
func answerTodoAsk(s *Service, row, ask, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	path := "/api/v1/todos/" + url.PathEscape(row) + "/asks/" + url.PathEscape(ask) + "/answer"
	s.ServeHTTP(rec, httptest.NewRequest("POST", path, strings.NewReader(body)))
	return rec
}

// rowAsks are the ids of the asks to-do id's row lists in its view.
func rowAsks(t *testing.T, s *Service, id string) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/todos/"+id, nil))
	var out struct{ Todo listedTodo }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.Todo.View == nil {
		t.Fatalf("GET /api/v1/todos/%s: %d %s", id, rec.Code, rec.Body)
	}
	var ids []string
	for _, a := range out.Todo.View.Asks {
		ids = append(ids, a.ID)
	}
	return ids
}

// Every case of spec/fixtures/todo-asks: the status and code, the reply (its row the to-do's as the item route reads
// it), whether the Agent heard anything or a start went out, and whether the row still lists the ask that waited.
func TestTodoAskAnswerFixtures(t *testing.T) {
	for _, c := range todoAskCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			sc := todoAskScene(t, c.Input.State)
			ask, body := sc.put(c.Input.Ask), sc.put(string(c.Input.Answer))
			sent, starts := len(sentToAgent(sc.f)), len(startsOf(sc.f))
			rec := answerTodoAsk(sc.s, sc.row, ask, body)
			if rec.Code != c.Expected.Status {
				t.Fatalf("%s %s: %d %s, want %d", ask, body, rec.Code, rec.Body, c.Expected.Status)
			}
			var reply map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
				t.Fatalf("reply: %s", rec.Body)
			}
			if c.Expected.Code != "" && string(reply["code"]) != `"`+c.Expected.Code+`"` {
				t.Fatalf("code %s, want %s: %s", reply["code"], c.Expected.Code, rec.Body)
			}
			var row struct{ ID string }
			if c.Expected.Row || c.Expected.Answered != nil {
				if err := json.Unmarshal(reply["todo"], &row); err != nil || row.ID != sc.row {
					t.Fatalf("the reply's row is not to-do %s's: %s", sc.row, rec.Body)
				}
			}
			if want := c.Expected.Answered; want != nil {
				var got assistantwire.TodoAskAnswered
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				if got.Ask != sc.put(want.Ask) || got.Outcome != want.Outcome || got.Delivered != want.Delivered ||
					(got.Receipt != "") != (want.Receipt != "") {
					t.Fatalf("reply %s, want %+v", rec.Body, *want)
				}
			}
			if want := c.Expected.Sent; want != nil && (len(sentToAgent(sc.f)) > sent) != *want {
				t.Fatalf("sent to the Agent: %q, want sent %v", sentToAgent(sc.f)[sent:], *want)
			}
			if want := c.Expected.Started; want != nil {
				if *want {
					waitFor(t, "the start", func() bool { return len(startsOf(sc.f)) > starts })
				} else if waitFor(t, "the starts to end", func() bool { return sc.s.taskStarting.Load() == 0 }); len(startsOf(sc.f)) != starts {
					t.Fatalf("%d starts after the answer", len(startsOf(sc.f))-starts)
				}
			}
			if want := c.Expected.Waits; want != nil {
				if got := slices.Contains(rowAsks(t, sc.s, sc.row), sc.own); got != *want {
					t.Fatalf("the row lists %v; want %s listed: %v", rowAsks(t, sc.s, sc.row), sc.own, *want)
				}
			}
			sc.s.waitWorkEvents() // do not let the next case's database truncate an event still in flight
		})
	}
}

// Two answers to one decision at once, from two devices: one is taken and reaches the Agent once, the other is told
// it is stale; neither waits on the other's lock for good. Don't handle against an option: when the option goes first,
// Don't handle is stale; when Don't handle goes first, the option is the owner handling it after all and is taken
// (docs/assistant/todos.md "Don't handle and Later"). Either way the decision is answered once. -race runs it.
func TestTodoAskAnswersOnce(t *testing.T) {
	for _, c := range []struct {
		second  string
		bothMay bool // the second may be taken too, when it goes first
	}{
		{second: `{"option":"Tsuki"}`},
		{second: `{"text":"Tsuki, please"}`},
		{second: `{"outcome":"decline"}`, bothMay: true},
	} {
		t.Run(c.second, func(t *testing.T) {
			sc := todoAskScene(t, "decision")
			ask := sc.put("todo:decision@" + fixtureRound)
			sent := len(sentToAgent(sc.f))
			var wg sync.WaitGroup
			codes := make([]int, 2)
			for i, body := range []string{`{"option":"Kaede"}`, c.second} {
				wg.Go(func() { codes[i] = answerTodoAsk(sc.s, sc.row, ask, body).Code })
			}
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("two answers at once did not both end")
			}
			slices.Sort(codes)
			if codes[0] != http.StatusOK || codes[1] != http.StatusConflict && !(c.bothMay && codes[1] == http.StatusOK) {
				t.Fatalf("codes %v, want one 200 and one 409", codes)
			}
			if got := len(sentToAgent(sc.f)) - sent; got != 1 {
				t.Fatalf("the Agent heard %d answers", got)
			}
			rows, err := sc.s.todoDB().ListTodoDecisions(context.Background(), sc.row, true)
			if err != nil || len(rows) != 1 || slices.Contains(rowAsks(t, sc.s, sc.row), ask) {
				t.Fatalf("decisions %+v, %v; the row lists %v", rows, err, rowAsks(t, sc.s, sc.row))
			}
			sc.s.waitWorkEvents()
		})
	}
}

// The conversation route keeps its guards: words for another conversation's task are 409 route_no_task, and words
// no Agent can receive still take nothing there (the asks route records them), as before the asks route.
func TestConversationRouteAnswerUnchanged(t *testing.T) {
	sc := todoAskScene(t, "decision")
	post := func(conversation string) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]string{"todo_id": sc.row, "text": "Hanami", "asked_at": sc.values[fixtureRound]})
		rec := httptest.NewRecorder()
		sc.s.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/assistant/conversations/"+conversation+"/route", strings.NewReader(string(raw))))
		return rec
	}
	rec := post("another-conversation")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), i18n.Err("assistant-tasks.route_no_task").Error()) {
		t.Fatalf("another conversation: %d %s", rec.Code, rec.Body)
	}
	sc.f.setSessions("box")
	if rec := post("conv1"); rec.Code != http.StatusConflict {
		t.Fatalf("no Agent: %d %s", rec.Code, rec.Body)
	}
	rows, err := sc.s.todoDB().ListTodoDecisions(context.Background(), sc.row, true)
	if err != nil || len(rows) != 0 || !slices.Contains(rowAsks(t, sc.s, sc.row), "todo:decision@"+sc.values[fixtureRound]) {
		t.Fatalf("the conversation route took words no Agent received: %+v, %v", rows, err)
	}
	sc.s.waitWorkEvents()
}

// What the route refuses before it looks at any ask: a device that may not change to-dos, a body that is not one
// answer, an unknown to-do; and the ask is one path segment, a / in it encoded (an unencoded one matches no route).
func TestTodoAskAnswerRefusals(t *testing.T) {
	e := newTodoEnv(t, nil)
	id := e.create("Book the ferry").ID
	path := "/api/v1/todos/" + id + "/asks/todo:retry@" + fixtureRound + "/answer"
	for _, role := range []string{"read", roleAnswer} {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		r.Header.Set(RoleHeader, role)
		e.s.ServeHTTP(rec, r)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("role %s: %d", role, rec.Code)
		}
	}
	for _, body := range []string{`{"option":"a","response":"explain_first"}`, `{"text":"  "}`, `{"response":"later"}`,
		`{"outcome":"never"}`, `{"outcome":"undo"}`, `{"outcome":"decline","receipt":"dismiss_x"}`, `not json`,
		`{"text":"` + strings.Repeat("a", decisionTextMax+1) + `"}`} {
		if rec := answerTodoAsk(e.s, id, "todo:retry@"+fixtureRound, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	if rec := answerTodoAsk(e.s, "todo_none", "todo:retry@"+fixtureRound, `{}`); rec.Code != http.StatusNotFound ||
		!strings.Contains(rec.Body.String(), "todo_not_found") {
		t.Fatalf("an unknown to-do: %d %s", rec.Code, rec.Body)
	}
	for _, c := range []struct {
		path string
		code int
		want string
	}{
		{"/api/v1/todos/" + id + "/asks/run:box%2Fs1@" + fixtureRound + "/answer", http.StatusConflict, "answer_in_session"},
		{"/api/v1/todos/" + id + "/asks/step:todo_x%2Ftodo:decision@" + fixtureRound + "/answer", http.StatusNotFound, "ask_not_found"},
		{"/api/v1/todos/" + id + "/asks/todo:decision@yesterday/answer", http.StatusNotFound, "ask_not_found"},
		{"/api/v1/todos/" + id + "/asks/run:box/s1@" + fixtureRound + "/answer", http.StatusNotFound, ""},
	} {
		rec := httptest.NewRecorder()
		e.s.ServeHTTP(rec, httptest.NewRequest("POST", c.path, strings.NewReader(`{}`)))
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s: %d %s", c.path, rec.Code, rec.Body)
		}
	}
}

// Words no Agent can receive are still the owner's answer (delivered none): kept whole as a manual decision, and the
// supervisor handed them to pass on, on a to-do a conversation opened and on one none did.
func TestTodoAskWordsNoAgentRecorded(t *testing.T) {
	for _, state := range []string{"decision_no_agent", "decision_no_origin_no_agent"} {
		t.Run(state, func(t *testing.T) {
			sc := todoAskScene(t, state)
			// The handoff skips the scene's one-hour gather (#17099): hold the digest timer so it is still pending
			// when read below, instead of already told to the main conversation.
			sc.s.stopSuperviseTimers()
			asked := askOf(sc.s, sc.row).AskedAt
			const words = "Hanami, but only with a late check-in"
			rec := answerTodoAsk(sc.s, sc.row, sc.own, `{"text":"`+words+`"}`)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"delivered":"none"`) {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			rows, err := sc.s.todoDB().ListTodoDecisions(context.Background(), sc.row, true)
			if err != nil || len(rows) != 1 || rows[0].Choice != words || rows[0].Source != db.DecisionManual {
				t.Fatalf("decisions %+v, %v", rows, err)
			}
			sc.s.supervise.mu.Lock()
			g, pending := sc.s.supervise.pending[sc.row+"|"+sigIdle]
			sc.s.supervise.mu.Unlock()
			if !pending || g.Key != "undelivered|"+asked.UTC().Format(time.RFC3339Nano) || !strings.Contains(g.Detail, words) {
				t.Fatalf("the supervisor was not handed the words: %+v", g)
			}
			sc.s.waitWorkEvents()
		})
	}
}

// Don't handle through the route answers a receipt the route's Undo takes: the ask leaves the row, then comes back.
func TestTodoAskDeclineThenUndo(t *testing.T) {
	sc := todoAskScene(t, "decision")
	rec := answerTodoAsk(sc.s, sc.row, sc.own, `{"outcome":"decline"}`)
	var declined assistantwire.TodoAskAnswered
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &declined) != nil ||
		declined.Outcome != assistantwire.TodoAskOutcomeDeclined || declined.Receipt == "" {
		t.Fatalf("decline: %d %s", rec.Code, rec.Body)
	}
	if slices.Contains(rowAsks(t, sc.s, sc.row), sc.own) {
		t.Fatalf("declined, the row still lists %s", sc.own)
	}
	undo, _ := json.Marshal(assistantwire.TodoAskAnswer{Outcome: assistantwire.TodoAskAnswerOutcomeUndo, Receipt: declined.Receipt})
	rec = answerTodoAsk(sc.s, sc.row, sc.own, string(undo))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"outcome":"undone"`) ||
		!slices.Contains(rowAsks(t, sc.s, sc.row), sc.own) {
		t.Fatalf("undo: %d %s; the row lists %v", rec.Code, rec.Body, rowAsks(t, sc.s, sc.row))
	}
	sc.s.waitWorkEvents()
}

// Don't handle from a screen that is behind never declines a newer review whose ask is still on its way: a review
// written after the ask was asked is another question, so the answer is stale and nothing is dismissed.
func TestTodoAskDeclineNewerReview(t *testing.T) {
	sc := todoAskScene(t, "decision")
	ctx := context.Background()
	if _, _, err := sc.s.updateSupervision(ctx, sc.row, func(v *Supervision) bool {
		r := v.Reviews[len(v.Reviews)-1]
		r.At = time.Now().UTC()
		v.Reviews = append(v.Reviews, r)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if rec := answerTodoAsk(sc.s, sc.row, sc.own, `{"outcome":"decline"}`); rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), "ask_stale") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if dismissed, err := sc.s.decisionDismissed(ctx, todoNow(sc.s, sc.row)); err != nil || dismissed {
		t.Fatalf("the newer review was dismissed: %v, %v", dismissed, err)
	}
	sc.s.waitWorkEvents()
}

// A conclusion that says the work cannot be done, come after a review that waits on the owner, takes Don't handle and
// its Undo like a decision: found among the asks the view lists while the to-do's action lock is held, which must not
// wait on itself.
func TestTodoAskDeclineCannotWithReview(t *testing.T) {
	sc := todoAskScene(t, "decision")
	s := sc.s
	s.accept.mu.Lock()
	s.accept.asks[sc.row] = &acceptAsk{Conclusion: true, Outcome: outcomeCannot, NoCard: true, AskedAt: time.Now().UTC(),
		Reason: "The ryokan takes no bookings from abroad"}
	s.saveAcceptLocked(context.Background())
	s.accept.mu.Unlock()
	own := askDecisionPrefix + viewRound(t, s, sc.row, askDecisionPrefix)
	if own == sc.own {
		t.Fatalf("the row still lists the decision's ask %s", own)
	}
	rec := answerTodoAsk(s, sc.row, own, `{"outcome":"decline"}`)
	var declined assistantwire.TodoAskAnswered
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &declined) != nil ||
		declined.Outcome != assistantwire.TodoAskOutcomeDeclined || declined.Receipt == "" {
		t.Fatalf("decline: %d %s", rec.Code, rec.Body)
	}
	if slices.Contains(rowAsks(t, s, sc.row), own) {
		t.Fatalf("declined, the row still lists %s", own)
	}
	undo, _ := json.Marshal(assistantwire.TodoAskAnswer{Outcome: assistantwire.TodoAskAnswerOutcomeUndo, Receipt: declined.Receipt})
	if rec := answerTodoAsk(s, sc.row, own, string(undo)); rec.Code != http.StatusOK || !slices.Contains(rowAsks(t, s, sc.row), own) {
		t.Fatalf("undo: %d %s; the row lists %v", rec.Code, rec.Body, rowAsks(t, s, sc.row))
	}
	s.waitWorkEvents()
}

// A decision handed back to its supervisor to recheck its facts (a day old) has left the view, so every answer the
// route takes for it is stale and nothing reaches the Agent: the route reads the same rounds the view does.
func TestTodoAskRecheckRoundIsStale(t *testing.T) {
	for _, body := range []string{`{"option":"Kaede"}`, `{"option":"Ryokan Hanami"}`, `{"text":"Tsuki, please"}`,
		`{"response":"explain_first"}`, `{"outcome":"decline"}`, `{"outcome":"later"}`} {
		t.Run(body, func(t *testing.T) {
			sc := todoAskScene(t, "decision")
			sc.s.sweepDecisionAges(time.Now().Add(decisionRecheckAfter + time.Minute))
			if a := askOf(sc.s, sc.row); a == nil || a.RecheckAt.IsZero() {
				t.Fatalf("the round was not handed back: %+v", a)
			}
			sent := len(sentToAgent(sc.f))
			if rec := answerTodoAsk(sc.s, sc.row, sc.own, body); rec.Code != http.StatusConflict ||
				!strings.Contains(rec.Body.String(), "ask_stale") {
				t.Fatalf("%d %s", rec.Code, rec.Body)
			}
			if got := len(sentToAgent(sc.f)) - sent; got != 0 {
				t.Fatalf("the Agent heard %d answers", got)
			}
			sc.s.waitWorkEvents()
		})
	}
}

// Two Retries at once, from two devices: one starts the to-do's Agent again, the other is told it is stale, and one
// start goes out. -race runs it.
func TestTodoAskRetriesOnce(t *testing.T) {
	sc := todoAskScene(t, "failed_start")
	starts := len(startsOf(sc.f))
	var wg sync.WaitGroup
	codes := make([]int, 2)
	for i := range codes {
		wg.Go(func() { codes[i] = answerTodoAsk(sc.s, sc.row, sc.own, `{}`).Code })
	}
	wg.Wait()
	slices.Sort(codes)
	if codes[0] != http.StatusOK || codes[1] != http.StatusConflict {
		t.Fatalf("codes %v, want one 200 and one 409", codes)
	}
	waitFor(t, "the start", func() bool { return len(startsOf(sc.f)) > starts })
	waitFor(t, "the starts to end", func() bool { return sc.s.taskStarting.Load() == 0 })
	if n := len(startsOf(sc.f)) - starts; n != 1 {
		t.Fatalf("%d starts", n)
	}
}

// A Retry answers only the failure its ask names, even when the to-do changes after the route found that failure and
// before the start reads it again: reopened by another start, whose agent the model service then cut off, it is not
// told to go on; failed anew, it is not started again. Either way the answer is stale (409 ask_stale with the row) and
// nothing goes out.
func TestTodoAskRetryChangedMeanwhileIsStale(t *testing.T) {
	for _, c := range []struct {
		name   string
		cut    bool // the to-do's agent s1 on box is cut off by the model service
		change func(todo *Todo)
	}{
		{"reopened", true, func(todo *Todo) {
			todo.DoneAt = nil
			todo.Agents = append(todo.Agents, TodoAgent{MachineID: "box", SessionID: "s1"})
		}},
		{"failed anew", false, func(todo *Todo) {
			later := todo.DoneAt.Add(time.Minute)
			todo.DoneAt = &later
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			sc := todoAskScene(t, "failed_start")
			sent, starts := len(sentToAgent(sc.f)), len(startsOf(sc.f))
			changed := false
			sc.s.askRetryHook = func(id string) { // the route found the failure the ask names
				if c.cut {
					cut := apiErrorTurn("s1", "API Error: 529 overloaded")
					cut.MachineID = "box"
					sc.f.setSessions("box", cut)
				}
				sc.s.todos.mu.Lock()
				c.change(sc.s.todos.todos[id])
				sc.s.todos.mu.Unlock()
				changed = true
			}
			rec := answerTodoAsk(sc.s, sc.row, sc.own, `{}`)
			var reply struct {
				Code string
				Todo struct{ ID string }
			}
			if !changed || rec.Code != http.StatusConflict || json.Unmarshal(rec.Body.Bytes(), &reply) != nil ||
				reply.Code != "ask_stale" || reply.Todo.ID != sc.row {
				t.Fatalf("changed %v: %d %s", changed, rec.Code, rec.Body)
			}
			waitFor(t, "the starts to end", func() bool { return sc.s.taskStarting.Load() == 0 })
			if got := sentToAgent(sc.f)[sent:]; len(got) != 0 {
				t.Fatalf("the Agent was told %q", got)
			}
			if n := len(startsOf(sc.f)) - starts; n != 0 {
				t.Fatalf("%d starts", n)
			}
		})
	}
}

// A to-do that closes while the owner's words are on their way is stale (409 ask_stale with the row), not 502 "answer
// again": the send's own guard saw the ask end, and nothing reached the Agent.
func TestTodoAskClosedWhileSendingIsStale(t *testing.T) {
	sc := todoAskScene(t, "decision")
	sent := len(sentToAgent(sc.f))
	sc.f.mu.Lock()
	sc.f.afterList = map[string]func(){"box": func() { // the to-do's agents were just read
		now := time.Now()
		sc.s.todos.mu.Lock()
		sc.s.todos.todos[sc.row].DoneAt = &now
		sc.s.todos.mu.Unlock()
	}}
	sc.f.mu.Unlock()
	ask, err := sc.s.ownAskOf(sc.row, sc.own)
	if err != nil {
		t.Fatal(err)
	}
	var out assistantwire.TodoAskAnswered
	if err := sc.s.answerDecision(listHooked(context.Background()), ask, assistantwire.TodoAskAnswer{Text: "Tsuki, please"}, &out); !errors.Is(err, errAskStale) {
		t.Fatalf("closed during the send: %v", err)
	}
	if got := len(sentToAgent(sc.f)) - sent; got != 0 {
		t.Fatalf("the Agent heard %d answers", got)
	}
	sc.s.waitWorkEvents()
}

// What the route answers for what its callees refuse: the retry route's 409s only for a failed start it did not start
// again (a decision's words that found the computer offline are the accept route's 502), a node's own refusal 423 with
// its code, the store 503, any other failure that typed nothing 502 accept_not_taken, each saying delivered "none";
// words that may be with the Agent, or are, 502 send_in_doubt, whatever the failure underneath (a node's coded answer
// that says it is not sure, a to-do gone once they were typed). A failed start's retry starts nothing before it answers.
func TestTodoAskAnswerErrorCodes(t *testing.T) {
	e := newTodoEnv(t, nil)
	for _, c := range []struct {
		err       error
		code      int
		want      string
		delivered string
	}{
		{machineOffline("box"), http.StatusBadGateway, "accept_not_taken", "none"},
		{startNotRetried{machineOffline("box")}, http.StatusConflict, "", "none"},
		{startNotRetried{errTaskNotRetryable}, http.StatusConflict, "", "none"},
		{startNotRetried{errors.New("the plan is gone")}, http.StatusBadGateway, "accept_not_taken", "none"},
		{startNotRetried{context.DeadlineExceeded}, http.StatusBadGateway, "accept_not_taken", "none"},
		{&rpc.Error{Code: "dialog_open", Message: "a dialog is open"}, http.StatusLocked, "dialog_open", "none"},
		{&rpc.Error{Code: "held_full", Message: "too many held"}, http.StatusLocked, "held_full", "none"},
		{errAcceptStore, http.StatusServiceUnavailable, "", "none"},
		{fmt.Errorf("%w: %w", errAcceptStore, context.DeadlineExceeded), http.StatusServiceUnavailable, "", "none"},
		{errAskStale, http.StatusConflict, "ask_stale", "none"},
		{errNotDeclinable, http.StatusConflict, "not_declinable", "none"},
		{errAnswerNeeded, http.StatusBadRequest, "", "none"},
		{db.ErrTodoNotFound, http.StatusNotFound, "todo_not_found", "none"},
		{errors.New("the link dropped"), http.StatusBadGateway, "accept_not_taken", "none"},
		{context.DeadlineExceeded, http.StatusBadGateway, codeSendInDoubt, "unknown"},
		{rpc.ErrClosed, http.StatusBadGateway, codeSendInDoubt, "unknown"},
		{&rpc.Error{Code: "peer_unconfirmed", Message: "the inbox read it", Data: json.RawMessage(`{"delivered":"unknown"}`)},
			http.StatusBadGateway, codeSendInDoubt, "unknown"},
		{wordsSent{db.ErrTodoNotFound}, http.StatusBadGateway, codeSendInDoubt, "yes"},
	} {
		rec := httptest.NewRecorder()
		e.s.writeAnswerError(rec, httptest.NewRequest("POST", "/", nil), "todo_x", c.err)
		var body struct{ Code, Delivered string }
		if rec.Code != c.code || json.Unmarshal(rec.Body.Bytes(), &body) != nil || c.want != "" && body.Code != c.want || body.Delivered != c.delivered {
			t.Errorf("%v: %d %s, want %d %q delivered %q", c.err, rec.Code, rec.Body, c.code, c.want, c.delivered)
		}
	}
}

// The manager tells the hub it answers a to-do's asks.
func TestTodoAsksBusHelloCapability(t *testing.T) {
	b := NewBusHub(BusOptions{Hub: "http://127.0.0.1:1", Token: "t"})
	p, _ := b.client.HelloParams().(map[string]any)
	if caps, _ := p["capabilities"].([]string); !slices.Contains(caps, assistantwire.CapabilityTodoAsks) {
		t.Fatalf("the manager does not name %s: %v", assistantwire.CapabilityTodoAsks, caps)
	}
}
