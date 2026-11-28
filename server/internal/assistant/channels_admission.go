package assistant

import (
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/channel"
)

type channelAdmission struct {
	binding     threadBindingRef
	reservation threadBindingReservation
}

// admitChannelMessage claims the message, captures its binding generation and reserves its position atomically.
// No node or channel RPC runs under the book lock. Reserve before persisting the claim so a later command cannot
// overtake a durable receipt. A failed write rolls both changes back for redelivery.
func (s *Service) admitChannelMessage(ctx context.Context, id string, m channel.Message) (*channelAdmission, error) {
	v2 := s.integrationsV2On(ctx)
	var v channel.View
	if v2 {
		var err error
		v, _, err = s.pluginChannelNow(ctx, id)
		if err != nil {
			return nil, err
		}
	}
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	s.loadChannelStateLocked(ctx)
	if !c.stLoaded {
		return nil, errChannelsUnread
	}
	seen, hadSeen := c.st.Seen[id]
	if m.ID == "" || slices.Contains(seen, m.ID) {
		return nil, nil
	}
	binding, err := s.resolveThreadBindingLocked(ctx, id, m, v, v2)
	if err != nil {
		return nil, err
	}
	reservation := c.bindings.reserve(binding)
	next := append(seen, m.ID)
	if len(next) > channelSeenKeep {
		next = next[len(next)-channelSeenKeep:]
	}
	c.st.Seen[id] = next
	if err := s.saveChannelStateLocked(); err != nil {
		if hadSeen {
			c.st.Seen[id] = seen
		} else {
			delete(c.st.Seen, id)
		}
		c.bindings.rollback(reservation)
		return nil, err
	}
	return &channelAdmission{binding: binding, reservation: reservation}, nil
}

// run keeps one binding generation's ordinary route or thread ordered. A new binding never waits for an old generation's RPC.
func (a *channelAdmission) run(s *Service, m channel.Message, action string) {
	defer func() {
		c := &s.chans
		c.mu.Lock()
		c.bindings.release(a.reservation)
		c.mu.Unlock()
	}()
	a.reservation.wait()
	s.receiveChannelMessage(a.binding, m, action)
}
