package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// queuedStart builds a supervised todo and a started one-task plan on machine, then runs its start synchronously.
func queuedStart(t *testing.T, s *Service, machine string) (*TaskPlan, Todo) {
	t.Helper()
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "排队活", "排队活", "conv1", "plan_q")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", nil, 0)
	task := admissionPlanTask(machine, machineFixed)
	task.TodoID = todo.ID
	p := &TaskPlan{ID: "plan_q", Conversation: "conv1", State: planStarted,
		Tasks: []PlanTask{*task}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.savePlan(ctx, p); err != nil {
		t.Fatal(err)
	}
	s.startPlanTask(p.ID, p.Conversation, 0, *task, false)
	loaded, err := s.loadPlan(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	return loaded, todoNow(s, todo.ID)
}

func busyOverloaded(d *resourceDevs) {
	d.machines["busy"] = admissionMachine(8)
	// load 99 on 8 cores: far over the 4x line, memory and procs fine.
	d.samples["busy"] = admissionSample(99.0, 16*1024*1024*1024, 2*1024*1024*1024, 1)
}

func busyHealthy(d *resourceDevs) {
	d.machines["busy"] = admissionMachine(8)
	d.samples["busy"] = admissionSample(0.5, 16*1024*1024*1024, 2*1024*1024*1024, 1)
}

// An admission-gate refusal parks the task: it is not failed, gets no failure receipt, and the plan carries the queue.
func TestAdmissionQueueParksInsteadOfFailing(t *testing.T) {
	s, f, d := admissionHub(t)
	busyOverloaded(d)
	p, todo := queuedStart(t, s, "busy")

	pt := p.Tasks[0]
	if pt.Queue == nil {
		t.Fatal("the refused task must be queued")
	}
	if pt.Queue.Attempt != 1 || pt.Queue.NextAt.IsZero() || pt.Queue.QueuedAt.IsZero() {
		t.Fatalf("bad queue record: %+v", pt.Queue)
	}
	if !strings.Contains(pt.Queue.Reason, admissionText(s, "admission_load", i18nArgsLoad())) {
		t.Fatalf("queue reason must name the load line, got %q", pt.Queue.Reason)
	}
	if todo.DoneAt != nil || todo.StartError != "" {
		t.Fatalf("a queued task must stay open, got done=%v error=%q", todo.DoneAt, todo.StartError)
	}
	if strings.Contains(feedKinds(s, "conv1"), "receipt") {
		t.Fatalf("a queued task must get no receipt: %s", feedKinds(s, "conv1"))
	}
	if starts := taskStartMachines(f); len(starts) != 0 {
		t.Fatalf("a refused start reached a node: %v", starts)
	}
}

// The queued task's card reads as waiting and carries the rendered queue reason (#6909).
func TestAdmissionQueueCardShowsWaiting(t *testing.T) {
	s, _, d := admissionHub(t)
	busyOverloaded(d)
	p, _ := queuedStart(t, s, "busy")

	items := []FeedItem{{ID: "plan", Kind: feedPlan, Ref: p.ID, At: time.Now()}}
	cards, err := s.taskCards(context.Background(), items, map[string]*TaskPlan{p.ID: p})
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].State != "waiting" {
		t.Fatalf("queued card = %+v, want waiting", cards)
	}
	want := admissionText(s, "admission_queued", i18n.Args{"reason": p.Tasks[0].Queue.Reason})
	if cards[0].Reason != want {
		t.Fatalf("card reason = %q, want %q", cards[0].Reason, want)
	}
}

// A due retry after the machine recovers starts the agent and clears the queue, still with no failure receipt.
func TestAdmissionQueueRetryStartsWhenGateOpens(t *testing.T) {
	s, f, d := admissionHub(t)
	busyOverloaded(d)
	p, _ := queuedStart(t, s, "busy")
	if p.Tasks[0].Queue == nil {
		t.Fatal("precondition: the task is queued")
	}

	// The machine is healthy now; the sweep's retry goes through the ordinary async start and passes the gate.
	busyHealthy(d)
	s.sweepAdmissionQueue(context.Background()) // NextAt is in the future, so this must not retry yet
	if got := taskStartMachines(f); len(got) != 0 {
		t.Fatalf("a retry before NextAt must not start anything: %v", got)
	}
	if err := s.patchPlanTask(context.Background(), p.ID, 0, func(pt *PlanTask) { pt.Queue.NextAt = time.Now().Add(-time.Second) }); err != nil {
		t.Fatal(err)
	}
	s.sweepAdmissionQueue(context.Background())
	waitFor(t, "the queued task's retry to start an agent", func() bool { return len(taskStartMachines(f)) == 1 })
	waitFor(t, "the queue marker to clear", func() bool {
		loaded, err := s.loadPlan(context.Background(), p.ID)
		return err == nil && loaded.Tasks[0].Queue == nil
	})
	if todo := todoNow(s, p.Tasks[0].TodoID); todo.DoneAt != nil || todo.StartError != "" {
		t.Fatalf("the retried task must stay open and un-failed: %+v", todo)
	}
	if strings.Contains(feedKinds(s, "conv1"), "receipt") {
		t.Fatalf("a successful retry must leave no receipt: %s", feedKinds(s, "conv1"))
	}
}

