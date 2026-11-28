package lark

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The fake core's side of the chat (chat.go): the settings row the chat's state is kept in, the chat's clock, and what
// the chat asks the core (where a target is, a run started, an interaction answered, a to-do opened). Each answers as
// the core would for the plain case; a test sets what it needs on chatData (ch) and reads back what was asked.

// chatData is the fake core's chat side.
type chatData struct {
	settings map[string][]byte
	now      func() time.Time
	hello    assistantwire.Hello
	sessions map[string][]proto.Session // by machine
	devices  []assistantwire.Device
	pending  []proto.Interaction
	main     string // the main conversation ("" none), on machine "server"
	runs     int    // the bot's runs the channels follow (FeishuRuns)

	// What the chat asked of the core, in order.
	forgot   int
	recalled []string
	started  []startedRun
	stopped  []assistantwire.ChatTarget
	resolved []resolvedAnswer
	opened   []assistantwire.ChatOpenTodo

	// What the core answers, when a test sets it.
	resolve     func(t assistantwire.ChatTarget) (assistantwire.ChatResolved, error)
	answer      func(id string, r proto.Resolution) (bool, error)
	view        func(ix proto.Interaction) assistantwire.AskView
	route       func(ix proto.Interaction) assistantwire.AskRoute
	sensitive   func(ix proto.Interaction) bool
	direct      func(ix proto.Interaction) bool
	state       func(id string) (ended, pending bool)
	pendingIx   func(id string) (proto.Interaction, bool)
	deadline    func(ix proto.Interaction) (time.Time, bool)
	gone        func(ix proto.Interaction) bool
	todoOf      func(chat, thread string) (assistantwire.ChatTodo, bool)
	decision    func(todoID, text, messageID string) string
	openTodo    func(t assistantwire.ChatOpenTodo) (string, error)
	acceptReply func(messageID, text string) bool

	guest *guestFake
}

// startedRun is a run the chat asked the core to start for a message (Host.StartRun).
type startedRun struct {
	rt        assistantwire.ChatResolved
	t         assistantwire.ChatTarget
	messageID string
	text      string
	images    []proto.Image
	probe     string
	reaction  string
	ext       map[string]string
}

// resolvedAnswer is an interaction the chat answered (Host.ResolveInteraction).
type resolvedAnswer struct {
	id       string
	r        proto.Resolution
	action   string
	expected *time.Time
}

func (s *Service) ch() *chatData {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if s.chd == nil {
		s.chd = &chatData{settings: map[string][]byte{}, sessions: map[string][]proto.Session{}}
	}
	return s.chd
}

// chat is the module's chat as the fake core's layer has it.
func (s *Service) chat() core { return s.layer.chat() }

func (s *Service) Setting(_ context.Context, key string, out any) (bool, error) {
	d := s.ch()
	s.tmu.Lock()
	raw, ok := d.settings[key]
	s.tmu.Unlock()
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, out)
}

func (s *Service) PutSetting(_ context.Context, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	d := s.ch()
	s.tmu.Lock()
	d.settings[key] = raw
	s.tmu.Unlock()
	return nil
}

func (s *Service) ChatNow() time.Time {
	d := s.ch()
	s.tmu.Lock()
	now := d.now
	s.tmu.Unlock()
	if now != nil {
		return now()
	}
	return time.Now()
}

func (s *Service) ForgetTurns() {
	d := s.ch()
	s.tmu.Lock()
	d.forgot++
	s.tmu.Unlock()
}

func (s *Service) RecallRun(messageID string) {
	d := s.ch()
	s.tmu.Lock()
	d.recalled = append(d.recalled, messageID)
	s.tmu.Unlock()
}

func (s *Service) CallApp(ctx context.Context, app, method string, params, out any) error {
	raw, _ := json.Marshal(params)
	res, err := s.hub.AppCall(ctx, AppCall{AppID: app, Method: method, Params: raw})
	if err != nil {
		return err
	}
	if out != nil && len(res) > 0 {
		return json.Unmarshal(res, out)
	}
	return nil
}

