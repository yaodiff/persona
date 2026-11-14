package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// ---- what the hub hands the channels (Handler) ----

// ChannelUnbound synchronously clears pending state before the connector's lifecycle request reports success.
func (s *Service) ChannelUnbound(_ context.Context, p ChannelUnbound) error {
	if p.Channel == "" {
		return badRequest(errors.New("a channel without an id"))
	}
	return s.forgetChannelTurns(p.Channel)
}

// ChannelReceive is channels.receive: the owner's message from a plugin's channel, taken once (the chat apps deliver
// some again) and handled in the background.
func (s *Service) ChannelReceive(_ context.Context, p ChannelReceive) error {
	var m channel.Message
	if err := json.Unmarshal(p.Message, &m); err != nil {
		return badRequest(err)
	}
	if m.ID == "" {
		return badRequest(errors.New("a message without an id"))
	}
	first, err := s.claimChannelMessage(p.Channel, m.ID)
	if err != nil {
		return &rpc.Error{Code: CodeTryAgain, Message: err.Error()} // the hub hands it over again
	}
	if !first {
		return nil // delivered again
	}
	go s.channelReceive(p.Channel, m, p.Action)
	return nil
}

// ChannelNoted is channels.noted: a channel's message went into a conversation through the apps' routes.
func (s *Service) ChannelNoted(_ context.Context, p ChannelNoted) {
	s.noteChannelConversation(p.Channel, p.MachineID, p.SessionID)
	s.channelsChanged()
}

// InteractionAnnounced is interaction.announced: a new card goes to the Feishu bot (as a card) and to the plugin
// channels that ask in text, when they drive its conversation (or take every agent's).
func (s *Service) InteractionAnnounced(_ context.Context, p InteractionAnnounced) {
	c := &s.chans
	c.mu.Lock()
	if c.announced == nil {
		c.announced = map[string]bool{}
	}
	c.announced[p.Interaction.ID] = true
	c.mu.Unlock()
	go s.feishuInteraction(p.Interaction)
	go s.channelInteraction(p.Interaction)
	go s.taskAsks(p.Interaction)
	s.boardChanged()
}

// heldAnnounced notes card id as announced without telling the Feishu bot or the channels: an escalation push tiering
// holds back (ask_route.go ringAsks) is the Needs you list's, and the channels' catch-up must not post it after all.
// The board shows it at once.
func (s *Service) heldAnnounced(id string) {
	c := &s.chans
	c.mu.Lock()
	if c.announced == nil {
		c.announced = map[string]bool{}
	}
	c.announced[id] = true
	c.mu.Unlock()
	s.boardChanged()
}

// InteractionEnded is interaction.ended: a card is over: the Feishu card says how, the text ask stops waiting.
func (s *Service) InteractionEnded(_ context.Context, p InteractionEnded) {
	c := &s.chans
	c.mu.Lock()
	delete(c.announced, p.Interaction.ID)
	now := time.Now()
	for id, at := range c.ended {
		if now.Sub(at) > 10*time.Minute {
			delete(c.ended, id)
		}
	}
	if c.ended == nil {
		c.ended = map[string]time.Time{}
	}
	c.ended[p.Interaction.ID] = now
	c.mu.Unlock()
	l := s.botLang(context.Background())
	how := i18n.T(l, "assistant-channels.ended")
	if p.Resolution != nil {
		how = resolutionText(l, p.Interaction, *p.Resolution)
	}
	// Taken back because the user wrote in the conversation: what they write next is not a late answer to it.
	inChat := p.Resolution != nil && p.Resolution.Dismiss && p.Resolution.Reason == proto.DismissedInChat && p.Resolution.AnsweredBy == ""
	s.feishuInteractionEnded(p.Interaction, how, inChat)
	s.channelInteractionEnded(p.Interaction, inChat)
	s.boardChanged()
}

// channelsPushed: the conversation pushed what matters itself (notify_user): it goes to the channels with pushes on
// too, and each channel's sync mirror leaves the turn alone only after its own copy arrives. A failed push must
// not suppress that channel's turn-end mirror.
func (s *Service) channelsPushed(machine, session, title, body string) {
	go s.feishuPush(machine, session, title, body)
	go s.channelPush(machine, session, title, body)
}

// markPushed records a delivered notify_user copy for one channel and conversation. Its mirror leaves the turn
// alone for the next three minutes (pushedLately). Entries older than an hour are swept.
func (s *Service) markPushed(ch, machine, session string) {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.notified == nil {
		c.notified = map[[3]string]time.Time{}
	}
	now := c.clock()
	for k, at := range c.notified {
		if now.Sub(at) > time.Hour {
			delete(c.notified, k)
		}
	}
	c.notified[[3]string{ch, machine, session}] = now
}

