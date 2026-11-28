package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

// Another Agent for an existing task (task_plan's continues, task_retry) runs on what that call names, field by field,
// else on the candidate of a tier its previous Agent ran on while that tier still lists it (#14701, which reverses
// #12618 for tiers: candidate_dispatch_test.go covers the keeping and the re-pick), else on the task's own launch as
// its plan named it, else on the current default: a tier resolves against the model profile in use now, and a task
// that names neither a launcher nor a tier, and whose previous Agent ran on no tier, gets Model configuration › Agents
// as it is now. What a default or the Agents binding resolved on an earlier start is not kept. Launcher, model and tier
// names are placeholders.

// restartHistoryStore is profileTaskStore that lists every plan as the plans' history, as the database does, so a
// continuation's failed start is retried through the plan that binds its to-do now (currentPlanAssignment).
type restartHistoryStore struct{ *profileTaskStore }

func (st restartHistoryStore) TaskPlansMadeSince(context.Context, time.Time) ([]db.TaskPlanRow, error) {
	st.planMu.Lock()
	defer st.planMu.Unlock()
	out := make([]db.TaskPlanRow, 0, len(st.plans))
	for _, p := range st.plans {
		p.Tasks = slices.Clone(p.Tasks)
		out = append(out, p)
	}
	return out, nil
}

// restartHub: box has Claude Code and launchers of its own, first and second (Claude Code) and other (another agent);
// conv1 is the main conversation. Model configuration › Agents starts first with model-one at high effort.
func restartHub(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	return restartHubWithStore(t, restartHistoryStore{&profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}})
}

func restartHubWithStore(t *testing.T, store Store) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.sessions["box"] = nil
	f.devices = []Device{
		{ID: "server", Name: "server", Home: "/home/me", Online: true, Reachable: true},
		{ID: "box", Name: "box", Home: "/Users/me", Online: true, Reachable: true, ClaudeVersion: "2.1.0"},
	}
	f.zshrc = map[string][]proto.Launcher{"box": {
		{Name: "first", Source: "zshrc"}, {Name: "second", Source: "zshrc"}, {Name: "other", Source: "zshrc", Agent: "codex"},
	}}
	s := newServiceOn(t, f, store)
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main = "server", "conv1" })
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.first", Model: "model-one", Effort: "high"})
	return s, f
}

// restartTask is a writable task on box with extra (its launcher, tier, model, effort, continues…) merged in.
func restartTask(extra map[string]any) json.RawMessage {
	task := map[string]any{"title": "Fix the build", "prompt": "Fix the build", "machine": "box", "cwd": "/Users/me/code/app",
		"why": "the repository is on box", "done_when": []string{"CI is green"}}
	for k, v := range extra {
		task[k] = v
	}
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{task}})
	return raw
}

// planRestartTask plans task in conv1 and answers its plan's id.
func planRestartTask(t *testing.T, s *Service, extra map[string]any) string {
	t.Helper()
	res, err := s.toolTaskPlan(context.Background(), "conv1", restartTask(extra))
	if err != nil {
		t.Fatal(err)
	}
	return planID(t, res)
}

// startRestartPlan starts plan id (the card's Start) with the node answering failure (nil: the agent starts), waits
// until its start is over, and answers the to-do and what its session.start carried.
func startRestartPlan(t *testing.T, s *Service, f *fakeHub, id string, failure error) (string, map[string]any) {
	t.Helper()
	f.mu.Lock()
	f.failStart = failure
	before := len(f.starts)
	f.mu.Unlock()
	if _, err := s.startPlan(context.Background(), id, nil, false); err != nil {
		t.Fatal(err)
	}
	todo := todoIDOfPlan(t, s, id)
	waitFor(t, "the start to be over", func() bool {
		cur := todoNow(s, todo)
		inRemedy := false
		if p, err := s.loadPlan(context.Background(), id); err == nil && len(p.Tasks) == 1 && p.Tasks[0].Start != nil {
			inRemedy = p.Tasks[0].Start.State == remedying
		}
		over := len(cur.Agents) == 1 || failure != nil && (cur.StartError != "" || inRemedy)
		return over && len(startsOf(f)) > before && s.taskStarting.Load() == 0
	})
	f.mu.Lock()
	f.failStart = nil
	f.mu.Unlock()
	return todo, startsOf(f)[before]
}

// retryRestart is task_retry from the main conversation with extra (launcher, model), waiting for its start.
func retryRestart(t *testing.T, s *Service, f *fakeHub, todo string, extra map[string]any) map[string]any {
	t.Helper()
	before := len(startsOf(f))
	a := map[string]any{"todo_id": todo, "reason": "the start failed once"}
	for k, v := range extra {
		a[k] = v
	}
	args, _ := json.Marshal(a)
	if _, err := s.runTool(context.Background(), "server", "conv1", "task_retry", args); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the retry's start", func() bool { return len(startsOf(f)) > before && s.taskStarting.Load() == 0 })
	return startsOf(f)[before]
}

// launchOf is what a session.start carried: launcher, model, effort ("" for one it did not carry).
func launchOf(st map[string]any) [3]string {
	l, _ := st["launcher"].(string)
	m, _ := st["model"].(string)
	e, _ := st["effort"].(string)
	return [3]string{l, m, e}
}

// failedOnceOnFirst plans a task with extra (nothing of its launch when nil) while Model configuration › Agents runs
// the other agent, then starts it after the default moved to first (model-one, high): Claude Code runs with Allow all,
// so the start settles the task's permission again and writes the task back to its plan (as a health or admission
// detour, a launcher swap or a policy's note would). The start fails for good.
func failedOnceOnFirst(t *testing.T, s *Service, f *fakeHub, extra map[string]any) string {
	t.Helper()
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.other", Model: "model-zero"})
	id := planRestartTask(t, s, extra)
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.first", Model: "model-one", Effort: "high"})
	todo, first := startRestartPlan(t, s, f, id, errors.New("the example start failed"))
	if got := launchOf(first); got != [3]string{"first", "model-one", "high"} {
		t.Fatalf("first start %v", got)
	}
	p, err := s.loadPlan(context.Background(), id)
	if err != nil || p.Tasks[0].ErrorArgs["launcher"] != "first" {
		t.Fatalf("the failed start did not record its launcher: %+v %v", p, err)
	}
	return todo
}

