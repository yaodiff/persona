package hub

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/streams"
)

// ---- what the manager reports ----

// startDevices makes the machines of hub.json before the managers report them: shown as before a restart while they
// come back (what the last hub process saw them report, saveDevicesWarm), offline otherwise, as their manager last
// reported them (machine_entries.go); a call waits for its manager meanwhile (waitDevicesFor).
func (h *Hub) startDevices(ctx context.Context) {
	d := &h.devices
	d.mu.Lock()
	d.entries, d.started = slices.Clone(h.cfg.Machines), true
	d.mu.Unlock()
	if err := h.loadMachineOwners(ctx); err != nil {
		h.logf("devices: ownership unavailable; dedicated local machines stay unassigned: %v", err)
	}
	_ = h.loadMachinesKept(ctx)   // and the machines their managers keep out of hub.json (machines_kept.go)
	_ = h.loadMachineEntries(ctx) // what each manager last reported of them (machine_entries.go)
	var saved devicesWarm
	if h.store != nil {
		if ok, err := h.store.TakeSetting(ctx, devicesWarmKey, &saved); err != nil || !ok || time.Since(saved.SavedAt) > devicesWarmStale {
			saved.Machines = nil
		}
	}
	until := time.Now().Add(devicesWarmGrace)
	n := 0
	known := h.knownEntries()
	h.fleet.mu.Lock()
	for _, mc := range known {
		if h.byID[mc.ID] != nil {
			continue
		}
		m := h.newManagedMachine(mc)
		m.engine = saved.Engines[mc.ID]
		if c, ok := saved.EngineHosts[mc.ID]; ok {
			m.engineHosts = &c
		}
		if w, ok := saved.Machines[mc.ID]; ok && (w.Until.IsZero() || w.Until.After(time.Now())) {
			m.info, m.sessions, m.listedOnce, m.warmUntil = w.Info, w.Sessions, true, until
			if len(w.Archived) > 0 {
				m.archived, m.archivedAt, m.archivedBy = w.Archived, archivedIndex(w.Archived), w.ArchivedBy
			}
			if !w.Until.IsZero() && w.Until.Before(until) {
				m.warmUntil = w.Until
			}
			n++
		}
		h.machines = append(h.machines, m)
		h.byID[mc.ID] = m
	}
	h.fleet.mu.Unlock()
	d.mu.Lock()
	d.warmUntil = until
	d.fleetReady = true
	d.mu.Unlock()
	if n > 0 {
		h.logf("warm start: %d machines shown as before while the manager reconnects them", n)
	}
	time.AfterFunc(devicesWarmGrace, h.devicesWarmEnded)
}

// newManagedMachine is a machine the hub lists while its manager has not reported it yet, with what that manager last
// reported of it.
func (h *Hub) newManagedMachine(mc MachineConfig) *Machine {
	m := newMachine(h, mc)
	m.managed, m.peer = true, &busNode{h: h, id: mc.ID, done: make(chan struct{})}
	m.owner = h.entryOwner(mc.ID)
	if e, ok := h.reportedEntryOf(m.owner, mc.ID); ok {
		m.info, m.lastSeen = e.Info, e.LastSeen
	}
	return m
}

// retryMachineAssignments reads what startDevices could not. The move locks make a recovered read and its handover one
// move against the managers' moves and removals. False leaves the trust loop retrying at trustRetryEvery.
func (h *Hub) retryMachineAssignments(ctx context.Context) bool {
	d := &h.devices
	d.mu.Lock()
	ownersRead, keptRead := d.assignRead, d.keptRead
	d.mu.Unlock()
	if ownersRead && keptRead {
		return true
	}
	h.moving.managers.RLock()
	defer h.moving.managers.RUnlock()
	d.moving.Lock()
	defer d.moving.Unlock()
	ownersRead = h.readOwnersMoving(ctx) == nil
	if !keptRead {
		d.mu.Lock()
		keptRead = d.keptRead // another operation may have read it while this one waited for the lock
		d.mu.Unlock()
		if !keptRead {
			if err := h.loadMachinesKept(ctx); err != nil {
				return false
			}
			keptRead = true
		}
	}
	return ownersRead && keptRead
}

