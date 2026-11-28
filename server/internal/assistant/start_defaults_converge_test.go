package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"time"
)

// The converged wire names (#11719): applies_when and candidates, with priority. The old names still decode, reads
// answer both, and a body whose two names disagree is refused — including on the nested start_defaults of PUT
// /api/v1/assistant, which has no DisallowUnknownFields of its own.
func TestStartDefaultsConvergedNamesAndAliases(t *testing.T) {
	s, f := defaultsHub(t)
	ctx := context.Background()
	// New names, with priority: the write works and reaches a real start.
	body := `{"start_defaults":{"rules":[{"applies_when":{"role":"worker"},"then":{
		"candidates":[{"name":"quick","priority":1},{"name":"deep","priority":5}],"effort":"max"}}]}}`
	if code, got := putStartDefaults(t, s, roleFull, "", body); code != http.StatusOK {
		t.Fatalf("converged write: %d %s", code, got)
	}
	// Reads carry both names with the same content, so either generation of client round-trips it.
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant/start-defaults", nil))
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	rule := got["start_defaults"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(rule["when"], rule["applies_when"]) {
		t.Fatalf("when and applies_when differ: %v", rule)
	}
	then := rule["then"].(map[string]any)
	if !reflect.DeepEqual(then["presets"], then["candidates"]) {
		t.Fatalf("presets and candidates differ: %v", then)
	}
	if c := then["candidates"].([]any)[1].(map[string]any); c["priority"] != float64(5) {
		t.Fatalf("priority not carried: %v", c)
	}
	task := defaultsTask()
	if _, _, err := s.startTaskAgent(ctx, &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["model"] != "gw-deep" || st["effort"] != "max" {
		t.Fatalf("the converged policy did not apply: %v", st)
	}
	// The stored policy (Postgres JSON) reads back after carrying both names.
	if p := s.activeStartDefaults(ctx); p == nil || p.Rules[0].When.Role != "worker" || p.Rules[0].Then.Presets[1].Priority != 5 {
		t.Fatalf("the stored policy did not round-trip: %+v", p)
	}
	// Old names still write, on this route and nested in PUT /api/v1/assistant.
	if code, got := putStartDefaults(t, s, roleFull, "", `{"start_defaults":{"rules":[{"when":{},"then":{"presets":[{"name":"quick"}]}}]}}`); code != http.StatusOK {
		t.Fatalf("legacy write: %d %s", code, got)
	}
	nested := httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(
		`{"start_defaults":{"rules":[{"applies_when":{},"then":{"candidates":[{"name":"deep"}]}}]}}`))
	nested.Header.Set(RoleHeader, roleFull)
	nrec := httptest.NewRecorder()
	s.ServeHTTP(nrec, nested)
	if nrec.Code != http.StatusOK {
		t.Fatalf("nested converged write: %d %s", nrec.Code, nrec.Body.String())
	}
	if p := s.activeStartDefaults(ctx); p == nil || p.Rules[0].Then.Presets[0].Name != "deep" {
		t.Fatalf("the nested write did not store the candidates: %+v", p)
	}
}

// A body that names both generations of a field is accepted when they agree (that is what a read answers) and
// refused when they disagree; a misspelt field is still a 400 everywhere, aliases included.
func TestStartDefaultsAliasConflictsAndUnknownFields(t *testing.T) {
	s, _ := defaultsHub(t)
	for _, c := range []struct {
		name, body string
		code       int
	}{
		{"both matches agree", `{"start_defaults":{"rules":[{"when":{"role":"worker"},"applies_when":{"role":"worker"},"then":{"effort":"max"}}]}}`, http.StatusOK},
		{"both matches disagree", `{"start_defaults":{"rules":[{"when":{"role":"worker"},"applies_when":{"role":"leader"},"then":{"effort":"max"}}]}}`, http.StatusBadRequest},
		{"both lists agree", `{"start_defaults":{"rules":[{"when":{},"then":{"presets":[{"name":"deep"}],"candidates":[{"name":"deep"}]}}]}}`, http.StatusOK},
		{"both lists disagree", `{"start_defaults":{"rules":[{"when":{},"then":{"presets":[{"name":"deep"}],"candidates":[{"name":"quick"}]}}]}}`, http.StatusBadRequest},
		{"misspelt applies_when", `{"start_defaults":{"rules":[{"applies_whenx":{},"then":{"effort":"max"}}]}}`, http.StatusBadRequest},
		{"misspelt candidates", `{"start_defaults":{"rules":[{"when":{},"then":{"candidatez":[{"name":"deep"}]}}]}}`, http.StatusBadRequest},
		{"unknown match field", `{"start_defaults":{"rules":[{"applies_when":{"rol":"worker"},"then":{"effort":"max"}}]}}`, http.StatusBadRequest},
		// An explicit null is present on the wire and must not slip past the conflict check the way a decoded nil
		// pointer would (#11719 review): a null beside a real value is a disagreement.
		{"null match beside a real match", `{"start_defaults":{"rules":[{"when":null,"applies_when":{"role":"leader"},"then":{"effort":"max"}}]}}`, http.StatusBadRequest},
		{"real match beside a null match", `{"start_defaults":{"rules":[{"when":{"role":"worker"},"applies_when":null,"then":{"effort":"max"}}]}}`, http.StatusBadRequest},
		{"null match beside an empty match", `{"start_defaults":{"rules":[{"when":null,"applies_when":{},"then":{"effort":"max"}}]}}`, http.StatusBadRequest},
		{"empty match beside a null match", `{"start_defaults":{"rules":[{"when":{},"applies_when":null,"then":{"effort":"max"}}]}}`, http.StatusBadRequest},
		{"null match beside null fields", `{"start_defaults":{"rules":[{"when":null,"applies_when":{"role":null},"then":{"effort":"max"}}]}}`, http.StatusBadRequest},
		{"null list beside a real list", `{"start_defaults":{"rules":[{"when":{},"then":{"presets":null,"candidates":[{"name":"deep"}]}}]}}`, http.StatusBadRequest},
		{"real list beside a null list", `{"start_defaults":{"rules":[{"when":{},"then":{"presets":[{"name":"deep"}],"candidates":null}}]}}`, http.StatusBadRequest},
		{"null list beside an empty list", `{"start_defaults":{"rules":[{"when":{},"then":{"presets":null,"candidates":[],"effort":"max"}}]}}`, http.StatusBadRequest},
		{"empty list beside a null list", `{"start_defaults":{"rules":[{"when":{},"then":{"presets":[],"candidates":null,"effort":"max"}}]}}`, http.StatusBadRequest},
		{"padded aliases still disagree", `{"start_defaults":{"rules":[{"when":{},"then":{"presets":[{"name":" deep "}],"candidates":[{"name":"deep"}]}}]}}`, http.StatusBadRequest},
		{"both matches null", `{"start_defaults":{"rules":[{"when":null,"applies_when":null,"then":{"effort":"max"}}]}}`, http.StatusOK},
		{"both lists null", `{"start_defaults":{"rules":[{"when":{},"then":{"presets":null,"candidates":null,"effort":"max"}}]}}`, http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			if code, got := putStartDefaults(t, s, roleFull, "", c.body); code != c.code {
				t.Fatalf("dedicated %s: %d %s", c.name, code, got)
			}
			req := httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(c.body))
			req.Header.Set(RoleHeader, roleFull)
			rec := httptest.NewRecorder()
			s.ServeHTTP(rec, req)
			if rec.Code != c.code {
				t.Fatalf("nested %s: %d %s", c.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// Priority orders the usable candidates: the highest priority with anything usable wins, however the list is ordered,
// and a group whose presets cannot run falls through to the next one. All-zero priorities keep the old order/weights.
func TestStartDefaultsPriorityOrdersAndFallsThrough(t *testing.T) {
	ctx := context.Background()
	// Higher priority wins even when it is listed last.
	s, f := defaultsHub(t)
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
		return StartDefaults{Presets: []PresetChoice{{Name: "quick", Priority: 1}, {Name: "deep", Priority: 9}}}, true
	})
	task := defaultsTask()
	if _, _, err := s.startTaskAgent(ctx, &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["model"] != "gw-deep" {
		t.Fatalf("the higher priority did not win: %v", st)
	}
	// The top group's presets cannot run here: the next group is used, and the skip is named.
	s, f = defaultsHub(t)
	saveTiers(t, s, map[string]ModelTier{
		"elsewhere": {Launcher: "other", Model: "o-1"},
		"quick":     {Launcher: "gateway", Model: "gw-quick"},
		"plain":     {Launcher: "claude", Model: "claude-plain"},
	})
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
		return StartDefaults{By: "Example", Presets: []PresetChoice{
			{Name: "elsewhere", Priority: 100}, {Name: "gone", Priority: 100},
			{Name: "plain", Priority: 2}, {Name: "quick", Priority: 1},
		}}, true
	})
	task = defaultsTask()
	task.MachinePolicy = machineFlexible
	if _, _, err := s.startTaskAgent(ctx, &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["model"] != "claude-plain" {
		t.Fatalf("the next priority did not take over: %v", st)
	}
	for _, name := range []string{"elsewhere", "gone"} {
		if !strings.Contains(task.Why, name) {
			t.Fatalf("the why does not name the passed-over %s: %q", name, task.Why)
		}
	}
	// Priority bounds are validated like weight's.
	for _, body := range []string{
		`{"start_defaults":{"rules":[{"when":{},"then":{"candidates":[{"name":"deep","priority":-1}]}}]}}`,
		`{"start_defaults":{"rules":[{"when":{},"then":{"candidates":[{"name":"deep","priority":1001}]}}]}}`,
	} {
		if code, got := putStartDefaults(t, s, roleFull, "", body); code != http.StatusBadRequest {
			t.Fatalf("out-of-range priority accepted: %d %s", code, got)
		}
	}
}

// Weights share starts only within the winning priority group: the roll's bound is that group's weight sum, never
// the whole list's, and a lower-priority candidate cannot be rolled into the top group's start.
func TestStartDefaultsWeightsWithinTheTopPriority(t *testing.T) {
	ctx := context.Background()
	old := presetIntN
	t.Cleanup(func() { presetIntN = old })
	var bounds []int
	presetIntN = func(n int) int { bounds = append(bounds, n); return n - 1 } // always the last of the rolled group
	s, f := defaultsHub(t)
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
		return StartDefaults{Presets: []PresetChoice{
			{Name: "deep", Priority: 5, Weight: 1}, {Name: "quick", Priority: 5, Weight: 3},
			{Name: "plain", Priority: 1, Weight: 40}, // lower priority: never in the roll, whatever its weight
		}}, true
	})
	task := defaultsTask()
	if _, _, err := s.startTaskAgent(ctx, &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["model"] != "gw-quick" {
		t.Fatalf("the roll did not stay in the top group: %v", st)
	}
	if !slices.Equal(bounds, []int{4}) {
		t.Fatalf("roll bounds = %v, want the top group's weight sum 4", bounds)
	}
	// The production roll is the crypto-grade source and stays in range; the var stays injectable.
	presetIntN = old
	for range 64 {
		if got := presetIntN(7); got < 0 || got >= 7 {
			t.Fatalf("presetIntN out of range: %d", got)
		}
	}
}

// Availability lookups may deduplicate a repeated name, but the rule's choices themselves keep their original
// multiplicity and weights: quick(2), quick(1), deep(1) still rolls over four, not three.
func TestStartDefaultsDuplicateChoicesKeepTheirWeights(t *testing.T) {
	old := presetIntN
	t.Cleanup(func() { presetIntN = old })
	bound := 0
	presetIntN = func(n int) int { bound = n; return 0 }
	s, f := defaultsHub(t)
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
		return StartDefaults{Presets: []PresetChoice{{Name: "quick", Weight: 2}, {Name: "quick", Weight: 1}, {Name: "deep", Weight: 1}}}, true
	})
	task := defaultsTask()
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["model"] != "gw-quick" || bound != 4 {
		t.Fatalf("duplicate choices changed the weighted roll: start=%v bound=%d", st, bound)
	}
}

