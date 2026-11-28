package lark

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The module's turn tests run on a fake of the core (Service, with the assistant's names): the channels' runs as the
// core keeps them (chans: the runs, each live run's state and its lock), the core's part of a run's life
// (channelProgressTick, finishChannelRun, startChannelRun's folding, watchChannelRun's drop), and the hub's feishu.*
// transport (fakeHub, as the assistant's tests fake it). It serves the module's Host as the core does; what the core
// draws for the module (the progress card, the answer's cards, the web link, a step's words) it draws from the same
// catalog keys, enough for what these tests read.

const (
	AppFeishu          = assistantwire.AppFeishu
	FeishuPost         = assistantwire.FeishuPost
	FeishuUpdateCard   = assistantwire.FeishuUpdateCard
	FeishuStatusGet    = assistantwire.FeishuStatusGet
	FeishuCotCreate    = assistantwire.FeishuCotCreate
	FeishuCotAppend    = assistantwire.FeishuCotAppend
	FeishuCotComplete  = assistantwire.FeishuCotComplete
	FeishuCardCreate   = assistantwire.FeishuCardCreate
	FeishuCardUpdate   = assistantwire.FeishuCardUpdate
	FeishuCardStream   = assistantwire.FeishuCardStream
	FeishuCardSettings = assistantwire.FeishuCardSettings
	CodeFeishuAPI      = assistantwire.CodeFeishuAPI

	testChat   = "oc_test"
	testThread = "omt_test"

	// channelAnswerTries is how long the core tries an answer that did not go out again (channels_answer.go).
	channelAnswerTries = 2 * time.Minute
)

// errOffline stands in for the hub's bus.ErrOffline.
var errOffline = errors.New("fake: the hub is offline")

// ---- the hub's transport ----

type appAnswer func(params json.RawMessage) (any, error)

// AppCall is one call of the transport.
type AppCall struct {
	AppID, Method string
	Params        json.RawMessage
}

// Hub is the transport (a test may wrap the fake).
type Hub interface {
	AppCall(ctx context.Context, p AppCall) (json.RawMessage, error)
}

type fakeHub struct {
	mu       sync.Mutex
	apps     map[string]appAnswer
	blocking map[string]bool
	appCalls []AppCall
}

func newFakeHub() *fakeHub {
	return &fakeHub{apps: map[string]appAnswer{}, blocking: map[string]bool{}}
}

func (f *fakeHub) answerApp(app, method string, answer appAnswer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.apps[app+" "+method] = answer
}

// answerAppBlocking is answerApp for an answer that waits on the test: it runs outside the fake's lock.
func (f *fakeHub) answerAppBlocking(app, method string, answer appAnswer) {
	f.answerApp(app, method, answer)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blocking[app+" "+method] = true
}

func (f *fakeHub) appParams(app, method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.appCalls {
		if c.AppID == app && c.Method == method {
			var m map[string]any
			_ = json.Unmarshal(c.Params, &m)
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeHub) AppCall(ctx context.Context, p AppCall) (json.RawMessage, error) {
	f.mu.Lock()
	f.appCalls = append(f.appCalls, p)
	answer := f.apps[p.AppID+" "+p.Method]
	blocking := f.blocking[p.AppID+" "+p.Method]
	if answer == nil {
		f.mu.Unlock()
		return json.RawMessage(`{}`), nil
	}
	if !blocking {
		defer f.mu.Unlock()
	} else {
		f.mu.Unlock()
	}
	res, err := answer(p.Params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(res)
}

// feishuHub is the fake with a bot paired with owner: its status, and the cards posted and updated, in posts.
func feishuHub(owner string, pairedAt time.Time) (*fakeHub, *[]map[string]any) {
	f := newFakeHub()
	var posts []map[string]any
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: owner != "", Enabled: true, Owner: owner, PairedAt: pairedAt, PublicURL: "http://hub.example:18660", Features: FeishuFeatures{Main: true, Cards: true, TopicCot: true}}, nil
	})
	f.answerApp(AppFeishu, FeishuPost, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		posts = append(posts, m)
		return map[string]any{"message_id": "om_card"}, nil
	})
	f.answerApp(AppFeishu, FeishuUpdateCard, func(p json.RawMessage) (any, error) {
		var m map[string]any
		_ = json.Unmarshal(p, &m)
		m["update"] = true
		posts = append(posts, m)
		return map[string]any{}, nil
	})
	return f, &posts
}

