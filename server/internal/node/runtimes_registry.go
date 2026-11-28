package node

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// runtimeRegistry is the node's registry of connected runtimes.
type runtimeRegistry struct {
	mu     sync.Mutex
	serial int
	conns  map[int]*runtimeConn
	// Only a node-directed start can bind protected command input to a session.
	inputBindings map[string]*runtimeInputBinding
	// owner: session id -> the runtime listing it (the newest connection wins when two list the same id).
	owner map[string]*runtimeConn
	// watching: sessions the hub watches -> the last seq pushed for each, so a runtime that reconnects is asked to
	// watch them again from where the phone is.
	watching    map[string]int
	lastHistory map[string]string // the history each watched cursor belongs to
	// watchParents: the hub connection each watch belongs to, including a runtime-only watch before its transcript exists.
	watchParents map[string]context.Context
	// known: session id -> the runtime that listed it or said it has it, kept after it leaves the list. A runtime
	// lists only its recent sessions (DSH: the latest 80 of thousands), but an older one is still its own: calls
	// about it go to that runtime, never to the terminal host as a Claude session to resume (that sent a DSH
	// session's message to herdr, on a machine without herdr).
	known map[string]knownSession
	// remoteSessions latches session IDs ever associated with a remote assistant runtime (caps.assistant &&
	// caps.remote_agent) so disconnects or ownership changes never drop the confirm-writes posture.
	remoteSessions map[string]struct{}
	// unclaimed: ids no connected runtime had when asked, until when not to ask again, and what the runtime whose own
	// id it is answered then (runtimeFor).
	unclaimed map[string]unclaimedID
	// unlisted: runtime id -> the Claude transcripts it names as sessions of its own that it does not list, kept a
	// while after it left (runtimeunlisted.go).
	unlisted map[string]unlistedOf
	// away: runtime id -> the rows of its last list whose conversation is a Claude transcript here, listed as not
	// running for a while after it left (runtimeaway.go).
	away map[string]awayOf
	// codexClaims: runtime id -> the Codex app-server instances it serves itself, which the node does not attach to,
	// kept for codexClaimKeep after it left (codex_instance_claims.go).
	codexClaims map[string]codexClaimsOf
	// logf reports what an operator should fix (a launcher two runtimes offer); nil: nowhere. warned: said once each.
	logf   func(format string, args ...any)
	warned map[string]bool
	// hostOff says a runtime is an engine host the serving node does not enable (Node.engineHostOff, set by attach):
	// its launchers are neither listed nor started. nil: every runtime may offer launchers.
	hostOff func(runtime string) bool
	// started: sessions the hub started through a runtime (session.start), by session id: the runtime that started
	// it and when. Another runtime that comes to run one (pi's companion starts sessions in pi-web) is told the hub
	// drove it (runtime.driven), so its cards go to the phone first as the starter's would.
	started     map[string]startedSession
	compactions map[string]*runtimeCompaction
	// agyWork: per-conversation Antigravity companion driving state, so the non-Linux presence fallback distinguishes
	// the companion's own turns and settling transcript exports from an external terminal agy (#14561).
	agyWork map[string]*agyCompanionTurn
	// lastInstance: runtime id -> most recent non-empty hello.Instance, used to distinguish same-instance reconnects
	// (whose persistent Assistant workers stay alive) from restarts/preemptions by a different instance.
	lastInstance map[string]string
}

// startedSession is who started a session for the hub, and when (runtimeRegistry.started).
type startedSession struct {
	runtime string
	at      time.Time
}

// drivenFor is how long after its start a session counts as driven from the hub (the runtime SDK's PHONE_DRIVEN_FOR).
const drivenFor = 6 * time.Hour

// maxStarted bounds runtimeRegistry.started; the oldest go first.
const maxStarted = 256

// noteStarted records that h started session id for the hub. Callers hold b.mu.
func (b *runtimeRegistry) noteStarted(h *runtimeConn, id string, now time.Time) {
	if b.started == nil {
		b.started = map[string]startedSession{}
	}
	b.started[id] = startedSession{runtime: h.hello.Runtime, at: now}
	for len(b.started) > maxStarted {
		oldest, at := "", now
		for k, v := range b.started {
			if !v.at.After(at) {
				oldest, at = k, v.at
			}
		}
		delete(b.started, oldest)
	}
}

// knownSession is the runtime a session belongs to, by the runtime's id (a reconnect is the same runtime).
type knownSession struct {
	runtime, label string
	remoteAgent    bool
	at             time.Time
}

// unclaimedID is an id no connected runtime had (runtimeRegistry.unclaimed): until when it is not asked about again,
// and the not-found of the runtime whose own id it is, when that one said so (nil otherwise).
type unclaimedID struct {
	until time.Time
	gone  error
}

// maxKnown bounds the remembered sessions; the oldest go first.
const maxKnown = 8192

func newRuntimeRegistry() *runtimeRegistry {
	return &runtimeRegistry{conns: map[int]*runtimeConn{}, owner: map[string]*runtimeConn{}, watching: map[string]int{},
		lastHistory: map[string]string{}, watchParents: map[string]context.Context{}, known: map[string]knownSession{},
		remoteSessions: map[string]struct{}{}, unclaimed: map[string]unclaimedID{}, warned: map[string]bool{},
		lastInstance: map[string]string{}}
}

