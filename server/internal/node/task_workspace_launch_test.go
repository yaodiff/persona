package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	repository "github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func TestBackendWorkspaceLaunchAndRepositoryRouting(t *testing.T) {
	for _, tc := range []struct {
		name     string
		layout   repository.Layout
		branches []string
		local    bool
	}{
		{name: "root", layout: repository.LayoutRoot, branches: []string{"trunk"}},
		{name: "container", layout: repository.LayoutContainer, branches: []string{"trunk", "develop"}},
		{name: "no repositories", layout: repository.LayoutContainer},
		{name: "local dispatch", layout: repository.LayoutRoot, local: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n, f, home, runtimeHost := piHerdrNode(t)
			// A workspace root may not pass through a symlink (workspaceNoLinks), and macOS keeps t.TempDir() behind
			// /var: take the home resolved, as workspaceTemp does for the other backend workspace tests.
			home = resolved(t, home)
			n.home, n.dir = home, filepath.Join(home, ".persona")
			var urls []string
			for _, branch := range tc.branches {
				urls = append(urls, workspaceRemote(t, branch))
			}
			var plan workspacenaming.Plan
			if tc.local {
				source := localWorkspaceSource(t, n, workspaceRemote(t, "trunk"), "")
				plan = localWorkspacePlan(t, n, source, "")
			} else {
				plan = workspacePlan(t, home, tc.layout, urls...)
			}
			req := taskworkspace.Request{WorkspaceID: "ws_example", OperationID: "op_example", UseID: "launch_example", Plan: plan, TaskDispatch: tc.local}
			root := req.Plan.RootPath
			ready := workspaceCall(t, n, "prepare", req)
			if ready.State != "ready" {
				t.Fatal(ready.Error, ready.State)
			}
			params := map[string]any{"backend_workspace": req, "cwd": root, "session_id": startID}
			if tc.local {
				params["task_id"], params["workspace_label"] = "todo_example", "Example task"
			}
			start := func() map[string]any {
				t.Helper()
				out, err := request(t, n, devicewire.MethodStartBackendSession, params)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			if got := start(); got["error"] == nil {
				t.Fatalf("unclaimed start: %v", got)
			}
			if r := workspaceCall(t, n, "acquire", req); r.Error != nil {
				t.Fatal(r.Error)
			}
			params["cwd"] = home
			if got := start(); got["error"] == nil {
				t.Fatalf("accepted another cwd: %v", got)
			}
			params["cwd"] = root
			params["host"] = "missing-engine"
			if _, err := request(t, n, devicewire.MethodStartBackendSession, params); err == nil {
				t.Fatal("unknown engine accepted")
			}
			if failed := workspaceCall(t, n, "inspect", req); failed.State != "ready" || failed.Launch != nil {
				t.Fatal("pre-dispatch failure changed ready workspace", failed.State, failed.Launch)
			}
			delete(params, "host")
			f.reply("workspace.create", map[string]any{"workspace": map[string]any{"workspace_id": "w-task", "label": "Example"}, "tab": map[string]any{"tab_id": "w-task:t1"}, "root_pane": map[string]any{"pane_id": "w-task:p1"}})
			startHerdr(f, "w-task:p1", proto.StatusIdle)
			f.reply("agent.get", map[string]any{"agent": map[string]any{"agent": "claude", "agent_status": proto.StatusIdle, "pane_id": "w-task:p1", "tab_id": "w-task:t1", "workspace_id": "w-task", "cwd": root}})
			f.setScreen("w-task:p1", screenFixture(t, "claude-2.1.312-empty-162.txt"))
			got := start()
			session, ok := got["session"].(map[string]any)
			if !ok || session["cwd"] != root || session["session_id"] != startID {
				t.Fatalf("unexpected start: %v", got)
			}
			if tc.local {
				space, ok := got["task_workspace"].(map[string]any)
				if !ok || space["workspace_id"] != "w-task" || space["task_id"] != "todo_example" {
					t.Fatal("local dispatch lost leader terminal placement", got)
				}
			}
			creates := f.trace("workspace.create")
			if len(creates) != 1 || !strings.Contains(creates[0], root) || len(f.trace("worktree.create")) != 0 {
				t.Fatalf("native placement: %v", creates)
			}
			receipt := workspaceCall(t, n, "inspect", req)
			if receipt.Launch == nil || receipt.Launch.SessionID != startID || receipt.Launch.UseID != req.UseID {
				t.Fatalf("wrong receipt: %v", receipt.Launch)
			}
			start()
			if len(f.trace("workspace.create")) != 1 {
				t.Fatal("duplicate engine launch")
			}
			if r := workspaceCall(t, n, "release", req); r.Error == nil {
				t.Fatal("released without evidence of stop")
			}
			if r := workspaceCall(t, n, "cleanup", req); r.State != "ready" || r.Error == nil || r.Error.Code != "worktree_in_use" {
				t.Fatal("cleanup did not preserve the active workspace", r.State, r.Error)
			}
			if err := n.guardBackendDeletion(root); err == nil {
				t.Fatal("native cleanup can remove the backend root")
			}
			for _, repo := range ready.Repositories {
				workspaceGit(t, repo.Path, "config", "user.name", "Example")
				workspaceGit(t, repo.Path, "config", "user.email", "me@example.com")
				name := repo.RepoID + ".txt"
				if err := os.WriteFile(filepath.Join(repo.Path, name), []byte("local work"), 0600); err != nil {
					t.Fatal(err)
				}
				target := map[string]any{"backend_workspace_id": req.WorkspaceID, "repo_id": repo.RepoID, "paths": []string{name}, "message": "Update " + repo.RepoID}
				for _, verb := range []string{"stage", "commit"} {
					if got, err := request(t, n, "git.backend."+verb, target); err != nil || got["ok"] == false {
						t.Fatal(verb, got, err)
					}
				}
				if msg := strings.TrimSpace(workspaceGit(t, repo.Path, "log", "-1", "--format=%s")); msg != "Update "+repo.RepoID {
					t.Fatal(msg)
				}
			}
			if _, err := request(t, n, "git.backend.status", map[string]any{"backend_workspace_id": req.WorkspaceID, "repo_id": "absent", "cwd": root}); err == nil {
				t.Fatal("fell back to first repository")
			}
			n.state.stopped(startID, proto.StopTaskCompleted)
			if r := workspaceCall(t, n, "release", req); r.Error != nil || r.Launch != nil {
				t.Fatal("confirmed stop did not release", r.Error, r.Launch)
			}
			if _, err := request(t, n, "session.start", map[string]any{"resume_session_id": startID, "cwd": root}); err == nil {
				t.Fatal("retired session reopened a released checkout")
			}
			if len(ready.Repositories) == 0 {
				if r := workspaceCall(t, n, "cleanup", req); r.State != "removed" || r.Error != nil {
					t.Fatal("stopped workspace was not removed", r.State, r.Error)
				}
				return
			}
			req.UseID = "launch_next"
			if r := workspaceCall(t, n, "acquire", req); r.Error != nil {
				t.Fatal("could not reuse ready checkout", r.Error)
			}
			if err := n.useEngine(t.Context(), proto.EnginePaseo); err != nil {
				t.Fatal(err)
			}
			hello := paseoEngineHello()
			hello["caps"].(map[string]any)["backend_workspace_root"] = !tc.local
			rt := dialRuntimeAt(t, runtimeHost.path, hello)
			rt.mu.Lock()
			rt.answer = func(method string, raw json.RawMessage) (any, error) {
				if method == "session.start" {
					var input struct {
						Cwd  string `json:"cwd"`
						Root bool   `json:"backend_workspace_root"`
					}
					if err := json.Unmarshal(raw, &input); err != nil || input.Root == tc.local || input.Cwd != root {
						return nil, os.ErrInvalid
					}
				}
				return map[string]any{"session": map[string]any{"session_id": "paseo-next", "status": "idle", "cwd": root}}, nil
			}
			rt.mu.Unlock()
			params["backend_workspace"], params["host"], params["launcher"] = req, "paseo", "paseo-claude"
			delete(params, "session_id")
			switched := start()
			actual, ok := switched["session"].(map[string]any)
			if !ok || actual["session_id"] != "paseo-next" || actual["cwd"] != root || len(rt.got("session.start")) != 1 {
				t.Fatal("switch failed", switched)
			}
			for _, repo := range ready.Repositories {
				if msg := strings.TrimSpace(workspaceGit(t, repo.Path, "log", "-1", "--format=%s")); msg != "Update "+repo.RepoID {
					t.Fatal("switch reset repository", msg)
				}
			}
		})
	}
}
