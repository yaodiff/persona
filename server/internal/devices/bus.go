package devices

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// What the hub asks of the manager about the machines (devicewire), and what the manager asks of it.

// Request answers the hub's devices.* calls; handled false: not one of them.
func (d *Devices) Request(ctx context.Context, method string, params json.RawMessage) (any, bool, error) {
	decode := func(v any) error {
		if err := json.Unmarshal(params, v); err != nil {
			return &rpc.Error{Code: "invalid_params", Message: method + ": " + err.Error()}
		}
		return nil
	}
	switch method {
	case devicewire.MethodCall:
		return handleDeviceRequest(devicewire.CallMethod, decode, func(p devicewire.NodeCall) (json.RawMessage, error) {
			return d.Call(ctx, p)
		})
	case devicewire.MethodFileCall:
		return handleDeviceRequest(devicewire.FileCallMethod, decode, func(p devicewire.NodeCall) (json.RawMessage, error) {
			return d.Call(ctx, p)
		})
	case devicewire.MethodRetireCall:
		return handleDeviceRequest(devicewire.RetireCallMethod, decode, func(p devicewire.NodeCall) (json.RawMessage, error) {
			if p.Method != nodeproto.StopIfIdle {
				return nil, &rpc.Error{Code: "invalid_params", Message: "devices.retire_call requires session.stop_if_idle"}
			}
			return d.Call(ctx, p)
		})
	case devicewire.MethodHoldStart:
		return handleDeviceRequest(devicewire.HoldStartMethod, decode, func(p devicewire.HoldStartRequest) (devicewire.HoldStartReply, error) {
			return d.holdStart(ctx, p)
		})
	case devicewire.MethodAccept:
		return handleDeviceRequest(devicewire.AcceptMethod, decode, func(p devicewire.Accept) (buswire.None, error) {
			return buswire.None{}, d.Accept(ctx, p)
		})
	case devicewire.MethodReconnect:
		return handleDeviceRequest(devicewire.ReconnectMethod, decode, func(p devicewire.ReconnectRequest) (buswire.None, error) {
			return buswire.None{}, d.Reconnect(p.MachineID)
		})
	case devicewire.MethodMachinesPut:
		return handleDeviceRequest(devicewire.MachinesPutMethod, decode, func(p devicewire.MachinePut) (buswire.None, error) {
			return buswire.None{}, d.putMachine(ctx, p.Entry)
		})
	case devicewire.MethodMachinesRemove:
		return handleDeviceRequest(devicewire.MachinesRemoveMethod, decode, func(p devicewire.MachineRef) (devicewire.MachineRemoved, error) {
			removed, err := d.removeMachine(ctx, p.MachineID)
			return devicewire.MachineRemoved{Removed: removed}, err
		})
	case devicewire.MethodMachinesGet:
		return handleDeviceRequest(devicewire.MachinesGetMethod, decode, func(p devicewire.MachineRef) (devicewire.MachineEntry, error) {
			entry, err := d.machineEntry(p.MachineID)
			return devicewire.MachineEntry{Entry: entry}, err
		})
	case devicewire.MethodEngineGet:
		return handleDeviceRequest(devicewire.EngineGetMethod, decode, d.engineChoiceToMove)
	case devicewire.MethodEnginePut:
		return handleDeviceRequest(devicewire.EnginePutMethod, decode, func(p devicewire.MachineEngine) (buswire.None, error) {
			if p.MachineID == "" || p.Choice == nil {
				return buswire.None{}, &rpc.Error{Code: bus.CodeInvalidParams, Message: method + ": machine_id and choice are required"}
			}
			return buswire.None{}, d.setEngineHosts(ctx, p.MachineID, p.Choice.Hosts, p.Choice.Default)
		})
	case devicewire.MethodUpdateNodes:
		return handleDeviceRequest(devicewire.UpdateNodesMethod, decode, func(buswire.None) (buswire.None, error) {
			go d.updateNodes(context.WithoutCancel(ctx))
			return buswire.None{}, nil
		})
	case devicewire.MethodUsageGatewayAccounts:
		return handleDeviceRequest(devicewire.UsageGatewayAccountsMethod, decode, func(buswire.None) (proto.GatewayAccounts, error) {
			return d.UsageGatewayAccounts(), nil
		})
	case devicewire.MethodPairConnector:
		return handleDeviceRequest(devicewire.PairConnectorMethod, decode, func(p devicewire.PairConnector) (devicewire.PairConnectorReply, error) {
			addr, version, err := d.PairConnector(ctx, p)
			return devicewire.PairConnectorReply{Address: addr, Version: version}, err
		})
	}
	return nil, false, nil
}

