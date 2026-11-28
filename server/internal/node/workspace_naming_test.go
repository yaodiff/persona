package node

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

func TestWorkspaceNamingUsesTargetHomeWithoutEngineOrCreation(t *testing.T) {
	for range 2 {
		home := workspaceTemp(t)
		n := &Node{home: home, dir: filepath.Join(home, ".persona")}
		source := workspacenaming.Source{TodoID: "todo_example", Title: "Example", PlannedAt: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC), Timezone: "UTC", Rules: workspacenaming.Defaults(), Layout: repositorywire.LayoutRoot, Repositories: repositorywire.PreparationSnapshot{Repositories: []repositorywire.PreparationRepository{{RepoID: "web", RequestedRef: repositorywire.RequestedRef{Kind: "default"}}}}}
		raw, _ := json.Marshal(source)
		out, err := n.handle(t.Context(), "workspace.naming.resolve", raw)
		if err != nil {
			t.Fatal(err)
		}
		preview := out.(workspacenaming.Preview)
		if preview.Plan == nil || preview.Plan.RootPath != filepath.Join(home, "workspace", "task-todo_example") || preview.Plan.Repositories[0].RelativePath != "." {
			t.Fatal(preview)
		}
		files, err := os.ReadDir(home)
		if err != nil || len(files) != 0 {
			t.Fatal("preview created resources", files, err)
		}
	}
}

