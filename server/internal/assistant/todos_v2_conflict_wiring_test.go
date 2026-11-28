package assistant

import (
	"strings"
	"testing"
)

func TestTodosV2ConflictOffBypass(t *testing.T) {
	s, _ := transferEnv(t)

	// Default todos_v2=false: dispatch two same-repo tasks with overlapping write_prefixes and changed_files.
	out1, bad := dispatchToolFrom(t, s, "lead0001", "task_plan", dispatchTaskArgs("Off task A", map[string]any{
		"cwd":            "/code/app",
		"write_prefixes": []string{"server/internal/assistant"},
		"changed_files":  []string{"server/internal/assistant/tasks_plan.go"},
	}))
	if bad {
		t.Fatalf("first task_plan failed: %s", out1)
	}
	waitFor(t, "first dispatch", func() bool { return s.taskStarting.Load() == 0 })

	out2, bad := dispatchToolFrom(t, s, "lead0001", "task_plan", dispatchTaskArgs("Off task B", map[string]any{
		"cwd":            "/code/app",
		"write_prefixes": []string{"server/internal/assistant/todo"},
		"changed_files":  []string{"server/internal/assistant/tasks_plan.go"},
	}))
	if bad {
		t.Fatalf("second task_plan failed: %s", out2)
	}
	waitFor(t, "second dispatch", func() bool { return s.taskStarting.Load() == 0 })

	if strings.Contains(out1, "Write-scope conflicts") || strings.Contains(out2, "Write-scope conflicts") {
		t.Fatalf("todos_v2=false must 100%% bypass conflict advisories:\nout1=%s\nout2=%s", out1, out2)
	}
}

func TestTodosV2ConflictSameRepoStaticOverlap(t *testing.T) {
	s, _ := transferEnv(t)
	setTodosV2ForTest(t, s, true, false)

	// 1. Dispatch an active task declaring write_prefixes via JSON field.
	firstTodo := dispatchNewTodo(t, s, "lead0001", dispatchTaskArgs("Active assistant refactor", map[string]any{
		"cwd":            "/code/app",
		"write_prefixes": []string{"server/internal/assistant"},
	}))

	// 2. Plan a second task in the same repo with overlapping subdirectory prefix.
	out, bad := dispatchToolFrom(t, s, "lead0001", "task_plan", dispatchTaskArgs("Subdir todo wiring", map[string]any{
		"cwd":            "/code/app",
		"write_prefixes": []string{"server/internal/assistant/todo"},
		"prompt":         "Update the to-do wiring.\nforce_parallel: true",
	}))
	if bad {
		t.Fatalf("second task_plan failed: %s", out)
	}
	waitFor(t, "second dispatch", func() bool { return s.taskStarting.Load() == 0 })

	for _, want := range []string{
		"Write-scope conflicts (same repository):",
		firstTodo.ID,
		"layer=static",
		"action=serialize",
		"server/internal/assistant/todo",
		"recommend running serially",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected conflict advisory to contain %q, got:\n%s", want, out)
		}
	}

	out, bad = dispatchToolFrom(t, s, "lead0001", "task_plan", dispatchTaskArgs("Text-only scope", map[string]any{
		"prompt":  "Review the example argument.\nwrite_prefixes: server/internal/assistant/todo",
		"context": "changed_files: server/internal/assistant/todo/plan.go",
		"goal":    "Verify the report.\nwrite_prefixes: server/internal/assistant",
	}))
	if bad || strings.Contains(out, "Write-scope conflicts") {
		t.Fatalf("free-form task text must not declare a write scope: %s", out)
	}
	waitFor(t, "text-only dispatch", func() bool { return s.taskStarting.Load() == 0 })
}

func TestTodosV2ConflictCrossRepoNoConflict(t *testing.T) {
	s, _ := transferEnv(t)
	setTodosV2ForTest(t, s, true, false)

	_ = dispatchNewTodo(t, s, "lead0001", dispatchTaskArgs("Repo A task", map[string]any{
		"cwd":            "/code/repo-a",
		"write_prefixes": []string{"src/shared"},
		"changed_files":  []string{"src/shared/index.ts"},
	}))

	out, bad := dispatchToolFrom(t, s, "lead0001", "task_plan", dispatchTaskArgs("Repo B task", map[string]any{
		"cwd":            "/code/repo-b",
		"write_prefixes": []string{"src/shared"},
		"changed_files":  []string{"src/shared/index.ts"},
	}))
	if bad {
		t.Fatalf("cross-repo task_plan failed: %s", out)
	}
	waitFor(t, "cross-repo dispatch", func() bool { return s.taskStarting.Load() == 0 })

	if strings.Contains(out, "Write-scope conflicts") {
		t.Fatalf("cross-repo tasks must not conflict, got:\n%s", out)
	}
}

