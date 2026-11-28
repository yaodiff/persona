package proto

import (
	"encoding/json"
	"math"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/wireids"
)

// Claude Code's effort levels and permission modes (node/modes.go sets them; the hub checks a scheduled task's).
var (
	EffortLevels    = []string{"low", "medium", "high", "xhigh", "max"}
	PermissionModes = []string{"default", "acceptEdits", "plan", "auto", "bypassPermissions"}
)

// PermissionModeSet is a set of Persona's permission modes (PermissionModes), on the wire the list of their ids in that
// order: a launcher's and a session's permission_modes (Launcher.PermissionModes, SessionCapabilities.PermissionModes,
// #8261). A byte, so the structs carrying it stay comparable and no larger (SessionCapabilities is allocated for every
// runtime row, TestRuntimeSessionsPushAllocatesOneList). Reading it keeps the ids it knows and drops anything else,
// whatever a runtime sent.
type PermissionModeSet uint8

// PermissionModeSetOf is the set of the ids among PermissionModes.
func PermissionModeSetOf(ids ...string) PermissionModeSet {
	var s PermissionModeSet
	for _, id := range ids {
		for i, m := range PermissionModes {
			if id == m {
				s |= 1 << i
			}
		}
	}
	return s
}

// Has is whether mode is in the set.
func (s PermissionModeSet) Has(mode string) bool {
	return s != 0 && s&PermissionModeSetOf(mode) != 0
}

// List is the set's ids in PermissionModes' order.
func (s PermissionModeSet) List() []string {
	out := []string{}
	for i, m := range PermissionModes {
		if s&(1<<i) != 0 {
			out = append(out, m)
		}
	}
	return out
}

func (s PermissionModeSet) MarshalJSON() ([]byte, error) { return json.Marshal(s.List()) }

func (s *PermissionModeSet) UnmarshalJSON(b []byte) error {
	var ids []string
	if json.Unmarshal(b, &ids) != nil { // not a list of strings: none
		ids = nil
	}
	*s = PermissionModeSetOf(ids...)
	return nil
}

