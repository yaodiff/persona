package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	repository "github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func repoMirrorCall[R any](t *testing.T, n *Node, method string, req any) R {
	t.Helper()
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	out, err := n.handle(t.Context(), method, raw)
	if err != nil {
		t.Fatalf("%s failed: %v", method, err)
	}
	return out.(R)
}

func testRepoIdentity(t *testing.T, name string) repository.Identity {
	t.Helper()
	id, err := repository.Parse("https://github.com/example/"+name, "")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRepoMirrorEnsureTTLPruneStatusAndRemove(t *testing.T) {
	home := workspaceTemp(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	n := &Node{
		home: home,
		dir:  filepath.Join(home, ".persona"),
		repoMirrors: repoMirrorManager{
			now: func() time.Time {
				mu.Lock()
				defer mu.Unlock()
				return now
			},
		},
	}

	// Create a local remote repo plus an unrelated user checkout under ~/workspace/myrepo.
	remote := workspaceRemote(t, "main")
	userCheckout := filepath.Join(home, "workspace", "myrepo")
	if err := os.MkdirAll(userCheckout, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userCheckout, "untracked.txt"), []byte("user-owned"), 0600); err != nil {
		t.Fatal(err)
	}
	initialHead := strings.TrimSpace(workspaceGit(t, remote, "rev-parse", "HEAD"))
	workspaceGit(t, remote, "checkout", "-b", "temp-branch")
	if err := os.WriteFile(filepath.Join(remote, "temp.txt"), []byte("temp"), 0600); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, remote, "add", ".")
	workspaceGit(t, remote, "commit", "-m", "Temp branch")
	workspaceGit(t, remote, "checkout", "main")

	var fetchCalls atomic.Int32
	n.repoMirrors.beforeFetch = func(_ context.Context, _ string) error {
		fetchCalls.Add(1)
		return nil
	}

	req := repository.MirrorEnsureRequest{
		RepoID:     "repo_web",
		Identity:   testRepoIdentity(t, "web"),
		CloneURL:   remote,
		TTLSeconds: 300,
	}

	// 1. Initial ensure initializes bare mirror strictly under ~/.persona/repos/repo_web.git.
	first := repoMirrorCall[repository.MirrorInfo](t, n, repository.MethodRepoMirrorEnsure, req)
	wantPath := filepath.Join(n.dir, "repos", "repo_web.git")
	if !first.Exists || !first.Fetched || first.Path != wantPath || first.DefaultRef != "refs/heads/main" || first.HeadCommit != initialHead || first.DiskBytes <= 0 {
		t.Fatalf("unexpected initial ensure: %+v", first)
	}
	if fetchCalls.Load() != 1 {
		t.Fatalf("expected 1 fetch call, got %d", fetchCalls.Load())
	}
	if strings.TrimSpace(workspaceGit(t, wantPath, "rev-parse", "--verify", "refs/heads/temp-branch")) == "" {
		t.Fatal("mirror did not fetch temp-branch")
	}

	// Verify ~/.persona/repos/ is protected from remote file-browsing APIs.
	if err := n.offLimits(wantPath); err == nil {
		t.Fatal("expected ~/.persona/repos mirror path to be off-limits")
	}

	// 2. Advance remote and delete temp-branch; ensure within TTL skips fetch.
	workspaceGit(t, remote, "branch", "-D", "temp-branch")
	if err := os.WriteFile(filepath.Join(remote, "file.txt"), []byte("v2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, remote, "add", ".")
	workspaceGit(t, remote, "commit", "-m", "Second commit")
	secondHead := strings.TrimSpace(workspaceGit(t, remote, "rev-parse", "HEAD"))

	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	fresh := repoMirrorCall[repository.MirrorInfo](t, n, repository.MethodRepoMirrorEnsure, req)
	if fresh.Fetched || fresh.SkippedReason != "ttl_fresh" || fresh.HeadCommit != initialHead {
		t.Fatalf("expected ttl_fresh hit, got %+v", fresh)
	}
	if fetchCalls.Load() != 1 {
		t.Fatalf("expected still 1 fetch call within TTL, got %d", fetchCalls.Load())
	}

	// 3. Advance clock past TTL -> runs git fetch --prune, sees secondHead, prunes temp-branch.
	mu.Lock()
	now = now.Add(4 * time.Minute)
	mu.Unlock()
	refreshed := repoMirrorCall[repository.MirrorInfo](t, n, repository.MethodRepoMirrorEnsure, req)
	if !refreshed.Fetched || refreshed.HeadCommit != secondHead || refreshed.SkippedReason != "" {
		t.Fatalf("expected refreshed mirror, got %+v", refreshed)
	}
	if fetchCalls.Load() != 2 {
		t.Fatalf("expected 2 fetch calls after TTL expiry, got %d", fetchCalls.Load())
	}
	if _, err := workspaceReadGit(t.Context(), wantPath, "rev-parse", "--verify", "refs/heads/temp-branch"); err == nil {
		t.Fatal("expected git fetch --prune to delete temp-branch in mirror")
	}

	// 4. Force bypasses TTL even at same timestamp.
	forceReq := req
	forceReq.Force = true
	forced := repoMirrorCall[repository.MirrorInfo](t, n, repository.MethodRepoMirrorEnsure, forceReq)
	if !forced.Fetched || fetchCalls.Load() != 3 {
		t.Fatalf("expected forced fetch (calls=%d): %+v", fetchCalls.Load(), forced)
	}

	// 5. Status reports single mirror and aggregate disk usage.
	single := repoMirrorCall[repository.MirrorStatusResponse](t, n, repository.MethodRepoMirrorStatus, repository.MirrorStatusRequest{RepoID: "repo_web"})
	if single.Mirror == nil || !single.Mirror.Exists || single.Mirror.HeadCommit != secondHead || single.TotalDiskBytes <= 0 {
		t.Fatalf("unexpected single status: %+v", single)
	}
	all := repoMirrorCall[repository.MirrorStatusResponse](t, n, repository.MethodRepoMirrorStatus, repository.MirrorStatusRequest{})
	if len(all.Mirrors) != 1 || all.Mirrors[0].RepoID != "repo_web" || all.TotalDiskBytes != single.TotalDiskBytes {
		t.Fatalf("unexpected list status: %+v", all)
	}

	// 6. Remove cleans up bare mirror and metadata safely without touching user workspace.
	removed := repoMirrorCall[repository.MirrorRemoveResponse](t, n, repository.MethodRepoMirrorRemove, repository.MirrorRemoveRequest{RepoID: "repo_web"})
	if !removed.Removed || removed.FreedBytes <= 0 {
		t.Fatalf("unexpected remove response: %+v", removed)
	}
	if _, err := os.Stat(wantPath); !os.IsNotExist(err) {
		t.Fatalf("mirror directory still exists: %v", err)
	}
	// Idempotent second remove.
	removedAgain := repoMirrorCall[repository.MirrorRemoveResponse](t, n, repository.MethodRepoMirrorRemove, repository.MirrorRemoveRequest{RepoID: "repo_web"})
	if removedAgain.Removed || removedAgain.FreedBytes != 0 {
		t.Fatalf("expected idempotent remove, got %+v", removedAgain)
	}
	// User workspace untouched.
	data, err := os.ReadFile(filepath.Join(userCheckout, "untracked.txt"))
	if err != nil || string(data) != "user-owned" {
		t.Fatalf("user checkout modified: %q %v", data, err)
	}
}

func TestConcurrentBackendWorkspacePreparesShareOneColdMirrorFetch(t *testing.T) {
	home := workspaceTemp(t)
	remote := workspaceRemote(t, "main")
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	entered := make(chan struct{})
	release := make(chan struct{})
	var fetchCount atomic.Int32

	n := &Node{
		home: home,
		dir:  filepath.Join(home, ".persona"),
		repoMirrors: repoMirrorManager{
			beforeFetch: func(ctx context.Context, _ string) error {
				if fetchCount.Add(1) == 1 {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				return nil
			},
		},
	}

	req := repository.MirrorEnsureRequest{
		RepoID:   "web",
		Identity: testRepoIdentity(t, "web"),
		CloneURL: remote,
	}

	var results [2]repository.MirrorInfo
	var errs [2]error
	wg.Go(func() { results[0], errs[0] = n.ensureRepoMirror(ctx, req) })

	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("first workspace never entered the mirror fetch")
	}
	wg.Go(func() { results[1], errs[1] = n.ensureRepoMirror(ctx, req) })

	// Hold the cold fetch until both preparations have joined it.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		n.repoMirrors.mu.Lock()
		f := n.repoMirrors.flights[req.RepoID]
		joined := f != nil && f.waiters == 2
		n.repoMirrors.mu.Unlock()
		if joined {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("second workspace never joined the in-flight mirror fetch")
		}
	}
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d failed: %v", i, err)
		}
	}
	if fetchCount.Load() != 1 {
		t.Fatalf("expected single-flight to execute 1 fetch, got %d", fetchCount.Load())
	}
	if results[0].HeadCommit == "" || results[0].HeadCommit != results[1].HeadCommit {
		t.Fatalf("mismatched coalesced results: %+v vs %+v", results[0], results[1])
	}
	if results[0].Coalesced || !results[1].Coalesced {
		t.Fatalf("expected caller 0 Coalesced=false, caller 1 Coalesced=true; got %v, %v", results[0].Coalesced, results[1].Coalesced)
	}

	// Take the upstream offline after preflight. Both real workspace prepares must use the shared mirror.
	if err := os.Rename(remote, filepath.Join(workspaceTemp(t), "offline")); err != nil {
		t.Fatal(err)
	}
	source := workspacePlan(t, home, repository.LayoutRoot, remote).Source
	var preparations [2]taskworkspace.Record
	for i, todoID := range []string{"todo_first", "todo_second"} {
		source.TodoID, source.Repositories.TodoID = todoID, todoID
		preview := workspacenaming.Resolve(source, workspacenaming.NodeContext{Home: home, PersonaHome: n.dir})
		if preview.Plan == nil {
			t.Fatal(preview)
		}
		raw, err := json.Marshal(taskworkspace.Request{
			WorkspaceID: "ws_" + todoID,
			OperationID: "op_" + todoID,
			Plan:        *preview.Plan,
			MirrorRepos: []string{req.RepoID},
		})
		if err != nil {
			t.Fatal(err)
		}
		wg.Go(func() {
			out, err := n.handle(ctx, "task.workspace.prepare", raw)
			errs[i] = err
			if err == nil {
				preparations[i] = out.(taskworkspace.Record)
			}
		})
	}
	wg.Wait()
	for i, prep := range preparations {
		if errs[i] != nil || prep.Error != nil || prep.State != "ready" || len(prep.Repositories) != 1 ||
			prep.Repositories[0].ResolvedCommit != results[i].HeadCommit {
			t.Fatalf("workspace %d did not prepare from the shared mirror: %+v, %v", i, prep, errs[i])
		}
	}
	if fetchCount.Load() != 1 {
		t.Fatalf("expected one mirror fetch across both workspace preparations, got %d", fetchCount.Load())
	}
}

