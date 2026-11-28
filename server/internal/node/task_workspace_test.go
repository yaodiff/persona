package node

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	repository "github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func workspaceGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = taskworkspace.GitEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
func workspaceRemote(t *testing.T, branch string) string {
	t.Helper()
	dir := workspaceTemp(t)
	workspaceGit(t, dir, "init", "-b", branch)
	workspaceGit(t, dir, "config", "user.name", "Example")
	workspaceGit(t, dir, "config", "user.email", "me@example.com")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, dir, "add", ".")
	workspaceGit(t, dir, "commit", "-m", "Initial")
	return dir
}
func workspacePlan(t *testing.T, home string, layout repository.Layout, urls ...string) workspacenaming.Plan {
	t.Helper()
	rows := []repository.PreparationRepository{}
	for i, url := range urls {
		id := []string{"web", "api"}[i]
		identity, _ := repository.Parse("https://github.com/example/"+id, "")
		rows = append(rows, repository.PreparationRepository{RepoID: id, RequestedRef: repository.RequestedRef{Kind: "default"}, SourceSnapshot: repository.SourceSnapshot{Identity: identity, CloneURL: url}})
	}
	source := workspacenaming.Source{Manager: "example", TodoID: "todo_example", MachineID: "node_example", Title: "Example", PlannedAt: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC), Timezone: "UTC", Rules: workspacenaming.Defaults(), Layout: layout, Repositories: repository.PreparationSnapshot{SchemaVersion: 1, Manager: "example", TodoID: "todo_example", Repositories: rows}}
	preview := workspacenaming.Resolve(source, workspacenaming.NodeContext{Home: home, PersonaHome: filepath.Join(home, ".persona")})
	if preview.Plan == nil {
		t.Fatal(preview)
	}
	return *preview.Plan
}
func workspaceCall(t *testing.T, n *Node, method string, req taskworkspace.Request) taskworkspace.Record {
	t.Helper()
	raw, _ := json.Marshal(req)
	out, err := n.handle(t.Context(), "task.workspace."+method, raw)
	if err != nil {
		t.Fatal(err)
	}
	return out.(taskworkspace.Record)
}

func TestBackendWorkspaceWithoutRepositories(t *testing.T) {
	home := workspaceTemp(t)
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	req := taskworkspace.Request{WorkspaceID: "ws_empty", OperationID: "op_empty", UseID: "launch_example", Plan: workspacePlan(t, home, repository.LayoutContainer)}
	// Preparing, inspecting, and cleaning an empty workspace do not require Git.
	t.Setenv("PATH", workspaceTemp(t))
	root := filepath.Join(home, "workspace", "task-todo_example")
	ready := workspaceCall(t, n, "prepare", req)
	if ready.State != "ready" || ready.Error != nil || len(ready.Repositories) != 0 || ready.Plan.RootPath != root {
		t.Fatal(ready)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("workspace contents = %v, %v", entries, err)
	}
	// A replacement node reads the same ownership receipt and respects its use claims.
	n = &Node{home: home, dir: n.dir}
	if got := workspaceCall(t, n, "acquire", req); got.Error != nil || len(got.Uses) != 1 {
		t.Fatal(got)
	}
	if got := workspaceCall(t, n, "cleanup", req); got.Error == nil || got.Error.Code != "worktree_in_use" {
		t.Fatal(got)
	}
	workspaceCall(t, n, "release", req)
	userFile := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(userFile, []byte("Keep my notes."), 0600); err != nil {
		t.Fatal(err)
	}
	req.Force, req.DeleteBranch = true, true
	if got := workspaceCall(t, n, "cleanup", req); got.Error == nil || got.Error.Code != "cleanup_retained" {
		t.Fatal(got)
	}
	if err := n.guardBackendDeletion(root); err == nil {
		t.Fatal("generic deletion bypassed the ownership receipt")
	}
	if err := os.Remove(userFile); err != nil {
		t.Fatal(err)
	}
	if got := workspaceCall(t, n, "cleanup", req); got.State != "removed" || got.Error != nil || got.Cleanup == nil || !got.Cleanup.Removed || len(got.Cleanup.RetainedPaths) != 0 {
		t.Fatal(got)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("removed root: %v", err)
	}
	if got := workspaceCall(t, n, "prepare", req); got.State != "removed" {
		t.Fatal("late prepare recreated the directory", got)
	}
}

