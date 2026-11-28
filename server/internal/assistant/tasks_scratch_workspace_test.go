package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func scratchTaskService(t *testing.T) (*Service, *fakeHub, *[]nodeproto.ScratchWorkspaceRequest) {
	t.Helper()
	s, f := workspaceTaskService(t)
	f.devices = []Device{{ID: "box", Name: "Box", Home: "/home/me", DataDir: "/home/me/.persona", Online: true, Reachable: true, ScratchWorkspace: true}}
	var calls []nodeproto.ScratchWorkspaceRequest
	created := map[string]bool{}
	f.nodes[nodeproto.MethodWorkspaceScratchPrepare] = func(_ string, raw json.RawMessage) (any, error) {
		var req nodeproto.ScratchWorkspaceRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
		calls = append(calls, req)
		todoID := string(req.TodoID)
		path := "/home/me/.persona/scratch/" + todoID
		if req.DryRun {
			return nodeproto.ScratchWorkspaceResult{Path: path, Reused: created[todoID]}, nil
		}
		if created[todoID] {
			return nodeproto.ScratchWorkspaceResult{Path: path, Reused: true}, nil
		}
		created[todoID] = true
		return nodeproto.ScratchWorkspaceResult{Path: path, Created: true}, nil
	}
	return s, f, &calls
}

func TestTaskWorkspaceScratchJSONRoundTrip(t *testing.T) {
	var ws TaskWorkspace
	if err := json.Unmarshal([]byte(`"scratch"`), &ws); err != nil {
		t.Fatalf("unmarshal scratch: %v", err)
	}
	if ws.Mode != workspaceScratch {
		t.Fatalf("mode = %q, want %q", ws.Mode, workspaceScratch)
	}
	encoded, err := json.Marshal(ws)
	if err != nil || string(encoded) != `"scratch"` {
		t.Fatalf("marshal scratch = %s, %v", encoded, err)
	}
	// Existing modes continue to round-trip unchanged.
	for _, raw := range []string{`"new"`, `"none"`, `{"existing":"/code/app"}`, `{"existing":"/code/app","queue":true}`, `{"new":{"branch":"task/demo"}}`} {
		var cur TaskWorkspace
		if err := json.Unmarshal([]byte(raw), &cur); err != nil {
			t.Fatalf("unmarshal %s: %v", raw, err)
		}
		out, err := json.Marshal(cur)
		if err != nil || string(out) != raw {
			t.Fatalf("roundtrip %s -> %s, %v", raw, out, err)
		}
	}
	var bad TaskWorkspace
	if err := json.Unmarshal([]byte(`"unknown"`), &bad); err == nil {
		t.Fatal("accepted unknown string workspace mode")
	}
}

func TestTaskWorkspaceScratchPlanPreviewAndDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f, calls := scratchTaskService(t)
		worktreeStatusCalled := false
		f.nodes["worktree.status"] = func(string, json.RawMessage) (any, error) {
			worktreeStatusCalled = true
			return proto.WorktreeStatus{}, nil
		}
		args := `{"tasks":[{"title":"Web research","prompt":"Investigate ecosystem options","role":"worker","machine":"box","workspace":"scratch","readonly":false,"permission":"bypassPermissions","user_requested":false,"why":"No local repository needed","done_when":["Summary produced"]}]}`
		_, err := s.toolTaskPlan(context.Background(), "scratch-chat", json.RawMessage(args))
		if err != nil {
			t.Fatalf("task_plan scratch failed: %v", err)
		}
		p := readonlyFeedPlan(t, s, "scratch-chat")
		if len(*calls) != 1 || !(*calls)[0].DryRun {
			t.Fatalf("preview calls = %+v, want single dry_run call", *calls)
		}
		if worktreeStatusCalled {
			t.Fatal("scratch workspace should not probe worktree.status")
		}
		if len(f.starts) != 0 {
			t.Fatal("waiting plan preview must not start an agent")
		}
		pt := p.Tasks[0]
		wantPath := "/home/me/.persona/scratch/" + pt.ReservedTodoID
		if pt.Workspace == nil || pt.Workspace.Mode != workspaceScratch || pt.WorkspaceResolved == nil ||
			pt.WorkspaceResolved.Path != wantPath || pt.WorkspaceResolved.Created || pt.WorkspaceResolved.Reused {
			t.Fatalf("unexpected preview resolution: %+v", pt)
		}

		started, err := s.startPlan(context.Background(), p.ID, nil, false)
		if err != nil {
			t.Fatalf("startPlan: %v", err)
		}
		synctest.Wait()

		saved, err := s.loadPlan(context.Background(), started.ID)
		if err != nil {
			t.Fatal(err)
		}
		// Admission must have issued a real (dry_run=false) prepare and started the Agent in that scratch dir.
		var realCalls int
		for _, c := range *calls {
			if !c.DryRun {
				realCalls++
			}
		}
		if realCalls != 1 {
			t.Fatalf("real scratch prepare calls = %d, want 1 (all=%+v)", realCalls, *calls)
		}
		if len(f.starts) != 1 || f.starts[0]["cwd"] != wantPath {
			t.Fatalf("starts = %+v, want cwd %s", f.starts, wantPath)
		}
		if saved.Tasks[0].Cwd != wantPath || saved.Tasks[0].WorkspaceResolved == nil || !saved.Tasks[0].WorkspaceResolved.Created ||
			saved.Tasks[0].MachinePolicy != machineFixed || onlyTodo(s).Agents[0].Cwd != wantPath {
			t.Fatalf("saved task=%+v todo=%+v", saved.Tasks[0], onlyTodo(s))
		}
	})
}