func TestTodosV2ConflictForceParallelOverridesStatic(t *testing.T) {
	s, _ := transferEnv(t)
	setTodosV2ForTest(t, s, true, false)

	firstTodo := dispatchNewTodo(t, s, "lead0001", dispatchTaskArgs("Web components task", map[string]any{
		"cwd":            "/code/app",
		"write_prefixes": []string{"web/src/components"},
	}))

	out, bad := dispatchToolFrom(t, s, "lead0001", "task_plan", dispatchTaskArgs("Parallel button tweak", map[string]any{
		"cwd":            "/code/app",
		"write_prefixes": []string{"web/src/components/button"},
		"force_parallel": true,
	}))
	if bad {
		t.Fatalf("force_parallel task_plan failed: %s", out)
	}
	waitFor(t, "force_parallel dispatch", func() bool { return s.taskStarting.Load() == 0 })

	for _, want := range []string{
		"Write-scope conflicts (same repository):",
		firstTodo.ID,
		"layer=static",
		"action=warn_overridden",
		"static overlap explicitly allowed via force_parallel",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected ActionWarnOverridden advisory containing %q, got:\n%s", want, out)
		}
	}
}

func TestTodosV2ConflictDeclaredFileIntersectionAlwaysSerializes(t *testing.T) {
	s, _ := transferEnv(t)
	setTodosV2ForTest(t, s, true, false)

	// The active task declares its scope and the files it changed through structured fields.
	activeTodo := dispatchNewTodo(t, s, "lead0001", dispatchTaskArgs("Active runtime editor", map[string]any{
		"cwd":            "/code/app",
		"write_prefixes": []string{"server/a"},
		"changed_files":  []string{"server/shared/util.go"},
	}))

	// Planned task declares disjoint static prefix "server/b", sets force_parallel=true,
	// and declares the same file "server/shared/util.go".
	out, bad := dispatchToolFrom(t, s, "lead0001", "task_plan", dispatchTaskArgs("Conflicting runtime editor", map[string]any{
		"cwd":            "/code/app",
		"write_prefixes": []string{"server/b"},
		"changed_files":  []string{"server/shared/util.go"},
		"force_parallel": true,
	}))
	if bad {
		t.Fatalf("runtime conflict task_plan failed: %s", out)
	}
	waitFor(t, "runtime conflict dispatch", func() bool { return s.taskStarting.Load() == 0 })

	for _, want := range []string{
		"Write-scope conflicts (same repository):",
		activeTodo.ID,
		"layer=runtime",
		"action=serialize",
		"server/shared/util.go",
		"force_parallel does not override runtime intersections",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected runtime ActionSerialize advisory containing %q, got:\n%s", want, out)
		}
	}
}

func TestTodosV2ConflictRollbackToOff(t *testing.T) {
	s, _ := transferEnv(t)
	setTodosV2ForTest(t, s, true, false)

	// 1. With todos_v2=true, static overlap emits conflict advisory.
	_ = dispatchNewTodo(t, s, "lead0001", dispatchTaskArgs("Rollback base task", map[string]any{
		"cwd":            "/code/app",
		"write_prefixes": []string{"server/internal/assistant"},
	}))
	outOn, bad := dispatchToolFrom(t, s, "lead0001", "task_plan", dispatchTaskArgs("Rollback overlapping ON", map[string]any{
		"cwd":            "/code/app",
		"write_prefixes": []string{"server/internal/assistant"},
	}))
	if bad {
		t.Fatalf("task_plan with todos_v2=true failed: %s", outOn)
	}
	waitFor(t, "rollback on dispatch", func() bool { return s.taskStarting.Load() == 0 })
	if !strings.Contains(outOn, "Write-scope conflicts (same repository):") {
		t.Fatalf("expected conflict advisory while todos_v2=true, got:\n%s", outOn)
	}

	// 2. Toggle todos_v2 back to false: immediate lossless rollback, zero conflict detection.
	setTodosV2ForTest(t, s, false, true)

	outOff, bad := dispatchToolFrom(t, s, "lead0001", "task_plan", dispatchTaskArgs("Rollback overlapping OFF", map[string]any{
		"cwd":            "/code/app",
		"write_prefixes": []string{"server/internal/assistant"},
	}))
	if bad {
		t.Fatalf("task_plan after rollback failed: %s", outOff)
	}
	waitFor(t, "rollback off dispatch", func() bool { return s.taskStarting.Load() == 0 })
	if strings.Contains(outOff, "Write-scope conflicts") {
		t.Fatalf("expected zero conflict advisory after rolling back to todos_v2=false, got:\n%s", outOff)
	}
}
