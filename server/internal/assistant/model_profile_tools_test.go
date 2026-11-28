package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// profileTaskStore adds only the plan operations needed by task dispatch to the existing settings fake.
type profileTaskStore struct {
	*memStore
	planMu sync.Mutex
	plans  map[string]db.TaskPlanRow
}

func (st *profileTaskStore) CreateTaskPlan(_ context.Context, p db.TaskPlanRow) error {
	st.planMu.Lock()
	defer st.planMu.Unlock()
	p.Tasks = slices.Clone(p.Tasks)
	st.plans[p.ID] = p
	return nil
}

func (st *profileTaskStore) TaskPlan(_ context.Context, id string) (*db.TaskPlanRow, error) {
	st.planMu.Lock()
	defer st.planMu.Unlock()
	p, ok := st.plans[id]
	if !ok {
		return nil, db.ErrTaskPlanNotFound
	}
	p.Tasks = slices.Clone(p.Tasks)
	return &p, nil
}

func (st *profileTaskStore) UpdateTaskPlan(_ context.Context, id, from, to string, tasks json.RawMessage, at time.Time) (bool, error) {
	st.planMu.Lock()
	defer st.planMu.Unlock()
	p, ok := st.plans[id]
	if !ok || p.State != from {
		return false, nil
	}
	p.State, p.Tasks, p.UpdatedAt = to, slices.Clone(tasks), at
	st.plans[id] = p
	return true, nil
}

func (*profileTaskStore) AppendFeed(context.Context, db.FeedRow) error { return nil }

func profileMCPCall(t *testing.T, s *Service, session, name string, args any, assistant, guest bool) proto.ToolResult {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"session_id": session, "name": name, "args": args, "assistant": assistant, "guest": guest})
	if err != nil {
		t.Fatal(err)
	}
	out, handled, err := s.NodeRequest(context.Background(), "server", "mcp.call", raw)
	if err != nil || !handled {
		t.Fatalf("%s: handled=%v err=%v", name, handled, err)
	}
	return out.(proto.ToolResult)
}

func requireProfileMCP(t *testing.T, s *Service, name string, args any, out any) {
	t.Helper()
	res := profileMCPCall(t, s, "main", name, args, true, false)
	if res.IsError || len(res.Content) != 1 {
		t.Fatalf("%s: %+v", name, res)
	}
	if out != nil {
		if err := json.Unmarshal([]byte(res.Content[0].Text), out); err != nil {
			t.Fatalf("%s: %v: %s", name, err, res.Content[0].Text)
		}
	}
}

func profileToolsDevices(s *Service) *fleetDevs {
	d := newFleetDevs()
	d.add("server", "Server")
	d.setSessions("server", []proto.Session{{SessionID: "main", Cwd: "/home/u/.persona/assistant"},
		{SessionID: "ordinary", Cwd: "/code/app", PaneID: "pane-1"}})
	s.devs = d
	return d
}

func newProfileTaskService(t *testing.T) (*Service, *fakeHub, *profileTaskStore) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.sessions["box"] = nil
	f.ownModels = true
	f.settings = &Settings{MachineID: "server", Timezone: "UTC"}
	st := &profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}
	s := newServiceOn(t, f, st)
	d := profileToolsDevices(s)
	d.add("box", "Box")
	ps := modelProfiles{Revision: 1, Profiles: []ModelProfile{
		{ID: "economy", Name: "Economy", Tiers: map[string]ModelTier{"standard": {Launcher: "claude", Model: "test-economy", Effort: "low"}}},
		{ID: "performance", Name: "Performance", Tiers: map[string]ModelTier{"standard": {Launcher: "codex", Model: "test-performance", Effort: "high"}}},
		{ID: "empty", Name: "Empty"},
	}}
	if err := st.PutSetting(context.Background(), modelProfilesKey, ps); err != nil {
		t.Fatal(err)
	}
	requireProfileMCP(t, s, "model_profile_switch", map[string]any{"id": "economy"}, nil)
	return s, f, st
}