// admissionHubOn is admissionHub with a real database, for the SQL-only queue-selection cases.
func admissionHubOn(t *testing.T, st *db.DB) (*Service, *fakeHub, *resourceDevs) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.devices = []Device{
		{ID: "busy", Name: "Busy", Home: "/home/me", Online: true, Reachable: true},
		{ID: "idle", Name: "Idle", Home: "/home/me", Online: true, Reachable: true},
	}
	f.sessions["busy"], f.sessions["idle"] = nil, nil
	f.launcherList = []proto.Launcher{{Name: "work"}}
	s := newServiceOn(t, f, st)
	d := &resourceDevs{fleetDevs: newFleetDevs(), machines: map[string]*proto.Machine{}, samples: map[string]*proto.MetricSample{}, versions: map[string]*bool{}}
	d.add("busy", "Busy")
	d.add("idle", "Idle")
	s.devs = d
	on := true
	s.admission.cached = &AdmissionSettings{Enabled: &on}
	s.admission.at = time.Now()
	s.admission.machineClaims = map[string]int{}
	s.admission.directionClaims = map[string]int{}
	return s, f, d
}

// A task still refused after the queue bounds fails for good, with the ordinary failure receipt.
func TestAdmissionQueueTimeoutFailsWithReceipt(t *testing.T) {
	s, _, d := admissionHub(t)
	busyOverloaded(d)
	p, _ := queuedStart(t, s, "busy")

	if err := s.patchPlanTask(context.Background(), p.ID, 0, func(pt *PlanTask) {
		pt.Queue.Attempt = admissionQueueMaxAttempts
		pt.Queue.QueuedAt = time.Now().Add(-admissionQueueMaxWait - time.Minute)
		pt.Queue.NextAt = time.Now().Add(-time.Second)
	}); err != nil {
		t.Fatal(err)
	}
	s.sweepAdmissionQueue(context.Background())
	waitFor(t, "the over-bounds queued task to fail", func() bool {
		return todoNow(s, p.Tasks[0].TodoID).StartError != ""
	})
	loaded, err := s.loadPlan(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Tasks[0].Queue != nil || loaded.Tasks[0].Error == "" {
		t.Fatalf("after timeout the queue must clear and the error persist: %+v", loaded.Tasks[0])
	}
	if !strings.Contains(feedKinds(s, "conv1"), "receipt") {
		t.Fatalf("the timed-out task must get a failure receipt: %s", feedKinds(s, "conv1"))
	}
}

// A task whose todo ended while queued simply drops the queue on the next sweep.
func TestAdmissionQueueDropsEndedTodo(t *testing.T) {
	s, _, d := admissionHub(t)
	busyOverloaded(d)
	p, _ := queuedStart(t, s, "busy")
	if err := s.failTodo(context.Background(), p.Tasks[0].TodoID, "owner gave up"); err != nil {
		t.Fatal(err)
	}
	if err := s.patchPlanTask(context.Background(), p.ID, 0, func(pt *PlanTask) { pt.Queue.NextAt = time.Now().Add(-time.Second) }); err != nil {
		t.Fatal(err)
	}
	s.sweepAdmissionQueue(context.Background())
	loaded, err := s.loadPlan(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Tasks[0].Queue != nil {
		t.Fatalf("the ended task's queue must clear: %+v", loaded.Tasks[0].Queue)
	}
}

// A due retry refused again keeps the first QueuedAt, counts the attempt on and schedules another wait.
func TestAdmissionQueueRepeatedRefusalCountsAttempts(t *testing.T) {
	s, _, d := admissionHub(t)
	busyOverloaded(d)
	p, _ := queuedStart(t, s, "busy")
	first := p.Tasks[0].Queue
	if first == nil || first.Attempt != 1 {
		t.Fatalf("precondition: %+v", first)
	}
	if err := s.patchPlanTask(context.Background(), p.ID, 0, func(pt *PlanTask) { pt.Queue.NextAt = time.Now().Add(-time.Second) }); err != nil {
		t.Fatal(err)
	}
	s.sweepAdmissionQueue(context.Background()) // the gate is still shut: the retry parks again
	waitFor(t, "the second refusal to be recorded", func() bool {
		loaded, err := s.loadPlan(context.Background(), p.ID)
		return err == nil && loaded.Tasks[0].Queue != nil && loaded.Tasks[0].Queue.Attempt == 2
	})
	loaded, err := s.loadPlan(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	q := loaded.Tasks[0].Queue
	if !q.QueuedAt.Equal(first.QueuedAt) {
		t.Fatalf("QueuedAt must be preserved: first=%v now=%v", first.QueuedAt, q.QueuedAt)
	}
	if !q.NextAt.After(time.Now()) {
		t.Fatalf("the next retry must wait: NextAt=%v", q.NextAt)
	}
	if todoNow(s, p.Tasks[0].TodoID).DoneAt != nil {
		t.Fatal("the re-parked task must stay open")
	}
}

func TestIsAdmissionDenied(t *testing.T) {
	s, _, d := admissionHub(t)
	busyOverloaded(d)
	_, err := s.admitTaskStart(context.Background(), admissionPlanTask("busy", machineFixed))
	if err == nil {
		t.Fatal("the overloaded machine must refuse")
	}
	if !isAdmissionDenied(err) {
		t.Fatalf("the gate's refusal must be typed as an admission denial: %v", err)
	}
	if isAdmissionDenied(machineOffline("box")) {
		t.Fatal("an unrelated error must not be classified as an admission denial")
	}
}

func i18nArgsLoad() i18n.Args {
	return i18n.Args{"load": "99.0", "cores": 8, "multiplier": 4.0}
}

// An old started plan with a current queue record must still be a retry candidate (#7405).
func TestAdmissionQueueRetryStartsWhenGateOpensOnOlderPlan(t *testing.T) {
	url := newDatabase(t)
	database, err := db.Open(context.Background(), url, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s, f, d := admissionHubOn(t, database)
	busyOverloaded(d)
	p, _ := queuedStart(t, s, "busy")
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(context.Background(), `UPDATE task_plans SET created_at = $1 WHERE id = $2`, time.Now().Add(-2*time.Hour), p.ID); err != nil {
		t.Fatal(err)
	}
	busyHealthy(d)
	if err := s.patchPlanTask(context.Background(), p.ID, 0, func(pt *PlanTask) { pt.Queue.NextAt = time.Now().Add(-time.Second) }); err != nil {
		t.Fatal(err)
	}
	s.sweepAdmissionQueue(context.Background())
	waitFor(t, "the old plan's queued task to start", func() bool { return len(taskStartMachines(f)) == 1 })
	waitFor(t, "the old plan's queue to clear", func() bool {
		loaded, err := s.loadPlan(context.Background(), p.ID)
		return err == nil && loaded.Tasks[0].Queue == nil
	})
	if starts := taskStartMachines(f); len(starts) != 1 || starts[0] != "busy" {
		t.Fatalf("the old plan must start exactly once on its machine: %v", starts)
	}
}

// A durable queue record outlives the recent-update window, so a long outage must not abandon it (#7722).
func TestAdmissionQueueTimesOutAfterLookbackDowntime(t *testing.T) {
	url := newDatabase(t)
	database, err := db.Open(context.Background(), url, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s, f, d := admissionHubOn(t, database)
	busyOverloaded(d)
	p, _ := queuedStart(t, s, "busy")
	if p.Tasks[0].Queue == nil {
		t.Fatal("precondition: the task is queued")
	}
	aged := time.Now().Add(-2 * time.Hour)
	if err := s.patchPlanTask(context.Background(), p.ID, 0, func(pt *PlanTask) {
		pt.Queue.QueuedAt = aged
		pt.Queue.NextAt = aged
	}); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(context.Background(), `UPDATE task_plans SET created_at = $1, updated_at = $1 WHERE id = $2`, aged, p.ID); err != nil {
		t.Fatal(err)
	}

	busyHealthy(d)
	s.sweepAdmissionQueue(context.Background())
	waitFor(t, "the persisted queue to time out after downtime", func() bool {
		return todoNow(s, p.Tasks[0].TodoID).StartError != ""
	})
	wantError := i18n.T(s.lang(context.Background()), "assistant-tasks.admission_queue_timeout", i18n.Args{
		"limit": admissionQueueMaxWait.Round(time.Minute).String(), "reason": p.Tasks[0].Queue.Reason})
	todo := todoNow(s, p.Tasks[0].TodoID)
	if todo.DoneAt == nil || todo.StartError != wantError {
		t.Fatalf("the durable queue must end with its ordinary timeout: %+v, want %q", todo, wantError)
	}
	loaded, err := s.loadPlan(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Tasks[0].Queue != nil || loaded.Tasks[0].Error != wantError {
		t.Fatalf("the timed-out queue must clear and retain its error: %+v", loaded.Tasks[0])
	}
	if starts := taskStartMachines(f); len(starts) != 0 {
		t.Fatalf("an over-bounds queue must not start an Agent: %v", starts)
	}
	s.sweepAdmissionQueue(context.Background())
	if receipts := strings.Count(feedKinds(s, "conv1"), feedReceipt); receipts != 1 {
		t.Fatalf("the durable queue must produce exactly one failure receipt, got %d", receipts)
	}
}
