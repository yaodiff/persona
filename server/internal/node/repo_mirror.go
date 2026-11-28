package node

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/link"
	repository "github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
)

func init() {
	nodeRPC(repository.MethodRepoMirrorEnsure, (*Node).repoMirrorEnsureRPC)
	nodeRPC(repository.MethodRepoMirrorStatus, (*Node).repoMirrorStatusRPC)
	nodeRPC(repository.MethodRepoMirrorRemove, (*Node).repoMirrorRemoveRPC)
}

const (
	repoMirrorFetchTimeout = 2 * time.Minute
	repoMirrorReadTimeout  = 15 * time.Second
)

// repoMirrorFlight is an in-flight ensure operation shared by coalesced callers for one repo_id.
type repoMirrorFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	force   bool
	waiters int
	info    repository.MirrorInfo
	err     error
}

// repoMirrorManager holds per-Node single-flight and per-repo coordination for ~/.persona/repos/.
// Its zero value is ready to use.
type repoMirrorManager struct {
	mu      sync.Mutex
	flights map[string]*repoMirrorFlight
	// removing tracks repos currently being removed so ensure waits until removal finishes.
	removing map[string]chan struct{}

	// Test hooks (nil in production).
	now          func() time.Time
	fetchTimeout time.Duration // Overrides both fetch budgets.
	beforeFetch  func(ctx context.Context, repoID string) error
}

type repoMirrorMeta struct {
	SchemaVersion int                 `json:"schema_version"`
	RepoID        string              `json:"repo_id"`
	Identity      repository.Identity `json:"identity"`
	CloneURL      string              `json:"clone_url"`
	DefaultRef    string              `json:"default_ref,omitempty"`
	HeadCommit    string              `json:"head_commit,omitempty"`
	LastSyncedAt  time.Time           `json:"last_synced_at"`
}

func (m *repoMirrorManager) clock() time.Time {
	if m.now != nil {
		return m.now().UTC()
	}
	return time.Now().UTC()
}

func (m *repoMirrorManager) timeout(cold bool) time.Duration {
	if m.fetchTimeout > 0 {
		return m.fetchTimeout
	}
	if cold {
		return repository.MirrorColdFetchTimeout
	}
	return repoMirrorFetchTimeout
}

func (n *Node) repoMirrorRoot() (string, error) {
	return n.workspaceControlDir("repos")
}

func (n *Node) repoMirrorPaths(repoID string) (root, mirrorDir, metaPath, lockPath, hooksDir string, err error) {
	if !repository.ValidateMirrorRepoID(repoID) {
		return "", "", "", "", "", errors.New("invalid repository id")
	}
	root, err = n.repoMirrorRoot()
	if err != nil {
		return "", "", "", "", "", err
	}
	mirrorDir = filepath.Join(root, repoID+".git")
	metaPath = filepath.Join(root, repoID+".meta.json")
	lockPath = filepath.Join(root, repoID+".lock")
	hooksDir = filepath.Join(root, ".hooks")
	for _, p := range []string{mirrorDir, metaPath, lockPath, hooksDir} {
		if workspaceNoLinks(p) != nil {
			return "", "", "", "", "", errors.New("mirror path unavailable")
		}
	}
	return root, mirrorDir, metaPath, lockPath, hooksDir, nil
}

// repoMirrorSourceAllowed validates CloneURL and Identity with the same rules as backend workspace preparation.
func (n *Node) repoMirrorSourceAllowed(identity repository.Identity, cloneURL string) bool {
	if cloneURL == "" || len(cloneURL) > 4096 || strings.ContainsFunc(cloneURL, unicode.IsControl) {
		return false
	}
	if repository.IsExtensionProvider(identity.Provider) {
		u, err := url.Parse(identity.InstanceURL)
		if err != nil || u.Hostname() == "" {
			return false
		}
		checked, err := repository.CheckParseAnswer(repository.ProviderInfo{ID: identity.Provider, Hosts: []string{u.Hostname()}},
			repository.ParseAnswer{InstanceURL: identity.InstanceURL, Path: identity.Path, CanonicalURL: identity.CanonicalURL})
		if err != nil || checked.Identity != identity {
			return false
		}
	} else {
		parsed, err := repository.Parse(identity.CanonicalURL, identity.Provider)
		if err != nil || parsed != identity {
			return false
		}
	}
	local := cloneURL
	if strings.HasPrefix(cloneURL, "file://") {
		u, err := url.Parse(cloneURL)
		if err != nil || u.Host != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return false
		}
		local = u.Path
	}
	if filepath.IsAbs(local) {
		local = n.resolveHomeAlias(local)
		return n.offLimits(local) == nil && workspaceNoLinks(local) == nil
	}
	_, err := repository.CloneTransport(cloneURL, identity)
	return err == nil
}