// setHostOff replaces how launcher ownership excludes engine runtimes the serving node does not enable.
func (b *runtimeRegistry) setHostOff(off func(runtime string) bool) {
	b.mu.Lock()
	b.hostOff = off
	b.mu.Unlock()
}

// warnOnce logs a message once per key. Callers hold b.mu.
func (b *runtimeRegistry) warnOnce(key, format string, args ...any) {
	if b.logf == nil || b.warned[key] {
		return
	}
	b.warned[key] = true
	b.logf(format, args...)
}

// withShell merges runtime launchers with the login shell's, replacing only discovered CLI fallbacks. An explicit
// shell definition keeps its name (runtimeStart), so the form lists it once. Where the start's host is an
// engine runtime that runs only its own launchers, its list is its own and it runs its provider of the name
// (engineHostLaunchers, runtimeOfIn; runtimes_samekind.go). Logged once per name.
func (b *runtimeRegistry) withShell(runtime, shell []proto.Launcher) []proto.Launcher {
	names := map[string]int{}
	for i, l := range shell {
		names[l.Name] = i
	}
	out := slices.Clone(shell)
	for _, l := range runtime {
		if i, exists := names[l.Name]; exists {
			if shell[i].RuntimeFallback {
				out[i] = l
				continue
			}
			b.mu.Lock()
			b.warnOnce("shell launcher "+l.Name, "runtime %s: launcher %q has the name of a launcher in the login shell, which a start in a terminal host "+
				"runs instead; a start whose host is %s runs its own", l.Source, l.Name, l.Source)
			b.mu.Unlock()
			continue
		}
		out = append(out, l)
	}
	return out
}

// shellLauncherNames are explicit shell launchers, excluding discovered CLI fallbacks that connected runtimes may
// own (none before the first list; nothing is run to get them).
func shellLauncherNames() map[string]bool {
	launcherCache.Lock()
	defer launcherCache.Unlock()
	names := make(map[string]bool, len(launcherCache.list))
	for _, l := range launcherCache.list {
		if !l.RuntimeFallback {
			names[l.Name] = true
		}
	}
	return names
}

// hello records h's runtime.hello unless another process holds the same runtime id. A ready connection with that id
// and another instance is a second process configured alike: refused with runtime_conflict (one of them needs another
// id). One with the same instance is this process's earlier connection — it reconnected before the node saw the old
// socket close — returned as stale for the caller to close. A runtime that sends no instance (an older SDK) is let in
// as before: the newest connection answers for the id.
//
// The ids in daemons.json (reserved) are the daemons' (daemons.go): a runtime on the socket may not take one, and a
// daemon whose id a runtime on the socket already has stays out until that one leaves (it is dialed again). A daemon's
// link says hello with the config's id and an instance of its own, so its next connection is its own again.
func (b *runtimeRegistry) hello(h *runtimeConn, hello runtimeHello, reserved func(string) bool, preempt func(o *runtimeConn, hello runtimeHello) bool) (stale []*runtimeConn, err error) {
	stale, _, err = b.registerHello(h, hello, reserved, preempt)
	return stale, err
}

func (b *runtimeRegistry) registerHello(h *runtimeConn, hello runtimeHello, reserved func(string) bool, preempt func(o *runtimeConn, hello runtimeHello) bool) (stale []*runtimeConn, replacedSessions []string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h.mu.Lock()
	daemon := h.daemon
	h.mu.Unlock()
	switch {
	case daemon != "" && hello.Runtime != daemon:
		return nil, nil, &rpc.Error{Code: "runtime_conflict", Message: fmt.Sprintf("daemon %s said hello as %q", daemon, hello.Runtime)}
	case daemon == "" && reserved != nil && reserved(hello.Runtime):
		return nil, nil, &rpc.Error{Code: "runtime_conflict",
			Message: fmt.Sprintf("runtime id %q is reserved by a daemon in daemons.json; give this runtime another id", hello.Runtime)}
	}
	prevInstance := b.lastInstance[hello.Runtime]
	for _, o := range b.conns {
		if o == h {
			continue
		}
		o.mu.Lock()
		same := o.ready && o.hello.Runtime == hello.Runtime
		instance := o.hello.Instance
		onSocket := o.daemon == ""
		o.mu.Unlock()
		if !same {
			continue
		}
		if daemon != "" && onSocket {
			return nil, nil, &rpc.Error{Code: "runtime_conflict",
				Message: fmt.Sprintf("runtime id %q is in use by a runtime on this machine's socket", hello.Runtime)}
		}
		if preempt != nil && preempt(o, hello) {
			if instance != "" {
				prevInstance = instance
			}
			stale = append(stale, o)
			continue
		}
		if instance == "" || hello.Instance == "" {
			continue
		}
		if instance != hello.Instance {
			return nil, nil, &rpc.Error{Code: "runtime_conflict",
				Message: fmt.Sprintf("another process is connected to this node as runtime %q", hello.Runtime)}
		}
		stale = append(stale, o)
	}
	if prevInstance != "" && hello.Instance != "" && prevInstance != hello.Instance {
		drop := map[string]struct{}{}
		for _, o := range stale {
			for sid, owner := range b.owner {
				if owner == o {
					drop[sid] = struct{}{}
				}
			}
		}
		for sid, k := range b.known {
			if k.runtime == hello.Runtime {
				if owner := b.owner[sid]; owner == nil || slices.Contains(stale, owner) {
					drop[sid] = struct{}{}
				}
			}
		}
		if len(drop) > 0 {
			replacedSessions = make([]string, 0, len(drop))
			for sid := range drop {
				replacedSessions = append(replacedSessions, sid)
			}
		}
	}
	for _, o := range stale {
		o.superseded = true
	}
	if hello.Instance != "" {
		if b.lastInstance == nil {
			b.lastInstance = map[string]string{}
		}
		b.lastInstance[hello.Runtime] = hello.Instance
	}
	h.mu.Lock()
	h.hello, h.ready = hello, true
	h.assistantPIDs = nil
	h.mu.Unlock()
	return stale, replacedSessions, nil
}

