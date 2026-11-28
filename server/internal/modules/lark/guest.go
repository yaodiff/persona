package lark

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Group guests (docs/extensions/bots.md "Group access for teammates"). A non-owner's @bot the policy let through runs
// as a guest conversation, one per chat and thread: its own directory, no memory, no skills, no snapshot, and only the
// reply tool plus the read-only connector tools the owner listed. It never enters the owner's conversations.

const (
	guestConvsKey = "bots.feishu.guest_convs"

	guestStateAccepted = "accepted"
	guestStatePosted   = "posted"
	guestStateClosed   = "closed"

	guestConvKeepFor    = 7 * 24 * time.Hour
	guestConvsKeep      = 200
	guestTurnRunningFor = 10 * time.Minute
	guestReplyMax       = 1800
	guestCotStepsMax    = 20
)

// guestConvRecord is one guest conversation, keyed by the chat and the thread (or root, or message) it answers.
type guestConvRecord struct {
	Key       string           `json:"key"`
	ChatID    string           `json:"chat_id"`
	Machine   string           `json:"machine,omitempty"`
	Session   string           `json:"session,omitempty"`
	Tools     []string         `json:"tools,omitempty"` // owner-listed tools the session started with, sorted
	StartedAt time.Time        `json:"started_at"`
	LastAt    time.Time        `json:"last_at"`
	Turn      *guestTurnRecord `json:"turn,omitempty"`
}

// guestTurnRecord is the question the conversation answers now.
type guestTurnRecord struct {
	ID     string    `json:"id"`
	State  string    `json:"state"` // accepted | posted | closed
	At     time.Time `json:"at"`
	CotID  string    `json:"cot_id,omitempty"`
	CotMsg string    `json:"cot_msg,omitempty"`
	CotOff bool      `json:"cot_off,omitempty"`
	Steps  int       `json:"steps,omitempty"`
}

// guestBook is the module's record of the guest conversations it runs, kept in the settings row guestConvsKey.
type guestBook struct {
	mu      sync.Mutex
	loaded  bool
	records map[string]*guestConvRecord
	now     func() time.Time // nil is time.Now; tests set it so they need no sleep
	// cot serializes one conversation's agent-message calls, so mu is never held across a call to Lark.
	cot map[string]*sync.Mutex
}

