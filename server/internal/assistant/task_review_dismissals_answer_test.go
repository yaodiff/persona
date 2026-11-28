package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// These tests inspect decisions and queued signals, not digest delivery. Keep the digest timer out of the fixture's
// short-lived reachability settings so it cannot outlive a subtest and race their restoration.
func newDecisionAnswerServiceOn(t *testing.T, st Store) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f := newQuietServiceOn(t, st)
	s.superviseTimes = superviseTimings{gather: time.Hour, every: time.Hour, urgent: time.Hour}
	f.judge = `{"verdict":"done","reason":"The example change is reviewed."}`
	args := json.RawMessage(`{"tasks":[{"title":"Fix the example UI","prompt":"Fix the example UI and add regression tests.","machine":"box","cwd":"/code/app","readonly":true,"why":"The example project is on this computer.","done_when":["The change is reviewed","Regression tests pass"],"expect_minutes":30}]}`)
	if _, err := s.toolTaskPlan(context.Background(), "conv1", args); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the task agent link", func() bool { return len(onlyTodo(s).Agents) == 1 })
	return s, f, onlyTodo(s)
}

func assertAnswerDismissal(t *testing.T, s *Service, st Store, id string, want taskReviewDismissal) {
	t.Helper()
	s.reviewDismissals.mu.Lock()
	got := s.reviewDismissals.byTodo[id]
	s.reviewDismissals.mu.Unlock()
	var saved map[string]taskReviewDismissal
	if _, err := st.Setting(context.Background(), taskReviewDismissalsKey, &saved); err != nil {
		t.Fatal(err)
	}
	if got != want || saved[id] != want {
		t.Fatalf("dismissal changed: memory %+v, stored %+v; want %+v", got, saved[id], want)
	}
}

func dismissalAnswerEnv(t *testing.T) (*todoEnv, *memStore, taskReviewDismissal) {
	t.Helper()
	at := time.Now().UTC().Add(-time.Hour)
	st := &memStore{}
	e := dismissalEnv(t, st, at, true)
	feed := &planMemStore{memStore: st}
	e.s.setStore(func() Store { return feed })
	e.s.todos.todos["task"].Origin = "conversation"
	e.s.todos.todos["task"].Agents = []TodoAgent{{MachineID: "server", SessionID: "worker"}}
	e.f.sessions["server"] = []proto.Session{{MachineID: "server", SessionID: "worker", Status: proto.StatusIdle}}
	dismissReview(t, e, at)
	return e, st, e.s.reviewDismissals.byTodo["task"]
}

func TestTaskReviewDismissalInvalidAcceptanceKeepsIt(t *testing.T) {
	for _, kind := range []string{"missing ask", "zero ask", "accepted", "old card", "no recommendation", "reopened", "closed", "ended review", "ask read failure"} {
		t.Run(kind, func(t *testing.T) {
			e, st, before := dismissalAnswerEnv(t)
			a := e.s.accept.asks["task"]
			card := ""
			switch kind {
			case "missing ask":
				delete(e.s.accept.asks, "task")
			case "zero ask":
				a.AskedAt = time.Time{}
			case "accepted":
				a.AcceptedAt = time.Now()
			case "old card":
				a.MessageID, a.Messages, card = "current", []string{"current"}, "old"
			case "no recommendation":
				a.Recommend = ""
			case "reopened":
				at := a.AskedAt.Add(time.Second)
				e.s.todos.todos["task"].ReopenedAt = &at
			case "closed":
				at := time.Now()
				e.s.todos.todos["task"].DoneAt = &at
			case "ended review":
				e.s.todos.todos["task"].Supervision.State = reviewRework
			case "ask read failure":
				e.s.accept.loaded = false
				st.failKey(acceptanceKey, errors.New("ask read failed"))
			}
			result, err := e.s.acceptTry(context.Background(), "task", card)
			if result.taken || kind == "ask read failure" && !errors.Is(err, errAcceptStore) || kind != "ask read failure" && err != nil {
				t.Fatalf("invalid acceptance: %v, %v", result, err)
			}
			assertAnswerDismissal(t, e.s, st, "task", before)
			if len(sentToAgent(e.f)) != 0 {
				t.Fatal("invalid acceptance reached the agent")
			}
		})
	}
}

