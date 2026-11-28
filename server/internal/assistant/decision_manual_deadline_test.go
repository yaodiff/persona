package assistant

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Terminal recovery on a decision (#13413).

const legacyTerminalOption = "Resume the Agent on its terminal page"

func manualResumeRefused(err error) bool {
	var m *i18n.Message
	return errors.As(err, &m) && m.Key == "assistant-reports.manual_terminal_resume"
}

// Past its deadline, an older reversible decision that recommends the owner's terminal Resume has no default the hub
// can take: it is marked handled and reminded once, not refused and reminded again on every sweep.
func TestLegacyTerminalResumeDecisionRemindedOnceAtDeadline(t *testing.T) {
	for _, tc := range []struct{ lang, label string }{
		{"en", legacyTerminalOption},
		{"zh-Hans", "在终端上恢复 Agent"},
		{"ja", "ターミナルでエージェントを再開"},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			at := time.Now().UTC().Add(-time.Hour)
			e := dismissalEnv(t, &memStore{}, at, true)
			e.s.todos.todos["task"].Origin = "conv"
			a := e.s.accept.asks["task"]
			e.f.mu.Lock()
			e.f.hello.Locale = tc.lang
			e.f.mu.Unlock()
			a.Recommend, a.Options[0].Label = tc.label, tc.label
			a.WhyOwner = whyPriority
			l := e.s.lang(t.Context())
			loc := e.s.location(t.Context())
			a.Deadline = time.Now().UTC().Add(time.Hour)
			if row := e.s.todoAsks(t.Context())["task"]; row.Risk != assistantwire.OwnerAskRiskOpen || row.Default != defaultNever {
				t.Fatalf("manual recommendation before deadline: risk %q, default %q", row.Risk, row.Default)
			}
			if !e.s.pushDecision(t.Context(), "task") {
				t.Fatal("missing initial decision push")
			}
			initial := e.f.pushed()[0]
			before := i18n.T(l, "assistant-reports.deadline_manual_resume", i18n.Args{"when": a.Deadline.In(loc).Format("01-02 15:04")})
			if initial.Todo.Lockscreen != assistantwire.OwnerAskLockscreenOpenOnly || !strings.Contains(initial.Body, before) {
				t.Fatalf("initial push: lockscreen %q, body %q", initial.Todo.Lockscreen, initial.Body)
			}
			a.Deadline = at
			if row := e.s.todoAsks(t.Context())["task"]; row.Default != defaultNever {
				t.Errorf("manual recommendation advertised as default %q", row.Default)
			}
			for range 3 {
				e.s.sweepTaskDeadlines(time.Now())
			}
			cur, _ := e.s.askCopy("task")
			if cur.DueAt.IsZero() || !cur.AcceptedAt.IsZero() || cur.ByDefault {
				t.Errorf("deadline not handled as a reminder: %+v", cur)
			}
			pushes := e.f.pushed()
			if len(pushes) != 2 {
				t.Fatalf("%d decision pushes for 3 sweeps, want 1", len(pushes)-1)
			}
			want := i18n.T(l, "assistant-reports.deadline_manual_resume_overdue", i18n.Args{"when": a.Deadline.In(loc).Format("01-02 15:04")})
			if !strings.Contains(pushes[1].Body, want) {
				t.Fatalf("missing manual reminder %q in %q", want, pushes[1].Body)
			}
		})
	}
}

// The owner's press on an older decision's terminal Resume (its OK, or the option itself) is refused before any answer
// is prepared or saved: the decision, its Don't handle and the stored settings stay exactly as they were.
func TestLegacyTerminalResumePressRefusedBeforeAnyWrite(t *testing.T) {
	for _, recommended := range []bool{true, false} {
		t.Run(map[bool]string{true: "recommended", false: "option"}[recommended], func(t *testing.T) {
			at := time.Now().UTC().Add(-time.Hour)
			st := &memStore{}
			e := dismissalEnv(t, st, at, true)
			a := e.s.accept.asks["task"]
			a.Options[1].Label = legacyTerminalOption
			if recommended {
				a.Recommend = legacyTerminalOption
				a.Options[0], a.Options[1] = a.Options[1], a.Options[0]
			}
			dismissed := dismissReview(t, e, at)
			before, _ := e.s.askCopy("task")
			puts, calls := st.putCount(), e.f.callCount()
			if recommended {
				if _, err := e.s.acceptTry(t.Context(), "task", ""); !manualResumeRefused(err) {
					t.Fatalf("OK on a terminal Resume: %v", err)
				}
			}
			if _, err := e.s.chooseOption(t.Context(), "task", before.AskedAt.Format(timeKeyLayout), legacyTerminalOption); !manualResumeRefused(err) {
				t.Fatalf("terminal Resume chosen: %v", err)
			}
			if got := st.putCount(); got != puts {
				t.Fatalf("a refused press wrote %d settings", got-puts)
			}
			if after, _ := e.s.askCopy("task"); !reflect.DeepEqual(before, after) {
				t.Fatalf("a refused press changed the decision: %+v", after)
			}
			if e.f.sent() != 0 || e.f.callCount() != calls {
				t.Fatal("a refused press reached a device")
			}
			list := []listedTodo{{Todo: todoNow(e.s, "task")}}
			if err := e.s.listDecisionReviews(t.Context(), list); err != nil || list[0].DecisionReview == nil ||
				list[0].DecisionReview.DismissalID != dismissed.DismissalID {
				t.Fatalf("a refused press undid Don't handle: %+v %v", list[0].DecisionReview, err)
			}
		})
	}
}

