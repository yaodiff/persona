package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/kernel"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/larkhost"
	"github.com/nwverse/persona/server/internal/modapi"
	"github.com/nwverse/persona/server/internal/moduleswitch"
)

// The built-in feature modules as the assistant loads them (modules.go). The briefing module is the pilot: loaded by
// default, and absent (no setting, no routine facts, no briefing) when switched off or not compiled in.

// needsBriefingModule skips a test of the briefing itself in a build without the module (persona_core_only,
// persona_without_briefing); the tests below cover the core without it in every build.
func needsBriefingModule(t testing.TB) {
	t.Helper()
	for _, m := range kernel.Builtin() {
		if m.Info().ID == "briefing" {
			return
		}
	}
	t.Skip("the briefing module is not in this build")
}

// In a build without the briefing module the assistant loads none: the default is the core alone.
func TestBuildWithoutBriefingLoadsNone(t *testing.T) {
	s := newTestService(t, newFakeHub().withMachine())
	for _, m := range kernel.Builtin() {
		if m.Info().ID == "briefing" {
			t.Skip("this build has the briefing module")
		}
	}
	if s.modules.Loaded("briefing") {
		t.Fatal("the briefing module loaded in a build without it")
	}
	assertBriefingAbsent(t, s)
}

// The manager's log names the loaded modules, or none (scripts/ci/core-only-smoke.py reads the line of a core-only build).
func TestLoadedModulesNamesThemOrNone(t *testing.T) {
	if got := loadedModules(kernel.New(nil, nil, nil)); got != "none" {
		t.Fatalf("no modules: %q, want none", got)
	}
	if got := loadedModules(nil); got != "none" {
		t.Fatalf("nil kernel: %q, want none", got)
	}
	var want []string
	for _, m := range kernel.Builtin() {
		want = append(want, m.Info().ID)
	}
	k := kernel.New(kernel.Builtin(), nil, nil)
	if len(k.Modules()) != len(want) {
		t.Skip("a built-in module does not load here; the kernel's tests say why")
	}
	if got := loadedModules(k); len(want) > 0 && got != strings.Join(want, ", ") {
		t.Fatalf("loaded %q, want %q", got, strings.Join(want, ", "))
	}
}

// A stored null is never set: it reads as nil and is left out when the settings are written again; a value round-trips
// byte for byte.
func TestModuleValueNullIsNeverSet(t *testing.T) {
	var v Settings
	if err := json.Unmarshal([]byte(`{"briefing_waits":null}`), &v); err != nil || v.BriefingWaits != nil {
		t.Fatalf("null read as %q (%v)", v.BriefingWaits, err)
	}
	if raw, _ := json.Marshal(v); strings.Contains(string(raw), "briefing_waits") {
		t.Fatalf("a never-set value was written: %s", raw)
	}
	stored := `{"stalled_minutes":25,"silent_minutes":150,"orphan_minutes":12,"card_minutes":4,"max_rows":7}`
	if err := json.Unmarshal([]byte(`{"briefing_waits":`+stored+`}`), &v); err != nil || string(v.BriefingWaits) != stored {
		t.Fatalf("value read as %q (%v)", v.BriefingWaits, err)
	}
	if raw, _ := json.Marshal(v); !strings.Contains(string(raw), `"briefing_waits":`+stored) {
		t.Fatalf("value written as %s", raw)
	}
}

func ownSettingKeys(s *Service) []string {
	var keys []string
	for _, o := range s.ownSettings() {
		keys = append(keys, o.Key)
	}
	return keys
}

func TestBriefingModuleIsLoadedByDefault(t *testing.T) {
	needsBriefingModule(t)
	s := newTestService(t, newFakeHub().withMachine())
	if !s.modules.Loaded("briefing") || !s.briefingOn() {
		t.Fatalf("the briefing module is not loaded: %+v (skipped: %q)", s.modules.Modules(), s.modules.Skipped("briefing"))
	}
	if !slices.Contains(ownSettingKeys(s), "assistant.briefing_waits") {
		t.Fatal("persona_settings does not list assistant.briefing_waits with the module on")
	}
	if !slices.Contains(s.moduleFeatures(), "module.briefing") {
		t.Fatalf("the briefing module feature is not announced: %v", s.moduleFeatures())
	}
}

