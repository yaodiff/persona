package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// admissionHub builds a storeless service on two reachable machines: "busy" (over every line) and "idle" (healthy),
// with a resourceDevs feeding crafted metrics. The admission cache is pinned so tests never depend on a setting read.
func admissionHub(t *testing.T) (*Service, *fakeHub, *resourceDevs) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.devices = []Device{
		{ID: "busy", Name: "Busy", Home: "/home/me", Online: true, Reachable: true},
		{ID: "idle", Name: "Idle", Home: "/home/me", Online: true, Reachable: true},
	}
	// The hub fake only answers node RPCs for machines in its session table; fs.stat then confirms any directory,
	// which is what makes a flexible reroute eligible.
	f.sessions["busy"], f.sessions["idle"] = nil, nil
	// The tasks name launcher "work": every machine must offer it for a reroute to be eligible (launcherOn).
	f.launcherList = []proto.Launcher{{Name: "work"}}
	s := newStorelessService(t, f)
	d := &resourceDevs{fleetDevs: newFleetDevs(),
		machines: map[string]*proto.Machine{}, samples: map[string]*proto.MetricSample{}, versions: map[string]*bool{}}
	d.add("busy", "Busy")
	d.add("idle", "Idle")
	s.devs = d
	// Pin the code defaults explicitly so every test sees the documented lines, and start from a clean claim book so
	// tests never inherit another's in-flight reservations.
	on := true
	s.admission.cached = &AdmissionSettings{Enabled: &on}
	s.admission.at = time.Now()
	s.admission.machineClaims = map[string]int{}
	s.admission.directionClaims = map[string]int{}
	return s, f, d
}

func admissionSample(load float64, memTotal, memUsed, procs int64) *proto.MetricSample {
	p := int(procs)
	return &proto.MetricSample{
		T: time.Now().Unix(), Load: &load,
		MemTotal: memTotal, MemUsed: memUsed,
		ClaudeProcs: &p,
	}
}

func admissionMachine(cores int) *proto.Machine {
	return &proto.Machine{System: &proto.System{Cores: cores}}
}

func admissionPlanTask(machine, policy string) *PlanTask {
	return &PlanTask{
		Title: "Ship it", Prompt: "do the work", Machine: machine, Cwd: "/home/me/code/app",
		MachinePolicy: policy, Launcher: "work", TodoID: "todo-1", Supervisor: "dir-1",
	}
}

// healthyIdle gives the idle machine 8 cores, light load, 10 GiB free and two claude processes.
func healthyIdle(d *resourceDevs) {
	d.machines["idle"] = admissionMachine(8)
	// 16 GiB total, 6 GiB used: 10 GiB available.
	d.samples["idle"] = admissionSample(0.5, 16*1024*1024*1024, 6*1024*1024*1024, 2)
}

// 1. Memory below the line refuses the start, on either policy.
func TestAdmissionMemoryDenied(t *testing.T) {
	s, f, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	// 16 GiB total, 15 GiB used: only 1 GiB free, below the 2 GiB line; load and procs otherwise fine.
	d.samples["busy"] = admissionSample(1.0, 16*1024*1024*1024, 15*1024*1024*1024, 2)
	ctx := context.Background()

	task := admissionPlanTask("busy", machineFixed)
	if _, err := s.admitTaskStart(ctx, task); err == nil {
		t.Fatal("low memory on a fixed task was admitted")
	} else if msg := err.Error(); !strings.Contains(msg, admissionText(s, "admission_memory", i18n.Args{
		"available": humanBytes(1024 * 1024 * 1024), "limit": humanBytes(2 * 1024 * 1024 * 1024)})) {
		t.Fatalf("want a memory refusal, got %q", msg)
	}
	if task.Machine != "busy" {
		t.Fatalf("a fixed task must not move: %+v", task)
	}
	if starts := taskStartMachines(f); len(starts) != 0 {
		t.Fatalf("a refused start reached a node: %v", starts)
	}
}

