package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// manageAnswers has the fake runtime rename and archive its workspaces as Paseo does: a title, a project's name, and an
// archive that removes a worktree it made from the disk (forced, as Paseo's is), its branch kept. archiveErr, when
// set, is its refusal instead.
func (r *runtimeRepo) manageAnswers(t *testing.T, archiveErr error) {
	t.Helper()
	r.rt.mu.Lock()
	defer r.rt.mu.Unlock()
	prev := r.rt.answer
	r.rt.answer = func(method string, params json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		switch method {
		case "workspace.rename":
			if id := str(p["workspace_id"]); id != "" {
				return map[string]any{"workspace": map[string]any{"workspace_id": id, "title": p["title"], "cwd": r.repo, "kind": "checkout",
					"branch": "main", "space_id": "prj_1", "space_name": "proj", "repo_root": r.repo}}, nil
			}
			return map[string]any{"space_name": "space " + str(p["title"])}, nil
		case "workspace.archive":
			if archiveErr != nil {
				return nil, archiveErr
			}
			for _, w := range r.n.runtimeWorkspaces() {
				if w.ID == str(p["workspace_id"]) && w.Owned {
					if _, err := git(context.Background(), r.repo, "worktree", "remove", "--force", w.Cwd); err != nil {
						return nil, err
					}
				}
			}
			return map[string]any{"archived_at": "2026-11-12T00:00:00.000Z"}, nil
		}
		return prev(method, params)
	}
}

// tellSessions has the fake runtime say its sessions (runtime.sessions) and waits until the node has them as said.
func (r *runtimeRepo) tellSessions(t *testing.T, sessions ...map[string]any) {
	t.Helper()
	list := make([]any, len(sessions))
	for i, s := range sessions {
		list[i] = s
	}
	_ = r.rt.peer.Notify("runtime.sessions", map[string]any{"sessions": list})
	h, _, _ := r.n.runtimeEngine(proto.EnginePaseo)
	waitFor(t, "the sessions", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		if len(h.sessions) != len(sessions) {
			return false
		}
		for i, s := range sessions {
			if h.sessions[i].SessionID != s["session_id"] || h.sessions[i].Status != s["status"] || h.sessions[i].Starting != (s["starting"] == true) {
				return false
			}
		}
		return true
	})
}

// runtimeWorkspaces is every workspace of the machine's engine runtimes (tests).
func (n *Node) runtimeWorkspaces() []runtimeWorkspace {
	runtimes, _ := n.catalogRuntimes()
	var out []runtimeWorkspace
	for _, h := range runtimes {
		out = append(out, h.workspacesCopy()...)
	}
	return out
}

// A Paseo workspace is renamed in Paseo: worktree.rename sets its title, space.rename the name of the space it is in
// (its project, by the project's id or by one of its workspaces', as Android names it), which the node answers; an
// id no engine has never reaches the runtime.
func TestRuntimeWorkspaceRename(t *testing.T) {
	r := runtimeRepoNode(t)
	r.manageAnswers(t, nil)
	if _, err := request(t, r.n, "worktree.rename", map[string]any{"workspace_id": "wks_main", "kind": "paseo", "name": "  Main  work "}); err != nil {
		t.Fatal(err)
	}
	var asked map[string]any
	_ = json.Unmarshal(r.rt.got("workspace.rename")[0], &asked)
	if asked["workspace_id"] != "wks_main" || asked["title"] != "Main work" || asked["space_id"] != nil {
		t.Fatalf("workspace.rename %v", asked)
	}
	if w := catalog(t, r.n)["wks_main"]; w.Title != "Main work" {
		t.Fatalf("the catalog says the new title at once: %+v", w)
	}
	for _, id := range []string{"prj_1", "wks_main"} {
		res, err := request(t, r.n, "space.rename", map[string]any{"workspace_id": id, "name": "App"})
		if err != nil || res["name"] != "space App" {
			t.Fatalf("space.rename %s: %v %v", id, res, err)
		}
		asked = map[string]any{}
		_ = json.Unmarshal(r.rt.got("workspace.rename")[len(r.rt.got("workspace.rename"))-1], &asked)
		if asked["space_id"] != "prj_1" || asked["title"] != "App" || asked["workspace_id"] != nil {
			t.Fatalf("space.rename %s asked %v", id, asked)
		}
	}
	calls := len(r.rt.got("workspace.rename"))
	if _, err := request(t, r.n, "worktree.rename", map[string]any{"workspace_id": "wks_gone", "kind": "paseo", "name": "x", "locale": "en"}); err == nil ||
		!strings.Contains(err.Error(), "Paseo no longer lists this workspace") {
		t.Fatalf("renamed a workspace Paseo does not list: %v", err)
	}
	// coded, so the hub words it in the reader's language (rpc-errors.space_gone)
	if _, err := request(t, r.n, "space.rename", map[string]any{"workspace_id": "w1", "name": "x"}); err == nil || rpcCode(err) != "space_gone" {
		t.Fatalf("renamed a space no engine lists: %v (code %q)", err, rpcCode(err))
	}
	if len(r.rt.got("workspace.rename")) != calls {
		t.Fatal("an unknown id reached the runtime")
	}
}

