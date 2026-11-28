package assistant

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func pendingTodoRun(t *testing.T, s *Service, id string, at time.Time) (Task, ScheduleRun) {
	t.Helper()
	task, ok := taskByID(s, id)
	if !ok {
		t.Fatal("missing recurring task")
	}
	before, _ := s.scheduledTodoSnapshot(t.Context(), task, ScheduleRun{})
	task.todoBefore = &before
	run := ScheduleRun{RefID: task.ID, At: at, Status: runOK, MachineID: "server", SessionID: "check",
		TodoProgress: &ScheduleTodoAssessment{TodoID: task.TodoID, State: "pending"}}
	run.ID = s.recordRun(run)
	return task, run
}

func TestScheduledTodoPriorConversationsRemainExcluded(t *testing.T) {
	store := &memStore{}
	s, f := newQuietServiceOn(t, store)
	s.superviseTimes.gather = time.Hour
	todo := Todo{ID: "prior-conversations", Origin: "conv1", Supervision: &Supervision{Supervisor: "conv1"}}
	putTodo(s, todo)
	task := todoProgressTask(todo.ID)
	task.FreshEachRun = true
	task.RunMachineID, task.RunSessionID = "server", "latest-patrol"
	setTasks(s, task)
	s.originSet(t.Context(), "server", "older-patrol", OriginTask, task.ID, "")
	s.originSet(t.Context(), "server", "latest-patrol", OriginTask, task.ID, "")
	f.answerNode(DeviceSessionItems, func(string, json.RawMessage) (any, error) {
		return map[string]any{"items": []proto.Item{{Kind: proto.KindText, Role: "assistant", Text: "Checked again; no batch started"}}}, nil
	})
	assessTodoBaseline(t, s, task.ID)
	_, result := assessTodoRun(t, s, task.ID, func() {
		cur, _ := s.todoNow(todo.ID)
		cur.Agents = []TodoAgent{{MachineID: "server", SessionID: "older-patrol"}}
		putTodo(s, cur)
	})
	if result.State != "no_progress" || result.NoProgress != 1 {
		t.Fatalf("an older FreshEachRun conversation counted its own output as task progress: %+v", result)
	}
	for i := 0; i <= maxRuns; i++ {
		s.recordRun(ScheduleRun{RefID: "unrelated", Status: runOK})
	}
	cur, _ := s.todoNow(todo.ID)
	// The database holds the first manager's pending signal: the second one's timings are set before its timer is.
	s, f = newQuietServiceTimed(t, store, heldSuperviseTimings())
	putTodo(s, cur)
	f.answerNode(DeviceSessionItems, func(string, json.RawMessage) (any, error) {
		return map[string]any{"items": []proto.Item{{Kind: proto.KindText, Role: "assistant", Text: "A different patrol summary, still no batch"}}}, nil
	})
	_, result = assessTodoRun(t, s, task.ID, nil)
	if result.State != "no_progress" || result.NoProgress != 2 {
		t.Fatalf("restart or bounded history lost the old conversation's ownership: %+v", result)
	}
}

func TestScheduledTodoUnknownBaselineDoesNotInventProgress(t *testing.T) {
	for _, initialStreak := range []bool{false, true} {
		t.Run(strconv.FormatBool(initialStreak), func(t *testing.T) {
			s, f, todo := scheduledTodoFixture(t)
			task := todoProgressTask(todo.ID)
			setTasks(s, task)
			if initialStreak {
				assessTodoBaseline(t, s, task.ID)
				assessTodoRun(t, s, task.ID, nil)
			}
			todo.Agents = []TodoAgent{{MachineID: "box", SessionID: "worker"}}
			putTodo(s, todo)
			unreadable := true
			f.answerNode(DeviceSessionItems, func(machine string, _ json.RawMessage) (any, error) {
				if machine == "box" && unreadable {
					return nil, errors.New("temporarily unreachable")
				}
				return map[string]any{"items": []proto.Item{{Kind: proto.KindText, Role: "assistant", Text: "A pre-existing artifact"}}}, nil
			})
			_, unknown := assessTodoRun(t, s, task.ID, nil)
			// The first linked run is a baseline even when it cannot read an Agent; a later one is unknown.
			if want := map[bool]string{false: "baseline", true: "unknown"}[initialStreak]; unknown.State != want {
				t.Fatalf("an unreadable baseline was not %s: %+v", want, unknown)
			}
			f.mu.Lock()
			unreadable = false
			f.mu.Unlock()
			_, recovered := assessTodoRun(t, s, task.ID, nil)
			if recovered.State == "progress" || initialStreak && recovered.NoProgress != 2 {
				t.Fatalf("revealing an unchanged, pre-existing answer fabricated progress: %+v", recovered)
			}
		})
	}
}

