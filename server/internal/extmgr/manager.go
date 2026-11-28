// Package extmgr is the manager's extensions, connectors and bots (docs/extensions/extensions.md "Who owns what"), moved here from
// the hub as they were: the extension registry and its tables (plugins, plugin_secrets, plugin_audit, the markets),
// installing, upgrading, rolling back and removing, the markets and auto-upgrade, the programs (extension/host, their
// sandbox and egress proxy, metrics, logs, dev taps), their settings and secrets (only this package opens them), the
// extensions' tokens and their scopes, every contribution's consumer (UI, themes, widgets, shortcuts, the personal
// agents' sources, agent filters, model providers and quota, cua / ego / imessage, programs on computers, the
// assistant's skills, the knowledge base's and the channels' parts); the connector framework with every source
// (connector_secrets, the inbox's rounds, mail, GitHub, iMessage, the lark-cli path, the sign-ins); the Feishu bot's
// long connection and the WeChat bot, their pairing and credentials. It runs in `personad assistant` beside the
// assistant (its inbox, knowledge base and channels take their sources in-process) and the devices (the computers);
// the hub relays the apps' requests on extwire.Routes to it and keeps the last of what it reports. It does not import
// the hub, nor the hub it.
package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/cliport"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/agents"
	"github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/moduleswitch"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/remoteimage"
	"github.com/nwverse/persona/server/internal/secretkey"
	"github.com/nwverse/persona/server/internal/subscriptions"
	"github.com/nwverse/persona/server/internal/update"
)

// Host is the bus connection to the hub (the manager's).
type Host interface {
	Call(ctx context.Context, method string, params, out any) error
	Notify(event string, params any) error
}

// Devices is the manager's own connections to the computers (internal/devices.Devices).
type Devices interface {
	Has(id string) bool
	CallNode(ctx context.Context, id, method string, params json.RawMessage) (json.RawMessage, error)
}

// Options are how the manager reaches what it needs.
type Options struct {
	// Modules is the run-time module switch the manager started with (the `modules` settings row, read before New,
	// assistant.ReadModuleSwitch): a module it switches off gets nothing from its provider port, exactly as in a build
	// without it (moduleswitch). The zero value has every module on.
	Modules moduleswitch.Set
	// Dir is the data directory (~/.persona): hub.json, extensions/ in it, and secret.key unless SecretKey says.
	Dir string
	// SecretKey is the manager's own key file (the built-in manager's in its directory, managerconf.OwnFile); "":
	// Dir's secret.key.
	SecretKey string
	// ConfigFile is the manager's manager.json, with its sections of the configuration (managerconf); "": hub.json's.
	ConfigFile string
	Config     *Config // nil: read from Dir and ConfigFile
	Store      Store   // nil: nothing is kept (tests)
	Logf       func(format string, args ...any)
	Build      update.Build
	Hub        Host
	Devices    Devices
	// ManagerID is the manager's id ("" is the built-in one's, the assistant).
	ManagerID string
	// HubURL and HubClient are how the manager reaches the hub (its manager.json's hub, and the client that pins the
	// hub's key), and HubProofKey its proof's key (extwire.ProofKey of its bus token, never the token): a relayed
	// manager's extensions call the hub's API through it (hub_relay.go). The built-in manager's reach the hub on this
	// machine, as hub.json says (Config.Listen).
	HubURL      string
	HubClient   *http.Client
	HubProofKey string
	// Assistant asks the assistant in this process what the hub used to ask it over the bus (same methods and
	// params: channels.receive, feishu.message, inbox.collected, kb.*…); nil: none here (tests).
	Assistant func(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error)
	// AssistantEvent tells the assistant an event the hub used to send it (feishu.owner).
	AssistantEvent func(event string, params json.RawMessage)
	// AssistantSocket serves the assistant's WebSocket routes (assistantwire.SocketRoutes: a meeting's audio socket),
	// which the hub relays here over a relay stream like the extensions' routes; nil: they are 404 (tests).
	AssistantSocket http.HandlerFunc
	// TranscriptionChanged invalidates the assistant's availability cache after an extension registry change.
	// It must not block or call back into the registry; nil means no observer.
	TranscriptionChanged func()
	// TracksChanged invalidates the headless track registry after extension contributions change.
	TracksChanged func()
	// DialRelay opens the hub's relay channel with query.
	DialRelay func(ctx context.Context, query string) (*websocket.Conn, error)
	// Connected says whether the bus is up now (nil: always), and Connection which connection it is: a comparable
	// value, another one for each connection whose handshake went through (nil: none known; hub_relay.go).
	Connected  func() bool
	Connection func() any
	// Ask shows a card on the phones and waits for its answer, in the assistant's interaction queue (its askHub);
	// ctx ending withdraws the card. nil: the hub is asked (tests).
	Ask func(ctx context.Context, ix proto.Interaction) (proto.Resolution, error)
	// AskPrepared retains an assistant-owned mutation through clarification. It is separate from Ask so runtime-owned
	// coding-Agent permission cards cannot accidentally acquire a second execution path.
	AskPrepared func(context.Context, proto.Interaction, approval.Operation) (approval.Outcome, error)
	// AssistantMachine is where the assistant's conversations run ("" while not known).
	AssistantMachine func() string
	// AssistantTools are the assistant's tools now (a connector's tool may not shadow one).
	AssistantTools func() []proto.Tool
	// LauncherReferences are the model providers' keys that hold references, for their providers' details (nil: none).
	LauncherReferences func(ctx context.Context) []LauncherReference
	// Lang and Timezone are the user's language and zone ("" when not known).
	Lang     func() i18n.Lang
	Timezone func() string
	// Pages draws the assistant's pages (Dashboards, #15017) under the reserved extension id PagesExtension
	// (pages_hook.go); nil: there are none (only the default manager has them).
	Pages Pages
}

