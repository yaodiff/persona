package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func profileStoreSnapshot(st *memStore) (map[string][]byte, int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return maps.Clone(st.vals), st.puts
}

func requireProfileStoreUnchanged(t *testing.T, st *memStore, values map[string][]byte, puts int) {
	t.Helper()
	got, n := profileStoreSnapshot(st)
	if !reflect.DeepEqual(got, values) || n != puts {
		t.Fatalf("failed or no-op operation changed stored settings: writes %d -> %d", puts, n)
	}
}

func profilesOf(t *testing.T, s *Service) ModelProfilesView {
	t.Helper()
	v, err := s.modelProfilesView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func storeProfiles(t *testing.T, st *downStore, profiles ...ModelProfile) {
	t.Helper()
	if err := st.PutSetting(context.Background(), modelProfilesKey, modelProfiles{Revision: 1, Profiles: profiles}); err != nil {
		t.Fatal(err)
	}
}

func TestModelProfilesCaptureCurrentConfiguration(t *testing.T) {
	s, f, st := modelService(t)
	ctx := context.Background()
	sealed, err := s.sealKey("test-secret-key")
	if err != nil {
		t.Fatal(err)
	}
	l := storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "current", Model: "test-current", SmallModel: "test-small",
		SubagentModel: "test-subagent", OpusModel: "test-opus", SonnetModel: "test-sonnet", HaikuModel: "test-haiku",
		FableModel: "test-fable", Effort: "high", BaseURL: "https://example.invalid", Args: []string{"test-argument"}}, KeySealed: sealed}
	putLaunchers(t, s, l)
	for id, binding := range map[string]ModelBinding{
		"assistant":      {Provider: "launcher.current", Model: "test-chat", ConfigDir: assistantConfigDirLegacy},
		"agent.new":      {Provider: "launcher.current", Model: "test-agents"},
		"agent.delegate": {Model: "test-delegate"},
	} {
		if err := st.PutSetting(ctx, modelRecordKey(id), modelRecord{Revision: 3, Binding: binding}); err != nil {
			t.Fatal(err)
		}
	}
	before, puts := profileStoreSnapshot(st.memStore)
	for revision, id := range []string{"performance", "economy"} {
		body := fmt.Sprintf(`{"revision":%d,"name":%q,"capture":true}`, revision, id)
		if r := request(s, http.MethodPut, "/api/v1/model-profiles/"+id, body); r.Code != http.StatusOK {
			t.Fatalf("creating a profile from current settings: %d %s", r.Code, r.Body.String())
		}
	}
	v := profilesOf(t, s)
	if v.Revision != 2 || v.Active != "" || v.Rollback != nil || len(v.Events) != 2 || v.Events[0].Action != "save" ||
		len(v.Profiles) != 2 {
		t.Fatalf("captured profiles: %+v", v)
	}
	for _, p := range v.Profiles {
		if p.Purposes["assistant"].Model != "test-chat" || p.Purposes["assistant"].ConfigDir != "" ||
			p.Purposes["agent.delegate"].Model != "test-delegate" || len(p.Changes) != 0 {
			t.Fatalf("capture differs from the current model-only configuration: %+v", p)
		}
		m := p.Launchers["current"]
		for _, field := range m.fields(&l.ManagedLauncher) {
			if *field.set == nil || **field.set != *field.launcher {
				t.Fatalf("capture lost launcher field %s", field.name)
			}
		}
		// Core is the main chat's choice and dev the Agents'.
		core, dev := p.Tiers["core"], p.Tiers["dev"]
		if core.Description == "" || dev.Description == "" || len(p.Tiers) != 2 {
			t.Fatalf("captured tiers lack descriptions: %+v", p.Tiers)
		}
		core.Description, dev.Description = "", ""
		// Each answers to its older name too, as a migrated one does (#15010).
		if !core.equal(ModelTier{Launcher: "current", Model: "test-chat", Aliases: []string{"strong"}}) ||
			!dev.equal(ModelTier{Launcher: "current", Model: "test-agents", Aliases: []string{"standard"}}) {
			t.Fatalf("tiers are not the current configuration: %+v", p.Tiers)
		}
	}
	after, n := profileStoreSnapshot(st.memStore)
	delete(after, modelProfilesKey)
	delete(after, modelProfileStateKey)
	if !reflect.DeepEqual(before, after) || n != puts+4 {
		t.Fatal("capturing reset model defaults or changed another setting")
	}
	if f.eventTypes() != "model_profile model_profile" || len(f.managed) != 0 {
		t.Fatal("capturing did not publish catalog changes or pushed launcher settings")
	}
	before, puts = profileStoreSnapshot(st.memStore)
	profilesOf(t, s)
	requireProfileStoreUnchanged(t, st.memStore, before, puts)

	// Capture changes only the selected profile, retaining its existing tier definitions.
	if err := st.PutSetting(ctx, modelRecordKey("assistant"), modelRecord{Revision: 4,
		Binding: ModelBinding{Provider: "launcher.current", Model: "test-later", ConfigDir: assistantConfigDirLegacy}}); err != nil {
		t.Fatal(err)
	}
	customTiers := map[string]ModelTier{"custom": {Launcher: "current", Model: "test-custom"}}
	if _, err := s.saveModelProfile(ctx, nil, "performance", ProfileActor{Kind: "hub"}, func(p *ModelProfile, _ bool) error {
		p.Tiers = customTiers
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r := request(s, "PUT", "/api/v1/model-profiles/performance", `{"revision":3,"name":"Captured","capture":true}`)
	if r.Code != http.StatusOK {
		t.Fatalf("capture: %d %s", r.Code, r.Body.String())
	}
	v = profilesOf(t, s)
	if v.Profiles[0].Purposes["assistant"].Model != "test-later" ||
		!reflect.DeepEqual(v.Profiles[0].Tiers, customTiers) || v.Profiles[1].Purposes["assistant"].Model != "test-chat" || v.Active != "" {
		t.Fatalf("capture applied or overwrote other profile data: %+v", v)
	}
	if got, err := s.modelRecord(ctx, "assistant"); err != nil || got.Binding.Model != "test-later" || got.Revision != 4 {
		t.Fatalf("capture changed running configuration: %+v %v", got, err)
	}
}

func TestModelProfileSwitchRollbackPreservesSecretsAndNewConversationProfile(t *testing.T) {
	s, f, st := modelService(t)
	ctx := context.Background()
	sealed, err := s.sealKey("test-key")
	if err != nil {
		t.Fatal(err)
	}
	headers, err := s.sealKey("X-Test: test-header-secret")
	if err != nil {
		t.Fatal(err)
	}
	env, err := s.sealKey("TEST_SECRET=test-env-secret")
	if err != nil {
		t.Fatal(err)
	}
	l := storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", BaseURL: "https://example.invalid", Model: "test-original",
		SmallModel: "test-small", Effort: "low", Auth: "bearer", Args: []string{"test-argument"}, Proxy: "http://proxy.invalid",
		TimeoutMS: 123, MaxContext: 456, CompactWindow: 789, SubagentForce: true, Source: "test-source",
		CustomModel: "test-custom", CustomModelName: "Custom", CustomModelDescription: "Description",
		Models: []proto.ProviderModel{{ID: "test-original"}}}, KeySealed: sealed, HeadersSealed: headers, EnvSealed: env,
		Preset: "test-preset", DisplayName: "Current gateway", Created: time.Now().UTC()}
	putLaunchers(t, s, l)
	original := modelRecord{Revision: 7, Binding: ModelBinding{Provider: "launcher.gw", Model: "test-chat", ConfigDir: assistantConfigDirLegacy}}
	if err := st.PutSetting(ctx, modelRecordKey("assistant"), original); err != nil {
		t.Fatal(err)
	}
	storeProfiles(t, st, ModelProfile{ID: "economy", Name: "Economy", Purposes: map[string]ModelBinding{"assistant": {Model: "test-selected"}},
		Launchers: map[string]LauncherModels{"gw": {Model: profileValue("test-economy"), SmallModel: profileValue(""), Effort: profileValue("high")}}})
	// Make the assistant's inherited provider match the existing explicit binding.
	setSettings(t, s, func(p *Settings) { p.Launcher = "gw" })
	by := ProfileActor{Kind: "device", Name: "Test device"}
	sw, err := s.applyModelProfile(ctx, "economy", by)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sw.Changed, []string{"assistant", "gw.model", "gw.small_model", "gw.effort"}) {
		t.Fatalf("changes: %+v", sw)
	}
	r, err := s.modelRecord(ctx, "assistant")
	if err != nil || r.Revision != 8 || r.Binding != (ModelBinding{Provider: "launcher.gw", Model: "test-selected", ConfigDir: assistantConfigDirLegacy}) {
		t.Fatalf("switch did not migrate legacy model-only binding or lost the new conversations' profile: %+v %v", r, err)
	}
	got := s.storedLaunchers(ctx)[0]
	want := l
	want.Model, want.SmallModel, want.Effort, want.Updated = "test-economy", "", "high", got.Updated
	if !reflect.DeepEqual(got, want) {
		t.Fatal("switch changed credentials or non-model launcher fields")
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil || state.Previous.Purposes["assistant"] != original.Binding || len(state.Events) != 1 || state.By != by {
		t.Fatalf("switch did not save history and rollback: %+v %v", state, err)
	}
	if f.eventTypes() != "model_profile" || len(f.managed["server"]) != 1 {
		t.Fatalf("switch was not published: %s", f.eventTypes())
	}

	// Repeating the same switch must preserve the rollback, cached context and preferences.
	before, puts := profileStoreSnapshot(st.memStore)
	cached := &prompts.Snapshot{Now: time.Now(), Zone: "UTC"}
	s.live.snap, s.live.at, s.prefs.at = cached, time.Now(), time.Now()
	cacheAt, prefsAt := s.live.at, s.prefs.at
	f.mu.Lock()
	calls := len(f.calls)
	f.mu.Unlock()
	if sw, err := s.applyModelProfile(ctx, "economy", by); err != nil || len(sw.Changed) != 0 {
		t.Fatalf("repeat switch: %+v %v", sw, err)
	}
	requireProfileStoreUnchanged(t, st.memStore, before, puts)
	if s.live.snap != cached || s.live.at != cacheAt || s.prefs.at != prefsAt || f.eventTypes() != "model_profile" {
		t.Fatal("no-op switch invalidated caches or broadcast an event")
	}
	f.mu.Lock()
	for _, call := range f.calls[calls:] {
		if call.Method == assistantwire.DeviceLaunchersManaged || call.Method == assistantwire.DeviceLaunchersModelCheck {
			f.mu.Unlock()
			t.Fatal("no-op switch pushed or checked launcher models")
		}
	}
	f.mu.Unlock()

	// The new conversations' profile and credentials edited after the switch must survive rollback and redo.
	r.Binding.ConfigDir = ""
	if err := st.PutSetting(ctx, modelRecordKey("assistant"), r); err != nil {
		t.Fatal(err)
	}
	rotated, err := s.sealKey("rotated-test-key")
	if err != nil {
		t.Fatal(err)
	}
	got.KeySealed, got.Proxy, got.DisplayName = rotated, "http://later-proxy.invalid", "Later display name"
	putLaunchers(t, s, got)
	for _, redo := range []bool{false, true} {
		if _, err := s.rollbackModelProfile(ctx, by); err != nil {
			t.Fatal(err)
		}
		r, err = s.modelRecord(ctx, "assistant")
		if err != nil || r.Binding.ConfigDir != "" {
			t.Fatalf("rollback restored an obsolete profile for new conversations: %+v %v", r, err)
		}
		wantBinding, wantModel := original.Binding, l.Model
		wantBinding.ConfigDir = ""
		if redo {
			wantBinding, wantModel = ModelBinding{Provider: "launcher.gw", Model: "test-selected"}, "test-economy"
		}
		if r.Binding != wantBinding {
			t.Fatalf("rollback/redo binding: %+v", r.Binding)
		}
		rolled := s.storedLaunchers(ctx)[0]
		if rolled.Model != wantModel || rolled.KeySealed != rotated || rolled.HeadersSealed != headers || rolled.EnvSealed != env ||
			rolled.Proxy != "http://later-proxy.invalid" || rolled.DisplayName != "Later display name" {
			t.Fatal("rollback lost current credentials or non-model fields")
		}
	}
	view := profilesOf(t, s)
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{sealed, rotated, headers, env, "rotated-test-key", "test-header-secret", "test-env-secret", "key_sealed", "headers_sealed", "env_sealed", "base_url", "test-argument", "test-preset"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("profile response contains credentials or non-model data: %q", secret)
		}
	}
	if view.Active != "economy" || len(view.Events) != 3 || view.Events[0].Action != "rollback" {
		t.Fatalf("rollback/redo history: %+v", view)
	}
}

