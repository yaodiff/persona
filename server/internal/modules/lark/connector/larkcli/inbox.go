package larkcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"

	"github.com/nwverse/persona/server/internal/cliwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	adapter "github.com/nwverse/persona/server/internal/modules/lark/larkcli"
	"github.com/nwverse/persona/server/internal/prompts"
)

const (
	inboxPollDefault     = 2 * time.Minute
	inboxPollMinimum     = time.Minute
	inboxPageLimit       = 5
	inboxPageLimitMax    = 40
	inboxSeenLimit       = 4096
	inboxTopicLimit      = 2048
	inboxSeedPages       = 2
	inboxInitialLookback = 2 * time.Minute
	// Message search indexes most messages at once but some only minutes later (a direct message was seen
	// arriving in search about ten minutes after it was sent). The cursor keeps this much behind the newest
	// message it saw so a late-indexed one is still inside the next rounds' window; the seen ledger drops
	// what was already collected.
	inboxCursorOverlap = 10 * time.Minute
	// Direct chats are also read from their own history (sweepDirectChats), which has no indexing delay:
	// up to inboxDirectChats per round, rotating through the most recent candidates.
	inboxDirectChats      = 10
	inboxDirectCandidates = 50
	inboxDirectLookback   = 2 * time.Hour
	// The context the models read (contextFor): the current batch and recent background. Keep the background from
	// contextHistory before the first new message, at most contextEarlier messages, but always the last
	// contextEarlierKeep however old (a short answer keeps its question). The chat's page of 40 brought weeks-old
	// messages and cards of thousands of characters into every triage of a quiet chat.
	contextHistory      = 24 * time.Hour
	contextEarlier      = 10
	contextEarlierKeep  = 3
	contextEarlierRunes = 300
	contextNewRunes     = 2000
)

// StateStore is the settings store the Inbox state is kept in (cliwire.StateStore).
type StateStore = cliwire.StateStore

const inboxStateKey = "connector.larkcli.inbox"

type persistedInboxState struct {
	Cursor       string                              `json:"cursor,omitempty"`
	PageToken    string                              `json:"page_token,omitempty"`
	Topics       []string                            `json:"topics,omitempty"`
	Seen         []string                            `json:"seen,omitempty"`
	TopicsSeeded bool                                `json:"topics_seeded,omitempty"`
	Things       map[string]connectorwire.InboxThing `json:"things,omitempty"`
}

type inboxGroup struct {
	key       string
	chatID    string
	threadID  string
	chatName  string
	mentioned bool
	messages  []adapter.Message
	latest    adapter.Message
	replied   bool
}

// SetStateStore attaches the manager's Postgres-backed settings store. Tests
// may omit it; the bounded state then remains in memory for that connector
// lifetime.
func (c *Connector) SetStateStore(store StateStore) {
	c.inboxMu.Lock()
	c.inboxStore = store
	c.inboxStateLoaded = false
	c.inboxMu.Unlock()
}

// SetInboxSource configures coexistence with the Lark gateway. The callback is
// evaluated at collection time because an extension can appear, disappear, or
// lose its login after the connector is constructed.
func (c *Connector) SetInboxSource(prefer string, gatewayAvailable func(context.Context) bool) {
	c.inboxMu.Lock()
	c.inboxPrefer = prefer
	c.gatewayAvailable = gatewayAvailable
	c.inboxMu.Unlock()
}

// SetPollInterval changes the Inbox poll period. Values below one minute are
// clamped to the protocol's minimum; zero restores the two-minute default.
func (c *Connector) SetPollInterval(interval time.Duration) {
	c.inboxMu.Lock()
	defer c.inboxMu.Unlock()
	if interval <= 0 {
		interval = inboxPollDefault
	}
	if interval < inboxPollMinimum {
		interval = inboxPollMinimum
	}
	c.inboxPoll = interval
}

// Poll performs one bounded Inbox round for both chat messages and watched
// document comments. It is exported for focused connector tests and for
// embedders that already own a scheduler.
func (c *Connector) Poll(ctx context.Context) (bool, error) {
	if c == nil || c.runner == nil {
		return false, &adapter.Error{Kind: adapter.KindNotInstalled}
	}
	if !c.inboxShouldCollect(ctx) {
		c.inboxMu.Lock()
		c.inboxThings = map[string]connectorwire.InboxThing{}
		c.inboxCollecting = false
		c.inboxMu.Unlock()
		c.clearDocumentThings()
		return false, nil
	}
	chatMoved, chatErr := c.pollChat(ctx)
	docMoved := c.pollDocumentsRound(ctx)
	if chatErr != nil {
		return chatMoved || docMoved, chatErr
	}
	return chatMoved || docMoved, nil
}