func TestTaskWorkspaceScratchReuseForSameTodo(t *testing.T) {
	s, _, calls := scratchTaskService(t)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Research task", "", "scratch-chat", "")
	if err != nil {
		t.Fatal(err)
	}
	task := PlanTask{
		TodoID:        todo.ID,
		Machine:       "box",
		MachinePolicy: machineFlexible,
		Workspace:     &TaskWorkspace{Mode: workspaceScratch},
	}
	if err := s.admitTaskWorkspace(ctx, &task); err != nil {
		t.Fatalf("first admit: %v", err)
	}
	wantPath := "/home/me/.persona/scratch/" + todo.ID
	if task.Cwd != wantPath || !task.WorkspaceResolved.Created || task.WorkspaceResolved.Reused || task.MachinePolicy != machineFixed {
		t.Fatalf("first admit state = %+v (resolved=%+v)", task, task.WorkspaceResolved)
	}
	// Simulate retry / continuation on the same todo_id: safely reuses the same managed scratch directory.
	retryTask := PlanTask{
		TodoID:            todo.ID,
		Machine:           "box",
		MachinePolicy:     machineFixed,
		Workspace:         &TaskWorkspace{Mode: workspaceScratch},
		WorkspaceResolved: task.WorkspaceResolved,
	}
	if err := s.admitTaskWorkspace(ctx, &retryTask); err != nil {
		t.Fatalf("second admit (reuse): %v", err)
	}
	if retryTask.Cwd != wantPath || retryTask.WorkspaceResolved.Created || !retryTask.WorkspaceResolved.Reused {
		t.Fatalf("reuse state = %+v (resolved=%+v)", retryTask, retryTask.WorkspaceResolved)
	}
	if len(*calls) != 2 || (*calls)[0].DryRun || (*calls)[1].DryRun {
		t.Fatalf("calls = %+v, want two non-dry-run calls", *calls)
	}
	// Moving an already allocated scratch workspace to a different computer is refused.
	moved := retryTask
	moved.Machine = "other-box"
	if err := s.admitTaskWorkspace(ctx, &moved); err == nil || !strings.Contains(err.Error(), "cannot move") {
		t.Fatalf("expected machine_local refusal, got %v", err)
	}
}

