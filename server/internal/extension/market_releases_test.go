package extension

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

func testMarketRelease(t *testing.T, version, persona string, change func(map[string]any)) MarketRelease {
	t.Helper()
	return MarketRelease{
		Manifest: with(t, func(m map[string]any) {
			m["version"] = version
			if persona == "" {
				delete(m["engines"].(map[string]any), "persona")
			} else {
				m["engines"].(map[string]any)["persona"] = persona
			}
			if change != nil {
				change(m)
			}
		}),
		Packages: []MarketPackage{{URL: version + ".tar.gz", SHA256: strings.Repeat("a", 64)}},
	}
}

func releaseCatalog(t *testing.T, base MarketRelease, releases ...MarketRelease) []byte {
	t.Helper()
	ix := MarketIndex{MarketVersion: 1, Name: "Café extensions", Extensions: []MarketEntry{{
		Manifest: base.Manifest, Packages: base.Packages, L10n: base.L10n, Releases: releases,
	}}}
	payload, err := json.Marshal(ix)
	if err != nil {
		t.Fatal(err)
	}
	return append(append([]byte(" \n"), payload...), '\n')
}

func futureConfigField(m map[string]any) {
	m["configuration"] = map[string]any{"type": "object", "properties": map[string]any{
		"account": map[string]any{"type": "string", "future_layout": true},
	}}
}

func TestMarketReleasesResolveByHubBuild(t *testing.T) {
	base := testMarketRelease(t, "1.2.0", ">=100", nil)
	base.L10n = json.RawMessage(`{"en":{"name":"Base translation"}}`)
	modern := testMarketRelease(t, "2.10.0", ">=200", func(m map[string]any) {
		m["configuration"] = map[string]any{"type": "object", "properties": map[string]any{
			"account": map[string]any{"type": "string", "section": "connection"},
		}}
	})
	modern.L10n = json.RawMessage(`{"en":{"name":"Release translation"}}`)
	future := testMarketRelease(t, "3.0.0", ">=300", futureConfigField)
	intermediate := testMarketRelease(t, "2.9.0", ">=150", nil)
	ceiling := testMarketRelease(t, "4.0.0", "<100", nil)
	raw := releaseCatalog(t, base, intermediate, modern, future, ceiling)
	for _, tt := range []struct {
		name                        string
		build                       int64
		version, translation, newer string
		warnings                    int
	}{
		{"old hub", 100, "1.2.0", "Base translation", "3.0.0", 0},
		{"inherit base translation", 150, "2.9.0", "Base translation", "3.0.0", 0},
		{"modern hub", 200, "2.10.0", "Release translation", "3.0.0", 0},
		{"future field still unknown at declared build", 300, "2.10.0", "Release translation", "", 1},
		{"development hub still validates manifests", 0, "4.0.0", "Base translation", "", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ix, warnings, err := ParseIndexForBuild(raw, tt.build)
			if err != nil || len(ix.Extensions) != 1 || len(warnings) != tt.warnings {
				t.Fatalf("index = %+v, warnings = %v, error = %v", ix, warnings, err)
			}
			e := &ix.Extensions[0]
			m, err := Parse(e.Manifest)
			if err != nil || m.Version != tt.version || e.Parsed.Version != tt.version ||
				e.PackageFor(Platform()).URL != tt.version+".tar.gz" || e.EngineProblem(tt.build) != nil {
				t.Fatalf("wrong active release: %+v, %v", e, err)
			}
			if got := e.Translations().Name(i18n.En, e.Parsed.Name); got != tt.translation {
				t.Fatalf("translation = %q, want %q", got, tt.translation)
			}
			version, reason := e.LatestIncompatible()
			if version != tt.newer || tt.newer != "" && engineProblemKey(reason) != "extension-version.hub_too_old" ||
				tt.newer == "" && reason != nil {
				t.Fatalf("latest incompatible = %q, %v", version, reason)
			}
		})
	}
	ix, warnings, err := ParseIndexForBuild(releaseCatalog(t, base, modern, future), 50)
	if err != nil || len(ix.Extensions) != 1 || len(warnings) != 0 {
		t.Fatalf("index with all releases blocked: %+v, %v, %v", ix, warnings, err)
	}
	e := &ix.Extensions[0]
	if e.Parsed.Version != "3.0.0" || e.PackageFor(Platform()) != nil ||
		engineProblemKey(e.EngineProblem(50)) != "extension-version.hub_too_old" {
		t.Fatalf("newest blocked release missing from catalog: %+v", e)
	}
}