// 2. The claude process cap refuses when the node's reported count plus hub claims reaches the limit.
func TestAdmissionProcsDenied(t *testing.T) {
	s, f, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	// Load and memory healthy, but 20 claude processes already run (the default cap).
	d.samples["busy"] = admissionSample(1.0, 16*1024*1024*1024, 2*1024*1024*1024, 20)
	ctx := context.Background()

	_, err := s.admitTaskStart(ctx, admissionPlanTask("busy", machineFixed))
	if err == nil || !strings.Contains(err.Error(), admissionText(s, "admission_procs", i18n.Args{
		"count": 20, "limit": 20})) {
		t.Fatalf("want a process-cap refusal, got %v", err)
	}
	if starts := taskStartMachines(f); len(starts) != 0 {
		t.Fatalf("a refused start reached a node: %v", starts)
	}

	// A node that cannot count processes (nil field) skips the process condition rather than reading zero.
	sample := d.samples["busy"]
	sample.ClaudeProcs = nil
	if _, err := s.admitTaskStart(ctx, admissionPlanTask("busy", machineFixed)); err != nil {
		t.Fatalf("missing process telemetry should skip the process cap, got %v", err)
	}
}

// 3. A direction already at its cap is refused even though the machine is healthy.
func TestAdmissionDirectionFull(t *testing.T) {
	s, f, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	d.samples["busy"] = admissionSample(0.8, 16*1024*1024*1024, 2*1024*1024*1024, 1)
	ctx := context.Background()
	if err := s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	// Eight open tasks of dir-1, each with an agent whose session is working a turn: the default cap is 8.
	s.todos.mu.Lock()
	for i := 0; i < 8; i++ {
		id := "dir-task-" + string(rune('a'+i))
		s.todos.todos[id] = &Todo{
			ID: id, Supervision: &Supervision{Supervisor: "dir-1"},
			Agents: []TodoAgent{{MachineID: "busy", SessionID: "s" + string(rune('a'+i))}},
		}
	}
	s.todos.mu.Unlock()
	admissionMarkWorking(s, "busy", "sa", "sb", "sc", "sd", "se", "sf", "sg", "sh")

	_, err := s.admitTaskStart(ctx, admissionPlanTask("busy", machineFlexible))
	if err == nil || !strings.Contains(err.Error(), admissionText(s, "admission_direction_full", i18n.Args{
		"running": 8, "limit": 8})) {
		t.Fatalf("want a direction-full refusal, got %v", err)
	}
	if starts := taskStartMachines(f); len(starts) != 0 {
		t.Fatalf("a refused start reached a node: %v", starts)
	}
}

// 4. A flexible task on the busy machine is moved to the least-loaded healthy one, with the reroute trail recorded.
func TestAdmissionFlexibleReroutes(t *testing.T) {
	s, f, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	d.samples["busy"] = admissionSample(40.0, 16*1024*1024*1024, 15*1024*1024*1024, 2) // load 40 > 8×4
	ctx := context.Background()

	task := admissionPlanTask("busy", machineFlexible)
	release, err := s.admitTaskStart(ctx, task)
	if err != nil {
		t.Fatalf("flexible task should reroute, not fail: %v", err)
	}
	defer release()
	if task.Machine != "idle" {
		t.Fatalf("flexible task was not moved to the idle machine: %+v", task)
	}
	if task.Reroute == nil || task.Reroute.FromMachine != "Busy" || task.Reroute.ToMachine != "Idle" {
		t.Fatalf("reroute trail not recorded: %+v", task.Reroute)
	}
	if got := s.directionInFlight(ctx, "dir-1"); got != 1 { // the held claim counts
		t.Fatalf("direction claim not held: %d", got)
	}
	release()
	if got := s.directionInFlight(ctx, "dir-1"); got != 0 {
		t.Fatalf("direction claim not released: %d", got)
	}
	if starts := taskStartMachines(f); len(starts) != 0 { // the gate itself never starts anything
		t.Fatalf("the gate dispatched a start: %v", starts)
	}
}

