package extmgr

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	mrand "math/rand/v2"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/botport"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Messages to the bot (docs/extensions/bots.md "How a message flows"), the hub's part: who may talk to it, pairing, and handing
// the owner's messages and card presses to the assistant (feishu.message, feishu.card_action), which does the rest. A
// message waits for the assistant while it is not connected (a hub start, an assistant restart: toAssistant); one it
// can't be handed is answered that the assistant can't be reached.

// botEvent takes one event off the long connection. Messages are handled in the background (Feishu waits 3 s for the
// answer); card buttons answer right away.
func (h *Manager) botEvent(ctx context.Context, payload []byte) (any, error) {
	h.bots.mu.Lock()
	api := h.bots.api
	h.bots.mu.Unlock()
	return h.botEventHandler(api)(ctx, payload)
}

// Each production connection keeps its source even if its callback runs after
// restartBot replaces it. A new connection for the same app can still use the
// stable message references reserved by its predecessor.
func (h *Manager) botEventHandler(api botport.Client) botcard.Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		return h.botEventFrom(ctx, payload, api)
	}
}

func (h *Manager) botEventFrom(ctx context.Context, payload []byte, api botport.Client) (any, error) {
	env, err := botcard.ParseEnvelope(payload)
	if err != nil {
		return nil, nil // something else (a 1.0 event, the url check): nothing to do
	}
	switch env.Header.EventType {
	case botcard.EventMessage:
		var ev botcard.MessageEvent
		if err := json.Unmarshal(env.Event, &ev); err != nil {
			return nil, err
		}
		// The chat of the owner's own messages in the DM and in a listed group is kept (before the claim: a redelivery
		// changes nothing). Nobody else's: a stranger writing to the bot must not push out what a reply goes to.
		b := &h.bots
		b.mu.Lock()
		if !b.acceptBotEventLocked(api, env.Header.AppID) {
			b.mu.Unlock()
			return nil, nil
		}
		set := b.set
		if set.Owner != nil && ev.Message.MessageID != "" && ev.Message.ChatID != "" && ev.Sender.SenderID.OpenID == set.Owner.OpenID &&
			(ev.Message.ChatType == "p2p" || set.topicChat(ev.Message.ChatID)) {
			b.msgChat.put(ev.Message.MessageID, ev.Message.ChatID)
		}
		queued, claimed := b.claimBotMessageLocked(ev)
		if !claimed {
			b.mu.Unlock()
			return nil, nil // redelivered
		}
		// Its place in line is taken here, as it comes: each handled on its own, a message sent right after another
		// could be handed to the assistant first, and the conversation read them the wrong way round.
		done := make(chan struct{})
		prev := b.line
		b.line = done
		b.mu.Unlock()
		go h.botMessage(ev, env.Event, time.Now(), prev, done, queued)
	case botcard.EventCardAction:
		h.bots.mu.Lock()
		current := h.bots.acceptBotEventLocked(api, env.Header.AppID)
		h.bots.mu.Unlock()
		if !current {
			return nil, nil
		}
		var a botcard.CardAction
		if err := json.Unmarshal(env.Event, &a); err != nil {
			return nil, err
		}
		return h.botCardAction(ctx, &a, env.Event), nil
	case botcard.EventRecalled:
		var ev botcard.Recalled
		if json.Unmarshal(env.Event, &ev) == nil {
			h.botRecalled(ev, api, env.Header.AppID)
		}
	case botcard.EventP2PEntered:
		h.bots.mu.Lock()
		current := h.bots.acceptBotEventLocked(api, env.Header.AppID)
		h.bots.mu.Unlock()
		if !current {
			return nil, nil
		}
		var ev botcard.P2PEntered
		if json.Unmarshal(env.Event, &ev) == nil {
			go h.botWelcome(ev.OperatorID.OpenID, ev.ChatID)
		}
	}
	return nil, nil
}