func (b *runtimeRegistry) activeInstance(id string) (string, bool) {
	if id == "" {
		return "", false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	h := b.byRuntime(id)
	if h == nil {
		return "", false
	}
	h.mu.Lock()
	inst := h.hello.Instance
	h.mu.Unlock()
	return inst, true
}

func (b *runtimeRegistry) unownedSessions(sessions []string) []string {
	if len(sessions) == 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(sessions))
	for _, sid := range sessions {
		if sid != "" && b.owner[sid] == nil {
			out = append(out, sid)
		}
	}
	return out
}

// startedHere: a row of a session the runtime has, not one it lists only while it is starting it or after that start
// failed (#8130). Only the former is remembered as the runtime's (remember): a Claude session whose takeover failed is
// this machine's own again once its row leaves the list, so its next message takes it over again and its pages are
// its transcript's, rather than everything going on to a runtime that has no such session.
func startedHere(s proto.Session) bool {
	return !s.Starting && s.StartError == ""
}

// markRemoteLocked latches id (or its parent for a subagent) as having belonged to a remote assistant runtime.
// Callers hold b.mu.
func (b *runtimeRegistry) markRemoteLocked(id string) {
	if id == "" {
		return
	}
	if parent, _, ok := splitRuntimeChild(id); ok {
		id = parent
	}
	if b.remoteSessions == nil {
		b.remoteSessions = map[string]struct{}{}
	}
	b.remoteSessions[id] = struct{}{}
	if k, ok := b.known[id]; ok {
		k.remoteAgent = true
		b.known[id] = k
	}
}

func (b *runtimeRegistry) markRemote(id string) {
	if b == nil || id == "" {
		return
	}
	b.mu.Lock()
	b.markRemoteLocked(id)
	b.mu.Unlock()
}

// remember records that id belongs to h's runtime. Callers hold b.mu and h.mu.
func (b *runtimeRegistry) remember(h *runtimeConn, id string, now time.Time) {
	label := h.hello.Name
	if label == "" {
		label = h.hello.Runtime
	}
	isRemote := b.known[id].remoteAgent || (h.hello.Caps.Assistant && h.hello.Caps.RemoteAgent)
	if _, latched := b.remoteSessions[id]; latched {
		isRemote = true
	}
	if isRemote {
		b.markRemoteLocked(id)
	}
	b.known[id] = knownSession{runtime: h.hello.Runtime, label: label, remoteAgent: isRemote, at: now}
	delete(b.unclaimed, id)
	if len(b.known) > maxKnown {
		oldest := make([]string, 0, len(b.known))
		for k := range b.known {
			oldest = append(oldest, k)
		}
		sort.Slice(oldest, func(i, j int) bool { return b.known[oldest[i]].at.Before(b.known[oldest[j]].at) })
		for _, k := range oldest[:len(oldest)-maxKnown*3/4] {
			delete(b.known, k)
			if compact := b.compactions[k]; compact != nil && compact.state.State != proto.CompactionRunning {
				delete(b.compactions, k)
			}
		}
	}
}

// byRuntime is the newest ready connection of the runtime with this id, nil when it is not connected. Callers hold b.mu.
func (b *runtimeRegistry) byRuntime(rt string) *runtimeConn {
	var best *runtimeConn
	for _, h := range b.conns {
		h.mu.Lock()
		ok := h.ready && h.hello.Runtime == rt
		h.mu.Unlock()
		if ok && (best == nil || h.serial > best.serial) {
			best = h
		}
	}
	return best
}

