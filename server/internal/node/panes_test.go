package node

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// streamingHerdr: this herdr streams and can be driven, as the other terminal tests assume (the answer is cached for
// the whole test binary: a first ask against a fake with no binary would stay "no" for every test after it).
func streamingHerdr() {
	observeSupport.once.Do(func() { observeSupport.ok = true })
	controlSupport.once.Do(func() { controlSupport.ok = true })
}

func paneSnapshot(cwd string) map[string]any {
	shell := map[string]any{"pane_id": "w1:p2", "tab_id": "w1:t2", "workspace_id": "w1", "cwd": cwd, "terminal_title": "zsh", "terminal_title_stripped": "zsh"}
	return snapshot(
		[]any{map[string]any{"workspace_id": "w2", "label": "docs", "number": 2},
			map[string]any{"workspace_id": "w1", "label": "proj", "number": 1, "cwd": cwd, "branch": "main"}},
		[]any{map[string]any{"tab_id": "w1:t2", "workspace_id": "w1", "label": "shell", "number": 2},
			map[string]any{"tab_id": "w1:t1", "workspace_id": "w1", "label": "agent-1", "number": 1}},
		[]any{claudeAgent("11111111-2222-4333-8444-555555555555", proto.StatusWorking, "w1:p1", "w1:t1", "w1", cwd, "fix it")},
		[]any{claudeAgent("11111111-2222-4333-8444-555555555555", proto.StatusWorking, "w1:p1", "w1:t1", "w1", cwd, "fix it"), shell},
	)
}

// The browser's tree comes from the snapshot the node already takes: workspaces and tabs in herdr's order, every pane
// (the agent's and the plain shell), and the agent's session id on its pane only.
func TestPanesListHasShellsAndAgents(t *testing.T) {
	streamingHerdr()
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	f.reply("session.snapshot", paneSnapshot(cwd))
	listSessions(t, n) // the node's own look at herdr, which fills the agents
	f.reset()

	res, err := n.listPanes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls()) != 0 {
		t.Errorf("a layout this fresh asked herdr again: %v", f.trace())
	}
	if len(res.Hosts) != 1 {
		t.Fatalf("hosts = %+v", res.Hosts)
	}
	h := res.Hosts[0]
	if h.Kind != host.Herdr || !h.NewTab || !h.Stream || !h.Control {
		t.Errorf("host = %+v", h)
	}
	if len(h.Workspaces) != 2 || h.Workspaces[0].ID != "w1" || h.Workspaces[0].Branch != "main" || h.Workspaces[1].ID != "w2" {
		t.Errorf("workspaces = %+v", h.Workspaces)
	}
	if len(h.Tabs) != 2 || h.Tabs[0].ID != "w1:t1" || h.Tabs[1].Label != "shell" {
		t.Errorf("tabs = %+v", h.Tabs)
	}
	byPane := map[string]proto.PaneItem{}
	for _, p := range h.Panes {
		byPane[p.Pane] = p
	}
	agent, shell := byPane["w1:p1"], byPane["w1:p2"]
	if agent.Agent != "claude" || agent.Status != proto.StatusWorking || agent.SessionID != "11111111-2222-4333-8444-555555555555" ||
		agent.Terminal != "pane:w1:p1" {
		t.Errorf("agent pane = %+v", agent)
	}
	if shell.Agent != "" || shell.SessionID != "" || shell.Terminal != "pane:w1:p2" || shell.TabID != "w1:t2" || shell.Title != "zsh" {
		t.Errorf("shell pane = %+v", shell)
	}
}

