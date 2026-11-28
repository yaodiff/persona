package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
)

func repositoryDuplicateService(t *testing.T, links ...string) (*Service, *fakeHub, *db.DB, Todo) {
	t.Helper()
	database, err := db.Open(t.Context(), newDatabase(t), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	s, f := newQuietServiceOn(t, database)
	s.briefsOptional = false
	todo, err := s.createTodo(t.Context(), "Repair the parser", "Existing work", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	addRepositoryDuplicateLinks(t, s, todo.ID, links...)
	return s, f, database, todo
}

func addRepositoryDuplicateLinks(t *testing.T, s *Service, id string, links ...string) {
	t.Helper()
	if len(links) == 0 {
		return
	}
	raw, _ := json.Marshal(map[string]any{"id": id, "urls": links})
	if _, err := s.toolTodoRepositoryLinksAdd(t.Context(), "", "", "todo_repository_links_add", raw); err != nil {
		t.Fatal(err)
	}
}

func assertRepositoryDuplicateNoDispatch(t *testing.T, s *Service, f *fakeHub, database *db.DB, plans, todos int) {
	t.Helper()
	saved, err := database.TaskPlans(t.Context(), "conv1")
	if err != nil || len(saved) != plans || todoCount(s) != todos || len(f.started()) != 0 {
		t.Fatalf("refusal had side effects: plans=%d, todos=%d, starts=%d, error=%v",
			len(saved), todoCount(s), len(f.started()), err)
	}
}

func TestTaskPlanRepositoryDuplicateRefusesBeforeSideEffects(t *testing.T) {
	links := []string{
		"https://github.com/example/project/issues/17",
		"https://github.com/example/project/pull/17",
		"https://gitlab.com/group/subgroup/project/-/issues/17",
		"https://gitlab.com/group/subgroup/project/-/merge_requests/17",
		"http://gitlab.example.com:8080/group/project/-/issues/17",
		"https://github.example.com/group/project/pull/17",
	}
	s, f, database, todo := repositoryDuplicateService(t, links...)
	for _, link := range links {
		for _, field := range []string{"title", "goal", "prompt"} {
			t.Run(field+"/"+link, func(t *testing.T) {
				task := briefTask(map[string]any{field: "Follow up on " + link, "user_requested": true})
				got, isErr := tool(t, s, "task_plan", task)
				if !isErr {
					t.Fatalf("accepted a fresh duplicate task: %s", got)
				}
				for _, want := range []string{todo.ID, todo.Title, link, "continues", "continues_why", "todo_id", "distinct_from"} {
					if !strings.Contains(got, want) {
						t.Errorf("refusal lacks %q: %s", want, got)
					}
				}
				assertRepositoryDuplicateNoDispatch(t, s, f, database, 0, 1)
			})
		}
	}
	t.Run("reference beyond the goal display limit", func(t *testing.T) {
		got, isErr := tool(t, s, "task_plan", briefTask(map[string]any{"goal": strings.Repeat("x", 310) + " " + links[0]}))
		if !isErr || !strings.Contains(got, todo.ID) {
			t.Fatalf("goal clipping hid the reference: %s", got)
		}
		assertRepositoryDuplicateNoDispatch(t, s, f, database, 0, 1)
	})
	t.Run("whole plan is refused", func(t *testing.T) {
		first := briefTask(map[string]any{"user_requested": true})["tasks"].([]map[string]any)[0]
		second := briefTask(map[string]any{"prompt": links[0]})["tasks"].([]map[string]any)[0]
		got, isErr := tool(t, s, "task_plan", map[string]any{"tasks": []map[string]any{first, second}})
		if !isErr || !strings.Contains(got, "task 2") {
			t.Fatalf("accepted part of a duplicate plan: %s", got)
		}
		assertRepositoryDuplicateNoDispatch(t, s, f, database, 0, 1)
	})
}

func TestTaskPlanRepositoryReferencesNormalizeIdentity(t *testing.T) {
	const canonical = "https://github.com/example/project/issues/17"
	for _, raw := range []string{
		canonical,
		"[issue](" + canonical + ").",
		"https://GITHUB.COM/Example/Project/issues/17#issuecomment-9",
		"http://github.com/example/project/issues/17/?utm_source=task",
	} {
		if got, err := planRepositoryReferences(PlanTask{Prompt: raw}); err != nil || len(got) != 1 || !got[canonical] {
			t.Errorf("reference %q: %v, %v", raw, got, err)
		}
	}
	for _, raw := range []string{
		"#17", "example/project#17", "https://github.com/example/project",
		"https://github.com/example/project/issues/017",
		"https://github.com/example/project/issues/17extra",
	} {
		if got, err := planRepositoryReferences(PlanTask{Prompt: raw}); err != nil || len(got) != 0 {
			t.Errorf("not an exact issue/PR reference %q: %v, %v", raw, got, err)
		}
	}
}

func TestTaskPlanRepositoryReferencesRejectUnsafeURLs(t *testing.T) {
	links := []string{
		"https://github.com/example/project/issues/17",
		"https://github.com/example/project/pull/17",
		"https://gitlab.com/group/project/-/issues/17",
		"https://gitlab.example.com/group/project/-/merge_requests/17",
	}
	for _, link := range links {
		for _, key := range []string{"token", "access_token", "private_token", "auth", "authorization", "password", "api_key", "apikey", "signature", "PRIVATE_TOKEN", "private%5Ftoken"} {
			raw := link + "?utm_source=task&" + key + "=test-only"
			refs, err := planRepositoryReferences(PlanTask{Prompt: raw})
			if err == nil || !strings.Contains(err.Error(), "unsafe repository") || len(refs) != 0 ||
				strings.Contains(err.Error(), raw) || strings.Contains(err.Error(), "test-only") {
				t.Fatalf("credential-bearing %s reference was accepted or echoed: refs=%v, error=%v", key, refs, err)
			}
		}
	}
	for _, raw := range []string{
		"https://me@example.com/example/project/issues/17",
		links[0] + "?private_token=%zz",
		links[0] + "?tracking=one;private_token=test-only",
		links[0] + "?private_token=test-only#%zz",
		links[0] + "#%zz",
		"https://%zz:@github.com/example/project/issues/17",
		"https://github.com:invalid/example/project/issues/17",
		"https://github.com/example/proj%zz/issues/17",
		links[3] + "#%zz",
	} {
		if refs, err := planRepositoryReferences(PlanTask{Prompt: raw}); err == nil || !strings.Contains(err.Error(), "unsafe repository") ||
			len(refs) != 0 || strings.Contains(err.Error(), raw) || strings.Contains(err.Error(), "test-only") {
			t.Fatalf("unsafe reference %q was accepted or echoed: refs=%v, error=%v", raw, refs, err)
		}
	}
	if refs, err := planRepositoryReferences(PlanTask{Prompt: "https://example.com/docs#%zz"}); err != nil || len(refs) != 0 {
		t.Fatalf("unrelated malformed URL was treated as a repository reference: refs=%v, error=%v", refs, err)
	}
}

func TestTaskPlanRepositoryRejectsUnsafeURLsBeforePlanning(t *testing.T) {
	const link = "https://github.com/example/project/issues/17"
	s, f, database, todo := repositoryDuplicateService(t, link)
	for _, field := range []string{"title", "goal", "prompt"} {
		for _, mode := range []string{"new", "distinct", "continues", "todo_id"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				unsafe := link + "?private_token=test-only"
				over := map[string]any{field: unsafe, "user_requested": true}
				switch mode {
				case "distinct":
					over["distinct_from"] = []TaskDistinctFrom{{TodoID: todo.ID, Reason: "Separate investigation"}}
				case "continues", "todo_id":
					over[mode], over["continues_why"] = todo.ID, "Follow-up requested by the owner"
				}
				got, isErr := tool(t, s, "task_plan", briefTask(over))
				if !isErr || !strings.Contains(got, "unsafe repository issue/PR URL") ||
					strings.Contains(got, unsafe) || strings.Contains(got, "test-only") {
					t.Fatalf("unsafe URL was admitted or echoed: %s", got)
				}
				assertRepositoryDuplicateNoDispatch(t, s, f, database, 0, 1)
			})
		}
	}
	t.Run("whole plan without a saved match", func(t *testing.T) {
		first := briefTask(nil)["tasks"].([]map[string]any)[0]
		second := briefTask(map[string]any{"prompt": "https://github.com/example/other/pull/19?signature=test-only#%zz"})["tasks"].([]map[string]any)[0]
		got, isErr := tool(t, s, "task_plan", map[string]any{"tasks": []map[string]any{first, second}})
		if !isErr || !strings.Contains(got, "task 2: unsafe repository") || strings.Contains(got, "test-only") {
			t.Fatalf("unsafe second task was admitted or echoed: %s", got)
		}
		assertRepositoryDuplicateNoDispatch(t, s, f, database, 0, 1)
	})
}

