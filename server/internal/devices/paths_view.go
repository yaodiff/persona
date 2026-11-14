package devices

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// pathInfos is a machine's paths for the admin page and the machine list.
func (d *Devices) pathInfos(m *Machine) []pathInfo {
	active := m.activePathKey()
	self := d.config().HubID
	var out []pathInfo
	for _, p := range d.pathsOf(m) {
		st := d.pathState(p)
		pi := pathInfo{Via: p.via, HubID: self, OK: st.avail && time.Now().After(st.failUntil), RTTMS: st.rtt,
			Active: p.key == active, Error: st.err}
		if p.via == viaHub {
			pi.HubID, pi.ID = p.hub, p.mid
		} else {
			pi.ID = p.src.cfg.ID
			if pi.Active {
				if t := m.transportNow(); t != "" {
					pi.Via = t // ssh that fell back to tailcat
				}
			}
		}
		if pi.Active {
			pi.OK, pi.Error = true, ""
		}
		out = append(out, pi)
	}
	return out
}

// measureNodes measures the round trip to every computer this hub drives, over the path in use. A connection that
// leaves pingMissLimit pings in a row unanswered is closed so the machine reconnects (#7510): a half-open link (an
// ssh proxy that still answers keepalives while the node behind it is gone) otherwise holds every call sent on it
// until its own wait runs out, a task's start for minutes.
func (d *Devices) measureNodes(ctx context.Context) {
	for _, m := range d.machinesNow() {
		p := m.activePath()
		if p == nil || !m.isOnline() {
			continue
		}
		m.mu.Lock()
		peer := m.peer
		m.mu.Unlock()
		cctx, cancel := context.WithTimeout(ctx, pingWait)
		start := time.Now()
		err := m.call(cctx, "ping", map[string]any{}, nil)
		cancel()
		if err == nil {
			d.pathRTT(p, max(1, int(time.Since(start).Milliseconds())))
		}
		if ctx.Err() == nil {
			m.pingAnswered(peer, err)
		}
	}
}

// pingWait bounds one ping of measureNodes; pingMissLimit is how many in a row may go unanswered before the
// connection is closed. Vars so tests shorten them.
var (
	pingWait      = 5 * time.Second
	pingMissLimit = 2
)

// pingAnswered counts a ping sent on peer: one written that ran out of time without an answer is a miss, anything
// else (an answer, an error the node answered with) clears the count; a connection already gone (closed, offline), a
// ping that could not be written in time, and one during a resident node's handover count neither way. pingMissLimit misses in a row on the same connection close it.
func (m *Machine) pingAnswered(peer *rpc.Peer, err error) {
	if peer == nil {
		return
	}
	var re *rpc.Error
	gone := errors.Is(err, rpc.ErrClosed) || errors.Is(err, rpc.ErrSuspended) || errors.Is(err, errOffline) || errors.Is(err, context.Canceled)
	missed := errors.Is(err, context.DeadlineExceeded) || errors.As(err, &re) && re.Code == "" && re.Message == context.DeadlineExceeded.Error()
	// Not a sign of a dead link: a ping that never got on the wire (a long write ahead of it on a slow but working
	// link), or one made while a resident node hands over to its next process (calls wait for the new connection).
	if gone && !missed || errors.Is(err, rpc.ErrNotWritten) || m.expectedReload() {
		return
	}
	m.mu.Lock()
	if m.pingPeer != peer {
		m.pingPeer, m.pingMisses = peer, 0
	}
	if missed {
		m.pingMisses++
	} else {
		m.pingMisses = 0
	}
	misses, current := m.pingMisses, m.peer == peer
	if misses >= pingMissLimit {
		m.pingMisses = 0
	}
	m.mu.Unlock()
	if misses < pingMissLimit || !current {
		return
	}
	m.d.logf("[%s] no answer to %d pings in a row over %s: closing the connection to reconnect", m.cfg.ID, misses, m.transportNow())
	peer.Close(fmt.Errorf("no answer to %d pings in a row (a half-open link?)", misses))
	m.wake()
}

// decorate fills in what the apps see of how a machine is reached: the hub it is reached through, its own id, the
// route, its paths.
func (d *Devices) decorate(m *Machine, s proto.Machine) proto.Machine {
	cfg := d.config()
	s.HubID, s.HubName = cfg.HubID, cfg.HubName
	f := &d.fleet
	f.mu.Lock()
	for _, p := range f.paths[m] {
		if uid := f.uids[p.key]; uid != "" {
			s.MachineUID = uid
			break
		}
	}
	f.mu.Unlock()
	if uid := m.nodeUID(); uid != "" {
		s.MachineUID = uid
	}
	infos := d.pathInfos(m)
	route := &proto.MachineRoute{Via: kindOf(m.cfg)}
	if m.derived {
		route.Via = viaHub
	}
	for i, pi := range infos {
		if pi.Active || i == 0 && !s.Online {
			route = &proto.MachineRoute{Via: pi.Via}
			if pi.Via == viaHub {
				route.Hops = 1
				s.HubID, s.HubName = pi.HubID, d.hubNameOf(pi.HubID)
			}
			if s.Online && pi.RTTMS > 0 {
				rtt := pi.RTTMS
				route.RTTMS = &rtt
			}
			if pi.Active {
				break
			}
		}
	}
	s.Route = route
	if len(infos) > 1 || m.derived {
		for _, pi := range infos {
			s.Paths = append(s.Paths, proto.MachinePath{Via: pi.Via, HubID: pi.HubID, OK: pi.OK, RTTMS: pi.RTTMS, Active: pi.Active})
		}
	}
	return s
}

// measureLoop measures the round trip to every computer now and then (the choice between paths uses it).
func (d *Devices) measureLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		d.measureNodes(ctx)
	}
}