func dispatchProfilePlan(t *testing.T, s *Service, st *profileTaskStore, task PlanTask) *TaskPlan {
	t.Helper()
	res := profileMCPCall(t, s, "main", "task_plan", map[string]any{"tasks": []PlanTask{task}}, true, false)
	if res.IsError {
		t.Fatalf("task_plan: %+v", res)
	}
	st.planMu.Lock()
	var id string
	for key := range st.plans {
		id = key
	}
	n := len(st.plans)
	st.planMu.Unlock()
	if n != 1 {
		t.Fatalf("task_plan stored %d plans", n)
	}
	p, err := s.loadPlan(context.Background(), id)
	if err != nil || p.State != planOpen || len(p.Tasks) != 1 {
		t.Fatalf("task_plan: %+v err=%v", p, err)
	}
	return p
}

func profilePlanTask() PlanTask {
	return PlanTask{Title: "Fix the login crash", Prompt: "Fix the login crash and verify the change", Role: "worker", Machine: "box", Cwd: "/code/app", Tier: "standard", Permission: "bypassPermissions"}
}

func profileNodeStarts(f *fakeHub) []DeviceCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var calls []DeviceCall
	for _, call := range f.calls {
		if call.Method == DeviceSessionStart {
			calls = append(calls, call)
		}
	}
	return calls
}

func TestTaskPlanDispatchUsesCurrentProfileAndOverrides(t *testing.T) {
	tests := []struct {
		name, launcher, model, effort string
		override                      PlanTask
	}{
		{name: "tier", launcher: "codex", model: "test-performance", effort: "high"},
		{name: "launcher", override: PlanTask{Launcher: "other"}, launcher: "other"},
		{name: "same launcher", override: PlanTask{Launcher: "codex"}, launcher: "codex", model: "test-performance", effort: "high"},
		{name: "model", override: PlanTask{Model: "test-custom"}, launcher: "codex", model: "test-custom"},
		{name: "effort", override: PlanTask{Effort: "low"}, launcher: "codex", model: "test-performance", effort: "low"},
		{name: "all fields", override: PlanTask{Launcher: "other", Model: "test-custom", Effort: "medium"}, launcher: "other", model: "test-custom", effort: "medium"},
	}
	for _, when := range []string{"plan", "start"} {
		for _, tt := range tests {
			t.Run(when+"/"+tt.name, func(t *testing.T) {
				s, f, st := newProfileTaskService(t)
				task := profilePlanTask()
				if when == "plan" {
					task.Launcher, task.Model, task.Effort = tt.override.Launcher, tt.override.Model, tt.override.Effort
				}
				p := dispatchProfilePlan(t, s, st, task)
				if got := p.Tasks[0]; got.Tier != task.Tier || got.Launcher != task.Launcher || got.Model != task.Model || got.Effort != task.Effort {
					t.Fatalf("plan pinned tier fields before dispatch: %+v", got)
				}
				if calls := profileNodeStarts(f); len(calls) != 0 {
					t.Fatalf("pending plan started %d agents", len(calls))
				}
				requireProfileMCP(t, s, "model_profile_switch", map[string]any{"id": "performance"}, nil)
				body := ""
				if when == "start" {
					raw, err := json.Marshal(map[string]any{"tasks": []PlanTask{tt.override}})
					if err != nil {
						t.Fatal(err)
					}
					body = string(raw)
				}
				if w := request(s, http.MethodPost, "/api/v1/task-plans/"+p.ID+"/start", body); w.Code != http.StatusOK {
					t.Fatalf("start: %d %s", w.Code, w.Body)
				}
				waitFor(t, "the task dispatch", func() bool {
					todo := onlyTodo(s)
					return len(todo.Agents) != 0 || todo.StartError != ""
				})
				if todo := onlyTodo(s); len(todo.Agents) != 1 || todo.StartError != "" {
					t.Fatalf("task failed: %+v", todo)
				}
				calls := profileNodeStarts(f)
				if len(calls) != 1 || calls[0].Device != "box" {
					t.Fatalf("session.start calls: %+v", calls)
				}
				var params map[string]any
				if err := json.Unmarshal(calls[0].Params, &params); err != nil {
					t.Fatal(err)
				}
				for key, want := range map[string]string{"launcher": tt.launcher, "model": tt.model, "effort": tt.effort} {
					if got, _ := params[key].(string); got != want {
						t.Errorf("session.start %s=%q want %q", key, got, want)
					}
				}
				if _, ok := params["tier"]; ok {
					t.Fatal("tier leaked to session.start")
				}
			})
		}
	}
}

