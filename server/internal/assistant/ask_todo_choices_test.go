package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Read the public payload rather than implementation-only fields, so the same regressions compile on the base.
type expiredQuestionView struct {
	Todo
	Ask *struct {
		Outcome   string           `json:"outcome"`
		Question  string           `json:"question"`
		AskedAt   time.Time        `json:"asked_at"`
		Questions []proto.Question `json:"questions"`
		Recommend string           `json:"recommend"`
		Options   []DecisionOption `json:"options"`
	} `json:"ask"`
}

func expiredQuestionPayload(e *askTodoEnv, id string) expiredQuestionView {
	e.t.Helper()
	var payload struct {
		Todos []expiredQuestionView `json:"todos"`
	}
	if err := json.Unmarshal(e.must("GET", "/api/v1/todos", "").Body.Bytes(), &payload); err != nil {
		e.t.Fatal(err)
	}
	for _, todo := range payload.Todos {
		if todo.ID == id {
			return todo
		}
	}
	e.t.Fatalf("question to-do %s missing from API", id)
	return expiredQuestionView{}
}

func answerExpiredQuestion(e *askTodoEnv, id string, at time.Time, answer map[string]any) int {
	e.t.Helper()
	body := map[string]any{"asked_at": at.Format(time.RFC3339Nano)}
	for k, v := range answer {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	return e.do("POST", "/api/v1/todos/"+id+"/accept", string(raw)).Code
}

func TestExpiredQuestionNeedsYouAndOriginalChoiceReturnsOnce(t *testing.T) {
	database := todoTestDB(t)
	e := newAskTodoEnv(t, database)
	ix, _ := e.expire()
	todo := e.onlyAskTodo()
	view := expiredQuestionPayload(e, todo.ID)
	if view.Ask == nil || view.Ask.Outcome != outcomeDecide || view.Ask.Question != ix.Questions[0].Question {
		t.Fatalf("expired owner question must have an answerable ask, not Not started: %+v", view)
	}
	if view.Origin != "" || len(view.Agents) != 0 || view.Supervision != nil {
		t.Fatalf("a question must not become an Agent task: %+v", view.Todo)
	}
	if len(view.Ask.Questions) != 1 || len(view.Ask.Questions[0].Options) != 2 ||
		view.Ask.Questions[0].Options[1].Label != "Wait" || view.Ask.Questions[0].Options[1].Description != "until the review is in" {
		t.Fatalf("original choices were lost: %+v", view.Ask)
	}
	if code := answerExpiredQuestion(e, todo.ID, view.Ask.AskedAt, map[string]any{"option": "Wait"}); code != http.StatusOK {
		t.Fatalf("inline original option: HTTP %d, want 200", code)
	}
	waitFor(t, "the answered question closes", func() bool { return e.get(todo.ID).DoneAt != nil })
	waitFor(t, "the accepted answer is delivered", func() bool { return len(e.told("c1x1x1")) == 1 })
	messages := e.told("c1x1x1")
	if len(messages) != 1 || !strings.Contains(messages[0], "Wait") || !strings.Contains(messages[0], ix.Questions[0].Question) {
		t.Fatalf("answer must reach its asking conversation once: %q", messages)
	}
	if len(e.told("m1")) != 0 {
		t.Fatalf("answer was sent to main instead of its original conversation: %q", e.told("m1"))
	}
	rows, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
	if err != nil || len(rows) != 1 || rows[0].Choice != "Wait" || rows[0].Source != "manual" {
		t.Fatalf("the owner's recorded decision: %+v, %v", rows, err)
	}
	if got := expiredQuestionPayload(e, todo.ID); got.Ask != nil {
		t.Fatalf("answered question still waits: %+v", got.Ask)
	}
	if code := answerExpiredQuestion(e, todo.ID, view.Ask.AskedAt, map[string]any{"option": "Wait"}); code != http.StatusConflict {
		t.Fatalf("replayed answer: HTTP %d, want 409", code)
	}
	if len(e.told("c1x1x1")) != 1 {
		t.Fatalf("replayed answer sent another message")
	}
	t.Logf("owner_question ask=1 original_options=%d manual_decisions=%d asker_messages=%d closed=true replay_http=409",
		len(view.Ask.Questions[0].Options), len(rows), len(messages))
}

func TestExpiredQuestionChoicesSurviveReloadAndLegacyHistory(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "persisted", true: "legacy_history"}[legacy], func(t *testing.T) {
			database := todoTestDB(t)
			e := newAskTodoEnv(t, database)
			ix, _ := expireRecorded(e, database, askTodoArgs)
			todo := e.onlyAskTodo()
			if legacy {
				var records []map[string]any
				if _, err := database.Setting(context.Background(), askTodosKey, &records); err != nil {
					t.Fatal(err)
				}
				for _, rec := range records {
					delete(rec, "question_details")
				}
				if err := database.PutSetting(context.Background(), askTodosKey, records); err != nil {
					t.Fatal(err)
				}
			}
			again := newAskTodoEnv(t, database)
			got := expiredQuestionPayload(again, todo.ID)
			if got.Ask == nil || len(got.Ask.Questions) != 1 || len(got.Ask.Questions[0].Options) != 2 ||
				got.Ask.Questions[0].Options[0].Label != ix.Questions[0].Options[0].Label {
				t.Fatalf("reload must preserve original question choices (legacy=%v): %+v", legacy, got)
			}
			if code := answerExpiredQuestion(again, todo.ID, got.Ask.AskedAt, map[string]any{"option": "Wait"}); code != http.StatusOK {
				t.Fatalf("answer after reload: HTTP %d", code)
			}
			waitFor(t, "late answer history", func() bool {
				rec := askRecord(t, database, ix.ID)
				return rec.Resolution != nil && rec.Resolution.Late
			})
			if rec := askRecord(t, database, ix.ID); rec.Resolution.Answers[ix.Questions[0].Question] != "Wait" || rec.Resolution.AnsweredBy != "" {
				t.Fatalf("not the owner's late answer: %+v", rec.Resolution)
			}
			t.Logf("reload legacy=%v original_options=%d answer_http=200 history_late=true", legacy, len(got.Ask.Questions[0].Options))
		})
	}
}

