package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

func sessionsByID(list []proto.Session) map[string]proto.Session {
	out := make(map[string]proto.Session, len(list))
	for _, s := range list {
		out[s.SessionID] = s
	}
	return out
}

// #13533, as seen on a Mac: Claude Code Agents run in the herdr workspace rooted in a folder, and the Claude desktop app
// works in the same folder. Its sessions were in the desktop apps' project space (codex-project:<folder>), a second
// project with the folder's name; they are in the herdr workspace's now, as Claude desktop app sessions still. A space
// action taken by one of its desktop sessions' space_kind reaches the workspace.
func TestFolderSpaceDesktopSessionsJoinTheHerdrWorkspace(t *testing.T) {
	const (
		terminal = "11111111-2222-4333-8444-555555555555" // Claude Code in a herdr pane
		desktop  = "abcdabcd-0000-4000-8000-000000000001" // the Claude desktop app's, nothing runs it now
	)
	n, herdr, home := herdrNode(t)
	forgetOfflineScan()
	t.Cleanup(forgetOfflineScan)
	app := mkdir(t, filepath.Join(physicalTempDir(t), "code", "app"))
	dir := mkdir(t, filepath.Join(home, ".claude", "projects", "-code-app"))
	writeTranscriptIn(t, dir, desktop, app, "claude-desktop")
	_, third := useDesktopDirs(t)
	origin, _ := json.Marshal(app)
	writeDesktopEntry(t, third, desktop, desktop, false, nil, `,"originCwd":`+string(origin))
	herdr.reply("session.snapshot", snapshot(
		[]any{map[string]any{"workspace_id": "wH", "label": "app", "number": 3, "cwd": app}},
		[]any{map[string]any{"tab_id": "wH:t1", "workspace_id": "wH", "label": "agent-1", "number": 1}},
		[]any{claudeAgent(terminal, proto.StatusWorking, "wH:p1", "wH:t1", "wH", app, "fix it")},
		nil))

	rows := sessionsByID(listSessions(t, n))
	for id, kind := range map[string]string{terminal: host.Herdr, desktop: proto.SourceClaudeDesktop} {
		s, ok := rows[id]
		if !ok {
			t.Fatalf("%s is not listed", id)
		}
		if s.SpaceID != "wH" || s.SpaceName != "app" || s.SpaceIndex != 3 || s.SpaceKind != kind {
			t.Errorf("%s: space %q %q #%d (%s), want wH app #3 (%s)", id, s.SpaceID, s.SpaceName, s.SpaceIndex, s.SpaceKind, kind)
		}
	}
	// iOS names a space's host by the kind of the first of its sessions in tab order, which can be the desktop app's.
	if got, err := n.gitDirOf(context.Background(), "", "wH", proto.SourceClaudeDesktop); err != nil || got != app {
		t.Errorf("git in the space named by a desktop session's kind: %q, %v; want %q", got, err, app)
	}
}

// #13533, as seen on Linux: an agy Agent runs in a herdr pane in the workspace rooted in a folder, and an agy
// conversation runs on the agy runtime in the same folder. The runtime names its space by the folder's path, which made
// a second project; the conversation is in the herdr workspace's now. The home folder is reached through a link to
// another volume: herdr names the workspace's folder through the link, the runtime by its physical path.
func TestFolderSpaceAgyRuntimeConversationJoinsTheHerdrWorkspace(t *testing.T) {
	n, herdr, home := herdrNode(t)
	volume := mkdir(t, filepath.Join(physicalTempDir(t), "volume"))
	app := mkdir(t, filepath.Join(volume, "code", "app"))
	link := filepath.Join(home, "linked")
	if err := os.Symlink(volume, link); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(link, "code", "app")
	conversation := "agy-" + agyOtherTestID
	n.runtimes = newRuntimeRegistry()
	rt := n.runtimes.add(nil)
	rt.ready, rt.hello.Runtime = true, agyAgent
	n.runtimes.setSessions(rt, normalizeRuntimeSessions(rt, []proto.Session{
		{SessionID: conversation, Agent: agyAgent, Cwd: app, Status: proto.StatusIdle, Title: "on the runtime", SpaceID: app, SpaceName: "app"},
	}))
	herdr.reply("session.snapshot", snapshot(
		[]any{map[string]any{"workspace_id": "w1", "label": "app", "number": 1, "cwd": linked}},
		[]any{map[string]any{"tab_id": "w1:t1", "workspace_id": "w1", "label": "agy", "number": 1}},
		[]any{agyTestAgent(agyTestPane, linked, "")},
		nil))

	rows := sessionsByID(listSessions(t, n))
	pane, ok := rows[agyTestRow]
	if !ok || pane.SpaceID != "w1" || pane.SpaceKind != host.Herdr {
		t.Fatalf("the agy pane: %+v (listed %v)", pane, ok)
	}
	s, ok := rows[conversation]
	if !ok {
		t.Fatalf("%s is not listed", conversation)
	}
	if s.SpaceID != "w1" || s.SpaceName != "app" || s.SpaceIndex != 1 || s.SpaceKind != agyAgent || s.SourceKind != proto.SourceRuntime {
		t.Errorf("the runtime's conversation: space %q %q #%d (%s, %s), want w1 app #1 (agy, runtime)",
			s.SpaceID, s.SpaceName, s.SpaceIndex, s.SpaceKind, s.SourceKind)
	}
}

