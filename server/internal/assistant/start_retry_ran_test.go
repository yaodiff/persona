package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A retry on another launcher starts from the launcher the failed start ran, which the plan does not always name: a
// tier or a start-defaults policy may have chosen it (#10365). Machine and launcher names are placeholders.

// launcherRetryDBHub is launcherRetryHub on a database, which model profiles (a tier) need.
func launcherRetryDBHub(t *testing.T) (*Service, *fakeHub) {
	t.Helper()
	_, f := launcherRetryHub(t)
	return newServiceOn(t, f, todoTestDB(t)), f
}

// retryMoves reports whether conversation's feed records a move of todo's start from launcher from to launcher to.
func retryMoves(s *Service, conversation, todo, from, to string) bool {
	items, _, _ := s.feed(context.Background(), conversation, time.Time{})
	return slices.ContainsFunc(items, func(it FeedItem) bool {
		var b startRemedyBody
		_ = json.Unmarshal(it.Body, &b)
		return it.Kind == feedStartRemedy && it.Ref == todo && b.LauncherFrom == from && b.LauncherTo == to
	})
}

// A task whose tier or start-defaults policy chose launcher work names no launcher itself. "Use claude instead"
// (the app's body, task_retry's launcher) pins claude and drops the tier: the next start runs claude, not work again,
// and the history and task_retry's answer say it moved from work. An uncoded failure (a key the gateway refused) has
// no launcher in its refusal: the hub notes the one that ran.
func TestRetryOnAnotherLauncherOfATaskWhoseLauncherWasChosenForIt(t *testing.T) {
	for _, c := range []struct {
		name    string
		policy  bool // the start-defaults policy's preset chose it, not the task's tier
		failure error
		tool    bool // task_retry, not the app
	}{
		{"tier, the app", false, terminalRefusal(proto.CodeLauncherMissing, "work"), false},
		{"tier, task_retry, uncoded failure", false, errors.New("API Error: 401 invalid x-api-key"), true},
		{"start-defaults policy, the app", true, terminalRefusal(proto.CodeLauncherMissing, "work"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, f := launcherRetryDBHub(t)
			ctx := context.Background()
			conv := "retry_" + randomHex(6)
			setSettings(t, s, func(v *Settings) { v.Main = conv; v.MachineID = "server" })
			saveTier(t, s, "fast", ModelTier{Launcher: "work"})
			task := map[string]any{"title": "Fix the build", "prompt": "Fix the build", "machine": "box", "cwd": "/code/app",
				"permission": "bypassPermissions", "why": "the code is on box"}
			if c.policy {
				stubStartDefaults(s, func(StartFacts) (StartDefaults, bool) {
					return StartDefaults{Presets: []PresetChoice{{Name: "fast"}}, By: "Example"}, true
				})
			} else {
				task["tier"] = "fast"
			}
			f.mu.Lock()
			f.failStart = c.failure
			f.mu.Unlock()
			args, _ := json.Marshal(map[string]any{"tasks": []map[string]any{task}})
			res, err := s.toolTaskPlan(ctx, conv, args)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.startPlan(ctx, planID(t, res), nil, false); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the start to fail", func() bool { return onlyTodo(s).StartError != "" && s.taskStarting.Load() == 0 })
			f.mu.Lock()
			f.failStart = nil
			f.mu.Unlock()
			todo := onlyTodo(s)
			_, p, i, err := s.remedyOf(ctx, todo.ID)
			if err != nil {
				t.Fatal(err)
			}
			if pt := p.Tasks[i]; pt.Launcher != "" || startsOf(f)[0]["launcher"] != "work" || pt.ErrorArgs["launcher"] != "work" ||
				!slices.Contains(pt.RetryWith, "claude") {
				t.Fatalf("the failed task %+v (start %v)", pt, startsOf(f)[0])
			}
			if c.tool {
				args, _ := json.Marshal(map[string]any{"todo_id": todo.ID, "reason": "work's key is refused", "launcher": "claude"})
				res, err := s.toolTaskRetry(ctx, conv, args)
				if err != nil || !strings.Contains(res, "with launcher claude") {
					t.Fatalf("task_retry: %q %v", res, err)
				}
			} else if status, body := serveJSON(t, s, "POST", "/api/v1/todos/"+todo.ID+"/retry", "en", `{"launcher":"claude"}`); status != 200 {
				t.Fatalf("retry: %d %v", status, body)
			}
			waitFor(t, "the second start to end", func() bool { return len(startsOf(f)) == 2 && s.taskStarting.Load() == 0 })
			if second := startsOf(f)[1]; second["launcher"] != "claude" {
				t.Fatalf("asked for claude, started %v", second)
			}
			_, p, i, _ = s.remedyOf(ctx, todo.ID)
			if pt := p.Tasks[i]; pt.Launcher != "claude" || pt.Tier != "" || !strings.Contains(pt.Why, "改用 claude 重新开始：work 在 box 上没开起来") {
				t.Fatalf("plan task %+v", pt)
			}
			if !retryMoves(s, conv, todo.ID, "work", "claude") {
				t.Fatalf("the move from work is not in the task's history: %s", feedKinds(s, conv))
			}
		})
	}
}

// A retry on another launcher that runs the same agent keeps the task's own permission mode: a Codex task planned in
// plan mode stays in plan mode on another Codex launcher, whatever the owner's default mode (#10365). Another agent
// settles it again (TestTaskRetrySuggestsNoManagedLauncherWhenPersonadMissing: Allow all on Claude Code).
func TestRetryOnAnotherLauncherOfTheSameAgentKeepsThePermissionMode(t *testing.T) {
	s, f := launcherRetryHub(t)
	ctx := context.Background()
	f.mu.Lock()
	f.launcherList = append(f.launcherList, proto.Launcher{Name: "cx2", Agent: "codex"})
	f.failStart = terminalRefusal(proto.CodeLauncherMissing, "cx")
	f.mu.Unlock()
	setSettings(t, s, func(v *Settings) { v.DefaultPermissionMode = "bypassPermissions" })
	res, err := s.toolTaskPlan(ctx, "conv1", json.RawMessage(`{"tasks":[{"title":"Plan the fix","prompt":"Plan the fix","machine":"box",
		"cwd":"/code/app","launcher":"cx","permission":"plan","why":"the code is on box"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.startPlan(ctx, planID(t, res), nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the start to fail", func() bool { return onlyTodo(s).StartError != "" && s.taskStarting.Load() == 0 })
	f.mu.Lock()
	f.failStart = nil
	f.mu.Unlock()
	todo := onlyTodo(s)
	if first := startsOf(f)[0]; first["launcher"] != "cx" || first["permission_mode"] != "plan" {
		t.Fatalf("first start %v", first)
	}
	if status, body := serveJSON(t, s, "POST", "/api/v1/todos/"+todo.ID+"/retry", "en", `{"launcher":"cx2"}`); status != 200 {
		t.Fatalf("retry: %d %v", status, body)
	}
	waitFor(t, "the second start to end", func() bool { return len(startsOf(f)) == 2 && s.taskStarting.Load() == 0 })
	if second := startsOf(f)[1]; second["launcher"] != "cx2" || second["permission_mode"] != "plan" {
		t.Fatalf("second start %v", second)
	}
	_, p, i, _ := s.remedyOf(ctx, todo.ID)
	if pt := p.Tasks[i]; pt.Launcher != "cx2" || pt.Permission != "plan" {
		t.Fatalf("plan task %+v", pt)
	}
}