// pollChat performs the bounded chat Inbox round. Source selection is handled
// by Poll so the document poller and chat poller share one primary-source rule.
func (c *Connector) pollChat(ctx context.Context) (bool, error) {
	auth, err := c.runner.AuthStatus(ctx)
	if err != nil {
		return false, err
	}
	if !auth.LoggedIn || auth.OpenID == "" {
		// A signed-out CLI is represented by connector status; the background
		// watcher waits and lets the user sign in instead of restarting noisily.
		c.inboxMu.Lock()
		c.inboxCollecting = false
		c.inboxMu.Unlock()
		return false, nil
	}

	c.inboxMu.Lock()
	if err := c.loadInboxStateLocked(ctx); err != nil {
		c.inboxMu.Unlock()
		return false, err
	}
	state := c.inboxState
	pageLimit := c.inboxPageLimit
	if pageLimit <= 0 {
		pageLimit = inboxPageLimit
	}
	if pageLimit > inboxPageLimitMax {
		pageLimit = inboxPageLimitMax
	}
	start := state.Cursor
	if start == "" {
		start = c.currentTime().Add(-inboxInitialLookback).UTC().Format(time.RFC3339)
	}
	seeded := state.TopicsSeeded
	topicsBefore := make(map[string]struct{}, len(c.inboxTopics))
	for topic := range c.inboxTopics {
		topicsBefore[topic] = struct{}{}
	}
	c.inboxMu.Unlock()

	visibilityEnabled := c.replyVisibilityEnabled()
	if !seeded && !visibilityEnabled {
		// Seed the topic set from the user's own history. Search is bounded just
		// like the live round; later rounds extend it whenever a user message or
		// mention is observed.
		if err := c.seedTopics(ctx, auth.OpenID, start, pageLimit); err != nil {
			return false, err
		}
		c.inboxMu.Lock()
		c.inboxState.TopicsSeeded = true
		state = c.inboxState
		topicsBefore = make(map[string]struct{}, len(c.inboxTopics))
		for topic := range c.inboxTopics {
			topicsBefore[topic] = struct{}{}
		}
		c.inboxMu.Unlock()
	}

	var found []adapter.Message
	pageToken := state.PageToken
	nextPageToken := ""
	var newest time.Time
	for pageNo := 0; !visibilityEnabled && pageNo < pageLimit; pageNo++ {
		// A global message search returns bodies before metadata filtering could run. With visibility controls enabled,
		// collection uses only named-chat history reads whose target was vetted before the body request.
		page, err := c.runner.SearchMessages(ctx, "", adapter.SearchOptions{
			Start: start, PageToken: pageToken, Limit: 50,
		})
		if err != nil {
			return false, err
		}
		for _, message := range page.Messages {
			if t := messageTime(message); !t.IsZero() && t.After(newest) {
				newest = t
			}
			if message.MessageID != "" {
				if c.replyMessageExcluded(message) {
					c.markSeen(message.MessageID)
					continue
				}
				if !conversationMessage(message) || isBoundAppMessage(message, auth.AppID) {
					// The app bound to lark-cli is Persona's own bot in the
					// recommended setup. Consume its messages and non-conversation
					// events without waking Inbox, but keep cursor/seen progress.
					c.markSeen(message.MessageID)
					continue
				}
				found = append(found, message)
			}
			c.noteTopic(message, auth.OpenID)
		}
		if !page.HasMore || page.PageToken == "" {
			nextPageToken = ""
			break
		}
		nextPageToken = page.PageToken
		pageToken = page.PageToken
	}

	// Search results are normally newest first, but ordering is not part of
	// the shortcut contract. Sort before grouping each conversation's incoming batch.
	// Direct chats read from their own history: what search has not indexed yet still comes in. A failed sweep
	// doesn't lose what search found; the round keeps it and reports the error at the end.
	direct, directErr := c.sweepDirectChats(ctx)
	for _, message := range direct {
		if c.replyMessageExcluded(message) {
			c.markSeen(message.MessageID)
			continue
		}
		if !conversationMessage(message) || isBoundAppMessage(message, auth.AppID) {
			c.markSeen(message.MessageID)
			continue
		}
		found = append(found, message)
	}

	sort.SliceStable(found, func(i, j int) bool {
		return messageLess(found[i], found[j])
	})

	newMessages := c.dedupMessages(found)
	groups := c.groupMessages(newMessages, auth.OpenID, topicsBefore)
	changed := false
	for _, group := range groups {
		thing := c.thingForGroup(ctx, group, auth.OpenID)
		if thing.Thread == "" {
			continue
		}
		c.inboxMu.Lock()
		previous, existed := c.inboxThings[thing.Thread]
		if !existed || previous.Last != thing.Last || previous.Done != thing.Done || previous.Context != thing.Context {
			changed = true
		}
		c.inboxThings[thing.Thread] = thing
		c.inboxMu.Unlock()
	}

	c.inboxMu.Lock()
	if !newest.IsZero() {
		candidate := newest.Add(-inboxCursorOverlap).UTC()
		if previous, err := time.Parse(time.RFC3339Nano, state.Cursor); err == nil && previous.After(candidate) {
			candidate = previous
		}
		state.Cursor = candidate.Format(time.RFC3339)
	}
	if !visibilityEnabled {
		// A visibility round performs no global search: leave its page and historical-seed progress untouched so
		// restoring the legacy configuration resumes the same search instead of claiming a seed that never ran.
		state.PageToken = nextPageToken
		state.TopicsSeeded = true
	}
	c.inboxState.Cursor = state.Cursor
	c.inboxState.PageToken = state.PageToken
	c.inboxState.TopicsSeeded = state.TopicsSeeded
	c.inboxCollecting = true
	if err := c.saveInboxStateLocked(ctx); err != nil {
		c.inboxMu.Unlock()
		return changed, err
	}
	c.inboxMu.Unlock()
	return changed, directErr
}

