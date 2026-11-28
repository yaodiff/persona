package extension

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"path"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// MarketIndex is the canonical extension index.
type MarketIndex struct {
	MarketVersion int    `json:"market_version"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Homepage      string `json:"homepage,omitempty"`
	// SigningKey is legacy catalog metadata, never a trust grant.
	SigningKey         string `json:"signing_key,omitempty"`
	DownloadsUpdatedAt string `json:"downloads_updated_at,omitempty"`
	// Extensions are "extensions" in an index; the old key "plugins" is read too, and still the one written out (the
	// hub keeps the index it read, which a hub rolled back to a build before #1844 reads again).
	Extensions []MarketEntry `json:"plugins"`
}

func (ix *MarketIndex) UnmarshalJSON(data []byte) error {
	type plain MarketIndex
	var v struct {
		plain
		Current []MarketEntry `json:"extensions"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*ix = MarketIndex(v.plain)
	if v.Current != nil {
		ix.Extensions = v.Current
	}
	return nil
}

// MarketEntry is one plugin of a market: its manifest (so the permissions are known before downloading) and its
// packages.
type MarketEntry struct {
	Replaces  string          `json:"replaces,omitempty"`
	Icon      string          `json:"icon,omitempty"`
	IconURL   string          `json:"icon_url,omitempty"`
	Downloads *int64          `json:"downloads,omitempty"`
	Manifest  json.RawMessage `json:"manifest"`
	Tags      []string        `json:"tags,omitempty"`
	Packages  []MarketPackage `json:"packages"`
	// L10n is the package's translations again ({"<language>": <l10n/<language>.json>}, l10n.go), so the catalog can
	// show them before anything is downloaded. Kept as written (read with Translations); hubs before it ignore the key.
	L10n json.RawMessage `json:"l10n,omitempty"`
	// Releases are alternative versions of the same extension. The top-level fields also represent a release.
	Releases []MarketRelease `json:"releases,omitempty"`
	// Parsed is the active manifest, or display metadata for an entry blocked by the hub build.
	Parsed              *Manifest `json:"-"`
	engineProblem       error
	newerVersion        string
	newerVersionProblem error
	// Only ParseCatalog can create this proof; public fields remain checked
	// against the snapshot before the registry accepts official provenance.
	verifiedKey   string
	verifiedEntry [sha256.Size]byte
}

// MarketRelease pairs a version's manifest with its packages and optional catalog translations.
type MarketRelease struct {
	Manifest json.RawMessage `json:"manifest"`
	Packages []MarketPackage `json:"packages"`
	L10n     json.RawMessage `json:"l10n,omitempty"`
}

// MarketPackage is the catalog's package reference.
type MarketPackage = extwire.MarketPackage

// ParseIndex reads an index with the strict manifest validation of build zero.
func ParseIndex(data []byte) (*MarketIndex, []string, error) {
	return ParseIndexForBuild(data, 0)
}

// ParseIndexForBuild reads an index and resolves its entries for the given hub build.
func ParseIndexForBuild(data []byte, build int64) (*MarketIndex, []string, error) {
	if len(data) > IndexMost {
		return nil, nil, i18n.Err("extension-market_read.too_large", i18n.Args{"bytes": IndexMost})
	}
	if err := strictJSON(data); err != nil {
		if !json.Valid(data) {
			return nil, nil, notCatalog(data)
		}
		return nil, nil, i18n.Err("extension-market_security.catalog")
	}
	return parseIndex(data, build)
}

// parseIndex takes a payload already checked by strictJSON.
func parseIndex(data []byte, build int64) (*MarketIndex, []string, error) {
	if len(data) > IndexMost {
		return nil, nil, i18n.Err("extension-market_read.too_large", i18n.Args{"bytes": IndexMost})
	}
	if err := catalogFields(data, catalogIndexType); err != nil {
		return nil, nil, err
	}
	var ix MarketIndex
	if err := json.Unmarshal(data, &ix); err != nil {
		return nil, nil, notCatalog(data)
	}
	switch {
	case ix.MarketVersion > 1:
		return nil, nil, i18n.Err("extension-market_read.newer_format", i18n.Args{"version": ix.MarketVersion})
	case ix.MarketVersion != 1:
		return nil, nil, notCatalog(data)
	}
	ix.Name = strings.TrimSpace(ix.Name)
	if ix.Name == "" || len([]rune(ix.Name)) > 60 {
		return nil, nil, i18n.Err("extension-market_read.no_name")
	}
	if len([]rune(ix.Description)) > StringMost {
		return nil, nil, i18n.Err("extension-market_read.description_too_long")
	}
	if ix.Homepage != "" && !isHTTPS(ix.Homepage) {
		ix.Homepage = ""
	}
	if ix.DownloadsUpdatedAt != "" {
		if _, err := time.Parse(time.RFC3339, ix.DownloadsUpdatedAt); err != nil {
			ix.DownloadsUpdatedAt = ""
		}
	}
	if len(ix.Extensions) > IndexExtensionsMost {
		return nil, nil, i18n.Err("extension-market_read.catalog_over_limit", i18n.Args{"most": IndexExtensionsMost})
	}
	// Warnings are kept with the market in the default language and shown in the reader's (i18n.MarketText).
	var warnings []string
	declaredTargets := map[string]bool{}
	seen := map[string]bool{}
	kept := ix.Extensions[:0]
	for i, e := range ix.Extensions {
		base, err := readMarketCandidate(MarketRelease{Manifest: e.Manifest, Packages: e.Packages, L10n: e.L10n}, build)
		if e.Replaces != "" {
			// Identity declarations are never silently dropped or resolved first-entry-wins.
			if err != nil || declaredTargets[base.parsed.ID] || !ValidID(e.Replaces) || e.Replaces == base.parsed.ID {
				return nil, nil, ErrIdentity
			}
			declaredTargets[base.parsed.ID] = true
		}
		var releaseWarnings []string
		if err == nil {
			releaseWarnings, err = e.resolve(base, build)
		}
		switch {
		case err != nil:
			warnings = append(warnings, i18n.T(i18n.Default, "extension-market_read.entry_skipped", i18n.Args{"n": i + 1, "err": err.Error()}))
		case seen[e.Parsed.ID]:
			warnings = append(warnings, i18n.T(i18n.Default, "extension-market_read.entry_duplicate", i18n.Args{"id": e.Parsed.ID}))
		default:
			seen[e.Parsed.ID] = true
			warnings = append(warnings, releaseWarnings...)
			if e.Icon != "" && !ValidIconPath(e.Icon) {
				e.Icon, e.IconURL = "", ""
			}
			if !ValidIconURL(e.IconURL) {
				e.IconURL = ""
			}
			if e.Downloads != nil && *e.Downloads < 0 {
				e.Downloads = nil
			}
			if _, err := ParseEntryTranslations(e.L10n); err != nil { // the entry stays, in its manifest's words
				warnings = append(warnings, i18n.T(i18n.En, "extension-status.entry_translation_skipped", i18n.Args{"id": e.Parsed.ID, "err": err.Error()}))
				e.L10n = nil
			}
			kept = append(kept, e)
		}
	}
	ix.Extensions = kept
	if err := ValidateReplacements(ix.Extensions); err != nil {
		return nil, warnings, err
	}
	return &ix, warnings, nil
}

