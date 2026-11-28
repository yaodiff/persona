package proto

import (
	"encoding/json"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/dialogwire"
)

// Interaction kinds.
const (
	IxQuestion    = "question"
	IxPlan        = "plan"
	IxPermission  = "permission"
	IxScreen      = "screen"
	IxElicitation = "elicitation"
)

// AnsweredByAssistant marks a resolution the assistant gave for the user (Resolution.AnsweredBy).
const AnsweredByAssistant = "assistant"

// AnsweredByTerminal marks a decision made in the terminal rather than on its card (Resolution.AnsweredBy).
const AnsweredByTerminal = "terminal"

// AnsweredByDefault marks a resolution nobody gave: the question's deadline passed unanswered and, as going ahead can
// be undone, its recommended options were taken by default (Resolution.AnsweredBy; docs/protocol/protocol.md "Who
// answered a card"). Only the history carries it, on a card that expired first (Resolution.Late).
const AnsweredByDefault = "default"

// DismissedInChat is the Reason of a Dismiss for an assistant question taken back because the user wrote in the
// conversation instead of answering it (#9): channels say "replied in the chat" on its card, not "dismissed".
const DismissedInChat = "replied_in_chat"

// DismissedExpired is the Reason of a Dismiss the manager hands one of the assistant's own questions whose card expired
// unanswered (#1617): an ask_user that expires becomes a to-do instead. Only the manager sets it; the apps' answers have
// their Reason dropped (assistant/fleet_routes.go).
const DismissedExpired = "expired"

// DismissedWithdrawn is the Reason of a Dismiss the manager hands the one waiting on a card that ended because the
// asker went away before anyone answered it: its call ended, its hook disconnected and did not come back. Nobody
// answered it: it is no decline (Resolution.Outcome). Only the manager sets it.
const DismissedWithdrawn = "withdrawn"

// DismissedCleared is the Reason of a Dismiss for a card that ended because it was cleared before anyone answered it:
// a newer card replaced it, or it was no longer the one pending under its id. Nobody answered it: it is no decline
// (Resolution.Outcome). Only the manager sets it.
const DismissedCleared = "cleared"

// AskedWithoutWaiting: ix is the assistant's ask_user raised with wait false (#4928, docs/assistant/assistant.md
// "Asking without holding the turn"): its turn ended when the card went up, so no turn is held behind it. It is the
// one ask_user card that is Resumable (a waiting one lives in the manager's memory and is not).
func (ix Interaction) AskedWithoutWaiting() bool { return ix.ToolName == "ask_user" && ix.Resumable }

// OneTapQuestion is the question ix asks when one tap may answer it without opening the app (a notification's
// options, a to-do's row): a question card with exactly one question, options to choose from and no fields to fill in,
// not raised in a Just me turn (whose push says nothing of what it asks), and whose asker did not say going ahead
// cannot be undone (cannotUndo: an answer that cannot be taken back needs a look first). A space conversation's card
// is never answered so either; only the hub and the manager can tell one (identity.InSpace on its cwd), and each
// checks it beside this. False otherwise.
func (ix Interaction) OneTapQuestion() (Question, bool) {
	if ix.Kind != IxQuestion || ix.ToolName == "persona_session_review" || len(ix.Questions) != 1 || len(ix.Fields) > 0 || len(ix.Questions[0].Options) == 0 ||
		ix.Private || ix.cannotUndo() {
		return Question{}, false
	}
	return ix.Questions[0], true
}

// cannotUndo: ix's asker said going ahead cannot be undone (an ask_user's reversible false), the card's twin of a
// decision that cannot be undone. A card that said nothing can be.
func (ix Interaction) cannotUndo() bool { return ix.Reversible != nil && !*ix.Reversible }

// QuestionOptionDefer dismisses a triage question without sending an agent reply.
// Other question runtimes do not interpret this triage-only action.
const QuestionOptionDefer = "defer"

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Action      string `json:"action,omitempty"` // Optional triage intent; empty means a normal answer.
}

type Question struct {
	Question    string           `json:"question"`
	Header      string           `json:"header,omitempty"`
	MultiSelect bool             `json:"multi_select"`
	Options     []QuestionOption `json:"options"`
	// Recommend is the label of the option the asker recommends (the assistant's ask_user recommend, always one of
	// Options' labels); absent when it recommended none.
	Recommend string `json:"recommend,omitempty"`
}

