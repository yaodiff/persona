// Package larkhost is the seam between the core and the Feishu / Lark module's assistant layer (internal/modules/lark,
// docs/architecture/core-and-modules.md "Lark", #13404): what the core asks of the module (Layer, whose outbound
// half is the channel port Outbound) and what the module asks of the core (Host), each in terms both sides name (the
// standard library, internal/botcard and the contracts).
//
// The composition root (internal/modules/all) provides the module in the init of its file, under the module's build
// constraint; without it nothing is provided and the core reaches the owner's chat through None, a channel switched
// off: its status says neither on nor ready (no error: nothing to try again), it supports nothing, every post answers
// ErrNoChannel, and notes and thread lines are dropped.
//
// The module is provided while the program initializes, before main runs, and only read afterwards.
package larkhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/nwverse/persona/server/internal/moduleswitch"
	"github.com/nwverse/persona/server/internal/prompts"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ModuleID is the lark module's id (its module.json).
const ModuleID = "lark"

// Target says where a post or a note goes (assistantwire.ChannelTarget: the zero value is the owner's private chat).
type Target = assistantwire.ChannelTarget

// ThreadEvent is one thing to tell the chat thread of a to-do (assistantwire.ThreadEvent).
type ThreadEvent = assistantwire.ThreadEvent

// What a line in a to-do's chat thread tells (assistantwire.ThreadKind).
const (
	ThreadDecision      = assistantwire.ThreadDecision
	ThreadAgentFinished = assistantwire.ThreadAgentFinished
	ThreadAgentNeeds    = assistantwire.ThreadAgentNeeds
	ThreadTodoAccepted  = assistantwire.ThreadTodoAccepted
	ThreadTodoDone      = assistantwire.ThreadTodoDone
	ThreadTodoFailed    = assistantwire.ThreadTodoFailed
)

// Outbound is the outbound channel port: how core code reaches the owner's chat. Status and Features say what the
// channel supports now; Post, Note, Reply and UpdateCard send; ThreadPost queues a line for a to-do's chat thread (nothing
// for a to-do bound to none).
type Outbound interface {
	Status(ctx context.Context) (assistantwire.FeishuStatus, error)
	Features(ctx context.Context) (f assistantwire.FeishuFeatures, ready bool)
	// Post sends card as a reply to replyTo, else into to's chat; fallback is the rich text it goes as when the card
	// is refused, and key, when not "", makes a send that may be made again land once.
	Post(ctx context.Context, to Target, replyTo string, card *botcard.Card, fallback, key string) (messageID string, err error)
	// Note writes a line of text under replyTo (a failure is only logged); Reply does and says whether it went, with
	// key as Post's and full when the text is a task's whole answer.
	Note(ctx context.Context, to Target, replyTo, text string)
	Reply(ctx context.Context, to Target, replyTo, text, key string, full bool) error
	UpdateCard(ctx context.Context, messageID string, card *botcard.Card) error
	ThreadPost(todoID string, ev ThreadEvent)
}

// ErrNoChannel: this build has no chat channel (the lark module is left out).
var ErrNoChannel = errors.New("no chat channel in this build")

// ErrGuestGone: the node no longer has the guest conversation, so the caller starts a fresh one. The module matches
// this error by its text (modules/lark/guest.go); it cannot import this package.
var ErrGuestGone = errors.New("the guest conversation is gone")

// None is Outbound without a channel: as a bot that is switched off.
type None struct{}

func (None) Status(context.Context) (assistantwire.FeishuStatus, error) {
	return assistantwire.FeishuStatus{}, nil
}
func (None) Features(context.Context) (assistantwire.FeishuFeatures, bool) {
	return assistantwire.FeishuFeatures{}, false
}
func (None) Post(context.Context, Target, string, *botcard.Card, string, string) (string, error) {
	return "", ErrNoChannel
}
func (None) Note(context.Context, Target, string, string)                      {}
func (None) Reply(context.Context, Target, string, string, string, bool) error { return ErrNoChannel }
func (None) UpdateCard(context.Context, string, *botcard.Card) error           { return ErrNoChannel }
func (None) ThreadPost(string, ThreadEvent)                                    {}

// Turns is how the Feishu bot's runs show in the chat (the channels' sink of channel.Feishu): the progress card and
// agent message, a folded or recalled message's marks, and the answer.
type Turns interface {
	TurnProgress(ctx context.Context, r *channel.Run, took time.Duration, steps []string, live string)
	// TurnFolded: o went into a later run; owner is that run's state (Host.RunState), nil when it has none.
	TurnFolded(ctx context.Context, o *channel.Run, owner any)
	TurnRecalled(ctx context.Context, m channel.Folded)
	TurnAnswer(ctx context.Context, r *channel.Run, answer, note string, took time.Duration, final, quiet bool, end channel.End) error
	TurnProgressAfter(r *channel.Run) time.Duration
	TurnProgressIdle() time.Duration
	// Wait waits until the module's background work (the turns' workers, the chat's messages being handled) has
	// ended, or ctx ends (the service stops; tests drain).
	Wait(ctx context.Context) error
}

