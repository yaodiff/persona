package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/builtinext"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/moduleswitch"
)

// The built-in voice extension handing off to its official package (#16264): the form's built-in may move to the
// package in the same row, comes back on a rollback, and stays where a quota gateway upstream, a call archive or
// hub.json's takeover is something the package cannot take; the key keeps following the address and the default
// meeting transcription ("") follows the verified successor on any build.

// voicePackageScopes are the named hub scopes the official voice package runs on (#17851): no hub_api.
var voicePackageScopes = []string{extwire.ScopeKBRead, extwire.ScopeAgentsRead, extwire.ScopeAgentsControl,
	extwire.ScopeMachinesRead, extwire.ScopeAssistantAsk}

// voiceEventsTopic is the topic the package's events contribution subscribes to: agent.changed once the hub has it
// (#17851), else a topic of today's, which leaves the handoff's check the same (it never reads the topics).
func voiceEventsTopic() string {
	if extension.EventTopics["agent.changed"] {
		return "agent.changed"
	}
	return "agent.finished"
}

// voiceLike is a manifest of the built-in voice extension's shape (its id, both contributions, its settings and key),
// written out so that a build without the module has it too. pkg makes it the official package's (named scopes, the
// address as its network, a required key), events adds the package's events subscription (which needs the protocol).
func voiceLike(version string, rt *extension.Runtime, pkg, events bool) *extension.Manifest {
	voice, _ := json.Marshal([]extension.VoiceItem{{ID: voiceExtensionID, Name: "实时语音"}})
	transcription, _ := json.Marshal([]extension.TranscriptionItem{{ID: "gpt-live", Name: "GPT-Live"}})
	m := &extension.Manifest{ManifestVersion: extension.ManifestVersion, ID: voiceExtensionID, Name: "实时语音", Version: version,
		Engines: extension.Engines{ExtensionProtocol: extension.Protocol}, Runtime: rt, ActivationEvents: []string{extension.OnStartup},
		Permissions:   extension.Permissions{HubAPI: true},
		Contributes:   map[string]json.RawMessage{"voice": voice, transcriptionPoint: transcription},
		Configuration: voiceConfigSchema(),
		Secrets:       []extension.SecretSpec{{Name: voiceSecretKey, Title: "API Key"}, {Name: voiceTTSSecretKey, Title: "TTS API Key"}}}
	if pkg {
		m.Permissions = extension.Permissions{Hub: slices.Clone(voicePackageScopes), Network: []string{"config:url:http", "config:tts_url:http"}}
		m.Secrets[0].Required = true
		m.Configuration = voicePackageConfig()
	}
	if events {
		subs, _ := json.Marshal([]extension.EventsItem{{ID: "runs", Subscribe: []string{voiceEventsTopic()}}})
		m.Contributes["events"] = subs
	}
	return m
}

// voicePackageConfig is the built-in's settings form as the package declares it: the same keys, patterns and
// defaults, with both addresses (url, tts_url) "format": "uri" fields, which a config:<field> network entry needs
// (Parse refuses it otherwise). Every address the built-in's patterns allow is one, so its saved settings still pass.
func voicePackageConfig() json.RawMessage {
	var schema map[string]any
	_ = json.Unmarshal(voiceConfigSchema(), &schema)
	for _, field := range []string{"url", "tts_url"} {
		schema["properties"].(map[string]any)[field].(map[string]any)["format"] = "uri"
	}
	b, _ := json.Marshal(schema)
	return b
}

// voicePackageDir writes the official voice package at version. runs makes its program the test binary's built-in
// voice program (a build with the module only); otherwise the program never runs (the rows it moves are off).
func voicePackageDir(t *testing.T, version string, runs, events bool) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nexit 1\n"
	if runs {
		self, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		script = fmt.Sprintf("#!/bin/sh\nexec %q voice \"$@\"\n", self)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "voice"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := &extension.Runtime{Command: extension.Command{Path: "bin/voice"}, RPC: "none", HTTP: true}
	if events {
		rt.RPC = "" // the protocol: events are delivered through it
	}
	b, err := json.Marshal(voiceLike(version, rt, true, events))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, extension.ManifestFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// stageOfficialVoice puts the package of dir in the test's official market and answers the market's id.
func stageOfficialVoice(t *testing.T, h *Manager, phone, dir string) string {
	t.Helper()
	pkg := tarDir(t, dir)
	key := newMarketKey(t)
	useOfficialMarket(t, "github:example/plugins", key.text)
	ix, err := json.Marshal(map[string]any{"market_version": 1, "name": "Official", "extensions": []any{marketEntry(t, dir, "release:v1/voice.tar.gz", pkg)}})
	if err != nil {
		t.Fatal(err)
	}
	gh := &fakeGitHub{release: "v1", files: map[string][]byte{extension.IndexFile: key.sign(ix)}, assets: map[string][]byte{"voice.tar.gz": pkg}}
	srv := httptest.NewTLSServer(gh)
	t.Cleanup(srv.Close)
	gh.storage = srv.URL
	h.extensions.markets.client, h.extensions.markets.githubAPI = srv.Client(), srv.URL
	markets := marketList(t, h, phone)
	if len(markets) != 1 {
		t.Fatalf("markets: %+v", markets)
	}
	waitMarketsRead(t, h)
	return markets[0]["id"].(string)
}

// handoffVoice upgrades the built-in voice extension to the official package of market mid (preview, then confirm),
// and waits for the upgrade's operation to settle: it reads the manager's extensions in the background, so a test
// that restarts them next must not race it.
func handoffVoice(t *testing.T, h *Manager, phone, mid string) {
	t.Helper()
	mux := h.routes()
	code, preview := call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market":%q,"extension":"voice"}`, mid))
	if code != 200 || preview["upgrade"] != true {
		t.Fatalf("handoff preview %d %+v", code, preview)
	}
	spec := fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, preview["preview_id"], preview["permissions_digest"])
	code, out := call(t, mux, "POST", "/api/v1/extensions/voice/-/upgrade", phone, spec)
	if code != 200 {
		t.Fatalf("handoff %d %+v", code, out)
	}
	if opID, _ := out["operation_id"].(string); opID != "" {
		waitOp(t, h, opID, opSucceeded, opFailed)
	}
}

