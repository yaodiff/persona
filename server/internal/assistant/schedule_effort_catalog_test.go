package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The shell and the Agent engine deliberately share a launcher name but not capabilities.
func scheduledCatalogFixture(t *testing.T, assistantEfforts []string) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.ownModels = true // No HTTP model server: every exercised boundary is the fake node RPC.
	f.launcherList = []proto.Launcher{{Name: "shared", Source: "zshrc", Agent: "claude"}}
	s := newTestService(t, f)
	// Workout admission appends to the schedule; unrelated default routines must not dispatch in this fixture.
	setTasks(s)
	f.answerNode(assistantwire.DeviceLaunchersModels, func(_ string, raw json.RawMessage) (any, error) {
		var p struct{ Purpose string }
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		model, efforts := "engine-model", []string{"low", "high", "max"}
		if p.Purpose == "assistant" {
			model, efforts = "shell-model", assistantEfforts
		}
		return proto.LauncherModels{Agent: "claude", Models: []proto.AgentModel{
			{ID: "default", Resolved: model, Default: true, Efforts: efforts},
			{ID: model, Efforts: efforts},
		}}, nil
	})
	// Visiting New Agent before the scheduled run must not select that catalog or cache partition.
	if !s.launcherModelSet(t.Context(), "server", "shared")["engine-model"] {
		t.Fatal("Agent catalog was not primed")
	}
	return s, f
}

func TestScheduledAssistantUsesExecutionCatalog(t *testing.T) {
	for _, entry := range []string{"due", "retry", "workout", "edit"} {
		for _, model := range []string{"", "default", "shell-model"} {
			t.Run(entry+"/"+model, func(t *testing.T) {
				s, f := scheduledCatalogFixture(t, []string{"low", "medium", "high", "xhigh", "max"})
				binding := ModelBinding{Provider: "launcher.shared", Model: model, Effort: "xhigh"}
				setModel(t, s, "assistant", binding)
				task := Task{ID: "report", Title: "Report", Prompt: "Give the report", Repeat: repeatOnce, At: time.Now(), Enabled: true}
				if entry == "workout" {
					v := sampleWorkout()
					raw, _ := json.Marshal(v)
					r := request(s, http.MethodPost, "/api/v1/assistant/health/workouts", string(raw))
					if r.Code != http.StatusOK {
						t.Fatalf("workout admission: %d %s", r.Code, r.Body)
					}
					var ok bool
					task, ok = taskByID(s, "workout-"+v.ID)
					if !ok {
						t.Fatal("admitted workout has no task")
					}
				} else {
					setTasks(s, task)
				}
				if entry == "edit" {
					body, _ := json.Marshal(map[string]any{"title": "Report", "prompt": "Give the report", "repeat": "interval", "every_minutes": 30, "enabled": true,
						"launcher": "shared", "model": model, "effort": "xhigh", "model_revision": 0})
					r := request(s, http.MethodPut, "/api/v1/assistant/tasks/report", string(body))
					if r.Code != http.StatusOK {
						t.Fatalf("valid task edit refused: %d %s", r.Code, r.Body)
					}
					task, _ = taskByID(s, task.ID)
				}
				if entry == "retry" || entry == "edit" {
					if _, err := s.runTask(task, task.At, time.Now(), entry == "edit", entry == "retry"); err != nil {
						t.Fatalf("retry refused before dispatch: %v", err)
					}
				} else {
					s.runDue(t.Context(), time.Now().Add(time.Second))
				}
				runs := waitRuns(t, s, 1)
				if len(runs) != 1 || runs[0].RefID != task.ID || runs[0].Status != runOK || f.sent() != 1 {
					t.Fatalf("scheduled dispatch: runs=%+v sends=%d", runs, f.sent())
				}
				got := f.lastSend(t)
				if got["launcher"] != "shared" || got["effort"] != "xhigh" || model != "" && got["model"] != model || model == "" && got["model"] != nil {
					t.Fatalf("requested binding changed: %v", got)
				}
				if current, err := s.resolveModel(t.Context(), "assistant"); err != nil || current != binding {
					t.Fatalf("Main binding changed: %+v %v", current, err)
				}
			})
		}
	}
}

