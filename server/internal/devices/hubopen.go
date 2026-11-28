package devices

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/streams"
)

// What the hub opens on a node through the manager — a stream (a forwarded port's: port.dial, a plugin program's:
// plugin.node.dial, a preview's browser tab: preview.open), a terminal it watches (terminal.watch) — is the hub's as
// long as the bus connection it came on lasts: the hub ends its side when a new connection says hello
// (hub/devices_reports.go devicesConnected), and a hub that restarted has none of it. The manager ends the node's side
// then (Hello), as the node itself would when its own connection to the hub ended; else a terminal would go on
// streaming, and a port's connection or a preview's browser tab stay open on the computer with nobody at the other end.

// hubOpened is what the hub has open on the machine's node now; m.mu guards it.
type hubOpened struct {
	streams map[string]bool // stream id -> offered directional EOF
	terms   map[string]bool // watched terminals by session id
}

type portDialRelay func(context.Context, string, any) error

func (relay portDialRelay) Call(ctx context.Context, method string, params, _ any) error {
	return relay(ctx, method, params)
}

// callPortDial records the stream before the request goes out. The node can send a close before its reply, and a
// new bus connection must also clean up a dial whose reply was lost or is still pending.
func (m *Machine) callPortDial(ctx context.Context, peer *rpc.Peer, dial devicewire.PortDial, out any) error {
	halfClose := dial.StreamVersion == devicewire.PortStreamHalfCloseV1
	m.mu.Lock()
	if err := rpc.ConnContext(ctx).Err(); err != nil {
		m.mu.Unlock()
		return rpc.NotSent(err)
	}
	if m.peer != peer {
		m.mu.Unlock()
		return rpc.NotSent(errOffline)
	}
	if m.opened.streams == nil {
		m.opened.streams = map[string]bool{}
	}
	m.opened.streams[dial.ID] = halfClose
	m.mu.Unlock()

	// The manager relays the node's result to the original caller, including
	// fields only newer peers know about.
	relay := portDialRelay(func(ctx context.Context, method string, params any) error {
		return peer.Call(ctx, method, params, out)
	})
	_, err := buswire.Call(ctx, relay, devicewire.PortDialMethod, dial)
	if err == nil {
		return nil
	}
	m.mu.Lock()
	_, tracked := m.opened.streams[dial.ID]
	delete(m.opened.streams, dial.ID)
	m.mu.Unlock()
	var refusal *rpc.Error
	if tracked && !errors.Is(err, rpc.ErrNotSent) && !errors.As(err, &refusal) {
		m.closePort(dial.ID, halfClose)
	}
	return err
}

// noteOpened keeps what a call of the hub's opened or closed on the node.
func (m *Machine) noteOpened(method string, params json.RawMessage, err error) {
	var p struct {
		ID        string `json:"id"`
		SessionID string `json:"session_id"`
	}
	switch method {
	case "plugin.node.dial", "preview.open", "terminal.watch", "terminal.unwatch":
	default:
		return
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	o := &m.opened
	switch {
	case (method == "plugin.node.dial" || method == "preview.open") && err == nil && p.ID != "":
		if o.streams == nil {
			o.streams = map[string]bool{}
		}
		o.streams[p.ID] = false
	case method == "terminal.watch" && err == nil && p.SessionID != "":
		if o.terms == nil {
			o.terms = map[string]bool{}
		}
		o.terms[p.SessionID] = true
	case method == "terminal.unwatch":
		delete(o.terms, p.SessionID)
	}
}

// noteClosed: a stream or a terminal ended (the hub's stream.close, the node's stream.close or terminal.end).
func (m *Machine) noteClosed(event string, params json.RawMessage) {
	if event != streams.EventClose && event != "terminal.end" {
		return
	}
	var p struct {
		ID        string `json:"id"`
		SessionID string `json:"session_id"`
		Control   bool   `json:"control"` // only its control ended: still watched
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if event == streams.EventClose {
		delete(m.opened.streams, p.ID)
	} else if !p.Control {
		delete(m.opened.terms, p.SessionID)
	}
}

// forgetOpenedLocked: the node's connection ended, and with it all it had open; m.mu is held.
func (m *Machine) forgetOpenedLocked() { m.opened = hubOpened{} }

// endHubOpened ends on the nodes what the hub had open through the last bus connection (a new one said hello).
func (d *Devices) endHubOpened() {
	for _, m := range d.machinesNow() {
		m.mu.Lock()
		o, p := m.opened, m.peer
		m.opened = hubOpened{}
		m.mu.Unlock()
		if p == nil || len(o.streams)+len(o.terms) == 0 {
			continue // no connection: nothing of it is open on the node
		}
		d.logf("[%s] the hub's connection is new: %d streams and %d terminals of the last one end on the node", m.cfg.ID,
			len(o.streams), len(o.terms))
		for id, halfClose := range o.streams {
			m.closePort(id, halfClose)
		}
		// On this node connection, now: before the hub asks anything through the new bus connection (a terminal an
		// app watches again).
		for id := range o.terms {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = p.Call(ctx, "terminal.unwatch", map[string]any{"session_id": id}, nil)
			}()
		}
	}
}

func (m *Machine) closePort(id string, abort bool) {
	b, _ := json.Marshal(struct {
		ID    string `json:"id"`
		Abort bool   `json:"abort,omitempty"`
	}{id, abort})
	m.queueNote(streams.EventClose, b)
}
