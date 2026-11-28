package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The launcher group the tests configure: the tasks' launcher "work" and a second member "work-2" share one budget of
// two running tasks; "free" is a launcher outside every group.
var testPool = LauncherGroup{Name: "pool", Launchers: []string{"work", "work-2"}, Cap: 2}

// withLauncherGroups pins an admission setting carrying groups (the cache stands in for the stored setting).
func withLauncherGroups(s *Service, groups ...LauncherGroup) {
	on := true
	s.admission.mu.Lock()
	s.admission.cached = &AdmissionSettings{Enabled: &on, LauncherGroups: groups}
	s.admission.at = time.Now()
	s.admission.mu.Unlock()
}

// fleetRow is one session row of a node's list: its launcher and whether it is working a turn.
type fleetRow struct {
	id, launcher string
	working      bool
}

// admissionFleet replaces machine's fleet snapshot with rows, as a node's session list would.
func admissionFleet(s *Service, machine string, rows ...fleetRow) {
	list := make([]proto.Session, 0, len(rows))
	for _, r := range rows {
		status := proto.StatusIdle
		if r.working {
			status = proto.StatusWorking
		}
		list = append(list, proto.Session{SessionID: r.id, Status: status, Launcher: r.launcher})
	}
	s.fleetKeepPrevious(machine, list)
}

// runningTask puts an open task of direction with one linked agent into the todo book.
func runningTask(t *testing.T, s *Service, id, direction, machine, session string) {
	t.Helper()
	if err := s.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.todos.mu.Lock()
	s.todos.todos[id] = admissionDirTask(id, direction, machine, session, nil)
	s.todos.mu.Unlock()
}

// The group counts running tasks on its launchers on every computer, whatever their direction: tasks on another
// launcher, idle agents and parked tasks do not count, and a task counts once however many working agents it links.
func TestAdmissionLauncherGroupCountsAcrossComputers(t *testing.T) {
	s, _, d := admissionHub(t)
	healthyIdle(d)
	busyHealthy(d)
	withLauncherGroups(s, testPool)
	runningTask(t, s, "a", "dir-1", "busy", "sa")     // working on "work"
	runningTask(t, s, "b", "dir-2", "idle", "sb")     // working on "work-2", another computer and direction
	runningTask(t, s, "c", "dir-1", "busy", "sc")     // working on a launcher outside the group
	runningTask(t, s, "d", "dir-1", "idle", "sd")     // linked, idle
	runningTask(t, s, "e", "dir-1", "busy", "se-1")   // two working agents: one task
	runningTask(t, s, "p", "dir-1", "busy", "parked") // parked task
	s.todos.mu.Lock()
	s.todos.todos["e"].Agents = append(s.todos.todos["e"].Agents, TodoAgent{MachineID: "busy", SessionID: "se-2"})
	s.todos.todos["p"].Supervision.State = reviewParked
	s.todos.todos["p"].Supervision.Reviews = []TaskReview{{Verdict: reviewParked, Until: time.Now().Add(time.Hour)}}
	s.todos.mu.Unlock()
	admissionFleet(s, "busy", fleetRow{"sa", "work", true}, fleetRow{"sc", "free", true},
		fleetRow{"se-1", "work", true}, fleetRow{"se-2", "work-2", true}, fleetRow{"parked", "work", true})
	admissionFleet(s, "idle", fleetRow{"sb", "work-2", true}, fleetRow{"sd", "work", false})

	if got := s.groupInFlight(testPool); got != 3 {
		t.Fatalf("a, b and e run on the group's launchers: want 3, got %d", got)
	}
	// Free the computers' own lines so only the group can refuse.
	ctx := context.Background()
	big := testPool
	big.Cap = 4
	withLauncherGroups(s, big)
	release, err := s.admitTaskStart(ctx, admissionPlanTask("busy", machineFixed))
	if err != nil {
		t.Fatalf("one slot is left under a cap of 4: %v", err)
	}
	if got := s.groupInFlight(big); got != 4 {
		t.Fatalf("the held claim counts against the group: want 4, got %d", got)
	}
	_, err = s.admitTaskStart(ctx, admissionPlanTask("idle", machineFixed))
	want := admissionText(s, "admission_launcher_group_full", i18n.Args{"group": "pool", "running": 4, "limit": 4})
	if err == nil || !strings.Contains(err.Error(), want) || !isAdmissionDenied(err) {
		t.Fatalf("a full group must refuse on any computer, naming the group: %v", err)
	}
	// Another computer frees no group slot, so the refusal recommends none.
	if strings.Contains(err.Error(), admissionText(s, "admission_recommend", i18n.Args{"machine": "Busy"})) {
		t.Fatalf("a group refusal must not recommend another computer: %v", err)
	}
	free := admissionPlanTask("busy", machineFixed)
	free.Launcher = "free"
	rel, err := s.admitTaskStart(ctx, free)
	if err != nil {
		t.Fatalf("a launcher outside the group is not held by it: %v", err)
	}
	rel()
	release()
	if got := s.groupInFlight(big); got != 3 {
		t.Fatalf("a released claim leaves the group: want 3, got %d", got)
	}
}