// Cards lays out what the core says to the chat as the bot's cards (the module's, #13404). With no module there is
// nothing to lay out: no cards and no note.
type Cards interface {
	// AnswerCards lays out a final answer as the bot's cards, each with the text it goes as when the card is refused:
	// the process panel on the first when steps is not nil, foot under the last, none for quiet. name names the button
	// of a bare address in the answer (nil: by its host).
	AnswerCards(l i18n.Lang, name func(*url.URL) string, answer string, quiet bool, steps []string, foot string) (cards []*botcard.Card, fallbacks []string)
}

// Push delivers notices and turn mirrors outside the bot's normal reply path. Without the module a
// keyed push returns nil, and there is no delivery or retry. Core records skipped copies separately.
type Push interface {
	PushKeyed(ctx context.Context, to assistantwire.SessionRef, title, body, key string, receipt assistantwire.LarkPushCopy) error
	TodoReminder(to assistantwire.SessionRef, reminder assistantwire.LarkReminder)
	AgentDone(to assistantwire.SessionRef, view func(context.Context) (assistantwire.LarkAgentDone, bool))
	Mirror(ctx context.Context, to assistantwire.LarkMirror, lang i18n.Lang, turn assistantwire.LarkMirrorTurn)
	MirrorPost(ctx context.Context, to assistantwire.LarkMirror, lang i18n.Lang, cards []*botcard.Card, fallbacks []string, key string, sent func())
}

// Asks is the interaction port: what the core tells the chat of what Persona and the agents ask (a question, a
// permission, a plan, a terminal dialog), each of which the chat shows as a card (the module's, #13404). What a card
// says of the core the module asks for as one value (Host.AskView). With no module nothing is shown and no card is
// wanted.
type Asks interface {
	// AskAnnounced: a new interaction (interaction.announced): its card is posted when the bot's switches want one
	// (AskWanted). AskPost posts it whatever the switches say (a report's decision, a card brought to the bottom).
	AskAnnounced(ix proto.Interaction)
	AskPost(ctx context.Context, ix proto.Interaction)
	AskWanted(ctx context.Context, ix proto.Interaction) bool
	// AskEnded: ix is over (answered anywhere, withdrawn, expired) and how says how; inChat: it was taken back
	// because the user wrote in the conversation, so their next words are no late answer to it.
	AskEnded(ix proto.Interaction, how string, inChat bool)
	// AskDetached: the interactions ids are about to end because their node went away; AskBack: ix is back after its
	// node reconnected. grace is how long a card outlives its node (the core's resumeGrace).
	AskDetached(ids []string, grace time.Duration)
	AskBack(ix proto.Interaction, grace time.Duration)
	// AsksCatchUp closes the cards whose interaction is over for good, as the hub's history says; pending are the
	// interactions up now (after a reconnection).
	AsksCatchUp(ctx context.Context, pending map[string]proto.Interaction)
}

// Workbench is the Lark workbench (/home, /today, /inbox and their buttons, the home card).
type Workbench interface {
	WorkbenchCommand(ctx context.Context, f assistantwire.FeishuFeatures, cmd string) bool
	WorkbenchNav(ctx context.Context, a *botcard.CardAction) *botcard.CardResponse
	WorkbenchInboxAction(ctx context.Context, a *botcard.CardAction) *botcard.CardResponse
	WorkbenchDraftCard(ctx context.Context, it assistantwire.WorkbenchItem, text string, err error, list string) *botcard.Card
	HomeCard(ctx context.Context, f assistantwire.FeishuFeatures) (card *botcard.Card, fallback, said string)
}

// Board is the standing "waiting for you" card, the pinned workbench and the companion bots (the module's, #13404): what
// the core tells them, and the notices it hands the notifications companion. With no module nothing is edited, no
// card carries the workbench button and no companion takes a notice.
type Board interface {
	// BoardChanged says something the board shows changed (a card opened or ended, a to-do entered or left "Please
	// verify", the assistant connected): the board is edited a moment later, once for all that came meanwhile.
	BoardChanged()
	// WorkbenchOn notes the workbench switch as just read; WorkbenchStamp adds its button to a card the bot posts or
	// redraws while it is on.
	WorkbenchOn(f assistantwire.FeishuFeatures, ready bool)
	WorkbenchStamp(ctx context.Context, c *botcard.Card) *botcard.Card
	// DivertNotices hands cards to the notifications companion when it takes them; false: they go to the chat.
	// An unknown kind is a defect, even when no companion takes the cards.
	DivertNotices(ctx context.Context, kind assistantwire.NoticeKind, cards []*botcard.Card, fallbacks []string) bool
}

