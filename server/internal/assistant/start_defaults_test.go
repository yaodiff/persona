package assistant

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// stubStartDefaults makes s ask fn for every task start's defaults; seen are the facts each start gave, in order.
func stubStartDefaults(s *Service, fn func(StartFacts) (StartDefaults, bool)) (seen func() []StartFacts) {
	var mu sync.Mutex
	var facts []StartFacts
	s.startDefaultsFor = func(_ context.Context, f StartFacts) (StartDefaults, bool, error) {
		mu.Lock()
		facts = append(facts, f)
		mu.Unlock()
		d, ok := fn(f)
		return d, ok, nil
	}
	return func() []StartFacts {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(facts)
	}
}

// saveTiers makes a model profile with these tiers in use.
func saveTiers(t *testing.T, s *Service, tiers map[string]ModelTier) {
	t.Helper()
	ctx := context.Background()
	ps := modelProfiles{Revision: 1, Profiles: []ModelProfile{{ID: "p", Name: "P", Tiers: tiers}}}
	if err := s.store().PutSetting(ctx, modelProfilesKey, ps); err != nil {
		t.Fatal(err)
	}
	if err := s.store().PutSetting(ctx, modelProfileStateKey, modelProfileState{Active: "p"}); err != nil {
		t.Fatal(err)
	}
}

// fullDefaults asks for everything: ultracode, effort max, a goal from done_when, and these presets in order.
func fullDefaults(presets ...string) StartDefaults {
	on := true
	d := StartDefaults{Ultracode: &on, Effort: "max", Goal: GoalFromDoneWhen, By: "Example"}
	for _, p := range presets {
		d.Presets = append(d.Presets, PresetChoice{Name: p})
	}
	return d
}

// defaultsTask is a writable task on box that names nothing it runs on, with two criteria.
func defaultsTask() PlanTask {
	return PlanTask{Title: "Fix the build", Prompt: "fix the build", Machine: "box", Cwd: "/Users/me/code/app",
		Permission: "bypassPermissions", DoneWhen: []string{"CI is green", "the fix is merged"}}
}

// defaultsHub is launcherFitHub (box has the launcher gateway, l4 has other; Model Settings › Agents runs gateway's
// gw-model at high) with three presets in use.
func defaultsHub(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	s, f := launcherFitHub(t)
	saveTiers(t, s, map[string]ModelTier{
		"deep":  {Launcher: "gateway", Model: "gw-deep", Effort: "high"},
		"quick": {Launcher: "gateway", Model: "gw-quick"},
		"plain": {Launcher: "claude", Model: "claude-plain"},
	})
	return s, f
}

// The defaults fill in what the task says nothing about: the first usable preset (its effort overridden), ultracode,
// effort max and a goal from done_when; the card's task records what ran and why.
func TestStartDefaultsFillWhatTheTaskLeavesOpen(t *testing.T) {
	s, f := defaultsHub(t)
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return fullDefaults("deep", "quick"), true })
	task := defaultsTask()
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	st := startsOf(f)[0]
	if st["launcher"] != "gateway" || st["model"] != "gw-deep" || st["effort"] != "max" || st["ultracode"] != true {
		t.Fatalf("start = %v", st)
	}
	if want := prompts.RenderTaskGoal(task.DoneWhen); st["goal"] != want || !strings.Contains(want, "CI is green") ||
		strings.Contains(want, "\n") {
		t.Fatalf("goal = %q, want %q", st["goal"], want)
	}
	if !strings.Contains(st["prompt"].(string), "fix the build") {
		t.Fatalf("the prompt went somewhere else: %v", st["prompt"])
	}
	if task.Ultracode == nil || !*task.Ultracode || task.Effort != "max" || task.Model != "gw-deep" || !strings.Contains(task.Why, "Example") {
		t.Fatalf("task = %+v", task)
	}
}

// Whatever the caller named itself wins: an effort, ultracode off, a tier, a model or a launcher.
func TestStartDefaultsYieldToTheCaller(t *testing.T) {
	off := false
	for _, c := range []struct {
		name string
		edit func(*PlanTask)
		want map[string]any // values the start must carry; nil: the key must be unset
	}{
		{"effort", func(p *PlanTask) { p.Effort = "low" },
			map[string]any{"effort": "low", "model": "gw-deep", "ultracode": true}},
		{"tier without one keeps the source's effort", func(p *PlanTask) { p.Tier = "quick" },
			map[string]any{"effort": "max", "model": "gw-quick", "ultracode": true}},
		{"ultracode off", func(p *PlanTask) { p.Ultracode = &off },
			map[string]any{"effort": "max", "model": "gw-deep", "ultracode": nil}},
		{"tier with an effort", func(p *PlanTask) { p.Tier = "deep" },
			map[string]any{"effort": "high", "model": "gw-deep", "ultracode": true}},

		{"model", func(p *PlanTask) { p.Model = "gw-own" },
			map[string]any{"launcher": "gateway", "effort": "max", "model": "gw-own", "ultracode": true}},
		{"launcher", func(p *PlanTask) { p.Launcher = "claude" },
			map[string]any{"launcher": "claude", "effort": "max", "model": nil, "ultracode": true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := defaultsHub(t)
			stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return fullDefaults("deep"), true })
			task := defaultsTask()
			c.edit(&task)
			if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
				t.Fatal(err)
			}
			st := startsOf(f)[0]
			for k, v := range c.want {
				if v == nil && !unset(st[k]) && st[k] != false || v != nil && st[k] != v {
					t.Fatalf("%s = %v, want %v (start %v)", k, st[k], v, st)
				}
			}
		})
	}
}

// No defaults, the source saying none or none at all: the start is exactly what it was before defaults existed.
func TestStartDefaultsNoneLeaveTheStartAsItWas(t *testing.T) {
	run := func(stub bool) map[string]any {
		s, f := defaultsHub(t)
		if stub {
			stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return StartDefaults{}, false })
		}
		task := defaultsTask()
		if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
			t.Fatal(err)
		}
		st := maps.Clone(startsOf(f)[0])
		delete(st, "session_id") // a new id each start
		return st
	}
	before, after := run(false), run(true)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("without defaults:\n%v\nwith none asked:\n%v", before, after)
	}
	for _, k := range []string{"goal", "ultracode"} {
		if _, ok := after[k]; ok {
			t.Fatalf("%s sent without defaults: %v", k, after)
		}
	}
}

// A preset that cannot run on the task's computer now is passed over for the next: not in the profile, a launcher the
// computer lacks, a launcher at its usage limit there.
func TestStartDefaultsPassOverPresetsThatCannotRun(t *testing.T) {
	s, f := defaultsHub(t)
	ctx := context.Background()
	saveTiers(t, s, map[string]ModelTier{
		"elsewhere": {Launcher: "other", Model: "o-1"}, // l4's, not box's
		"limited":   {Launcher: "gateway", Model: "gw-deep"},
		"plain":     {Launcher: "claude", Model: "claude-plain"},
	})
	resets := time.Now().Add(2 * time.Hour)
	s.recordHealth(ctx, "box", "gateway", healthResult{err: "You've hit your usage limit", source: healthSourceSession,
		kind: errKindUsageLimit, resetsAt: &resets})
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
		return fullDefaults("gone", "elsewhere", "limited", "plain"), true
	})
	task := defaultsTask()
	task.MachinePolicy = machineFlexible // a fixed task would fail on the limited launcher instead of passing over
	if _, _, err := s.startTaskAgent(ctx, &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["launcher"] != "claude" || st["model"] != "claude-plain" {
		t.Fatalf("start = %v", st)
	}
	for _, name := range []string{"gone", "elsewhere", "limited"} {
		if !strings.Contains(task.Why, name) {
			t.Fatalf("why does not say %s was passed over: %q", name, task.Why)
		}
	}
	// None usable: the task starts as it would have with no preset, and says why.
	s, f = defaultsHub(t)
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return fullDefaults("gone"), true })
	task = defaultsTask()
	if _, _, err := s.startTaskAgent(ctx, &task, false); err != nil {
		t.Fatal(err)
	}
	st := startsOf(f)[0]
	if st["launcher"] != "gateway" || st["model"] != "gw-model" {
		t.Fatalf("no usable preset: %v", st)
	}
	if st["effort"] != "max" { // no caller effort: the source's effort still wins over Model Settings
		t.Fatalf("effort = %v, want the source's max", st["effort"])
	}
}