// task_retry with no launcher, after a start that wrote the task back: the retry runs on the current Agents default,
// not on what the failed start resolved, which stays only as the failed start's record (its launcher).
func TestTaskRetryFollowsTheCurrentDefault(t *testing.T) {
	s, f := restartHub(t)
	todo := failedOnceOnFirst(t, s, f, nil)
	_, p, i, err := s.remedyOf(context.Background(), todo)
	if err != nil {
		t.Fatal(err)
	}
	if pt := p.Tasks[i]; pt.Launcher != "" || pt.Model != "" || pt.Effort != "" || pt.Tier != "" || pt.ErrorArgs["launcher"] != "first" {
		t.Fatalf("the plan keeps %q/%q/%q/%q (failed on %q), want the task's own launch and the failed start's launcher",
			pt.Launcher, pt.Model, pt.Effort, pt.Tier, pt.ErrorArgs["launcher"])
	}
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.second", Model: "model-two", Effort: "low"})
	if got := launchOf(retryRestart(t, s, f, todo, nil)); got != [3]string{"second", "model-two", "low"} {
		t.Fatalf("task_retry started %v, want the current default [second model-two low]", got)
	}
}

// The same for the owner's Retry on the card (POST /api/v1/todos/{id}/retry), which shares the retry's start.
func TestOwnerRetryFollowsTheCurrentDefault(t *testing.T) {
	s, f := restartHub(t)
	todo := failedOnceOnFirst(t, s, f, nil)
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.second", Model: "model-two", Effort: "low"})
	before := len(startsOf(f))
	if _, err := s.retryTask(context.Background(), todo); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the retry's start", func() bool { return len(startsOf(f)) > before && s.taskStarting.Load() == 0 })
	if got := launchOf(startsOf(f)[before]); got != [3]string{"second", "model-two", "low"} {
		t.Fatalf("Retry started %v, want the current default [second model-two low]", got)
	}
}

// A tier the task named is resolved against the model profile in use when the retry starts: a one-candidate tier
// whose candidate changed no longer lists the candidate the failed start ran on, so there is nothing to keep.
func TestTaskRetryResolvesTheTaskTierNow(t *testing.T) {
	s, f := restartHub(t)
	saveTier(t, s, "deep", ModelTier{Launcher: "other", Model: "model-zero"})
	id := planRestartTask(t, s, map[string]any{"tier": "deep"})
	saveTier(t, s, "deep", ModelTier{Launcher: "first", Model: "model-one", Effort: "high"})
	todo, first := startRestartPlan(t, s, f, id, errors.New("the example start failed"))
	if got := launchOf(first); got != [3]string{"first", "model-one", "high"} {
		t.Fatalf("first start %v", got)
	}
	saveTier(t, s, "deep", ModelTier{Launcher: "second", Model: "model-two", Effort: "low"})
	if got := launchOf(retryRestart(t, s, f, todo, nil)); got != [3]string{"second", "model-two", "low"} {
		t.Fatalf("task_retry started %v, want tier deep as it is now [second model-two low]", got)
	}
}

// task_retry's own launcher, and its launcher with a model, win for their fields; the rest is not what the failed
// start resolved: the launcher's own default model, no effort of the earlier default.
func TestTaskRetryCallerLauncherAndModelWin(t *testing.T) {
	for _, c := range []struct {
		name  string
		extra map[string]any
		want  [3]string
	}{
		{"launcher", map[string]any{"launcher": "second"}, [3]string{"second", "", ""}},
		{"launcher and model", map[string]any{"launcher": "second", "model": "model-three"}, [3]string{"second", "model-three", ""}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := restartHub(t)
			todo := failedOnceOnFirst(t, s, f, nil)
			if got := launchOf(retryRestart(t, s, f, todo, c.extra)); got != c.want {
				t.Fatalf("task_retry %v started %v, want %v", c.extra, got, c.want)
			}
		})
	}
}

// What the task's own plan named stays the task's: a retry with no launcher keeps it, whatever the default is now.
func TestTaskRetryKeepsWhatThePlanNamed(t *testing.T) {
	s, f := restartHub(t)
	id := planRestartTask(t, s, map[string]any{"launcher": "second", "model": "model-three", "effort": "medium"})
	todo, _ := startRestartPlan(t, s, f, id, errors.New("the example start failed"))
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.first", Model: "model-two", Effort: "low"})
	if got := launchOf(retryRestart(t, s, f, todo, nil)); got != [3]string{"second", "model-three", "medium"} {
		t.Fatalf("task_retry started %v, want the plan's own [second model-three medium]", got)
	}
}

// continuedOnFirst plans and starts a task with original as its own launch; its agent runs. The default then moves to
// second (model-two, low).
func continuedOnFirst(t *testing.T, s *Service, f *fakeHub, original map[string]any) string {
	t.Helper()
	todo, _ := startRestartPlan(t, s, f, planRestartTask(t, s, original), nil)
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.second", Model: "model-two", Effort: "low"})
	return todo
}