func TestModelProfilesInheritedOverridesAndValidation(t *testing.T) {
	for _, tt := range []struct {
		name      string
		b         ModelBinding
		want      ModelBinding
		api       string
		wantError bool
	}{
		{"inherited model", ModelBinding{Model: "test-selected"}, ModelBinding{Provider: "legacy.triage", Model: "test-selected"}, "responses", false},
		{"inherited effort", ModelBinding{Effort: "high"}, ModelBinding{Effort: "high"}, "responses", false},
		{"messages effort", ModelBinding{Effort: "high"}, ModelBinding{Effort: "high"}, "messages", false},
		{"chat effort", ModelBinding{Effort: "high"}, ModelBinding{Effort: "high"}, "chat", false},
		{"missing provider", ModelBinding{Provider: "launcher.missing", Model: "test-selected"}, ModelBinding{}, "responses", true},
		{"bad effort", ModelBinding{Effort: "invalid"}, ModelBinding{}, "responses", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, f, st := modelService(t)
			ctx := context.Background()
			f.hello.Models.AI = ai.Config{API: tt.api, BaseURL: "https://example.invalid", APIKey: "test-key", Model: "test-original"}
			storeProfiles(t, st, ModelProfile{ID: "selected", Name: "Selected", Purposes: map[string]ModelBinding{"inbox.triage": tt.b}})
			before, puts := profileStoreSnapshot(st.memStore)
			_, err := s.applyModelProfile(ctx, "selected", ProfileActor{Kind: "hub"})
			if (err != nil) != tt.wantError {
				t.Fatalf("switch error: %v", err)
			}
			if tt.wantError {
				requireProfileStoreUnchanged(t, st.memStore, before, puts)
				if f.eventTypes() != "" {
					t.Fatal("failed validation broadcast an event")
				}
			} else if r, err := s.modelRecord(ctx, "inbox.triage"); err != nil || r.Binding != tt.want {
				t.Fatalf("binding = %+v, want %+v (err=%v)", r, tt.want, err)
			}
		})
	}
}

// profileFailureStore fails one read or the entire atomic batch, without bypassing memStore's atomic behavior.
type profileFailureStore struct {
	*memStore
	readKey    string
	writeError error
	readError  error
	batches    int
}

func (st *profileFailureStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == st.readKey {
		return false, st.readError
	}
	return st.memStore.Setting(ctx, key, out)
}

func (st *profileFailureStore) PutSettings(ctx context.Context, values map[string]any) error {
	st.batches++
	if st.writeError != nil {
		return st.writeError
	}
	return st.memStore.PutSettings(ctx, values)
}

// Embedding only Store deliberately hides PutSettings, like a store without transaction support.
type nonAtomicProfileStore struct{ Store }

func TestModelProfilesFailuresAreAtomicAndPropagated(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		operation string
		failures  []string
	}{
		{"view", []string{modelProfilesKey, modelProfileStateKey, launchersManagedKey, modelRecordKey("assistant")}},
		{"capture", []string{launchersManagedKey, modelRecordKey("assistant")}},
		{"switch", []string{modelProfilesKey, modelProfileStateKey, launchersManagedKey, modelRecordKey("assistant"), "write", "non-atomic"}},
		{"rollback", []string{modelProfileStateKey, launchersManagedKey, modelRecordKey("assistant"), "write", "non-atomic"}},
		{"save", []string{modelProfilesKey, modelProfileStateKey, launchersManagedKey, "write", "non-atomic"}},
		{"delete", []string{modelProfilesKey, modelProfileStateKey, "write", "non-atomic"}},
		{"deactivate", []string{modelProfileStateKey, "write", "non-atomic"}},
		{"clear", []string{modelProfilesKey, modelProfileStateKey, "write", "non-atomic"}},
	} {
		for _, failure := range tc.failures {
			t.Run(tc.operation+"/"+failure, func(t *testing.T) {
				s, f, st := modelService(t)
				f.hello.Models.AI = ai.Config{API: "responses", BaseURL: "https://example.invalid", APIKey: "test-key", Model: "test-original"}
				putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", Model: "test-original"}})
				storeProfiles(t, st, ModelProfile{ID: "selected", Name: "Selected", Purposes: map[string]ModelBinding{"assistant": {Model: "test-selected"}},
					Launchers: map[string]LauncherModels{"gw": {Model: profileValue("test-selected")}}})
				if err := st.PutSetting(ctx, modelProfileStateKey, modelProfileState{Active: "selected", Previous: &modelProfileImage{Profile: "before",
					Purposes: map[string]ModelBinding{"assistant": {Model: "test-before"}}, Launchers: map[string]LauncherModels{"gw": {Model: profileValue("test-before")}}}}); err != nil {
					t.Fatal(err)
				}
				failed := errors.New("test database failure")
				failing := &profileFailureStore{memStore: st.memStore, readError: failed}
				s.setStore(func() Store { return failing })
				switch failure {
				case "write":
					failing.writeError = failed
				case "non-atomic":
					s.setStore(func() Store { return nonAtomicProfileStore{Store: failing} })
				default:
					failing.readKey = failure
				}
				before, puts := profileStoreSnapshot(st.memStore)
				cached := &prompts.Snapshot{Now: time.Now(), Zone: "UTC"}
				s.live.snap = cached
				var err error
				switch tc.operation {
				case "view":
					_, err = s.modelProfilesView(ctx)
				case "capture":
					_, _, _, err = s.captureModels(ctx)
				case "switch":
					_, err = s.applyModelProfile(ctx, "selected", ProfileActor{})
				case "rollback":
					_, err = s.rollbackModelProfile(ctx, ProfileActor{})
				case "save":
					_, err = s.saveModelProfile(ctx, nil, "selected", ProfileActor{}, func(p *ModelProfile, _ bool) error { p.Name = "Edited"; return nil })
				case "delete":
					err = s.deleteModelProfile(ctx, nil, "selected", ProfileActor{})
				case "deactivate":
					err = s.deactivateModelProfile(ctx, ProfileActor{})
				case "clear":
					err = s.clearModelProfiles(ctx, nil, ProfileActor{})
				}
				want := failed
				if failure == "non-atomic" {
					want = errModelProfileAtomicStore
				}
				if !errors.Is(err, want) {
					t.Fatalf("failure not propagated: %v, want %v", err, want)
				}
				requireProfileStoreUnchanged(t, st.memStore, before, puts)
				if s.live.snap != cached || f.eventTypes() != "" || len(f.managed) != 0 {
					t.Fatal("failed operation invalidated the cache or pushed configuration")
				}
			})
		}
	}
}