func TestBackendEmptyWorkspaceChecksRootBeforeUse(t *testing.T) {
	home := workspaceTemp(t)
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	req := taskworkspace.Request{WorkspaceID: "ws_empty", OperationID: "op_empty", UseID: "launch_example", Plan: workspacePlan(t, home, repository.LayoutContainer)}
	if got := workspaceCall(t, n, "prepare", req); got.State != "ready" {
		t.Fatal(got)
	}
	// Keep the old directory present so that the replacement cannot reuse its file ID.
	if err := os.Rename(req.Plan.RootPath, req.Plan.RootPath+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(req.Plan.RootPath, 0700); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"inspect", "acquire", "cleanup"} {
		if got := workspaceCall(t, n, method, req); got.Error == nil || got.Error.Code != "identity_mismatch" {
			t.Fatalf("%s accepted a replacement directory: %+v", method, got)
		}
	}
}

func TestBackendWorkspaceRealLayoutsAndRecovery(t *testing.T) {
	for _, layout := range []repository.Layout{repository.LayoutRoot, repository.LayoutContainer} {
		t.Run(string(layout), func(t *testing.T) {
			home := workspaceTemp(t)
			n := &Node{home: home, dir: filepath.Join(home, ".persona")}
			urls := []string{workspaceRemote(t, "trunk")}
			if layout == repository.LayoutContainer {
				urls = append(urls, workspaceRemote(t, "develop"))
			}
			req := taskworkspace.Request{WorkspaceID: "ws_example", OperationID: "op_example", Plan: workspacePlan(t, home, layout, urls...)}
			result := workspaceCall(t, n, "prepare", req)
			if result.State != "ready" || len(result.Repositories) != len(urls) {
				t.Fatal(result)
			}
			for _, repo := range result.Repositories {
				if repo.ResolvedCommit == "" || repo.State != "ready" || repo.CommonDir == repo.Path {
					t.Fatal(repo)
				}
				workspaceGit(t, repo.Path, "config", "user.name", "Example")
				workspaceGit(t, repo.Path, "config", "user.email", "me@example.com")
			}
			path := result.Repositories[0].Path
			os.WriteFile(filepath.Join(path, "local.txt"), []byte("keep"), 0600)
			workspaceGit(t, path, "add", "local.txt")
			workspaceGit(t, path, "commit", "-m", "Local work")
			head := workspaceGit(t, path, "rev-parse", "HEAD")
			os.WriteFile(filepath.Join(path, "dirty.txt"), []byte("keep"), 0600)
			remote := result.Repositories[0].Input.SourceSnapshot.CloneURL
			os.WriteFile(filepath.Join(remote, "remote-update.txt"), []byte("new upstream"), 0600)
			workspaceGit(t, remote, "add", ".")
			workspaceGit(t, remote, "commit", "-m", "Advance remote")
			// A replacement Node has no in-memory receipts. Neither retry nor inspect resets local work.
			n = &Node{home: home, dir: filepath.Join(home, ".persona")}
			again := workspaceCall(t, n, "prepare", req)
			if again.State != "ready" || again.Repositories[0].ResolvedCommit != result.Repositories[0].ResolvedCommit || workspaceGit(t, path, "rev-parse", "HEAD") != head {
				t.Fatal(again)
			}
			if _, err := os.Stat(filepath.Join(path, "dirty.txt")); err != nil {
				t.Fatal(err)
			}
			cleanup := workspaceCall(t, n, "cleanup", req)
			if cleanup.Error == nil || cleanup.Error.Code != "worktree_dirty" {
				t.Fatal(cleanup)
			}
			req.Force = true
			cleanup = workspaceCall(t, n, "cleanup", req)
			if cleanup.Cleanup == nil || !cleanup.Cleanup.Removed {
				t.Fatal(cleanup)
			}
			for i, repo := range cleanup.Repositories {
				if i == 0 && workspaceGit(t, repo.CommonDir, "rev-parse", "refs/heads/"+repo.TaskBranch) != head {
					t.Fatal("retained branch lost its commits")
				}
				if _, err := os.Stat(repo.CommonDir); err != nil {
					t.Fatal("retained branch storage missing", err)
				}
			}
			n = &Node{home: home, dir: filepath.Join(home, ".persona")}
			late := workspaceCall(t, n, "prepare", req)
			if late.State != "removed" {
				t.Fatal(late)
			}
		})
	}
}
func TestBackendWorkspaceConcurrentAndCorrectedInput(t *testing.T) {
	home := workspaceTemp(t)
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	urls := []string{workspaceRemote(t, "trunk"), workspaceRemote(t, "develop")}
	plan := workspacePlan(t, home, repository.LayoutContainer, urls...)
	plan.Source.Repositories.Repositories[1].RequestedRef = repository.RequestedRef{Kind: "branch", Value: "missing"}
	// Resolve after editing the fixed T1 input, rather than altering a frozen naming plan.
	preview := workspacenaming.Resolve(plan.Source, plan.Node)
	plan = *preview.Plan
	req := taskworkspace.Request{WorkspaceID: "ws_example", OperationID: "op_bad", Plan: plan}
	first := workspaceCall(t, n, "prepare", req)
	if first.State != "failed" || first.Repositories[0].State != "ready" || first.Error == nil {
		t.Fatal(first)
	}
	path := first.Repositories[0].Path
	os.WriteFile(filepath.Join(path, "keep.txt"), []byte("keep"), 0600)
	workspaceCall(t, n, "cancel", req)
	req.PreviousOperationID = req.OperationID
	req.OperationID = "op_corrected"
	req.Plan = workspacePlan(t, home, repository.LayoutContainer, urls...)
	var group sync.WaitGroup
	results := make(chan taskworkspace.Record, 2)
	for range 2 {
		group.Go(func() { results <- workspaceCall(t, n, "prepare", req) })
	}
	group.Wait()
	close(results)
	ready := false
	for result := range results {
		if result.State == "ready" {
			ready = true
		} else if result.Error == nil || result.Error.Code != "operation_busy" {
			t.Fatal(result)
		}
	}
	if !ready {
		t.Fatal("no completed preparation")
	}
	if _, err := os.Stat(filepath.Join(path, "keep.txt")); err != nil {
		t.Fatal(err)
	}
	old := req
	old.OperationID = "op_bad"
	old.PreviousOperationID = ""
	if result := workspaceCall(t, n, "prepare", old); result.Error == nil {
		t.Fatal("old operation revived", result)
	}
}

