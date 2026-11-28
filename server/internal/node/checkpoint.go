package node

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Working-tree checkpoints for a turn (#12533). A turn's end is captured in a hidden commit under
// refs/persona/checkpoints/<session>/<turn>; the end of turn N is the base of turn N+1. The capture uses a private
// index seeded from the real one (so Git's stat cache avoids re-reading unchanged files) and a private object
// directory that is published only once the snapshot is complete. The user's index, HEAD, branches, stash and working
// tree are never changed.

type checkpointLimits struct {
	timeout          time.Duration
	maxTracked       int
	disableAbove     int
	maxSnapshotBytes int64
	maxChangedBytes  int64
	keep             int
}

var checkpointDefaults = checkpointLimits{
	timeout:          2 * time.Second,
	maxTracked:       15000,
	disableAbove:     50000,
	maxSnapshotBytes: 250 << 20,
	maxChangedBytes:  20 << 20,
	keep:             20,
}

type checkpointTracker struct {
	mu      sync.Mutex
	status  map[string]string
	running map[string]bool
	pending map[string][]string
	waiters map[string][]chan struct{}
	repo    map[string]*sync.Mutex
	limits  checkpointLimits
	capture func(context.Context, string, string, int64) (checkpointResult, error)
}

type checkpointRef struct {
	Ref  string
	Hash string
	Turn int64
}

type checkpointResult struct {
	Ref  string
	Hash string
	Turn int64
}

type checkpointSkipError struct{ reason string }

func (e *checkpointSkipError) Error() string { return "checkpoint skipped: " + e.reason }

func checkpointSkipped(reason string) error { return &checkpointSkipError{reason: reason} }

func (n *Node) checkpointConfig() checkpointLimits {
	l := n.checkpoints.limits
	d := checkpointDefaults
	if l.timeout <= 0 {
		l.timeout = d.timeout
	}
	if l.maxTracked <= 0 {
		l.maxTracked = d.maxTracked
	}
	if l.disableAbove <= 0 {
		l.disableAbove = d.disableAbove
	}
	if l.maxSnapshotBytes <= 0 {
		l.maxSnapshotBytes = d.maxSnapshotBytes
	}
	if l.maxChangedBytes <= 0 {
		l.maxChangedBytes = d.maxChangedBytes
	}
	if l.keep <= 0 {
		l.keep = d.keep
	}
	return l
}

// noteCheckpointEnds follows the rows a session list just built. A turn that went from working or blocked to idle or
// done gets one asynchronous capture; the caller never waits on Git.
func (n *Node) noteCheckpointEnds(rows []proto.Session) {
	if len(rows) == 0 {
		return
	}
	type start struct {
		id, cwd string
	}
	var starts []start
	seen := make(map[string]bool, len(rows))
	n.checkpoints.mu.Lock()
	if n.checkpoints.status == nil {
		n.checkpoints.status = map[string]string{}
	}
	if n.checkpoints.running == nil {
		n.checkpoints.running = map[string]bool{}
	}
	if n.checkpoints.pending == nil {
		n.checkpoints.pending = map[string][]string{}
	}
	if n.checkpoints.repo == nil {
		n.checkpoints.repo = map[string]*sync.Mutex{}
	}
	// The status update and the running/pending reservation are one critical section: waitCheckpoint never observes a
	// turn as ended before its capture reservation exists.
	for _, row := range rows {
		if row.SessionID == "" || seen[row.SessionID] {
			continue
		}
		seen[row.SessionID] = true
		prev, had := n.checkpoints.status[row.SessionID]
		n.checkpoints.status[row.SessionID] = row.Status
		ended := had && checkpointTurnEnded(prev, row.Status)
		if !had {
			// A node that starts after the turn ended sees only done/idle. Capture that first observation too: it
			// establishes the base for the next turn instead of losing the turn entirely.
			ended = row.Status == proto.StatusDone || row.Status == proto.StatusIdle
		}
		cwd := n.checkpointRowCwd(row)
		if !ended || cwd == "" || !checkpointInRepo(cwd) {
			continue
		}
		if n.checkpoints.running[row.SessionID] {
			n.checkpoints.pending[row.SessionID] = append(n.checkpoints.pending[row.SessionID], cwd)
			continue
		}
		n.checkpoints.running[row.SessionID] = true
		starts = append(starts, start{id: row.SessionID, cwd: cwd})
	}
	n.checkpoints.mu.Unlock()

	for _, s := range starts {
		go n.captureTurnCheckpoint(s.id, s.cwd)
	}
}

func checkpointTurnEnded(prev, status string) bool {
	return (prev == proto.StatusWorking || prev == proto.StatusBlocked) &&
		(status == proto.StatusIdle || status == proto.StatusDone)
}

func (n *Node) checkpointRowCwd(row proto.Session) string {
	if row.Cwd != "" {
		return row.Cwd
	}
	if n.state != nil {
		if st, ok := n.state.get(row.SessionID); ok {
			return st.Cwd
		}
	}
	return ""
}

// checkpointInRepo avoids spawning Git for the many sessions whose folder is not a repository. A linked worktree has a
// .git file, and a session started in a subdirectory finds the repository above it.
func checkpointInRepo(cwd string) bool {
	cwd = filepath.Clean(cwd)
	for {
		if _, err := os.Stat(filepath.Join(cwd, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(cwd)
		if parent == cwd {
			return false
		}
		cwd = parent
	}
}

func (n *Node) captureTurnCheckpoint(session, cwd string) {
	for {
		capture := n.captureCheckpoint
		n.checkpoints.mu.Lock()
		if n.checkpoints.capture != nil {
			capture = n.checkpoints.capture
		}
		n.checkpoints.mu.Unlock()
		_, err := capture(context.Background(), cwd, session, 0)
		if err != nil {
			n.logf("checkpoint %s: %v", checkpointSessionLabel(session), err)
		}
		n.checkpoints.mu.Lock()
		if queue := n.checkpoints.pending[session]; len(queue) > 0 {
			next := queue[0]
			if len(queue) == 1 {
				delete(n.checkpoints.pending, session)
			} else {
				n.checkpoints.pending[session] = queue[1:]
			}
			n.checkpoints.mu.Unlock()
			cwd = next
			continue
		}
		delete(n.checkpoints.running, session)
		waiters := n.checkpoints.waiters[session]
		delete(n.checkpoints.waiters, session)
		for _, ch := range waiters {
			close(ch)
		}
		n.checkpoints.mu.Unlock()
		return
	}
}

// waitCheckpoint keeps the next turn from starting while the previous turn's capture is still reading the working tree.
func (n *Node) waitCheckpoint(ctx context.Context, session string) error {
	n.checkpoints.mu.Lock()
	if !n.checkpoints.running[session] {
		n.checkpoints.mu.Unlock()
		return nil
	}
	if n.checkpoints.waiters == nil {
		n.checkpoints.waiters = map[string][]chan struct{}{}
	}
	ch := make(chan struct{})
	n.checkpoints.waiters[session] = append(n.checkpoints.waiters[session], ch)
	n.checkpoints.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func checkpointSessionLabel(session string) string {
	if len(session) <= 12 {
		return session
	}
	return session[:12]
}

func checkpointErrReason(ctx context.Context, err error) string {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timed out"
	}
	return err.Error()
}

func splitNul(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\x00"), "\x00")
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// captureCheckpoint writes one snapshot commit for session. turn zero chooses the next turn after the refs already
// present. It returns a skip error for every expected refusal: outside Git, too large, too slow, or no index space.
func (n *Node) captureCheckpoint(ctx context.Context, cwd, session string, turn int64) (checkpointResult, error) {
	cfg := n.checkpointConfig()
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	fail := func(reason string) (checkpointResult, error) {
		return checkpointResult{}, checkpointSkipped(reason)
	}
	if strings.TrimSpace(session) == "" {
		return fail("empty session")
	}
	dir, err := n.gitDir(cwd)
	if err != nil {
		return fail(err.Error())
	}
	if _, err := os.Stat(dir); err != nil {
		return fail("folder is not there")
	}
	root, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		if ctx.Err() != nil {
			return fail("timed out")
		}
		return fail("not a Git repository")
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return fail("not a Git repository")
	}
	gitDir, err := git(ctx, root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	gitDir = strings.TrimSpace(gitDir)
	cleanupCheckpointTemps(gitDir)
	commonDir, err := git(ctx, root, "rev-parse", "--git-common-dir")
	if err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	commonDir = strings.TrimSpace(commonDir)
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(root, commonDir)
	}
	commonDir = filepath.Clean(commonDir)
	objectsDir := filepath.Join(commonDir, "objects")
	if st, err := os.Stat(objectsDir); err != nil || !st.IsDir() {
		return fail("Git object directory is not there")
	}

	head, _ := git(ctx, root, "rev-parse", "--verify", "-q", "HEAD")
	head = strings.TrimSpace(head)
	refs, err := checkpointRefs(ctx, root, session)
	if err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	if turn <= 0 {
		if len(refs) > 0 {
			turn = refs[len(refs)-1].Turn + 1
		} else {
			turn = 1
		}
	}

	trackedOut, err := git(ctx, root, "ls-files", "-z", "--cached")
	if err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	tracked := splitNul(trackedOut)
	if len(tracked) > cfg.disableAbove {
		return fail(fmt.Sprintf("repository too large (%d tracked files)", len(tracked)))
	}
	if len(tracked) > cfg.maxTracked {
		return fail(fmt.Sprintf("too many tracked files (%d > %d)", len(tracked), cfg.maxTracked))
	}
	allOut, err := git(ctx, root, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	snapshotBytes, err := pathBytes(ctx, root, splitNul(allOut))
	if err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	if snapshotBytes > cfg.maxSnapshotBytes {
		return fail(fmt.Sprintf("snapshot is too large (%d > %d bytes)", snapshotBytes, cfg.maxSnapshotBytes))
	}

	var changed []string
	if head == "" {
		changed = splitNul(allOut)
	} else {
		diffOut, err := git(ctx, root, "diff", "--name-only", "-z", "HEAD", "--")
		if err != nil {
			return fail(checkpointErrReason(ctx, err))
		}
		changed = splitNul(diffOut)
		othersOut, err := git(ctx, root, "ls-files", "-z", "--others", "--exclude-standard")
		if err != nil {
			return fail(checkpointErrReason(ctx, err))
		}
		changed = append(changed, splitNul(othersOut)...)
	}
	changedBytes, err := pathBytes(ctx, root, uniqueStrings(changed))
	if err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	if changedBytes > cfg.maxChangedBytes {
		return fail(fmt.Sprintf("changed content is too large (%d > %d bytes)", changedBytes, cfg.maxChangedBytes))
	}
	if err := ctx.Err(); err != nil {
		return fail("timed out")
	}

	indexPath, err := seedCheckpointIndex(gitDir)
	if err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	defer func() {
		_ = os.Remove(indexPath)
		_ = os.Remove(indexPath + ".lock")
	}()
	tmpObjects, err := os.MkdirTemp(gitDir, checkpointTempPrefix("objects"))
	if err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	defer os.RemoveAll(tmpObjects)
	if err := os.MkdirAll(filepath.Join(tmpObjects, "pack"), 0o700); err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	captureEnv := []string{
		"GIT_INDEX_FILE=" + indexPath,
		"GIT_OBJECT_DIRECTORY=" + tmpObjects,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + objectsDir,
	}
	if _, err := gitWithEnv(ctx, root, captureEnv, "add", "-A"); err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	tree, err := gitWithEnv(ctx, root, captureEnv, "write-tree")
	if err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	tree = strings.TrimSpace(tree)
	args := []string{"commit-tree", tree}
	if head != "" {
		args = append(args, "-p", head)
	}
	commitEnv := append(append([]string{}, captureEnv...),
		"GIT_AUTHOR_NAME=Persona Checkpoints",
		"GIT_AUTHOR_EMAIL=persona@localhost",
		"GIT_COMMITTER_NAME=Persona Checkpoints",
		"GIT_COMMITTER_EMAIL=persona@localhost",
	)
	commit, err := gitWithEnvInput(ctx, root, commitEnv, strings.NewReader(fmt.Sprintf("persona checkpoint: turn %d\n", turn)), args...)
	if err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return fail("commit-tree wrote no commit")
	}
	publishDir := filepath.Join(tmpObjects, "publish")
	if err := os.MkdirAll(publishDir, 0o700); err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	packPrefix := filepath.Join(publishDir, "pack")
	var revisions strings.Builder
	revisions.WriteString(commit + "\n")
	if head != "" {
		revisions.WriteString("^" + head + "\n")
	}
	packName, err := gitWithEnvInput(ctx, root, captureEnv, strings.NewReader(revisions.String()), "pack-objects", "--revs", "--delta-base-offset", packPrefix)
	if err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	packName = strings.TrimSpace(packName)
	if packName == "" {
		return fail("pack-objects wrote no pack")
	}
	tmpIdx := packPrefix + "-" + packName + ".idx"
	tmpPack := packPrefix + "-" + packName + ".pack"
	if _, err := os.Stat(tmpIdx); err != nil {
		return fail("pack index is not there")
	}
	if _, err := os.Stat(tmpPack); err != nil {
		return fail("pack is not there")
	}
	repoUnlock := n.checkpointRepoLock(commonDir)
	defer repoUnlock()
	gcLock, err := acquireCheckpointGCLock(commonDir)
	if err != nil {
		return fail(err.Error())
	}
	defer gcLock()
	mainPackDir := filepath.Join(objectsDir, "pack")
	if err := os.MkdirAll(mainPackDir, 0o755); err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	dstIdx := filepath.Join(mainPackDir, filepath.Base(tmpIdx))
	dstPack := filepath.Join(mainPackDir, filepath.Base(tmpPack))
	_, idxErr := os.Stat(dstIdx)
	_, packErr := os.Stat(dstPack)
	hadIdx, hadPack := idxErr == nil, packErr == nil
	if hadIdx != hadPack {
		return fail("published pack is incomplete")
	}
	published := !hadIdx
	if published {
		if err := os.Rename(tmpIdx, dstIdx); err != nil {
			return fail(checkpointErrReason(ctx, err))
		}
		if err := os.Rename(tmpPack, dstPack); err != nil {
			_ = os.Remove(dstIdx)
			return fail(checkpointErrReason(ctx, err))
		}
		defer func() {
			if published {
				_ = os.Remove(dstPack)
				_ = os.Remove(dstIdx)
			}
		}()
	}
	// Objects are in the repository before the ref exists: a reader can never resolve the ref to a missing blob. If
	// update-ref fails, the deferred cleanup removes the pack we published.
	if err := ctx.Err(); err != nil {
		return fail("timed out")
	}
	ref := checkpointRefName(session, turn)
	zero := strings.Repeat("0", len(commit))
	if _, err := gitWithEnv(ctx, root, nil, "update-ref", ref, commit, zero); err != nil {
		return fail(checkpointErrReason(ctx, err))
	}
	published = false
	if err := n.pruneCheckpointRefs(ctx, root, session, cfg.keep); err != nil {
		n.logf("checkpoint %s: prune: %v", checkpointSessionLabel(session), err)
	}
	return checkpointResult{Ref: ref, Hash: commit, Turn: turn}, nil
}

func pathBytes(ctx context.Context, root string, paths []string) (int64, error) {
	var total int64
	for i, p := range paths {
		if i%64 == 0 {
			if err := ctx.Err(); err != nil {
				return total, err
			}
		}
		if p == "" {
			continue
		}
		st, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return total, err
		}
		if st.IsDir() {
			continue
		}
		total += st.Size()
	}
	return total, nil
}

func seedCheckpointIndex(gitDir string) (string, error) {
	f, err := os.CreateTemp(gitDir, checkpointTempPrefix("index"))
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	src, err := os.Open(filepath.Join(gitDir, "index"))
	if errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(path)
		return path, nil
	}
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	dst, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		_ = os.Remove(path)
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return "", closeErr
	}
	_ = os.Chmod(path, info.Mode().Perm())
	_ = os.Chtimes(path, info.ModTime(), info.ModTime())
	return path, nil
}

