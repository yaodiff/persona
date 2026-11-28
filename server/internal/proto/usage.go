package proto

// UsageBucket is one minute of one model's token usage on a machine, summed over every transcript there (node
// usage.go). A node reports a bucket's whole total each time it changes, so reporting it again replaces it. Runtime-only agents instead send per-request deltas
// in a usage notification with add: true; those buckets are added and survive a file-meter full window.
type UsageBucket struct {
	Minute int64  `json:"minute"` // Unix seconds of the minute's start
	Model  string `json:"model"`
	// Launcher ran the replies: the session's launcher as Persona started it, "" when unknown (typed in a terminal, an
	// older node). A minute has one bucket per model, launcher, agent and dir.
	Launcher string `json:"launcher,omitempty"`
	// Agent ran the replies: Session.Agent's names ("claude", "codex", "traex", …; tokenuse); "" from an older node,
	// and for the usage the manager stored before it kept agents apart.
	Agent string `json:"agent,omitempty"`
	// Dir is where the replies' session started (docs/server/machines.md "Token usage by project", #16530): its start
	// directory written from the home folder ("~/code/app", "~"; a directory outside home as it is), or a UsageDir*
	// token. "" from an older node, from a node whose manager does not keep it, and for the usage stored before.
	Dir        string `json:"dir,omitempty"`
	Input      int64  `json:"in,omitempty"` // not from the cache
	CacheWrite int64  `json:"cache_write,omitempty"`
	CacheRead  int64  `json:"cache_read,omitempty"`
	Output     int64  `json:"out,omitempty"`
	Requests   int    `json:"req,omitempty"` // model replies
}

// The UsageBucket.Dir tokens: where no directory stands. A directory is absolute or starts with "~", so a token never
// names one.
const (
	UsageDirAssistant = ":assistant" // a Persona assistant conversation
	// UsageDirTemp: a session started in a temporary folder (the system's temp dir, /tmp), sent to a manager that does
	// not take them by their paths (usage.features' temp_dirs, #16827).
	UsageDirTemp    = ":temp"
	UsageDirUnknown = ":unknown" // a record that names no directory
)

// UsageDir is what a node resolved a start directory (UsageBucket.Dir) to, sent with the "usage" notification that
// first names it on a connection ("dirs"). The manager keeps the latest of each machine's (usage_dirs), so the usage
// of a directory removed since keeps its project.
type UsageDir struct {
	Dir string `json:"dir"`
	// Project is the key the manager sums by, across machines: "git:<host>/<path>" (a git working tree with a remote,
	// credentials and port dropped), "local:<root commit>" (a repository with no usable remote), "local:dir:<folder>"
	// (one with no commit yet), "folder:<folder name, lower case>" (not in a repository), "home", "temp" (a temporary
	// folder in no repository made inside it, or gone: never named, and never replacing another resolution, #16827),
	// "assistant".
	Project string `json:"project"`
	// Name is how the apps show the project: "owner/repo" as the remote writes it, a folder's name; "" for home,
	// temp and assistant (the apps name them).
	Name string `json:"name,omitempty"`
	Host string `json:"host,omitempty"` // a git remote's host
	// Place is the working tree's top (a repository) or the folder (not in one), written as Dir is.
	Place    string `json:"place,omitempty"`
	Worktree bool   `json:"worktree,omitempty"` // Place is a linked worktree, not the main checkout
}
