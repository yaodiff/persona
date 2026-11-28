package node

import (
	"context"

	"github.com/nwverse/persona/server/internal/host"
)

// Every tab Persona opens for an agent is marked with personaTabToken (herdr keeps a pane's metadata tokens until the
// pane goes; a herdr restart drops them). When the agent exits — by itself, by /exit, or stopped by Persona
// (retire.go) — the tab stays with the shell it ran in (#4754, docs/integrations/engines.md "Agents run in a
// terminal"), for the user to read and Resume in, until it has sat unchanged for the grace period: then the node
// closes it (finished_close.go, #10111), on herdr only a tab with this mark. The mark names the session the terminal was
// opened for.

// personaTabToken marks a pane Persona opened for an agent; its value is the session id when the start knew it.
const personaTabToken = "persona_tab"

// personaWorktreeToken marks the first shell of a worktree workspace the assistant made for an Agent (#10624): the
// engine opens it with the workspace, and the Agent runs in a tab beside it. The grace-period close takes it once
// every other pane of its workspace is a finished Agent terminal (finished_close.go).
const personaWorktreeToken = "persona_worktree"

// markPersonaTab marks pane as one Persona opened for session id (empty when not known yet): herdr's persona_tab
// token, or, on an engine that keeps no marks (Orca), the node's own record (agentTerminalOpened).
func (n *Node) markPersonaTab(ctx context.Context, launch host.Launcher, pane host.PaneRef, id string) {
	if pane.ID == "" {
		return
	}
	if id == "" {
		id = "agent"
	}
	r, ok := launch.(host.MetadataReporter)
	if !ok {
		n.agentTerminalOpened(launch, pane, id)
		return
	}
	_ = r.ReportMetadata(ctx, pane, "persona", map[string]*string{personaTabToken: &id})
}

// personaTabLocked is the mark of a pane Persona opened for an Agent: the session id it was opened for, "agent" when
// the start did not know it yet, "" for a pane the user opened. herdr's persona_tab token, else the node's own record
// on an engine that keeps no marks. On Orca a matching creation receipt can restore the mark after a node restart.
// n.mu is held.
func (n *Node) personaTabLocked(p host.Agent) string {
	if id := p.Tokens[personaTabToken]; id != "" {
		return id
	}
	id := n.agentTerms[p.PaneID]
	if host.ParsePane(p.PaneID).Kind != host.Orca {
		return id
	}
	bound := n.panes[p.PaneID]
	if id == "" {
		// After a node restart there is no live mark; only a matching receipt
		// may restore one. A binding alone never owns a terminal.
		return n.orcaFinishedOwnerLocked(p, bound, "")
	}
	if bound != "" && id != "agent" && bound != id {
		return ""
	}
	return n.orcaFinishedOwnerLocked(p, id, id)
}

// namePersonaTab gives a pane Persona marked before its session was known ("agent": a Codex start, whose thread id
// comes once Codex runs) the session id (#10111): the mark then names the session as a Claude start's does, so the
// terminal is the session's kept shell (keptPane) and the grace-period close takes the session's recovery lock. A pane
// with no mark, or one naming another session, is left as it is.
func (n *Node) namePersonaTab(ctx context.Context, pane, id string) {
	if pane == "" || !validID(id) {
		return
	}
	n.mu.Lock()
	if n.agentTerms[pane] == "agent" { // an engine without marks: the node's own record
		n.agentTerms[pane] = id
	}
	n.mu.Unlock()
	if host.ParsePane(pane).Kind == host.Orca {
		if h, ok := n.hosts.Get(host.Orca).(*orcaHost); ok {
			if owner := h.claimTerminalOwner(pane, id); owner != nil {
				n.state.update(id, func(st *sessionState) bool {
					if st.Pane != pane || st.OrcaTerminal != nil {
						return false
					}
					st.OrcaTerminal = owner
					return true
				})
			}
		}
		return
	}
	r, ref, err := paneCap[host.MetadataReporter](n, pane)
	if err != nil {
		return
	}
	n.mu.Lock()
	mark, found := "", false
	for _, l := range n.layouts {
		for _, p := range l.panes {
			if p.PaneID == pane {
				mark, found = p.Tokens[personaTabToken], true
			}
		}
	}
	n.mu.Unlock()
	if found && mark != "agent" {
		return
	}
	_ = r.ReportMetadata(ctx, ref, "persona", map[string]*string{personaTabToken: &id})
}

type agentWorktreeKey struct{}

// withAgentWorktree says whether the worktree.create this ctx carries is the assistant's, for an Agent (for_agent).
func withAgentWorktree(ctx context.Context, forAgent bool) context.Context {
	if !forAgent {
		return ctx
	}
	return context.WithValue(ctx, agentWorktreeKey{}, true)
}

// markWorktreeShell marks the first shell of a worktree the assistant made for an Agent (withAgentWorktree) as
// Persona's: herdr's persona_worktree token, or, on an engine without tokens, the node's in-memory record. A worktree
// the user makes, or one with no first pane, is left unmarked.
func (n *Node) markWorktreeShell(ctx context.Context, h host.Host, created *host.Created) {
	if on, _ := ctx.Value(agentWorktreeKey{}).(bool); !on || created == nil || created.Pane.ID == "" {
		return
	}
	if r, ok := h.(host.MetadataReporter); ok {
		mark := "1"
		_ = r.ReportMetadata(ctx, created.Pane, "persona", map[string]*string{personaWorktreeToken: &mark})
		return
	}
	n.mu.Lock()
	if n.agentRoots == nil {
		n.agentRoots = map[string]bool{}
	}
	n.agentRoots[created.Pane.String()] = true
	n.mu.Unlock()
}

// closeWorktreeShells closes the first shell of an Agent's worktree in workspace ws (markWorktreeShell) once the
// Agent's own tab (keep) is open there: an empty tab next to the Agent's otherwise stays until the Agent finishes
// (finishedPanes). Only a shell worktreeShellLocked still passes (no Agent in it or bound to it, nobody watching it) of
// the host's last snapshot is closed; a failed close leaves it for finishedPanes.
func (n *Node) closeWorktreeShells(ctx context.Context, launch host.Launcher, ws, keep string) {
	h, ok := launch.(host.Host)
	if !ok {
		return
	}
	org, ok := h.(host.Organizer)
	if !ok {
		return
	}
	_, marks := h.(host.MetadataReporter)
	var shells []host.Agent
	n.mu.Lock()
	inUse := map[string]bool{}
	for _, a := range n.agents {
		inUse[a.PaneID] = true
	}
	for _, p := range n.layouts[h.Kind()].panes {
		if p.WorkspaceID == ws && p.PaneID != "" && p.PaneID != keep && n.worktreeShellLocked(p, marks, inUse) {
			shells = append(shells, p)
		}
	}
	n.mu.Unlock()
	for _, p := range shells {
		if err := org.Close(ctx, n.hosts.Ref(p.PaneID), p.WorkspaceID, p.TabID); err != nil {
			continue
		}
		n.mu.Lock()
		delete(n.agentRoots, p.PaneID)
		n.mu.Unlock()
	}
}
