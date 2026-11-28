package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/builtinext"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/moduleswitch"
)

// voiceTestStore is what the built-in voice extension's tests run on: the hub's store with the extensions' tables.
type voiceTestStore interface {
	Store
	extension.Store
}

// onVoiceStores runs f on the in-memory store, and on Postgres when PERSONA_TEST_DATABASE_URL is set
// (scripts/test/test-server.sh): the rows and the sealed key of the extensions ids, cleared before and after.
func onVoiceStores(t *testing.T, ids []string, f func(t *testing.T, store voiceTestStore)) {
	t.Run("memory", func(t *testing.T) { f(t, newExtensionStore()) })
	t.Run("postgres", func(t *testing.T) {
		url := os.Getenv("PERSONA_TEST_DATABASE_URL")
		if url == "" {
			t.Skip("PERSONA_TEST_DATABASE_URL not set (scripts/test/test-server.sh runs this on the hub machine)")
		}
		database, err := db.Open(context.Background(), url, db.ManagerSet)
		if err != nil {
			t.Fatal(err)
		}
		clear := func() {
			for _, id := range ids {
				if err := database.DeleteExtension(context.Background(), id); err != nil {
					t.Error(err)
				}
			}
		}
		clear()
		t.Cleanup(func() {
			clear()
			database.Close()
		})
		f(t, database)
	})
}

// voiceEntry is hub.json's entry of `personad voice`, as written before the built-in took it over.
func voiceEntry(id string) ExtensionConfig {
	return ExtensionConfig{ID: id, Name: "语音", Kind: "voice", Command: "personad", Args: []string{"voice"},
		Env: map[string]string{"VOICE_TEST_ENV": "from-hub-json"},
		Config: json.RawMessage(`{"providers":[{"id":"rt","name":"实时服务","url":"wss://rt.example.com/v1/live","models":["m1"],` +
			`"key_command":"printf hub-json-credential"}],"owner":"hub owner"}`)}
}

// needsVoiceModule is the voice extension its module provides, or skips the test in a build without the module
// (persona_without_voice, persona_core_only); TestNoVoiceWithoutItsModule covers that build.
func needsVoiceModule(t *testing.T) builtinext.Voice {
	t.Helper()
	v, ok := builtinext.VoiceExtension(moduleswitch.Set{})
	if !ok {
		t.Skip("the voice module is not in this build")
	}
	return v
}

// Without its module, or with it switched off in the `modules` setting the manager started with, there is no built-in
// voice extension: none is registered, a hub.json entry running `personad voice` stays hub.json's own (whose program
// this build does not have, or the host does not start), and no meeting transcription is the built-in one.
func TestNoVoiceWithoutItsModule(t *testing.T) {
	h := newTestManager(t)
	_, built := builtinext.VoiceExtension(moduleswitch.Set{})
	if built { // built in: switched off at run time, absent the same way
		h.opts.Modules = moduleswitch.Off("voice")
	}
	h.store = newExtensionStore()
	startTestExtensions(t, h, voiceEntry("talk"))
	if p := h.extensions.reg.Get(voiceExtensionID); p != nil {
		t.Fatalf("%+v", p.Record)
	}
	if p := h.extensions.reg.Get("talk"); p == nil || p.Record.Source == extension.SourceBuiltin {
		t.Fatalf("hub.json's entry: %+v", p)
	}
	if h.extensions.voiceID != "" || takesOverVoice(h.modules(), voiceEntry("talk")) ||
		voiceURL(h.modules(), json.RawMessage(`{"url":"wss://x/v1/live"}`)) != "" ||
		voiceTTSURL(h.modules(), json.RawMessage(`{"tts_url":"https://tts.example.com"}`)) != "" {
		t.Fatal("the built-in voice extension is named without its module")
	}
	if _, _, ok := h.extensions.transcriptionRef(""); ok {
		t.Fatal("the built-in transcription is there without the module")
	}
	if built { // the host does not start the switched-off module's program for hub.json's entry
		p := h.extensions.reg.Get("talk")
		if _, err := h.extensions.specOf(context.Background(), p, &hubExtension{}); err == nil || !strings.Contains(err.Error(), "voice") {
			t.Fatalf("hub.json's personad voice with voice switched off: %v, want it refused", err)
		}
	}
}

// The manager's HTTP surface treats an unavailable built-in voice extension as any other absent extension: its
// detail, settings, secret and proxy routes all return 404 instead of colliding with hub.json's separate entry.
func TestVoiceExtensionRoutesAre404WithoutItsModule(t *testing.T) {
	h := newTestManager(t)
	_, built := builtinext.VoiceExtension(moduleswitch.Set{})
	if built {
		h.opts.Modules = moduleswitch.Off("voice")
	}
	h.store = newExtensionStore()
	startTestExtensions(t, h, voiceEntry("talk"))
	requests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/v1/extensions/voice/-/", ""},
		{http.MethodPut, "/api/v1/extensions/voice/-/enabled", `{"enabled":true}`},
		{http.MethodPut, "/api/v1/extensions/voice/-/config", `{"config":{"url":"wss://live.example.com/v1/live"}}`},
		{http.MethodPut, "/api/v1/extensions/voice/-/secrets/key", `{"value":"secret"}`},
		{http.MethodGet, "/api/v1/extensions/voice/echo", ""},
		{http.MethodGet, "/api/v1/plugins/voice/echo", ""},
	}
	for _, rq := range requests {
		t.Run(rq.method+" "+rq.path, func(t *testing.T) {
			if code, out := call(t, h.routes(), rq.method, rq.path, h.cfg.Token, rq.body); code != http.StatusNotFound {
				t.Fatalf("%s %s = %d %v, want 404", rq.method, rq.path, code, out)
			}
		})
	}
	code, out := call(t, h.routes(), http.MethodGet, "/api/v1/extensions", h.cfg.Token, "")
	if code != http.StatusOK {
		t.Fatalf("generic extension list = %d %v, want 200", code, out)
	}
	var voiceListed, talkListed bool
	items, ok := out["extensions"].([]any)
	if !ok {
		t.Fatalf("generic extension list payload = %v", out)
	}
	for _, item := range items {
		entry, _ := item.(map[string]any)
		switch entry["id"] {
		case voiceExtensionID:
			voiceListed = true
		case "talk":
			talkListed = true
		}
	}
	if voiceListed || !talkListed {
		t.Fatalf("generic extension list = %v: voice absent and separate hub.json entry retained", out["extensions"])
	}
}

