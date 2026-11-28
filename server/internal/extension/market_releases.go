package extension

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
)

const marketReleaseMost = 16

type marketCandidate struct {
	MarketRelease
	parsed  *Manifest
	problem error
}

// readMarketCandidate takes a manifest from a catalog whose JSON keys and encoding have already been checked.
func readMarketCandidate(release MarketRelease, build int64) (marketCandidate, error) {
	c := marketCandidate{MarketRelease: release}
	if len(release.Manifest) == 0 {
		return c, i18n.Err("extension-market_read.entry_no_manifest")
	}
	m, err := Parse(release.Manifest)
	if err == nil {
		c.parsed = m
		_, c.problem = CheckEngines(m, build)
		return c, nil
	}

	// Read the identity even for a rejected alternative so it cannot claim another extension's ID.
	c.parsed = marketDisplayMetadata(release.Manifest)
	m = c.parsed
	if build <= 0 || m == nil || !ValidID(m.ID) || len(m.Version) > StringMost ||
		len(m.Engines.Persona) > StringMost {
		return c, err
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(m.Name)); n == 0 || n > 40 {
		return c, err
	}
	if _, versionErr := ParseVersion(m.Version); versionErr != nil {
		return c, err
	}
	_, problem := CheckEngines(m, build)
	switch engineProblemKey(problem) {
	case "extension-version.hub_too_old", "extension-version.hub_incompatible":
		c.problem = problem
		return c, nil
	default:
		return c, err
	}
}

// marketDisplayMetadata reads only the fields needed to show an incompatible, non-installable release.
func marketDisplayMetadata(raw json.RawMessage) *Manifest {
	if len(raw) > ManifestMost {
		return nil
	}
	var fields struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description"`
		Publisher   string `json:"publisher"`
		Homepage    string `json:"homepage"`
		Engines     struct {
			Persona string `json:"persona"`
		} `json:"engines"`
	}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil
	}
	homepage := fields.Homepage
	if len(homepage) > StringMost || !isHTTPS(homepage) {
		homepage = ""
	}
	return &Manifest{
		ID: fields.ID, Name: fields.Name, Version: fields.Version,
		Description: clip(fields.Description, StringMost), Publisher: clip(fields.Publisher, StringMost),
		Homepage: homepage, Engines: Engines{Persona: fields.Engines.Persona},
	}
}

func engineProblemKey(err error) string {
	if message, ok := err.(*i18n.Message); ok {
		return message.Key
	}
	return ""
}

func (c marketCandidate) newerThan(other marketCandidate) bool {
	return other.parsed == nil || NewerVersion(c.parsed.Version, other.parsed.Version)
}

func (e *MarketEntry) resolve(base marketCandidate, build int64) ([]string, error) {
	id := base.parsed.ID
	if len(e.Releases) > marketReleaseMost {
		return nil, i18n.Err("extension-market_read.entry_release_count", i18n.Args{"id": id, "most": marketReleaseMost})
	}
	if err := checkMarketPackages(id, base.Packages); err != nil {
		return nil, err
	}
	var warnings []string
	var compatible, incompatible, needsNewerHub marketCandidate
	var compatibleHasPackage bool
	platform := Platform()
	consider := func(c marketCandidate) {
		if c.problem != nil {
			if c.newerThan(incompatible) {
				incompatible = c
			}
			if engineProblemKey(c.problem) == "extension-version.hub_too_old" && c.newerThan(needsNewerHub) {
				needsNewerHub = c
			}
			return
		}
		hasPackage := packageFor(c.Packages, platform) != nil
		if compatible.parsed == nil || hasPackage && !compatibleHasPackage ||
			hasPackage == compatibleHasPackage && c.newerThan(compatible) {
			compatible, compatibleHasPackage = c, hasPackage
		}
	}
	for i, release := range e.Releases {
		c, err := readMarketCandidate(release, build)
		if c.parsed == nil {
			return nil, err // The release's identity could not be checked.
		}
		if c.parsed.ID != id {
			return nil, i18n.Err("extension-market_read.entry_release_id", i18n.Args{"id": id, "n": i + 1})
		}
		if packageErr := checkMarketPackages(id, release.Packages); packageErr != nil {
			return nil, packageErr
		}
		if err != nil {
			warnings = append(warnings, i18n.T(i18n.Default, "extension-market_read.entry_release_skipped",
				i18n.Args{"id": id, "n": i + 1, "err": err.Error()}))
			continue
		}
		consider(c)
	}
	consider(base) // Earlier releases win equal versions with the same platform support.

	selected := compatible
	if selected.parsed == nil {
		selected = incompatible
	}
	e.Manifest, e.Parsed, e.Packages = selected.Manifest, selected.parsed, selected.Packages
	e.engineProblem = selected.problem
	if len(selected.L10n) != 0 {
		e.L10n = selected.L10n
	}
	if needsNewerHub.parsed != nil && needsNewerHub.newerThan(selected) {
		e.newerVersion, e.newerVersionProblem = needsNewerHub.parsed.Version, needsNewerHub.problem
	}
	// An entry without tags of its own shows the active manifest's tags.
	if len(e.Tags) == 0 {
		e.Tags = e.Parsed.Tags
	}
	if len(e.Tags) > marketTagMost {
		return nil, i18n.Err("extension-market_read.entry_too_many_tags")
	}
	if err := checkTags(e.Tags); err != nil {
		return nil, i18n.Err("extension-market_read.entry_bad_tag")
	}
	return warnings, nil
}

// EngineProblem reports a release blocked by the hub build. A display-only entry stays blocked if reused.
func (e *MarketEntry) EngineProblem(build int64) error {
	if e.engineProblem != nil {
		return e.engineProblem
	}
	_, err := CheckEngines(e.Parsed, build)
	return err
}

// LatestIncompatible reports a version above the active release that needs a newer hub, if one was published.
func (e *MarketEntry) LatestIncompatible() (version string, reason error) {
	return e.newerVersion, e.newerVersionProblem
}
