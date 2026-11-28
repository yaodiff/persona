package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func taskInboxItem(t *testing.T, s *Service, id, machine, conversation string) *InboxItem {
	t.Helper()
	ctx := context.Background()
	if err := s.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	it := &InboxItem{ID: id, Source: "example", Kind: KindDM, Title: "Investigate a report", Tier: TierAction, Last: "1"}
	s.inbox.mu.Lock()
	s.inbox.items[id] = it
	s.inbox.noteHandoff(it, SessionRef{MachineID: machine, SessionID: conversation}, namedClientID(id), time.Now())
	s.inbox.mu.Unlock()
	if s.saveInbox(ctx, []*InboxItem{it})[id] {
		t.Fatal("save handed item")
	}
	return it
}

func taskInboxArgs(items ...string) json.RawMessage {
	var tasks []map[string]any
	for _, item := range items {
		tasks = append(tasks, map[string]any{
			"title": "Investigate report", "prompt": "Inspect the reported defect and verify the result", "role": "worker",
			"machine": "box", "cwd": "/code/app", "inbox_item": item,
			"inbox_handoff": "untrusted-operation", "done_when": []string{"Regression verified"}, "expect_minutes": 10,
		})
	}
	raw, _ := json.Marshal(map[string]any{"tasks": tasks})
	return raw
}

