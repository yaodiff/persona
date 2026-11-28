package node

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// agyIdleRetryCooldown avoids hammering a session when an automatic compaction attempt fails.
const agyIdleRetryCooldown = 5 * time.Minute

// agyIdleCompactMaxConcurrent bounds how many automatic idle compactions can run concurrently across a node
// so a cold start or periodic sweep does not spawn an unbounded storm of Antigravity ACP workers at once.
var agyIdleCompactMaxConcurrent = 2

func (n *Node) tryAcquireAgyIdleSlot() bool {
	max := agyIdleCompactMaxConcurrent
	if max <= 0 {
		max = 1
	}
	n.agyIdleMu.Lock()
	defer n.agyIdleMu.Unlock()
	if n.agyIdleInFlight >= max {
		return false
	}
	n.agyIdleInFlight++
	return true
}

func (n *Node) releaseAgyIdleSlot() {
	n.agyIdleMu.Lock()
	if n.agyIdleInFlight > 0 {
		n.agyIdleInFlight--
	}
	n.agyIdleMu.Unlock()
}

func (n *Node) agyNow() time.Time {
	n.agyIdleMu.Lock()
	clock := n.agyIdleClock
	n.agyIdleMu.Unlock()
	if clock != nil {
		return clock()
	}
	return time.Now()
}

type agyIdleState struct {
	host          *runtimeConn
	gen           uint64
	attemptToken  uint64
	safeIdleSince time.Time
	lastRowTime   time.Time
	lastTokens    int
	attempted     bool
	lastAttempt   time.Time
}

type agyIdlePruneCandidate struct {
	id    string
	host  *runtimeConn
	state *agyIdleState
	gen   uint64
}

func runtimeConnClosed(h *runtimeConn) bool {
	if h == nil || h.peer == nil {
		return false
	}
	select {
	case <-h.peer.Done():
		return true
	default:
		return false
	}
}

func (b *runtimeRegistry) hasUnlistedAgyRuntime(rt string) bool {
	if b == nil || rt == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, h := range b.conns {
		if runtimeConnClosed(h) {
			continue
		}
		h.mu.Lock()
		agent := h.hello.Agent
		if agent == "" {
			agent = h.hello.Runtime
		}
		pending := h.ready && agent == agyAgent && h.hello.Runtime == rt && !h.sessionsListed
		h.mu.Unlock()
		if pending {
			return true
		}
	}
	return false
}

type agyRuntimeSnapshot struct {
	host *runtimeConn
	row  proto.Session
}

func (n *Node) setAgyIdleCompaction(p assistantwire.AgyIdleCompaction) {
	norm := p.Normalize()
	n.agyIdleMu.Lock()
	n.agyIdlePolicy = &norm
	n.agyIdleMu.Unlock()
}

func (n *Node) agyIdleCompactionPolicy() assistantwire.AgyIdleCompaction {
	n.agyIdleMu.Lock()
	defer n.agyIdleMu.Unlock()
	if n.agyIdlePolicy == nil {
		return assistantwire.DefaultAgyIdleCompaction()
	}
	return n.agyIdlePolicy.Normalize()
}

func (n *Node) setPendingInteractionSessions(ids []string) {
	next := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" {
			next[id] = true
		}
	}
	n.agyIdleMu.Lock()
	n.pendingIxSessions = next
	n.agyIdleMu.Unlock()
}

func (n *Node) hasPendingInteraction(id string) bool {
	if id == "" {
		return false
	}
	n.agyIdleMu.Lock()
	defer n.agyIdleMu.Unlock()
	return n.pendingIxSessions[id]
}