// voiceMarketHub is marketHub with hub.json's extensions cfgs.
func voiceMarketHub(t *testing.T, cfgs ...ExtensionConfig) (h *Manager, phone string) {
	h = newTestManager(t)
	h.store = &marketTestStore{extensionStore: newExtensionStore()}
	startTestExtensions(t, h, cfgs...)
	return h, deviceToken("dev_phone", roleFull)
}

// setUpBuiltinVoice saves the form built-in's address and key through the routes (on: switched on too).
func setUpBuiltinVoice(t *testing.T, h *Manager, on bool) {
	t.Helper()
	mux, tok, base := h.routes(), h.cfg.Token, "/api/v1/extensions/voice/-/"
	if code, out := call(t, mux, "PUT", base+"config", tok, `{"config": {"url": "wss://live.example.com/v1/live", "owner": "form owner"}}`); code != 200 {
		t.Fatalf("config %d %v", code, out)
	}
	if code, out := call(t, mux, "PUT", base+"secrets/key", tok, `{"value": "sk-test-voice"}`); code != 200 {
		t.Fatalf("key %d %v", code, out)
	}
	if on {
		if code, out := call(t, mux, "PUT", base+"enabled", tok, `{"enabled": true}`); code != 200 {
			t.Fatalf("enabled %d %v", code, out)
		}
	}
}

// voiceCatalogItem is the catalog's voice entry.
func voiceCatalogItem(t *testing.T, h *Manager, phone string) map[string]any {
	t.Helper()
	code, out := call(t, h.routes(), "GET", "/api/v1/extensions/-/catalog", phone, "")
	if code != 200 {
		t.Fatalf("catalog %d %+v", code, out)
	}
	entries, _ := out["extensions"].([]any)
	if len(entries) != 1 {
		t.Fatalf("catalog entries %+v", entries)
	}
	return entries[0].(map[string]any)
}

// voiceUpgradeOffered is whether the official market offers its voice package as the built-in's upgrade, asked of
// the decision and of the catalog alike.
func voiceUpgradeOffered(t *testing.T, h *Manager, phone, mid string) bool {
	t.Helper()
	rec, entry, err := h.marketEntry(mid, voiceExtensionID)
	if err != nil {
		t.Fatal(err)
	}
	direct := h.builtinMarketUpgrade(rec, entry)
	listed := voiceCatalogItem(t, h, phone)["upgradable"] == true
	if direct != listed {
		t.Fatalf("the decision says %v, the catalog %v", direct, listed)
	}
	return direct
}

// officialVoiceRow is an installed row of the official voice package from market mid (predecessor: the built-in's
// successor), switched off.
func officialVoiceRow(mid string, predecessor bool, config string) extension.Record {
	raw, _ := json.Marshal(voiceLike("1.0.0", &extension.Runtime{Command: extension.Command{Path: "bin/voice"}, RPC: "none", HTTP: true}, true, false))
	at := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	r := extension.Record{ID: voiceExtensionID, Source: extension.SourceInstalled, Origin: extension.MarketOrigin(mid, voiceExtensionID),
		MarketKey: extension.Official.SigningKey, Version: "1.0.0", Manifest: raw, Config: json.RawMessage(config), InstalledAt: at, UpdatedAt: at}
	if predecessor {
		r.BuiltinPredecessor, r.PreviousOrigin, r.PreviousVersion = true, "builtin:voice", "test"
	}
	return r
}

