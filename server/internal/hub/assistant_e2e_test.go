package hub

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/proto"
)

// The assistant as `personad assistant` runs it, end to end: the hub's whole HTTP stack on a test server, the
// assistant (internal/assistant, with its schedule) dialing the hub's bus with a token from the database and keeping
// its state in the same Postgres, an app on the WebSocket, fake nodes for the assistant's machine and an agent's.
func TestAssistantOverTheBus(t *testing.T) {
	h, srv, _ := newBusHub(t)
	store := managerDB(t, h) // the assistant's state: its settings and tables
	ctx := context.Background()
	keys := []string{"assistant", "assistant.tasks", "assistant.routines", "assistant.followups", "assistant.runs",
		"assistant.titles", "assistant.archived", "assistant.relay"}
	clear := func() {
		for _, k := range keys {
			_ = store.PutSetting(context.Background(), k, nil)
		}
	}
	clear()
	t.Cleanup(clear)
	old := taskSettleDelay
	taskSettleDelay = func(NotifyConfig) time.Duration { return 0 }
	t.Cleanup(func() { taskSettleDelay = old })
	n := newAssistantNode(t, h) // the assistant's machine, "server"
	n.m.info.Home = "/home/me"
	n.m.online = true // runTask's execution guard sees this in-process peer as an online machine (#6806, as #11718).
	now := time.Now().UTC()
	editSessions(n.m, func([]proto.Session) []proto.Session {
		return []proto.Session{{SessionID: "main-1", Cwd: convDir, Title: "长期对话", UpdatedAt: now},
			{SessionID: "side-2", Cwd: convDir, Title: "1030 | 探索 | 出游", UpdatedAt: now.Add(-time.Hour)}}
	})
	n.known["main-1"], n.known["side-2"] = true, true
	box := connectTaskNode(t, h, "box") // where a task's agent runs
	if err := store.PutSetting(ctx, "assistant", assistant.Settings{MachineID: "server", Main: "main-1", Timezone: "Asia/Shanghai"}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSetting(ctx, "assistant.tasks", []assistant.Task{{ID: "r1", Title: "早报", Prompt: "给我今天的早报",
		Repeat: "once", At: now.Add(-time.Second), Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	app := dialTestApp(t, h, srv)
	a := dialAssistant(t, h, srv.URL, true)

	do := func(method, path, body string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	mcp := func(name string, args any) proto.ToolResult {
		t.Helper()
		raw, _ := json.Marshal(args)
		params, _ := json.Marshal(map[string]any{"name": name, "args": json.RawMessage(raw), "session_id": "main-1", "assistant": true})
		res, err := nodeAsks(ctx, n.m, "mcp.call", params)
		if err != nil { // the manager is gone (the node's tools are its): as an error result
			return proto.TextResult(err.Error(), true)
		}
		return res.(proto.ToolResult)
	}

	// A scheduled run: the assistant's own loop starts it on its machine (device.call assistant.start); its record
	// is the assistant's route.
	eventually(t, 10*time.Second, "the scheduled run", func() bool {
		n.mu.Lock()
		defer n.mu.Unlock()
		for _, s := range n.sends {
			if b, _ := json.Marshal(s); strings.Contains(string(b), "给我今天的早报") {
				return true
			}
		}
		return false
	})
	eventually(t, 10*time.Second, "its record", func() bool {
		code, body := do("GET", "/api/v1/assistant/runs", "")
		return code == http.StatusOK && strings.Contains(body, `"r1"`)
	})

	// A conversation route (http.request): archived by the assistant, shown so by the hub (its snapshot), the
	// machine's list pushed to the app.
	if code, body := do("POST", "/api/v1/assistant/conversations/side-2/archive", ""); code != http.StatusOK {
		t.Fatalf("archive: %d %s", code, body)
	}
	if !shown(h, "side-2").Archived {
		t.Fatal("the hub does not show it archived")
	}
	if _, body := do("GET", "/api/v1/assistant?archived=1", ""); !strings.Contains(body, `"session_id":"side-2"`) || !strings.Contains(body, `"archived":true`) {
		t.Fatalf("GET /assistant: %s", body)
	}
	app.wait(t, "the sessions push", func(m map[string]any) bool {
		return m["type"] == "sessions" && strings.Contains(m["raw"].(string), `"archived":true`)
	})

	// A task plan: the tool's card reaches the app (ws.broadcast), the app starts it (relayed), its agent starts on box.
	// The main conversation's own plan starts at once unless a task is the owner's to start (decision rights): this one
	// says it takes an irreversible step, so it waits on its card.
	var planIn map[string]any
	_ = json.Unmarshal(planArgs(false), &planIn)
	planIn["tasks"].([]any)[0].(map[string]any)["owner_step"] = "irreversible"
	if r := mcp("task_plan", planIn); r.IsError {
		t.Fatalf("task_plan: %+v", r)
	}
	plan := app.wait(t, "the plan card", func(m map[string]any) bool { return m["type"] == "task_plan" })
	id, _ := plan["plan"].(map[string]any)["id"].(string)
	if code, body := do("POST", "/api/v1/task-plans/"+id+"/start", `{}`); code != http.StatusOK {
		t.Fatalf("start: %d %s", code, body)
	}
	eventually(t, 10*time.Second, "the task's agent", func() bool { s, _ := box.calls(); return len(s) == 1 })
	eventually(t, 10*time.Second, "the task (a todo) following its agent", func() bool {
		for _, todo := range taskTodos(h) {
			if todo.PlanID == id && len(todo.Agents) == 1 && todo.Agents[0].MachineID == "box" {
				return true
			}
		}
		return false
	})

	// notify_user: a push to the app (notify.request).
	if r := mcp("notify_user", map[string]string{"title": "提醒", "body": "早报好了"}); r.IsError {
		t.Fatalf("notify_user: %+v", r)
	}
	app.wait(t, "the push", func(m map[string]any) bool {
		return m["type"] == "notify" && m["kind"] == "assistant" && m["title"] == "提醒" && m["body"] == "早报好了"
	})

	// ask_user: a card on the phone (its snapshot's interactions); the answer goes back as manager.action.
	asked := make(chan proto.ToolResult, 1)
	go func() {
		asked <- mcp("ask_user", map[string]any{"why_owner": "visible_to_others", "questions": []map[string]any{{"question": "发吗？", "options": []map[string]string{{"label": "发"}, {"label": "不发"}}}}})
	}()
	card := app.wait(t, "the question card", func(m map[string]any) bool {
		ix, _ := m["interaction"].(map[string]any)
		return m["type"] == "interaction" && ix["session_id"] == "main-1"
	})
	ixID := card["interaction"].(map[string]any)["id"].(string)
	if code, body := do("POST", "/api/v1/interactions/"+ixID+"/resolve", `{"answers":{"发吗？":"发"}}`); code != http.StatusOK {
		t.Fatalf("resolve: %d %s", code, body)
	}
	select {
	case r := <-asked:
		if r.IsError || !strings.Contains(textOf(r), "发吗？ → 发") {
			t.Fatalf("ask_user: %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the answer did not reach the assistant")
	}

	// The connection drops: the assistant dials again by itself and reports its whole snapshot again.
	conn := h.busConnOf(busAssistant)
	h.assistant.mu.RLock()
	seq := h.assistant.seq
	h.assistant.mu.RUnlock()
	_ = conn.ws.CloseNow()
	eventually(t, 10*time.Second, "connected again", func() bool {
		c := h.busConnOf(busAssistant)
		h.assistant.mu.RLock()
		defer h.assistant.mu.RUnlock()
		return c != nil && c != conn && h.assistant.seq > seq
	})
	if !shown(h, "side-2").Archived {
		t.Fatal("lost what it reported")
	}

	// Not running: its routes answer 503 assistant_offline, its tools fail the same way; the hub's own still answer.
	a.stop()
	<-a.done
	eventually(t, 10*time.Second, "gone", func() bool { return h.busConnOf(busAssistant) == nil })
	if code, body := do("GET", "/api/v1/assistant/tasks", ""); code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"assistant_offline"`) {
		t.Fatalf("offline route: %d %s", code, body)
	}
	if r := mcp("notify_user", map[string]string{"title": "x", "body": "y"}); !r.IsError || !strings.Contains(textOf(r), "助手暂时连不上") {
		t.Fatalf("offline tool: %+v", r)
	}
	if code, body := do("GET", "/api/v1/assistant", ""); code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"assistant_offline"`) {
		t.Fatalf("GET /assistant offline (its settings are its own): %d %s", code, body)
	}
	if code, _ := do("GET", "/api/v1/machines", ""); code != http.StatusOK {
		t.Fatalf("the hub's own offline: %d", code)
	}
	if !shown(h, "side-2").Archived {
		t.Fatal("the hub forgot its snapshot while it is away")
	}
	// Back (started again): it answers again.
	dialAssistant(t, h, srv.URL, true)
	if code, body := do("GET", "/api/v1/assistant/tasks", ""); code != http.StatusOK {
		t.Fatalf("back: %d %s", code, body)
	}
}

// `personad hub` runs the assistant as its child process: a fresh token on its stdin, the hub's address in its
// environment; when it exits it is started again.
func TestAssistantProcessIsSupervised(t *testing.T) {
	h, srv, _ := newBusHub(t)
	t.Setenv("PERSONA_TEST_ASSISTANT", "1") // the test binary is the assistant (TestMain)
	t.Setenv("PERSONA_DATABASE_URL", os.Getenv("PERSONA_TEST_DATABASE_URL"))
	t.Setenv("PERSONA_HOME", shortTempDir(t)) // the child keeps its manager state here, not in the real ~/.persona
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var started []*exec.Cmd
	old := assistantCommand
	assistantCommand = func(ctx context.Context) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, exe, "-test.run=^$")
		mu.Lock()
		started = append(started, cmd)
		mu.Unlock()
		return cmd, nil
	}
	t.Cleanup(func() { assistantCommand = old })
	h.cfg.Listen = srv.Listener.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.superviseAssistant(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	var since time.Time // the connected child's
	child := func(n int) bool {
		mu.Lock()
		defer mu.Unlock()
		if len(started) != n {
			return false
		}
		for _, p := range h.busRoster() {
			if p.ID == busAssistant && p.Deploy == "child" && p.Since.After(since) {
				since = p.Since
				return true
			}
		}
		return false
	}
	eventually(t, 30*time.Second, "the child connected", func() bool { return child(1) })
	for _, a := range started[0].Args {
		if strings.HasPrefix(a, "jb_") {
			t.Fatal("the token is on the command line")
		}
	}
	_ = started[0].Process.Kill()
	eventually(t, 30*time.Second, "started again and connected", func() bool { return child(2) })
}

// The hub's child stops when the hub is gone (its stdin closes), so a hub that died leaves no assistant behind.
func TestAssistantStopsWithItsHub(t *testing.T) {
	h, srv, _ := newBusHub(t)
	tok := busToken(t, h, busAssistant, "助手")
	t.Setenv("PERSONA_HOME", shortTempDir(t)) // the child keeps its manager state here, not in the real ~/.persona
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$")
	cmd.Env = append(os.Environ(), "PERSONA_TEST_ASSISTANT=1", "PERSONA_DATABASE_URL="+os.Getenv("PERSONA_TEST_DATABASE_URL"),
		"PERSONA_ASSISTANT_HUB="+srv.URL, "PERSONA_ASSISTANT_DEPLOY=child")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	_, _ = io.WriteString(stdin, tok+"\n")
	eventually(t, 30*time.Second, "connected", func() bool { return h.busConnOf(busAssistant) != nil })
	_ = stdin.Close()
	select {
	case err := <-exited:
		if err != nil {
			t.Fatalf("exited with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("still running with its stdin closed")
	}
}

// testApp is an app on the hub's WebSocket, as the phone connects.
type testApp struct {
	msgs chan map[string]any // each message, with its bytes as "raw"
}

func dialTestApp(t *testing.T, h *Hub, srv *httptest.Server) *testApp {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn, _, err := websocket.Dial(ctx, strings.Replace(srv.URL, "http", "ws", 1)+"/api/v1/ws?token="+h.cfg.Token, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetReadLimit(64 << 20)
	t.Cleanup(func() { _ = conn.CloseNow() })
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"client_hello","features":["ws_notify"]}`)); err != nil {
		t.Fatal(err)
	}
	a := &testApp{msgs: make(chan map[string]any, 1024)}
	go func() {
		for {
			_, b, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var m map[string]any
			if json.Unmarshal(b, &m) == nil {
				m["raw"] = string(b)
				select {
				case a.msgs <- m:
				default:
				}
			}
		}
	}()
	return a
}

// wait is the next message the app gets that ok accepts (the others are skipped).
func (a *testApp) wait(t *testing.T, what string, ok func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case m := <-a.msgs:
			if ok(m) {
				return m
			}
		case <-deadline:
			t.Fatalf("the app did not get %s", what)
		}
	}
}