func TestMarketReleasesPreferLocalPackageAndKeepOrderOnTies(t *testing.T) {
	base := testMarketRelease(t, "2.0.0", "", nil)
	remote := testMarketRelease(t, "3.0.0", "", nil)
	remote.Packages[0].Platforms = []string{Platform() + "-other"}
	local := testMarketRelease(t, "2.0.0", "", nil)
	local.Packages[0].URL = "local.tar.gz"
	for _, tt := range []struct {
		name     string
		base     MarketRelease
		releases []MarketRelease
		wantURL  string
	}{
		{"local package wins and the first release wins ties", base, []MarketRelease{remote, local}, "local.tar.gz"},
		{"highest manifest when no package matches", remote, []MarketRelease{{
			Manifest: base.Manifest, Packages: []MarketPackage{{Platforms: remote.Packages[0].Platforms, URL: "old.tar.gz", SHA256: base.Packages[0].SHA256}},
		}}, "3.0.0.tar.gz"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ix, warnings, err := ParseIndexForBuild(releaseCatalog(t, tt.base, tt.releases...), 200)
			if err != nil || len(ix.Extensions) != 1 || len(warnings) != 0 || ix.Extensions[0].Packages[0].URL != tt.wantURL {
				t.Fatalf("index = %+v, warnings = %v, error = %v", ix, warnings, err)
			}
			if version, reason := ix.Extensions[0].LatestIncompatible(); version != "" || reason != nil {
				t.Fatalf("platform difference produced an engine hint: %q, %v", version, reason)
			}
		})
	}
}

func TestMarketFutureManifestDisplayRequiresValidIncompatibleEngines(t *testing.T) {
	for _, tt := range []struct {
		name, persona  string
		build          int64
		change         func(map[string]any)
		wantProblemKey string
	}{
		{"needs a newer hub", ">=200", 100, nil, "extension-version.hub_too_old"},
		{"past the supported hub range", "<100", 100, nil, "extension-version.hub_incompatible"},
		{"compatible but malformed", ">=200", 200, nil, ""},
		{"development build", ">=200", 0, nil, ""},
		{"no range", "", 100, nil, ""},
		{"invalid range", ">=soon", 100, nil, ""},
		{"unsafe ID", ">=200", 100, func(m map[string]any) { m["id"] = "../outside" }, ""},
		{"invalid version", ">=200", 100, func(m map[string]any) { m["version"] = "next" }, ""},
		{"manifest over the size limit", ">=200", 100, func(m map[string]any) { m["future"] = strings.Repeat("x", ManifestMost) }, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			release := testMarketRelease(t, "2.0.0", tt.persona, func(m map[string]any) {
				futureConfigField(m)
				if tt.change != nil {
					tt.change(m)
				}
			})
			ix, warnings, err := ParseIndexForBuild(releaseCatalog(t, release), tt.build)
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantProblemKey == "" {
				if len(ix.Extensions) != 0 || len(warnings) != 1 {
					t.Fatalf("accepted invalid manifest: %+v, %v", ix.Extensions, warnings)
				}
				return
			}
			if len(ix.Extensions) != 1 || len(warnings) != 0 {
				t.Fatalf("lost incompatible entry: %+v, %v", ix.Extensions, warnings)
			}
			e := &ix.Extensions[0]
			m := e.Parsed
			if m.ID != "example-notes" || m.Name != "Example Notes" || m.Version != "2.0.0" || m.Description == "" ||
				m.Publisher != "example" || m.Homepage != "https://example.com/persona-plugin" ||
				m.Runtime != nil || len(m.Contributes) != 0 || len(m.Configuration) != 0 ||
				e.PackageFor(Platform()) != nil ||
				engineProblemKey(e.EngineProblem(tt.build)) != tt.wantProblemKey ||
				engineProblemKey(e.EngineProblem(0)) != tt.wantProblemKey {
				t.Fatalf("wrong display metadata or compatibility result: %+v, %v", m, e.EngineProblem(tt.build))
			}
		})
	}
}

