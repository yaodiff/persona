package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Work handed to a leader (#3608): task_plan's supervisor, a hand-started agent put under a leader, the New agent
// suggestion, and a leader's plan waiting for Start reported as a decision. All ids and names are placeholders.

func TestAgentRouteRuleTable(t *testing.T) {
	leaders := []routeLeader{{ID: "lead_a", Title: "Infra", Project: "infra"}, {ID: "lead_b", Title: "Android parity", Project: "android"},
		{ID: "lead_c", Title: "Docs"}}
	for _, c := range []struct {
		name    string
		leaders []routeLeader
		dirs    []string
		answer  agentRouteAnswer
		err     error
		want    agentRoute
	}{
		{name: "no leaders: Persona", want: agentRoute{Supervisor: "conv1", Main: true, By: agentRouteDefault}},
		{name: "the directory's project has a leader", leaders: leaders, dirs: []string{"other", "android"},
			answer: agentRouteAnswer{Leader: "lead_c"}, want: agentRoute{Supervisor: "lead_b", Title: "Android parity", By: agentRouteProject}},
		{name: "no project match: the model's leader", leaders: leaders, dirs: []string{"other"},
			answer: agentRouteAnswer{Leader: " lead_c "}, want: agentRoute{Supervisor: "lead_c", Title: "Docs", By: agentRouteModel}},
		{name: "the model named none", leaders: leaders, want: agentRoute{Supervisor: "conv1", Main: true, By: agentRouteDefault}},
		{name: "a made-up leader", leaders: leaders, answer: agentRouteAnswer{Leader: "lead_zz"},
			want: agentRoute{Supervisor: "conv1", Main: true, By: agentRouteDefault}},
		{name: "timeout: Persona", leaders: leaders, answer: agentRouteAnswer{Leader: "lead_c"}, err: context.DeadlineExceeded,
			want: agentRoute{Supervisor: "conv1", Main: true, By: agentRouteDefault}},
		{name: "timeout still takes the project's leader", leaders: leaders, dirs: []string{"infra"}, err: errors.New("503"),
			want: agentRoute{Supervisor: "lead_a", Title: "Infra", By: agentRouteProject}},
	} {
		if got := decideAgentRoute("conv1", c.leaders, c.dirs, c.answer, c.err); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

// The New agent suggestion over HTTP: no leaders asks nothing; a slow model leaves Persona within the budget; a model
// that names a leader is taken.
func TestAgentRouteEndpoint(t *testing.T) {
	s, _ := directionRouteEnv(t, "lead1")
	asked := 0
	s.offers.ask = func(ctx context.Context, system, input string, out any) error {
		asked++
		if !strings.Contains(input, "lead1") || system != prompts.RenderRouteAgent() {
			t.Errorf("asked with %q / %q", system, input)
		}
		return json.Unmarshal([]byte(`{"leader":"lead1"}`), out)
	}
	body := `{"machine_id":"box","cwd":"/code/app","prompt":"Fix the Android sidebar"}`
	route := func() agentRoute {
		code, out := doRoute(t, s, http.MethodPost, "/api/v1/assistant/route/agent", body, "")
		if code != 200 {
			t.Fatalf("route: %d", code)
		}
		raw, _ := json.Marshal(out)
		var r agentRoute
		_ = json.Unmarshal(raw, &r)
		return r
	}
	if code, _ := doRoute(t, s, http.MethodPost, "/api/v1/assistant/route/agent", body, "readonly"); code != http.StatusForbidden {
		t.Fatalf("a read-only device asked: %d", code)
	}
	if r := route(); r.Supervisor != "conv1" || !r.Main || asked != 0 {
		t.Fatalf("no leaders: %+v (asked %d)", r, asked)
	}
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	if r := route(); r.Supervisor != "lead1" || r.Main || r.By != agentRouteModel {
		t.Fatalf("with a leader: %+v", r)
	}
	old := routeTimeout
	routeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { routeTimeout = old })
	s.offers.ask = func(ctx context.Context, _, _ string, _ any) error {
		<-ctx.Done()
		return ctx.Err()
	}
	start := time.Now()
	if r := route(); r.Supervisor != "conv1" || !r.Main || time.Since(start) > time.Second {
		t.Fatalf("a slow model: %+v after %v", r, time.Since(start))
	}
}

// An agent the owner started by hand and put under a leader is that leader's task: its supervision message goes to the
// leader, not the main conversation; its card is in the leader's conversation. Moved back under Persona, the next
// signal goes to the main conversation.
func TestHandStartedAgentUnderALeaderGetsItsSignals(t *testing.T) {
	s, f := newQuietService(t)
	f.judge = `{"verdict":"done","reason":"PR 已合并"}`
	f.known["lead1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	agentTurn(s, f, proto.StatusWorking, "", 0) // the agent the owner started on box

	if code, _ := doRoute(t, s, http.MethodPost, "/api/v1/assistant/agents/box/s1/supervisor", `{"supervisor":"lead1"}`, "readonly"); code != http.StatusForbidden {
		t.Fatalf("a read-only device: %d", code)
	}
	if code, _ := doRoute(t, s, http.MethodPost, "/api/v1/assistant/agents/box/s1/supervisor", `{"supervisor":"side9"}`, ""); code != http.StatusConflict {
		t.Fatalf("not a leader: %d", code)
	}
	if code, _ := doRoute(t, s, http.MethodPost, "/api/v1/assistant/agents/server/lead1/supervisor", `{"supervisor":"lead1"}`, ""); code != http.StatusConflict {
		t.Fatalf("an assistant conversation: %d", code)
	}
	code, out := doRoute(t, s, http.MethodPost, "/api/v1/assistant/agents/box/s1/supervisor", `{"supervisor":"lead1","title":"Android sidebar groups"}`, "")
	if code != 200 || string(out["made"]) != "true" {
		t.Fatalf("put under the leader: %d %s", code, out["error"])
	}
	todo := onlyTodo(s)
	if todo.Supervision == nil || todo.Supervision.Supervisor != "lead1" || len(todo.Agents) != 1 || todo.Agents[0].SessionID != "s1" {
		t.Fatalf("task: %+v %+v", todo, todo.Supervision)
	}
	if items, _, err := s.feed(context.Background(), "lead1", time.Time{}); err != nil || len(items) != 1 || items[0].Ref != todo.ID {
		t.Fatalf("the card is not in the leader's conversation: %+v %v", items, err)
	}

	agentTurn(s, f, proto.StatusDone, "做完了，PR https://github.com/o/r/pull/7 已合并", 0)
	waitFor(t, "the leader's message", func() bool { return len(f.noticesTo("lead1")) == 1 })
	if n := f.noticesTo("lead1")[0]; !strings.Contains(n, "📋 督办") || !strings.Contains(n, "你是这些任务的 leader 对话") {
		t.Fatalf("the leader's message:\n%s", n)
	}
	if got := f.noticesTo("conv1"); len(got) != 0 {
		t.Fatalf("the main conversation was told too: %q", got)
	}

	// Again under the same leader: nothing changes. Moved under Persona: its next signal is the main conversation's.
	if code, out := doRoute(t, s, http.MethodPost, "/api/v1/assistant/agents/box/s1/supervisor", `{"supervisor":"lead1"}`, ""); code != 200 ||
		string(out["made"]) != "false" {
		t.Fatalf("again: %d %s", code, out["made"])
	}
	if code, _ := doRoute(t, s, http.MethodPost, "/api/v1/assistant/agents/box/s1/supervisor", `{}`, ""); code != 200 {
		t.Fatalf("moved under Persona: %d", code)
	}
	if v := todoNow(s, todo.ID).Supervision; v == nil || v.Supervisor != "conv1" {
		t.Fatalf("moved: %+v", v)
	}
	if items, _, err := s.feed(context.Background(), "conv1", time.Time{}); err != nil || !slices.ContainsFunc(items, func(it FeedItem) bool { return it.Ref == todo.ID }) {
		t.Fatalf("the card did not follow the task: %+v %v", items, err)
	}
	if code, _ := doRoute(t, s, http.MethodPost, "/api/v1/assistant/agents/box/nope/supervisor", `{}`, ""); code != http.StatusNotFound {
		t.Fatalf("an unknown session: %d", code)
	}
}

// task_plan's supervisor: the main conversation hands the plan to a leader, whose tasks they are; nobody else may, and
// only to a current leader.
func TestTaskPlanSupervisor(t *testing.T) {
	s, f := newQuietService(t)
	f.known["lead1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "side2", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	plan := func(self, supervisor string) (string, error) {
		raw, _ := json.Marshal(map[string]any{"supervisor": supervisor, "tasks": []map[string]any{{
			"title": "修登录崩溃", "prompt": "iPad 横屏时登录页崩溃，修好并补测试", "machine": "box", "cwd": "/code/app", "readonly": true,
			"why": "这个仓库最近在 box 上", "done_when": []string{"PR 合并到 main"}, "expect_minutes": 30,
		}}})
		return s.toolTaskPlan(context.Background(), self, raw)
	}
	if _, err := plan("side2", "lead1"); err == nil || !strings.Contains(err.Error(), "only the main conversation") {
		t.Fatalf("a side conversation handed work to a leader: %v", err)
	}
	if _, err := plan("conv1", "side2"); err == nil || !strings.Contains(err.Error(), "not a leader") {
		t.Fatalf("handed to a conversation that is no leader: %v", err)
	}
	res, err := plan("conv1", "lead1")
	if err != nil || !strings.Contains(res, "Leader conversation lead1 supervises them") {
		t.Fatalf("handed to the leader: %s %v", res, err)
	}
	waitFor(t, "the task", func() bool { return len(onlyTodo(s).Agents) == 1 })
	if v := onlyTodo(s).Supervision; v == nil || v.Supervisor != "lead1" || len(v.DoneWhen) != 1 {
		t.Fatalf("supervision: %+v", v)
	}
}

// A plan of a leader's waiting for the owner's Start is a decision report, told once (not again after a restart);
// the main conversation's own plans, and started ones, are not reported.
func TestLeaderPlanWaitingIsADecision(t *testing.T) {
	st := &planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}
	env := func() *todoEventEnv {
		e := newTodoEventEnv(t, st)
		if err := e.s.ensureTodosLoaded(context.Background()); err != nil {
			t.Fatal(err)
		}
		setSettings(t, e.s, func(v *Settings) { v.Main, v.Leaders = "conv_main", []string{"conv_lead_a"} })
		return e
	}
	e := env()
	ctx := context.Background()
	e.s.leaderReportsSync(ctx, time.Now(), false) // reports start now
	main := e.s.settings(ctx).Main
	put := func(conv, supervisor, state string, titles ...string) *TaskPlan {
		p := &TaskPlan{ID: "plan_" + randomHex(4), Conversation: conv, State: state, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		for _, title := range titles {
			p.Tasks = append(p.Tasks, PlanTask{Title: title, Supervisor: supervisor})
		}
		if err := e.s.savePlan(ctx, p); err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.appendFeed(ctx, conv, feedPlan, p.ID, map[string]any{"plan": p}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	own := put("conv_lead_a", "", planOpen, "Android sidebar groups", "Tablet layout")
	handed := put(main, "conv_lead_a", planOpen, "Fix push after redeploy")
	put(main, "", planOpen, "A plan of the main conversation's own")
	put("conv_lead_a", "", planStarted, "Started already")

	if got := e.reports(time.Now(), false); len(got) != 0 {
		t.Fatalf("a change's sync read the plans unasked: %+v", got)
	}
	e.s.lreports.plansDue.Store(true) // as task_plan sets it
	got := e.reports(time.Now(), false)
	if len(got) != 2 {
		t.Fatalf("reports: %+v", got)
	}
	byPlan := map[string]assistantwire.LeaderReportEvent{got[0].Plan: got[0], got[1].Plan: got[1]}
	if r := byPlan[own.ID]; r.Kind != assistantwire.LeaderDecision || r.Leader != "conv_lead_a" || r.Todo != "" ||
		!strings.Contains(r.Text, "Android sidebar groups") || !strings.Contains(r.Text, "Tablet layout") || r.At.IsZero() {
		t.Fatalf("the leader's own plan: %+v", r)
	}
	if r := byPlan[handed.ID]; r.Kind != assistantwire.LeaderDecision || r.Leader != "conv_lead_a" || !strings.Contains(r.Text, "Fix push") {
		t.Fatalf("the plan handed to the leader: %+v", r)
	}
	for _, r := range got {
		if r.Level != "" {
			t.Fatalf("a level with push tiering off: %+v", r)
		}
	}
	if again := e.reports(time.Now(), true); len(again) != 0 {
		t.Fatalf("told again: %+v", again)
	}
	// With push tiering on a plan waiting for Start is P1 (a briefing line, no alert), though its kind is a decision.
	on := true
	e.s.setQuietForTest(&on)
	late := put("conv_lead_a", "", planOpen, "Settings search")
	e.s.lreports.plansDue.Store(true)
	tiered := e.reports(time.Now(), false)
	if len(tiered) != 1 || tiered[0].Plan != late.ID || tiered[0].Kind != assistantwire.LeaderDecision || tiered[0].Level != assistantwire.LevelP1 {
		t.Fatalf("a waiting plan with push tiering on: %+v", tiered)
	}
	e.s.setQuietForTest(nil)
	e2 := env()
	if again := e2.reports(time.Now(), false); len(again) != 0 {
		t.Fatalf("told again after a restart: %+v", again)
	}
}

// planMemStore is memStore with the task plans and the feed, in memory.
type planMemStore struct {
	*memStore
	pmu   sync.Mutex
	plans map[string]db.TaskPlanRow
	feed  []db.FeedRow
}

func (m *planMemStore) CreateTaskPlan(_ context.Context, p db.TaskPlanRow) error {
	m.pmu.Lock()
	defer m.pmu.Unlock()
	m.plans[p.ID] = p
	return nil
}

func (m *planMemStore) TaskPlan(_ context.Context, id string) (*db.TaskPlanRow, error) {
	m.pmu.Lock()
	defer m.pmu.Unlock()
	p, ok := m.plans[id]
	if !ok {
		return nil, db.ErrTaskPlanNotFound
	}
	return &p, nil
}

func (m *planMemStore) TaskPlans(_ context.Context, conversation string) ([]db.TaskPlanRow, error) {
	m.pmu.Lock()
	defer m.pmu.Unlock()
	var out []db.TaskPlanRow
	for _, p := range m.plans {
		if p.Conversation == conversation {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *planMemStore) TaskPlansByID(_ context.Context, ids []string) ([]db.TaskPlanRow, error) {
	m.pmu.Lock()
	defer m.pmu.Unlock()
	var out []db.TaskPlanRow
	for _, id := range ids {
		if p, ok := m.plans[id]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

func (m *planMemStore) AppendFeed(_ context.Context, r db.FeedRow) error {
	m.pmu.Lock()
	defer m.pmu.Unlock()
	m.feed = append(m.feed, r)
	return nil
}

func (m *planMemStore) Feed(_ context.Context, conversation string, since time.Time, limit int) ([]db.FeedRow, error) {
	m.pmu.Lock()
	defer m.pmu.Unlock()
	var out []db.FeedRow
	for _, r := range m.feed {
		if r.Conversation == conversation && r.At.After(since) && (limit <= 0 || len(out) < limit) {
			out = append(out, r)
		}
	}
	return out, nil
}

func TestGoldenRouteAgent(t *testing.T) {
	golden(t, "route_agent", prompts.RenderRouteAgent())
}