func checkpointTempPrefix(kind string) string {
	return fmt.Sprintf("persona-checkpoint-%s-%d-", kind, os.Getpid())
}

// cleanupCheckpointTemps removes a crashed earlier node's private index/object directories in this repository. Names
// carry the PID so this process's concurrent captures are left alone.
func cleanupCheckpointTemps(gitDir string) {
	entries, err := os.ReadDir(gitDir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, e := range entries {
		name := e.Name()
		kind := ""
		for _, k := range []string{"index", "objects"} {
			if strings.HasPrefix(name, "persona-checkpoint-"+k+"-") {
				kind = k
				break
			}
		}
		if kind == "" {
			continue
		}
		rest := strings.TrimPrefix(name, "persona-checkpoint-"+kind+"-")
		pidText, _, ok := strings.Cut(rest, "-")
		if ok {
			if pid, err := strconv.Atoi(pidText); err == nil && pid == os.Getpid() {
				continue
			} else if err == nil && checkpointProcessAlive(pid) {
				continue
			}
		} else if info, err := e.Info(); err == nil && info.ModTime().After(now.Add(-time.Minute)) {
			continue // a name from before PIDs were added: give a live writer time to finish
		}
		_ = os.RemoveAll(filepath.Join(gitDir, name))
	}
}

// acquireCheckpointGCLock keeps an automatic or explicit git gc from pruning the pack between its publication and the
// checkpoint ref. Git's own gc.pid check makes a concurrent gc refuse; the lock is removed as soon as the ref exists.
func acquireCheckpointGCLock(gitDir string) (func(), error) {
	path := filepath.Join(gitDir, "gc.pid")
	host, _ := os.Hostname()
	content := fmt.Sprintf("%d %s\n", os.Getpid(), host)
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, writeErr := f.WriteString(content); writeErr != nil {
				_ = f.Close()
				_ = os.Remove(path)
				return nil, writeErr
			}
			if closeErr := f.Close(); closeErr != nil {
				_ = os.Remove(path)
				return nil, closeErr
			}
			return func() {
				if data, err := os.ReadFile(path); err == nil && string(data) == content {
					_ = os.Remove(path)
				}
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		data, readErr := os.ReadFile(path)
		fields := strings.Fields(string(data))
		if readErr == nil && len(fields) > 0 {
			if pid, parseErr := strconv.Atoi(fields[0]); parseErr == nil && checkpointProcessAlive(pid) {
				return nil, fmt.Errorf("git gc is already running")
			}
			if len(fields) > 1 && fields[1] != host {
				if info, statErr := os.Stat(path); statErr == nil && info.ModTime().After(time.Now().Add(-12*time.Hour)) {
					return nil, fmt.Errorf("git gc is already running")
				}
			}
		}
		if info, statErr := os.Stat(path); statErr == nil && info.ModTime().After(time.Now().Add(-12*time.Hour)) && len(fields) == 0 {
			return nil, fmt.Errorf("git gc is already running")
		}
		_ = os.Remove(path)
	}
	return nil, fmt.Errorf("could not acquire the git gc lock")
}

func (n *Node) checkpointRepoLock(repo string) func() {
	n.checkpoints.mu.Lock()
	if n.checkpoints.repo == nil {
		n.checkpoints.repo = map[string]*sync.Mutex{}
	}
	lock := n.checkpoints.repo[repo]
	if lock == nil {
		lock = &sync.Mutex{}
		n.checkpoints.repo[repo] = lock
	}
	n.checkpoints.mu.Unlock()
	lock.Lock()
	return lock.Unlock
}

func checkpointRefs(ctx context.Context, root, session string) ([]checkpointRef, error) {
	prefix := "refs/persona/checkpoints/" + checkpointSessionComponent(session) + "/"
	out, err := git(ctx, root, "for-each-ref", "--format=%(refname) %(objectname)", prefix)
	if err != nil {
		return nil, err
	}
	var refs []checkpointRef
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		turn, err := strconv.ParseInt(filepath.Base(f[0]), 10, 64)
		if err != nil || turn <= 0 {
			continue
		}
		refs = append(refs, checkpointRef{Ref: f[0], Hash: f[1], Turn: turn})
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Turn == refs[j].Turn {
			return refs[i].Ref < refs[j].Ref
		}
		return refs[i].Turn < refs[j].Turn
	})
	return refs, nil
}

