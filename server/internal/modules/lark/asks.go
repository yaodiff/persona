package lark

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Cards for what Persona and the agents ask (the bot's cards switch): a question, a plan, a permission or a terminal
// dialog shows in the chat as a card with the same choices as the app's card; answering it in either place answers it
// (interactions.resolve), and the card in Feishu then says how it was answered (interaction.ended). Cards go out for
// what the chat drives (its target, and the conversations of its runs), or for everything with all_cards. The core
// tells the chat of each through the interaction port (larkhost.Asks, the Ask* methods of layer.go); what a card says
// of the core comes as one value (Host.AskView), where it goes as another (Host.AskRoute). The card buttons and the
// chat's commands are card_actions.go and commands.go; the pushes and sync mirror are the core's feishu_push.go.

// feishuInteraction: a new card (interaction.announced).
func (s core) feishuInteraction(ix proto.Interaction) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if !s.feishuCardWanted(ctx, ix) {
		return
	}
	s.postInteractionCard(ctx, ix)
}

// feishuCardWanted: the bot posts a card for ix (feishuInteraction): it is on with the cards switch, and the chat
// drives the conversation, it is an eligible non-main assistant write approval, or "all cards" is on. A card the chat
// does not drive itself never crosses the channel boundary when its words or track are sensitive (health.go, info_use.go).
func (s core) feishuCardWanted(ctx context.Context, ix proto.Interaction) bool {
	f, ready := s.feishuFeatures(ctx)
	if !ready || !f.Cards {
		return false
	}
	if ix.Private {
		return false // a Just me turn's card: never on Lark (meeting_justme.go)
	}
	if s.botDrives(ctx, ix) {
		return true
	}
	eligible := s.h.AskDirect(ctx, ix)
	if !eligible && !f.AllCards {
		return false
	}
	// The direct write-approval route is independent of all_cards, but not of the sensitive-content boundary.
	return !s.h.AskSensitive(ctx, ix)
}

// botCardLook is interaction id as the hub's queue and its history say (InteractionsGet): waiting, still up, with the
// interaction as the queue has it; over, ended for good; neither, not up and not known to be over (not restored yet,
// or the history could not say).
func (s core) botCardLook(ctx context.Context, id string) (ix proto.Interaction, waiting, over bool) {
	var got struct {
		OK          bool              `json:"ok"`
		Status      string            `json:"status"`
		Interaction proto.Interaction `json:"interaction"`
	}
	switch err := s.appCallTo(ctx, assistantwire.AppInteractions, InteractionsGet, map[string]any{"interaction_id": id}, &got); {
	case err != nil:
		return proto.Interaction{}, false, false
	case got.OK:
		return got.Interaction, true, false
	case finalStatus(got.Status):
		return proto.Interaction{}, false, true
	}
	return proto.Interaction{}, false, false
}

// finalStatus: how an interaction ended for good (InteractionsGet's status); "pending" (in the history, not up yet)
// and "" (no record, or the history could not be read) are not.
func finalStatus(status string) bool {
	switch status {
	case "resolved", "dismissed", "expired", "cancelled":
		return true
	}
	return false
}

// interactionEnded: the card's exact occurrence ended (interaction.ended came), maybe while it was being sent.
func (s core) interactionEnded(ix proto.Interaction) bool {
	ended, _ := s.h.AskStateFor(ix)
	return ended
}

