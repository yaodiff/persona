package proto

import (
	"net/url"
	"slices"
	"strings"
)

type Launcher struct {
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Model   string `json:"model"`
	// Source: "zshrc" (a function in the machine's ~/.zshrc), "path" (a wrapper script on its PATH, node/pathlaunchers.go), "persona" (managed in Settings › Models, the manager's assistant/models_launchers.go) "claude_desktop" (claude-desktop, a new session in Claude's desktop app, node/claudedesktop_new.go) or "codex_app" (codex-app, a new chat in the Codex App, node/codexapp_new.go). Empty
	// from an older node: zshrc.
	Source string `json:"source,omitempty"`
	// RuntimeFallback is node-local discovery provenance: an ordinary CLI command yields to a connected runtime
	// offering the same launcher. Explicit shell definitions never do. This flag is not part of the wire protocol.
	RuntimeFallback bool `json:"-"`
	// ConfigDir is the launcher's literal CLAUDE_CONFIG_DIR (when known), kept node-local for matching terminal
	// sessions whose ANTHROPIC_BASE_URL is empty or ambiguous. Not part of the wire protocol.
	ConfigDir string `json:"-"`
	// Agent is what it starts when not Claude Code: "codex" (plain codex, or an alias running it; docs/integrations/codex.md
	// "Starting from the phone").
	Agent string `json:"agent,omitempty"`
	// PromptOptional: a Codex launcher this node starts with no first message too (docs/integrations/codex.md
	// "Starting without a first message"). Older nodes leave it out: their Codex starts need the first message.
	PromptOptional bool `json:"prompt_optional,omitempty"`
	// RunHere: a Codex launcher this node starts in a shell pane the user picked (Run Agent Here) and never closes or
	// queues that pane for cleanup when the start fails (#5504; docs/integrations/terminal.md, section 21). Older
	// nodes leave it out: Run Agent Here offers them Claude Code only.
	RunHere bool `json:"run_here,omitempty"`
	// PermissionModes are the permission modes a start on a runtime's launcher takes when its agent is not Claude Code
	// (whose five the apps know), Persona's ids (#8261): default, the agent's own approval, which the apps send as no
	// mode, and bypassPermissions where it has an Allow all (Paseo's Auto Accept and Codex's full-access). A list
	// tells the apps the start takes what the runtime says: the modes listed (a choice only with two or more) and the
	// efforts its models list. Older nodes and runtimes leave it out; the apps then keep their own rule.
	PermissionModes PermissionModeSet `json:"permission_modes,omitempty"`
	// Assistant: a runtime's launcher that may run the Persona assistant (docs/integrations/runtimes.md "Assistant"), whatever its agent.
	Assistant bool `json:"assistant,omitempty"`
	// SupervisionPolicy is an explicitly verified runtime instruction channel, independent of Assistant.
	SupervisionPolicy bool `json:"supervision_policy,omitempty"`
	// External is a sanitized, machine-local adapter advertisement. It grants no execution authority.
	External *ExternalLauncher `json:"external,omitempty"`
	// AssistantFor: on an Assistant launcher, the Agent launcher of this computer it runs the assistant for, as the
	// runtime declares it (the person who set it up says it is the same agent and account): integrations/grok's
	// grok-chat for the Paseo provider grok, which only starts Agents. The main chat's picker points that one at it
	// (docs/integrations/runtimes.md "Assistant"); nothing is ever run on one in place of the other. Older runtimes
	// leave it out. A string, so Launcher stays comparable.
	AssistantFor string `json:"assistant_for,omitempty"`
	// AutoAccept: a runtime's launcher whose agents ask for approval unless a switch is on, which a start in
	// bypassPermissions turns on (Paseo's Auto Accept: its ACP providers such as Grok, and OpenCode;
	// docs/integrations/paseo.md). The Agents Persona itself starts on it run with the switch on, as Claude Code's run in
	// bypassPermissions (#8141). Older nodes and runtimes leave it out.
	AutoAccept bool `json:"auto_accept,omitempty"`
	// DisplayName is what the apps call it: a Persona-managed provider's own name (the manager sets it,
	// launcher_auto_name), or a runtime's name for a provider it runs (Paseo's label, "Grok"; #8261). Empty: Name.
	DisplayName string `json:"display_name,omitempty"`
	// Pi: a Persona-managed provider pi can use on this computer too (ManagedLauncher.PiAPI, docs/integrations/pi.md
	// "Persona's model providers").
	Pi bool `json:"pi,omitempty"`
	// BuiltinCompanion: this runtime launcher is backed by the node's own built-in Antigravity companion (wrapping the
	// local `agy` CLI), not an external runtime. Set only by the node when the verified built-in companion registers.
	BuiltinCompanion bool `json:"builtin_companion,omitempty"`
}

