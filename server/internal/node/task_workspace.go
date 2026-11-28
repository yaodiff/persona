package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

type workspaceJournal struct {
	taskworkspace.Record
	Identities           map[string]string `json:"identities"`
	Tombstones           []string          `json:"tombstones"`
	InFlight             bool              `json:"in_flight"`
	LocalGitIdentity     string            `json:"local_git_identity,omitempty"`
	LastGitStderrSummary string            `json:"last_git_stderr_summary,omitempty"`
}
type workspaceExecutor struct {
	n                *Node
	journal          workspaceJournal
	path             string
	locks            []*os.File
	mirrorRepos      []string
	mirrorTTLSeconds int
	coldFetchTimeout time.Duration
}

var errWorkspaceSymlink = errors.New("symbolic link")

func workspaceNoLinks(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("invalid path")
	}
	next := filepath.VolumeName(path) + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(path, next), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		next = filepath.Join(next, part)
		st, err := os.Lstat(next)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return errWorkspaceSymlink
		}
	}
	return nil
}
func workspaceWrite(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".receipt-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err == nil {
		err = workspaceSyncDir(filepath.Dir(path))
	}
	return err
}
func (e *workspaceExecutor) save() error {
	e.journal.Revision++
	return workspaceWrite(e.path, &e.journal)
}
func (e *workspaceExecutor) fail(code, stage, repo, path string) taskworkspace.Record {
	p := taskworkspace.Failure(code, stage, e.journal.OperationID, repo, path)
	e.journal.Error = p
	if e.journal.State != "cancelled" && e.journal.State != "removed" {
		e.journal.State = "failed"
	}
	for i := range e.journal.Repositories {
		if e.journal.Repositories[i].RepoID == repo {
			e.journal.Repositories[i].Error = p
		}
	}
	return e.journal.Record
}
func (e *workspaceExecutor) owned(path string) bool {
	id, err := workspaceFileID(path)
	return err == nil && e.journal.Identities[path] != "" && id == e.journal.Identities[path] && workspaceNoLinks(path) == nil
}
func (e *workspaceExecutor) claimDir(path string) error {
	if e.owned(path) {
		return nil
	}
	if workspaceNoLinks(path) != nil {
		return errors.New("invalid path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil {
		return err
	}
	id, err := workspaceFileID(path)
	if err != nil {
		return err
	}
	e.journal.Identities[path] = id
	return e.save()
}

// Each command is bounded, noninteractive, and stores no raw stderr or URL in an
// error. The inherited locks outlive a crashed Node while Git is still running.
func (e *workspaceExecutor) git(ctx context.Context, dir string, args ...string) (string, error) {
	return e.gitWithTimeout(ctx, taskworkspace.GitTimeout, dir, args...)
}

func (e *workspaceExecutor) gitWithTimeout(ctx context.Context, timeout time.Duration, dir string, args ...string) (string, error) {
	e.journal.InFlight = true
	if err := e.save(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "--no-pager", "-c", "core.hooksPath=" + filepath.Join(filepath.Dir(e.path), "hooks"), "-c", "core.quotepath=off", "-c", "core.fsmonitor=false", "-c", "gc.auto=0"}, args...)...)
	cmd.Env = taskworkspace.GitEnv(os.Environ())
	if e.journal.LocalGitIdentity != "" {
		cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=")
	}
	cmd.WaitDelay = time.Second
	stopGitTransportOnCancel(cmd, args)
	workspaceInheritLocks(cmd, e.locks)
	var out workspaceOutput
	stderr := keptOutput{tailMost: workspaceGitStderrTail}
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil && ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		e.journal.LastGitStderrSummary = workspaceGitStderrSummary(stderr.String())
	}
	e.journal.InFlight = false
	if saveErr := e.save(); saveErr != nil {
		return "", saveErr
	}
	if out.overflow {
		if err == nil {
			err = errors.New("output limit")
		}
		return "", err
	}
	return strings.TrimSpace(out.String()), err
}

