package assistant

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func todoProgressTask(todoID string) Task {
	return Task{ID: "progress-check", Title: "Backfill check", Prompt: "Advance the backfill",
		TodoID: todoID, Repeat: repeatInterval, EveryMinutes: 30, Enabled: true, Push: pushAsked}
}

func assessTodoRun(t *testing.T, s *Service, id string, change func()) (ScheduleRun, ScheduleTodoAssessment) {
	t.Helper()
	task, _ := taskByID(s, id)
	before, _ := s.scheduledTodoSnapshot(t.Context(), task, ScheduleRun{})
	task.todoBefore = &before
	run := ScheduleRun{RefID: task.ID, At: time.Now().UTC(), Status: runOK, MachineID: "server", SessionID: "check",
		TodoProgress: &ScheduleTodoAssessment{TodoID: task.TodoID, State: "pending"}}
	run.ID = s.recordRun(run)
	if change != nil {
		change()
	}
	s.finishScheduledTodo(t.Context(), task, run)
	_, runs := scheduleState(s)
	for _, r := range runs {
		if r.ID == run.ID && r.TodoProgress != nil {
			return run, *r.TodoProgress
		}
	}
	t.Fatal("missing assessment")
	return run, ScheduleTodoAssessment{}
}

// assessTodoBaseline runs task id's first linked check, which only sets the baseline the next one compares with.
func assessTodoBaseline(t *testing.T, s *Service, id string) {
	t.Helper()
	if _, result := assessTodoRun(t, s, id, nil); result.State != "baseline" || result.NoProgress != 0 {
		t.Fatalf("the first linked run was more than a baseline: %+v", result)
	}
}

func TestScheduledTodoEvidenceAndStreakReset(t *testing.T) {
	for _, kind := range []string{"answer", "criterion", "explicit report"} {
		t.Run(kind, func(t *testing.T) {
			s, f, todo := scheduledTodoFixture(t)
			todo.Agents = []TodoAgent{{MachineID: "box", SessionID: "worker"}}
			putTodo(s, todo)
			answer := "An existing result"
			f.answerNode(DeviceSessionItems, func(machine string, _ json.RawMessage) (any, error) {
				text := "Nothing was started because execution slots are busy."
				if machine == "box" {
					text = answer
				}
				return map[string]any{"items": []proto.Item{{Kind: proto.KindText, Role: "assistant", Text: text}}}, nil
			})
			task := todoProgressTask(todo.ID)
			setTasks(s, task)
			assessTodoBaseline(t, s, task.ID)
			_, first := assessTodoRun(t, s, task.ID, nil)
			if first.State != "no_progress" || first.NoProgress != 1 {
				t.Fatalf("first: %+v", first)
			}
			_, moved := assessTodoRun(t, s, task.ID, func() {
				switch kind {
				case "answer":
					f.mu.Lock()
					answer = "Produced a new backfill artifact."
					f.mu.Unlock()
				case "criterion":
					cur, _ := s.todoNow(todo.ID)
					cur.Supervision.Criteria[0].State = "met"
					putTodo(s, cur)
				case "explicit report":
					s.noteProgress(t.Context(), todo.ID, TaskProgress{Kind: upReportProgress, Text: "Backfilled two batches", Step: 2, Steps: 10, At: time.Now()})
				}
			})
			if moved.State != "progress" || moved.NoProgress != 0 {
				t.Fatalf("%s did not reset the streak: %+v", kind, moved)
			}
			_, next := assessTodoRun(t, s, task.ID, nil)
			if next.State != "no_progress" || next.NoProgress != 1 {
				t.Fatalf("streak did not restart: %+v", next)
			}
			cur, _ := s.todoNow(todo.ID)
			if cur.Supervision.State == reviewNeedsUser {
				t.Fatal("nonconsecutive no-progress runs escalated to the owner")
			}
		})
	}
}

