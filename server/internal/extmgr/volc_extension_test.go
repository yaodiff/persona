package extmgr

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/builtinext"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/modules/volc/volctest"
	"github.com/nwverse/persona/server/internal/moduleswitch"
)

// needsVolcModule is the extension its module provides, or skips the test in a build without the module
// (persona_without_volc, persona_core_only); TestNoVolcWithoutItsModule covers that build.
func needsVolcModule(t *testing.T) builtinext.Volc {
	t.Helper()
	volc, ok := builtinext.VolcSpeech(moduleswitch.Set{})
	if !ok {
		t.Skip("the volc module is not in this build")
	}
	return volc
}

// Without its module, or with it switched off in the `modules` setting the manager started with, the extension is not
// there: nothing registers it, nothing names it a transcription, and the assistant's settings tool knows no meetings
// switch of it.
func TestNoVolcWithoutItsModule(t *testing.T) {
	h := newTestManager(t)
	if _, ok := builtinext.VolcSpeech(moduleswitch.Set{}); ok { // built in: switched off at run time
		h.opts.Modules = moduleswitch.Off("volc")
	}
	h.store = newExtensionStore()
	startTestExtensions(t, h)
	if p := h.extensions.reg.Get("volc"); p != nil {
		t.Fatalf("%+v", p.Record)
	}
	if volcRef(h.modules()) != "" || isVolc(h.modules(), nil) || h.IsTranscriptionContribution("volc/volc") {
		t.Fatal("the extension is named without its module")
	}
}

