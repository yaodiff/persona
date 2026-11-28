package node

// The worktree.* methods (Node.handle, #8089); see handle_file.go.
const (
	methodWorktreePrepare  = "worktree.prepare"
	methodWorktreeCreate   = "worktree.create"
	methodWorktreeSource   = "worktree.source"
	methodWorktreePreset   = "worktree.preset"
	methodWorktreeDelete   = "worktree.delete"
	methodWorktreeAdd      = "worktree.add"
	methodWorktreeStatus   = "worktree.status"
	methodWorktreeRemove   = "worktree.remove"
	methodWorktreeSnapshot = "worktree.snapshot"
	methodWorktreeRename   = "worktree.rename"
)

// worktreePrepareParams is worktree.prepare's request (worktree.go): Composer asks for the composer's variant.
type worktreePrepareParams struct {
	Composer    bool   `json:"composer"`
	Kind        string `json:"kind"`
	WorkspaceID string `json:"workspace_id"`
	Cwd         string `json:"cwd"`
	Branch      string `json:"branch"`
	// AllowUntracked applies only to an automatic task branch in the checkouts folder.
	AllowUntracked bool `json:"allow_untracked"`
}

// worktreeCreateParams is worktree.create's request (worktree.go): Composer asks for the composer's variant, AutoName for
// a host-generated branch and path, ParentWorkspaceID is independent of the Git source.
type worktreeCreateParams struct {
	Composer          bool   `json:"composer"`
	Kind              string `json:"kind"`
	WorkspaceID       string `json:"workspace_id"`
	Cwd               string `json:"cwd"`
	Branch            string `json:"branch"`
	Path              string `json:"path"`
	ParentWorkspaceID string `json:"parent_workspace_id"`
	AutoName          bool   `json:"auto_name"`
	// ForAgent: the assistant makes this worktree for an Agent it starts there, so its first shell is Persona's
	// (finished_close.go, #10624). The user's "New worktree…" never sends it.
	ForAgent bool `json:"for_agent"`
	// OpenWorkspace defaults to true. A grouped herdr or Orca task sets false for a checkout whose Agent tab opens in the
	// task's shared workspace instead (#4786).
	OpenWorkspace *bool `json:"open_workspace,omitempty"`
}

// worktreeSourceParams is the request of worktree.source, .preset and .delete: which workspace (their handlers read the rest of the request themselves).
type worktreeSourceParams struct {
	Kind        string `json:"kind"`
	WorkspaceID string `json:"workspace_id"`
}

// worktreeAddParams is worktree.add's request (agentworktree.go, #6115).
type worktreeAddParams struct {
	Kind     string `json:"kind"`
	Cwd      string `json:"cwd"`
	Branch   string `json:"branch"`
	NameHint string `json:"name_hint"`
}

// worktreeStatusParams is worktree.status's request.
type worktreeStatusParams struct {
	Kind string `json:"kind"`
	Cwd  string `json:"cwd"`
}

// worktreeRemoveParams is worktree.remove's request.
type worktreeRemoveParams struct {
	Kind         string `json:"kind"`
	Path         string `json:"path"`
	DeleteBranch bool   `json:"delete_branch"`
	Force        bool   `json:"force"`
}

// worktreeSnapshotParams is worktree.snapshot's request (reviewsnapshot.go, #2173).
type worktreeSnapshotParams struct {
	Kind     string `json:"kind"`
	Cwd      string `json:"cwd"`
	NameHint string `json:"name_hint"`
}

// worktreeRenameParams is worktree.rename's request.
type worktreeRenameParams struct {
	Kind        string `json:"kind"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
}