// Handoffs is a proxy reply handed to the bot from a group (modules/lark/handoff.go). A handoff never enters the
// owner's conversations: the module runs it as a guest with one hub-owned tool, and that tool is how the answer
// reaches the bot.
type Handoffs interface {
	// Handoff accepts a group question the manager already persisted. The call returns once the handoff is kept; the
	// guest run starts afterwards.
	Handoff(ctx context.Context, p assistantwire.FeishuHandoff) error
	// HandoffTools is the one tool a proxy reply's guest lists. Empty without the module.
	HandoffTools() []proto.Tool
	// HandoffToolCall runs that tool for handoff. A bad call comes back as an error result and counts nothing.
	HandoffToolCall(ctx context.Context, handoff, name string, args json.RawMessage) proto.ToolResult
	// HandoffDecided is the owner answering the confirmation. option empty (a typed answer, a fixed response) declines.
	HandoffDecided(ctx context.Context, handoff, option string)
	// HandoffTick ends handoffs nobody finished. The core's minute sweep calls it, as NudgeTick.
	HandoffTick(ctx context.Context)
}

// Guests is a non-owner's @bot the group access policy let through (modules/lark/guest.go). It runs as a guest
// conversation, one per chat and thread, and never enters the owner's conversations.
type Guests interface {
	// Guest accepts a trigger the manager already kept. The call returns once it is kept; the run starts afterwards.
	// The conversation still answering its last question returns an error with assistantwire.CodeGuestBusy.
	Guest(ctx context.Context, p assistantwire.FeishuGuestTurn) error
	// GuestToolCall runs the guest's reply tool. own is false for any other tool, which the caller runs itself.
	GuestToolCall(ctx context.Context, key, session, name string, args json.RawMessage) (proto.ToolResult, bool)
	// GuestToolStep records one sanitized step of an owner-listed tool. The returned func finishes the step.
	GuestToolStep(ctx context.Context, key, name string) func(failed bool)
	// GuestTick ends turns nobody answered. The core's minute sweep calls it, as HandoffTick.
	GuestTick(ctx context.Context)
}

// Layer is what the core asks of the lark module: the outbound port, the Feishu turns, the cards of what is asked, the
// workbench, the board, the chat, pushes and the proxy replies (#13404).
type Layer interface {
	Outbound
	Turns
	Cards
	Push
	Asks
	Workbench
	Board
	Chat
	Handoffs
	Guests
}

// Chat is the bot's chat: the hub's events it takes (the owner's messages and presses, pairing, recalls, what the
// apps show of it), what the core asks of it, and its records (Book), which the core's cards and the briefing's
// waits still read.
type Chat interface {
	DecideTopicTodo(ctx context.Context, run channel.Run, title, notes string) (string, error)
	// Message takes the owner's message (feishu.message); Action answers a press of one of the bot's cards
	// (feishu.card_action, or a companion's), action being the owner's action for it; Owner is feishu.owner,
	// Recalled feishu.recalled and View feishu.view.
	Message(ctx context.Context, p assistantwire.FeishuMessage) error
	Action(ctx context.Context, a *botcard.CardAction, action string) *botcard.CardResponse
	Owner(ctx context.Context, p assistantwire.FeishuOwner)
	Recalled(p assistantwire.FeishuRecalled)
	View(ctx context.Context) assistantwire.FeishuView

	// StatusKept is Status kept a moment, for what looks every second; ForgetStatus drops it (the switches changed).
	StatusKept(ctx context.Context) (assistantwire.FeishuStatus, error)
	ForgetStatus()
	// Unpaired: the owner went: the chat's state and what it waited on go.
	Unpaired()
	// Route is where a message of the chat goes that replies to parent in the thread rooted at root: the target the
	// bot's message it replies to (or its thread) is bound to, else the chat's current one (Route("", "")).
	Route(root, parent string) assistantwire.ChatTarget
	// SetTarget makes the chat's messages go to t; RememberThread binds a message the bot posted to a target.
	SetTarget(t assistantwire.ChatTarget)
	RememberThread(messageID string, t assistantwire.ChatTarget, title string)
	// RememberTopicSession binds a group chat topic (chatID/threadID) to a conversation target so replies
	// in that topic continue the bound conversation.
	RememberTopicSession(ctx context.Context, chatID, threadID string, t assistantwire.ChatTarget, title string)
	// Send passes a message of the chat on to its target and starts the run that answers it.
	Send(ctx context.Context, ev *botcard.MessageEvent, content botcard.Content, t assistantwire.ChatTarget) error
	// CardsWaiting is how many cards are open; CardNudged whether interaction id's card was nudged under.
	CardsWaiting() int
	CardNudged(id string) bool
	// LegacyRuns are the runs an older hub kept in the chat's state, taken out of it (once).
	LegacyRuns(ctx context.Context) []*channel.Run
	// TopicAnchor is the message to reply to in a topic, "" when none is known.
	TopicAnchor(ctx context.Context, chatID, threadID string) string
	// NudgeTick nudges under the cards nobody answered that are due (the core's sweep runs it once a minute).
	NudgeTick(ctx context.Context)
	// Book runs f on the chat's records under the chat's lock, the state read first.
	Book(ctx context.Context, f func(assistantwire.ChatBook))
}

