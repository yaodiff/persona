package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/botport"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Topic group chats (#5279, docs/extensions/bots.md "Topic group chats"): the bot's chat with the owner is the private one, and
// anyone in a group could write to a bot in it, so a group is talked to only where the owner said so. The owner opts a
// group in (the /topics command in it, or the bot's settings), and from then on, in that group:
//
//   - a message reaches the assistant when five things all hold: the bot is paired, the sender is the owner, the chat
//     is on the list, the message is in a topic (it has a thread_id), and it is not the command below. A message from
//     anyone else is judged by the group access policy instead (bots_guest.go): allowed, it runs as a guest; otherwise
//     it is dropped without a word. A chat on the blocklist is dropped for everyone, the owner included.
//   - the bot may send into the chat (botChatAllowed).
//
// A group message never changes where the owner's pushes go (BotOwner.ChatID), which stays the private chat.
//
// One more thing is taken from a group that is not on the list (#6146): the owner's "@bot /ai …". It is handed to the
// assistant marked assistantwire.FeishuFromElsewhere, and the message is kept as a source (BotAISource). The bot may
// then reply to that message, and write nothing else in that group: botChatAllowed is unchanged, the group does not
// become a chat the bot may send into, and the reply is a separate rule of feishu.text (aiSourceReply).

// BotChat is a group chat the owner opted in.
type BotChat struct {
	ChatID string `json:"chat_id"`
	Name   string `json:"name,omitempty"` // a label the app may set; the command in the chat leaves it empty
	// Home marks the home topic group (#6146): where a request from a group that is not opted in opens its topic. At
	// most one chat has it; with none marked, the first legacy enrollment is the home (homeChat).
	Home                bool                        `json:"home,omitempty"`
	RequireOwnerMention bool                        `json:"require_owner_mention,omitempty"`
	TodoMode            assistantwire.TopicTodoMode `json:"todo_mode,omitempty"`
	// Space binds this topic group to a domain space slug: unbound topics start isolated conversations in that space.
	Space string `json:"space,omitempty"`
	// Strict enrollments never acquire an implicit home, even after their policy is reversed.
	ExplicitHomeOnly bool      `json:"explicit_home_only,omitempty"`
	At               time.Time `json:"at"`
}

type botChatView struct {
	ChatID              string                      `json:"chat_id"`
	Name                string                      `json:"name,omitempty"`
	AddedAt             int64                       `json:"added_at"`
	Home                bool                        `json:"home,omitempty"` // the effective home topic group
	RequireOwnerMention bool                        `json:"require_owner_mention"`
	TodoMode            assistantwire.TopicTodoMode `json:"todo_mode"`
	Space               string                      `json:"space,omitempty"`
}

// BotAISource is a message the hub took as the owner's "@bot /ai …" in a group chat that is not opted in (#6146).
type BotAISource struct {
	ChatID    string    `json:"chat_id"`
	MessageID string    `json:"message_id"`
	At        time.Time `json:"at"`
	// Replies is how many replies the bot has been let write under it (botSourceReplies at most, botReadReplies for
	// a /read source).
	Replies int `json:"replies,omitempty"`
	// Kind is what the owner asked with: "" for "/ai" (#6146), botSourceRead for "/read" (#11803, bots_read.go), whose
	// answer is cards under the message rather than one short line.
	Kind string `json:"kind,omitempty"`
	// HandoffID is the hub's id of a proxy reply ("bh_" + 16 hex); the rest of these fields belong to one too.
	HandoffID string `json:"handoff_id,omitempty"`
	// Confirm: the owner approves the draft before the bot posts (decided when the handoff was taken).
	Confirm bool `json:"confirm,omitempty"`
	// InThread puts an anchored reply into the anchor's thread.
	InThread bool `json:"in_thread,omitempty"`
	// By is who handed it over: "extension:<id>" or the device actor.
	By string `json:"by,omitempty"`
	// Ref is the caller's idempotency key; RefSum is the sha256 of the caller, the ref and the payload.
	Ref    string `json:"ref,omitempty"`
	RefSum string `json:"ref_sum,omitempty"`
	// Ended is how a proxy reply finished: posted, declined, expired, no_reply or failed.
	Ended string `json:"ended,omitempty"`
}

