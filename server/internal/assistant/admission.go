package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The execution-agent admission gate. The hub machine once ran 40+ claude processes at load 160 and timed out, so an
// execution Agent can no longer start on a computer just because a plan named it: before every session.start for an
// execution Agent the hub checks the target's reported resources and how many Agents the task's direction already has
// running. The assistant's own conversations (main, leaders, scheduled) start through assistant.send and never pass
// this gate. Thresholds live in the assistant.admission setting; code defaults mirror the guard script the operators
// ran by hand. Fail-closed applies to a machine the resource layer DOES retain whose metrics are missing or stale;
// a computer it does not retain, or a hub without a devices manager, is skipped (nothing to read),
// and a direction that cannot be resolved skips only the direction line. A fresh sample missing one optional reading
// (a load average on Windows, a claude process count on an older node) skips just that condition, not the machine.

// admissionMessage builds an *i18n.Message; i18n.Err always returns one, so the gate can keep its typed reasons and
// render them in the request's language without re-looking the key up.
func admissionMessage(key string, args ...i18n.Args) *i18n.Message {
	return i18n.Err(key, args...).(*i18n.Message)
}

const admissionSettingsKey = "assistant.admission"

// Default thresholds. The hub's own computer (the one the assistant's settings route the main conversation to) gets
// the tighter memory line and its own process cap; every other computer gets the generic line. All are overridable in
// the setting, per machine, without naming any machine in code.
const (
	admissionLoadPerCore = 4.0
	// admissionDirectionCap is the code default every direction falls back to when neither direction_cap_by_direction
	// nor direction_cap_by_project names it. The counts are taken per supervising conversation, so the default must fit
	// the busiest charter direction (the main product line's charter is 8); narrower lines are configured per direction.
	admissionDirectionCap      = 8
	admissionMinMemoryBytes    = int64(2) * 1024 * 1024 * 1024        // 2 GiB
	admissionHubMinMemory      = int64(2.5 * float64(1024*1024*1024)) // 2.5 GiB
	admissionMaxClaudeProcs    = 20
	admissionHubMaxClaudeProcs = 20
	admissionSettingsFresh     = 5 * time.Second
	// admissionSampleFresh matches devices.metricsFresh (3 samples): an older sample means the node stopped reporting.
	admissionSampleFresh = 30 * time.Second
)

// AdmissionSettings is the assistant.admission setting. Pointer fields leave the code default in place when nil or
// out of bounds, so a half-filled setting can never zero a limit by accident.
type AdmissionSettings struct {
	// Enabled nil means on. Set it false to switch the whole gate off without redeploying.
	Enabled *bool `json:"enabled,omitempty"`
	// LoadPerCore refuses when load1 exceeds cores times this (4 by default).
	LoadPerCore *float64 `json:"load_per_core,omitempty"`
	// DirectionCap is the most running execution Agents one direction may have (8 by default). DirectionCapByDirection
	// overrides it for one supervising conversation id or the direction's displayed name, and DirectionCapByProject for
	// a knowledge-base project slug; the direction map wins over the project map.
	DirectionCap            *int           `json:"direction_cap,omitempty"`
	DirectionCapByDirection map[string]int `json:"direction_cap_by_direction,omitempty"`
	DirectionCapByProject   map[string]int `json:"direction_cap_by_project,omitempty"`
	// Generic machine lines. MaxClaudeProcs bounds sessions working a turn at once on one computer (the fleet's working
	// count); only a computer without a fleet snapshot falls back to its OS claude process count.
	MinMemoryBytes *int64 `json:"min_memory_bytes,omitempty"`
	MaxClaudeProcs *int   `json:"max_claude_procs,omitempty"`
	// HubMachine* override the generic lines for the hub's own computer.
	HubMinMemoryBytes *int64 `json:"hub_min_memory_bytes,omitempty"`
	HubMaxClaudeProcs *int   `json:"hub_max_claude_procs,omitempty"`
	// Machines overrides any machine's lines by its id.
	Machines map[string]MachineAdmission `json:"machines,omitempty"`
}

