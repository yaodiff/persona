package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// briefService is the main conversation conv1 on a fake hub, with task_plan held to what a brief requires, and the
// owner's message said in it at asked.
func briefService(t testing.TB, said string) (*Service, *fakeHub, time.Time) {
	t.Helper()
	return briefServiceOn(t, nil, said)
}

func briefServiceOn(t testing.TB, st Store, said string) (*Service, *fakeHub, time.Time) {
	t.Helper()
	s, f := newQuietServiceOn(t, st)
	s.briefsOptional = false
	asked := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
	f.setItems(proto.Item{ID: "m1", Seq: 1, TS: asked, Role: "user", Kind: proto.KindText, Text: said})
	return s, f, asked
}

// tool is a model's call of an assistant tool from conv1, as the hub passes it on.
func tool(t testing.TB, s *Service, name string, args any) (string, bool) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Tool(context.Background(), ToolCall{Name: name, Arguments: raw, MachineID: "server", SessionID: "conv1"})
	if len(r.Content) == 0 {
		t.Fatalf("%s: no answer", name)
	}
	return r.Content[0].Text, r.IsError
}

func briefTask(over map[string]any) map[string]any {
	task := map[string]any{"title": "fix the login crash", "prompt": "the login page crashes in landscape: fix it and add a test",
		"role": "worker", "machine": "box", "cwd": "/code/app", "readonly": true, "permission": "bypassPermissions", "why": "the repository is on box",
		"goal": "the login page no longer crashes in landscape", "done_when": []string{"PR merged to main", "a test covers landscape"},
		"expect_minutes": 30}
	for k, v := range over {
		if v == nil {
			delete(task, k)
		} else {
			task[k] = v
		}
	}
	return map[string]any{"tasks": []map[string]any{task}}
}

func todoByTitle(s *Service, title string) (Todo, bool) {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for _, t := range s.todos.todos {
		if t.Title == title {
			return copyTodoLocked(t), true
		}
	}
	return Todo{}, false
}

// task_plan refuses a writable task without criteria and a read-only one without a goal. Missing time is automatic;
// a negative override is still invalid. Refusals name what is missing and plan nothing.
func TestTaskPlanRequiresABrief(t *testing.T) {
	s, _, _ := briefService(t, "please fix the login crash")
	for _, c := range []struct {
		name string
		over map[string]any
		want []string
	}{
		{"writable, nothing", map[string]any{"readonly": false, "done_when": nil, "expect_minutes": nil}, []string{"a writable task needs", "done_when"}},
		{"writable, blank criteria", map[string]any{"readonly": false, "done_when": []string{" ", ""}}, []string{"done_when"}},
		{"negative time", map[string]any{"expect_minutes": -1}, []string{"expect_minutes"}},
		{"read-only, no goal", map[string]any{"goal": nil, "done_when": nil}, []string{"a read-only task needs", "goal"}},
		{"continues without why", map[string]any{"continues": "todo_x"}, []string{"continues_why"}},
	} {
		got, isErr := tool(t, s, "task_plan", briefTask(c.over))
		if !isErr {
			t.Fatalf("%s: accepted: %s", c.name, got)
		}
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: the refusal does not say %q: %s", c.name, w, got)
			}
		}
	}
	if strings.Contains(func() string {
		got, _ := tool(t, s, "task_plan", briefTask(map[string]any{"readonly": false, "expect_minutes": -1}))
		return got
	}(), "done_when (") {
		t.Fatal("a refusal for an invalid time must not ask for criteria that were given")
	}
	s.todos.mu.RLock()
	n := len(s.todos.todos)
	s.todos.mu.RUnlock()
	if n != 0 {
		t.Fatalf("%d to-dos were made by refused plans", n)
	}
	// A read-only task needs no criteria; a writable one with criteria is planned with an automatic estimate (its card waits).
	if got, isErr := tool(t, s, "task_plan", briefTask(map[string]any{"done_when": nil})); isErr {
		t.Fatalf("read-only with a goal and a time: %s", got)
	}
	if got, isErr := tool(t, s, "task_plan", briefTask(map[string]any{"readonly": false, "goal": nil})); isErr {
		t.Fatalf("writable with criteria and a time: %s", got)
	}
}

func TestTaskBriefContractAcrossTodoModes(t *testing.T) {
	t.Run("memory", func(t *testing.T) { testTaskBriefContractAcrossTodoModes(t, nil) })
	t.Run("postgres", func(t *testing.T) { testTaskBriefContractAcrossTodoModes(t, todoTestDB(t)) })
}

