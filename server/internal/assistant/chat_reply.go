package assistant

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Replies where a conversation wrote as the user (#10777, docs/assistant/assistant.md "Replies where it wrote as the
// user"). One of the assistant's conversations writes into a Lark chat in the user's name (the node's gate records it
// in the sent-as-user ledger), and the other side answers: the conversation hears of it. The Lark connector hands
// other people's new messages in a chat as a batch (Inbox event grouping, collectSource); each conversation of this
// manager's machine that wrote a message into that chat in the last chatReplyWindow, before the batch's newest
// message, gets the messages it was not told yet through its relay queue (chat_reply), as other people's words. One
// conversation hears of one chat at most once every chatReplyGap; what comes in between merges into the message that
// waits. The Inbox's own handling of the batch is unchanged.

const (
	// chatReplyTool is the relay kind of a chat's new messages told to a conversation that wrote there as the user.
	chatReplyTool = "chat_reply"
	// chatReplyWindow: a send older than this opens nothing, and what was told is forgotten after it.
	chatReplyWindow = 24 * time.Hour
	// chatReplyGap: one conversation hears of one chat at most this often.
	chatReplyGap = 10 * time.Minute
	// chatReplyContextMax is how many characters of the messages' text one message carries: the newest.
	chatReplyContextMax = 3000
	// chatReplyMutedChatsKey is the persona_settings key listing Lark chats muted from chat_reply relays.
	chatReplyMutedChatsKey = "assistant.chat_reply_relay_muted_chats"
	chatReplyMutedChatsMax = 200
	// chatReplyTakeoverKey persists per-chat owner takeover timestamps in the settings store for chatReplyWindow.
	chatReplyTakeoverKey = "assistant.chat_reply_takeover"
	// chatReplyAround is how many messages either side of the newest batch message to fetch for owner-reply detection.
	chatReplyAround = 10
	// chatReplyCheckTimeout bounds the GroupMessagesAround call per batch.
	chatReplyCheckTimeout = 10 * time.Second
	// chatReplyClockSkew tolerates clock skew when matching unreferenced assistant sends by timestamp.
	chatReplyClockSkew = 2 * time.Minute
)

// chatReply is what a chat_reply message tells, kept with it in the queue (relayMsg.Chat) so that a later batch of the
// same chat merges into it, after a restart too.
type chatReply struct {
	Thread string    `json:"thread"`
	Title  string    `json:"title,omitempty"`
	Group  bool      `json:"group,omitempty"`  // a group chat; else a direct one
	Sender string    `json:"sender,omitempty"` // who wrote the newest message
	Sent   time.Time `json:"sent"`             // the conversation's latest send there
	IDs    []string  `json:"ids"`              // the messages it tells, by the connector's ids
	// Earlier: Context also has messages a chat_reply that went in before told.
	Earlier bool   `json:"earlier,omitempty"`
	Context string `json:"context"`
}

type chatReplyMeta struct {
	title string
	group bool
	at    time.Time
}

// chatReplyBook is what each conversation was told of each chat, in memory: after a restart, a batch the Inbox has not
// put in an event yet may be told once more (one queued then is not: its ids are in the queue).
type chatReplyBook struct {
	running sync.Mutex // one round's batches at a time
	mu      sync.Mutex
	told    map[string]*chatTold // by chatReplyKey
	// refused: by conversation, when the owner was pushed that it refused a chat_reply for good (chatReplyRefused); one
	// push until a chat_reply goes into it again or chatReplyWindow is over. In memory: a restart may push once more.
	refused        map[string]time.Time
	meta           map[string]chatReplyMeta // by feed ID
	takeover       map[string]time.Time     // by feed ID: latest time the owner personally spoke there
	takeoverLoaded bool
	takeoverDirty  bool
	takeoverSaveMu sync.Mutex
	wg             sync.WaitGroup // the rounds being told
}

// chatTold is what one conversation was told of one chat: the messages told or queued (when last handed), and when a
// chat_reply last went in.
type chatTold struct {
	ids  map[string]time.Time
	went time.Time
}

func chatReplyKey(to, thread string) string { return to + "\x00" + thread }

// toldLocked is conversation to's record of thread, made when there is none; b.mu is held.
func (b *chatReplyBook) toldLocked(to, thread string) *chatTold {
	if b.told == nil {
		b.told = map[string]*chatTold{}
	}
	key := chatReplyKey(to, thread)
	told := b.told[key]
	if told == nil {
		told = &chatTold{ids: map[string]time.Time{}}
		b.told[key] = told
	}
	return told
}

// pruneLocked forgets messages last handed more than chatReplyWindow ago, and a record left with none once its gap is
// over; b.mu is held.
func (b *chatReplyBook) pruneLocked(now time.Time) {
	for key, told := range b.told {
		for id, at := range told.ids {
			if now.Sub(at) > chatReplyWindow {
				delete(told.ids, id)
			}
		}
		if len(told.ids) == 0 && now.Sub(told.went) > chatReplyGap {
			delete(b.told, key)
		}
	}
	for to, at := range b.refused {
		if now.Sub(at) > chatReplyWindow {
			delete(b.refused, to)
		}
	}
	for feed, m := range b.meta {
		if now.Sub(m.at) > chatReplyWindow {
			delete(b.meta, feed)
		}
	}
	for feed, at := range b.takeover {
		if now.Sub(at) > chatReplyWindow {
			delete(b.takeover, feed)
		}
	}
}

// chatSentStore is the part of the assistant's store that reads the sent-as-user ledger (the manager's Postgres).
type chatSentStore interface {
	SentAsUsersForArchive(context.Context, string, time.Time, time.Time) ([]db.SentAsUser, error)
}

// larkChatFeed is the chat a Lark thread names: "chat:<feed>", "chat:<account>:<feed>" (migratedThread).
func larkChatFeed(thread string) (string, bool) {
	rest, ok := strings.CutPrefix(thread, "chat:")
	if !ok || rest == "" {
		return "", false
	}
	if _, feed, two := strings.Cut(rest, ":"); two {
		return feed, feed != ""
	}
	return rest, true
}

// chatReplyRelayOn is assistant.chat_reply_relay in effect with settings cfg: off unless set to true.
func chatReplyRelayOn(cfg Settings) bool {
	return cfg.ChatReplyRelay != nil && *cfg.ChatReplyRelay
}

// dropQueuedChatReplies removes matching chat_reply messages still waiting in relay queues.
// When feed is empty every queued chat_reply is dropped; when feed is set only that chat is dropped
// (and when before is non-zero, only messages tied to assistant sends at or before before).
// A chat_reply already draining at the head of a queue is skipped here and dropped by the drain via chatReplyStale.
func (s *Service) dropQueuedChatReplies(ctx context.Context, feed string, before time.Time) {
	b := &s.relays
	b.mu.Lock()
	if !s.relayLocked(ctx) {
		b.mu.Unlock()
		return
	}
	changed := false
	for to, q := range b.st.Queue {
		kept := q[:0:0]
		for i, m := range q {
			if m.Tool == chatReplyTool && !(i == 0 && b.draining[to]) && chatReplyMatchesDrop(m, feed, before) {
				changed = true
				continue
			}
			kept = append(kept, m)
		}
		if len(kept) == 0 {
			delete(b.st.Queue, to)
		} else {
			b.st.Queue[to] = kept
		}
	}
	if changed {
		s.saveRelayLocked()
	}
	b.mu.Unlock()
	if changed {
		s.publish(ctx)
	}
}

func chatReplyMatchesDrop(m relayMsg, feed string, before time.Time) bool {
	if feed == "" {
		return true
	}
	if m.Chat == nil {
		return false
	}
	f, ok := larkChatFeed(m.Chat.Thread)
	if !ok || f != feed {
		return false
	}
	return before.IsZero() || !m.Chat.Sent.After(before)
}

// chatReplyStale reports whether a queued chat_reply about to drain should be discarded because the global switch is
// off, the chat is muted, or the owner personally spoke in that chat after the assistant's send.
func (s *Service) chatReplyStale(ctx context.Context, msg relayMsg) bool {
	if msg.Tool != chatReplyTool {
		return false
	}
	cfg := s.settings(ctx)
	if !chatReplyRelayOn(cfg) {
		return true
	}
	if msg.Chat == nil {
		return false
	}
	feed, ok := larkChatFeed(msg.Chat.Thread)
	if !ok {
		return false
	}
	return chatReplyChatMuted(cfg, feed) || s.chatReplyTakenOver(ctx, feed, msg.Chat.Sent, time.Now())
}

func (s *Service) noteChatMeta(feed, title string, group bool, now time.Time) {
	if feed == "" || title == "" {
		return
	}
	b := &s.chatReplies
	b.mu.Lock()
	if b.meta == nil {
		b.meta = map[string]chatReplyMeta{}
	}
	b.meta[feed] = chatReplyMeta{title: title, group: group, at: now}
	b.mu.Unlock()
}

// ensureChatReplyTakeoverLoaded ensures persisted takeover stops are loaded into b.takeover and flushes any
// in-memory entries recorded while an earlier store read failed. Returns false if the store is absent or its read
// fails so callers avoid overwriting previously persisted entries with an incomplete in-memory map.
func (s *Service) ensureChatReplyTakeoverLoaded(ctx context.Context) bool {
	b := &s.chatReplies
	b.mu.Lock()
	if b.takeoverLoaded && !b.takeoverDirty {
		b.mu.Unlock()
		return true
	}
	alreadyLoaded := b.takeoverLoaded
	b.mu.Unlock()

	st := s.store()
	if st == nil {
		b.mu.Lock()
		if b.takeover == nil {
			b.takeover = map[string]time.Time{}
		}
		b.mu.Unlock()
		return false
	}

	var saved map[string]time.Time
	if !alreadyLoaded {
		if _, err := st.Setting(ctx, chatReplyTakeoverKey, &saved); err != nil {
			s.logf("chat replies: takeover state could not be loaded: %v", err)
			b.mu.Lock()
			if b.takeover == nil {
				b.takeover = map[string]time.Time{}
			}
			b.mu.Unlock()
			return false
		}
	}

	needFlush := false
	b.mu.Lock()
	if !b.takeoverLoaded {
		if b.takeover == nil {
			b.takeover = map[string]time.Time{}
		}
		for feed, at := range saved {
			if prev, ok := b.takeover[feed]; !ok || at.After(prev) {
				b.takeover[feed] = at
			}
		}
		b.takeoverLoaded = true
	}
	needFlush = b.takeoverDirty
	b.mu.Unlock()

	if needFlush {
		s.saveChatReplyTakeover(ctx, st, time.Now())
	}
	return true
}

func (s *Service) saveChatReplyTakeover(ctx context.Context, st Store, now time.Time) {
	b := &s.chatReplies
	b.takeoverSaveMu.Lock()
	defer b.takeoverSaveMu.Unlock()
	b.mu.Lock()
	b.pruneLocked(now)
	snap := maps.Clone(b.takeover)
	b.takeoverDirty = false
	b.mu.Unlock()
	if err := st.PutSetting(ctx, chatReplyTakeoverKey, snap); err != nil {
		b.mu.Lock()
		b.takeoverDirty = true
		b.mu.Unlock()
		s.logf("chat replies: takeover state could not be saved: %v", err)
	}
}

func (s *Service) chatReplyTakenOver(ctx context.Context, feed string, sent, now time.Time) bool {
	if feed == "" {
		return false
	}
	s.ensureChatReplyTakeoverLoaded(ctx)
	b := &s.chatReplies
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked(now)
	at, ok := b.takeover[feed]
	return ok && now.Sub(at) <= chatReplyWindow && !sent.After(at)
}

func (s *Service) recordChatReplyTakeover(ctx context.Context, feed string, at, now time.Time) {
	if feed == "" || at.IsZero() {
		return
	}
	loaded := s.ensureChatReplyTakeoverLoaded(ctx)
	b := &s.chatReplies
	b.mu.Lock()
	if b.takeover == nil {
		b.takeover = map[string]time.Time{}
	}
	if prev, ok := b.takeover[feed]; !ok || at.After(prev) {
		b.takeover[feed] = at
	}
	b.pruneLocked(now)
	if !loaded {
		// Store was unavailable or failed to read: keep in memory and flush once a read succeeds so persisted
		// stops for other chats are never clobbered by a partial map.
		b.takeoverDirty = true
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	if st := s.store(); st != nil {
		s.saveChatReplyTakeover(ctx, st, now)
	}
}

// tellChatWriters takes the batches of new messages the Lark connector handed in a round (people's chats, not a
// bot's) and tells the conversations that wrote into their chats as the user, in the background (tellChatBatches).
func (s *Service) tellChatWriters(ctx context.Context, src assistantwire.InboxSource) {
	if src.ID != migratedConnectorID || chatReplyHinted(s.settings(ctx)) && !chatReplyRelayOn(s.settings(ctx)) {
		return // off and the hint was given: nothing to look at
	}
	var batches []connector.InboxThing
	for _, t := range src.Things {
		if g := t.Group; g == nil || g.Batch == "" || len(g.New) == 0 || t.Done != "" || t.Kind == KindBot {
			continue
		}
		if _, ok := larkChatFeed(t.Thread); ok {
			batches = append(batches, t)
		}
	}
	if len(batches) == 0 {
		return
	}
	b := &s.chatReplies
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		s.tellChatBatches(context.WithoutCancel(ctx), batches, time.Now())
	}()
}

// tellChatBatches tells each conversation that wrote a message into a batch's chat as the user, in the last
// chatReplyWindow and before the batch's newest message, the batch's messages it was not told yet. Only the
// assistant's own conversations count: on this manager's machine, not archived, neither the Inbox's (its AI twin
// hears of the chat through the Inbox) nor the background supervisor.
func (s *Service) tellChatBatches(ctx context.Context, batches []connector.InboxThing, now time.Time) {
	ledger, ok := s.store().(chatSentStore)
	machine := s.settings(ctx).MachineID
	if !ok || machine == "" {
		return
	}
	b := &s.chatReplies
	b.running.Lock()
	defer b.running.Unlock()
	rows, err := ledger.SentAsUsersForArchive(ctx, connector.Lark, now.Add(-chatReplyWindow), now.Add(time.Minute))
	if err != nil {
		s.logf("chat replies: the sent-as-user ledger could not be read: %v", err)
		return
	}
	var list []proto.Session
	listed := false
	for _, t := range batches {
		feed, _ := larkChatFeed(t.Thread)
		s.noteChatMeta(feed, clip(oneLine(t.Title), 60), t.Kind != KindDM, now)
		cfg := s.settings(ctx)
		if chatReplyChatMuted(cfg, feed) {
			continue
		}
		var candidates []chatWriter
		for _, w := range chatWriters(rows, machine, feed) {
			if t.At.After(w.at) && now.Sub(w.at) <= chatReplyWindow {
				candidates = append(candidates, w)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		if !listed {
			if list, err = s.sessions(ctx, machine); err != nil {
				s.logf("chat replies: the conversations could not be listed: %v", err) // the batch comes again
				return
			}
			listed = true
		}
		var openWriters []chatWriter
		for _, w := range candidates {
			if s.chatWriterOpen(ctx, machine, w.session, list) {
				openWriters = append(openWriters, w)
			}
		}
		if len(openWriters) == 0 {
			continue
		}
		if !chatReplyRelayOn(cfg) {
			s.hintChatReply(ctx, openWriters[0].session, t)
			return // one hint is all there is while it is off
		}
		var active []chatWriter
		for _, w := range openWriters {
			if !s.chatReplyTakenOver(ctx, feed, w.at, now) {
				active = append(active, w)
			}
		}
		if len(active) == 0 || !s.anyChatBatchFresh(active, t.Thread, t.Group.New, now) {
			continue
		}
		if msgs, ok := s.fetchChatMessagesAround(ctx, t.Thread, t.Group.New[len(t.Group.New)-1]); ok && len(msgs) > 0 {
			var maxTakeover time.Time
			for _, w := range active {
				if at, stopped := ownerSpokeAfterSend(msgs, feed, rows, w); stopped && at.After(maxTakeover) {
					maxTakeover = at
				}
			}
			if !maxTakeover.IsZero() {
				s.recordChatReplyTakeover(ctx, feed, maxTakeover, now)
				s.dropQueuedChatReplies(ctx, feed, maxTakeover)
				kept := active[:0]
				for _, w := range active {
					if w.at.After(maxTakeover) {
						kept = append(kept, w)
					}
				}
				active = kept
			}
		}
		for _, w := range active {
			s.tellChatBatch(ctx, w.session, w.at, t, now)
		}
	}
}

func chatReplyHinted(cfg Settings) bool { return cfg.ChatReplyHinted }

// hintChatReply is the whole of the feature's discovery while it is off: once, ever, the first conversation that
// wrote into a chat as the user and has others' replies there hears that they are not passed on and that the owner
// can turn that on. It carries no one's words and is dropped like a chat_reply is if the setting is turned on
// meanwhile only by being a plain message (it is not a chat_reply, so the drop of queued ones leaves it).
func (s *Service) hintChatReply(ctx context.Context, to string, t connector.InboxThing) {
	won := false
	if _, err := s.updateSettings(ctx, func(v *Settings) error {
		if v.ChatReplyHinted {
			return nil
		}
		v.ChatReplyHinted, won = true, true
		return nil
	}); err != nil || !won {
		return
	}
	lang := answerLanguage(s.hello(ctx).Locale)
	text := prompts.RenderChatReplyHint(prompts.ChatReplyHint{Language: lang, Title: clip(oneLine(t.Title), 60), Group: t.Kind != KindDM})
	s.logf("chat replies: the setting is off; the one-time hint goes to %s", short(to))
	s.relayOrQueue(ctx, to, relayMsg{From: t.Thread, FromTitle: "Persona", Tool: taskEndTool, Text: text, At: time.Now().UTC()}) // i18n-ignore: the queue's label
}

// chatWriter is a conversation that wrote into a chat as the user, and when it last did.
type chatWriter struct {
	session string
	at      time.Time
	ref     string
	rowID   int64
}

// chatWriters are the conversations of machine that wrote a message into chat feed as the user, by the ledger's rows,
// each with its latest such send. A recalled message counts as not sent.
func chatWriters(rows []db.SentAsUser, machine, feed string) []chatWriter {
	var out []chatWriter
	for _, r := range rows {
		if r.RecalledAt != nil || !wroteInto(r, feed) {
			continue
		}
		var o struct {
			Machine string `json:"machine_id"`
			Session string `json:"session_id"`
		}
		if json.Unmarshal(r.Origin, &o) != nil || o.Session == "" || o.Machine != machine {
			continue
		}
		if i := slices.IndexFunc(out, func(w chatWriter) bool { return w.session == o.Session }); i < 0 {
			out = append(out, chatWriter{session: o.Session, at: r.At, ref: r.MessageRef, rowID: r.ID})
		} else if r.At.After(out[i].at) {
			out[i].at = r.At
			out[i].ref = r.MessageRef
			out[i].rowID = r.ID
		} else if r.At.Equal(out[i].at) && (r.MessageRef != "" || out[i].rowID == 0) {
			if r.MessageRef != "" {
				out[i].ref = r.MessageRef
			}
			out[i].rowID = r.ID
		}
	}
	return out
}

// chatMessageRoutes are the gated routes that put a message into the chat their path names (extmgr's createsMessage).
var chatMessageRoutes = []string{"messages", "images", "files", "attachments"}

// wroteFeed extracts the Lark chat feed ID from ledger row r when it wrote a message into a chat (a gated
// POST /conversations/<feed>/<route> of chatMessageRoutes, or a connector send whose target names conversation/chat
// without naming a single message).
func wroteFeed(r db.SentAsUser) (string, bool) {
	var t map[string]any
	if json.Unmarshal(r.Target, &t) != nil {
		return "", false
	}
	str := func(k string) string { v, _ := t[k].(string); return v }
	if p, _, _ := strings.Cut(str("path"), "?"); p != "" {
		segs := strings.Split(strings.Trim(p, "/"), "/")
		if strings.EqualFold(str("method"), "POST") && len(segs) == 3 && segs[0] == "conversations" && segs[1] != "" &&
			slices.Contains(chatMessageRoutes, segs[2]) {
			return segs[1], true
		}
		return "", false
	}
	if str("message") != "" {
		return "", false
	}
	if conv := str("conversation"); conv != "" {
		return conv, true
	}
	if chat := str("chat"); chat != "" {
		return chat, true
	}
	return "", false
}

// wroteInto says whether ledger row r is a message written into chat feed.
func wroteInto(r db.SentAsUser, feed string) bool {
	got, ok := wroteFeed(r)
	return ok && got == feed
}

func (s *Service) anyChatBatchFresh(writers []chatWriter, thread string, ids []string, now time.Time) bool {
	b := &s.chatReplies
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pruneLocked(now)
	for _, w := range writers {
		told := b.told[chatReplyKey(w.session, thread)]
		for _, id := range ids {
			if told == nil {
				return true
			}
			if _, seen := told.ids[id]; !seen {
				return true
			}
		}
	}
	return false
}

func (s *Service) fetchChatMessagesAround(ctx context.Context, thread, messageID string) ([]settleMsg, bool) {
	if messageID == "" {
		return nil, false
	}
	cctx, cancel := context.WithTimeout(ctx, chatReplyCheckTimeout)
	defer cancel()
	args, _ := json.Marshal(map[string]any{"message": messageID, "n": chatReplyAround})
	var res struct {
		Result string `json:"result"`
	}
	err := s.appCall(cctx, AppInbox, inboxGroupTool, map[string]any{
		"source": migratedConnectorID,
		"call":   connector.GroupToolCall{Thread: thread, Name: connector.GroupMessagesAround, Args: args},
	}, &res)
	if err != nil {
		if !unsupported(err) {
			s.logf("chat replies: reading messages around %s: %v", thread, err)
		}
		return nil, false
	}
	return settleMessages(res.Result), true
}

// ownerSpokeAfterSend checks structured chat history around the latest batch message for any non-recalled message
// written personally by the owner after assistant send w (reconciled 1-to-1 against sent_as_user ledger rows so
// recorded sends and replies are excluded once without swallowing subsequent personal messages).
func ownerSpokeAfterSend(msgs []settleMsg, feed string, rows []db.SentAsUser, w chatWriter) (time.Time, bool) {
	msgAt := make([]time.Time, len(msgs))
	msgHasAt := make([]bool, len(msgs))
	msgNorm := make([]string, len(msgs))
	for i, m := range msgs {
		if t, err := time.Parse(time.RFC3339Nano, m.At); err == nil {
			msgAt[i], msgHasAt[i] = t, true
		}
		msgNorm[i] = normalizeSentText(m.Text)
	}

	claimedMsg := make([]bool, len(msgs))
	claimedRow := make([]bool, len(rows))
	var wPos string
	var wMsgAt time.Time
	isWriterRow := func(r db.SentAsUser) bool {
		if w.rowID != 0 {
			return r.ID == w.rowID
		}
		if w.ref != "" {
			return r.MessageRef == w.ref
		}
		return wroteInto(r, feed) && r.At.Equal(w.at)
	}
	noteAnchor := func(ri, mi int) {
		if !isWriterRow(rows[ri]) {
			return
		}
		if msgs[mi].Pos != "" {
			wPos = msgs[mi].Pos
		}
		if msgHasAt[mi] {
			wMsgAt = msgAt[mi]
		}
	}

	// Pass 1: exact MessageRef match on Lark rows.
	for ri, r := range rows {
		if r.Connector != connector.Lark || r.MessageRef == "" {
			continue
		}
		for mi, m := range msgs {
			if claimedMsg[mi] || !m.Mine || m.Text == recalledText || m.ID != r.MessageRef {
				continue
			}
			claimedRow[ri] = true
			claimedMsg[mi] = true
			noteAnchor(ri, mi)
			break
		}
	}
	// Populate the writer's own anchor even if matched above by an earlier duplicate ref or recalled.
	if w.ref != "" && wPos == "" && wMsgAt.IsZero() {
		for mi, m := range msgs {
			if m.ID == w.ref {
				if m.Pos != "" {
					wPos = m.Pos
				}
				if msgHasAt[mi] {
					wMsgAt = msgAt[mi]
				}
				break
			}
		}
	}

	// Pass 2a: unreferenced Lark reply rows (/messages/<id>/replies) matched 1-to-1 by ReplyTo (+ optional text).
	for ri, r := range rows {
		if claimedRow[ri] || r.Connector != connector.Lark || r.RecalledAt != nil || r.MessageRef != "" {
			continue
		}
		var t map[string]any
		if json.Unmarshal(r.Target, &t) != nil {
			continue
		}
		replyMsg, _ := t["message"].(string)
		if replyMsg == "" {
			continue
		}
		rNorm := normalizeSentText(r.Text)
		if mi, ok := matchUnreferencedRow(r, msgs, msgAt, msgHasAt, claimedMsg, false, func(i int, m settleMsg) bool {
			if m.ReplyTo != replyMsg {
				return false
			}
			return rNorm == "" || msgNorm[i] == "" || rNorm == msgNorm[i]
		}); ok {
			claimedRow[ri] = true
			claimedMsg[mi] = true
			noteAnchor(ri, mi)
		}
	}

	// Pass 2b: unreferenced Lark chat rows with non-empty text matched 1-to-1 by normalized text.
	for ri, r := range rows {
		if claimedRow[ri] || r.Connector != connector.Lark || r.RecalledAt != nil || r.MessageRef != "" || !wroteInto(r, feed) {
			continue
		}
		rNorm := normalizeSentText(r.Text)
		if rNorm == "" {
			continue
		}
		if mi, ok := matchUnreferencedRow(r, msgs, msgAt, msgHasAt, claimedMsg, false, func(i int, _ settleMsg) bool {
			return msgNorm[i] == rNorm
		}); ok {
			claimedRow[ri] = true
			claimedMsg[mi] = true
			noteAnchor(ri, mi)
		}
	}

	// Pass 2c: conservative 1-to-1 fallback for writer w's own unreferenced textless row (e.g. file/image upload).
	for ri, r := range rows {
		if claimedRow[ri] || r.Connector != connector.Lark || r.RecalledAt != nil || r.MessageRef != "" ||
			!wroteInto(r, feed) || normalizeSentText(r.Text) != "" || !isWriterRow(r) {
			continue
		}
		if mi, ok := matchUnreferencedRow(r, msgs, msgAt, msgHasAt, claimedMsg, true, func(_ int, m settleMsg) bool {
			return m.ReplyTo == ""
		}); ok {
			claimedRow[ri] = true
			claimedMsg[mi] = true
			noteAnchor(ri, mi)
		}
	}

	var latest time.Time
	for mi, m := range msgs {
		if claimedMsg[mi] || !m.Mine || m.Text == recalledText {
			continue
		}
		var cand time.Time
		switch {
		case wPos != "" && m.Pos != "":
			if !positionAfter(m.Pos, wPos) {
				continue
			}
			cand = w.at
			if msgHasAt[mi] && msgAt[mi].After(cand) {
				cand = msgAt[mi]
			}
		case !wMsgAt.IsZero() && msgHasAt[mi]:
			if !msgAt[mi].After(wMsgAt) {
				continue
			}
			cand = w.at
			if msgAt[mi].After(cand) {
				cand = msgAt[mi]
			}
		default:
			if !msgHasAt[mi] || !msgAt[mi].After(w.at.Add(chatReplyClockSkew)) {
				continue
			}
			cand = msgAt[mi]
		}
		if cand.After(latest) {
			latest = cand
		}
	}
	return latest, !latest.IsZero()
}

func matchUnreferencedRow(r db.SentAsUser, msgs []settleMsg, msgAt []time.Time, msgHasAt, claimedMsg []bool,
	requireTime bool, match func(mi int, m settleMsg) bool) (int, bool) {
	best, bestDelta := -1, time.Duration(0)
	timedCount, untimedCount, untimedIdx := 0, 0, -1
	for mi, m := range msgs {
		if claimedMsg[mi] || !m.Mine || m.Text == recalledText || !match(mi, m) {
			continue
		}
		if !msgHasAt[mi] {
			untimedCount++
			untimedIdx = mi
			continue
		}
		d := absDuration(msgAt[mi].Sub(r.At))
		if d > chatReplyClockSkew {
			continue
		}
		timedCount++
		if best < 0 || d < bestDelta {
			best, bestDelta = mi, d
		}
	}
	if best >= 0 {
		return best, true
	}
	if !requireTime && timedCount == 0 && untimedCount == 1 {
		return untimedIdx, true
	}
	return -1, false
}

func normalizeSentText(s string) string { return strings.Join(strings.Fields(s), "") }

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// chatWriterOpen: conversation id is one of the assistant's on machine (list), not archived, neither one the Inbox
// opened nor the background supervisor.
func (s *Service) chatWriterOpen(ctx context.Context, machine, id string, list []proto.Session) bool {
	i := slices.IndexFunc(list, func(c proto.Session) bool { return c.SessionID == id })
	if i < 0 {
		return false
	}
	c := &list[i]
	return c.Assistant && !c.Archived && !c.IsDeleted() && !s.isArchived(id) && !s.isInboxModelCall(ctx, machine, id, "") &&
		!s.isBackground(ctx, id)
}

// tellChatBatch tells conversation to, which last wrote into t's chat at sent, of t's messages it was not told yet:
// in the chat_reply about that chat still queued for it, else in a new one, which waits until chatReplyGap after the
// last one went in.
func (s *Service) tellChatBatch(ctx context.Context, to string, sent time.Time, t connector.InboxThing, now time.Time) {
	b := &s.chatReplies
	b.mu.Lock()
	b.pruneLocked(now)
	told := b.toldLocked(to, t.Thread)
	var fresh []string
	for _, id := range t.Group.New {
		if _, seen := told.ids[id]; !seen {
			fresh = append(fresh, id)
		}
		told.ids[id] = now // handed again: kept while the batch keeps coming
	}
	went := told.went
	b.mu.Unlock()
	if len(fresh) == 0 {
		return // told already, or waiting to be
	}
	c := &chatReply{Thread: t.Thread, Title: clip(oneLine(t.Title), 60), Group: t.Kind != KindDM, Sender: clip(oneLine(t.Sender), 40),
		Sent: sent.UTC(), IDs: fresh, Earlier: len(fresh) < len(t.Group.New), Context: lastRunes(strings.TrimSpace(t.Context), chatReplyContextMax)}
	loc, lang := s.location(ctx), answerLanguage(s.hello(ctx).Locale)
	if s.mergeChatReply(to, c, t.Group.New, loc, lang, now) {
		s.logf("chat replies: %d new message(s) merged into the one waiting for %s", len(fresh), short(to))
		s.publish(ctx)
		return
	}
	msg := chatReplyMsg(c, loc, lang, now)
	if due := went.Add(chatReplyGap); !went.IsZero() && due.After(now) {
		msg.Due = due.UTC()
	} else if s.chatReplyGoing(to, t.Thread) {
		msg.Due = now.Add(chatReplyGap).UTC()
	}
	s.logf("chat replies: %d new message(s) for %s", len(fresh), short(to))
	s.relayOrQueue(ctx, to, msg)
}

// mergeChatReply puts c into the chat_reply about the same chat still queued for conversation to (not one going in
// right now) and says whether there was one. batch is every message of c's batch: when it has all of the queued
// one's, its text has theirs too and takes its place; otherwise the texts are put together, the newest kept.
func (s *Service) mergeChatReply(to string, c *chatReply, batch []string, loc *time.Location, lang string, now time.Time) bool {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.relayLocked(context.Background()) {
		return false
	}
	q := b.st.Queue[to]
	for i := range q {
		old := q[i].Chat
		if q[i].Tool != chatReplyTool || old == nil || old.Thread != c.Thread || i == 0 && b.draining[to] {
			continue
		}
		m := *c
		m.IDs = slices.Clone(old.IDs)
		for _, id := range c.IDs {
			if !slices.Contains(m.IDs, id) {
				m.IDs = append(m.IDs, id)
			}
		}
		if len(m.IDs) == len(old.IDs) {
			return true // all of them wait in it already (queued before a restart)
		}
		if old.Sent.After(m.Sent) {
			m.Sent = old.Sent
		}
		earlier := slices.ContainsFunc(batch, func(id string) bool { return !slices.Contains(m.IDs, id) })
		if slices.ContainsFunc(old.IDs, func(id string) bool { return !slices.Contains(batch, id) }) {
			m.Context = lastRunes(old.Context+"\n"+c.Context, chatReplyContextMax)
			earlier = earlier || old.Earlier
		}
		m.Earlier = earlier
		msg := chatReplyMsg(&m, loc, lang, now)
		msg.Due = q[i].Due // the gap it waits for stays
		q[i] = msg.replacing(q[i], now)
		s.saveRelayLocked()
		return true
	}
	return false
}

// chatReplyGoing: the chat_reply about thread is going into conversation to right now (the head of its draining
// queue); one that comes meanwhile waits its gap from now.
func (s *Service) chatReplyGoing(to, thread string) bool {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	q := b.st.Queue[to]
	return len(q) > 0 && b.draining[to] && q[0].Tool == chatReplyTool && q[0].Chat != nil && q[0].Chat.Thread == thread
}

// chatReplyDelivered: a chat_reply went into conversation to; the next one about that chat waits chatReplyGap. Its
// messages count as told, also when it was queued before a restart. A refusal of to's is over: the next one is pushed.
func (s *Service) chatReplyDelivered(to string, msg relayMsg) {
	b := &s.chatReplies
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.refused, to)
	b.wentLocked(to, msg, time.Now())
}

// chatReplyRefused: conversation to refused chat_reply msg for good (relayRefused). It counts as gone for the gap, so
// the chat's next batch waits chatReplyGap and what comes meanwhile merges into that one, not a try and a refusal per
// batch; its messages count as told. True when the owner is to be pushed: the first such refusal by to since a
// chat_reply last went into it, within chatReplyWindow.
func (s *Service) chatReplyRefused(to string, msg relayMsg) bool {
	b := &s.chatReplies
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.wentLocked(to, msg, now)
	if at, pushed := b.refused[to]; pushed && now.Sub(at) <= chatReplyWindow {
		return false
	}
	if b.refused == nil {
		b.refused = map[string]time.Time{}
	}
	b.refused[to] = now
	return true
}

// wentLocked notes chat_reply msg as gone into conversation to at now: the gap starts, its messages are told. b.mu is
// held.
func (b *chatReplyBook) wentLocked(to string, msg relayMsg, now time.Time) {
	if msg.Chat == nil {
		return
	}
	told := b.toldLocked(to, msg.Chat.Thread)
	told.went = now
	for _, id := range msg.Chat.IDs {
		if _, ok := told.ids[id]; !ok {
			told.ids[id] = now
		}
	}
}

// chatReplyMsg is the relay message that tells c, in the owner's language (lang, answerLanguage) with the time of the
// send in their zone.
func chatReplyMsg(c *chatReply, loc *time.Location, lang string, now time.Time) relayMsg {
	// i18n-ignore: the queue's label, as the task notices'
	label := "群「" + c.Title + "」"
	if !c.Group {
		label = "私聊（" + c.Title + "）" // i18n-ignore: the queue's label, as the task notices'
	}
	text := prompts.RenderChatReply(prompts.ChatReply{Language: lang, Title: c.Title, Group: c.Group, Sender: c.Sender,
		Sent: c.Sent.In(loc).Format("01-02 15:04"), Count: len(c.IDs), Earlier: c.Earlier, Context: c.Context})
	return relayMsg{From: c.Thread, FromTitle: label, Tool: chatReplyTool, Text: text, At: now.UTC(), Chat: c}
}

// lastRunes is the end of s, at most n characters, starting with … where it was cut.
func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n+1:])
}
