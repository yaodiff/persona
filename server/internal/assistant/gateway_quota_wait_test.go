package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func configureQuotaFixture(s *Service, f *fakeHub, d *resourceDevs) *atomic.Bool {
	busyHealthy(d)
	f.launcherList = []proto.Launcher{{Name: "work", Agent: "claude"}}
	group := testPool
	group.QuotaPool = &GatewayQuotaPool{Source: "fixture-pool", Provider: "claude"}
	withLauncherGroups(s, group)
	full := new(atomic.Bool)
	full.Store(true)
	d.gatewayRetry = func(source, provider string, now time.Time) time.Time {
		if source == "fixture-pool" && provider == "claude" && full.Load() {
			return now.Add(4 * time.Minute)
		}
		return time.Time{}
	}
	return full
}

func quotaPlan(t *testing.T, s *Service, id string) *TaskPlan {
	t.Helper()
	plan, err := s.loadPlan(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func dueQuota(t *testing.T, s *Service, id string, expire bool) *TaskPlan {
	t.Helper()
	if err := s.patchPlanTask(context.Background(), id, 0, func(pt *PlanTask) {
		queue := *pt.Queue
		queue.NextAt = time.Now().Add(-time.Second)
		if expire {
			wait := *queue.Gateway
			wait.Deadline = queue.NextAt
			queue.Gateway = &wait
		}
		pt.Queue = &queue
	}); err != nil {
		t.Fatal(err)
	}
	return quotaPlan(t, s, id)
}

func TestGatewayQuotaWaitParksBeforeAdmissionAndResumes(t *testing.T) {
	s, f, d := admissionHub(t)
	full := configureQuotaFixture(s, f, d)
	p, todo := queuedStart(t, s, "busy")
	q := p.Tasks[0].Queue
	if q == nil || q.Gateway == nil || q.Key != gatewayQuotaWaitKey ||
		q.Gateway.Pool.Source != "fixture-pool" || q.Gateway.Launcher != "work" ||
		q.Gateway.Deadline.Sub(q.QueuedAt) != gatewayQuotaMaxWait ||
		q.NextAt.Sub(q.QueuedAt) != admissionQueueInterval {
		t.Fatalf("bad persisted quota wait: %+v", q)
	}
	if todo.DoneAt != nil || todo.StartError != "" || len(taskStartMachines(f)) != 0 ||
		s.admission.machineClaims["busy"] != 0 || s.admission.groupClaims["pool"] != 0 {
		t.Fatalf("deferral reached admission, reported failure, or reached a node: %+v", todo)
	}
	checkPlacementGolden(t, p.Tasks[0].Placement, "v1 enforce busy ->  (assistant-tasks.gateway_quota_wait) | busy assistant-tasks.gateway_quota_wait")
	card := queuedCard(t, s, p.ID)
	if card.State != "waiting" || !strings.Contains(card.Reason, q.Reason) {
		t.Fatalf("the card must explain the wait: %+v", card)
	}

	s.sweepAdmissionQueue(context.Background())
	if len(taskStartMachines(f)) != 0 {
		t.Fatal("a quota wait must not be released before its next check")
	}
	dueQuota(t, s, p.ID, false)
	s.sweepAdmissionQueue(context.Background())
	wait := quotaPlan(t, s, p.ID).Tasks[0].Queue
	if wait == nil || wait.Claimed || wait.Attempt != 2 || !wait.Gateway.Deadline.Equal(q.Gateway.Deadline) ||
		len(taskStartMachines(f)) != 0 {
		t.Fatalf("recheck must keep the same wait budget without a start: %+v", wait)
	}

	full.Store(false)
	dueQuota(t, s, p.ID, false)
	s.sweepAdmissionQueue(context.Background())
	waitFor(t, "one successful quota release", func() bool {
		return len(taskStartMachines(f)) == 1 && quotaPlan(t, s, p.ID).Tasks[0].Queue == nil
	})
	s.sweepAdmissionQueue(context.Background())
	if starts := taskStartMachines(f); len(starts) != 1 || starts[0] != "busy" {
		t.Fatalf("the scheduled retry must start once on the original computer: %v", starts)
	}
	if got := quotaPlan(t, s, p.ID).Tasks[0]; got.Launcher != "work" || got.Machine != "busy" {
		t.Fatalf("quota deferral changed placement or launcher: %+v", got)
	}
}

func TestGatewayQuotaWaitExpiresIntoOrdinaryAdmission(t *testing.T) {
	s, f, d := admissionHub(t)
	configureQuotaFixture(s, f, d)
	p, _ := queuedStart(t, s, "busy")
	if p.Tasks[0].Queue == nil {
		t.Fatal("expected an initial quota wait")
	}
	dueQuota(t, s, p.ID, true)
	busyOverloaded(d)
	s.sweepAdmissionQueue(context.Background())
	waitFor(t, "expiry to leave quota and enter the existing resource gate", func() bool {
		q := quotaPlan(t, s, p.ID).Tasks[0].Queue
		return q != nil && q.Key == "assistant-tasks.admission_load"
	})
	q := quotaPlan(t, s, p.ID).Tasks[0].Queue
	if q.Attempt != 1 || q.Gateway == nil || q.Claimed || len(taskStartMachines(f)) != 0 {
		t.Fatalf("ordinary admission must have a new retry budget: %+v", q)
	}
	busyHealthy(d)
	dueQuota(t, s, p.ID, false)
	s.sweepAdmissionQueue(context.Background())
	waitFor(t, "the ordinary gate to start after the bounded quota wait", func() bool {
		return len(taskStartMachines(f)) == 1 && quotaPlan(t, s, p.ID).Tasks[0].Queue == nil
	})
}

func TestGatewayQuotaWaitRespectsCancelledTask(t *testing.T) {
	s, f, d := admissionHub(t)
	full := configureQuotaFixture(s, f, d)
	p, todo := queuedStart(t, s, "busy")
	if p.Tasks[0].Queue == nil {
		t.Fatal("expected an initial quota wait")
	}
	if err := s.failTodo(context.Background(), todo.ID, "ended by fixture"); err != nil {
		t.Fatal(err)
	}
	full.Store(false)
	s.sweepAdmissionQueue(context.Background()) // clear an ended task even before the next check was due
	if quotaPlan(t, s, p.ID).Tasks[0].Queue != nil || len(taskStartMachines(f)) != 0 {
		t.Fatal("an ended quota wait must be removed without contacting a node")
	}
}

func TestGatewayQuotaWaitLeavesAnAlreadyLinkedTaskAlone(t *testing.T) {
	ctx := context.Background()
	s, f, d := admissionHub(t)
	full := configureQuotaFixture(s, f, d)
	p, todo := queuedStart(t, s, "busy")
	if p.Tasks[0].Queue == nil {
		t.Fatal("expected an initial quota wait")
	}
	if err := s.linkTodoAgent(ctx, todo.ID, "busy", "fixture-existing-agent", "Already started", "/fixture/work", "claude"); err != nil {
		t.Fatal(err)
	}
	full.Store(false)
	s.sweepAdmissionQueue(ctx)
	if quotaPlan(t, s, p.ID).Tasks[0].Queue != nil || len(taskStartMachines(f)) != 0 {
		t.Fatal("the task already had an Agent and must not start another one")
	}
}

func TestGatewayQuotaWaitRequiresOptInAndActualLauncherFit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Service, *fakeHub, *resourceDevs)
	}{
		{"unconfigured quota", func(s *Service, _ *fakeHub, _ *resourceDevs) {
			withLauncherGroups(s, testPool)
		}},
		{"unknown launcher kind", func(_ *Service, f *fakeHub, _ *resourceDevs) {
			f.launcherList = []proto.Launcher{{Name: "work"}}
		}},
		{"different launcher provider", func(_ *Service, f *fakeHub, _ *resourceDevs) {
			f.launcherList = []proto.Launcher{{Name: "work", Agent: "codex"}}
		}},
		{"recovery past the bounded wait", func(_ *Service, _ *fakeHub, d *resourceDevs) {
			d.gatewayRetry = func(_, _ string, now time.Time) time.Time { return now.Add(2 * gatewayQuotaMaxWait) }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, d := admissionHub(t)
			configureQuotaFixture(s, f, d)
			tc.change(s, f, d)
			p, _ := queuedStart(t, s, "busy")
			if q := p.Tasks[0].Queue; q != nil || len(taskStartMachines(f)) != 1 {
				t.Fatalf("must start through the normal path, queue %+v, starts %v", q, taskStartMachines(f))
			}
		})
	}
}

