package assistant

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// The two tiers Persona seeded under strength names read back under the kind of work, the old name kept as an alias
// and nothing else changed; a tier the user made, and a seeded one whose new name is taken, keep their names (#14700).
func TestSeededTierNamesMigrateWithAliases(t *testing.T) {
	s, _, st := modelService(t)
	ctx := context.Background()
	strong := ModelTier{Launcher: "claude", Model: "test-big", Effort: "high", Speed: "fast", PermissionMode: "plan", Description: "Design"}
	standard := ModelTier{Launcher: "codex", Model: "test-coding", Effort: "medium"}
	bulk := ModelTier{Launcher: "claude", Model: "test-small"}
	storeProfiles(t, st,
		ModelProfile{ID: "performance", Name: "Performance", Tiers: map[string]ModelTier{"strong": strong, "standard": standard, "bulk": bulk}},
		ModelProfile{ID: "economy", Name: "Economy", Tiers: map[string]ModelTier{"standard": standard, "dev": bulk}})
	if err := st.PutSetting(ctx, modelProfileStateKey, modelProfileState{Active: "performance"}); err != nil {
		t.Fatal(err)
	}
	ps, _ := storedTiers(t, s)
	perf, econ := ps.Profiles[0].Tiers, ps.Profiles[1].Tiers
	if len(perf) != 3 || !slices.Equal(slices.Sorted(maps.Keys(perf)), []string{"bulk", "core", "dev"}) {
		t.Fatalf("performance tiers: %+v", perf)
	}
	wantCore, wantDev := strong, standard
	wantCore.Aliases, wantDev.Aliases = []string{"strong"}, []string{"standard"}
	if !perf["core"].equal(wantCore) || !perf["dev"].equal(wantDev) || !perf["bulk"].equal(bulk) {
		t.Fatalf("migration changed what the tiers run on: %+v", perf)
	}
	if _, ok := econ["standard"]; !ok || !econ["dev"].equal(bulk) || len(econ["standard"].Aliases) != 0 {
		t.Fatalf("a seeded tier whose new name is taken was renamed: %+v", econ)
	}
	// The old names keep resolving, to the same launcher and model as before.
	for name, want := range map[string]ModelTier{"strong": strong, "core": strong, "standard": standard, "dev": standard, "bulk": bulk} {
		pick, err := s.pickTierCandidate(ctx, name, tierFacts{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !pick.Tier.equal(want) || pick.Reason != tierReasonOnly || pick.Of != 1 || pick.Candidate != 0 || len(pick.Checks) != 0 {
			t.Fatalf("%s picked %+v", name, pick)
		}
	}
	if pick, _ := s.pickTierCandidate(ctx, "strong", tierFacts{}); pick.Name != "core" {
		t.Fatalf("an alias did not resolve to its tier's own name: %+v", pick)
	}
	// The next write stores the new names; a read again changes nothing more.
	if _, err := s.saveModelProfile(ctx, nil, "performance", ProfileActor{Kind: "hub"}, func(p *ModelProfile, _ bool) error {
		p.Description = "saved"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var raw modelProfiles
	if _, err := st.Setting(ctx, modelProfilesKey, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw.Profiles[0].Tiers["core"]; !ok || !slices.Equal(raw.Profiles[0].Tiers["core"].Aliases, []string{"strong"}) {
		t.Fatalf("stored after a save: %+v", raw.Profiles[0].Tiers)
	}
}

// Candidates and aliases are checked on save: at most six candidates, each with a launcher and a valid model, effort,
// speed and permission mode; an alias is a tier name no other tier has or answers to.
func TestTierCandidatesAndAliasesValidation(t *testing.T) {
	ok := map[string]ModelTier{"dev": {Launcher: "claude", Candidates: []TierCandidate{{Launcher: "claude"}, {Launcher: "codex", Effort: "high", Speed: "fast"}},
		Aliases: []string{" standard ", "standard", "bulk"}}, "core": {Launcher: "claude", Aliases: []string{"strong"}}}
	if err := checkModelTiers(ok); err != nil {
		t.Fatalf("good tiers: %v", err)
	}
	if !slices.Equal(ok["dev"].Aliases, []string{"standard", "bulk"}) {
		t.Fatalf("aliases not normalized: %+v", ok["dev"].Aliases)
	}
	seven := make([]TierCandidate, maxTierCandidates+1)
	for i := range seven {
		seven[i] = TierCandidate{Launcher: "claude"}
	}
	for name, tiers := range map[string]map[string]ModelTier{
		"too many candidates":   {"dev": {Launcher: "claude", Candidates: seven}},
		"candidate no launcher": {"dev": {Launcher: "claude", Candidates: []TierCandidate{{Launcher: "claude"}, {Model: "m"}}}},
		"candidate bad effort":  {"dev": {Launcher: "claude", Candidates: []TierCandidate{{Launcher: "claude"}, {Launcher: "codex", Effort: "huge"}}}},
		"candidate bad speed":   {"dev": {Launcher: "claude", Candidates: []TierCandidate{{Launcher: "claude"}, {Launcher: "codex", Speed: "warp"}}}},
		"candidate bad mode":    {"dev": {Launcher: "claude", Candidates: []TierCandidate{{Launcher: "claude"}, {Launcher: "codex", PermissionMode: "anything"}}}},
		"candidate no modes":    {"dev": {Launcher: "claude", Candidates: []TierCandidate{{Launcher: "claude"}, {Launcher: "omp", PermissionMode: "plan"}}}},
		"alias is a tier name":  {"dev": {Launcher: "claude", Aliases: []string{"core"}}, "core": {Launcher: "claude"}},
		"alias of two tiers":    {"dev": {Launcher: "claude", Aliases: []string{"old"}}, "core": {Launcher: "claude", Aliases: []string{"old"}}},
		"alias bad form":        {"dev": {Launcher: "claude", Aliases: []string{"Old Name"}}},
	} {
		if err := checkModelTiers(tiers); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// A model id is held only on the candidates added or changed.
	was := ModelTier{Launcher: "claude", Candidates: []TierCandidate{{Launcher: "claude"}, {Launcher: "codex", Model: "old+model"}}}
	if err := checkTierModel("dev", was, &was); err != nil {
		t.Fatalf("an unchanged legacy model blocked the save: %v", err)
	}
	changed := ModelTier{Launcher: "claude", Candidates: []TierCandidate{{Launcher: "claude"}, {Launcher: "codex", Model: "new+model"}}}
	if err := checkTierModel("dev", changed, &was); err == nil || !strings.Contains(err.Error(), "dev #2") {
		t.Fatalf("a changed bad model on candidate 2: %v", err)
	}
}

// An older client's PUT sends a tier's flat fields only: they replace the first candidate, and the other candidates
// and the aliases stay. A body with candidates and aliases says them itself; an empty list clears them.
func TestModelProfilePutMergesOldShapeTiers(t *testing.T) {
	s, _, _ := modelService(t)
	ctx := context.Background()
	if _, err := s.saveModelProfile(ctx, nil, "custom", ProfileActor{Kind: "hub"}, func(p *ModelProfile, _ bool) error {
		p.Name = "Custom"
		p.Tiers = map[string]ModelTier{"dev": {Description: "Development",
			Candidates: []TierCandidate{{Launcher: "first", Model: "m-first"}, {Launcher: "second", Model: "m-second"}, {Launcher: "third"}},
			Aliases:    []string{"standard"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tier := func() ModelTier {
		t.Helper()
		ps, _ := storedTiers(t, s)
		got, ok := tierOf(ps, "custom", "dev")
		if !ok {
			t.Fatal("tier dev is gone")
		}
		return got
	}
	if got := tier(); got.Launcher != "first" || got.Model != "m-first" || len(got.Candidates) != 3 {
		t.Fatalf("flat fields do not mirror the first candidate: %+v", got)
	}
	revision := func() int64 { ps, _ := storedTiers(t, s); return ps.Revision }
	put := func(tiers string) {
		t.Helper()
		body := `{"revision":` + jsonInt(revision()) + `,"name":"Custom","tiers":` + tiers + `}`
		if r := request(s, http.MethodPut, "/api/v1/model-profiles/custom", body); r.Code != http.StatusOK {
			t.Fatalf("PUT %s: %d %s", tiers, r.Code, r.Body.String())
		}
	}
	put(`{"dev":{"launcher":"replaced","model":"m-new","effort":"high","description":"Development"}}`)
	got := tier()
	want := []TierCandidate{{Launcher: "replaced", Model: "m-new", Effort: "high"}, {Launcher: "second", Model: "m-second"}, {Launcher: "third"}}
	if !slices.Equal(got.Candidates, want) || got.Launcher != "replaced" || got.Effort != "high" || !slices.Equal(got.Aliases, []string{"standard"}) {
		t.Fatalf("old-shape PUT lost candidates or aliases: %+v", got)
	}
	put(`{"dev":{"launcher":"ignored","candidates":[{"launcher":"second"},{"launcher":"replaced","model":"m-new"}],"aliases":["standard","bulk"]}}`)
	got = tier()
	if got.Launcher != "second" || got.Model != "" || len(got.Candidates) != 2 || !slices.Equal(got.Aliases, []string{"standard", "bulk"}) {
		t.Fatalf("new-shape PUT: %+v", got)
	}
	put(`{"dev":{"launcher":"only","candidates":[],"aliases":[]}}`)
	if got = tier(); got.Launcher != "only" || got.Candidates != nil || got.Aliases != nil {
		t.Fatalf("empty lists did not clear: %+v", got)
	}
	// A body that sends seven candidates is refused.
	seven := strings.Repeat(`{"launcher":"x"},`, maxTierCandidates) + `{"launcher":"x"}`
	body := `{"revision":` + jsonInt(revision()) + `,"name":"Custom","tiers":{"dev":{"launcher":"x","candidates":[` + seven + `]}}}`
	if r := request(s, http.MethodPut, "/api/v1/model-profiles/custom", body); r.Code != http.StatusBadRequest {
		t.Fatalf("seven candidates: %d %s", r.Code, r.Body.String())
	}
}

func jsonInt(n int64) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

// pickerService is an assistant on machine server, which lists claude and fast, with one profile in use.
func pickerService(t *testing.T, tiers map[string]ModelTier) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.launcherList = []proto.Launcher{{Name: "claude"}, {Name: "fast", Model: "m-fast"}}
	s := newTestService(t, f)
	putTierProfiles(t, s, "economy", tiers, nil)
	return s, f
}

// One candidate starts exactly as a tier did before candidates: the same launcher, model, effort, speed and permission
// mode, nothing read of the computer, and the reason "only".
func TestSingleCandidateTierStartsAsBefore(t *testing.T) {
	tier := ModelTier{Launcher: "absent", Model: "m-one", Effort: "high", Speed: "fast", PermissionMode: "plan", Description: "Dev"}
	s, f := pickerService(t, map[string]ModelTier{"dev": tier})
	listCalls := func() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.launcherListParams) }
	before := listCalls()
	ctx, slot := withTierStart(context.Background(), "server")
	params := map[string]any{"tier": "dev"}
	if err := s.applyStartTier(ctx, params); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"launcher": "absent", "model": "m-one", "effort": "high", "speed": "fast", "permission_mode": "plan"} {
		if got, _ := params[key].(string); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if _, ok := params["tier"]; ok {
		t.Fatal("tier leaked to the node")
	}
	if listed := listCalls() - before; listed != 0 {
		t.Fatalf("a one-candidate tier read the computer's launchers %d times", listed)
	}
	if slot.pick == nil || slot.pick.Reason != tierReasonOnly || slot.pick.Of != 1 || slot.pick.Candidate != 0 {
		t.Fatalf("pick: %+v", slot.pick)
	}
	d := slot.pick.dispatch()
	if d.Tier != "dev" || d.Launcher != "absent" || d.Model != "m-one" || d.Reason != "only" || d.Of != 1 || d.Args != nil {
		t.Fatalf("dispatch: %+v", d)
	}
	// A launcher of the start's own: the tier gives nothing, and there is no dispatch to record.
	ctx, slot = withTierStart(context.Background(), "server")
	if err := s.applyStartTier(ctx, map[string]any{"tier": "dev", "launcher": "claude"}); err != nil || slot.pick != nil {
		t.Fatalf("own launcher: %v %+v", err, slot.pick)
	}
}

// Several candidates: one this computer lacks or whose health check failed is passed over, the first usable one in
// order is taken, and with none usable the first; nothing outside the list is ever chosen.
func TestTierCandidatesFilterInOrder(t *testing.T) {
	s, _ := pickerService(t, map[string]ModelTier{
		"dev":  {Launcher: "missing", Candidates: []TierCandidate{{Launcher: "missing", Model: "m0"}, {Launcher: "fast", Model: "m1"}, {Launcher: "claude", Model: "m2"}}},
		"none": {Launcher: "gone", Candidates: []TierCandidate{{Launcher: "gone", Model: "g0"}, {Launcher: "away", Model: "g1"}}},
	})
	ctx := context.Background()
	pick, err := s.pickTierCandidate(ctx, "dev", tierFacts{Machine: "server"})
	if err != nil {
		t.Fatal(err)
	}
	if pick.Candidate != 1 || pick.Tier.Launcher != "fast" || pick.Tier.Model != "m1" || pick.Of != 3 || pick.Reason != tierReasonUnavailable ||
		len(pick.Checks) != 3 || pick.Checks[0].Available || !pick.Checks[1].Available || !pick.Checks[2].Available {
		t.Fatalf("pick: %+v", pick)
	}
	if d := pick.dispatch(); d.Candidate != 1 || d.Of != 3 || d.Reason != "unavailable" || !slices.Equal(d.Args["passed_over"].([]string), []string{"missing"}) {
		t.Fatalf("dispatch: %+v", d)
	}
	// A failed health check passes fast over too.
	s.recordStartFailure(ctx, "server", "fast", terminalRefusal(proto.CodeLauncherMissing, "fast"))
	if pick, _ = s.pickTierCandidate(ctx, "dev", tierFacts{Machine: "server"}); pick.Candidate != 2 || pick.Tier.Launcher != "claude" {
		t.Fatalf("an unhealthy candidate was taken: %+v", pick)
	}
	// None usable: the first, for the start's own refusal to say why.
	if pick, _ = s.pickTierCandidate(ctx, "none", tierFacts{Machine: "server"}); pick.Candidate != 0 || pick.Tier.Launcher != "gone" || pick.Reason != tierReasonFirst {
		t.Fatalf("none usable: %+v", pick)
	}
	// The ranker (#14701) may take another usable one, never an unusable or unlisted one.
	s.tierRank = func(_ context.Context, in tierRankInput) (int, string, bool) { return 0, "score", true }
	if pick, _ = s.pickTierCandidate(ctx, "dev", tierFacts{Machine: "server"}); pick.Candidate != 2 {
		t.Fatalf("the ranker took an unusable candidate: %+v", pick)
	}
	s.tierRank = func(_ context.Context, in tierRankInput) (int, string, bool) { return 2, "score", true }
	s.recordStartSuccess(ctx, "server", "fast")
	if pick, _ = s.pickTierCandidate(ctx, "dev", tierFacts{Machine: "server"}); pick.Candidate != 2 || pick.Reason != "score" {
		t.Fatalf("the ranker's pick: %+v", pick)
	}
	s.tierRank = nil
	// The start-defaults source and the New Agent form take the same first usable candidate.
	got, err := s.candidateSource().candidates(ctx, "server", []string{"dev", "none"})
	if err != nil || !got["dev"].Available || got["dev"].Launcher != "fast" || got["none"].Available || got["none"].Launcher != "gone" {
		t.Fatalf("start-defaults candidates: %+v %v", got, err)
	}
	_, tiers := s.pickerTiers(ctx, "server", []ConfiguredLauncher{{Launcher: proto.Launcher{Name: "claude"}}, {Launcher: proto.Launcher{Name: "fast"}}})
	if len(tiers) != 1 || tiers[0].Name != "dev" || tiers[0].Launcher != "fast" || tiers[0].Candidate != 1 || tiers[0].Candidates != nil {
		t.Fatalf("picker tiers: %+v", tiers)
	}
}

// A plan task's start on a tier records its dispatch on the plan and one model_routing row for the profile in use.
func TestTaskStartRecordsTheDispatch(t *testing.T) {
	s, f, st := newProfileTaskService(t)
	requireProfileMCP(t, s, "model_profile_switch", map[string]any{"id": "performance"}, nil)
	p := dispatchProfilePlan(t, s, st, profilePlanTask())
	if p.Tasks[0].Dispatch != nil {
		t.Fatalf("a plan not started has a dispatch: %+v", p.Tasks[0].Dispatch)
	}
	if w := request(s, http.MethodPost, "/api/v1/task-plans/"+p.ID+"/start", ""); w.Code != http.StatusOK {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	waitFor(t, "the dispatch record", func() bool { return len(st.memStore.modelRoutings()) >= 2 })
	rows := st.memStore.modelRoutings()
	todo := onlyTodo(s)
	if len(rows) != 2 || len(todo.Agents) != 1 {
		t.Fatalf("rows %+v todo %+v", rows, todo)
	}
	r := rows[0]
	if !r.Active || r.ProfileID != "performance" || r.Tier != "dev" || r.Candidate != 0 || r.Of != 1 || r.Launcher != "codex" ||
		r.Model != "test-performance" || r.Effort != "high" || r.Reason != tierReasonOnly || r.TodoID != todo.ID || r.PlanID != p.ID ||
		r.SessionID != todo.Agents[0].SessionID || r.SessionID == "" {
		t.Fatalf("model_routing active row: %+v", r)
	}
	// Inactive profile economy (which has tier standard → dev) gets a trial row; empty (no tiers) writes nothing.
	tr := rows[1]
	if tr.Active || tr.ProfileID != "economy" || tr.Tier != "dev" || tr.Candidate != 0 || tr.Of != 1 || tr.Launcher != "claude" ||
		tr.Model != "test-economy" || tr.Effort != "low" || tr.Reason != tierReasonOnly || tr.TodoID != todo.ID || tr.PlanID != p.ID ||
		tr.SessionID != r.SessionID {
		t.Fatalf("model_routing trial row: %+v", tr)
	}
	waitFor(t, "the plan's dispatch", func() bool {
		current, err := s.loadPlan(context.Background(), p.ID)
		return err == nil && current.Tasks[0].Dispatch != nil
	})
	current, _ := s.loadPlan(context.Background(), p.ID)
	if d := current.Tasks[0].Dispatch; d.Tier != "dev" || d.Launcher != "codex" || d.Reason != "only" || current.Tasks[0].Tier != "standard" {
		t.Fatalf("plan dispatch: %+v (task tier %q)", d, current.Tasks[0].Tier)
	}
	if calls := profileNodeStarts(f); len(calls) != 1 {
		t.Fatalf("starts: %d", len(calls))
	}
	// A task_plan call cannot set the hub's dispatch.
	task := profilePlanTask()
	task.Dispatch = &TaskDispatch{Tier: "forged", Launcher: "x", Reason: "score"}
	res := profileMCPCall(t, s, "main", "task_plan", map[string]any{"tasks": []PlanTask{task}}, true, false)
	if !res.IsError && len(res.Content) > 0 && strings.Contains(res.Content[0].Text, "forged") {
		t.Fatalf("task_plan: %+v", res)
	}
	st.planMu.Lock()
	defer st.planMu.Unlock()
	for _, row := range st.plans {
		if strings.Contains(string(row.Tasks), "forged") {
			t.Fatalf("a tool call stored a dispatch: %s", row.Tasks)
		}
	}
}

// A capture gives core and dev their legacy aliases, so stored plans and older clients still resolve them (#15010).
func TestCapturedTiersAnswerToOldNames(t *testing.T) {
	s, _, st := modelService(t)
	ctx := context.Background()
	sealed, err := s.sealKey("test-secret-key")
	if err != nil {
		t.Fatal(err)
	}
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "current", Model: "test-current",
		BaseURL: "https://example.invalid"}, KeySealed: sealed})
	for id, binding := range map[string]ModelBinding{
		"assistant": {Provider: "launcher.current", Model: "test-chat"},
		"agent.new": {Provider: "launcher.current", Model: "test-agents"},
	} {
		if err := st.PutSetting(ctx, modelRecordKey(id), modelRecord{Revision: 3, Binding: binding}); err != nil {
			t.Fatal(err)
		}
	}
	oldNames := func(where string, tiers map[string]ModelTier) {
		t.Helper()
		if !slices.Equal(tiers["core"].Aliases, []string{"strong"}) || !slices.Equal(tiers["dev"].Aliases, []string{"standard"}) {
			t.Fatalf("%s: tiers do not answer to their older names: %+v", where, tiers)
		}
	}
	resolves := func(where string) {
		t.Helper()
		for name, want := range map[string][2]string{"strong": {"core", "test-chat"}, "standard": {"dev", "test-agents"}} {
			pick, err := s.pickTierCandidate(ctx, name, tierFacts{})
			if err != nil {
				t.Fatalf("%s: %s: %v", where, name, err)
			}
			if pick.Name != want[0] || pick.Tier.Launcher != "current" || pick.Tier.Model != want[1] || pick.Reason != tierReasonOnly {
				t.Fatalf("%s: %s picked %+v", where, name, pick)
			}
		}
	}
	raw := func() modelProfiles {
		t.Helper()
		var ps modelProfiles
		if _, err := st.Setting(ctx, modelProfilesKey, &ps); err != nil {
			t.Fatal(err)
		}
		return ps
	}
	capturedTiers := func() map[string]ModelTier {
		t.Helper()
		for _, p := range raw().Profiles {
			if p.ID == "captured" {
				return p.Tiers
			}
		}
		t.Fatal("no profile captured stored")
		return nil
	}
	capture := func() {
		t.Helper()
		body := `{"revision":` + jsonInt(raw().Revision) + `,"name":"Captured","capture":true}`
		if r := request(s, http.MethodPut, "/api/v1/model-profiles/captured", body); r.Code != http.StatusOK {
			t.Fatalf("capture: %d %s", r.Code, r.Body.String())
		}
	}
	// A capture into a new profile stores the aliases.
	capture()
	tiers := capturedTiers()
	oldNames("captured, as stored", tiers)
	if err := checkModelTiers(maps.Clone(tiers)); err != nil {
		t.Fatalf("captured tiers fail the save check: %v", err)
	}
	if _, err := s.applyModelProfile(ctx, "captured", ProfileActor{Kind: "hub"}); err != nil {
		t.Fatal(err)
	}
	if _, state := storedTiers(t, s); state.Active != "captured" {
		t.Fatalf("captured is not in use: %+v", state)
	}
	resolves("captured in use")
	// Recapturing preserves the aliases.
	capture()
	oldNames("recaptured", capturedTiers())
}

// renamedTiers pairs a tier an older client renamed with the stored one it replaces: an unchanged first candidate and
// description, or one tier gone and one added with the same non-empty description. Nothing else pairs.
func TestRenamedTiersPairs(t *testing.T) {
	core := ModelTier{Launcher: "first", Model: "m-first", Description: "Core",
		Candidates: []TierCandidate{{Launcher: "first", Model: "m-first"}, {Launcher: "second"}}, Aliases: []string{"strong"}}
	dev := ModelTier{Launcher: "codex", Description: "Dev", Aliases: []string{"standard"}}
	flat := func(t ModelTier) ModelTier {
		return ModelTier{Launcher: t.Launcher, Model: t.Model, Description: t.Description}
	}
	for _, tc := range []struct {
		name    string
		was, in map[string]ModelTier
		want    map[string]string
	}{
		{"exact rename", map[string]ModelTier{"core": core}, map[string]ModelTier{"lead": flat(core)}, map[string]string{"lead": "core"}},
		{"rename and edit, same description", map[string]ModelTier{"core": core},
			map[string]ModelTier{"lead": {Launcher: "other", Description: "Core"}}, map[string]string{"lead": "core"}},
		{"same first candidate, other description", map[string]ModelTier{"core": core},
			map[string]ModelTier{"review": {Launcher: "first", Model: "m-first", Description: "Review"}}, map[string]string{}},
		{"same first candidate, no description", map[string]ModelTier{"core": {Launcher: "first", Aliases: []string{"strong"}}},
			map[string]ModelTier{"review": {Launcher: "first"}}, map[string]string{"review": "core"}},
		{"changed, no description", map[string]ModelTier{"core": {Launcher: "first", Aliases: []string{"strong"}}},
			map[string]ModelTier{"review": {Launcher: "other"}}, map[string]string{}},
		{"nothing shared", map[string]ModelTier{"core": core}, map[string]ModelTier{"fresh": {Launcher: "x", Description: "Other"}}, map[string]string{}},
		{"new shape is never paired", map[string]ModelTier{"core": core},
			map[string]ModelTier{"lead": {Launcher: "first", Model: "m-first", Description: "Core", Aliases: []string{}}}, map[string]string{}},
		{"kept name is not a rename", map[string]ModelTier{"core": core, "dev": dev},
			map[string]ModelTier{"core": flat(core), "lead": flat(dev)}, map[string]string{"lead": "dev"}},
		{"two renames, each by its own fields", map[string]ModelTier{"core": core, "dev": dev},
			map[string]ModelTier{"x": flat(core), "y": flat(dev)}, map[string]string{"x": "core", "y": "dev"}},
		{"two added, two gone, nothing equal", map[string]ModelTier{"core": core, "dev": dev},
			map[string]ModelTier{"x": {Launcher: "c", Description: "Core"}, "y": {Launcher: "d", Description: "Dev"}}, map[string]string{}},
		{"two added equal to one gone: the first takes it", map[string]ModelTier{"core": core},
			map[string]ModelTier{"a": flat(core), "b": flat(core)}, map[string]string{"a": "core"}},
	} {
		if got := renamedTiers(tc.was, tc.in); !maps.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

// An older client renames a tier by sending it in the flat shape under its new name: it keeps its candidates and
// aliases. Deleting a tier and adding an unrelated one in the same save hands nothing over (#15010).
func TestModelProfilePutRenameKeepsCandidatesAndAliases(t *testing.T) {
	s, _, _ := modelService(t)
	ctx := context.Background()
	seed := func(tiers map[string]ModelTier) {
		t.Helper()
		if _, err := s.saveModelProfile(ctx, nil, "custom", ProfileActor{Kind: "hub"}, func(p *ModelProfile, _ bool) error {
			p.Name, p.Tiers = "Custom", tiers
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	core := ModelTier{Launcher: "first", Model: "m-first", Description: "Core",
		Candidates: []TierCandidate{{Launcher: "first", Model: "m-first"}, {Launcher: "second", Model: "m-second"}}, Aliases: []string{"strong"}}
	dev := ModelTier{Launcher: "codex", Description: "Dev", Aliases: []string{"standard"}}
	profile := func() *ModelProfile {
		t.Helper()
		ps, _ := storedTiers(t, s)
		for i := range ps.Profiles {
			if ps.Profiles[i].ID == "custom" {
				return &ps.Profiles[i]
			}
		}
		t.Fatal("no profile custom")
		return nil
	}
	put := func(tiers string) int {
		t.Helper()
		ps, _ := storedTiers(t, s)
		body := `{"revision":` + jsonInt(ps.Revision) + `,"name":"Custom","tiers":` + tiers + `}`
		return request(s, http.MethodPut, "/api/v1/model-profiles/custom", body).Code
	}

	// A pure rename.
	seed(map[string]ModelTier{"core": core})
	if code := put(`{"lead":{"launcher":"first","model":"m-first","description":"Core"}}`); code != http.StatusOK {
		t.Fatalf("rename: %d", code)
	}
	p := profile()
	if _, ok := p.Tiers["core"]; ok {
		t.Fatalf("the old name stayed: %+v", p.Tiers)
	}
	if lead := p.Tiers["lead"]; !slices.Equal(lead.Aliases, []string{"strong"}) || !slices.Equal(lead.Candidates, core.Candidates) {
		t.Fatalf("rename lost candidates or aliases: %+v", lead)
	}
	if own, ok := p.lookupTier("strong"); !ok || own != "lead" {
		t.Fatalf("strong resolves to %q %v", own, ok)
	}

	// A rename that also changes the launcher, the description unchanged: candidate 0 is the new one, the rest stay.
	seed(map[string]ModelTier{"core": core})
	if code := put(`{"lead":{"launcher":"other","description":"Core"}}`); code != http.StatusOK {
		t.Fatalf("rename and edit: %d", code)
	}
	lead := profile().Tiers["lead"]
	if want := []TierCandidate{{Launcher: "other"}, {Launcher: "second", Model: "m-second"}}; !slices.Equal(lead.Candidates, want) ||
		!slices.Equal(lead.Aliases, []string{"strong"}) {
		t.Fatalf("rename and edit: %+v", lead)
	}

	// Deleting core and adding an unrelated tier on the same launcher and model hands it nothing.
	seed(map[string]ModelTier{"core": core})
	if code := put(`{"review":{"launcher":"first","model":"m-first","description":"Review"}}`); code != http.StatusOK {
		t.Fatalf("delete and add: %d", code)
	}
	p = profile()
	if review := p.Tiers["review"]; review.Aliases != nil || review.Candidates != nil {
		t.Fatalf("an unrelated new tier inherited: %+v", review)
	}
	if _, ok := p.lookupTier("strong"); ok {
		t.Fatal("strong still resolves after its tier was deleted")
	}

	// Two tiers renamed at once each keep their own aliases; two unrelated new ones get none.
	seed(map[string]ModelTier{"core": core, "dev": dev})
	if code := put(`{"x":{"launcher":"c","description":"E"},"y":{"launcher":"d","description":"F"}}`); code != http.StatusOK {
		t.Fatalf("two new: %d", code)
	}
	if p = profile(); p.Tiers["x"].Aliases != nil || p.Tiers["y"].Aliases != nil {
		t.Fatalf("unrelated new tiers inherited: %+v", p.Tiers)
	}
	seed(map[string]ModelTier{"core": core, "dev": dev})
	if code := put(`{"x":{"launcher":"first","model":"m-first","description":"Core"},"y":{"launcher":"codex","description":"Dev"}}`); code != http.StatusOK {
		t.Fatalf("two renames: %d", code)
	}
	if p = profile(); !slices.Equal(p.Tiers["x"].Aliases, []string{"strong"}) || !slices.Equal(p.Tiers["y"].Aliases, []string{"standard"}) {
		t.Fatalf("two renames: %+v", p.Tiers)
	}
}

// An alias the older client cannot see gives way to a tier name it sends: adding a tier named strong while core
// answers to strong saves, and strong then names the new tier. An alias a client sends itself that is another tier's
// name is still refused, from a PUT and from the assistant's tier edit (#15010).
func TestModelProfilePutHiddenAliasGivesWay(t *testing.T) {
	s, _, _ := modelService(t)
	ctx := context.Background()
	seed := func(tiers map[string]ModelTier) {
		t.Helper()
		if _, err := s.saveModelProfile(ctx, nil, "custom", ProfileActor{Kind: "hub"}, func(p *ModelProfile, _ bool) error {
			p.Name, p.Tiers = "Custom", tiers
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	core := ModelTier{Launcher: "first", Description: "Core", Aliases: []string{"strong", "lead"}}
	dev := ModelTier{Launcher: "codex", Description: "Dev", Aliases: []string{"standard"}}
	tiers := func() map[string]ModelTier {
		t.Helper()
		ps, _ := storedTiers(t, s)
		for _, p := range ps.Profiles {
			if p.ID == "custom" {
				return p.Tiers
			}
		}
		t.Fatal("no profile custom")
		return nil
	}
	put := func(body string) int {
		t.Helper()
		ps, _ := storedTiers(t, s)
		return request(s, http.MethodPut, "/api/v1/model-profiles/custom",
			`{"revision":`+jsonInt(ps.Revision)+`,"name":"Custom","tiers":`+body+`}`).Code
	}

	// An older client adds a tier named strong: core keeps its other alias and strong is the new tier, also on the
	// next read (the seeded-name migration leaves it, since core exists).
	seed(map[string]ModelTier{"core": core, "dev": dev})
	if code := put(`{"core":{"launcher":"first","description":"Core"},"dev":{"launcher":"codex","description":"Dev"},` +
		`"strong":{"launcher":"x","model":"m2"}}`); code != http.StatusOK {
		t.Fatalf("adding strong: %d", code)
	}
	got := tiers()
	if !slices.Equal(got["core"].Aliases, []string{"lead"}) || got["strong"].Model != "m2" || !slices.Equal(got["dev"].Aliases, []string{"standard"}) {
		t.Fatalf("adding strong: %+v", got)
	}
	p := &ModelProfile{Tiers: got}
	if own, _ := p.lookupTier("strong"); own != "strong" {
		t.Fatalf("strong resolves to %q", own)
	}
	if own, _ := p.lookupTier("lead"); own != "core" {
		t.Fatalf("lead resolves to %q", own)
	}

	// The same for standard against dev: dev keeps no aliases at all.
	seed(map[string]ModelTier{"core": core, "dev": dev})
	if code := put(`{"core":{"launcher":"first","description":"Core"},"dev":{"launcher":"codex","description":"Dev"},` +
		`"standard":{"launcher":"y"}}`); code != http.StatusOK {
		t.Fatalf("adding standard: %d", code)
	}
	if got = tiers(); got["dev"].Aliases != nil || got["standard"].Launcher != "y" {
		t.Fatalf("adding standard: %+v", got)
	}

	// A hidden alias also gives way to an alias another tier sends itself.
	seed(map[string]ModelTier{"core": core, "dev": dev})
	if code := put(`{"core":{"launcher":"first","description":"Core"},"dev":{"launcher":"codex","aliases":["standard","lead"]}}`); code != http.StatusOK {
		t.Fatalf("sent alias over a hidden one: %d", code)
	}
	if got = tiers(); !slices.Equal(got["core"].Aliases, []string{"strong"}) || !slices.Equal(got["dev"].Aliases, []string{"standard", "lead"}) {
		t.Fatalf("sent alias over a hidden one: %+v", got)
	}

	// Renaming core to strong from an older client is accepted, and the seeded-name migration reads it back as core
	// answering to strong (a known quirk of the read-time migration, as before this change).
	seed(map[string]ModelTier{"core": core})
	if code := put(`{"strong":{"launcher":"first","description":"Core"}}`); code != http.StatusOK {
		t.Fatalf("rename core to strong: %d", code)
	}
	if got = tiers(); len(got) != 1 || !slices.Contains(got["core"].Aliases, "strong") {
		t.Fatalf("rename core to strong read back as %+v", got)
	}

	// Sent explicitly, an alias that is another tier's name is still refused.
	seed(map[string]ModelTier{"core": core, "dev": dev})
	if code := put(`{"core":{"launcher":"first","aliases":["dev"]},"dev":{"launcher":"codex"}}`); code != http.StatusBadRequest {
		t.Fatalf("explicit alias dev on core: %d", code)
	}
	if got = tiers(); !slices.Equal(got["core"].Aliases, core.Aliases) {
		t.Fatalf("a refused PUT changed the tiers: %+v", got)
	}
	// And from the assistant's tier edit.
	r := s.Tool(ctx, ToolCall{Name: "persona_settings", MachineID: "server", SessionID: "c1",
		Arguments: json.RawMessage(`{"action":"set","key":"model_profiles.tiers","value":{"tier":"core","aliases":["dev"],"profiles":["custom"]}}`)})
	if !r.IsError || len(s.pendingQuestions()) != 0 {
		t.Fatalf("tier edit setting alias dev on core: %+v", r)
	}
}