func (n *Node) repoMirrorEnsureRPC(ctx context.Context, req repository.MirrorEnsureRequest) (repository.MirrorInfo, error) {
	return n.ensureRepoMirror(ctx, req)
}

func (n *Node) repoMirrorStatusRPC(ctx context.Context, req repository.MirrorStatusRequest) (repository.MirrorStatusResponse, error) {
	return n.repoMirrorStatus(ctx, req)
}

func (n *Node) repoMirrorRemoveRPC(ctx context.Context, req repository.MirrorRemoveRequest) (repository.MirrorRemoveResponse, error) {
	return n.removeRepoMirror(ctx, req)
}

// ensureRepoMirror ensures ~/.persona/repos/<repo_id>.git exists and is synced within TTL.
// Concurrent requests for the same repo_id coalesce via single-flight locking.
func (n *Node) ensureRepoMirror(ctx context.Context, req repository.MirrorEnsureRequest) (repository.MirrorInfo, error) {
	if !repository.ValidateMirrorRepoID(req.RepoID) {
		return repository.MirrorInfo{}, errors.New("invalid repository id")
	}
	ttlSeconds, err := repository.NormalizeMirrorTTLSeconds(req.TTLSeconds)
	if err != nil {
		return repository.MirrorInfo{}, errors.New("invalid ttl_seconds")
	}
	if !n.repoMirrorSourceAllowed(req.Identity, req.CloneURL) {
		return repository.MirrorInfo{}, errors.New("invalid repository source")
	}

	m := &n.repoMirrors
	for {
		m.mu.Lock()
		if remDone, removing := m.removing[req.RepoID]; removing {
			m.mu.Unlock()
			select {
			case <-remDone:
				continue
			case <-ctx.Done():
				return repository.MirrorInfo{}, ctx.Err()
			}
		}
		if f, exists := m.flights[req.RepoID]; exists {
			// If caller needs a forced fetch and the running flight is non-force, wait for the
			// running flight to complete and then start a fresh forced flight.
			if req.Force && !f.force {
				done := f.done
				m.mu.Unlock()
				select {
				case <-done:
					continue
				case <-ctx.Done():
					return repository.MirrorInfo{}, ctx.Err()
				}
			}
			f.waiters++
			m.mu.Unlock()
			return waitRepoMirrorFlight(ctx, m, req.RepoID, f, true)
		}
		if m.flights == nil {
			m.flights = map[string]*repoMirrorFlight{}
		}
		// Bound the whole shared flight, including time queued for network Git.
		// Individual commands get their cold or warm budgets after admission.
		flightCtx, cancel := context.WithTimeout(context.Background(), repository.MirrorColdFetchTimeout)
		f := &repoMirrorFlight{
			done:    make(chan struct{}),
			cancel:  cancel,
			force:   req.Force,
			waiters: 1,
		}
		m.flights[req.RepoID] = f
		m.mu.Unlock()

		go func() {
			defer cancel()
			info, runErr := n.runRepoMirrorEnsure(flightCtx, req, time.Duration(ttlSeconds)*time.Second)
			m.mu.Lock()
			f.info = info
			f.err = runErr
			delete(m.flights, req.RepoID)
			close(f.done)
			m.mu.Unlock()
		}()

		return waitRepoMirrorFlight(ctx, m, req.RepoID, f, false)
	}
}