type Interaction struct {
	// Manager is the manager it belongs to (docs/assistant/managers.md, "Ownership"); the hub stamps it on what it sends the apps.
	Manager string `json:"manager,omitempty"`
	// Private: a Just me turn raised it (docs/assistant/meeting-minutes.md "Just me"): its push and the Fleet Live
	// Activity say nothing of what it asks, as a health conversation's card, and it never goes to Lark or a channel.
	Private bool   `json:"private,omitempty"`
	ID      string `json:"id"`
	// MirrorOf identifies the authoritative interaction for a display-only copy in another assistant conversation.
	// The hub normalizes its actions to that interaction; it is never a second pending operation or aggregate request.
	MirrorOf string `json:"mirror_of,omitempty"`
	// MirrorMain is manager-owned intent on the authoritative card: it has a main-conversation display copy.
	// It lets reconnects preserve that copy after the preference changes. Nodes cannot declare either mirror field.
	MirrorMain   bool      `json:"mirror_main,omitempty"`
	MachineID    string    `json:"machine_id"`
	SessionID    string    `json:"session_id"`
	SessionTitle string    `json:"session_title,omitempty"`
	TaskTitle    string    `json:"task_title,omitempty"` // the task its agent works for (docs/assistant/unified-entry.md), set when sent to clients
	Cwd          string    `json:"cwd,omitempty"`
	Kind         string    `json:"kind"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	// SeenAt is when the user first viewed an owner-only timed card in an app; nil while unseen.
	SeenAt *time.Time `json:"seen_at,omitempty"`
	// SeenTTLSeconds is the countdown length (in seconds) that starts once SeenAt is recorded.
	SeenTTLSeconds int        `json:"seen_ttl_seconds,omitempty"`
	ToolName       string     `json:"tool_name,omitempty"`
	ToolUseID      string     `json:"tool_use_id,omitempty"`
	Questions      []Question `json:"questions,omitempty"`
	Plan           string     `json:"plan,omitempty"`
	// An explicit independent session review (tool persona_session_review) keeps the report beside its ordinary
	// question-card fallback. Older clients ignore these additive fields and retain every other card's decoding.
	ReviewReport      string          `json:"review_report,omitempty"`
	ReviewCount       *int            `json:"review_count,omitempty"`
	ReviewerSessionID string          `json:"reviewer_session_id,omitempty"`
	ToolInput         json.RawMessage `json:"tool_input,omitempty"`
	// PlanThen is the permission mode recorded at a plan-mode start to use after approval; empty on legacy cards.
	PlanThen string `json:"plan_then,omitempty"`
	// EditableText is the outgoing body text an eligible Lark send/reply approval card (Claude Code Bash PreToolUse,
	// a local assistant_environment runtime command gate, or the node's MCP command-gate tool) may let the user edit
	// before allowing. Only those command gates set it; untrusted runtimes and other cards cannot declare it.
	EditableText string `json:"editable_text,omitempty"`
	// OutwardWrite marks a permission card raised by a trusted connector gate for an outward write acting as
	// the owner (node/larkgate.go). Runtimes cannot declare it (node/runtimes_events.go clears it), and the
	// manager honors it only on permission cards.
	OutwardWrite bool `json:"outward_write,omitempty"`
	// Format is a script ask's opt-in card rendering. Only "markdown" is emitted; absent keeps literal text.
	Format          string       `json:"format,omitempty"`
	Summary         string       `json:"summary,omitempty"`
	Screen          string       `json:"screen,omitempty"`
	AgentID         string       `json:"agent_id,omitempty"`
	AgentType       string       `json:"agent_type,omitempty"`
	MCPServerName   string       `json:"mcp_server_name,omitempty"`
	MCPConfigSource string       `json:"mcp_config_source,omitempty"`
	Elicitation     *Elicitation `json:"elicitation,omitempty"`
	// CanCancel is advertised only by runtimes supporting question-only cancellation.
	CanCancel bool `json:"can_cancel,omitempty"`
	// CanClarify advertises an assistant-owned prepared approval whose owner may ask a question without resolving it.
	// Only the authoritative creation path sets it; older clients ignore it and keep their existing Allow/Deny behavior.
	CanClarify bool `json:"can_clarify,omitempty"`
	// NoHandBack marks a card that has no interactive computer terminal or UI to hand back to (manager-owned approvals,
	// headless assistant conversations and command gates, and headless ACP companions such as Antigravity (agy),
	// Grok (grok / grok-chat), and the builtin OpenCode companion (opencode, distinct from the opencode-<pid> TUI
	// plugin) where dismiss cancels or denies instead of handing back). Only the manager sets it; absent or false
	// preserves the existing "Leave to computer" action for interactive terminal cards and older hubs.
	NoHandBack bool `json:"no_hand_back,omitempty"`
	// Detachable is a node command gate's request to release its initial hook while the owner decides. The manager
	// honors it only for a permission in one of its assistant conversations with complete input and ExecutionCwd;
	// a later Allow creates a one-time grant for the exact original call rather than executing it in another process.
	Detachable bool `json:"detachable,omitempty"`
	// WriteOperation is the node command gate's classification for display routing:
	// true only for a known write, never for a GET/HEAD or an unknown command. It
	// does not authorize execution; the ordinary approval path remains the gate.
	WriteOperation bool `json:"write_operation,omitempty"`
	// ExecutionCwd is where this command actually runs, which can differ from the conversation's display directory
	// (Cwd). The node sets it for a detachable command; the manager requires it and includes it in the exact-call grant.
	ExecutionCwd string `json:"execution_cwd,omitempty"`
	// Dialog is the screen's dialog read on the hub (screen interactions); answers name an option and are checked
	// against Dialog.Rev before any key is pressed. Absent when the screen shows no dialog it could read.
	Dialog *dialogwire.Dialog `json:"dialog,omitempty"`
	// Secret: the screen asks for a password. The app takes it in a password field and keeps no copy; the hub never
	// stores what is typed into a terminal card anyway.
	Secret bool `json:"secret,omitempty"`
	// Fields are filled in on the phone before approving (a permission card that needs a password): the values come
	// back in Resolution.Values and never pass through the model. Quick approve (notification buttons, preview
	// cards, the watch, Siri) is off for such cards: they open the form.
	Fields []Field `json:"fields,omitempty"`
	// Resumable: a hook waits on it that reconnects when its node goes away (hub or node restart), so the hub keeps
	// it pending through the gap instead of cancelling it (docs/protocol/protocol.md "Interactions survive restarts").
	Resumable bool `json:"resumable,omitempty"`
	// ApprovalBatchID and ApprovalItemID identify a server-owned exact owner-action proposal. NoExpiry is valid only
	// beside these fields on a resumable proposal created by the manager; it never comes from a node or an app.
	ApprovalBatchID string `json:"approval_batch_id,omitempty"`
	ApprovalItemID  string `json:"approval_item_id,omitempty"`
	OwnerProposal   bool   `json:"owner_proposal,omitempty"`
	NoExpiry        bool   `json:"no_expiry,omitempty"`
	// OwnerApprovalPhrases are display hints for the proposal's reply control.
	// The manager's current settings, not this snapshot, authorize a reply.
	OwnerApprovalPhrases []string `json:"owner_approval_phrases,omitempty"`
	// Agent is who asks when it is not Claude Code: "codex" or "traex" (a card the node read off a Codex TUI,
	// docs/integrations/codex.md "Questions and approvals").
	Agent string `json:"agent,omitempty"`
	// Reason: why the agent asks for this permission, in its words (Codex's "Reason: …").
	Reason string `json:"reason,omitempty"`
	// AlwaysLabel names what "always allow" does for this request ("don't ask about this command again in this session", in Chinese); a permission card offers it
	// only when set or for Claude.
	AlwaysLabel string `json:"always_label,omitempty"`
	// AgentLabel is what to call Agent when the apps do not know it (a runtime's own name, "DSH"; docs/integrations/runtimes.md),
	// as proto.Session.AgentLabel.
	AgentLabel string `json:"agent_label,omitempty"`
	// AlwaysKeys: what "Always allow in this session" grants on an assistant conversation's permission card, set by the node (a tool,
	// a Codex command or file, a gated write's route). The hub keeps them for the conversation once answered so, and
	// answers a later card whose keys are all kept by itself: the agent's own session rule dies with its process
	// (docs/assistant/assistant.md "Always allow in this session"). Absent on Agents sessions' cards.
	AlwaysKeys []string `json:"always_keys,omitempty"`
	// AlwaysHonored is the manager's word on whether an answer of "Always allow" on this permission card is kept
	// (docs/protocol/protocol.md "Always allow"): true when it is (a node's card with AlwaysKeys the manager remembers,
	// a card of its own that says what it keeps in AlwaysLabel), false when nothing would keep it (the manager's other
	// cards: the extensions' tools, persona_settings…), so the apps offer only Allow and Deny. Absent (an older manager,
	// and Agents sessions' cards, whose agent keeps it itself) the apps keep their own rule.
	AlwaysHonored *bool `json:"always_honored,omitempty"`
	// ToolTitle is the tool's name for people in the user's language ("iPhone control" for iphone_do), set by the
	// manager on its own tools' cards and on a node's card for one of Persona's own MCP tools ("Report to supervisor ·
	// Done", #4966); the apps name the tool with it, else with ToolName as before.
	ToolTitle string `json:"tool_title,omitempty"`
	// ToolNameTitle is the tool alone, without what this call does, where ToolTitle says that too ("Report to
	// supervisor" for "Report to supervisor · Done"): what "Always allow" covers, so a line about every call of the
	// tool names it this way. Set by the manager only beside such a ToolTitle; otherwise ToolTitle names the tool.
	ToolNameTitle string `json:"tool_name_title,omitempty"`
	// Delegating: the assistant is deciding whether it may answer this question for the user (agents.delegate,
	// docs/assistant/assistant.md "Assistant answers questions first"); nothing was pushed yet. The card shows in the apps meanwhile, and an answer
	// given there wins.
	Delegating bool `json:"delegating,omitempty"`
	// PushLevel is the hub push-grading urgency for this card ("P0", "P1", "P2"): empty/absent defaults to "P0",
	// while "P1" marks a supervisor-escalated card held for the Needs you list and briefing when grading is on.
	PushLevel string `json:"push_level,omitempty"`
	// Delegate is what the assistant found when it left the question to the user: the context it filled in.
	Delegate *Delegate `json:"delegate,omitempty"`
	// Suggestion: the owner answered this kind of card the same way three times; the card offers a standing rule
	// (rule_suggestion.go). Set by the manager on a question a supervisor passed up; absent otherwise.
	Suggestion *RuleSuggestion `json:"suggestion,omitempty"`
	// Muted says the user muted this agent or its space on the hub. The hub stamps it when answering GET
	// /api/v1/interactions so an answer-only device (the watch) can hide the card and not buzz for it; the phone
	// keeps its own mutes. Absent on older hubs' lists and on the manager's bus copy.
	Muted bool `json:"muted,omitempty"`
	// FoldedUnder is its session's Session.FoldedUnder: the listed session whose chat shows the card when the asking
	// session is a child folded under it (docs/protocol/protocol.md "Session hierarchy"). Set when sent to clients.
	FoldedUnder string `json:"folded_under,omitempty"`
	// FoldedUnderMachineID is its session's Session.FoldedUnderMachineID: the machine of FoldedUnder when the parent
	// lives on another computer; empty when on the same machine. Set beside FoldedUnder when sent to clients.
	FoldedUnderMachineID string `json:"folded_under_machine_id,omitempty"`
	// FoldedAs is its session's Session.FoldedAs: "worker" when the asking session is a tab's worker folded under its
	// lead (#5180), "side" when it is a side session (#7944). Set beside FoldedUnder when sent to clients.
	FoldedAs string `json:"folded_as,omitempty"`
	// FoldedSide is its session's Session.FoldedSide: the side session whose chat the card is in when the asking
	// session folds through one and is not that side (#7944). Set beside FoldedUnder when sent to clients.
	FoldedSide string `json:"folded_side,omitempty"`
	// Deadline is when an ask_user's questions take their recommended options by default (when Reversible) or are
	// reminded again (when not); absent without one. Reversible is whether the asker said going ahead can be undone;
	// absent when it did not say. Both are the asker's own words, kept on the card for the history
	// (docs/protocol/protocol.md "Decision fields").
	Deadline *time.Time `json:"deadline,omitempty"`
	// Reversible: see Deadline.
	Reversible *bool `json:"reversible,omitempty"`
}

// Delegate is the assistant's context on a question it left to the user (Interaction.Delegate): it is a red line or
// about the goal itself, or the assistant could not tell.
type Delegate struct {
	What      string     `json:"what,omitempty"`      // what this is about, in a line
	WhyNow    string     `json:"why_now,omitempty"`   // why the agent asks now
	Recommend string     `json:"recommend,omitempty"` // the answer it recommends (an option's label when there is one)
	Cost      string     `json:"cost,omitempty"`      // what not taking the recommendation costs
	Reason    string     `json:"reason,omitempty"`    // why it is the user's to answer
	Deadline  *time.Time `json:"deadline,omitempty"`  // by when: 24 hours, or the card's expiry when that is sooner
}

// Field is one input of a card (Interaction.Fields).
type Field struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder,omitempty"`
	Secret      bool   `json:"secret,omitempty"` // a password: a secure field, redacted in the history
	Optional    bool   `json:"optional,omitempty"`
}