// ResolveTarget is the target as the core resolves it: an agent by its ids, else the main conversation ("main" on
// "server"), a new one when none is given.
func (s *Service) ResolveTarget(_ context.Context, t assistantwire.ChatTarget) (assistantwire.ChatResolved, error) {
	d := s.ch()
	if d.resolve != nil {
		return d.resolve(t)
	}
	if t.Kind == targetAgent {
		return assistantwire.ChatResolved{Machine: t.MachineID, MachineName: t.MachineID, Label: "agent " + t.SessionID,
			Session: proto.Session{MachineID: t.MachineID, SessionID: t.SessionID}}, nil
	}
	sid := t.SessionID
	if t.Kind == targetMain {
		sid = d.main
	}
	r := assistantwire.ChatResolved{Machine: "server", MachineName: "server", Main: sid != "" && sid == d.main}
	if sid == "" {
		r.IsNew, r.Main, r.Label = true, t.Kind == targetMain, "new"
		return r, nil
	}
	r.Session, r.Label = proto.Session{MachineID: "server", SessionID: sid}, "chat "+sid
	return r, nil
}

func (s *Service) SessionExists(_ context.Context, machine, session string) bool {
	if machine == "" {
		machine = "server"
	}
	for _, sess := range s.ch().sessions[machine] {
		if sess.SessionID == session {
			return true
		}
	}
	return false
}

func (s *Service) TargetLabel(context.Context, assistantwire.ChatTarget) string { return s.wb().target }

// Allowed is the core's botAllowed.
func (s *Service) Allowed(l i18n.Lang, f assistantwire.FeishuFeatures, t assistantwire.ChatTarget) error {
	switch {
	case t.Agent() && !f.Agents:
		return errors.New(i18n.T(l, "assistant-feishu.agents_off"))
	case !t.Agent() && !f.Main:
		if f.Agents {
			return errors.New(i18n.T(l, "assistant-feishu.main_off_agents_on"))
		}
		return errors.New(i18n.T(l, "assistant-feishu.chat_off"))
	}
	return nil
}

func (s *Service) StartRun(_ context.Context, rt assistantwire.ChatResolved, t assistantwire.ChatTarget, messageID, text string, images []proto.Image, probe, reaction string, ext map[string]string) error {
	d := s.ch()
	s.tmu.Lock()
	d.started = append(d.started, startedRun{rt: rt, t: t, messageID: messageID, text: text, images: images, probe: probe,
		reaction: reaction, ext: ext})
	s.tmu.Unlock()
	return nil
}

func (s *Service) Hello(context.Context) assistantwire.Hello { return s.ch().hello }

// AnswerLanguage is the core's answerLanguage: "" for Chinese and for none.
func (s *Service) AnswerLanguage(locale string) string {
	l := i18n.Lang(locale)
	if !i18n.Valid(l) || l == i18n.ZhHans {
		return ""
	}
	return l.Name()
}

func (s *Service) ReminderTodo(context.Context, string) *prompts.BotTodo { return nil }

// NoReplyCommand is the core's noReplyCommand.
func (s *Service) NoReplyCommand(text string) bool {
	t := strings.TrimSpace(text)
	rest, ok := strings.CutPrefix(t, "/")
	if !ok {
		rest, ok = strings.CutPrefix(t, "／") // i18n-ignore: the full-width slash, as the core's
	}
	return ok && strings.EqualFold(rest, "noreply")
}

func (s *Service) ChatNoReply(context.Context, botcard.MessageEvent) bool { return false }

func (s *Service) ConclusionQuestion(context.Context, *botcard.MessageEvent, *botcard.Content, assistantwire.FeishuFeatures) bool {
	return false
}

func (s *Service) AcceptReply(_ context.Context, messageID, text string, _ bool, _, _ string) bool {
	if f := s.ch().acceptReply; f != nil {
		return f(messageID, text)
	}
	return false
}

