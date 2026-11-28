package proto

// CodexConfig is the non-secret inventory remembered by the hub. Live owners and connection generations are never persisted.
type CodexConfig struct {
	BlockedIDs []string          `json:"blocked_ids,omitempty"`
	Discovery  *bool             `json:"discovery,omitempty"`
	Homes      []string          `json:"homes,omitempty"`
	LegacyHome string            `json:"legacy_home,omitempty"`
	Servers    []CodexServer     `json:"servers,omitempty"`
	Joined     map[string]string `json:"joined,omitempty"` // public session id -> stable instance id
}

type CodexServer struct {
	ID       string `json:"id"`
	Home     string `json:"home"`
	Endpoint string `json:"endpoint"`
	Name     string `json:"name,omitempty"`
}

type SessionCapabilities struct {
	Read      bool `json:"read"`
	Send      bool `json:"send"`
	Interrupt bool `json:"interrupt"`
	Approve   bool `json:"approve"`
	Terminal  bool `json:"terminal"`
	Attach    bool `json:"attach"`
	// PluginInput: a Claude outside Persona's terminals whose Persona plugin polls the node: the owner's own messages
	// go in as their input while it rests, not as another session's through its inbox (docs/integrations/
	// claude-integration.md "Owner input through the Persona plugin"). Absent from older nodes and everywhere else.
	PluginInput bool `json:"plugin_input,omitempty"`
	// PermissionModes are the permission modes session.modes takes for a session whose agent is not Claude Code (Modes
	// is about Claude Code's five), as a runtime's row lists them (#8261): default, the agent's own approval, and
	// bypassPermissions where it has an Allow all (Paseo's Auto Accept and Codex's full-access). The apps offer a
	// switch between them with two or more; its PermissionMode bypassPermissions is Allow all, any other (none too)
	// its own. Here rather than on Session, which has no room left (TestSessionFitsItsSizeClass), in the bools' padding.
	// Absent from older nodes and hubs, and wherever Modes is true.
	PermissionModes PermissionModeSet `json:"permission_modes,omitempty"`
	// StopCause qualifies stopped-session recovery; absent/unknown never permits a supervisor resume.
	StopCause StopCause `json:"stop_cause,omitempty"`
	// A2AWakeOnSend lets a runtime accept task updates for an idle-retired session.
	A2AWakeOnSend bool `json:"a2a_wake_on_send,omitempty"`
	// SupervisionPolicy reports that a runtime-hosted session accepts manager-owned supervision_instructions
	// on its own policy channel (separate from user messages) and fails closed if it cannot apply them.
	// Absent/false keeps the conservative non-capable runtime routing and refusal behavior (#10338, #11121).
	SupervisionPolicy bool `json:"supervision_policy,omitempty"`
	Fork              bool `json:"fork,omitempty"`
	// AnswerQuestions permits asynchronous question replies without granting permission approvals.
	AnswerQuestions bool `json:"answer_questions,omitempty"`
	// Close is independent of terminal access. Absent on older nodes, where clients retain terminal-only closing.
	Close *bool `json:"close,omitempty"`
	// Rename permits session.rename's title without a terminal (a runtime's session, #6294). Absent on older nodes,
	// where clients keep renaming for terminal sessions only.
	Rename *bool `json:"rename,omitempty"`
	// Modes: the apps' permission mode and effort chips can change this session (session.modes). Absent (nil) from a node
	// older than it, where the apps keep guessing from agent and source_kind; false where a change would only be refused
	// (a Claude outside Persona's terminals, a runtime that did not declare caps.modes). A pointer so a hub that decodes
	// an older node's row and encodes it again keeps it absent.
	Modes *bool `json:"modes,omitempty"`
	// Model explicitly permits changing the model through session.modes. Absent preserves older clients' heuristics.
	Model        *bool `json:"model,omitempty"`
	QueueEdit    *bool `json:"queue_edit,omitempty"`
	QueueSendNow *bool `json:"queue_send_now,omitempty"`
	// Rewind permits returning to before a user message. RewindCode additionally permits restoring files.
	// Absent preserves older clients' Claude terminal behavior.
	Rewind     *bool `json:"rewind,omitempty"`
	RewindCode *bool `json:"rewind_code,omitempty"`
	// Takeover is advertised for eligible local, read-only Codex CLI and idle unowned Codex App threads.
	Takeover *CodexTakeover `json:"takeover,omitempty"`
}

type CodexTakeover struct {
	Running   bool   `json:"running"`
	CanResume bool   `json:"can_resume"`
	Reason    string `json:"reason,omitempty"`
	AppOwned  bool   `json:"app_owned,omitempty"`
}

// CanModes is a Modes value.
func CanModes(on bool) *bool { return &on }

// Bool returns a pointer to on for optional capability flags.
func Bool(on bool) *bool { return &on }

// CodeCodexEffortTerminal is a pre-launch refusal until the task card explains the fallback.
const CodeCodexEffortTerminal = "codex_effort_terminal"

// Session starts from a task card first request a notice. Only the acknowledged retry
// pins the terminal route, so a change in daemon availability cannot stale the notice.
const (
	CodexFallbackNotify    = "notify"
	CodexFallbackAnnounced = "announced"
)