// withWebBot answers the status with publicURL as the hub's web address.
func withWebBot(f *fakeHub, publicURL string) {
	f.answerApp(AppFeishu, FeishuStatusGet, func(json.RawMessage) (any, error) {
		return FeishuStatus{Ready: true, Enabled: true, Owner: "ou_a", PairedAt: time.Unix(100, 0), PublicURL: publicURL,
			Features: FeishuFeatures{Main: true, Cards: true}}, nil
	})
}

// feishuAPIError is Feishu's error code as the hub puts it on the bus.
func feishuAPIError(code int) error {
	data, _ := json.Marshal(map[string]int{"code": code})
	return &rpc.Error{Code: CodeFeishuAPI, Message: fmt.Sprintf("feishu: code %d", code), Data: data}
}

// ---- the channels ----

// chanLive is a live run as the core keeps it (channels_runs.go): its progress lock, what the turn showed so far and
// the module's own state of it (sink).
type chanLive struct {
	mu       sync.Mutex
	items    []proto.Item
	steps    []string
	from     int
	updated  time.Time
	dirty    bool
	finished bool
	failing  time.Time
	done     chan struct{}
	sink     any
}

func newLive() *chanLive { return &chanLive{done: make(chan struct{})} }

// Service is the fake core.
type Service struct {
	t     testing.TB
	hub   Hub
	layer *Layer
	chans struct {
		mu        sync.Mutex
		live      map[string]*chanLive
		st        struct{ Runs []*channel.Run }
		now       func() time.Time
		answering map[string]bool
	}
	// save, when set, is the channels' state saved (under the channels' lock, as the core's saveChannelStateLocked).
	save    func(runs []*channel.Run) error
	tmu     sync.Mutex
	timings map[string]time.Duration
	// lang is the language the core answers in ("" is the default, Chinese). A test sets it.
	lang  string
	lists map[string][]time.Duration
	ctx   context.Context
	logs  []string
	wbd   *wbData      // the workbench's data (wb)
	thd   *threadData  // the to-do threads (th)
	chd   *chatData    // the chat side (ch)
	hod   *handoffFake // a proxy reply's asks of the core (chat_fake_test.go)
}

func newTestService(t testing.TB, f Hub) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{t: t, hub: f, ctx: ctx}
	s.chans.live = map[string]*chanLive{}
	s.layer = New(s)
	s.layer.turns.board.now = s.clock // the board's clock is the fake one (the home card's "updated at")
	t.Cleanup(cancel)
	return s
}

