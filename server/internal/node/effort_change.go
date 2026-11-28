package node

import (
	"context"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// effortChange keeps a busy Claude Code's requested effort separate from its confirmed effort.
// Clients that can retain a refused send can wait for the confirmed choice.
type effortChange struct {
	target string
	pane   string
	failed bool
}

type terminalEffort struct {
	pane  string
	value string
	at    time.Time
}

func (s *switchShare) effortChange(id string) effortChange {
	change, _ := s.terminalEfforts(id)
	return change
}

func (s *switchShare) terminalEfforts(id string) (effortChange, terminalEffort) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.efforts[id], s.confirmed[id]
}

func (s *switchShare) setEffortChange(id string, change effortChange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if change.target == "" {
		delete(s.efforts, id)
		return
	}
	if s.efforts == nil {
		s.efforts = make(map[string]effortChange)
	}
	s.efforts[id] = change
}

// The confirming picker can finish on a disconnected Node. Store its result and lift the barrier together so
// every subsequent connection can list the confirmed value, even if no Node was serving when the picker finished.
func (s *switchShare) confirmTerminalEffort(id, pane, effort string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.confirmed == nil {
		s.confirmed = make(map[string]terminalEffort)
	}
	s.confirmed[id] = terminalEffort{pane: pane, value: effort, at: time.Now()}
	delete(s.efforts, id)
}

func (c effortChange) refusal() error {
	return refusal(proto.CodeEffortChangePending, i18n.T(i18n.Default, "rpc-errors.effort_change_pending"),
		map[string]any{"effort": c.target})
}

// setTerminalEffort runs under the input locks also used by the queued worker.
func (n *Node) setTerminalEffort(ctx context.Context, id, pane, effort string, pending *bool) error {
	a, live := n.agent(id)
	if live && a.Agent == "claude" && pending != nil {
		h, _, err := n.paneHost(pane)
		if err != nil {
			return err
		}
		if err := n.refreshHosts(ctx, h); err != nil {
			return err
		}
		a, live = n.agent(id)
		if !live || a.Agent != "claude" || a.PaneID != pane {
			return n.noPaneForModes(id)
		}
		share := n.switches()
		if change := share.effortChange(id); change.target != "" {
			if _, current := n.modesOf(id); current == effort {
				share.setEffortChange(id, effortChange{})
				return nil
			}
		}
		if a.AgentStatus != proto.StatusIdle && a.AgentStatus != proto.StatusDone {
			share.setEffortChange(id, effortChange{target: effort, pane: pane})
			*pending = true
			return nil
		}
	} else if live && (a.AgentStatus == proto.StatusWorking || a.AgentStatus == proto.StatusBlocked) {
		return fmt.Errorf("Agent 正在工作，这一轮结束后再改思考强度")
	}

	if err := n.driveTerminalEffort(ctx, id, pane, effort); err != nil {
		return err
	}
	share := n.switches()
	if live && a.Agent == "claude" && a.PaneID == pane {
		share.confirmTerminalEffort(id, pane, effort)
		if serving := share.serving(); serving != nil && serving != n {
			serving.notifySessionsSoon()
		}
	} else {
		n.noteModes(id, "", effort)
		share.setEffortChange(id, effortChange{})
	}
	return nil
}

// Read pending and confirmed terminal effort from the same snapshot.
func (n *Node) terminalModesRow(s *proto.Session, m quickMeta) {
	change, confirmed := n.switches().terminalEfforts(s.SessionID)
	if change.target != "" {
		s.PendingEffort = &change.target
		s.EffortChangeFailed = change.failed
	}
	if s.Agent != "claude" || s.PaneID != confirmed.pane {
		confirmed = terminalEffort{}
	}
	s.PermissionMode, s.Effort = n.currentModesWithEffort(s.SessionID, m, confirmed.value, confirmed.at)
}

// Held sends use the cached pane when present, and verify a disappearance before lifting the barrier.
func (n *Node) effortPaneStillLive(ctx context.Context, id, pane string) bool {
	if a, ok := n.agent(id); ok && a.Agent == "claude" && a.PaneID == pane {
		return true
	}
	h, _, err := n.paneHost(pane)
	if err != nil {
		return false
	}
	if err := n.refreshHosts(ctx, h); err != nil {
		return true // the host could not confirm that the pane went away
	}
	a, live := n.agent(id)
	return live && a.Agent == "claude" && a.PaneID == pane
}

func (n *Node) wakeEffortChange(s *proto.Session) {
	if s.PendingEffort != nil && !s.Starting && (s.PaneID == "" ||
		!s.EffortChangeFailed && (s.Status == proto.StatusIdle || s.Status == proto.StatusDone)) {
		go n.tryEffortChange(s.SessionID)
	}
}

func (n *Node) tryEffortChange(id string) {
	unlock, ok := n.typing.tryLock(id)
	if !ok {
		return
	}
	defer unlock()
	share := n.switches()
	unlockEffort, ok := share.modesInput.tryLock(id)
	if !ok {
		return
	}
	defer unlockEffort()
	change := share.effortChange(id)
	if change.target == "" || share.inFlight(id) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), switchProbeWait)
	defer cancel()
	h, _, err := n.paneHost(change.pane)
	if err != nil {
		share.setEffortChange(id, effortChange{})
		n.notifyEffortChange()
		return
	}
	if err := n.refreshHosts(ctx, h); err != nil {
		return // a failed refresh is not evidence that the turn ended
	}
	a, live := n.agent(id)
	switch {
	case !live || a.PaneID != change.pane || a.Agent != "claude":
		share.setEffortChange(id, effortChange{})
		n.notifyEffortChange()
		return
	case change.failed || a.AgentStatus != proto.StatusIdle && a.AgentStatus != proto.StatusDone:
		return
	case n.checkPaneFor(ctx, change.pane, "claude", sendOpts{readBox: true}) != nil:
		err = fmt.Errorf("Claude Code's prompt is not available")
	default:
		err = n.driveTerminalEffort(ctx, id, change.pane, change.target)
	}
	if err != nil {
		n.logf("session %s: queued effort change failed: %v", id, err)
		change.failed = true
		share.setEffortChange(id, change)
	} else {
		share.confirmTerminalEffort(id, change.pane, change.target)
	}
	n.notifyEffortChange()
}

func (n *Node) notifyEffortChange() {
	if serving := n.switches().serving(); serving != nil {
		serving.notifySessionsSoon()
	} else {
		n.notifySessionsSoon()
	}
}

// The terminal's mode controls can look blocked to herdr while the node drives them.
func (n *Node) driveModes(id string) func() {
	n.mu.Lock()
	n.driving[id] = true
	n.mu.Unlock()
	return func() {
		n.mu.Lock()
		delete(n.driving, id)
		n.mu.Unlock()
	}
}

func (n *Node) driveTerminalEffort(ctx context.Context, id, pane, effort string) error {
	defer n.driveModes(id)()
	return n.tuiSetEffort(ctx, pane, effort)
}
