package extmgr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

func TestMarketListsFutureManifestAndBlocksPreview(t *testing.T) {
	h := newTestManager(t)
	h.build.Number = 100
	h.store = newExtensionStore()
	startTestExtensions(t, h)
	mk := newUpgradeMarket(t)
	h.extensions.markets.client = mk.srv.Client()
	dir := uiPackage(t)
	setUIVersion(t, dir, "2.0.0")
	editManifest(t, dir, func(m map[string]any) {
		m["engines"] = map[string]any{"persona": ">=200", "plugin_protocol": 1}
		m["future_metadata"] = true
	})
	mk.publish(dir)
	view, err := h.marketAdd(t.Context(), mk.srv.URL+"/plugins/", "")
	if err != nil {
		t.Fatal(err)
	}
	mux := h.routes()
	token := deviceToken("dev_phone", roleFull)
	for _, lang := range i18n.Langs {
		t.Run(string(lang), func(t *testing.T) {
			args := i18n.Args{"required": ">=200", "current": 100}
			want := i18n.T(lang, "extension-version.hub_too_old", args)
			catalog := h.catalogIn(lang, "example ui", view.ID)
			if len(catalog) != 1 || catalog[0].ID != "example-ui" || catalog[0].Version != "2.0.0" || catalog[0].Problem != want {
				t.Fatalf("catalog = %+v, want future extension with %q", catalog, want)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/extensions/-/catalog/preview",
				strings.NewReader(fmt.Sprintf(`{"market":%q,"extension":"example-ui"}`, view.ID)))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Accept-Language", string(lang))
			req.RemoteAddr = "127.0.0.1:12345"
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			var body struct{ Error string }
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || rr.Code != http.StatusBadRequest || body.Error != want {
				t.Fatalf("preview = %d %s, want 400 with %q", rr.Code, rr.Body, want)
			}
		})
	}
	mk.mu.Lock()
	defer mk.mu.Unlock()
	if mk.downloads != 0 {
		t.Fatalf("downloaded %d incompatible packages", mk.downloads)
	}
}