// botRecalled cancels a queued owner message locally, or tells the assistant
// about a recall after handoff has begun. Group recalls require an exact known
// owner message because the recall event does not identify its sender.
func (h *Manager) botRecalled(ev botcard.Recalled, api botport.Client, appID string) {
	if ev.MessageID == "" || ev.RecallType != "" && ev.RecallType != "message_owner" {
		return
	}
	b := &h.bots
	b.mu.Lock()
	set := b.set
	if !b.acceptBotEventLocked(api, appID) || set.Owner == nil || !botChatAllowed(set, ev.ChatID) {
		b.mu.Unlock()
		return
	}
	ref := botMessageReference(b.api, set.Owner.OpenID, ev.ChatID, ev.MessageID)
	state, known := b.recentMessages[ref]
	queued := b.queuedMessages[ref]
	if queued != nil && !queued.handed && state != botMessageHandedDM && state != botMessageHandedGroup {
		queued.recalled = true
		b.rememberBotMessageLocked(ev.MessageID)
		b.mu.Unlock()
		return
	}
	if state == botMessageCanceled {
		b.rememberBotMessageLocked(ev.MessageID)
		b.mu.Unlock()
		return
	}
	if ev.ChatID != set.Owner.ChatID && state != botMessageHandedGroup ||
		queued == nil && !known && b.messageInOtherContextLocked(ref) {
		b.mu.Unlock()
		return
	}
	if queued != nil {
		queued.recalled = true
	}
	if queued != nil || known {
		b.rememberBotMessageLocked(ev.MessageID)
	}
	b.mu.Unlock()
	if err := h.notifyAssistant(assistantwire.EventFeishuRecalled, assistantwire.FeishuRecalled{MessageID: ev.MessageID}); err != nil {
		h.logf("bots: tell the assistant of a recall: %v", err)
	}
}

// botSeenKeep is how many message ids the hub remembers: Feishu delivers again what it thinks was missed. The
// assistant keeps its own (bots.feishu.state), across restarts.
const botSeenKeep = 300

// claimBotMessageLocked dedupes and reserves pending ownership in the same
// critical section as the message's queue position. Recent history can evict
// an id, but cannot make a second handler for the same pending reference.
func (b *botHub) claimBotMessageLocked(ev botcard.MessageEvent) (*botQueuedMessage, bool) {
	id := ev.Message.MessageID
	ref := botMessageReference(b.api, ev.Sender.SenderID.OpenID, ev.Message.ChatID, id)
	if b.queuedMessages[ref] != nil || b.recentMessages[ref] == botMessageCanceled {
		return nil, false
	}
	if id == "" || slices.Contains(b.seen, id) {
		return nil, false
	}
	b.rememberBotMessageLocked(id)
	return b.queueMessageLocked(ev), true
}

// Refreshing a completed pending id gives immediate redelivery the same bounded
// protection as a new arrival, without retaining completed pending records.
func (b *botHub) rememberBotMessageLocked(id string) {
	b.seen = slices.DeleteFunc(b.seen, func(seen string) bool { return seen == id })
	b.seen = append(b.seen, id)
	if len(b.seen) > botSeenKeep {
		b.seen = b.seen[len(b.seen)-botSeenKeep:]
	}
}

// feishuMaxUUID is the open platform's uuid length cap on the send/reply message APIs.
const feishuMaxUUID = 50

// feishuReplyUUIDIn is the deterministic idempotency key for a reply: same parent message, same purpose (kind:
// c card, f rich-text fallback, t plain text, n note) and same content replay the same key even across a restart;
// different content (the several cards of one turn, or the card vs its fallback) gets a different key, so the
// gateway does not dedupe it. inThread: the reply may go into the parent's thread. The thread flag is part of
// the key: otherwise a plain reply and a thread reply with the same parent and content would share one, and the
// gateway would drop whichever came second within its dedupe window. Not in a thread, the key is the same bytes as
// ever.
func feishuReplyUUIDIn(messageID, kind, content string, inThread bool) string {
	if inThread {
		content = "thread\x00" + content
	}
	sum := sha256.Sum256([]byte(content))
	fp := hex.EncodeToString(sum[:6])
	u := "r:" + messageID + ":" + kind + fp
	if len(u) <= feishuMaxUUID {
		return u
	}
	keep := feishuMaxUUID - len("r:") - len(":"+kind) - len(fp)
	if keep < 1 {
		keep = 1
	}
	return "r:" + messageID[:keep] + ":" + kind + fp
}