// MachineAdmission is one machine's line overrides.
type MachineAdmission struct {
	MinMemoryBytes *int64   `json:"min_memory_bytes,omitempty"`
	MaxClaudeProcs *int     `json:"max_claude_procs,omitempty"`
	LoadPerCore    *float64 `json:"load_per_core,omitempty"`
}

// admissionLimits is the resolved line for one start decision.
type admissionLimits struct {
	minMemory    int64
	maxProcs     int
	loadPerCore  float64
	directionCap int
}

// admissionBook keeps the setting cache and the in-flight claims that make check-then-start atomic: a start counts
// against its machine and direction from the moment it passes until its agent is linked into the todo (or the start
// fails), so neither concurrent admits nor the 10-second metrics gap can let two starts take the last slot.
type admissionBook struct {
	mu              sync.Mutex
	cached          *AdmissionSettings
	at              time.Time
	machineClaims   map[string]int
	directionClaims map[string]int
}

// admissionSettings reads the setting with the same short cache as the assistant's own settings; a storeless hub uses
// the code defaults.
func (s *Service) admissionSettings(ctx context.Context) AdmissionSettings {
	b := &s.admission
	b.mu.Lock()
	if b.cached != nil && time.Since(b.at) < admissionSettingsFresh {
		v := *b.cached
		b.mu.Unlock()
		return v
	}
	b.mu.Unlock()
	var v AdmissionSettings
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, admissionSettingsKey, &v); err != nil {
			s.logf("admission: settings: %v", err)
		}
	}
	b.mu.Lock()
	cp := v
	b.cached, b.at = &cp, time.Now()
	b.mu.Unlock()
	return v
}

func (s *Service) admissionEnabled(ctx context.Context) bool {
	v := s.admissionSettings(ctx)
	return v.Enabled == nil || *v.Enabled
}

func positiveOr64(v *int64, def int64) int64 {
	if v != nil && *v > 0 {
		return *v
	}
	return def
}

func positiveOrInt(v *int, def int) int {
	if v != nil && *v > 0 {
		return *v
	}
	return def
}

func positiveOrFloat(v *float64, def float64) float64 {
	if v != nil && *v > 0 {
		return *v
	}
	return def
}

// limitsFor resolves the lines for machineID and the starting task's direction and project.
func (s *Service) limitsFor(ctx context.Context, machineID, direction, project string, cfg AdmissionSettings) admissionLimits {
	hubMachine := s.settings(ctx).MachineID
	minMemory := positiveOr64(cfg.MinMemoryBytes, admissionMinMemoryBytes)
	maxProcs := positiveOrInt(cfg.MaxClaudeProcs, admissionMaxClaudeProcs)
	if machineID == hubMachine {
		minMemory = positiveOr64(cfg.HubMinMemoryBytes, admissionHubMinMemory)
		maxProcs = positiveOrInt(cfg.HubMaxClaudeProcs, admissionHubMaxClaudeProcs)
	}
	if over, ok := cfg.Machines[machineID]; ok {
		minMemory = positiveOr64(over.MinMemoryBytes, minMemory)
		maxProcs = positiveOrInt(over.MaxClaudeProcs, maxProcs)
	}
	directionCap := s.directionCap(ctx, direction, project, cfg)
	loadPerCore := positiveOrFloat(cfg.LoadPerCore, admissionLoadPerCore)
	if over, ok := cfg.Machines[machineID]; ok {
		loadPerCore = positiveOrFloat(over.LoadPerCore, loadPerCore)
	}
	return admissionLimits{minMemory: minMemory, maxProcs: maxProcs, loadPerCore: loadPerCore, directionCap: directionCap}
}

