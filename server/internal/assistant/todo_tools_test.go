package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// todoTool runs one of the todo tools from conversation conv1.
func (e *todoEnv) tool(name string, args map[string]any) (string, error) {
	e.t.Helper()
	return runJSON(e.t, e.s, "conv1", name, args)
}

// todoByID is the todo as the apps read it now.
func (e *todoEnv) todo(id string) (Todo, bool) {
	e.t.Helper()
	var res struct{ Todos []Todo }
	_ = json.Unmarshal(e.must("GET", "/api/v1/todos", "").Body.Bytes(), &res)
	for _, t := range res.Todos {
		if t.ID == id {
			return t, true
		}
	}
	return Todo{}, false
}

// idIn is the first todo id in a tool's answer.
func idIn(t *testing.T, res string) string {
	t.Helper()
	i := strings.Index(res, "todo_")
	if i < 0 {
		t.Fatalf("no todo id in %q", res)
	}
	return strings.Fields(res[i:])[0]
}

// The assistant's todo tools on the user's own to-dos: created with a due day, reminder and priority, not twice;
// listed by due date and filtered; changed (a due moment replaces the day, "" clears); completed and reopened without
// a card; read in full.
func TestTodoTools(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		loc := e.s.location(context.Background())
		day := time.Now().In(loc).AddDate(0, 0, 3).Format(dayLayout)
		remind := time.Now().In(loc).Add(2 * time.Hour).Format("2006-01-02 15:04")
		res, err := e.tool("todo_create", map[string]any{"title": "Submit the homework", "notes": "course page link", "due": day,
			"remind": remind, "priority": "high"})
		if err != nil || !strings.HasPrefix(res, "Created: ") || !strings.Contains(res, "high priority") || !strings.Contains(res, "the whole day") {
			t.Fatalf("create: %s %v", res, err)
		}
		id := idIn(t, res)
		got, ok := e.todo(id)
		if !ok || got.DueDate != day || got.DueAt != nil || got.RemindAt == nil || got.Priority != db.PriorityHigh ||
			got.Notes != "course page link" || len(got.Agents) != 0 || got.Origin != "" {
			t.Fatalf("stored: %+v", got)
		}
		if res, err := e.tool("todo_create", map[string]any{"title": "Submit the homework", "due": day}); err != nil ||
			!strings.Contains(res, "Already there") || idIn(t, res) != id {
			t.Fatalf("created twice: %s %v", res, err)
		}
		other, err := e.tool("todo_create", map[string]any{"title": "Renew the domain"})
		if err != nil {
			t.Fatal(err)
		}
		soon, err := e.tool("todo_create", map[string]any{"title": "Review the paper", "due": "1d"})
		if err != nil {
			t.Fatal(err)
		}

		list, err := e.tool("todo_list", nil)
		if err != nil {
			t.Fatal(err)
		}
		a, b, c := strings.Index(list, idIn(t, soon)), strings.Index(list, id), strings.Index(list, idIn(t, other))
		if a < 0 || b < 0 || c < 0 || !(a < b && b < c) || !strings.Contains(list, "notes: course page link") {
			t.Fatalf("list, by due date: %s", list)
		}
		if list, _ := e.tool("todo_list", map[string]any{"due_before": time.Now().In(loc).AddDate(0, 0, 2).Format(dayLayout)}); strings.Contains(list, id) ||
			!strings.Contains(list, idIn(t, soon)) || strings.Contains(list, idIn(t, other)) {
			t.Fatalf("due_before: %s", list)
		}
		if list, _ := e.tool("todo_list", map[string]any{"query": "domain"}); !strings.Contains(list, idIn(t, other)) || strings.Contains(list, id) {
			t.Fatalf("query: %s", list)
		}

		moment := time.Now().In(loc).AddDate(0, 0, 5).Format("2006-01-02") + " 18:00"
		if res, err := e.tool("todo_update", map[string]any{"id": id, "due": moment, "remind": "", "priority": "normal"}); err != nil ||
			!strings.HasPrefix(res, "Updated: ") {
			t.Fatalf("update: %s %v", res, err)
		}
		got, _ = e.todo(id)
		if got.DueAt == nil || got.DueDate != "" || got.DueAt.In(loc).Format("2006-01-02 15:04") != moment || got.RemindAt != nil ||
			got.Priority != "" || got.Notes != "course page link" || got.Title != "Submit the homework" {
			t.Fatalf("updated: %+v", got)
		}
		if _, err := e.tool("todo_update", map[string]any{"id": id}); err == nil {
			t.Error("an update with nothing to change went through")
		}
		if _, err := e.tool("todo_update", map[string]any{"id": id, "title": " "}); err == nil {
			t.Error("an empty title was taken")
		}
		if _, err := e.tool("todo_update", map[string]any{"id": id, "remind": "2020-01-01 09:00"}); err == nil {
			t.Error("a reminder in the past was taken")
		}

		if res, err := e.tool("todo_complete", map[string]any{"id": id}); err != nil || !strings.HasPrefix(res, "Completed: ") {
			t.Fatalf("complete: %s %v", res, err)
		}
		if got, _ = e.todo(id); got.DoneAt == nil {
			t.Fatal("not done")
		}
		if list, _ := e.tool("todo_list", nil); strings.Contains(list, id) {
			t.Fatalf("a done one is listed as open: %s", list)
		}
		if res, err := e.tool("todo_reopen", map[string]any{"id": id}); err != nil || !strings.HasPrefix(res, "Reopened: ") {
			t.Fatalf("reopen: %s %v", res, err)
		}
		if got, _ = e.todo(id); got.DoneAt != nil {
			t.Fatal("still done")
		}
		res, err = e.tool("todo_get", map[string]any{"id": id})
		if err != nil || !strings.Contains(res, "Submit the homework") || !strings.Contains(res, "notes:\ncourse page link") {
			t.Fatalf("get: %s %v", res, err)
		}
		if _, err := e.tool("todo_get", map[string]any{"id": "todo_none"}); err == nil {
			t.Error("an unknown id was read")
		}
		if len(e.s.pendingQuestions()) != 0 {
			t.Error("a card was shown for the user's own to-do")
		}
	})
}

