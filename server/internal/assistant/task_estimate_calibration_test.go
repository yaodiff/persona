package assistant

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

func TestCalibratedEstimateUsesTheMostSpecificSupportedMedian(t *testing.T) {
	key := db.TaskEstimateCohort{Kind: "code", Machine: "box", Repository: "/fixture/repository"}
	sample := func(machine, repository string, minutes int) db.TaskEstimateSample {
		return db.TaskEstimateSample{Machine: machine, Repository: repository, ActualMinutes: minutes}
	}
	sameRepo := []db.TaskEstimateSample{
		sample("box", key.Repository, 8), sample("box", key.Repository, 14),
		sample("box", key.Repository, 700), sample("box", key.Repository, 10),
		sample("box", key.Repository, 12),
	}
	for _, test := range []struct {
		name    string
		samples []db.TaskEstimateSample
		want    TaskEstimateEvidence
		learned bool
	}{
		{"repository before broad outliers", append(slices.Clone(sameRepo), sample("other", key.Repository, 1000)),
			TaskEstimateEvidence{Minutes: 12, SampleCount: 5, Cohort: estimateRepoMachine}, true},
		{"machine without sufficient repository samples", append(slices.Clone(sameRepo[:4]), sample("box", "/other", 16)),
			TaskEstimateEvidence{Minutes: 14, SampleCount: 5, Cohort: estimateMachine}, true},
		{"same local paths on other machines stay broader", []db.TaskEstimateSample{
			sample("box", key.Repository, 2), sample("other", key.Repository, 6), sample("other", key.Repository, 8),
			sample("other", key.Repository, 9), sample("other", key.Repository, 19), sample("other", key.Repository, 21),
		}, TaskEstimateEvidence{Minutes: 9, SampleCount: 6, Cohort: estimateComparable}, true},
		{"few samples keep the default", sameRepo[:4],
			TaskEstimateEvidence{Minutes: 60, SampleCount: 4, Cohort: estimateDefault}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, learned := chooseTaskEstimate(key, test.samples)
			if got != test.want || learned != test.learned {
				t.Fatalf("got %+v learned=%t, want %+v learned=%t", got, learned, test.want, test.learned)
			}
		})
	}
	capped := make([]db.TaskEstimateSample, estimateSampleLimit+15)
	for i := range capped {
		capped[i] = sample("box", key.Repository, 3000)
		if i >= estimateSampleLimit {
			capped[i].ActualMinutes = 1
		}
	}
	got, learned := chooseTaskEstimate(key, capped)
	if !learned || got.Minutes != 24*60 || got.SampleCount != estimateSampleLimit {
		t.Fatalf("bounded median = %+v (%t)", got, learned)
	}
}

func startCalibrationFixture(t *testing.T, s *Service, title string, override any) (Todo, TaskBrief, string) {
	t.Helper()
	answer, isErr := tool(t, s, "task_plan", briefTask(map[string]any{"title": title, "expect_minutes": override}))
	if isErr {
		t.Fatalf("task_plan: %s", answer)
	}
	_, tail, found := strings.Cut(answer, "→ todo ")
	if !found || len(strings.Fields(tail)) == 0 {
		t.Fatalf("no dispatched fixture to-do: %s", answer)
	}
	id := strings.Fields(tail)[0]
	waitFor(t, "admitted calibration fixture", func() bool {
		todo, ok := s.todoNow(id)
		return ok && len(todo.Agents) == 1 && s.taskStarting.Load() == 0
	})
	todo, _ := s.todoNow(id)
	return todo, s.mustBrief(t, id), answer
}

type interruptedEstimateProjection struct {
	*db.DB
	fail     atomic.Bool
	attempts atomic.Int32
}

func (d *interruptedEstimateProjection) AcceptTaskEstimateRound(ctx context.Context, round, todo string, at time.Time, minutes int) error {
	if d.fail.Load() {
		d.attempts.Add(1)
		return errors.New("synthetic interruption before projecting")
	}
	return d.DB.AcceptTaskEstimateRound(ctx, round, todo, at, minutes)
}

type deadlineExhaustingEstimateStore struct {
	*memStore
	todoStore
	taskEstimateStore
	recovered      atomic.Bool
	reconcileCalls atomic.Int32
	sampleCalls    atomic.Int32
}

// ReconcileTaskEstimateRounds restores the fifth accepted round, as after an
// interrupted projection, then keeps working until its deadline.
func (d *deadlineExhaustingEstimateStore) ReconcileTaskEstimateRounds(ctx context.Context, _ db.TaskEstimateCohort, _, _ time.Time, _ int) error {
	d.reconcileCalls.Add(1)
	if d.sampleCalls.Load() == 0 {
		d.recovered.Store(true)
	}
	<-ctx.Done()
	return ctx.Err()
}