// directionCap resolves one start's direction line. The count is taken per supervising conversation, so the first and
// exact match is DirectionCapByDirection keyed by that conversation id; the same map also accepts the direction's
// displayed name for a hand-written setting. A task's knowledge-base project stays as the fallback the original gate
// shipped with, then the global DirectionCap, then the code default.
func (s *Service) directionCap(ctx context.Context, direction, project string, cfg AdmissionSettings) int {
	if direction != "" {
		if cap, ok := cfg.DirectionCapByDirection[direction]; ok && cap > 0 {
			return cap
		}
		if name, ok := s.directionDisplayName(ctx, direction); ok {
			if cap, ok := cfg.DirectionCapByDirection[name]; ok && cap > 0 {
				return cap
			}
		}
	}
	if cap, ok := cfg.DirectionCapByProject[project]; ok && cap > 0 {
		return cap
	}
	return positiveOrInt(cfg.DirectionCap, admissionDirectionCap)
}

// directionDisplayName is the displayed name of a current leader conversation (the directions tree's name), so a
// direction_cap_by_direction entry written with a name resolves to the same line as one written with the id. False
// for a conversation that is not a current leader (the main conversation): configure its line by id.
func (s *Service) directionDisplayName(ctx context.Context, id string) (string, bool) {
	_, names := s.currentLeaders(ctx, s.settings(ctx))
	name, ok := names[id]
	return name, ok && strings.TrimSpace(name) != ""
}

// admissionMachineFacts is the resource view the gate uses for one machine.
type admissionMachineFacts struct {
	cores     int
	load1     *float64 // nil: the node did not report a load average (e.g. Windows), so the load condition is skipped
	available int64
	// claudeProcs nil: the node did not report a count, so the process condition is skipped; replaced by the fleet's
	// working-session count when a snapshot exists
	claudeProcs *int
}

// machineFacts is what the gate reads for one machine. managed=false means the resource layer does not retain this
// computer at all (no fleet bus, or a machine outside it): the caller skips machine-level checks rather than inventing
// a reason. A managed computer whose metrics are missing or stale returns a message: it denies. A fresh sample that
// simply lacks a load average (Windows has none) is not a reason to deny: the load condition is skipped for it, the
// same way a missing claude process count skips the process condition.
func (s *Service) machineFacts(machineID string) (facts admissionMachineFacts, managed bool, why *i18n.Message) {
	if s.devs == nil {
		return admissionMachineFacts{}, false, nil // no fleet manager: the caller treats this as inert
	}
	m, sample, _ := s.devs.MachineResources(machineID)
	if m == nil {
		return admissionMachineFacts{}, false, nil // not retained by the resource layer: nothing to decide on
	}
	if sample == nil {
		return admissionMachineFacts{}, true, admissionMessage("assistant-tasks.admission_no_metrics")
	}
	if time.Since(time.Unix(sample.T, 0)) > admissionSampleFresh {
		return admissionMachineFacts{}, true, admissionMessage("assistant-tasks.admission_stale")
	}
	if m.System == nil || m.System.Cores <= 0 {
		return admissionMachineFacts{}, true, admissionMessage("assistant-tasks.admission_no_cores")
	}
	if sample.MemTotal <= 0 || sample.MemUsed < 0 || sample.MemUsed > sample.MemTotal {
		return admissionMachineFacts{}, true, admissionMessage("assistant-tasks.admission_memory_ambiguous")
	}
	return admissionMachineFacts{
		cores:       m.System.Cores,
		load1:       sample.Load, // nil on nodes without a load average, such as Windows: the load check skips it
		available:   sample.MemTotal - sample.MemUsed,
		claudeProcs: sample.ClaudeProcs,
	}, true, nil
}

// workingSessions counts a machine's sessions the fleet last saw working a turn. ok is false only when the fleet has no
// snapshot for the machine at all (the fleet bus is off, or it never reported a list): a reporting machine with an
// empty or all-idle list is ok with the true count, including zero. It reads the same snapshot setSessions keeps for
// the session lists, so it costs one short lock and no node RPC.
func (s *Service) workingSessions(machineID string) (count int, ok bool) {
	if s.devs == nil {
		return 0, false
	}
	machineID = s.devs.Canonical(machineID)
	s.fleet.mu.Lock()
	prev, has := s.fleet.prev[machineID]
	s.fleet.mu.Unlock()
	if !has {
		return 0, false
	}
	for _, p := range prev {
		if p.Status == proto.StatusWorking {
			count++
		}
	}
	return count, true
}