func TestSignedMarketReleasesCheckEveryIdentityAndPackage(t *testing.T) {
	base := testMarketRelease(t, "1.2.0", "", nil)
	alt := testMarketRelease(t, "2.0.0", ">=200", futureConfigField)
	other := testMarketRelease(t, "2.0.0", ">=200", func(m map[string]any) {
		futureConfigField(m)
		m["id"] = "other-extension"
	})
	badHash := alt
	badHash.Packages = []MarketPackage{{URL: "future.tar.gz", SHA256: "invalid"}}
	badURL := alt
	badURL.Packages = []MarketPackage{{URL: "https://example.com/%xx.tar.gz", SHA256: alt.Packages[0].SHA256}}
	badPath := alt
	badPath.Packages = []MarketPackage{{URL: "../future.tar.gz", SHA256: alt.Packages[0].SHA256}}
	noPackages := alt
	noPackages.Packages = nil
	key, sign := testKey(t)
	for _, tt := range []struct {
		name     string
		releases []MarketRelease
		valid    bool
	}{
		{"limit", slices.Repeat([]MarketRelease{alt}, marketReleaseMost), true},
		{"over limit", slices.Repeat([]MarketRelease{alt}, marketReleaseMost+1), false},
		{"mismatched ID", []MarketRelease{other}, false},
		{"bad hash", []MarketRelease{badHash}, false},
		{"bad URL", []MarketRelease{badURL}, false},
		{"bad relative path", []MarketRelease{badPath}, false},
		{"missing packages", []MarketRelease{noPackages}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, err := ParseCatalogKeysForBuild(sign(releaseCatalog(t, base, tt.releases...)), []string{key}, 100)
			if err != nil {
				t.Fatal(err)
			}
			if f.SignedBy != key || tt.valid != (len(f.Index.Extensions) == 1 && len(f.Warnings) == 0) ||
				!tt.valid && (len(f.Index.Extensions) != 0 || len(f.Warnings) != 1) {
				t.Fatalf("entries = %d, warnings = %v", len(f.Index.Extensions), f.Warnings)
			}
		})
	}
}

func TestSignedMarketReleaseRejectsAmbiguousJSON(t *testing.T) {
	base := testMarketRelease(t, "1.2.0", "", nil)
	future := testMarketRelease(t, "2.0.0", ">=200", futureConfigField)
	manifest, packages := future.Manifest, mustReleasePackages(t, future.Packages)
	key, sign := testKey(t)
	for _, fields := range []string{
		fmt.Sprintf(`"Manifest":%s,"packages":%s`, manifest, packages),
		fmt.Sprintf(`"manifest":%s,"PACKAGES":%s`, manifest, packages),
		fmt.Sprintf(`"manifest":%s,"packages":%s,"L10N":{}`, manifest, packages),
		fmt.Sprintf(`"manifest":%s,"packages":%s,"l10n":{},"l10n":{}`, manifest, packages),
		fmt.Sprintf(`"manifest":%s,"packages":%s`, bytes.Replace(manifest, []byte(`"id":`), []byte(`"Id":"other","id":`), 1), packages),
		fmt.Sprintf(`"manifest":%s,"packages":%s`, manifest, bytes.Replace(packages, []byte(`"url":`), []byte(`"URL":"other","url":`), 1)),
	} {
		payload := fmt.Appendf(nil, `{"market_version":1,"name":"Example","extensions":[{"manifest":%s,"packages":%s,"releases":[{%s}]}]}`,
			base.Manifest, mustReleasePackages(t, base.Packages), fields)
		if _, err := ParseCatalogKeysForBuild(sign(payload), []string{key}, 100); !errors.Is(err, ErrCatalogUnverified) {
			t.Fatalf("accepted ambiguous signed release: %s, %v", fields, err)
		}
	}
	payload := releaseCatalog(t, base, future)
	for _, bad := range [][]byte{
		bytes.Replace(payload, []byte(`"releases":`), []byte(`"Releases":[],"releases":`), 1),
		bytes.Replace(payload, []byte(`"future_layout":true`), []byte(`"future_layout":true,"future_layout":false`), 1),
	} {
		if _, err := ParseCatalogKeysForBuild(sign(bad), []string{key}, 100); !errors.Is(err, ErrCatalogUnverified) {
			t.Fatalf("accepted ambiguous signed catalog: %v", err)
		}
	}
	badUTF8 := bytes.Replace(payload, []byte("future_layout"), []byte{'f', 0xff}, 1)
	if _, _, err := ParseIndexForBuild(badUTF8, 100); err == nil {
		t.Fatal("accepted invalid UTF-8 in a future manifest")
	}
}

