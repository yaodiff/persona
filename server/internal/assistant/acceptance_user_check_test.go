package assistant

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func acceptedForUserCheck(t *testing.T) (*Service, *fakeHub, Todo) {
	t.Helper()
	return acceptedForUserCheckOn(t, nil)
}

func acceptedForUserCheckOn(t *testing.T, store Store) (*Service, *fakeHub, Todo) {
	t.Helper()
	s, f, todo, _, _ := conclusionServiceOn(t, store, false)
	waitsForReview(t, s, f, todo.ID, "The layout is ready to inspect.")
	f.judge = `{"changed":"The layout is updated","link":"","how":"Inspect the task page"}`
	reviewTask(t, s, todo.ID, map[string]any{
		"verdict": "accepted", "user_check": true, "note": "Ready for the owner's check",
		"checks": completeReviewChecks(todoNow(s, todo.ID)),
	})
	return s, f, todoNow(s, todo.ID)
}

func TestUserCheckKeepsTaskAndAgentOpenUntilPass(t *testing.T) {
	ctx := context.Background()
	s, f, todo := acceptedForUserCheck(t)
	if todo.DoneAt != nil || todo.DoneBy != "" {
		t.Fatalf("personal verification already closed the task: done_at=%v done_by=%q", todo.DoneAt, todo.DoneBy)
	}
	if got := todo.Supervision.state(todo); got != "user_check" {
		t.Fatalf("personal verification state = %q, want user_check", got)
	}
	if len(s.doneTaskAgents()) != 0 {
		t.Fatal("a personal-verification task entered completed-task retirement")
	}
	a := askOf(s, todo.ID)
	if a == nil || !a.AcceptedAt.IsZero() || waitingForOwner(s) != 1 {
		t.Fatalf("the owner has no pending verification: %+v", a)
	}

	// An owner taking longer than the ordinary stalled-task limit must not lose this Agent.
	old := time.Now().Add(-100 * time.Hour)
	s.todos.mu.Lock()
	s.todos.todos[todo.ID].UpdatedAt = old
	s.todos.mu.Unlock()
	f.setSessions("box", idleAgent(old))
	seen := doneSeen{}
	s.closeDoneTaskAgents(ctx, taskCloseRefused{}, seen)
	s.closeIdleAgents(ctx, taskCloseRefused{}, seen, idleSeen{{"box", "s1"}: old})
	if got := f.retires(); len(got) != 0 {
		t.Fatalf("retired an Agent awaiting personal verification: %v", got)
	}
	if got := watchSignals(todoNow(s, todo.ID), nil, time.Now().Add(100*time.Hour)); len(got) != 0 {
		t.Fatalf("personal verification raised supervisor follow-ups: %+v", got)
	}

	if result, err := s.acceptTry(ctx, todo.ID, a.MessageID); err != nil || !result.taken {
		t.Fatalf("owner Pass: %+v, %v", result, err)
	}
	done := todoNow(s, todo.ID)
	if done.DoneAt == nil || done.DoneBy != db.DoneByReview || done.Stage != "verify" || done.Supervision.state(done) != reviewAccepted {
		t.Fatalf("Pass did not finish and verify the task: %+v", done)
	}
	if waitingForOwner(s) != 0 {
		t.Fatal("Pass left the owner-verification ask pending")
	}
	if result, err := s.acceptTry(ctx, todo.ID, a.MessageID); err != nil || result.taken {
		t.Fatalf("duplicate Pass was taken: %+v, %v", result, err)
	}

	idle := taskCloseIdle
	taskCloseIdle = 0
	t.Cleanup(func() { taskCloseIdle = idle })
	s.closeDoneTaskAgents(ctx, taskCloseRefused{}, seen)
	if got := f.retires(); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("Pass did not permit normal completed-task retirement: %v", got)
	}
}