func TestScheduledTodoNewWorkerAfterDispatchCanMakeProgress(t *testing.T) {
	for _, beforeDispatch := range []bool{false, true} {
		t.Run(strconv.FormatBool(beforeDispatch), func(t *testing.T) {
			s, f, todo := scheduledTodoFixture(t)
			task := todoProgressTask(todo.ID)
			setTasks(s, task)
			// A different schedule's conversation is not this schedule's own patrol output.
			s.originSet(t.Context(), "box", "worker", OriginTask, "another-schedule", "")
			f.answerNode(DeviceSessionItems, func(string, json.RawMessage) (any, error) {
				return map[string]any{"items": []proto.Item{{Kind: proto.KindText, Role: "assistant", Text: "Produced a new batch"}}}, nil
			})
			assessTodoRun(t, s, task.ID, nil)
			linkWorker := func() {
				cur, _ := s.todoNow(todo.ID)
				cur.Agents = []TodoAgent{{MachineID: "box", SessionID: "worker"}}
				putTodo(s, cur)
			}
			if beforeDispatch {
				linkWorker()
			}
			_, result := assessTodoRun(t, s, task.ID, func() {
				if !beforeDispatch {
					linkWorker()
				}
			})
			if result.State != "progress" || result.NoProgress != 0 {
				t.Fatalf("a real worker's new output was mistaken for old patrol output: %+v", result)
			}
		})
	}
}

func TestScheduledTodoPartialBaselineRetainsKnownEvidence(t *testing.T) {
	s, f, todo := scheduledTodoFixture(t)
	todo.Agents = []TodoAgent{{MachineID: "box", SessionID: "readable"}, {MachineID: "box", SessionID: "unreadable"}}
	putTodo(s, todo)
	answer := "An existing artifact"
	f.answerNode(DeviceSessionItems, func(machine string, raw json.RawMessage) (any, error) {
		var args struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(raw, &args)
		if machine == "box" && args.SessionID == "unreadable" {
			return nil, errors.New("temporarily unreachable")
		}
		return map[string]any{"items": []proto.Item{{Kind: proto.KindText, Role: "assistant", Text: answer}}}, nil
	})
	task := todoProgressTask(todo.ID)
	setTasks(s, task)
	assessTodoRun(t, s, task.ID, nil)
	_, result := assessTodoRun(t, s, task.ID, func() {
		f.mu.Lock()
		answer = "A new artifact from the readable worker"
		f.mu.Unlock()
	})
	if result.State != "progress" || result.NoProgress != 0 {
		t.Fatalf("an unreadable peer hid a real change against known Agent evidence: %+v", result)
	}
}

func TestScheduledTodoKnownReportSurvivesUnreadableAgent(t *testing.T) {
	s, f, todo := scheduledTodoFixture(t)
	todo.Agents = []TodoAgent{{MachineID: "box", SessionID: "worker"}}
	putTodo(s, todo)
	unreadable := false
	f.answerNode(DeviceSessionItems, func(machine string, _ json.RawMessage) (any, error) {
		if machine == "box" && unreadable {
			return nil, errors.New("temporarily unreachable")
		}
		return map[string]any{"items": []proto.Item{{Kind: proto.KindText, Role: "assistant", Text: "An existing artifact"}}}, nil
	})
	task := todoProgressTask(todo.ID)
	setTasks(s, task)
	assessTodoRun(t, s, task.ID, nil)
	f.mu.Lock()
	unreadable = true
	f.mu.Unlock()
	_, result := assessTodoRun(t, s, task.ID, func() {
		s.noteProgress(t.Context(), todo.ID, TaskProgress{Kind: upReportProgress, Text: "Finished a new batch", At: time.Now()})
	})
	if result.State != "progress" || result.NoProgress != 0 {
		t.Fatalf("an unrelated Agent read failure hid an explicit new progress report: %+v", result)
	}
}