// reconcileKeptMachines lists the machines whose kept records were unread at start. It runs only when those records
// are first recovered: a manager's later report that drops one must not have it put back by the retry loop.
func (h *Hub) reconcileKeptMachines() {
	known := h.knownEntries()
	added := false
	h.fleet.mu.Lock()
	for _, mc := range known {
		if h.cfgEntry(mc.ID) != nil || h.byID[mc.ID] != nil {
			continue
		}
		m := h.newManagedMachine(mc)
		h.machines = append(h.machines, m)
		h.byID[mc.ID] = m
		added = true
	}
	h.fleet.mu.Unlock()
	if added {
		h.broadcastMachines()
	}
}

// markUnknownLocked shows m's sessions that are not offline as unknown, its archived rows told apart (archived) as
// well, as when they were in its list, in copies: the kept lists are never changed in place (Machine.sessions). m.mu
// is held.
func (m *Machine) markUnknownLocked() {
	m.sessions = markedUnknown(m.sessions)
	if len(m.archived) > 0 {
		m.archived = markedUnknown(m.archived) // the same rows in the same order: archivedAt still holds
	}
}

// markedUnknown is a copy of list with the sessions that are not offline shown as unknown.
func markedUnknown(list []proto.Session) []proto.Session {
	list = slices.Clone(list)
	for i := range list {
		if list[i].Status != proto.StatusOffline {
			list[i].Status = proto.StatusUnknown
			if list[i].PendingInteractions == 0 { // the manager's states as it would show them: a card still waits
				list[i].State = proto.StateUnknown
			}
		}
	}
	return list
}

// devicesUp: the carrier is connected and has reported its machines (devicesUpFor): hub.json's machines nobody moved,
// and what names no machine.
func (h *Hub) devicesUp() bool { return h.devicesUpFor(devicesOwner) }

// devicesUpFor: manager owner is connected and has reported its machines.
func (h *Hub) devicesUpFor(owner string) bool {
	d := &h.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.upOpen[owner]
}

// upChanLocked is owner's up channel, made at the first ask (the maps too); d.mu is held.
func (d *devicesState) upChanLocked(owner string) chan struct{} {
	if d.up == nil {
		d.up, d.upOpen = map[string]chan struct{}{}, map[string]bool{}
	}
	up, ok := d.up[owner]
	if !ok {
		up = make(chan struct{})
		d.up[owner] = up
	}
	return up
}

// waitDevicesFor returns nil once manager owner is up; errManagerOffline at once while it is not, unless the hub just
// started (its machines shown as before) and owner has not reported since: then a call waits for it that long. A
// manager of the roster that is away is waited for like the carrier; a machine nobody is known to carry
// (unknownMachineOwner: the ownership could not be read, ownership.go) is offline at once.
func (h *Hub) waitDevicesFor(ctx context.Context, owner string) error {
	if owner == unknownMachineOwner {
		return errManagerOffline
	}
	d := &h.devices
	d.mu.Lock()
	if d.upOpen[owner] {
		d.mu.Unlock()
		return nil
	}
	up, until := d.upChanLocked(owner), d.warmUntil
	if d.reportedSince[owner] { // its start is over: from now on a call without it fails at once
		until = time.Time{}
	}
	d.mu.Unlock()
	if !time.Now().Before(until) {
		return errManagerOffline
	}
	t := time.NewTimer(time.Until(until))
	defer t.Stop()
	select {
	case <-up:
		return nil
	case <-t.C:
	case <-ctx.Done():
		return ctx.Err()
	}
	return errManagerOffline
}

// awaitManager is awaitManagerFor the carrier.
func (h *Hub) awaitManager(ctx context.Context, limit time.Duration) bool {
	return h.awaitManagerFor(ctx, devicesOwner, limit)
}

// awaitManagerFor is waitDevicesFor what cannot wait long (a WebSocket handshake, a peer's request): at most limit.
// True once manager owner is up.
func (h *Hub) awaitManagerFor(ctx context.Context, owner string, limit time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	return h.waitDevicesFor(ctx, owner) == nil
}