// sweepDirectChats rotates through recent direct chats' history, back to inboxDirectLookback.
// Message search is the round's main source, but it indexes some messages minutes late: one indexed after the cursor
// had passed its time was never collected. Every direct message belongs in the Inbox, and chat history has no
// indexing delay, so these chats are read here as well; the seen ledger drops what search already delivered. Search
// results carry the chat's type and partner; history does not, so they are filled in from the chat list.
func (c *Connector) sweepDirectChats(ctx context.Context) ([]adapter.Message, error) {
	c.inboxMu.Lock()
	allDirectExcluded := c.inboxExcludeDirectChats || c.managedInboxExcludeDirectChats || c.managedInboxVisibilityInvalid
	c.inboxMu.Unlock()
	if allDirectExcluded {
		return nil, nil
	}
	list, err := c.runner.ListDirectChats(ctx, inboxDirectCandidates)
	if err != nil {
		return nil, err
	}
	floor := c.currentTime().Add(-inboxDirectLookback).UTC()
	start := floor.Format(time.RFC3339)
	var out []adapter.Message
	var chats []adapter.Chat
	for _, chat := range list.Chats {
		if chat.ChatID == "" || (chat.ChatMode != "p2p" && chat.ChatType != "p2p") ||
			c.replyChatExcluded(chat.ChatID, chat.ChatType, chat.ChatMode) {
			continue
		}
		chats = append(chats, chat)
	}
	if len(chats) == 0 {
		return nil, nil
	}
	// Keep ten reads per round without permanently skipping the eleventh active chat.
	c.inboxMu.Lock()
	offset := c.inboxDirectOffset % len(chats)
	read := minInt(inboxDirectChats, len(chats))
	c.inboxDirectOffset = (offset + read) % len(chats)
	c.inboxMu.Unlock()
	for i := 0; i < read; i++ {
		chat := chats[(offset+i)%len(chats)]
		page, err := c.runner.ListMessagesSince(ctx, chat.ChatID, start, 50)
		if err != nil {
			return out, err
		}
		partner := directPartner(chat)
		for _, message := range page.Messages {
			if message.MessageID == "" || message.Deleted || (message.ChatID != "" && message.ChatID != chat.ChatID) {
				continue
			}
			// The CLI prints minutes; a message in floor's own minute reads as just before it.
			if t := messageTime(message); !t.IsZero() && t.Before(floor.Add(-time.Minute)) {
				continue
			}
			message.ChatID = firstNonEmpty(message.ChatID, chat.ChatID)
			message.ChatType = "p2p"
			message.ChatName = firstNonEmpty(message.ChatName, chat.Name)
			if len(message.ChatPartner) == 0 {
				message.ChatPartner = partner
			}
			out = append(out, message)
		}
	}
	return out, nil
}

// directPartner is the chat partner a search result would carry, from a direct chat's list entry.
func directPartner(chat adapter.Chat) json.RawMessage {
	if chat.P2PTargetID == "" && chat.Name == "" {
		return nil
	}
	senderType := "user"
	if strings.EqualFold(chat.P2PTargetType, "bot") {
		senderType = "app"
	}
	b, err := json.Marshal(map[string]string{"id": chat.P2PTargetID, "sender_type": senderType, "name": chat.Name})
	if err != nil {
		return nil
	}
	return b
}

// Watch owns the background poll and emits the protocol's inbox change event
// only when either poller produced a new or updated item.
func (c *Connector) Watch(ctx context.Context, changed func(connectorwire.Scope)) error {
	for {
		moved, err := c.Poll(ctx)
		if moved && changed != nil {
			changed(connectorwire.ScopeInbox)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		c.inboxMu.Lock()
		wait := c.inboxPoll
		if wait <= 0 {
			wait = inboxPollDefault
		}
		c.inboxMu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

// Collect is memory-only. Polling and persistence happen in Watch/Poll; this
// method never invokes lark-cli, matching inbox.collect's protocol semantics.
func (c *Connector) Collect(context.Context) ([]connectorwire.InboxThing, error) {
	c.inboxMu.Lock()
	out := make([]connectorwire.InboxThing, 0, len(c.inboxThings))
	if c.inboxCollecting {
		for _, thing := range c.inboxThings {
			if !c.replyThingExcludedLocked(thing) {
				out = append(out, thing)
			}
		}
	}
	c.inboxMu.Unlock()

	c.ensureDocuments()
	c.docs.mu.Lock()
	for _, thing := range c.docs.things {
		out = append(out, thing)
	}
	c.docs.mu.Unlock()

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].At.Equal(out[j].At) {
			return out[i].Thread < out[j].Thread
		}
		return out[i].At.Before(out[j].At)
	})
	return out, nil
}

// Reply is the existing Inbox Send path: the user's tap is the confirmation,
// and the CLI write itself is explicitly made as the bot identity decided in
// the L4 plan. A message id lets us preserve a topic reply; a chat-only thing
// falls back to messages-send.
func (c *Connector) Reply(ctx context.Context, r connectorwire.InboxReply) error {
	if strings.HasPrefix(r.Thread, "doc:") {
		return fmt.Errorf("%w: lark-cli document comments are read-only", connectorwire.ErrCannotReply)
	}
	if c == nil || c.runner == nil {
		return &adapter.Error{Kind: adapter.KindNotInstalled}
	}
	chatID, ok := chatIDFromThread(r.Thread)
	if !ok {
		return fmt.Errorf("%w: the thread names no chat", connectorwire.ErrCannotReply) // never the thread: it is a private id
	}
	var err error
	identity := connectorwire.SendIdentity(ctx)
	if identity == "" {
		identity = "bot"
	}
	if identity != "bot" && identity != "user" {
		return errors.New("unsupported message sender identity")
	}
	if strings.TrimSpace(r.Last) != "" && strings.Contains(r.Thread, ":thread:") {
		_, err = c.runner.ReplyTextAs(ctx, identity, r.Last, r.Text, r.ClientID, strings.Contains(r.Thread, ":thread:"))
	} else {
		_, err = c.runner.SendTextAs(ctx, identity, chatID, r.Text, r.ClientID)
	}
	if err != nil {
		return err
	}
	c.inboxMu.Lock()
	if c.inboxThings != nil {
		if thing, exists := c.inboxThings[r.Thread]; exists {
			thing.Done = connectorwire.DoneReplied
			c.inboxThings[r.Thread] = thing
		}
	}
	c.inboxMu.Unlock()
	return nil
}

