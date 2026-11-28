package lark

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Proxy replies (docs/extensions/bots.md "Proxy replies handed off from a group"). A teammate's question handed to the
// bot runs as a guest conversation: its own directory, no memory, no skills, no snapshot and one tool, bot_group_reply,
// which is also how the answer reaches the bot. It never enters the owner's conversations, so it never reaches
// handToAssistant, StartRun, feishuMessage or openFromElsewhere.
//
// Native message_cot is not part of this: a handoff has no channel run, its chat is deliberately not one the bot may
// send into, and the default tier asks the owner before anything is posted. That waits for the guest-safe COT of the
// main proposal.

const (
	botGroupReplyTool = "bot_group_reply"
	handoffsKey       = "bots.feishu.handoffs"

	// handoffDraftMax is the longest draft the owner is asked to approve (the card shows one line of it).
	handoffDraftMax = 400
	// handoffClip is how much of the question a title or a header shows.
	handoffClip = 60

	handoffStatePosted  = "posted"
	handoffStateWaiting = "waiting"
	handoffStateClosed  = "closed"

	// handoffRecordsKeepFor is how long a finished handoff stays in the book; an unfinished one stays until it expires.
	handoffRecordsKeepFor = 7 * 24 * time.Hour
)

// handoffRecord is one proxy reply as the module keeps it. The question is clipped for the confirmation's title; the
// draft is what the owner approves, and exactly what is posted.
type handoffRecord struct {
	ID         string    `json:"id"`
	ChatID     string    `json:"chat_id"`
	ChatName   string    `json:"chat_name,omitempty"`
	SenderName string    `json:"sender_name,omitempty"`
	Question   string    `json:"question_clip,omitempty"`
	Confirm    bool      `json:"confirm"`
	State      string    `json:"state"`
	Machine    string    `json:"machine,omitempty"`
	Session    string    `json:"session,omitempty"`
	DecisionID string    `json:"decision_id,omitempty"`
	Draft      string    `json:"draft,omitempty"`
	PostLabel  string    `json:"post_label,omitempty"`
	At         time.Time `json:"at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// handoffBook is the module's record of the proxy replies it has taken, kept in the settings row handoffsKey.
type handoffBook struct {
	mu       sync.Mutex
	loaded   bool
	records  map[string]*handoffRecord
	deciding map[string]string // decision id → handoff id
}

func (t *turns) handoffs() *handoffBook {
	if t.handoff == nil {
		t.handoff = &handoffBook{records: map[string]*handoffRecord{}, deciding: map[string]string{}}
	}
	return t.handoff
}

// Handoff is feishu.handoff: the manager accepted a group question. The call returns once the handoff is kept; the
// guest run starts afterwards, so a slow model never holds the caller's request. A bot that is not ready refuses it.
func (l *Layer) Handoff(ctx context.Context, p assistantwire.FeishuHandoff) error {
	st, err := l.Status(ctx)
	if err != nil {
		return err
	}
	if !st.Ready {
		return errors.New("feishu: the bot is not ready")
	}
	s := l.chat()
	r := &handoffRecord{ID: string(p.ID), ChatID: string(p.ChatID), ChatName: p.ChatName, SenderName: p.SenderName,
		Question: clipRunes(p.Question, 120), Confirm: p.Confirm, State: "accepted", At: time.Now().UTC(), ExpiresAt: p.ExpiresAt}
	if err := s.handoffPut(ctx, r); err != nil {
		return err
	}
	s.t.workers.Add(1)
	go func() {
		defer s.t.workers.Done()
		s.runHandoff(p)
	}()
	return nil
}

// runHandoff starts the guest and, when that fails, ends the handoff.
func (s core) runHandoff(p assistantwire.FeishuHandoff) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	hello := s.hello(ctx)
	max := handoffReplyMax(p.Confirm)
	text := prompts.RenderBotProxyReply(prompts.BotProxyReply{Owner: strings.TrimSpace(hello.Owner), Language: s.h.AnswerLanguage(hello.Locale),
		Chat: p.ChatName, Sender: p.SenderName, Question: p.Question, Context: p.Context, Max: max, Confirm: p.Confirm})
	id := string(p.ID)
	if machine, session, err := s.h.StartHandoffGuest(ctx, id, text, p.Launcher); err != nil {
		s.logf("lark: handoff %s: start: %v", id, err)
		s.handoffFail(id, "failed")
	} else {
		s.handoffBind(id, machine, session)
	}
}

// handoffReplyMax is how many runes the tool takes: the posted text, header and footer included, stays within what the
// manager accepts, and a draft the owner approves stays within handoffDraftMax.
func handoffReplyMax(confirm bool) int {
	if confirm {
		return handoffDraftMax
	}
	return 1800
}

// HandoffTools is the one tool a proxy reply's guest lists.
func (l *Layer) HandoffTools() []proto.Tool {
	t := prompts.Tool(botGroupReplyTool)
	return []proto.Tool{{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}}
}

// HandoffToolCall runs the guest's one tool. A bad call comes back as an error result and counts nothing, so the model
// can shorten the text and call once more; a second accepted call is refused.
func (l *Layer) HandoffToolCall(ctx context.Context, handoff, name string, args json.RawMessage) proto.ToolResult {
	if name != botGroupReplyTool {
		return proto.TextResult("unknown tool "+name, true)
	}
	var in struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return proto.TextResult("bot_group_reply needs {\"text\": \"...\"}", true)
	}
	text := strings.TrimSpace(in.Text)
	s := l.chat()
	r := s.handoffGet(handoff)
	switch {
	case r == nil || r.State == handoffStateClosed || r.State == handoffStatePosted || time.Now().After(r.ExpiresAt):
		return proto.TextResult("this request is closed; end your turn", true)
	case r.State == handoffStateWaiting:
		return proto.TextResult("this request is closed; end your turn", true)
	case text == "":
		return proto.TextResult("bot_group_reply needs a text", true)
	}
	limit := handoffReplyMax(r.Confirm)
	if utf8.RuneCountInString(text) > limit {
		return proto.TextResult("the reply is over "+strconv.Itoa(limit)+" characters; shorten it and call bot_group_reply once more", true)
	}
	posted := s.handoffCompose(ctx, r, text)
	if !r.Confirm {
		if err := s.handoffSend(ctx, r.ID, posted, false); err != nil {
			s.logf("lark: handoff %s: post: %v", r.ID, err)
			s.handoffFail(r.ID, "failed")
			return proto.TextResult("the reply could not be posted; end your turn", true)
		}
		s.handoffSetState(r.ID, handoffStatePosted, "")
		return proto.TextResult("posted", false)
	}
	if err := s.handoffRaise(ctx, r, posted); err != nil {
		s.logf("lark: handoff %s: confirm: %v", r.ID, err)
		s.handoffFail(r.ID, "failed")
		return proto.TextResult("the reply could not be held for confirmation; end your turn", true)
	}
	return proto.TextResult("held until the owner confirms; end your turn now, do not call again", false)
}

// handoffCompose is what the bot posts, in the owner's language: who asked, the answer, and that it is an AI reply.
func (s core) handoffCompose(ctx context.Context, r *handoffRecord, answer string) string {
	l := s.botLang(ctx)
	header := i18n.T(l, "assistant-feishu.handoff_reply_header", i18n.Args{"sender": handoffFirst(r.SenderName, i18n.T(l, "assistant-feishu.unknown_sender")), "question": clipRunes(r.Question, handoffClip)})
	footer := i18n.T(l, "assistant-feishu.handoff_reply_footer", i18n.Args{"owner": strings.TrimSpace(s.hello(ctx).Owner)})
	return header + "\n" + answer + "\n" + footer
}

// handoffSend posts the reply through the manager. confirmed is the owner's approval, which a confirm handoff needs.
func (s core) handoffSend(ctx context.Context, id, text string, confirmed bool) error {
	return s.appCall(ctx, assistantwire.FeishuText, map[string]any{
		"handoff": id, "text": text, "confirmed": confirmed,
	}, nil)
}

// handoffRaise asks the owner, on the phone, before the bot posts. What they approve is the stored draft.
func (s core) handoffRaise(ctx context.Context, r *handoffRecord, draft string) error {
	l := s.botLang(ctx)
	post := i18n.T(l, "assistant-feishu.handoff_decision_post")
	sender := handoffFirst(r.SenderName, i18n.T(l, "assistant-feishu.unknown_sender"))
	req := assistantwire.OutsideDecisionRequest{
		Ref:        "handoff:" + r.ID,
		Title:      clipRunes(i18n.T(l, "assistant-feishu.handoff_decision_title", i18n.Args{"sender": sender, "chat": handoffFirst(r.ChatName, r.ChatID)}), 200),
		Background: i18n.T(l, "assistant-feishu.handoff_decision_background", i18n.Args{"question": clipRunes(r.Question, handoffClip), "draft": clipRunes(handoffOneLine(draft), handoffDraftMax)}),
		WhyOwner:   "visible_to_others",
		Options: []assistantwire.OutsideDecisionOption{
			{Label: post, LeadsTo: i18n.T(l, "assistant-feishu.handoff_decision_post_leads")},
			{Label: i18n.T(l, "assistant-feishu.handoff_decision_drop"), LeadsTo: i18n.T(l, "assistant-feishu.handoff_decision_drop_leads")},
		},
		Recommend:     post,
		CostOfWaiting: i18n.T(l, "assistant-feishu.handoff_decision_cost", i18n.Args{"sender": sender}),
	}
	no := false
	req.Reversible, req.AllowDefault = &no, &no
	decision, err := s.h.RaiseHandoffDecision(ctx, r.ID, req)
	if err != nil {
		return err
	}
	return s.handoffHold(r.ID, draft, post, decision)
}

// HandoffDecided is the owner answering the confirmation. The post label posts the stored draft; anything else,
// including a typed answer, declines.
func (l *Layer) HandoffDecided(ctx context.Context, handoff, option string) {
	s := l.chat()
	r := s.handoffGet(handoff)
	if r == nil || r.State != handoffStateWaiting {
		return
	}
	if option != "" && option == r.PostLabel {
		if err := s.handoffSend(ctx, r.ID, r.Draft, true); err != nil {
			s.logf("lark: handoff %s: confirmed post: %v", r.ID, err)
			s.handoffFail(r.ID, "failed")
			return
		}
		s.handoffSetState(r.ID, handoffStatePosted, "")
		return
	}
	s.handoffFail(r.ID, "declined")
}

// HandoffTick ends handoffs nobody finished (the core's minute sweep runs it). A restart finishes one whose decision
// was answered while the assistant was down: the decision card is already closed, so the record is expired here and the
// owner is not asked twice. A decision answered after the restart comes through HandoffDecided.
func (l *Layer) HandoffTick(ctx context.Context) {
	s := l.chat()
	now := time.Now()
	for _, r := range s.handoffDue(now) {
		if r.DecisionID != "" {
			s.h.WithdrawHandoffDecision(ctx, r.DecisionID)
		}
		outcome := "expired"
		if r.State == "accepted" {
			outcome = "no_reply"
		}
		s.handoffFail(r.ID, outcome)
	}
}

// handoffFail ends a handoff and tells the manager, which frees the source.
func (s core) handoffFail(id, outcome string) {
	r := s.handoffGet(id)
	if r == nil || r.State == handoffStateClosed || r.State == handoffStatePosted {
		return
	}
	s.handoffSetState(id, handoffStateClosed, outcome)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.appCall(ctx, assistantwire.FeishuHandoffEnd, map[string]any{"handoff": id, "outcome": outcome}, nil); err != nil {
		s.logf("lark: handoff %s: end %s: %v", id, outcome, err)
	}
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func handoffFirst(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func handoffOneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// ---- the book ----

func (s core) handoffLoad(ctx context.Context) error {
	b := s.t.handoffs()
	if b.loaded {
		return nil
	}
	m := map[string]*handoffRecord{}
	if _, err := s.h.Setting(ctx, handoffsKey, &m); err != nil {
		return err
	}
	if m == nil {
		m = map[string]*handoffRecord{}
	}
	b.records = m
	for id, r := range m {
		if r.DecisionID != "" {
			b.deciding[r.DecisionID] = id
		}
	}
	b.loaded = true
	return nil
}

func (s core) handoffSave(ctx context.Context) error {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.h.PutSetting(sctx, handoffsKey, s.t.handoffs().records)
}

func (s core) handoffPut(ctx context.Context, r *handoffRecord) error {
	b := s.t.handoffs()
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.handoffLoad(ctx); err != nil {
		return err
	}
	for id, old := range b.records {
		done := old.State == handoffStateClosed || old.State == handoffStatePosted
		if done && r.At.Sub(old.At) > handoffRecordsKeepFor {
			delete(b.records, id)
			if old.DecisionID != "" {
				delete(b.deciding, old.DecisionID)
			}
		}
	}
	b.records[r.ID] = r
	return s.handoffSave(ctx)
}

func (s core) handoffGet(id string) *handoffRecord {
	b := s.t.handoffs()
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.handoffLoad(context.Background()); err != nil {
		return nil
	}
	r := b.records[id]
	if r == nil {
		return nil
	}
	cp := *r
	return &cp
}

func (s core) handoffBind(id, machine, session string) {
	b := s.t.handoffs()
	b.mu.Lock()
	defer b.mu.Unlock()
	if r := b.records[id]; r != nil {
		r.Machine, r.Session = machine, session
		_ = s.handoffSave(context.Background())
	}
}

func (s core) handoffSetState(id, state, _ string) {
	b := s.t.handoffs()
	b.mu.Lock()
	defer b.mu.Unlock()
	if r := b.records[id]; r != nil {
		r.State = state
		_ = s.handoffSave(context.Background())
	}
}

func (s core) handoffHold(id, draft, postLabel, decision string) error {
	b := s.t.handoffs()
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.records[id]
	if r == nil {
		return errors.New("handoff gone")
	}
	r.Draft, r.PostLabel, r.DecisionID, r.State = draft, postLabel, decision, handoffStateWaiting
	b.deciding[decision] = id
	return s.handoffSave(context.Background())
}

func (s core) handoffDue(now time.Time) []*handoffRecord {
	b := s.t.handoffs()
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := s.handoffLoad(context.Background()); err != nil {
		return nil
	}
	var out []*handoffRecord
	for _, r := range b.records {
		if r.State != handoffStateClosed && r.State != handoffStatePosted && !r.ExpiresAt.IsZero() && now.After(r.ExpiresAt) {
			cp := *r
			out = append(out, &cp)
		}
	}
	return out
}

// handoffByDecision is the handoff a decision card belongs to.
func (s core) handoffByDecision(decision string) string {
	b := s.t.handoffs()
	b.mu.Lock()
	defer b.mu.Unlock()
	_ = s.handoffLoad(context.Background())
	return b.deciding[decision]
}