// The form's built-in hands off to the official package (preview, then confirm): the same row (its installation, its
// settings), the key still set, the default meeting transcription still resolving to it and pinned as before, one
// process (the built-in's stopped, the package's answering /info and the transcription's info), and the module flag
// now the package's.
func TestVoiceBuiltinMarketHandoff(t *testing.T) {
	needsVoiceModule(t)
	ctx := context.Background()
	h, phone := voiceMarketHub(t)
	reg := h.extensions.reg
	if !reg.CanHandoffBuiltin(voiceExtensionID) {
		t.Fatal("the form's voice extension was not registered for handoff")
	}
	setUpBuiltinVoice(t, h, true)
	waitLong(t, "the built-in's program", func() bool {
		hp := h.extensions.get(voiceExtensionID)
		return hp != nil && hp.proc != nil && hp.healthy(ctx)
	})
	builtin := h.extensions.get(voiceExtensionID)
	before := reg.SnapshotExact(voiceExtensionID).Record
	pinned := h.TranscriptionInstall("")
	if pinned != "builtin:voice" || !h.IsTranscriptionContribution("") {
		t.Fatalf("the default transcription before: %q", pinned)
	}

	mid := stageOfficialVoice(t, h, phone, voicePackageDir(t, "1.0.0", true, false))
	if item := voiceCatalogItem(t, h, phone); item["upgradable"] != true || item["installed_source"] != extension.SourceBuiltin {
		t.Fatalf("catalog item %+v", item)
	}
	handoffVoice(t, h, phone, mid)

	cur := reg.SnapshotExact(voiceExtensionID)
	if cur == nil || cur.Record.Source != extension.SourceInstalled || !cur.Record.VerifiedBuiltinPredecessor() || !cur.Record.Enabled ||
		h.extensions.isVoice(cur) || !h.extensions.isVoiceProvider(cur) || !cur.Record.InstalledAt.Equal(before.InstalledAt) ||
		!sameValue(reg.SavedConfig(cur), before.Config) {
		t.Fatalf("after the handoff %+v\nbefore %+v", cur.Record, before)
	}
	if !reg.SecretsSet(ctx, cur)[voiceSecretKey] {
		t.Fatal("the key did not stay")
	}
	if !h.IsTranscriptionContribution("") || h.TranscriptionInstall("") != pinned {
		t.Fatalf("the default transcription after: %q", h.TranscriptionInstall(""))
	}
	if builtin.proc.Generation() != 0 {
		t.Fatal("the built-in's program still runs beside the package's")
	}
	waitLong(t, "the package's program", func() bool {
		hp := h.extensions.get(voiceExtensionID)
		return hp != nil && hp != builtin && hp.proc != nil && hp.healthy(ctx) && hp.proc.Ready()
	})
	if code, info := call(t, h.routes(), "GET", "/api/v1/extensions/voice/info", h.cfg.Token, ""); code != 200 {
		t.Fatalf("the package's /info %d %v", code, info)
	}
	if info, err := h.TranscriptionInfo(ctx, ""); err != nil || info.Protocol != 1 {
		t.Fatalf("the default transcription's info: %+v %v", info, err)
	}
	if got := h.reportModules(); !slices.Contains(got, extwire.ModuleProvider{ID: "voice", On: true}) {
		t.Fatalf("report modules %+v", got)
	}
}

// After the handoff a restart registers the built-in before the package's row is loaded (the successor's row does not
// keep it out, #16264), so a rollback still finds the built-in: the same settings, the key, the handoff offered again.
func TestVoiceSuccessorKeepsYieldedBuiltinAfterRestart(t *testing.T) {
	needsVoiceModule(t)
	ctx := context.Background()
	h, phone := voiceMarketHub(t)
	setUpBuiltinVoice(t, h, false)
	before := h.extensions.reg.SnapshotExact(voiceExtensionID).Record
	mid := stageOfficialVoice(t, h, phone, voicePackageDir(t, "1.0.0", false, false))
	handoffVoice(t, h, phone, mid)

	reg := restartExtensions(h)
	reloadMarkets(h)
	reloaded := reg.SnapshotExact(voiceExtensionID)
	if reloaded == nil || reloaded.Record.Source != extension.SourceInstalled || !reloaded.Record.VerifiedBuiltinPredecessor() ||
		h.extensions.voiceID != voiceExtensionID || h.TranscriptionInstall("") != "builtin:voice" {
		t.Fatalf("restarted: %+v (voice id %q)", reloaded, h.extensions.voiceID)
	}
	if code, out := call(t, h.routes(), "POST", "/api/v1/extensions/voice/-/rollback", phone, `{}`); code != 200 {
		t.Fatalf("rollback %d %+v", code, out)
	}
	cur := reg.SnapshotExact(voiceExtensionID)
	if cur == nil || !h.extensions.isVoice(cur) || !sameValue(reg.SavedConfig(cur), before.Config) || !reg.SecretsSet(ctx, cur)[voiceSecretKey] ||
		!reg.CanHandoffBuiltin(voiceExtensionID) || h.TranscriptionInstall("") != "builtin:voice" {
		t.Fatalf("rolled back: %+v", cur)
	}
}

