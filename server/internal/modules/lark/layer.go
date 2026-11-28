package lark

import (
	"context"
	"encoding/json"
	"net/url"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Host is what the module's assistant layer needs of the core: the assistant's Service, through
// internal/larkhost.Host, which has the same methods (internal/modules/all checks it). Each takes and gives only what
// both sides can name: the standard library's, internal/botcard's, internal/channel's and the contract packages'.
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
	// FindInterruptibleRun rechecks match under the channel state lock and calls interrupt under the progress lock alone.
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
	// What a proxy reply asks of the core (handoff.go): start its guest, and ask the owner before a confirm reply
	// is posted. The guest is registered before the node starts it.
	StartHandoffGuest(ctx context.Context, handoff, text, launcher string) (machine, session string, err error)
	RaiseHandoffDecision(ctx context.Context, handoff string, req assistantwire.OutsideDecisionRequest) (id string, err error)
	WithdrawHandoffDecision(ctx context.Context, id string)
	// What a group guest asks of the core (guest.go): start its conversation, and send a later question into the one
	// it already has. A conversation the node no longer has returns an error whose text is guestGone (guest.go).
	StartGroupGuest(ctx context.Context, group, text, launcher string, tools []string) (machine, session string, err error)
	SendGroupGuest(ctx context.Context, machine, session, turn, text string) error
}

// Layer is the module's assistant layer, what the core asks of it (internal/larkhost.Layer): the outbound channel port
// through which core code reaches the owner's chat, and the Feishu turns (turn.go).
type Layer struct {
	h     Host
	turns *turns
}

// New is the layer of one manager's host.
func New(h Host) *Layer { return &Layer{h: h, turns: newTurns(h)} }

// Status is the bot as the hub has it now: on and paired, its switches.
func (l *Layer) Status(ctx context.Context) (assistantwire.FeishuStatus, error) {
	return l.chat().feishuStatus(ctx)
}

// Features are the bot's switches and whether it can post now.
func (l *Layer) Features(ctx context.Context) (assistantwire.FeishuFeatures, bool) {
	return l.chat().feishuFeatures(ctx)
}

// Post sends a card as a reply to replyTo, else into to's chat.
func (l *Layer) Post(ctx context.Context, to assistantwire.ChannelTarget, replyTo string, card *botcard.Card, fallback, key string) (string, error) {
	return l.h.Post(ctx, to, replyTo, card, fallback, key)
}

// Note writes a line of text under replyTo; a failure is only logged.
func (l *Layer) Note(ctx context.Context, to assistantwire.ChannelTarget, replyTo, text string) {
	l.h.Note(ctx, to, replyTo, text)
}

// Reply writes a line of text under replyTo and says whether it went.
func (l *Layer) Reply(ctx context.Context, to assistantwire.ChannelTarget, replyTo, text, key string, full bool) error {
	return l.chat().botReplyTo(ctx, to, replyTo, text, key, full)
}

// AnswerCards lays out an answer as the bot's cards, each with the text it goes as when the card is refused.
func (l *Layer) AnswerCards(lang i18n.Lang, name func(*url.URL) string, answer string, quiet bool, steps []string, foot string) ([]*botcard.Card, []string) {
	return answerCardParts(answerCards(lang, name, answer, quiet, steps, foot))
}

func answerCardParts(acs []botAnswerCard) ([]*botcard.Card, []string) {
	cards, fallbacks := make([]*botcard.Card, len(acs)), make([]string, len(acs))
	for i, ac := range acs {
		cards[i], fallbacks[i] = ac.card, ac.fallback
	}
	return cards, fallbacks
}

// UpdateCard edits a card the bot sent.
func (l *Layer) UpdateCard(ctx context.Context, messageID string, card *botcard.Card) error {
	return l.h.UpdateCard(ctx, messageID, card)
}

// ThreadPost queues a line for a to-do's chat thread (thread_posts.go): nothing for a to-do bound to none.
func (l *Layer) ThreadPost(todoID string, ev assistantwire.ThreadEvent) {
	l.turns.sink().s.threadPost(todoID, threadPostFromPort(ev))
}

// TurnProgress shows a run's progress: its progress card, and its agent message in a topic.
func (l *Layer) TurnProgress(ctx context.Context, r *channel.Run, took time.Duration, steps []string, live string) {
	l.turns.sink().progress(ctx, r, took, steps, live)
}

// TurnFolded closes the progress of run o, folded into a later message; owner is the later run's state (RunState),
// nil when it has none.
func (l *Layer) TurnFolded(ctx context.Context, o *channel.Run, owner any) {
	p, _ := owner.(*runLive)
	l.turns.sink().folded(ctx, o, p)
}