func TestTaskPlanRepositoryRechecksUnsafeURLAtStart(t *testing.T) {
	s, f, database, _ := repositoryDuplicateService(t)
	if got, isErr := tool(t, s, "task_plan", briefTask(map[string]any{"readonly": false})); isErr {
		t.Fatal(got)
	}
	rows, err := database.TaskPlans(t.Context(), "conv1")
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	edits := []PlanTask{{Prompt: "https://github.com/example/project/issues/17?private_token=test-only"}}
	if _, err := s.startPlan(t.Context(), rows[0].ID, edits, false); err == nil ||
		!strings.Contains(err.Error(), "unsafe repository") || strings.Contains(err.Error(), "test-only") {
		t.Fatalf("waiting card dispatched or echoed an unsafe URL: %v", err)
	}
	assertRepositoryDuplicateNoDispatch(t, s, f, database, 1, 1)
	plan, err := s.loadPlan(t.Context(), rows[0].ID)
	if err != nil || plan.State != planOpen {
		t.Fatal("refused card no longer waits", plan, err)
	}
}

func TestTaskPlanRepositoryDuplicateUsesOnlySavedOpenLinks(t *testing.T) {
	const link = "https://github.com/example/project/issues/17"
	for _, tc := range []struct {
		name   string
		saved  string
		prompt string
		done   bool
	}{
		{"different repository", link, "https://github.com/example/other/issues/17", false},
		{"different provider", link, "https://gitlab.com/example/project/-/issues/17", false},
		{"different host", link, "https://github.example.com/example/project/issues/17", false},
		{"different kind", link, "https://github.com/example/project/pull/17", false},
		{"different number", link, "https://github.com/example/project/issues/18", false},
		{"different self-hosted scheme", "http://gitlab.example.com/group/project/-/issues/17", "https://gitlab.example.com/group/project/-/issues/17", false},
		{"different self-hosted port", "https://gitlab.example.com:8080/group/project/-/issues/17", "https://gitlab.example.com/group/project/-/issues/17", false},
		{"bare number stays advisory", link, "Follow up on #17", false},
		{"notes and legacy PR URL are not saved associations", "", link, false},
		{"closed to-do", link, link, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var links []string
			if tc.saved != "" {
				links = []string{tc.saved}
			}
			s, f, database, todo := repositoryDuplicateService(t, links...)
			s.todos.mu.Lock()
			original := s.todos.todos[todo.ID]
			original.Title, original.Notes, original.PRURL = "Fix the login crash", tc.prompt, tc.prompt
			if tc.done {
				original.DoneAt = new(time.Now())
			}
			s.todos.mu.Unlock()
			got, isErr := tool(t, s, "task_plan", briefTask(map[string]any{"readonly": false, "prompt": tc.prompt}))
			if isErr {
				t.Fatalf("refused unrelated or closed work: %s", got)
			}
			assertRepositoryDuplicateNoDispatch(t, s, f, database, 1, 1)
			// Admission reads must not import a link from notes or legacy metadata.
			set, err := repositorystore.New(database).TodoRepositories(t.Context(), todo.ID)
			if err != nil || tc.saved == "" && len(set.Repositories) != 0 {
				t.Fatalf("read changed saved associations: %+v, %v", set, err)
			}
		})
	}
}

