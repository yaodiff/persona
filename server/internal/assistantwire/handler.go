package assistantwire

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/wireids"
)

// ---- what the hub sends the assistant (Handler) ----

// RoleHeader carries the role of the device a relayed request came from (the hub's auth decides it and sets the
// header, replacing any the client sent). Absent (a test's own request) means a full device.
const RoleHeader = "X-Persona-Role"

// ExtensionHeader carries the extension a relayed request came from when RoleHeader is "extension" (set by the hub
// like RoleHeader; never the client's).
const ExtensionHeader = "X-Persona-Extension"

// AdminHeader is "1" on a relayed request that came through the hub's admin UI with a signed-in admin session (the
// hub's admin routes it relays, devicewire.AdminUsageRoutes); set by the hub like RoleHeader, never the client's.
// Its value is the one extwire.HeaderAdmin carries on the extensions' relay.
const AdminHeader = "X-Persona-Admin"

// DeviceNameHeader carries the name of the device a relayed request came from ("" for the hub's own token), so a
// manager can say who did something (a model profile's switch, docs/assistant/model-profiles.md); set by the hub like
// RoleHeader, never the client's.
const DeviceNameHeader = "X-Persona-Device-Name"

// DeviceIDHeader carries the hub-authenticated paired device ID (for example "dev_...") of the device a relayed
// request came from ("" when the caller is not a paired user device), set by the hub like RoleHeader, never the
// client's.
const DeviceIDHeader = "X-Persona-Device-ID"

// PersonHeader is "1" on a relayed request from a person's own full device (a device token with its own id, not the
// hub's token or a pairing code), set by the hub like RoleHeader, never the client's: what may change a message held
// for an Agent while its computer is away (#10661), whose delivery carries that person's provenance.
const PersonHeader = "X-Persona-Person"

// HandoffApprover is the device the hub found may approve a hand-off card (proto.SecretHandoffTool): a full device
// whose app shows the card (docs/extensions/secret-providers.md "Who may approve a hand-off"). It travels in
// HTTPRequest, never in a header, so no client can set it, and an older hub never sends it: a manager approves a
// hand-off only with it.
type HandoffApprover struct {
	DeviceID   string `json:"device_id"`
	DeviceName string `json:"device_name,omitempty"`
}

type handoffApproverKey struct{}

// WithHandoffApprover is ctx with the device the hub found may approve a hand-off card.
func WithHandoffApprover(ctx context.Context, a HandoffApprover) context.Context {
	return context.WithValue(ctx, handoffApproverKey{}, a)
}

// HandoffApproverOf is the device WithHandoffApprover put on ctx; false when there is none.
func HandoffApproverOf(ctx context.Context) (HandoffApprover, bool) {
	a, ok := ctx.Value(handoffApproverKey{}).(HandoffApprover)
	return a, ok && a.DeviceID != ""
}

type humanStartKey struct{}

// WithHumanStart is ctx of a new conversation's start that a person's own app asked for, as the hub vouched
// (HTTPRequest.Human): its first message counts as theirs (StartParams.Human).
func WithHumanStart(ctx context.Context) context.Context {
	return context.WithValue(ctx, humanStartKey{}, true)
}

// HumanStartOf reports whether WithHumanStart marked ctx.
func HumanStartOf(ctx context.Context) bool {
	human, _ := ctx.Value(humanStartKey{}).(bool)
	return human
}

type clarificationProofKey struct{}

// WithClarificationProof carries the opaque one-time token the hub attached to
// an authenticated clarify request, or the extension manager attached after a
// fresh channel owner action. It never carries a caller-supplied owner_input.
func WithClarificationProof(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, clarificationProofKey{}, token)
}

// ClarificationProofOf returns the opaque token, empty when this is an older
// hub/manager combination without clarification provenance.
func ClarificationProofOf(ctx context.Context) string {
	token, _ := ctx.Value(clarificationProofKey{}).(string)
	return token
}

type replyInputKey struct{}

// WithReplyInput marks a conversation start as a direct legacy app start for missing-reply recovery.
func WithReplyInput(ctx context.Context) context.Context {
	return context.WithValue(ctx, replyInputKey{}, true)
}

// ReplyInputOf reports whether WithReplyInput marked ctx.
func ReplyInputOf(ctx context.Context) bool {
	reply, _ := ctx.Value(replyInputKey{}).(bool)
	return reply
}

// ToolCall is assistant.tool's params.
type ToolCall struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	MachineID string          `json:"machine_id"` // the machine of the conversation that called it
	SessionID string          `json:"session_id"` // the conversation; "" when it is not known
}

// SessionRef names one session on one machine.
type SessionRef struct {
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
}