func TestRepoMirrorFetchDeadlines(t *testing.T) {
	home := workspaceTemp(t)
	remote := workspaceRemote(t, "main")
	var remaining time.Duration
	n := &Node{
		home: home,
		dir:  filepath.Join(home, ".persona"),
		repoMirrors: repoMirrorManager{
			beforeFetch: func(ctx context.Context, _ string) error {
				deadline, _ := ctx.Deadline()
				remaining = time.Until(deadline)
				return nil
			},
		},
	}
	for _, tc := range []struct {
		name, repoID string
		override     time.Duration
		min, max     time.Duration
	}{
		{"cold creation", "repo_web", 0, 9 * time.Minute, 10 * time.Minute},
		{"incremental refresh", "repo_web", 0, time.Minute, 2 * time.Minute},
		{"cold override", "repo_api", 30 * time.Second, 20 * time.Second, 30 * time.Second},
		{"incremental override", "repo_api", 30 * time.Second, 20 * time.Second, 30 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n.repoMirrors.fetchTimeout = tc.override
			info := repoMirrorCall[repository.MirrorInfo](t, n, repository.MethodRepoMirrorEnsure, repository.MirrorEnsureRequest{
				RepoID:   tc.repoID,
				Identity: testRepoIdentity(t, "web"),
				CloneURL: remote,
				Force:    true,
			})
			if !info.Fetched || remaining < tc.min || remaining > tc.max {
				t.Fatalf("fetch with %v remaining, want a completed fetch with deadline in [%v, %v]", remaining, tc.min, tc.max)
			}
		})
	}
}

