package node

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
)

// assistantIdleTimeout: how long an idle process is kept, unless the main conversation's release policy says otherwise
// (assistantRelease).
const assistantIdleTimeout = 20 * time.Minute

// assistantRelease is when the main conversation's idle process is let go and the context it is compacted at first,
// from the manager's idle compaction policy (assistant.idle_release_minutes, a saved assistant.idle_auto_compact_tokens,
// #12008). Zero values keep assistantIdleTimeout and the window's threshold (compactIdleAt).
type assistantRelease struct {
	after     time.Duration
	compactAt int
}

// releaseOf reads a policy's release fields; one out of range is left at zero, as if it were not sent.
func releaseOf(c assistantwire.IdleCompaction) assistantRelease {
	var r assistantRelease
	if assistantwire.ReleaseMinutesValid(c.ReleaseMinutes) {
		r.after = time.Duration(c.ReleaseMinutes) * time.Minute
	}
	if assistantwire.ReleaseTokensValid(c.ReleaseTokens) {
		r.compactAt = c.ReleaseTokens
	}
	return r
}

// assistantReleases returns the release policy of each of ids that is the main conversation and has one, from the
// policy the manager last sent with its messages (kept on its record, so it outlives a node restart). Read without
// n.mu held: the reaper takes n.mu and each process's lock afterwards.
func (n *Node) assistantReleases(ids []string) map[string]assistantRelease {
	out := map[string]assistantRelease{}
	for _, id := range ids {
		st, ok := n.state.get(id)
		if !ok || !st.IsMain || st.IdleCompaction == nil {
			continue
		}
		if r := releaseOf(st.IdleCompaction.Policy); r != (assistantRelease{}) {
			out[id] = r
		}
	}
	return out
}

// A process with background tasks or monitors running is kept: they are its children, and their notifications are
// what wakes the conversation up again. Past this long without a turn it is let go anyway (a forgotten `tail -f`).
const assistantTaskMaxIdle = 12 * time.Hour

// assistantInterrupt stops the current turn. claude -p has no interrupt over stdin, so the process is killed;
// the next message resumes the conversation from its transcript. Codex has turn/interrupt: the process stays.
func (n *Node) assistantInterrupt(id string) error {
	n.state.stopped(id, proto.StopUser)
	// An explicit Stop wins over any queued same-model or fallback continuation: cancelled before the session lock, a
	// recovery send that has not started gives up at once instead of holding Stop behind it (#3495).
	n.cancelExternalStartups(id)
	n.cancelAssistantRecovery(id)
	if n.stopAwaited(id) { // a message waits for the round's turn, holding the lock: it goes in now (assistant_round.go)
		return nil
	}
	defer n.typing.lock(id)()
	n.cancelAssistantRecovery(id)
	if n.runtimeAssistant(id) {
		return n.assistantInterruptRuntime(context.Background(), id)
	}
	n.mu.Lock()
	p := n.procs[id]
	n.mu.Unlock()
	if p != nil && p.cx != nil && p.alive() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := p.cx.interrupt(ctx); err == nil {
			return nil
		}
	}
	n.stopAssistantProcess(id)
	n.rememberTasks(id, nil) // stopped on purpose, background tasks included
	return nil
}

// stopAssistant cancels recovery before replacing a process. Explicit user stops additionally hold the session send
// lock (assistantInterrupt and session.close); internal callers may already hold settings locks and must not take it.
func (n *Node) stopAssistant(id string) {
	n.forgetCacheWarm(id)
	n.cancelAssistantRecovery(id)
	n.stopAssistantProcess(id)
}

func (n *Node) stopAssistantProcess(id string) {
	if p := n.takeAssistant(id); p != nil {
		p.stop()
	}
}

// takeAssistant removes id's process from this Node's and returns it (nil for none).
func (n *Node) takeAssistant(id string) *assistantProc {
	n.mu.Lock()
	defer n.mu.Unlock()
	p := n.procs[id]
	delete(n.procs, id)
	return p
}