func TestUserCheckOwnerPassWhileViewing(t *testing.T) {
	ctx := t.Context()
	s, f, todo, _, _ := conclusionServiceOn(t, todoTestDB(t), false)
	waitsForReview(t, s, f, todo.ID, "The layout is ready to inspect.")
	f.judge = `{"changed":"The layout is updated","link":"","how":"Inspect the task page"}`
	setTaskViewer(t, s, "box", "s1", true)
	reviewTask(t, s, todo.ID, map[string]any{
		"verdict": "accepted", "user_check": true, "checks": completeReviewChecks(todoNow(s, todo.ID)),
	})
	a := askOf(s, todo.ID)
	if got := todoNow(s, todo.ID); got.DoneAt != nil || got.Supervision.state(got) != reviewUserCheck || a == nil {
		t.Fatalf("personal verification did not keep the task open: state=%s, done=%v, ask=%+v", got.Supervision.state(got), got.DoneAt, a)
	}
	if result, err := s.acceptTry(ctx, todo.ID, a.MessageID); err != nil || !result.taken {
		t.Fatalf("owner Pass while viewing: %+v, %v", result, err)
	}
	if got := todoNow(s, todo.ID); got.DoneAt == nil || got.DoneBy != db.DoneByReview || len(f.retires()) != 0 {
		t.Fatalf("owner Pass did not finish the task while keeping its Agent: done=%v, by=%s, retires=%v", got.DoneAt, got.DoneBy, f.retires())
	}
}

func TestUserCheckCancellationClosesTask(t *testing.T) {
	ctx := t.Context()
	store := todoTestDB(t)
	s, hub, todo := acceptedForUserCheckOn(t, store)
	a := askOf(s, todo.ID)
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "cancelled", "note": "No longer needed"})
	restarted := newServiceOn(t, hub, store)
	if err := restarted.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	got := todoNow(restarted, todo.ID)
	if got.DoneAt == nil || got.DoneBy != db.DoneByCancelled || got.Supervision.state(got) != reviewCancelled || len(hub.retires()) != 0 {
		t.Fatalf("cancellation did not close the task while keeping its Agent: done=%v, by=%s, state=%s, retires=%v",
			got.DoneAt, got.DoneBy, got.Supervision.state(got), hub.retires())
	}
	if result, err := restarted.acceptTry(ctx, todo.ID, a.MessageID); err != nil || result.taken {
		t.Fatalf("old Pass was still usable after cancellation: %+v, %v", result, err)
	}
}

func TestUserCheckFeedbackReturnsToTheSameAgent(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo := acceptedForUserCheck(t)
	a := askOf(s, todo.ID)
	before := len(sentToAgent(f))
	if !s.acceptReply(context.Background(), "om_feedback", "Please fix the spacing.", false, a.MessageID) {
		t.Fatal("feedback was not recognized")
	}
	got := todoNow(s, todo.ID)
	if got.DoneAt != nil || got.Supervision.state(got) == "user_check" || got.ReopenedAt == nil {
		t.Fatalf("feedback did not return the open task to ongoing work: %+v", got)
	}
	if askOf(s, todo.ID) != nil {
		t.Fatal("feedback left an obsolete Pass action available")
	}
	f.mu.Lock()
	sent := append([]map[string]any(nil), f.agentSent[before:]...)
	f.mu.Unlock()
	if len(sent) != 1 || sent[0]["session_id"] != "s1" || !strings.Contains(sent[0]["text"].(string), "Please fix the spacing.") {
		t.Fatalf("feedback did not reach the original Agent exactly once: %+v", sent)
	}
	if result, err := s.acceptTry(context.Background(), todo.ID, a.MessageID); err != nil || result.taken || todoNow(s, todo.ID).DoneAt != nil {
		t.Fatalf("a superseded verification card closed rework: %+v, %v", result, err)
	}
}