func TestRepoMirrorValidationRejectsTraversalAndSymlinks(t *testing.T) {
	home := workspaceTemp(t)
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	remote := workspaceRemote(t, "main")

	for _, badID := range []string{"", "../escape", "a/b", "CON", "repo..bad", ".hidden"} {
		if _, err := n.ensureRepoMirror(t.Context(), repository.MirrorEnsureRequest{
			RepoID:   badID,
			Identity: testRepoIdentity(t, "web"),
			CloneURL: remote,
		}); err == nil {
			t.Errorf("expected invalid repo_id %q to fail ensure", badID)
		}
		if _, err := n.removeRepoMirror(t.Context(), repository.MirrorRemoveRequest{RepoID: badID}); err == nil {
			t.Errorf("expected invalid repo_id %q to fail remove", badID)
		}
	}

	// Symlinked repos root is rejected.
	outside := workspaceTemp(t)
	if err := os.MkdirAll(n.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(n.dir, "repos")); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ensureRepoMirror(t.Context(), repository.MirrorEnsureRequest{
		RepoID:   "repo_web",
		Identity: testRepoIdentity(t, "web"),
		CloneURL: remote,
	}); err == nil {
		t.Fatal("expected symlinked repos directory to be rejected")
	}
}

func TestBackendWorkspacePreparationLeveragesManagedMirror(t *testing.T) {
	home := workspaceTemp(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	var fetchCalls atomic.Int32
	n := &Node{
		home: home,
		dir:  filepath.Join(home, ".persona"),
		repoMirrors: repoMirrorManager{
			now: func() time.Time { return now },
			beforeFetch: func(_ context.Context, _ string) error {
				fetchCalls.Add(1)
				return nil
			},
		},
	}

	remote := workspaceRemote(t, "trunk")
	head := strings.TrimSpace(workspaceGit(t, remote, "rev-parse", "HEAD"))

	// Pre-populate the managed mirror for repo "web".
	info := repoMirrorCall[repository.MirrorInfo](t, n, repository.MethodRepoMirrorEnsure, repository.MirrorEnsureRequest{
		RepoID:   "web",
		Identity: testRepoIdentity(t, "web"),
		CloneURL: remote,
	})
	if !info.Exists || !info.Fetched || fetchCalls.Load() != 1 {
		t.Fatalf("unexpected pre-ensure: %+v", info)
	}

	// Prepare a backend workspace with MirrorRepos=["web"] while mirror is fresh within TTL:
	// should NOT perform another remote fetch (fetchCalls remains 1), and should create a
	// self-contained worktree pointing origin at the real remote with zero alternates.
	plan := workspacePlan(t, home, repository.LayoutRoot, remote)
	req := taskworkspace.Request{
		WorkspaceID: "ws_mirror",
		OperationID: "op_mirror",
		Plan:        plan,
		MirrorRepos: []string{"web"},
	}
	result := workspaceCall(t, n, "prepare", req)
	if result.State != "ready" || len(result.Repositories) != 1 {
		t.Fatalf("prepare failed: %+v", result)
	}
	if fetchCalls.Load() != 1 {
		t.Fatalf("expected TTL-fresh mirror reuse without extra remote fetch, got %d fetches", fetchCalls.Load())
	}
	repo := result.Repositories[0]
	if repo.ResolvedCommit != head || repo.ResolvedRef != "refs/heads/trunk" {
		t.Fatalf("unexpected resolved repo: %+v", repo)
	}
	// Verify remote.origin.url points to the real remote, not ~/.persona/repos/.
	originURL := strings.TrimSpace(workspaceGit(t, repo.Path, "config", "--get", "remote.origin.url"))
	if originURL != remote {
		t.Fatalf("expected origin=%q, got %q", remote, originURL)
	}
	// Verify no objects/info/alternates file exists (workspace remains self-contained).
	if _, err := os.Stat(filepath.Join(repo.CommonDir, "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Fatalf("expected no alternates file, err=%v", err)
	}
	// Removing the managed mirror must not break inspect or worktree commits.
	_ = repoMirrorCall[repository.MirrorRemoveResponse](t, n, repository.MethodRepoMirrorRemove, repository.MirrorRemoveRequest{RepoID: "web"})
	inspected := workspaceCall(t, n, "inspect", req)
	if inspected.Error != nil || inspected.Repositories[0].CurrentHead != head {
		t.Fatalf("inspect after mirror removal failed: %+v", inspected)
	}
}

func TestBackendWorkspaceMirrorForceRefreshesMissingRef(t *testing.T) {
	home := workspaceTemp(t)
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	var fetchCalls atomic.Int32
	n := &Node{
		home: home,
		dir:  filepath.Join(home, ".persona"),
		repoMirrors: repoMirrorManager{
			now: func() time.Time { return now },
			beforeFetch: func(_ context.Context, _ string) error {
				fetchCalls.Add(1)
				return nil
			},
		},
	}

	remote := workspaceRemote(t, "main")
	// Populate mirror before new feature branch exists.
	_ = repoMirrorCall[repository.MirrorInfo](t, n, repository.MethodRepoMirrorEnsure, repository.MirrorEnsureRequest{
		RepoID:   "web",
		Identity: testRepoIdentity(t, "web"),
		CloneURL: remote,
	})
	if fetchCalls.Load() != 1 {
		t.Fatalf("expected 1 fetch, got %d", fetchCalls.Load())
	}

	// Push a brand-new branch on remote while mirror is still within TTL.
	workspaceGit(t, remote, "checkout", "-b", "release/v2")
	if err := os.WriteFile(filepath.Join(remote, "v2.txt"), []byte("release\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workspaceGit(t, remote, "add", ".")
	workspaceGit(t, remote, "commit", "-m", "Release v2")
	v2Head := strings.TrimSpace(workspaceGit(t, remote, "rev-parse", "HEAD"))
	workspaceGit(t, remote, "checkout", "main")

	plan := workspacePlanWithRef(t, home, remote, repository.RequestedRef{Kind: "branch", Value: "release/v2"})

	req := taskworkspace.Request{
		WorkspaceID: "ws_mirror_ref",
		OperationID: "op_mirror_ref",
		Plan:        plan,
		MirrorRepos: []string{"web"},
	}
	result := workspaceCall(t, n, "prepare", req)
	if result.State != "ready" || result.Repositories[0].ResolvedCommit != v2Head {
		t.Fatalf("expected mirror force-refresh to resolve new branch commit %s, got %+v", v2Head, result)
	}
	if fetchCalls.Load() != 2 {
		t.Fatalf("expected 1 initial fetch + 1 force refresh for missing branch, got %d", fetchCalls.Load())
	}
}

func workspacePlanWithRef(t *testing.T, home, url string, ref repository.RequestedRef) workspacenaming.Plan {
	t.Helper()
	id := testRepoIdentity(t, "web")
	rows := []repository.PreparationRepository{{
		RepoID:         "web",
		RequestedRef:   ref,
		SourceSnapshot: repository.SourceSnapshot{Identity: id, CloneURL: url},
	}}
	source := workspacenaming.Source{
		Manager:   "example",
		TodoID:    "todo_example",
		MachineID: "node_example",
		Title:     "Example",
		PlannedAt: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC),
		Timezone:  "UTC",
		Rules:     workspacenaming.Defaults(),
		Layout:    repository.LayoutRoot,
		Repositories: repository.PreparationSnapshot{
			SchemaVersion: 1,
			Manager:       "example",
			TodoID:        "todo_example",
			Repositories:  rows,
		},
	}
	preview := workspacenaming.Resolve(source, workspacenaming.NodeContext{Home: home, PersonaHome: filepath.Join(home, ".persona")})
	if preview.Plan == nil {
		t.Fatal(preview)
	}
	return *preview.Plan
}
