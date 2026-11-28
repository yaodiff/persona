package assistant

import (
	"context"
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/connector"
)

// connectorMainTrack is the main track's name in a connector call's context, as PERSONA_SPACE says it.
const connectorMainTrack = "main"

const maxOriginDeviceIDLen = 128

type conversationOriginDevices struct {
	mu   sync.RWMutex
	byID map[string]string
}

func sanitizeOriginDeviceID(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" || len(s) > maxOriginDeviceIDLen {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 0x20 || c >= 0x7f {
			return ""
		}
	}
	return s
}

func (s *Service) setConversationOriginDevice(sid, deviceID string) {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return
	}
	clean := sanitizeOriginDeviceID(deviceID)
	s.originDevices.mu.Lock()
	defer s.originDevices.mu.Unlock()
	if clean == "" {
		delete(s.originDevices.byID, sid)
		return
	}
	if s.originDevices.byID == nil {
		s.originDevices.byID = map[string]string{}
	}
	s.originDevices.byID[sid] = clean
}

func (s *Service) clearConversationOriginDevice(sid string) {
	s.setConversationOriginDevice(sid, "")
}

func (s *Service) conversationOriginDevice(sid string) string {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return ""
	}
	s.originDevices.mu.RLock()
	id := s.originDevices.byID[sid]
	s.originDevices.mu.RUnlock()
	return id
}

// connectorCallContext is where a connector call from conversation sid comes from (#13246,
// docs/protocol/connector-protocol.md "Call context"): its track, the track's kind, the conversation's
// information categories as a whole (conversationUse), left out when not known (a track the manager cannot tell, a
// store that cannot be read, a turn waiting to be tagged): the connector takes that for sensitive, and the
// hub-authenticated paired device ID that originated the current user turn (if any).
func (s *Service) connectorCallContext(ctx context.Context, sid string) connector.CallContext {
	space, known := s.conversationSpace(ctx, sid)
	if !known {
		return connector.CallContext{}
	}
	out := connector.CallContext{
		Track:          connectorMainTrack,
		OriginDeviceID: s.conversationOriginDevice(sid),
	}
	if space != "" {
		out.Track = space
		if e, ok := s.trackEntryOf(ctx, space); ok {
			out.TrackKind = e.Kind
		}
	}
	if info := s.conversationUse(ctx, sid); info.Known {
		out.Categories = append([]string{}, info.Categories...)
	}
	return out
}
