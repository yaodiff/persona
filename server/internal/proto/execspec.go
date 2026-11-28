package proto

import (
	"slices"
	"strings"
	"time"
)

// Four-dimension execution (docs/assistant/execution-spec.md): what runs a conversation or an Agent is four
// independent choices, the engine (Runtime), the connection to the upstream (Channel), the canonical model (Model) and
// the thinking effort (Effort). A launcher is a read-only compatibility alias of one (runtime, channel) pair with a
// default model and effort, so every client and `personad launch <name>` that knows only launchers keeps working.
// Nothing here names a channel, a gateway or a model list: those come from what runtimes, channels and computers
// declare.

// ExecutionSpec is the four dimensions. Every field may be empty: it inherits from the layer below (Inherit).
type ExecutionSpec struct {
	// Runtime is the engine that drives the Agent loop on a computer ("claude", "agy", "codex", a runtime plugin's).
	Runtime string `json:"runtime,omitempty"`
	// Channel is the connection the engine reaches its model through: a native sign-in ("native:<runtime>"), a
	// Persona-managed gateway or API (its name), or a computer's own shell launcher ("shell:<name>").
	Channel string `json:"channel,omitempty"`
	// Model is the canonical model id, never with an effort folded into it.
	Model string `json:"model,omitempty"`
	// Effort is the platform's thinking level ("auto", "low" … "max"); the channel's adapter maps it onto the wire.
	Effort string `json:"effort,omitempty"`
}

// Trimmed is s with the spaces around each field removed.
func (s ExecutionSpec) Trimmed() ExecutionSpec {
	return ExecutionSpec{Runtime: strings.TrimSpace(s.Runtime), Channel: strings.TrimSpace(s.Channel),
		Model: strings.TrimSpace(s.Model), Effort: strings.TrimSpace(s.Effort)}
}

// IsZero reports whether s sets no dimension.
func (s ExecutionSpec) IsZero() bool { return s == (ExecutionSpec{}) }

// Inherit fills every dimension s leaves empty from base: the lower layer's value wins only where s says nothing.
func (s ExecutionSpec) Inherit(base ExecutionSpec) ExecutionSpec {
	if s.Runtime == "" {
		s.Runtime = base.Runtime
	}
	if s.Channel == "" {
		s.Channel = base.Channel
	}
	if s.Model == "" {
		s.Model = base.Model
	}
	if s.Effort == "" {
		s.Effort = base.Effort
	}
	return s
}

// nativeChannelPrefix starts the name of a runtime's own sign-in channel.
const nativeChannelPrefix = "native:"

// shellChannelPrefix starts the name of a computer's own shell launcher channel.
const shellChannelPrefix = "shell:"

// NativeChannel is the name of runtime's own sign-in channel.
func NativeChannel(runtime string) string { return nativeChannelPrefix + runtime }

// NativeChannelRuntime is the runtime whose own sign-in channel name is; false for any other channel.
func NativeChannelRuntime(name string) (string, bool) {
	r, ok := strings.CutPrefix(name, nativeChannelPrefix)
	return r, ok && r != ""
}

// ShellChannel is the name of the channel of a computer's own shell launcher called launcher.
func ShellChannel(launcher string) string { return shellChannelPrefix + launcher }

// ShellChannelLauncher is the shell launcher a shell channel name stands for; false for any other channel.
func ShellChannelLauncher(name string) (string, bool) {
	l, ok := strings.CutPrefix(name, shellChannelPrefix)
	return l, ok && l != ""
}

// ChannelKind is how a channel reaches its upstream.
type ChannelKind string

// ChannelNative is a runtime's own sign-in on a computer (a subscription login, an OAuth session).
const ChannelNative ChannelKind = "native"

// ChannelManaged is a gateway or direct API the hub keeps, its key sealed on the hub.
const ChannelManaged ChannelKind = "managed"

// ChannelExternalShell is a computer's own launcher (a shell function, a wrapper script on its PATH).
const ChannelExternalShell ChannelKind = "external_shell"

// Valid reports whether k is one of the kinds.
func (k ChannelKind) Valid() bool {
	return k == ChannelNative || k == ChannelManaged || k == ChannelExternalShell
}

