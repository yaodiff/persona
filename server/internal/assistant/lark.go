package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larkhost"
	"github.com/nwverse/persona/server/internal/proto"
)

// The Feishu / Lark module's assistant layer (internal/modules/lark, docs/architecture/core-and-modules.md "Lark",
// #13404). Core code reaches the owner's chat only through the outbound channel port (s.chat(), larkhost.Outbound):
// what the channel supports (Status, Features), a card (Post), a line under a message (Note, Reply), a card edited in
// place (UpdateCard) and a line for a to-do's chat thread (ThreadPost). With the module in the build the port is the
// module's layer; without it (persona_without_lark, persona_core_only) it is larkhost.None, which posts nothing and
// says so. The transport that posts and edits cards (feishu_transport.go) is still the core's and is served to the
// module through larkHost; the chat itself (its state, the owner's messages and presses) is the module's, reached through larkhost.Chat
// (lark_chat.go).

// larkLayer is the module's layer, made once per service on first use (a service a test makes without New has one
// too), the module's times over its defaults (Options.LarkTimings; tests change them while it runs) and the chat's
// clock (nil: the time now; tests set it).
type larkLayer struct {
	once    sync.Once
	layer   larkhost.Layer
	mu      sync.Mutex
	timings map[string]time.Duration
	lists   map[string][]time.Duration
	now     func() time.Time
}

// larkModule is the lark module's layer, or larkhost.None without the module.
func (s *Service) larkModule() larkhost.Layer {
	s.lark.once.Do(func() {
		s.lark.layer = larkhost.None{}
		if l, ok := larkhost.New(s.moduleSwitch, larkHost{s}); ok { // nothing when built out or switched off
			s.lark.layer = l
		}
	})
	return s.lark.layer
}

// chat is the outbound channel port: the lark module's layer, or larkhost.None without the module.
func (s *Service) chat() larkhost.Outbound { return s.larkModule() }

// larkAbsent says the lark module is not in this hub (built out or switched off), as opposed to on with the bot not
// ready: its pushes have nowhere to go and are skipped, not failed.
func (s *Service) larkAbsent() bool {
	_, none := s.larkModule().(larkhost.None)
	return none
}

// setLarkTiming sets one of the lark module's times (tests).
func (s *Service) setLarkTiming(name string, d time.Duration) {
	s.lark.mu.Lock()
	defer s.lark.mu.Unlock()
	if s.lark.timings == nil {
		s.lark.timings = map[string]time.Duration{}
	}
	s.lark.timings[name] = d
}

// setLarkTimingList sets one of the lark module's lists of times (tests).
func (s *Service) setLarkTimingList(name string, l []time.Duration) {
	s.lark.mu.Lock()
	defer s.lark.mu.Unlock()
	if s.lark.lists == nil {
		s.lark.lists = map[string][]time.Duration{}
	}
	s.lark.lists[name] = l
}

// feishuSink is the Feishu bot as the channels' sink (channels_runs.go): the lark module's turns (larkhost.Turns).
type feishuSink struct{ s *Service }

func (f feishuSink) progress(ctx context.Context, r *channel.Run, took time.Duration, steps []string, live string) {
	f.s.larkModule().TurnProgress(ctx, r, took, steps, live)
}

func (f feishuSink) folded(ctx context.Context, o *channel.Run, owner *chanLive) {
	var st any
	if owner != nil {
		f.s.chans.mu.Lock()
		st = owner.sink
		f.s.chans.mu.Unlock()
	}
	f.s.larkModule().TurnFolded(ctx, o, st)
}

func (f feishuSink) recalled(ctx context.Context, m channel.Folded) {
	f.s.larkModule().TurnRecalled(ctx, m)
}

func (f feishuSink) answer(ctx context.Context, r *channel.Run, answer, note string, took time.Duration, final, quiet bool, end channel.End) error {
	return f.s.larkModule().TurnAnswer(ctx, r, answer, note, took, final, quiet, end)
}

func (f feishuSink) progressAfter(r *channel.Run) time.Duration {
	return f.s.larkModule().TurnProgressAfter(r)
}
func (f feishuSink) progressIdle() time.Duration { return f.s.larkModule().TurnProgressIdle() }

