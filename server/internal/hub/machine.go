package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/streams"
)

var errOffline = apierr.ErrOffline

// Machine is what the hub knows of one of the user's computers: what the manager that carries it reports
// (devices.state, devicewire; hub/devices_link.go) and the node's events it passes on, with what is the hub's own
// beside it — the apps watching its sessions, the Cua / ego state, what the hub shows of its sessions. The hub does not
// connect it: the manager does (internal/devices), and a call goes there over the bus (busNode). Tests attach an
// in-process node as peer instead.
type Machine struct {
	cfg MachineConfig
	hub *Hub
	// named is what the computer is called now: cfg.Name until it is renamed (renameMachineLive, the manager's
	// report), which changes nothing else of it; read through name().
	named atomic.Pointer[string]
	// kindNow is how its manager reaches it now: cfg.Kind until a switch to Persona Link, or back, changed it
	// (link_switch.go), as its manager's next report says (applyDeviceStatesFrom); read through kind().
	kindNow atomic.Pointer[string]

	mu sync.Mutex
	// peer is how calls reach the node: the manager over the bus (busNode) for a machine it reported, or a test's
	// rpc.Peer.
	peer    nodeCaller
	streams *streams.Mux // the forwarded ports' connections on the node's current connection (ports.go)
	info    proto.NodeInfo
	online  bool
	// connectedAt: when the node's current connection began, as the manager says (sessions.changed carries it to the
	// assistant).
	connectedAt time.Time
	// engine is the engine the manager handed over at its last stop, shown until it reports (snap has it then).
	engine string
	// engineHosts: the set the last manager handed over (#2576), nil when it handed over none (a single engine then).
	engineHosts *proto.EngineChoice
	// warmUntil: shown as before a restart until then — the hub's, while the manager comes back (calls wait for it in
	// busNode), or the manager's, while the node comes back (its report's) (devices_warm.go).
	warmUntil time.Time
	err       string
	lastSeen  time.Time
	// sessions is the manager's last list. A list once kept is never changed in place, only replaced (a change copies
	// it first): keptSessions hands it out without a copy, to be read after m.mu is let go.
	sessions             []proto.Session
	deletedConversations map[string]proto.Session // Permanent manager tombstones, overlaid on delayed inventories; guarded by mu.
	responseMu           sync.Mutex
	responseFences       map[string]*conversationResponseFence
	listedOnce           bool // a list of its sessions came since the hub started (hadList)
	// lists decodes the manager's lists (fleet.sessions), only the sessions whose bytes changed (fleet_link.go).
	lists proto.SessionListDecoder
	// archived are the archived rows the manager archivedBy tells apart from its list (fleet.archived, #6836), in the
	// order told, and archivedAt each one's index by id. The apps never get them (Hub.shown leaves archived rows out);
	// everything else that read the manager's list when they were in it reads them too (session, sessionList,
	// keptWhole), so a row answers the same either way (a port it listens on, a lookup by id, a busy agent). Replaced
	// whole, never changed in place.
	archived   []proto.Session
	archivedAt map[string]int
	archivedBy string
	// raws are the bytes each session of the list rawsOf names came in, by id ("" for an id the list names twice),
	// when that list was decoded from them: a list kept any other way since has none (rawsFor). Never changed: a new
	// list gets a new map.
	raws   map[string]string
	rawsOf keptList
	// rows are the sessions' rows as last pushed, by id (encodeKept), under rowsMu; never changed in place.
	rowsMu    sync.Mutex
	rows      map[string]keptRow
	rowLen    atomic.Int64                   // the average length of the rows encodeKept gave last (rowsSize)
	watchers  map[string]int                 // session id -> subscribed clients
	liveTools map[string]proto.LiveToolState // pending invocations for watched sessions, guarded by mu
	// What the node was told about each session's watch (syncWatch): watchMu orders the session.watch /
	// session.unwatch calls per session (a 1-slot channel, so a wait for it can end with the caller's ctx), nodeWatch
	// is what the last of them left (absent: not watched). A slot goes when no call holds or waits for it and the
	// session is neither subscribed nor watched on the node (dropWatchSlot).
	watchMu   map[string]*watchSlot
	nodeWatch map[string]nodeWatchState
	// The web panels' open folders on this machine (fswatch.go): fswMu guards the fields below. One worker syncs the
	// node at a time, with fswPending coalescing changes made while it is out; fswForce resends even an empty set on a
	// new connection. fswSent says an empty set must still be sent (the node has a non-empty one, or a forced one
	// failed), fswWarned that a failure was logged.
	fswMu      sync.Mutex
	fswSent    bool
	fswWarned  bool
	fswRunning bool
	fswPending bool
	fswForce   bool

	lastSeq     map[string]int // session id -> latest cursor within its history
	lastHistory map[string]string
	// derived: a computer only a member hub has in hub.json; the center reaches it through that hub.
	derived bool

	// The manager's report (devices_reports.go): managed is set once one came for this machine (or the hub made it for
	// one the manager will report); snap is the machine as it said; connected, a call would reach the node now;
	// hookedAt, the connection the hub did its part for (devices.connected); paths, its ways for the admin page.
	managed bool
	// owner is the manager that carries it (ownership.go machineOwner) as of its last report or hand-over; "" before
	// either.
	owner     string
	snap      *proto.Machine
	connected bool
	hookedAt  time.Time
	paths     []pathInfo
}

