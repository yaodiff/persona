package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/modapi"
	"github.com/nwverse/persona/server/internal/moduleswitch"
)

// volcModule is the host's entry for volc in modapi.ExtensionModules.
func volcModule(t *testing.T) modapi.ExtensionModule {
	t.Helper()
	for _, m := range modapi.ExtensionModules() {
		if m.ID == "volc" {
			return m
		}
	}
	t.Fatal("volc is not in modapi.ExtensionModules")
	return modapi.ExtensionModule{}
}

// Only the official installation of a listed module supplies its flag (#16254): the counterexamples of the design
// (a local upload, a private market's package, a private market holding a retired official key, a package of another
// id, a remote plugin without a package, the built-in) are not providers at all; an official package whose program
// is attached is one. An official one that is disabled, has a needed contribution off or is switched off in the
// modules setting is a provider whose flag is off.
func TestModuleProviderPredicate(t *testing.T) {
	m := volcModule(t)
	official, retired, private := newMarketKey(t).text, newMarketKey(t).text, newMarketKey(t).text
	was := extension.Official
	extension.Official = extension.OfficialMarket{Spec: "github:example/plugins", SigningKey: official, PreviousSigningKeys: []string{retired}}
	t.Cleanup(func() { extension.Official = was })
	specs := map[string]string{
		"off1": "github:example/plugins",
		"off2": "https://github.com/example/plugins", // the official market added by hand, under another spec
		"priv": "github:someone/plugins",
	}
	specOf := func(id string) (string, bool) { s, ok := specs[id]; return s, ok }
	good := extension.Record{ID: "volc", Source: extension.SourceInstalled, Origin: extension.MarketOrigin("off1", "volc"),
		MarketKey: official, Enabled: true}
	with := func(edit func(*extension.Record)) extension.Record {
		r := good
		edit(&r)
		return r
	}
	for _, tc := range []struct {
		name     string
		rec      extension.Record
		approved bool
	}{
		{"official, current key", good, true},
		{"official, retired key", with(func(r *extension.Record) { r.MarketKey = retired }), true},
		{"official market under another spec", with(func(r *extension.Record) { r.Origin = extension.MarketOrigin("off2", "volc") }), true},
		// 1. A local upload takes the id: its origin is no market.
		{"local upload", with(func(r *extension.Record) { r.Origin, r.MarketKey = "", "" }), false},
		{"local directory", with(func(r *extension.Record) { r.Origin, r.MarketKey = "dir:/tmp/volc", "" }), false},
		// 2. A private market's package of the id, with its own key.
		{"private market", with(func(r *extension.Record) { r.Origin, r.MarketKey = extension.MarketOrigin("priv", "volc"), private }), false},
		// 3. A private market that holds a retired official key (refused from now on: NormalizeMarketKeys).
		{"private market, retired official key", with(func(r *extension.Record) { r.Origin, r.MarketKey = extension.MarketOrigin("priv", "volc"), retired }), false},
		{"private market, current official key", with(func(r *extension.Record) { r.Origin = extension.MarketOrigin("priv", "volc") }), false},
		// The official market with a key that is not official.
		{"official market, private key", with(func(r *extension.Record) { r.MarketKey = private }), false},
		{"official market, no key", with(func(r *extension.Record) { r.MarketKey = "" }), false},
		{"market the manager no longer has", with(func(r *extension.Record) { r.Origin = extension.MarketOrigin("gone", "volc") }), false},
		// 7. Another id from the official market (volc2): the list names volc only.
		{"official volc2", with(func(r *extension.Record) { r.ID, r.Origin = "volc2", extension.MarketOrigin("off1", "volc2") }), false},
		{"origin of another id", with(func(r *extension.Record) { r.Origin = extension.MarketOrigin("off1", "volc2") }), false},
		// An official package whose program is attached (Attach) keeps its package and supplies the module.
		{"official package, program attached", with(func(r *extension.Record) {
			r.Remote = &extension.Remote{RPCSocket: "/run/volc.sock", Package: true}
		}), true},
		// A remote plugin installed from remote: has no package, even with the official origin and key.
		{"remote plugin", with(func(r *extension.Record) { r.Remote = &extension.Remote{} }), false},
		{"remote plugin with sockets", with(func(r *extension.Record) { r.Remote = &extension.Remote{RPCSocket: "/run/volc.sock"} }), false},
		{"built-in", with(func(r *extension.Record) {
			r.Source, r.Origin, r.MarketKey = extension.SourceBuiltin, "builtin:volc", ""
		}), false},
	} {
		if got := approvedModuleProvider(m, tc.rec, specOf); got != tc.approved {
			t.Errorf("%s: approved = %v, want %v", tc.name, got, tc.approved)
		}
	}
	if slices.ContainsFunc(modapi.ExtensionModules(), func(x modapi.ExtensionModule) bool { return x.ID == "volc2" }) {
		t.Fatal("the host's list names volc2")
	}

	// 5 and 6: the flag of an approved provider follows the owner's choices, not its program.
	for _, tc := range []struct {
		name string
		rec  extension.Record
		sw   moduleswitch.Set
		on   bool
	}{
		{"enabled", good, moduleswitch.Set{}, true},
		{"enabled with a problem", with(func(r *extension.Record) { r.DisabledContributions = []string{"ui/status"} }), moduleswitch.Set{}, true},
		{"disabled", with(func(r *extension.Record) { r.Enabled = false }), moduleswitch.Set{}, false},
		{"speech off", with(func(r *extension.Record) { r.DisabledContributions = []string{"speech/volc"} }), moduleswitch.Set{}, false},
		{"transcription off", with(func(r *extension.Record) { r.DisabledContributions = []string{"transcription/volc"} }), moduleswitch.Set{}, false},
		{"modules setting off", good, moduleswitch.Off("volc"), false},
		{"another module off", good, moduleswitch.Off("lark"), true},
	} {
		if got := moduleProviderOn(m, tc.rec, tc.sw); got != tc.on {
			t.Errorf("%s: on = %v, want %v", tc.name, got, tc.on)
		}
	}
}