// stopAtConnectionEnd ends the conversations ids, the warm spare, and any already-retired assistant process owned by
// a node without a resident keeper, whose process exits once its connection has (`node --stdio`), and returns when
// they have exited (stopAndWait). Each process leads its own group, so nothing else takes them down with the node: a
// node the manager killed left them running beside the processes the next node started for the same conversations, the
// old ones refused the assistant's tools by that node (mcpSession) and their background tasks reported lost while
// still running (#4259). What they were running is still remembered (rememberTasks), so the next node tells each
// conversation.
func (n *Node) stopAtConnectionEnd(ids []string, spare *assistantProc) {
	seen := map[*assistantProc]bool{}
	var procs []*assistantProc
	add := func(p *assistantProc) {
		if p == nil || seen[p] {
			return
		}
		seen[p] = true
		procs = append(procs, p)
	}
	for _, id := range ids {
		n.cancelAssistantRecovery(id)
		add(n.takeAssistant(id))
	}
	add(spare)
	// Retired processes (timed-out startup attempts, interrupted turns, replaced spares) have already been removed
	// from n.procs / n.spare, but remain in liveAssistants until their process group has exited and been swept.
	liveAssistants.Range(func(k, _ any) bool {
		if p := k.(*assistantProc); p.owner.Load() == n {
			add(p)
		}
		return true
	})
	if left := stopAndWait(procs); left > 0 {
		n.logger.Printf("assistant: %d conversations still running after SIGKILL as the connection ended", left)
	}
}

// stopAllAssistants stops every conversation, warm spare, and retired assistant process owned by n, waiting until all
// have exited.
func (n *Node) stopAllAssistants() {
	n.mu.Lock()
	ids := make([]string, 0, len(n.procs))
	for id := range n.procs {
		ids = append(ids, id)
	}
	spare := n.spare
	n.spare = nil
	n.mu.Unlock()
	n.stopAtConnectionEnd(ids, spare)
}

// reaperStopWait bounds how long Serve waits for reapAssistants to stop the conversations as the connection ends:
// stopAndWait's own bounds, and a margin.
func reaperStopWait() time.Duration { return keeperStopGrace + keeperKillWait + time.Second }

// inShellInit: this process was launched through an interactive shell (zsh -ic / bash -ic) that has not finished
// sourcing its rc files yet (no assistantShellReady line and no assistant JSON output). Interactive shells ignore
// SIGTERM, so sending SIGTERM to the group kills the current rc-file child and lets the shell continue into launching
// claude after stop() was called; there is no claude process yet to shut down gracefully.
func (p *assistantProc) inShellInit() bool {
	if p.agent != "claude" || p.ready == nil || p.tui || proto.IsExternalLauncher(p.launcher) {
		return false
	}
	if p.hasStarted() {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.shellReadyAt.IsZero()
}

func (p *assistantProc) stop() {
	if p.stopped.Swap(true) {
		return
	}
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.cmd == nil || p.cmd.Process == nil {
		return
	}
	if p.exited != nil && !p.alive() {
		return
	}
	if p.inShellInit() {
		_ = osproc.StopGroup(p.cmd.Process.Pid, true)
		return
	}
	_ = osproc.StopGroup(p.cmd.Process.Pid, false)
	if p.exited != nil {
		go func() {
			t := time.NewTimer(5 * time.Second)
			defer t.Stop()
			select {
			case <-p.exited:
			case <-t.C:
				_ = osproc.StopGroup(p.cmd.Process.Pid, true)
			}
		}()
	}
}

// reapable: idle long enough to end; p.mu is held. Background tasks and monitors are children of the process
// (killed with it) and wake it when they report, so a process waiting on them stays (2026-10-30: a 23-minute
// background command was killed at minute 20 and the conversation never heard back).
func (p *assistantProc) reapable(now time.Time) bool {
	if p.compacting {
		return now.Sub(p.lastActive) > 10*time.Minute // a compaction that never reported back
	}
	if p.busy {
		return false
	}
	if len(p.tasks) > 0 {
		return now.Sub(p.lastActive) > assistantTaskMaxIdle
	}
	idle := assistantIdleTimeout
	if p.release.after > 0 {
		idle = p.release.after
	}
	return now.Sub(p.lastActive) > idle
}

// idleCompactable: about to be let go with a context worth compacting first (assistantIdleCompact); p.mu is held. A
// supervision round's process never is (assistantProc.round): its next round starts from the first turn anyway. The
// main conversation's release policy, when it sets a threshold, replaces the window's (assistantRelease).
func (p *assistantProc) idleCompactable() bool {
	threshold := p.compactIdleAt
	if threshold == 0 {
		threshold = assistantIdleCompact
	}
	if p.release.compactAt > 0 {
		threshold = p.release.compactAt
	}
	return p.agent == "claude" && !p.round && !p.compacting && !p.compacted && p.context >= threshold
}

// compactIdle sends /compact to an idle process before releasing it. Main chat
// compactions report their result using the session's existing status.
func (n *Node) compactIdle(p *assistantProc) {
	if n.reload.Preparing() {
		return
	}
	// Not while a send, a restore or the user's compaction has this conversation (assistantLife), and only on the same
	// process, still idle: the next sweep tries again.
	unlock, ok := n.assistantLife.tryLock(p.id)
	if !ok {
		return
	}
	defer unlock()
	n.mu.Lock()
	same := n.procs[p.id] == p
	n.mu.Unlock()
	p.mu.Lock()
	idle := !p.busy && !p.compacting && !p.manual
	p.mu.Unlock()
	state, _ := n.state.get(p.id)
	if !same || !idle || state.SegmentRetired {
		return
	}
	n.forgetCacheWarm(p.id)
	line, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": n.assistantCompactCommand(p.id)}})
	if n.reload.Preparing() {
		return
	}
	p.mu.Lock()
	if !p.alive() || p.busy || p.compacting || p.manual {
		p.mu.Unlock()
		return
	}
	tokens := p.context
	startedAt := time.Now().UTC()
	p.compacting, p.compacted = true, true
	p.releaseCompaction = state.IsMain
	p.lastActive = startedAt
	p.mu.Unlock()
	if state.IsMain {
		n.state.update(p.id, func(st *sessionState) bool {
			st.Compaction = &proto.Compaction{State: proto.CompactionRunning, StartedAt: startedAt}
			if st.IdleCompaction != nil && !st.IdleCompaction.Attempted {
				a := *st.IdleCompaction
				a.recordAttempt(startedAt)
				st.IdleCompaction = &a
			}
			return true
		})
		n.notifySessions()
	}
	if err := p.write(line); err != nil {
		n.logger.Printf("assistant %s: compact: %v", p.id[:8], err)
		if state.IsMain {
			n.compactionEnded(p.id, "没能开始压缩："+err.Error())
		}
		n.stopAssistant(p.id)
		return
	}
	n.logger.Printf("assistant %s: idle with %d tokens in context, compacting before letting it go", p.id[:8], tokens)
}

// reapAssistants ends processes idle for a while; their conversations resume on the next message. When ctx ends (the
// connection did) a resident node hands its conversations over; any other node stops them and returns once they have
// exited (stopAtConnectionEnd).
func (n *Node) reapAssistants(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			n.mu.Lock()
			ids := make([]string, 0, len(n.procs))
			for id := range n.procs {
				ids = append(ids, id)
			}
			spare := n.spare
			n.spare = nil
			n.mu.Unlock()
			if n.keeper != nil { // a resident node: the next connection takes them over (assistant_keep.go)
				if spare != nil {
					spare.stop()
				}
				n.handOverAssistants()
				return
			}
			n.cacheWarmPool().close()
			n.stopAtConnectionEnd(ids, spare)
			return
		case <-t.C:
		}
		n.sweepAssistants()
		n.sweepAgyIdleCompactions(ctx, n.agyNow())
		n.sweepCacheWarm(ctx, time.Now())
	}
}

