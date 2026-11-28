package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func seedRepoWorkflowBinding(t *testing.T, s *Service, id, rawURL, workflow string) {
	t.Helper()
	parsed, err := repository.Parse(rawURL, "")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := s.repositoryDocument(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range doc.Repositories {
		if doc.Repositories[i].ID == id {
			doc.Repositories[i].Identity = parsed
			doc.Repositories[i].TaskWorkflow = workflow
			found = true
			break
		}
	}
	if !found {
		doc.Repositories = append(doc.Repositories, RepositoryBinding{
			Identity:     parsed,
			ID:           id,
			TaskWorkflow: workflow,
			VerifiedAt:   time.Now().UTC(),
		})
	}
	doc.Revision++
	if err := s.store().PutSetting(context.Background(), repositoriesKey, doc); err != nil {
		t.Fatal(err)
	}
}

type workflowPreferenceMemStore struct {
	*containerWorkspaceMemStore
}

func (m *workflowPreferenceMemStore) TaskPlansQueued(_ context.Context, _ time.Time) ([]db.TaskPlanRow, error) {
	m.memRepoStore.mu.Lock()
	defer m.memRepoStore.mu.Unlock()
	out := make([]db.TaskPlanRow, 0, len(m.memRepoStore.plans))
	for _, row := range m.memRepoStore.plans {
		if row.State == planStarted {
			out = append(out, row)
		}
	}
	return out, nil
}

func workflowPreferenceService(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	f := newFakeHub()
	f.devices = []Device{
		{ID: "server", Name: "Server", Home: "/home/me", Online: true, Reachable: true},
		{ID: "box", Name: "Box", Home: "/home/me", Online: true, Reachable: true},
	}
	f.sessions["server"], f.sessions["box"] = nil, nil
	f.launcherList = []proto.Launcher{{Name: "claude", Agent: "claude"}}
	f.ownModels = true
	f.nodes = map[string]nodeAnswer{
		"worktree.status": func(string, json.RawMessage) (any, error) {
			return proto.WorktreeStatus{Path: "/code/app", Branch: "main"}, nil
		},
		"workspace.resolve": func(_ string, raw json.RawMessage) (any, error) {
			var in struct{ Path string }
			_ = json.Unmarshal(raw, &in)
			return proto.Workspace{Cwd: strings.TrimSuffix(in.Path, "/"), Available: true}, nil
		},
	}
	s := newServiceOn(t, f, &workflowPreferenceMemStore{newContainerWorkspaceMemStore()})
	s.briefsOptional = true
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	return s, f
}

func TestTaskPlanSharedBranchQueuesSeriallyOnOccupiedWorkspace(t *testing.T) {
	s, f := workflowPreferenceService(t)
	// Even with global prefer-new, a repository explicitly set to shared_branch reuses and queues serially.
	setSettings(t, s, func(v *Settings) { v.TaskWorkspacePreference = preferNew })
	seedRepoWorkflowBinding(t, s, "repo_app", "https://github.com/example/app", repository.TaskWorkflowSharedBranch)

	f.nodes["git.status"] = func(_ string, raw json.RawMessage) (any, error) {
		return proto.RepositoryIdentity{
			Version: 1,
			Cwd:     "/code/app",
			Root:    "/code/app",
			IsRepo:  true,
			Origin:  "github.com/example/app",
		}, nil
	}

	// Task 1 omits workspace -> resolves to existing /code/app with queue:true and starts immediately.
	_, p1 := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
		"title":  "First feature step",
		"prompt": "Implement step 1",
	})
	waitWorkspaceStarts(t, s, f, 1)
	t1 := p1.Tasks[0]
	if t1.Workspace == nil || t1.Workspace.Mode != workspaceExisting || !t1.Workspace.Queue || !t1.WorkspaceInferred {
		t.Fatalf("expected inferred existing+queue workspace for shared_branch, got %+v (inferred=%v)", t1.Workspace, t1.WorkspaceInferred)
	}

	// Task 2 omits workspace while Task 1 occupies /code/app -> queues serially instead of splitting to new or failing.
	_, p2 := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
		"title":  "Second feature step",
		"prompt": "Implement step 2",
	})
	waitWorkspaceStarts(t, s, f, 1)
	t2 := p2.Tasks[0]
	if t2.Workspace == nil || t2.Workspace.Mode != workspaceExisting || !t2.Workspace.Queue {
		t.Fatalf("expected inferred existing+queue for task 2, got %+v", t2.Workspace)
	}
	todo2, ok := s.todoNow(t2.TodoID)
	if !ok || todo2.StartError != "" || todo2.DoneAt != nil {
		t.Fatalf("task 2 should be open and queued without StartError, got %+v", todo2)
	}
	loaded2, err := s.loadPlan(context.Background(), p2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded2.Tasks[0].Queue == nil || loaded2.Tasks[0].Queue.Key != workspaceQueueKey {
		t.Fatalf("expected task 2 parked in workspace_occupied queue, got %+v", loaded2.Tasks[0].Queue)
	}

	// Completing Task 1 deterministically dequeues and auto-starts Task 2 on /code/app.
	done := true
	if _, err := s.editTodo(context.Background(), t1.TodoID, db.TodoEdit{Done: &done}); err != nil {
		t.Fatal(err)
	}
	waitWorkspaceStarts(t, s, f, 2)
	loaded2, err = s.loadPlan(context.Background(), p2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded2.Tasks[0].Queue != nil {
		t.Fatalf("expected task 2 queue cleared after auto-start, got %+v", loaded2.Tasks[0].Queue)
	}
}

func TestTaskPlanIsolatedAndUnsetPreserveExistingDefaults(t *testing.T) {
	for _, tc := range []struct {
		name     string
		workflow string
	}{
		{"unset", ""},
		{"isolated", repository.TaskWorkflowIsolated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := workflowPreferenceService(t)
			seedRepoWorkflowBinding(t, s, "repo_app", "https://github.com/example/app", tc.workflow)

			var gitStatusCalls atomic.Int32
			f.nodes["git.status"] = func(_ string, _ json.RawMessage) (any, error) {
				gitStatusCalls.Add(1)
				return proto.RepositoryIdentity{Version: 1, IsRepo: true, Origin: "github.com/example/app"}, nil
			}
			f.nodes[DeviceWorktreePrepare] = func(_ string, _ json.RawMessage) (any, error) {
				return proto.WorktreePrepare{Path: "/worktrees/app"}, nil
			}
			f.nodes["worktree.create"] = func(_ string, raw json.RawMessage) (any, error) {
				var in struct{ Branch string }
				_ = json.Unmarshal(raw, &in)
				return proto.WorktreeCreateResult{Path: "/worktrees/app/" + in.Branch, Branch: in.Branch}, nil
			}

			// First task uses free existing checkout (without queue:true).
			_, p1 := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
				"title":  "First isolated task",
				"prompt": "Do work 1",
			})
			waitWorkspaceStarts(t, s, f, 1)
			if p1.Tasks[0].Workspace.Mode != workspaceExisting || p1.Tasks[0].Workspace.Queue {
				t.Fatalf("expected existing without queue, got %+v", p1.Tasks[0].Workspace)
			}

			// Second task while occupied splits to workspace:new (unchanged existing behavior).
			_, p2 := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
				"title":  "Second isolated task",
				"prompt": "Do work 2",
			})
			waitWorkspaceStarts(t, s, f, 2)
			if p2.Tasks[0].Workspace == nil || p2.Tasks[0].Workspace.Mode != workspaceNew {
				t.Fatalf("expected workspaceNew on busy checkout, got %+v", p2.Tasks[0].Workspace)
			}
			if gitStatusCalls.Load() != 0 {
				t.Fatalf("expected zero git.status calls when no shared_branch repo exists, got %d", gitStatusCalls.Load())
			}
		})
	}
}

