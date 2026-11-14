package assistant

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Regression for #7619: an open supervised todo in a leader conversation must not shorten an interval
// follow-up. The task runs on its configured `every`, the views show that schedule, and the run prompt
// states the configured interval. The former leader cadence (#3811, #3870, #4014) fired such tasks every
// 10 minutes while a supervised todo was open.

// workingSupervisedTodo seeds an open todo supervised by "leader" with a working agent.
func workingSupervisedTodo(f *fakeHub, id string) Todo {
	f.setSessions("box", proto.Session{SessionID: "agent", Status: proto.StatusWorking, State: proto.StateWorking})
	return Todo{ID: id, Supervision: &Supervision{Supervisor: "leader"}, Agents: []TodoAgent{{MachineID: "box", SessionID: "agent"}}}
}

func TestIntervalFollowupKeepsConfiguredCadenceUnderOpenSupervision(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for _, minutes := range []int{30, 60} {
		t.Run(ruleName(minutes), func(t *testing.T) {
			f := newFakeHub()
			f.settings = &Settings{Main: "main", Leaders: []string{"leader"}, Timezone: "UTC"}
			s := newStorelessService(t, f)
			firstAt := now.Add(time.Duration(minutes) * time.Minute)
			setTasks(s, Task{ID: "fu_patrol", Title: "巡检", Prompt: "跑 detect.sh", Repeat: repeatInterval, EveryMinutes: minutes,
				At: firstAt, CreatedAt: now, Enabled: true, SessionID: "leader"})
			putTodo(s, workingSupervisedTodo(f, "todo_open"))

			// A brand-new task with its first run in the future is not pulled to "now" or to ten minutes.
			if got := s.nextDue(ctx, now); !got.Equal(firstAt) {
				t.Fatalf("next run must stay on the configured schedule: got %v, want %v", got, firstAt)
			}

			// Nothing fires at the old accelerated ten-minute mark.
			s.runDue(ctx, now.Add(10*time.Minute))
			s.sched.inflight.Wait()
			if task, _ := taskByID(s, "fu_patrol"); !task.LastRun.IsZero() {
				t.Fatalf("the task must not run ten minutes in: %+v", task)
			}
			if got := s.nextDue(ctx, now.Add(10*time.Minute)); !got.Equal(firstAt) {
				t.Fatalf("next run moved without a dispatch: got %v, want %v", got, firstAt)
			}

			// It fires at its configured time and then advances by the configured interval.
			s.runDue(ctx, firstAt)
			s.sched.inflight.Wait()
			wantNext := firstAt.Add(time.Duration(minutes) * time.Minute)
			task, _ := taskByID(s, "fu_patrol")
			if !task.LastRun.Equal(firstAt) || !task.At.Equal(wantNext) || task.EveryMinutes != minutes {
				t.Fatalf("the run must keep the configured interval: %+v", task)
			}
			if got := s.nextDue(ctx, firstAt); !got.Equal(wantNext) {
				t.Fatalf("next run after the dispatch: got %v, want %v", got, wantNext)
			}

			// The next run is another full interval away, not ten minutes.
			s.runDue(ctx, firstAt.Add(10*time.Minute))
			s.sched.inflight.Wait()
			if task, _ = taskByID(s, "fu_patrol"); !task.LastRun.Equal(firstAt) {
				t.Fatalf("the task must not run again ten minutes later: %+v", task)
			}
		})
	}
}

func TestIntervalFollowupViewsShowConfiguredSchedule(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	f := newFakeHub()
	f.settings = &Settings{Main: "main", Leaders: []string{"leader"}, Timezone: "UTC"}
	s := newStorelessService(t, f)
	originalAt := now.Add(3 * time.Hour)
	setTasks(s, Task{ID: "fu_patrol", Prompt: "inspect", Repeat: repeatInterval, EveryMinutes: 180,
		At: originalAt, LastRun: now, Enabled: true, SessionID: "leader"})
	putTodo(s, workingSupervisedTodo(f, "todo_open"))

	views := s.tasksView(ctx, nil)
	if len(views) != 1 || !views[0].NextRun.Equal(originalAt) || !views[0].At.Equal(originalAt) || views[0].EveryMinutes != 180 {
		t.Fatalf("the API view must show the configured schedule: %+v", views)
	}
	snapshots := s.taskViews(ctx)
	if len(snapshots) != 1 || !snapshots[0].At.Equal(originalAt) || snapshots[0].EveryMinutes != 180 {
		t.Fatalf("the snapshot must show the configured schedule: %+v", snapshots)
	}
}