func TestUserCheckChatFeedbackInvalidatesPass(t *testing.T) {
	ctx := context.Background()
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "task card", true: "Agent chat"}[direct], func(t *testing.T) {
			s, f, todo := acceptedForUserCheck(t)
			a := askOf(s, todo.ID)
			before := len(sentToAgent(f))
			if direct {
				s.userSpokeText(ctx, SessionRef{MachineID: "box", SessionID: "s1"}, "Please fix the spacing.")
			} else if _, err := s.routeToTask(ctx, todo.Origin, todo.ID, "Please fix the spacing."); err != nil {
				t.Fatal(err)
			}
			got := todoNow(s, todo.ID)
			if got.DoneAt != nil || got.Supervision.state(got) == reviewUserCheck || got.ReopenedAt == nil {
				t.Fatalf("chat feedback did not resume ongoing work: %+v", got)
			}
			if !direct {
				f.mu.Lock()
				sent := append([]map[string]any(nil), f.agentSent[before:]...)
				f.mu.Unlock()
				if len(sent) != 1 || sent[0]["session_id"] != "s1" {
					t.Fatalf("chat feedback did not reach the same Agent: %+v", sent)
				}
			}
			if result, err := s.acceptTry(ctx, todo.ID, a.MessageID); err != nil || result.taken {
				t.Fatalf("old Pass accepted after chat feedback: %+v, %v", result, err)
			}
		})
	}
}

func TestUserCheckOldUserPromptDoesNotInvalidatePass(t *testing.T) {
	s, _, todo := acceptedForUserCheck(t)
	a := askOf(s, todo.ID)
	s.agentDecisionSpoke(context.Background(), SessionRef{MachineID: "box", SessionID: "s1"}, a.AskedAt.Add(-time.Second), "")
	if got := todoNow(s, todo.ID); got.Supervision.state(got) != reviewUserCheck {
		t.Fatalf("an old replayed user prompt cancelled personal verification: %+v", got)
	}
}

func TestUserCheckPassHTTPCompletesTheTask(t *testing.T) {
	s, _, todo := acceptedForUserCheck(t)
	if todo.DoneAt != nil {
		t.Fatal("task was closed before the web's Pass")
	}
	row := listedTodos(t, s)[todo.ID]
	view, _ := row["view"].(map[string]any)
	ask, _ := row["ask"].(map[string]any)
	at, _ := ask["asked_at"].(string)
	// #15680 (#15638) keeps personal verification in the calm to-verify layer, not red Needs you; #16002's Pass-round
	// repair is preserved through the listed ask's asked_at, which the /accept Pass below must complete.
	if view["section"] == assistantwire.TodoListSectionNeedsYou || view["reason"] == assistantwire.TodoReasonVerify {
		t.Fatalf("verification must stay out of Needs you under #15680: %v", row)
	}
	if at == "" {
		t.Fatalf("verification keeps its Pass round asked_at for the /accept action: %v", row)
	}
	body := `{"asked_at":"` + at + `"}`
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/todos/"+todo.ID+"/accept", strings.NewReader(body)))
	if rec.Code != http.StatusOK || todoNow(s, todo.ID).DoneAt == nil {
		t.Fatalf("web Pass: status=%d body=%s todo=%+v", rec.Code, rec.Body.String(), todoNow(s, todo.ID))
	}
	if ask := listedAsk(t, s, todo.ID); ask != nil {
		t.Fatalf("completed task still has a verification ask: %v", ask)
	}
}

func TestUserCheckPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	database := todoTestDB(t)
	_, f, todo := acceptedForUserCheckOn(t, database)
	if row, err := database.GetTodo(ctx, todo.ID); err != nil || row.DoneAt != nil {
		t.Fatalf("verification was not persisted as open: %+v, %v", row, err)
	}
	restarted := newServiceOn(t, f, database)
	if err := restarted.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	if got := todoNow(restarted, todo.ID); got.Supervision.state(got) != "user_check" {
		t.Fatalf("restart lost pending personal verification: %+v", got)
	}
	if result, err := restarted.acceptTry(ctx, todo.ID, ""); err != nil || !result.taken {
		t.Fatalf("Pass after restart: %+v, %v", result, err)
	}
	if row, err := database.GetTodo(ctx, todo.ID); err != nil || row.DoneAt == nil || row.DoneBy != db.DoneByReview {
		t.Fatalf("Pass was not persisted as completed: %+v, %v", row, err)
	}
}