func TestExpiredQuestionRejectsStaleInvalidAndReadOnlyAnswers(t *testing.T) {
	database := todoTestDB(t)
	e := newAskTodoEnv(t, database)
	ix, _ := e.expire()
	todo := e.onlyAskTodo()
	for _, tc := range []struct {
		name   string
		at     time.Time
		answer map[string]any
		want   int
	}{
		{"stale", ix.CreatedAt.Add(time.Second), map[string]any{"option": "Wait"}, http.StatusConflict},
		{"unknown option", ix.CreatedAt, map[string]any{"option": "Invented choice"}, http.StatusBadRequest},
		{"missing answer", ix.CreatedAt, map[string]any{}, http.StatusBadRequest},
		{"wrong question", ix.CreatedAt, map[string]any{"answers": map[string]string{"Other question": "Wait"}}, http.StatusBadRequest},
		{"mixed forms", ix.CreatedAt, map[string]any{"option": "Wait", "answers": map[string]string{ix.Questions[0].Question: "Wait"}}, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code := answerExpiredQuestion(e, todo.ID, tc.at, tc.answer); code != tc.want {
				t.Fatalf("HTTP %d, want %d", code, tc.want)
			}
		})
	}
	body, _ := json.Marshal(map[string]any{"asked_at": ix.CreatedAt, "option": "Wait"})
	if w := e.doAs("readonly", "POST", "/api/v1/todos/"+todo.ID+"/accept", string(body)); w.Code != http.StatusForbidden {
		t.Fatalf("read-only answer: %d %s", w.Code, w.Body)
	}
	rows, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
	if err != nil || len(rows) != 0 || len(e.told("c1x1x1")) != 0 || e.get(todo.ID).DoneAt != nil {
		t.Fatalf("rejected answer mutated state: rows=%+v err=%v", rows, err)
	}
	e.must("PATCH", "/api/v1/todos/"+todo.ID, `{"done":true}`)
	if view := expiredQuestionPayload(e, todo.ID); view.Ask != nil {
		t.Fatalf("closed question must not wait: %+v", view.Ask)
	}
	if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, map[string]any{"option": "Wait"}); code != http.StatusConflict {
		t.Fatalf("closed question accepted an answer: HTTP %d", code)
	}
}