func TestBackendWorkspacePathOwnershipAndCleanupSemantics(t *testing.T) {
	t.Run("foreign directory", func(t *testing.T) {
		home := workspaceTemp(t)
		n := &Node{home: home, dir: filepath.Join(home, ".persona")}
		req := taskworkspace.Request{WorkspaceID: "ws_foreign", OperationID: "op_foreign", Plan: workspacePlan(t, home, repository.LayoutRoot, workspaceRemote(t, "trunk"))}
		if err := os.MkdirAll(req.Plan.RootPath, 0700); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(req.Plan.RootPath, "keep.txt")
		os.WriteFile(marker, []byte("external"), 0600)
		r := workspaceCall(t, n, "prepare", req)
		if r.Error == nil || r.Error.Code != "path_conflict" {
			t.Fatal(r)
		}
		if data, err := os.ReadFile(marker); err != nil || string(data) != "external" {
			t.Fatal(string(data), err)
		}
	})
	t.Run("symlink parent", func(t *testing.T) {
		home := workspaceTemp(t)
		external := workspaceTemp(t)
		n := &Node{home: home, dir: filepath.Join(home, ".persona")}
		req := taskworkspace.Request{WorkspaceID: "ws_link", OperationID: "op_link", Plan: workspacePlan(t, home, repository.LayoutRoot, workspaceRemote(t, "trunk"))}
		if err := os.Symlink(external, filepath.Join(home, "workspace")); err != nil {
			t.Skip(err)
		}
		r := workspaceCall(t, n, "prepare", req)
		if r.Error == nil || r.Error.Code != "path_symlink" {
			t.Fatal(r)
		}
		entries, err := os.ReadDir(external)
		if err != nil || len(entries) != 0 {
			t.Fatal(entries, err)
		}
	})
	t.Run("branch deletion and use", func(t *testing.T) {
		home := workspaceTemp(t)
		n := &Node{home: home, dir: filepath.Join(home, ".persona")}
		req := taskworkspace.Request{WorkspaceID: "ws_use", OperationID: "op_use", Plan: workspacePlan(t, home, repository.LayoutRoot, workspaceRemote(t, "trunk"))}
		r := workspaceCall(t, n, "prepare", req)
		if r.State != "ready" {
			t.Fatal(r)
		}
		path := r.Repositories[0].Path
		common := r.Repositories[0].CommonDir
		branch := r.Repositories[0].TaskBranch
		req.UseID = "session_example"
		if r = workspaceCall(t, n, "acquire", req); len(r.Uses) != 1 {
			t.Fatal(r)
		}
		if r = workspaceCall(t, n, "cleanup", req); r.Error == nil || r.Error.Code != "worktree_in_use" {
			t.Fatal(r)
		}
		workspaceCall(t, n, "release", req)
		workspaceGit(t, path, "config", "user.name", "Example")
		workspaceGit(t, path, "config", "user.email", "me@example.com")
		os.WriteFile(filepath.Join(path, "work.txt"), []byte("work"), 0600)
		workspaceGit(t, path, "add", ".")
		workspaceGit(t, path, "commit", "-m", "Work")
		req.DeleteBranch = true
		if r = workspaceCall(t, n, "cleanup", req); r.Error == nil || r.Error.Code != "worktree_unmerged" {
			t.Fatal(r)
		}
		req.Force = true
		r = workspaceCall(t, n, "cleanup", req)
		if r.Cleanup == nil || !r.Cleanup.Removed || !r.Repositories[0].BranchDeleted {
			t.Fatal(r)
		}
		if _, err := workspaceReadGit(t.Context(), common, "show-ref", "--verify", "refs/heads/"+branch); err == nil {
			t.Fatal("branch remains")
		}
		// The old tombstone remains, while a new operation can prepare the same Todo/root.
		req.WorkspaceID = "ws_after_cleanup"
		req.OperationID = "op_after_cleanup"
		r = workspaceCall(t, n, "prepare", req)
		if r.State != "ready" {
			t.Fatal(r)
		}
	})
}

