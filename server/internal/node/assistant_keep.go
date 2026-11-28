package node

import (
	"cmp"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
)

// A resident node keeps its headless conversations when the hub's connection ends (docs/operations/hub-restart.md "Chats not interrupted: hosting by the resident node"):
// a hub restart or a reconnect replaces the connection, and with it the Node serving it (attach.go), but the assistant
// processes go on — their turn, their background tasks and monitors — and the Node serving the next connection takes
// them over: what they say from then on reaches the new hub, the answer being generated included. Without a resident
// node (`node --stdio` serving the hub itself) the process ends with the connection, and so do they: Serve returns
// only once reapAssistants has stopped them and they have exited (stopAndWait), or they would go on without a node
// beside the processes the next node starts for the same conversations (#4259).

// liveAssistants is every headless assistant this process runs, whichever Node it reports to (AssistantsBusy).
var liveAssistants sync.Map // *assistantProc -> struct{}

// busyAssistant describes one live headless assistant blocking a resident reload or update.
type busyAssistant struct {
	id         string
	busy       bool
	compacting bool
	tasks      int
}

func busyAssistants() []busyAssistant {
	var out []busyAssistant
	liveAssistants.Range(func(k, _ any) bool {
		p := k.(*assistantProc)
		p.mu.Lock()
		if p.alive() && (p.busy || p.compacting || len(p.tasks) > 0) {
			out = append(out, busyAssistant{
				id:         p.id,
				busy:       p.busy,
				compacting: p.compacting,
				tasks:      len(p.tasks),
			})
		}
		p.mu.Unlock()
		return true
	})
	return out
}

func hasBusyBackgroundTasks(busy []busyAssistant) bool {
	for _, b := range busy {
		if b.tasks > 0 {
			return true
		}
	}
	return false
}

func busyIDSet(busy []busyAssistant) map[string]struct{} {
	ids := make(map[string]struct{}, len(busy))
	for _, b := range busy {
		ids[b.id] = struct{}{}
	}
	return ids
}

func anyIDBusy(busy []busyAssistant, ids map[string]struct{}) bool {
	for _, b := range busy {
		if _, ok := ids[b.id]; ok {
			return true
		}
	}
	return false
}

// formatBusyAssistants returns a deterministic, privacy-safe summary of blocking conversations
// (truncated conversation id and reason/task count only, never task descriptions or prompt text).
func formatBusyAssistants(busy []busyAssistant) string {
	sorted := append([]busyAssistant(nil), busy...)
	slices.SortFunc(sorted, func(a, b busyAssistant) int {
		if c := cmp.Compare(a.id, b.id); c != 0 {
			return c
		}
		if c := cmp.Compare(a.tasks, b.tasks); c != 0 {
			return c
		}
		if a.busy != b.busy {
			if a.busy {
				return -1
			}
			return 1
		}
		if a.compacting != b.compacting {
			if a.compacting {
				return -1
			}
			return 1
		}
		return 0
	})
	parts := make([]string, 0, len(sorted))
	for _, b := range sorted {
		var reasons []string
		if b.busy {
			reasons = append(reasons, "busy")
		}
		if b.compacting {
			reasons = append(reasons, "compacting")
		}
		if b.tasks > 0 {
			reasons = append(reasons, fmt.Sprintf("tasks=%d", b.tasks))
		}
		label := short(b.id)
		if label == "" {
			label = "unknown"
		}
		parts = append(parts, fmt.Sprintf("conv=%s (%s)", label, strings.Join(reasons, ", ")))
	}
	return strings.Join(parts, ", ")
}

// AssistantsBusy: a conversation is answering or waiting on its background tasks or monitors; a resident node asked
// to restart (SIGHUP, a deploy) waits until none is (cmd/personad runResident). Idle ones resume on the next message.
func AssistantsBusy() bool {
	return len(busyAssistants()) > 0
}

