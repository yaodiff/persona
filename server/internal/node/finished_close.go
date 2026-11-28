package node

import (
	"context"
	"crypto/sha256"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/host"
)

// A finished Agent's terminal closes on its own after a grace period (#10111, docs/integrations/engines.md "Agents run
// in a terminal"). Since #4754 the pane an Agent leaves keeps its shell so its last output can be read and Resume can
// run in it; nothing closed it, and the shells piled up by the dozen. The node now closes such a pane once it has sat
// unchanged for agentTerminalGrace, checking everything again under the session's recovery lock right before:
//
//   - Persona opened it for an Agent: herdr's persona_tab mark, Orca's live mark or the original Orca terminal
//     identity saved with the session (personatab.go);
//   - no Agent is in it: the engine lists none there, no session the node tracks is bound to it, no failed start keeps
//     it for a retry and no Codex waits for its thread there;
//   - only a shell is in front (herdr: pane.process_info), or, on Orca, Orca proves the shell at rest with nothing
//     else in the terminal (terminal.inspectProcess, restingShell); an engine that cannot say has nothing closed;
//   - no client has its terminal open;
//   - its screen has not changed for the whole grace period; any change starts the clock again.
//
// A pane the user opened has no mark and is never touched; neither is one the user picked for "Run Agent Here"
// (adoptedPane: newTab marks nothing there). Close closes the pane, its tab when it was the tab's only pane, and the
// engine drops a workspace only with its last tab: a workspace goes only when everything in it was such a terminal.
//
// Failure keeps a terminal rather than guessing: an engine that did not answer this round has nothing closed, a close
// that fails is tried again on a later round after the checks, and the clocks live in memory, so a node restart closes
// a terminal later, never sooner. Resume needs nothing new: within the grace period it runs in the kept shell
// (inKeptTerminal, under the same recovery lock), after it in a new tab, as after the user closed the terminal.

// agentTerminalGraceEnv sets the grace period: a Go duration ("30m", "2h"); "0" or "off" keeps finished Agents'
// terminals until the user closes them, as before #10111.
const agentTerminalGraceEnv = "PERSONA_AGENT_TERMINAL_GRACE"

// defaultAgentTerminalGrace is the grace period when agentTerminalGraceEnv is not set.
const defaultAgentTerminalGrace = 30 * time.Minute

// agentTerminalGrace is the grace period: 0 when finished terminals are kept. A var for tests.
var agentTerminalGrace = func() time.Duration {
	v := strings.TrimSpace(os.Getenv(agentTerminalGraceEnv))
	switch strings.ToLower(v) {
	case "":
		return defaultAgentTerminalGrace
	case "0", "off", "false", "never", "no":
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		log.Printf("%s=%q is not a duration: using %s", agentTerminalGraceEnv, v, defaultAgentTerminalGrace)
		return defaultAgentTerminalGrace
	}
	return d
}

// finishedCloseTick is how often the node looks; a short grace period is looked at four times within it.
func finishedCloseTick(grace time.Duration) time.Duration {
	tick := time.Minute
	if q := grace / 4; q < tick {
		tick = max(q, 5*time.Second)
	}
	return tick
}

// finishedClock is when a finished Agent's terminal was first seen at rest with the screen it still shows.
type finishedClock struct {
	since  time.Time
	screen [32]byte
}

// finishedPane is a terminal that may be a finished Agent's: the pane as the last snapshot showed it.
type finishedPane struct {
	p  host.Agent
	h  host.Host
	id string // the session the mark names; "" or "agent" when the start did not know it yet
}

// agentTerminalOpened records pane as a terminal Persona opened for session id's Agent ("agent" when the start does
// not know it yet) on an engine that keeps no marks of its own (herdr's are personaTabToken): the in-memory ownership
// finished_close.go, keptPane and Close finished terminals need there (personaTabLocked).
func (n *Node) agentTerminalOpened(launch host.Launcher, pane host.PaneRef, id string) {
	if pane.ID == "" {
		return
	}
	if _, marks := launch.(host.MetadataReporter); marks {
		return
	}
	if id == "" {
		id = "agent"
	}
	n.mu.Lock()
	if n.agentTerms == nil {
		n.agentTerms = map[string]string{}
	}
	n.agentTerms[pane.String()] = id
	n.mu.Unlock()
}