func waitRepoMirrorFlight(ctx context.Context, m *repoMirrorManager, repoID string, f *repoMirrorFlight, coalesced bool) (repository.MirrorInfo, error) {
	select {
	case <-f.done:
		info := f.info
		if coalesced {
			info.Coalesced = true
		}
		return info, f.err
	case <-ctx.Done():
		m.mu.Lock()
		f.waiters--
		if f.waiters <= 0 {
			f.cancel()
		}
		m.mu.Unlock()
		return repository.MirrorInfo{}, ctx.Err()
	}
}

func (n *Node) runRepoMirrorEnsure(ctx context.Context, req repository.MirrorEnsureRequest, ttl time.Duration) (repository.MirrorInfo, error) {
	root, mirrorDir, metaPath, lockPath, hooksDir, err := n.repoMirrorPaths(req.RepoID)
	if err != nil {
		return repository.MirrorInfo{}, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return repository.MirrorInfo{}, err
	}
	if err := os.MkdirAll(hooksDir, 0700); err != nil {
		return repository.MirrorInfo{}, err
	}
	lockFile, err := link.LockFileHandle(lockPath, true)
	if err != nil {
		return repository.MirrorInfo{}, err
	}
	defer lockFile.Close()

	now := n.repoMirrors.clock()
	meta, hasMeta := readRepoMirrorMeta(metaPath)
	exists := isValidBareRepo(ctx, mirrorDir)
	if err := ctx.Err(); err != nil {
		return repository.MirrorInfo{}, err
	}

	// Re-initialize cleanly if the bare repo is corrupt or belongs to a different identity.
	if exists && hasMeta && meta.Identity != (repository.Identity{}) && meta.Identity != req.Identity {
		if err := os.RemoveAll(mirrorDir); err != nil {
			return repository.MirrorInfo{}, err
		}
		_ = os.Remove(metaPath)
		exists = false
		hasMeta = false
	}

	// Short-circuit within TTL when the mirror is already initialized for the same CloneURL.
	if exists && hasMeta && !req.Force && meta.CloneURL == req.CloneURL && !meta.LastSyncedAt.IsZero() && now.Sub(meta.LastSyncedAt) < ttl {
		diskBytes := dirDiskBytes(mirrorDir)
		syncedAt := meta.LastSyncedAt
		return repository.MirrorInfo{
			RepoID:        req.RepoID,
			Path:          mirrorDir,
			Exists:        true,
			CloneURL:      meta.CloneURL,
			DefaultRef:    meta.DefaultRef,
			HeadCommit:    meta.HeadCommit,
			LastSyncedAt:  &syncedAt,
			DiskBytes:     diskBytes,
			Fetched:       false,
			SkippedReason: "ttl_fresh",
		}, nil
	}

	timeout := n.repoMirrors.timeout(!exists)
	if !exists {
		if err := os.RemoveAll(mirrorDir); err != nil && !errors.Is(err, os.ErrNotExist) {
			return repository.MirrorInfo{}, err
		}
		if err := n.initRepoMirror(ctx, req, timeout, root, mirrorDir, hooksDir); err != nil {
			return repository.MirrorInfo{}, err
		}
	} else {
		if err := n.fetchRepoMirror(ctx, req, timeout, mirrorDir, hooksDir); err != nil {
			return repository.MirrorInfo{}, err
		}
	}

	defaultRef, headCommit := inspectRepoMirrorHead(ctx, mirrorDir)
	if err := ctx.Err(); err != nil {
		return repository.MirrorInfo{}, err
	}
	syncedAt := n.repoMirrors.clock()
	nextMeta := repoMirrorMeta{
		SchemaVersion: 1,
		RepoID:        req.RepoID,
		Identity:      req.Identity,
		CloneURL:      req.CloneURL,
		DefaultRef:    defaultRef,
		HeadCommit:    headCommit,
		LastSyncedAt:  syncedAt,
	}
	if err := workspaceWrite(metaPath, &nextMeta); err != nil {
		return repository.MirrorInfo{}, err
	}
	diskBytes := dirDiskBytes(mirrorDir)
	return repository.MirrorInfo{
		RepoID:       req.RepoID,
		Path:         mirrorDir,
		Exists:       true,
		CloneURL:     req.CloneURL,
		DefaultRef:   defaultRef,
		HeadCommit:   headCommit,
		LastSyncedAt: &syncedAt,
		DiskBytes:    diskBytes,
		Fetched:      true,
	}, nil
}

