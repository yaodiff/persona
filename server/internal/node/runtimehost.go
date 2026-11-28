package node

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/acceptloop"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/runtimepkg"
)

// RuntimeHost is the runtime socket (runtime.sock, runtimes.go) and the runtimes connected to it. A resident node
// (`personad node --attach`, attach.go) makes one when it starts and shares it with the Node it makes for each hub
// connection, so runtimes connect before the hub does and stay connected while it reconnects: their sessions are in
// the first list the next connection sends, and nothing on the runtime's side notices the gap. A node serving its one
// connection itself (`--stdio` with no resident node) has one of its own, for that connection (listenRuntimeSocket).
//
// Between hub connections runtimes are still answered: hello (with hub: false), their lists are kept, what they push
// for the phone is dropped (the hub re-watches from where the phone is when it is back), and an ask answers node_gone
// so the runtime asks again, with the same id, once a hub is connected. Runtimes are told when the hub comes and goes
// (runtime.hub), so they do not wait on a phone that cannot answer.
type RuntimeHost struct {
	reg     *runtimeRegistry
	path    string
	version string
	logger  *log.Logger
	// pipe is the named pipe runtimes the node starts dial on Windows (runtimepipe_windows.go); "" elsewhere, or
	// when it could not be made. Set by Listen before any runtime starts, then fixed.
	pipe string

	mu                   sync.Mutex
	node                 *Node                           // the Node serving a hub connection now; nil between connections
	hubSeq               uint64                          // monotonic attach/detach transition counter
	hubEpoch             uint64                          // monotonic hub-connection epoch incremented on every attach
	gp                   *gateProxy                      // last attached Node's gate proxy (resident keeper's) so disconnects between hub links release leases
	leaseDisconnectGrace time.Duration                   // zero: runtimeLeaseDisconnectGrace (tests shorten it)
	pendingReleases      map[string]*pendingLeaseRelease // runtime id -> deferred disconnect release while same instance may reconnect
	closed               bool                            // Listen context ended; no new deferred timers are armed

	// The daemons it dials (daemons.go), by id: their ids are reserved against runtimes on the socket.
	dmu        sync.Mutex
	daemons    map[string]*daemonRun
	daemonsSum [32]byte      // daemons.json as last applied
	dtimes     daemonTimings // zero: defaultDaemonTimings (tests make them quick)

	// ctx is Listen's: what runs for the socket's sake (the Paseo bridge) ends with it.
	ctx context.Context
	// paseo runs the Paseo bridge while the machine's engine is Paseo (paseobridge.go).
	paseo paseoBridge
	// orca runs the bridge to Orca's structured sessions while the machine's engine is Orca (orcabridge.go).
	orca orcaBridgeState
	// orcaBots is what the Orca bridge last learned of the computer's bots for the session list (orcabots.go); nil
	// while it runs no bridge or knows of no bot with a listed Home Chat.
	orcaBots atomic.Pointer[orcaBotHomes]
	// cindy runs the Cindy bridge while the machine's engine is Cindy (cindybridge.go).
	cindy cindyBridgeState
	// t3code runs the T3 Code bridge while the machine's engine is T3 Code (t3codebridge.go).
	t3code t3codeBridgeState
	// agy runs the Antigravity CLI companion when agy is installed and autoCompanions is enabled (agybridge.go).
	agy agyBridge
	// opencode runs the opencode ACP companion when opencode is installed and autoCompanions is enabled (opencodebridge.go).
	opencode       opencodeBridge
	autoCompanions bool
}

// NewRuntimeHost is a runtime socket at path (RuntimeSocketPath when empty) for a resident node; Attach starts it.
func NewRuntimeHost(version, path string) *RuntimeHost {
	if path == "" {
		path = RuntimeSocketPath()
	}
	rh := &RuntimeHost{reg: newRuntimeRegistry(), path: path, version: version,
		logger: log.New(os.Stderr, "node: ", log.LstdFlags|log.Lmsgprefix)}
	rh.reg.logf = rh.logger.Printf
	return rh
}

// current is the Node serving the hub now, nil between connections.
func (rh *RuntimeHost) current() *Node {
	rh.mu.Lock()
	defer rh.mu.Unlock()
	return rh.node
}

// attach makes n the Node serving the hub: it lists and drives the runtimes' sessions from now on. What the last hub
// connection watched is forgotten (the new one watches afresh); runtimes hear that a hub is there.
func (rh *RuntimeHost) attach(n *Node) {
	rh.mu.Lock()
	if n.runtimes != rh.reg {
		n.runtimes = rh.reg
	}
	if n.runtimeHost != rh {
		n.runtimeHost = rh
	}
	if rh.node != n {
		rh.reg.clearAssistantPIDs()
	}
	rh.node = n
	rh.hubSeq++
	rh.hubEpoch++
	seq, epoch := rh.hubSeq, rh.hubEpoch
	rh.reg.setHostOff(n.engineHostOff)
	rh.reg.resetWatches()
	if gp := n.existingGates(); gp != nil {
		rh.gp = gp
	}
	rh.mu.Unlock()
	rh.notifyHub(seq, epoch, true)
}