// Manager is the extensions, connectors and bots as the manager runs them.
type Manager struct {
	// handoffRulesMu: one change of the hand-off rules at a time (secret_handoff_rules.go).
	handoffRulesMu sync.Mutex
	// inboxReads are each source's latest read by the Inbox, for the Inbox Sources pages (inbox_sources.go).
	inboxReads sourceReads
	// errReports: the error reports' pipeline (error_reports.go).
	errReports errorReports
	// browserBackends: pluggable BrowserBackend registry for extension browser.machines / browser.run (#12866).
	browserBackends browserRegistry
	// iphoneTurns: one iphone_do at a time per iPhone, across conversations (iphone.go, iphoneTurn).
	iphoneTurns sync.Map
	// serving: the requests the hub relayed to the extensions being served now, a WebSocket (a voice call, an
	// extension's live view) for as long as it is open (Serving).
	serving atomic.Int64

	opts    Options
	cfg     *Config
	version string
	build   update.Build
	logger  *log.Logger
	store   Store
	secret  *secretkey.Box

	ctx context.Context

	mail               Mail
	connectors         *connector.Registry
	larkCLIRefreshMu   sync.Mutex
	larkCLIRecovery    larkCLIRecovery
	imessageSettingsMu imessageContextLock
	imessageArchive    imessageArchiveFence
	imessageAIPending  atomic.Bool
	subs               *subscriptions.Service // source subscriptions (subscriptions.go); nil until Run
	// subsLive is subs once it runs, for the connectors' events, which start before it (connectorChanged).
	subsLive      atomic.Pointer[subscriptions.Service]
	connectorsOff map[string]bool
	migrated      sync.Map
	media         mediaCache
	avatars       *remoteimage.Fetcher // the avatars route's (avatars.go)
	recalls       recallLocks
	extensions    *extensions
	hostCLIMu     sync.Mutex
	hostCLIState  *hostCLIState
	extTokens     extTokens
	legacyCalls   legacyCalls
	uiPages       pageSessions
	uiDelivery    pageDelivery
	agents        *agents.Service
	// orcaBots are the Orca bots found on the computers (orcabots.go, #16891), each an Agents → Bots source.
	orcaBots orcaBotDiscovery
	bots     botHub
	channels channelsHub
	chans    struct{ in inbound }
	// clarificationProofs are one-time owner-question attestations issued after
	// a fresh channel action, never reusable approval capabilities.
	clarificationProofs clarificationProofBook
	inbox               inboxSources
	larkDefault         larkDefault
	admin               struct {
		extensions extensionGate
		now        func() time.Time // the admin UI's clock (tests move it)
	}

	// kbPostsMu drains admitted connector posts before disabling ingestion and deleting saved content.
	kbPostsMu sync.RWMutex

	// apiMux are the manager's routes (Handler).
	apiMux *http.ServeMux

	// machines, byID: the computers (machines.go); fleetMu guards them.
	fleetMu     sync.Mutex
	machines    []*Machine
	byID        map[string]*Machine
	isMemberNow atomic.Bool
	hello       atomic.Pointer[assistantwire.Hello]

	// cuaSessions is what is known of the Cua Driver session labels the computer tools call under (computer_session.go).
	cuaSessions cuaSessionBook

	started chan struct{} // closed once Run started everything

	reportMu            sync.Mutex
	reportSent          []byte
	reportBotIdentities []extwire.BotIdentity
	// reservedLogged are the "<extension> <feature>" pairs whose reserved module feature was dropped and logged once
	// (extension_api.go).
	reservedLogged sync.Map

	// ruleStore keeps the user's per-chat rules (chat_rules.go): the manager's setting connector_chat_rules, made in New.
	// It is set before Run, which reads it as the gate starts (startConnectorGate): assigned later it would race with that
	// read. Tests put another in its place.
	ruleStore userRuleStore
	userRules userRules

	// images are the pictures extensions serve through the hub (extension_images.go).
	images imageProxy

	// droppedWait is noticeDropped's first wait between two tries, doubled after each.
	droppedWait time.Duration
}