// nodeCaller is how calls reach a node: *rpc.Peer (tests), busNode (the manager).
type nodeCaller interface {
	Call(ctx context.Context, method string, params, out any) error
	Notify(event string, params any) error
	Done() <-chan struct{}
	Close(err error)
}

// ownerID is the manager that reported m; "" before one did.
func (m *Machine) ownerID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.owner
}

// hasCapability: m's node announced capability c (nodeproto), as its manager reported it; a node from before the
// handshake (protocol 0) has none of the later ones, a test's (no version) all.
func (m *Machine) hasCapability(c string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.info.Protocol > 0 {
		return slices.Contains(m.info.Capabilities, c)
	}
	return m.info.Version == ""
}

// announces: m's node announced capability c, as its manager reported it. Unlike hasCapability, a node the hub has
// no word of (none reported since the hub started, or a test's) has none: for a check an older node would skip.
func (m *Machine) announces(c string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info.Protocol > 0 && slices.Contains(m.info.Capabilities, c)
}

// nodeReported: m's manager has reported m's node (its version or protocol) since the hub started. A devices manager
// that restarted without its warm file reports a zero node until the node connects again, and a hub that could not
// read its machines' entries has no report until the manager's first: until then announces says nothing of the node.
func (m *Machine) nodeReported() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.info.Protocol > 0 || m.info.Version != ""
}

// hadList: the hub has had a list of m's sessions since it started, its manager's or the one the last hub process
// handed over (startDevices). Before, a session it does not list may be one that list will have.
func (m *Machine) hadList() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.listedOnce
}

// ownerOrCarrier is m's owner, the carrier of hub.json's machines before one reported it; m.mu must not be held.
func (m *Machine) ownerOrCarrier() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cmp.Or(m.owner, devicesOwner)
}

func newMachine(h *Hub, cfg MachineConfig) *Machine {
	m := &Machine{cfg: cfg, hub: h, watchers: map[string]int{}, lastSeq: map[string]int{}, lastHistory: map[string]string{}}
	m.setName(cfg.Name)
	return m
}

// name is what the computer is called now: as renamed, else its entry's name, else its id.
func (m *Machine) name() string {
	if n := m.named.Load(); n != nil && *n != "" {
		return *n
	}
	return cmp.Or(m.cfg.Name, m.cfg.ID)
}

// setName renames the computer; "" leaves the name as it is.
func (m *Machine) setName(name string) {
	if name != "" {
		m.named.Store(&name)
	}
}

// kind is how the computer is reached now (its entry's kind, "" for ssh): cfg.Kind unless a switch changed it.
func (m *Machine) kind() string {
	if k := m.kindNow.Load(); k != nil {
		return *k
	}
	return m.cfg.Kind
}

// setKind takes the computer's entry's new kind (a switch from ssh to Persona Link, or back); nothing else changes.
func (m *Machine) setKind(kind string) { m.kindNow.Store(&kind) }