func (d *deadlineExhaustingEstimateStore) RecentTaskEstimateSamples(ctx context.Context, _ db.TaskEstimateCohort, _, _ time.Time, _ int) ([]db.TaskEstimateSample, error) {
	d.sampleCalls.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	samples := []db.TaskEstimateSample{
		{Machine: "box", ActualMinutes: 8},
		{Machine: "box", ActualMinutes: 10},
		{Machine: "box", ActualMinutes: 12},
		{Machine: "box", ActualMinutes: 14},
	}
	if d.recovered.Load() {
		samples = append(samples, db.TaskEstimateSample{Machine: "box", ActualMinutes: 120})
	}
	return samples, nil
}

// Slow recovery must neither hide durable history nor run after the read
// whose missing sample it restores.
func TestTaskEstimateHistorySurvivesReconciliationDeadline(t *testing.T) {
	store := &deadlineExhaustingEstimateStore{memStore: &memStore{}}
	s, _, _ := briefServiceOn(t, nil, "please fix the fixture issue")
	s.setStore(func() Store { return store })
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	task := PlanTask{Goal: "Return a fixture answer", Readonly: true, Machine: "box"}
	s.estimateNewPlanTask(ctx, "conv1", &task)
	want := TaskEstimateEvidence{Minutes: 12, SampleCount: 5, Cohort: estimateMachine}
	if task.ExpectMinutes != 12 || task.EstimateSource != briefEstimateLearned ||
		task.EstimateEvidence == nil || *task.EstimateEvidence != want {
		t.Fatalf("reconciliation deadline hid durable history: %d minutes (%s), evidence %+v",
			task.ExpectMinutes, task.EstimateSource, task.EstimateEvidence)
	}
	if store.reconcileCalls.Load() != 1 || store.sampleCalls.Load() != 1 {
		t.Fatalf("estimate store calls: reconcile=%d samples=%d", store.reconcileCalls.Load(), store.sampleCalls.Load())
	}
}