func TestUserCheckRecoversAStoredPassBeforeCompletion(t *testing.T) {
	ctx := context.Background()
	database := todoTestDB(t)
	s, f, todo := acceptedForUserCheckOn(t, database)
	// The process ended after persisting Pass but before completing the task.
	s.accept.mu.Lock()
	s.accept.asks[todo.ID].AcceptedAt = time.Now().UTC()
	err := s.saveAcceptErrLocked(ctx)
	s.accept.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	restarted := newServiceOn(t, f, database)
	restarted.verifySupervisedAsks(ctx)
	if row, err := database.GetTodo(ctx, todo.ID); err != nil || row.DoneAt == nil || row.DoneBy != db.DoneByReview {
		t.Fatalf("restart left a recorded owner Pass incomplete: %+v, %v", row, err)
	}
	if got := todoNow(restarted, todo.ID); got.Stage != "verify" || got.Supervision.state(got) != reviewAccepted {
		t.Fatalf("recovered Pass was not verified: %+v", got)
	}
}

func TestUserCheckRecoversPassAfterCompletionBeforeVerification(t *testing.T) {
	ctx := context.Background()
	database := todoTestDB(t)
	s, f, todo := acceptedForUserCheckOn(t, database)
	s.accept.mu.Lock()
	s.accept.asks[todo.ID].AcceptedAt = time.Now().UTC()
	err := s.saveAcceptErrLocked(ctx)
	s.accept.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.reviewDoneTodo(ctx, todo.ID, "Owner passed", true); err != nil {
		t.Fatal(err)
	}
	restarted := newServiceOn(t, f, database)
	restarted.verifySupervisedAsks(ctx)
	if got := todoNow(restarted, todo.ID); got.DoneAt == nil || got.Stage != "verify" || got.Supervision.State != reviewAccepted {
		t.Fatalf("restart did not finish the verified stage: %+v", got)
	}
}

type userCheckCloseFailure struct {
	*db.DB
	fail bool
}

func (d *userCheckCloseFailure) ReviewTodo(ctx context.Context, id, reason string, accepted bool, at time.Time, ledger *db.TodoSupervisionRow, guard func() error) (*db.TodoRow, bool, error) {
	if d.fail {
		return nil, false, errors.New("completion store unavailable")
	}
	return d.DB.ReviewTodo(ctx, id, reason, accepted, at, ledger, guard)
}

func TestUserCheckFailedCompletionLeavesPassRetryable(t *testing.T) {
	ctx := context.Background()
	store := &userCheckCloseFailure{DB: todoTestDB(t)}
	s, _, todo := acceptedForUserCheckOn(t, store)
	store.fail = true
	if result, err := s.acceptTry(ctx, todo.ID, ""); result.taken || !errors.Is(err, errAcceptStore) {
		t.Fatalf("failed completion was reported accepted: %+v, %v", result, err)
	}
	if got := todoNow(s, todo.ID); got.DoneAt != nil || got.Supervision.state(got) != "user_check" {
		t.Fatalf("failed Pass changed the task: %+v", got)
	}
	if a := askOf(s, todo.ID); a == nil || !a.AcceptedAt.IsZero() {
		t.Fatalf("failed Pass consumed the ask: %+v", a)
	}
	store.fail = false
	if result, err := s.acceptTry(ctx, todo.ID, ""); err != nil || !result.taken || todoNow(s, todo.ID).DoneAt == nil {
		t.Fatalf("retry did not complete: %+v, %v", result, err)
	}
}

func TestUserCheckIsNotOverwrittenByAnotherJudgment(t *testing.T) {
	ctx := context.Background()
	s, _, todo := acceptedForUserCheck(t)
	s.superviseJudged(ctx, todo, time.Now().Add(time.Hour), taskDoneCheck{Verdict: verdictDone}, nil)
	s.superviseJudged(ctx, todo, time.Now().Add(time.Hour), taskDoneCheck{Verdict: verdictNotDone}, nil)
	if got := todoNow(s, todo.ID); got.DoneAt != nil || got.Supervision.state(got) != "user_check" {
		t.Fatalf("a later automatic judgment settled personal verification: %+v", got)
	}
}