// 5. A fixed task is refused with a clear reason and the recommended alternative in the error.
func TestAdmissionFixedRefused(t *testing.T) {
	s, _, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	d.samples["busy"] = admissionSample(40.0, 16*1024*1024*1024, 15*1024*1024*1024, 2)
	ctx := context.Background()

	task := admissionPlanTask("busy", machineFixed)
	_, err := s.admitTaskStart(ctx, task)
	if err == nil {
		t.Fatal("fixed task on an overloaded machine was admitted")
	}
	msg := err.Error()
	// The wrapper carries the concrete machine-level reason (here low memory) and names the recommended machine.
	if !strings.Contains(msg, admissionText(s, "admission_memory", i18n.Args{
		"available": humanBytes(1024 * 1024 * 1024), "limit": humanBytes(2 * 1024 * 1024 * 1024)})) {
		t.Fatalf("refusal lacks the concrete reason: %q", msg)
	}
	if !strings.Contains(msg, "Busy") || !strings.Contains(msg, "Idle") {
		t.Fatalf("refusal does not name the refused and recommended machines: %q", msg)
	}
	if task.Machine != "busy" {
		t.Fatalf("a fixed task must stay on its machine: %+v", task)
	}
}

// 6. The assistant's own conversations do not pass through the gate: with every machine over its lines, an assistant
// conversation still starts (assistant.send), and no admission claim is taken.
func TestAdmissionDoesNotBlockAssistantConversation(t *testing.T) {
	s, f, d := admissionHub(t)
	// Both machines over every line.
	d.machines["busy"] = admissionMachine(8)
	d.samples["busy"] = admissionSample(99.0, 16*1024*1024*1024, 15*1024*1024*1024, 40)
	d.machines["idle"] = admissionMachine(8)
	d.samples["idle"] = admissionSample(99.0, 16*1024*1024*1024, 15*1024*1024*1024, 40)
	ctx := context.Background()

	// The gate itself refuses an execution Agent on those machines.
	if _, err := s.admitTaskStart(ctx, admissionPlanTask("busy", machineFixed)); err == nil {
		t.Fatal("the execution-agent gate did not refuse the overloaded machine")
	}
	// Yet the assistant conversation starts through assistant.send (the assistant machine is not a fleet node here,
	// so the hub fake records the send).
	before := len(f.sends)
	if _, err := s.startConversation(ctx, startParams{Text: "hello", Origin: OriginSpawned}); err != nil {
		t.Fatalf("assistant conversation blocked by the admission gate: %v", err)
	}
	if len(f.sends) != before+1 {
		t.Fatalf("assistant.send was not issued: %v", f.sends)
	}
	// No execution-agent session.start was taken on either fleet machine.
	for _, id := range []string{"busy", "idle"} {
		if starts := d.callsTo(id, DeviceSessionStart); len(starts) != 0 {
			t.Fatalf("an assistant conversation must never take the session.start path: %v on %s", starts, id)
		}
	}
	s.admission.mu.Lock()
	claims := len(s.admission.machineClaims) + len(s.admission.directionClaims)
	s.admission.mu.Unlock()
	if claims != 0 {
		t.Fatalf("the assistant conversation took admission claims: %d", claims)
	}
}

// Fail-closed: a managed machine with no sample at all, or a stale sample, refuses rather than being read as healthy.
func TestAdmissionFailClosedOnMissingOrStaleMetrics(t *testing.T) {
	s, _, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	ctx := context.Background()

	// No sample at all.
	if _, err := s.admitTaskStart(ctx, admissionPlanTask("busy", machineFixed)); err == nil ||
		!strings.Contains(err.Error(), admissionText(s, "admission_no_metrics")) {
		t.Fatalf("missing sample must refuse: %v", err)
	}
	// A stale sample.
	stale := admissionSample(0.2, 16*1024*1024*1024, 2*1024*1024*1024, 1)
	stale.T = time.Now().Add(-time.Minute).Unix()
	d.samples["busy"] = stale
	if _, err := s.admitTaskStart(ctx, admissionPlanTask("busy", machineFixed)); err == nil ||
		!strings.Contains(err.Error(), admissionText(s, "admission_stale")) {
		t.Fatalf("stale sample must refuse: %v", err)
	}
}