// Two starts racing for the group's last slot: exactly one wins, on whichever computers they start.
func TestAdmissionLauncherGroupClaimsAreAtomic(t *testing.T) {
	s, _, d := admissionHub(t)
	healthyIdle(d)
	busyHealthy(d)
	one := testPool
	one.Cap = 1
	withLauncherGroups(s, one)
	ctx := context.Background()
	const n = 16
	var wg sync.WaitGroup
	results := make(chan error, n)
	releases := make(chan func(), n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		machine := []string{"busy", "idle"}[i%2]
		go func() {
			defer wg.Done()
			rel, err := s.admitAgentStart(ctx, machine, "work")
			results <- err
			if err == nil {
				releases <- rel
			}
		}()
	}
	wg.Wait()
	close(results)
	admitted := 0
	for err := range results {
		if err == nil {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("exactly one start may take the group's last slot, got %d", admitted)
	}
	close(releases)
	for rel := range releases {
		rel()
	}
}

// agent_start cannot queue: a full group refuses it before the confirmation card, naming the group and its cap.
func TestAdmissionLauncherGroupRefusesAgentStart(t *testing.T) {
	s, f, d := admissionHub(t)
	healthyIdle(d)
	busyHealthy(d)
	one := testPool
	one.Cap = 1
	withLauncherGroups(s, one)
	runningTask(t, s, "a", "dir-1", "idle", "sa")
	admissionFleet(s, "idle", fleetRow{"sa", "work-2", true})
	args, _ := json.Marshal(map[string]string{
		"machine": "busy", "cwd": "/home/me/code/app", "prompt": "do the work", "launcher": "work"})
	_, err := s.runTool(context.Background(), "server", "conv9", "agent_start", args)
	want := admissionText(s, "admission_launcher_group_full", i18n.Args{"group": "pool", "running": 1, "limit": 1})
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("agent_start on a full group must be refused naming it: %v", err)
	}
	if starts := taskStartMachines(f); len(starts) != 0 {
		t.Fatalf("a refused agent_start reached a node: %v", starts)
	}
	if _, err := s.admitAgentStart(context.Background(), "busy", "work"); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("the claim step must refuse the same way: %v", err)
	}
}

// fullPoolQueued queues a task of launcher "work" behind a one-slot group held by a task on the idle computer.
func fullPoolQueued(t *testing.T, s *Service, d *resourceDevs) *TaskPlan {
	t.Helper()
	healthyIdle(d)
	busyHealthy(d)
	one := testPool
	one.Cap = 1
	withLauncherGroups(s, one)
	runningTask(t, s, "holder", "dir-9", "idle", "sh")
	admissionFleet(s, "idle", fleetRow{"sh", "work-2", true})
	p, _ := queuedStart(t, s, "busy")
	if q := p.Tasks[0].Queue; q == nil || q.Key != admissionGroupFullKey {
		t.Fatalf("precondition: the task waits on the group: %+v", q)
	}
	return p
}

// pastBounds makes task 0's queue record overdue and past every queue bound.
func pastBounds(t *testing.T, s *Service, planID string, ago time.Duration) {
	t.Helper()
	if err := s.patchPlanTask(context.Background(), planID, 0, func(pt *PlanTask) {
		pt.Queue.Attempt = admissionQueueMaxAttempts + 5
		pt.Queue.QueuedAt = time.Now().Add(-ago)
		pt.Queue.NextAt = time.Now().Add(-time.Second)
	}); err != nil {
		t.Fatal(err)
	}
}