// TurnRecalled takes the marks off a recalled message that was folded into a run.
func (l *Layer) TurnRecalled(ctx context.Context, m channel.Folded) { l.turns.sink().recalled(ctx, m) }

// TurnAnswer sends a run's answer (channels_runs.go's channelSink.answer).
func (l *Layer) TurnAnswer(ctx context.Context, r *channel.Run, answer, note string, took time.Duration, final, quiet bool, end channel.End) error {
	return l.turns.sink().answer(ctx, r, answer, note, took, final, quiet, end)
}

// Wait waits until the layer's background work (the agent messages' workers, the chat's messages being handled) has
// ended, or ctx ends.
func (l *Layer) Wait(ctx context.Context) error { return l.turns.wait(ctx) }

// TurnProgressAfter and TurnProgressIdle are when the first progress card goes out and how long it may stay unchanged.
func (l *Layer) TurnProgressAfter(r *channel.Run) time.Duration {
	return l.turns.sink().progressAfter(r)
}
func (l *Layer) TurnProgressIdle() time.Duration { return l.turns.sink().progressIdle() }

// WorkbenchCommand answers /home, /today and /inbox with the workbench on; false when cmd is none of them.
func (l *Layer) WorkbenchCommand(ctx context.Context, f assistantwire.FeishuFeatures, cmd string) bool {
	return l.turns.sink().s.workbenchCommand(ctx, f, cmd)
}

// WorkbenchNav answers a workbench button ({a: nav}).
func (l *Layer) WorkbenchNav(ctx context.Context, a *botcard.CardAction) *botcard.CardResponse {
	return l.turns.sink().s.workbenchNav(ctx, a)
}

// WorkbenchInboxAction answers an Inbox button of the workbench ({a: ib}).
func (l *Layer) WorkbenchInboxAction(ctx context.Context, a *botcard.CardAction) *botcard.CardResponse {
	return l.turns.sink().s.workbenchInboxAction(ctx, a)
}

// WorkbenchDraftCard renders the same draft card as the action path for an already captured item snapshot.
func (l *Layer) WorkbenchDraftCard(ctx context.Context, it assistantwire.WorkbenchItem, text string, err error, list string) *botcard.Card {
	return l.turns.sink().s.draftCard(ctx, it, text, err, list)
}

// HomeCard is the workbench's home card; said is the card without its "updated at" note.
func (l *Layer) HomeCard(ctx context.Context, f assistantwire.FeishuFeatures) (card *botcard.Card, fallback, said string) {
	return l.turns.sink().s.homeCard(ctx, f)
}

// chat is the layer as the chat's code calls it.
func (l *Layer) chat() core { return l.turns.sink().s }

// ---- the chat's inbound path: the hub's bus hands these over ----

// Message takes the owner's message to the bot (feishu.message): once, and in the background, one at a time in the
// order they came.
func (l *Layer) Message(ctx context.Context, p assistantwire.FeishuMessage) error {
	return l.chat().FeishuMessage(ctx, p)
}

// Action answers a press of one of the bot's cards (feishu.card_action, or a companion's); action is the owner's
// action for the card it presses.
func (l *Layer) Action(ctx context.Context, a *botcard.CardAction, action string) *botcard.CardResponse {
	return l.chat().botCardAction(ctx, a, action)
}

// Owner: the bot was paired or unpaired (feishu.owner).
func (l *Layer) Owner(ctx context.Context, p assistantwire.FeishuOwner) { l.chat().FeishuOwner(ctx, p) }

// Recalled: the owner recalled a message in the bot's chat (feishu.recalled).
func (l *Layer) Recalled(p assistantwire.FeishuRecalled) { l.chat().FeishuRecalled(p) }

// View is where the chat's messages go now, and the agents bound to its threads (feishu.view).
func (l *Layer) View(ctx context.Context) assistantwire.FeishuView { return l.chat().FeishuView(ctx) }

// ---- what the core asks of the chat ----

// StatusKept is Status kept a moment: for what looks every second.
func (l *Layer) StatusKept(ctx context.Context) (assistantwire.FeishuStatus, error) {
	return l.chat().feishuStatusKept(ctx)
}

// ForgetStatus drops the status kept (the bot's switches changed).
func (l *Layer) ForgetStatus() { l.chat().feishuForgetStatus() }

// Unpaired: the owner went (from the channels' page): the chat's state and what it waited on go.
func (l *Layer) Unpaired() { l.chat().feishuUnpaired() }

// Route is where a message replying to parent in the thread rooted at root goes.
func (l *Layer) Route(root, parent string) assistantwire.ChatTarget {
	return l.chat().botTargetFor(root, parent)
}