func TestScheduledTodoRepeatedReportAndBookkeepingAreNotProgress(t *testing.T) {
	s, _, todo := scheduledTodoFixture(t)
	task := todoProgressTask(todo.ID)
	setTasks(s, task)
	report := TaskProgress{Kind: upReportProgress, Text: "Completed two batches", Step: 2, Steps: 10, At: time.Now()}
	s.noteProgress(t.Context(), todo.ID, report)
	assessTodoBaseline(t, s, task.ID)
	assessTodoRun(t, s, task.ID, nil)
	_, result := assessTodoRun(t, s, task.ID, func() {
		cur, _ := s.todoNow(todo.ID)
		cur.UpdatedAt = time.Now().Add(time.Hour)
		cur.Notes = "Checked again; no work started."
		cur.Supervision.UpdatedAt = cur.UpdatedAt
		cur.Supervision.Criteria[0].Evidence = "The same state checked again"
		cur.Supervision.Criteria[0].At = &cur.UpdatedAt
		putTodo(s, cur)
		report.At = cur.UpdatedAt
		report.Line = "A refreshed display summary"
		s.noteProgress(t.Context(), todo.ID, report)
	})
	if result.State != "no_progress" || result.NoProgress != 2 {
		t.Fatalf("timestamp, evidence-only edit or identical report counted as progress: %+v", result)
	}
}

func TestScheduledTodoUnknownEvidenceDoesNotCount(t *testing.T) {
	s, f, todo := scheduledTodoFixture(t)
	task := todoProgressTask(todo.ID)
	setTasks(s, task)
	assessTodoBaseline(t, s, task.ID)
	assessTodoRun(t, s, task.ID, nil)
	todo.Agents = []TodoAgent{{MachineID: "box", SessionID: "worker"}}
	putTodo(s, todo)
	f.answerNode(DeviceSessionItems, func(machine string, _ json.RawMessage) (any, error) {
		if machine == "box" {
			return nil, errors.New("unreachable")
		}
		return map[string]any{"items": []proto.Item{}}, nil
	})
	_, result := assessTodoRun(t, s, task.ID, nil)
	if result.State != "unknown" || result.NoProgress != 1 {
		t.Fatalf("unreadable evidence changed the streak: %+v", result)
	}
	cur, _ := s.todoNow(todo.ID)
	if cur.Supervision.State == reviewNeedsUser {
		t.Fatal("unknown evidence escalated as a second no-progress run")
	}
}

func TestScheduledTodoProgressAfterUnknownReadResetsKnownStreak(t *testing.T) {
	s, f, todo := scheduledTodoFixture(t)
	todo.Agents = []TodoAgent{{MachineID: "box", SessionID: "worker"}}
	putTodo(s, todo)
	unreadable, answer := false, "An existing artifact"
	f.answerNode(DeviceSessionItems, func(machine string, _ json.RawMessage) (any, error) {
		if machine == "box" && unreadable {
			return nil, errors.New("unreachable")
		}
		return map[string]any{"items": []proto.Item{{Kind: proto.KindText, Role: "assistant", Text: answer}}}, nil
	})
	task := todoProgressTask(todo.ID)
	setTasks(s, task)
	assessTodoBaseline(t, s, task.ID)
	assessTodoRun(t, s, task.ID, nil)
	f.mu.Lock()
	unreadable = true
	f.mu.Unlock()
	_, unknown := assessTodoRun(t, s, task.ID, nil)
	if unknown.State != "unknown" || unknown.NoProgress != 1 {
		t.Fatalf("unreadable run: %+v", unknown)
	}
	f.mu.Lock()
	unreadable, answer = false, "A new artifact produced while the node was unreachable"
	f.mu.Unlock()
	_, recovered := assessTodoRun(t, s, task.ID, nil)
	if recovered.State != "progress" || recovered.NoProgress != 0 {
		t.Fatalf("recovery lost observable progress against the last readable baseline: %+v", recovered)
	}
	_, next := assessTodoRun(t, s, task.ID, nil)
	if next.State != "no_progress" || next.NoProgress != 1 {
		t.Fatalf("an unknown read bridged two unrelated no-progress stretches: %+v", next)
	}
}