func TestUserCheckReworkInvalidatesTheOldPass(t *testing.T) {
	s, _, todo := acceptedForUserCheck(t)
	a := askOf(s, todo.ID)
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "rework", "note": "Fix the spacing before another personal check."})
	got := todoNow(s, todo.ID)
	if got.DoneAt != nil || got.Supervision.state(got) != reviewRework {
		t.Fatalf("supervisor rework did not resume the task: %+v", got)
	}
	if result, err := s.acceptTry(context.Background(), todo.ID, a.MessageID); err != nil || result.taken {
		t.Fatalf("the previous personal check still accepts rework: %+v, %v", result, err)
	}
}

func TestUserCheckReplacesAnEarlierDecision(t *testing.T) {
	s, f, todo, _, _ := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "The owner needs to choose a layout.")
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "Choose the layout", "recommend": "Use the compact layout"})
	// The result now permits personal verification, while the earlier decision
	// still exists and must be retired when the verification request opens.
	f.setItems(proto.Item{Role: "assistant", Kind: proto.KindText, Text: "I applied the compact layout and verified the task page."})
	f.judge = `{"changed":"The layout is updated","link":"","how":"Inspect the task page"}`
	reviewTask(t, s, todo.ID, map[string]any{
		"verdict": "accepted", "user_check": true, "checks": completeReviewChecks(todoNow(s, todo.ID)),
	})
	if a := askOf(s, todo.ID); a == nil || decideAsk(a) || a.Conclusion || !a.AcceptedAt.IsZero() {
		t.Fatalf("the earlier decision displaced personal verification: %+v", a)
	}
	if result, err := s.acceptTry(context.Background(), todo.ID, ""); err != nil || !result.taken || todoNow(s, todo.ID).DoneAt == nil {
		t.Fatalf("personal Pass after a decision did not complete: %+v, %v", result, err)
	}
}

func TestUserCheckFeedbackRoundPersistsAcrossRestart(t *testing.T) {
	needsLarkLayer(t)
	ctx := context.Background()
	for _, via := range []string{"verification card", "task card", "Agent chat"} {
		t.Run(via, func(t *testing.T) {
			database := todoTestDB(t)
			s, f, todo := acceptedForUserCheckOn(t, database)
			a := askOf(s, todo.ID)
			switch via {
			case "verification card":
				if !s.acceptReply(ctx, "om_feedback", "Please fix the spacing.", false, a.MessageID) {
					t.Fatal("feedback was not recognized")
				}
			case "task card":
				if _, err := s.routeToTask(ctx, todo.Origin, todo.ID, "Please fix the spacing."); err != nil {
					t.Fatal(err)
				}
			case "Agent chat":
				s.userSpokeText(ctx, SessionRef{MachineID: "box", SessionID: "s1"}, "Please fix the spacing.")
			}
			row, err := database.GetTodo(ctx, todo.ID)
			if err != nil || row.DoneAt != nil || row.ReopenedAt == nil || !row.ReopenedAt.After(a.AskedAt) {
				t.Fatalf("feedback did not persist its new work round: %+v, %v", row, err)
			}
			restarted := newServiceOn(t, f, database)
			if err := restarted.ensureTodosLoaded(ctx); err != nil {
				t.Fatal(err)
			}
			if result, err := restarted.acceptTry(ctx, todo.ID, a.MessageID); err != nil || result.taken {
				t.Fatalf("old Pass still accepts rework after a restart: %+v, %v", result, err)
			}
			if got := todoNow(restarted, todo.ID); got.DoneAt != nil || got.Stage == "verify" || got.Supervision.state(got) == reviewUserCheck {
				t.Fatalf("restarted feedback did not remain ongoing: %+v", got)
			}
		})
	}
}