func TestBackendWorkspaceTypedRefsAndFrozenSource(t *testing.T) {
	remote := workspaceRemote(t, "unusual-default")
	workspaceGit(t, remote, "tag", "v1")
	commit := strings.TrimSpace(workspaceGit(t, remote, "rev-parse", "HEAD"))
	workspaceGit(t, remote, "update-ref", "refs/changes/example", commit)
	for _, ref := range []repository.RequestedRef{{Kind: "branch", Value: "unusual-default"}, {Kind: "tag", Value: "v1"}, {Kind: "commit", Value: commit}, {Kind: "ref", Value: "refs/changes/example"}} {
		t.Run(ref.Kind, func(t *testing.T) {
			home := workspaceTemp(t)
			n := &Node{home: home, dir: filepath.Join(home, ".persona")}
			p := workspacePlan(t, home, repository.LayoutRoot, remote)
			p.Source.Repositories.Repositories[0].RequestedRef = ref
			p = *workspacenaming.Resolve(p.Source, p.Node).Plan
			req := taskworkspace.Request{WorkspaceID: "ws_ref", OperationID: "op_ref", Plan: p}
			r := workspaceCall(t, n, "prepare", req)
			if r.State != "ready" || r.Repositories[0].ResolvedCommit != commit {
				t.Fatal(r)
			}
			journalPath := filepath.Join(n.dir, "task-workspaces", taskworkspace.Hash(req.WorkspaceID), "receipt.json")
			before, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			inspect := workspaceCall(t, n, "inspect", req)
			after, err := os.ReadFile(journalPath)
			if err != nil || inspect.State != "ready" || string(before) != string(after) {
				t.Fatal("inspect changed receipt", inspect, err)
			}
			if err := n.offLimits(journalPath); err == nil {
				t.Fatal("file API can edit ownership receipt")
			}
		})
	}
}

