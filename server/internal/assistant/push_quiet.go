package assistant

import (
	"context"
	"sync"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// The hub's low-distraction mode, as the assistant honours it (docs/server/server.md "Low-distraction
// mode"). The hub filters the phone pushes by level; what never was a hub push is the assistant's to hold back: with
// the switch on, an agent's card does not climb to the owner on its own (ask_route.go askTick), an escalated card
// pushes only when what it asks is the owner's alone (escalateLevel), and conclusions, "Please verify" asks and leader
// progress reach the owner through the briefing and the board instead of a card (quietHoldsAsk, leaderReportLevel).
// With the switch off, everything is as before.

// quietSwitch overrides the hub's answer in tests.
type quietSwitch struct {
	mu sync.Mutex
	// force, when set, answers instead of the hub's hello (tests).
	force *bool
}

// quietModeOn says whether the hub's push tiering switch is on, as its hello last said (Hello.QuietPush; the hub sends
// manager.updated when the switch changes). The manager never reads the hub's settings itself (db/settings.go: each
// side has its own), and an older hub that does not send it is off.
func (s *Service) quietModeOn(ctx context.Context) bool {
	b := &s.pushQuiet
	b.mu.Lock()
	force := b.force
	b.mu.Unlock()
	if force != nil {
		return *force
	}
	return s.hello(ctx).QuietPush
}

// quietHoldsAsk: low-distraction mode keeps ask a off the Feishu bot: a conclusion (task_review accepted, cannot) or a "Please verify"
// is listed by the briefing, the board and the web app instead (NoCard), as one held for a briefing is. A decision is
// not: what the owner must decide still goes out (its own push is P0, todo_ask.go).
func (s *Service) quietHoldsAsk(ctx context.Context, a *acceptAsk) bool {
	return a != nil && !decideAsk(a) && s.quietModeOn(ctx)
}

// askKind names ask a in a log line.
func askKind(a *acceptAsk) string {
	if a.Conclusion {
		return "conclusion"
	}
	return "acceptance"
}

// leaderReportLevel is the push level a leader report carries to the extensions in low-distraction mode: progress and a goal
// reached are P1 (the briefing's and the work log's, no alert); a decision P0; stuck P1. "" with low-distraction mode off: the
// extensions do as before.
func (s *Service) leaderReportLevel(ctx context.Context, kind string) string {
	if !s.quietModeOn(ctx) {
		return ""
	}
	if kind == assistantwire.LeaderDecision {
		return assistantwire.LevelP0
	}
	return assistantwire.LevelP1
}