func TestTaskPlanRepositoryDuplicateDistinctReasonsPersist(t *testing.T) {
	const link = "https://github.com/example/project/issues/17"
	s, f, database, todo := repositoryDuplicateService(t, link)
	other, err := s.createTodo(t.Context(), "Document the parser", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	addRepositoryDuplicateLinks(t, s, other.ID, link)
	valid := []TaskDistinctFrom{
		{TodoID: todo.ID, Reason: "This task documents the public interface; the existing task repairs parsing."},
		{TodoID: other.ID, Reason: "This task covers migration examples, not the existing reference documentation."},
	}
	for _, tc := range []struct {
		name string
		in   any
	}{
		{"missing", nil},
		{"empty reason", []TaskDistinctFrom{{TodoID: todo.ID, Reason: " \n "}}},
		{"missing todo", []TaskDistinctFrom{{Reason: "Separate work"}}},
		{"wrong todo", []TaskDistinctFrom{{TodoID: "todo_other", Reason: "Separate work"}}},
		{"only one of two conflicts", valid[:1]},
		{"duplicate declarations", []TaskDistinctFrom{valid[0], valid[0]}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, isErr := tool(t, s, "task_plan", briefTask(map[string]any{"prompt": link, "distinct_from": tc.in}))
			if !isErr {
				t.Fatalf("accepted invalid distinct-work declaration: %s", got)
			}
			assertRepositoryDuplicateNoDispatch(t, s, f, database, 0, 2)
		})
	}
	got, isErr := tool(t, s, "task_plan", briefTask(map[string]any{
		"readonly": false, "prompt": link, "distinct_from": valid,
	}))
	if isErr {
		t.Fatal(got)
	}
	rows, err := database.TaskPlans(t.Context(), "conv1")
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	plan, err := s.loadPlan(t.Context(), rows[0].ID)
	if err != nil || plan.Tasks[0].Brief == nil || !slices.Equal(plan.Tasks[0].Brief.DistinctFrom, valid) {
		t.Fatalf("plan brief lost reasons: %+v, %v", plan, err)
	}
	// The original may close while the distinct-work card waits; that is not a new conflict.
	s.todos.mu.Lock()
	s.todos.todos[other.ID].DoneAt = new(time.Now())
	s.todos.mu.Unlock()
	started, err := s.startPlan(t.Context(), plan.ID, nil, false)
	if err != nil || started.Tasks[0].TodoID == "" {
		t.Fatalf("separate work could not start: %+v, %v", started, err)
	}
	waitFor(t, "distinct worker start", func() bool { return len(f.started()) == 1 })
	restarted, _ := newQuietServiceOn(t, database)
	if err := restarted.ensureTodosLoaded(t.Context()); err != nil {
		t.Fatal(err)
	}
	brief, ok := restarted.briefOfTodo(started.Tasks[0].TodoID)
	if !ok || !slices.Equal(brief.DistinctFrom, valid) {
		t.Fatalf("stored brief lost distinct-work reasons after restart: %+v", brief)
	}
	brief.DistinctFrom[0].Reason = "Changed by a reader"
	again, _ := restarted.briefOfTodo(started.Tasks[0].TodoID)
	if !slices.Equal(again.DistinctFrom, valid) {
		t.Fatal("a returned brief aliases its immutable record")
	}
}