// Weights share starts among the usable presets in proportion; without weights the first usable one always wins.
func TestStartDefaultsWeights(t *testing.T) {
	ctx := context.Background()
	pick := func(roll int, choices ...PresetChoice) any {
		s, f := defaultsHub(t)
		old := presetIntN
		presetIntN = func(n int) int {
			if n != 4 {
				t.Fatalf("rolled over %d, want the weights' sum 4", n)
			}
			return roll
		}
		t.Cleanup(func() { presetIntN = old })
		d := fullDefaults()
		d.Presets = choices
		stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return d, true })
		task := defaultsTask()
		if _, _, err := s.startTaskAgent(ctx, &task, false); err != nil {
			t.Fatal(err)
		}
		return startsOf(f)[0]["model"]
	}
	weighted := []PresetChoice{{Name: "deep", Weight: 1}, {Name: "gone", Weight: 5}, {Name: "quick", Weight: 3}}
	if got := pick(0, weighted...); got != "gw-deep" {
		t.Fatalf("roll 0: %v", got)
	}
	if got := pick(1, weighted...); got != "gw-quick" {
		t.Fatalf("roll 1: %v", got)
	}
	if got := pick(3, weighted...); got != "gw-quick" {
		t.Fatalf("roll 3: %v", got)
	}
}

// A goal goes only with criteria, to Claude Code and Codex alike; ultracode and effort max only to Claude Code.
func TestStartDefaultsOnlyWhatTheAgentTakes(t *testing.T) {
	s, f := defaultsHub(t)
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return fullDefaults(), true })
	task := defaultsTask()
	task.DoneWhen = nil
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["goal"] != nil || st["ultracode"] != true {
		t.Fatalf("no criteria: %v", st)
	}

	s, f = defaultsHub(t)
	f.zshrc["box"] = append(f.zshrc["box"], proto.Launcher{Name: "cx", Source: "zshrc", Agent: "codex"})
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return fullDefaults(), true })
	task = defaultsTask()
	task.Launcher = "cx"
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["goal"] != prompts.RenderTaskGoal(task.DoneWhen) || st["ultracode"] != nil || st["effort"] == "max" {
		t.Fatalf("codex: %v", st)
	}
	if st := startsOf(f)[0]; st["launcher"] != "cx" {
		t.Fatalf("the codex launcher was changed: %v", st)
	}

	// Codex without criteria: no goal either.
	s, f = defaultsHub(t)
	f.zshrc["box"] = append(f.zshrc["box"], proto.Launcher{Name: "cx", Source: "zshrc", Agent: "codex"})
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return fullDefaults(), true })
	task = defaultsTask()
	task.Launcher, task.DoneWhen = "cx", nil
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["goal"] != nil {
		t.Fatalf("codex without criteria: %v", st)
	}
}

// A source can ask for the self-planning constraint in the prompt: for launches whose model does not act on effort.
func TestStartDefaultsPlanFirstGoesInThePrompt(t *testing.T) {
	s, f := defaultsHub(t)
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
		d := fullDefaults()
		d.PlanFirst = true
		return d, true
	})
	task := defaultsTask()
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	if prompt, _ := startsOf(f)[0]["prompt"].(string); !strings.Contains(prompt, prompts.RenderPlanFirstTaskConstraint()) {
		t.Fatalf("prompt = %q", prompt)
	}
}

// A computer whose node cannot set a start goal starts the agent without one, and the task says why; with ultracode
// refused too, each goes in turn.
func TestStartDefaultsGoalRefusedStartsWithoutIt(t *testing.T) {
	s, f := defaultsHub(t)
	f.refuseGoal = &rpc.Error{Code: proto.CodeStartGoalUnsupported, Message: "the node is too old"}
	f.refuseUltra = &rpc.Error{Code: "ultracode_unsupported", Message: "no ultracode here"}
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return fullDefaults(), true })
	task := defaultsTask()
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	// The node refuses ultracode first, then the goal: three tries, each without what the one before was refused.
	starts := startsOf(f)
	if len(starts) != 3 || starts[0]["goal"] == nil || starts[0]["ultracode"] != true ||
		starts[1]["goal"] == nil || starts[1]["ultracode"] != nil ||
		starts[2]["goal"] != nil || starts[2]["ultracode"] != nil {
		t.Fatalf("starts = %v", starts)
	}
	if !strings.Contains(task.Why, "the node is too old") || !strings.Contains(task.Why, "no ultracode here") {
		t.Fatalf("why = %q", task.Why)
	}
}

