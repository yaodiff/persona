package extmgr

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/botport"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A guest trigger (docs/extensions/bots.md "Group access for teammates"). Each non-owner @bot the policy lets through is
// kept here, not in AISources: that list is capped and holds the owner's /ai sources for 90 days, and guest traffic
// would evict them. Each source may get one reply, under its own message.

type botGuestSource struct {
	ID        string    `json:"id"` // "bg_" + 16 hex (handoffHex(8))
	ChatID    string    `json:"chat_id"`
	MessageID string    `json:"message_id"` // the trigger: the reply's only parent
	Conv      string    `json:"conv"`       // thread_id, else root_id, else message_id
	InThread  bool      `json:"in_thread,omitempty"`
	Sender    string    `json:"sender"` // open id; never a name, never the words
	At        time.Time `json:"at"`
	Replies   int       `json:"replies,omitempty"`
	Cot       bool      `json:"cot,omitempty"`   // its one agent message was created
	Ended     string    `json:"ended,omitempty"` // posted | failed | no_reply | expired
}

const (
	botGuestSourcesKeep    = 300
	botGuestSourcesKeepFor = 24 * time.Hour   // the daily cap reads them
	botGuestReplyFor       = 30 * time.Minute // a reply later than this is refused
	botGuestRunningFor     = 10 * time.Minute // an unfinished turn counts as running, and its thread as busy
	botGuestQuestionMax    = 2000             // runes
	botGuestPerSenderHour  = 10
	botGuestPerDay         = 200
	botGuestRunning        = 3
	botGuestNoteEvery      = 10 * time.Minute // one rate-limit note per chat
	botGuestAsksKeep       = 20               // recent refused @bot requests, memory only
	botGuestKeyPrefix      = "bg:"            // idempotency key of the reply
)

// botGuestAsk is one refused @bot, kept in memory so the owner can allow or block the person from the settings page.
type botGuestAsk struct {
	OpenID string
	ChatID string
	Name   string
	At     time.Time
}

