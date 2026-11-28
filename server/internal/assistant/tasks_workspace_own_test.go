package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A new plan for a to-do whose earlier start failed (or whose earlier Agent stopped) gets the same task/<todo id>
// branch and checkout path; the checkout that start left is this to-do's own, and is reused when the node finds it
// clean and idle in the checkouts folder (#10365). Explicit branches keep #8357's rules.

// ownBranchPrepare answers worktree.prepare: with branch task/<todo>, existing (made by the fake's create) when
// existing returns one; any other branch is not checked out.
func ownBranchPrepare(existing func(branch string) (map[string]any, error)) nodeAnswer {
	return func(_ string, raw json.RawMessage) (any, error) {
		var in struct{ Branch string }
		_ = json.Unmarshal(raw, &in)
		prep := map[string]any{"root": "/code/app", "repoName": "app", "branch": "main", "path": "/wt/app"}
		if in.Branch != "" && existing != nil {
			at, err := existing(in.Branch)
			if err != nil {
				return nil, err
			}
			if at != nil {
				prep["existing"] = at
			}
		}
		return prep, nil
	}
}

// continuePlan plans a writable task that continues to-do from in a new workspace, and starts it.
func continuePlan(t *testing.T, s *Service, from string) {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"tasks": []map[string]any{{"title": "Fix login again", "prompt": "Carry on fixing login",
		"machine": "box", "cwd": "/code/app", "permission": "bypassPermissions", "why": "the repository is on box",
		"workspace": "new", "continues": from, "continues_why": "The first start failed",
		"goal": "login works", "done_when": []string{"a test covers it"}, "expect_minutes": 20}}})
	if _, err := s.toolTaskPlan(context.Background(), "workspace-chat", args); err != nil {
		t.Fatal(err)
	}
	_, plans, err := s.feed(context.Background(), "workspace-chat", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range plans {
		if p.State == planOpen {
			if _, err := s.startPlan(context.Background(), p.ID, nil, false); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no open plan to start: %v", plans)
}

func countCreates(f *fakeHub) int {
	n := 0
	for _, call := range f.worktrees {
		if call["method"] == DeviceWorktreeCreate {
			n++
		}
	}
	return n
}

func TestTaskWorkspaceOwnBranchContinuesWithUntrackedDrafts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := workspaceTaskService(t)
		f.failStart = errors.New("launcher failed")
		p := workspacePlan(t, s, `"new"`)
		if _, err := s.startPlan(context.Background(), p.ID, nil, false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		todo := onlyTodo(s)
		branch, path := "task/"+todo.ID, f.worktrees[len(f.worktrees)-1]["path"].(string)
		if countCreates(f) != 1 || todo.StartError == "" {
			t.Fatalf("fixture: the first start should have made a checkout and failed: %v %+v", f.worktrees, todo)
		}
		asked := ""
		prepare := ownBranchPrepare(func(b string) (map[string]any, error) {
			asked = b
			if b != branch {
				return nil, nil
			}
			return map[string]any{"path": path, "branch": b, "in_checkouts": true}, nil
		})
		f.nodes[DeviceWorktreePrepare] = func(machine string, raw json.RawMessage) (any, error) {
			var in struct {
				Branch         string
				AllowUntracked bool `json:"allow_untracked"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, err
			}
			if in.Branch == branch && !in.AllowUntracked {
				return nil, &rpc.Error{Code: proto.CodeCheckoutDirty, Message: "Untracked draft blocks strict reuse"}
			}
			return prepare(machine, raw)
		}
		f.failStart = nil
		continuePlan(t, s, todo.ID)
		synctest.Wait()
		if asked != branch || countCreates(f) != 1 || len(f.starts) != 2 || f.starts[1]["cwd"] != path {
			t.Fatalf("the leftover was not reused: asked %q creates=%d starts=%v", asked, countCreates(f), f.starts)
		}
		var task PlanTask
		_, plans, _ := s.feed(context.Background(), "workspace-chat", time.Time{})
		for _, p := range plans {
			if p.Tasks[0].Title == "Fix login again" {
				task = p.Tasks[0]
			}
		}
		if r := task.WorkspaceResolved; r == nil || !r.Reused || r.Created || r.Path != path || task.MachinePolicy != machineFixed {
			t.Fatalf("reused checkout not saved truthfully: %+v policy %s", task.WorkspaceResolved, task.MachinePolicy)
		}
	})
}

func TestTaskWorkspaceOwnBranchRefusals(t *testing.T) {
	coded := func(code string, data map[string]string) error {
		b, _ := json.Marshal(data)
		return &rpc.Error{Code: code, Message: code, Data: b}
	}
	for _, tc := range []struct {
		name     string
		existing map[string]any
		err      error
		ownerCwd string
		want     []string
	}{
		{name: "outside the checkouts folder", existing: map[string]any{"path": "/code/app", "in_checkouts": false},
			want: []string{"/code/app", `{"existing": "/code/app"}`, "outside the folder"}},
		{name: "dirty", err: coded(proto.CodeCheckoutDirty, map[string]string{"path": "/wt/app/left", "branch": "b", "files": `"app.txt"`}),
			want: []string{"/wt/app/left", "app.txt", "Commit or stash", "task_retry", "different branch"}},
		{name: "in use", err: coded(proto.CodeCheckoutInUse, map[string]string{"path": "/wt/app/left", "branch": "b", "owner": "writer"}),
			want: []string{"/wt/app/left", "writer"}},
		{name: "unverified", err: coded(proto.CodeCheckoutUnverified, map[string]string{"path": "/wt/app/left", "branch": "b"}),
			want: []string{"/wt/app/left", `{"existing": "/wt/app/left"}`}},
		{name: "another to-do works under it", existing: map[string]any{"path": "/wt/app/left", "in_checkouts": true},
			ownerCwd: "/wt/app/left/sub", want: []string{"Other writer"}},
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
					if err := s.linkTodoAgent(ctx, owner.ID, "box", "writer", owner.Title, tc.ownerCwd, ""); err != nil {
						t.Fatal(err)
					}
				}
				f.answerNode(DeviceWorktreePrepare, ownBranchPrepare(func(b string) (map[string]any, error) {
					if tc.err != nil {
						return nil, tc.err
					}
					at := map[string]any{"branch": b}
					for k, v := range tc.existing {
						at[k] = v
					}
					return at, nil
				}))
				creates := 0
				f.answerNode(DeviceWorktreeCreate, func(string, json.RawMessage) (any, error) {
					creates++
					return proto.WorktreeCreateResult{Path: "/wt/app/new", Branch: "x"}, nil
				})
				p := workspacePlan(t, s, `"new"`)
				if _, err := s.startPlan(ctx, p.ID, nil, false); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				saved, _ := s.loadPlan(ctx, p.ID)
				msg := saved.Tasks[0].Error
				if len(f.starts) != 0 || creates != 0 {
					t.Fatalf("an unusable leftover was taken over or replaced: creates=%d starts=%v", creates, f.starts)
				}
				for _, w := range tc.want {
					if !strings.Contains(msg, w) {
						t.Errorf("refusal %q lacks %q", msg, w)
					}
				}
				if tc.name == "dirty" {
					todo := onlyTodo(s)
					if saved.Tasks[0].ErrorCode != proto.CodeCheckoutDirty || !strings.Contains(todo.StartError, "app.txt") ||
						!strings.Contains(todo.StartError, "task_retry") || !strings.Contains(todo.StartError, "另选分支") {
						t.Fatalf("failed card lost its localized recovery info: %q (code %q)", todo.StartError, saved.Tasks[0].ErrorCode)
					}
				}
			})
		})
	}
}

// A brand-new to-do has no checkout of its branch: the lookup finds none (or an older node ignores the branch) and
// the checkout is made as before, once.
func TestTaskWorkspaceNewTodoBranchStillCreates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := workspaceTaskService(t)
		var branches []string
		f.nodes[DeviceWorktreePrepare] = ownBranchPrepare(func(b string) (map[string]any, error) {
			branches = append(branches, b)
			return nil, nil
		})
		creates := 0
		f.nodes[DeviceWorktreeCreate] = func(_ string, raw json.RawMessage) (any, error) {
			creates++
			var in struct{ Branch, Path string }
			_ = json.Unmarshal(raw, &in)
			return proto.WorktreeCreateResult{WorkspaceID: "ws-new", Path: in.Path, Branch: in.Branch}, nil
		}
		p := workspacePlan(t, s, `"new"`)
		if _, err := s.startPlan(context.Background(), p.ID, nil, false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		todo := onlyTodo(s)
		if creates != 1 || len(f.starts) != 1 || len(branches) != 1 || branches[0] != "task/"+todo.ID {
			t.Fatalf("a new to-do: creates=%d starts=%v lookups=%v", creates, f.starts, branches)
		}
	})
}

// A lookup that fails for another reason (an older node, git unable to list) falls back to making the checkout, and
// a folder already there is then refused with its path and the {existing} next step, the node's code kept.
func TestTaskWorkspaceOwnBranchLookupErrorFallsBackToCreate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := workspaceTaskService(t)
		f.nodes[DeviceWorktreePrepare] = ownBranchPrepare(func(string) (map[string]any, error) {
			return nil, errors.New("git worktree list failed")
		})
		creates := 0
		f.nodes[DeviceWorktreeCreate] = func(_ string, raw json.RawMessage) (any, error) {
			creates++
			var in struct{ Path string }
			_ = json.Unmarshal(raw, &in)
			data, _ := json.Marshal(map[string]string{"path": in.Path})
			return nil, &rpc.Error{Code: proto.CodeWorktreePathTaken, Message: "taken", Data: data}
		}
		p := workspacePlan(t, s, `"new"`)
		if _, err := s.startPlan(context.Background(), p.ID, nil, false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		saved, _ := s.loadPlan(context.Background(), p.ID)
		path := saved.Tasks[0].WorkspaceResolved.Path
		if msg := saved.Tasks[0].Error; creates != 1 || len(f.starts) != 0 || !strings.Contains(msg, `{"existing": "`+path+`"}`) {
			t.Fatalf("lookup failure: creates=%d starts=%v error %q", creates, f.starts, msg)
		}
	})
}

// The hub's sentence keeps the node's coded refusal under it.
func TestTaskCheckoutRefusalKeepsTheNodeCode(t *testing.T) {
	data, _ := json.Marshal(map[string]string{"path": "/wt/app/left", "branch": "task/x"})
	node := &rpc.Error{Code: proto.CodeCheckoutDirty, Message: "dirty", Data: data}
	err := ownCheckoutRefusal(node)
	var re *rpc.Error
	if err == nil || !errors.As(err, &re) || re.Code != proto.CodeCheckoutDirty || !strings.Contains(err.Error(), "/wt/app/left") {
		t.Fatalf("refusal = %v", err)
	}
	if ownCheckoutRefusal(errors.New("other")) != nil || ownCheckoutRefusal(&rpc.Error{Code: "worktree_failed"}) != nil {
		t.Fatal("an unclassified failure must fall back to creation")
	}
	if !nodePathWithin("/wt/app/task-x", "/wt/app") || nodePathWithin("/wt/app-other", "/wt/app") ||
		!nodePathWithin(`c:\WT\app\x`, `C:\wt\app`) || nodePathWithin("/wt/app", "") {
		t.Fatal("nodePathWithin")
	}
}
