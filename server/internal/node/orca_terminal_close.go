package node

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/orcart"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	orcaHandleToken      = "orca_terminal_handle"
	orcaIncarnationToken = "orca_terminal_incarnation"
)

// orcaTerminalOwner ties the session to the original terminal.create receipt
// and birth of the local PTY's root process. The shell environment alone
// cannot identify the terminal because other processes can inherit it.
type orcaTerminalOwner struct {
	Pane        string `json:"pane"`
	Workspace   string `json:"workspace"`
	Handle      string `json:"handle"`
	Incarnation string `json:"incarnation"`
	RootPID     int    `json:"root_pid"`
	RootStart   string `json:"root_start"`
	TaskID      string `json:"task_id,omitempty"` // Set only when this task created the terminal.
	TaskToken   string `json:"task_token,omitempty"`
}

type orcaStartTerminal struct {
	id string // empty until a runtime that assigns its own session id is bound
	orcaTerminalOwner
}

// markOrcaTerminal runs only after newTab created a terminal. An adopted pane
// and a retry in a kept pane never mint a new ownership record or task receipt.
func (h *orcaHost) markOrcaTerminal(ctx context.Context, pane host.PaneRef, id string) {
	h.mu.Lock()
	t := h.made[pane.ID]
	h.mu.Unlock()
	if t.Handle == "" || t.IncarnationID == "" || t.WorktreeID == "" || !t.AcceptedCreateEnv || t.ProcessID <= 0 {
		return
	}
	start := procStartMark(t.ProcessID)
	if start == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.made[pane.ID] != t {
		return
	}
	owner := orcaTerminalOwner{Pane: pane.String(), Workspace: t.WorktreeID, Handle: t.Handle,
		Incarnation: t.IncarnationID, RootPID: t.ProcessID, RootStart: start}
	if ts := taskSpaceIn(ctx, h); ts != nil {
		owner.TaskID = ts.task
	}
	if task, _ := ctx.Value(taskTerminalStartKey{}).(*taskTerminalStart); task != nil {
		owner.TaskToken = task.token
		task.pane = pane.String()
	}
	if h.agentTabs == nil {
		h.agentTabs = map[string]orcaStartTerminal{}
	}
	h.agentTabs[pane.ID] = orcaStartTerminal{id: id, orcaTerminalOwner: owner}
}