func voiceHub(t *testing.T, store voiceTestStore, cfgs ...ExtensionConfig) *Manager {
	needsVoiceModule(t)
	h := newTestManager(t)
	h.store = store
	startTestExtensions(t, h, cfgs...)
	return h
}

// envOf is the environment the hub starts extension id with.
func envOf(t *testing.T, h *Manager, id string) map[string]string {
	t.Helper()
	p := h.extensions.reg.Get(id)
	s, err := h.extensions.spec(context.Background(), p, &hubExtension{id: id})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, kv := range s.Env {
		k, v, _ := strings.Cut(kv, "=")
		out[k] = v
	}
	return out
}

func viewOf(h *Manager, id string) ExtensionView {
	return h.extensions.view(context.Background(), h.extensions.reg.Get(id))
}

func waitVoiceStatus(t *testing.T, h *Manager, id, status string) ExtensionView {
	t.Helper()
	var v ExtensionView
	waitLong(t, id+" "+status, func() bool {
		v = viewOf(h, id)
		return v.Status == status
	})
	return v
}

func secretSet(out map[string]any, name string) bool {
	secrets, _ := out["secrets"].([]any)
	for _, s := range secrets {
		if m := s.(map[string]any); m["name"] == name {
			return m["set"] == true
		}
	}
	return false
}

