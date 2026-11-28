package assistant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
)

// A cut-off chain-of-thought line coming back from any of the three short naming purposes is dropped at its own
// write-back boundary; ordinary answers still name the title.
func TestShortTitleSitesDropThinkingLeaks(t *testing.T) {
	ctx := context.Background()

	// todo.title: the model line becomes "" and nameTodoLater then keeps the title it has.
	e := newTodoEnv(t, nil)
	e.f.mu.Lock()
	e.f.todoTitleReply = "We need answer title"
	e.f.mu.Unlock()
	if got := e.s.todoTitleFromModel(ctx, "some long task prompt that needs a title"); got != "" {
		t.Fatalf("leaked todo title = %q, want empty (keep the old title)", got)
	}
	e.f.mu.Lock()
	e.f.todoTitleReply = "修复登录崩溃"
	e.f.mu.Unlock()
	if got := e.s.todoTitleFromModel(ctx, "some long task prompt that needs a title"); got != "修复登录崩溃" {
		t.Fatalf("normal todo title = %q, want 修复登录崩溃", got)
	}

	// agent.title: tabSlug must never turn monologue into "we-need-answer-title".
	f := newFakeHub()
	f.tabReply = "We need answer title"
	s := newTestService(t, f)
	if got := s.tabNameWithTimeout(ctx, "fix the login crash", time.Second); got != "" {
		t.Fatalf("leaked tab name = %q, want empty", got)
	}
	f.tabReply = "Fix Login Crash"
	if got := s.tabNameWithTimeout(ctx, "fix the login crash", time.Second); got != "fix-login-crash" {
		t.Fatalf("normal tab name = %q, want fix-login-crash", got)
	}

	// assistant.title: a leak is never accepted as the topic, even with a known kind prefix; two leaks land on
	// the deterministic explore fallback instead.
	started := time.Date(2026, 11, 1, 16, 30, 0, 0, time.UTC)
	leakSvc, _ := titleService(t, []string{"功能 | We need answer title", "We need answer only"})
	got, final := leakSvc.makeTitle(ctx, started, searchAsk, searchReply, "")
	if strings.Contains(strings.ToLower(got), "we need") {
		t.Fatalf("makeTitle accepted thinking leakage: %q", got)
	}
	if !strings.HasPrefix(got, "1102 | 探索 |") || !final {
		t.Fatalf("double leak title = %q final=%v, want final explore fallback", got, final)
	}

	// One leak spends the single format retry; a clean second answer is accepted.
	retrySvc, rf := titleService(t, []string{"We need answer title", searchTitle})
	got, final = retrySvc.makeTitle(ctx, started, searchAsk, searchReply, "")
	if want := "1102 | " + searchTitle; got != want || !final {
		t.Fatalf("after leak retry got %q final=%v, want %q", got, final, want)
	}
	if n := len(rf.modelCalls()); n != 2 {
		t.Fatalf("leak should use the one retry, calls = %d", n)
	}
}

// titleCaptureService points the legacy "ai" config at a local Messages server recording every request body.
func titleCaptureService(t *testing.T) (*Service, *[]map[string]any, *sync.Mutex) {
	t.Helper()
	var mu sync.Mutex
	bodies := []map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"修复 | 登录崩溃"}],"stop_reason":"end_turn"}`)
	}))
	t.Cleanup(srv.Close)
	key := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(key, []byte("test-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := newFakeHub()
	f.settings = &Settings{Timezone: "Asia/Shanghai"}
	f.hello.Models.AI = ai.Config{BaseURL: srv.URL, APIKeyFile: key, Model: "wire-model"}
	return newTestService(t, f), &bodies, &mu
}

// All three short naming purposes explicitly ask the Direct API to disable thinking on the wire. Removing
// DisableThinking from any one call site makes its subtest fail.
func TestShortTitleRequestsDisableThinkingWire(t *testing.T) {
	ctx := context.Background()
	started := time.Date(2026, 11, 1, 16, 30, 0, 0, time.UTC)

	t.Run("todo.title", func(t *testing.T) {
		s, bodies, mu := titleCaptureService(t)
		if got := s.todoTitleFromModel(ctx, "a task prompt long enough to name"); got == "" {
			t.Fatal("capture answer should produce a title")
		}
		mu.Lock()
		defer mu.Unlock()
		if len(*bodies) == 0 {
			t.Fatal("no model request")
		}
		thinking, _ := (*bodies)[len(*bodies)-1]["thinking"].(map[string]any)
		if thinking["type"] != "disabled" {
			t.Fatalf("todo.title thinking = %#v, want disabled", thinking)
		}
	})

	t.Run("agent.title", func(t *testing.T) {
		s, bodies, mu := titleCaptureService(t)
		_ = s.tabNameWithTimeout(ctx, "fix the login crash", 5*time.Second)
		mu.Lock()
		defer mu.Unlock()
		if len(*bodies) == 0 {
			t.Fatal("no model request")
		}
		thinking, _ := (*bodies)[len(*bodies)-1]["thinking"].(map[string]any)
		if thinking["type"] != "disabled" {
			t.Fatalf("agent.title thinking = %#v, want disabled", thinking)
		}
	})

	t.Run("assistant.title", func(t *testing.T) {
		s, bodies, mu := titleCaptureService(t)
		if got, _ := s.makeTitle(ctx, started, searchAsk, searchReply, ""); !strings.Contains(got, "登录崩溃") {
			t.Fatalf("capture title = %q", got)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(*bodies) != 1 {
			t.Fatalf("makeTitle calls = %d, want 1", len(*bodies))
		}
		thinking, _ := (*bodies)[0]["thinking"].(map[string]any)
		if thinking["type"] != "disabled" {
			t.Fatalf("assistant.title thinking = %#v, want disabled", thinking)
		}
	})
}