func TestModelProfileMissingLauncherRejectedBeforeTransaction(t *testing.T) {
	for _, scope := range []string{"launcher fields", "purpose provider"} {
		for _, rollback := range []bool{false, true} {
			t.Run(scope+"/"+map[bool]string{false: "switch", true: "rollback"}[rollback], func(t *testing.T) {
				s, f, st := modelService(t)
				ctx := context.Background()
				purposes := map[string]ModelBinding{"assistant": {Model: "test-before"}}
				launchers := map[string]LauncherModels{"missing": {Model: profileValue("test-model")}}
				if scope == "purpose provider" {
					purposes["assistant"] = ModelBinding{Provider: "launcher.missing", Model: "test-before"}
					launchers = nil
				}
				storeProfiles(t, st, ModelProfile{ID: "selected", Name: "Selected", Purposes: purposes, Launchers: launchers})
				if err := st.PutSetting(ctx, modelProfileStateKey, modelProfileState{Active: "selected", Previous: &modelProfileImage{
					Profile: "before", Purposes: purposes, Launchers: launchers}}); err != nil {
					t.Fatal(err)
				}
				fs := &profileFailureStore{memStore: st.memStore}
				s.setStore(func() Store { return fs })
				before, puts := profileStoreSnapshot(st.memStore)
				var err error
				if rollback {
					_, err = s.rollbackModelProfile(ctx, ProfileActor{})
				} else {
					_, err = s.applyModelProfile(ctx, "selected", ProfileActor{})
				}
				if err == nil || fs.batches != 0 {
					t.Fatalf("missing launcher was not rejected before writing: %v, batches %d", err, fs.batches)
				}
				requireProfileStoreUnchanged(t, st.memStore, before, puts)
				if f.eventTypes() != "" {
					t.Fatal("failed operation broadcast")
				}
			})
		}
	}
}

func TestModelProfilesRoutesRolesRevisionsAndLocales(t *testing.T) {
	s, _, st := modelService(t)
	for _, path := range []string{"GET /api/v1/model-profiles", "PUT /api/v1/model-profiles/{id}", "DELETE /api/v1/model-profiles/{id}",
		"DELETE /api/v1/model-profiles", "POST /api/v1/model-profiles/{id}/apply",
		"POST /api/v1/model-profiles/rollback", "POST /api/v1/model-profiles/deactivate"} {
		if !slices.Contains(s.Routes(), path) {
			t.Fatalf("unregistered route: %s", path)
		}
	}
	for _, tt := range []struct {
		method, path, body string
		status             int
	}{
		{"PUT", "/api/v1/model-profiles/custom", `{"name":"Custom"}`, 400},
		{"PUT", "/api/v1/model-profiles/custom", `{"revision":1,"name":"Custom"}`, 409},
		{"PUT", "/api/v1/model-profiles/custom", `{"revision":0,"name":"Custom","api_key":"secret"}`, 400},
		{"PUT", "/api/v1/model-profiles/rollback", `{"revision":0,"name":"Invalid"}`, 400},
		{"POST", "/api/v1/model-profiles/missing/apply", "", 404},
		{"POST", "/api/v1/model-profiles/rollback", "", 409},
		{"DELETE", "/api/v1/model-profiles?revision=invalid", "", 400},
		{"DELETE", "/api/v1/model-profiles?revision=-1", "", 400},
	} {
		before, puts := profileStoreSnapshot(st.memStore)
		r := request(s, tt.method, tt.path, tt.body)
		if r.Code != tt.status {
			t.Fatalf("%s %s: %d %s", tt.method, tt.path, r.Code, r.Body.String())
		}
		requireProfileStoreUnchanged(t, st.memStore, before, puts)
	}
	for _, lang := range i18n.Langs {
		for _, route := range []struct{ method, path string }{
			{"PUT", "/api/v1/model-profiles/performance"},
			{"DELETE", "/api/v1/model-profiles/performance"},
			{"DELETE", "/api/v1/model-profiles"},
			{"POST", "/api/v1/model-profiles/performance/apply"},
			{"POST", "/api/v1/model-profiles/deactivate"},
		} {
			res, err := serveHTTP(context.Background(), s, assistantwire.HTTPRequest{Method: route.method, Path: route.path,
				Header: map[string][]string{RoleHeader: {"answer"}, "Accept-Language": {string(lang)}},
				Body:   json.RawMessage(`{"revision":1,"name":"Edited"}`)}, s.lang)
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != http.StatusForbidden || !strings.Contains(string(res.Body), i18n.T(lang, "assistant-model_profiles.err_role")) {
				t.Fatalf("role or locale %s: %d %s", lang, res.Status, res.Body)
			}
		}
	}
	if r := request(s, "PUT", "/api/v1/model-profiles/custom", `{"revision":0,"name":"Custom","purposes":{"agent.new":{"model":"test-inherited"}}}`); r.Code != 200 {
		t.Fatalf("saving inherited model-only override: %d %s", r.Code, r.Body.String())
	}
	if r := request(s, "DELETE", "/api/v1/model-profiles/custom?revision=0", ""); r.Code != 409 {
		t.Fatalf("stale delete: %d %s", r.Code, r.Body.String())
	}
	if r := request(s, "DELETE", "/api/v1/model-profiles/custom?revision=1", ""); r.Code != 200 {
		t.Fatalf("delete: %d %s", r.Code, r.Body.String())
	}
}

func TestUnconfiguredModelProfileFailureDoesNotWrite(t *testing.T) {
	s, _, st := modelService(t)
	before, puts := profileStoreSnapshot(st.memStore)
	if _, err := s.applyModelProfile(context.Background(), "missing", ProfileActor{}); !errors.Is(err, errModelProfileNotFound) {
		t.Fatal(err)
	}
	requireProfileStoreUnchanged(t, st.memStore, before, puts)
	if _, err := s.saveModelProfile(context.Background(), nil, "bad", ProfileActor{}, func(p *ModelProfile, _ bool) error { return nil }); err == nil {
		t.Fatal("invalid profile was accepted")
	}
	requireProfileStoreUnchanged(t, st.memStore, before, puts)
}

func TestModelProfileAtomicMemoryStoreAndDownStore(t *testing.T) {
	ctx := context.Background()
	st := &memStore{}
	if err := st.PutSetting(ctx, "original", "before"); err != nil {
		t.Fatal(err)
	}
	before, puts := profileStoreSnapshot(st)
	if err := st.PutSettings(ctx, map[string]any{"original": "after", "invalid": make(chan int)}); err == nil {
		t.Fatal("invalid JSON value was accepted")
	}
	requireProfileStoreUnchanged(t, st, before, puts)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := st.PutSettings(cancelled, map[string]any{"original": "after"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	requireProfileStoreUnchanged(t, st, before, puts)
	down := &downStore{memStore: st}
	down.failing(true)
	if err := down.PutSettings(ctx, map[string]any{"original": "after"}); err == nil {
		t.Fatal("downStore bypassed atomic failure")
	}
	requireProfileStoreUnchanged(t, st, before, puts)
	down.failing(false)
	if err := down.PutSettings(ctx, map[string]any{"original": "after", "second": true}); err != nil {
		t.Fatal(err)
	}
	if values, n := profileStoreSnapshot(st); len(values) != 2 || n != puts+2 {
		t.Fatal("atomic batch was not committed")
	}
}

func TestModelProfileRollbackNoOpPreservesPreviousAndCaches(t *testing.T) {
	s, f, st := modelService(t)
	ctx := context.Background()
	binding := ModelBinding{Model: "test-current", ConfigDir: assistantConfigDirLegacy}
	if err := st.PutSetting(ctx, modelRecordKey("assistant"), modelRecord{Revision: 4, Binding: binding}); err != nil {
		t.Fatal(err)
	}
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", Model: "test-current"}})
	if err := st.PutSetting(ctx, modelProfileStateKey, modelProfileState{Active: "selected", Previous: &modelProfileImage{
		Profile: "selected", Purposes: map[string]ModelBinding{"assistant": {Model: "test-current"}},
		Launchers: map[string]LauncherModels{"gw": {Model: profileValue("test-current")}}}}); err != nil {
		t.Fatal(err)
	}
	cached := &prompts.Snapshot{Now: time.Now(), Zone: "UTC"}
	s.live.snap, s.live.at, s.prefs.at = cached, time.Now(), time.Now()
	cacheAt, prefsAt := s.live.at, s.prefs.at
	before, puts := profileStoreSnapshot(st.memStore)
	if sw, err := s.rollbackModelProfile(ctx, ProfileActor{}); err != nil || len(sw.Changed) != 0 {
		t.Fatalf("no-op rollback: %+v %v", sw, err)
	}
	requireProfileStoreUnchanged(t, st.memStore, before, puts)
	if s.live.snap != cached || s.live.at != cacheAt || s.prefs.at != prefsAt || f.eventTypes() != "" || len(f.managed) != 0 {
		t.Fatal("no-op rollback invalidated caches or pushed an event")
	}
}

func TestModelProfileCaptureDirectAPIAndUnrelatedLaunchers(t *testing.T) {
	s, _, _ := modelService(t)
	ctx := context.Background()
	putLaunchers(t, s,
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Kind: "api", Name: "direct", Model: "test-direct", BaseURL: "https://example.invalid", Effort: "high"}},
		storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "unused", Model: "test-unused"}})
	if err := s.saveModel(ctx, "kb", modelRecord{Binding: ModelBinding{Provider: "launcher.direct"}}); err != nil {
		t.Fatal(err)
	}
	_, launchers, _, err := s.captureModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(launchers) != 1 || launchers["direct"].Model == nil || *launchers["direct"].Model != "test-direct" ||
		launchers["direct"].Effort != nil || launchers["direct"].SmallModel != nil {
		t.Fatalf("capture included unsupported API or unrelated launcher fields: %+v", launchers)
	}
}