// feishuSendUUID is a one-logical-send key for sends without a parent message to anchor to (pairing notes, test
// messages): random per call, never content-derived, so two legitimate sends are never deduped; the client's retries
// still replay this same key.
func feishuSendUUID() string {
	var b [8]byte
	if _, err := crand.Read(b[:]); err != nil {
		// crypto/rand failing is extraordinary; all-zero keys would make the gateway dedupe every send, so mix the
		// clock with the process RNG instead.
		binary.LittleEndian.PutUint64(b[:], uint64(time.Now().UnixNano())^mrand.Uint64())
	}
	return "s:" + hex.EncodeToString(b[:6])
}

// botTell refuses a non-owner, at most every 10 minutes per person: a stranger's client must not spam the owner's
// bot. The pairing path has its own short window (botPairTellFor), so a legitimate retry is never swallowed.
func (h *Manager) botTell(api botport.Client, openID, chatID, text string) {
	b := &h.bots
	h.botNoteThrottled(api, openID, chatID, text, &b.told, 10*time.Minute)
}

// botPairTellFor is the pairing path's window: it only absorbs a redelivery of the same message, not a person's
// repeated tries.
const botPairTellFor = 30 * time.Second

// botPairTell answers the pairing welcome hint at most every botPairTellFor per person. Wrong-code and expired-code
// notes are not throttled here: botPair marks its own per-text redelivery key, then sends them via botSendNote.
func (h *Manager) botPairTell(api botport.Client, openID, chatID, text string) {
	b := &h.bots
	h.botNoteThrottled(api, openID, chatID, text, &b.pairTold, botPairTellFor)
}

// botNoteThrottled sends one text note per openID per window, remembering when in the *told map (held under b.mu).
func (h *Manager) botNoteThrottled(api botport.Client, openID, chatID, text string, told *map[string]time.Time, window time.Duration) {
	b := &h.bots
	b.mu.Lock()
	if *told == nil {
		*told = map[string]time.Time{}
	}
	if t, ok := (*told)[openID]; ok && b.clock().Sub(t) < window {
		b.mu.Unlock()
		return
	}
	(*told)[openID] = b.clock()
	b.mu.Unlock()
	h.botSendNote(api, chatID, text)
}

// botSendNote sends one text note into a chat without any throttling.
func (h *Manager) botSendNote(api botport.Client, chatID, text string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := api.Send(ctx, "chat_id", chatID, botcard.TypeText, botcard.TextContent(text), feishuSendUUID()); err != nil {
		h.logf("bots: tell: %v", err)
	}
}

var regexpDigits = lazyre.New(`^\d{4,8}$`)

func (h *Manager) botWelcome(openID, chatID string) {
	b := &h.bots
	b.mu.Lock()
	api, owner := b.api, b.set.Owner
	b.mu.Unlock()
	if api == nil || owner != nil || openID == "" || chatID == "" {
		return
	}
	h.botPairTell(api, openID, chatID, i18n.T(h.userLang(), "hub-bots_chat.pair_hint"))
}

// botTurnWait is how long a message waits for the one before it to be handed to the assistant. That is quick, unless
// the assistant is away: then the one before waits for it up to inboundWait (a try may run 20 s past that). After
// this the message goes on anyway.
func botTurnWait() time.Duration { return inboundWait + 25*time.Second }

