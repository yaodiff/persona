package update

import (
	"debug/buildinfo"
	"fmt"
	"strings"

	"github.com/nwverse/persona/server/internal/nodeproto"
)

// Editions (nodeproto.Edition): a release and every swap are of one edition, and an install keeps the one it was
// installed as. This build's is ThisEdition (edition_full.go, edition_core.go); any Go executable's is read from the
// -tags its build info records, for every OS and architecture, without running it (EditionOf).

// EditionOf is the edition of the Go executable at path: EditionCore when it was built with the persona_core_only
// tag, EditionFull otherwise (every build from before editions too). ok is false when path is not a Go executable
// that can be read: nothing is known about it.
func EditionOf(path string) (e nodeproto.Edition, ok bool) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", false
	}
	for _, s := range info.Settings {
		if s.Key == "-tags" {
			return editionOfTags(s.Value), true
		}
	}
	return nodeproto.EditionFull, true
}

// editionOfTags reads a -tags value as go build records it (comma separated; spaces from older command lines).
func editionOfTags(tags string) nodeproto.Edition {
	for _, tag := range strings.FieldsFunc(tags, func(r rune) bool { return r == ',' || r == ' ' }) {
		if tag == nodeproto.CoreOnlyTag {
			return nodeproto.EditionCore
		}
	}
	return nodeproto.EditionFull
}

// CheckEdition refuses the executable at path when it is not of edition want: a core-only install is never given a
// full build and a full install never a core-only one (docs/operations/core-install.md "Updates stay core-only").
// what names the file for the error. A file whose build info cannot be read says nothing and passes, as CheckReplace
// lets it: every program a release lists is a Go build, whose other checks (sha256, version -v) still hold.
func CheckEdition(path string, want nodeproto.Edition, what string) error {
	if got, ok := EditionOf(path); ok && got != want {
		return fmt.Errorf("%s is the %s edition, not %s: an install keeps its edition", what, got, want)
	}
	return nil
}

// ValidateEdition keeps a release of another edition out: m must say want (a manifest from before editions, or a
// full one, says nothing and is full).
func (m *Manifest) ValidateEdition(want nodeproto.Edition) error {
	got, ok := nodeproto.ParseEdition(m.Edition)
	if !ok {
		return fmt.Errorf("the release manifest names an unknown edition %q", m.Edition)
	}
	if got != want {
		return fmt.Errorf("the release manifest is of the %s edition, not %s", got, want)
	}
	return nil
}

// FeedName is the newest release's manifest for edition e, beside its downloads: latest.json, core-latest.json.
func FeedName(e nodeproto.Edition) string { return e.Served("latest.json") }

// ReleaseManifestName is one release's manifest for edition e under dl/<version>/: manifest.json, core-manifest.json.
func ReleaseManifestName(e nodeproto.Edition) string { return e.Served("manifest.json") }

// Remote is the name f is downloaded under: Served when the manifest gives one (the core edition's core-<name>),
// else Name. Name stays what the file is called once here (personad-linux-amd64.gz), whatever the edition.
func (f File) Remote() string {
	if f.Served != "" {
		return f.Served
	}
	return f.Name
}