func TestScheduledTodoDelayedHistoryReplayPreservesNewerAssessment(t *testing.T) {
	for _, mode := range []string{"later", "same time", "same time restart", "same time evicted"} {
		t.Run(mode, func(t *testing.T) {
			store := &scheduleWriteStore{Store: &memStore{}}
			s, _ := newQuietServiceOn(t, store)
			s.superviseTimes.gather = time.Hour
			s.todoAskAfter = time.Nanosecond
			todo := Todo{ID: "delayed-replay", Origin: "conv1", Supervision: &Supervision{Supervisor: "conv1"}}
			putTodo(s, todo)
			task := todoProgressTask(todo.ID)
			setTasks(s, task)
			at := time.Now().UTC()
			baselineTask, baseline := pendingTodoRun(t, s, task.ID, at.Add(-time.Minute))
			s.finishScheduledTodo(t.Context(), baselineTask, baseline)
			firstTask, first := pendingTodoRun(t, s, task.ID, at)
			store.failWrites(runsKey)
			s.finishScheduledTodo(t.Context(), firstTask, first)
			store.failWrites("")
			if mode == "later" {
				at = at.Add(time.Minute)
			}
			nextTask, next := pendingTodoRun(t, s, task.ID, at)
			s.finishScheduledTodo(t.Context(), nextTask, next)
			current, _ := taskByID(s, task.ID)
			if current.TodoProgress.Run != next.ID || current.TodoProgress.NoProgress != 2 {
				t.Fatalf("a distinct physical run at the same time was lost: %+v", current.TodoProgress)
			}
			if strings.HasSuffix(mode, "evicted") {
				for i := 0; i <= maxRuns; i++ {
					s.recordRun(ScheduleRun{RefID: "unrelated", Status: runOK})
				}
			}
			if strings.HasSuffix(mode, "restart") {
				cur, _ := s.todoNow(todo.ID)
				s, _ = newQuietServiceTimed(t, store, heldSuperviseTimings())
				s.todoAskAfter = time.Nanosecond
				putTodo(s, cur)
			}
			s.finishScheduledTodo(t.Context(), firstTask, first)
			current, _ = taskByID(s, task.ID)
			cur, _ := s.todoNow(todo.ID)
			if current.TodoProgress.Run != next.ID || current.TodoProgress.NoProgress != 2 || len(cur.Supervision.Reviews) != 1 {
				t.Fatalf("a delayed history replay replaced or double-counted newer evidence: %+v %+v",
					current.TodoProgress, cur.Supervision)
			}
			_, runs := scheduleState(s)
			i := slices.IndexFunc(runs, func(r ScheduleRun) bool { return r.ID == first.ID })
			wantState, wantCount := "no_progress", 1
			if strings.HasSuffix(mode, "evicted") {
				// Only an evicted assessment has to be reconstructed as superseded.
				wantState, wantCount = "superseded", 0
			}
			if i < 0 || runs[i].TodoProgress.State != wantState || runs[i].TodoProgress.NoProgress != wantCount {
				t.Fatalf("the old replay lost its committed result or counted again: %+v", runs)
			}
		})
	}
}