// An extension's api.features never carry the modules marker or a module's flag (#16254): the manager leaves them out
// of what it reports (and logs once), so only the host announces modules. module.<id> cannot even be declared (the
// manifest schema refuses the dot); the marker can, and is dropped.
func TestExtensionAPIFeaturesDropReservedPrefix(t *testing.T) {
	for f, reserved := range map[string]bool{"modules": true, "module.volc": true, "module.lark": true, "modules_v2": false, "example_feed": false} {
		if reservedModuleFeature(f) != reserved {
			t.Errorf("reservedModuleFeature(%q) = %v", f, !reserved)
		}
	}
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	pkg := func(features ...string) string {
		dir := t.TempDir()
		self, _ := os.Executable()
		_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
		_ = os.WriteFile(filepath.Join(dir, "bin", "feed"), []byte("#!/bin/sh\nPERSONA_TEST_PLUGIN=api exec "+self+" \"$@\"\n"), 0o755)
		list, _ := json.Marshal(features)
		manifest := fmt.Sprintf(`{"manifest_version": 1, "id": "example-feed", "name": "Example feed", "version": "1.0.0",
			"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/feed"}, "http": true},
			"activation_events": ["on_startup"],
			"contributes": {"api": [{"id": "feed", "prefix": "claimsmodules", "features": %s}]}}`, runtime.GOOS+"-"+runtime.GOARCH, list)
		_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
		return dir
	}
	if code, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+pkg("module.volc")+`"}`); code == 200 && pv["preview_id"] != nil {
		t.Fatalf("a manifest declaring module.volc was taken: %d %v", code, pv)
	}
	code, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", master, `{"source": "dir:`+pkg("modules", "claims_ok")+`"}`)
	if code != 200 {
		t.Fatalf("preview: %d %v", code, pv)
	}
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", master,
		fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("install: %d %v", code, out)
	}
	waitLong(t, "the plugin up", func() bool {
		hp := h.extensions.get("example-feed")
		return hp != nil && hp.proc != nil && hp.proc.Ready()
	})
	got := h.report().Features
	if !slices.Contains(got, "claims_ok") {
		t.Fatalf("the extension's own feature is missing: %v", got)
	}
	if slices.ContainsFunc(got, reservedModuleFeature) {
		t.Fatalf("the report carries a reserved module feature: %v", got)
	}
	if _, logged := h.reservedLogged.Load("example-feed modules"); !logged {
		t.Fatal("the dropped reserved feature was not logged")
	}
	if len(h.report().Modules) != 0 {
		t.Fatalf("an unlisted extension reported a module: %+v", h.report().Modules)
	}
}

// officialVolcPackage stages a packaged volc in an official market of the test (the built-in's manifest, its program
// the test binary's volc fixture) and returns the market's id. The caller hands the built-in off with handoffVolc.
func officialVolcPackage(t *testing.T, h *Manager, phone string, built *extension.Extension) string {
	t.Helper()
	m := *built.Manifest
	m.Version = "2.0.0"
	rt := *m.Runtime
	rt.Command = extension.Command{Path: "bin/speech"}
	rt.Args = nil
	m.Runtime = &rt
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "speech"), []byte(fmt.Sprintf("#!/bin/sh\nexec %q volc 2.0.0 \"$@\"\n", self)), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, extension.ManifestFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
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

// handoffVolc upgrades the built-in volc to the official market's package (preview, then confirm).
func handoffVolc(t *testing.T, h *Manager, phone, mid, id string) {
	t.Helper()
	mux := h.routes()
	code, preview := call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market":%q,"extension":%q}`, mid, id))
	if code != 200 || preview["upgrade"] != true {
		t.Fatalf("handoff preview %d %+v", code, preview)
	}
	spec := fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, preview["preview_id"], preview["permissions_digest"])
	if code, installed := call(t, mux, "POST", "/api/v1/extensions/"+id+"/-/upgrade", phone, spec); code != 200 {
		t.Fatalf("handoff %d %+v", code, installed)
	}
}

