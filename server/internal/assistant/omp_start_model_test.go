package assistant

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The app, agent_start and an approved task_plan must agree on whether a new omp process gets a model.
func TestOmpNewAgentModel(t *testing.T) {
	const bound = "provider/settings-model"
	const preset = "provider/preset-model"
	const typed = "provider/typed-model"
	for _, tc := range []struct{ name, launcher, model, tier, want string }{
		{"model settings", "", "", "", bound},
		{"launcher default", "omp", "", "", ""},
		{"manual model", "omp", typed, "", typed},
		{"preset", "", "", "standard", preset},
		{"preset with override", "", typed, "standard", typed},
		{"preset using launcher default", "", "", "local", ""},
	} {
		setup := func(t *testing.T, s *Service, f *fakeHub) {
			t.Helper()
			f.launcherList = []proto.Launcher{{Name: "omp", Agent: "omp", Source: "path"}}
			f.ownModels = true
			setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.omp", Model: bound, Effort: "high"})
			saveTiers(t, s, map[string]ModelTier{
				"standard": {Launcher: "omp", Model: preset, Effort: "medium"},
				"local":    {Launcher: "omp", Effort: "high"},
			})
		}
		check := func(t *testing.T, params map[string]any) {
			t.Helper()
			model, sent := params["model"]
			if params["launcher"] != "omp" || (tc.want == "" && sent) || (tc.want != "" && model != tc.want) {
				t.Fatalf("launcher=%v model=%v (present=%t), want omp and %q", params["launcher"], model, sent, tc.want)
			}
			noModesStart(t, params)
		}
		checkStart := func(t *testing.T, f *fakeHub) {
			t.Helper()
			calls := profileNodeStarts(f)
			if len(calls) != 1 || calls[0].Device != "box" {
				t.Fatalf("session.start calls: %+v", calls)
			}
			var params map[string]any
			if err := json.Unmarshal(calls[0].Params, &params); err != nil {
				t.Fatal(err)
			}
			check(t, params)
		}
		t.Run(tc.name+"/app-and-agent_start", func(t *testing.T) {
			s, f := newQuietServiceOn(t, &memStore{})
			setup(t, s, f)
			pick := map[string]any{"launcher": tc.launcher, "model": tc.model, "tier": tc.tier}
			// POST /api/v1/sessions obtains the params it sends the node from this manager RPC.
			b := &BusHub{svc: s, helloed: make(chan struct{})}
			close(b.helloed)
			raw, err := json.Marshal(assistantwire.AgentStartParams{MachineID: "box", Params: pick})
			if err != nil {
				t.Fatal(err)
			}
			reply, err := b.request(t.Context(), assistantwire.MethodAgentStartParams, raw)
			if err != nil {
				t.Fatal(err)
			}
			params := reply.(map[string]any)["params"].(map[string]any)
			check(t, params)

			pick["machine"], pick["cwd"], pick["prompt"] = "box", "/code/app", "Fix the login crash"
			args, err := json.Marshal(pick)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.runTool(t.Context(), "server", "conv1", "agent_start", args); err != nil {
				t.Fatal(err)
			}
			checkStart(t, f)
		})
		t.Run(tc.name+"/task_plan", func(t *testing.T) {
			s, f, st := newProfileTaskService(t)
			setup(t, s, f)
			task := profilePlanTask()
			task.Launcher, task.Model, task.Tier, task.Permission = tc.launcher, tc.model, tc.tier, ""
			task.DoneWhen, task.ExpectMinutes = []string{"Login succeeds"}, 20
			plan := dispatchProfilePlan(t, s, st, task)
			if w := request(s, http.MethodPost, "/api/v1/task-plans/"+plan.ID+"/start", ""); w.Code != http.StatusOK {
				t.Fatalf("start: %d %s", w.Code, w.Body)
			}
			waitFor(t, "omp task dispatch", func() bool {
				todo := onlyTodo(s)
				return len(todo.Agents) != 0 || todo.StartError != ""
			})
			if todo := onlyTodo(s); todo.StartError != "" {
				t.Fatal(todo.StartError)
			}
			checkStart(t, f)
		})
	}
}

func TestOmpNewAgentModelLeavesOtherStartsAndResumesAlone(t *testing.T) {
	const bound = "provider/settings-model"
	binding := ModelBinding{Provider: "launcher.omp", Model: bound, Effort: "high"}
	s, _ := noModesService(t, "omp")
	setModel(t, s, "agent.new", binding)
	params, err := s.AgentStartParams(t.Context(), AgentStartParams{MachineID: "box", Params: map[string]any{
		"launcher": "omp", "resume_session_id": "existing",
	}})
	if err != nil || params["model"] != nil || params["effort"] != nil || params["model_fingerprint"] != nil {
		t.Fatalf("resume inherited a new Agent setting: %+v, %v", params, err)
	}
	params, err = s.AgentResumeParams(t.Context(), AgentResumeParams{MachineID: "box", SessionID: "outside"})
	if err != nil || params["model"] != bound || params["effort"] != "high" {
		t.Fatalf("resume lost its fallback binding: %+v, %v", params, err)
	}
	for _, tc := range []struct{ name, host, agent, source string }{
		{"Paseo provider of the same name", proto.EnginePaseo, "codex", "paseo"},
		{"shell launcher running Claude", proto.EngineHerdr, "claude", "zshrc"},
		{"non-herdr terminal", proto.EngineTern, "omp", "path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			f.devices = []Device{{ID: "box", Online: true, Reachable: true, Engine: tc.host}}
			f.launcherList = []proto.Launcher{{Name: "omp", Agent: tc.agent, Source: tc.source}}
			if proto.RuntimeEngine(tc.host) {
				f.hostLaunchers = map[string][]proto.Launcher{"box": f.launcherList}
				f.launcherList = []proto.Launcher{{Name: "omp", Agent: "omp", Source: "path"}}
			}
			s := newServiceOn(t, f, &memStore{})
			setModel(t, s, "agent.new", binding)
			for _, pick := range []map[string]any{{"launcher": "omp"}, {}} {
				p, err := s.AgentStartParams(t.Context(), AgentStartParams{MachineID: "box", Params: pick})
				if err != nil || p["launcher"] != "omp" || p["model"] != bound || p["effort"] != "high" {
					t.Fatalf("start lost its binding: %+v, %v", p, err)
				}
			}
		})
	}
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "omp", Agent: "claude"}})
	for _, name := range []string{"omp", "claude"} {
		setModel(t, s, "agent.new", ModelBinding{Provider: launcherProvider(name), Model: bound, Effort: "high"})
		p, err := s.AgentStartParams(t.Context(), AgentStartParams{MachineID: "box", Params: map[string]any{"launcher": name}})
		if err != nil || p["model"] != bound || p["effort"] != "high" {
			t.Fatalf("%s lost its configured model: %+v, %v", name, p, err)
		}
	}
}