func testTaskBriefContractAcrossTodoModes(t *testing.T, store Store) {
	t.Helper()
	s, _, _ := briefServiceOn(t, store, "prepare the weekly report")
	ctx := t.Context()
	plan, failed := tool(t, s, "task_plan", briefTask(map[string]any{
		"goal": "Prepare the weekly report", "done_when": []string{"A draft report is available"},
		"work_mode": "coordinate", "completion_mode": "draft",
	}))
	if failed {
		t.Fatal(plan)
	}
	waitFor(t, "the agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	brief, ok := s.briefOfTodo(todo.ID)
	if !ok {
		t.Fatal("missing brief")
	}
	assertBriefFields := func(brief TaskBrief, fields ...string) {
		t.Helper()
		raw, err := json.Marshal(brief)
		if err != nil {
			t.Fatal(err)
		}
		for _, field := range fields {
			if !strings.Contains(string(raw), field) {
				t.Fatalf("brief missing %s: %s", field, raw)
			}
		}
	}
	assertBriefFields(brief, `"work_mode":"coordinate"`, `"completion_mode":"draft"`,
		`"work_mode_now":"coordinate"`, `"completion_mode_now":"draft"`)

	setTodosV2Enabled(t, s, true)
	if _, err := runJSON(t, s, "conv1", "task_scope", map[string]any{
		"todo_id": todo.ID, "change": "Make the report ready for review", "why": "The draft is complete",
		"work_mode": "research", "completion_mode": "ready", "expect_minutes": 50,
	}); err != nil {
		t.Fatal(err)
	}
	changed, _ := s.briefOfTodo(todo.ID)
	if !changed.scopeRevision().After(brief.scopeRevision()) || changed.ExpectNow != 50 {
		t.Fatalf("the combined contract and estimate change did not advance the scope: %+v", changed)
	}
	event := changed.Log[len(changed.Log)-1]
	if event.Why != "The draft is complete" || event.Kind != briefEventScope {
		t.Fatalf("scope change was not audited: %+v", event)
	}

	setTodosV2Enabled(t, s, false)
	for _, on := range []bool{false, true} {
		setTodosV2Enabled(t, s, on)
		if s.settings(ctx).todosV2On() != on {
			t.Fatalf("todos_v2 did not switch to %t", on)
		}
		raw, err := s.todoView(ctx, todo.ID)
		if err != nil {
			t.Fatal(err)
		}
		var got Todo
		if err := json.Unmarshal(raw, &got); err != nil || got.Brief == nil {
			t.Fatalf("todos_v2=%t brief: %s (%v)", on, raw, err)
		}
		assertBriefFields(*got.Brief, `"work_mode":"coordinate"`, `"completion_mode":"draft"`,
			`"work_mode_now":"research"`, `"completion_mode_now":"ready"`,
			`"work_mode":"research"`, `"completion_mode":"ready"`)
		if got.Brief.ID != brief.ID || !slices.Equal(got.Brief.Criteria, brief.Criteria) {
			t.Fatalf("todos_v2=%t changed the brief or criteria: %+v", on, got.Brief)
		}
	}
	if _, err := runJSON(t, s, "conv1", "task_scope", map[string]any{
		"todo_id": todo.ID, "change": "Use the workspace delivery policy", "why": "Review is complete",
		"completion_mode": "inherit",
	}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.briefOfTodo(todo.ID)
	last := after.Log[len(after.Log)-1]
	if after.WorkModeNow != assistantwire.WorkModeResearch || after.CompletionModeNow != assistantwire.CompletionModeInherit ||
		last.WorkMode != "" || last.CompletionMode != assistantwire.CompletionModeInherit {
		t.Fatalf("completion-only edit changed the work mode or recorded the wrong event: %+v", after)
	}
	if store != nil {
		restored, _, _ := briefServiceOn(t, store, "")
		if err := restored.ensureTodosLoaded(ctx); err != nil {
			t.Fatal(err)
		}
		got, ok := restored.briefOfTodo(todo.ID)
		if !ok || got.ID != after.ID || got.WorkMode != after.WorkMode || got.CompletionMode != after.CompletionMode ||
			got.WorkModeNow != after.WorkModeNow || got.CompletionModeNow != after.CompletionModeNow ||
			len(got.Log) != len(after.Log) || got.Log[len(got.Log)-1].CompletionMode != last.CompletionMode {
			t.Fatalf("the stored brief did not survive a restart: %+v", got)
		}
	}
}

func TestTaskBriefContractToolValidationAndAdoption(t *testing.T) {
	s, f, _ := briefService(t, "prepare the weekly report")
	secretary(f)
	for field, value := range map[string]string{"work_mode": "code", "completion_mode": ""} {
		if got, failed := tool(t, s, "task_plan", briefTask(map[string]any{field: value})); !failed || !strings.Contains(got, field) {
			t.Fatalf("task_plan %s = %s", field, got)
		}
		if got, failed := tool(t, s, "agent_supervise", map[string]any{
			"machine": "box", "session_id": "sec00001", field: value,
		}); !failed || !strings.Contains(got, field) {
			t.Fatalf("agent_supervise %s = %s", field, got)
		}
	}
	if todo := onlyTodo(s); todo.ID != "" {
		t.Fatalf("an invalid contract made a to-do: %+v", todo)
	}
	if got, failed := tool(t, s, "agent_supervise", map[string]any{
		"machine": "box", "session_id": "sec00001", "work_mode": "coordinate", "completion_mode": "verified",
	}); failed {
		t.Fatal(got)
	}
	todo := onlyTodo(s)
	brief, ok := s.briefOfTodo(todo.ID)
	if !ok || brief.WorkMode != assistantwire.WorkModeCoordinate || brief.WorkModeNow != brief.WorkMode ||
		brief.CompletionMode != assistantwire.CompletionModeVerified || brief.CompletionModeNow != brief.CompletionMode {
		t.Fatalf("adopted brief = %+v", brief)
	}
	if got, failed := tool(t, s, "task_scope", map[string]any{
		"todo_id": todo.ID, "change": "Revisit the mode", "why": "The scope changed", "completion_mode": "released",
	}); !failed || !strings.Contains(got, "completion_mode") {
		t.Fatalf("task_scope with an invalid mode: %s", got)
	}
	if got, _ := s.briefOfTodo(todo.ID); len(got.Log) != len(brief.Log) || got.CompletionModeNow != brief.CompletionModeNow {
		t.Fatalf("invalid scope change was recorded: %+v", got)
	}
}

func TestScopeContractKeepsOwnerApproval(t *testing.T) {
	s, f, asked := briefService(t, "Prepare a draft PR. Do not merge it.")
	if got, failed := tool(t, s, "task_plan", briefTask(map[string]any{
		"work_mode": "coordinate", "completion_mode": "draft",
		"goal": "Prepare a draft PR", "done_when": []string{"A draft PR is available"},
	})); failed {
		t.Fatal(got)
	}
	waitFor(t, "the agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	before, _ := s.briefOfTodo(todo.ID)
	args := map[string]any{
		"todo_id": todo.ID, "change": "Require landed delivery", "why": "The review is complete",
		"work_mode": "execute", "completion_mode": "landed", "expect_minutes": 40,
	}
	if _, err := runJSON(t, s, "conv1", "task_scope", args); err == nil || !strings.Contains(err.Error(), "only the owner may allow (merge") {
		t.Fatalf("scope change ignored the owner's restriction: %v", err)
	}
	if got, _ := s.briefOfTodo(todo.ID); len(got.Log) != len(before.Log) || got.CompletionModeNow != before.CompletionModeNow {
		t.Fatalf("refused scope change was applied: %+v", got)
	}
	f.setItems(proto.Item{ID: "m2", Seq: 2, TS: asked.Add(time.Hour), Role: "user", Kind: proto.KindText, Text: "Yes, merge the PR now."})
	if got, failed := tool(t, s, "task_scope", args); failed {
		t.Fatal(got)
	}
	after, _ := s.briefOfTodo(todo.ID)
	event := after.Log[len(after.Log)-1]
	if event.Approval == nil || event.Approval.MessageID != "m2" || event.Kind != briefEventScope ||
		after.WorkModeNow != assistantwire.WorkModeExecute || after.CompletionModeNow != assistantwire.CompletionModeLanded {
		t.Fatalf("approved scope change was not recorded: %+v", after)
	}
}

// The plan card carries the brief before anything starts: the owner's words as the hub read them, the criteria and the
// time; a brief the tool call supplies itself is dropped.
func TestPlanCardCarriesTheBrief(t *testing.T) {
	s, _, asked := briefService(t, "please fix the login crash before Friday")
	args := briefTask(map[string]any{"readonly": false, "out_of_bounds": []string{"do not merge"},
		"brief": map[string]any{"request": map[string]any{"text": "made up by the model"}}})
	if got, isErr := tool(t, s, "task_plan", args); isErr || !strings.Contains(got, "nothing has started") {
		t.Fatalf("task_plan: %s", got)
	}
	s.tasks.mu.Lock()
	var plan *TaskPlan
	for _, p := range s.tasks.plans {
		plan = p
	}
	s.tasks.mu.Unlock()
	if plan == nil || plan.State != planOpen || plan.Tasks[0].Brief == nil {
		t.Fatalf("plan = %+v", plan)
	}
	b := plan.Tasks[0].Brief
	if b.Request == nil || b.Request.Text != "please fix the login crash before Friday" || !b.Request.At.Equal(asked) ||
		b.Request.Conversation != "conv1" || b.Request.MessageID != "m1" || b.StartedBy != briefByOwner {
		t.Fatalf("request = %+v (%s)", b.Request, b.StartedBy)
	}
	if b.Goal != "the login page no longer crashes in landscape" || len(b.DoneWhen) != 2 || b.ExpectMinutes != 30 ||
		len(b.OutOfBounds) != 1 || len(b.Criteria) != 2 {
		t.Fatalf("brief = %+v", b)
	}
	if n := len(s.briefTodos(b.ID)); n != 0 {
		t.Fatalf("a brief was kept before the plan started (%d to-dos)", n)
	}
}

// A rejected takeover scope aborts the adoption: the new card cannot keep the unapproved criterion or a linked Agent.
func TestRiskyTakeOverScopeAbortsAdoption(t *testing.T) {
	s, f, asked := briefService(t, "please fix the login crash before Friday")
	closed := time.Now().UTC()
	putTodo(s, Todo{ID: "todo_previous", Title: "fix the login crash", Origin: "conv1", DoneAt: &closed,
		Supervision: &Supervision{Supervisor: "conv1", DoneWhen: []string{"a test covers landscape"}, ExpectMinutes: 30},
		Agents:      []TodoAgent{{MachineID: "box", SessionID: "agent-old-1"}}})
	first := todoNow(s, "todo_previous")
	if _, err := s.ensureTaskBrief(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	f.setSessions("box", proto.Session{SessionID: "agent-old-1", MachineID: "box", Cwd: "/code/app", Agent: "claude",
		Title: "fix the login crash", Status: proto.StatusIdle, State: proto.StateIdle, UpdatedAt: time.Now()})
	f.setItems(
		proto.Item{ID: "m1", Seq: 1, TS: asked, Role: "user", Kind: proto.KindText, Text: "please fix the login crash before Friday"},
		proto.Item{ID: "m2", Seq: 2, TS: asked.Add(time.Hour), Role: "user", Kind: proto.KindText,
			Text: prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{{ID: first.ID, Title: first.Title, Kind: sigOrphaned}}})},
	)
	res, err := runJSON(t, s, "conv1", "agent_supervise", map[string]any{
		"machine": "box", "session_id": "agent-old-1",
		"done_when": []string{"PR merged to main", "send the release note to the customers"}, "expect_minutes": 5,
	})
	if err == nil || !strings.Contains(err.Error(), "only the owner may allow") {
		t.Fatalf("risky takeover was not refused: %s (%v)", res, err)
	}
	if got := todoCount(s); got != 2 {
		t.Fatalf("todos = %d, want the closed original and aborted card", got)
	}
	for _, todo := range s.todosOf(context.Background(), "*") {
		if todo.ID == first.ID {
			continue
		}
		if len(todo.Agents) != 0 {
			t.Fatalf("aborted takeover linked an Agent: %+v", todo.Agents)
		}
		if todo.Supervision != nil && strings.Contains(strings.Join(todo.Supervision.DoneWhen, "\n"), "deployed to production") {
			t.Fatalf("aborted takeover kept the unapproved scope: %+v", todo.Supervision)
		}
		if todo.DoneAt == nil {
			t.Fatalf("aborted takeover left an open card: %+v", todo)
		}
		if _, linked := s.briefOfTodo(todo.ID); linked {
			t.Fatalf("aborted takeover stayed on the previous task's brief: %+v", todo)
		}
	}
	if lineage := s.taskLineage(first.ID); len(lineage) != 1 || lineage[0].SupersededBy != "" {
		t.Fatalf("aborted takeover rewrote the previous task's lineage: %+v", lineage)
	}
}

// A task taken over twice still has the owner's original message and date, its criteria, and one lineage; each
// take-over's prompt is its own, and the review is against the original criteria.
func TestBriefSurvivesTakeOvers(t *testing.T) {
	s, f, asked := briefService(t, "please fix the login crash before Friday")
	if got, isErr := tool(t, s, "task_plan", briefTask(nil)); isErr {
		t.Fatal(got)
	}
	waitFor(t, "the first agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
	first := onlyTodo(s)
	brief, ok := s.briefOfTodo(first.ID)
	if !ok || brief.Request == nil || brief.Request.Text != "please fix the login crash before Friday" || brief.Reconstructed {
		t.Fatalf("brief = %+v", brief)
	}
	// Later turns of the conversation are the supervisor's own: the take-overs follow a supervision message.
	f.setItems(proto.Item{ID: "m1", Seq: 1, TS: asked, Role: "user", Kind: proto.KindText, Text: "please fix the login crash before Friday"},
		proto.Item{ID: "m2", Seq: 2, TS: asked.Add(time.Hour), Role: "user", Kind: proto.KindText,
			Text: prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{{ID: first.ID, Title: first.Title, Kind: sigOrphaned}}})})
	takeOver := func(title, prompt, from, why string) Todo {
		t.Helper()
		before := len(todoNow(s, first.ID).Agents)
		got, isErr := tool(t, s, "task_plan", briefTask(map[string]any{"title": title, "prompt": prompt, "continues": from, "continues_why": why,
			"done_when": []string{"something else entirely"}, "expect_minutes": 5, "goal": nil}))
		if isErr {
			t.Fatal(got)
		}
		var todo Todo
		waitFor(t, "the take-over "+title+": "+got, func() bool {
			todo, ok = s.todoNow(first.ID)
			return ok && len(todo.Agents) == before+1
		})
		return todo
	}
	second := takeOver("take over login", "you are taking over a session that stopped: carry on from its branch", first.ID, "its computer went offline")
	third := takeOver("retry login fix", "second hand-over: the branch is pushed, finish the test", second.ID, "the model service kept failing")
	for _, todo := range []Todo{second, third} {
		if todo.ID != first.ID || todoCount(s) != 1 {
			t.Fatalf("take-over split the original task: %+v", todo)
		}
		got, ok := s.briefOfTodo(todo.ID)
		if !ok || got.ID != brief.ID || got.Request.Text != brief.Request.Text || !got.Request.At.Equal(asked) {
			t.Fatalf("%s: brief = %+v", todo.Title, got)
		}
		if v := todo.Supervision; v == nil || strings.Join(v.DoneWhen, "|") != "PR merged to main|a test covers landscape" || v.ExpectMinutes != 30 {
			t.Fatalf("%s: the take-over's own criteria replaced the brief's: %+v", todo.Title, todo.Supervision)
		}
	}
	f.mu.Lock()
	lastPrompt, _ := f.starts[len(f.starts)-1]["prompt"].(string)
	f.mu.Unlock()
	if third.Notes != first.Notes || !strings.Contains(lastPrompt, "second hand-over: the branch is pushed, finish the test") {
		t.Fatalf("the original notes or the take-over's own prompt were lost: notes=%q, prompt=%q", third.Notes, lastPrompt)
	}
	// The to-do as the apps get it: one brief and lineage entry, with all Agents kept in its history.
	raw, err := s.todoView(context.Background(), third.ID)
	if err != nil {
		t.Fatal(err)
	}
	var view struct {
		Brief   *TaskBrief    `json:"brief"`
		Lineage []LineageStep `json:"lineage"`
	}
	if err := json.Unmarshal(raw, &view); err != nil || view.Brief == nil || view.Brief.Request.Text != brief.Request.Text {
		t.Fatalf("view = %s (%v)", raw, err)
	}
	if len(view.Lineage) != 1 || view.Lineage[0].TodoID != first.ID || !view.Lineage[0].Current ||
		view.Lineage[0].SupersededBy != "" || view.Lineage[0].Session == "" || len(third.Agents) != 3 {
		t.Fatalf("lineage = %+v", view.Lineage)
	}
	if b, _ := s.briefOfTodo(third.ID); b.Result != nil {
		t.Fatalf("cancelling a superseded task ended the brief: %+v", b.Result)
	}
	now := time.Now()
	if _, done := s.briefDigest(context.Background(), now, now.Add(-time.Hour), time.UTC); len(done) != 0 {
		t.Fatalf("the briefing says the work ended while its take-over goes on: %+v", done)
	}
	// An old criterion and a missing current one cannot be accepted: the refusal writes neither a review result nor completion.
	rejected, isErr := tool(t, s, "task_review", map[string]any{"todo_id": third.ID, "verdict": "accepted",
		"checks":    []map[string]any{{"criterion": "PR merged to main", "met": true, "evidence": "merged"}, {"criterion": "something else entirely", "met": true}},
		"also_done": []string{"removed a dead flag"}})
	if !isErr || !strings.Contains(rejected, "unknown current done_when") {
		t.Fatalf("partial task_review was accepted: %s", rejected)
	}
	if cur := todoNow(s, third.ID); cur.DoneAt != nil || len(cur.Supervision.Reviews) != 0 {
		t.Fatalf("the refused review changed the task: %+v %+v", cur, cur.Supervision)
	}
	if b, _ := s.briefOfTodo(first.ID); b.Result != nil {
		t.Fatalf("the refused review recorded a result: %+v", b.Result)
	}
	// A complete acceptance has exactly one met check with evidence for each current criterion.
	got, isErr := tool(t, s, "task_review", map[string]any{"todo_id": third.ID, "verdict": "accepted",
		"checks": []map[string]any{
			{"criterion": "PR merged to main", "met": true, "evidence": "merged"},
			{"criterion": "a test covers landscape", "met": true, "evidence": "landscape test passed"},
		},
		"also_done": []string{"removed a dead flag"}})
	if isErr || !strings.Contains(got, `"PR merged to main" met`) || !strings.Contains(got, `"a test covers landscape" met`) ||
		!strings.Contains(got, "Also done: removed a dead flag") {
		t.Fatalf("task_review: %s", got)
	}
	done, _ := s.briefOfTodo(first.ID)
	if r := done.Result; r == nil || len(r.Criteria) != 2 || r.Criteria[0].State != criterionMet || r.Criteria[1].State != criterionMet ||
		r.Todo != third.ID || len(r.AlsoDone) != 1 {
		t.Fatalf("result = %+v", done.Result)
	}
	// The next briefing says it ended, once, in the owner's words with each criterion's result.
	now = time.Now()
	_, ended := s.briefDigest(context.Background(), now, now.Add(-time.Hour), time.UTC)
	if len(ended) != 1 || ended[0].Words != "please fix the login crash before Friday" || len(ended[0].Result) != 2 ||
		ended[0].Result[0].State != criterionMet || ended[0].Result[1].State != criterionMet || len(ended[0].AlsoDone) != 1 {
		t.Fatalf("digest = %+v", ended)
	}
	s.briefDigestTold(context.Background(), now)
	later := now.Add(time.Minute)
	if _, ended := s.briefDigest(context.Background(), later, s.briefDigestSince(context.Background(), later), time.UTC); len(ended) != 0 {
		t.Fatalf("listed a second time: %+v", ended)
	}
}

// Historical multi-to-do briefs remain readable even though new continuations no longer create successor to-dos.
func TestLegacyBriefLineageSurvivesSupersededTaskCancellation(t *testing.T) {
	s, _, _ := briefService(t, "fix the login crash")
	ctx := context.Background()
	first, err := s.createTaskTodo(ctx, "Original fix", "Fix login", "conv1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	brief, err := s.ensureTaskBrief(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.createTaskTodo(ctx, "Legacy successor", "Finish login", "conv1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.linkTaskBrief(ctx, brief.ID, second.ID, first.ID, "The old Agent stopped"); err != nil {
		t.Fatal(err)
	}
	if got, isErr := tool(t, s, "task_review", map[string]any{
		"todo_id": first.ID, "verdict": "cancelled", "note": "superseded",
	}); isErr {
		t.Fatal(got)
	}
	if got, _ := s.briefOfTodo(second.ID); got.Result != nil {
		t.Fatalf("cancelling a historical predecessor ended its open successor's brief: %+v", got.Result)
	}
	lineage := s.taskLineage(second.ID)
	if len(lineage) != 2 || lineage[0].SupersededBy != second.ID ||
		lineage[0].Why != "The old Agent stopped" || lineage[1].Continues != first.ID || !lineage[1].Current {
		t.Fatalf("legacy lineage was lost: %+v", lineage)
	}
}

// Work the assistant starts after a supervision message is marked as its own, with what it followed and the reason it
// gave; an older message of the owner's is not attributed to it.
func TestBriefOfAssistantInitiatedWork(t *testing.T) {
	s, f, asked := briefService(t, "what is on my calendar today?")
	f.setItems(proto.Item{ID: "m1", Seq: 1, TS: asked, Role: "user", Kind: proto.KindText, Text: "what is on my calendar today?"},
		proto.Item{ID: "a1", Seq: 2, TS: asked.Add(time.Minute), Role: "assistant", Kind: proto.KindText, Text: "Two meetings."},
		proto.Item{ID: "m2", Seq: 3, TS: asked.Add(time.Hour), Role: "user", Kind: proto.KindText,
			Text: prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{{ID: "todo_1", Title: "deploy", Kind: sigReview}}})})
	if got, isErr := tool(t, s, "task_plan", briefTask(map[string]any{"started_why": "the deploy task left its issue open"})); isErr {
		t.Fatal(got)
	}
	waitFor(t, "the agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
	b, ok := s.briefOfTodo(onlyTodo(s).ID)
	if !ok || b.Request != nil || b.StartedBy != briefByAssistant || b.Trigger != "supervision" || b.Reason != "the deploy task left its issue open" {
		t.Fatalf("brief = %+v", b)
	}
	// A scheduled routine's run is a routine's; a conversation that cannot be read names nobody.
	for _, c := range []struct {
		items []proto.Item
		by    string
	}{
		{[]proto.Item{{ID: "m1", Role: "user", Kind: proto.KindText, Text: "hello", TS: asked},
			{ID: "m2", Role: "user", Kind: proto.KindText, TS: asked, Text: "run", Source: &proto.MessageSource{Kind: proto.SourcePatrol, Title: "evening review"}}}, briefByRoutine},
		{[]proto.Item{{ID: "m1", Role: "user", Kind: proto.KindText, Text: "hello", TS: asked},
			{ID: "m2", Role: "user", Kind: proto.KindText, TS: asked, Text: "from a leader", Source: &proto.MessageSource{Kind: proto.SourceConversation, Title: "billing"}}}, briefByAssistant},
		{nil, briefByUnknown},
	} {
		f.setItems(c.items...)
		if got := s.briefOrigin(context.Background(), "server", "conv1"); got.StartedBy != c.by || got.Request != nil {
			t.Fatalf("%s: %+v", c.by, got)
		}
	}
	// The owner's words come without the context blocks the app put before them, and a long message is cut with a
	// pointer to the rest.
	long := strings.Repeat("x", briefRequestMax+50)
	f.setItems(proto.Item{ID: "m9", Role: "user", Kind: proto.KindText, TS: asked, Text: "<persona-context>snapshot</persona-context>\n" + long})
	got := s.briefOrigin(context.Background(), "server", "conv1")
	if got.Request == nil || !got.Request.Truncated || len([]rune(got.Request.Text)) != briefRequestMax || got.Request.MessageID != "m9" ||
		strings.Contains(got.Request.Text, "snapshot") {
		t.Fatalf("request = %+v", got.Request)
	}
}

// A scope change is an entry in the brief's log that applies the new criteria; the brief itself stays. One that adds a
// step only the owner may allow is refused unless the owner's message started the turn; agent_supervise on one's own
// task goes through the same log. The change is in the next briefing, once.
func TestTaskScopeIsLogged(t *testing.T) {
	s, f, asked := briefService(t, "please fix the login crash before Friday")
	if got, isErr := tool(t, s, "task_plan", briefTask(nil)); isErr {
		t.Fatal(got)
	}
	waitFor(t, "the agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	supervision := proto.Item{ID: "m2", Seq: 2, TS: asked.Add(time.Hour), Role: "user", Kind: proto.KindText,
		Text: prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{{ID: todo.ID, Title: todo.Title, Kind: sigStalled}}})}
	f.setItems(supervision)

	if got, isErr := tool(t, s, "task_scope", map[string]any{"todo_id": todo.ID, "change": "", "why": "x"}); !isErr {
		t.Fatalf("a change without words: %s", got)
	}
	// Dropping a criterion: logged, applied, the brief unchanged.
	got, isErr := tool(t, s, "task_scope", map[string]any{"todo_id": todo.ID, "change": "the landscape test is no longer required",
		"why": "the simulator cannot rotate in CI", "done_when": []string{"PR merged to main"}, "expect_minutes": 90})
	if isErr || !strings.Contains(got, "Logged") {
		t.Fatalf("task_scope: %s", got)
	}
	b, _ := s.briefOfTodo(todo.ID)
	if len(b.DoneWhen) != 2 || len(b.Criteria) != 1 || b.ExpectMinutes != 30 || b.ExpectNow != 90 || len(b.Log) != 2 ||
		b.Log[1].Kind != briefEventScope || strings.Join(b.Log[1].Dropped, "|") != "a test covers landscape" || b.Log[1].By != "conv1" ||
		!b.ClockAt.Equal(b.Log[1].At) {
		t.Fatalf("brief = %+v", b)
	}
	if v := onlyTodo(s).Supervision; strings.Join(v.DoneWhen, "|") != "PR merged to main" || v.ExpectMinutes != 90 {
		t.Fatalf("ledger = %+v", v)
	}
	// Adding a deploy of work the owner ordered: the supervisor's own to add (decision rights), logged without asking.
	deploy := map[string]any{"todo_id": todo.ID, "change": "also deploy it to production", "why": "it is ready",
		"done_when": []string{"PR merged to main", "deployed to production and checked"}}
	if got, isErr := tool(t, s, "task_scope", deploy); isErr || !strings.Contains(got, "Logged") {
		t.Fatalf("a deploy added by the supervisor was refused: %s", got)
	}
	// Speaking for the owner to other people: only the owner's, refused after a supervision message.
	risky := map[string]any{"todo_id": todo.ID, "change": "also send the release note to the customers", "why": "it is ready",
		"done_when": []string{"PR merged to main", "deployed to production and checked", "send the release note to the customers"}}
	if got, isErr := tool(t, s, "task_scope", risky); !isErr || !strings.Contains(got, "only the owner may allow") || !strings.Contains(got, "ask_user") {
		t.Fatalf("a risky change after a supervision message: %s", got)
	}
	if b, _ := s.briefOfTodo(todo.ID); len(b.Log) != 3 || len(b.Criteria) != 2 {
		t.Fatalf("the refused change was applied: %+v", b)
	}
	// The owner says yes: the turn their message starts may add the step, and the message stays on the entry.
	f.setItems(supervision, proto.Item{ID: "m3", Seq: 3, TS: asked.Add(2 * time.Hour), Role: "user", Kind: proto.KindText, Text: "yes, send the release note to the customers too"})
	if got, isErr := tool(t, s, "task_scope", risky); isErr {
		t.Fatalf("the approved change: %s", got)
	}
	b, _ = s.briefOfTodo(todo.ID)
	if e := b.Log[len(b.Log)-1]; e.Approval == nil || e.Approval.Text != "yes, send the release note to the customers too" || e.Approval.MessageID != "m3" {
		t.Fatalf("approval = %+v", e)
	}
	// Another conversation is not its supervisor.
	raw, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "change": "x", "why": "y"})
	if r := s.Tool(context.Background(), ToolCall{Name: "task_scope", Arguments: raw, MachineID: "server", SessionID: "conv2"}); !r.IsError {
		t.Fatalf("another conversation changed the scope: %+v", r)
	}

	// The next briefing says the changes, in the owner's words; the one after has nothing new to say of it.
	now := time.Now()
	open, done := s.briefDigest(context.Background(), now, s.briefDigestSince(context.Background(), now), time.UTC)
	if len(done) != 0 || len(open) != 1 || open[0].Words != "please fix the login crash before Friday" || open[0].Date != "2026-11-03" ||
		len(open[0].Scope) != 3 || open[0].Scope[0] != "the landscape test is no longer required" {
		t.Fatalf("digest = %+v / %+v", open, done)
	}
	s.briefDigestTold(context.Background(), now)
	if open, _ := s.briefDigest(context.Background(), now.Add(time.Hour), s.briefDigestSince(context.Background(), now.Add(time.Hour)), time.UTC); len(open) != 0 {
		t.Fatalf("an unchanged task that needs nothing was listed again: %+v", open)
	}
}

