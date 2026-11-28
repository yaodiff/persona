package node

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	repository "github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func TestRemoteFetchSharedByWorkspaceAndMirrors(t *testing.T) {
	home := workspaceTemp(t)
	seed := workspaceRemote(t, "main")
	workspaceGit(t, seed, "update-server-info")
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	var workers sync.WaitGroup
	defer func() {
		cancel()
		workers.Wait()
	}()

	directFetching, releaseDirect := make(chan struct{}), make(chan struct{})
	otherDiscovering, releaseOther := make(chan struct{}), make(chan struct{})
	directEntered := sync.OnceFunc(func() { close(directFetching) })
	otherEntered := sync.OnceFunc(func() { close(otherDiscovering) })
	files := http.FileServer(http.Dir(filepath.Join(seed, ".git")))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Serve real dumb HTTP Git repositories. The two web aliases share
		// the same files, and only the direct workspace uses the suffixless URL.
		repo, path, found := strings.Cut(strings.TrimPrefix(r.URL.Path, "/example/"), "/")
		if !found {
			http.NotFound(w, r)
			return
		}
		switch {
		case repo == "web" && strings.HasPrefix(path, "objects/"):
			directEntered()
			select {
			case <-releaseDirect:
			case <-ctx.Done():
				return
			}
		case repo == "api.git" && path == "info/refs":
			otherEntered()
			select {
			case <-releaseOther:
			case <-ctx.Done():
				return
			}
		}
		r.URL.Path = "/" + path
		files.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)

	upstream := server.URL + "/example/web.git"
	identity, err := repository.Parse(upstream, "github")
	if err != nil {
		t.Fatal(err)
	}
	web := repository.MirrorEnsureRequest{RepoID: "web", Identity: identity, CloneURL: upstream}
	refresh := web
	refresh.RepoID = "repo_web_refresh"
	for _, req := range []repository.MirrorEnsureRequest{web, refresh} {
		if info, err := n.ensureRepoMirror(ctx, req); err != nil || !info.Fetched {
			t.Fatalf("seed mirror %s: fetched=%v, err=%v", req.RepoID, info.Fetched, err)
		}
	}

	directURL := server.URL + "/example/web/"
	direct := remoteFetchWorkspace(t, home, "direct", directURL, identity)
	directDone := make(chan error, 1)
	workers.Go(func() { directDone <- prepareRemoteFetchWorkspace(ctx, n, direct) })
	awaitRemoteFetch(t, ctx, directFetching)

	// A warm mirror's network fetch and the workspace's fetch use the same
	// turn despite the different repo IDs and URL spellings.
	refresh.Force = true
	refreshDone := make(chan error, 1)
	workers.Go(func() {
		_, err := n.ensureRepoMirror(ctx, refresh)
		refreshDone <- err
	})
	awaitRemoteFetchUsers(t, ctx, &n.remoteFetches, upstream, 2)

	otherURL := server.URL + "/example/api.git"
	otherID, err := repository.Parse(otherURL, "github")
	if err != nil {
		t.Fatal(err)
	}
	otherDone := make(chan error, 1)
	workers.Go(func() {
		_, err := n.ensureRepoMirror(ctx, repository.MirrorEnsureRequest{RepoID: "api", Identity: otherID, CloneURL: otherURL})
		otherDone <- err
	})
	awaitRemoteFetch(t, ctx, otherDiscovering)
	n.remoteFetches.mu.Lock()
	slotsInUse := len(n.remoteFetches.slots)
	n.remoteFetches.mu.Unlock()
	if slotsInUse != 2 {
		t.Fatalf("workspace fetch and mirror discovery hold %d slots, want 2", slotsInUse)
	}

	// When its last RPC caller goes away, a queued mirror flight must give
	// up its place without waiting for either running Git command.
	queuedCtx, stopQueued := context.WithCancel(ctx)
	defer stopQueued()
	queued := web
	queued.RepoID = "repo_queued"
	queuedDone := make(chan error, 1)
	workers.Go(func() {
		_, err := n.ensureRepoMirror(queuedCtx, queued)
		queuedDone <- err
	})
	awaitRemoteFetchUsers(t, ctx, &n.remoteFetches, upstream, 3)
	n.repoMirrors.mu.Lock()
	flight := n.repoMirrors.flights[queued.RepoID]
	n.repoMirrors.mu.Unlock()
	if flight == nil {
		t.Fatal("queued mirror did not have a shared flight")
	}
	stopQueued()
	if err := awaitRemoteFetch(t, ctx, queuedDone); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceling a queued mirror returned %v", err)
	}
	awaitRemoteFetch(t, ctx, flight.done)
	awaitRemoteFetchUsers(t, ctx, &n.remoteFetches, upstream, 2)

	// Both slots are occupied by real Git commands. A fresh managed mirror
	// still supplies the private bare store, and Git creates the worktree.
	local := remoteFetchWorkspace(t, home, "cached", upstream, identity)
	local.MirrorRepos = []string{web.RepoID}
	if err := prepareRemoteFetchWorkspace(ctx, n, local); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(local.Plan.RootPath, "file.txt")); err != nil || string(data) != "base\n" {
		t.Fatalf("local mirror did not materialize the worktree: %q, %v", data, err)
	}
	localSource := seed
	if filepath.VolumeName(seed) == "" {
		localSource = (&url.URL{Scheme: "file", Path: seed}).String()
	}
	fileReq := repository.MirrorEnsureRequest{RepoID: "repo_file", Identity: identity, CloneURL: localSource}
	if info, err := n.ensureRepoMirror(ctx, fileReq); err != nil || !info.Fetched {
		t.Fatalf("file mirror with both network slots occupied: fetched=%v, err=%v", info.Fetched, err)
	}

	close(releaseDirect)
	for _, done := range []<-chan error{directDone, refreshDone} {
		if err := awaitRemoteFetch(t, ctx, done); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.TrimSpace(workspaceGit(t, direct.Plan.RootPath, "config", "--get", "remote.origin.url")); got != directURL {
		t.Fatalf("workspace origin = %q, want the original %q", got, directURL)
	}
	close(releaseOther)
	if err := awaitRemoteFetch(t, ctx, otherDone); err != nil {
		t.Fatal(err)
	}
	assertRemoteFetchIdle(t, &n.remoteFetches)
}