// detach: n no longer serves the hub (its connection ended). A newer Node may have attached already: then nothing.
func (rh *RuntimeHost) detach(n *Node) {
	rh.mu.Lock()
	if rh.node != n {
		rh.mu.Unlock()
		return
	}
	if gp := n.existingGates(); gp != nil {
		rh.gp = gp
	}
	rh.node = nil
	rh.hubSeq++
	seq, epoch := rh.hubSeq, rh.hubEpoch
	rh.reg.clearAssistantPIDs()
	rh.reg.resetWatches()
	rh.mu.Unlock()
	rh.notifyHub(seq, epoch, false)
}

// runtimeLeaseDisconnectGrace keeps session-scoped gate proxy leases alive across a brief non-overlapping socket
// disconnect so a surviving companion process (runtime-sdk RETRY_PRESENT = 1s) reconnecting with the same instance
// keeps its live Assistant workers' proxy URLs working, while still reclaiming leases if it never returns.
const runtimeLeaseDisconnectGrace = 15 * time.Second

type pendingLeaseRelease struct {
	instance string
	sessions []string
	timer    *time.Timer
}

func (rh *RuntimeHost) leaseGrace() time.Duration {
	if rh.leaseDisconnectGrace > 0 {
		return rh.leaseDisconnectGrace
	}
	return runtimeLeaseDisconnectGrace
}

func (rh *RuntimeHost) releaseSessionGates(sessions []string) {
	if len(sessions) == 0 {
		return
	}
	rh.mu.Lock()
	n, gp := rh.node, rh.gp
	rh.mu.Unlock()
	if cur := n.existingGates(); cur != nil {
		cur.releaseSessions(sessions)
	} else if gp != nil {
		gp.releaseSessions(sessions)
	}
}