func TestTaskReviewDismissalInvalidAnswersKeepIt(t *testing.T) {
	for _, entry := range []string{"option", "owner response", "already told"} {
		for _, kind := range []string{"invalid value", "stale ask", "accepted", "closed", "ended review"} {
			t.Run(entry+"/"+kind, func(t *testing.T) {
				e, st, before := dismissalAnswerEnv(t)
				a := e.s.accept.asks["task"]
				asked, label, response := a.AskedAt.Format(timeKeyLayout), "Option B", respondExplainFirst
				switch kind {
				case "invalid value":
					label, response = "Not an option", "Not a response"
				case "stale ask":
					if entry == "already told" {
						at := a.AskedAt.Add(time.Second)
						e.s.todos.todos["task"].ReopenedAt = &at
					} else {
						asked = a.AskedAt.Add(-time.Second).Format(timeKeyLayout)
					}
				case "accepted":
					a.AcceptedAt = time.Now()
				case "closed":
					at := time.Now()
					e.s.todos.todos["task"].DoneAt = &at
				case "ended review":
					e.s.todos.todos["task"].Supervision.State = reviewRework
				}
				var ok bool
				var err error
				switch entry {
				case "option":
					var result acceptResult
					result, err = e.s.chooseOption(context.Background(), "task", asked, label)
					ok = result.taken
				case "owner response":
					ok, err = e.s.ownerCannotAnswer(context.Background(), "task", asked, response)
				case "already told":
					ok, err = e.s.decisionTold(context.Background(), "task", label, "m", "conv", "the owner's words")
				}
				wantOptionError := kind == "invalid value" && entry != "owner response"
				if ok || wantOptionError && !errors.Is(err, errDecisionOption) || !wantOptionError && err != nil {
					t.Fatalf("invalid answer: %v, %v", ok, err)
				}
				assertAnswerDismissal(t, e.s, st, "task", before)
			})
		}
	}
}

func dismissalAnswer(s *Service, entry string) (bool, error) {
	ctx := context.Background()
	switch entry {
	case "accept":
		result, err := s.acceptTry(ctx, "task", "")
		return result.taken, err
	case "recommended option":
		result, err := s.chooseOption(ctx, "task", "", "Option A")
		return result.taken, err
	case "alternative option":
		result, err := s.chooseOption(ctx, "task", "", "Option B")
		return result.taken, err
	case "owner response":
		return s.ownerCannotAnswer(ctx, "task", "", respondNotAsked)
	default:
		return s.decisionTold(ctx, "task", "Option B", "m", "conv", "the owner's words")
	}
}

func TestTaskReviewDismissalAnswerDelivery(t *testing.T) {
	for _, entry := range []string{"accept", "recommended option", "alternative option", "owner response", "already told"} {
		for _, failure := range []string{"", "dismissal read", "dismissal write", "delivery"} {
			if failure == "delivery" && (entry == "owner response" || entry == "already told") {
				continue // These record the answer without delivering it to the task agent.
			}
			t.Run(entry+"/"+failure, func(t *testing.T) {
				e, st, before := dismissalAnswerEnv(t)
				switch failure {
				case "dismissal read":
					e.s.reviewDismissals.loaded = false
					st.failKey(taskReviewDismissalsKey, errors.New("read failed"))
				case "dismissal write":
					st.setFailPut(errors.New("write failed"))
				case "delivery":
					e.f.failSend = errors.New("send refused")
				}
				ok, err := dismissalAnswer(e.s, entry)
				if failure != "" {
					if ok || err == nil || failure != "delivery" && !errors.Is(err, errAcceptStore) {
						t.Fatalf("failed answer: %v, %v", ok, err)
					}
					st.failKey(taskReviewDismissalsKey, nil)
					st.setFailPut(nil)
					assertAnswerDismissal(t, e.s, st, "task", before)
					if len(sentToAgent(e.f)) != 0 {
						t.Fatal("a failed answer was delivered")
					}
					if a, _ := e.s.askCopy("task"); !a.AcceptedAt.IsZero() {
						t.Fatal("failed answer accepted the ask")
					}
					return
				}
				if !ok || err != nil {
					t.Fatalf("valid answer: %v, %v", ok, err)
				}
				before.Undone = true
				assertAnswerDismissal(t, e.s, st, "task", before)
				sent := len(sentToAgent(e.f))
				if ok, err := dismissalAnswer(e.s, entry); ok || err != nil || len(sentToAgent(e.f)) != sent {
					t.Fatalf("answer replay: %v, %v", ok, err)
				}
			})
		}
	}
}

