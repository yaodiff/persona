package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

const conclusionCrashChild = "PERSONA_TEST_CONCLUSION_CRASH_CHILD"
const conclusionCrashDatabase = "PERSONA_TEST_CONCLUSION_DATABASE_URL"

// TestSupervisionConclusionSurvivesManagerRestart compares a returned
// Feishu failure with an exit during that same first post. In either case a
// fresh manager on the same PostgreSQL database should deliver the conclusion once, under its original key (#10393).
func TestSupervisionConclusionSurvivesManagerRestart(t *testing.T) {
	needsLarkLayer(t)
	// The parent creates and owns the database; the child must reconnect to that exact database to exercise restart.
	rawURL := os.Getenv(conclusionCrashDatabase)
	if os.Getenv(conclusionCrashChild) == "" {
		rawURL = newMigratedDatabase(t, db.ManagerSet)
	} else {
		dsn, err := url.Parse(rawURL)
		if err != nil || dsn.Scheme != "postgres" || !strings.HasPrefix(dsn.Path, "/persona_owndb_t_") {
			t.Fatal("child requires the parent's disposable database")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	database, err := db.Open(ctx, rawURL, db.ManagerSet)
	if err != nil {
		t.Fatal("open the disposable conclusion database:", err)
	}
	t.Cleanup(database.Close)

	if mode := os.Getenv(conclusionCrashChild); mode != "" {
		runConclusionFirstPost(t, database, mode)
		return
	}

	t.Run("first post returned an error", func(t *testing.T) {
		assertConclusionAfterRestart(ctx, t, database, rawURL, "return-error")
	})
	t.Run("manager exited during first post", func(t *testing.T) {
		assertConclusionAfterRestart(ctx, t, database, rawURL, "exit")
	})
}

func assertConclusionAfterRestart(ctx context.Context, t *testing.T, database *db.DB, rawURL, mode string) {
	if err := database.TruncateForTest(ctx); err != nil {
		t.Fatal("reset the disposable audit database:", err)
	}
	child, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, child, "-test.run=^TestSupervisionConclusionSurvivesManagerRestart$")
	cmd.Env = []string{conclusionCrashDatabase + "=" + rawURL, conclusionCrashChild + "=" + mode, "TMPDIR=" + t.TempDir()}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if mode == "exit" {
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 86 {
			t.Fatalf("expected the test manager to exit at the provider boundary, got %v\n%s", err, out)
		}
	} else if err != nil {
		t.Fatalf("expected the first send to return an error and task_review to finish, got %v\n%s", err, out)
	}
	_, tail, found := strings.Cut(string(out), "test: reached first Feishu post with key ")
	key, _, _ := strings.Cut(tail, "\n")
	if !found || !strings.HasPrefix(key, "pv:") {
		t.Fatalf("missing the keyed post marker:\n%s", out)
	}

	s, f := newQuietServiceOn(t, database)
	f.answerApp(AppFeishu, FeishuStatusGet, conclusionCrashReady)
	lark := newCardLark(f, "结论：")
	s.verifySupervisedAsks(ctx) // the acceptance loop runs this at startup
	todo := onlyTodo(s)
	if todo.ID == "" || todo.DoneAt == nil || todo.DoneBy != db.DoneByReview || !supervisorVerified(todo) {
		t.Fatalf("the accepted completion did not survive: id=%q done=%v by=%q", todo.ID, todo.DoneAt, todo.DoneBy)
	}
	_, askRecovered := s.askCopy(todo.ID)
	s.retryAcceptance(todo.DoneAt.Add(10 * time.Minute))
	_, sweepKeys := lark.sent()
	reviewTask(t, s, todo.ID, map[string]any{
		"verdict": "accepted", "outcome": "done", "reason": "Review complete",
		"checks": completeReviewChecks(todo),
	})
	s.retryAcceptance(todo.DoneAt.Add(20 * time.Minute))
	cards, keys := lark.sent()
	if len(cards) != 1 || len(keys) != 1 || keys[0] != key {
		t.Fatalf("task is durably accepted, recovered ask=%v, sends after sweep=%d, sends after repeat review and sweep=%d, cards=%d; want one delivered card using the original key",
			askRecovered, len(sweepKeys), len(keys), len(cards))
	}
	t.Logf("recovered ask=%v, sends after sweep=%d, final sends=%d, cards=%d, key unchanged",
		askRecovered, len(sweepKeys), len(keys), len(cards))
}

func runConclusionFirstPost(t *testing.T, database *db.DB, mode string) {
	t.Helper()
	s, f, todo := newSupervisedServiceOn(t, database)
	f.answerApp(AppFeishu, FeishuStatusGet, conclusionCrashReady)
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var card struct {
			Card string `json:"card"`
			UUID string `json:"uuid"`
		}
		if err := json.Unmarshal(p, &card); err != nil {
			return nil, err
		}
		if strings.Contains(card.Card, "结论：") {
			fmt.Fprintln(os.Stdout, "test: reached first Feishu post with key", card.UUID)
			if mode == "exit" {
				os.Exit(86) // terminate before the fake provider accepts the card
			}
			return nil, errLarkDown
		}
		return map[string]any{"message_id": "om_other"}, nil
	})
	reviewTask(t, s, todo.ID, map[string]any{
		"verdict": "accepted", "outcome": "done", "reason": "Review complete",
		"checks": completeReviewChecks(todo),
	})
	if mode == "exit" {
		t.Fatal("task_review returned without calling Feishu")
	}
}

func conclusionCrashReady(json.RawMessage) (any, error) {
	return FeishuStatus{
		Ready: true, Enabled: true, Owner: "ou_test", PublicURL: "https://hub.example.com",
		Features: FeishuFeatures{Main: true, Cards: true, Reports: true, OnlyConclusions: true},
	}, nil
}