func TestScheduledTodoPersistedStreakOutlivesHistoryAndRestart(t *testing.T) {
	store := &memStore{}
	s, _ := newQuietServiceOn(t, store)
	s.superviseTimes.gather = time.Hour
	todo := Todo{ID: "persisted", Title: "Backfill", Origin: "conv1", Supervision: &Supervision{Supervisor: "conv1"}}
	putTodo(s, todo)
	task := todoProgressTask(todo.ID)
	setTasks(s, task)
	assessTodoBaseline(t, s, task.ID)
	first, _ := assessTodoRun(t, s, task.ID, nil)
	for i := 0; i <= maxRuns; i++ {
		s.recordRun(ScheduleRun{RefID: "unrelated", Status: runOK})
	}
	_, runs := scheduleState(s)
	for _, r := range runs {
		if r.ID == first.ID {
			t.Fatal("fixture did not evict the first assessment")
		}
	}
	other, _ := newQuietServiceTimed(t, store, heldSuperviseTimings())
	other.todoAskAfter = time.Nanosecond
	putTodo(other, todo)
	_, result := assessTodoRun(t, other, task.ID, nil)
	if result.State != "no_progress" || result.NoProgress != 2 {
		t.Fatalf("restart or bounded history lost the streak: %+v", result)
	}
	cur, _ := other.todoNow(todo.ID)
	if cur.Supervision.State != reviewNeedsUser || len(cur.Supervision.Reviews) != 1 {
		t.Fatalf("persisted second no-progress run did not create one decision: %+v", cur.Supervision)
	}
}

func TestScheduledTodoCompletionIsIdempotentAndOrdered(t *testing.T) {
	s, _, todo := scheduledTodoFixture(t)
	task := todoProgressTask(todo.ID)
	setTasks(s, task)
	assessTodoBaseline(t, s, task.ID)
	first, _ := assessTodoRun(t, s, task.ID, nil)
	s.finishScheduledTodo(t.Context(), task, first)
	current, _ := taskByID(s, task.ID)
	if current.TodoProgress.NoProgress != 1 {
		t.Fatal("replaying the first completed run counted it twice")
	}
	second, _ := assessTodoRun(t, s, task.ID, nil)
	s.finishScheduledTodo(t.Context(), task, second)
	s.finishScheduledTodo(t.Context(), task, first)
	cur, _ := s.todoNow(todo.ID)
	current, _ = taskByID(s, task.ID)
	if current.TodoProgress.NoProgress != 2 || len(cur.Supervision.Reviews) != 1 {
		t.Fatalf("replays duplicated or replaced the decision: %+v %+v", current.TodoProgress, cur.Supervision)
	}
	older := ScheduleRun{RefID: task.ID, At: first.At.Add(-time.Minute), Status: runOK, MachineID: "server", SessionID: "old"}
	older.ID = s.recordRun(older)
	s.finishScheduledTodo(t.Context(), task, older)
	current, _ = taskByID(s, task.ID)
	if current.TodoProgress.Run != second.ID || current.TodoProgress.NoProgress != 2 {
		t.Fatal("a late older run overwrote the current streak")
	}
}