// botAnswerCard is one card of an answer and what is sent instead when Feishu refuses it.
type botAnswerCard struct {
	card     *botcard.Card
	fallback string
}

// answerCards lays out a final answer as the bot's cards (the module's, Layer.AnswerCards); none without the module.
func (s *Service) answerCards(l i18n.Lang, name func(*url.URL) string, answer string, quiet bool, steps []string, foot string) []botAnswerCard {
	cards, fallbacks := s.larkModule().AnswerCards(l, name, answer, quiet, steps, foot)
	out := make([]botAnswerCard, len(cards))
	for i, c := range cards {
		out[i] = botAnswerCard{card: c, fallback: fallbacks[i]}
	}
	return out
}

// larkHost is the core as the lark module reaches it (larkhost.Host).
type larkHost struct{ s *Service }

func (h larkHost) Post(ctx context.Context, to larkhost.Target, replyTo string, card *botcard.Card, fallback, key string) (string, error) {
	return h.s.botPostTo(ctx, to, replyTo, card, fallback, key)
}

func (h larkHost) Note(ctx context.Context, to larkhost.Target, replyTo, text string) {
	h.s.botNoteTo(ctx, to, replyTo, text)
}

func (h larkHost) UpdateCard(ctx context.Context, messageID string, card *botcard.Card) error {
	return h.s.botUpdateCard(ctx, messageID, card)
}

// ---- a to-do's topic lines (the lark module's thread_posts.go) ----

func (h larkHost) ThreadOf(todoID string) (string, string, bool) {
	th := h.s.threadOf(todoID)
	if th == nil || th.Channel != channel.Feishu {
		return "", "", false
	}
	return th.ChatID, th.ThreadID, true
}

func (h larkHost) TodoLink(web, todoID string) string  { return todoLink(web, todoID) }
func (h larkHost) PostNow() time.Time                  { return h.s.tposts.clock() }
func (h larkHost) PostLater(d time.Duration, f func()) { h.s.tposts.later(d, f) }

func (h larkHost) Logf(format string, args ...any) { h.s.logf(format, args...) }
func (h larkHost) RunCtx() context.Context         { return h.s.runCtx() }

func (h larkHost) Timings() (map[string]time.Duration, map[string][]time.Duration) {
	h.s.lark.mu.Lock()
	defer h.s.lark.mu.Unlock()
	return maps.Clone(h.s.lark.timings), maps.Clone(h.s.lark.lists)
}

func (h larkHost) Lang(ctx context.Context) i18n.Lang { return h.s.botLang(ctx) }

func (h larkHost) SendAs(ctx context.Context, kind, machine, session string) context.Context {
	return feishuSendAs(ctx, kind, machine, session)
}

func (h larkHost) Call(ctx context.Context, method string, params, out any) error {
	return h.s.appCall(ctx, AppFeishu, method, params, out)
}

func (h larkHost) PostContent(ctx context.Context, to larkhost.Target, replyTo, content, fallback, key string) (string, error) {
	return h.s.botPostContent(ctx, to, replyTo, content, fallback, key)
}

func (h larkHost) LinkNamer(ctx context.Context) func(*url.URL) string { return h.s.linkNamer(ctx) }

func (h larkHost) OpenButton(ctx context.Context, machine, session string) botcard.Button {
	return h.s.openButton(ctx, machine, session)
}

func (h larkHost) Unreachable(err error) bool { return unreachable(err) }
func (h larkHost) Offline(err error) bool     { return errors.Is(err, bus.ErrOffline) }
func (h larkHost) Answered(err error) bool    { return answered(err) }

func (h larkHost) StepParts(l i18n.Lang, it proto.Item) (string, string) { return botStepParts(l, it) }

func (h larkHost) RunProgress(r *channel.Run) assistantwire.RunProgress { return h.s.runProgress(r) }