func (m *Machine) snapshot() proto.Machine {
	computer, browser, iphone := m.hub.extrasOf(m) // before m.mu: it takes it
	m.mu.Lock()
	defer m.mu.Unlock()
	warm := m.warmLocked(time.Now())
	var s proto.Machine
	if m.snap != nil {
		s = *m.snap
		s.Name = m.name() // renamed since it was reported: as it is called now
	} else {
		// Not reported yet (the hub just started, or a test's machine): what the hub has.
		icon := m.cfg.Icon
		if icon == "" {
			icon = "laptop"
		}
		s = proto.Machine{ID: m.cfg.ID, Name: m.name(), Kind: m.kind(), Icon: icon, SSHHost: m.cfg.SSHHost,
			Hostname: m.info.Hostname, OS: m.info.OS, Arch: m.info.Arch, Home: m.info.Home, HerdrVersion: m.info.HerdrVersion,
			ClaudeVersion: m.info.ClaudeVersion, LastSeen: m.lastSeen, NodeVersion: m.info.Version, Engine: cmp.Or(m.engine, proto.EngineHerdr),
			Tailcat: m.cfg.TailcatAddr != ""}
		s.Engines = []proto.Engine{}
		if m.online || warm {
			s.Engines = append(s.Engines, m.info.Engines...)
		}
		choice := proto.EngineHostsOf(s.Engine)
		if m.engineHosts != nil {
			choice = *m.engineHosts
		}
		s.EngineHosts, s.EngineDefault = choice.Hosts, choice.Default
		s.NodeMismatch = m.online && nodeMismatch(m.hub.version, m.info.Version)
		can, why := m.canDeployLocked()
		s.CanDeploy, s.DeployReason = &can, why
	}
	s.Online, s.Error = m.online || warm, m.err
	if m.lastSeen.After(s.LastSeen) { // the session lists the hub got since the manager's last report
		s.LastSeen = m.lastSeen
	}
	if s.Engines == nil {
		s.Engines = []proto.Engine{}
	}
	if s.EngineHosts == nil { // a manager older than sets: the set its single engine stands for
		c := proto.EngineHostsOf(cmp.Or(s.Engine, proto.EngineHerdr))
		s.EngineHosts, s.EngineDefault = c.Hosts, c.Default
	}
	s.Computer, s.Browser, s.IPhone = computer, browser, iphone // the system is in the manager's report (its monitoring)
	return s
}

// canDeployLocked is whether the manager can update the machine's node from here, before it said (its own rules,
// internal/devices deploy_rpc.go): the hub machine's goes with the hub; one reached over its link or connector needs a
// node that updates itself; an offline one waits.
func (m *Machine) canDeployLocked() (bool, string) {
	if m.cfg.Kind == "local" {
		return false, i18n.T(m.hub.userLang(), "hub-machine.deploy_local")
	}
	if kind := m.kind(); (kind == kindLink || kind == kindService || m.derived) && !m.info.NodeUpdate {
		if m.info.NodeUpdateReason != "" {
			return false, m.info.NodeUpdateReason
		}
		return false, i18n.T(m.hub.userLang(), "hub-machine.deploy_upgrade_first")
	}
	if !m.online {
		return false, i18n.T(m.hub.userLang(), "hub-machine.deploy_offline")
	}
	return true, ""
}

// reconnect has the manager connect the machine's node again.
func (m *Machine) reconnect() {
	caller := m.hub.managerCaller(m.ownerOrCarrier())
	if _, err := buswire.Call(context.Background(), caller, devicewire.ReconnectMethod, devicewire.ReconnectRequest{MachineID: m.cfg.ID}); err != nil {
		m.hub.logf("[%s] reconnect: %v", m.cfg.ID, err)
	}
}

func (m *Machine) call(ctx context.Context, method string, params any, out any) error {
	if err := m.deletedCall(method, params); err != nil {
		return err
	}
	m.mu.Lock()
	p := m.peer
	info := m.info
	m.mu.Unlock()
	if info.Version != "" && info.EngineHosts == nil && devicewire.IsSessionStart(method) && proto.StartHost(params) != "" {
		// An older node ignores host and would start in its engine instead (#2576): refused, nothing started.
		return &rpc.Error{Code: proto.CodeNodeOutdated, Message: i18n.T(m.hub.contextLang(ctx), "hub-machine.node_no_engine_hosts")}
	}
	if info.Version != "" && !info.ModelConfiguration && (method == "assistant.send" || method == "assistant.warm" || devicewire.IsSessionStart(method) || method == "session.send") {
		raw, _ := json.Marshal(params)
		var fields struct {
			Model       string `json:"model"`
			Fingerprint string `json:"model_fingerprint"`
		}
		_ = json.Unmarshal(raw, &fields)
		if fields.Model != "" || fields.Fingerprint != "" {
			return &rpc.Error{Code: proto.CodeNodeOutdated, Message: i18n.T(m.hub.contextLang(ctx), "hub-machine.node_no_model_config")}
		}
	}
	if p == nil {
		return fmt.Errorf("%s: %w", m.name(), errOffline)
	}
	select {
	case <-p.Done(): // closed, a new connection on its way: nothing was sent
		return fmt.Errorf("%s: %w", m.name(), errOffline)
	default:
	}
	if err := p.Call(ctx, method, params, out); err != nil {
		return err
	}
	return m.deletedCall(method, params)
}