func TestTaskReviewDismissalNoAgentHandoffUndoesIt(t *testing.T) {
	for _, option := range []string{"先灰度 10%", "直接全量"} { // i18n-ignore: the shared rollout decision's option labels
		t.Run(option, func(t *testing.T) {
			database := todoTestDB(t)
			s, f, todo := newDecisionAnswerServiceOn(t, database)
			waitsForReview(t, s, f, todo.ID, "Choose a rollout plan.")
			if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
				t.Fatal(err)
			}
			review := currentDecisionReview(todoNow(s, todo.ID))
			if _, err := s.changeDecisionDismissal(context.Background(), todo.ID, review.ReviewAt, "", false); err != nil {
				t.Fatal(err)
			}
			before := s.reviewDismissals.byTodo[todo.ID]
			f.setSessions("box")
			// The handoff skips the fixture's one-hour gather (#17099): hold the digest timer so it is still pending
			// when read below, instead of already told to the main conversation.
			s.stopSuperviseTimers()
			w := answerAsk(s, todo.ID, map[string]string{"option": option})
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK || body["delivered"] != "none" {
				t.Fatalf("supervisor handoff: %d %s (%v)", w.Code, w.Body, err)
			}
			before.Undone = true
			assertAnswerDismissal(t, s, database, todo.ID, before)
			rows, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
			if err != nil || len(rows) != 1 || rows[0].Choice != option {
				t.Fatalf("recorded choice: %+v, %v", rows, err)
			}
			if w := answerAsk(s, todo.ID, map[string]string{"option": option}); w.Code != http.StatusConflict {
				t.Fatalf("handoff replay: %d %s", w.Code, w.Body)
			}
			key := "undelivered|" + askOf(s, todo.ID).AskedAt.UTC().Format(time.RFC3339Nano)
			s.supervise.mu.Lock()
			signal, pending := s.supervise.pending[todo.ID+"|"+sigIdle]
			s.supervise.mu.Unlock()
			if !pending || signal.Key != key {
				t.Fatalf("supervisor handoff was not queued: %+v", signal)
			}
			s.waitWorkEvents()
		})
	}
}

type dismissalAnswerWriteFails struct {
	Store
	key        string
	onlyUndone bool
}

func (st *dismissalAnswerWriteFails) PutSetting(ctx context.Context, key string, value any) error {
	if key == st.key {
		if !st.onlyUndone || value.(map[string]taskReviewDismissal)["task"].Undone {
			return errors.New("test write refused")
		}
	}
	return st.Store.PutSetting(ctx, key, value)
}

func TestTaskReviewDismissalAcceptanceSaveFailureKeepsIt(t *testing.T) {
	e, st, before := dismissalAnswerEnv(t)
	base := e.s.store()
	e.s.setStore(func() Store { return &dismissalAnswerWriteFails{Store: base, key: acceptanceKey} })
	if result, err := e.s.acceptTry(context.Background(), "task", ""); result.taken || !errors.Is(err, errAcceptStore) {
		t.Fatalf("unsaved acceptance: %v, %v", result, err)
	}
	assertAnswerDismissal(t, e.s, st, "task", before)
	if len(sentToAgent(e.f)) != 0 {
		t.Fatal("unsaved acceptance was delivered")
	}
}