// interactionEnded: the card ended (interaction.ended came) — maybe while it was being sent to a channel.
func (s *Service) interactionEnded(id string) bool {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.ended[id]
	return ok
}

// pushedLately: this channel received the conversation's push within the last few minutes.
func (s *Service) pushedLately(ch, machine, session string) bool {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clock().Sub(c.notified[[3]string{ch, machine, session}]) < 3*time.Minute
}

// channelsReconnected: a new connection to the hub (manager.hello): the conversations the runs follow are followed
// again (the hub forgot them), the runs left without a watcher are watched again.
func (s *Service) channelsReconnected(ctx context.Context) {
	c := &s.chans
	c.mu.Lock()
	var keys [][2]string
	for k, n := range c.follows {
		if n > 0 {
			keys = append(keys, k)
		}
	}
	c.mu.Unlock()
	for _, k := range keys {
		s.askFollow(ctx, k[0], k[1]) // a failure is asked again by the run's watcher
	}
	s.forgetChannelList()
	s.feishuForgetStatus()
	if _, err := s.feishuStatus(ctx); err != nil { // the pairing as it is now (feishuReconcile)
		s.logf("channels: the Feishu bot's status: %v", err)
	}
	go s.sweepChannelRuns()
	s.channelsCatchUp(ctx)
}

// channelsCatchUp takes what came and went while the assistant was not connected: a card that came meanwhile goes to
// the channels as a new one would, one that ended meanwhile is closed (its Feishu card says "Ended", its text ask
// stops waiting). The first connection of this process only notes what is pending: a card asked before it started may
// have gone out already.
func (s *Service) channelsCatchUp(ctx context.Context) {
	var res struct {
		Interactions []proto.Interaction `json:"interactions"`
	}
	if err := s.call(ctx, DeviceAll, DeviceInteractionsList, map[string]any{}, &res); err != nil {
		s.logf("channels: the cards waiting now: %v", err)
		return
	}
	pending := map[string]bool{}
	c := &s.chans
	c.mu.Lock()
	first := !c.connected
	c.connected = true
	if c.announced == nil {
		c.announced = map[string]bool{}
	}
	var missed []proto.Interaction
	for _, ix := range res.Interactions {
		pending[ix.ID] = true
		if !c.announced[ix.ID] {
			c.announced[ix.ID] = true
			if !first {
				missed = append(missed, ix)
			}
		}
	}
	for id := range c.announced {
		if !pending[id] {
			delete(c.announced, id)
		}
	}
	var gone []*channel.Ask
	if c.stLoaded {
		for _, a := range c.st.Asks {
			if !pending[a.ID] {
				gone = append(gone, a)
			}
		}
	}
	c.mu.Unlock()
	quiet := map[[2]string]bool{} // a quiet task's agent asks only in the main conversation, never in a channel (#1731)
	if len(missed) > 0 {
		if r := s.todoReport(ctx); r != nil {
			for _, q := range r.Quiet {
				quiet[[2]string{q.MachineID, q.SessionID}] = true
			}
		}
	}
	for _, ix := range missed {
		if !quiet[[2]string{ix.MachineID, ix.SessionID}] {
			s.InteractionAnnounced(ctx, InteractionAnnounced{Interaction: ix})
		}
	}
	for _, a := range gone {
		s.channelInteractionDone(proto.Interaction{ID: a.ID})
	}
	s.closeEndedBotCards(ctx, pending, true)
}

// closeEndedBotCards closes the bot's cards whose interaction is over. Not being in pending is not enough: right
// after a restart the manager's table is empty or partial (interactions are restored, screen and Codex cards rebuilt,
// after this runs), so each is closed only once the history says it ended for good (botCardOver). One that may still
// be waiting is looked at again a little later, once (again).
func (s *Service) closeEndedBotCards(ctx context.Context, pending map[string]bool, again bool) {
	b := &s.fbot
	b.mu.Lock()
	s.loadBotLocked(ctx) // cards posted before a restart are in the chat's state
	var maybe []botCard
	for id, card := range b.cards {
		if !pending[id] && card.MessageID != "" {
			maybe = append(maybe, card)
		}
	}
	b.mu.Unlock()
	var unsure []botCard
	for _, card := range maybe {
		switch s.botCardOver(ctx, card.Ix.ID) {
		case cardOver:
			s.feishuInteractionDone(card.Ix, i18n.T(s.botLang(ctx), "assistant-channels.ended"))
		case cardUnsure:
			unsure = append(unsure, card)
		}
	}
	if again && len(unsure) > 0 {
		time.AfterFunc(botCardRecheck, func() {
			cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			s.closeEndedBotCards(cctx, nil, false)
		})
	}
}