// A manual round replacing a due recommendation gets its own single reminder.
func TestTerminalResumeReplacesDueDefault(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour)
	e := dismissalEnv(t, &memStore{}, at, true)
	e.s.todos.todos["task"].Origin = "conv"
	e.s.deadlineDefaultHook = func(id string) {
		e.s.accept.mu.Lock()
		defer e.s.accept.mu.Unlock()
		a := e.s.accept.asks[id]
		a.AskedAt = a.AskedAt.Add(time.Second)
		a.Recommend, a.Options[0].Label = legacyTerminalOption, legacyTerminalOption
	}
	e.s.sweepTaskDeadlines(time.Now())
	e.s.deadlineDefaultHook = nil
	if got := len(e.f.pushed()); got != 0 {
		t.Fatalf("old default triggered %d reminders on the replacement round", got)
	}
	e.s.sweepTaskDeadlines(time.Now())
	if got := len(e.f.pushed()); got != 1 {
		t.Fatalf("new round received %d reminders, want 1", got)
	}
}

func TestTerminalResumeDeadlineRespectsDismissalAndRecheck(t *testing.T) {
	for _, scenario := range []string{"dismissed during sweep", "due for fresh review"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now().UTC()
			at := now.Add(-time.Hour)
			if scenario == "due for fresh review" {
				at = now.Add(-decisionRecheckAfter - time.Minute)
			}
			e := dismissalEnv(t, &memStore{}, at, true)
			e.s.todos.todos["task"].Origin = "conv"
			a := e.s.accept.asks["task"]
			a.Recommend, a.Options[0].Label = legacyTerminalOption, legacyTerminalOption
			if scenario == "dismissed during sweep" {
				e.s.deadlineDefaultHook = func(string) { dismissReview(t, e, at) }
			}
			e.s.sweepTaskDeadlines(now)
			if got := len(e.f.pushed()); got != 0 || !a.DueAt.IsZero() {
				t.Fatalf("handled a quiet deadline: pushes %d, handled at %v", got, a.DueAt)
			}
		})
	}
}