// A retry re-evaluates the rule and re-rolls, keeping nothing of the earlier try's priority choice: the caller's own
// values are restored first, and a rule that no longer fits leaves no preset, effort or ultracode behind.
func TestStartDefaultsRetryRerollsAndKeepsNothingOfTheOldRule(t *testing.T) {
	ctx := context.Background()
	old := presetIntN
	t.Cleanup(func() { presetIntN = old })
	rolls := 0
	presetIntN = func(n int) int { rolls++; return n - 1 } // deep(1), quick(3): the last roll picks quick
	s, f := defaultsHub(t)
	first := true
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
		if first {
			return StartDefaults{Ultracode: boolp(true), Effort: "max", Presets: []PresetChoice{
				{Name: "deep", Priority: 1}, {Name: "quick", Priority: 5, Weight: 2}, {Name: "plain", Priority: 5, Weight: 1},
			}}, true
		}
		return StartDefaults{}, false // the policy no longer applies at all
	})
	task := defaultsTask()
	if _, _, err := s.startTaskAgent(ctx, &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["model"] != "claude-plain" || st["ultracode"] != true || st["effort"] != "max" {
		t.Fatalf("the first rule did not apply: %v", st)
	}
	if rolls != 1 {
		t.Fatalf("the first try did not roll once: %d", rolls)
	}
	first = false
	if _, _, err := s.startTaskAgent(ctx, &task, true); err != nil {
		t.Fatal(err)
	}
	st := startsOf(f)[1]
	if st["model"] != "gw-model" || st["launcher"] != "gateway" || st["ultracode"] != nil || st["effort"] == "max" {
		t.Fatalf("the retry kept the old rule's values: %v", st)
	}
	if rolls != 1 {
		t.Fatalf("the retry rolled although no rule fits: %d", rolls)
	}
	if task.Ultracode != nil || task.Effort == "max" || task.Tier != "" {
		t.Fatalf("the task kept policy values: %+v", task)
	}
}

// The preview answers what the policy would do and writes nothing: no stored policy, no start, no task, no admission
// claim, no note. A draft in the body is validated but never stored; without one, the policy in force is read.
func TestStartDefaultsPreviewIsReadOnly(t *testing.T) {
	s, f := defaultsHub(t)
	ctx := context.Background()
	// A stored policy exists, so a draft preview must not replace it and a draft-less preview must use it.
	setSettings(t, s, func(v *Settings) {
		v.StartDefaults = &StartDefaultsPolicy{By: "owner", Rules: []StartDefaultsRule{
			{When: StartDefaultsMatch{Role: "leader"}, Then: StartDefaultsThen{Effort: "low"}},
		}}
	})
	before, err := json.Marshal(s.settings(ctx).StartDefaults)
	if err != nil {
		t.Fatal(err)
	}
	draft := `{"start_defaults":{"rules":[{"applies_when":{"role":"worker"},"then":{
		"candidates":[{"name":"gone","priority":9},{"name":"deep","priority":2,"weight":2},{"name":"quick","priority":2,"weight":1}],
		"effort":"max","ultracode":true,"goal":"done_when","plan_first":true}}]}}`
	post := func(body string) (int, map[string]any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/start-defaults/preview", strings.NewReader(body))
		req.Header.Set(RoleHeader, roleFull)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	code, out := post(`{"task":{"machine":"box","role":"worker","supervisor":"leader","done_when":2},` + draft[1:])
	if code != http.StatusOK {
		t.Fatalf("preview: %d %v", code, out)
	}
	if out["matched"] != true || out["rule_index"] != float64(0) {
		t.Fatalf("the draft rule did not match: %v", out)
	}
	if out["effort"] != "max" || out["ultracode"] != true || out["goal"] != "done_when" || out["plan_first"] != true {
		t.Fatalf("the draft's extras are missing: %v", out)
	}
	usable, _ := out["usable"].([]any)
	if len(usable) != 2 { // only the top usable priority, with "gone" passed over
		t.Fatalf("usable = %v", out["usable"])
	}
	for _, row := range usable {
		if row.(map[string]any)["priority"] != float64(2) {
			t.Fatalf("a lower-priority row leaked into usable: %v", usable)
		}
	}
	passed, _ := out["passed_over"].([]any)
	if len(passed) != 1 || passed[0].(map[string]any)["name"] != "gone" {
		t.Fatalf("passed_over = %v", out["passed_over"])
	}
	if _, ok := out["sample"]; !ok {
		t.Fatalf("no sample pick: %v", out)
	}
	// Nothing changed: same stored policy, no Agent started, no plan or task written.
	after, _ := json.Marshal(s.settings(ctx).StartDefaults)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the preview changed the stored policy:\nbefore %s\nafter  %s", before, after)
	}
	if len(startsOf(f)) != 0 {
		t.Fatalf("the preview started an Agent: %v", startsOf(f))
	}
	if s.taskStarting.Load() != 0 {
		t.Fatal("the preview claimed a start")
	}
	// A draft-less preview reads the stored policy (role leader), and an unmatched one says so without side effects.
	code, out = post(`{"task":{"machine":"box","role":"leader"}}`)
	if code != http.StatusOK || out["matched"] != true || out["effort"] != "low" {
		t.Fatalf("the stored policy was not previewed: %d %v", code, out)
	}
	code, out = post(`{"task":{"machine":"box","role":"worker"}}`)
	if code != http.StatusOK || out["matched"] != false {
		t.Fatalf("an unmatched task should report matched=false: %d %v", code, out)
	}
	if len(startsOf(f)) != 0 {
		t.Fatal("a preview started an Agent")
	}
	// Validation: machine required, unknown machine 404, unknown field 400, bad draft 400.
	if code, _ := post(`{"task":{"role":"worker"}}`); code != http.StatusBadRequest {
		t.Fatalf("a preview without a machine: %d", code)
	}
	if code, _ := post(`{"task":{"machine":"nope"}}`); code != http.StatusNotFound {
		t.Fatalf("an unknown machine: %d", code)
	}
	if code, _ := post(`{"task":{"machine":"box"},"wat":1}`); code != http.StatusBadRequest {
		t.Fatalf("an unknown preview field was accepted")
	}
	if code, _ := post(`{"task":{"machine":"box"},"start_defaults":{"rules":[{"when":{},"then":{"effort":"banana"}}]}}`); code != http.StatusBadRequest {
		t.Fatalf("a bad draft was accepted")
	}
}

// New preview validation errors travel through the real assistant relay and its language carrier. Protocol field
// names and legal values stay verbatim while the surrounding sentence is localized in every shipped language.
func TestStartDefaultsPreviewValidationLocalization(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		for _, tc := range []struct {
			name, body, key, wantEN string
			args                    i18n.Args
		}{
			{name: "machine", body: `{"task":{}}`, key: "assistant-start_defaults_preview.field_required", wantEN: "task.machine is required", args: i18n.Args{"field": i18n.Verbatim("task.machine")}},
			{name: "role", body: `{"task":{"machine":"box","role":"typo"}}`, key: "assistant-start_defaults_preview.field_one_of", wantEN: "task.role must be one of worker, leader", args: i18n.Args{"field": i18n.Verbatim("task.role"), "values": i18n.Verbatim("worker, leader")}},
			{name: "supervisor", body: `{"task":{"machine":"box","supervisor":"typo"}}`, key: "assistant-start_defaults_preview.field_one_of", wantEN: "task.supervisor must be one of main, leader, background_supervisor, scheduled, other", args: i18n.Args{"field": i18n.Verbatim("task.supervisor"), "values": i18n.Verbatim("main, leader, background_supervisor, scheduled, other")}},
			{name: "done when", body: `{"task":{"machine":"box","done_when":-1}}`, key: "assistant-start_defaults_preview.field_nonnegative", wantEN: "task.done_when must be 0 or more", args: i18n.Args{"field": i18n.Verbatim("task.done_when")}},
			{name: "effort", body: `{"task":{"machine":"box"},"caller":{"effort":"typo"}}`, key: "assistant-start_defaults_preview.field_one_of", wantEN: "caller.effort must be one of low, medium, high, xhigh, max", args: i18n.Args{"field": i18n.Verbatim("caller.effort"), "values": i18n.Verbatim("low, medium, high, xhigh, max")}},
		} {
			t.Run(string(lang)+"/"+tc.name, func(t *testing.T) {
				s, _ := defaultsHub(t)
				res, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{
					Method: http.MethodPost, Path: "/api/v1/assistant/start-defaults/preview", Body: []byte(tc.body),
					Header: map[string][]string{RoleHeader: {roleFull}, "Accept-Language": {string(lang)}},
				}, nil)
				if err != nil {
					t.Fatal(err)
				}
				var got struct {
					Code      string `json:"code"`
					Error     string `json:"error"`
					Retryable bool   `json:"retryable"`
				}
				if err := json.Unmarshal(res.Body, &got); err != nil {
					t.Fatal(err)
				}
				want := i18n.T(lang, tc.key, tc.args)
				if lang == i18n.En {
					want = tc.wantEN
				}
				if res.Status != http.StatusBadRequest || got.Code != "bad_request" || got.Retryable || got.Error != want {
					t.Fatalf("status=%d body=%s; want error %q", res.Status, res.Body, want)
				}
			})
		}
	}
}