func TestTaskPlanDispatchRejectsUnknownTier(t *testing.T) {
	for _, when := range []string{"plan", "profile switch", "start edit"} {
		t.Run(when, func(t *testing.T) {
			s, f, st := newProfileTaskService(t)
			task := profilePlanTask()
			if when == "plan" {
				before, puts := profileStoreSnapshot(st.memStore)
				task.Tier, task.Readonly = "missing", true
				res := profileMCPCall(t, s, "main", "task_plan", map[string]any{"tasks": []PlanTask{task}}, true, false)
				if !res.IsError || len(res.Content) != 1 || !strings.Contains(res.Content[0].Text, "missing") {
					t.Fatalf("unknown tier: %+v", res)
				}
				requireProfileStoreUnchanged(t, st.memStore, before, puts)
				st.planMu.Lock()
				n := len(st.plans)
				st.planMu.Unlock()
				if n != 0 || onlyTodo(s).ID != "" {
					t.Fatal("unknown tier created a plan or todo")
				}
			} else {
				p := dispatchProfilePlan(t, s, st, task)
				body := ""
				missing := "standard"
				if when == "profile switch" {
					requireProfileMCP(t, s, "model_profile_switch", map[string]any{"id": "empty"}, nil)
				} else {
					body = `{"tasks":[{"tier":"missing"}]}`
					missing = "missing"
				}
				if w := request(s, http.MethodPost, "/api/v1/task-plans/"+p.ID+"/start", body); w.Code != http.StatusOK {
					t.Fatalf("start: %d %s", w.Code, w.Body)
				}
				waitFor(t, "the failed plan", func() bool {
					current, err := s.loadPlan(context.Background(), p.ID)
					return err == nil && current.Tasks[0].Error != ""
				})
				current, err := s.loadPlan(context.Background(), p.ID)
				if err != nil || !strings.Contains(current.Tasks[0].Error, "no tier "+missing) {
					t.Fatalf("unknown tier plan error: %+v err=%v", current, err)
				}
				if todo := onlyTodo(s); todo.DoneAt == nil || !strings.Contains(todo.StartError, "no tier "+missing) || len(todo.Agents) != 0 {
					t.Fatalf("unknown tier not recorded as a failed task: %+v", todo)
				}
			}
			if calls := profileNodeStarts(f); len(calls) != 0 {
				t.Fatalf("unknown tier sent %d session.start calls", len(calls))
			}
		})
	}
}