// Host is what the lark module asks of the core: the transport the outbound port forwards to, and what the module's
// code (the turns, the workbench, the chat's inbound path and its state) reads of the core and asks it to do. The
// module declares the same methods (internal/modules/lark's Host); each move takes code into the module and drops the
// host methods it no longer needs.
type Host interface {
	// The bot's transport, which the core keeps with the hub's feishu.* calls (the outbound port forwards to it).
	Post(ctx context.Context, to assistantwire.ChannelTarget, replyTo string, card *botcard.Card, fallback, key string) (string, error)
	Note(ctx context.Context, to assistantwire.ChannelTarget, replyTo, text string)
	UpdateCard(ctx context.Context, messageID string, card *botcard.Card) error

	// What a to-do's topic lines (thread_posts.go) read of the core: the Feishu thread the to-do is bound to (ok
	// false: none, or one of another channel), the to-do's web link, and the clock and timer the lines wait on.
	ThreadOf(todoID string) (chatID, threadID string, ok bool)
	TodoLink(web, todoID string) string
	PostNow() time.Time
	PostLater(d time.Duration, f func())

	// What the turn rendering (turn.go, cot.go, cot_worker.go, stream.go) needs of the core.
	Logf(format string, args ...any)
	RunCtx() context.Context
	// Timings are the module's times over its defaults (timings.go): a test and tuning knob of the core's
	// (assistant.Options.LarkTimings), never a user setting.
	Timings() (one map[string]time.Duration, lists map[string][]time.Duration)
	Lang(ctx context.Context) i18n.Lang
	// SendAs labels the cards posted within ctx for the log line of each card that lands.
	SendAs(ctx context.Context, kind, machine, session string) context.Context
	// Call is a call of the hub's feishu.* transport (assistantwire.Feishu*); CallApp one of another of its apps.
	Call(ctx context.Context, method string, params, out any) error
	CallApp(ctx context.Context, app, method string, params, out any) error
	PostContent(ctx context.Context, to assistantwire.ChannelTarget, replyTo, content, fallback, key string) (string, error)
	LinkNamer(ctx context.Context) func(*url.URL) string
	OpenButton(ctx context.Context, machine, session string) botcard.Button
	// Unreachable: the call did not reach the hub or did not come back; Offline: the hub is not connected.
	Unreachable(err error) bool
	Offline(err error) bool
	// Answered reports a definitive hub refusal, so a status failure does not consume a push retry.
	Answered(err error) bool
	StepParts(l i18n.Lang, it proto.Item) (verb, detail string)
	// RunProgress is what run r's progress card shows besides its steps (the channels' live state of it).
	RunProgress(r *channel.Run) assistantwire.RunProgress

	// The channels' runs, under their lock.
	Now() time.Time
	RunExt(r *channel.Run, key string) string
	RunExts(r *channel.Run, keys ...string) map[string]string
	SetRunExt(r *channel.Run, key, value string)
	// SetLiveRunExt sets a value on the live run as well when r is a copy of it (an answer works on a copy).
	SetLiveRunExt(r *channel.Run, key, value string)
	// RunState is the module's own state of live run id (made with make the first time); done closes when the run
	// ends. False when the run is not live or, with kept, no longer among the channels' runs.
	RunState(id string, kept bool, make func() any) (state any, done <-chan struct{}, ok bool)
	RunItems(id string) ([]proto.Item, bool)
	RunSteps(id string) []string
	// PersistRunExt sets ext on run id when it is still live with state and still() holds (asked under the lock):
	// live says it was, kept that the run is still among the channels' runs (and has ext now). SaveRuns saves them.
	PersistRunExt(id string, state any, still func() bool, ext map[string]string) (live, kept bool)
	SaveRuns()

	// What the workbench (workbench.go) reads of the core and asks it to do.
	WebBase(ctx context.Context) string
	Location(ctx context.Context) *time.Location
	ClipFallback(text string) string
	WorkbenchAgents(ctx context.Context, now time.Time) (assistantwire.WorkbenchCounts, []assistantwire.WorkbenchAgent)
	WorkbenchInbox() []assistantwire.WorkbenchItem
	WorkbenchItem(id string) (assistantwire.WorkbenchItem, bool)
	WorkbenchBoard(ctx context.Context, l i18n.Lang, web string, now time.Time) []assistantwire.WorkbenchBoardRow
	NextMeeting(ctx context.Context, now time.Time) *assistantwire.WorkbenchMeeting
	TodayBody(ctx context.Context) json.RawMessage
	TasksOverview(ctx context.Context) (cards []*botcard.Card, fallbacks []string)
	// BoardCard is the waiting list as a card (the core's board_rows.go), drawn as of now; said is the card without its
	// "updated at" note. FleetReady: the pending cards are read (before it a board would say nothing is waiting).
	BoardCard(ctx context.Context, now time.Time) (card *botcard.Card, fallback, said string)
	FleetReady() bool
	// InboxDone files an item (dismissed: muted along with it); InboxMute mutes what is tagged tag = value in source.
	InboxDone(ctx context.Context, id string, dismissed bool) error
	InboxMute(ctx context.Context, source, tag, value, name string) error
	DraftReply(ctx context.Context, id, instruction string, expectedVersion int, strictVersion bool) (item assistantwire.WorkbenchItem, text string, err error)
	// ReplyInbox sends text as the owner's reply to item id, once per clientID and exact rendered suggestion version;
	// filed: the item was filed meanwhile and nothing was sent.
	ReplyInbox(ctx context.Context, id, text, clientID string, expectedVersion int) (filed bool, err error)

	// The chat's state (chat_state.go): the settings row it is kept in, the chat's clock, and what the core does when
	// the chat starts afresh (it forgets the bot's runs) or the owner recalls a message (its run stops). WorkbenchOn
	// tells the core what the posts' workbench button follows; WorkbenchActive is whether it is shown.
	Setting(ctx context.Context, key string, out any) (bool, error)
	PutSetting(ctx context.Context, key string, value any) error
	ChatNow() time.Time
	ForgetTurns()
	RecallRun(messageID string)

	// Where a message of the chat goes (the core's feishu_route.go): a target as it is now and what to call it,
	// whether the bot's switches allow it, and the run that answers a message sent to it (StartRun: rt, t and the
	// owner's message; ext the run's channel values besides the reaction).
	ResolveTarget(ctx context.Context, t assistantwire.ChatTarget) (assistantwire.ChatResolved, error)
	// SessionExists reads the named session. An empty machine uses the configured assistant machine.
	SessionExists(ctx context.Context, machine, session string) bool
	TargetLabel(ctx context.Context, t assistantwire.ChatTarget) string
	Allowed(l i18n.Lang, f assistantwire.FeishuFeatures, t assistantwire.ChatTarget) error
	StartRun(ctx context.Context, rt assistantwire.ChatResolved, t assistantwire.ChatTarget, messageID, text string, images []proto.Image, probe, reaction string, ext map[string]string) error
	Hello(ctx context.Context) assistantwire.Hello
	// AnswerLanguage is the language a prompt asks the answer in, for the owner's locale ("" for the default).
	AnswerLanguage(locale string) string
	// ReminderTodo is the to-do a reply to its reminder carries (nil: gone, unreadable or kept private).
	ReminderTodo(ctx context.Context, todoID string) *prompts.BotTodo

	// What the owner's message may be besides a message to its target (message.go): "don't report back" for a
	// to-do taken in the chat, an answer to a conclusion's question or a "Please verify" card, the exact approval of
	// an owner-action proposal. Each says whether it took the message.
	NoReplyCommand(text string) bool
	ChatNoReply(ctx context.Context, ev botcard.MessageEvent) bool
	ConclusionQuestion(ctx context.Context, ev *botcard.MessageEvent, content *botcard.Content, f assistantwire.FeishuFeatures) bool
	AcceptReply(ctx context.Context, messageID, text string, media bool, parent, root string) bool
	ApproveOwnerAction(ctx context.Context, ev botcard.MessageEvent, text, action string) bool
	// ProjectReviewOwnerInput queues the owner's paired-chat message ref ("feishu:<message id>"), sent to the
	// conversation machine/session, for a configured project review that takes owner inputs; nil when none takes it.
	// An error means it could not be committed, and the message is not taken.
	ProjectReviewOwnerInput(ctx context.Context, ref, machine, session string) error

	// The commands (commands.go, models.go): stopping a conversation's turn (kind is the toast's), the lists to pick
	// a conversation or an agent from, what a /status card shows, the standing board, and the main conversation's
	// model.
	InterruptConversation(ctx context.Context, rt assistantwire.ChatResolved) (kind, message string)
	TurnPending(machine, session string) bool
	// FindInterruptibleRun returns a guard if the first matching run is watched. The guard rechecks the same run
	// and watcher after resolution. match may only read the run during the call, under the channel state lock.
	// The guard calls interrupt with the progress lock alone and stores the returned last successful stop.
	FindInterruptibleRun(match func(*channel.Run) bool) (guard func(session proto.Session, interrupt func(lastStop string) string))
	Conversations(ctx context.Context) (list []proto.Session, main string)
	ConversationName(sess proto.Session, main string) string
	Devices(ctx context.Context) []assistantwire.Device
	Device(ctx context.Context, id string) (assistantwire.Device, bool)
	Sessions(ctx context.Context, machine string) ([]proto.Session, error)
	UseHerdrTabName(ctx context.Context) bool
	SessionNamesAs(sess proto.Session, preferTab bool) (primary, secondary string)
	OpenLinkButton(ctx context.Context, web string) botcard.Button
	SessionLink(web, machine, session string, assistant bool) string
	FeishuRuns() int
	VisiblePending() []proto.Interaction
	PendingQuestion(ix proto.Interaction) string
	// MainChat is the main conversation and the machine it runs on ("" without one); MainModel what it runs on now.
	MainChat(ctx context.Context) (main, machine string)
	MainModel(ctx context.Context, machine, main string, l i18n.Lang) (launcher, model, effort string, err error)
	LauncherModels(ctx context.Context, machine, launcher string) (proto.LauncherModels, error)
	ListsModel(res proto.LauncherModels, model string) bool
	PickModel(ctx context.Context, main, machine string, pick proto.ModelPick, l i18n.Lang) error

	// The cards' presses and typed answers (card_actions.go, text_answer.go): an interaction answered, closed or
	// asked about, a card's words, a decision or acceptance pressed (kind is the toast's), a later question handled,
	// an agent started.
	EndedText(l i18n.Lang, status, fallback string) string
	ResolutionText(l i18n.Lang, ix proto.Interaction, r proto.Resolution) string
	ResolveInteraction(ctx context.Context, id string, r proto.Resolution, action string) (bool, error)
	ResolveInteractionAt(ctx context.Context, id string, r proto.Resolution, action string, expected *time.Time) (bool, error)
	ValidateClarifyQuestion(text string) error
	ClarifyInteraction(ctx context.Context, id, question, clientMsgID, action, chatID, messageID string) (bool, error)
	ClarifyInteractionAt(ctx context.Context, id, question, clientMsgID, action, chatID, messageID string, expected *time.Time) (bool, error)
	AcceptAction(ctx context.Context, a *botcard.CardAction) (kind, text string)
	LaterHandled(ctx context.Context, id string) (bool, error)
	TabName(ctx context.Context, prompt string) string
	ApplyModel(ctx context.Context, kind string, params map[string]any) error
	// StartAgent starts a coding agent on machine from the /start card (the owner's own start: unsupervised, as the
	// core's start paths list it).
	StartAgent(ctx context.Context, machine string, params map[string]any) (proto.Session, error)
	StartChannelRun(r *channel.Run)

	// The agents' cards (asks.go, ask_cards.go): what a card says of the core as one value (who asks, the task, the
	// conversation's page, the core's wording of the interaction), where it goes as the channels' runs say now, whether
	// its words are sensitive (asked only for a card the chat does not drive, with "all cards" on: it may wait for
	// the text to be tagged), and the core's live record of it (ended: interaction.ended came; pending: it is up;
	// AskPending: the interaction up under id now, which a card back after a reconnect must be).
	AskView(ctx context.Context, ix proto.Interaction) assistantwire.AskView
	AskRoute(ctx context.Context, ix proto.Interaction) assistantwire.AskRoute
	AskSensitive(ctx context.Context, ix proto.Interaction) bool
	// AskDirect admits an eligible owner-decision Assistant write approval independently of all_cards.
	AskDirect(ctx context.Context, ix proto.Interaction) bool
	AskState(id string) (ended, pending bool)
	// AskStateFor binds the terminal marker and announcement to ix's exact occurrence.
	AskStateFor(ix proto.Interaction) (ended, pending bool)
	AskPending(id string) (proto.Interaction, bool)

	// The nudges (nudge.go): a card's agent gone, a prepared card's deadline, the standing board told a card was
	// nudged, the tool a permission asks about.
	AgentGone(ix proto.Interaction) bool
	ApprovalNudgeDeadline(ix proto.Interaction) (time.Time, bool)
	IxToolName(ix proto.Interaction) string

	// The to-dos of topics and of requests from elsewhere (thread.go, thread_open.go, elsewhere.go): the core's
	// to-do, decision and question ledgers.
	SourceChannels() []string
	EnsureTodosLoaded(ctx context.Context) error
	TodoOfThread(chatID, threadID string) (assistantwire.ChatTodo, bool)
	ThreadNoReply(ctx context.Context, messageID, todoID string, to assistantwire.ChannelTarget)
	// ThreadRouteCandidates reads the to-do's active agent and opening or asking conversation, in that order.
	ThreadRouteCandidates(ctx context.Context, todoID string) []assistantwire.ChatTarget
	// TopicDecision notes a topic message as the to-do's decision (assistantwire.Decision*).
	TopicDecision(ctx context.Context, todoID, question, text, messageID string) string
	// TopicWake tells the to-do's supervisor what the owner said in its topic, when it waits for that.
	TopicWake(ctx context.Context, todoID, label, text, key string)
	AskTodoTopicAnswer(ctx context.Context, todoID, text string) (answer string, ok bool)
	SettleAskClaim(ctx context.Context, todoID string, ok bool)
	CloseAskTodo(ctx context.Context, todoID string)
	// TodoTitle is an opened to-do's title from the words (linkLabel: they are a link's label, kept as they are);
	// retitle: it is named again later.
	TodoTitle(ctx context.Context, words string, l i18n.Lang, linkLabel bool) (title string, retitle bool)
	// OpenTodo makes the to-do, binds it, records its source, has the main conversation supervise it and shows it
	// in its feed; anything failing takes it back.
	OpenTodo(ctx context.Context, t assistantwire.ChatOpenTodo) (todoID string, err error)
	// What a proxy reply asks of the core (modules/lark/handoff.go): start its guest, and ask the owner before a
	// confirm reply is posted. The guest is registered before the node starts it.
	StartHandoffGuest(ctx context.Context, handoff, text, launcher string) (machine, session string, err error)
	RaiseHandoffDecision(ctx context.Context, handoff string, req assistantwire.OutsideDecisionRequest) (id string, err error)
	WithdrawHandoffDecision(ctx context.Context, id string)
	// What a group guest asks of the core (modules/lark/guest.go): start its conversation, and send a later question
	// into the one it already has. A conversation the node no longer has comes back as ErrGuestGone.
	StartGroupGuest(ctx context.Context, group, text, launcher string, tools []string) (machine, session string, err error)
	SendGroupGuest(ctx context.Context, machine, session, turn, text string) error
}