// The official package is not offered where the built-in has what it cannot take: a quota gateway's upstream (given
// to the built-in only), a computer to archive calls on (the package's 1.0.0 writes none), or hub.json's takeover
// (its id and upstreams are hub.json's; disabled there it is held off as well). The form's built-in alone is offered.
func TestVoiceHandoffNotOfferedWithGatewayOrHubJSON(t *testing.T) {
	needsVoiceModule(t)
	ctx := context.Background()
	t.Run("form", func(t *testing.T) {
		h, phone := voiceMarketHub(t)
		setUpBuiltinVoice(t, h, false)
		mid := stageOfficialVoice(t, h, phone, voicePackageDir(t, "1.0.0", false, false))
		if !voiceUpgradeOffered(t, h, phone, mid) {
			t.Fatal("the form's built-in is not offered the upgrade")
		}
		// A package without one of the built-in's points (the calls, or meeting transcription) cannot stand in for it.
		rec, entry, err := h.marketEntry(mid, voiceExtensionID)
		if err != nil {
			t.Fatal(err)
		}
		for _, point := range []string{"voice", transcriptionPoint} {
			partial, m := *entry, *entry.Parsed
			m.Contributes = maps.Clone(m.Contributes)
			delete(m.Contributes, point)
			partial.Parsed = &m
			if h.builtinMarketUpgrade(rec, &partial) {
				t.Fatalf("a package without %s is offered", point)
			}
		}
	})
	t.Run("gateway", func(t *testing.T) {
		h, phone := voiceMarketHub(t)
		setUpBuiltinVoice(t, h, false)
		mid := stageOfficialVoice(t, h, phone, voicePackageDir(t, "1.0.0", false, false))
		addQuotaGateway(t, h, true)
		if h.extensions.voiceGatewayState(ctx) == nil {
			t.Fatal("no gateway upstream")
		}
		if voiceUpgradeOffered(t, h, phone, mid) {
			t.Fatal("offered with a gateway upstream")
		}
		code, preview := call(t, h.routes(), "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market":%q,"extension":"voice"}`, mid))
		if code == 200 && preview["upgrade"] == true {
			t.Fatalf("previewed as an upgrade: %+v", preview)
		}
	})
	t.Run("archive", func(t *testing.T) {
		h, phone := voiceMarketHub(t)
		setUpBuiltinVoice(t, h, false)
		// Saved as the settings page saves it once the computer is known (the form checks the machine's id).
		row, _ := storedVoice(t, h)
		row.Config = json.RawMessage(`{"url":"wss://live.example.com/v1/live","archive_machine":"m1","archive_dir":"~/Calls"}`)
		if err := h.store.(extension.Store).PutExtension(ctx, row); err != nil {
			t.Fatal(err)
		}
		reg := restartExtensions(h)
		mid := stageOfficialVoice(t, h, phone, voicePackageDir(t, "1.0.0", false, false))
		if !reg.CanHandoffBuiltin(voiceExtensionID) || voiceUpgradeOffered(t, h, phone, mid) {
			t.Fatal("offered with a call archive set")
		}
		code, preview := call(t, h.routes(), "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market":%q,"extension":"voice"}`, mid))
		if code == 200 && preview["upgrade"] == true {
			t.Fatalf("previewed as an upgrade: %+v", preview)
		}
		// Clearing the archive computer makes the hub's built-in movable again.
		if err := reg.SetConfig(ctx, voiceExtensionID, json.RawMessage(`{"url":"wss://live.example.com/v1/live"}`), "test"); err != nil {
			t.Fatal(err)
		}
		if !voiceUpgradeOffered(t, h, phone, mid) {
			t.Fatal("not offered once the archive is cleared")
		}
	})
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("hub.json disabled=%v", disabled), func(t *testing.T) {
			entry := voiceEntry(voiceExtensionID)
			entry.Disabled = disabled
			h, phone := voiceMarketHub(t, entry)
			p := h.extensions.reg.SnapshotExact(voiceExtensionID)
			if p == nil || !h.extensions.isVoice(p) || h.extensions.reg.CanHandoffBuiltin(voiceExtensionID) {
				t.Fatalf("hub.json's takeover: %+v", p)
			}
			mid := stageOfficialVoice(t, h, phone, voicePackageDir(t, "1.0.0", false, false))
			if rec, entry, err := h.marketEntry(mid, voiceExtensionID); err != nil || h.builtinMarketUpgrade(rec, entry) ||
				voiceCatalogItem(t, h, phone)["upgradable"] == true {
				t.Fatalf("hub.json's takeover is offered the upgrade (%v)", err)
			}
		})
	}
}

