// Package modapi is the module contract of Persona Core (docs/architecture/core-and-modules.md): what a built-in
// feature module declares about itself (Info, the manifest of schema/module.schema.json), what it is (Module), and the
// one way it reaches the core (Host). It holds types, constants, the list of client-only module ids and the list of
// modules installed extensions may supply, imports nothing but the standard library and other contract packages
// (internal/wireids and internal/assistantwire), and is a contract package
// (internal/archguard/testdata/contracts.txt): the core (internal/assistant/kernel serves Host) and the modules
// (internal/modules/<id>) both import it, never each other.
package modapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// API is the version of this contract the host serves. A module whose Info.API differs is not loaded: a change that
// breaks a module bumps it.
const API = 1

// Origin says where a module comes from. Built-in modules are compiled into the manager (internal/modules/all);
// extensions are installed at run time (docs/extensions/extensions.md) and will share this registry shape.
type Origin string

const (
	OriginBuiltin   Origin = "builtin"
	OriginExtension Origin = "extension"
)

// Info is a module's manifest: the fields of schema/module.schema.json with the same names on every platform
// (#13430, section 4). Each list names what the module adds through one kind of slot; a module adds nothing it does
// not declare, and the host refuses a contribution that is not declared. Slots the server does not serve yet are
// declared here so manifests can be written against the whole contract; a module that fills one is not loaded until
// the host serves it (docs/architecture/core-and-modules.md "Slots").
type Info struct {
	// ID is the module's id: lower case, words joined by "-" (briefing, voice, screen-share).
	ID string `json:"id"`
	// API is the contract version the module was written for (API).
	API    int    `json:"api"`
	Origin Origin `json:"origin"`
	// Title is a short English label for logs and settings ("Daily briefing"); clients localize by id.
	Title string `json:"title,omitempty"`
	// Needs are module ids that must be loaded for this one to load (it may import them); Wants are ones it uses
	// when they are, through the kernel only: a wanted module may be left out of the build.
	Needs []string `json:"needs,omitempty"`
	Wants []string `json:"wants,omitempty"`
	// Feature is the wire feature flag module.<id> announced while the module is loaded ("" for none). A publishing
	// manager also announces the reserved modules marker, including when every module is absent.
	Feature string `json:"feature,omitempty"`

	HubRoutes       []string `json:"hub_routes,omitempty"`       // "METHOD /api/v1/…" patterns
	MCPTools        []string `json:"mcp_tools,omitempty"`        // assistant tool names
	PromptSections  []string `json:"prompt_sections,omitempty"`  // prompt template names
	Settings        []string `json:"settings,omitempty"`         // persona_settings keys
	ShellPlaces     []string `json:"shell_places,omitempty"`     // client navigation places
	ChatCardKinds   []string `json:"chat_card_kinds,omitempty"`  // chat item / card kinds
	ComposerActions []string `json:"composer_actions,omitempty"` // composer buttons and sources
	ChatAccessories []string `json:"chat_accessories,omitempty"` // views around the chat
	BackgroundJobs  []string `json:"background_jobs,omitempty"`  // loops the module runs
	Schedules       []string `json:"schedules,omitempty"`        // scheduler contributions (#13402)
	DBMigrations    []string `json:"db_migrations,omitempty"`    // migration file names the module owns
	I18nNamespaces  []string `json:"i18n_namespaces,omitempty"`  // localization namespaces
	NodePrograms    []string `json:"node_programs,omitempty"`    // programs the node runs for it
	ChannelKinds    []string `json:"channel_kinds,omitempty"`    // channel kinds it adds
	FrameHandlers   []string `json:"frame_handlers,omitempty"`   // hub frame / hello hooks
	PushCategories  []string `json:"push_categories,omitempty"`  // notification categories
}

// Schedule names a Schedules entry the host knows.
const (
	// ScheduleFactsName is the facts a scheduled routine with facts on opens with (Host.AddScheduleFacts).
	ScheduleFactsName = "facts"
)

// Module is a built-in feature module: its manifest and its registration. Register runs once, when the manager
// starts, and only for a module that is compiled in and switched on; everything the module adds goes through h. A
// module that returns an error is left out whole: nothing it added stays.
type Module interface {
	Info() Info
	Register(h Host) error
}