// applyDeviceStatesFrom takes devices.state from manager owner: every machine it carries, in the apps' order. The
// other managers' machines stay as they were, and only the machines owner carries are taken (ownership.go
// machineOwner: another manager's, and a mesh member's from any manager but the carrier, are dropped). The apps get
// the managers' machines grouped: devicesOwner's first, then the others' by id.
func (h *Hub) applyDeviceStatesFrom(owner string, st devicewire.States) {
	// A transport alias cannot take a configured or another manager's identity.
	// The manager owns all session resolution; this only retains a bus destination.
	validAliases := map[string]bool{}
	for _, state := range st.Machines {
		id, parent := state.Machine.ID, state.TransportFor
		if id == "" || parent == "" || id == parent || h.entryOwner(parent) != owner {
			continue
		}
		if h.knownEntry(id) != nil && h.entryOwner(id) != owner {
			continue
		}
		h.devices.mu.Lock()
		if h.devices.transportOwners == nil {
			h.devices.transportOwners = map[string]string{}
		}
		old := h.devices.transportOwners[id]
		if old == "" || old == owner {
			h.devices.transportOwners[id] = owner
			validAliases[id] = true
		}
		h.devices.mu.Unlock()
	}

	// Reports are complete snapshots. Dropped logical roots must release their
	// transport ownership, so a later manager cannot inherit a stale alias.
	h.devices.mu.Lock()
	for id, aliasOwner := range h.devices.transportOwners {
		if aliasOwner == owner && !validAliases[id] {
			delete(h.devices.transportOwners, id)
		}
	}
	h.devices.mu.Unlock()

	now := time.Now()
	type change struct {
		m              *Machine
		ended, offline bool
		began          bool // the node's connection is a new one (or the hub just met it): it knows no fs.watch (fswatch.go)
		oldMux         *streams.Mux
	}
	var changes []change
	h.fleet.mu.Lock()
	prev := h.byID
	order := make([]*Machine, 0, len(st.Machines))
	byID := map[string]*Machine{}
	for _, s := range st.Machines {
		id := s.Machine.ID
		if id == "" || byID[id] != nil {
			continue
		}
		m := prev[id]
		// Only its manager reports a machine (ownership.go): hub.json's or one a manager keeps, its owner's, derived or
		// not (another one is letting it go, or claims what is not its own); any other (a mesh member's), the
		// carrier's only.
		if known := h.knownEntry(id) != nil; known && h.entryOwner(id) != owner || !known && owner != devicesOwner && !validAliases[id] {
			continue
		}
		// What it is called and how it is reached: hub.json's entry says for one of this hub's own in hub.json (the
		// manager follows it a moment later; mirrorConfig), the manager for one it keeps (machines_kept.go) and for a
		// member's (the member renamed it).
		name, kind := s.Machine.Name, s.Machine.Kind
		if own := h.cfgEntry(id); own != nil && !s.Derived {
			name, kind = cmp.Or(own.Name, id), own.Kind
		}
		kindChanged := false
		if m == nil || !m.managed {
			// Its entry stays as it was made (it is read without m.mu everywhere, as when the hub connected it).
			m = newMachine(h, h.mirrorConfig(s))
			m.managed, m.peer = true, &busNode{h: h, id: id, done: make(chan struct{})}
		} else if kindName(kind) != kindName(m.kind()) {
			// Switched to Persona Link, or back (link_switch.go): the same computer, reached another way. What ran
			// through the old way ends as when its connection ends.
			m.setKind(kind)
			kindChanged = true
		}
		m.mu.Lock()
		m.owner = owner
		was := m.online || m.warmLocked(now)
		wasConn, wasAt := m.connected, m.connectedAt
		snap := s.Machine
		// online is the node connected now, as it was when the hub connected it (self-update waits for it, the node
		// updates go to it); the manager's warm start is warmUntil, as the hub's own was (the apps see both as online).
		m.snap, m.info, m.online, m.err = &snap, s.Info, s.Connected, s.Machine.Error
		m.setName(name)
		if s.Machine.LastSeen.After(m.lastSeen) {
			m.lastSeen = s.Machine.LastSeen
		}
		m.derived, m.paths = s.Derived, s.Paths
		m.connected, m.connectedAt = s.Connected, s.ConnectedAt
		m.warmUntil = s.WarmUntil
		c := change{m: m}
		c.ended = wasConn && (!s.Connected || !s.ConnectedAt.Equal(wasAt) || kindChanged)
		c.began = s.Connected && (!wasConn || !s.ConnectedAt.Equal(wasAt) || kindChanged)
		if c.ended || s.Connected && !wasConn {
			c.oldMux, m.streams = m.streams, nil
		}
		if s.Connected && m.streams == nil {
			m.streams = streams.New(m.notify) // forwarded ports (ports.go)
		}
		if was && !m.online && !m.warmLocked(now) {
			m.markUnknownLocked()
			c.offline = true
		}
		m.mu.Unlock()
		order = append(order, m)
		byID[id] = m
		changes = append(changes, c)
	}
	var gone []*Machine
	groups := map[string][]*Machine{owner: order}
	var unmanaged []*Machine
	for _, m := range h.machines { // the previous order
		if byID[m.cfg.ID] != nil {
			continue
		}
		if !m.managed {
			unmanaged = append(unmanaged, m) // a test's machine stays
			continue
		}
		if o := m.ownerOrCarrier(); o != owner {
			groups[o] = append(groups[o], m)
		} else {
			gone = append(gone, m)
		}
	}
	for id, m := range prev { // one not in the order (none, but a map is the truth)
		if byID[id] == nil && !slices.Contains(h.machines, m) && !m.managed {
			unmanaged = append(unmanaged, m)
		}
	}
	owners := make([]string, 0, len(groups))
	for o := range groups {
		if o != devicesOwner {
			owners = append(owners, o)
		}
	}
	sort.Strings(owners)
	all := slices.Clone(groups[devicesOwner])
	for _, o := range owners {
		all = append(all, groups[o]...)
	}
	all = append(all, unmanaged...)
	for _, m := range all {
		byID[m.cfg.ID] = m
	}
	h.machines, h.byID = all, byID
	h.fleet.mu.Unlock()

	d := &h.devices
	d.mu.Lock()
	if d.recEntries == nil {
		d.recEntries = map[string][]devicewire.RecMachine{}
	}
	d.recEntries[owner] = st.Entries
	if owner == devicesOwner { // hub.json's entries another one absorbed: the carrier's to say
		d.absorbed = st.Absorbed
	}
	// Its machines are reachable and the calls waiting for it go (waitDevicesFor); not on a report sent after it was
	// removed from the roster (machine_entries.go: that one is dropped too). ReportedSince is visible in the same
	// transition: a disconnect cannot make a post-report call enter the warm wait again.
	if !d.upOpen[owner] && !d.reportedForgot[owner] {
		if d.reportedSince == nil {
			d.reportedSince = map[string]bool{}
		}
		d.reportedSince[owner] = true
		close(d.upChanLocked(owner))
		d.upOpen[owner] = true
	}
	fresh := d.fresh[owner]
	delete(d.fresh, owner)
	d.mu.Unlock()
	h.takeMachineEntries(owner, st) // kept for when it is away (machine_entries.go)
	named := map[string]bool{}
	for _, e := range st.Entries {
		named[e.ID] = true
	}
	d.mu.Lock()
	if d.ownListHeld == nil {
		d.ownListHeld, d.lastNamed = map[string]bool{}, map[string]map[string]bool{}
	}
	heldBefore := d.ownListHeld[owner]
	d.ownListHeld[owner], d.lastNamed[owner] = st.OwnList, named
	d.mu.Unlock()
	if heldBefore != st.OwnList {
		h.devicesConfigChanged() // its hand-over carries machines, or no longer does
	}
	if st.OwnList && h.ownsList(owner) {
		go h.settleOwnList(owner) // hub.json's machines it keeps now move out (machines_kept.go)
	}
	// The rejected computers the manager let go are not kept out any more (machine_trust.go).
	h.fleetLeft(func(id string) bool { return byID[id] != nil || st.Absorbed[id] != "" })
	if fresh {
		go h.watchAgain(order)
	}

	for _, c := range changes {
		if c.oldMux != nil {
			c.oldMux.CloseAll()
		}
		if c.ended {
			h.connectionEnded(c.m, i18n.T(h.userLang(), "hub-devices_link.computer_offline"))
		}
	}
	for _, m := range gone {
		h.connectionEnded(m, i18n.T(h.userLang(), "hub-devices_link.computer_offline"))
		m.mu.Lock()
		mux := m.streams
		m.streams = nil
		m.mu.Unlock()
		if mux != nil {
			mux.CloseAll()
		}
	}
	h.broadcastMachines()
	for _, c := range changes {
		if c.offline {
			h.sessionsChanged(c.m)
		}
		if c.began {
			h.fsWatchReconnected(c.m) // queues the web panels' open folders; never calls the node here (#2439)
		}
	}
}

