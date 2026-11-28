package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func countDeviceCalls(f *fakeHub, device, method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if (device == "" || call.Device == device) && call.Method == method {
			n++
		}
	}
	return n
}

func TestAssignPendingPlacementReducesWorkspaceAndRepoRPCs(t *testing.T) {
	t.Run("writable-inferred-existing", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		e.f.mu.Lock()
		e.f.nodes = map[string]nodeAnswer{
			"worktree.status": func(_ string, raw json.RawMessage) (any, error) {
				var in struct{ Cwd string }
				_ = json.Unmarshal(raw, &in)
				return proto.WorktreeStatus{Path: in.Cwd, Branch: "main"}, nil
			},
			"workspace.resolve": func(_ string, raw json.RawMessage) (any, error) {
				var in struct{ Path string }
				_ = json.Unmarshal(raw, &in)
				return proto.Workspace{ID: in.Path, Cwd: in.Path, Available: true}, nil
			},
		}
		e.f.mu.Unlock()
		_ = e.doReq(http.MethodGet, "/api/v1/assistant/placement/pending", roleExtension, "sched-ext", "")

		res, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
			"tasks":[{
				"title":"Edit repository",
				"prompt":"Implement feature",
				"cwd":"~/repo",
				"launcher":"codex",
				"role":"worker",
				"permission":"bypassPermissions",
				"goal":"Ship change",
				"done_when":["tests pass"],
				"user_requested":true,
				"why":"Any Linux worker",
				"placement_constraints":{"os":"linux"}
			}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		planID := extractPlanID(t, res)
		p, err := e.s.loadPlan(ctx, planID)
		if err != nil {
			t.Fatal(err)
		}
		todoID := p.Tasks[0].TodoID

		rec := e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+todoID+"/assign", roleExtension, "sched-ext",
			`{"machine":"linux-idle","reason":"lowest load","version":1}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("assign = %d %s, want 200", rec.Code, rec.Body.String())
		}
		waitFor(t, "assigned writable task starts", func() bool {
			return len(taskStartMachines(e.f.fakeHub)) == 1 && e.s.taskStarting.Load() == 0
		})

		fsStat := countDeviceCalls(e.f.fakeHub, "linux-idle", "fs.stat")
		wtStatus := countDeviceCalls(e.f.fakeHub, "linux-idle", "worktree.status")
		wsResolve := countDeviceCalls(e.f.fakeHub, "linux-idle", "workspace.resolve")
		t.Logf("writable assign+admit RPCs: fs.stat=%d worktree.status=%d workspace.resolve=%d (total=%d)",
			fsStat, wtStatus, wsResolve, fsStat+wtStatus+wsResolve)
		if fsStat != 1 || wtStatus != 1 || wsResolve != 1 {
			t.Fatalf("unexpected RPC counts: fs.stat=%d worktree.status=%d workspace.resolve=%d; want 1, 1, 1",
				fsStat, wtStatus, wsResolve)
		}
		stored, err := e.s.loadPlan(ctx, planID)
		if err != nil {
			t.Fatal(err)
		}
		if got := stored.Tasks[0]; got.Repo != "/home/linux/repo" || got.Workspace == nil || got.Workspace.Mode != workspaceExisting ||
			got.WorkspaceResolved == nil || got.WorkspaceResolved.Path != "/home/linux/repo" {
			t.Fatalf("unexpected resolved task state: %+v", got)
		}
	})

	t.Run("fallback-assign-deduplicates-dir-check-and-admit", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		e.f.mu.Lock()
		e.f.nodes = map[string]nodeAnswer{
			"worktree.status": func(_ string, raw json.RawMessage) (any, error) {
				var in struct{ Cwd string }
				_ = json.Unmarshal(raw, &in)
				return proto.WorktreeStatus{Path: in.Cwd, Branch: "main"}, nil
			},
			"workspace.resolve": func(_ string, raw json.RawMessage) (any, error) {
				var in struct{ Path string }
				_ = json.Unmarshal(raw, &in)
				return proto.Workspace{ID: in.Path, Cwd: in.Path, Available: true}, nil
			},
		}
		e.f.mu.Unlock()

		_, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
			"tasks":[{
				"title":"Fallback writable job",
				"prompt":"Run change",
				"cwd":"~/repo",
				"launcher":"codex",
				"role":"worker",
				"permission":"bypassPermissions",
				"goal":"Ship change",
				"done_when":["tests pass"],
				"user_requested":true,
				"why":"Fallback Linux worker",
				"placement_constraints":{"os":"linux","candidate_machines":["linux-idle"]}
			}]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		e.s.sweepPendingPlacement(ctx)
		waitFor(t, "fallback task starts", func() bool {
			return len(taskStartMachines(e.f.fakeHub)) == 1 && e.s.taskStarting.Load() == 0
		})

		fsStat := countDeviceCalls(e.f.fakeHub, "linux-idle", "fs.stat")
		wtStatus := countDeviceCalls(e.f.fakeHub, "linux-idle", "worktree.status")
		wsResolve := countDeviceCalls(e.f.fakeHub, "linux-idle", "workspace.resolve")
		t.Logf("fallback assign+admit RPCs: fs.stat=%d worktree.status=%d workspace.resolve=%d (total=%d)",
			fsStat, wtStatus, wsResolve, fsStat+wtStatus+wsResolve)
		if fsStat != 1 || wtStatus != 1 || wsResolve != 1 {
			t.Fatalf("unexpected fallback RPC counts: fs.stat=%d worktree.status=%d workspace.resolve=%d; want 1, 1, 1",
				fsStat, wtStatus, wsResolve)
		}
	})

	t.Run("second-assign-same-dir-reuses-cached-metadata", func(t *testing.T) {
		e := newPlacementTestEnv(t)
		ctx := context.Background()
		e.f.mu.Lock()
		e.f.nodes = map[string]nodeAnswer{
			"worktree.status": func(_ string, raw json.RawMessage) (any, error) {
				var in struct{ Cwd string }
				_ = json.Unmarshal(raw, &in)
				return proto.WorktreeStatus{Path: in.Cwd, Branch: "main"}, nil
			},
		}
		e.f.mu.Unlock()
		_ = e.doReq(http.MethodGet, "/api/v1/assistant/placement/pending", roleExtension, "sched-ext", "")

		res, err := e.s.toolTaskPlan(ctx, "conv_pub", json.RawMessage(`{
			"tasks":[
				{"title":"Read one","prompt":"Inspect A","cwd":"~/repo","launcher":"codex","role":"worker","readonly":true,"goal":"Check A","user_requested":true,"why":"Linux","placement_constraints":{"os":"linux"}},
				{"title":"Read two","prompt":"Inspect B","cwd":"~/repo","launcher":"codex","role":"worker","readonly":true,"goal":"Check B","user_requested":true,"why":"Linux","placement_constraints":{"os":"linux"}}
			]
		}`))
		if err != nil {
			t.Fatal(err)
		}
		planID := extractPlanID(t, res)
		p, err := e.s.loadPlan(ctx, planID)
		if err != nil {
			t.Fatal(err)
		}
		for _, task := range p.Tasks {
			rec := e.doReq(http.MethodPost, "/api/v1/assistant/placement/"+task.TodoID+"/assign", roleExtension, "sched-ext",
				`{"machine":"linux-idle","reason":"lowest load","version":1}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("assign %s = %d %s, want 200", task.TodoID, rec.Code, rec.Body.String())
			}
		}
		waitFor(t, "both readonly tasks start", func() bool {
			return len(taskStartMachines(e.f.fakeHub)) == 2 && e.s.taskStarting.Load() == 0
		})
		fsStat := countDeviceCalls(e.f.fakeHub, "linux-idle", "fs.stat")
		wtStatus := countDeviceCalls(e.f.fakeHub, "linux-idle", "worktree.status")
		wsResolve := countDeviceCalls(e.f.fakeHub, "linux-idle", "workspace.resolve")
		// Separate HTTP assign requests each run a live fs.stat check (so deleted directories are never accepted),
		// while sharing the cached structural worktree.status lookup.
		if fsStat != 2 || wtStatus != 1 || wsResolve != 0 {
			t.Fatalf("2 readonly assigns RPCs: fs.stat=%d worktree.status=%d workspace.resolve=%d; want 2, 1, 0",
				fsStat, wtStatus, wsResolve)
		}
	})
}

