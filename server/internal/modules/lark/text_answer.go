package lark

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// botAnswerQuestion lets the owner's next plain message answer a waiting single-question card (IxQuestion,
// one question) in the same Feishu topic, like its press: channel.MatchOption reads a number, ordinal or option name
// (several for a multi-select), and interactions.resolve carries the message's one-use owner action. Words that name no
// option are, inside the card's topic, the free answer itself, submitted exactly like the card's input box and its
// Answer button (answerFromCard, free=1): a question without options is answered in words, and an option question
// takes its Other that way. In the root chat such words stay a message to the conversation, as before. Forms, plans,
// permissions and terminal dialogs take no typed answer (#4675). A message written inside another agent's topic never
// answers a card outside that topic; a message in the root chat answers the newest card anywhere. False leaves the
// message to the agent; a card no longer pending is closed, saying whether it was answered elsewhere, expired or was
// withdrawn, and the words are swallowed rather than let into a conversation. The same goes for an option typed for
// a card that already ended elsewhere and was closed (b.ended); any other message forgets such cards in its scope.
func (s core) botAnswerQuestion(ctx context.Context, messageID, text, action, root, parent string) bool {
	text = strings.TrimSpace(text)
	if action == "" || text == "" {
		return false
	}
	inTopic := root != "" || parent != ""
	inScope := func(c botCard) bool {
		return typedAnswerable(c) && (!inTopic || cardInTopic(c, root, parent)) // words in another agent's topic are not stolen for a card elsewhere
	}
	b := s.fbot()
	b.mu.Lock()
	var card botCard
	for _, c := range b.cards {
		if inScope(c) && (card.Ix.ID == "" || c.At.After(card.At)) {
			card = c
		}
	}
	var gone botCard
	now := b.clock()
	for id, e := range b.ended {
		switch {
		case now.Sub(e.at) >= endedAskFor:
			delete(b.ended, id)
		case inScope(e.card):
			delete(b.ended, id) // read once: whatever this message is, what comes after it is not a late answer
			if gone.Ix.ID == "" || e.card.At.After(gone.At) {
				gone = e.card
			}
		}
	}
	b.mu.Unlock()
	if gone.Ix.ID != "" && (card.Ix.ID == "" || gone.At.After(card.At)) {
		// The newest card here already ended elsewhere: an option for it is a late answer, not a new message.
		if _, ok := channel.MatchOption(gone.Ix.Questions[0], text); !ok {
			return false
		}
		s.botNote(ctx, messageID, i18n.T(s.botLang(ctx), "assistant-channels_plugin.answered_elsewhere"))
		return true
	}
	if card.Ix.ID == "" {
		return false
	}
	ix := card.Ix
	answer, matched := channel.MatchOption(ix.Questions[0], text)
	if !matched {
		// Inside the card's topic the words are the free answer itself (the card input box's free=1 submission):
		// a question without options, or an option question's Other. In the root chat they are left to the
		// conversation, the previous behavior (#5436).
		if !inTopic {
			return false
		}
		answer = text
	}
	l := s.botLang(ctx)
	var got struct {
		OK          bool              `json:"ok"`
		Status      string            `json:"status"`
		Interaction proto.Interaction `json:"interaction"`
	}
	if err := s.appCallTo(ctx, assistantwire.AppInteractions, InteractionsGet, map[string]any{"interaction_id": ix.ID}, &got); err != nil {
		s.logf("bots: typed answer %s: %v", ix.ID, err)
		return false
	}
	if !got.OK {
		// A replacement may arrive after the lookup. Only this displayed
		// occurrence's ledger entry may be removed by its cleanup.
		s.takeBotCardOccurrence(ix.ID, ix)
		s.botCardDone(ctx, card.MessageID, ix, s.h.EndedText(l, got.Status, "assistant-feishu_cards.handled"))
		s.botNote(ctx, messageID, s.h.EndedText(l, got.Status, "assistant-channels_plugin.answered_elsewhere"))
		return true
	}
	if differentBotCardOccurrence(ix, got.Interaction) {
		// The words were constructed from this occurrence's question. Never submit
		// them to a same-ID replacement or remove that replacement's ledger entry.
		s.botNote(ctx, messageID, i18n.T(l, "assistant-channels_plugin.answered_elsewhere"))
		return true
	}
	r := proto.Resolution{Answers: map[string]string{ix.Questions[0].Question: answer}}
	// Close the card optimistically, as a press does; put it back when the answer could not be handed over and the card
	// still waits (botRestoreCard). The occurrence check makes removal a compare-and-swap against a new announcement.
	current, had, same := s.takeBotCardOccurrence(ix.ID, ix)
	if !same {
		s.botNote(ctx, messageID, i18n.T(l, "assistant-channels_plugin.answered_elsewhere"))
		return true
	}
	var expected *time.Time
	if !ix.CreatedAt.IsZero() {
		expected = &ix.CreatedAt
	}
	switch ok, err := s.h.ResolveInteractionAt(ctx, ix.ID, r, action, expected); {
	case err != nil:
		s.logf("bots: typed answer %s: %v", ix.ID, err)
		if had {
			s.botRestoreCard(ctx, ix.ID, current)
		}
		s.botNote(ctx, messageID, i18n.T(l, "assistant-channels_plugin.answer_not_sent",
			i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
	case !ok:
		s.botCardDone(ctx, card.MessageID, ix, i18n.T(l, "assistant-feishu_cards.handled"))
		s.botNote(ctx, messageID, i18n.T(l, "assistant-channels_plugin.answered_elsewhere"))
	default:
		s.botCardDone(ctx, card.MessageID, ix, s.h.ResolutionText(l, ix, r))
	}
	return true
}

// typedAnswerable: c is a posted card a plain message can answer, one question (IxQuestion).
func typedAnswerable(c botCard) bool {
	return c.Ix.Kind == proto.IxQuestion && len(c.Ix.Questions) == 1 && c.MessageID != ""
}

// endedCard is a question card closed because it ended elsewhere, and when (feishuBook.ended).
type endedCard struct {
	card botCard
	at   time.Time
}

// noteEndedLocked remembers c, a card that ended without being answered in the chat, for endedAskFor; older ones past
// their time are dropped.
func (b *feishuBook) noteEndedLocked(c botCard) {
	now := b.clock()
	for id, e := range b.ended {
		if now.Sub(e.at) >= endedAskFor {
			delete(b.ended, id)
		}
	}
	if b.ended == nil {
		b.ended = map[string]endedCard{}
	}
	b.ended[c.Ix.ID] = endedCard{card: c, at: now}
}

// cardInTopic says whether a topic message belongs to the card's topic: it replies to the card itself (the card is
// then the topic root), or to the run anchor the card was posted under (the topic's root is the run's first message).
func cardInTopic(c botCard, root, parent string) bool {
	return c.MessageID == root || c.MessageID == parent ||
		(c.Thread != "" && (c.Thread == root || c.Thread == parent))
}