// The seed process runs actual plan, link, completion and review paths. The
// child process plans again against the same DB with no shared manager state.
func TestTaskEstimateCalibrationAfterProcessRestart(t *testing.T) {
	const restartEnv = "PERSONA_TEST_ESTIMATE_RESTART_DSN"
	const reopenEnv = "PERSONA_TEST_ESTIMATE_REOPEN_TODO"
	if dsn := os.Getenv(restartEnv); dsn != "" {
		database, err := db.Open(t.Context(), dsn, db.ManagerSet)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(database.Close)
		s, _, _ := briefServiceOn(t, database, "please fix another fixture issue")
		todo, brief, answer := startCalibrationFixture(t, s, "Learned task", nil)
		want := TaskEstimateEvidence{Minutes: 12, SampleCount: 5, Cohort: estimateMachine}
		if !strings.Contains(answer, "12 minutes (learned, 5 samples, machine)") ||
			brief.ExpectNow != 12 || brief.EstimateSourceNow != briefEstimateLearned ||
			brief.EstimateEvidenceNow == nil || *brief.EstimateEvidenceNow != want {
			t.Fatalf("plan and durable brief did not calibrate: %s %+v", answer, brief)
		}
		due := brief.EstimateClockAt.Add(12 * time.Minute)
		if !brief.EstimateDueAt.Equal(due) {
			t.Fatalf("review clock = %v, want %v", brief.EstimateDueAt, due)
		}
		s.briefClock(t.Context(), todo, due)
		s.estimateDueTold(t.Context(), []superviseSignal{{Todo: todo.ID, Kind: sigEstimateDue, Key: estimateDueKey(brief), At: due, Minutes: 12}})
		s.clearEstimateDue(todo.ID)
		s.briefClock(t.Context(), todo, due.Add(time.Minute))
		if s.supervise.pending[todo.ID+"|"+sigEstimateDue].Kind != "" || s.mustBrief(t, todo.ID).EstimateDueToldAt == nil {
			t.Fatal("learned review clock was not armed and marked once")
		}
		_, override, answer := startCalibrationFixture(t, s, "Explicit estimate", 35)
		if !strings.Contains(answer, "35 minutes (override)") || override.ExpectNow != 35 ||
			override.EstimateSourceNow != briefEstimateOverride || override.EstimateEvidenceNow != nil {
			t.Fatalf("explicit override consulted history: %s %+v", answer, override)
		}
		reopen := os.Getenv(reopenEnv)
		if err := s.reopenTodo(t.Context(), reopen); err != nil {
			t.Fatal(err)
		}
		_, reopenedEstimate, answer := startCalibrationFixture(t, s, "After reopening", nil)
		if reopenedEstimate.EstimateSourceNow != briefEstimateSystemDefault || reopenedEstimate.ExpectNow != 60 ||
			reopenedEstimate.EstimateEvidenceNow == nil || reopenedEstimate.EstimateEvidenceNow.SampleCount != 4 {
			t.Fatalf("reopened work was still sampled: %s %+v", answer, reopenedEstimate)
		}
		if closed, err := s.reviewDoneTodo(t.Context(), reopen, "Manually reopened fixture", true); err != nil || !closed {
			t.Fatalf("close reopened fixture: %t %v", closed, err)
		}
		_, afterReclose, answer := startCalibrationFixture(t, s, "After manual closing", nil)
		if afterReclose.EstimateEvidenceNow == nil || afterReclose.EstimateEvidenceNow.SampleCount != 4 ||
			afterReclose.EstimateSourceNow != briefEstimateSystemDefault {
			t.Fatalf("a re-close reused an earlier acceptance: %s %+v", answer, afterReclose)
		}
		return
	}

	var reopen string
	dsn := newMigratedDatabase(t, db.ManagerSet)
	if !t.Run("seed", func(t *testing.T) {
		database, err := db.Open(t.Context(), dsn, db.ManagerSet)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(database.Close)
		projection := &interruptedEstimateProjection{DB: database}
		s, _, _ := briefServiceOn(t, projection, "please fix the fixture issues")
		ctx := t.Context()
		var completed []string
		var pending Todo
		for _, duration := range []int{8, 10, 12, 14, 120} {
			todo, brief, _ := startCalibrationFixture(t, s, "Comparable fixture task", nil)
			if brief.EstimateSourceNow != briefEstimateSystemDefault {
				t.Fatalf("learned too early from %+v", brief.EstimateEvidenceNow)
			}
			// Advance the synthetic report timestamps without sleeping.
			reported := brief.EstimateClockAt.Add(time.Duration(duration) * time.Minute)
			s.recordBriefCompletion(ctx, todo, briefCompletionReport, reported)
			s.recordBriefCompletion(ctx, todo, briefCompletionReport, reported.Add(time.Hour))
			if duration == 120 {
				pending = todo
				continue
			}
			if closed, err := s.reviewDoneTodo(ctx, todo.ID, "Fixture verified", true); err != nil || !closed {
				t.Fatalf("accept fixture: %t %v", closed, err)
			}
			completed = append(completed, todo.ID)
		}

		canceled, brief, _ := startCalibrationFixture(t, s, "Canceled after a report", 12)
		s.recordBriefCompletion(ctx, canceled, briefCompletionReport, brief.EstimateClockAt.Add(time.Minute))
		if closed, err := s.reviewDoneTodo(ctx, canceled.ID, "Fixture cancellation", false); err != nil || !closed {
			t.Fatalf("cancel fixture: %t %v", closed, err)
		}
		legacy, legacyBrief, _ := startCalibrationFixture(t, s, "Previous release fixture", 22)
		// A previous release has a real brief dispatch but no frozen cohort.
		execIn(t, dsn, "DELETE FROM task_estimate_rounds WHERE todo_id = '"+legacy.ID+"'")
		s.recordBriefCompletion(ctx, legacy, briefCompletionReport, legacyBrief.EstimateClockAt.Add(time.Minute))
		if closed, err := s.reviewDoneTodo(ctx, legacy.ID, "Historical fixture accepted", true); err != nil || !closed {
			t.Fatalf("accept legacy fixture: %t %v", closed, err)
		}
		// The owner workspace endpoint constructs a plan directly without
		// task_plan provenance. Start it through the same shared plan path.
		now := time.Now().UTC()
		ownerPlan := &TaskPlan{ID: "plan_owner_fixture", Conversation: "conv1", State: planOpen, CreatedAt: now, UpdatedAt: now,
			Tasks: []PlanTask{{Title: "Owner start fixture", Prompt: "Owner start", Machine: "box", Cwd: "/code/app", Readonly: true}}}
		if err := s.savePlan(ctx, ownerPlan); err != nil {
			t.Fatal(err)
		}
		started, err := s.startPlan(ctx, ownerPlan.ID, nil, false)
		if err != nil || started.Tasks[0].TodoID == "" {
			t.Fatalf("owner fixture did not dispatch: %v", err)
		}
		ownerTodoID := started.Tasks[0].TodoID
		waitFor(t, "owner fixture's Agent", func() bool { return s.taskStarting.Load() == 0 })
		ownerTodo, _ := s.todoNow(ownerTodoID)
		ownerBrief := s.mustBrief(t, ownerTodoID)
		s.recordBriefCompletion(ctx, ownerTodo, briefCompletionReport, ownerBrief.EstimateClockAt.Add(time.Minute))
		if closed, err := s.reviewDoneTodo(ctx, ownerTodoID, "Owner fixture accepted", true); err != nil || !closed {
			t.Fatalf("accept owner fixture: %t %v", closed, err)
		}

		var retries sync.WaitGroup
		for _, id := range append(completed, canceled.ID, legacy.ID) {
			for range 3 {
				retries.Go(func() { s.learnAcceptedEstimate(ctx, id) })
			}
		}
		retries.Wait()
		reopen = completed[0]

		// Commit the review and its first completion to real PostgreSQL,
		// then lose the projection write before this process exits.
		projection.fail.Store(true)
		if closed, err := s.reviewDoneTodo(ctx, pending.ID, "Fixture verified before interruption", true); err != nil || !closed {
			t.Fatalf("accept fixture with interrupted projection: %t %v", closed, err)
		}
		if projection.attempts.Load() == 0 {
			t.Fatal("the fixture did not interrupt a projection")
		}
		// A restored proof outside the window must stay out. The child
		// process will reconcile it with the normal, in-date window.
		after := time.Now().Add(time.Minute)
		if err := database.ReconcileTaskEstimateRounds(ctx, db.TaskEstimateCohort{Kind: "general", Readonly: true},
			after, after.Add(time.Hour), estimateRepairLimit); err != nil {
			t.Fatal(err)
		}
		if got := countIn(t, dsn, "SELECT count(*) FROM task_estimate_rounds WHERE accepted_at IS NOT NULL"); got != 4 {
			t.Fatalf("samples before restart after duplicates, expiry, cancellation and owner start = %d, want 4", got)
		}
		if got := countIn(t, dsn, "SELECT count(*) FROM task_estimate_rounds WHERE admitted_at IS NOT NULL"); got != 6 {
			t.Fatalf("admitted fixture rounds = %d, want 6", got)
		}
	}) {
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run", "^TestTaskEstimateCalibrationAfterProcessRestart$", "-test.v")
	child.Env = append(os.Environ(), restartEnv+"="+dsn, reopenEnv+"="+reopen)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("reopened manager process: %v\n%s", err, output)
	}

	t.Run("another manager starts without this history", func(t *testing.T) {
		isolated, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(isolated.Close)
		s, _, _ := briefServiceOn(t, isolated, "please fix the same fixture issues")
		_, brief, answer := startCalibrationFixture(t, s, "Another manager", nil)
		if brief.ExpectNow != 60 || brief.EstimateSourceNow != briefEstimateSystemDefault ||
			brief.EstimateEvidenceNow == nil || brief.EstimateEvidenceNow.SampleCount != 0 ||
			!strings.Contains(answer, "60 minutes (system_default, 0 samples") {
			t.Fatalf("manager inherited foreign evidence: %s %+v", answer, brief)
		}
	})
}