// addQuotaGateway adds an enabled quota gateway extension (TestVoiceLinksQuotaGatewayConfig's), with its key when
// keyed, read into the running registry.
func addQuotaGateway(t *testing.T, h *Manager, keyed bool) {
	t.Helper()
	ctx := context.Background()
	gw := `{"manifest_version":1,"id":"quota-relay","name":"Relay","version":"2.3.0",` +
		`"contributes":{"quota":[{"id":"relay","name":"Relay"}]},` +
		`"configuration":{"type":"object","properties":{"gateway_url":{"type":"string","format":"uri"},"key_kind":{"type":"string","default":"management"}}},` +
		`"permissions":{"network":["config:gateway_url"],"launcher_keys":true},"secrets":[{"name":"gateway_key","title":"Key"}]}`
	if err := h.store.(extension.Store).PutExtension(ctx, extension.Record{ID: "quota-relay", Source: extension.SourceInstalled, Version: "2.3.0",
		Manifest: json.RawMessage(gw), Config: json.RawMessage(`{"gateway_url":"http://127.0.0.1:8080","key_kind":"api"}`), Enabled: true,
		InstalledAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if keyed {
		if err := h.extensions.reg.SetSecret(ctx, "quota-relay", gatewaySecretKey, "gw-api-key-123456", "test"); err != nil {
			t.Fatal(err)
		}
	}
}

// A package of voice's id that adds a point of its own (the official package's events subscription) is still the
// built-in's upgrade: the handoff needs every point the built-in serves, not the same set. The preview shows the
// added subscription to agree to, and the upgrade, a restart and a rollback go as without it.
func TestVoiceHandoffAcceptsAnAddedEventsPoint(t *testing.T) {
	needsVoiceModule(t)
	ctx := context.Background()
	h, phone := voiceMarketHub(t)
	setUpBuiltinVoice(t, h, false)
	before := h.extensions.reg.SnapshotExact(voiceExtensionID).Record
	mid := stageOfficialVoice(t, h, phone, voicePackageDir(t, "1.0.0", false, true))
	if !voiceUpgradeOffered(t, h, phone, mid) {
		t.Fatal("an added events point keeps the upgrade from being offered")
	}
	mux := h.routes()
	code, preview := call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market":%q,"extension":"voice"}`, mid))
	if code != 200 || preview["upgrade"] != true {
		t.Fatalf("preview %d %+v", code, preview)
	}
	if raw, _ := json.Marshal(preview["added_permissions"]); !strings.Contains(string(raw), `"events"`) {
		t.Fatalf("the preview does not show the added subscription: %s", raw)
	}
	spec := fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, preview["preview_id"], preview["permissions_digest"])
	code, out := call(t, mux, "POST", "/api/v1/extensions/voice/-/upgrade", phone, spec)
	if code != 200 {
		t.Fatalf("upgrade %d %+v", code, out)
	}
	if opID, _ := out["operation_id"].(string); opID != "" {
		waitOp(t, h, opID, opSucceeded, opFailed)
	}
	reg := h.extensions.reg
	cur := reg.SnapshotExact(voiceExtensionID)
	if cur == nil || !cur.Record.VerifiedBuiltinPredecessor() || len(cur.Full.Items("events")) != 1 || !reg.SecretsSet(ctx, cur)[voiceSecretKey] ||
		!sameValue(reg.SavedConfig(cur), before.Config) || h.TranscriptionInstall("") != "builtin:voice" {
		t.Fatalf("after the upgrade: %+v", cur)
	}
	reg = restartExtensions(h)
	reloadMarkets(h)
	if code, out := call(t, h.routes(), "POST", "/api/v1/extensions/voice/-/rollback", phone, `{}`); code != 200 {
		t.Fatalf("rollback %d %+v", code, out)
	}
	if back := reg.SnapshotExact(voiceExtensionID); back == nil || !h.extensions.isVoice(back) || !reg.SecretsSet(ctx, back)[voiceSecretKey] {
		t.Fatalf("rolled back: %+v", back)
	}
}

// The official voice package's key follows its address as the built-in's does, on any build: another host deletes it
// (the answer says so), no address refuses one. A local upload or a private market's package of the id is not held
// to it (the host keeps that promise for the official one only). Saving settings from many goroutines while the
// catalog is read neither deadlocks nor races: the registry's callbacks take the markets' lock, never the reverse.
func TestVoiceProviderKeyFollowsTheAddress(t *testing.T) {
	ctx := context.Background()
	h, phone, _ := marketHub(t)
	mid := stageOfficialVoice(t, h, phone, voicePackageDir(t, "1.0.0", false, false))
	other := newMarketKey(t).text
	for _, tc := range []struct {
		name    string
		edit    func(*extension.Record)
		follows bool
	}{
		{"official, installed fresh", func(*extension.Record) {}, true},
		{"official, the built-in's successor", func(r *extension.Record) {
			r.BuiltinPredecessor, r.PreviousOrigin, r.PreviousVersion = true, "builtin:voice", "test"
		}, true},
		{"local upload", func(r *extension.Record) { r.Origin, r.MarketKey = "", "" }, false},
		{"another key", func(r *extension.Record) { r.MarketKey = other }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := officialVoiceRow(mid, false, `{"url":"wss://a.example.com/v1/live"}`)
			tc.edit(&row)
			if err := h.store.(extension.Store).PutExtension(ctx, row); err != nil {
				t.Fatal(err)
			}
			_ = h.store.(extension.Store).DeleteExtensionSecret(ctx, voiceExtensionID, voiceSecretKey)
			reg := restartExtensions(h)
			reloadMarkets(h)
			p := reg.SnapshotExact(voiceExtensionID)
			if p == nil || p.Record.Source != extension.SourceInstalled || h.extensions.isVoiceProvider(p) != tc.follows {
				t.Fatalf("listed %+v, provider %v", p, h.extensions.isVoiceProvider(p))
			}
			keySet := func() bool { return reg.SecretsSet(ctx, reg.Get(voiceExtensionID))[voiceSecretKey] }
			if err := reg.SetSecret(ctx, voiceExtensionID, voiceSecretKey, "sk-test-voice", "test"); err != nil {
				t.Fatal(err)
			}
			code, out := call(t, h.routes(), "PUT", "/api/v1/extensions/voice/-/config", h.cfg.Token, `{"config": {"url": "wss://b.example.com/v1/live"}}`)
			if code != 200 {
				t.Fatalf("config %d %v", code, out)
			}
			cleared := slices.Equal(toStrings(out["secrets_cleared"]), []string{voiceSecretKey}) &&
				slices.Contains(toStrings(out["warnings"]), i18n.T(i18n.Default, "hub-voice_extension.key_cleared"))
			if cleared != tc.follows || keySet() == tc.follows {
				t.Fatalf("another host: cleared %v, key set %v (%v)", cleared, keySet(), out)
			}
			// The TTS key (#17881) follows its own address the same way.
			if err := reg.SetConfig(ctx, voiceExtensionID, json.RawMessage(`{"url":"wss://b.example.com/v1/live","tts_url":"https://tts-a.example.com/v1"}`), "test"); err != nil {
				t.Fatal(err)
			}
			if err := reg.SetSecret(ctx, voiceExtensionID, voiceTTSSecretKey, "sk-test-tts", "test"); err != nil {
				t.Fatal(err)
			}
			if err := reg.SetConfig(ctx, voiceExtensionID, json.RawMessage(`{"url":"wss://b.example.com/v1/live","tts_url":"https://tts-b.example.com/v1"}`), "test"); err != nil {
				t.Fatal(err)
			}
			if ttsSet := reg.SecretsSet(ctx, reg.Get(voiceExtensionID))[voiceTTSSecretKey]; ttsSet == tc.follows {
				t.Fatalf("another TTS host: the TTS key set %v", ttsSet)
			}
			if err := reg.SetConfig(ctx, voiceExtensionID, json.RawMessage(`{}`), "test"); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{voiceSecretKey, voiceTTSSecretKey} {
				err := reg.SetSecret(ctx, voiceExtensionID, name, "sk-test-voice", "test")
				if refused := errors.As(err, new(keyNeedsURLError)); refused != tc.follows || !refused && err != nil {
					t.Fatalf("%s without an address: %v", name, err)
				}
			}
			if !tc.follows {
				return
			}
			// The lock order: settings saves (BoundSecrets, SecretNeeds read the markets) beside catalog reads.
			done := make(chan struct{})
			go func() {
				defer close(done)
				var wg sync.WaitGroup
				for i := range 8 {
					wg.Add(2)
					go func() {
						defer wg.Done()
						host := []string{"a", "b"}[i%2]
						_ = reg.SetConfig(ctx, voiceExtensionID, json.RawMessage(`{"url":"wss://`+host+`.example.com/v1/live"}`), "test")
						_ = reg.SetSecret(ctx, voiceExtensionID, voiceSecretKey, "sk-test-voice", "test")
					}()
					go func() {
						defer wg.Done()
						_ = h.catalog("", "")
						_ = h.marketViews()
						_ = h.reportModules()
					}()
				}
				wg.Wait()
			}()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatal("settings saves and market reads deadlocked")
			}
		})
	}
}