func TestTaskPlanExplicitWorkspaceAndReadonlyOverrideSharedBranch(t *testing.T) {
	s, f := workflowPreferenceService(t)
	seedRepoWorkflowBinding(t, s, "repo_app", "https://github.com/example/app", repository.TaskWorkflowSharedBranch)
	f.nodes["git.status"] = func(_ string, _ json.RawMessage) (any, error) {
		return proto.RepositoryIdentity{Version: 1, IsRepo: true, Origin: "github.com/example/app"}, nil
	}
	f.nodes[DeviceWorktreePrepare] = func(_ string, _ json.RawMessage) (any, error) {
		return proto.WorktreePrepare{Path: "/worktrees/app"}, nil
	}
	f.nodes["worktree.create"] = func(_ string, raw json.RawMessage) (any, error) {
		var in struct{ Branch string }
		_ = json.Unmarshal(raw, &in)
		return proto.WorktreeCreateResult{Path: "/worktrees/app/" + in.Branch, Branch: in.Branch}, nil
	}

	// Explicit workspace: "new" overrides shared_branch.
	_, pNew := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
		"title":     "Explicit new",
		"prompt":    "Isolate anyway",
		"workspace": "new",
	})
	waitWorkspaceStarts(t, s, f, 1)
	if pNew.Tasks[0].Workspace.Mode != workspaceNew || pNew.Tasks[0].WorkspaceInferred {
		t.Fatalf("explicit new should win: %+v", pNew.Tasks[0].Workspace)
	}

	// Readonly task still uses workspace: "none".
	_, pRO := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
		"title":    "Readonly check",
		"prompt":   "Inspect code",
		"readonly": true,
	})
	waitWorkspaceStarts(t, s, f, 2)
	if pRO.Tasks[0].Workspace.Mode != workspaceNone {
		t.Fatalf("readonly should stay workspaceNone, got %+v", pRO.Tasks[0].Workspace)
	}
}