func TestScheduledTodoInactiveAndExistingDecisionGuards(t *testing.T) {
	for _, kind := range []string{"done", "cancelled", "superseded", "paused", "existing decision"} {
		t.Run(kind, func(t *testing.T) {
			s, _, todo := scheduledTodoFixture(t)
			task := todoProgressTask(todo.ID)
			setTasks(s, task)
			assessTodoBaseline(t, s, task.ID)
			assessTodoRun(t, s, task.ID, nil)
			_, result := assessTodoRun(t, s, task.ID, func() {
				cur, _ := s.todoNow(todo.ID)
				now := time.Now()
				switch kind {
				case "done":
					cur.DoneAt = &now
				case "cancelled":
					cur.CancelledAt = &now
				case "superseded":
					cur.Lineage = []LineageStep{{TodoID: cur.ID, SupersededBy: "successor"}}
				case "paused":
					sc := s.loadSchedule(t.Context())
					sc.mu.Lock()
					sc.tasks[0].Enabled = false
					sc.mu.Unlock()
				case "existing decision":
					cur.Supervision.State = reviewNeedsUser
					cur.Supervision.Reviews = []TaskReview{{Verdict: reviewNeedsUser, Reason: "An existing owner question"}}
				}
				putTodo(s, cur)
			})
			cur, _ := s.todoNow(todo.ID)
			if kind == "existing decision" {
				if result.NoProgress != 2 || len(cur.Supervision.Reviews) != 1 || cur.Supervision.Reviews[0].Reason != "An existing owner question" {
					t.Fatal("a no-progress assessment overwrote another owner decision")
				}
			} else if result.State != "inactive" || result.NoProgress != 0 || len(cur.Supervision.Reviews) != 0 {
				t.Fatalf("inactive work escalated: %+v %+v", result, cur.Supervision)
			}
		})
	}
}

func TestScheduledTodoPersistenceFailureIsVisibleWithoutDoubleCount(t *testing.T) {
	for _, key := range []string{tasksKey, runsKey} {
		t.Run(key, func(t *testing.T) {
			store := &scheduleWriteStore{Store: &memStore{}}
			s, _ := newQuietServiceOn(t, store)
			s.superviseTimes.gather = time.Hour
			todo := Todo{ID: "write-failure", Origin: "conv1", Supervision: &Supervision{Supervisor: "conv1"},
				Agents: []TodoAgent{{MachineID: "box", SessionID: "worker"}}}
			putTodo(s, todo)
			task := todoProgressTask(todo.ID)
			setTasks(s, task)
			assessTodoBaseline(t, s, task.ID)
			before, _ := s.scheduledTodoSnapshot(t.Context(), task, ScheduleRun{})
			task.todoBefore = &before
			run := ScheduleRun{RefID: task.ID, Status: runOK, At: time.Now(), MachineID: "server", SessionID: "check",
				TodoProgress: &ScheduleTodoAssessment{TodoID: todo.ID, State: "pending"}}
			run.ID = s.recordRun(run)
			store.failWrites(key)
			logs := &logBuffer{}
			s.logf = logs.logf
			s.finishScheduledTodo(t.Context(), task, run)
			s.supervise.mu.Lock()
			signals := len(s.supervise.pending)
			told := false
			for _, signal := range s.supervise.pending {
				// The hub's check failed, about the task's Agent: never the check run's own conversation.
				told = told || signal.Todo == todo.ID && signal.Kind == sigCheck && signal.Report == checkErrorReport &&
					strings.Contains(signal.Detail, run.ID) && signal.Machine == "box" && signal.Session == "worker"
			}
			s.supervise.mu.Unlock()
			if signals == 0 || !told || !strings.Contains(logs.String(), "save to-do progress") {
				t.Fatal("a failed assessment disappeared without a log and a check signal to the supervisor")
			}
			runOf := func(runs []ScheduleRun) *ScheduleRun {
				t.Helper()
				i := slices.IndexFunc(runs, func(r ScheduleRun) bool { return r.ID == run.ID })
				if i < 0 {
					t.Fatal("the run under test is not in the history")
				}
				return &runs[i]
			}
			_, failedRuns := scheduleState(s)
			wantState := "error"
			if key == runsKey {
				wantState = "no_progress"
			}
			if failed := runOf(failedRuns); failed.TodoProgress.State != wantState || failed.TodoProgress.Reason == "" {
				t.Fatalf("the failed write misrepresented the assessment: %+v", failed.TodoProgress)
			}
			if key == tasksKey {
				cur, _ := taskByID(s, task.ID)
				if cur.TodoProgress == nil || cur.TodoProgress.Run == run.ID {
					t.Fatal("a failed snapshot write published undurable no-progress evidence")
				}
				var persisted []ScheduleRun
				if _, err := store.Setting(t.Context(), runsKey, &persisted); err != nil {
					t.Fatal(err)
				}
				if len(persisted) != 2 || runOf(persisted).TodoProgress.State != "error" {
					t.Fatal("the visible assessment error was not saved in run history")
				}
			}
			store.failWrites("")
			s.finishScheduledTodo(t.Context(), task, run)
			cur, _ := taskByID(s, task.ID)
			if cur.TodoProgress == nil || cur.TodoProgress.NoProgress != 1 || !cur.TodoProgress.Signaled {
				t.Fatalf("a retried persistence write lost or double-counted the run: %+v", cur.TodoProgress)
			}
		})
	}
}