func (s *Service) ApproveOwnerAction(context.Context, botcard.MessageEvent, string, string) bool {
	return false
}

func (s *Service) ProjectReviewOwnerInput(context.Context, string, string, string) error { return nil }

func (s *Service) InterruptConversation(_ context.Context, rt assistantwire.ChatResolved) (string, string) {
	d := s.ch()
	s.tmu.Lock()
	d.stopped = append(d.stopped, assistantwire.ChatTarget{MachineID: rt.Machine, SessionID: rt.Session.SessionID})
	s.tmu.Unlock()
	return "success", "stopped " + rt.Session.SessionID
}

func (s *Service) TurnPending(string, string) bool { return false }

func (s *Service) FindInterruptibleRun(func(*channel.Run) bool) func(proto.Session, func(string) string) {
	return nil
}

func (s *Service) Conversations(context.Context) ([]proto.Session, string) {
	d := s.ch()
	return d.sessions["server"], d.main
}

func (s *Service) ConversationName(sess proto.Session, main string) string {
	if sess.SessionID == main {
		return "main"
	}
	return sess.Title
}

func (s *Service) Devices(context.Context) []assistantwire.Device { return s.ch().devices }

func (s *Service) Device(_ context.Context, id string) (assistantwire.Device, bool) {
	for _, d := range s.ch().devices {
		if d.ID == id {
			return d, true
		}
	}
	return assistantwire.Device{}, false
}

func (s *Service) Sessions(_ context.Context, machine string) ([]proto.Session, error) {
	return s.ch().sessions[machine], nil
}

func (s *Service) UseHerdrTabName(context.Context) bool { return false }

func (s *Service) SessionNamesAs(sess proto.Session, _ bool) (string, string) { return sess.Title, "" }

func (s *Service) OpenLinkButton(context.Context, string) botcard.Button { return nil }

func (s *Service) SessionLink(web, machine, session string, _ bool) string {
	return web + "/s/" + machine + "/" + session
}

func (s *Service) FeishuRuns() int                     { return s.ch().runs }
func (s *Service) VisiblePending() []proto.Interaction { return s.ch().pending }

func (s *Service) PendingQuestion(ix proto.Interaction) string { return strings.TrimSpace(ix.Summary) }

func (s *Service) MainChat(context.Context) (string, string) { return s.ch().main, "server" }

func (s *Service) MainModel(context.Context, string, string, i18n.Lang) (string, string, string, error) {
	return "claude", "", "", nil
}

func (s *Service) LauncherModels(context.Context, string, string) (proto.LauncherModels, error) {
	return proto.LauncherModels{}, nil
}

func (s *Service) ListsModel(res proto.LauncherModels, model string) bool {
	for _, m := range res.Models {
		if m.ID == model {
			return true
		}
	}
	return false
}

func (s *Service) PickModel(context.Context, string, string, proto.ModelPick, i18n.Lang) error {
	return nil
}

// EndedText is the core's endedText.
func (s *Service) EndedText(l i18n.Lang, status, fallback string) string {
	switch status {
	case "expired":
		return i18n.T(l, "assistant-feishu_cards.card_expired")
	case "cancelled":
		return i18n.T(l, "assistant-feishu_cards.card_withdrawn")
	}
	return i18n.T(l, fallback)
}

func (s *Service) ResolutionText(_ i18n.Lang, _ proto.Interaction, r proto.Resolution) string {
	for _, a := range r.Answers {
		return "answered: " + a
	}
	return "answered"
}

// AskView is what the core says of ix for its card: the test's (view), else the session's title as the agent and the
// tool by its name.
func (s *Service) AskView(_ context.Context, ix proto.Interaction) assistantwire.AskView {
	if f := s.ch().view; f != nil {
		return f(ix)
	}
	v := assistantwire.AskView{Agent: ix.SessionTitle, Tool: s.IxToolName(ix)}
	if v.Agent == "" {
		v.Agent = "Persona"
	}
	return v
}