func TestTaskPlanSharedBranchMultiRepoInfersContainerWorkspace(t *testing.T) {
	h := newContainerTestHarness(t)
	seedRepoWorkflowBinding(t, h.s, "repo_web", "https://github.com/example/web", repository.TaskWorkflowSharedBranch)
	seedRepoWorkflowBinding(t, h.s, "repo_api", "https://github.com/example/api", repository.TaskWorkflowSharedBranch)

	// Create an existing to-do bound to both repositories.
	todo, err := h.s.createTaskTodoWithRepositories(
		context.Background(),
		"todo_multi_shared",
		"Cross-repo feature",
		"Coordinate API and web",
		"conv1",
		"",
		"",
		"",
		[]string{"repo_web", "repo_api"},
	)
	if err != nil {
		t.Fatal(err)
	}

	// Plan without workspace and without cwd: shared_branch infers workspace.container for the multi-repo to-do.
	raw, _ := json.Marshal(map[string]any{
		"tasks": []any{
			map[string]any{
				"title":          "Cross-repo feature",
				"prompt":         "Update API and web together",
				"role":           "worker",
				"machine":        "box",
				"todo_id":        todo.ID,
				"readonly":       false,
				"user_requested": true,
				"why":            "Shared-branch multi-repo integration",
				"done_when":      []string{"Both repos pass tests"},
			},
		},
	})
	if _, err := h.s.toolTaskPlan(context.Background(), "conv1", raw); err != nil {
		t.Fatalf("task_plan multi-repo shared_branch: %v", err)
	}
	waitFor(t, "container start finishes", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.s.taskStarting.Load() == 0 && h.startCalls == 1
	})

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.prepareCalls != 1 || h.resolveCalls == 0 {
		t.Fatalf("expected container resolve+prepare, got resolve=%d prepare=%d", h.resolveCalls, h.prepareCalls)
	}
	if h.lastPrepare.Plan.Source.Layout != repository.LayoutContainer || len(h.lastPrepare.Plan.Repositories) != 2 {
		t.Fatalf("expected 2-repo container prepare, got layout=%s repos=%d", h.lastPrepare.Plan.Source.Layout, len(h.lastPrepare.Plan.Repositories))
	}
}