// A fresh sample that simply has no load average (Windows nodes never report one) skips only the load condition:
// the start is admitted, while the memory and process lines still apply and a reported overload still denies.
func TestAdmissionMissingLoadSkipsLoadCondition(t *testing.T) {
	s, _, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	ctx := context.Background()

	// Fresh sample without a load reading, otherwise healthy: admitted (the Windows case from #6907).
	noLoad := admissionSample(0.2, 16*1024*1024*1024, 2*1024*1024*1024, 1)
	noLoad.Load = nil
	d.samples["busy"] = noLoad
	release, err := s.admitTaskStart(ctx, admissionPlanTask("busy", machineFixed))
	if err != nil {
		t.Fatalf("a missing load reading must skip the load condition, got %v", err)
	}
	release()

	// The memory line still applies with no load reading.
	noLoadMem := admissionSample(0.2, 16*1024*1024*1024, 15*1024*1024*1024, 1)
	noLoadMem.Load = nil
	d.samples["busy"] = noLoadMem
	if _, err := s.admitTaskStart(ctx, admissionPlanTask("busy", machineFixed)); err == nil ||
		!strings.Contains(err.Error(), admissionText(s, "admission_memory", i18n.Args{
			"available": humanBytes(1024 * 1024 * 1024), "limit": humanBytes(2 * 1024 * 1024 * 1024)})) {
		t.Fatalf("low memory must still refuse with a missing load reading, got %v", err)
	}

	// The process-cap line still applies with no load reading.
	noLoadProcs := admissionSample(0.2, 16*1024*1024*1024, 2*1024*1024*1024, 20)
	noLoadProcs.Load = nil
	d.samples["busy"] = noLoadProcs
	if _, err := s.admitTaskStart(ctx, admissionPlanTask("busy", machineFixed)); err == nil ||
		!strings.Contains(err.Error(), admissionText(s, "admission_procs", i18n.Args{
			"count": 20, "limit": 20})) {
		t.Fatalf("the process cap must still refuse with a missing load reading, got %v", err)
	}

	// When the node does report a load, an overload still denies: skipping only applies to a missing reading.
	d.samples["busy"] = admissionSample(99.0, 16*1024*1024*1024, 2*1024*1024*1024, 1)
	if _, err := s.admitTaskStart(ctx, admissionPlanTask("busy", machineFixed)); err == nil ||
		!strings.Contains(err.Error(), admissionText(s, "admission_load", i18n.Args{
			"load": "99.0", "cores": 8, "multiplier": 4.0})) {
		t.Fatalf("a reported overload must refuse, got %v", err)
	}
}

// A machine that reports no load average stays eligible for a reroute recommendation.
func TestAdmissionRecommendIncludesMissingLoadMachine(t *testing.T) {
	s, _, d := admissionHub(t)
	// The usual idle machine is over its load line, so only the no-load machine can host the work.
	d.machines["idle"] = admissionMachine(8)
	d.samples["idle"] = admissionSample(99.0, 16*1024*1024*1024, 2*1024*1024*1024, 1)
	d.machines["busy"] = admissionMachine(8)
	windowsLike := admissionSample(0, 16*1024*1024*1024, 2*1024*1024*1024, 1)
	windowsLike.Load = nil
	d.samples["busy"] = windowsLike
	ctx := context.Background()

	got, ok := s.recommendMachine(ctx, "calm", nil)
	if !ok {
		t.Fatal("the no-load machine must be recommendable")
	}
	if got.ID != "busy" {
		t.Fatalf("recommendation = %q, want the no-load busy machine", got.ID)
	}
}

// The gate stays inert when no devices manager is attached (storeless hubs without a fleet bus).
func TestAdmissionInertWithoutDevices(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newStorelessService(t, f)
	s.devs = nil
	if _, err := s.admitTaskStart(context.Background(), admissionPlanTask("busy", machineFixed)); err != nil {
		t.Fatalf("the gate must stay inert without a devices manager: %v", err)
	}
}

// Disabling the setting switches the whole gate off, even on an overloaded machine.
func TestAdmissionDisabledSettingBypasses(t *testing.T) {
	s, _, d := admissionHub(t)
	d.machines["busy"] = admissionMachine(8)
	d.samples["busy"] = admissionSample(99.0, 16*1024*1024*1024, 15*1024*1024*1024, 40)
	off := false
	s.admission.cached = &AdmissionSettings{Enabled: &off}
	s.admission.at = time.Now()
	release, err := s.admitTaskStart(context.Background(), admissionPlanTask("busy", machineFixed))
	if err != nil {
		t.Fatalf("a disabled gate must not refuse: %v", err)
	}
	release()
}