// machineViolation checks the machine-level lines for machineID against the claims held right now, WITHOUT taking a
// claim. It is the read-only view used when ranking recommendations and on the pre-confirmation agent_start check.
// nil means it passes, including when the resource layer does not retain the machine (nothing to read, nothing to deny).
func (s *Service) machineViolation(ctx context.Context, machineID string, lim admissionLimits) *i18n.Message {
	s.admission.mu.Lock()
	claims := s.admission.machineClaims[machineID]
	s.admission.mu.Unlock()
	return s.machineViolationWith(machineID, lim, claims)
}

// machineViolationWith applies the lines to facts plus an explicit claim count. The shared facts read happens outside
// the admission lock; only the claim count is shared state and is passed in by the caller that holds the lock.
func (s *Service) machineViolationWith(machineID string, lim admissionLimits, claims int) *i18n.Message {
	facts, managed, why := s.machineFacts(machineID)
	if !managed {
		return nil
	}
	if why != nil {
		return why
	}
	if facts.available < lim.minMemory {
		// Memory is the measured available bytes, not a reservation counter: several in-flight starts read the same
		// sample, so claims deliberately do not decrement it the way they do the process and direction caps.
		return admissionMessage("assistant-tasks.admission_memory", i18n.Args{
			"available": humanBytes(facts.available), "limit": humanBytes(lim.minMemory)})
	}
	// The line bounds agents actually working a turn, not OS processes: a session that finished, parks or stays idle
	// keeps its claude process alive for hours, and counting those fills the machine while nothing runs. The fleet's
	// last session snapshot says how many sessions are working; only when it has no snapshot for this machine (the
	// fleet bus is off or the machine never reported a list) does the node's process count stand in.
	if working, has := s.workingSessions(machineID); has {
		facts.claudeProcs = &working
	}
	if facts.claudeProcs != nil && *facts.claudeProcs+claims >= lim.maxProcs {
		return admissionMessage("assistant-tasks.admission_procs", i18n.Args{
			"count": *facts.claudeProcs + claims, "limit": lim.maxProcs})
	}
	if facts.load1 != nil && *facts.load1 > float64(facts.cores)*lim.loadPerCore {
		return admissionMessage("assistant-tasks.admission_load", i18n.Args{
			"load": fmt.Sprintf("%.1f", *facts.load1), "cores": facts.cores, "multiplier": lim.loadPerCore})
	}
	return nil
}

// recommendMachine names the reachable computer best able to take work now: fresh metrics, every machine line
// passing, lowest load1-per-core (then most free memory). except and the optional suitable filter narrow the field.
func (s *Service) recommendMachine(ctx context.Context, except string, suitable func(Device) bool) (Device, bool) {
	if s.devs == nil {
		return Device{}, false
	}
	cfg := s.admissionSettings(ctx)
	type candidate struct {
		d        Device
		loadRate float64
		free     int64
	}
	var candidates []candidate
	for _, d := range s.devices(ctx) {
		if d.ID == except || !d.Reachable {
			continue
		}
		if suitable != nil && !suitable(d) {
			continue
		}
		facts, managed, why := s.machineFacts(d.ID)
		if !managed || why != nil {
			continue
		}
		if s.machineViolation(ctx, d.ID, s.limitsFor(ctx, d.ID, "", "", cfg)) != nil {
			continue
		}
		// A machine without a load average cannot show measured pressure: rank it at a zero rate so it stays eligible
		// for a reroute, and let the free-memory tiebreak order it against machines that do report a load.
		loadRate := 0.0
		if facts.load1 != nil {
			loadRate = *facts.load1 / float64(facts.cores)
		}
		candidates = append(candidates, candidate{d: d, loadRate: loadRate, free: facts.available})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].loadRate != candidates[j].loadRate {
			return candidates[i].loadRate < candidates[j].loadRate
		}
		return candidates[i].free > candidates[j].free
	})
	if len(candidates) == 0 {
		return Device{}, false
	}
	return candidates[0].d, true
}

