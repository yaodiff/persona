package proto

// Reviewing a session's changes (GET /api/v1/machines/{mid}/git/review → node git.review, docs/web/review.md, #151): every
// file that changed in a scope, each with its hunks, for the web's Review panel; read only.

// Review scopes: what the working tree, or a turn's checkpoints, are compared with.
const (
	ReviewUncommitted = "uncommitted" // HEAD: the staged and unstaged changes, and the new files
	ReviewBranch      = "branch"      // where the branch left the default branch (or Base): its commits and the above
	ReviewCommit      = "commit"      // a commit (Base): everything since it
	// ReviewTurn compares two working-tree checkpoints for a session, or its latest checkpoint with the working tree.
	// It is written by the node at the end of a turn, under refs/persona/checkpoints/<session>/<turn>.
	ReviewTurn = "turn"
)

// The kinds of failure git.review answers with as rpc.Error codes (the hub passes them on with 400 / 404 / 409).
const (
	CodeNotGit          = "not_git"           // cwd is not in a Git repository
	CodeNoFolder        = "no_folder"         // cwd is not there: moved or deleted (a removed worktree)
	CodeBadScope        = "bad_scope"         // a scope other than the three, or commit without a base
	CodeBadBase         = "bad_base"          // the base is not a commit (or branch) of the repository
	CodeNoDefaultBranch = "no_default_branch" // branch: no origin/HEAD, origin/main, origin/master, main or master
	CodeNoMergeBase     = "no_merge_base"     // branch: HEAD and the default branch have no commit in common
	CodeNoCheckpoint    = "no_checkpoint"     // turn: the session has no completed checkpoint yet
)

type GitReviewRequest struct {
	Cwd   string `json:"cwd"`
	Scope string `json:"scope"`
	// Base: for commit the commit; for branch the branch to compare with instead of the default one; for turn the
	// start checkpoint (or HEAD for the first turn).
	Base string `json:"base,omitempty"`
	// Session names whose checkpoints turn compares. The answer pins both checkpoint hashes; asking for a big file
	// again sends them back in Base and Target.
	Session string `json:"session,omitempty"`
	// Target pins turn's end checkpoint. Empty with Live asks for the latest checkpoint against the working tree;
	// empty without Live chooses the latest completed turn.
	Target string `json:"target,omitempty"`
	// Live asks turn for the latest checkpoint against the working tree, for a turn that is still running.
	Live bool `json:"live,omitempty"`
	// Paths: only these files (relative to the root): a file whose diff was too big for the page, asked for on its
	// own; a renamed one with both of its paths.
	Paths []string `json:"paths,omitempty"`
}

type GitReview struct {
	Root    string `json:"root"`
	Scope   string `json:"scope"`
	Session string `json:"session,omitempty"` // turn: whose checkpoints were compared
	// Base is what the working tree was compared with; its hash pins it for a file asked for later (scope commit).
	Base GitReviewBase `json:"base"`
	// Target is turn's end checkpoint, or Working when it is the files on disk.
	Target *GitReviewTarget `json:"target,omitempty"`
	// Turn is the turn of Target (or of the latest checkpoint a live turn starts from).
	Turn int64          `json:"turn,omitempty"`
	Head *GitReviewHead `json:"head,omitempty"` // absent before the first commit
	// Files in path order; the ones file.read refuses are listed without their diff.
	Files []GitReviewFile `json:"files"`
	// Truncated: more files changed than listed (the node's maxReviewFiles, node/gitreview.go).
	Truncated bool `json:"truncated,omitempty"`
}

type GitReviewBase struct {
	Hash    string `json:"hash,omitempty"`
	Short   string `json:"short,omitempty"`
	Subject string `json:"subject,omitempty"`
	// Ref: the branch the branch scope compared with (origin/main…); empty otherwise.
	Ref string `json:"ref,omitempty"`
	// Empty: the repository has no commit yet; everything is new.
	Empty bool `json:"empty,omitempty"`
}

type GitReviewTarget struct {
	Hash    string `json:"hash,omitempty"`
	Short   string `json:"short,omitempty"`
	Subject string `json:"subject,omitempty"`
	// Working: the target is the files on disk, not a checkpoint commit.
	Working bool `json:"working,omitempty"`
}

type GitReviewHead struct {
	Hash   string `json:"hash"`
	Short  string `json:"short"`
	Branch string `json:"branch,omitempty"` // empty when detached
}

type GitReviewFile struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"` // renamed or copied from
	// Status: added, modified, deleted, renamed, copied, type_changed.
	Status    string `json:"status"`
	Untracked bool   `json:"untracked,omitempty"` // a new file Git does not know yet
	Added     int    `json:"added"`
	Deleted   int    `json:"deleted"`
	Binary    bool   `json:"binary,omitempty"`
	Generated bool   `json:"generated,omitempty"` // .gitattributes says linguist-generated
	// Refused: a file file.read refuses (a key, a credentials folder…): listed, its content is not sent.
	Refused bool `json:"refused,omitempty"`
	// Big: its diff is bigger than a page carries; asked for on its own (Paths) it comes unless it is huge.
	Big bool `json:"big,omitempty"`
	// Revision: the new side's blob id (short), which changes when the file does (viewed marks).
	Revision string    `json:"revision,omitempty"`
	Hunks    []GitHunk `json:"hunks,omitempty"`
}

// GitHunk is one hunk of a unified diff: its lines with their prefix (" ", "+", "-", "\"). A side with no lines starts
// after the line the header names (old_start = a+1 for "-a,0"), so the numbers count from where the hunk is.
type GitHunk struct {
	Header   string   `json:"header,omitempty"` // the text after the second @@ (the function it is in)
	OldStart int      `json:"old_start"`
	NewStart int      `json:"new_start"`
	Lines    []string `json:"lines"`
}
