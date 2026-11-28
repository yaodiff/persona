package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The Feishu bot's chat is the lark module's (its chat_state.go, message.go, commands.go, card_actions.go and the
// rest, #13404): its state (bots.feishu.state), the owner's messages and presses, the commands, the nudges, the topics'
// to-dos. The hub's bus hands its events to the Service (bus.go), which passes them on through larkhost.Chat; the core's
// cards, standing board and companion bots read and write the chat's records through Chat.Book. What the module asks of
// the core for it is served below (larkHost).

// botTarget is where a message of the chat goes (assistantwire.ChatTarget): the main conversation, another Persona
// conversation, or a coding agent.
type botTarget = assistantwire.ChatTarget

const (
	targetMain    = assistantwire.ChatMain
	targetPersona = assistantwire.ChatPersona
	targetAgent   = assistantwire.ChatAgent
)

// botCard is a card asking an interaction, open in the chat (assistantwire.ChatCard).
type botCard = assistantwire.ChatCard

// ---- the hub's events, handed to the module ----

// FeishuMessage is feishu.message: the owner's message in the bot's chat.
func (s *Service) FeishuMessage(ctx context.Context, p FeishuMessage) error {
	return s.larkModule().Message(ctx, p)
}

// FeishuAction is feishu.card_action: the owner pressed one of the bot's cards (the hub checked it is the owner); the
// answer is the toast and the card to show instead. Feishu waits 3 seconds for it.
func (s *Service) FeishuAction(ctx context.Context, p FeishuAction) (*botcard.CardResponse, error) {
	var a botcard.CardAction
	if err := json.Unmarshal(p.Event, &a); err != nil {
		return nil, badRequest(err)
	}
	resp := s.larkModule().Action(ctx, &a, p.Action)
	if resp != nil && resp.Card != nil {
		if c, ok := resp.Card.Data.(*botcard.Card); ok {
			s.workbenchStamp(ctx, c) // the card drawn in its place keeps its way back to the workbench
		}
	}
	return resp, nil
}

// FeishuOwner is feishu.owner: the bot was paired or unpaired.
func (s *Service) FeishuOwner(ctx context.Context, p FeishuOwner) { s.larkModule().Owner(ctx, p) }

// FeishuRecalled is feishu.recalled: the owner recalled a message in the bot's chat.
func (s *Service) FeishuRecalled(p FeishuRecalled) { s.larkModule().Recalled(p) }

// FeishuView is feishu.view: where the chat's messages go now, and the agents bound to its threads.
func (s *Service) FeishuView(ctx context.Context) FeishuView { return s.larkModule().View(ctx) }

// ---- the channels' page ----

// feishuChannelTarget is the Feishu channel's target as the channels' page shows it.
func (s *Service) feishuChannelTarget(ctx context.Context) channel.Target {
	cur := s.larkModule().Route("", "")
	t := channel.Target{Kind: channel.TargetMain}
	switch cur.Kind {
	case targetPersona:
		t = channel.Target{Kind: channel.TargetConversation, MachineID: cur.MachineID, SessionID: cur.SessionID}
		if cur.SessionID == "" {
			t = channel.Target{Kind: channel.TargetNew}
		}
	case targetAgent:
		t = channel.Target{Kind: channel.TargetAgent, MachineID: cur.MachineID, SessionID: cur.SessionID}
	}
	if t.Kind != channel.TargetMain {
		t.Title = s.targetLabel(ctx, cur)
	}
	return t
}

// updateFeishuChannel maps the channel's settings onto the bot's: its switch and features are the hub's
// (feishu.configure), where its messages go is the chat's.
func (s *Service) updateFeishuChannel(ctx context.Context, enabled *bool, target *channel.Target, features map[string]bool) error {
	if enabled != nil || len(features) > 0 {
		p := map[string]any{}
		if enabled != nil {
			p["enabled"] = *enabled
		}
		if len(features) > 0 {
			p["features"] = features
		}
		if err := s.appCall(ctx, AppFeishu, FeishuConfigure, p, nil); err != nil {
			return err
		}
		s.larkModule().ForgetStatus()
	}
	if target != nil {
		t := botTarget{Kind: targetMain}
		switch target.Kind {
		case channel.TargetNew:
			t = botTarget{Kind: targetPersona}
		case channel.TargetConversation:
			t = botTarget{Kind: targetPersona, MachineID: target.MachineID, SessionID: target.SessionID}
		}
		s.larkModule().SetTarget(t)
	}
	return nil
}

// ---- the nudges under the bot's cards nobody answered ----

// nudgeEvery is how often the bot's cards are looked at for a nudge (the lark module's nudge.go): the reply reminder's.
const nudgeEvery = replyEvery

// nudgeLoop runs the chat's nudge sweep every nudgeEvery until ctx ends.
func (s *Service) nudgeLoop(ctx context.Context) {
	t := time.NewTicker(nudgeEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cctx, cancel := context.WithTimeout(ctx, time.Minute)
			s.larkModule().NudgeTick(cctx)
			s.larkModule().HandoffTick(cctx)
			s.larkModule().GuestTick(cctx)
			cancel()
		}
	}
}

