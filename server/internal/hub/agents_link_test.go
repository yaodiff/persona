package hub

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
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The Agents business is the manager's (internal/assistant, agents_*.go); these are the hub's ends: the lists show its
// report, the turns reach it, its cards are shown and answered, it fills in the starts.

// A test without a manager does not wait for one either.
func init() { managerStartWait = 0 }

// workNode is a fake node: it records session.send and session.start and answers session.items with reply as the
// turn's last answer.
type workNode struct {
	mu     sync.Mutex
	sent   []string // "session: text"
	starts []map[string]any
	ask    string
	reply  string
}

func (n *workNode) sends() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.sent...)
}

func (n *workNode) lastStart(t *testing.T) map[string]any {
	t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.starts) == 0 {
		t.Fatal("nothing started")
	}
	return n.starts[len(n.starts)-1]
}

func workMachine(t *testing.T, h *Hub) (*Machine, *workNode) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h2nR, h2nW := io.Pipe()
	n2hR, n2hW := io.Pipe()
	hubSide, nodeSide := rpc.New("h", h2nW), rpc.New("n", n2hW)
	n := &workNode{}
	nodeSide.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		n.mu.Lock()
		defer n.mu.Unlock()
		switch method {
		case "session.send":
			n.sent = append(n.sent, p["session_id"].(string)+": "+p["text"].(string))
		case "session.start":
			n.starts = append(n.starts, p)
			return map[string]any{"session": proto.Session{SessionID: "new-1", Cwd: "/code"}}, nil
		case "session.items":
			items := []proto.Item{}
			if n.ask != "" {
				items = append(items, proto.Item{Kind: proto.KindText, Role: "user", Text: n.ask})
			}
			if n.reply != "" {
				items = append(items, proto.Item{Kind: proto.KindText, Role: "assistant", Text: n.reply})
			}
			return map[string]any{"items": items}, nil
		}
		return map[string]any{}, nil
	}
	go func() { _ = hubSide.Serve(ctx, n2hR) }()
	go func() { _ = nodeSide.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = h2nW.Close(); _ = n2hW.Close() })
	m := newMachine(h, MachineConfig{ID: "box", Name: "示例电脑", Kind: "local"})
	m.peer, m.online = hubSide, true
	registerMachine(h, m)
	return m, n
}

// workModel answers every model call with reply.
func workModel(t *testing.T, reply string) ai.Config {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": reply}}})
	}))
	t.Cleanup(srv.Close)
	key := filepath.Join(t.TempDir(), "key")
	_ = os.WriteFile(key, []byte("k"), 0o600)
	return ai.Config{BaseURL: srv.URL, APIKeyFile: key, Model: "m"}
}

func workSession(t *testing.T, h *Hub, m *Machine, sid string) proto.Session {
	t.Helper()
	s, ok := h.decoratedSession(m, sid)
	if !ok {
		t.Fatalf("no session %s", sid)
	}
	return s
}

func pendingWorkCards(h *Hub) []proto.Interaction {
	var out []proto.Interaction
	for _, ix := range h.pendingInteractions() {
		if ix.ToolName == workCardTool {
			out = append(out, ix)
		}
	}
	return out
}

