package assistant

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A recommendation the node refused with a code of its own (a dialog open in the agent's terminal) answers with that
// code and says what to do in the request's language. It used to be 502 "internal" with the reason folded into the
// sentence, which the web app showed as "the service could not complete this request (HTTP 502)" however often it was
// pressed (#7343). Nothing is taken, and the button works again. The status is 423, not the 409 other node calls
// answer: the iOS and Android apps already installed read any 409 on this route as "already handled" and lock the
// button.
func TestAcceptRouteSaysTheTerminalDialogBlocksIt(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.ZhHans, i18n.En} {
		s, f, todo, _, _ := conclusionService(t, false)
		waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")                                                            // i18n-ignore
		reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"}) // i18n-ignore
		a := askOf(s, todo.ID)
		f.mu.Lock()
		f.failSend = &rpc.Error{Code: "dialog_open", Message: "a dialog is open in the terminal"}
		f.nodes = map[string]nodeAnswer{"session.screen": func(string, json.RawMessage) (any, error) {
			return map[string]any{"text": rateLimitScreen(t)}, nil
		}}
		f.mu.Unlock()
		asked, _ := json.Marshal(a.AskedAt)
		status, body := serveJSON(t, s, "POST", "/api/v1/todos/"+todo.ID+"/accept", string(lang), `{"asked_at":`+string(asked)+`}`)
		want := i18n.T(lang, "rpc-errors.dialog_open")
		if status != http.StatusLocked || body["code"] != "dialog_open" || body["error"] != want || body["delivered"] != "none" {
			t.Fatalf("%s: %d %v, want 423 dialog_open %q", lang, status, body, want)
		}
		// what the card says and where it sends the owner: the dialog's own first line, read off a Codex screen whose node
		// did not say it, and the agent whose terminal it is
		if body["dialog"] != "Approaching rate limits" || body["machine_id"] == nil || body["machine_id"] == "" || body["session_id"] == nil || body["session_id"] == "" {
			t.Fatalf("%s: %v, want the dialog's first line and the agent", lang, body)
		}
		if got := askOf(s, todo.ID); !got.AcceptedAt.IsZero() {
			t.Fatalf("%s: taken after all: %+v", lang, got)
		}
	}
}

// The owner's OK on a decision whose Agent's node answers "Authentication required" (no code) is made anyway (#17431):
// it used to be 502 accept_not_taken, which the web app showed as "the service could not complete this request (HTTP
// 502)" on every one of seven presses, and nothing was kept. Now the decision is recorded, the round ends, the
// supervisor is handed it with the reason, and the reply says it did not reach the Agent. Nothing is sent again.
func TestAcceptRouteRecordsADecisionWhoseAgentCannotTakeIt(t *testing.T) {
	database := todoTestDB(t)
	s, f, todo, _, _ := conclusionServiceOn(t, database, false)
	waitsForReview(t, s, f, todo.ID, "两种走法：A 先灰度，B 直接全量。")                                                            // i18n-ignore
	reviewTask(t, s, todo.ID, map[string]any{"verdict": "needs_user", "reason": "先灰度还是直接全量", "recommend": "先灰度 10%"}) // i18n-ignore
	a := askOf(s, todo.ID)
	f.mu.Lock()
	f.failSend = &rpc.Error{Message: "Authentication required"}
	f.mu.Unlock()
	asked, _ := json.Marshal(a.AskedAt)
	status, body := serveJSON(t, s, "POST", "/api/v1/todos/"+todo.ID+"/accept", "en", `{"asked_at":`+string(asked)+`}`)
	if status != http.StatusOK || body["ok"] != true || body["delivered"] != "none" || body["reason"] != "Authentication required" {
		t.Fatalf("%d %v, want ok, delivered none, the reason", status, body)
	}
	list, err := database.ListTodoDecisions(t.Context(), todo.ID, true)
	if err != nil || len(list) != 1 || list[0].Choice != "先灰度 10%" { // i18n-ignore
		t.Fatalf("decision not recorded: %+v %v", list, err)
	}
	if got := askOf(s, todo.ID); got != nil && got.AcceptedAt.IsZero() {
		t.Fatalf("the round still waits: %+v", got)
	}
	key := "undelivered|" + a.AskedAt.UTC().Format(time.RFC3339Nano)
	s.supervise.dedupMu.Lock()
	s.supervise.mu.Lock()
	g, pending := s.supervise.pending[todo.ID+"|"+sigIdle]
	told := s.supervise.sent[todo.ID+"|"+sigIdle] == key
	s.supervise.mu.Unlock()
	s.supervise.dedupMu.Unlock()
	if (!pending || g.Key != key || !strings.Contains(g.Detail, "Authentication required")) && !told {
		t.Fatalf("supervisor not handed it: %+v (told %v)", g, told)
	}
	sent := len(sentToAgent(f))
	if status, _ := serveJSON(t, s, "POST", "/api/v1/todos/"+todo.ID+"/accept", "en", `{"asked_at":`+string(asked)+`}`); status != http.StatusConflict || len(sentToAgent(f)) != sent {
		t.Fatalf("a second press: %d, %d sends", status, len(sentToAgent(f))-sent)
	}
	s.waitWorkEvents()
}

// rateLimitScreen is Codex 0.169's "Approaching rate limits" picker as session.screen reads it.
func rateLimitScreen(t *testing.T) string {
	b, err := os.ReadFile(filepath.Join("..", "screen", "testdata", "codex-0.169-rate-limit-picker.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