// handleDeviceRequest checks a handler against its descriptor and keeps Request's lenient decoder.
func handleDeviceRequest[P, R any](_ buswire.Method[P, R], decode func(any) error, run func(P) (R, error)) (any, bool, error) {
	var p P
	// Methods without parameters historically ignored the payload entirely.
	if _, unused := any(p).(buswire.None); !unused {
		if err := decode(&p); err != nil {
			return nil, true, err
		}
	}
	out, err := run(p)
	if err != nil {
		return nil, true, err
	}
	return out, true, nil
}

// Event takes the hub's devices.* and credential revocation events; false: not one of them.
func (d *Devices) Event(name string, params json.RawMessage) bool {
	switch name {
	case bus.EventCredentialRevoked:
		var p bus.DeviceCredential
		if json.Unmarshal(params, &p) == nil && p.ID != "" {
			d.heldCredentialRevoked(p.ID)
		}
	case devicewire.EventNotify:
		var p devicewire.NodeEvent
		if json.Unmarshal(params, &p) == nil {
			d.Notify(p)
		}
	case devicewire.EventConfig:
		var cfg devicewire.Config
		if json.Unmarshal(params, &cfg) == nil {
			d.Configure(cfg)
		}
	case devicewire.EventLinkRTT:
		var p devicewire.LinkRTT
		if json.Unmarshal(params, &p) == nil {
			d.linkRTT(p)
		}
	default:
		return false
	}
	return true
}

// Hello takes the devices section of the hub's answer to manager.hello (every handshake).
func (d *Devices) Hello(res json.RawMessage) {
	// Before the connection counts as up: the hub asks nothing through it yet (hubopen.go).
	d.endHubOpened()
	var h struct {
		Devices *devicewire.Config `json:"devices"`
	}
	if json.Unmarshal(res, &h) == nil && h.Devices != nil {
		d.Configure(*h.Devices)
	}
}