// keeperStopGrace is how long stopAndWait gives conversations to end on SIGTERM before it kills their process
// groups; keeperKillWait bounds the wait after that SIGKILL. Variables so tests can shorten them.
var (
	keeperStopGrace = 5 * time.Second
	keeperKillWait  = 2 * time.Second
)

// stopAll ends what is kept: the resident node itself is ending (stopAndWait).
func (k *assistantKeeper) stopAll() {
	k.mu.Lock()
	kept := k.procs
	k.procs = map[string]*assistantProc{}
	warm := k.cacheWarm
	k.mu.Unlock()
	if warm != nil {
		warm.close()
	}
	procs := make([]*assistantProc, 0, len(kept))
	for _, p := range kept {
		procs = append(procs, p)
	}
	if left := stopAndWait(procs); left > 0 {
		log.Printf("node: %d kept conversations still running after SIGKILL", left)
	}
}

// stopAndWait stops procs and returns once they have exited; the number still running after SIGKILL. Its caller's
// process ends right after (a resident node exiting or exec'ing, nodecmd runResident; a node whose connection ended,
// Serve), and the SIGKILL fallback stop() leaves to a goroutine goes with it, so a conversation that ignores SIGTERM
// would go on orphaned, holding its gate and files. Nothing else will finish the job: it waits for them, kills the
// process groups of those still running after keeperStopGrace, and waits for those too.
func stopAndWait(procs []*assistantProc) int {
	var running []*assistantProc
	for _, p := range procs {
		p.stop()
		if p.cmd != nil && p.cmd.Process != nil && p.exited != nil { // never started: nothing to wait for
			running = append(running, p)
		}
	}
	left := waitExited(running, keeperStopGrace)
	for _, p := range left {
		_ = osproc.StopGroup(p.cmd.Process.Pid, true)
	}
	return len(waitExited(left, keeperKillWait))
}

// waitExited waits up to within for every process in procs to exit and returns the ones still running. exited closes
// only after cmd.Wait has reaped the process, so the pid of one still running, and with it its process group, cannot
// have been reused: killing that group reaches nobody else.
func waitExited(procs []*assistantProc, within time.Duration) (left []*assistantProc) {
	if len(procs) == 0 {
		return nil
	}
	timer := time.NewTimer(within)
	defer timer.Stop()
	for i, p := range procs {
		select {
		case <-p.exited:
		case <-timer.C:
			for _, q := range procs[i:] {
				if q.alive() {
					left = append(left, q)
				}
			}
			return left
		}
	}
	return nil
}

// assistantKeeper holds the conversations between two connections of a resident node.
type assistantKeeper struct {
	cacheWarm      *assistantCachePool
	mu             sync.Mutex
	procs          map[string]*assistantProc
	recoveryOwners map[string]*Node              // each session's previous Node, holding its recovery under that Node's mu
	fallbacks      map[string]*assistantFallback // the conversations' holds on a fallback (assistant_fallback_hold.go)
	unavailable    map[string]time.Time          // models a recovery found the gateway does not serve, by launcher
	// gp is the process's gate proxy: the conversations kept hold their addresses at it (Node.gates, gateproxy.go).
	gp *gateProxy
}

func newAssistantKeeper() *assistantKeeper {
	return &assistantKeeper{procs: map[string]*assistantProc{}, recoveryOwners: map[string]*Node{}}
}

// keep takes the conversations of a Node whose connection ended; the ones it cannot keep are returned, to be stopped.
// Recovery stays in the old Node's closed map until adopt, so a result in the gap can still be queued without a data race.
func (k *assistantKeeper) keep(owner *Node, procs map[string]*assistantProc, recoveries map[string]*assistantTurnRecovery) (stop []*assistantProc) {
	k.mu.Lock()
	defer k.mu.Unlock()
	for id, p := range procs {
		if (p.agent == "claude" || p.agent == "codex" || p.agent == traexAgent) && p.alive() {
			k.procs[id] = p
			if recoveries[id] != nil {
				k.recoveryOwners[id] = owner
			} else {
				delete(k.recoveryOwners, id)
			}
		} else {
			stop = append(stop, p)
			delete(k.recoveryOwners, id)
		}
	}
	return stop
}