// The manifest is one an installed extension could have: it validates, its form is in the subset, the key is a
// secret shown by its last four characters, it says the one host it reaches, and its text is in every language.
func TestVolcManifest(t *testing.T) {
	volc := needsVolcModule(t)
	h := newTestManager(t)
	h.version = "1.2.3"
	raw, _ := json.Marshal(volcManifest(h, volc))
	m, err := extension.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !m.SettingsCheck || !slices.Equal(m.Activation(), []string{extension.OnStartup}) || !slices.Equal(m.Permissions.Network, []string{volcHost}) ||
		m.Permissions.FullHub() || m.Runtime.RPC != "" || !m.Runtime.HTTP {
		t.Fatalf("%+v", m)
	}
	if !slices.Equal(m.Items(transcriptionPoint), []string{"volc"}) || !slices.Equal(m.Items(speechPoint), []string{"volc"}) ||
		!slices.Equal(m.Items("ui"), []string{volc.StatusPage}) {
		t.Fatalf("contributes %v", m.Contributions())
	}
	if len(m.Secrets) != 1 || m.Secrets[0].Name != volc.SecretKey || m.Secrets[0].Required || m.Secrets[0].Hint != "last4" {
		t.Fatalf("secrets %+v", m.Secrets)
	}
	schema, err := extension.ParseSchema(m.Configuration)
	if err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{`{}`, `{"meetings":true,"read_answers":false,"voice":"zh_male_m191_uranus_bigtts"}`,
		`{"advanced":{"asr_model":"volc.bigasr.sauc.concurrent","voice_id":"S_abc.12-x","app_id":"1234567"}}`} {
		if err := schema.Check(json.RawMessage(ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{`{"voice":"alloy"}`, `{"advanced":{"asr_model":"volc.other"}}`, `{"advanced":{"voice_id":"has space"}}`,
		`{"meetings":"yes"}`, `{"key":"in the config"}`} {
		if err := schema.Check(json.RawMessage(bad)); err == nil {
			t.Errorf("%s: taken", bad)
		}
	}
	ts := volcTranslations(volc)
	for _, l := range []i18n.Lang{i18n.ZhHans, i18n.Ja} {
		f := ts[l].Configuration
		if ts[l].Name == "" || f["meetings"].Confirm == "" || f["advanced"].Properties["asr_model"].Choices["volc.seedasr.sauc.duration"] == "" ||
			f["voice"].Choices[volc.Voices[0]] == "" || ts.UITitle(l, volc.StatusPage, "") == "" {
			t.Errorf("%s: %+v", l, ts[l])
		}
	}
	if ts.Name(i18n.ZhHans, m.Name) != "火山语音" || ts.Name(i18n.En, m.Name) != "Volcengine Speech" { // i18n-ignore: the catalog's name
		t.Errorf("names %q %q", ts.Name(i18n.ZhHans, m.Name), ts.Name(i18n.En, m.Name))
	}
}

// volcHub is a hub with the built-in extension and a fake assistant that keeps the meeting transcription choice as
// the real one does; its program reaches the stand-in service.
func volcHub(t *testing.T) (*Manager, *volctest.Service, *assistantwire.MeetingTranscriptionChoice) {
	t.Helper()
	needsVolcModule(t)
	svc := volctest.New(t)
	t.Setenv(volctest.EnvService, svc.URL)
	h := newTestManager(t)
	h.store = newExtensionStore()
	a := withAssistant(h)
	choice := &assistantwire.MeetingTranscriptionChoice{Default: "device"}
	a.on(assistantwire.MethodMeetingTranscriptionChoice, func(raw json.RawMessage) (any, error) {
		var p assistantwire.MeetingTranscriptionChoose
		_ = json.Unmarshal(raw, &p)
		switch {
		case p.Set && p.Use:
			choice.Extension, choice.Default = p.Extension, "hub"
		case p.Set && choice.Extension == p.Extension:
			choice.Extension, choice.Default = "", "device"
		}
		return *choice, nil
	})
	startTestExtensions(t, h)
	return h, svc, choice
}

// Off until turned on; on without a key it runs and says what it needs; a key is checked on the service before it is
// saved (a refused one is not saved, and the refusal says what to do without the key in it), then sealed and handed to
// the program; its transcription and speech answer through the hub.
func TestVolcSetup(t *testing.T) {
	h, svc, _ := volcHub(t)
	volc := needsVolcModule(t)
	mux := h.routes()
	tok := h.cfg.Token
	base := "/api/v1/extensions/volc/-/"
	p := h.extensions.reg.Get(volc.ExtensionID)
	if p == nil || p.Record.Source != extension.SourceBuiltin || p.Record.Enabled {
		t.Fatalf("%+v", p)
	}
	if v := viewOf(h, volc.ExtensionID); v.Status != extensionDisabled {
		t.Fatalf("%+v", v)
	}
	if code, out := call(t, mux, "PUT", base+"enabled", tok, `{"enabled": true}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	v := waitVoiceStatus(t, h, volc.ExtensionID, extensionNeedsSetup)
	if !slices.Contains(v.NeedsSetup, i18n.T(i18n.Default, "hub-volc_extension.setup_key")) {
		t.Fatalf("%+v", v)
	}
	waitLong(t, "the program to run", func() bool {
		hp := h.extensions.get(volc.ExtensionID)
		return hp != nil && hp.proc != nil && hp.healthy(context.Background())
	})
	if !h.IsTranscriptionContribution(volcRef(moduleswitch.Set{})) {
		t.Fatal("volc/volc is not a transcription contribution")
	}
	waitLong(t, "the transcription's info without a key", func() bool {
		info, err := h.TranscriptionInfo(context.Background(), volcRef(moduleswitch.Set{}))
		return err == nil && info.Setup == "key"
	})

	// A refused key: not saved; the words say what to do, without the key.
	svc.Set(func(s *volctest.Script) {
		s.RefuseASR, s.RefuseBody = 401, `{"code":45000010,"message":"invalid api key"}`
		s.TTSStatus, s.TTSBody = 401, `{"code":45000010,"message":"invalid api key"}`
	})
	code, out := call(t, mux, "PUT", base+"secrets/key", tok, `{"value": "wrong-key-0123456789"}`)
	msg, _ := out["error"].(string)
	if code != 400 || out["code"] != "check_failed" || !strings.Contains(msg, i18n.T(i18n.Default, "volc-check.why_forbidden")) || strings.Contains(msg, "wrong-key") {
		t.Fatalf("%d %v", code, out)
	}
	if h.extensions.reg.SecretsSet(context.Background(), h.extensions.reg.Get(volc.ExtensionID))[volc.SecretKey] {
		t.Fatal("a refused key was saved")
	}

	svc.Set(func(s *volctest.Script) { s.RefuseASR, s.TTSStatus = 0, 0 })
	code, out = call(t, mux, "PUT", base+"secrets/key", tok, `{"value": "test-key-0123456789"}`)
	raw, _ := json.Marshal(out)
	if code != 200 || !secretSet(out, volc.SecretKey) || strings.Contains(string(raw), "test-key-0123456789") || !strings.Contains(string(raw), `"hint":"6789"`) {
		t.Fatalf("%d %s", code, raw)
	}
	if s := svc.Session(svc.Count() - 1); s == nil || s.Header.Get("X-Api-Key") != "test-key-0123456789" || len(svc.Syntheses()) == 0 {
		t.Fatalf("the check did not reach the service: %+v", s)
	}
	v = waitVoiceStatus(t, h, volc.ExtensionID, host.StateRunning)
	if len(v.NeedsSetup) != 0 {
		t.Fatalf("%+v", v)
	}
	waitLong(t, "the restarted program's transcription", func() bool {
		info, err := h.TranscriptionInfo(context.Background(), volcRef(moduleswitch.Set{}))
		return err == nil && info.Setup == "" && slices.Contains(info.Features, "interim")
	})

	// Speech through the apps' forward: the voice extension's read-aloud contract.
	code, info := call(t, mux, "GET", "/api/v1/extensions/volc/speech/volc/info", tok, "")
	if code != 200 || info["protocol"] != 1.0 || info["rate"] != 24000.0 || info["setup"] != nil {
		t.Fatalf("speech info %d %v", code, info)
	}
	req := httptest.NewRequest("POST", "/api/v1/extensions/volc/speech/volc/speak", strings.NewReader(`{"text":"好的"}`)) // i18n-ignore: test data
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 200 || w.Header().Get("Content-Type") != "audio/pcm" || w.Header().Get("X-Persona-Audio-Rate") != "24000" || w.Body.Len() != 2*480 {
		t.Fatalf("speak %d %v %d bytes", w.Code, w.Header(), w.Body.Len())
	}

	// The page, in the request's language.
	req = httptest.NewRequest("GET", "/api/v1/extensions/-/ui?placement=extension_page", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept-Language", "ja")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	var places struct {
		UI []UIPlace `json:"ui"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &places)
	i := slices.IndexFunc(places.UI, func(u UIPlace) bool { return u.Extension == volc.ExtensionID })
	if i < 0 || places.UI[i].Title != i18n.T(i18n.Ja, "hub-volc_extension.manifest_page") || places.UI[i].ExtensionName != i18n.T(i18n.Ja, "hub-volc_extension.manifest_name") {
		t.Fatalf("%s", w.Body.Bytes())
	}
}

// The meetings switch is the assistant's choice: turning it on chooses the extension (and new meetings on the hub),
// off goes back; it shows the choice as it is, also when Recording settings changed it; it is never saved with the
// extension's settings, a save that changes only it does not restart the program, and the assistant's own tool cannot
// set it.
func TestVolcMeetingsSwitch(t *testing.T) {
	h, _, choice := volcHub(t)
	volc := needsVolcModule(t)
	mux := h.routes()
	tok := h.cfg.Token
	base := "/api/v1/extensions/volc/-/"
	if code, out := call(t, mux, "PUT", base+"enabled", tok, `{"enabled": true}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	waitLong(t, "the program to run", func() bool {
		hp := h.extensions.get(volc.ExtensionID)
		return hp != nil && hp.proc != nil && hp.healthy(context.Background())
	})
	config := func(out map[string]any) map[string]any { c, _ := out["config"].(map[string]any); return c }

	code, out := call(t, mux, "PUT", base+"config", tok, `{"config": {"meetings": true, "voice": "zh_male_m191_uranus_bigtts"}}`)
	if code != 200 || config(out)["meetings"] != true || choice.Extension != "volc/volc" || choice.Default != "hub" {
		t.Fatalf("on: %d %v, choice %+v", code, out, choice)
	}
	saved := string(h.extensions.reg.SavedConfig(h.extensions.reg.Get(volc.ExtensionID)))
	if strings.Contains(saved, "meetings") || !strings.Contains(saved, "zh_male_m191_uranus_bigtts") {
		t.Fatalf("saved %s", saved)
	}

	// Recording settings choose another: the switch shows off.
	choice.Extension, choice.Default = "", "hub"
	if code, out := call(t, mux, "GET", base, tok, ""); code != 200 || config(out)["meetings"] != false {
		t.Fatalf("after another choice: %d %v", code, config(out))
	}
	// A save that changes only the switch (back on) restarts nothing.
	hp := h.extensions.get(volc.ExtensionID)
	proc := hp.proc
	code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"meetings": true, "voice": "zh_male_m191_uranus_bigtts"}}`)
	if code != 200 || choice.Extension != "volc/volc" || h.extensions.get(volc.ExtensionID).proc != proc {
		t.Fatalf("switch only: %d %v, choice %+v", code, out, choice)
	}
	// Off: back to the built-in one and the phone.
	code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"meetings": false, "voice": "zh_male_m191_uranus_bigtts"}}`)
	if code != 200 || config(out)["meetings"] != false || choice.Extension != "" || choice.Default != "device" {
		t.Fatalf("off: %d %v, choice %+v", code, out, choice)
	}

	// The assistant's settings tool: the switch is neither listed nor set.
	text, err := h.extensionSettingsText(volc.ExtensionID)
	if err != nil || strings.Contains(text, "- meetings") || !strings.Contains(text, "read_answers") {
		t.Fatalf("%v\n%s", err, text)
	}
	if _, err := h.setExtensionSettings(volc.ExtensionID, "x", json.RawMessage(`{"meetings": true}`)); err == nil {
		t.Fatal("the assistant's tool set the meetings switch")
	}
	// The row in the store never holds the switch either (#16263, plan §14-1): a package's schema has no meetings key,
	// so a row holding one could not be handed off or adopted.
	stored := h.store.(*extensionStore)
	stored.mu.Lock()
	row := stored.rows[volc.ExtensionID]
	stored.mu.Unlock()
	if row.Source != extension.SourceBuiltin || strings.Contains(string(row.Config), "meetings") || !strings.Contains(string(row.Config), "zh_male_m191_uranus_bigtts") {
		t.Fatalf("stored row %s %s", row.Source, row.Config)
	}
}
