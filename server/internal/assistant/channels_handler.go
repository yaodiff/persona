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
func (s *Service) ChannelReceive(ctx context.Context, p ChannelReceive) error {
	var m channel.Message
	if err := json.Unmarshal(p.Message, &m); err != nil {
		return badRequest(err)
	}
	if m.ID == "" {
		return badRequest(errors.New("a message without an id"))
	}
	if m.Owner != nil && !*m.Owner {
		return badRequest(errors.New(i18n.T(s.botLang(ctx), "assistant-channels_plugin.mention_owner_required")))
	}
	admission, err := s.admitChannelMessage(ctx, p.Channel, m)
	if err != nil {
		return &rpc.Error{Code: CodeTryAgain, Message: err.Error()} // the hub hands it over again
	}
	if admission == nil {
		return nil // delivered again
	}
	go admission.run(s, m, p.Action)
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
	if announcedAt, ok := c.announcedOccurrence[p.Interaction.ID]; ok &&
		!announcedAt.IsZero() && !p.Interaction.CreatedAt.IsZero() && p.Interaction.CreatedAt.Before(announcedAt) {
		c.mu.Unlock()
		return // an older replay cannot replace or repost the current occurrence
	}
	if endedAt, ok := c.endedOccurrence[p.Interaction.ID]; ok {
		if !endedAt.IsZero() && !p.Interaction.CreatedAt.IsZero() && p.Interaction.CreatedAt.Before(endedAt) {
			c.mu.Unlock()
			return // the newer occurrence is already terminal; do not resurrect the older one
		}
		if !p.Interaction.CreatedAt.Equal(endedAt) {
			// A new occurrence must be deliverable and answerable even while the old
			// occurrence's terminal event is still inside the in-flight guard window.
			delete(c.ended, p.Interaction.ID)
			delete(c.endedOccurrence, p.Interaction.ID)
		}
	}
	if c.announced == nil {
		c.announced = map[string]bool{}
	}
	if c.announcedOccurrence == nil {
		c.announcedOccurrence = map[string]time.Time{}
	}
	c.announced[p.Interaction.ID] = true
	c.announcedOccurrence[p.Interaction.ID] = p.Interaction.CreatedAt
	c.mu.Unlock()
	go s.larkModule().AskAnnounced(p.Interaction)
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
	announcedAt, announced := c.announcedOccurrence[p.Interaction.ID]
	if !announced || announcedAt.IsZero() || p.Interaction.CreatedAt.IsZero() || announcedAt.Equal(p.Interaction.CreatedAt) {
		// A delayed terminal event for A must not remove B's announcement. Legacy
		// ID-only announcements retain their previous cleanup behavior.
		delete(c.announced, p.Interaction.ID)
		delete(c.announcedOccurrence, p.Interaction.ID)
	}
	now := time.Now()
	for id, at := range c.ended {
		if now.Sub(at) > 10*time.Minute {
			delete(c.ended, id)
			delete(c.endedOccurrence, id)
		}
	}
	if c.ended == nil {
		c.ended = map[string]time.Time{}
	}
	if c.endedOccurrence == nil {
		c.endedOccurrence = map[string]time.Time{}
	}
	if previous, ok := c.endedOccurrence[p.Interaction.ID]; !ok || previous.IsZero() || p.Interaction.CreatedAt.IsZero() || !p.Interaction.CreatedAt.Before(previous) {
		c.ended[p.Interaction.ID] = now
		c.endedOccurrence[p.Interaction.ID] = p.Interaction.CreatedAt
	}
	c.mu.Unlock()
	l := s.botLang(context.Background())
	how := i18n.T(l, "assistant-channels.ended")
	if p.Resolution != nil {
		how = resolutionText(l, p.Interaction, *p.Resolution)
	}
	// Taken back because the user wrote in the conversation: what they write next is not a late answer to it.
	inChat := p.Resolution != nil && p.Resolution.Dismiss && p.Resolution.Reason == proto.DismissedInChat && p.Resolution.AnsweredBy == ""
	s.larkModule().AskEnded(p.Interaction, how, inChat)
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

// interactionEndedFor checks the terminal marker and current announcement for
// one occurrence. A delayed A terminal event remains a guard for A's in-flight
// post without suppressing B; a current different announcement supersedes A.
func (s *Service) interactionEndedFor(ix proto.Interaction) bool {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	return s.interactionEndedForLocked(ix)
}

func (s *Service) interactionEndedForLocked(ix proto.Interaction) bool {
	c := &s.chans
	if endedAt, ok := c.endedOccurrence[ix.ID]; ok {
		if ix.CreatedAt.IsZero() || endedAt.IsZero() || ix.CreatedAt.Equal(endedAt) {
			return true
		}
	} else if _, ok := c.ended[ix.ID]; ok && ix.CreatedAt.IsZero() {
		return true
	}
	if announcedAt, ok := c.announcedOccurrence[ix.ID]; ok && !announcedAt.IsZero() && !ix.CreatedAt.IsZero() {
		return !announcedAt.Equal(ix.CreatedAt)
	}
	return false
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
	s.larkModule().ForgetStatus()
	if _, err := s.chat().Status(ctx); err != nil { // the pairing as it is now (feishuReconcile)
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
	pending := map[string]proto.Interaction{}
	c := &s.chans
	c.mu.Lock()
	first := !c.connected
	c.connected = true
	if c.announced == nil {
		c.announced = map[string]bool{}
	}
	if c.announcedOccurrence == nil {
		c.announcedOccurrence = map[string]time.Time{}
	}
	var missed []proto.Interaction
	for _, ix := range res.Interactions {
		announcedAt, hasOccurrence := c.announcedOccurrence[ix.ID]
		currentAt := announcedAt
		if !c.announced[ix.ID] {
			currentAt = time.Time{}
		}
		if endedAt, ok := c.endedOccurrence[ix.ID]; ok && endedAt.After(currentAt) {
			currentAt = endedAt
		}
		if !currentAt.IsZero() && !ix.CreatedAt.IsZero() && ix.CreatedAt.Before(currentAt) {
			// A delayed snapshot must not downgrade newer live state before the
			// announcement guard runs. Keep the newer occurrence in the pending
			// identity used by cleanup, and do not queue the older card as missed.
			ix.CreatedAt = currentAt
			pending[ix.ID] = ix
			continue
		}
		pending[ix.ID] = ix
		same := c.announced[ix.ID] && (!hasOccurrence || announcedAt.IsZero() || ix.CreatedAt.IsZero() || announcedAt.Equal(ix.CreatedAt))
		if !same {
			c.announced[ix.ID] = true
			c.announcedOccurrence[ix.ID] = ix.CreatedAt
			if !first {
				missed = append(missed, ix)
			}
		}
	}
	for id := range c.announced {
		if _, ok := pending[id]; !ok {
			delete(c.announced, id)
			delete(c.announcedOccurrence, id)
		}
	}
	var gone []*channel.Ask
	if c.stLoaded {
		for _, a := range c.st.Asks {
			current, ok := pending[a.ID]
			replaced := ok && !a.CreatedAt.IsZero() && !current.CreatedAt.IsZero() && a.CreatedAt.Before(current.CreatedAt)
			if !ok || replaced {
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
		s.channelInteractionDone(proto.Interaction{ID: a.ID, MachineID: a.MachineID, SessionID: a.SessionID, Kind: a.Kind, CreatedAt: a.CreatedAt})
	}
	s.larkModule().AsksCatchUp(ctx, pending)
}
