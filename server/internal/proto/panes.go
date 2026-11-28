package proto

// PanesResponse is a machine's terminal host layout for the terminal browser (docs/integrations/terminal.md "Standalone terminal"): every
// workspace, tab and pane of each host, agent or plain shell. It is read from the snapshot the node already takes;
// ids are host-local (classic herdr's also per herdr session: a named session's are "@<session>:<id>", herdr.QualifyID).
type PanesResponse struct {
	Hosts []PaneHost `json:"hosts"`
}

// PaneHost is one terminal host (herdr, herdr-core, Orca…) and what it can do with a pane.
type PaneHost struct {
	Kind string `json:"kind"`
	// Stream: the host streams a pane's terminal (observe); without it a pane can only be read once a second.
	Stream bool `json:"stream"`
	// Control: a phone can drive a pane at its own size ("Take over").
	Control bool `json:"control"`
	// NewTab: the host can open a new shell tab in a workspace (panes.new).
	NewTab bool `json:"new_tab"`
	// Worktree: the host can make a Git worktree of a workspace's repository and open it as a workspace (worktree.create).
	Worktree bool `json:"worktree"`
	// Abilities: what else the node does with the host's workspaces, tabs and panes, for the clients' menus (#17053).
	// Clients offer an action where it says so, never by the host's name. An older node leaves it out; clients then keep
	// a table of what such a node does.
	Abilities  *PaneAbilities `json:"abilities,omitempty"`
	Workspaces []PaneSpace    `json:"workspaces"`
	Tabs       []PaneTab      `json:"tabs"`
	Panes      []PaneItem     `json:"panes"`
}

// PaneAbilities is what the node's menu methods (docs/integrations/terminal.md "Node menus") do on one terminal host,
// read from what the host implements, so a client follows a host that gains an ability without knowing its name.
type PaneAbilities struct {
	// Rename: its workspaces and tabs can be renamed (spaces rename, tabs.rename; host.Organizer).
	Rename bool `json:"rename"`
	// ClosePane: one pane can be closed (panes.close; host.Organizer).
	ClosePane bool `json:"close_pane"`
	// Split: a pane can be split in two (panes.split; host.Splitter).
	Split bool `json:"split"`
	// CloseTab: a whole tab can be closed with every pane in it (tabs.close; host.SlotCloser).
	CloseTab bool `json:"close_tab"`
	// Git: Pull, Push and Fetch run in its panes' and workspaces' folders (panes.git); false for a host whose terminals
	// the node does not drive (botmux).
	Git bool `json:"git"`
	// RunHere: "Run agent here" starts an agent in one of its plain shell panes (panes.agent): the host is the
	// default engine's and can name a pane's foreground programs.
	RunHere bool `json:"run_here"`
}

type PaneSpace struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Number int    `json:"number,omitempty"`
	Cwd    string `json:"cwd,omitempty"`
	Branch string `json:"branch,omitempty"`
}

type PaneTab struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Number      int    `json:"number,omitempty"`
}

// PaneItem is one pane. Terminal is what the terminal stream calls it ("pane:<pane>", docs/protocol/protocol.md "Terminal stream");
// SessionID is set when an agent session of this machine runs in it (open that one instead: it shares the stream the
// agent's own terminal panel uses).
type PaneItem struct {
	Pane        string `json:"pane"`
	Terminal    string `json:"terminal"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	// Agent is the kind of agent running there ("claude", "codex"…), "" for a plain shell.
	Agent     string `json:"agent,omitempty"`
	Status    string `json:"status,omitempty"` // the host's status for the agent (Status*)
	Title     string `json:"title,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	// Foreground is the program in front of a pane with no agent ("zsh" at its prompt, "vim", "npm"…), as the host
	// names it; nothing on the screen is read (#4754). "" when the host could not say in time.
	Foreground string `json:"foreground,omitempty"`
	// AgentSession is the stopped agent whose terminal this is: a pane Persona opened for that session, with no agent
	// in it now (#4754). "Close finished terminals" closes these (panes.close_finished).
	AgentSession string `json:"agent_session,omitempty"`
}

// NewShellResult is what panes.new opened.
type NewShellResult struct {
	Pane     string `json:"pane"`
	Terminal string `json:"terminal"`
	TabID    string `json:"tab_id,omitempty"`
}

// PaneSplitResult is what panes.split opened: the new pane next to the one split.
type PaneSplitResult struct {
	Pane     string `json:"pane"`
	Terminal string `json:"terminal"`
}

// PaneGitResult is what panes.git did in a pane's or workspace's directory. Outcome is what came of it, for clients to
// say in their own language (GitOutcome…); Message the same in English for older clients; Output what git printed
// (cut short).
type PaneGitResult struct {
	Op      string `json:"op"`
	Outcome string `json:"outcome,omitempty"`
	Message string `json:"message"`
	Output  string `json:"output,omitempty"`
}

// The outcomes of panes.git (#1888).
const (
	GitOutcomeFetched       = "fetched"
	GitOutcomeUpToDate      = "up_to_date"
	GitOutcomePulled        = "pulled"
	GitOutcomeNothingToPush = "nothing_to_push"
	GitOutcomePushed        = "pushed"
)