func TestScheduledTodoPartialWriteReplayAfterRestart(t *testing.T) {
	store := &scheduleWriteStore{Store: &memStore{}}
	s, _ := newQuietServiceOn(t, store)
	s.superviseTimes.gather = time.Hour
	todo := Todo{ID: "partial-write", Origin: "conv1", Supervision: &Supervision{Supervisor: "conv1"}}
	putTodo(s, todo)
	task := todoProgressTask(todo.ID)
	setTasks(s, task)
	assessTodoBaseline(t, s, task.ID)
	task.todoBefore = &scheduleTodoSnapshot{}
	run := ScheduleRun{RefID: task.ID, Status: runOK, At: time.Now(), MachineID: "server", SessionID: "check",
		TodoProgress: &ScheduleTodoAssessment{TodoID: todo.ID, State: "pending"}}
	run.ID = s.recordRun(run)
	store.failWrites(runsKey)
	s.finishScheduledTodo(t.Context(), task, run)
	current, _ := taskByID(s, task.ID)
	_, runs := scheduleState(s)
	i := slices.IndexFunc(runs, func(r ScheduleRun) bool { return r.ID == run.ID })
	if i < 0 || *runs[i].TodoProgress != current.TodoProgress.Assessment {
		t.Fatalf("history lost the committed assessment before restart: %+v %+v", runs, current.TodoProgress)
	}
	store.failWrites("")
	other, _ := newQuietServiceTimed(t, store, heldSuperviseTimings())
	putTodo(other, todo)
	current, _ = taskByID(other, task.ID)
	other.finishScheduledTodo(t.Context(), current, run)
	current, _ = taskByID(other, task.ID)
	if current.TodoProgress.NoProgress != 1 || !current.TodoProgress.Signaled {
		t.Fatalf("a partially persisted assessment counted twice after restart: %+v", current.TodoProgress)
	}
	cur, _ := other.todoNow(todo.ID)
	if cur.Supervision.State == reviewNeedsUser {
		t.Fatal("one physical run became two no-progress runs after restart")
	}
}

func TestScheduledTodoHistoryFailureDoesNotSwallowEscalation(t *testing.T) {
	store := &scheduleWriteStore{Store: &memStore{}}
	s, _ := newQuietServiceOn(t, store)
	s.superviseTimes.gather = time.Hour
	s.todoAskAfter = time.Nanosecond
	todo := Todo{ID: "history-failure", Origin: "conv1", Supervision: &Supervision{Supervisor: "conv1"}}
	putTodo(s, todo)
	task := todoProgressTask(todo.ID)
	setTasks(s, task)
	assessTodoBaseline(t, s, task.ID)
	assessTodoRun(t, s, task.ID, nil)
	run, result := assessTodoRun(t, s, task.ID, func() { store.failWrites(runsKey) })
	current, _ := taskByID(s, task.ID)
	cur, _ := s.todoNow(todo.ID)
	if result.State != "no_progress" || result.NoProgress != 2 || result != current.TodoProgress.Assessment ||
		current.TodoProgress.NoProgress != 2 || !current.TodoProgress.Escalated ||
		cur.Supervision.State != reviewNeedsUser || len(cur.Supervision.Reviews) != 1 {
		t.Fatalf("a failed history write swallowed escalation from durable evidence: assessment=%+v progress=%+v supervision=%+v",
			result, current.TodoProgress, cur.Supervision)
	}
	store.failWrites("")
	s.finishScheduledTodo(t.Context(), current, run)
	cur, _ = s.todoNow(todo.ID)
	_, runs := scheduleState(s)
	last := runs[len(runs)-1].TodoProgress
	if len(cur.Supervision.Reviews) != 1 || last.State != "no_progress" || last.NoProgress != 2 {
		t.Fatalf("recovering history duplicated the decision or count: %+v %+v", last, cur.Supervision)
	}
}