// With herdr enabled beside Paseo as the default engine, a herdr space is renamed in herdr: the one host that renames,
// though it is not the default engine's, is asked once it lists the space (it used to look for a "paseo" host and fail).
func TestRenameSpaceInHerdrBesidePaseo(t *testing.T) {
	n, _ := testNode(t)
	hh := &renameHost{bareHost: bareHost{kind: host.Herdr}, spaces: []string{"w1"}}
	n.hosts = host.NewRegistry(hh)
	c := proto.EngineChoice{Hosts: []string{"herdr", "paseo"}, Default: "paseo"}
	n.eng.name, n.eng.set = proto.LegacyEngine(c), &c
	res, err := request(t, n, "space.rename", map[string]any{"workspace_id": "w1", "name": "work"})
	if err != nil || res["name"] != "work" || !slices.Equal(hh.renamed, []string{"w1=work"}) {
		t.Fatalf("renamed %v %v, herdr %q", res, err, hh.renamed)
	}
	if _, err := request(t, n, "space.rename", map[string]any{"workspace_id": "w9", "name": "work"}); err == nil ||
		strings.Contains(err.Error(), "no paseo host") || len(hh.renamed) != 1 {
		t.Fatalf("a space herdr does not list: %v, herdr %q", err, hh.renamed)
	}
}

