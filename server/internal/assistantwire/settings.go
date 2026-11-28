package assistantwire

import (
	"time"

	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/wireids"
)

// ---- the assistant's settings, and the conversations the hub's features start (docs/assistant/assistant.md "Boundary with hub") ----

// Profile is the assistant's settings (settings `assistant`, its own) as the hub's own features need them, in every
// snapshot: a phone's message to the main conversation is told apart, the model configuration's main chat runs on its
// machine, the hub reads times in the user's zone, and the nodes keep listing Keep (the Feishu bot and the channels are
// the assistant's own). The hub keeps the last
// one; before one came in it goes by hub.json's assistant section, its own time zone and no main conversation.
type Profile struct {
	MachineID     string `json:"machine_id"` // where its conversations run
	Main          string `json:"main,omitempty"`
	MainMachineID string `json:"main_machine_id,omitempty"` // confirmed route of Main, independent of the default
	// Launcher is the one it runs on when "Models" › main chat names none (its own setting, else hub.json's, else claude):
	// that purpose's default.
	Launcher string `json:"launcher"`
	Timezone string `json:"timezone,omitempty"` // the user's (the phone reports it), IANA; "" while not known
	// Keep are the conversations their machines list however long they are quiet: the main one, the main ones it
	// replaced, scheduled tasks' own (node.configure keep_sessions: a busy machine's newest transcripts crowd them out).
	Keep []SessionRef `json:"keep"`
	// ExtensionOwners lists the extensions the user marked as their own. It carries the legacy trust snapshot for
	// track reads. Current managers also include the live verdict in extensions.authorize.
	ExtensionOwners []string `json:"extension_owners,omitempty"`
	// Attention is the attention mode the user set (attention.go, #13503), which the hub's push filter reads; nil (an
	// older manager, or never set) is normal. The hub reads its end itself (Attention.Effective).
	Attention *Attention `json:"attention,omitempty"`
}

// LabsEventType is the client event discriminator for Labs receipts.
type LabsEventType string

const LabsSeenEventType LabsEventType = "assistant_labs_seen"

// LabsSeenEvent is the committed read-receipt set sent to connected clients via ws.broadcast.
type LabsSeenEvent struct {
	Type     LabsEventType `json:"type"`
	LabsSeen []string      `json:"labs_seen"`
}

// MainRouteMachine keeps legacy profiles usable while preferring the confirmed main binding.
func (p Profile) MainRouteMachine() string {
	if p.MainMachineID != "" {
		return p.MainMachineID
	}
	return p.MachineID
}

// The hub asks the assistant (Handler) for what its own features need of the assistant's conversations.
const (
	// MethodStart: StartParams → {session}: a conversation the hub's features start (the inbox's AI twin).
	MethodStart = "assistant.start"
	// MethodSendParams: SendParams → {params}: a message for one of its conversations (the phone's, one the node sends
	// by itself) with the launcher and model it runs on, and what
	// the old main conversation left before the text of the first one to a new main conversation started empty.
	MethodSendParams = "assistant.send_params"
	// MethodRename: RenameParams → the node's answer: the user renames one of its conversations in the app (the main
	// one has no title to change, a Codex one only its tab); the new title is the user's from now on.
	MethodRename = "assistant.rename"
	// MethodKeepUndelivered: KeepUndeliveredParams → {undelivered}: the owner's message to one of its conversations
	// went nowhere (nothing was typed: its process did not start, its computer was away), its transport outcome is
	// unknown, or SendParams answered CodeUndeliveredAhead. The manager keeps it and sends it again by itself, once
	// (#10194); an unknown first attempt is only looked for in the transcript, never sent again by itself, and left to
	// the owner's Retry or Delete when it is not found (#10669). An older manager answers "unknown method": the hub
	// answers the app with the failure, as before.
	MethodKeepUndelivered = "assistant.keep_undelivered"
)

var (
	StartMethod           = buswire.Method[StartParams, StartResult]{Name: MethodStart}
	KeepUndeliveredMethod = buswire.Method[KeepUndeliveredParams, KeepUndeliveredResult]{Name: MethodKeepUndelivered}
)

// InboxPermissionModeParam is manager-only authorization metadata on assistant.send, session.send and
// assistant.model. It carries the AI twin's own saved mode separately from the caller-controlled permission_mode.
// Older nodes ignore it and retain their default clamp; newer nodes never treat its absence as permission to bypass.
const InboxPermissionModeParam = "inbox_permission_mode"