// ---- the chat's clock ----

// larkNow is the chat's clock: the time now, or the one a test set.
func (s *Service) larkNow() time.Time {
	s.lark.mu.Lock()
	now := s.lark.now
	s.lark.mu.Unlock()
	if now != nil {
		return now()
	}
	return time.Now()
}

// ---- what the module's chat asks of the core (larkhost.Host) ----

func (h larkHost) Setting(ctx context.Context, key string, out any) (bool, error) {
	st := h.s.store()
	if st == nil {
		return false, nil
	}
	return st.Setting(ctx, key, out)
}

func (h larkHost) PutSetting(ctx context.Context, key string, value any) error {
	st := h.s.store()
	if st == nil {
		return nil
	}
	return st.PutSetting(ctx, key, value)
}

func (h larkHost) ChatNow() time.Time { return h.s.larkNow() }

func (h larkHost) CallApp(ctx context.Context, app, method string, params, out any) error {
	return h.s.appCall(ctx, app, method, params, out)
}
func (h larkHost) ForgetTurns() { h.s.forgetChannelTurns(channel.Feishu) }

func (h larkHost) RecallRun(messageID string) {
	h.s.recallChannelRun(channel.Feishu, messageID, channel.ClientMsgID(channel.Feishu, messageID))
}

func (h larkHost) ResolveTarget(ctx context.Context, t botTarget) (assistantwire.ChatResolved, error) {
	rt, err := h.s.resolveBotTarget(ctx, t)
	return chatResolved(rt), err
}

func (h larkHost) SessionExists(ctx context.Context, machine, session string) bool {
	if machine == "" {
		machine = h.s.settings(ctx).MachineID
	}
	_, ok := h.s.session(ctx, machine, session)
	return ok
}

// chatResolved is a resolved target as the module names it.
func chatResolved(rt resolvedTarget) assistantwire.ChatResolved {
	return assistantwire.ChatResolved{Machine: rt.machine, MachineName: rt.machineName, Session: rt.session, Label: rt.label,
		Secondary: rt.secondary, Main: rt.main, IsNew: rt.isNew}
}

func (h larkHost) TargetLabel(ctx context.Context, t botTarget) string {
	return h.s.targetLabel(ctx, t)
}

func (h larkHost) Allowed(l i18n.Lang, f FeishuFeatures, t botTarget) error {
	return botAllowed(l, f, t)
}

func (h larkHost) StartRun(ctx context.Context, rt assistantwire.ChatResolved, t botTarget, messageID, text string, images []proto.Image, probe, reaction string, ext map[string]string) error {
	r := resolvedTarget{machine: rt.Machine, machineName: rt.MachineName, session: rt.Session, label: rt.Label, secondary: rt.Secondary,
		main: rt.Main, isNew: rt.IsNew}
	return h.s.startBotRun(ctx, r, t, messageID, text, images, probe, reaction, ext)
}