func TestModelProfileCaptureDoesNotTurnMissingAliasesIntoClears(t *testing.T) {
	s, _, store := modelService(t)
	ctx := t.Context()
	launcher := storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "work", Model: "vendor/main", SonnetModel: "vendor/standard"}}
	putLaunchers(t, s, launcher)
	if err := s.saveModel(ctx, "agent.new", modelRecord{Binding: ModelBinding{Provider: "launcher.work"}}); err != nil {
		t.Fatal(err)
	}
	_, launchers, _, err := s.captureModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	models := launchers["work"]
	if models.SonnetModel == nil || *models.SonnetModel != "vendor/standard" || models.OpusModel != nil ||
		models.HaikuModel != nil || models.FableModel != nil || models.SubagentModel != nil {
		t.Fatal("capture did not distinguish configured aliases from missing aliases")
	}
	storeProfiles(t, store, ModelProfile{ID: "captured", Name: "Captured", Launchers: launchers})
	launcher.OpusModel = "vendor/strong"
	launcher.HaikuModel = "vendor/fast"
	launcher.FableModel = "vendor/fable"
	launcher.SubagentModel = "vendor/helper"
	putLaunchers(t, s, launcher)
	if _, err := s.applyModelProfile(ctx, "captured", ProfileActor{Kind: "hub"}); err != nil {
		t.Fatal(err)
	}
	got := s.storedLaunchers(ctx)[0]
	if got.SonnetModel != "vendor/standard" || got.OpusModel != "vendor/strong" || got.HaikuModel != "vendor/fast" ||
		got.FableModel != "vendor/fable" || got.SubagentModel != "vendor/helper" {
		t.Fatalf("profile switch erased aliases set after capture: %+v", got)
	}
}

func TestModelProfileSwitchAllowsMessagesEffort(t *testing.T) {
	for _, tc := range []struct{ name, api, effort string }{
		{name: "messages", api: "messages", effort: "low"},
		{name: "legacy empty protocol", api: "", effort: "off"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, st := modelService(t)
			key, err := s.sealKey("test-key")
			if err != nil {
				t.Fatal(err)
			}
			putLaunchers(t, s, storedLauncher{
				ManagedLauncher: proto.ManagedLauncher{Kind: "api", Name: "direct", API: tc.api, Model: "test-direct", BaseURL: "https://example.invalid"},
				KeySealed:       key,
			})
			storeProfiles(t, st, ModelProfile{ID: "selected", Name: "Selected", Purposes: map[string]ModelBinding{
				"inbox.triage": {Provider: "launcher.direct", Effort: tc.effort},
			}})
			if _, err := s.applyModelProfile(context.Background(), "selected", ProfileActor{}); err != nil {
				t.Fatalf("switch Messages profile: %v", err)
			}
			b, err := s.resolveModel(context.Background(), "inbox.triage")
			if err != nil || b.Provider != "launcher.direct" || b.Effort != tc.effort {
				t.Fatalf("profile effort did not survive: %+v, %v", b, err)
			}
		})
	}
}

func TestModelProfileUnrelatedDefaultsStayIntact(t *testing.T) {
	s, f, st := modelService(t)
	ctx := context.Background()
	f.hello.Models.AI = ai.Config{API: "responses", BaseURL: "https://example.invalid", APIKey: "test-key", Model: "test-default"}
	for id, b := range map[string]ModelBinding{
		"assistant":      {Provider: "launcher.claude", Model: "test-current-sol", ConfigDir: assistantConfigDirLegacy},
		"agent.new":      {Provider: "launcher.claude", Model: "test-new-sol"},
		"agent.delegate": {Model: "test-delegate"},
	} {
		if err := st.PutSetting(ctx, modelRecordKey(id), modelRecord{Revision: 9, Binding: b}); err != nil {
			t.Fatal(err)
		}
	}
	storeProfiles(t, st, ModelProfile{ID: "selected", Name: "Selected", Purposes: map[string]ModelBinding{"kb": {Model: "test-kb"}}})
	before, _ := profileStoreSnapshot(st.memStore)
	for _, rollback := range []bool{false, true} {
		var err error
		if rollback {
			_, err = s.rollbackModelProfile(ctx, ProfileActor{})
		} else {
			_, err = s.applyModelProfile(ctx, "selected", ProfileActor{})
		}
		if err != nil {
			t.Fatal(err)
		}
		after, _ := profileStoreSnapshot(st.memStore)
		for _, id := range []string{"assistant", "agent.new", "agent.delegate"} {
			if string(before[modelRecordKey(id)]) != string(after[modelRecordKey(id)]) {
				t.Fatalf("partial profile reset %s's default", id)
			}
		}
	}
}

func TestModelProfileCannotClearTriageWhilePluginsNeedIt(t *testing.T) {
	s, f, st := modelService(t)
	ctx := context.Background()
	f.needTriage = []string{"Test inbox plugin"}
	if err := st.PutSetting(ctx, modelRecordKey("inbox.triage"), modelRecord{Binding: ModelBinding{Provider: "launcher.current", Model: "test-current"}}); err != nil {
		t.Fatal(err)
	}
	storeProfiles(t, st, ModelProfile{ID: "selected", Name: "Selected", Purposes: map[string]ModelBinding{"inbox.triage": {}}})
	before, puts := profileStoreSnapshot(st.memStore)
	if _, err := s.applyModelProfile(ctx, "selected", ProfileActor{}); err == nil || !strings.Contains(err.Error(), "Test inbox plugin") {
		t.Fatalf("triage safety check was bypassed: %v", err)
	}
	requireProfileStoreUnchanged(t, st.memStore, before, puts)
}

// recordReadStore counts the reads of each category's record (modelRecordKey) on top of a downStore.
type recordReadStore struct {
	*downStore
	mu    sync.Mutex
	reads map[string]int // category id -> reads
}

func (st *recordReadStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	if i := slices.IndexFunc(modelCategoryIDs, func(id string) bool { return modelRecordKey(id) == key }); i >= 0 {
		st.mu.Lock()
		st.reads[modelCategoryIDs[i]]++
		st.mu.Unlock()
	}
	return st.downStore.Setting(ctx, key, out)
}

// takeReads is the reads counted since the last call, by category id.
func (st *recordReadStore) takeReads() map[string]int {
	st.mu.Lock()
	defer st.mu.Unlock()
	reads := st.reads
	st.reads = map[string]int{}
	return reads
}

// countRecordReads makes s read its settings through a recordReadStore on st.
func countRecordReads(s *Service, st *downStore) *recordReadStore {
	counter := &recordReadStore{downStore: st, reads: map[string]int{}}
	s.setStore(func() Store { return counter })
	return counter
}