func (n *Node) initRepoMirror(ctx context.Context, req repository.MirrorEnsureRequest, timeout time.Duration, root, mirrorDir, hooksDir string) error {
	var rnd [8]byte
	_, _ = rand.Read(rnd[:])
	staging := filepath.Join(root, ".staging-"+hex.EncodeToString(rnd[:])+".git")
	if err := os.Mkdir(staging, 0700); err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(staging)
		}
	}()

	defaultRef, advertised, err := n.lsRemoteDefaultHead(ctx, req, timeout, staging, hooksDir)
	if err != nil {
		return fmt.Errorf("mirror source unavailable: %w", err)
	}
	objectFormat := "sha1"
	if len(advertised) == 64 {
		objectFormat = "sha256"
	}
	if _, err := runLocalMirrorGit(ctx, staging, hooksDir, "init", "--bare", "--object-format="+objectFormat, "."); err != nil {
		return errors.New("mirror init failed")
	}
	if _, err := runLocalMirrorGit(ctx, staging, hooksDir, "config", "remote.origin.url", req.CloneURL); err != nil {
		return errors.New("mirror config failed")
	}
	if _, err := runLocalMirrorGit(ctx, staging, hooksDir, "config", "remote.origin.fetch", "+refs/heads/*:refs/heads/*"); err != nil {
		return errors.New("mirror config failed")
	}
	if _, err := n.runMirrorSourceGit(ctx, req, timeout, staging, hooksDir, "fetch", "--prune", "--tags", "--no-recurse-submodules", "origin"); err != nil {
		return err
	}
	if defaultRef != "" {
		_, _ = runLocalMirrorGit(ctx, staging, hooksDir, "symbolic-ref", "HEAD", defaultRef)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(staging, mirrorDir); err != nil {
		return err
	}
	cleanup = false
	_ = workspaceSyncDir(root)
	return nil
}

func (n *Node) fetchRepoMirror(ctx context.Context, req repository.MirrorEnsureRequest, timeout time.Duration, mirrorDir, hooksDir string) error {
	for _, sub := range []string{filepath.Join(mirrorDir, "config"), filepath.Join(mirrorDir, "objects"), filepath.Join(mirrorDir, "refs")} {
		if workspaceNoLinks(sub) != nil {
			return errors.New("mirror layout invalid")
		}
	}
	if _, err := runLocalMirrorGit(ctx, mirrorDir, hooksDir, "config", "remote.origin.url", req.CloneURL); err != nil {
		return errors.New("mirror config failed")
	}
	if _, err := runLocalMirrorGit(ctx, mirrorDir, hooksDir, "config", "remote.origin.fetch", "+refs/heads/*:refs/heads/*"); err != nil {
		return errors.New("mirror config failed")
	}
	if _, err := n.runMirrorSourceGit(ctx, req, timeout, mirrorDir, hooksDir, "fetch", "--prune", "--tags", "--no-recurse-submodules", "origin"); err != nil {
		return err
	}
	// If HEAD no longer resolves to a valid commit (for example the upstream default branch changed),
	// re-discover HEAD from the remote.
	if _, err := workspaceReadGit(ctx, mirrorDir, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		defaultRef, _, lsErr := n.lsRemoteDefaultHead(ctx, req, timeout, mirrorDir, hooksDir)
		if err := ctx.Err(); err != nil {
			return err
		}
		if lsErr == nil && defaultRef != "" {
			_, _ = runLocalMirrorGit(ctx, mirrorDir, hooksDir, "symbolic-ref", "HEAD", defaultRef)
		}
	}
	return nil
}

func (n *Node) lsRemoteDefaultHead(ctx context.Context, req repository.MirrorEnsureRequest, timeout time.Duration, dir, hooksDir string) (defaultRef, advertised string, err error) {
	listing, err := n.runMirrorSourceGit(ctx, req, timeout, dir, hooksDir, "ls-remote", "--symref", "--", req.CloneURL, "HEAD")
	if err != nil {
		return "", "", err
	}
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "HEAD" {
			advertised = fields[0]
		}
		if len(fields) == 3 && fields[0] == "ref:" && fields[2] == "HEAD" && strings.HasPrefix(fields[1], "refs/heads/") {
			defaultRef = fields[1]
		}
	}
	return defaultRef, advertised, nil
}