func TestResolveTaskWorkspaceAndRepoDeduplication(t *testing.T) {
	t.Run("inferred-existing-avoids-duplicate-workspace-resolve", func(t *testing.T) {
		s, f := workspaceTaskService(t)
		task := PlanTask{Machine: "box", Cwd: "/code/app"}
		if err := s.resolveTaskWorkspace(context.Background(), &task); err != nil {
			t.Fatal(err)
		}
		s.resolveTaskRepo(context.Background(), &task)
		wtStatus := countDeviceCalls(f, "box", "worktree.status")
		wsResolve := countDeviceCalls(f, "box", "workspace.resolve")
		t.Logf("inferred existing resolve+repo RPCs: worktree.status=%d workspace.resolve=%d", wtStatus, wsResolve)
		if wtStatus != 1 || wsResolve != 1 {
			t.Fatalf("got worktree.status=%d workspace.resolve=%d; want 1, 1", wtStatus, wsResolve)
		}
		if task.Repo != "/code/app" || task.Workspace == nil || task.Workspace.Mode != workspaceExisting || task.WorkspaceResolved.Path != "/code/app" {
			t.Fatalf("unexpected task resolution: %+v", task)
		}
	})

	t.Run("symlinked-subdir-reuses-cwd-status-in-resolveTaskRepo", func(t *testing.T) {
		s, f := workspaceTaskService(t)
		f.nodes["worktree.status"] = func(string, json.RawMessage) (any, error) {
			return proto.WorktreeStatus{Path: "/data/real/repo", Branch: "main"}, nil
		}
		f.nodes["workspace.resolve"] = func(string, json.RawMessage) (any, error) {
			return proto.Workspace{ID: "/data/real/repo/sub", Cwd: "/data/real/repo/sub", Available: true}, nil
		}
		task := PlanTask{Machine: "box", Cwd: "/home/me/link/sub"}
		if err := s.resolveTaskWorkspace(context.Background(), &task); err != nil {
			t.Fatal(err)
		}
		s.resolveTaskRepo(context.Background(), &task)
		wtStatus := countDeviceCalls(f, "box", "worktree.status")
		wsResolve := countDeviceCalls(f, "box", "workspace.resolve")
		t.Logf("symlinked subdir resolve+repo RPCs: worktree.status=%d workspace.resolve=%d", wtStatus, wsResolve)
		if wtStatus != 1 || wsResolve != 1 {
			t.Fatalf("got worktree.status=%d workspace.resolve=%d; want 1, 1", wtStatus, wsResolve)
		}
		if task.Repo != "/data/real/repo" || task.WorkspaceResolved.Path != "/data/real/repo/sub" {
			t.Fatalf("unexpected symlinked resolution: repo=%q resolved=%+v", task.Repo, task.WorkspaceResolved)
		}
	})

	t.Run("non-git-directory-skips-workspace-resolve-and-reuses-status", func(t *testing.T) {
		s, f := workspaceTaskService(t)
		f.nodes["worktree.status"] = func(string, json.RawMessage) (any, error) {
			return proto.WorktreeStatus{}, nil
		}
		for range 2 {
			task := PlanTask{Machine: "box", Cwd: "/tmp/plain-dir"}
			if err := s.resolveTaskWorkspace(context.Background(), &task); err != nil {
				t.Fatal(err)
			}
			s.resolveTaskRepo(context.Background(), &task)
			if task.Workspace == nil || task.Workspace.Mode != workspaceNone || task.Repo != "" {
				t.Fatalf("non-git task = %+v, want workspaceNone and empty repo", task)
			}
		}
		if got := countDeviceCalls(f, "box", "worktree.status"); got != 1 {
			t.Fatalf("non-git worktree.status calls = %d, want 1", got)
		}
		if got := countDeviceCalls(f, "box", "workspace.resolve"); got != 0 {
			t.Fatalf("non-git workspace.resolve calls = %d, want 0", got)
		}
	})

	t.Run("readonly-task-keeps-workspace-none-and-caches-repo-lookup", func(t *testing.T) {
		s, f := workspaceTaskService(t)
		for range 2 {
			task := PlanTask{Machine: "box", Cwd: "/code/app", Readonly: true}
			if err := s.resolveTaskWorkspace(context.Background(), &task); err != nil {
				t.Fatal(err)
			}
			s.resolveTaskRepo(context.Background(), &task)
			if task.Workspace == nil || task.Workspace.Mode != workspaceNone || task.Repo != "/code/app" {
				t.Fatalf("readonly task = %+v, want workspaceNone and repo /code/app", task)
			}
		}
		if got := countDeviceCalls(f, "box", "workspace.resolve"); got != 0 {
			t.Fatalf("readonly workspace.resolve calls = %d, want 0", got)
		}
		if got := countDeviceCalls(f, "box", "worktree.status"); got != 1 {
			t.Fatalf("readonly worktree.status calls = %d, want 1", got)
		}
	})

	t.Run("dirty-and-branch-fields-are-never-cached", func(t *testing.T) {
		s, f := workspaceTaskService(t)
		f.nodes["worktree.status"] = func(string, json.RawMessage) (any, error) {
			return proto.WorktreeStatus{
				Worktree:   true,
				Ours:       true,
				Path:       "/wt/app/fix-a",
				Branch:     "fix/a",
				Root:       "/code/app",
				MainBranch: "main",
				Checkouts:  "/wt/app",
				Dirty:      7,
				Unmerged:   3,
			}, nil
		}
		st, err := s.taskWorktreeStatus(context.Background(), "box", "/wt/app/fix-a", repoLookup)
		if err != nil {
			t.Fatal(err)
		}
		if st.Path != "/wt/app/fix-a" || !st.Worktree || st.Root != "/code/app" {
			t.Fatalf("structural repo identity lost: %+v", st)
		}
		if st.Dirty != 0 || st.Unmerged != 0 || st.Branch != "" || st.MainBranch != "" || st.Ours || st.Checkouts != "" {
			t.Fatalf("mutable or unused worktree fields leaked into cache: %+v", st)
		}
	})

	t.Run("prefer-new-overlaps-resolve-and-prepare-and-preserves-fallbacks", func(t *testing.T) {
		base := newFakeHub().withMachine()
		base.sessions["box"] = nil
		bh := &blockingHub{fakeHub: base}
		resolveEntered := make(chan struct{})
		prepareEntered := make(chan struct{})
		bothEntered := make(chan struct{})
		var entered atomic.Int32
		bh.onCall = func(ctx context.Context, p DeviceCall) (json.RawMessage, bool, error) {
			switch p.Method {
			case "worktree.status":
				raw, err := json.Marshal(proto.WorktreeStatus{Path: "/code/app", Branch: "main"})
				return raw, true, err
			case "workspace.resolve":
				close(resolveEntered)
				if entered.Add(1) == 2 {
					close(bothEntered)
				}
				select {
				case <-bothEntered:
				case <-ctx.Done():
					return nil, true, ctx.Err()
				}
				raw, err := json.Marshal(proto.Workspace{ID: "/code/app", Cwd: "/code/app", Available: true})
				return raw, true, err
			case DeviceWorktreePrepare:
				close(prepareEntered)
				if entered.Add(1) == 2 {
					close(bothEntered)
				}
				select {
				case <-bothEntered:
				case <-ctx.Done():
					return nil, true, ctx.Err()
				}
				raw, err := json.Marshal(proto.WorktreePrepare{Root: "/code/app", RepoName: "app", Branch: "main", Path: "/wt/app"})
				return raw, true, err
			}
			return nil, false, nil
		}
		s := newBlockingService(t, bh)
		setSettings(t, s, func(v *Settings) { v.TaskWorkspacePreference = preferNew })

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		task := PlanTask{Machine: "box", Cwd: "/code/app", ReservedTodoID: "todo_new"}
		if err := s.resolveTaskWorkspace(ctx, &task); err != nil {
			t.Fatalf("prefer-new parallel resolve failed: %v", err)
		}
		select {
		case <-bothEntered:
		default:
			t.Fatal("workspace.resolve and worktree.prepare did not overlap concurrently")
		}
		if task.Workspace == nil || task.Workspace.Mode != workspaceNew || !strings.HasPrefix(task.WorkspaceResolved.Path, "/wt/app/") {
			t.Fatalf("unexpected prefer-new result: workspace=%+v resolved=%+v", task.Workspace, task.WorkspaceResolved)
		}

		// A subsequent no-commits refusal on the same directory must NOT be masked by caching worktree.prepare.
		bh.mu.Lock()
		bh.onCall = func(_ context.Context, p DeviceCall) (json.RawMessage, bool, error) {
			if p.Method == DeviceWorktreePrepare {
				return nil, true, noCommits()
			}
			return nil, false, nil
		}
		bh.mu.Unlock()
		task2 := PlanTask{Machine: "box", Cwd: "/code/app", ReservedTodoID: "todo_nocommit"}
		if err := s.resolveTaskWorkspace(ctx, &task2); err != nil {
			t.Fatalf("no-commits fallback failed: %v", err)
		}
		if task2.Workspace == nil || task2.Workspace.Mode != workspaceExisting || task2.WorkspaceResolved.Path != "/code/app" {
			t.Fatalf("no-commits fallback lost: workspace=%+v resolved=%+v", task2.Workspace, task2.WorkspaceResolved)
		}
	})

	t.Run("missing-directory-and-rpc-errors-never-cached", func(t *testing.T) {
		s, f := workspaceTaskService(t)
		ctx := context.Background()

		// 1. fs.stat missing -> records directory_missing and is not cached; next call succeeds.
		statKind := "missing"
		var statErr error
		f.nodes["fs.stat"] = func(_ string, raw json.RawMessage) (any, error) {
			if statErr != nil {
				return nil, statErr
			}
			return directoryStats(statRequestPath(t, raw), statKind), nil
		}
		pl1 := newPlacement("box", "enforce")
		if s.taskDirectoryExists(context.WithValue(ctx, placementKey{}, pl1), "box", "/code/missing") {
			t.Fatal("missing directory reported as existing")
		}
		if len(pl1.Candidates) != 1 || len(pl1.Candidates[0].Hits) != 1 || pl1.Candidates[0].Hits[0].Key != "directory_missing" {
			t.Fatalf("missing directory audit = %+v, want directory_missing", pl1.Candidates)
		}
		statErr = errors.New("node offline")
		pl2 := newPlacement("box", "enforce")
		if s.taskDirectoryExists(context.WithValue(ctx, placementKey{}, pl2), "box", "/code/missing") {
			t.Fatal("failed fs.stat reported as existing")
		}
		if len(pl2.Candidates) != 1 || len(pl2.Candidates[0].Hits) != 1 || pl2.Candidates[0].Hits[0].Key != "directory_unknown" {
			t.Fatalf("failed fs.stat audit = %+v, want directory_unknown", pl2.Candidates)
		}
		statErr, statKind = nil, "dir"
		pl3 := newPlacement("box", "enforce")
		pctx3 := context.WithValue(ctx, placementKey{}, pl3)
		if !s.taskDirectoryExists(pctx3, "box", "/code/missing") {
			t.Fatal("recovered directory was still rejected")
		}
		if got := countDeviceCalls(f, "box", "fs.stat"); got != 3 {
			t.Fatalf("fs.stat calls = %d, want 3 (no caching of missing/error)", got)
		}
		// Reuse within the same logical placement hits the placement-scoped positive cache.
		if !s.taskDirectoryExists(pctx3, "box", "/code/missing") {
			t.Fatal("same-placement directory check failed")
		}
		if got := countDeviceCalls(f, "box", "fs.stat"); got != 3 {
			t.Fatalf("same-placement fs.stat calls = %d, want 3", got)
		}
		// A separate request without that placement sees live deletion immediately.
		statKind = "missing"
		if s.taskDirectoryExists(ctx, "box", "/code/missing") {
			t.Fatal("separate request accepted a deleted directory from another placement")
		}
		if got := countDeviceCalls(f, "box", "fs.stat"); got != 4 {
			t.Fatalf("separate-request fs.stat calls = %d, want 4", got)
		}

		// 2. worktree.status error -> never cached; next call succeeds.
		wtErr := errors.New("rpc timeout")
		f.nodes["worktree.status"] = func(string, json.RawMessage) (any, error) {
			if wtErr != nil {
				return nil, wtErr
			}
			return proto.WorktreeStatus{Path: "/code/recover", Branch: "main"}, nil
		}
		task := PlanTask{Machine: "box", Cwd: "/code/recover"}
		if err := s.resolveTaskWorkspace(ctx, &task); err == nil {
			t.Fatal("worktree.status failure succeeded")
		}
		s.resolveTaskRepo(ctx, &task)
		if task.Repo != "" {
			t.Fatalf("failed resolveTaskRepo set repo %q", task.Repo)
		}
		wtErr = nil
		if err := s.resolveTaskWorkspace(ctx, &task); err != nil {
			t.Fatalf("recovered resolveTaskWorkspace failed: %v", err)
		}
		s.resolveTaskRepo(ctx, &task)
		if task.Repo != "/code/recover" {
			t.Fatalf("recovered repo = %q, want /code/recover", task.Repo)
		}
		if got := countDeviceCalls(f, "box", "worktree.status"); got != 3 {
			t.Fatalf("worktree.status calls = %d, want 3 (2 failures + 1 success)", got)
		}

		// 3. workspace.resolve error / unavailable -> never cached; next call succeeds.
		wsAvailable := false
		wsErr := errors.New("resolve failed")
		f.nodes["workspace.resolve"] = func(_ string, raw json.RawMessage) (any, error) {
			if wsErr != nil {
				return nil, wsErr
			}
			var in struct{ Path string }
			_ = json.Unmarshal(raw, &in)
			return proto.Workspace{Cwd: in.Path, Available: wsAvailable}, nil
		}
		taskWS := PlanTask{Machine: "box", Cwd: "/code/ws-err", Workspace: &TaskWorkspace{Mode: workspaceExisting, Existing: "/code/ws-err"}}
		if err := s.resolveTaskWorkspace(ctx, &taskWS); err == nil {
			t.Fatal("workspace.resolve RPC error succeeded")
		}
		wsErr = nil
		if err := s.resolveTaskWorkspace(ctx, &taskWS); err == nil {
			t.Fatal("unavailable workspace.resolve succeeded")
		}
		wsAvailable = true
		if err := s.resolveTaskWorkspace(ctx, &taskWS); err != nil || taskWS.WorkspaceResolved.Path != "/code/ws-err" {
			t.Fatalf("recovered workspace.resolve failed: err=%v resolved=%+v", err, taskWS.WorkspaceResolved)
		}
		if got := countDeviceCalls(f, "box", "workspace.resolve"); got != 4 {
			// 1 call from /code/recover above + 3 calls for /code/ws-err (error, unavailable, success).
			t.Fatalf("workspace.resolve calls = %d, want 4", got)
		}
	})

	t.Run("ttl-expiration-refreshes-metadata-deterministically", func(t *testing.T) {
		s, f := workspaceTaskService(t)
		ctx := context.Background()
		var clockMu sync.Mutex
		now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
		s.tasks.dirMeta.now = func() time.Time {
			clockMu.Lock()
			defer clockMu.Unlock()
			return now
		}
		advance := func(d time.Duration) {
			clockMu.Lock()
			now = now.Add(d)
			clockMu.Unlock()
		}

		pl := newPlacement("box", "enforce")
		pctx := context.WithValue(ctx, placementKey{}, pl)
		resolveOnce := func() {
			t.Helper()
			if !s.taskDirectoryExists(pctx, "box", "/code/app") {
				t.Fatal("expected directory to exist")
			}
			task := PlanTask{Machine: "box", Cwd: "/code/app"}
			if err := s.resolveTaskWorkspace(pctx, &task); err != nil {
				t.Fatal(err)
			}
			s.resolveTaskRepo(pctx, &task)
		}

		resolveOnce()
		if countDeviceCalls(f, "box", "fs.stat") != 1 || countDeviceCalls(f, "box", "worktree.status") != 1 || countDeviceCalls(f, "box", "workspace.resolve") != 1 {
			t.Fatalf("initial counts = fs.stat:%d worktree.status:%d workspace.resolve:%d; want 1, 1, 1",
				countDeviceCalls(f, "box", "fs.stat"), countDeviceCalls(f, "box", "worktree.status"), countDeviceCalls(f, "box", "workspace.resolve"))
		}

		// Just before TTL: all three hit cache.
		advance(taskDirCacheTTL - time.Second)
		resolveOnce()
		if countDeviceCalls(f, "box", "fs.stat") != 1 || countDeviceCalls(f, "box", "worktree.status") != 1 || countDeviceCalls(f, "box", "workspace.resolve") != 1 {
			t.Fatalf("pre-expiry counts grew: fs.stat:%d worktree.status:%d workspace.resolve:%d",
				countDeviceCalls(f, "box", "fs.stat"), countDeviceCalls(f, "box", "worktree.status"), countDeviceCalls(f, "box", "workspace.resolve"))
		}

		// Past TTL: all three refresh cleanly.
		advance(2 * time.Second)
		resolveOnce()
		if countDeviceCalls(f, "box", "fs.stat") != 2 || countDeviceCalls(f, "box", "worktree.status") != 2 || countDeviceCalls(f, "box", "workspace.resolve") != 2 {
			t.Fatalf("post-expiry counts did not refresh: fs.stat:%d worktree.status:%d workspace.resolve:%d; want 2, 2, 2",
				countDeviceCalls(f, "box", "fs.stat"), countDeviceCalls(f, "box", "worktree.status"), countDeviceCalls(f, "box", "workspace.resolve"))
		}
	})

	t.Run("plan-start-admit-reuses-workspace-and-repo-metadata", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			s, f := workspaceTaskService(t)
			args := `{"tasks":[
				{"title":"Task one","prompt":"Work one","machine":"box","cwd":"/code/app","permission":"bypassPermissions"},
				{"title":"Task two","prompt":"Work two","machine":"box","cwd":"/code/app","permission":"bypassPermissions"}
			]}`
			if _, err := s.toolTaskPlan(context.Background(), "workspace-chat", json.RawMessage(args)); err != nil {
				t.Fatal(err)
			}
			p := readonlyFeedPlan(t, s, "workspace-chat")
			if _, err := s.startPlan(context.Background(), p.ID, nil, false); err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			wtStatus := countDeviceCalls(f, "box", "worktree.status")
			wsResolve := countDeviceCalls(f, "box", "workspace.resolve")
			// Task 1 reuses /code/app (1 worktree.status + 1 workspace.resolve); task 2 creates a new checkout
			// under /wt/app/ whose repository is verified once at admission (1 additional worktree.status).
			t.Logf("2-task plan+start+admit RPCs: worktree.status=%d workspace.resolve=%d", wtStatus, wsResolve)
			if wtStatus != 2 || wsResolve != 1 {
				t.Fatalf("got worktree.status=%d workspace.resolve=%d; want 2, 1", wtStatus, wsResolve)
			}
			if len(f.starts) != 2 {
				t.Fatalf("starts = %d, want 2", len(f.starts))
			}
			// Second task saw occupied cwd at admission and still got its own new checkout.
			saved, err := s.loadPlan(context.Background(), p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Tasks[0].Workspace.Mode != workspaceExisting && saved.Tasks[1].Workspace.Mode != workspaceExisting {
				t.Fatalf("expected one task in existing workspace: %+v", saved.Tasks)
			}
			if !strings.HasPrefix(saved.Tasks[0].Cwd, "/wt/app/") && !strings.HasPrefix(saved.Tasks[1].Cwd, "/wt/app/") {
				t.Fatalf("expected second task to isolate in a new checkout: %+v", saved.Tasks)
			}
		})
	})
}

