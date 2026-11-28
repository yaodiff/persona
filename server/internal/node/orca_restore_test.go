package node

import (
	"maps"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
)

// A reboot stops orcad and its terminal daemon, and Orca 1.4.277 takes no command from Persona to bring an Agent back
// with: it lists none of its kept tabs until a client opens one, then resumes the Agent with its own bare command
// (#17038, docs/integrations/engines.md "Orca after a reboot (#17038)"). What the node shows meanwhile is what happened:
// before the tab is opened, a session Persona started in it is interrupted (Continue resumes it on its launcher);
// once Orca has brought it back bare, it runs in that tab off its launcher, and the next message after a switch back to
// that launcher quits it and starts it there again in the same terminal, with Persona's command.
func TestOrcaAfterARebootAPersonaAgentIsShownAsItIs(t *testing.T) {
	withOrcaShell(t, "-zsh")
	n, _, o, procs, home := orcaNode(t)
	withLoginShellConfigDir(t, "", true)
	withShellLaunchers(t)
	forgetOfflineScan()
	cwd := mkdir(t, filepath.Join(home, "proj"))
	pad := `{"type":"user","message":{"role":"user","content":"` + strings.Repeat("x", 2048) + `"}}` // offline listing skips files under 2 KB
	writeTranscript(t, home, cwd, sidA, time.Now().Add(-time.Hour), append([]string{pad}, finishedTurn(cwd)...)...)
	knownProbes(t, "work")
	n.setManaged([]proto.ManagedLauncher{{Name: "work", BaseURL: "https://gateway.example.com", Model: "glm-5"}})
	n.bootAt = time.Now()
	n.state.put(sidA, sessionState{Pane: "orca:tab1:leaf1", Launcher: "work", Cwd: cwd,
		StartedAt: n.bootAt.Add(-2 * time.Hour).UTC(), RanAt: n.bootAt.Add(-time.Minute).Unix()})
	o.SetWorktrees(orcarttest.Worktree("repo::"+cwd, cwd, "refs/heads/main"))

	// The claudes the scan of running claudes sees (claudedirs.go), by pid: none yet.
	var mu sync.Mutex
	running := map[int]procEnv{}
	rescan := func() {
		n.cdirs = newClaudeDirs()
		n.cdirs.scan = func() map[int]procEnv {
			mu.Lock()
			defer mu.Unlock()
			return maps.Clone(running)
		}
	}
	was := processAlive
	processAlive = func(pid int, _ string) bool {
		mu.Lock()
		defer mu.Unlock()
		_, ok := running[pid]
		return ok
	}
	t.Cleanup(func() { processAlive = was })
	rescan()
	// What the Orca host keeps from its last look goes, as it would in the time a reboot and reopening a tab take.
	forget := func() {
		h := n.hosts.Get(host.Orca).(*orcaHost)
		h.mu.Lock()
		h.view, h.viewAt, h.procsAt = nil, time.Time{}, time.Time{}
		h.mu.Unlock()
	}

	// Orca lists none of its kept tabs: the session is interrupted, stopped by the restart.
	if row := byID(listSessions(t, n))[sidA]; row.Status != proto.StatusInterrupted {
		t.Fatalf("before Orca reopens the tab: %+v", row)
	}
	if c := n.stopCause(sidA); c != proto.StopRestart {
		t.Errorf("stop cause %q, want the restart's", c)
	}

	// The user opened the tab in Orca, which resumed it with its own command: no Persona --mcp-config.
	const orcasPID, oursPID = 4242, 4343
	mu.Lock()
	running[orcasPID] = procEnv{Cmd: true}
	mu.Unlock()
	rescan()
	writeClaudePID(t, home, orcasPID, sidA)
	o.SetTerminals(orcarttest.Terminal("term_1", "tab1", "leaf1", "repo::"+cwd, cwd, "refs/heads/main", "claude", "claude"))
	o.SetWorktrees(orcarttest.Worktree("repo::"+cwd, cwd, "refs/heads/main", orcarttest.Agent("tab1:leaf1", "claude", "done")))
	procs.set("tab1:leaf1", orcaProc{pid: orcasPID, agent: "claude"})
	o.SetScreen("term_1", orcaClaudeScreen(orcaManualFooter))
	forget()
	if row := byID(listSessions(t, n))[sidA]; row.PaneID != "orca:tab1:leaf1" || row.Status == proto.StatusInterrupted ||
		row.Launcher != "work" || !row.OffLauncher {
		t.Fatalf("resumed by Orca: pane %q, status %q, launcher %q, off_launcher %v", row.PaneID, row.Status, row.Launcher, row.OffLauncher)
	}

	// Back to the launcher it was started on: carried out with the next message, in the same terminal.
	if err := pick(t, n, map[string]any{"launcher": "work", "from": "work"}); err != nil {
		t.Fatalf("moving it back to its launcher: %v", err)
	}
	if ps := n.pendingSwitch(sidA); ps == nil || ps.Launcher != "work" {
		t.Fatalf("no switch back to its launcher waits: %+v", ps)
	}
	writeClaudePID(t, home, oursPID, sidA) // read only while that pid runs
	o.Reset()
	ctrlC := 0
	var startLine string
	o.Handle("terminal.send", func(p map[string]any) any {
		text, _ := p["text"].(string)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case text == "\x03":
			if ctrlC++; ctrlC == 2 { // Orca's claude quits to its shell
				delete(running, orcasPID)
				procs.set("tab1:leaf1")
				o.SetProcess("term_1", orcarttest.ShellAtRest(orcaShellPID))
				o.SetScreen("term_1", "% ")
			}
		case strings.HasPrefix(text, "cd -- "): // Persona's start: claude comes up on its launcher
			startLine = text
			_, line, _ := strings.Cut(strings.TrimSuffix(text, "\r"), " && ")
			if cmd, ok := startFileCommand(line); ok {
				startLine = cmd
			}
			running[oursPID] = procEnv{Cmd: true, MCPConfig: filepath.Join(n.dir, "mcp.json")}
			procs.set("tab1:leaf1", orcaProc{pid: oursPID, agent: "claude"})
			o.SetProcess("term_1", orcarttest.Running("claude", orcaShellPID))
			o.SetScreen("term_1", orcaClaudeScreen(orcaManualFooter))
		}
		return map[string]any{"send": map[string]any{"handle": p["terminal"], "accepted": true, "bytesWritten": len(text)}}
	})
	if err := tell(t, n, "go on", true); err != nil {
		t.Fatalf("send: %v\n%s", err, strings.Join(o.Trace(), "\n"))
	}
	if c := o.Trace("terminal.create", "terminal.close"); len(c) != 0 {
		t.Errorf("a terminal was opened or closed: %v", c)
	}
	mu.Lock()
	started := startLine
	mu.Unlock()
	if !strings.Contains(started, " launch work ") || !strings.Contains(started, "--resume "+sidA) ||
		!strings.Contains(started, "--mcp-config "+filepath.Join(n.dir, "mcp.json")) {
		t.Fatalf("started %q", started)
	}
	for _, p := range o.Params("terminal.send") {
		if p["terminal"] != "term_1" || p["expectedIncarnationId"] != "inc-term_1" {
			t.Errorf("a write outside the agent's terminal process: %v", p)
		}
	}
	rescan()
	forget()
	if row := byID(listSessions(t, n))[sidA]; row.Launcher != "work" || row.OffLauncher {
		t.Errorf("started by Persona's command: launcher %q, off_launcher %v", row.Launcher, row.OffLauncher)
	}
}