func (n *Node) runMirrorSourceGit(ctx context.Context, req repository.MirrorEnsureRequest, timeout time.Duration, dir, hooksDir, verb string, args ...string) (string, error) {
	return n.remoteFetches.run(ctx, req.CloneURL, func(ctx context.Context) (string, error) {
		cmdCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if hook := n.repoMirrors.beforeFetch; hook != nil && verb == "fetch" {
			if err := hook(cmdCtx, req.RepoID); err != nil {
				return "", err
			}
		}
		out, err := runMirrorGit(cmdCtx, dir, hooksDir, append([]string{verb}, args...)...)
		if err != nil {
			if ctxErr := cmdCtx.Err(); ctxErr != nil {
				return "", ctxErr
			}
			return "", fmt.Errorf("mirror %s failed", verb)
		}
		return out, nil
	})
}

func runLocalMirrorGit(ctx context.Context, dir, hooksDir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, taskworkspace.GitTimeout)
	defer cancel()
	return runMirrorGit(ctx, dir, hooksDir, args...)
}

func runMirrorGit(ctx context.Context, dir, hooksDir string, args ...string) (string, error) {
	base := []string{"-C", dir, "--no-pager", "-c", "core.hooksPath=" + hooksDir, "-c", "core.quotepath=off", "-c", "core.fsmonitor=false", "-c", "gc.auto=0"}
	cmd := exec.CommandContext(ctx, "git", append(base, args...)...)
	cmd.Env = taskworkspace.GitEnv(os.Environ())
	cmd.WaitDelay = time.Second
	stopGitTransportOnCancel(cmd, args)
	var out workspaceOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if out.overflow {
		return "", errors.New("output limit")
	}
	return strings.TrimSpace(out.String()), err
}

func isValidBareRepo(ctx context.Context, mirrorDir string) bool {
	st, err := os.Lstat(mirrorDir)
	if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if workspaceNoLinks(mirrorDir) != nil {
		return false
	}
	out, err := workspaceReadGit(ctx, mirrorDir, "rev-parse", "--is-bare-repository")
	return err == nil && out == "true"
}

func inspectRepoMirrorHead(ctx context.Context, mirrorDir string) (defaultRef, headCommit string) {
	readCtx, cancel := context.WithTimeout(ctx, repoMirrorReadTimeout)
	defer cancel()
	if sym, err := workspaceReadGit(readCtx, mirrorDir, "symbolic-ref", "--quiet", "HEAD"); err == nil {
		defaultRef = sym
	}
	if commit, err := workspaceReadGit(readCtx, mirrorDir, "rev-parse", "--verify", "HEAD^{commit}"); err == nil {
		headCommit = commit
	}
	return defaultRef, headCommit
}

func readRepoMirrorMeta(path string) (repoMirrorMeta, bool) {
	if workspaceNoLinks(path) != nil {
		return repoMirrorMeta{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return repoMirrorMeta{}, false
	}
	var meta repoMirrorMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return repoMirrorMeta{}, false
	}
	return meta, true
}

func dirDiskBytes(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&os.ModeSymlink != 0 || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

func (n *Node) repoMirrorStatus(ctx context.Context, req repository.MirrorStatusRequest) (repository.MirrorStatusResponse, error) {
	if req.RepoID != "" {
		info, err := n.inspectSingleRepoMirror(ctx, req.RepoID)
		if err != nil {
			return repository.MirrorStatusResponse{}, err
		}
		return repository.MirrorStatusResponse{
			Mirror:         &info,
			TotalDiskBytes: info.DiskBytes,
		}, nil
	}
	root, err := n.repoMirrorRoot()
	if err != nil {
		return repository.MirrorStatusResponse{}, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return repository.MirrorStatusResponse{Mirrors: []repository.MirrorInfo{}}, nil
	}
	if err != nil {
		return repository.MirrorStatusResponse{}, err
	}
	mirrors := make([]repository.MirrorInfo, 0)
	var total int64
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".git") || strings.HasPrefix(name, ".") {
			continue
		}
		repoID := strings.TrimSuffix(name, ".git")
		if !repository.ValidateMirrorRepoID(repoID) {
			continue
		}
		info, err := n.inspectSingleRepoMirror(ctx, repoID)
		if err != nil || !info.Exists {
			continue
		}
		mirrors = append(mirrors, info)
		total += info.DiskBytes
	}
	slices.SortFunc(mirrors, func(a, b repository.MirrorInfo) int { return strings.Compare(a.RepoID, b.RepoID) })
	return repository.MirrorStatusResponse{
		Mirrors:        mirrors,
		TotalDiskBytes: total,
	}, nil
}