// Host is everything a module may use of the core. It grows one method per slot as the server learns to serve the
// slot (docs/architecture/core-and-modules.md "Slots"), and narrow read/write ports for the ledgers a module acts on;
// a module never reaches the core any other way.
type Host interface {
	// Logf writes to the manager's log, prefixed with the module's id.
	Logf(format string, args ...any)
	// AddSetting serves one of Info.Settings.
	AddSetting(Setting) error
	// AddScheduleFacts serves Info.Schedules' ScheduleFactsName entry.
	AddScheduleFacts(ScheduleFacts) error
	// AddHubRoute serves one declared "METHOD /api/v1/…" route while the module is loaded.
	AddHubRoute(pattern string, handler http.HandlerFunc) error
	// AddMCPTool serves one declared assistant tool (Info.MCPTools names it) while the module is loaded.
	// The tool is built with Tool (typed arguments, decoded strictly) or as an MCPTool value (tool.go).
	AddMCPTool(MCPTool) error
	// AddBackgroundJob runs one declared loop under the Service Run context while the module is loaded.
	AddBackgroundJob(name string, run func(context.Context) error) error

	// Narrow core ports a module uses at run time (each may be nil in registration-only tests).
	Tasks() TaskPort
	Todos() TodoPort
	Inbox() InboxPort
	Later() LaterPort
	Schedule() SchedulePort
	Feed() FeedPort
	Delivery() DeliveryPort
	DayRecap() DayRecapPort
	Prompts() PromptPort
}

// BackgroundJob is a module-owned loop. Run inherits Service.Run's cancellation; an off module never starts it.
type BackgroundJob struct {
	Name string
	Run  func(context.Context) error
}

// Setting is a module's value in persona_settings. The core keeps the stored value (also while the module is off,
// when the key is hidden), shows it, and asks the owner to approve a change; the module says what it means.
type Setting struct {
	// Key is the persona_settings key, one of Info.Settings.
	Key string
	// Value is the value in force for what is stored (nil: never set), as persona_settings shows it.
	Value func(stored json.RawMessage) json.RawMessage
	// Check reads a proposed value: the value to store, nil for JSON null (back to the default), or an error that
	// tells the model what is allowed.
	Check func(proposed json.RawMessage) (json.RawMessage, error)
	// Describe is a value in force (Value's) on one line, for the before / after approval card.
	Describe func(value json.RawMessage) string
}

// ScheduleFacts is a module's part in the facts a scheduled routine with facts on opens with (the briefing). The core
// owns the ledgers (to-dos, cards, agents, queues, schedule); the module gathers the briefing snapshot, builds its feed
// item, manages per-run retention and model-failure fallbacks, and decides when stopped work counts as waiting. Without
// it, such a routine opens with no facts.
type ScheduleFacts struct {
	// Setting is the key of the module's Setting the rules read ("" for none).
	Setting string
	// Waits are the wait rules for the setting's stored value (nil: never set).
	Waits func(stored json.RawMessage) WaitRules
	// Runner owns the briefing's fact gathering, feed item, fallback and schedule lifecycle hooks (optional for
	// wait-only test modules).
	Runner ScheduleFactsRunner
}

// WaitRules say when stopped work is waiting and how many rows a direction shows.
type WaitRules interface {
	// CardAfter is how long an unanswered card waits before it is a row.
	CardAfter() time.Duration
	// OrphanAfter is how long a task whose computer is offline waits before it is a row.
	OrphanAfter() time.Duration
	// StalledAfter and SilentAfter are how long a working, or any other, latest session may stay quiet; expectMinutes
	// is the task's own expectation (0: none).
	StalledAfter(expectMinutes int) time.Duration
	SilentAfter(expectMinutes int) time.Duration
	// FoldDirection orders the rows and counts those beyond the direction's display limit.
	FoldDirection(*BriefDirection)
}

// ClientModules are the ids of the feature modules that live only in the clients (web, iOS, Android): the server has
// no module of that id, so no manifest announces their flag. The manager announces module.<id> for each of them beside
// the built-in modules' flags unless the modules setting switches it off, so a client that gates one on the hello
// (docs/architecture/core-and-modules.md "Hub flags") keeps it on by default and the owner can turn it off from the
// hub. An id leaves this list in the PR that gives the module a server half with its own feature
// (internal/modules/all checks that no built-in module is listed).
func ClientModules() []string {
	return []string{"glasses"}
}

// ExtensionModule is a module an approved installed extension may supply (docs/architecture/core-and-modules.md
// "Modules from extensions"): the extension of the same id, installed from the official market and signed by an
// official key, announces module.<ID> while it is enabled, every one of Contributions ("<point>/<id>") is on and the
// modules setting has not switched ID off. Where the built-in module of that id is loaded too, the installed one
// decides the flag.
type ExtensionModule struct {
	ID            ModuleID
	Contributions []string
}

// ModuleID is a module's id (Info.ID's form) where a contract names a module by it.
type ModuleID string

// ExtensionModules is the host-owned list of the modules installed extensions may supply (#16254). Only the host adds
// to it: a package never names the module it claims, and one whose id is not here announces no module flag.
func ExtensionModules() []ExtensionModule {
	return []ExtensionModule{{ID: "volc", Contributions: []string{"transcription/volc", "speech/volc"}}}
}
