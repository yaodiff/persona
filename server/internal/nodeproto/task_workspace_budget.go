package nodeproto

// Budgets for the node calls that prepare a task's backend workspace (task.workspace.*, TaskWorkspace) and ensure a
// repository mirror for it (repo.mirror.ensure), in seconds like ResumeWindowSeconds: this contract imports nothing.
// The node works within them; the manager that asks and the hub that relays the manager's routes wait by them, and
// repositorywire, taskworkspace and assistantwire turn them into durations. The hub links this contract, not
// repositorywire (depguard).
const (
	TaskWorkspaceGitSeconds       = 2 * 60
	TaskWorkspaceColdFetchSeconds = 10 * 60
	TaskWorkspaceRPCSeconds       = 5 * 60
	// TaskWorkspaceMaxRepositories is the most repositories one task carries (repositorywire.MaxTaskRepositories), and
	// so the most one prepare covers.
	TaskWorkspaceMaxRepositories = 20
	// RepoMirrorColdFetchSeconds is the node's cold build budget for a repository mirror.
	RepoMirrorColdFetchSeconds = 10 * 60
	// RepoMirrorEnsureSeconds leaves time beyond the cold fetch for the node to return its result.
	RepoMirrorEnsureSeconds = RepoMirrorColdFetchSeconds + 60
)

// TaskWorkspacePrepareSeconds budgets ref discovery and a cold fetch for each sequential remote source, with time for
// local setup and the reply.
func TaskWorkspacePrepareSeconds(remoteRepos int) int {
	return TaskWorkspaceRPCSeconds + remoteRepos*(TaskWorkspaceGitSeconds+TaskWorkspaceColdFetchSeconds)
}