// reachable: a call would go out now (call answers errOffline otherwise).
func (m *Machine) reachable() bool {
	m.mu.Lock()
	p := m.peer
	managed, connected := m.managed, m.connected
	owner := cmp.Or(m.owner, devicesOwner)
	m.mu.Unlock()
	if managed {
		return connected && m.hub.devicesUpFor(owner) // its own manager's state (devices_reports.go)
	}
	if p == nil {
		return false
	}
	select {
	case <-p.Done():
		return false
	default:
		return true
	}
}

// notify sends the node a one-way event; events arrive in the order sent.
func (m *Machine) notify(event string, params any) error {
	m.mu.Lock()
	p := m.peer
	m.mu.Unlock()
	if p == nil {
		return fmt.Errorf("%s: %w", m.name(), errOffline)
	}
	return p.Notify(event, params)
}

// sessionList is a copy of the manager's whole list: its list, then the archived rows it told apart (archived).
// What the apps get leaves archived rows out (Hub.shown) either way.
func (m *Machine) sessionList() []proto.Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]proto.Session, 0, len(m.sessions)+len(m.archived))
	return append(append(out, m.sessions...), m.archived...)
}

// keptSessions is the machine's list itself, not a copy: only to be read (Machine.sessions). The pushes read a busy
// machine's ~200 KB list on every change of one session; a copy each time was most of what they allocated.
func (m *Machine) keptSessions() []proto.Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions
}

// keptWhole is keptSessions and the archived rows told apart (archived), neither copied, only to be read: the
// manager's whole list, for what reads every row it listed rather than what the apps get (port access, busy agents,
// space names).
func (m *Machine) keptWhole() (list, archived []proto.Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions, m.archived
}

// session is session id as the manager listed it: in its list, else among its archived rows (archived).
func (m *Machine) session(id string) (proto.Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.sessions { // by index: a range value copies every (large) session it passes
		if m.sessions[i].SessionID == id {
			return m.sessions[i], true
		}
	}
	if i, ok := m.archivedAt[id]; ok {
		return m.archived[i], true
	}
	return proto.Session{}, false
}

// setArchived keeps list as the archived rows manager told apart (fleet.archived).
func (m *Machine) setArchived(manager string, list []proto.Session) {
	at := archivedIndex(list)
	m.mu.Lock()
	m.archived, m.archivedAt, m.archivedBy = list, at, manager
	m.mu.Unlock()
}

// archivedIndex is each row's index in list by id, the first for an id listed twice, as a lookup in the list finds it.
func archivedIndex(list []proto.Session) map[string]int {
	at := make(map[string]int, len(list))
	for i := range list {
		if _, ok := at[list[i].SessionID]; !ok {
			at[list[i].SessionID] = i
		}
	}
	return at
}

// archivedOnlyOf drops the archived rows unless manager told them: a list from another manager (the machine moved to
// one that tells them in its list) has its own.
func (m *Machine) archivedOnlyOf(manager string) {
	m.mu.Lock()
	if m.archivedBy != manager {
		m.archived, m.archivedAt, m.archivedBy = nil, nil, ""
	}
	m.mu.Unlock()
}

// watcherJoined counts one more app subscribed to session id, watcherLeft one fewer. Neither tells the node: the
// caller syncs the node's watch afterwards (syncWatch, syncUnwatch). An app's connection counts in its read loop, in
// the order its messages came, and syncs from a goroutine: the node may take up to watchCallTimeout to answer, and
// nothing else the app sends is read while its read loop waits (#5915). Counting cannot move to the goroutine
// too: a leave that ran ahead of its join would find nothing to take away, and the join would then stay for good.
func (m *Machine) watcherJoined(id string) {
	m.mu.Lock()
	m.watchers[id]++
	m.mu.Unlock()
}

func (m *Machine) watcherLeft(id string) {
	m.mu.Lock()
	m.watchers[id]--
	if m.watchers[id] <= 0 {
		delete(m.watchers, id)
		delete(m.liveTools, id)
	}
	m.mu.Unlock()
}

// syncUnwatch syncs session id's watch after an app left it, not on that app's connection, which may be gone.
func (m *Machine) syncUnwatch(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), watchCallTimeout)
	defer cancel()
	m.syncWatch(ctx, id)
}

// addWatcher is a join and its sync in one call, removeWatcher a leave and its sync: for a caller that may wait for
// the node, which a connection's read loop may not (watcherJoined).
func (m *Machine) addWatcher(ctx context.Context, id string) {
	m.watcherJoined(id)
	m.syncWatch(ctx, id)
}