func TestTaskPlanRepositoryDuplicatePreservesExplicitReuse(t *testing.T) {
	for _, field := range []string{"continues", "todo_id"} {
		t.Run(field, func(t *testing.T) {
			const link = "https://github.com/example/project/issues/17"
			s, f, database, todo := repositoryDuplicateService(t, link)
			args := briefTask(map[string]any{"readonly": false, "prompt": link, field: todo.ID, "continues_why": "Owner-requested follow-up"})
			got, isErr := tool(t, s, "task_plan", args)
			if isErr {
				t.Fatalf("refused explicit %s: %s", field, got)
			}
			assertRepositoryDuplicateNoDispatch(t, s, f, database, 1, 1)
		})
	}
}

func TestTaskPlanRepositoryDuplicateRechecksWaitingCards(t *testing.T) {
	const link = "https://github.com/example/project/issues/17"
	for _, edit := range []bool{false, true} {
		t.Run(fmt.Sprintf("edited=%t", edit), func(t *testing.T) {
			s, f, database, todo := repositoryDuplicateService(t)
			prompt := link
			if edit {
				prompt = "Unrelated work"
				addRepositoryDuplicateLinks(t, s, todo.ID, link)
			}
			got, isErr := tool(t, s, "task_plan", briefTask(map[string]any{"readonly": false, "prompt": prompt}))
			if isErr {
				t.Fatal(got)
			}
			rows, err := database.TaskPlans(t.Context(), "conv1")
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			var edits []PlanTask
			if edit {
				// Card edits cannot inject an unvalidated distinct-work override.
				edits = []PlanTask{{Prompt: link, DistinctFrom: []TaskDistinctFrom{{TodoID: todo.ID, Reason: "Injected by an edit"}}}}
			} else {
				addRepositoryDuplicateLinks(t, s, todo.ID, link)
			}
			if _, err := s.startPlan(t.Context(), rows[0].ID, edits, false); err == nil || !strings.Contains(err.Error(), todo.ID) {
				t.Fatalf("waiting card bypassed conflict: %v", err)
			}
			assertRepositoryDuplicateNoDispatch(t, s, f, database, 1, 1)
			plan, err := s.loadPlan(t.Context(), rows[0].ID)
			if err != nil || plan.State != planOpen {
				t.Fatal("refused card no longer waits", plan, err)
			}
		})
	}
}

