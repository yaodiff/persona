package repositorywire

import "time"

// Budgets for preparing a task's backend workspace (task.workspace.*, #17786). The Node that prepares the workspace,
// the Manager that asks for it and the hub that relays the request all wait by them, so they live in this contract.
const (
	TaskWorkspaceGitTimeout       = 2 * time.Minute
	TaskWorkspaceColdFetchTimeout = 10 * time.Minute
	TaskWorkspaceRPCTimeout       = 5 * time.Minute
)

// TaskWorkspacePrepareTimeout budgets ref discovery and a cold fetch for each sequential remote source, with time for
// local setup and the reply.
func TaskWorkspacePrepareTimeout(remoteRepos int) time.Duration {
	return TaskWorkspaceRPCTimeout + time.Duration(remoteRepos)*(TaskWorkspaceGitTimeout+TaskWorkspaceColdFetchTimeout)
}