// The facts a start gives say which conversation actually follows the task up, not which one planned it. New work
// planned by a lead, or explicitly assigned to one by main, uses that lead’s class; read-only and role ride along.
// The explicit manual-Agent and task-leader worker topologies keep their specialized class.
func TestStartDefaultsFactsSayWhoSupervises(t *testing.T) {
	s, f := newQuietService(t)
	f.devices = []Device{{ID: "box", Name: "box", Home: "/home/me", Online: true, Reachable: true}}
	f.known["lead1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	seen := stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return StartDefaults{}, false })
	ctx := context.Background()
	plan := func(conversation, supervisor string, task map[string]any) {
		t.Helper()
		task["machine"], task["cwd"], task["why"] = "box", "/code/app", "the repo is on box"
		args := map[string]any{"tasks": []map[string]any{task}}
		if supervisor != "" {
			args["supervisor"] = supervisor // explicit initial assignment by main
		}
		raw, _ := json.Marshal(args)
		res, err := s.toolTaskPlan(ctx, conversation, raw)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res, "nothing has started") {
			id := planID(t, res)
			if id == "" {
				return // an auto-queued task dispatches itself
			}
			if _, err := s.startPlan(ctx, id, nil, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	planWait := func(want int) {
		waitFor(t, "a task's defaults", func() bool { return len(seen()) == want })
	}
	plan("lead1", "", map[string]any{"title": "A", "prompt": "fix it", "readonly": false, "done_when": []string{"CI is green"}, "expect_minutes": 30})
	planWait(1)
	plan("conv1", "", map[string]any{"title": "B", "prompt": "look it up", "readonly": true, "goal": "the answer", "expect_minutes": 10})
	planWait(2)
	plan("conv1", "lead1", map[string]any{"title": "C", "prompt": "fix that", "readonly": false, "done_when": []string{"CI is green"},
		"expect_minutes": 30})
	planWait(3)
	waitFor(t, "the starts to finish their bookkeeping", func() bool { return s.taskStarting.Load() == 0 })
	got := seen()
	if a := got[0]; a.Supervisor != dispatchClassLeader || a.Readonly || a.Role != "worker" || a.DoneWhen != 1 || a.Machine != "box" {
		t.Fatalf("task planned by the leader: %+v", a)
	}
	if b := got[1]; b.Supervisor != dispatchClassMain || !b.Readonly {
		t.Fatalf("main's own task: %+v", b)
	}
	if c := got[2]; c.Supervisor != dispatchClassLeader {
		t.Fatalf("task with an explicit supervisor: %+v", c)
	}
}

// putStartDefaults sends PUT /api/v1/assistant/start-defaults as role (full, extension, read-only) and answers the code
// and body.
func putStartDefaults(t *testing.T, s *Service, role, ext, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/assistant/start-defaults", strings.NewReader(body))
	if role != "" {
		req.Header.Set(RoleHeader, role)
	}
	if ext != "" {
		req.Header.Set(ExtensionHeader, ext)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// A policy matches the actual supervisor, not the planning conversation. A leader-only rule leaves new work alone even
// when a leader planned it; the explicit manual-leader worker topology still matches. Replacing it with a main rule
// fills writable starts, while read-only stays untouched. The policy is the one an extension wrote through the route.
func TestStartDefaultsPolicyOnlyWhereItsRuleMatches(t *testing.T) {
	s, f := newQuietService(t)
	f.devices = []Device{{ID: "box", Name: "box", Home: "/home/me", Online: true, Reachable: true}}
	f.known["lead1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	expires := time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339)
	code, body := putStartDefaults(t, s, roleExtension, "kit", `{"start_defaults":{"expires_at":"`+expires+`","rules":[
		{"when":{"supervisor":["leader"],"readonly":false},"then":{"effort":"max","ultracode":true,"goal":"done_when"}}]}}`)
	if code != http.StatusOK {
		t.Fatalf("put: %d %s", code, body)
	}
	ctx := context.Background()
	start := func(conversation string, readonly bool) map[string]any {
		t.Helper()
		before := len(startsOf(f))
		task := map[string]any{"title": "Fix", "prompt": "fix it", "machine": "box", "cwd": "/code/app", "why": "the repo is on box",
			"readonly": readonly, "expect_minutes": 30}
		if readonly {
			task["goal"] = "the answer"
		} else {
			task["done_when"] = []string{"CI is green"}
		}
		raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{task}})
		res, err := s.toolTaskPlan(ctx, conversation, raw)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(res, "nothing has started") {
			if _, err := s.startPlan(ctx, planID(t, res), nil, false); err != nil {
				t.Fatal(err)
			}
		}
		waitFor(t, "the start", func() bool { return len(startsOf(f)) == before+1 })
		return startsOf(f)[before]
	}
	led := start("lead1", false)
	if led["ultracode"] != true || led["goal"] == nil || led["effort"] != "max" {
		t.Fatalf("the planning lead missed its defaults: %v", led)
	}
	own := start("conv1", false)
	if own["ultracode"] != nil || own["goal"] != nil || own["effort"] == "max" {
		t.Fatalf("the main conversation's own task got the leader-only defaults: %v", own)
	}
	// The specialized manual-leader topology still matches. Its worker takes the leader conversation's class from the
	// parent task's ledger, rather than from the new-plan default (#5732).
	putTodo(s, Todo{ID: "todo_leader_defaults", Title: "L", Supervision: &Supervision{Supervisor: "lead1"},
		Agents: []TodoAgent{{MachineID: "server", SessionID: "lead1", Source: sourceLeader, Agent: "claude"}}})
	worker := PlanTask{Title: "W", Prompt: "fix it", Machine: "box", Cwd: "/code/app", Permission: "bypassPermissions",
		DoneWhen: []string{"CI is green"}, ExpectMinutes: 30, Parent: "todo_leader_defaults"}
	if _, _, err := s.startTaskAgent(ctx, &worker, false); err != nil {
		t.Fatal(err)
	}
	led = startsOf(f)[2]
	if led["ultracode"] != true || led["effort"] != "max" || led["goal"] == nil {
		t.Fatalf("a manual leader's worker started without the defaults: %v", led)
	}
	ro := start("lead1", true)
	if ro["ultracode"] != nil || ro["goal"] != nil || ro["effort"] == "max" {
		t.Fatalf("a read-only task got the leader-only defaults: %v", ro)
	}

	code, body = putStartDefaults(t, s, roleExtension, "kit", `{"start_defaults":{"expires_at":"`+expires+`","rules":[
		{"when":{"supervisor":["main"],"readonly":false},"then":{"effort":"max","ultracode":true,"goal":"done_when"}}]}}`)
	if code != http.StatusOK {
		t.Fatalf("put main rule: %d %s", code, body)
	}
	led = start("lead1", false)
	if led["ultracode"] != nil || led["effort"] == "max" || led["goal"] != nil {
		t.Fatalf("the planning lead received main-only defaults: %v", led)
	}
	own = start("conv1", false)
	if own["ultracode"] != true || own["effort"] != "max" || own["goal"] == nil {
		t.Fatalf("the main conversation's own task missed the defaults: %v", own)
	}
	ro = start("lead1", true)
	if ro["ultracode"] != nil || ro["goal"] != nil || ro["effort"] == "max" {
		t.Fatalf("a read-only task got the main defaults: %v", ro)
	}
	waitFor(t, "the starts to finish their bookkeeping", func() bool { return s.taskStarting.Load() == 0 })
}

// The route's rules: an extension's policy must expire within an hour; the owner may keep one without expiry; a
// read-only device is refused; an unknown field, class or effort is a 400; empty rules clear it; once expired it is
// gone from GET and from task starts.
func TestStartDefaultsRoute(t *testing.T) {
	s, f := defaultsHub(t)
	soon := time.Now().Add(30 * time.Minute).UTC().Format(time.RFC3339)
	late := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	rule := `"rules":[{"when":{},"then":{"effort":"max"}}]`
	for _, c := range []struct {
		name, role, body string
		code             int
	}{
		{"extension without expiry", roleExtension, `{"start_defaults":{` + rule + `}}`, http.StatusBadRequest},
		{"extension too far ahead", roleExtension, `{"start_defaults":{"expires_at":"` + late + `",` + rule + `}}`, http.StatusBadRequest},
		{"read-only device", "read", `{"start_defaults":{` + rule + `}}`, http.StatusForbidden},
		{"unknown field", roleFull, `{"start_defaults":{"rules":[{"when":{},"then":{"efort":"max"}}]}}`, http.StatusBadRequest},
		{"unknown class", roleFull, `{"start_defaults":{"rules":[{"when":{"supervisor":["owner"]},"then":{}}]}}`, http.StatusBadRequest},
		{"unknown effort", roleFull, `{"start_defaults":{"rules":[{"when":{},"then":{"effort":"banana"}}]}}`, http.StatusBadRequest},
		{"no body key", roleFull, `{}`, http.StatusBadRequest},
		{"owner without expiry", roleFull, `{"start_defaults":{` + rule + `}}`, http.StatusOK},
		{"extension within the hour", roleExtension, `{"start_defaults":{"expires_at":"` + soon + `",` + rule + `}}`, http.StatusOK},
	} {
		if code, body := putStartDefaults(t, s, c.role, "kit", c.body); code != c.code {
			t.Errorf("%s: %d %s", c.name, code, body)
		}
	}
	if p := s.activeStartDefaults(context.Background()); p == nil || p.By != "kit" {
		t.Fatalf("the extension's policy is not the one in force: %+v", p)
	}
	// The policy in force reaches a task start.
	task := defaultsTask()
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["effort"] != "max" {
		t.Fatalf("the policy did not reach the start: %v", st)
	}
	// Expired: gone from GET and from starts.
	past := time.Now().Add(-time.Minute)
	setSettings(t, s, func(v *Settings) { v.StartDefaults.ExpiresAt = &past })
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant/start-defaults", nil))
	if got := strings.TrimSpace(rec.Body.String()); got != `{"start_defaults":{"rules":[]}}` {
		t.Fatalf("expired policy still listed: %s", got)
	}
	task = defaultsTask()
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[1]; st["effort"] == "max" {
		t.Fatalf("an expired policy still applied: %v", st)
	}
	// Empty rules clear it.
	if code, body := putStartDefaults(t, s, roleExtension, "kit", `{"start_defaults":{"rules":[]}}`); code != http.StatusOK {
		t.Fatalf("clear: %d %s", code, body)
	}
	if s.settings(context.Background()).StartDefaults != nil {
		t.Fatal("not cleared")
	}
}

// A task that names its own model and asks ultracode off keeps both when a later try finds no matching policy: the
// policy's fill-in never replaces what the task named.
func TestStartDefaultsDroppedKeepsTheCallersModelAndUltracode(t *testing.T) {
	s, f := defaultsHub(t)
	off := false
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
		return StartDefaults{Effort: "max", Presets: []PresetChoice{{Name: "plain"}}}, true
	})
	task := defaultsTask()
	task.Model = "gw-own"
	task.Ultracode = &off
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	if !task.startFilled.any() {
		t.Fatal("the first try did not take the policy")
	}
	// The policy may not override the caller's model or its explicit ultracode off.
	st := startsOf(f)[0]
	if st["model"] != "gw-own" || st["ultracode"] != nil {
		t.Fatalf("the policy overrode the caller: %v", st)
	}
	// No rule applies on the second try: the model and ultracode off are still the caller's, the preset is gone.
	s.startDefaultsFor = nil
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	st = startsOf(f)[1]
	if st["model"] != "gw-own" {
		t.Fatalf("the caller's model was wiped: %v", st)
	}
	// A task that names a model gets no preset, so its launcher is Model Settings' on both tries; without the policy
	// the effort is Model Settings' again, not the policy's max.
	if st["ultracode"] != nil || st["launcher"] != "gateway" || st["effort"] == "max" {
		t.Fatalf("the retry kept policy values: %v", st)
	}
	// The explicit off is still the task's: a later policy asking ultracode on must not turn it on.
	if task.Ultracode == nil || *task.Ultracode {
		t.Fatalf("the caller's explicit ultracode off was lost: %v", task.Ultracode)
	}
	on := true
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return StartDefaults{Ultracode: &on}, true })
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[2]; st["ultracode"] != nil {
		t.Fatalf("a policy overrode the caller's explicit ultracode off: %v", st)
	}
}