// storeManyProfiles stores a record for every category and n profiles that each name every category: the even ones
// as the records are (the main chat's profile for new conversations aside), the odd ones with other kb and
// agent.delegate choices. It returns the Changes each profile's view shows.
func storeManyProfiles(tb testing.TB, st *downStore, n int) [][]string {
	tb.Helper()
	ctx := context.Background()
	records := map[string]ModelBinding{
		"assistant":      {Provider: "launcher.current", Model: "test-chat", ConfigDir: assistantConfigDirLegacy},
		"agent.new":      {Provider: "launcher.current", Model: "test-agents"},
		"kb":             {Model: "test-kb"},
		"inbox.triage":   {Effort: "high"},
		"agent.delegate": {Model: "test-delegate"},
		"translate":      {Model: "test-translation"},
	}
	for id, b := range records {
		if err := st.PutSetting(ctx, modelRecordKey(id), modelRecord{Revision: 2, Binding: b}); err != nil {
			tb.Fatal(err)
		}
	}
	profiles, want := make([]ModelProfile, n), make([][]string, n)
	for i := range profiles {
		purposes := maps.Clone(records)
		purposes["assistant"] = ModelBinding{Provider: "launcher.current", Model: "test-chat"}
		want[i] = []string{}
		if i%2 == 1 {
			purposes["kb"], purposes["agent.delegate"] = ModelBinding{Model: "test-kb-other"}, ModelBinding{}
			want[i] = []string{"kb", "agent.delegate"}
		}
		profiles[i] = ModelProfile{ID: fmt.Sprintf("profile-%d", i), Name: fmt.Sprintf("Profile %d", i), Purposes: purposes}
	}
	if err := st.PutSetting(ctx, modelProfilesKey, modelProfiles{Revision: 1, Profiles: profiles}); err != nil {
		tb.Fatal(err)
	}
	return want
}

