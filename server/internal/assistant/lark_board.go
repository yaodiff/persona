package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
)

// The standing board, the pinned workbench and the companion bots are the lark module's (internal/modules/lark, #13404);
// the core tells them what changed through larkhost.Board, and keeps the state the rest of the core reads.

// boardChanged says something the board shows changed: a card opened or ended, a to-do entered or left "Please verify",
// the assistant connected. The module edits the board a moment later, once for all that came meanwhile.
func (s *Service) boardChanged() { s.larkModule().BoardChanged() }

// fleetReady: the pending cards can be read. Without devices there is no fleet and nothing to wait for.
func (s *Service) fleetReady() bool { return !s.fleetOn() || s.fleetUp.Load() }

// boardFleetUp is FleetUp having read the pending cards: the board is brought up to date once.
func (s *Service) boardFleetUp() {
	s.fleetUp.Store(true)
	s.boardChanged()
}

// workbenchStamp adds a way back to the workbench to a card the bot posts or redraws, with the switch on.
func (s *Service) workbenchStamp(ctx context.Context, c *botcard.Card) *botcard.Card {
	return s.larkModule().WorkbenchStamp(ctx, c)
}

// divertNotices hands cards to the notifications companion when it takes them; false: they go to the bot's chat.
func (s *Service) divertNotices(ctx context.Context, kind assistantwire.NoticeKind, cards []*botcard.Card, fallbacks []string) bool {
	return s.larkModule().DivertNotices(ctx, kind, cards, fallbacks)
}