// botGuestMessage takes a group message from someone who is not the owner (bots_topics.go calls it and returns).
// It never touches the owner's path: no handToAssistant, no feishu.message, no change of the owner's chat. It lets the
// next message through before it does anything a stranger could slow down.
func (h *Manager) botGuestMessage(api botport.Client, set FeishuBotSettings, ev botcard.MessageEvent, pass func()) {
	pass()
	msg := ev.Message
	sender := ev.Sender.SenderID.OpenID
	switch set.guestAccess(sender, msg.ChatID) {
	case botGuestBlockedUser, botGuestBlockedChat:
		return
	case botGuestOwnerOnly, botGuestChatNotAllowed, botGuestUserNotAllowed:
		if mentionsBot(msg.Mentions, h.cachedBotOpenID()) {
			h.noteGuestAsk(sender, msg.ChatID)
		}
		return
	}
	if msg.MessageType != "text" && msg.MessageType != "post" || msg.MessageID == "" {
		return
	}
	content := botcard.ParseContent(msg.MessageType, msg.Content, msg.Mentions)
	if content.Forward {
		return
	}
	botID := h.botOpenID(api)
	if botID == "" || !mentionsBot(msg.Mentions, botID) {
		return
	}
	question := strings.TrimSpace(content.StripLeadingMentions(msg.Mentions, nil))
	if question == "" {
		return
	}
	if strings.HasPrefix(question, "/") || strings.HasPrefix(question, "／") { // i18n-ignore: the full-width slash
		return // /topics, /ai, /read, /stop and every other command stay the owner's
	}
	if utf8.RuneCountInString(question) > botGuestQuestionMax {
		h.guestNote(api, msg.ChatID, msg.MessageID, msg.ThreadID != "", "hub-bots_chat.guest_too_long", "")
		return
	}
	conv, inThread := guestConversation(msg.ThreadID, msg.RootID, msg.MessageID)
	b := &h.bots
	b.mu.Lock()
	if err := h.loadBotsLocked(context.Background()); err != nil {
		b.mu.Unlock()
		return
	}
	set = b.set.copy()
	if set.guestAccess(sender, msg.ChatID) != "" || guestByMessage(set.GuestSources, msg.MessageID) != nil {
		b.mu.Unlock()
		return // the policy changed, or this message was already taken
	}
	now := b.clock().UTC()
	if why := set.guestLimits(msg.ChatID, sender, conv, now); why != "" {
		b.mu.Unlock()
		key := "hub-bots_chat.guest_rate_limited"
		if why == "busy" {
			key = "hub-bots_chat.guest_busy"
		}
		h.guestNote(api, msg.ChatID, msg.MessageID, msg.ThreadID != "", key, why)
		return
	}
	src := botGuestSource{ID: "bg_" + handoffHex(8), ChatID: msg.ChatID, MessageID: msg.MessageID, Conv: conv,
		InThread: inThread, Sender: sender, At: now}
	kept := guestKept(set.GuestSources, now)
	kept = append(kept, src)
	if len(kept) > botGuestSourcesKeep {
		kept = kept[len(kept)-botGuestSourcesKeep:]
	}
	before := b.set.GuestSources
	b.set.GuestSources = kept
	if err := h.saveBotSettingsLocked(); err != nil {
		b.set.GuestSources = before
		b.mu.Unlock()
		return
	}
	mode := set.GroupAccess.Mode.Effective()
	tools := append([]string(nil), set.GroupAccess.GuestTools...)
	var launcher string
	if set.Handoff != nil {
		launcher = set.Handoff.Launcher
	}
	b.mu.Unlock()

	name := h.guestName(api, sender)
	h.audit(context.Background(), channel.Feishu, auditBotGuestTriggered, botChatActor(sender), map[string]any{
		"chat_id": msg.ChatID, "guest_id": src.ID, "mode": string(mode), "in_thread": inThread,
	})
	turn := assistantwire.FeishuGuestTurn{ID: assistantwire.FeishuGuestID(src.ID), ChatID: assistantwire.FeishuChatID(msg.ChatID),
		Conversation: conv, SenderName: name, Question: question, Tools: tools, Launcher: launcher, ExpiresAt: now.Add(botGuestReplyFor)}
	if err := h.callAssistantWaiting(assistantwire.MethodFeishuGuest, turn, nil, time.Now().Add(inboundWait), 20*time.Second); err != nil {
		h.finishGuest(src.ID, "failed")
		h.logf("bots: guest %s: %v", src.ID, err)
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == assistantwire.CodeGuestBusy {
			h.guestNote(api, msg.ChatID, msg.MessageID, msg.ThreadID != "", "hub-bots_chat.guest_busy", "busy")
		}
	}
}

// guestName is the sender's display name, "" when it cannot be read. It is not cached: the cache's fallback is the
// owner's name, which a guest must never be shown as.
func (h *Manager) guestName(api botport.Client, sender string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	name, err := api.UserName(ctx, sender)
	if err != nil {
		return ""
	}
	return name
}

// guestConversation is the conversation key: the thread, else the root, else the message itself.
func guestConversation(threadID, rootID, messageID string) (string, bool) {
	if threadID != "" {
		return threadID, true
	}
	if rootID != "" {
		return rootID, false
	}
	return messageID, false
}

// mentionsBot says whether one of the structured mentions is the bot's own open id, the same test as a group's
// require_owner_mention (#10238).
func mentionsBot(mentions []botcard.Mention, botID string) bool {
	if botID == "" {
		return false
	}
	for _, m := range mentions {
		if m.ID.OpenID == botID {
			return true
		}
	}
	return false
}

// cachedBotOpenID is the bot's open id as start-up read it, without a call. A refused trigger must cost none.
func (h *Manager) cachedBotOpenID() string {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.info.OpenID
}

func guestByMessage(list []botGuestSource, messageID string) *botGuestSource {
	for i := range list {
		if list[i].MessageID == messageID {
			return &list[i]
		}
	}
	return nil
}

// guestKept drops sources older than botGuestSourcesKeepFor.
func guestKept(list []botGuestSource, now time.Time) []botGuestSource {
	out := make([]botGuestSource, 0, len(list))
	for _, s := range list {
		if now.Sub(s.At) <= botGuestSourcesKeepFor {
			out = append(out, s)
		}
	}
	return out
}