func TestMarketAutoUpgradeUsesCompatibleReleaseAndReopensSignedCatalog(t *testing.T) {
	h := newTestManager(t)
	h.build.Number = 100
	h.store = newExtensionStore()
	startTestExtensions(t, h)
	mk := newUpgradeMarket(t)
	h.extensions.markets.client = mk.srv.Client()
	key := newMarketKey(t)
	makeRelease := func(version, persona string, change func(map[string]any)) map[string]any {
		dir := uiPackage(t)
		setUIVersion(t, dir, version)
		editManifest(t, dir, func(m map[string]any) {
			if persona != "" {
				m["engines"] = map[string]any{"persona": persona, "plugin_protocol": 1}
			}
			if change != nil {
				change(m)
			}
		})
		pkg := tarDir(t, dir)
		filename := version + ".tar.gz"
		mk.mu.Lock()
		mk.pkgs[filename] = pkg
		mk.mu.Unlock()
		return marketEntry(t, dir, "pkg/"+filename, pkg)
	}
	publish := func(base map[string]any, releases ...map[string]any) []byte {
		entry := map[string]any{"manifest": base["manifest"], "packages": base["packages"]}
		if len(releases) > 0 {
			entry["releases"] = releases
		}
		raw := key.sign(marketIndex(t, "Example marketplace", entry))
		mk.mu.Lock()
		mk.index = raw
		mk.mu.Unlock()
		return raw
	}
	base := makeRelease("1.0.0", "", nil)
	publish(base)
	view, err := h.marketAdd(t.Context(), mk.srv.URL+"/plugins/", "", key.text)
	if err != nil || view.Signature.State != extension.SigSigned {
		t.Fatalf("register signed market: %+v, %v", view, err)
	}
	mux, token := h.routes(), deviceToken("dev_phone", roleFull)
	previewRequest := fmt.Sprintf(`{"market":%q,"extension":"example-ui"}`, view.ID)
	code, pv := call(t, mux, "POST", "/api/v1/extensions/-/catalog/preview", token, previewRequest)
	if code != http.StatusOK {
		t.Fatalf("preview legacy release: %d %v", code, pv)
	}
	confirm := fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, pv["preview_id"], pv["permissions_digest"])
	if code, out := call(t, mux, "POST", "/api/v1/extensions/-/install", token, confirm); code != http.StatusOK {
		t.Fatalf("install legacy release: %d %v", code, out)
	}
	waitLong(t, "the first release to run", func() bool {
		return h.extensions.view(t.Context(), h.extensions.reg.Get("example-ui")).Status == "running"
	})

	compatible := makeRelease("1.5.0", ">=100", nil)
	modern := makeRelease("2.0.0", ">=200", func(m map[string]any) {
		m["configuration"] = map[string]any{"type": "object", "properties": map[string]any{
			"account": map[string]any{"type": "string", "section": "connection"},
		}}
	})
	future := makeRelease("3.0.0", ">=300", func(m map[string]any) { m["future_metadata"] = true })
	raw := publish(base, future, compatible, modern)
	if _, err := h.marketRead(t.Context(), view.ID); err != nil {
		t.Fatal(err)
	}
	wantProblem := i18n.T(h.userLang(), "extension-version.hub_too_old", i18n.Args{"required": ">=300", "current": 100})
	code, out := call(t, mux, "GET", "/api/v1/extensions/-/catalog?market="+view.ID, token, "")
	items, _ := out["extensions"].([]any)
	if code != http.StatusOK || len(items) != 1 {
		t.Fatalf("catalog: %d %v", code, out)
	}
	item := items[0].(map[string]any)
	if item["version"] != "1.5.0" || item["latest_incompatible_version"] != "3.0.0" ||
		item["latest_incompatible_reason"] != wantProblem || item["problem"] != nil {
		t.Fatalf("active release or upgrade hint missing: %+v", item)
	}
	choice, err := h.findBundleChoice("example-ui", nil, view.ID)
	if err != nil || choice.manifest.Version != "1.5.0" {
		t.Fatalf("bundle resolved a different release: %+v, %v", choice, err)
	}
	if up := upgradeOf(t, mux, token); up["to"] != "1.5.0" || up["state"] != upgradeAvailable {
		t.Fatalf("wrong upgrade candidate: %v", up)
	}
	h.extensionUpgrades().upgrade(t.Context())
	if p := h.extensions.reg.Get("example-ui"); p.Record.Version != "1.5.0" || p.Record.MarketKey != key.text {
		t.Fatalf("compatible release or signed provenance was not installed: %+v", p.Record)
	}
	if up := upgradeOf(t, mux, token); up["to"] != "3.0.0" || up["state"] != upgradeIncompatible || up["detail"] != wantProblem {
		t.Fatalf("newer incompatible release was not surfaced: %v", up)
	}

	// Reopen the same saved envelope on a higher build without another index read.
	restarted := provenanceTestManager(t, nil, mk.srv.Client(), "")
	restarted.extensions.markets.loaded = false
	restarted.store = h.store
	restarted.build.Number = 200
	reads := mk.readCount()
	if err := restarted.marketsLoad(t.Context()); err != nil {
		t.Fatal(err)
	}
	rec, active, err := restarted.marketEntry(view.ID, "example-ui")
	if err != nil || active.Parsed.Version != "2.0.0" || !bytes.Equal(rec.Index, raw) || mk.readCount() != reads {
		t.Fatalf("cached envelope was not re-resolved: %+v, %v", active, err)
	}
	reg := extension.NewRegistry(t.TempDir(), newExtensionStore(), nil, 200)
	market, err := restarted.market(rec)
	if err != nil {
		t.Fatal(err)
	}
	highPreview, err := reg.PreviewMarket(t.Context(), market, rec.ID, active, "")
	if err != nil || highPreview.Manifest.Version != "2.0.0" {
		t.Fatalf("preview did not use the selected signed archive: %+v, %v", highPreview, err)
	}
	reg.Discard(highPreview.ID)

	mk.mu.Lock()
	downloads := mk.downloads
	mk.mu.Unlock()
	h.extensionUpgrades().upgrade(t.Context()) // Only the hint is newer than the installed release.
	publish(future)
	if _, err := h.marketRead(t.Context(), view.ID); err != nil {
		t.Fatal(err)
	}
	if up := upgradeOf(t, mux, token); up["to"] != "3.0.0" || up["state"] != upgradeIncompatible || up["detail"] != wantProblem {
		t.Fatalf("future-only catalog lost its upgrade explanation: %v", up)
	}
	if _, err := h.findBundleChoice("example-ui", nil, view.ID); err == nil || !strings.Contains(err.Error(), wantProblem) {
		t.Fatalf("bundle did not refuse the incompatible release: %v", err)
	}
	h.extensionUpgrades().upgrade(t.Context())
	if v := installedVersion(h); v != "1.5.0" {
		t.Fatalf("installed incompatible release %s", v)
	}
	mk.mu.Lock()
	defer mk.mu.Unlock()
	if mk.downloads != downloads {
		t.Fatalf("incompatible upgrade requested a package: %d to %d downloads", downloads, mk.downloads)
	}
}