func (h larkHost) Now() time.Time                           { return h.s.chansNow() }
func (h larkHost) RunExt(r *channel.Run, key string) string { return h.s.runExt(r, key) }
func (h larkHost) RunExts(r *channel.Run, keys ...string) map[string]string {
	return h.s.runExts(r, keys...)
}
func (h larkHost) SetRunExt(r *channel.Run, key, value string) { h.s.setRunExt(r, key, value) }

func (h larkHost) SetLiveRunExt(r *channel.Run, key, value string) {
	if real, _ := h.s.channelRunByID(r.ID); real != nil && real != r {
		h.s.setRunExt(real, key, value)
	}
	h.s.setRunExt(r, key, value)
}

func (h larkHost) RunState(id string, kept bool, make func() any) (any, <-chan struct{}, bool) {
	c := &h.s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.live[id]
	if p == nil || kept && !slices.ContainsFunc(c.st.Runs, func(r *channel.Run) bool { return r.ID == id }) {
		return nil, nil, false
	}
	if p.sink == nil {
		p.sink = make()
	}
	return p.sink, p.done, true
}

func (h larkHost) RunItems(id string) ([]proto.Item, bool) {
	p, items := h.s.runItems(id)
	return items, p != nil
}

func (h larkHost) RunSteps(id string) []string { return h.s.runSteps(id) }

func (h larkHost) PersistRunExt(id string, state any, still func() bool, ext map[string]string) (live, kept bool) {
	c := &h.s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.live[id]
	if p == nil || p.sink != state || !still() {
		return false, false
	}
	for _, r := range c.st.Runs {
		if r.ID != id {
			continue
		}
		if r.Ext == nil {
			r.Ext = map[string]string{}
		}
		for k, v := range ext {
			r.Ext[k] = v
		}
		return true, true
	}
	return true, false
}

func (h larkHost) SaveRuns() {
	c := &h.s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	h.s.saveChannelStateLocked()
}

// larkWait waits, at most d, for the lark module's background work (the Feishu agent messages' workers, the chat's
// messages being handled) to end.
func (s *Service) larkWait(d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	if err := s.larkModule().Wait(ctx); err != nil {
		s.logf("lark: background work still running at the end: %v", err)
	}
}

// ---- the workbench (the lark module's workbench.go) ----

func (h larkHost) WebBase(ctx context.Context) string          { return h.s.webBase(ctx) }
func (h larkHost) Location(ctx context.Context) *time.Location { return h.s.location(ctx) }
func (h larkHost) ClipFallback(text string) string             { return clip(text, reportFallbackMax) }

func (h larkHost) WorkbenchAgents(ctx context.Context, now time.Time) (assistantwire.WorkbenchCounts, []assistantwire.WorkbenchAgent) {
	c, busy := h.s.workbenchCount(ctx, now)
	out := make([]assistantwire.WorkbenchAgent, len(busy))
	for i, b := range busy {
		out[i] = assistantwire.WorkbenchAgent{Machine: b.d.ID, MachineName: b.d.Name, Session: b.sess}
	}
	return assistantwire.WorkbenchCounts{Working: c.working, Unread: c.unread, Idle: c.idle, Pending: c.pending,
		Online: c.online, Machines: c.machines}, out
}

func (h larkHost) WorkbenchInbox() []assistantwire.WorkbenchItem {
	open := h.s.workbenchInbox()
	out := make([]assistantwire.WorkbenchItem, len(open))
	for i, it := range open {
		out[i] = workbenchItemOf(it)
	}
	return out
}

func (h larkHost) WorkbenchItem(id string) (assistantwire.WorkbenchItem, bool) {
	it, ok := h.s.workbenchItem(id)
	if !ok {
		return assistantwire.WorkbenchItem{}, false
	}
	return workbenchItemOf(it), true
}

func (s *Service) workbenchInboxAction(ctx context.Context, a *botcard.CardAction) *botcard.CardResponse {
	return s.larkModule().WorkbenchInboxAction(ctx, a)
}

func (s *Service) draftCard(ctx context.Context, it InboxItem, text string, err error, list string) *botcard.Card {
	return s.larkModule().WorkbenchDraftCard(ctx, workbenchItemOf(it), text, err, list)
}

