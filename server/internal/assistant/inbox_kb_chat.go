package assistant

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/deeplink"
	"github.com/nwverse/persona/server/internal/kb"
)

// The Inbox's side of a knowledge base chat and its items (#15356, kb_inbox.go): which of the Inbox's items came from
// a chat, and which chat an item came from. These read the Inbox's book; the chat's page (kb_inbox.go) only words
// what they return.

// fromKBChat says whether it came from chat c. It reads only it and the sources' word (kbChatOf takes srcMu, after b.mu
// when the caller holds that).
func (b *inboxBook) fromKBChat(it *InboxItem, c *kb.Chat) bool {
	if x := it.Connector; x != nil && x.Thread != "" && x.Thread == c.SourceID && it.Source == kbChatConnector(c) {
		return true
	}
	return b.kbChatOf(it) == c.ID
}

// inboxOfKBChat is chat c's items as the Inbox lists them (sources=all: open, and the done ones the list still has),
// open first, each group newest first, at most kbChatInboxMost, each with what it received (said). nil when the Inbox
// has none of c's, or cannot be read.
func (s *Service) inboxOfKBChat(ctx context.Context, c *kb.Chat) []*kbChatInboxItem {
	if s.inboxReady(ctx) != nil {
		return nil
	}
	b := &s.inbox
	b.mu.Lock()
	cutoff := time.Now().Add(-inboxKeepDone)
	cut := b.listedDoneCut(cutoff, true)
	byID := map[string]*kbChatInboxItem{}
	var out []*kbChatInboxItem
	for _, it := range b.items {
		shown := it
		if it.MergedInto != "" { // its survivor shows, as a link naming it opens that one
			if shown = b.items[it.MergedInto]; shown == nil {
				continue
			}
		}
		// Joined on the row itself, not its survivor: the survivor is listed on the chat of each row it stands for and
		// takes there what that row received, which is what that chat's recent holds (a member keeps its own chain, and
		// its source keeps collecting into it, followMerged). No visibility filter: that boundary keeps content from the
		// AI, not from the owner, whose Inbox list has a quarantined item too (inboxListAt), and this asks no model.
		if shown.MergedInto != "" || !inboxShows(shown, cutoff, cut, true) || !b.fromKBChat(it, c) {
			continue
		}
		x := byID[shown.ID]
		if x == nil {
			x = &kbChatInboxItem{ID: shown.ID, Title: shown.Title, Tier: shown.Tier, Status: shown.status(), Summary: shown.Summary,
				Preview: clip(oneLine(shown.Preview), chainMessageText), UpdatedAt: shown.UpdatedAt,
				DoneAt: doneAtOf(shown)}
			byID[shown.ID] = x
			out = append(out, x)
		}
		x.said = append(x.said, inboxSaidOf(it)...)
	}
	b.mu.Unlock()
	if len(out) == 0 {
		return nil
	}
	slices.SortFunc(out, func(x, y *kbChatInboxItem) int {
		if (x.DoneAt == nil) != (y.DoneAt == nil) {
			if x.DoneAt == nil {
				return -1
			}
			return 1
		}
		return inboxListOrder(x.UpdatedAt, x.ID, y.UpdatedAt, y.ID)
	})
	return out[:min(len(out), kbChatInboxMost)]
}

// doneAtOf is a copy of it's done time for a view of it (nil while it is open), so the view never shares the item's.
// Under b.mu.
func doneAtOf(it *InboxItem) *time.Time {
	if it.DoneAt == nil {
		return nil
	}
	at := *it.DoneAt
	return &at
}

// inboxSaidOf is what it received, by its chain's received entries. Under b.mu.
func inboxSaidOf(it *InboxItem) []inboxSaid {
	var out []inboxSaid
	from := it.StartedAt
	for _, e := range it.Chain {
		if e.Kind != ChainReceived {
			continue
		}
		for _, m := range e.Messages {
			x := inboxSaid{sender: m.Sender, text: m.Text, from: from, to: e.At}
			if x.sender == "" {
				x.sender = e.Sender
			}
			if m.At != nil {
				x.from, x.to = *m.At, *m.At
			}
			out = append(out, x)
		}
		from = e.At
	}
	return out
}

// itemKBChat is the knowledge base chat it came from, when the knowledge base has one: the chat its connector
// archives its thread under, or the one kbChatOf names. nil without, or with the knowledge base off or not open.
//
// The connector's chat is "<source>-<thread>" (kb.ChatID), where source is the connector's id or one of its
// manifest's ArchiveSources (connectorwire.ChatBatch.Source), and the chat keeps the connector as its Connector
// whichever name it is filed under. So it is looked up under the connector's id first, then among the chats whose id
// ends in "-<thread>", each held to fromKBChat (its Connector and SourceID). A chat whose id kb.SafeID cut short is not
// found that way.
func (s *Service) itemKBChat(ctx context.Context, it *InboxItem) *inboxKBChat {
	var ids []string
	thread := ""
	if x := it.Connector; x != nil && x.Thread != "" {
		thread = x.Thread
		ids = append(ids, kb.ChatID(it.Source, thread))
	}
	b := &s.inbox
	if id := b.kbChatOf(it); id != "" {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	e, _ := s.kbGet(ctx)
	if e == nil {
		return nil
	}
	found := func(id string) *inboxKBChat {
		if c, ok := e.Store.Chat(id); ok && b.fromKBChat(it, c) {
			return &inboxKBChat{ID: c.ID, Path: deeplink.KnowledgeEntry(kb.KindChat, c.ID)}
		}
		return nil
	}
	for _, id := range ids {
		if c := found(id); c != nil {
			return c
		}
	}
	if thread == "" {
		return nil
	}
	suffix := kb.SafeID("-" + thread) // what kb.ChatID ends in, whatever the source before it
	for _, id := range e.Store.IDs(kb.KindChat) {
		if strings.HasSuffix(id, suffix) && !slices.Contains(ids, id) {
			if c := found(id); c != nil {
				return c
			}
		}
	}
	return nil
}