// A preset the policy picks starts exactly as if the task had named that tier: the same launcher, model, launch
// fingerprint and permission, here for a preset whose launcher is not Model Settings' default.
func TestStartDefaultsPresetStartsAsIfTheTaskNamedIt(t *testing.T) {
	start := func(policy bool) map[string]any {
		t.Helper()
		s, f := defaultsHub(t)
		task := defaultsTask()
		if policy {
			stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
				return StartDefaults{Presets: []PresetChoice{{Name: "plain"}}}, true
			})
		} else {
			task.Tier = "plain"
		}
		if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
			t.Fatal(err)
		}
		if task.Tier != "plain" {
			t.Fatalf("the task does not name the preset it runs: %+v", task)
		}
		st := maps.Clone(startsOf(f)[0])
		delete(st, "session_id")
		return st
	}
	picked, named := start(true), start(false)
	if !reflect.DeepEqual(picked, named) {
		t.Fatalf("picked by the policy:\n%v\nnamed by the task:\n%v", picked, named)
	}
	if picked["launcher"] != "claude" || picked["model"] != "claude-plain" {
		t.Fatalf("not the preset's launcher and model: %v", picked)
	}
}

// A rule whose then does nothing is a mistake: it would shadow every later rule, so the write is refused; as is an
// extension request without the header naming the extension.
func TestStartDefaultsRouteRejectsEmptyThenAndAnUnknownExtension(t *testing.T) {
	s, _ := defaultsHub(t)
	if code, body := putStartDefaults(t, s, roleFull, "",
		`{"start_defaults":{"rules":[{"when":{},"then":{}}]}}`); code != http.StatusBadRequest {
		t.Fatalf("empty then: %d %s", code, body)
	}
	if code, body := putStartDefaults(t, s, roleExtension, "",
		`{"start_defaults":{"expires_at":"`+time.Now().Add(time.Minute).Format(time.RFC3339)+`","rules":[{"when":{},"then":{"effort":"max"}}]}}`); code != http.StatusForbidden {
		t.Fatalf("extension without its header: %d %s", code, body)
	}
	// The owner can also write the policy through the general settings route; empty rules clear it there.
	rec := httptest.NewRecorder()
	body := `{"start_defaults":{"rules":[{"when":{},"then":{"effort":"max"}}]}}`
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT settings: %d %s", rec.Code, rec.Body.String())
	}
	if got := s.settings(context.Background()).StartDefaults; got == nil || got.By != "owner" {
		t.Fatalf("the settings write was not applied: %+v", got)
	}
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(`{"start_defaults":{"rules":[]}}`)))
	if rec.Code != http.StatusOK || s.settings(context.Background()).StartDefaults != nil {
		t.Fatalf("empty rules did not clear the policy: %d %s", rec.Code, rec.Body.String())
	}
}

// The policy is visible only with its own scope: an assistant.ask extension reads GET /api/v1/assistant without it.
func TestStartDefaultsHiddenFromTheGeneralSettingsRoute(t *testing.T) {
	s, f := defaultsHub(t)
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
		return StartDefaults{Effort: "max"}, true
	})
	expires := time.Now().Add(time.Minute)
	setSettings(t, s, func(v *Settings) {
		v.StartDefaults = &StartDefaultsPolicy{ExpiresAt: &expires, Rules: []StartDefaultsRule{{Then: StartDefaultsThen{Effort: "max"}}}}
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/assistant", nil)
	req.Header.Set(RoleHeader, roleExtension)
	req.Header.Set(ExtensionHeader, "kit")
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "start_defaults") {
		t.Fatalf("an extension read the policy through assistant.ask: %d %s", rec.Code, rec.Body.String())
	}
	_ = f
}

// A try without a matching policy must not keep an earlier try's fill-in: the start goes back to what the task named,
// and the plan-first prompt and goal are gone.
func TestStartDefaultsDroppedWhenThePolicyNoLongerApplies(t *testing.T) {
	s, f := defaultsHub(t)
	on := func() bool { return true }
	off := func() bool { return false }
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
		if on() {
			return StartDefaults{Effort: "max", Presets: []PresetChoice{{Name: "deep"}}, PlanFirst: true}, true
		}
		return StartDefaults{}, false
	})
	task := defaultsTask()
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	if !task.startFilled.any() || task.planFirst != true || task.Tier != "deep" || task.Effort != "max" {
		t.Fatalf("the first try did not take the defaults: %+v", task)
	}
	on = off
	// A second try of the same task: no policy matches now, so everything it filled in is gone.
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	if task.startFilled.any() && (task.planFirst || task.Tier == "deep" || task.Effort == "max") {
		t.Fatalf("the second try kept the dropped policy: %+v", task)
	}
	st := startsOf(f)[1]
	if st["goal"] != nil || st["ultracode"] != nil || st["effort"] == "max" || st["model"] == "gw-deep" {
		t.Fatalf("the second start kept policy params: %v", st)
	}
	if st["launcher"] != "gateway" || st["model"] != "gw-model" { // Model Settings' default still applies
		t.Fatalf("the ordinary default launch changed: %v", st)
	}
	_ = off
}

// A hidden launcher still starts a preset: availability agrees with GET worker-presets, which offers hidden-but-usable
// launchers.
func TestStartDefaultsPresetOnAHiddenLauncher(t *testing.T) {
	s, f := defaultsHub(t)
	f.zshrc["box"] = append(f.zshrc["box"], proto.Launcher{Name: "secret", Source: "zshrc"})
	if err := s.putSetting(context.Background(), launchersHiddenKey, []string{"secret"}); err != nil {
		t.Fatal(err)
	}
	saveTiers(t, s, map[string]ModelTier{"h": {Launcher: "secret", Model: "m-1"}})
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
		return StartDefaults{Presets: []PresetChoice{{Name: "h"}}}, true
	})
	task := defaultsTask()
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["launcher"] != "secret" {
		t.Fatalf("the hidden preset was passed over: %v", st)
	}
}

// A rule can match a task leader (role) and a task leader's worker (parent): the facts carry the role and the class of
// the conversation that follows the leader's own task up.
func TestStartDefaultsRoleAndLeaderWorkerFacts(t *testing.T) {
	s, f := newQuietService(t)
	f.devices = []Device{{ID: "box", Name: "box", Home: "/home/me", Online: true, Reachable: true}}
	f.known["lead1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "lead1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	var got StartFacts
	stubStartDefaults(s, func(facts StartFacts) (StartDefaults, bool) { got = facts; return StartDefaults{}, false })
	ctx := context.Background()

	// The leader's own task, supervised by the leader conversation, with its coding agent (the session a worker's
	// parent lookup resolves) already running.
	putTodo(s, Todo{ID: "todo_leader", Title: "L",
		Supervision: &Supervision{Supervisor: "lead1"},
		Agents:      []TodoAgent{{MachineID: "server", SessionID: "lead1", Source: sourceLeader, Agent: "claude"}}})

	// A task leader the main conversation hands out: role leader, followed up by the leader conversation.
	leader := PlanTask{Title: "L", Prompt: "lead the work", Machine: "box", Cwd: "/code/app", Role: roleLeader,
		Permission: "bypassPermissions", DoneWhen: []string{"the worker finishes"}, ExpectMinutes: 30,
		Supervisor: "lead1", TodoID: "todo_leader2"}
	putTodo(s, Todo{ID: "todo_leader2", Title: "L", Supervision: &Supervision{Supervisor: "lead1"},
		Agents: []TodoAgent{{MachineID: "server", SessionID: "lead1", Source: sourceLeader, Agent: "claude"}}})
	if _, _, err := s.startTaskAgent(ctx, &leader, false); err != nil {
		t.Fatal(err)
	}
	if got.Role != roleLeader || got.Supervisor != dispatchClassLeader {
		t.Fatalf("the leader task: %+v", got)
	}

	// Its worker: the parent todo names the leader task; its supervisor is the leader agent, whose session the ledger
	// records as a leader, so the worker's class is still the leader conversation's.
	worker := PlanTask{Title: "W", Prompt: "do the work", Machine: "box", Cwd: "/code/app",
		Permission: "bypassPermissions", DoneWhen: []string{"CI is green"}, ExpectMinutes: 30,
		Parent: "todo_leader"}
	if _, _, err := s.startTaskAgent(ctx, &worker, false); err != nil {
		t.Fatal(err)
	}
	if got.Role != "worker" || got.Supervisor != dispatchClassLeader {
		t.Fatalf("the leader's worker: %+v", got)
	}
}

// A retry that a different rule fits starts from what the task named again: the earlier rule's ultracode and effort are
// gone even when the task named its own launcher, model or tier (no preset is chosen then), and only the rule that
// fits now adds anything.
func TestStartDefaultsRetryUnderAChangedRuleKeepsNothingOfTheOldRule(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*PlanTask)
	}{
		{"names nothing", func(*PlanTask) {}},
		{"names its model", func(p *PlanTask) { p.Model = "gw-own" }},
		{"names its tier", func(p *PlanTask) { p.Tier = "quick" }},
		{"names its launcher", func(p *PlanTask) { p.Launcher = "gateway" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := defaultsHub(t)
			first := true
			stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
				if first {
					return fullDefaults("deep"), true // ultracode on, effort max, a goal, preset deep
				}
				return StartDefaults{PlanFirst: true}, true // a different rule: only the self-planning prompt
			})
			task := defaultsTask()
			c.edit(&task)
			if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
				t.Fatal(err)
			}
			if st := startsOf(f)[0]; st["ultracode"] != true || st["effort"] != "max" {
				t.Fatalf("the first rule did not apply: %v", st)
			}
			first = false
			if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
				t.Fatal(err)
			}
			st := startsOf(f)[1]
			if st["ultracode"] != nil || st["effort"] == "max" || st["goal"] != nil || st["model"] == "gw-deep" {
				t.Fatalf("the retry kept the old rule's values: %v", st)
			}
			if !strings.Contains(st["prompt"].(string), prompts.RenderPlanFirstTaskConstraint()) {
				t.Fatalf("the rule that fits now did not apply: %v", st["prompt"])
			}
			if task.Ultracode != nil || task.Effort == "max" {
				t.Fatalf("the task kept the old rule's values: ultracode %v effort %q", task.Ultracode, task.Effort)
			}
		})
	}
}

