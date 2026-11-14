package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// paseoWorkspacesHello is Paseo keeping workspaces of its own (caps.workspaces, runtimeworkspaces.go).
func paseoWorkspacesHello() map[string]any {
	h := paseoHello()
	caps := h["caps"].(map[string]any)
	caps["workspaces"] = true
	return h
}

// runtimeRepo is a node whose only engine is Paseo, its runtime keeping workspaces, on a real repository: branch main
// with one commit, a second local branch "spare". The fake runtime makes worktrees as Paseo does: git worktree add in
// its own folder (trees), a new branch from base, an existing one as it is.
type runtimeRepo struct {
	n             *Node
	rt            *fakeRuntime
	repo, trees   string
	creates       atomic.Int32
	mainWorkspace map[string]any
}

func runtimeRepoNode(t *testing.T) *runtimeRepo {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Sample Dev")
	t.Setenv("GIT_AUTHOR_EMAIL", "dev@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Sample Dev")
	t.Setenv("GIT_COMMITTER_EMAIL", "dev@example.com")
	n, _ := runtimeNode(t)
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	r := &runtimeRepo{n: n}
	r.repo = resolved(t, mkdir(t, filepath.Join(t.TempDir(), "proj")))
	r.trees = filepath.Join(resolved(t, t.TempDir()), "worktrees", "abcd1234")
	runPullGit(t, r.repo, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(r.repo, "app.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runPullGit(t, r.repo, "add", "app.txt")
	runPullGit(t, r.repo, "commit", "-m", "first")
	runPullGit(t, r.repo, "branch", "spare")
	r.rt = dialRuntime(t, n, paseoWorkspacesHello())
	r.rt.answer = func(method string, params json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		switch method {
		case "workspace.prepare":
			return map[string]any{"checkouts": r.trees}, nil
		case "workspace.create":
			r.creates.Add(1)
			branch, name := str(p["branch"]), str(p["name"])
			if branch == "" {
				branch = fmt.Sprintf("generated-%d", r.creates.Load())
			}
			if name == "" {
				name = branch
			}
			dest := filepath.Join(r.trees, name)
			args := []string{"worktree", "add", "--", dest, branch}
			if p["create_branch"] == true {
				args = []string{"worktree", "add", "-b", branch, "--", dest, str(p["base"])}
			}
			if err := os.MkdirAll(r.trees, 0o755); err != nil {
				return nil, err
			}
			if _, err := git(context.Background(), str(p["cwd"]), args...); err != nil {
				return nil, err
			}
			return map[string]any{"workspace": map[string]any{"workspace_id": fmt.Sprintf("wks_new%d", r.creates.Load()), "title": branch,
				"cwd": dest, "kind": "worktree", "branch": branch, "space_id": "prj_1", "space_name": "proj", "repo_root": r.repo, "owned": true}}, nil
		}
		return map[string]any{"ok": true}, nil
	}
	r.mainWorkspace = map[string]any{"workspace_id": "wks_main", "title": "main", "cwd": r.repo, "kind": "checkout", "branch": "main",
		"space_id": "prj_1", "space_name": "proj", "repo_root": r.repo}
	_ = r.rt.peer.Notify("runtime.workspaces", map[string]any{"workspaces": []any{r.mainWorkspace}})
	waitFor(t, "the runtime's workspaces", func() bool { return n.isRuntimeWorkspace("wks_main") })
	return r
}

// catalog is the node's workspaces.list, by source id.
func catalog(t *testing.T, n *Node) map[string]proto.Workspace {
	t.Helper()
	raw, err := nodeCall(t, n, "workspaces.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var res proto.WorkspacesResponse
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	out := map[string]proto.Workspace{}
	for _, w := range res.Workspaces {
		out[w.SourceID] = w
	}
	return out
}

// An engine runtime that keeps workspaces is the machine's catalog: each workspace it lists, sessions in it or not, as
// the apps read a host's (source, project, main checkout); what it may not list is left out.
func TestRuntimeWorkspacesAreTheCatalog(t *testing.T) {
	n, _ := runtimeNode(t)
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	dir := resolved(t, t.TempDir())
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	rt := dialRuntime(t, n, paseoWorkspacesHello())
	_ = rt.peer.Notify("runtime.workspaces", map[string]any{"workspaces": []any{
		map[string]any{"workspace_id": "wks_main", "title": "Main", "cwd": link, "kind": "checkout", "branch": "main", "space_id": "prj_1", "space_name": "app", "repo_root": link},
		map[string]any{"workspace_id": "wks_tree", "cwd": "/w/trees/feat", "kind": "worktree", "branch": "feat", "space_id": "prj_1", "space_name": "app", "repo_root": link, "owned": true},
		map[string]any{"workspace_id": "wks_notes", "title": "Notes", "cwd": "/w/notes", "kind": "directory", "space_id": "prj_2", "space_name": "notes"},
		map[string]any{"workspace_id": "bad id", "cwd": "/w/x"},
		map[string]any{"workspace_id": "wks_rel", "cwd": "relative/dir"},
		map[string]any{"workspace_id": "wks_main", "cwd": "/w/again"},
	}})
	waitFor(t, "the workspaces", func() bool { return len(catalog(t, n)) == 3 })
	got := catalog(t, n)
	main, tree, notes := got["wks_main"], got["wks_tree"], got["wks_notes"]
	if main.Source != "paseo" || main.Title != "Main" || main.Cwd != dir || main.ProjectPath != dir || main.ProjectID != "prj_1" ||
		main.ProjectName != "app" || main.IsMain == nil || !*main.IsMain || main.WorkspaceKind != "git" || !main.Available || main.Branch != "main" {
		t.Fatalf("the main checkout: %+v", main)
	}
	if tree.Title != "feat" || tree.IsMain == nil || *tree.IsMain || tree.WorkspaceKind != "git" || tree.Cwd != "/w/trees/feat" {
		t.Fatalf("the worktree, named by its branch: %+v", tree)
	}
	// Only a worktree the runtime made says so (owned, #6297): the one Delete worktree is offered for.
	if !tree.Owned || main.Owned || notes.Owned {
		t.Fatalf("owned: worktree %v, main checkout %v, folder %v", tree.Owned, main.Owned, notes.Owned)
	}
	if notes.IsMain != nil || notes.WorkspaceKind != "folder" || notes.ProjectID != "prj_2" {
		t.Fatalf("a folder: %+v", notes)
	}
	if !n.isRuntimeWorkspace("wks_tree") || n.isRuntimeWorkspace("w1") {
		t.Fatal("the runtime's workspaces are told from a terminal host's")
	}

	// A runtime that does not say it keeps workspaces lists none: its pushes are not taken.
	dsh := dialRuntime(t, n, dshHello())
	_ = dsh.peer.Notify("runtime.workspaces", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "dsh-1", "cwd": "/w/dsh"}}})
	_ = dsh.peer.Notify("runtime.sessions", map[string]any{"sessions": []any{map[string]any{"session_id": "session-x1", "status": "idle"}}})
	waitFor(t, "the dsh list (after its workspaces)", func() bool { _, ok := listed(t, n)["session-x1"]; return ok })
	if _, ok := catalog(t, n)["dsh-1"]; ok || n.isRuntimeWorkspace("dsh-1") {
		t.Fatal("a runtime without caps.workspaces listed its workspaces")
	}
}