func (c *Connector) inboxShouldCollect(ctx context.Context) bool {
	if c == nil || c.runner == nil {
		return false
	}
	c.inboxMu.Lock()
	prefer, available := c.inboxPrefer, c.gatewayAvailable
	c.inboxMu.Unlock()
	if prefer == "cli" {
		// An explicitly preferred CLI still gives the gateway a turn when its
		// login has disappeared. This check is intentionally read-only; the
		// actual poll reads the same status again before searching.
		auth, err := c.runner.AuthStatus(ctx)
		if err == nil && auth.LoggedIn {
			return true
		}
		return available == nil || !available(ctx)
	}
	if available != nil && available(ctx) {
		return false
	}
	return true
}

func (c *Connector) loadInboxStateLocked(ctx context.Context) error {
	if c.inboxStateLoaded {
		return nil
	}
	c.inboxStateLoaded = true
	if c.inboxThings == nil {
		c.inboxThings = map[string]connectorwire.InboxThing{}
	}
	if c.inboxSeen == nil {
		c.inboxSeen = map[string]struct{}{}
	}
	if c.inboxTopics == nil {
		c.inboxTopics = map[string]struct{}{}
	}
	if c.inboxStore == nil {
		return nil
	}
	var saved persistedInboxState
	ok, err := c.inboxStore.Setting(ctx, inboxStateKey, &saved)
	if err != nil {
		c.inboxStateLoaded = false
		return fmt.Errorf("lark-cli Inbox state: %w", err)
	}
	if !ok {
		return nil
	}
	c.inboxState = saved
	for thread, thing := range saved.Things {
		if thread != "" {
			c.inboxThings[thread] = thing
		}
	}
	for _, id := range saved.Seen {
		if id != "" {
			c.inboxSeen[id] = struct{}{}
		}
	}
	for _, topic := range saved.Topics {
		if topic != "" {
			c.inboxTopics[topic] = struct{}{}
		}
	}
	return nil
}

func (c *Connector) saveInboxStateLocked(ctx context.Context) error {
	if c.inboxStore == nil {
		return nil
	}
	state := persistedInboxState{Cursor: c.inboxState.Cursor, PageToken: c.inboxState.PageToken, TopicsSeeded: c.inboxState.TopicsSeeded,
		Things: make(map[string]connectorwire.InboxThing, len(c.inboxThings))}
	for thread, thing := range c.inboxThings {
		state.Things[thread] = thing
	}
	for topic := range c.inboxTopics {
		state.Topics = append(state.Topics, topic)
	}
	sort.Strings(state.Topics)
	if len(state.Topics) > inboxTopicLimit {
		for _, topic := range state.Topics[:len(state.Topics)-inboxTopicLimit] {
			delete(c.inboxTopics, topic)
		}
		state.Topics = state.Topics[len(state.Topics)-inboxTopicLimit:]
	}
	for id := range c.inboxSeen {
		state.Seen = append(state.Seen, id)
	}
	sort.Strings(state.Seen)
	if len(state.Seen) > inboxSeenLimit {
		state.Seen = state.Seen[len(state.Seen)-inboxSeenLimit:]
	}
	c.inboxState = state
	if err := c.inboxStore.PutSetting(ctx, inboxStateKey, state); err != nil {
		return fmt.Errorf("lark-cli Inbox state: %w", err)
	}
	return nil
}

func (c *Connector) seedTopics(ctx context.Context, me, start string, pageLimit int) error {
	token := ""
	for pageNo := 0; pageNo < minInt(pageLimit, inboxSeedPages); pageNo++ {
		page, err := c.runner.SearchMessages(ctx, "", adapter.SearchOptions{Sender: me, Start: start, PageToken: token, Limit: 50})
		if err != nil {
			return err
		}
		for _, message := range page.Messages {
			c.noteTopic(message, me)
		}
		if !page.HasMore || page.PageToken == "" {
			break
		}
		token = page.PageToken
	}
	return nil
}

func (c *Connector) noteTopic(message adapter.Message, me string) {
	topic := topicID(message)
	if topic == "" {
		return
	}
	if message.Sender.ID == me || messageMentions(message, me) {
		c.inboxMu.Lock()
		if c.inboxTopics == nil {
			c.inboxTopics = map[string]struct{}{}
		}
		c.inboxTopics[topic] = struct{}{}
		c.inboxMu.Unlock()
	}
}

func (c *Connector) dedupMessages(messages []adapter.Message) []adapter.Message {
	c.inboxMu.Lock()
	defer c.inboxMu.Unlock()
	if c.inboxSeen == nil {
		c.inboxSeen = map[string]struct{}{}
	}
	out := make([]adapter.Message, 0, len(messages))
	for _, message := range messages {
		if message.MessageID == "" {
			continue
		}
		if _, ok := c.inboxSeen[message.MessageID]; ok {
			continue
		}
		c.inboxSeen[message.MessageID] = struct{}{}
		out = append(out, message)
	}
	// The persisted state is rebuilt from this bounded map on save. Remove
	// arbitrary oldest-by-id entries only when necessary; IDs are opaque and
	// boundedness matters more than their lexical order.
	for len(c.inboxSeen) > inboxSeenLimit {
		for id := range c.inboxSeen {
			delete(c.inboxSeen, id)
			break
		}
	}
	return out
}

func (c *Connector) markSeen(messageID string) {
	if messageID == "" {
		return
	}
	c.inboxMu.Lock()
	defer c.inboxMu.Unlock()
	if c.inboxSeen == nil {
		c.inboxSeen = map[string]struct{}{}
	}
	c.inboxSeen[messageID] = struct{}{}
	for len(c.inboxSeen) > inboxSeenLimit {
		for id := range c.inboxSeen {
			delete(c.inboxSeen, id)
			break
		}
	}
}