const (
	// botAISourcesKeep bounds the list; the oldest go first. botAISourcesKeepFor drops the ones nobody replied to for good.
	botAISourcesKeep    = 500
	botAISourcesKeepFor = 90 * 24 * time.Hour
	// botSourceTextMax is the longest reply the bot may write under such a message: one paragraph, not a document.
	botSourceTextMax = 2000
	// botSourceReplies is how many replies the bot may write under one source: the one thing it has to say (a hint, a
	// failure note or the outcome) and one more for a try that must be made again.
	botSourceReplies = 2
)

// botTopicChatsMax bounds the list: it is the owner's own, so this only stops a runaway client.
const botTopicChatsMax = 50

// topicChat says whether chatID is a group chat the owner opted in.
func (s FeishuBotSettings) topicChat(chatID string) bool {
	return chatID != "" && slices.ContainsFunc(s.TopicChats, func(c BotChat) bool { return c.ChatID == chatID })
}

// homeChat uses an explicit home, else the first legacy enrollment. A strict enrollment alone never redirects other groups.
func (s FeishuBotSettings) homeChat() string {
	for _, c := range s.TopicChats {
		if c.Home {
			return c.ChatID
		}
	}
	for _, c := range s.TopicChats {
		if !c.ExplicitHomeOnly {
			return c.ChatID
		}
	}
	return ""
}

// aiSource is the source with that message id, nil when the hub took no such message or took it longer ago than
// botAISourcesKeepFor. A message id is unique, but the chat is kept with it: the reply goes into that chat and no other.
func (s FeishuBotSettings) aiSource(messageID string, now time.Time) *BotAISource {
	if messageID == "" {
		return nil
	}
	for i := range s.AISources {
		if s.AISources[i].Kind == botSourceProxyReply {
			continue // a handoff is never an /ai or /read source (handoffSource finds it)
		}
		if s.AISources[i].MessageID == messageID && now.Sub(s.AISources[i].At) <= botAISourcesKeepFor {
			return &s.AISources[i]
		}
	}
	return nil
}

// checkTopicChats says whether the list a client sent is one the hub keeps: ids that look like a chat id, no repeats.
func checkTopicChats(list []BotChat) error {
	if len(list) > botTopicChatsMax {
		return i18n.Err("hub-bots.topic_chats_many", i18n.Args{"n": botTopicChatsMax})
	}
	seen := map[string]bool{}
	homes := 0
	for _, c := range list {
		id := strings.TrimSpace(c.ChatID)
		if id == "" || len(id) > 100 || strings.ContainsFunc(id, unicode.IsSpace) || seen[id] {
			return i18n.Err("hub-bots.topic_chat_bad")
		}
		seen[id] = true
		if c.Home {
			homes++
		}
	}
	if homes > 1 {
		return i18n.Err("hub-bots.topic_chat_home_many")
	}
	return nil
}

var errNoTopicOwner = i18n.Err("hub-bots.topic_needs_owner")

// topicChatsPossible says whether a PUT that carries a list can keep it: the bot has an owner now, and the same request
// does not change the App ID (which unpairs the owner, and the list with it).
func (h *Manager) topicChatsPossible(ctx context.Context, appID *string) error {
	b := &h.bots
	b.mu.Lock()
	if err := h.loadBotsLocked(ctx); err != nil {
		b.mu.Unlock()
		return err
	}
	owner, saved := b.set.Owner != nil, b.set.AppID
	b.mu.Unlock()
	if !owner {
		return errNoTopicOwner
	}
	if appID != nil && strings.TrimSpace(*appID) != saved {
		return i18n.Err("hub-bots.topic_chats_new_app")
	}
	return nil
}