// The project override raises the direction cap (the Persona product's line of 8) above the default of 3.
func TestAdmissionProjectDirectionOverride(t *testing.T) {
	s, _, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	d.samples["busy"] = admissionSample(0.8, 16*1024*1024*1024, 2*1024*1024*1024, 1)
	ctx := context.Background()
	if err := s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	// Three open tasks stay under the default cap of 8, and the project's line of 8 admits a fourth.
	for i := 0; i < 3; i++ {
		id := "p-task-" + string(rune('a'+i))
		s.todos.mu.Lock()
		s.todos.todos[id] = &Todo{ID: id, Supervision: &Supervision{Supervisor: "dir-1"},
			Agents: []TodoAgent{{MachineID: "busy", SessionID: "s" + string(rune('a'+i))}}}
		s.todos.mu.Unlock()
	}
	s.admission.cached = &AdmissionSettings{
		Enabled: ptrBool(true), DirectionCapByProject: map[string]int{"persona": 8}}
	s.admission.at = time.Now()
	task := admissionPlanTask("busy", machineFixed)
	task.Project = "persona"
	release, err := s.admitTaskStart(ctx, task)
	if err != nil {
		t.Fatalf("the project cap of 8 must admit a fourth agent: %v", err)
	}
	release()
}

// Concurrent admits are decided atomically: with one slot left, only one of many starts may take it.
func TestAdmissionConcurrentClaimsAreAtomic(t *testing.T) {
	s, _, d := admissionHub(t)
	d.machines["busy"] = admissionMachine(8)
	// 19 procs against a cap of 20: exactly one slot.
	d.samples["busy"] = admissionSample(0.8, 16*1024*1024*1024, 2*1024*1024*1024, 19)
	ctx := context.Background()
	const n = 16
	var wg sync.WaitGroup
	results := make(chan error, n)
	releases := make(chan func(), n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			// No direction: isolate the machine-level atomic decision.
			rel, err := s.admitAgentStart(ctx, "busy")
			results <- err
			if err == nil {
				releases <- rel
			}
		}()
	}
	wg.Wait()
	close(results)
	admitted, refused := 0, 0
	for err := range results {
		if err == nil {
			admitted++
		} else {
			refused++
		}
	}
	if admitted != 1 || refused != n-1 {
		t.Fatalf("exactly one start must win the last slot: admitted=%d refused=%d", admitted, refused)
	}
	close(releases)
	for rel := range releases {
		rel()
	}
}

// recommendMachine picks the lowest load1-per-core among two healthy candidates, breaking ties by free memory.
func TestAdmissionRecommendPicksLowestLoad(t *testing.T) {
	s, f, d := admissionHub(t)
	d.machines["busy"] = admissionMachine(8)
	d.samples["busy"] = admissionSample(40.0, 16*1024*1024*1024, 2*1024*1024*1024, 1)
	// A third, even less loaded machine on both the hub list and the resource layer.
	f.devices = append(f.devices, Device{ID: "calm", Name: "Calm", Home: "/home/me", Online: true, Reachable: true})
	f.sessions["calm"] = nil
	d.add("calm", "Calm")
	d.machines["calm"] = admissionMachine(8)
	// load 0.1 vs idle's 0.5: calm must win despite being added last.
	d.samples["calm"] = admissionSample(0.1, 16*1024*1024*1024, 2*1024*1024*1024, 1)
	ctx := context.Background()
	got, ok := s.recommendMachine(ctx, "busy", nil)
	if !ok {
		t.Fatal("no recommended machine")
	}
	if got.ID != "calm" {
		t.Fatalf("recommendation = %q, want the least-loaded calm", got.ID)
	}
}

// A flexible task with no eligible alternative (every other machine fails the gate) is refused, not moved.
func TestAdmissionFlexibleNoAlternativeRefused(t *testing.T) {
	s, _, d := admissionHub(t)
	d.machines["busy"] = admissionMachine(8)
	d.samples["busy"] = admissionSample(40.0, 16*1024*1024*1024, 15*1024*1024*1024, 2)
	// The usual idle machine is over its lines too, so no alternative can host the work.
	d.machines["idle"] = admissionMachine(8)
	d.samples["idle"] = admissionSample(40.0, 16*1024*1024*1024, 15*1024*1024*1024, 2)
	ctx := context.Background()
	task := admissionPlanTask("busy", machineFlexible)
	if _, err := s.admitTaskStart(ctx, task); err == nil {
		t.Fatal("a flexible task with no eligible machine must be refused")
	}
	if task.Machine != "busy" {
		t.Fatalf("a refused flexible task must not be moved: %+v", task)
	}
}