func mergeUniqueStrings(a, b []string) []string {
	out := make([]string, 0, len(a)+len(b))
	seen := make(map[string]struct{}, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, s := range list {
			if s == "" {
				continue
			}
			if _, ok := seen[s]; ok {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	return out
}

func (rh *RuntimeHost) scheduleSessionGateRelease(h *runtimeConn, procID osproc.ProcIdentity, released []string) {
	if len(released) == 0 {
		return
	}
	h.mu.Lock()
	id, instance, pid := h.hello.Runtime, h.hello.Instance, h.pid
	h.mu.Unlock()
	dead := pid > 0 && procID.Start != "" && !procID.StillIs(pid)

	var immediate []string
	rh.mu.Lock()
	if id == "" || rh.closed {
		immediate = released
	} else if activeInst, connected := rh.reg.activeInstance(id); connected {
		// Another connection for id is already active: same-instance reconnect preserves leases, and
		// different-instance takeover already handled replacement in onRuntimeHello.
		_ = activeInst
	} else if dead {
		immediate = released
		if prev := rh.pendingReleases[id]; prev != nil {
			prev.timer.Stop()
			delete(rh.pendingReleases, id)
			immediate = mergeUniqueStrings(prev.sessions, immediate)
		}
	} else {
		if rh.pendingReleases == nil {
			rh.pendingReleases = map[string]*pendingLeaseRelease{}
		}
		sessions := released
		if prev := rh.pendingReleases[id]; prev != nil {
			prev.timer.Stop()
			if prev.instance != "" && instance != "" && prev.instance != instance {
				immediate = prev.sessions
			} else {
				sessions = mergeUniqueStrings(prev.sessions, sessions)
			}
		}
		entry := &pendingLeaseRelease{instance: instance, sessions: sessions}
		grace := rh.leaseGrace()
		entry.timer = time.AfterFunc(grace, func() { rh.expirePendingLeaseRelease(id, entry) })
		rh.pendingReleases[id] = entry
	}
	rh.mu.Unlock()
	rh.releaseSessionGates(immediate)
}

func (rh *RuntimeHost) expirePendingLeaseRelease(id string, entry *pendingLeaseRelease) {
	rh.mu.Lock()
	if rh.pendingReleases[id] != entry {
		rh.mu.Unlock()
		return
	}
	if activeInst, connected := rh.reg.activeInstance(id); connected && (activeInst == "" || entry.instance == "" || activeInst == entry.instance) {
		delete(rh.pendingReleases, id)
		rh.mu.Unlock()
		return
	}
	delete(rh.pendingReleases, id)
	sessions := rh.reg.unownedSessions(entry.sessions)
	rh.mu.Unlock()
	rh.releaseSessionGates(sessions)
}

func (rh *RuntimeHost) onRuntimeHello(id, instance string, replacedSessions []string) {
	var toRelease []string
	rh.mu.Lock()
	if prev := rh.pendingReleases[id]; prev != nil {
		prev.timer.Stop()
		delete(rh.pendingReleases, id)
		if prev.instance != "" && instance != "" && prev.instance != instance {
			toRelease = append(toRelease, prev.sessions...)
		}
	}
	rh.mu.Unlock()
	toRelease = mergeUniqueStrings(toRelease, replacedSessions)
	rh.releaseSessionGates(toRelease)
}

func (rh *RuntimeHost) cancelPendingLeaseReleases() {
	rh.mu.Lock()
	rh.closed = true
	for id, entry := range rh.pendingReleases {
		entry.timer.Stop()
		delete(rh.pendingReleases, id)
	}
	rh.mu.Unlock()
}

// notifyHub schedules per-connection ordered runtime.hub notifications without blocking
// healthy runtimes behind a slow/wedged peer and without holding rh.mu across writes.
func (rh *RuntimeHost) notifyHub(seq, epoch uint64, connected bool) {
	for _, h := range rh.reg.ready() {
		h.enqueueHub(seq, epoch, connected)
	}
}

// releaseHubAfterHello unblocks per-connection runtime.hub delivery once the runtime.hello
// reply has been written to the wire, reconciling any attach/detach that raced with hello.
func (rh *RuntimeHost) releaseHubAfterHello(h *runtimeConn) {
	rh.mu.Lock()
	seq, epoch, connected := rh.hubSeq, rh.hubEpoch, rh.node != nil
	rh.mu.Unlock()
	h.mu.Lock()
	if h.hubHolders > 0 {
		h.hubHolders--
	}
	var start bool
	if h.ready && h.hubHolders == 0 {
		if seq > h.hubTargetSeq {
			h.hubTargetSeq = seq
			h.hubTargetEpoch = epoch
			h.hubTargetConn = connected
		}
		start = h.tryStartHubDrainLocked()
	}
	h.mu.Unlock()
	if start {
		go h.drainHubNotifications()
	}
}

func (h *runtimeConn) enqueueHub(seq, epoch uint64, connected bool) {
	if h == nil || h.peer == nil {
		return
	}
	h.mu.Lock()
	if h.hubClosed || seq <= h.hubTargetSeq {
		h.mu.Unlock()
		return
	}
	h.hubTargetSeq = seq
	h.hubTargetEpoch = epoch
	h.hubTargetConn = connected
	start := h.tryStartHubDrainLocked()
	h.mu.Unlock()
	if start {
		go h.drainHubNotifications()
	}
}

// nextHubNotificationLocked returns the next runtime.hub boolean to write on h.
// If h was last told connected=true for an earlier hub epoch and the latest target is
// connected=true for a newer hub epoch, it emits connected=false first so edge-triggered
// runtime SDKs observe the disconnect edge and re-report assistant PIDs. Caller holds h.mu.
func (h *runtimeConn) nextHubNotificationLocked() (connected bool, send bool) {
	if !h.ready || h.hubHolders > 0 || h.hubClosed || h.peer == nil {
		return false, false
	}
	if !h.hubStateInit {
		return h.hubTargetConn, true
	}
	if h.hubLastConnected == h.hubTargetConn && (!h.hubLastConnected || h.hubLastEpoch == h.hubTargetEpoch) {
		return false, false
	}
	if h.hubLastConnected && h.hubTargetConn && h.hubLastEpoch != h.hubTargetEpoch {
		return false, true
	}
	return h.hubTargetConn, true
}

func (h *runtimeConn) tryStartHubDrainLocked() bool {
	if h.hubNotifying || !h.ready || h.hubHolders > 0 || h.hubClosed || h.peer == nil {
		return false
	}
	if _, send := h.nextHubNotificationLocked(); !send {
		return false
	}
	h.hubNotifying = true
	return true
}

func (h *runtimeConn) drainHubNotifications() {
	done := false
	defer func() {
		if !done {
			h.mu.Lock()
			h.hubNotifying = false
			h.mu.Unlock()
		}
	}()
	for {
		h.mu.Lock()
		connected, send := h.nextHubNotificationLocked()
		if !send {
			h.hubNotifying = false
			done = true
			h.mu.Unlock()
			return
		}
		sentEpoch := h.hubTargetEpoch
		if !connected && h.hubStateInit && h.hubLastConnected && h.hubTargetConn && h.hubLastEpoch != h.hubTargetEpoch {
			sentEpoch = h.hubLastEpoch
		}
		peer := h.peer
		h.mu.Unlock()

		if err := peer.Notify("runtime.hub", map[string]any{"connected": connected}); err != nil {
			h.mu.Lock()
			h.hubNotifying = false
			done = true
			h.mu.Unlock()
			return
		}

		h.mu.Lock()
		h.hubStateInit = true
		h.hubLastConnected = connected
		h.hubLastEpoch = sentEpoch
		h.mu.Unlock()
	}
}

// Listen serves runtimes on the socket until ctx ends, re-creating the socket file if something removes it (an older
// node shutting down), as the hook socket does. It dials the daemons in daemons.json beside the socket too.
func (rh *RuntimeHost) Listen(ctx context.Context) error {
	rh.ctx = ctx
	if _, err := rh.listenOnce(ctx); err != nil {
		rh.ctx = nil
		return err
	}
	rh.listenPipe(ctx)
	rh.startDaemons(ctx)
	if rh.autoCompanions {
		rh.setAgyBridge(true)
		rh.setOpencodeBridge(true)
	}
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				rh.cancelPendingLeaseReleases()
				return
			case <-t.C:
			}
			if _, err := os.Stat(rh.path); os.IsNotExist(err) {
				rh.logger.Printf("runtime socket vanished, re-creating")
				if _, err := rh.listenOnce(ctx); err != nil {
					rh.logger.Printf("runtime socket: %v", err)
				}
			}
		}
	}()
	return nil
}

// dialPath is where a runtime the node starts connects: the named pipe on Windows, whose Node.js cannot reach a Unix
// socket (runtimepipe_windows.go), else the socket.
func (rh *RuntimeHost) dialPath() string {
	if rh.pipe != "" {
		return rh.pipe
	}
	return rh.path
}

// mcpConfig is the node's mcp.json, beside the runtime socket. It follows rh.path, never dialPath: on Windows that is
// a pipe name, whose directory is \\.\pipe.
func (rh *RuntimeHost) mcpConfig() string {
	return filepath.Join(filepath.Dir(rh.path), "mcp.json")
}

