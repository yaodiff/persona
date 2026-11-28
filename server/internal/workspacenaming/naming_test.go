package workspacenaming

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
)

func source() Source {
	id, _ := repository.Parse("https://github.com/example/web", "")
	return Source{Manager: "example", TodoID: "todo_123", Title: "Fix / Login.. Bug", MachineID: "computer", PlannedAt: time.Date(2026, 11, 13, 16, 30, 0, 0, time.UTC), Timezone: "Asia/Shanghai", Rules: Defaults(), Layout: repository.LayoutContainer,
		Repositories: repository.PreparationSnapshot{SchemaVersion: 1, Manager: "example", TodoID: "todo_123", Repositories: []repository.PreparationRepository{{RepoID: "web", RequestedRef: repository.RequestedRef{Kind: "default"}, SourceSnapshot: repository.SourceSnapshot{Identity: id}}}}}
}
func TestDeterministicOperationAndLayouts(t *testing.T) {
	s := source()
	s.Rules.TaskDirectoryTemplate = "{yyyy}{mmdd}/{task-id}-{task-slug}"
	p := Resolve(s, NodeContext{Home: "/home/example", PersonaHome: "/home/example/.persona"})
	if p.Plan == nil {
		t.Fatal(p.Diagnostics)
	}
	if p.Plan.RootPath != "/home/example/workspace/20261114/todo_123-fix-login-bug" || p.Plan.Repositories[0].RelativePath != "web" {
		t.Fatal(p.Plan)
	}
	if p.Plan.Repositories[0].RequestedRef.Kind != "default" {
		t.Fatal("ref lost")
	}
	raw, _ := json.Marshal(p.Plan)
	var retry Plan
	if err := json.Unmarshal(raw, &retry); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*p.Plan, retry) {
		t.Fatal("persisted operation changed")
	}
	s.Title = "Changed tomorrow"
	s.PlannedAt = s.PlannedAt.Add(48 * time.Hour)
	if retry.RootPath != p.Plan.RootPath || retry.Digest != p.Plan.Digest {
		t.Fatal("retry renamed")
	}
	s = source()
	s.Layout = repository.LayoutRoot
	p = Resolve(s, NodeContext{Home: "/Users/example"})
	if p.Plan == nil || p.Plan.Repositories[0].RelativePath != "." || p.Plan.Repositories[0].AbsolutePath != p.Plan.RootPath {
		t.Fatal(p)
	}
}
func TestTypedRefsAndOnlyUsedFields(t *testing.T) {
	for _, ref := range []repository.RequestedRef{{Kind: "default"}, {Kind: "tag", Value: "v1"}, {Kind: "ref", Value: "refs/pull/7/head"}, {Kind: "commit", Value: strings.Repeat("a", 40)}, {Kind: "branch", Value: "feature/login"}} {
		s := source()
		s.Repositories.Repositories[0].RequestedRef = ref
		p := Resolve(s, NodeContext{Home: "/home/example"})
		if p.Plan == nil || p.Plan.Repositories[0].RequestedRef != ref {
			t.Fatal(ref, p)
		}
		s.Rules.BranchTemplate = "persona/{branch}/{base-branch}"
		p = Resolve(s, NodeContext{Home: "/home/example"})
		if ref.Kind == "branch" {
			if p.Plan == nil || p.Plan.Repositories[0].TaskBranch != "persona/feature-login/feature-login" {
				t.Fatal(p)
			}
		} else if p.Status != "incomplete" {
			t.Fatal(ref, p)
		}
	}
}

func TestFolderOnlyWorkspaceLayout(t *testing.T) {
	s := source()
	s.Rules.BranchTemplate = "persona/{base-branch}/{task-id}"
	s.Repositories.Repositories = nil
	n := NodeContext{Home: "/home/example"}
	preview := Resolve(s, n)
	if preview.Status != "complete" || preview.Plan == nil || preview.Plan.RootPath != "/home/example/workspace/task-todo_123" || len(preview.Plan.Repositories) != 0 {
		t.Fatal(preview)
	}
	s.Layout = repository.LayoutRoot
	if got := Resolve(s, n); got.Plan != nil || got.Status != "invalid" {
		t.Fatal("root layout accepted without a repository", got)
	}
	plan := *preview.Plan
	plan.Source.Layout = repository.LayoutRoot
	plan.Digest = Digest(plan)
	if err := CheckPlan(plan); err == nil {
		t.Fatal("a matching digest accepted an invalid root layout")
	}
}
func TestTemplateBoundaries(t *testing.T) {
	for _, template := range []string{"../x", "a/../b", "/absolute", "a//b", "a\\b", "{repo}", "{branch}", "{unknown}", "{task-id", "$(whoami)", ".persona/x", "C:/x"} {
		s := source()
		s.Rules.TaskDirectoryTemplate = template
		if p := Resolve(s, NodeContext{Home: "/home/example"}); p.Plan != nil {
			t.Fatal("accepted", template, p)
		}
	}
	s := source()
	s.Rules.BaseDirectory = "/home/example/.persona/data"
	if Resolve(s, NodeContext{Home: "/home/example", PersonaHome: "/home/example/.persona"}).Plan != nil {
		t.Fatal("state directory accepted")
	}
	s = source()
	s.Repositories.Repositories = append(s.Repositories.Repositories, s.Repositories.Repositories[0])
	s.Layout = repository.LayoutRoot
	if Resolve(s, NodeContext{Home: "/home/example"}).Plan != nil {
		t.Fatal("multi root accepted")
	}
}
func TestRulePatchInheritanceAndAtomicErrors(t *testing.T) {
	cur := Rules{BranchTemplate: "custom/{task-id}"}
	got, err := Patch(cur, []byte(`{"base_directory":"/work","branch_template":null}`))
	if err != nil || got.BaseDirectory != "/work" || got.BranchTemplate != "" {
		t.Fatal(got, err)
	}
	effective, sources := Effective(got, Rules{BranchTemplate: "task/{task-id}"})
	if effective.BaseDirectory != "/work" || effective.BranchTemplate != "task/{task-id}" || sources["task_directory_template"] != "default" {
		t.Fatal(effective, sources)
	}
	for _, raw := range []string{`{"branch_template":""}`, `{"unknown":"x"}`, `{"branch_template":"a","branch_template":"b"}`, `[]`, `{"task_directory_template":"{branch}"}`} {
		if _, err := Patch(cur, []byte(raw)); err == nil {
			t.Fatal("accepted", raw)
		}
	}
	if got, err := Patch(cur, []byte(`null`)); err != nil || got != (Rules{}) {
		t.Fatal(got, err)
	}
}

