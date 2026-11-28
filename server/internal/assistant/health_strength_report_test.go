package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/healthhost"
)

func strengthReportTestID(seed string) string {
	v := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("%x-%x-%x-%x-%x", v[:4], v[4:6], v[6:8], v[8:10], v[10:16])
}

func strengthReportTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

type workoutReportFault struct {
	healthhost.Layer
	fail     bool
	received int
}

func (f *workoutReportFault) WorkoutReport(ctx context.Context, id, language string) (assistantwire.StrengthReportPrompt, bool, error) {
	if f.fail {
		return assistantwire.StrengthReportPrompt{}, false, errors.New("synthetic report failure")
	}
	return f.Layer.WorkoutReport(ctx, id, language)
}
func (f *workoutReportFault) WorkoutReceived(ctx context.Context, w assistantwire.StrengthWorkout) error {
	f.received++
	if f.fail {
		return errors.New("synthetic receive failure")
	}
	return f.Layer.WorkoutReceived(ctx, w)
}

func TestStrengthWorkoutReceiptLinksBothOrdersAndOtherActivities(t *testing.T) {
	needsHealthModule(t)
	d := strengthReportTestDB(t)
	s := newServiceOn(t, newFakeHub(), d)
	ctx := context.Background()
	e := assistantwire.StrengthExercise{ID: strengthReportTestID("workout-core-exercise"), Name: "Workout press " + randomHex(4), WeightRule: "stack"}
	if _, _, err := d.HealthStrengthExerciseSave(ctx, e); err != nil {
		t.Fatal(err)
	}
	v := sampleWorkout()
	v.ID = strengthReportTestID("workout-core-window")
	v.Activity = assistantwire.WorkoutActivityStrength
	for _, first := range []bool{true, false} {
		if !first {
			v.ID = strengthReportTestID("workout-core-window-second")
			v.Start = v.Start.Add(-24 * time.Hour)
			v.End = v.End.Add(-24 * time.Hour)
		}
		add := func() {
			_, err := d.HealthStrengthSetAdd(ctx, []assistantwire.StrengthSet{{ID: strengthReportTestID("locker-" + v.ID), ExerciseID: e.ID, Weight: 40, Reps: 10, At: v.End.Add(2 * time.Hour)}})
			if err != nil {
				t.Fatal(err)
			}
		}
		if first {
			add()
		}
		if err := s.acceptWorkout(ctx, v); err != nil {
			t.Fatal(err)
		}
		if !first {
			add()
		}
		rows, err := d.HealthStrengthSets(ctx, assistantwire.StrengthSetFilter{Workout: v.ID})
		if err != nil || len(rows) != 1 || rows[0].ID != strengthReportTestID("locker-"+v.ID) {
			t.Fatal("arrival order failed", first, rows, err)
		}
		if err := s.acceptWorkout(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	running := sampleWorkout()
	running.ID = strengthReportTestID("running-no-strength")
	if err := s.acceptWorkout(ctx, running); err != nil {
		t.Fatal(err)
	}
	if w, err := d.HealthStrengthWorkout(ctx, running.ID); w != nil || err != nil {
		t.Fatal("running entered strength table")
	}
	fault := &workoutReportFault{Layer: s.healthMod, fail: true}
	s.healthMod = fault
	v.ID = strengthReportTestID("failed-strength-receive")
	if err := s.acceptWorkout(ctx, v); err == nil {
		t.Fatal("failed strength receive acknowledged")
	}
	if _, ok := taskByID(s, "workout-"+v.ID); ok {
		t.Fatal("failed receive saved task")
	}
	s.healthMod = nil
	v.ID = strengthReportTestID("absent-strength-receive")
	if err := s.acceptWorkout(ctx, v); err != nil {
		t.Fatal("absent module blocked ordinary report", err)
	}
	if fault.received != 1 {
		t.Fatal("absent module was called")
	}
}

func TestStrengthWorkoutTaskAssemblesAtExecutionAndFallsBack(t *testing.T) {
	needsHealthModule(t)
	d := strengthReportTestDB(t)
	s, f, _ := healthExecutionService(t)
	s.setStore(func() Store { return d })
	ctx := context.Background()
	if err := d.PutSetting(ctx, "health.items", map[string]any{"items": []string{"strength"}, "revision": 1}); err != nil {
		t.Fatal(err)
	}
	e := assistantwire.StrengthExercise{ID: strengthReportTestID("run-report-exercise"), Name: "Just logged press", WeightRule: "stack"}
	if _, _, err := d.HealthStrengthExerciseSave(ctx, e); err != nil {
		t.Fatal(err)
	}
	v := sampleWorkout()
	v.ID = strengthReportTestID("run-report-workout")
	v.Activity = assistantwire.WorkoutActivityStrength
	if err := s.acceptWorkout(ctx, v); err != nil {
		t.Fatal(err)
	}
	task, ok := taskByID(s, "workout-"+v.ID)
	if !ok {
		t.Fatal("no task")
	}
	stored := task.Prompt
	if _, err := d.HealthStrengthSetAdd(ctx, []assistantwire.StrengthSet{{ID: strengthReportTestID("just-before-run"), ExerciseID: e.ID, Weight: 47.5, Reps: 9, At: v.End.Add(-time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.runTask(task, time.Now(), time.Now(), true, false); err != nil {
		t.Fatal(err)
	}
	text, _ := f.lastSend(t)["text"].(string)
	if !strings.Contains(text, `"weight":47.5`) || !strings.Contains(text, e.Name) {
		t.Fatal("execution missed just-logged set", text)
	}
	current, _ := taskByID(s, task.ID)
	if current.Prompt != stored {
		t.Fatal("execution changed durable fallback")
	}
	fault := &workoutReportFault{Layer: s.healthMod, fail: true}
	s.healthMod = fault
	fallback, day := s.workoutPromptNow(ctx, task)
	if day != "" {
		t.Fatal("failed assembly selected a training day")
	}
	if fallback.Prompt != stored {
		t.Fatal("assembly failure lost fallback")
	}
	// Run the actual scheduler with the fault, then with the module absent.
	for _, absent := range []bool{false, true} {
		if absent {
			s.healthMod = nil
		}
		task.ID = "fallback-" + randomHex(4)
		setTasks(s, task)
		if _, err := s.runTask(task, time.Now(), time.Now(), true, true); err != nil {
			t.Fatal(err)
		}
		text, _ = f.lastSend(t)["text"].(string)
		if !strings.Contains(text, stored) || strings.Contains(text, `"weight":47.5`) {
			t.Fatal("scheduler did not use stored fallback", text)
		}
	}
	running := sampleWorkout()
	raw, _ := json.Marshal(running)
	plain := Task{Space: SpaceHealth, WorkoutID: running.ID, Prompt: string(raw)}
	if got, day := s.workoutPromptNow(ctx, plain); got.Prompt != plain.Prompt || day != "" {
		t.Fatal("ordinary report changed")
	}
}

func TestStrengthWorkoutAbsentModuleKeepsStoredReport(t *testing.T) {
	s, f, _ := healthExecutionService(t)
	s.healthMod = nil
	v := sampleWorkout()
	v.ID = strengthReportTestID("absent-module-report")
	v.Activity = assistantwire.WorkoutActivityStrength
	if err := s.acceptWorkout(context.Background(), v); err != nil {
		t.Fatal(err)
	}
	task, ok := taskByID(s, "workout-"+v.ID)
	if !ok {
		t.Fatal("missing legacy task")
	}
	stored := task.Prompt
	if _, err := s.runTask(task, time.Now(), time.Now(), true, false); err != nil {
		t.Fatal(err)
	}
	text, _ := f.lastSend(t)["text"].(string)
	if !strings.Contains(text, stored) || strings.Contains(text, "Sets with previous session") {
		t.Fatal("absent module changed report", text)
	}
}