func (m *Machine) removeWatcher(id string) {
	m.watcherLeft(id)
	m.syncUnwatch(id)
}

// setSessionList keeps list as the sessions the node has now, and drops the highest item seq of those it no longer
// lists that no app watches and the node was not told to watch: nothing will ask for their since_seq again. A
// session it no longer lists whose last session.watch / session.unwatch failed (nodeWatchUnknown) and that no app
// watches gets its unwatch sent again in the background, then its watch state goes (settleEnded).
func (m *Machine) setSessionList(list []proto.Session) { m.setSessionListRaws(list, nil) }

// setSessionListRaws is setSessionList of a list the manager's bytes were decoded into: raws[i] are the bytes list[i]
// came in (proto.SessionListDecoder), by which a push tells which sessions' rows it can send again (encodeKept); nil
// for a list that came another way.
func (m *Machine) setSessionListRaws(list []proto.Session, raws []string) {
	var newDeletions []string
	for _, row := range list {
		if row.IsDeleted() && !m.conversationDeleted(row.SessionID) {
			newDeletions = append(newDeletions, row.SessionID)
		}
	}
	slices.Sort(newDeletions)
	newDeletions = slices.Compact(newDeletions)
	for _, id := range newDeletions {
		defer m.lockConversationResponse(id)()
	}
	m.mu.Lock()
	for _, row := range list {
		if row.IsDeleted() {
			if m.deletedConversations == nil {
				m.deletedConversations = map[string]proto.Session{}
			}
			m.deletedConversations[row.SessionID] = row
		}
	}
	if len(m.deletedConversations) > 0 {
		list = m.overlayDeletedLocked(list)
		raws = nil // A delayed row's original bytes must not bypass the tombstone.
	}
	m.sessions, m.listedOnce = list, true
	m.raws, m.rawsOf = nil, keptList{}
	if len(raws) == len(list) && len(list) > 0 {
		byID := make(map[string]string, len(list))
		for i, raw := range raws {
			id := list[i].SessionID
			if _, twice := byID[id]; twice {
				raw = "" // named twice, which no manager does: encoded every time
			}
			byID[id] = raw
		}
		m.raws, m.rawsOf = byID, keptListOf(list)
	}
	m.lastSeen = time.Now().UTC()
	listed := make(map[string]bool, len(list))
	for _, s := range list {
		listed[s.SessionID] = true
	}
	// An archived row told apart (archived) is still the manager's, as when it came in the list.
	gone := func(id string) bool {
		if listed[id] {
			return false
		}
		_, archived := m.archivedAt[id]
		return !archived
	}
	for id := range m.lastSeq {
		if gone(id) && !m.watchStateInUseLocked(id) {
			delete(m.lastSeq, id)
			delete(m.lastHistory, id)
		}
	}
	var unsettled []string
	for id, state := range m.nodeWatch {
		if state == nodeWatchUnknown && gone(id) && m.watchers[id] <= 0 {
			unsettled = append(unsettled, id)
		}
	}
	m.mu.Unlock()
	for _, id := range unsettled {
		go m.settleEnded(id)
	}
}

// keptList is a kept list as rawsOf names it: where its sessions are and how many. A list is never changed in place
// (Machine.sessions), so the same first session and length are the same list.
type keptList struct {
	first *proto.Session
	n     int
}

func keptListOf(list []proto.Session) keptList {
	if len(list) == 0 {
		return keptList{}
	}
	return keptList{&list[0], len(list)}
}

// rawsFor is the bytes each session of list came in, by id, when list is the kept list they were recorded for (an
// older or a decorated copy has none); only read.
func (m *Machine) rawsFor(list []proto.Session) map[string]string {
	if !proto.DecodeChangedSessions || len(list) == 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rawsOf != keptListOf(list) {
		return nil
	}
	return m.raws
}

// settleEnded sends the unwatch an ended session's failed call left owed (syncWatch), then drops what the hub keeps
// for the session's watch if the node still does not list it and no app watches it: it is tried once per list that
// leaves the session out, so a node that keeps failing the unwatch of a session it ended does not keep its state for
// good. A call holding or waiting for the session's turn keeps it all (the next list tries again).
func (m *Machine) settleEnded(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), watchCallTimeout)
	defer cancel()
	m.syncWatch(ctx, id)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.watchers[id] > 0 || m.listsLocked(id) {
		return
	}
	if slot := m.watchMu[id]; slot != nil {
		if slot.refs > 0 {
			return
		}
		delete(m.watchMu, id)
	}
	delete(m.nodeWatch, id)
	delete(m.lastSeq, id)
	delete(m.lastHistory, id)
}