// guestLimits says why a trigger may not run now: busy (this conversation still answers), chat (the chat's hour),
// sender (the sender's hour), day (the day) or running (too many at once). "" when it may.
func (s FeishuBotSettings) guestLimits(chatID, sender, conv string, now time.Time) string {
	hourly, senderHour, day, running := 0, 0, 0, 0
	cap := botGuestPerChatHourDefault
	if s.GroupAccess != nil {
		cap = s.GroupAccess.perChatHourly()
	}
	for _, g := range s.GuestSources {
		age := now.Sub(g.At)
		if g.Ended == "" && age < botGuestRunningFor && g.ChatID == chatID && g.Conv == conv {
			return "busy"
		}
		if g.ChatID == chatID && age < time.Hour {
			hourly++
		}
		if g.Sender == sender && age < time.Hour {
			senderHour++
		}
		if age < botGuestSourcesKeepFor {
			day++
		}
		if g.Ended == "" && age < botGuestRunningFor {
			running++
		}
	}
	switch {
	case hourly >= cap:
		return "chat"
	case senderHour >= botGuestPerSenderHour:
		return "sender"
	case day >= botGuestPerDay:
		return "day"
	case running >= botGuestRunning:
		return "running"
	}
	return ""
}

// guestNote posts at most one note per chat per botGuestNoteEvery, under the trigger and never into the chat, and
// audits the refusal only when the note is sent.
func (h *Manager) guestNote(api botport.Client, chatID, messageID string, inThread bool, key, reason string) {
	b := &h.bots
	b.mu.Lock()
	now := b.clock()
	if b.guestTold == nil {
		b.guestTold = map[string]time.Time{}
	}
	if last, ok := b.guestTold[chatID]; ok && now.Sub(last) < botGuestNoteEvery {
		b.mu.Unlock()
		return
	}
	b.guestTold[chatID] = now
	b.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	text := i18n.T(h.userLang(), key)
	if _, _, err := h.replyOrSendOpt(ctx, api, chatID, messageID, botcard.TypeText, botcard.TextContent(text),
		feishuReplyUUIDIn(messageID, "n", text, inThread), inThread, true); err != nil {
		h.logf("bots: guest note: %v", err)
	}
	if reason != "" {
		h.audit(context.Background(), channel.Feishu, auditBotGuestRefused, botChatActor(""), map[string]any{"chat_id": chatID, "reason": reason})
	}
}

// noteGuestAsk records a refused structured @bot, deduped by open id and chat, newest last, at most botGuestAsksKeep.
func (h *Manager) noteGuestAsk(openID, chatID string) {
	if openID == "" || chatID == "" {
		return
	}
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock()
	for i := range b.guestAsks {
		if b.guestAsks[i].OpenID == openID && b.guestAsks[i].ChatID == chatID {
			b.guestAsks[i].At = now
			ask := b.guestAsks[i]
			b.guestAsks = append(append(b.guestAsks[:i], b.guestAsks[i+1:]...), ask)
			return
		}
	}
	b.guestAsks = append(b.guestAsks, botGuestAsk{OpenID: openID, ChatID: chatID, At: now})
	if len(b.guestAsks) > botGuestAsksKeep {
		b.guestAsks = b.guestAsks[len(b.guestAsks)-botGuestAsksKeep:]
	}
}

// guestSource is the live source with that id, nil when it is gone or older than botGuestReplyFor. b.mu is held.
func (h *Manager) guestSource(id string, now time.Time) *botGuestSource {
	for i := range h.bots.set.GuestSources {
		if h.bots.set.GuestSources[i].ID == id && now.Sub(h.bots.set.GuestSources[i].At) <= botGuestReplyFor {
			return &h.bots.set.GuestSources[i]
		}
	}
	return nil
}

// useGuestReply counts a reply under the source (at most botSourceReplies) before it is made.
func (h *Manager) useGuestReply(id string) bool {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	src := h.guestSource(id, b.clock().UTC())
	if src == nil || src.Ended != "" || src.Replies >= botSourceReplies {
		return false
	}
	src.Replies++
	h.saveBotSettingsLocked()
	return true
}