func (h larkHost) Hello(ctx context.Context) Hello        { return h.s.hello(ctx) }
func (h larkHost) AnswerLanguage(locale string) string    { return answerLanguage(locale) }
func (h larkHost) NoReplyCommand(text string) bool        { return noReplyCommand(text) }
func (h larkHost) IxToolName(ix proto.Interaction) string { return ixToolName(ix) }

func (h larkHost) ReminderTodo(ctx context.Context, todoID string) *prompts.BotTodo {
	todo, ok, err := h.s.todo(ctx, todoID)
	if err != nil {
		h.s.logf("bots: reminder reply: todo %s: %v", todoID, err)
		return nil
	}
	if !ok || h.s.todoPolicy(ctx, todo).Redact {
		return nil
	}
	return &prompts.BotTodo{ID: todo.ID, Title: clip(todo.Title, 500), Notes: clip(todo.Notes, 4000)}
}

func (h larkHost) ChatNoReply(ctx context.Context, ev botcard.MessageEvent) bool {
	return h.s.chatNoReply(ctx, ev)
}

func (h larkHost) ConclusionQuestion(ctx context.Context, ev *botcard.MessageEvent, content *botcard.Content, f FeishuFeatures) bool {
	return h.s.conclusionQuestion(ctx, ev, content, f)
}

func (h larkHost) AcceptReply(ctx context.Context, messageID, text string, media bool, parent, root string) bool {
	return h.s.acceptReply(ctx, messageID, text, media, parent, root)
}

func (h larkHost) ApproveOwnerAction(ctx context.Context, ev botcard.MessageEvent, text, action string) bool {
	return h.s.botApproveOwnerAction(ctx, ev, text, action)
}

func (h larkHost) ProjectReviewOwnerInput(ctx context.Context, ref, machine, session string) error {
	return h.s.projectReviewFeishuInput(ctx, ref, SessionRef{MachineID: machine, SessionID: session})
}

// ---- the commands ----

func (h larkHost) InterruptConversation(ctx context.Context, rt assistantwire.ChatResolved) (string, string) {
	out := h.s.interruptBotConversation(ctx, resolvedTarget{machine: rt.Machine, machineName: rt.MachineName, session: rt.Session,
		label: rt.Label, secondary: rt.Secondary, main: rt.Main, isNew: rt.IsNew})
	return out.kind, out.message
}

func (h larkHost) TurnPending(machine, session string) bool { return h.s.turnPending(machine, session) }

func (h larkHost) Conversations(ctx context.Context) ([]proto.Session, string) {
	return h.s.conversations(ctx, false, "")
}

func (h larkHost) ConversationName(sess proto.Session, main string) string {
	return conversationName(sess, main)
}

func (h larkHost) Devices(ctx context.Context) []Device { return h.s.devices(ctx) }

func (h larkHost) Device(ctx context.Context, id string) (Device, bool) { return h.s.device(ctx, id) }

func (h larkHost) Sessions(ctx context.Context, machine string) ([]proto.Session, error) {
	return h.s.sessions(ctx, machine)
}

func (h larkHost) UseHerdrTabName(ctx context.Context) bool { return h.s.useHerdrTabName(ctx) }

func (h larkHost) SessionNamesAs(sess proto.Session, preferTab bool) (string, string) {
	return feishuSessionNames(sess, preferTab)
}

func (h larkHost) OpenLinkButton(ctx context.Context, web string) botcard.Button {
	return h.s.openLinkButton(ctx, web)
}

func (h larkHost) SessionLink(web, machine, session string, assistant bool) string {
	return sessionLink(web, machine, session, assistant)
}

func (h larkHost) FeishuRuns() int                             { return len(h.s.channelRuns(channel.Feishu)) }
func (h larkHost) VisiblePending() []proto.Interaction         { return h.s.visiblePendingInteractions() }
func (h larkHost) PendingQuestion(ix proto.Interaction) string { return pendingQuestion(ix) }

func (h larkHost) MainChat(ctx context.Context) (string, string) {
	cfg := h.s.settings(ctx)
	return cfg.Main, cfg.mainRouteMachine()
}