func (rh *RuntimeHost) listenOnce(ctx context.Context) (net.Listener, error) {
	_ = os.Remove(rh.path)
	ln, err := osproc.Listen("unix", rh.path)
	if err != nil {
		return nil, err
	}
	ln.(*osproc.UnixListener).SetUnlinkOnClose(false) // by then the path may belong to a newer node
	_ = os.Chmod(rh.path, 0o600)
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	go acceptloop.Run(ctx, ln, rh.logger.Printf, func(c net.Conn) { go rh.serve(ctx, c) })
	return ln, nil
}

// serve runs one runtime's connection until it ends; its sessions leave the list with it.
func (rh *RuntimeHost) serve(ctx context.Context, c net.Conn) {
	rh.serveConn(ctx, c, "")
}

// serveConn is serve for any stream: a socket connection, or a daemon's link (daemon: its id in daemons.json).
func (rh *RuntimeHost) serveConn(ctx context.Context, c io.ReadWriteCloser, daemon string) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	defer c.Close()
	peer := rpc.New("r", c)
	peer.TrackReplies = true
	pid := 0
	var procID osproc.ProcIdentity
	if nc, ok := c.(net.Conn); ok {
		pid, _ = peerPID(nc) // 0 for a pipe, or where the system does not tell
	}
	// This node or an ancestor of it is a relay, not the runtime's own process: the manager's egress proxy leads an
	// extension's sandbox here (extmgr/extension_sandbox.go), and the manager may run this node, so everything the node
	// runs would descend from it.
	if pid > 0 && slices.Contains(nodeLineage(), pid) {
		pid = 0
	}
	if pid > 0 {
		procID = osproc.Identify(pid)
	}
	h := rh.reg.add(peer)
	h.mu.Lock()
	h.drop = cancel
	h.daemon = daemon
	h.pid = pid
	h.mu.Unlock()
	peer.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		return rh.request(ctx, h, method, params)
	}
	peer.OnEvent = func(event string, params json.RawMessage) { rh.event(h, event, params) }
	err := peer.Serve(ctx, c)
	peer.Close(err)
	cancel()
	released := rh.reg.remove(h)
	h.mu.Lock()
	h.hubClosed = true
	h.mu.Unlock()
	rh.scheduleSessionGateRelease(h, procID, released)
	if id := h.id(); id != "" {
		if id == agyRuntimeID {
			rh.wakeAgyBridge()
		}
		if h.isOpencodeRuntime() {
			rh.wakeOpencodeBridge()
		}
		rh.logger.Printf("runtime %s #%d left: %v", id, h.serial, err)
	}
	if n := rh.current(); n != nil {
		n.notifySessions()
		if rh.autoCompanions && n.peer != nil {
			_ = n.peer.Notify("launchers.changed", map[string]any{})
		}
	}
}

