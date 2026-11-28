package extwire

import (
	"encoding/json"

	"github.com/nwverse/persona/server/internal/buswire"
)

// Moving a source (a mail account) from one manager to another (docs/assistant/managers.md): each manager has its own database
// and its own secret.key, so the source's sealed secrets cannot be copied as they are. The hub asks the old manager
// for the source (export), gives it to the new one (import), and then tells the old one to let it go (release). The
// hub never stores or logs the bundle.
const (
	// MethodSourceExport: SourceRef → SourceBundle. The source as it is, its secrets in plaintext (only over the bus).
	MethodSourceExport = "extensions.source_export"
	// MethodSourceImport: SourceBundle → SourceRef: the receiving manager takes the source (sealing its secrets with
	// its own key) and starts using it; the ref it answers is the source as it now has it.
	MethodSourceImport = "extensions.source_import"
	// MethodSourceRelease: SourceRef → {}: the old manager lets the source go: it stops using it and deletes it and
	// its secrets (not the knowledge it already filed).
	MethodSourceRelease = "extensions.source_release"

	// CodeNoSource: no such source here; CodeSourceExists: the receiving manager has it already; CodeSourceKind: a
	// kind of source that cannot move (yet).
	CodeNoSource     = "no_source"
	CodeSourceExists = "source_exists"
	CodeSourceKind   = "source_kind"
)

var (
	SourceExportMethod  = buswire.Method[SourceRef, SourceBundle]{Name: MethodSourceExport}
	SourceImportMethod  = buswire.Method[SourceBundle, SourceRef]{Name: MethodSourceImport}
	SourceReleaseMethod = buswire.Method[SourceRef, buswire.None]{Name: MethodSourceRelease}
)

// SourceRef names a source: "mail:<account id>" (the only kind so far).
type SourceRef struct {
	Source string `json:"source"`
}

// SourceBundle is a source on its way from one manager to another.
type SourceBundle struct {
	Source string `json:"source"`
	Kind   string `json:"kind"` // "mail"
	// Data is the kind's own shape; it holds the secrets: never logged or stored as it is.
	Data json.RawMessage `json:"data"`
}