// botMessage handles one message to the bot, which came at since: the owner's go to the assistant, after the one
// before it (prev; nil for none) is handed over. done is closed once this one is, whatever became of it.
func (h *Manager) botMessage(ev botcard.MessageEvent, raw json.RawMessage, since time.Time, prev, done chan struct{}, queued *botQueuedMessage) {
	if queued != nil {
		defer func() {
			h.bots.mu.Lock()
			if h.bots.queuedMessages[queued.ref] == queued {
				if queued.recalled && !queued.handed {
					h.bots.rememberBotMessageSourceLocked(queued.ref, botMessageCanceled)
				}
				delete(h.bots.queuedMessages, queued.ref)
				h.bots.rememberBotMessageLocked(queued.ref.id)
			}
			h.bots.mu.Unlock()
		}()
	}
	passed := false
	pass := func() {
		if !passed {
			passed = true
			close(done)
		}
	}
	defer pass()
	if prev != nil {
		t := time.NewTimer(botTurnWait())
		select {
		case <-prev:
		case <-t.C:
		}
		t.Stop()
	}
	b := &h.bots
	b.mu.Lock()
	api, set := b.api, b.set.copy()
	stop := queued != nil && (queued.recalled || set.Owner == nil || queued.ref != botMessageReference(api, set.Owner.OpenID, ev.Message.ChatID, ev.Message.MessageID))
	b.mu.Unlock()
	if stop {
		return
	}
	msg := ev.Message
	sender := ev.Sender.SenderID.OpenID
	if api == nil || ev.Sender.SenderType != "user" || sender == "" {
		return
	}
	if msg.ChatType != "p2p" {
		// A group chat: the owner's messages in a topic of a chat the owner opted in, and the command that opts one in
		// (bots_topics.go). A message from anyone else is judged by the group access policy (bots_guest.go) and never
		// reaches the owner's conversations. Everything else is dropped without a word, and the owner's private chat
		// (Owner.ChatID) is never rewritten from here.
		h.botGroupMessage(api, set, ev, raw, since, pass)
		return
	}
	if set.Owner == nil {
		content := botcard.ParseContent(msg.MessageType, msg.Content, msg.Mentions)
		h.botPair(api, sender, msg.ChatID, strings.TrimSpace(content.Text))
		return
	}
	if sender != set.Owner.OpenID {
		h.botTell(api, sender, msg.ChatID, i18n.T(h.userLang(), "hub-bots_chat.owner_only"))
		return
	}
	if msg.ChatID != set.Owner.ChatID { // the owner's chat with the bot is the same one; keep it current anyway
		b.mu.Lock()
		if b.set.Owner != nil {
			b.set.Owner.ChatID = msg.ChatID
			h.saveBotSettingsLocked()
		}
		b.mu.Unlock()
	}
	// The owner wrote it (the hub checked the sender). The action keeps the old
	// question-only answer capability and a separate, exact direct-parent proof
	// for server-owned permission proposals. It never broadens legacy typed
	// answers to plans, permissions or terminal dialogs.
	content := botcard.ParseContent(msg.MessageType, msg.Content, msg.Mentions)
	action := h.issueOwnerReplyAction(channel.Feishu, assistantwire.OwnerApprovalProof{
		MessageID: msg.MessageID, ParentID: msg.ParentID, ChatID: msg.ChatID, Text: content.Text,
	})
	h.handToAssistant(api, ev, raw, action, "", since, pass, false)
}

