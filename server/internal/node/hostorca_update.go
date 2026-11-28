package node

import (
	"context"
	"errors"
	"maps"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/orcart"
)

// orcaUpdateSessionSource names where an Orca pane's session came from in an update inventory read: the kernel argv of
// its foreground Codex (orcaUpdateSession).
const orcaUpdateSessionSource = "orca:remote-resume-argv"

// orcaUpdateArgv reads a process's kernel argv for the update inventory's Orca read (tests swap in a fake table).
var orcaUpdateArgv = updateProcessArgv

// SnapshotForUpdate is the update inventory's read of Orca (update_inventory_hosts.go): terminal.list with its layouts,
// a full worktree.ps and a new scan of the processes, none of them from what the listing keeps between polls, and
// neither list cut short at Orca's limit. Where the listing goes on without a part (worktree.ps failing, the processes
// unreadable), this read fails instead: an agent
// would then be told only by Orca's rows. Orca not running is host.ErrNotRunning, an empty source.
//
// The states stay display evidence only: the terminal.agentStatus fallback is not asked. Orca's rows name no session,
// so a Codex pane's session is the thread its foreground Codex resumes on an app-server, by its kernel argv
// (orcaUpdateSession); the inventory then binds that process to the app-server's activity as it does under herdr.
//
// It also stands for the Orca bridge (orcabridge.go), whose sessions are Orca's chats: a chat's row is Orca's display
// state, with no background or child inventory behind it, so any chat on this machine refuses the read, as a Claude
// terminal does (updateSourceCoverage then does not count the bridge as a runtime of its own).
func (h *orcaHost) SnapshotForUpdate(ctx context.Context) (*host.Layout, error) {
	terms, tabs, err := h.listTerminals(ctx, true)
	if err != nil {
		return nil, err
	}
	trees, err := h.c.AllWorktrees(ctx)
	if err != nil {
		return nil, orcaErr(err)
	}
	if orcaHasChat(trees) {
		return nil, errors.New("Orca chat lacks authoritative update inventory")
	}
	procs, err := h.scan()
	h.mu.Lock()
	h.procs, h.procsErr, h.procsAt = procs, err, time.Now()
	rekeyed := maps.Clone(h.rekeyed)
	h.mu.Unlock()
	if err != nil {
		return nil, err
	}
	v := &orcaView{terms: terms, tabs: tabs, trees: trees, rows: orcaRows(trees), procs: procs, procsKnown: true,
		fallback: map[string]string{}, rekeyed: rekeyed}
	l := v.layout()
	sessions := map[string]*host.AgentSession{}
	for _, t := range terms {
		if a := v.agent(t); a.Agent == "codex" {
			sessions[a.PaneID] = orcaUpdateSession(v.paneProcs(t.PaneKey()))
		}
	}
	for _, list := range [][]host.Agent{l.Panes, l.Agents} {
		for i := range list {
			if s := sessions[list[i].PaneID]; s != nil {
				list[i].AgentSession = s
			}
		}
	}
	return l, ctx.Err()
}

// orcaUpdateRead: Orca is one of the configured hosts, so the update inventory reads it (SnapshotForUpdate), its chats
// included.
func (n *Node) orcaUpdateRead() bool {
	if n.hosts == nil {
		return false
	}
	for _, h := range n.hosts.All() {
		if _, ok := h.(*orcaHost); ok {
			return true
		}
	}
	return false
}

// orcaHasChat: a worktree on this machine has one of Orca's chats with a turn (an agent row under a session pane key,
// any agent: what orcaOpenChats lists from worktree.ps). A chat only the tabs list has had no turn yet, so nothing of it
// runs; one the bridge lists closed (its book) is not open in Orca. A worktree on an SSH target is that machine's.
func orcaHasChat(trees []orcart.Worktree) bool {
	for _, w := range trees {
		if w.HostID != "" && w.HostID != "local" {
			continue
		}
		for _, a := range w.Agents {
			if _, ok := orcart.SessionOf(a.PaneKey); ok {
				return true
			}
		}
	}
	return false
}

// orcaRows are worktree.ps's agent rows by pane key.
func orcaRows(trees []orcart.Worktree) map[string]orcart.AgentRow {
	rows := map[string]orcart.AgentRow{}
	for _, w := range trees {
		for _, a := range w.Agents {
			rows[a.PaneKey] = a
		}
	}
	return rows
}

// paneProcs are the agent processes in front in a pane: under its key, else the one Orca re-keyed it to (#5547).
func (v *orcaView) paneProcs(key string) []orcaProc {
	if ps := v.procs[key]; len(ps) > 0 {
		return ps
	}
	if now := v.rekeyed[key]; now != "" {
		return v.procs[now]
	}
	return nil
}

// orcaUpdateSession is a Codex pane's session for the update inventory: the thread its foreground Codex explicitly resumes
// on an app-server (updateRemoteResume, from the kernel argv, never a title or ps's formatted line). nil when no Codex
// there does, when an argv cannot be read, or when two of them name different threads: the inventory then refuses the
// pane, as it does a herdr pane without a session.
func orcaUpdateSession(procs []orcaProc) *host.AgentSession {
	thread := ""
	for _, p := range procs {
		if p.agent != "codex" {
			continue
		}
		argv, err := orcaUpdateArgv(p.pid)
		if err != nil {
			return nil
		}
		_, id := updateRemoteResume(argv)
		if id == "" {
			continue
		}
		if thread != "" && id != thread {
			return nil
		}
		thread = id
	}
	if thread == "" {
		return nil
	}
	return &host.AgentSession{Source: orcaUpdateSessionSource, Agent: "codex", Kind: "id", Value: thread}
}