// A task whose agent never started stays visible to the assistant, with why, instead of reading as finished work (#6014).
func TestTodoToolsFailedStart(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		ctx := context.Background()
		task, err := e.s.createTodo(ctx, "Fix the build", "", "conv1", "plan_failed")
		if err != nil {
			t.Fatal(err)
		}
		reason := "claude did not start within 3m0s: the tab's shell had not run it yet"
		if err := e.s.failTodo(ctx, task.ID, reason); err != nil {
			t.Fatal(err)
		}
		line := task.ID + ` "Fix the build" [failed to start, task]`
		for _, args := range []map[string]any{nil, {"status": "done"}} {
			list, err := e.tool("todo_list", args)
			if err != nil || !strings.Contains(list, line) {
				t.Fatalf("list %v: %s %v", args, list, err)
			}
		}
		got, err := e.tool("todo_get", map[string]any{"id": task.ID})
		if err != nil || !strings.Contains(got, line) || !strings.Contains(got, "failed to start: "+reason) {
			t.Fatalf("get: %s %v", got, err)
		}
	})
}

// Deleting is confirmed on the phone every time: declined, the to-do stays; allowed, it is gone. Completing a task
// (agents work on it) is confirmed too; the user's own to-do is not.
func TestTodoToolsConfirm(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		res, err := e.tool("todo_create", map[string]any{"title": "Pay the rent"})
		if err != nil {
			t.Fatal(err)
		}
		id := idIn(t, res)
		shown := make(chan proto.Interaction, 1)
		go answerArchiveCard(t, e.s, false, shown)
		if res, err := e.tool("todo_delete", map[string]any{"id": id}); err != nil || !strings.Contains(res, "declined") {
			t.Fatalf("declined: %s %v", res, err)
		}
		if card := <-shown; card.Kind != proto.IxPermission || card.ToolName != "todo_delete" || card.SessionID != "conv1" ||
			!strings.Contains(card.Summary, "Pay the rent") {
			t.Fatalf("card: %+v", card)
		}
		if _, ok := e.todo(id); !ok {
			t.Fatal("deleted though declined")
		}
		go answerArchiveCard(t, e.s, true, shown)
		if res, err := e.tool("todo_delete", map[string]any{"id": id}); err != nil || !strings.HasPrefix(res, "Deleted ") {
			t.Fatalf("allowed: %s %v", res, err)
		}
		<-shown
		if _, ok := e.todo(id); ok {
			t.Fatal("still there")
		}

		task, err := e.s.createTodo(context.Background(), "Fix the build", "", "conv-task", "plan_1")
		if err != nil {
			t.Fatal(err)
		}
		go answerArchiveCard(t, e.s, false, shown)
		if res, err := e.tool("todo_complete", map[string]any{"id": task.ID}); err != nil || !strings.Contains(res, "declined") {
			t.Fatalf("task, declined: %s %v", res, err)
		}
		if card := <-shown; card.ToolName != "todo_complete" || !strings.Contains(card.Summary, "Fix the build") {
			t.Fatalf("task card: %+v", card)
		}
		if got, _ := e.todo(task.ID); got.DoneAt != nil {
			t.Fatal("a task was completed though declined")
		}
	})
}