// admitTaskStart is the gate for startTaskAgent. A fixed task that fails is refused. A flexible task is moved to the
// recommended machine when one can host its directory (its plan row records the reroute); when no machine can, it is
// refused too. The direction cap cannot be fixed by moving machines, so it refuses either policy. The returned release
// is idempotent and must run once the agent is linked into its todo or the start has failed for good — not when the
// session.start RPC returns — so the slot stays counted through the metrics/ledger gap.
func (s *Service) admitTaskStart(ctx context.Context, t *PlanTask) (func(), error) {
	if s.devs == nil || !s.admissionEnabled(ctx) {
		return func() {}, nil
	}
	cfg := s.admissionSettings(ctx)
	direction := s.taskDirection(ctx, t)
	lim := s.limitsFor(ctx, t.Machine, direction, t.Project, cfg)
	if violation := s.machineViolation(ctx, t.Machine, lim); violation != nil {
		if t.MachinePolicy != machineFlexible {
			return nil, s.admissionRefused(ctx, t.Machine, direction, violation)
		}
		alt, ok := s.rerouteTarget(ctx, t)
		if !ok {
			return nil, s.admissionRefused(ctx, t.Machine, direction, violation)
		}
		from, _ := s.machineByName(ctx, t.Machine)
		s.reroutePlanTask(ctx, t, from, alt, violation.Text(s.lang(ctx)))
		lim = s.limitsFor(ctx, t.Machine, direction, t.Project, cfg)
	}
	// Atomic decision: evaluate against the current claims and reserve the slot in one critical section, so two
	// concurrent starts cannot both observe room and both take the last slot.
	release, violation := s.claimIfAllowed(ctx, t.Machine, direction, lim)
	if violation != nil {
		return nil, s.admissionRefused(ctx, t.Machine, direction, violation)
	}
	return release, nil
}

// rerouteTarget finds a reachable machine that passes the gate and actually has the task's directory (a leader never
// lands on a work-trust computer), mirroring healthyPlace's constraints.
func (s *Service) rerouteTarget(ctx context.Context, t *PlanTask) (Device, bool) {
	from, ok := s.machineByName(ctx, t.Machine)
	if !ok {
		return Device{}, false
	}
	return s.recommendMachine(ctx, t.Machine, func(alt Device) bool {
		if t.Role == roleLeader && s.isWorkMachine(alt.ID) {
			return false
		}
		// A reroute must land where the task's launcher can actually run, or fitTaskLauncher later turns a detour into a
		// hard failure on a machine the plan never named.
		if !s.launcherOn(ctx, alt.ID, s.startLauncher(ctx, t.Launcher)) {
			return false
		}
		return s.taskDirectoryExists(ctx, alt.ID, rehome(t.Cwd, from, alt))
	})
}

// reroutePlanTask moves t to alt and records the trail, the same fields healthyPlace writes.
func (s *Service) reroutePlanTask(ctx context.Context, t *PlanTask, from, alt Device, reason string) {
	note := i18n.T(s.lang(ctx), "assistant-tasks.admission_reroute", i18n.Args{
		"from": firstNonEmpty(from.Name, from.ID), "to": firstNonEmpty(alt.Name, alt.ID), "reason": reason})
	if t.Reroute == nil {
		t.Reroute = &PlanReroute{FromMachine: firstNonEmpty(from.Name, from.ID)}
	}
	t.Reroute.ToMachine = firstNonEmpty(alt.Name, alt.ID)
	t.Reroute.Reason = note
	t.Machine, t.Cwd = alt.ID, rehome(t.Cwd, from, alt)
	t.noteWhy(note)
}

// admissionDeniedError marks a refusal made by the admission gate (machine resources or direction capacity), as
// against a launcher or shell failure. A plan task that hits one is queued and retried by the admission queue loop
// instead of being failed (task_queue.go, #6909); an agent_start tool call still returns it to the caller directly.
type admissionDeniedError struct {
	// msg is the user-facing admission_refused sentence for the request's language.
	msg *i18n.Message
	// reason is the specific gate line (load, memory, process or direction cap) the task is waiting on.
	reason *i18n.Message
}