// The main conversation's model, read and changed for the assistant (its launcher purpose).

func (h larkHost) MainModel(ctx context.Context, machine, main string, l i18n.Lang) (string, string, string, error) {
	params := map[string]any{}
	if err := h.s.sendModel(withLauncherPurpose(ctx, "assistant"), machine, main, params, l); err != nil {
		return "", "", "", err
	}
	launcher, _ := params["launcher"].(string)
	current, _ := params["model"].(string)
	effort, _ := params["effort"].(string)
	return launcher, current, effort, nil
}

func (h larkHost) LauncherModels(ctx context.Context, machine, launcher string) (proto.LauncherModels, error) {
	ctx = withLauncherPurpose(ctx, "assistant")
	listCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := h.s.askLauncherModels(listCtx, machine, launcher, false)
	if err != nil {
		return proto.LauncherModels{}, err
	}
	return h.s.withCustomModels(ctx, machine, launcher, res), nil
}

func (h larkHost) ListsModel(res proto.LauncherModels, model string) bool {
	return listsModel(res, model)
}

func (h larkHost) PickModel(ctx context.Context, main, machine string, pick proto.ModelPick, l i18n.Lang) error {
	_, err := h.s.pickModel(withLauncherPurpose(ctx, "assistant"), main, machine, pick, l)
	return err
}

// ---- the cards' presses and typed answers ----

func (h larkHost) EndedText(l i18n.Lang, status, fallback string) string {
	return endedText(l, status, fallback)
}

func (h larkHost) ResolutionText(l i18n.Lang, ix proto.Interaction, r proto.Resolution) string {
	return resolutionText(l, ix, r)
}

// ---- the agents' cards (ask_view.go) ----

func (h larkHost) AskView(ctx context.Context, ix proto.Interaction) assistantwire.AskView {
	return h.s.ixView(ctx, ix)
}

func (h larkHost) AskRoute(ctx context.Context, ix proto.Interaction) assistantwire.AskRoute {
	return h.s.ixRoute(ctx, ix)
}

func (h larkHost) AskSensitive(ctx context.Context, ix proto.Interaction) bool {
	return h.s.ixSensitive(ctx, ix)
}

func (h larkHost) AskDirect(ctx context.Context, ix proto.Interaction) bool {
	return h.s.nonMainAssistantPermission(ctx, ix)
}

func (h larkHost) AskState(id string) (ended, pending bool) { return h.s.ixState(id) }

func (h larkHost) AskStateFor(ix proto.Interaction) (ended, pending bool) {
	_, pending = h.s.ixState(ix.ID)
	return h.s.interactionEndedFor(ix), pending
}

// AskPostHook is the existing test-only race hook; the module calls it through
// an optional interface after the first occurrence check, before claiming a card.
func (h larkHost) AskPostHook(ix proto.Interaction) {
	if h.s.postCardHook != nil {
		h.s.postCardHook(ix)
	}
}

func (h larkHost) AskPending(id string) (proto.Interaction, bool) { return h.s.pendingIx(id) }

func (h larkHost) ResolveInteraction(ctx context.Context, id string, r proto.Resolution, action string) (bool, error) {
	return h.s.resolveInteraction(ctx, channel.Feishu, id, r, action)
}

func (h larkHost) ResolveInteractionAt(ctx context.Context, id string, r proto.Resolution, action string, expected *time.Time) (bool, error) {
	return h.s.resolveInteractionAt(ctx, channel.Feishu, id, r, action, expected)
}

func (h larkHost) ValidateClarifyQuestion(text string) error { return validateClarifyQuestion(text) }

func (h larkHost) ClarifyInteraction(ctx context.Context, id, question, clientMsgID, action, chatID, messageID string) (bool, error) {
	return h.s.clarifyInteraction(ctx, channel.Feishu, id, question, clientMsgID, action, chatID, messageID)
}

func (h larkHost) ClarifyInteractionAt(ctx context.Context, id, question, clientMsgID, action, chatID, messageID string, expected *time.Time) (bool, error) {
	return h.s.clarifyInteractionAt(ctx, channel.Feishu, id, question, clientMsgID, action, chatID, messageID, expected)
}