// restingShell is a host that can say a pane holds its shell at rest and nothing else: the shell in front, no job
// behind it or stopped, no output since it looked (Orca: terminal.inspectProcess's idleShellProven). The grace-period
// close asks it instead of the programs in front, which say nothing of the jobs behind the shell (#17039).
type restingShell interface {
	PaneShellAtRest(ctx context.Context, pane host.PaneRef) (bool, error)
}

// closeFinishedLoop closes finished Agents' terminals after the grace period until ctx ends.
func (n *Node) closeFinishedLoop(ctx context.Context) {
	grace := agentTerminalGrace()
	if grace <= 0 || n.sessionScope != nil {
		return
	}
	t := time.NewTicker(finishedCloseTick(grace))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			n.closeFinishedRound(ctx, grace, time.Now())
		}
	}
}

// closeFinishedRound is one look: a fresh snapshot of every host, the clocks of the terminals at rest moved on, and
// the ones whose grace period is over closed. It returns the panes it closed.
func (n *Node) closeFinishedRound(ctx context.Context, grace time.Duration, now time.Time) []string {
	if grace <= 0 {
		return nil
	}
	start := time.Now()
	if err := n.refreshHosts(ctx); err != nil {
		return nil // no engine answered: nothing is closed this round
	}
	var closed []string
	seen := map[string]bool{}
	for _, c := range n.finishedPanes(start, "") {
		seen[c.p.PaneID] = true
		screen, ok := n.finishedAtRest(ctx, c)
		n.mu.Lock()
		if n.finishedSince == nil {
			n.finishedSince = map[string]finishedClock{}
		}
		clock, had := n.finishedSince[c.p.PaneID]
		switch {
		case !ok:
			delete(n.finishedSince, c.p.PaneID)
		case !had || clock.screen != screen:
			n.finishedSince[c.p.PaneID] = finishedClock{since: now, screen: screen}
		}
		n.mu.Unlock()
		if !ok || !had || clock.screen != screen || now.Sub(clock.since) < grace {
			continue
		}
		if n.closeFinishedPane(ctx, c, screen) {
			closed = append(closed, c.p.PaneID)
		}
	}
	n.mu.Lock()
	for pane := range n.finishedSince {
		if !seen[pane] {
			delete(n.finishedSince, pane)
		}
	}
	// Closed, or its engine restarted: a record never outlives its terminal.
	for pane := range n.agentTerms {
		if !n.inFreshLayout(pane, start) {
			delete(n.agentTerms, pane)
		}
	}
	for pane := range n.agentRoots {
		if !n.inFreshLayout(pane, start) {
			delete(n.agentRoots, pane)
		}
	}
	n.mu.Unlock()
	if len(closed) > 0 {
		_ = n.refreshHosts(ctx)
		n.notifySessions()
	}
	return closed
}

func (n *Node) finishedOrcaScreen(ctx context.Context, h *orcaHost, id, pane string, st sessionState) ([32]byte, bool) {
	t, _, err := n.orcaStoppedTarget(ctx, h, id, pane, st)
	if err != nil || t.Handle == "" {
		return [32]byte{}, false
	}
	screen, err := h.c.Read(ctx, t.Target())
	if err != nil {
		return [32]byte{}, false
	}
	return sha256.Sum256([]byte(t.IncarnationID + "\x00" + screen.Text())), true
}

// inFreshLayout: pane is in the snapshot its host gave at or after since; true when that host has not answered since
// (nothing is known about it then). n.mu held.
func (n *Node) inFreshLayout(pane string, since time.Time) bool {
	kind := host.ParsePane(pane).Kind
	l, ok := n.layouts[kind]
	if !ok || l.at.Before(since) {
		return true
	}
	for _, p := range l.panes {
		if p.PaneID == pane {
			return true
		}
	}
	return false
}

