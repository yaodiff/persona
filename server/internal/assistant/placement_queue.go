package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/wireids"
)

// Two-layer automatic machine placement kernel mechanism:
//
// When task_plan omits machine, the task is persisted inside its started plan with TaskQueue.Key == "pending_placement".
// Visible tasks emit a one-way placement.pending wake event carrying only {plan_id, todo_id, batch_size}. External
// scheduler extensions granted the named-only tasks.schedule scope poll GET /api/v1/assistant/placement/pending
// (refreshing a short 60s active lease) and submit CAS assign/defer calls. The kernel re-verifies every hard constraint
// and atomically reserves an admission slot (claimIfAllowed) before starting the Agent and writing placement.go audit.
//
// Fallback is never fail-closed: with no active tasks.schedule lease (or for hidden privacy-space tasks), fallback runs
// at 0s; with an active lease, any task still unassigned after 15s falls back to admission.go:recommendMachine. Tasks
// with no matching computer remain queued up to the existing 30m admissionQueueMaxWait bound.

const (
	pendingPlacementQueueKey = "pending_placement"
	placementReasonExtension = "extension_assigned"
	placementReasonFallback  = "fallback_recommend"

	placementLeaseTTL     = 60 * time.Second
	placementDeferMin     = time.Second
	placementDeferMax     = 5 * time.Minute
	placementDeferDefault = 30 * time.Second
	placementBodyLimit    = 16 << 10
	placementIdleSweep    = time.Minute

	placementMaxCapabilities = 16
	placementMaxCandidates   = 32
	placementMaxTagRunes     = 64
	placementMaxReasonRunes  = 480
)

var (
	pendingPlacementFallbackWait  = 15 * time.Second
	pendingPlacementRetryInterval = 30 * time.Second
)

// taskPlacementWait persists automatic placement state inside TaskQueue so hub restarts resume cleanly.
type taskPlacementWait struct {
	Version    int       `json:"version"`
	BatchID    string    `json:"batch_id,omitempty"`
	BatchSize  int       `json:"batch_size,omitempty"`
	BatchIndex int       `json:"batch_index,omitempty"`
	EnqueuedAt time.Time `json:"enqueued_at"`
	FallbackAt time.Time `json:"fallback_at"`
	DeferUntil time.Time `json:"defer_until,omitempty"`
	LastReason string    `json:"last_reason,omitempty"`
	Notified   bool      `json:"notified,omitempty"`
}

// placementSchedulerBook holds short-TTL active scheduler leases and coordinates fallback sweeps.
type placementSchedulerBook struct {
	mu          sync.Mutex
	leases      map[string]time.Time // extension id -> lease expiry
	wake        chan struct{}
	now         func() time.Time // tests override clock without sleeps
	inFlight    map[string]bool  // todo_id currently executing fallback
	manualSweep bool             // tests disable automatic background goroutine when placementFallbackLoop is not running
	loopRunning atomic.Bool
}

type placementEventPublisher interface {
	PlacementEvent(event string, params any) error
}

func (b *BusHub) PlacementEvent(event string, params any) error { return b.notifyHub(event, params) }