// Elicitation carries the hook's original schema or URL without interpreting its fields.
type Elicitation struct {
	Mode            string          `json:"mode"`
	Message         string          `json:"message"`
	RequestedSchema json.RawMessage `json:"requested_schema,omitempty"`
	URL             string          `json:"url,omitempty"`
	ID              string          `json:"elicitation_id,omitempty"`
}

// PlanAfterMode is a valid mode to apply after a plan, or "" when a card from another or older source names no
// supported following mode. In particular plan itself is not a following mode; unknown values keep legacy behavior.
func PlanAfterMode(mode string) string {
	if mode == "plan" || !slices.Contains(PermissionModes, mode) {
		return ""
	}
	return mode
}

type Resolution struct {
	// ModeActor is manager-authenticated display provenance for a plan mode change; callers cannot set it.
	ModeActor *ModeActor `json:"mode_actor,omitempty"`
	// Cancel discards only this question, without supplying answers or interrupting the task.
	Cancel   bool              `json:"cancel,omitempty"`
	Action   string            `json:"action,omitempty"`
	Content  map[string]any    `json:"content,omitempty"`
	Answers  map[string]string `json:"answers,omitempty"`
	Approve  *bool             `json:"approve,omitempty"`
	Mode     string            `json:"mode,omitempty"`
	Feedback string            `json:"feedback,omitempty"`
	Allow    *bool             `json:"allow,omitempty"`
	Always   bool              `json:"always,omitempty"`
	Message  string            `json:"message,omitempty"`
	// EditedText is the replacement body text on a plain Allow of an editable_text card, and nothing else in the
	// answer: no route, target, recipients, tool or other arguments can be changed from the card.
	EditedText string   `json:"edited_text,omitempty"`
	Keys       []string `json:"keys,omitempty"`
	Text       string   `json:"text,omitempty"`
	// AnsweredBy is who answered when it was not the user in the app: AnsweredByAssistant for a question the
	// assistant answered on their behalf (docs/assistant/assistant.md), AnsweredByTerminal for a terminal-decided
	// dismissal, and AnsweredByDefault for recommended options taken at a deadline. Absent when the user answered.
	AnsweredBy string `json:"answered_by,omitempty"`
	// Late: the history's record of an answer that came after the card expired: the user's own answer on the to-do the
	// question became, or its recommendation taken at its deadline (AnsweredByDefault). The card stays "expired".
	Late bool `json:"late,omitempty"`
	// Reason is why the assistant answered so (AnsweredByAssistant): the Y of "Answered by the assistant: X, because Y".
	// With Dismiss and no AnsweredBy it is why the card was dismissed (DismissedInChat).
	Reason string `json:"reason,omitempty"`
	// Remembered marks an allow the manager gave from an "Always allow in this session" it had kept (or that a card
	// answered meanwhile covers), not from an answer to this card: the node's sent-as-user ledger records such a write
	// as conversation_always, not as a card (docs/extensions/connectors.md "Sent as the user"). Only the manager sets it; an
	// answer from an app or a channel never carries it. An older manager does not send it.
	Remembered bool `json:"remembered,omitempty"`
	// PreKeys go in before Text (Keys after it): pick "Type something." by its number, type the answer, enter.
	PreKeys []string `json:"pre_keys,omitempty"`
	Dismiss bool     `json:"dismiss,omitempty"`
	// Option answers a screen dialog by its option's key (Interaction.Dialog): the keys are worked out where they are
	// pressed, from the screen as it is then (Text goes with a type_something option).
	Option string `json:"option,omitempty"`
	// Options answer a multi-select screen dialog (Dialog.Multi): the keys of the options to have checked when it is
	// sent, the rest unchecked; Text is its free-text field (empty: left unchecked).
	Options []string `json:"options,omitempty"`
	// Pending marks a non-final dismissal of a detachable command's initial hook: the owner has not decided, nothing
	// ran, and the card remains pending. The originating conversation receives the final outcome separately.
	Pending bool `json:"pending,omitempty"`
	// ApprovalID identifies the authoritative card behind a pending result or a consumed one-time continuation grant.
	// It is empty on an ordinary answer to the card currently being waited on.
	ApprovalID string `json:"approval_id,omitempty"`
	// Values are the card's Fields as filled in on the phone, by key.
	Values map[string]string `json:"values,omitempty"`
}