func TestScheduledTodoFailedAttemptsDoNotCountAsNoProgress(t *testing.T) {
	s, f, todo := scheduledTodoFixture(t)
	s.runRetry = time.Millisecond
	f.mu.Lock()
	reads := 0
	f.page = func(int) []proto.Item {
		reads++
		return []proto.Item{{ID: strconv.Itoa(reads), Kind: proto.KindText, Role: "assistant",
			Text: "API Error: 503 upstream attempt budget is exhausted"}}
	}
	f.onAssistantSend = func(f *fakeHub, args map[string]any) {
		sid, _ := args["session_id"].(string)
		if sid == "" {
			sid, _ = args["new_session_id"].(string)
		}
		if sid == "" {
			sid = "s" + strconv.Itoa(f.made+1)
		}
		f.sessions["server"] = append(f.sessions["server"], proto.Session{MachineID: "server",
			SessionID: sid, Status: proto.StatusIdle, State: proto.StateIdle})
	}
	f.mu.Unlock()
	if _, err := runJSON(t, s, "conv1", "schedule_followup", map[string]any{
		"title": "Backfill check", "prompt": "Advance the backfill", "todo_id": todo.ID,
		"every": "30m", "new_conversation": true, "push": "asked",
	}); err != nil {
		t.Fatal(err)
	}
	tasks, _ := scheduleState(s)
	if _, err := runNow(t, s, tasks[0].ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the failed attempt and its one automatic retry to finish", func() bool {
		_, runs := scheduleState(s)
		return len(runs) == 2 && slices.ContainsFunc(runs, func(r ScheduleRun) bool { return r.Retried }) &&
			!slices.ContainsFunc(runs, func(r ScheduleRun) bool {
				return r.Status != runFailed || r.TodoProgress == nil || r.TodoProgress.State == "pending"
			})
	})
	current, _ := taskByID(s, tasks[0].ID)
	cur, _ := s.todoNow(todo.ID)
	_, runs := scheduleState(s)
	if current.TodoProgress != nil || cur.Supervision.State == reviewNeedsUser {
		t.Fatalf("one API failure and its retry became two no-progress checks: %+v %+v", current.TodoProgress, cur.Supervision)
	}
	for _, run := range runs {
		if run.TodoProgress.State != "not_run" || run.TodoProgress.Reason == "" {
			t.Fatalf("a runtime failure was treated as an evidence assessment: %+v", run)
		}
	}
}

func TestScheduledTodoEvictedPendingRunStillAssessed(t *testing.T) {
	store := &memStore{}
	s, _ := newQuietServiceOn(t, store)
	s.superviseTimes.gather = time.Hour
	s.todoAskAfter = time.Nanosecond
	todo := Todo{ID: "evicted-pending", Origin: "conv1", Supervision: &Supervision{Supervisor: "conv1"}}
	putTodo(s, todo)
	task := todoProgressTask(todo.ID)
	setTasks(s, task)
	assessTodoBaseline(t, s, task.ID)
	for count := 1; count <= 2; count++ {
		dispatched, run := pendingTodoRun(t, s, task.ID, time.Now().UTC())
		for i := 0; i <= maxRuns; i++ {
			s.recordRun(ScheduleRun{RefID: "unrelated", Status: runOK})
		}
		s.finishScheduledTodo(t.Context(), dispatched, run)
		current, _ := taskByID(s, task.ID)
		if current.TodoProgress == nil || current.TodoProgress.Run != run.ID || current.TodoProgress.NoProgress != count {
			t.Fatalf("bounded history prevented a completed run's durable assessment: %+v", current.TodoProgress)
		}
		var runs []ScheduleRun
		if _, err := store.Setting(t.Context(), runsKey, &runs); err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(runs, func(r ScheduleRun) bool { return r.ID == run.ID })
		if len(runs) > maxRuns || i < 0 || runs[i].TodoProgress.State != "no_progress" {
			t.Fatal("recovering the run lost its identity, assessment, or history bound")
		}
		s.finishScheduledTodo(t.Context(), dispatched, run)
		current, _ = taskByID(s, task.ID)
		if current.TodoProgress.NoProgress != count {
			t.Fatal("replaying a recovered run counted it twice")
		}
	}
	cur, _ := s.todoNow(todo.ID)
	if cur.Supervision.State != reviewNeedsUser || len(cur.Supervision.Reviews) != 1 {
		t.Fatalf("two evicted no-progress runs never escalated: %+v", cur.Supervision)
	}
}