func (n *Node) pruneCheckpointRefs(ctx context.Context, root, session string, keep int) error {
	refs, err := checkpointRefs(ctx, root, session)
	if err != nil {
		return err
	}
	if keep <= 0 {
		keep = checkpointDefaults.keep
	}
	if len(refs) <= keep {
		return nil
	}
	for _, r := range refs[:len(refs)-keep] {
		if _, err := gitWithEnv(ctx, root, nil, "update-ref", "-d", r.Ref); err != nil {
			return err
		}
	}
	return nil
}

func checkpointRefName(session string, turn int64) string {
	return fmt.Sprintf("refs/persona/checkpoints/%s/%d", checkpointSessionComponent(session), turn)
}

func checkpointSessionComponent(session string) string {
	if checkpointRefPart(session) {
		return session
	}
	sum := sha256.Sum256([]byte(session))
	return "s-" + hex.EncodeToString(sum[:8])
}

func checkpointRefPart(s string) bool {
	if s == "" || len(s) > 120 || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") || strings.HasSuffix(s, ".lock") || strings.Contains(s, "..") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

func gitWithEnv(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	return gitWithEnvInput(ctx, dir, env, nil, args...)
}

func gitWithEnvInput(ctx context.Context, dir string, env []string, in io.Reader, args ...string) (string, error) {
	cmd := gitCmd(ctx, dir, args...)
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = in
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), gitFailed(ctx, args, errb.String(), err)
	}
	return out.String(), nil
}