// The modules row (read when the manager starts) switches a module off: it is not loaded, its setting is hidden from persona_settings
// but stays stored (a change to another setting keeps it), and the routines degrade: a routine with facts on opens with
// none and keeps no briefing run, a daily report has the north star's lines alone, and the acceptance reminders wait for
// no briefing.
func TestSwitchedOffBriefingModuleIsAbsent(t *testing.T) {
	needsBriefingModule(t) // switching off needs it compiled in; TestBuildWithoutBriefingLoadsNone covers the rest
	ctx := context.Background()
	st := &memStore{}
	if err := st.PutSetting(ctx, modulesKey, ModuleSwitch{Disabled: []string{"briefing"}}); err != nil {
		t.Fatal(err)
	}
	stored := json.RawMessage(`{"stalled_minutes":25,"silent_minutes":150,"orphan_minutes":12,"card_minutes":4,"max_rows":7}`)
	if err := st.PutSetting(ctx, settingsKey, Settings{BriefingWaits: moduleValue(stored)}); err != nil {
		t.Fatal(err)
	}
	off := ReadModuleSwitch(ctx, st, t.Logf)
	if len(off) != 1 || off[0] != "briefing" {
		t.Fatalf("switch read as %v", off)
	}
	f := newFakeHub().withMachine()
	s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), ModulesOff: off}))
	if s.modules.Loaded("briefing") || s.briefingOn() || s.modules.Skipped("briefing") != "switched off" {
		t.Fatalf("loaded %+v, skipped %q", s.modules.Modules(), s.modules.Skipped("briefing"))
	}
	assertBriefingAbsent(t, s)
	if _, err := s.updateSettings(ctx, func(v *Settings) error { note := "kept apart"; v.Notes = &note; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := s.settings(ctx).BriefingWaits; string(got) != string(stored) {
		t.Fatalf("the switched-off module's setting was not kept: %s", got)
	}
}

// A binary without the module (persona_without_briefing, persona_core_only) behaves the same.
func TestCoreWithoutModulesHasNoBriefing(t *testing.T) {
	f := newFakeHub().withMachine()
	st := &memStore{}
	s := withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), Modules: []modapi.Module{}}))
	if len(s.modules.Modules()) != 0 {
		t.Fatalf("modules loaded from an empty list: %+v", s.modules.Modules())
	}
	assertBriefingAbsent(t, s)
}