func TestModelProfileMCPDispatch(t *testing.T) {
	s, _, st := modelService(t)
	profileToolsDevices(s)
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", Model: "test-original"}})
	storeProfiles(t, st, ModelProfile{ID: "economy", Name: "Economy", Launchers: map[string]LauncherModels{"gw": {Model: profileValue("test-economy")}},
		Tiers: map[string]ModelTier{"standard": {Launcher: "gw", Model: "test-economy", Effort: "low"}}})
	var v ModelProfilesView
	before, puts := profileStoreSnapshot(st.memStore)
	requireProfileMCP(t, s, "model_profiles_list", map[string]any{}, &v)
	if v.Active != "" || len(v.Profiles) != 1 || v.Profiles[0].ID != "economy" || !slices.Contains(v.Profiles[0].Changes, "gw.model") {
		t.Fatalf("list: %+v", v)
	}
	var p ModelProfileView
	requireProfileMCP(t, s, "model_profile_get", map[string]any{"id": "economy"}, &p)
	// The seeded strength name reads back under its kind-of-work name, the old one kept as an alias (#14700).
	if p.ID != "economy" || p.Active || p.Tiers["dev"].Model != "test-economy" || !slices.Equal(p.Tiers["dev"].Aliases, []string{"standard"}) {
		t.Fatalf("get: %+v", p)
	}
	requireProfileStoreUnchanged(t, st.memStore, before, puts)
	for _, name := range []string{"model_profile_get", "model_profile_switch"} {
		res := profileMCPCall(t, s, "main", name, map[string]any{"id": "missing"}, true, false)
		if !res.IsError {
			t.Fatalf("%s accepted a missing profile: %+v", name, res)
		}
	}
	if res := profileMCPCall(t, s, "main", "model_profile_switch", map[string]any{}, true, false); !res.IsError {
		t.Fatal("switch accepted an empty id")
	}
	if res := profileMCPCall(t, s, "main", "model_profile_rollback", map[string]any{}, true, false); !res.IsError {
		t.Fatal("rollback without a previous image succeeded")
	}
	requireProfileStoreUnchanged(t, st.memStore, before, puts)
	requireProfileMCP(t, s, "model_profile_switch", map[string]any{"id": "economy"}, &v)
	if v.Active != "economy" || v.By == nil || *v.By != (ProfileActor{Kind: "assistant"}) || v.Rollback == nil || len(v.Events) != 1 || v.Events[0].Action != "switch" {
		t.Fatalf("switch: %+v", v)
	}
	if got := s.storedLaunchers(context.Background())[0].Model; got != "test-economy" {
		t.Fatalf("switch launcher model=%q", got)
	}
	requireProfileMCP(t, s, "model_profile_get", map[string]any{"id": "economy"}, &p)
	if !p.Active || len(p.Changes) != 0 {
		t.Fatalf("get after switch: %+v", p)
	}
	// Decode into a fresh view so omitted JSON fields cannot retain the previous result.
	v = ModelProfilesView{}
	requireProfileMCP(t, s, "model_profile_rollback", map[string]any{}, &v)
	if v.Active != "" || v.Rollback == nil || v.Rollback.To != "economy" || len(v.Events) != 2 || v.Events[0].Action != "rollback" || v.Events[0].By != (ProfileActor{Kind: "assistant"}) {
		t.Fatalf("rollback: %+v", v)
	}
	if got := s.storedLaunchers(context.Background())[0].Model; got != "test-original" {
		t.Fatalf("rollback launcher model=%q", got)
	}
	v = ModelProfilesView{}
	requireProfileMCP(t, s, "model_profile_rollback", map[string]any{}, &v)
	if v.Active != "economy" || len(v.Events) != 3 || v.Events[0].Action != "rollback" || s.storedLaunchers(context.Background())[0].Model != "test-economy" {
		t.Fatalf("redo: %+v", v)
	}
}