// UserSpokeParams carries text only when the hub authenticated a person's own app, for both
// Agent conversations and the assistant's own supervisor / direction conversations. The event
// reaches the manager before the message is persisted, so a direction's decision cards rely on
// this text rather than a transcript read.
// Keep it out of SessionRef: session references are also used as map keys.
type UserSpokeParams struct {
	SessionRef
	Text string `json:"text,omitempty"`
	// Source is set only with Text from authenticated owner input to an Agent: "app" for the app's message route,
	// "terminal" for the terminal Agent-message route. It is never a runtime transcript role.
	Source string `json:"source,omitempty"`
	// ClientMsgID is the send's client_msg_id, sent with Text when the message has one: the same id its
	// session.assistant_sent (ConversationSent) names, so the manager can wait for the send's outcome before an
	// irreversible step (#13041). An older hub sends none; an older manager ignores it.
	ClientMsgID wireids.ClientMsgID `json:"client_msg_id,omitempty"`
	At          time.Time           `json:"at,omitempty"`
}

// Verified owner-input provenance carried by UserSpokeParams.
const (
	OwnerInputApp      = "app"
	OwnerInputTerminal = "terminal"
)

// ContextParams is assistant.context's params. MachineID is the asking node's machine: the hub sets it from the
// node's connection, never from the node's params. Cwd is the conversation's launch directory as the node reports it,
// used only before the session is noted or listed (such as a new space conversation's first turn, including an
// empty-id Codex start). With Ack the call only confirms that a message carrying those view errors
// (ContextResult.ViewErrors) was delivered, and answers nothing.
type ContextParams struct {
	RecoveryID      string             `json:"recovery_id,omitempty"`
	RecoveryOnly    bool               `json:"recovery_only,omitempty"`
	PrepareRecovery bool               `json:"prepare_recovery,omitempty"`
	SessionID       string             `json:"session_id"`
	MachineID       string             `json:"machine_id,omitempty"`
	Cwd             string             `json:"cwd,omitempty"`
	Query           string             `json:"query,omitempty"`
	Ack             []ViewErrorReceipt `json:"ack,omitempty"`
}

// ContextResult is assistant.context's answer: the <persona-context> text, and the <persona-view-errors> block of the
// conversation's rich views that failed their check and were not reported yet (docs/assistant/assistant-views.md "Next-turn feedback").
// Reading it consumes nothing: the node acks Receipts once the message is delivered.
// Receipts also carry what else the hub wants confirmed as delivered with the message (the background supervisor's
// announcement, item_id "background_supervisor"): the node sends them all back as they are.
type ContextResult struct {
	RecoveryID          string             `json:"recovery_id,omitempty"`
	Recovery            string             `json:"recovery,omitempty"`
	CompactInstructions string             `json:"compact_instructions,omitempty"`
	Text                string             `json:"text"`
	ViewErrors          string             `json:"view_errors,omitempty"`
	Receipts            []ViewErrorReceipt `json:"view_error_receipts,omitempty"`
}

// ViewErrorReceipt names the reported errors of one reply as they were read: an ack whose digest no longer matches
// (the errors changed since) confirms nothing.
type ViewErrorReceipt struct {
	ItemID string `json:"item_id"`
	Digest string `json:"digest"`
}

// Action is manager.action's params (appkit.Action): op answer, with the user's answer (a proto.Resolution).
type Action struct {
	ActionID           string          `json:"action_id"`
	SessionID          string          `json:"session_id,omitempty"`
	Op                 string          `json:"op"` // answer
	TurnID             string          `json:"turn_id,omitempty"`
	InteractionID      string          `json:"interaction_id"`
	InteractionVersion uint64          `json:"interaction_version,omitempty"`
	Answer             json.RawMessage `json:"answer,omitempty"`
}

// ActionResult is manager.action's answer.
type ActionResult struct {
	OK  bool   `json:"ok"`
	Seq uint64 `json:"seq"`
}

// InteractionResolved is interaction.resolved's params: the card, the answer, and resolved / dismissed / failed.
type InteractionResolved struct {
	Interaction proto.Interaction `json:"interaction"`
	Resolution  proto.Resolution  `json:"resolution"`
	Status      string            `json:"status"`
}

// InteractionRaised is interaction.raised's params: an agent of a quiet task (#1731) asks the user something; the
// hub sends no push for it, the main conversation is told instead. The card carries its task's title.
type InteractionRaised struct {
	Interaction proto.Interaction `json:"interaction"`
}