func TestTaskPlanEstimateProvenanceSurvivesOwnerStart(t *testing.T) {
	dsn := newMigratedDatabase(t, db.ManagerSet)
	database, err := db.Open(t.Context(), dsn, db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s, _, _ := briefServiceOn(t, database, "please fix the fixture issue")
	answer, isErr := tool(t, s, "task_plan", briefTask(map[string]any{
		"readonly": false, "workspace": "none", "expect_minutes": nil,
	}))
	fields := strings.Fields(answer)
	if isErr || len(fields) < 2 || fields[0] != "Plan" {
		t.Fatalf("writable fixture must await the owner: %s", answer)
	}
	id := fields[1]
	saved, err := s.loadPlan(t.Context(), id)
	if err != nil || len(saved.Tasks) != 1 || !saved.Tasks[0].EstimateCalibration {
		t.Fatalf("plan did not preserve its tool provenance: %v", err)
	}
	started, err := s.startPlan(t.Context(), id, []PlanTask{{Title: "Edited on the card"}}, false)
	if err != nil || started.Tasks[0].TodoID == "" {
		t.Fatalf("owner Start failed: %v", err)
	}
	waitFor(t, "owner-started calibration round", func() bool { return s.taskStarting.Load() == 0 })
	if got := countIn(t, dsn, "SELECT count(*) FROM task_estimate_rounds WHERE admitted_at IS NOT NULL"); got != 1 {
		t.Fatalf("owner Start dropped tool provenance: %d admitted rounds", got)
	}
}
