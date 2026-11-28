package node

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func init() { nodeRPC("workspace.naming.resolve", (*Node).resolveWorkspaceNaming) }

func (n *Node) resolveWorkspacePlan(source workspacenaming.Source) workspacenaming.Preview {
	ctx := workspacenaming.NodeContext{Home: n.home, PersonaHome: n.dir}
	return workspacenaming.ResolveWithPhysicalHome(source, ctx, n.workspacePhysicalHome())
}

func (n *Node) workspacePhysicalHome() string {
	home, err := n.realPath(filepath.Clean(n.home))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return n.home
	}
	return home
}

func (n *Node) resolveHomeAlias(path string) string {
	return workspacenaming.PhysicalHomePath(path, n.home, n.workspacePhysicalHome())
}

// workspaceControlDir follows the configured state prefix only. A link in
// place of the control directory is inspected solely to classify the refusal.
func (n *Node) workspaceControlDir(name string) (string, error) {
	state, err := n.realPath(filepath.Clean(n.dir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("workspace state path unavailable")
	}
	path := filepath.Join(state, name)
	err = workspaceNoLinks(path)
	if errors.Is(err, errWorkspaceSymlink) {
		code := "path_symlink"
		if target, err := os.Readlink(path); err == nil {
			if !filepath.IsAbs(target) {
				target = state + string(filepath.Separator) + target
			}
			if !n.workspacePathInScope(target, state) {
				code = "base_scope_denied"
			}
		}
		return "", taskworkspace.Failure(code, "validation", "", "", "")
	}
	return path, err
}

func (n *Node) workspacePathInScope(path, state string) bool {
	real, err := n.realPath(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) || n.offLimitsResolved(real) != nil || workspacenaming.InPrivateState(real, state) {
		return false
	}
	standard := filepath.Join(n.home, ".persona")
	if n.dir == "" || n.dir == standard {
		return true
	}
	standard, err = n.realPath(standard)
	return (err == nil || errors.Is(err, fs.ErrNotExist)) && !workspacenaming.InPrivateState(real, standard)
}

func (n *Node) resolveWorkspaceNaming(ctx context.Context, raw json.RawMessage) (any, error) {
	var source workspacenaming.Source
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, err
	}
	preview := n.resolveWorkspacePlan(source)
	if preview.Plan == nil {
		return preview, nil
	}
	if problem := n.workspacePlanPathProblem(preview.Plan); problem != nil {
		preview.Plan = nil
		preview.Status = "invalid"
		preview.Diagnostics = append(preview.Diagnostics, *problem)
		return preview, nil
	}
	for _, repo := range preview.Plan.Repositories {
		checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := exec.CommandContext(checkCtx, "git", "check-ref-format", "--branch", repo.TaskBranch).Run()
		cancel()
		if err != nil {
			code := "git_validation_unavailable"
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				code = "branch_invalid"
			}
			preview.Plan = nil
			preview.Status = "invalid"
			preview.Diagnostics = append(preview.Diagnostics, workspacenaming.Problem{Code: code, Field: "branch_template", RepoID: repo.RepoID, Severity: "error"})
			break
		}
	}
	return preview, nil
}

// The same path policy runs before preview and preparation. Inspect resolved
// paths first to preserve diagnostics for links into private or credential paths.
func (n *Node) workspacePlanPathProblem(plan *workspacenaming.Plan) *workspacenaming.Problem {
	scopeDenied := func(repoID string) *workspacenaming.Problem {
		return &workspacenaming.Problem{Code: "base_scope_denied", Field: "base_directory", RepoID: repoID, Severity: "error"}
	}
	state := n.dir
	if state == "" {
		state = filepath.Join(n.home, ".persona")
	}
	state, err := n.realPath(state)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return scopeDenied("")
	}
	check := func(path, field, repoID string) *workspacenaming.Problem {
		if !n.workspacePathInScope(path, state) {
			return scopeDenied(repoID)
		}
		switch err := workspaceNoLinks(path); {
		case errors.Is(err, errWorkspaceSymlink):
			return &workspacenaming.Problem{Code: "path_symlink", Field: field, RepoID: repoID, Severity: "error"}
		case err != nil:
			return scopeDenied(repoID)
		}
		return nil
	}
	if problem := check(plan.BasePath, "base_directory", ""); problem != nil {
		return problem
	}
	if problem := check(plan.RootPath, "task_directory_template", ""); problem != nil {
		return problem
	}
	for _, repo := range plan.Repositories {
		if repo.AbsolutePath == plan.RootPath {
			continue
		}
		if problem := check(repo.AbsolutePath, "directory_alias", repo.RepoID); problem != nil {
			return problem
		}
	}
	return nil
}