// Without a language carrier the English-origin errors keep their old bytes; a relay without Accept-Language still
// uses the owner's language fallback rather than dropping into the process default.
func TestStartDefaultsPreviewValidationLanguageFallbacks(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"task":{}}`, "task.machine is required"},
		{`{"task":{"machine":"box","role":"typo"}}`, "task.role must be one of worker, leader"},
		{`{"task":{"machine":"box","supervisor":"typo"}}`, "task.supervisor must be one of main, leader, background_supervisor, scheduled, other"},
		{`{"task":{"machine":"box","done_when":-1}}`, "task.done_when must be 0 or more"},
		{`{"task":{"machine":"box"},"caller":{"effort":"typo"}}`, "caller.effort must be one of low, medium, high, xhigh, max"},
	} {
		s, _ := defaultsHub(t)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/start-defaults/preview", strings.NewReader(tc.body))
		req.Header.Set(RoleHeader, roleFull)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		var got struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusBadRequest || got.Error != tc.want {
			t.Fatalf("no-carrier status=%d error=%q, want %q", rec.Code, got.Error, tc.want)
		}
	}
	s, _ := defaultsHub(t)
	res, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{
		Method: http.MethodPost, Path: "/api/v1/assistant/start-defaults/preview", Body: []byte(`{"task":{}}`),
		Header: map[string][]string{RoleHeader: {roleFull}},
	}, func(context.Context) i18n.Lang { return i18n.Ja })
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(res.Body, &got); err != nil {
		t.Fatal(err)
	}
	want := i18n.T(i18n.Ja, "assistant-start_defaults_preview.field_required", i18n.Args{"field": i18n.Verbatim("task.machine")})
	if got.Error != want {
		t.Fatalf("owner-language fallback = %q, want %q", got.Error, want)
	}
}

// Tier-resolution errors used by both resolveStart and preview keep their legacy English Error() text and expose the
// same typed message in every client language.
func TestTierModelResolutionErrorsLocalized(t *testing.T) {
	for _, tc := range []struct {
		name, tier, key, wantEN string
		args                    i18n.Args
		setup                   func(*testing.T, *Service)
	}{
		{name: "no active profile", tier: "quick", key: "assistant-model_profiles.err_no_active_profile", wantEN: "no model profile is in use: switch to one with model_profile_switch, or leave tier out", setup: func(t *testing.T, s *Service) {
			if err := s.putSetting(context.Background(), modelProfileStateKey, modelProfileState{}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "deleted active profile", tier: "quick", key: "assistant-model_profiles.err_active_profile_missing", wantEN: "the model profile in use (gone) no longer exists: switch to another with model_profile_switch, or leave tier out", args: i18n.Args{"profile": i18n.Verbatim("gone")}, setup: func(t *testing.T, s *Service) {
			putTierProfiles(t, s, "gone", map[string]ModelTier{"quick": {Launcher: "claude"}}, nil)
		}},
		{name: "unknown tier", tier: "typo", key: "assistant-model_profiles.err_tier_not_found", wantEN: "the model profile in use (P) has no tier typo; its tiers: deep, plain, quick", args: i18n.Args{"profile": i18n.Verbatim("P"), "tier": i18n.Verbatim("typo"), "tiers": i18n.Verbatim("deep, plain, quick")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := defaultsHub(t)
			if tc.setup != nil {
				tc.setup(t, s)
			}
			_, err := s.pickTierCandidate(context.Background(), tc.tier, tierFacts{})
			if err == nil || err.Error() != tc.wantEN {
				t.Fatalf("legacy error = %v, want %q", err, tc.wantEN)
			}
			for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
				if got, want := i18n.Localize(err, lang), i18n.T(lang, tc.key, tc.args); got != want {
					t.Fatalf("%s error = %q, want %q", lang, got, want)
				}
			}
		})
	}
}

// The preview honours the caller's explicit values exactly as a start does: a named launcher, model or tier leaves no
// preset to choose, and an explicit effort or ultracode (on or off) is named as winning over the policy's.
func TestStartDefaultsPreviewCallerValuesWin(t *testing.T) {
	s, _ := defaultsHub(t)
	draft := `,"start_defaults":{"rules":[{"applies_when":{},"then":{"candidates":[{"name":"deep"}],"effort":"max","ultracode":true}}]}`
	post := func(caller string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/start-defaults/preview",
			strings.NewReader(`{"task":{"machine":"box"},`+strings.TrimPrefix(caller, ",")+draft+`}`))
		req.Header.Set(RoleHeader, roleFull)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	for _, caller := range []string{`"caller":{"launcher":"claude"}`, `"caller":{"model":"gw-own"}`, `"caller":{"tier":"quick"}`} {
		out := post(caller)
		if out["preset_blocked_by_caller"] != true {
			t.Fatalf("%s did not block the preset: %v", caller, out)
		}
		if _, ok := out["usable"]; ok {
			t.Fatalf("%s still listed usable presets: %v", caller, out)
		}
	}
	out := post(`"caller":{"effort":"low","ultracode":false}`)
	wins, _ := out["caller_wins"].([]any)
	if len(wins) != 2 {
		t.Fatalf("caller_wins = %v", out["caller_wins"])
	}
	// A caller tier that itself names an effort also keeps the policy's effort out.
	out = post(`"caller":{"tier":"deep"}`)
	wins, _ = out["caller_wins"].([]any)
	if !slices.Contains(wins, "effort") {
		t.Fatalf("the tier's effort did not win: %v", out)
	}
}

// Preview uses the same caller resolution as a real start, including when an explicit launcher accompanies a bad tier
// or model. Validation errors are 400; a storage failure stays an operational 500 and none of these paths writes state.
func TestStartDefaultsPreviewCallerResolutionMatchesStart(t *testing.T) {
	for _, tc := range []struct {
		name, caller string
		params       map[string]any
	}{
		{name: "unknown tier", caller: `{"tier":"typo"}`, params: map[string]any{"tier": "typo"}},
		{name: "invalid model", caller: `{"model":"--bad"}`, params: map[string]any{"model": "--bad"}},
		{name: "launcher and unknown tier", caller: `{"launcher":"claude","tier":"typo"}`, params: map[string]any{"launcher": "claude", "tier": "typo"}},
		{name: "launcher and invalid model", caller: `{"launcher":"claude","model":"--bad"}`, params: map[string]any{"launcher": "claude", "model": "--bad"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := defaultsHub(t)
			before, err := json.Marshal(s.settings(context.Background()))
			if err != nil {
				t.Fatal(err)
			}
			_, realErr := s.resolveStart(context.Background(), tc.params, false)
			if realErr == nil {
				t.Fatal("real resolution unexpectedly accepted the caller")
			}
			body := `{"task":{"machine":"box"},"caller":` + tc.caller + `,"start_defaults":{"rules":[{"applies_when":{},"then":{"effort":"max"}}]}}`
			code, out := postStartDefaultsPreview(t, s, body)
			if code != http.StatusBadRequest || out["code"] != "bad_request" || out["error"] != realErr.Error() {
				t.Fatalf("preview status=%d body=%v; real error=%v", code, out, realErr)
			}
			assertStartDefaultsPreviewNoSideEffects(t, s, f, before)
		})
	}
	t.Run("profile read failure is not a caller error", func(t *testing.T) {
		s, f := defaultsHub(t)
		before, err := json.Marshal(s.settings(context.Background()))
		if err != nil {
			t.Fatal(err)
		}
		s.store().(*profileTaskStore).failKey(modelProfilesKey, errors.New("profile read failed"))
		code, out := postStartDefaultsPreview(t, s, `{"task":{"machine":"box"},"caller":{"tier":"quick"}}`)
		if code != http.StatusInternalServerError || !strings.Contains(out["error"].(string), "profile read failed") {
			t.Fatalf("profile read failure: %d %v", code, out)
		}
		assertStartDefaultsPreviewNoSideEffects(t, s, f, before)
	})
}

// External adapter refusals are deterministic caller/target failures, not internal server errors. Keep this
// classification separate from storage and configuration-read failures.
func TestStartDefaultsPreviewExternalRefusalsAreBadRequests(t *testing.T) {
	for _, key := range []string{
		"purpose_unsupported", "target_not_found", "credentials_missing", "platform_unsupported", "runtime_unsupported",
	} {
		if got := startDefaultsPreviewErrorStatus(i18n.Err("external-adapter." + key)); got != http.StatusBadRequest {
			t.Errorf("%s: status=%d, want 400", key, got)
		}
	}
	if got := startDefaultsPreviewErrorStatus(i18n.Err("assistant-models.err_no_database")); got != http.StatusInternalServerError {
		t.Errorf("database read failure: status=%d, want 500", got)
	}
}

// The built-in Codex goal is omitted for launchers whose runtime cannot accept /goal. An explicit rule goal remains
// visible, matching applyStartGoal's default-only refusal behavior.
func TestStartDefaultsPreviewCodexAppGoalRefusal(t *testing.T) {
	s, f := defaultsHub(t)
	f.zshrc["box"] = append(f.zshrc["box"], proto.Launcher{Name: "codex-app", Source: "codex_app", Agent: "codex"})
	code, out := postStartDefaultsPreview(t, s, `{"task":{"machine":"box","done_when":2},"caller":{"launcher":"codex-app"}}`)
	if code != http.StatusOK {
		t.Fatalf("Codex App preview: %d %v", code, out)
	}
	if _, ok := out["goal_by_default"]; ok {
		t.Fatalf("Codex App preview promised a default goal the real start omits: %v", out)
	}
	code, out = postStartDefaultsPreview(t, s, `{"task":{"machine":"box","done_when":2},"caller":{"launcher":"codex-app"},"start_defaults":{"rules":[{"applies_when":{},"then":{"goal":"done_when"}}]}}`)
	if code != http.StatusOK || out["goal"] != "done_when" {
		t.Fatalf("an explicit goal was suppressed with the default: %d %v", code, out)
	}
	task := defaultsTask()
	task.Launcher = "codex-app"
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["goal"] != nil {
		t.Fatalf("real Codex App start carried a default goal: %v", st)
	}
}

// Effort is validated after fitting, using the selected agent's vocabulary rather than Claude Code's list for everyone.
func TestStartDefaultsPreviewEffortUsesFittedAgentVocabulary(t *testing.T) {
	for _, effort := range []string{"minimal", "ultra"} {
		s, f := codexDefaultsHub(t)
		code, out := postStartDefaultsPreview(t, s, `{"task":{"machine":"box"},"caller":{"launcher":"cx","effort":"`+effort+`"}}`)
		if code != http.StatusOK {
			t.Fatalf("Codex effort %q: %d %v", effort, code, out)
		}
		task := defaultsTask()
		task.Launcher, task.Effort = "cx", effort
		if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
			t.Fatalf("real Codex start rejected %q: %v", effort, err)
		}
		if st := startsOf(f)[0]; st["effort"] != effort {
			t.Fatalf("real Codex effort = %v, want %q", st["effort"], effort)
		}
	}
	s, _ := codexDefaultsHub(t)
	code, out := postStartDefaultsPreview(t, s, `{"task":{"machine":"box"},"caller":{"launcher":"cx","effort":"banana"}}`)
	if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "minimal, low, medium, high, xhigh, max, ultra") {
		t.Fatalf("invalid Codex effort did not list Codex values: %d %v", code, out)
	}
	s, _ = defaultsHub(t)
	code, out = postStartDefaultsPreview(t, s, `{"task":{"machine":"box"},"caller":{"launcher":"claude","effort":"ultra"}}`)
	if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "low, medium, high, xhigh, max") || strings.Contains(out["error"].(string), "minimal") {
		t.Fatalf("Claude effort validation used the wrong vocabulary: %d %v", code, out)
	}
}

// A wrapper whose agent the hub cannot tell (no readable launcher list, no last-seen metadata) is not assumed to be
// Claude Code: a Codex level the real start dispatches unchanged is accepted by the preview too, while a level no
// task agent takes is still refused, and a known Claude launcher keeps its own list.
func TestStartDefaultsPreviewUnknownAgentEffortMatchesDispatch(t *testing.T) {
	for _, effort := range []string{"minimal", "ultra", "max"} {
		s, f := defaultsHub(t)
		f.mu.Lock()
		f.zshrc["box"] = nil // an empty own list: launchersHere reports it unknown, with nothing last seen
		f.mu.Unlock()
		task := defaultsTask()
		task.Launcher, task.Effort = "review-wrapper", effort
		if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
			t.Fatalf("real start of an unknown wrapper with %q: %v", effort, err)
		}
		if st := startsOf(f)[0]; st["launcher"] != "review-wrapper" || st["effort"] != effort {
			t.Fatalf("real start did not dispatch the unknown wrapper with %q: %v", effort, st)
		}
		code, out := postStartDefaultsPreview(t, s, `{"task":{"machine":"box"},"caller":{"launcher":"review-wrapper","effort":"`+effort+`"}}`)
		if code != http.StatusOK {
			t.Fatalf("preview refused %q the real start dispatches to an unknown wrapper: %d %v", effort, code, out)
		}
	}
	s, f := defaultsHub(t)
	f.mu.Lock()
	f.zshrc["box"] = nil
	f.mu.Unlock()
	code, out := postStartDefaultsPreview(t, s, `{"task":{"machine":"box"},"caller":{"launcher":"review-wrapper","effort":"banana"}}`)
	if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "minimal, low, medium, high, xhigh, max, ultra") {
		t.Fatalf("a level no task agent takes was accepted for an unknown wrapper: %d %v", code, out)
	}
	s, _ = defaultsHub(t)
	code, out = postStartDefaultsPreview(t, s, `{"task":{"machine":"box"},"caller":{"launcher":"claude","effort":"minimal"}}`)
	if code != http.StatusBadRequest || strings.Contains(out["error"].(string), "minimal") {
		t.Fatalf("a known Claude launcher accepted a Codex-only level: %d %v", code, out)
	}
}

// A rule's ultracode false is never applied (applyStartExtras acts only on true), so the preview does not show it as
// the start's value: with the caller's explicit true the real start still sends true, and the preview neither says
// false nor claims a caller win over a value the rule never sets. A rule's true still shows, and loses to the caller.
func TestStartDefaultsPreviewFalseUltracodeIsNotShownOverCaller(t *testing.T) {
	s, f := defaultsHub(t)
	policy := `{"start_defaults":{"rules":[{"applies_when":{},"then":{"ultracode":false}}]}}`
	if code, out := putStartDefaults(t, s, roleFull, "", policy); code != http.StatusOK {
		t.Fatalf("policy: %d %s", code, out)
	}
	for _, caller := range []string{`,"caller":{"ultracode":true}`, `,"caller":{"ultracode":false}`, ``} {
		code, out := postStartDefaultsPreview(t, s, `{"task":{"machine":"box"}`+caller+`}`)
		if code != http.StatusOK || out["matched"] != true {
			t.Fatalf("preview %s: %d %v", caller, code, out)
		}
		if v, ok := out["ultracode"]; ok {
			t.Fatalf("preview %s showed the rule's inert ultracode %v", caller, v)
		}
		if _, ok := out["caller_wins"]; ok {
			t.Fatalf("preview %s claimed a caller win over a rule that sets nothing: %v", caller, out)
		}
	}
	task := defaultsTask()
	on := true
	task.Ultracode = &on
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["ultracode"] != true {
		t.Fatalf("the rule's false turned off the caller's ultracode: %v", st)
	}
	task = defaultsTask()
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[1]; st["ultracode"] != nil {
		t.Fatalf("the rule's false sent an ultracode: %v", st)
	}
	code, out := postStartDefaultsPreview(t, s, `{"task":{"machine":"box"},"caller":{"ultracode":false},"start_defaults":{"rules":[{"applies_when":{},"then":{"ultracode":true}}]}}`)
	wins, _ := out["caller_wins"].([]any)
	if code != http.StatusOK || out["ultracode"] != true || len(wins) != 1 || wins[0] != "ultracode" {
		t.Fatalf("a rule's ultracode true lost its value or the caller's win: %d %v", code, out)
	}
}

// A matched rule resets the caller effort before validation, just as applyStartPreset trims it for a real start.
func TestStartDefaultsPreviewMatchedRuleTrimsCallerEffort(t *testing.T) {
	s, f := defaultsHub(t)
	body := `{"task":{"machine":"box"},"caller":{"effort":" high "},"start_defaults":{"rules":[{"applies_when":{},"then":{"effort":"max"}}]}}`
	code, out := postStartDefaultsPreview(t, s, body)
	if code != http.StatusOK {
		t.Fatalf("padded caller effort with a matched rule: %d %v", code, out)
	}
	policy := `{"start_defaults":{"rules":[{"applies_when":{},"then":{"effort":"max"}}]}}`
	if code, out := putStartDefaults(t, s, roleFull, "", policy); code != http.StatusOK {
		t.Fatalf("store effort policy: %d %s", code, out)
	}
	task := defaultsTask()
	task.Effort = " high "
	if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["effort"] != "high" {
		t.Fatalf("real start effort = %v, want high", st["effort"])
	}
}

// With no active profile every named tier is unknown, but its passed-over reason is still part of preview.
func TestStartDefaultsPreviewNoProfileKeepsPassedOverReason(t *testing.T) {
	s, _ := defaultsHub(t)
	if err := s.putSetting(context.Background(), modelProfileStateKey, modelProfileState{}); err != nil {
		t.Fatal(err)
	}
	code, out := postStartDefaultsPreview(t, s, `{"task":{"machine":"box"},"start_defaults":{"rules":[{"applies_when":{},"then":{"candidates":[{"name":"deep"}]}}]}}`)
	if code != http.StatusOK {
		t.Fatalf("no-profile preview: %d %v", code, out)
	}
	rows, _ := out["passed_over"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["name"] != "deep" || rows[0].(map[string]any)["reason"] != "unknown preset" {
		t.Fatalf("no-profile passed_over = %v", out["passed_over"])
	}
}

// NUL is never accepted into a policy that must be stored in PostgreSQL jsonb.
func TestStartDefaultsCandidateNameRejectsNUL(t *testing.T) {
	s, _ := defaultsHub(t)
	body, err := json.Marshal(map[string]any{"start_defaults": map[string]any{"rules": []any{map[string]any{
		"applies_when": map[string]any{}, "then": map[string]any{"candidates": []any{map[string]any{"name": "\x00"}}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	if code, out := putStartDefaults(t, s, roleFull, "", string(body)); code != http.StatusBadRequest {
		t.Fatalf("NUL candidate name: %d %s", code, out)
	}
}

// Alias conflicts keep their legacy English text without a language carrier and localize through the real relay.
func TestStartDefaultsAliasConflictsLocalized(t *testing.T) {
	for _, tc := range []struct {
		name, body, key string
	}{
		{name: "match", body: `{"start_defaults":{"rules":[{"when":null,"applies_when":{},"then":{"effort":"max"}}]}}`, key: "assistant-start_defaults.alias_match_conflict"},
		{name: "candidates", body: `{"start_defaults":{"rules":[{"when":{},"then":{"presets":null,"candidates":[],"effort":"max"}}]}}`, key: "assistant-start_defaults.alias_candidates_conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := defaultsHub(t)
			res, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{
				Method: http.MethodPut, Path: "/api/v1/assistant/start-defaults", Body: []byte(tc.body),
				Header: map[string][]string{RoleHeader: {roleFull}, "Accept-Language": {string(i18n.Ja)}},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Code  string `json:"code"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(res.Body, &got); err != nil {
				t.Fatal(err)
			}
			want := i18n.T(i18n.Ja, tc.key)
			if res.Status != http.StatusBadRequest || got.Code != "bad_request" || got.Error != want {
				t.Fatalf("status=%d body=%s; want error %q", res.Status, res.Body, want)
			}
		})
	}
}

