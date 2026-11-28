package assistant

import (
	"fmt"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// idlePlanReuse looks at live sessions, including ones released from a closed to-do.
// A task already assigned to an Agent is covered by the open-work hints instead.
func (s *Service) idlePlanReuse(space string, tasks []PlanTask, sessions []proto.Session) string {
	var b strings.Builder
	seen := make(map[[2]string]bool)
	for _, task := range tasks {
		for _, agent := range sessions {
			key := [2]string{agent.MachineID, agent.SessionID}
			if agent.MachineID != task.Machine || seen[key] || !idleAgentForPlan(agent, space) || !samePlanCheckout(task, agent) {
				continue
			}
			if _, busy := s.openTodoOfAgent(agent.MachineID, agent.SessionID); busy {
				continue
			}
			if len(seen) == 0 {
				b.WriteString("\n\nIdle Agents available in the same directory or worktree. If context and permissions fit, use agent_send with these arguments and text. Add task for a new supervised to-do. This hint is advisory.")
			}
			fmt.Fprintf(&b, "\n- machine=%q session_id=%q", agent.MachineID, agent.SessionID)
			seen[key] = true
			if len(seen) == 4 {
				return b.String()
			}
		}
	}
	return b.String()
}

func idleAgentForPlan(agent proto.Session, space string) bool {
	return len(agent.SessionID) >= 6 && !agent.Assistant && !agent.Archived && !agent.IsDeleted() && !agent.IsSubagent &&
		!agent.HiddenByDirectory() && runSpace(agent) == space && agent.PendingInteractions == 0 &&
		(agent.Status == proto.StatusIdle || agent.Status == proto.StatusDone) &&
		(agent.State == "" || agent.State == proto.StateIdle || agent.State == proto.StateUnread) &&
		!agent.BackgroundWorking && (agent.SourceKind == "" || agent.Capabilities != nil && agent.Capabilities.Send)
}

func samePlanCheckout(task PlanTask, agent proto.Session) bool {
	if task.Workspace == nil || task.Workspace.Mode == workspaceNew || task.backendWorkspace() {
		return false
	}
	dir := taskRunDir(task)
	if !proto.NodeAbs(dir) {
		return false
	}
	dir = proto.NodeKey(dir)
	for _, cwd := range []string{agent.Cwd, agent.WorkspaceCwd} {
		if proto.NodeAbs(cwd) && dir == proto.NodeKey(cwd) {
			return true
		}
	}
	if !proto.NodeAbs(agent.Worktree) {
		return false
	}
	root := proto.NodeKey(agent.Worktree)
	sep := "/"
	if proto.WindowsPath(root) {
		sep = `\`
	}
	return dir == root || strings.HasPrefix(dir, strings.TrimRight(root, sep)+sep)
}