func assertBriefingAbsent(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	if slices.Contains(ownSettingKeys(s), "assistant.briefing_waits") {
		t.Error("persona_settings lists assistant.briefing_waits without its module")
	}
	if _, err := s.moduleSettingGet("assistant.briefing_waits")(ctx); err == nil {
		t.Error("the hidden setting answered a get")
	}
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/api/v1/later/old/handled", http.StatusNotFound},
		{"/api/v1/todos/review/accept", http.StatusBadRequest}, // still served: requires asked_at
	} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest(http.MethodPost, tc.path, nil))
		if w.Code != tc.status {
			t.Errorf("POST %s = %d, want %d", tc.path, w.Code, tc.status)
		}
	}
	f := s.hub.(*fakeHub)
	f.answerAppBlocking(AppSettings, SettingsList, func(json.RawMessage) (any, error) {
		return map[string]any{"settings": []Setting{{Key: "assistant.briefing_waits"}, {Key: "inbox.rules"}}}, nil
	})
	list := callReversibleSettingAction(ctx, s, "list", "", `null`, "conv1")
	if len(f.appParams(AppSettings, SettingsList)) != 1 {
		t.Fatal("the settings list did not query the hub catalog")
	}
	if text := reversibleResultText(list); list.IsError || strings.Contains(text, "assistant.briefing_waits") || !strings.Contains(text, "inbox.rules") {
		t.Fatalf("settings from both catalogs with briefing absent: %s", text)
	}
	requireUnavailableBriefingAction(t, s, "get", `null`)
	requireUnavailableBriefingAction(t, s, "set", `null`)
	now := time.Now()
	if got := s.runFacts(ctx, Task{ID: "morning", Facts: true}, now); got != "" {
		t.Errorf("a routine with facts on opened with facts: %q", got)
	}
	if got, want := s.runFacts(ctx, Task{ID: "dot", Report: true}, now), s.northStarRunFacts(ctx, now); got != want {
		t.Errorf("a daily report's facts = %q, want the north star's lines %q", got, want)
	}
	if runs := s.briefTaskRunCount(); runs != 0 {
		t.Errorf("%d briefing runs kept without the module", runs)
	}
	if dirs, off := s.briefDirections(ctx, now, s.botLang(ctx), testWeb); dirs != nil || off != nil {
		t.Errorf("wait rows without the module: %+v %+v", dirs, off)
	}
	if g := s.briefingGrace(ctx, now, 9*60); g != 0 {
		t.Errorf("acceptance reminders wait %d minutes for a briefing that does not come", g)
	}
	// The hooks of a scheduled run find no briefing run and do nothing.
	s.briefSent(ScheduleRun{RefID: "morning", SessionID: "sess", Due: now})
	s.briefReached("sess")
	if push := s.briefPush("sess", s.botLang(ctx), true); push != "" {
		t.Errorf("a push for a briefing without the module: %q", push)
	}
}

// End to end: the morning routine with facts on runs with the module absent as a routine with facts off does: its answer
// goes out, with no briefing rows and no brief item.
func TestMorningRoutineRunsWithoutTheBriefingModule(t *testing.T) {
	off := briefRunEnvWith(t, true, func(e *briefRunEnv) { e.s.modules = kernel.New(nil, nil, nil) })
	off.mirror()
	plain := newBriefRunEnv(t, false)
	plain.mirror()
	if items := off.briefItems(); len(items) != 0 {
		t.Fatalf("brief items without the module: %+v", items)
	}
	if !larkhost.InBuild(moduleswitch.Set{}) {
		if len(off.cards()) != 0 || len(plain.cards()) != 0 {
			t.Fatal("a chat card was posted without its channel")
		}
		return
	}
	got, want := off.cards(), plain.cards()
	if len(got) != len(want) || len(got) == 0 || !strings.Contains(got[0], briefSummary) {
		t.Fatalf("%d posts without the module, %d with facts off; want the model's answer: %v", len(got), len(want), got)
	}
	for _, c := range got {
		if strings.Contains(c, "等你处理") || strings.Contains(c, "待验收") { // i18n-ignore: the zh-Hans card's section titles
			t.Fatalf("a briefing section without the module:\n%s", c)
		}
	}
}

// A briefing persisted before the switch is still part of the conversation's ordinary feed.
func TestHistoricalBriefingReadableWithoutModule(t *testing.T) {
	body := json.RawMessage(`{"title":"Earlier briefing","waiting":[]}`)
	st := &sideFeedStore{}
	if err := st.AppendFeed(t.Context(), db.FeedRow{ID: "old", Conversation: "chat", Kind: "brief", At: time.Now(), Body: body}); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Hub: newFakeHub().withMachine(), Store: func() Store { return st }, ModulesOff: []string{"briefing"}, Logf: testLogf(t)})
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/assistant/conversations/chat/feed", nil))
	var out struct {
		Items []FeedItem `json:"items"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != http.StatusOK || len(out.Items) != 1 ||
		out.Items[0].ID != "old" || out.Items[0].Kind != "brief" || string(out.Items[0].Body) != string(body) {
		t.Fatalf("historical feed = %d %s (%v)", w.Code, w.Body.String(), err)
	}
}