// An engine's deterministic no-provider refusal is a bad request too, and nothing is started or claimed.
func TestStartDefaultsPreviewEngineLauncherRefusal(t *testing.T) {
	s, f := paseoFitHub(t)
	before, err := json.Marshal(s.settings(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	code, out := postStartDefaultsPreview(t, s, `{"task":{"machine":"pbox"},"caller":{"launcher":"traex"}}`)
	if code != http.StatusBadRequest || out["code"] != "bad_request" {
		t.Fatalf("engine launcher refusal: %d %v", code, out)
	}
	assertStartDefaultsPreviewNoSideEffects(t, s, f, before)
}

// The two caller-validation examples are also typed, localized errors through the real relay.
func TestStartDefaultsPreviewCallerErrorsLocalized(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		for _, tc := range []struct {
			name, caller, key string
			args              i18n.Args
		}{
			{name: "unknown tier", caller: `{"tier":"typo"}`, key: "assistant-model_profiles.err_tier_not_found", args: i18n.Args{"profile": i18n.Verbatim("P"), "tier": i18n.Verbatim("typo"), "tiers": i18n.Verbatim("deep, plain, quick")}},
			{name: "invalid model", caller: `{"model":"--bad"}`, key: "assistant-models.err_model_invalid", args: i18n.Args{"model": strconv.Quote("--bad")}},
		} {
			t.Run(string(lang)+"/"+tc.name, func(t *testing.T) {
				s, _ := defaultsHub(t)
				body := `{"task":{"machine":"box"},"caller":` + tc.caller + `,"start_defaults":{"rules":[{"applies_when":{},"then":{"effort":"max"}}]}}`
				res, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{
					Method: http.MethodPost, Path: "/api/v1/assistant/start-defaults/preview", Body: []byte(body),
					Header: map[string][]string{RoleHeader: {roleFull}, "Accept-Language": {string(lang)}},
				}, nil)
				if err != nil {
					t.Fatal(err)
				}
				var got struct {
					Code  string `json:"code"`
					Error string `json:"error"`
				}
				if err := json.Unmarshal(res.Body, &got); err != nil {
					t.Fatal(err)
				}
				want := i18n.T(lang, tc.key, tc.args)
				if res.Status != http.StatusBadRequest || got.Code != "bad_request" || got.Error != want {
					t.Fatalf("status=%d body=%s; want error %q", res.Status, res.Body, want)
				}
			})
		}
	}
}

// applyStartPreset trims the task's blank caller fields before resolving whenever a rule matches. Preview performs the
// same reset only on a matched path; without a rule the unknown blank tier must reach the same real resolver error.
func TestStartDefaultsPreviewBlankCallerResetParity(t *testing.T) {
	caller := `{"launcher":" ","tier":" "}`
	t.Run("matched extras-only rule resets blank caller", func(t *testing.T) {
		s, f := defaultsHub(t)
		before, err := json.Marshal(s.settings(context.Background()))
		if err != nil {
			t.Fatal(err)
		}
		body := `{"task":{"machine":"box"},"caller":` + caller + `,"start_defaults":{"rules":[{"applies_when":{},"then":{"effort":"max"}}]}}`
		code, out := postStartDefaultsPreview(t, s, body)
		if code != http.StatusOK || out["effort"] != "max" {
			t.Fatalf("blank caller with a matched rule: %d %v", code, out)
		}
		assertStartDefaultsPreviewNoSideEffects(t, s, f, before)
		policy := `{"start_defaults":{"rules":[{"applies_when":{},"then":{"effort":"max"}}]}}`
		if code, out := putStartDefaults(t, s, roleFull, "", policy); code != http.StatusOK {
			t.Fatalf("store the matched rule for the real-start comparison: %d %s", code, out)
		}
		task := defaultsTask()
		task.Launcher, task.Tier = " ", " "
		if _, _, err := s.startTaskAgent(context.Background(), &task, false); err != nil {
			t.Fatalf("real start rejected a blank caller that applyStartPreset should reset: %v", err)
		}
		if st := startsOf(f)[0]; st["effort"] != "max" {
			t.Fatalf("real start did not keep the matched extras: %v", st)
		}
	})
	t.Run("no rule leaves blank caller validation to the real resolver", func(t *testing.T) {
		s, f := defaultsHub(t)
		before, err := json.Marshal(s.settings(context.Background()))
		if err != nil {
			t.Fatal(err)
		}
		body := `{"task":{"machine":"box"},"caller":` + caller + `}`
		code, out := postStartDefaultsPreview(t, s, body)
		if code != http.StatusBadRequest || out["code"] != "bad_request" {
			t.Fatalf("blank caller without a rule: %d %v", code, out)
		}
		assertStartDefaultsPreviewNoSideEffects(t, s, f, before)
		task := defaultsTask()
		task.Launcher, task.Tier = " ", " "
		if _, _, err := s.startTaskAgent(context.Background(), &task, false); err == nil {
			t.Fatal("real start unexpectedly accepted a blank tier without a matching rule")
		}
	})
}

// A tier-inherited speed is validated in preview, for both a caller's tier and a sampled candidate, exactly as a real
// plan/start would validate it. Fast succeeds on Claude; ultrafast is a stable refusal and starts nothing.
func TestStartDefaultsPreviewTierSpeedValidation(t *testing.T) {
	for _, origin := range []string{"caller", "candidate"} {
		t.Run(origin, func(t *testing.T) {
			for _, tc := range []struct {
				name, speed string
				wantStatus  int
			}{
				{name: "fast is supported", speed: "fast", wantStatus: http.StatusOK},
				{name: "ultrafast is refused", speed: "ultrafast", wantStatus: http.StatusBadRequest},
			} {
				t.Run(tc.name, func(t *testing.T) {
					s, f := defaultsHub(t)
					saveTiers(t, s, map[string]ModelTier{"speed-tier": {Launcher: "gateway", Model: "gw-speed", Speed: tc.speed}})
					before, err := json.Marshal(s.settings(context.Background()))
					if err != nil {
						t.Fatal(err)
					}
					var body, policy string
					if origin == "caller" {
						body = `{"task":{"machine":"box"},"caller":{"tier":"speed-tier"}}`
					} else {
						policy = `{"start_defaults":{"rules":[{"applies_when":{},"then":{"candidates":[{"name":"speed-tier"}]}}]}}`
						body = `{"task":{"machine":"box"},"start_defaults":` + strings.TrimPrefix(policy, `{"start_defaults":`)
					}
					code, out := postStartDefaultsPreview(t, s, body)
					if code != tc.wantStatus {
						t.Fatalf("preview status=%d body=%v, want %d", code, out, tc.wantStatus)
					}
					if tc.wantStatus == http.StatusOK && origin == "candidate" && out["sample"] != "speed-tier" {
						t.Fatalf("the sampled speed tier was not applied: %v", out)
					}
					assertStartDefaultsPreviewNoSideEffects(t, s, f, before)
					if origin == "candidate" {
						if code, out := putStartDefaults(t, s, roleFull, "", policy); code != http.StatusOK {
							t.Fatalf("store speed policy for the real-start comparison: %d %s", code, out)
						}
					}
					task := defaultsTask()
					if origin == "caller" {
						task.Tier = "speed-tier"
					}
					_, _, err = s.startTaskAgent(context.Background(), &task, false)
					if tc.wantStatus == http.StatusOK {
						if err != nil {
							t.Fatalf("real start rejected supported speed: %v", err)
						}
						if st := startsOf(f)[0]; st["speed"] != tc.speed {
							t.Fatalf("real start speed = %v, want %q", st["speed"], tc.speed)
						}
						return
					}
					if err == nil || !strings.Contains(err.Error(), "cannot apply speed") {
						t.Fatalf("real start error = %v, want a speed refusal", err)
					}
					if len(startsOf(f)) != 0 {
						t.Fatalf("an unsupported-speed preview/start started an Agent: %v", startsOf(f))
					}
				})
			}
		})
	}
}

