package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The computers are the manager's (docs/server/machines.md "Who connects to computers", devicewire): the manager that carries them — the
// chief, the assistant's process, during the migration to managers — connects every one and holds what is known of it.
// The hub hands it hub.json's machines as defaults (the hello's devices section, devices.config when they change),
// relays the apps' calls and streams to it (busNode: devices.call, devices.notify), and keeps what it reports
// (devices.state, devices.event) to show the apps and drive its own features. It never dials or accepts a computer;
// a Persona Link that dials the hub's address is relayed to the manager as it is (devices_relay.go).

// devicesOwner is the manager that carries the machines.
const devicesOwner = assistantwire.AssistantID

// errManagerOffline: the manager that carries the machine is not connected; an app's call is answered 503
// assistant_offline. It counts as offline (apierr.ErrOffline) for the code that only asks that.
var errManagerOffline error = managerOfflineError{}

type managerOfflineError struct{}

func (managerOfflineError) Error() string        { return errAssistantOffline.Error() }
func (managerOfflineError) Is(target error) bool { return target == apierr.ErrOffline }

// devicesState is the hub's side of the manager's machines.
type devicesState struct {
	transportOwners map[string]string // manager-owned transport aliases, no session mapping
	mu              sync.Mutex
	// entries are hub.json's machines and those the admin page added since (handed to the manager).
	entries []MachineConfig
	// absorbed: a hub.json entry whose computer another entry has → that machine's id; recEntries: this hub's
	// computers as its mesh record lists them (what each one's manager says of reaching it), by manager.
	absorbed   map[string]string
	recEntries map[string][]devicewire.RecMachine
	// reported: what each manager last reported of its own machines (machine_entries.go), kept in hub_settings;
	// reportedRead: the setting was read into it (at start, or before a write since); reportedForgot, entriesForgot:
	// the managers removed since, the machines removed since, not taken back from the setting read late; refused:
	// the ids each manager's last report named that are not its own (logged once); reportedDue: a write is to go;
	// reportedSaved: the last write; reportedSave keeps writes one at a time.
	reported         reportedMachines
	reportedSince    map[string]bool // the managers that reported since the hub started
	reportedRead     bool
	reportedForgot   map[string]bool
	entriesForgot    map[string]bool
	refused          map[string]map[string]bool
	reportedDue      bool
	reportedDeferred bool
	reportedSaved    time.Time
	reportedSave     sync.Mutex
	// kept: the machines their manager keeps in its own list, out of hub.json (machines_kept.go), machine → manager;
	// keptRead: that is what machinesKeptKey holds. putTried: the hub.json machines put to their manager's own list
	// (settleOwnList), each once per manager.
	kept     map[string]string
	keptRead bool
	putTried map[string]string
	// ownListHeld: each manager's last report said it holds its own list in its manager.json (States.OwnList);
	// lastNamed: the entries its last report named (accepted or not); settleDue: a settleOwnList waits for it;
	// saidLost: the kept machines its list lacked, said once; warnedOld: the keepers whose build lost machines.own,
	// said once.
	ownListHeld map[string]bool
	lastNamed   map[string]map[string]bool
	settleDue   map[string]bool
	saidLost    map[string]bool
	warnedOld   map[string]bool
	// up is closed while its manager is connected and has reported its machines, by manager (upChanLocked makes one):
	// what a machine's reachability, a call to its node and its manager's devices.list "complete" read. The state is
	// each manager's own, never the carrier's for everyone: one flag made the carrier's restart show the other
	// managers' machines unreachable and their lists incomplete, and let a call to another manager's machine fail at
	// once while the hub's start showed it online (#6795). warmUntil: a hub just restarted, the machines shown as
	// before; until then a call waits for a manager that has not reported since the start (reportedSince).
	up        map[string]chan struct{}
	upOpen    map[string]bool
	warmUntil time.Time
	// conns counts owner's connections (devicesConnected): the end of one forgets its up state only when no newer one
	// came meanwhile (upGone).
	conns map[string]uint64
	// fresh: the manager's connection is new (by manager); at its first report the hub asks it to watch what the
	// apps watch.
	fresh map[string]bool
	// sent is the config last handed over; due: a devices.config is to go out.
	sent map[string][]byte // by manager
	// assign: hub.json's machines handed to another manager than the carrier (ownership.go), machine → manager.
	assign map[string]string
	// assignRead: assign is what machineOwnersKey holds (read, or nothing to read); a move writes only over a read one.
	assignRead   bool
	assignFailed bool // ownership read failed: dedicated local machines must not fall back to the carrier
	// moving keeps moves one at a time: each writes the whole of assign, from the one before it (moveMachine).
	moving        sync.Mutex
	due           bool
	nodeUpdateDue bool // an explicit policy write also requests a fresh package check
	started       bool
	fleetReady    bool // startDevices finished the initial fleet; late kept-list reads reconcile recovered machines
	// reqs are the nodes' requests being answered (devices.request) by the manager's key; reqEnded the keys whose
	// devices.request_end came before the request did (an event is read at once, a call in its own goroutine).
	reqs     map[string]context.CancelFunc
	reqEnded map[string]time.Time
}