// The manifest is one an installed extension could have: it validates, its form is in the subset, the key is a
// secret, and what runs commands or reaches other machines is hub.json's only.
func TestVoiceManifest(t *testing.T) {
	v := needsVoiceModule(t)
	h := newTestManager(t)
	h.version = "1.2.3"
	m := voiceManifest(h, v, "voice", "")
	raw, _ := json.Marshal(m)
	parsed, err := extension.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Kind() != "voice" || parsed.Name != "实时语音" || !parsed.Permissions.FullHub() || !slices.Equal(parsed.Activation(), []string{extension.OnStartup}) {
		t.Fatalf("%+v", parsed)
	}
	if len(parsed.Secrets) != 2 || parsed.Secrets[0].Name != voiceSecretKey || parsed.Secrets[0].Required ||
		parsed.Secrets[1].Name != voiceTTSSecretKey || parsed.Secrets[1].Required {
		t.Fatalf("the keys are optional secrets: %+v", parsed.Secrets)
	}
	schema, err := extension.ParseSchema(parsed.Configuration)
	if err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{`{}`, `{"url":"wss://live.example.com/v1/live","dialect":"codex","models":["gpt-live-1"],"input_rate":24000,"owner":"me"}`,
		`{"url":"ws://gateway.example.com:8080/v1/live"}`,
		`{"tts_url":"https://tts.example.com","tts_model":"default","tts_default_voice":"af_heart","tts_sample_rate":24000}`,
		`{"tts_url":"http://127.0.0.1:8880/prefix/v1/audio/speech","tts_model":"org/model-v1","tts_sample_rate":8000}`,
		`{"tts_url":"http://[::1]:8880","tts_sample_rate":48000}`} {
		if err := schema.Check(json.RawMessage(ok)); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{`{"url":"https://live.example.com"}`, `{"url":"wss://"}`, `{"dialect":"fake"}`, `{"input_rate":8000}`,
		`{"models":["has space"]}`, `{"key_command":"cat ~/.ssh/id_rsa"}`, `{"ssh":"elsewhere"}`, `{"key_file":"/etc/shadow"}`,
		`{"tools":true}`, `{"providers":[]}`, `{"key":"in the config"}`, `{"tts_key":"in the config"}`,
		`{"tts_url":"wss://tts.example.com"}`, `{"tts_url":"https://"}`, `{"tts_url":"https://user:pass@tts.example.com"}`,
		`{"tts_url":"https://tts.example.com?k=1"}`, `{"tts_model":"has space"}`,
		`{"tts_default_voice":"has space"}`, `{"tts_sample_rate":7999}`, `{"tts_sample_rate":48001}`} {
		if err := schema.Check(json.RawMessage(bad)); err == nil {
			t.Errorf("%s: taken", bad)
		}
	}
	ts := voiceTranslations()
	for _, l := range []i18n.Lang{i18n.ZhHans, i18n.Ja} {
		if got := ts.Secret(l, parsed.Secrets[1]); got.Title != i18n.T(l, "hub-voice_extension.manifest_tts_key_title") {
			t.Errorf("%s tts_key title = %q", l, got.Title)
		}
		loc := ts.Schema(l, schema)
		var foundTTSURL bool
		for _, f := range loc.Fields {
			if f.Name == "tts_url" {
				foundTTSURL = f.Title == i18n.T(l, "hub-voice_extension.manifest_tts_url_title")
			}
		}
		if !foundTTSURL {
			t.Errorf("%s tts_url field not translated", l)
		}
	}
	if m := voiceManifest(h, v, "talk", "语音"); m.ID != "talk" || m.Name != "语音" || m.Items("voice")[0] != "talk" {
		t.Fatalf("%+v", m)
	}
}

// hub.json's `personad voice` entry becomes the built-in under its own id (its model choices, plugin.<id>, stay): its
// config reaches the program as PERSONA_VOICE_HUB_CONFIG with its env, the form's settings and key as any extension's;
// the config never goes out of the hub. Upgrading a hub with the entry and no row: on, as it was.
func TestVoiceTakesOverTheHubJSONEntry(t *testing.T) {
	onVoiceStores(t, []string{"talk", "node-voice"}, func(t *testing.T, store voiceTestStore) {
		node := ExtensionConfig{ID: "node-voice", Kind: "voice", Command: "/usr/bin/node", Args: []string{"main.mjs"}, Disabled: true}
		second := voiceEntry("talk2")
		h := voiceHub(t, store, voiceEntry("talk"), second, node)
		reg := h.extensions.reg
		p := reg.Get("talk")
		if p == nil || p.Record.Source != extension.SourceBuiltin || !p.Record.Enabled || p.Manifest.Kind() != "voice" || p.Full.Name != "语音" {
			t.Fatalf("taken over: %+v", p)
		}
		if reg.Get(voiceExtensionID) != nil {
			t.Fatal("the entry is the built-in: no second one")
		}
		if q := reg.Get("talk2"); q == nil || q.Record.Source != extension.SourceHubJSON {
			t.Fatal("only the first entry is taken over")
		}
		if q := reg.Get("node-voice"); q == nil || q.Record.Source != extension.SourceHubJSON {
			t.Fatal("another program stays hub.json's")
		}

		env := envOf(t, h, "talk")
		if env["PERSONA_VOICE_HUB_CONFIG"] != string(voiceEntry("talk").Config) || env["VOICE_TEST_ENV"] != "from-hub-json" ||
			env["PERSONA_EXTENSION_SECRETS"] != "{}" || env["PERSONA_HUB_TOKEN"] != h.cfg.Token || env["PERSONA_EXTENSION_ID"] != "talk" ||
			env["PERSONA_EXTENSION_SOCKET"] != filepath.Join(h.extensions.root, "talk.sock") {
			t.Fatalf("env %v", env)
		}
		var form map[string]any
		if err := json.Unmarshal([]byte(env["PERSONA_EXTENSION_CONFIG"]), &form); err != nil || form["name"] != "GPT-Live" || form["dialect"] != "live" || form["providers"] != nil {
			t.Fatalf("the form's settings with their defaults: %v %v", env["PERSONA_EXTENSION_CONFIG"], err)
		}

		// It runs (the test binary as `personad voice`), hub.json's upstream serving: no setup needed.
		v := waitVoiceStatus(t, h, "talk", host.StateRunning)
		if v.Kind != "voice" || v.Source != extension.SourceBuiltin || !v.Enabled || len(v.NeedsSetup) != 0 {
			t.Fatalf("%+v", v)
		}
		mux := h.routes()
		code, models := call(t, mux, "GET", "/api/v1/extensions/talk/models", h.cfg.Token, "")
		if code != 200 || models["default"] != "rt/m1" {
			t.Fatalf("models %d %v", code, models)
		}
		code, info := call(t, mux, "GET", "/api/v1/extensions/talk/info", h.cfg.Token, "")
		if code != 200 || info["setup"] != nil || info["protocol"] == nil {
			t.Fatalf("info %d %v", code, info)
		}
		code, detail := call(t, mux, "GET", "/api/v1/extensions/talk/-/", h.cfg.Token, "")
		raw, _ := json.Marshal(detail)
		if code != 200 || detail["read_only"] == true || detail["form"] == nil || secretSet(detail, voiceSecretKey) ||
			strings.Contains(string(raw), "hub-json-credential") || strings.Contains(string(raw), "rt.example.com") {
			t.Fatalf("detail %d %s", code, raw)
		}
		if choices, err := h.extensionModelChoices(context.Background(), "plugin.talk"); err != nil || choices.Default != "rt/m1" {
			t.Fatalf("plugin.talk keeps its model choices: %+v %v", choices, err)
		}
	})
}

// hub.json's disabled: always off, whatever the row says; switching it on says why. The hold never reaches the row:
// settings and a key set meanwhile keep the row's switch (or write the default, on, when there was none), so the
// entry runs again once hub.json drops disabled; only switching it off is the owner's choice and kept.
func TestVoiceDisabledInHubJSON(t *testing.T) {
	onVoiceStores(t, []string{"talk", "talk-new", "talk-off"}, func(t *testing.T, store voiceTestStore) {
		ctx := context.Background()
		if err := store.PutExtension(ctx, extension.Record{ID: "talk", Source: extension.SourceBuiltin, Version: "test",
			Manifest: json.RawMessage(`{}`), Enabled: true, InstalledAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		held := func(id string) ExtensionConfig {
			entry := voiceEntry(id)
			entry.Disabled = true
			return entry
		}
		h := voiceHub(t, store, held("talk"))
		if p := h.extensions.reg.Get("talk"); p == nil || p.Record.Source != extension.SourceBuiltin || p.Record.Enabled {
			t.Fatalf("%+v", p)
		}
		if v := viewOf(h, "talk"); v.Status != extensionDisabled {
			t.Fatalf("%+v", v)
		}
		mux := h.routes()
		code, out := call(t, mux, "PUT", "/api/v1/extensions/talk/-/enabled", h.cfg.Token, `{"enabled": true}`)
		if code != 409 || out["error"] != i18n.T(i18n.Default, "hub-voice_extension.disabled_in_hub_json") {
			t.Fatalf("%d %v", code, out)
		}
		// Its settings and key stay editable for when hub.json lets it run, and leave the row's switch as it was.
		if code, out = call(t, mux, "PUT", "/api/v1/extensions/talk/-/config", h.cfg.Token, `{"config": {"owner": "me", "url": "wss://live.example.com/v1/live"}}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		if code, out = call(t, mux, "PUT", "/api/v1/extensions/talk/-/secrets/key", h.cfg.Token, `{"value": "sk-test-voice"}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		rowOn := func(id string) bool {
			t.Helper()
			rows, err := store.Extensions(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				if r.ID == id {
					return r.Enabled
				}
			}
			t.Fatalf("no row for %s", id)
			return false
		}
		if !rowOn("talk") {
			t.Fatal("the hold reached the row")
		}

		// No row yet: editing writes the default (on), not the hold.
		h2 := voiceHub(t, store, held("talk-new"))
		if code, out = call(t, h2.routes(), "PUT", "/api/v1/extensions/talk-new/-/config", h2.cfg.Token, `{"config": {"owner": "me"}}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		if !rowOn("talk-new") {
			t.Fatal("the hold reached the new row")
		}
		// Switched off while held: that is the owner's choice, kept.
		h3 := voiceHub(t, store, held("talk-off"))
		if code, out = call(t, h3.routes(), "PUT", "/api/v1/extensions/talk-off/-/enabled", h3.cfg.Token, `{"enabled": false}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		if code, out = call(t, h3.routes(), "PUT", "/api/v1/extensions/talk-off/-/config", h3.cfg.Token, `{"config": {"owner": "me"}}`); code != 200 || rowOn("talk-off") {
			t.Fatalf("%d %v", code, out)
		}

		// hub.json drops disabled: on as the rows say, with what was set meanwhile.
		for id, on := range map[string]bool{"talk": true, "talk-new": true, "talk-off": false} {
			again := voiceHub(t, store, voiceEntry(id))
			p := again.extensions.reg.Get(id)
			if p == nil || p.Record.Enabled != on {
				t.Fatalf("%s: %+v", id, p)
			}
			if id == "talk" && (voiceURL(moduleswitch.Set{}, p.Record.Config) == "" || !again.extensions.reg.SecretsSet(ctx, p)[voiceSecretKey]) {
				t.Fatalf("the settings and key set while held: %+v", p.Record)
			}
		}
	})
}

// Without a hub.json entry the built-in is there as "voice", off; with another program's voice entry, it is not.
func TestVoiceWithoutAHubJSONEntry(t *testing.T) {
	onVoiceStores(t, []string{voiceExtensionID}, func(t *testing.T, store voiceTestStore) {
		h := voiceHub(t, store)
		p := h.extensions.reg.Get(voiceExtensionID)
		if p == nil || p.Record.Source != extension.SourceBuiltin || p.Record.Enabled || p.Full.Name != "实时语音" {
			t.Fatalf("%+v", p)
		}
		if v := viewOf(h, voiceExtensionID); v.Status != extensionDisabled || v.Kind != "voice" {
			t.Fatalf("%+v", v)
		}
		if ch, ok := h.voiceChannel(context.Background()); !ok || ch.State != "disabled" {
			t.Fatalf("%+v", ch)
		}
		raw, _, err := h.modelsAppCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppPlugins, Method: assistantwire.PluginsVoice})
		if err != nil || string(raw) != `{"plugins":[]}` {
			t.Fatalf("a switched-off voice offers no models to pick: %s %v", raw, err)
		}
	})
	h := voiceHub(t, newExtensionStore(), ExtensionConfig{ID: "voice", Kind: "voice", Command: "/usr/bin/node", Args: []string{"main.mjs"}, Disabled: true})
	if p := h.extensions.reg.Get(voiceExtensionID); p == nil || p.Record.Source != extension.SourceHubJSON {
		t.Fatalf("hub.json's own voice program keeps its id: %+v", p)
	}
}

// Switched on without a service it runs but needs setting up (its /info says what is missing); a key needs an address
// first; the key is sealed, never sent back, handed to the program only; moving the address to another host clears
// it in the same request, another path on the same host does not, a refused configuration changes nothing.
func TestVoiceSetupAndTheKeyFollowsTheAddress(t *testing.T) {
	onVoiceStores(t, []string{voiceExtensionID}, func(t *testing.T, store voiceTestStore) {
		h := voiceHub(t, store)
		mux := h.routes()
		tok := h.cfg.Token
		l := i18n.Default
		base := "/api/v1/extensions/voice/-/"

		if code, out := call(t, mux, "PUT", base+"enabled", tok, `{"enabled": true}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		v := waitVoiceStatus(t, h, voiceExtensionID, extensionNeedsSetup)
		if !slices.Contains(v.NeedsSetup, i18n.T(l, "hub-voice_extension.setup_url_and_key")) {
			t.Fatalf("%+v", v)
		}
		waitLong(t, "the program to run", func() bool {
			hp := h.extensions.get(voiceExtensionID)
			return hp != nil && hp.proc != nil && hp.healthy(context.Background())
		})
		if code, info := call(t, mux, "GET", "/api/v1/extensions/voice/info", tok, ""); code != 200 || info["setup"] != "url_and_key" {
			t.Fatalf("info %d %v", code, info)
		}
		if ch, _ := h.voiceChannel(context.Background()); ch.State != "not_connected" {
			t.Fatalf("channel %+v", ch)
		}

		code, out := call(t, mux, "PUT", base+"secrets/key", tok, `{"value": "sk-test-voice"}`)
		if code != 400 || out["code"] != "needs_url" {
			t.Fatalf("a key before an address: %d %v", code, out)
		}
		code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://live.example.com/v1/live", "models": ["gpt-live-1"]}}`)
		if code != 200 || out["secrets_cleared"] != nil || !slices.Contains(toStrings(out["needs_setup"]), i18n.T(l, "hub-voice_extension.setup_key")) {
			t.Fatalf("%d %v", code, out)
		}
		code, out = call(t, mux, "PUT", base+"secrets/key", tok, `{"value": "sk-test-voice"}`)
		raw, _ := json.Marshal(out)
		if code != 200 || !secretSet(out, voiceSecretKey) || strings.Contains(string(raw), "sk-test-voice") {
			t.Fatalf("%d %s", code, raw)
		}
		sealed, _ := store.ExtensionSecrets(context.Background(), voiceExtensionID)
		if sealed[voiceSecretKey] == "" || strings.Contains(sealed[voiceSecretKey], "sk-test-voice") {
			t.Fatalf("kept sealed: %v", sealed)
		}
		if env := envOf(t, h, voiceExtensionID); env["PERSONA_EXTENSION_SECRETS"] != `{"key":"sk-test-voice"}` {
			t.Fatalf("handed to the program: %v", env["PERSONA_EXTENSION_SECRETS"])
		}
		v = waitVoiceStatus(t, h, voiceExtensionID, host.StateRunning)
		if len(v.NeedsSetup) != 0 {
			t.Fatalf("%+v", v)
		}
		waitLong(t, "the restarted program", func() bool {
			code, models := call(t, mux, "GET", "/api/v1/extensions/voice/models", tok, "")
			return code == 200 && models["default"] == "custom/gpt-live-1"
		})

		// The same origin (case, default port, another path): the key stays.
		code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://LIVE.example.com:443/v2/live", "models": ["gpt-live-1"]}}`)
		if code != 200 || !secretSet(out, voiceSecretKey) || out["secrets_cleared"] != nil {
			t.Fatalf("%d %v", code, out)
		}
		// A refused configuration (held to the schema) changes nothing, the key included.
		code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "https://elsewhere.example.com/v1/live"}}`)
		if code != 400 || out["code"] != "invalid_config" || !h.extensions.reg.SecretsSet(context.Background(), h.extensions.reg.Get(voiceExtensionID))[voiceSecretKey] {
			t.Fatalf("%d %v", code, out)
		}
		if code, _ = call(t, mux, "PUT", base+"config", tok, `{}`); code == 200 || !h.extensions.reg.SecretsSet(context.Background(), h.extensions.reg.Get(voiceExtensionID))[voiceSecretKey] {
			t.Fatalf("no configuration at all: %d, and the key must stay", code)
		}
		// Another host: the key goes, and the answer says so.
		code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://elsewhere.example.com/v1/live"}}`)
		if code != 200 || secretSet(out, voiceSecretKey) || !slices.Equal(toStrings(out["secrets_cleared"]), []string{voiceSecretKey}) ||
			!slices.Contains(toStrings(out["warnings"]), i18n.T(l, "hub-voice_extension.key_cleared")) {
			t.Fatalf("%d %v", code, out)
		}
		if sealed, _ := store.ExtensionSecrets(context.Background(), voiceExtensionID); sealed[voiceSecretKey] != "" {
			t.Fatal("still stored")
		}
		waitVoiceStatus(t, h, voiceExtensionID, extensionNeedsSetup)
		// Clearing the address is moving it too.
		call(t, mux, "PUT", base+"secrets/key", tok, `{"value": "sk-test-voice"}`)
		if code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {}}`); code != 200 || secretSet(out, voiceSecretKey) {
			t.Fatalf("%d %v", code, out)
		}
		if es, ok := store.(*extensionStore); ok {
			if audit := strings.Join(es.audit, "\n"); !strings.Contains(audit, "voice secret_delete hub token") {
				t.Fatalf("audit:\n%s", audit)
			}
		}
	})
}

// flakyStore fails reading the extensions' secrets or deleting one, on demand (a connection reset, a statement
// timeout).
type flakyStore struct {
	*extensionStore
	failRead, failDelete atomic.Bool
}

func (s *flakyStore) ExtensionSecrets(ctx context.Context, id string) (map[string]string, error) {
	if s.failRead.Load() {
		return nil, errors.New("statement timeout")
	}
	return s.extensionStore.ExtensionSecrets(ctx, id)
}

func (s *flakyStore) DeleteExtensionSecret(ctx context.Context, id, name string) error {
	if s.failDelete.Load() {
		return errors.New("connection reset")
	}
	return s.extensionStore.DeleteExtensionSecret(ctx, id, name)
}

// The key follows the address fail closed: a failed read of the secrets still deletes it (and says so, unsure);
// a delete that fails saves nothing; a configuration that cannot be saved after the delete says the key went. Any
// caller of SetConfig is held to it, not only the route.
func TestVoiceKeyFollowsTheAddressFailClosed(t *testing.T) {
	store := &flakyStore{extensionStore: newExtensionStore()}
	h := voiceHub(t, store)
	mux, tok, ctx := h.routes(), h.cfg.Token, context.Background()
	base := "/api/v1/extensions/voice/-/"
	reg := h.extensions.reg
	keyStored := func() bool {
		sealed, _ := store.extensionStore.ExtensionSecrets(ctx, voiceExtensionID)
		return sealed[voiceSecretKey] != ""
	}
	setKey := func(url string) {
		t.Helper()
		if code, out := call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "`+url+`"}}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		if code, out := call(t, mux, "PUT", base+"secrets/key", tok, `{"value": "sk-test-voice"}`); code != 200 || !keyStored() {
			t.Fatalf("%d %v", code, out)
		}
	}

	setKey("wss://a.example.com/v1/live")
	store.failRead.Store(true)
	code, out := call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://b.example.com/v1/live"}}`)
	store.failRead.Store(false)
	if code != 200 || keyStored() || !slices.Equal(toStrings(out["secrets_cleared"]), []string{voiceSecretKey}) {
		t.Fatalf("a failed read keeps no key: %d %v (stored %v)", code, out, keyStored())
	}

	setKey("wss://a.example.com/v1/live")
	store.failDelete.Store(true)
	code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://b.example.com/v1/live"}}`)
	store.failDelete.Store(false)
	if code != 500 || out["code"] != "secret_not_cleared" || out["secrets_cleared"] != nil || !keyStored() ||
		voiceURL(moduleswitch.Set{}, reg.Config(reg.Get(voiceExtensionID))) != "wss://a.example.com/v1/live" {
		t.Fatalf("a key that cannot be deleted saves nothing: %d %v", code, out)
	}

	store.failPut = true
	code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://b.example.com/v1/live"}}`)
	store.failPut = false
	if code != 409 || !slices.Equal(toStrings(out["secrets_cleared"]), []string{voiceSecretKey}) || keyStored() ||
		voiceURL(moduleswitch.Set{}, reg.Config(reg.Get(voiceExtensionID))) != "wss://a.example.com/v1/live" {
		t.Fatalf("the key went before the save failed, and the answer says so: %d %v", code, out)
	}

	// Not only the route: any SetConfig, with the read failing too.
	setKey("wss://a.example.com/v1/live")
	store.failRead.Store(true)
	err := reg.SetConfig(ctx, voiceExtensionID, json.RawMessage(`{"url": "wss://b.example.com/v1/live"}`), "assistant")
	store.failRead.Store(false)
	if err != nil || keyStored() {
		t.Fatalf("%v, stored %v", err, keyStored())
	}
	// And a key needs an address whoever sets it.
	if err := reg.SetConfig(ctx, voiceExtensionID, json.RawMessage(`{}`), "assistant"); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetSecret(ctx, voiceExtensionID, voiceSecretKey, "sk-test-voice", "assistant"); !errors.As(err, new(keyNeedsURLError)) {
		t.Fatalf("%v", err)
	}
}