func TestExpiredQuestionSeveralAndMultipleSelectionStayWhole(t *testing.T) {
	database := todoTestDB(t)
	e := newAskTodoEnv(t, database)
	args := `{"why_owner":"priority","questions":[{"question":"Which work?","options":[{"label":"Docs"},{"label":"Tests"}]},` +
		`{"question":"Which clients?","multiSelect":true,"options":[{"label":"Web"},{"label":"Phone"}]}]}`
	ix, _ := expireRecorded(e, database, args)
	todo := e.onlyAskTodo()
	view := expiredQuestionPayload(e, todo.ID)
	if view.Ask == nil || len(view.Ask.Questions) != 2 || !view.Ask.Questions[1].MultiSelect {
		t.Fatalf("several/multi-select question shape lost: %+v", view)
	}
	if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, map[string]any{"answers": map[string]string{"Which work?": "Tests"}}); code != http.StatusBadRequest {
		t.Fatalf("partial answers must not close a multi-question to-do: HTTP %d", code)
	}
	answers := map[string]string{"Which work?": "Tests", "Which clients?": "Web, Phone"}
	if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, map[string]any{"answers": answers}); code != http.StatusOK {
		t.Fatalf("whole answer: HTTP %d", code)
	}
	waitFor(t, "the whole answer closes its to-do", func() bool { return e.get(todo.ID).DoneAt != nil })
	waitFor(t, "the whole answer is delivered", func() bool { return len(e.told("c1x1x1")) == 1 })
	if messages := e.told("c1x1x1"); len(messages) != 1 || !strings.Contains(messages[0], "Tests") || !strings.Contains(messages[0], "Web, Phone") {
		t.Fatalf("the whole answer must return in one message: %q", messages)
	}
	if rec := askRecord(t, database, ix.ID); rec.Resolution == nil || len(rec.Resolution.Answers) != 2 || rec.Resolution.Answers["Which clients?"] != "Web, Phone" {
		t.Fatalf("the history must retain both answers: %+v", rec.Resolution)
	}
	rows, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
	if err != nil || len(rows) != 2 || rows[0].Question != "Which work?" || rows[1].Question != "Which clients?" ||
		!rows[0].CreatedAt.Before(rows[1].CreatedAt) {
		t.Fatalf("the decision history must preserve the question order regardless of decision IDs: rows=%+v err=%v", rows, err)
	}
	t.Log("whole_form questions=2 answers=2 asker_messages=1 closed=true")
}

type expiredQuestionClaimStore struct {
	Store
	todoStore
	failClaim atomic.Bool
	failRead  atomic.Bool
}

func (s *expiredQuestionClaimStore) Setting(ctx context.Context, key string, value any) (bool, error) {
	if key == askTodosKey && s.failRead.Load() {
		return false, errors.New("synthetic question read failure")
	}
	return s.Store.Setting(ctx, key, value)
}

func (s *expiredQuestionClaimStore) PutSetting(ctx context.Context, key string, value any) error {
	if key == askTodosKey && s.failClaim.Load() {
		return errors.New("synthetic claim write failure")
	}
	return s.Store.PutSetting(ctx, key, value)
}

func TestExpiredQuestionFailedClaimIsRetryableNotAlreadyHandled(t *testing.T) {
	database := todoTestDB(t)
	base := newAskTodoEnv(t, database).s.store()
	store := &expiredQuestionClaimStore{Store: base, todoStore: base.(todoStore)}
	e := newAskTodoEnv(t, store)
	ix, _ := e.expire()
	todo := e.onlyAskTodo()
	store.failClaim.Store(true)
	if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, map[string]any{"option": "Wait"}); code != http.StatusServiceUnavailable {
		t.Fatalf("unwritten claim: HTTP %d, want retryable 503, not already-handled 409", code)
	}
	if got := expiredQuestionPayload(e, todo.ID); got.Ask == nil || got.DoneAt != nil || len(e.told("c1x1x1")) != 0 {
		t.Fatalf("a failed claim lost its owner question")
	}
	rows, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
	if err != nil || len(rows) != 0 {
		t.Fatalf("failed claim wrote decisions: %+v, %v", rows, err)
	}
	store.failClaim.Store(false)
	if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, map[string]any{"option": "Wait"}); code != http.StatusOK {
		t.Fatalf("retry after recovery: HTTP %d", code)
	}
	waitFor(t, "the recovered answer is delivered", func() bool { return len(e.told("c1x1x1")) == 1 })
	if len(e.told("c1x1x1")) != 1 {
		t.Fatalf("recovery did not return the answer once: %q", e.told("c1x1x1"))
	}
}