func TestTaskWorkspaceScratchGuardrailsAndValidation(t *testing.T) {
	s, f, _ := scratchTaskService(t)
	f.devices = []Device{{ID: "box", Name: "Box", Home: "/home/me", DataDir: "/state", Online: true, Reachable: true, ScratchWorkspace: true}}
	ctx := context.Background()

	// 1. Explicit assistant workspace is STILL refused with agent_assistant_workspace even with workspace: "scratch".
	for _, cwd := range []string{"~/.persona/assistant", "/state/assistant", "/home/me/.persona/assistant-m-abc"} {
		args, _ := json.Marshal(map[string]any{
			"tasks": []any{map[string]any{
				"title": "Bad", "prompt": "Test", "role": "worker", "machine": "box",
				"cwd": cwd, "workspace": "scratch", "readonly": true, "user_requested": true, "why": "test", "goal": "test",
			}},
		})
		_, err := s.toolTaskPlan(ctx, "guard-chat", args)
		var msg *i18n.Message
		if !errors.As(err, &msg) || msg.Key != "rpc-errors.agent_assistant_workspace" {
			t.Fatalf("cwd=%q with scratch: got %v, want rpc-errors.agent_assistant_workspace", cwd, err)
		}
	}

	// 2. Combining non-assistant cwd with workspace: "scratch" is refused cleanly.
	argsWithCwd := `{"tasks":[{"title":"Bad","prompt":"Test","role":"worker","machine":"box","cwd":"/code/app","workspace":"scratch","readonly":true,"user_requested":true,"why":"test","goal":"test"}]}`
	if _, err := s.toolTaskPlan(ctx, "guard-chat", json.RawMessage(argsWithCwd)); err == nil || !strings.Contains(err.Error(), "cannot be combined with cwd") {
		t.Fatalf("scratch+cwd: got %v, want cannot be combined with cwd", err)
	}

	// 3. Omitting cwd WITHOUT workspace: "scratch" still fails as before.
	argsNoCwd := `{"tasks":[{"title":"Bad","prompt":"Test","role":"worker","machine":"box","readonly":true,"user_requested":true,"why":"test","goal":"test"}]}`
	if _, err := s.toolTaskPlan(ctx, "guard-chat", json.RawMessage(argsNoCwd)); err == nil || !strings.Contains(err.Error(), "title, prompt and cwd are required") {
		t.Fatalf("missing cwd without scratch: got %v, want title, prompt and cwd are required", err)
	}

	// 4. Defense-in-depth: if a node ever returned an assistant directory from workspace.scratch.prepare, Hub refuses it.
	f.nodes[nodeproto.MethodWorkspaceScratchPrepare] = func(string, json.RawMessage) (any, error) {
		return nodeproto.ScratchWorkspaceResult{Path: "/state/assistant", Created: true}, nil
	}
	argsScratch := `{"tasks":[{"title":"Research","prompt":"Test","role":"worker","machine":"box","workspace":"scratch","readonly":true,"user_requested":true,"why":"test","goal":"test"}]}`
	_, err := s.toolTaskPlan(ctx, "guard-chat", json.RawMessage(argsScratch))
	var msg *i18n.Message
	if !errors.As(err, &msg) || msg.Key != "rpc-errors.agent_assistant_workspace" {
		t.Fatalf("misbehaving node returning assistant dir: got %v, want rpc-errors.agent_assistant_workspace", err)
	}
	if len(f.starts) != 0 {
		t.Fatalf("expected no starts, got %v", f.starts)
	}
}

func TestTaskWorkspaceScratchOldNodeCompatibility(t *testing.T) {
	ctx := context.Background()

	// 1. Node without ScratchWorkspace capability fails cleanly before any RPC or start.
	t.Run("capability_absent", func(t *testing.T) {
		s, f, calls := scratchTaskService(t)
		f.devices[0].ScratchWorkspace = false
		args := `{"tasks":[{"title":"Research","prompt":"Investigate","role":"worker","machine":"box","workspace":"scratch","readonly":true,"user_requested":true,"why":"test","goal":"test"}]}`
		_, err := s.toolTaskPlan(ctx, "old-node-chat", json.RawMessage(args))
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != "scratch_unsupported" {
			t.Fatalf("old node plan err = %v, want rpc.Error code scratch_unsupported", err)
		}
		if len(*calls) != 0 || len(f.starts) != 0 {
			t.Fatalf("old node should not invoke RPC or start: calls=%v starts=%v", *calls, f.starts)
		}
	})

	// 2. Unknown method RPC fallback maps cleanly to scratch_unsupported.
	t.Run("unknown_method_rpc", func(t *testing.T) {
		s, f, _ := scratchTaskService(t)
		f.nodes[nodeproto.MethodWorkspaceScratchPrepare] = func(string, json.RawMessage) (any, error) {
			return nil, errors.New("unknown method workspace.scratch.prepare")
		}
		args := `{"tasks":[{"title":"Research","prompt":"Investigate","role":"worker","machine":"box","workspace":"scratch","readonly":true,"user_requested":true,"why":"test","goal":"test"}]}`
		_, err := s.toolTaskPlan(ctx, "old-rpc-chat", json.RawMessage(args))
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != "scratch_unsupported" {
			t.Fatalf("unknown method err = %v, want rpc.Error code scratch_unsupported", err)
		}
		if len(f.starts) != 0 {
			t.Fatalf("starts = %v, want 0", f.starts)
		}
	})
}