// End to end with the manager: a turn ends on the hub, the manager triages it (on the hub's model), puts a card up
// for the decision, the user answers it on the hub, and the answer goes to the agent.
func TestAgentWorkOverTheBus(t *testing.T) {
	h := newTestHub(t)
	database := setupTestDB(t)
	// The shared fixture does not truncate session_work; repeated runs must not restore the previous turn.
	if err := database.PruneSessionWork(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	setManagerJSON(t, h, "agents", map[string]any{"triage": map[string]any{"settle_seconds": 1}})
	m, n := workMachine(t, h)
	useModel(t, h, workModel(t, `{"phase":"blocked","on":"user","line":"等你选方案",
		"questions":[{"question":"用哪个方案？","header":"方案","options":[{"label":"用 D4","description":"推荐"},{"label":"用 D2"}]}]}`))
	n.reply = "用哪个方案？D2 / D4 选哪个？"
	connectAssistant(t, h, database) // Owned question answers require the manager's durable Store.
	mux := h.routes()
	if code, out := call(t, mux, "PUT", "/api/v1/agents/triage", h.cfg.Token, `{"enabled":true,"decide":true,"auto_continue":true,"wake":true}`); code != 200 {
		t.Fatalf("switches: %d %v", code, out)
	}
	if code, out := call(t, mux, "GET", "/api/v1/agents/triage", h.cfg.Token, ""); code != 200 || out["decide"] != true {
		t.Fatalf("GET switches: %d %v", code, out)
	}
	at := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	working := proto.Session{SessionID: "s1", Title: "选方案", Cwd: "/code/app", Status: proto.StatusWorking, UpdatedAt: at}
	m.setSessions([]proto.Session{working})
	done := working
	done.Status, done.LastText = proto.StatusDone, n.reply
	m.setSessions([]proto.Session{done}) // the turn ends: session.turn_ended goes to the manager
	var card proto.Interaction
	eventually(t, 20*time.Second, "the manager's card", func() bool {
		cards := pendingWorkCards(h)
		if len(cards) == 1 {
			card = cards[0]
		}
		return len(cards) == 1
	})
	if card.SessionTitle == "" || card.Questions[0].Options[0].Label != "用 D4" {
		t.Fatalf("card %+v", card)
	}
	eventually(t, 10*time.Second, "the work on the list", func() bool {
		s := workSession(t, h, m, "s1")
		return s.State == proto.StateNeedsYou && s.Work != nil && s.Work.InteractionID == card.ID
	})
	if err := h.resolve(card.ID, proto.Resolution{Answers: map[string]string{"用哪个方案？": "用 D4"}}); err != nil {
		t.Fatal(err)
	}
	status, kept, err := database.InteractionOutcome(t.Context(), card.ID)
	if err != nil || status != "resolved" || kept == nil || kept.Answers["用哪个方案？"] != "用 D4" {
		t.Fatalf("the accepted answer was not durably recorded: status=%q resolution=%+v err=%v", status, kept, err)
	}
	eventually(t, 10*time.Second, "the answer at the agent", func() bool {
		got := n.sends()
		return len(got) == 1 && strings.Contains(got[0], "用哪个方案？ → 用 D4")
	})
	eventually(t, 10*time.Second, "the card off the list", func() bool {
		s := workSession(t, h, m, "s1")
		return len(pendingWorkCards(h)) == 0 && s.Work != nil && s.Work.InteractionID == ""
	})
	// Off: the lists show no work.
	if code, _ := call(t, mux, "PUT", "/api/v1/agents/triage", h.cfg.Token, `{"enabled":false}`); code != 200 {
		t.Fatal("switch off")
	}
	eventually(t, 10*time.Second, "no work while off", func() bool { return workSession(t, h, m, "s1").Work == nil })
}

// POST /api/v1/sessions: the manager says what a new Agent runs on (Models › Agents, a model picked for this start
// over it) and gives its tab a local placeholder before naming it in the background. A start waits a little for a manager that is not connected yet (a hub that just
// started); while there is none the start is refused.
func TestAgentStartAsksTheManager(t *testing.T) {
	h := newTestHub(t)
	h.store = &newAgentStore{memStore: newMemStore(), b: assistantwire.ModelBinding{Provider: "launcher.work", Model: "m-1"}}
	useModel(t, h, workModel(t, "fix-login"))
	m, n := workMachine(t, h)
	m.mu.Lock()
	m.info.Protocol = nodeproto.Version
	m.info.Capabilities = []string{nodeproto.SessionStartModelAliases}
	m.mu.Unlock()
	mux := h.routes()
	start := func(body string) func(string) string {
		t.Helper()
		if code, out := call(t, mux, "POST", "/api/v1/sessions", h.cfg.Token, body); code != 200 {
			t.Fatalf("start %s: %d %v", body, code, out)
		}
		p := n.lastStart(t)
		return func(k string) string { v, _ := p[k].(string); return v }
	}
	// Without the manager there is no start: the machines are its to reach (devices_link.go).
	h.agentWork.mu.Lock()
	h.agentWork.everUp = true // it was there, and went away
	h.agentWork.mu.Unlock()
	if code, out := call(t, mux, "POST", "/api/v1/sessions", h.cfg.Token, `{"machine_id":"box","cwd":"/code","prompt":"x"}`); code != 503 ||
		out["code"] != "assistant_offline" {
		t.Fatalf("without the manager: %d %v", code, out)
	}
	n.mu.Lock()
	started := len(n.starts)
	n.mu.Unlock()
	if started != 0 {
		t.Fatal("started without the manager")
	}
	h.agentWork.mu.Lock()
	h.agentWork.everUp = false
	h.agentWork.mu.Unlock()
	var p func(string) string

	// The hub just started: a start waits for the manager connecting.
	saved := managerStartWait
	managerStartWait = 20 * time.Second
	t.Cleanup(func() { managerStartWait = saved })
	h.agentWork.mu.Lock()
	h.agentWork.firstWait = time.Time{}
	h.agentWork.mu.Unlock()
	waited := make(chan func(string) string, 1)
	go func() { waited <- start(`{"machine_id":"box","cwd":"/code","prompt":"修一下登录页的报错"}`) }()
	time.Sleep(300 * time.Millisecond)
	connectAssistant(t, h)
	select {
	case p = <-waited:
	case <-time.After(15 * time.Second):
		t.Fatal("the start did not go on once the manager connected")
	}
	if p("launcher") != "work" || p("tab_name") != "persona" {
		t.Fatalf("waited for the manager: %q %q", p("launcher"), p("tab_name"))
	}

	p = start(`{"machine_id":"box","cwd":"/code","prompt":"修一下登录页的报错"}`)
	if p("launcher") != "work" || p("model") != "m-1" || p("model_fingerprint") == "" || p("tab_name") != "persona" {
		t.Fatalf("with the manager: %q %q %q", p("launcher"), p("model"), p("tab_name"))
	}
	p = start(`{"machine_id":"box","cwd":"/code","prompt":"x","launcher":"other","model":"picked","tab_name":"mine"}`)
	start(`{"machine_id":"box","cwd":"/code","prompt":"x","model_aliases":{"opus_model":"vendor/strong"}}`)
	if aliases, _ := n.lastStart(t)["model_aliases"].(map[string]any); aliases["opus_model"] != "vendor/strong" {
		t.Fatal("HTTP start lost model aliases before reaching the node")
	}
	if p("launcher") != "other" || p("model") != "picked" || p("tab_name") != "mine" {
		t.Fatalf("picked for this start: %q %q %q", p("launcher"), p("model"), p("tab_name"))
	}
	p = start(`{"machine_id":"box","cwd":"/code","resume_session_id":"old-1"}`)
	if p("launcher") != "" || p("model") != "" || p("tab_name") != "" {
		t.Fatalf("a resume keeps what it runs on: %q %q %q", p("launcher"), p("model"), p("tab_name"))
	}
	// The Feishu card's start and an Agent started in the background ask for a tab name alone.
	if got := h.autoTabName(context.Background(), "修一下登录页的报错"); got != "fix-login" {
		t.Fatalf("tab name %q", got)
	}
}
