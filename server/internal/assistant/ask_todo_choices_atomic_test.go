package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Reject real database writes, not an adapter method: acceptance must also survive failures inside its transaction.
func rejectExpiredAnswerWrite(t *testing.T, database *db.DB, table, condition string) func() {
	t.Helper()
	exec := func(sql string) {
		t.Helper()
		if err := database.Transaction(context.Background(), func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), sql)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	exec(fmt.Sprintf(`CREATE FUNCTION test_reject_expired_answer() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF %s THEN RAISE EXCEPTION 'synthetic expired answer write failure'; END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER test_reject_expired_answer BEFORE INSERT OR UPDATE ON %s
			FOR EACH ROW EXECUTE FUNCTION test_reject_expired_answer()`, condition, table))
	remove := func() {
		exec("DROP TRIGGER IF EXISTS test_reject_expired_answer ON " + table +
			"; DROP FUNCTION IF EXISTS test_reject_expired_answer()")
	}
	t.Cleanup(remove)
	return remove
}

func TestExpiredQuestionCompleteAnswerCommitsOrRemainsRetryableWithoutSending(t *testing.T) {
	for _, failure := range []struct {
		name, table, condition string
	}{
		{"second question insert", "todo_decisions", `NEW.question = 'Which clients?'`},
		{"answer settlement setting", "settings", `NEW.key = 'assistant.ask_todos' AND
			EXISTS (SELECT 1 FROM jsonb_array_elements(NEW.value) a WHERE a->>'answered_at' IS NOT NULL)`},
		{"relay queue setting", "settings", `NEW.key = '` + relayKey + `'`},
		{"to-do completion", "todos", `NEW.done_at IS NOT NULL AND OLD.done_at IS NULL`},
	} {
		t.Run(failure.name, func(t *testing.T) {
			database := todoTestDB(t)
			e := newAskTodoEnv(t, database)
			args := `{"why_owner":"priority","questions":[{"question":"Which work?","options":[{"label":"Docs"},{"label":"Tests"}]},` +
				`{"question":"Which clients?","multiSelect":true,"options":[{"label":"Web"},{"label":"Phone"}]}]}`
			ix, _ := expireRecorded(e, database, args)
			todo := e.onlyAskTodo()
			// A working recipient forces durable queue acceptance, not a lucky direct send.
			e.sessions("server", proto.Session{SessionID: "c1x1x1", Assistant: true, Status: proto.StatusWorking})
			removeFailure := rejectExpiredAnswerWrite(t, database, failure.table, failure.condition)
			answer := map[string]any{"answers": map[string]string{"Which work?": "Tests", "Which clients?": "Web, Phone"}}
			if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, answer); code != 503 {
				t.Errorf("a failed %s must not say Answer recorded: HTTP %d, want 503", failure.name, code)
			}
			rows, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
			if err != nil || len(rows) != 0 {
				t.Errorf("a rejected answer must leave no partial manual decisions: rows=%+v err=%v", rows, err)
			}
			var records []askTodo
			if _, err := database.Setting(context.Background(), askTodosKey, &records); err != nil {
				t.Fatal(err)
			}
			if len(records) != 1 || !records[0].AnsweredAt.IsZero() || !records[0].ClaimedAt.IsZero() {
				t.Errorf("a rejected answer must release its claim without persisting success: %+v", records)
			}
			var relays relayState
			if _, err := database.Setting(context.Background(), relayKey, &relays); err != nil {
				t.Fatal(err)
			}
			if len(relays.Queue["c1x1x1"]) != 0 || len(e.told("c1x1x1")) != 0 || e.get(todo.ID).DoneAt != nil {
				t.Error("a rejected answer must neither queue/send a message nor close the question")
			}
			if rec := askRecord(t, database, ix.ID); rec.Resolution != nil && len(rec.Resolution.Answers) != 0 {
				t.Errorf("a rejected submission must not appear as an answer in card history: %+v", rec.Resolution)
			}
			removeFailure()
			again := newAskTodoEnv(t, database)
			if view := expiredQuestionPayload(again, todo.ID); view.Ask == nil || view.DoneAt != nil {
				t.Errorf("the complete form must remain answerable after reload: %+v", view)
			}
			again.sessions("server", proto.Session{SessionID: "c1x1x1", Assistant: true, Status: proto.StatusWorking})
			if code := answerExpiredQuestion(again, todo.ID, ix.CreatedAt, answer); code != 200 {
				t.Fatalf("retry after recovering %s: HTTP %d", failure.name, code)
			}
			rows, err = database.ListTodoDecisions(context.Background(), todo.ID, true)
			if err != nil || len(rows) != 2 || again.get(todo.ID).DoneAt == nil || len(again.told("c1x1x1")) != 0 {
				t.Fatalf("acceptance must commit both decisions and close with delivery still queued: rows=%+v err=%v", rows, err)
			}
			reloaded := newAskTodoEnv(t, database)
			waitFor(t, "the committed complete answer after restart", func() bool { return len(reloaded.told("c1x1x1")) == 1 })
			messages := reloaded.told("c1x1x1")
			if !strings.Contains(messages[0], "Tests") || !strings.Contains(messages[0], "Web, Phone") {
				t.Fatalf("the recovered relay must carry the complete answer: %q", messages)
			}
			a, _ := reloaded.s.askTodoOf(context.Background(), todo.ID)
			if a.AnsweredAt.IsZero() || !a.ClaimedAt.IsZero() || expiredQuestionPayload(reloaded, todo.ID).Ask != nil {
				t.Fatalf("acceptance must remain terminal after restart: %+v", a)
			}
			if code := answerExpiredQuestion(reloaded, todo.ID, ix.CreatedAt, answer); code != 409 || len(reloaded.told("c1x1x1")) != 1 {
				t.Fatalf("retrying an accepted answer must not relay it twice: HTTP %d", code)
			}
			t.Logf("failure=%q rejected_http=503 rejected_decisions=0 rejected_relays=0 retry_http=200 decisions=2 recovered_messages=1 replay_http=409",
				failure.name)
		})
	}
}