func TestTaskWorkspaceScratchAutoPlacementAndOldNodeFiltering(t *testing.T) {
	// Fleet: linux-idle (lowest load, OLD node without ScratchWorkspace), linux-heavy (NEW node with ScratchWorkspace), hub-1 (OLD).
	// Auto-placement must skip linux-idle with hit scratch_unsupported and place on linux-heavy without directory_missing.
	t.Run("skips_old_node_and_places_on_capable_node", func(t *testing.T) {
		env := newPlacementTestEnv(t)
		env.f.devices[0].ScratchWorkspace = false // hub-1
		env.f.devices[1].ScratchWorkspace = true  // linux-heavy
		env.f.devices[2].ScratchWorkspace = false // linux-idle
		env.f.devices[3].ScratchWorkspace = false // mac-1
		env.f.nodes = map[string]nodeAnswer{
			nodeproto.MethodWorkspaceScratchPrepare: func(machine string, raw json.RawMessage) (any, error) {
				var req nodeproto.ScratchWorkspaceRequest
				_ = json.Unmarshal(raw, &req)
				return nodeproto.ScratchWorkspaceResult{
					Path:    "/home/linux/.persona/scratch/" + string(req.TodoID),
					Created: !req.DryRun,
				}, nil
			},
		}
		ctx := context.Background()
		args := `{"tasks":[{"title":"Ecosystem scan","prompt":"Read docs online","role":"worker","workspace":"scratch","readonly":true,"user_requested":true,"why":"No local checkout","goal":"Summarize docs"}]}`
		if _, err := env.s.toolTaskPlanFrom(ctx, "hub-1", "conv_pub", json.RawMessage(args)); err != nil {
			t.Fatalf("task_plan auto scratch: %v", err)
		}
		env.s.sweepPendingPlacement(ctx)
		waitFor(t, "scratch auto-placed agent started", func() bool {
			return env.s.taskStarting.Load() == 0 && len(onlyTodo(env.s).Agents) == 1
		})
		agent := onlyTodo(env.s).Agents[0]
		if agent.MachineID != "linux-heavy" {
			t.Fatalf("placed on %q, want linux-heavy (the only ScratchWorkspace-capable node)", agent.MachineID)
		}
		if !strings.HasPrefix(agent.Cwd, "/home/linux/.persona/scratch/todo_") {
			t.Fatalf("started cwd = %q, want managed scratch path", agent.Cwd)
		}
	})

	// Single-hub fallback: when no non-hub computer supports scratch, hub-1 with ScratchWorkspace is selected.
	t.Run("hub_fallback_succeeds_for_scratch", func(t *testing.T) {
		env := newPlacementTestEnv(t)
		env.f.devices[0].ScratchWorkspace = true  // hub-1
		env.f.devices[1].ScratchWorkspace = false // linux-heavy
		env.f.devices[2].ScratchWorkspace = false // linux-idle
		env.f.devices[3].ScratchWorkspace = false // mac-1
		env.f.nodes = map[string]nodeAnswer{
			nodeproto.MethodWorkspaceScratchPrepare: func(machine string, raw json.RawMessage) (any, error) {
				var req nodeproto.ScratchWorkspaceRequest
				_ = json.Unmarshal(raw, &req)
				return nodeproto.ScratchWorkspaceResult{
					Path:    "/home/hub/.persona/scratch/" + string(req.TodoID),
					Created: !req.DryRun,
				}, nil
			},
		}
		ctx := context.Background()
		args := `{"tasks":[{"title":"Hub scratch","prompt":"Read docs","role":"worker","workspace":"scratch","readonly":true,"user_requested":true,"why":"No local checkout","goal":"Summarize"}]}`
		if _, err := env.s.toolTaskPlanFrom(ctx, "hub-1", "conv_pub", json.RawMessage(args)); err != nil {
			t.Fatalf("task_plan hub scratch: %v", err)
		}
		env.s.sweepPendingPlacement(ctx)
		waitFor(t, "hub scratch started", func() bool {
			return env.s.taskStarting.Load() == 0 && len(onlyTodo(env.s).Agents) == 1
		})
		agent := onlyTodo(env.s).Agents[0]
		if agent.MachineID != "hub-1" {
			t.Fatalf("placed on %q, want hub-1", agent.MachineID)
		}
		if !strings.HasPrefix(agent.Cwd, "/home/hub/.persona/scratch/todo_") {
			t.Fatalf("started cwd = %q, want managed scratch path", agent.Cwd)
		}
	})

	// When NO node supports scratch, manual assign to an old node returns 422 scratch_unsupported,
	// while fallback records concrete scratch_unsupported diagnostics (and explicit missing cwd still returns 422 directory_missing).
	t.Run("all_old_nodes_refused_and_directory_missing_preserved", func(t *testing.T) {
		env := newPlacementTestEnv(t)
		// All devices have ScratchWorkspace == false.
		ctx := context.Background()
		args := `{"tasks":[{"title":"No scratch nodes","prompt":"Read docs","role":"worker","workspace":"scratch","readonly":true,"user_requested":true,"why":"No repo","goal":"Read"}]}`
		if _, err := env.s.toolTaskPlanFrom(ctx, "hub-1", "conv_pub", json.RawMessage(args)); err != nil {
			t.Fatalf("task_plan: %v", err)
		}
		todo := onlyTodo(env.s)
		// Explicit assignment to an old node returns 422 scratch_unsupported.
		rec := env.doReq(http.MethodPost, "/api/v1/assistant/placement/"+todo.ID+"/assign", roleExtension, "sched-ext", `{"machine":"linux-idle","version":1}`)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "scratch_unsupported") {
			t.Fatalf("assign old node = %d %s, want 422 scratch_unsupported", rec.Code, rec.Body.String())
		}
		// Fallback sweep keeps task queued with scratch_unsupported hit detail.
		env.s.sweepPendingPlacement(ctx)
		p, idx, pt, found := env.s.findPendingPlacementTask(ctx, todo.ID)
		if !found || pt.Queue == nil || pt.Queue.Key != pendingPlacementQueueKey || pt.Placement == nil {
			t.Fatalf("expected queued task with placement diagnostics: plan=%+v idx=%d pt=%+v", p, idx, pt)
		}
		var sawHit bool
		for _, c := range pt.Placement.Candidates {
			for _, h := range c.Hits {
				if h.Key == "scratch_unsupported" {
					sawHit = true
				}
			}
		}
		if !sawHit {
			t.Fatalf("expected scratch_unsupported candidate hit, got %+v", pt.Placement.Candidates)
		}

		// Existing non-scratch missing directory still returns 422 directory_missing.
		env.f.nodes = map[string]nodeAnswer{
			"fs.stat": func(string, json.RawMessage) (any, error) {
				return map[string]any{"stats": []any{}}, nil
			},
		}
		argsMissing := `{"tasks":[{"title":"Missing dir","prompt":"Read code","role":"worker","cwd":"/does/not/exist","readonly":true,"user_requested":true,"why":"Repo","goal":"Read"}]}`
		if _, err := env.s.toolTaskPlanFrom(ctx, "hub-1", "conv_pub", json.RawMessage(argsMissing)); err != nil {
			t.Fatalf("task_plan missing cwd: %v", err)
		}
		var missingTodoID string
		for _, td := range env.s.todosOf(ctx, "*") {
			if td.Title == "Missing dir" {
				missingTodoID = td.ID
			}
		}
		rec2 := env.doReq(http.MethodPost, "/api/v1/assistant/placement/"+missingTodoID+"/assign", roleExtension, "sched-ext", `{"machine":"linux-idle","version":1}`)
		if rec2.Code != http.StatusUnprocessableEntity || !strings.Contains(rec2.Body.String(), "directory_missing") {
			t.Fatalf("missing dir assign = %d %s, want 422 directory_missing", rec2.Code, rec2.Body.String())
		}
	})
}