// Speed validation is shared with real starts, so its legacy English text remains byte-identical while the same typed
// errors localize for preview callers. This also prevents a new English-only error from slipping in with tier fitting.
func TestStartSpeedErrorsLocalized(t *testing.T) {
	for _, tc := range []struct {
		name, key, wantEN string
		args              i18n.Args
		call              func() error
	}{
		{name: "type", key: "assistant-tasks.speed_type", wantEN: "speed must be a string: standard, fast or ultrafast", call: func() error {
			_, err := validateStartSpeed(map[string]any{"speed": true})
			return err
		}},
		{name: "invalid", key: "assistant-tasks.speed_invalid", wantEN: `invalid Agent speed "warp": use standard, fast or ultrafast`, args: i18n.Args{"speed": i18n.Verbatim(`"warp"`)}, call: func() error {
			_, err := validateStartSpeed(map[string]any{"speed": "warp"})
			return err
		}},
		{name: "resume", key: "assistant-tasks.speed_resume", wantEN: `speed "fast" applies only to a new Agent start, not a resume; leave speed out to use the launcher's own setting`, args: i18n.Args{"speed": i18n.Verbatim(`"fast"`)}, call: func() error {
			return checkStartSpeed(map[string]any{"speed": "fast"}, startAgent{agent: "claude"}, "session-1")
		}},
		{name: "unsupported", key: "assistant-tasks.speed_unsupported", wantEN: `launcher "gateway" (claude) cannot apply speed "ultrafast" to a new Agent start; leave speed out or choose standard`, args: i18n.Args{"launcher": i18n.Verbatim(`"gateway"`), "kind": i18n.Verbatim("claude"), "speed": i18n.Verbatim(`"ultrafast"`)}, call: func() error {
			return checkStartSpeed(map[string]any{"speed": "ultrafast", "launcher": "gateway"}, startAgent{agent: "claude"}, "")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil || err.Error() != tc.wantEN {
				t.Fatalf("legacy error = %v, want %q", err, tc.wantEN)
			}
			for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
				if got, want := i18n.Localize(err, lang), i18n.T(lang, tc.key, tc.args); got != want {
					t.Fatalf("%s error = %q, want %q", lang, got, want)
				}
			}
		})
	}
}

