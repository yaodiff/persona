package assistant

import (
	"cmp"
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// agentTabNameStart binds a start's naming input to the session and exact placeholder the node answered with. The
// requested name is only a fallback for an incomplete answer: the node's expected-name recheck remains the authority.
func agentTabNameStart(machine string, c proto.Session, requested, nameFrom string) assistantwire.AgentTabNameStart {
	return assistantwire.AgentTabNameStart{
		MachineID: assistantwire.AgentMachineID(machine), SessionID: assistantwire.AgentSessionID(c.SessionID),
		TabID:           assistantwire.AgentTabID(cmp.Or(c.SlotID, c.TabID)),
		ExpectedTabName: cmp.Or(c.RenamableTabName(), requested), NameFrom: nameFrom,
	}
}

// Budgets for the detached start-naming job (#15674). Unlike the synchronous agents.tab_name and
// autoname callers, this goroutine blocks no start, so its model budget can cover a cold CLI
// launcher plus the first reasoning turn, which regularly passes tabName's eight-second cap. Both
// stay finite so a stuck model cannot leak the goroutine; tests compress these vars.
var (
	// agentTitleAsyncTimeout bounds only the agent.title model call.
	agentTitleAsyncTimeout = 30 * time.Second
	// agentTabLaterTimeout bounds the whole job: the model call, delayed tab identity resolution and
	// the guarded rename, leaving headroom after a model answer that spends the model budget.
	agentTabLaterTimeout = 45 * time.Second
)

// nameAgentTabLater finishes a start's model naming off its caller's path. A start with no usable session/tab identity
// is left to the ordinary background naming pass rather than guessing which session or user-chosen label to replace.
func (s *Service) nameAgentTabLater(p assistantwire.AgentTabNameStart) {
	if string(p.SessionID) == "" || p.ExpectedTabName == "" || strings.TrimSpace(p.NameFrom) == "" {
		return
	}
	b := &s.agentNames
	b.mu.Lock()
	if s.runCtx().Err() != nil {
		b.mu.Unlock()
		return
	}
	b.runs.Add(1)
	b.mu.Unlock()
	go func() {
		defer b.runs.Done()
		ctx, cancel := context.WithTimeout(s.runCtx(), agentTabLaterTimeout)
		defer cancel()
		name := s.tabNameWithTimeout(ctx, p.NameFrom, agentTitleAsyncTimeout)
		if name == "" || name == p.ExpectedTabName {
			return
		}
		p, ok := s.startedTabIdentity(ctx, p)
		if !ok {
			return
		}
		// The node compares both identities again immediately before renaming, so a manual rename while the model was
		// running always wins. Never replace this with an unguarded session.rename.
		if err := s.call(ctx, string(p.MachineID), assistantwire.DeviceSessionRenameTabIfCurrent, map[string]any{
			"session_id": string(p.SessionID), "tab_id": string(p.TabID), "expected_tab_name": p.ExpectedTabName, "tab_name": name,
		}, nil); err != nil {
			if ctx.Err() == nil {
				s.logf("tab name %s: %v", string(p.SessionID), err)
			}
			return
		}
		// nameStartedAgent records the local placeholder for display. Advance it only after the guarded rename lands.
		if err := s.putInitialAgentTitle(ctx, string(p.MachineID), string(p.SessionID), name); err != nil {
			s.logf("tab name %s: save session title: %v", string(p.SessionID), err)
			return
		}
		s.publishSessions(string(p.MachineID))
	}()
}

// startedTabIdentity waits off the start path when an async node's first answer had no tab yet. A different observed
// label ends the attempt rather than guessing that it was a host suffix: it may be the owner's rename.
func (s *Service) startedTabIdentity(ctx context.Context, p assistantwire.AgentTabNameStart) (assistantwire.AgentTabNameStart, bool) {
	if p.TabID != "" {
		return p, true
	}
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		current, found := s.rawAgentForNaming(ctx, string(p.MachineID), string(p.SessionID))
		if found {
			label := current.RenamableTabName()
			if label != "" && label != p.ExpectedTabName {
				return p, false
			}
			if id := cmp.Or(current.SlotID, current.TabID); label == p.ExpectedTabName && id != "" {
				p.TabID = assistantwire.AgentTabID(id)
				return p, true
			}
		}
		select {
		case <-ctx.Done():
			return p, false
		case <-tick.C:
		}
	}
}