func (c *Connector) groupMessages(messages []adapter.Message, me string, knownTopics map[string]struct{}) []*inboxGroup {
	if len(messages) == 0 {
		return nil
	}
	topics := make(map[string]struct{}, len(knownTopics))
	for topic := range knownTopics {
		topics[topic] = struct{}{}
	}
	c.inboxMu.Lock()
	allChats := make(map[string]struct{}, len(c.inboxAllChats))
	for chat := range c.inboxAllChats {
		allChats[chat] = struct{}{}
	}
	c.inboxMu.Unlock()
	var out []*inboxGroup
	byKey := map[string]*inboxGroup{}
	for _, message := range messages {
		key := messageKey(message)
		if key == "" {
			continue
		}
		if message.Sender.ID == me {
			if prior := byKey[key]; prior != nil {
				prior.replied = after(message, prior.latest)
			}
			if topic := topicID(message); topic != "" {
				topics[topic] = struct{}{}
			}
			continue
		}
		keep := false
		mentioned := messageMentions(message, me)
		if mentioned {
			if topic := topicID(message); topic != "" {
				topics[topic] = struct{}{}
			}
		}
		_, topicKnown := topics[topicID(message)]
		_, allChat := allChats[message.ChatID]
		switch {
		case allChat:
			keep = true
		case mentioned:
			keep = true
		case message.ChatType == "p2p":
			keep = true
		case topicKnown:
			keep = true
		}
		if !keep {
			continue
		}
		current := byKey[key]
		if current == nil {
			current = &inboxGroup{key: key, chatID: message.ChatID, threadID: topicID(message), chatName: message.ChatName}
			out = append(out, current)
			byKey[key] = current
		}
		current.messages = append(current.messages, message)
		current.latest = message
		current.replied = false
		current.mentioned = current.mentioned || mentioned
	}
	return out
}

func (c *Connector) thingForGroup(ctx context.Context, group *inboxGroup, me string) connectorwire.InboxThing {
	if group == nil || len(group.messages) == 0 || c.replyChatExcluded(group.chatID, group.latest.ChatType, group.latest.ChatMode) {
		return connectorwire.InboxThing{}
	}
	latest := group.latest
	contextMessages := append([]adapter.Message(nil), group.messages...)
	if page, err := c.runner.ListMessages(ctx, group.chatID, group.threadID, "", 40); err == nil && len(page.Messages) > 0 {
		contextMessages = mergeMessages(page.Messages, group.messages)
	}
	visible := contextMessages[:0]
	for _, message := range contextMessages {
		if conversationMessage(message) && messageKey(message) == group.key {
			visible = append(visible, message)
		}
	}
	contextMessages = visible
	currentMessages := append([]adapter.Message(nil), group.messages...)
	currentIDs := make(map[string]int, len(currentMessages))
	for i, message := range currentMessages {
		currentIDs[message.MessageID] = i
	}
	// History can already contain a follow-up which search has not indexed. Only
	// unseen eligible activity in this chat/topic can supersede the search hit.
	c.inboxMu.Lock()
	_, allChat := c.inboxAllChats[group.chatID]
	c.inboxMu.Unlock()
	for _, message := range contextMessages {
		if _, incoming := currentIDs[message.MessageID]; incoming {
			continue // History metadata cannot change an incoming message's identity or freshness.
		}
		if message.Deleted || messageKey(message) != group.key || message.Sender.ID == me || !after(message, latest) {
			continue
		}
		if latest.ChatType != "p2p" && group.threadID == "" && !allChat && !messageMentions(message, me) {
			continue
		}
		c.inboxMu.Lock()
		_, seen := c.inboxSeen[message.MessageID]
		c.inboxMu.Unlock()
		if seen {
			continue
		}
		message.ChatType = firstNonEmpty(message.ChatType, latest.ChatType)
		if len(message.ChatPartner) == 0 {
			message.ChatPartner = latest.ChatPartner
		}
		latest = message
		currentIDs[message.MessageID] = len(currentMessages)
		currentMessages = append(currentMessages, message)
		group.replied = false
		group.mentioned = group.mentioned || messageMentions(message, me)
	}
	for _, message := range contextMessages {
		if !message.Deleted && messageKey(message) == group.key && message.Sender.ID == me && after(message, latest) {
			group.replied = true
		}
	}
	thread := "larkcli:chat:" + group.chatID
	if group.threadID != "" {
		thread += ":thread:" + group.threadID
	}
	kind := "group"
	lang := i18n.From(ctx).Or(i18n.Default)
	label := i18n.T(lang, "hub-connectors.larkcli_message")
	// No tier: the Inbox's triage model decides Needs you / FYI / Collapsed, with the user's triage rules and policies
	// (docs/assistant/inbox.md "AI triage", "Policies"). A fixed tier here would skip triage and put every bot's
	// direct message in Needs you.
	switch {
	case latest.ChatType == "p2p" && p2pIsBot(group, contextMessages, me):
		kind = "bot"
		label = i18n.T(lang, "hub-connectors.larkcli_direct_message")
	case latest.ChatType == "p2p":
		kind = "dm"
		label = i18n.T(lang, "hub-connectors.larkcli_direct_message")
	case group.mentioned:
		kind = "mention"
		label = i18n.T(lang, "hub-connectors.larkcli_mention")
	}
	title := firstNonEmpty(group.chatName, group.chatID)
	name := group.chatName // the chat's name, never an opaque id: what ReplyTo may show
	if latest.ChatType == "p2p" {
		// Direct chats have no stable display name in some search results. The
		// other sender is the conversation's name; use any sender name only as
		// a fallback, and keep the opaque chat id as the final fallback.
		title = p2pTitle(group, contextMessages, me)
		name = p2pName(group, contextMessages, me)
	}
	tags := []connectorwire.Tag{{Key: connectorwire.TagChat, Value: latest.ChatID, Name: title}}
	if senderTag, ok := latestSenderTag(contextMessages, me); ok {
		tags = append(tags, senderTag)
	}
	var history []adapter.Message
	for _, message := range contextMessages {
		i, current := currentIDs[message.MessageID]
		if !current {
			history = append(history, message)
		} else if currentMessages[i].MessagePosition == "" {
			// Search may omit positions. Use the chat's position for display order
			// without replacing the incoming message or its reply metadata.
			currentMessages[i].MessagePosition = message.MessagePosition
		}
	}
	sort.SliceStable(currentMessages, func(i, j int) bool {
		return messageLess(currentMessages[i], currentMessages[j])
	})
	thing := connectorwire.InboxThing{
		Thread:   thread,
		Kind:     kind,
		Label:    label,
		Title:    title,
		Sender:   latest.Sender.Name,
		Preview:  firstLine(inboxMessageText(latest, lang)),
		Context:  contextFor(currentMessages, history, me),
		At:       messageTime(latest),
		Last:     latest.MessageID,
		ReplyTo:  replyTo(lang, name, group.threadID != ""),
		CanReply: latest.MessageID != "",
		Rules:    c.rules(),
		Tags:     tags,
	}
	// Keep the CLI's link opaque: it includes the message position and, for a
	// topic, the source thread. Do not guess a link from a sender or a thread id.
	for _, message := range contextMessages {
		if message.MessageID != latest.MessageID || message.ChatID != group.chatID || group.threadID != "" && topicID(message) != group.threadID {
			continue
		}
		link, err := url.Parse(message.MessageAppLink)
		if err == nil && link.Scheme == "https" && link.User == nil &&
			(link.Host == "applink.feishu.cn" || link.Host == "applink.larksuite.com") &&
			isSourceAppLinkPath(link.Path) {
			thing.OpenURL = message.MessageAppLink
		}
		break
	}
	if thing.At.IsZero() {
		thing.At = time.Now().UTC()
	}
	if group.replied {
		thing.Done = connectorwire.DoneReplied
	}
	return thing
}