// A policy's values are for the start they were applied to: the plan keeps only what the task named, so a start that
// reads the task back from the store (a remedy, a queued start, a retry from the card, a manager restart) does not take
// an expired policy's ultracode, effort, model or preset for the task's own, and the card does not show them as asked.
func TestStartDefaultsNotStoredWithTheTask(t *testing.T) {
	s, f := defaultsHub(t)
	ctx := context.Background()
	policy := true
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return fullDefaults("deep"), policy })
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Fix the build","prompt":"fix the build",
		"machine":"box","cwd":"/Users/me/code/app","why":"the repo is on box","permission":"bypassPermissions",
		"done_when":["CI is green","the fix is merged"],"expect_minutes":30}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planID(t, res)
	if _, err := s.startPlan(ctx, id, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start", func() bool { return len(startsOf(f)) == 1 })
	if st := startsOf(f)[0]; st["ultracode"] != true || st["effort"] != "max" || st["model"] != "gw-deep" {
		t.Fatalf("the policy did not apply: %v", st)
	}
	todoID := todoIDOfPlan(t, s, id)
	waitFor(t, "the agent linked", func() bool { return len(todoNow(s, todoID).Agents) == 1 })
	waitFor(t, "the start to finish its bookkeeping", func() bool { return s.taskStarting.Load() == 0 })
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	stored := p.Tasks[0]
	if stored.Ultracode != nil || stored.Effort == "max" || stored.Model == "gw-deep" || stored.Tier == "deep" {
		t.Fatalf("the policy's values were stored with the task: ultracode %v effort %q model %q tier %q",
			stored.Ultracode, stored.Effort, stored.Model, stored.Tier)
	}
	if !strings.Contains(stored.Why, "Example") {
		t.Fatalf("the task's why lost the policy's note: %q", stored.Why)
	}
	// The policy has expired; the task read back from the store starts as it would without the feature.
	policy = false
	if _, _, err := s.startTaskAgent(ctx, &stored, true); err != nil {
		t.Fatal(err)
	}
	st := startsOf(f)[1]
	if st["ultracode"] != nil || st["effort"] == "max" || st["model"] == "gw-deep" || st["goal"] != nil {
		t.Fatalf("an expired policy still decided the start: %v", st)
	}
	if st["launcher"] != "gateway" || st["model"] != "gw-model" || st["effort"] != "high" {
		t.Fatalf("the ordinary default launch changed: %v", st)
	}
}

// What the task named itself stays stored as it was: a task's own ultracode off and effort survive a policy-filled
// start in the plan, and a task's own ultracode on stays on.
func TestStartDefaultsStoredTaskKeepsItsOwnValues(t *testing.T) {
	for _, c := range []struct {
		name      string
		ultracode *bool
		effort    string
	}{
		{"its own ultracode off and effort", func() *bool { b := false; return &b }(), "low"},
		{"its own ultracode on", func() *bool { b := true; return &b }(), ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := defaultsHub(t)
			stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return fullDefaults("deep"), true })
			task := defaultsTask()
			task.Ultracode, task.Effort = c.ultracode, c.effort
			if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
				t.Fatal(err)
			}
			if len(startsOf(f)) != 1 {
				t.Fatalf("starts: %v", startsOf(f))
			}
			kept := task.storedLaunch()
			if (kept.ultracode == nil) != (c.ultracode == nil) || kept.ultracode != nil && *kept.ultracode != *c.ultracode ||
				kept.effort != c.effort || kept.tier != "" || kept.model != "" || kept.launcher != "" {
				t.Fatalf("stored launch = %+v, want the task's own (ultracode %v, effort %q)", kept, c.ultracode, c.effort)
			}
		})
	}
}

// A policy's preset can change which agent runs, and with it the permission mode its launcher sets (Claude Code runs
// with bypassPermissions): through a real plan, the stored task keeps its own mode, and the task read back from the
// store after the policy expired runs its own agent with it, not with the mode the policy's agent set.
func TestStartDefaultsPermissionOfThePolicysAgentNotStored(t *testing.T) {
	s, f := defaultsHub(t)
	ctx := context.Background()
	f.zshrc["box"] = append(f.zshrc["box"], proto.Launcher{Name: "cx", Source: "zshrc", Agent: "codex"})
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.cx"}) // the task's own default runs Codex
	policy := true
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
		return StartDefaults{Presets: []PresetChoice{{Name: "plain"}}, By: "Example"}, policy // a Claude Code preset
	})
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Fix the build","prompt":"fix the build",
		"machine":"box","cwd":"/Users/me/code/app","why":"the repo is on box","permission":"acceptEdits",
		"done_when":["CI is green"],"expect_minutes":30}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planID(t, res)
	if p, err := s.loadPlan(ctx, id); err != nil || p.Tasks[0].Permission != "acceptEdits" {
		t.Fatalf("the plan does not keep the task's mode: %+v %v", p, err)
	}
	if _, err := s.startPlan(ctx, id, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start", func() bool { return len(startsOf(f)) == 1 })
	if st := startsOf(f)[0]; st["launcher"] != "claude" || st["permission_mode"] != "bypassPermissions" {
		t.Fatalf("the policy's Claude Code preset did not run: %v", st)
	}
	todoID := todoIDOfPlan(t, s, id)
	waitFor(t, "the agent linked", func() bool { return len(todoNow(s, todoID).Agents) == 1 })
	waitFor(t, "the start to finish its bookkeeping", func() bool { return s.taskStarting.Load() == 0 })
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	stored := p.Tasks[0]
	if !strings.Contains(stored.Why, "Example") {
		t.Fatalf("the start did not write the task back (no policy note in its why): %q", stored.Why)
	}
	if stored.Permission != "acceptEdits" || stored.Launcher == "claude" || stored.Tier == "plain" || stored.Model == "claude-plain" {
		t.Fatalf("the policy's agent was stored with the task: permission %q launcher %q tier %q model %q",
			stored.Permission, stored.Launcher, stored.Tier, stored.Model)
	}
	policy = false
	if _, _, err := s.startTaskAgent(ctx, &stored, true); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[1]; st["launcher"] != "cx" || st["permission_mode"] != "acceptEdits" {
		t.Fatalf("without the policy the task did not start as its own: %v", st)
	}
}

// A rule that fits but fills nothing in (here: a goal for a task without criteria) leaves the start as it is without
// the feature, and the launch the plan keeps is the same as without the feature: the task's own, which names nothing,
// not the launcher and model Model configuration › Agents resolved for this try (#12618).
func TestStartDefaultsARuleThatFillsNothingStoresAsWithout(t *testing.T) {
	stored := func(rule bool) startPick {
		t.Helper()
		s, _ := defaultsHub(t)
		if rule {
			stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return StartDefaults{Goal: GoalFromDoneWhen}, true })
		}
		task := defaultsTask()
		task.DoneWhen = nil
		if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
			t.Fatal(err)
		}
		if task.startFilled.any() {
			t.Fatalf("the rule filled something in: %+v", task.startFilled)
		}
		return task.storedLaunch()
	}
	with, without := stored(true), stored(false)
	if !reflect.DeepEqual(with, without) || with.launcher != "" || with.model != "" || with.effort != "" {
		t.Fatalf("with a rule that fills nothing: %+v\nwithout the feature: %+v", with, without)
	}
}