// AcceptAction answers a press of a "Please verify" or decision card's button ({a: accept}): the toast's kind and
// text. The hub checked the owner pressed it; marking a task verified approves nothing.
func (h larkHost) AcceptAction(ctx context.Context, a *botcard.CardAction) (string, string) {
	s := h.s
	l := s.botLang(ctx)
	var result acceptResult
	var err error
	ask, _ := s.askCopy(a.Str("id"))
	if resp := a.Str("resp"); resp != "" { // "I can't answer this": to its supervisor, nothing taken (decision.go)
		if s.askCardNow(a.Str("id"), a.Context.MessageID) {
			result.taken, err = s.ownerCannotAnswer(ctx, a.Str("id"), "", resp)
		}
		ask.Response = resp
	} else if opt := a.Str("opt"); opt != "" { // an option of the decision other than the recommended one
		if s.askCardNow(a.Str("id"), a.Context.MessageID) {
			result, err = s.chooseOption(ctx, a.Str("id"), "", opt)
		}
		ask.Chosen = opt
		ask.OptionTaken = result.taken && err == nil // this successful click, not an ambiguous legacy choice
		if opt == ask.Recommend {
			ask.Chosen = "" // the recommended one: taken as OK takes it
		}
	} else if at := a.Str("asked_at"); at != "" { // a button of a briefing card: it accepts the round it was asked in (briefing.go)
		result, err = s.acceptReviewRound(ctx, a.Str("id"), at)
	} else {
		result, err = s.acceptTry(ctx, a.Str("id"), a.Context.MessageID)
	}
	if errors.Is(err, errAcceptStore) { // the asks could not be read or saved: not accepted, the button works again
		return "error", i18n.T(l, "http-status.unavailable")
	}
	if note := inDoubtNote(l, err); note != "" { // it may be with the agent already: look before pressing again
		return "error", note
	}
	if err != nil { // another delivery failure: not taken, nothing recorded, the button works again
		return "error", i18n.T(l, "assistant-reports.accept_not_taken", i18n.Args{"error": clip(oneLine(i18n.Localize(err, l)), 120)})
	}
	if result.undelivered != nil {
		why := clip(oneLine(i18n.Localize(result.undelivered, l)), 120)
		return "success", i18n.T(l, "assistant-reports.accept_decided_not_sent", i18n.Args{"error": why})
	}
	if result.taken {
		return "success", acceptedNote(l, &ask) // "verified", "got it", or the recommendation taken (conclusion.go)
	}
	return "info", i18n.T(l, "assistant-feishu_cards.already_handled")
}

func (h larkHost) LaterHandled(ctx context.Context, id string) (bool, error) {
	return h.s.laterHandled(ctx, id, time.Now())
}

func (h larkHost) TabName(ctx context.Context, prompt string) string { return localTabName(prompt) }

func (h larkHost) ApplyModel(ctx context.Context, kind string, params map[string]any) error {
	return h.s.applyModel(ctx, kind, params)
}

func (h larkHost) StartAgent(ctx context.Context, machine string, params map[string]any) (proto.Session, error) {
	return h.s.startChatAgent(ctx, machine, params)
}

// startChatAgent starts the coding agent the owner asked for on the Lark bot's /start card.
func (s *Service) startChatAgent(ctx context.Context, machine string, params map[string]any) (proto.Session, error) {
	var res struct {
		Session proto.Session `json:"session"`
	}
	err := s.call(ctx, machine, DeviceSessionStart, params, &res)
	if err == nil {
		nameFrom, _ := params["prompt"].(string)
		requested, _ := params["tab_name"].(string)
		s.nameAgentTabLater(agentTabNameStart(machine, res.Session, requested, nameFrom))
	}
	return res.Session, err
}

func (h larkHost) StartChannelRun(r *channel.Run) { h.s.startChannelRun(r) }

// ---- the nudges ----