// cotLock takes key's agent-message lock and returns its unlock. b.mu must not be held.
func (b *guestBook) cotLock(key string) func() {
	b.mu.Lock()
	if b.cot == nil {
		b.cot = map[string]*sync.Mutex{}
	}
	m := b.cot[key]
	if m == nil {
		m = &sync.Mutex{}
		b.cot[key] = m
	}
	b.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func (b *guestBook) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

func (t *turns) guests() *guestBook {
	if t.guest == nil {
		t.guest = &guestBook{records: map[string]*guestConvRecord{}}
	}
	return t.guest
}

// guestGone is the text of larkhost.ErrGuestGone. The module matches it by text: importing internal/larkhost
// would reach the core from inside a module, which the architecture guard forbids.
const guestGone = "the guest conversation is gone"

// guestKey is one conversation per chat and thread (or root, or message).
func guestKey(chat, conv string) string { return chat + "/" + conv }

// Guest is feishu.guest: the manager accepted a non-owner's @bot. The call returns once the turn is kept; the guest
// run starts afterwards. A conversation still answering its last question refuses with assistantwire.CodeGuestBusy.
func (l *Layer) Guest(ctx context.Context, p assistantwire.FeishuGuestTurn) error {
	st, err := l.Status(ctx)
	if err != nil {
		return err
	}
	if !st.Ready {
		return errors.New("feishu: the bot is not ready")
	}
	s := l.chat()
	key := guestKey(string(p.ChatID), p.Conversation)
	if busy, err := s.guestBegin(ctx, key, string(p.ChatID), string(p.ID)); err != nil {
		return err
	} else if busy {
		return &rpc.Error{Code: assistantwire.CodeGuestBusy, Message: "the conversation still answers its last question"}
	}
	s.t.workers.Add(1)
	go func() {
		defer s.t.workers.Done()
		s.runGuest(p)
	}()
	return nil
}

// guestBegin stores the new turn, or says the conversation is still busy with its last one.
func (s core) guestBegin(ctx context.Context, key, chat, id string) (bool, error) {
	b := s.t.guests()
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.guestLoad(ctx); err != nil {
		return false, err
	}
	now := b.clock()
	if r := b.records[key]; r != nil && r.Turn != nil && r.Turn.State == guestStateAccepted && now.Sub(r.Turn.At) < guestTurnRunningFor {
		return true, nil
	}
	r := b.records[key]
	if r == nil {
		r = &guestConvRecord{Key: key, ChatID: chat, StartedAt: now}
		b.records[key] = r
	}
	r.LastAt = now
	r.Turn = &guestTurnRecord{ID: id, State: guestStateAccepted, At: now}
	s.guestTrim(now)
	return false, s.guestSave(ctx)
}

// runGuest renders the turn and starts, or reuses, the guest conversation.
func (s core) runGuest(p assistantwire.FeishuGuestTurn) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	hello := s.hello(ctx)
	key := guestKey(string(p.ChatID), p.Conversation)
	r := s.guestGet(key)
	first := r == nil || r.Session == ""
	text := prompts.RenderBotGuestTurn(prompts.BotGuestTurn{Owner: strings.TrimSpace(hello.Owner), Language: s.h.AnswerLanguage(hello.Locale),
		Chat: string(p.ChatID), Sender: p.SenderName, Question: p.Question, Max: guestReplyMax, First: first, Tools: len(p.Tools) > 0})
	id := string(p.ID)
	if r != nil && r.Session != "" && s.t.guests().clock().Sub(r.StartedAt) < guestConvKeepFor && slices.Equal(r.Tools, sortedTools(p.Tools)) {
		if err := s.h.SendGroupGuest(ctx, r.Machine, r.Session, id, text); err == nil {
			return
		} else if err.Error() != guestGone {
			s.logf("lark: guest %s: send: %v", id, err)
			s.guestFail(key, "failed")
			return
		}
	}
	machine, session, err := s.h.StartGroupGuest(ctx, key, text, p.Launcher, p.Tools)
	if err != nil {
		s.logf("lark: guest %s: start: %v", id, err)
		s.guestFail(key, "failed")
		return
	}
	s.guestBind(key, machine, session, sortedTools(p.Tools))
}

func sortedTools(tools []string) []string {
	out := append([]string(nil), tools...)
	slices.Sort(out)
	return out
}

// GuestToolCall runs the guest's reply tool. A call of any other tool is not the module's (own is false), so the
// caller runs it. A bad reply comes back as an error result and counts nothing, so the model can shorten it and call
// once more.
func (l *Layer) GuestToolCall(ctx context.Context, key, session, name string, args json.RawMessage) (proto.ToolResult, bool) {
	if name != botGroupReplyTool {
		return proto.ToolResult{}, false
	}
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return proto.TextResult("bot_group_reply needs {\"text\": \"...\"}", true), true
	}
	text := strings.TrimSpace(in.Text)
	s := l.chat()
	r := s.guestGet(key)
	switch {
	case r == nil || r.Turn == nil || r.Turn.State != guestStateAccepted || s.t.guests().clock().Sub(r.Turn.At) > guestTurnRunningFor,
		// Only the session the conversation is bound to answers its turn: a session left behind by an unanswered turn
		// or a changed tool list must not post under a newer question.
		r.Session != "" && session != r.Session:
		return proto.TextResult("this request is closed; end your turn", true), true
	case text == "":
		return proto.TextResult("bot_group_reply needs a text", true), true
	case utf8.RuneCountInString(text) > guestReplyMax:
		return proto.TextResult("the reply is over "+strconv.Itoa(guestReplyMax)+" characters; shorten it and call bot_group_reply once more", true), true
	}
	posted := botcard.GuestSafeText(text, s.guestHosts(ctx)) + "\n" + i18n.T(s.botLang(ctx), "assistant-feishu.handoff_reply_footer", i18n.Args{"owner": strings.TrimSpace(s.hello(ctx).Owner)})
	if err := s.appCall(ctx, assistantwire.FeishuText, map[string]any{"guest": r.Turn.ID, "text": posted}, nil); err != nil {
		s.logf("lark: guest %s: post: %v", r.Turn.ID, err)
		s.guestFail(key, "failed")
		return proto.TextResult("the reply could not be posted; end your turn", true), true
	}
	s.guestSetState(key, guestStatePosted)
	s.guestCotFinish(ctx, key, true)
	return proto.TextResult("posted", false), true
}