// DefaultPermissionModeParam carries the manager's current default separately from an explicit permission_mode.
// PermissionModeExplicitParam marks the latter as a deliberate per-conversation choice. Older nodes ignore both and
// retain their legacy resume behavior; newer nodes keep the legacy behavior when an older manager omits them.
const (
	DefaultPermissionModeParam  = "default_permission_mode"
	PermissionModeExplicitParam = "permission_mode_explicit"
)

// CodeUndeliveredAhead: SendParams refuses the owner's message because older ones to the same conversation are kept
// undelivered (MethodKeepUndelivered): the hub keeps this one behind them instead of sending it first.
const CodeUndeliveredAhead = "undelivered_ahead"

// CodeDeliveryUnknown: the node connection or caller's wait ended before a send's result was known. The first attempt
// may have typed the message, so the manager only looks for it in the transcript and leaves it to the owner when it
// is not found, never sending it again by itself (#10669).
const CodeDeliveryUnknown = "delivery_unknown"

// The node's methods the assistant calls for its directory, its conversations and its settings
// (docs/protocol/protocol.md "hub ↔ node"), passed through by the hub's device.call like the others.
const (
	DeviceAssistantDirectory = "assistant.directory" // {path} -> {cwd}; validates an existing directory without writes
	// DeviceAssistantPrepare: {dir, claude_md, work_guide_md?, connectors, clis, plugin_skills, deny_dirs?} → {cwd,
	// plugin_skills_need}. deny_dirs: [{dir, track}], the track directories the assistant's other conversations' Read
	// and Edit are denied (nodeproto.DenyDirs).
	DeviceAssistantPrepare   = "assistant.prepare"
	DeviceAssistantExecution = "assistant.execution" // {session_id} → proto.AssistantExecution
	// DeviceAssistantGuestPrepare: {dir, claude_md} → {cwd}: a guest conversation's directory (assistant/guest.go), only
	// its CLAUDE.md / AGENTS.md written; the assistant's connectors, CLIs and skills are left as they are. A node without
	// it answers "unknown method": no guest conversation starts there.
	DeviceAssistantGuestPrepare = "assistant.guest_prepare"
	// DeviceAssistantGuestDelete: {dir, session_id} → {deleted}: a meeting's room removed at its transcript's cut or
	// with its recording (docs/assistant/meeting-minutes.md "Deletion"): its process, transcript and directory. With
	// kind meeting, assistant.guest_prepare answers {cwd, kind, start_ids} on a node that starts rooms; an older node
	// answers {cwd} only, or "unknown method" for this one.
	DeviceAssistantGuestDelete = "assistant.guest_delete"
	// DeviceAssistantWarm: {cwd, launcher, …model} → {}: a process ready for the next new conversation.
	DeviceAssistantWarm = "assistant.warm"
	// DeviceAssistantMemory: {op, cwd, name?, content?} → the memory files (op list) or {}.
	DeviceAssistantMemory = "assistant.memory"
	// DeviceAssistantCompact: {session_id, only_running?} → {}; only_running refuses not_running instead of starting a
	// process that is not running (an older node ignores it and starts one).
	DeviceAssistantCompact = "assistant.compact"
	// DeviceAssistantHandoff: {session_id} → {summary, recent}: what a main conversation leaves for the next one.
	DeviceAssistantHandoff = "assistant.handoff"
	// DeviceLaunchersList: {} → {launchers}, the node's own.
	DeviceLaunchersList = "launchers.list"
	// DeviceLaunchersProbe: {name} → {detail}.
	DeviceLaunchersProbe = "launchers.probe"
)

// ConfigDirClaudeOnly is the node's refusal of a config_dir for a conversation whose launcher runs another agent than
// Claude Code, which alone has profiles. The node refuses it before starting anything (node/assistant.go), so a caller
// that asked for a profile without knowing what the launcher runs can start again without one (assistant
// startSignedIn, #6884).
const ConfigDirClaudeOnly = "config_dir is only supported for Claude assistant conversations"

