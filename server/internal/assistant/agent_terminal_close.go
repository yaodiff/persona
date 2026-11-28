package assistant

import (
	"context"

	"github.com/nwverse/persona/server/internal/proto"
)

// Closing an Agent's terminal when its session ends (#10355): the owner's setting assistant.close_ended_terminals,
// off by default. Off, nothing here runs and every Agent keeps its terminal after it stops (#4754). On, the manager
// closes the tab Persona opened for an Agent once its session has ended, through the node's existing close, the
// "Archive and close" one (session.retire with close_terminal, node retire.go); nothing is archived and the
// transcript stays, so a message or the owner's Resume brings it back in a new tab.
//
// What counts as an end, from the session's states:
//   - its task is done or cancelled (tasks_close.go): after the Agent and owner inactivity grace, the guarded
//     request also closes its idle terminal; working Agents and active viewers are left alone;
//   - it quit: told to (/exit, /quit through Persona: stop cause exit) or by itself (its process exited: no cause),
//     seen by this pass as running in a pane and then stopped. The node closes the marked, idle shell it left
//     (after_exit, close_after_exit.go; Herdr and Orca).
//
// Not an end, so the terminal stays: the user's Stop (stop cause user), a stop for being idle (idle), a restart that
// cut it off (restart, interrupted). Never closed: a busy Agent (agentCloseBusy: working, waiting for an answer,
// background work, running subagents or workflows, a /loop, queued messages, a server it runs), assistant
// conversations, other tools' sessions, Agents Persona did not start, and panes Persona did not open for the session
// (the node checks its mark). The node checks again under the session's locks before it closes anything.

// endedSeen: (machine, session) → seen running in a pane by closeEndedAgentTerminals. In memory: after a manager
// restart an Agent has to be seen running again before its end closes anything, so a terminal is closed later or
// not at all, never by mistake.
type endedSeen map[[2]string]bool

// closeEndedTerminals: assistant.close_ended_terminals is on (off by default).
func (s *Service) closeEndedTerminals(ctx context.Context) bool {
	v := s.settings(ctx).CloseEndedTerminals
	return v != nil && *v
}

// closeEndedTerminal asks machine's node to close session's terminal: the running, idle Agent and its tab, or with
// it already gone the shell it left (after_exit). cause is what the close records (an automatic cause keeps the
// supervisor's resume, node retire.go); empty for an Agent that already quit, whose own cause stays. closed false
// with no error: it was not running and left no shell the node could close.
func (s *Service) closeEndedTerminal(ctx context.Context, machine, session string, cause proto.StopCause) (bool, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentArchiveCloseTimeout)
	defer cancel()
	params := map[string]any{"session_id": session, "close_terminal": true, "after_exit": true}
	if cause != proto.StopUnknown {
		params["stop_cause"] = cause
	}
	var out struct {
		Closed bool `json:"closed"`
	}
	if err := s.call(ctx, machine, deviceSessionRetire, params, &out); err != nil {
		return false, err
	}
	return out.Closed, nil
}

// endedOnItsOwn: c, an Agent Persona started that was running, stopped because it quit (told to or by itself), not
// because the user stopped it, it was idle or a restart cut it off; and nothing in it is still going on.
func endedOnItsOwn(c proto.Session) bool {
	switch {
	case c.Assistant, c.SourceKind != "", !c.StartedByPersona, c.Starting:
		return false
	case c.Status != proto.StatusOffline || c.PaneID != "" || c.Capabilities == nil:
		return false
	case c.PendingInteractions > 0 || agentCloseBusy(c):
		return false
	}
	switch c.Agent {
	case "", "claude", "codex", "traex":
	default:
		return false
	}
	switch c.Capabilities.StopCause {
	case proto.StopUnknown, proto.StopExit:
		return true
	}
	return false
}

// closeEndedAgentTerminals closes, with the setting on, the terminals Agents left when they quit (endedOnItsOwn):
// each once, after seen saw it running in a pane. Off, it forgets what it saw and asks nothing.
func (s *Service) closeEndedAgentTerminals(ctx context.Context, seen endedSeen) {
	if !s.closeEndedTerminals(ctx) {
		clear(seen)
		return
	}
	listed := map[[2]string]bool{}
	for _, d := range s.devices(ctx) {
		var list []proto.Session
		var err error
		if d.Reachable {
			list, err = s.listedSessions(ctx, d.ID)
		}
		if !d.Reachable || err != nil {
			for k := range seen { // an unreachable machine keeps what was seen
				if k[0] == d.ID {
					listed[k] = true
				}
			}
			continue
		}
		for _, c := range list {
			key := [2]string{d.ID, c.SessionID}
			if c.PaneID != "" && c.Status != proto.StatusOffline && c.Status != proto.StatusInterrupted {
				if c.StartedByPersona && !c.Assistant && c.SourceKind == "" {
					seen[key], listed[key] = true, true
				}
				continue
			}
			if !seen[key] {
				continue
			}
			delete(seen, key) // one end, one attempt
			if !endedOnItsOwn(c) {
				continue
			}
			if c.Capabilities.StopCause == proto.StopUnknown {
				if err := s.ensureTodosLoaded(ctx); err != nil {
					continue
				}
				if _, active := s.openTodoOfAgent(d.ID, c.SessionID); active {
					s.logf("ended agent %s/%s: kept its terminal for the open task (unknown stop cause)", d.ID, short(c.SessionID))
					continue
				}
			}
			closed, err := s.closeEndedTerminal(ctx, d.ID, c.SessionID, proto.StopUnknown)
			switch {
			case err != nil:
				s.logf("ended agent %s/%s: its terminal stays: %v", d.ID, short(c.SessionID), err)
			case closed:
				s.logf("ended agent %s/%s: closed its terminal (close_ended_terminals)", d.ID, short(c.SessionID))
			}
		}
	}
	for k := range seen {
		if !listed[k] {
			delete(seen, k)
		}
	}
}