func TestFrozenPlanVerificationDoesNotUseManagerPathFlavor(t *testing.T) {
	p := Resolve(source(), NodeContext{Home: "/home/example"}).Plan
	p.Node = NodeContext{Home: `C:\Users\example`, PathFlavor: "windows"}
	p.BasePath = `C:\Users\example\workspace`
	p.RootPath = p.BasePath + `\task-todo_123`
	p.Repositories[0].AbsolutePath = p.RootPath + `\web`
	p.Digest = Digest(*p)
	if err := CheckPlan(*p); err != nil {
		t.Fatal("snapshot was re-resolved on the manager", err)
	}
	p.RootPath += "-tampered"
	if err := CheckPlan(*p); err == nil {
		t.Fatal("tampered snapshot accepted")
	}
}

func TestWorkspaceNamingContextUsesTargetPlatform(t *testing.T) {
	if runtimeFlavor() != "posix" {
		t.Skip("POSIX host control for the Windows path tests")
	}
	s := source()
	s.Rules.BaseDirectory = "C:/work"
	if err := Validate(s.Rules); err != nil {
		t.Fatal("settings must permit a Windows target", err)
	}
	for _, tc := range []struct {
		node NodeContext
		code string
	}{
		{NodeContext{Home: "C:/Users/example", PathFlavor: "windows"}, "context_invalid"},
		{NodeContext{Home: "/home/example"}, "base_scope_denied"},
	} {
		p := Resolve(s, tc.node)
		if p.Plan != nil || len(p.Diagnostics) != 1 || p.Diagnostics[0].Code != tc.code {
			t.Fatal("host path handling was mistaken for Windows resolution", p)
		}
	}
}

func TestRejectNamespaceBaseTemplatesOnAnyHost(t *testing.T) {
	// Settings may be saved on a different OS from the target Node. They must
	// reject namespace syntax there too, before target-specific resolution.
	for _, base := range []string{"//?/C:/private-state/work", "//?/UNC/files.example.com/share/work", "//?/unc/files.example.com/share/work", "//./C:/work", "/??/C:/work"} {
		if err := Validate(Rules{BaseDirectory: base}); err == nil {
			t.Fatalf("accepted namespace base %q", base)
		}
	}
}

func TestTaskDirectoryExpansionReservedNames(t *testing.T) {
	for _, layout := range []repository.Layout{repository.LayoutRoot, repository.LayoutContainer} {
		for _, title := range []string{"NUL", "CON", "COM1", "LPT9"} {
			s := source()
			s.Layout, s.Title = layout, title
			s.Rules.TaskDirectoryTemplate = "{task-id}/{task-slug}/work"
			if err := Validate(s.Rules); err != nil {
				t.Fatal("title is unavailable during static validation", err)
			}
			p := Resolve(s, NodeContext{Home: "/home/example"})
			if p.Status != "invalid" || p.Plan != nil || len(p.Diagnostics) != 1 || p.Diagnostics[0].Code != "path_invalid" || p.Diagnostics[0].Field != "task_directory_template" {
				t.Errorf("reserved expansion produced a plan: %s, %s: %+v", layout, title, p)
			}
			// An unused reserved title must not invalidate an ordinary task path,
			// and a Git branch does not use directory-segment restrictions.
			s.Rules.TaskDirectoryTemplate = Defaults().TaskDirectoryTemplate
			s.Rules.BranchTemplate = "persona/NUL"
			if p := Resolve(s, NodeContext{Home: "/home/example"}); p.Plan == nil {
				t.Fatal("unused title or valid Git branch rejected", p)
			}
		}
		for _, path := range portableTaskDirectories() {
			s := source()
			s.Layout, s.Rules.TaskDirectoryTemplate = layout, path
			if p := Resolve(s, NodeContext{Home: "/home/example"}); p.Status != "complete" || p.Plan == nil {
				t.Errorf("ordinary POSIX path rejected: %q: %+v", path, p)
			}
		}
	}
}