// hubConnected hands the node, through the hub, what is the hub's (Codex settings, the sends it
// remembers), before the manager asks the node for its sessions; the hub also starts what it does on a connection
// (the plugins' programs, Cua). The hub not there: it does that when it is back (it sees the connection then).
// theirs: the hub refused it as another manager's machine (it moved away while this manager still had it): the
// manager gives it nothing of its own (its launchers with their keys, its extensions' part).
func (d *Devices) hubConnected(ctx context.Context, m *Machine, at time.Time) (reply *devicewire.ConnectedReply, theirs bool) {
	if !d.busUp() {
		return nil, false
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := buswire.Call(cctx, d.opts.Hub, devicewire.ConnectedMethod, devicewire.Connected{MachineID: m.cfg.ID, ConnectedAt: at})
	if err != nil {
		d.logf("[%s] the hub's part of the connection: %v", m.cfg.ID, err)
		var re *rpc.Error
		return nil, errors.As(err, &re) && re.Code == devicewire.CodeNoDevice
	}
	return &out, false
}

// withOwn adds the manager's own part of a connection (Options.Own) to the hub's reply (nil: the hub is not there).
func (d *Devices) withOwn(ctx context.Context, machine string, hub *devicewire.ConnectedReply) *devicewire.ConnectedReply {
	if d.opts.Own == nil {
		return hub
	}
	own := d.opts.Own(ctx, machine)
	if len(own) == 0 {
		return hub
	}
	out := &devicewire.ConnectedReply{Configure: map[string]json.RawMessage{}}
	if hub != nil {
		out.Calls = hub.Calls
		for k, v := range hub.Configure {
			out.Configure[k] = v
		}
	}
	for k, v := range own {
		out.Configure[k] = v
	}
	return out
}

// rehook does the hub's part of every live connection again: the hub is new (it restarted) or the manager's
// connection to it is.
func (d *Devices) rehook(ctx context.Context) {
	for _, m := range d.machinesNow() {
		m.mu.Lock()
		peer, at, ready := m.peer, m.connectedAt, m.sessionsReady
		m.mu.Unlock()
		if peer == nil {
			continue
		}
		go func() {
			if ready != nil {
				// A first list that is still out has no replay in BusUp. Its normal initial forward closes ready;
				// configuring only afterwards keeps the hub's rehook behind that real snapshot.
				select {
				case <-ready:
				case <-peer.Done():
					return
				case <-ctx.Done():
					return
				}
			}
			if reply, _ := d.hubConnected(ctx, m, at); reply != nil {
				m.configureNode(ctx, peer, d.withOwn(ctx, m.cfg.ID, reply), true)
			}
		}()
	}
}

// keptOn is the sessions the machine's node lists however quiet; ok false while the assistant has not said.
func (d *Devices) keptOn(machine string) ([]string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.keep == nil {
		return nil, false
	}
	return slices.Clone(d.keep[machine]), true
}

// SetKeep takes the sessions each machine's node lists however long they are quiet (the assistant's profile: the
// main conversation, the ones it replaced, scheduled tasks' own) and tells the nodes whose list changed; the first
// time, every node. A node that connects later gets it with its configuration.
func (d *Devices) SetKeep(keep map[string][]string) {
	d.mu.Lock()
	before := d.keep
	d.keep = keep
	d.mu.Unlock()
	for _, m := range d.machinesNow() {
		if before != nil && slices.Equal(before[m.cfg.ID], keep[m.cfg.ID]) {
			continue
		}
		go func() {
			// One at a time, and what the latest report says when it goes: two reports close together (a new main
			// conversation, then its first message) must not reach the node in the other order.
			d.keepMu.Lock()
			defer d.keepMu.Unlock()
			list, ok := d.keptOn(m.cfg.ID)
			if !ok {
				return
			}
			if list == nil {
				list = []string{}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := m.call(ctx, "node.configure", map[string]any{"keep_sessions": list}, nil); err != nil && !isOffline(err) {
				d.logf("[%s] keep sessions: %v", m.cfg.ID, err)
			}
		}()
	}
}

// Handler serves the apps' routes on machines that are the manager's (devicewire.Routes), relayed by the hub; other
// paths go to next.
func (d *Devices) Handler(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	route := func(pattern string, fn func(http.ResponseWriter, *http.Request, *Machine)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			m := d.machineByID(r.PathValue("mid"))
			if m == nil {
				writeErr(w, http.StatusNotFound, errUnknownMachine)
				return
			}
			fn(w, r, m)
		})
	}
	route("POST /api/v1/machines/{mid}/reconnect", func(w http.ResponseWriter, r *http.Request, m *Machine) {
		m.reconnect()
		writeJSON(w, map[string]any{"ok": true})
	})
	route("GET /api/v1/machines/{mid}/codex/{sid}/takeover", d.prepareCodexTakeover)
	route("GET /api/v1/sessions/{mid}/{sid}/skills", d.getSessionSkills)
	route("GET /api/v1/sessions/{mid}/{sid}/mode-changes", d.getModeChanges)
	route("POST /api/v1/sessions/{mid}/{sid}/start/continue", d.postStartContinue)
	route(devicewire.HeldOutboxRoute, d.listHeldOutbox)
	route("POST /api/v1/sessions/{mid}/{sid}/held/{msg}/retry", d.postHeldRetry)
	route("DELETE /api/v1/sessions/{mid}/{sid}/held/{msg}", d.deleteHeld)
	route(devicewire.HeldStartsListRoute, d.listHeldStarts)
	route(devicewire.HeldStartRetryRoute, d.postHeldStartRetry)
	route(devicewire.HeldStartDeleteRoute, d.deleteHeldStart)
	route("POST /api/v1/machines/{mid}/codex/{sid}/takeover", d.commitCodexTakeover)
	route("PUT /api/v1/machines/{mid}/engine", d.putEngine)
	route("GET /api/v1/machines/{mid}/herdr", d.getHerdr)
	route("PUT /api/v1/machines/{mid}/herdr", d.putHerdr)
	route("POST /api/v1/machines/{mid}/herdr/start", d.postHerdrStart)
	route("GET /api/v1/machines/{mid}/herdr/sessions", d.getHerdrSessions)
	route("PUT /api/v1/machines/{mid}/herdr/sessions/blocked", d.putHerdrSessionsBlocked)
	route("GET /api/v1/machines/{mid}/botmux", d.getBotmux)
	route("PUT /api/v1/machines/{mid}/botmux", d.putBotmux)
	route("GET /api/v1/machines/{mid}/orca-chat-starts", d.getOrcaChatStarts)
	route("PUT /api/v1/machines/{mid}/orca-chat-starts", d.putOrcaChatStarts)
	route("GET /api/v1/machines/{mid}/health/reports", d.listHealthReports)
	route("DELETE /api/v1/machines/{mid}/health/reports/{name}", d.deleteHealthReport)
	route("POST /api/v1/machines/{mid}/deploy", func(w http.ResponseWriter, r *http.Request, m *Machine) {
		if err := m.startDeploy(); err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	route("POST /api/v1/machines/{mid}/link-switch", d.postLinkSwitch)
	route("GET /api/v1/machines/{mid}/diagnosis", d.getDiagnosis)
	route("POST /api/v1/machines/{mid}/cleanup", d.postCleanup)
	d.usageMapRoutes(route)
	route("GET /api/v1/machines/{mid}/claude-dirs", d.getClaudeDirs)
	route("GET /api/v1/machines/{mid}/agent-dirs/hidden", d.getHiddenAgentDirs)
	route("PUT /api/v1/machines/{mid}/agent-dirs/hidden", d.putHiddenAgentDirs)
	route("GET /api/v1/machines/{mid}/claude-live-replies", d.getLiveReplies)
	route("PUT /api/v1/machines/{mid}/claude-live-replies", d.putLiveReplies)
	route("GET /api/v1/machines/{mid}/clipboard", d.getClipboard)
	route("PUT /api/v1/machines/{mid}/clipboard", d.putClipboard)
	route("GET /api/v1/machines/{mid}/clipboard/settings", d.getClipboardSettings)
	route("PUT /api/v1/machines/{mid}/clipboard/settings", d.putClipboardSettings)
	route("GET /api/v1/machines/{mid}/file-transfer/settings", d.getFileTransferSettings)
	route("PUT /api/v1/machines/{mid}/file-transfer/settings", d.putFileTransferSettings)
	route("GET /api/v1/machines/{mid}/files/operations/{oid}", d.getFileOperation)
	d.transferRoutes(mux)
	d.metricsRoutes(mux)
	d.usageRoutes(mux)
	d.usageHistoryRoutes(mux)
	d.usageProjectRoutes(mux)
	d.usageWindowRoutes(mux)
	d.adminUsageRoutes(mux)
	d.shareRoutes(mux)
	route("PUT /api/v1/machines/{mid}/claude-dirs/blocked", d.putClaudeDirsBlocked)
	mux.Handle("/", next)
	return mux
}