func init() {
	handleBusEvent(devicewire.EventState, func(h *Hub, c *busConn, params json.RawMessage) {
		var st devicewire.States
		if err := json.Unmarshal(params, &st); err != nil {
			h.logf("bus: [%s] %s: %v", c.ident.ID, devicewire.EventState, err)
			return
		}
		h.applyDeviceStatesFrom(c.managerID(), st)
	})
	handleBusEvent(devicewire.EventNode, func(h *Hub, c *busConn, params json.RawMessage) {
		var ev devicewire.NodeEvent
		if json.Unmarshal(params, &ev) != nil || h.ownsMachine(c, ev.MachineID) != nil {
			return // only its manager speaks for a machine's node (ownership.go)
		}
		m := h.machineByID(ev.MachineID)
		if m == nil {
			return
		}
		if mux := m.portStreams(); mux != nil && mux.Event(ev.Event, ev.Params) {
			return
		}
		m.onEvent(ev.Event, ev.Params)
	})
	handleTypedBus(devicewire.RequestMethod, devicewire.MethodRequest+": ", func(h *Hub, ctx context.Context, c *busConn, p devicewire.NodeCall) (json.RawMessage, error) {
		if err := h.ownsMachine(c, p.MachineID); err != nil {
			return nil, err
		}
		m := h.machineByID(p.MachineID)
		if m == nil {
			return nil, &rpc.Error{Code: devicewire.CodeNoDevice, Message: fmt.Sprintf("no machine %q", p.MachineID)}
		}
		args := p.Params
		if len(args) == 0 {
			args = json.RawMessage(`{}`)
		}
		ctx, done := h.requestScope(ctx, requestKey(c, p.Request))
		defer done()
		return deviceRequestReply(m.onRequest(ctx, p.Method, args))
	})
	handleBusEvent(devicewire.EventRequestEnd, func(h *Hub, c *busConn, params json.RawMessage) {
		var p devicewire.RequestEnd
		if json.Unmarshal(params, &p) != nil || p.Request == "" {
			return
		}
		// Its own requests only (the keys are its own); an end mark kept ahead of the request only for its own machine.
		h.requestEnded(requestKey(c, p.Request), h.ownsMachine(c, p.MachineID) == nil)
	})
	handleTypedBus(devicewire.ConnectedMethod, devicewire.MethodConnected+": ", func(h *Hub, ctx context.Context, c *busConn, p devicewire.Connected) (devicewire.ConnectedReply, error) {
		if err := h.ownsMachine(c, p.MachineID); err != nil {
			return devicewire.ConnectedReply{}, err
		}
		m := h.machineByID(p.MachineID)
		if m == nil {
			return devicewire.ConnectedReply{}, &rpc.Error{Code: devicewire.CodeNoDevice, Message: fmt.Sprintf("no machine %q", p.MachineID)}
		}
		return h.machineConnected(ctx, m, p.ConnectedAt), nil
	})
	// A service machine's credential goes only to the manager that carries it (ownership.go).
	handleTypedBus(devicewire.ConnectorCredMethod, "", func(h *Hub, ctx context.Context, c *busConn, p devicewire.ConnectorCredRequest) (devicewire.ConnectorCred, error) {
		if err := h.ownsMachine(c, p.MachineID); err != nil {
			return devicewire.ConnectorCred{}, err
		}
		return h.connectorCred(ctx, p.MachineID)
	})
	handleTypedBus(devicewire.ConnectorPairedMethod, "", func(h *Hub, ctx context.Context, c *busConn, p devicewire.ConnectorCred) (buswire.None, error) {
		if err := h.ownsMachine(c, p.MachineID); err != nil {
			return buswire.None{}, err
		}
		return buswire.None{}, h.keepConnectorCred(ctx, p)
	})
}

// ---- the nodes' requests ----

func deviceRequestReply(result any, err error) (json.RawMessage, error) {
	if err != nil {
		return nil, err
	}
	// Use the same encoder as the RPC peer, including its HTML escaping policy.
	raw, err := rpc.Marshal(result)
	if err != nil {
		return nil, &rpc.Error{Message: err.Error()}
	}
	return raw, nil
}

// requestScope is a node's request's context: the bus connection's, ended too when the manager says the node's
// connection is gone (devices.request_end), as a node's own connection ending ends what the hub does for it.
func (h *Hub) requestScope(ctx context.Context, key string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	if key == "" {
		return ctx, cancel
	}
	d := &h.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ended := d.reqEnded[key]; ended {
		delete(d.reqEnded, key)
		cancel()
		return ctx, cancel
	}
	if d.reqs == nil {
		d.reqs = map[string]context.CancelFunc{}
	}
	d.reqs[key] = cancel
	return ctx, func() {
		d.mu.Lock()
		delete(d.reqs, key)
		d.mu.Unlock()
		cancel()
	}
}

// requestKey is manager c's request key: one manager's keys never name another's requests.
func requestKey(c *busConn, key string) string {
	if key == "" {
		return ""
	}
	return c.managerID() + "\x00" + key
}

// requestEnded: devices.request_end. A request that is not running yet is marked ended for when it comes, if mark.
func (h *Hub) requestEnded(key string, mark bool) {
	d := &h.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	if cancel := d.reqs[key]; cancel != nil {
		delete(d.reqs, key)
		cancel()
		return
	}
	if !mark {
		return
	}
	now := time.Now()
	for k, at := range d.reqEnded { // its request never came (it was answered, or the bus dropped): not kept long
		if now.Sub(at) > time.Minute {
			delete(d.reqEnded, k)
		}
	}
	if d.reqEnded == nil {
		d.reqEnded = map[string]time.Time{}
	}
	d.reqEnded[key] = now
}