func (e *admissionDeniedError) Error() string { return e.msg.Error() }
func (e *admissionDeniedError) Unwrap() error { return e.msg }

// isAdmissionDenied reports whether err is (or wraps) an admission-gate refusal.
func isAdmissionDenied(err error) bool {
	var denied *admissionDeniedError
	return errors.As(err, &denied)
}

// admissionRefused words a fixed-policy denial, naming the least-loaded alternative when one exists, and records one
// structured log line so a blocked dispatch is visible in the hub log without reading the todo ledger. The line carries
// only the machine id, the direction id (empty for an agent_start check), the reason's stable i18n key and its numeric
// args; the user-facing sentence stays in the returned error.
func (s *Service) admissionRefused(ctx context.Context, machineID, direction string, reason *i18n.Message) error {
	if reason != nil {
		if direction != "" {
			s.logf("admission: refused: machine=%s direction=%s reason=%s args=%v", machineID, direction, reason.Key, reason.Args)
		} else {
			s.logf("admission: refused: machine=%s reason=%s args=%v", machineID, reason.Key, reason.Args)
		}
	}
	machine := machineID
	if d, ok := s.device(ctx, machineID); ok {
		machine = firstNonEmpty(d.Name, machineID)
	}
	recommend := ""
	if alt, ok := s.recommendMachine(ctx, machineID, nil); ok {
		recommend = i18n.T(s.lang(ctx), "assistant-tasks.admission_recommend", i18n.Args{
			"machine": firstNonEmpty(alt.Name, alt.ID)})
	}
	return &admissionDeniedError{
		msg: admissionMessage("assistant-tasks.admission_refused", i18n.Args{
			"machine": machine, "reason": reason.Text(s.lang(ctx)), "recommend": recommend}),
		reason: reason,
	}
}

// checkAgentStart is the agent_start tool's machine-level check without a claim: run it before the phone confirmation
// card so the user is never asked to approve a start that will be refused.
func (s *Service) checkAgentStart(ctx context.Context, machineID string) error {
	if s.devs == nil || !s.admissionEnabled(ctx) {
		return nil
	}
	cfg := s.admissionSettings(ctx)
	if violation := s.machineViolation(ctx, machineID, s.limitsFor(ctx, machineID, "", "", cfg)); violation != nil {
		return s.admissionRefused(ctx, machineID, "", violation)
	}
	return nil
}

// admitAgentStart re-checks the (possibly rerouted) target and atomically reserves its machine slot. The release runs
// once the session.start attempt ends: an agent_start tool call is not linked to a todo, so there is no later ledger
// to carry the count.
func (s *Service) admitAgentStart(ctx context.Context, machineID string) (func(), error) {
	if s.devs == nil || !s.admissionEnabled(ctx) {
		return func() {}, nil
	}
	cfg := s.admissionSettings(ctx)
	lim := s.limitsFor(ctx, machineID, "", "", cfg)
	release, violation := s.claimIfAllowed(ctx, machineID, "", lim)
	if violation != nil {
		return nil, s.admissionRefused(ctx, machineID, "", violation)
	}
	return release, nil
}