// mirrorConfig is a reported machine's entry: hub.json's for one of its own, else the member's, with the id the
// manager gave it.
func (h *Hub) mirrorConfig(s devicewire.State) MachineConfig {
	if mc := h.cfgEntry(s.Machine.ID); mc != nil && !s.Derived {
		return *mc
	}
	var mc MachineConfig
	_ = json.Unmarshal(s.Config, &mc)
	mc.ID, mc.Kind = s.Machine.ID, s.Machine.Kind
	if s.Machine.Name != "" {
		mc.Name = s.Machine.Name
	}
	return mc
}

// devicesConnected: the manager that carries the machines connected (its handshake). The port streams and terminals
// that ran through its last connection ended with it, as they do when a node's own connection ends (the manager ends
// them on the nodes: internal/devices hubopen.go); a terminal whose first watch waits for this connection stays.
func (h *Hub) devicesConnected(owner string) {
	d := &h.devices
	d.mu.Lock()
	delete(d.reportedForgot, owner) // a manager in the roster again: what it reports is kept
	// A new connection holds its own list only once its report says so: until then its hand-over carries what
	// hub.json stages for it, and edits are staged there (machines_kept.go).
	delete(d.ownListHeld, owner)
	delete(d.lastNamed, owner)
	if d.fresh == nil {
		d.fresh = map[string]bool{}
	}
	d.fresh[owner] = true
	if d.conns == nil {
		d.conns = map[string]uint64{}
	}
	d.conns[owner]++
	d.mu.Unlock()
	for _, m := range h.machinesNow() {
		m.mu.Lock()
		mux := m.streams
		if !m.managed || mux == nil || cmp.Or(m.owner, devicesOwner) != owner {
			m.mu.Unlock()
			continue
		}
		m.streams = streams.New(m.notify)
		m.mu.Unlock()
		mux.CloseAll()
	}
	t := &h.terms
	t.mu.Lock()
	ran := map[[2]string]*termRelay{}
	for k, r := range t.relays {
		if r.synced != nil || r.observing {
			ran[k] = r
		}
	}
	t.mu.Unlock()
	for k, r := range ran {
		if m := h.machineByID(k[0]); m != nil && m.managed && m.ownerOrCarrier() == owner {
			h.terminalEnded(k[0], k[1], errAssistantOffline.Error(), r)
		}
	}
}