// WorktreePrepare is what worktree.prepare knows of a workspace's repository, for the "New worktree…" dialog. Path is the
// directory the host puts a repository's checkouts under (`<dir>/<repo>`): a checkout of branch b goes in
// Path + "/" + Slug(b), the host's own naming, which is what the dialog fills in from the branch.
type WorktreePrepare struct {
	Root     string `json:"root"`
	RepoName string `json:"repoName"`
	// Branch is the branch checked out in the workspace ("" on a detached HEAD).
	Branch string `json:"branch"`
	Path   string `json:"path"`
	// ChooseParent permits parent_workspace_id independently of the source workspace. Old nodes omit it.
	ChooseParent bool `json:"choose_parent,omitempty"`
	// AutomaticName permits an unnamed create. The host owns naming when supported, otherwise the node chooses a unique name.
	AutomaticName bool `json:"automatic_name,omitempty"`
	// Composer exposes the owning runtime's creation choices; absent on older nodes.
	Composer *WorktreeComposer `json:"composer,omitempty"`
	// Existing is a successfully inspected clean, idle checkout of the requested branch.
	// Absent when the branch is not checked out, no branch was requested, or the node predates branch lookup.
	Existing *WorktreeCheckout `json:"existing,omitempty"`
}

type WorktreeCheckout struct {
	Path   string `json:"path"`
	Branch string `json:"branch"`
	// InCheckouts: the checkout lies in the folder where checkouts of this repository go (WorktreePrepare.Path), not
	// the main checkout or a folder of the user's own. Older nodes leave it out.
	InCheckouts bool `json:"in_checkouts,omitempty"`
}

type WorktreeComposer struct {
	Capabilities []string         `json:"capabilities"`
	Project      string           `json:"project"`
	BaseRef      string           `json:"base_ref"`
	Refs         []string         `json:"refs"`
	Presets      []WorktreePreset `json:"presets"`
}

type WorktreePreset struct {
	ID          string   `json:"id"`
	Name        string   `json:"name,omitempty"`
	Directories []string `json:"directories"`
}

// WorktreeAdded is the worktree worktree.add made for a new agent (#6115): its checkout, its branch, the repository's root.
// WorkspaceID is the workspace a runtime engine made it as (Paseo's, docs/integrations/runtimes.md "Workspaces"), when one did.
type WorktreeAdded struct {
	Path        string `json:"path"`
	Branch      string `json:"branch"`
	Root        string `json:"root"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// WorktreeSnapshot is what worktree.snapshot made (#2173): a detached checkout of Root's HEAD at Path with Root's
// uncommitted change in it, for a reviewer to read without touching Root. Skipped counts the untracked files left
// out (over the copy's limits).
type WorktreeSnapshot struct {
	Path    string `json:"path"`
	Root    string `json:"root"`
	Head    string `json:"head"`
	Skipped int    `json:"skipped,omitempty"`
}

// WorktreeStatus is what worktree.status says of a directory (#6115): whether it is a linked Git worktree, whether
// Persona made it (it lies under the directory the computer puts this repository's checkouts in), and what removing
// it would lose: changed files, and commits not on the branch the repository's main checkout is on. Checkouts is the
// directory a new agent's worktree of this repository goes under, for any folder in a repository.
type WorktreeStatus struct {
	Worktree   bool   `json:"worktree"`
	Ours       bool   `json:"ours"`
	Path       string `json:"path,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Root       string `json:"root,omitempty"`
	MainBranch string `json:"main_branch,omitempty"`
	Checkouts  string `json:"checkouts,omitempty"`
	Dirty      int    `json:"dirty"`
	Unmerged   int    `json:"unmerged"`
}

// WorktreeRemoved is what worktree.remove did.
type WorktreeRemoved struct {
	Removed       bool `json:"removed"`
	BranchDeleted bool `json:"branch_deleted"`
}

// WorktreeCreateResult is what worktree.create opened: the new workspace, and the checkout it is in.
type WorktreeCreateResult struct {
	WorkspaceID       string `json:"workspace_id"`
	ParentWorkspaceID string `json:"parent_workspace_id,omitempty"`
	TabID             string `json:"tab_id,omitempty"`
	Pane              string `json:"pane,omitempty"`
	Terminal          string `json:"terminal,omitempty"`
	Path              string `json:"path"`
	Branch            string `json:"branch"`
}

// WorktreeDeletePreview binds confirmation to a host-owned workspace and an opaque, short-lived node token.
type WorktreeDeletePreview struct {
	WorkspaceID  string `json:"workspace_id"`
	Kind         string `json:"kind"`
	HostID       string `json:"host_id"`
	InstanceID   string `json:"instance_id"`
	Path         string `json:"path"`
	Branch       string `json:"branch"`
	Dirty        int    `json:"dirty"`
	Folder       bool   `json:"folder"`
	PreviewToken string `json:"preview_token"`
	CloseOnly    bool   `json:"close_only,omitempty"`
}

// WorktreeDeleteResult reports completion separately from runtime acceptance or an ambiguous transport outcome.
type WorktreeDeleteResult struct {
	WorkspaceID   string `json:"workspace_id"`
	Removed       bool   `json:"removed"`
	State         string `json:"state"`
	BranchDeleted bool   `json:"branch_deleted,omitempty"`
	// Present only when this Herdr node can atomically surrender the exact device's unspent token.
	CanRevokePreview    bool   `json:"can_revoke_preview,omitempty"`
	RevokedPreviewToken string `json:"revoked_preview_token,omitempty"`
}
