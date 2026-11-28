package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func TestTaskDispatchPreparesWorkspaceOnNode(t *testing.T) {
	for _, tc := range []struct {
		name         string
		preferNew    bool
		occupied     bool
		explicit     bool
		plain        bool
		boundCount   int
		explicitName string
	}{
		{name: "prefer new", preferNew: true},
		{name: "occupied cwd", occupied: true},
		{name: "explicit new", explicit: true},
		{name: "explicit branch", explicit: true, explicitName: "topic/requested"},
		{name: "folder only", explicit: true, plain: true},
		{name: "bound remote root", explicit: true, boundCount: 1},
		{name: "bound remote container", explicit: true, boundCount: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			database, err := db.Open(ctx, newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(database.Close)
			st := repositorystore.New(database)
			h := newWorkspaceTestHarness(t, st)
			setHub(h.s, workspaceCallDeadlineHub{Hub: h.f, observe: func(ctx context.Context, call DeviceCall) {
				if call.Method != taskworkspace.MethodPrefix+"prepare" {
					return
				}
				deadline, ok := ctx.Deadline()
				left := time.Until(deadline)
				if !ok || tc.boundCount > 0 && left <= time.Duration(tc.boundCount)*taskworkspace.ColdFetchTimeout {
					t.Errorf("prepare budget %v for %d bound repositories", left, tc.boundCount)
				}
				if tc.boundCount == 0 && left > taskworkspace.RPCTimeout {
					t.Errorf("local prepare budget increased to %v", left)
				}
			}})
			h.f.nodes["task.workspace.capabilities"] = func(string, json.RawMessage) (any, error) {
				return taskworkspace.Capabilities{Capability: taskworkspace.Capability, Start: true, LocalGit: true, EmptyRepositories: true}, nil
			}
			h.f.nodes["worktree.status"] = func(string, json.RawMessage) (any, error) {
				if tc.plain {
					return proto.WorktreeStatus{}, nil
				}
				return proto.WorktreeStatus{Path: "/code/app", Branch: "feature/base"}, nil
			}
			commit := strings.Repeat("a", 40)
			h.f.nodes[taskworkspace.LocalSourceMethod] = func(_ string, raw json.RawMessage) (any, error) {
				var in taskworkspace.LocalSourceRequest
				if err := json.Unmarshal(raw, &in); err != nil {
					return nil, err
				}
				if in.Cwd != "/code/app" || in.TaskBranch != tc.explicitName || tc.boundCount > 0 {
					return nil, errors.New("unexpected local source lookup")
				}
				if tc.plain {
					return taskworkspace.LocalSource{}, nil
				}
				return taskworkspace.LocalSource{Repository: &repositorywire.PreparationRepository{
					RepoID: "local", DirectoryAlias: "app", RequestedRef: repository.RequestedRef{Kind: "branch", Value: "feature/base"},
					Local: &repositorywire.LocalCheckout{CommonDir: "/code/app/.git", Commit: commit},
				}}, nil
			}
			if _, err := h.s.updateSettings(ctx, func(set *Settings) error {
				if tc.preferNew {
					set.TaskWorkspacePreference = preferNew
				}
				set.WorkspaceRules = workspacenaming.Rules{BaseDirectory: "{home}/projects",
					TaskDirectoryTemplate: "dispatch/{task-id}", BranchTemplate: "review/{base-branch}/{task-id}"}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if tc.occupied {
				owner, err := h.s.createTodo(ctx, "Current writer", "", "other-chat", "")
				if err != nil || !h.s.preclaimWorkspace(ctx, "box", "/code/app", owner.ID) {
					t.Fatal("occupy source", err)
				}
			}
			task := map[string]any{"title": "Prepare an isolated task", "prompt": "Complete the task", "machine": "box",
				"cwd": "/code/app", "permission": "bypassPermissions"}
			if tc.explicit {
				task["workspace"] = "new"
			}
			if tc.explicitName != "" {
				task["workspace"] = map[string]any{"new": map[string]string{"branch": tc.explicitName}}
			}
			if tc.boundCount > 0 {
				todo, err := h.s.createTodo(ctx, "Bound task", "", "dispatch-chat", "")
				if err != nil {
					t.Fatal(err)
				}
				task["todo_id"] = todo.ID
				var inputs []repository.TaskInput
				for _, id := range []string{"repo_web", "repo_api"}[:tc.boundCount] {
					inputs = append(inputs, repository.TaskInput{RepoID: id, RequestedRef: repository.RequestedRef{Kind: "branch", Value: "feature/base"}})
				}
				if _, err := h.s.setTodoRepositories(ctx, todo.ID, repository.SetInput{ExpectedRevision: new(int64(0)), ExpectedCatalogRevision: new(int64(1)), Repositories: inputs}); err != nil {
					t.Fatal(err)
				}
				if _, err := st.SetTodoWorkspaceRules(ctx, todo.ID, 0, json.RawMessage(`{"task_directory_template":"bound/{task-id}"}`)); err != nil {
					t.Fatal(err)
				}
			}
			raw, err := json.Marshal(map[string]any{"tasks": []map[string]any{task}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.s.toolTaskPlan(ctx, "dispatch-chat", raw); err != nil {
				t.Fatal(err)
			}
			plan := readonlyFeedPlan(t, h.s, "dispatch-chat")
			id := plan.Tasks[0].ReservedTodoID
			dir, branch, count := "/home/example/projects/dispatch/"+id, "review/feature-base/"+id, 1
			if tc.explicitName != "" {
				branch = tc.explicitName
			}
			if tc.plain {
				branch, count = "", 0
			}
			if tc.boundCount > 0 {
				dir, count = "/home/example/projects/bound/"+id, tc.boundCount
				if count > 1 {
					branch = ""
				}
			}
			resolved := plan.Tasks[0].WorkspaceResolved
			if resolved == nil || resolved.Path != dir || resolved.Branch != branch || len(resolved.Repositories) != count ||
				h.prepareCalls != 0 || h.startCalls != 0 {
				t.Fatal("plan was not read-only or used the wrong path", resolved)
			}
			if _, err := h.s.startPlan(ctx, plan.ID, nil, false); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "managed workspace launch", func() bool {
				todo, ok := h.s.todoNow(id)
				return ok && (len(todo.Agents) == 1 || todo.StartError != "")
			})
			todo, _ := h.s.todoNow(id)
			if len(todo.Agents) != 1 || todo.Agents[0].Cwd != dir {
				t.Fatal("dispatch failed", todo.StartError, todo.Agents)
			}
			h.mu.Lock()
			start, prepared, prepares, starts := h.lastStart, h.lastPrepare, h.prepareCalls, h.startCalls
			h.mu.Unlock()
			h.f.mu.Lock()
			legacyCalls := len(h.f.worktrees)
			h.f.mu.Unlock()
			if legacyCalls != 0 || prepares != 1 || starts != 1 || start.cwd != dir || start.workspaceID != "" ||
				!start.workspace.TaskDispatch || start.workspace.UseID == "" || start.workspace.OperationID != prepared.OperationID {
				t.Fatalf("dispatch = %d engine calls, %d prepares, %d starts (%+v)", legacyCalls, prepares, starts, start)
			}
			got, err := st.TaskWorkspace(ctx, id, prepared.OperationID)
			if err != nil || got == nil || got.State != "ready" || len(got.Uses) != 1 || len(got.Repositories) != count {
				t.Fatal("workspace receipt missing", got, err)
			}
			for i, row := range got.Repositories {
				if tc.boundCount == 0 && (row.Input.Local == nil || row.ResolvedCommit != commit) ||
					tc.boundCount > 0 && (row.Input.Local != nil || row.Input.SourceSnapshot.CloneURL == "") {
					t.Fatal("wrong source for repository " + strconv.Itoa(i))
				}
			}
		})
	}
}

func TestTaskDispatchDoesNotFallBackAfterCapabilityTransportFailure(t *testing.T) {
	s, f := workspaceTaskService(t)
	f.nodes["task.workspace.capabilities"] = func(string, json.RawMessage) (any, error) {
		return nil, errors.New("node disconnected")
	}
	task := PlanTask{Title: "Isolated task", Machine: "box", Cwd: "/code/app", Workspace: &TaskWorkspace{Mode: workspaceNew}}
	err := s.resolveTaskWorkspace(t.Context(), &task)
	if p, ok := errors.AsType[*taskworkspace.Problem](err); !ok || p.Code != "node_unavailable" || len(f.worktrees) != 0 {
		t.Fatalf("unexpected engine fallback: %v, calls %v", err, f.worktrees)
	}
}

func TestTaskDispatchRecoversNamingPlanSavedBeforeTaskUpdate(t *testing.T) {
	ctx := t.Context()
	st := newContainerWorkspaceMemStore()
	h := newWorkspaceTestHarness(t, st)
	todo, err := h.s.createTodo(ctx, "Recover an isolated task", "", "dispatch-chat", "")
	if err != nil {
		t.Fatal(err)
	}
	task := PlanTask{TodoID: todo.ID, planID: "plan-example", Machine: "box", Cwd: "/code/app", Workspace: &TaskWorkspace{Mode: workspaceNew}}
	op := "op_" + taskworkspace.Hash([]string{todo.ID, task.planID, "0", "new"})[:24]
	set, err := st.TodoRepositories(ctx, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.Snapshot(h.s.managerIdentity(), todo.ID, set.Revision, set.Repositories, nil)
	if err != nil {
		t.Fatal(err)
	}
	preview := workspacenaming.Resolve(workspacenaming.Source{Manager: h.s.managerIdentity(), TodoID: todo.ID,
		Title: todo.Title, MachineID: task.Machine, PlannedAt: time.Now(), Layout: repository.LayoutContainer,
		Rules: workspacenaming.Defaults(), Repositories: snapshot}, workspacenaming.NodeContext{Home: t.TempDir()})
	if preview.Plan == nil {
		t.Fatal(preview.Diagnostics)
	}
	if _, err := st.SaveWorkspaceNamingPlan(ctx, todo.ID, op, 0, *preview.Plan); err != nil {
		t.Fatal(err)
	}
	h.f.answerNode(taskworkspace.MethodPrefix+"capabilities", func(string, json.RawMessage) (any, error) {
		return taskworkspace.Capabilities{Capability: taskworkspace.Capability, Start: true}, nil
	})
	err = h.s.admitTaskWorkspace(ctx, &task)
	if p, ok := errors.AsType[*taskworkspace.Problem](err); !ok || p.Code != "unsupported" || p.OperationID != op ||
		task.WorkspaceResolved == nil || task.WorkspaceResolved.Path != preview.Plan.RootPath || len(h.f.worktrees) != 0 {
		t.Fatalf("lost the frozen operation during node rollback: workspace=%+v calls=%v err=%v", task.WorkspaceResolved, h.f.worktrees, err)
	}
}

func TestDriftedDispatchReplacesWorkspaceOnRetryAndContinuation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		retry   bool
		oldLive bool
	}{
		{name: "retry frozen operation", retry: true},
		{name: "continues with a different branch"},
		{name: "continues cannot release a live launch", oldLive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			database, err := db.Open(ctx, newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(database.Close)
			st := repositorystore.New(database)
			h := newWorkspaceTestHarness(t, st)
			h.f.nodes["task.workspace.capabilities"] = func(string, json.RawMessage) (any, error) {
				return taskworkspace.Capabilities{Capability: taskworkspace.Capability, Start: true, LocalGit: true}, nil
			}
			sourceRepo := repositorywire.PreparationRepository{
				RepoID: "local", DirectoryAlias: "app", RequestedRef: repository.RequestedRef{Kind: "branch", Value: "main"},
				Local: &repositorywire.LocalCheckout{CommonDir: "/code/app/.git", Commit: strings.Repeat("a", 40)},
			}
			h.f.nodes[taskworkspace.LocalSourceMethod] = func(string, json.RawMessage) (any, error) {
				return taskworkspace.LocalSource{Repository: &sourceRepo}, nil
			}

			const chat, oldPlan, oldUse = "dispatch-chat", "plan_original", "launch_previous"
			todo, err := h.s.createTodo(ctx, "Recover a task", "Complete the task", chat, oldPlan)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := repositorywire.LocalSnapshot(h.s.managerIdentity(), todo.ID, sourceRepo)
			if err != nil {
				t.Fatal(err)
			}
			src := workspacenaming.Source{Manager: h.s.managerIdentity(), TodoID: todo.ID, Title: todo.Title, MachineID: "box",
				PlannedAt: time.Now(), Layout: repository.LayoutRoot, Rules: workspacenaming.Defaults(), Repositories: snapshot}
			view := workspacenaming.Resolve(src, workspacenaming.NodeContext{Home: "/home/example"})
			if view.Plan == nil {
				t.Fatal(view.Diagnostics)
			}
			task := PlanTask{TodoID: todo.ID, Title: todo.Title, Prompt: todo.Notes, Machine: "box",
				Launcher: "claude", Permission: "bypassPermissions", planID: oldPlan, Workspace: &TaskWorkspace{Mode: workspaceNew}}
			op := task.dispatchedWorkspaceOperationID()
			if _, err := st.SaveWorkspaceNamingPlan(ctx, todo.ID, op, 0, *view.Plan); err != nil {
				t.Fatal(err)
			}
			applySavedBackendPlan(&task, view.Plan, op)
			p := &TaskPlan{ID: oldPlan, Conversation: chat, State: planStarted, Tasks: []PlanTask{task},
				CreatedAt: time.Now(), UpdatedAt: time.Now()}
			if err := h.s.savePlan(ctx, p); err != nil {
				t.Fatal(err)
			}
			prepared, err := h.s.PrepareBackendWorkspace(ctx, todo.ID, op)
			if err != nil || prepared.State != "ready" {
				t.Fatal("prepare old workspace", prepared, err)
			}
			use, err := h.s.AcquireBackendWorkspace(ctx, todo.ID, op, oldUse)
			if err != nil {
				t.Fatal(err)
			}
			drift := taskworkspace.Failure("workspace_drift", "inspect", op, "local", prepared.Plan.RootPath)
			h.mu.Lock()
			h.physical[op].Error = drift
			h.physical[op].Launch = &taskworkspace.Launch{UseID: oldUse, SessionID: "previous-agent", TaskDispatch: true}
			h.mu.Unlock()
			release := h.f.nodes["task.workspace.release"]
			h.f.nodes["task.workspace.release"] = func(machine string, raw json.RawMessage) (any, error) {
				if tc.oldLive {
					observed := *h.physical[op]
					observed.Error = taskworkspace.Failure("worktree_in_use", "release", op, "", "")
					return observed, nil
				}
				h.mu.Lock()
				h.physical[op].Error = nil
				h.mu.Unlock()
				return release(machine, raw)
			}
			if tc.retry {
				use.State, use.Error = "failed", drift
				if _, err := st.SaveTaskWorkspaceResult(ctx, todo.ID, op, use.Revision, *use); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.s.failTodo(ctx, todo.ID, drift.Error()); err != nil {
				t.Fatal(err)
			}
			if tc.retry {
				if _, err := h.s.retryTask(ctx, todo.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				raw, err := json.Marshal(map[string]any{"tasks": []map[string]any{{
					"title": todo.Title, "prompt": todo.Notes, "machine": "box", "cwd": "/code/app",
					"continues": todo.ID, "continues_why": "The previous checkout drifted", "readonly": true,
					"workspace": map[string]any{"new": map[string]any{"branch": "topic/replacement"}},
				}}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := h.s.toolTaskPlan(ctx, chat, raw); err != nil {
					t.Fatal(err)
				}
			}
			waitFor(t, "the start outcome", func() bool {
				todo, ok := h.s.todoNow(todo.ID)
				if !ok {
					return false
				}
				return len(todo.Agents) == 1 || tc.oldLive && strings.Contains(todo.StartError, "worktree_in_use")
			})
			head, err := st.TaskWorkspace(ctx, todo.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			prior, err := st.TaskWorkspace(ctx, todo.ID, op)
			if err != nil {
				t.Fatal(err)
			}
			if tc.oldLive {
				if head.OperationID != op || len(prior.Uses) != 1 {
					t.Fatalf("the live launch lost its workspace: head=%+v prior=%+v", head, prior)
				}
				return
			}
			if head.OperationID == op || head.Plan.RootPath == prior.Plan.RootPath || head.State != "ready" ||
				prior.State != "cancelled" || len(prior.Uses) != 0 || head.PreviousOperationID != "" {
				t.Fatalf("workspace was not safely replaced: head=%+v prior=%+v", head, prior)
			}
			agent := todoNow(h.s, todo.ID).Agents[0]
			if agent.Cwd != head.Plan.RootPath || !strings.Contains(agent.Cwd, "-retry-") {
				t.Fatalf("agent did not start in the fresh checkout: %+v", agent)
			}
			if !tc.retry && head.Repositories[0].TaskBranch != "topic/replacement" {
				t.Fatalf("continuation lost the requested branch: %+v", head.Repositories)
			}
			if tc.retry && head.Repositories[0].TaskBranch == prior.Repositories[0].TaskBranch {
				t.Fatal("automatic recovery reused the previous branch")
			}
		})
	}
}