// codexStart starts a task on a fresh defaultsHub (which also has the Codex launcher cx) with this launcher and these
// criteria, after policy (nil: none stored) set up the start-defaults policy, and returns what session.start carried.
func codexStart(t *testing.T, launcher string, doneWhen []string, policy func(*Service)) map[string]any {
	t.Helper()
	starts, _ := codexStarts(t, launcher, doneWhen, func(s *Service, _ *fakeHub) {
		if policy != nil {
			policy(s)
		}
	})
	return starts[0]
}

// codexStarts is codexStart with setup given the fake hub too; it returns every session.start sent (a refused one
// included) and the task as the start left it.
func codexStarts(t *testing.T, launcher string, doneWhen []string, setup func(*Service, *fakeHub)) ([]map[string]any, PlanTask) {
	t.Helper()
	s, f := defaultsHub(t)
	f.zshrc["box"] = append(f.zshrc["box"], proto.Launcher{Name: "cx", Source: "zshrc", Agent: "codex"},
		proto.Launcher{Name: "codex-app", Source: "codex_app", Agent: "codex"})
	if setup != nil {
		setup(s, f)
	}
	task := defaultsTask()
	task.Launcher, task.DoneWhen = launcher, doneWhen
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	return startsOf(f), task
}

// With no start-defaults rule deciding the goal, a new Codex task Agent whose task has criteria starts with them as its
// goal (Codex has no Stop hook; the goal keeps it going). Without criteria it gets none, and Claude Code keeps starting
// without one, as before.
func TestStartDefaultsCodexGoalByDefault(t *testing.T) {
	criteria := []string{"CI is green", "the fix is merged"}
	want := prompts.RenderTaskGoal(criteria)
	if st := codexStart(t, "cx", criteria, nil); st["goal"] != want || st["launcher"] != "cx" {
		t.Fatalf("codex, no policy: %v", st)
	}
	// A stub that finds no rule, and a rule that fits but says nothing about the goal: the default still applies.
	none := func(s *Service) {
		stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return StartDefaults{}, false })
	}
	if st := codexStart(t, "cx", criteria, none); st["goal"] != want {
		t.Fatalf("codex, no rule fits: %v", st)
	}
	silent := func(s *Service) {
		stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return StartDefaults{Effort: "max", By: "Example"}, true })
	}
	if st := codexStart(t, "cx", criteria, silent); st["goal"] != want {
		t.Fatalf("codex, a rule silent on the goal: %v", st)
	}
	if st := codexStart(t, "cx", nil, nil); st["goal"] != nil {
		t.Fatalf("codex without criteria: %v", st)
	}
	if st := codexStart(t, "claude", criteria, nil); st["goal"] != nil || st["launcher"] != "claude" {
		t.Fatalf("claude code, no policy: %v", st)
	}
	if st := codexStart(t, "claude", criteria, silent); st["goal"] != nil {
		t.Fatalf("claude code, a rule silent on the goal: %v", st)
	}
}

// The owner turns the default off with a rule saying goal "off" (through the route, as stored), and clearing the policy
// brings it back. A rule asking for done_when still sets the goal, on Claude Code too; an unknown goal value is a 400.
func TestStartDefaultsGoalOffAndExplicitRule(t *testing.T) {
	criteria := []string{"CI is green"}
	want := prompts.RenderTaskGoal(criteria)
	put := func(body string) func(*Service) {
		return func(s *Service) {
			t.Helper()
			if code, res := putStartDefaults(t, s, roleFull, "", body); code != http.StatusOK {
				t.Fatalf("put %s: %d %s", body, code, res)
			}
		}
	}
	off := put(`{"start_defaults":{"rules":[{"when":{},"then":{"goal":"off"}}]}}`)
	if st := codexStart(t, "cx", criteria, off); st["goal"] != nil {
		t.Fatalf("codex with goal off: %v", st)
	}
	if st := codexStart(t, "claude", criteria, off); st["goal"] != nil {
		t.Fatalf("claude code with goal off: %v", st)
	}
	// Off only where its rule matches: a read-only rule leaves this writable task to the default.
	readonlyOff := put(`{"start_defaults":{"rules":[{"when":{"readonly":true},"then":{"goal":"off"}}]}}`)
	if st := codexStart(t, "cx", criteria, readonlyOff); st["goal"] != want {
		t.Fatalf("codex, an off rule that does not match: %v", st)
	}
	cleared := func(s *Service) {
		off(s)
		put(`{"start_defaults":{"rules":[]}}`)(s)
	}
	if st := codexStart(t, "cx", criteria, cleared); st["goal"] != want {
		t.Fatalf("codex after the policy was cleared: %v", st)
	}
	asked := put(`{"start_defaults":{"rules":[{"when":{},"then":{"goal":"done_when"}}]}}`)
	if st := codexStart(t, "cx", criteria, asked); st["goal"] != want {
		t.Fatalf("codex with goal done_when: %v", st)
	}
	if st := codexStart(t, "claude", criteria, asked); st["goal"] != want {
		t.Fatalf("claude code with goal done_when: %v", st)
	}
	s, _ := defaultsHub(t)
	for _, v := range []string{"none", "on", "OFF"} {
		if code, body := putStartDefaults(t, s, roleFull, "",
			`{"start_defaults":{"rules":[{"when":{},"then":{"goal":"`+v+`"}}]}}`); code != http.StatusBadRequest {
			t.Fatalf("goal %q: %d %s", v, code, body)
		}
	}
}

// Where a start's goal comes from, case by case: a resume never takes one (it would replace the thread's own), nor a
// task without criteria or another agent; a rule decides when it names a goal; else Codex gets done_when by default.
func TestStartGoalSource(t *testing.T) {
	asked, off, silent := StartDefaults{Goal: GoalFromDoneWhen}, StartDefaults{Goal: GoalOff}, StartDefaults{Effort: "max"}
	for _, c := range []struct {
		name         string
		d            StartDefaults
		ruled        bool
		agent        string
		resume       bool
		doneWhen     int
		goal, byDflt bool
	}{
		{"codex default", StartDefaults{}, false, "codex", false, 2, true, true},
		{"codex, rule silent", silent, true, "codex", false, 2, true, true},
		{"codex, no criteria", StartDefaults{}, false, "codex", false, 0, false, false},
		{"codex resume", StartDefaults{}, false, "codex", true, 2, false, false},
		{"codex resume, rule asks", asked, true, "codex", true, 2, false, false},
		{"codex, rule off", off, true, "codex", false, 2, false, false},
		{"codex, rule asks", asked, true, "codex", false, 2, true, false},
		{"codex, an unmatched rule's goal does not count", off, false, "codex", false, 2, true, true},
		{"claude, no rule", StartDefaults{}, false, "claude", false, 2, false, false},
		{"claude, rule silent", silent, true, "claude", false, 2, false, false},
		{"claude, rule asks", asked, true, "claude", false, 2, true, false},
		{"claude resume, rule asks", asked, true, "claude", true, 2, false, false},
		{"another agent, rule asks", asked, true, "omp", false, 2, false, false},
	} {
		goal, byDefault := startGoalSource(c.d, c.ruled, c.agent, c.resume, c.doneWhen)
		if goal != c.goal || byDefault != c.byDflt {
			t.Errorf("%s: goal %v by default %v, want %v %v", c.name, goal, byDefault, c.goal, c.byDflt)
		}
	}
}