// The Feishu turns without a channel: nothing is shown and an answer has nowhere to go (a run of the bot kept from a
// build that had it ends).
func (None) StartGroupGuest(context.Context, string, string, string, []string) (string, string, error) {
	return "", "", ErrNoChannel
}
func (None) SendGroupGuest(context.Context, string, string, string, string) error {
	return ErrNoChannel
}

func (None) TurnProgress(context.Context, *channel.Run, time.Duration, []string, string) {}
func (None) TurnFolded(context.Context, *channel.Run, any)                               {}
func (None) TurnRecalled(context.Context, channel.Folded)                                {}
func (None) TurnAnswer(context.Context, *channel.Run, string, string, time.Duration, bool, bool, channel.End) error {
	return nil
}
func (None) TurnProgressAfter(*channel.Run) time.Duration { return channel.ProgressAfter }
func (None) TurnProgressIdle() time.Duration              { return channel.ProgressIdle }
func (None) Wait(context.Context) error                   { return nil }

// No cards without a channel.
func (None) AnswerCards(i18n.Lang, func(*url.URL) string, string, bool, []string, string) ([]*botcard.Card, []string) {
	return nil, nil
}
func (None) PushKeyed(context.Context, assistantwire.SessionRef, string, string, string, assistantwire.LarkPushCopy) error {
	return nil
}
func (None) TodoReminder(assistantwire.SessionRef, assistantwire.LarkReminder) {}
func (None) AgentDone(assistantwire.SessionRef, func(context.Context) (assistantwire.LarkAgentDone, bool)) {
}
func (None) Mirror(context.Context, assistantwire.LarkMirror, i18n.Lang, assistantwire.LarkMirrorTurn) {
}
func (None) MirrorPost(context.Context, assistantwire.LarkMirror, i18n.Lang, []*botcard.Card, []string, string, func()) {
}