func (s *Service) placementNow() time.Time {
	s.placementSched.mu.Lock()
	fn := s.placementSched.now
	s.placementSched.mu.Unlock()
	if fn != nil {
		return fn().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) placementWakeChan() chan struct{} {
	s.placementSched.mu.Lock()
	defer s.placementSched.mu.Unlock()
	if s.placementSched.wake == nil {
		s.placementSched.wake = make(chan struct{}, 1)
	}
	return s.placementSched.wake
}

func (s *Service) wakePendingPlacement() {
	ch := s.placementWakeChan()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// touchPlacementLease refreshes extID's short-TTL active scheduler lease.
func (s *Service) touchPlacementLease(extID string, now time.Time) time.Time {
	extID = strings.TrimSpace(extID)
	if extID == "" {
		return time.Time{}
	}
	exp := now.Add(placementLeaseTTL)
	s.placementSched.mu.Lock()
	if s.placementSched.leases == nil {
		s.placementSched.leases = map[string]time.Time{}
	}
	s.placementSched.leases[extID] = exp
	s.placementSched.mu.Unlock()
	return exp
}

// hasActivePlacementLease reports whether at least one scheduler extension holds an unexpired lease at now.
func (s *Service) hasActivePlacementLease(now time.Time) bool {
	s.placementSched.mu.Lock()
	defer s.placementSched.mu.Unlock()
	active := false
	for id, exp := range s.placementSched.leases {
		if !exp.After(now) {
			delete(s.placementSched.leases, id)
			continue
		}
		active = true
	}
	return active
}

// normalizePlacementConstraints validates and normalizes optional placement_constraints on t.
func (s *Service) normalizePlacementConstraints(ctx context.Context, t *PlanTask) error {
	c := t.PlacementConstraints
	if c == nil {
		return nil
	}
	out := &assistantwire.PlacementConstraints{
		OS:         strings.ToLower(strings.TrimSpace(c.OS)),
		RequireHub: c.RequireHub,
		Workload:   strings.ToLower(strings.TrimSpace(c.Workload)),
	}
	switch out.OS {
	case "", "linux", "darwin", "windows":
	default:
		return fmt.Errorf("placement_constraints.os %q must be linux, darwin, or windows", c.OS)
	}
	switch out.Workload {
	case "", "light", "standard", "heavy":
	default:
		return fmt.Errorf("placement_constraints.workload %q must be light, standard, or heavy", c.Workload)
	}
	if len(c.Capabilities) > placementMaxCapabilities {
		return fmt.Errorf("placement_constraints.capabilities accepts at most %d items", placementMaxCapabilities)
	}
	if len(c.Capabilities) > 0 {
		seen := make(map[string]bool, len(c.Capabilities))
		for _, raw := range c.Capabilities {
			v := strings.ToLower(strings.TrimSpace(raw))
			if v == "" {
				continue
			}
			if utf8.RuneCountInString(v) > placementMaxTagRunes {
				return fmt.Errorf("placement_constraints.capabilities entry exceeds %d characters", placementMaxTagRunes)
			}
			if !seen[v] {
				seen[v] = true
				out.Capabilities = append(out.Capabilities, v)
			}
		}
	}
	if len(c.CandidateMachines) > placementMaxCandidates {
		return fmt.Errorf("placement_constraints.candidate_machines accepts at most %d items", placementMaxCandidates)
	}
	if len(c.CandidateMachines) > 0 {
		seen := make(map[string]bool, len(c.CandidateMachines))
		for _, raw := range c.CandidateMachines {
			v := strings.TrimSpace(raw)
			if v == "" {
				continue
			}
			if m, ok := s.machineByName(ctx, v); ok {
				v = m.ID
			}
			if !seen[v] {
				seen[v] = true
				out.CandidateMachines = append(out.CandidateMachines, v)
			}
		}
	}
	t.PlacementConstraints = out
	return nil
}

func (s *Service) isHubDevice(ctx context.Context, d Device) bool {
	var m *proto.Machine
	if s.devs != nil {
		m, _, _ = s.devs.MachineResources(d.ID)
	}
	return s.isHubMachine(ctx, d, m)
}

// machineMatchesPlacementConstraintsWith checks kernel-enforced hard placement constraints for candidate device d.
// Unknown capability tags are passed through because custom inventory belongs to the extension layer.
// When allowHubFallback is true, the default hub exclusion is relaxed because no online non-hub computer satisfies
// the task's hard constraints (require_hub=true, candidate_machines, os, capabilities, and hub admission still apply).
func (s *Service) machineMatchesPlacementConstraintsWith(ctx context.Context, t PlanTask, d Device, allowHubFallback bool) (hitKey string, hitArgs i18n.Args, err error) {
	var m *proto.Machine
	if s.devs != nil {
		m, _, _ = s.devs.MachineResources(d.ID)
	}
	isHub := s.isHubMachine(ctx, d, m)
	c := t.PlacementConstraints
	requireHub := c != nil && c.RequireHub
	if !requireHub && isHub && !allowHubFallback {
		return "hub_excluded", nil, errors.New("automatic placement excludes the hub computer unless require_hub is true")
	}
	if requireHub && !isHub {
		return "hub_required", nil, errors.New("placement_constraints.require_hub requires the hub computer")
	}
	if c != nil && len(c.CandidateMachines) > 0 {
		canon := s.canonical(d.ID)
		matched := false
		for _, cand := range c.CandidateMachines {
			if cand == d.ID || cand == canon || cand == d.Name || s.canonical(cand) == canon {
				matched = true
				break
			}
		}
		if !matched {
			return "not_candidate", nil, fmt.Errorf("computer %q is not in placement_constraints.candidate_machines", firstNonEmpty(d.Name, d.ID))
		}
	}
	osName := machinePlatformOS(m)
	arch := machinePlatformArch(m)
	if c != nil && c.OS != "" && osName != c.OS {
		return "os_mismatch", i18n.Args{"required": c.OS, "actual": cmp.Or(osName, "unknown")},
			fmt.Errorf("computer %q does not match required os %q", firstNonEmpty(d.Name, d.ID), c.OS)
	}
	if c != nil {
		for _, capTag := range c.Capabilities {
			switch capTag {
			case "darwin", "macos", "xcode":
				if osName != "darwin" {
					return "capability_mismatch", i18n.Args{"capability": capTag},
						fmt.Errorf("computer %q lacks capability %q (requires darwin)", firstNonEmpty(d.Name, d.ID), capTag)
				}
			case "linux":
				if osName != "linux" {
					return "capability_mismatch", i18n.Args{"capability": capTag},
						fmt.Errorf("computer %q lacks capability %q (requires linux)", firstNonEmpty(d.Name, d.ID), capTag)
				}
			case "windows":
				if osName != "windows" {
					return "capability_mismatch", i18n.Args{"capability": capTag},
						fmt.Errorf("computer %q lacks capability %q (requires windows)", firstNonEmpty(d.Name, d.ID), capTag)
				}
			case "arm64", "aarch64":
				if arch != "arm64" {
					return "capability_mismatch", i18n.Args{"capability": capTag},
						fmt.Errorf("computer %q lacks capability %q (requires arm64)", firstNonEmpty(d.Name, d.ID), capTag)
				}
			case "amd64", "x86_64":
				if arch != "amd64" {
					return "capability_mismatch", i18n.Args{"capability": capTag},
						fmt.Errorf("computer %q lacks capability %q (requires amd64)", firstNonEmpty(d.Name, d.ID), capTag)
				}
			case "hub":
				if !isHub {
					return "capability_mismatch", i18n.Args{"capability": capTag},
						fmt.Errorf("computer %q lacks capability %q", firstNonEmpty(d.Name, d.ID), capTag)
				}
			}
		}
	}
	return "", nil, nil
}

func (s *Service) devicePassesPlacementHardConstraints(ctx context.Context, pt PlanTask, d Device, allowHubFallback, checkPresence bool, pl *placement) (eligible, structurallyEligible bool) {
	checkCtx := ctx
	if pl == nil {
		checkCtx = context.WithValue(ctx, placementKey{}, (*placement)(nil))
	} else if placementOf(ctx) != pl {
		checkCtx = context.WithValue(ctx, placementKey{}, pl)
	}
	if err := s.leaderOnWorkMachine(&pt, d); err != nil {
		pl.hit(d.ID, "leader_work_machine", nil)
		return false, false
	}
	if hitKey, hitArgs, err := s.machineMatchesPlacementConstraintsWith(checkCtx, pt, d, allowHubFallback); err != nil {
		pl.hit(d.ID, hitKey, hitArgs)
		return false, false
	}
	var dir string
	if pt.scratchWorkspace() {
		if !d.ScratchWorkspace {
			pl.hit(d.ID, "scratch_unsupported", nil)
			return false, false
		}
	} else {
		if strings.HasPrefix(pt.Cwd, "~") && d.Home == "" {
			pl.hit(d.ID, "directory_missing", nil)
			return false, false
		}
		dir = proto.ExpandHome(pt.Cwd, d.Home)
		if assistantWorkspace(d, dir) {
			pl.hit(d.ID, "assistant_workspace", nil)
			return false, false
		}
	}
	// Resolve the effective launcher on this computer even when the task names no launcher: a default- or
	// tier-launcher task otherwise skips both checks here and gets rejected by assignPendingPlacement on every
	// retry, livelocking until the 30m deadline (#15953).
	resolved, err := s.fallbackTaskLauncher(checkCtx, pt, d.ID)
	if err != nil {
		var line *i18n.Message
		if errors.As(err, &line) {
			pl.violation(d.ID, line)
		} else {
			pl.hit(d.ID, "tier_unavailable", i18n.Args{"tier": pt.Tier})
		}
		return false, false
	}
	if checkPresence && !s.launcherOn(checkCtx, d.ID, resolved) {
		pl.hit(d.ID, "launcher_missing", i18n.Args{"launcher": resolved})
		return false, false
	}
	if dir != "" && !pt.backendWorkspace() && !pt.scratchWorkspace() && !s.taskDirectoryExists(checkCtx, d.ID, dir) {
		return false, false
	}
	// The same on-the-spot probe assignPendingPlacement runs: a stale failing record clears here too instead
	// of requeuing a task a healthy computer could start. A transient launcher failure leaves the computer
	// structurally eligible so online non-hub workers still protect the hub from unintended fallback.
	if h, bad := s.launcherUnusableNow(checkCtx, d.ID, resolved); bad {
		pl.hit(d.ID, "launcher_unhealthy", i18n.Args{"launcher": resolved, "kind": cmp.Or(h.Kind, "unhealthy")})
		return false, true
	}
	return true, true
}

// hasOnlineNonHubEligibleForHardConstraints reports whether at least one reachable non-hub computer satisfies
// pt's non-capacity hard constraints. When false, automatic placement may fall back to the hub computer.
func (s *Service) hasOnlineNonHubEligibleForHardConstraints(ctx context.Context, pt PlanTask) bool {
	checkPresence := pt.Launcher != "" || pt.Tier != ""
	for _, d := range s.devices(ctx) {
		if !d.Reachable || s.isHubDevice(ctx, d) {
			continue
		}
		if _, structural := s.devicePassesPlacementHardConstraints(ctx, pt, d, false, checkPresence, nil); structural {
			return true
		}
	}
	return false
}

func replacePlacementCandidate(dst *placement, src placementCandidate) {
	if dst == nil {
		return
	}
	cp := src
	cp.Hits = slices.Clone(src.Hits)
	for i := range dst.Candidates {
		if dst.Candidates[i].Machine == src.Machine {
			dst.Candidates[i] = cp
			return
		}
	}
	dst.Candidates = append(dst.Candidates, cp)
}

func placementArg(args i18n.Args, key, fallback string) string {
	if args == nil {
		return fallback
	}
	v, ok := args[key]
	if !ok || v == nil {
		return fallback
	}
	if s := strings.TrimSpace(fmt.Sprint(v)); s != "" {
		return s
	}
	return fallback
}

func placementHitText(lang i18n.Lang, h placementHit) string {
	if strings.HasPrefix(h.Key, "assistant-tasks.") {
		return strings.TrimRightFunc(i18n.T(lang, h.Key, h.Args), func(r rune) bool {
			return r == '.' || r == '\u3002'
		})
	}
	switch h.Key {
	case "offline":
		return i18n.T(lang, "assistant-tasks.placement_hit_offline")
	case "hub_excluded":
		return i18n.T(lang, "assistant-tasks.placement_hit_hub_excluded")
	case "hub_required":
		return i18n.T(lang, "assistant-tasks.placement_hit_hub_required")
	case "not_candidate":
		return i18n.T(lang, "assistant-tasks.placement_hit_not_candidate")
	case "os_mismatch":
		return i18n.T(lang, "assistant-tasks.placement_hit_os_mismatch", i18n.Args{
			"actual":   placementArg(h.Args, "actual", "unknown"),
			"required": placementArg(h.Args, "required", "unknown"),
		})
	case "capability_mismatch":
		return i18n.T(lang, "assistant-tasks.placement_hit_capability_mismatch", i18n.Args{
			"capability": placementArg(h.Args, "capability", "unknown"),
		})
	case "leader_work_machine":
		return i18n.T(lang, "assistant-tasks.placement_hit_leader_work_machine")
	case "assistant_workspace":
		return i18n.T(lang, "assistant-tasks.placement_hit_assistant_workspace")
	case "directory_missing":
		return i18n.T(lang, "assistant-tasks.placement_hit_directory_missing")
	case "directory_unknown":
		return i18n.T(lang, "assistant-tasks.placement_hit_directory_unknown")
	case "scratch_unsupported":
		return i18n.T(lang, "assistant-tasks.placement_hit_scratch_unsupported")
	case "launcher_missing":
		return i18n.T(lang, "assistant-tasks.placement_hit_launcher_missing", i18n.Args{
			"launcher": placementArg(h.Args, "launcher", "unknown"),
		})
	case "launcher_unhealthy":
		return i18n.T(lang, "assistant-tasks.placement_hit_launcher_unhealthy", i18n.Args{
			"launcher": placementArg(h.Args, "launcher", "unknown"),
			"kind":     placementArg(h.Args, "kind", "unhealthy"),
		})
	case "no_resources":
		return i18n.T(lang, "assistant-tasks.placement_hit_no_resources")
	default:
		return h.Key
	}
}

func (s *Service) placementLocalizedErr(ctx context.Context, err error) string {
	var msg *i18n.Message
	if errors.As(err, &msg) {
		return msg.Text(s.lang(ctx))
	}
	return err.Error()
}

func (s *Service) placementNoMatchReason(ctx context.Context, pl *placement) string {
	lang := s.lang(ctx)
	if pl == nil || len(pl.Candidates) == 0 {
		return i18n.T(lang, "assistant-tasks.placement_no_match")
	}
	var onlineParts, offlineParts []string
	for _, c := range pl.Candidates {
		if len(c.Hits) == 0 {
			continue
		}
		name := c.Machine
		if d, ok := s.device(ctx, c.Machine); ok {
			name = firstNonEmpty(d.Name, c.Machine)
		}
		hits := make([]string, 0, len(c.Hits))
		onlyOffline := true
		for _, h := range c.Hits {
			if h.Key != "offline" {
				onlyOffline = false
			}
			hits = append(hits, placementHitText(lang, h))
		}
		entry := fmt.Sprintf("%s: %s", name, strings.Join(hits, ", "))
		if onlyOffline {
			offlineParts = append(offlineParts, entry)
		} else {
			onlineParts = append(onlineParts, entry)
		}
	}
	parts := append(onlineParts, offlineParts...)
	if len(parts) == 0 {
		return i18n.T(lang, "assistant-tasks.placement_no_match")
	}
	return clip(oneLine(i18n.T(lang, "assistant-tasks.placement_no_match_detail", i18n.Args{
		"details": strings.Join(parts, "; "),
	})), placementMaxReasonRunes)
}

func placementDiagnosticLog(pl *placement) string {
	if pl == nil || len(pl.Candidates) == 0 {
		return "no_candidates"
	}
	parts := make([]string, 0, len(pl.Candidates))
	for _, c := range pl.Candidates {
		if len(c.Hits) == 0 {
			continue
		}
		hits := make([]string, 0, len(c.Hits))
		for _, h := range c.Hits {
			if len(h.Args) == 0 {
				hits = append(hits, h.Key)
				continue
			}
			keys := make([]string, 0, len(h.Args))
			for k := range h.Args {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			kvs := make([]string, 0, len(keys))
			for _, k := range keys {
				kvs = append(kvs, fmt.Sprintf("%s=%v", k, h.Args[k]))
			}
			hits = append(hits, fmt.Sprintf("%s(%s)", h.Key, strings.Join(kvs, ",")))
		}
		parts = append(parts, fmt.Sprintf("%s[%s]", c.Machine, strings.Join(hits, ",")))
	}
	if len(parts) == 0 {
		return "no_hits"
	}
	return strings.Join(parts, " ")
}

func (s *Service) pendingPlacementQueueEntry(ctx context.Context, planID string, batchSize, batchIdx int, now time.Time) TaskQueue {
	if batchSize < 1 {
		batchSize = 1
	}
	fallbackAt := now
	if s.hasActivePlacementLease(now) {
		fallbackAt = now.Add(pendingPlacementFallbackWait)
	}
	reason := i18n.T(s.lang(ctx), "assistant-tasks.placement_waiting")
	return TaskQueue{
		Reason:   reason,
		Key:      pendingPlacementQueueKey,
		Attempt:  0,
		QueuedAt: now,
		NextAt:   fallbackAt,
		Placement: &taskPlacementWait{
			Version:    1,
			BatchID:    planID,
			BatchSize:  batchSize,
			BatchIndex: batchIdx,
			EnqueuedAt: now,
			FallbackAt: fallbackAt,
			Notified:   true,
		},
	}
}

func (s *Service) pendingPlacementPending(ctx context.Context, t Todo) bool {
	if t.ID == "" {
		return false
	}
	for _, p := range s.queueCandidatePlans(ctx) {
		if p.State != planStarted {
			continue
		}
		for _, pt := range p.Tasks {
			if pt.TodoID == t.ID && pt.Queue != nil && pt.Queue.Key == pendingPlacementQueueKey {
				return true
			}
		}
	}
	return false
}

func (s *Service) placementVisibleToExtensions(ctx context.Context, todoID string) bool {
	if todoID == "" {
		return false
	}
	return s.todoVerdicts(ctx, []string{todoID}, nil, nil)[todoID] == todoShown
}

func (s *Service) emitPlacementPending(planID, todoID string, batchSize int) {
	if planID == "" || todoID == "" || batchSize <= 0 {
		return
	}
	ev := assistantwire.PlacementPendingEvent{PlanID: assistantwire.PlanID(planID), TodoID: wireids.TodoID(todoID), BatchSize: batchSize}
	if pub, ok := s.hub.(placementEventPublisher); ok {
		if err := pub.PlacementEvent(assistantwire.EventPlacementPending, ev); err != nil {
			s.logf("placement: event %s: %v", todoID, err)
		}
		return
	}
	if pub, ok := s.hub.(todoEventPublisher); ok {
		if err := pub.TodoEvent(assistantwire.EventPlacementPending, ev); err != nil {
			s.logf("placement: event %s: %v", todoID, err)
		}
	}
}

func (s *Service) onPendingPlacementEnqueued(ctx context.Context, planID, todoID string, batchSize int) {
	now := s.placementNow()
	visible := s.placementVisibleToExtensions(ctx, todoID)
	activeLease := s.hasActivePlacementLease(now)
	s.logf("task %s: placement queue: enqueued (plan %s, visible=%t, active_lease=%t)", todoID, planID, visible, activeLease)
	if visible {
		s.emitPlacementPending(planID, todoID, batchSize)
	}
	needsImmediate := !visible || !activeLease
	if s.placementSched.loopRunning.Load() {
		s.wakePendingPlacement()
		return
	}
	s.placementSched.mu.Lock()
	manual := s.placementSched.manualSweep
	s.placementSched.mu.Unlock()
	if needsImmediate && !manual {
		s.taskStarting.Add(1)
		go func() {
			defer s.taskStarting.Add(-1)
			s.sweepPendingPlacement(s.runCtx())
		}()
	}
}

// placementFallbackLoop resumes persisted pending_placement tasks across hub restarts and drives 0s/15s fallback and 30m timeouts.
func (s *Service) placementFallbackLoop(ctx context.Context) {
	s.placementSched.loopRunning.Store(true)
	defer s.placementSched.loopRunning.Store(false)
	wake := s.placementWakeChan()
	for {
		nextWait := s.sweepPendingPlacement(ctx)
		if nextWait <= 0 {
			nextWait = placementIdleSweep
		}
		timer := time.NewTimer(nextWait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// sweepPendingPlacement runs one pass over persisted pending_placement tasks and returns when the next check is due.
func (s *Service) sweepPendingPlacement(ctx context.Context) time.Duration {
	cctx, cancel := context.WithTimeout(ctx, taskFailTimeout)
	defer cancel()
	if err := s.ensureTodosLoaded(cctx); err != nil {
		s.logf("placement queue: load todos: %v", err)
		return placementIdleSweep
	}
	now := s.placementNow()
	activeLease := s.hasActivePlacementLease(now)
	nextWake := placementIdleSweep
	updateWake := func(d time.Duration) {
		if d < 0 {
			d = 0
		}
		if d < nextWake {
			nextWake = d
		}
	}

	for _, p := range s.queueCandidatePlans(cctx) {
		if p.State != planStarted {
			continue
		}
		for i := range p.Tasks {
			pt := p.Tasks[i]
			q := pt.Queue
			if q == nil || q.Key != pendingPlacementQueueKey {
				continue
			}
			todo, ok := s.todoNow(pt.TodoID)
			if !ok || todo.DoneAt != nil || todo.Cancelled() {
				if err := s.patchPlanTask(cctx, p.ID, i, func(t *PlanTask) {
					if t.Queue != nil && t.Queue.Key == pendingPlacementQueueKey {
						t.Queue = nil
					}
				}); err != nil {
					s.logf("task %s: placement queue: clear ended task: %v", pt.TodoID, err)
				}
				continue
			}
			if now.Sub(q.QueuedAt) >= admissionQueueMaxWait {
				s.failQueuedStart(cctx, p, i, pt, q)
				continue
			}
			pw := q.Placement
			if pw == nil {
				pw = &taskPlacementWait{Version: 1, BatchID: p.ID, BatchSize: 1, EnqueuedAt: q.QueuedAt, FallbackAt: q.NextAt}
			}
			// If explicitly deferred into the future, wait until DeferUntil (bounded by the 30m overall deadline).
			if !pw.DeferUntil.IsZero() && pw.DeferUntil.After(now) {
				updateWake(pw.DeferUntil.Sub(now))
				updateWake(admissionQueueMaxWait - now.Sub(q.QueuedAt))
				continue
			}
			visible := s.placementVisibleToExtensions(cctx, pt.TodoID)
			// When a deferred task becomes due while a scheduler extension is active, emit one wake hint and give it its 15s window.
			if activeLease && visible && !pw.DeferUntil.IsZero() && !pw.Notified {
				s.emitPlacementPending(p.ID, pt.TodoID, max(pw.BatchSize, 1))
				_ = s.patchPlanTask(cctx, p.ID, i, func(t *PlanTask) {
					if t.Queue != nil && t.Queue.Key == pendingPlacementQueueKey && t.Queue.Placement != nil && t.Queue.Placement.Version == pw.Version {
						t.Queue.Placement.Notified = true
					}
				})
			}
			// On the initial attempt (Attempt == 0), run immediately when hidden from extensions or no scheduler lease is
			// active. On subsequent retries (Attempt > 0), honor FallbackAt so unrelated wakes do not hammer retries faster
			// than pendingPlacementRetryInterval.
			fallbackDue := !pw.FallbackAt.After(now) || (q.Attempt == 0 && (!visible || !activeLease))
			if !fallbackDue {
				updateWake(pw.FallbackAt.Sub(now))
				updateWake(admissionQueueMaxWait - now.Sub(q.QueuedAt))
				continue
			}
			s.runPlacementFallback(cctx, p, i, pt, pw.Version, now)
			updateWake(pendingPlacementRetryInterval)
			updateWake(admissionQueueMaxWait - now.Sub(q.QueuedAt))
		}
	}
	return nextWake
}

// fallbackTaskLauncher is the launcher a deferred task would start on machine: the task's own launcher when it names
// one, else the candidate its tier picks on that machine (the same per-machine pick applyStartTier runs at dispatch),
// else the configured default (model settings' agent.new, then claude). The fallback's candidate filter must judge
// this launcher so a task without an explicit launcher never gets recommended a computer whose resolved launcher is
// missing or unhealthy, which would reject the assignment every 30s until the 30m queue deadline.
func (s *Service) fallbackTaskLauncher(ctx context.Context, pt PlanTask, machine string) (string, error) {
	if pt.Launcher != "" {
		return s.startLauncher(ctx, pt.Launcher), nil
	}
	if pt.Tier != "" {
		pick, err := s.pickTierCandidate(ctx, pt.Tier, tierFacts{Machine: machine})
		if err != nil {
			return "", err
		}
		return s.startLauncher(ctx, pick.Tier.Launcher), nil
	}
	return s.startLauncher(ctx, ""), nil
}

func (s *Service) runPlacementFallback(ctx context.Context, p *TaskPlan, idx int, pt PlanTask, version int, now time.Time) {
	s.placementSched.mu.Lock()
	if s.placementSched.inFlight == nil {
		s.placementSched.inFlight = map[string]bool{}
	}
	if s.placementSched.inFlight[pt.TodoID] {
		s.placementSched.mu.Unlock()
		return
	}
	s.placementSched.inFlight[pt.TodoID] = true
	s.placementSched.mu.Unlock()
	defer func() {
		s.placementSched.mu.Lock()
		delete(s.placementSched.inFlight, pt.TodoID)
		s.placementSched.mu.Unlock()
	}()

	pl := newPlacement("auto", "enforce")
	pl.TaskID = pt.TodoID
	pctx := context.WithValue(ctx, placementKey{}, pl)

	nonHubEligible := false
	// Candidate filtering must match what assignPendingPlacement enforces. checkPresence is only relaxed on a second
	// pass for tasks without any launcher or tier: fitLauncher swaps a default launcher a computer lacks instead of
	// refusing, so a missing default alone must not starve the task when no computer offers it. A failing launcher is
	// always a refusal there and stays one here.
	suitable := func(checkPresence bool) func(Device) bool {
		return func(d Device) bool {
			eligible, structural := s.devicePassesPlacementHardConstraints(pctx, pt, d, false, checkPresence, pl)
			if structural && !s.isHubDevice(pctx, d) {
				nonHubEligible = true
			}
			return eligible
		}
	}
	alt, ok := s.recommendMachine(pctx, "", suitable(true))
	if !ok && pt.Launcher == "" && pt.Tier == "" {
		// No computer offers the resolved default: keep the pre-#15953 behavior and let the assignment swap to a
		// launcher the chosen computer has rather than failing at the queue deadline. Health stays enforced.
		alt, ok = s.recommendMachine(pctx, "", suitable(false))
	}

	hubFallback := false
	requireHub := pt.PlacementConstraints != nil && pt.PlacementConstraints.RequireHub
	if !ok && !nonHubEligible && !requireHub {
		tryHub := func(checkPresence bool) (*placement, Device, bool) {
			hubPl := newPlacement("auto", "enforce")
			hubPl.TaskID = pt.TodoID
			hubCtx := context.WithValue(ctx, placementKey{}, hubPl)
			hubAlt, hubOK := s.recommendMachine(hubCtx, "", func(d Device) bool {
				if !s.isHubDevice(hubCtx, d) {
					return false
				}
				eligible, _ := s.devicePassesPlacementHardConstraints(hubCtx, pt, d, true, checkPresence, hubPl)
				return eligible
			})
			return hubPl, hubAlt, hubOK
		}
		hubPl, hubAlt, hubOK := tryHub(true)
		if !hubOK && pt.Launcher == "" && pt.Tier == "" {
			if swapPl, swapAlt, swapOK := tryHub(false); swapOK {
				hubPl, hubAlt, hubOK = swapPl, swapAlt, true
			}
		}
		for _, c := range hubPl.Candidates {
			if d, found := s.device(ctx, c.Machine); found && s.isHubDevice(ctx, d) {
				replacePlacementCandidate(pl, c)
			}
		}
		if hubOK {
			alt, ok, hubFallback = hubAlt, true, true
		}
	}

	var failReason string
	var assignErr error
	if ok {
		reason := "kernel fallback: lowest load"
		if hubFallback {
			reason = "kernel fallback: hub computer (no eligible non-hub computer online)"
		}
		if _, err := s.assignPendingPlacementWith(pctx, p.ID, p.Conversation, idx, pt, alt.ID, version, placementReasonFallback, reason, pl, hubFallback); err == nil {
			return
		} else {
			assignErr = err
			failReason = clip(oneLine(s.placementLocalizedErr(ctx, err)), placementMaxReasonRunes)
		}
	} else {
		failReason = s.placementNoMatchReason(ctx, pl)
	}

	retryAt := now.Add(pendingPlacementRetryInterval)
	attempt := 0
	updated := false
	if err := s.patchPlanTask(ctx, p.ID, idx, func(t *PlanTask) {
		if t.TodoID != pt.TodoID || t.Queue == nil || t.Queue.Key != pendingPlacementQueueKey {
			return
		}
		if t.Queue.Placement == nil {
			t.Queue.Placement = &taskPlacementWait{Version: version, BatchID: p.ID, BatchSize: 1, EnqueuedAt: t.Queue.QueuedAt}
		}
		if t.Queue.Placement.Version != version {
			return
		}
		t.Queue.Attempt++
		attempt = t.Queue.Attempt
		t.Queue.Reason = failReason
		t.Queue.NextAt = retryAt
		t.Queue.Placement.Version++
		t.Queue.Placement.LastReason = failReason
		t.Queue.Placement.DeferUntil = time.Time{}
		t.Queue.Placement.FallbackAt = retryAt
		t.Queue.Placement.Notified = true
		pl.ReasonKey = pendingPlacementQueueKey
		pl.Reason = failReason
		pl.WaitAt = retryAt
		t.Placement = pl
		updated = true
	}); err != nil {
		s.logf("task %s: placement fallback requeue: %v", pt.TodoID, err)
		return
	}
	if updated {
		if assignErr != nil {
			s.logf("task %s: placement queue (attempt %d): assign %s failed: %s", pt.TodoID, attempt, alt.ID, failReason)
		} else {
			s.logf("task %s: placement queue (attempt %d): %s [%s]", pt.TodoID, attempt, failReason, placementDiagnosticLog(pl))
		}
	}
}

type placementHTTPError struct {
	status int
	code   string
	err    error
	extra  map[string]any
}

func (e *placementHTTPError) Error() string { return e.err.Error() }
func (e *placementHTTPError) Unwrap() error { return e.err }

type placementAssignResult struct {
	PlanID      string `json:"plan_id"`
	TodoID      string `json:"todo_id"`
	Machine     string `json:"machine"`
	Cwd         string `json:"cwd"`
	PlacementID string `json:"placement_id"`
}

// assignPendingPlacement verifies every kernel hard constraint and atomically claims an admission slot (claimIfAllowed)
// before CAS-committing the target machine into the plan and launching the Agent asynchronously.
func (s *Service) assignPendingPlacement(
	ctx context.Context,
	planID, conversation string,
	idx int,
	pt PlanTask,
	targetMachine string,
	expectedVersion int,
	reasonKey, reason string,
	audit *placement,
) (*placementAssignResult, error) {
	allowHubFallback := false
	if m, ok := s.machineByName(ctx, targetMachine); ok && s.isHubDevice(ctx, m) && (pt.PlacementConstraints == nil || !pt.PlacementConstraints.RequireHub) {
		allowHubFallback = !s.hasOnlineNonHubEligibleForHardConstraints(ctx, pt)
	}
	return s.assignPendingPlacementWith(ctx, planID, conversation, idx, pt, targetMachine, expectedVersion, reasonKey, reason, audit, allowHubFallback)
}

func (s *Service) assignPendingPlacementWith(
	ctx context.Context,
	planID, conversation string,
	idx int,
	pt PlanTask,
	targetMachine string,
	expectedVersion int,
	reasonKey, reason string,
	audit *placement,
	allowHubFallback bool,
) (*placementAssignResult, error) {
	m, ok := s.machineByName(ctx, targetMachine)
	if !ok {
		return nil, &placementHTTPError{status: http.StatusUnprocessableEntity, code: "unknown_machine",
			err: fmt.Errorf("unknown machine %q", targetMachine)}
	}
	if !m.Reachable {
		return nil, &placementHTTPError{status: http.StatusUnprocessableEntity, code: "machine_offline",
			err: machineOffline(firstNonEmpty(m.Name, m.ID))}
	}
	if err := s.leaderOnWorkMachine(&pt, m); err != nil {
		return nil, &placementHTTPError{status: http.StatusUnprocessableEntity, code: "leader_work_machine", err: err}
	}
	if hitKey, _, err := s.machineMatchesPlacementConstraintsWith(ctx, pt, m, allowHubFallback); err != nil {
		return nil, &placementHTTPError{status: http.StatusUnprocessableEntity, code: "constraint_violated", err: err,
			extra: map[string]any{"constraint": hitKey}}
	}

	candidate := pt
	candidate.Machine = m.ID
	if candidate.scratchWorkspace() {
		if !m.ScratchWorkspace {
			return nil, &placementHTTPError{status: http.StatusUnprocessableEntity, code: "scratch_unsupported",
				err: scratchUnsupportedError(ctx, s.lang(ctx), firstNonEmpty(m.Name, m.ID))}
		}
	} else {
		if strings.HasPrefix(candidate.Cwd, "~") && m.Home == "" {
			return nil, &placementHTTPError{status: http.StatusUnprocessableEntity, code: "directory_missing",
				err: fmt.Errorf("computer %q has not reported a home directory to expand %q", firstNonEmpty(m.Name, m.ID), candidate.Cwd)}
		}
		candidate.Cwd = proto.ExpandHome(candidate.Cwd, m.Home)
		if assistantWorkspace(m, candidate.Cwd) {
			return nil, &placementHTTPError{status: http.StatusUnprocessableEntity, code: "agent_assistant_workspace",
				err: i18n.Err("rpc-errors.agent_assistant_workspace")}
		}
		if candidate.Cwd != "" && !candidate.backendWorkspace() && !s.taskDirectoryExists(ctx, m.ID, candidate.Cwd) {
			return nil, &placementHTTPError{status: http.StatusUnprocessableEntity, code: "directory_missing",
				err: fmt.Errorf("directory %q does not exist on %s", candidate.Cwd, firstNonEmpty(m.Name, m.ID))}
		}
	}

	// Launcher fitting, speed/readonly/mode checks, and launcher health verification on the chosen computer.
	asked := ""
	if candidate.CallerPermission != nil {
		asked = strings.TrimSpace(*candidate.CallerPermission)
	}
	own := candidate.launchPick()
	fit, fitParams, fitOK, err := s.resolveFitPlanTask(ctx, &candidate)
	if err != nil {
		return nil, &placementHTTPError{status: http.StatusUnprocessableEntity, code: "launcher_unavailable", err: err}
	}
	resolvedLauncher := candidate.Launcher
	if lp, _ := fitParams["launcher"].(string); lp != "" {
		resolvedLauncher = lp
	}
	takesModes := true
	if fitOK {
		if err := checkStartSpeed(fitParams, fit, ""); err != nil {
			return nil, &placementHTTPError{status: http.StatusUnprocessableEntity, code: "launcher_unavailable", err: err}
		}
		if candidate.Readonly {
			if err := s.readonlyLauncherRefusal(ctx, m, fitParams, fit); err != nil {
				return nil, &placementHTTPError{status: http.StatusUnprocessableEntity, code: "launcher_unavailable", err: err}
			}
		}
		configuredMode, _ := fitParams["permission_mode"].(string)
		if !candidate.preservePermission {
			if candidate.CallerPermission != nil {
				candidate.Permission = *candidate.CallerPermission
				candidate.normalizeReadonlyPermission(readonlyModeOf(s.settings(ctx)))
			}
			switch {
			case candidate.Readonly:
				if fit.allowAll() {
					candidate.Permission = "bypassPermissions"
				}
			case configuredMode != "" && fit.supportsPermissionMode(configuredMode):
				if candidate.Permission == "" {
					candidate.Permission = configuredMode
				}
			case fit.allowAllOver(candidate.Permission):
				candidate.Permission = "bypassPermissions"
			}
		}
		if takesModes = startTakesModes(fit.agent); !takesModes {
			if err := startModesRefused(cmp.Or(candidate.Launcher, resolvedLauncher), fit.agent, asked, own.effort); err != nil {
				return nil, &placementHTTPError{status: http.StatusUnprocessableEntity, code: "launcher_unavailable", err: err}
			}
			if asked != "" {
				candidate.Permission = ""
			}
		}
		if !startTakesEffort(fit.agent) {
			candidate.Effort = ""
		}
	}
	if candidate.Permission == "" && takesModes {
		candidate.Permission = s.taskPermission(ctx, m.ID, nil)
	}
	// Name the resolved launcher, not the task's possibly empty field: a default-launcher task must never format a
	// blank launcher in fixed_launcher_unhealthy.
	checkedLauncher := s.startLauncher(ctx, cmp.Or(candidate.Launcher, resolvedLauncher))
	if r, bad := s.launcherUnusableNow(ctx, m.ID, checkedLauncher); bad {
		return nil, &placementHTTPError{status: http.StatusUnprocessableEntity, code: "launcher_unavailable",
			err: i18n.Err("assistant-tasks.fixed_launcher_unhealthy", i18n.Args{
				"machine": m.ID, "launcher": checkedLauncher, "reason": r.Error,
			})}
	}
	if err := s.resolveTaskWorkspace(ctx, &candidate); err != nil {
		code := "workspace_unavailable"
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == "scratch_unsupported" {
			code = "scratch_unsupported"
		}
		return nil, &placementHTTPError{status: http.StatusUnprocessableEntity, code: code, err: err}
	}
	s.resolveTaskRepo(ctx, &candidate)

	pl := audit
	if pl == nil {
		pl = newPlacement("auto", "enforce")
	} else {
		cp := *pl
		cp.Candidates = pl.cloneCandidates()
		pl = &cp
	}
	if pt.Placement != nil && pt.Placement.ID != "" {
		pl.ID = pt.Placement.ID
		if !pt.Placement.At.IsZero() {
			pl.At = pt.Placement.At
		}
	}
	pl.TaskID = pt.TodoID
	pl.Choice = m.ID
	pl.ReasonKey = reasonKey
	pl.Reason = clip(oneLine(reason), placementMaxReasonRunes)
	pl.candidate(m.ID)

	// Atomic admission gate & slot claim. Released on any failure or CAS conflict; handed to runPlanTaskStart on success.
	var release func()
	releaseTransferred := false
	defer func() {
		if release != nil && !releaseTransferred {
			release()
		}
	}()
	if s.devs != nil && s.admissionEnabled(ctx) {
		cfg := s.admissionSettings(ctx)
		direction := s.taskDirection(ctx, &candidate)
		launcher := s.startLauncher(ctx, cmp.Or(candidate.Launcher, resolvedLauncher))
		lim := s.limitsFor(ctx, m.ID, direction, candidate.Project, cfg).withLauncher(cfg, launcher)
		var violation *i18n.Message
		release, violation = s.claimIfAllowed(context.WithValue(ctx, placementKey{}, pl), m.ID, direction, lim)
		if violation != nil {
			return nil, &placementHTTPError{status: http.StatusUnprocessableEntity, code: "admission_refused",
				err: s.admissionRefused(context.WithValue(ctx, placementKey{}, pl), m.ID, direction, violation)}
		}
	}

	if pl.Reason != "" {
		candidate.noteWhy(pl.Reason)
	}
	candidate.Placement = pl

	todo, _ := s.todoNow(pt.TodoID)
	readiness, readinessErr := s.taskTodoReadiness(ctx, todo, pt)
	if readinessErr != nil {
		s.logf("task %s: placement queue: read prerequisites: %v", pt.TodoID, readinessErr)
	}
	blocked := readinessErr != nil || !readiness.ready()

	wasRetry := false
	casResult := "not_pending"
	currentVersion := 0
	currentMachine := ""
	if err := s.patchPlanTask(ctx, planID, idx, func(live *PlanTask) {
		if live.TodoID != pt.TodoID {
			casResult = "not_found"
			return
		}
		if live.Queue == nil || live.Queue.Key != pendingPlacementQueueKey {
			casResult = "already_assigned"
			currentMachine = live.Machine
			return
		}
		liveVer := 1
		if live.Queue.Placement != nil && live.Queue.Placement.Version > 0 {
			liveVer = live.Queue.Placement.Version
		}
		if liveVer != expectedVersion {
			casResult = "version_mismatch"
			currentVersion = liveVer
			return
		}
		wasRetry = live.Queue.Retry
		live.Machine = candidate.Machine
		live.Cwd = candidate.Cwd
		live.Repo = candidate.Repo
		live.Workspace = candidate.Workspace
		live.WorkspaceResolved = candidate.WorkspaceResolved
		live.WorkspaceInferred = candidate.WorkspaceInferred
		live.Launcher = candidate.Launcher
		live.Model = candidate.Model
		live.Effort = candidate.Effort
		live.Speed = candidate.Speed
		live.Tier = candidate.Tier
		live.Permission = candidate.Permission
		live.Why = candidate.Why
		live.Placement = pl
		if blocked {
			q := s.blockerWaitQueue(ctx, readiness.refs())
			q.Retry = wasRetry
			live.Queue = q
			live.Placement = queuedPlacement(*live, q)
			pl = live.Placement
		} else {
			live.Queue = nil
		}
		casResult = "ok"
	}); err != nil {
		return nil, err
	}
	switch casResult {
	case "ok":
	case "version_mismatch":
		return nil, &placementHTTPError{status: http.StatusConflict, code: "placement_version_mismatch",
			err:   fmt.Errorf("placement version mismatch: got %d, current is %d", expectedVersion, currentVersion),
			extra: map[string]any{"current_version": currentVersion}}
	case "already_assigned":
		return nil, &placementHTTPError{status: http.StatusConflict, code: "placement_conflict",
			err:   errors.New("task is no longer waiting for placement"),
			extra: map[string]any{"machine": currentMachine}}
	default:
		return nil, &placementHTTPError{status: http.StatusNotFound, code: "placement_not_found",
			err: errors.New("pending placement task not found")}
	}

	s.savePlacement(ctx, pl)
	if blocked {
		s.logf("task %s: placed on %s and waiting for prerequisites", pt.TodoID, m.ID)
		assignedCwd := candidate.Cwd
		if assignedCwd == "" && candidate.WorkspaceResolved != nil {
			assignedCwd = candidate.WorkspaceResolved.Path
		}
		return &placementAssignResult{
			PlanID:      planID,
			TodoID:      pt.TodoID,
			Machine:     m.ID,
			Cwd:         assignedCwd,
			PlacementID: pl.ID,
		}, nil
	}
	s.logf("task %s: placement queue: assigned to %s (%s)", pt.TodoID, m.ID, pl.Reason)
	candidate.Queue = nil
	candidate.admissionRelease = release
	candidate.admissionClaimMachine = m.ID
	releaseTransferred = true
	s.startPlanTaskAsync(planID, conversation, idx, candidate, wasRetry, nil)

	assignedCwd := candidate.Cwd
	if assignedCwd == "" && candidate.WorkspaceResolved != nil {
		assignedCwd = candidate.WorkspaceResolved.Path
	}
	return &placementAssignResult{
		PlanID:      planID,
		TodoID:      pt.TodoID,
		Machine:     m.ID,
		Cwd:         assignedCwd,
		PlacementID: pl.ID,
	}, nil
}

func (s *Service) findPendingPlacementTask(ctx context.Context, todoID string) (*TaskPlan, int, PlanTask, bool) {
	for _, p := range s.queueCandidatePlans(ctx) {
		if p.State != planStarted {
			continue
		}
		for i, pt := range p.Tasks {
			if pt.TodoID == todoID {
				return p, i, pt, true
			}
		}
	}
	if todo, ok := s.todoNow(todoID); ok {
		if p, i, err := s.currentPlanAssignment(ctx, todo); err == nil {
			return p, i, p.Tasks[i], true
		}
	}
	return nil, 0, PlanTask{}, false
}

func (s *Service) deferPendingPlacement(ctx context.Context, planID string, idx int, todoID, reason string, nextAt time.Time, expectedVersion int) (int, time.Time, error) {
	now := s.placementNow()
	if nextAt.IsZero() {
		nextAt = now.Add(placementDeferDefault)
	} else {
		nextAt = nextAt.UTC()
		if minAt := now.Add(placementDeferMin); nextAt.Before(minAt) {
			nextAt = minAt
		}
		if maxAt := now.Add(placementDeferMax); nextAt.After(maxAt) {
			nextAt = maxAt
		}
	}
	reason = clip(oneLine(reason), placementMaxReasonRunes)
	fallbackAt := nextAt.Add(pendingPlacementFallbackWait)
	casResult := "not_pending"
	newVersion := 0
	currentVersion := 0
	currentMachine := ""
	if err := s.patchPlanTask(ctx, planID, idx, func(live *PlanTask) {
		if live.TodoID != todoID {
			casResult = "not_found"
			return
		}
		if live.Queue == nil || live.Queue.Key != pendingPlacementQueueKey {
			casResult = "already_assigned"
			currentMachine = live.Machine
			return
		}
		if live.Queue.Placement == nil {
			live.Queue.Placement = &taskPlacementWait{Version: 1, BatchID: planID, BatchSize: 1, EnqueuedAt: live.Queue.QueuedAt}
		}
		if live.Queue.Placement.Version != expectedVersion {
			casResult = "version_mismatch"
			currentVersion = live.Queue.Placement.Version
			return
		}
		live.Queue.Attempt++
		live.Queue.Reason = reason
		live.Queue.NextAt = nextAt
		live.Queue.Placement.Version++
		live.Queue.Placement.LastReason = reason
		live.Queue.Placement.DeferUntil = nextAt
		live.Queue.Placement.FallbackAt = fallbackAt
		live.Queue.Placement.Notified = false
		live.Placement = queuedPlacement(*live, live.Queue)
		newVersion = live.Queue.Placement.Version
		casResult = "ok"
	}); err != nil {
		return 0, time.Time{}, err
	}
	switch casResult {
	case "ok":
		s.wakePendingPlacement()
		return newVersion, nextAt, nil
	case "version_mismatch":
		return 0, time.Time{}, &placementHTTPError{status: http.StatusConflict, code: "placement_version_mismatch",
			err:   fmt.Errorf("placement version mismatch: got %d, current is %d", expectedVersion, currentVersion),
			extra: map[string]any{"current_version": currentVersion}}
	case "already_assigned":
		return 0, time.Time{}, &placementHTTPError{status: http.StatusConflict, code: "placement_conflict",
			err:   errors.New("task is no longer waiting for placement"),
			extra: map[string]any{"machine": currentMachine}}
	default:
		return 0, time.Time{}, &placementHTTPError{status: http.StatusNotFound, code: "placement_not_found",
			err: errors.New("pending placement task not found")}
	}
}

func writePlacementErr(w http.ResponseWriter, err error) {
	var pe *placementHTTPError
	if errors.As(err, &pe) {
		body := map[string]any{"error": pe.Error(), "code": pe.code}
		for k, v := range pe.extra {
			body[k] = v
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(pe.status)
		_ = json.NewEncoder(w).Encode(body)
		return
	}
	apierr.Write(w, http.StatusServiceUnavailable, err)
}

func (s *Service) checkPlacementCaller(w http.ResponseWriter, r *http.Request) (fromExtension bool, extID string, ok bool) {
	role := r.Header.Get(assistantwire.RoleHeader)
	if role != "" && role != roleFull && role != roleExtension {
		apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot access placement scheduling"))
		return false, "", false
	}
	if role == roleExtension {
		extID = strings.TrimSpace(r.Header.Get(ExtensionHeader))
		if extID == "" {
			apierr.Write(w, http.StatusForbidden, errors.New("the extension of this request is unknown"))
			return false, "", false
		}
		s.touchPlacementLease(extID, s.placementNow())
		return true, extID, true
	}
	return false, "", true
}

// placementRoutes registers the three tasks.schedule endpoints:
//   - GET  /api/v1/assistant/placement/pending
//   - POST /api/v1/assistant/placement/{todo_id}/assign
//   - POST /api/v1/assistant/placement/{todo_id}/defer
func (s *Service) placementRoutes() {
	s.handle("GET /api/v1/assistant/placement/pending", func(w http.ResponseWriter, r *http.Request) {
		fromExt, _, ok := s.checkPlacementCaller(w, r)
		if !ok {
			return
		}
		ctx := r.Context()
		if err := s.ensureTodosLoaded(ctx); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		now := s.placementNow()
		type pendingCandidate struct {
			planID string
			pt     PlanTask
		}
		var raw []pendingCandidate
		var todoIDs []string
		for _, p := range s.queueCandidatePlans(ctx) {
			if p.State != planStarted {
				continue
			}
			for _, pt := range p.Tasks {
				if pt.TodoID == "" || pt.Queue == nil || pt.Queue.Key != pendingPlacementQueueKey {
					continue
				}
				todo, exists := s.todoNow(pt.TodoID)
				if !exists || todo.DoneAt != nil || todo.Cancelled() {
					continue
				}
				if pt.Queue.Placement != nil && !pt.Queue.Placement.DeferUntil.IsZero() && pt.Queue.Placement.DeferUntil.After(now) {
					continue
				}
				raw = append(raw, pendingCandidate{planID: p.ID, pt: pt})
				todoIDs = append(todoIDs, pt.TodoID)
			}
		}
		var hidden map[string]bool
		if fromExt && len(todoIDs) > 0 {
			hidden = s.todosHidden(ctx, todoIDs, nil)
		}
		tasks := make([]assistantwire.PlacementPendingTask, 0, len(raw))
		for _, item := range raw {
			if hidden[item.pt.TodoID] {
				continue
			}
			q := item.pt.Queue
			pw := q.Placement
			ver, batchID, batchSize, batchIdx := 1, item.planID, 1, 0
			fallbackAt := q.NextAt
			lastReason := ""
			if pw != nil {
				if pw.Version > 0 {
					ver = pw.Version
				}
				if pw.BatchID != "" {
					batchID = pw.BatchID
				}
				if pw.BatchSize > 0 {
					batchSize = pw.BatchSize
				}
				batchIdx = pw.BatchIndex
				if !pw.FallbackAt.IsZero() {
					fallbackAt = pw.FallbackAt
				}
				lastReason = pw.LastReason
			}
			var constraints *assistantwire.PlacementConstraints
			if item.pt.PlacementConstraints != nil {
				cp := *item.pt.PlacementConstraints
				cp.Capabilities = slices.Clone(cp.Capabilities)
				cp.CandidateMachines = slices.Clone(cp.CandidateMachines)
				constraints = &cp
			}
			tasks = append(tasks, assistantwire.PlacementPendingTask{
				PlanID:               assistantwire.PlanID(item.planID),
				TodoID:               wireids.TodoID(item.pt.TodoID),
				Version:              ver,
				Role:                 assistantwire.PlacementTaskRole(item.pt.Role),
				Readonly:             item.pt.Readonly,
				Launcher:             item.pt.Launcher,
				Tier:                 item.pt.Tier,
				Host:                 item.pt.Host,
				CwdHint:              item.pt.Cwd,
				MachinePolicy:        item.pt.MachinePolicy,
				PlacementConstraints: constraints,
				BatchID:              assistantwire.BatchID(batchID),
				BatchSize:            batchSize,
				BatchIndex:           batchIdx,
				Attempt:              q.Attempt,
				QueuedAt:             q.QueuedAt,
				FallbackAt:           fallbackAt,
				LastReason:           lastReason,
			})
		}
		apierr.WriteJSON(w, map[string]any{
			"lease_ttl_seconds": int(placementLeaseTTL.Seconds()),
			"tasks":             tasks,
		})
	})

	s.handle("POST /api/v1/assistant/placement/{todo_id}/assign", func(w http.ResponseWriter, r *http.Request) {
		fromExt, extID, ok := s.checkPlacementCaller(w, r)
		if !ok {
			return
		}
		todoID := strings.TrimSpace(r.PathValue("todo_id"))
		if todoID == "" {
			apierr.Write(w, http.StatusBadRequest, errors.New("todo_id is required"))
			return
		}
		var in struct {
			Machine string `json:"machine"`
			Reason  string `json:"reason"`
			Version int    `json:"version"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, placementBodyLimit))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		in.Machine = strings.TrimSpace(in.Machine)
		in.Reason = strings.TrimSpace(in.Reason)
		if in.Machine == "" {
			apierr.Write(w, http.StatusBadRequest, errors.New("machine is required"))
			return
		}
		if in.Version <= 0 {
			apierr.Write(w, http.StatusBadRequest, errors.New("version must be a positive integer"))
			return
		}
		ctx := r.Context()
		if err := s.ensureTodosLoaded(ctx); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		if fromExt && s.todosHidden(ctx, []string{todoID}, nil)[todoID] {
			apierr.WriteCode(w, http.StatusNotFound, "todo_not_found", i18n.Err("assistant-todos.not_found"))
			return
		}
		todo, exists := s.todoNow(todoID)
		if !exists {
			apierr.WriteCode(w, http.StatusNotFound, "todo_not_found", i18n.Err("assistant-todos.not_found"))
			return
		}
		if todo.DoneAt != nil || todo.Cancelled() {
			writePlacementErr(w, &placementHTTPError{status: http.StatusConflict, code: "placement_conflict",
				err: errors.New("task is no longer open")})
			return
		}
		p, idx, pt, found := s.findPendingPlacementTask(ctx, todoID)
		if !found {
			writePlacementErr(w, &placementHTTPError{status: http.StatusNotFound, code: "placement_not_found",
				err: errors.New("pending placement task not found")})
			return
		}
		if pt.Queue == nil || pt.Queue.Key != pendingPlacementQueueKey {
			writePlacementErr(w, &placementHTTPError{status: http.StatusConflict, code: "placement_conflict",
				err:   errors.New("task is no longer waiting for placement"),
				extra: map[string]any{"machine": pt.Machine}})
			return
		}
		curVer := 1
		if pt.Queue.Placement != nil && pt.Queue.Placement.Version > 0 {
			curVer = pt.Queue.Placement.Version
		}
		if curVer != in.Version {
			writePlacementErr(w, &placementHTTPError{status: http.StatusConflict, code: "placement_version_mismatch",
				err:   fmt.Errorf("placement version mismatch: got %d, current is %d", in.Version, curVer),
				extra: map[string]any{"current_version": curVer}})
			return
		}
		auditReason := in.Reason
		if auditReason == "" {
			if fromExt {
				auditReason = "assigned by " + extID
			} else {
				auditReason = "assigned explicitly"
			}
		}
		res, err := s.assignPendingPlacement(ctx, p.ID, p.Conversation, idx, pt, in.Machine, in.Version, placementReasonExtension, auditReason, nil)
		if err != nil {
			writePlacementErr(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{
			"assigned":     true,
			"plan_id":      res.PlanID,
			"todo_id":      res.TodoID,
			"machine":      res.Machine,
			"cwd":          res.Cwd,
			"placement_id": res.PlacementID,
		})
	})

	s.handle("POST /api/v1/assistant/placement/{todo_id}/defer", func(w http.ResponseWriter, r *http.Request) {
		fromExt, _, ok := s.checkPlacementCaller(w, r)
		if !ok {
			return
		}
		todoID := strings.TrimSpace(r.PathValue("todo_id"))
		if todoID == "" {
			apierr.Write(w, http.StatusBadRequest, errors.New("todo_id is required"))
			return
		}
		var in struct {
			Reason  string    `json:"reason"`
			NextAt  time.Time `json:"next_at"`
			Version int       `json:"version"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, placementBodyLimit))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		in.Reason = strings.TrimSpace(in.Reason)
		if in.Reason == "" {
			apierr.Write(w, http.StatusBadRequest, errors.New("reason is required"))
			return
		}
		if in.Version <= 0 {
			apierr.Write(w, http.StatusBadRequest, errors.New("version must be a positive integer"))
			return
		}
		ctx := r.Context()
		if err := s.ensureTodosLoaded(ctx); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, errTodosUnavailable)
			return
		}
		if fromExt && s.todosHidden(ctx, []string{todoID}, nil)[todoID] {
			apierr.WriteCode(w, http.StatusNotFound, "todo_not_found", i18n.Err("assistant-todos.not_found"))
			return
		}
		todo, exists := s.todoNow(todoID)
		if !exists {
			apierr.WriteCode(w, http.StatusNotFound, "todo_not_found", i18n.Err("assistant-todos.not_found"))
			return
		}
		if todo.DoneAt != nil || todo.Cancelled() {
			writePlacementErr(w, &placementHTTPError{status: http.StatusConflict, code: "placement_conflict",
				err: errors.New("task is no longer open")})
			return
		}
		p, idx, pt, found := s.findPendingPlacementTask(ctx, todoID)
		if !found {
			writePlacementErr(w, &placementHTTPError{status: http.StatusNotFound, code: "placement_not_found",
				err: errors.New("pending placement task not found")})
			return
		}
		newVer, nextAt, err := s.deferPendingPlacement(ctx, p.ID, idx, pt.TodoID, in.Reason, in.NextAt, in.Version)
		if err != nil {
			writePlacementErr(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{
			"deferred": true,
			"plan_id":  p.ID,
			"todo_id":  pt.TodoID,
			"version":  newVer,
			"next_at":  nextAt,
		})
	})
}