// A machine whose only engine is a runtime has a catalog even with the runtime away (none, as a terminal host that is not
// running has none); a runtime that keeps no workspaces gives it none to have.
func TestRuntimeEngineCatalogWhenItCannotList(t *testing.T) {
	n, _ := runtimeNode(t)
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	if got := catalog(t, n); len(got) != 0 {
		t.Fatalf("Paseo not running: %v", got)
	}
	rt := dialRuntime(t, n, paseoHello())
	_ = rt.peer.Notify("runtime.workspaces", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "wks_1", "cwd": "/w/app"}}})
	waitFor(t, "connected", func() bool { h, _, _ := n.runtimeEngine(""); return h != nil })
	// Either no catalog at all (no terminal host either) or one without the runtime's workspaces.
	raw, err := nodeCall(t, n, "workspaces.list", map[string]any{})
	var res proto.WorkspacesResponse
	_ = json.Unmarshal(raw, &res)
	if err == nil && len(res.Workspaces) != 0 || err != nil && !strings.Contains(err.Error(), "no workspace host") {
		t.Fatalf("a runtime without caps.workspaces: %s %v", raw, err)
	}
}

// An engine runtime's session names the workspace it is in, which ties it to the catalog's row; another runtime's, or
// one that keeps no workspaces, does not. A move to another workspace changes the list.
func TestRuntimeSessionsKeepTheirWorkspace(t *testing.T) {
	n, _ := runtimeNode(t)
	enablePaseo(n)
	pa := dialRuntime(t, n, paseoWorkspacesHello())
	_ = pa.peer.Notify("runtime.sessions", map[string]any{"sessions": []any{
		map[string]any{"session_id": "paseo-w1", "status": "idle", "workspace_id": "wks_1", "space_id": "prj_1"},
		map[string]any{"session_id": "paseo-w2", "status": "idle", "workspace_id": "../bad"},
	}})
	dsh := dialRuntime(t, n, dshHello())
	_ = dsh.peer.Notify("runtime.sessions", map[string]any{"sessions": []any{map[string]any{"session_id": "session-d1", "status": "idle", "workspace_id": "w9"}}})
	waitFor(t, "listed", func() bool {
		l := listed(t, n)
		return l["paseo-w1"].SessionID != "" && l["session-d1"].SessionID != ""
	})
	l := listed(t, n)
	if l["paseo-w1"].WorkspaceID != "wks_1" || l["paseo-w2"].WorkspaceID != "" || l["session-d1"].WorkspaceID != "" {
		t.Fatalf("workspace ids: %q %q %q", l["paseo-w1"].WorkspaceID, l["paseo-w2"].WorkspaceID, l["session-d1"].WorkspaceID)
	}
	row := proto.Session{SessionID: "paseo-w1", SourceKind: proto.SourceRuntime, WorkspaceID: "wks_1"}
	moved := row
	moved.WorkspaceID = "wks_2"
	if listKey([]proto.Session{row}) == listKey([]proto.Session{moved}) {
		t.Fatal("a session moving to another workspace is no change of the list")
	}
}