type topicSpaceStore interface {
	Tracks(ctx context.Context) ([]db.Track, error)
}

// checkTopicSpace validates an optional domain space slug bound to a topic group.
// Empty clears/omits the binding; "health" or any non-archived, non-builtin space track is allowed.
func (h *Manager) checkTopicSpace(ctx context.Context, space string) error {
	space = strings.TrimSpace(space)
	if space == "" {
		return nil
	}
	if len(space) > 100 || strings.ContainsFunc(space, unicode.IsSpace) || strings.Contains(space, "/") || strings.Contains(space, "..") {
		return i18n.Err("hub-bots.topic_space_bad")
	}
	if space == "health" {
		return nil
	}
	st, ok := h.store.(topicSpaceStore)
	if !ok {
		return i18n.Err("hub-bots.topic_space_bad")
	}
	tracks, err := st.Tracks(ctx)
	if err != nil {
		return i18n.Err("hub-bots.settings_unavailable")
	}
	for _, t := range tracks {
		if t.Slug == space && t.ArchivedAt == nil && !t.Builtin && (t.Template == "space" || (t.Template == "" && t.DirName != "")) {
			return nil
		}
	}
	return i18n.Err("hub-bots.topic_space_bad")
}

// setTopicChats replaces the owner's list (PUT /api/v1/bots/feishu): the chats that stay keep the time they were
// added, new ones get now. keepHome: the request said nothing about the home group, so the one the list had stays
// (when it is still in the list). A bot with no owner has no list to keep.
func (h *Manager) setTopicChats(ctx context.Context, want []BotChat, keepHome bool, mentionEdits map[string]bool) error {
	return h.setTopicChatsOpt(ctx, want, keepHome, mentionEdits, nil)
}

func (h *Manager) setTopicChatsOpt(ctx context.Context, want []BotChat, keepHome bool, mentionEdits map[string]bool, spaceEdits map[string]string) error {
	b := &h.bots
	b.mu.Lock()
	if err := h.loadBotsLocked(ctx); err != nil {
		b.mu.Unlock()
		return err
	}
	if b.set.Owner == nil {
		nothing := len(want) == 0 && len(b.set.TopicChats) == 0 // read under the lock
		b.mu.Unlock()
		if nothing {
			return nil
		}
		return errNoTopicOwner
	}
	before := b.set.TopicChats
	next := make([]BotChat, 0, len(want))
	for _, c := range want {
		c.ChatID, c.Name = strings.TrimSpace(c.ChatID), strings.TrimSpace(c.Name)
		c.Space = strings.TrimSpace(c.Space)
		c.At = b.clock().UTC()
		existing := slices.IndexFunc(before, func(o BotChat) bool { return o.ChatID == c.ChatID })
		if i := existing; i >= 0 {
			c.At = before[i].At
			c.RequireOwnerMention = before[i].RequireOwnerMention
			if c.TodoMode == "" {
				c.TodoMode = before[i].TodoMode
			}
			c.Space = before[i].Space
			c.ExplicitHomeOnly = before[i].ExplicitHomeOnly
			if keepHome {
				c.Home = before[i].Home
			}
		}
		if required, ok := mentionEdits[c.ChatID]; ok {
			c.RequireOwnerMention = required
		}
		if sp, ok := spaceEdits[c.ChatID]; ok {
			c.Space = strings.TrimSpace(sp)
		}
		if existing < 0 {
			c.ExplicitHomeOnly = c.RequireOwnerMention
		}
		next = append(next, c)
	}
	records := topicRecords(before, next)
	if was, now := markedHome(before), markedHome(next); now != "" && now != was {
		records = append(records, botRecord{auditBotTopicHome, map[string]any{"chat_id": now}})
	}
	for _, c := range next {
		prevSpace := ""
		if i := slices.IndexFunc(before, func(o BotChat) bool { return o.ChatID == c.ChatID }); i >= 0 {
			prevSpace = before[i].Space
		}
		if c.Space != prevSpace {
			records = append(records, botRecord{auditBotTopicSpace, map[string]any{"chat_id": c.ChatID, "space": c.Space}})
		}
	}
	b.set.TopicChats = next
	if len(records) > 0 || !slices.EqualFunc(before, next, func(a, b BotChat) bool { return a == b }) {
		if err := h.saveBotSettingsLocked(); err != nil {
			b.set.TopicChats = before
			b.mu.Unlock()
			return err
		}
	}
	b.mu.Unlock()
	h.auditBot(ctx, records...)
	return nil
}