// claimIfAllowed evaluates every line against the current claims and, only when they all pass, reserves one slot on
// the machine and direction in the SAME critical section. This is the atomic check-and-claim: no two concurrent starts
// can both read the pre-admit counts and both take the final slot. The machine facts come from the devices manager's
// in-memory snapshots (no node RPC or lock cycle back into this package), so reading them under admission.mu cannot
// stall; only the claim maps are mutable shared state, and every mutation is here.
func (s *Service) claimIfAllowed(ctx context.Context, machineID, direction string, lim admissionLimits) (func(), *i18n.Message) {
	release := func() {}
	s.admission.mu.Lock()
	defer s.admission.mu.Unlock()
	if violation := s.machineViolationWith(machineID, lim, s.admission.machineClaims[machineID]); violation != nil {
		return release, violation
	}
	if direction != "" {
		// The ledger count is read under admission.mu (directionCountLocked takes the todo read lock, never a write
		// lock; no caller holds a todo lock when entering the gate), so a concurrent todo change can at most lag by one
		// update, which the held claim covers for the start in progress.
		if running := s.directionCountLocked(direction) + s.admission.directionClaims[direction]; running >= lim.directionCap {
			return release, admissionMessage("assistant-tasks.admission_direction_full", i18n.Args{
				"running": running, "limit": lim.directionCap})
		}
	}
	if s.admission.machineClaims == nil {
		s.admission.machineClaims = map[string]int{}
	}
	if s.admission.directionClaims == nil {
		s.admission.directionClaims = map[string]int{}
	}
	s.admission.machineClaims[machineID]++
	if direction != "" {
		s.admission.directionClaims[direction]++
	}
	var done bool
	return func() {
		s.admission.mu.Lock()
		defer s.admission.mu.Unlock()
		if done { // idempotent: link and failure paths can both run
			return
		}
		done = true
		if s.admission.machineClaims[machineID] > 0 {
			s.admission.machineClaims[machineID]--
		}
		if direction != "" && s.admission.directionClaims[direction] > 0 {
			s.admission.directionClaims[direction]--
		}
	}, nil
}

// taskDirection resolves the direction (supervising conversation) a plan task's running agents count against: its
// todo's stored ledger, else the plan task's own supervisor. "" means the direction cannot be established and the
// direction cap is not evaluated for this start.
func (s *Service) taskDirection(ctx context.Context, t *PlanTask) string {
	if t.TodoID != "" {
		if todo, ok := s.todoNow(t.TodoID); ok && todo.Supervision != nil && todo.Supervision.Supervisor != "" {
			return todo.Supervision.Supervisor
		}
	}
	return t.Supervisor
}

// directionCountLocked is the ledger part of a direction's in-flight count: open tasks of the direction whose linked
// execution Agent is actually working a turn in the fleet's latest session snapshot. A task that is parked, waiting
// for the owner's decision, or finished and held short of live keeps its agent linked but idle, so it occupies no
// direction slot, and neither does an agent whose session is not working. It only reads the todo book (its RLock),
// never the admission lock, so it is safe to call while admission.mu is held. The fleet snapshot is read after the
// todo lock is released, so this never nests the fleet lock under the todo lock.
func (s *Service) directionCountLocked(direction string) int {
	type linkedAgent struct{ machine, session string }
	var candidates []linkedAgent
	s.todos.mu.RLock()
	for _, t := range s.todos.todos {
		if t.DoneAt != nil || t.Cancelled() || t.Supervision == nil || t.Supervision.Supervisor != direction {
			continue
		}
		if directionIdle(t) {
			continue
		}
		for _, a := range t.Agents {
			if a.active() {
				candidates = append(candidates, linkedAgent{a.MachineID, a.SessionID})
				break // one slot per task, however many agents it links
			}
		}
	}
	s.todos.mu.RUnlock()
	n := 0
	for _, a := range candidates {
		if s.seenWorking(a.machine, a.session) {
			n++
		}
	}
	return n
}

// directionIdle says an open task's linked agent is not consuming a direction slot: parked tasks, tasks waiting for
// the owner to decide (a needs_user review or the done judge's owner wait), and tasks whose work is finished and held
// short of live (stopped at a PR, merged but not deployed, or deployed off by default).
func directionIdle(t *Todo) bool {
	if v := t.Supervision; v != nil {
		if v.parking() != nil || v.State == reviewNeedsUser || v.Waits != nil {
			return true
		}
	}
	return t.Delivery != nil && t.Delivery.held()
}

// directionInFlight counts a direction's linked active agents plus starts admitted but not linked yet. Used by tests
// and diagnostics; the hot admit path calls directionCountLocked directly while holding the admission lock.
func (s *Service) directionInFlight(ctx context.Context, direction string) int {
	s.admission.mu.Lock()
	claims := s.admission.directionClaims[direction]
	s.admission.mu.Unlock()
	return s.directionCountLocked(direction) + claims
}