func TestExpiredQuestionLegacyRowsNeverInventARecommendation(t *testing.T) {
	for _, recommend := range []string{"", "Second", "Not an option"} {
		t.Run(recommend, func(t *testing.T) {
			e := newAskTodoEnv(t, nil)
			ix := proto.Interaction{ID: "ix_legacy_choices", MachineID: "server", SessionID: "c1x1x1", CreatedAt: time.Now().UTC(),
				Questions: []proto.Question{{Question: "Which next?", Recommend: recommend,
					Options: []proto.QuestionOption{{Label: "First"}, {Label: "Second"}}}}}
			todo, err := e.s.askExpired(context.Background(), ix)
			if err != nil {
				t.Fatal(err)
			}
			view := expiredQuestionPayload(e, todo.ID)
			if view.Ask == nil || len(view.Ask.Questions) != 1 || view.Ask.Questions[0].Options[0].Label != "First" {
				t.Fatalf("the original form must retain its order: %+v", view.Ask)
			}
			if recommend == "Second" {
				if view.Ask.Recommend != "Second" || len(view.Ask.Options) != 2 || view.Ask.Options[0].Label != "Second" {
					t.Fatalf("legacy rows require the genuine recommended option first: %+v", view.Ask)
				}
			} else if view.Ask.Recommend != "" || len(view.Ask.Options) != 0 {
				t.Fatalf("legacy rows must not mark an arbitrary first option recommended: %+v", view.Ask)
			}
		})
	}
}

func TestExpiredQuestionAnswerQueueSurvivesReload(t *testing.T) {
	database := todoTestDB(t)
	e := newAskTodoEnv(t, database)
	ix, _ := e.expire()
	todo := e.onlyAskTodo()
	e.sessions("server", proto.Session{SessionID: "c1x1x1", Assistant: true, Status: proto.StatusWorking})
	if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, map[string]any{"option": "Wait"}); code != http.StatusOK {
		t.Fatalf("queueing an answer behind the asking conversation: HTTP %d", code)
	}
	if e.get(todo.ID).DoneAt == nil || len(e.told("c1x1x1")) != 0 {
		t.Fatalf("queued answer must close its question but not interrupt the working conversation")
	}
	again := newAskTodoEnv(t, database)
	waitFor(t, "the queued answer after reload", func() bool { return len(again.told("c1x1x1")) == 1 })
	if view := expiredQuestionPayload(again, todo.ID); view.Ask != nil {
		t.Fatalf("an already queued answer reappeared in Needs you: %+v", view.Ask)
	}
	if code := answerExpiredQuestion(again, todo.ID, ix.CreatedAt, map[string]any{"option": "Wait"}); code != http.StatusConflict {
		t.Fatalf("replay after queue reload: HTTP %d", code)
	}
	if len(again.told("c1x1x1")) != 1 {
		t.Fatal("the queued answer was sent twice")
	}
	t.Log("queued_answer before_reload_messages=0 after_reload_messages=1 question_closed=true replay_http=409")
}

func TestExpiredQuestionUnavailableRecordIsNotAlreadyHandled(t *testing.T) {
	database := todoTestDB(t)
	e := newAskTodoEnv(t, database)
	ix, _ := e.expire()
	todo := e.onlyAskTodo()
	base := e.s.store()
	store := &expiredQuestionClaimStore{Store: base, todoStore: base.(todoStore)}
	store.failRead.Store(true)
	again := newAskTodoEnv(t, store)
	if code := answerExpiredQuestion(again, todo.ID, ix.CreatedAt, map[string]any{"option": "Wait"}); code != http.StatusServiceUnavailable {
		t.Fatalf("unreadable question: HTTP %d, want retryable 503", code)
	}
	if len(again.told("c1x1x1")) != 0 {
		t.Fatal("unreadable question sent an answer")
	}
	store.failRead.Store(false)
	if code := answerExpiredQuestion(again, todo.ID, ix.CreatedAt, map[string]any{"option": "Wait"}); code != http.StatusOK {
		t.Fatalf("answer after the record becomes readable: HTTP %d", code)
	}
}

