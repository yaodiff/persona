package assistant

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Node recovery sweep (#14781): when a node reconnects after an extended outage
// or after a host reboot (reported BootTime later than its last online time),
// run a recovery sweep in order:
//
//  0. Freeze first: pause running old Agents and background commands on the
//     returning node before deciding, so two sides cannot redo work or push at
//     the same time.
//  1. Check whether the work was covered elsewhere (moved to another node,
//     already delivered, or accepted/cancelled). If covered, release the old
//     Agent, drop its leftover waits/gates, and record "taken over by X" in
//     supervision. If both sides made diverging progress (e.g. both pushed
//     commits), hand the conflict to the supervisor without picking a side.
//  2. Only if not covered and not conflicted, check whether the Agent and its
//     in-flight step can continue: wake resumable Agents (including cross-node
//     waiters) with what was lost, or emit orphaned/recovered signals with the
//     outage length and affected step.
//  3. Deliver all supervision results into the supervision digest, never as a
//     push to the owner.

var (
	// nodeRecoveryOfflineAfter is the minimum outage duration before a reconnect
	// triggers a recovery sweep (aligned with orphanOfflineAfter; tests shorten it).
	nodeRecoveryOfflineAfter = 10 * time.Minute
)

const nodeRecoveryKeyPrefix = "node-recovery:"

type nodeOnlineRecord struct {
	machine     string
	lastOnline  time.Time
	offlineAt   time.Time
	bootTime    int64
	sweptConnAt time.Time
	sweptBootAt int64
}

type nodeRecoveryBook struct {
	mu      sync.Mutex
	records []nodeOnlineRecord
	// nodeInfoHook is an optional test hook supplying machine metadata without
	// replacing Service.devs (which switches sessions() to fleet localSessions).
	nodeInfoHook func(machine string) (name, hostname string, bootTime int64, connectedAt time.Time, ok bool)
}

type nodeRecoveryEvent struct {
	machine     string
	machineName string
	hostname    string
	outageStart time.Time
	outage      time.Duration
	rebooted    bool
	connectedAt time.Time
	bootTime    int64
}

// nodeRecoveryResult summarizes one sweep for logging and unit tests.
type nodeRecoveryResult struct {
	Triggered  bool
	Rebooted   bool
	Outage     time.Duration
	Checked    int
	Frozen     int
	Released   int
	Conflicts  int
	Woken      int
	Signalled  int
	WokenTodos []string
}

func (b *nodeRecoveryBook) indexLocked(id string) int {
	for i := range b.records {
		if b.records[i].machine == id {
			return i
		}
	}
	return -1
}

// noteNodeOnline refreshes lastOnline for an already-tracked node without heap
// allocations on the hot SessionsChanged path.
func (s *Service) noteNodeOnline(machine string, now time.Time) {
	id := s.canonical(machine)
	if id == "" {
		return
	}
	b := &s.nodeRecovery
	b.mu.Lock()
	if i := b.indexLocked(id); i >= 0 {
		if now.After(b.records[i].lastOnline) {
			b.records[i].lastOnline = now
		}
		b.records[i].offlineAt = time.Time{}
	}
	b.mu.Unlock()
}

func (s *Service) noteNodeOffline(machine string, now time.Time) {
	id := s.canonical(machine)
	if id == "" {
		return
	}
	b := &s.nodeRecovery
	b.mu.Lock()
	defer b.mu.Unlock()
	if i := b.indexLocked(id); i >= 0 {
		if b.records[i].offlineAt.IsZero() {
			b.records[i].offlineAt = now
		}
		if b.records[i].lastOnline.IsZero() {
			b.records[i].lastOnline = now
		}
		return
	}
	b.records = append(b.records, nodeOnlineRecord{
		machine:    id,
		lastOnline: now,
		offlineAt:  now,
	})
}

// nodeOfflineSince reports when machine was first marked offline in the current
// outage, reusing node_recovery's tracking book.
func (s *Service) nodeOfflineSince(machine string) (time.Time, bool) {
	id := s.canonical(machine)
	if id == "" {
		return time.Time{}, false
	}
	b := &s.nodeRecovery
	b.mu.Lock()
	defer b.mu.Unlock()
	if i := b.indexLocked(id); i >= 0 && !b.records[i].offlineAt.IsZero() {
		return b.records[i].offlineAt, true
	}
	return time.Time{}, false
}

// NodeConnected is called when a node finishes connecting and listing sessions
// (devices.Options.OnConnected). If the node was offline longer than
// nodeRecoveryOfflineAfter or rebooted since it was last online, it runs a
// recovery sweep for affected tasks.
func (s *Service) NodeConnected(ctx context.Context, machine string) {
	if machine != "" {
		s.invalidateLauncherListCache(machine)
	}
	s.nodeConnectedAt(ctx, machine, time.Now())
}

func (s *Service) nodeConnectedAt(ctx context.Context, machine string, now time.Time) nodeRecoveryResult {
	id := s.canonical(machine)
	if id == "" {
		return nodeRecoveryResult{}
	}
	var (
		connectedAt time.Time
		bootTime    int64
		machineName = id
		hostname    string
	)
	b := &s.nodeRecovery
	b.mu.Lock()
	hook := b.nodeInfoHook
	b.mu.Unlock()
	if hook != nil {
		if n, h, bt, ca, ok := hook(id); ok {
			if n != "" {
				machineName = n
			}
			hostname = h
			bootTime = bt
			connectedAt = ca
		}
	} else if s.devs != nil {
		connectedAt = s.devs.ConnectedAt(id)
		if m, _, _ := s.devs.MachineResources(id); m != nil {
			if m.Name != "" {
				machineName = m.Name
			}
			hostname = m.Hostname
			if m.System != nil {
				bootTime = m.System.BootTime
			}
		}
	}
	if machineName == id {
		if d, ok := s.device(ctx, id); ok && d.Name != "" {
			machineName = d.Name
		}
	}
	if connectedAt.IsZero() {
		connectedAt = now
	}

	b.mu.Lock()
	idx := b.indexLocked(id)
	had := idx >= 0
	var rec nodeOnlineRecord
	if had {
		rec = b.records[idx]
	} else {
		rec.machine = id
	}
	prevOnline := rec.lastOnline
	if !rec.offlineAt.IsZero() && (prevOnline.IsZero() || rec.offlineAt.After(prevOnline)) {
		prevOnline = rec.offlineAt
	}
	prevBoot := rec.bootTime

	var outage time.Duration
	if !prevOnline.IsZero() && now.After(prevOnline) {
		outage = now.Sub(prevOnline)
	}
	rebooted := false
	if bootTime > 0 {
		if !prevOnline.IsZero() && time.Unix(bootTime, 0).After(prevOnline) {
			rebooted = true
		} else if prevBoot > 0 && bootTime > prevBoot {
			rebooted = true
		}
	}
	alreadySwept := !rec.sweptConnAt.IsZero() && rec.sweptConnAt.Equal(connectedAt) && rec.sweptBootAt == bootTime
	shouldSweep := had && !alreadySwept && (rebooted || outage >= nodeRecoveryOfflineAfter)

	rec.lastOnline = now
	rec.offlineAt = time.Time{}
	if bootTime > 0 {
		rec.bootTime = bootTime
	}
	if shouldSweep {
		rec.sweptConnAt = connectedAt
		rec.sweptBootAt = bootTime
	}
	if had {
		b.records[idx] = rec
	} else {
		b.records = append(b.records, rec)
	}
	b.mu.Unlock()

	// Wake logicalSweep promptly so any retired segments with ClosePending on the
	// returning node are closed right away, preventing split-brain after failover.
	s.historyWake()

	if !shouldSweep {
		return nodeRecoveryResult{Rebooted: rebooted, Outage: outage}
	}
	ev := nodeRecoveryEvent{
		machine:     id,
		machineName: machineName,
		hostname:    hostname,
		outageStart: prevOnline,
		outage:      outage,
		rebooted:    rebooted,
		connectedAt: connectedAt,
		bootTime:    bootTime,
	}
	return s.runNodeRecoverySweep(ctx, ev, now)
}

type recoveryTarget struct {
	agent    TodoAgent
	session  proto.Session
	listed   bool
	isWaiter bool
	step     string
}

type recoveryTaskPlan struct {
	todo        Todo
	active      []TodoAgent
	byKey       map[[2]string]proto.Session
	onNode      []recoveryTarget
	waiters     []recoveryTarget
	otherActive []TodoAgent
}

type recoveryCoverageKind int

const (
	recoveryNotCovered recoveryCoverageKind = iota
	recoveryCovered
	recoveryConflict
)

type recoveryCoverage struct {
	kind   recoveryCoverageKind
	by     string
	detail string
}

func (s *Service) runNodeRecoverySweep(ctx context.Context, ev nodeRecoveryEvent, now time.Time) nodeRecoveryResult {
	res := nodeRecoveryResult{
		Triggered: true,
		Rebooted:  ev.rebooted,
		Outage:    ev.outage,
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return res
	}
	// Include both open supervised tasks and tasks closed during the outage that
	// still have an unreleased Agent on the returning node.
	tasks := s.todosWhere(ctx, "*", func(t *Todo) bool {
		if t.Supervision == nil || len(t.Agents) == 0 {
			return false
		}
		if t.DoneAt != nil {
			for _, a := range t.Agents {
				if a.ReleasedAt == nil && a.DismissedAt == nil && s.canonical(a.MachineID) == ev.machine {
					return true
				}
			}
			return false
		}
		switch t.Supervision.state(*t) {
		case reviewClosing, reviewTimedOut, reviewUserCheck, reviewPending:
			return false
		}
		return true
	})
	if len(tasks) == 0 {
		return res
	}

	lists := machineSessions{}
	tokens := nodeMatchTokens(ev.machine, ev.machineName, ev.hostname)
	outageMin := max(1, int((ev.outage+30*time.Second)/time.Minute))
	if ev.outage <= 0 {
		outageMin = 0
	}

	var plans []recoveryTaskPlan
	for _, t := range tasks {
		active := activeTodoAgents(t)
		if len(active) == 0 {
			continue
		}
		allAgents := s.taskAgentsIn(ctx, t, lists)
		byKey := make(map[[2]string]proto.Session, len(allAgents))
		for _, c := range allAgents {
			byKey[[2]string{s.canonical(c.MachineID), c.SessionID}] = c
		}
		var plan recoveryTaskPlan
		plan.todo = t
		plan.active = active
		plan.byKey = byKey
		for _, a := range active {
			am := s.canonical(a.MachineID)
			c, listed := byKey[[2]string{am, a.SessionID}]
			if am == ev.machine {
				plan.onNode = append(plan.onNode, recoveryTarget{
					agent:    a,
					session:  c,
					listed:   listed,
					isWaiter: false,
					step:     affectedStepSummary(t, c, ev.machineName, false),
				})
				continue
			}
			plan.otherActive = append(plan.otherActive, a)
			if t.DoneAt == nil && listed {
				if waitStep, ok := waitingOnNodeStep(c, tokens); ok {
					plan.waiters = append(plan.waiters, recoveryTarget{
						agent:    a,
						session:  c,
						listed:   true,
						isWaiter: true,
						step:     waitStep,
					})
				}
			}
		}
		if len(plan.onNode) == 0 && len(plan.waiters) == 0 {
			continue
		}
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		return res
	}

	// Step 0: Freeze first — pause running old Agents and background commands on
	// the returning node before deciding whether work was covered elsewhere.
	frozen := map[[2]string]bool{}
	for _, plan := range plans {
		for _, item := range plan.onNode {
			k := [2]string{item.agent.MachineID, item.agent.SessionID}
			if frozen[k] || !item.listed || !sessionNeedsFreeze(item.session) {
				continue
			}
			fctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := s.call(fctx, item.agent.MachineID, "session.interrupt", map[string]any{"session_id": item.agent.SessionID}, nil)
			cancel()
			if err == nil {
				frozen[k] = true
				res.Frozen++
			}
		}
	}

	baseKey := fmt.Sprintf("%s%s@%s", nodeRecoveryKeyPrefix, ev.machine, ev.connectedAt.UTC().Format(time.RFC3339))
	if ev.bootTime > 0 {
		baseKey += fmt.Sprintf("#%d", ev.bootTime)
	}

	for _, plan := range plans {
		res.Checked++
		t := plan.todo

		// Step 1: Check whether the work was covered elsewhere or diverged in conflict.
		if len(plan.onNode) > 0 {
			cov := classifyRecoveryCoverage(plan, ev)
			switch cov.kind {
			case recoveryCovered:
				released := s.releaseCoveredNodeAgents(ctx, t, plan.onNode)
				res.Released += released
				if t.DoneAt == nil {
					detail := fmt.Sprintf("%s; taken over by %s; old Agent released and leftover waits/gates withdrawn",
						recoveryEventHeader(ev, outageMin), cov.by)
					s.signal(superviseSignal{
						Todo:    t.ID,
						Kind:    sigRecovered,
						Key:     baseKey + ":covered",
						Machine: plan.onNode[0].agent.MachineID,
						Session: plan.onNode[0].agent.SessionID,
						Minutes: outageMin,
						Detail:  detail,
					})
					res.Signalled++
				} else {
					s.logf("node recovery sweep %s: task %s already closed (%s), released %d old agent(s)",
						ev.machine, t.ID, cov.by, released)
				}
				continue

			case recoveryConflict:
				res.Conflicts++
				detail := fmt.Sprintf("%s; diverging progress conflict (old Agent kept frozen, supervisor decision required): %s",
					recoveryEventHeader(ev, outageMin), cov.detail)
				s.signal(superviseSignal{
					Todo:    t.ID,
					Kind:    sigRecovered,
					Key:     baseKey + ":conflict",
					Machine: plan.onNode[0].agent.MachineID,
					Session: plan.onNode[0].agent.SessionID,
					Minutes: outageMin,
					Detail:  detail,
				})
				res.Signalled++
				continue
			}
		}

		// Step 2: Not covered and not conflicted — check whether each affected
		// Agent can continue; unfreeze and wake resumable ones, signal otherwise.
		affected := make([]recoveryTarget, 0, len(plan.onNode)+len(plan.waiters))
		affected = append(affected, plan.onNode...)
		affected = append(affected, plan.waiters...)

		var (
			wokenAny       bool
			unresumable    []string
			unresumableRef TodoAgent
			allGone        = true
			orphanReason   = orphanClosed
		)
		for _, a := range plan.active {
			am := s.canonical(a.MachineID)
			c, listed := plan.byKey[[2]string{am, a.SessionID}]
			if listed && !sessionClosed(c) && c.Status != proto.StatusOffline && c.Status != proto.StatusInterrupted {
				allGone = false
			}
		}
		for _, item := range affected {
			if item.listed && sessionResumableAfterOutage(item.session) {
				wakeText := formatRecoveryWakeMessage(ev, outageMin, item.step, item.isWaiter)
				sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
				err := s.call(sctx, item.agent.MachineID, DeviceSessionSend, sendParams(item.agent.SessionID, wakeText, false), nil)
				cancel()
				if err == nil {
					wokenAny = true
					res.Woken++
					continue
				}
				unresumableRef = item.agent
				unresumable = append(unresumable, fmt.Sprintf("%s (auto-wake failed: %v)", item.step, err))
				continue
			}
			unresumableRef = item.agent
			why := unresumableReason(item.listed, item.session, ev.rebooted)
			if item.listed && item.session.TurnError != "" && !proto.SilentTurnKind(item.session.TurnErrorKind) {
				orphanReason = orphanError
			}
			unresumable = append(unresumable, fmt.Sprintf("%s (%s)", item.step, why))
		}
		if wokenAny {
			res.WokenTodos = append(res.WokenTodos, t.ID)
		}
		if len(unresumable) == 0 {
			continue
		}
		detail := formatRecoverySignalDetail(ev, outageMin, unresumable)
		kind := sigRecovered
		if allGone {
			kind = sigOrphaned
		}
		sig := superviseSignal{
			Todo:    t.ID,
			Kind:    kind,
			Key:     baseKey,
			Machine: cmp.Or(unresumableRef.MachineID, ev.machine),
			Session: unresumableRef.SessionID,
			Minutes: outageMin,
			Detail:  detail,
		}
		if kind == sigOrphaned {
			sig.Orphan = orphanReason
		}
		s.signal(sig)
		res.Signalled++
	}
	s.logf("node recovery sweep %s (rebooted=%v, outage=%s): checked=%d frozen=%d released=%d conflicts=%d woken=%d signalled=%d",
		ev.machine, ev.rebooted, ev.outage.Round(time.Second), res.Checked, res.Frozen, res.Released, res.Conflicts, res.Woken, res.Signalled)
	return res
}

func sessionNeedsFreeze(c proto.Session) bool {
	if c.Assistant || sessionClosed(c) {
		return false
	}
	if c.Status == proto.StatusOffline || c.Status == proto.StatusInterrupted {
		return false
	}
	return c.Status == proto.StatusWorking || c.State == proto.StateWorking ||
		c.BackgroundWorking || c.BackgroundWaiting || len(c.Background) > 0
}

// classifyRecoveryCoverage determines whether work on the returning node was
// already covered elsewhere during the outage, or whether both sides made
// diverging progress that requires supervisor judgment.
func classifyRecoveryCoverage(plan recoveryTaskPlan, ev nodeRecoveryEvent) recoveryCoverage {
	t := plan.todo
	if t.DoneAt != nil {
		if t.Cancelled() {
			return recoveryCoverage{kind: recoveryCovered, by: "task cancelled"}
		}
		return recoveryCoverage{kind: recoveryCovered, by: cmp.Or(t.DoneReason, "task completed")}
	}
	if t.PRStatus != nil && t.PRStatus.State == "merged" {
		return recoveryCoverage{kind: recoveryCovered, by: "merged PR " + cmp.Or(t.PRStatus.URL, t.PRURL)}
	}

	// Check whether another active Agent on a different node took over during the outage.
	var newestOther *TodoAgent
	for i := range plan.otherActive {
		other := &plan.otherActive[i]
		// Exclude pure waiters that are waiting on the returning node itself.
		if slices.ContainsFunc(plan.waiters, func(w recoveryTarget) bool {
			return w.agent.MachineID == other.MachineID && w.agent.SessionID == other.SessionID
		}) {
			continue
		}
		if newestOther == nil || other.LinkedAt.After(newestOther.LinkedAt) {
			newestOther = other
		}
	}
	if newestOther == nil {
		return recoveryCoverage{kind: recoveryNotCovered}
	}

	otherSess, otherListed := plan.byKey[[2]string{newestOther.MachineID, newestOther.SessionID}]
	if !otherListed {
		for k, sess := range plan.byKey {
			if k[1] == newestOther.SessionID {
				otherSess, otherListed = sess, true
				break
			}
		}
	}
	tookOver := false
	for _, item := range plan.onNode {
		if newestOther.LinkedAt.After(item.agent.LinkedAt) ||
			(!ev.outageStart.IsZero() && newestOther.LinkedAt.After(ev.outageStart)) ||
			(otherListed && !ev.outageStart.IsZero() && otherSess.UpdatedAt.After(ev.outageStart)) {
			tookOver = true
			break
		}
	}
	if !tookOver {
		return recoveryCoverage{kind: recoveryNotCovered}
	}

	// Check for diverging progress / conflicting commits on both sides.
	for _, item := range plan.onNode {
		if detail, conflict := detectDivergingProgress(item, *newestOther, otherSess, otherListed, ev.outageStart); conflict {
			return recoveryCoverage{kind: recoveryConflict, detail: detail}
		}
	}

	return recoveryCoverage{
		kind: recoveryCovered,
		by:   leaderRef(newestOther.MachineID, newestOther.SessionID),
	}
}

// detectDivergingProgress reports whether both the returning node's old Agent
// and the replacement Agent on another node made conflicting progress (for
// example both pushing commits, especially on the same branch) during the outage.
func detectDivergingProgress(old recoveryTarget, other TodoAgent, otherSess proto.Session, otherListed bool, outageStart time.Time) (string, bool) {
	if !old.listed || !otherListed {
		return "", false
	}
	oldProgressed := !outageStart.IsZero() && old.session.UpdatedAt.After(outageStart)
	otherProgressed := !outageStart.IsZero() && (otherSess.UpdatedAt.After(outageStart) || other.LinkedAt.After(outageStart))
	if !oldProgressed || !otherProgressed {
		return "", false
	}
	oldHasCommit := sessionHasCommitProgress(old.session)
	otherHasCommit := sessionHasCommitProgress(otherSess)
	sameBranch := old.session.Branch != "" && otherSess.Branch != "" && old.session.Branch == otherSess.Branch
	if !sameBranch && !(oldHasCommit && otherHasCommit) {
		return "", false
	}
	oldRef := leaderRef(old.agent.MachineID, old.agent.SessionID)
	otherRef := leaderRef(other.MachineID, other.SessionID)
	branchNote := ""
	if sameBranch {
		branchNote = fmt.Sprintf("on branch %s ", old.session.Branch)
	}
	oldNote := clip(oneLine(cmp.Or(old.session.LastText, old.session.Activity, old.step)), 120)
	otherNote := clip(oneLine(cmp.Or(otherSess.LastText, otherSess.Activity, other.Title)), 120)
	return fmt.Sprintf("%sboth %s (%s) and %s (%s) made progress/commits; supervisor must choose which side to keep",
		branchNote, oldRef, oldNote, otherRef, otherNote), true
}

func sessionHasCommitProgress(c proto.Session) bool {
	combined := strings.ToLower(c.LastText + " " + c.Activity)
	for _, kw := range []string{"pushed", "push ", "commit", "committed", "git push"} {
		if strings.Contains(combined, kw) {
			return true
		}
	}
	return strings.Contains(combined, "提交") || strings.Contains(combined, "推送") // i18n-ignore: matches agent transcript output
}

// releaseCoveredNodeAgents releases old Agents on the returning node whose work
// has already been covered elsewhere, stops live ones, and withdraws their
// leftover background waits and pending approval gates.
func (s *Service) releaseCoveredNodeAgents(ctx context.Context, t Todo, items []recoveryTarget) int {
	agents := make([]TodoAgent, 0, len(items))
	workKeys := make([][2]string, 0, len(items))
	for _, item := range items {
		agents = append(agents, item.agent)
		workKeys = append(workKeys, [2]string{item.agent.MachineID, item.agent.SessionID})
		if item.listed && !sessionClosed(item.session) && item.session.Status != proto.StatusOffline {
			sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			_ = s.call(sctx, item.agent.MachineID, deviceSessionStop, map[string]any{"session_id": item.agent.SessionID}, nil)
			cancel()
		}
		s.cancelSessionInteractions(item.agent.MachineID, item.agent.SessionID)
	}
	s.retireTodoWork(workKeys...)
	if err := s.releaseStoppedAgents(ctx, t.ID, agents); err != nil {
		s.logf("node recovery: release covered agents on task %s: %v", t.ID, err)
		return 0
	}
	return len(agents)
}

func activeTodoAgents(t Todo) []TodoAgent {
	var out []TodoAgent
	for _, a := range t.Agents {
		if a.ReleasedAt == nil && a.DismissedAt == nil {
			out = append(out, a)
		}
	}
	return out
}

// sessionResumableAfterOutage reports whether a listed session still has a live
// terminal/host process and can safely accept an automatic recovery wake.
func sessionResumableAfterOutage(c proto.Session) bool {
	if c.Assistant || c.Starting {
		return true
	}
	if sessionClosed(c) {
		return false
	}
	if c.Status == proto.StatusOffline || c.Status == proto.StatusInterrupted {
		return false
	}
	if c.State == proto.StateNeedsYou || c.PendingInteractions > 0 {
		return false
	}
	if c.TurnError != "" && c.TurnErrorKind != proto.TurnErrorTransient && !proto.SilentTurnKind(c.TurnErrorKind) {
		return false
	}
	return true
}

func unresumableReason(listed bool, c proto.Session, rebooted bool) string {
	switch {
	case !listed:
		if rebooted {
			return "session missing after node reboot"
		}
		return "session not listed after reconnect"
	case c.Status == proto.StatusInterrupted:
		return "terminal process interrupted by node reboot"
	case sessionClosed(c) || c.Status == proto.StatusOffline:
		return "agent tab or process closed"
	case c.State == proto.StateNeedsYou || c.PendingInteractions > 0:
		return "agent waits on an approval/question card"
	case c.TurnError != "":
		return "agent stopped on error: " + clip(oneLine(c.TurnError), 120)
	default:
		return "agent cannot resume automatically"
	}
}

func nodeMatchTokens(machineID, machineName, hostname string) []string {
	var out []string
	for _, raw := range []string{machineID, machineName, hostname} {
		v := strings.ToLower(strings.TrimSpace(raw))
		if len(v) < 2 || slices.Contains(out, v) {
			continue
		}
		out = append(out, v)
	}
	return out
}

func matchesNodeTokens(text string, tokens []string) bool {
	if text == "" || len(tokens) == 0 {
		return false
	}
	lower := strings.ToLower(text)
	for _, tok := range tokens {
		if strings.Contains(lower, tok) {
			return true
		}
	}
	return false
}

// waitingOnNodeStep checks whether an Agent running on another machine is
// waiting on a background command, CI job or Monitor tied to the reconnected node.
func waitingOnNodeStep(c proto.Session, tokens []string) (string, bool) {
	if len(tokens) == 0 {
		return "", false
	}
	for _, bg := range c.Background {
		if matchesNodeTokens(bg.Command, tokens) || matchesNodeTokens(bg.Description, tokens) {
			label := cmp.Or(strings.TrimSpace(bg.Description), strings.TrimSpace(bg.Command), bg.Kind)
			return fmt.Sprintf("background %s %q", cmp.Or(bg.Kind, "task"), clip(oneLine(label), 120)), true
		}
	}
	if c.Work != nil && (c.Work.Phase == proto.WorkBlocked || c.Work.Wait != "" || c.Work.Line != "") {
		if matchesNodeTokens(c.Work.Wait, tokens) || matchesNodeTokens(c.Work.Line, tokens) {
			label := cmp.Or(strings.TrimSpace(c.Work.Wait), strings.TrimSpace(c.Work.Line))
			return fmt.Sprintf("waiting on %q", clip(oneLine(label), 120)), true
		}
	}
	if matchesNodeTokens(c.Activity, tokens) && (c.BackgroundWorking || c.BackgroundWaiting || c.State == proto.StateWorking) {
		return fmt.Sprintf("step %q", clip(oneLine(c.Activity), 120)), true
	}
	return "", false
}

func affectedStepSummary(t Todo, c proto.Session, machineName string, isWaiter bool) string {
	if len(c.Background) > 0 {
		bg := c.Background[0]
		label := cmp.Or(strings.TrimSpace(bg.Description), strings.TrimSpace(bg.Command), bg.Kind)
		return fmt.Sprintf("background %s %q", cmp.Or(bg.Kind, "task"), clip(oneLine(label), 120))
	}
	if c.Work != nil && (c.Work.Wait != "" || c.Work.Line != "") {
		return clip(oneLine(cmp.Or(c.Work.Wait, c.Work.Line)), 120)
	}
	if c.Plan != nil && strings.TrimSpace(c.Plan.Current) != "" {
		return clip(oneLine(c.Plan.Current), 120)
	}
	if strings.TrimSpace(c.Activity) != "" {
		return clip(oneLine(c.Activity), 120)
	}
	if strings.TrimSpace(t.Stage) != "" {
		return "stage " + t.Stage
	}
	if strings.TrimSpace(c.LastText) != "" {
		return clip(oneLine(c.LastText), 120)
	}
	if isWaiter {
		return fmt.Sprintf("background job waiting on node %s", machineName)
	}
	return fmt.Sprintf("in-flight step on node %s", machineName)
}

func recoveryEventHeader(ev nodeRecoveryEvent, outageMin int) string {
	if ev.rebooted {
		if outageMin > 0 {
			return fmt.Sprintf("node %s rebooted and reconnected after %dm offline", ev.machineName, outageMin)
		}
		return fmt.Sprintf("node %s rebooted and reconnected", ev.machineName)
	}
	return fmt.Sprintf("node %s reconnected after %dm offline", ev.machineName, outageMin)
}

func formatRecoveryWakeMessage(ev nodeRecoveryEvent, outageMin int, step string, isWaiter bool) string {
	header := recoveryEventHeader(ev, outageMin)
	if isWaiter {
		return fmt.Sprintf("[Node recovery] Target %s. Your %s may have been interrupted and its results lost during the outage. Check the remote state, re-submit any dropped job, and continue the task.",
			header, step)
	}
	return fmt.Sprintf("[Node recovery] Host %s. Your %s and any background commands, Monitors, or tunnels may have been interrupted or lost. Verify local state, re-submit dropped work, and continue.",
		header, step)
}

func formatRecoverySignalDetail(ev nodeRecoveryEvent, outageMin int, items []string) string {
	return fmt.Sprintf("%s; affected step: %s", recoveryEventHeader(ev, outageMin), strings.Join(items, "; "))
}