// New is the manager of opts; nothing runs until Run.
func New(opts Options) *Manager {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	cfg := opts.Config
	if cfg == nil {
		var err error
		if cfg, err = loadConfig(opts.Dir, opts.ConfigFile); err != nil {
			opts.Logf("extensions: configuration: %v", err)
			cfg = &Config{}
		}
	}
	for _, w := range cfg.warnings {
		opts.Logf("extensions: %s", w)
	}
	if cfg.dir == "" {
		cfg.dir = opts.Dir
	}
	if cfg.Lark.CLI != nil && !cliport.Available(opts.Modules) { // built in but switched off: as a build without it
		opts.Logf("extensions: lark.cli is not read: the lark module is switched off in the modules setting")
		c := *cfg
		c.Lark.CLI = nil
		cfg = &c
	}
	h := &Manager{opts: opts, cfg: cfg, version: opts.Build.Version, build: opts.Build, store: opts.Store,
		logger: log.New(logWriter(opts.Logf), "", 0)}
	h.secret = secretkey.NewWithFallback(h.secretKeyPath()) // and the old keys while a rotation keeps them
	h.ruleStore = &settingRuleStore{h: h}
	h.byID = map[string]*Machine{}
	h.started = make(chan struct{})
	h.extTokens.dropped = h.tellHubDropped
	h.droppedWait = time.Second
	h.avatars = newAvatars(cfg.dir, h.avatarDoH, opts.Logf)
	return h
}

// logWriter is a log.Logger's writer onto logf.
type logWriter func(string, ...any)

func (l logWriter) Write(p []byte) (int, error) {
	s := string(p)
	if n := len(s); n > 0 && s[n-1] == '\n' {
		s = s[:n-1]
	}
	l("%s", s)
	return len(p), nil
}

func (h *Manager) logf(format string, args ...any) { h.opts.Logf(format, args...) }

// ---- what the code moved from the hub asks of the hub's parts, here ----

// userLang is the user's language (the hub's hello says it).
func (h *Manager) userLang() i18n.Lang {
	if h.opts.Lang != nil {
		if l := h.opts.Lang(); i18n.Valid(l) {
			return l
		}
	}
	return i18n.Default
}

// requestLang is the language of the answer to r: the hub's, passed on with it.
func (h *Manager) requestLang(r *http.Request) i18n.Lang {
	if l := i18n.From(r.Context()); l != "" {
		return l
	}
	if l := principalOf(r.Context()).Lang; i18n.Valid(l) {
		return l
	}
	return i18n.FromRequest(r).Or(h.userLang())
}

// ownerName is whose Persona this is (manager.json assistant.owner, the hub's hello).
func (h *Manager) ownerName() string {
	if hl := h.hello.Load(); hl != nil {
		return hl.Owner
	}
	return ""
}

// isMember: this hub is a member of a mesh group (its center runs what runs once).
func (h *Manager) isMember() bool { return h.isMemberNow.Load() }

func (h *Manager) sealKey(plain string) (string, error) { return h.secret.Seal(plain) }

func (h *Manager) openKey(sealed string) (string, error) { return h.secret.Open(sealed) }

// errBusOffline: the hub is not connected.
var errBusOffline = errors.New("not connected to the hub")