func (h *orcaHost) claimTerminalOwner(pane, id string) *orcaTerminalOwner {
	if !validID(id) {
		return nil
	}
	ref := host.ParsePane(pane)
	if ref.Kind != host.Orca {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	start, ok := h.agentTabs[ref.ID]
	if !ok || start.id != "" && start.id != id {
		return nil
	}
	start.id = id
	h.agentTabs[ref.ID] = start
	return &start.orcaTerminalOwner
}

// orcaFinishedOwnerLocked checks the saved receipt against the snapshot. Use
// fallback only for upstream's live marks that have no saved receipt. n.mu held.
func (n *Node) orcaFinishedOwnerLocked(p host.Agent, id, fallback string) string {
	if !validID(id) {
		return fallback
	}
	st, _ := n.state.get(id)
	o := st.OrcaTerminal
	if o == nil {
		return fallback
	}
	if st.Pane != p.PaneID || o.Pane != p.PaneID || o.Workspace != p.WorkspaceID ||
		o.Handle == "" || o.Incarnation == "" || o.Handle != p.Tokens[orcaHandleToken] || o.Incarnation != p.Tokens[orcaIncarnationToken] {
		return ""
	}
	return id
}

func orcaTerminalRefusal() error { return terminalChangedRefusal() }

// terminalChangedRefusal refuses a close of a stopped Agent whose terminal changed or is in use (Orca's or herdr's,
// close_after_exit.go): agent_busy, which the hub words by its reason (apierr's reasonKeys, #17057); the words here stay
// English, as they were for a hub that does not.
func terminalChangedRefusal() error {
	return refusal("agent_busy", i18n.T(i18n.En, "rpc-errors.agent_busy_terminal_changed"), map[string]any{"reason": "terminal_changed"})
}

// orcaCloseTarget uses a direct inventory. A missing key is idempotent only
// when the inventory is complete and the old handle did not move elsewhere.
func (h *orcaHost) orcaCloseTarget(ctx context.Context, pane string, owner *orcaTerminalOwner, task bool) (orcart.Terminal, error) {
	list, err := h.c.CloseInventory(ctx, task)
	if err != nil {
		return orcart.Terminal{}, orcaErr(err)
	}
	if list.InventoryIncomplete || owner == nil || owner.Pane != pane || owner.Handle == "" || owner.Incarnation == "" {
		return orcart.Terminal{}, orcaTerminalRefusal()
	}
	key := host.ParsePane(pane).ID
	var found orcart.Terminal
	for _, t := range list.Terminals {
		if t.PaneKey() != key && t.Handle != owner.Handle {
			continue
		}
		if found.Handle != "" || t.Handle != owner.Handle || t.IncarnationID != owner.Incarnation ||
			t.WorktreeID != owner.Workspace || !t.Local() || !t.Connected || t.Verdict == "unverifiable" {
			return orcart.Terminal{}, orcaTerminalRefusal()
		}
		if t.Orphaned {
			// An orphan has no tab layout to authorize task acceptance. For a
			// stopped shell, follow only a re-key of the terminal this host made,
			// not a user-moved pane or an unproved orphan after reconnect.
			if task || t.PaneKey() == key || t.TabID == "" || t.LeafID == "" || !h.originalOrcaPlacement(key, t) {
				return orcart.Terminal{}, orcaTerminalRefusal()
			}
		} else if t.PaneKey() != key {
			return orcart.Terminal{}, orcaTerminalRefusal()
		}
		found = t
	}
	if task && found.Handle != "" && !orcaTaskTabInBackground(list, found) {
		return orcart.Terminal{}, orcaTerminalRefusal()
	}
	return found, nil
}

// originalOrcaPlacement is the live creation record for the old pane key.
// A close additionally compares the same identity with the durable session record.
func (h *orcaHost) originalOrcaPlacement(key string, current orcart.Terminal) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	made, ok := h.made[key]
	return ok && made.Handle == current.Handle && made.IncarnationID == current.IncarnationID &&
		made.WorktreeID == current.WorktreeID
}

func orcaTaskTabInBackground(list *orcart.TerminalList, terminal orcart.Terminal) bool {
	var target, neighbors int
	for _, tab := range list.Tabs {
		if tab.WorktreeID != terminal.WorktreeID {
			continue
		}
		if tab.ID != terminal.TabID {
			neighbors++
			continue
		}
		if !tab.FocusKnown || tab.Active || tab.PaneCount != 1 {
			return false
		}
		target++
	}
	if target != 1 || neighbors == 0 {
		return false
	}
	panes := 0
	for _, t := range list.Terminals {
		if t.TabID == terminal.TabID {
			if t.WorktreeID != terminal.WorktreeID || t.Orphaned {
				return false
			}
			panes++
		}
	}
	return panes == 1
}