// finishedPanes are the terminals that may be finished Agents' in the snapshots taken at or after since (only pane,
// when given): owned by Persona, no Agent in them or bound to them, not kept for a retry, not being watched. The first
// shell of a worktree the assistant made for an Agent (personaWorktreeToken, #10624) is one too, once every other
// pane of its workspace is such a terminal (or another such shell): never while an Agent, or anything the user
// opened, is still in that workspace.
func (n *Node) finishedPanes(since time.Time, only string) []finishedPane {
	n.mu.Lock()
	defer n.mu.Unlock()
	inUse := map[string]bool{}
	for _, a := range n.agents {
		inUse[a.PaneID] = true
	}
	var out []finishedPane
	for _, h := range n.hosts.All() {
		switch h.Kind() {
		case host.Herdr, host.Orca:
		default:
			continue // other engines keep their terminals as before
		}
		l, ok := n.layouts[h.Kind()]
		if !ok || l.at.Before(since) {
			continue // this host did not answer this round
		}
		_, marks := h.(host.MetadataReporter)
		agentDone := map[string]finishedPane{} // pane -> a finished Agent terminal
		var roots []host.Agent
		for _, p := range l.panes {
			if p.PaneID == "" {
				continue
			}
			if c, ok := n.finishedAgentPaneLocked(h, p, inUse); ok {
				agentDone[p.PaneID] = c
			} else if n.worktreeShellLocked(p, marks, inUse) {
				roots = append(roots, p)
			}
		}
		for _, p := range l.panes {
			if c, ok := agentDone[p.PaneID]; ok && (only == "" || p.PaneID == only) {
				out = append(out, c)
			}
		}
		for _, r := range roots {
			if only != "" && r.PaneID != only {
				continue
			}
			alone := true
			for _, p := range l.panes {
				if p.PaneID == "" || p.PaneID == r.PaneID || p.WorkspaceID != r.WorkspaceID {
					continue
				}
				if _, done := agentDone[p.PaneID]; !done && !slices.ContainsFunc(roots, func(q host.Agent) bool { return q.PaneID == p.PaneID }) {
					alone = false // an Agent, a start, a viewer or the user's own pane is still here
					break
				}
			}
			if alone && r.WorkspaceID != "" {
				out = append(out, finishedPane{p: r, h: h})
			}
		}
	}
	return out
}

// finishedAgentPaneLocked: p is a terminal Persona opened for an Agent that has finished (finishedPanes). n.mu held.
func (n *Node) finishedAgentPaneLocked(h host.Host, p host.Agent, inUse map[string]bool) (finishedPane, bool) {
	if p.Agent != "" || inUse[p.PaneID] {
		return finishedPane{}, false
	}
	id := n.personaTabLocked(p)
	if id == "" {
		return finishedPane{}, false // not one Persona opened for an Agent
	}
	if bound := n.panes[p.PaneID]; bound != "" {
		if _, running := n.agents[bound]; running {
			return finishedPane{}, false
		}
		if id == "" || id == "agent" {
			id = bound
		}
	}
	if _, kept := n.keptStarts[p.PaneID]; kept || n.codexWaiting[p.PaneID] != nil {
		return finishedPane{}, false
	}
	if n.terms[panePrefix+p.PaneID] != nil || validID(id) && n.terms[id] != nil {
		return finishedPane{}, false // someone has it open
	}
	if validID(id) {
		if r := n.starts.get(id); r != nil && r.ended.IsZero() {
			return finishedPane{}, false
		}
	}
	return finishedPane{p: p, h: h, id: id}, true
}

// worktreeShellLocked: p is the first shell of an Agent's worktree (markWorktreeShell), with no Agent in it, not
// bound to a session and nobody watching it. n.mu held.
func (n *Node) worktreeShellLocked(p host.Agent, marks bool, inUse map[string]bool) bool {
	if p.Agent != "" || inUse[p.PaneID] || n.panes[p.PaneID] != "" || n.terms[panePrefix+p.PaneID] != nil {
		return false
	}
	if _, kept := n.keptStarts[p.PaneID]; kept || n.codexWaiting[p.PaneID] != nil {
		return false
	}
	if marks {
		return p.Tokens[personaWorktreeToken] != "" && p.Tokens[personaTabToken] == ""
	}
	return n.agentRoots[p.PaneID]
}