// listsLocked reports whether the node's last list has session id, among the archived rows told apart too
// (archived); m.mu is held.
func (m *Machine) listsLocked(id string) bool {
	for i := range m.sessions {
		if m.sessions[i].SessionID == id {
			return true
		}
	}
	_, ok := m.archivedAt[id]
	return ok
}

// watchStateInUseLocked reports whether an app watches session id or the node may be watching it; m.mu is held.
func (m *Machine) watchStateInUseLocked(id string) bool {
	_, told := m.nodeWatch[id]
	return m.watchers[id] > 0 || told
}

// watchCallTimeout bounds each session.watch / session.unwatch call, so a node that never answers holds a session's
// watchLock for at most this long. A variable for the tests.
var watchCallTimeout = 10 * time.Second

// nodeWatchState is what the node was told about a session's watch. The zero value is not watched.
type nodeWatchState int

const (
	nodeUnwatched nodeWatchState = iota
	nodeWatching
	nodeWatchUnknown // the last session.watch / session.unwatch failed: either may hold, the next sync sends again
)

// watchSlot is session id's watchLock: sem is the 1-slot turn, refs the calls holding or waiting for it. The count
// keeps a slot from being dropped while a waiter has it, which would let a second slot give out a concurrent turn.
type watchSlot struct {
	sem  chan struct{}
	refs int
}

// watchLock takes the lock that orders what the node is asked about session id's watch, giving up when ctx ends
// first (false). The returned func releases it.
func (m *Machine) watchLock(ctx context.Context, id string) (func(), bool) {
	m.mu.Lock()
	if m.watchMu == nil {
		m.watchMu = map[string]*watchSlot{}
	}
	slot := m.watchMu[id]
	if slot == nil {
		slot = &watchSlot{sem: make(chan struct{}, 1)}
		m.watchMu[id] = slot
	}
	slot.refs++
	m.mu.Unlock()
	select {
	case slot.sem <- struct{}{}:
		return func() {
			<-slot.sem
			m.dropWatchSlot(id, slot)
		}, true
	case <-ctx.Done():
		m.dropWatchSlot(id, slot)
		return nil, false
	}
}

// dropWatchSlot ends one call's hold on (or wait for) session id's slot, and drops the slot when it was the last and
// the session's watch is idle: no app subscribed and nothing told to the node.
func (m *Machine) dropWatchSlot(id string, slot *watchSlot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	slot.refs--
	if slot.refs == 0 && m.watchMu[id] == slot && !m.watchStateInUseLocked(id) {
		delete(m.watchMu, id)
	}
}

// syncWatch makes the node watch session id when an app is subscribed to it and stop when none is. The calls for one
// session go one at a time, and each looks at the subscriber count when its turn comes: the watch of an app that
// subscribes while the last one's unwatch is on its way goes after it, not before (the node ignores a watch for a
// session it watches, so the unwatch arriving second would leave the subscriber with no live items).
//
// A caller whose ctx ends while another call holds the turn gives up waiting: the holder looks at the count again
// when its call is done and sends what changed meanwhile (syncWatchLocked), so no change is lost.
func (m *Machine) syncWatch(ctx context.Context, id string) {
	unlock, ok := m.watchLock(ctx, id)
	if !ok {
		return
	}
	defer unlock()
	m.syncWatchLocked(ctx, id)
}

// syncWatchLocked is syncWatch with session id's watchLock held. It sends calls until the node was told what the
// subscriber count wants or a call fails (the next sync sends again then).
func (m *Machine) syncWatchLocked(ctx context.Context, id string) {
	for first := true; ; first = false {
		if !first {
			// A later round is for subscribers that came or went meanwhile, not for this caller's connection.
			ctx = context.Background()
		}
		if !m.syncWatchOnce(ctx, id) {
			return
		}
	}
}