// task_plan with continues: the replacement runs on what this call names, field by field; else, when the Agent it
// continues ran on a tier, on that tier's candidate it ran on (kept, #14701), with this call's effort laid over it;
// else on the current default. A launcher, model and effort the task it continues named are not this call's.
func TestTaskPlanContinuesKeepsTheTiersCandidate(t *testing.T) {
	saveTiers := func(t *testing.T, s *Service) {
		t.Helper()
		ps := modelProfiles{Revision: 1, Profiles: []ModelProfile{{ID: "p", Name: "P", Tiers: map[string]ModelTier{
			"deep":  {Launcher: "first", Model: "model-one", Effort: "high"},
			"quick": {Launcher: "second", Model: "model-four", Effort: "medium"},
		}}}}
		if err := s.store().PutSetting(context.Background(), modelProfilesKey, ps); err != nil {
			t.Fatal(err)
		}
		if err := s.store().PutSetting(context.Background(), modelProfileStateKey, modelProfileState{Active: "p"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, original := range []struct {
		name string
		pick map[string]any
	}{
		{"original named launcher, model and effort", map[string]any{"launcher": "first", "model": "model-one", "effort": "high"}},
		{"original named a tier", map[string]any{"tier": "deep"}},
		{"original named nothing", nil},
	} {
		onTier := original.pick["tier"] != nil
		for _, c := range []struct {
			name string
			pick map[string]any
			want [3]string
		}{
			{"none named", nil, [3]string{"second", "model-two", "low"}},
			{"launcher", map[string]any{"launcher": "first"}, [3]string{"first", "", ""}},
			{"tier", map[string]any{"tier": "quick"}, [3]string{"second", "model-four", "medium"}},
			{"model", map[string]any{"model": "model-three"}, [3]string{"second", "model-three", "low"}},
			{"effort", map[string]any{"effort": "medium"}, [3]string{"second", "model-two", "medium"}},
		} {
			want := c.want
			if onTier && c.pick["launcher"] == nil && c.pick["model"] == nil && c.pick["tier"] == nil {
				// The Agent it continues ran on tier deep's candidate: kept, this call's effort over it.
				want = [3]string{"first", "model-one", cmp.Or(c.pick["effort"], any("high")).(string)}
			}
			t.Run(original.name+"/"+c.name, func(t *testing.T) {
				s, f := restartHub(t)
				saveTiers(t, s)
				todo := continuedOnFirst(t, s, f, original.pick)
				extra := map[string]any{"continues": todo, "continues_why": "the first agent stopped"}
				for k, v := range c.pick {
					extra[k] = v
				}
				before := len(startsOf(f))
				id := planRestartTask(t, s, extra)
				if _, err := s.startPlan(context.Background(), id, nil, false); err != nil {
					t.Fatal(err)
				}
				waitFor(t, "the continuation's start", func() bool { return len(startsOf(f)) > before && s.taskStarting.Load() == 0 })
				if got := launchOf(startsOf(f)[before]); got != want {
					t.Fatalf("continuation %v started %v, want %v", c.pick, got, want)
				}
			})
		}
	}
}

// A continuation whose own start failed after writing itself back, of an Agent that ran on no tier, is retried on
// the current default too.
func TestContinuationRetryFollowsTheCurrentDefault(t *testing.T) {
	s, f := restartHub(t)
	original, _ := startRestartPlan(t, s, f, planRestartTask(t, s, nil), nil)
	todo := failedOnceOnFirst(t, s, f, map[string]any{"continues": original, "continues_why": "the first agent stopped"})
	if todo != original {
		t.Fatalf("the continuation runs on %s, not on the to-do it continues (%s)", todo, original)
	}
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.second", Model: "model-two", Effort: "low"})
	if got := launchOf(retryRestart(t, s, f, todo, nil)); got != [3]string{"second", "model-two", "low"} {
		t.Fatalf("task_retry started %v, want the current default [second model-two low]", got)
	}
}

// What one try put in place of the task's launch is that try's own: a flexible task moved to another computer between
// tries, whose later try runs the default there (its launch went back to the task's own after a policy's effort), keeps
// the task's own launch in the plan, not that default.
func TestAnEarlierTrysSwapIsNotALaterTrysDefault(t *testing.T) {
	s, f := defaultsHub(t) // Model configuration › Agents runs gateway (gw-model, high), which box has and l4 lacks
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return StartDefaults{Effort: "max"}, true })
	ctx := context.Background()
	task := defaultsTask()
	task.Machine, task.Cwd = "l4", "/home/me/code/app"
	if _, _, err := s.startTaskAgent(ctx, &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["launcher"] != "claude" {
		t.Fatalf("l4 started %v", st)
	}
	task.Machine, task.Cwd = "box", "/Users/me/code/app"
	if _, _, err := s.startTaskAgent(ctx, &task, true); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[1]; st["launcher"] != "gateway" || st["model"] != "gw-model" {
		t.Fatalf("box started %v", st)
	}
	if kept := task.storedLaunch(); kept.launcher != "" || kept.model != "" || kept.effort != "" || kept.tier != "" {
		t.Fatalf("the plan keeps %+v, want the task's own launch (none)", kept)
	}
}

// What an engine ran in place of what the task asked for (#8140) is what the task names from then on: its launcher
// (without the tier) and that launcher's own default model, as the card shows it.
func TestEngineInsteadIsWhatThePlanKeeps(t *testing.T) {
	s, f := paseoFitHub(t)
	ctx := context.Background()
	f.startReply = func(params map[string]any, sess *proto.Session) *proto.StartInstead {
		sess.Launcher = "krcc"
		return &proto.StartInstead{Launcher: "claude-sub", Model: "model-three"}
	}
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions",
		"prompt":"Reply with pong","machine":"pbox","cwd":"~/code/app","launcher":"claude-sub","model":"model-three","why":"A quick check"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planID(t, res)
	if _, err := s.startPlan(ctx, id, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start's bookkeeping", func() bool { return len(startsOf(f)) == 1 && s.taskStarting.Load() == 0 })
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if pt := p.Tasks[0]; pt.Launcher != "krcc" || pt.Model != "" || pt.Tier != "" || !containsAll(pt.Why, "krcc", "claude-sub") {
		t.Fatalf("the plan keeps %q/%q/%q (%q), want what the engine ran", pt.Launcher, pt.Model, pt.Tier, pt.Why)
	}
}

// A launcher the engine stands in at dispatch for the one the task named (the card could not tell when it was made)
// keeps the task's own model when it offers it: the plan names the stand-in and still the task's model.
func TestDispatchStandInKeepsTheTasksModel(t *testing.T) {
	s, f := paseoFitHub(t)
	ctx := context.Background()
	f.agentModels = map[string][]proto.AgentModel{"codex-cpa": {{ID: "model-three"}}}
	f.mu.Lock()
	host := f.hostLaunchers["pbox"]
	delete(f.hostLaunchers, "pbox") // no engine list while the plan is made
	f.mu.Unlock()
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions",
		"prompt":"Reply with pong","machine":"pbox","cwd":"~/code/app","launcher":"codex","model":"model-three","why":"A quick check"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planID(t, res)
	f.mu.Lock()
	f.hostLaunchers["pbox"] = host
	f.mu.Unlock()
	s.invalidateLauncherListCache("pbox")
	if _, err := s.startPlan(ctx, id, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start's bookkeeping", func() bool { return len(startsOf(f)) == 1 && s.taskStarting.Load() == 0 })
	if st := startsOf(f)[0]; st["launcher"] != "codex-cpa" || st["model"] != "model-three" {
		t.Fatalf("started %v", st)
	}
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if pt := p.Tasks[0]; pt.Launcher != "codex-cpa" || pt.Model != "model-three" || !containsAll(pt.Why, "codex-cpa") {
		t.Fatalf("the plan keeps %q/%q (%q), want the stand-in with the task's model", pt.Launcher, pt.Model, pt.Why)
	}
}

// A launcher the computer has, swapped in at dispatch for a default it lacks (#2661), keeps an explicit caller model.
// Only a model that came from the old default is replaced by the swapped launcher's own default. The plan names the
// swapped launcher and keeps the caller's model, as the card shows it.
func TestDispatchSwapKeepsTheTasksModel(t *testing.T) {
	s, f := launcherFitHub(t)
	ctx := context.Background()
	l4 := f.zshrc["l4"]
	f.mu.Lock()
	delete(f.zshrc, "l4") // no list from l4 while the plan is made
	f.mu.Unlock()
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions",
		"prompt":"Reply with pong","machine":"l4","cwd":"~/code/app","model":"model-three"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planID(t, res)
	f.mu.Lock()
	f.zshrc["l4"] = l4
	f.mu.Unlock()
	if _, err := s.startPlan(ctx, id, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start's bookkeeping", func() bool { return len(startsOf(f)) == 1 && s.taskStarting.Load() == 0 })
	if st := startsOf(f)[0]; st["launcher"] != "claude" || st["model"] != "model-three" {
		t.Fatalf("started %v, want the fallback launcher with the explicit model", st)
	}
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if pt := p.Tasks[0]; pt.Launcher != "claude" || pt.Model != "model-three" || !strings.Contains(pt.Why, "gateway") || !strings.Contains(pt.Why, "model-three") || strings.Contains(pt.Why, "default model") {
		t.Fatalf("the plan keeps %q/%q (%q), want the swapped launcher with the explicit model named in the note", pt.Launcher, pt.Model, pt.Why)
	}
}

// A launcher-only retry override selects that launcher's native model default, not a model from the failed task or a
// matching Agents binding. The choice also survives reading the saved plan back before another start.
func TestTaskRetryLauncherOnlyUsesTheLaunchersOwnModel(t *testing.T) {
	for _, c := range []struct {
		name     string
		original map[string]any
		failure  error
	}{
		{"matching Agents provider", map[string]any{"launcher": "second", "model": "model-three"}, nil},
		{"same launcher as the plan", map[string]any{"launcher": "first", "model": "model-three"}, nil},
		{"same launcher already without a model in a kept tab", map[string]any{"launcher": "first"}, keptRefusal(proto.CodeShellWaiting)},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := restartHub(t)
			failure := c.failure
			if failure == nil {
				failure = errors.New("the example start failed")
			}
			todo, _ := startRestartPlan(t, s, f, planRestartTask(t, s, c.original), failure)
			if st := retryRestart(t, s, f, todo, map[string]any{"launcher": "first"}); st["launcher"] != "first" || !unset(st["model"]) {
				t.Fatalf("launcher-only retry started %v, want first with its native model default", st)
			}

			_, p, i, err := s.remedyOf(context.Background(), todo)
			if err != nil {
				t.Fatal(err)
			}
			pt := p.Tasks[i] // callerPick is nil: this is the same launch a store read-back starts from.
			before := len(startsOf(f))
			if _, _, err := s.startTaskAgent(context.Background(), &pt, true); err != nil {
				t.Fatal(err)
			}
			if st := startsOf(f)[before]; st["launcher"] != "first" || !unset(st["model"]) {
				t.Fatalf("read-back start carried %v, want the saved native model default", st)
			}
		})
	}
}

// Clearing a saved tier is part of a same-launcher override even when launcher and model strings do not change.
// Otherwise a kept-tab read-back resurrects the tier and its model on the next start.
func TestTaskRetrySameLauncherDropsSavedTier(t *testing.T) {
	s, f := restartHub(t)
	saveTier(t, s, "deep", ModelTier{Launcher: "first", Model: "model-three", Effort: "low"})
	todo, _ := startRestartPlan(t, s, f, planRestartTask(t, s, map[string]any{"launcher": "first"}), keptRefusal(proto.CodeShellWaiting))
	_, p, i, err := s.remedyOf(context.Background(), todo)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.patchPlanTask(context.Background(), p.ID, i, func(pt *PlanTask) {
		pt.Launcher, pt.Model, pt.Tier = "first", "", "deep"
	}); err != nil {
		t.Fatal(err)
	}
	if st := retryRestart(t, s, f, todo, map[string]any{"launcher": "first"}); st["launcher"] != "first" || !unset(st["model"]) {
		t.Fatalf("same-launcher retry started %v, want the native model default", st)
	}
	stored, err := s.loadPlan(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored.Tasks[i].Tier; got != "" {
		t.Fatalf("same-launcher retry left saved tier %q, want it cleared", got)
	}
	pt := stored.Tasks[i]
	before := len(startsOf(f))
	if _, _, err := s.startTaskAgent(context.Background(), &pt, true); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[before]; st["launcher"] != "first" || !unset(st["model"]) {
		t.Fatalf("read-back start carried %v, want the saved native model default without the old tier", st)
	}
}

// A permission the launcher generated is not a caller choice. A later start on another runtime settles the current
// configured or ordinary task default instead of carrying the old Allow-all mode across the runtime boundary.
func TestTaskRetryResettlesGeneratedPermission(t *testing.T) {
	for _, origin := range []string{"plan", "start"} {
		for _, c := range []struct {
			name       string
			configured string
			want       string
		}{
			{"configured mode", "plan", "plan"},
			{"ordinary task default", "", "default"},
		} {
			t.Run(origin+"/"+c.name, func(t *testing.T) {
				s, f := restartHub(t)
				var todo string
				if origin == "plan" {
					todo, _ = startRestartPlan(t, s, f, planRestartTask(t, s, nil), errors.New("the example start failed"))
				} else {
					todo = failedOnceOnFirst(t, s, f, nil)
				}
				setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.other", Model: "model-two", PermissionMode: c.configured})
				st := retryRestart(t, s, f, todo, nil)
				if st["permission_mode"] != c.want {
					t.Fatalf("retry permission_mode %v, want %q (start %v)", st["permission_mode"], c.want, st)
				}
			})
		}
	}
}

// An actual caller permission remains the caller's across the same runtime change, including Allow all itself.
func TestTaskRetryKeepsExplicitPermission(t *testing.T) {
	for _, mode := range []string{"bypassPermissions", "acceptEdits"} {
		t.Run(mode, func(t *testing.T) {
			s, f := restartHub(t)
			todo, _ := startRestartPlan(t, s, f, planRestartTask(t, s, map[string]any{"permission": mode}), errors.New("the example start failed"))
			setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.other", Model: "model-two", PermissionMode: "plan"})
			if st := retryRestart(t, s, f, todo, nil); st["permission_mode"] != mode {
				t.Fatalf("retry permission_mode %v, want explicit %q", st["permission_mode"], mode)
			}
		})
	}
}

// A launcher override between two launchers of the same non-Allow-all agent keeps the mode the failed start settled.
// Caller provenance must not reset that deliberate same-agent preservation to the ordinary task default.
func TestTaskRetrySameAgentKeepsGeneratedPermission(t *testing.T) {
	for _, target := range []string{"codex-a", "codex-b"} {
		t.Run(target, func(t *testing.T) {
			s, f := restartHub(t)
			f.zshrc["box"] = append(f.zshrc["box"],
				proto.Launcher{Name: "codex-a", Source: "zshrc", Agent: "codex"},
				proto.Launcher{Name: "codex-b", Source: "zshrc", Agent: "codex"})
			setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.codex-a", Model: "model-zero", PermissionMode: "plan"})
			id := planRestartTask(t, s, map[string]any{"launcher": "codex-a"})
			todo, first := startRestartPlan(t, s, f, id, errors.New("the example start failed"))
			if first["permission_mode"] != "plan" {
				t.Fatalf("first permission_mode %v, want the binding's plan", first["permission_mode"])
			}
			setSettings(t, s, func(v *Settings) { v.DefaultPermissionMode = "acceptEdits" })
			setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.codex-a", Model: "model-zero", PermissionMode: "acceptEdits"})
			s.taskRetry = time.Millisecond
			calls := 0
			f.onStart = func(_ *fakeHub, _ map[string]any) {
				calls++
				if calls == 1 {
					f.failStart = &rpc.Error{Code: proto.CodeTerminalNotReady, Message: "pane_send_failed"}
				} else {
					f.failStart = nil
				}
			}
			before := len(startsOf(f))
			if st := retryRestart(t, s, f, todo, map[string]any{"launcher": target}); st["permission_mode"] != "plan" {
				t.Fatalf("same-agent retry permission_mode %v, want the settled plan (start %v)", st["permission_mode"], st)
			}
			starts := startsOf(f)
			if len(starts) != before+2 || starts[before]["permission_mode"] != "plan" || starts[before+1]["permission_mode"] != "plan" {
				t.Fatalf("same-agent automatic attempts %v, want plan on both starts", starts[before:])
			}
		})
	}
}

// Admission can serialize and read back a retry before it starts. The same-agent permission decision must survive
// without process-only fields or the cleared failure arguments.
func TestTaskRetrySameAgentPermissionSurvivesQueueReadBack(t *testing.T) {
	s, f := restartHub(t)
	f.zshrc["box"] = append(f.zshrc["box"],
		proto.Launcher{Name: "codex-a", Source: "zshrc", Agent: "codex"},
		proto.Launcher{Name: "codex-b", Source: "zshrc", Agent: "codex"})
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.codex-a", Model: "model-zero", PermissionMode: "plan"})
	id := planRestartTask(t, s, map[string]any{"launcher": "codex-a"})
	todo, _ := startRestartPlan(t, s, f, id, errors.New("the example start failed"))
	_, p, i, err := s.remedyOf(context.Background(), todo)
	if err != nil {
		t.Fatal(err)
	}
	pt := p.Tasks[i]
	m, ok := s.machineByName(context.Background(), "box")
	if !ok {
		t.Fatal("box not found")
	}
	if err := s.retryOn(context.Background(), &pt, m, retryWith{launcher: "codex-b"}); err != nil {
		t.Fatal(err)
	}
	if pt.Permission != "plan" {
		t.Fatalf("retryOn permission %q, want the settled plan", pt.Permission)
	}
	pt.ErrorArgs, pt.Queue = nil, &TaskQueue{Retry: true} // what the retry write-back and admission queue persist
	raw, err := json.Marshal(pt)
	if err != nil {
		t.Fatal(err)
	}
	var queued PlanTask
	if err := json.Unmarshal(raw, &queued); err != nil {
		t.Fatal(err)
	}
	setSettings(t, s, func(v *Settings) { v.DefaultPermissionMode = "acceptEdits" })
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.codex-a", Model: "model-zero", PermissionMode: "acceptEdits"})
	before := len(startsOf(f))
	if _, _, err := s.startTaskAgent(context.Background(), &queued, true); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[before]; st["permission_mode"] != "plan" {
		t.Fatalf("queued same-agent retry permission_mode %v, want the settled plan (start %v)", st["permission_mode"], st)
	}
}

// The post-override launcher is not evidence of the old agent. A queued cross-runtime retry must settle the new
// binding's permission rather than preserving the ordinary mode retryOn entered with.
func TestTaskRetryCrossRuntimePermissionAfterQueueReadBack(t *testing.T) {
	s, f := restartHub(t)
	id := planRestartTask(t, s, nil)
	todo, first := startRestartPlan(t, s, f, id, errors.New("the example start failed"))
	if first["permission_mode"] != "bypassPermissions" {
		t.Fatalf("first permission_mode %v, want generated Allow all", first["permission_mode"])
	}
	_, p, i, err := s.remedyOf(context.Background(), todo)
	if err != nil {
		t.Fatal(err)
	}
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.other", Model: "model-two", PermissionMode: "plan"})
	pt := p.Tasks[i]
	m, ok := s.machineByName(context.Background(), "box")
	if !ok {
		t.Fatal("box not found")
	}
	if err := s.retryOn(context.Background(), &pt, m, retryWith{launcher: "other"}); err != nil {
		t.Fatal(err)
	}
	pt.ErrorArgs, pt.Queue = nil, &TaskQueue{Retry: true}
	raw, err := json.Marshal(pt)
	if err != nil {
		t.Fatal(err)
	}
	var queued PlanTask
	if err := json.Unmarshal(raw, &queued); err != nil {
		t.Fatal(err)
	}
	before := len(startsOf(f))
	if _, _, err := s.startTaskAgent(context.Background(), &queued, true); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[before]; st["permission_mode"] != "plan" {
		t.Fatalf("queued cross-runtime retry permission_mode %v, want the new binding's plan (start %v)", st["permission_mode"], st)
	}
}

// Launcher names are machine-local. A flexible retry rerouted to another computer must compare the failed agent kind,
// not resolve the old name again on the destination where it may name a different runtime.
func TestTaskRetryDoesNotResolveOldLauncherOnDestination(t *testing.T) {
	s, f := restartHub(t)
	f.zshrc["box"] = append(f.zshrc["box"],
		proto.Launcher{Name: "old", Source: "zshrc", Agent: "claude"},
		proto.Launcher{Name: "new", Source: "zshrc", Agent: "claude"})
	f.devices = append(f.devices, Device{ID: "otherbox", Name: "otherbox", Home: "/Users/me", Online: true, Reachable: true})
	f.sessions["otherbox"] = nil
	f.zshrc["otherbox"] = []proto.Launcher{{Name: "old", Source: "zshrc", Agent: "codex"}, {Name: "new", Source: "zshrc", Agent: "codex"}}
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.new", Model: "model-two", PermissionMode: "plan"})
	empty := ""
	pt := PlanTask{Title: "Fix", Prompt: "Fix", Machine: "box", Cwd: "/Users/me/code/app", Launcher: "old",
		Permission: "bypassPermissions", CallerPermission: &empty, ErrorArgs: map[string]string{"launcher": "old"}}
	m, ok := s.machineByName(context.Background(), "box")
	if !ok {
		t.Fatal("box not found")
	}
	if err := s.retryOn(context.Background(), &pt, m, retryWith{launcher: "new"}); err != nil {
		t.Fatal(err)
	}
	pt.Machine = "otherbox" // the admission gate's flexible reroute after retry provenance was recorded
	before := len(startsOf(f))
	if _, _, err := s.startTaskAgent(context.Background(), &pt, true); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[before]; st["permission_mode"] != "plan" {
		t.Fatalf("rerouted retry permission_mode %v, want the destination runtime's plan (start %v)", st["permission_mode"], st)
	}
}

// A retry can wait while the owner strengthens read-only mode. Current normalization must win over the mode captured
// from the earlier attempt, even when the launcher still runs the same agent.
func TestTaskRetryReadonlyModeChangeWinsOverSettledPermission(t *testing.T) {
	s, f := restartHub(t)
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.other", Model: "model-two"})
	setSettings(t, s, func(v *Settings) { v.ReadonlyMode = readonlyPrompt })
	empty := ""
	pt := PlanTask{Title: "Fix", Prompt: "Fix", Machine: "box", Cwd: "/Users/me/code/app", Launcher: "other", Readonly: true,
		Permission: "default", CallerPermission: &empty, ErrorArgs: map[string]string{"launcher": "other"}}
	m, ok := s.machineByName(context.Background(), "box")
	if !ok {
		t.Fatal("box not found")
	}
	if err := s.retryOn(context.Background(), &pt, m, retryWith{launcher: "other"}); err != nil {
		t.Fatal(err)
	}
	setSettings(t, s, func(v *Settings) { v.ReadonlyMode = readonlySandbox }) // changed while the retry waited
	before := len(startsOf(f))
	if _, _, err := s.startTaskAgent(context.Background(), &pt, true); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[before]; st["permission_mode"] != "plan" {
		t.Fatalf("retry after read-only change permission_mode %v, want normalized plan (start %v)", st["permission_mode"], st)
	}
}

// A plan row written before caller-choice markers existed is opaque. Its non-empty launch tuple is retained rather
// than guessing that the old launcher, model, effort or permission had been generated by a default.
func TestLegacyOpaqueLaunchTupleStaysConservative(t *testing.T) {
	s, f := restartHub(t)
	id := planRestartTask(t, s, nil)
	todo, _ := startRestartPlan(t, s, f, id, errors.New("the example start failed"))
	if err := s.patchPlanTask(context.Background(), id, 0, func(pt *PlanTask) {
		raw, _ := json.Marshal(pt)
		var task map[string]any
		_ = json.Unmarshal(raw, &task)
		task["launcher"], task["model"], task["effort"], task["permission"] = "first", "model-three", "high", "bypassPermissions"
		delete(task, "launcher_default_model")
		delete(task, "caller_permission")
		raw, _ = json.Marshal(task)
		var legacy PlanTask
		_ = json.Unmarshal(raw, &legacy)
		*pt = legacy
	}); err != nil {
		t.Fatal(err)
	}
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.other", Model: "model-two", Effort: "low", PermissionMode: "plan"})
	st := retryRestart(t, s, f, todo, nil)
	if got := launchOf(st); got != [3]string{"first", "model-three", "high"} || st["permission_mode"] != "bypassPermissions" {
		t.Fatalf("legacy retry started %v, want the opaque saved tuple", st)
	}
}

// The current default's effort and the task's explicit ultracode choice reach the retry unchanged. Placeholder models
// keep the test independent of any configured provider or model; no live model is called.
func TestTaskRetryKeepsMaxEffortAndUltracode(t *testing.T) {
	s, f := restartHub(t)
	todo, _ := startRestartPlan(t, s, f, planRestartTask(t, s, map[string]any{"ultracode": true}), errors.New("the example start failed"))
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.second", Model: "model-two", Effort: "max"})
	st := retryRestart(t, s, f, todo, nil)
	if got := launchOf(st); got != [3]string{"second", "model-two", "max"} || st["ultracode"] != true {
		t.Fatalf("retry started %v, want the current model at max effort with ultracode on", st)
	}
}

// A default substituted while the plan card is prepared becomes the plan-owned launcher the card confirms. It does
// not switch back to the current default on a later retry; an explicit model still survives that substitution.
func TestPlanTimeSubstitutionStaysPlanOwned(t *testing.T) {
	s, f := launcherFitHub(t)
	ctx := context.Background()
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions",
		"prompt":"Reply with pong","machine":"l4","cwd":"~/code/app","model":"model-three"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planID(t, res)
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if pt := p.Tasks[0]; pt.Launcher != "claude" || pt.Model != "model-three" {
		t.Fatalf("plan-time substitution saved %q/%q, want claude with the explicit model", pt.Launcher, pt.Model)
	}
	f.mu.Lock()
	f.failStart = errors.New("the example start failed")
	f.mu.Unlock()
	if _, err := s.startPlan(ctx, id, nil, false); err != nil {
		t.Fatal(err)
	}
	todo := todoIDOfPlan(t, s, id)
	waitFor(t, "the substituted start to fail", func() bool { return todoNow(s, todo).StartError != "" && s.taskStarting.Load() == 0 })
	f.mu.Lock()
	f.failStart = nil
	f.mu.Unlock()
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.gateway", Model: "model-four", Effort: "low"})
	before := len(startsOf(f))
	if _, err := s.retryTask(ctx, todo); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the plan-owned retry", func() bool { return len(startsOf(f)) > before && s.taskStarting.Load() == 0 })
	if st := startsOf(f)[before]; st["launcher"] != "claude" || st["model"] != "model-three" {
		t.Fatalf("retry started %v, want the plan-owned substitution and explicit model", st)
	}
}

// backendOwnerStore supplies the naming record but no physical workspace store. The start therefore fails after
// launcher and permission resolution, which is enough to exercise the retry's provenance without a real database.
type backendOwnerStore struct {
	restartHistoryStore
	naming *workspacenaming.Plan
}

func (st *backendOwnerStore) TodoWorkspaceRules(context.Context, string) (repositorystore.WorkspaceRulesRecord, error) {
	return repositorystore.WorkspaceRulesRecord{}, nil
}
func (st *backendOwnerStore) SetTodoWorkspaceRules(context.Context, string, int64, json.RawMessage) (repositorystore.WorkspaceRulesRecord, error) {
	return repositorystore.WorkspaceRulesRecord{}, nil
}
func (st *backendOwnerStore) WorkspaceNamingPlan(context.Context, string, string) (*workspacenaming.Plan, error) {
	return st.naming, nil
}
func (st *backendOwnerStore) WorkspaceNamingPlanForSave(context.Context, string, string) (*workspacenaming.Plan, error) {
	return st.naming, nil
}
func (st *backendOwnerStore) SaveWorkspaceNamingPlan(_ context.Context, _, _ string, _ int64, p workspacenaming.Plan) (*workspacenaming.Plan, error) {
	st.naming = &p
	return &p, nil
}

// A newly created backend-owner plan must not look like an opaque legacy row. Its caller named no permission, so the
// next runtime gets to settle its own rather than retaining the first Claude start's generated Allow-all mode.
func TestOwnerBackendPlanInitializesCallerPermission(t *testing.T) {
	st := &backendOwnerStore{restartHistoryStore: restartHistoryStore{&profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}}
	s, f := restartHubWithStore(t, st)
	ctx := context.Background()
	todo, err := s.createTaskTodo(ctx, "Backend permission retry", "Retry the backend start", "conv1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	st.naming = &workspacenaming.Plan{Source: workspacenaming.Source{
		Manager: s.managerIdentity(), TodoID: todo.ID, MachineID: "box", PlannedAt: time.Now(), Timezone: "UTC",
	}, RootPath: "/home/example"}
	if f.nodes == nil {
		f.nodes = map[string]nodeAnswer{}
	}
	f.nodes["task.workspace.capabilities"] = func(string, json.RawMessage) (any, error) {
		return map[string]any{"capability": "task_workspace", "start": true}, nil
	}
	ownerCtx := context.WithValue(ownerStart(ctx), ownerBackendTodoKey{}, todo.ID)
	plan, err := s.ownerBackendPlan(ownerCtx, todo.ID, backendStartInput{StartID: "start_permission_retry", OperationID: "op_permission_retry", Host: "herdr"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the backend start to fail after permission resolution", func() bool {
		return todoNow(s, todo.ID).StartError != "" && s.taskStarting.Load() == 0
	})
	f.mu.Lock()
	f.launcherList = []proto.Launcher{{Name: "other", Agent: "codex"}}
	f.mu.Unlock()
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.other", PermissionMode: "plan"})
	args, _ := json.Marshal(map[string]string{"todo_id": todo.ID, "reason": "settle the current runtime permission"})
	if _, err := s.toolTaskRetry(ctx, "conv1", args); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the backend retry to finish", func() bool {
		p, err := s.loadPlan(ctx, plan.ID)
		return err == nil && p.Tasks[0].Start != nil && p.Tasks[0].Start.State == remedyFailed && s.taskStarting.Load() == 0
	})
	stored, err := s.loadPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	pt := stored.Tasks[0]
	if pt.Permission != "plan" {
		t.Fatalf("backend retry saved effective=%q, want current plan", pt.Permission)
	}
}

// A full card round-trip with the same effective permission is not a new caller choice. Only a real permission edit
// replaces caller provenance, so an unrelated edit cannot pin generated Allow-all across a runtime change.
func TestTaskPlanEditDoesNotPromoteSettledPermission(t *testing.T) {
	for _, c := range []struct {
		name string
		edit PlanTask
		want string
	}{
		{"unchanged generated permission", PlanTask{Title: "Renamed", Permission: "bypassPermissions"}, "plan"},
		{"actually changed permission", PlanTask{Permission: "acceptEdits"}, "acceptEdits"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := restartHub(t)
			id := planRestartTask(t, s, nil)
			f.mu.Lock()
			f.failStart = errors.New("the example start failed")
			f.mu.Unlock()
			if _, err := s.startPlan(context.Background(), id, []PlanTask{c.edit}, false); err != nil {
				t.Fatal(err)
			}
			todo := todoIDOfPlan(t, s, id)
			waitFor(t, "the edited start to fail", func() bool { return todoNow(s, todo).StartError != "" && s.taskStarting.Load() == 0 })
			f.mu.Lock()
			f.failStart = nil
			f.mu.Unlock()
			setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.other", Model: "model-two", PermissionMode: "plan"})
			if st := retryRestart(t, s, f, todo, nil); st["permission_mode"] != c.want {
				t.Fatalf("retry after card edit carried %v, want %q", st["permission_mode"], c.want)
			}
		})
	}
}