func TestIntervalFollowupRunPromptStatesConfiguredInterval(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		minutes int
		want    string
	}{
		{30, "每 30 分钟"},
		{180, "每 3 小时"},
	} {
		t.Run(ruleName(tt.minutes), func(t *testing.T) {
			f := readyHub()
			f.settings = &Settings{Main: "main", Leaders: []string{"leader"}, Timezone: "UTC"}
			s := newStorelessService(t, f)
			setTasks(s, Task{ID: "fu_patrol", Title: "巡检", Prompt: "巡检", Repeat: repeatInterval, EveryMinutes: tt.minutes,
				At: now.Add(time.Duration(tt.minutes) * time.Minute), Enabled: true, MachineID: "server", SessionID: "leader"})
			putTodo(s, workingSupervisedTodo(f, "todo_open"))
			task, _ := taskByID(s, "fu_patrol")
			if _, err := s.runTask(task, now, now, false, false); err != nil {
				t.Fatal(err)
			}
			text := f.lastSend(t)["text"].(string)
			if !strings.Contains(text, tt.want) || strings.Contains(text, "每 10 分钟") {
				t.Fatalf("the run must be told its real configured interval %q, not ten minutes: %q", tt.want, text)
			}
			if strings.Contains(text, "没有变化就立即结束，不做长汇报") {
				t.Fatalf("the accelerated-run note must be gone: %q", text)
			}
			if got := stripScheduleHeader(text[strings.Index(text, "⏰"):]); got != "巡检" {
				t.Fatalf("the run's own prompt must survive without accumulated schedule notes: %q", got)
			}
		})
	}
}

func TestIntervalFollowupCadenceDoesNotAffectOtherSchedules(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		task Task
		want time.Time
	}{
		{name: "once", task: Task{ID: "task", Prompt: "inspect", Repeat: repeatOnce,
			At: now.Add(3 * time.Hour), Enabled: true, SessionID: "leader"}, want: now.Add(3 * time.Hour)},
		{name: "daily", task: Task{ID: "task", Prompt: "inspect", Repeat: repeatDaily, Time: "09:00",
			Enabled: true, SessionID: "leader"}, want: time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC)},
		{name: "disabled", task: Task{ID: "task", Prompt: "inspect", Repeat: repeatInterval, EveryMinutes: 30,
			At: now.Add(30 * time.Minute), Enabled: false, SessionID: "leader"}, want: time.Time{}},
		{name: "no source conversation", task: Task{ID: "task", Prompt: "inspect", Repeat: repeatInterval, EveryMinutes: 30,
			At: now.Add(30 * time.Minute), Enabled: true}, want: now.Add(30 * time.Minute)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeHub()
			f.settings = &Settings{Main: "main", Leaders: []string{"leader"}, Timezone: "UTC"}
			s := newStorelessService(t, f)
			tt.task.CreatedAt = now
			setTasks(s, tt.task)
			putTodo(s, workingSupervisedTodo(f, "todo_open"))
			if got := s.nextDue(ctx, now); !got.Equal(tt.want) {
				t.Fatalf("next run: got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIntervalFollowupPostgresHonorsConfiguredInterval(t *testing.T) {
	ctx := context.Background()
	database := scheduleStore(t)
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	seedTodos(t, database, db.TodoRow{ID: "cadence_todo", Title: "Inspect work"})
	if err := database.PutTodoSupervision(ctx, db.TodoSupervisionRow{TodoID: "cadence_todo", Supervisor: "leader", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.LinkTodoAgentWithSource(ctx, "cadence_todo", "box", "agent", "Worker", "", "", "", "manual", "", now); err != nil {
		t.Fatal(err)
	}
	f := newFakeHub()
	f.settings = &Settings{Main: "main", Leaders: []string{"leader"}, Timezone: "UTC"}
	f.setSessions("box", proto.Session{SessionID: "agent", Status: proto.StatusWorking, State: proto.StateWorking})
	s := newServiceOn(t, f, database)
	firstAt := now.Add(30 * time.Minute)
	sc := s.loadSchedule(ctx)
	sc.mu.Lock()
	sc.tasks = []Task{{ID: "fu_patrol", Prompt: "inspect", Repeat: repeatInterval, EveryMinutes: 30,
		At: firstAt, CreatedAt: now, Enabled: true, SessionID: "leader"}}
	if err := s.saveLocked(sc, sc.tasks); err != nil {
		t.Fatal(err)
	}
	sc.mu.Unlock()

	s.runDue(ctx, now.Add(10*time.Minute))
	s.sched.inflight.Wait()
	if got := s.nextDue(ctx, now.Add(10*time.Minute)); !got.Equal(firstAt) {
		t.Fatalf("open supervision must not move the stored schedule: got %v, want %v", got, firstAt)
	}

	s.runDue(ctx, firstAt)
	s.sched.inflight.Wait()
	task, _ := taskByID(s, "fu_patrol")
	if !task.LastRun.Equal(firstAt) || !task.At.Equal(firstAt.Add(30*time.Minute)) || task.EveryMinutes != 30 {
		t.Fatalf("the persisted task must run on its configured interval: %+v", task)
	}
}

func ruleName(minutes int) string {
	return strconv.Itoa(minutes) + "m"
}
