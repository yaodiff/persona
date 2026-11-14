package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/streams"
)

// The computers are the manager's (internal/devices, docs/server/machines.md "Who connects to computers"): the code moved
// here from the hub reaches them as it did, through a Machine per computer; its calls go to the devices in this
// process. A Machine keeps what this code keeps of a computer (Cua's and ego's state) across the devices' reconnects.

var errOffline = apierr.ErrOffline

// MachineConfig is what this code reads of a computer's hub.json entry.
type MachineConfig struct {
	ID      string
	Name    string
	Kind    string
	SSHHost string
}

// nodeCaller is how calls reach a computer's node: the devices here, or a test's rpc.Peer.
type nodeCaller interface {
	Call(ctx context.Context, method string, params, out any) error
	Notify(event string, params any) error
	Done() <-chan struct{}
	Close(err error)
}

// Machine is one computer as this code sees it. Its sessions are the devices' (sessionFinder): every lookup refreshed
// a copy of them here, 6.3 MB at 20 computers of 200 sessions (#3197).
type Machine struct {
	// cfg is what the Machine was made with, never written again: lookups read its ID without a lock while another
	// refreshes the mirror (machinesNow). The name and ssh alias the devices list now are config's.
	cfg    MachineConfig
	listed atomic.Pointer[MachineConfig] // set by machinesNow once they differ from cfg's

	mu     sync.Mutex
	peer   nodeCaller
	info   proto.NodeInfo
	online bool
	// computer: what the node last said of Cua Driver (computer.status), while the plugin cua is on for it
	// (computer.go); computerAt: when it answered, so an older answer never replaces it (keepComputer).
	computer   *proto.Computer
	computerAt time.Time
	// browser, browserAt: the same for ego lite (browser.status, the plugin ego; browser.go).
	browser   *proto.Browser
	browserAt time.Time
	// iphone, iphoneAt: the same for the iPhones it reaches (iphone.status, the extension iphone; iphone.go).
	iphone   *proto.IPhone
	iphoneAt time.Time
	// streams are the manager's streams to its node (portStreams).
	streams *streams.Mux
	// noLockMu and cursorMu send computer.nolock and computer.cursor to the node one at a time (tellNoLock,
	// tellCursor). Each tell reads the setting and then waits for the node, and the plugin's changes start them
	// concurrently; without the lock, a tell that read the old setting could reach the node after a newer one and
	// leave it held awake (for up to the node's 30 minutes) or with the wrong cursor.
	noLockMu, cursorMu sync.Mutex
}

// config is the computer's configuration as the devices list it now.
func (m *Machine) config() MachineConfig {
	if c := m.listed.Load(); c != nil {
		return *c
	}
	return m.cfg
}

// name is the computer's name as the devices list it now.
func (m *Machine) name() string { return m.config().Name }

// sshHost is the computer's ssh alias as the devices list it now.
func (m *Machine) sshHost() string { return m.config().SSHHost }

func (m *Machine) call(ctx context.Context, method string, params any, out any) error {
	m.mu.Lock()
	p := m.peer
	m.mu.Unlock()
	if p == nil {
		return fmt.Errorf("%s: %w", m.name(), errOffline)
	}
	select {
	case <-p.Done():
		return fmt.Errorf("%s: %w", m.name(), errOffline)
	default:
	}
	return p.Call(ctx, method, params, out)
}

func (m *Machine) isOnline() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.online
}

// mcpWaitCapable is the target node's authenticated handshake promise to preserve call_id through a nested elicitation.
// Protocol 0 and an omitted capability are deliberately incapable, so an older target yields the unknown fallback.
func (m *Machine) mcpWaitCapable() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info.Protocol > 0 && slices.Contains(m.info.Capabilities, nodeproto.MCPWait)
}

// devicesPeer is a computer's node through the devices in this process.
type devicesPeer struct {
	d  Devices
	id string
}

func (p devicesPeer) Call(ctx context.Context, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	if params == nil {
		raw = json.RawMessage(`{}`)
	}
	res, err := p.d.CallNode(ctx, p.id, method, raw)
	if err != nil {
		return err
	}
	if out == nil || len(res) == 0 {
		return nil
	}
	return json.Unmarshal(res, out)
}