// CodexEffortLevels are Codex's reasoning efforts, weakest first: a codex launcher's model catalog advertises ultra,
// so a start on one takes it too (node/codex_effort.go). Claude Code's list stops at max.
var CodexEffortLevels = []string{"minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

// EffortWords are the thinking levels gateways and APIs name, weakest first: Claude Code's (EffortLevels), the APIs'
// none and minimal below them and Codex's ultra above. Every other list of levels is a part of it in this order.
var EffortWords = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

// ProjectEfforts is levels (EffortWords) in the words vocab takes, weakest first and each once: a level vocab names
// stays as it is, minimal becomes low and ultra max where vocab lacks them (cindy's clampEffortForClaude), and
// anything else vocab lacks (none, a word outside EffortWords) is left out.
func ProjectEfforts(levels, vocab []string) []string {
	var out []string
	for _, w := range EffortWords {
		for _, e := range levels {
			p := e
			if !slices.Contains(vocab, p) {
				switch p {
				case "minimal":
					p = "low"
				case "ultra":
					p = "max"
				}
			}
			if p == w && slices.Contains(vocab, p) && !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// ModeDefault applies the ordinary permission rules.
const ModeDefault = "default"

// ModeBypass skips every permission prompt; a headless claude only takes it at start (--permission-mode).
const ModeBypass = "bypassPermissions"

// ModePlan permits investigation while requiring approval for changes and other non-read-only tools.
const ModePlan = "plan"

// ClaudeDefaultModeSetting is Claude Code's permissions.defaultMode, a key of Settings › Claude Code (node
// claudeconfig.go): the mode a session started without --permission-mode runs in. The hub reads it for a planned
// task with no mode of its own (assistant/tasks_permission.go).
const ClaudeDefaultModeSetting = "permissions.defaultMode"

// Session statuses mirror herdr agent states, plus offline for transcripts with no live agent.
const (
	StatusIdle    = "idle"
	StatusWorking = "working"
	StatusBlocked = "blocked"
	StatusDone    = "done"
	StatusUnknown = "unknown"
	StatusOffline = "offline"
	// StatusInterrupted: it was running in a pane when the node restarted, which took herdr's panes with it. The
	// conversation is still resumable (session.start with resume) but nothing carries it on until the user says so
	// (#1917, interrupted.go in the node).
	StatusInterrupted = "interrupted"
)

// LauncherRefreshSendWait leaves time for a terminal Claude send to check the provider, quit and resume the
// conversation in place before typing. The node bounds preparation and the restart separately.
const LauncherRefreshSendWait = 4 * time.Minute

// CanRefreshTerminalLauncher identifies sends that may need the longer wait. Only the node knows which process
// and launcher configuration are actually in the pane.
func (s *Session) CanRefreshTerminalLauncher() bool {
	return s.PaneID != "" && !s.Assistant && s.SourceKind == "" && (s.Agent == "claude" || s.Agent == "")
}

// Unified agent states (docs/protocol/agent-states.md).
const (
	StateNeedsYou = "needs_you" // waiting for the user: a question, plan, permission or any TUI dialog
	StateWorking  = "working"
	StateUnread   = "unread" // finished, result not looked at yet
	StateIdle     = "idle"   // finished and seen, ready for input
	StateOffline  = "offline"
	StateUnknown  = "unknown"
)

// CodedStartErrorMax is how many runes of a coded refusal's start_error a list row or a task keeps where a summary would
// be clipped shorter: the hub's sentence for the code (StartErrorCode) is bounded, and its remedy comes last (#1642).
const CodedStartErrorMax = 600

// TitleMax is the most runes a session's title has in a list of sessions or a session frame (#16343): the node, the
// manager and the hub cut a longer one (transcript.ListTitle), and the list answers' rows were already cut to it. A
// title that long is an opening prompt an agent stored as its title (a Codex thread name, a runtime's own title),
// which no row shows; the transcript keeps the prompt whole.
const TitleMax = 256

// ToolLoop is a node-detected repetition. It contains no tool input or output.
// Since identifies the start of this repetition within the current turn.
type ToolLoop struct {
	Tool  string    `json:"tool"`
	Count int       `json:"count"`
	Since time.Time `json:"since"`
}

// Session is one row of a computer's list. Rows are copied one at a time all over the hub, the manager and the node,
// so the size matters: its bools sit next to other bools, several to an 8-byte word, which keeps a row inside Go's
// 1,536-byte size class (TestSessionFitsItsSizeClass). A bool between two wider fields takes a word to itself.
type Session struct {
	ToolLoop *ToolLoop `json:"tool_loop,omitempty"`
	// Manager is the manager it belongs to (docs/assistant/managers.md, "Ownership"); the hub stamps it on what it sends the apps.
	Manager          string               `json:"manager,omitempty"`
	MachineID        string               `json:"machine_id"`
	SessionID        string               `json:"session_id"`
	SourceKind       string               `json:"source_kind,omitempty"`
	SourceThreadID   string               `json:"source_thread_id,omitempty"`
	SourceInstanceID string               `json:"source_instance_id,omitempty"`
	ParentThreadID   string               `json:"parent_thread_id,omitempty"`
	ParentSessionID  string               `json:"parent_session_id,omitempty"` // The session that started it: a Codex parent's public ID (scoped to the Codex home on this machine), or a runtime's own session (#4494, #4495).
	RootSessionID    string               `json:"root_session_id,omitempty"`
	ReadOnlyReason   string               `json:"read_only_reason,omitempty"`
	Capabilities     *SessionCapabilities `json:"capabilities,omitempty"`
	IsSubagent       bool                 `json:"is_subagent,omitempty"`
	Attached         bool                 `json:"attached,omitempty"`
	// CanFork: "Branch to new chat" works for it (node/fork.go); older nodes
	// never say so, and the clients hide it.
	CanFork          bool `json:"can_fork,omitempty"`
	StartedByPersona bool `json:"started_by_persona"`
	Assistant        bool `json:"assistant"` // runs in the assistant directory (the app's "Chat" tab)
	// Archived: a retained conversation moved out of the normal list. Assistant
	// conversations and Claude/Codex Agents are managed by their owning manager.
	Archived    bool `json:"archived,omitempty"`
	TitleManual bool `json:"title_manual,omitempty"` // A runtime record proves the user chose a title.
	// BypassAvailable sits here, beside the bools before it, not with PermissionMode: alone between two wider fields it
	// took an 8-byte word, and a Session must stay at most 1,528 bytes (see BackgroundWorking below); PendingSwitch got
	// that word (#5035).
	// BypassAvailable: this Claude Code can be switched to bypassPermissions (Allow All) while it runs, since it was
	// started with --dangerously-skip-permissions, --allow-dangerously-skip-permissions or --permission-mode
	// bypassPermissions, which put that mode in its shift+tab cycle (#1379). The node reads it off the claude process its
	// hooks run under; false while unknown (no hook yet, a node too old to say).
	BypassAvailable bool `json:"bypass_available,omitempty"`
	// Tags are the user-defined labels of an assistant conversation (assistant/conversation_tags.go), set by the user
	// or the assistant. A pointer so a row without tags adds no size to Session's size class (session_size_test.go);
	// nil and an empty slice both mean "no tags" and are omitted on the wire.
	Tags  *[]string `json:"tags,omitempty"`
	Title string    `json:"title"`
	Cwd   string    `json:"cwd"`
	// WorkspaceCwd is the node-verified physical directory for Cwd. Consumers
	// matching workspace bindings prefer it and fall back to Cwd on older nodes.
	WorkspaceCwd string `json:"workspace_cwd,omitempty"`
	// RepoRoot is the main checkout of the repository Cwd belongs to, when the host knows it (#2840: the Paseo bridge,
	// whose worktrees live under Paseo's own folder): "By repo" groups by it; without it the clients infer the repository
	// from Cwd's path.
	RepoRoot string `json:"repo_root,omitempty"`
	// Worktree is the top of the git working tree Cwd is in (RepoRoot itself for the main checkout, a linked worktree's
	// own folder otherwise) and Branch the branch checked out there ("" when detached or unknown). The node reads both,
	// and RepoRoot when no host names it, off the filesystem (node/gitplace.go): the apps list machine → project
	// (RepoRoot) → worktree (Worktree, labelled Branch) → sessions, as Orca does (#1805). RepoRoot and Worktree are
	// physical directories (symbolic links resolved, #4492), however Cwd is written and however a host wrote the
	// RepoRoot it named: the apps compare them as strings.
	Worktree    string `json:"worktree,omitempty"`
	Branch      string `json:"branch,omitempty"`
	Status      string `json:"status"`
	PaneID      string `json:"pane_id,omitempty"`
	TabID       string `json:"tab_id,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	AgentName   string `json:"agent_name,omitempty"`
	// CodexRoute: how Persona reaches a Codex in a terminal (docs/integrations/codex.md "Sessions on the daemon") — "app_server" (joined on
	// the shared app-server: questions, approvals and messages go through its protocol) or "terminal" (its rollout and
	// screen, keys and typing); CodexRouteNote says why when it is the terminal.
	CodexRoute     string `json:"codex_route,omitempty"`
	CodexRouteNote string `json:"codex_route_note,omitempty"`
	// Agent is the coding agent in the pane: "claude" (also when empty, from older nodes) or "codex"; for a session a
	// runtime lists (docs/integrations/runtimes.md), the agent it says it runs ("dsh").
	Agent string `json:"agent,omitempty"`
	// AgentLabel is what the apps call the agent when they do not know it themselves: a runtime's own name ("DSH").
	// Empty for the agents the apps know (Claude Code, Codex, a compatible CLI).
	AgentLabel string `json:"agent_label,omitempty"`
	// AgentSurface is the client the agent runs in: "desktop" (Claude's or Codex's desktop app) or "cli" (its command
	// line); empty when its record does not say (an SDK, an older version, a runtime). The apps badge the agent's mark
	// with </> for "cli" (#6390).
	AgentSurface string `json:"agent_surface,omitempty"`
	Launcher     string `json:"launcher,omitempty"`
	Model        string `json:"model,omitempty"`
	// RequestedModel is the last model passed to a Persona-managed process.
	// Nil means an older node or an observed session; an empty value means launcher default.
	// Model remains the transcript-reported name, which a gateway may rewrite.
	RequestedModel *string `json:"requested_model,omitempty"`
	// PermissionMode and Effort are what Claude Code runs with now: default / acceptEdits / plan / auto /
	// bypassPermissions, and low … max. Empty while unknown (before the first turn, Codex). A Codex thread the phone
	// drives through its app-server has an Effort too (Codex's levels, see Efforts) but never a PermissionMode.
	PermissionMode string `json:"permission_mode,omitempty"`
	Effort         string `json:"effort,omitempty"`
	// PendingEffort is a terminal change waiting for this Claude Code turn to end. If the attempt fails, the
	// target stays visible and messages remain held until the user chooses an effort again. A pointer keeps
	// optional effort metadata within the row size budget.
	PendingEffort      *string `json:"pending_effort,omitempty"`
	EffortChangeFailed bool    `json:"effort_change_failed,omitempty"`
	// ContextRestartRecommended: the latest three measured requests each used over 300,000 input
	// tokens since the last compaction. Omitted when false or the runtime has no request-level evidence.
	// Kept with EffortChangeFailed so it fits the row's allocation size class.
	ContextRestartRecommended bool `json:"context_restart_recommended,omitempty"`
	// OffLauncher: Persona started this Claude Code Agent on Launcher, but the claude that runs it now was not started
	// by Persona's command (it lacks Persona's --mcp-config): herdr's bare `claude --resume <id>` after a restart on a
	// herdr older than 0.9.2, or a resume typed by hand (#11792). Launcher's settings, model, MCP and hooks do not
	// apply to it, and a launcher switch to Launcher itself brings it back there. Beside EffortChangeFailed, in its
	// padding: a Session stays in its allocation size class.
	OffLauncher bool `json:"off_launcher,omitempty"`
	// NativeTurnVerified says the AGY terminal's working or done status is backed by its current transcript and lifecycle hook.
	NativeTurnVerified bool `json:"native_turn_verified,omitempty"`
	// LogicalPlaceholder is manager-internal provenance: this row stands in for a logical conversation whose active
	// physical segment is not listed. Its Assistant identity comes from the logical root, not from a physical Cwd.
	// Kept with NativeTurnVerified beside BackgroundStalledSince so Session stays in its allocation size class.
	LogicalPlaceholder bool `json:"-"`
	// BackgroundStalledSince (#13744, #16418): the time since which this session's background commands have used no CPU
	// and written no output, set while the turn is over once that lasted the node's grace (ten minutes for a known
	// worker such as a build, tests or a download; three for a process it does not know or a command moved from the
	// foreground; two for shells gone from under the live claude). Beside a subagent, workflow or Monitor, state stays
	// working and this only tells a client the work looks stuck; when stalled background commands are all that is left,
	// the manager leaves the finished turn's own state and the task's turn settles. Zero (omitted) otherwise. Whole
	// seconds in a uint32, in the padding before Ultracode: a Session stays in its allocation size class
	// (TestSessionFitsItsSizeClass).
	BackgroundStalledSince UnixTime32 `json:"background_stalled_since,omitempty"`
	// Ultracode is Claude Code's independent session-only workflow switch: nil means unknown.
	// UltracodePending (beside AsksRead) means a requested change waits for an idle input box; LostAt asks the client
	// to offer reopening after a resumed process reset an enabled switch. Reopening or dismissing the notice clears
	// LostAt.
	Ultracode       *bool      `json:"ultracode,omitempty"`
	UltracodeNote   string     `json:"ultracode_note,omitempty"`
	UltracodeLostAt *time.Time `json:"ultracode_lost_at,omitempty"`
	// PendingSwitch is a switch of this running Claude Code Agent to another launcher (model provider) of the same
	// engine that the user picked and that waits for the next message (#5035, docs/integrations/claude-integration.md
	// "Switching a running Agent to another provider"): that message quits the agent and resumes the same conversation
	// on the new launcher in the same terminal first. Nil when none waits.
	PendingSwitch *PendingSwitch `json:"pending_switch,omitempty"`
	// ContextTokens is what the model saw in the agent's latest reply (Claude: input + cache read + cache creation;
	// Codex: the last token_count's input) and ContextWindow the model's window, both read off the transcript tail the
	// node already reads (#2430). ContextTokens is omitted while unknown (no reply yet); ContextWindow too, when the
	// node cannot tell the model's window. The apps show used / window as the context bar.
	// Both are int32, next to each other, to leave Session the word Hierarchy takes (TestSessionFitsItsSizeClass): the
	// largest windows are a few million tokens (Tokens32 saturates anything bigger).
	ContextTokens int32 `json:"context_tokens,omitempty"`
	ContextWindow int32 `json:"context_window,omitempty"`
	// Cumulative generated output lives in SessionHierarchy; see OutputTokensValue / SetOutputTokens.
	// Efforts are the levels session.modes takes for Effort when they are not Claude Code's (which the apps know): a
	// Codex thread the phone drives through its app-server (node/codex_effort.go), its model's levels weakest first; a
	// runtime's row of an agent that is not Claude Code's, as the runtime lists them (Paseo's, #8133; the node drops
	// them where the runtime takes no session.modes). Speed has its own advertised choices in ServiceTiers. Empty for
	// every other session: Claude's and the assistant's keep the apps' own list, the rest cannot change it from the phone.
	Efforts []string `json:"efforts,omitempty"`
	// ServiceTier is the acknowledged native speed, omitted while unknown.
	ServiceTier  string        `json:"service_tier,omitempty"`
	ServiceTiers []ServiceTier `json:"service_tiers,omitempty"`
	// Space is "health" for a session in the health space (docs/assistant/health.md), or empty otherwise.
	// The node can preserve it from the start directory even when a live pane's working directory moves.
	Space string `json:"space,omitempty"`
	// ModelPick is the launcher / model / effort picked for one of the assistant's conversations, which its next
	// messages run on (#5033, docs/assistant/model-config.md "A conversation's own model"); nil follows Model
	// configuration › Main chat (the main conversation, a scheduled task's) or what the conversation runs. Set by the
	// manager.
	ModelPick *ModelPick `json:"model_pick,omitempty"`
	// ArchiveChangedAt is the manager's last explicit archive/restore visibility change; nil when there was none. The
	// hub uses it only to order its legacy delete tombstones against that report. A pointer, not a time.Time: the 16
	// bytes it saves keep Session in its size class (session_size_test.go).
	ArchiveChangedAt *time.Time `json:"archive_changed_at,omitempty"`
	// NativeArchive is whether the tool that owns an archived (or just restored) agent session has it the same way
	// in its own archive (Codex's archived threads): only on GET /api/v1/sessions/archived rows, nil when the manager
	// never asked the tool.
	NativeArchive *NativeArchive `json:"native_archive,omitempty"`
	// Origin is who opened an assistant conversation and OriginRef what of (docs/protocol/protocol.md"Assistant conversation origins"):
	// user, channel, inbox, task, spawned, lark_agent, extension; "" is the user's. Set by the manager's fleet from
	// the assistant's snapshot (internal/assistant/fleet.go).
	Origin    string `json:"origin,omitempty"`
	OriginRef string `json:"origin_ref,omitempty"`
	// OpenedBy is the extension whose token opened an assistant conversation, by id (docs/protocol/protocol.md "Origin of
	// assistant chats"): noted when it opens, never backfilled; "" for any other. Set like Origin.
	OpenedBy string `json:"opened_by,omitempty"`
	// Compaction is the last compaction the user asked for in an assistant conversation (POST …/compact), kept by the
	// node until the next one: running, then done or failed (docs/assistant/assistant.md "Manual compaction").
	Compaction *Compaction `json:"compaction,omitempty"`
	// Channels are the channels messages came into this conversation from, newest first ("wechat", "feishu"):
	// the conversation list draws the latest's icon (docs/extensions/channels.md"Source labels"). Set by the manager's fleet
	// (internal/assistant/fleet.go).
	Channels []string `json:"channels,omitempty"`
	// Space / slot generalize where an agent lives in its host tool: for herdr a workspace and a tab
	// (docs/protocol/agent-states.md). Agents sharing a space are shown together as one collaborating group.
	SpaceKind string `json:"space_kind,omitempty"` // herdr
	SpaceID   string `json:"space_id,omitempty"`
	SpaceName string `json:"space_name,omitempty"`
	// SpaceIndex and SlotIndex are small host-side ordinal numbers (a workspace's and a tab's place), so int32 is wide
	// enough; keeping the two four-byte fields in one word leaves room in Session's size class (session_size_test.go).
	SpaceIndex int32 `json:"space_index,omitempty"`
	SlotIndex  int32 `json:"slot_index,omitempty"`
	// ParentSpaceID is the space this one is nested under in its host, same kind and machine (Orca: a child
	// worktree's parent, #2073); empty for a top-level space. Clients that know it fold the child under its parent.
	ParentSpaceID string `json:"parent_space_id,omitempty"`
	SlotID        string `json:"slot_id,omitempty"`
	SlotName      string `json:"slot_name,omitempty"`
	// PaneIndex is the pane's 0-based place in its slot by layout, top left first: 0 is the pane the others were split
	// from (the group's "Initiator" when an agent runs there). Absent (nil) when the host does not say where its panes sit
	// (older nodes, hosts without layouts, a pane missing from the layout). A pointer so that 0 is sent.
	PaneIndex *int `json:"pane_index,omitempty"`
	// State is the unified agent state the apps render (needs_you, working, unread, idle, offline, unknown),
	// computed by the hub from the host status, pending interactions and whether the user has seen the result.
	State string `json:"state"`
	// BackgroundWorking: State is working only because of background work (#2935): its turn ended while its
	// background commands, subagents, Monitors or workflows still run. Clients say "Working (background)". Set by the hub.
	BackgroundWorking bool `json:"background_working,omitempty"`
	// BackgroundWaiting: BackgroundWorking, and all that background work only waits (#6045): its background commands
	// sleep, loop on a check or watch something, using next to no CPU; no subagent or workflow runs. Clients show
	// "Waiting in the background" apart from Working (lighter, not in the Working count); State stays working, so an
	// older app shows Working. The node says whether its background commands wait; the manager keeps it only so.
	BackgroundWaiting bool `json:"background_waiting,omitempty"`
	// The fields from BackgroundWorking to QueuedRelayed sit together, not beside the ones they go with: with Go's
	// 8-byte allocation header a Session must stay at most 1,528 bytes to keep its 1,536-byte size class, and every
	// row the manager copies out of a node's list pays the next class's 256 bytes otherwise (internal/assistant's
	// TestSessionReadersCopyNoList, #2528). Mind the padding when adding a field.
	// WokeByTask: the latest turn was started by a background task reporting back, not by the user; finishing
	// it sends no push (the user did not ask for anything).
	WokeByTask bool `json:"woke_by_task,omitempty"`
	// Starting: Persona is opening its tab and waiting for claude (node/starting.go); messages sent now go in once it is
	// ready.
	Starting bool `json:"starting,omitempty"`
	// Quiet: the agent works (or worked) for a quiet task, one the main conversation opened while the user wanted
	// those told only there (#1731): set by the hub, also once the task is done. Clients leave it out of task
	// notifications and global badges; the Agents page shows it as always.
	Quiet bool `json:"quiet,omitempty"`
	// AwaitingFirstMessage: a Codex Persona opened with no first message (node/codex_waiting.go): Codex runs in its
	// terminal but has no thread until it gets one, so this row is keyed by an id of the node's, not the thread's.
	// It takes the first message (text only) and opens its terminal; once the thread exists the row goes and the
	// thread's comes, in the same pane (docs/integrations/codex.md "Starting without a first message").
	AwaitingFirstMessage bool `json:"awaiting_first_message,omitempty"`
	// QueuedRelayed: the last this many of Queued are messages from other conversations that an assistant
	// conversation's manager holds until its turn ends (docs/assistant/assistant.md "Queue while running"); the apps
	// show them but take back, delete, move and send now only the others (#4342). A uint16 here sits in the padding
	// before SeenAt, so a Session stays the size the allocation budgets measured.
	QueuedRelayed uint16    `json:"queued_relayed,omitempty"`
	SeenAt        time.Time `json:"seen_at,omitempty"`
	// StateSince is when the agent entered its current state, as the hub saw it (the hub restarting resets it):
	// lists sort "Working" by it, since updated_at moves on every step.
	StateSince time.Time `json:"state_since,omitempty"`
	// Queued is input typed while the agent works, waiting to be taken (shown above the composer, not in the chat).
	Queued []string `json:"queued,omitempty"`
	// Undelivered are the owner's messages to an assistant conversation that could not go in (its process did not
	// start, its computer was away) and that its manager keeps and sends again by itself (#10194, docs/assistant/
	// assistant.md "Messages that could not go in"); oldest first. Shown in the chat as the owner's own, marked not
	// delivered, on every client. Behind a pointer: a Session stays in its allocation size class (see QueuedRelayed).
	Undelivered *[]UndeliveredMessage `json:"undelivered,omitempty"`
	// Background lists commands / agents Claude started in the background that have not reported back yet
	// (the TUI's "2 shells still running").
	Background []BackgroundTask `json:"background,omitempty"`
	// Subagents the agent launched (Agent / Task tool); each one's transcript is the session "<id>@<agent id>".
	Subagents []Subagent `json:"subagents,omitempty"`
	// Workflows are the Claude Code workflow runs (the Workflow tool) the agent launched in the last day, newest first:
	// a running one is background work, as Background is (a running one is in Background too, kind "workflow").
	Workflows []Workflow `json:"workflows,omitempty"`
	// Loop is the /loop driving this agent, when there is one (it wakes itself up on a schedule).
	Loop *LoopInfo `json:"loop,omitempty"`
	// Goal is the goal the agent works toward, when one is set (Claude Code's /goal, a Codex thread goal; docs/integrations/agent-goal.md).
	Goal *GoalInfo `json:"goal,omitempty"`
	// Plan is the agent's own task list (Claude Code's TodoWrite, or TaskCreate / TaskUpdate), when it keeps one: how far
	// along it is and the step in progress (docs/integrations/agent-goal.md "Plan"). Absent for Codex and for runtimes
	// whose conversation is not a Claude transcript on the node.
	Plan *PlanInfo `json:"plan,omitempty"`
	// Ports are the TCP ports the agent's processes listen on that answer HTTP (docs/operations/ports.md): the dev servers
	// it started, which the apps open through the hub.
	Ports     []Port    `json:"ports,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	LastText  string    `json:"last_text,omitempty"`
	// Asks is what the agent's last reply asks the user, when its closing words put a question or a decision to
	// them (transcript.AskOf, #5096): read by the node off the whole reply, since LastText is only its beginning.
	Asks string `json:"asks,omitempty"`
	// UltracodePending: a requested switch waits for an idle input box (grouped here to keep rows compact).
	UltracodePending bool `json:"ultracode_pending,omitempty"`
	// AsksRead: the node read Asks off the reply (an empty Asks then means it asks nothing, or the user answered it);
	// without it (an older node, a host that does not say) the hub reads the list's last words when they are whole.
	AsksRead bool `json:"asks_read,omitempty"`
	// AwaitsReply: the agent stopped on a question to the user nobody has answered yet (docs/protocol/agent-states.md "Awaiting your reply",
	// #5096) — it asks (Asks, or the triage says it waits on the user) and has no card, error or background work. Set by
	// the hub on a stopped session (Unread or Seen); an Seen one is listed as Awaiting Reply, not folded with the idle ones.
	AwaitsReply bool `json:"awaits_reply,omitempty"`
	// TurnErrorResumes: the CLI said it goes on by itself once the usage limit behind TurnError resets (Claude Code's
	// "Usage limit reached · continuing automatically at …") and nothing cancelled that since. Typing into the session
	// cancels it, so nothing is sent to it while it waits.
	TurnErrorResumes bool `json:"turn_error_resumes,omitempty"`
	// TurnErrorAfterWork: the turn did some work (a reply, a tool call) before TurnError ended it; false when the error
	// was all it did (a continuation the limit turned down again).
	TurnErrorAfterWork bool `json:"turn_error_after_work,omitempty"`
	// Known transcript provenance, packed with flags; its JSON remains a string.
	TitleSource TitleSource `json:"title_source,omitempty"`
	// SuggestionRisky: Suggestion names a command, a path, a link, a secret-looking string or a risky action (delete,
	// push, deploy, send…): the apps fill it in but offer no one-tap send (#4644). Set by the manager; kept beside
	// the flags above, where it fits in their padding: a larger Session costs every copy of a list.
	SuggestionRisky bool `json:"suggestion_risky,omitempty"`
	// BypassPending: Allow All was picked for this Claude Code while it cannot reach it with shift+tab (BypassAvailable
	// false), so the node restarts it in that mode once its turn is over: it quits the agent and resumes the same
	// conversation in the same terminal with --permission-mode bypassPermissions (node/mode_restart.go, #6476). Until
	// then PermissionMode is the mode it still runs in. Kept here, in the flags' padding, as SuggestionRisky is.
	BypassPending bool `json:"bypass_pending,omitempty"`
	// TurnError: the agent's latest turn ended in a model API error (the connection lost mid-response, overloaded,
	// rate limited, the model service down…). The node sets it when the agent's own record says the turn failed
	// (Codex: a rollout's task_complete.error, an app-server's failed turn, #2185); otherwise the hub reads it off the
	// last words, as Claude Code writes them ("API Error: …"). The hub keeps it only while the agent is stopped: empty
	// while it works or when the turn ended normally. The task it works for is interrupted, not done
	// (docs/assistant/todos.md "State calculation rules", #1516).
	TurnError string `json:"turn_error,omitempty"`
	// TurnErrorKind is what the agent's record says of TurnError, when it says (Claude Code's error field, Codex's
	// error kinds): TurnErrorTransient (worth a "Continue"), TurnErrorUser (the user must act), TurnErrorUsageLimit
	// (a subscription's usage limit, until it resets), or TurnErrorUnrecoverable (the conversation is invalid).
	// The hub's permanent-error classification overrides a generic runtime kind; empty: read off the text.
	// Or the hub's own word on a task Agent's turn that had no output at all
	// (SilentTurnKind: TurnErrorNoOutput, TurnErrorStopped), with TurnError
	// the sentence for it in the user's language (#10187).
	TurnErrorKind string `json:"turn_error_kind,omitempty"`
	// TurnErrorResetsAt is when the subscription usage limit that ended the turn resets (TurnErrorKind
	// TurnErrorUsageLimit): what the agent's record says (Claude Code's quotaLimits.resetsAt, the full window of a Codex
	// rollout's own rate limits), else what the hub reads off Claude Code's words when they name a time zone. Nil: not
	// known, and nothing waits for it. Only a subscription's limit has one: a gateway's or an API key's 429 never does.
	TurnErrorResetsAt *time.Time `json:"turn_error_resets_at,omitempty"`
	// TurnErrorCode says what is wrong when the hub or node can tell from TurnError (docs/assistant/onboarding.md 5.2):
	// one of the TurnError* values below, for a client fix card (sign in, the key, the model) or for upstream-stall
	// visibility (TurnErrorUpstreamUnavailable). Empty: not known.
	TurnErrorCode string `json:"turn_error_code,omitempty"`
	// ModelFallback is the refusal fallback still active in this session, as the runtime recorded it.
	ModelFallback *ModelRefusal `json:"model_fallback,omitempty"`
	// ModelRestore: "back to the configured model" was asked for this session's refusal fallback (#2528) and no newer
	// model evidence has come since. Not proof the configured model runs: only a newer reply says that.
	ModelRestore *ModelRestore `json:"model_restore,omitempty"`
	// Work is where the session stands in its work (docs/assistant/agent-work.md): working, blocked on a named precondition, or
	// done, with a line saying what it waits for or what it concluded. Set by the hub; absent before the hub has
	// triaged a stopped session's turn.
	Work *WorkState `json:"work,omitempty"`
	// Activity says what the agent is doing when its latest step is a tool call no text came after ("running tests",
	// "reading tasks.go", "waiting for CI", transcript.Activity), and ActivityAt when that call started: while an agent only runs
	// tools its last_text stays put for minutes, and a task card showing it looked stuck. Empty once it writes text.
	Activity   string     `json:"activity,omitempty"`
	ActivityAt *time.Time `json:"activity_at,omitempty"`
	// Rev changes whenever the transcript does (its size): the app pulls new items for every session whose rev
	// differs from what it has, without opening it. Changes travel as the light `revs` event.
	Rev                 int64 `json:"rev,omitempty"`
	PendingInteractions int   `json:"pending_interactions"`
	// Suggestion is the next message Claude Code proposes after a turn (grey in its empty input box), read off the
	// screen; gone once the agent works again or a message is sent.
	Suggestion string `json:"suggestion,omitempty"`
	// StartError: the start failed (shown in the chat for a while). StartErrorCode is the start's coded refusal
	// when it was one (claude_missing, launcher_missing…: the code a synchronous start is answered 409 with), with
	// StartErrorArgs, the refusal's details (launcher, where, missing, pane_shell…, docs/protocol/protocol.md "Error body"):
	// the hub writes StartError as the sentence for that code in the user's language, and a client offers its remedy.
	StartError     string            `json:"start_error,omitempty"`
	StartErrorCode string            `json:"start_error_code,omitempty"`
	StartErrorArgs map[string]string `json:"start_error_args,omitempty"`
	// Todo is the active (uncompleted, unreleased) todo this session is linked to (assistant/todos.go).
	Todo *TodoRef `json:"todo,omitempty"`
	// SpawnFrom is persona_from reported by node for todo derivation (#762), stripped by hub before client push.
	SpawnFrom string `json:"spawn_from,omitempty"`
	// AttentionReason is why the session waits for the user, in the host's words (a botmux attention's reason).
	AttentionReason string `json:"attention_reason,omitempty"`
	// FailureReason: the host stopped the session's turn on an error it will not retry by itself (a botmux notice),
	// until a message goes to the session. The apps show it with a retry; the session is not waiting for the user.
	FailureReason string `json:"failure_reason,omitempty"`
	// ExternalURL opens the session where it lives outside Persona (a botmux session's Feishu topic).
	ExternalURL string `json:"external_url,omitempty"`
	// Chat, Topic, Dispatched: a botmux session's Feishu chat, its topic's root message and the topics it dispatched
	// (`botmux dispatch`), for the todos to follow (docs/assistant/todos.md "botmux following"); stripped by hub like SpawnFrom.
	Chat       string   `json:"chat,omitempty"`
	Topic      string   `json:"topic,omitempty"`
	Dispatched []string `json:"dispatched,omitempty"`
	// AlsoOpenIn are the other hosts (host kinds: herdr, herdr-core, orca…) the session is live in at the same time
	// (#2576): it is listed and driven in the host that owns it (node/ownership.go); the apps warn of the conflict.
	AlsoOpenIn []string `json:"also_open_in,omitempty"`
	// Host is the engine host the session lives in (#2576): the kind of its pane (herdr, herdr-core, orca, botmux) or
	// paseo for the Paseo runtime's; empty for transcripts with no live host and for other runtimes. The apps label a
	// row with it when the machine enables more than one host.
	Host string `json:"host,omitempty"`
	// Hierarchy is where the session stands among the sessions that started one another, and when it began
	// (docs/protocol/protocol.md "Session hierarchy"); nil for most rows. Its members are written on the row itself
	// (parent_agent_id, folded_under, folded_as, children, started_at, asking_parent_session_id, deleted: sessionWire), so it has
	// no key of its own. Read it with ParentAgentID, FoldedUnder, FoldedAs, Children, StartedAt and
	// AskingParentSessionID and IsDeleted; it is shared by every copy of the row, so it is never written in place:
	// SetParentAgentID, SetFold, SetStartedAt, SetAskingParentSessionID and SetDeleted put a new one.
	Hierarchy *SessionHierarchy `json:"-"`
}

// Session.TurnErrorKind values.
const (
	TurnErrorTransient     = "transient"     // the model service failed for a moment: worth a "Continue"
	TurnErrorUser          = "user"          // sign-in, context window, the model…: the user must act
	TurnErrorUsageLimit    = "usage_limit"   // a subscription's usage limit: it goes on once the limit resets (TurnErrorResetsAt)
	TurnErrorUnrecoverable = "unrecoverable" // this conversation cannot continue; start a new one
	// The hub's word on a task Agent's turn that ended with no reply, tool call or image (#2349, #10187): no error the
	// model service reported, and no "Done" either.
	TurnErrorNoOutput = "no_output" // the model service answered nothing: interrupted, worth a "Continue"
	TurnErrorStopped  = "stopped"   // Persona's own Stop interrupted it (its conversation was stopped): the owner's to go on with
)

// SilentTurnKind: kind is the hub's word on a turn with no output (TurnErrorNoOutput, TurnErrorStopped), not an error
// the model service reported: nothing for Session Triage to ask the user about or to continue by itself.
func SilentTurnKind(kind string) bool { return kind == TurnErrorNoOutput || kind == TurnErrorStopped }

// Session.TurnErrorCode values (docs/assistant/onboarding.md 4.6).
const (
	TurnErrorNotLoggedIn              = "not_logged_in"              // Claude Code on that machine is not signed in
	TurnErrorAuthFailed               = "auth_failed"                // the key or token was refused or has expired
	TurnErrorModelRoute               = "model_route"                // the model name is not one the service or gateway knows
	TurnErrorRateLimited              = "rate_limited"               // too many requests, or a usage limit
	TurnErrorModelFallbackUnavailable = "model_fallback_unavailable" // a refusal selected a model the service cannot use
	TurnErrorCompactionSealed         = "compaction_sealed"          // native context is encrypted to another account
	// TurnErrorToolSearchUnsupported: the gateway refused Claude Code's on-demand tool loading (tool search, #10365). The
	// node now starts that launcher and model with every tool loaded, so a retry works: not the user's to fix.
	TurnErrorToolSearchUnsupported = "tool_search_unsupported"
	// TurnErrorUpstreamUnavailable: the model gateway reported an exhausted upstream account pool, or the session hit >= 2
	// consecutive upstream 5xx turns with no assistant progress in between (#13372). Visibility label: TurnErrorKind stays
	// transient unless another rule marks it.
	TurnErrorUpstreamUnavailable = "upstream_unavailable"
	// TurnErrorImagesTooLarge: accumulated pictures in the conversation exceed the model API's size, count or dimension
	// limit (#13045). Compacting the conversation clears older pictures so the failed turn can be sent again.
	TurnErrorImagesTooLarge = "images_too_large"
)

// Codes a call refuses with when the assistant's conversation cannot start on a machine (docs/assistant/onboarding.md 4.6, 5.2):
// rpc.Error codes the hub answers 409 with, the text in the request's language (apierr.Call).
const (
	CodeNoMachine       = "no_machine"       // no machine to run the main conversation on
	CodeMachinePending  = "machine_pending"  // the machine has not been confirmed as the user's yet
	CodeClaudeMissing   = "claude_missing"   // Claude Code is not installed (or not on the launcher's PATH) there
	CodeLauncherMissing = "launcher_missing" // the launcher (a ~/.zshrc function or alias) is not there
	// CodeTerminalNotReady: the new tab's terminal would not take the start command (herdr's pane_send_failed), or the
	// start line never showed in an Orca tab and never ran (node/startevidence.go, #10588); nothing ran, the tab is
	// closed, a retry may work (node/typedstart.go). 503, retryable.
	CodeTerminalNotReady = "terminal_not_ready"
	// CodeAgentStartStalled: the agent opened but its session never became identifiable. This says nothing about
	// launcher health and must not trigger an automatic retry or a health detour.
	CodeAgentStartStalled = "agent_start_stalled"
	// CodeCodexNotLoggedIn: a Codex start met Codex's sign-in screen (node/codex_start.go): Codex has no login on that
	// computer, so no wait would bring its session up. The launcher's sign-in (launcher health's auth), not a stalled
	// start; the new tab is closed, and someone runs `codex login` on that computer.
	CodeCodexNotLoggedIn = "codex_not_logged_in"
	// CodeShellWaiting: the new tab's shell stopped on a question of its startup files (an update prompt, [Y/n]) before
	// the start command was typed (#2570). Nothing ran; the tab is kept a while for someone to answer (node/shellsettle.go).
	CodeShellWaiting = "shell_waiting"
	// CodeStartLineRejected: the new tab's shell did not take the typed start line, twice (a startup prompt ate its first
	// characters, #2570). Nothing ran; the tab is closed.
	CodeStartLineRejected = "start_line_rejected"
	// CodeDirectoryUntypable: the start's directory has a control character in its name, which a shell's line editor
	// would act on when an engine types the directory (Orca's `cd`, node/hostorca_launch.go). Nothing ran; a refusal of
	// the request, not the launcher's fault.
	CodeDirectoryUntypable = "directory_untypable"
	// CodeAgyCompanionOffline: the start named HostAgy on a computer whose Antigravity companion is not connected
	// (node/starthost.go). Nothing ran. A task start the hub sent there by default starts again in the computer's
	// default host (assistant/tasks_placement.go, #13447); one that named the host is refused.
	CodeAgyCompanionOffline = "agy_companion_offline"
)

// Codes session.modes refuses with (node/modes.go), the text in the request's language (apierr.Call). 409.
const (
	// CodeModesOnComputer: a Claude outside Persona's terminals (Claude's desktop app, another terminal); its mode and
	// effort change only there. Its row's capabilities say modes: false.
	CodeModesOnComputer = "modes_on_computer"
	// CodeModesNotRunning: no process or pane runs the session; a message resumes it.
	CodeModesNotRunning = "modes_not_running"
	// CodeModesUnsupported: an agent in a terminal whose mode, effort and model are its start's (cursor, agy).
	CodeModesUnsupported    = "modes_unsupported"
	CodeEffortChangePending = "effort_change_pending"
	// A Codex on the terminal route, whose model and effort the node types as `/model <model> --reasoning=<level>`
	// (node/codex_terminal_modes.go, #12572). CodeModesCodexVersion: a Codex older than 0.169 started the session, and
	// its /model takes no arguments. CodeModesCodexNoRollout: the session has no rollout yet to confirm a change by.
	// CodeModesCodexRefused: Codex answered the command with a refusal; the message is Codex's own words.
	// CodeModesCodexUnconfirmed: the command went in, and the rollout did not record the change in time (delivered
	// "unknown": it may still have taken).
	CodeModesCodexVersion     = "modes_codex_version"
	CodeModesCodexNoRollout   = "modes_codex_no_rollout"
	CodeModesCodexRefused     = "modes_codex_refused"
	CodeModesCodexUnconfirmed = "modes_codex_unconfirmed"
)

// CodeNotLoaded is what session.items and session.item refuse with when reading the session would start its agent: an
// engine that lets idle agents go (Paseo's closed and archived ones) loads one to answer for its conversation, which
// relaunches its provider's process, so a runtime reads such a session only while it is watched (someone has its chat
// open, docs/integrations/runtimes.md "What to implement", #8132). Nothing was read; the text is in the request's
// language (apierr.Call). 409.
const CodeNotLoaded = "not_loaded"

// NotFoundNoTranscript and NotFoundUnknownSession are why a node's read of a session's history (session.items,
// session.item, session.image, session.visualization) is not_found, its data.reason (node/transcript_files.go
// readErr): a session the node knows with no transcript to read (its file gone, or none ever written), or an id it
// has no record of. The hub words each in the request's language (apierr.Call); a not_found without one (an older
// node, a runtime's own answer, a missing file) keeps the plain summary (#11388).
const (
	NotFoundNoTranscript   = "no_transcript"
	NotFoundUnknownSession = "unknown_session"
)

// NotRunningEngine is the data.reason of a node's not_running refusal of a message to a session that is not live in an
// engine that cannot bring it back from the phone (node/sendsafe.go notRunningRefusal), with data.engine the engine's
// name. The hub words it in the request's language (apierr.Call, rpc-errors.not_running_engine); a not_running without
// it (an older node, a runtime's own answer, a resume that never came up) keeps the node's words (#17044).
const NotRunningEngine = "engine_cannot_reopen"

// CodeTurnOverBackground is session.interrupt's answer when the Escape went to a session whose turn was already over
// and only its background work (a background command, a subagent, a workflow) keeps it Working: nothing was
// interrupted and that work goes on (#10828).
const CodeTurnOverBackground = "turn_over_background"

// CodeAgentStopped is what session.send refuses with for an Agent that has stopped and is read-only (node/stopped.go,
// #4754): nothing was typed, and it takes no message until it is resumed (the user's Resume on its terminal page).
const CodeAgentStopped = "agent_stopped"

// CodeResumeUnknown is what session.start refuses a resume with when the computer has nothing of the conversation to
// resume (no transcript, start or record of the id), and, for a resume carrying resume_outside_health, also when what
// it has started or was recorded in a health space or an Agent folder kept out of Persona (#10941): a new
// conversation would otherwise be listed under the id of another. Nothing was started. 409.
const CodeResumeUnknown = "resume_unknown"

// PendingSwitch is Session.PendingSwitch: the launcher the Agent moves to, the model and effort to run it with ("":
// the launcher's own default model; the effort it runs with now), and when the user picked it. Agent is the engine
// ("claude" or "codex") a switch to another engine moves the conversation to (#5054, docs/protocol/protocol.md
// "Switching a running Agent to another engine"): its next message starts that engine in the same terminal, with a
// handoff summary, or resumes the conversation's own earlier session of it. Empty for a switch between launchers of
// the same engine (#5035).
type PendingSwitch struct {
	Launcher string    `json:"launcher"`
	Model    string    `json:"model,omitempty"`
	Effort   string    `json:"effort,omitempty"`
	Agent    string    `json:"agent,omitempty"`
	At       time.Time `json:"at"`
}

// Codes a launcher switch of a running Agent refuses with (node/launcher_switch.go, #5035): 409, nothing typed, the
// text in the request's language (apierr.Call, rpc-errors.<code>); the node's own words, with the names, are the
// body's detail. switch_stale comes only from POST …/modes; the not-stopped and failed ones only from the user's
// message that carries a switch out; the rest from both, a message's when something changed since the pick. A message
// refused with one of them drops the switch: nothing tries it again by itself.
const (
	// CodeSwitchStale: the pick was made against another launcher than the session runs on by now (POST …/modes'
	// from): a switch was carried out meanwhile. Nothing was recorded.
	CodeSwitchStale = "switch_stale"
	// CodeSwitchUnderWay: a switch of the session is being carried out right now (maybe on the last connection's Node
	// of a resident node): what would type into its pane or press its keys (a model, mode or effort change, a rewind,
	// an answer, keys, a close), and a message that would carry out another, is refused with nothing done.
	CodeSwitchUnderWay = "switch_under_way"
	// CodeSwitchPickModel: no model was picked, and what the launcher runs by default cannot be seen (a wrapper script,
	// a provider that names no model): resumed without --model, the conversation would go on with the model of the
	// launcher it ran on.
	CodeSwitchPickModel = "switch_pick_model"
	// CodeSwitchUnsupported: not a Claude Code Agent running in a terminal Persona can restart it in (Codex, another
	// CLI, a runtime's session, an assistant conversation, botmux).
	CodeSwitchUnsupported = "switch_unsupported"
	// CodeCompactFailed: a send that asked to compact a nearly full Agent first (#11137) found the compaction failed:
	// nothing was sent, and the refusal says why.
	CodeCompactFailed = "compact_failed"
	// CodeSwitchLauncherMissing: the launcher picked is not on the session's computer.
	CodeSwitchLauncherMissing = "switch_launcher_missing"
	// CodeSwitchOtherAgent: the launcher picked runs another agent than Claude Code.
	CodeSwitchOtherAgent = "switch_other_agent"
	// CodeSwitchModelLocked: a model was picked, and the launcher fixes its own.
	CodeSwitchModelLocked = "switch_model_locked"
	// CodeSwitchConfigDir: the launcher runs Claude Code with another config directory than the one the conversation
	// is in: --resume would not find it there, and the sign-in lives in that directory too.
	CodeSwitchConfigDir = "switch_config_dir"
	// CodeSwitchNotStopped: the agent did not quit; it runs on as before.
	CodeSwitchNotStopped = "switch_not_stopped"
	// CodeSwitchFailed: the agent did not come up on the new launcher; it is back on its own one.
	CodeSwitchFailed = "switch_failed"
	// CodeSwitchFailedStopped: the agent did not come up on the new launcher and could not be brought back on its own
	// one: it has stopped (Resume on its terminal page).
	CodeSwitchFailedStopped = "switch_failed_stopped"
	// CodeSwitchBusy: the assistant's switch (session.send with switch_now, #5973) could not be carried out now: the
	// agent works, waits for the user, is starting or has background work a quit would end. Nothing was typed and the
	// switch was dropped.
	CodeSwitchBusy = "switch_busy"
)

// TodoRef names a session's associated todo; Session.Todo is active, CompletedTodo() is display-only history.
type TodoRef struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

// SourceRuntime is the source_kind of a session an agent runtime lists (docs/integrations/runtimes.md): source_instance_id names
// the runtime ("dsh"); its capabilities come from what the runtime declared (no terminal, never attach).
const SourceRuntime = "runtime"

// Port is a listening TCP port on a machine (docs/operations/ports.md).
type Port struct {
	Port int `json:"port"`
	// Addr is what it is bound to: 127.0.0.1, 0.0.0.0, ::, ::1 or one of the machine's addresses.
	Addr    string `json:"addr,omitempty"`
	PID     int    `json:"pid,omitempty"`
	Process string `json:"process,omitempty"` // the command, as ps / lsof name it (node, python3.12…)
	Title   string `json:"title,omitempty"`   // the <title> of its page at /
}

// NativeArchive is a session's state in its own tool's archive, as its manager last asked for it
// (docs/protocol/protocol.md "session.archive").
type NativeArchive struct {
	// Archived is what the tool was asked for: archived (true) or restored.
	Archived bool `json:"archived"`
	// State: done (the tool has it so), pending (not yet: the machine offline, the agent working, an error; tried
	// again), unsupported (the tool has no archive Persona can drive: the archive is Persona's alone).
	State string `json:"state"`
	// Error is why the last try did not finish, while pending.
	Error string `json:"error,omitempty"`
}

// Direction is a leader conversation's record (assistantwire.SessionView.Direction, GET /api/v1/assistant/leaders): Project the knowledge-base project's slug ("" while
// none), By "assistant" or "user" (the owner's choice, never redone by the hub), At when it became a direction, EndedAt
// when it stopped being a leader (nothing is deleted).
type Direction struct {
	Project string     `json:"project,omitempty"`
	By      string     `json:"by"`
	At      time.Time  `json:"at"`
	EndedAt *time.Time `json:"ended_at,omitempty"`
}

// ModelPick is what an assistant conversation was set to run on in the apps (PUT
// /api/v1/assistant/conversations/{id}/model): a launcher and, when one was picked, its model and reasoning effort.
type ModelPick struct {
	Launcher string `json:"launcher"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
	// Runtime and Channel are the pick's two other dimensions (docs/assistant/execution-spec.md): the engine and the
	// connection the launcher alias stands for. A newer client may send them without a launcher; the hub then finds
	// the alias, and an older client keeps reading the launcher.
	Runtime string `json:"runtime,omitempty"`
	Channel string `json:"channel,omitempty"`
}

// UndeliveredMessage is one of Session.Undelivered: an owner's message its manager keeps after it could not go in.
type UndeliveredMessage struct {
	// ID is the message's client_msg_id: the bubble the sending app shows for it, and its once-only key when it goes.
	ID     string    `json:"id"`
	Text   string    `json:"text"`
	Images int       `json:"images,omitempty"` // how many pictures it carries (they are kept and sent with it)
	At     time.Time `json:"at"`               // when the owner sent it
	// State is UndeliveredWaiting (it is sent again by itself) or UndeliveredFailed (it gave up: Retry or Delete).
	State      string     `json:"state"`
	Attempts   int        `json:"attempts,omitempty"`
	NextAt     *time.Time `json:"next_at,omitempty"`     // when the next try is due while waiting
	Error      string     `json:"error,omitempty"`       // why the last try did not go in, in the owner's language
	DeleteOnly bool       `json:"delete_only,omitempty"` // a failure the owner can delete but cannot retry
	// Computer is set on a message held for an Agent while its computer was away (#10661, internal/devices held.go):
	// that computer's name. Error is then the whole line to show under the bubble ("Will send when … is back"), and
	// Retry / Delete go to the session's held routes (POST /api/v1/sessions/{mid}/{sid}/held/{msg}/retry, DELETE …).
	Computer string `json:"computer,omitempty"`
	// Reason is set on an input of a logical conversation's delivery queue (assistant/logical_undelivered.go,
	// docs/operations/logical-input-reconciliation.md "Delivery deadline"): why it has not gone in yet. Empty on the
	// kept and held messages above, which say it in Error alone. Error is still the line in the owner's language.
	Reason UndeliveredReason `json:"reason,omitempty"`
	// Since is when the current wait began: the attempt's dispatch for awaiting_receipt, the queueing otherwise, the
	// expiry for receipt_expired.
	Since time.Time `json:"since,omitzero"`
	// Deadline is when an awaiting_receipt attempt stops waiting for its receipt and expires (nothing is sent twice).
	Deadline time.Time `json:"deadline,omitzero"`
	// Ahead is how many inputs wait before this one; Behind how many wait after the one awaiting its receipt.
	Ahead  int `json:"ahead,omitempty"`
	Behind int `json:"behind,omitempty"`
	// BlockedBy is the client_msg_id of the input a queued one waits behind (the one awaiting its receipt).
	BlockedBy wireids.ClientMsgID `json:"blocked_by,omitempty"`
}

// UndeliveredMessage states.
const (
	UndeliveredWaiting = "waiting"
	UndeliveredFailed  = "failed"
)

// UndeliveredReason is UndeliveredMessage.Reason: a closed set.
type UndeliveredReason string

const (
	// UndeliveredAwaitingReceipt: it was dispatched and its receipt is not in yet. Waiting, until Deadline.
	UndeliveredAwaitingReceipt UndeliveredReason = "awaiting_receipt"
	// UndeliveredBehindUnconfirmed: queued behind an input awaiting its receipt (BlockedBy). Waiting.
	UndeliveredBehindUnconfirmed UndeliveredReason = "behind_unconfirmed"
	// UndeliveredSwitchPending: queued while the conversation moves to another engine. Waiting.
	UndeliveredSwitchPending UndeliveredReason = "switch_pending"
	// UndeliveredQueued: queued and about to go in. Waiting.
	UndeliveredQueued UndeliveredReason = "queued"
	// UndeliveredReceiptExpired: no receipt came by its deadline; it left the queue and later input went on. Failed:
	// it may have gone in, so the owner looks and picks Retry (sent once more, last in the queue) or Delete.
	UndeliveredReceiptExpired UndeliveredReason = "receipt_expired"
)

func (r UndeliveredReason) Valid() bool {
	switch r {
	case UndeliveredAwaitingReceipt, UndeliveredBehindUnconfirmed, UndeliveredSwitchPending, UndeliveredQueued, UndeliveredReceiptExpired:
		return true
	}
	return false
}

// QueueEdit is the answer to POST /api/v1/sessions/{mid}/{sid}/queue in a Persona conversation (node session.queue.edit,
// docs/protocol/protocol.md): the messages that were queued before the edit, in order, and their photos for the composer
// when any carried some. Images[i] is message i's photos, null when it had none; the key is left out when no message did.
type QueueEdit struct {
	Queue  []string  `json:"queue"`
	Images [][]Image `json:"images,omitempty"`
}

// ServiceTier is an advertised running-session speed or a native Codex catalog tier.
type ServiceTier struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Tokens32 is n as Session.ContextTokens and ContextWindow hold it: saturated at the int32 bounds.
func Tokens32(n int) int32 {
	return int32(max(min(n, math.MaxInt32), math.MinInt32))
}

// ContextNearlyFullShare is the share of its model's window an Agent's context may fill before the assistant's
// agent_send compacts it ahead of the message (#11137): above it the Agent would compact, or run out, as it reads.
const ContextNearlyFullShare = 0.85

// ContextNearlyFull: what the model saw in the latest reply fills ContextNearlyFullShare of its window (both known).
func ContextNearlyFull(tokens, window int) bool {
	return window > 0 && tokens > 0 && float64(tokens) >= ContextNearlyFullShare*float64(window)
}

// UnixTime32 is a time in whole seconds since the epoch (zero: not set) that is sent as RFC 3339 like the other times;
// four bytes, so that it fits the padding in Session.
type UnixTime32 uint32

// UnixTime32Of is t as a UnixTime32 (zero for the zero time and for times it cannot hold).
func UnixTime32Of(t time.Time) UnixTime32 {
	if t.IsZero() || t.Unix() <= 0 || t.Unix() > 1<<32-1 {
		return 0
	}
	return UnixTime32(t.Unix())
}

// Time is the time it holds (the zero time when not set).
func (u UnixTime32) Time() time.Time {
	if u == 0 {
		return time.Time{}
	}
	return time.Unix(int64(u), 0).UTC()
}

// MarshalJSON writes null for a zero one, so that a Session leaves it out of every encoder's output.
func (u UnixTime32) MarshalJSON() ([]byte, error) {
	if u == 0 {
		return []byte("null"), nil
	}
	return u.Time().MarshalJSON()
}

func (u *UnixTime32) UnmarshalJSON(b []byte) error {
	var t time.Time
	if string(b) != "null" {
		if err := t.UnmarshalJSON(b); err != nil {
			return err
		}
	}
	*u = UnixTime32Of(t)
	return nil
}
