package repositorystore

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func backendPlan(t *testing.T, d *Store, op string, change func(*workspacenaming.Source)) workspacenaming.Plan {
	t.Helper()
	ctx := t.Context()
	set, err := d.TodoRepositories(ctx, "backend")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := repository.Snapshot("assistant", "backend", set.Revision, set.Repositories, []string{"web"})
	if err != nil {
		t.Fatal(err)
	}
	source := workspacenaming.Source{Manager: "assistant", TodoID: "backend", Title: "Example", MachineID: "computer", PlannedAt: time.Now().UTC(), Timezone: "UTC", Rules: workspacenaming.Defaults(), Layout: repository.LayoutRoot, Repositories: snapshot}
	if change != nil {
		change(&source)
	}
	preview := workspacenaming.Resolve(source, workspacenaming.NodeContext{Home: "/home/example"})
	if preview.Plan == nil {
		t.Fatal(preview)
	}
	if _, err = d.SaveWorkspaceNamingPlan(ctx, "backend", op, 0, *preview.Plan); err != nil {
		t.Fatal(err)
	}
	return *preview.Plan
}
func backendDB(t *testing.T) *Store {
	t.Helper()
	d := open(t)
	ctx := t.Context()
	one := int64(1)
	identity, _ := repository.Parse("https://github.com/example/web", "")
	if err := d.PutSetting(ctx, repository.CatalogKey, repository.Catalog{Revision: 1, Repositories: []repository.CatalogEntry{{ID: "web", Identity: identity}}}); err != nil {
		t.Fatal(err)
	}
	if err := d.CreateTodoWithRepositories(ctx, db.TodoRow{ID: "backend", Title: "Example", CreatedAt: time.Now(), UpdatedAt: time.Now()}, repository.CreateBindings{CatalogRevision: &one, Repositories: []repository.TaskInput{{RepoID: "web"}}}); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestBackendWorkspacePersistenceWithoutRepositories(t *testing.T) {
	d := backendDB(t)
	ctx := t.Context()
	const op = "operation_empty"
	plan := backendPlan(t, d, op, func(source *workspacenaming.Source) {
		source.Layout = repository.LayoutContainer
		source.Repositories, _ = repository.Snapshot(source.Manager, source.TodoID, source.Repositories.Revision, nil, nil)
		source.Rules.BranchTemplate = "persona/{base-branch}/{task-id}"
	})
	r, err := d.BeginTaskWorkspace(ctx, "backend", op, "assistant")
	if err != nil || r.Plan.Digest != plan.Digest || len(r.Plan.Repositories) != 0 {
		t.Fatal(r, err)
	}
	result := *r
	result.State = "ready"
	ready, err := d.SaveTaskWorkspaceResult(ctx, "backend", op, r.Revision, result)
	if err != nil || ready.State != "ready" || len(ready.Repositories) != 0 {
		t.Fatal(ready, err)
	}
	if _, err := d.MutateTaskWorkspace(ctx, "backend", op, ready.Revision, "acquire", "launch_example"); err != nil {
		t.Fatal(err)
	}
}

func TestBackendWorkspacePersistenceConcurrencyAndLateResults(t *testing.T) {
	d := backendDB(t)
	ctx := t.Context()
	plan := backendPlan(t, d, "operation_one", nil)
	var group sync.WaitGroup
	results := make(chan *taskworkspace.Record, 2)
	errs := make(chan error, 2)
	for range 2 {
		group.Go(func() {
			r, err := d.BeginTaskWorkspace(ctx, "backend", "operation_one", "assistant")
			results <- r
			errs <- err
		})
	}
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first *taskworkspace.Record
	for result := range results {
		if first != nil && first.WorkspaceID != result.WorkspaceID {
			t.Fatal("duplicate workspace")
		}
		first = result
	}
	if first.Plan.Digest != plan.Digest {
		t.Fatal(first)
	}
	if _, err := d.MutateTaskWorkspace(ctx, "backend", first.OperationID, 0, "cancel", ""); err != nil {
		t.Fatal(err)
	}
	result := *first
	result.State = "ready"
	saved, err := d.SaveTaskWorkspaceResult(ctx, "backend", first.OperationID, first.Revision, result)
	if err != nil || saved.State != "cancelled" {
		t.Fatal("late response revived canceled input", saved, err)
	}
	nextPlan := backendPlan(t, d, "operation_two", func(s *workspacenaming.Source) { s.Rules.BranchTemplate = "corrected/{task-id}" })
	next, err := d.BeginTaskWorkspace(ctx, "backend", "operation_two", "assistant")
	if err != nil || next.WorkspaceID != first.WorkspaceID || next.PreviousOperationID != first.OperationID || next.Plan.Digest != nextPlan.Digest {
		t.Fatal(next, err)
	}
	// Old clients and editable associations cannot clear an operation's snapshot.
	one := int64(1)
	if _, err = d.SetTodoRepositories(ctx, "backend", repository.SetInput{ExpectedRevision: &one, ExpectedCatalogRevision: &one, Repositories: []repository.TaskInput{}}); err != nil {
		t.Fatal(err)
	}
	title := "Changed"
	if _, err = d.EditTodo(ctx, "backend", db.TodoEdit{Title: &title}, time.Now()); err != nil {
		t.Fatal(err)
	}
	again, err := d.BeginTaskWorkspace(ctx, "backend", "operation_two", "assistant")
	if err != nil || again.Plan.Digest != next.Plan.Digest || len(again.Plan.Repositories) != 1 {
		t.Fatal(again, err)
	}
	var raw []byte
	if err = d.QueryRow(ctx, `SELECT record FROM task_workspace_operations WHERE operation_id=$1`, next.OperationID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var persisted taskworkspace.Record
	if json.Unmarshal(raw, &persisted) != nil || persisted.Plan.Digest != next.Plan.Digest {
		t.Fatal(string(raw))
	}
}
func TestBackendWorkspaceUseCleanupCompletionAndReprepare(t *testing.T) {
	d := backendDB(t)
	ctx := t.Context()
	backendPlan(t, d, "operation_one", nil)
	r, err := d.BeginTaskWorkspace(ctx, "backend", "operation_one", "assistant")
	if err != nil {
		t.Fatal(err)
	}
	ready := *r
	ready.State = "ready"
	if _, err = d.SaveTaskWorkspaceResult(ctx, "backend", r.OperationID, r.Revision, ready); err == nil {
		t.Fatal("incomplete repository result became Ready")
	}
	for i, p := range ready.Plan.Repositories {
		ready.Repositories = append(ready.Repositories, taskworkspace.Instance{RepoID: p.RepoID, Input: ready.Plan.Source.Repositories.Repositories[i], Path: p.AbsolutePath, RelativePath: p.RelativePath, TaskBranch: p.TaskBranch, CommonDir: "/home/example/storage.git", ResolvedCommit: strings.Repeat("a", 40), State: "ready"})
	}
	r, err = d.SaveTaskWorkspaceResult(ctx, "backend", r.OperationID, r.Revision, ready)
	if err != nil {
		t.Fatal(err)
	}
	r, err = d.MutateTaskWorkspace(ctx, "backend", r.OperationID, r.Revision, "acquire", "session_example")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.MutateTaskWorkspace(ctx, "backend", r.OperationID, 0, "cleanup", ""); err == nil {
		t.Fatal("cleanup admitted during use")
	}
	done := true
	if _, err = d.EditTodo(ctx, "backend", db.TodoEdit{Done: &done}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = d.MutateTaskWorkspace(ctx, "backend", r.OperationID, 0, "check", "session_example"); err == nil {
		t.Fatal("late start after completion")
	}
	done = false
	if _, err = d.EditTodo(ctx, "backend", db.TodoEdit{Done: &done}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err = d.MutateTaskWorkspace(ctx, "backend", r.OperationID, 0, "release", "session_example"); err != nil {
		t.Fatal(err)
	}
	r, err = d.MutateTaskWorkspace(ctx, "backend", r.OperationID, 0, "cleanup", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.MutateTaskWorkspace(ctx, "backend", r.OperationID, 0, "acquire", "session_late"); err == nil {
		t.Fatal("start admitted during cleanup")
	}
	removed := *r
	removed.State = "removed"
	removed.Cleanup = &taskworkspace.CleanupResult{Removed: true, RetainedPaths: []string{"/home/example/retained.git"}}
	r, err = d.SaveTaskWorkspaceResult(ctx, "backend", r.OperationID, r.Revision, removed)
	if err != nil {
		t.Fatal(err)
	}
	old, err := d.BeginTaskWorkspace(ctx, "backend", "operation_one", "assistant")
	if err != nil || old.State != "removed" {
		t.Fatal(old, err)
	}
	backendPlan(t, d, "operation_new", nil)
	next, err := d.BeginTaskWorkspace(ctx, "backend", "operation_new", "assistant")
	if err != nil || next.WorkspaceID == r.WorkspaceID {
		t.Fatal(next, err)
	}
	if _, err = d.BeginTaskWorkspace(ctx, "backend", "operation_new", "foreign"); err == nil {
		t.Fatal("foreign manager adopted input")
	}
	done = true
	if _, err = d.EditTodo(ctx, "backend", db.TodoEdit{Done: &done}, time.Now()); err != nil {
		t.Fatal(err)
	}
	backendPlan(t, d, "operation_late", nil)
	if _, err = d.BeginTaskWorkspace(ctx, "backend", "operation_late", "assistant"); !errors.Is(err, db.ErrTodoCompleted) {
		t.Fatal(err)
	}
}

func TestBackendWorkspaceFailedCorrectionKeepsNodeOwner(t *testing.T) {
	d := backendDB(t)
	ctx := t.Context()
	backendPlan(t, d, "op_first", nil)
	first, err := d.BeginTaskWorkspace(ctx, "backend", "op_first", "assistant")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.MutateTaskWorkspace(ctx, "backend", first.OperationID, 0, "cancel", ""); err != nil {
		t.Fatal(err)
	}
	backendPlan(t, d, "op_second", func(s *workspacenaming.Source) { s.Rules.BranchTemplate = "different/{task-id}" })
	next, err := d.BeginTaskWorkspace(ctx, "backend", "op_second", "assistant")
	if err != nil {
		t.Fatal(err)
	}
	rejected := *next
	rejected.NodeOperationID = first.OperationID
	rejected.State = "failed"
	rejected.Error = taskworkspace.Failure("resource_conflict", "prepare", next.OperationID, "", first.Plan.RootPath)
	next, err = d.SaveTaskWorkspaceResult(ctx, "backend", next.OperationID, next.Revision, rejected)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.MutateTaskWorkspace(ctx, "backend", next.OperationID, 0, "cancel", ""); err != nil {
		t.Fatal(err)
	}
	backendPlan(t, d, "op_corrected", nil)
	corrected, err := d.BeginTaskWorkspace(ctx, "backend", "op_corrected", "assistant")
	if err != nil || corrected.PreviousOperationID != first.OperationID {
		t.Fatal("correction targeted an operation never admitted by Node", corrected, err)
	}
}