// AgentGone: the agent ix asks for is no longer in its computer's list (a list that is read, and does not have it).
func (h larkHost) AgentGone(ix proto.Interaction) bool {
	if ix.SessionID == "" {
		return false
	}
	// Only whether it is listed: no copy of the list, nor of the card (the closure takes the id alone).
	sid := ix.SessionID
	found, listed := h.s.nodeSessionsWhere(ix.MachineID, func(c *proto.Session) bool { return c.SessionID == sid })
	return listed > 0 && len(found) == 0
}

func (h larkHost) ApprovalNudgeDeadline(ix proto.Interaction) (time.Time, bool) {
	return h.s.approvalNudgeDeadline(ix)
}

// ---- the to-dos of topics and of requests from elsewhere ----

func (h larkHost) SourceChannels() []string { return sourceChannels }

func (h larkHost) EnsureTodosLoaded(ctx context.Context) error { return h.s.ensureTodosLoaded(ctx) }

func (h larkHost) TodoOfThread(chatID, threadID string) (assistantwire.ChatTodo, bool) {
	t, ok := h.s.todoOfThread(TodoThread{Channel: channel.Feishu, ChatID: chatID, ThreadID: threadID})
	if !ok {
		return assistantwire.ChatTodo{}, false
	}
	out := assistantwire.ChatTodo{ID: t.ID, Title: t.Title, Done: t.DoneAt != nil}
	if t.Thread != nil {
		out.ThreadTitle = t.Thread.Title
	}
	return out, true
}

// ThreadNoReply records the owner's choice to stop reporting back, then replies in the to-do's topic.
func (h larkHost) ThreadNoReply(ctx context.Context, messageID, todoID string, to assistantwire.ChannelTarget) {
	t, ok := h.s.todoNow(todoID)
	if !ok {
		t = Todo{ID: todoID}
	}
	h.s.stopReportOf(ctx, messageID, t, to, "thread_question", db.DecisionThread)
}

// TopicDecision notes text, said in a topic as message messageID, as a decision of todo todoID (asked as question).
func (h larkHost) TopicDecision(ctx context.Context, todoID, question, text, messageID string) string {
	store := h.s.todoDB()
	if store == nil {
		return assistantwire.DecisionNotNoted
	}
	at := decisionNow()
	inserted, err := h.s.insertDecision(ctx, store, db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: todoID,
		Question: question, Choice: decisionText(text), Source: db.DecisionThread, SourceMessage: "feishu-" + messageID, CreatedAt: at})
	switch {
	case errors.Is(err, db.ErrTodoNotFound): // deleted since it was looked up
		return assistantwire.DecisionTodoGone
	case err != nil:
		h.s.logf("todo %s: topic decision: %v", todoID, err) // as an acceptance's: the words still go to the agent
		return assistantwire.DecisionNotNoted
	case !inserted:
		return assistantwire.DecisionSeen
	}
	h.s.decisionsChanged(ctx, todoID, at)
	return assistantwire.DecisionNoted
}

func (h larkHost) TopicWake(ctx context.Context, todoID, label, text, key string) {
	if t, ok := h.s.todoNow(todoID); ok && needsUserWakeEligible(t) {
		h.s.emitNeedsUserExternalInfo(ctx, t, label, text, key)
	}
}

func (h larkHost) AskTodoTopicAnswer(ctx context.Context, todoID, text string) (string, bool) {
	t, ok := h.s.todoNow(todoID)
	if !ok {
		return "", false
	}
	return h.s.askTodoTopicAnswer(ctx, t, text)
}

func (h larkHost) SettleAskClaim(ctx context.Context, todoID string, ok bool) {
	h.s.settleAskClaim(ctx, todoID, ok)
}

func (h larkHost) CloseAskTodo(ctx context.Context, todoID string) { h.s.closeAskTodo(ctx, todoID) }

