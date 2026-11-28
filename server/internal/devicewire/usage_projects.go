package devicewire

import "github.com/nwverse/persona/server/internal/wireids"

// Token usage by project (docs/server/machines.md "Token usage by project", #16530): GET /api/v1/usage/projects and
// GET /api/v1/usage/projects/detail. The manager sums every machine's usage by the project each session's start
// directory resolved to on its node, across machines; the apps only show what it sends.

// The project kinds (UsageProject.Kind) and the groups the apps show them in (UsageProject.Group). Development is a
// git working tree; everything else a session started in is non-development ("other"); usage whose directory no node
// resolved is "unknown".
type (
	UsageProjectKind  string
	UsageProjectGroup string
)

const (
	UsageProjectRepo      UsageProjectKind = "repo"      // a git working tree with a remote: Name is "owner/repo", Host its host
	UsageProjectLocal     UsageProjectKind = "local"     // a git working tree with no usable remote: Name is its folder's
	UsageProjectFolder    UsageProjectKind = "folder"    // a folder not in a repository: Name is its name
	UsageProjectHome      UsageProjectKind = "home"      // the home folder itself (the apps name it; never the account's name)
	UsageProjectTemp      UsageProjectKind = "temp"      // temporary folders
	UsageProjectAssistant UsageProjectKind = "assistant" // the Persona assistant's conversations
	UsageProjectUnknown   UsageProjectKind = "unknown"   // a directory no node resolved, or a record that names none

	UsageGroupDevelopment UsageProjectGroup = "development"
	UsageGroupOther       UsageProjectGroup = "other"
	UsageGroupUnknown     UsageProjectGroup = "unknown"
)

// UsageProjectRanges are the days the ranges cover, in order: today, the last 3 days, the last 7 days (each ending
// today, the calendar's days in the user's zone).
var UsageProjectRanges = []int{1, 3, 7}

// UsageTokens are the four kinds of tokens and the model replies over some time, as the other usage routes give them.
type UsageTokens struct {
	Input      int64 `json:"in"` // not from the cache
	CacheWrite int64 `json:"cache_write"`
	CacheRead  int64 `json:"cache_read"`
	Output     int64 `json:"out"`
	Requests   int   `json:"req"`
}

// UsageProject is one project's usage over a range.
type UsageProject struct {
	Key   string            `json:"key"`   // what GET /api/v1/usage/projects/detail?key= takes; the apps never read into it
	Kind  UsageProjectKind  `json:"kind"`  // UsageProjectRepo …
	Group UsageProjectGroup `json:"group"` // UsageGroupDevelopment, UsageGroupOther or UsageGroupUnknown
	// Name: "owner/repo" as its remote writes it, a folder's name; "" for home, temp, assistant and unknown.
	Name     string      `json:"name"`
	Host     string      `json:"host,omitempty"` // a remote's host
	Machines int         `json:"machines"`       // how many computers it ran on in the range
	Share    float64     `json:"share"`          // of the range's total, 0…1
	Tokens   UsageTokens `json:"tokens"`
}

// UsageProjectRange is one range of GET /api/v1/usage/projects: its total (the calendar's for the same days), each
// project's part, largest first, and what is not split: usage stored before the manager kept directories, or from an
// older node.
type UsageProjectRange struct {
	Days     int            `json:"days"`
	Total    UsageTokens    `json:"total"`
	Projects []UsageProject `json:"projects"`
	NotSplit UsageTokens    `json:"not_split"`
}

// UsageProjects is GET /api/v1/usage/projects: the ranges of UsageProjectRanges, in that order, so the apps switch
// between them without asking again.
type UsageProjects struct {
	Zone   string              `json:"zone"`
	Ranges []UsageProjectRange `json:"ranges"`
	// Stored: the manager keeps the history (Postgres); false: only what is in memory (the last 26 hours).
	Stored bool `json:"stored"`
}

// UsagePart is one machine's, agent's, model's or launcher's part of a project's usage over a range, largest first.
type UsagePart struct {
	// Key: the machine's id, the agent (proto.Session.Agent's names), the model, or the launcher ("" a session typed in
	// a terminal, an older node's).
	Key string `json:"key"`
	// Name: the machine's name ("" for a machine no longer here); "" for the others (the apps name agents, models and
	// launchers).
	Name   string      `json:"name,omitempty"`
	Tokens UsageTokens `json:"tokens"`
}

// UsagePlace is one working tree or folder of a project on one machine: written from the home folder.
type UsagePlace struct {
	Machine     wireids.MachineID `json:"machine"`
	MachineName string            `json:"machine_name,omitempty"`
	Place       string            `json:"place"`
	Worktree    bool              `json:"worktree,omitempty"` // a linked worktree, not the main checkout
	Tokens      UsageTokens       `json:"tokens"`
}

// UsageProjectBreakdown is one range of a project's breakdown.
type UsageProjectBreakdown struct {
	Days  int         `json:"days"`
	Total UsageTokens `json:"total"`
	Share float64     `json:"share"` // of every project's usage in the range, 0…1
	// CacheHit: cache reads over all input (input, cache writes and cache reads), 0…1; 0 with none.
	CacheHit  float64      `json:"cache_hit"`
	Machines  []UsagePart  `json:"machines"`
	Agents    []UsagePart  `json:"agents"`
	Models    []UsagePart  `json:"models"`
	Launchers []UsagePart  `json:"launchers"`
	Places    []UsagePlace `json:"places"`
}

// UsageProjectDay is one calendar day of a project's usage; UsageProjectHour one hour of today.
type UsageProjectDay struct {
	Date   string      `json:"date"` // 2006-01-02
	Tokens UsageTokens `json:"tokens"`
}

// UsageProjectHour is one hour on the clock of today, in the user's zone (a day with a clock change has 23 or 25).
type UsageProjectHour struct {
	T      int64       `json:"t"` // Unix seconds of its start
	Hour   int         `json:"hour"`
	Tokens UsageTokens `json:"tokens"`
}

// UsageProjectDetail is GET /api/v1/usage/projects/detail?key=: the project (its tokens are the last 7 days'), its
// breakdown for each range of UsageProjectRanges, its last 7 days, oldest first, and today's hours.
type UsageProjectDetail struct {
	Zone    string                  `json:"zone"`
	Project UsageProject            `json:"project"`
	Ranges  []UsageProjectBreakdown `json:"ranges"`
	Days    []UsageProjectDay       `json:"days"`
	Hours   []UsageProjectHour      `json:"hours"`
	Stored  bool                    `json:"stored"`
}

// UsageProjectsReply is GET /api/v1/usage/projects's body; UsageProjectDetailReply GET /api/v1/usage/projects/detail's.
type UsageProjectsReply struct {
	Usage UsageProjects `json:"usage"`
}

type UsageProjectDetailReply struct {
	Usage UsageProjectDetail `json:"usage"`
}