// MessageCard is a message the inbox's AI twin drafted a reply to, about one of the user's tasks: what the card in the
// task's conversation shows until the item is sent or done (the app sends the draft through the inbox, by the item's
// id).
type MessageCard struct {
	TodoID  string `json:"todo_id"`
	Source  string `json:"source"`            // lark | mail | …
	Account string `json:"account,omitempty"` // Lark / the mail address
	Chat    string `json:"chat"`              // the chat's name / the mail's subject
	Sender  string `json:"sender,omitempty"`
	Text    string `json:"text"` // the message, clipped
	Draft   string `json:"draft"`
	// MessageVersion and ProgressVersion identify the source message and task
	// progress used to produce the draft, independently of its wording.
	MessageVersion  string `json:"message_version,omitempty"`
	ProgressVersion string `json:"progress_version,omitempty"`
	// SentAt: the reply to the item went out (from this card or from the inbox, on any device) and Sent is what it
	// said; set when the inbox sent the reply (the inbox is the assistant's too), never cleared, so the card stays sent
	// when the item opens again. Older send records can lack Sent, in which case the sent text is unknown.
	SentAt *time.Time `json:"sent_at,omitempty"`
	Sent   string     `json:"sent,omitempty"`
	// Dismissal is per draft card, not inbox filing or task completion. Undo
	// removes these two fields but retains DismissalUpdatedAt for incremental reads.
	DismissedAt        *time.Time `json:"dismissed_at,omitempty"`
	DismissalID        string     `json:"dismissal_id,omitempty"`
	DismissalUpdatedAt *time.Time `json:"dismissal_updated_at,omitempty"`
}

// APIErrorMarkers start what Claude Code answers when the model call itself failed (an error card in the app). Both
// sides read a turn by them: the hub's session lists say an agent's turn was cut off (proto.Session.TurnError), the
// assistant's scheduled runs and tasks act on it. "Not logged in · Please run /login" is what Claude Code answers when
// it has no credentials (a model of its own, "<synthetic>"): the turn did not run, the user must sign in
// (docs/assistant/onboarding.md 4.4). Its separator is in the marker, so a reply that talks about being logged out is a reply.
var APIErrorMarkers = []string{"API Error:", "There's an issue with the selected model", "Failed to authenticate",
	"OAuth token has expired", "Invalid API key", "Not logged in ·", "Request too large"}

// refusalMarkers start the other lines Claude Code writes itself instead of an answer when the request was refused
// before the model ran: a sign-in that is gone ("Please run /login · API Error: 403", "Login expired · Please run
// /login"); a subscription's usage limit is its own line (UsageLimitText). Newer versions mark every such
// line in their record (isApiErrorMessage, read by the node into proto.Session.TurnError); these read an older node's
// list. Only as the start of the answer, never inside one (a reply may quote them).
var refusalMarkers = []string{"Please run /login", "Login expired ·", "OAuth token revoked ·", "Login reached its time limit ·"}

// InterruptedSummary is what is said of a turn the model service cut off, instead of its last words (the error is no
// conclusion, #1516): an interrupted task's receipt, the push.
const InterruptedSummary = "模型服务出错，中断了"

// CutOff: an agent's answer is the model API error that ended its turn (it starts with one, APIErrorMarkers or
// refusalMarkers, or is Claude Code's usage-limit line).
func CutOff(answer string) bool {
	answer = strings.TrimSpace(answer)
	const sealedPrefix = "this conversation was compacted natively by "
	if len(answer) >= len(sealedPrefix) && strings.EqualFold(answer[:len(sealedPrefix)], sealedPrefix) &&
		proto.UnrecoverableTurnErrorCode(answer) != "" {
		return true
	}
	return startsWithAny(answer, APIErrorMarkers) || startsWithAny(answer, refusalMarkers) || UsageLimitText(answer)
}

func startsWithAny(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// StartEnded is session.start_ended's params: how an app's start that the node ran before answering went (a resume, a
// Codex, an older app; one answered at once is followed in the session lists), for launcher health
// (docs/server/launcher-health.md). Launcher is the one the start ran ("" : claude); Error and Code, Data are the
// node's answer when it failed (its rpc.Error's code and data, when it had them).
type StartEnded struct {
	MachineID string          `json:"machine_id"`
	SessionID string          `json:"session_id,omitempty"`
	Launcher  string          `json:"launcher,omitempty"`
	OK        bool            `json:"ok"`
	Error     string          `json:"error,omitempty"`
	Code      string          `json:"code,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// ConversationSent is session.assistant_sent's params: an app's message the hub sent to the assistant's conversation
// SessionID, and what became of it. Outcome is ConversationDelivered, ConversationFailed or ConversationUnknown (it may
// have been typed); Code and Error are the hub's answer when it was not delivered. ClientMsgID is the message's
// client_msg_id when it has one, so the manager settles each message by its own notice (#11392); an older hub sends
// none.
type ConversationSent struct {
	MachineID   string `json:"machine_id"`
	SessionID   string `json:"session_id"`
	Outcome     string `json:"outcome"`
	Code        string `json:"code,omitempty"`
	Error       string `json:"error,omitempty"`
	ClientMsgID string `json:"client_msg_id,omitempty"`
}

// ConversationSent's outcomes.
const (
	ConversationDelivered = "delivered"
	ConversationFailed    = "failed"
	ConversationUnknown   = "unknown"
)

// DeviceSessions is sessions.changed's params: the list as the node sent it, and when the node's connection to the hub
// began (a new one: the node may have restarted and forgotten what it was told, e.g. the herdr todo marks).
type DeviceSessions struct {
	MachineID string          `json:"machine_id"`
	Sessions  []proto.Session `json:"sessions"`
	Connected time.Time       `json:"connected,omitzero"`
}