// notCatalog says what a document that is not a market's catalog is, for whoever entered the address: a web page,
// or JSON that is something else (a GitHub API answer, another file, or the answer of a proxy or sign-in page on the
// way, quoted when it carries a message). Without market_version it is never taken for a catalog of format 0.
func notCatalog(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '<' {
		return i18n.Err("extension-market_read.web_page")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) == nil {
		// GitHub answers a file's page with the page's own data when asked for JSON: {"meta": …, "payload": {…}}.
		if p := bytes.TrimSpace(fields["payload"]); len(p) > 0 && p[0] == '{' && fields["meta"] != nil {
			return i18n.Err("extension-market_read.web_page")
		}
		for _, key := range []string{"message", "error", "msg"} {
			var said string
			if json.Unmarshal(fields[key], &said) == nil {
				if said = clipSaid(said); said != "" {
					return i18n.Err("extension-market_read.not_catalog_said", i18n.Args{"message": said})
				}
			}
		}
	}
	return i18n.Err("extension-market_read.not_catalog")
}

// clipSaid is what a non-catalog answer said, on one line and at most 160 characters.
func clipSaid(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160]) + "…"
	}
	return s
}

// Translations are the entry's l10n, read (ParseIndex has left out one that does not check out).
func (e *MarketEntry) Translations() Translations {
	ts, _ := ParseEntryTranslations(e.L10n)
	return ts
}

func checkMarketPackages(id string, packages []MarketPackage) error {
	if len(packages) == 0 || len(packages) > marketPackageMost {
		return i18n.Err("extension-market_read.entry_package_count", i18n.Args{"id": id, "most": marketPackageMost})
	}
	for _, p := range packages {
		if b, err := hex.DecodeString(p.SHA256); err != nil || len(b) != sha256.Size {
			return i18n.Err("extension-market_read.entry_package_sha256", i18n.Args{"id": id})
		}
		if strings.HasPrefix(p.URL, "oci:") {
			if _, err := parseOCI(p.URL); err != nil || p.Size <= 0 || p.Size > PackageMost {
				return i18n.Err("extension-market_security.package")
			}
			continue
		}
		if p.Size < 0 || p.Size > PackageMost {
			return i18n.Err("extension-market_security.package")
		}
		if !validMarketPackageURL(p.URL) {
			return i18n.Err("extension-market_read.entry_package_url", i18n.Args{"id": id})
		}
	}
	return nil
}

func validMarketPackageURL(ref string) bool {
	switch {
	case strings.HasPrefix(ref, "https://"):
		u, err := url.Parse(ref)
		return err == nil && u.Hostname() != "" && u.User == nil
	case strings.HasPrefix(ref, "release:"):
		tag, asset, _ := strings.Cut(strings.TrimPrefix(ref, "release:"), "/")
		return tag != "" && asset != ""
	case ref == "" || strings.Contains(ref, "://"):
		return false
	default:
		return path.Clean("/" + ref)[1:] == ref
	}
}

func ValidIconURL(s string) bool {
	if len(s) > 2048 {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil
}

func isHTTPS(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

// Platform is this hub's GOOS-GOARCH.
func Platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

// PackageFor is the active entry's package for platform, or nil when unavailable or blocked by the hub build.
func (e *MarketEntry) PackageFor(platform string) *MarketPackage {
	if e.engineProblem != nil {
		return nil
	}
	return packageFor(e.Packages, platform)
}

func packageFor(packages []MarketPackage, platform string) *MarketPackage {
	for i, p := range packages {
		if len(p.Platforms) == 0 || slices.Contains(p.Platforms, platform) {
			return &packages[i]
		}
	}
	return nil
}