// replyTo says where a reply to the item goes and who sees it (connectorwire.InboxThing.ReplyTo): Reply sends as the
// app's bot, not as the user (Runner.SendText / ReplyText, docs/plans/plan-lark-cli.md), to the chat or into the topic
// the message is in, and the sentence says so (#12399). name is the chat's name; without one (only an opaque id is
// known), the sentence names no chat.
func replyTo(lang i18n.Lang, name string, topic bool) string {
	switch {
	case topic && name != "":
		return i18n.T(lang, "hub-connectors.larkcli_reply_to_topic", i18n.Args{"chat": name})
	case topic:
		return i18n.T(lang, "hub-connectors.larkcli_reply_to_topic_unnamed")
	case name != "":
		return i18n.T(lang, "hub-connectors.larkcli_reply_to", i18n.Args{"chat": name})
	}
	return i18n.T(lang, "hub-connectors.larkcli_reply_to_unnamed")
}

// p2pName is a direct chat's name: the other sender, else the chat partner, else the chat's own name; "" when only
// opaque ids are known.
func p2pName(group *inboxGroup, contextMessages []adapter.Message, me string) string {
	if group == nil {
		return ""
	}
	messages := append(append([]adapter.Message(nil), group.messages...), contextMessages...)
	for _, message := range messages {
		if message.Sender.ID == me {
			continue
		}
		if name := strings.TrimSpace(message.Sender.Name); name != "" {
			return name
		}
	}
	for i := len(messages) - 1; i >= 0; i-- {
		partner := chatPartner(messages[i].ChatPartner)
		if partner.Name != "" {
			return partner.Name
		}
	}
	return group.chatName
}

func p2pTitle(group *inboxGroup, contextMessages []adapter.Message, me string) string {
	if group == nil {
		return ""
	}
	if name := p2pName(group, contextMessages, me); name != "" {
		return name
	}
	messages := append(append([]adapter.Message(nil), group.messages...), contextMessages...)
	for i := len(messages) - 1; i >= 0; i-- {
		partner := chatPartner(messages[i].ChatPartner)
		if value := firstNonEmpty(partner.OpenID, partner.AppID, partner.ID); value != "" {
			return value
		}
	}
	for _, message := range messages {
		if name := strings.TrimSpace(message.Sender.Name); name != "" {
			return name
		}
	}
	return group.chatID
}

type partnerInfo struct {
	ID         string
	IDType     string
	OpenID     string
	AppID      string
	SenderType string
	Name       string
}

