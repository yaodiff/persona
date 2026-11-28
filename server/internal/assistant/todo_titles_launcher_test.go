package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/proto"
)

// titleRunDevs is a node that answers every model.run at once and keeps the params it was sent.
type titleRunDevs struct {
	viaDevs
	mu   sync.Mutex
	runs []proto.ModelRunParams
}

func (d *titleRunDevs) CallNode(_ context.Context, id, method string, params json.RawMessage) (json.RawMessage, error) {
	if method != proto.MethodModelRun || id != "server" {
		return nil, fmt.Errorf("unexpected %s on %s", method, id)
	}
	var p proto.ModelRunParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	d.mu.Lock()
	d.runs = append(d.runs, p)
	d.mu.Unlock()
	return json.Marshal(proto.ModelRunAnswer{Text: "fix-login", Model: "m-run"})
}

// A to-do title made in the background waits 50 s on a launcher (a cold CLI), inside nameTodoLater's minute; on an API
// connection, and where a caller waits (the retitle route), it keeps 15 s (#13443).
func TestLaterTodoTitleWaitsForACLIOnALauncher(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	f.ownModels = true
	f.hello.Models.AI = ai.Config{BaseURL: "http://127.0.0.1:1", APIKey: "test-key", Model: "relay-model"}
	s := newServiceOn(t, f, &downStore{memStore: &memStore{}})
	if _, got, err := s.laterTodoTitleModel(ctx); err != nil || got != 15*time.Second {
		t.Fatalf("on an API connection: %s; want 15s", got)
	}

	devs := &titleRunDevs{}
	s.devs = devs
	model := fmt.Sprintf("m-todo-title-%d", time.Now().UnixNano()) // the launcher gates are process-wide
	if r := modelRequest(s, "PATCH", "kb", fmt.Sprintf(`{"revision":0,"provider":"launcher.claude","model":%q}`, model)); r.Code != 200 {
		t.Fatalf("pick claude: %d %s", r.Code, r.Body.String())
	}
	if _, got, err := s.laterTodoTitleModel(ctx); err != nil || got != 50*time.Second {
		t.Fatalf("on a launcher: %s; want 50s", got)
	}
	s.nameTodoLater(ctx, "no-such-todo", "from", "fix the login crash on the phone")
	s.todos.titling.Wait()
	if s.todoTitleFromModel(ctx, "fix the login crash") == "" {
		t.Fatal("no title where a caller waits")
	}
	devs.mu.Lock()
	defer devs.mu.Unlock()
	if len(devs.runs) != 2 {
		t.Fatalf("%d runs; want 2", len(devs.runs))
	}
	if ms := devs.runs[0].TimeoutMS; ms <= 15_000 || ms > 50_000 {
		t.Fatalf("a background to-do title on a launcher waits %d ms; want about 50 s", ms)
	}
	if ms := devs.runs[1].TimeoutMS; ms > 15_000 {
		t.Fatalf("a to-do title its caller waits for waits %d ms; want 15 s", ms)
	}
}