// StartParams is assistant.start's params.
type StartParams struct {
	RequireLauncher     bool          `json:"-"` // start on the requested preset or refuse before sending
	TaskModel           *ModelBinding `json:"-"`
	Cwd                 string        `json:"-"` // internal scheduled task directory; validated on the target computer
	TaskExecution       bool          `json:"-"` // refuse execution substitutions
	LegacyTaskExecution bool          `json:"-"` // an unconfigured scheduled turn may use an older node
	PromptSelections    bool          `json:"-"` // from the stored, owner-approved scheduled prompt
	Text                string        `json:"text"`
	Images              []proto.Image `json:"images,omitempty"`
	Launcher            string        `json:"launcher,omitempty"`
	Model               string        `json:"model,omitempty"`      // the first message's model, picked as a message's is (#5033)
	ConfigDir           string        `json:"config_dir,omitempty"` // Claude profile on the target machine; "" private, "legacy" old resolution, "default" unset
	PermissionMode      string        `json:"permission_mode,omitempty"`
	Effort              string        `json:"effort,omitempty"`
	// ServiceTier: the native speed for a new Codex conversation, acknowledged before its first turn. Independent of Effort.
	ServiceTier string `json:"service_tier,omitempty"`
	Purpose     string `json:"purpose,omitempty"`    // the model configuration's purpose; "" is the main chat's
	MachineID   string `json:"machine_id,omitempty"` // "" is the assistant's machine
	// Space: "health" starts it in the health space (its own directory, instructions and memory, docs/assistant/health.md);
	// "" is the assistant's own.
	Space string `json:"space,omitempty"`
	// Main: it becomes the main conversation (what the old one left goes before Text).
	Main bool `json:"main,omitempty"`
	// Public: the first message answers in someone else's chat. It suppresses the owner's live snapshot for this turn
	// only; never over the bus or accepted from an app's conversation-start request.
	Public bool `json:"-"`
	// Untitled: not one of the conversations it names and lists as its own (the inbox's AI twin: its result is the
	// draft in the inbox).
	Untitled bool `json:"untitled,omitempty"`
	// Origin and OriginRef: who opens it (SessionView.Origin); "" is the user's.
	Origin    string `json:"origin,omitempty"`
	OriginRef string `json:"origin_ref,omitempty"`
	// OpenedBy: the extension whose token opened it (SessionView.OpenedBy). Only POST /api/v1/assistant/conversations
	// sets it, from the hub's headers; never over the bus.
	OpenedBy string `json:"-"`
	// GuestTools: a guest conversation (assistant/guest.go) that may use only these connector tools; nil is an
	// ordinary one.
	GuestTools []string `json:"guest_tools,omitempty"`
	// GuestHandoff: a proxy reply's guest (assistant/bot_handoff.go). The manager registers it before the node
	// starts it; never over the bus.
	GuestHandoff string `json:"-"`
	// GuestGroup: a group guest's conversation key (chat and thread, assistant/bot_guest.go). The manager registers it
	// before the node starts it; never over the bus.
	GuestGroup string `json:"-"`
	// NewSessionID: start it under this session id (a meeting's follow-up, made once also across a restart); a node
	// that cannot picks its own. Never over the bus.
	NewSessionID string `json:"-"`
	// ClientMsgID: the first message's client_msg_id (with NewSessionID: the node sends it once and its transcript
	// item carries it, as a send's; without, the node ties it to the new conversation's first line). Never over the bus.
	ClientMsgID string `json:"-"`
	// Human attributes the first message to an app start vouched for by the hub. It is used for missing replies.
	// The node adds no client message ID to that input. Never over the bus.
	Human bool `json:"-"`
	// OriginDeviceID is the hub-authenticated paired device ID that opened this conversation, set from
	// DeviceIDHeader on POST /api/v1/assistant/conversations; never over the bus.
	OriginDeviceID wireids.DeviceID `json:"-"`
}

// StartResult carries the conversation started for the hub.
type StartResult struct {
	Session proto.Session `json:"session"`
}

// FirstMessageIDPrefix begins the client_msg_id the manager gives the first message of a conversation a person's own
// app opened (StartParams.Human). It is the manager's alone: the hub refuses an app's send that names one, so no later
// message can carry it; a channel's ids (<channel>-<message>) never begin with it.
const FirstMessageIDPrefix = "start:"

// SendParams is assistant.send_params's params: Params are the node's assistant.send / session.send params. Lang is
// the app's language, the one a refusal is said in ("": the user's; an older hub sends none).
type SendParams struct {
	MachineID string         `json:"machine_id"`
	SessionID string         `json:"session_id"`
	Params    map[string]any `json:"params"`
	Lang      string         `json:"lang,omitempty"`
	// Human is the hub's word that a person's own app sent this message: the request's credential is a full device's
	// (never an extension's, a computer link's or the hub's own token). Set by the hub's auth, never from the request;
	// the manager's own sends (channels, the inbox, meetings) never set it, and an older hub never sends it. A hand-off's
	// Always rule spares the card only for a turn of such messages (assistant secret_handoff.go).
	Human bool `json:"human,omitempty"`
	// VerbatimText is internal-only: an approval clarification keeps the owner's
	// exact question instead of expanding workspace mentions or main-chat handoff.
	VerbatimText bool `json:"-"`
	// ReplyInput is a direct app send authenticated by the hub's legacy owner credential. It only qualifies for
	// missing-reply recovery. Internal sends and other roles cannot set it, and it does not authorize a hand-off.
	ReplyInput bool `json:"reply_input,omitempty"`
	// OriginDeviceID is the hub-authenticated paired device ID (for example "dev_...") that sent this message, or
	// "" when not sent by a paired user device. Set by the hub from verified credentials, never from request body.
	OriginDeviceID wireids.DeviceID `json:"origin_device_id,omitempty"`
}

