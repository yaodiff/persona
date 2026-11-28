package assistantwire

import (
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the Agents business: the manager's (docs/assistant/agent-work.md "Responsibilities") ----
//
// The manager triages an agent's turn once it ends (rules first, then a model), sends "Continue" after a transient model
// error, wakes an agent when what it waits for should be over, and opens a card for what the user is to decide; it
// names the tabs of new agents and says what a new or resumed agent runs on ("Models" › Agents, a model picked for one
// start). The hub only carries it: it tells the manager what happened (session.turn_started / turn_ended,
// session.seen), shows what the manager reports (Snapshot.Work on the session lists, the cards among
// Snapshot.Interactions) and asks it to fill in a start (agents.start_params, agents.resume_params, agents.tab_name).

// AgentsConfig is manager.json "agents" (Hello.Agents, over the hub's hello): what the manager does about agents'
// turns by default.
type AgentsConfig struct {
	Triage   TriageConfig   `json:"triage,omitzero"`
	Delegate DelegateConfig `json:"delegate,omitzero"`
	// Dispatch is manager.json agents.dispatch: an agent handing a subtask to another connected computer
	// (docs/plans/plan-agent-dispatch.md).
	Dispatch DispatchConfig `json:"dispatch,omitzero"`
	A2A      A2AConfig      `json:"a2a,omitzero"`
	// FileTransfer is manager.json agents.file_transfer: the Agents' and the assistant's file_transfer tool, moving
	// files between the user's computers (docs/protocol/file-transfer.md "Agents and the assistant").
	FileTransfer FileTransferConfig `json:"file_transfer,omitzero"`
	// LauncherHealth is manager.json agents.launcher_health: checking each machine's launchers (docs/server/launcher-health.md).
	LauncherHealth LauncherHealthConfig `json:"launcher_health,omitzero"`
	// Review is manager.json agents.review: the independent reviewer of a supervised task's finished change (#2173,
	// docs/assistant/unified-entry.md "Independent review").
	Review ReviewConfig `json:"review,omitzero"`
	// SortByActivity and ShowFilter are manager.json agents.sort_by_activity / agents.show_filter: the initial values of
	// the Agents list's display settings (GET /api/v1/agents/display-settings) until someone saves their own choice. Nil
	// is off.
	SortByActivity *bool `json:"sort_by_activity,omitempty"`
	ShowFilter     *bool `json:"show_filter,omitempty"`
}

// A2AConfig defaults to the manager's confirmed personal computers. A supplied
// list narrows that scope. PublicURL enables the authenticated HTTP interface.
// Launch defaults to enabled (nil or true) within the allowed machine scope;
// set Launch to false to keep A2A messaging while disabling new session launches.
type A2AConfig struct {
	Machines  []string `json:"machines,omitzero"`
	PublicURL string   `json:"public_url,omitempty"`
	Launch    *bool    `json:"launch,omitempty"`
}

// LaunchAllowed reports whether A2A may start new subagent sessions in scope (default true).
func (c A2AConfig) LaunchAllowed() bool { return c.Launch == nil || *c.Launch }

const ScopeA2A = "agents.a2a"

// A2AToolNames are the A2A tools every session in scope gets, whatever its harness: the node binds their caller
// (node.callerTools) and the A2A-only catalogs (session MCP tickets, daemons, `personad tool`) keep exactly these.
// a2a_read reads another session's recent conversation (#13831).
var A2AToolNames = []string{"a2a_sessions", "a2a_read", "a2a_send", "a2a_get", "a2a_report", "a2a_cancel"}

func IsA2ATool(name string) bool {
	return slices.Contains(A2AToolNames, name)
}

// DispatchConfig is manager.json agents.dispatch: the agents' dispatch_start / dispatch_read tools. Off unless Enabled
// is true: a dispatched task runs an agent on another computer on its model, started by an agent and not by the user.
type DispatchConfig struct {
	// Enabled: off unless true.
	Enabled *bool `json:"enabled,omitempty"`
	// MaxActive: how many dispatched tasks of one agent may be working at a time. Default 3.
	MaxActive int `json:"max_active,omitempty"`
}

// On: the switch, off unless set true.
func (c DispatchConfig) On() bool { return c.Enabled != nil && *c.Enabled }

// Limit is MaxActive, 3 when unset.
func (c DispatchConfig) Limit() int {
	if c.MaxActive <= 0 {
		return 3
	}
	return c.MaxActive
}

// FileTransferConfig is manager.json agents.file_transfer: the file_transfer tool. On unless Enabled is false: it
// moves files only between the user's own confirmed personal computers, at an Agent's or the assistant's call, under
// the guards the tool's own (no overwrite unless asked, credentials refused, an audit row each).
type FileTransferConfig struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// On: the switch, on unless set false.
func (c FileTransferConfig) On() bool { return c.Enabled == nil || *c.Enabled }

// ReviewConfig is manager.json agents.review: when a supervised task's agent is judged done, a reviewer agent started
// with Launcher (a launcher on the agent's computer), Model and Effort (its defaults when empty) and Permission (a
// permission mode; default plan) reviews the change before the task waits for review. TimeoutMinutes bounds one
// review (default 30). Off unless Launcher is set: there is no default reviewer, so the review runs on a model the
// user picked, apart from the agents'.
type ReviewConfig struct {
	Launcher       string `json:"launcher,omitempty"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	Permission     string `json:"permission,omitempty"`
	TimeoutMinutes int    `json:"timeout_minutes,omitempty"`
}

// On: a reviewer launcher is configured.
func (c ReviewConfig) On() bool { return strings.TrimSpace(c.Launcher) != "" }

// Timeout is TimeoutMinutes, 30 minutes when unset.
func (c ReviewConfig) Timeout() time.Duration {
	if c.TimeoutMinutes <= 0 {
		return 30 * time.Minute
	}
	return time.Duration(c.TimeoutMinutes) * time.Minute
}

// PermissionMode is Permission, plan when unset: the launcher reads and answers, it does not edit.
func (c ReviewConfig) PermissionMode() string {
	if p := strings.TrimSpace(c.Permission); p != "" {
		return p
	}
	return "plan"
}

// LauncherHealthConfig is manager.json agents.launcher_health: how often each machine's launchers are checked, and the agent
// that repairs the ones a node cannot repair itself.
type LauncherHealthConfig struct {
	// Enabled: check on a schedule and repair what fails (unset: off). Off, failed starts are still recorded and
	// steer new agents away.
	Enabled *bool `json:"enabled,omitempty"`
	// IntervalMinutes between two checks of one launcher on one machine. Default 30.
	IntervalMinutes int                  `json:"interval_minutes,omitempty"`
	Repair          LauncherRepairConfig `json:"repair,omitzero"`
}

// LauncherRepairConfig is the agent that repairs a machine's launchers: started on Machine (default: the assistant's)
// in Cwd (default: home) with Launcher (default: a healthy one there) and Permission (a permission mode; default:
// bypassPermissions, so it repairs without waiting on approvals), told to use Skill. No Skill: no repair agent.
type LauncherRepairConfig struct {
	Skill      string `json:"skill,omitempty"`
	Machine    string `json:"machine,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	Launcher   string `json:"launcher,omitempty"`
	Permission string `json:"permission,omitempty"`
}

// DelegateConfig is manager.json agents.delegate: an agent's question goes to the assistant before the user
// (docs/assistant/assistant.md "Assistant answers questions first").
type DelegateConfig struct {
	// Enabled: off unless true.
	Enabled *bool `json:"enabled,omitempty"`
	// TimeoutSeconds: how long the hub waits for the assistant before it asks the user as it would have. Default 180.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// Rules are the user's own, read with the built-in ones ("add the missing tests, don't ask me" …). They cannot take a red line off
	// the user: an answer that would set one off is left to them all the same.
	Rules string `json:"rules,omitempty"`
}

// On: the switch, off unless set true.
func (c DelegateConfig) On() bool { return c.Enabled != nil && *c.Enabled }

// Timeout is TimeoutSeconds, 3 minutes when unset.
func (c DelegateConfig) Timeout() time.Duration {
	if c.TimeoutSeconds <= 0 {
		return 3 * time.Minute
	}
	return time.Duration(c.TimeoutSeconds) * time.Second
}

// TriageConfig is manager.json agents.triage: the limits, and the switches a hub whose user never saved them starts with
// (unset: off, but for transient_retry).
type TriageConfig struct {
	Enabled      *bool `json:"enabled,omitempty"`
	AutoContinue *bool `json:"auto_continue,omitempty"`
	Wake         *bool `json:"wake,omitempty"`
	Decide       *bool `json:"decide,omitempty"`
	ToolLoop     *bool `json:"tool_loop,omitempty"`
	Closeout     *bool `json:"closeout,omitempty"`
	CloseoutAsk  *bool `json:"closeout_ask,omitempty"`
	// TransientRetry: the initial value of the transient_retry switch (unset: on): "Continue" after a transient model
	// error, as a rule, whether or not the triage itself is on.
	TransientRetry *bool `json:"transient_retry,omitempty"`
	// MaxRetries is retained for rolling compatibility; temporary model retries have no attempt limit.
	MaxRetries int `json:"max_retries,omitempty"`
	// RetryBaseSeconds: the first "Continue" after a transient error waits this long, each next one twice as long as
	// the one before (a rate limit four times as long). Default 15.
	RetryBaseSeconds int `json:"retry_base_seconds,omitempty"`
	// AccountExhaustionMaxRetries bounds automatic continuations for coding Agents, including task-leader Agents,
	// when the provider says no upstream account is available. All Assistant conversations keep their existing
	// recovery: main, leader and background-supervision chats, side chats, scheduled-task and health chats, and
	// meeting rooms. Zero (the default) disables these continuations; a positive value compares the shared consecutive
	// transient retry count with that bound. Ordinary transient failures are not limited by this setting.
	AccountExhaustionMaxRetries int `json:"account_exhaustion_max_retries,omitempty"`
	// MaxWakes: wakes for one wait before asking the user. Default 6.
	MaxWakes int `json:"max_wakes,omitempty"`
	// SettleSeconds: how long a stopped session stays stopped before its turn is triaged. Default 15.
	SettleSeconds int `json:"settle_seconds,omitempty"`
	// JustFinishedHours: how long after its turn ended a done turn nobody has seen stays Just finished on the lists
	// (proto.WorkState.JustFinished, #9751). Default 3.
	JustFinishedHours int `json:"just_finished_hours,omitempty"`
}

// WorkReport is the snapshot's work (Snapshot.Work): where each agent stands after its latest turn, as the manager
// found it. The hub puts a session's on the lists while the session is still stopped on that turn (Work.TurnAt), and
// none at all while On is false; what an agent shows while it works or asks on its own card is the hub's to say.
type WorkReport struct {
	On       bool          `json:"on"` // Settings › session triage is on
	Sessions []SessionWork `json:"sessions"`
}

// SessionWork is one session's work state in the report; Work.TurnAt is the end of the turn it is about.
type SessionWork struct {
	MachineID string          `json:"machine_id"`
	SessionID string          `json:"session_id"`
	Work      proto.WorkState `json:"work"`
}

// CodeGone is manager.action's refusal of an answer whose question or card is not there any more (answered, withdrawn,
// expired): final, the hub does not keep the answer to try again (any other error it does).
const CodeGone = "gone"

// WorkCardTool is the tool_name of the manager's cards about an agent's turn (among Snapshot.Interactions, answered
// like its other questions with manager.action): the lists put an agent's own question before one of these.
const WorkCardTool = "persona_triage"

// AgentOwnTools are the Persona tools an Agent Persona starts calls without a permission card (#4966,
// docs/integrations/claude-integration.md "Persona tools Agents call without a card"): the agent's own tools (the
// manager's agentToolNames) and a task leader's task_plan and task_review. Each acts only inside Persona, for the
// Agent calling it (the node names the session of its pane), and the manager bounds it: a leader's task_plan adds
// workers only below its own task, its task_review reaches only its direct workers. The node allows them on the
// command line of the Claude Agents it starts (--allowedTools), the Paseo bridge for the Agents it creates
// (integrations/paseo/lib/map.js OWN_TOOLS). Not the phone's tools, the connectors' or dispatch_start / dispatch_read:
// they act outside Persona, or start and read an Agent on another computer, and still ask.
var AgentOwnTools = []string{"report_to_supervisor", "todo_decisions", "todo_decision_add", "todo_bind_thread", "task_plan", "task_review"}

// AgentToolNames are the manager's tools of the agents (not the assistant's conversations), its agentToolNames:
// reporting to the supervisor and the to-do it works on (decisions, thread, repositories and their links). Each acts
// for the agent calling it, so the node names the session of the caller's pane for every one of them (node
// callerTools). One list for both sides: a tool added here cannot reach the hub without its caller (#9089).
var AgentToolNames = []string{"report_to_supervisor", "todo_decisions", "todo_decision_add", "todo_bind_thread",
	"todo_repositories_get", "todo_repositories_set", "todo_repository_links_set", "todo_repository_links_add"}

const (
	// MethodAgentStartParams: AgentStartParams → {params}: an Agent the app starts (POST /api/v1/sessions), with what
	// it runs on filled in and its tab named. An error with CodeBadRequest is the app's to see (400); while the manager
	// is not connected the hub starts it as the app asked.
	MethodAgentStartParams = "agents.start_params"
	// MethodAgentResumeParams: AgentResumeParams → {params}: a message that resumes an Agent nothing says the launcher
	// of (started outside Persona), with the launcher and model it is to run on.
	MethodAgentResumeParams = "agents.resume_params"
	// MethodAgentTabName: {prompt} → {name}: a herdr tab's name for an Agent started with prompt ("" when none).
	MethodAgentTabName = "agents.tab_name"
	// EventAgentTabNameStarted: AgentTabNameStart, one way: a start has its session, so its model name can finish off the caller's path.
	EventAgentTabNameStarted = "agents.tab_name_started"
	// EventSessionSeen: SessionSeen, one way: the user looked at a session in an app.
	EventSessionSeen = "session.seen"
)

// AgentTabNameMethod asks the manager for an Agent tab name.
var AgentTabNameMethod = buswire.Method[AgentTabNameParams, AgentTabNameResult]{Name: MethodAgentTabName}

// AgentTabNameParams carries the prompt used to name the tab.
type AgentTabNameParams struct {
	Prompt string `json:"prompt"`
}

// AgentTabNameResult carries the tab name, which can be empty.
type AgentTabNameResult struct {
	Name string `json:"name"`
}

// AgentStartParams is agents.start_params's params: Params are POST /api/v1/sessions's (the node's session.start);
// NameFrom is what the tab is named from now ("": not now: it has a name, resumes a session, or is named later). Lang
// is the app's language, the one a refusal is said in ("": the user's; an older hub sends none).
type AgentStartParams struct {
	MachineID string         `json:"machine_id"`
	Params    map[string]any `json:"params"`
	NameFrom  string         `json:"name_from,omitempty"`
	Lang      string         `json:"lang,omitempty"`
}

// AgentTabNameStart is agents.tab_name_started's params: the started session and the placeholder its tab still has to
// carry before the late automatic name may replace it. NameFrom is the prompt or name hint, without project context.
type AgentTabNameStart struct {
	MachineID       AgentMachineID `json:"machine_id"`
	SessionID       AgentSessionID `json:"session_id"`
	TabID           AgentTabID     `json:"tab_id"`
	ExpectedTabName string         `json:"expected_tab_name"`
	NameFrom        string         `json:"name_from"`
}

// The distinct ID types keep a late rename from accidentally crossing computers, sessions and terminal tabs. They are
// named strings only at this wire boundary; their JSON bytes are unchanged.
type (
	AgentMachineID string
	AgentSessionID string
	AgentTabID     string
)

// AgentResumeParams is agents.resume_params's params: Params are the message's (the node's session.send; the hub
// sends none and puts the answer's over its own: a message may carry pictures).
type AgentResumeParams struct {
	MachineID string         `json:"machine_id"`
	SessionID string         `json:"session_id"`
	Params    map[string]any `json:"params"`
}

// SessionSeen is session.seen's params.
type SessionSeen struct {
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	At        time.Time `json:"at"`
}

// agentRoutes are the Agents business's routes the manager answers (agents_routes.go there): its switches, its
// numbers, renaming the tabs nobody named.
var agentRoutes = []string{
	"GET /api/v1/agents/display-settings",
	"PUT /api/v1/agents/display-settings",
	"GET /api/v1/agents/botmux-owner-candidates",
	"GET /api/v1/agents/triage",
	"PUT /api/v1/agents/triage",
	"GET /api/v1/agents/work/stats",
	"POST /api/v1/tabs/autoname",
	"POST /api/v1/sessions/{mid}/{sid}/review",
	"GET /api/v1/sessions/{mid}/{sid}/review",
}

func init() { Routes = append(Routes, agentRoutes...) }
