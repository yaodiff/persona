package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A new Agent's launcher on the computer it starts on (#2640, launcher_fit.go).

// launcherFitHub: Model configuration › Agents runs "gateway", which only box has (a ~/.zshrc function there); l4 has
// Claude Code and a launcher of its own; the hub machine's list can't be read.
func launcherFitHub(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.sessions["l4"], f.sessions["box"] = nil, nil
	f.devices = []Device{
		{ID: "server", Name: "server", Home: "/home/me", Online: true, Reachable: true},
		{ID: "l4", Name: "l4", Home: "/home/me", Online: true, Reachable: true, ClaudeVersion: "2.1.0"},
		{ID: "box", Name: "box", Home: "/Users/me", Online: true, Reachable: true, ClaudeVersion: "2.1.0"},
	}
	f.zshrc = map[string][]proto.Launcher{
		"l4":  {{Name: "other", Source: "zshrc"}},
		"box": {{Name: "gateway", Source: "zshrc"}},
	}
	s := newServiceOn(t, f, &profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}) // plans and settings
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.gateway", Model: "gw-model", Effort: "high"})
	return s, f
}

func planID(t *testing.T, res string) string {
	t.Helper()
	id := regexp.MustCompile(`plan_[0-9a-f]+`).FindString(res)
	if id == "" {
		t.Fatalf("no plan in %q", res)
	}
	return id
}

func scopedLauncherFitHub(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	s, f := launcherFitHub(t)
	addProvider(t, s, `{"name":"managed","base_url":"https://provider.example.com","model":"managed-model","api_key":"test-key","share":"selected","scope_machines":["box"]}`)
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.managed", Model: "managed-model", Effort: "high"})
	return s, f
}

func TestScopedManagedDefaultIsSwappedForTask(t *testing.T) {
	s, f := scopedLauncherFitHub(t)
	ctx := t.Context()
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions","prompt":"Reply with pong","machine":"l4","cwd":"~/code/app","why":"A quick check"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planID(t, res)
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if task := p.Tasks[0]; task.Machine != "l4" || task.Launcher != "claude" ||
		!strings.Contains(task.Why, "managed") || !strings.Contains(task.Why, "claude") {
		t.Fatalf("task placement = %+v, want claude on l4 with a swap note", task)
	}
	if _, err := s.startPlan(ctx, id, nil, false); err != nil {
		t.Fatal(err)
	}
	todoID := todoIDOfPlan(t, s, id)
	waitFor(t, "the swapped agent linked", func() bool { return len(todoNow(s, todoID).Agents) == 1 })
	if st := startsOf(f); len(st) != 1 || st[0]["launcher"] != "claude" ||
		!unset(st[0]["model"]) || !unset(st[0]["effort"]) || !slices.Equal(taskStartMachines(f), []string{"l4"}) {
		t.Fatalf("starts = %v on %v, want l4's claude with its own defaults", st, taskStartMachines(f))
	}
}

func TestScopedManagedExplicitStartIsRefused(t *testing.T) {
	s, f := scopedLauncherFitHub(t)
	saveTier(t, s, "strong", ModelTier{Launcher: "managed", Model: "managed-model"})
	for _, tc := range []struct {
		key, value string
	}{
		{"launcher", "managed"},
		{"tier", "strong"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			params := map[string]any{"machine": "l4", "cwd": "/home/me/code", "prompt": "Reply with pong", tc.key: tc.value}
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			if _, err := s.runTool(ctx, "server", "conv9", "agent_start", raw); err == nil || !strings.Contains(err.Error(), "managed") {
				t.Fatalf("agent_start refusal = %v", err)
			}
			if _, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "l4", Params: params}); codeOf(err) != proto.CodeLauncherOutOfScope {
				t.Fatalf("app start refusal = %v, want %s", err, proto.CodeLauncherOutOfScope)
			}
			if len(startsOf(f)) != 0 {
				t.Fatalf("a start reached the node: %v", startsOf(f))
			}
		})
	}
}