// native is the run's agent-message worker, nil when it has none (the core's chanLive.native before the move).
func (p *chanLive) native() *cotWorker {
	st, _ := p.sink.(*runLive)
	if st == nil {
		return nil
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.native
}

// sink is the module's Feishu sink, as the core's feishuSink reaches it.
func (s *Service) sink() feishuSink { return s.layer.turns.sink() }

func (s *Service) clock() time.Time {
	if s.chans.now != nil {
		return s.chans.now()
	}
	return time.Now()
}

func (s *Service) runExt(r *channel.Run, key string) string { return s.RunExt(r, key) }

func (s *Service) setRunExt(r *channel.Run, key, value string) { s.SetRunExt(r, key, value) }

func (s *Service) channelRunByID(id string) (*channel.Run, *chanLive) {
	s.chans.mu.Lock()
	defer s.chans.mu.Unlock()
	for _, r := range s.chans.st.Runs {
		if r.ID == id {
			return r, s.chans.live[id]
		}
	}
	return nil, nil
}

// saveChannelStateLocked saves the channels' state (the caller holds the channels' lock).
func (s *Service) saveChannelStateLocked() error {
	if s.save != nil {
		return s.save(slices.Clone(s.chans.st.Runs))
	}
	return nil
}

// channelProgressTick shows a live run's progress when its lock is free, as the core's tick does.
func (s *Service) channelProgressTick(id string) {
	r, prog := s.channelRunByID(id)
	if r == nil || prog == nil || !prog.mu.TryLock() {
		return
	}
	defer prog.mu.Unlock()
	s.chans.mu.Lock()
	steps, started := slices.Clone(prog.steps), r.WaitFrom()
	now := s.clock()
	prog.dirty, prog.updated = false, now
	s.chans.mu.Unlock()
	s.layer.TurnProgress(context.Background(), r, now.Sub(started), steps, "")
}

// finishChannelRun answers run r as the core does (channels_answer.go): marked as answering, under its progress lock,
// on a copy of the run; an answer that did not go out is tried again for channelAnswerTries; then a held run forgets
// its turn (NextTurn) and any other ends (no longer kept or live, its done closed).
func (s *Service) finishChannelRun(r *channel.Run, res *channel.Result) {
	c := &s.chans
	c.mu.Lock()
	prog := c.live[r.ID]
	if prog != nil {
		if c.answering == nil {
			c.answering = map[string]bool{}
		}
		c.answering[r.ID] = true
	}
	c.mu.Unlock()
	if prog == nil {
		return
	}
	defer func() {
		c.mu.Lock()
		delete(c.answering, r.ID)
		c.mu.Unlock()
	}()
	prog.mu.Lock()
	defer prog.mu.Unlock()
	if prog.finished {
		return
	}
	c.mu.Lock()
	now := s.clock()
	took := now.Sub(r.WaitFrom())
	snap := *r
	snap.Ext = cloneExt(r.Ext)
	snap.Folded = slices.Clone(r.Folded)
	c.mu.Unlock()
	quiet := res.Quiet || strings.TrimSpace(res.Answer) == "" && res.Note == "" && snap.Held
	answer := res.Answer
	if strings.TrimSpace(answer) == "" && res.Note == "" && !quiet {
		answer = "(empty: " + res.Stopped + ")"
	}
	if err := s.layer.TurnAnswer(context.Background(), &snap, answer, res.Note, took, !res.Held, quiet, res.End); err != nil {
		c.mu.Lock()
		if prog.failing.IsZero() {
			prog.failing = now
		}
		again := now.Sub(prog.failing) < channelAnswerTries
		c.mu.Unlock()
		if again {
			return
		}
	}
	c.mu.Lock()
	prog.failing = time.Time{}
	if res.Held {
		r.Held = true
		delete(r.Ext, "card")
		for _, k := range []string{"cot", "cot_msg", "cot_para", "cot_calls", "cot_done", "cot_off", "cot_answer"} {
			delete(r.Ext, k)
		}
		prog.steps, prog.from = nil, 0
		prog.items = nil
		if n, ok := prog.sink.(interface{ NextTurn() }); ok {
			n.NextTurn()
		}
		_ = s.saveChannelStateLocked()
		c.mu.Unlock()
		return
	}
	prog.finished = true
	delete(c.live, r.ID)
	c.st.Runs = slices.DeleteFunc(c.st.Runs, func(o *channel.Run) bool { return o == r })
	_ = s.saveChannelStateLocked()
	c.mu.Unlock()
	close(prog.done)
}

// startChannelRun takes a new run in as the core does: the runs of the same conversation and place that are not being
// answered fold into it (no longer kept or live; their progress closed with their own state), and it is live.
func (s *Service) startChannelRun(r *channel.Run) {
	c := &s.chans
	c.mu.Lock()
	var folded []*channel.Run
	keep := c.st.Runs[:0]
	for _, o := range c.st.Runs {
		if o.Channel == r.Channel && o.MachineID == r.MachineID && o.SessionID == r.SessionID && !c.answering[o.ID] &&
			o.Ext["chat"] == r.Ext["chat"] && o.Ext["thread"] == r.Ext["thread"] {
			folded = append(folded, o)
			r.Fold(o)
			continue
		}
		keep = append(keep, o)
	}
	c.st.Runs = append(keep, r)
	gone := map[string]*chanLive{}
	for _, o := range folded {
		if p := c.live[o.ID]; p != nil {
			gone[o.ID] = p
			delete(c.live, o.ID)
		}
	}
	c.live[r.ID] = newLive()
	_ = s.saveChannelStateLocked()
	c.mu.Unlock()
	for _, o := range folded {
		ended := true
		var st any
		if p := gone[o.ID]; p != nil {
			p.mu.Lock()
			ended = !p.finished
			if ended {
				p.finished = true
				close(p.done)
			}
			p.mu.Unlock()
			c.mu.Lock()
			st = p.sink
			c.mu.Unlock()
		}
		if ended {
			s.layer.TurnFolded(context.Background(), o, st)
		}
	}
}

// watchChannelRun follows a run as the core's watcher does: it makes the run live, and once the run is no longer among
// the channels' runs it is no longer live either and the module's work for it stops (Abort, outside the lock).
func (s *Service) watchChannelRun(r *channel.Run) {
	c := &s.chans
	c.mu.Lock()
	prog := c.live[r.ID]
	if prog == nil {
		prog = newLive()
		c.live[r.ID] = prog
	}
	c.mu.Unlock()
	for {
		select {
		case <-prog.done:
			return
		case <-time.After(5 * time.Millisecond):
		}
		if run, _ := s.channelRunByID(r.ID); run == nil {
			c.mu.Lock()
			if c.live[r.ID] == prog {
				delete(c.live, r.ID)
			}
			st := prog.sink
			c.mu.Unlock()
			if a, ok := st.(interface{ Abort() }); ok {
				a.Abort()
			}
			return
		}
	}
}

// ---- the module's host ----

func (s *Service) call(ctx context.Context, method string, params, out any) error {
	raw, _ := json.Marshal(params)
	res, err := s.hub.AppCall(ctx, AppCall{AppID: AppFeishu, Method: method, Params: raw})
	if err != nil {
		return err
	}
	if out != nil && len(res) > 0 {
		return json.Unmarshal(res, out)
	}
	return nil
}

func (s *Service) Post(ctx context.Context, to assistantwire.ChannelTarget, replyTo string, card *botcard.Card, fallback, key string) (string, error) {
	return s.PostContent(ctx, to, replyTo, card.JSON(), fallback, key)
}

func (s *Service) PostContent(ctx context.Context, to assistantwire.ChannelTarget, replyTo, content, fallback, key string) (string, error) {
	var res struct {
		MessageID string `json:"message_id"`
	}
	p := map[string]any{"reply_to": replyTo, "card": content, "fallback": fallback}
	if key != "" {
		p["uuid"] = key
	}
	err := s.call(ctx, FeishuPost, targetParams(to, p), &res)
	return res.MessageID, err
}

func (s *Service) Note(ctx context.Context, to assistantwire.ChannelTarget, replyTo, text string) {
	_ = s.call(ctx, FeishuText, targetParams(to, map[string]any{"reply_to": replyTo, "text": text}), nil)
}

func (s *Service) UpdateCard(ctx context.Context, messageID string, card *botcard.Card) error {
	return s.call(ctx, FeishuUpdateCard, map[string]any{"message_id": messageID, "card": card.JSON()}, nil)
}

func (s *Service) ThreadPost(string, assistantwire.ThreadEvent) {}

func (s *Service) Logf(format string, args ...any) {
	s.tmu.Lock()
	s.logs = append(s.logs, fmt.Sprintf(format, args...))
	s.tmu.Unlock()
}

func (s *Service) RunCtx() context.Context { return s.ctx }

func (s *Service) Timings() (map[string]time.Duration, map[string][]time.Duration) {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	one, lists := map[string]time.Duration{}, map[string][]time.Duration{}
	for k, v := range s.timings {
		one[k] = v
	}
	for k, v := range s.lists {
		lists[k] = v
	}
	return one, lists
}

// setLarkTiming and setLarkTimingList set the module's times, as the core's Options.LarkTimings does.
func (s *Service) setLarkTiming(name string, d time.Duration) {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if s.timings == nil {
		s.timings = map[string]time.Duration{}
	}
	s.timings[name] = d
}

func (s *Service) setLarkTimingList(name string, l []time.Duration) {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if s.lists == nil {
		s.lists = map[string][]time.Duration{}
	}
	s.lists[name] = l
}

func (s *Service) Lang(context.Context) i18n.Lang { return i18n.Lang(s.lang) }

func (s *Service) SendAs(ctx context.Context, _, _, _ string) context.Context { return ctx }

func (s *Service) Call(ctx context.Context, method string, params, out any) error {
	return s.call(ctx, method, params, out)
}

func (s *Service) LinkNamer(context.Context) func(*url.URL) string { return nil }

// OpenButton is the conversation's web link when the hub has a web address: /persona/<id> as the core's for a Persona
// conversation.
func (s *Service) OpenButton(ctx context.Context, machine, session string) botcard.Button {
	var st FeishuStatus
	_ = s.call(ctx, FeishuStatusGet, map[string]any{}, &st)
	if st.PublicURL == "" {
		return botcard.Button{}
	}
	return botcard.LinkBtn(i18n.T("", "assistant-feishu_cards.open_web"), strings.TrimRight(st.PublicURL, "/")+"/persona/"+session)
}

func (s *Service) Unreachable(err error) bool {
	return errors.Is(err, errOffline) || errors.Is(err, rpc.ErrClosed) || errors.Is(err, context.DeadlineExceeded)
}

func (s *Service) Offline(err error) bool { return errors.Is(err, errOffline) }

func (s *Service) Answered(err error) bool {
	var reply *rpc.Error
	return errors.As(err, &reply)
}

// StepParts is a tool call's step in the words of the core's botStepParts, for the tools these tests call.
func (s *Service) StepParts(_ i18n.Lang, it proto.Item) (string, string) {
	var in struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
	}
	_ = json.Unmarshal(it.ToolInput, &in)
	switch it.ToolName {
	case "Bash":
		return "运行", in.Command // i18n-ignore: the core's step in its default language
	case "Read":
		return "读取", in.FilePath // i18n-ignore
	}
	return it.ToolName, ""
}