// keepFallbacks takes an ended connection's holds on a fallback, and the models found unavailable, for the next one:
// a hub restart does not move the conversations back onto a model that refused them or does not exist. The caller holds the owner's mu (the order keep takes them in).
func (k *assistantKeeper) keepFallbacks(fallbacks map[string]*assistantFallback, unavailable map[string]time.Time) {
	if len(fallbacks) == 0 && len(unavailable) == 0 {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(fallbacks) > 0 && k.fallbacks == nil {
		k.fallbacks = map[string]*assistantFallback{}
	}
	for id, f := range fallbacks {
		k.fallbacks[id] = f
	}
	if len(unavailable) > 0 && k.unavailable == nil {
		k.unavailable = map[string]time.Time{}
	}
	for key, until := range unavailable {
		k.unavailable[key] = until
	}
}

// carryRecovery records owner as the Node whose closed map holds p's recovery, while the keeper still holds p: true
// when the next adopt will carry it. owner.mu is held (the order keep takes them in).
func (k *assistantKeeper) carryRecovery(p *assistantProc, owner *Node) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.procs[p.id] != p {
		return false
	}
	k.recoveryOwners[p.id] = owner
	return true
}

// adopt hands what is kept to the Node serving the new connection, including the remaining per-model retry budget.
func (k *assistantKeeper) adopt(n *Node) []*assistantProc {
	k.mu.Lock()
	kept := k.procs
	k.procs = map[string]*assistantProc{}
	recoveryOwners := k.recoveryOwners
	k.recoveryOwners = map[string]*Node{}
	fallbacks, unavailable := k.fallbacks, k.unavailable
	k.fallbacks, k.unavailable = nil, nil
	k.mu.Unlock()
	if len(unavailable) > 0 {
		n.mu.Lock()
		if n.unavailable == nil {
			n.unavailable = map[string]time.Time{}
		}
		for key, until := range unavailable {
			n.unavailable[key] = until
		}
		n.mu.Unlock()
	}
	if len(fallbacks) > 0 {
		n.mu.Lock()
		if n.fallbacks == nil {
			n.fallbacks = map[string]*assistantFallback{}
		}
		for id, f := range fallbacks {
			n.fallbacks[id] = f
		}
		n.mu.Unlock()
	}
	var out []*assistantProc
	var resume []adoptedFailure
	for id, p := range kept {
		if !p.alive() {
			continue
		}
		previous := recoveryOwners[id]
		if previous == n {
			previous = nil // tests and embedders do not normally readopt into the same Node
		}
		if previous != nil {
			previous.mu.Lock()
		}
		var r *assistantTurnRecovery
		var pending *assistantPendingFailure
		if previous != nil {
			r = previous.recoveries[id]
			if r != nil {
				pending = r.pending
				p.mu.Lock()
				busy := p.busy
				p.mu.Unlock()
				if r.finished || (!busy && pending == nil) {
					r = nil // a result completed between handover and adoption; there is nothing left to recover
				} else {
					r.running, r.done, r.proc = false, nil, p // pending stays until resumeAdoptedFailures: it still holds the turn
				}
				delete(previous.recoveries, id)
			}
		}
		n.mu.Lock()
		p.owner.Store(n)
		n.procs[id] = p
		if p.cx != nil {
			n.akeys[p.cx.key] = p
		}
		if r != nil {
			if n.recoveries == nil {
				n.recoveries = map[string]*assistantTurnRecovery{}
			}
			n.recoveries[id] = r
		}
		n.mu.Unlock()
		if previous != nil {
			previous.mu.Unlock()
		}
		out = append(out, p)
		if pending != nil && r != nil {
			resume = append(resume, adoptedFailure{p, r, pending})
		}
	}
	n.mu.Lock()
	n.adoptedFailures = append(n.adoptedFailures, resume...)
	n.mu.Unlock()
	return out
}