func TestRemoteFetchWorkspaceQueueDeadline(t *testing.T) {
	home := workspaceTemp(t)
	n := &Node{home: home, dir: filepath.Join(home, ".persona")}
	server := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(server.Close)
	upstream := server.URL + "/example/web.git"
	identity, err := repository.Parse(upstream, "github")
	if err != nil {
		t.Fatal(err)
	}
	req := remoteFetchWorkspace(t, home, "queued", upstream, identity)
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		holders := make(chan error, 2)
		for _, cloneURL := range []string{"https://example.com/first", "https://example.com/second"} {
			go func() {
				_, err := n.remoteFetches.run(ctx, cloneURL, func(ctx context.Context) (string, error) {
					<-ctx.Done()
					return "", ctx.Err()
				})
				holders <- err
			}()
		}
		synctest.Wait()

		prepareCtx, stop := context.WithTimeout(ctx, time.Minute)
		defer stop()
		raw, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		out, err := n.handle(prepareCtx, "task.workspace.prepare", raw)
		if err != nil {
			t.Fatal(err)
		}
		got := out.(taskworkspace.Record)
		if !errors.Is(prepareCtx.Err(), context.DeadlineExceeded) || got.State != "failed" || got.Error == nil || got.Error.Code != "source_fetch_timeout" {
			t.Fatalf("prepare queued at ref discovery returned state %q, error %v and context error %v", got.State, got.Error, prepareCtx.Err())
		}
		stored, lock, err := n.lockBackendReceipt(req.WorkspaceID, "inspect", "")
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		if stored.journal.InFlight || stored.journal.Error == nil || stored.journal.Error.Code != "source_fetch_timeout" {
			t.Fatalf("queued timeout receipt has in_flight=%v and error %v", stored.journal.InFlight, stored.journal.Error)
		}
		cancel()
		for range 2 {
			if err := <-holders; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		}
		assertRemoteFetchIdle(t, &n.remoteFetches)
	})
}

func remoteFetchWorkspace(t *testing.T, home, id, cloneURL string, identity repository.Identity) taskworkspace.Request {
	t.Helper()
	plan := workspacePlan(t, home, repository.LayoutRoot, cloneURL)
	plan.Source.TodoID = "todo_" + id
	plan.Source.Repositories.TodoID = plan.Source.TodoID
	plan.Source.Repositories.Repositories[0].SourceSnapshot.Identity = identity
	resolved := workspacenaming.Resolve(plan.Source, plan.Node)
	if resolved.Plan == nil {
		t.Fatal(resolved)
	}
	return taskworkspace.Request{WorkspaceID: "ws_" + id, OperationID: "op_" + id, Plan: *resolved.Plan}
}

func prepareRemoteFetchWorkspace(ctx context.Context, n *Node, req taskworkspace.Request) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	out, err := n.handle(ctx, "task.workspace.prepare", raw)
	if err != nil {
		return err
	}
	result := out.(taskworkspace.Record)
	if result.Error != nil {
		return result.Error
	}
	if result.State != "ready" {
		return errors.New("workspace preparation did not finish")
	}
	return nil
}

func awaitRemoteFetch[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-ctx.Done():
		t.Fatalf("waiting for remote Git test event: %v", ctx.Err())
		var zero T
		return zero
	}
}

func awaitRemoteFetchUsers(t *testing.T, ctx context.Context, c *remoteFetchCoordinator, cloneURL string, want int) {
	t.Helper()
	key := remoteFetchKey(cloneURL)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		c.mu.Lock()
		turn := c.turns[key]
		ready := turn != nil && turn.users == want
		c.mu.Unlock()
		if ready {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("waiting for %d callers on one upstream: %v", want, ctx.Err())
		}
	}
}