// AskRoute is where ix's card goes: the test's (route), else no run of the chat is on its conversation.
func (s *Service) AskRoute(_ context.Context, ix proto.Interaction) assistantwire.AskRoute {
	if f := s.ch().route; f != nil {
		return f(ix)
	}
	return assistantwire.AskRoute{}
}

func (s *Service) AskSensitive(_ context.Context, ix proto.Interaction) bool {
	if f := s.ch().sensitive; f != nil {
		return f(ix)
	}
	return false
}

func (s *Service) AskDirect(_ context.Context, ix proto.Interaction) bool {
	if f := s.ch().direct; f != nil {
		return f(ix)
	}
	return false
}

// AskState is the core's record of interaction id: the test's (state), else not ended and up.
func (s *Service) AskState(id string) (ended, pending bool) {
	if f := s.ch().state; f != nil {
		return f(id)
	}
	return false, true
}

func (s *Service) AskStateFor(ix proto.Interaction) (ended, pending bool) { return s.AskState(ix.ID) }

// AskPending is the interaction up under id: the test's (pendingIx), else none.
func (s *Service) AskPending(id string) (proto.Interaction, bool) {
	if f := s.ch().pendingIx; f != nil {
		return f(id)
	}
	return proto.Interaction{}, false
}

func (s *Service) ResolveInteraction(ctx context.Context, id string, r proto.Resolution, action string) (bool, error) {
	return s.ResolveInteractionAt(ctx, id, r, action, nil)
}

func (s *Service) ResolveInteractionAt(_ context.Context, id string, r proto.Resolution, action string, expected *time.Time) (bool, error) {
	d := s.ch()
	s.tmu.Lock()
	d.resolved = append(d.resolved, resolvedAnswer{id: id, r: r, action: action, expected: expected})
	answer := d.answer
	s.tmu.Unlock()
	if answer != nil {
		return answer(id, r)
	}
	return true, nil
}

func (s *Service) ValidateClarifyQuestion(string) error { return nil }

func (s *Service) ClarifyInteraction(context.Context, string, string, string, string, string, string) (bool, error) {
	return true, nil
}

func (s *Service) ClarifyInteractionAt(context.Context, string, string, string, string, string, string, *time.Time) (bool, error) {
	return true, nil
}

func (s *Service) AcceptAction(context.Context, *botcard.CardAction) (string, string) {
	return "info", "accepted"
}
func (s *Service) LaterHandled(context.Context, string) (bool, error)       { return true, nil }
func (s *Service) TabName(context.Context, string) string                   { return "" }
func (s *Service) ApplyModel(context.Context, string, map[string]any) error { return nil }
func (s *Service) StartAgent(context.Context, string, map[string]any) (proto.Session, error) {
	return proto.Session{}, nil
}
func (s *Service) StartChannelRun(r *channel.Run) { s.startChannelRun(r) }

func (s *Service) AgentGone(ix proto.Interaction) bool {
	if f := s.ch().gone; f != nil {
		return f(ix)
	}
	return false
}

func (s *Service) ApprovalNudgeDeadline(ix proto.Interaction) (time.Time, bool) {
	if f := s.ch().deadline; f != nil {
		return f(ix)
	}
	return ix.ExpiresAt, !ix.ExpiresAt.IsZero()
}

func (s *Service) IxToolName(ix proto.Interaction) string {
	if ix.ToolTitle != "" {
		return ix.ToolTitle
	}
	return ix.ToolName
}

func (s *Service) SourceChannels() []string                { return []string{channel.Feishu} }
func (s *Service) EnsureTodosLoaded(context.Context) error { return nil }

func (s *Service) TodoOfThread(chat, thread string) (assistantwire.ChatTodo, bool) {
	if f := s.ch().todoOf; f != nil {
		return f(chat, thread)
	}
	return assistantwire.ChatTodo{}, false
}

func (s *Service) ThreadNoReply(context.Context, string, string, assistantwire.ChannelTarget) {}

func (s *Service) ThreadRouteCandidates(context.Context, string) []assistantwire.ChatTarget {
	return nil
}