type failedDuplicateRepositoryStore struct{ *repositorystore.Store }

func (failedDuplicateRepositoryStore) TodoRepositories(context.Context, string) (repository.TaskSet, error) {
	return repository.TaskSet{}, errors.New("test repository read failure")
}

func TestTaskPlanRepositoryDuplicateFailsClosedOnReadError(t *testing.T) {
	const link = "https://github.com/example/project/issues/17"
	_, _, database, _ := repositoryDuplicateService(t, link)
	s, f := newQuietServiceOn(t, failedDuplicateRepositoryStore{repositorystore.New(database)})
	got, isErr := tool(t, s, "task_plan", briefTask(map[string]any{"prompt": link}))
	if !isErr || !strings.Contains(got, "test repository read failure") {
		t.Fatalf("repository read failure was ignored: %s", got)
	}
	assertRepositoryDuplicateNoDispatch(t, s, f, database, 0, 1)
}

func TestTaskPlanRepositoryDuplicateFailsClosedWithoutStore(t *testing.T) {
	s, f := newQuietService(t)
	if _, err := s.createTodo(t.Context(), "Existing work", "", "", ""); err != nil {
		t.Fatal(err)
	}
	got, isErr := tool(t, s, "task_plan", briefTask(map[string]any{"prompt": "https://github.com/example/project/issues/17"}))
	if !isErr || !strings.Contains(got, "repository links are unavailable") || todoCount(s) != 1 || len(f.started()) != 0 {
		t.Fatalf("missing association store was ignored: %s", got)
	}
}

func TestTaskPlanRepositoryDuplicateLeaderCannotBypass(t *testing.T) {
	const link = "https://github.com/example/project/issues/17"
	s, f, database, todo := repositoryDuplicateService(t, link)
	scope := &leaderScope{task: todo, agent: TodoAgent{MachineID: "box", SessionID: "leader", Cwd: "/code/app"}}
	task := briefTask(map[string]any{"readonly": false, "prompt": link})
	raw, _ := json.Marshal(task)
	if _, err := s.taskPlanFrom(t.Context(), "box", "leader:box/leader", raw, scope); err == nil || !strings.Contains(err.Error(), todo.ID) {
		t.Fatalf("leader worker bypassed conflict: %v", err)
	}
	assertRepositoryDuplicateNoDispatch(t, s, f, database, 0, 1)
	task = briefTask(map[string]any{"readonly": false, "prompt": link,
		"distinct_from": []TaskDistinctFrom{{TodoID: todo.ID, Reason: "A bounded documentation worker, separate from the parent implementation."}}})
	raw, _ = json.Marshal(task)
	tasks, err := scope.workerTasks(raw)
	if err != nil || len(tasks) != 1 || len(tasks[0].DistinctFrom) != 1 {
		t.Fatal("leader schema lost the explicit distinction", tasks, err)
	}
	if err := s.checkPlanRepositoryDuplicates(t.Context(), tasks); err != nil {
		t.Fatal(err)
	}
}