func TestScheduledForwardingCheckUsesAssistantPurpose(t *testing.T) {
	s, f := scheduledCatalogFixture(t, []string{"xhigh"})
	f.answerNode(assistantwire.DeviceLaunchersModelCheck, func(_ string, raw json.RawMessage) (any, error) {
		var p struct{ Purpose string }
		_ = json.Unmarshal(raw, &p)
		if p.Purpose != "assistant" {
			return nil, errors.New("checked the Agent engine instead of the Assistant launcher")
		}
		return map[string]string{"agent": "claude"}, nil
	})
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.shared", Model: "shell-model", Effort: "xhigh"})
	setTasks(s, Task{ID: "report", Prompt: "Report", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
	if _, err := runNow(t, s, "report"); err != nil {
		t.Fatal(err)
	}
}

func TestScheduledProviderOverrideKeepsItsOwnDefaults(t *testing.T) {
	s, f := scheduledCatalogFixture(t, []string{"xhigh"})
	f.mu.Lock()
	f.launcherList = append(f.launcherList, proto.Launcher{Name: "other", Source: "zshrc", Agent: "claude"})
	f.mu.Unlock()
	s.invalidateLauncherListCache("server")
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.shared", Model: "shell-model", Effort: "xhigh"})
	setTasks(s, Task{ID: "report", Prompt: "Report", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
	setModel(t, s, "task.report", ModelBinding{Provider: "launcher.other"})
	if _, err := runNow(t, s, "report"); err != nil {
		t.Fatal(err)
	}
	got := f.lastSend(t)
	model, _ := got["model"].(string)
	effort, _ := got["effort"].(string)
	if got["launcher"] != "other" || model != "" || effort != "" {
		t.Fatalf("task inherited another provider's parameters: %v", got)
	}
}

func TestScheduledConfigurationFailureIsNotRetried(t *testing.T) {
	for _, kind := range []string{"one-off", "workout", "facts", "report"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "facts" || kind == "report" { // the fallback notice is the briefing's
				needsBriefingModule(t)
			}
			synctest.Test(t, func(t *testing.T) {
				s, f := scheduledCatalogFixture(t, []string{"low"})
				initBriefFallbackState(s)
				setSettings(t, s, func(v *Settings) { v.Main = "main" })
				setModel(t, s, "assistant", ModelBinding{Provider: "launcher.shared", Effort: "xhigh"})
				s.runRetry = time.Minute
				task := Task{ID: "report", Title: "Report", Prompt: "Report", Repeat: repeatOnce, Enabled: true, At: time.Now(), Facts: kind == "facts", Report: kind == "report"}
				if task.Facts || task.Report {
					task.Repeat, task.EveryMinutes = repeatInterval, 30
				}
				if kind == "workout" {
					if err := s.acceptWorkout(t.Context(), sampleWorkout()); err != nil {
						t.Fatal(err)
					}
				} else {
					setTasks(s, task)
				}
				s.runDue(t.Context(), time.Now().Add(time.Second))
				synctest.Wait()
				time.Sleep(2 * s.runRetry) // sleep-ok: advances synctest's virtual clock beyond the retry deadline
				synctest.Wait()
				runs := waitRuns(t, s, 1)
				if len(runs) != 1 || runs[0].Status != runFailed || runs[0].Retried || runs[0].SessionID != "" || f.sent() != 0 {
					t.Fatalf("configuration failure created another attempt: runs=%+v sends=%d", runs, f.sent())
				}
				if task.Facts || task.Report {
					if got := len(f.pushed()); got != 1 {
						t.Fatalf("briefing fallback notices=%d, want one", got)
					}
				} else {
					if got := len(f.pushed()); got != 0 {
						t.Fatalf("non-briefing schedule_failed pushed=%d, want 0", got)
					}
					if !hasRelayFrom(s, "main", cardScheduleFailed, "") {
						t.Fatalf("non-briefing schedule_failed did not route to main: %+v", laterFor(s, "main"))
					}
				}
			})
		})
	}
}

func TestScheduledTransientFailureStillRetries(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := scheduledCatalogFixture(t, []string{"xhigh"})
		setModel(t, s, "assistant", ModelBinding{Provider: "launcher.shared", Effort: "xhigh"})
		// Similar provider prose is not a typed pre-dispatch configuration refusal.
		failure := i18n.Err("assistant-models.err_effort_unsupported").Error()
		f.failAssistant = errors.New(failure)
		s.runRetry = time.Minute
		setTasks(s, Task{ID: "report", Prompt: "Report", Repeat: repeatOnce, Enabled: true, At: time.Now()})
		s.runDue(context.Background(), time.Now().Add(time.Second))
		synctest.Wait()
		time.Sleep(2 * s.runRetry) // sleep-ok: advances synctest's virtual clock through the permitted retry
		synctest.Wait()
		runs := waitRuns(t, s, 2)
		if len(runs) != 2 || !runs[1].Retried || slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.Error != failure }) {
			t.Fatalf("transient attempts: %+v", runs)
		}
	})
}