// A tier-inherited speed refusal is localized through the actual preview relay, not only when calling the helper.
func TestStartDefaultsPreviewSpeedErrorLocalized(t *testing.T) {
	s, _ := defaultsHub(t)
	saveTiers(t, s, map[string]ModelTier{"speed-tier": {Launcher: "gateway", Model: "gw-speed", Speed: "ultrafast"}})
	body := `{"task":{"machine":"box"},"start_defaults":{"rules":[{"applies_when":{},"then":{"candidates":[{"name":"speed-tier"}]}}]}}`
	res, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{
		Method: http.MethodPost, Path: "/api/v1/assistant/start-defaults/preview", Body: []byte(body),
		Header: map[string][]string{RoleHeader: {roleFull}, "Accept-Language": {string(i18n.Ja)}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Code  string `json:"code"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(res.Body, &got); err != nil {
		t.Fatal(err)
	}
	want := i18n.T(i18n.Ja, "assistant-tasks.speed_unsupported", i18n.Args{
		"launcher": i18n.Verbatim(`"gateway"`), "kind": i18n.Verbatim("claude"), "speed": i18n.Verbatim(`"ultrafast"`),
	})
	if res.Status != http.StatusBadRequest || got.Code != "bad_request" || got.Error != want {
		t.Fatalf("status=%d body=%s; want error %q", res.Status, res.Body, want)
	}
}

// A coded fit refusal retains its code and structured launcher data, localized by the ordinary apierr path.
func TestStartDefaultsPreviewReadonlyRefusalKeepsCode(t *testing.T) {
	s, f := defaultsHub(t)
	f.zshrc["box"] = append(f.zshrc["box"], proto.Launcher{Name: "pi", Source: "zshrc", Agent: "pi"})
	before, err := json.Marshal(s.settings(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	res, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{
		Method: http.MethodPost, Path: "/api/v1/assistant/start-defaults/preview",
		Body:   []byte(`{"task":{"machine":"box","readonly":true},"caller":{"launcher":"pi"}}`),
		Header: map[string][]string{RoleHeader: {roleFull}, "Accept-Language": {string(i18n.Ja)}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(res.Body, &got); err != nil {
		t.Fatal(err)
	}
	if res.Status != http.StatusBadRequest || got["code"] != "readonly_unsupported" || got["launcher"] != "pi" || got["delivered"] != "none" {
		t.Fatalf("readonly refusal lost its contract: %d %v", res.Status, got)
	}
	if _, ok := got["launchers"].(string); !ok {
		t.Fatalf("readonly refusal lost its launcher choices: %v", got)
	}
	if want := i18n.T(i18n.Ja, "rpc-errors.readonly_unsupported"); got["error"] != want {
		t.Fatalf("readonly refusal error = %q, want %q", got["error"], want)
	}
	assertStartDefaultsPreviewNoSideEffects(t, s, f, before)
}

// boolp is a pointer to b, for the retry test's ultracode.
func boolp(b bool) *bool { return &b }

// The candidate source seam: an injected source replaces the profile's data without touching matching or retry, and a
// preview and a real start read the same rows. A source that cannot be read leaves no preset, as the profile's error
// always has.
func TestStartDefaultsCandidateSourceSeam(t *testing.T) {
	ctx := context.Background()
	s, f := defaultsHub(t)
	var requested [][]string
	s.startCandidates = stubCandidateSource{requested: &requested, rows: map[string]startCandidate{
		"deep":  {Name: "deep", Launcher: "gateway", Available: false, Reason: "gateway is not available yet"},
		"quick": {Name: "quick", Launcher: "gateway", Available: true},
	}}
	stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
		return StartDefaults{Presets: []PresetChoice{{Name: "deep", Priority: 5}, {Name: "quick", Priority: 1}}}, true
	})
	task := defaultsTask()
	if _, _, err := s.startTaskAgent(ctx, &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f)[0]; st["model"] != "gw-quick" {
		t.Fatalf("the injected source was not used: %v", st)
	}
	if want := [][]string{{"deep", "quick"}}; !reflect.DeepEqual(requested, want) {
		t.Fatalf("real start requested candidates %v, want %v", requested, want)
	}
	// An unreadable source keeps the old fail-closed behavior: no preset, the ordinary start, no error.
	s2, f2 := defaultsHub(t)
	s2.startCandidates = stubCandidateSource{err: context.DeadlineExceeded}
	stubStartDefaults(s2, func(StartFacts) (StartDefaults, bool) {
		return StartDefaults{Presets: []PresetChoice{{Name: "deep"}}}, true
	})
	task = defaultsTask()
	if _, _, err := s2.startTaskAgent(ctx, &task, false); err != nil {
		t.Fatal(err)
	}
	if st := startsOf(f2)[0]; st["model"] != "gw-model" {
		t.Fatalf("an unreadable source did not fall back to the ordinary start: %v", st)
	}
}

// stubCandidateSource is a fixed candidate source for the seam test; err makes it unreadable.
type stubCandidateSource struct {
	rows      map[string]startCandidate
	err       error
	requested *[][]string
}

func (s stubCandidateSource) candidates(_ context.Context, _ string, names []string) (map[string]startCandidate, error) {
	if s.requested != nil {
		*s.requested = append(*s.requested, slices.Clone(names))
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.rows, nil
}

// Candidate evaluation is bounded by the rule, not by unrelated profile tiers. One ordinary inventory read covers
// visible choices; explicitly requested hidden launchers get at most one unhidden read per distinct launcher.
func TestStartDefaultsCandidateSourceScopesAndCachesHidden(t *testing.T) {
	for _, tc := range []struct {
		name          string
		secretPresent bool
		choices       []PresetChoice
		wantTier      string
		wantRPCs      int
	}{
		{name: "visible choice ignores unrelated hidden tiers", secretPresent: true, choices: []PresetChoice{{Name: "quick"}}, wantTier: "quick", wantRPCs: 1},
		{name: "requested tiers share a present hidden probe", secretPresent: true, choices: []PresetChoice{{Name: "hidden-a"}, {Name: "hidden-b"}}, wantTier: "hidden-a", wantRPCs: 1},
		{name: "requested tiers share an absent hidden probe", secretPresent: false, choices: []PresetChoice{{Name: "hidden-a"}, {Name: "hidden-b"}}, wantRPCs: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := defaultsHub(t)
			ctx := context.Background()
			if tc.secretPresent {
				f.zshrc["box"] = append(f.zshrc["box"], proto.Launcher{Name: "secret", Source: "zshrc"})
			}
			if err := s.putSetting(ctx, launchersHiddenKey, []string{"secret"}); err != nil {
				t.Fatal(err)
			}
			tiers := map[string]ModelTier{
				"quick":    {Launcher: "gateway", Model: "gw-quick"},
				"hidden-a": {Launcher: "secret", Model: "secret-a"},
				"hidden-b": {Launcher: "secret", Model: "secret-b"},
			}
			for i := 0; i < 11; i++ {
				tiers[fmt.Sprintf("unrelated-%d", i)] = ModelTier{Launcher: "secret", Model: "secret-other"}
			}
			saveTiers(t, s, tiers)
			f.mu.Lock()
			before := len(f.calls)
			f.mu.Unlock()
			task := defaultsTask()
			params := map[string]any{}
			s.applyStartPreset(ctx, &task, startPick{}, StartDefaults{Presets: tc.choices}, params)
			f.mu.Lock()
			calls := 0
			for _, call := range f.calls[before:] {
				if call.Method == DeviceLaunchersList {
					calls++
				}
			}
			f.mu.Unlock()
			if got, _ := params["tier"].(string); got != tc.wantTier {
				t.Fatalf("selected tier %q, want %q", got, tc.wantTier)
			}
			if calls != tc.wantRPCs {
				t.Fatalf("launcher-list RPCs = %d, want %d", calls, tc.wantRPCs)
			}
		})
	}
}

// Hidden usability is cached per launcher, only for one evaluation. Opposite outcomes stay independent, and a later
// evaluation sees a launcher added after an earlier miss instead of inheriting a stale Service-wide false.
func TestStartDefaultsHiddenCandidateCacheIsPerLauncherAndAttempt(t *testing.T) {
	s, f := defaultsHub(t)
	ctx := context.Background()
	f.zshrc["box"] = append(f.zshrc["box"], proto.Launcher{Name: "present", Source: "zshrc"})
	if err := s.putSetting(ctx, launchersHiddenKey, []string{"present", "absent"}); err != nil {
		t.Fatal(err)
	}
	saveTiers(t, s, map[string]ModelTier{
		"present": {Launcher: "present", Model: "present-model"},
		"absent":  {Launcher: "absent", Model: "absent-model"},
	})
	f.mu.Lock()
	before := len(f.calls)
	f.mu.Unlock()
	rows, err := s.candidateSource().candidates(ctx, "box", []string{"present", "absent"})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	calls := 0
	for _, call := range f.calls[before:] {
		if call.Method == DeviceLaunchersList {
			calls++
		}
	}
	f.mu.Unlock()
	if calls != 1 {
		t.Fatalf("two distinct hidden launchers caused %d RPCs, want 1", calls)
	}
	if !rows["present"].Available || rows["absent"].Available || !strings.Contains(rows["absent"].Reason, "absent") {
		t.Fatalf("hidden availability leaked between launchers: %+v", rows)
	}

	rows, err = s.candidateSource().candidates(ctx, "box", []string{"absent"})
	if err != nil {
		t.Fatal(err)
	}
	if rows["absent"].Available {
		t.Fatalf("absent launcher unexpectedly became usable: %+v", rows["absent"])
	}
	f.mu.Lock()
	f.zshrc["box"] = append(f.zshrc["box"], proto.Launcher{Name: "absent", Source: "zshrc"})
	f.mu.Unlock()
	s.invalidateLauncherListCache("box")
	rows, err = s.candidateSource().candidates(ctx, "box", []string{"absent"})
	if err != nil {
		t.Fatal(err)
	}
	if !rows["absent"].Available {
		t.Fatalf("a later attempt reused the earlier attempt's false: %+v", rows["absent"])
	}
}

// The owner's own write still may omit the expiry, and an extension's still may not: the converged names did not
// loosen that guard, and a policy past its expiry matches nothing.
func TestStartDefaultsConvergedExpiryUnchanged(t *testing.T) {
	s, _ := defaultsHub(t)
	rule := `{"applies_when":{},"then":{"candidates":[{"name":"deep"}]}}`
	if code, got := putStartDefaults(t, s, roleExtension, "kit", `{"start_defaults":{"rules":[`+rule+`]}}`); code != http.StatusBadRequest {
		t.Fatalf("an extension's policy without expiry was accepted: %d %s", code, got)
	}
	soon := time.Now().Add(20 * time.Minute).UTC().Format(time.RFC3339)
	if code, got := putStartDefaults(t, s, roleExtension, "kit", `{"start_defaults":{"expires_at":"`+soon+`","rules":[`+rule+`]}}`); code != http.StatusOK {
		t.Fatalf("an expiring converged policy was refused: %d %s", code, got)
	}
	past := time.Now().Add(-time.Minute)
	setSettings(t, s, func(v *Settings) { v.StartDefaults.ExpiresAt = &past })
	if p := s.activeStartDefaults(context.Background()); p != nil {
		t.Fatalf("an expired policy is still in force: %+v", p)
	}
}

// A policy at the very top of every bound must still round-trip through the dual-name GET within the write body
// limit: 16 rules of 16 candidates each, every name 64 runes, every priority and weight at its maximum, and every
// supervisor class. The write is single-name (applies_when / candidates only), the read answers both names, and that
// read is itself a valid write body — the single-name-write → dual-name-read → write-back path the unbounded
// supervisor array used to break.
func TestStartDefaultsRoundTripStaysWithinBodyLimit(t *testing.T) {
	s, _ := defaultsHub(t)
	rules := make([]map[string]any, 0, startDefaultsMaxRules)
	for i := 0; i < startDefaultsMaxRules; i++ {
		cands := make([]map[string]any, 0, startDefaultsMaxPresets)
		for j := 0; j < startDefaultsMaxPresets; j++ {
			// '<' takes six JSON bytes per rune and, unlike NUL, is valid in PostgreSQL jsonb storage.
			cands = append(cands, map[string]any{"name": strings.Repeat("<", 64), "priority": startDefaultsMaxPriority, "weight": startDefaultsMaxWeight})
		}
		rules = append(rules, map[string]any{
			"applies_when": map[string]any{"role": "worker", "supervisor": []string{"main", "leader", "background_supervisor", "scheduled", "other"}},
			"then":         map[string]any{"candidates": cands, "effort": "max"},
		})
	}
	// Built as plain maps, so the body really is single-name (the custom marshaler would have written both names).
	body, err := json.Marshal(map[string]any{"start_defaults": map[string]any{"rules": rules}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"when"`) || strings.Contains(string(body), `"presets"`) {
		t.Fatal("the initial write is not single-name")
	}
	if code, got := putStartDefaults(t, s, roleFull, "", string(body)); code != http.StatusOK {
		t.Fatalf("the maximum single-name policy was refused: %d %s", code, got[:200])
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant/start-defaults", nil))
	got := rec.Body.Bytes()
	if len(got) >= startDefaultsBodyLimit {
		t.Fatalf("the dual-name read is %d bytes, at or over the %d-byte write limit; a GET-to-PUT round-trip would break",
			len(got), startDefaultsBodyLimit)
	}
	// The read carries both names with equal values, so it is itself accepted as a write (this is what an older or
	// newer client does when it saves the policy back untouched).
	if code, out := putStartDefaults(t, s, roleFull, "", string(got)); code != http.StatusOK {
		t.Fatalf("the dual-name read could not be written back: %d %s", code, out[:200])
	}
}

// Candidate names are canonical on every decode path. In particular, unbounded outer whitespace cannot be accepted
// once and then duplicated under presets and candidates until the unchanged GET no longer fits the PUT body limit.
func TestStartDefaultsCandidateNameCanonicalizationRoundTrips(t *testing.T) {
	padded := strings.Repeat(" ", 140*1024) + "deep \n\t"
	write := func(t *testing.T, s *Service, route, body string) {
		t.Helper()
		if route == "dedicated" {
			if code, got := putStartDefaults(t, s, roleFull, "", body); code != http.StatusOK {
				t.Fatalf("dedicated write: %d %.200s", code, got)
			}
			return
		}
		req := httptest.NewRequest(http.MethodPut, "/api/v1/assistant", strings.NewReader(body))
		req.Header.Set(RoleHeader, roleFull)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("nested write: %d %.200s", rec.Code, rec.Body.String())
		}
	}
	check := func(t *testing.T, s *Service, route string) {
		t.Helper()
		if p := s.activeStartDefaults(context.Background()); p == nil || p.Rules[0].Then.Presets[0].Name != "deep" {
			t.Fatalf("candidate name was not canonicalized: %+v", p)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant/start-defaults", nil))
		if rec.Code != http.StatusOK || rec.Body.Len() >= startDefaultsBodyLimit {
			t.Fatalf("canonical GET: status=%d bytes=%d limit=%d", rec.Code, rec.Body.Len(), startDefaultsBodyLimit)
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		then := got["start_defaults"].(map[string]any)["rules"].([]any)[0].(map[string]any)["then"].(map[string]any)
		for _, alias := range []string{"presets", "candidates"} {
			rows := then[alias].([]any)
			if len(rows) != 1 || rows[0].(map[string]any)["name"] != "deep" {
				t.Fatalf("%s retained a padded name: %v", alias, rows)
			}
		}
		t.Logf("route=%s dual-name GET bytes=%d limit=%d", route, rec.Body.Len(), startDefaultsBodyLimit)
		write(t, s, route, rec.Body.String())
	}
	body, err := json.Marshal(map[string]any{"start_defaults": map[string]any{"rules": []any{map[string]any{
		"applies_when": map[string]any{},
		"then":         map[string]any{"candidates": []any{map[string]any{"name": padded}}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"dedicated", "nested"} {
		t.Run(route, func(t *testing.T) {
			s, _ := defaultsHub(t)
			write(t, s, route, string(body))
			check(t, s, route)
		})
	}
	t.Run("legacy stored policy", func(t *testing.T) {
		s, _ := defaultsHub(t)
		ctx := context.Background()
		raw, err := json.Marshal(map[string]any{
			"notes": "preserve me",
			"start_defaults": map[string]any{"by": "owner", "rules": []any{map[string]any{
				"when": map[string]any{"role": "worker", "readonly": false},
				"then": map[string]any{"presets": []any{map[string]any{"name": padded, "weight": 2}}, "ultracode": false},
			}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.store().PutSetting(ctx, settingsKey, json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
		s.prefs.mu.Lock()
		s.prefs.at = time.Time{}
		s.prefs.mu.Unlock()
		v, ok := s.savedSettings(ctx)
		if !ok || v.Notes == nil || *v.Notes != "preserve me" || v.StartDefaults == nil {
			t.Fatalf("legacy settings did not survive: %+v", v)
		}
		rule := v.StartDefaults.Rules[0]
		if rule.When.Role != "worker" || rule.When.Readonly == nil || *rule.When.Readonly || rule.Then.Ultracode == nil || *rule.Then.Ultracode || rule.Then.Presets[0].Weight != 2 {
			t.Fatalf("legacy fields changed: %+v", rule)
		}
		check(t, s, "dedicated")
	})
}

// The preview answers what a real start would do, not just what the policy literally says: an omitted role is a
// worker (roleOf), the goal follows startGoalSource (nothing without criteria, Codex's default when no rule names
// one, off turning even that off), and effort/ultracode reach Claude Code only. Candidate rows come from the same
// injected source a start would read.
func TestStartDefaultsPreviewMatchesRealStartSemantics(t *testing.T) {
	s, _ := defaultsHub(t)
	post := func(body string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/start-defaults/preview", strings.NewReader(body))
		req.Header.Set(RoleHeader, roleFull)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	// An omitted role is a worker: a worker-scoped rule matches a preview that never names a role.
	out := post(`{"task":{"machine":"box"},"start_defaults":{"rules":[{"applies_when":{"role":"worker"},"then":{"effort":"max"}}]}}`)
	if out["matched"] != true || out["effort"] != "max" {
		t.Fatalf("an omitted role did not match the worker rule: %v", out)
	}
	// goal done_when with no criteria: a real start sets no goal, so the preview must not echo it.
	out = post(`{"task":{"machine":"box","done_when":0},"start_defaults":{"rules":[{"applies_when":{},"then":{"goal":"done_when"}}]}}`)
	if _, ok := out["goal"]; ok {
		t.Fatalf("a goal was previewed with no acceptance criteria: %v", out)
	}
	// The same rule with criteria does set it.
	out = post(`{"task":{"machine":"box","done_when":2},"start_defaults":{"rules":[{"applies_when":{},"then":{"goal":"done_when"}}]}}`)
	if out["goal"] != "done_when" {
		t.Fatalf("the goal was not previewed with criteria: %v", out)
	}
	// No fitting rule: Claude Code takes no default goal (the Codex cases are in TestStartDefaultsPreviewCodexAgent).
	out = post(`{"task":{"machine":"box","done_when":2}}`)
	if _, ok := out["goal_by_default"]; ok {
		t.Fatalf("Claude Code should take no default goal: %v", out)
	}
	// The preview reads the injected candidate source, the same one a start uses.
	var requested [][]string
	s.startCandidates = stubCandidateSource{requested: &requested, rows: map[string]startCandidate{
		"deep":  {Name: "deep", Launcher: "gateway", Available: false, Reason: "gateway is not available yet"},
		"quick": {Name: "quick", Launcher: "gateway", Available: true},
	}}
	out = post(`{"task":{"machine":"box"},"start_defaults":{"rules":[{"applies_when":{},"then":{"candidates":[{"name":"deep"},{"name":"quick"}]}}]}}`)
	usable, _ := out["usable"].([]any)
	if len(usable) != 1 || usable[0].(map[string]any)["name"] != "quick" {
		t.Fatalf("the preview did not use the injected candidate source: %v", out["usable"])
	}
	passed, _ := out["passed_over"].([]any)
	if len(passed) != 1 || passed[0].(map[string]any)["name"] != "deep" {
		t.Fatalf("the preview did not report the injected source's unavailable row: %v", out["passed_over"])
	}
	if want := [][]string{{"deep", "quick"}}; !reflect.DeepEqual(requested, want) {
		t.Fatalf("preview requested candidates %v, want %v", requested, want)
	}
}

// The preview writes nothing anywhere in the assistant's settings, not just the policy field, and starts no Agent.
func TestStartDefaultsPreviewLeavesAllSettingsUntouched(t *testing.T) {
	s, f := defaultsHub(t)
	ctx := context.Background()
	setSettings(t, s, func(v *Settings) {
		v.StartDefaults = &StartDefaultsPolicy{By: "owner", Rules: []StartDefaultsRule{{Then: StartDefaultsThen{Effort: "max"}}}}
	})
	before, err := json.Marshal(s.settings(ctx))
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"task":{"machine":"box"}}`,
		`{"task":{"machine":"box"},"start_defaults":{"rules":[{"applies_when":{},"then":{"candidates":[{"name":"deep"}],"effort":"low","ultracode":true}}]}}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/start-defaults/preview", strings.NewReader(body))
		req.Header.Set(RoleHeader, roleFull)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
		}
	}
	after, _ := json.Marshal(s.settings(ctx))
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the preview changed settings:\nbefore %s\nafter  %s", before, after)
	}
	if len(startsOf(f)) != 0 || s.taskStarting.Load() != 0 {
		t.Fatalf("the preview started or claimed work: starts %v", startsOf(f))
	}
}

// Accepting both aliases with equal values keeps their content: the stored rule still scopes to the role it named,
// so a regression that dropped the match on a both-names write would be caught.
func TestStartDefaultsAliasAgreementKeepsTheMatch(t *testing.T) {
	s, _ := defaultsHub(t)
	if code, got := putStartDefaults(t, s, roleFull, "",
		`{"start_defaults":{"rules":[{"when":{"role":"worker"},"applies_when":{"role":"worker"},"then":{"effort":"max"}}]}}`); code != http.StatusOK {
		t.Fatalf("both-names write: %d %s", code, got)
	}
	p := s.activeStartDefaults(context.Background())
	if p == nil || len(p.Rules) != 1 || p.Rules[0].When.Role != "worker" {
		t.Fatalf("the both-names write lost the match: %+v", p)
	}
	if startDefaultsMatch(p.Rules[0].When, StartFacts{Role: "leader"}) {
		t.Fatal("the stored rule now matches leaders too")
	}
	if !startDefaultsMatch(p.Rules[0].When, StartFacts{Role: "worker"}) {
		t.Fatal("the stored rule no longer matches workers")
	}
}

// The production roll really draws from the trusted entropy source: point it at a failing reader and the roll must
// surface that failure (the package's panic precedent), never fall back to a predictable value.
func TestPresetIntNReadsTheTrustedEntropySource(t *testing.T) {
	oldReader, oldRoll := presetEntropy, presetIntN
	t.Cleanup(func() { presetEntropy, presetIntN = oldReader, oldRoll })
	presetIntN = oldRoll // the production implementation, not another test's stub
	presetEntropy = failingEntropy{}
	defer func() {
		if recover() == nil {
			t.Fatal("a failing entropy source did not fail the roll: the value came from somewhere else")
		}
	}()
	presetIntN(4)
}

// failingEntropy always fails, to prove the roll reads the entropy source.
type failingEntropy struct{}

func (failingEntropy) Read([]byte) (int, error) { return 0, errors.New("entropy unavailable") }

// codexDefaultsHub is launcherFitHub plus a Codex launcher cx on box and a codex-tier that runs on it, so a preview
// and a real start can both be driven onto Codex rather than an assumed agent.
func codexDefaultsHub(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	s, f := launcherFitHub(t)
	f.zshrc["box"] = append(f.zshrc["box"], proto.Launcher{Name: "cx", Source: "zshrc", Agent: "codex"})
	saveTiers(t, s, map[string]ModelTier{
		"codex-tier": {Launcher: "cx", Model: "cx-model"},
		"deep":       {Launcher: "gateway", Model: "gw-deep", Effort: "high"},
		"quick":      {Launcher: "gateway", Model: "gw-quick"},
	})
	return s, f
}

// The effective agent comes from the caller's launcher or the selected candidate's launcher, never from a free-form
// input: an explicit Codex launcher, or a Codex candidate, takes the Codex goal default and skips Claude-only effort
// and ultracode; a Codex goal off suppresses even the default. Claude Code takes no default goal.
func TestStartDefaultsPreviewCodexAgent(t *testing.T) {
	s, _ := codexDefaultsHub(t)
	post := func(body string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/start-defaults/preview", strings.NewReader(body))
		req.Header.Set(RoleHeader, roleFull)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	// An explicit Codex launcher with no fitting rule: the Codex default goal applies; Claude Code takes none.
	out := post(`{"task":{"machine":"box","done_when":2},"caller":{"launcher":"cx"}}`)
	if out["goal_by_default"] != true {
		t.Fatalf("the Codex default goal was not previewed for the caller's Codex launcher: %v", out)
	}
	// goal off suppresses even that.
	out = post(`{"task":{"machine":"box","done_when":2},"caller":{"launcher":"cx"},"start_defaults":{"rules":[{"applies_when":{},"then":{"goal":"off"}}]}}`)
	if _, ok := out["goal"]; ok {
		t.Fatalf("goal off still previewed a goal: %v", out)
	}
	if _, ok := out["goal_by_default"]; ok {
		t.Fatalf("goal off left the Codex default on: %v", out)
	}
	// Effort and ultracode reach Claude Code only: a caller's Codex launcher reports neither.
	out = post(`{"task":{"machine":"box"},"caller":{"launcher":"cx"},"start_defaults":{"rules":[{"applies_when":{},"then":{"effort":"max","ultracode":true}}]}}`)
	if _, ok := out["effort"]; ok {
		t.Fatalf("a Codex preview advertised effort: %v", out)
	}
	if _, ok := out["ultracode"]; ok {
		t.Fatalf("a Codex preview advertised ultracode: %v", out)
	}
	// A Codex candidate: the sampled pick's launcher decides the agent, so the extras are Codex's, and the usable row
	// says which agent that candidate runs.
	out = post(`{"task":{"machine":"box","done_when":2},"start_defaults":{"rules":[{"applies_when":{},"then":{"candidates":[{"name":"codex-tier"}],"effort":"max","ultracode":true}}]}}`)
	if out["sample"] != "codex-tier" || out["sample_agent"] != "codex" {
		t.Fatalf("the sampled candidate did not resolve to codex: %v", out)
	}
	if out["goal_by_default"] != true {
		t.Fatalf("the Codex candidate did not take the default goal: %v", out)
	}
	if _, ok := out["effort"]; ok {
		t.Fatalf("a Codex candidate advertised effort: %v", out)
	}
	if _, ok := out["ultracode"]; ok {
		t.Fatalf("a Codex candidate advertised ultracode: %v", out)
	}
	usable := out["usable"].([]any)
	if usable[0].(map[string]any)["agent"] != "codex" {
		t.Fatalf("the usable row does not name the candidate's agent: %v", usable)
	}
}

// The preview and a real start agree on the agent a candidate selects: a Codex candidate starts Codex, with the
// default goal, no effort and no ultracode — exactly what the preview reports. This mirrors the real startTaskAgent
// path, not just the helpers.
func TestStartDefaultsPreviewAgentFromSelectedCandidate(t *testing.T) {
	s, f := codexDefaultsHub(t)
	ctx := context.Background()
	// Store the rule so both the preview and the real start read the same policy.
	setSettings(t, s, func(v *Settings) {
		v.StartDefaults = &StartDefaultsPolicy{By: "owner", Rules: []StartDefaultsRule{
			{Then: StartDefaultsThen{
				Presets:   []StartDefaultsPreset{{Name: "codex-tier"}},
				Effort:    "max",
				Ultracode: boolp(true),
			}},
		}}
	})
	// Preview: no explicit launcher/model/tier, two criteria.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/start-defaults/preview",
		strings.NewReader(`{"task":{"machine":"box","done_when":2}}`))
	req.Header.Set(RoleHeader, roleFull)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["sample"] != "codex-tier" || out["sample_agent"] != "codex" || out["goal_by_default"] != true {
		t.Fatalf("preview did not resolve the Codex candidate: %v", out)
	}
	if _, ok := out["effort"]; ok {
		t.Fatalf("preview advertised effort the real start would not set: %v", out)
	}
	if _, ok := out["ultracode"]; ok {
		t.Fatalf("preview advertised ultracode the real start would not set: %v", out)
	}
	// The real start of the same task: launcher cx, no effort, no ultracode, and the Codex default goal.
	task := defaultsTask()
	if _, _, err := s.startTaskAgent(ctx, &task, false); err != nil {
		t.Fatal(err)
	}
	st := startsOf(f)[0]
	if st["launcher"] != "cx" {
		t.Fatalf("the real start did not pick the Codex launcher: %v", st)
	}
	if !unset(st["effort"]) || st["effort"] == "max" {
		t.Fatalf("the real start applied effort the preview hid: %v", st)
	}
	if _, ok := st["ultracode"]; ok {
		t.Fatalf("the real start applied ultracode the preview hid: %v", st)
	}
	if _, ok := st["goal"]; !ok {
		t.Fatalf("the real start took no goal, but the preview reported the Codex default: %v", st)
	}
}

func assertStartDefaultsPreviewNoSideEffects(t *testing.T, s *Service, f *fakeHub, before []byte) {
	t.Helper()
	after, err := json.Marshal(s.settings(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("preview changed settings:\nbefore %s\nafter  %s", before, after)
	}
	if len(startsOf(f)) != 0 {
		t.Fatalf("preview started an Agent: %v", startsOf(f))
	}
	if s.taskStarting.Load() != 0 {
		t.Fatal("preview claimed a start")
	}
}

func postStartDefaultsPreview(t *testing.T, s *Service, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/start-defaults/preview", strings.NewReader(body))
	req.Header.Set(RoleHeader, roleFull)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("preview response: %v; body %.200s", err, rec.Body.String())
	}
	return rec.Code, out
}

// Candidate agent metadata follows real configured, last-seen and managed-launcher sources. A wrapper absent from all
// readable metadata stays unknown rather than being guessed as Claude.
func TestStartDefaultsCandidateAgentMetadataDoesNotGuessUnknownWrappers(t *testing.T) {
	s, _ := defaultsHub(t)
	ctx := context.Background()
	if err := s.putSetting(ctx, launchersZshrcKey, map[string][]proto.Launcher{
		"box": {{Name: "seen", Source: "zshrc", Agent: "codex"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.putSetting(ctx, launchersManagedKey, []storedLauncher{{
		ManagedLauncher: proto.ManagedLauncher{Name: "direct", Kind: "api", Agent: "codex", BaseURL: "http://example.test"},
	}}); err != nil {
		t.Fatal(err)
	}
	lastSeen := s.candidateAgentLookup(ctx, "box", nil, false)
	for name, want := range map[string]string{"claude": "claude", "seen": "codex", "direct": "codex", "absent": ""} {
		if got := lastSeen(name); got != want {
			t.Fatalf("last-seen metadata for %q = %q, want %q", name, got, want)
		}
	}
	known := s.candidateAgentLookup(ctx, "box", []proto.Launcher{{Name: "gateway", Source: "zshrc"}}, true)
	if got := known("gateway"); got != "claude" {
		t.Fatalf("ordinary gateway agent = %q, want claude", got)
	}
	if got := known("absent"); got != "" {
		t.Fatalf("a known inventory's absent wrapper was guessed as %q", got)
	}
}

// An explicitly selected hidden launcher carries its real metadata through candidate evaluation and the illustrative
// fit. Preview and the actual start both choose Codex, with its default goal and no Claude-only extras.
func TestStartDefaultsPreviewHiddenCodexCandidateMatchesStart(t *testing.T) {
	s, f := codexDefaultsHub(t)
	ctx := context.Background()
	if err := s.putSetting(ctx, launchersHiddenKey, []string{"cx"}); err != nil {
		t.Fatal(err)
	}
	policy := `{"start_defaults":{"rules":[{"applies_when":{},"then":{"candidates":[{"name":"codex-tier"}],"effort":"max","ultracode":true}}]}}`
	if code, out := putStartDefaults(t, s, roleFull, "", policy); code != http.StatusOK {
		t.Fatalf("policy: %d %s", code, out)
	}
	code, out := postStartDefaultsPreview(t, s, `{"task":{"machine":"box","done_when":2}}`)
	if code != http.StatusOK || out["sample"] != "codex-tier" || out["sample_agent"] != "codex" || out["goal_by_default"] != true {
		t.Fatalf("hidden Codex preview: %d %v", code, out)
	}
	if _, ok := out["effort"]; ok {
		t.Fatalf("hidden Codex preview advertised effort: %v", out)
	}
	if _, ok := out["ultracode"]; ok {
		t.Fatalf("hidden Codex preview advertised ultracode: %v", out)
	}
	usable := out["usable"].([]any)
	if len(usable) != 1 || usable[0].(map[string]any)["agent"] != "codex" {
		t.Fatalf("hidden usable row has the wrong agent: %v", usable)
	}
	task := defaultsTask()
	if _, _, err := s.startTaskAgent(ctx, &task, false); err != nil {
		t.Fatal(err)
	}
	st := startsOf(f)[0]
	if st["launcher"] != "cx" || !unset(st["effort"]) || st["ultracode"] != nil || st["goal"] == nil {
		t.Fatalf("real start disagrees with hidden Codex preview: %v", st)
	}
}

// A default launcher missing on the selected machine falls back through the same fit. The fallback to Claude keeps a
// matching rule's Claude extras and suppresses the Codex default goal, in preview and in the actual start alike.
func TestStartDefaultsPreviewUnavailableDefaultFallsBackLikeStart(t *testing.T) {
	s, f := defaultsHub(t)
	ctx := context.Background()
	setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.codex", Model: "codex-model"})
	policy := `{"start_defaults":{"rules":[{"applies_when":{},"then":{"effort":"max","ultracode":true}}]}}`
	if code, out := putStartDefaults(t, s, roleFull, "", policy); code != http.StatusOK {
		t.Fatalf("policy: %d %s", code, out)
	}
	code, out := postStartDefaultsPreview(t, s, `{"task":{"machine":"l4","done_when":2}}`)
	if code != http.StatusOK || out["effort"] != "max" || out["ultracode"] != true {
		t.Fatalf("Claude fallback did not keep the rule's extras: %d %v", code, out)
	}
	if _, ok := out["goal_by_default"]; ok {
		t.Fatalf("Claude fallback kept the absent Codex's default goal: %v", out)
	}
	task := defaultsTask()
	task.Machine = "l4"
	task.Cwd = "/home/me/code/app"
	if _, _, err := s.startTaskAgent(ctx, &task, false); err != nil {
		t.Fatal(err)
	}
	st := startsOf(f)[0]
	if st["launcher"] != "claude" || st["effort"] != "max" || st["ultracode"] != true || st["goal"] != nil {
		t.Fatalf("real fallback start disagrees with preview: %v", st)
	}
}

// Candidate metadata rides the existing ordinary/hidden reads; only the sampled launch is fitted. A no-policy preview
// performs one fit inventory read, and a non-sampled hidden row needs no additional read of its own.
func TestStartDefaultsPreviewMetadataDoesNotAddInventoryReads(t *testing.T) {
	count := func(f *fakeHub, since int) int {
		f.mu.Lock()
		defer f.mu.Unlock()
		calls := 0
		for _, call := range f.calls[since:] {
			if call.Method == DeviceLaunchersList {
				calls++
			}
		}
		return calls
	}
	t.Run("no policy has one fit read", func(t *testing.T) {
		s, f := defaultsHub(t)
		f.mu.Lock()
		before := len(f.calls)
		f.mu.Unlock()
		if code, out := postStartDefaultsPreview(t, s, `{"task":{"machine":"box","done_when":2}}`); code != http.StatusOK {
			t.Fatalf("preview: %d %v", code, out)
		}
		if got := count(f, before); got != 1 {
			t.Fatalf("no-policy preview made %d inventory reads, want 1", got)
		}
	})
	t.Run("non-sampled hidden row reuses its candidate read", func(t *testing.T) {
		s, f := codexDefaultsHub(t)
		if err := s.putSetting(context.Background(), launchersHiddenKey, []string{"cx"}); err != nil {
			t.Fatal(err)
		}
		draft := `{"task":{"machine":"box","done_when":2},"start_defaults":{"rules":[{"applies_when":{},"then":{"candidates":[{"name":"quick"},{"name":"codex-tier"}]}}]}}`
		f.mu.Lock()
		before := len(f.calls)
		f.mu.Unlock()
		code, out := postStartDefaultsPreview(t, s, draft)
		if code != http.StatusOK || out["sample"] != "quick" || out["sample_agent"] != "claude" {
			t.Fatalf("mixed-agent preview: %d %v", code, out)
		}
		rows := out["usable"].([]any)
		if len(rows) != 2 || rows[0].(map[string]any)["agent"] != "claude" || rows[1].(map[string]any)["agent"] != "codex" {
			t.Fatalf("mixed-agent rows: %v", rows)
		}
		// Shared per-machine launcher-list cache covers ordinary, explicit-hidden, and fit reads in one RPC.
		if got := count(f, before); got != 1 {
			t.Fatalf("mixed-agent preview made %d inventory reads, want 1", got)
		}
	})
}

// An empty draft previews the no-policy start instead of failing validation, and it does not clear the stored policy.
func TestStartDefaultsPreviewEmptyDraft(t *testing.T) {
	s, _ := defaultsHub(t)
	ctx := context.Background()
	setSettings(t, s, func(v *Settings) {
		v.StartDefaults = &StartDefaultsPolicy{By: "owner", Rules: []StartDefaultsRule{{Then: StartDefaultsThen{Effort: "max"}}}}
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/start-defaults/preview",
		strings.NewReader(`{"task":{"machine":"box"},"start_defaults":{"rules":[]}}`))
	req.Header.Set(RoleHeader, roleFull)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("an empty draft was refused: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["matched"] != false {
		t.Fatalf("an empty draft should preview the no-policy start: %v", out)
	}
	if p := s.activeStartDefaults(ctx); p == nil || p.Rules[0].Then.Effort != "max" {
		t.Fatalf("the empty-draft preview cleared the stored policy: %+v", p)
	}
}

// caller_wins and preset_blocked_by_caller are reported only where the policy actually offers something it could
// apply: an extras-only rule with a caller launcher and ultracode off reports neither a blocked preset nor an
// ultracode win, and a candidates-only rule with a caller effort reports no effort win.
func TestStartDefaultsPreviewCallerMarkersOnlyWhenReal(t *testing.T) {
	s, _ := defaultsHub(t)
	post := func(body string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/start-defaults/preview", strings.NewReader(body))
		req.Header.Set(RoleHeader, roleFull)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	// An effort-only rule: the caller's launcher and ultracode off block nothing, because the rule names no candidates
	// and no ultracode.
	out := post(`{"task":{"machine":"box"},"caller":{"launcher":"claude","ultracode":false},"start_defaults":{"rules":[{"applies_when":{},"then":{"effort":"max"}}]}}`)
	if out["matched"] != true || out["effort"] != "max" {
		t.Fatalf("the effort-only rule did not apply: %v", out)
	}
	if _, ok := out["caller_wins"]; ok {
		t.Fatalf("caller_wins reported a win over a field the rule never set: %v", out)
	}
	if _, ok := out["preset_blocked_by_caller"]; ok {
		t.Fatalf("preset_blocked_by_caller reported with no candidates: %v", out)
	}
	// A candidates-only rule with a caller effort: no effort win, because the rule sets none.
	out = post(`{"task":{"machine":"box"},"caller":{"effort":"low"},"start_defaults":{"rules":[{"applies_when":{},"then":{"candidates":[{"name":"deep"}]}}]}}`)
	if _, ok := out["caller_wins"]; ok {
		t.Fatalf("caller_wins reported an effort win over a candidates-only rule: %v", out)
	}
	// But a caller launcher does block this rule's preset.
	out = post(`{"task":{"machine":"box"},"caller":{"launcher":"claude"},"start_defaults":{"rules":[{"applies_when":{},"then":{"candidates":[{"name":"deep"}]}}]}}`)
	if out["preset_blocked_by_caller"] != true {
		t.Fatalf("a caller launcher did not block the rule's preset: %v", out)
	}
	// And a rule that would turn ultracode on loses to the caller's explicit off.
	out = post(`{"task":{"machine":"box"},"caller":{"ultracode":false},"start_defaults":{"rules":[{"applies_when":{},"then":{"ultracode":true}}]}}`)
	wins, _ := out["caller_wins"].([]any)
	if len(wins) != 1 || wins[0] != "ultracode" {
		t.Fatalf("an explicit ultracode off did not win over the rule's ultracode on: %v", out)
	}
}

// A passed-over candidate keeps its name and reason as separate fields even when the name contains ": ", so an API
// consumer can map the reason back to the exact input candidate.
func TestStartDefaultsPreviewPassedOverNameWithSeparator(t *testing.T) {
	s, _ := defaultsHub(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/start-defaults/preview",
		strings.NewReader(`{"task":{"machine":"box"},"start_defaults":{"rules":[{"applies_when":{},"then":{"candidates":[{"name":"foo: bar"}]}}]}}`))
	req.Header.Set(RoleHeader, roleFull)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: %d %s", rec.Code, rec.Body.String())
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	passed, _ := out["passed_over"].([]any)
	if len(passed) != 1 {
		t.Fatalf("passed_over = %v", out["passed_over"])
	}
	row := passed[0].(map[string]any)
	if row["name"] != "foo: bar" || row["reason"] != "unknown preset" {
		t.Fatalf("the candidate name was split on its own separator: %v", row)
	}
}

// applies_when.supervisor is normalized to its distinct classes, so an accepted policy's dual-name read always fits
// the write limit: a repeated class is stored once (this is what made an unbounded array break GET→PUT), an unknown
// class is still refused, and the policy round-trips.
func TestStartDefaultsSupervisorBounds(t *testing.T) {
	s, _ := defaultsHub(t)
	rule := func(sup string) string {
		return `{"start_defaults":{"rules":[{"applies_when":{"supervisor":` + sup + `},"then":{"effort":"max"}}]}}`
	}
	// A repeated class is accepted but stored once, not rejected.
	if code, got := putStartDefaults(t, s, roleFull, "", rule(`["main","main"]`)); code != http.StatusOK {
		t.Fatalf("a repeated supervisor class was not accepted and deduped: %d %s", code, got)
	}
	p := s.activeStartDefaults(context.Background())
	if p == nil || !slices.Equal(p.Rules[0].When.Supervisor, []string{"main"}) {
		t.Fatalf("the repeated class was not stored once: %+v", p.Rules[0].When.Supervisor)
	}
	// The unbounded reproduction (thousands of one class) is accepted, deduped to one entry, and round-trips.
	big := `["main"` + strings.Repeat(`,"main"`, 100) + `]`
	if code, got := putStartDefaults(t, s, roleFull, "", rule(big)); code != http.StatusOK {
		t.Fatalf("an unbounded repeated supervisor array was not accepted and deduped: %d %s", code, got[:120])
	}
	// Every distinct class once (with a duplicate thrown in) is stored deduped, and its dual-name read writes back.
	if code, got := putStartDefaults(t, s, roleFull, "", rule(`["main","leader","main","background_supervisor","scheduled","other"]`)); code != http.StatusOK {
		t.Fatalf("distinct classes with a duplicate were refused: %d %s", code, got)
	}
	p = s.activeStartDefaults(context.Background())
	if p == nil || len(p.Rules[0].When.Supervisor) != len(dispatchClasses) {
		t.Fatalf("the classes were not deduped to the distinct set: %+v", p.Rules[0].When.Supervisor)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant/start-defaults", nil))
	if rec.Body.Len() >= startDefaultsBodyLimit {
		t.Fatalf("the deduped supervisor policy still exceeds the write limit: %d bytes", rec.Body.Len())
	}
	if code, got := putStartDefaults(t, s, roleFull, "", rec.Body.String()); code != http.StatusOK {
		t.Fatalf("the deduped supervisor policy did not round-trip: %d %s", code, got[:120])
	}
	// An unknown class is still refused.
	if code, got := putStartDefaults(t, s, roleFull, "", rule(`["owner"]`)); code != http.StatusBadRequest {
		t.Fatalf("an unknown supervisor class was accepted: %d %s", code, got)
	}
}