// A task waiting on a full group stays queued past the attempt and time bounds, then starts once a slot frees.
func TestAdmissionQueueWaitsOnFullLauncherGroupPastTheBounds(t *testing.T) {
	s, f, d := admissionHub(t)
	p := fullPoolQueued(t, s, d)
	checkPlacementGolden(t, p.Tasks[0].Placement, "v1 enforce busy ->  (assistant-tasks.admission_launcher_group_full) | busy assistant-tasks.admission_launcher_group_full")
	pastBounds(t, s, p.ID, 3*admissionQueueMaxWait)
	s.sweepAdmissionQueue(context.Background())
	waitFor(t, "the over-bounds group wait to be retried and queued again", func() bool {
		loaded, err := s.loadPlan(context.Background(), p.ID)
		return err == nil && loaded.Tasks[0].Queue != nil && loaded.Tasks[0].Queue.Attempt == admissionQueueMaxAttempts+6
	})
	todo := todoNow(s, p.Tasks[0].TodoID)
	if todo.DoneAt != nil || todo.StartError != "" || strings.Contains(feedKinds(s, "conv1"), "receipt") {
		t.Fatalf("a full group must not fail the start: %+v, feed %s", todo, feedKinds(s, "conv1"))
	}
	if card := queuedCard(t, s, p.ID); card.State != "waiting" ||
		!strings.Contains(card.Reason, admissionText(s, "admission_launcher_group_full", i18n.Args{"group": "pool", "running": 1, "limit": 1})) {
		t.Fatalf("the card must read waiting with the group's reason: %+v", card)
	}
	if starts := taskStartMachines(f); len(starts) != 0 {
		t.Fatalf("a full group must not start an Agent: %v", starts)
	}

	admissionFleet(s, "idle", fleetRow{"sh", "work-2", false}) // the holder's turn ends: one slot
	if err := s.patchPlanTask(context.Background(), p.ID, 0, func(pt *PlanTask) { pt.Queue.NextAt = time.Now().Add(-time.Second) }); err != nil {
		t.Fatal(err)
	}
	s.sweepAdmissionQueue(context.Background())
	waitFor(t, "the queued task to start once the group has room", func() bool { return len(taskStartMachines(f)) == 1 })
}

// Another line keeps its bounds while a group is configured, counted from when that line took over: a task that waited
// hours on the group and is then held by machine load is queued, not failed, and fails only past the bounds.
func TestAdmissionQueueOtherLinesKeepTheirBoundsWithGroups(t *testing.T) {
	s, _, d := admissionHub(t)
	p := fullPoolQueued(t, s, d)
	pastBounds(t, s, p.ID, 5*time.Hour)
	admissionFleet(s, "idle", fleetRow{"sh", "work-2", false}) // the group has room now
	busyOverloaded(d)                                          // but the task's computer is overloaded
	s.sweepAdmissionQueue(context.Background())
	waitFor(t, "the load refusal to start a new queue stretch", func() bool {
		loaded, err := s.loadPlan(context.Background(), p.ID)
		if err != nil || loaded.Tasks[0].Queue == nil {
			return false
		}
		q := loaded.Tasks[0].Queue
		return q.Key == "assistant-tasks.admission_load" && q.Attempt == 1 && time.Since(q.QueuedAt) < time.Minute
	})
	if todo := todoNow(s, p.Tasks[0].TodoID); todo.DoneAt != nil {
		t.Fatalf("hours on the group must not count against the load line's bounds: %+v", todo)
	}
	timeOutQueue(t, s, p.ID)
	waitFor(t, "the load wait to fail at its bounds", func() bool {
		return todoNow(s, p.Tasks[0].TodoID).StartError != ""
	})
	if !strings.Contains(feedKinds(s, "conv1"), "receipt") {
		t.Fatalf("the timed-out start must get its failure receipt: %s", feedKinds(s, "conv1"))
	}
}

