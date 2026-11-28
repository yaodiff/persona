// Package knowledgeuihost is the seam between the core and the knowledge pages' module (internal/modules/knowledge-ui,
// docs/architecture/core-and-modules.md "Knowledge UI"): what the core asks of the module (Layer) and what the module
// asks of the core (Host), each in terms both sides name (the standard library, the knowledge base, the contracts).
// The composition root (internal/modules/all) provides the module in the init of its file, under the module's build
// constraint; without it nothing is provided and the core runs with no such layer: no history import, its tools and
// its chat lists, and knowledge base days without the marks of what the owner sent from Persona.
//
// The module is provided while the program initializes, before main runs, and only read afterwards.
package knowledgeuihost

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/moduleswitch"
)

// ModuleID is the module's id (its module.json): the kernel's switch turns it off by this name.
const ModuleID = "knowledge-ui"

// Layer is what the core asks of the knowledge pages' module.
type Layer interface {
	ProviderCheckpoint(e *kb.Engine, cfg *assistantwire.KBHistory, binding assistantwire.KBImportProvider, day string, now time.Time) (wire.KBImportCheckpointResult, bool)
	// Tool runs one of the history import's assistant tools (kb_history_plan, kb_history_status, kb_history_stop,
	// kb_history_resume).
	Tool(ctx context.Context, name string, args json.RawMessage) (string, error)
	// RunOnce runs what the history import of the knowledge base in data can run now.
	RunOnce(ctx context.Context, data string) error
	// Chats are source's chats for the selection page (GET /api/v1/kb/selection/candidates): its plan's while the
	// import runs or is stopped (cached), else listed by the source now (the names refreshed through the host). A
	// chat's Source is what it is filed under ("": the connector's id).
	Chats(ctx context.Context, data, source string) (chats []connectorwire.HistoryChat, listedAt time.Time, cached bool, err error)
	// ChatsProblem says why Chats failed: "no_history" (the source offers no history now), "stopped" (the import
	// stopped; text is why, in l) or "failed" (text is the error's code).
	ChatsProblem(err error, l i18n.Lang) (problem, text string)
	// Need is what the import reads of chat c of the connector now, by the selection.
	Need(sel *kb.Selection, ret kb.Retention, connector string, c connectorwire.HistoryChat) kb.ReadPlan
	// Day is the day p names with the lines the owner sent from Persona marked.
	Day(ctx context.Context, e *kb.Engine, p assistantwire.KBDayParams) (assistantwire.KBDayResult, error)
}

// Host is what the module asks of the core: the same methods as the module's own Host.
type Host interface {
	Logf(format string, args ...any)
	Lang(ctx context.Context) i18n.Lang
	Location(ctx context.Context) *time.Location
	// Engine is the knowledge base open now; WritableEngine the same only when it can be written.
	Engine(ctx context.Context) (*kb.Engine, error)
	WritableEngine(ctx context.Context) (*kb.Engine, error)
	// Settings are the knowledge base's settings; History its history import's while the knowledge base is on and
	// writable (nil otherwise).
	Settings(ctx context.Context) *assistantwire.KBSettings
	History(ctx context.Context) *assistantwire.KBHistory
	// Connected: the manager is connected to the hub; Current is the knowledge base open now (nil: none).
	Connected() bool
	Current() *kb.Engine
	// Wake has the core's import loop look again now.
	Wake()
	// Notify pushes the import's notice to the owner.
	Notify(ctx context.Context, title, body string)
	UpdateSelection(ctx context.Context, f func(*kb.Selection) (bool, error)) (*kb.Selection, error)
	RefreshNames(ctx context.Context, connector string, chats []connectorwire.HistoryChat)
	// The connectors' archives, through the hub (connectors.*).
	Archives(ctx context.Context) ([]assistantwire.ArchiveSource, error)
	HistoryChats(ctx context.Context, id string) ([]connectorwire.HistoryChat, error)
	HistoryPage(ctx context.Context, id string, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error)
	HistoryAnchors(ctx context.Context, id string, q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error)
	ArchiveMedia(ctx context.Context, id, ref string) (connectorwire.Media, error)
	// HasSentLedger: the store keeps the ledger of what was written as the owner; SentAsUsersForArchive reads one
	// connector's rows of it in [since, until), oldest first.
	HasSentLedger() bool
	SentAsUsersForArchive(ctx context.Context, connector string, since, until time.Time) ([]connectorwire.SentAsUser, error)
	// DayStart is the start of the day now is in, in now's zone.
	DayStart(now time.Time) time.Time
}

var port = moduleswitch.NewPort[func(Host) Layer]("knowledgeuihost")

// Provide provides the module under its id: new makes its layer for one manager's host (init only; twice panics).
func Provide(module string, new func(Host) Layer) { port.Provide(module, new) }

// InBuild says whether the module is in this build and s has it on.
func InBuild(s moduleswitch.Set) bool { return port.On(s) }

// New is the module's layer for h; false when the module is not in this build or s switched it off (the `modules`
// setting the manager started with), so it is absent exactly as one built out.
func New(s moduleswitch.Set, h Host) (Layer, bool) {
	newLayer, ok := port.Get(s)
	if !ok {
		return nil, false
	}
	return newLayer(h), true
}
