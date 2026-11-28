package assistant

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// deviceSessionSeen tells a node the user saw one of its sessions (node runtimes_calls.go): a runtime keeping an unread
// mark of its own clears it there too (Paseo, #6299).
const deviceSessionSeen = "session.seen"

// seenCallTimeout bounds telling the node (the runtime's own answer is bounded at 10 s).
var seenCallTimeout = 15 * time.Second

// ---- seen ----

// loadSeen reads the seen times from the store, merging the ones marked meanwhile, unless that was done already. It
// answers whether this call read them; a failed read is tried again by the next call.
func (s *Service) loadSeen(ctx context.Context) bool {
	s.fleet.mu.Lock()
	loaded := s.fleet.seenLoaded
	s.fleet.mu.Unlock()
	if loaded {
		return false
	}
	st := s.fleetStore()
	if st == nil {
		return false
	}
	m, err := st.SeenTimes(ctx)
	if err != nil {
		if errors.Is(err, errNoTable) { // a store that does not keep them: nothing to read again
			s.fleet.mu.Lock()
			s.fleet.seenLoaded = true
			s.fleet.mu.Unlock()
		} else {
			s.logf("load seen times: %v", err)
		}
		return false
	}
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	if s.fleet.seenLoaded { // another call read them meanwhile
		return false
	}
	for k, v := range s.fleet.seen { // one marked meanwhile is newer
		m[k] = v
	}
	s.fleet.seen = m
	s.fleet.seenLoaded = true
	return true
}

// retrySeen reads the seen times again if they could not be read so far, and refreshes the session lists' states
// once they are.
func (s *Service) retrySeen(ctx context.Context) {
	if !s.loadSeen(ctx) {
		return
	}
	for _, m := range s.fleetMachines() {
		s.publishSessions(m.ID)
	}
}

func (s *Service) seenAt(machine, session string) time.Time {
	s.fleet.mu.Lock()
	defer s.fleet.mu.Unlock()
	return s.fleet.seen[[2]string{machine, session}]
}

// recordSessionSeen updates the read receipt without inferring owner activity.
func (s *Service) recordSessionSeen(ctx context.Context, machine, session string) time.Time {
	now := time.Now().UTC()
	s.fleet.mu.Lock()
	if s.fleet.seen == nil {
		s.fleet.seen = map[[2]string]time.Time{}
	}
	s.fleet.seen[[2]string{machine, session}] = now
	s.fleet.mu.Unlock()
	if st := s.fleetStore(); st != nil {
		if err := st.MarkSeen(context.WithoutCancel(ctx), machine, session, now); err != nil {
			s.logf("mark seen: %v", err)
		}
	}
	return now
}

// markSeen handles read receipts explicitly reported by the owner or their client.
func (s *Service) markSeen(ctx context.Context, machine, session string) {
	s.rememberTaskActivity(ctx, machine, session, taskReadActivityPrefix+runtimeListID(machine, session))
	s.markSessionRead(ctx, machine, session)
}

func (s *Service) markSessionRead(ctx context.Context, machine, session string) {
	now := s.recordSessionSeen(ctx, machine, session)
	s.SessionSeen(ctx, SessionSeen{MachineID: machine, SessionID: session, At: now}) // how long a triaged turn waited
	s.publishSessions(machine)
	s.seenToRuntime(machine, session)
	s.markSessionOwnerCardsSeen(ctx, machine, session)
}

// seenToRuntime tells the node of a runtime's session the user saw, while its node lists it as unread (done), #6299: a
// runtime that keeps an unread mark of its own (Paseo's requiresAttention) clears it, so the session is not unread in
// the runtime's own app as well. In the background, best effort; a node too old for it says nothing is known of it.
func (s *Service) seenToRuntime(machine, session string) {
	row, ok := s.nodeSession(machine, session)
	if !ok || row.SourceKind != proto.SourceRuntime || row.Status != proto.StatusDone {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), seenCallTimeout)
		defer cancel()
		err := s.nodeCall(ctx, machine, deviceSessionSeen, map[string]any{"session_id": session}, nil)
		var re *rpc.Error
		if err != nil && !(errors.As(err, &re) && strings.HasPrefix(re.Message, "unknown method")) {
			s.logf("seen %s on %s: %v", session, machine, err)
		}
	}()
}