func TestScheduledTodoOptionsAreLocalizedAndNeverAutoApplied(t *testing.T) {
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		t.Run(string(lang), func(t *testing.T) {
			s, f, todo := scheduledTodoFixture(t)
			f.mu.Lock()
			f.hello.Locale = string(lang)
			f.mu.Unlock()
			task := todoProgressTask(todo.ID)
			setTasks(s, task)
			ctx := t.Context()
			before, _ := s.scheduledTodoSnapshot(ctx, task, ScheduleRun{})
			task.todoBefore = &before
			for i := 0; i < 3; i++ { // the baseline, then two runs without progress
				run := ScheduleRun{RefID: task.ID, At: time.Now(), Status: runOK, MachineID: "server", SessionID: "check",
					TodoProgress: &ScheduleTodoAssessment{TodoID: todo.ID, State: "pending"}}
				run.ID = s.recordRun(run)
				s.finishScheduledTodo(ctx, task, run)
			}
			cur, _ := s.todoNow(todo.ID)
			review := cur.Supervision.Reviews[len(cur.Supervision.Reviews)-1]
			if len(review.Options) != 2 || review.Options[0].Label != i18n.T(lang, "assistant-schedule.todo_change") ||
				review.Options[1].Label != i18n.T(lang, "assistant-schedule.todo_wait") ||
				review.Reversible == nil || *review.Reversible || review.Deadline == nil {
				t.Fatalf("missing localized choices or an automatic strategy change: %+v", review)
			}
		})
	}
}

func TestScheduledTodoPromptAndUnlinkedExclusions(t *testing.T) {
	s, _, todo := scheduledTodoFixture(t)
	task := todoProgressTask(todo.ID)
	now := time.Now()
	if text := taskPrompt(task, now, now, time.UTC, false, false, false); !strings.Contains(text, "Linked to-do: "+todo.ID) {
		t.Fatal("the scheduled run was not told its associated to-do")
	}
	for _, kind := range []string{"unlinked", "one shot"} {
		t.Run(kind, func(t *testing.T) {
			other := task
			if kind == "unlinked" {
				other.TodoID = ""
			} else {
				other.Repeat = repeatOnce
			}
			setTasks(s, other)
			run := ScheduleRun{RefID: other.ID, At: now, Status: runOK}
			run.ID = s.recordRun(run)
			s.finishScheduledTodo(t.Context(), other, run)
			cur, _ := taskByID(s, other.ID)
			if cur.TodoProgress != nil {
				t.Fatal("unlinked or one-shot work entered progress supervision")
			}
		})
	}
}

func TestScheduledTodoCriterionDefinitionEditIsNotStateProgress(t *testing.T) {
	before := scheduleTodoSnapshot{Criteria: map[string]string{"original criterion": "pending"}}
	after := scheduleTodoSnapshot{Criteria: map[string]string{"replacement criterion": "pending"}}
	if scheduledTodoAdvanced(before, after) {
		t.Fatal("rewording a criterion counted as a state change")
	}
	after.Criteria = map[string]string{"original criterion": "not_met"}
	if !scheduledTodoAdvanced(before, after) {
		t.Fatal("an explicit criterion state transition was lost")
	}
}