// markedHome is the chat marked home in the list, "" for none.
func markedHome(list []BotChat) string {
	for _, c := range list {
		if c.Home {
			return c.ChatID
		}
	}
	return ""
}

// topicRecords are the audit records for the chats that came and went between two lists.
func topicRecords(before, after []BotChat) []botRecord {
	var out []botRecord
	for _, c := range after {
		if !slices.ContainsFunc(before, func(o BotChat) bool { return o.ChatID == c.ChatID }) {
			out = append(out, botRecord{auditBotTopicChatAdded, map[string]any{"chat_id": c.ChatID}})
		}
	}
	for _, c := range before {
		if !slices.ContainsFunc(after, func(o BotChat) bool { return o.ChatID == c.ChatID }) {
			out = append(out, botRecord{auditBotTopicChatRemoved, map[string]any{"chat_id": c.ChatID}})
		}
	}
	return out
}

// The commands /topics takes: it opts the group in, out, makes it the home topic group (opting it in if it is not),
// or binds/clears a domain space for its topics.
const (
	topicsOn    = "on"
	topicsOff   = "off"
	topicsHome  = "home"
	topicsSpace = "space"
)

// topicsCmd reads the owner's message as the command that opts a group in or out: /topics or /话题群 (a full-width
// slash too), alone or with "on" / "off" (关) or "home" (主群 in Chinese), after the @ of the bot that a group
// message may start with. ok is false for any other text, which is then an ordinary message.
func topicsCmd(text string, mentions []botcard.Mention) (cmd string, ok bool) {
	cmd, _, ok = parseTopicsCmd(text, mentions)
	return cmd, ok
}

func parseTopicsCmd(text string, mentions []botcard.Mention) (cmd, arg string, ok bool) {
	t := strings.TrimSpace(text)
	for stripped := true; stripped; {
		stripped = false
		for _, m := range mentions {
			for _, tok := range []string{m.Key, "@" + m.Name} {
				if tok != "" && tok != "@" && strings.HasPrefix(t, tok) {
					t, stripped = strings.TrimSpace(strings.TrimPrefix(t, tok)), true
				}
			}
		}
	}
	if after, found := strings.CutPrefix(t, "／"); found { // i18n-ignore: the full-width slash
		t = "/" + after
	}
	word, ok := strings.CutPrefix(t, "/")
	if !ok {
		return "", "", false
	}
	fields := strings.Fields(word)
	if len(fields) == 0 || len(fields) > 3 {
		return "", "", false
	}
	switch strings.ToLower(fields[0]) {
	case "topics", "话题群": // i18n-ignore: the command's own name
	default:
		return "", "", false
	}
	if len(fields) == 3 {
		switch strings.ToLower(fields[1]) {
		case "space", "空间": // i18n-ignore: the command's own words
			val := strings.TrimSpace(fields[2])
			switch strings.ToLower(val) {
			case "off", "none", "clear", "关", "关闭", "无": // i18n-ignore: the command's own words
				return topicsSpace, "", true
			}
			return topicsSpace, val, true
		}
		return "", "", false
	}
	switch a := strings.ToLower(strings.Join(fields[1:], "")); a {
	case "", "on", "开": // i18n-ignore: the command's own word
		return topicsOn, "", true
	case "off", "关", "关闭": // i18n-ignore: the command's own words
		return topicsOff, "", true
	case "home", "主群": // i18n-ignore: the command's own words
		return topicsHome, "", true
	}
	return "", "", false
}

