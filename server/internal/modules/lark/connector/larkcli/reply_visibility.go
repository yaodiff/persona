package larkcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nwverse/persona/server/internal/connectorwire"
	adapter "github.com/nwverse/persona/server/internal/modules/lark/larkcli"
)

var errReplyVisibilityDenied = errors.New("lark-cli: this chat is excluded from AI and knowledge-base reads")

func parseExcludedChatIDs(raw json.RawMessage) (map[string]struct{}, error) {
	var values []string
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("options.inbox_exclude_chats: must be an array of chat ids")
		}
	}
	ids := make(map[string]struct{}, len(values))
	for _, id := range values {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("options.inbox_exclude_chats contains an empty chat id")
		}
		if len(id) > 256 {
			return nil, fmt.Errorf("options.inbox_exclude_chats chat id is too long")
		}
		if _, exists := ids[id]; exists {
			return nil, fmt.Errorf("options.inbox_exclude_chats contains a duplicate chat id")
		}
		ids[id] = struct{}{}
	}
	return ids, nil
}

func directChatType(chatType, chatMode string) (direct, known bool) {
	values := []string{strings.ToLower(strings.TrimSpace(chatType)), strings.ToLower(strings.TrimSpace(chatMode))}
	for _, kind := range values {
		if kind == "p2p" || kind == "direct" {
			return true, true
		}
	}
	for _, kind := range values {
		switch kind {
		case "group", "topic":
			return false, true
		}
	}
	return false, false
}

func (c *Connector) replyChatExcludedLocked(chatID, chatType, chatMode string) bool {
	if c.managedInboxVisibilityInvalid {
		return true
	}
	chatID = strings.TrimSpace(chatID)
	_, listed := c.inboxExcludeChats[chatID]
	_, managedListed := c.managedInboxExcludeChats[chatID]
	if (listed || managedListed) && chatID != "" {
		return true
	}
	if !c.inboxExcludeDirectChats && !c.managedInboxExcludeDirectChats {
		return false
	}
	direct, known := directChatType(chatType, chatMode)
	return !known || direct
}

func (c *Connector) replyChatExcluded(chatID, chatType, chatMode string) bool {
	c.inboxMu.Lock()
	defer c.inboxMu.Unlock()
	return c.replyChatExcludedLocked(chatID, chatType, chatMode)
}

func (c *Connector) replyMessageExcluded(message adapter.Message) bool {
	return c.replyChatExcluded(message.ChatID, message.ChatType, message.ChatMode)
}

func (c *Connector) replyThingExcluded(thing connectorwire.InboxThing) bool {
	c.inboxMu.Lock()
	defer c.inboxMu.Unlock()
	return c.replyThingExcludedLocked(thing)
}

func (c *Connector) replyThingExcludedLocked(thing connectorwire.InboxThing) bool {
	chatID := ""
	for _, tag := range thing.Tags {
		if tag.Key == connectorwire.TagChat {
			chatID = tag.Value
			break
		}
	}
	kind := ""
	switch thing.Kind {
	case "dm", "bot":
		kind = "p2p"
	case "group", "mention", connectorwire.OwnerPromiseKind:
		kind = "group"
	}
	return c.replyChatExcludedLocked(chatID, kind, "")
}

// SetManagedInboxVisibility applies manager.json's central boundary without persisting it in connector options.
// Invalid input fails the whole source closed: a malformed privacy boundary must not silently select legacy reads.
func (c *Connector) SetManagedInboxVisibility(chats []string, direct bool) error {
	raw, _ := json.Marshal(chats)
	excluded, err := parseExcludedChatIDs(raw)
	c.inboxMu.Lock()
	c.managedInboxVisibilityInvalid = err != nil
	c.managedInboxExcludeChats = excluded
	c.managedInboxExcludeDirectChats = direct
	c.inboxMu.Unlock()
	return err
}

func (c *Connector) replyVisibilityEnabled() bool {
	c.inboxMu.Lock()
	defer c.inboxMu.Unlock()
	return c.managedInboxVisibilityInvalid || c.inboxExcludeDirectChats || c.managedInboxExcludeDirectChats ||
		len(c.inboxExcludeChats) > 0 || len(c.managedInboxExcludeChats) > 0
}

func (c *Connector) filterReplyChats(page adapter.ChatList) adapter.ChatList {
	if !c.replyVisibilityEnabled() {
		return page
	}
	visible := page.Chats[:0]
	for _, chat := range page.Chats {
		if !c.replyChatExcluded(chat.ChatID, chat.ChatType, chat.ChatMode) {
			visible = append(visible, chat)
		}
	}
	page.Chats = visible
	page.Total = len(visible)
	return page
}

// checkReplyChat vetoes a model/KB target before any message-body read. Explicit exclusions need no subprocess. The
// all-direct-chats switch uses one single-chat metadata read; an absent or unknown type fails closed. Thread-only reads
// have no verifiable chat and are refused whenever either control is enabled.
func (c *Connector) checkReplyChat(ctx context.Context, chatID string) error {
	chatID = strings.TrimSpace(chatID)
	c.inboxMu.Lock()
	_, listed := c.inboxExcludeChats[chatID]
	_, managedListed := c.managedInboxExcludeChats[chatID]
	listed = listed || managedListed
	allDirect := c.inboxExcludeDirectChats || c.managedInboxExcludeDirectChats
	invalid := c.managedInboxVisibilityInvalid
	enabled := invalid || allDirect || len(c.inboxExcludeChats) > 0 || len(c.managedInboxExcludeChats) > 0
	c.inboxMu.Unlock()
	if !enabled {
		return nil
	}
	if invalid || chatID == "" || listed {
		return errReplyVisibilityDenied
	}
	if !allDirect {
		return nil
	}
	chat, err := c.runner.GetChat(ctx, chatID)
	if err != nil {
		return errReplyVisibilityDenied
	}
	direct, known := directChatType(chat.ChatType, chat.ChatMode)
	if known && !direct {
		return nil
	}
	return errReplyVisibilityDenied
}