type expiredAnswerLostCommitReplyStore struct {
	Store
	todoStore
}

func (s expiredAnswerLostCommitReplyStore) CommitTodoAnswer(ctx context.Context, id string, decisions []db.TodoDecisionRow, settings map[string]any, at time.Time) (*db.TodoRow, []db.TodoDecisionRow, error) {
	row, inserted, err := s.todoStore.CommitTodoAnswer(ctx, id, decisions, settings, at)
	if err == nil {
		err = errors.New("synthetic lost commit reply")
	}
	return row, inserted, err
}

func TestExpiredQuestionLostCommitReplyNeverReleasesAnAcceptedAnswer(t *testing.T) {
	database := todoTestDB(t)
	base := newAskTodoEnv(t, database).s.store()
	e := newAskTodoEnv(t, expiredAnswerLostCommitReplyStore{Store: base, todoStore: base.(todoStore)})
	ix, _ := e.expire()
	todo := e.onlyAskTodo()
	e.s.relays.mu.Lock()
	e.s.relayLocked(context.Background())
	e.s.relays.draining["other_conversation"] = true
	e.s.relays.ownerTurns["other_conversation"] = ownerTurn{at: time.Now().UTC(), working: true}
	e.s.relays.mu.Unlock()
	answer := map[string]any{"option": "Wait"}
	if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, answer); code != 503 {
		t.Fatalf("a lost commit reply is not a confirmed success: HTTP %d", code)
	}
	a, _ := e.s.askTodoOf(context.Background(), todo.ID)
	if a.AnsweredAt.IsZero() || !a.ClaimedAt.IsZero() {
		t.Fatalf("claim recovery must reread the commit rather than undo its terminal state: %+v", a)
	}
	if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, answer); code != 409 {
		t.Fatalf("the same service must reconcile the committed answer instead of resubmitting it: HTTP %d", code)
	}
	e.s.relays.mu.Lock()
	reloaded := e.s.relayLocked(context.Background())
	draining := e.s.relays.draining["other_conversation"]
	_, ownerWorking := e.s.relays.ownerTurns["other_conversation"]
	e.s.relays.mu.Unlock()
	if !reloaded || !draining || !ownerWorking {
		t.Fatal("reloading a committed answer must preserve other conversations' in-flight delivery and owner-turn gates")
	}
	again := newAskTodoEnv(t, database)
	waitFor(t, "the accepted answer survives its lost acknowledgement", func() bool { return len(again.told("c1x1x1")) == 1 })
	if again.get(todo.ID).DoneAt == nil || expiredQuestionPayload(again, todo.ID).Ask != nil {
		t.Fatal("the lost acknowledgement must not reopen a committed answer after restart")
	}
	rows, err := database.ListTodoDecisions(context.Background(), todo.ID, true)
	if err != nil || len(rows) != 1 || len(e.told("c1x1x1")) != 0 {
		t.Fatalf("the durable outbox must retain one accepted decision and one delivery: rows=%+v err=%v", rows, err)
	}
	t.Log("lost_commit_reply_http=503 reconciled_http=409 decisions=1 recovered_messages=1 closed=true")
}