// agent_supervise on a task one supervises already changes its criteria through the brief's log, under task_scope's
// rules; the task it first made has a brief with the owner's words.
func TestAgentSuperviseRescopesThroughTheLog(t *testing.T) {
	s, f, _ := briefService(t, "keep an eye on the weekly report agent")
	secretary(f)
	args := func(doneWhen ...string) map[string]any {
		return map[string]any{"machine": "box", "session_id": "sec00001", "title": "weekly report", "done_when": doneWhen}
	}
	if got, isErr := tool(t, s, "agent_supervise", args("the table link is posted")); isErr {
		t.Fatal(got)
	}
	todo := onlyTodo(s)
	b, ok := s.briefOfTodo(todo.ID)
	if !ok || b.Reconstructed || b.Request == nil || b.Request.Text != "keep an eye on the weekly report agent" || len(b.DoneWhen) != 1 || len(b.Log) != 0 {
		t.Fatalf("brief = %+v, %v", b, ok)
	}
	// A turn the owner did not start: a risky criterion is refused, a plain one is logged.
	f.setItems(proto.Item{ID: "m2", Role: "user", Kind: proto.KindText, TS: time.Now(),
		Text: prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{{ID: todo.ID, Title: todo.Title, Kind: sigStalled}}})})
	if got, isErr := tool(t, s, "agent_supervise", args("the table link is posted", "send the table to the whole team")); !isErr || !strings.Contains(got, "only the owner may allow") {
		t.Fatalf("agent_supervise with a risky criterion: %s", got)
	}
	if got, isErr := tool(t, s, "agent_supervise", args("the table link is posted", "last week's column is there")); isErr {
		t.Fatalf("agent_supervise: %s", got)
	}
	b, _ = s.briefOfTodo(todo.ID)
	if len(b.Log) != 1 || strings.Join(b.Log[0].Added, "|") != "last week's column is there" || len(b.DoneWhen) != 1 || len(b.Criteria) != 2 {
		t.Fatalf("agent_supervise did not go through the log: %+v", b)
	}
	if v := onlyTodo(s).Supervision; len(v.DoneWhen) != 2 {
		t.Fatalf("ledger = %+v", v)
	}
}