// KeepUndeliveredParams is assistant.keep_undelivered's params. Params are the app's message as it came (text, images,
// launcher, model; never what SendParams made of it): a retry runs SendParams again. Human is SendParams.Human for it;
// Error the failure the owner is told, Code its code ("" for none).
type KeepUndeliveredParams struct {
	MachineID      string           `json:"machine_id"`
	SessionID      string           `json:"session_id"`
	Params         map[string]any   `json:"params"`
	Human          bool             `json:"human,omitempty"`
	Lang           string           `json:"lang,omitempty"`
	Code           string           `json:"code,omitempty"`
	Error          string           `json:"error,omitempty"`
	CredentialID   string           `json:"credential_id,omitempty"`
	OriginDeviceID wireids.DeviceID `json:"origin_device_id,omitempty"`
	// TriedAt is when the first send began for CodeDeliveryUnknown: its line is looked for from then on. Zero means
	// nothing was typed, so the message is simply sent again. The original start, not when the connection ended,
	// covers a slow first attempt.
	TriedAt time.Time `json:"tried_at,omitempty"`
}

// KeepUndeliveredResult carries the message kept for later delivery.
type KeepUndeliveredResult struct {
	Undelivered *proto.UndeliveredMessage `json:"undelivered"`
}

// RenameParams is assistant.rename's params: Params are the app's ({title?, tab_name?}).
type RenameParams struct {
	MachineID string         `json:"machine_id"`
	SessionID string         `json:"session_id"`
	Params    map[string]any `json:"params"`
	// Extension is the extension whose token asked, as the hub's auth found it (never what the caller says), "" for
	// anyone else: its title is a hint the hub names the conversation by, not the user's (docs/protocol/protocol.md
	// "Origin of assistant chats"). An older hub never sends it, and an older manager takes every rename as the user's.
	Extension string `json:"extension,omitempty"`
}

// CodeBadRequest is what the assistant refuses a request of the hub's with when the app's request itself is wrong
// (the hub answers it 400 with the message).
const CodeBadRequest = "bad_request"

// What the hub's apps tell the assistant for its directory and its settings.
const (
	// ConnectorsSignedIn: {} → {connectors, clis, lark_cli}: the connectors switched on and signed in (rate limited
	// counts), their command lines (name → connector id), and whether hub.json sets up lark-cli.
	ConnectorsSignedIn = "connectors.signed_in"

	// AppPlugins is the hub's plugins (docs/extensions/extensions.md "Assistant").
	AppPlugins = "plugins"
	// PluginsAssistant: {machine_id} → {instructions, skills}: the plugins' parts of the assistant's instructions, and
	// their skills for that machine (files only with those it was not sent yet).
	PluginsAssistant = "plugins.assistant"
	// PluginsSkillsNeed: {machine_id, names} → {}: the machine's node lacks these skills (its assistant.prepare said
	// plugin_skills_need): their files go next time.
	PluginsSkillsNeed = "plugins.skills_need"
	// PluginsProjectContext: ProjectContextParams → ExtensionContextResult: what the extensions wrote for these
	// projects' background (assistant_data.go).
	PluginsProjectContext = "plugins.project_context"
	// PluginsInboxContext: {} → ExtensionContextResult: what the extensions wrote for the Inbox's triage and drafts,
	// and the sentences a draft must not repeat.
	PluginsInboxContext = "plugins.inbox_context"

	// SettingsList: {} → {settings: [Setting]}: the hub's own settings persona_settings offers; app.tool persona_settings
	// runs get / set / add / remove on them (a change waits for the user's approval on the phone).
	SettingsList = "settings.list"
)

// Setting is one of the hub's settings persona_settings offers.
type Setting struct {
	Key     string   `json:"key"`
	Title   string   `json:"title"`
	About   string   `json:"about"`
	Value   string   `json:"value,omitempty"` // what set / add take, for the model
	Actions []string `json:"actions"`
}