// handToAssistant hands the owner's message to the assistant; one it cannot take is answered that it could not be
// sent (in the topic, for a message that came in one). pass releases the next message's wait once this one is handed
// over, whatever came of it. from is FeishuMessage.From: assistantwire.FeishuFromElsewhere for the owner's "/ai" from a
// group that is not opted in, whose note goes under that message and nowhere else.
func (h *Manager) handToAssistant(api botport.Client, ev botcard.MessageEvent, raw json.RawMessage, action, from string, since time.Time, pass func(), inThread bool) {
	msg := ev.Message
	if !h.beginBotHandoff(api, ev, inThread) {
		return
	}
	err := h.toAssistantSince(assistantwire.MethodFeishuMessage, assistantwire.FeishuMessage{Event: raw, Action: action, From: from}, since)
	pass() // handed over (or not): the next one need not wait for the note below
	if err == nil {
		return
	}
	if !errors.Is(err, errAssistantOffline) {
		h.logf("bots: hand the message to the assistant: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	text := i18n.T(h.userLang(), "hub-bots_chat.not_sent", i18n.Args{"error": clip(oneLine(err.Error()), 200)})
	if from != "" {
		// Under the owner's "/ai" message, and only there: were it gone, the note is lost rather than written into a
		// group that is not the bot's to write in (replyOrSendOpt, no_detour).
		content := botcard.TextContent(text)
		if _, _, err := h.replyOrSendOpt(ctx, api, msg.ChatID, msg.MessageID, botcard.TypeText, content, feishuReplyUUIDIn(msg.MessageID, "n", text, false), false, true); err != nil {
			h.logf("bots: note: %v", err)
		}
		return
	}
	h.botNoteIn(ctx, api, msg.ChatID, msg.MessageID, text, inThread)
}

// botCardAction answers a press on one of the bot's cards: only the owner's count, and the assistant answers them.
func (h *Manager) botCardAction(ctx context.Context, a *botcard.CardAction, raw json.RawMessage) any {
	api, set := h.botAPI()
	if set.Owner == nil || a.Operator.OpenID != set.Owner.OpenID {
		return toast("error", i18n.T(h.userLang(), "hub-bots_chat.owner_only_action"))
	}
	// A press counts only in the owner's private chat or a group the owner has opted in (so a card left in a group the
	// owner took out again is dead), and only the owner's: a card shown to a group is shown to everyone in it. Where
	// the callback names no chat, the chat of the card's message decides, as for a reply's parent; one that cannot be
	// told is refused rather than let through.
	chat := a.Context.ChatID
	if chat == "" {
		chat = h.cardChat(ctx, api, a.Context.MessageID)
	}
	if !botChatAllowed(set, chat) {
		key := "hub-bots_chat.chat_not_enabled"
		if chat == "" {
			key = "hub-bots_chat.chat_unknown"
		}
		return toast("error", i18n.T(h.userLang(), key))
	}
	// The owner pressed it (checked here): the assistant answers the card with this, once (interactions.resolve).
	action := ""
	if a.Str("a") == "ix" && a.Str("id") != "" {
		action = h.issueOwnerAction(channel.Feishu, a.Str("id"))
	}
	var out json.RawMessage
	// Feishu waits 3 s: a moment for an assistant that is reconnecting, then the call itself.
	err := h.callAssistantWaiting(assistantwire.MethodFeishuAction, assistantwire.FeishuAction{Event: raw, Action: action}, &out,
		time.Now().Add(2500*time.Millisecond), 2*time.Second)
	switch {
	case errors.Is(err, errAssistantOffline):
		return toast("error", errAssistantOffline.Error())
	case err != nil:
		return toast("error", clip(oneLine(err.Error()), 80))
	case len(out) == 0 || string(out) == "null":
		return nil
	}
	return out
}

// cardChat is the chat of the message a card press names when the callback does not say: what the hub remembers of the
// message, else the Open Platform's answer, asked within the time Feishu leaves for the callback. "" when it cannot be
// told. Only a chat the bot may use is remembered, as in checkReplyParent.
func (h *Manager) cardChat(ctx context.Context, api botport.Client, messageID string) string {
	if messageID == "" || api == nil {
		return ""
	}
	if chat := h.messageChat(messageID); chat != "" {
		return chat
	}
	ctx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancel()
	items, err := api.Message(ctx, messageID)
	if err != nil {
		h.logf("bots: which chat is the card %s in: %v", messageID, err)
		return ""
	}
	for _, it := range items {
		if it.MessageID == messageID {
			if _, set := h.botAPI(); botChatAllowed(set, it.ChatID) {
				h.rememberMessageChat(messageID, it.ChatID)
			}
			return it.ChatID
		}
	}
	return ""
}

func toast(kind, text string) *botcard.CardResponse {
	return &botcard.CardResponse{Toast: &botcard.Toast{Type: kind, Content: text}}
}

// botPair: the message is the code shown in the app, or the sender is told how to pair. Paired, the assistant is told
// (it starts the chat afresh and says hello); while it is not connected the hub says hello itself.
func (h *Manager) botPair(api botport.Client, openID, chatID, text string) {
	b := &h.bots
	b.mu.Lock()
	// Feishu can redeliver the very same message; claimBotMessageLocked drops a repeated id, but a short per-text window
	// also covers a duplicate that reached us under another id. It must not swallow a genuine retry: a different
	// answer (the next code guessed) is a new try, so the key is the person plus the exact text.
	dupKey := openID + "|" + text
	if t, ok := b.pairTold[dupKey]; ok && b.clock().Sub(t) < botPairTellFor {
		b.mu.Unlock()
		return
	}
	p := b.set.Pairing
	live := p != nil && b.clock().Before(p.Expires)
	ok := live && text == p.Code
	noteKey := "hub-bots_chat.pair_hint"
	var noteArgs i18n.Args
	var record *botRecord
	previousOwner, previousPairing := b.set.Owner, b.set.Pairing
	if !ok && live && regexpDigits.MatchString(text) {
		// A code-shaped answer while a pairing is still live is a real try: say specifically that it was wrong and
		// how many tries remain, instead of the generic hint. At the cap the pairing is dropped, so the note says
		// the code is gone. A time-expired pairing is not "wrong": it keeps the generic hint and no failure.
		// Keep the previous code, expiry and attempt count available until this write commits.
		next := *p
		p = &next
		b.set.Pairing = p
		if p.Fails++; p.Fails >= botPairTries {
			b.set.Pairing = nil // guessed at: the user asks for a new code in the app
			noteKey = "hub-bots_chat.pair_expired"
			record = &botRecord{auditBotPairGuessed, map[string]any{"fails": p.Fails}}
		} else {
			noteKey = "hub-bots_chat.pair_wrong"
			noteArgs = i18n.Args{"n": botPairTries - p.Fails}
		}
	}
	if ok {
		b.set.Owner = &BotOwner{OpenID: openID, ChatID: chatID, At: b.clock().UTC()}
		b.set.Pairing = nil
		record = &botRecord{auditBotPaired, map[string]any{"open_id": openID}}
	}
	if ok || (!ok && live && regexpDigits.MatchString(text)) {
		if err := h.saveBotSettingsLocked(); err != nil {
			b.set.Owner, b.set.Pairing = previousOwner, previousPairing
			b.mu.Unlock()
			// Do not throttle an uncommitted attempt: sending the same code again is a legitimate retry.
			h.botSendNote(api, chatID, i18n.T(h.userLang(), "hub-bots_chat.pair_save_failed"))
			return
		}
	}
	if !ok {
		now := b.clock()
		if b.pairTold == nil {
			b.pairTold = map[string]time.Time{}
		}
		b.pairTold[dupKey] = now
		if len(b.pairTold)%64 == 0 { // bound the map: keys are useless past the short window
			for k, at := range b.pairTold {
				if now.Sub(at) >= botPairTellFor {
					delete(b.pairTold, k)
				}
			}
		}
	}
	b.mu.Unlock()
	if record != nil { // done in the chat: the Feishu account that sent the message did it
		h.audit(context.Background(), channel.Feishu, record.action, botChatActor(openID), record.detail)
	}
	if !ok {
		h.botSendNote(api, chatID, i18n.T(h.userLang(), noteKey, noteArgs))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	name := h.botName(ctx, api, openID)
	b.mu.Lock()
	if b.set.Owner != nil && b.set.Owner.OpenID == openID {
		previousName := b.set.Owner.Name
		b.set.Owner.Name = name
		if err := h.saveBotSettingsLocked(); err != nil {
			// Pairing is already durable. Only this optional display-name enrichment failed.
			b.set.Owner.Name = previousName
		}
	}
	b.mu.Unlock()
	if h.notifyAssistant(assistantwire.EventFeishuOwner, assistantwire.FeishuOwner{Paired: true}) == nil {
		return
	}
	l := h.userLang()
	body := i18n.T(l, "hub-bots_chat.paired_body")
	card := botcard.NewCard(i18n.T(l, "hub-bots_chat.paired"), botcard.Green, i18n.T(l, "hub-bots_chat.paired")).Markdown(body)
	if _, err := h.botPost(ctx, api, chatID, "", card, body); err != nil {
		h.logf("bots: paired note: %v", err)
	}
}

func (h *Manager) botFeatures() BotFeatures {
	h.bots.mu.Lock()
	defer h.bots.mu.Unlock()
	return h.bots.set.Features
}

// botName is a user's name (the contact API; the app may not have that permission, then the hub's owner name).
func (h *Manager) botName(ctx context.Context, api botport.Client, openID string) string {
	b := &h.bots
	b.mu.Lock()
	if n, ok := b.names[openID]; ok {
		b.mu.Unlock()
		return n
	}
	owner := b.set.Owner
	b.mu.Unlock()
	name, err := api.UserName(ctx, openID)
	if err != nil || name == "" {
		name = "某人"
		if owner != nil && owner.OpenID == openID {
			name = h.ownerName()
		}
	}
	b.mu.Lock()
	if b.names == nil {
		b.names = map[string]string{}
	}
	b.names[openID] = name
	b.mu.Unlock()
	return name
}

// feishuKeyRE is what a caller's key (feishu.post / feishu.text uuid) may be made of: letters, digits, ":", "_" and "-".
var feishuKeyRE = lazyre.New(`^[A-Za-z0-9:_-]+$`)

// callerKey is the key a call names, cut to max characters, or "" when it names none or one that is not made of the
// characters above (it is then ignored, and the key is the one the call would have had without it).
func callerKey(key string, max int) string {
	if !feishuKeyRE.MatchString(key) {
		return ""
	}
	return cutUTF8(key, max)
}

// cutUTF8 is s cut to at most n bytes without splitting a character.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// replyOrSend replies to replyTo when its parent still stands, or sends into chatID when replyTo is "". A definitive
// 230011/230110 (parent withdrawn/deleted, which proves the reply never landed) reroutes the same content to a chat
// send with a fresh one-shot key and rerouted=true; transport or other failures come back untouched, so one blip
// never becomes two messages. inThread puts a reply in the parent's thread (reply_in_thread); it means nothing for a
// send into the chat, so a rerouted send ignores it too.
func (h *Manager) replyOrSend(ctx context.Context, api botport.Client, chatID, replyTo, msgType, content, replyUUID string, inThread bool) (id string, rerouted bool, err error) {
	return h.replyOrSendOpt(ctx, api, chatID, replyTo, msgType, content, replyUUID, inThread, false)
}

// replyOrSendOpt is replyOrSend that, with noDetour, does not reroute a reply whose parent is gone: the error comes
// back (botcard.MessageGone tells it). For a caller whose line belongs under that parent or nowhere (a to-do's line
// into its topic): in the chat it would be a stray message.
func (h *Manager) replyOrSendOpt(ctx context.Context, api botport.Client, chatID, replyTo, msgType, content, replyUUID string, inThread, noDetour bool) (id string, rerouted bool, err error) {
	if replyTo == "" {
		id, err = api.Send(ctx, "chat_id", chatID, msgType, content, feishuSendUUID())
		return
	}
	id, err = api.Reply(ctx, replyTo, msgType, content, replyUUID, inThread)
	if !botcard.MessageGone(err) {
		return
	}
	if noDetour {
		h.logf("bots: parent message %s was withdrawn or deleted; not resending the reply into chat %s (the caller asked not to)", replyTo, chatID)
		return
	}
	h.logf("bots: parent message %s was withdrawn or deleted; resending the answer into chat %s", replyTo, chatID)
	id, err = api.Send(ctx, "chat_id", chatID, msgType, content, feishuSendUUID())
	rerouted = true
	return
}

// errBotChatDenied is an rpc.Error with a code, so the assistant can tell a refusal (nothing was sent) from a call that
// failed some other way (it may have gone out).
var errBotChatDenied = &rpc.Error{Code: assistantwire.CodeChatDenied, Message: "feishu: the bot only sends into the owner's private chat and the group chats the owner opted in"}

// botChatAllowed says whether the bot may send into chatID on the assistant's behalf, and take a recall or a card press
// from it: the owner's private chat, and the group chats the owner opted in (FeishuBotSettings.TopicChats). Everything
// else is refused.
func botChatAllowed(set FeishuBotSettings, chatID string) bool {
	if set.Owner == nil || chatID == "" {
		return false
	}
	return chatID == set.Owner.ChatID || set.topicChat(chatID)
}

// botSendChats lists the chats botChatAllowed lets the bot send into: the owner's private chat (when it is known),
// then the opted-in groups. feishu.status carries it so the assistant can tell before it posts.
func botSendChats(set FeishuBotSettings) []string {
	var out []string
	for _, id := range append([]string{set.Owner.ChatID}, topicChatIDs(set)...) {
		if botChatAllowed(set, id) && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

func topicChatIDs(set FeishuBotSettings) []string {
	ids := make([]string, 0, len(set.TopicChats))
	for _, c := range set.TopicChats {
		ids = append(ids, c.ChatID)
	}
	return ids
}

// botSendChat is the chat a feishu.post / feishu.text goes into: the owner's private chat when the caller names none,
// else the one it names, if botChatAllowed lets it.
func botSendChat(set FeishuBotSettings, chatID string) (string, error) {
	if set.Owner == nil {
		return "", errBotOff
	}
	if chatID == "" {
		return set.Owner.ChatID, nil
	}
	if !botChatAllowed(set, chatID) {
		return "", errBotChatDenied
	}
	return chatID, nil
}

// botNoteIn replies to a message with a line of text; a parent withdrawn meanwhile gets the note in the chat instead.
// inThread: it may go into the parent's thread, for a message that came in a topic.
func (h *Manager) botNoteIn(ctx context.Context, api botport.Client, chatID, messageID, text string, inThread bool) {
	content := botcard.TextContent(text)
	if _, _, err := h.replyOrSend(ctx, api, chatID, messageID, botcard.TypeText, content, feishuReplyUUIDIn(messageID, "n", text, inThread), inThread); err != nil {
		h.logf("bots: note: %v", err)
	}
}

// botPost sends a card: as a reply to replyTo, else into the chat. A card Feishu refuses goes again as rich text
// (fallback, else the card's own Markdown is lost: then a line saying so).
func (h *Manager) botPost(ctx context.Context, api botport.Client, chatID, replyTo string, card *botcard.Card, fallback string) (string, error) {
	return h.botPostJSON(ctx, api, chatID, replyTo, card.JSON(), fallback)
}

func (h *Manager) botPostJSON(ctx context.Context, api botport.Client, chatID, replyTo, card, fallback string) (string, error) {
	return h.botPostJSONKeyed(ctx, api, chatID, replyTo, card, fallback, "", false)
}

// botPostJSONKeyed is botPostJSON with the caller's key: the same key, the card and its fallback each land once
// however often the send is made again, and two sends under different keys are two messages even with the same words
// under one parent. "" is a fresh one-shot key for a send into the chat, the parent-and-content key for a reply.
// inThread puts a reply (and its rich-text fallback) in the parent's thread.
func (h *Manager) botPostJSONKeyed(ctx context.Context, api botport.Client, chatID, replyTo, card, fallback, key string, inThread bool) (string, error) {
	return h.botPostJSONKeyedOpt(ctx, api, chatID, replyTo, card, fallback, key, inThread, false)
}

// botPostJSONKeyedOpt is botPostJSONKeyed with noDetour (replyOrSendOpt).
func (h *Manager) botPostJSONKeyedOpt(ctx context.Context, api botport.Client, chatID, replyTo, card, fallback, key string, inThread, noDetour bool) (string, error) {
	key = cutUTF8(key, feishuMaxUUID-4)
	cardUUID := feishuSendUUID()
	switch {
	case key != "": // the caller's key wins, for a reply too: it can tell two cards with the same words apart
		cardUUID = "k:" + key + ":c"
	case replyTo != "":
		cardUUID = feishuReplyUUIDIn(replyTo, "c", card, inThread)
	}
	send := func(typ, content, uuid string) (string, error) {
		if replyTo == "" && key != "" {
			return api.Send(ctx, "chat_id", chatID, typ, content, uuid) // the caller's key, not a fresh one
		}
		id, rerouted, err := h.replyOrSendOpt(ctx, api, chatID, replyTo, typ, content, uuid, inThread, noDetour)
		if rerouted {
			replyTo = "" // a fallback after the detour must go to the chat too, never retry the dead reply
		}
		return id, err
	}
	id, err := send(botcard.TypeInteractive, card, cardUUID)
	if err != nil && botcard.CardRejected(err) {
		h.logf("bots: card refused (%v); sending it as rich text", err)
		if fallback == "" {
			fallback = i18n.T(h.botLang(ctx), "hub-bots_chat.card_refused")
		}
		fallbackUUID := feishuSendUUID()
		switch {
		case key != "":
			fallbackUUID = "k:" + key + ":f"
		case replyTo != "":
			fallbackUUID = feishuReplyUUIDIn(replyTo, "f", fallback, inThread)
		}
		return send(botcard.TypePost, botcard.PostMarkdown(fallback), fallbackUUID)
	}
	return id, err
}

// uploadBytes saves a file on a machine (its temp directory, like the app's uploads) and returns the path.
func (h *Manager) uploadBytes(ctx context.Context, m *Machine, name string, data []byte) (string, error) {
	var begin struct {
		UploadID string `json:"upload_id"`
	}
	if err := m.call(ctx, "file.upload.begin", map[string]any{"name": name, "size": len(data)}, &begin); err != nil {
		return "", err
	}
	for off := 0; off < len(data); off += proto.UploadChunkBytes {
		end := min(off+proto.UploadChunkBytes, len(data))
		if err := m.call(ctx, "file.upload.chunk", map[string]any{"upload_id": begin.UploadID, "offset": off, "data": data[off:end]}, nil); err != nil {
			_ = m.call(context.WithoutCancel(ctx), "file.upload.abort", map[string]any{"upload_id": begin.UploadID}, nil)
			return "", err
		}
	}
	var out proto.UploadedFile
	if err := m.call(ctx, "file.upload.end", map[string]any{"upload_id": begin.UploadID}, &out); err != nil {
		return "", err
	}
	return out.Path, nil
}
