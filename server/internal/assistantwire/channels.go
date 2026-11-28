package assistantwire

import (
	"encoding/json"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the channels: the Feishu bot and the chat app bots (docs/extensions/channels.md, docs/extensions/bots.md) ----

// Which conversation a channel talks to, following the turn and sending its answer back, what is asked there and its
// answer, the commands, the pushes and the sync mirror are the assistant's; the hub keeps the transport — the Feishu
// open platform's long connection and REST (pairing among them), the plugins' channel programs — and hands the
// assistant what comes in. The channels' routes are the assistant's (Routes) but binding (POST …/bind: credentials,
// sign-in fields, pairing): no secret of a channel or the bot passes through the assistant. /api/v1/bots/feishu* stays
// the hub's. What answers a card in a channel carries the owner's action (InteractionsOwnerAction): the hub issued
// it when it handed the owner's press or message over, and takes it once — the assistant can't approve anything on
// its own.
const (
	// MethodChannelReceive: ChannelReceive → {}: the owner's message from a plugin's channel, taken (not its turn).
	MethodChannelReceive = "channels.receive"
	// MethodChannelUnbound: ChannelUnbound → {}: clear the channel's pending turns and text questions before
	// signing out finishes, including a connector that revoked its own authorization.
	MethodChannelUnbound = "channels.unbound"
	// MethodFeishuMessage: FeishuMessage → {}: the owner's message in the Feishu bot's private chat, taken.
	MethodFeishuMessage = "feishu.message"
	// MethodFeishuAction: FeishuAction → the card callback's answer (botcard.CardResponse): the owner pressed one
	// of the bot's cards. Feishu waits 3 seconds for it.
	MethodFeishuAction = "feishu.card_action"
	// MethodFeishuView: {} → FeishuView, what GET /api/v1/bots/feishu shows of the bot's chat.
	MethodFeishuView = "feishu.view"
	// MethodFeishuHandoff: FeishuHandoff → {}: a group question handed to the bot, accepted (not its run).
	// An assistant that does not know it answers "unknown method", so an older one never treats the payload
	// as one of the owner's messages.
	MethodFeishuHandoff = "feishu.handoff"

	// EventInteractionAnnounced: InteractionAnnounced, one way: a new card (not a quiet task's).
	EventInteractionAnnounced = "interaction.announced"
	// EventInteractionEnded: InteractionEnded, one way: a card is over — answered (with the resolution), withdrawn,
	// expired.
	EventInteractionEnded = "interaction.ended"
	// EventSessionItems and EventSessionDelta: SessionItems / SessionDelta, one way: a conversation the assistant
	// follows (DeviceSessionFollow) has new items, or is writing text.
	EventSessionItems = "session.items"
	// EventInlineViewPage carries candidate cards from a transcript page the hub read for an app (SessionItems).
	EventInlineViewPage = "session.inline_view_page"
	EventSessionDelta   = "session.delta"
	// EventFeishuOwner: FeishuOwner, one way: the Feishu bot was paired with its owner, or unpaired.
	EventFeishuOwner = "feishu.owner"
	// EventFeishuRecalled: FeishuRecalled, one way: the owner recalled a message in the bot's private chat.
	EventFeishuRecalled = "feishu.recalled"
	// EventChannelNoted: ChannelNoted, one way: a channel's message went into a conversation through the apps' routes
	// (a voice conversation's errand: client_msg_id voice-…).
	EventChannelNoted = "channels.noted"

	// The hub's device methods for the channels' turns.
	// DeviceSessionFollow: {session_id} → {}: the conversation's new items and the text it writes come as
	// EventSessionItems / EventSessionDelta until DeviceSessionUnfollow, or until the assistant disconnects.
	DeviceSessionFollow   = "session.follow"
	DeviceSessionUnfollow = "session.unfollow"
	// DeviceSessionSendOnce: the node's session.send params (with client_msg_id) → {}: sent under the apps' once-only
	// guard — a message id already delivered is not sent again, one whose send was cut off is refused.
	DeviceSessionSendOnce = "session.send_once"

	// ClarificationProofParam carries an opaque, one-time hub- or extension-manager-issued proof on
	// session.send_once. ClarificationInteractionParam binds it to the prepared card. Both are stripped
	// before the node call; neither is an owner_input attestation supplied by the assistant.
	ClarificationProofParam       = "approval_clarification_proof"
	ClarificationInteractionParam = "approval_clarification_id"

	// DeviceAssistantAnswerAgain is a node send that must match the completed transcript tail.
	// The manager and node both use this distinct method, so an older peer cannot
	// silently ignore the guard. The hub claims the client_msg_id before forwarding.
	DeviceAssistantAnswerAgain = "assistant.answer_again"
	CodeAnswerAgainChanged     = "assistant_answer_again_changed"

	// AppFeishu is the Feishu bot's transport (the open platform, as the paired owner's bot).
	AppFeishu = "feishu"
	// FeishuStatusGet: {} → FeishuStatus.
	FeishuStatusGet = "feishu.status"
	// FeishuPost: {reply_to?, card, fallback?, uuid?, no_detour?} → {message_id}: a card (JSON 2.0) as a reply, else
	// into the owner's chat; one Feishu refuses goes again as rich text (fallback, Markdown). uuid is the caller's key
	// for a send that may be made again (a retried morning brief): the same key lands once, and a reply under it is
	// not dropped as a repeat of another with the same words. It may be made of letters, digits, ":", "_" and "-" only
	// (anything else is ignored, as if none was given) and is cut to 46 characters; without one a reply's key is made
	// of its parent and content. no_detour? makes a reply whose parent was withdrawn or deleted fail (botcard.MessageGone
	// tells why) in place of going into the chat as a message of its own.
	// chat_id? names another chat to send into (and to fall back to when the parent was withdrawn) and in_thread? puts
	// a reply in the parent's thread; the hub refuses any chat but the owner's private chat and the group chats the
	// owner opted in, and with neither field the call is what it always was.
	FeishuPost = "feishu.post"
	// FeishuText: {reply_to?, text, chat_id?, in_thread?, uuid?, no_detour?, full_answer?} → {message_id}: plain text as a reply,
	// else into the owner's chat; chat_id, in_thread, uuid (cut to 50 characters here) and no_detour are FeishuPost's.
	// One more place is open to it (#6146): a reply to a message the hub took as the owner's "@bot /ai …" in a group that
	// is not opted in (FeishuFromElsewhere), which lands under that message and nowhere else in the group (at most
	// 2000 characters, never a detour into the chat); a send into that group, or a reply to any other message in it,
	// is refused as ever. With full_answer, the owner must enable full replies, a parent is required, and the
	// answer is sent as literal rich text, or as a text attachment when too large for inline delivery.
	// A feishu.text with handoff set is a proxy reply (bots_handoff.go) and skips the usual path: handoff is the
	// hub's id, confirmed says the owner approved the draft, and the text goes under the handoff's anchor or,
	// with none, into its chat. The chat does not become one the bot may send into.
	// A feishu.text with guest set is a guest's one reply (bots_guest.go) and skips the usual path too: guest is the
	// hub's id, and the text goes under the trigger that started it, in its thread when it has one, or not at all. The
	// chat does not become one the bot may send into.
	FeishuText = "feishu.text"
	// FeishuUpdateCard: {message_id, card} → {}.
	FeishuUpdateCard = "feishu.update_card"
	// FeishuCardCreate: {card} → {card_id}: a card entity (CardKit) made of the card (JSON 2.0); FeishuPost sends it with
	// botcard.CardEntityContent(card_id) as its card, and FeishuUpdateCard still updates that message whole.
	FeishuCardCreate = "feishu.card_create"
	// FeishuCardUpdate: {card_id, card, sequence} → {}: the whole card written into an entity. FeishuCardStream:
	// {card_id, element, text, sequence} → {}: an element's whole text streamed into it (feishubot.StreamClosed tells
	// one that stopped streaming). FeishuCardSettings: {card_id, streaming, sequence} → {}: its streaming turned on or
	// off. A write's sequence must be larger than the entity's last; errors carry Feishu's code.
	FeishuCardUpdate   = "feishu.card_update"
	FeishuCardStream   = "feishu.card_stream"
	FeishuCardSettings = "feishu.card_settings"
	// FeishuPin: {message_id, exclusive?} → {}: one of the bot's messages in the owner's private chat pinned there;
	// exclusive takes off the other pins the bot made in that chat (never the owner's own) (#1961, the workbench).
	FeishuPin = "feishu.pin"
	// FeishuRecall: {message_id} → {}: one of the bot's messages in the owner's private chat withdrawn (Lark lets a
	// bot withdraw its messages for a while after sending; past that the call fails).
	FeishuRecall = "feishu.recall"
	// FeishuCotCreate: {reply_to?, chat_id?, in_thread?} → {cot_id, message_id}: an agent message (the open
	// platform's message_cot, a collapsible process) under the message replied to, in its topic when in_thread is set.
	// With guest set it is the guest turn's one agent message, under its trigger (bots_guest.go); a second one, or one
	// for a turn that has ended, is refused.
	FeishuCotCreate = "feishu.cot_create"
	// FeishuCotAppend: {cot_id, message_id, events: [botcard.CotEvent]} → {}.
	FeishuCotAppend = "feishu.cot_append"
	// FeishuCotComplete: {cot_id, message_id, reason: done | error} → {}: one that will not get its RUN_FINISHED.
	FeishuCotComplete = "feishu.cot_complete"
	// FeishuReact: {message_id, emoji} → {reaction_id}; FeishuUnreact: {message_id, reaction_id} → {}.
	FeishuReact   = "feishu.react"
	FeishuUnreact = "feishu.unreact"
	// FeishuMessages: {message_id} → {items: [feishubot.Item]}: a message read back (a quote; a merged forward with
	// the messages in it).
	FeishuMessages = "feishu.messages"
	// FeishuResource: {message_id, key, kind, machine_id, as_file?} → {data, mime} for an image at most 3 MiB,
	// or {path, mime, name} saved on the machine from the same download (over 3 MiB or as_file),
	// or {too_large: true, name} above the 50 MiB file limit. Save failures carry CodeFeishuSave.
	FeishuResource = "feishu.resource"
	// FeishuSave: {message_id, key, kind, name, machine_id} → {path}: a file (or a large image) of a message saved on
	// a machine (its temp directory, like the apps' uploads), at most 50 MB.
	FeishuSave = "feishu.save"
	// FeishuUserName: {open_id} → {name}: a user's name (the Chinese for "someone" when it can't be read; the owner's falls back to hub.json's).
	FeishuUserName = "feishu.user_name"
	// FeishuConfigure: {enabled?, features?: {id: on}} → {}: the bot switched, its feature switches changed (only
	// those given).
	FeishuConfigure = "feishu.configure"
	// FeishuTest: {} → {}: the test card to the owner.
	FeishuTest = "feishu.test"
	// FeishuHandoffEnd: {handoff, outcome} → {}: a handoff is over without a post (declined, expired, no_reply, failed).
	FeishuHandoffEnd = "feishu.handoff_end"
	// MethodFeishuGuest: FeishuGuestTurn → {}: a non-owner's @bot the policy let through, accepted (not its run).
	// An assistant that does not know it answers "unknown method", so an older one never treats the payload as one of
	// the owner's messages.
	MethodFeishuGuest = "feishu.guest"
	// FeishuGuestEnd: {guest, outcome} → {}: a guest turn is over without a post (failed, no_reply, expired).
	FeishuGuestEnd = "feishu.guest_end"
	// CodeGuestBusy: the conversation still answers its last question.
	CodeGuestBusy = "guest_busy"

	// AppChannels is the plugins' channel programs, and the list of every channel as the transport has it.
	AppChannels = "channels"
	// ChannelsList: {} → {channels: [channel.View]}: every channel without what is the assistant's (targets, the
	// plugin channels' features); the Feishu bot's features are its switches.
	ChannelsList = "channels.list"
	// ChannelsSend: {channel, ref, text, kind, reply_to?} → {message_id?}: one piece to the owner (retried with the same
	// ref); CodeCannotSend when there is nowhere to send to yet.
	ChannelsSend = "channels.send"
	// ChannelsTyping: {channel, on} → {}.
	ChannelsTyping = "channels.typing"
	// ChannelsEnable: {channel, on} → {}: the plugin (and its connector), or the Feishu bot, switched.
	ChannelsEnable = "channels.enable"
	// ChannelsUnbind: {channel} → {}: signed out, unpaired.
	ChannelsUnbind = "channels.unbind"

	// InteractionsResolve: {interaction_id, resolution, action, channel} → {ok, status}: the card answered as the user
	// answered it in channel. action is the owner's action the hub handed over with that press or message there
	// (single use; a card's is for that card only, a message's for a kind of card the channel takes a typed answer
	// for); without one, or with another channel's, the hub refuses. ok false (status as InteractionsGet's) when it is no
	// longer pending, an error when the answer does not fit it.
	InteractionsResolve = "interactions.resolve"
	// InteractionsClarify: {interaction_id, text, client_msg_id, action, channel, chat_id, message_id} → {ok, status}:
	// an owner's non-final question on a prepared approval. It consumes the same fresh, card/channel-bound owner action
	// as InteractionsResolve but never supplies or creates a Resolution; the original card remains pending.
	InteractionsClarify = "interactions.clarify"

	// CodeCannotSend: a channel has nowhere to send to yet (channel.CodeCannotSend); CodeNeedsTriageModel: a plugin
	// can't be switched on before the inbox's triage model is set (409 to the app).
	CodeCannotSend       = "cannot_send"
	CodeNeedsTriageModel = "needs_triage_model"
	// CodeTryAgain: the assistant could not take an owner's message now (what it needs of the hub did not answer):
	// the hub hands it over again, as after a cut-off call.
	CodeTryAgain = "try_again"
)

// ChannelReceive is channels.receive's params.
type ChannelReceive struct {
	Channel string          `json:"channel"`
	Message json.RawMessage `json:"message"` // channel.Message
	// Action is the owner's action: what answers a card with this message passes it (InteractionsResolve).
	Action string `json:"action,omitempty"`
}

// ChannelUnbound is channels.unbound's params.
type ChannelUnbound struct {
	Channel string `json:"channel"`
}

// FeishuMessage is feishu.message's params: im.message.receive_v1's event as Feishu sent it (the owner's, in the
// bot's private chat), and the owner's action for what this message answers (InteractionsResolve).
type FeishuMessage struct {
	Event  json.RawMessage `json:"event"`
	Action string          `json:"action,omitempty"`
	// From says where the hub took the message from when it is not the owner's private chat or a topic of a group the
	// owner opted in: FeishuFromElsewhere is the owner's "@bot /ai …" in a group that is not opted in (#6146), the only
	// thing the hub lets through from one; FeishuFromRead is the owner's "@bot /read …" from any group (#11803). "" for
	// the usual two.
	From string `json:"from,omitempty"`
}

// OwnerApprovalProof is the hub-authenticated location and exact text of an
// owner's direct reply. It is returned only after the matching one-use owner
// action is consumed; the assistant still has to bind ParentID to the exact
// pending proposal card and compare Text with the configured affirmative word.
type OwnerApprovalProof struct {
	MessageID string `json:"message_id"`
	ParentID  string `json:"parent_id"`
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
}

// FeishuHandoff is feishu.handoff's params: a group question an authorized caller handed to the bot. The
// question, the context and the names are the caller's, untrusted, and are never stored with the bot's settings.
type FeishuHandoff struct {
	ID         FeishuHandoffID `json:"id"`
	ChatID     FeishuChatID    `json:"chat_id"`
	ChatName   string          `json:"chat_name,omitempty"`
	SenderName string          `json:"sender_name,omitempty"`
	Question   string          `json:"question"`
	Context    string          `json:"context,omitempty"`
	Confirm    bool            `json:"confirm"`
	Launcher   string          `json:"launcher,omitempty"`
	By         string          `json:"by,omitempty"`
	ExpiresAt  time.Time       `json:"expires_at"`
}

// FeishuHandoffID names one proxy-reply handoff ("bh_" and hex), chosen by the manager.
type FeishuHandoffID string

// FeishuChatID is a Lark open-platform chat id (oc_…).
type FeishuChatID string

// FeishuThreadID is a Lark open-platform thread id (omt_…) or root message id (om_…).
type FeishuThreadID string

// FeishuHandoffState is the state of a handoff the hub answers with; it only ever answers once one is accepted.
type FeishuHandoffState string

const FeishuHandoffStateAccepted FeishuHandoffState = "accepted"

func (s FeishuHandoffState) Valid() bool { return s == FeishuHandoffStateAccepted }

// FeishuHandoffDelivery is how a handoff is answered: confirm (the owner approves the draft first) or direct.
type FeishuHandoffDelivery string

const (
	FeishuHandoffDeliveryConfirm FeishuHandoffDelivery = "confirm"
	FeishuHandoffDeliveryDirect  FeishuHandoffDelivery = "direct"
)

func (d FeishuHandoffDelivery) Valid() bool {
	return d == FeishuHandoffDeliveryConfirm || d == FeishuHandoffDeliveryDirect
}

// FeishuHandoffAccepted is the handoff POST /api/v1/bots/feishu/handoffs took.
type FeishuHandoffAccepted struct {
	ID        FeishuHandoffID       `json:"id"`
	State     FeishuHandoffState    `json:"state"`
	Delivery  FeishuHandoffDelivery `json:"delivery"`
	ExpiresAt string                `json:"expires_at"`
}

// FeishuHandoffResponse is the body of POST /api/v1/bots/feishu/handoffs. Replayed: a repeat of an earlier ref.
type FeishuHandoffResponse struct {
	Handoff  FeishuHandoffAccepted `json:"handoff"`
	Replayed bool                  `json:"replayed"`
}

// FeishuFromElsewhere is FeishuMessage.From of a message the hub took from a group chat that is not opted in: the owner's
// "@bot /ai …" and nothing else. The assistant opens a to-do for it in the home topic group, or in the owner's private chat when there is none (modules/lark/elsewhere.go).
const FeishuFromElsewhere = "elsewhere"

// FeishuFromRead is FeishuMessage.From of the owner's "@bot /read <task>" from a group chat, opted in or not (#11803):
// the assistant reads the group as the owner, then does the task, and answers under that message
// (modules/lark/read.go). The hub lets it through only with the read_group switch on.
const FeishuFromRead = "read"

// FeishuAction is feishu.card_action's params: card.action.trigger's event as Feishu sent it (the owner's press), and
// the owner's action for the card it presses (InteractionsResolve).
type FeishuAction struct {
	Event  json.RawMessage `json:"event"`
	Action string          `json:"action,omitempty"`
}

// ClarificationProof is the bound question returned when a one-time proof is
// consumed at the hub's final transport boundary. The opaque token itself is
// never a caller, submission, approval, or reusable owner capability.
type ClarificationProof struct {
	InteractionID string `json:"interaction_id"`
	MachineID     string `json:"machine_id"`
	SessionID     string `json:"session_id"`
	Text          string `json:"text"`
	ClientMsgID   string `json:"client_msg_id"`
}

// FeishuView is feishu.view's answer.
type FeishuView struct {
	Target string        `json:"target,omitempty"` // where the chat's messages go now
	Agents []FeishuAgent `json:"agents,omitempty"` // agents bound to threads of the chat
}

// FeishuAgent is an agent bound to a thread of the bot's chat.
type FeishuAgent struct {
	Title     string `json:"title"`
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
}

// ChannelNoted is channels.noted's params.
type ChannelNoted struct {
	Channel   string `json:"channel"`
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
}

// FeishuOwner is feishu.owner's params.
type FeishuOwner struct {
	Paired bool `json:"paired"`
	// Greeted: the hub already greeted the owner in the private chat (an app created and paired in one
	// authorization, #4347); the assistant starts the chat afresh without its own greeting.
	Greeted bool `json:"greeted,omitempty"`
}

// FeishuRecalled is feishu.recalled's params: the message the owner recalled.
type FeishuRecalled struct {
	MessageID string `json:"message_id"`
}

// FeishuFeatures are the Feishu bot's switches (settings bots.feishu, the hub's with its credentials).
type FeishuFeatures struct {
	Main     bool `json:"main"`
	Cards    bool `json:"cards"`
	AllCards bool `json:"all_cards"`
	Pushes   bool `json:"pushes"`
	Sync     bool `json:"sync"`
	Agents   bool `json:"agents"`
	Reports  bool `json:"reports"` // the main conversation's tasks are told in the chat (#2768)
	Board    bool `json:"board"`   // /tasks keeps one standing "waiting for you" card, edited in place (#6052)
	// Workbench: /home, /today and /inbox, the standing board drawn as a pinned home card, a workbench button on every
	// card, a permission field in /start and the companion bots' hooks (#1961).
	Workbench bool `json:"workbench"`
	// OnlyConclusions: a task's finished turn, waiting and interrupted states are not pushed on their own; the task list
	// and the briefing carry them, and only its conclusion card goes out (#1226).
	OnlyConclusions bool `json:"only_conclusions"`
	// ReportSteps: the task report card also carries the execution steps (a finished turn with the stage bar and what the
	// agent said, interrupted, closed), the card as it was before #3218. Off: only goal-level changes are pushed.
	ReportSteps bool `json:"report_steps"`
	// AnswerNewCard: a finished run's answer goes out as a new card and its progress card is folded, so the answer is
	// the chat's newest message and Lark notifies on it (#5396).
	AnswerNewCard bool `json:"answer_new_card"`
	// TopicCot: a topic run also shows its thinking and tools in a native agent message beside the progress card.
	TopicCot bool `json:"topic_cot"`
	// DMCot: a private-chat run also shows its thinking and tools in a native agent message beside the progress card
	// (#16847). Off by default.
	DMCot bool `json:"dm_cot"`
	// CotAnswer: a run whose agent message is allowed (dm_cot, topic_cot) gets its answer inside that message and no
	// progress or answer card; cards take over when the message cannot carry it (#16847). Off by default.
	CotAnswer bool `json:"cot_answer"`
	// ReadGroup: "@bot /read <task>" from the owner in a group reads the group before the task (#11803,
	// FeishuFromRead).
	ReadGroup bool `json:"read_group"`
	// ReadGroupDirect: a /read's answer goes straight into the group under the owner's message. Off: it goes to the
	// owner's private chat first, and into the group only when the owner presses its button (#14437).
	ReadGroupDirect bool `json:"read_group_direct"`
}

// FeishuStatus is feishu.status's answer.
type FeishuStatus struct {
	// Ready: switched on, connected and paired: the bot can post now.
	Ready    bool           `json:"ready"`
	Enabled  bool           `json:"enabled"`
	Features FeishuFeatures `json:"features"`
	Owner    string         `json:"owner,omitempty"` // the owner's open id ("" before pairing)
	// PairedAt is when the owner paired: with Owner, which pairing the chat's state belongs to (a new one, or none,
	// starts it afresh).
	PairedAt time.Time `json:"paired_at,omitzero"`
	// PublicURL is the hub's web app (a card's "Open web app").
	PublicURL string `json:"public_url,omitempty"`
	// Chats are the chats the bot may send into for the assistant (the hub's own rule for feishu.post / feishu.text):
	// the owner's private chat and the group chats the owner opted in. A thread the assistant posts into is checked
	// against it before the call, so nothing goes to a chat an agent made up.
	Chats []string `json:"chats,omitempty"`
	// HomeChat is the home topic group (#6146): the opted-in group the owner marked with "/topics home", else the first
	// one opted in; "" while none is. It is one of Chats. Where a request from a group that is not opted in opens its topic.
	HomeChat string `json:"home_chat,omitempty"`
	// TopicTodoModes holds the non-default to-do policies of opted-in groups, keyed by chat ID.
	TopicTodoModes map[string]TopicTodoMode `json:"topic_todo_modes,omitempty"`
	// TopicSpaces maps opted-in topic group chat IDs to their bound domain space slug.
	TopicSpaces map[string]string `json:"topic_spaces,omitempty"`
	// NudgeAfter is the bot's setting nudge_after (#6084): minutes a card waits unanswered before the one nudge under it,
	// 0 off. nil (a status from before the setting) is NudgeAfterDefault.
	NudgeAfter *int `json:"nudge_after,omitempty"`
	// SourceReportDelayMinutes is the preview window before replying to a source group. Zero skips the preview.
	SourceReportDelayMinutes *int `json:"source_report_delay_minutes,omitempty"`
	// SourceReportFullReply is the owner's opt-in to posting the task's final answer in its source group.
	SourceReportFullReply bool `json:"source_report_full_reply,omitempty"`
}

type TopicTodoMode string

const (
	TopicAnswerOnly  TopicTodoMode = "answer_only"
	TopicMainDecides TopicTodoMode = "main_decides"
	TopicAlwaysTodo  TopicTodoMode = "always_todo"
)

func (m TopicTodoMode) Valid() bool {
	return m == TopicAnswerOnly || m == TopicMainDecides || m == TopicAlwaysTodo
}

func (m TopicTodoMode) Effective() TopicTodoMode {
	if !m.Valid() {
		return TopicAnswerOnly
	}
	return m
}

// NudgeAfterDefault is nudge_after when never set: minutes a card of the Lark bot waits before it is nudged.
const NudgeAfterDefault = 60

// SourceReportDelayDefault is the preview window, in minutes, when it has never been set.
const SourceReportDelayDefault = 10

// InteractionAnnounced is interaction.announced's params.
type InteractionAnnounced struct {
	Interaction proto.Interaction `json:"interaction"`
}

// InteractionEnded is interaction.ended's params: Resolution when it was answered (how, for its cards), nil when it
// ended otherwise (withdrawn, expired, answered and gone).
type InteractionEnded struct {
	Interaction proto.Interaction `json:"interaction"`
	Resolution  *proto.Resolution `json:"resolution,omitempty"`
}

// SessionItems is session.items's params.
type SessionItems struct {
	MachineID string       `json:"machine_id"`
	SessionID string       `json:"session_id"`
	Items     []proto.Item `json:"items"`
}

// SessionDelta is session.delta's params: the text the conversation is writing now.
type SessionDelta struct {
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

// ChannelsReport is what the hub shows of the channels, in the snapshot (Snapshot.Channels): which channels each
// conversation's messages came from (the session lists' channels), and the conversations whose turn a channel follows
// now or answered lately (the hub's finished push leaves those out: the answer is in the channel). Small, whole every
// time; null while the channels' state could not be read (the hub keeps the last).
type ChannelsReport struct {
	Marks []ChannelMark `json:"marks"`
	Turns []ChannelTurn `json:"turns"`
}

// ChannelMark is one conversation's channels, newest first.
type ChannelMark struct {
	MachineID string   `json:"machine_id"`
	SessionID string   `json:"session_id"`
	Channels  []string `json:"channels"`
}

// ChannelTurn is a conversation a channel's turn is followed in (Answered zero) or was answered in at Answered.
type ChannelTurn struct {
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	Answered  time.Time `json:"answered,omitzero"`
}