func TestTaskWorkspaceScratchFlexibleHealthDetour(t *testing.T) {
	s, f := rerouteHub(t)
	// rerouteHub has l4 (primary), server (old node: ScratchWorkspace=false), box (new node: ScratchWorkspace=true).
	for i := range f.devices {
		switch f.devices[i].ID {
		case "l4", "box":
			f.devices[i].ScratchWorkspace = true
		case "server":
			f.devices[i].ScratchWorkspace = false
		}
	}
	blockWorkLauncher(s, "l4")
	pl := newPlacement("l4", "enforce")
	ctx := context.WithValue(context.Background(), placementKey{}, pl)
	task := PlanTask{
		Machine:       "l4",
		MachinePolicy: machineFlexible,
		Launcher:      "work",
		Role:          "worker",
		Workspace:     &TaskWorkspace{Mode: workspaceScratch},
	}
	moved := s.healthyPlace(ctx, &task)
	if !moved || task.Machine != "box" || task.Cwd != "" || task.WorkspaceResolved != nil {
		t.Fatalf("healthyPlace moved=%v task=%+v, want box with empty cwd", moved, task)
	}
	var skippedOld bool
	for _, c := range pl.Candidates {
		if c.Machine == "server" {
			for _, h := range c.Hits {
				if h.Key == "scratch_unsupported" {
					skippedOld = true
				}
			}
		}
	}
	if !skippedOld {
		t.Fatalf("expected server to be skipped with scratch_unsupported hit, got %+v", pl.Candidates)
	}
}