func TestTaskPlanSharedBranchContainerIgnoresLinkOnlyRepositories(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bound []string
	}{
		{"one bound repo does not infer a container", []string{"repo_web"}},
		{"two bound repos infer a container without links", []string{"repo_web", "repo_api"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			st := newContainerWorkspaceMemStore()
			h := newWorkspaceTestHarness(t, st)
			seedRepoWorkflowBinding(t, h.s, "repo_web", "https://github.com/example/web", repository.TaskWorkflowSharedBranch)
			todo, err := h.s.createTaskTodoWithRepositories(ctx, "todo_shared_links", "Shared work", "Update the bound repositories", "conv1", "", "", "", tc.bound)
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := st.catalog(ctx)
			if err != nil {
				t.Fatal(err)
			}
			set, err := st.TodoRepositories(ctx, todo.ID)
			if err != nil {
				t.Fatal(err)
			}
			set.Repositories, err = repository.MergeTaskLinkDetails(set.Repositories, []repository.LinkInput{
				{URL: "https://github.com/example/unlisted/issues/7", Role: repository.LinkRoleReference},
			}, catalog, repository.LinkProvenanceUnbound)
			if err != nil || len(set.Repositories) != len(tc.bound)+1 {
				t.Fatalf("link import: rows=%+v err=%v", set.Repositories, err)
			}
			st.memRepoStore.mu.Lock()
			st.memRepoStore.sets[todo.ID] = set
			st.memRepoStore.mu.Unlock()

			task := PlanTask{TodoID: todo.ID}
			got := h.s.inferredSharedBranchContainerRepos(ctx, task)
			if len(tc.bound) == 1 {
				if len(got) != 0 || h.s.canInferContainerWorkspace(ctx, task) {
					t.Fatalf("a sibling link triggered container inference: %v", got)
				}
				if workflow := h.s.resolvedTaskWorkflow(ctx, task); workflow != repository.TaskWorkflowSharedBranch {
					t.Fatalf("a sibling link masked the bound repository workflow: %q", workflow)
				}
				return
			}
			if !slices.Equal(got, tc.bound) {
				t.Fatalf("inferred repository IDs = %v, want %v", got, tc.bound)
			}
		})
	}
}

func TestTaskPlanMultiRepoWithoutSharedBranchDoesNotInferContainer(t *testing.T) {
	h := newContainerTestHarness(t)
	todo, err := h.s.createTaskTodoWithRepositories(
		context.Background(),
		"todo_multi_default",
		"Default multi-repo",
		"No workflow preference",
		"conv1",
		"",
		"",
		"",
		[]string{"repo_web", "repo_api"},
	)
	if err != nil {
		t.Fatal(err)
	}

	// Without shared_branch, omitting cwd + workspace keeps the existing validation refusal.
	raw, _ := json.Marshal(map[string]any{
		"tasks": []any{
			map[string]any{
				"title":          "Default multi-repo",
				"prompt":         "Work on default repos",
				"role":           "worker",
				"machine":        "box",
				"todo_id":        todo.ID,
				"readonly":       false,
				"user_requested": true,
				"why":            "Default behavior check",
				"done_when":      []string{"Done"},
			},
		},
	})
	if _, err := h.s.toolTaskPlan(context.Background(), "conv1", raw); err == nil {
		t.Fatal("expected missing cwd error when repos do not opt into shared_branch")
	}
}

func TestTaskPlanSharedBranchSingleRepoViaTodoBinding(t *testing.T) {
	s, f := workflowPreferenceService(t)
	setSettings(t, s, func(v *Settings) { v.TaskWorkspacePreference = preferNew })
	seedRepoWorkflowBinding(t, s, "repo_app", "https://github.com/example/app", repository.TaskWorkflowSharedBranch)

	todo, err := s.createTaskTodoWithRepositories(
		context.Background(),
		"todo_single_shared",
		"Feature task",
		"Single bound repo",
		"conv1",
		"",
		"",
		"",
		[]string{"repo_app"},
	)
	if err != nil {
		t.Fatal(err)
	}

	// No git.status mock needed when the to-do already binds a single saved repository.
	_, p := planWorkspaceQueueTask(t, s, "conv1", map[string]any{
		"title":   "Feature task",
		"prompt":  "Work on feature branch",
		"todo_id": todo.ID,
	})
	waitWorkspaceStarts(t, s, f, 1)
	if p.Tasks[0].Workspace == nil || p.Tasks[0].Workspace.Mode != workspaceExisting || !p.Tasks[0].Workspace.Queue || !p.Tasks[0].WorkspaceInferred {
		t.Fatalf("expected inferred existing+queue via direct todo binding, got %+v", p.Tasks[0].Workspace)
	}
}
