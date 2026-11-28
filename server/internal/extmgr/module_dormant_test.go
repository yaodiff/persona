package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/builtinext"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/moduleswitch"
)

// Dormant rows and adoption through the manager (#16263, extension/dormant.go). The test store overwrites whole rows,
// so these prove the orchestration only; internal/db's TestOrphanAdoptionThroughRegistry and
// TestExtensionOrphanAdoptionCAS prove the database refuses what the registry refuses.

// volcLike is a manifest of the built-in volc's shape (its id, both contributions, its page, its settings and key),
// written out so that a build without the module has it too. runtime is the built-in's or a package's program.
func volcLike(version string, rt *extension.Runtime) *extension.Manifest {
	transcription, _ := json.Marshal([]extension.TranscriptionItem{{ID: "volc", Name: "Volcengine"}})
	speech, _ := json.Marshal([]extension.SpeechItem{{ID: "volc", Name: "Volcengine"}})
	ui, _ := json.Marshal([]extension.UIItem{{ID: "status", Title: "Status", Placement: "extension_page"}})
	return &extension.Manifest{ManifestVersion: extension.ManifestVersion, ID: "volc", Name: "Volcengine speech", Version: version,
		Engines: extension.Engines{ExtensionProtocol: extension.Protocol}, Runtime: rt, ActivationEvents: []string{extension.OnStartup},
		Permissions: extension.Permissions{Network: []string{volcHost}},
		Contributes: map[string]json.RawMessage{transcriptionPoint: transcription, speechPoint: speech, "ui": ui},
		Configuration: json.RawMessage(`{"type":"object","properties":{"read_answers":{"type":"boolean","default":true},` +
			`"voice":{"type":"string","default":"zh_female_vv_uranus_bigtts"}}}`),
		Secrets: []extension.SecretSpec{{Name: "key", Title: "API Key", Hint: "last4"}}}
}