// watchAgain asks a manager that is new to watch the sessions the apps watch (it watches them again itself whenever
// a node reconnects). A few at a time: one slow computer does not hold up the others' items.
func (h *Hub) watchAgain(list []*Machine) {
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for _, m := range list {
		m.mu.Lock()
		managed := m.managed
		// The sessions the apps watch, and those the old manager was told to watch: the new one watches none of them
		// yet (watchAgainFresh).
		ids := map[string]bool{}
		for id, n := range m.watchers {
			if n > 0 {
				ids[id] = true
			}
		}
		for id := range m.nodeWatch {
			ids[id] = true
		}
		m.mu.Unlock()
		if !managed {
			continue
		}
		for id := range ids {
			sem <- struct{}{}
			wg.Add(1)
			go func() {
				defer func() { <-sem; wg.Done() }()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				m.watchAgainFresh(ctx, id)
			}()
		}
	}
	wg.Wait()
}

// connectionEnded ends what ran on a node's connection that is gone: its terminals, the text being generated.
func (h *Hub) connectionEnded(m *Machine, why string) {
	m.clearLiveTools()
	h.forgetLiveText(m.cfg.ID) // the node that was generating it is gone: the transcript has what was finished
	h.terminalsEnded(m.cfg.ID, why)
	h.fullsOffline(m.cfg.ID) // the full herdr views ran on that connection too
	// The questions waiting on it are the manager's (assistant/fleet_ix.go NodeDetached).
}