// The goal is decided by the first fitting rule that names one, whichever rule supplies the rest: an earlier fitting
// rule silent on the goal keeps its effort and presets, and a later goal "off" (or done_when) still applies. A rule
// that does not fit decides nothing, and the first fitting rule that names a goal wins over later ones.
func TestStartDefaultsGoalFromFirstFittingRuleThatNamesIt(t *testing.T) {
	yes, no := true, false
	p := &StartDefaultsPolicy{By: "owner", Rules: []StartDefaultsRule{
		{When: StartDefaultsMatch{Readonly: &yes}, Then: StartDefaultsThen{Goal: GoalFromDoneWhen}},
		{When: StartDefaultsMatch{Role: "worker"}, Then: StartDefaultsThen{Effort: "max", Presets: []StartDefaultsPreset{{Name: "deep"}}}},
		{When: StartDefaultsMatch{}, Then: StartDefaultsThen{Goal: GoalOff, Effort: "low"}},
		{When: StartDefaultsMatch{}, Then: StartDefaultsThen{Goal: GoalFromDoneWhen}},
	}}
	d, ok := matchStartDefaults(p, StartFacts{Role: "worker", Readonly: no})
	if !ok || d.Effort != "max" || len(d.Presets) != 1 || d.Presets[0].Name != "deep" || d.Goal != GoalOff {
		t.Fatalf("writable worker: %+v %v", d, ok)
	}
	if d, ok := matchStartDefaults(p, StartFacts{Role: "worker", Readonly: yes}); !ok || d.Goal != GoalFromDoneWhen || d.Effort != "" {
		t.Fatalf("read-only worker: %+v %v", d, ok)
	}
	if d, ok := matchStartDefaults(p, StartFacts{Role: roleLeader}); !ok || d.Goal != GoalOff || d.Effort != "low" {
		t.Fatalf("leader: %+v %v", d, ok)
	}
	silent := &StartDefaultsPolicy{Rules: []StartDefaultsRule{{Then: StartDefaultsThen{Effort: "max"}}}}
	if d, ok := matchStartDefaults(silent, StartFacts{Role: "worker"}); !ok || d.Goal != "" {
		t.Fatalf("no rule names the goal: %+v %v", d, ok)
	}
	// Through the route and a real start: an earlier rule silent on the goal no longer hides a later off.
	policy := func(s *Service) {
		body := `{"start_defaults":{"rules":[{"when":{"role":"worker"},"then":{"plan_first":true}},` +
			`{"when":{},"then":{"goal":"off"}}]}}`
		if code, res := putStartDefaults(t, s, roleFull, "", body); code != http.StatusOK {
			t.Fatalf("put: %d %s", code, res)
		}
	}
	st := codexStart(t, "cx", []string{"CI is green"}, policy)
	if st["goal"] != nil {
		t.Fatalf("codex with a later goal off: %v", st)
	}
	if prompt, _ := st["prompt"].(string); !strings.Contains(prompt, prompts.RenderPlanFirstTaskConstraint()) {
		t.Fatalf("the earlier rule's plan_first was lost: %q", prompt)
	}
}

// The built-in default goal is the owner's only when a rule asked for it: a node that refuses it starts the Agent
// again without it, and the task's why says nothing (it is only logged); where the hub already knows the start cannot
// take a goal (the Codex app's launcher), the default is not sent at all, so there is one start and no note. A rule's
// goal on the same launcher still goes, and its refusal still reaches the why.
func TestStartDefaultsRefusedDefaultGoalLeavesNoNote(t *testing.T) {
	criteria := []string{"CI is green"}
	refuse := func(s *Service, f *fakeHub) {
		f.refuseGoal = &rpc.Error{Code: proto.CodeStartGoalUnsupported, Message: "screenless host"}
	}
	starts, task := codexStarts(t, "cx", criteria, refuse)
	if len(starts) != 2 || starts[0]["goal"] == nil || starts[1]["goal"] != nil {
		t.Fatalf("starts = %v", starts)
	}
	if strings.Contains(task.Why, "screenless host") {
		t.Fatalf("a refused default goal reached the why: %q", task.Why)
	}
	starts, task = codexStarts(t, "codex-app", criteria, refuse)
	if len(starts) != 1 || starts[0]["goal"] != nil || starts[0]["launcher"] != "codex-app" {
		t.Fatalf("codex-app starts = %v", starts)
	}
	if task.Why != "" {
		t.Fatalf("codex-app why = %q", task.Why)
	}
	asked := func(s *Service, f *fakeHub) {
		refuse(s, f)
		stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return StartDefaults{Goal: GoalFromDoneWhen}, true })
	}
	starts, task = codexStarts(t, "codex-app", criteria, asked)
	if len(starts) != 2 || starts[0]["goal"] == nil || starts[1]["goal"] != nil || !strings.Contains(task.Why, "screenless host") {
		t.Fatalf("a rule's goal: starts = %v, why = %q", starts, task.Why)
	}
}