// The apps set the plan through POST / PATCH: due_date or due_at (not both), remind_at, priority; null clears, a field
// left out is kept; unreadable values are refused.
func TestTodoPlanAPI(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		send := func(method, path, body string) Todo {
			var res struct{ Todo Todo }
			_ = json.Unmarshal(e.must(method, path, body).Body.Bytes(), &res)
			return res.Todo
		}
		got := send("POST", "/api/v1/todos", `{"title":"Renew the passport","due_date":"2026-12-01","remind_at":"2026-11-30T09:00:00+08:00","priority":"low"}`)
		id := got.ID
		if got.DueDate != "2026-12-01" || got.RemindAt == nil || !got.RemindAt.Equal(time.Date(2026, 11, 30, 1, 0, 0, 0, time.UTC)) ||
			got.Priority != db.PriorityLow {
			t.Fatalf("created: %+v", got)
		}
		got = send("PATCH", "/api/v1/todos/"+id, `{"due_at":"2026-12-01T18:00:00Z","notes":"bring photos"}`)
		if got.DueDate != "" || got.DueAt == nil || got.RemindAt == nil || got.Priority != db.PriorityLow || got.Notes != "bring photos" {
			t.Fatalf("due moment: %+v", got)
		}
		got = send("PATCH", "/api/v1/todos/"+id, `{"due_at":null,"remind_at":null,"priority":""}`)
		if got.DueAt != nil || got.DueDate != "" || got.RemindAt != nil || got.Priority != "" || got.Notes != "bring photos" {
			t.Fatalf("cleared: %+v", got)
		}
		for _, body := range []string{`{"due_at":"2026-12-01T18:00:00Z","due_date":"2026-12-01"}`, `{"due_date":"next week"}`,
			`{"remind_at":"tomorrow"}`, `{"priority":"urgent"}`, `{"due_at":7}`} {
			if w := e.do("PATCH", "/api/v1/todos/"+id, body); w.Code != http.StatusBadRequest {
				t.Errorf("%s: %d %s", body, w.Code, w.Body)
			}
		}
		if w := e.do("POST", "/api/v1/todos", `{"title":"x","priority":"urgent"}`); w.Code != http.StatusBadRequest {
			t.Errorf("POST with an unknown priority: %d", w.Code)
		}
	})
}

// A reminder that is due is pushed once, with the to-do's title and due date, and marked pushed; a new time re-arms
// it; a done to-do's is not pushed; a future one is the next to wake for.
func TestTodoReminders(t *testing.T) {
	todoStores(t, func(t *testing.T, e *todoEnv, _ *db.DB) {
		ctx := context.Background()
		past := time.Now().Add(-time.Second).UTC().Format(time.RFC3339)
		created := func(body string) string {
			var res struct{ Todo Todo }
			_ = json.Unmarshal(e.must("POST", "/api/v1/todos", body).Body.Bytes(), &res)
			return res.Todo.ID
		}
		id := created(`{"title":"Call the bank","due_date":"2026-12-01","remind_at":"` + past + `"}`)
		e.must("PATCH", "/api/v1/todos/"+created(`{"title":"Done already","remind_at":"`+past+`"}`), `{"done":true}`)
		future := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
		e.must("POST", "/api/v1/todos", `{"title":"Later","remind_at":"`+future.Format(time.RFC3339)+`"}`)

		if next := e.s.remindDue(ctx, time.Now()); !next.Equal(future) {
			t.Errorf("next reminder %v, want %v", next, future)
		}
		pushes := e.f.pushed()
		if len(pushes) != 1 || pushes[0].Title != "Call the bank" || !strings.Contains(pushes[0].Body, "12-01") {
			t.Fatalf("pushes: %+v", pushes)
		}
		// The owner's own reminder is theirs to act on: P0 even with push tiering on, keyed by to-do and due time.
		if pushes[0].Level != "P0" || !strings.HasPrefix(pushes[0].RelatedKey, "todo-remind:"+id+"@") {
			t.Fatalf("reminder level %q key %q", pushes[0].Level, pushes[0].RelatedKey)
		}
		if got, _ := e.todo(id); got.RemindedAt == nil {
			t.Fatal("not marked pushed")
		}
		e.s.remindDue(ctx, time.Now())
		if n := len(e.f.pushed()); n != 1 {
			t.Fatalf("pushed again: %d", n)
		}
		again := time.Now().Add(-time.Millisecond).UTC().Format(time.RFC3339Nano)
		e.must("PATCH", "/api/v1/todos/"+id, `{"remind_at":"`+again+`"}`)
		e.s.remindDue(ctx, time.Now())
		if n := len(e.f.pushed()); n != 2 {
			t.Fatalf("a new reminder time was not pushed: %d", n)
		}
	})
}

// The tools list open to-dos first, then by due moment (a due day counts to its end), those without one last,
// higher priority first among equals.
func TestSortTodosByDue(t *testing.T) {
	loc := time.UTC
	at := func(h int) *time.Time { v := time.Date(2026, 11, 10, h, 0, 0, 0, loc); return &v }
	done := at(1)
	list := []Todo{
		{ID: "none_low", Priority: db.PriorityLow},
		{ID: "none_high", Priority: db.PriorityHigh},
		{ID: "day10", DueDate: "2026-11-10"},
		{ID: "at9", DueAt: at(9)},
		{ID: "done_at8", DueAt: at(8), DoneAt: done},
		{ID: "at23", DueAt: at(23)},
	}
	sortTodosByDue(list, loc)
	var got []string
	for _, t := range list {
		got = append(got, t.ID)
	}
	if want := "at9 at23 day10 none_high none_low done_at8"; strings.Join(got, " ") != want {
		t.Fatalf("order %v, want %s", got, want)
	}
}
