package nodeproto

// Edition is which build of Persona a program is: every built-in module (EditionFull, the default build) or Persona
// Core alone (EditionCore, -tags persona_core_only; docs/architecture/core-and-modules.md "How to build and run the
// core alone"). A closed set: a release, a manifest and a swap are each of one edition, and an install keeps the
// edition it was installed as (docs/operations/core-install.md "Updates stay core-only").
type Edition string

const (
	EditionFull Edition = "full"
	EditionCore Edition = "core"
)

// CoreOnlyTag is the build tag that makes a program EditionCore.
const CoreOnlyTag = "persona_core_only"

// ParseEdition reads an edition as node.info and a release manifest write it. Left out is EditionFull: every node
// and every manifest from before editions is a full build. ok is false for any other word, which no caller may take
// for either edition.
func ParseEdition(s string) (e Edition, ok bool) {
	switch Edition(s) {
	case "", EditionFull:
		return EditionFull, true
	case EditionCore:
		return EditionCore, true
	}
	return "", false
}

// OrFull is e, or EditionFull for the zero Edition: a value nothing set (a test's zero Build, a node that says no
// edition) is the full build, as everything from before editions is.
func (e Edition) OrFull() Edition {
	if e == "" {
		return EditionFull
	}
	return e
}

// Marker is the edition as node.info and a manifest carry it: left out for EditionFull, so a full build's node.info and
// manifest stay byte for byte what they were before editions.
func (e Edition) Marker() string {
	if e == EditionCore {
		return string(EditionCore)
	}
	return ""
}

// artifactPrefix is what the core edition's downloads are served under beside the full ones: one flat namespace (the
// publisher's dl/, a GitHub release's assets) holds both, and no full install ever reads a name that starts with it.
const artifactPrefix = "core-"

// Served is the name a file called name is served under for this edition: name itself for EditionFull,
// core-<name> for EditionCore (core-latest.json, core-manifest.json, core-SHA256SUMS, core-personad-linux-amd64.gz).
func (e Edition) Served(name string) string {
	if e == EditionCore {
		return artifactPrefix + name
	}
	return name
}