// postInteractionCard posts ix's card once, unless it ended meanwhile.
func (s core) postInteractionCard(ctx context.Context, ix proto.Interaction) {
	if s.interactionEnded(ix) { // ended while this looked at the bot: not asked (the core's record before the chat, never in it)
		return
	}
	if hook, ok := s.h.(interface{ AskPostHook(proto.Interaction) }); ok {
		hook.AskPostHook(ix)
	}
	claimed := false
	var replaced *botCard
	post := &botCardPost{done: make(chan struct{}), card: botCard{Ix: ix}}
	dc := &s.t.detached
	dc.mu.Lock()
	s.book(ctx, func(b chatBook) {
		if current, ok := b.Card(ix.ID); ok {
			if !current.Ix.CreatedAt.IsZero() && !ix.CreatedAt.IsZero() && ix.CreatedAt.Before(current.Ix.CreatedAt) {
				return // a newer post won while this older occurrence was in flight
			}
			if !differentBotCardOccurrence(current.Ix, ix) {
				return
			}
			old := current
			replaced = &old
		}
		// The in-flight claim carries the occurrence too. An old terminal event or
		// callback can no longer delete a replacement's placeholder by ID alone.
		b.KeepCard(ix.ID, &botCard{Ix: ix})
		claimed = true
	})
	if claimed {
		if dc.posting == nil {
			dc.posting = map[string]*botCardPost{}
		}
		dc.posting[ix.ID] = post
	}
	dc.mu.Unlock()
	if !claimed {
		return
	}
	if replaced != nil && replaced.MessageID != "" {
		go func(old botCard) {
			closeCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			s.botCardDone(closeCtx, old.MessageID, old.Ix, i18n.T(s.botLang(closeCtx), "assistant-feishu_cards.handled"))
		}(*replaced)
	}
	defer func() {
		dc.mu.Lock()
		if dc.posting[ix.ID] == post {
			delete(dc.posting, ix.ID)
		}
		dc.mu.Unlock()
		close(post.done)
	}()
	reply, to, topic := s.interactionPlace(ctx, ix)
	ctx = s.sendAs(ctx, string(ix.Kind), ix.MachineID, ix.SessionID)
	l := s.botLang(ctx)
	v := s.h.AskView(ctx, ix)
	fallback := ""
	switch ix.Kind {
	case proto.IxQuestion:
		fallback = "❓ " + clip(firstQuestion(ix), 100)
	case proto.IxPermission:
		fallback = "🔐 " + v.Tool
	case proto.IxPlan:
		fallback = "📋 " + clip(firstLine(ix.Plan), 100)
	case proto.IxScreen:
		fallback = "🖥 " + i18n.T(l, "assistant-feishu_cards.screen_summary")
		if v.Terminal {
			fallback = "🖥 " + ixWhere(v) + " · " + v.TerminalText
		}
	}
	card := interactionCard(l, ix, v)
	if s.interactionEnded(ix) {
		// Answered while its card was being made (a program that answers cards at once): nobody needs to look at it,
		// so nothing is posted and only this occurrence's claim goes (#5430). One that ends during the post is closed after it, below.
		s.dropClaim(ctx, ix.ID, post)
		return
	}
	// A card for a topic is a reply to its run's message and belongs under it: if that message is gone it would be a
	// stray card in the group (the hub would send it into the chat), and the owner may not see it there. It goes to the
	// owner instead, as with a chat that refused it.
	to.NoDetour = to.ChatID != ""
	mid, err := s.botPostTo(ctx, to, reply, card, fallback, "")
	if err != nil && to.ChatID != "" && (chatRefused(err) || botcard.MessageGone(err)) {
		// The topic was refused outright (the group was taken off the owner's list, the bot is not in it, it was
		// dissolved) or the message to reply under is gone: nothing went out there, and the owner's DM gets the card
		// instead, as a message of its own (a reply to the topic's message would land in the topic). Any other failure
		// (a timeout, a gateway error) may have gone out, and a second card would show the same question twice: it is
		// only logged.
		s.logf("bots: card for %s refused in the topic: %v; sending it to the owner", ix.ID, err)
		reply, to = "", feishuTarget{}
		mid, err = s.botPostTo(ctx, to, "", card, fallback, "")
	}
	ended := s.interactionEnded(ix)
	if err != nil {
		s.logf("bots: card for %s: %v", ix.ID, err)
		s.dropClaim(ctx, ix.ID, post)
		return
	}
	kept, detached := false, false
	dc.mu.Lock()
	s.book(ctx, func(b chatBook) {
		post.card = botCard{MessageID: mid, Ix: ix, At: b.Now(), Thread: reply, ChatID: to.ChatID}
		if post.detached { // feishuDetach took it while Feishu had not answered: it closes this message, a reconnect reopens it
			detached = true
			return
		}
		current, still := b.Card(ix.ID)
		same := still && !differentBotCardOccurrence(current.Ix, ix)
		if !same || ended { // answered or replaced while this occurrence was being posted
			if same {
				b.KeepCard(ix.ID, nil)
			}
			return
		}
		card := post.card
		b.KeepCard(ix.ID, &card)
		kept = true
	})
	dc.mu.Unlock()
	if detached {
		return
	}
	if !kept {
		s.botCardDone(ctx, mid, ix, i18n.T(s.botLang(ctx), "assistant-feishu_cards.handled"))
		return
	}
	if topic != nil {
		// The run was asked in a topic and the card went to the DM: the topic is told where it is.
		s.botNoteTo(ctx, topic.to, topic.reply, i18n.T(s.botLang(ctx), "assistant-feishu_cards.sent_to_dm"))
	}
}