func stopGitTransportOnCancel(cmd *exec.Cmd, args []string) {
	if len(args) == 0 || args[0] != "fetch" && args[0] != "ls-remote" {
		return
	}
	// Stop the transport helper and pack writer before returning the slot.
	cmd.SysProcAttr = osproc.GroupAttr()
	cmd.Cancel = func() error {
		if err := osproc.StopGroup(cmd.Process.Pid, true); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}

type workspaceOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *workspaceOutput) Write(p []byte) (int, error) {
	n := len(p)
	left := (1 << 20) - b.Len()
	if n > left {
		b.overflow = true
		p = p[:max(0, left)]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func workspaceReadGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir, "--no-pager", "-c", "core.quotepath=off", "-c", "core.fsmonitor=false", "-c", "gc.auto=0"}, args...)...)
	cmd.Env = append(taskworkspace.GitEnv(os.Environ()), "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=")
	cmd.WaitDelay = time.Second
	var out workspaceOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	err := cmd.Run()
	if out.overflow {
		return "", errors.New("output limit")
	}
	return strings.TrimSpace(out.String()), err
}

// Under both resource locks, only an absent root and an unused private store
// prove there is nothing to remove. Unknown entries may belong to a lost receipt.
func (e *workspaceExecutor) hasNoResources(root string) bool {
	if len(e.journal.Identities) != 0 || len(e.journal.Repositories) != 0 || len(e.journal.Uses) != 0 {
		return false
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		return false
	}
	storage := filepath.Dir(e.path)
	entries, err := os.ReadDir(storage)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		switch entry.Name() {
		case "receipt.json":
			if !entry.Type().IsRegular() {
				return false
			}
		case "hooks":
			if !entry.IsDir() {
				return false
			}
			hooks, err := os.ReadDir(filepath.Join(storage, "hooks"))
			if err != nil || len(hooks) != 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (n *Node) taskWorkspace(ctx context.Context, method string, raw, start json.RawMessage) (any, error) {
	if method == devicewire.MethodStartBackendSession && len(start) == 0 {
		return nil, errors.New("unknown workspace method")
	}
	var req taskworkspace.Request
	if json.Unmarshal(raw, &req) != nil || !taskworkspace.ValidRequest(req) {
		return taskworkspace.Record{Error: taskworkspace.Failure("invalid_input", "validation", req.OperationID, "", "")}, nil
	}
	// A digest detects accidental changes; re-resolve on the target to validate
	// paths and templates rather than trusting a caller-computed digest.
	preview := n.resolveWorkspacePlan(req.Plan.Source)
	if preview.Plan == nil || preview.Plan.Digest != req.Plan.Digest {
		return taskworkspace.Record{Error: taskworkspace.Failure("invalid_path", "validation", req.OperationID, "", req.Plan.RootPath)}, nil
	}
	if problem := n.workspacePlanPathProblem(preview.Plan); problem != nil {
		return taskworkspace.Record{Error: taskworkspace.Failure(problem.Code, "validation", req.OperationID, problem.RepoID, req.Plan.RootPath)}, nil
	}
	remoteRepos := 0
	for _, source := range req.Plan.Source.Repositories.Repositories {
		// An existing receipt must remain inspectable and cleanable if the source
		// checkout disappears. The local store is attested before any Git use.
		if source.Local != nil {
			continue
		}
		remoteRepos++
		if !n.workspaceSourceAllowed(source) {
			return taskworkspace.Record{Error: taskworkspace.Failure("source_invalid", "validation", req.OperationID, source.RepoID, "")}, nil
		}
	}
	control, err := n.workspaceControlDir("task-workspaces")
	if err != nil {
		var problem *taskworkspace.Problem
		if errors.As(err, &problem) {
			return taskworkspace.Record{Error: taskworkspace.Failure(problem.Code, "validation", req.OperationID, "", req.Plan.RootPath)}, nil
		}
		return nil, err
	}
	if err := os.MkdirAll(control, 0700); err != nil {
		return nil, err
	}
	keys := []string{"workspace-" + taskworkspace.Hash(req.WorkspaceID), "root-" + taskworkspace.Hash(req.Plan.RootPath)}
	e := &workspaceExecutor{n: n, mirrorRepos: req.MirrorRepos, mirrorTTLSeconds: req.MirrorTTLSeconds}
	defer func() {
		for _, f := range e.locks {
			f.Close()
		}
	}()
	for _, key := range keys {
		f, err := link.LockFileHandle(filepath.Join(control, key+".lock"), false)
		if err != nil {
			if errors.Is(err, link.ErrLocked) {
				return taskworkspace.Record{WorkspaceID: req.WorkspaceID, OperationID: req.OperationID, State: "preparing", Error: taskworkspace.Failure("operation_busy", method, req.OperationID, "", req.Plan.RootPath)}, nil
			}
			return nil, err
		}
		e.locks = append(e.locks, f)
	}
	storage := filepath.Join(control, taskworkspace.Hash(req.WorkspaceID))
	if workspaceNoLinks(filepath.Join(storage, "hooks")) != nil {
		return nil, errors.New("workspace storage unavailable")
	}
	if err := os.MkdirAll(filepath.Join(storage, "hooks"), 0700); err != nil {
		return nil, err
	}
	e.path = filepath.Join(storage, "receipt.json")
	if workspaceNoLinks(e.path) != nil {
		return nil, errors.New("workspace receipt unavailable")
	}
	data, err := os.ReadFile(e.path)
	if err == nil {
		err = json.Unmarshal(data, &e.journal)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("workspace receipt unavailable")
	}
	if errors.Is(err, os.ErrNotExist) {
		state := "preparing"
		switch method {
		case "prepare":
		case "cancel", "cleanup":
			if !e.hasNoResources(req.Plan.RootPath) {
				return taskworkspace.Record{Error: taskworkspace.Failure("operation_unknown", method, req.OperationID, "", req.Plan.RootPath)}, nil
			}
			state = "cancelled"
			if method == "cleanup" {
				state = "removed"
			}
		default:
			return taskworkspace.Record{Error: taskworkspace.Failure("not_found", method, req.OperationID, "", req.Plan.RootPath)}, nil
		}
		e.journal = workspaceJournal{Record: taskworkspace.Record{SchemaVersion: 1, WorkspaceID: req.WorkspaceID, OperationID: req.OperationID, NodeOperationID: req.OperationID, PreviousOperationID: req.PreviousOperationID, Plan: req.Plan, InputDigest: taskworkspace.InputKey(req.Plan), State: state, Repositories: []taskworkspace.Instance{}, Uses: []string{}}, Identities: map[string]string{}, Tombstones: []string{}}
		if state == "removed" {
			e.journal.Cleanup = &taskworkspace.CleanupResult{Removed: true, RetainedPaths: []string{}}
		}
		if err = e.save(); err != nil {
			return nil, err
		}
		if state != "preparing" {
			return e.journal.Record, nil
		}
	}
	j := &e.journal
	j.NodeOperationID = j.OperationID
	if j.Plan.Source.Manager != req.Plan.Source.Manager || j.Plan.Source.TodoID != req.Plan.Source.TodoID || j.Plan.Source.MachineID != req.Plan.Source.MachineID || j.WorkspaceID != req.WorkspaceID {
		return taskworkspace.Record{Error: taskworkspace.Failure("identity_mismatch", method, req.OperationID, "", req.Plan.RootPath)}, nil
	}
	if slices.Contains(j.Tombstones, req.OperationID) {
		return taskworkspace.Record{WorkspaceID: req.WorkspaceID, OperationID: req.OperationID, State: "cancelled", Error: taskworkspace.Failure("operation_conflict", method, req.OperationID, "", req.Plan.RootPath)}, nil
	}
	if j.InFlight {
		if !workspaceLockSurvivesCommand {
			return e.fail("operation_unknown", method, "", j.Plan.RootPath), nil
		}
		// Acquiring both inherited locks proves the previous command no longer owns
		// them. Effects are reconciled below, never undone or blindly overwritten.
		j.InFlight = false
		if method != "inspect" {
			if err = e.save(); err != nil {
				return nil, err
			}
		}
	}
	if j.OperationID != req.OperationID {
		if method != "prepare" || req.PreviousOperationID != j.OperationID || j.State != "cancelled" || len(j.Uses) > 0 {
			return e.conflict(req, "operation_conflict", method), nil
		}
		if err = e.adopt(ctx, req); err != nil {
			return e.conflict(req, "resource_conflict", method), nil
		}
	}
	if j.InputDigest != taskworkspace.InputKey(req.Plan) {
		return e.fail("operation_conflict", method, "", j.Plan.RootPath), nil
	}
	j.Error = nil
	switch method {
	case devicewire.MethodStartBackendSession:
		return e.startSession(ctx, req, start)
	case "inspect":
		if j.Launch != nil && j.Launch.SessionID == "" && j.Launch.ExpectedSessionID != "" {
			e.observedLaunch(ctx)
		}
		if j.State == "ready" && !e.owned(j.Plan.RootPath) {
			j.Error = taskworkspace.Failure("identity_mismatch", method, j.OperationID, "", j.Plan.RootPath)
			return j.Record, nil
		}
		for i := range j.Repositories {
			r := &j.Repositories[i]
			if r.State == "materializing" && e.checkRegistration(ctx, r) == "" {
				if id, err := workspaceFileID(r.Path); err == nil {
					j.Identities[r.Path] = id
					r.State = "ready"
				}
			}
			if r.State == "ready" && !r.Removed {
				if code := e.inspect(ctx, r); code != "" {
					r.Error = taskworkspace.Failure(code, "inspect", j.OperationID, r.RepoID, r.Path)
					j.Error = r.Error
				}
			}
		}
		return j.Record, nil
	case "prepare":
		if j.State == "removed" || j.State == "cancelled" {
			return j.Record, nil
		}
		if j.Cleanup != nil {
			return e.fail("resource_conflict", "prepare", "", j.Plan.RootPath), nil
		}
		// The Node RPC context may have no deadline. Bound preparation,
		// including time queued for network Git, with the shared RPC budget.
		prepareCtx, cancel := context.WithTimeout(ctx, taskworkspace.PrepareTimeout(remoteRepos))
		defer cancel()
		result := e.prepare(prepareCtx)
		if err = e.save(); err != nil {
			return nil, err
		}
		return result, nil
	case "cancel":
		if j.State != "removed" {
			j.State = "cancelled"
		}
	case "acquire":
		if strings.HasPrefix(req.UseID, "launch_") && slices.ContainsFunc(j.Uses, func(other string) bool { return strings.HasPrefix(other, "launch_") && other != req.UseID }) {
			return e.conflict(req, "worktree_in_use", method), nil
		}
		if j.State != "ready" || j.Cleanup != nil || !workspacenaming.ValidOperationID(req.UseID) {
			return e.fail("not_ready", method, "", j.Plan.RootPath), nil
		}
		if !e.owned(j.Plan.RootPath) {
			return e.fail("identity_mismatch", method, "", j.Plan.RootPath), nil
		}
		for i := range j.Repositories {
			if code := e.inspect(ctx, &j.Repositories[i]); code != "" {
				return e.fail(code, method, j.Repositories[i].RepoID, j.Repositories[i].Path), nil
			}
		}
		if !slices.Contains(j.Uses, req.UseID) {
			j.Uses = append(j.Uses, req.UseID)
		}
	case "release":
		if j.Launch != nil && j.Launch.UseID == req.UseID {
			if !e.launchStopped(ctx) {
				return e.conflict(req, "worktree_in_use", "release"), nil
			}
			j.Launch = nil
		}
		j.Uses = slices.DeleteFunc(j.Uses, func(id string) bool { return id == req.UseID })
	case "cleanup":
		result := e.cleanup(ctx, req)
		if err = e.save(); err != nil {
			return nil, err
		}
		return result, nil
	default:
		return nil, errors.New("unsupported task workspace method")
	}
	if err = e.save(); err != nil {
		return nil, err
	}
	return j.Record, nil
}
func (e *workspaceExecutor) adopt(ctx context.Context, req taskworkspace.Request) error {
	j := &e.journal
	reused := []taskworkspace.Instance{}
	for _, old := range j.Repositories {
		if old.State == "materializing" {
			if e.checkRegistration(ctx, &old) == "" {
				old.State = "ready"
				if id, err := workspaceFileID(old.Path); err == nil {
					j.Identities[old.Path] = id
				} else {
					return err
				}
			} else if entries, err := os.ReadDir(old.Path); err == nil && len(entries) > 0 {
				return errors.New("unfinished instance requires cleanup")
			}
		}
		if old.State != "ready" {
			continue
		}
		found := false
		for i, p := range req.Plan.Repositories {
			source := req.Plan.Source.Repositories.Repositories[i]
			if old.RepoID == p.RepoID && old.Path == p.AbsolutePath && old.TaskBranch == p.TaskBranch && taskworkspace.Hash(old.Input) == taskworkspace.Hash(source) {
				reused = append(reused, old)
				found = true
				break
			}
		}
		if !found {
			return errors.New("successful instance requires explicit cleanup")
		}
	}
	if j.Cleanup != nil {
		return errors.New("cleanup must settle first")
	}
	if j.Plan.RootPath != req.Plan.RootPath && len(j.Identities) > 0 {
		return errors.New("root requires explicit cleanup")
	}
	j.Tombstones = append(j.Tombstones, j.OperationID)
	j.OperationID = req.OperationID
	j.NodeOperationID = req.OperationID
	j.PreviousOperationID = req.PreviousOperationID
	j.Plan = req.Plan
	j.InputDigest = taskworkspace.InputKey(req.Plan)
	j.State = "preparing"
	j.Repositories = reused
	if len(reused) == 0 {
		j.LocalGitIdentity = ""
	}
	j.Cleanup = nil
	j.Error = nil
	return e.save()
}

func (e *workspaceExecutor) privateCommonDir(repoID string) string {
	return filepath.Join(filepath.Dir(e.path), "git", taskworkspace.Hash(repoID)+".git")
}

func (e *workspaceExecutor) prepare(ctx context.Context) taskworkspace.Record {
	j := &e.journal
	j.State = "preparing"
	j.LastGitStderrSummary = ""
	if !e.owned(j.Plan.RootPath) {
		if len(j.Plan.Repositories) == 0 && j.Identities[j.Plan.RootPath] != "" {
			return e.fail("identity_mismatch", "reserve", "", j.Plan.RootPath)
		}
		if err := e.claimDir(j.Plan.RootPath); err != nil {
			return e.fail("path_conflict", "reserve", "", j.Plan.RootPath)
		}
	}
	for index, p := range j.Plan.Repositories {
		i := slices.IndexFunc(j.Repositories, func(r taskworkspace.Instance) bool { return r.RepoID == p.RepoID })
		if i < 0 {
			input := j.Plan.Source.Repositories.Repositories[index]
			common := e.privateCommonDir(p.RepoID)
			if input.Local != nil {
				common = input.Local.CommonDir
			}
			j.Repositories = append(j.Repositories, taskworkspace.Instance{RepoID: p.RepoID, Input: input, RelativePath: p.RelativePath, Path: p.AbsolutePath, CommonDir: common, TaskBranch: p.TaskBranch, State: "pending"})
			i = len(j.Repositories) - 1
			if err := e.save(); err != nil {
				return e.fail("persistence_failed", "reserve", p.RepoID, p.AbsolutePath)
			}
		}
		r := &j.Repositories[i]
		r.Error = nil
		if r.Removed {
			return e.fail("resource_conflict", "prepare", r.RepoID, r.Path)
		}
		if r.State == "ready" {
			if code := e.inspect(ctx, r); code != "" {
				return e.fail(code, "inspect", r.RepoID, r.Path)
			}
			continue
		}
		if code := e.source(ctx, r); code != "" {
			return e.fail(code, "source", r.RepoID, r.Path)
		}
		if code := e.materialize(ctx, r); code != "" {
			return e.fail(code, "worktree", r.RepoID, r.Path)
		}
	}
	j.State = "ready"
	return j.Record
}

func (e *workspaceExecutor) conflict(req taskworkspace.Request, code, stage string) taskworkspace.Record {
	return taskworkspace.Record{SchemaVersion: 1, WorkspaceID: req.WorkspaceID, OperationID: req.OperationID, NodeOperationID: e.journal.OperationID, PreviousOperationID: req.PreviousOperationID, Plan: req.Plan, InputDigest: taskworkspace.InputKey(req.Plan), State: "failed", Repositories: e.journal.Repositories, Uses: []string{}, Error: taskworkspace.Failure(code, stage, req.OperationID, "", e.journal.Plan.RootPath)}
}