// The agent_start tool path refuses before a node start and names the alternative.
func TestAdmissionAgentStartToolRefuses(t *testing.T) {
	s, f, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	d.samples["busy"] = admissionSample(40.0, 16*1024*1024*1024, 15*1024*1024*1024, 2)
	ctx := context.Background()
	args, _ := json.Marshal(map[string]string{
		"machine": "busy", "cwd": "/home/me/code/app", "prompt": "do the work"})
	if _, err := s.runTool(ctx, "server", "conv9", "agent_start", args); err == nil {
		t.Fatal("agent_start on an overloaded machine must be refused")
	}
	if starts := taskStartMachines(f); len(starts) != 0 {
		t.Fatalf("a refused agent_start reached a node: %v", starts)
	}
}

func ptrBool(b bool) *bool { return &b }

// admissionMarkWorking puts the sessions into the fleet snapshot the gate reads (fleetPrevious) as working, the same
// rows a node's session list would carry.
func admissionMarkWorking(s *Service, machine string, sessionIDs ...string) {
	rows := make([]proto.Session, 0, len(sessionIDs))
	for _, id := range sessionIDs {
		rows = append(rows, proto.Session{SessionID: id, Status: proto.StatusWorking, State: proto.StateWorking})
	}
	s.fleetKeepPrevious(machine, rows)
}

// admissionDirTask builds one open, supervised task of direction with a linked agent.
func admissionDirTask(id, direction, machine, session string, extra func(*Todo)) *Todo {
	t := &Todo{ID: id, Supervision: &Supervision{Supervisor: direction},
		Agents: []TodoAgent{{MachineID: machine, SessionID: session}}}
	if extra != nil {
		extra(t)
	}
	return t
}

// The running count only counts agents actually working a turn: linked agents whose sessions are idle, and tasks
// parked, waiting for the owner or finished and held short of live, occupy no direction slot.
func TestAdmissionDirectionCountExcludesIdleAgents(t *testing.T) {
	s, _, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	d.samples["busy"] = admissionSample(0.8, 16*1024*1024*1024, 2*1024*1024*1024, 1)
	ctx := context.Background()
	if err := s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour)
	put := func(t *Todo) {
		s.todos.mu.Lock()
		s.todos.todos[t.ID] = t
		s.todos.mu.Unlock()
	}
	// One genuinely working agent.
	put(admissionDirTask("work", "dir-1", "busy", "sw", nil))
	// A linked agent whose session is idle.
	put(admissionDirTask("idle", "dir-1", "busy", "si", nil))
	// A parked task.
	put(admissionDirTask("parked", "dir-1", "busy", "sp", func(t *Todo) {
		t.Supervision.State = reviewParked
		t.Supervision.Reviews = []TaskReview{{Verdict: reviewParked, Until: until}}
	}))
	// A task waiting for the owner's decision.
	put(admissionDirTask("needs-user", "dir-1", "busy", "sn", func(t *Todo) {
		t.Supervision.State = reviewNeedsUser
	}))
	// A task whose done judge is waiting on an owner decision.
	put(admissionDirTask("owner-wait", "dir-1", "busy", "so", func(t *Todo) {
		t.Supervision.Waits = &OwnerWait{At: time.Now()}
	}))
	// A finished task held short of live (merged, not deployed).
	put(admissionDirTask("held", "dir-1", "busy", "sh", func(t *Todo) {
		t.Delivery = &TodoDelivery{State: deliveryMerged}
	}))
	// Only "sw" works a turn; the rest stay idle or are filtered structurally.
	admissionMarkWorking(s, "busy", "sw")
	if got := s.directionInFlight(ctx, "dir-1"); got != 1 {
		t.Fatalf("only the working agent must count, got %d", got)
	}
	// Once the idle agent's session starts working, it takes a second slot.
	admissionMarkWorking(s, "busy", "sw", "si")
	if got := s.directionInFlight(ctx, "dir-1"); got != 2 {
		t.Fatalf("the now-working agent must count, got %d", got)
	}
}