// A quota gateway changing restarts the built-in voice extension (which is given its upstream), never the package
// that took its id: restarting it would drop its calls.
func TestGatewayChangeDoesNotRestartInstalledVoice(t *testing.T) {
	needsVoiceModule(t)
	ctx := context.Background()
	h, phone := voiceMarketHub(t)
	setUpBuiltinVoice(t, h, true)
	addQuotaGateway(t, h, false) // no key yet: no upstream, so the upgrade is offered
	mid := stageOfficialVoice(t, h, phone, voicePackageDir(t, "1.0.0", true, false))
	handoffVoice(t, h, phone, mid)
	waitLong(t, "the package's program", func() bool {
		hp := h.extensions.get(voiceExtensionID)
		return hp != nil && hp.proc != nil && hp.healthy(ctx) && !h.extensions.isVoice(h.extensions.reg.Get(voiceExtensionID))
	})
	hp := h.extensions.get(voiceExtensionID)
	// The registry calls its OnChange (ps.changed) before SetSecret returns, and the restart it would decide on
	// (ps.apply) drops the running entry and stops its program in that same call: a restart, were there one, has
	// happened by the time SetSecret returns, so the check below needs no wait.
	if err := h.extensions.reg.SetSecret(ctx, "quota-relay", gatewaySecretKey, "gw-api-key-123456", "test"); err != nil {
		t.Fatal(err)
	}
	if h.extensions.voiceGatewaySpec(ctx) == nil {
		t.Fatal("the gateway gives no upstream")
	}
	// The same entry, its program still answering (a stopped program's socket does not): not restarted.
	if now := h.extensions.get(voiceExtensionID); now != hp || !hp.healthy(ctx) {
		t.Fatalf("the package was restarted (entry replaced %v)", now != hp)
	}
}

// H7: the official voice package installed fresh (a core-only hub, not the built-in's successor) keeps its own slot
// on a build with the module: no built-in beside it, no handoff, and the default meeting transcription is not it.
func TestVoiceInstalledPackageKeepsItsSlotOnModuleBuild(t *testing.T) {
	needsVoiceModule(t)
	h, phone, _ := marketHub(t)
	mid := stageOfficialVoice(t, h, phone, voicePackageDir(t, "1.0.0", false, false))
	if err := h.store.(extension.Store).PutExtension(context.Background(), officialVoiceRow(mid, false, `{}`)); err != nil {
		t.Fatal(err)
	}
	reg := restartExtensions(h)
	reloadMarkets(h)
	p := reg.SnapshotExact(voiceExtensionID)
	if p == nil || p.Record.Source != extension.SourceInstalled || p.Record.BuiltinPredecessor || h.extensions.isVoice(p) ||
		h.extensions.voiceID != "" || reg.CanHandoffBuiltin(voiceExtensionID) || strings.Contains(p.Problem, "身份") {
		t.Fatalf("the installed package: %+v (voice id %q)", p, h.extensions.voiceID)
	}
	if h.IsTranscriptionContribution("") || !h.extensions.isVoiceProvider(p) {
		t.Fatal("the fresh package took the default transcription, or lost the key's guarantee")
	}
}

// withoutVoiceModule skips the test on a build with the voice module: there the built-in serves its own row (run it
// with -tags persona_without_voice or persona_core_only).
func withoutVoiceModule(t *testing.T) {
	t.Helper()
	if _, ok := builtinext.VoiceExtension(moduleswitch.Set{}); ok {
		t.Skip("the voice module is in this build: its built-in serves the row")
	}
}