// The rules of placeInFolders, on rows as the list has them: which space stands for a folder, which workspace a folder's
// sessions join, and the folder's own space when none.
func TestPlaceInFolders(t *testing.T) {
	// /home/me is a link to /vol/me; /gone was removed.
	dirs := map[string]string{
		"/code/app": "/code/app", "/code/site": "/code/site", "/code/docs": "/code/docs", "/code/both": "/code/both",
		"/code/tasks": "/code/tasks", "/code/kit": "/code/kit", "/code/kit/child": "/code/kit/child",
		"/home/me/notes": "/vol/me/notes", "/vol/me/notes": "/vol/me/notes",
	}
	physical := func(p string) string { return dirs[p] }
	space := func(kind, id, name string) proto.Session {
		return proto.Session{SpaceKind: kind, SpaceID: id, SpaceName: name}
	}
	runtimeRow := func(runtime, dir string) proto.Session {
		return proto.Session{SourceKind: proto.SourceRuntime, SourceInstanceID: runtime, SpaceKind: runtime, SpaceID: dir, SpaceName: filepath.Base(dir)}
	}
	codexApp := space(codexAppKind, projectSpace+"/code/site", "Website")
	codexApp.SpaceIndex = 2
	assistant := runtimeRow(agyAgent, "/code/app")
	assistant.Assistant = true
	workspaces := []folderWorkspace{
		{host.Herdr, host.Space{ID: "w1", Label: "app", Number: 1, Cwd: "/code/app"}},
		// /code/docs: herdr-core's terminals outside its app's spaces and botmux's topics go after a host's workspace
		{host.HerdrCore, host.Space{ID: dirSpace + "/code/docs", Label: "docs", Number: 1, Cwd: "/code/docs"}},
		{host.Herdr, host.Space{ID: "w4", Label: "Fix the build", Number: 4, Cwd: "/code/docs"}},
		// /code/both: of two named after the folder, the default engine's
		{host.HerdrCore, host.Space{ID: "sp1", Label: "both", Number: 1, Cwd: "/code/both"}},
		{host.Herdr, host.Space{ID: "w7", Label: "both", Number: 7, Cwd: "/code/both"}},
		{host.Herdr, host.Space{ID: "w2", Label: "Release", Number: 2, Cwd: "/code/both"}},
		// /code/tasks: two task workspaces, neither the folder's
		{host.Herdr, host.Space{ID: "w5", Label: "Task one", Number: 5, Cwd: "/code/tasks"}},
		{host.Herdr, host.Space{ID: "w6", Label: "Task two", Number: 6, Cwd: "/code/tasks"}},
		// /code/kit/child: an Orca worktree nested under its repository's
		{host.Orca, host.Space{ID: "kit::/code/kit/child", Label: "child", Number: 2, Cwd: "/code/kit/child", ParentID: "kit::/code/kit"}},
		// a workspace whose folder is gone joins nothing
		{host.Herdr, host.Space{ID: "w9", Label: "gone", Number: 9, Cwd: "/gone"}},
	}
	list := []proto.Session{
		// 0: a host's own: as it is; 1, 2: a desktop app's and a runtime's in its folder join it
		space(host.Herdr, "w1", "app"),
		space(proto.SourceClaudeDesktop, projectSpace+"/code/app", "app"),
		runtimeRow(agyAgent, "/code/app"),
		// 3 to 5: no workspace there: the folder's own space, named and numbered as the Codex App has its project
		codexApp,
		space(proto.SourceClaudeDesktop, projectSpace+"/code/site", "site"),
		runtimeRow("dsh", "/code/site"),
		// 6, 7: the same folder through a link and by its physical path: its own space, by the physical path
		runtimeRow(agyAgent, "/home/me/notes"),
		space(proto.SourceClaudeDesktop, projectSpace+"/vol/me/notes", "notes"),
		// 8: a host's workspace before the directory group; 9: of several, the one named after the folder
		runtimeRow(agyAgent, "/code/docs"),
		space(proto.SourceClaudeDesktop, projectSpace+"/code/both", "both"),
		// 10: several, none named after the folder: which is the folder's is not known, so its own space
		runtimeRow(agyAgent, "/code/tasks"),
		// 11: an Orca worktree, under its parent
		runtimeRow(agyAgent, "/code/kit/child"),
		// 12 to 16, as they are: an engine's project, the assistant's (in no project), a folder this computer does not
		// have, no folder, the Codex App's chats in no project
		runtimeRow(proto.EngineT3Code, "/code/app"),
		assistant,
		runtimeRow(agyAgent, "/gone"),
		runtimeRow(agyAgent, "relative"),
		space(codexAppKind, "codex-chats:/home/me/.codex", "Codex chats"),
	}
	want := []struct {
		id, name string
		index    int32
		parent   string
	}{
		{"w1", "app", 0, ""},
		{"w1", "app", 1, ""},
		{"w1", "app", 1, ""},
		{projectSpace + "/code/site", "Website", 2, ""},
		{projectSpace + "/code/site", "Website", 2, ""},
		{projectSpace + "/code/site", "Website", 2, ""},
		{projectSpace + "/vol/me/notes", "notes", 0, ""},
		{projectSpace + "/vol/me/notes", "notes", 0, ""},
		{"w4", "Fix the build", 4, ""},
		{"w7", "both", 7, ""},
		{projectSpace + "/code/tasks", "tasks", 0, ""},
		{"kit::/code/kit/child", "child", 2, "kit::/code/kit"},
		{"/code/app", "app", 0, ""},
		{"/code/app", "app", 0, ""},
		{"/gone", "gone", 0, ""},
		{"relative", "relative", 0, ""},
		{"codex-chats:/home/me/.codex", "Codex chats", 0, ""},
	}
	kinds := make([]string, len(list))
	for i := range list {
		kinds[i] = list[i].SpaceKind
	}
	placeInFolders(list, workspaces, host.Herdr, physical)
	for i, w := range want {
		s := list[i]
		if s.SpaceID != w.id || s.SpaceName != w.name || s.SpaceIndex != w.index || s.ParentSpaceID != w.parent || s.SpaceKind != kinds[i] {
			t.Errorf("row %d: %q %q #%d under %q (%s), want %q %q #%d under %q (%s)", i, s.SpaceID, s.SpaceName, s.SpaceIndex,
				s.ParentSpaceID, s.SpaceKind, w.id, w.name, w.index, w.parent, kinds[i])
		}
	}

	// A list with no session whose space stands for a folder is not looked at: no workspace, no path.
	n, _ := testNode(t)
	quiet := []proto.Session{space(host.Herdr, "w1", "app"), {SessionID: "none"}}
	n.joinFolderSpaces(quiet)
	if quiet[0].SpaceID != "w1" || quiet[1].SpaceID != "" {
		t.Errorf("a list with no folder space changed: %+v", quiet)
	}
}

// A space action names its host by a session's space_kind; one that is no host's or engine's goes to the workspace's.
func TestSpaceHostKind(t *testing.T) {
	for kind, want := range map[string]string{
		"": "", host.Herdr: host.Herdr, host.HerdrCore: host.HerdrCore, host.Orca: host.Orca, host.Tern: host.Tern,
		proto.EnginePaseo: proto.EnginePaseo, proto.EngineT3Code: proto.EngineT3Code,
		proto.SourceClaudeDesktop: "", codexAppKind: "", agyAgent: "", "dsh": "",
	} {
		if got := spaceHostKind(kind); got != want {
			t.Errorf("spaceHostKind(%q) = %q, want %q", kind, got, want)
		}
	}
}