// dropClaim forgets card id's claim when its first post ends without a card to keep, unless feishuDetach took the card
// meanwhile: what is kept under id then is the detached card's, or a reconnect's. A same-ID replacement's claim stays.
func (s core) dropClaim(ctx context.Context, id string, post *botCardPost) {
	dc := &s.t.detached
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if post.detached {
		return
	}
	s.book(ctx, func(b chatBook) {
		current, still := b.Card(id)
		if still && !differentBotCardOccurrence(current.Ix, post.card.Ix) {
			b.KeepCard(id, nil)
		}
	})
}

// chatRefused: the hub or Feishu refused the send into a chat outright, so nothing went out: the hub's own refusal
// (a code on the bus) or Feishu's "the bot is not in the chat" / "the chat was dissolved".
func chatRefused(err error) bool {
	var re *rpc.Error
	if errors.As(err, &re) && re.Code == assistantwire.CodeChatDenied {
		return true
	}
	return botcard.ChatRefused(err)
}

// interactionPlace is where the card of ix goes: the message of the run it answers to reply to, and the topic if that
// run was asked in one (Host.AskRoute: the latest run of its conversation asked in the owner's DM and the latest asked
// in a topic). A conversation can have a private chat's run and a topic's at once (they are not folded into one); then
// the owner's DM wins, where only the owner sees the card (an approval or a plan must not be shown to a group), and of
// several runs of one place the one asked last decides.
func (s core) interactionPlace(ctx context.Context, ix proto.Interaction) (reply string, to feishuTarget, note *topicNote) {
	r := s.h.AskRoute(ctx, ix)
	private, topic := r.Private, r.Topic
	// An eligible Assistant approval always goes to the owner directly, even
	// when its only existing run is in a topic.
	if private == nil && topic != nil && s.h.AskDirect(ctx, ix) {
		return "", feishuTarget{}, &topicNote{reply: topic.ReplyTo, to: topic.To}
	}
	switch {
	case private != nil && topic != nil:
		// The card is not in the topic that asked: a short line there says it went to the DM.
		return private.ReplyTo, feishuTarget{}, &topicNote{reply: topic.ReplyTo, to: topic.To}
	case private != nil:
		return private.ReplyTo, feishuTarget{}, nil
	case topic != nil:
		return topic.ReplyTo, topic.To, nil
	}
	return "", feishuTarget{}, nil
}

// topicNote is where a line goes that tells a topic its run's card went to the DM: the run's message, in its topic.
type topicNote struct {
	reply string
	to    feishuTarget
}

// botDrives: the chat drives ix's conversation (its target now, a run on it, or a thread bound to it).
func (s core) botDrives(ctx context.Context, ix proto.Interaction) bool {
	machine, session := ix.MachineID, ix.SessionID
	r := s.h.AskRoute(ctx, ix)
	if r.Driven {
		return true
	}
	bound := false
	s.book(ctx, func(b chatBook) { bound = b.Bound(machine, session, 24*time.Hour) })
	if bound {
		return true
	}
	cur := s.botTargetFor("", "")
	if session == "" {
		return false
	}
	if cur.Kind == targetMain {
		return r.Main
	}
	return cur.MachineID == machine && cur.SessionID == session
}

// feishuInteractionDone: a card is over (answered anywhere, withdrawn, expired): its card in Feishu says so.
func (s core) feishuInteractionDone(ix proto.Interaction, how string) {
	s.feishuInteractionEnded(ix, how, false)
}

// feishuInteractionEnded is feishuInteractionDone; inChat: the card was taken back because the user wrote in the
// conversation, so their next words are no late answer to it.
func (s core) feishuInteractionEnded(ix proto.Interaction, how string, inChat bool) {
	var c botCard
	ok := false
	// A card posted before a restart is in the chat's state, which book reads first.
	s.book(context.Background(), func(b chatBook) {
		c, ok = b.Card(ix.ID)
		if ok && differentBotCardOccurrence(c.Ix, ix) {
			// The terminal event belongs to an earlier same-ID occurrence. Its old
			// message must not remove or close the replacement's ledger entry.
			ok = false
			return
		}
		b.KeepCard(ix.ID, nil)
		if ok && typedAnswerable(c) && !inChat {
			b.NoteEnded(c) // a question a plain message could have answered: an option typed for it late is no new message
		}
	})
	if !ok || c.MessageID == "" {
		return
	}
	if ix.Kind == "" { // the catch-up knows only what was kept of it
		ix = c.Ix
	} else if ix.TaskTitle == "" {
		ix.TaskTitle = c.Ix.TaskTitle
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		s.botCardDone(ctx, c.MessageID, ix, how)
	}()
}