func (rh *RuntimeHost) registerRuntime(ctx context.Context, h *runtimeConn, hello runtimeHello) error {
	var preempt func(*runtimeConn, runtimeHello) bool
	isBuiltinAgy := false
	if hello.Runtime == agyRuntimeID && !h.isDaemon() {
		instance := rh.builtinAgyInstance()
		isBuiltinAgy = instance != "" && hello.Instance == instance
		if instance != "" && hello.Instance != instance {
			if !rh.agy.takeover.TryLock() {
				return &rpc.Error{Code: "runtime_conflict", Message: "another agy runtime is taking over"}
			}
			defer rh.agy.takeover.Unlock()
			if builtin, external := rh.agyPeers(instance); builtin != nil && !external {
				builtin.peer.SuspendCalls()
				defer builtin.peer.ResumeCalls()
				grace := rh.agy.yieldGrace
				if grace == 0 {
					grace = agyYieldGrace
				}
				drain, cancel := context.WithTimeout(ctx, grace)
				builtin.waitAgyIdle(drain)
				cancel()
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			preempt = func(old *runtimeConn, _ runtimeHello) bool {
				old.mu.Lock()
				defer old.mu.Unlock()
				return old.hello.Instance == instance
			}
		}
	}
	var yieldedOpencode *runtimeConn
	if !h.isDaemon() && isOpencodeRuntimeHello(hello) {
		instance := rh.builtinOpencodeInstance()
		isBuiltinOpencode := hello.Runtime == opencodeRuntimeID && instance != "" && hello.Instance == instance
		if instance != "" && !isBuiltinOpencode {
			if !rh.opencode.takeover.TryLock() {
				return &rpc.Error{Code: "runtime_conflict", Message: "another opencode runtime is taking over"}
			}
			defer rh.opencode.takeover.Unlock()
			if builtin, external := rh.opencodePeers(instance); builtin != nil && !external {
				builtin.peer.SuspendCalls()
				defer builtin.peer.ResumeCalls()
				grace := rh.opencode.yieldGrace
				if grace == 0 {
					grace = opencodeYieldGrace
				}
				drain, cancel := context.WithTimeout(ctx, grace)
				builtin.waitAgyIdle(drain)
				cancel()
				if err := ctx.Err(); err != nil {
					return err
				}
				yieldedOpencode = builtin
			}
			if hello.Runtime == opencodeRuntimeID {
				preempt = func(old *runtimeConn, _ runtimeHello) bool {
					old.mu.Lock()
					defer old.mu.Unlock()
					return old.hello.Instance == instance
				}
			}
		}
	}
	stale, replacedSessions, err := rh.reg.registerHello(h, hello, rh.reservedID, preempt)
	if err != nil {
		return err
	}
	rh.onRuntimeHello(hello.Runtime, hello.Instance, replacedSessions)
	if yieldedOpencode != nil && !slices.Contains(stale, yieldedOpencode) {
		stale = append(stale, yieldedOpencode)
	}
	h.mu.Lock()
	h.builtinAgy = isBuiltinAgy
	h.mu.Unlock()
	for _, old := range stale {
		old.mu.Lock()
		drop := old.drop
		old.mu.Unlock()
		old.peer.Close(errors.New("the runtime reconnected"))
		if drop != nil {
			drop()
		}
	}
	if hello.Runtime == agyRuntimeID {
		rh.wakeAgyBridge()
	}
	if !h.isDaemon() && isOpencodeRuntimeHello(hello) {
		rh.noteOpencodeHello(hello)
		rh.wakeOpencodeBridge()
	}
	return nil
}

// request answers a runtime's call.
func (rh *RuntimeHost) request(ctx context.Context, h *runtimeConn, method string, params json.RawMessage) (any, error) {
	switch method {
	case "runtime.hello":
		var hello runtimeHello
		if err := json.Unmarshal(params, &hello); err != nil {
			return nil, fmt.Errorf("bad runtime.hello: %w", err)
		}
		hello.Runtime = strings.TrimSpace(hello.Runtime)
		if hello.Runtime == "" || !runtimeIDRe.MatchString(hello.Runtime) {
			return nil, errors.New("runtime.hello needs a runtime id ([A-Za-z0-9][A-Za-z0-9_.-]*)")
		}
		hello.Name = strings.TrimSpace(hello.Name)
		if hello.Agent = strings.TrimSpace(hello.Agent); hello.Agent != "" && !runtimeIDRe.MatchString(hello.Agent) {
			return nil, fmt.Errorf("runtime.hello: bad agent %q", hello.Agent)
		}
		hello.Instance = strings.TrimSpace(hello.Instance)
		hello.Launchers = cleanLaunchers(hello.Launchers)
		h.mu.Lock()
		h.hubHolders++
		h.mu.Unlock()
		if err := rh.registerRuntime(ctx, h, hello); err != nil {
			rh.releaseHubAfterHello(h)
			rh.logger.Printf("runtime %s #%d refused: %v", hello.Runtime, h.serial, err)
			return nil, err
		}
		rh.logger.Printf("runtime %s %s #%d connected", hello.Runtime, hello.Version, h.serial)
		rh.claimCodex(h, hello.CodexClaims)
		if rh.autoCompanions && (hello.Launcher != "" || len(hello.Launchers) > 0) {
			if n := rh.current(); n != nil && n.peer != nil {
				_ = n.peer.Notify("launchers.changed", map[string]any{})
			}
		}
		// features: what this node does beyond the first protocol. prefer_running: when two runtimes list a session, the
		// one running it answers for it (runtimes_registry.go recomputeOwners), so a runtime may list sessions it only
		// has on disk.
		// resume_argv: a session listed offline with resume_argv is resumed in the terminal engine (pi.go).
		// handover: a send refused with the handover code goes to the runtime that takes the session over (handOver).
		// transcript: a session listed with a Claude transcript has its content read from it here (runtimetranscript.go).
		// codex_thread: a session listed with the Codex thread it is has that thread listed once (runtimecodex.go).
		// unlisted_transcripts: the transcripts a list names as the runtime's own sessions it no longer lists stay among
		// the sessions not running (runtimeunlisted.go).
		// launchers: hello's launchers and runtime.launchers are offered; start_params: session.start carries the
		// launcher picked and the form's model, permission mode, effort and space; modes: session.modes reaches a
		// runtime with caps.modes; resume: a runtime with caps.resume that is the machine's engine is asked to take
		// sessions over (runtimecontrol.go). workspaces: runtime.workspaces is kept and, for a runtime with
		// caps.workspaces that is the machine's engine, listed as its catalog and asked to make worktrees
		// (runtimeworkspaces.go). codex_claims: hello's codex_claims name Codex app-server instances the node leaves
		// to the runtime (codex_instance_claims.go), a runtime on the socket's only.
		features := []string{"prefer_running", "resume_argv", "handover", "transcript", "launchers", "start_params", "modes", "resume", "codex_thread", "unlisted_transcripts", "workspaces", "usage"}
		if !h.isDaemon() {
			features = append(features, "usage_windows", "leader_tools", "supervision_policy", "a2a_mcp", "remote_agent", "memory_tools", "codex_claims", "input")
			if hello.Caps.Assistant {
				features = append(features, "assistant_pids")
				if hello.Caps.AssistantEnvironment && !hello.Caps.RemoteAgent {
					features = append(features, "assistant_command_gate")
				}
			}
		}
		offer := rh.packageOffer(h, hello)
		rh.mu.Lock()
		helloSeq, helloEpoch, helloHub := rh.hubSeq, rh.hubEpoch, rh.node != nil
		rh.mu.Unlock()
		h.mu.Lock()
		h.hubStateInit = true
		h.hubLastConnected = helloHub
		h.hubLastEpoch = helloEpoch
		if helloSeq > h.hubTargetSeq {
			h.hubTargetSeq = helloSeq
			h.hubTargetEpoch = helloEpoch
			h.hubTargetConn = helloHub
		}
		h.mu.Unlock()
		if !rpc.AfterReply(ctx, func() { rh.releaseHubAfterHello(h) }) {
			rh.releaseHubAfterHello(h)
		}
		res := map[string]any{"protocol": RuntimeProtocol, "node_version": rh.version, "hub": helloHub,
			"features": features}
		if offer != nil {
			res["update"] = offer
		}
		return res, nil
	case "runtime.package":
		return rh.servePackage(h, params)
	case "runtime.a2a_mcp":
		return rh.a2aMCPToken(h, params)
	case "interaction.create":
		n := rh.current()
		if n == nil { // no hub to put it in front of the phone: ask again (same id) once one is connected
			return nil, &rpc.Error{Message: "no hub connected", Code: "node_gone"}
		}
		return n.runtimeAsk(ctx, h, params)
	case "mcp.tools":
		n := rh.current()
		if n == nil || n.peer == nil {
			return map[string]any{"tools": []proto.Tool{}}, nil
		}
		// The assistant's tools (ask_user, schedule_followup…) only for a runtime that runs the assistant, with the
		// command gates' tools among them (assistant_runtime.go).
		var p struct {
			Assistant  bool   `json:"assistant"`
			LeaderOnly bool   `json:"leader_only"`
			SessionID  string `json:"session_id"`
		}
		_ = json.Unmarshal(params, &p)
		if p.SessionID != "" && !rh.reg.owns(h, p.SessionID) || h.isDaemon() && p.SessionID == "" {
			return map[string]any{"tools": []proto.Tool{}}, nil
		}
		assistant := p.Assistant && h.assistant()
		var res struct {
			Tools []proto.Tool `json:"tools"`
		}
		query := map[string]any{"assistant": assistant}
		if p.SessionID != "" {
			query["session_id"] = p.SessionID
			query["guest"] = n.guestOf(p.SessionID) != nil
		}
		if p.LeaderOnly {
			query["leader_only"] = true
		}
		if err := n.peer.Call(ctx, "mcp.tools", query, &res); err != nil {
			return nil, err
		}
		if assistant && !p.LeaderOnly {
			res.Tools = append(res.Tools, n.gateTools()...)
			if h.remoteAgent() {
				if p.SessionID == "" {
					res.Tools = append(res.Tools, n.memoryTools()...)
				} else if _, ok := n.sessionAllowsMemoryTools(p.SessionID); ok {
					res.Tools = append(res.Tools, n.memoryTools()...)
				}
			}
		}
		if h.isDaemon() {
			res.Tools = slices.DeleteFunc(res.Tools, func(tool proto.Tool) bool { return !assistantwire.IsA2ATool(tool.Name) })
		}
		if res.Tools == nil {
			res.Tools = []proto.Tool{}
		}
		return res, nil
	case "mcp.call":
		n := rh.current()
		if n == nil || n.peer == nil {
			return proto.TextResult("Persona hub is not connected", true), nil
		}
		var p struct {
			Name      string          `json:"name"`
			Args      json.RawMessage `json:"args"`
			SessionID string          `json:"session_id"`
			Assistant bool            `json:"assistant"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("bad mcp.call: %w", err)
		}
		if h.isDaemon() && !assistantwire.IsA2ATool(p.Name) {
			return nil, errUnsupportedForDaemons("non-A2A tools")
		}
		// As the assistant only for one of the assistant's conversations that this runtime runs.
		assistant := p.Assistant && h.assistant() && rh.reg.owns(h, p.SessionID) && n.isAssistantSession(p.SessionID)
		if !assistant && slices.Contains(callerTools, p.Name) && !rh.reg.owns(h, p.SessionID) {
			return proto.TextResult("Persona Agent tool requires a session owned by this runtime", true), nil
		}
		if p.Name == toolMailSaveAttachment { // writes on this machine (mailattachment.go)
			return n.saveMailAttachment(ctx, p.Args, p.SessionID, assistant, n.guestOf(p.SessionID) != nil), nil
		}
		if g := n.gateTool(p.Name); assistant && g != nil {
			if h.remoteAgent() {
				rh.reg.markRemote(p.SessionID)
				n.gates().markRemote(p.SessionID)
			}
			return n.assistantGateTool(ctx, g, p.SessionID, p.Args), nil
		}
		if isMemoryTool(p.Name) {
			if !h.remoteAgent() || !assistant {
				return proto.TextResult("Memory tools require a remote assistant session", true), nil
			}
			return n.assistantMemoryTool(ctx, p.Name, p.SessionID, p.Args), nil
		}
		var res json.RawMessage
		params := map[string]any{"name": p.Name, "args": p.Args, "session_id": p.SessionID, "assistant": assistant, "guest": n.guestOf(p.SessionID) != nil}
		if origin := n.assistantOrigin(p.SessionID); origin != "" {
			params["origin"] = origin
		}
		err := n.peer.Call(ctx, "mcp.call", params, &res)
		if err == nil {
			res = n.toolViews.stripRaw(p.SessionID, p.Name, res)
		}
		return res, err
	}
	return nil, fmt.Errorf("unknown method %s", method)
}

// event takes a runtime's push: its list is kept whether or not a hub is connected; the rest goes to the hub, or
// nowhere between connections.
func (rh *RuntimeHost) event(h *runtimeConn, event string, params json.RawMessage) {
	h.mu.Lock()
	ready := h.ready
	h.mu.Unlock()
	if !ready {
		return // hello first
	}
	if event == "runtime.assistant_pids" {
		var p struct {
			PIDs map[string]int `json:"pids"`
		}
		if json.Unmarshal(params, &p) == nil {
			rh.mu.Lock()
			defer rh.mu.Unlock()
			n := rh.node
			if n == nil {
				p.PIDs = nil
			}
			rh.reg.setAssistantPIDs(h, p.PIDs, func(id string) bool { return n != nil && n.runtimeAssistantChat(h, id) })
		}
		return
	}
	n := rh.current()
	if event == "runtime.launchers" { // its launchers changed (runtimecontrol.go)
		var p struct {
			Launchers []runtimeLauncher `json:"launchers"`
		}
		if json.Unmarshal(params, &p) == nil {
			h.setLaunchers(cleanLaunchers(p.Launchers))
			if rh.autoCompanions && n != nil && n.peer != nil {
				_ = n.peer.Notify("launchers.changed", map[string]any{})
			}
		}
		return
	}
	if event == "session.compaction.settled" {
		var p struct {
			SessionID string `json:"session_id"`
			Error     string `json:"error"`
		}
		if json.Unmarshal(params, &p) == nil && p.SessionID != "" {
			rh.reg.settleCompaction(h, p.SessionID, p.Error)
		}
		return
	}
	if event == "runtime.workspaces" { // its own workspaces (runtimeworkspaces.go): only from one that keeps them
		var p struct {
			Workspaces []runtimeWorkspace `json:"workspaces"`
		}
		if h.makesWorkspaces() && json.Unmarshal(params, &p) == nil {
			h.setWorkspaces(cleanWorkspaces(p.Workspaces))
		}
		return
	}
	if event == "runtime.sessions" {
		var p struct {
			Sessions []proto.Session `json:"sessions"`
		}
		// Decoded into one slice the size of the last list (a few rows to spare), which normalizeRuntimeSessions keeps
		// in place: not grown row by row from nil and then copied once more.
		p.Sessions = make([]proto.Session, 0, h.listLen()+4)
		if json.Unmarshal(params, &p) != nil {
			return
		}
		var moved []string
		var agyPaneBackground map[string][]proto.BackgroundTask
		var waits map[runtimeInputKey]runtimeInputReport
		if !h.isDaemon() { // a daemon's offline sessions are not resumed in this machine's terminal engine
			if h.reportsInput() {
				waits = runtimeInputReports(params)
			}
			x := runtimeRowExtras(params, len(p.Sessions))
			if h.id() == agyRuntimeID {
				agyPaneBackground = agyPaneBackgroundFromRows(params)
			}
			h.setResume(x.resume)
			// …nor read from this machine's transcripts: a daemon names none (runtimetranscript.go).
			ts := x.transcripts
			if n != nil {
				ts = n.foundTranscripts(ts)
			}
			moved = h.setTranscripts(ts)
			h.setCodexThreads(x.threads) // …nor are its Codex threads this machine's (runtimecodex.go)
			if x.unlistedSaid && rh.reg.setUnlisted(h, x.unlisted) {
				forgetOfflineScan() // …nor its past sessions' transcripts (runtimeunlisted.go): the next list reads them again
			}
		}
		list := normalizeRuntimeSessions(h, p.Sessions)
		if cap(list) > 2*len(list)+8 { // the list shrank: keep no array of the old size
			list = append(make([]proto.Session, 0, len(list)), list...)
		}
		rewatch, driven := rh.reg.setSessionList(h, list, agyPaneBackground, waits)
		for _, id := range driven { // started for the hub by another runtime: its cards go to the phone first here too
			if h.peer != nil {
				_ = h.peer.Notify("runtime.driven", map[string]any{"session_id": id})
			}
		}
		if n == nil {
			return
		}
		n.observeAgySessions(h, list)
		for _, id := range moved {
			n.transcriptMoved(h, id)
		}
		for id, attempt := range rewatch {
			go func() {
				if err := n.runtimeWatch(h, id, attempt.since); err != nil {
					n.runtimes.watchFailed(h, id, attempt.token)
				}
			}()
		}
		n.publishSessionsSoon()
		return
	}
	if n != nil {
		n.runtimeEvent(h, event, params)
	}
}

// rowExtras is what a runtime.sessions push says beyond the rows the apps see.
type rowExtras struct {
	resume      map[string][]string // session → resume_argv (resumeArgvs)
	transcripts map[string]string   // session → the Claude transcript its conversation is (runtimeTranscripts)
	threads     map[string]string   // session → the Codex thread its conversation is (runtimeCodexThreads)
	// Claude transcripts of sessions of its own it does not list (unlistedTranscripts), when the push says: one
	// without the field leaves what the runtime named before.
	unlisted     map[string]bool
	unlistedSaid bool
}

// runtimeRowExtras is resumeArgvs, runtimeTranscripts, runtimeCodexThreads and unlistedTranscripts of a
// runtime.sessions push, read in one pass rather than one each, its rows into a slice of rows rows (the size the
// push's sessions were decoded into, so it is not grown row by row). A push one of them cannot read (a field of the
// wrong type) has each read on its own, as before: the others may still read.
func runtimeRowExtras(params json.RawMessage, rows int) rowExtras {
	type extras struct {
		SessionID   string   `json:"session_id"`
		ResumeArgv  []string `json:"resume_argv"`
		Transcript  string   `json:"transcript"`
		CodexThread string   `json:"codex_thread"`
	}
	var p struct {
		Sessions []extras `json:"sessions"`
		Unlisted []string `json:"unlisted_transcripts"`
	}
	p.Sessions = make([]extras, 0, rows)
	if json.Unmarshal(params, &p) != nil {
		unlisted, said := unlistedTranscripts(params)
		return rowExtras{resumeArgvs(params), runtimeTranscripts(params), runtimeCodexThreads(params), unlisted, said}
	}
	x := rowExtras{resume: map[string][]string{}, transcripts: map[string]string{}, unlisted: unlistedSet(p.Unlisted), unlistedSaid: p.Unlisted != nil}
	for _, s := range p.Sessions {
		if resumable(s.SessionID, s.ResumeArgv) {
			x.resume[s.SessionID] = s.ResumeArgv
		}
		if namesTranscript(s.SessionID, s.Transcript) {
			x.transcripts[s.SessionID] = s.Transcript
		}
		if namesCodexThread(s.SessionID, s.CodexThread) {
			if x.threads == nil { // few runtimes name one: no map until a row does
				x.threads = map[string]string{}
			}
			x.threads[s.SessionID] = s.CodexThread
		}
	}
	return x
}

// resumeArgvs reads the resume_argv of a runtime.sessions push: how the terminal engine resumes a session the runtime
// lists offline (a pi that ran in herdr, pi.go).
func resumeArgvs(params json.RawMessage) map[string][]string {
	var p struct {
		Sessions []struct {
			SessionID  string   `json:"session_id"`
			ResumeArgv []string `json:"resume_argv"`
		} `json:"sessions"`
	}
	if json.Unmarshal(params, &p) != nil {
		return nil
	}
	out := map[string][]string{}
	for _, s := range p.Sessions {
		if resumable(s.SessionID, s.ResumeArgv) {
			out[s.SessionID] = s.ResumeArgv
		}
	}
	return out
}

// resumable: argv is a resume_argv the node takes for session id. A command, then its arguments; bounded, no empty
// words.
func resumable(id string, argv []string) bool {
	if len(argv) == 0 || len(argv) > 16 || !runtimeIDRe.MatchString(id) {
		return false
	}
	size := 0
	for _, w := range argv {
		size += len(w)
		if w == "" || strings.ContainsAny(w, "\x00\n\r") {
			return false
		}
	}
	return size <= 4096
}

// packageOffer is the update a runtime gets in its hello reply (docs/integrations/runtimes.md "Auto-update"): this node's package of
// the name the runtime runs, when the hashes differ. A daemon gets none: its code is not this machine's to replace
// (its link does not pass the package on either).
func (rh *RuntimeHost) packageOffer(h *runtimeConn, hello runtimeHello) map[string]any {
	if hello.Package == nil || h.isDaemon() || !runtimepkg.NameRe.MatchString(hello.Package.Name) {
		return nil
	}
	pkg, err := runtimepkg.Load(hello.Package.Name)
	if err != nil {
		rh.logger.Printf("runtime %s: package %s: %v", hello.Runtime, hello.Package.Name, err)
		return nil
	}
	if pkg == nil || pkg.Hash == hello.Package.Hash {
		return nil
	}
	rh.logger.Printf("runtime %s #%d: offering package %s %s (it runs %s)", hello.Runtime, h.serial, pkg.Name, pkgHash(pkg.Hash), pkgHash(hello.Package.Hash))
	return map[string]any{"name": pkg.Name, "hash": pkg.Hash, "version": rh.version}
}

// servePackage answers runtime.package {name}: the files of this node's package, base64, with their hash.
func (rh *RuntimeHost) servePackage(h *runtimeConn, params json.RawMessage) (any, error) {
	if h.isDaemon() {
		return nil, errUnsupportedForDaemons("runtime packages")
	}
	if h.id() == "" {
		return nil, errors.New("runtime.package before runtime.hello")
	}
	var p struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(params, &p)
	if !runtimepkg.NameRe.MatchString(p.Name) {
		return nil, &rpc.Error{Message: fmt.Sprintf("bad package name %q", p.Name), Code: "bad_request"}
	}
	pkg, err := runtimepkg.Load(p.Name)
	if err != nil {
		return nil, err
	}
	if pkg == nil {
		return nil, &rpc.Error{Message: "this personad carries no package " + p.Name, Code: "not_found"}
	}
	files := make([]map[string]string, len(pkg.Files))
	for i, f := range pkg.Files {
		files[i] = map[string]string{"path": f.Path, "data": base64.StdEncoding.EncodeToString(f.Data)}
	}
	rh.logger.Printf("runtime %s #%d: sent package %s %s", h.id(), h.serial, pkg.Name, pkgHash(pkg.Hash))
	return map[string]any{"name": pkg.Name, "hash": pkg.Hash, "version": rh.version, "files": files}, nil
}

// pkgHash is the start of a package hash for the log; "none" for a runtime that has no package hash.
func pkgHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	if hash == "" {
		return "none"
	}
	return hash
}
