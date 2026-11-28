package node

import (
	"context"
	"errors"
	"os"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/herdr"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

const taskTerminalToken = "persona_task_terminal"

type taskTerminalStartKey struct{}

type taskTerminalStart struct {
	token, pane string
}

// markTaskTerminal runs only for a tab created by this start. Adopted panes
// and failed starts reused by a retry never get this marker.
func markTaskTerminal(ctx context.Context, launch host.Launcher, pane host.PaneRef) {
	mark, _ := ctx.Value(taskTerminalStartKey{}).(*taskTerminalStart)
	h, ok := launch.(*herdrHost)
	if mark == nil || !ok || pane.ID == "" {
		return
	}
	if h.ReportMetadata(ctx, pane, "persona_task", map[string]*string{taskTerminalToken: &mark.token}) == nil {
		mark.pane = pane.String()
	}
}

func (n *Node) taskTerminalReceipt(ctx context.Context, s *proto.Session, mark taskTerminalStart) *devicewire.TaskTerminal {
	if s.Starting || s.PaneID != mark.pane || s.TabID == "" || s.WorkspaceID == "" ||
		(s.Agent != "claude" && s.Agent != "codex") {
		return nil
	}
	target := devicewire.TaskTerminal{SessionID: s.SessionID, PaneID: s.PaneID, TabID: s.TabID, WorkspaceID: s.WorkspaceID, Token: mark.token}
	if host.ParsePane(target.PaneID).Kind == host.Orca {
		h, ok := n.hosts.Get(host.Orca).(*orcaHost)
		if !ok {
			return nil
		}
		process := n.orcaTaskAgent(ctx, h, target, false)
		if process.start == "" {
			return nil
		}
		target.PID, target.Process = process.pid, process.start
		return &target
	}
	route, ok := n.taskTerminalRoute(ctx, target)
	if !ok {
		return nil
	}
	target.PID, target.Process = taskTerminalProcess(ctx, route)
	if target.Process == "" {
		return nil
	}
	return &target
}

func (n *Node) taskTerminalRoute(ctx context.Context, target devicewire.TaskTerminal) (herdrRoute, bool) {
	h, ref, err := n.paneHost(target.PaneID)
	hh, ok := h.(*herdrHost)
	if err != nil || !ok {
		return herdrRoute{}, false
	}
	route, err := hh.target(ctx, ref.ID)
	return route, err == nil
}

func taskTerminalProcess(ctx context.Context, route herdrRoute) (int, string) {
	procs, err := route.c.PaneForeground(ctx, route.id)
	if err != nil || len(procs) != 1 || procs[0].PID <= 0 {
		return 0, ""
	}
	return procs[0].PID, procStartMark(procs[0].PID)
}

// closeTaskTerminal closes a single marked pane once the same process, tab and
// workspace are verified. Holding the session's locks excludes Persona sends,
// resumes and launcher switches while the node checks and closes the pane.
func (n *Node) closeTaskTerminal(ctx context.Context, target devicewire.TaskTerminal, authorize func() bool) string {
	const skipped = devicewire.TaskTerminalSkipped
	id := target.SessionID
	if !validID(id) || !validID(target.Token) || target.PID <= 0 || target.Process == "" {
		return skipped
	}
	release, ok := n.recovery.tryLock(id)
	if !ok {
		return skipped
	}
	defer release()
	unlock, ok := n.typing.tryLock(id)
	if !ok {
		return skipped
	}
	defer unlock()
	if host.ParsePane(target.PaneID).Kind == host.Orca {
		if h, ok := n.hosts.Get(host.Orca).(*orcaHost); ok {
			return n.closeOrcaTaskTerminal(ctx, h, target, authorize)
		}
		return skipped
	}
	route, ok := n.taskTerminalRoute(ctx, target)
	if !ok {
		return skipped
	}
	agent, ok := taskTerminalAgent(ctx, route, target)
	if !ok || !n.taskTerminalAtRest(ctx, target, agent) {
		return skipped
	}
	pid, process := taskTerminalProcess(ctx, route)
	if pid != target.PID || process != target.Process || !taskTerminalProcessesIdle(ctx, pid) {
		return skipped
	}
	attempted, err := route.c.PaneCloseIf(ctx, route.id, func() bool {
		if !n.taskTerminalAtRest(ctx, target, agent) || !authorize() {
			return false
		}
		agent, ok := taskTerminalAgent(ctx, route, target)
		return ok && procStartMark(target.PID) == target.Process && n.taskTerminalLocallyIdle(target, agent)
	})
	if !attempted {
		return skipped
	}
	if err != nil {
		var refused *herdr.Error
		if errors.As(err, &refused) {
			return skipped
		}
		return devicewire.TaskTerminalPending
	}
	n.state.stopped(id, proto.StopTaskCompleted)
	n.notifySessions()
	return devicewire.TaskTerminalClosed
}

// taskTerminalAgent reads the selected server directly. Closing the workspace's
// last tab could remove the workspace, so even an exclusive tab needs a neighbor.
func taskTerminalAgent(ctx context.Context, route herdrRoute, target devicewire.TaskTerminal) (host.Agent, bool) {
	snap, err := route.c.Snapshot(ctx)
	if err != nil || snap.InventoryIncomplete || snap.FocusedPaneID == "" || snap.FocusedTabID == "" ||
		snap.FocusedPaneID == route.id || route.qualify(snap.FocusedTabID) == target.TabID {
		return host.Agent{}, false
	}
	var panes, otherTabs int
	for _, p := range snap.Panes {
		if p.PaneID == "" || p.TabID == "" || p.WorkspaceID == "" {
			return host.Agent{}, false
		}
		if route.qualify(p.TabID) == target.TabID && route.qualify(p.WorkspaceID) != target.WorkspaceID {
			return host.Agent{}, false
		}
		if route.qualify(p.WorkspaceID) != target.WorkspaceID {
			continue
		}
		if p.TabID != "" && route.qualify(p.TabID) != target.TabID {
			otherTabs++
			continue
		}
		if p.PaneID != route.id || route.qualify(p.TabID) != target.TabID ||
			p.Tokens[taskTerminalToken] != target.Token || p.Tokens[personaTabToken] != target.SessionID {
			return host.Agent{}, false
		}
		panes++
	}
	if panes != 1 || otherTabs == 0 {
		return host.Agent{}, false
	}
	var matched host.Agent
	for _, a := range fromHerdrAgents(route.session, snap.Agents) {
		if a.PaneID != target.PaneID && a.SessionID() != target.SessionID {
			continue
		}
		if matched.PaneID != "" || a.PaneID != target.PaneID || a.SessionID() != target.SessionID ||
			a.TabID != target.TabID || a.WorkspaceID != target.WorkspaceID ||
			(a.Agent != "claude" && a.Agent != "codex") || a.Attention != "" {
			return host.Agent{}, false
		}
		matched = a
	}
	return matched, matched.AgentStatus == proto.StatusDone || matched.AgentStatus == proto.StatusIdle
}

func (n *Node) taskTerminalAtRest(ctx context.Context, target devicewire.TaskTerminal, agent host.Agent) bool {
	id, pane := target.SessionID, target.PaneID
	if !n.taskTerminalLocallyIdle(target, agent) {
		return false
	}
	p := n.transcriptPath(id)
	fi, err := os.Stat(p)
	if err != nil {
		return false
	}
	m := n.meta(p, fi)
	if m.bgWait || len(m.queue) > 0 || len(m.bg) > 0 || m.loop != nil ||
		slices.ContainsFunc(n.subagents(id), func(a proto.Subagent) bool { return a.Running }) ||
		slices.ContainsFunc(n.workflows(id, m.wf, m.wfScript), func(w proto.Workflow) bool { return w.Running }) ||
		len(n.portsOf(pane)) > 0 {
		return false
	}
	if agent.Agent == "codex" {
		v, err := n.codexScreen(ctx, pane)
		return err == nil && v.View == screen.CodexComposer && v.Draft == "" && len(v.Queued) == 0
	}
	text, err := n.dialogScreen(ctx, pane)
	if err != nil || screen.DialogHead(text) != "" || offersSendNow(text) || n.allowDialog(id) != "" {
		return false
	}
	draft, err := n.promptHasDraft(ctx, pane)
	return err == nil && !draft
}

func (n *Node) taskTerminalLocallyIdle(target devicewire.TaskTerminal, agent host.Agent) bool {
	id, pane := target.SessionID, target.PaneID
	st, mine := n.state.get(id)
	status := n.liveStatus(id, agent, time.Now())
	if !mine || st.Pane != pane || st.PendingSwitch != nil || st.StopCause != proto.StopUnknown ||
		(status != proto.StatusDone && status != proto.StatusIdle) || n.restarting(id) || n.isAssistantSession(id) {
		return false
	}
	if r := n.starts.get(id); r != nil && r.ended.IsZero() {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if current, ok := n.agents[id]; ok && current.PaneID != pane {
		return false
	}
	for sid, a := range n.agents {
		if sid != id && a.PaneID == pane {
			return false
		}
	}
	_, kept := n.keptStarts[pane]
	return !n.driving[id] && n.panes[pane] == id && !kept && n.codexWaiting[pane] == nil &&
		n.terms[id] == nil && n.terms[panePrefix+pane] == nil
}

// taskTerminalProcessesIdle treats unidentified processes sharing the terminal
// or descending from the agent as live work. Its launcher and shell are ancestors.
func taskTerminalProcessesIdle(ctx context.Context, pid int) bool {
	procs, err := listProcs(ctx)
	if err != nil {
		return false
	}
	if _, ok := procs[pid]; !ok {
		return false
	}
	ancestors := map[int]bool{pid: true}
	for parent := procs[pid].ppid; parent > 0 && !ancestors[parent]; parent = procs[parent].ppid {
		ancestors[parent] = true
	}
	for member := range owned([]int{pid}, procs) {
		if !ancestors[member] {
			return false
		}
	}
	return true
}