// A task past its clock is raised once to its supervisor and then stands as overdue in the briefing; a scope change
// starts the clock again.
func TestBriefClockRaisesOverdueOnce(t *testing.T) {
	s, _, _ := briefService(t, "please fix the login crash before Friday")
	if got, isErr := tool(t, s, "task_plan", briefTask(nil)); isErr {
		t.Fatal(got)
	}
	waitFor(t, "the agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	b, _ := s.briefOfTodo(todo.ID)
	if want := b.ClockAt.Add(24 * time.Hour); !b.DueAt.Equal(want) { // 30 min × 3 is under a day
		t.Fatalf("due %s, want %s", b.DueAt, want)
	}
	pending := func() (superviseSignal, bool) {
		s.supervise.mu.Lock()
		defer s.supervise.mu.Unlock()
		g, ok := s.supervise.pending[todo.ID+"|"+sigOverdue]
		return g, ok
	}
	ctx := context.Background()
	s.briefClock(ctx, todo, b.ClockAt.Add(23*time.Hour))
	if _, ok := pending(); ok {
		t.Fatal("raised before its clock ran out")
	}
	late := b.ClockAt.Add(25 * time.Hour)
	s.briefClock(ctx, todo, late)
	g, ok := pending()
	if !ok || g.Minutes != 25*60 {
		t.Fatalf("signal = %+v, %v", g, ok)
	}
	e, ok := s.digestEntry(ctx, g, "")
	if !ok || e.Kind != sigOverdue || e.Asked != "please fix the login crash before Friday" || e.AskedAt != "2026-11-03" {
		t.Fatalf("entry = %+v, %v", e, ok)
	}
	if text := prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{e}, Overdue: true}); !strings.Contains(text, "signal=overdue") ||
		!strings.Contains(text, "please fix the login crash before Friday") {
		t.Fatalf("the supervision message is missing brief facts:\n%s", text)
	}
	s.clearOverdue(todo.ID)
	s.briefClock(ctx, todo, late.Add(time.Hour))
	if _, ok := pending(); ok {
		t.Fatal("raised a second time for the same clock")
	}
	b, _ = s.briefOfTodo(todo.ID)
	if b.Overdue == nil || len(b.Log) != 2 || b.Log[1].Kind != briefEventOverdue {
		t.Fatalf("brief = %+v", b)
	}
	if open, _ := s.briefDigest(ctx, late, late.Add(-time.Hour), time.UTC); len(open) != 1 || !open[0].Overdue {
		t.Fatalf("digest = %+v", open)
	}
	// Re-scoped: the clock starts again, and runs out again later.
	if got, isErr := tool(t, s, "task_scope", map[string]any{"todo_id": todo.ID, "change": "still right, it needs the weekend's data", "why": "the data comes in on Monday", "expect_minutes": 600}); isErr {
		t.Fatal(got)
	}
	b, _ = s.briefOfTodo(todo.ID)
	if b.Overdue != nil || !b.DueAt.Equal(b.ClockAt.Add(30*time.Hour)) { // 600 min × 3
		t.Fatalf("after the scope change: overdue %v, due %s (clock %s)", b.Overdue, b.DueAt, b.ClockAt)
	}
	s.briefClock(ctx, todo, b.ClockAt.Add(29*time.Hour))
	if _, ok := pending(); ok {
		t.Fatal("raised before the new clock ran out")
	}
	s.briefClock(ctx, todo, b.ClockAt.Add(31*time.Hour))
	if _, ok := pending(); !ok {
		t.Fatal("not raised when the new clock ran out")
	}
}