// storedVoice is the store's voice row and sealed secrets, copied.
func storedVoice(t *testing.T, h *Manager) (extension.Record, map[string]string) {
	t.Helper()
	s := h.store.(*marketTestStore)
	s.mu.Lock()
	row, ok := s.rows[voiceExtensionID]
	secrets := map[string]string{}
	for k, v := range s.secrets[voiceExtensionID] {
		secrets[k] = v
	}
	s.mu.Unlock()
	if !ok {
		t.Fatal("no voice row")
	}
	raw, _ := json.Marshal(row)
	var copied extension.Record
	_ = json.Unmarshal(raw, &copied)
	return copied, secrets
}

// On a build without the module, the built-in voice row an earlier build left is taken over by the official package
// installed from the catalog (offered as an install): the same slot, settings, switch and key, the default meeting
// transcription resolving to it and pinned as the built-in's, the module flag the package's; a restart reads it back.
// The package's events subscription is one more point than the built-in had, and changes nothing of that.
func TestVoiceOrphanBuiltinRowAdoptedByOfficialPackage(t *testing.T) {
	withoutVoiceModule(t)
	h, phone, _ := marketHub(t)
	ctx := context.Background()
	raw, _ := json.Marshal(voiceLike("1.2.3", &extension.Runtime{Command: extension.Command{Path: "personad"}, Args: []string{"voice"}, RPC: "none", HTTP: true}, false, false))
	at := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	store := h.store.(*marketTestStore)
	if err := store.PutExtension(ctx, extension.Record{ID: voiceExtensionID, Source: extension.SourceBuiltin, Version: "1.2.3", Manifest: raw,
		Config: json.RawMessage(`{"url":"wss://live.example.com/v1/live","owner":"form owner"}`), DisabledContributions: []string{"transcription/gpt-live"},
		InstalledAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutExtensionSecret(ctx, voiceExtensionID, voiceSecretKey, "sealed-synthetic-key"); err != nil {
		t.Fatal(err)
	}
	if reg := restartExtensions(h); reg.Get(voiceExtensionID) != nil || h.IsTranscriptionContribution("") {
		t.Fatal("the orphan is listed, or transcribes")
	}
	orphan, secrets := storedVoice(t, h)
	mid := stageOfficialVoice(t, h, phone, voicePackageDir(t, "1.0.0", false, true))
	if item := voiceCatalogItem(t, h, phone); item["installed"] != nil || item["upgradable"] == true {
		t.Fatalf("catalog offers %+v", item)
	}
	mux := h.routes()
	code, preview := call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market":%q,"extension":"voice"}`, mid))
	if code != 200 || preview["upgrade"] == true {
		t.Fatalf("preview %d %+v", code, preview)
	}
	code, installed := call(t, mux, "POST", "/api/v1/extensions/-/install", phone,
		fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, preview["preview_id"], preview["permissions_digest"]))
	if code != 200 {
		t.Fatalf("install %d %+v", code, installed)
	}
	opID, _ := installed["operation_id"].(string)
	waitOp(t, h, opID, opSucceeded, opFailed)
	check := func(step string, reg *extension.Registry) {
		t.Helper()
		p := reg.SnapshotExact(voiceExtensionID)
		if p == nil {
			t.Fatalf("%s: not listed", step)
		}
		rec := p.Record
		if rec.Source != extension.SourceInstalled || !rec.VerifiedBuiltinPredecessor() || rec.PreviousOrigin != "builtin:voice" ||
			rec.PreviousVersion != "1.2.3" || rec.Version != "1.0.0" || rec.Enabled || !sameValue(rec.Config, orphan.Config) ||
			!slices.Equal(rec.DisabledContributions, orphan.DisabledContributions) || !rec.InstalledAt.Equal(orphan.InstalledAt) ||
			len(p.Full.Items("events")) != 1 || !slices.Equal(rec.Granted.Hub, voicePackageScopes) && len(rec.Granted.Hub) != len(voicePackageScopes) {
			t.Fatalf("%s: %+v\nfrom %+v", step, rec, orphan)
		}
		if _, now := storedVoice(t, h); !reflect.DeepEqual(now, secrets) {
			t.Fatalf("%s: the key moved", step)
		}
		if !h.IsTranscriptionContribution("") || h.TranscriptionInstall("") != "builtin:voice" {
			t.Fatalf("%s: the default transcription is %q", step, h.TranscriptionInstall(""))
		}
		if id, cid, ok := h.extensions.transcriptionRef(""); !ok || id != voiceExtensionID || cid != "gpt-live" {
			t.Fatalf("%s: \"\" names %s/%s %v", step, id, cid, ok)
		}
		if got := h.reportModules(); !slices.Contains(got, extwire.ModuleProvider{ID: "voice", On: false}) {
			t.Fatalf("%s: report modules %+v", step, got)
		}
	}
	check("installed", h.extensions.reg)
	reg := restartExtensions(h)
	reloadMarkets(h)
	check("restarted", reg)
}

// The modules setting switching voice off holds the official package's row (on any build): not listed, nothing runs,
// no module flag, no default transcription, its row and key unchanged; switched on again it is read as it was.
func TestVoiceModulesOffHoldsInstalledProvider(t *testing.T) {
	h, phone, _ := marketHub(t)
	ctx := context.Background()
	mid := stageOfficialVoice(t, h, phone, voicePackageDir(t, "1.0.0", false, false))
	store := h.store.(*marketTestStore)
	if err := store.PutExtension(ctx, officialVoiceRow(mid, true, `{"url":"wss://live.example.com/v1/live"}`)); err != nil {
		t.Fatal(err)
	}
	if err := store.PutExtensionSecret(ctx, voiceExtensionID, voiceSecretKey, "sealed-synthetic-key"); err != nil {
		t.Fatal(err)
	}
	before, secrets := storedVoice(t, h)
	saved := h.opts.Modules
	h.opts.Modules = moduleswitch.Off("voice")
	reg := restartExtensions(h)
	reloadMarkets(h)
	if reg.Get(voiceExtensionID) != nil || reg.SnapshotExact(voiceExtensionID) != nil || h.extensions.get(voiceExtensionID) != nil ||
		len(h.reportModules()) != 0 || h.IsTranscriptionContribution("") {
		t.Fatal("a switched-off module's provider is listed, runs, reports or transcribes")
	}
	if after, now := storedVoice(t, h); !reflect.DeepEqual(after, before) || !reflect.DeepEqual(now, secrets) {
		t.Fatalf("the held row changed:\n%+v\n%+v", after, before)
	}
	h.opts.Modules = saved
	reg = restartExtensions(h)
	reloadMarkets(h)
	p := reg.SnapshotExact(voiceExtensionID)
	if p == nil || p.Record.Source != extension.SourceInstalled || !sameValue(p.Record.Config, before.Config) || h.extensions.isVoice(p) ||
		!h.IsTranscriptionContribution("") {
		t.Fatalf("switched on again: %+v", p)
	}
}

// The default meeting transcription ("") follows the built-in's verified successor on any build, pinned as the
// built-in's; the official package installed fresh does not get it (installing an extension never sends it meeting
// audio), and neither does a package of the id from anywhere else.
func TestDefaultTranscriptionFollowsVoiceSuccessor(t *testing.T) {
	h, phone, _ := marketHub(t)
	mid := stageOfficialVoice(t, h, phone, voicePackageDir(t, "1.0.0", false, false))
	for _, tc := range []struct {
		name string
		row  extension.Record
		want bool
	}{
		{"the built-in's successor", officialVoiceRow(mid, true, `{}`), true},
		{"installed fresh", officialVoiceRow(mid, false, `{}`), false},
		{"a successor's row with another key", func() extension.Record {
			r := officialVoiceRow(mid, true, `{}`)
			r.MarketKey = newMarketKey(t).text
			return r
		}(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := h.store.(extension.Store).PutExtension(context.Background(), tc.row); err != nil {
				t.Fatal(err)
			}
			restartExtensions(h)
			reloadMarkets(h)
			if got := h.IsTranscriptionContribution(""); got != tc.want {
				t.Fatalf("\"\" resolves: %v", got)
			}
			if tc.want && h.TranscriptionInstall("") != "builtin:voice" {
				t.Fatalf("the pin %q", h.TranscriptionInstall(""))
			}
			if _, _, _, err := h.beginTranscriber(context.Background(), ""); !errors.Is(err, ErrNoTranscriber) {
				t.Fatalf("a switched-off provider transcribes: %v", err)
			}
		})
	}
}

// The report's voice module follows the official package from the handoff on (#16264): its switch and its call
// contribution's; switching meeting transcription off keeps the calls, so the flag stays. A rollback ends it.
func TestReportModulesFollowRegistryForVoice(t *testing.T) {
	needsVoiceModule(t)
	ctx := context.Background()
	h, phone := voiceMarketHub(t)
	setUpBuiltinVoice(t, h, false)
	reg := h.extensions.reg
	voice := func(step string, want ...extwire.ModuleProvider) {
		t.Helper()
		var got []extwire.ModuleProvider
		for _, m := range h.report().Modules {
			if m.ID == "voice" {
				got = append(got, m)
			}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("%s: report's voice = %+v, want %+v", step, got, want)
		}
	}
	voice("built-in")
	mid := stageOfficialVoice(t, h, phone, voicePackageDir(t, "1.0.0", false, false))
	handoffVoice(t, h, phone, mid)
	voice("handed off, off", extwire.ModuleProvider{ID: "voice", On: false})
	// Enabled without its program running (the package's never starts here): the flag says the owner's choice.
	if err := reg.SetEnabled(ctx, voiceExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	voice("on", extwire.ModuleProvider{ID: "voice", On: true})
	if err := reg.SetContribution(ctx, voiceExtensionID, transcriptionPoint, "gpt-live", false, "test"); err != nil {
		t.Fatal(err)
	}
	voice("meeting transcription off", extwire.ModuleProvider{ID: "voice", On: true})
	if err := reg.SetContribution(ctx, voiceExtensionID, "voice", "voice", false, "test"); err != nil {
		t.Fatal(err)
	}
	voice("calls off", extwire.ModuleProvider{ID: "voice", On: false})
	if code, out := call(t, h.routes(), "POST", "/api/v1/extensions/voice/-/rollback", phone, `{}`); code != 200 {
		t.Fatalf("rollback %d %+v", code, out)
	}
	voice("rolled back")
}