// chatPartner normalizes the small partner object emitted by different
// lark-cli releases. Keeping Message.ChatPartner raw lets older contracts pass
// through unchanged while still making direct-chat titles useful when the
// search batch contains only messages from the user.
func chatPartner(raw json.RawMessage) partnerInfo {
	if len(raw) == 0 || string(raw) == "null" {
		return partnerInfo{}
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return partnerInfo{}
	}
	get := func(keys ...string) string {
		for _, key := range keys {
			if s, ok := value[key].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
		return ""
	}
	return partnerInfo{
		ID:         get("id"),
		IDType:     get("id_type", "idType"),
		OpenID:     get("open_id", "openId"),
		AppID:      get("app_id", "appId"),
		SenderType: get("sender_type", "senderType"),
		Name:       get("name", "display_name", "displayName", "user_name", "userName", "app_name", "appName"),
	}
}

func senderIsBot(sender adapter.Sender) bool {
	return strings.EqualFold(strings.TrimSpace(sender.SenderType), "app") ||
		strings.EqualFold(strings.TrimSpace(sender.IDType), "app_id")
}

func partnerIsBot(partner partnerInfo) bool {
	return strings.EqualFold(strings.TrimSpace(partner.SenderType), "app") ||
		strings.EqualFold(strings.TrimSpace(partner.IDType), "app_id") || partner.AppID != ""
}

func p2pIsBot(group *inboxGroup, contextMessages []adapter.Message, me string) bool {
	if group == nil {
		return false
	}
	messages := append(append([]adapter.Message(nil), group.messages...), contextMessages...)
	for _, message := range messages {
		if message.Sender.ID != me && senderIsBot(message.Sender) {
			return true
		}
		if partnerIsBot(chatPartner(message.ChatPartner)) {
			return true
		}
	}
	return false
}

func latestSenderTag(messages []adapter.Message, me string) (connectorwire.Tag, bool) {
	messages = mergeMessages(nil, messages)
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Sender.ID == me || (message.Sender.ID == "" && message.Sender.AppID == "" && message.Sender.OpenBotID == "") {
			continue
		}
		value := firstNonEmpty(message.Sender.ID, message.Sender.AppID)
		if senderIsBot(message.Sender) {
			value = firstNonEmpty(message.Sender.AppID, value, message.Sender.OpenBotID)
		}
		if value == "" {
			continue
		}
		return connectorwire.Tag{Key: connectorwire.TagSender, Value: value, Name: firstNonEmpty(message.Sender.Name, value)}, true
	}
	return connectorwire.Tag{}, false
}

func isBoundAppMessage(message adapter.Message, appID string) bool {
	appID = strings.TrimSpace(appID)
	if appID == "" || strings.TrimSpace(message.Sender.ID) != appID {
		return false
	}
	return senderIsBot(message.Sender)
}

// System events are not new conversation requests. Keep real attachments and bot
// notifications even when their body has no extractable text.
func conversationMessage(message adapter.Message) bool {
	return !message.Deleted && !strings.EqualFold(strings.TrimSpace(message.MsgType), "system") &&
		firstNonEmpty(message.Sender.ID, message.Sender.AppID, message.Sender.OpenBotID) != ""
}

func (c *Connector) inboxStateForTests() persistedInboxState {
	c.inboxMu.Lock()
	defer c.inboxMu.Unlock()
	return c.inboxState
}

func topicID(message adapter.Message) string {
	if message.ThreadID != "" {
		return message.ThreadID
	}
	if message.RootID != "" {
		return message.RootID
	}
	if message.ParentID != "" {
		return message.ParentID
	}
	return ""
}

func chatIDFromThread(thread string) (string, bool) {
	const prefix = "larkcli:chat:"
	rest, ok := strings.CutPrefix(thread, prefix)
	if !ok || rest == "" {
		return "", false
	}
	if i := strings.Index(rest, ":thread:"); i >= 0 {
		rest = rest[:i]
	}
	return rest, rest != ""
}

func messageKey(message adapter.Message) string {
	if message.ChatID == "" {
		return ""
	}
	if topic := topicID(message); topic != "" {
		return message.ChatID + "\x00" + topic
	}
	return message.ChatID
}

// atAll is an @all written in the content: lark-cli lists only people in mentions, so an @all reaches the Inbox as
// "@_all" in a text message and as <at user_id="all"> in a post.
var atAll = lazyre.New(`(?:^|\s)@_all(?:\s|$)|<at\s+user_id="all"`)

func messageMentions(message adapter.Message, me string) bool {
	return mentionsOwner(message, me) || mentionsAll(message)
}

// mentionsOwner: the message @'s the owner, by the mention's id only: another person can have the owner's display name.
func mentionsOwner(message adapter.Message, me string) bool {
	if me == "" {
		return false
	}
	for _, mention := range message.Mentions {
		if mention.ID == me {
			return true
		}
	}
	return false
}

// mentionsAll: the message @'s everyone.
func mentionsAll(message adapter.Message) bool {
	if atAll.MatchString(rawText(message.Content)) {
		return true
	}
	for _, mention := range message.Mentions {
		id, key, name := strings.ToLower(strings.TrimSpace(mention.ID)), strings.ToLower(strings.TrimSpace(mention.Key)), strings.ToLower(strings.TrimSpace(mention.Name))
		if id == "all" || id == "@all" || strings.Contains(id, "@_all") || strings.Contains(key, "@all") || strings.Contains(name, "@all") || strings.Contains(name, "everyone") || strings.Contains(name, "所有人") { // i18n-ignore: match Feishu's localized @all mention name
			return true
		}
	}
	return false
}

func messageTime(message adapter.Message) time.Time {
	value := strings.TrimSpace(message.CreateTime)
	if value == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t.UTC()
	}
	// lark-cli 1.0.99 prints message times as local wall-clock minutes ("2006-01-02 15:04") in the time zone of the
	// machine running it, which is the hub's. Without this the Inbox cursor never moved past its first two minutes.
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, value, cliLocation()); err == nil {
			return t.UTC()
		}
	}
	if n, err := strconv.ParseInt(value, 10, 64); err == nil && n > 0 {
		if len(value) >= 13 {
			return time.UnixMilli(n).UTC()
		}
		return time.Unix(n, 0).UTC()
	}
	return time.Time{}
}

func after(message, previous adapter.Message) bool {
	if message.ChatID != "" && message.ChatID == previous.ChatID {
		position, err := strconv.ParseUint(message.MessagePosition, 10, 64)
		prior, priorErr := strconv.ParseUint(previous.MessagePosition, 10, 64)
		if err == nil && priorErr == nil && position > 0 && prior > 0 {
			return position > prior
		}
	}
	tm, tp := messageTime(message), messageTime(previous)
	if tm.IsZero() || tp.IsZero() {
		return false // An opaque ID cannot establish whether the owner replied later.
	}
	return tm.After(tp)
}