// A task from before briefs gets one from what it still has: its title as the goal, its notes as the prompt at
// hand-over (not the owner's words), marked reconstructed; nothing is invented.
func TestBriefReconstructedForOldTasks(t *testing.T) {
	s, f, _ := briefService(t, "an unrelated later message")
	ctx := context.Background()
	todo, err := s.newTodo(ctx, "migrate the logs", "you are taking over a session that stopped half-way", "conv1", "", true)
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv1", []string{"old logs are gone"}, 45)
	f.setSessions("box", proto.Session{SessionID: "s9", MachineID: "box", Status: proto.StatusIdle})
	if err := s.linkTodoAgent(ctx, todo.ID, "box", "s9", "migrate the logs", "/code", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.briefOfTodo(todo.ID); ok {
		t.Fatal("it has a brief already")
	}
	s.superviseWatch(ctx, time.Now())
	b, ok := s.briefOfTodo(todo.ID)
	if !ok || !b.Reconstructed || b.Request != nil || b.StartedBy != briefByUnknown || b.Goal != "migrate the logs" ||
		b.Handover != "you are taking over a session that stopped half-way" || strings.Join(b.Criteria, "|") != "old logs are gone" ||
		b.ExpectNow != 45 || !b.CreatedAt.Equal(todo.CreatedAt) {
		t.Fatalf("brief = %+v, %v", b, ok)
	}
	raw, _ := s.todoView(ctx, todo.ID)
	if !strings.Contains(string(raw), `"reconstructed":true`) || strings.Contains(string(raw), "an unrelated later message") {
		t.Fatalf("view = %s", raw)
	}
}

// What the done judge is given: the owner's words and the criteria as the scope changes left them, with the dropped
// ones named, whatever the latest hand-over prompt says.
func TestGoldenTaskDoneAgainstTheBrief(t *testing.T) {
	golden(t, "task_done.brief.user", prompts.RenderTaskDoneInput(prompts.TaskDoneInput{Title: "接手登录修复",
		Prompt: "你在接手一个中断的会话：分支已经推上去了，把测试补完。", Asked: "登录页横屏会崩，周五前修好", AskedAt: "2026-11-03",
		Criteria: []string{"PR 合并到 main"}, Dropped: []string{"横屏有测试覆盖"},
		Replies: []prompts.TaskDoneReply{{Machine: "box", Text: "PR #12 已合并。"}}}))
}

// The supervision message with a task's brief: the owner's words, who started work that was not theirs, the dropped
// criteria, and an overdue task with what to do about it.
func TestGoldenSuperviseBrief(t *testing.T) {
	golden(t, "supervise.brief", prompts.RenderSupervise(prompts.Supervise{Overdue: true, Entries: []prompts.SuperviseEntry{
		{ID: "todo_1", Title: "接手登录修复", Kind: sigOverdue, Minutes: 1500, DoneWhen: []string{"PR 合并到 main"},
			Asked: "登录页横屏会崩，周五前修好", AskedAt: "2026-11-03", Dropped: []string{"横屏有测试覆盖"}},
		{ID: "todo_2", Title: "清理旧日志", Kind: sigReview, DoneWhen: []string{"旧日志已删除"}, Started: briefByAssistant, StartedWhy: "部署任务留下的尾巴"},
	}}))
}

// The briefing's facts with briefs: open work in the owner's words, and what ended since the last briefing with each
// criterion's result. Without any, the facts read as before (routine_facts golden).
func TestGoldenBriefFactsBriefs(t *testing.T) {
	golden(t, "routine_facts.briefs", prompts.RenderBriefFacts(prompts.BriefFacts{Card: true, Hours: 12,
		Briefs: []prompts.BriefTask{
			{Date: "2026-11-03", Words: "登录页横屏会崩，周五前修好", Now: "测试补完了，等合并", Status: "not_done", Need: "要不要今天合并", Overdue: true,
				Scope: []string{"不再要求横屏测试"}},
			{Date: "2026-11-05", By: briefByAssistant, Goal: "清理旧日志", Now: "在做", Decide: true},
		},
		BriefsDone: []prompts.BriefTask{
			{Date: "2026-11-01", Words: "把周报整理一下", Now: "验收通过", Result: []prompts.BriefCriterion{{Criterion: "周报发到群里", State: "met"},
				{Criterion: "附上数据表", State: "dropped", Change: "数据表下周才有"}, {Criterion: "配图", State: "unchecked"}}, AlsoDone: []string{"顺手修了模板"}},
			{Date: "2026-11-02", By: briefByRoutine, Goal: "巡检", Now: "不做了", Cancelled: true},
		}}))
}

func TestContinuationUsesAuthorizedScope(t *testing.T) {
	s, f, asked := briefService(t, "keep the recurring check enabled")
	const original = "the recurring check stays enabled"
	const effective = "report the result of one inspection"
	got, isErr := tool(t, s, "task_plan", briefTask(map[string]any{
		"title": "Verify the recurring check", "goal": "Check the existing schedule",
		"prompt": "Verify the check's status", "done_when": []string{original},
	}))
	if isErr {
		t.Fatal(got)
	}
	waitFor(t, "original start", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	f.mu.Lock()
	fresh, _ := f.starts[0]["prompt"].(string)
	f.mu.Unlock()
	plan := &TaskPlan{ID: "waiting-continuation", Conversation: "conv1", State: planOpen, Tasks: []PlanTask{
		{Title: "Continue task", Prompt: "Continue the earlier work", Machine: "box", Cwd: "/code/app", Continues: todo.ID, ContinuesWhy: "The earlier Agent stopped"},
	}}
	ctx := context.Background()
	if err := s.planBriefs(ctx, "", plan.Conversation, plan.Tasks); err != nil {
		t.Fatal(err)
	}
	if err := s.savePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	f.setItems(proto.Item{ID: "m2", Seq: 2, TS: asked.Add(time.Hour), Role: "user", Kind: proto.KindText,
		Text: "Cancel the recurring check and do one inspection."})
	reply, isErr := tool(t, s, "task_scope", map[string]any{
		"todo_id": todo.ID, "change": "Cancel the recurring check", "why": "The owner requested one inspection",
		"done_when": []string{effective},
	})
	if isErr || !strings.Contains(reply, "Effective criteria:") || !strings.Contains(reply, "Dropped criteria:") {
		t.Fatalf("task_scope: %s", reply)
	}
	brief, _ := s.briefOfTodo(todo.ID)
	if _, err := s.startPlan(ctx, plan.ID, nil, false); err == nil || !strings.Contains(err.Error(), "scope of "+todo.ID+" changed") {
		t.Fatalf("stale card start: %v", err)
	}
	args := map[string]any{"title": "Inspect once", "prompt": "Inspect once, without re-enabling any schedule",
		"continues": todo.ID, "continues_why": "The owner cancelled recurring work",
		"scope_revision": plan.Tasks[0].Brief.ClockAt.Format(time.RFC3339Nano)}
	if got, isErr := tool(t, s, "task_plan", briefTask(args)); !isErr || !strings.Contains(got, "scope of "+todo.ID+" changed") {
		t.Fatalf("stale scope revision: %s", got)
	}
	args["scope_revision"] = brief.ClockAt.Format(time.RFC3339Nano)
	if got, isErr := tool(t, s, "task_plan", briefTask(args)); isErr {
		t.Fatal(got)
	}
	waitFor(t, "replacement start", func() bool { return len(onlyTodo(s).Agents) == 2 })
	f.mu.Lock()
	replacement, _ := f.starts[len(f.starts)-1]["prompt"].(string)
	starts := len(f.starts)
	f.mu.Unlock()
	if starts != 2 || todoCount(s) != 1 || strings.Count(replacement, effective) != 1 || strings.Contains(replacement, original) {
		t.Fatalf("replacement: %d starts, %d to-dos, prompt %q", starts, todoCount(s), replacement)
	}
	if !strings.Contains(fresh, original) || brief.Request == nil || brief.Request.Text != "keep the recurring check enabled" ||
		len(brief.Log) != 2 || len(brief.Log[1].Dropped) != 1 || brief.Log[1].Dropped[0] != original {
		t.Fatalf("original assignment or scope history lost: %+v", brief)
	}
	t.Logf("synthetic full prompts (runes/bytes): fresh %d/%d, re-scoped continuation %d/%d",
		len([]rune(fresh)), len(fresh), len([]rune(replacement)), len(replacement))
}

func TestContinuationSurvivesEstimateUpdate(t *testing.T) {
	s, _, _ := briefService(t, "check one job")
	got, failed := tool(t, s, "task_plan", briefTask(map[string]any{"title": "Check job", "goal": "Check one job", "prompt": "Inspect the job", "done_when": []string{"Result reported"}}))
	if failed {
		t.Fatal(got)
	}
	waitFor(t, "original start", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	tasks := []PlanTask{{Title: "Continue", Machine: "box", Prompt: "Same inspection", Continues: todo.ID, ContinuesWhy: "Agent stopped"}}
	if err := s.planBriefs(t.Context(), "", "conv1", tasks); err != nil {
		t.Fatal(err)
	}
	rev := tasks[0].Brief.scopeRevision()
	// Quoting an operation in a schedule note grants no authority to carry it out.
	b, err := s.applyScope(t.Context(), todo, scopeChange{By: "conv1", Expect: 42, Change: "Waiting for the merge report", Why: "More time needed"})
	if err != nil || !b.scopeRevision().Equal(rev) || !b.ClockAt.After(rev) || b.Log[len(b.Log)-1].Kind != briefEventEstimate {
		t.Fatalf("estimate changed authorized scope: %+v, %v", b, err)
	}
	if err := s.checkContinuationScope(&tasks[0]); err != nil || tasks[0].ExpectMinutes != 42 {
		t.Fatalf("pending continuation: %v, estimate %d", err, tasks[0].ExpectMinutes)
	}
	got, failed = tool(t, s, "todo_get", map[string]any{"id": todo.ID})
	if failed || !strings.Contains(got, "scope_revision: "+rev.Format(time.RFC3339Nano)) {
		t.Fatalf("todo_get: %s", got)
	}
	if _, err = s.applyScope(t.Context(), todo, scopeChange{By: "conv1", Change: "Send the release note to the customers", DoneWhen: []string{"Send the release note to the customers"}, Expect: 42}); err == nil {
		t.Fatal("adding an owner-only criterion needs owner approval")
	}
}

// Decision rights (docs/designs/decision-rights-20261123.md): merging and deploying work the owner ordered is the
// supervisor's, so a scope may add it without asking, unless the owner's words on record hold it off. Speaking for the
// owner to others is theirs, unless their words on record already say it. A lead hands an owner's step to the main
// conversation; only the main conversation asks the owner.
func TestScopeRiskFollowsDecisionRights(t *testing.T) {
	for _, c := range []struct {
		text, known string
		want        string
	}{
		{"deploy it to production", "", ""},
		{"merge the PR and release it", "fix the crash", ""},
		{"send the release note to the customers", "", "send"},
		{"send the release note to the customers", "the owner: send the release note to the customers when it ships", ""},
		{"merge the PR", "do not merge; stop at a PR", "merge"},
		{"delete the old exports", "", "delete"},
	} {
		if got := strings.Join(scopeRisk(c.text, c.known), ","); got != c.want {
			t.Errorf("scopeRisk(%q, %q) = %q, want %q", c.text, c.known, got, c.want)
		}
	}
	s, _ := newQuietService(t)
	setSettings(t, s, func(v *Settings) { v.Main, v.Leaders = "conv1", []string{"lead1"} })
	ctx := context.Background()
	if got := s.ownerStepRoute(ctx, "conv1"); !strings.Contains(got, "ask_user") {
		t.Errorf("the main conversation's route: %s", got)
	}
	if got := s.ownerStepRoute(ctx, "lead1"); !strings.Contains(got, "main conversation") || strings.Contains(got, "ask_user") {
		t.Errorf("a lead's route: %s", got)
	}
}

// An estimate change is logged as an estimate, which authorizes nothing new, and still reaches the owner in the next
// digest, as task_scope's reply says (review of #13130). An estimate set through agent_send carries no sentence for
// the owner and adds no empty line; a lesson learns from either, as a revised estimate also hides an overrun.
func TestEstimateChangeReachesTheDigestAndLessons(t *testing.T) {
	s, _, _ := briefService(t, "please fix the login crash before Friday")
	if got, isErr := tool(t, s, "task_plan", briefTask(nil)); isErr {
		t.Fatal(got)
	}
	waitFor(t, "the agent", func() bool { return len(onlyTodo(s).Agents) == 1 })
	todo := onlyTodo(s)
	got, isErr := tool(t, s, "task_scope", map[string]any{"todo_id": todo.ID, "change": "the migration needs another hour",
		"why": "the backfill is slower than measured", "expect_minutes": 90})
	if isErr || !strings.Contains(got, "the owner sees it in the next digest") {
		t.Fatalf("task_scope: %s", got)
	}
	b, _ := s.briefOfTodo(todo.ID)
	if last := b.Log[len(b.Log)-1]; last.Kind != briefEventEstimate || b.ExpectNow != 90 {
		t.Fatalf("not logged as an estimate: %+v", b)
	}
	if _, err := s.rescope(context.Background(), todoNow(s, todo.ID), "conv1", "conv1", "agent_send", nil, 120); err != nil {
		t.Fatal(err)
	}
	if b, _ = s.briefOfTodo(todo.ID); b.ExpectNow != 120 || b.Log[len(b.Log)-1].Kind != briefEventEstimate || b.Log[len(b.Log)-1].Change != "" {
		t.Fatalf("agent_send's estimate: %+v", b)
	}
	now := time.Now()
	open, _ := s.briefDigest(context.Background(), now, s.briefDigestSince(context.Background(), now), time.UTC)
	if len(open) != 1 || !slices.Equal(open[0].Scope, []string{"the migration needs another hour"}) {
		t.Fatalf("digest = %+v", open)
	}
	if !lessonFriction(nil, &b, time.Time{}, 0) {
		t.Fatal("a revised estimate gave a lesson nothing to learn from")
	}
}