// SetTarget makes the chat's messages go to t.
func (l *Layer) SetTarget(t assistantwire.ChatTarget) { l.chat().setBotCurrent(t) }

// RememberThread binds a message the bot posted to a target: replying to it talks to that target.
func (l *Layer) RememberThread(messageID string, t assistantwire.ChatTarget, title string) {
	l.chat().rememberBotThread(messageID, t, title)
}

// RememberTopicSession binds a group chat topic to a conversation target so follow-up replies route back to it.
func (l *Layer) RememberTopicSession(ctx context.Context, chatID, threadID string, t assistantwire.ChatTarget, title string) {
	l.chat().rememberTopicSession(ctx, chatID, threadID, t, title)
}

// Send passes a message of the chat on to its target and starts the run that answers it.
func (l *Layer) Send(ctx context.Context, ev *botcard.MessageEvent, content botcard.Content, t assistantwire.ChatTarget) error {
	return l.chat().botSend(ctx, ev, content, t)
}

// CardsWaiting is how many cards asking something are open in the chat.
func (l *Layer) CardsWaiting() int { return l.chat().feishuCardsWaiting() }

// CardNudged: the card asking interaction id was nudged under.
func (l *Layer) CardNudged(id string) bool { return l.chat().cardNudged(id) }

// LegacyRuns are the runs an older hub kept in the chat's state, taken out of it (once).
func (l *Layer) LegacyRuns(ctx context.Context) []*channel.Run {
	return l.chat().takeLegacyBotRuns(ctx)
}

// TopicAnchor is the message to reply to in a topic, "" when none is known.
func (l *Layer) TopicAnchor(ctx context.Context, chatID, threadID string) string {
	return l.chat().topicMessage(ctx, chatID, threadID)
}

// NudgeTick nudges under the cards nobody answered that are due.
func (l *Layer) NudgeTick(ctx context.Context) { l.chat().nudgeTick(ctx) }

// Book runs f on the chat's records under the chat's lock, the state read first (the core's cards, standing board
// and companion bots).
func (l *Layer) Book(ctx context.Context, f func(assistantwire.ChatBook)) {
	l.chat().book(ctx, func(b chatBook) { f(b) })
}

// ---- the interaction port: what the core tells the chat of what Persona and the agents ask (asks.go) ----

// AskAnnounced: a new interaction (interaction.announced): its card is posted when the bot's switches want one.
func (l *Layer) AskAnnounced(ix proto.Interaction) { l.chat().feishuInteraction(ix) }

// AskPost posts ix's card whatever the cards switches say, once, unless it ended meanwhile.
func (l *Layer) AskPost(ctx context.Context, ix proto.Interaction) {
	l.chat().postInteractionCard(ctx, ix)
}

// AskWanted: the bot posts a card for ix when it is announced.
func (l *Layer) AskWanted(ctx context.Context, ix proto.Interaction) bool {
	return l.chat().feishuCardWanted(ctx, ix)
}

// AskEnded: ix is over (answered anywhere, withdrawn, expired), how says how: its card says so. inChat: it was taken
// back because the user wrote in the conversation.
func (l *Layer) AskEnded(ix proto.Interaction, how string, inChat bool) {
	l.chat().feishuInteractionEnded(ix, how, inChat)
}

// AskDetached: the cards ids are about to end because their node went away; they are kept for grace in case they
// come back (AskBack).
func (l *Layer) AskDetached(ids []string, grace time.Duration) { l.chat().feishuDetach(ids, grace) }

// AskBack: ix is back on the apps after its node reconnected within grace: its card asks again.
func (l *Layer) AskBack(ix proto.Interaction, grace time.Duration) {
	l.chat().feishuCardBack(ix, grace)
}

// AsksCatchUp closes the cards whose interaction is over for good; pending are the interactions up now.
func (l *Layer) AsksCatchUp(ctx context.Context, pending map[string]proto.Interaction) {
	l.chat().closeEndedBotCards(ctx, pending, true)
}

// AskCard and AnsweredCard are the card asking ix and the card it becomes once over, as posted: for the assistant's
// black-box tests of what the cards say of the core. Not part of the port (larkhost.Asks).
func (l *Layer) AskCard(ctx context.Context, ix proto.Interaction) *botcard.Card {
	s := l.chat()
	return interactionCard(s.botLang(ctx), ix, s.h.AskView(ctx, ix))
}

func (l *Layer) AnsweredCard(ctx context.Context, ix proto.Interaction, how string) *botcard.Card {
	return l.chat().answeredCard(ctx, ix, how)
}