// belongs is the runtime a session belongs to: the connection listing it, else the connected runtime that listed it
// before; known: it belongs to a runtime (connected or not), with that runtime's name. A subagent of a runtime's
// session ("<parent>@<child>", splitRuntimeChild) belongs to the parent's runtime, which answers for it.
//
// A session its runtime no longer lists and names as one of its Claude transcripts instead (unlisted_transcripts: an
// agent archived in Paseo, runtimeunlisted.go) is no longer the runtime's to answer for, whatever it listed before:
// it is a Claude session not running, read from its transcript, which a message has the machine's engine take over
// (continueInRuntime). Sent to the runtime, every call about it was refused until the node restarted and forgot it.
func (b *runtimeRegistry) belongs(id string) (h *runtimeConn, label string, known bool) {
	if b == nil || id == "" {
		return nil, "", false
	}
	if parent, _, ok := splitRuntimeChild(id); ok {
		id = parent
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if o := b.owner[id]; o != nil {
		return o, "", true
	}
	k, ok := b.known[id]
	if !ok || b.namesUnlisted(k.runtime, id) {
		return nil, "", false
	}
	return b.byRuntime(k.runtime), k.label, true
}

// owns: the session's calls and pushes are h's (it lists the session, or listed or claimed it before).
func (b *runtimeRegistry) owns(h *runtimeConn, id string) bool {
	got, _, _ := b.belongs(id)
	return got != nil && got == h
}

// isRemoteSession reports whether id (or any listing/claimed metadata for it) has been associated with a remote
// assistant runtime, failing closed on ambiguous ownership across multiple connections.
func (b *runtimeRegistry) isRemoteSession(id string) bool {
	if b == nil || id == "" {
		return false
	}
	if parent, _, ok := splitRuntimeChild(id); ok {
		id = parent
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.remoteSessions[id]; ok {
		return true
	}
	if k, ok := b.known[id]; ok && k.remoteAgent {
		return true
	}
	if o := b.owner[id]; o != nil && o.remoteAgent() {
		return true
	}
	for _, h := range b.conns {
		h.mu.Lock()
		isRemoteConn := h.ready && h.hello.Caps.Assistant && h.hello.Caps.RemoteAgent
		hasRow := false
		if isRemoteConn {
			for _, s := range h.sessions {
				if s.SessionID == id {
					hasRow = true
					break
				}
			}
		}
		h.mu.Unlock()
		if hasRow {
			return true
		}
	}
	if k, ok := b.known[id]; ok && !b.namesUnlisted(k.runtime, id) {
		if h := b.byRuntime(k.runtime); h != nil && h.remoteAgent() {
			return true
		}
	}
	return false
}

// claim records that h has the session (it answered for it).
func (b *runtimeRegistry) claim(h *runtimeConn, id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h.mu.Lock()
	b.remember(h, id, time.Now())
	h.mu.Unlock()
}

// askable are the connections to ask about a session nobody claimed yet, newest first; none while the id was asked
// about lately.
func (b *runtimeRegistry) askable(id string) []*runtimeConn {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if u, ok := b.unclaimed[id]; ok && time.Now().Before(u.until) {
		return nil
	}
	var out []*runtimeConn
	for _, h := range b.conns {
		h.mu.Lock()
		if h.ready {
			out = append(out, h)
		}
		h.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].serial > out[j].serial })
	return out
}

// unclaim: no runtime had the session; it is not asked about again for a minute. gone: the not-found of the runtime
// whose own id it is, given meanwhile for it (goneOf); nil when none said so.
func (b *runtimeRegistry) unclaim(id string, gone error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, u := range b.unclaimed {
		if time.Now().After(u.until) {
			delete(b.unclaimed, k)
		}
	}
	b.unclaimed[id] = unclaimedID{until: time.Now().Add(time.Minute), gone: gone}
}

// goneOf is the not-found the runtime whose own id it is gave for session id when it was asked lately (unclaim); nil
// when none did, or longer ago than a minute.
func (b *runtimeRegistry) goneOf(id string) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if u, ok := b.unclaimed[id]; ok && u.gone != nil && time.Now().Before(u.until) {
		return u.gone
	}
	return nil
}

// ready are the connections that said hello.
func (b *runtimeRegistry) ready() []*runtimeConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*runtimeConn
	for _, h := range b.conns {
		h.mu.Lock()
		if h.ready {
			out = append(out, h)
		}
		h.mu.Unlock()
	}
	return out
}

// resetWatches forgets what the hub watched (another hub connection, or none, from now on). Runtimes are not asked to
// stop: an unwatch could reach one after the next hub has watched the session again, and what they push meanwhile is
// harmless (the hub drops items nobody subscribed to).
func (b *runtimeRegistry) resetWatches() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.watching = map[string]int{}
	b.lastHistory = map[string]string{}
	b.watchParents = map[string]context.Context{}
	for _, h := range b.conns {
		h.mu.Lock()
		h.watched = map[string]bool{}
		h.watchTokens = map[string]uint64{}
		h.mu.Unlock()
	}
}

// assistantIn is the connected runtime that runs assistant conversations (caps.assistant in its hello) whose process
// is one of pids, nil when none is.
func (b *runtimeRegistry) assistantIn(pids []int) *runtimeConn {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, h := range b.conns {
		h.mu.Lock()
		ok := h.ready && h.hello.Caps.Assistant && h.pid > 0 && slices.Contains(pids, h.pid)
		h.mu.Unlock()
		if ok {
			return h
		}
	}
	return nil
}

