package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestTaskWorkspaceExplicitBranchReusesIdleCheckout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := workspaceTaskService(t)
		creates := 0
		f.nodes[DeviceWorktreePrepare] = func(_ string, raw json.RawMessage) (any, error) {
			var in struct{ Branch string }
			_ = json.Unmarshal(raw, &in)
			prep := map[string]any{"root": "/code/app", "path": "/wt/app"}
			if in.Branch == "fix/login" {
				prep["existing"] = map[string]any{"path": "/code/retained", "branch": in.Branch}
			}
			return prep, nil
		}
		f.nodes[DeviceWorktreeCreate] = func(string, json.RawMessage) (any, error) {
			creates++
			return nil, errors.New("branch already checked out at /code/retained")
		}
		p := workspacePlan(t, s, `{"new":{"branch":"fix/login"}}`)
		if len(f.starts) != 0 || creates != 0 {
			t.Fatal("preview must not start an Agent or create a checkout")
		}
		if _, err := s.startPlan(context.Background(), p.ID, nil, false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		saved, _ := s.loadPlan(context.Background(), p.ID)
		if creates != 0 || len(f.starts) != 1 || f.starts[0]["cwd"] != "/code/retained" ||
			saved.Tasks[0].Cwd != "/code/retained" || onlyTodo(s).Agents[0].Cwd != "/code/retained" {
			t.Fatalf("clean branch checkout was not reused: creates=%d starts=%v task=%+v", creates, f.starts, saved.Tasks[0])
		}
		task := saved.Tasks[0]
		if !task.WorkspaceResolved.Reused || task.WorkspaceResolved.Created || task.MachinePolicy != machineFixed {
			t.Fatalf("reused checkout was not saved truthfully and machine-local: %+v", task.WorkspaceResolved)
		}
		f.nodes[DeviceWorktreePrepare] = func(string, json.RawMessage) (any, error) {
			t.Fatal("retry must keep the already allocated checkout")
			return nil, nil
		}
		if err := s.admitTaskWorkspace(context.Background(), &task); err != nil || task.Cwd != "/code/retained" {
			t.Fatalf("retry lost the reused checkout: %+v %v", task, err)
		}
		task.Machine = "another-machine"
		if err := s.admitTaskWorkspace(context.Background(), &task); err == nil {
			t.Fatal("a reused checkout must not move to a different machine")
		}
	})
}

func TestTaskWorkspaceExplicitBranchReuseRefusals(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing map[string]any
		err      error
		ownerCwd string
		reserved bool
		want     string
	}{
		{name: "inspection fails", err: errors.New("cannot inspect /code/retained"), want: "/code/retained"},
		{name: "relative path", existing: map[string]any{"path": "relative", "branch": "fix/login"}, want: "checkout"},
		{name: "wrong branch", existing: map[string]any{"path": "/code/retained", "branch": "fix/other"}, want: "checkout"},
		{name: "task owns checkout", existing: map[string]any{"path": "/code/retained", "branch": "fix/login"}, ownerCwd: "/code/retained", want: "Other writer"},
		{name: "task owns subdirectory", existing: map[string]any{"path": "/code/retained", "branch": "fix/login"}, ownerCwd: "/code/retained/sub", want: "Other writer"},
		{name: "subdirectory admission is reserved", existing: map[string]any{"path": "/code/retained", "branch": "fix/login"}, ownerCwd: "/code/retained/sub", reserved: true, want: "Other writer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, f := workspaceTaskService(t)
				ctx := context.Background()
				if tc.ownerCwd != "" {
					owner, err := s.createTodo(ctx, "Other writer", "", "other-chat", "")
					if err != nil {
						t.Fatal(err)
					}
					if tc.reserved {
						s.tasks.workspaceClaims = map[string]string{"box\x00" + tc.ownerCwd: owner.ID}
					} else {
						if err := s.linkTodoAgent(ctx, owner.ID, "box", "writer", owner.Title, tc.ownerCwd, ""); err != nil {
							t.Fatal(err)
						}
					}
				}
				f.answerNode(DeviceWorktreePrepare, func(_ string, raw json.RawMessage) (any, error) {
					var in struct{ Branch string }
					_ = json.Unmarshal(raw, &in)
					prep := map[string]any{"root": "/code/app", "path": "/wt/app"}
					if in.Branch != "" {
						if tc.err != nil {
							return nil, tc.err
						}
						prep["existing"] = tc.existing
					}
					return prep, nil
				})
				creates := 0
				f.answerNode(DeviceWorktreeCreate, func(string, json.RawMessage) (any, error) {
					creates++
					return proto.WorktreeCreateResult{Path: "/wt/app/fix-login", Branch: "fix/login"}, nil
				})
				p := workspacePlan(t, s, `{"new":{"branch":"fix/login"}}`)
				if _, err := s.startPlan(ctx, p.ID, nil, false); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				saved, _ := s.loadPlan(ctx, p.ID)
				if len(f.starts) != 0 || creates != 0 || !strings.Contains(saved.Tasks[0].Error, tc.want) {
					t.Fatalf("unsafe reuse did not refuse: creates=%d starts=%v task=%+v", creates, f.starts, saved.Tasks[0])
				}
			})
		})
	}
}

func TestTaskWorkspaceExplicitBranchOlderNodeStillCreates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := workspaceTaskService(t)
		p := workspacePlan(t, s, `{"new":{"branch":"fix/login"}}`)
		if _, err := s.startPlan(context.Background(), p.ID, nil, false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if len(f.starts) != 1 || f.starts[0]["cwd"] != "/wt/app/fix-login" {
			t.Fatalf("a node without branch lookup must retain normal creation: %v", f.starts)
		}
	})
}

func TestTaskCheckoutOwnershipIncludesOnlyDescendants(t *testing.T) {
	for _, tc := range []struct {
		root, dir string
		owned     bool
	}{
		{"/code/app", "/code/app/sub", true},
		{"/code/app", "/code/app-other", false},
		{`C:\code\app`, `c:\CODE\APP\sub`, true},
		{`C:\code\app`, `C:\code\app-other`, false},
	} {
		t.Run(tc.dir, func(t *testing.T) {
			s, _ := workspaceTaskService(t)
			ctx := context.Background()
			owner, err := s.createTodo(ctx, "Reserved writer", "", "other-chat", "")
			if err != nil {
				t.Fatal(err)
			}
			self, err := s.createTodo(ctx, "This task", "", "workspace-chat", "")
			if err != nil {
				t.Fatal(err)
			}
			s.tasks.workspaceClaims = map[string]string{
				"box\x00" + proto.NodeKey(tc.root): self.ID,
				"box\x00" + proto.NodeKey(tc.dir):  owner.ID,
			}
			if got := s.taskWorkspaceOwner(ctx, "box", tc.root, self.ID, true); (got != nil) != tc.owned {
				t.Fatalf("checkout ownership at %s for %s = %+v; want owned=%v", tc.root, tc.dir, got, tc.owned)
			}
			if s.taskWorkspaceOwner(ctx, "box", tc.root, self.ID, false) != nil {
				t.Fatal("ordinary existing-workspace ownership must retain exact-directory semantics")
			}
		})
	}
}