// workbenchItemOf is an Inbox item as the workbench's cards show it.
func workbenchItemOf(it InboxItem) assistantwire.WorkbenchItem {
	w := assistantwire.WorkbenchItem{ID: it.ID, Source: it.Source, AccountName: it.AccountName, Title: it.Title,
		Sender: it.Sender, Preview: it.Preview, Summary: it.Summary, Recap: it.Recap, Tier: it.Tier, Urgent: it.Urgent,
		UpdatedAt: it.UpdatedAt, Done: it.DoneAt != nil, Mail: it.Mail != nil, Lark: it.Lark != nil, Connector: it.Connector != nil}
	if g := currentSuggestion(&it); g != nil && g.Kind == SuggestReply {
		w.ReplyVersion = g.Version
	}
	if c := it.Connector; c != nil {
		w.ConnectorLabel, w.ConnectorCanReply, w.ConnectorReplyTo = c.Label, c.CanReply, c.ReplyTo
	}
	for _, m := range it.Mutes {
		w.Mutes = append(w.Mutes, assistantwire.WorkbenchMute{Tag: m.Tag, Value: m.Value, Name: m.Name})
	}
	return w
}

func (h larkHost) WorkbenchBoard(ctx context.Context, l i18n.Lang, web string, now time.Time) []assistantwire.WorkbenchBoardRow {
	rows := h.s.boardRows(ctx, l, web, now)
	out := make([]assistantwire.WorkbenchBoardRow, len(rows))
	for i, r := range rows {
		link := r.link
		if !boardLinkOK(link) {
			link = ""
		}
		out[i] = assistantwire.WorkbenchBoardRow{ID: r.id, From: r.from, Summary: r.summary, Waited: boardWait(l, r.waited),
			Link: link, Blocking: r.blocking}
	}
	return out
}

func (h larkHost) NextMeeting(ctx context.Context, now time.Time) *assistantwire.WorkbenchMeeting {
	cal := snapshotCalendar(h.s.connectorSnapshots(ctx), now)
	if cal == nil || len(cal.Events) == 0 {
		return nil
	}
	return &assistantwire.WorkbenchMeeting{Start: cal.Events[0].Start, Title: cal.Events[0].Title}
}

func (h larkHost) TodayBody(ctx context.Context) json.RawMessage {
	main := h.s.settings(ctx).Main
	if main == "" {
		return nil
	}
	it, _, err := h.s.ensureToday(ctx, main)
	if err != nil {
		return nil
	}
	return it.Body
}

func (h larkHost) BoardCard(ctx context.Context, now time.Time) (*botcard.Card, string, string) {
	return h.s.boardCard(ctx, now)
}

func (h larkHost) FleetReady() bool { return h.s.fleetReady() }

func (h larkHost) TasksOverview(ctx context.Context) ([]*botcard.Card, []string) {
	var cards []*botcard.Card
	var fallbacks []string
	for _, oc := range h.s.tasksOverview(ctx) {
		cards, fallbacks = append(cards, oc.card), append(fallbacks, oc.fallback)
	}
	return cards, fallbacks
}

func (h larkHost) InboxDone(ctx context.Context, id string, dismissed bool) error {
	_, err := h.s.setInboxDone(ctx, id, true, dismissed)
	return err
}

func (h larkHost) InboxMute(ctx context.Context, source, tag, value, name string) error {
	_, _, err := h.s.addMute(ctx, InboxMute{Source: source, Tag: tag, Value: value, Name: name})
	return err
}

func (h larkHost) DraftReply(ctx context.Context, id, instruction string, expectedVersion int, strictVersion bool) (assistantwire.WorkbenchItem, string, error) {
	return h.s.draftWorkbenchReply(ctx, id, instruction, expectedVersion, strictVersion)
}

func (h larkHost) ReplyInbox(ctx context.Context, id, text, clientID string, expectedVersion int) (bool, error) {
	_, err := h.s.replyInboxFromVersion(ctx, id, text, clientID, true, expectedVersion)
	if errors.Is(err, errInboxItemDone) {
		return true, nil
	}
	return false, err
}