// What the hub already knows refuses a goal: a start in an engine runtime (the machine's own or the one asked for) and
// a desktop app's launcher; a terminal launcher is left to the node.
func TestDefaultGoalRefusedOn(t *testing.T) {
	term, paseo := Device{ID: "box"}, Device{ID: "box", Engine: proto.EnginePaseo}
	for _, c := range []struct {
		name   string
		m      Device
		params map[string]any
		agent  startAgent
		want   bool
	}{
		{"terminal codex", term, map[string]any{}, startAgent{agent: "codex", source: "zshrc"}, false},
		{"managed launcher", term, map[string]any{}, startAgent{agent: "codex", source: "persona"}, false},
		{"unknown source", term, map[string]any{}, startAgent{agent: "codex"}, false},
		{"codex app", term, map[string]any{}, startAgent{agent: "codex", source: "codex_app"}, true},
		{"claude desktop", term, map[string]any{}, startAgent{agent: "claude", source: "claude_desktop"}, true},
		{"a runtime's launcher", term, map[string]any{}, startAgent{agent: "codex", source: proto.EnginePaseo}, true},
		{"a runtime machine", paseo, map[string]any{}, startAgent{agent: "codex", source: "zshrc"}, true},
		{"a runtime asked for", term, map[string]any{"host": proto.EngineCindy}, startAgent{agent: "codex"}, true},
	} {
		if got := defaultGoalRefusedOn(c.m, c.params, c.agent); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCodexTerminalFallbackPublishedBeforeStart(t *testing.T) {
	ctx := context.Background()
	s, f := defaultsHub(t)
	f.zshrc["box"] = append(f.zshrc["box"], proto.Launcher{Name: "cx", Source: "zshrc", Agent: "codex"})
	task := defaultsTask()
	task.Launcher, task.Effort, task.TodoID = "cx", "high", "todo_effort"
	putTodo(s, Todo{ID: task.TodoID, Origin: "owner", Supervision: &Supervision{Supervisor: "owner"}})

	var checked bool
	f.onStart = func(f *fakeHub, params map[string]any) {
		if params["codex_effort_fallback"] == proto.CodexFallbackNotify {
			f.failStart = &rpc.Error{Code: proto.CodeCodexEffortTerminal}
			return
		}
		f.failStart = nil
		// The fake node holds its mutex while invoking onStart.
		if params["codex_effort_fallback"] != proto.CodexFallbackAnnounced || !slices.Contains(f.events, "assistant_feed") {
			t.Error("missing pre-start feed event")
		}

		checked = true
	}
	if _, _, err := s.startTaskAgent(ctx, &task, false); err != nil {
		t.Fatal(err)
	}
	if !checked || len(startsOf(f)) != 2 {
		t.Fatal("expected one preflight refusal followed by the announced start")
	}
}

func TestAgentStartCodexFallbackKeepsPlacement(t *testing.T) {
	s, f := launcherFitHub(t)
	f.zshrc["box"] = append(f.zshrc["box"], proto.Launcher{Name: "cx", Source: "zshrc", Agent: "codex"})
	f.onStart = func(f *fakeHub, params map[string]any) {
		f.failStart = nil
		if params["codex_effort_fallback"] == proto.CodexFallbackNotify {
			f.failStart = &rpc.Error{Code: proto.CodeCodexEffortTerminal}
		}
	}
	_, err := s.runTool(t.Context(), "server", "owner", "agent_start", json.RawMessage(
		`{"machine":"box","cwd":"/Users/me/code","launcher":"cx","effort":"high","prompt":"Check the build"}`))
	if err != nil {
		t.Fatal(err)
	}
	if starts := startsOf(f); len(starts) != 2 || starts[1]["codex_effort_fallback"] != proto.CodexFallbackAnnounced {
		t.Fatalf("expected the announced retry, got %v", starts)
	}
	task := onlyTodo(s)
	p := onlyStoredPlacement(t, s.store().(settingsPrefixStore))
	if len(task.Agents) != 1 || p.Mode != "enforce" || p.Choice != "box" || p.TaskID != task.ID || p.SessionID == "" || p.SessionID != task.Agents[0].SessionID {
		t.Fatalf("placement %+v for task %s, agents %+v", p, task.ID, task.Agents)
	}
}

func TestCodexFallbackNoticeKeepsAgentResult(t *testing.T) {
	ctx := t.Context()
	s, f := newTaskService(t)
	at := time.Now().UTC().Add(-time.Minute)
	todo := Todo{ID: "todo_effort", Origin: "task_owner", CreatedAt: at, UpdatedAt: at,
		Agents:      []TodoAgent{{MachineID: "box", SessionID: "worker"}},
		Supervision: &Supervision{Supervisor: "task_supervisor"}}
	putTodo(s, todo)
	params := map[string]any{"effort": "high"}
	calls := 0
	err := s.startWithCodexRouteNotice(ctx, todo.ID, "codex", params, func() error {
		calls++
		if calls == 1 {
			return &rpc.Error{Code: proto.CodeCodexEffortTerminal}
		}
		for _, conversation := range []string{todo.Origin, todo.Supervision.Supervisor} {
			items, err := s.rawFeed(ctx, conversation, time.Time{})
			if err != nil {
				return err
			}
			cards, err := s.taskCards(ctx, items, nil)
			if err != nil {
				return err
			}
			var notice noticeBody
			if len(items) != 1 || items[0].Kind != feedNotice || items[0].Ref != todo.ID ||
				json.Unmarshal(items[0].Body, &notice) != nil || !strings.Contains(notice.Text, "Codex") || len(cards) != 0 {
				t.Errorf("%s: expected a system notice and no report card, got items %+v and cards %+v", conversation, items, cards)
			}
		}
		return nil
	})
	if calls != 2 || err != nil {
		t.Fatalf("starts=%d, err=%v", calls, err)
	}
	if got, _ := s.todoNow(todo.ID); got.Supervision.Progress != nil {
		t.Errorf("startup notice became Agent progress: %+v", got.Supervision.Progress)
	}

	const result = "The export now includes all rows."
	replyAt := at.Add(20 * time.Second)
	f.sessions["box"] = []proto.Session{{MachineID: "box", SessionID: "worker", State: proto.StateUnread, LastText: result, UpdatedAt: replyAt}}
	assertSnapshot := func(wantReport string) {
		t.Helper()
		rows := s.snapshotTasks(ctx, "")
		if len(rows) != 1 || rows[0].Last != result {
			t.Fatalf("snapshot did not show the Agent's last reply: %+v", rows)
		}
		report := rows[0].Reported
		if wantReport == "" && report != nil || wantReport != "" && (report == nil || report.Text != wantReport) {
			t.Errorf("snapshot report %+v, want %q", report, wantReport)
		}
	}
	assertSnapshot("")

	// During work a real progress report is still useful. After an accepted round
	// reaches the owner's verification, an older progress report yields to the final reply.
	const progress = "Checking the export."
	f.sessions["box"][0].State = proto.StateWorking
	s.noteProgress(ctx, todo.ID, TaskProgress{Kind: upReportProgress, Text: progress, At: at.Add(10 * time.Second)})
	assertSnapshot(progress)
	f.sessions["box"][0].State = proto.StateUnread
	todo, _ = s.todoNow(todo.ID)
	todo.Supervision.State = reviewUserCheck
	todo.Supervision.Reviews = []TaskReview{{At: replyAt, Verdict: reviewAccepted, UserCheck: true}}
	putTodo(s, todo)
	assertSnapshot("")

	// A real progress report that arrives after the session's last reply is not hidden by the stale text.
	const laterProgress = "Rechecking the export checksums."
	s.noteProgress(ctx, todo.ID, TaskProgress{Kind: upReportProgress, Text: laterProgress, At: replyAt.Add(15 * time.Second)})
	assertSnapshot(laterProgress)

	// Once a newer session reply lands after that progress report, the reply wins again.
	f.sessions["box"][0].UpdatedAt = replyAt.Add(30 * time.Second)
	assertSnapshot("")

	const conclusion = "Verified the export against the source."
	s.noteProgress(ctx, todo.ID, TaskProgress{Kind: upReportDone, Text: conclusion, At: replyAt.Add(40 * time.Second)})
	assertSnapshot(conclusion)
}

func TestLegacyCodexFallbackProgressIgnored(t *testing.T) {
	for _, lang := range i18n.Langs {
		t.Run(string(lang), func(t *testing.T) {
			ctx := t.Context()
			s, f := newTaskService(t)
			at := time.Now().UTC().Add(-time.Minute)
			text := i18n.T(lang, "assistant-tasks.codex_effort_terminal")
			legacy := Todo{
				ID: "todo_legacy_progress", Origin: "owner", CreatedAt: at, UpdatedAt: at,
				Agents: []TodoAgent{{MachineID: "box", SessionID: "worker"}},
				Supervision: &Supervision{
					Supervisor: "owner",
					Progress:   &TaskProgress{Kind: upReportProgress, Text: text, At: at.Add(30 * time.Second)},
				},
			}
			// Even while working with empty LastText and a newer progress timestamp, the legacy startup notice
			// must not surface in todoNow, formatTodo, or snapshotTasks.
			if got := formatTodo(legacy); got.Supervision != nil && got.Supervision.Progress != nil {
				t.Fatalf("formatTodo kept legacy startup progress: %+v", got.Supervision.Progress)
			}
			putTodo(s, legacy)
			f.sessions["box"] = []proto.Session{{MachineID: "box", SessionID: "worker", State: proto.StateWorking, UpdatedAt: at}}
			if got, ok := s.todoNow(legacy.ID); !ok || got.Supervision == nil || got.Supervision.Progress != nil {
				t.Fatalf("todoNow kept legacy startup progress: ok=%v sup=%+v", ok, got.Supervision)
			}
			rows := s.snapshotTasks(ctx, "")
			if len(rows) != 1 || rows[0].Reported != nil {
				t.Fatalf("snapshotTasks surfaced legacy startup progress: %+v", rows)
			}
			if s.noteProgress(ctx, legacy.ID, TaskProgress{Kind: upReportProgress, Text: text, At: at.Add(40 * time.Second)}) {
				t.Fatal("noteProgress accepted legacy startup notice")
			}
			if got, _ := s.todoNow(legacy.ID); got.Supervision.Progress != nil {
				t.Fatalf("noteProgress stored legacy startup notice: %+v", got.Supervision.Progress)
			}
		})
	}
}

func TestLegacyCodexFallbackReportReadsAsNotice(t *testing.T) {
	for _, lang := range i18n.Langs {
		t.Run(string(lang), func(t *testing.T) {
			ctx := t.Context()
			s, _ := newTaskService(t)
			todo := Todo{ID: "todo_effort", Origin: "owner"}
			putTodo(s, todo)
			text := i18n.T(lang, "assistant-tasks.codex_effort_terminal")
			legacy, err := s.appendFeed(ctx, todo.Origin, feedReport, todo.ID, reportBody{Kind: upReportProgress, Text: text})
			if err != nil {
				t.Fatal(err)
			}
			done, err := s.appendFeed(ctx, todo.Origin, feedReport, todo.ID, reportBody{Kind: upReportDone, Text: "Export verified."})
			if err != nil {
				t.Fatal(err)
			}
			check := func(items []FeedItem, err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
				if len(items) != 2 || items[0].ID != legacy.ID || items[0].Kind != feedNotice ||
					items[1].ID != done.ID || items[1].Kind != feedReport {
					t.Errorf("expected startup notice and real report, got %+v", items)
				}
				cards, err := s.taskCards(ctx, items, nil)
				if err != nil || len(cards) != 1 || cards[0].ReportID != done.ID {
					t.Errorf("task cards %+v, err %v", cards, err)
				}
			}
			check(s.feedItems(ctx, todo.Origin, time.Time{}))
			page, _, err := s.pagedFeed(ctx, todo.Origin, time.Time{}, "")
			check(page, err)
			timeline, err := s.todoFeed(ctx, todo.ID, false)
			if err != nil || len(timeline) != 1 || timeline[0].ID != done.ID {
				t.Errorf("task timeline %+v, err %v", timeline, err)
			}
		})
	}
}

type legacyFeedByRefStore struct {
	*memStore
	rows []db.FeedRow
}

func (f *legacyFeedByRefStore) FeedByRef(context.Context, string, []string, int) ([]db.FeedRow, error) {
	return slices.Clone(f.rows), nil
}

func TestLegacyCodexFallbackTimelineStoreFiltersNotice(t *testing.T) {
	for _, lang := range i18n.Langs {
		t.Run(string(lang), func(t *testing.T) {
			ctx := t.Context()
			at := time.Now().UTC().Add(-time.Minute)
			legacyBody, _ := json.Marshal(reportBody{Kind: upReportProgress, Text: i18n.T(lang, "assistant-tasks.codex_effort_terminal")})
			doneBody, _ := json.Marshal(reportBody{Kind: upReportDone, Text: "Export verified."})
			st := &legacyFeedByRefStore{
				memStore: &memStore{},
				rows: []db.FeedRow{
					{ID: "done", Conversation: "owner", At: at.Add(time.Second), Kind: feedReport, Ref: "todo_effort", Body: doneBody},
					{ID: "legacy", Conversation: "owner", At: at, Kind: feedReport, Body: legacyBody},
				},
			}
			s, _ := newTaskServiceOn(t, st)
			timeline, err := s.todoFeed(ctx, "todo_effort", false)
			if err != nil || len(timeline) != 1 || timeline[0].ID != "done" {
				t.Fatalf("store-backed task timeline %+v, err %v", timeline, err)
			}
		})
	}
}
