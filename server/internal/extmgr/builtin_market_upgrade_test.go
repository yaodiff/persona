package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
)

func TestVolcBuiltinMarketHandoff(t *testing.T) {
	ctx := context.Background()
	v := needsVolcModule(t)
	h, phone, _ := marketHub(t)
	reg := h.extensions.reg
	if !reg.CanHandoffBuiltin(v.ExtensionID) {
		t.Fatal("Volc speech was not registered for handoff")
	}

	// Stage the package while the owner's existing speech extension is switched off.
	built := reg.SnapshotExact(v.ExtensionID)
	ref := volcRef(h.modules())
	pinned := h.TranscriptionInstall(ref)
	if pinned == "" {
		t.Fatal("missing initial speech selection")
	}
	row := built.Record
	row.Config = json.RawMessage(`{"read_answers":true}`)
	if err := h.store.(*marketTestStore).PutExtension(ctx, row); err != nil {
		t.Fatal(err)
	}
	h.extensions = newExtensionsWith(h.cfg.dir, nil, h.logf, h.modules())
	h.extensions.sandboxes.noDefault = true
	h.attachExtensions(ctx)
	reg = h.extensions.reg
	m := *built.Manifest
	m.Version = "2.0.0"
	rt := *m.Runtime
	rt.Command = extension.Command{Path: "bin/speech"}
	rt.Args = nil
	m.Runtime = &rt
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0755); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// The packaged executable uses the test binary's speech fixture, so the
	// ordinary extension sockets and both HTTP contribution routes are real.
	if err := os.WriteFile(filepath.Join(dir, "bin", "speech"), []byte(fmt.Sprintf("#!/bin/sh\nexec %q volc 2.0.0 \"$@\"\n", self)), 0755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, extension.ManifestFile), b, 0644); err != nil {
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
	defer srv.Close()
	gh.storage = srv.URL
	h.extensions.markets.client, h.extensions.markets.githubAPI = srv.Client(), srv.URL
	markets := marketList(t, h, phone)
	if len(markets) != 1 {
		t.Fatalf("markets: %+v", markets)
	}
	mid := markets[0]["id"].(string)
	waitMarketsRead(t, h)
	mux := h.routes()
	code, out := call(t, mux, "GET", "/api/v1/extensions/-/catalog", phone, "")
	if code != 200 {
		t.Fatalf("catalog response %d %+v", code, out)
	}
	entries, _ := out["extensions"].([]any)
	if len(entries) != 1 {
		t.Fatalf("catalog entries %+v", entries)
	}
	item := entries[0].(map[string]any)
	if item["upgradable"] != true || item["installed_source"] != extension.SourceBuiltin {
		t.Fatalf("catalog item %+v", item)
	}

	rec, entry, err := h.marketEntry(mid, v.ExtensionID)
	if err != nil {
		t.Fatal(err)
	}
	// A private catalog and an unrelated or incomplete provider cannot take this slot.
	private := rec
	private.Spec = "github:example/other"
	incomplete := *entry
	incompleteManifest := *entry.Parsed
	incompleteManifest.Contributes = nil
	incomplete.Parsed = &incompleteManifest
	if h.builtinMarketUpgrade(private, entry) || h.builtinMarketUpgrade(rec, &incomplete) {
		t.Fatal("unrelated catalog or incomplete provider offered a built-in upgrade")
	}

	code, preview := call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", phone, fmt.Sprintf(`{"market":%q,"extension":%q}`, mid, v.ExtensionID))
	if code != 200 || preview["upgrade"] != true {
		t.Fatalf("handoff preview %d %+v", code, preview)
	}
	spec := fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, preview["preview_id"], preview["permissions_digest"])
	code, installed := call(t, mux, "POST", "/api/v1/extensions/"+v.ExtensionID+"/-/upgrade", phone, spec)
	if code != 200 {
		t.Fatalf("handoff %d %+v", code, installed)
	}
	current := reg.SnapshotExact(v.ExtensionID)
	if current == nil || current.Record.Source != extension.SourceInstalled || !current.Record.BuiltinPredecessor || current.Record.Enabled || isVolc(h.modules(), current) {
		t.Fatalf("installed record %+v", current)
	}
	if !h.IsTranscriptionContribution(ref) || h.TranscriptionInstall(ref) != pinned {
		t.Fatal("stable transcription reference no longer resolves")
	}
	if got := string(reg.SavedConfig(current)); got != `{"read_answers":true}` {
		t.Fatalf("settings after handoff %s", got)
	}
	// A restarted host registers the built-in factory before the installed row is
	// loaded, so rollback can return to the actual built-in executable.
	h.extensions = newExtensionsWith(h.cfg.dir, nil, h.logf, h.modules())
	h.extensions.sandboxes.noDefault = true
	h.attachExtensions(ctx)
	reg = h.extensions.reg
	reloaded := reg.SnapshotExact(v.ExtensionID)
	if reloaded == nil || reloaded.Record.Source != extension.SourceInstalled || h.TranscriptionInstall(ref) != pinned {
		t.Fatalf("reload selected %+v", reloaded)
	}
	if err := reg.SetEnabled(ctx, v.ExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	waitLong(t, "installed speech process is active", func() bool {
		hp := h.extensions.get(v.ExtensionID)
		return hp != nil && hp.proc != nil && hp.healthy(ctx)
	})
	packaged := h.extensions.get(v.ExtensionID)
	if info, err := h.TranscriptionInfo(ctx, ref); err != nil || info.Protocol != 1 {
		t.Fatalf("installed transcription status: %+v %v", info, err)
	}
	code, speech := call(t, mux, "GET", "/api/v1/extensions/"+v.ExtensionID+"/speech/"+v.SpeechContribution+"/info", phone, "")
	if code != 200 || speech["protocol"] != 1.0 {
		t.Fatalf("installed speech route %d %+v", code, speech)
	}
	code, rolled := call(t, mux, "POST", "/api/v1/extensions/"+v.ExtensionID+"/-/rollback", phone, `{}`)
	if code != 200 {
		t.Fatalf("rollback %d %+v", code, rolled)
	}
	if packaged.proc.Generation() != 0 {
		t.Fatal("packaged process survived rollback")
	}
	waitLong(t, "restored built-in speech process is active", func() bool {
		hp := h.extensions.get(v.ExtensionID)
		return hp != nil && hp != packaged && hp.proc != nil && hp.healthy(ctx)
	})
	current = reg.SnapshotExact(v.ExtensionID)
	if current == nil || !isVolc(h.modules(), current) || h.TranscriptionInstall(ref) != pinned || !current.Record.Enabled || string(reg.SavedConfig(current)) != `{"read_answers":true}` {
		t.Fatalf("restored record %+v", current)
	}
}