// The built-in voice extension keeps its id: a package of the same id installed later is refused (so no other
// program is handed the key set on it); one installed before the built-in existed keeps the id instead.
func TestVoiceIDIsNotYielded(t *testing.T) {
	onVoiceStores(t, []string{voiceExtensionID}, func(t *testing.T, store voiceTestStore) {
		ctx := context.Background()
		h := voiceHub(t, store)
		mux, tok := h.routes(), h.cfg.Token
		base := "/api/v1/extensions/voice/-/"
		call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://live.example.com/v1/live"}}`)
		if code, out := call(t, mux, "PUT", base+"secrets/key", tok, `{"value": "sk-test-voice"}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		_, err := h.extensions.reg.Preview(ctx, bundleGraphPackage(t, voiceExtensionID, "1.0.0"), "")
		if err == nil || !strings.Contains(err.Error(), "已经有一个叫 voice 的扩展") {
			t.Fatalf("a package taking the built-in's id: %v", err)
		}
		if p := h.extensions.reg.Get(voiceExtensionID); p == nil || p.Record.Source != extension.SourceBuiltin ||
			!h.extensions.reg.SecretsSet(ctx, p)[voiceSecretKey] {
			t.Fatalf("%+v", p)
		}
	})

	store := newExtensionStore()
	manifest := `{"manifest_version":1,"id":"voice","name":"Other voice","version":"1.0.0","contributes":{"providers":[{"id":"voice-provider","name":"v","base_url":"https://provider.example.com"}]}}`
	if err := store.PutExtension(context.Background(), extension.Record{ID: voiceExtensionID, Source: extension.SourceInstalled, Version: "1.0.0",
		Manifest: json.RawMessage(manifest), InstalledAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	h := voiceHub(t, store)
	if p := h.extensions.reg.Get(voiceExtensionID); p == nil || p.Record.Source != extension.SourceInstalled || h.extensions.voiceID != "" {
		t.Fatalf("installed before: %+v", p)
	}
}

// Rolling back to a personad before the built-in: it reads hub.json's entry as it always did and leaves the built-in's
// row alone (Load takes a built-in row only for a built-in); upgrading again finds the settings and the key there.
func TestVoiceUpgradeRollbackUpgrade(t *testing.T) {
	onVoiceStores(t, []string{"talk"}, func(t *testing.T, store voiceTestStore) {
		h := voiceHub(t, store, voiceEntry("talk"))
		mux := h.routes()
		base := "/api/v1/extensions/talk/-/"
		if code, out := call(t, mux, "PUT", base+"config", h.cfg.Token, `{"config": {"url": "wss://live.example.com/v1/live", "owner": "form owner"}}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		if code, out := call(t, mux, "PUT", base+"secrets/key", h.cfg.Token, `{"value": "sk-test-voice"}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}

		// The personad before: hub.json's entry as a hub.json extension (newExtensions and Load as they were).
		old := extension.NewRegistry(t.TempDir(), store, hubSealer{h}, 0)
		entry := voiceEntry("talk")
		entry.Name = "语音"
		if err := old.AddHubJSON(legacyManifest(entry), entry, true); err != nil {
			t.Fatal(err)
		}
		if err := old.Load(context.Background()); err != nil {
			t.Fatal(err)
		}
		if p := old.Get("talk"); p == nil || p.Record.Source != extension.SourceHubJSON || !p.Record.Enabled || len(p.Record.Config) != 0 {
			t.Fatalf("rolled back: %+v", p)
		}

		// And the new one again: the same settings, the same key.
		again := voiceHub(t, store, voiceEntry("talk"))
		p := again.extensions.reg.Get("talk")
		if p == nil || p.Record.Source != extension.SourceBuiltin || !p.Record.Enabled || voiceURL(moduleswitch.Set{}, p.Record.Config) != "wss://live.example.com/v1/live" ||
			!again.extensions.reg.SecretsSet(context.Background(), p)[voiceSecretKey] {
			t.Fatalf("upgraded again: %+v", p)
		}
		// Both upstreams are taken (hub.json's rt, the form's under its own id) and the form's owner kept.
		setup, warnings, err := needsVoiceModule(t).State(builtinext.VoiceSources{Form: again.extensions.reg.Config(p), Key: "sk", HubJSON: again.extensions.voiceHubJSON()})
		if err != nil || setup != "" || len(warnings) != 0 || !strings.Contains(string(again.extensions.reg.Config(p)), "form owner") ||
			!strings.Contains(string(again.extensions.voiceHubJSON()), `"id":"rt"`) {
			t.Fatalf("%q %v %v", setup, warnings, err)
		}
	})
}

func TestSameOrigin(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"wss://a.example.com/v1/live", "wss://a.example.com/v2", true},
		{"wss://a.example.com/v1/live", "wss://A.EXAMPLE.com:443/v1/live", true},
		{"ws://gateway.example.com/v1/live", "ws://gateway.example.com:80/x", true},
		{"wss://a.example.com/v1/live", "ws://a.example.com/v1/live", false},
		{"wss://a.example.com/v1/live", "wss://a.example.com:8443/v1/live", false},
		{"wss://a.example.com/v1/live", "wss://b.example.com/v1/live", false},
		{"wss://a.example.com/v1/live", "wss://a.example.com.evil.example/v1/live", false},
		{"wss://a.example.com/v1/live", "", false},
		{"", "", true},
		{"wss://[::1]:9000/v1", "wss://[::1]:9000/v2", true},
		{"https://tts.example.com", "https://TTS.example.com:443/v1/audio/speech", true},
		{"http://tts.example.com/prefix", "http://tts.example.com:80/v1/audio/speech", true},
		{"https://tts.example.com", "http://tts.example.com", false},
		{"https://tts.example.com", "https://tts.example.com:8443", false},
		{"https://tts.example.com", "https://other.example.com", false},
		{"https://tts.example.com", "", false},
	} {
		if got := sameOrigin(tc.a, tc.b); got != tc.same {
			t.Errorf("%q %q: %v", tc.a, tc.b, got)
		}
	}
}

func toStrings(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// When a quota gateway extension is configured with gateway_url (https or loopback http) and an API key, the built-in
// voice extension automatically receives PERSONA_VOICE_GATEWAY_CONFIG and runs without needing separate voice setup.
func TestVoiceLinksQuotaGatewayConfig(t *testing.T) {
	store := newExtensionStore()
	h := voiceHub(t, store)
	ctx := context.Background()
	reg := h.extensions.reg

	gwManifest := `{"manifest_version":1,"id":"quota-relay","name":"Relay","version":"2.3.0",` +
		`"contributes":{"quota":[{"id":"relay","name":"Relay"}]},` +
		`"configuration":{"type":"object","properties":{"gateway_url":{"type":"string","format":"uri"},"key_kind":{"type":"string","default":"management"}}},` +
		`"permissions":{"network":["config:gateway_url"],"launcher_keys":true},"secrets":[{"name":"gateway_key","title":"Key"}]}`
	if err := store.PutExtension(ctx, extension.Record{
		ID:          "quota-relay",
		Source:      extension.SourceInstalled,
		Version:     "2.3.0",
		Manifest:    json.RawMessage(gwManifest),
		Config:      json.RawMessage(`{"gateway_url":"http://127.0.0.1:8080","key_kind":"api"}`),
		Enabled:     true,
		InstalledAt: time.Now(),
		UpdatedAt:   time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := reg.SetSecret(ctx, "quota-relay", gatewaySecretKey, "gw-api-key-123456", "test"); err != nil {
		t.Fatal(err)
	}

	vp := reg.Get(voiceExtensionID)
	if setup, warns := h.extensions.voiceState(ctx, vp); setup != "" || len(warns) != 0 {
		t.Fatalf("voiceState with quota gateway api key: setup=%q warns=%v", setup, warns)
	}
	env := envOf(t, h, voiceExtensionID)
	var gwProv struct{ ID, Name, URL, Dialect, Key string }
	if err := json.Unmarshal([]byte(env["PERSONA_VOICE_GATEWAY_CONFIG"]), &gwProv); err != nil {
		t.Fatalf("PERSONA_VOICE_GATEWAY_CONFIG: %q (%v)", env["PERSONA_VOICE_GATEWAY_CONFIG"], err)
	}
	if gwProv.ID != "gateway" || gwProv.Name != "Relay" || gwProv.URL != "ws://127.0.0.1:8080/v1/live" || gwProv.Dialect != "codex" || gwProv.Key != "gw-api-key-123456" {
		t.Fatalf("gwProv: %+v", gwProv)
	}
}

// tts_key requires tts_url first, stays sealed and reaches the extension via PERSONA_EXTENSION_SECRETS, follows
// tts_url's origin independently of url/key, and clears when tts_url moves origin or is cleared.
func TestVoiceTTSKeyFollowsTTSURL(t *testing.T) {
	onVoiceStores(t, []string{voiceExtensionID}, func(t *testing.T, store voiceTestStore) {
		h := voiceHub(t, store)
		mux, tok := h.routes(), h.cfg.Token
		l := i18n.Default
		base := "/api/v1/extensions/voice/-/"

		if code, out := call(t, mux, "PUT", base+"enabled", tok, `{"enabled": true}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		// Setting the realtime url alone does not allow setting tts_key.
		if code, out := call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://live.example.com/v1/live"}}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		if code, out := call(t, mux, "PUT", base+"secrets/key", tok, `{"value": "sk-live-voice"}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		code, out := call(t, mux, "PUT", base+"secrets/tts_key", tok, `{"value": "sk-tts-secret"}`)
		if code != 400 || out["code"] != "needs_url" {
			t.Fatalf("tts_key before tts_url: %d %v", code, out)
		}

		// Save tts_url + optional TTS fields, then set tts_key.
		code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://live.example.com/v1/live", "tts_url": "https://tts.example.com", "tts_model": "kokoro", "tts_default_voice": "af_heart", "tts_sample_rate": 24000}}`)
		if code != 200 || out["secrets_cleared"] != nil || !secretSet(out, voiceSecretKey) {
			t.Fatalf("save tts config: %d %v", code, out)
		}
		code, out = call(t, mux, "PUT", base+"secrets/tts_key", tok, `{"value": "sk-tts-secret"}`)
		raw, _ := json.Marshal(out)
		if code != 200 || !secretSet(out, voiceTTSSecretKey) || !secretSet(out, voiceSecretKey) || strings.Contains(string(raw), "sk-tts-secret") {
			t.Fatalf("set tts_key: %d %s", code, raw)
		}
		sealed, _ := store.ExtensionSecrets(context.Background(), voiceExtensionID)
		if sealed[voiceTTSSecretKey] == "" || strings.Contains(sealed[voiceTTSSecretKey], "sk-tts-secret") {
			t.Fatalf("tts_key not sealed: %v", sealed)
		}
		env := envOf(t, h, voiceExtensionID)
		var envSecrets map[string]string
		if err := json.Unmarshal([]byte(env["PERSONA_EXTENSION_SECRETS"]), &envSecrets); err != nil ||
			envSecrets[voiceSecretKey] != "sk-live-voice" || envSecrets[voiceTTSSecretKey] != "sk-tts-secret" {
			t.Fatalf("PERSONA_EXTENSION_SECRETS = %q (%v)", env["PERSONA_EXTENSION_SECRETS"], err)
		}

		// Same origin on tts_url (case, default port, full speech path): tts_key stays.
		code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://live.example.com/v1/live", "tts_url": "https://TTS.example.com:443/v1/audio/speech"}}`)
		if code != 200 || !secretSet(out, voiceTTSSecretKey) || !secretSet(out, voiceSecretKey) || out["secrets_cleared"] != nil {
			t.Fatalf("same tts origin: %d %v", code, out)
		}

		// Invalid config does not clear tts_key.
		code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://live.example.com/v1/live", "tts_url": "wss://bad.example.com"}}`)
		if code != 400 || out["code"] != "invalid_config" || !h.extensions.reg.SecretsSet(context.Background(), h.extensions.reg.Get(voiceExtensionID))[voiceTTSSecretKey] {
			t.Fatalf("invalid config cleared tts_key: %d %v", code, out)
		}

		// Moving realtime url origin clears only key, leaving tts_key untouched.
		code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://live2.example.com/v1/live", "tts_url": "https://tts.example.com/v1/audio/speech"}}`)
		if code != 200 || secretSet(out, voiceSecretKey) || !secretSet(out, voiceTTSSecretKey) || !slices.Equal(toStrings(out["secrets_cleared"]), []string{voiceSecretKey}) {
			t.Fatalf("url origin change affected tts_key: %d %v", code, out)
		}
		if code, out = call(t, mux, "PUT", base+"secrets/key", tok, `{"value": "sk-live-2"}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}

		// Moving tts_url origin clears only tts_key, leaving key untouched.
		code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://live2.example.com/v1/live", "tts_url": "https://tts2.example.com"}}`)
		if code != 200 || secretSet(out, voiceTTSSecretKey) || !secretSet(out, voiceSecretKey) ||
			!slices.Equal(toStrings(out["secrets_cleared"]), []string{voiceTTSSecretKey}) ||
			!slices.Contains(toStrings(out["warnings"]), i18n.T(l, "hub-voice_extension.key_cleared")) {
			t.Fatalf("tts_url origin change: %d %v", code, out)
		}
		if sealed, _ := store.ExtensionSecrets(context.Background(), voiceExtensionID); sealed[voiceTTSSecretKey] != "" {
			t.Fatal("tts_key still stored after origin change")
		}

		// Clearing tts_url also clears tts_key.
		if code, out = call(t, mux, "PUT", base+"secrets/tts_key", tok, `{"value": "sk-tts-2"}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://live2.example.com/v1/live"}}`)
		if code != 200 || secretSet(out, voiceTTSSecretKey) || !secretSet(out, voiceSecretKey) ||
			!slices.Equal(toStrings(out["secrets_cleared"]), []string{voiceTTSSecretKey}) {
			t.Fatalf("clearing tts_url: %d %v", code, out)
		}

		// Moving both origins at once clears both secrets.
		if code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://live2.example.com/v1/live", "tts_url": "https://tts2.example.com"}}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		if code, out = call(t, mux, "PUT", base+"secrets/tts_key", tok, `{"value": "sk-tts-3"}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://live3.example.com/v1/live", "tts_url": "https://tts3.example.com"}}`)
		if code != 200 || secretSet(out, voiceSecretKey) || secretSet(out, voiceTTSSecretKey) ||
			!slices.Equal(toStrings(out["secrets_cleared"]), []string{voiceSecretKey, voiceTTSSecretKey}) {
			t.Fatalf("moving both origins: %d %v", code, out)
		}
	})
}

// tts_key origin binding fails closed and applies to direct registry callers too.
func TestVoiceTTSKeyFollowsTTSURLFailClosed(t *testing.T) {
	store := &flakyStore{extensionStore: newExtensionStore()}
	h := voiceHub(t, store)
	mux, tok, ctx := h.routes(), h.cfg.Token, context.Background()
	base := "/api/v1/extensions/voice/-/"
	reg := h.extensions.reg
	ttsKeyStored := func() bool {
		sealed, _ := store.extensionStore.ExtensionSecrets(ctx, voiceExtensionID)
		return sealed[voiceTTSSecretKey] != ""
	}
	setTTSKey := func(u string) {
		t.Helper()
		if code, out := call(t, mux, "PUT", base+"config", tok, `{"config": {"tts_url": "`+u+`"}}`); code != 200 {
			t.Fatalf("%d %v", code, out)
		}
		if code, out := call(t, mux, "PUT", base+"secrets/tts_key", tok, `{"value": "sk-tts-voice"}`); code != 200 || !ttsKeyStored() {
			t.Fatalf("%d %v", code, out)
		}
	}

	setTTSKey("https://tts-a.example.com")
	store.failRead.Store(true)
	code, out := call(t, mux, "PUT", base+"config", tok, `{"config": {"tts_url": "https://tts-b.example.com"}}`)
	store.failRead.Store(false)
	if code != 200 || ttsKeyStored() || !slices.Equal(toStrings(out["secrets_cleared"]), []string{voiceTTSSecretKey}) {
		t.Fatalf("read failure must still clear tts_key: %d %v (stored %v)", code, out, ttsKeyStored())
	}

	setTTSKey("https://tts-a.example.com")
	store.failDelete.Store(true)
	code, out = call(t, mux, "PUT", base+"config", tok, `{"config": {"tts_url": "https://tts-b.example.com"}}`)
	store.failDelete.Store(false)
	if code != 500 || out["code"] != "secret_not_cleared" || !ttsKeyStored() ||
		voiceTTSURL(moduleswitch.Set{}, reg.Config(reg.Get(voiceExtensionID))) != "https://tts-a.example.com" {
		t.Fatalf("delete failure must save nothing: %d %v", code, out)
	}

	// Direct SetConfig / SetSecret follow the same rule.
	if err := reg.SetConfig(ctx, voiceExtensionID, json.RawMessage(`{}`), "assistant"); err != nil || ttsKeyStored() {
		t.Fatalf("direct SetConfig: err=%v stored=%v", err, ttsKeyStored())
	}
	if err := reg.SetSecret(ctx, voiceExtensionID, voiceTTSSecretKey, "sk-tts-voice", "assistant"); !errors.As(err, new(keyNeedsURLError)) {
		t.Fatalf("direct SetSecret without tts_url: %v", err)
	}
}

// Bad TTS settings in hub.json disable TTS with localized warnings while leaving the extension running.
func TestVoiceHubJSONTTSWarnings(t *testing.T) {
	entry := voiceEntry("talk")
	entry.Config = json.RawMessage(`{"providers":[{"id":"rt","url":"wss://rt.example.com/v1/live","models":["m1"]}],"tts_url":"ftp://bad.example.com","tts_sample_rate":4000}`)
	h := voiceHub(t, newExtensionStore(), entry)
	v := waitVoiceStatus(t, h, "talk", host.StateRunning)
	l := i18n.Default
	for _, key := range []string{"hub-voice_extension.warn_tts_url", "hub-voice_extension.warn_tts_sample_rate"} {
		if !slices.Contains(v.Warnings, i18n.T(l, key)) {
			t.Errorf("view warnings missing %s: %v", key, v.Warnings)
		}
	}
}