func (n *Node) isKeptSession(id string) bool {
	if id == "" {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.kept[id]
}

// isManagedAgySession reports whether id is a Persona-managed Antigravity session (either an assistant/direction
// conversation or a Persona-started task Agent).
func (n *Node) isManagedAgySession(id string) bool {
	if id == "" {
		return false
	}
	if n.isAssistantSession(id) {
		return true
	}
	if n.state != nil {
		if _, ok := n.state.get(id); ok {
			return true
		}
	}
	if n.runtimes != nil && n.runtimes.wasStarted(id) {
		return true
	}
	return n.isKeptSession(id)
}

func agySessionRowSafeIdle(row proto.Session) bool {
	if row.SessionID == "" || row.Archived || row.IsSubagent || row.Starting || row.StartError != "" {
		return false
	}
	if row.Status != proto.StatusIdle && row.Status != proto.StatusDone {
		return false
	}
	if row.State == proto.StateWorking || row.State == proto.StateNeedsYou {
		return false
	}
	if row.PendingInteractions > 0 || len(row.Queued) > 0 {
		return false
	}
	if row.BackgroundWorking || row.BackgroundWaiting || len(row.Background) > 0 || row.WokeByTask || row.Loop != nil {
		return false
	}
	for _, sub := range row.Subagents {
		if sub.Running {
			return false
		}
	}
	for _, wf := range row.Workflows {
		if wf.Running {
			return false
		}
	}
	return true
}

func (n *Node) agySessionSafeIdle(h *runtimeConn, row proto.Session) bool {
	if h == nil || h.peer == nil || runtimeConnClosed(h) || h.agent() != agyAgent {
		return false
	}
	if !agySessionRowSafeIdle(row) {
		return false
	}
	if n.reload.Preparing() {
		return false
	}
	id := row.SessionID
	if n.runtimes != nil && n.runtimes.ownerOf(id) != h {
		return false
	}
	if !n.isManagedAgySession(id) {
		return false
	}
	if n.runtimes != nil && n.runtimes.compacting(id) {
		return false
	}
	if n.activeAsks.active(id) || n.hasPendingInteraction(id) {
		return false
	}
	if n.state != nil {
		if st, ok := n.state.get(id); ok && (st.SegmentRetired || len(st.Tasks) > 0) {
			return false
		}
	}
	if _, _, held := n.agyOpenIn(id); held {
		return false
	}
	return true
}

func (n *Node) deleteAgyIdleCandidates(drop []agyIdlePruneCandidate) {
	if len(drop) == 0 {
		return
	}
	n.agyIdleMu.Lock()
	for _, c := range drop {
		cur, ok := n.agyIdleSessions[c.id]
		if !ok || cur != c.state {
			continue
		}
		if cur != nil && cur.gen != c.gen {
			continue
		}
		delete(n.agyIdleSessions, c.id)
	}
	n.agyIdleMu.Unlock()
}

func (n *Node) observeAgySessions(h *runtimeConn, list []proto.Session) {
	if h == nil || runtimeConnClosed(h) || h.agent() != agyAgent {
		return
	}
	now := n.agyNow()
	policy := n.agyIdleCompactionPolicy()
	seen := make(map[string]bool, len(list))
	for _, row := range list {
		if row.SessionID == "" {
			continue
		}
		// Multiple AGY companions can be connected on one node (e.g. CLI + Desktop); only the current
		// owner of a session updates its idle compaction state so a non-owner cannot reset latch/cooldown.
		if n.runtimes != nil && n.runtimes.ownerOf(row.SessionID) != h {
			continue
		}
		seen[row.SessionID] = true
		safe := n.agySessionSafeIdle(h, row)
		// Re-check ownership after agySessionSafeIdle (which performs agyOpenIn disk I/O) so an older
		// companion returning late cannot overwrite a newer owner's st.host or reset its latch/cooldown.
		if runtimeConnClosed(h) {
			return
		}
		if n.runtimes != nil && n.runtimes.ownerOf(row.SessionID) != h {
			delete(seen, row.SessionID)
			continue
		}
		n.updateAgyIdleState(h, row, safe, policy.ContextTokens, now)
	}
	// Only prune entries that belonged to h (leaving other AGY companions' state intact), and verify
	// outside agyIdleMu that no other companion owns the session and no reconnecting companion for the
	// same runtime id is still awaiting its initial session list.
	var stale []agyIdlePruneCandidate
	n.agyIdleMu.Lock()
	for id, st := range n.agyIdleSessions {
		if seen[id] {
			continue
		}
		if st == nil {
			stale = append(stale, agyIdlePruneCandidate{id: id})
		} else if st.host == nil || st.host == h {
			stale = append(stale, agyIdlePruneCandidate{id: id, host: st.host, state: st, gen: st.gen})
		}
	}
	n.agyIdleMu.Unlock()
	if len(stale) == 0 {
		return
	}
	var drop []agyIdlePruneCandidate
	for _, c := range stale {
		if n.runtimes != nil && n.runtimes.ownerOf(c.id) != nil {
			continue
		}
		if c.host != nil && n.runtimes != nil && n.runtimes.hasUnlistedAgyRuntime(c.host.id()) {
			continue
		}
		drop = append(drop, c)
	}
	n.deleteAgyIdleCandidates(drop)
}

func (n *Node) updateAgyIdleState(h *runtimeConn, row proto.Session, safe bool, tokenThreshold int, now time.Time) time.Time {
	n.agyIdleMu.Lock()
	defer n.agyIdleMu.Unlock()
	if n.agyIdleSessions == nil {
		n.agyIdleSessions = map[string]*agyIdleState{}
	}
	st := n.agyIdleSessions[row.SessionID]
	if st == nil {
		st = &agyIdleState{}
		n.agyIdleSessions[row.SessionID] = st
	}
	if h != nil {
		st.host = h
	}
	if int(row.ContextTokens) < tokenThreshold {
		st.attempted = false
		st.lastAttempt = time.Time{}
		st.attemptToken = 0
	}
	if !safe {
		st.safeIdleSince = time.Time{}
		st.lastRowTime = row.UpdatedAt
		st.lastTokens = int(row.ContextTokens)
		st.gen++
		return time.Time{}
	}
	if st.safeIdleSince.IsZero() {
		if !row.UpdatedAt.IsZero() && !row.UpdatedAt.After(now) {
			st.safeIdleSince = row.UpdatedAt
		} else {
			st.safeIdleSince = now
		}
	} else if st.safeIdleSince.After(now) {
		// On a wall-clock rollback, reset safeIdleSince to now rather than an older row.UpdatedAt so
		// rollback cannot immediately trip IdleMinutes and trigger a false compaction.
		st.safeIdleSince = now
	} else if !row.UpdatedAt.IsZero() && row.UpdatedAt.After(st.lastRowTime) && row.UpdatedAt.After(st.safeIdleSince) {
		if row.UpdatedAt.After(now) {
			st.safeIdleSince = now
		} else {
			st.safeIdleSince = row.UpdatedAt
		}
	}
	st.lastRowTime = row.UpdatedAt
	st.lastTokens = int(row.ContextTokens)
	st.gen++
	return st.safeIdleSince
}

func (n *Node) agyIdleReady(h *runtimeConn, row proto.Session, policy assistantwire.AgyIdleCompaction, now time.Time) (time.Duration, bool) {
	if !policy.Enabled || int(row.ContextTokens) < policy.ContextTokens {
		return 0, false
	}
	if h == nil || h.peer == nil || runtimeConnClosed(h) || h.agent() != agyAgent {
		return 0, false
	}
	if n.runtimes != nil && n.runtimes.ownerOf(row.SessionID) != h {
		return 0, false
	}
	safe := n.agySessionSafeIdle(h, row)
	if runtimeConnClosed(h) || (n.runtimes != nil && n.runtimes.ownerOf(row.SessionID) != h) {
		return 0, false
	}
	if !safe {
		n.updateAgyIdleState(h, row, false, policy.ContextTokens, now)
		return 0, false
	}
	idleSince := n.updateAgyIdleState(h, row, true, policy.ContextTokens, now)
	if idleSince.IsZero() {
		return 0, false
	}
	idleFor := now.Sub(idleSince)
	if idleFor < time.Duration(policy.IdleMinutes)*time.Minute {
		return idleFor, false
	}
	n.agyIdleMu.Lock()
	st := n.agyIdleSessions[row.SessionID]
	latched := st != nil && st.attempted
	cooling := st != nil && !st.lastAttempt.IsZero() && now.Sub(st.lastAttempt) < agyIdleRetryCooldown
	n.agyIdleMu.Unlock()
	if latched || cooling {
		return idleFor, false
	}
	return idleFor, true
}

func (n *Node) markAgyIdleAttempt(h *runtimeConn, id string, now time.Time) (token uint64, prevAttempted bool, prevLastAttempt time.Time) {
	n.agyIdleMu.Lock()
	defer n.agyIdleMu.Unlock()
	if n.agyIdleSessions == nil {
		n.agyIdleSessions = map[string]*agyIdleState{}
	}
	st := n.agyIdleSessions[id]
	if st == nil {
		st = &agyIdleState{}
		n.agyIdleSessions[id] = st
	}
	prevAttempted = st.attempted
	prevLastAttempt = st.lastAttempt
	n.agyAttemptSeq++
	token = n.agyAttemptSeq
	if h != nil {
		st.host = h
	}
	st.attemptToken = token
	st.attempted = true
	st.lastAttempt = now
	st.gen++
	return token, prevAttempted, prevLastAttempt
}

func (n *Node) recordAgyIdleFailure(h *runtimeConn, id string, now time.Time, token uint64, prevAttempted bool, prevLastAttempt time.Time) {
	staleOwner := runtimeConnClosed(h) || (n.runtimes != nil && n.runtimes.ownerOf(id) != h)
	n.agyIdleMu.Lock()
	defer n.agyIdleMu.Unlock()
	st := n.agyIdleSessions[id]
	if st == nil || token == 0 || st.attemptToken != token {
		return
	}
	st.attemptToken = 0
	if staleOwner {
		st.attempted = prevAttempted
		st.lastAttempt = prevLastAttempt
	} else {
		st.attempted = false
		st.lastAttempt = now
	}
	st.gen++
}

func (n *Node) pruneStaleAgyIdleStates(snaps []agyRuntimeSnapshot) {
	live := make(map[string]bool, len(snaps))
	for _, snap := range snaps {
		live[snap.row.SessionID] = true
	}
	type hostAdopt struct {
		id      string
		newHost *runtimeConn
		state   *agyIdleState
		gen     uint64
	}
	var adopt []hostAdopt
	var check []agyIdlePruneCandidate
	n.agyIdleMu.Lock()
	for _, snap := range snaps {
		if snap.host == nil {
			continue
		}
		if st := n.agyIdleSessions[snap.row.SessionID]; st != nil && st.host != snap.host {
			adopt = append(adopt, hostAdopt{id: snap.row.SessionID, newHost: snap.host, state: st, gen: st.gen})
		}
	}
	for id, st := range n.agyIdleSessions {
		if live[id] {
			continue
		}
		if st == nil {
			check = append(check, agyIdlePruneCandidate{id: id})
		} else {
			check = append(check, agyIdlePruneCandidate{id: id, host: st.host, state: st, gen: st.gen})
		}
	}
	n.agyIdleMu.Unlock()

	if len(adopt) > 0 {
		var verified []hostAdopt
		for _, a := range adopt {
			if !runtimeConnClosed(a.newHost) && (n.runtimes == nil || n.runtimes.ownerOf(a.id) == a.newHost) {
				verified = append(verified, a)
			}
		}
		if len(verified) > 0 {
			n.agyIdleMu.Lock()
			for _, a := range verified {
				if cur, ok := n.agyIdleSessions[a.id]; ok && cur == a.state && cur.gen == a.gen {
					cur.host = a.newHost
					cur.gen++
				}
			}
			n.agyIdleMu.Unlock()
		}
	}

	if len(check) == 0 {
		return
	}
	var drop []agyIdlePruneCandidate
	for _, c := range check {
		if n.runtimes != nil && n.runtimes.ownerOf(c.id) != nil {
			continue
		}
		if c.host != nil {
			if !runtimeConnClosed(c.host) {
				if _, ok := c.host.row(c.id); ok {
					continue
				}
			}
			if n.runtimes != nil && n.runtimes.hasUnlistedAgyRuntime(c.host.id()) {
				continue
			}
		}
		drop = append(drop, c)
	}
	n.deleteAgyIdleCandidates(drop)
}

// sweepAgyIdleCompactions proactively compacts Persona-managed Antigravity sessions whose context and safe-idle
// duration have both crossed the configured thresholds.
func (n *Node) sweepAgyIdleCompactions(ctx context.Context, now time.Time) {
	if n.runtimes == nil || n.reload.Preparing() {
		return
	}
	snaps := n.runtimes.agySnapshots()
	n.pruneStaleAgyIdleStates(snaps)
	policy := n.agyIdleCompactionPolicy()
	if !policy.Enabled {
		return
	}
	for _, snap := range snaps {
		idleFor, ready := n.agyIdleReady(snap.host, snap.row, policy, now)
		if !ready {
			continue
		}
		if !n.tryAcquireAgyIdleSlot() {
			break
		}
		go n.runAgyIdleCompaction(ctx, snap.host, snap.row.SessionID, int(snap.row.ContextTokens), idleFor)
	}
}

func (n *Node) runAgyIdleCompaction(ctx context.Context, h *runtimeConn, id string, tokens int, idleFor time.Duration) {
	handOffSlot := false
	defer func() {
		if !handOffSlot {
			n.releaseAgyIdleSlot()
		}
	}()
	unlock, ok := n.typing.tryLock(id)
	if !ok {
		return
	}
	defer unlock()
	if ctx.Err() != nil {
		return
	}
	if runtimeConnClosed(h) || (n.runtimes != nil && n.runtimes.ownerOf(id) != h) {
		return
	}
	row, ok := h.row(id)
	if !ok {
		return
	}
	now := n.agyNow()
	policy := n.agyIdleCompactionPolicy()
	if _, ready := n.agyIdleReady(h, row, policy, now); !ready {
		return
	}
	if runtimeConnClosed(h) || (n.runtimes != nil && n.runtimes.ownerOf(id) != h) {
		return
	}
	token, prevAttempted, prevLastAttempt := n.markAgyIdleAttempt(h, id, now)
	isAssistant := n.isAssistantSession(id)
	compaction, err := n.startRuntimeCompactionLocked(ctx, h, id, isAssistant, true, false)
	if err != nil {
		n.recordAgyIdleFailure(h, id, n.agyNow(), token, prevAttempted, prevLastAttempt)
		n.logger.Printf("agy %s: idle auto-compact start failed: %v", id, err)
		return
	}
	n.logger.Printf("agy %s: idle %s with %d tokens (>= %d), auto-compacting", id, idleFor.Round(time.Second), tokens, policy.ContextTokens)
	handOffSlot = true
	go func() {
		defer n.releaseAgyIdleSlot()
		<-compaction.done
		if failure := compaction.failureMessage(); failure != "" {
			n.recordAgyIdleFailure(h, id, n.agyNow(), token, prevAttempted, prevLastAttempt)
			n.logger.Printf("agy %s: idle auto-compact failed: %s", id, failure)
		}
	}()
}

func isAgySkipPreInputCompactCommand(text string, images int) bool {
	if images > 0 {
		return false
	}
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return true
	}
	switch strings.ToLower(fields[0]) {
	case "/compact", "/clear", "/new", "/exit", "/quit", "/reset":
		return true
	default:
		return false
	}
}

