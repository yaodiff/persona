package assistant

import (
	"context"
	"crypto/rand"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// newStartSessionID is the id the hub asks a node to start its next Claude session under, so a start whose answer
// never comes back can be reconciled by this exact id instead of a launcher/cwd guess. It is a version-4 UUID, the
// shape Claude Code's --session-id takes (the node generates the same shape itself, node newUUID).
func newStartSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not a recoverable runtime condition; the start cannot carry a trustworthy id.
		panic(fmt.Sprintf("assistant: cannot make a start session id: %v", err))
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// openTodoSessionIDs is the sessions on machine linked to a still-open todo. An adoption must never take one: it
// already belongs to another task, however fresh and same-directory it looks. The bool is false when the todo book
// could not be read, in which case callers fail closed and skip the heuristic for this attempt.
func (s *Service) openTodoSessionIDs(ctx context.Context, machine string) (map[string]bool, bool) {
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return nil, false
	}
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	out := make(map[string]bool)
	for key := range s.todos.activeBySession {
		if key[0] == machine {
			out[key[1]] = true
		}
	}
	return out, true
}

// adoptStartedSession finds the session an unanswered start created.
//
// First choice is expectID, the session id the hub itself put on this session.start: only the session that start
// created can carry this random id, so it is an exact match with no other condition — a session the owner started by
// hand and two workspace-none tasks in one directory can never be mistaken for each other. A node too old to honor
// session_id simply never lists it, and the call falls back to the heuristic below.
//
// The heuristic takes a session not in the pre-start snapshot (knownIDs), hub-started, a non-assistant agent of the
// same launcher and directory, with a fresh sign of life, that no open todo has linked yet; newest wins. It lists
// twice, since the hub's held sessions can lag the node by a changed event. nil knownIDs (the pre-start list failed)
// disables only this heuristic: the exact expectID above still needs no snapshot and must keep running (#7772).
//
// Limits of the fallback (only reachable on a node too old to honor session_id): two unanswered starts in the same
// directory and launcher at the same instant cannot be told apart, so one could adopt the other's session (a
// mislabeled card, never a duplicate agent); newest-wins is the best guess and the neutral fallback stays safe
// otherwise. A session the owner started by hand is indistinguishable from a hub-started one once it is on no open
// todo (both carry StartedByPersona), so such a session can still be adopted — the exact-id path on an up-to-date
// node is what closes that risk. A session already on an open todo is never taken, so a task can never steal another
// task's agent; and an unreadable todo book fails closed instead of guessing.
func (s *Service) adoptStartedSession(ctx context.Context, m Device, launcher, cwd, expectID string, knownIDs map[string]bool) (proto.Session, bool) {
	if knownIDs == nil && expectID == "" {
		return proto.Session{}, false
	}
	// The open-todo exclusion is read once and shared by both list attempts: the reconciliation is one decision
	// about one start, so it must see one consistent set of linked sessions. Failing to read the book fails closed:
	// no heuristic adoption without knowing what the open todos already own.
	var linked map[string]bool
	if expectID == "" {
		var ok bool
		linked, ok = s.openTodoSessionIDs(ctx, m.ID)
		if !ok {
			return proto.Session{}, false
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return proto.Session{}, false
			case <-time.After(agentStartAdoptRetry):
			}
		}
		lctx, cancel := context.WithTimeout(ctx, agentStartListTimeout)
		list, err := s.sessions(lctx, m.ID)
		cancel()
		if err != nil {
			return proto.Session{}, false
		}
		if expectID != "" {
			for _, sess := range list {
				if sess.SessionID == expectID {
					return sess, true
				}
			}
			if knownIDs == nil {
				// Without the pre-start snapshot there is no safe heuristic; the next attempt remains exact-only.
				continue
			}
			// The node did not list the expected id (an older node ignores session_id, or its list still lags): load
			// the open-todo book now, lazily, so a new-node exact match never pays for it, then keep the old
			// heuristic without taking a session another open todo already links.
			if linked == nil {
				var ok bool
				linked, ok = s.openTodoSessionIDs(ctx, m.ID)
				if !ok {
					return proto.Session{}, false
				}
			}
		}
		var best proto.Session
		found := false
		for _, sess := range list {
			if knownIDs[sess.SessionID] || !sess.StartedByPersona || sess.Assistant || sess.Launcher != launcher ||
				(sess.Cwd != cwd && sess.WorkspaceCwd != cwd) || sess.UpdatedAt.IsZero() || linked[sess.SessionID] {
				continue
			}
			if time.Since(sess.UpdatedAt) > agentStartAdoptWindow || found && !sess.UpdatedAt.After(best.UpdatedAt) {
				continue
			}
			best, found = sess, true
		}
		if found {
			return best, true
		}
	}
	return proto.Session{}, false
}