// syncWatchOnce sends the one call session id's watch needs now, if any, and records what it left. It reports whether
// a call went out and succeeded (another may be needed: the count may have changed while it was on its way).
func (m *Machine) syncWatchOnce(ctx context.Context, id string) bool {
	m.mu.Lock()
	want := m.watchers[id] > 0
	state := m.nodeWatch[id]
	since, history := m.lastSeq[id], m.lastHistory[id]
	m.mu.Unlock()
	var method string
	var params map[string]any
	switch {
	case want && state != nodeWatching:
		method, params = "session.watch", map[string]any{"session_id": id, "since_seq": since}
		if history != "" {
			params["history"] = history
		}
	case !want && state != nodeUnwatched:
		method, params = "session.unwatch", map[string]any{"session_id": id}
		ctx = context.Background() // not the leaving app's connection
	default:
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, watchCallTimeout)
	defer cancel()
	err := m.call(ctx, method, params, nil)
	next := nodeUnwatched
	switch {
	case err != nil:
		next = nodeWatchUnknown
		if !errors.Is(err, errOffline) {
			m.hub.logf("[%s] %s %s: %v", m.cfg.ID, method, id, err)
		}
	case want:
		next = nodeWatching
	}
	m.mu.Lock()
	if m.nodeWatch == nil {
		m.nodeWatch = map[string]nodeWatchState{}
	}
	if next == nodeUnwatched {
		delete(m.nodeWatch, id)
	} else {
		m.nodeWatch[id] = next
	}
	m.mu.Unlock()
	return err == nil
}

// watchAgainFresh syncs session id's watch with a manager that is new: it watches nothing yet, whatever the old one
// was told.
func (m *Machine) watchAgainFresh(ctx context.Context, id string) {
	unlock, ok := m.watchLock(ctx, id)
	if !ok {
		m.hub.logf("[%s] watch %s again: an earlier session.watch / session.unwatch is still on its way", m.cfg.ID, id)
		return
	}
	defer unlock()
	m.mu.Lock()
	delete(m.nodeWatch, id)
	m.mu.Unlock()
	m.syncWatchLocked(ctx, id)
}