// stoppedOrcaShell checks local rebinding after the engine and process reads.
// The caller holds the session's recovery lock when closing.
func (n *Node) stoppedOrcaShell(id, pane string, owner *orcaTerminalOwner) bool {
	st, ok := n.state.get(id)
	if !ok || st.Pane != pane || st.OrcaTerminal == nil || *st.OrcaTerminal != *owner ||
		st.PendingSwitch != nil || n.restarting(id) || n.isAssistantSession(id) {
		return false
	}
	if r := n.starts.get(id); r != nil && r.ended.IsZero() {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for sid, a := range n.agents {
		if sid == id || a.PaneID == pane {
			return false
		}
	}
	_, kept := n.keptStarts[pane]
	return (n.panes[pane] == "" || n.panes[pane] == id) && !kept && n.codexWaiting[pane] == nil &&
		!n.driving[id] && n.terms[id] == nil && n.terms[panePrefix+pane] == nil
}

func (n *Node) orcaStoppedTarget(ctx context.Context, h *orcaHost, id, pane string, st sessionState) (orcart.Terminal, orcaShell, error) {
	if st.OrcaTerminal == nil || !n.stoppedOrcaShell(id, pane, st.OrcaTerminal) {
		return orcart.Terminal{}, orcaShell{}, orcaTerminalRefusal()
	}
	t, err := h.orcaCloseTarget(ctx, pane, st.OrcaTerminal, false)
	if err != nil || t.Handle == "" {
		return t, orcaShell{}, err
	}
	// Read the exact incarnation: unlike terminal display, a close must
	// never retry inspection through a replacement pane handle.
	process, err := h.c.InspectProcess(ctx, t.Target())
	if err != nil || !orcaIdleShell(process) {
		return orcart.Terminal{}, orcaShell{}, orcaTerminalRefusal()
	}
	shell, ok := h.inspectCloseShell(ctx, *st.OrcaTerminal, 0)
	if !ok || !orcaShellPIDMatches(process, shell) || st.Cwd != "" && (shell.cwd == "" || filepath.Clean(shell.cwd) != filepath.Clean(st.Cwd)) ||
		!n.stoppedOrcaShell(id, pane, st.OrcaTerminal) {
		return orcart.Terminal{}, orcaShell{}, orcaTerminalRefusal()
	}
	return t, shell, nil
}

// closeStoppedOrca rechecks the original shell and placement after authorization.
// Use terminal.close (one pane), so a newly split sibling cannot be closed too.
func (n *Node) closeStoppedOrca(ctx context.Context, h *orcaHost, id, pane string, st sessionState, before func() error) (bool, error) {
	t, shell, err := n.orcaStoppedTarget(ctx, h, id, pane, st)
	if err != nil || t.Handle == "" {
		return false, err
	}
	if before != nil {
		if err := before(); err != nil {
			return false, err
		}
	}
	after, current, err := n.orcaStoppedTarget(ctx, h, id, pane, st)
	if err != nil || after.Handle == "" {
		return false, err
	}
	if shell != current || n.hosts.Get(host.Orca) != h {
		return false, orcaTerminalRefusal()
	}
	err = h.c.CloseVerified(ctx, after.Target())
	return err == nil, orcaErr(err)
}

type orcaTaskProcess struct {
	terminal orcart.Terminal
	owner    orcaTerminalOwner
	agent    host.Agent
	pid      int
	start    string
}

func (n *Node) orcaTaskAgent(ctx context.Context, h *orcaHost, target devicewire.TaskTerminal, closing bool) orcaTaskProcess {
	st, ok := n.state.get(target.SessionID)
	o := st.OrcaTerminal
	if !ok || o == nil || st.Pane != target.PaneID || o.TaskToken != target.Token {
		return orcaTaskProcess{}
	}
	t, err := h.orcaCloseTarget(ctx, target.PaneID, o, closing)
	if err != nil || t.Handle == "" || t.TabID != target.TabID || t.WorktreeID != target.WorkspaceID {
		return orcaTaskProcess{}
	}
	procs, err := h.scan()
	ps := procs[t.PaneKey()]
	if err != nil || len(ps) != 1 || ps[0].pid <= 0 {
		return orcaTaskProcess{}
	}
	p := ps[0]
	a, live := n.agent(target.SessionID)
	if !live || a.PaneID != target.PaneID || a.Agent != p.agent || a.Attention != "" ||
		(a.Agent != "claude" && a.Agent != "codex") || a.Agent == "claude" && n.sessionOfPID(target.PaneID, p.pid) != target.SessionID {
		return orcaTaskProcess{}
	}
	return orcaTaskProcess{terminal: t, owner: *o, agent: a, pid: p.pid, start: procStartMark(p.pid)}
}

func (n *Node) closeOrcaTaskTerminal(ctx context.Context, h *orcaHost, target devicewire.TaskTerminal, authorize func() bool) string {
	const skipped = devicewire.TaskTerminalSkipped
	first := n.orcaTaskAgent(ctx, h, target, true)
	if first.pid != target.PID || first.start != target.Process || !n.taskTerminalAtRest(ctx, target, first.agent) {
		return skipped
	}
	original, ok := h.inspectOrcaTaskProcess(ctx, first)
	if !ok || !authorize() {
		return skipped
	}
	// Recheck work, live bindings and the direct layout after authorization.
	if err := n.refreshHosts(ctx, h); err != nil || !n.taskTerminalAtRest(ctx, target, first.agent) {
		return skipped
	}
	last := n.orcaTaskAgent(ctx, h, target, true)
	if last.pid != target.PID || last.start != target.Process || last.owner != first.owner {
		return skipped
	}
	current, ok := h.inspectOrcaTaskProcess(ctx, last)
	if !ok || current != original || procStartMark(last.pid) != last.start ||
		n.hosts.Get(host.Orca) != h || !n.taskTerminalLocallyIdle(target, last.agent) {
		return skipped
	}
	if err := h.c.CloseVerified(ctx, last.terminal.Target()); err != nil {
		var remote *orcart.Error
		if errors.As(err, &remote) {
			return skipped
		}
		return devicewire.TaskTerminalPending
	}
	n.state.stopped(target.SessionID, proto.StopTaskCompleted)
	n.notifySessions()
	return devicewire.TaskTerminalClosed
}

// A live task cannot have an idle shell in front. Require Orca to identify
// the expected foreground Agent, then verify its process tree independently.
func (h *orcaHost) inspectOrcaTaskProcess(ctx context.Context, task orcaTaskProcess) (orcaShell, bool) {
	p, err := h.c.InspectProcess(ctx, task.terminal.Target())
	if err != nil || !p.Known() || p.IdleShell() || orcaAgentOfCmd([]string{p.Foreground()}) != task.agent.Agent {
		return orcaShell{}, false
	}
	shell, ok := h.inspectCloseShell(ctx, task.owner, task.pid)
	return shell, ok && orcaShellPIDMatches(p, shell)
}

func orcaShellPIDMatches(p *orcart.Process, shell orcaShell) bool {
	return p.ShellPID() > 0 && p.ShellPID() == shell.pid
}

// orcaShell is the shell process that started in the original terminal. The
// process birth mark detects a recycled pid between the two inspections.
type orcaShell struct {
	pid   int
	start string
	cwd   string
}

func inspectOrcaCloseShell(ctx context.Context, owner orcaTerminalOwner, agentPID int) (orcaShell, bool) {
	if owner.RootPID <= 0 || owner.RootStart == "" || procStartMark(owner.RootPID) != owner.RootStart {
		return orcaShell{}, false
	}
	procs, err := listProcs(ctx)
	if err != nil {
		return orcaShell{}, false
	}
	shell := orcaRootShell(procs, owner.RootPID)
	if shell == 0 || !orcaCloseProcessTree(procs, shell, agentPID) || !orcaShellMatchesTerminal(shell, host.ParsePane(owner.Pane).ID) {
		return orcaShell{}, false
	}
	foreground := shell
	if agentPID != 0 {
		foreground = agentPID
	}
	if !orcaCloseForeground(foreground) {
		return orcaShell{}, false
	}
	current := orcaShell{pid: shell, start: procStartMark(shell)}
	if agentPID == 0 {
		current.cwd = orcaCloseCwd(ctx, shell)
	}
	return current, current.start != "" && procStartMark(shell) == current.start && procStartMark(owner.RootPID) == owner.RootStart
}

// Orca starts a login process on macOS. Its shell is the only direct child
// sharing its tty. Linux can start the shell as the root process itself.
func orcaRootShell(procs map[int]proc, root int) int {
	p := procs[root]
	if p.tty == "" {
		return 0
	}
	if shellProgram(p.cmd) {
		return root
	}
	if p.cmd != "login" {
		return 0
	}
	var shell int
	for pid, child := range procs {
		if child.ppid == root && child.tty == p.tty {
			if shell != 0 || !shellProgram(child.cmd) {
				return 0
			}
			shell = pid
		}
	}
	return shell
}

func orcaCloseProcessTree(procs map[int]proc, shell, agent int) bool {
	allowed := map[int]bool{shell: true}
	if parent := procs[shell].ppid; procs[parent].cmd == "login" && procs[parent].tty == procs[shell].tty {
		allowed[parent] = true
	}
	if agent != 0 {
		tty := procs[shell].tty
		for pid := agent; pid != shell; pid = procs[pid].ppid {
			if pid <= 0 || procs[pid].tty != tty || allowed[pid] {
				return false
			}
			allowed[pid] = true
		}
	}
	for pid := range owned([]int{shell}, procs) {
		if !allowed[pid] {
			return false
		}
	}
	return true
}

func orcaCloseEnvMatches(env, pane string) bool {
	var agent, key string
	for entry := range strings.SplitSeq(env, "\x00") {
		name, value, _ := strings.Cut(entry, "=")
		switch name {
		case "PERSONA_AGENT":
			agent = value
		case orcaPaneEnv:
			key = value
		}
	}
	return agent == "1" && key == pane
}

// closeOrcaTaskSpace retires the task's stopped terminals individually. The
// worktree itself can belong to the user or contain several tasks' terminals.
func (n *Node) closeOrcaTaskSpace(ctx context.Context, task, workspace string) (map[string]any, error) {
	if task == "" || workspace == "" {
		return nil, errors.New("task_id and workspace_id are required")
	}
	h, ok := n.hosts.Get(host.Orca).(*orcaHost)
	if !ok {
		return nil, errors.New("Orca is unavailable")
	}
	n.state.mu.Lock()
	members := map[string]bool{}
	targets := map[string]sessionState{}
	for id, st := range n.state.Sessions {
		if st.WorkspaceHost != host.Orca || st.TaskID != task || st.WorkspaceID != workspace {
			continue
		}
		members[st.Pane] = true
		if o := st.OrcaTerminal; o != nil && o.TaskID == task && o.Workspace == workspace && o.Pane == st.Pane {
			targets[id] = st
		}
	}
	n.state.mu.Unlock()
	if len(members) == 0 {
		return nil, refusal(notTaskSpace, "the node has no record of this task in the Orca workspace", nil)
	}
	// The manager's list and the last display scan can lag a resume.
	if _, err := h.agentProcs(0); err != nil {
		return nil, err
	}
	if err := n.refreshHosts(ctx, h); err != nil {
		return nil, err
	}
	n.mu.Lock()
	for _, a := range n.agents {
		if a.PaneID != "" && members[a.PaneID] {
			n.mu.Unlock()
			return nil, refusal(taskSpaceBusyCode, "an Agent in this Orca task is still running", nil)
		}
	}
	n.mu.Unlock()
	var closed bool
	var pending error
	for id, st := range targets {
		ok, err := n.closeStoppedOrcaTaskTab(ctx, h, id, st)
		closed = closed || ok
		pending = errors.Join(pending, err)
	}
	if closed {
		n.notifySessions()
	}
	return map[string]any{"closed": closed}, pending
}

func (n *Node) closeStoppedOrcaTaskTab(ctx context.Context, h *orcaHost, id string, st sessionState) (bool, error) {
	unlock, ok := n.recovery.tryLock(id)
	if !ok {
		return false, orcaTerminalRefusal()
	}
	defer unlock()
	unlockTyping, ok := n.typing.tryLock(id)
	if !ok {
		return false, orcaTerminalRefusal()
	}
	defer unlockTyping()
	check := func() error {
		current, ok := n.state.get(id)
		if !ok || current.TaskID != st.TaskID || current.WorkspaceHost != host.Orca || current.WorkspaceID != st.WorkspaceID {
			return orcaTerminalRefusal()
		}
		return nil
	}
	return n.closeStoppedOrca(ctx, h, id, st.Pane, st, check)
}
