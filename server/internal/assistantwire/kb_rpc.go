package assistantwire

import (
	"encoding/json"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/wireids"
)

// Extension-facing knowledge-base calls. The hub checks the extension's granted scope before forwarding these.
const (
	MethodKBList   = "kb.list"
	MethodKBGet    = "kb.get"
	MethodKBDay    = "kb.day"
	MethodKBSearch = "kb.search"
	MethodKBUpdate = "kb.update"
	// MethodKBRecent lists what came in from every source since a time, newest first (#6100).
	MethodKBRecent = "kb.recent"
	// MethodKBExtensionUpdate is how the manager hands the assistant an extension's kb.update: with the extension's
	// id, which the extension cannot set (KBExtensionUpdate). Not on the extensions' allowlist.
	MethodKBExtensionUpdate = "kb.extension_update"
	// MethodKBExtensionDay adds sent provenance after the extension manager checks sent.read.
	MethodKBExtensionDay        = "kb.extension_day"
	MethodKBProviderCheckpoint  = "kb.provider_import_checkpoint"
	MethodKBProviderSentMarkers = "kb.provider_sent_markers"
	// CodeKBConflict: an extension's kb.update touches fields the user or another extension holds; data is
	// KBConflict.
	CodeKBConflict = "conflict"
)

var (
	KBListMethod            = buswire.Method[KBListParams, KBListResult]{Name: MethodKBList}
	KBRecentMethod          = buswire.Method[KBRecentParams, KBRecentResult]{Name: MethodKBRecent}
	KBGetMethod             = buswire.Method[KBGetParams, KBGetResult]{Name: MethodKBGet}
	KBDayMethod             = buswire.Method[KBDayParams, KBDayResult]{Name: MethodKBDay}
	KBExtensionDayMethod    = buswire.Method[KBDayParams, KBDayResult]{Name: MethodKBExtensionDay}
	KBSearchMethod          = buswire.Method[KBSearchParams, KBSearchResult]{Name: MethodKBSearch}
	KBUpdateMethod          = buswire.Method[KBUpdateParams, KBUpdateResult]{Name: MethodKBUpdate}
	KBExtensionUpdateMethod = buswire.Method[KBExtensionUpdate, KBUpdateResult]{Name: MethodKBExtensionUpdate}

	KBProviderCheckpointMethod  = buswire.Method[KBProviderRequest, wire.KBImportCheckpointResult]{Name: MethodKBProviderCheckpoint}
	KBProviderSentMarkersMethod = buswire.Method[KBProviderRequest, wire.KBSentMarkersResult]{Name: MethodKBProviderSentMarkers}
)

// KBProviderRequest is the manager's authenticated installation and its bounded query.
// InstalledAt identifies the installation across process restarts and upgrades.
type KBProviderRequest struct {
	Extension   wireids.ExtensionID `json:"extension"`
	StorageID   wireids.ExtensionID `json:"storage_id"`
	InstalledAt string              `json:"installed_at"`
	Params      KBImportQuery       `json:"params"`
}

// KBImportQuery holds the only selectors forwarded from the SDK: kb.sent_markers' params as the schema has them
// (KBSentMarkersParams: day and approval always there). A checkpoint takes an empty object and forwards the zero query;
// a marker page must supply a day and its approval.
type KBImportQuery struct {
	Day      string `json:"day"`
	Approval string `json:"approval"`
	After    int    `json:"after,omitempty"`
}

// KBExtensionUpdate is an extension's kb.update params as it sent them, and who it is.
type KBExtensionUpdate struct {
	StorageID string          `json:"storage_id,omitempty"`
	Extension string          `json:"extension"`
	Params    json.RawMessage `json:"params"`
}

func (x KBExtensionUpdate) StorageKey() string {
	if x.StorageID != "" {
		return x.StorageID
	}
	return x.Extension
}

// KBConflict is the data of a conflict error: field → its holder ("user" or "ext:<id>"); fields of a linked entity
// are "<kind>/<id>.<field>".
type KBConflict struct {
	Owners map[string]string `json:"owners"`
}

type KBListParams struct {
	Kind   string `json:"kind"`
	Tag    string `json:"tag,omitempty"`
	Limit  int    `json:"limit,omitempty"`  // default 30, at most 200
	Offset int    `json:"offset,omitempty"` // default 0; pages reflect the current ranking, without a snapshot
	Since  string `json:"since,omitempty"`  // RFC 3339: only entities updated at or after it
}

type KBListResult struct {
	Items      []kbwire.Entry `json:"items"`
	Tags       []string       `json:"tags"`
	Total      int            `json:"total"`
	NextOffset *int           `json:"next_offset,omitempty"`
}

type KBRecentParams struct {
	Since  string   `json:"since"`            // RFC 3339, required
	Until  string   `json:"until,omitempty"`  // RFC 3339: before it; none is now
	Kinds  []string `json:"kinds,omitempty"`  // default chats, docs, meetings, events, recordings
	Limit  int      `json:"limit,omitempty"`  // default 50, at most 200
	Offset int      `json:"offset,omitempty"` // default 0
}

type KBRecentResult struct {
	Items      []kbwire.Entry `json:"items"`
	Total      int            `json:"total"`
	NextOffset *int           `json:"next_offset,omitempty"`
}

type KBGetParams struct {
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	RecentChars *int   `json:"recent_chars,omitempty"` // default 12000; 0 omits recent messages; at most 20000
}

type KBGetResult struct {
	Detail *kbwire.Detail `json:"detail"`
}

type KBDayParams struct {
	ID  string `json:"id"`
	Day string `json:"day"` // YYYY-MM-DD in the knowledge base's time zone
}

type KBDayResult struct {
	SentOrigin string              `json:"sent_origin"` // available, missing_scope, or unavailable
	Day        string              `json:"day"`
	Messages   []kbwire.DayMessage `json:"messages"` // n is the first source-file line, 1-based
	Prev       string              `json:"prev"`
	Next       string              `json:"next"`
}

type KBSearchParams struct {
	Query    string `json:"query"`
	Messages *bool  `json:"messages,omitempty"` // default true
	Limit    int    `json:"limit,omitempty"`    // default 40, at most 200
}

type KBSearchResult struct {
	Hits []kbwire.Hit `json:"hits"`
}

type KBUpdateParams struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	kbwire.Update
	// Evidence: what the written values rest on, kept as the provenance of the fields this write holds (at most 20).
	Evidence []kbwire.Evidence `json:"evidence,omitempty"`
	// Unlock: fields this extension holds that it hands back to the built-in pipeline (unlocking pinned clears it).
	Unlock []string `json:"unlock,omitempty"`
}

type KBUpdateResult struct {
	ID string `json:"id"`
	// Before: the previous value of each field the update wrote (null when unset; summary is the old text, profile the
	// extension's own profile before), for undo.
	Before map[string]json.RawMessage `json:"before,omitempty"`
	// Ignored: fields the update carried that its kind does not use, not applied, each with why.
	Ignored []kbwire.IgnoredField `json:"ignored,omitempty"`
}