func TestWorkspaceNamingRejectsPrivateSymlinkAncestors(t *testing.T) {
	for _, level := range []string{"base", "task", "repository"} {
		for _, target := range []string{"state", "ssh", "missing-ssh", "missing-step-secret"} {
			t.Run(level+"/"+target, func(t *testing.T) {
				home := workspaceTemp(t)
				state := filepath.Join(workspaceTemp(t), "private-state")
				if err := os.Mkdir(state, 0o700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PERSONA_HOME", state)
				n := &Node{home: home, dir: state}
				to := state
				if target != "state" {
					to = filepath.Join(home, ".ssh")
					if target == "ssh" {
						if err := os.Mkdir(to, 0o700); err != nil {
							t.Fatal(err)
						}
					}
					if target == "missing-step-secret" {
						to = filepath.Join(home, "missing") + string(filepath.Separator) + ".." + string(filepath.Separator) + ".ssh"
					}
				}
				base := filepath.Join(home, "workspace")
				at := base
				if level == "task" {
					at = filepath.Join(base, "task-todo_example")
				}
				if level == "repository" {
					at = filepath.Join(base, "task-todo_example", "web")
				}
				if err := os.MkdirAll(filepath.Dir(at), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(to, at); err != nil {
					t.Skip("symlinks:", err)
				}
				s := namingGuardSource()
				before := namingGuardTree(t, home, state)
				got := namingGuardPreview(t, n, s)
				if got.Plan != nil || got.Status != "invalid" || len(got.Diagnostics) != 1 || got.Diagnostics[0].Code != "base_scope_denied" {
					t.Fatalf("%s link into %s accepted: %+v", level, target, got)
				}
				if level == "repository" && got.Diagnostics[0].RepoID != "web" {
					t.Fatal("missing repository attribution", got)
				}
				if !reflect.DeepEqual(before, namingGuardTree(t, home, state)) {
					t.Fatal("naming created or changed resources")
				}
			})
		}
	}
}

func TestWorkspaceNamingResolvesPrivateStateItself(t *testing.T) {
	home, state := workspaceTemp(t), workspaceTemp(t)
	t.Setenv("PERSONA_HOME", state)
	alias := filepath.Join(home, "state-alias")
	if err := os.Symlink(state, alias); err != nil {
		t.Skip("symlinks:", err)
	}
	if err := os.Symlink(state, filepath.Join(home, "workspace")); err != nil {
		t.Fatal(err)
	}
	s := namingGuardSource()
	s.Layout, s.Repositories.Repositories = repositorywire.LayoutRoot, s.Repositories.Repositories[:1]
	n := &Node{home: home, dir: alias}
	before := namingGuardTree(t, home, state)
	got := namingGuardPreview(t, n, s)
	if got.Plan != nil || got.Status != "invalid" || len(got.Diagnostics) != 1 || got.Diagnostics[0].Code != "base_scope_denied" {
		t.Fatal("physical private state accepted", got)
	}
	if !reflect.DeepEqual(before, namingGuardTree(t, home, state)) {
		t.Fatal("preview changed resources")
	}
}

func TestWorkspaceNamingMissingPrivateStateBehindSymlink(t *testing.T) {
	for _, link := range []string{"ancestor", "state-root"} {
		for _, level := range []string{"base", "task", "repository"} {
			for _, target := range []string{"state", "descendant", "sibling-prefix"} {
				t.Run(link+"/"+level+"/"+target, func(t *testing.T) {
					home, physical := workspaceTemp(t), physicalTempDir(t)
					physicalState := filepath.Join(physical, "missing", "private-state")
					alias := filepath.Join(home, "state-alias")
					state, to := alias, physicalState
					if link == "ancestor" {
						state, to = filepath.Join(alias, "missing", "private-state"), physical
					}
					if err := os.Symlink(to, alias); err != nil {
						t.Skip("symlinks:", err)
					}
					t.Setenv("PERSONA_HOME", state)
					n := &Node{home: home, dir: state}
					resolved, err := n.realPath(state)
					if !errors.Is(err, fs.ErrNotExist) || resolved != physicalState {
						t.Fatalf("missing state = %q, %v; want physical tail %q", resolved, err, physicalState)
					}
					to = physicalState
					if target == "descendant" {
						to = filepath.Join(to, "project")
					} else if target == "sibling-prefix" {
						to += "-projects"
					}
					at := filepath.Join(home, "workspace")
					if level != "base" {
						at = filepath.Join(at, "task-todo_example")
					}
					if level == "repository" {
						at = filepath.Join(at, "web")
					}
					if err := os.MkdirAll(filepath.Dir(at), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(to, at); err != nil {
						t.Fatal(err)
					}
					source := namingGuardSource()
					if lexical := workspacenaming.Resolve(source, workspacenaming.NodeContext{Home: home, PersonaHome: state}); lexical.Plan == nil {
						t.Fatal("fixture did not reach the physical guard", lexical)
					}
					before := namingGuardTree(t, home, physical)
					got := namingGuardPreview(t, n, source)
					code := "base_scope_denied"
					if target == "sibling-prefix" {
						code = "path_symlink"
					}
					if got.Plan != nil || got.Status != "invalid" || len(got.Diagnostics) != 1 || got.Diagnostics[0].Code != code {
						t.Fatal("workspace link was accepted or misclassified", got)
					}
					if level == "repository" && got.Diagnostics[0].RepoID != "web" {
						t.Fatal("missing repository attribution", got)
					}
					if !reflect.DeepEqual(before, namingGuardTree(t, home, physical)) {
						t.Fatal("preview changed resources")
					}
				})
			}
		}
	}
}

func TestWorkspaceNamingFailsClosedOnPathResolutionErrors(t *testing.T) {
	for _, kind := range []string{"loop", "not-directory"} {
		t.Run(kind, func(t *testing.T) {
			home := workspaceTemp(t)
			at := filepath.Join(home, "workspace")
			if kind == "loop" {
				if err := os.Symlink(at, at); err != nil {
					t.Skip("symlinks:", err)
				}
			} else if err := os.WriteFile(at, []byte("ordinary file"), 0o600); err != nil {
				t.Fatal(err)
			}
			n := &Node{home: home, dir: filepath.Join(home, ".persona")}
			got := namingGuardPreview(t, n, namingGuardSource())
			if got.Plan != nil || got.Status != "invalid" || len(got.Diagnostics) != 1 || got.Diagnostics[0].Code != "base_scope_denied" {
				t.Fatal("unresolved path accepted", got)
			}
		})
	}
}

func TestWorkspaceNamingAndPrepareCheckTheSamePaths(t *testing.T) {
	for _, level := range []string{"missing tail", "base", "task", "repository"} {
		t.Run(level, func(t *testing.T) {
			home := workspaceTemp(t)
			n := &Node{home: home, dir: filepath.Join(home, ".persona")}
			plan := workspacePlan(t, home, repositorywire.LayoutContainer, workspaceRemote(t, "trunk"))
			linkAt, field, repo := "", "", ""
			switch level {
			case "base":
				linkAt, field = plan.BasePath, "base_directory"
			case "task":
				linkAt, field = plan.RootPath, "task_directory_template"
			case "repository":
				linkAt, field, repo = plan.Repositories[0].AbsolutePath, "directory_alias", "web"
			}
			if linkAt != "" {
				if err := os.MkdirAll(filepath.Dir(linkAt), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(workspaceTemp(t), linkAt); err != nil {
					t.Skip("symlinks:", err)
				}
			}
			before := namingGuardTree(t, home)
			got := namingGuardPreview(t, n, plan.Source)
			if !reflect.DeepEqual(before, namingGuardTree(t, home)) {
				t.Fatal("preview created resources")
			}
			req := taskworkspace.Request{WorkspaceID: "ws_parity", OperationID: "op_parity", Plan: plan}
			prepared := workspaceCall(t, n, "prepare", req)
			if linkAt == "" {
				if got.Plan == nil || got.Plan.Digest != plan.Digest || prepared.State != "ready" {
					t.Fatal(got, prepared)
				}
				return
			}
			if got.Plan != nil || got.Status != "invalid" || len(got.Diagnostics) != 1 ||
				got.Diagnostics[0].Code != "path_symlink" || got.Diagnostics[0].Field != field || got.Diagnostics[0].RepoID != repo ||
				prepared.Error == nil || prepared.Error.Code != got.Diagnostics[0].Code || prepared.Error.RepoID != repo {
				t.Fatal(got, prepared)
			}
			if !reflect.DeepEqual(before, namingGuardTree(t, home)) {
				t.Fatal("refused preparation created resources")
			}
		})
	}
}

func TestWorkspaceNamingAndPrepareAllowSymlinkedHomeAncestor(t *testing.T) {
	for _, homeLinkKind := range []string{"home-itself", "home-parent"} {
		for _, baseKind := range []string{"default", "tilde", "literal-home"} {
			t.Run(homeLinkKind+"/"+baseKind, func(t *testing.T) {
				root := workspaceTemp(t)
				realHome := filepath.Join(root, "data", "home", "me")
				if err := os.MkdirAll(realHome, 0o700); err != nil {
					t.Fatal(err)
				}
				var linkHome string
				if homeLinkKind == "home-itself" {
					linkParent := filepath.Join(root, "home")
					if err := os.MkdirAll(linkParent, 0o700); err != nil {
						t.Fatal(err)
					}
					linkHome = filepath.Join(linkParent, "me")
					if err := os.Symlink(realHome, linkHome); err != nil {
						t.Skip("symlinks:", err)
					}
				} else {
					linkParent := filepath.Join(root, "home")
					if err := os.Symlink(filepath.Dir(realHome), linkParent); err != nil {
						t.Skip("symlinks:", err)
					}
					linkHome = filepath.Join(linkParent, "me")
				}

				n := &Node{home: linkHome, dir: filepath.Join(linkHome, ".persona")}
				repoReal := filepath.Join(realHome, "code", "app")
				if err := os.MkdirAll(repoReal, 0o700); err != nil {
					t.Fatal(err)
				}
				workspaceGit(t, repoReal, "init", "-b", "trunk")
				workspaceGit(t, repoReal, "config", "user.name", "Example")
				workspaceGit(t, repoReal, "config", "user.email", "me@example.com")
				if err := os.WriteFile(filepath.Join(repoReal, "file.txt"), []byte("base\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				workspaceGit(t, repoReal, "add", ".")
				workspaceGit(t, repoReal, "commit", "-m", "Initial")

				baseRule := "{home}/workspace"
				wantBase := filepath.Join(realHome, "workspace")
				switch baseKind {
				case "tilde":
					baseRule = "~/.herdr/worktrees"
					wantBase = filepath.Join(realHome, ".herdr", "worktrees")
				case "literal-home":
					baseRule = filepath.Join(linkHome, ".herdr", "worktrees")
					wantBase = filepath.Join(realHome, ".herdr", "worktrees")
				}

				// Local Git checkout reached through the symlinked home resolves and prepares cleanly.
				localSrc := localWorkspaceSource(t, n, filepath.Join(linkHome, "code", "app"), "")
				if localSrc.Error != "" || localSrc.Repository == nil {
					t.Fatalf("local source lookup failed: %+v", localSrc)
				}
				source := namingGuardSource()
				source.Manager = "example"
				source.MachineID = "node_example"
				source.Layout = repositorywire.LayoutRoot
				source.Rules.BaseDirectory = baseRule
				var err error
				source.Repositories, err = repositorywire.LocalSnapshot(source.Manager, source.TodoID, *localSrc.Repository)
				if err != nil {
					t.Fatal(err)
				}
				before := namingGuardTree(t, realHome)
				preview := namingGuardPreview(t, n, source)
				if preview.Plan == nil || preview.Status != "complete" || preview.Plan.BasePath != wantBase {
					t.Fatalf("naming preview rejected symlinked home: %+v", preview)
				}
				if !reflect.DeepEqual(before, namingGuardTree(t, realHome)) {
					t.Fatal("preview created resources")
				}

				req := taskworkspace.Request{WorkspaceID: "ws_home_link", OperationID: "op_home_link", UseID: "launch_home_link", Plan: *preview.Plan}
				ready := workspaceCall(t, n, "prepare", req)
				if ready.State != "ready" || ready.Error != nil || len(ready.Repositories) != 1 {
					t.Fatalf("prepare under symlinked home failed: %+v", ready)
				}
				if got := workspaceCall(t, n, "inspect", req); got.State != "ready" || got.Error != nil {
					t.Fatalf("inspect under symlinked home failed: %+v", got)
				}
				if err := n.guardBackendDeletion(ready.Plan.RootPath); err == nil {
					t.Fatal("deletion guard missed resolved path")
				}
				if err := n.guardBackendDeletion(filepath.Join(linkHome, ".herdr", "worktrees", "task-todo_example")); baseKind != "default" && err == nil {
					t.Fatal("deletion guard missed symlinked-home path")
				}
				if got := workspaceCall(t, n, "cleanup", req); got.State != "removed" || got.Error != nil {
					t.Fatalf("cleanup under symlinked home failed: %+v", got)
				}

				// Empty-repository workspace under symlinked home also prepares and cleans up cleanly.
				emptySource := namingGuardSource()
				emptySource.Manager = "example"
				emptySource.MachineID = "node_example"
				emptySource.TodoID = "todo_empty_home_link"
				emptySource.Layout = repositorywire.LayoutContainer
				emptySource.Rules.BaseDirectory = baseRule
				emptySource.Repositories = repositorywire.PreparationSnapshot{
					SchemaVersion: 1, Manager: emptySource.Manager, TodoID: emptySource.TodoID, Repositories: []repositorywire.PreparationRepository{},
				}
				emptyPreview := namingGuardPreview(t, n, emptySource)
				if emptyPreview.Plan == nil || emptyPreview.Status != "complete" || emptyPreview.Plan.BasePath != wantBase {
					t.Fatalf("empty workspace preview under symlinked home failed: %+v", emptyPreview)
				}
				emptyReq := taskworkspace.Request{WorkspaceID: "ws_empty_home_link", OperationID: "op_empty_home_link", Plan: *emptyPreview.Plan}
				if got := workspaceCall(t, n, "prepare", emptyReq); got.State != "ready" || got.Error != nil {
					t.Fatalf("empty workspace prepare under symlinked home failed: %+v", got)
				}
				if got := workspaceCall(t, n, "cleanup", emptyReq); got.State != "removed" || got.Error != nil {
					t.Fatalf("empty workspace cleanup under symlinked home failed: %+v", got)
				}

				// Local clone URL under symlinked home and managed mirror under symlinked state dir also succeed.
				mirrorInfo := repoMirrorCall[repositorywire.MirrorInfo](t, n, repositorywire.MethodRepoMirrorEnsure, repositorywire.MirrorEnsureRequest{
					RepoID:     "repo_web",
					Identity:   testRepoIdentity(t, "web"),
					CloneURL:   filepath.Join(linkHome, "code", "app"),
					TTLSeconds: 300,
				})
				if !mirrorInfo.Exists || !mirrorInfo.Fetched {
					t.Fatalf("repo mirror ensure under symlinked home failed: %+v", mirrorInfo)
				}
			})
		}
	}
}

func TestWorkspaceNamingSymlinkedHomeRejectsInnerAndPrivateSymlinks(t *testing.T) {
	cases := []struct {
		name      string
		level     string
		target    string
		wantCode  string
		wantField string
		wantRepo  string
	}{
		{name: "base external symlink", level: "base", target: "external", wantCode: "path_symlink", wantField: "base_directory"},
		{name: "task external symlink", level: "task", target: "external", wantCode: "path_symlink", wantField: "task_directory_template"},
		{name: "repo external symlink", level: "repository", target: "external", wantCode: "path_symlink", wantField: "directory_alias", wantRepo: "web"},
		{name: "task symlink to ssh via home link", level: "task", target: "ssh-link", wantCode: "base_scope_denied", wantField: "base_directory"},
		{name: "task symlink to ssh via real home", level: "task", target: "ssh-real", wantCode: "base_scope_denied", wantField: "base_directory"},
		{name: "task symlink to persona state via home link", level: "task", target: "state-link", wantCode: "base_scope_denied", wantField: "base_directory"},
		{name: "task symlink to persona state via real home", level: "task", target: "state-real", wantCode: "base_scope_denied", wantField: "base_directory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := workspaceTemp(t)
			realHome := filepath.Join(root, "data", "home", "me")
			external := filepath.Join(root, "external")
			if err := os.MkdirAll(filepath.Join(realHome, ".ssh"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(realHome, ".persona"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(external, 0o700); err != nil {
				t.Fatal(err)
			}
			linkHome := filepath.Join(root, "home", "me")
			if err := os.MkdirAll(filepath.Dir(linkHome), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(realHome, linkHome); err != nil {
				t.Skip("symlinks:", err)
			}

			n := &Node{home: linkHome, dir: filepath.Join(linkHome, ".persona")}
			source := namingGuardSource()
			source.Manager = "example"
			source.MachineID = "node_example"
			source.Repositories.SchemaVersion = 1
			source.Repositories.Manager = source.Manager
			source.Repositories.TodoID = source.TodoID
			source.Rules.BaseDirectory = "~/.herdr/worktrees"
			cleanPreview := namingGuardPreview(t, n, source)
			if cleanPreview.Plan == nil {
				t.Fatalf("clean baseline under symlinked home failed: %+v", cleanPreview)
			}

			var target string
			switch tc.target {
			case "external":
				target = external
			case "ssh-link":
				target = filepath.Join(linkHome, ".ssh")
			case "ssh-real":
				target = filepath.Join(realHome, ".ssh")
			case "state-link":
				target = filepath.Join(linkHome, ".persona")
			case "state-real":
				target = filepath.Join(realHome, ".persona")
			}

			var linkAt string
			switch tc.level {
			case "base":
				linkAt = cleanPreview.Plan.BasePath
			case "task":
				linkAt = cleanPreview.Plan.RootPath
			case "repository":
				for _, repo := range cleanPreview.Plan.Repositories {
					if repo.RepoID == "web" {
						linkAt = repo.AbsolutePath
					}
				}
			}
			if err := os.MkdirAll(filepath.Dir(linkAt), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, linkAt); err != nil {
				t.Fatal(err)
			}

			got := namingGuardPreview(t, n, source)
			if got.Plan != nil || got.Status != "invalid" || len(got.Diagnostics) != 1 ||
				got.Diagnostics[0].Code != tc.wantCode || got.Diagnostics[0].Field != tc.wantField || got.Diagnostics[0].RepoID != tc.wantRepo {
				t.Fatalf("preview accepted or misclassified inner/private symlink: %+v", got)
			}

			req := taskworkspace.Request{WorkspaceID: "ws_inner_link", OperationID: "op_inner_link", Plan: *cleanPreview.Plan}
			prepared := workspaceCall(t, n, "prepare", req)
			if prepared.Error == nil || prepared.Error.Code != tc.wantCode || prepared.Error.RepoID != tc.wantRepo {
				t.Fatalf("prepare accepted or misclassified inner/private symlink: %+v", prepared)
			}
		})
	}

}

func TestWorkspacePhysicalStateDirectory(t *testing.T) {
	home, state := workspaceTemp(t), workspaceTemp(t)
	alias := filepath.Join(home, "node-state")
	if err := os.Symlink(state, alias); err != nil {
		t.Skip("symlinks:", err)
	}
	t.Setenv("PERSONA_HOME", alias)
	n := &Node{home: home, dir: alias}
	source := workspacePlan(t, home, repositorywire.LayoutRoot, workspaceRemote(t, "trunk")).Source
	preview := namingGuardPreview(t, n, source)
	if preview.Plan == nil {
		t.Fatal(preview)
	}
	req := taskworkspace.Request{WorkspaceID: "ws_state", OperationID: "op_state", Plan: *preview.Plan, MirrorRepos: []string{"web"}}
	for _, stage := range []string{"prepare", "inspect", "cleanup"} {
		got := workspaceCall(t, n, stage, req)
		if got.Error != nil || stage == "cleanup" && got.State != "removed" || stage != "cleanup" && got.State != "ready" {
			t.Fatalf("%s: %+v", stage, got)
		}
	}
	if _, err := os.Stat(filepath.Join(state, "repos", "web.git")); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceControlDirectoryDiagnostics(t *testing.T) {
	for _, control := range []string{"task-workspaces", "repos"} {
		for _, target := range []string{"ordinary", ".ssh", ".persona", "configured-state"} {
			t.Run(control+"/"+target, func(t *testing.T) {
				home, state := workspaceTemp(t), workspaceTemp(t)
				n := &Node{home: home, dir: filepath.Join(home, "node-state")}
				if err := os.Symlink(state, n.dir); err != nil {
					t.Skip("symlinks:", err)
				}
				t.Setenv("PERSONA_HOME", n.dir)
				dst := filepath.Join(home, target)
				if target == "configured-state" {
					dst = filepath.Join(state, "nested")
				}
				if err := os.Mkdir(dst, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(dst, filepath.Join(state, control)); err != nil {
					t.Fatal(err)
				}
				source := workspacePlan(t, home, repositorywire.LayoutRoot, workspaceRemote(t, "trunk")).Source
				preview := namingGuardPreview(t, n, source)
				if preview.Plan == nil {
					t.Fatal(preview)
				}
				req := taskworkspace.Request{WorkspaceID: "ws_control", OperationID: "op_control", Plan: *preview.Plan, MirrorRepos: []string{"web"}}
				got := workspaceCall(t, n, "prepare", req)
				want := "base_scope_denied"
				if target == "ordinary" {
					want = "path_symlink"
				}
				if got.Error == nil || got.Error.Code != want {
					t.Fatalf("prepare error = %v, want %s", got.Error, want)
				}
				if entries, err := os.ReadDir(dst); err != nil || len(entries) != 0 {
					t.Fatalf("link target changed: %v, %v", entries, err)
				}
			})
		}
	}
}

func namingGuardSource() workspacenaming.Source {
	return workspacenaming.Source{TodoID: "todo_example", Title: "Example", PlannedAt: time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC), Timezone: "UTC", Rules: workspacenaming.Defaults(), Layout: repositorywire.LayoutContainer,
		Repositories: repositorywire.PreparationSnapshot{Repositories: []repositorywire.PreparationRepository{
			{RepoID: "api", DirectoryAlias: "api", RequestedRef: repositorywire.RequestedRef{Kind: "default"}},
			{RepoID: "web", DirectoryAlias: "web", RequestedRef: repositorywire.RequestedRef{Kind: "default"}},
		}}}
}

func namingGuardPreview(t *testing.T, n *Node, s workspacenaming.Source) workspacenaming.Preview {
	t.Helper()
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	out, err := n.handle(t.Context(), "workspace.naming.resolve", raw)
	if err != nil {
		t.Fatal(err)
	}
	return out.(workspacenaming.Preview)
}

func namingGuardTree(t *testing.T, roots ...string) map[string]os.FileMode {
	t.Helper()
	entries := map[string]os.FileMode{}
	for _, root := range roots {
		if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err == nil {
				entries[path] = info.Mode()
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return entries
}