func TestGatewayQuotaWaitReleasesAfterBindingRemoval(t *testing.T) {
	s, f, d := admissionHub(t)
	configureQuotaFixture(s, f, d)
	p, _ := queuedStart(t, s, "busy")
	if p.Tasks[0].Queue == nil {
		t.Fatal("expected an initial quota wait")
	}
	// The old pool still reports it is full. A removed binding no longer consents to waiting.
	withLauncherGroups(s, testPool)
	dueQuota(t, s, p.ID, false)
	s.sweepAdmissionQueue(context.Background())
	waitFor(t, "a task to start after removing its binding", func() bool {
		return len(taskStartMachines(f)) == 1 && quotaPlan(t, s, p.ID).Tasks[0].Queue == nil
	})
}

func TestGatewayQuotaWaitDoesNotChangeDirectAdmission(t *testing.T) {
	s, f, d := admissionHub(t)
	configureQuotaFixture(s, f, d)
	release, err := s.admitAgentStart(context.Background(), "busy", "work")
	if err != nil {
		t.Fatalf("direct admission cannot take the quota wait: %v", err)
	}
	release()
}

func TestGatewayQuotaWaitDoesNotReplayAnUnconfirmedStart(t *testing.T) {
	s, f, d := admissionHub(t)
	full := configureQuotaFixture(s, f, d)
	p, todo := queuedStart(t, s, "busy")
	if p.Tasks[0].Queue == nil {
		t.Fatal("expected an initial quota wait")
	}
	full.Store(false)
	f.failStart = context.DeadlineExceeded
	dueQuota(t, s, p.ID, false)
	s.sweepAdmissionQueue(context.Background())
	waitFor(t, "the unanswered start to finish handling", func() bool {
		return len(taskStartMachines(f)) == 1 && s.taskStarting.Load() == 0
	})
	s.sweepAdmissionQueue(context.Background())
	queue := quotaPlan(t, s, p.ID).Tasks[0].Queue
	if queue == nil || !queue.Claimed || len(taskStartMachines(f)) != 1 || todoNow(s, todo.ID).DoneAt != nil {
		t.Fatalf("the unconfirmed start must keep its claim without another launch: %+v", queue)
	}
}