// volcPackageDir writes the official package of volc at version; its program never runs in these tests (the rows
// they move are switched off).
func volcPackageDir(t *testing.T, version string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "speech"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(volcLike(version, &extension.Runtime{Command: extension.Command{Path: "bin/speech"}, HTTP: true}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, extension.ManifestFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// stageOfficialVolc puts the package of dir in the test's official market and answers the market's id.
func stageOfficialVolc(t *testing.T, h *Manager, phone, dir string) string {
	t.Helper()
	pkg := tarDir(t, dir)
	key := newMarketKey(t)
	useOfficialMarket(t, "github:example/plugins", key.text)
	ix, err := json.Marshal(map[string]any{"market_version": 1, "name": "Official", "extensions": []any{marketEntry(t, dir, "release:v1/speech.tar.gz", pkg)}})
	if err != nil {
		t.Fatal(err)
	}
	gh := &fakeGitHub{release: "v1", files: map[string][]byte{extension.IndexFile: key.sign(ix)}, assets: map[string][]byte{"speech.tar.gz": pkg}}
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

// restartExtensions rebuilds the manager's registry from its store, as a restart (of this build, or another) does.
func restartExtensions(h *Manager) *extension.Registry {
	h.extensions = newExtensionsWith(h.cfg.dir, nil, h.logf, h.modules())
	h.extensions.sandboxes.noDefault = true
	h.attachExtensions(context.Background())
	return h.extensions.reg
}

// withoutVolcModule skips the test on a build with the volc module: there the built-in serves its own row, so no row
// is an orphan (run it with -tags persona_without_volc or persona_core_only).
func withoutVolcModule(t *testing.T) {
	t.Helper()
	if _, ok := builtinext.VolcSpeech(moduleswitch.Set{}); ok {
		t.Skip("the volc module is in this build: its built-in serves the row")
	}
}

// storedVolc is the store's volc row and sealed secrets, copied.
func storedVolc(t *testing.T, h *Manager) (extension.Record, map[string]string) {
	t.Helper()
	s := h.store.(*marketTestStore)
	s.mu.Lock()
	row, ok := s.rows["volc"]
	secrets := map[string]string{}
	for k, v := range s.secrets["volc"] {
		secrets[k] = v
	}
	s.mu.Unlock()
	if !ok {
		t.Fatal("no volc row")
	}
	raw, _ := json.Marshal(row)
	var copied extension.Record
	_ = json.Unmarshal(raw, &copied)
	return copied, secrets
}

// orphanVolcHub is a manager of a build without volc whose store has the built-in's row from an earlier build: the
// owner's settings, the extension switched off, its page switched off, and a key.
func orphanVolcHub(t *testing.T) (*Manager, string) {
	t.Helper()
	withoutVolcModule(t)
	h, phone, _ := marketHub(t)
	ctx := context.Background()
	raw, _ := json.Marshal(volcLike("1.2.3", &extension.Runtime{Command: extension.Command{Path: "personad"}, Args: []string{"volc"}, HTTP: true}))
	at := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	store := h.store.(*marketTestStore)
	if err := store.PutExtension(ctx, extension.Record{ID: "volc", Source: extension.SourceBuiltin, Version: "1.2.3", Manifest: raw,
		Config: json.RawMessage(`{"read_answers":false,"voice":"zh_male_m191_uranus_bigtts"}`), DisabledContributions: []string{"ui/status"},
		InstalledAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutExtensionSecret(ctx, "volc", "key", "sealed-synthetic-key"); err != nil {
		t.Fatal(err)
	}
	if reg := restartExtensions(h); reg.Get("volc") != nil || reg.SnapshotExact("volc") != nil {
		t.Fatal("the orphan is listed")
	}
	return h, phone
}

// On a build without the module, the catalog offers the official volc package as an install, and installing it
// takes the built-in's row over: the same slot, the owner's settings, switch, switched-off page and key, the
// transcription pinned to the built-in still pinned to it, and a restart reads it back as the installed package.
func TestOrphanBuiltinRowAdoptedByOfficialPackage(t *testing.T) {
	h, phone := orphanVolcHub(t)
	orphan, secrets := storedVolc(t, h)
	mid := stageOfficialVolc(t, h, phone, volcPackageDir(t, "2.0.0"))
	mux := h.routes()
	code, out := call(t, mux, "GET", "/api/v1/extensions/-/catalog", phone, "")
	if code != 200 {
		t.Fatalf("catalog %d %+v", code, out)
	}
	entries, _ := out["extensions"].([]any)
	if len(entries) != 1 || entries[0].(map[string]any)["installed"] != nil || entries[0].(map[string]any)["upgradable"] == true {
		t.Fatalf("catalog offers %+v", entries)
	}
	code, preview := call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market":%q,"extension":"volc"}`, mid))
	if code != 200 || preview["upgrade"] == true {
		t.Fatalf("preview %d %+v", code, preview)
	}
	code, installed := call(t, mux, "POST", "/api/v1/extensions/-/install", phone,
		fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, preview["preview_id"], preview["permissions_digest"]))
	if code != 200 {
		t.Fatalf("install %d %+v", code, installed)
	}
	// The install settles in the background (it reads the manager's extensions): let it finish before a restart.
	opID, _ := installed["operation_id"].(string)
	waitOp(t, h, opID, opSucceeded, opFailed)
	check := func(step string, reg *extension.Registry) {
		t.Helper()
		p := reg.SnapshotExact("volc")
		if p == nil {
			t.Fatalf("%s: not listed", step)
		}
		rec := p.Record
		if rec.Source != extension.SourceInstalled || !rec.VerifiedBuiltinPredecessor() || rec.PreviousOrigin != "builtin:volc" ||
			rec.PreviousVersion != "1.2.3" || rec.Version != "2.0.0" || rec.Enabled || !sameValue(rec.Config, orphan.Config) ||
			!slices.Equal(rec.DisabledContributions, orphan.DisabledContributions) || !rec.InstalledAt.Equal(orphan.InstalledAt) {
			t.Fatalf("%s: %+v\nfrom %+v", step, rec, orphan)
		}
		if _, now := storedVolc(t, h); !reflect.DeepEqual(now, secrets) {
			t.Fatalf("%s: the key moved", step)
		}
		if !h.IsTranscriptionContribution("volc/volc") || h.TranscriptionInstall("volc/volc") != "builtin:volc" {
			t.Fatalf("%s: the transcription pin is %q", step, h.TranscriptionInstall("volc/volc"))
		}
	}
	check("installed", h.extensions.reg)
	check("restarted", restartExtensions(h))
}

// A local upload (or a command-line install) of a package with volc's id never takes the orphan's row: it is refused
// and the row and its key stay as they were.
func TestLocalUploadOverOrphanRefused(t *testing.T) {
	h, _ := orphanVolcHub(t)
	before, secrets := storedVolc(t, h)
	mux := h.routes()
	master := h.cfg.Token
	code, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+volcPackageDir(t, "2.0.0")+`"}`)
	if code == 200 && pv["preview_id"] != nil {
		code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", master,
			fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"]))
		if code == 200 || !strings.Contains(fmt.Sprint(out), "identity") && !strings.Contains(fmt.Sprint(out), "身份") {
			t.Fatalf("the upload was installed over the orphan: %d %+v", code, out)
		}
	}
	after, now := storedVolc(t, h)
	if !reflect.DeepEqual(after, before) || !reflect.DeepEqual(now, secrets) {
		t.Fatalf("the refused upload changed the row:\n%+v\n%+v", after, before)
	}
	if h.extensions.reg.Get("volc") != nil {
		t.Fatal("the refused upload is listed")
	}
}

// The modules setting switching volc off holds an installed provider's row (on any build): not listed, nothing runs,
// no module flag, and its row and key unchanged. Switching it back on and restarting reads it as it was.
func TestModulesOffHoldsInstalledProvider(t *testing.T) {
	h, _, _ := marketHub(t)
	ctx := context.Background()
	raw, _ := json.Marshal(volcLike("2.0.0", &extension.Runtime{Command: extension.Command{Path: "bin/speech"}, HTTP: true}))
	at := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	store := h.store.(*marketTestStore)
	if err := store.PutExtension(ctx, extension.Record{ID: "volc", Source: extension.SourceInstalled, Origin: extension.MarketOrigin("official", "volc"),
		Version: "2.0.0", Manifest: raw, Config: json.RawMessage(`{"read_answers":false}`), InstalledAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutExtensionSecret(ctx, "volc", "key", "sealed-synthetic-key"); err != nil {
		t.Fatal(err)
	}
	before, secrets := storedVolc(t, h)
	saved := h.opts.Modules
	h.opts.Modules = moduleswitch.Off("volc")
	reg := restartExtensions(h)
	if reg.Get("volc") != nil || reg.SnapshotExact("volc") != nil || h.extensions.get("volc") != nil || len(h.reportModules()) != 0 {
		t.Fatal("a switched-off module's provider is listed, runs or reports")
	}
	if after, now := storedVolc(t, h); !reflect.DeepEqual(after, before) || !reflect.DeepEqual(now, secrets) {
		t.Fatalf("the held row changed:\n%+v\n%+v", after, before)
	}
	h.opts.Modules = saved
	p := restartExtensions(h).SnapshotExact("volc")
	if p == nil || p.Record.Source != extension.SourceInstalled || !sameValue(p.Record.Config, before.Config) || isVolc(h.modules(), p) {
		t.Fatalf("switched on again: %+v", p)
	}
}

// H7: a package installed fresh under volc's id on a core-only hub (not the built-in's successor) keeps its own slot
// on a build with the module: the built-in is not registered beside it, nothing calls it the built-in's, and its
// transcription pin is its own installation's.
func TestVolcInstalledPackageKeepsItsSlotOnModuleBuild(t *testing.T) {
	needsVolcModule(t)
	h, _, _ := marketHub(t)
	ctx := context.Background()
	raw, _ := json.Marshal(volcLike("2.0.0", &extension.Runtime{Command: extension.Command{Path: "bin/speech"}, HTTP: true}))
	at := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	if err := h.store.(*marketTestStore).PutExtension(ctx, extension.Record{ID: "volc", Source: extension.SourceInstalled,
		Origin: extension.MarketOrigin("official", "volc"), Version: "2.0.0", Manifest: raw, Config: json.RawMessage(`{}`), InstalledAt: at, UpdatedAt: at}); err != nil {
		t.Fatal(err)
	}
	reg := restartExtensions(h)
	p := reg.SnapshotExact("volc")
	if p == nil || p.Record.Source != extension.SourceInstalled || p.Record.BuiltinPredecessor || isVolc(h.modules(), p) {
		t.Fatalf("the installed package: %+v", p)
	}
	if strings.Contains(p.Problem, "身份") || reg.CanHandoffBuiltin("volc") {
		t.Fatalf("the built-in was registered beside the package: problem %q", p.Problem)
	}
	if pin := h.TranscriptionInstall("volc/volc"); pin == "builtin:volc" || !strings.HasPrefix(pin, extension.SourceInstalled+":volc@") {
		t.Fatalf("the transcription pin: %q", pin)
	}
}