func TestScopedManagedLauncherAvailability(t *testing.T) {
	s, f := scopedLauncherFitHub(t)
	ctx := t.Context()
	saveTier(t, s, "strong", ModelTier{Candidates: []TierCandidate{{Launcher: "managed"}, {Launcher: "claude"}}})
	for _, tc := range []struct {
		name string
		list []proto.Launcher
	}{
		{"readable", []proto.Launcher{{Name: "claude", Source: "zshrc"}}},
		{"stale alias", []proto.Launcher{{Name: "managed", Source: "zshrc"}, {Name: "claude", Source: "zshrc"}}},
		{"unreadable", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f.mu.Lock()
			f.zshrc["l4"] = tc.list
			f.mu.Unlock()
			if available, reason, agent := s.availabilityOn("l4").check(ctx, "managed"); available || reason == "" || agent != "" {
				t.Errorf("l4 tier candidate = %v, %q, %q", available, reason, agent)
			}
			pick, err := s.pickTierCandidate(ctx, "strong", tierFacts{Machine: "l4"})
			if err != nil || pick.Tier.Launcher != "claude" || pick.Reason != tierReasonUnavailable {
				t.Errorf("l4 tier pick = %+v, %v", pick, err)
			}
			if names, _, err := s.healthLaunchers(ctx, "l4"); err != nil || slices.Contains(names, "managed") {
				t.Errorf("l4 health launcher list = %v, %v", names, err)
			}
			if s.launcherOn(ctx, "l4", "managed") {
				t.Error("l4 can receive a health detour for the scoped launcher")
			}
			if _, got := s.probeLauncherHealth(ctx, "l4", "managed"); got {
				t.Error("l4 returned a health probe for the out-of-scope launcher")
			}
			p := newPlacement("l4", "fixed")
			params := map[string]any{"launcher": "managed", "model": "managed-model"}
			m, _ := s.device(ctx, "l4")
			note, _, err := s.fitLauncher(context.WithValue(ctx, placementKey{}, p), m, startPick{}, params)
			if err != nil || params["launcher"] != "claude" || note == "" ||
				!slices.ContainsFunc(p.candidate("l4").Hits, func(h placementHit) bool { return h.Key == "launcher_missing" && h.Args["launcher"] == "managed" }) {
				t.Errorf("l4 default fit = %v, note %q, error %v, placement %+v", params, note, err, p.Candidates)
			}
		})
	}
	if available, reason, agent := s.availabilityOn("box").check(ctx, "managed"); !available || reason != "" || agent != "claude" {
		t.Errorf("box tier candidate = %v, %q, %q", available, reason, agent)
	}
	if names, _, err := s.healthLaunchers(ctx, "box"); err != nil || !slices.Contains(names, "managed") {
		t.Errorf("box health launcher list = %v, %v", names, err)
	}
}