func TestExpiredQuestionLostCommitReplySurvivesConcurrentDrain(t *testing.T) {
	database := todoTestDB(t)
	base := newAskTodoEnv(t, database).s.store()
	e := newAskTodoEnv(t, expiredAnswerLostCommitReplyStore{Store: base, todoStore: base.(todoStore)})
	ix, _ := e.expire()
	todo := e.onlyAskTodo()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sending, resume := make(chan struct{}), make(chan struct{})
	finishSend := sync.OnceFunc(func() { close(resume) })
	defer finishSend()
	e.f.mu.Lock()
	e.f.beforeAssistantSend = func() {
		close(sending)
		select {
		case <-resume:
		case <-ctx.Done():
		}
	}
	e.f.mu.Unlock()
	// This message takes no relay hop on delivery, so there is no intervening queue read before its completion.
	if n := e.s.queueRelay("m1", relayMsg{Tool: taskEndTool, From: "earlier_task", Text: "An earlier task ended.", At: decisionNow()}); n != 1 {
		t.Fatalf("earlier message queue length = %d", n)
	}
	e.s.drainRelays("server", []proto.Session{{SessionID: "m1", Assistant: true, Status: proto.StatusIdle}})
	select {
	case <-sending:
	case <-ctx.Done():
		t.Fatal("the earlier relay did not reach its send")
	}
	answer := map[string]any{"option": "Wait"}
	if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, answer); code != 503 {
		t.Fatalf("lost commit reply: HTTP %d, want 503", code)
	}
	finishSend()
	waitFor(t, "the earlier drain to finish", func() bool {
		e.s.relays.mu.Lock()
		defer e.s.relays.mu.Unlock()
		return !e.s.relays.draining["m1"]
	})
	var persisted relayState
	if _, err := database.Setting(ctx, relayKey, &persisted); err != nil {
		t.Fatal(err)
	}
	if earlier, answer := persisted.Queue["m1"], persisted.Queue["c1x1x1"]; len(earlier) != 0 || len(answer) != 1 || answer[0].From != todo.ID {
		t.Fatalf("the earlier drain must remove its message and preserve the committed answer: earlier=%d answer=%+v", len(earlier), answer)
	}
	if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, answer); code != 409 {
		t.Fatalf("replay of committed answer: HTTP %d, want 409", code)
	}
	again := newAskTodoEnv(t, database)
	waitFor(t, "the committed answer to reach its conversation after restart", func() bool { return len(again.told("c1x1x1")) == 1 })
	if messages := again.told("c1x1x1"); len(messages) != 1 || !strings.Contains(messages[0], "Wait") {
		t.Fatalf("the original conversation must receive the committed answer once: %q", messages)
	}
}

func TestExpiredQuestionFailedCompletionDoesNotSendToIdleAsker(t *testing.T) {
	database := todoTestDB(t)
	e := newAskTodoEnv(t, database)
	ix, _ := e.expire()
	todo := e.onlyAskTodo()
	rejectExpiredAnswerWrite(t, database, "todos", `NEW.done_at IS NOT NULL AND OLD.done_at IS NULL`)
	if code := answerExpiredQuestion(e, todo.ID, ix.CreatedAt, map[string]any{"option": "Wait"}); code != 503 {
		t.Fatalf("failed completion must not acknowledge acceptance: HTTP %d", code)
	}
	if len(e.told("c1x1x1")) != 0 || e.get(todo.ID).DoneAt != nil || expiredQuestionPayload(e, todo.ID).Ask == nil {
		t.Fatal("an idle recipient must not see an answer that failed its acceptance transaction")
	}
}