// Deleting a worktree Paseo made goes as a terminal host's does: a preview of what goes (its changes, its branch), a
// confirmation bound to it, then Paseo archives the workspace, which removes the folder; the branch stays unless the
// user lets it go. A main checkout, a folder, a worktree Paseo did not make, and closing are refused.
func TestRuntimeWorktreeDelete(t *testing.T) {
	r := runtimeRepoNode(t)
	r.manageAnswers(t, nil)
	tree := filepath.Join(r.trees, "feat-x")
	if _, err := request(t, r.n, "worktree.create", map[string]any{"workspace_id": "wks_main", "kind": "paseo", "branch": "feat/x", "path": tree}); err != nil {
		t.Fatal(err)
	}
	other := resolved(t, t.TempDir())
	_ = r.rt.peer.Notify("runtime.workspaces", map[string]any{"workspaces": []any{r.mainWorkspace,
		map[string]any{"workspace_id": "wks_new1", "title": "feat/x", "cwd": tree, "kind": "worktree", "branch": "feat/x", "space_id": "prj_1", "repo_root": r.repo, "owned": true},
		map[string]any{"workspace_id": "wks_notes", "cwd": other, "kind": "folder", "space_id": "prj_2"},
		map[string]any{"workspace_id": "wks_theirs", "cwd": other, "kind": "worktree", "branch": "x", "space_id": "prj_1"},
	}})
	waitFor(t, "the list", func() bool { return r.n.isRuntimeWorkspace("wks_theirs") })
	del := func(p map[string]any) (map[string]any, error) {
		p["kind"], p["locale"] = "paseo", "en"
		return request(t, r.n, "worktree.delete", p)
	}
	for id, want := range map[string]string{"wks_main": "main checkout", "wks_notes": "is a folder, not a worktree",
		"wks_theirs": "Paseo did not make this worktree", "wks_gone": "differ from the preview"} {
		if _, err := del(map[string]any{"workspace_id": id, "preview": true}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("preview of %s: %v", id, err)
		}
	}
	if _, err := del(map[string]any{"workspace_id": "wks_new1", "preview": true, "close_only": true}); err == nil {
		t.Error("closing a Paseo workspace from Persona")
	}

	// Its changes: shown, then only with force; a change since the preview needs another.
	if err := os.WriteFile(filepath.Join(tree, "new.txt"), []byte("draft\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prev, err := del(map[string]any{"workspace_id": "wks_new1", "preview": true})
	if err != nil || prev["path"] != tree || prev["branch"] != "feat/x" || prev["dirty"] != float64(1) || prev["folder"] != false ||
		prev["host_id"] != "local" || prev["kind"] != "paseo" || str(prev["preview_token"]) == "" {
		t.Fatalf("preview: %v %v", prev, err)
	}
	token := prev["preview_token"]
	if _, err := del(map[string]any{"workspace_id": "wks_new1", "preview_token": token}); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("changes without force: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tree, "new.txt"), []byte("draft 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := del(map[string]any{"workspace_id": "wks_new1", "preview_token": token, "force": true}); err == nil || !strings.Contains(err.Error(), "differ from the preview") {
		t.Fatalf("changed since the preview: %v", err)
	}
	// A session started in it since the preview is one more the archive would end.
	prev, _ = del(map[string]any{"workspace_id": "wks_new1", "preview": true})
	_ = r.rt.peer.Notify("runtime.sessions", map[string]any{"sessions": []any{map[string]any{"session_id": "paseo-in-tree", "status": "idle", "workspace_id": "wks_new1"}}})
	waitFor(t, "the session", func() bool { _, ok := listed(t, r.n)["paseo-in-tree"]; return ok })
	if _, err := del(map[string]any{"workspace_id": "wks_new1", "preview_token": prev["preview_token"], "force": true}); err == nil {
		t.Fatal("deleted a worktree with a session the preview did not show")
	}
	if len(r.rt.got("workspace.archive")) != 0 {
		t.Fatal("a refused deletion reached the runtime")
	}

	prev, _ = del(map[string]any{"workspace_id": "wks_new1", "preview": true})
	// Renamed in Paseo meanwhile (a new title, its project another name): still the worktree the preview showed.
	_ = r.rt.peer.Notify("runtime.workspaces", map[string]any{"workspaces": []any{r.mainWorkspace,
		map[string]any{"workspace_id": "wks_new1", "title": "Renamed", "cwd": tree, "kind": "worktree", "branch": "feat/x", "space_id": "prj_1",
			"space_name": "app 2", "repo_root": r.repo, "owned": true},
		map[string]any{"workspace_id": "wks_notes", "cwd": other, "kind": "folder", "space_id": "prj_2"},
		map[string]any{"workspace_id": "wks_theirs", "cwd": other, "kind": "worktree", "branch": "x", "space_id": "prj_1"},
	}})
	waitFor(t, "the new title", func() bool { return catalog(t, r.n)["wks_new1"].Title == "Renamed" })
	res, err := del(map[string]any{"workspace_id": "wks_new1", "preview_token": prev["preview_token"], "force": true})
	if err != nil || res["removed"] != true || res["state"] != "removed" {
		t.Fatalf("delete: %v %v", res, err)
	}
	var asked map[string]any
	_ = json.Unmarshal(r.rt.got("workspace.archive")[0], &asked)
	if asked["workspace_id"] != "wks_new1" || len(asked) != 1 {
		t.Fatalf("workspace.archive %v", asked)
	}
	if _, err := os.Lstat(tree); !os.IsNotExist(err) {
		t.Fatal("the folder is still there")
	}
	if runPullGit(t, r.repo, "branch", "--list", "feat/x") == "" {
		t.Fatal("the branch went too")
	}
	if _, ok := catalog(t, r.n)["wks_new1"]; ok {
		t.Fatal("still in the catalog")
	}
	if res, err := del(map[string]any{"workspace_id": "wks_new1", "preview_token": prev["preview_token"], "reconcile": true}); err != nil || res["state"] != "removed" {
		t.Fatalf("reconcile: %v %v", res, err)
	}
	if _, err := del(map[string]any{"workspace_id": "wks_new1", "preview_token": prev["preview_token"], "force": true}); err == nil {
		t.Fatal("a token was used twice")
	}

	// The branch too, when the user lets it go (git's safe delete: it is merged).
	tree2 := filepath.Join(r.trees, "feat-y")
	made, err := request(t, r.n, "worktree.create", map[string]any{"workspace_id": "wks_main", "kind": "paseo", "branch": "feat/y", "path": tree2})
	if err != nil {
		t.Fatal(err)
	}
	prev, _ = del(map[string]any{"workspace_id": made["workspace_id"], "preview": true})
	res, err = del(map[string]any{"workspace_id": made["workspace_id"], "preview_token": prev["preview_token"], "delete_branch": true})
	if err != nil || res["removed"] != true || res["branch_deleted"] != true || runPullGit(t, r.repo, "branch", "--list", "feat/y") != "" {
		t.Fatalf("delete with its branch: %v %v", res, err)
	}
}

// A worktree an Agent may still be writing in is not deleted: one of its workspace's sessions at work (working,
// waiting for the user, starting), or an Agent of another engine (herdr's) running in its folder; at the preview and
// again at the confirmation. Idle and done sessions are what the archive ends, as the preview showed.
func TestRuntimeWorktreeDeleteWhileAtWork(t *testing.T) {
	r := runtimeRepoNode(t)
	r.manageAnswers(t, nil)
	tree := filepath.Join(r.trees, "feat-w")
	if _, err := request(t, r.n, "worktree.create", map[string]any{"workspace_id": "wks_main", "kind": "paseo", "branch": "feat/w", "path": tree}); err != nil {
		t.Fatal(err)
	}
	session := func(status string, starting bool) {
		t.Helper()
		r.tellSessions(t, map[string]any{"session_id": "paseo-w", "status": status, "starting": starting, "workspace_id": "wks_new1"})
	}
	del := func(p map[string]any) (map[string]any, error) {
		p["workspace_id"], p["kind"], p["locale"] = "wks_new1", "paseo", "en"
		return request(t, r.n, "worktree.delete", p)
	}
	const busy = "An Agent is still working in this worktree"
	for _, s := range []struct {
		status   string
		starting bool
	}{{"working", false}, {"blocked", false}, {"idle", true}} {
		session(s.status, s.starting)
		if _, err := del(map[string]any{"preview": true}); err == nil || !strings.Contains(err.Error(), busy) {
			t.Fatalf("preview with a session %s (starting %v): %v", s.status, s.starting, err)
		}
	}
	session("idle", false)
	prev, err := del(map[string]any{"preview": true})
	if err != nil {
		t.Fatal(err)
	}
	session("working", false) // back at work after the preview
	if _, err := del(map[string]any{"preview_token": prev["preview_token"]}); err == nil || !strings.Contains(err.Error(), busy) {
		t.Fatalf("confirmed while a session works: %v", err)
	}
	session("done", false)
	r.n.mu.Lock()
	r.n.agents = map[string]host.Agent{"herdr-1": {Agent: "claude", AgentStatus: proto.StatusIdle, Cwd: filepath.Join(tree, "web")}}
	r.n.mu.Unlock()
	if _, err := del(map[string]any{"preview_token": prev["preview_token"]}); err == nil || !strings.Contains(err.Error(), busy) {
		t.Fatalf("confirmed with a herdr Agent in the folder: %v", err)
	}
	if _, err := del(map[string]any{"preview": true}); err == nil || !strings.Contains(err.Error(), busy) {
		t.Fatalf("preview with a herdr Agent in the folder: %v", err)
	}
	if len(r.rt.got("workspace.archive")) != 0 {
		t.Fatal("a worktree at work reached the archive")
	}
	r.n.mu.Lock()
	r.n.agents = nil
	r.n.mu.Unlock()
	if res, err := del(map[string]any{"preview_token": prev["preview_token"]}); err != nil || res["removed"] != true {
		t.Fatalf("delete once nothing works in it: %v %v", res, err)
	}
}

// The preview says why a worktree Paseo lists is not one to delete: its folder gone, git no longer knowing it (its
// record in the repository pruned), a folder inside a worktree; a main checkout is said apart from those.
func TestRuntimeWorktreeDeleteSaysWhyNot(t *testing.T) {
	r := runtimeRepoNode(t)
	r.manageAnswers(t, nil)
	pruned := filepath.Join(r.trees, "pruned")
	if err := os.MkdirAll(r.trees, 0o755); err != nil {
		t.Fatal(err)
	}
	runPullGit(t, r.repo, "worktree", "add", "-b", "pruned", pruned)
	if err := os.RemoveAll(filepath.Join(r.repo, ".git", "worktrees", "pruned")); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(r.trees, "feat-v")
	runPullGit(t, r.repo, "worktree", "add", "-b", "feat/v", tree)
	inside := mkdir(t, filepath.Join(tree, "web"))
	row := func(id, cwd string) map[string]any {
		return map[string]any{"workspace_id": id, "cwd": cwd, "kind": "worktree", "branch": "x", "space_id": "prj_1", "repo_root": r.repo, "owned": true}
	}
	_ = r.rt.peer.Notify("runtime.workspaces", map[string]any{"workspaces": []any{r.mainWorkspace,
		row("wks_gone", filepath.Join(r.trees, "missing")), row("wks_pruned", pruned), row("wks_inside", inside)}})
	waitFor(t, "the list", func() bool { return r.n.isRuntimeWorkspace("wks_inside") })
	for id, want := range map[string]string{
		"wks_gone":   "The worktree’s folder " + filepath.Join(r.trees, "missing") + " is gone",
		"wks_pruned": pruned + " is no longer a Git worktree",
		"wks_inside": inside + " is a folder inside a worktree",
		"wks_main":   "The repository's main checkout cannot be deleted",
	} {
		_, err := request(t, r.n, "worktree.delete", map[string]any{"workspace_id": id, "kind": "paseo", "preview": true, "locale": "en"})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("preview of %s: %v", id, err)
		}
	}
}

// A confirmed deletion has one deadline for the node's checks and Paseo's archive: one that runs out says the outcome
// is not known (reconcile tells), rather than the hub timing out while it goes on.
func TestRuntimeWorktreeDeleteHasOneDeadline(t *testing.T) {
	r := runtimeRepoNode(t)
	r.manageAnswers(t, nil)
	tree := filepath.Join(r.trees, "feat-s")
	if _, err := request(t, r.n, "worktree.create", map[string]any{"workspace_id": "wks_main", "kind": "paseo", "branch": "feat/s", "path": tree}); err != nil {
		t.Fatal(err)
	}
	prev, err := request(t, r.n, "worktree.delete", map[string]any{"workspace_id": "wks_new1", "kind": "paseo", "preview": true})
	if err != nil {
		t.Fatal(err)
	}
	old := runtimeWorktreeDeadline
	runtimeWorktreeDeadline = 500 * time.Millisecond
	t.Cleanup(func() { runtimeWorktreeDeadline = old })
	// Paseo's archive is held until the test ends: only the deadline ends the call. Should the node not give up, the
	// archive goes ahead after 3 s, so that the test fails rather than hangs.
	held := make(chan struct{})
	t.Cleanup(func() { close(held) })
	r.rt.mu.Lock()
	prevAnswer := r.rt.answer
	r.rt.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "workspace.archive" {
			select {
			case <-held:
				return nil, errors.New("the test ended")
			case <-time.After(3 * time.Second):
			}
		}
		return prevAnswer(method, params)
	}
	r.rt.mu.Unlock()
	start := time.Now()
	res, err := request(t, r.n, "worktree.delete", map[string]any{"workspace_id": "wks_new1", "kind": "paseo", "preview_token": prev["preview_token"]})
	if err != nil || res["state"] != "unknown" || res["removed"] == true {
		t.Fatalf("past the deadline: %v %v", res, err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("took %s", took)
	}
}

// Closing an Agent whose own worktree (#6115) Paseo made offers to remove it, as for herdr: worktree.remove has Paseo
// archive the workspace, which removes the folder, with Delete worktree's guards: never while a session in the
// workspace is running or an Agent of another engine runs in the folder, changes and an unmerged branch only with
// force; its branch goes after the folder when asked.
func TestRuntimeAgentWorktreeRemove(t *testing.T) {
	r := runtimeRepoNode(t)
	r.manageAnswers(t, nil)
	added, err := request(t, r.n, "worktree.add", map[string]any{"cwd": r.repo, "name_hint": "Fix it"})
	if err != nil {
		t.Fatal(err)
	}
	tree, id := str(added["path"]), str(added["workspace_id"])
	if st, err := request(t, r.n, "worktree.status", map[string]any{"cwd": tree, "kind": "paseo"}); err != nil || st["ours"] != true {
		t.Fatalf("status: %v %v", st, err)
	}
	// herdr's own folder does not hold it: not herdr's to remove
	if st, err := request(t, r.n, "worktree.status", map[string]any{"cwd": tree, "kind": "herdr"}); err != nil || st["ours"] == true {
		t.Fatalf("status as herdr's: %v %v", st, err)
	}
	remove := func(p map[string]any) (map[string]any, error) {
		p["path"], p["kind"] = tree, "paseo"
		return request(t, r.n, "worktree.remove", p)
	}
	// Another session still in the workspace: the archive would end it.
	r.tellSessions(t, map[string]any{"session_id": "paseo-other", "status": "idle", "workspace_id": id})
	if _, err := remove(map[string]any{}); rpcCode(err) != "worktree_in_use" {
		t.Fatalf("with another session in it: %v", err)
	}
	r.tellSessions(t, map[string]any{"session_id": "paseo-other", "status": "offline", "workspace_id": id})
	r.n.mu.Lock()
	r.n.agents = map[string]host.Agent{"herdr-1": {Agent: "claude", AgentStatus: proto.StatusIdle, Cwd: tree}}
	r.n.mu.Unlock()
	if _, err := remove(map[string]any{}); rpcCode(err) != "worktree_in_use" {
		t.Fatalf("with a herdr Agent in the folder: %v", err)
	}
	r.n.mu.Lock()
	r.n.agents = nil
	r.n.mu.Unlock()
	if err := os.WriteFile(filepath.Join(tree, "draft.txt"), []byte("draft\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := remove(map[string]any{}); rpcCode(err) != "worktree_dirty" {
		t.Fatalf("changes without force: %v", err)
	}
	if len(r.rt.got("workspace.archive")) != 0 {
		t.Fatal("a refused removal reached Paseo")
	}
	res, err := remove(map[string]any{"force": true, "delete_branch": true})
	if err != nil || res["removed"] != true || res["branch_deleted"] != true {
		t.Fatalf("remove: %v %v", res, err)
	}
	var asked map[string]any
	_ = json.Unmarshal(r.rt.got("workspace.archive")[0], &asked)
	if asked["workspace_id"] != id {
		t.Fatalf("workspace.archive %v", asked)
	}
	if _, err := os.Lstat(tree); !os.IsNotExist(err) {
		t.Fatal("the folder is still there")
	}
	if runPullGit(t, r.repo, "branch", "--list", str(added["branch"])) != "" {
		t.Fatal("the branch is still there")
	}
	if _, ok := catalog(t, r.n)[id]; ok {
		t.Fatal("still in the catalog")
	}
}

// A closed Agent's worktree Paseo does not remove is never called removed: Paseo's refusal in its own words, no answer
// in time said in the user's language, and an archive that kept the folder said as such.
func TestRuntimeAgentWorktreeRemoveFails(t *testing.T) {
	r := runtimeRepoNode(t)
	r.manageAnswers(t, nil)
	added, err := request(t, r.n, "worktree.add", map[string]any{"cwd": r.repo, "name_hint": "Fix it"})
	if err != nil {
		t.Fatal(err)
	}
	tree := str(added["path"])
	archive := func(answer func() (any, error)) {
		r.rt.mu.Lock()
		defer r.rt.mu.Unlock()
		prev := r.rt.answer
		r.rt.answer = func(method string, params json.RawMessage) (any, error) {
			if method == "workspace.archive" {
				return answer()
			}
			return prev(method, params)
		}
	}
	remove := func(what, want string) {
		t.Helper()
		res, err := request(t, r.n, "worktree.remove", map[string]any{"path": tree, "kind": "paseo", "locale": "en"})
		if rpcCode(err) != "worktree_failed" || !strings.Contains(err.Error(), want) || res["removed"] == true {
			t.Fatalf("%s: %v %v", what, res, err)
		}
		if _, err := os.Stat(tree); err != nil {
			t.Fatalf("%s: the folder is gone", what)
		}
	}
	archive(func() (any, error) {
		return nil, &rpc.Error{Code: "archive_failed", Message: "Paseo could not archive it"}
	})
	remove("refused", "Paseo could not archive it")

	old := runtimeRemoveDeadline
	t.Cleanup(func() { runtimeRemoveDeadline = old })
	runtimeRemoveDeadline = time.Second
	// no answer until the test ends (or after 4 s, so that a node that never gives up fails the test rather than hangs)
	held := make(chan struct{})
	t.Cleanup(func() { close(held) })
	archive(func() (any, error) {
		select {
		case <-held:
		case <-time.After(4 * time.Second):
		}
		return map[string]any{}, nil
	})
	start := time.Now()
	remove("no answer", "didn’t answer while removing the worktree")
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("no answer took %s", took)
	}
	runtimeRemoveDeadline = old

	archive(func() (any, error) { return map[string]any{"archived_at": "2026-11-12T00:00:00.000Z"}, nil }) // the folder left
	remove("folder kept", "archived the workspace but kept its folder")
}

// Paseo refusing the archive is an error, and the worktree is still there: present, not removed.
func TestRuntimeWorktreeDeleteRefused(t *testing.T) {
	r := runtimeRepoNode(t)
	r.manageAnswers(t, &rpc.Error{Code: "archive_failed", Message: "Paseo did not archive the workspace: busy"})
	tree := filepath.Join(r.trees, "feat-z")
	made, err := request(t, r.n, "worktree.create", map[string]any{"workspace_id": "wks_main", "kind": "paseo", "branch": "feat/z", "path": tree})
	if err != nil {
		t.Fatal(err)
	}
	prev, err := request(t, r.n, "worktree.delete", map[string]any{"workspace_id": made["workspace_id"], "kind": "paseo", "preview": true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = request(t, r.n, "worktree.delete", map[string]any{"workspace_id": made["workspace_id"], "kind": "paseo", "preview_token": prev["preview_token"]})
	var refused *rpc.Error
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("refused: %v", err)
	}
	res, err := request(t, r.n, "worktree.delete", map[string]any{"workspace_id": made["workspace_id"], "kind": "paseo", "preview_token": prev["preview_token"], "reconcile": true})
	if err != nil || res["state"] != "present" || res["removed"] == true {
		t.Fatalf("reconcile: %v %v", res, err)
	}
	if _, err := os.Stat(tree); err != nil {
		t.Fatal(err)
	}
}