// detachedCard is a Feishu card closed because its node's connection ended (the core's cancelMachineInteractions): the
// same card read again once the node is back opens it again (feishuCardBack). closed is shut once the close went out,
// so the reopening lands after it.
type detachedCard struct {
	card botCard
	// post is the card's first post when the node went away while it was in flight: card has no message ID then,
	// and the post's (once done is shut) is the one closed and reopened.
	post   *botCardPost
	at     time.Time
	closed chan struct{}
}

// botCardPost is a card's first post while it is in flight (postInteractionCard): a node can go away after Feishu
// took the post but before it returned the message ID (#15323).
type botCardPost struct {
	done     chan struct{}
	card     botCard // set before done is shut; no message ID when the post returned none
	detached bool    // under detachedCards.mu: feishuDetach took the card, and closes it once the post returns
}

// feishuDetach: the cards ids are about to end because their node went away. Their Feishu cards say "Ended" as any
// ended card's, and are kept for grace (the core's resumeGrace) in case the same card comes back (#4583).
func (s core) feishuDetach(ids []string, grace time.Duration) {
	dc := &s.t.detached
	dc.mu.Lock()
	var closing []detachedCard
	s.book(context.Background(), func(b chatBook) {
		now := b.Now()
		for id, d := range dc.cards {
			if now.Sub(d.at) > grace {
				delete(dc.cards, id)
			}
		}
		for _, id := range ids {
			c, ok := b.Card(id)
			var post *botCardPost
			if ok && c.MessageID == "" {
				post = dc.posting[id] // posted, its message ID not back yet: closed once it is
			}
			if !ok || (c.MessageID == "" && post == nil) {
				continue
			}
			if post != nil {
				post.detached = true
			}
			b.KeepCard(id, nil) // closed here, not again by feishuInteractionDone
			if dc.cards == nil {
				dc.cards = map[string]detachedCard{}
			}
			d := detachedCard{card: c, post: post, at: now, closed: make(chan struct{})}
			dc.cards[id] = d
			closing = append(closing, d)
		}
	})
	dc.mu.Unlock()
	for _, d := range closing {
		go func() {
			defer close(d.closed)
			card := d.card
			if d.post != nil {
				<-d.post.done
				card = d.post.card
			}
			if card.MessageID == "" { // the post returned no message: there is nothing to close
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			s.botCardDone(ctx, card.MessageID, card.Ix, i18n.T(s.botLang(ctx), "assistant-channels.ended"))
		}()
	}
}

// feishuCardBack: ix is a card back on the apps after its node reconnected (shown, not announced). Its Feishu card,
// closed when the node went away, asks it again, in the same message and without a new one. A card that did not come
// back (answered, moved on, away past grace) stays closed.
func (s core) feishuCardBack(ix proto.Interaction, grace time.Duration) {
	dc := &s.t.detached
	dc.mu.Lock()
	d, ok := dc.cards[ix.ID]
	if ok && d.post != nil {
		// Its first post was in flight when the node went away: wait for the post and its close, then reopen the
		// message that post made, and only for the card's own session and generation.
		dc.mu.Unlock()
		select {
		case <-d.closed:
		case <-time.After(30 * time.Second):
			return
		}
		current, pending := s.h.AskPending(ix.ID)
		dc.mu.Lock()
		if cur, still := dc.cards[ix.ID]; !still || cur.closed != d.closed { // another reconnect took or replaced it meanwhile
			dc.mu.Unlock()
			return
		}
		d.card = d.post.card
		if pending && d.card.MessageID != "" {
			owner := d.card.Ix
			if owner.MachineID != ix.MachineID || owner.SessionID != ix.SessionID ||
				current.MachineID != ix.MachineID || current.SessionID != ix.SessionID || !current.CreatedAt.Equal(ix.CreatedAt) {
				dc.mu.Unlock()
				return // a notice for another session or an older generation must not take this message
			}
		}
		ok = pending && d.card.MessageID != ""
	}
	delete(dc.cards, ix.ID)
	back := false
	s.book(context.Background(), func(b chatBook) {
		if _, posted := b.Card(ix.ID); !ok || posted || b.Now().Sub(d.at) > grace {
			return
		}
		b.KeepCard(ix.ID, &botCard{MessageID: d.card.MessageID, Thread: d.card.Thread, ChatID: d.card.ChatID, Ix: ix, At: b.Now(),
			NudgedAt: d.card.NudgedAt, SkippedAt: d.card.SkippedAt, Tries: d.card.Tries, TriedAt: d.card.TriedAt})
		back = true
	})
	dc.mu.Unlock()
	if !back {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	select {
	case <-d.closed:
	case <-ctx.Done():
		return
	}
	if err := s.botUpdateCard(ctx, d.card.MessageID, interactionCard(s.botLang(ctx), ix, s.h.AskView(ctx, ix))); err != nil {
		s.logf("bots: reopen card %s: %v", ix.ID, err)
		s.book(ctx, func(b chatBook) {
			if c, still := b.Card(ix.ID); still && c.MessageID == d.card.MessageID {
				b.KeepCard(ix.ID, nil)
			}
		})
		return
	}
	still := false
	s.book(ctx, func(b chatBook) { _, still = b.Card(ix.ID) })
	if _, pending := s.h.AskState(ix.ID); !still || !pending { // answered or ended meanwhile: it says so again
		s.book(ctx, func(b chatBook) { b.KeepCard(ix.ID, nil) })
		s.botCardDone(ctx, d.card.MessageID, ix, i18n.T(s.botLang(ctx), "assistant-feishu_cards.handled"))
	}
}

func (s core) botCardDone(ctx context.Context, messageID string, ix proto.Interaction, how string) {
	if err := s.botUpdateCard(ctx, messageID, s.answeredCard(ctx, ix, how)); err != nil {
		s.logf("bots: close card %s: %v", ix.ID, err)
		s.botNote(ctx, messageID, "✓ "+how)
	}
}

// closeEndedBotCards closes the bot's cards whose interaction is over. Not being in pending is not enough: right
// after a restart the manager's table is empty or partial (interactions are restored, screen and Codex cards rebuilt,
// after this runs), so each is closed only once the history says it ended for good (botCardLook). One that may still
// be waiting is looked at again a little later, once (again).
func (s core) closeEndedBotCards(ctx context.Context, pending map[string]proto.Interaction, again bool) {
	type candidate struct {
		card     botCard
		replaced bool
	}
	var maybe []candidate
	s.book(ctx, func(b chatBook) { // cards posted before a restart are in the chat's state
		for id, card := range b.Cards() {
			current, ok := pending[id]
			replaced := ok && !card.Ix.CreatedAt.IsZero() && !current.CreatedAt.IsZero() && card.Ix.CreatedAt.Before(current.CreatedAt)
			if (!ok || replaced) && card.MessageID != "" {
				maybe = append(maybe, candidate{card: card, replaced: replaced})
			}
		}
	})
	var unsure []botCard
	for _, candidate := range maybe {
		card := candidate.card
		if candidate.replaced {
			// A live same-ID replacement proves this older posted occurrence is over;
			// its history can be pending under the replacement's occurrence instead.
			s.feishuInteractionDone(card.Ix, i18n.T(s.botLang(ctx), "assistant-channels.ended"))
			continue
		}
		switch _, waiting, over := s.botCardLook(ctx, card.Ix.ID); {
		case over:
			s.feishuInteractionDone(card.Ix, i18n.T(s.botLang(ctx), "assistant-channels.ended"))
		case !waiting:
			unsure = append(unsure, card)
		}
	}
	if again && len(unsure) > 0 {
		time.AfterFunc(s.timing("card_recheck"), func() {
			cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			s.closeEndedBotCards(cctx, nil, false)
		})
	}
}

// detachedCards are the Feishu cards closed because their node's connection ended (feishuDetach), by interaction id,
// kept for the core's resumeGrace in case the same card comes back (feishuCardBack). The chat's lock is taken inside
// mu, never the other way round.
type detachedCards struct {
	mu    sync.Mutex
	cards map[string]detachedCard
	// posting are the cards' first posts in flight, by interaction id.
	posting map[string]*botCardPost
}

// book runs f on the chat's records under the chat's lock, the state read first.
func (s core) book(ctx context.Context, f func(chatBook)) {
	b := s.fbot()
	b.mu.Lock()
	defer b.mu.Unlock()
	s.loadBotLocked(ctx)
	f(chatBook{s})
}