// The view compares every profile with the configuration, but reads each category's record once, not once per
// profile (P x 5 point reads under models.mu before).
func TestModelProfilesViewReadsEachRecordOnce(t *testing.T) {
	for _, n := range []int{2, 4, 8} {
		t.Run(fmt.Sprintf("%d profiles", n), func(t *testing.T) {
			s, _, st := modelService(t)
			want := storeManyProfiles(t, st, n)
			counter := countRecordReads(s, st)
			v := profilesOf(t, s)
			if reads := counter.takeReads(); len(reads) != len(modelCategoryIDs) || slices.ContainsFunc(modelCategoryIDs, func(id string) bool { return reads[id] != 1 }) {
				t.Fatalf("record reads = %v, want each of %v once", reads, modelCategoryIDs)
			}
			if len(v.Profiles) != n {
				t.Fatalf("profiles = %d, want %d", len(v.Profiles), n)
			}
			for i, p := range v.Profiles {
				if !reflect.DeepEqual(p.Changes, want[i]) {
					t.Fatalf("%s changes = %v, want %v", p.ID, p.Changes, want[i])
				}
			}
		})
	}

	// A category no profile names is not read.
	s, _, st := modelService(t)
	storeProfiles(t, st, ModelProfile{ID: "one", Name: "One", Purposes: map[string]ModelBinding{"kb": {Model: "test-kb"}}},
		ModelProfile{ID: "two", Name: "Two", Purposes: map[string]ModelBinding{"kb": {}, "assistant": {Model: "test-chat"}}})
	counter := countRecordReads(s, st)
	v := profilesOf(t, s)
	if reads := counter.takeReads(); !maps.Equal(reads, map[string]int{"assistant": 1, "kb": 1}) {
		t.Fatalf("record reads = %v, want assistant and kb once", reads)
	}
	if !reflect.DeepEqual(v.Profiles[0].Changes, []string{"kb"}) || !reflect.DeepEqual(v.Profiles[1].Changes, []string{"assistant"}) {
		t.Fatalf("changes = %v, %v", v.Profiles[0].Changes, v.Profiles[1].Changes)
	}

	// An explicit capture reads every record once (inbox.triage twice more for the delegation and translation defaults).
	s, _, st = modelService(t)
	counter = countRecordReads(s, st)
	capture := map[string]int{"assistant": 1, "agent.new": 1, "kb": 1, "inbox.triage": 3, "agent.delegate": 1, "translate": 1}
	if _, _, _, err := s.captureModels(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reads := counter.takeReads(); !maps.Equal(reads, capture) {
		t.Fatalf("capture record reads = %v, want %v", reads, capture)
	}
	if v := profilesOf(t, s); len(v.Profiles) != 0 {
		t.Fatalf("unconfigured view: %+v", v.Profiles)
	}
	if reads := counter.takeReads(); len(reads) != 0 {
		t.Fatalf("empty profile view read live model records: %v", reads)
	}
}

// BenchmarkModelProfilesView is GET /api/v1/model-profiles' view on memStore with P stored profiles that each name
// every category; record-reads/op is the category record reads it makes.
func BenchmarkModelProfilesView(b *testing.B) {
	for _, n := range []int{2, 4, 8} {
		b.Run(fmt.Sprintf("profiles=%d", n), func(b *testing.B) {
			st := &downStore{memStore: &memStore{}}
			storeManyProfiles(b, st, n)
			s := New(Options{Hub: newFakeHub(), Store: func() Store { return st }})
			counter := countRecordReads(s, st)
			ctx := context.Background()
			b.ReportAllocs()
			for b.Loop() {
				if _, err := s.modelProfilesView(ctx); err != nil {
					b.Fatal(err)
				}
			}
			reads := 0
			for _, r := range counter.takeReads() {
				reads += r
			}
			b.ReportMetric(float64(reads)/float64(b.N), "record-reads/op")
		})
	}
}

func TestModelProfileRoundTripRollbackPreservesPreSwitchConfig(t *testing.T) {
	t.Run("identical stale templates A->B->A step-by-step rollback", func(t *testing.T) {
		s, _, st := modelService(t)
		ctx := context.Background()
		key, err := s.sealKey("test-key")
		if err != nil {
			t.Fatal(err)
		}
		putLaunchers(t, s, storedLauncher{
			ManagedLauncher: proto.ManagedLauncher{Name: "gw", API: "messages", Model: "test-default", BaseURL: "https://example.invalid"},
			KeySealed:       key,
		})
		// Store legacy templates with explicit empty bindings, as installed hubs did in #14060.
		purposes, launchers, tiers, err := s.captureModels(ctx)
		if err != nil {
			t.Fatal(err)
		}
		storeProfiles(t, st, ModelProfile{ID: "performance", Name: "Performance", Purposes: purposes, Launchers: launchers, Tiers: tiers},
			ModelProfile{ID: "economy", Name: "Economy", Purposes: purposes, Launchers: launchers, Tiers: tiers})
		if err := st.PutSetting(ctx, modelProfileStateKey, modelProfileState{Active: "performance"}); err != nil {
			t.Fatal(err)
		}
		// User customizes five live categories without updating the stale templates.
		live0 := map[string]ModelBinding{
			"assistant":      {Provider: "launcher.gw", Model: "live-chat"},
			"agent.new":      {Provider: "launcher.gw", Model: "live-agents"},
			"kb":             {Provider: "launcher.gw", Model: "live-kb"},
			"inbox.triage":   {Provider: "launcher.gw", Model: "live-triage", Effort: "off"},
			"agent.delegate": {Provider: "launcher.gw", Model: "live-delegate"},
		}
		for id, b := range live0 {
			if err := st.PutSetting(ctx, modelRecordKey(id), modelRecord{Revision: 3, Binding: b}); err != nil {
				t.Fatal(err)
			}
		}

		// Quick round-trip switch: performance -> economy -> performance overwrites all 5 purposes with empty bindings.
		if r := request(s, http.MethodPost, "/api/v1/model-profiles/economy/apply", ""); r.Code != http.StatusOK {
			t.Fatalf("apply economy: %d %s", r.Code, r.Body.String())
		}
		if r := request(s, http.MethodPost, "/api/v1/model-profiles/performance/apply", ""); r.Code != http.StatusOK {
			t.Fatalf("apply performance: %d %s", r.Code, r.Body.String())
		}
		for id := range live0 {
			if got, err := s.modelRecord(ctx, id); err != nil || got.Binding != (ModelBinding{}) {
				t.Fatalf("expected stale template to clear %s, got %+v (%v)", id, got, err)
			}
		}

		v := profilesOf(t, s)
		if v.Active != "performance" || len(v.Rollbacks) != 2 || v.Rollback == nil {
			t.Fatalf("expected 2 rollback points after A->B->A: %+v", v)
		}
		if v.Rollbacks[0].To != "economy" || len(v.Rollbacks[0].Changes) != 0 ||
			v.Rollbacks[1].To != "performance" || !slices.Equal(v.Rollbacks[1].Changes, []string{"agent.delegate", "agent.new", "assistant", "inbox.triage", "kb"}) {
			t.Fatalf("unexpected rollback previews: %+v", v.Rollbacks)
		}

		// Step-by-step rollback through the existing body-less endpoint restores economy, then original live0.
		if r := request(s, http.MethodPost, "/api/v1/model-profiles/rollback", ""); r.Code != http.StatusOK {
			t.Fatalf("first rollback: %d %s", r.Code, r.Body.String())
		}
		v = profilesOf(t, s)
		if v.Active != "economy" || len(v.Rollbacks) != 1 || v.Rollback == nil || v.Rollback.To != "performance" || len(v.Rollback.Changes) != 5 {
			t.Fatalf("after first rollback: %+v", v)
		}
		if r := request(s, http.MethodPost, "/api/v1/model-profiles/rollback", ""); r.Code != http.StatusOK {
			t.Fatalf("second rollback: %d %s", r.Code, r.Body.String())
		}
		v = profilesOf(t, s)
		if v.Active != "performance" || v.Rollback == nil || !v.Rollback.Redo {
			t.Fatalf("after second rollback: %+v", v)
		}
		for id, want := range live0 {
			if got, err := s.modelRecord(ctx, id); err != nil || got.Binding != want {
				t.Fatalf("pre-switch %s not restored: got %+v, want %+v (%v)", id, got, want, err)
			}
		}
	})

	t.Run("distinct templates direct jump composes purposes and launcher fields", func(t *testing.T) {
		s, _, st := modelService(t)
		ctx := context.Background()
		key, err := s.sealKey("test-key")
		if err != nil {
			t.Fatal(err)
		}
		putLaunchers(t, s, storedLauncher{
			ManagedLauncher: proto.ManagedLauncher{Name: "gw", API: "messages", Model: "live-gw", SmallModel: "live-small", BaseURL: "https://example.invalid"},
			KeySealed:       key,
		})
		live0 := map[string]ModelBinding{
			"assistant": {Provider: "launcher.gw", Model: "live-chat"},
			"agent.new": {Provider: "launcher.gw", Model: "live-agent"},
			"kb":        {Provider: "launcher.gw", Model: "live-kb"},
		}
		for id, b := range live0 {
			if err := st.PutSetting(ctx, modelRecordKey(id), modelRecord{Revision: 2, Binding: b}); err != nil {
				t.Fatal(err)
			}
		}
		storeProfiles(t, st,
			ModelProfile{
				ID: "performance", Name: "Performance",
				Purposes:  map[string]ModelBinding{"assistant": {Provider: "launcher.gw", Model: "perf-chat"}, "agent.new": {Provider: "launcher.gw", Model: "perf-agent"}},
				Launchers: map[string]LauncherModels{"gw": {SmallModel: profileValue("perf-small")}},
			},
			ModelProfile{
				ID: "economy", Name: "Economy",
				Purposes:  map[string]ModelBinding{"assistant": {Provider: "launcher.gw", Model: "econ-chat"}, "kb": {Provider: "launcher.gw", Model: "econ-kb"}},
				Launchers: map[string]LauncherModels{"gw": {Model: profileValue("econ-gw")}},
			},
		)
		if err := st.PutSetting(ctx, modelProfileStateKey, modelProfileState{Active: "performance"}); err != nil {
			t.Fatal(err)
		}

		// performance (live0) -> economy -> performance (stale perf) touches overlapping and disjoint fields.
		if _, err := s.applyModelProfile(ctx, "economy", ProfileActor{Kind: "hub"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.applyModelProfile(ctx, "performance", ProfileActor{Kind: "hub"}); err != nil {
			t.Fatal(err)
		}
		v := profilesOf(t, s)
		if len(v.Rollbacks) != 2 {
			t.Fatalf("rollbacks = %+v, want 2", v.Rollbacks)
		}
		oldest := v.Rollbacks[1]
		wantChanges := []string{"agent.new", "assistant", "kb", "gw.model", "gw.small_model"}
		if oldest.To != "performance" || !slices.Equal(oldest.Changes, wantChanges) {
			t.Fatalf("composed rollback preview = %+v, want To=performance Changes=%v", oldest, wantChanges)
		}

		// Jump directly to the pre-round-trip point with an extra unknown optional field from a newer client.
		body := fmt.Sprintf(`{"id":%q,"future_client_hint":"v2"}`, oldest.ID)
		if r := request(s, http.MethodPost, "/api/v1/model-profiles/rollback", body); r.Code != http.StatusOK {
			t.Fatalf("direct rollback: %d %s", r.Code, r.Body.String())
		}
		for id, want := range live0 {
			if got, err := s.modelRecord(ctx, id); err != nil || got.Binding != want {
				t.Fatalf("direct rollback %s = %+v, want %+v (%v)", id, got, want, err)
			}
		}
		gw := s.storedLaunchers(ctx)[0]
		if gw.Model != "live-gw" || gw.SmallModel != "live-small" {
			t.Fatalf("direct rollback launcher = %+v, want live-gw/live-small", gw.ManagedLauncher)
		}
		// A second rollback redoes back to the post-switch state cleanly.
		if _, err := s.rollbackModelProfile(ctx, ProfileActor{Kind: "hub"}); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.modelRecord(ctx, "assistant"); got.Binding.Model != "perf-chat" {
			t.Fatalf("redo assistant = %+v, want perf-chat", got)
		}
		if got, _ := s.modelRecord(ctx, "kb"); got.Binding.Model != "econ-kb" {
			t.Fatalf("redo kb = %+v, want econ-kb", got)
		}
	})

	t.Run("new forward switch clears redo entry", func(t *testing.T) {
		s, _, st := modelService(t)
		ctx := context.Background()
		putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", Model: "live-model"}})
		storeProfiles(t, st,
			ModelProfile{ID: "economy", Name: "Economy", Launchers: map[string]LauncherModels{"gw": {Model: profileValue("econ-model")}}},
			ModelProfile{ID: "budget", Name: "Budget", Launchers: map[string]LauncherModels{"gw": {Model: profileValue("budget-model")}}},
		)
		if _, err := s.applyModelProfile(ctx, "economy", ProfileActor{Kind: "hub"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.rollbackModelProfile(ctx, ProfileActor{Kind: "hub"}); err != nil {
			t.Fatal(err)
		}
		if v := profilesOf(t, s); v.Rollback == nil || !v.Rollback.Redo {
			t.Fatalf("expected redo entry after rolling back only switch: %+v", v)
		}
		if _, err := s.applyModelProfile(ctx, "budget", ProfileActor{Kind: "hub"}); err != nil {
			t.Fatal(err)
		}
		v := profilesOf(t, s)
		if len(v.Rollbacks) != 1 || v.Rollbacks[0].Redo || v.Rollbacks[0].To != "" {
			t.Fatalf("expected forward switch to replace redo branch: %+v", v.Rollbacks)
		}
		if _, err := s.rollbackModelProfile(ctx, ProfileActor{Kind: "hub"}); err != nil {
			t.Fatal(err)
		}
		if got := s.storedLaunchers(ctx)[0].Model; got != "live-model" {
			t.Fatalf("rollback after forward switch restored %q, want live-model", got)
		}
	})

	t.Run("legacy Previous-only storage and unknown rollback id", func(t *testing.T) {
		s, _, st := modelService(t)
		ctx := context.Background()
		putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", Model: "after"}})
		if err := st.PutSetting(ctx, modelProfileStateKey, modelProfileState{
			Active: "economy",
			Previous: &modelProfileImage{
				Profile:   "performance",
				At:        time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC),
				Launchers: map[string]LauncherModels{"gw": {Model: profileValue("before")}},
			},
		}); err != nil {
			t.Fatal(err)
		}
		v := profilesOf(t, s)
		if v.Rollback == nil || v.Rollback.ID != legacyRollbackID || len(v.Rollbacks) != 1 || v.Rollbacks[0].To != "performance" {
			t.Fatalf("legacy Previous view: %+v", v)
		}
		before, puts := profileStoreSnapshot(st.memStore)
		if r := request(s, http.MethodPost, "/api/v1/model-profiles/rollback", `{"id":"missing"}`); r.Code != http.StatusConflict {
			t.Fatalf("unknown rollback id: %d %s", r.Code, r.Body.String())
		}
		requireProfileStoreUnchanged(t, st.memStore, before, puts)
		if r := request(s, http.MethodPost, "/api/v1/model-profiles/rollback", `{"id":"previous"}`); r.Code != http.StatusOK {
			t.Fatalf("legacy rollback id: %d %s", r.Code, r.Body.String())
		}
		if got := s.storedLaunchers(ctx)[0].Model; got != "before" {
			t.Fatalf("legacy rollback model = %q, want before", got)
		}
	})
}

func TestModelProfileRollbackHistoryBounded(t *testing.T) {
	s, _, st := modelService(t)
	ctx := context.Background()
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", Model: "m-0"}})
	profiles := make([]ModelProfile, 0, maxProfileRollbacks+4)
	for i := 1; i <= maxProfileRollbacks+4; i++ {
		profiles = append(profiles, ModelProfile{
			ID:        fmt.Sprintf("p%d", i),
			Name:      fmt.Sprintf("Profile %d", i),
			Launchers: map[string]LauncherModels{"gw": {Model: profileValue(fmt.Sprintf("m-%d", i))}},
		})
	}
	storeProfiles(t, st, profiles...)
	var droppedID string
	for i := 1; i <= maxProfileRollbacks+4; i++ {
		if _, err := s.applyModelProfile(ctx, fmt.Sprintf("p%d", i), ProfileActor{Kind: "hub"}); err != nil {
			t.Fatalf("switch %d: %v", i, err)
		}
		if i == 1 {
			droppedID = profilesOf(t, s).Rollbacks[0].ID
		}
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Rollbacks) != maxProfileRollbacks {
		t.Fatalf("stored rollbacks = %d, want %d", len(state.Rollbacks), maxProfileRollbacks)
	}
	if state.Previous == nil || state.Previous.ID != state.Rollbacks[maxProfileRollbacks-1].ID {
		t.Fatalf("Previous out of sync with top rollback: prev=%+v top=%+v", state.Previous, state.Rollbacks[maxProfileRollbacks-1])
	}
	v := profilesOf(t, s)
	if len(v.Rollbacks) != maxProfileRollbacks || v.Rollback == nil || v.Rollback.ID != v.Rollbacks[0].ID {
		t.Fatalf("view rollbacks = %d, want %d", len(v.Rollbacks), maxProfileRollbacks)
	}
	// Evicted rollback ID is rejected cleanly with 409.
	if r := request(s, http.MethodPost, "/api/v1/model-profiles/rollback", fmt.Sprintf(`{"id":%q}`, droppedID)); r.Code != http.StatusConflict {
		t.Fatalf("evicted rollback id %q: %d %s", droppedID, r.Code, r.Body.String())
	}
	// Unwinding all maxProfileRollbacks steps reaches m-4 (the state before switch 5).
	for step := maxProfileRollbacks + 3; step >= 4; step-- {
		if _, err := s.rollbackModelProfile(ctx, ProfileActor{Kind: "hub"}); err != nil {
			t.Fatalf("rollback to m-%d: %v", step, err)
		}
		if got := s.storedLaunchers(ctx)[0].Model; got != fmt.Sprintf("m-%d", step) {
			t.Fatalf("after rollback got %q, want m-%d", got, step)
		}
	}
}

func TestPresetHostProfileRoundTrip(t *testing.T) {
	s, _, _ := modelService(t)
	for _, tc := range []struct {
		name, fields, want string
		status             int
	}{
		{name: "set", fields: `,"host":" herdr "`, want: "herdr", status: http.StatusOK},
		{name: "omit", want: "herdr", status: http.StatusOK},
		{name: "invalid", fields: `,"host":"tmux"`, want: "herdr", status: http.StatusBadRequest},
		{name: "clear", fields: `,"host":""`, status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"revision":%d,"name":"Host preset","tiers":{"agy-work":{"launcher":"agy"%s}}}`, profilesOf(t, s).Revision, tc.fields)
			res := request(s, http.MethodPut, "/api/v1/model-profiles/host-preset", body)
			if res.Code != tc.status {
				t.Fatalf("PUT = %d %s, want %d", res.Code, res.Body, tc.status)
			}
			ps, _ := storedTiers(t, s)
			tier, ok := tierOf(ps, "host-preset", "agy-work")
			if !ok || tier.startHost() != tc.want {
				t.Fatalf("stored tier = %+v, want host %q", tier, tc.want)
			}
		})
	}
}

func TestModelProfilePutCrossVersionTolerantDecoding(t *testing.T) {
	s, _, st := modelService(t)
	key, err := s.sealKey("test-key")
	if err != nil {
		t.Fatal(err)
	}
	putLaunchers(t, s, storedLauncher{
		ManagedLauncher: proto.ManagedLauncher{Name: "gw", API: "messages", Model: "base-model", BaseURL: "https://example.invalid"},
		KeySealed:       key,
	})
	v := profilesOf(t, s)

	// Unknown optional fields from newer clients at top level, purposes, launchers, and tiers are ignored cleanly.
	tolerantBody := fmt.Sprintf(`{
		"revision": %d,
		"name": "Cross Version",
		"description": "Saved from newer client",
		"schema_version": 2,
		"ui_color": "amber",
		"purposes": {
			"assistant": {"provider": "launcher.gw", "model": "claude-opus-5-5", "effort": "high", "permission_mode": "auto", "thinking_budget": 4096, "future_flags": {"streaming": true}},
			"kb": {"provider": "launcher.gw", "model": "claude-haiku-5-5", "future_opt": "ignored"}
		},
		"launchers": {
			"gw": {"model": "base-model", "future_alias": "experimental"}
		},
		"tiers": {
			"strong": {"launcher": "gw", "model": "claude-opus-5-5", "speed": "fast", "description": "Primary tier", "future_tier_opt": 123}
		}
	}`, v.Revision)
	r := request(s, http.MethodPut, "/api/v1/model-profiles/cross", tolerantBody)
	if r.Code != http.StatusOK {
		t.Fatalf("tolerant PUT failed: %d %s", r.Code, r.Body.String())
	}
	rawStored := rawSetting(t, s, modelProfilesKey)
	for _, unknown := range []string{"schema_version", "ui_color", "thinking_budget", "future_flags", "future_opt", "future_alias", "future_tier_opt"} {
		if strings.Contains(rawStored, unknown) {
			t.Fatalf("unknown field %q was persisted: %s", unknown, rawStored)
		}
	}
	v = profilesOf(t, s)
	idx := slices.IndexFunc(v.Profiles, func(p ModelProfileView) bool { return p.ID == "cross" })
	if idx < 0 || v.Profiles[idx].Purposes["assistant"].Model != "claude-opus-5-5" ||
		v.Profiles[idx].Purposes["assistant"].PermissionMode != "auto" ||
		v.Profiles[idx].Tiers["core"].Speed != "fast" || !slices.Contains(v.Profiles[idx].Tiers["core"].Aliases, "strong") {
		// a tier named strong is read back as core, with strong kept as its alias (#14700)
		t.Fatalf("known fields lost on tolerant save: %+v", v.Profiles)
	}

	// Capture with unknown optional fields also succeeds.
	captureBody := fmt.Sprintf(`{"revision":%d,"name":"Captured Future","capture":true,"future_capture_mode":"full"}`, v.Revision)
	if r := request(s, http.MethodPut, "/api/v1/model-profiles/cross", captureBody); r.Code != http.StatusOK {
		t.Fatalf("tolerant capture PUT failed: %d %s", r.Code, r.Body.String())
	}
	rev := profilesOf(t, s).Revision

	// Invalid JSON, trailing junk, non-object shapes, invalid known fields, and forbidden credential/endpoint fields are rejected.
	rejectCases := []struct {
		name   string
		method string
		path   string
		body   string
		status int
	}{
		{"empty body", http.MethodPut, "/api/v1/model-profiles/cross", "", http.StatusBadRequest},
		{"null body", http.MethodPut, "/api/v1/model-profiles/cross", "null", http.StatusBadRequest},
		{"array body", http.MethodPut, "/api/v1/model-profiles/cross", "[]", http.StatusBadRequest},
		{"malformed json", http.MethodPut, "/api/v1/model-profiles/cross", `{"revision":1,"name":"X"`, http.StatusBadRequest},
		{"trailing junk", http.MethodPut, "/api/v1/model-profiles/cross", fmt.Sprintf(`{"revision":%d,"name":"X"} trailing`, rev), http.StatusBadRequest},
		{"second json object", http.MethodPut, "/api/v1/model-profiles/cross", fmt.Sprintf(`{"revision":%d,"name":"X"}{"extra":1}`, rev), http.StatusBadRequest},
		{"wrong revision type", http.MethodPut, "/api/v1/model-profiles/cross", `{"revision":"1","name":"X"}`, http.StatusBadRequest},
		{"negative revision", http.MethodPut, "/api/v1/model-profiles/cross", `{"revision":-1,"name":"X"}`, http.StatusBadRequest},
		{"wrong name type", http.MethodPut, "/api/v1/model-profiles/cross", fmt.Sprintf(`{"revision":%d,"name":123}`, rev), http.StatusBadRequest},
		{"invalid effort with unknown field", http.MethodPut, "/api/v1/model-profiles/cross", fmt.Sprintf(`{"revision":%d,"name":"X","future_opt":true,"purposes":{"assistant":{"provider":"launcher.gw","effort":"bogus"}}}`, rev), http.StatusBadRequest},
		{"invalid speed with unknown field", http.MethodPut, "/api/v1/model-profiles/cross", fmt.Sprintf(`{"revision":%d,"name":"X","future_opt":true,"tiers":{"strong":{"launcher":"gw","speed":"warp"}}}`, rev), http.StatusBadRequest},
		{"invalid category with unknown field", http.MethodPut, "/api/v1/model-profiles/cross", fmt.Sprintf(`{"revision":%d,"name":"X","future_opt":true,"purposes":{"unknown.cat":{"model":"m"}}}`, rev), http.StatusBadRequest},
		{"config_dir still rejected", http.MethodPut, "/api/v1/model-profiles/cross", fmt.Sprintf(`{"revision":%d,"name":"X","purposes":{"assistant":{"config_dir":"legacy"}}}`, rev), http.StatusBadRequest},
		{"forbidden top-level api_key", http.MethodPut, "/api/v1/model-profiles/cross", fmt.Sprintf(`{"revision":%d,"name":"X","api_key":"secret"}`, rev), http.StatusBadRequest},
		{"forbidden uppercase API_KEY", http.MethodPut, "/api/v1/model-profiles/cross", fmt.Sprintf(`{"revision":%d,"name":"X","API_KEY":"secret"}`, rev), http.StatusBadRequest},
		{"forbidden nested launcher base_url", http.MethodPut, "/api/v1/model-profiles/cross", fmt.Sprintf(`{"revision":%d,"name":"X","launchers":{"gw":{"model":"m","base_url":"https://evil.invalid"}}}`, rev), http.StatusBadRequest},
		{"forbidden nested purpose token", http.MethodPut, "/api/v1/model-profiles/cross", fmt.Sprintf(`{"revision":%d,"name":"X","purposes":{"assistant":{"model":"m","token":"secret"}}}`, rev), http.StatusBadRequest},
		{"forbidden nested tier headers", http.MethodPut, "/api/v1/model-profiles/cross", fmt.Sprintf(`{"revision":%d,"name":"X","tiers":{"strong":{"launcher":"gw","headers":"X-Key: 1"}}}`, rev), http.StatusBadRequest},
		{"oversized body", http.MethodPut, "/api/v1/model-profiles/cross", fmt.Sprintf(`{"revision":%d,"name":"X","pad":%q}`, rev, strings.Repeat("a", 70<<10)), http.StatusRequestEntityTooLarge},
		{"rollback malformed json", http.MethodPost, "/api/v1/model-profiles/rollback", `{bad`, http.StatusBadRequest},
		{"rollback null body", http.MethodPost, "/api/v1/model-profiles/rollback", `null`, http.StatusBadRequest},
		{"rollback trailing junk", http.MethodPost, "/api/v1/model-profiles/rollback", `{} extra`, http.StatusBadRequest},
	}
	for _, tc := range rejectCases {
		t.Run(tc.name, func(t *testing.T) {
			before, puts := profileStoreSnapshot(st.memStore)
			res := request(s, tc.method, tc.path, tc.body)
			if res.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", res.Code, tc.status, res.Body.String())
			}
			requireProfileStoreUnchanged(t, st.memStore, before, puts)
		})
	}
}

func TestModelProfileLegacyModelOnlyBindingMigration(t *testing.T) {
	t.Run("display save capture apply and rollback normalize legacy model-only bindings", func(t *testing.T) {
		s, _, st := modelService(t)
		ctx := context.Background()
		key, err := s.sealKey("test-key")
		if err != nil {
			t.Fatal(err)
		}
		putLaunchers(t, s,
			storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: "gw", API: "messages", Model: "gw-default", BaseURL: "https://gw.example.invalid"}, KeySealed: key},
			storedLauncher{ManagedLauncher: proto.ManagedLauncher{Kind: "api", Name: "api-default", API: "messages", Model: "api-model", BaseURL: "https://api.example.invalid"}, KeySealed: key},
			storedLauncher{ManagedLauncher: proto.ManagedLauncher{Kind: "api", Name: "triage-gw", API: "messages", Model: "triage-model", BaseURL: "https://triage.example.invalid"}, KeySealed: key},
		)
		setSettings(t, s, func(p *Settings) { p.Launcher = "gw" })

		// Live assistant already has the explicit provider+model; live inbox.triage points to api-default.
		if err := st.PutSetting(ctx, modelRecordKey("assistant"), modelRecord{Revision: 1, Binding: ModelBinding{Provider: "launcher.gw", Model: "chat-model"}}); err != nil {
			t.Fatal(err)
		}
		if err := st.PutSetting(ctx, modelRecordKey("inbox.triage"), modelRecord{Revision: 1, Binding: ModelBinding{Provider: "launcher.api-default", Model: "old-triage"}}); err != nil {
			t.Fatal(err)
		}

		// Stored legacy profile carries model-only bindings (no provider), an empty "follow default" binding, and an effort-only binding.
		storeProfiles(t, st,
			ModelProfile{
				ID: "legacy", Name: "Legacy",
				Purposes: map[string]ModelBinding{
					"assistant":      {Model: "chat-model"},                             // resolves to launcher.gw (matches live!)
					"agent.new":      {Model: "agent-model"},                            // resolves to launcher.claude
					"kb":             {Model: "kb-model"},                               // resolves to launcher.api-default
					"inbox.triage":   {Provider: "launcher.triage-gw", Model: "fast-m"}, // explicit provider in profile
					"agent.delegate": {Model: "delegate-model"},                         // must follow profile's inbox.triage (launcher.triage-gw), not live!
					"translate":      {Model: "translate-model"},                        // must follow profile's inbox.triage (launcher.triage-gw)
				},
			},
			ModelProfile{
				ID: "preserve", Name: "Preserve",
				Purposes: map[string]ModelBinding{
					"assistant":    {},               // empty = follow default, must stay empty
					"inbox.triage": {Effort: "high"}, // effort-only, must stay inherited
				},
			},
		)

		// 1. Read/display normalizes legacy model-only bindings without writing to storage or reporting phantom drift.
		before, puts := profileStoreSnapshot(st.memStore)
		v := profilesOf(t, s)
		requireProfileStoreUnchanged(t, st.memStore, before, puts)

		legacyView := v.Profiles[0]
		wantPurposes := map[string]ModelBinding{
			"assistant":      {Provider: "launcher.gw", Model: "chat-model"},
			"agent.new":      {Provider: "launcher.claude", Model: "agent-model"},
			"kb":             {Provider: "launcher.api-default", Model: "kb-model"},
			"inbox.triage":   {Provider: "launcher.triage-gw", Model: "fast-m"},
			"agent.delegate": {Provider: "launcher.triage-gw", Model: "delegate-model"},
			"translate":      {Provider: "launcher.triage-gw", Model: "translate-model"},
		}
		if !reflect.DeepEqual(legacyView.Purposes, wantPurposes) {
			t.Fatalf("normalized display purposes = %+v, want %+v", legacyView.Purposes, wantPurposes)
		}
		// assistant matches live {Provider: "launcher.gw", Model: "chat-model"}, so no phantom drift on assistant.
		if slices.Contains(legacyView.Changes, "assistant") {
			t.Fatalf("phantom drift reported on matching legacy assistant binding: %v", legacyView.Changes)
		}
		// Empty and effort-only bindings are preserved untouched.
		preserveView := v.Profiles[1]
		if preserveView.Purposes["assistant"] != (ModelBinding{}) || preserveView.Purposes["inbox.triage"] != (ModelBinding{Effort: "high"}) {
			t.Fatalf("non-model-only bindings mutated: %+v", preserveView.Purposes)
		}

		// 2. Apply writes explicit providers to live records and keeps assistant untouched (no downgrade to "").
		if _, err := s.applyModelProfile(ctx, "legacy", ProfileActor{Kind: "hub"}); err != nil {
			t.Fatalf("apply legacy: %v", err)
		}
		for id, want := range wantPurposes {
			if got, err := s.modelRecord(ctx, id); err != nil || got.Binding != want {
				t.Fatalf("applied %s = %+v, want %+v (%v)", id, got, want, err)
			}
		}

		// 3. PUT with legacy model-only bindings persists explicit providers in storage.
		putBody := fmt.Sprintf(`{"revision":%d,"name":"Migrated","purposes":{"assistant":{"model":"saved-chat"},"agent.delegate":{"model":"saved-del"}}}`, profilesOf(t, s).Revision)
		if r := request(s, http.MethodPut, "/api/v1/model-profiles/migrated", putBody); r.Code != http.StatusOK {
			t.Fatalf("PUT migrated: %d %s", r.Code, r.Body.String())
		}
		storedProfiles, _, err := s.readModelProfiles(ctx)
		if err != nil {
			t.Fatal(err)
		}
		mi := slices.IndexFunc(storedProfiles.Profiles, func(p ModelProfile) bool { return p.ID == "migrated" })
		if mi < 0 || storedProfiles.Profiles[mi].Purposes["assistant"] != (ModelBinding{Provider: "launcher.gw", Model: "saved-chat"}) ||
			storedProfiles.Profiles[mi].Purposes["agent.delegate"] != (ModelBinding{Provider: "launcher.triage-gw", Model: "saved-del"}) {
			t.Fatalf("persisted migrated profile = %+v", storedProfiles.Profiles[mi])
		}

		// 4. capture: true migrates live legacy model-only records into explicit providers.
		if err := st.PutSetting(ctx, modelRecordKey("kb"), modelRecord{Revision: 5, Binding: ModelBinding{Model: "live-legacy-kb"}}); err != nil {
			t.Fatal(err)
		}
		captureBody := fmt.Sprintf(`{"revision":%d,"name":"Migrated","capture":true}`, profilesOf(t, s).Revision)
		if r := request(s, http.MethodPut, "/api/v1/model-profiles/migrated", captureBody); r.Code != http.StatusOK {
			t.Fatalf("capture migrated: %d %s", r.Code, r.Body.String())
		}
		storedProfiles, _, err = s.readModelProfiles(ctx)
		if err != nil {
			t.Fatal(err)
		}
		mi = slices.IndexFunc(storedProfiles.Profiles, func(p ModelProfile) bool { return p.ID == "migrated" })
		if storedProfiles.Profiles[mi].Purposes["kb"] != (ModelBinding{Provider: "launcher.api-default", Model: "live-legacy-kb"}) {
			t.Fatalf("captured legacy live kb = %+v", storedProfiles.Profiles[mi].Purposes["kb"])
		}
	})

	t.Run("unconfigured API baseline preserves model without inventing ghost legacy provider", func(t *testing.T) {
		s, _, st := modelService(t)
		storeProfiles(t, st, ModelProfile{
			ID: "no-api", Name: "No API",
			Purposes: map[string]ModelBinding{"kb": {Model: "custom-kb-model"}},
		})
		v := profilesOf(t, s)
		if got := v.Profiles[0].Purposes["kb"]; got != (ModelBinding{Model: "custom-kb-model"}) {
			t.Fatalf("unconfigured API binding = %+v, want Provider empty and Model preserved", got)
		}
	})
}