func (s *Service) TopicDecision(_ context.Context, todoID, _, text, messageID string) string {
	if f := s.ch().decision; f != nil {
		return f(todoID, text, messageID)
	}
	return assistantwire.DecisionNoted
}

func (s *Service) TopicWake(context.Context, string, string, string, string) {}

func (s *Service) AskTodoTopicAnswer(context.Context, string, string) (string, bool) {
	return "", false
}
func (s *Service) SettleAskClaim(context.Context, string, bool) {}
func (s *Service) CloseAskTodo(context.Context, string)         {}

func (s *Service) TodoTitle(_ context.Context, words string, _ i18n.Lang, _ bool) (string, bool) {
	return oneLine(words), true
}

func (s *Service) OpenTodo(_ context.Context, t assistantwire.ChatOpenTodo) (string, error) {
	d := s.ch()
	s.tmu.Lock()
	d.opened = append(d.opened, t)
	open := d.openTodo
	s.tmu.Unlock()
	if open != nil {
		return open(t)
	}
	return "todo_1", nil
}

// startedHandoff is a proxy reply the module asked the core to run as a guest (Host.StartHandoffGuest).
type startedHandoff struct {
	id, text, launcher string
}

func (s *Service) handoffs() *handoffFake {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if s.hod == nil {
		s.hod = &handoffFake{}
	}
	return s.hod
}

// handoffFake is what the module asked of the core for a proxy reply, and what the core answers.
type handoffFake struct {
	started   []startedHandoff
	raised    []assistantwire.OutsideDecisionRequest
	withdrawn []string
	startErr  error
	raiseErr  error
	nextID    int
}

func (s *Service) StartHandoffGuest(_ context.Context, handoff, text, launcher string) (string, string, error) {
	d := s.handoffs()
	s.tmu.Lock()
	defer s.tmu.Unlock()
	d.started = append(d.started, startedHandoff{handoff, text, launcher})
	if d.startErr != nil {
		return "", "", d.startErr
	}
	return "mac", "sess-" + handoff, nil
}

func (s *Service) RaiseHandoffDecision(_ context.Context, _ string, req assistantwire.OutsideDecisionRequest) (string, error) {
	d := s.handoffs()
	s.tmu.Lock()
	defer s.tmu.Unlock()
	d.raised = append(d.raised, req)
	if d.raiseErr != nil {
		return "", d.raiseErr
	}
	d.nextID++
	return "odc_" + strconv.Itoa(d.nextID), nil
}

func (s *Service) WithdrawHandoffDecision(_ context.Context, id string) {
	d := s.handoffs()
	s.tmu.Lock()
	defer s.tmu.Unlock()
	d.withdrawn = append(d.withdrawn, id)
}

// guestFake records the group guest conversations the module started and the later questions it sent.
type guestFake struct {
	mu       sync.Mutex
	starts   []guestStart
	sends    []guestSend
	startErr error
	gone     bool
}

type guestStart struct {
	key, text, launcher string
	tools               []string
}
type guestSend struct{ machine, session, turn string }

func (s *Service) guests() *guestFake {
	d := s.ch()
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if d.guest == nil {
		d.guest = &guestFake{}
	}
	return d.guest
}

func (s *Service) StartGroupGuest(_ context.Context, group, text, launcher string, tools []string) (string, string, error) {
	f := s.guests()
	f.mu.Lock()
	f.starts = append(f.starts, guestStart{group, text, launcher, append([]string(nil), tools...)})
	err := f.startErr
	f.mu.Unlock()
	if err != nil {
		return "", "", err
	}
	return "m1", "s1", nil
}

func (s *Service) SendGroupGuest(_ context.Context, machine, session, turn, _ string) error {
	f := s.guests()
	f.mu.Lock()
	f.sends = append(f.sends, guestSend{machine, session, turn})
	gone := f.gone
	f.mu.Unlock()
	if gone {
		return errors.New(guestGone)
	}
	return nil
}