// adoptedFailure is a failed turn a handover carried, with its recovery and the result to resume it from.
type adoptedFailure struct {
	p       *assistantProc
	r       *assistantTurnRecovery
	failure *assistantPendingFailure
}

// resumeAdoptedFailures goes on with the failures adopt carried, once the session state has loaded (startState): a
// send before it would read an empty state (no guest restrictions, no main flag, no launcher) and write it back (#3495).
// Without the state, or when one cannot go on, the failure is published as final.
func (n *Node) resumeAdoptedFailures(loaded bool) {
	n.mu.Lock()
	resume := n.adoptedFailures
	n.adoptedFailures = nil
	n.mu.Unlock()
	for _, item := range resume {
		n.mu.Lock()
		if n.recoveries[item.p.id] == item.r {
			item.r.pending = nil // going on from it now
		}
		n.mu.Unlock()
		if !loaded || !n.recoverAssistantResultFor(item.p, item.r, true, item.failure.text, item.failure.kinds...) {
			n.finishAssistantRecovery(item.p, item.r)
		}
	}
}

// forget drops a kept process that exited between two connections.
func (k *assistantKeeper) forget(p *assistantProc) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.procs[p.id] == p {
		delete(k.procs, p.id)
	}
}

// ownerOr is the Node a process reports to: the one serving the hub now if it was taken over, else n (which started
// it).
func (p *assistantProc) ownerOr(n *Node) *Node {
	if o := p.owner.Load(); o != nil {
		return o
	}
	return n
}

// resumeAssistants: the Node serving a new connection took kept conversations over. Their state goes to the hub
// (which lost nothing it had been told, but this Node's state starts from what the hub has), and the text being
// generated is sent again so an app that reconnects sees the answer go on where it was.
func (n *Node) resumeAssistants(adopted []*assistantProc) {
	for _, p := range adopted {
		p.mu.Lock()
		tasks := append([]string(nil), p.tasks...)
		text := p.partial.String()
		busy := p.busy
		p.mu.Unlock()
		n.rememberTasks(p.id, tasks)
		if busy && text != "" {
			n.emitDelta(p.id, text)
		}
	}
	if len(adopted) > 0 {
		n.logger.Printf("took over %d running conversations from the last hub connection", len(adopted))
		n.notifySessions()
	}
}

// handOverAssistants leaves this Node's conversations to the next connection (Serve's end, before the next one
// starts: attach.go waits for it) and stops what cannot be kept.
func (n *Node) handOverAssistants() {
	n.mu.Lock()
	n.recoveryClosed = true
	recovering := make([]string, 0, len(n.recoveries))
	for id := range n.recoveries {
		recovering = append(recovering, id)
	}
	n.mu.Unlock()
	for _, id := range recovering {
		for {
			n.mu.Lock()
			r := n.recoveries[id]
			var done chan struct{}
			if r != nil {
				done = r.done
			}
			n.mu.Unlock()
			if r == nil || done == nil {
				break
			}
			<-done // the active send has delivered or the chain has published its final state; no old sender remains
		}
		// Also serialize with any ordinary in-flight send before the process changes owner.
		unlock := n.typing.lock(id)
		unlock()
	}
	n.mu.Lock()
	stop := n.keeper.keep(n, n.procs, n.recoveries)
	n.keeper.keepFallbacks(n.fallbacks, n.unavailable)
	n.fallbacks, n.unavailable = nil, nil
	n.procs = map[string]*assistantProc{}
	n.akeys = map[string]*assistantProc{}
	n.mu.Unlock()
	for _, p := range stop {
		p.stop()
	}
}