// guestHosts is the host of the hub's public URL, which a guest's reply must not link to.
func (s core) guestHosts(ctx context.Context) []string {
	st, err := s.feishuStatus(ctx)
	if err != nil || st.PublicURL == "" {
		return nil
	}
	return []string{st.PublicURL}
}

// GuestTick ends turns nobody answered (the core's minute sweep runs it).
func (l *Layer) GuestTick(ctx context.Context) {
	s := l.chat()
	now := s.t.guests().clock()
	for _, key := range s.guestDue(now) {
		s.guestFail(key, "no_reply")
	}
}

// guestFail ends a turn and tells the manager, which frees the source. A COT that was opened is closed with an error.
func (s core) guestFail(key, outcome string) {
	r := s.guestGet(key)
	if r == nil || r.Turn == nil || r.Turn.State == guestStateClosed || r.Turn.State == guestStatePosted {
		return
	}
	id := r.Turn.ID
	s.guestSetState(key, guestStateClosed)
	if outcome == "no_reply" {
		// The session may still be busy with this question: the next one gets a fresh session, so a late answer
		// cannot land under it (GuestToolCall checks the session).
		s.guestBind(key, "", "", nil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s.guestCotFinish(ctx, key, false)
	if err := s.appCall(ctx, assistantwire.FeishuGuestEnd, map[string]any{"guest": id, "outcome": outcome}, nil); err != nil {
		s.logf("lark: guest %s: end %s: %v", id, outcome, err)
	}
}

// ---- the book ----

func (s core) guestLoad(ctx context.Context) error {
	b := s.t.guests()
	if b.loaded {
		return nil
	}
	m := map[string]*guestConvRecord{}
	if _, err := s.h.Setting(ctx, guestConvsKey, &m); err != nil {
		return err
	}
	if m == nil {
		m = map[string]*guestConvRecord{}
	}
	b.records, b.loaded = m, true
	return nil
}

func (s core) guestSave(ctx context.Context) error {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.h.PutSetting(sctx, guestConvsKey, s.t.guests().records)
}

// guestTrim drops the oldest conversations past the cap and the ones idle past guestConvKeepFor. b.mu is held.
func (s core) guestTrim(now time.Time) {
	b := s.t.guests()
	for key, r := range b.records {
		if now.Sub(r.LastAt) > guestConvKeepFor {
			delete(b.records, key)
		}
	}
	for len(b.records) > guestConvsKeep {
		var oldest string
		var at time.Time
		for key, r := range b.records {
			if oldest == "" || r.LastAt.Before(at) {
				oldest, at = key, r.LastAt
			}
		}
		delete(b.records, oldest)
	}
}

func (s core) guestGet(key string) *guestConvRecord {
	b := s.t.guests()
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.guestLoad(context.Background()); err != nil {
		return nil
	}
	r := b.records[key]
	if r == nil {
		return nil
	}
	return guestCopy(r)
}

func guestCopy(r *guestConvRecord) *guestConvRecord {
	cp := *r
	cp.Tools = slices.Clone(r.Tools)
	if r.Turn != nil {
		t := *r.Turn
		cp.Turn = &t
	}
	return &cp
}

func (s core) guestBind(key, machine, session string, tools []string) {
	b := s.t.guests()
	b.mu.Lock()
	defer b.mu.Unlock()
	if r := b.records[key]; r != nil {
		r.Machine, r.Session, r.Tools = machine, session, tools
		if r.StartedAt.IsZero() {
			r.StartedAt = b.clock()
		}
		_ = s.guestSave(context.Background())
	}
}

func (s core) guestSetState(key, state string) {
	b := s.t.guests()
	b.mu.Lock()
	defer b.mu.Unlock()
	if r := b.records[key]; r != nil && r.Turn != nil {
		r.Turn.State = state
		_ = s.guestSave(context.Background())
	}
}

// guestDue are the conversations whose turn has been accepted for longer than guestTurnRunningFor.
func (s core) guestDue(now time.Time) []string {
	b := s.t.guests()
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.guestLoad(context.Background()); err != nil {
		return nil
	}
	var out []string
	for key, r := range b.records {
		if r.Turn != nil && r.Turn.State == guestStateAccepted && now.Sub(r.Turn.At) > guestTurnRunningFor {
			out = append(out, key)
		}
	}
	return out
}