func TestModelProfileMCPSharedRestrictions(t *testing.T) {
	s, _, st := modelService(t)
	profileToolsDevices(s)
	storeProfiles(t, st, ModelProfile{ID: "economy", Name: "Economy"})
	requireProfileMCP(t, s, "model_profile_switch", map[string]any{"id": "economy"}, nil)
	if err := s.guestSet(context.Background(), "server", "guest", []string{"connector_reply"}); err != nil {
		t.Fatal(err)
	}
	before, puts := profileStoreSnapshot(st.memStore)
	tools := []string{"model_profiles_list", "model_profile_get", "model_profile_switch", "model_profile_rollback",
		"model_profile_capture", "model_profile_deactivate", "model_profile_clear"}
	for _, tt := range []struct {
		name, session    string
		assistant, guest bool
	}{
		{"ordinary agent", "ordinary", false, false},
		{"forged assistant", "ordinary", true, false},
		{"guest", "guest", true, true},
		{"guest omits hint", "guest", true, false},
		{"unknown guest", "unknown", true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"session_id": tt.session, "assistant": tt.assistant, "guest": tt.guest})
			out, handled, err := s.NodeRequest(context.Background(), "server", "mcp.tools", raw)
			if err != nil || !handled {
				t.Fatalf("mcp.tools: handled=%v err=%v", handled, err)
			}
			for _, tool := range out.(map[string]any)["tools"].([]proto.Tool) {
				if slices.Contains(tools, tool.Name) {
					t.Fatalf("restricted caller discovered %s", tool.Name)
				}
			}
			for _, name := range tools {
				res := profileMCPCall(t, s, tt.session, name, map[string]any{"id": "economy"}, tt.assistant, tt.guest)
				if !res.IsError {
					t.Fatalf("restricted caller ran %s: %+v", name, res)
				}
			}
			requireProfileStoreUnchanged(t, st.memStore, before, puts)
		})
	}
	// The shared filter still exposes the tools to the configured assistant.
	names := privacyTools(t, s, "server", "main", true)
	for _, name := range tools {
		if !slices.Contains(names, name) {
			t.Fatalf("assistant cannot discover %s", name)
		}
	}
}

func TestStartTierOverrideAndUnknown(t *testing.T) {
	s, _, st := modelService(t)
	ctx := context.Background()
	ps := modelProfiles{Revision: 1, Profiles: []ModelProfile{{ID: "economy", Name: "Economy", Tiers: map[string]ModelTier{"standard": {Launcher: "codex", Model: "test-standard", Effort: "high"}}}}}
	if err := st.PutSetting(ctx, modelProfilesKey, ps); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSetting(ctx, modelProfileStateKey, modelProfileState{Active: "economy"}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name                    string
		params                  map[string]any
		launcher, model, effort string
	}{
		{"default", map[string]any{"tier": "standard"}, "codex", "test-standard", "high"},
		{"other launcher", map[string]any{"tier": "standard", "launcher": "other"}, "other", "", ""},
		{"other model", map[string]any{"tier": "standard", "model": "test-custom"}, "codex", "test-custom", ""},
		{"explicit effort", map[string]any{"tier": "standard", "effort": "low"}, "codex", "test-standard", "low"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := s.applyStartTier(ctx, tt.params); err != nil {
				t.Fatal(err)
			}
			for key, want := range map[string]string{"launcher": tt.launcher, "model": tt.model, "effort": tt.effort} {
				got, _ := tt.params[key].(string)
				if got != want {
					t.Fatalf("%s=%q want %q", key, got, want)
				}
			}
			if _, ok := tt.params["tier"]; ok {
				t.Fatal("tier leaked to node")
			}
		})
	}
	if err := s.applyStartTier(ctx, map[string]any{"tier": "missing"}); err == nil {
		t.Fatal("unknown tier accepted")
	}
}

func TestProfileSnapshotFreshOutsideCache(t *testing.T) {
	s, _, st := modelService(t)
	ctx := context.Background()
	ps := modelProfiles{Revision: 1, Profiles: []ModelProfile{{ID: "economy", Name: "Economy"}, {ID: "performance", Name: "Performance"}}}
	if err := st.PutSetting(ctx, modelProfilesKey, ps); err != nil {
		t.Fatal(err)
	}
	s.live.snap = &prompts.Snapshot{Now: time.Now(), Zone: "UTC"}
	s.live.at = time.Now()
	for _, id := range []string{"economy", "performance", ""} {
		if err := st.PutSetting(ctx, modelProfileStateKey, modelProfileState{Active: id}); err != nil {
			t.Fatal(err)
		}
		got := s.contextSnapshot(ctx, "", false)
		if !strings.Contains(got, "- 模型档位：") {
			t.Fatal("profile line missing")
		}
		if id != "" && !strings.Contains(got, "("+id+")") {
			t.Fatalf("cached profile: %s", got)
		}
		if id == "" && strings.Contains(got, "(performance)") {
			t.Fatal("rollback retained stale profile")
		}
	}
}