// devicesGone: the manager that carries the machines disconnected. The machines show offline (why: it is not
// there), their terminals and streams close, their questions end; when it is back it reports them again.
func (h *Hub) devicesGone(owner string) {
	d := &h.devices
	d.mu.Lock()
	conns := d.conns[owner]
	d.mu.Unlock()
	h.upGone(owner, conns)
	// The hub's own stop (the manager gets its SIGTERM at the same time): the next hub shows them as they were
	// (startDevices). A moment's wait lets the hub's stop begin first.
	time.Sleep(500 * time.Millisecond)
	if h.stopping.Load() || h.busConnOf(owner) != nil || h.devicesUpFor(owner) {
		return
	}
	d.mu.Lock()
	delete(d.recEntries, owner) // it reaches none of them now (the mesh record says so)
	d.mu.Unlock()
	var off []*Machine
	for _, m := range h.machinesNow() {
		m.mu.Lock()
		if !m.managed || cmp.Or(m.owner, devicesOwner) != owner {
			m.mu.Unlock()
			continue
		}
		m.mu.Unlock()
		h.offlineLocal(m, i18n.Localize(errAssistantOffline, h.userLang()))
		off = append(off, m)
	}
	for _, m := range off {
		h.connectionEnded(m, i18n.Localize(errAssistantOffline, h.userLang()))
	}
	if len(off) > 0 {
		h.machineEntriesChanged() // seen until now (offlineLocal)
		h.logf("devices: [%s] the manager is gone: %d machines offline until it is back", owner, len(off))
		h.broadcastMachines()
		for _, m := range off {
			h.sessionsChanged(m)
		}
	}
}

// upGone forgets owner's up state with the connection that ended, unless a newer one is there already: current now, or
// connected (devicesConnected) since conns was read. A manager reports only after its handshake, so an up state found
// with no newer connection is the old connection's; one found after a newer handshake is that connection's and stays.
func (h *Hub) upGone(owner string, conns uint64) {
	if h.busConnOf(owner) != nil {
		return
	}
	d := &h.devices
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conns[owner] != conns || !d.upOpen[owner] {
		return
	}
	delete(d.up, owner) // the next wait for it gets a channel of its own (upChanLocked)
	delete(d.upOpen, owner)
}

// offlineLocal shows m offline with error why until its manager reports it again: its streams close, its sessions'
// states are unknown (the caller ends what ran on its connection and tells the apps).
func (h *Hub) offlineLocal(m *Machine, why string) {
	m.mu.Lock()
	if m.connected { // seen until now
		m.lastSeen = time.Now().UTC()
	}
	mux := m.streams
	m.streams, m.connected = nil, false
	m.online, m.err = false, why
	m.endWarmLocked()
	if m.snap != nil {
		s := *m.snap
		s.Online, s.Error, s.Transport, s.TailcatPath, s.Link = false, m.err, "", "", nil
		m.snap = &s
	}
	m.markUnknownLocked()
	m.mu.Unlock()
	if mux != nil {
		mux.CloseAll()
	}
}

// machineConnected is the hub's part of a node's new connection (devices.connected): what it adds to the node's
// configuration and the calls right after it, and what it starts for the computer.
func (h *Hub) machineConnected(ctx context.Context, m *Machine, at time.Time) devicewire.ConnectedReply {
	m.mu.Lock()
	m.hookedAt = at
	m.mu.Unlock()
	out := devicewire.ConnectedReply{Configure: map[string]json.RawMessage{}}
	// Nodes use the same explicit capability announcement as the app hello. The
	// manager forwards this authenticated response in node.configure on reconnect.
	if b, err := json.Marshal(h.features()); err == nil {
		out.Configure["hub_features"] = b
	}
	if b, err := json.Marshal(m.codexConfig(ctx)); err == nil {
		out.Configure["codex"] = b
	}
	if list := m.echoesToSend(ctx); len(list) > 0 {
		if b, err := json.Marshal(map[string]any{"echoes": list}); err == nil {
			out.Calls = append(out.Calls, devicewire.NodeCall{MachineID: m.cfg.ID, Method: "session.echoes", Params: b})
		}
	}
	return out
}

// updateNodes tells connected managers to check their own computers after
// the Hub publishes packages or the operator enables automatic updates.
func (h *Hub) updateNodes(ctx context.Context) {
	for _, p := range h.busRoster() {
		if p.Role != bus.RoleManager {
			continue
		}
		call, cancel := context.WithTimeout(ctx, updateRequestLimit)
		_, err := buswire.Call(call, h.managerCaller(p.ID), devicewire.UpdateNodesMethod, buswire.None{})
		cancel()
		if err != nil {
			h.logf("update: [%s] checks its node packages: %v", p.ID, err)
		}
	}
}
