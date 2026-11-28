package extmgr

import (
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/larksyncwire"
)

// larkMessagesPageSize is the maximum number of messages carried in one event.lark.messages notification.
// Large sync rounds are split into multiple pages of this size so each JSON-RPC frame stays small and bounded.
const larkMessagesPageSize = 100

// emitLarkMessages queues batched, paginated event.lark.messages metadata notifications after a sync round commits
// new messages to the outbox. Only extensions subscribed to lark.messages and explicitly granted lark.messages.read
// receive them; message bodies, display names and attachments are never included.
func (h *Manager) emitLarkMessages(binding string, msgs []larksyncwire.Message) {
	if h == nil || !h.extensions.larkMessagesWanted() || len(msgs) == 0 {
		return
	}
	for _, ev := range larkMessageEventPages(binding, msgs, larkMessagesPageSize) {
		h.extensions.queueOn(&h.extensions.larkEvents, extensionNotice{
			topic:   larksyncwire.EventLarkMessages,
			payload: fieldsOf(ev),
		})
	}
}

// larkMessagesWanted reports whether any enabled, compatible extension is subscribed to lark.messages and granted
// lark.messages.read. With no registry (queue-only unit tests) it returns true; emit rechecks before delivery.
func (ps *extensions) larkMessagesWanted() bool {
	if ps == nil {
		return false
	}
	return ps.reg == nil || len(ps.reg.EventSubscribers(larksyncwire.EventLarkMessages, extension.EventScopes[larksyncwire.EventLarkMessages])) > 0
}

// larkMessageEventPages projects msgs to allowlisted metadata (no body, no display names, no attachments), sorts
// stably in chronological order, and splits the round into pages of at most pageSize items.
func larkMessageEventPages(binding string, msgs []larksyncwire.Message, pageSize int) []larksyncwire.MessagesEvent {
	if pageSize <= 0 {
		pageSize = larkMessagesPageSize
	}
	binding = strings.TrimSpace(binding)
	items := make([]larksyncwire.MessageEventItem, 0, len(msgs))
	for _, m := range msgs {
		chatID := strings.TrimSpace(m.ChatID)
		msgID := strings.TrimSpace(m.ID)
		if chatID == "" || msgID == "" {
			continue
		}
		item := larksyncwire.MessageEventItem{
			ChatID:       larksyncwire.ChatID(chatID),
			MessageID:    larksyncwire.MessageID(msgID),
			SenderOpenID: larksyncwire.OpenID(strings.TrimSpace(m.SenderID)),
			Type:         larksyncwire.MessageType(strings.TrimSpace(m.Type)),
		}
		if !m.At.IsZero() {
			item.At = m.At.UTC()
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil
	}
	slices.SortStableFunc(items, func(a, b larksyncwire.MessageEventItem) int {
		if cmp := a.At.Compare(b.At); cmp != 0 {
			return cmp
		}
		if a.ChatID != b.ChatID {
			return strings.Compare(string(a.ChatID), string(b.ChatID))
		}
		return strings.Compare(string(a.MessageID), string(b.MessageID))
	})
	total := len(items)
	pages := (total + pageSize - 1) / pageSize
	out := make([]larksyncwire.MessagesEvent, 0, pages)
	for p := range pages {
		start := p * pageSize
		end := min(start+pageSize, total)
		out = append(out, larksyncwire.MessagesEvent{
			Binding:  binding,
			Page:     p + 1,
			Pages:    pages,
			Total:    total,
			Messages: items[start:end],
		})
	}
	return out
}