// A first call (no layout yet) and a stale one take a snapshot; one that fails keeps showing what was seen.
func TestPanesListRefreshesAStaleLayout(t *testing.T) {
	streamingHerdr()
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	f.reply("session.snapshot", paneSnapshot(cwd))
	res, err := n.listPanes(context.Background())
	if err != nil || len(res.Hosts) != 1 || len(res.Hosts[0].Panes) != 2 {
		t.Fatalf("first list = %+v, %v", res, err)
	}
	if got := f.trace("session.snapshot"); len(got) != 1 {
		t.Errorf("snapshots = %v", got)
	}
	n.mu.Lock()
	l := n.layouts[host.Herdr]
	l.at = time.Now().Add(-time.Minute)
	n.layouts[host.Herdr] = l
	n.mu.Unlock()
	f.handle("session.snapshot", func(map[string]any) any { return herdrFail{Code: "boom", Message: "herdr is gone"} })
	f.handle("session.list", func(map[string]any) any { return herdrFail{Code: "boom", Message: "herdr is gone"} })
	res, err = n.listPanes(context.Background())
	if err != nil || len(res.Hosts) != 1 || len(res.Hosts[0].Panes) != 2 {
		t.Errorf("list while herdr fails = %+v, %v", res, err)
	}
}

// Only the terminal calls know a pane address, and only for a pane the host lists.
func TestPaneAddressReachesOnlyTheTerminal(t *testing.T) {
	streamingHerdr()
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	f.reply("session.snapshot", paneSnapshot(cwd))
	f.screen["w1:p2"] = "$ ls\nfile\n"
	if _, err := n.listPanes(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if pane, err := n.terminalPane(ctx, "pane:w1:p2"); err != nil || pane != "w1:p2" {
		t.Fatalf("terminalPane = %q, %v", pane, err)
	}
	if _, err := n.terminalPane(ctx, "pane:w9:p9"); err == nil {
		t.Error("a pane the host does not list was addressed")
	}
	if _, err := n.terminalPane(ctx, "pane:"); err == nil {
		t.Error("an empty pane address was accepted")
	}
	// The chat paths resolve a session id and never an address: typing into a shell through them is not possible.
	if pane, err := n.paneFor(ctx, "pane:w1:p2"); err == nil {
		t.Errorf("paneFor accepted a pane address: %q", pane)
	}
	if _, err := request(t, n, "session.send", map[string]any{"session_id": "pane:w1:p2", "text": "rm -rf ~"}); err == nil {
		t.Error("session.send reached a shell by pane address")
	}
	for _, c := range f.calls() {
		if strings.Contains(c.Method, "send") && c.Params["pane_id"] == "w1:p2" {
			t.Errorf("herdr was told to type into the shell: %s", c)
		}
	}
	// The screen read (the polling fallback) works through the address.
	res, err := request(t, n, "session.screen", map[string]any{"session_id": "pane:w1:p2", "lines": 10})
	if err != nil || !strings.Contains(res["text"].(string), "file") {
		t.Errorf("screen = %v, %v", res, err)
	}
	gone, err := request(t, n, "session.screen", map[string]any{"session_id": "pane:w9:p9"})
	if err != nil || gone["status"] != proto.StatusOffline {
		t.Errorf("screen of a pane that is not there = %v, %v", gone, err)
	}
}

// A shell pane streams like an agent's: terminal.watch on its address starts the observe on that pane.
func TestTerminalWatchStreamsAShellByPaneAddress(t *testing.T) {
	streamingHerdr()
	n := &Node{agents: map[string]host.Agent{}, procs: map[string]*assistantProc{}, state: newState(),
		hosts:   herdrHosts(&herdr.Client{Bin: fakeObserveHerdr(t)}),
		layouts: map[string]layout{host.Herdr: {panes: []host.Agent{{PaneID: "w1:p1"}}, at: time.Now()}}}
	ctx := context.Background()
	res, err := n.terminalWatch(ctx, "pane:w1:p1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res["cols"] != 80 || res["rows"] != 24 || res["control"] != false {
		t.Fatalf("watch = %v", res)
	}
	if n.termStreamOf("pane:w1:p1") == nil {
		t.Error("no stream kept under the address")
	}
	n.terminalUnwatch("pane:w1:p1")
	if n.termStreamOf("pane:w1:p1") != nil {
		t.Error("unwatch left the stream")
	}
	if _, err := n.terminalWatch(ctx, "pane:w1:p7", nil); err == nil {
		t.Error("watched a pane the host does not list")
	}
}

// "New terminal": a tab in the workspace, in the workspace's own directory, not focused, named shell. The new pane is
// then in the layout, so its terminal opens at once. With cwd (the file page's Open Terminal Here, #5832) the tab starts
// in that folder when the read guard lets the page see it; the folder is the tab's directory, never typed into it.
func TestNewShellOpensATabInTheWorkspace(t *testing.T) {
	streamingHerdr()
	n, f, home := herdrNode(t)
	cwd := mkdir(t, filepath.Join(home, "proj"))
	f.reply("session.snapshot", paneSnapshot(cwd))
	if _, err := n.listPanes(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.reset()
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t3"}, "root_pane": map[string]any{"pane_id": "w1:p3"}})
	f.reply("session.snapshot", snapshot(nil, nil, nil, []any{map[string]any{"pane_id": "w1:p3", "tab_id": "w1:t3", "workspace_id": "w1"}}))

	res, err := request(t, n, "panes.new", map[string]any{"workspace_id": "w1"})
	if err != nil {
		t.Fatal(err)
	}
	if res["pane"] != "w1:p3" || res["terminal"] != "pane:w1:p3" || res["tab_id"] != "w1:t3" {
		t.Errorf("result = %v", res)
	}
	assertTrace(t, f.trace("tab.create"), []string{
		call("tab.create", map[string]any{"workspace_id": "w1", "cwd": cwd, "label": "shell", "focus": false}),
	})
	if _, err := n.terminalPane(context.Background(), "pane:w1:p3"); err != nil {
		t.Errorf("the new pane is not addressable: %v", err)
	}

	// Open Terminal Here: the folder asked for, in the workspace named or, with none, the host's first.
	sub := mkdir(t, filepath.Join(cwd, "sub"))
	realSub, _ := filepath.EvalSymlinks(sub)
	for _, params := range []map[string]any{{"workspace_id": "w1", "cwd": "~/proj/sub"}, {"cwd": sub}} {
		f.reset()
		f.reply("session.snapshot", paneSnapshot(cwd))
		if err := n.refreshHosts(context.Background()); err != nil { // the layout with its workspaces again
			t.Fatal(err)
		}
		f.reset()
		f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w1:t4"}, "root_pane": map[string]any{"pane_id": "w1:p4"}})
		f.reply("session.snapshot", paneSnapshot(cwd))
		if _, err := request(t, n, "panes.new", params); err != nil {
			t.Fatalf("%v: %v", params, err)
		}
		got := f.trace("tab.create")
		if len(got) != 1 || !strings.Contains(got[0], realSub) || !strings.Contains(got[0], `"workspace_id":"w1"`) {
			t.Fatalf("%v: tab.create = %v, want %s in w1", params, got, realSub)
		}
	}
	// Not a folder the page may see, nor a file.
	mkdir(t, filepath.Join(home, ".ssh"))
	if err := os.WriteFile(filepath.Join(cwd, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.reset()
	for _, bad := range []string{"~/.ssh", "~/proj/a.txt", "~/proj/nope"} {
		if _, err := request(t, n, "panes.new", map[string]any{"workspace_id": "w1", "cwd": bad}); err == nil {
			t.Errorf("opened a terminal in %s", bad)
		}
	}
	if got := f.trace("tab.create"); len(got) != 0 {
		t.Errorf("tab.create for a refused folder: %v", got)
	}

	f.reset()
	if _, err := request(t, n, "panes.new", map[string]any{"workspace_id": "w404"}); err == nil {
		t.Error("opened a tab in a workspace the host does not have")
	}
	if _, err := request(t, n, "panes.new", map[string]any{}); err == nil {
		t.Error("opened a tab without a workspace")
	}
	if got := f.trace("tab.create"); len(got) != 0 {
		t.Errorf("tab.create for a refused request: %v", got)
	}
}

// "New Terminal" on a computer whose engine runs with no workspace yet (cwd "~", no workspace_id): the shell opens as the
// first tab of a new workspace in the home folder, named shell, the way an Agent start opens one.
func TestNewShellOpensAWorkspaceWhenThereIsNone(t *testing.T) {
	streamingHerdr()
	n, f, home := herdrNode(t)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	if _, err := n.listPanes(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.reset()
	f.reply("workspace.create", map[string]any{"workspace": map[string]any{"workspace_id": "w1"},
		"tab": map[string]any{"tab_id": "w1:t1"}, "root_pane": map[string]any{"pane_id": "w1:p1"}})
	f.reply("session.snapshot", snapshot([]any{map[string]any{"workspace_id": "w1", "label": "home", "number": 1}}, nil, nil,
		[]any{map[string]any{"pane_id": "w1:p1", "tab_id": "w1:t1", "workspace_id": "w1"}}))

	res, err := request(t, n, "panes.new", map[string]any{"kind": host.Herdr, "cwd": "~"})
	if err != nil {
		t.Fatal(err)
	}
	if res["pane"] != "w1:p1" || res["terminal"] != "pane:w1:p1" || res["tab_id"] != "w1:t1" {
		t.Errorf("result = %v", res)
	}
	realHome, _ := filepath.EvalSymlinks(home)
	assertTrace(t, f.trace("workspace.create", "tab.create", "tab.rename"), []string{
		call("workspace.create", map[string]any{"cwd": realHome, "label": filepath.Base(realHome), "focus": false}),
		call("tab.rename", map[string]any{"tab_id": "w1:t1", "label": "shell"}),
	})
	if _, err := n.terminalPane(context.Background(), "pane:w1:p1"); err != nil {
		t.Errorf("the new pane is not addressable: %v", err)
	}
}

// Open Terminal Here from a runtime's session (a Paseo session names its runtime's workspace, which has no terminal):
// the shell opens in the terminal engine's workspace that holds the folder, not in the first one; with none holding it,
// in a new workspace there.
func TestNewShellForARuntimeWorkspaceOpensWhereTheFolderIs(t *testing.T) {
	streamingHerdr()
	n, f, home := herdrNode(t)
	other := mkdir(t, filepath.Join(home, "other"))
	cwd := mkdir(t, filepath.Join(home, "proj"))
	paseoWorkspaceOn(n, "wks_1", cwd)
	herdrSpaces(f, other, cwd)
	if _, err := n.listPanes(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.reset()
	herdrSpaces(f, other, cwd)
	f.reply("tab.create", map[string]any{"tab": map[string]any{"tab_id": "w2:t3"}, "root_pane": map[string]any{"pane_id": "w2:p3"}})
	if _, err := request(t, n, "panes.new", map[string]any{"workspace_id": "wks_1", "cwd": cwd}); err != nil {
		t.Fatal(err)
	}
	realCwd, _ := filepath.EvalSymlinks(cwd) // the node asks herdr for the resolved folder
	assertTrace(t, f.trace("workspace.create", "tab.create"), []string{
		call("tab.create", map[string]any{"workspace_id": "w2", "cwd": realCwd, "label": "shell", "focus": false}),
	})

	f.reset()
	f.reply("workspace.list", map[string]any{"workspaces": []any{map[string]any{"workspace_id": "w1", "label": "other", "cwd": other}}})
	f.reply("workspace.create", map[string]any{"workspace": map[string]any{"workspace_id": "w3"},
		"tab": map[string]any{"tab_id": "w3:t1"}, "root_pane": map[string]any{"pane_id": "w3:p1"}})
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	if _, err := request(t, n, "panes.new", map[string]any{"workspace_id": "wks_1", "cwd": cwd}); err != nil {
		t.Fatal(err)
	}
	assertTrace(t, f.trace("workspace.create", "tab.create", "tab.rename"), []string{
		call("workspace.create", map[string]any{"cwd": realCwd, "label": "proj", "focus": false}),
		call("tab.rename", map[string]any{"tab_id": "w3:t1", "label": "shell"}),
	})
}

func TestPanesResponseJSON(t *testing.T) {
	b, err := json.Marshal(proto.PanesResponse{Hosts: []proto.PaneHost{{Kind: "herdr", Panes: []proto.PaneItem{{Pane: "w1:p1", Terminal: "pane:w1:p1"}}}}})
	if err != nil || !strings.Contains(string(b), `"terminal":"pane:w1:p1"`) || strings.Contains(string(b), "session_id") {
		t.Errorf("json = %s, %v", b, err)
	}
}
