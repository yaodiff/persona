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

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

type workspaceCallDeadlineHub struct {
	Hub
	observe func(context.Context, DeviceCall)
}

func (h workspaceCallDeadlineHub) DeviceCall(ctx context.Context, call DeviceCall) (json.RawMessage, error) {
	h.observe(ctx, call)
	return h.Hub.DeviceCall(ctx, call)
}

func TestBackendWorkspaceCallAllowsColdFetchAndPreservesTimeoutCode(t *testing.T) {
	f := newFakeHub().withMachine()
	s := newKBService(t, f)
	req := taskworkspace.Request{WorkspaceID: "ws_timeout", OperationID: "op_timeout"}
	req.Plan.Source.MachineID = "server"
	req.Plan.Source.Repositories.Repositories = []repositorywire.PreparationRepository{{RepoID: "first"}, {RepoID: "second"}}
	f.answerNode(taskworkspace.MethodPrefix+"prepare", func(string, json.RawMessage) (any, error) {
		return taskworkspace.Record{
			WorkspaceID: req.WorkspaceID, OperationID: req.OperationID, State: "failed",
			Error: taskworkspace.Failure("source_fetch_timeout", "source", req.OperationID, "second", ""),
		}, nil
	})
	f.answerNode(taskworkspace.MethodPrefix+"inspect", func(string, json.RawMessage) (any, error) {
		return taskworkspace.Record{WorkspaceID: req.WorkspaceID}, nil
	})
	setHub(s, workspaceCallDeadlineHub{Hub: f, observe: func(ctx context.Context, call DeviceCall) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Errorf("%s has no deadline", call.Method)
			return
		}
		left := time.Until(deadline)
		switch call.Method {
		case taskworkspace.MethodPrefix + "prepare":
			if left <= 20*time.Minute {
				t.Errorf("prepare budget %v cannot cover two sequential cold fetches", left)
			}
		case taskworkspace.MethodPrefix + "inspect":
			if left > 5*time.Minute {
				t.Errorf("inspect budget increased to %v", left)
			}
		}
	}})
	got, err := s.backendWorkspaceCall(t.Context(), "prepare", req)
	if err != nil || got.Error == nil || got.Error.Code != "source_fetch_timeout" || !slices.Contains(got.Error.Actions, "retry") {
		t.Fatalf("prepare = %+v, %v; want retryable fetch timeout", got, err)
	}
	if _, err := s.backendWorkspaceCall(t.Context(), "inspect", req); err != nil {
		t.Fatal(err)
	}
}

// Interleave another Manager call after registration, before the first prepare reaches Node.
type heldWorkspacePrepareHub struct {
	Hub
	beforePrepare func()
}

func (h *heldWorkspacePrepareHub) DeviceCall(ctx context.Context, call DeviceCall) (json.RawMessage, error) {
	if call.Method == "task.workspace.prepare" && h.beforePrepare != nil {
		before := h.beforePrepare
		h.beforePrepare = nil
		before()
	}
	return h.Hub.DeviceCall(ctx, call)
}