func TestScheduledTodoEditsKeepOrExplicitlyClearAssociation(t *testing.T) {
	s, _, todo := scheduledTodoFixture(t)
	task := todoProgressTask(todo.ID)
	task.At = time.Now().Add(time.Hour)
	task.TodoProgress = &scheduleTodoProgress{Run: "previous", NoProgress: 1}
	setTasks(s, task)
	if _, err := runJSON(t, s, "conv1", "schedule_routine", map[string]any{"id": task.ID, "title": "Renamed"}); err != nil {
		t.Fatal(err)
	}
	cur, _ := taskByID(s, task.ID)
	if cur.TodoID != todo.ID || cur.TodoProgress == nil || cur.TodoProgress.NoProgress != 1 {
		t.Fatal("omitting the association on an edit discarded the baseline")
	}
	raw := scheduleJSON(t, cur)
	raw["todo_id"] = "another-to-do"
	raw["todo_progress"] = map[string]any{"no_progress": 100}
	body, _ := json.Marshal(raw)
	if w := scheduleRequest(s, "PUT", "tasks/"+task.ID, string(body)); w.Code != 200 {
		t.Fatalf("client edit: %d %s", w.Code, w.Body)
	}
	cur, _ = taskByID(s, task.ID)
	if cur.TodoID != todo.ID || cur.TodoProgress.NoProgress != 1 {
		t.Fatal("a client spoofed hub-owned evidence or changed the scoped association")
	}
	if _, err := runJSON(t, s, "conv1", "schedule_routine", map[string]any{"id": task.ID, "todo_id": ""}); err != nil {
		t.Fatal(err)
	}
	cur, _ = taskByID(s, task.ID)
	if cur.TodoID != "" || cur.TodoProgress != nil {
		t.Fatal("explicitly unlinking kept a previous task's progress state")
	}
}

func TestScheduledTodoNonOutputDoesNotResetStreak(t *testing.T) {
	// A tool's result alone is no call; tool calls themselves are TestScheduledTodoToolCallsAreWorkWaitsAreNot's.
	for _, kind := range []string{"API error", "tool result", "own summary", "released agent", "blocked report"} {
		t.Run(kind, func(t *testing.T) {
			s, f, todo := scheduledTodoFixture(t)
			todo.Agents = []TodoAgent{{MachineID: "box", SessionID: "worker"}}
			putTodo(s, todo)
			items := []proto.Item{{Kind: proto.KindText, Role: "assistant", Text: "An earlier artifact"}}
			f.answerNode(DeviceSessionItems, func(string, json.RawMessage) (any, error) {
				return map[string]any{"items": items}, nil
			})
			task := todoProgressTask(todo.ID)
			if kind == "own summary" {
				task.MachineID, task.SessionID = "box", "worker"
			}
			setTasks(s, task)
			assessTodoBaseline(t, s, task.ID)
			_, result := assessTodoRun(t, s, task.ID, func() {
				f.mu.Lock()
				items = []proto.Item{{Kind: proto.KindText, Role: "assistant", Text: "Another status summary"}}
				if kind == "API error" {
					items[0].Text = "API Error: overloaded"
				} else if kind == "tool result" {
					items[0].Kind = proto.KindToolResult
				}
				f.mu.Unlock()
				switch kind {
				case "released agent":
					cur, _ := s.todoNow(todo.ID)
					now := time.Now()
					cur.Agents[0].ReleasedAt = &now
					putTodo(s, cur)
				case "blocked report":
					f.mu.Lock()
					items[0].Text = "An earlier artifact"
					f.mu.Unlock()
					s.noteProgress(t.Context(), todo.ID, TaskProgress{Kind: upReportBlocked, Text: "Still waiting for a slot", At: time.Now()})
				}
			})
			if result.State != "no_progress" || result.NoProgress != 1 {
				t.Fatalf("%s counted as new task output: %+v", kind, result)
			}
		})
	}
}