func mustReleasePackages(t *testing.T, packages []MarketPackage) []byte {
	t.Helper()
	data, err := json.Marshal(packages)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSignedMarketReleaseProvenanceBindsResolution(t *testing.T) {
	base := testMarketRelease(t, "1.2.0", "", nil)
	alt := testMarketRelease(t, "2.0.0", ">=200", nil)
	alt.L10n = json.RawMessage(`{"en":{"name":"Release translation"}}`)
	future := testMarketRelease(t, "3.0.0", ">=300", futureConfigField)
	key, sign := testKey(t)
	payload := releaseCatalog(t, base, alt, future)
	raw := sign(payload)
	if _, err := ParseCatalog(bytes.Replace(raw, []byte("3.0.0.tar.gz"), []byte("other.tar.gz"), 1), key); !errors.Is(err, ErrCatalogUnverified) {
		t.Fatalf("changed release in signed payload: %v", err)
	}
	market := &Market{Spec: MarketSpec{Kind: "https", URL: "https://example.com/catalog.json"}, TrustedKeys: []string{key}}
	for _, tt := range []struct {
		name   string
		change func(*MarketEntry)
	}{
		{"active manifest", func(e *MarketEntry) { e.Manifest = base.Manifest }},
		{"parsed manifest", func(e *MarketEntry) { e.Parsed.Version = "3.0.0" }},
		{"active package", func(e *MarketEntry) { e.Packages = base.Packages }},
		{"translations", func(e *MarketEntry) { e.L10n = nil }},
		{"unselected release", func(e *MarketEntry) { e.Releases[1].Packages = base.Packages }},
		{"all active fields replaced by other signed release", func(e *MarketEntry) {
			e.Manifest, e.Packages, e.L10n = base.Manifest, base.Packages, base.L10n
			e.Parsed, _ = Parse(base.Manifest)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, err := ParseCatalogKeysForBuild(raw, []string{key}, 200)
			if err != nil || f.SignedBy != key || !bytes.Equal(f.Payload, payload) || !bytes.Equal(f.Raw, raw) {
				t.Fatalf("exact signed payload lost: %v", err)
			}
			e := &f.Index.Extensions[0]
			if e.Parsed.Version != "2.0.0" || e.verifiedEntry != entryFingerprint(e) {
				t.Fatalf("wrong resolved entry or fingerprint: %+v", e)
			}
			tt.change(e)
			reg := NewRegistry(t.TempDir(), newMemStore(), nil, 200)
			_, err = reg.PreviewMarket(t.Context(), market, "example", e, "")
			if engineProblemKey(err) != "extension-market_security.private_signature" {
				t.Fatalf("tampered resolution was not rejected by the signature check: %v", err)
			}
		})
	}
}