// The direct agent_start path carries an explicit caller model through the same default-launcher fallback as task_plan.
func TestAgentStartFallbackKeepsExplicitModel(t *testing.T) {
	s, f := launcherFitHub(t)
	out, err := s.runTool(i18n.With(context.Background(), i18n.En), "server", "conv9", "agent_start", json.RawMessage(`{"machine":"l4","cwd":"/home/me/code","prompt":"Reply with pong","model":"model-three"}`))
	if err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["launcher"] != "claude" || st["model"] != "model-three" {
		t.Fatalf("agent_start fallback carried %v, want claude with the explicit model", st)
	}
	if !strings.Contains(out, "model-three") || strings.Contains(out, "default model") {
		t.Fatalf("fallback confirmation does not name the explicit model: %q", out)
	}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		note, _, err := s.fitLauncher(i18n.With(context.Background(), lang), Device{ID: "l4", Name: "l4"}, startPick{model: "model-three"}, map[string]any{"launcher": "gateway", "model": "model-three"})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(note, "model-three") || strings.Contains(note, "default model") || strings.Contains(note, "既定のモデル") {
			t.Fatalf("fallback note in %s does not preserve the explicit model: %q", lang, note)
		}
	}
}

// An automatic second attempt starts from the caller's fields again, not from a model the first try resolved. A
// default-launcher fallback on that second attempt uses the replacement's native default, not the old default's model.
func TestAutomaticRetryDoesNotPreserveGeneratedModelOnFallback(t *testing.T) {
	s, f := launcherFitHub(t)
	s.taskRetry = time.Millisecond
	available := f.zshrc["l4"]
	f.mu.Lock()
	delete(f.zshrc, "l4")
	f.mu.Unlock()
	res, err := s.toolTaskPlan(context.Background(), "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions",
		"prompt":"Reply with pong","machine":"l4","cwd":"~/code/app"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planID(t, res)
	calls := 0
	f.onStart = func(_ *fakeHub, _ map[string]any) {
		calls++
		if calls == 1 {
			f.failStart = &rpc.Error{Code: proto.CodeTerminalNotReady, Message: "pane_send_failed"}
			f.zshrc["l4"] = available
		} else {
			f.failStart = nil
		}
	}
	if _, err := s.startPlan(context.Background(), id, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the automatic second start", func() bool { return len(startsOf(f)) >= 2 && s.taskStarting.Load() == 0 })
	starts := startsOf(f)
	if len(starts) != 2 || starts[0]["model"] != "gw-model" || !unset(starts[1]["model"]) {
		t.Fatalf("automatic retry starts %v, want old generated model only on first start and native default on fallback", starts)
	}
}
func TestPolicyPresetKeepsAssignmentPermissionForTaskSpawn(t *testing.T) {
	for _, c := range []struct {
		name              string
		beforeStart       string
		callerPermission  string
		presetLauncher    string
		bindingPermission string
		wantStored        string
		wantSpawn         string
	}{
		{"unchanged default", "acceptEdits", "", "other", "", "acceptEdits", "acceptEdits"},
		{"changed default", "plan", "", "other", "", "plan", "plan"},
		{"explicit caller with Allow all", "acceptEdits", "acceptEdits", "first", "", "acceptEdits", "bypassPermissions"},
		{"explicit plan on Allow all", "acceptEdits", "plan", "first", "plan", "plan", "plan"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := restartHub(t)
			setSettings(t, s, func(v *Settings) { v.DefaultPermissionMode = "acceptEdits" })
			saveTier(t, s, "deep", ModelTier{Launcher: c.presetLauncher})
			stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
				return StartDefaults{Presets: []PresetChoice{{Name: "deep"}}, By: "Example"}, true
			})
			setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.other", Model: "model-zero"})
			var extra map[string]any
			if c.callerPermission != "" {
				extra = map[string]any{"permission": c.callerPermission}
			}
			id := planRestartTask(t, s, extra)
			setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.first", Model: "model-one", Effort: "high", PermissionMode: c.bindingPermission})
			setSettings(t, s, func(v *Settings) { v.DefaultPermissionMode = c.beforeStart })
			todo, first := startRestartPlan(t, s, f, id, nil)
			parent := todoNow(s, todo).Agents[0]
			cwd, _ := first["cwd"].(string)
			if first["permission_mode"] != c.wantSpawn {
				t.Fatalf("parent permission_mode %v, want %q", first["permission_mode"], c.wantSpawn)
			}
			agent := "claude"
			if c.presetLauncher == "other" {
				agent = "codex"
			}
			f.setSessions("box", proto.Session{SessionID: parent.SessionID, MachineID: "box", Agent: agent, Launcher: c.presetLauncher, Cwd: cwd, Status: proto.StatusWorking})
			if res := s.mcpCall(context.Background(), "box", parent.SessionID, "task_spawn", json.RawMessage(spawnArgs), false); res.IsError {
				t.Fatalf("task_spawn failed: %+v", res.Content)
			}
			stored, err := s.loadPlan(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if got := stored.Tasks[0].Permission; got != c.wantStored {
				t.Fatalf("stored assignment permission %q, want caller-owned %q", got, c.wantStored)
			}
			starts := startsOf(f)
			if got := starts[len(starts)-1]["permission_mode"]; got != c.wantSpawn {
				t.Fatalf("task_spawn permission_mode %v, want the assignment's effective %q (starts %v)", got, c.wantSpawn, starts)
			}
		})
	}
}