func TestTerminalResumeDeadlineLoadsTasksBeforeRecordingReminder(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC()
	st := &failingStore{DB: todoTestDB(t)}
	const id = "task"
	if err := st.CreateTodo(ctx, db.TodoRow{ID: id, Title: "Resume the task", Origin: "conv", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutTodoSupervision(ctx, db.TodoSupervisionRow{TodoID: id, Supervisor: "conv", State: reviewNeedsUser, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	asks := map[string]acceptAsk{id: {
		Conclusion: true, Outcome: outcomeDecide, Recommend: legacyTerminalOption, Reversible: true,
		AskedAt: now.Add(-time.Hour), Deadline: now.Add(-time.Minute),
	}}
	if err := st.PutSetting(ctx, acceptanceKey, asks); err != nil {
		t.Fatal(err)
	}
	st.failLoad.Store(true)
	e := newTodoEnv(t, st)
	e.s.sweepTaskDeadlines(now)
	var saved map[string]acceptAsk
	if found, err := st.Setting(ctx, acceptanceKey, &saved); err != nil || !found || !saved[id].DueAt.IsZero() || deadlinePushes(e.f, id) != 0 {
		t.Fatalf("unreadable tasks consumed the reminder: ask %+v, found %v, error %v", saved[id], found, err)
	}

	st.failLoad.Store(false)
	for range 2 {
		e.s.sweepTaskDeadlines(now)
	}
	if found, err := st.Setting(ctx, acceptanceKey, &saved); err != nil || !found || saved[id].DueAt.IsZero() || deadlinePushes(e.f, id) != 1 {
		t.Fatalf("after reload: ask %+v, pushes %d, found %v, error %v", saved[id], deadlinePushes(e.f, id), found, err)
	}
}

func TestTerminalResumeDeadlineRechecksTaskBeforeReminder(t *testing.T) {
	for _, state := range []string{"closed", "reopened", "deleted", "returned", "legacy park", "agent reply"} {
		t.Run(state, func(t *testing.T) {
			now := time.Now().UTC()
			e := dismissalEnv(t, &memStore{}, now.Add(-time.Hour), true)
			e.s.todos.todos["task"].Origin = "conv"
			a := e.s.accept.asks["task"]
			a.Recommend, a.Options[0].Label = legacyTerminalOption, legacyTerminalOption
			changed := false
			e.s.deadlineDefaultHook = func(id string) {
				changed = true
				switch state {
				case "closed", "reopened":
					done := state == "closed"
					if _, err := e.s.editTodo(t.Context(), id, db.TodoEdit{Done: &done, RestartWork: !done}); err != nil {
						t.Fatal(err)
					}
				case "deleted":
					e.s.todos.mu.Lock()
					delete(e.s.todos.todos, id)
					e.s.todos.mu.Unlock()
				case "returned", "legacy park":
					e.s.todos.mu.Lock()
					r := &e.s.todos.todos[id].Supervision.Reviews[0]
					if state == "returned" {
						r.RecheckAt = now
					} else {
						r.Verdict, r.WaitingOn = reviewParked, parkOwnerPriority
					}
					e.s.todos.mu.Unlock()
				case "agent reply":
					e.s.accept.mu.Lock()
					a.AgentReply = &decisionAgentReply{At: now}
					e.s.accept.mu.Unlock()
				}
			}
			e.s.sweepTaskDeadlines(now)
			if !changed || !a.DueAt.IsZero() || deadlinePushes(e.f, "task") != 0 {
				t.Fatalf("task changed %v, handled at %v, overdue pushes %d", changed, a.DueAt, deadlinePushes(e.f, "task"))
			}
		})
	}
}

func TestTerminalResumeDeadlineOnOutsideDecisionReturnsChoiceToSource(t *testing.T) {
	e := newTodoEnv(t, nil)
	in := outsideFixture()
	in.Recommend, in.Options[1].Label = legacyTerminalOption, legacyTerminalOption
	d := raiseOutside(t, e, "release-script", in)
	pastDeadline(t, e.s, d.AskTodoID)
	pushes := deadlinePushes(e.f, d.AskTodoID)
	e.s.sweepTaskDeadlines(time.Now())
	got := pollOutside(t, e, "release-script", d.ID)
	if got.Answer == nil || got.Answer.Option != legacyTerminalOption || got.Answer.AnsweredBy != assistantwire.OutsideAnswerDefault {
		t.Fatalf("outside default did not reach its caller: %+v", got)
	}
	if n := deadlinePushes(e.f, d.AskTodoID) - pushes; n != 0 {
		t.Fatalf("outside default generated %d reminders", n)
	}
}

// A to-do PATCH can finish after the deadline worker has checked its round. The push sender's
// own snapshot decides whether that specific reminder is still due.
func TestTerminalResumeOverduePushRechecksRoundAndTask(t *testing.T) {
	for _, change := range []string{"close", "replace"} {
		t.Run(change, func(t *testing.T) {
			at := time.Now().UTC().Add(-time.Hour)
			e := dismissalEnv(t, &memStore{}, at, true)
			e.s.todos.todos["task"].Origin = "conv"
			a := e.s.accept.asks["task"]
			a.Recommend, a.Options[0].Label, a.DueAt = legacyTerminalOption, legacyTerminalOption, time.Now().UTC()
			checked, err := e.s.askRound("task", a.AskedAt.Format(timeKeyLayout))
			if err != nil || checked == nil || checked.DueAt.IsZero() {
				t.Fatalf("fixture has no handled deadline: %+v, %v", checked, err)
			}
			switch change {
			case "close":
				e.must("PATCH", "/api/v1/todos/task", `{"done":true}`)
			case "replace":
				e.s.accept.mu.Lock()
				a.AskedAt = a.AskedAt.Add(time.Minute)
				e.s.accept.mu.Unlock()
			}
			sent, err := e.s.pushDecisionAbout(t.Context(), "task", checked.AskedAt)
			if err != nil || sent || deadlinePushes(e.f, "task") != 0 {
				t.Fatalf("obsolete round sent: %v, error %v, pushes %d", sent, err, deadlinePushes(e.f, "task"))
			}
		})
	}
}