func (m *Machine) onEvent(event string, params json.RawMessage) {
	switch event {
	case "fs.changed": // a folder or Git directory a web panel has open changed (fswatch.go)
		var p struct {
			Dirs []string `json:"dirs"`
			Git  []string `json:"git"`
		}
		if json.Unmarshal(params, &p) == nil {
			m.hub.fsChanged(m.cfg.ID, p.Dirs, p.Git)
		}
	case "sessions.revs":
		var p struct {
			Revs map[string]int64 `json:"revs"`
		}
		if json.Unmarshal(params, &p) != nil || len(p.Revs) == 0 {
			return
		}
		m.mu.Lock()
		described := m.rawsOf == keptListOf(m.sessions)
		m.sessions = slices.Clone(m.sessions) // the kept list may be being read (keptSessions)
		for i := range m.sessions {
			if r, ok := p.Revs[m.sessions[i].SessionID]; ok {
				m.sessions[i].Rev = r
			}
		}
		if described {
			// Each session is still what its bytes give but for its rev, which its kept row is checked against too
			// (encodeKept): the sessions whose rev changed are encoded again, the others' rows still hold.
			m.rawsOf = keptListOf(m.sessions)
		}
		// An archived row told apart (archived) takes its rev as it did in the list: a lookup by id reads it.
		cloned := false
		for id, r := range p.Revs {
			if i, ok := m.archivedAt[id]; ok {
				if !cloned {
					m.archived, cloned = slices.Clone(m.archived), true // replaced whole, never changed in place
				}
				m.archived[i].Rev = r
			}
		}
		m.mu.Unlock()
		// Every app, not only those watching: this is what lets them pull a session before it is opened.
		m.hub.broadcast(map[string]any{"type": "revs", "machine_id": m.cfg.ID, "revs": p.Revs})
		m.hub.reindexSessions(m)
	case "session.items":
		var p struct {
			SessionID string       `json:"session_id"`
			Items     []proto.Item `json:"items"`
			MoreAfter bool         `json:"more_after"`
			itemSpan
		}
		// A batch without items still matters when it has a span: it moves the apps' "complete up to" forward.
		if json.Unmarshal(params, &p) != nil || (len(p.Items) == 0 && p.ToSeq == nil) {
			return
		}
		last := 0
		if len(p.Items) > 0 {
			last = p.Items[len(p.Items)-1].Seq
		}
		if p.ToSeq != nil {
			last = max(last, *p.ToSeq)
		}
		m.mu.Lock()
		reset := (p.FromSeq != nil && *p.FromSeq == 0) || (p.History != "" && p.History != m.lastHistory[p.SessionID])
		if reset || last > m.lastSeq[p.SessionID] {
			m.lastSeq[p.SessionID] = last
		}
		if p.History != "" {
			m.lastHistory[p.SessionID] = p.History
		}
		m.mu.Unlock()
		p.Items = m.hub.withInjected(p.Items)
		m.hub.ttft.annotate(m.cfg.ID, p.SessionID, p.Items, true)
		m.hub.itemsToSubscribers(m.cfg.ID, p.SessionID, p.Items, p.itemSpan, p.MoreAfter)
	case "session.screen":
		var p struct {
			SessionID string `json:"session_id"`
			Text      string `json:"text"`
			Status    string `json:"status"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		m.hub.toSubscribers(m.cfg.ID, p.SessionID, screenPush{Type: "screen", MachineID: m.cfg.ID, SessionID: p.SessionID,
			Text: p.Text, Status: p.Status, TS: time.Now().UTC()})
	case "session.live_tools":
		var p struct {
			SessionID string `json:"session_id"`
			proto.LiveToolState
		}
		if json.Unmarshal(params, &p) == nil && p.SessionID != "" && p.Tools != nil {
			m.setLiveTools(p.SessionID, p.LiveToolState)
		}
	case "session.live": // a working Claude's status line as it is now: not kept, not replayed (docs/protocol/protocol.md "Live status")
		var p struct {
			SessionID string             `json:"session_id"`
			Live      *proto.SessionLive `json:"live"`
		}
		if json.Unmarshal(params, &p) == nil && p.SessionID != "" {
			m.hub.toSubscribersWith(m.cfg.ID, p.SessionID, proto.FeatureSessionLive,
				livePush{Type: "live", MachineID: m.cfg.ID, SessionID: p.SessionID, Live: p.Live})
		}
	case "terminal.frame":
		m.hub.onTerminalFrame(m.cfg.ID, params)
	case "herdr.client.data":
		m.hub.onHerdrClientData(m.cfg.ID, params)
	case "herdr.client.end":
		m.hub.onHerdrClientEnd(m.cfg.ID, params)
	case "herdr.focus.changed":
		m.hub.onHerdrFocus(m.cfg.ID, params)
	case "terminal.end":
		var p struct {
			SessionID string `json:"session_id"`
			Error     string `json:"error"`
			Control   bool   `json:"control"`
			Taken     bool   `json:"taken"`
		}
		if json.Unmarshal(params, &p) != nil {
			return
		}
		if p.Control {
			m.hub.terminalControlEnded(m.cfg.ID, p.SessionID, p.Error, p.Taken)
		} else {
			m.hub.terminalEnded(m.cfg.ID, p.SessionID, p.Error, nil)
		}
	case "session.delta":
		var p struct {
			SessionID string `json:"session_id"`
			Text      string `json:"text"`
		}
		if json.Unmarshal(params, &p) == nil {
			if p.Text != "" {
				m.hub.ttft.firstToken(m.cfg.ID, p.SessionID, time.Now())
			}
			m.hub.rememberLiveText(m.cfg.ID, p.SessionID, p.Text)
			m.hub.toSubscribers(m.cfg.ID, p.SessionID, deltaPush{Type: "delta", MachineID: m.cfg.ID, SessionID: p.SessionID, Text: p.Text})
		}
	case "codex.inventory":
		if !m.codexInventoryHanded() {
			m.hub.logf("[%s] Codex inventory from a connection the hub has not handed its own yet: not kept", m.cfg.ID)
			return
		}
		m.rememberCodexInventory(params)
	case "session.echo":
		m.recordEcho(params)
	case "hook.event": // what it says of the session's turn is the manager's (assistant/fleet.go); the apps see it
		var p map[string]any
		if json.Unmarshal(params, &p) == nil {
			p["type"] = "hook_event"
			p["machine_id"] = m.cfg.ID
			m.hub.broadcast(p)
		}
	}
}

// onRequest answers what is the hub's of a node's requests (devices.request): personad notify on the machine. The
// interactions, the MCP tools and assistant.lark are the manager's (assistant/fleet.go).
func (m *Machine) onRequest(_ context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "notify": // personad notify on this machine (node/scriptcli.go)
		return m.scriptNotify(params)
	case assistantwire.MethodViewCheck:
		return m.checkAssistantView(params)
	}
	return nil, fmt.Errorf("unknown method %s", method)
}

// nodeMismatch: the node's personad is another build than the hub's. Builds are named <commit>[-dirty]-<time>
// (deploy.sh); dev builds ("dev", empty) are never flagged. The local node is the hub binary itself.
func nodeMismatch(hub, node string) bool {
	if hub == "" || node == "" || hub == "dev" || node == "dev" {
		return false
	}
	return hub != node
}

func (m *Machine) isOnline() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.online
}
