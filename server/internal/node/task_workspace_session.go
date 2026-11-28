package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

// Both native resume and Git requests share the physical workspace lock with cleanup.
func (n *Node) lockBackendReceipt(workspace, stage, repo string) (*workspaceExecutor, *os.File, error) {
	if !workspacenaming.ValidOperationID(workspace) {
		return nil, nil, taskworkspace.Failure("invalid_input", stage, "", repo, "")
	}
	control, err := n.workspaceControlDir("task-workspaces")
	if err != nil {
		return nil, nil, err
	}
	key := taskworkspace.Hash(workspace)
	path := filepath.Join(control, key, "receipt.json")
	if err := workspaceNoLinks(path); err != nil {
		return nil, nil, err
	}
	lock, err := link.LockFileHandle(filepath.Join(control, "workspace-"+key+".lock"), false)
	if err != nil {
		return nil, nil, taskworkspace.Failure("operation_busy", stage, "", repo, "")
	}
	data, err := os.ReadFile(path)
	e := &workspaceExecutor{n: n, path: path}
	if err == nil {
		err = json.Unmarshal(data, &e.journal)
	}
	if err != nil || e.journal.WorkspaceID != workspace {
		lock.Close()
		return nil, nil, taskworkspace.Failure("not_found", stage, "", repo, "")
	}
	return e, lock, nil
}

// The index only locates a receipt. After release, the old session must no
// longer reopen a checkout now used by another plan or removed by its owner.
func (n *Node) backendSessionIndex(id string) (string, error) {
	control, err := n.workspaceControlDir("task-workspaces")
	if err != nil {
		return "", err
	}
	path := filepath.Join(control, "sessions", taskworkspace.Hash(id)+".json")
	return path, workspaceNoLinks(path)
}
func (e *workspaceExecutor) rememberLaunch(id string) error {
	path, err := e.n.backendSessionIndex(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return workspaceWrite(path, e.journal.WorkspaceID)
}
func (n *Node) pinBackendSession(ctx context.Context, id, cwd string, resume bool) (*backendLaunch, *os.File, error) {
	if id == "" || n.dir == "" {
		return nil, nil, nil
	}
	path, err := n.backendSessionIndex(id)
	if err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var workspace string
	if err := json.Unmarshal(data, &workspace); err != nil {
		return nil, nil, err
	}
	e, lock, err := n.lockBackendReceipt(workspace, "resume", "")
	if err != nil {
		return nil, nil, err
	}
	j := &e.journal
	if j.State != "ready" || j.Cleanup != nil || j.Launch == nil || j.Launch.SessionID != id || !slices.Contains(j.Uses, j.Launch.UseID) || cwd != "" && cwd != j.Plan.RootPath {
		lock.Close()
		return nil, nil, taskworkspace.Failure("not_ready", "resume", j.OperationID, "", j.Plan.RootPath)
	}
	if !e.owned(j.Plan.RootPath) {
		lock.Close()
		return nil, nil, taskworkspace.Failure("identity_mismatch", "resume", j.OperationID, "", j.Plan.RootPath)
	}
	if resume {
		for i := range j.Repositories {
			repo := &j.Repositories[i]
			if code := e.inspect(ctx, repo); code != "" {
				lock.Close()
				return nil, nil, taskworkspace.Failure(code, "resume", j.OperationID, repo.RepoID, repo.Path)
			}
		}
	}
	return &backendLaunch{executor: e, use: j.Launch.UseID, taskDispatch: j.Launch.TaskDispatch}, lock, nil
}
