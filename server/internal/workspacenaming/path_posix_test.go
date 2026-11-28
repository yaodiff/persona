//go:build !windows

package workspacenaming

import (
	"runtime"
	"testing"
)

func TestPrivateStateContainmentTargetCaseRules(t *testing.T) {
	for _, tc := range []struct {
		name, state, path string
		darwin, linux     bool
	}{
		{"mixed base", "/Users/example/.persona", "/Users/example/.PERSONA/work", true, false},
		{"mixed parents", "/Users/example/.persona", "/users/EXAMPLE/.PeRsOnA/work", true, false},
		{"custom root", "/Users/example/WORK/tasks", "/Users/example/work/tasks/task-example", true, false},
		{"equal", "/Users/example/.persona", "/Users/example/.persona", true, true},
		{"descendant", "/Users/example/.persona", "/Users/example/.persona/work", true, true},
		{"sibling prefix", "/Users/example/.persona", "/Users/example/.personality/work", false, false},
		{"mixed sibling prefix", "/Users/example/.persona", "/Users/example/.PERSONA-backup/work", false, false},
		{"outside", "/Users/example/.persona", "/Users/example/work", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for goos, want := range map[string]bool{"darwin": tc.darwin, "linux": tc.linux} {
				if got := withinOS(tc.state, tc.path, goos); got != want {
					t.Errorf("%s: got %v, want %v", goos, got, want)
				}
			}
		})
	}
}

// On macOS these exercise Resolve itself on the native platform branch. Linux
// checks that distinct case-sensitive directories are not treated as private.
func TestResolvePrivateStateTargetCaseRules(t *testing.T) {
	for _, tc := range []struct{ name, state, base, task, code string }{
		{"base", "/Users/example/.persona", "/Users/example/.PERSONA/work", "task-{task-id}", "base_scope_denied"},
		{"root", "/Users/example/WORK/tasks", "/Users/example/work", "tasks/{task-id}", "path_invalid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := source()
			s.Rules.BaseDirectory, s.Rules.TaskDirectoryTemplate = tc.base, tc.task
			p := Resolve(s, NodeContext{Home: "/Users/example", PersonaHome: tc.state})
			if runtime.GOOS == "darwin" {
				if p.Plan != nil || p.Status != "invalid" || len(p.Diagnostics) != 1 || p.Diagnostics[0].Code != tc.code {
					t.Fatal("private path accepted", p)
				}
			} else if runtime.GOOS == "linux" && p.Plan == nil {
				t.Fatal("distinct Linux path rejected", p)
			}
		})
	}
}

func TestLiteralBaseUnderPhysicalHome(t *testing.T) {
	ctx := NodeContext{Home: "/var/home/example", PersonaHome: "/var/home/example/state"}
	const physicalHome = "/private/var/home/example"
	s := source()
	s.Rules.BaseDirectory = ctx.Home + "/tasks"
	p := ResolveWithPhysicalHome(s, ctx, physicalHome)
	if p.Plan == nil || p.Plan.BasePath != physicalHome+"/tasks" ||
		p.Plan.Repositories[0].AbsolutePath != physicalHome+"/tasks/task-todo_123/web" ||
		p.Plan.Source.Rules.BaseDirectory != s.Rules.BaseDirectory || CheckPlan(*p.Plan) != nil {
		t.Fatal(p)
	}

	// Existing plans outside the home retain their digest, even with a common name prefix.
	s.Rules.BaseDirectory = ctx.Home + "-shared/tasks"
	old := Resolve(s, ctx)
	current := ResolveWithPhysicalHome(s, ctx, physicalHome)
	if old.Plan == nil || current.Plan == nil || old.Plan.Digest != current.Plan.Digest {
		t.Fatal(old, current)
	}
}
