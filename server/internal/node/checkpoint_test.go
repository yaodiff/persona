package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func checkpointRefsNow(t *testing.T, r *reviewRepo, session string) []checkpointRef {
	t.Helper()
	refs, err := checkpointRefs(context.Background(), r.dir, session)
	if err != nil {
		t.Fatal(err)
	}
	return refs
}

func checkpointTempNames(t *testing.T, r *reviewRepo) []string {
	t.Helper()
	dir := strings.TrimSpace(r.git("rev-parse", "--absolute-git-dir"))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "persona-checkpoint-") {
			out = append(out, e.Name())
		}
	}
	return out
}

func waitForCheckpointRefs(t *testing.T, r *reviewRepo, session string, want int) []checkpointRef {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		refs := checkpointRefsNow(t, r, session)
		if len(refs) >= want {
			return refs
		}
		select {
		case <-deadline.C:
			return refs
		case <-ticker.C:
		}
	}
}

func worktreeDigest(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\\x00%s\\x00%d\\x00", filepath.ToSlash(rel), info.Mode(), info.Size())
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(h, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func TestCheckpointCaptureIsReadOnlyAndSnapshotsTheWorkingTree(t *testing.T) {
	r := newReviewRepo(t, map[string]string{
		"a.txt":      "one\n",
		".gitignore": "ignored.txt\n",
	})
	r.write("a.txt", "one\ntwo\n")
	r.git("add", "a.txt")
	r.write("a.txt", "one\ntwo\nthree\n")
	r.write("new.txt", "new\n")
	r.write("ignored.txt", "ignored\n")

	gitDir := strings.TrimSpace(r.git("rev-parse", "--absolute-git-dir"))
	indexPath := filepath.Join(gitDir, "index")
	beforeIndex, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeIndexInfo, err := os.Stat(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeHead := r.git("rev-parse", "HEAD")
	beforeRefs := r.git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/tags", "refs/remotes", "refs/stash")
	beforeStash := r.git("stash", "list")
	beforeStatus := r.git("status", "--porcelain=v2", "-z", "--untracked-files=all")
	beforeWorktree := worktreeDigest(t, r.dir)

	res, err := r.node.captureCheckpoint(context.Background(), r.dir, "session-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Turn != 1 || res.Hash == "" {
		t.Fatalf("capture: %+v", res)
	}
	afterIndex, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	afterIndexInfo, err := os.Stat(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeIndex, afterIndex) || !beforeIndexInfo.ModTime().Equal(afterIndexInfo.ModTime()) {
		t.Fatal("the capture changed the user's index")
	}
	if got := r.git("rev-parse", "HEAD"); got != beforeHead {
		t.Fatalf("HEAD changed: %s -> %s", beforeHead, got)
	}
	if got := r.git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads", "refs/tags", "refs/remotes", "refs/stash"); got != beforeRefs {
		t.Fatalf("user refs changed:\n%s\n---\n%s", beforeRefs, got)
	}
	if got := r.git("stash", "list"); got != beforeStash {
		t.Fatalf("stash changed: %q -> %q", beforeStash, got)
	}
	if got := r.git("status", "--porcelain=v2", "-z", "--untracked-files=all"); got != beforeStatus {
		t.Fatal("the capture changed the working-tree status")
	}
	if got := worktreeDigest(t, r.dir); got != beforeWorktree {
		t.Fatal("the capture changed the working tree")
	}
	if got := r.git("show", res.Ref+":a.txt"); got != "one\ntwo\nthree" {
		t.Fatalf("snapshot a.txt = %q", got)
	}
	if got := r.git("show", res.Ref+":new.txt"); got != "new" {
		t.Fatalf("snapshot new.txt = %q", got)
	}
	if tree := r.git("ls-tree", "-r", "--name-only", res.Ref); strings.Contains(tree, "ignored.txt") {
		t.Fatalf("ignored file was captured:\n%s", tree)
	}
	if temps := checkpointTempNames(t, r); len(temps) != 0 {
		t.Fatalf("temporary checkpoint files were left: %v", temps)
	}
}

func TestCheckpointTurnReviewFindsShellChangesAndLiveWork(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
	r.write("carry.txt", "carry\n")
	first, err := r.node.captureCheckpoint(context.Background(), r.dir, "session-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "printf 'shell\\n' > shell.txt; printf 'two\\n' >> a.txt")
	cmd.Dir = r.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shell edit: %v: %s", err, out)
	}
	second, err := r.node.captureCheckpoint(context.Background(), r.dir, "session-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.node.gitReview(context.Background(), proto.GitReviewRequest{
		Cwd: r.dir, Scope: proto.ReviewTurn, Session: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Turn != 2 || res.Base.Hash != first.Hash || res.Target == nil || res.Target.Hash != second.Hash {
		t.Fatalf("turn review chose %+v", res)
	}
	fileOf(t, res, "a.txt")
	fileOf(t, res, "shell.txt")

	r.write("live.txt", "live\n")
	live, err := r.node.gitReview(context.Background(), proto.GitReviewRequest{
		Cwd: r.dir, Scope: proto.ReviewTurn, Session: "session-1", Live: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if live.Target == nil || !live.Target.Working || live.Base.Hash != second.Hash {
		t.Fatalf("live turn review chose %+v", live)
	}
	for _, f := range live.Files {
		if f.Path == "carry.txt" {
			t.Fatalf("live review reported the previous checkpoint's untracked file: %+v", f)
		}
	}
	fileOf(t, live, "live.txt")
	r.write("carry.txt", "changed carry\n")
	changed, err := r.node.gitReview(context.Background(), proto.GitReviewRequest{
		Cwd: r.dir, Scope: proto.ReviewTurn, Session: "session-1", Live: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	carry := fileOf(t, changed, "carry.txt")
	if carry.Status != "added" || !carry.Untracked {
		t.Fatalf("changed untracked checkpoint file = %+v, want added/untracked", carry)
	}

	pinned, err := r.node.gitReview(context.Background(), proto.GitReviewRequest{
		Cwd: r.dir, Scope: proto.ReviewTurn, Session: "session-1",
		Base: res.Base.Hash, Target: res.Target.Hash, Paths: []string{"shell.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pinned.Files) != 1 || pinned.Files[0].Path != "shell.txt" {
		t.Fatalf("pinned turn review: %+v", pathsOf(pinned))
	}
	targetOnly, err := r.node.gitReview(context.Background(), proto.GitReviewRequest{
		Cwd: r.dir, Scope: proto.ReviewTurn, Session: "session-1", Target: second.Hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if targetOnly.Base.Hash != first.Hash {
		t.Fatalf("target-only base = %s, want previous checkpoint %s", targetOnly.Base.Hash, first.Hash)
	}
	legacy, err := r.node.gitReview(context.Background(), proto.GitReviewRequest{
		Cwd: r.dir, Scope: proto.ReviewTurn, Session: "session-1", Base: r.git("rev-parse", "--short", first.Hash), Live: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Base.Hash != first.Hash || legacy.Target == nil || !legacy.Target.Working {
		t.Fatalf("legacy turn short base: %+v", legacy)
	}
	legacyCompleted, err := r.node.gitReview(context.Background(), proto.GitReviewRequest{
		Cwd: r.dir, Scope: proto.ReviewTurn, Session: "session-1", Base: r.git("rev-parse", "--short", first.Hash),
	})
	if err != nil {
		t.Fatal(err)
	}
	if legacyCompleted.Base.Hash != first.Hash || legacyCompleted.Target == nil || legacyCompleted.Target.Hash != second.Hash {
		t.Fatalf("legacy completed turn short base: %+v", legacyCompleted)
	}
	_, err = r.node.gitReview(context.Background(), proto.GitReviewRequest{
		Cwd: r.dir, Scope: proto.ReviewTurn, Session: "session-1", Base: r.git("rev-parse", "--short", second.Hash),
	})
	if e, ok := err.(*rpc.Error); !ok || e.Code != proto.CodeNoCheckpoint {
		t.Fatalf("legacy short base without a following turn: %v", err)
	}
}

func TestCheckpointFirstTurnComparesWithHead(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
	parent := r.git("rev-parse", "HEAD")
	r.write("a.txt", "turn change\n")
	res, err := r.node.captureCheckpoint(context.Background(), r.dir, "session-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	r.write("a.txt", "a later user commit\n")
	r.git("add", "a.txt")
	r.git("commit", "-q", "-m", "later")
	got, err := r.node.gitReview(context.Background(), proto.GitReviewRequest{
		Cwd: r.dir, Scope: proto.ReviewTurn, Session: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Turn != 1 || got.Base.Hash != parent || got.Target == nil || got.Target.Hash != res.Hash {
		t.Fatalf("first turn review chose %+v", got)
	}
	pinned, err := r.node.gitReview(context.Background(), proto.GitReviewRequest{
		Cwd: r.dir, Scope: proto.ReviewTurn, Session: "session-1",
		Base: got.Base.Hash, Target: got.Target.Hash, Paths: []string{"a.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pinned.Files) != 1 || pinned.Files[0].Path != "a.txt" {
		t.Fatalf("pinned first turn review: %+v", pathsOf(pinned))
	}
	shortBase, err := r.node.gitReview(context.Background(), proto.GitReviewRequest{
		Cwd: r.dir, Scope: proto.ReviewTurn, Session: "session-1",
		Base: r.git("rev-parse", "--short", got.Base.Hash), Target: got.Target.Hash, Paths: []string{"a.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if shortBase.Base.Hash != got.Base.Hash || len(shortBase.Files) != 1 || shortBase.Files[0].Path != "a.txt" {
		t.Fatalf("pinned first turn review with an abbreviated base: %+v %+v", shortBase, pathsOf(shortBase))
	}
}

func TestCheckpointFirstTurnBeforeAnyCommit(t *testing.T) {
	home := reviewGitEnv(t)
	dir := t.TempDir()
	runPullGit(t, dir, "init", "-q", "--initial-branch=main")
	r := &reviewRepo{t: t, node: &Node{home: home, state: newState()}, dir: dir}
	r.write("new.txt", "new\n")
	res, err := r.node.captureCheckpoint(context.Background(), dir, "session-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.node.gitReview(context.Background(), proto.GitReviewRequest{
		Cwd: dir, Scope: proto.ReviewTurn, Session: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Base.Empty || got.Target == nil || got.Target.Hash != res.Hash || got.Head != nil {
		t.Fatalf("unborn turn review: %+v", got)
	}
	fileOf(t, got, "new.txt")
	pinned, err := r.node.gitReview(context.Background(), proto.GitReviewRequest{
		Cwd: dir, Scope: proto.ReviewTurn, Session: "session-1", Target: res.Hash, Paths: []string{"new.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pinned.Files) != 1 || pinned.Files[0].Path != "new.txt" {
		t.Fatalf("pinned unborn turn review: %+v", pathsOf(pinned))
	}
}

func TestCheckpointReviewRefusals(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
	_, err := r.node.gitReview(context.Background(), proto.GitReviewRequest{Cwd: r.dir, Scope: proto.ReviewTurn, Session: "session-1"})
	if e, ok := err.(*rpc.Error); !ok || e.Code != proto.CodeNoCheckpoint {
		t.Fatalf("no checkpoint: %v", err)
	}
	_, err = r.node.gitReview(context.Background(), proto.GitReviewRequest{Cwd: r.dir, Scope: proto.ReviewTurn, Session: "session-1", Live: true})
	if e, ok := err.(*rpc.Error); !ok || e.Code != proto.CodeNoCheckpoint {
		t.Fatalf("live without a checkpoint: %v", err)
	}
	if _, err := r.node.gitReview(context.Background(), proto.GitReviewRequest{Cwd: r.dir, Scope: proto.ReviewTurn}); err == nil {
		t.Fatal("turn without a session was accepted")
	}
	one, err := r.node.captureCheckpoint(context.Background(), r.dir, "session-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	r.write("b.txt", "other\n")
	two, err := r.node.captureCheckpoint(context.Background(), r.dir, "session-2", 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.node.gitReview(context.Background(), proto.GitReviewRequest{
		Cwd: r.dir, Scope: proto.ReviewTurn, Session: "session-1", Base: one.Hash, Target: two.Hash,
	})
	if e, ok := err.(*rpc.Error); !ok || e.Code != proto.CodeBadBase {
		t.Fatalf("another session's checkpoint: %v", err)
	}
}

func TestCheckpointLimitsSkipAndLeaveNothing(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*reviewRepo)
		limit checkpointLimits
		want  string
	}{
		{
			name:  "tracked files",
			setup: func(r *reviewRepo) { r.write("b.txt", "b\n"); r.git("add", "b.txt") },
			limit: checkpointLimits{maxTracked: 1},
			want:  "too many tracked files",
		},
		{
			name:  "repository too large",
			setup: func(r *reviewRepo) { r.write("b.txt", "b\n"); r.git("add", "b.txt") },
			limit: checkpointLimits{disableAbove: 1, maxTracked: 100},
			want:  "repository too large",
		},
		{
			name:  "snapshot bytes",
			setup: func(r *reviewRepo) { r.write("a.txt", "0123456789\n") },
			limit: checkpointLimits{maxSnapshotBytes: 1},
			want:  "snapshot is too large",
		},
		{
			name:  "changed bytes",
			setup: func(r *reviewRepo) { r.write("a.txt", "0123456789\n") },
			limit: checkpointLimits{maxChangedBytes: 1},
			want:  "changed content is too large",
		},
		{
			name:  "timeout",
			setup: func(r *reviewRepo) { r.write("a.txt", "changed\n") },
			limit: checkpointLimits{timeout: time.Nanosecond},
			want:  "timed out",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
			tc.setup(r)
			r.node.checkpoints.limits = tc.limit
			if _, err := r.node.captureCheckpoint(context.Background(), r.dir, "session-1", 1); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("capture error = %v, want %q", err, tc.want)
			}
			if refs := checkpointRefsNow(t, r, "session-1"); len(refs) != 0 {
				t.Fatalf("a skipped capture left refs: %+v", refs)
			}
			if temps := checkpointTempNames(t, r); len(temps) != 0 {
				t.Fatalf("a skipped capture left temporary files: %v", temps)
			}
		})
	}
}

func TestCheckpointPruneKeepsRefsReachableAcrossGC(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "start\n"})
	r.node.checkpoints.limits = checkpointLimits{keep: 3}
	var refs []checkpointRef
	for turn := int64(1); turn <= 4; turn++ {
		r.write("a.txt", fmt.Sprintf("turn %d\n", turn))
		if _, err := r.node.captureCheckpoint(context.Background(), r.dir, "session-1", turn); err != nil {
			t.Fatal(err)
		}
	}
	refs = checkpointRefsNow(t, r, "session-1")
	if len(refs) != 3 || refs[0].Turn != 2 || refs[2].Turn != 4 {
		t.Fatalf("refs after prune: %+v", refs)
	}
	r.git("gc", "--prune=now", "-q")
	if got := r.git("rev-parse", refs[2].Ref); got != refs[2].Hash {
		t.Fatalf("kept ref after gc: %s, want %s", got, refs[2].Hash)
	}
}

func TestCheckpointEndIsCapturedOffTheCallingPath(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
	r.write("a.txt", "working tree\n")
	start := time.Now()
	r.node.noteCheckpointEnds([]proto.Session{{SessionID: "session-1", Cwd: r.dir, Status: proto.StatusWorking}})
	r.node.noteCheckpointEnds([]proto.Session{{SessionID: "session-1", Cwd: r.dir, Status: proto.StatusDone}})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("turn-end handling blocked for %s", elapsed)
	}
	refs := waitForCheckpointRefs(t, r, "session-1", 1)
	if len(refs) != 1 {
		t.Fatalf("turn-end refs: %+v", refs)
	}
	r.node.noteCheckpointEnds([]proto.Session{{SessionID: "session-1", Cwd: r.dir, Status: proto.StatusDone}})
	if len(checkpointRefsNow(t, r, "session-1")) != 1 {
		t.Fatal("the same turn was captured twice")
	}
}

func TestCheckpointQueuesTheNextTurnAndWaitsBeforeItStarts(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
	cwds := []string{filepath.Join(r.dir, "a"), filepath.Join(r.dir, "b"), filepath.Join(r.dir, "c")}
	for _, cwd := range cwds {
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	firstStarted := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var calls []string
	r.node.checkpoints.capture = func(_ context.Context, cwd, _ string, _ int64) (checkpointResult, error) {
		mu.Lock()
		calls = append(calls, cwd)
		n := len(calls)
		mu.Unlock()
		if n == 1 {
			close(firstStarted)
			<-release
		}
		return checkpointResult{}, nil
	}
	r.node.noteCheckpointEnds([]proto.Session{{SessionID: "session-1", Cwd: cwds[0], Status: proto.StatusWorking}})
	r.node.noteCheckpointEnds([]proto.Session{{SessionID: "session-1", Cwd: cwds[0], Status: proto.StatusDone}})
	<-firstStarted
	r.node.noteCheckpointEnds([]proto.Session{{SessionID: "session-1", Cwd: cwds[1], Status: proto.StatusWorking}})
	r.node.noteCheckpointEnds([]proto.Session{{SessionID: "session-1", Cwd: cwds[1], Status: proto.StatusDone}})
	r.node.noteCheckpointEnds([]proto.Session{{SessionID: "session-1", Cwd: cwds[2], Status: proto.StatusWorking}})
	r.node.noteCheckpointEnds([]proto.Session{{SessionID: "session-1", Cwd: cwds[2], Status: proto.StatusDone}})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	waited := make(chan error, 1)
	go func() { waited <- r.node.waitCheckpoint(ctx, "session-1") }()
	select {
	case err := <-waited:
		t.Fatalf("wait returned before the capture was done: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-waited:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	mu.Lock()
	got := append([]string(nil), calls...)
	mu.Unlock()
	if !slices.Equal(got, cwds) {
		t.Fatalf("captures = %v, want FIFO queue %v", got, cwds)
	}
}

func TestCheckpointSendWaitsForThePreviousCapture(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
	started := make(chan struct{})
	release := make(chan struct{})
	r.node.checkpoints.capture = func(context.Context, string, string, int64) (checkpointResult, error) {
		close(started)
		<-release
		return checkpointResult{}, nil
	}
	r.node.noteCheckpointEnds([]proto.Session{{SessionID: "session-1", Cwd: r.dir, Status: proto.StatusWorking}})
	r.node.noteCheckpointEnds([]proto.Session{{SessionID: "session-1", Cwd: r.dir, Status: proto.StatusDone}})
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.node.send(ctx, "session-1", "next", nil, sendOpts{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("send error = %v, want the capture wait to stop it", err)
	}
	close(release)
}

func TestCheckpointSendContinuesAfterCapture(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
	r.node.hosts = host.NewRegistry()
	started := make(chan struct{})
	release := make(chan struct{})
	r.node.checkpoints.capture = func(context.Context, string, string, int64) (checkpointResult, error) {
		close(started)
		<-release
		return checkpointResult{}, nil
	}
	r.node.noteCheckpointEnds([]proto.Session{{SessionID: "session-1", Cwd: r.dir, Status: proto.StatusWorking}})
	r.node.noteCheckpointEnds([]proto.Session{{SessionID: "session-1", Cwd: r.dir, Status: proto.StatusDone}})
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.node.send(ctx, "session-1", "next", nil, sendOpts{}) }()
	select {
	case err := <-done:
		t.Fatalf("send returned before the capture finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("send did not continue after the capture: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("send did not continue: %v", ctx.Err())
	}
}

func TestCheckpointSendSucceedsAfterCapture(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
	n, f, _ := herdrNode(t)
	f.setScreen("w1:p1", cxEmpty)
	seedAgents(t, n, f, agentOf("codex", "session-1", proto.StatusIdle, "w1:p1", "w1:t1", "w1", r.dir, ""))
	started := make(chan struct{})
	release := make(chan struct{})
	n.checkpoints.capture = func(context.Context, string, string, int64) (checkpointResult, error) {
		close(started)
		<-release
		return checkpointResult{}, nil
	}
	n.noteCheckpointEnds([]proto.Session{{SessionID: "session-1", Cwd: r.dir, Status: proto.StatusWorking}})
	n.noteCheckpointEnds([]proto.Session{{SessionID: "session-1", Cwd: r.dir, Status: proto.StatusDone}})
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	params, err := json.Marshal(map[string]any{"session_id": "session-1", "text": "next"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := n.handle(ctx, "session.send", params)
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("send returned before the capture finished: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("send after capture: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("send did not finish: %v", ctx.Err())
	}
}

func TestCheckpointFirstDoneObservationIsCaptured(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
	r.node.state.put("session-1", sessionState{Cwd: r.dir})
	r.node.noteCheckpointEnds([]proto.Session{{SessionID: "session-1", Status: proto.StatusDone}})
	if refs := waitForCheckpointRefs(t, r, "session-1", 1); len(refs) != 1 {
		t.Fatalf("first done observation refs: %+v", refs)
	}
}

func TestCheckpointRefsSurviveANewNode(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
	first, err := r.node.captureCheckpoint(context.Background(), r.dir, "session-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	restarted := &Node{home: r.node.home, state: newState()}
	r.write("a.txt", "after restart\n")
	second, err := restarted.captureCheckpoint(context.Background(), r.dir, "session-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if second.Turn != 2 {
		t.Fatalf("turn after restart = %d, want 2", second.Turn)
	}
	got, err := restarted.gitReview(context.Background(), proto.GitReviewRequest{
		Cwd: r.dir, Scope: proto.ReviewTurn, Session: "session-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Base.Hash != first.Hash || got.Target == nil || got.Target.Hash != second.Hash {
		t.Fatalf("restarted turn review: %+v", got)
	}
}

func TestCheckpointCleansCrashedTempFiles(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
	gitDir := strings.TrimSpace(r.git("rev-parse", "--absolute-git-dir"))
	oldPID := os.Getpid() + 100000
	staleIndex := filepath.Join(gitDir, fmt.Sprintf("persona-checkpoint-index-%d-dead", oldPID))
	staleObjects := filepath.Join(gitDir, fmt.Sprintf("persona-checkpoint-objects-%d-dead", oldPID))
	currentIndex := filepath.Join(gitDir, fmt.Sprintf("persona-checkpoint-index-%d-live", os.Getpid()))
	if err := os.WriteFile(staleIndex, []byte("dead"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(staleObjects, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(currentIndex, []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanupCheckpointTemps(gitDir)
	if _, err := os.Stat(staleIndex); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale index remains: %v", err)
	}
	if _, err := os.Stat(staleObjects); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale objects remain: %v", err)
	}
	if _, err := os.Stat(currentIndex); err != nil {
		t.Fatalf("current process temp was removed: %v", err)
	}
}

func TestCheckpointRefFailureRemovesThePublishedPack(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
	r.write("a.txt", "changed\n")
	gitDir := strings.TrimSpace(r.git("rev-parse", "--absolute-git-dir"))
	common := strings.TrimSpace(r.git("rev-parse", "--git-common-dir"))
	if !filepath.IsAbs(common) {
		common = filepath.Join(r.dir, common)
	}
	packDir := filepath.Join(common, "objects", "pack")
	if err := os.MkdirAll(packDir, 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(packDir)
	if err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(gitDir, "refs", "persona", "checkpoints", "session-1", "1.lock")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.node.captureCheckpoint(context.Background(), r.dir, "session-1", 1); err == nil {
		t.Fatal("capture succeeded despite a locked ref")
	}
	if refs := checkpointRefsNow(t, r, "session-1"); len(refs) != 0 {
		t.Fatalf("failed ref write left refs: %+v", refs)
	}
	after, err := os.ReadDir(packDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("failed ref write left packs: before=%d after=%d", len(before), len(after))
	}
}

func TestCheckpointSameRepoTwoSessionsConcurrent(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
	r.write("a.txt", "changed\n")
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, session := range []string{"session-1", "session-2"} {
		wg.Add(1)
		go func(session string) {
			defer wg.Done()
			_, err := r.node.captureCheckpoint(context.Background(), r.dir, session, 1)
			errs <- err
		}(session)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent capture: %v", err)
		}
	}
	for _, session := range []string{"session-1", "session-2"} {
		if refs := checkpointRefsNow(t, r, session); len(refs) != 1 {
			t.Fatalf("%s refs: %+v", session, refs)
		}
	}
}

func TestCheckpointGCLockBlocksConcurrentGC(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
	gitDir := strings.TrimSpace(r.git("rev-parse", "--absolute-git-dir"))
	release, err := acquireCheckpointGCLock(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "-C", r.dir, "gc", "--prune=now")
	if err := cmd.Run(); err == nil {
		t.Fatal("git gc ran while the checkpoint lock was held")
	}
	release()
	if err := exec.Command("git", "-C", r.dir, "gc", "--prune=now").Run(); err != nil {
		t.Fatalf("git gc did not run after the lock was released: %v", err)
	}
}

func TestCheckpointCleanupKeepsALiveProcessTemp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep is not a portable test helper here")
	}
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = child.Process.Kill()
		_, _ = child.Process.Wait()
	}()
	r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
	gitDir := strings.TrimSpace(r.git("rev-parse", "--absolute-git-dir"))
	live := filepath.Join(gitDir, fmt.Sprintf("persona-checkpoint-index-%d-live", child.Process.Pid))
	if err := os.WriteFile(live, []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanupCheckpointTemps(gitDir)
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live process temp was removed: %v", err)
	}
}

func TestCheckpointLargeWorkingBlobIsCompared(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"a.txt": "one\n"})
	big := strings.Repeat("x", 33<<20)
	r.write("big.bin", big)
	r.git("add", "big.bin")
	r.git("commit", "-q", "-m", "add big")
	base := r.git("rev-parse", "HEAD")
	r.git("rm", "--cached", "-q", "big.bin")
	st, err := os.Lstat(filepath.Join(r.dir, "big.bin"))
	if err != nil {
		t.Fatal(err)
	}
	want, ok := checkpointBlob(context.Background(), r.dir, base, "big.bin")
	if !ok {
		t.Fatal("base blob not found")
	}
	got, err := workingBlob(context.Background(), r.dir, "big.bin", st)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("large working blob = %s, want %s", got, want)
	}
}

func TestGitReviewUncommittedKeepsCachedRemoval(t *testing.T) {
	r := newReviewRepo(t, map[string]string{"tracked.txt": "same\n"})
	r.git("rm", "--cached", "-q", "tracked.txt")
	status := r.git("status", "--porcelain")
	if !strings.Contains(status, "D  tracked.txt") || !strings.Contains(status, "?? tracked.txt") {
		t.Fatalf("git status = %q", status)
	}
	f := fileOf(t, r.review(proto.ReviewUncommitted, ""), "tracked.txt")
	if f.Status != "deleted" {
		t.Fatalf("uncommitted cached removal = %+v, want deleted", f)
	}
}