// The queue record lives in the plan, so a restarted hub (a fresh service on the same database, with empty claim books)
// still waits on the group past the bounds and starts the task once there is room.
func TestAdmissionQueueGroupWaitSurvivesRestart(t *testing.T) {
	url := newMigratedDatabase(t, db.ManagerSet)
	database, err := db.Open(context.Background(), url, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s, _, d := admissionHubOn(t, database)
	p := fullPoolQueued(t, s, d)
	pastBounds(t, s, p.ID, 3*admissionQueueMaxWait)

	s2, f2, d2 := admissionHubOn(t, database)
	healthyIdle(d2)
	busyHealthy(d2)
	one := testPool
	one.Cap = 1
	withLauncherGroups(s2, one)
	runningTask(t, s2, "holder", "dir-9", "idle", "sh")
	admissionFleet(s2, "idle", fleetRow{"sh", "work-2", true})
	s2.sweepAdmissionQueue(context.Background())
	waitFor(t, "the restarted hub to retry and queue the group wait again", func() bool {
		loaded, err := s2.loadPlan(context.Background(), p.ID)
		return err == nil && loaded.Tasks[0].Queue != nil && loaded.Tasks[0].Queue.Attempt == admissionQueueMaxAttempts+6
	})
	if todo := todoNow(s2, p.Tasks[0].TodoID); todo.DoneAt != nil || todo.StartError != "" {
		t.Fatalf("the restarted hub must keep the group wait open: %+v", todo)
	}
	admissionFleet(s2, "idle", fleetRow{"sh", "work-2", false})
	if err := s2.patchPlanTask(context.Background(), p.ID, 0, func(pt *PlanTask) { pt.Queue.NextAt = time.Now().Add(-time.Second) }); err != nil {
		t.Fatal(err)
	}
	s2.sweepAdmissionQueue(context.Background())
	waitFor(t, "the restarted hub to start the task once the group has room", func() bool { return len(taskStartMachines(f2)) == 1 })
}

// Without launcher groups nothing changes: no group line resolves and a busy fleet on any launcher is not held.
func TestAdmissionWithoutLauncherGroupsIsUnchanged(t *testing.T) {
	s, _, d := admissionHub(t)
	healthyIdle(d)
	busyHealthy(d)
	for i := 0; i < 6; i++ {
		id := string(rune('a' + i))
		runningTask(t, s, id, "dir-"+id, "idle", "s"+id)
	}
	admissionFleet(s, "idle", fleetRow{"sa", "work", true}, fleetRow{"sb", "work", true}, fleetRow{"sc", "work", true},
		fleetRow{"sd", "work", true}, fleetRow{"se", "work", true}, fleetRow{"sf", "work", true})
	if _, ok := s.admissionSettings(context.Background()).launcherGroup("work"); ok {
		t.Fatal("no group may resolve without the setting")
	}
	release, err := s.admitTaskStart(context.Background(), admissionPlanTask("busy", machineFixed))
	if err != nil {
		t.Fatalf("without groups only the existing lines apply: %v", err)
	}
	release()
	for name, n := range s.admission.groupClaims {
		if n != 0 {
			t.Fatalf("no group claim may be taken without groups: %q holds %d", name, n)
		}
	}
}

// The setting refuses groups that could not be applied as written.
func TestAdmissionLauncherGroupValidation(t *testing.T) {
	ok := AdmissionSettings{LauncherGroups: []LauncherGroup{testPool, {Name: "other", Launchers: []string{"free"}, Cap: 1}}}
	if err := ok.validate(); err != nil {
		t.Fatalf("valid groups refused: %v", err)
	}
	for name, groups := range map[string][]LauncherGroup{
		"no name":         {{Launchers: []string{"work"}, Cap: 1}},
		"no launcher":     {{Name: "pool", Cap: 1}},
		"empty launcher":  {{Name: "pool", Launchers: []string{" "}, Cap: 1}},
		"zero cap":        {{Name: "pool", Launchers: []string{"work"}}},
		"negative cap":    {{Name: "pool", Launchers: []string{"work"}, Cap: -1}},
		"duplicate name":  {{Name: "pool", Launchers: []string{"a"}, Cap: 1}, {Name: "pool", Launchers: []string{"b"}, Cap: 1}},
		"launcher in two": {{Name: "one", Launchers: []string{"work"}, Cap: 1}, {Name: "two", Launchers: []string{"work"}, Cap: 1}},
	} {
		if err := (AdmissionSettings{LauncherGroups: groups}).validate(); err == nil {
			t.Errorf("%s: want a refusal", name)
		}
	}
	var decoded AdmissionSettings
	if err := json.Unmarshal([]byte(`{"launcher_groups":[{"name":"pool","launchers":["work","work-2"],"cap":2}]}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if g, found := decoded.launcherGroup("work-2"); !found || g.Name != "pool" || g.Cap != 2 {
		t.Fatalf("the documented JSON must resolve: %+v %v", g, found)
	}
}
