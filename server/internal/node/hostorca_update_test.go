package node

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/orcart/orcarttest"
	"github.com/nwverse/persona/server/internal/proto"
)

// The manager update inventory with Orca among the hosts (#17049): Orca's update read (hostorca_update.go) against a
// fake Orca runtime and a fake process table. No test here talks to a real Orca.

// orcaUpdateFixture is an update inventory node whose hosts are Orca (plus extra), on a fake runtime with no
// terminals, no worktrees and no agent processes.
func orcaUpdateFixture(t *testing.T, extra ...host.Host) (*Node, nodeclient.UpdateInventoryTarget, *orcarttest.Fake, *fakeProcs, *orcaHost) {
	t.Helper()
	n, target := updateInventoryFixture(t)
	o := orcarttest.New(t)
	procs := &fakeProcs{}
	h := newOrcaHost(&orcart.Client{MetaPath: o.MetaPath})
	h.scan = procs.scan
	n.hosts = host.NewRegistry(append([]host.Host{h}, extra...)...)
	return n, target, o, procs, h
}

// orcaArgv makes the update read find these kernel argvs, by pid.
func orcaArgv(t *testing.T, argv map[int][]string) {
	t.Helper()
	old := orcaUpdateArgv
	orcaUpdateArgv = func(pid int) ([]string, error) {
		if a, ok := argv[pid]; ok {
			return a, nil
		}
		return nil, errors.New("no such process")
	}
	t.Cleanup(func() { orcaUpdateArgv = old })
}

// An idle machine with Orca among its hosts gets complete evidence, alone or next to herdr; Orca not running is an
// empty source. Before #17049 the Orca host had no update read and every such inventory failed.
func TestOrcaUpdateInventoryCompletes(t *testing.T) {
	for _, name := range []string{"orca", "herdr+orca", "not-running"} {
		t.Run(name, func(t *testing.T) {
			var extra []host.Host
			if name == "herdr+orca" {
				extra = append(extra, &inventoryHost{bareHost: bareHost{kind: host.Herdr}})
			}
			n, target, o, _, h := orcaUpdateFixture(t, extra...)
			o.SetTerminals(orcaTerm("term_1", "tab1", "leaf1", "")) // a shell
			o.SetWorktrees(orcarttest.Worktree("repo::"+orcaCwd, orcaCwd, "refs/heads/feature-x"))
			if name == "not-running" {
				h.c = &orcart.Client{MetaPath: t.TempDir() + "/missing.json"}
			}
			out, err := n.collectUpdateInventory(context.Background(), target, emptyUpdateCensus)
			if err != nil || !out.Complete {
				t.Fatalf("idle machine with Orca: %+v %v", out, err)
			}
			source := out.Sources["host:"+host.Orca]
			if name == "not-running" && source != "not-running" || name != "not-running" && !validUpdateRevision(source) {
				t.Fatalf("Orca source %q", source)
			}
			if name == "herdr+orca" && !validUpdateRevision(out.Sources["host:"+host.Herdr]) {
				t.Fatalf("herdr source lost: %+v", out.Sources)
			}
			if inv := o.Invalid(); len(inv) != 0 {
				t.Fatalf("params Orca would refuse: %v", inv)
			}
		})
	}
}

