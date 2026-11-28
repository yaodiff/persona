package assistant

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestExternalUnreadablePickBookCannotFallBackAnExternalConversation(t *testing.T) {
	s, f, l := externalPickFixture(t)
	pickExternal(t, s, l, "vendor/model-one")
	f.setSessions("server", proto.Session{SessionID: "main", Assistant: true, Launcher: l.Name})
	st := &downStore{memStore: s.store().(*memStore)}
	again := newServiceOn(t, f, st)
	_ = again.settings(t.Context())
	_ = again.isGuest(t.Context(), "server", "main")
	st.failing(true)
	for _, params := range []map[string]any{{}, {"model": "vendor/model-one"}} {
		if err := again.sendModel(t.Context(), "server", "main", params, i18n.En); err == nil || params["launcher"] != nil || params["external"] != nil {
			t.Fatalf("an unreadable external selection fell back: %v %v", params, err)
		}
	}
	st.failing(false)
	params, err := send(t, again, "main", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	externalExecutionOf(t, params, l)
}

func TestExternalCannotBeConfiguredForAnyPurpose(t *testing.T) {
	s, _, l := externalPickFixture(t)
	binding := ModelBinding{Provider: launcherProvider(l.Name), Model: "vendor/model-one"}
	st := s.store().(*memStore)
	_ = s.modelProviders(t.Context())
	_ = s.taskViews(t.Context())
	// Provider discovery also initializes the enabled node's external directory asynchronously.
	// Finish that observable cache write before checking that every rejected edit writes nothing.
	waitFor(t, "the fixture's external directory initialization", func() bool {
		s.models.zshrcMu.Lock()
		defer s.models.zshrcMu.Unlock()
		return s.models.externalRefresh["server"] == nil && slices.ContainsFunc(s.models.zshrc["server"], func(x proto.Launcher) bool { return x.Name == l.Name })
	})
	s.waitWorkEvents()
	before, puts := profileStoreSnapshot(st)
	for _, p := range modelPurposes {
		if err := s.saveModelRecord(t.Context(), p.ID, 0, binding); err == nil {
			t.Fatalf("external default saved for %s", p.ID)
		}
		if err := s.validateModel(t.Context(), p, binding, nil); err == nil {
			t.Fatalf("external default validated for %s", p.ID)
		}
	}
	if _, err := s.apiConfig(t.Context(), binding); err == nil {
		t.Fatal("external connection became a direct API/CLI call")
	}
	for _, id := range []string{"assistant", "agent.new", "inbox.triage", "kb", "supervise.background"} {
		r := modelRequest(s, "PUT", id, fmt.Sprintf(`{"revision":0,"provider":%q,"model":"vendor/model-one"}`, binding.Provider))
		if r.Code == 200 {
			t.Fatalf("external default saved over HTTP for %s: %s", id, r.Body.String())
		}
	}
	r := request(s, "PUT", "/api/v1/assistant", fmt.Sprintf(`{"launcher":%q}`, l.Name))
	if r.Code != 400 {
		t.Fatalf("assistant launcher: %d %s", r.Code, r.Body.String())
	}
	if _, err := s.updateSettings(t.Context(), func(v *Settings) error { v.Launcher = l.Name; return nil }); err == nil {
		t.Fatal("external connection entered assistant settings directly")
	}
	value, _ := json.Marshal(l.Name)
	if _, err := s.setLauncher(t.Context(), value); err == nil {
		t.Fatal("settings tool offered external as the global default")
	}
	requireProfileStoreUnchanged(t, st, before, puts)
}

func TestExternalStoredConfigurationIsRejectedAtResolution(t *testing.T) {
	for _, id := range []string{"assistant", "agent.new", "inbox.triage", "kb", purposeSupervise, purposeLarkAgent} {
		t.Run(id, func(t *testing.T) {
			s, _, l := externalPickFixture(t)
			if err := s.store().PutSetting(t.Context(), modelRecordKey(id), modelRecord{Revision: 3,
				Binding: ModelBinding{Provider: launcherProvider(l.Name), Model: "vendor/model-one"}}); err != nil {
				t.Fatal(err)
			}
			_, err := s.resolveModel(t.Context(), id)
			requireExternalError(t, err, "purpose_unsupported")
			if err := s.applyModel(t.Context(), id, map[string]any{}); err == nil {
				t.Fatal("stored external purpose reached execution")
			}
		})
	}
	// Legacy hub/settings defaults cannot become an external fallback either.
	s, f, l := externalPickFixture(t)
	if err := s.store().PutSetting(t.Context(), modelRecordKey("assistant"), modelRecord{}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.hello.Assistant.Launcher = l.Name
	f.mu.Unlock()
	s.prefs.mu.Lock()
	s.prefs.cached = &Settings{Main: "main", MachineID: "server", Launcher: l.Name}
	s.prefs.mu.Unlock()
	_, err := s.resolveModel(t.Context(), "assistant")
	requireExternalError(t, err, "purpose_unsupported")
}

func TestExternalCannotEnterProfilesTiersOrManagedProviders(t *testing.T) {
	s, f, l := externalPickFixture(t)
	binding := ModelBinding{Provider: launcherProvider(l.Name), Model: "vendor/model-one"}
	for _, p := range []ModelProfile{
		{ID: "sample", Name: "Sample", Purposes: map[string]ModelBinding{"assistant": binding}},
		{ID: "sample", Name: "Sample", Launchers: map[string]LauncherModels{l.Name: {Model: profileValue("vendor/model-one")}}},
		{ID: "sample", Name: "Sample", Tiers: map[string]ModelTier{"standard": {Launcher: l.Name, Model: "vendor/model-one"}}},
	} {
		p := p
		requireExternalError(t, checkModelProfile(&p, nil), "purpose_unsupported")
		if _, err := s.saveModelProfile(t.Context(), nil, "sample", ProfileActor{Kind: "user"}, func(out *ModelProfile, _ bool) error {
			*out = p
			return nil
		}); err == nil {
			t.Fatalf("external connection entered a saved profile: %+v", p)
		}
	}
	requireExternalError(t, checkModelTiers(map[string]ModelTier{"standard": {Launcher: l.Name}}), "purpose_unsupported")
	requireExternalError(t, s.tierLauncherCheck(t.Context(), l.Name), "purpose_unsupported")
	requireExternalError(t, launcherInput{Name: l.Name}.check(), "purpose_unsupported")
	if _, err := s.saveLauncher(t.Context(), launcherInput{Name: l.Name}, nil); err == nil {
		t.Fatal("external namespace became a hub-managed provider")
	}
	for _, kind := range []string{"api", "launcher"} {
		r := request(s, "POST", "/api/v1/launchers", fmt.Sprintf(`{"name":%q,"kind":%q,"base_url":"https://example.invalid","model":"vendor/model-one"}`, l.Name, kind))
		if r.Code != 400 {
			t.Fatalf("external namespace provider %s: %d %s", kind, r.Code, r.Body.String())
		}
	}
	// Old persisted profiles and rollback images must fail at use, not merely at save.
	st := s.store().(*memStore)
	if err := st.PutSettings(t.Context(), map[string]any{
		modelProfilesKey: modelProfiles{Revision: 1, Profiles: []ModelProfile{{ID: "old", Name: "Old",
			Tiers: map[string]ModelTier{"standard": {Launcher: l.Name}}}}},
		modelProfileStateKey: modelProfileState{Active: "old", Previous: &modelProfileImage{Purposes: map[string]ModelBinding{"assistant": binding}}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := s.pickTierCandidate(t.Context(), "standard", tierFacts{})
	requireExternalError(t, err, "purpose_unsupported")
	if _, err := s.applyModelProfile(t.Context(), "old", ProfileActor{Kind: "user"}); err == nil {
		t.Fatal("an old external profile became active")
	}
	if _, err := s.rollbackModelProfile(t.Context(), ProfileActor{Kind: "user"}); err == nil {
		t.Fatal("an external rollback image became a default")
	}
	if len(f.sends) != 0 || len(f.starts) != 0 {
		t.Fatal("invalid external configuration reached execution")
	}
}

func TestExternalCodingAndResumeRequestsAreRefused(t *testing.T) {
	s, f, l := externalPickFixture(t)
	for _, resume := range []string{"", "thread-id"} {
		_, err := s.AgentStartParams(t.Context(), AgentStartParams{MachineID: "server", Params: map[string]any{
			"launcher": l.Name, "resume_session_id": resume, "external": &proto.ExternalExecution{Scope: proto.ExternalMainChatScope}}})
		if err == nil {
			t.Fatalf("coding start accepted external (resume=%q)", resume)
		}
	}
	if _, err := s.AgentResumeParams(t.Context(), AgentResumeParams{MachineID: "server", SessionID: "agent", Params: map[string]any{
		"launcher": l.Name, "external": &proto.ExternalExecution{Scope: proto.ExternalMainChatScope}}}); err == nil {
		t.Fatal("coding resume accepted external")
	}
	p, err := s.AgentStartParams(t.Context(), AgentStartParams{MachineID: "server", Params: map[string]any{
		"launcher": "claude", "external": map[string]any{"scope": proto.ExternalMainChatScope}}})
	if err != nil || p["external"] != nil || p["launcher"] != "claude" {
		t.Fatalf("ordinary coding semantics changed: %v %v", p, err)
	}
	if len(f.starts) != 0 || len(f.sends) != 0 {
		t.Fatal("refused coding request ran")
	}
}

func TestExternalNeverEntersAutomaticFallbackCandidates(t *testing.T) {
	s, _, l := externalPickFixture(t)
	fakeShell := l
	fakeShell.Source = "path" // the reserved identity still wins over a stale/misclassified source
	for _, list := range [][]proto.Launcher{{l}, {fakeShell}, {l, {Name: "ordinary", Source: "path"}}} {
		if got := launcherInstead(Device{}, list); proto.IsExternalLauncher(got) {
			t.Fatalf("external launcher chosen as an Agent fallback: %s", got)
		}
	}
	if got := s.mainChatInstead(t.Context(), Device{ID: "server"}, "missing", "claude", []ConfiguredLauncher{{Launcher: l, AgentType: "claude"}}, []string{l.Name}); got != "" {
		t.Fatalf("external launcher chosen as main-chat fallback: %s", got)
	}
	withoutList, _ := s.launcherWithoutInventory(t.Context(), "mac", l.Name)
	if startableLauncher(l) || startableLauncher(fakeShell) || withoutList.agent != "" {
		t.Fatal("reserved external identity was treated as an ordinary automatic launcher")
	}
	if !startableLauncher(proto.Launcher{Name: "external_example", Source: "path"}) {
		t.Fatal("the reserved prefix accidentally captured an ordinary shell launcher")
	}
	// A corrupt old managed record cannot participate in an API fallback.
	putLaunchers(t, s, storedLauncher{ManagedLauncher: proto.ManagedLauncher{Name: l.Name, BaseURL: "https://example.invalid", Model: "vendor/model-one"}, KeySealed: "sealed"})
	if got := s.fallbackAPIProvider(t.Context()); got != "" {
		t.Fatalf("external launcher chosen as API fallback: %s", got)
	}
	if names, _, err := s.healthLaunchers(t.Context(), "server"); err != nil || slices.Contains(names, l.Name) {
		t.Fatalf("reserved managed record became a health candidate: %v %v", names, err)
	}
	// The manual availability-test endpoint is not permission to execute this source.
	r := request(s, "POST", "/api/v1/launchers/"+l.Name+"/check", "")
	if r.Code != 400 {
		t.Fatalf("external availability probe: %d %s", r.Code, r.Body.String())
	}
}