// No board without a channel: nothing is edited, no card carries the workbench button, no companion takes a notice.
func (None) BoardChanged()                                                   {}
func (None) WorkbenchOn(assistantwire.FeishuFeatures, bool)                  {}
func (None) WorkbenchStamp(_ context.Context, c *botcard.Card) *botcard.Card { return c }
func (None) DivertNotices(_ context.Context, kind assistantwire.NoticeKind, _ []*botcard.Card, _ []string) bool {
	kind.RequireKnown()
	return false
}

// No cards for what is asked without a channel: none is wanted, none is posted.
func (None) AskAnnounced(proto.Interaction)                            {}
func (None) AskPost(context.Context, proto.Interaction)                {}
func (None) AskWanted(context.Context, proto.Interaction) bool         { return false }
func (None) AskEnded(proto.Interaction, string, bool)                  {}
func (None) AskDetached([]string, time.Duration)                       {}
func (None) AskBack(proto.Interaction, time.Duration)                  {}
func (None) AsksCatchUp(context.Context, map[string]proto.Interaction) {}

// The chat without a channel: a bot switched off. Nothing is taken or answered, nothing waits, no target is set, and
// its records are empty: what is kept in them is dropped.
func (None) Message(context.Context, assistantwire.FeishuMessage) error { return nil }
func (None) DecideTopicTodo(context.Context, channel.Run, string, string) (string, error) {
	return "", ErrNoChannel
}
func (None) Action(context.Context, *botcard.CardAction, string) *botcard.CardResponse {
	return nil
}
func (None) Owner(context.Context, assistantwire.FeishuOwner) {}
func (None) Recalled(assistantwire.FeishuRecalled)            {}
func (None) View(context.Context) assistantwire.FeishuView    { return assistantwire.FeishuView{} }
func (None) StatusKept(context.Context) (assistantwire.FeishuStatus, error) {
	return assistantwire.FeishuStatus{}, nil
}
func (None) ForgetStatus() {}
func (None) Unpaired()     {}
func (None) Route(string, string) assistantwire.ChatTarget {
	return assistantwire.ChatTarget{Kind: assistantwire.ChatMain}
}
func (None) SetTarget(assistantwire.ChatTarget)                      {}
func (None) RememberThread(string, assistantwire.ChatTarget, string) {}
func (None) RememberTopicSession(context.Context, string, string, assistantwire.ChatTarget, string) {
}
func (None) Send(context.Context, *botcard.MessageEvent, botcard.Content, assistantwire.ChatTarget) error {
	return ErrNoChannel
}
func (None) CardsWaiting() int                                      { return 0 }
func (None) CardNudged(string) bool                                 { return false }
func (None) LegacyRuns(context.Context) []*channel.Run              { return nil }
func (None) TopicAnchor(context.Context, string, string) string     { return "" }
func (None) NudgeTick(context.Context)                              {}
func (None) Book(_ context.Context, f func(assistantwire.ChatBook)) { f(noBook{}) }