func TestExpiredQuestionAcceptanceIsDurableBeforeInFlightDelivery(t *testing.T) {
	e := newAskTodoEnv(t, todoTestDB(t))
	ix, _ := e.expire()
	todo := e.onlyAskTodo()
	entered, finished := make(chan struct{}), make(chan struct{})
	gate, release := context.WithCancel(context.Background())
	var code atomic.Int32
	e.f.mu.Lock()
	e.f.beforeAssistantSend = func() {
		close(entered)
		<-gate.Done()
	}
	e.f.mu.Unlock()
	go func() {
		defer close(finished)
		code.Store(int32(answerExpiredQuestion(e, todo.ID, ix.CreatedAt, map[string]any{"option": "Wait"})))
	}()
	defer func() {
		release()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("the blocked answer did not finish after release")
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the answer never reached its relay")
	}
	inFlight := expiredQuestionPayload(e, todo.ID)
	if inFlight.Ask != nil || inFlight.DoneAt == nil {
		t.Errorf("an accepted answer must be terminal before its delivery completes: %+v", inFlight)
	}
	release()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the released answer did not finish")
	}
	waitFor(t, "the accepted answer finishes delivery", func() bool { return len(e.told("c1x1x1")) == 1 })
	if code.Load() != http.StatusOK || len(e.told("c1x1x1")) != 1 || e.get(todo.ID).DoneAt == nil {
		t.Fatalf("the original answer must still finish once: HTTP %d", code.Load())
	}
	t.Logf("in_flight ask_present=%t open=%t completed_http=%d asker_messages=%d",
		inFlight.Ask != nil, inFlight.DoneAt == nil, code.Load(), len(e.told("c1x1x1")))
}

func TestExpiredQuestionEmptyStructuredAnswersNeverTakeRecommendation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer map[string]any
		want   int
	}{
		{"empty object", map[string]any{"answers": map[string]string{}}, http.StatusBadRequest},
		{"omitted legacy answers", nil, http.StatusOK},
		{"null legacy answers", map[string]any{"answers": nil}, http.StatusOK},
		{"legacy option", map[string]any{"option": "Docs"}, http.StatusOK},
		{"complete structured answer", map[string]any{"answers": map[string]string{"Which next?": "Docs"}}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := todoTestDB(t)
			e := newAskTodoEnv(t, database)
			ix := proto.Interaction{ID: "ix_empty_answers", MachineID: "server", SessionID: "c1x1x1", CreatedAt: time.Now().UTC(),
				Questions: []proto.Question{{Question: "Which next?", Recommend: "Tests",
					Options: []proto.QuestionOption{{Label: "Docs"}, {Label: "Tests"}}}}}
			todo, err := e.s.askExpired(context.Background(), ix)
			if err != nil {
				t.Fatal(err)
			}
			if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, tc.answer); code != tc.want {
				t.Fatalf("HTTP %d, want %d", code, tc.want)
			}
			if tc.want == http.StatusOK {
				waitFor(t, "the complete accepted answer is delivered", func() bool { return len(e.told("c1x1x1")) == 1 })
			}
			rows, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == http.StatusBadRequest {
				if len(rows) != 0 || len(e.told("c1x1x1")) != 0 || e.get(todo.ID).DoneAt != nil ||
					expiredQuestionPayload(e, todo.ID).Ask == nil {
					t.Fatal("an empty answer recorded consent or lost the unanswered question")
				}
			} else if len(rows) != 1 || len(e.told("c1x1x1")) != 1 || e.get(todo.ID).DoneAt == nil {
				t.Fatal("a valid legacy or complete answer did not finish once")
			}
		})
	}
}