// sweepAssistants is one look of reapAssistants: it compacts the idle processes worth compacting before they go
// (idleCompactable) and lets the other idle ones go (reapable).
func (n *Node) sweepAssistants() {
	n.mu.Lock()
	ids := make([]string, 0, len(n.procs))
	for id := range n.procs {
		ids = append(ids, id)
	}
	n.mu.Unlock()
	release := n.assistantReleases(ids)
	reloading := n.reload.Preparing()
	n.mu.Lock()
	var stale *assistantProc
	if n.spare != nil && time.Since(n.spare.born) > assistantSpareMaxAge {
		stale, n.spare = n.spare, nil
	}
	var idle []string
	var compact []*assistantProc
	var timedOut []string
	for id, p := range n.procs {
		p.mu.Lock()
		p.release = release[id]
		if p.manual && time.Since(p.lastActive) > assistantCompactTimeout {
			p.manual, p.compacting = false, false
			timedOut = append(timedOut, id)
		}
		if p.reapable(time.Now()) {
			if p.releaseCompaction {
				p.releaseCompaction = false
				timedOut = append(timedOut, id)
			}
			if !reloading && p.idleCompactable() {
				compact = append(compact, p)
			} else {
				idle = append(idle, id)
			}
		}
		p.mu.Unlock()
	}
	n.mu.Unlock()
	for _, id := range timedOut {
		n.compactionEnded(id, "压缩超过 10 分钟没有结果")
	}
	n.sweepCompactions()
	for _, p := range compact {
		n.compactIdle(p)
	}
	for _, id := range idle {
		n.cancelAssistantRecovery(id)
		n.stopAssistantProcess(id) // idle release preserves the independent cache snapshot
		n.rememberTasks(id, nil)   // let go on purpose: nothing to tell it after a restart
	}
	if stale != nil {
		stale.stop()
	}
}
