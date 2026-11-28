package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentStartPassesModelAliases(t *testing.T) {
	s, f := newTaskService(t)
	if _, err := s.runTool(t.Context(), "server", "conv9", "agent_start", json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"Inspect","model_aliases":{"sonnet_model":"vendor/standard","haiku_model":"vendor/fast"}}`)); err != nil {
		t.Fatal(err)
	}
	if len(f.starts) != 1 {
		t.Fatalf("starts: %d", len(f.starts))
	}
	raw, _ := json.Marshal(f.starts[0]["model_aliases"])
	if string(raw) != `{"haiku_model":"vendor/fast","sonnet_model":"vendor/standard"}` {
		t.Fatalf("node aliases: %s", raw)
	}
}

// agent_start with worktree: the machine's engine makes the worktree first (named for the branch the way the dialog
// names it, from the workspace cwd is in), then the agent starts in it, in that new workspace.
func TestAgentStartInANewWorktree(t *testing.T) {
	s, f := newTaskService(t)
	res, err := s.runTool(context.Background(), "server", "conv9", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"修登录","worktree":{"branch":"fix/Login"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.worktrees) != 2 || f.worktrees[0]["method"] != DeviceWorktreePrepare || f.worktrees[0]["cwd"] != "/code/app" ||
		f.worktrees[1]["method"] != DeviceWorktreeCreate || f.worktrees[1]["cwd"] != "/code/app" ||
		f.worktrees[1]["branch"] != "fix/Login" || f.worktrees[1]["path"] != "/wt/app/fix-login" {
		t.Fatalf("worktree calls %v", f.worktrees)
	}
	if f.worktrees[1]["for_agent"] != true { // the node marks its first shell as Persona's (#10624)
		t.Fatalf("worktree.create without for_agent: %v", f.worktrees[1])
	}
	if len(f.starts) != 1 || f.starts[0]["cwd"] != "/wt/app/fix-login" || f.starts[0]["workspace_id"] != "ws-new" {
		t.Fatalf("starts %v", f.starts)
	}
	if !strings.Contains(res, "/wt/app/fix-login") || !strings.Contains(res, "new worktree on branch fix/Login") {
		t.Fatalf("answer does not say where it runs:\n%s", res)
	}
}

// A worktree the engine refuses (a branch checked out elsewhere…) starts no agent, and says why.
func TestAgentStartWorktreeRefusedStartsNothing(t *testing.T) {
	s, f := newTaskService(t)
	f.failWorktree = errors.New("分支 dev 已经在 /code/app 检出了")
	_, err := s.runTool(context.Background(), "server", "conv9", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"修登录","worktree":{"branch":"dev"}}`))
	if err == nil || !strings.Contains(err.Error(), "已经在") || !strings.Contains(err.Error(), "no agent was started") {
		t.Fatalf("err = %v", err)
	}
	if len(f.starts) != 0 {
		t.Fatalf("an agent was started: %v", f.starts)
	}
}

// Without worktree nothing changes: no worktree is made and the agent starts in cwd.
func TestAgentStartWithoutWorktreeMakesNone(t *testing.T) {
	s, f := newTaskService(t)
	if _, err := s.runTool(context.Background(), "server", "conv9", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI"}`)); err != nil {
		t.Fatal(err)
	}
	if len(f.worktrees) != 0 || len(f.starts) != 1 || f.starts[0]["cwd"] != "/code/app" || f.starts[0]["workspace_id"] != nil {
		t.Fatalf("worktrees %v, starts %v", f.worktrees, f.starts)
	}
}

// A branch with letters the slug drops (a Chinese name) still gets a folder of its own under the engine's worktrees
// directory: never that directory itself (an empty slug), nor another branch's (fix/登录 and fix/注册 both slug to
// "fix"); an ASCII branch keeps the dialog's name (#5775).
func TestAgentStartWorktreeForABranchWithNoASCIIName(t *testing.T) {
	paths := map[string]string{}
	for _, branch := range []string{"修复登录", "需求/重构", "fix/登录", "fix/注册"} {
		s, f := newTaskService(t)
		if _, err := s.runTool(context.Background(), "server", "conv9", "agent_start",
			json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"修登录","worktree":{"branch":"`+branch+`"}}`)); err != nil {
			t.Fatal(err)
		}
		if len(f.worktrees) != 2 {
			t.Fatalf("%s: worktree calls %v", branch, f.worktrees)
		}
		p := f.worktrees[1]["path"].(string)
		if filepath.Dir(p) != "/wt/app" || filepath.Base(p) == "" || filepath.Base(p) == "app" {
			t.Fatalf("%s: checkout at %q, want a folder of its own in /wt/app", branch, p)
		}
		paths[branch] = p
	}
	seen := map[string]string{}
	for b, p := range paths {
		if other, dup := seen[p]; dup {
			t.Fatalf("%s and %s share the checkout %q", b, other, p)
		}
		seen[p] = b
	}
}