// finishedAtRest says whether c's pane holds only its shell, and hashes its screen; ok false when the engine cannot
// say (then it is not closed). An engine that can prove its shell at rest (restingShell, Orca) is asked that; else the
// programs in front must all be shells (herdr). "No agent process carries the pane" is not asked: a dev server, an
// editor or a CLI Persona does not know is none either (#17039).
func (n *Node) finishedAtRest(ctx context.Context, c finishedPane) (screen [32]byte, ok bool) {
	if h, ok := c.h.(*orcaHost); ok && validID(c.id) {
		if st, _ := n.state.get(c.id); st.OrcaTerminal != nil {
			return n.finishedOrcaScreen(ctx, h, c.id, c.p.PaneID, st)
		}
	}
	ref := n.hosts.Ref(c.p.PaneID)
	switch h := c.h.(type) {
	case restingShell:
		rest, err := h.PaneShellAtRest(ctx, ref)
		if err != nil || !rest {
			return screen, false
		}
	case host.ForegroundNamer:
		names, err := h.PaneForeground(ctx, ref)
		if err != nil || len(names) == 0 || !allShells(names) {
			return screen, false
		}
	default:
		return screen, false
	}
	text, err := c.h.Read(ctx, ref, host.ReadOpts{Source: "visible"})
	if err != nil {
		return screen, false
	}
	return sha256.Sum256([]byte(text)), true
}

// closeFinishedPane closes c's pane when, under its session's recovery lock (a Resume holds it while it picks the kept
// shell) and on a fresh snapshot of its host, it is still a finished Agent's terminal showing screen. A lock held now
// or a failed close leaves it for a later round.
func (n *Node) closeFinishedPane(ctx context.Context, c finishedPane, screen [32]byte) bool {
	if validID(c.id) {
		release, ok := n.recovery.tryLock(c.id)
		if !ok {
			return false
		}
		defer release()
	}
	st, _ := n.state.get(c.id)
	if h, ok := c.h.(*orcaHost); ok && validID(c.id) && st.OrcaTerminal != nil {
		unlock, ok := n.typing.tryLock(c.id)
		if !ok {
			return false
		}
		defer unlock()
		closed, err := n.closeStoppedOrca(ctx, h, c.id, c.p.PaneID, st, func() error {
			if current, ok := n.finishedOrcaScreen(ctx, h, c.id, c.p.PaneID, st); !ok || current != screen {
				return orcaTerminalRefusal()
			}
			return nil
		})
		if err != nil {
			log.Printf("finished terminal %s (session %s): close failed, trying again later: %v", c.p.PaneID, c.id, err)
		}
		return closed
	}
	org, ok := c.h.(host.Organizer)
	if !ok {
		return false
	}
	start := time.Now()
	if err := n.refreshHosts(ctx, c.h); err != nil {
		return false
	}
	again := n.finishedPanes(start, c.p.PaneID)
	if len(again) != 1 {
		return false // an Agent, a viewer or a start came in the meantime, or the pane is gone
	}
	now, ok := n.finishedAtRest(ctx, again[0])
	if !ok || now != screen {
		return false
	}
	p := again[0].p
	if err := org.Close(ctx, n.hosts.Ref(p.PaneID), p.WorkspaceID, p.TabID); err != nil {
		log.Printf("finished terminal %s (session %s): close failed, trying again later: %v", p.PaneID, c.id, err)
		return false
	}
	n.mu.Lock()
	delete(n.finishedSince, p.PaneID)
	delete(n.agentTerms, p.PaneID)
	delete(n.agentRoots, p.PaneID)
	n.mu.Unlock()
	log.Printf("finished terminal %s (session %s): closed after its grace period", p.PaneID, c.id)
	return true
}