// The report's modules follow the registry through a provider's whole life (#16254): none while the built-in serves
// the id, then the official package from the handoff on, its flag following enable, disable, a needed contribution's
// switch and the modules setting; it stays across a restart (the registry rebuilt from the store), and goes when a
// rollback returns the built-in or an uninstall removes the package.
func TestReportModulesFollowRegistry(t *testing.T) {
	ctx := context.Background()
	v := needsVolcModule(t)
	m := volcModule(t)
	h, phone, _ := marketHub(t)
	reg := h.extensions.reg
	modules := func(step string, want ...extwire.ModuleProvider) {
		t.Helper()
		got := h.report().Modules
		if len(got) != len(want) || len(want) == 1 && got[0] != want[0] {
			t.Fatalf("%s: report modules = %+v, want %+v", step, got, want)
		}
	}
	built := reg.SnapshotExact(v.ExtensionID)
	if built == nil || built.Record.Source != extension.SourceBuiltin {
		t.Fatalf("built-in volc: %+v", built)
	}
	modules("built-in")
	if err := h.store.(*marketTestStore).PutExtension(ctx, built.Record); err != nil {
		t.Fatal(err)
	}
	h.extensions = newExtensionsWith(h.cfg.dir, nil, h.logf, h.modules())
	h.extensions.sandboxes.noDefault = true
	h.attachExtensions(ctx)
	reg = h.extensions.reg
	mid := officialVolcPackage(t, h, phone, built)
	modules("built-in, official package offered")

	handoffVolc(t, h, phone, mid, v.ExtensionID)
	cur := reg.SnapshotExact(v.ExtensionID)
	if cur == nil || cur.Record.Source != extension.SourceInstalled || !cur.Record.BuiltinPredecessor {
		t.Fatalf("after handoff: %+v", cur)
	}
	modules("installed, disabled", extwire.ModuleProvider{ID: "volc", On: cur.Record.Enabled})
	if err := reg.SetEnabled(ctx, v.ExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	modules("installed, enabled", extwire.ModuleProvider{ID: "volc", On: true})
	for _, c := range m.Contributions {
		point, cid, _ := strings.Cut(c, "/")
		if err := reg.SetContribution(ctx, v.ExtensionID, point, cid, false, "test"); err != nil {
			t.Fatal(err)
		}
		modules(c+" off", extwire.ModuleProvider{ID: "volc", On: false})
		if err := reg.SetContribution(ctx, v.ExtensionID, point, cid, true, "test"); err != nil {
			t.Fatal(err)
		}
		modules(c+" on again", extwire.ModuleProvider{ID: "volc", On: true})
	}
	saved := h.opts.Modules
	h.opts.Modules = moduleswitch.Off("volc")
	modules("modules setting off", extwire.ModuleProvider{ID: "volc", On: false})
	h.opts.Modules = saved

	// A restart: the registry and the markets are read from the store again.
	h.extensions = newExtensionsWith(h.cfg.dir, nil, h.logf, h.modules())
	h.extensions.sandboxes.noDefault = true
	h.attachExtensions(ctx)
	reg = h.extensions.reg
	reloadMarkets(h)
	modules("after a restart", extwire.ModuleProvider{ID: "volc", On: true})
	if err := reg.SetEnabled(ctx, v.ExtensionID, false, "test"); err != nil {
		t.Fatal(err)
	}
	modules("disabled after a restart", extwire.ModuleProvider{ID: "volc", On: false})
	if err := reg.SetEnabled(ctx, v.ExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}

	// Rollback returns the built-in: no installed provider any more.
	if code, rolled := call(t, h.routes(), "POST", "/api/v1/extensions/"+v.ExtensionID+"/-/rollback", phone, `{}`); code != 200 {
		t.Fatalf("rollback %d %+v", code, rolled)
	}
	if cur := reg.SnapshotExact(v.ExtensionID); cur == nil || cur.Record.Source != extension.SourceBuiltin {
		t.Fatalf("after rollback: %+v", cur)
	}
	modules("rolled back to the built-in")

	// Handed off again, then uninstalled: the built-in comes back, and so does no module in the report.
	handoffVolc(t, h, phone, mid, v.ExtensionID)
	if got := h.report().Modules; len(got) != 1 || got[0].ID != "volc" {
		t.Fatalf("handed off again: %+v", got)
	}
	if err := reg.Uninstall(ctx, v.ExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if cur := reg.SnapshotExact(v.ExtensionID); cur == nil || cur.Record.Source != extension.SourceBuiltin {
		t.Fatalf("after uninstall: %+v", cur)
	}
	modules("uninstalled")
}