// callHub is app.call on the hub's app AppHub.
func (h *Manager) callHub(ctx context.Context, method string, params, out any) error {
	if h.opts.Hub == nil {
		return errBusOffline
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	var res json.RawMessage
	if err := h.opts.Hub.Call(ctx, assistantwire.MethodApp, assistantwire.AppCall{AppID: extwire.AppHub, Method: method, Params: raw}, &res); err != nil {
		return err
	}
	if out == nil || len(res) == 0 {
		return nil
	}
	return json.Unmarshal(res, out)
}

// broadcast sends message to every app (ws.broadcast).
func (h *Manager) broadcast(message any) {
	if h.opts.Hub == nil {
		return
	}
	raw, err := json.Marshal(message)
	if err != nil {
		return
	}
	if err := h.opts.Hub.Notify(assistantwire.EventBroadcast, assistantwire.Broadcast{Message: raw}); err != nil && !errors.Is(err, context.Canceled) {
		h.logf("broadcast: %v", err)
	}
}

// approveOutcome asks the user on the phone to allow a sensitive call (the hub's card), and says how the card ended
// (proto.Resolution.Outcome): a refusal is the user's own decline only when it is proto.OutcomeDeclined; otherwise
// nobody answered it (notApproved).
func (h *Manager) approveOutcome(ctx context.Context, by asker, tool, summary string, input json.RawMessage) (bool, string) {
	r, _ := h.approveCard(ctx, by, tool, summary, input)
	return r.Allow != nil && *r.Allow, r.Outcome()
}

// declinedText is what a call the user declined on the phone answers.
const declinedText = "The user declined this on the phone. Do not retry it unless they ask."

// notApproved is what a call whose card did not let it go answers (outcome: how the card ended): the user's decline
// only when they declined it themselves on a device; otherwise nobody answered it (it expired, was withdrawn or
// cleared, someone else answered it), and the model is told the user did not decline it.
func notApproved(outcome string) proto.ToolResult {
	if outcome == proto.OutcomeDeclined {
		return proto.TextResult(declinedText, true)
	}
	return proto.TextResult(prompts.RenderOutboundUnanswered(prompts.OutboundUnanswered{Outcome: outcome, Nothing: "nothing was done"}), true)
}

// refusalWord is how the log says a card ended that did not let a call go: "declined" only for the user's own decline.
func refusalWord(outcome string) string {
	if outcome == proto.OutcomeDeclined {
		return "declined on the phone"
	}
	return "not answered on the phone (" + outcome + ")"
}

// approveCard is approveOutcome for a caller that keeps a record of the answer: it gives the answer itself and the id of the
// card that asked.
func (h *Manager) approveCard(ctx context.Context, by asker, tool, summary string, input json.RawMessage) (proto.Resolution, string) {
	ix := proto.Interaction{
		ID: "ix_" + randomHex(6), MachineID: by.machine, SessionID: by.session, Kind: proto.IxPermission, ToolName: tool, Summary: summary,
		ToolInput: input, SessionTitle: "Persona", Cwd: h.askerCwd(by), CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(15 * time.Minute).UTC(),
	}
	r, _ := h.waitInteraction(ctx, ix)
	return r, ix.ID
}

// waitInteraction shows ix on the phones and waits for its answer (the assistant's interaction queue, in this
// process; the caller going away withdraws it).
func (h *Manager) waitInteraction(ctx context.Context, ix proto.Interaction) (proto.Resolution, error) {
	if h.opts.Ask == nil {
		return proto.Resolution{}, errAssistantOffline
	}
	r, err := h.opts.Ask(ctx, ix)
	if r.Allow != nil && *r.Allow && !r.UserAllowed() && !proto.MaintenanceAllowed(ix, r, time.Now()) {
		r.Allow = nil
	}
	if proto.InternalMaintenanceApproval(ix) && r.Remembered {
		r.Allow = nil // forced reruns require a fresh decision, not a remembered grant
	}
	if err != nil || ctx.Err() != nil || !time.Now().Before(ix.ExpiresAt) || r.Late || r.Cancel || r.Dismiss {
		r.Allow = nil
	}
	return r, err
}

func (h *Manager) waitPrepared(ctx context.Context, ix proto.Interaction, op approval.Operation) (approval.Outcome, error) {
	if proto.InternalMaintenanceApproval(ix) {
		if op.Apply == nil {
			return approval.Outcome{}, fmt.Errorf("%w: maintenance operation has no apply callback", approval.ErrNotApplied)
		}
		r, err := h.waitInteraction(ctx, ix)
		if err != nil {
			return approval.Outcome{}, fmt.Errorf("%w: %w", approval.ErrNotApplied, err)
		}
		if ctx.Err() == nil && time.Now().Before(ix.ExpiresAt) && (r.UserAllowed() || proto.MaintenanceAllowed(ix, r, time.Now())) {
			text, err := op.Apply(ctx, r)
			return approval.Outcome{Text: text}, err
		}
		return approval.Outcome{Text: op.Unanswered}, nil
	}
	if h.opts.AskPrepared != nil {
		return h.opts.AskPrepared(ctx, ix, op)
	}
	if h.opts.Ask == nil {
		return approval.Outcome{}, fmt.Errorf("%w: %w", approval.ErrNotApplied, errAssistantOffline)
	}
	// Embedders that predate AskPrepared keep their original one-shot approval
	// behavior. The production assistant supplies AskPrepared for clarification.
	r, err := h.opts.Ask(ctx, ix)
	if err != nil {
		// The legacy wait never entered Apply. Keep the original error while making
		// that definite outcome distinguishable from an uncertain Apply failure.
		return approval.Outcome{}, fmt.Errorf("%w: %w", approval.ErrNotApplied, err)
	}
	switch {
	case r.UserAllowed():
		text, err := op.Apply(ctx, r)
		return approval.Outcome{Text: text}, err
	case r.UserDenied():
		text := op.Denied
		if text == "" {
			text = "The user declined the change: nothing changed."
		}
		return approval.Outcome{Text: text}, nil
	default:
		return approval.Outcome{Text: op.Unanswered}, nil
	}
}

// callAssistant asks the assistant in this process (Options.Assistant).
func (h *Manager) callAssistant(ctx context.Context, method string, params, out any) error {
	if h.opts.Assistant == nil {
		return errAssistantOffline
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	res, err := h.opts.Assistant(ctx, method, raw)
	if err != nil {
		return err
	}
	if out == nil || len(res) == 0 {
		return nil
	}
	return json.Unmarshal(res, out)
}

// notifyAssistant tells the assistant in this process an event.
func (h *Manager) notifyAssistant(event string, params any) error {
	if h.opts.AssistantEvent == nil {
		return errAssistantOffline
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	h.opts.AssistantEvent(event, raw)
	return nil
}

// assistantUp: the assistant is here to take calls.
func (h *Manager) assistantUp() bool { return h.opts.Assistant != nil }

// assistantEvents are what this code takes of the assistant's events to the hub (inbox.changed, kb.changed…): they
// come here in-process instead (AssistantSaid).
var assistantEvents = map[string]func(*Manager, json.RawMessage){}

func onAssistantEvent(event string, f func(*Manager, json.RawMessage)) { assistantEvents[event] = f }

// AssistantSaid takes one of the assistant's events that is this code's (handled false: the hub's).
func (h *Manager) AssistantSaid(event string, params json.RawMessage) bool {
	f, ok := assistantEvents[event]
	if ok {
		f(h, params)
	}
	return ok
}

// errAssistantOffline is the hub's "the assistant can't be reached right now".
var errAssistantOffline = i18n.Err("hub-assistant_link.offline")

func (h *Manager) String() string { return fmt.Sprintf("extmgr(%s)", h.cfg.dir) }

// secretKeyFile is the key file's name in the data directory (internal/secretkey) when Options.SecretKey is "".
const secretKeyFile = "secret.key"

// secretKeyPath is the key file the manager seals its secrets with.
func (h *Manager) secretKeyPath() string {
	if h.opts.SecretKey != "" {
		return h.opts.SecretKey
	}
	return filepath.Join(h.cfg.dir, secretKeyFile)
}

// modules is the run-time module switch the manager started with (Options.Modules); a nil manager (a test's) has every
// module on.
func (h *Manager) modules() moduleswitch.Set {
	if h == nil {
		return moduleswitch.Set{}
	}
	return h.opts.Modules
}

// modules is the manager's switch the extensions were made with, every module on without one.
func (ps *extensions) modules() moduleswitch.Set {
	if ps == nil {
		return moduleswitch.Set{}
	}
	return ps.moduleSwitch
}