func TestTaskPlanInboxExplicitSourceUsesTheActualHandoff(t *testing.T) {
	ctx := context.Background()
	s, f := newTaskService(t)
	f.inbox = newFakeInbox()
	f.mu.Lock()
	f.hello.Assistant.MachineID = "box"
	f.mu.Unlock()
	setSettings(t, s, func(v *Settings) { v.Main, v.MachineID = "m1", "box" })
	f.setSessions("box", proto.Session{SessionID: "m1", Assistant: true, Status: proto.StatusIdle})
	if err := s.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	it := &InboxItem{ID: "example:handoff", Source: "example", Kind: KindDM, Title: "A report", Tier: TierAction}
	s.inbox.mu.Lock()
	s.inbox.items[it.ID] = it
	s.inbox.mu.Unlock()
	if _, _, err := s.handOff(ctx, it.ID, handoffReq{Text: "Investigate the report", ClientID: namedClientID("actual handoff")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.toolTaskPlanFrom(ctx, "box", "m1", taskInboxArgs(it.ID)); err != nil {
		t.Fatal(err)
	}
	p, err := s.startPlan(ctx, planOf(t, s, "m1").ID, nil, false)
	if err != nil || p.Tasks[0].Error != "" {
		t.Fatalf("handoff admission: %+v %v", p, err)
	}
	todo, _ := s.todoNow(p.Tasks[0].TodoID)
	if todo.InboxItem != it.ID || len(toMain(f)) != 1 ||
		!strings.Contains(toMain(f)[0], it.ID) {
		t.Fatalf("handoff source or prompt missing: %+v, %v", todo, toMain(f))
	}
}

func TestTaskPlanInboxSourcesPersistWithExplicitItemChoices(t *testing.T) {
	ctx := context.Background()
	const conversation = "inbox-source-test"
	st := todoTestDB(t)
	s, _ := newTaskServiceOn(t, st)
	a := taskInboxItem(t, s, "example:a", "box", conversation)
	b := taskInboxItem(t, s, "example:b", "box", conversation)
	if _, err := s.toolTaskPlanFrom(ctx, "box", conversation, taskInboxArgs(a.ID, b.ID, a.ID)); err != nil {
		t.Fatal(err)
	}
	p := planOf(t, s, conversation)
	for i, want := range []string{a.ID, b.ID, a.ID} {
		if p.Tasks[i].InboxItem != want || want != "" && p.Tasks[i].InboxHandoff != namedClientID(want) {
			t.Fatalf("task %d lost its validated source: %+v", i, p.Tasks[i])
		}
	}
	started, err := s.startPlan(ctx, p.ID, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{a.ID, b.ID, a.ID} {
		task := started.Tasks[i]
		if task.Error != "" || task.TodoID == "" {
			t.Fatalf("task %d: %+v", i, task)
		}
		waitFor(t, "task Agent", func() bool {
			td, _ := s.todoNow(task.TodoID)
			return len(td.Agents) > 0
		})
		td, _ := s.todoNow(task.TodoID)
		if td.InboxItem != want {
			t.Fatalf("task %d source %q, want %q", i, td.InboxItem, want)
		}
	}
	reloaded, _ := newTaskServiceOn(t, st)
	if err := reloaded.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.inboxReady(ctx); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{a.ID, b.ID, a.ID} {
		td, ok := reloaded.todoNow(started.Tasks[i].TodoID)
		if !ok || td.InboxItem != want || len(td.Agents) != 1 {
			t.Fatalf("reloaded task %d: %+v, want source %q and its Agent", i, td, want)
		}
	}
	reloaded.inbox.mu.Lock()
	defer reloaded.inbox.mu.Unlock()
	for id, want := range map[string]string{a.ID: started.Tasks[2].TodoID, b.ID: started.Tasks[1].TodoID} {
		it := reloaded.inbox.items[id]
		if !it.open() || it.Agent == nil || it.Agent.TodoID != want || it.Suggestion != nil || it.Handling != nil {
			t.Fatalf("item %s: %+v", id, it)
		}
	}
}

func TestTaskPlanInboxRejectsInvalidSources(t *testing.T) {
	for _, why := range []string{"missing", "another manager", "conversation", "machine", "filed", "bot", "owner bot"} {
		t.Run(why, func(t *testing.T) {
			s, _ := newTaskService(t)
			source := s
			if why == "another manager" {
				source, _ = newTaskService(t)
			}
			it := taskInboxItem(t, source, "example:source", "box", "c1x1x1")
			s.inbox.mu.Lock()
			switch why {
			case "missing":
				delete(s.inbox.items, it.ID)
			case "conversation":
				it.Handoffs[0].SessionID = "another"
			case "machine":
				it.Handoffs[0].MachineID = "another"
			case "filed":
				it.done(time.Now(), "user")
			case "bot":
				it.Kind = KindBot
			case "owner bot":
				it.Source, it.Kind = "larkcli", "bot_handoff"
			}
			s.inbox.mu.Unlock()
			if _, err := s.toolTaskPlanFrom(context.Background(), "box", "c1x1x1", taskInboxArgs(it.ID)); err == nil {
				t.Fatal("invalid source accepted")
			}
			if todoCount(s) != 0 {
				t.Fatal("invalid source created a task")
			}
		})
	}
}

func TestTaskPlanInboxRechecksHandoffAtStart(t *testing.T) {
	for _, change := range []string{"filed", "handoff replaced", "excluded"} {
		t.Run(change, func(t *testing.T) {
			s, _ := newTaskService(t)
			it := taskInboxItem(t, s, "example:source", "box", "c1x1x1")
			if _, err := s.toolTaskPlanFrom(context.Background(), "box", "c1x1x1", taskInboxArgs(it.ID)); err != nil {
				t.Fatal(err)
			}
			p := planOf(t, s, "c1x1x1")
			s.inbox.mu.Lock()
			switch change {
			case "filed":
				it.done(time.Now(), "user")
			case "handoff replaced":
				it.Handoffs[0].Op = "another-operation"
			case "excluded":
				it.Kind = KindBot
			}
			s.inbox.mu.Unlock()
			p, err := s.startPlan(context.Background(), p.ID, nil, false)
			if err != nil || p.Tasks[0].Error == "" || p.Tasks[0].TodoID != "" || todoCount(s) != 0 {
				t.Fatalf("stale admission: %+v, %v, todos %d", p, err, todoCount(s))
			}
		})
	}
}

func TestTaskPlanInboxReusePreservesSourceAndCorrectsTheLegacyLink(t *testing.T) {
	ctx := context.Background()
	s, _ := newTaskService(t)
	it := taskInboxItem(t, s, "example:source", "box", "c1x1x1")
	old, err := s.createTodo(ctx, "Unrelated task", "", "c1x1x1", "")
	if err != nil {
		t.Fatal(err)
	}
	td, err := s.createTodo(ctx, "Existing task", "", "c1x1x1", "")
	if err != nil {
		t.Fatal(err)
	}
	s.inbox.mu.Lock()
	it.Agent = &InboxAgent{State: AgentDrafted, TodoID: old.ID, Draft: "Do not change this draft"}
	chain := len(it.Chain)
	s.inbox.mu.Unlock()
	var args map[string][]map[string]any
	_ = json.Unmarshal(taskInboxArgs(it.ID), &args)
	args["tasks"][0]["todo_id"] = td.ID
	raw, _ := json.Marshal(args)
	if _, err := s.toolTaskPlanFrom(ctx, "box", "c1x1x1", raw); err != nil {
		t.Fatal(err)
	}
	p, err := s.startPlan(ctx, planOf(t, s, "c1x1x1").ID, nil, false)
	if err != nil || p.Tasks[0].TodoID != td.ID {
		t.Fatalf("reuse: %+v %v", p, err)
	}
	current, _ := s.todoNow(td.ID)
	if current.InboxItem != it.ID || todoCount(s) != 2 {
		t.Fatalf("reuse made another task or lost source: %+v", current)
	}
	if _, err := s.linkPlannedInbox(ctx, p.Conversation, p.Tasks[0], current); err != nil {
		t.Fatal(err)
	}
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	if it.Agent.TodoID != td.ID || it.Agent.Draft != "Do not change this draft" || it.Agent.State != AgentDrafted ||
		!it.open() || len(it.Chain) != chain {
		t.Fatalf("binding changed processing or duplicated history: %+v", it)
	}
}

type taskInboxFailStore struct {
	*db.DB
	failCreate   atomic.Bool
	failEdit     atomic.Bool
	failInbox    atomic.Bool
	onInboxFail  func()
	expectSource string
}

func (s *taskInboxFailStore) CreateTodo(ctx context.Context, row db.TodoRow) error {
	if s.expectSource != "" && row.InboxItem != s.expectSource {
		return errors.New("task source missing at creation")
	}
	if s.failCreate.Load() {
		return errors.New("test task creation failed")
	}
	return s.DB.CreateTodo(ctx, row)
}

func (s *taskInboxFailStore) EditTodo(ctx context.Context, id string, edit db.TodoEdit, at time.Time) (*db.TodoRow, error) {
	if s.failEdit.Load() {
		return nil, errors.New("test source write failed")
	}
	return s.DB.EditTodo(ctx, id, edit, at)
}

func (s *taskInboxFailStore) PutInboxItem(ctx context.Context, id string, data []byte, at time.Time, done *time.Time) error {
	if s.failInbox.Load() {
		if s.onInboxFail != nil {
			s.onInboxFail()
		}
		return errors.New("test item write failed")
	}
	return s.DB.PutInboxItem(ctx, id, data, at, done)
}

func TestTaskPlanInboxSourceIsPresentWhenCreated(t *testing.T) {
	ctx := context.Background()
	st := &taskInboxFailStore{DB: todoTestDB(t)}
	s, _ := newTaskServiceOn(t, st)
	const conversation = "inbox-insert-source-test"
	it := taskInboxItem(t, s, "example:insert-source", "box", conversation)
	st.expectSource = it.ID
	if _, err := s.toolTaskPlanFrom(ctx, "box", conversation, taskInboxArgs(it.ID)); err != nil {
		t.Fatal(err)
	}
	p, err := s.startPlan(ctx, planOf(t, s, conversation).ID, nil, false)
	if err != nil || p.Tasks[0].Error != "" || p.Tasks[0].TodoID == "" {
		t.Fatalf("source must be in the initial insert: %+v %v", p, err)
	}
	row, err := st.GetTodo(ctx, p.Tasks[0].TodoID)
	if err != nil || row.InboxItem != it.ID {
		t.Fatalf("source was not created atomically: %+v %v", row, err)
	}
	waitFor(t, "sourced task Agent", func() bool {
		td, _ := s.todoNow(row.ID)
		return len(td.Agents) == 1
	})
}

func TestTaskPlanInboxConcurrentWriteCannotLeaveADeletedTarget(t *testing.T) {
	ctx := context.Background()
	st := &taskInboxFailStore{DB: todoTestDB(t)}
	s, _ := newTaskServiceOn(t, st)
	const conversation = "inbox-concurrent-write-test"
	it := taskInboxItem(t, s, "example:concurrent", "box", conversation)
	if _, err := s.toolTaskPlanFrom(ctx, "box", conversation, taskInboxArgs(it.ID)); err != nil {
		t.Fatal(err)
	}
	st.onInboxFail = func() {
		st.failInbox.Store(false)
		s.inbox.mu.Lock()
		newer := beginWrite(it)
		it.Summary = "A newer user edit must survive"
		s.inbox.settle(newer)
		id := it.Agent.TodoID
		s.inbox.mu.Unlock()
		if _, err := s.reusableTodo(ctx, conversation, id, false); err == nil {
			t.Error("unadmitted source task was reusable during an in-flight Inbox write")
		}
	}
	st.failInbox.Store(true)
	p, err := s.startPlan(ctx, planOf(t, s, conversation).ID, nil, false)
	if err != nil || p.Tasks[0].Error == "" {
		t.Fatalf("expected a visible admission failure: %+v %v", p, err)
	}
	// The concurrent writer runs after the failed save releases the saving window.
	if s.saveInbox(ctx, []*InboxItem{it})[it.ID] {
		t.Fatal("persist newer Inbox write")
	}
	s.inbox.mu.Lock()
	id, summary := it.Agent.TodoID, it.Summary
	s.inbox.mu.Unlock()
	row, err := st.GetTodo(ctx, id)
	if err != nil || row == nil {
		t.Fatalf("newer Inbox write points at a deleted task %q: %v", id, err)
	}
	if row.InboxItem != it.ID || p.Tasks[0].TodoID != id || summary != "A newer user edit must survive" {
		t.Fatalf("concurrent winner lost its task or user edit: %+v, plan %+v, summary %q", row, p, summary)
	}
	if td, _ := s.todoNow(id); len(td.Agents) != 0 || s.taskStartOnWay(id) {
		t.Fatal("an unpersisted admission started an Agent")
	}
	var retry map[string][]map[string]any
	_ = json.Unmarshal(taskInboxArgs(it.ID), &retry)
	retry["tasks"][0]["todo_id"] = id
	raw, _ := json.Marshal(retry)
	if _, err := s.toolTaskPlanFrom(ctx, "box", conversation, raw); err != nil {
		t.Fatal(err)
	}
	_, plans, err := s.feed(ctx, conversation, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, next := range plans {
		if next.ID == p.ID {
			continue
		}
		retried, err := s.startPlan(ctx, next.ID, nil, false)
		if err != nil || retried.Tasks[0].TodoID != id || retried.Tasks[0].Error != "" {
			t.Fatalf("retry on the retained task: %+v %v", retried, err)
		}
	}
	waitFor(t, "retained task retry", func() bool {
		td, _ := s.todoNow(id)
		return len(td.Agents) == 1
	})
	if todoCount(s) != 1 {
		t.Fatal("retry duplicated the retained task")
	}
}

func TestTaskPlanInboxFailedAdmissionLeavesNoDanglingLink(t *testing.T) {
	for _, failure := range []string{"create", "source", "item"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			conversation := "inbox-failure-" + failure
			st := &taskInboxFailStore{DB: todoTestDB(t)}
			s, _ := newTaskServiceOn(t, st)
			it := taskInboxItem(t, s, "example:source", "box", conversation)
			args := taskInboxArgs(it.ID)
			wantTodos := 0
			if failure == "source" {
				existing, err := s.createTodo(ctx, "Existing task", "", conversation, "")
				if err != nil {
					t.Fatal(err)
				}
				var reused map[string][]map[string]any
				_ = json.Unmarshal(args, &reused)
				reused["tasks"][0]["todo_id"] = existing.ID
				args, _ = json.Marshal(reused)
				wantTodos = 1
			}
			if _, err := s.toolTaskPlanFrom(ctx, "box", conversation, args); err != nil {
				t.Fatal(err)
			}
			st.failCreate.Store(failure == "create")
			st.failEdit.Store(failure == "source")
			st.failInbox.Store(failure == "item")
			p, err := s.startPlan(ctx, planOf(t, s, conversation).ID, nil, false)
			if err != nil || p.Tasks[0].Error == "" || p.Tasks[0].TodoID != "" || todoCount(s) != wantTodos {
				t.Fatalf("failed admission: %+v %v, todos %d", p, err, todoCount(s))
			}
			s.inbox.mu.Lock()
			if it.Agent != nil && it.Agent.TodoID != "" || !it.open() {
				t.Errorf("failed admission left a link: %+v", it)
			}
			s.inbox.mu.Unlock()
			rows, err := st.ListTodos(ctx)
			if err != nil || len(rows) != wantTodos {
				t.Fatalf("dangling database tasks: %+v %v", rows, err)
			}
			for _, row := range rows {
				if row.InboxItem != "" {
					t.Fatalf("failed source write changed the reused task: %+v", row)
				}
			}
			s.tasks.mu.Lock()
			holds := len(s.tasks.reusing)
			s.tasks.mu.Unlock()
			if holds != 0 {
				t.Fatal("failed admission retained its task reservation")
			}
		})
	}
}

func TestTaskPlanInboxReuseCannotReplaceAnotherSource(t *testing.T) {
	ctx := context.Background()
	s, _ := newTaskService(t)
	it := taskInboxItem(t, s, "example:source", "box", "c1x1x1")
	td, err := s.createTodo(ctx, "Another item's task", "", "c1x1x1", "")
	if err != nil {
		t.Fatal(err)
	}
	other := "example:other"
	if _, err := s.editTodo(ctx, td.ID, db.TodoEdit{InboxItem: &other}); err != nil {
		t.Fatal(err)
	}
	var args map[string][]map[string]any
	_ = json.Unmarshal(taskInboxArgs(it.ID), &args)
	args["tasks"][0]["todo_id"] = td.ID
	raw, _ := json.Marshal(args)
	if _, err := s.toolTaskPlanFrom(ctx, "box", "c1x1x1", raw); err == nil || !strings.Contains(err.Error(), "another Inbox item") {
		t.Fatalf("cross-item reuse: %v", err)
	}
}

func TestTaskPlanInboxExplicitLinkSurvivesURLRecheck(t *testing.T) {
	ctx := context.Background()
	s, f := newTaskService(t)
	f.inbox = newFakeInbox()
	it := taskInboxItem(t, s, "example:source", "box", "c1x1x1")
	gh := githubItem(it.ID, "issues", "10")
	s.inbox.mu.Lock()
	gh.Handoffs = it.Handoffs
	s.inbox.items[it.ID] = gh
	s.inbox.mu.Unlock()
	if _, err := s.toolTaskPlanFrom(ctx, "box", "c1x1x1", taskInboxArgs(it.ID)); err != nil {
		t.Fatal(err)
	}
	p, err := s.startPlan(ctx, planOf(t, s, "c1x1x1").ID, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	s.linkInboxTaskURLs(ctx)
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	if gh.Agent == nil || gh.Agent.TodoID != p.Tasks[0].TodoID || gh.Agent.TodoID == "" {
		t.Fatalf("inferred repository matching erased explicit provenance: %+v", gh)
	}
}

func TestTaskPlanInboxOneFinishedTaskDoesNotFileOtherWork(t *testing.T) {
	ctx := context.Background()
	s, _ := newTaskService(t)
	it := taskInboxItem(t, s, "example:source", "box", "c1x1x1")
	first, _ := s.createTodo(ctx, "First task", "", "c1x1x1", "")
	last, _ := s.createTodo(ctx, "Second task", "", "c1x1x1", "")
	for _, id := range []string{first.ID, last.ID} {
		if _, err := s.editTodo(ctx, id, db.TodoEdit{InboxItem: &it.ID}); err != nil {
			t.Fatal(err)
		}
	}
	s.inbox.mu.Lock()
	it.Agent = &InboxAgent{State: AgentSkipped, TodoID: last.ID}
	s.inbox.mu.Unlock()
	done := true
	if _, err := s.editTodo(ctx, last.ID, db.TodoEdit{Done: &done}); err != nil {
		t.Fatal(err)
	}
	s.fileTaskDone(ctx, []*InboxItem{it})
	s.inbox.mu.Lock()
	open := it.open()
	s.inbox.mu.Unlock()
	if !open {
		t.Fatal("one task filed an item with other unfinished work")
	}
	if _, err := s.editTodo(ctx, first.ID, db.TodoEdit{Done: &done}); err != nil {
		t.Fatal(err)
	}
	s.fileTaskDone(ctx, []*InboxItem{it})
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	if it.open() {
		t.Fatal("all completed tasks did not settle the item")
	}
}

func TestTaskPlanCanDeclineInboxSourceWithSeveralHandoffs(t *testing.T) {
	const conversation = "sample-inbox-handoffs"
	s, _ := newTaskService(t)
	a := taskInboxItem(t, s, "example:first", "box", conversation)
	b := taskInboxItem(t, s, "example:second", "box", conversation)
	if _, err := s.toolTaskPlanFrom(t.Context(), "box", conversation, taskInboxArgs("none")); err != nil {
		t.Fatalf("an unrelated task should be accepted: %v", err)
	}
	plan := planOf(t, s, conversation)
	got := plan.Tasks[0]
	if got.InboxItem != "" || got.InboxHandoff != "" {
		t.Fatalf("unrelated plan recorded an Inbox association: %q %q", got.InboxItem, got.InboxHandoff)
	}
	started, err := s.startPlan(t.Context(), plan.ID, nil, false)
	if err != nil || started.Tasks[0].Error != "" {
		t.Fatalf("unrelated plan did not start: %+v, %v", started, err)
	}
	if todo := todoNow(s, started.Tasks[0].TodoID); todo.InboxItem != "" {
		t.Fatalf("new task was linked to %q", todo.InboxItem)
	}
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	for _, it := range []*InboxItem{a, b} {
		if it.Agent != nil && it.Agent.TodoID != "" {
			t.Fatalf("handoff %s was attached to %s", it.ID, it.Agent.TodoID)
		}
	}
}

func TestExistingTaskToolsCanDeclineInboxSource(t *testing.T) {
	for _, name := range []string{"agent_send", "agent_send with task", "task_scope"} {
		t.Run(name, func(t *testing.T) {
			s, f := newQuietService(t)
			task := agentTask(t, s, f)
			a := taskInboxItem(t, s, "example:first", "server", "conv1")
			b := taskInboxItem(t, s, "example:second", "server", "conv1")
			args := map[string]any{"inbox_item": "none"}
			tool := name
			if name == "task_scope" {
				args["todo_id"], args["change"], args["why"] = task.ID, "Check the sample output", "It needs a second example"
			} else {
				tool = "agent_send"
				args["machine"], args["session_id"], args["text"] = task.Agents[0].MachineID, task.Agents[0].SessionID, "Check the sample output"
				if name == "agent_send with task" {
					args["task"] = map[string]any{"title": task.Title, "done_when": []string{"Sample output checked"}}
				}
			}
			if _, err := runJSON(t, s, "conv1", tool, args); err != nil {
				t.Fatalf("unrelated action refused: %v", err)
			}
			if todo := todoNow(s, task.ID); todo.InboxItem != "" {
				t.Fatalf("unrelated action changed the task source: %s", todo.InboxItem)
			}
			s.inbox.mu.Lock()
			defer s.inbox.mu.Unlock()
			for _, it := range []*InboxItem{a, b} {
				if it.Agent != nil && it.Agent.TodoID != "" {
					t.Fatalf("handoff %s was attached to %s", it.ID, it.Agent.TodoID)
				}
			}
		})
	}
}

func TestExistingTaskToolsOmittedSourceIgnoresUnrelatedHandoff(t *testing.T) {
	for _, name := range []string{"agent_send", "agent_send with task", "task_scope"} {
		t.Run(name, func(t *testing.T) {
			s, f := newQuietService(t)
			task := agentTask(t, s, f)
			a := taskInboxItem(t, s, "example:first", "server", "conv1")
			b := taskInboxItem(t, s, "example:second", "server", "conv1")
			s.inbox.mu.Lock()
			a.Agent = &InboxAgent{TodoID: "another-task"}
			now := time.Now()
			b.DoneAt = &now
			s.inbox.mu.Unlock()
			args := map[string]any{}
			tool := name
			if name == "task_scope" {
				args["todo_id"], args["change"], args["why"] = task.ID, "Check the sample output", "It needs a second example"
			} else {
				tool = "agent_send"
				args["machine"], args["session_id"], args["text"] = task.Agents[0].MachineID, task.Agents[0].SessionID, "Check the sample output"
				if name == "agent_send with task" {
					args["task"] = map[string]any{"title": task.Title, "done_when": []string{"Sample output checked"}}
				}
			}
			if _, err := runJSON(t, s, "conv1", tool, args); err != nil {
				t.Fatalf("unrelated action refused: %v", err)
			}
			if todo := todoNow(s, task.ID); todo.InboxItem != "" {
				t.Fatalf("unrelated action changed the task source: %s", todo.InboxItem)
			}
			s.inbox.mu.Lock()
			defer s.inbox.mu.Unlock()
			for _, it := range []*InboxItem{a, b} {
				if it.Agent != nil && it.Agent.TodoID != "another-task" {
					t.Fatalf("handoff %s was attached to %s", it.ID, it.Agent.TodoID)
				}
			}
		})
	}
}
