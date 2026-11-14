package assistant

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

func sampleWorkout() WorkoutReport {
	now := time.Now().UTC().Truncate(time.Second)
	return WorkoutReport{ID: "12345678-1234-1234-1234-123456789abc", Title: "Workout report", Activity: "Running", EnabledAt: now.Add(-2 * time.Hour), Start: now.Add(-time.Hour), End: now, Duration: 3600}
}
func TestWorkoutValidation(t *testing.T) {
	v := sampleWorkout()
	if err := v.validate(time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*WorkoutReport){
		"invalid UUID":      func(v *WorkoutReport) { v.ID = "12345678-1234-1234-1234-123456789abz" },
		"short UUID":        func(v *WorkoutReport) { v.ID = "12345678-1234-1234-1234-123456789ab" },
		"before consent":    func(v *WorkoutReport) { v.EnabledAt = v.Start.Add(time.Second) },
		"short":             func(v *WorkoutReport) { v.Duration = 599 },
		"duration overflow": func(v *WorkoutReport) { v.Duration = 3602 },
		"not finite":        func(v *WorkoutReport) { v.Duration = math.NaN() },
		"duplicate minute":  func(v *WorkoutReport) { v.HeartRate = []WorkoutHeartRate{{1, 100}, {1, 120}} },
		"negative minute":   func(v *WorkoutReport) { v.HeartRate = []WorkoutHeartRate{{-1, 100}} },
		"outside workout":   func(v *WorkoutReport) { v.HeartRate = []WorkoutHeartRate{{61, 100}} },
		"future":            func(v *WorkoutReport) { v.End = time.Now().Add(workoutClockSkew + time.Hour) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := v
			change(&bad)
			if bad.validate(time.Now()) == nil {
				t.Fatal("accepted invalid workout")
			}
		})
	}
}

// Splits, elevation and the heart rate's average and maximum (#2526) are checked like the other measurements; an
// older phone's report, without them, stays valid.
func TestWorkoutValidationSplitsElevationAndHeartRate(t *testing.T) {
	f := func(x float64) *float64 { return &x }
	v := sampleWorkout()
	v.ElevationGain, v.HeartRateAvg, v.HeartRateMax = f(42), f(141), f(178)
	v.Splits = []WorkoutSplit{{1, 330}, {2, 318.5}, {3, 341}}
	if err := v.validate(time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*WorkoutReport){
		"negative elevation":  func(v *WorkoutReport) { v.ElevationGain = f(-1) },
		"elevation too high":  func(v *WorkoutReport) { v.ElevationGain = f(20001) },
		"zero average":        func(v *WorkoutReport) { v.HeartRateAvg = f(0) },
		"maximum over 300":    func(v *WorkoutReport) { v.HeartRateMax = f(301) },
		"average over max":    func(v *WorkoutReport) { v.HeartRateAvg = f(180) },
		"splits skip a km":    func(v *WorkoutReport) { v.Splits = []WorkoutSplit{{1, 330}, {3, 330}} },
		"splits from zero":    func(v *WorkoutReport) { v.Splits = []WorkoutSplit{{0, 330}} },
		"empty split":         func(v *WorkoutReport) { v.Splits = []WorkoutSplit{{1, 0}} },
		"split not finite":    func(v *WorkoutReport) { v.Splits = []WorkoutSplit{{1, math.Inf(1)}} },
		"splits past the end": func(v *WorkoutReport) { v.Splits = []WorkoutSplit{{1, 1800}, {2, 1802}} },
		"too many splits": func(v *WorkoutReport) {
			v.Splits = make([]WorkoutSplit, workoutMaxSplits+1)
			for i := range v.Splits {
				v.Splits[i] = WorkoutSplit{i + 1, 1}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := v
			change(&bad)
			if bad.validate(time.Now()) == nil {
				t.Fatal("accepted invalid workout")
			}
		})
	}
}

// A phone clock running ahead of the hub's must not turn a finished workout into a permanent rejection.
func TestWorkoutValidationToleratesClockDrift(t *testing.T) {
	v := sampleWorkout()
	for _, ahead := range []time.Duration{time.Minute + time.Second, 10 * time.Minute, 6 * time.Hour} {
		if err := v.validate(time.Now().Add(-ahead)); err != nil {
			t.Fatalf("phone %v ahead: %v", ahead, err)
		}
	}
}
func TestWorkoutReceiptSurvivesRestartAndDeletion(t *testing.T) {
	store := scheduleStore(t)
	ctx := context.Background()
	v := sampleWorkout()
	// Isolate the persistent receipt from previous test runs.
	if err := store.PutSetting(ctx, "health.workout.receipt."+v.ID, false); err != nil {
		t.Fatal(err)
	}
	boot := func() *Service { return newServiceOn(t, newFakeHub(), store) }
	s := boot()
	if err := s.acceptWorkout(ctx, v); err != nil {
		t.Fatal(err)
	}
	v.ID = strings.ToUpper(v.ID)
	if err := s.acceptWorkout(ctx, v); err != nil {
		t.Fatal(err)
	}
	tasks := s.tasksView(ctx, nil)
	if len(tasks) != 1 || tasks[0].Space != SpaceHealth || tasks[0].Repeat != repeatOnce {
		t.Fatalf("tasks: %+v", tasks)
	}
	s = boot()
	if _, err := s.deleteTask(ctx, tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	s = boot()
	if err := s.acceptWorkout(ctx, v); err != nil {
		t.Fatal(err)
	}
	if tasks := s.tasksView(ctx, nil); len(tasks) != 0 {
		t.Fatal("retry recreated deleted report")
	}
}
func TestGoldenWorkoutReport(t *testing.T) {
	golden(t, "workout_report", prompts.RenderWorkoutReport(`{"activity":"Running","duration":600,"heart_rate":[]}`))
}

func TestWorkoutUploadRequiresFullRole(t *testing.T) {
	s := newServiceOn(t, newFakeHub(), &memStore{})
	for _, role := range []string{"answer", "read", "extension", "full"} {
		t.Run(role, func(t *testing.T) {
			// Invalid input reaches validation only after the role check; it never creates a task.
			r := httptest.NewRequest(http.MethodPost, "/api/v1/assistant/health/workouts", strings.NewReader(`{}`))
			r.Header.Set(RoleHeader, role)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			want := http.StatusForbidden
			if role == "full" {
				want = http.StatusBadRequest
			}
			if w.Code != want {
				t.Fatalf("status = %d, want %d: %s", w.Code, want, w.Body.String())
			}
		})
	}
}