func (p devicesPeer) Notify(event string, params any) error {
	n, ok := p.d.(Notifier)
	if !ok {
		return errOffline
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	n.Notify(devicewire.NodeEvent{MachineID: p.id, Event: event, Params: raw})
	return nil
}
func (p devicesPeer) Done() <-chan struct{} { return nil }
func (p devicesPeer) Close(error)           {}

// Viewer is the devices' list of the computers (devices.Devices.Views).
type Viewer interface {
	Views() []devices.View
}

// sessionFinder reads a computer's sessions where the devices keep them (devices.Devices.FindSession): the mirror
// keeps none.
type sessionFinder interface {
	FindSession(id string, match func(*proto.Session) bool) bool
}

// findSession reports whether match accepts one of the sessions m's node lists, stopping at the first; false without
// devices that keep them. match reads each in place under the devices' lock: it must not keep it or call them.
func (h *Manager) findSession(m *Machine, match func(*proto.Session) bool) bool {
	f, ok := h.opts.Devices.(sessionFinder)
	return ok && f.FindSession(m.cfg.ID, match)
}

// machinesNow are the computers now, as the devices list them (tests set h.machines and h.byID themselves).
func (h *Manager) machinesNow() []*Machine {
	v, ok := h.opts.Devices.(Viewer)
	h.fleetMu.Lock()
	defer h.fleetMu.Unlock()
	if !ok {
		return slices.Clone(h.machines)
	}
	views := v.Views()
	order := make([]*Machine, 0, len(views))
	if h.byID == nil {
		h.byID = map[string]*Machine{}
	}
	seen := map[string]bool{}
	for _, d := range views {
		cfg := MachineConfig{ID: d.ID, Name: d.Name, Kind: d.Kind, SSHHost: d.SSHHost}
		m := h.byID[d.ID]
		if m == nil {
			m = &Machine{cfg: cfg}
			h.byID[d.ID] = m
		} else if m.config() != cfg { // renamed: never written into cfg, which lookups read without a lock
			m.listed.Store(&cfg)
		}
		m.mu.Lock()
		m.info, m.online = d.Info, d.Online
		if d.Reachable {
			m.peer = devicesPeer{d: h.opts.Devices, id: d.ID}
		} else {
			m.peer = nil
		}
		m.mu.Unlock()
		order = append(order, m)
		seen[d.ID] = true
	}
	for id := range h.byID {
		if !seen[id] {
			delete(h.byID, id)
		}
	}
	h.machines = order
	return slices.Clone(order)
}

func (h *Manager) machineByID(id string) *Machine {
	if id == "" {
		return nil
	}
	h.machinesNow()
	h.fleetMu.Lock()
	defer h.fleetMu.Unlock()
	return h.byID[id]
}

func (h *Manager) localMachine(id string) *Machine { return h.machineByID(id) }

func (h *Manager) machineName(id string) string {
	if m := h.localMachine(id); m != nil {
		return m.name()
	}
	return id
}

// machineByName is a machine by id, name or ssh alias.
func (h *Manager) machineByName(id string) *Machine {
	if m := h.localMachine(id); m != nil {
		return m
	}
	for _, m := range h.machinesNow() {
		if strings.EqualFold(m.name(), id) || strings.EqualFold(m.sshHost(), id) {
			return m
		}
	}
	return nil
}

// broadcastMachines: what the apps show of the computers changed here (Cua's, ego's state): the report has it.
func (h *Manager) broadcastMachines() { h.sendReport() }

// Streams to a computer's node for the extensions' programs there (plugin.node.dial): the manager's own, over the
// devices' connection, ids starting with streamPrefix (the hub's forwarded ports have theirs); the node's
// stream.* events for them come here (NodeEvent) and do not go on to the hub.

const (
	maxPortStreams = 64
	streamPrefix   = "mx"
)

var streamSeq atomic.Int64

// Notifier sends a computer's node a one-way event (devices.Devices.Notify).
type Notifier interface {
	Notify(p devicewire.NodeEvent)
}

// portStreams is the Mux of the manager's streams to m's node; nil while it is offline.
func (m *Machine) portStreams() *streams.Mux {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.peer == nil {
		return nil
	}
	if m.streams == nil {
		peer := m.peer
		m.streams = streams.New(func(event string, params any) error { return peer.Notify(event, params) })
	}
	return m.streams
}

// NodeEvent takes a node's event that is the manager's own (its streams): handled false for the rest.
func (h *Manager) NodeEvent(machine, event string, params json.RawMessage) bool {
	switch event {
	case streams.EventData, streams.EventAck, streams.EventClose:
	case "plugin.node.state", "plugin.node.event": // the extensions' programs on this computer (extension_nodes.go)
		if m := h.machineByID(machine); m != nil {
			h.extensionNodeEvent(m, event, params)
		}
		return true
	default:
		return false
	}
	var p struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(params, &p) != nil || !strings.HasPrefix(p.ID, streamPrefix) {
		return false
	}
	if m := h.machineByID(machine); m != nil {
		if mux := m.portStreams(); mux != nil {
			mux.Event(event, params)
		}
	}
	return true
}