// humanBytes renders a byte size with binary units for the denial sentences; units are language-neutral.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, symbol := float64(n), ""
	for _, s := range []string{"KiB", "MiB", "GiB", "TiB"} {
		value /= unit
		symbol = s
		if value < unit {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", value, symbol)
}

// ---- persona_settings: read and change assistant.admission with the phone approval card ----

// getAdmission renders the whole admission setting for persona_settings (the code defaults fill every nil field, so
// the reader always sees the effective lines).
func (s *Service) getAdmission(ctx context.Context) (string, error) {
	raw, err := json.Marshal(s.admissionSettings(ctx))
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// setAdmission replaces the admission setting after the user approves the change card. A half-filled document is
// valid: every pointer field nil means "keep the code default", but an explicitly non-positive line is refused.
func (s *Service) setAdmission(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var in AdmissionSettings
	if err := json.Unmarshal(v, &in); err != nil {
		return nil, fmt.Errorf("value must be an AdmissionSettings JSON object: %v", err)
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	before, _ := json.Marshal(s.admissionSettings(ctx))
	after, _ := json.Marshal(in)
	if bytes.Equal(before, after) {
		return &ownChange{summary: "assistant.admission did not change"}, nil
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_admission", i18n.Args{
			"before": clipped(l, string(before)), "after": clipped(l, string(after))})
	},
		apply: func(ctx context.Context) (string, error) {
			if err := s.putSetting(ctx, admissionSettingsKey, &in); err != nil {
				return "", err
			}
			// Drop the cache so the next gate decision reads the new lines (the 5-second freshness window cannot hold
			// an old setting past an approved change).
			s.admission.mu.Lock()
			s.admission.cached, s.admission.at = nil, time.Time{}
			s.admission.mu.Unlock()
			return "assistant.admission updated; the next execution-agent start uses the new lines.", nil
		}}, nil
}

// validate refuses an explicitly non-positive line rather than letting positiveOr* silently ignore it, and keeps every
// map key non-empty so a setting cannot carry dead entries.
func (cfg AdmissionSettings) validate() error {
	bad := func(field string) error { return fmt.Errorf("%s must be a positive number when set", field) }
	if cfg.LoadPerCore != nil && *cfg.LoadPerCore <= 0 {
		return bad("load_per_core")
	}
	if cfg.DirectionCap != nil && *cfg.DirectionCap <= 0 {
		return bad("direction_cap")
	}
	if cfg.MinMemoryBytes != nil && *cfg.MinMemoryBytes <= 0 {
		return bad("min_memory_bytes")
	}
	if cfg.MaxClaudeProcs != nil && *cfg.MaxClaudeProcs <= 0 {
		return bad("max_claude_procs")
	}
	if cfg.HubMinMemoryBytes != nil && *cfg.HubMinMemoryBytes <= 0 {
		return bad("hub_min_memory_bytes")
	}
	if cfg.HubMaxClaudeProcs != nil && *cfg.HubMaxClaudeProcs <= 0 {
		return bad("hub_max_claude_procs")
	}
	for k, cap := range cfg.DirectionCapByDirection {
		if strings.TrimSpace(k) == "" || cap <= 0 {
			return fmt.Errorf("direction_cap_by_direction entries need a non-empty direction id or name and a positive cap")
		}
	}
	for k, cap := range cfg.DirectionCapByProject {
		if strings.TrimSpace(k) == "" || cap <= 0 {
			return fmt.Errorf("direction_cap_by_project entries need a non-empty project slug and a positive cap")
		}
	}
	for id, m := range cfg.Machines {
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("machines entries need a machine id")
		}
		if m.MinMemoryBytes != nil && *m.MinMemoryBytes <= 0 {
			return bad("machines.min_memory_bytes")
		}
		if m.MaxClaudeProcs != nil && *m.MaxClaudeProcs <= 0 {
			return bad("machines.max_claude_procs")
		}
		if m.LoadPerCore != nil && *m.LoadPerCore <= 0 {
			return bad("machines.load_per_core")
		}
	}
	return nil
}