// A default the computer lacks: the plan card already names the launcher that will run there (claude, with its own
// default model) and its why says so; the task starts on it, even with the default's failed start on record there.
func TestDefaultLauncherTheComputerLacksIsSwappedAndSaid(t *testing.T) {
	s, f := launcherFitHub(t)
	ctx := context.Background()
	// The first try, before this fix, failed in the terminal: a record that would hold the default there.
	s.recordHealth(ctx, "l4", "gateway", healthResult{err: "zsh:1: command not found: gateway", source: healthSourceStart, kind: errKindNotInTerminal})
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions","prompt":"Reply with pong","machine":"l4","cwd":"~/code/app","why":"A quick check"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planID(t, res)
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	task := p.Tasks[0]
	if task.Machine != "l4" || task.Launcher != "claude" || task.Model != "" || task.Effort != "" ||
		!strings.HasPrefix(task.Why, "A quick check (") || !strings.Contains(task.Why, "gateway") || !strings.Contains(task.Why, "claude") {
		t.Fatalf("the card does not show what runs: %+v", task)
	}
	if _, err := s.startPlan(ctx, id, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start", func() bool { return len(startsOf(f)) == 1 })
	st := startsOf(f)[0]
	if _, model := st["model"]; st["launcher"] != "claude" || model || !unset(st["effort"]) || taskStartMachines(f)[0] != "l4" {
		t.Fatalf("started with %v", st)
	}
	todoID := todoIDOfPlan(t, s, id)
	waitFor(t, "the agent linked", func() bool { return len(todoNow(s, todoID).Agents) == 1 })
	if e := todoNow(s, todoID).StartError; e != "" {
		t.Fatalf("start error: %s", e)
	}

	// agent_start says so in its answer and starts claude; on box, which has it, the default runs as configured.
	out, err := s.runTool(ctx, "server", "conv9", "agent_start", json.RawMessage(`{"machine":"l4","cwd":"/home/me/code","prompt":"Reply with pong"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Note: ") || !strings.Contains(out, "gateway") || !strings.Contains(out, "claude") {
		t.Fatalf("the answer does not say: %s", out)
	}
	if st := startsOf(f)[1]; st["launcher"] != "claude" || st["model"] != nil {
		t.Fatalf("agent_start started %v", st)
	}
	if out, err = s.runTool(ctx, "server", "conv9", "agent_start", json.RawMessage(`{"machine":"box","cwd":"/Users/me/code","prompt":"Reply with pong"}`)); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[2]; st["launcher"] != "gateway" || st["model"] != "gw-model" || st["effort"] != "high" || strings.Contains(out, "Note: ") {
		t.Fatalf("box: %v\n%s", st, out)
	}

	// New Agent in the apps with no launcher picked, and a message resuming an Agent started outside Persona.
	params, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "l4", Params: map[string]any{"cwd": "/home/me/code", "launcher": ""}})
	if err != nil || params["launcher"] != "claude" || params["model"] != nil || params["effort"] != nil {
		t.Fatalf("new agent: %v %v", params, err)
	}
	params, err = s.AgentResumeParams(ctx, AgentResumeParams{MachineID: "l4", SessionID: "outside-1", Params: map[string]any{}})
	if err != nil || params["launcher"] != "claude" || params["model"] != nil {
		t.Fatalf("resume: %v %v", params, err)
	}
	if params, _ = s.AgentResumeParams(ctx, AgentResumeParams{MachineID: "box", SessionID: "outside-2", Params: map[string]any{}}); params["launcher"] != "gateway" {
		t.Fatalf("resume on box: %v", params)
	}
}

// An explicit tier is a caller choice, not a default: where its launcher is not on the computer the start is refused
// before a tab opens and the computer's launchers are named; nothing is substituted and the preset is not cleared
// silently (#12032).
func TestExplicitTierLauncherTheComputerLacksIsRefused(t *testing.T) {
	s, f := launcherFitHub(t)
	ctx := context.Background()
	saveTier(t, s, "strong", ModelTier{Launcher: "gateway", Model: "gw-big", Effort: "max"})
	// agent_start (the direct MCP path): no agent starts, the refusal says what l4 has.
	_, err := s.runTool(ctx, "server", "conv9", "agent_start", json.RawMessage(`{"machine":"l4","cwd":"/home/me/code","prompt":"Reply with pong","tier":"strong"}`))
	if err == nil || !strings.Contains(err.Error(), "gateway") || !strings.Contains(err.Error(), "claude, other") ||
		!strings.Contains(err.Error(), "no agent was started") {
		t.Fatalf("agent_start: %v", err)
	}
	if starts := startsOf(f); len(starts) != 0 {
		t.Fatalf("a tab opened: %v", starts)
	}
	// New Agent in the apps: the same coded refusal as a launcher the computer lacks.
	_, err = s.AgentStartParams(ctx, AgentStartParams{MachineID: "l4", Params: map[string]any{"tier": "strong"}, Lang: "en"})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != CodeBadRequest || !strings.Contains(re.Message, "Launcher gateway isn’t on l4") ||
		!strings.Contains(re.Message, "claude, other") {
		t.Fatalf("new agent: %v", err)
	}
	if starts := startsOf(f); len(starts) != 0 {
		t.Fatalf("a tab opened: %v", starts)
	}
}

// An explicit tier whose launcher the computer has runs that preset unchanged: its launcher, model, effort and speed
// reach the start, with no swap note (#12032).
func TestExplicitTierWithItsLauncherPresentPreservesThePreset(t *testing.T) {
	s, f := launcherFitHub(t)
	ctx := context.Background()
	f.mu.Lock()
	f.zshrc["box"] = []proto.Launcher{{Name: "gateway", Source: "zshrc", Agent: "codex"}}
	f.mu.Unlock()
	saveTier(t, s, "strong", ModelTier{Launcher: "gateway", Model: "gw-big", Effort: "max", Speed: "fast"})
	out, err := s.runTool(ctx, "server", "conv9", "agent_start", json.RawMessage(`{"machine":"box","cwd":"/Users/me/code","prompt":"Reply with pong","tier":"strong"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Note: ") {
		t.Fatalf("a present launcher needs no swap note: %s", out)
	}
	if st := startsOf(f)[0]; st["launcher"] != "gateway" || st["model"] != "gw-big" || st["effort"] != "max" || st["speed"] != "fast" {
		t.Fatalf("the preset did not reach the start: %v", st)
	}
	// New Agent in the apps preserves the same preset.
	params, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "box", Params: map[string]any{"tier": "strong"}, Lang: "en"})
	if err != nil {
		t.Fatal(err)
	}
	if params["launcher"] != "gateway" || params["model"] != "gw-big" || params["effort"] != "max" || params["speed"] != "fast" {
		t.Fatalf("new agent lost the preset: %v", params)
	}
}

// A launcher named alongside a tier keeps the existing precedence: the caller's launcher runs when the computer has it,
// and a tier whose launcher differs gives nothing, so no new refusal is introduced (#12032).
func TestNamedLauncherKeepsPrecedenceOverATier(t *testing.T) {
	s, f := launcherFitHub(t)
	ctx := context.Background()
	saveTier(t, s, "strong", ModelTier{Launcher: "gateway", Model: "gw-big", Effort: "max"})
	// l4 has claude, not the tier's gateway: claude is named explicitly and runs, the tier contributes nothing.
	if _, err := s.runTool(ctx, "server", "conv9", "agent_start", json.RawMessage(
		`{"machine":"l4","cwd":"/home/me/code","prompt":"Reply with pong","launcher":"claude","tier":"strong"}`)); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["launcher"] != "claude" || st["model"] != nil {
		t.Fatalf("the named launcher did not win: %v", st)
	}
}

func TestTaskPlanTierLauncherTheComputerLacksIsRefused(t *testing.T) {
	s, f := launcherFitHub(t)
	ctx := context.Background()
	saveTier(t, s, "standard", ModelTier{Launcher: "gateway", Model: "gw-big", Effort: "max"})
	_, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions","prompt":"Reply with pong","machine":"l4","cwd":"~/code/app","tier":"standard"}]}`))
	if err == nil || !strings.Contains(err.Error(), "task 1: ") || !strings.Contains(err.Error(), "gateway") ||
		!strings.Contains(err.Error(), "claude, other") {
		t.Fatalf("task_plan: %v", err)
	}
	if starts := startsOf(f); len(starts) != 0 {
		t.Fatalf("a tab opened: %v", starts)
	}
}

// A planned tier whose launcher the destination has is kept on the card and reaches the start with its whole preset.
func TestTaskPlanTierWithItsLauncherPresentKeepsThePreset(t *testing.T) {
	s, f := launcherFitHub(t)
	ctx := context.Background()
	f.mu.Lock()
	f.zshrc["box"] = []proto.Launcher{{Name: "gateway", Source: "zshrc", Agent: "codex"}}
	f.mu.Unlock()
	saveTier(t, s, "standard", ModelTier{Launcher: "gateway", Model: "gw-big", Effort: "max", Speed: "fast"})
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions","prompt":"Reply with pong","machine":"box","cwd":"~/code/app","tier":"standard"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planID(t, res)
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if task := p.Tasks[0]; task.Tier != "standard" || task.Launcher != "" {
		t.Fatalf("the card did not keep the explicit tier: %+v", task)
	}
	if _, err := s.startPlan(ctx, id, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the preset's start", func() bool { return len(startsOf(f)) == 1 })
	if st := startsOf(f)[0]; st["launcher"] != "gateway" || st["model"] != "gw-big" || st["effort"] != "max" || st["speed"] != "fast" {
		t.Fatalf("the preset did not reach the start: %v", st)
	}
}

// Refusal at dispatch: a tier whose launcher was present while the card was made but is gone before session.start
// starts nothing and records the refusal on the to-do (#12032).
func TestExplicitTierRefusedAtDispatchWhenLauncherDisappears(t *testing.T) {
	s, f := launcherFitHub(t)
	ctx := context.Background()
	saveTier(t, s, "standard", ModelTier{Launcher: "gateway", Model: "gw-big", Effort: "max"})
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions","prompt":"Reply with pong","machine":"box","cwd":"~/code/app","tier":"standard"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planID(t, res)
	f.mu.Lock()
	f.zshrc["box"] = []proto.Launcher{{Name: "claude", Source: "zshrc"}}
	f.mu.Unlock()
	s.invalidateLauncherListCache("box")
	if _, err := s.startPlan(ctx, id, nil, false); err != nil {
		t.Fatal(err)
	}
	todoID := todoIDOfPlan(t, s, id)
	waitFor(t, "the refusal", func() bool { return todoNow(s, todoID).StartError != "" })
	if e := todoNow(s, todoID).StartError; !strings.Contains(e, "gateway") || len(startsOf(f)) != 0 {
		t.Fatalf("start error %q, starts %v", e, startsOf(f))
	}
}

// A tier on a computer whose launcher list cannot be read is not second-guessed: the start goes out with the preset,
// as a named launcher already does on an unreadable list (#12032).
func TestExplicitTierOnAnUnreadableLauncherListIsNotRefused(t *testing.T) {
	s, f := launcherFitHub(t)
	ctx := context.Background()
	saveTier(t, s, "standard", ModelTier{Launcher: "gateway", Model: "gw-big", Effort: "max"})
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions","prompt":"Reply with pong","machine":"server","cwd":"~/code/app","why":"A quick check","tier":"standard"}]}`))
	if err != nil {
		t.Fatalf("refused on a guess: %v", err)
	}
	id := planID(t, res)
	if _, err := s.startPlan(ctx, id, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start", func() bool { return len(startsOf(f)) == 1 })
	if st := startsOf(f)[0]; st["launcher"] != "gateway" || st["model"] != "gw-big" || st["effort"] != "max" {
		t.Fatalf("the preset did not reach the start: %v", st)
	}
}

// A tier the start-defaults policy selected for this start is a choice too: a preset whose launcher the computer lacks
// at fit time is refused rather than substituted (#12032). The candidate source says the preset can start (its view
// when the rule chose it), while the computer's live launcher list no longer has gateway: a stale read or a launcher
// removed between selection and the fit.
func TestStartDefaultsPresetTierMissingLauncherIsRefused(t *testing.T) {
	s, f := defaultsHub(t)
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) { return fullDefaults("deep"), true })
	s.startCandidates = stubCandidateSource{rows: map[string]startCandidate{
		"deep": {Name: "deep", Launcher: "gateway", Agent: "codex", Available: true},
	}}
	task := defaultsTask()
	task.Machine, task.Cwd = "l4", "/home/me/code/app"
	_, _, err := s.startTaskAgent(context.Background(), &task, false)
	if err == nil || !strings.Contains(err.Error(), "gateway") || !strings.Contains(err.Error(), "claude, other") {
		t.Fatalf("policy start: %v", err)
	}
	if starts := startsOf(f); len(starts) != 0 {
		t.Fatalf("a tab opened: %v", starts)
	}
}

// A launcher the caller named that the computer lacks starts nothing, and the refusal lists what that computer has.
func TestNamedLauncherTheComputerLacksIsRefused(t *testing.T) {
	s, f := launcherFitHub(t)
	ctx := context.Background()
	_, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions","prompt":"Reply with pong","machine":"l4","cwd":"~/code/app","launcher":"gateway"}]}`))
	if err == nil || !strings.Contains(err.Error(), "task 1: ") || !strings.Contains(err.Error(), "gateway") || !strings.Contains(err.Error(), "claude, other") {
		t.Fatalf("task_plan: %v", err)
	}
	_, err = s.runTool(ctx, "server", "conv9", "agent_start", json.RawMessage(`{"machine":"l4","cwd":"/home/me/code","prompt":"Reply with pong","launcher":"gateway"}`))
	if err == nil || !strings.Contains(err.Error(), "claude, other") || !strings.Contains(err.Error(), "no agent was started") {
		t.Fatalf("agent_start: %v", err)
	}
	if starts := startsOf(f); len(starts) != 0 {
		t.Fatalf("a tab opened: %v", starts)
	}
	_, err = s.AgentStartParams(ctx, AgentStartParams{MachineID: "l4", Params: map[string]any{"launcher": "gateway"}, Lang: "en"})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != CodeBadRequest || !strings.Contains(re.Message, "Launcher gateway isn’t on l4") || !strings.Contains(re.Message, "claude, other") {
		t.Fatalf("new agent: %v", err)
	}
	// Where it is, it runs; so do claude and a managed launcher everywhere.
	if params, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "box", Params: map[string]any{"launcher": "gateway"}}); err != nil || params["launcher"] != "gateway" {
		t.Fatalf("box: %v %v", params, err)
	}
	if params, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "l4", Params: map[string]any{"launcher": "claude"}}); err != nil || params["launcher"] != "claude" {
		t.Fatalf("claude: %v %v", params, err)
	}
	// A plan the card moves to a computer that lacks the launcher it names fails that task, without a tab.
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions","prompt":"Reply with pong","machine":"box","cwd":"~/code/app","launcher":"gateway"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planID(t, res)
	if _, err := s.startPlan(ctx, id, []PlanTask{{Machine: "l4"}}, false); err != nil {
		t.Fatal(err)
	}
	todoID := todoIDOfPlan(t, s, id)
	waitFor(t, "the refusal", func() bool { return todoNow(s, todoID).StartError != "" })
	if e := todoNow(s, todoID).StartError; !strings.Contains(e, "gateway") || len(startsOf(f)) != 0 {
		t.Fatalf("start error %q, starts %v", e, startsOf(f))
	}
}