// WireProtocol is an LLM protocol a runtime speaks or a channel serves. The set is open (a runtime plugin may
// declare its own); the constants are the ones the built-in adapters know.
type WireProtocol string

// ProtocolAnthropicMessages is Anthropic's Messages API.
const ProtocolAnthropicMessages WireProtocol = "anthropic_messages"

// ProtocolOpenAIResponses is OpenAI's Responses API.
const ProtocolOpenAIResponses WireProtocol = "openai_responses"

// ProtocolOpenAIChat is OpenAI's Chat Completions API.
const ProtocolOpenAIChat WireProtocol = "openai_chat"

// ProtocolGeminiNative is Gemini's own generateContent API.
const ProtocolGeminiNative WireProtocol = "gemini_native"

// ProtocolACPNative is an engine's own Agent Client Protocol session against its native sign-in.
const ProtocolACPNative WireProtocol = "acp_native"

// WellFormed reports whether p is a protocol name: 1 to 40 of a-z, 0-9 and _.
func (p WireProtocol) WellFormed() bool {
	if len(p) == 0 || len(p) > 40 {
		return false
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// EdgeTier is how faithfully a channel serves a model over a protocol.
type EdgeTier string

// EdgeNative is the model's own protocol, passed straight through.
const EdgeNative EdgeTier = "native"

// EdgeBridgedStable is a translation between protocols whose core loop (replies, tools, thinking) is stable.
const EdgeBridgedStable EdgeTier = "bridged_stable"

// EdgeBridgedExperimental is a translation with known behaviour gaps: it is allowed with a notice and kept out of
// the default recommendations.
const EdgeBridgedExperimental EdgeTier = "bridged_experimental"

// Valid reports whether t is one of the tiers.
func (t EdgeTier) Valid() bool {
	return t == EdgeNative || t == EdgeBridgedStable || t == EdgeBridgedExperimental
}

// Rank orders the tiers, best first: native 0, bridged_stable 1, bridged_experimental 2, anything else 3.
func (t EdgeTier) Rank() int {
	switch t {
	case EdgeNative:
		return 0
	case EdgeBridgedStable:
		return 1
	case EdgeBridgedExperimental:
		return 2
	}
	return 3
}

// EffortVia is how a runtime takes a thinking effort.
type EffortVia string

// EffortCLIFlag is a start flag (--effort, -c model_reasoning_effort).
const EffortCLIFlag EffortVia = "cli_flag"

// EffortEnv is an environment variable set at start.
const EffortEnv EffortVia = "env"

// EffortACPConfig is a per-session ACP configuration option.
const EffortACPConfig EffortVia = "acp_config"

// EffortModelSuffix is a model id with the effort appended ("<model>-<effort>"), for upstreams that take only that.
const EffortModelSuffix EffortVia = "model_suffix"

// EffortNone is no effort setting at all.
const EffortNone EffortVia = "none"

// RuntimeCapability is what a runtime declares it can do (its built-in adapter, or a runtime plugin through
// assistant.execution).
type RuntimeCapability struct {
	// Runtime is the runtime's name, as launchers name their agent.
	Runtime string `json:"runtime"`
	// Protocols are the protocols it speaks to a gateway or API, preferred first. Empty: it runs on its own native
	// sign-in or shell launchers only.
	Protocols []WireProtocol `json:"protocols,omitempty"`
	// EffortVia is how it takes a thinking effort.
	EffortVia EffortVia `json:"effort_via,omitempty"`
	// Efforts are the efforts it takes, weakest first; empty is unknown (any).
	Efforts []string `json:"efforts,omitempty"`
	// LiveSwitch are the dimensions a running session can change for its next turn ("model", "effort", "channel").
	LiveSwitch []string `json:"live_switch,omitempty"`
	// Managed: a managed launcher runs it, so a managed channel can bind it.
	Managed bool `json:"managed,omitempty"`
}

// Speaks reports whether the runtime speaks p.
func (c RuntimeCapability) Speaks(p WireProtocol) bool { return slices.Contains(c.Protocols, p) }

// ChannelEdge is one protocol a channel serves a model over, and how well.
type ChannelEdge struct {
	// Protocol is the protocol.
	Protocol WireProtocol `json:"protocol"`
	// Tier is how faithfully the model is served over it.
	Tier EdgeTier `json:"tier"`
	// WireModel is the id the upstream takes over this protocol when it differs from the canonical one.
	WireModel string `json:"wire_model,omitempty"`
	// EffortVia is model_suffix when the upstream takes the effort only as a model id suffix over this protocol.
	EffortVia EffortVia `json:"effort_via,omitempty"`
	// Warnings are the channel's own words about known gaps on this edge, shown as they are.
	Warnings []string `json:"warnings,omitempty"`
}

// ChannelModel is one model a channel offers, by its canonical id.
type ChannelModel struct {
	// Canonical is the canonical model id (no effort suffix).
	Canonical string `json:"canonical"`
	// Name is how people read it; empty: Canonical.
	Name string `json:"name,omitempty"`
	// Efforts are the efforts it takes, weakest first; empty is unknown.
	Efforts []string `json:"efforts,omitempty"`
	// DefaultEffort is what it runs at with no effort given.
	DefaultEffort string `json:"default_effort,omitempty"`
	// ContextWindow in tokens; 0 is unknown.
	ContextWindow int `json:"context_window,omitempty"`
	// MaxOutputTokens is the longest reply it writes; 0 is unknown.
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// QuotaPool names the model pool of the channel's quota source it draws on (a per-model usage window's name);
	// empty: the model's own id.
	QuotaPool string `json:"quota_pool,omitempty"`
	// Edges are the protocols it is served over; empty: every endpoint of the channel at its default tier.
	Edges []ChannelEdge `json:"edges,omitempty"`
}

// ChannelQuota is the usage source a channel's accounts are listed under, for dispatch by headroom: a gateway's
// account pool (usage.gateway_accounts) and the provider its accounts are of.
type ChannelQuota struct {
	// Source is the gateway source of the pool.
	Source string `json:"source"`
	// Provider is the provider the pool's accounts are of ("claude" or "codex").
	Provider string `json:"provider"`
}

// ChannelSupply is what a channel offers, as the hub knows it.
type ChannelSupply struct {
	// Name is the channel's name (ExecutionSpec.Channel).
	Name string `json:"name"`
	// DisplayName is how people read it; empty: Name.
	DisplayName string `json:"display_name,omitempty"`
	// Kind is how it reaches its upstream.
	Kind ChannelKind `json:"kind"`
	// Runtimes are the runtimes that run on it today, through a launcher alias or as its own sign-in.
	Runtimes []string `json:"runtimes,omitempty"`
	// Endpoints are the protocols it serves; empty for a native or shell channel (its runtime's own).
	Endpoints []WireProtocol `json:"endpoints,omitempty"`
	// Models are the models it offers; empty is unknown (not listed).
	Models []ChannelModel `json:"models,omitempty"`
	// Quota is the usage source its accounts draw on; nil: none declared.
	Quota *ChannelQuota `json:"quota,omitempty"`
	// Aliases are the launcher names that stand for it (each with one runtime).
	Aliases []string `json:"aliases,omitempty"`
	// Rev changes whenever the channel is saved.
	Rev string `json:"rev,omitempty"`
}

// Model is the model of the channel whose canonical id is id; false when it does not list it.
func (c ChannelSupply) Model(id string) (ChannelModel, bool) {
	i := slices.IndexFunc(c.Models, func(m ChannelModel) bool { return m.Canonical == id })
	if i < 0 {
		return ChannelModel{}, false
	}
	return c.Models[i], true
}

// ChannelBinding is one runtime a managed channel runs, under the launcher alias older clients and
// `personad launch <alias>` know it by.
type ChannelBinding struct {
	// Runtime is the runtime.
	Runtime string `json:"runtime"`
	// Alias is the launcher name of this runtime on the channel.
	Alias string `json:"alias"`
	// API is the direct API protocol of a direct API connection ("messages", "responses", "chat"); empty for a
	// launcher.
	API string `json:"api,omitempty"`
	// BaseURL overrides the channel's Base URL for this runtime (a gateway that serves each protocol at a path of its
	// own); empty: the channel's.
	BaseURL string `json:"base_url,omitempty"`
	// Model is the alias's default model; empty: the runtime's own default.
	Model string `json:"model,omitempty"`
	// Effort is the alias's default effort; empty: the model's default.
	Effort string `json:"effort,omitempty"`
}

// ChannelConfig is one managed channel as Settings shows and edits it. The key and headers are write-only: a view
// says only whether there are any.
type ChannelConfig struct {
	// Name is the channel's name: letters, digits, _ and -.
	Name string `json:"name"`
	// DisplayName is how people read it.
	DisplayName string `json:"display_name,omitempty"`
	// BaseURL is the gateway's address.
	BaseURL string `json:"base_url"`
	// APIKey (write-only): the key or a secret reference; empty on an update keeps the one there.
	APIKey string `json:"api_key,omitempty"`
	// HasKey says the channel has a key.
	HasKey bool `json:"has_key,omitempty"`
	// Headers (write-only): extra request headers, one "Name: value" a line; empty on an update keeps them.
	Headers string `json:"headers,omitempty"`
	// HasHeaders says the channel has headers.
	HasHeaders bool `json:"has_headers,omitempty"`
	// Auth is how the key goes: "api_key" (the default) or "bearer".
	Auth string `json:"auth,omitempty"`
	// Proxy is the HTTP(S) proxy for the gateway.
	Proxy string `json:"proxy,omitempty"`
	// NoProxy is NO_PROXY for the gateway.
	NoProxy string `json:"no_proxy,omitempty"`
	// Share is its device scope: "all", "selected" or "none".
	Share string `json:"share,omitempty"`
	// ScopeMachines are the computers of a "selected" scope (and the work computers of an "all" one).
	ScopeMachines []string `json:"scope_machines,omitempty"`
	// Delivery is how computers get its key: "on_demand" or "push".
	Delivery string `json:"delivery,omitempty"`
	// Endpoints are the protocols it serves.
	Endpoints []WireProtocol `json:"endpoints"`
	// Models are the models it offers; empty is unknown.
	Models []ChannelModel `json:"models,omitempty"`
	// Quota is the usage source its accounts draw on.
	Quota *ChannelQuota `json:"quota,omitempty"`
	// Bindings are the runtimes it runs, each under its launcher alias.
	Bindings []ChannelBinding `json:"bindings"`
	// ReplaceLaunchers (write-only): managed launchers folded into this channel by this save. Each must be the alias
	// of one of its bindings; their key and scope seed the channel's when it has none, and the launchers themselves
	// go, their names living on as aliases.
	ReplaceLaunchers []string `json:"replace_launchers,omitempty"`
	// Rev changes whenever it is saved.
	Rev string `json:"rev,omitempty"`
	// Updated is when it was last saved.
	Updated time.Time `json:"updated,omitzero"`
}

// ChannelRevision is one point of the channel ledger a rollback returns to.
type ChannelRevision struct {
	// Revision is the catalog's revision this point restores.
	Revision int64 `json:"revision"`
	// At is when the change that replaced it was made.
	At time.Time `json:"at"`
	// Action is that change: "save", "delete" or "rollback".
	Action string `json:"action"`
	// Channel is the channel that change was about; empty for a rollback.
	Channel string `json:"channel,omitempty"`
	// Channels are the channels the point holds, by name.
	Channels []string `json:"channels"`
}

// ChannelCatalogView is GET /api/v1/execution/channels.
type ChannelCatalogView struct {
	// Revision is the catalog's revision; a save may name it to refuse a concurrent change.
	Revision int64 `json:"revision"`
	// Channels are the managed channels.
	Channels []ChannelConfig `json:"channels"`
	// Rollbacks are the points a rollback returns to, newest first.
	Rollbacks []ChannelRevision `json:"rollbacks"`
}

// MachineReadiness is what the hub last saw of a computer for execution: the runtimes it has, the channels it may
// use now. Known is false when the hub has never seen its launchers.
type MachineReadiness struct {
	// Machine is the computer.
	Machine string `json:"machine"`
	// Name is how people read it.
	Name string `json:"name,omitempty"`
	// Online says it is connected now.
	Online bool `json:"online"`
	// Known says the hub has seen its launchers.
	Known bool `json:"known"`
	// Runtimes are the runtimes installed there.
	Runtimes []string `json:"runtimes,omitempty"`
	// Channels are the channels it may use: its runtimes' own sign-ins and shell launchers, and the managed channels
	// whose scope takes it.
	Channels []string `json:"channels,omitempty"`
}

// CombinationLevel is how the hub judges a combination.
type CombinationLevel string

// CombinationAllowed runs silently: a native or stably bridged combination with nothing to say.
const CombinationAllowed CombinationLevel = "allowed"

// CombinationNotice runs, with a gentle note (an experimental bridge, an effort clamped, a model unverified).
const CombinationNotice CombinationLevel = "notice"

// CombinationBlocked does not run; Reasons say why in plain words and Alternatives what would.
const CombinationBlocked CombinationLevel = "blocked"

// CombinationReasonCode says what a reason is about, for clients that act on it.
type CombinationReasonCode string

// The reason codes. A blocking one makes the verdict blocked; the others are notices.
const (
	// ReasonRuntimeUnknown blocks: no runtime of that name is declared.
	ReasonRuntimeUnknown CombinationReasonCode = "runtime_unknown"
	// ReasonChannelUnknown blocks: no channel of that name.
	ReasonChannelUnknown CombinationReasonCode = "channel_unknown"
	// ReasonNoCommonProtocol blocks: the runtime and the channel share no protocol for the model.
	ReasonNoCommonProtocol CombinationReasonCode = "no_common_protocol"
	// ReasonModelNotOffered blocks: the channel lists its models and that one is not among them.
	ReasonModelNotOffered CombinationReasonCode = "model_not_offered"
	// ReasonMachineUnknown blocks: no computer of that id.
	ReasonMachineUnknown CombinationReasonCode = "machine_unknown"
	// ReasonRuntimeNotReady blocks: the computer has not got the runtime.
	ReasonRuntimeNotReady CombinationReasonCode = "runtime_not_ready"
	// ReasonChannelNotReady blocks: the computer has not got the runtime's own sign-in or shell launcher.
	ReasonChannelNotReady CombinationReasonCode = "channel_not_ready"
	// ReasonChannelOutOfScope blocks: the managed channel's device scope leaves the computer out.
	ReasonChannelOutOfScope CombinationReasonCode = "channel_out_of_scope"
	// ReasonNoLauncher blocks: no launcher runs the runtime on the channel, so nothing could start it.
	ReasonNoLauncher CombinationReasonCode = "no_launcher"
	// ReasonBridgedExperimental notes an experimental cross-protocol bridge.
	ReasonBridgedExperimental CombinationReasonCode = "bridged_experimental"
	// ReasonBridgeWarning notes one of the channel's own words about the edge.
	ReasonBridgeWarning CombinationReasonCode = "bridge_warning"
	// ReasonEffortClamped notes an effort the model does not take, run at the nearest one it does.
	ReasonEffortClamped CombinationReasonCode = "effort_clamped"
	// ReasonEffortIgnored notes an effort the runtime has no setting for.
	ReasonEffortIgnored CombinationReasonCode = "effort_ignored"
	// ReasonModelUnlisted notes a managed channel that lists no models, so the model could not be checked.
	ReasonModelUnlisted CombinationReasonCode = "model_unlisted"
	// ReasonMachineOffline notes a computer that is offline now.
	ReasonMachineOffline CombinationReasonCode = "machine_offline"
	// ReasonReadinessUnknown notes a computer whose launchers the hub has never seen.
	ReasonReadinessUnknown CombinationReasonCode = "readiness_unknown"
)

// Blocks reports whether a reason of code c blocks the combination.
func (c CombinationReasonCode) Blocks() bool {
	switch c {
	case ReasonRuntimeUnknown, ReasonChannelUnknown, ReasonNoCommonProtocol, ReasonModelNotOffered,
		ReasonMachineUnknown, ReasonRuntimeNotReady, ReasonChannelNotReady, ReasonChannelOutOfScope, ReasonNoLauncher:
		return true
	}
	return false
}

// CombinationReason is one reason of a verdict, in plain words in the request's language.
type CombinationReason struct {
	// Code says what it is about.
	Code CombinationReasonCode `json:"code"`
	// Dimension is the dimension it is about: runtime, channel, model, effort or machine.
	Dimension string `json:"dimension,omitempty"`
	// Text is the reason in plain words.
	Text string `json:"text"`
}

// CombinationVerdict is the hub's judgement of one combination on one computer (or on none).
type CombinationVerdict struct {
	// Spec is the combination judged, its empty dimensions filled as the hub would run it.
	Spec ExecutionSpec `json:"spec"`
	// Level is the judgement.
	Level CombinationLevel `json:"level"`
	// Tier is the edge it runs over; empty when blocked before one was found.
	Tier EdgeTier `json:"tier,omitempty"`
	// Protocol is the protocol it runs over; empty for a native or shell channel's own.
	Protocol WireProtocol `json:"protocol,omitempty"`
	// WireModel is the model id the upstream gets.
	WireModel string `json:"wire_model,omitempty"`
	// WireEffort is the effort the runtime gets (after clamping); empty: the model's default.
	WireEffort string `json:"wire_effort,omitempty"`
	// Launcher is the launcher alias that runs it, for clients and starts that know only launchers.
	Launcher string `json:"launcher,omitempty"`
	// Recommended says it belongs in the default (everyday) recommendations: not blocked and not experimental.
	Recommended bool `json:"recommended"`
	// Reasons are why it is blocked or what to know, blocking ones first.
	Reasons []CombinationReason `json:"reasons,omitempty"`
	// Alternatives are combinations that would run instead, best first (a blocked verdict's suggestions).
	Alternatives []ExecutionSpec `json:"alternatives,omitempty"`
}

// ExecutionCatalog is GET /api/v1/execution/catalog: the three declarations the matrix is computed from, and the
// launcher aliases, so a client can filter the four selectors against each other. Verdicts come from
// POST /api/v1/execution/validate or, for every alias on one computer, from Combinations.
type ExecutionCatalog struct {
	// Runtimes are the runtime declarations.
	Runtimes []RuntimeCapability `json:"runtimes"`
	// Channels are the channel declarations.
	Channels []ChannelSupply `json:"channels"`
	// Machines are the computers' readiness.
	Machines []MachineReadiness `json:"machines"`
	// Aliases are the launchers, each as the alias of the combination it runs, by name.
	Aliases []LauncherAlias `json:"aliases"`
	// Combinations are, with ?machine=, the verdict of every launcher alias's own combination there.
	Combinations []CombinationVerdict `json:"combinations,omitempty"`
}

// LauncherAlias is a launcher as the alias of its runtime and channel, with its own default model and effort.
type LauncherAlias struct {
	// Launcher is the launcher's name, as older clients and `personad launch` use it.
	Launcher string `json:"launcher"`
	// Spec is what it runs.
	Spec ExecutionSpec `json:"spec"`
}

// ExecutionValidateRequest is POST /api/v1/execution/validate.
type ExecutionValidateRequest struct {
	// Spec is the combination; empty dimensions are filled as the hub would run it.
	Spec ExecutionSpec `json:"spec"`
	// Machine is the computer to judge it on; empty: on none (no readiness checks).
	Machine string `json:"machine,omitempty"`
}

// LastGoodSpec is a conversation's last known good execution: the four dimensions (and their launcher alias) its
// last turn that ended without an error ran on.
type LastGoodSpec struct {
	// Spec is the four dimensions.
	Spec ExecutionSpec `json:"spec"`
	// Launcher is the launcher alias it ran on.
	Launcher string `json:"launcher"`
	// Machine is the computer it ran on.
	Machine string `json:"machine"`
	// At is when that turn ended.
	At time.Time `json:"at"`
}