// StartInstead is what a new session's start asked for and does not run with, as an engine runtime that runs only its
// own launchers answers session.start (caps.same_kind, Paseo; docs/integrations/paseo.md "A launcher Paseo does not
// have"): the launcher it has no usable provider of (one of the same kind of agent runs instead: the session's
// launcher), the model that one does not offer (it runs its own default), the effort the model does not offer (the
// session's effort is the one it runs at; none: the model's default). Each empty field ran as asked. The node passes it
// on with the session (session.start's instead); the hub and the manager say it where the start was asked for.
type StartInstead struct {
	Launcher string `json:"launcher,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

// CodeLauncherKindMissing: such an engine has no usable provider of the kind of agent a start's launcher starts, so
// nothing was started and no other kind of agent runs in its place (#8140).
const CodeLauncherKindMissing = "launcher_kind_missing"

// LauncherDetail is what a ~/.zshrc launcher really sets (the node runs it with the agent stubbed out,
// node/launcherprobe.go): every field the page shows; secrets are never in it.
type LauncherDetail struct {
	Agent         string          `json:"agent"` // claude, codex, traex
	BaseURL       string          `json:"base_url,omitempty"`
	Auth          string          `json:"auth,omitempty"` // which variable carries the key (its value is not here)
	Models        []LauncherModel `json:"models,omitempty"`
	ContextWindow int             `json:"context_window,omitempty"`
	CompactWindow int             `json:"compact_window,omitempty"` // when it differs from the window
	Effort        string          `json:"effort,omitempty"`
	TimeoutMS     int             `json:"timeout_ms,omitempty"`
	Args          []string        `json:"args,omitempty"`
	Env           []LauncherEnv   `json:"env,omitempty"` // the other settings, secrets as •••
	Error         string          `json:"error,omitempty"`
}

// LauncherCheck is one "Test" run: a real one-word request through the launcher.
type LauncherCheck struct {
	OK     bool   `json:"ok"`
	Agent  string `json:"agent,omitempty"`
	Model  string `json:"model,omitempty"` // which model answered
	Answer string `json:"answer,omitempty"`
	Millis int    `json:"millis"`
	Error  string `json:"error,omitempty"`
	// AssistantTurn: the check ran as the assistant runs a turn of conversation (launchers.check assistant_turn, a node
	// with launcher_check_assistant_turn, Claude Code launchers only); absent, it was the plain check.
	AssistantTurn bool `json:"assistant_turn,omitempty"`
}

// ClaudeAuthStatus is what a node's claude.auth_status found: `claude auth status --json` run as the launcher runs
// claude (docs/assistant/onboarding.md 4.3). It spends no tokens. Only these fields leave the machine: never the account's
// email or organization, never a key (KeySource names where a key came from, e.g. ANTHROPIC_API_KEY).
type ClaudeAuthStatus struct {
	State       string `json:"state"`                  // ClaudeLoggedIn, ClaudeNotLoggedIn or ClaudeAuthUnknown
	AuthMethod  string `json:"auth_method,omitempty"`  // Claude Code's authMethod: claude.ai, api_key, api_key_helper, oauth_token, third_party, none
	APIProvider string `json:"api_provider,omitempty"` // firstParty, bedrock, vertex…
	KeySource   string `json:"key_source,omitempty"`
	Version     string `json:"version,omitempty"` // Claude Code's, when known
	Reason      string `json:"reason,omitempty"`  // why it is unknown: ClaudeAuthOld, ClaudeAuthNotClaude, ClaudeAuthUnreadable, ClaudeAuthTimeout
}

// ClaudeAuthStatus states and the reasons for an unknown one.
const (
	ClaudeLoggedIn    = "logged_in"
	ClaudeNotLoggedIn = "not_logged_in"
	ClaudeAuthUnknown = "unknown"

	ClaudeAuthOld        = "old_claude" // a Claude Code before `claude auth` (2.1.41)
	ClaudeAuthNotClaude  = "not_claude" // the launcher runs Codex or a runtime
	ClaudeAuthUnreadable = "unreadable" // no status in what it printed (claude not found, a launcher that drops the arguments)
	ClaudeAuthTimeout    = "timeout"
)

// LauncherRepair is what a node did for launchers.repair (docs/server/launcher-health.md): one of its fixed repairs.
type LauncherRepair struct {
	OK          bool   `json:"ok"`
	Unsupported bool   `json:"unsupported,omitempty"` // this repair does not apply to the launcher's agent
	Output      string `json:"output,omitempty"`      // the command's last lines, secrets as •••
}

type LauncherModel struct {
	Role  string `json:"role"` // main model, Opus, small model, subagent… (in Chinese)
	Env   string `json:"env"`
	Model string `json:"model"`
}

// LauncherModels is what a launcher's agent offers in its /model menu, for "New agent"'s model picker
// (node/launcher_models.go). No models: its agent has no list to ask for (a compatible CLI, a runtime's).
type LauncherModels struct {
	Agent  string       `json:"agent"`
	Models []AgentModel `json:"models"`
	// Locked: why a model picked here would not reach the agent (the launcher fixes its own --model); empty: it would.
	Locked string `json:"locked,omitempty"`
	// SupportsStartModel: the node verified a new native omp session can take a model in this host.
	SupportsStartModel bool `json:"supports_start_model,omitempty"`
	// SupportsStartServiceTier means this launcher and host accept a native Codex tier on session.start.
	SupportsStartServiceTier bool `json:"supports_start_service_tier,omitempty"`
}

// ModelPrice is the catalog's reference price in USD per one million tokens. It is display metadata, not a gateway quote.
type ModelPrice struct {
	Input     float64  `json:"input"`
	Output    float64  `json:"output"`
	CacheRead *float64 `json:"cache_read,omitempty"`
	// CacheWrite: a 5-minute cache write; CacheWrite1h a 1-hour one (absent: CacheWrite's price, as cindy prices it).
	CacheWrite   *float64 `json:"cache_write,omitempty"`
	CacheWrite1h *float64 `json:"cache_write_1h,omitempty"`
}

// AgentModel is one model of the list: Claude Code's initialize, Codex's model/list.
type AgentModel struct {
	ID          string `json:"id"` // what --model takes (Claude's aliases too: opus, sonnet…)
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Resolved: the model an alias stands for with this launcher's settings (opus → its model id).
	Resolved string `json:"resolved,omitempty"`
	// ResolvedOffered: the gateway's own catalog also lists Resolved verbatim as a selectable ID.
	// Absent when unknown. The node preserves this evidence on the alias row without adding a picker row.
	ResolvedOffered bool `json:"resolved_offered,omitempty"`
	// Default: what the agent runs when no model is given; picking it is the same as picking none.
	Default bool `json:"default,omitempty"`
	// Efforts: the thinking efforts it takes, weakest first; none: no effort setting.
	Efforts       []string `json:"efforts,omitempty"`
	DefaultEffort string   `json:"default_effort,omitempty"`
	// ServiceTiers: the speed choices this model natively advertises (Codex's serviceTiers), unchanged.
	ServiceTiers []ServiceTier `json:"service_tiers,omitempty"`
	// Custom: an id the user typed that the agent's own list does not have, remembered on the hub for this machine
	// and launcher after it ran (#1644, assistant/models_custom.go); listed after the agent's own, most recent first.
	// Its efforts are not known, so none are given.
	Custom bool `json:"custom,omitempty"`
	// Price is the built-in catalog's exact-entry reference price; nil when unknown or inherited from another model.
	Price *ModelPrice `json:"price,omitempty"`
	// Aliases: the other spellings the manager folded into this row because they run the same model with the same
	// context window on this launcher (Claude Code's tier aliases on a gateway whose tiers name one model, #10365,
	// assistant/models_launchers_fold.go). Each still picks this row: a saved pick of one keeps working.
	Aliases []string `json:"aliases,omitempty"`
}

type LauncherEnv struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// ManagedLauncher is a launcher kept by the hub (Settings › Models): what `personad launch <name>` sets before it runs claude.
// The key only goes to nodes (in memory), never to the app.
type ManagedLauncher struct {
	Kind       string `json:"kind,omitempty"`  // launcher (default) or api
	Agent      string `json:"agent,omitempty"` // claude (default), codex, or agy
	API        string `json:"api,omitempty"`   // messages, responses, or chat for direct API providers
	Name       string `json:"name"`
	BaseURL    string `json:"base_url"`
	Model      string `json:"model"`
	SmallModel string `json:"small_model,omitempty"`
	// SubagentModel is CLAUDE_CODE_SUBAGENT_MODEL; "" keeps the model (the old behaviour).
	SubagentModel string `json:"subagent_model,omitempty"`
	// CustomModel is the gateway's /model extra entry (ANTHROPIC_CUSTOM_MODEL_OPTION) and how it reads.
	CustomModel            string `json:"custom_model,omitempty"`
	CustomModelName        string `json:"custom_model_name,omitempty"`
	CustomModelDescription string `json:"custom_model_description,omitempty"`
	// Args go before the caller's (e.g. --dangerously-skip-permissions).
	Args   []string `json:"args,omitempty"`
	APIKey string   `json:"api_key,omitempty"`
	// Auth is how the key goes to the gateway: "api_key" (x-api-key, ANTHROPIC_API_KEY; the default) or "bearer"
	// (Authorization: Bearer, ANTHROPIC_AUTH_TOKEN), for gateways that take only that.
	Auth string `json:"auth,omitempty"`

	// The provider as cindy models it (#2455): what is known of each model the gateway serves, the model per Claude
	// Code tier, and the gateway's own needs. All optional; an older launcher reads as before.
	//
	// Models: the gateway's models with their context window and thinking levels (the first is not the default; Model
	// is). A model whose window is over 200K runs as "<id>[1m]" (CLI and SDK), its compact window capped to it.
	Models []ProviderModel `json:"models,omitempty"`
	// The model of each Claude Code tier (ANTHROPIC_DEFAULT_*_MODEL); "" is Model.
	OpusModel   string `json:"opus_model,omitempty"`
	SonnetModel string `json:"sonnet_model,omitempty"`
	HaikuModel  string `json:"haiku_model,omitempty"`
	FableModel  string `json:"fable_model,omitempty"`
	// SubagentForce: CLAUDE_CODE_SUBAGENT_MODEL_FORCE=1 (the subagent model wins over an agent's own model:).
	SubagentForce bool `json:"subagent_force,omitempty"`
	// Headers: ANTHROPIC_CUSTOM_HEADERS, one "Name: value" a line. A secret like the key: sealed at rest, never sent
	// back to clients (they see HasHeaders and, in the manager's LauncherView, only the configured names).
	Headers    string `json:"headers,omitempty"`
	HasHeaders bool   `json:"has_headers,omitempty"`
	// Proxy / NoProxy: HTTP_PROXY and HTTPS_PROXY / NO_PROXY for the gateway.
	Proxy   string `json:"proxy,omitempty"`
	NoProxy string `json:"no_proxy,omitempty"`
	// Env: more environment for the launch, one NAME=value a line (#5953), set last: it wins over what Persona sets,
	// the shell's environment and the user's settings env. A secret like the key: sealed at rest, never sent back to
	// clients (they see HasEnv).
	Env    string `json:"env,omitempty"`
	HasEnv bool   `json:"has_env,omitempty"`
	// TimeoutMS: API_TIMEOUT_MS; 0 is 3000000 for a gateway (BaseURL set), Claude Code's own default otherwise.
	TimeoutMS int `json:"timeout_ms,omitempty"`
	// CompactWindow: CLAUDE_CODE_AUTO_COMPACT_WINDOW; 0 is the default model's context window (none when unknown).
	// Assistant conversations follow it like agents do (#3066); the main one and the leader conversations are held to
	// assistant.main_compact_window when that is smaller (#7314, #15318).
	CompactWindow int `json:"compact_window,omitempty"`
	// MaxContext: CLAUDE_CODE_MAX_CONTEXT_TOKENS; 0 is the default model's context window when it is over 200K.
	MaxContext int `json:"max_context,omitempty"`
	// Effort: the default thinking level when the caller gives none (passed as --effort by personad launch, #2307).
	Effort string `json:"effort,omitempty"`
	// Source: where it came from: "" (made by hand), "zshrc:<alias>@<machine id>" (imported), "preset:<id>".
	Source string `json:"source,omitempty"`

	// Device scope and on-demand delivery (docs/integrations/launchers.md "Device scope and on-demand delivery").
	// Delivery: "on_demand" (a computer fetches the secrets the first time it needs them) or "push" (each computer in
	// scope fetches them as soon as it connects); "" is a record from before scopes: push.
	Delivery string `json:"delivery,omitempty"`
	// OwnService: "skip" (not used on a computer whose harness has a usable service of its own) or "use"; "" is use.
	OwnService string `json:"own_service,omitempty"`
	// Withheld (launchers.catalog only): APIKey, Headers and Env are left out; the node asks launchers.pull for them.
	Withheld bool `json:"withheld,omitempty"`
	// Rev changes whenever the provider is saved: a node drops what it pulled of an older one.
	Rev string `json:"rev,omitempty"`
	// Channel is the managed channel this launcher is a read-only alias of (docs/assistant/execution-spec.md): its key,
	// Base URL, headers, proxy and scope are the channel's, shared by every runtime the channel runs. "" is a launcher
	// of its own, as before channels.
	Channel string `json:"channel,omitempty"`
}

// The values of ManagedLauncher.Delivery and OwnService.
const (
	DeliveryOnDemand = "on_demand"
	DeliveryPush     = "push"
	OwnServiceSkip   = "skip"
	OwnServiceUse    = "use"
)

// Pushed: every computer in scope fetches the provider's secrets as soon as it connects.
func (l ManagedLauncher) Pushed() bool { return l.Delivery == "" || l.Delivery == DeliveryPush }

// SkipsOwnService: the provider is not used on a computer whose harness already has a usable service of its own.
func (l ManagedLauncher) SkipsOwnService() bool { return l.OwnService == OwnServiceSkip }

// Public is the provider without its secrets, as launchers.catalog names it to a node that pulls them.
func (l ManagedLauncher) Public() ManagedLauncher {
	l.APIKey, l.Headers, l.Env, l.HasHeaders, l.HasEnv, l.Withheld = "", "", "", false, false, true
	return l
}

// OwnServiceState is what a node found of a harness's own service (launchers.own_service, launchers.pull): Known
// false when it could not tell (no detection for that harness, a timeout). Via names how, never a secret: "login",
// "api_key", "launcher".
type OwnServiceState struct {
	Agent string `json:"agent"`
	Known bool   `json:"known"`
	Own   bool   `json:"own"`
	Via   string `json:"via,omitempty"`
}

// Codes of a refused launchers.pull and of a start the manager refuses for a provider's scope.
const (
	CodeLauncherOutOfScope = "launcher_out_of_scope"
	CodeLauncherOwnService = "launcher_own_service"
	// CodeLauncherKeyUnresolved: the key that computer uses is a reference its provider extension did not resolve
	// (off, signed out, not found…); the message is the provider's reason (docs/extensions/secret-providers.md).
	CodeLauncherKeyUnresolved = "launcher_key_unresolved"
)

// ProviderModel is one model a provider's gateway serves (cindy's ProviderRuntimeModelConfig, the Anthropic part).
type ProviderModel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	// ContextWindow in tokens; 0 is unknown (Claude Code then assumes 200K).
	ContextWindow int `json:"context_window,omitempty"`
	// MaxOutputTokens: the longest reply the gateway says the model writes; 0 is unknown.
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// Efforts: the thinking levels it takes (low, medium, high, xhigh, max); empty is unknown (all are offered),
	// unless NoEffort says it takes none.
	Efforts []string `json:"efforts,omitempty"`
	// AllEfforts: every level the gateway reported, present only when some fall outside EffortLevels: in discovery's
	// answer the gateway's own words (EffortWords), once saved those the provider's agent or API takes (Codex's
	// minimal and ultra, a direct API's none). Efforts is always these as Claude Code takes them
	// (ProjectEfforts(AllEfforts, EffortLevels)), so a reader that knows only Efforts never gets a level it can't use.
	AllEfforts    []string `json:"all_efforts,omitempty"`
	DefaultEffort string   `json:"default_effort,omitempty"`
	// NoEffort: the gateway said the model takes no thinking level (Efforts is then empty, which alone is unknown).
	NoEffort bool `json:"no_effort,omitempty"`
	// ImageInput and ToolCalls: whether the gateway says the model takes images and tools; nil is unknown.
	ImageInput *bool `json:"image_input,omitempty"`
	ToolCalls  *bool `json:"tool_calls,omitempty"`
	// InheritedFrom is the catalog id an unlisted newer generation took a missing context window or thinking
	// level from. Empty for an exact catalog entry, and when the gateway already reported every one of those.
	InheritedFrom string `json:"inherited_from,omitempty"`
	// Price is the exact catalog entry's reference price. It is never inherited from an earlier generation.
	Price *ModelPrice `json:"price,omitempty"`
}

// BearerAuth: the launcher's key goes as Authorization: Bearer (ANTHROPIC_AUTH_TOKEN) rather than x-api-key.
func (l ManagedLauncher) BearerAuth() bool { return l.Auth == "bearer" }

// AgentType is the agent CLI this managed launcher runs ("claude" by default, or "codex" / "agy").
func (l ManagedLauncher) AgentType() string {
	if l.Agent != "" {
		return l.Agent
	}
	return "claude"
}

// CustomHeadersSupported reports whether this managed runtime has a real launch-time channel for every configured
// header. A probe may send a header over HTTP even when the runtime cannot; those combinations must be refused instead
// of presenting the probe as proof of a working launch.
func (l ManagedLauncher) CustomHeadersSupported() bool {
	switch l.AgentType() {
	case "claude", "codex":
		return true
	case "omp":
		// omp documents ANTHROPIC_CUSTOM_HEADERS for its Anthropic provider behind a non-Anthropic base URL.
		return (l.API == "" || l.API == "messages") && !AnthropicAPI(l.LaunchBaseURL())
	default:
		return false
	}
}

// PiAPI is the pi API (pi's registerProvider "api") a provider speaks when pi can use it too (docs/integrations/pi.md
// "Persona's model providers"): "anthropic-messages" for a Messages provider that runs Claude Code (or Oh My Pi, which
// takes the same gateway), "openai-responses" for a Codex one; "" for the rest (Antigravity, a direct API connection
// that speaks Responses or Chat Completions, which the node is never sent), and for a provider with a proxy of its
// own: pi sends straight to the gateway, and one reachable only through that proxy would fail every turn.
func (l ManagedLauncher) PiAPI() string {
	if l.Proxy != "" {
		return ""
	}
	if l.Kind == "api" {
		if l.API == "" || l.API == "messages" {
			return "anthropic-messages"
		}
		return ""
	}
	switch l.AgentType() {
	case "claude", "omp":
		return "anthropic-messages"
	case "codex":
		return "openai-responses"
	}
	return ""
}

// MessagesBase: the provider's Base URL reaches its agent as ANTHROPIC_BASE_URL (node managedEnv), which gets
// /v1/messages added to it: a Messages provider whose agent is not Codex or agy (they take OPENAI_BASE_URL /
// GOOGLE_GEMINI_BASE_URL). Responses and Chat Completions bases keep their /v1.
func (l ManagedLauncher) MessagesBase() bool {
	if a := l.AgentType(); a == "codex" || a == "agy" {
		return false
	}
	return l.API == "" || l.API == "messages"
}

// LaunchBaseURL is the Base URL as the provider's agent or direct API takes it: a Messages base through
// MessagesBaseURL, a Chat Completions or Responses base through APIBaseURL, and any other as stored. The hub stores
// and delivers it, and the node launches with it, so a provider saved before the hub dropped a trailing /v1 or pasted
// endpoint path still reaches what Test connection called.
func (l ManagedLauncher) LaunchBaseURL() string {
	if l.MessagesBase() {
		return MessagesBaseURL(l.BaseURL)
	}
	if l.AgentType() == "codex" {
		return APIBaseURL(l.BaseURL, "responses")
	}
	if l.API == "chat" || l.API == "responses" {
		return APIBaseURL(l.BaseURL, l.API)
	}
	return l.BaseURL
}

// AnthropicAPIHost is the host of Anthropic's own API: where Claude Code goes with no Base URL.
const AnthropicAPIHost = "api.anthropic.com"

// AnthropicAPI: base (a Base URL) is Anthropic's own API, not a relay, a gateway or another vendor. The probe's
// error reading (the manager's models_probe.go) and the node's provider environment (launcherenv.go) both ask.
func AnthropicAPI(base string) bool {
	u, err := url.Parse(strings.TrimSpace(base))
	return err == nil && strings.EqualFold(u.Hostname(), AnthropicAPIHost)
}

// APIBaseURL normalizes a gateway Base URL for api ("messages"/empty, "responses", or "chat"), stripping a pasted
// trailing endpoint path while keeping /v1 on OpenAI-compatible bases ("https://host/v1/v1/chat/completions" →
// "https://host/v1/v1") and preserving bare /responses deployment prefixes ("https://gw/responses" stays unchanged;
// "https://gw/v1/responses" → "https://gw/v1").
func APIBaseURL(base, api string) string {
	switch api {
	case "", "messages":
		return MessagesBaseURL(base)
	case "chat":
		return trimSinglePathSuffix(base, "/chat/completions")
	case "responses":
		return trimVersionedEndpointSuffix(base, "/responses")
	default:
		return strings.TrimRight(strings.TrimSpace(base), "/")
	}
}

// MessagesBaseURL is a gateway address as ANTHROPIC_BASE_URL takes it. Unlike APIBaseURL for "chat" and "responses",
// MessagesBaseURL produces an ANTHROPIC_BASE_URL without a trailing /v1: Claude Code (node/launcherenv.go) and
// connection probes (assistant/models_probe.go) unconditionally append /v1/messages themselves, and saving plus
// launch-time re-application require strict idempotence (MessagesBaseURL(got) == got; a run of /v1 goes whole).
//
// Normalization runs in two ordered phases: first strip a pasted /v1/messages endpoint suffix (only /messages
// preceded by /v1, never a /messages deployment prefix), then strip any trailing /v1 segments so
// "https://gw/prefix/v1/" → "https://gw/prefix" and both "https://gw/messages/v1" and
// "https://gw/messages/v1/messages" → "https://gw/messages". Anything else (no host, a query) is only trimmed.
func MessagesBaseURL(base string) string {
	return trimBasePathSuffix(trimVersionedEndpointSuffix(base, "/messages"), "/v1")
}

func splitBaseURLPath(base string) (root, path string, ok bool) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return base, "", false
	}
	authority := strings.Index(base, "//") + len("//")
	slash := strings.IndexByte(base[authority:], '/')
	if slash < 0 {
		return base, "", false // no path
	}
	return base[:authority+slash], base[authority+slash:], true
}

// trimVersionedEndpointSuffix drops a single pasted /v1<suffix> endpoint (e.g. /v1/messages or /v1/responses) while
// keeping the preceding /v1 and leaving bare deployment prefixes ("https://gw/messages", "https://gw/responses") intact.
func trimVersionedEndpointSuffix(base, suffix string) string {
	root, path, ok := splitBaseURLPath(base)
	if !ok {
		return root
	}
	rest, found := strings.CutSuffix(path, suffix)
	if !found {
		return root + path
	}
	rest = strings.TrimRight(rest, "/")
	if !strings.HasSuffix(rest, "/v1") {
		return root + path
	}
	return root + rest
}

func trimSinglePathSuffix(base, suffix string) string {
	root, path, ok := splitBaseURLPath(base)
	if !ok {
		return root
	}
	if rest, found := strings.CutSuffix(path, suffix); found {
		return root + strings.TrimRight(rest, "/")
	}
	return root + path
}

func trimBasePathSuffix(base, suffix string) string {
	root, path, ok := splitBaseURLPath(base)
	if !ok {
		return root
	}
	for {
		rest, found := strings.CutSuffix(path, suffix)
		if !found {
			return root + path
		}
		path = strings.TrimRight(rest, "/")
	}
}

// The agents the node starts without Claude Code's permission mode and effort (Codex takes its own sandbox and
// effort): the hub matches what the node does with one a start names (assistant/launcher_modes.go), and node
// TestStartModeAgentTypesMatchTheNode keeps these lists and the node's together.
//
// WorkAgentTypes are the work agents (docs/integrations/launchers.md "Work agents"): pi, the Cursor CLI and the
// Antigravity CLI. The node starts them with the two ignored rather than refusing the start (node/control.go, #8151).
var WorkAgentTypes = []string{"pi", "cursor", "agy"}

// NoModeAgentTypes are the other terminal agents whose starts refuse Persona permission modes, TRAE CLI and Oh My Pi.
var NoModeAgentTypes = []string{"traex", "omp"}

// IsWorkAgentType: agent is one of WorkAgentTypes.
func IsWorkAgentType(agent string) bool { return slices.Contains(WorkAgentTypes, agent) }

// IsNoModeAgentType: agent is one of NoModeAgentTypes.
func IsNoModeAgentType(agent string) bool { return slices.Contains(NoModeAgentTypes, agent) }