// finishGuest ends a source. An outcome already set stays: a posted reply is not rewritten as a failure.
func (h *Manager) finishGuest(id, outcome string) {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.set.GuestSources {
		if b.set.GuestSources[i].ID == id && b.set.GuestSources[i].Ended == "" {
			b.set.GuestSources[i].Ended = outcome
			h.saveBotSettingsLocked()
			return
		}
	}
}

// guestText is feishu.text with guest set: one reply, under the trigger, never a detour into the chat.
func (h *Manager) guestText(ctx context.Context, api botport.Client, id, chatID, replyTo, text string, fullAnswer bool) (any, error) {
	b := &h.bots
	b.mu.Lock()
	set := b.set.copy()
	src := h.guestSource(id, b.clock().UTC())
	var found botGuestSource
	if src != nil {
		found = *src
	}
	b.mu.Unlock()
	switch {
	case src == nil || found.Ended != "":
		return nil, errBotSourceReply
	case chatID != "" && chatID != found.ChatID, replyTo != "" && replyTo != found.MessageID:
		return nil, errBotSourceReply
	case fullAnswer, strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > botSourceTextMax:
		return nil, errBotSourceReply
	case set.guestAccess(found.Sender, found.ChatID) != "":
		return nil, errBotSourceReply
	}
	if !h.useGuestReply(id) {
		return nil, errBotSourceReply
	}
	text = botcard.GuestSafeText(text, h.hubHosts())
	posted, err := api.Reply(ctx, found.MessageID, botcard.TypeText, botcard.TextContent(text), botGuestKeyPrefix+id, found.InThread)
	if err != nil {
		return nil, err
	}
	h.finishGuest(id, "posted")
	h.audit(context.Background(), channel.Feishu, auditBotGuestPosted, botChatActor(found.Sender), map[string]any{"chat_id": found.ChatID, "guest_id": id})
	return map[string]any{"message_id": posted}, nil
}

// guestCotCreate is feishu.cot_create with guest set: one agent message, under the trigger.
func (h *Manager) guestCotCreate(ctx context.Context, api botport.Client, id string) (any, error) {
	b := &h.bots
	b.mu.Lock()
	set := b.set.copy()
	src := h.guestSource(id, b.clock().UTC())
	if src == nil || src.Ended != "" || src.Cot || set.guestAccess(src.Sender, src.ChatID) != "" {
		b.mu.Unlock()
		return nil, errBotSourceReply
	}
	src.Cot = true
	if err := h.saveBotSettingsLocked(); err != nil {
		src.Cot = false
		b.mu.Unlock()
		return nil, err
	}
	chatID, messageID, inThread := src.ChatID, src.MessageID, src.InThread
	b.mu.Unlock()
	cot, mid, err := api.CotCreate(ctx, chatID, messageID, inThread)
	return map[string]any{"cot_id": cot, "message_id": mid}, err
}

// endGuest is feishu.guest_end: a turn over without a post. A second call changes nothing.
func (h *Manager) endGuest(ctx context.Context, id, outcome string) error {
	switch outcome {
	case "failed", "no_reply", "expired":
	default:
		return errBotSourceReply
	}
	b := &h.bots
	b.mu.Lock()
	var chatID string
	changed := false
	for i := range b.set.GuestSources {
		if b.set.GuestSources[i].ID != id {
			continue
		}
		chatID = b.set.GuestSources[i].ChatID
		if b.set.GuestSources[i].Ended == "" {
			b.set.GuestSources[i].Ended = outcome
			h.saveBotSettingsLocked()
			changed = true
		}
		break
	}
	b.mu.Unlock()
	if changed {
		h.audit(ctx, channel.Feishu, auditBotGuestEnded, botChatActor(""), map[string]any{"chat_id": chatID, "guest_id": id, "outcome": outcome})
	}
	return nil
}

// hubHosts is the host of the hub's public URL, the one a guest's reply must not link to.
func (h *Manager) hubHosts() []string {
	if h.cfg == nil || h.cfg.PublicURL == "" {
		return nil
	}
	return []string{h.cfg.PublicURL}
}