// The read asks Orca and the process table again each time: the listing's cached look, its kept worktree.ps snapshot
// and its last process scan are not evidence.
func TestOrcaUpdateReadIsFresh(t *testing.T) {
	_, _, o, procs, h := orcaUpdateFixture(t)
	o.SetTerminals(orcaTerm("term_1", "tab1", "leaf1", ""))
	o.SetWorktrees(orcarttest.Worktree("repo::"+orcaCwd, orcaCwd, "refs/heads/feature-x"))
	if _, err := h.Snapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	o.Reset()
	scans := procs.scans
	for range 2 {
		if _, err := h.SnapshotForUpdate(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if o.Count("terminal.list") != 2 || o.Count("worktree.ps") != 2 || procs.scans != scans+2 {
		t.Fatalf("cached reads: terminal.list %d, worktree.ps %d, scans %d", o.Count("terminal.list"), o.Count("worktree.ps"), procs.scans-scans)
	}
	for _, p := range o.Params("worktree.ps") {
		if p["afterSnapshotId"] != nil {
			t.Fatalf("worktree.ps inside the listing's snapshot series: %v", p)
		}
	}
	for _, p := range o.Params("terminal.list") {
		if p["includeVisualLayouts"] != true {
			t.Fatalf("terminal.list without its layouts: %v", p)
		}
	}
	if tr := o.Trace("terminal.agentStatus"); len(tr) != 0 {
		t.Fatalf("display status asked: %v", tr)
	}
}

// A Codex in an Orca terminal that explicitly resumes a thread on a configured app-server is covered by that
// app-server's activity, as under herdr: Orca names no session, so it comes from the foreground Codex's kernel argv,
// in either remote form (Persona's own start writes the global one).
func TestOrcaUpdateInventoryCoversRemoteCodex(t *testing.T) {
	for _, form := range []string{"subcommand", "global", "global-no-alt-screen"} {
		t.Run(form, func(t *testing.T) {
			n, target, o, procs, _ := orcaUpdateFixture(t, &inventoryHost{bareHost: bareHost{kind: host.Herdr}})
			f, c := connectRemoteFixture(t, n, t.TempDir(), "terminal-source", remoteRaw)
			n.codices.mu.Lock()
			n.codices.config.Servers = append(n.codices.config.Servers, c.spec)
			n.codices.mu.Unlock()
			o.SetTerminals(orcaTerm("term_1", "tab1", "leaf1", "codex"))
			o.SetWorktrees(orcarttest.Worktree("repo::"+orcaCwd, orcaCwd, "refs/heads/feature-x",
				orcarttest.Agent("tab1:leaf1", "codex", "done")))
			procs.set("tab1:leaf1", orcaProc{pid: 120, agent: "codex"}, orcaProc{pid: 123, agent: "codex"}) // npm's wrapper, then codex
			argv := map[string][]string{
				"subcommand":           {"/usr/bin/codex", "resume", "--remote", c.spec.Endpoint, remoteRaw},
				"global":               {"codex", "--remote", c.spec.Endpoint, "resume", remoteRaw},
				"global-no-alt-screen": {"codex", "--remote", c.spec.Endpoint, "--no-alt-screen", "resume", remoteRaw},
			}[form]
			orcaArgv(t, map[int][]string{120: {"node", "/usr/lib/node_modules/codex/bin/codex.js"}, 123: argv})
			endpoint, thread := updateRemoteResume(argv)
			p := updateProcess{PID: 123, Signature: strings.Repeat("b", 64), RemoteEndpoint: endpoint, RemoteThread: thread}
			out, err := n.collectUpdateInventory(context.Background(), target, func(context.Context) ([]updateProcess, error) { return []updateProcess{p}, nil })
			if err != nil || !out.Complete {
				t.Fatalf("remote Codex in Orca: %+v %v", out, err)
			}
			found, id := false, codexSourceID(c.spec.Home, remoteRaw, n.codices.config.LegacyHome)
			for _, row := range out.Rows {
				found = found || row.Source == "app-server:terminal-source" && row.Session == id && row.Live
			}
			if !found || !validUpdateRevision(out.Sources["host:"+host.Orca]) {
				t.Fatalf("Orca's Codex not reported through its app-server: %+v", out)
			}
			for _, method := range []string{"thread/resume", "turn/start"} {
				if f.count(method) != 0 {
					t.Fatalf("inventory mutated runtime through %s", method)
				}
			}
			if tr := o.Trace("terminal.send", "terminal.close", "terminal.create"); len(tr) != 0 {
				t.Fatalf("inventory wrote to Orca: %v", tr)
			}
		})
	}
}

// Everything the read cannot vouch for refuses the evidence: a Claude or an unbound Codex in a terminal (as under
// herdr), an argv it cannot read, two threads in one pane, an Orca chat, a list Orca cut short, worktree.ps or the
// process table failing, and a foreign runtime calling itself Orca.
func TestOrcaUpdateInventoryRefusesWhatItCannotCover(t *testing.T) {
	for _, name := range []string{"claude", "local-codex", "argv-unreadable", "two-threads", "chat", "terminals-truncated",
		"worktrees-truncated", "worktree-ps-fails", "scan-fails", "foreign-orca-runtime", "orca-known-without-orca-host"} {
		t.Run(name, func(t *testing.T) {
			n, target, o, procs, _ := orcaUpdateFixture(t)
			_, c := connectRemoteFixture(t, n, t.TempDir(), "terminal-source", remoteRaw)
			n.codices.mu.Lock()
			n.codices.config.Servers = append(n.codices.config.Servers, c.spec)
			n.codices.mu.Unlock()
			o.SetTerminals(orcaTerm("term_1", "tab1", "leaf1", "codex"))
			trees := []map[string]any{orcarttest.Worktree("repo::"+orcaCwd, orcaCwd, "refs/heads/feature-x")}
			procs.set("tab1:leaf1", orcaProc{pid: 123, agent: "codex"})
			bound := []string{"codex", "--remote", c.spec.Endpoint, "resume", remoteRaw}
			argv := map[int][]string{123: bound}
			p := updateProcess{PID: 123, Signature: strings.Repeat("b", 64), RemoteEndpoint: c.spec.Endpoint, RemoteThread: remoteRaw}
			switch name {
			case "claude":
				procs.set("tab1:leaf1", orcaProc{pid: 123, agent: "claude"})
				p = updateProcess{PID: 123, Signature: strings.Repeat("b", 64)}
			case "local-codex":
				argv[123] = []string{"codex", "--no-daemon"}
				p.RemoteEndpoint, p.RemoteThread = "", ""
			case "argv-unreadable":
				delete(argv, 123)
			case "two-threads":
				procs.set("tab1:leaf1", orcaProc{pid: 123, agent: "codex"}, orcaProc{pid: 124, agent: "codex"})
				argv[124] = []string{"codex", "--remote", c.spec.Endpoint, "resume", remoteOther}
			case "chat":
				trees[0] = orcarttest.Worktree("repo::"+orcaCwd, orcaCwd, "refs/heads/feature-x",
					orcarttest.Agent(orcart.SessionPanePrefix+"chat-1", "claude", "done"))
			case "terminals-truncated":
				o.Handle("terminal.list", func(map[string]any) any {
					return map[string]any{"terminals": []any{}, "visualLayouts": []any{}, "truncated": true}
				})
			case "worktrees-truncated":
				o.Handle("worktree.ps", func(map[string]any) any {
					return map[string]any{"worktrees": []any{}, "totalCount": 300, "truncated": true}
				})
			case "worktree-ps-fails":
				o.Handle("worktree.ps", func(map[string]any) any { return orcarttest.Fail{Code: "internal_error"} })
			case "scan-fails":
				procs.err = errors.New("process table unavailable")
			case "foreign-orca-runtime":
				n.runtimeHost = &RuntimeHost{}
				n.runtimeHost.orca.instance = "bridge-instance"
				n.runtimes.conns[1] = &runtimeConn{ready: true, hello: runtimeHello{Runtime: orcaRuntimeID, Instance: "another-instance"}}
			case "orca-known-without-orca-host":
				n.hosts = host.NewRegistry(&inventoryHost{bareHost: bareHost{kind: host.Herdr}})
				n.runtimes.known["chat-session"] = knownSession{runtime: orcaRuntimeID}
			}
			o.SetWorktrees(trees...)
			orcaArgv(t, argv)
			out, err := n.collectUpdateInventory(context.Background(), target, func(context.Context) ([]updateProcess, error) { return []updateProcess{p}, nil })
			if err == nil || out.Complete {
				t.Fatalf("uncovered Orca state became complete: %+v %v", out, err)
			}
		})
	}
}

// The built-in Orca bridge is no runtime of its own for the inventory while Orca's update read stands for it: its
// connection (told by its hello instance) and the chats it once listed, Orca having none now.
func TestOrcaUpdateInventoryCoversTheOrcaBridge(t *testing.T) {
	n, target, o, _, _ := orcaUpdateFixture(t)
	o.SetWorktrees(orcarttest.Worktree("repo::"+orcaCwd, orcaCwd, "refs/heads/feature-x"))
	n.runtimeHost = &RuntimeHost{}
	n.runtimeHost.orca.instance = "bridge-instance"
	n.runtimes.conns[1] = &runtimeConn{ready: true, hello: runtimeHello{Runtime: orcaRuntimeID, Instance: "bridge-instance"}}
	n.runtimes.known["chat-session"] = knownSession{runtime: orcaRuntimeID}
	out, err := n.collectUpdateInventory(context.Background(), target, emptyUpdateCensus)
	if err != nil || !out.Complete {
		t.Fatalf("the built-in bridge with no chats: %+v %v", out, err)
	}
	// A chat in Orca now: the same read refuses.
	o.SetWorktrees(orcarttest.Worktree("repo::"+orcaCwd, orcaCwd, "refs/heads/feature-x",
		orcarttest.Agent(orcart.SessionPanePrefix+"chat-1", "claude", "done")))
	if out, err := n.collectUpdateInventory(context.Background(), target, emptyUpdateCensus); err == nil || out.Complete {
		t.Fatalf("an Orca chat became complete: %+v", out)
	}
	// A chat on an SSH target is that machine's.
	remote := orcarttest.Worktree("ssh-repo::/srv/x", "/srv/x", "refs/heads/main", orcarttest.Agent(orcart.SessionPanePrefix+"chat-2", "claude", "done"))
	remote["hostId"] = "ssh-test-host"
	o.SetWorktrees(orcarttest.Worktree("repo::"+orcaCwd, orcaCwd, "refs/heads/feature-x"), remote)
	if out, err := n.collectUpdateInventory(context.Background(), target, emptyUpdateCensus); err != nil || !out.Complete {
		t.Fatalf("another machine's chat refused this one's evidence: %+v %v", out, err)
	}
}

// As wired in a node: the runtime host's own registry, with the bridge it runs while Orca is enabled connected to it
// (it connects even when Orca has nothing to list). The inventory completes, and an external runtime still refuses it.
func TestOrcaUpdateInventoryWithTheRunningBridge(t *testing.T) {
	o := orcarttest.New(t)
	o.SetWorktrees(orcarttest.Worktree("repo::"+orcaCwd, orcaCwd, "refs/heads/feature-x"))
	saved, savedPoll := orcaClient, orcaBridgePoll
	orcaClient = func() *orcart.Client { return &orcart.Client{MetaPath: o.MetaPath} }
	orcaBridgePoll = 50 * time.Millisecond
	n, _ := runtimeNode(t)
	n.runtimeHost.setOrcaBridge(true)
	t.Cleanup(func() {
		n.runtimeHost.setOrcaBridge(false)
		orcaClient, orcaBridgePoll = saved, savedPoll
	})
	waitFor(t, "the bridge connected", func() bool {
		instance := n.runtimeHost.orcaBridgeInstance()
		n.runtimes.mu.Lock()
		defer n.runtimes.mu.Unlock()
		for _, c := range n.runtimes.conns {
			c.mu.Lock()
			ok := c.ready && c.hello.Instance == instance
			c.mu.Unlock()
			if ok && instance != "" {
				return true
			}
		}
		return false
	})
	n.machineID, n.uid = "fixture-machine", "fixture-uid"
	n.stateReady = make(chan struct{})
	close(n.stateReady)
	n.codices = newCodexRegistry()
	n.codices.scan = func() []codexProcess { return nil }
	n.codices.checked = func() ([]codexProcess, bool) { return nil, true }
	n.configureCodex(proto.CodexConfig{})
	n.configured()
	off := false
	n.eng.botmux = &off
	h := newOrcaHost(&orcart.Client{MetaPath: o.MetaPath})
	h.scan = (&fakeProcs{}).scan
	n.hosts = host.NewRegistry(h)
	target := nodeclient.UpdateInventoryTarget{Manager: "m-fixture", Main: remoteRaw, Machine: n.machineID, MachineUID: n.uid,
		AuthorityRevision: strings.Repeat("a", 64)}
	out, err := n.collectUpdateInventory(context.Background(), target, emptyUpdateCensus)
	if err != nil || !out.Complete {
		t.Fatalf("the running Orca bridge refused an idle machine: %+v %v", out, err)
	}
	n.runtimes.mu.Lock()
	n.runtimes.conns[1<<20] = &runtimeConn{ready: true, hello: runtimeHello{Runtime: "dsh", Instance: "x"}}
	n.runtimes.mu.Unlock()
	if out, err := n.collectUpdateInventory(context.Background(), target, emptyUpdateCensus); err == nil || out.Complete {
		t.Fatal("another runtime next to the bridge became complete")
	}
}