func TestTaskReviewDismissalAgentReplyAndLaterAnswer(t *testing.T) {
	for _, answer := range []string{"accept", "already told", "chat answer", "delivery failure", "acceptance save failure"} {
		t.Run(answer, func(t *testing.T) {
			ctx := context.Background()
			database := todoTestDB(t)
			s, f, todo := newDecisionAnswerServiceOn(t, database)
			waitsForReview(t, s, f, todo.ID, "Choose a rollout plan.")
			if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
				t.Fatal(err)
			}
			review := currentDecisionReview(todoNow(s, todo.ID))
			if _, err := s.changeDecisionDismissal(ctx, todo.ID, review.ReviewAt, "", false); err != nil {
				t.Fatal(err)
			}
			before := s.reviewDismissals.byTodo[todo.ID]
			a := askOf(s, todo.ID)
			s.UserSpoke(ctx, SessionRef{MachineID: a.MachineID, SessionID: a.SessionID})
			paused := askOf(s, todo.ID)
			if paused.AgentReply == nil || paused.DueAt.IsZero() || !paused.AcceptedAt.IsZero() {
				t.Fatalf("Agent chat did not pause the pending default: %+v", paused)
			}
			assertAnswerDismissal(t, s, database, todo.ID, before)
			pastDeadline(t, s, todo.ID)
			sent := len(sentToAgent(f))
			s.sweepTaskDeadlines(time.Now())
			if result, err := s.acceptTryAs(ctx, todo.ID, "", true); result.taken || err != nil {
				t.Fatalf("paused, dismissed default was taken: %+v %v", result, err)
			}
			assertAnswerDismissal(t, s, database, todo.ID, before)
			if len(sentToAgent(f)) != sent {
				t.Fatal("paused default reached the Agent")
			}

			store := s.storeAccessor()
			switch answer {
			case "acceptance save failure":
				base := s.store()
				s.setStore(func() Store { return &dismissalAnswerWriteFails{Store: base, key: acceptanceKey} })
			case "delivery failure":
				f.failSend = errors.New("retryable send failure")
			}
			var err error
			switch answer {
			case "already told":
				_, err = s.decisionTold(ctx, todo.ID, a.Recommend, "box", "conv1", "Use the recommended option.")
			case "chat answer":
				_, err = s.routeDecisionAnswer(ctx, "conv1", todo.ID, a.AskedAt.Format(timeKeyLayout), "Use the recommended option.")
			default:
				_, err = s.acceptTry(ctx, todo.ID, "")
			}
			if answer == "delivery failure" || answer == "acceptance save failure" {
				if err == nil || answer == "acceptance save failure" && !errors.Is(err, errAcceptStore) {
					t.Fatalf("answer failure was hidden: %v", err)
				}
				assertAnswerDismissal(t, s, database, todo.ID, before)
				failed := askOf(s, todo.ID)
				if failed.AgentReply == nil || !failed.DueAt.Equal(paused.DueAt) || !failed.AcceptedAt.IsZero() || taskDeadlineDue(failed, time.Now()) {
					t.Fatalf("failed answer lost the Agent-chat pause: %+v", failed)
				}
				var saved map[string]*acceptAsk
				if _, err := database.Setting(ctx, acceptanceKey, &saved); err != nil {
					t.Fatal(err)
				}
				if saved[todo.ID] == nil || saved[todo.ID].AgentReply == nil || !saved[todo.ID].DueAt.Equal(paused.DueAt) || !saved[todo.ID].AcceptedAt.IsZero() {
					t.Fatalf("failed answer lost the persisted pause: %+v", saved[todo.ID])
				}
				if len(sentToAgent(f)) != sent {
					t.Fatal("failed answer reached the Agent")
				}
				s.setStore(store)
				f.failSend = nil
				if result, err := s.acceptTry(ctx, todo.ID, ""); err != nil || !result.taken {
					t.Fatalf("valid retry after the pause was rejected: %+v %v", result, err)
				}
			} else if err != nil {
				t.Fatalf("later explicit answer was rejected: %v", err)
			}
			before.Undone = true
			assertAnswerDismissal(t, s, database, todo.ID, before)
			answered := askOf(s, todo.ID)
			if answered.AgentReply == nil || answered.AcceptedAt.IsZero() || answered.ByDefault {
				t.Fatalf("explicit answer did not end the paused round: %+v", answered)
			}
			wantSent := sent + 1
			if answer == "already told" {
				wantSent = sent
			}
			if len(sentToAgent(f)) != wantSent {
				t.Fatalf("answer sent %d messages; want %d", len(sentToAgent(f))-sent, wantSent-sent)
			}
		})
	}
}

func TestTaskReviewDismissalCompletionWriteFailureDoesNotResend(t *testing.T) {
	for _, entry := range []string{"accept", "recommended option", "alternative option", "owner response", "already told"} {
		t.Run(entry, func(t *testing.T) {
			e, st, before := dismissalAnswerEnv(t)
			base := e.s.store()
			e.s.setStore(func() Store {
				return &dismissalAnswerWriteFails{Store: base, key: taskReviewDismissalsKey, onlyUndone: true}
			})
			if ok, err := dismissalAnswer(e.s, entry); !ok || err != nil {
				t.Fatalf("already-delivered answer became retryable: %v, %v", ok, err)
			}
			assertAnswerDismissal(t, e.s, st, "task", before)
			sent := len(sentToAgent(e.f))
			if ok, err := dismissalAnswer(e.s, entry); ok || err != nil || len(sentToAgent(e.f)) != sent {
				t.Fatalf("answer replay: %v, %v", ok, err)
			}
		})
	}
}
