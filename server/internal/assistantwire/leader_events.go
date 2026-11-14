package assistantwire

import "time"

// The leaders' events (docs/design/proposals/owner-at-a-glance.md "Contract with a work-log extension", #3484, #3485):
// they travel from the assistant to the hub like the to-do events; extensions receive event.leader.changed and
// event.leader.report with leaders.read.
const (
	EventLeaderChanged = "leader.changed"
	EventLeaderReport  = "leader.report"
)

// LeaderChangedEvent: a leader conversation (one direction) was made, its title or project changed, or it ended (it
// left assistant.leaders; nothing is deleted). ID is the leader conversation's session id; Title its title as the apps
// show it; Project the slug of the knowledge-base project its direction is filed under ("" while none); Ended true once
// it is no longer a leader, left out while it is. Each change is told once: a restart or a replay tells nothing again.
type LeaderChangedEvent struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Name is what the direction is called (#3679): the name the owner or the secretary gave it, else its project's
	// name, else Title without its "MMDD | kind | " prefix. Always set by this hub; a name change is told like any other.
	Name    string `json:"name,omitempty"`
	Project string `json:"project,omitempty"`
	Ended   bool   `json:"ended,omitempty"`
}

// The kinds of a LeaderReportEvent.
const (
	LeaderReached  = "reached"
	LeaderDecision = "decision"
	LeaderStuck    = "stuck"
	// LeaderProgress: the leader's own summary of its direction (leader_progress, #3679): Reached, Doing, NeedsYou
	// and Links; Todo "".
	LeaderProgress = "progress"
)

// LeaderReportEvent: one of a leader's tasks reached its goal, needs the owner's decision, or is stuck; written by the
// hub from the task's ledger, never by a model. ID names the report (the same report is never sent twice; an extension
// may still key on it); Leader the leader conversation's id; Todo the task's to-do id; Text at most 300 characters in
// the owner's language; Link the task's PR or delivery link when it has one; At when what it reports happened. A plan
// of the leader's waiting for the owner's Start is a decision too (#3608): Plan is its id, Todo "" (it has none yet).
type LeaderReportEvent struct {
	ID     string    `json:"id"`
	Leader string    `json:"leader"`
	Kind   string    `json:"kind"`
	Todo   string    `json:"todo"`
	Plan   string    `json:"plan,omitempty"`
	Text   string    `json:"text"`
	Link   string    `json:"link,omitempty"`
	At     time.Time `json:"at"`
	// A progress report's parts as the leader wrote them (Text puts them together): what was reached, what is being
	// done and where it is stuck, what the owner has to do; Links its evidence (Link is the first).
	Reached  string   `json:"reached,omitempty"`
	Doing    string   `json:"doing,omitempty"`
	NeedsYou string   `json:"needs_you,omitempty"`
	Links    []string `json:"links,omitempty"`
	// Level is set while the hub's low-distraction mode is on (Hello.QuietPush): P1 for progress, a goal reached and
	// stuck, which an extension keeps in its log and briefing without alerting the owner (no message that rings, no
	// mention); P0 for a decision. Left out with the mode off, when an extension does as before.
	Level string `json:"level,omitempty"`
}

// LeaderReportTextMost is how long a report's text may be, in characters.
const LeaderReportTextMost = 300