// botGroupMessage takes a message from a group chat (not the bot's private chat): see the five conditions above, and
// the owner's "@bot /ai …" from a group that is not on the list (botAIElsewhere). Every one that fails is a plain return:
// no note, no reaction, nothing in the chat.
func (h *Manager) botGroupMessage(api botport.Client, set FeishuBotSettings, ev botcard.MessageEvent, raw json.RawMessage, since time.Time, pass func()) {
	msg := ev.Message
	if set.Owner == nil || msg.ChatID == "" {
		return
	}
	if set.groupChatBlocked(msg.ChatID) {
		return // the blocklist wins, over the owner too
	}
	if ev.Sender.SenderID.OpenID != set.Owner.OpenID {
		h.botGuestMessage(api, set, ev, pass) // bots_guest.go; never the owner's path
		return
	}
	// Apply the group's current policy before settings commands or any topic side effect.
	if i := slices.IndexFunc(set.TopicChats, func(c BotChat) bool { return c.ChatID == msg.ChatID }); i >= 0 && set.TopicChats[i].RequireOwnerMention {
		if !mentionsBot(msg.Mentions, h.botOpenID(api)) {
			return
		}
	}
	if msg.MessageType == "text" || msg.MessageType == "post" {
		content := botcard.ParseContent(msg.MessageType, msg.Content, msg.Mentions)
		if cmd, arg, ok := parseTopicsCmd(content.Text, msg.Mentions); ok && !content.Forward {
			// The list is changed before the next message is let go: the one right after /topics off must not pass the
			// gate, and the one right after /topics must.
			key, args, recs := h.applyTopicsCommand(context.Background(), set.Owner.OpenID, msg.ChatID, cmd, arg)
			pass()
			if key == "" {
				return // unpaired meanwhile
			}
			for _, rec := range recs {
				h.audit(context.Background(), channel.Feishu, rec.action, botChatActor(set.Owner.OpenID), rec.detail)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			h.botNoteIn(ctx, api, msg.ChatID, msg.MessageID, i18n.T(h.userLang(), key, args), msg.ThreadID != "")
			return // the hub's own: it never goes to the assistant
		}
	}
	if h.botReadGroup(api, set, ev, raw, since, pass) {
		return // the owner's "@bot /read …", from any group (bots_read.go)
	}
	if !set.topicChat(msg.ChatID) {
		h.botAIElsewhere(api, set, ev, raw, since, pass)
		return
	}
	if msg.ThreadID == "" {
		return
	}
	// No action: a message in a topic answers no card (the assistant does not look for one), so none is issued.
	h.handToAssistant(api, ev, raw, "", "", since, pass, true)
}

// botAIElsewhere takes the owner's "@bot /ai …" from a group chat that is not on the list (#6146, docs/extensions/bots.md "Asking
// from any group"): the one thing such a group gets through. The owner's message (the caller checked), a text or
// rich-text message (a merged forward is nothing to run), that @-mentions the bot, whose words, the leading @ of the bot
// taken off, start with /ai. It is kept as a source (the bot may reply to it, aiSourceReply), audited, and handed to the
// assistant marked FeishuFromElsewhere. Anything else is dropped without a word, as every message of such a group was.
func (h *Manager) botAIElsewhere(api botport.Client, set FeishuBotSettings, ev botcard.MessageEvent, raw json.RawMessage, since time.Time, pass func()) {
	msg := ev.Message
	if msg.MessageType != "text" && msg.MessageType != "post" || msg.MessageID == "" {
		return
	}
	content := botcard.ParseContent(msg.MessageType, msg.Content, msg.Mentions)
	if content.Forward {
		return
	}
	// Cheap first, from the text alone: what does not start with /ai (after the @s it starts with) is not asked about the
	// bot at all, so a group's ordinary messages cost no call.
	if _, ok := botcard.AICommand(content.StripLeadingMentions(msg.Mentions, nil)); !ok {
		return
	}
	botID := h.botOpenID(api)
	if botID == "" {
		return // the bot cannot tell which @ is its own: nothing is taken
	}
	isBot := func(m botcard.Mention) bool { return m.ID.OpenID == botID }
	if !mentionsBot(msg.Mentions, botID) {
		return
	}
	if _, ok := botcard.AICommand(content.StripLeadingMentions(msg.Mentions, isBot)); !ok {
		return
	}
	h.takeAISource(msg.ChatID, msg.MessageID, "")
	h.audit(context.Background(), channel.Feishu, auditBotAITaken, botChatActor(set.Owner.OpenID), map[string]any{"chat_id": msg.ChatID})
	h.handToAssistant(api, ev, raw, "", assistantwire.FeishuFromElsewhere, since, pass, false)
}

// botOpenID is the bot's own open id, which tells its @ from anyone else's: read at start-up, and once more here when
// that did not work.
func (h *Manager) botOpenID(api botport.Client) string {
	b := &h.bots
	b.mu.Lock()
	id := b.info.OpenID
	b.mu.Unlock()
	if id != "" {
		return id
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	info, err := api.BotInfo(ctx)
	if err != nil {
		h.logf("bots: bot info: %v", err)
		return ""
	}
	b.mu.Lock()
	if b.api == api {
		b.info = info
	}
	b.mu.Unlock()
	return info.OpenID
}

// useAISourceReply counts a reply under the source (at most botSourceReplies) before it is made: false when it has had
// them all (or is gone), and then nothing is sent. A reply that fails still counts: the bot does not keep writing under
// a message in a group it is not in charge of.
func (h *Manager) useAISourceReply(messageID string, fullReply bool) bool {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	src := b.set.aiSource(messageID, b.clock().UTC())
	if b.set.Owner == nil || src == nil || src.Replies >= src.repliesMax() || fullReply && !b.set.SourceReportFullReply {
		return false
	}
	src.Replies++
	h.saveBotSettingsLocked()
	return true
}

// takeAISource keeps the message as one the bot may reply to (the same message again changes nothing), dropping the
// oldest past botAISourcesKeep and the ones older than botAISourcesKeepFor.
func (h *Manager) takeAISource(chatID, messageID, kind string) {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.set.Owner == nil || b.set.aiSource(messageID, b.clock().UTC()) != nil {
		return
	}
	now := b.clock().UTC()
	kept := slices.DeleteFunc(slices.Clone(b.set.AISources), func(s BotAISource) bool {
		keep := botAISourcesKeepFor
		if s.Kind == botSourceProxyReply {
			keep = botHandoffKeepFor
		}
		return now.Sub(s.At) > keep
	})
	kept = append(kept, BotAISource{ChatID: chatID, MessageID: messageID, At: now, Kind: kind})
	if len(kept) > botAISourcesKeep {
		kept = kept[len(kept)-botAISourcesKeep:]
	}
	b.set.AISources = kept
	h.saveBotSettingsLocked()
}

// errBotSourceReply: a reply under a message taken through /ai that the bot may not write.
var errBotSourceReply = &rpc.Error{Code: assistantwire.CodeChatDenied, Message: "feishu: the bot may only reply to the original /ai message in its chat using the configured reply format"}

// aiSourceReply says whether a feishu.text is a reply to a message the hub took as the owner's "/ai" in a group that is
// not on the list, and so may go there: found is false when replyTo is no such message (the usual rules decide). For one
// that is, the reply must name no chat but its message's, respect the owner's full answer preference, and never detour
// into the group if the original is gone. This is all that is open in such a group: a send into
// it, a card, a reply to any other message, or a reply with a chat of its own is refused as ever.
func aiSourceReply(set FeishuBotSettings, chatID, replyTo, text string, now time.Time, fullReply bool) (chat string, found bool, err error) {
	src := set.aiSource(replyTo, now)
	if src == nil || set.Owner == nil {
		return "", false, nil
	}
	if chatID != "" && chatID != src.ChatID || !fullReply && utf8.RuneCountInString(text) > botSourceTextMax ||
		fullReply && (!set.SourceReportFullReply || src.Kind == botSourceRead) {
		return "", true, errBotSourceReply
	}
	return src.ChatID, true, nil
}

// applyTopicsCommand opts the group chat in or out for the owner, makes it the home topic group, or binds/clears a
// domain space (whose open id it is, checked by the caller) and returns the key and args of the line that says what
// came of it, with the audit records when the list changed; "" when the owner is gone or another by now.
func (h *Manager) applyTopicsCommand(ctx context.Context, ownerID, chatID, cmd, arg string) (key string, args i18n.Args, recs []botRecord) {
	if cmd == topicsSpace && arg != "" {
		if err := h.checkTopicSpace(ctx, arg); err != nil {
			return "hub-bots_chat.topics_space_bad", i18n.Args{"space": arg}, nil
		}
	}
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	cur := &b.set
	if cur.Owner == nil || cur.Owner.OpenID != ownerID {
		return "", nil, nil
	}
	idx := slices.IndexFunc(cur.TopicChats, func(c BotChat) bool { return c.ChatID == chatID })
	has := idx >= 0
	added := botRecord{auditBotTopicChatAdded, map[string]any{"chat_id": chatID}}
	home := botRecord{auditBotTopicHome, map[string]any{"chat_id": chatID}}
	switch {
	case cmd == topicsOn && has:
		return "hub-bots_chat.topics_already", nil, nil
	case (cmd == topicsOn || cmd == topicsHome || (cmd == topicsSpace && arg != "")) && !has && len(cur.TopicChats) >= botTopicChatsMax:
		return "hub-bots_chat.topics_full", nil, nil
	case cmd == topicsOn:
		cur.TopicChats = append(slices.Clone(cur.TopicChats), BotChat{ChatID: chatID, At: b.clock().UTC()})
		h.saveBotSettingsLocked()
		return "hub-bots_chat.topics_on", nil, []botRecord{added}
	case cmd == topicsHome && has && markedHome(cur.TopicChats) == chatID:
		return "hub-bots_chat.topics_home_already", nil, nil
	case cmd == topicsHome:
		// Opted in with it when it was not on: the owner said so in so many words, and the group is the owner's own pick.
		list := slices.Clone(cur.TopicChats)
		for i := range list {
			list[i].Home = list[i].ChatID == chatID
		}
		key, recs = "hub-bots_chat.topics_home", []botRecord{home}
		if !has {
			list = append(list, BotChat{ChatID: chatID, Home: true, At: b.clock().UTC()})
			key, recs = "hub-bots_chat.topics_on_home", []botRecord{added, home}
		}
		cur.TopicChats = list
		h.saveBotSettingsLocked()
		return key, nil, recs
	case cmd == topicsSpace && arg == "":
		if !has || cur.TopicChats[idx].Space == "" {
			return "hub-bots_chat.topics_space_none", nil, nil
		}
		list := slices.Clone(cur.TopicChats)
		list[idx].Space = ""
		cur.TopicChats = list
		h.saveBotSettingsLocked()
		return "hub-bots_chat.topics_space_cleared", nil, []botRecord{{auditBotTopicSpace, map[string]any{"chat_id": chatID, "space": ""}}}
	case cmd == topicsSpace:
		list := slices.Clone(cur.TopicChats)
		spaceRec := botRecord{auditBotTopicSpace, map[string]any{"chat_id": chatID, "space": arg}}
		if !has {
			list = append(list, BotChat{ChatID: chatID, Space: arg, At: b.clock().UTC()})
			cur.TopicChats = list
			h.saveBotSettingsLocked()
			return "hub-bots_chat.topics_on_space", i18n.Args{"space": arg}, []botRecord{added, spaceRec}
		}
		list[idx].Space = arg
		cur.TopicChats = list
		h.saveBotSettingsLocked()
		return "hub-bots_chat.topics_space", i18n.Args{"space": arg}, []botRecord{spaceRec}
	case has: // off
		cur.TopicChats = slices.DeleteFunc(slices.Clone(cur.TopicChats), func(c BotChat) bool { return c.ChatID == chatID })
		h.saveBotSettingsLocked()
		return "hub-bots_chat.topics_off", nil, []botRecord{{auditBotTopicChatRemoved, map[string]any{"chat_id": chatID}}}
	}
	return "hub-bots_chat.topics_not_on", nil, nil
}

// ---- which chat a message is in ----

// botMsgChatKeep is how many message ids the hub remembers the chat of.
const botMsgChatKeep = 500

// idRing is a bounded set of message ids with what is known of each, oldest dropped first. Held under botHub.mu.
type idRing struct {
	m     map[string]string
	order []string
}

func (r *idRing) put(id, v string) {
	if r.m == nil {
		r.m = map[string]string{}
	}
	if _, ok := r.m[id]; !ok {
		r.order = append(r.order, id)
		if len(r.order) > botMsgChatKeep {
			delete(r.m, r.order[0])
			r.order = r.order[1:]
		}
	}
	r.m[id] = v
}

// rememberMessageChat notes the chat a message the bot may use is in (the owner's own messages in the DM and in a
// listed group, and what the bot itself sent there), for checkReplyParent. Messages of anyone else, and of chats the
// bot may not use, are not kept: they would only push out the ones a reply goes to.
func (h *Manager) rememberMessageChat(id, chatID string) {
	if id == "" || chatID == "" {
		return
	}
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	b.msgChat.put(id, chatID)
}

func (h *Manager) messageChat(id string) string {
	b := &h.bots
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.msgChat.m[id]
}

var errBotReplyChat = &rpc.Error{Code: assistantwire.CodeChatDenied, Message: "feishu: a reply goes into the chat of the message it answers, and that chat is not one the bot may send into"}

// checkReplyParent refuses a reply whose parent message is in a chat the bot may not send into: a reply lands in the
// parent's chat whatever chat_id says. It returns that chat. The chat is the one remembered for the message, else the
// open platform's answer (read again by the client when it is busy or the network blips; then remembered, if it is a
// chat the bot may use). A parent that is withdrawn or deleted cannot be replied to at all, so the send falls back to
// the chat the call names (replyOrSend), which botSendChat checked: the chat returned is "" then. A chat that cannot be
// told is refused. With no parent there is nothing to check.
func (h *Manager) checkReplyParent(ctx context.Context, api botport.Client, set FeishuBotSettings, replyTo string) (string, error) {
	if replyTo == "" {
		return "", nil
	}
	chat := h.messageChat(replyTo)
	if chat == "" {
		items, err := api.Message(ctx, replyTo)
		if botcard.MessageGone(err) {
			return "", nil
		}
		if err != nil {
			return "", fmt.Errorf("feishu: could not tell which chat message %s is in: %w", replyTo, err)
		}
		for _, it := range items {
			if it.MessageID == replyTo {
				chat = it.ChatID
			}
		}
		if chat == "" {
			return "", fmt.Errorf("feishu: could not tell which chat message %s is in", replyTo)
		}
		if botChatAllowed(set, chat) {
			h.rememberMessageChat(replyTo, chat)
		}
	}
	if !botChatAllowed(set, chat) {
		return "", errBotReplyChat
	}
	return chat, nil
}