// The direction cap is taken from direction_cap_by_direction by supervisor conversation id, then by the direction's
// displayed name; the project map stays as the fallback, and the global default comes last.
func TestAdmissionDirectionCapResolution(t *testing.T) {
	s, _, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	d.samples["busy"] = admissionSample(0.8, 16*1024*1024*1024, 2*1024*1024*1024, 1)
	ctx := context.Background()
	cfg := AdmissionSettings{
		DirectionCapByDirection: map[string]int{"dir-1": 2, "Infra": 3},
		DirectionCapByProject:   map[string]int{"persona": 8},
	}
	// Exact supervisor id match wins over everything.
	if got := s.directionCap(ctx, "dir-1", "persona", cfg); got != 2 {
		t.Fatalf("supervisor id override must win, got %d", got)
	}
	// No id entry: the project fallback applies.
	if got := s.directionCap(ctx, "dir-2", "persona", cfg); got != 8 {
		t.Fatalf("project fallback must apply, got %d", got)
	}
	// Neither map names it: the code default.
	if got := s.directionCap(ctx, "dir-3", "other", cfg); got != admissionDirectionCap {
		t.Fatalf("code default must apply, got %d", got)
	}
	// A direction name entry resolves through the displayed name of a current leader.
	s.prefs.cached = &Settings{Main: "main-conv", Leaders: []string{"dir-4"}}
	s.prefs.at = time.Now()
	s.directions.mu.Lock()
	s.directions.m["dir-4"] = &direction{By: directionByUser, Name: "Infra", At: time.Now()}
	s.directions.mu.Unlock()
	if got := s.directionCap(ctx, "dir-4", "", cfg); got != 3 {
		t.Fatalf("direction name override must resolve, got %d", got)
	}
	// The global DirectionCap still overrides the code default when no map entry matches.
	four := 4
	if got := s.directionCap(ctx, "dir-5", "", AdmissionSettings{DirectionCap: &four}); got != 4 {
		t.Fatalf("global direction_cap must apply, got %d", got)
	}
}

// With a narrow direction cap of 2, a third working agent of that direction is refused while another direction keeps
// dispatching up to its own line.
func TestAdmissionDirectionCapByDirectionDenies(t *testing.T) {
	s, f, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	d.samples["busy"] = admissionSample(0.8, 16*1024*1024*1024, 2*1024*1024*1024, 1)
	ctx := context.Background()
	if err := s.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	s.todos.mu.Lock()
	for _, sid := range []string{"s1", "s2"} {
		id := "cap-task-" + sid
		s.todos.todos[id] = admissionDirTask(id, "dir-1", "busy", sid, nil)
	}
	s.todos.mu.Unlock()
	admissionMarkWorking(s, "busy", "s1", "s2")
	s.admission.cached = &AdmissionSettings{Enabled: ptrBool(true),
		DirectionCapByDirection: map[string]int{"dir-1": 2}}
	s.admission.at = time.Now()

	if _, err := s.admitTaskStart(ctx, admissionPlanTask("busy", machineFixed)); err == nil ||
		!strings.Contains(err.Error(), admissionText(s, "admission_direction_full", i18n.Args{
			"running": 2, "limit": 2})) {
		t.Fatalf("the direction line of 2 must refuse a third working agent, got %v", err)
	}
	if starts := taskStartMachines(f); len(starts) != 0 {
		t.Fatalf("a refused start reached a node: %v", starts)
	}
	// A task of another direction is not counted against dir-1's line.
	task := admissionPlanTask("busy", machineFixed)
	task.Supervisor = "dir-2"
	release, err := s.admitTaskStart(ctx, task)
	if err != nil {
		t.Fatalf("another direction must dispatch under its own line: %v", err)
	}
	release()
}

// admissionText renders an admission i18n key (short name) in the test service's language, matching how the gate
// words errors.
func admissionText(s *Service, key string, args ...i18n.Args) string {
	return i18n.T(s.lang(context.Background()), "assistant-tasks."+key, args...)
}