// RunProgress is a run that is going on and has nothing more to tell: its time is the fake's clock, its Stop button
// goes to no conversation.
func (s *Service) RunProgress(*channel.Run) assistantwire.RunProgress {
	return assistantwire.RunProgress{Now: s.Now()}
}

func (s *Service) Now() time.Time {
	s.chans.mu.Lock()
	defer s.chans.mu.Unlock()
	return s.clock()
}

func (s *Service) RunExt(r *channel.Run, key string) string {
	s.chans.mu.Lock()
	defer s.chans.mu.Unlock()
	return r.Ext[key]
}

func (s *Service) RunExts(r *channel.Run, keys ...string) map[string]string {
	s.chans.mu.Lock()
	defer s.chans.mu.Unlock()
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = r.Ext[k]
	}
	return out
}

func (s *Service) SetRunExt(r *channel.Run, key, value string) {
	s.chans.mu.Lock()
	defer s.chans.mu.Unlock()
	if r.Ext == nil {
		r.Ext = map[string]string{}
	}
	r.Ext[key] = value
	_ = s.saveChannelStateLocked()
}

func (s *Service) SetLiveRunExt(r *channel.Run, key, value string) {
	if real, _ := s.channelRunByID(r.ID); real != nil && real != r {
		s.SetRunExt(real, key, value)
	}
	s.SetRunExt(r, key, value)
}