func (h larkHost) TodoTitle(ctx context.Context, words string, l i18n.Lang, linkLabel bool) (string, bool) {
	var title string
	retitle := true
	if linkLabel {
		title = cutTitle(oneLine(words), todoTitleMax(string(l)))
	} else {
		title, retitle = h.s.taskTitle(ctx, "", words)
	}
	if title == "" {
		title = cutRunes(oneLine(words), todoTitleMax(h.s.hello(ctx).Locale))
	}
	return title, retitle
}

// OpenTodo makes the to-do the chat opens, bound to its topic and with its source; who takes it, the main
// conversation, supervises it and shows it in its feed. A topic that turns out to be taken (db.ErrThreadTaken), or a
// supervision that cannot start, leaves no to-do behind.
func (h larkHost) OpenTodo(ctx context.Context, o assistantwire.ChatOpenTodo) (string, error) {
	s := h.s
	todo, err := s.createTodo(ctx, o.Title, o.Notes, o.Origin, "")
	if err != nil {
		s.logf("bots: %s: open a to-do: %v", o.Where, err)
		return "", err
	}
	if o.Topic {
		var th TodoThread
		if th, err = cleanThread(TodoThread{Channel: channel.Feishu, ChatID: o.ChatID, ThreadID: o.ThreadID, Title: o.Title}); err == nil {
			_, err = s.bindThread(ctx, todo.ID, th, nil)
		}
	}
	if err == nil && o.Source != nil {
		src := TodoSource{Channel: o.Source.Channel, ChatID: o.Source.ChatID, MessageID: o.Source.MessageID, Anchor: o.Source.Anchor,
			Handoff: o.Source.Handoff}
		if err = s.setTodoSource(ctx, todo.ID, src); err == nil {
			s.todosChanged(ctx)
		}
	}
	if err != nil {
		// Taken by another to-do meanwhile (or not a thread that can be bound): the to-do just made goes again.
		if rerr := s.removeTodo(context.WithoutCancel(ctx), todo.ID); rerr != nil {
			s.logf("todo %s: take back: %v", todo.ID, rerr)
		}
		s.logf("bots: %s: bind: %v", o.Where, err)
		return "", err
	}
	if o.Retitle {
		s.nameTodoLater(ctx, todo.ID, o.Title, o.Text)
	}
	// The main conversation follows up what it hands out (supervise.go), and its feed shows the task as task_plan's do.
	if err := s.superviseTodo(ctx, todo.ID, o.Origin, nil, 0); err != nil {
		if rerr := s.removeTodo(context.WithoutCancel(ctx), todo.ID); rerr != nil {
			s.logf("todo %s: take back after supervision failure: %v", todo.ID, rerr)
		}
		return "", err
	}
	if _, err := s.appendFeed(ctx, o.Origin, feedTask, todo.ID, nil); err != nil {
		s.logf("task %s: feed: %v", todo.ID, err)
	}
	return todo.ID, nil
}

// StartHandoffGuest starts the guest that answers a proxy reply (bot_handoff.go).
func (h larkHost) StartHandoffGuest(ctx context.Context, handoff, text, launcher string) (string, string, error) {
	return h.s.StartHandoffGuest(ctx, handoff, text, launcher)
}

// RaiseHandoffDecision asks the owner to approve one draft before the bot posts it.
func (h larkHost) RaiseHandoffDecision(ctx context.Context, handoff string, req assistantwire.OutsideDecisionRequest) (string, error) {
	return h.s.RaiseHandoffDecision(ctx, handoff, req)
}

// WithdrawHandoffDecision takes back a confirmation nobody answered.
func (h larkHost) WithdrawHandoffDecision(ctx context.Context, id string) {
	h.s.WithdrawHandoffDecision(ctx, id)
}

// StartGroupGuest starts the guest that answers one chat and thread (bot_guest.go).
func (h larkHost) StartGroupGuest(ctx context.Context, group, text, launcher string, tools []string) (string, string, error) {
	return h.s.StartGroupGuest(ctx, group, text, launcher, tools)
}

// SendGroupGuest sends a later question into the conversation that already answers the group.
func (h larkHost) SendGroupGuest(ctx context.Context, machine, session, turn, text string) error {
	return h.s.SendGroupGuest(ctx, machine, session, turn, text)
}