// Proxy replies without a channel: nothing is taken, and the guest lists no tool.
func (None) Handoff(context.Context, assistantwire.FeishuHandoff) error { return ErrNoChannel }
func (None) HandoffTools() []proto.Tool                                 { return nil }
func (None) HandoffToolCall(context.Context, string, string, json.RawMessage) proto.ToolResult {
	return proto.TextResult("this request is closed; end your turn", true)
}
func (None) HandoffDecided(context.Context, string, string) {}
func (None) HandoffTick(context.Context)                    {}

// Group guests without a channel: nothing is taken, and a tool call is not the module's.
func (None) Guest(context.Context, assistantwire.FeishuGuestTurn) error { return ErrNoChannel }
func (None) GuestToolCall(context.Context, string, string, string, json.RawMessage) (proto.ToolResult, bool) {
	return proto.ToolResult{}, false
}
func (None) GuestToolStep(context.Context, string, string) func(bool) {
	return func(bool) {}
}
func (None) GuestTick(context.Context) {}

// noBook is the records of a chat switched off: none, and nothing kept.
type noBook struct{}

func (noBook) Loaded() bool                               { return false }
func (noBook) Card(string) (assistantwire.ChatCard, bool) { return assistantwire.ChatCard{}, false }
func (noBook) KeepCard(string, *assistantwire.ChatCard)   {}
func (noBook) Cards() map[string]assistantwire.ChatCard   { return nil }
func (noBook) CardRef(string) (assistantwire.ChatCardRef, bool) {
	return assistantwire.ChatCardRef{}, false
}
func (noBook) ProposalFor(string) string { return "" }
func (noBook) Current() assistantwire.ChatTarget {
	return assistantwire.ChatTarget{Kind: assistantwire.ChatMain}
}
func (noBook) Board() *assistantwire.ChatBoardRef         { return nil }
func (noBook) SetBoard(*assistantwire.ChatBoardRef)       {}
func (noBook) Pairing() *string                           { return nil }
func (noBook) Companions() []assistantwire.CompanionBot   { return nil }
func (noBook) SetCompanions([]assistantwire.CompanionBot) {}