func (s *Service) RunState(id string, kept bool, make func() any) (any, <-chan struct{}, bool) {
	c := &s.chans
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

func (s *Service) RunItems(id string) ([]proto.Item, bool) {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.live[id]
	if p == nil {
		return nil, false
	}
	return append([]proto.Item(nil), p.items...), true
}

func (s *Service) RunSteps(id string) []string {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	if p := c.live[id]; p != nil {
		return append([]string(nil), p.steps...)
	}
	return nil
}

func (s *Service) PersistRunExt(id string, state any, still func() bool, ext map[string]string) (bool, bool) {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.live[id]
	if p == nil || p.sink != state || !still() {
		return false, false
	}
	for _, r := range c.st.Runs {
		if r.ID == id {
			if r.Ext == nil {
				r.Ext = map[string]string{}
			}
			for k, v := range ext {
				r.Ext[k] = v
			}
			return true, true
		}
	}
	return true, false
}

func (s *Service) SaveRuns() {
	s.chans.mu.Lock()
	defer s.chans.mu.Unlock()
	_ = s.saveChannelStateLocked()
}

// ---- runs ----

// waitFor polls ok until it holds, for at most five seconds (as the assistant's).
func waitFor(t testing.TB, what string, ok func() bool) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(5 * time.Second)
	for !ok() {
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// lastCard is the card the run's progress card was last rewritten to.
func lastCard(t *testing.T, r *channel.Run, posts []map[string]any) string {
	t.Helper()
	last := posts[len(posts)-1]
	if last["update"] != true || last["message_id"] != r.Ext["card"] {
		t.Fatalf("the progress card was not rewritten: %v", last)
	}
	card, _ := last["card"].(string)
	return card
}

// ---- the workbench's part of the host ----

// wbData is what the fake core gives the workbench (tests set it).
type wbData struct {
	web     string
	counts  assistantwire.WorkbenchCounts
	agents  []assistantwire.WorkbenchAgent
	inbox   []assistantwire.WorkbenchItem
	board   []assistantwire.WorkbenchBoardRow
	today   json.RawMessage
	target  string
	main    *bool
	done    []string
	mutes   []string
	drafts  []string
	replies []string
	draft   string
}

func (s *Service) wb() *wbData {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if s.wbd == nil {
		s.wbd = &wbData{target: "Persona"}
	}
	return s.wbd
}

func (s *Service) WebBase(context.Context) string          { return s.wb().web }
func (s *Service) Location(context.Context) *time.Location { return time.UTC }
func (s *Service) ClipFallback(text string) string         { return clip(text, 1500) }

func (s *Service) WorkbenchAgents(context.Context, time.Time) (assistantwire.WorkbenchCounts, []assistantwire.WorkbenchAgent) {
	d := s.wb()
	return d.counts, d.agents
}

func (s *Service) WorkbenchInbox() []assistantwire.WorkbenchItem { return slices.Clone(s.wb().inbox) }

func (s *Service) WorkbenchItem(id string) (assistantwire.WorkbenchItem, bool) {
	for _, it := range s.wb().inbox {
		if it.ID == id {
			return it, true
		}
	}
	return assistantwire.WorkbenchItem{}, false
}

func (s *Service) WorkbenchBoard(context.Context, i18n.Lang, string, time.Time) []assistantwire.WorkbenchBoardRow {
	return s.wb().board
}

func (s *Service) NextMeeting(context.Context, time.Time) *assistantwire.WorkbenchMeeting { return nil }

func (s *Service) TodayBody(context.Context) json.RawMessage { return s.wb().today }

func (s *Service) TasksOverview(context.Context) ([]*botcard.Card, []string) { return nil, nil }

func (s *Service) BoardCard(context.Context, time.Time) (*botcard.Card, string, string) {
	return botcard.NewCard("Board", botcard.Blue, "Board"), "board", "board"
}

func (s *Service) FleetReady() bool { return true }

func (s *Service) InboxDone(_ context.Context, id string, dismissed bool) error {
	d := s.wb()
	s.tmu.Lock()
	defer s.tmu.Unlock()
	d.done = append(d.done, fmt.Sprintf("%s dismissed=%v", id, dismissed))
	return nil
}

func (s *Service) InboxMute(_ context.Context, source, tag, value, name string) error {
	d := s.wb()
	s.tmu.Lock()
	defer s.tmu.Unlock()
	d.mutes = append(d.mutes, source+" "+tag+"="+value+" "+name)
	return nil
}

func (s *Service) DraftReply(_ context.Context, id, instruction string, _ int, _ bool) (assistantwire.WorkbenchItem, string, error) {
	d := s.wb()
	s.tmu.Lock()
	defer s.tmu.Unlock()
	d.drafts = append(d.drafts, id+": "+instruction)
	return assistantwire.WorkbenchItem{ID: id}, d.draft, nil
}

func (s *Service) ReplyInbox(_ context.Context, id, text, clientID string, _ int) (bool, error) {
	d := s.wb()
	s.tmu.Lock()
	defer s.tmu.Unlock()
	d.replies = append(d.replies, id+" "+clientID+": "+text)
	return false, nil
}

// ---- a to-do's topic lines' part of the host ----

// threadData is the fake core's to-do threads, and the timers the lines asked for (tests set them).
type threadData struct {
	bound map[string][2]string // to-do → chat, thread
	later []func()
}

func (s *Service) th() *threadData {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	if s.thd == nil {
		s.thd = &threadData{bound: map[string][2]string{}}
	}
	return s.thd
}

func (s *Service) ThreadOf(id string) (string, string, bool) {
	d := s.th()
	s.tmu.Lock()
	defer s.tmu.Unlock()
	b, ok := d.bound[id]
	return b[0], b[1], ok
}

func (s *Service) TodoLink(web, id string) string { return web + "/todos/" + id }
func (s *Service) PostNow() time.Time             { return s.clock() }

// PostLater keeps the line's timer for the test to fire (fireLater).
func (s *Service) PostLater(_ time.Duration, f func()) {
	d := s.th()
	s.tmu.Lock()
	defer s.tmu.Unlock()
	d.later = append(d.later, f)
}

// fireLater runs the timers asked for so far.
func (s *Service) fireLater() {
	d := s.th()
	s.tmu.Lock()
	fs := d.later
	d.later = nil
	s.tmu.Unlock()
	for _, f := range fs {
		f()
	}
}
