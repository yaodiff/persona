package devices

import (
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type sessionSnapshot struct {
	peer     *rpc.Peer
	revision uint64
}

func (m *Machine) sessionsRevision() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessionRevision
}

// The reconnect read can finish after a newer sessions.changed notification.
// Only install its snapshot if neither the connection nor the list advanced.
func (m *Machine) setInitialSessions(peer *rpc.Peer, revision uint64, list []proto.Session) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.peer != peer || m.sessionRevision != revision {
		return false
	}
	m.takeRows(list)
	m.sessions = list
	m.sessionsListed = true
	m.lastSeen = time.Now().UTC()
	return true
}

// acceptSessionsList installs and optionally forwards a response only while its attempt still owns the current list.
// It always returns the current accepted list so a caller can make its own decision without using rejected rows.
// sessionsEventMu makes accepting the response, reading the current list, and forwarding it one ordering unit.
func (m *Machine) acceptSessionsList(snapshot *sessionSnapshot, list []proto.Session, forward bool) ([]proto.Session, bool) {
	m.sessionsEventMu.Lock()
	defer m.sessionsEventMu.Unlock()
	if !m.setInitialSessions(snapshot.peer, snapshot.revision, list) {
		return m.sessionList(), false
	}
	m.d.heldWake(m.cfg.ID)      // a message held for a session listed now goes (held.go)
	m.d.heldStartWake(m.cfg.ID) // held new-Agent starts for this computer go now (held_starts.go)
	sessions := m.sessionList()
	if forward {
		m.d.forward(m.cfg.ID, "sessions.changed", map[string]any{"sessions": sessions})
	}
	return sessions, true
}