func TestBackendWorkspaceManagerRecoveryAndLegacyCompatibility(t *testing.T) {
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	f := newFakeHub()
	f.sessions["box"] = nil
	f.sessions["first-box"] = nil
	var firstBox *taskworkspace.Record
	missingReceipt := true
	var physical *taskworkspace.Record
	created, starts := 0, 0
	lose := true
	dropCancel := false
	f.nodes = map[string]nodeAnswer{
		"workspace.naming.resolve": func(_ string, raw json.RawMessage) (any, error) {
			var src workspacenaming.Source
			json.Unmarshal(raw, &src)
			return workspacenaming.Resolve(src, workspacenaming.NodeContext{Home: "/home/example"}), nil
		},
		"task.workspace.capabilities": func(string, json.RawMessage) (any, error) {
			return map[string]string{"capability": taskworkspace.Capability}, nil
		},
		"task.workspace.prepare": func(machine string, raw json.RawMessage) (any, error) {
			var req taskworkspace.Request
			json.Unmarshal(raw, &req)
			if machine == "first-box" {
				if firstBox == nil || firstBox.OperationID != req.OperationID || firstBox.State != "removed" {
					return nil, errors.New("late prepare arrived without a removal receipt")
				}
				return *firstBox, nil
			}
			if physical != nil && physical.OperationID != req.OperationID && (physical.OperationID != req.PreviousOperationID || physical.State != "cancelled") {
				return nil, errors.New("Node still owns an uncancelled operation")
			}
			if physical == nil || physical.OperationID != req.OperationID {
				created++
				r := taskworkspace.Record{SchemaVersion: 1, WorkspaceID: req.WorkspaceID, OperationID: req.OperationID, NodeOperationID: req.OperationID, Plan: req.Plan, InputDigest: taskworkspace.InputKey(req.Plan), State: "ready", Uses: []string{}}
				for i, p := range req.Plan.Repositories {
					r.Repositories = append(r.Repositories, taskworkspace.Instance{RepoID: p.RepoID, Input: req.Plan.Source.Repositories.Repositories[i], Path: p.AbsolutePath, RelativePath: p.RelativePath, TaskBranch: p.TaskBranch, CommonDir: "/home/example/owned.git", ResolvedCommit: strings.Repeat("a", 40), State: "ready"})
				}
				physical = &r
			}
			if lose {
				lose = false
				return nil, errors.New("response lost")
			}
			return *physical, nil
		},
		"task.workspace.inspect": func(_ string, raw json.RawMessage) (any, error) {
			if physical == nil {
				return taskworkspace.Record{Error: taskworkspace.Failure("not_found", "inspect", "", "", "")}, nil
			}
			var req taskworkspace.Request
			if err := json.Unmarshal(raw, &req); err != nil {
				return nil, err
			}
			if req.OperationID != physical.OperationID {
				return taskworkspace.Record{WorkspaceID: req.WorkspaceID, OperationID: req.OperationID, NodeOperationID: physical.OperationID,
					InputDigest: taskworkspace.InputKey(req.Plan), Error: taskworkspace.Failure("operation_conflict", "inspect", req.OperationID, "", "")}, nil
			}
			return *physical, nil
		},
		"task.workspace.cancel": func(machine string, raw json.RawMessage) (any, error) {
			if machine == "first-box" {
				var req taskworkspace.Request
				if err := json.Unmarshal(raw, &req); err != nil {
					return nil, err
				}
				if missingReceipt {
					return taskworkspace.Record{Error: taskworkspace.Failure("not_found", "cancel", req.OperationID, "", "")}, nil
				}
				firstBox = &taskworkspace.Record{SchemaVersion: 1, WorkspaceID: req.WorkspaceID, OperationID: req.OperationID,
					NodeOperationID: req.OperationID, Plan: req.Plan, InputDigest: taskworkspace.InputKey(req.Plan), State: "cancelled"}
				return *firstBox, nil
			}
			if physical == nil {
				return taskworkspace.Record{Error: taskworkspace.Failure("not_found", "cancel", "", "", "")}, nil
			}
			if dropCancel {
				dropCancel = false
				return nil, errors.New("Node disconnected before receiving cancellation")
			}
			physical.State = "cancelled"
			return *physical, nil
		},
		"task.workspace.cleanup": func(machine string, raw json.RawMessage) (any, error) {
			if machine == "first-box" {
				if firstBox == nil {
					return taskworkspace.Record{Error: taskworkspace.Failure("not_found", "cleanup", "op_never_sent", "", "")}, nil
				}
				firstBox.State = "removed"
				firstBox.Cleanup = &taskworkspace.CleanupResult{Removed: true}
				return *firstBox, nil
			}
			r := *physical
			r.State = "removed"
			r.Cleanup = &taskworkspace.CleanupResult{Removed: true, RetainedPaths: []string{"/home/example/owned.git"}}
			return r, nil
		},
		"task.workspace.acquire": func(string, json.RawMessage) (any, error) { return *physical, nil },
		"task.workspace.release": func(string, json.RawMessage) (any, error) { return *physical, nil },
		DeviceSessionStart: func(string, json.RawMessage) (any, error) {
			starts++
			return nil, errors.New("not part of preparation")
		},
	}
	s, _ := repositoryService(t, database)
	transport := &heldWorkspacePrepareHub{Hub: f}
	setHub(s, transport)
	w := placeDo(s, "", "POST", repoBase, `{"url":"https://github.com/example/web"}`)
	var catalog struct{ Repository RepositoryBinding }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &catalog) != nil {
		t.Fatal(w.Code, w.Body)
	}
	payload := fmt.Sprintf(`{"title":"Example","repository_bindings":{"catalog_revision":1,"repositories":[{"repo_id":%q}]}}`, catalog.Repository.ID)
	w = placeDo(s, "", "POST", "/api/v1/todos", payload)
	var createdTodo struct{ Todo Todo }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &createdTodo) != nil {
		t.Fatal(w.Code, w.Body)
	}
	id := createdTodo.Todo.ID
	base := "/api/v1/todos/" + id + "/backend-workspace"
	input := fmt.Sprintf(`{"operation_id":"op_example","machine_id":"box","layout":"root","selected_repo_ids":[%q]}`, catalog.Repository.ID)
	if w = placeDo(s, "read", "POST", base+"/prepare", input); w.Code != 403 {
		t.Fatal("readonly created resources", w.Code, w.Body)
	}
	never := namingRequest{OperationID: "op_never_sent", MachineID: "first-box", Layout: "root", SelectedRepoIDs: []string{catalog.Repository.ID}}
	if err = s.ensureBackendWorkspacePlan(t.Context(), id, never); err != nil {
		t.Fatal(err)
	}
	var next namingRequest
	if err = json.Unmarshal([]byte(input), &next); err != nil {
		t.Fatal(err)
	}
	if err = s.ensureBackendWorkspacePlan(t.Context(), id, next); err != nil {
		t.Fatal(err)
	}
	transport.beforePrepare = func() {
		// An older Node's not_found conveys no durable proof that a late
		// request cannot still arrive. Neither handoff nor removal is confirmed.
		canceled, err := s.CancelBackendWorkspace(t.Context(), id, never.OperationID)
		if err != nil || canceled.Error == nil || canceled.Error.Code != "not_found" || canceled.NodeOperationID != "" {
			t.Fatal("unconfirmed cancellation", canceled, err)
		}
		held, err := s.PrepareBackendWorkspace(t.Context(), id, next.OperationID)
		if err != nil || held.OperationID != never.OperationID || held.Error == nil || held.Error.Code != "not_found" {
			t.Fatal("new operation overtook unconfirmed cancellation", held, err)
		}
		clean, err := s.CleanupBackendWorkspace(t.Context(), id, never.OperationID, false, false)
		if err != nil || clean.State == "removed" || clean.Cleanup != nil || clean.Error == nil || clean.Error.Code != "not_found" {
			t.Fatal("unconfirmed cleanup", clean, err)
		}
		missingReceipt = false
		canceled, err = s.CancelBackendWorkspace(t.Context(), id, never.OperationID)
		if err != nil || canceled.Error != nil || canceled.NodeOperationID != never.OperationID {
			t.Fatal("missing durable cancellation", canceled, err)
		}
		clean, err = s.CleanupBackendWorkspace(t.Context(), id, never.OperationID, false, false)
		if err != nil || clean.Error != nil || clean.State != "removed" || clean.Cleanup == nil || !clean.Cleanup.Removed {
			t.Fatal("missing removal receipt", clean, err)
		}
	}
	lateFirst, err := s.PrepareBackendWorkspace(t.Context(), id, never.OperationID)
	if err != nil || lateFirst.State != "removed" || lateFirst.Error != nil || firstBox == nil || firstBox.State != "removed" || created != 0 {
		t.Fatal("late prepare revived an unused workspace", lateFirst, err)
	}
	w = placeDo(s, "", "POST", base+"/prepare", input)
	var first taskworkspace.Record
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &first) != nil || first.State != "preparing" || first.WorkspaceID == lateFirst.WorkspaceID || first.Plan.Source.MachineID != "box" || first.Error == nil || first.Error.Code != "operation_unknown" {
		t.Fatal(w.Code, w.Body)
	}
	restarted := newKBServiceOn(t, f, s.store())
	restartTransport := &heldWorkspacePrepareHub{Hub: f}
	setHub(restarted, restartTransport)
	view, err := restarted.InspectBackendWorkspace(t.Context(), id, "op_example")
	if err != nil || view.Observation == nil || view.Observation.State != "ready" {
		t.Fatal(view, err)
	}
	w = placeDo(restarted, "", "POST", base+"/prepare", `{"operation_id":"op_example"}`)
	var ready taskworkspace.Record
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ready) != nil || ready.State != "ready" || created != 1 || starts != 0 {
		t.Fatal(w.Code, w.Body, created, starts)
	}
	// Exercise launch admission against the saved T2 plan and T3 ready receipt.
	task := PlanTask{TodoID: id, Machine: "box", Title: "Example", Prompt: "Check the workspace", planID: "plan_backend",
		Workspace: &TaskWorkspace{Mode: workspaceBackend, Backend: &BackendWorkspaceSelection{OperationID: ready.OperationID, Host: "herdr"}}}
	plan := TaskPlan{ID: task.planID, Conversation: "self", Tasks: []PlanTask{task}, State: planStarted, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := restarted.savePlan(t.Context(), &plan); err != nil {
		t.Fatal(err)
	}
	if err := restarted.admitBackendTask(t.Context(), &task); err == nil {
		t.Fatal("old Node accepted a backend launch")
	}
	f.nodes["task.workspace.capabilities"] = func(string, json.RawMessage) (any, error) {
		return map[string]any{"capability": taskworkspace.Capability, "start": true}, nil
	}
	if err := restarted.admitBackendTask(t.Context(), &task); err != nil {
		t.Fatal(err)
	}
	saved, err := restarted.loadPlan(t.Context(), plan.ID)
	if err != nil || saved.Tasks[0].WorkspaceResolved.BackendWorkspaceID != ready.WorkspaceID || saved.Tasks[0].WorkspaceResolved.WorkspaceID != "" {
		t.Fatal(saved, err)
	}
	if err := restarted.admitBackendTask(t.Context(), &task); err != nil || created != 1 {
		t.Fatal("ready retry prepared again", created, err)
	}
	dispatched := 0
	f.nodes[devicewire.MethodStartBackendSession] = func(machine string, raw json.RawMessage) (any, error) {
		var in struct {
			Workspace taskworkspace.Request `json:"backend_workspace"`
			Host      string                `json:"host"`
			Cwd       string                `json:"cwd"`
		}
		if err := json.Unmarshal(raw, &in); err != nil || in.Host != "herdr" || in.Cwd != ready.Plan.RootPath || in.Workspace.UseID != task.backendRequest.UseID {
			t.Fatalf("wrong start: %v, %v", in, err)
		}
		dispatched++
		physical.Launch = &taskworkspace.Launch{UseID: in.Workspace.UseID, SessionID: "session_example"}
		f.sessions[machine] = []proto.Session{{SessionID: "unrelated", Cwd: in.Cwd}, {SessionID: "session_example", Cwd: in.Cwd, MachineID: machine}}
		return nil, rpc.ErrClosed
	}
	var answer struct{ Session proto.Session }
	mid, sid, err := restarted.startTaskAgent(t.Context(), &task, false)
	if err != nil || mid != "box" || sid != "session_example" || dispatched != 1 || created != 1 {
		t.Fatalf("lost start reply: %s, %s, %v, starts %d, prepares %d", mid, sid, err, dispatched, created)
	}
	if task.admissionRelease != nil {
		task.admissionRelease()
	}

	if _, err := restarted.AcquireBackendWorkspace(t.Context(), id, ready.OperationID, "launch_other"); err == nil {
		t.Fatal("concurrent launch claimed a live workspace")
	}
	restarted.releaseUnusedBackendStart(t.Context(), &task) // the fixture's engine confirms release
	physical.Launch = nil
	// Manager has saved the cancellation, but the Node never received it. A
	// correction must cancel the old owner before asking the Node to adopt it.
	dropCancel = true
	old, err := restarted.CancelBackendWorkspace(t.Context(), id, ready.OperationID)
	if err != nil || old.State != "cancelled" || old.Error == nil || old.Error.Code != "operation_unknown" || physical.State != "ready" {
		t.Fatal("lost cancellation", old, physical, err)
	}
	if err := restarted.startBackendTask(t.Context(), &task, map[string]any{"cwd": task.Cwd}, &answer); err == nil || dispatched != 1 {
		t.Fatal("cancelled operation dispatched", err, dispatched)
	}
	w = placeDo(restarted, "", "POST", base+"/prepare", strings.Replace(input, "op_example", "op_correction", 1))
	var corrected taskworkspace.Record
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &corrected) != nil || corrected.OperationID != "op_correction" || corrected.NodeOperationID != corrected.OperationID || corrected.WorkspaceID != ready.WorkspaceID || corrected.State != "ready" || corrected.Error != nil {
		t.Fatal("unsettled correction", w.Code, w.Body)
	}
	// Metadata edits do not invalidate the physical workspace or change task stage.
	todo, _ := restarted.todoNow(id)
	stage := todo.Stage
	if w = placeDo(restarted, "", "PATCH", "/api/v1/todos/"+id, `{"notes":"Old client edit"}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w = placeDo(restarted, "", "PUT", "/api/v1/todos/"+id+"/repositories", `{"expected_revision":1,"expected_catalog_revision":1,"repositories":[]}`); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if got, _ := restarted.todoNow(id); got.Stage != stage {
		t.Fatal("preparation changed task stage", got)
	}
	use, err := restarted.AcquireBackendWorkspace(t.Context(), id, corrected.OperationID, "session_example")
	if err != nil || use.State != "ready" {
		t.Fatal(use, err)
	}
	if _, err = restarted.CleanupBackendWorkspace(t.Context(), id, corrected.OperationID, false, false); err == nil {
		t.Fatal("cleanup ignored use")
	}
	if _, err = restarted.ReleaseBackendWorkspace(t.Context(), id, corrected.OperationID, "session_example"); err != nil {
		t.Fatal(err)
	}
	removed, err := restarted.CleanupBackendWorkspace(t.Context(), id, corrected.OperationID, false, false)
	if err != nil || removed.State != "removed" || !removed.Cleanup.Removed {
		t.Fatal(removed, err)
	}
	late, err := restarted.PrepareBackendWorkspace(t.Context(), id, corrected.OperationID)
	if err != nil || late.State != "removed" || created != 2 {
		t.Fatal("removed operation revived", late, err)
	}
	for _, action := range []string{"complete", "cancel"} {
		t.Run("late ready after "+action, func(t *testing.T) {
			w := placeDo(restarted, "", "POST", "/api/v1/todos", payload)
			var created struct{ Todo Todo }
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil {
				t.Fatal(w.Code, w.Body)
			}
			current := created.Todo.ID
			in := next
			in.OperationID = "op_late_" + action
			if err := restarted.ensureBackendWorkspacePlan(t.Context(), current, in); err != nil {
				t.Fatal(err)
			}
			physical = nil
			restartTransport.beforePrepare = func() {
				if action == "complete" {
					done := placeDo(restarted, "", "PATCH", "/api/v1/todos/"+current, `{"done":true}`)
					if done.Code != 200 {
						t.Fatal(done.Code, done.Body)
					}
				} else if _, err := restarted.CancelBackendWorkspace(t.Context(), current, in.OperationID); err != nil {
					t.Fatal(err)
				}
			}
			task := PlanTask{TodoID: current, Machine: "box", planID: "plan_late", Workspace: &TaskWorkspace{Mode: workspaceBackend, Backend: &BackendWorkspaceSelection{OperationID: in.OperationID}}}
			if err := restarted.admitBackendTask(t.Context(), &task); err == nil || task.backendRequest != nil || dispatched != 1 {
				t.Fatal("late preparation authorized launch", task, err)
			}
			stored, _, err := restarted.backendWorkspace(t.Context(), current, in.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			r, err := stored.TaskWorkspace(t.Context(), current, in.OperationID)
			if err != nil || r.State != "cancelled" {
				t.Fatal("late ready changed task intent", r, err)
			}
		})
	}
	t.Run("owner starts a manual todo and replays the same request", func(t *testing.T) {
		w := placeDo(restarted, "", "POST", "/api/v1/todos", payload)
		var created struct{ Todo Todo }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil {
			t.Fatal(w.Code, w.Body)
		}
		id := created.Todo.ID
		in := next
		in.OperationID = "op_manual"
		in.Layout = "container"
		if err := restarted.ensureBackendWorkspacePlan(t.Context(), id, in); err != nil {
			t.Fatal(err)
		}
		naming, err := restarted.store().(workspaceNamingStore).WorkspaceNamingPlanForSave(t.Context(), id, in.OperationID)
		if err != nil || naming == nil || len(naming.Repositories) != 1 {
			t.Fatalf("missing container repository: %v, %v", naming, err)
		}
		e := kbEng(t, restarted)
		for _, project := range []struct {
			slug, summary string
			dirs          []string
		}{
			{slug: "todo-project", summary: "Explicit todo project background"},
			{slug: "repo-project", summary: "Repository project background", dirs: []string{"box:" + naming.Repositories[0].AbsolutePath}},
		} {
			if _, err := e.Apply(kb.KindProject, project.slug, kb.Update{
				Dirs: project.dirs, Log: kb.TodoOutcomePrefix + project.summary, Source: kb.TodoLogSource + "Example",
			}); err != nil {
				t.Fatal(err)
			}
		}
		if edit := placeDo(restarted, "", "PATCH", "/api/v1/todos/"+id, `{"project":"todo-project"}`); edit.Code != 200 {
			t.Fatal(edit.Code, edit.Body)
		}
		physical = nil
		f.nodes[devicewire.MethodStartBackendSession] = func(machine string, raw json.RawMessage) (any, error) {
			var in struct {
				Cwd       string                `json:"cwd"`
				Prompt    string                `json:"prompt"`
				Workspace taskworkspace.Request `json:"backend_workspace"`
			}
			if err := json.Unmarshal(raw, &in); err != nil || in.Workspace.Plan.Source.TodoID != id || in.Cwd != in.Workspace.Plan.RootPath || !strings.Contains(in.Prompt, "resolved_commit") {
				return nil, errors.New("bad manual handoff")
			}
			for _, want := range []string{"Explicit todo project background", "Repository project background"} {
				if count := strings.Count(in.Prompt, want); count != 1 {
					t.Errorf("manual handoff contains %q %d times, want once", want, count)
				}
			}
			dispatched++
			row := proto.Session{SessionID: "manual_session", MachineID: machine, Cwd: in.Cwd, Agent: "claude", Status: proto.StatusIdle}
			f.sessions[machine] = append(f.sessions[machine], row)
			return map[string]any{"session": row}, nil
		}
		url := "/api/v1/todos/" + id + "/backend-workspace/start"
		body := `{"start_id":"start_example","operation_id":"op_manual","host":"herdr"}`
		if denied := placeDo(restarted, "read", "POST", url, body); denied.Code != 403 {
			t.Fatal(denied.Code)
		}
		var firstPlan string
		for i := 0; i < 2; i++ {
			resp := placeDo(restarted, "", "POST", url, body)
			var result struct{ Plan TaskPlan }
			if resp.Code != 200 || json.Unmarshal(resp.Body.Bytes(), &result) != nil || result.Plan.State != planStarted || len(result.Plan.Tasks) != 1 || result.Plan.Tasks[0].TodoID != id {
				t.Fatal(resp.Code, resp.Body)
			}
			if i == 1 && firstPlan != result.Plan.ID {
				t.Fatal("replayed a second plan")
			}
			firstPlan = result.Plan.ID
		}
		waitFor(t, "manual task agent link", func() bool {
			todo, _ := restarted.todoNow(id)
			return len(todo.Agents) == 1 && todo.Agents[0].SessionID == "manual_session"
		})
		if dispatched != 2 {
			t.Fatalf("duplicate manual start: %d", dispatched)
		}
		conflict := placeDo(restarted, "", "POST", url, strings.Replace(body, "herdr", "orca", 1))
		if conflict.Code != 409 {
			t.Fatal("reused start id with a different host", conflict.Code, conflict.Body)
		}
	})
	// The assistant has the same recovery actions without requiring UI steps.
	output, err := restarted.toolTodoWorkspace(t.Context(), "box", "self", json.RawMessage(fmt.Sprintf(`{"id":%q,"action":"inspect"}`, id)))
	if err != nil || !strings.Contains(output, "workspace") {
		t.Fatal(output, err)
	}
}