func TestOmpNewAgentModelOnOrca(t *testing.T) {
	const bound = "provider/settings-model"
	const typed = "provider/typed-model"
	for _, tc := range []struct{ name, launcher, model, want string }{
		{"model settings", "", "", bound},
		{"launcher default", "omp", "", ""},
		{"manual model", "omp", typed, typed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			f.sessions["box"] = nil
			f.devices = []Device{{ID: "box", Online: true, Reachable: true, Engine: proto.EngineOrca}}
			f.launcherList = []proto.Launcher{{Name: "omp", Agent: "omp", Source: "path"}}
			f.ownModels = true
			s := newServiceOn(t, f, &memStore{})
			setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.omp", Model: bound, Effort: "high"})
			p, err := s.AgentStartParams(t.Context(), AgentStartParams{MachineID: "box", Params: map[string]any{
				"launcher": tc.launcher, "model": tc.model,
			}})
			if err != nil {
				t.Fatal(err)
			}
			model, sent := p["model"]
			if p["launcher"] != "omp" || (tc.want == "" && sent) || (tc.want != "" && model != tc.want) {
				t.Fatalf("launcher=%v model=%v (present=%t), want omp and %q", p["launcher"], model, sent, tc.want)
			}
			noModesStart(t, p)
		})
	}
}

func TestOmpNewAgentModelKeepsExplicitEffortForNodeRefusal(t *testing.T) {
	s, _ := noModesService(t, "omp")
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.omp", Model: "settings-model", Effort: "high"})
	saveTier(t, s, "standard", ModelTier{Launcher: "omp", Model: "preset-model", Effort: "medium"})
	p, err := s.AgentStartParams(t.Context(), AgentStartParams{MachineID: "box", Params: map[string]any{
		"tier": "standard", "effort": "low", "permission_mode": "plan",
	}})
	if err != nil || p["model"] != "preset-model" || p["effort"] != "low" || p["permission_mode"] != "plan" {
		t.Fatalf("explicit unsupported options lost before node validation: %+v, %v", p, err)
	}
}

func TestOmpNewAgentModelCatalogRelaysTheNodesHostCapability(t *testing.T) {
	for _, tc := range []struct {
		name, launcher, host, locked string
		supported, legacy            bool
	}{
		{name: "herdr", launcher: "omp", host: "herdr", supported: true},
		{name: "orca", launcher: "omp", host: "orca", supported: true},
		{name: "old node", launcher: "omp", host: "herdr", legacy: true},
		{name: "other host", launcher: "omp", host: "tern", locked: "requires herdr"},
		{name: "fixed model", launcher: "omp", host: "herdr", locked: "launcher fixes the model"},
		{name: "alias", launcher: "my-omp", host: "herdr", locked: "requires native omp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			s := newTestService(t, f)
			f.answerNode(assistantwire.DeviceLaunchersModels, func(mid string, raw json.RawMessage) (any, error) {
				var args struct{ Name, Host string }
				if err := json.Unmarshal(raw, &args); err != nil {
					return nil, err
				}
				if mid != "server" || args.Host != tc.host || args.Name != tc.launcher {
					t.Errorf("node catalog request: %s %+v", mid, args)
				}
				if tc.legacy {
					return json.RawMessage(`{"agent":"omp","models":[]}`), nil
				}
				return proto.LauncherModels{Agent: "omp", Models: []proto.AgentModel{}, Locked: tc.locked, SupportsStartModel: tc.supported}, nil
			})
			r := request(s, http.MethodGet, "/api/v1/machines/server/launchers/"+tc.launcher+"/models?host="+tc.host, "")
			var wire struct {
				Agent              string             `json:"agent"`
				Models             []proto.AgentModel `json:"models"`
				Locked             string             `json:"locked"`
				SupportsStartModel *bool              `json:"supports_start_model"`
			}
			if err := json.Unmarshal(r.Body.Bytes(), &wire); err != nil || r.Code != http.StatusOK || wire.Agent != "omp" || wire.Models == nil || len(wire.Models) != 0 || wire.Locked != tc.locked {
				t.Fatalf("catalog: %d %s (%v)", r.Code, r.Body, err)
			}
			if tc.supported != (wire.SupportsStartModel != nil && *wire.SupportsStartModel) || tc.legacy && wire.SupportsStartModel != nil {
				t.Fatalf("unexpected start capability: %s", r.Body)
			}
		})
	}
}