func (b *runtimeRegistry) clearAssistantPIDs() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, h := range b.conns {
		h.mu.Lock()
		h.assistantPIDs = nil
		h.mu.Unlock()
	}
}

func (b *runtimeRegistry) setAssistantPIDs(h *runtimeConn, reports map[string]int, isAssistant func(string) bool) {
	h.mu.Lock()
	pid := h.pid
	ready := h.ready && h.hello.Caps.Assistant
	h.mu.Unlock()
	if !ready || pid <= 0 {
		return
	}
	verified := make(map[string]int)
	counts := make(map[int]int)
	// A process reported for two sessions is shared, even when one of those
	// sessions is an ordinary Agent. Never grant it assistant authority.
	for _, worker := range reports {
		counts[worker]++
	}
	for id, worker := range reports {
		if counts[worker] == 1 && runtimeSessionID(id) && strictDescendant(worker, pid, parentPID) && isAssistant(id) {
			verified[id] = worker
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conns[h.serial] != h {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ready && h.hello.Caps.Assistant && h.pid == pid {
		h.assistantPIDs = verified
	}
}

type runtimeAssistantWorker struct {
	host            *runtimeConn
	runtimePID, pid int
	session         string
}

func (b *runtimeRegistry) assistantWorkers() []runtimeAssistantWorker {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var workers []runtimeAssistantWorker
	for _, h := range b.conns {
		if h.peer != nil {
			select {
			case <-h.peer.Done():
				continue
			default:
			}
		}
		h.mu.Lock()
		if h.ready && h.hello.Caps.Assistant && h.pid > 0 {
			for id, worker := range h.assistantPIDs {
				workers = append(workers, runtimeAssistantWorker{host: h, runtimePID: h.pid, pid: worker, session: id})
			}
		}
		h.mu.Unlock()
	}
	return workers
}

func (b *runtimeRegistry) add(peer *rpc.Peer) *runtimeConn {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.serial++
	h := &runtimeConn{serial: b.serial, peer: peer, watched: map[string]bool{}, watchTokens: map[string]uint64{}}
	b.conns[h.serial] = h
	return h
}

func (b *runtimeRegistry) remove(h *runtimeConn) []string {
	id := h.id()
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.conns, h.serial)
	h.mu.Lock()
	h.assistantPIDs = nil
	h.mu.Unlock()
	var owned []string
	if !h.superseded {
		for sid, o := range b.owner {
			if o == h {
				owned = append(owned, sid)
			}
		}
	}
	b.leftUnlisted(id)
	b.leftRows(h, id)
	b.leftCodexClaims(id)
	b.agyWorkRemoveConnLocked(h, agyPresenceNow())
	b.recomputeOwners()
	if h.superseded {
		return nil
	}
	drop := map[string]struct{}{}
	for _, sid := range owned {
		if b.owner[sid] == nil {
			drop[sid] = struct{}{}
		}
	}
	if id != "" && b.byRuntime(id) == nil {
		for sid, k := range b.known {
			if k.runtime == id && b.owner[sid] == nil {
				drop[sid] = struct{}{}
			}
		}
	}
	if len(drop) == 0 {
		return nil
	}
	released := make([]string, 0, len(drop))
	for sid := range drop {
		released = append(released, sid)
	}
	return released
}

// recomputeOwners picks, for every listed session, the connection that answers for it. Two runtimes can list the
// same session: pi's web server lists sessions it can open from disk (status offline) while a pi in a terminal has
// one of them running. The one running it wins — a send must not reach a second writer of a file pi does not lock —
// and among equals the newest connection (a runtime that reconnected). Callers hold b.mu.
func (b *runtimeRegistry) recomputeOwners() {
	type claim struct {
		h    *runtimeConn
		live bool
	}
	best := map[string]claim{}
	for _, h := range b.conns {
		h.mu.Lock()
		for _, s := range h.sessions {
			c := claim{h: h, live: s.Status != proto.StatusOffline}
			o, seen := best[s.SessionID]
			if !seen || c.live && !o.live || c.live == o.live && h.serial > o.h.serial {
				best[s.SessionID] = c
			}
		}
		h.mu.Unlock()
	}
	b.owner = make(map[string]*runtimeConn, len(best))
	for id, c := range best {
		b.owner[id] = c.h
	}
}

// runtimeWatchAttempt is one registered session.watch RPC. The token changes for every explicit or automatic attempt;
// since is the sequence to resume from. A failure may clear watched only while it still belongs to this attempt.
type runtimeWatchAttempt struct {
	since int
	token uint64
}

// beginWatchLocked installs a fresh attempt while watched continues to suppress duplicate automatic calls, including
// while the RPC is pending. Callers hold b.mu and h.mu.
func (b *runtimeRegistry) beginWatchLocked(h *runtimeConn, id string, since int) runtimeWatchAttempt {
	h.nextWatchToken++
	if h.watched == nil {
		h.watched = map[string]bool{}
	}
	if h.watchTokens == nil {
		h.watchTokens = map[string]uint64{}
	}
	h.watched[id] = true
	h.watchTokens[id] = h.nextWatchToken
	return runtimeWatchAttempt{since: since, token: h.nextWatchToken}
}

// setSessions records a runtime's list and returns the watched sessions this connection has not been asked to watch
// yet (with the sequence and generation of each attempt), and the sessions it now runs that another runtime started
// for the hub lately, which it has not been told of yet (runtime.driven).
func (b *runtimeRegistry) setSessions(h *runtimeConn, list []proto.Session) (map[string]runtimeWatchAttempt, []string) {
	return b.setSessionList(h, list, nil, nil)
}

func (b *runtimeRegistry) setSessionList(h *runtimeConn, list []proto.Session, agyPaneBackground map[string][]proto.BackgroundTask, waits map[runtimeInputKey]runtimeInputReport) (map[string]runtimeWatchAttempt, []string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h.mu.Lock()
	h.sessions = list
	h.sessionsListed = true
	h.agyPaneBackground = agyPaneBackground
	h.inputWaits = b.reconcileInputs(h, list, waits)
	if h.hello.Runtime == agyRuntimeID && h.daemon == "" {
		h.agyPreviews = nextAgyPreviews(h.agyPreviews, list)
	}
	rt := h.hello.Runtime
	agent := h.hello.Agent
	if agent == "" {
		agent = rt
	}
	h.mu.Unlock()
	b.backAgain(rt) // its rows while it was away (runtimeaway.go): this list says what it has
	b.recomputeOwners()
	rewatch := map[string]runtimeWatchAttempt{}
	var driven []string
	now := time.Now()
	b.agyWorkObserveSessionsLocked(h, agent, list, agyPresenceNow())
	h.mu.Lock()
	isRemoteConn := h.hello.Caps.Assistant && h.hello.Caps.RemoteAgent
	for _, s := range list {
		if isRemoteConn {
			b.markRemoteLocked(s.SessionID)
		}
		if b.owner[s.SessionID] != h {
			continue // another runtime has it running (recomputeOwners)
		}
		if startedHere(s) {
			b.remember(h, s.SessionID, now)
		}
		if compact := b.compactions[s.SessionID]; compact != nil && compact.runtime == h {
			compact.observe(s)
		}
		if st, ok := b.started[s.SessionID]; ok && st.runtime != h.hello.Runtime && now.Sub(st.at) < drivenFor &&
			s.Status != proto.StatusOffline && !h.drivenTold[s.SessionID] {
			if h.drivenTold == nil {
				h.drivenTold = map[string]bool{}
			}
			h.drivenTold[s.SessionID] = true
			driven = append(driven, s.SessionID)
		}
		if since, ok := b.watching[s.SessionID]; ok && !h.watched[s.SessionID] {
			rewatch[s.SessionID] = b.beginWatchLocked(h, s.SessionID, since)
		}
	}
	h.mu.Unlock()
	return rewatch, driven
}

// assistantRuntimeByPrefix returns the newest ready assistant runtime whose runtime id prefixes id ("<runtime>-...").
func (b *runtimeRegistry) assistantRuntimeByPrefix(id string) (*runtimeConn, bool) {
	if b == nil || id == "" {
		return nil, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var best *runtimeConn
	for _, h := range b.conns {
		h.mu.Lock()
		ok := h.ready && h.hello.Caps.Assistant && h.hello.Runtime != "" && strings.HasPrefix(id, h.hello.Runtime+"-")
		h.mu.Unlock()
		if ok && (best == nil || h.serial > best.serial) {
			best = h
		}
	}
	return best, best != nil
}

// unlistedAssistantRuntime reports a ready assistant runtime that has not yet pushed its first runtime.sessions list.
func (b *runtimeRegistry) unlistedAssistantRuntime() (string, bool) {
	if b == nil {
		return "", false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var first *runtimeConn
	var label string
	for _, h := range b.conns {
		h.mu.Lock()
		pending := h.ready && h.hello.Caps.Assistant && !h.sessionsListed
		name := h.hello.Name
		if name == "" {
			name = h.hello.Runtime
		}
		h.mu.Unlock()
		if pending && (first == nil || h.serial < first.serial) {
			first, label = h, name
		}
	}
	if first == nil {
		return "", false
	}
	return label, true
}

func (b *runtimeRegistry) ownerOf(id string) *runtimeConn {
	if b == nil || id == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.owner[id]
}

// appendSessions appends every connected runtime's sessions to dst, each id once, in the order the runtimes connected:
// straight into the caller's list (sessionList's), not into a slice of their own grown row by row and copied again.
// Then the rows the runtimes that left are still listed with (runtimeaway.go). b.mu is held while each connection's
// rows are read under its h.mu, the order setSessions takes them in; nothing here calls out of the registry.
func (b *runtimeRegistry) appendSessions(dst []proto.Session) []proto.Session {
	if b == nil {
		return dst
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	conns := make([]*runtimeConn, 0, len(b.conns))
	for _, h := range b.conns {
		conns = append(conns, h)
	}
	sort.Slice(conns, func(i, j int) bool { return conns[i].serial < conns[j].serial })
	for _, h := range conns {
		h.mu.Lock()
		for i := range h.sessions {
			if b.owner[h.sessions[i].SessionID] == h {
				row := h.sessions[i]
				if compact := b.compactions[row.SessionID]; compact != nil {
					row.Compaction = compact.state
					if compact.silent && compact.state != nil && compact.state.State == proto.CompactionRunning &&
						row.Status == proto.StatusWorking && compact.prevStatus != "" {
						row.Status = compact.prevStatus
					}
				}
				dst = append(dst, row)
			}
		}
		h.mu.Unlock()
	}
	return b.appendAway(dst)
}

func (b *runtimeRegistry) wasStarted(id string) bool {
	if b == nil || id == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.started[id]
	return ok
}

func (b *runtimeRegistry) agySnapshots() []agyRuntimeSnapshot {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []agyRuntimeSnapshot
	for _, h := range b.conns {
		if h.peer != nil {
			select {
			case <-h.peer.Done():
				continue
			default:
			}
		}
		h.mu.Lock()
		agent := h.hello.Agent
		if agent == "" {
			agent = h.hello.Runtime
		}
		if h.ready && agent == agyAgent {
			for i := range h.sessions {
				if b.owner[h.sessions[i].SessionID] == h {
					out = append(out, agyRuntimeSnapshot{host: h, row: h.sessions[i]})
				}
			}
		}
		h.mu.Unlock()
	}
	return out
}

// launcherOwners maps each launcher name the runtimes offer to the one connection that answers for it: the one
// connected first, so the form and session.start agree and do not change while both stay connected. The others'
// launcher of that name is not offered (logged once). A disabled engine runtime (b.hostOff) is left out before it can
// claim a name, so it never hides an enabled runtime's launcher of the same name. Callers hold b.mu.
func (b *runtimeRegistry) launcherOwners() map[string]*runtimeConn {
	conns := make([]*runtimeConn, 0, len(b.conns))
	for _, h := range b.conns {
		conns = append(conns, h)
	}
	sort.Slice(conns, func(i, j int) bool { return conns[i].serial < conns[j].serial })
	owners := map[string]*runtimeConn{}
	ownerIDs := map[string]string{}
	for _, h := range conns {
		h.mu.Lock()
		rt, ok := h.hello.Runtime, h.ready && h.hello.Caps.Start
		h.mu.Unlock()
		if !ok || b.hostOff != nil && b.hostOff(rt) {
			continue
		}
		for _, l := range h.launcherList() {
			name := l.Name
			if owners[name] != nil {
				b.warnOnce(fmt.Sprintf("launcher %d %s", h.serial, name), "runtime %s #%d: launcher %q is offered by runtime %s already; not offered twice",
					rt, h.serial, name, ownerIDs[name])
				continue
			}
			owners[name], ownerIDs[name] = h, rt
		}
	}
	return owners
}

// launchers are the runtimes' entries for the new-session form.
func (b *runtimeRegistry) launchers() []proto.Launcher {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []proto.Launcher
	for name, h := range b.launcherOwners() {
		l, _ := h.launcherNamed(name)
		h.mu.Lock()
		out = append(out, h.formLauncherLocked(l))
		h.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// formLauncherLocked is h's launcher l as the new-session form lists it. Agent, as for a Codex launcher
// (proto.Launcher.Agent): what it starts is not Claude Code, so the apps do not offer it where only Claude Code or
// Codex can run (the Persona assistant). The permission modes it lists only then: Claude Code's are the apps' own
// five (#8261). Callers hold h.mu.
func (h *runtimeConn) formLauncherLocked(l runtimeLauncher) proto.Launcher {
	agent := l.Agent
	if agent == "" {
		agent = h.hello.Agent
	}
	if agent == "" {
		agent = h.hello.Runtime
	}
	modes := l.PermissionModes
	if agent == "claude" {
		agent, modes = "", 0
	}
	assistant := h.hello.Caps.Assistant && l.Name == h.hello.Launcher
	assistantFor := ""
	if assistant && l.AssistantFor != l.Name { // only the launcher that runs the assistant says what it runs it for
		assistantFor = l.AssistantFor
	}
	builtinCompanion := h.builtinAgy && h.daemon == "" && h.hello.Runtime == agyRuntimeID && agent == agyAgent
	return proto.Launcher{Name: l.Name, Model: l.Model, Source: h.hello.Runtime, Agent: agent, PromptOptional: l.PromptOptional,
		Assistant: assistant, SupervisionPolicy: assistant && h.hello.Caps.SupervisionPolicy, AssistantFor: assistantFor, AutoAccept: l.AutoAccept, DisplayName: l.DisplayName, PermissionModes: modes,
		BuiltinCompanion: builtinCompanion}
}

// launcherRuntime is the runtime offering a launcher name, nil when none does.
func (b *runtimeRegistry) launcherRuntime(name string) *runtimeConn {
	if b == nil || name == "" {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.launcherOwners()[name]
}

func (b *runtimeRegistry) watch(h *runtimeConn, id string, since int, history string, parent context.Context) runtimeWatchAttempt {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cur, ok := b.watching[id]; !ok || since > cur || history != "" && history != b.lastHistory[id] {
		b.watching[id] = since
	}
	if history != "" {
		b.lastHistory[id] = history
	}
	if parent != nil {
		b.watchParents[id] = parent
	} else {
		delete(b.watchParents, id)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return b.beginWatchLocked(h, id, since)
}

// watchFailed releases a failed attempt so the next list can retry it. watched is connection-private: clearing h's
// current attempt cannot clear another owner's state, whether this connection is temporarily not the owner or another
// connection has taken over. The registered serial fences a departed connection, while the token fences newer attempts
// and unwatch/reset generations. Success deliberately has no registry write: watched covers pending and established watches.
func (b *runtimeRegistry) watchFailed(h *runtimeConn, id string, token uint64) {
	if b == nil || h == nil || token == 0 {
		return
	}
	if h.onWatchFailed != nil {
		defer h.onWatchFailed()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conns[h.serial] != h {
		return
	}
	if _, ok := b.watching[id]; !ok {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.watched[id] && h.watchTokens[id] == token {
		delete(h.watched, id)
		delete(h.watchTokens, id)
	}
}

func (b *runtimeRegistry) unwatch(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.watching, id)
	delete(b.lastHistory, id)
	delete(b.watchParents, id)
	for _, h := range b.conns {
		h.mu.Lock()
		delete(h.watched, id)
		delete(h.watchTokens, id)
		h.mu.Unlock()
	}
}

// watchedOn: the hub watches session id, and connection h was asked to watch it (watch, setSessions).
func (b *runtimeRegistry) watchedOn(h *runtimeConn, id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	_, watching := b.watching[id]
	return watching && h.watched[id]
}

// watchParent is the connection a transcriptMoved watch must inherit, including when the original watch was runtime-only.
// A parent already gone is forgotten rather than given a persistent replacement.
func (b *runtimeRegistry) watchParent(id string) context.Context {
	b.mu.Lock()
	defer b.mu.Unlock()
	parent := b.watchParents[id]
	if parent != nil && parent.Err() != nil {
		delete(b.watchParents, id)
		return nil
	}
	return parent
}

// lastPushed is the last seq pushed for a watched session (0: not watched, or nothing yet).
func (b *runtimeRegistry) lastPushed(id string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.watching[id]
}

// pushed tracks the current history's cursor, including replacements that shorten it.
func (b *runtimeRegistry) pushed(id string, seq int, history string, reset bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cur, ok := b.watching[id]
	if !ok {
		return
	}
	if reset || history != "" && history != b.lastHistory[id] || seq > cur {
		b.watching[id] = seq
	}
	if history != "" {
		b.lastHistory[id] = history
	}
}

// sessionInPane is the session a runtime runs in the terminal pane (its list's pane_id, as own writes it: node ownPane
// takes the node's own herdr session off), "" when none says so. Only sessions it lists as running: a pane outlives
// the pi that told it.
func (b *runtimeRegistry) sessionInPane(pane string, own func(string) string) string {
	if b == nil || pane == "" {
		return ""
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, h := range b.conns {
		h.mu.Lock()
		for _, s := range h.sessions {
			// The hint as the node writes panes, or bare as an extension that does not name the session sends it.
			if p := h.panes[s.SessionID]; p != "" && (p == pane || own(p) == pane) && s.Status != proto.StatusOffline {
				h.mu.Unlock()
				return s.SessionID
			}
		}
		h.mu.Unlock()
	}
	return ""
}

// rowOf is the row of the runtime that answers for session id (ownerOf), with that runtime.
func (b *runtimeRegistry) rowOf(id string) (proto.Session, *runtimeConn, bool) {
	h := b.ownerOf(id)
	if h == nil {
		return proto.Session{}, nil, false
	}
	s, ok := h.row(id)
	return s, h, ok
}

// unlistedRuntimeByPrefix reports a ready runtime matching id's "<runtime>-" prefix that has not pushed its first
// runtime.sessions list yet.
func (b *runtimeRegistry) unlistedRuntimeByPrefix(id string) (string, bool) {
	if b == nil || id == "" {
		return "", false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var first *runtimeConn
	var label string
	for _, h := range b.conns {
		h.mu.Lock()
		pending := h.ready && !h.sessionsListed && h.hello.Runtime != "" && strings.HasPrefix(id, h.hello.Runtime+"-")
		name := h.hello.Name
		if name == "" {
			name = h.hello.Runtime
		}
		h.mu.Unlock()
		if pending && (first == nil || h.serial < first.serial) {
			first, label = h, name
		}
	}
	if first == nil {
		return "", false
	}
	return label, true
}

// hasListedRuntimePrefix reports whether any ready connected runtime using id's "<runtime>-" prefix has already
// pushed its initial runtime.sessions list.
func (b *runtimeRegistry) hasListedRuntimePrefix(id string) bool {
	if b == nil || id == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, h := range b.conns {
		h.mu.Lock()
		listed := h.ready && h.sessionsListed && h.hello.Runtime != "" && strings.HasPrefix(id, h.hello.Runtime+"-")
		h.mu.Unlock()
		if listed {
			return true
		}
	}
	return false
}