// A computer whose list can't be read (offline, too slow, an old node) is not second-guessed: the default and a named
// launcher go out as they are.
func TestUnreadableLauncherListDoesNotBlock(t *testing.T) {
	s, f := launcherFitHub(t)
	ctx := context.Background()
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions","prompt":"Reply with pong","machine":"server","cwd":"~/code/app","why":"A quick check"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	id := planID(t, res)
	p, err := s.loadPlan(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if task := p.Tasks[0]; task.Launcher != "" || task.Why != "A quick check" {
		t.Fatalf("changed on a guess: %+v", task)
	}
	if _, err := s.startPlan(ctx, id, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start", func() bool { return len(startsOf(f)) == 1 })
	if st := startsOf(f)[0]; st["launcher"] != "gateway" || st["model"] != "gw-model" {
		t.Fatalf("started with %v", st)
	}
	if _, err := s.runTool(ctx, "server", "conv9", "agent_start", json.RawMessage(`{"machine":"server","cwd":"/home/me/code","prompt":"Reply with pong","launcher":"gateway"}`)); err != nil {
		t.Fatalf("refused on a guess: %v", err)
	}
	if st := startsOf(f)[1]; st["launcher"] != "gateway" {
		t.Fatalf("agent_start started %v", st)
	}
	if params, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "server", Params: map[string]any{"launcher": "gateway"}}); err != nil || params["launcher"] != "gateway" {
		t.Fatalf("new agent: %v %v", params, err)
	}
	// An answer that names none of the node's own launchers is no list either (a node that read its shell names
	// claude at least): l4 says nothing now.
	f.mu.Lock()
	f.zshrc["l4"] = []proto.Launcher{}
	f.mu.Unlock()
	if params, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "l4", Params: map[string]any{"launcher": "gateway"}}); err != nil || params["launcher"] != "gateway" {
		t.Fatalf("an empty answer: %v %v", params, err)
	}
	if params, err := s.AgentStartParams(ctx, AgentStartParams{MachineID: "l4", Params: map[string]any{}}); err != nil || params["launcher"] != "gateway" || params["model"] != "gw-model" {
		t.Fatalf("an empty answer, the default: %v %v", params, err)
	}
}