// The workbench without a channel: no command is one of its, no button answers, no home card.
func (None) WorkbenchCommand(context.Context, assistantwire.FeishuFeatures, string) bool {
	return false
}
func (None) WorkbenchNav(context.Context, *botcard.CardAction) *botcard.CardResponse { return nil }
func (None) WorkbenchInboxAction(context.Context, *botcard.CardAction) *botcard.CardResponse {
	return nil
}
func (None) WorkbenchDraftCard(context.Context, assistantwire.WorkbenchItem, string, error, string) *botcard.Card {
	return nil
}
func (None) HomeCard(context.Context, assistantwire.FeishuFeatures) (*botcard.Card, string, string) {
	return botcard.NewCard("", "", ""), "", ""
}

var port = moduleswitch.NewPort[func(Host) Layer]("larkhost")

// Provide provides the module under its id: new makes its layer for one manager's host (init only; twice panics).
func Provide(module string, new func(Host) Layer) { port.Provide(module, new) }

// InBuild says whether the lark module is in this build and s has it on.
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

type layerKey struct{}

// WithLayer puts the manager's layer on ctx, for the module's hub routes: a route of a module is served by a handler
// the kernel holds for every manager in the process, so the request tells which manager's layer it is for. layer is
// asked when a handler wants it.
func WithLayer(ctx context.Context, layer func() Layer) context.Context {
	return context.WithValue(ctx, layerKey{}, layer)
}

// LayerFrom is the layer WithLayer put on ctx; None when there is none.
func LayerFrom(ctx context.Context) Layer {
	if f, ok := ctx.Value(layerKey{}).(func() Layer); ok {
		return f()
	}
	return None{}
}