// A runtime engine that cannot make worktrees says so: one that keeps no workspaces (Cindy, an older bridge) with the
// code the hub reads as "run the task in its folder" (#4946); one away, as away. A missing terminal host is neither.
func TestWorktreePrepareOnARuntimeEngineIsUnsupported(t *testing.T) {
	n, _ := runtimeNode(t)
	if err := n.useEngine(context.Background(), proto.EnginePaseo); err != nil {
		t.Fatal(err)
	}
	_, err := nodeCall(t, n, "worktree.prepare", map[string]any{"cwd": "/w/app", "locale": "en"})
	if err == nil || rpcCode(err) == proto.CodeWorktreeUnsupported || !strings.Contains(err.Error(), "Paseo is not connected") {
		t.Fatalf("prepare with Paseo away = %v (code %q)", err, rpcCode(err))
	}
	dialRuntime(t, n, paseoHello()) // no caps.workspaces
	waitFor(t, "connected", func() bool { h, _, _ := n.runtimeEngine(""); return h != nil })
	_, err = nodeCall(t, n, "worktree.prepare", map[string]any{"cwd": "/w/app"})
	if rpcCode(err) != proto.CodeWorktreeUnsupported || !errors.Is(err, host.ErrUnsupported) || err.Error() != "unsupported: paseo cannot do this" {
		t.Fatalf("prepare on a runtime keeping no workspaces = %v (code %q)", err, rpcCode(err))
	}
	if _, err := nodeCall(t, n, "worktree.prepare", map[string]any{"cwd": "/w/app", "kind": "orca"}); err == nil || rpcCode(err) != "" {
		t.Fatalf("a missing host is not a runtime without checkouts: %v (code %q)", err, rpcCode(err))
	}
}