func TestGatewayQuotaWaitRestartsAndClaimsOnceInPostgres(t *testing.T) {
	t.Run("quota wait", func(t *testing.T) { testGatewayClaimOnce(t, false) })
	t.Run("quota followed by resource wait", func(t *testing.T) { testGatewayClaimOnce(t, true) })
}

func testGatewayClaimOnce(t *testing.T, resourceWait bool) {
	t.Helper()
	ctx := context.Background()
	database, err := db.Open(ctx, newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	before, f0, d0 := admissionHubOn(t, database)
	configureQuotaFixture(before, f0, d0)
	original, _ := queuedStart(t, before, "busy")
	if q := original.Tasks[0].Queue; q == nil || q.Claimed || len(taskStartMachines(f0)) != 0 {
		t.Fatalf("must be durable before restarting: %+v", q)
	}
	if resourceWait {
		busyOverloaded(d0)
		d0.gatewayRetry = func(string, string, time.Time) time.Time { return time.Time{} }
		dueQuota(t, before, original.ID, false)
		before.sweepAdmissionQueue(ctx)
		waitFor(t, "resource gate to preserve the quota origin", func() bool {
			q := quotaPlan(t, before, original.ID).Tasks[0].Queue
			return q != nil && q.Gateway != nil && q.Key == "assistant-tasks.admission_load" && !q.Claimed
		})
	}

	first, f1, d1 := admissionHubOn(t, database)
	second, f2, d2 := admissionHubOn(t, database)
	configureQuotaFixture(first, f1, d1).Store(false)
	configureQuotaFixture(second, f2, d2).Store(false)
	for _, s := range []*Service{first, second} {
		if err := s.ensureTodosLoaded(ctx); err != nil {
			t.Fatal(err)
		}
	}
	p := dueQuota(t, first, original.ID, false)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, s := range []*Service{first, second} {
		workers.Add(1)
		go func(s *Service) {
			defer workers.Done()
			<-start
			s.retryGatewayWait(ctx, p, 0, p.Tasks[0], time.Now())
		}(s)
	}
	close(start)
	workers.Wait()
	waitFor(t, "the one claimed start to link and clear the receipt", func() bool {
		return len(taskStartMachines(f1))+len(taskStartMachines(f2)) == 1 && quotaPlan(t, first, p.ID).Tasks[0].Queue == nil
	})
	first.sweepAdmissionQueue(ctx)
	second.sweepAdmissionQueue(ctx)
	if attempts := len(taskStartMachines(f0)) + len(taskStartMachines(f1)) + len(taskStartMachines(f2)); attempts != 1 {
		t.Fatalf("restarting and concurrent sweeps started %d agents", attempts)
	}
}

func TestGatewayQuotaWaitKeepsUnconfirmedClaimAcrossRestart(t *testing.T) {
	ctx := context.Background()
	database, err := db.Open(ctx, newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	before, f0, d0 := admissionHubOn(t, database)
	configureQuotaFixture(before, f0, d0)
	plan, _ := queuedStart(t, before, "busy")
	task := plan.Tasks[0]
	if task.Queue == nil {
		t.Fatal("expected a persisted quota wait")
	}
	task.planID, task.planIndex = plan.ID, 0
	claimed := *task.Queue
	claimed.Claimed, claimed.NextAt = true, time.Now().Add(-time.Second)
	if !before.replaceTaskQueue(ctx, task, &claimed, queueQuotaStart) {
		t.Fatal("fixture must persist the release claim before stopping")
	}
	restarted, f1, d1 := admissionHubOn(t, database)
	configureQuotaFixture(restarted, f1, d1).Store(false)
	if err := restarted.recoverTaskStarts(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	restarted.sweepAdmissionQueue(ctx)
	queue := quotaPlan(t, restarted, plan.ID).Tasks[0].Queue
	if queue == nil || !queue.Claimed || len(taskStartMachines(f0))+len(taskStartMachines(f1)) != 0 {
		t.Fatalf("a claim with an unknown outcome must help the owner reconcile it without replay: %+v", queue)
	}
}

func TestGatewayQuotaWaitChecksPeerCancellationAndLink(t *testing.T) {
	for _, how := range []string{"cancel", "link"} {
		for _, phase := range []string{"before claim", "before dispatch"} {
			t.Run(how+"/"+phase, func(t *testing.T) {
				ctx := context.Background()
				database, err := db.Open(ctx, newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(database.Close)
				first, f0, d0 := admissionHubOn(t, database)
				configureQuotaFixture(first, f0, d0)
				p, todo := queuedStart(t, first, "busy")
				if p.Tasks[0].Queue == nil {
					t.Fatal("expected an initial quota wait")
				}
				dueQuota(t, first, p.ID, false)
				peer, f1, d1 := admissionHubOn(t, database)
				configureQuotaFixture(peer, f1, d1).Store(false)
				if err := peer.ensureTodosLoaded(ctx); err != nil {
					t.Fatal(err)
				}
				p = quotaPlan(t, peer, p.ID)
				task := p.Tasks[0]
				task.planID, task.planIndex = p.ID, 0
				claim := *task.Queue
				claim.Claimed = true
				if phase == "before dispatch" && !peer.replaceTaskQueue(ctx, task, &claim, queueQuotaStart) {
					t.Fatal("fixture must claim the task before peer change")
				}
				now := time.Now().UTC()
				if how == "cancel" {
					if _, applied, err := database.ReviewDoneTodo(ctx, todo.ID, "cancelled by fixture", false, now); err != nil || !applied {
						t.Fatalf("fixture must durably cancel the task: %v", err)
					}
				} else if _, err := database.LinkTodoAgent(ctx, todo.ID, "busy", "fixture-linked-agent", "Already linked", "/fixture/work", "claude", "", now); err != nil {
					t.Fatal(err)
				}
				if cached, ok := peer.todoNow(todo.ID); !ok || !unstartedTodo(cached) {
					t.Fatal("fixture must retain the now-obsolete peer cache")
				}
				if phase == "before claim" {
					if peer.replaceTaskQueue(ctx, task, &claim, queueQuotaStart) {
						t.Fatal("the atomic claim took a task already handled by another service")
					}
					peer.retryGatewayWait(ctx, p, 0, task, now)
				} else {
					task.Queue = &claim
					peer.startPlanTask(p.ID, p.Conversation, 0, task, false)
				}
				if q := quotaPlan(t, peer, p.ID).Tasks[0].Queue; q != nil || len(taskStartMachines(f0))+len(taskStartMachines(f1)) != 0 {
					t.Fatalf("durable peer change must stop dispatch and clear the stale wait: %+v", q)
				}
				if phase == "before dispatch" {
					// The stale worker can get a machine refusal after the peer already cleared its claim.
					denied := admissionMessage("assistant-tasks.admission_load", i18nArgsLoad())
					first.queueAdmissionRefusal(p.ID, 0, task, &admissionDeniedError{msg: denied, reason: denied}, false)
					if q := quotaPlan(t, first, p.ID).Tasks[0].Queue; q != nil {
						t.Fatalf("a stale refusal recreated the cleared release claim: %+v", q)
					}
				}
			})
		}
	}
}

func TestGatewayQuotaPoolSettingValidation(t *testing.T) {
	valid := GatewayQuotaPool{Source: "test.gateway_1", Provider: "claude"}
	for _, item := range []struct {
		binding GatewayQuotaPool
		ok      bool
	}{
		{valid, true}, {GatewayQuotaPool{Source: valid.Source}, false},
		{GatewayQuotaPool{Source: "https://gateway.example.test", Provider: "claude"}, false},
		{GatewayQuotaPool{Source: valid.Source, Provider: "unknown"}, false},
	} {
		group := testPool
		group.QuotaPool = &item.binding
		cfg := AdmissionSettings{LauncherGroups: []LauncherGroup{group}}
		if err := cfg.validate(); (err == nil) != item.ok {
			t.Fatalf("binding %+v validity = %v", item.binding, err)
		}
		if item.ok {
			raw, err := json.Marshal(cfg)
			if err != nil || !strings.Contains(string(raw), "\"quota_pool\"") {
				t.Fatalf("the explicit binding needs to be saved: %s, %v", raw, err)
			}
		}
	}
}