func TestBackendWorkspaceRejectsCredentialsBeforeReceipt(t *testing.T) {
	home := workspaceTemp(t)
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	p := workspacePlan(t, home, repository.LayoutRoot, workspaceRemote(t, "trunk"))
	identity, _ := repository.Parse("https://code.example.com/example/web", "gitlab")
	p.Source.Repositories.Repositories[0].SourceSnapshot.Identity = identity
	p.Source.Repositories.Repositories[0].SourceSnapshot.CloneURL = "https://example:secret-value@code.example.com/example/web.git"
	p = *workspacenaming.Resolve(p.Source, p.Node).Plan
	req := taskworkspace.Request{WorkspaceID: "ws_secret", OperationID: "op_secret", Plan: p}
	r := workspaceCall(t, n, "prepare", req)
	if r.Error == nil || r.Error.Code != "source_invalid" {
		t.Fatal(r)
	}
	if _, err := os.Stat(n.dir); !os.IsNotExist(err) {
		t.Fatal("invalid transport persisted before validation", err)
	}
}

func TestBackendWorkspaceResumesBranchCreatedBeforeCheckout(t *testing.T) {
	home := workspaceTemp(t)
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	req := taskworkspace.Request{WorkspaceID: "ws_checkout", OperationID: "op_checkout", Plan: workspacePlan(t, home, repository.LayoutRoot, workspaceRemote(t, "trunk"))}
	r := workspaceCall(t, n, "prepare", req)
	if r.State != "ready" {
		t.Fatal(r)
	}
	instance := r.Repositories[0]
	// Reconstruct a real Git failure boundary: the owned branch exists, while
	// checkout was rolled back before a successful materialization receipt.
	workspaceGit(t, instance.CommonDir, "worktree", "remove", "--", instance.Path)
	if err := os.Mkdir(instance.Path, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(n.dir, "task-workspaces", taskworkspace.Hash(req.WorkspaceID), "receipt.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var journal workspaceJournal
	if err = json.Unmarshal(raw, &journal); err != nil {
		t.Fatal(err)
	}
	journal.State = "preparing"
	journal.Repositories[0].State = "materializing"
	journal.Identities[instance.Path], err = workspaceFileID(instance.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err = workspaceWrite(path, &journal); err != nil {
		t.Fatal(err)
	}
	n = &Node{home: home, dir: filepath.Join(home, ".persona")}
	r = workspaceCall(t, n, "prepare", req)
	if r.State != "ready" || r.Repositories[0].ResolvedCommit != instance.ResolvedCommit {
		t.Fatal(r)
	}
}

func workspaceTemp(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBackendWorkspaceSecondSourceTransientFailureKeepsSameOperation(t *testing.T) {
	home := workspaceTemp(t)
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	p := workspacePlan(t, home, repository.LayoutContainer, workspaceRemote(t, "trunk"), workspaceRemote(t, "develop"))
	source := p.Source.Repositories.Repositories[1].SourceSnapshot.CloneURL
	offline := source + "-offline"
	if err := os.Rename(source, offline); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(offline); err == nil {
			os.Rename(offline, source)
		}
	})
	req := taskworkspace.Request{WorkspaceID: "ws_transient", OperationID: "op_transient", Plan: p}
	first := workspaceCall(t, n, "prepare", req)
	if first.State != "failed" || first.Error == nil || first.Repositories[0].State != "ready" {
		t.Fatal(first)
	}
	kept := filepath.Join(first.Repositories[0].Path, "keep.txt")
	os.WriteFile(kept, []byte("keep"), 0600)
	if err := os.Rename(offline, source); err != nil {
		t.Fatal(err)
	}
	n = &Node{home: home, dir: filepath.Join(home, ".persona")}
	recovered := workspaceCall(t, n, "prepare", req)
	if recovered.State != "ready" || recovered.OperationID != first.OperationID || recovered.Repositories[0].ResolvedCommit != first.Repositories[0].ResolvedCommit {
		t.Fatal(recovered)
	}
	if data, err := os.ReadFile(kept); err != nil || string(data) != "keep" {
		t.Fatal(string(data), err)
	}
}

func TestBackendWorkspaceRepeatedCorrectionsKeepPhysicalOperationIdentity(t *testing.T) {
	home := workspaceTemp(t)
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	req := taskworkspace.Request{WorkspaceID: "ws_correction", OperationID: "op_first", Plan: workspacePlan(t, home, repository.LayoutRoot, workspaceRemote(t, "trunk"))}
	first := workspaceCall(t, n, "prepare", req)
	if first.State != "ready" {
		t.Fatal(first)
	}
	workspaceCall(t, n, "cancel", req)
	incompatible := req
	incompatible.OperationID = "op_incompatible"
	incompatible.PreviousOperationID = req.OperationID
	source := req.Plan.Source
	source.Rules.BranchTemplate = "different/{task-id}"
	incompatible.Plan = *workspacenaming.Resolve(source, req.Plan.Node).Plan
	refused := workspaceCall(t, n, "prepare", incompatible)
	if refused.Error == nil || refused.Error.Code != "resource_conflict" || refused.OperationID != incompatible.OperationID || refused.NodeOperationID != req.OperationID {
		t.Fatal(refused)
	}
	corrected := req
	corrected.OperationID = "op_corrected"
	corrected.PreviousOperationID = refused.NodeOperationID
	r := workspaceCall(t, n, "prepare", corrected)
	if r.State != "ready" || r.NodeOperationID != corrected.OperationID || r.Repositories[0].ResolvedCommit != first.Repositories[0].ResolvedCommit {
		t.Fatal(r)
	}
}

func TestBackendWorkspaceIgnoresInheritedRepositoryLocators(t *testing.T) {
	external := workspaceRemote(t, "existing")
	head := workspaceGit(t, external, "rev-parse", "HEAD")
	home := workspaceTemp(t)
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	req := taskworkspace.Request{WorkspaceID: "ws_environment", OperationID: "op_environment", Plan: workspacePlan(t, home, repository.LayoutRoot, workspaceRemote(t, "trunk"))}
	t.Setenv("GIT_DIR", filepath.Join(external, ".git"))
	t.Setenv("GIT_WORK_TREE", external)
	r := workspaceCall(t, n, "prepare", req)
	if r.State != "ready" {
		t.Fatal(r)
	}
	if workspaceGit(t, external, "rev-parse", "HEAD") != head || strings.TrimSpace(workspaceGit(t, external, "status", "--porcelain")) != "" {
		t.Fatal("preparation altered the existing checkout")
	}
}

func TestBackendWorkspaceCorrectsUnresolvedDefaultToExplicitBranch(t *testing.T) {
	remote := workspaceRemote(t, "trunk")
	workspaceGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/missing")
	home := workspaceTemp(t)
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	req := taskworkspace.Request{WorkspaceID: "ws_head", OperationID: "op_default", Plan: workspacePlan(t, home, repository.LayoutRoot, remote)}
	failed := workspaceCall(t, n, "prepare", req)
	if failed.Error == nil || failed.Error.Code != "default_branch_unresolved" {
		t.Fatal(failed)
	}
	workspaceCall(t, n, "cancel", req)
	req.PreviousOperationID = req.OperationID
	req.OperationID = "op_explicit"
	req.Plan.Source.Repositories.Repositories[0].RequestedRef = repository.RequestedRef{Kind: "branch", Value: "trunk"}
	req.Plan = *workspacenaming.Resolve(req.Plan.Source, req.Plan.Node).Plan
	ready := workspaceCall(t, n, "prepare", req)
	if ready.State != "ready" || ready.Repositories[0].ResolvedRef != "refs/heads/trunk" {
		t.Fatal(ready)
	}
}