// A plan made while the list could not be read swaps at dispatch, and the card then shows it.
func TestDispatchSwapsWhenThePreviewCouldNotTell(t *testing.T) {
	s, f := launcherFitHub(t)
	ctx := context.Background()
	l4 := f.zshrc["l4"]
	f.mu.Lock()
	delete(f.zshrc, "l4") // no list from l4 while the plan is made
	f.mu.Unlock()
	res, err := s.toolTaskPlan(ctx, "conv9", json.RawMessage(`{"tasks":[{"title":"Ping","permission":"bypassPermissions","prompt":"Reply with pong","machine":"l4","cwd":"~/code/app"}]}`))
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
	waitFor(t, "the start", func() bool { return len(startsOf(f)) == 1 })
	if st := startsOf(f)[0]; st["launcher"] != "claude" || st["model"] != nil {
		t.Fatalf("started with %v", st)
	}
	waitFor(t, "the card", func() bool {
		p, err := s.loadPlan(ctx, id)
		return err == nil && p.Tasks[0].Launcher == "claude" && strings.Contains(p.Tasks[0].Why, "gateway")
	})
}

func TestLauncherInstead(t *testing.T) {
	runtime := proto.Launcher{Name: "dsh", Source: "dsh"}
	codex := proto.Launcher{Name: "codex", Agent: "codex", Source: "zshrc"}
	codexApp := proto.Launcher{Name: "codex-app", Agent: "codex", Source: "codex_app"}
	for _, c := range []struct {
		m    Device
		list []proto.Launcher
		want string
	}{
		{Device{ClaudeVersion: "2.1.0"}, []proto.Launcher{codex}, "claude"},
		{Device{}, []proto.Launcher{{Name: "claude", Source: "zshrc"}, codex}, "claude"},
		{Device{}, []proto.Launcher{runtime, codex}, "codex"}, // no Claude Code there: the first that takes a prompt
		{Device{}, []proto.Launcher{runtime}, "claude"},
		{Device{}, nil, "claude"},
		// the Codex App's launcher is only ever an explicit pick: a default gives way to plain codex, never to it (#10437)
		{Device{}, []proto.Launcher{codexApp, codex}, "codex"},
		{Device{}, []proto.Launcher{codexApp}, "claude"},
	} {
		if got := launcherInstead(c.m, c.list); got != c.want {
			t.Errorf("launcherInstead(%+v, %v) = %q, want %q", c.m, c.list, got, c.want)
		}
	}
	if got := strings.Join(launcherNamesOn(Device{ClaudeVersion: "2.1.0"}, []proto.Launcher{codex, {Name: "claude"}}), ", "); got != "claude, codex" {
		t.Errorf("names: %s", got)
	}
}

// unset: a start param that says nothing (absent, or "" as the node takes it).
func unset(v any) bool {
	s, _ := v.(string)
	return s == ""
}

// saveTier makes a model profile with tier name in use.
func saveTier(t *testing.T, s *Service, name string, tier ModelTier) {
	t.Helper()
	ctx := context.Background()
	ps := modelProfiles{Revision: 1, Profiles: []ModelProfile{{ID: "p", Name: "P", Tiers: map[string]ModelTier{name: tier}}}}
	if err := s.store().PutSetting(ctx, modelProfilesKey, ps); err != nil {
		t.Fatal(err)
	}
	if err := s.store().PutSetting(ctx, modelProfileStateKey, modelProfileState{Active: "p"}); err != nil {
		t.Fatal(err)
	}
}

// todoIDOfPlan is the todo the first task of started plan id became.
func todoIDOfPlan(t *testing.T, s *Service, id string) string {
	t.Helper()
	p, err := s.loadPlan(context.Background(), id)
	if err != nil || p.Tasks[0].TodoID == "" {
		t.Fatalf("plan %s: %+v %v", id, p, err)
	}
	return p.Tasks[0].TodoID
}
