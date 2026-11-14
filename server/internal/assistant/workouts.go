package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
)

// WorkoutReport contains aggregates only: no route coordinates or raw HealthKit records.
type WorkoutReport struct {
	ID        string             `json:"id"`
	Title     string             `json:"title"`
	Activity  string             `json:"activity"`
	Source    string             `json:"source"`
	Start     time.Time          `json:"start"`
	End       time.Time          `json:"end"`
	EnabledAt time.Time          `json:"enabled_at"`
	Duration  float64            `json:"duration"`
	Distance  *float64           `json:"distance,omitempty"`
	Energy    *float64           `json:"energy,omitempty"`
	HeartRate []WorkoutHeartRate `json:"heart_rate,omitempty"`
	// Since #2526's second step; an older phone sends none of them.
	ElevationGain *float64       `json:"elevation_gain,omitempty"` // meters climbed, from the workout's own metadata
	HeartRateAvg  *float64       `json:"heart_rate_avg,omitempty"` // bpm over the whole workout
	HeartRateMax  *float64       `json:"heart_rate_max,omitempty"`
	Splits        []WorkoutSplit `json:"splits,omitempty"` // whole kilometres in order, pauses left out
}
type WorkoutHeartRate struct {
	Minute int     `json:"minute"`
	BPM    float64 `json:"bpm"`
}

// WorkoutSplit is the time one whole kilometre took, the first being kilometre 1.
type WorkoutSplit struct {
	KM      int     `json:"km"`
	Seconds float64 `json:"seconds"`
}

// workoutMaxSplits bounds the splits a report may carry: a 1000 km ride is past any single workout.
const workoutMaxSplits = 1000

var workoutUUID = lazyre.New(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// workoutClockSkew bounds how far past the hub's clock a workout may end. The phone drops a report the hub
// answers 400, so the bound only rejects data that cannot be a finished workout, never ordinary clock drift.
const workoutClockSkew = 24 * time.Hour

// validate failures are permanent: the phone drops the report instead of retrying it.
func (v WorkoutReport) validate(now time.Time) error {
	if !workoutUUID.MatchString(v.ID) || v.EnabledAt.IsZero() || v.Start.Before(v.EnabledAt) || !v.End.After(v.Start) || v.End.After(now.Add(workoutClockSkew)) || v.Duration < 600 || v.Duration > v.End.Sub(v.Start).Seconds()+1 || v.End.Sub(v.Start) > 7*24*time.Hour || len(v.Title) > 200 || len(v.Source) > 256 || len(v.Activity) > 80 || len(v.HeartRate) > 10080 {
		return errors.New("invalid workout")
	}
	for _, n := range []*float64{&v.Duration, v.Distance, v.Energy, v.ElevationGain} {
		if n != nil && (math.IsNaN(*n) || math.IsInf(*n, 0) || *n < 0) {
			return errors.New("invalid measurement")
		}
	}
	if v.ElevationGain != nil && *v.ElevationGain > 20000 {
		return errors.New("invalid measurement")
	}
	for _, n := range []*float64{v.HeartRateAvg, v.HeartRateMax} {
		if n != nil && (math.IsNaN(*n) || math.IsInf(*n, 0) || *n <= 0 || *n > 300) {
			return errors.New("invalid heart rate")
		}
	}
	if v.HeartRateAvg != nil && v.HeartRateMax != nil && *v.HeartRateAvg > *v.HeartRateMax {
		return errors.New("invalid heart rate")
	}
	if len(v.Splits) > workoutMaxSplits {
		return errors.New("invalid splits")
	}
	var total float64
	for i, sp := range v.Splits {
		if sp.KM != i+1 || math.IsNaN(sp.Seconds) || math.IsInf(sp.Seconds, 0) || sp.Seconds <= 0 {
			return errors.New("invalid splits")
		}
		total += sp.Seconds
	}
	if total > v.End.Sub(v.Start).Seconds()+1 {
		return errors.New("invalid splits")
	}
	last := -1
	for _, p := range v.HeartRate {
		if p.Minute <= last || p.Minute > int(v.End.Sub(v.Start).Minutes()) || p.BPM <= 0 || p.BPM > 300 || math.IsNaN(p.BPM) || math.IsInf(p.BPM, 0) {
			return errors.New("invalid heart rate series")
		}
		last = p.Minute
	}
	return nil
}
func (s *Service) keepWorkoutReceipt(ctx context.Context, t Task) error {
	if t.WorkoutID == "" {
		return nil
	}
	st := s.store()
	if st == nil {
		return errors.New("workout storage unavailable")
	}
	return st.PutSetting(ctx, "health.workout.receipt."+t.WorkoutID, true)
}

// The task and receipt are written before acknowledgment. A failed receipt write leaves the durable task
// as a second deduplication key; pruning and deletion must preserve its receipt first.
func (s *Service) acceptWorkout(ctx context.Context, v WorkoutReport) error {
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		return err
	}
	defer sc.mu.Unlock()
	st := s.store()
	if st == nil {
		return errors.New("workout storage unavailable")
	}
	v.ID = strings.ToLower(v.ID)
	var accepted bool
	if _, err := st.Setting(ctx, "health.workout.receipt."+v.ID, &accepted); err != nil {
		return err
	}
	if accepted {
		return nil
	}
	id := "workout-" + v.ID
	if i := sc.indexLocked(id); i >= 0 {
		return s.keepWorkoutReceipt(ctx, sc.tasks[i])
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	task := Task{ID: id, WorkoutID: v.ID, Title: v.Title, Prompt: prompts.RenderWorkoutReport(string(data)), Repeat: repeatOnce, At: now, Enabled: true, Space: SpaceHealth, CreatedBy: "device", CreatedAt: now}
	if err := s.saveLocked(sc, append(slices.Clone(sc.tasks), task)); err != nil {
		return err
	}
	return s.keepWorkoutReceipt(ctx, task)
}
func (s *Service) workoutRoutes() {
	s.handle("POST /api/v1/assistant/health/workouts", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var v WorkoutReport
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&v); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if err := v.validate(time.Now()); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if err := s.acceptWorkout(r.Context(), v); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accepted":true}`))
	})
}