func TestTaskDirectoryAndWorkspaceRPCSingleflightAndCancelSafety(t *testing.T) {
	t.Run("concurrent-callers-coalesce-into-one-rpc", func(t *testing.T) {
		base := newFakeHub().withMachine()
		base.sessions["box"] = nil
		bh := &blockingHub{fakeHub: base}

		release := make(chan struct{})
		var statCalls, wtCalls, wsCalls atomic.Int32
		statEntered := make(chan struct{}, 1)
		wtEntered := make(chan struct{}, 1)
		wsEntered := make(chan struct{}, 1)

		bh.onCall = func(ctx context.Context, p DeviceCall) (json.RawMessage, bool, error) {
			switch p.Method {
			case "fs.stat":
				statCalls.Add(1)
				select {
				case statEntered <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-ctx.Done():
					return nil, true, ctx.Err()
				}
				raw, err := json.Marshal(directoryStats("/code/app", "dir"))
				return raw, true, err
			case "worktree.status":
				wtCalls.Add(1)
				select {
				case wtEntered <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-ctx.Done():
					return nil, true, ctx.Err()
				}
				raw, err := json.Marshal(proto.WorktreeStatus{Path: "/code/app", Branch: "main"})
				return raw, true, err
			case "workspace.resolve":
				wsCalls.Add(1)
				select {
				case wsEntered <- struct{}{}:
				default:
				}
				raw, err := json.Marshal(proto.Workspace{ID: "/code/app", Cwd: "/code/app", Available: true})
				return raw, true, err
			}
			return nil, false, nil
		}

		s := newBlockingService(t, bh)
		const workers = 8
		key := taskDirCacheKey("box", "/code/app")

		// Coalesce 8 concurrent taskDirectoryExists callers into 1 fs.stat RPC.
		var wg sync.WaitGroup
		results := make([]bool, workers)
		for i := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = s.taskDirectoryExists(context.Background(), "box", "/code/app")
			}()
		}
		<-statEntered
		waitForWaiters(t, &s.tasks.dirMeta.dirExists, key, workers)
		close(release)
		wg.Wait()
		for i, ok := range results {
			if !ok {
				t.Fatalf("worker %d taskDirectoryExists = false, want true", i)
			}
		}
		if got := statCalls.Load(); got != 1 {
			t.Fatalf("concurrent fs.stat calls = %d, want 1", got)
		}

		// Coalesce 8 concurrent resolveTaskWorkspace + resolveTaskRepo callers into 1 worktree.status + 1 workspace.resolve.
		release = make(chan struct{})
		errs := make([]error, workers)
		repos := make([]string, workers)
		paths := make([]string, workers)
		for i := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				task := PlanTask{Machine: "box", Cwd: "/code/app"}
				errs[i] = s.resolveTaskWorkspace(context.Background(), &task)
				if errs[i] == nil {
					s.resolveTaskRepo(context.Background(), &task)
					repos[i] = task.Repo
					paths[i] = task.WorkspaceResolved.Path
				}
			}()
		}
		<-wtEntered
		waitForWaiters(t, &s.tasks.dirMeta.worktree, key, workers)
		close(release)
		wg.Wait()
		for i := range workers {
			if errs[i] != nil || repos[i] != "/code/app" || paths[i] != "/code/app" {
				t.Fatalf("worker %d: err=%v repo=%q path=%q", i, errs[i], repos[i], paths[i])
			}
		}
		if wtCalls.Load() != 1 || wsCalls.Load() != 1 {
			t.Fatalf("concurrent resolve calls: worktree.status=%d workspace.resolve=%d; want 1, 1", wtCalls.Load(), wsCalls.Load())
		}
	})

	t.Run("canceled-waiter-does-not-poison-live-peer", func(t *testing.T) {
		base := newFakeHub().withMachine()
		base.sessions["box"] = nil
		bh := &blockingHub{fakeHub: base}

		entered := make(chan struct{})
		release := make(chan struct{})
		var calls atomic.Int32
		var callCtxCanceled atomic.Bool

		bh.onCall = func(ctx context.Context, p DeviceCall) (json.RawMessage, bool, error) {
			if p.Method != "fs.stat" {
				return nil, false, nil
			}
			calls.Add(1)
			close(entered)
			select {
			case <-release:
				raw, err := json.Marshal(directoryStats("/code/shared", "dir"))
				return raw, true, err
			case <-ctx.Done():
				callCtxCanceled.Store(true)
				return nil, true, ctx.Err()
			}
		}

		s := newBlockingService(t, bh)
		key := taskDirCacheKey("box", "/code/shared")

		ctx1, cancel1 := context.WithCancel(context.Background())
		res1 := make(chan bool, 1)
		go func() { res1 <- s.taskDirectoryExists(ctx1, "box", "/code/shared") }()
		<-entered

		res2 := make(chan bool, 1)
		go func() { res2 <- s.taskDirectoryExists(context.Background(), "box", "/code/shared") }()
		waitForWaiters(t, &s.tasks.dirMeta.dirExists, key, 2)

		cancel1()
		if got := <-res1; got {
			t.Fatal("canceled caller returned true")
		}
		if callCtxCanceled.Load() {
			t.Fatal("canceling one waiter aborted the shared RPC")
		}
		close(release)
		if got := <-res2; !got {
			t.Fatal("live peer failed after another waiter canceled")
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("fs.stat calls = %d, want 1", got)
		}
	})

	t.Run("last-waiter-cancel-aborts-flight-and-fresh-caller-retries", func(t *testing.T) {
		base := newFakeHub().withMachine()
		base.sessions["box"] = nil
		bh := &blockingHub{fakeHub: base}

		firstEntered := make(chan struct{})
		firstAborted := make(chan struct{})
		var calls atomic.Int32

		bh.onCall = func(ctx context.Context, p DeviceCall) (json.RawMessage, bool, error) {
			if p.Method != "fs.stat" {
				return nil, false, nil
			}
			if calls.Add(1) == 1 {
				close(firstEntered)
				<-ctx.Done()
				close(firstAborted)
				return nil, true, ctx.Err()
			}
			raw, err := json.Marshal(directoryStats("/code/retry", "dir"))
			return raw, true, err
		}

		s := newBlockingService(t, bh)
		ctx1, cancel1 := context.WithCancel(context.Background())
		res1 := make(chan bool, 1)
		go func() { res1 <- s.taskDirectoryExists(ctx1, "box", "/code/retry") }()
		<-firstEntered

		cancel1()
		if got := <-res1; got {
			t.Fatal("canceled sole caller returned true")
		}
		<-firstAborted

		// A fresh caller starts a clean flight immediately rather than joining the canceled one.
		if !s.taskDirectoryExists(context.Background(), "box", "/code/retry") {
			t.Fatal("fresh caller after sole-waiter cancellation failed")
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("fs.stat calls = %d, want 2", got)
		}
	})
}

func waitForWaiters[T any](t *testing.T, cache *taskMetaCache[T], key string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		cache.mu.Lock()
		f := cache.inFlight[key]
		n := 0
		if f != nil {
			n = f.waiters
		}
		cache.mu.Unlock()
		if n >= want {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("timed out waiting for %d waiters on %q", want, key)
}

func newBlockingService(t *testing.T, bh *blockingHub) *Service {
	t.Helper()
	st := &memStore{}
	return withSettings(t, bh.fakeHub, New(Options{Hub: bh, Store: func() Store { return st }, Logf: testLogf(t)}))
}

// blockingHub wraps fakeHub to allow holding specific node RPCs open without holding fakeHub.mu.
type blockingHub struct {
	*fakeHub
	mu     sync.Mutex
	onCall func(ctx context.Context, p DeviceCall) (json.RawMessage, bool, error)
}

func (b *blockingHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	b.mu.Lock()
	hook := b.onCall
	b.mu.Unlock()
	if hook != nil {
		if out, handled, err := hook(ctx, p); handled {
			return out, err
		}
	}
	return b.fakeHub.DeviceCall(ctx, p)
}