func (n *Node) inspectSingleRepoMirror(ctx context.Context, repoID string) (repository.MirrorInfo, error) {
	_, mirrorDir, metaPath, _, _, err := n.repoMirrorPaths(repoID)
	if err != nil {
		return repository.MirrorInfo{}, err
	}
	if !isValidBareRepo(ctx, mirrorDir) {
		return repository.MirrorInfo{RepoID: repoID, Exists: false}, nil
	}
	meta, hasMeta := readRepoMirrorMeta(metaPath)
	defaultRef, headCommit := inspectRepoMirrorHead(ctx, mirrorDir)
	if defaultRef == "" && hasMeta {
		defaultRef = meta.DefaultRef
	}
	if headCommit == "" && hasMeta {
		headCommit = meta.HeadCommit
	}
	info := repository.MirrorInfo{
		RepoID:     repoID,
		Path:       mirrorDir,
		Exists:     true,
		CloneURL:   meta.CloneURL,
		DefaultRef: defaultRef,
		HeadCommit: headCommit,
		DiskBytes:  dirDiskBytes(mirrorDir),
	}
	if hasMeta && !meta.LastSyncedAt.IsZero() {
		synced := meta.LastSyncedAt
		info.LastSyncedAt = &synced
	}
	return info, nil
}

func (n *Node) removeRepoMirror(ctx context.Context, req repository.MirrorRemoveRequest) (repository.MirrorRemoveResponse, error) {
	_, mirrorDir, metaPath, lockPath, _, err := n.repoMirrorPaths(req.RepoID)
	if err != nil {
		return repository.MirrorRemoveResponse{}, err
	}
	m := &n.repoMirrors
	for {
		m.mu.Lock()
		if f, exists := m.flights[req.RepoID]; exists {
			done := f.done
			m.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return repository.MirrorRemoveResponse{}, ctx.Err()
			}
		}
		if remDone, removing := m.removing[req.RepoID]; removing {
			m.mu.Unlock()
			select {
			case <-remDone:
				continue
			case <-ctx.Done():
				return repository.MirrorRemoveResponse{}, ctx.Err()
			}
		}
		if m.removing == nil {
			m.removing = map[string]chan struct{}{}
		}
		remDone := make(chan struct{})
		m.removing[req.RepoID] = remDone
		m.mu.Unlock()

		defer func() {
			m.mu.Lock()
			delete(m.removing, req.RepoID)
			close(remDone)
			m.mu.Unlock()
		}()
		break
	}

	if _, err := os.Lstat(mirrorDir); errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(metaPath)
		_ = os.Remove(lockPath)
		return repository.MirrorRemoveResponse{RepoID: req.RepoID, Removed: false, FreedBytes: 0}, nil
	}
	// Hold the OS file lock while removing so no concurrent process touches the mirror.
	lockFile, err := link.LockFileHandle(lockPath, true)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return repository.MirrorRemoveResponse{}, err
	}
	if lockFile != nil {
		defer func() {
			lockFile.Close()
			_ = os.Remove(lockPath)
		}()
	}

	freed := dirDiskBytes(mirrorDir)
	if err := os.RemoveAll(mirrorDir); err != nil {
		return repository.MirrorRemoveResponse{}, err
	}
	_ = os.Remove(metaPath)
	return repository.MirrorRemoveResponse{
		RepoID:     req.RepoID,
		Removed:    true,
		FreedBytes: freed,
	}, nil
}