// agyIdleCompactBeforeInput ensures an idle Antigravity session over both thresholds is compacted before delivering
// new input. If a silent idle compaction is already in flight, it waits for it to finish so the input lands cleanly
// right after compaction. Failures are logged and never block delivering the caller's message.
func (n *Node) agyIdleCompactBeforeInput(ctx context.Context, h *runtimeConn, id, text string, images []proto.Image, isAssistant, lockHeld bool) error {
	if h == nil || h.agent() != agyAgent || id == "" {
		return nil
	}
	if strings.TrimSpace(text) == "" && len(images) == 0 {
		return nil
	}
	if isAgySkipPreInputCompactCommand(text, len(images)) {
		return nil
	}
	// If an automatic silent compaction is already running (e.g. triggered by the idle sweep right before this input),
	// wait for it to finish before taking the input lock or delivering the message.
	if active := n.runtimes.activeCompaction(id); active != nil && active.silent {
		select {
		case <-active.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if !lockHeld {
		unlock := n.typing.lock(id)
		defer unlock()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if active := n.runtimes.activeCompaction(id); active != nil && active.silent {
			select {
			case <-active.done:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	if runtimeConnClosed(h) || (n.runtimes != nil && n.runtimes.ownerOf(id) != h) {
		return nil
	}
	row, ok := h.row(id)
	if !ok {
		return nil
	}
	now := n.agyNow()
	policy := n.agyIdleCompactionPolicy()
	idleFor, ready := n.agyIdleReady(h, row, policy, now)
	if !ready {
		return nil
	}
	if runtimeConnClosed(h) || (n.runtimes != nil && n.runtimes.ownerOf(id) != h) {
		return nil
	}
	token, prevAttempted, prevLastAttempt := n.markAgyIdleAttempt(h, id, now)
	compaction, err := n.startRuntimeCompactionLocked(ctx, h, id, isAssistant, true, false)
	if err != nil {
		n.recordAgyIdleFailure(h, id, n.agyNow(), token, prevAttempted, prevLastAttempt)
		n.logger.Printf("agy %s: pre-input auto-compact start failed (continuing send): %v", id, err)
		return nil
	}
	n.logger.Printf("agy %s: pre-input auto-compact after %s idle with %d tokens (>= %d)", id, idleFor.Round(time.Second), row.ContextTokens, policy.ContextTokens)
	select {
	case <-compaction.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if failure := compaction.failureMessage(); failure != "" {
		n.recordAgyIdleFailure(h, id, n.agyNow(), token, prevAttempted, prevLastAttempt)
		n.logger.Printf("agy %s: pre-input auto-compact failed (continuing send): %s", id, failure)
	}
	return nil
}