// messageLess keeps messages with the same wall-clock minute in source order.
// Position is scoped to a chat; group chats first so the comparator remains transitive.
func messageLess(a, b adapter.Message) bool {
	ta, tb := messageTime(a), messageTime(b)
	if !ta.Equal(tb) {
		return ta.Before(tb)
	}
	if a.ChatID != b.ChatID {
		return a.ChatID < b.ChatID
	}
	pa, _ := strconv.ParseUint(a.MessagePosition, 10, 64)
	pb, _ := strconv.ParseUint(b.MessagePosition, 10, 64)
	if (pa > 0) != (pb > 0) {
		return pa > 0
	}
	if pa != pb {
		return pa < pb
	}
	return a.MessageID < b.MessageID
}

func messageText(message adapter.Message) string {
	return cardText(rawText(message.Content))
}

func inboxMessageText(message adapter.Message, lang i18n.Lang) string {
	if text := strings.TrimSpace(messageText(message)); text != "" {
		return text
	}
	key := "hub-connectors.larkcli_placeholder_message"
	switch strings.ToLower(strings.TrimSpace(message.MsgType)) {
	case "":
		return ""
	case "image":
		key = "hub-connectors.larkcli_placeholder_image"
	case "file":
		key = "hub-connectors.larkcli_placeholder_file"
	case "audio":
		key = "hub-connectors.larkcli_placeholder_audio"
	case "media", "video":
		key = "hub-connectors.larkcli_placeholder_video"
	case "sticker":
		key = "hub-connectors.larkcli_placeholder_sticker"
	case "interactive":
		key = "hub-connectors.larkcli_placeholder_card"
	}
	return "[" + i18n.T(lang, key) + "]"
}

var (
	cardOpen  = lazyre.New(`<card(?:\s+title="([^"]*)")?\s*>`)
	cardClose = lazyre.New(`</card>`)
)

// cardText turns lark-cli's rendering of an interactive card (`<card title="…">body</card>`) into plain text: the
// title on its own line, then the body, so a bot's card reads "Merge Request Merged" rather than its markup.
func cardText(s string) string {
	if !strings.Contains(s, "<card") {
		return s
	}
	s = cardOpen.ReplaceAllString(s, "$1\n")
	s = cardClose.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		var nested json.RawMessage
		if json.Unmarshal([]byte(s), &nested) == nil {
			return rawText(nested)
		}
		return s
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return strings.TrimSpace(string(raw))
	}
	var parts []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if text, ok := x["text"].(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, text)
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		}
	}
	walk(value)
	return strings.Join(parts, "\n")
}

// The marks after a sender that say whom a message @'s, as the tier criteria read them (prompts/inbox/tiers.md): there a
// Lark group message without them did not @ the owner.
const (
	markOwner = "（@了我）"  // i18n-ignore: the tier criteria's mark, read by the model
	markAll   = "（@所有人）" // i18n-ignore: the tier criteria's mark, read by the model
)

// contextFor renders the incoming batch and bounded background, each in time order.
func contextFor(current, history []adapter.Message, me string) string {
	from := max(len(history)-contextEarlier, 0)
	if first := messageTime(current[0]); !first.IsZero() {
		for from < len(history)-contextEarlierKeep {
			if at := messageTime(history[from]); !at.IsZero() && first.Sub(at) <= contextHistory {
				break
			}
			from++
		}
	}
	return prompts.RenderLarkCLIInboxContext(prompts.LarkCLIInboxContext{
		Current: contextLines(current, me, contextNewRunes),
		History: contextLines(history[from:], me, contextEarlierRunes),
	})
}

// contextLines writes one line per message with its timestamp and structured mention marks.
// The text is limited to runes (0: whole). me is the owner's id.
func contextLines(messages []adapter.Message, me string, runes int) string {
	var b strings.Builder
	for _, message := range messages {
		text := inboxMessageText(message, i18n.En)
		if text == "" {
			continue
		}
		if r := []rune(text); runes > 0 && len(r) > runes {
			text = string(r[:runes]) + "…"
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		if at := messageTime(message); !at.IsZero() {
			b.WriteString("[")
			b.WriteString(at.Format(time.RFC3339))
			b.WriteString("] ")
		}
		start := b.Len()
		name := message.Sender.Name
		if name == "" {
			name = message.Sender.ID
		}
		b.WriteString(name)
		if mentionsOwner(message, me) {
			b.WriteString(markOwner)
		}
		if mentionsAll(message) {
			b.WriteString(markAll)
		}
		if b.Len() > start {
			b.WriteString(": ")
		}
		b.WriteString(text)
	}
	return b.String()
}

func mergeMessages(primary, extra []adapter.Message) []adapter.Message {
	seen := make(map[string]struct{}, len(primary)+len(extra))
	out := make([]adapter.Message, 0, len(primary)+len(extra))
	for _, list := range [][]adapter.Message{primary, extra} {
		for _, message := range list {
			if message.MessageID != "" {
				if _, ok := seen[message.MessageID]; ok {
					continue
				}
				seen[message.MessageID] = struct{}{}
			}
			out = append(out, message)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return messageLess(out[i], out[j])
	})
	return out
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if i := strings.IndexByte(value, '\n'); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	runes := []rune(value)
	if len(runes) > 180 {
		return string(runes[:180]) + "…"
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ connectorwire.Inbox = (*Connector)(nil)
var _ connectorwire.Events = (*Connector)(nil)