// "New worktree…" on a runtime engine: the dialog starts from the runtime's folder; the worktree is the runtime's, made
// as Persona promises one (a new branch from the source checkout's HEAD, an existing one as it is), listed at once.
func TestRuntimeWorktreePrepareAndCreate(t *testing.T) {
	r := runtimeRepoNode(t)
	prep, err := request(t, r.n, "worktree.prepare", map[string]any{"workspace_id": "wks_main", "kind": "paseo"})
	if err != nil || prep["path"] != r.trees || prep["root"] != r.repo || prep["branch"] != "main" || prep["repoName"] != "proj" ||
		prep["automatic_name"] != true || prep["choose_parent"] == true {
		t.Fatalf("prepare: %v %v", prep, err)
	}
	var asked map[string]any
	_ = json.Unmarshal(r.rt.got("workspace.prepare")[0], &asked)
	if asked["cwd"] != r.repo || asked["repo_root"] != r.repo {
		t.Fatalf("the runtime was asked about %v", asked)
	}
	if prep, err := request(t, r.n, "worktree.prepare", map[string]any{"cwd": filepath.Join(r.repo, ".")}); err != nil || prep["path"] != r.trees {
		t.Fatalf("prepare by folder on the default engine: %v %v", prep, err)
	}

	res, err := request(t, r.n, "worktree.create", map[string]any{"workspace_id": "wks_main", "kind": "paseo", "branch": "feat/x",
		"path": filepath.Join(r.trees, "feat-x")})
	if err != nil || res["workspace_id"] != "wks_new1" || res["path"] != filepath.Join(r.trees, "feat-x") || res["branch"] != "feat/x" {
		t.Fatalf("create: %v %v", res, err)
	}
	var made map[string]any
	_ = json.Unmarshal(r.rt.got("workspace.create")[0], &made)
	if made["cwd"] != r.repo || made["workspace_id"] != "wks_main" || made["branch"] != "feat/x" || made["create_branch"] != true ||
		made["base"] != "main" || made["name"] != "feat-x" || !strings.HasPrefix(str(made["key"]), "persona-") {
		t.Fatalf("workspace.create %v", made)
	}
	if head := runPullGit(t, filepath.Join(r.trees, "feat-x"), "rev-parse", "HEAD"); head != runPullGit(t, r.repo, "rev-parse", "HEAD") {
		t.Fatal("the new branch does not start at the source HEAD")
	}
	if w, ok := catalog(t, r.n)["wks_new1"]; !ok || w.Branch != "feat/x" || w.IsMain == nil || *w.IsMain {
		t.Fatalf("the new worktree is listed at once: %+v", w)
	}

	// An existing branch is checked out as it is.
	if _, err := request(t, r.n, "worktree.create", map[string]any{"workspace_id": "wks_main", "kind": "paseo", "branch": "spare",
		"path": filepath.Join(r.trees, "spare")}); err != nil {
		t.Fatal(err)
	}
	made = map[string]any{}
	_ = json.Unmarshal(r.rt.got("workspace.create")[1], &made)
	if made["create_branch"] != false || made["base"] != nil {
		t.Fatalf("an existing branch: %v", made)
	}

	// The folder is named as Paseo names it: the name asked for, as its slug, so the checks are on the folder it uses.
	res, err = request(t, r.n, "worktree.create", map[string]any{"workspace_id": "wks_main", "kind": "paseo", "branch": "feat/y",
		"path": filepath.Join(r.trees, "Feat Y")})
	if err != nil || res["path"] != filepath.Join(r.trees, "feat-y") {
		t.Fatalf("a name Paseo slugs: %v %v", res, err)
	}
	made = map[string]any{}
	_ = json.Unmarshal(r.rt.got("workspace.create")[2], &made)
	if made["name"] != "feat-y" {
		t.Fatalf("the name sent: %v", made)
	}

	// Refusals, none of which reaches the runtime.
	mkdir(t, filepath.Join(r.trees, "empty")) // Paseo would not use it, but put the worktree beside it (empty-1)
	calls := len(r.rt.got("workspace.create"))
	for name, p := range map[string]map[string]any{
		"checked out elsewhere": {"workspace_id": "wks_main", "branch": "main", "path": filepath.Join(r.trees, "main"), "locale": "en"},
		"another folder":        {"workspace_id": "wks_main", "branch": "other", "path": filepath.Join(r.repo, "..", "other"), "locale": "en"},
		"a folder in use":       {"workspace_id": "wks_main", "branch": "other", "path": filepath.Join(r.trees, "feat-x")},
		"its slug in use":       {"workspace_id": "wks_main", "branch": "other", "path": filepath.Join(r.trees, "FEAT_X"), "locale": "en"},
		"an empty folder":       {"workspace_id": "wks_main", "branch": "other", "path": filepath.Join(r.trees, "empty"), "locale": "en"},
		"no slug at all":        {"workspace_id": "wks_main", "branch": "other", "path": filepath.Join(r.trees, "新功能"), "locale": "en"},
		"another parent":        {"workspace_id": "wks_main", "branch": "other", "path": filepath.Join(r.trees, "other"), "parent_workspace_id": "wks_new1", "locale": "en"},
		"an unknown workspace":  {"workspace_id": "wks_gone", "branch": "other", "path": filepath.Join(r.trees, "other"), "locale": "en"},
		"a bad branch":          {"workspace_id": "wks_main", "branch": "-x", "path": filepath.Join(r.trees, "x")},
		"no source":             {"workspace_id": "", "cwd": "", "branch": "other", "path": filepath.Join(r.trees, "other"), "locale": "en"},
	} {
		_, err := request(t, r.n, "worktree.create", p)
		if err == nil {
			t.Errorf("%s: created", name)
			continue
		}
		want := map[string]string{"checked out elsewhere": "already checked out", "another folder": "Paseo keeps its worktrees in " + r.trees,
			"its slug in use": filepath.Join(r.trees, "feat-x") + " already exists", "an empty folder": filepath.Join(r.trees, "empty") + " already exists",
			"no slug at all": "Paseo can’t name a worktree folder “新功能”", "another parent": "another parent cannot be chosen",
			"an unknown workspace": "Paseo no longer lists this workspace", "no source": "Choose the workspace or folder"}[name]
		if want != "" && !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(r.rt.got("workspace.create")) != calls {
		t.Fatal("a refused worktree reached the runtime")
	}

	// Automatic naming: the runtime names the branch and the folder; the branch still starts at the source HEAD.
	res, err = request(t, r.n, "worktree.create", map[string]any{"workspace_id": "wks_main", "kind": "paseo", "auto_name": true})
	if err != nil || res["workspace_id"] == "" || res["branch"] == "" {
		t.Fatalf("automatic: %v %v", res, err)
	}
	made = map[string]any{}
	_ = json.Unmarshal(r.rt.got("workspace.create")[calls], &made)
	if made["branch"] != nil || made["name"] != nil || made["create_branch"] != true || made["base"] != "main" {
		t.Fatalf("automatic workspace.create %v", made)
	}
}

// A new agent's own worktree (#6115) on a runtime engine is the runtime's, named as on a terminal host; worktree.status
// says where the runtime's go, and calls it Persona's to remove, which worktree.remove does through the runtime
// (TestRuntimeAgentWorktreeRemove).
func TestRuntimeAgentWorktree(t *testing.T) {
	r := runtimeRepoNode(t)
	res, err := request(t, r.n, "worktree.add", map[string]any{"cwd": r.repo, "name_hint": "Fix the login"})
	want := filepath.Join(r.trees, "persona-fix-the-login")
	if err != nil || res["path"] != want || res["branch"] != "persona/fix-the-login" || res["root"] != r.repo || res["workspace_id"] != "wks_new1" {
		t.Fatalf("add: %v %v", res, err)
	}
	var made map[string]any
	_ = json.Unmarshal(r.rt.got("workspace.create")[0], &made)
	if made["create_branch"] != true || made["base"] != "main" || made["name"] != "persona-fix-the-login" || made["workspace_id"] != nil {
		t.Fatalf("workspace.create %v", made)
	}
	// Its name taken: the next one, as on a terminal host.
	if res, err := request(t, r.n, "worktree.add", map[string]any{"cwd": r.repo, "name_hint": "Fix the login"}); err != nil || res["branch"] != "persona/fix-the-login-2" {
		t.Fatalf("again: %v %v", res, err)
	}
	st, err := request(t, r.n, "worktree.status", map[string]any{"cwd": want})
	if err != nil || st["worktree"] != true || st["ours"] != true || st["checkouts"] != r.trees || st["branch"] != "persona/fix-the-login" {
		t.Fatalf("status: %v %v", st, err)
	}
	// The repository's main checkout is no worktree of Paseo's to remove.
	if st, err := request(t, r.n, "worktree.status", map[string]any{"cwd": r.repo}); err != nil || st["ours"] == true {
		t.Fatalf("status of the main checkout: %v %v", st, err)
	}

	// A folder already there, even an empty one, takes its name: Paseo would put the worktree beside it.
	mkdir(t, filepath.Join(r.trees, "persona-fix-the-login-3"))
	if res, err := request(t, r.n, "worktree.add", map[string]any{"cwd": r.repo, "name_hint": "Fix the login"}); err != nil ||
		res["branch"] != "persona/fix-the-login-4" || res["path"] != filepath.Join(r.trees, "persona-fix-the-login-4") {
		t.Fatalf("past an empty folder: %v %v", res, err)
	}
	// A name as long as Paseo's folders take: its -10 shortens the name, not the number (Paseo's own cut would drop the
	// number and land on the name's folder).
	long := "persona/" + strings.Repeat("a", 40)
	for i := 2; i <= 9; i++ {
		runPullGit(t, r.repo, "branch", fmt.Sprintf("%s-%d", long, i))
	}
	mkdir(t, filepath.Join(r.trees, "persona-"+strings.Repeat("a", 40)))
	res, err = request(t, r.n, "worktree.add", map[string]any{"cwd": r.repo, "name_hint": strings.Repeat("a", 40)})
	folder := "persona-" + strings.Repeat("a", 39) + "-10"
	if err != nil || res["branch"] != long+"-10" || res["path"] != filepath.Join(r.trees, folder) {
		t.Fatalf("a long name's -10: %v %v", res, err)
	}
	creates := r.rt.got("workspace.create")
	made = map[string]any{}
	_ = json.Unmarshal(creates[len(creates)-1], &made)
	if made["name"] != folder || runtimeFolderName(folder) != folder {
		t.Fatalf("the folder sent for a long name: %v", made)
	}
}

// The folder a runtime engine makes for a name is Paseo's slug of it (its slugify, @getpaseo/protocol branch-slug.js:
// these are its answers).
func TestRuntimeFolderName(t *testing.T) {
	for in, want := range map[string]string{
		"feat-x": "feat-x", "Feature X": "feature-x", "--a__b--": "a-b", "新功能": "", "fix/登录": "fix", "İstanbul": "i-stanbul",
		strings.Repeat("a", 30) + "-" + strings.Repeat("b", 29): strings.Repeat("a", 30),
		strings.Repeat("a", 10) + "-" + strings.Repeat("b", 60): strings.Repeat("a", 10) + "-" + strings.Repeat("b", 39),
		strings.Repeat("a", 49) + "-bbb":                        strings.Repeat("a", 49),
		strings.Repeat("a", 60):                                 strings.Repeat("a", 50),
		"persona-" + strings.Repeat("a", 40) + "-10":            "persona-" + strings.Repeat("a", 40),
	} {
		if got := runtimeFolderName(in); got != want {
			t.Errorf("runtimeFolderName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A runtime that is connected but not one of the machine's engines (Paseo's bridge running while the machine enables
// herdr only) makes, lists and deletes no workspaces: its workspaces are not the catalog's, and no worktree asked for
// with its kind reaches it.
func TestRuntimeWorkspacesOnlyAsAnEngine(t *testing.T) {
	r := runtimeRepoNode(t)
	r.n.eng.mu.Lock()
	r.n.eng.set = &proto.EngineChoice{Hosts: []string{proto.EngineHerdr}, Default: proto.EngineHerdr}
	r.n.eng.mu.Unlock()
	if _, _, ok := r.n.runtimeEngine(proto.EnginePaseo); ok {
		t.Fatal("Paseo is an engine of a machine that does not enable it")
	}
	if raw, err := nodeCall(t, r.n, "workspaces.list", map[string]any{}); err == nil && strings.Contains(string(raw), "wks_main") {
		t.Fatalf("the catalog lists a runtime that is not an engine: %s", raw)
	}
	for _, c := range []struct {
		method string
		params map[string]any
	}{
		{"worktree.prepare", map[string]any{"workspace_id": "wks_main", "kind": "paseo"}},
		{"worktree.create", map[string]any{"workspace_id": "wks_main", "kind": "paseo", "branch": "x", "path": filepath.Join(r.trees, "x")}},
		{"worktree.create", map[string]any{"workspace_id": "wks_main", "kind": "paseo", "auto_name": true}},
		{"worktree.add", map[string]any{"cwd": r.repo, "kind": "paseo", "name_hint": "x"}},
		{"worktree.status", map[string]any{"cwd": r.repo, "kind": "paseo"}},
	} {
		_, _ = request(t, r.n, c.method, c.params)
	}
	if p, c := r.rt.got("workspace.prepare"), r.rt.got("workspace.create"); len(p)+len(c) != 0 {
		t.Fatalf("a runtime that is not an engine was asked: prepare %d, create %d", len(p), len(c))
	}
	enablePaseo(r.n)
	if _, err := request(t, r.n, "worktree.prepare", map[string]any{"workspace_id": "wks_main", "kind": "paseo"}); err != nil ||
		len(r.rt.got("workspace.prepare")) != 1 {
		t.Fatalf("enabled again: %v", err)
	}
}

// One deadline for the whole of a create: what asking the runtime for its folder took is not given again to making the
// worktree, and running out of it says so.
func TestRuntimeWorktreeCreateHasOneDeadline(t *testing.T) {
	r := runtimeRepoNode(t)
	old := runtimeWorktreeDeadline
	runtimeWorktreeDeadline = 600 * time.Millisecond
	t.Cleanup(func() { runtimeWorktreeDeadline = old })
	// The create is held until the test ends: only the deadline ends the call. Should the node not give up, the runtime
	// answers after 3 s, so that the test fails rather than hangs.
	held := make(chan struct{})
	t.Cleanup(func() { close(held) })
	r.rt.answer = func(method string, _ json.RawMessage) (any, error) {
		switch method {
		case "workspace.prepare":
			time.Sleep(400 * time.Millisecond) // sleep-ok: a slow runtime's prepare spends part of the create's one deadline
			return map[string]any{"checkouts": r.trees}, nil
		case "workspace.create":
			select {
			case <-held:
			case <-time.After(3 * time.Second):
			}
		}
		return map[string]any{"ok": true}, nil
	}
	start := time.Now()
	_, err := request(t, r.n, "worktree.create", map[string]any{"workspace_id": "wks_main", "kind": "paseo", "branch": "slow",
		"path": filepath.Join(r.trees, "slow"), "locale": "en"})
	if err == nil || !strings.Contains(err.Error(), "Paseo didn’t finish making the worktree in time") {
		t.Fatalf("a create past its deadline: %v", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("took %s: making the worktree had a deadline of its own", took)
	}
}
