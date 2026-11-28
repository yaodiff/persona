package assistant

import (
	"context"
	"slices"
)

// A hand-off conversation's write-back as a card in that conversation (#5932, docs/assistant/inbox.md "Item actions"):
// when the conversation an item was handed to hands in a reply or a conclusion, the user sees it where they are
// talking, not only on the item. The card is a snapshot of the suggestion as it was handed in; the item stays the one
// home of the pending request: the apps read the item to tell whether the card can still be sent (its suggestion is
// still this version from this conversation) or how it ended (sent, filed, replaced), and confirm it through
// POST /api/v1/inbox/{id}/suggestion like the item's own card. Nothing is patched into the card afterwards, and it
// is not counted in Needs you: the item is.

// feedInboxSuggestion is the card's feed kind; ref: the inbox item id; body: handoffCard. Apps that do not know it
// draw nothing for it.
const feedInboxSuggestion = "inbox_suggestion"

// handoffCard is what the card shows: the suggestion and enough of the item to draw it without the item.
type handoffCard struct {
	Version  int            `json:"version"`
	Kind     string         `json:"kind"`              // reply | conclusion
	Outcome  string         `json:"outcome,omitempty"` // a conclusion's: fyi | done
	Text     string         `json:"text"`
	Mentions []InboxMention `json:"mentions,omitempty"`
	Note     string         `json:"note,omitempty"`
	Recap    string         `json:"recap,omitempty"`
	Title    string         `json:"title"`
	Source   string         `json:"source"`
	Account  string         `json:"account,omitempty"`
	Sender   string         `json:"sender,omitempty"`
}

// handoffCardOf is the card for suggestion g handed in from a conversation the item was handed to, with a
// conclusion's outcome; nil for one that gets none (an Agent to start, a handback: the item's page asks those).
// Under b.mu.
func handoffCardOf(it *InboxItem, g *InboxSuggestion, outcome string) *handoffCard {
	switch {
	case g == nil || g.Conversation == nil || g.Handback:
		return nil
	case g.Kind == SuggestReply:
		outcome = ""
	case g.Kind != SuggestConclusion:
		return nil
	}
	return &handoffCard{Version: g.Version, Kind: g.Kind, Outcome: outcome, Text: g.Text, Mentions: slices.Clone(g.Mentions),
		Note: g.Note, Recap: it.Recap, Title: clip(oneLine(it.Title), 200), Source: it.Source, Account: it.AccountName, Sender: it.Sender}
}

// postHandoffCard puts the card for item itemID into the conversation it came from. A failure is only logged: the
// suggestion is on the item either way.
func (s *Service) postHandoffCard(ctx context.Context, to SessionRef, itemID string, card handoffCard) {
	if _, err := s.appendFeed(ctx, to.SessionID, feedInboxSuggestion, itemID, card); err != nil {
		s.logf("inbox %s: card in %s: %v", itemID, short(to.SessionID), err)
	}
}
