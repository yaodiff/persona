package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The inbox on the bus: what the inbox tells the hub. The hub's calls about it are bus methods the Inbox module
// registers through the kernel (inbox_module.go).

// InboxChanged tells the hub inbox items changed (inbox.changed, one way).
func (b *BusHub) InboxChanged(_ context.Context, c assistantwire.InboxChanged) {
	if err := b.notifyHub(assistantwire.EventInboxChanged, c); err != nil {
		b.opts.Logf("assistant: %s: %v", assistantwire.EventInboxChanged, err)
	}
}

// sendFromCard is "Send" on a message card (POST /api/v1/inbox/{id}/reply with card): the reply goes out once per
// card however many taps, devices or retries ask — a card already sent answers with the item as it is (already).
func (s *Service) sendFromCard(ctx context.Context, cardID, itemID string, send func() (InboxItem, error)) (it InboxItem, already bool, err error) {
	b := &s.inbox
	b.cardSends.acquire(cardID)
	defer b.cardSends.release(cardID)
	sent, err := s.cardSent(ctx, cardID, itemID)
	if err != nil {
		return InboxItem{}, false, err
	}
	if sent {
		b.mu.Lock()
		if ib := b.items[itemID]; ib != nil {
			it = ib.listed()
		}
		b.mu.Unlock()
		return it, true, nil
	}
	// A card put in before its item was filed ("Done", "Ignore", read elsewhere, archived) is not a way to reply to it:
	// the reply would go out unasked and the item's mark would become "Replied". Moved back to the inbox, it sends.
	b.mu.Lock()
	ib := b.items[itemID]
	filed := ib != nil && !ib.open()
	b.mu.Unlock()
	if filed {
		return InboxItem{}, false, errInboxItemDone
	}
	it, err = send()
	return it, false, err
}
