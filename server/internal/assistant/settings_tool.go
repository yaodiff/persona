package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	approvalops "github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// persona_settings: Persona changes its own settings for the user (docs/assistant/assistant.md "Current structure", the
// "Control Persona itself" row). The assistant's own ("About me", its launcher, "notify tasks the main chat starts only
// in the main chat") are its to read and change; the hub's (the inbox's rules, the blocked Claude config directories,
// mail accounts…) are the hub's settings app's, which asks the user itself (app.tool persona_settings). Reads are free;
// every change is a card on the phone that says what changes (before → after) and is applied only when approved, except
// the exact reversible-setting allowlist when the administrator's manager.json switch is on.

// personaSettingsInput is the complete built-in call, shared by direct and exact-batch execution.
type personaSettingsInput struct {
	Action string          `json:"action"`
	Key    string          `json:"key"`
	Value  json.RawMessage `json:"value"`
	ID     string          `json:"id"` // remove's, as the hub's own settings take it
}

// ownSetting is one of the assistant's own settings persona_settings offers.
type ownSetting struct {
	Setting
	get func(ctx context.Context) (string, error)
	set func(ctx context.Context, v json.RawMessage) (*ownChange, error)
	// cardTitle localizes a dynamic setting's title (direction.<id>.prompt); static keys use settingTitles instead.
	cardTitle func(l i18n.Lang) string
	// toolName is the managed wrapper's card name; empty is persona_settings.
	toolName string
	// change is a setting's other actions (a list's add and remove: tasks.trust), in place of set.
	change func(ctx context.Context, action string, v json.RawMessage) (*ownChange, error)
}

// ownChange is a change waiting for the phone: what the card says and how it is made (none: nothing to change).
type ownChange struct {
	summary  string                   // for the assistant: the change made nothing to do
	cardText func(l i18n.Lang) string // what the card says, in the user's language
	apply    func(ctx context.Context) (string, error)
	// revision is the state revision captured for an exact owner-action batch,
	// when this setter has one. modelProfileWrite marks one catalog increment;
	// later items in that same batch expect the revision advanced by their own
	// predecessors, while an external write still conflicts.
	revision          *int64
	modelProfileWrite bool
	profileAfter      map[string]ModelProfile
	// applyChecksProposal marks an apply that re-reads current state and
	// atomically checks the exact card revision itself. Rebuilding such an
	// operation would move its revision baseline forward and hide a conflict.
	applyChecksProposal bool
	// reversible is present only on the audited, exactly restorable settings. Its raw before-image distinguishes an
	// unset pointer from an explicitly saved default; applyAutomatic checks it again under the settings write lock.
	reversible *reversibleSetting
}

const autoApproveReversibleSettingsKey = "assistant.auto_approve_reversible_settings"

// reversibleSettingAutoApproved is the complete allowlist. Adding a setter descriptor alone cannot bypass a card.
func reversibleSettingAutoApproved(key string) bool {
	switch key {
	case "assistant.main_background_folded", "assistant.recent_task_chats", "assistant.briefing_waits":
		return true
	default:
		return false
	}
}

type reversibleSetting struct {
	before  json.RawMessage
	after   json.RawMessage
	current func(Settings) json.RawMessage
	assign  func(*Settings)
	applied string
}

// reversibleSettingJSON cannot fail for the Boolean pointers and raw module values in the allowlist.
func reversibleSettingJSON(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

// card is what the approval card says in l.
func (c *ownChange) card(l i18n.Lang) string {
	if c.cardText != nil {
		return c.cardText(l)
	}
	return c.summary
}

func (s *Service) ownSettings() []ownSetting {
	// A module's setting is listed only while its module is loaded (modules.go): its value stays stored, hidden.
	return slices.DeleteFunc(s.allOwnSettings(), func(o ownSetting) bool {
		_, loaded := s.modules.Setting(o.Key)
		return moduleSettingKeys[o.Key] && !loaded
	})
}

// moduleSettingKeys are the keys below that a feature module serves (modules.go moduleStored).
var moduleSettingKeys = map[string]bool{"assistant.briefing_waits": true}

func (s *Service) allOwnSettings() []ownSetting {
	getSet := []string{"get", "set"}
	return []ownSetting{
		{Setting: Setting{Key: outboundIdentityKey, Title: "Outward-message identity", Actions: getSet,
			About: "default Lark message sender; bot preserves existing behavior; changing it never broadens a standing message grant",
			Value: `"bot" or "user"; only supported identity-aware transports use this setting`},
			get: s.outboundIdentity, set: s.setOutboundIdentity},
		s.hiddenDirSetting(),
		s.blockedHerdrSessionsSetting(),
		{Setting: Setting{Key: modulesKey, Title: "Built-in modules", Actions: getSet,
			About: "built-in feature modules switched off; a change applies when the manager next starts, not to the running process",
			Value: `{"disabled": ["<module id>", …]}; null or an empty list restores every compiled-in module`},
			get: s.getModuleSwitch, set: s.setModuleSwitch},
		{Setting: Setting{Key: standingAuthorizationsKey, Title: "Standing authorizations", Actions: getSet,
			About: "user-confirmed computer/directory actions or exact outward-message scopes; only the main conversation may set them, and every change requires the user’s own permission approval",
			Value: "{revision, authorizations: [{id?, machine_id, directory, actions, conditions, quote, source, reversible, messages?}]}; messages: {channel: larkcli, target: exact oc_ chat or empty for all chats, scenario: inbox_reply or assistant_message, identity: bot or user}; message grants require actions [send], reversible true, empty conditions/computer/directory. Get first; retain existing entries unchanged or remove them; omit id for new grants; granted_at is set by the server"},
			get: s.getStandingAuthorizations, set: s.setStandingAuthorizations},
		{Setting: Setting{Key: "assistant.notes", Title: "关于我", Actions: getSet,
			About: "what Persona knows about the user (设置 › 助手 › 关于我), Markdown appended to the assistant's instructions; new conversations read it",
			Value: "a string (Markdown; the whole text)"},
			get: func(ctx context.Context) (string, error) { return orNone(s.notes(ctx)), nil },
			set: s.setNotes},
		{Setting: Setting{Key: "assistant.main_tasks_quiet", Title: "主对话派的任务只在主对话里通知", Actions: getSet,
			About: "on (the default): tasks the main conversation opens (task_plan, agent_start) send no pushes, stay off the Live Activity and " +
				"task badges, and the main conversation is told when one is done, waits for the user or fails instead; off: they notify like any agent. " +
				"Applies to tasks opened from now on",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.mainTasksQuiet(ctx)), nil },
			set: s.setMainTasksQuiet},
		{Setting: Setting{Key: "assistant.approval_mirror_main", Title: "Show approvals in the main chat", Actions: getSet,
			About: "on (the default): a non-private approval for a change in another Assistant conversation also appears in the main conversation; " +
				"read-only requests stay in their original conversation, and answering either copy of a routed request resolves the same request. Off applies to new cards. Agent, private and health conversations are unchanged",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.approvalMirrorMain(ctx)), nil },
			set: s.setApprovalMirrorMain},
		{Setting: Setting{Key: "assistant.start_requested_tasks", Title: "我在对话里说过的，直接开始", Actions: getSet,
			About: "on (the default): task_plan tasks marked user_requested (the user asked for that very task in this turn) start at once " +
				"instead of waiting for 开始 on the plan card; off: only read-only tasks and remembered kinds start without asking",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.startRequestedTasks(ctx)), nil },
			set: s.setStartRequestedTasks},
		{Setting: Setting{Key: "assistant.idle_auto_compact", Title: "Compact idle conversations", Actions: getSet,
			About: "on (the default): compact an idle conversation when the idle time and context size thresholds are both reached",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.idleAutoCompact(ctx)), nil },
			set: s.setIdleAutoCompact},
		{Setting: Setting{Key: "assistant.idle_auto_compact_minutes", Title: "Idle time before compaction", Actions: getSet,
			About: "minimum idle time before automatic compaction; 30 minutes by default",
			Value: "a whole number from 1 to 1440"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.idleAutoCompactMinutes(ctx)), nil },
			set: s.setIdleAutoCompactMinutes},
		{Setting: Setting{Key: "assistant.idle_auto_compact_tokens", Title: "Context size before compaction", Actions: getSet,
			About: "minimum context size before automatic compaction; 300000 tokens by default. Once saved, it is also the size " +
				"at which the main chat is compacted before its idle process is let go (assistant.idle_release_minutes), " +
				"whatever assistant.idle_auto_compact says; never saved, that compaction stays at 75% of the conversation's window",
			Value: "a whole number from 10000 to 10000000"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.idleAutoCompactTokens(ctx)), nil },
			set: s.setIdleAutoCompactTokens},
		{Setting: Setting{Key: "assistant.agy_idle_auto_compact", Title: "Compact idle Antigravity sessions", Actions: getSet,
			About: "on (the default): proactively compact Persona-managed Antigravity sessions (task Agents and AGY assistant/direction chats) once both idle time and context size thresholds are reached",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.agyIdleAutoCompact(ctx)), nil },
			set: s.setAgyIdleAutoCompact},
		{Setting: Setting{Key: "assistant.agy_idle_auto_compact_minutes", Title: "Antigravity idle time before compaction", Actions: getSet,
			About: "minimum idle time before automatic Antigravity session compaction; 10 minutes by default",
			Value: "a whole number from 1 to 1440"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.agyIdleAutoCompactMinutes(ctx)), nil },
			set: s.setAgyIdleAutoCompactMinutes},
		{Setting: Setting{Key: "assistant.agy_idle_auto_compact_tokens", Title: "Antigravity context size before compaction", Actions: getSet,
			About: "minimum context size before automatic Antigravity session compaction; 200000 tokens by default",
			Value: "a whole number from 10000 to 10000000"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.agyIdleAutoCompactTokens(ctx)), nil },
			set: s.setAgyIdleAutoCompactTokens},
		{Setting: Setting{Key: "assistant.idle_release_minutes", Title: "Main chat stays loaded when idle", Actions: getSet,
			About: "how long the main chat's process stays loaded after its last activity before it is let go (it resumes on the " +
				"next message); 20 minutes by default. Just before, it is compacted when its context is large (assistant.idle_auto_compact_tokens " +
				"once saved). Kept below an hour so that compaction still reads the prompt cache. Applies from the main chat's next message",
			Value: "a whole number of minutes from 5 to 55"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.idleReleaseMinutes(ctx)), nil },
			set: s.setIdleReleaseMinutes},
		{Setting: Setting{Key: readonlyModeKey, Title: "Read-only task protection", Actions: getSet,
			About: "sandbox (the default) keeps current runtime protection: Codex uses a read-only sandbox, while Claude uses " +
				"bypassPermissions and a first-message read-only instruction. prompt keeps the instruction without forcing plan " +
				"mode or a read-only sandbox; normal launcher permissions still apply. Instructions are not a security boundary. " +
				"Applies to new task starts and retries, not running agents. Switch back to sandbox to restore the default",
			Value: "sandbox or prompt"},
			get: func(ctx context.Context) (string, error) { return readonlyModeOf(s.settings(ctx)), nil },
			set: s.setReadonlyMode},
		{Setting: Setting{Key: "assistant.task_workspace_preference", Title: "Task workspace preference", Actions: getSet,
			About: "prefer-existing (default) reuses a free task directory. prefer-new isolates writable Git tasks. Read-only tasks use their original directory. Busy Git directories use a new workspace. Explicit choices take precedence. New workspaces follow naming rules on supported computers",
			Value: "prefer-existing or prefer-new"},
			get: func(ctx context.Context) (string, error) { return s.taskWorkspacePreference(ctx), nil },
			set: s.setTaskWorkspacePreference},
		{Setting: Setting{Key: "assistant.recording_audio_days", Title: "Keep recording audio", Actions: getSet,
			About: "how many days recording audio is kept: the phone and watch delete their local recordings (audio and their " +
				"copy of the text) older than this; the hub keeps no meeting audio past its minutes. 14 by default. " +
				"The text in the knowledge base follows assistant.recording_text_days",
			Value: "0 or forever, or a whole number of days, 1–3650"},
			get: func(ctx context.Context) (string, error) {
				return minutesDaysLabel(s.lang(ctx), s.recordingAudioDays(ctx)), nil
			},
			set: s.setRecordingAudioDays},
		{Setting: Setting{Key: "assistant.recording_text_days", Title: "Keep recording text", Actions: getSet,
			About: "how many days a recording's text stays in the knowledge base (kind recordings): its transcript, summary and " +
				"live log, whether a phone, a watch or an extension wrote it (meeting notes, imported transcripts). Past it the " +
				"text is deleted; minutes follow assistant.minutes_days. 0 (or forever) keeps it until the user deletes it, " +
				"which is the default",
			Value: "0 or forever, or a whole number of days, 1–3650"},
			get: func(ctx context.Context) (string, error) {
				return minutesDaysLabel(s.lang(ctx), s.recordingTextDays(ctx)), nil
			},
			set: s.setRecordingTextDays},
		{Setting: Setting{Key: "assistant.recording_days", Title: "Keep recording audio (old name)", Actions: getSet,
			About: "old name of assistant.recording_audio_days, kept for compatibility: reads and sets the same value",
			Value: "0 or forever, or a whole number of days, 1–3650"},
			get: func(ctx context.Context) (string, error) {
				return minutesDaysLabel(s.lang(ctx), s.recordingAudioDays(ctx)), nil
			},
			set: s.setRecordingAudioDays},
		{Setting: Setting{Key: "assistant.minutes_days", Title: "Keep meeting minutes", Actions: getSet,
			About: "how many days a meeting recording's minutes stay in the knowledge base. Past a finite " +
				"assistant.recording_text_days its transcript, summary and live log are deleted and only the minutes (and the " +
				"recording's metadata) stay, still searchable and readable with kb_get, until this many days after the meeting; " +
				"then the rest goes too. Never fewer than assistant.recording_text_days (forever while the text is kept " +
				"forever). 0 (or forever) keeps minutes until the user deletes them, which is the default",
			Value: "0 or forever, or a whole number of days, 1–3650"},
			get: func(ctx context.Context) (string, error) {
				return minutesDaysLabel(s.lang(ctx), s.minutesDays(ctx)), nil
			},
			set: s.setMinutesDays},
		{Setting: Setting{Key: "assistant.kb_topics", Title: "Organize knowledge points by topic", Actions: getSet,
			About: "on (the default): the knowledge base pulls durable knowledge (concepts, decisions, practices, pitfalls, facts, open " +
				"questions) out of the projects' timelines and the chats' summaries and files it by topic (kind topics), a few model calls " +
				"a round on the knowledge base's model; off: no more calls, the topics already written stay",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.kbTopics(ctx)), nil },
			set: s.setKBTopics},
		{Setting: Setting{Key: "assistant.main_background_folded", Title: "Fold background rounds", Actions: getSet,
			About: "on (the default): in the apps, a main-conversation round the user did not start (another conversation's " +
				"conversation_send / conversation_new message, a scheduled task or follow-up) is one line that opens on request, and " +
				"a scheduled task's prompt is not shown; rounds that asked or told the user something (ask_user, notify_user, a " +
				"task_review verdict card) stay shown. Off: every round is shown in full. Display only: what runs is unchanged",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.mainBackgroundFolded(ctx)), nil },
			set: s.setMainBackgroundFolded},
		{Setting: Setting{Key: "assistant.fold_routine_relay_replies", Title: "Fold routine relay replies", Actions: getSet,
			About: "off by default. When background rounds are folded in the main chat, keep routine replies from other conversations in their expandable round and skip their ordinary finished notification. Explicit owner notifications and errors retain their normal behavior.",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) {
				return fmt.Sprint(s.settings(ctx).foldRoutineRelayReplies()), nil
			},
			set: s.setFoldRoutineRelayReplies},
		{Setting: Setting{Key: "assistant.main_compact_window", Title: "Compaction window for the main chat and leader conversations", Actions: getSet,
			About: "400000 (the default): the main conversation and each leader conversation (a direction's lead, " +
				"assistant.leaders) are compacted at about this many tokens (and sent /compact when idle at 75% of it) instead " +
				"of their model's whole window, when they run on a launcher Persona manages that declares a larger one; fewer " +
				"tokens re-read every turn and rewritten after a long pause, while early details stay in the task ledger and the " +
				"old conversation. 0: the launcher's window, as for every other conversation and Agent. Applies from each one's " +
				"next message while it is idle",
			Value: "0, or a whole number of tokens, 100000–10000000"},
			get: func(ctx context.Context) (string, error) {
				return fmt.Sprint(mainCompactWindowOf(s.settings(ctx))), nil
			},
			set: s.setMainCompactWindow},
		{Setting: Setting{Key: "assistant.recent_task_chats", Title: "Show scheduled task chats in Recent", Actions: getSet,
			About: "off (the default): the sidebar's Recent in the apps leaves out the chats a scheduled task or the background " +
				"supervisor started (they stay in All chats and under Scheduled Tasks); on: Recent lists them by last activity with " +
				"the user's other chats, within its usual count. One value for all of the user's devices. Display only: what runs " +
				"is unchanged",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.recentTaskChats(ctx)), nil },
			set: s.setRecentTaskChats},
		{Setting: Setting{Key: "assistant.meeting_attendance_default", Title: "Persona in recorded meetings", Actions: getSet,
			About: "the role a meeting recording on the phone pre-selects (the user can still change it when it starts): off (the " +
				"default) only records and makes minutes afterwards; listener follows the meeting in a session and answers questions " +
				"on the phone's screen; participant also says the answers aloud. Applies to recordings started from now on",
			Value: "off, listener or participant"},
			get: func(ctx context.Context) (string, error) { return s.meetingAttendanceDefault(ctx), nil },
			set: s.setMeetingAttendanceDefault},
		{Setting: Setting{Key: "assistant.meeting_transcription_default", Title: i18n.T(i18n.Default, "assistant-settings_tool.title_meeting_transcription_default"), Actions: getSet,
			About: prompts.RenderMeetingTranscriptionSetting(), Value: "device or hub"},
			get: s.getMeetingTranscriptionDefault, set: s.setMeetingTranscriptionDefault},
		{Setting: Setting{Key: "assistant.delivery_gate", Title: i18n.T(i18n.Default, "assistant-settings_tool.title_delivery_gate"), Actions: getSet,
			About: "merged (the default): supervised tasks with linked PR(s) or GitHub issue(s) stay open until the PR is merged " +
				"and linked issue(s) are closed (unless explicitly PR-only/draft-only or outcome cannot). off: close strictly on " +
				"done_when with no delivery gate. live: also require the change to be deployed and live on the running hub " +
				"(equivalent to assistant.done_needs_delivery=true). Applies from the next judgment / review",
			Value: "off, merged or live"},
			get: func(ctx context.Context) (string, error) { return s.deliveryGate(ctx), nil },
			set: s.setDeliveryGate},
		{Setting: Setting{Key: "assistant.done_needs_delivery", Title: "上线并实测才算做完", Actions: getSet,
			About: "legacy boolean alias for assistant.delivery_gate: true sets delivery_gate to live (close only once live and " +
				"checked, and hand the delivery standard to the agent); false sets delivery_gate to off (no delivery gate). " +
				"Prefer assistant.delivery_gate (off / merged / live). Either way the judge never closes a supervised task: delivery " +
				"waits for its supervisor's task_review. Applies from the next judgment / task",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.doneNeedsDelivery(ctx)), nil },
			set: s.setDoneNeedsDelivery},
		{Setting: Setting{Key: "assistant.review_sample_percent", Title: "Share of finished tasks to spot-check", Actions: getSet,
			About: "100 (the default): the done judge adds no per-criterion check. Below 100 it also checks each done_when criterion " +
				"and the delivery as evidence for review. Passing, failing, sampled and unsampled work all stay open as delivered, " +
				"awaiting their supervisor's task_review; the judge never accepts or closes a task. Applies from the next judgment on",
			Value: "a whole number, 0–100"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.reviewSamplePercent(ctx)), nil },
			set: s.setReviewSamplePercent},
		{Setting: Setting{Key: "assistant.leaders", Title: "Leader conversations", Actions: getSet,
			About: "empty (the default): ordinary conversations use main for supervision. A direction lead supervises the tasks it " +
				"plans or starts, and Agents explicitly assigned to it: ordinary signals, question cards, failed starts, retries, " +
				"task_review and cancellation stay with that lead. Only needs_user escalations go to the main conversation. " +
				"Main supervises its own tasks; explicit agent_transfer assignments persist across manager restarts",
			Value: "the whole list of leader conversation ids (conversations_list), [] for none"},
			get: s.getLeaders,
			set: s.setLeaders},
		{Setting: Setting{Key: "assistant.briefing_waits", Title: "Briefing wait rows", Actions: getSet,
			About: "thresholds for the deterministic per-direction wait rows in morning/evening facts. Defaults come from the existing " +
				"supervision floors: stalled_minutes=20 (superviseStallMin), silent_minutes=120 (superviseSilentMin), " +
				"orphan_minutes=10 (orphanOfflineAfter), card_minutes=3 (superviseNeedsAfter); max_rows=15 bounds only non-owner " +
				"rows per direction, and every owner-wait row is kept. These values do not change supervision notifications",
			Value: "the whole object {stalled_minutes, silent_minutes, orphan_minutes, card_minutes, max_rows}; minutes 1–10080, max_rows 1–100; null restores the defaults"},
			get: s.moduleSettingGet("assistant.briefing_waits"),
			set: s.moduleSettingSet("assistant.briefing_waits", "change_briefing_waits",
				"assistant.briefing_waits updated; the next briefing's deterministic wait lists use it.")},
		{Setting: Setting{Key: "assistant.interject_background", Title: "Move running work to the background when I interject", Actions: getSet,
			About: "off (the default): a message the user sends while a Claude conversation is still answering waits until the command " +
				"or subagent running in the foreground ends; on: that work moves to the background (it goes on and reports when done) " +
				"and the message is answered at once. " +
				"Either way, a message the user sends into a round they did not start (supervision, a scheduled task or follow-up, another " +
				"conversation's message, a task's notice) moves that round's work to the background. " +
				"Needs Claude Code 2.1.139 or later on the conversation's computer; applies from the next message",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.interjectBackground(ctx)), nil },
			set: s.setInterjectBackground},
		{Setting: Setting{Key: "assistant.claude_interactive", Title: "Run Claude chats in interactive Claude Code", Actions: getSet,
			About: "off (the default): Claude assistant conversations run Claude Code in print mode (claude -p), as always. on: they run " +
				"the real interactive Claude Code in a private terminal on their computer instead, with the same instructions, tools, " +
				"permission mode and settings. Why: people report that on a Claude subscription print mode uses the 5-hour limit faster " +
				"than interactive use (Anthropic does not document this). Risk: Persona then drives a program made for a person, which " +
				"Anthropic may treat as a different kind of use than it is; an account restriction is possible. Changes: replies show " +
				"a block at a time instead of word by word; images reach Claude as files it reads; cancelling a queued message or Send " +
				"now is not available. Needs tmux on that computer; when the interactive start fails the conversation runs in print mode " +
				"and says why (Settings › Assistant › Runs on), trying again 10 minutes later. Applies to each conversation's next " +
				"start; a running idle one restarts with its next message",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.claudeInteractive(ctx)), nil },
			set: s.setClaudeInteractive},
		{Setting: Setting{Key: "assistant.load_all_tools", Title: "Load every Persona tool up front", Actions: getSet,
			About: "off (the default): only the Persona tools conversations use often are sent in full with every request; the " +
				"others are listed by name and loaded with ToolSearch when needed, which keeps every request smaller. on: every " +
				"Persona tool is sent in full with every request, as before. Applies at once: running conversations are sent their " +
				"new tool list and new ones start with it (a conversation on a computer whose Persona is older than tool refresh " +
				"keeps its tools until its process restarts)",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.loadAllTools(ctx)), nil },
			set: s.setLoadAllTools},
		{Setting: Setting{Key: "assistant.agent_tasks", Title: "Every Agent Persona starts is a supervised task", Actions: getSet,
			About: "off (the default): assistant-dispatched task_plan, agent_start and agent_send task work is still supervised by its planning lead or main. " +
				"on: manually linked user to-dos and reporting-Agent compatibility are supervised too; an Agent opened by hand on the Agents " +
				"page is not adopted merely because it did work. Agents started by hand in a terminal are left alone. Costs model calls " +
				"(the done judge, supervision turns). Applies from the next start, turn end and report",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.agentTasks(ctx)), nil },
			set: s.setAgentTasks},
		{Setting: Setting{Key: "assistant.task_lessons", Title: "Learn from finished tasks", Actions: getSet,
			About: "off (the default): nothing is learned from accepted tasks. on: when the supervisor accepts a task that needed " +
				"corrections (a rework round, a criterion not met, the user's answer to one of its questions, a scope change, far " +
				"more time than estimated), a background model (Information processing) writes up to three lessons for the task's " +
				"project (a mistake that came back, background the user had to add, a step that wasted time, a better way to work), " +
				"and every new Agent of that project gets the most repeated ones before its first prompt. A smooth task makes no " +
				"model call. The user sees, edits and deletes the lessons on the project's page in Knowledge; a deleted one is not " +
				"learned again. Applies from the next acceptance and the next new Agent",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.taskLessons(ctx)), nil },
			set: s.setTaskLessons},
		{Setting: Setting{Key: "assistant.close_ended_terminals", Title: "Close an Agent's terminal when it ends", Actions: getSet,
			About: "off (the default): an Agent's terminal tab stays open after it stops, with the shell it ran in, until the user " +
				"closes it. on: when an Agent Persona started ends, Persona closes the tab it opened for it, the way Archive and " +
				"close does: when its task is done or cancelled (an idle Agent), when it is stopped for being idle (idle_close_hours), " +
				"and when it quits by itself or is told to (/exit). " +
				"Never closed: a busy Agent (working, waiting for an answer, background work, subagents or workflows running, a " +
				"/loop, queued messages, a server it runs), one the user stopped with Stop, and " +
				"terminals Persona did not open. The conversation is kept and can be resumed in a new tab. Applies from the next " +
				"Agent that ends",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.closeEndedTerminals(ctx)), nil },
			set: s.setCloseEndedTerminals},
		{Setting: Setting{Key: "assistant.inbox_signals", Title: "Tell a task's supervisor about new Inbox messages on it", Actions: getSet,
			About: "off (the default): an Inbox message the AI twin's judge links to a task only reaches the twin's handling chat " +
				"(and a message card once it drafts a reply). on: new messages from other people linked to a supervised task also " +
				"reach the task's supervisor (its leader conversation, else the main conversation) as an inbox entry of the next " +
				"supervision message, several collapsed into one per task with their count. Needs the Inbox AI twin, which makes the " +
				"link; Persona's own sends, bot chats and tasks waiting for the user's decision never raise one. Costs supervisor " +
				"turns. Applies from the next new message",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.inboxSignalsOn(ctx)), nil },
			set: s.setInboxSignals},
		{Setting: Setting{Key: "assistant.auto_supervision_mode", Title: "Automatic supervision messages", Actions: getSet,
			About: "all (the default): the assistant sends every follow-up message about supervised tasks on its own (they wake the " +
				"main conversation, a leader or the background supervisor, and each costs a model turn). outcomes: only the one-shot " +
				"ones a supervisor has to act on, when a task finishes for review, stops unfinished, fails, its Agent reports done, " +
				"blocked or a question, or a decision or the user's attention is needed; the routine checks while a task runs " +
				"(stalled, no progress, estimates, review nudges, schedule checks, the parked summary, PR and Inbox side signals) " +
				"are dropped, and those still waiting are withdrawn once. off: it sends none, queues none and schedules none, and " +
				"what was waiting is withdrawn once. Not affected: the task ledger and states, the user's own questions and " +
				"task_review, approval and permission cards, and the safety checks. A wider mode takes only what happens " +
				"afterwards. Applies at once",
			Value: "all, outcomes or off"},
			get: func(ctx context.Context) (string, error) { return s.supervisionMode(ctx), nil },
			set: s.setAutoSupervisionMode},
		{Setting: Setting{Key: supervisionTriageSettingKey, Title: i18n.T(i18n.Default, "assistant-settings_tool.title_supervision_triage"), Actions: getSet,
			About: "off (the default) sends eligible routine reminders through ordinary delivery. on asks the Sorting model " +
				"whether each eligible reminder should wake the supervisor or wait for the morning and evening briefings. " +
				"Applies to new decisions as soon as the owner approves the change",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.supervisionTriageOn(ctx)), nil },
			set: s.setSupervisionTriage},
		{Setting: Setting{Key: "assistant.auto_supervision_messages", Title: "Automatic supervision messages", Actions: getSet,
			About: "older on/off form of assistant.auto_supervision_mode: reads true unless the mode is off; true sets the mode " +
				"to all, false to off (withdrawing what was waiting once). Prefer assistant.auto_supervision_mode (all / outcomes / " +
				"off). Applies at once",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.autoSupervisionOn(ctx)), nil },
			set: s.setAutoSupervisionMessages},
		{Setting: Setting{Key: "inbox.todo_forward.enabled", Title: "Auto-forward PR/Issue Inbox events to the bound to-do's Agent", Actions: getSet,
			About: "off (the default): Inbox ingestion performs zero automatic forwards to task Agents. on: incoming Inbox items " +
				"with an exact canonical GitHub PR or Issue URL bound to an open to-do's pr_url or todo_repositories are " +
				"deterministically forwarded (zero LLM calls) to that to-do's latest bound Agent (direct send if idle, queued if " +
				"working, resumed on non-user stops), wrapped in an untrusted-content envelope with self-author suppression and " +
				"idempotent deduplication; unavailable Agents fall back to the supervising conversation. Applies from the next " +
				"Inbox round",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.inboxTodoForwardOn(ctx)), nil },
			set: s.setInboxTodoForward},
		{Setting: Setting{Key: "assistant.pr_events_route_to_agent", Title: "Route PR events directly to the bound to-do Agent", Actions: getSet,
			About: "off (the default): pr_status keeps existing supervision behavior unchanged. on: new PR merge conflicts, current-head " +
				"CI failures, and current-head changes_requested or actionable review events on an open to-do route deterministically " +
				"to its bound Agent (direct send when idle, queued behind the current turn when working, or non-user resume when stopped), " +
				"deduplicated per event, with fallback to the supervisor wake path when no usable Agent is available",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.prEventsRouteToAgentOn(ctx)), nil },
			set: s.setPREventsRouteToAgent},
		{Setting: Setting{Key: "assistant.main_supervises_own_tasks", Title: "The main chat supervises its own tasks", Actions: getSet,
			About: "compatibility only, whether off (the default) or on: the main conversation supervises every new task no leader " +
				"supervises and receives its supervision messages. The main conversation still hears of failed starts with no current " +
				"leader and of what an already configured background conversation hands to the user; that background only finishes its " +
				"previously assigned supervision notices, and no background conversation is created or selected for new work. Changing " +
				"the setting does not move existing notices or stop tasks",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.mainSupervisesOwnTasks(ctx)), nil },
			set: s.setMainSupervisesOwnTasks},
		{Setting: Setting{Key: "assistant.supervision_batch_minutes", Title: "Routine supervision batching per recipient", Actions: getSet,
			About: "0 (the default) keeps the existing supervision gather and shared spacing. A nonzero interval batches routine signals independently for each final recipient conversation, anchored at that recipient's first routine signal. needs_you, ask, escalated, decide, decision_reply, park_decided, owner_cannot_answer, recheck, undecidable and failed bypass the batch after the existing 15-second gather; any one of them releases that recipient's whole digest. Due batches still use the recipient's existing busy queue",
			Value: "a whole number from 0 to 1440 minutes"},
			get: func(ctx context.Context) (string, error) {
				return fmt.Sprint(supervisionBatchMinutesOf(s.settings(ctx))), nil
			},
			set: s.setSupervisionBatchMinutes},
		{Setting: Setting{Key: "assistant.background_supervisor_keeps_context", Title: "The background supervisor keeps every round in context", Actions: getSet,
			About: "off (the default): each round of the background supervisor starts from its first turn, in the same conversation: " +
				"it reads its start brief and that round's supervision message, with only the tools supervision uses; what " +
				"earlier rounds settled is on the tasks (brief, scope changes, decisions, the last review). on: from the next " +
				"round, one long conversation again that keeps each later round in context, with all of the assistant's tools, " +
				"as before (rounds already run from the first " +
				"turn stay on their own branches, out of that context). Applies from the next round (one already running " +
				"finishes as it is)",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) {
				return fmt.Sprint(backgroundKeepsContext(s.settings(ctx))), nil
			},
			set: s.setBackgroundSupervisorKeepsContext},
		{Setting: Setting{Key: "assistant.chat_reply_relay", Title: "Tell chats where it wrote as me about new replies", Actions: getSet,
			About: "off (the default): nothing is passed on. on: after a conversation wrote into a Lark chat as the user, other " +
				"people's new messages in that chat are told to that conversation (once, at most every 10 minutes per chat), until " +
				"the owner personally speaks in that chat or mutes the chat via assistant.chat_reply_relay_muted_chats. Turning it off drops those still waiting in a conversation's queue",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) {
				return fmt.Sprint(chatReplyRelayOn(s.settings(ctx))), nil
			},
			set: s.setChatReplyRelay},
		s.chatReplyMutedChatsSetting(),
		{Setting: Setting{Key: "assistant.launcher", Title: "Persona 的启动器", Actions: getSet,
			About: "which launcher (model) new Persona conversations start with by default",
			Value: "a launcher name from this setting's get"},
			get: s.getLauncher,
			set: s.setLauncher},
		{Setting: Setting{Key: "assistant.limit_fallbacks", Title: "Backup launchers for a usage limit", Actions: getSet,
			About: "[] (the default): a conversation a subscription's usage limit stops waits for the reset, and the main chat, the " +
				"leaders and the background supervisor are continued a minute after it, as before. A list of the assistant's " +
				"launchers, first choice first: when the limit stops the main chat, a leader, the background supervisor or a " +
				"side chat answering the owner, and it resets 10 minutes or more later, the conversation moves to the first " +
				"listed launcher it is not on that is not itself failing (launcher health) and that its role can run on, with " +
				"the usual summary of what went before, and goes on with the work there; a minute after the reset it moves " +
				"back to the launcher it was on, unless someone switched it meanwhile. Coding Agents and spaces are never " +
				"moved; a backup that meets its own limit waits for its reset. Once moved, a conversation keeps the launcher " +
				"and model it was moved to, as after any switch: a later change of the main chat's model setting applies to " +
				"the next new main chat, and the chat's own model menu still changes it",
			Value: "a JSON list of launcher names from this setting's get, at most 8; [] turns it off"},
			get: s.getLimitFallbacks,
			set: s.setLimitFallbacks},
		{Setting: Setting{Key: "assistant.machine_fallbacks", Title: "Backup computers when offline", Actions: getSet,
			About: "[] (the default): automatic cross-machine failover is off; the main chat, direction leaders and default " +
				"scheduled routines stay on their configured computer while it is offline. An ordered list of non-work computer " +
				"IDs, first choice first: when the computer hosting the main chat or an active direction leader stays offline " +
				"longer than assistant.machine_fallback_minutes, Persona moves it to the first listed computer that is online, " +
				"not a work computer, and has a healthy launcher supporting supervision policy, carrying over persisted history " +
				"and summary and draining queued messages. Unbound default scheduled routines also fall back to the first " +
				"eligible online backup computer. Manual conversation changes are never overwritten (epoch fenced)",
			Value: "a JSON list of non-work computer IDs from this setting's get, at most 8; [] turns it off"},
			get: s.getMachineFallbacks,
			set: s.setMachineFallbacks},
		{Setting: Setting{Key: "assistant.machine_fallback_minutes", Title: "Offline minutes before computer failover", Actions: getSet,
			About: "how many minutes a computer must stay offline before assistant.machine_fallbacks moves the main chat, " +
				"direction leaders or unbound default scheduled routines to a backup computer; 10 minutes by default",
			Value: "a whole number of minutes, 1–1440"},
			get: func(ctx context.Context) (string, error) {
				return fmt.Sprint(machineFallbackMinutesOf(s.settings(ctx))), nil
			},
			set: s.setMachineFallbackMinutes},
		{Setting: Setting{Key: modelProfileBindingsKey, Title: "Model profile purpose and launcher bindings", Actions: getSet,
			About: "one model profile's purpose bindings and managed-launcher model fields; set atomically patches the named profile at the revision read for its approval card, without applying current settings or changing tiers",
			Value: `{"profile": id from model_profiles_list, "purposes": {purpose id: {provider, model, effort, permission_mode} | null}, "launchers": {launcher name: {model field: string | null}}}; a purpose object replaces that whole binding, null or {} inherits on the next switch; omitted launcher fields stay unchanged, null or "" clears to the launcher default on the next switch (model cannot be cleared)`},
			get: s.getModelProfileBindings,
			set: s.setModelProfileBindings},
		{Setting: Setting{Key: "model_profiles.tiers", Title: "Agent presets (the model profiles' tiers)", Actions: getSet,
			About: "the Agent presets: each model profile's named tiers, what agent_start / task_plan's tier and the New Agent " +
				"form's presets run on (launcher, model, effort) and when to use each; get lists every profile's, set adds, " +
				"changes or removes one tier, in every profile unless profiles names some (a switch then keeps it)",
			Value: modelTiersValue},
			get: s.getModelTiers,
			set: s.setModelTiers},
		{Setting: Setting{Key: admissionSettingsKey, Title: "Execution Agent admission gate", Actions: getSet,
			About: "the lines an execution Agent's start is checked against on every computer: enabled, load_per_core, min_memory_bytes, " +
				"max_claude_procs, max_swap_percent (and the hub machine's own hub_* lines, per-machine overrides), and the per-direction cap. " +
				"direction_cap_by_direction keys are a supervisor conversation id or a direction's displayed name and win over " +
				"direction_cap_by_project, which wins over direction_cap and the code default. launcher_groups (none by default) caps " +
				"running tasks across launchers that share one budget, on every computer: each {name, launchers (exact launcher names), " +
				"cap}, a launcher in one group at most; a task held by a full group stays queued until it has room. " +
				"Get first and send the whole object",
			Value: "the full AdmissionSettings JSON object from get; a nil pointer keeps the code default, every set line must be positive"},
			get: s.getAdmission,
			set: s.setAdmission},
		// Dashboards (#15017, pages_tool.go): read only. The owner turns it on in Settings › Assistant; a change from
		// here would be a phone card the card ledger counts as a bug.
		{Setting: Setting{Key: dashboardsSettingKey, Title: i18n.T(i18n.Default, "assistant-settings_tool.title_dashboards"), Actions: []string{"get"},
			About: "whether conversations may draft dashboards with the page tool (pages the owner keeps in the sidebar with Keep on a preview card); " +
				"off by default; only the owner changes it, in Settings › Assistant",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) { return fmt.Sprint(s.dashboardsOn(ctx)), nil }},
		{Setting: Setting{Key: todosV2AutoSeenSettingKey, Title: "Auto-clear unseen badge on completed tasks (todos_v2)", Actions: getSet,
			About: "controls whether autonomously completed to-dos auto-clear unseen badges under todos_v2=true: all_autonomous (default), subtasks (part_of children only), or off; no effect when todos_v2 is off",
			Value: "off, subtasks or all_autonomous"},
			get: func(ctx context.Context) (string, error) { return s.settings(ctx).todosV2AutoSeenMode(), nil },
			set: s.setTodosV2AutoSeen},
		{Setting: Setting{Key: todosV2PendingReviewSLASettingKey, Title: "Pending review quiet SLA hours (todos_v2)", Actions: getSet,
			About: "how many hours pending_review tasks stay quiet in Working under todos_v2=true before escalating to Needs you: 0 (off, immediate Needs you), 12, 24 (default), or 48; no effect when todos_v2 is off",
			Value: "0, 12, 24 or 48"},
			get: func(ctx context.Context) (string, error) {
				return fmt.Sprint(s.settings(ctx).todosV2PendingReviewSLAHours()), nil
			},
			set: s.setTodosV2PendingReviewSLAHours},
		{Setting: Setting{Key: todosV2CanWaitSLASettingKey, Title: "Can-wait decision aging SLA hours (todos_v2)", Actions: getSet,
			About: "how many hours non-critical can_wait decisions wait under todos_v2=true before aging into Needs you: 0 (off, never auto-escalate), 24, 48 (default), or 72; no effect when todos_v2 is off",
			Value: "0, 24, 48 or 72"},
			get: func(ctx context.Context) (string, error) {
				return fmt.Sprint(s.settings(ctx).todosV2CanWaitSLAHours()), nil
			},
			set: s.setTodosV2CanWaitSLAHours},
		{Setting: Setting{Key: todosV2FoldChildrenSettingKey, Title: "Fold child tasks under parent goals (todos_v2)", Actions: getSet,
			About: "on (default): non-urgent child tasks fold under their top-level parent goal in todos_v2 board projection; off: show child tasks flat; no effect when todos_v2 is off",
			Value: "true or false"},
			get: func(ctx context.Context) (string, error) {
				return fmt.Sprint(s.settings(ctx).todosV2FoldChildrenOn()), nil
			},
			set: s.setTodosV2FoldChildren},
		{Setting: Setting{Key: todosV2QuietHoursSettingKey, Title: "Quiet hours for routine notifications (todos_v2)", Actions: getSet,
			About: "optional 24-hour local-time quiet window (HH:MM-HH:MM, e.g. 22:00-08:00 or 13:00-15:00) under todos_v2=true during which routine non-card, non-P0, non-notify_user pushes downgrade to LevelP2; empty string disables quiet hours (default); P0, cards, approvals, and notify_user are never muted; no effect when todos_v2 is off",
			Value: `"" (off) or "HH:MM-HH:MM"`},
			get: func(ctx context.Context) (string, error) {
				return s.settings(ctx).todosV2QuietHours(), nil
			},
			set: s.setTodosV2QuietHours},
		{Setting: Setting{Key: todosV2DigestWindowMinutesSettingKey, Title: "Routine notification digest window in minutes (todos_v2)", Actions: getSet,
			About: "optional burst-coalescing window in minutes under todos_v2=true: 0 (off, default), 30, 60, or 120; follow-up routine non-card, non-P0, non-notify_user pushes for the same session/direction within the window downgrade to LevelP2; P0, cards, approvals, and notify_user are never muted; no effect when todos_v2 is off",
			Value: "0, 30, 60 or 120"},
			get: func(ctx context.Context) (string, error) {
				return fmt.Sprint(s.settings(ctx).todosV2DigestWindowMinutes()), nil
			},
			set: s.setTodosV2DigestWindowMinutes},
		s.trustSetting(),
		s.urgentRulesSetting(), // urgent_call_rules.go
	}
}

func (s *Service) managedOwnSettings(ctx context.Context) []ownSetting {
	own := append(s.ownSettings(), s.modelGroupOwnSettings()...)
	return append(own, s.directionOwnSettings(ctx)...)
}

func (s *Service) lookupManagedOwnSetting(ctx context.Context, key string) (ownSetting, bool, error) {
	for _, setting := range s.managedOwnSettings(ctx) {
		if setting.Key == key {
			return setting, true, nil
		}
	}
	return s.orphanTaskOwnSetting(ctx, key)
}

func (s *Service) checkPersonaSettingLocation(ctx context.Context, call ToolCall, a personaSettingsInput) error {
	if a.Key == standingAuthorizationsKey && a.Action == "set" && (call.SessionID == "" || call.SessionID != s.settings(ctx).Main || call.MachineID != s.settings(ctx).MachineID) {
		return errors.New("only the main conversation may change standing authorizations")
	}
	if _, ok := directionPromptSettingID(a.Key); ok && a.Action == "set" && (call.SessionID == "" || call.SessionID != s.settings(ctx).Main) {
		return errors.New("only the main conversation may change direction prompts")
	}
	return nil
}

func (s *Service) toolPersonaSettings(ctx context.Context, call ToolCall, args json.RawMessage) proto.ToolResult {
	var a personaSettingsInput
	if err := json.Unmarshal(args, &a); err != nil {
		return proto.TextResult(fmt.Sprintf("invalid arguments: %v", err), true)
	}
	if a.Action == "remove" && len(a.Value) == 0 && a.ID != "" {
		a.Value, _ = json.Marshal(a.ID)
	}
	var execution *ownerActionExecution
	if a.Action == "set" || a.Action == "add" || a.Action == "remove" {
		var err error
		execution, err = s.consumeOwnerBuiltin(ctx, call.MachineID, call.SessionID, "persona_settings", args)
		if err != nil {
			return proto.TextResult(err.Error(), true)
		}
	}
	fail := func(err error) proto.ToolResult {
		if execution != nil {
			_, finishErr := s.finishOwnerBuiltin(ctx, *execution, "", err)
			if finishErr != nil {
				return proto.TextResult(finishErr.Error(), true)
			}
		}
		return proto.TextResult(err.Error(), true)
	}
	if err := s.checkPersonaSettingLocation(ctx, call, a); err != nil {
		return fail(err)
	}
	own := s.managedOwnSettings(ctx)
	if a.Action == "" || a.Action == "list" {
		text, err := s.listSettings(ctx, own)
		if err != nil {
			return proto.TextResult(err.Error(), true)
		}
		return proto.TextResult(text, false)
	}
	setting, found, err := s.lookupManagedOwnSetting(ctx, a.Key)
	if err != nil {
		return fail(err)
	}
	if found {
		text, err := s.ownSettingActionExact(ctx, call, setting, a.Action, a.Value, execution)
		if err != nil {
			return proto.TextResult(err.Error(), true)
		}
		return proto.TextResult(text, false)
	}
	if execution != nil {
		return fail(fmt.Errorf("%s is no longer a managed built-in setting", a.Key))
	}
	if _, ok := directionPromptSettingID(a.Key); ok {
		return proto.TextResult(errNoDirection.Error(), true)
	}
	// A disabled manager module still owns its stored setting. Its key does
	// not become a hub or extension setting when the module is unavailable.
	if moduleSettingKeys[a.Key] {
		return proto.TextResult(unavailableModuleSetting(a.Key).Error(), true)
	}
	return s.appTool(ctx, AppSettings, call, args) // the hub's
}

// listSettings is action list: the assistant's settings and the hub's, by key.
func (s *Service) listSettings(ctx context.Context, own []ownSetting) (string, error) {
	var res struct {
		Settings []Setting `json:"settings"`
	}
	if err := s.appCall(ctx, AppSettings, SettingsList, map[string]any{}, &res); err != nil {
		return "", err
	}
	// The manager supplies its module keys when loaded and hides them when
	// disabled, even if the hub returns an older copy of the catalog.
	all := slices.DeleteFunc(res.Settings, func(st Setting) bool { return moduleSettingKeys[st.Key] })
	for _, o := range own {
		all = append(all, o.Setting)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Key < all[j].Key })
	var b strings.Builder
	b.WriteString("Settings you can read and (with the user's approval on the phone) change:\n")
	for _, st := range all {
		fmt.Fprintf(&b, "- %s (%s): %s. Actions: %s.", st.Key, st.Title, st.About, strings.Join(st.Actions, ", "))
		if st.Value != "" {
			fmt.Fprintf(&b, " Value: %s.", st.Value)
		}
		b.WriteByte('\n')
	}
	b.WriteString("Routines and follow-ups have their own tools (schedule_routine, schedule_followup); knowledge base tags are kb_update. " +
		"Not changeable here: tokens, pairing, devices, the machines the hub reaches, permission modes.")
	return b.String(), nil
}

// settingCardWait is how long a change's approval card waits for the user before it expires (and changes nothing).
var settingCardWait = 15 * time.Minute

// prepareOwnSettingAction runs a setter's validation without publishing a card or writing anything.
func (s *Service) prepareOwnSettingAction(ctx context.Context, o ownSetting, action string, value json.RawMessage) (*ownChange, error) {
	switch {
	case action == "set" && o.set != nil:
		return o.set(ctx, value)
	case action != "set" && o.change != nil && slices.Contains(o.Actions, action):
		return o.change(ctx, action, value)
	default:
		return nil, fmt.Errorf("%s does not take %q (it takes %s)", o.Key, action, strings.Join(o.Actions, ", "))
	}
}

// ownSettingAction reads one of the assistant's settings, or changes it once the user approves on the phone.
func (s *Service) ownSettingAction(ctx context.Context, call ToolCall, o ownSetting, action string, value json.RawMessage) (string, error) {
	return s.ownSettingActionExact(ctx, call, o, action, value, nil)
}

// ownSettingActionExact executes an optional grant already consumed by toolPersonaSettings. Consuming before setting
// resolution and validation ensures that every failure of an approved item records its position and stops later items.
// Without a grant the ordinary approval path is byte-for-byte unchanged.
func (s *Service) ownSettingActionExact(ctx context.Context, call ToolCall, o ownSetting, action string, value json.RawMessage, execution *ownerActionExecution) (string, error) {
	if action == "get" {
		return o.get(ctx)
	}
	change, err := s.prepareOwnSettingAction(ctx, o, action, value)
	if err != nil {
		if execution != nil {
			return s.finishOwnerBuiltin(ctx, *execution, "", err)
		}
		return "", err
	}
	l := s.lang(ctx)
	proposal := change.card(l)
	if execution != nil {
		if change.apply == nil || !ownerChangeMatches(change, proposal, execution.Item) {
			return s.finishOwnerBuiltin(ctx, *execution, "", fmt.Errorf("the setting changed after the batch card was shown: %w", approvalops.ErrStale))
		}
		text, runErr := change.apply(ctx)
		return s.finishOwnerBuiltin(ctx, *execution, text, runErr)
	}
	if change.apply == nil {
		return change.summary, nil // nothing to change
	}
	title := o.Title
	if o.cardTitle != nil {
		title = o.cardTitle(l)
	} else if k, ok := settingTitles[o.Key]; ok {
		title = i18n.T(l, "assistant-settings_tool."+k)
	}
	shown, _ := json.Marshal(map[string]string{"setting": title, "action": action})
	now := time.Now().UTC()
	if action == "set" && s.hello(ctx).Assistant.AutoApproveReversibleSettings && reversibleSettingAutoApproved(o.Key) && change.reversible != nil {
		return s.applyAutomaticReversibleSetting(ctx, call, o.Key, proposal, change.reversible)
	}
	toolName := o.toolName
	if toolName == "" {
		toolName = "persona_settings"
	}
	card := proto.Interaction{ID: "ix_" + randomHex(6), MachineID: call.MachineID, SessionID: call.SessionID,
		Kind: proto.IxPermission, ToolName: toolName, Summary: proposal, ToolInput: shown, SessionTitle: "Persona",
		CreatedAt: now, ExpiresAt: now.Add(settingCardWait)}
	op := approvalops.Operation{
		Replacement: call.MachineID + "\x00" + call.SessionID + "\x00setting\x00" + o.Key,
		Denied:      "The user declined the change: nothing changed.",
		Unanswered:  prompts.RenderSettingUnanswered(),
		Apply: func(ctx context.Context, _ proto.Resolution) (string, error) {
			if change.applyChecksProposal {
				// This apply already re-reads and compares the approved revision
				// under its state lock; do not rebase it onto a newer revision.
				return change.apply(ctx)
			}
			// Rebuild from current state: only the exact visible proposal the owner
			// approved may execute, and the freshly validated operation is used.
			fresh, err := s.prepareOwnSettingAction(ctx, o, action, value)
			if err != nil {
				// The setter only rebuilds and validates a fresh operation here;
				// fresh.apply below is the first possible side effect.
				return "", fmt.Errorf("%w: validate the approved operation against current state: %v", approvalops.ErrNotApplied, err)
			}
			if fresh == nil || fresh.apply == nil || fresh.card(l) != proposal {
				return "", approvalops.ErrStale
			}
			return fresh.apply(ctx)
		},
	}
	if call.MachineID == "" && call.SessionID == "" {
		// Settings-page approvals have no conversation to receive questions.
		answer, err := s.ask(ctx, card)
		if err != nil {
			return "", err
		}
		switch {
		case answer.UserAllowed():
			return op.Apply(ctx, answer)
		case answer.UserDenied():
			return op.Denied, nil
		default:
			return op.Unanswered, nil
		}
	}
	outcome, err := s.waitPreparedCard(ctx, card, op)
	return outcome.Text, err
}

// applyAutomaticReversibleSetting records the assistant's decision before attempting the write, then updates the same
// row with saved or not_saved. If the history row cannot be written, the setting is left alone. If the outcome update
// fails, attempted remains as an explicit unknown rather than a false result. The raw before-image is checked again
// under the same lock as the settings write.
func (s *Service) applyAutomaticReversibleSetting(ctx context.Context, call ToolCall, key, proposal string, r *reversibleSetting) (string, error) {
	st := s.fleetStore()
	writer, ok := st.(resolvedInteractionInserter)
	if !ok {
		return "", fmt.Errorf("%w: this store cannot record a reversible-setting decision and outcome", approvalops.ErrNotApplied)
	}
	restoreArguments, _ := json.Marshal(map[string]any{"action": "set", "key": key, "value": r.before})
	restore, _ := json.Marshal(map[string]any{"tool": "persona_settings", "arguments": json.RawMessage(restoreArguments), "while_switch_enabled": autoApproveReversibleSettingsKey})
	audit, _ := json.Marshal(struct {
		Actor       string          `json:"actor"`
		Operation   string          `json:"operation"`
		Setting     string          `json:"setting"`
		Before      json.RawMessage `json:"before"`
		After       json.RawMessage `json:"after"`
		Proposal    string          `json:"proposal"`
		Outcome     string          `json:"outcome"`
		CardSkipped bool            `json:"card_skipped"`
		Switch      string          `json:"switch"`
		Restore     json.RawMessage `json:"restore"`
	}{Actor: proto.AnsweredByAssistant, Operation: "persona_settings.set", Setting: key, Before: r.before, After: r.after,
		Proposal: proposal, Outcome: "attempted", CardSkipped: true, Switch: autoApproveReversibleSettingsKey, Restore: restore})
	allow := true
	now := time.Now().UTC()
	resolution := &proto.Resolution{Allow: &allow, AnsweredBy: proto.AnsweredByAssistant,
		Reason: autoApproveReversibleSettingsKey + "; exactly reversible; restore with persona_settings " + string(restoreArguments)}
	ix := proto.Interaction{ID: "ix_" + randomHex(6), MachineID: call.MachineID, SessionID: call.SessionID,
		Kind: proto.IxPermission, ToolName: "persona_settings", SessionTitle: "Persona", CreatedAt: now, ToolInput: audit,
		Private: s.justMeNow(call.MachineID, call.SessionID), Summary: proposal}
	if err := writer.InsertResolvedInteraction(db.WithEndedBy(ctx, db.EndedByAssistant), ix, resolution); err != nil {
		return "", fmt.Errorf("%w: write the reversible-setting history record: %v", approvalops.ErrNotApplied, err)
	}
	_, saveErr := s.updateSettingsChecked(ctx, func(v *Settings) error {
		r.assign(v)
		return nil
	}, func(v Settings) error {
		if !bytes.Equal(r.current(v), r.before) {
			return fmt.Errorf("%w: %s changed concurrently", approvalops.ErrStale, key)
		}
		return nil
	})
	outcome := "not_saved"
	if saveErr == nil {
		outcome = "saved"
	}
	if err := writer.MarkResolvedInteractionOutcome(ctx, ix.ID, ix.CreatedAt, outcome, time.Now().UTC()); err != nil {
		s.logf("reversible setting %s: record %s outcome for %s: %v", key, outcome, ix.ID, err)
	}
	if saveErr != nil {
		return "", saveErr
	}
	s.settingsChanged(ctx)
	return r.applied, nil
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "（无）"
	}
	return s
}

// settingTitles are the settings' titles for the card the user approves on, in this file's namespace (Title stays
// what the assistant reads).
var settingTitles = map[string]string{modulesKey: "title_modules", standingAuthorizationsKey: "title_standing_authorizations", "assistant.notes": "title_notes", "assistant.main_tasks_quiet": "title_main_tasks_quiet",
	"assistant.idle_auto_compact": "title_idle_auto_compact", "assistant.idle_auto_compact_minutes": "title_idle_auto_compact_minutes", "assistant.idle_auto_compact_tokens": "title_idle_auto_compact_tokens",
	"assistant.agy_idle_auto_compact": "title_agy_idle_auto_compact", "assistant.agy_idle_auto_compact_minutes": "title_agy_idle_auto_compact_minutes", "assistant.agy_idle_auto_compact_tokens": "title_agy_idle_auto_compact_tokens",
	"assistant.idle_release_minutes":      "title_idle_release_minutes",
	readonlyModeKey:                       "title_readonly_mode",
	"assistant.task_workspace_preference": "title_task_workspace_preference",
	"assistant.start_requested_tasks":     "title_start_requested_tasks", "assistant.launcher": "title_launcher", "assistant.limit_fallbacks": "title_limit_fallbacks",
	"assistant.machine_fallbacks": "title_machine_fallbacks", "assistant.machine_fallback_minutes": "title_machine_fallback_minutes",
	"assistant.recording_days": "title_recording_days", "assistant.recording_audio_days": "title_recording_audio_days", "assistant.recording_text_days": "title_recording_text_days", "assistant.delivery_gate": "title_delivery_gate", "assistant.done_needs_delivery": "title_done_needs_delivery",
	"assistant.review_sample_percent": "title_review_sample_percent", "assistant.leaders": "title_leaders", "assistant.briefing_waits": "title_briefing_waits", "assistant.background_supervisor_keeps_context": "title_background_supervisor_keeps_context", "assistant.chat_reply_relay": "title_chat_reply_relay", chatReplyMutedChatsKey: "title_chat_reply_relay_muted_chats",
	"assistant.interject_background": "title_interject_background", "assistant.claude_interactive": "title_claude_interactive", "assistant.agent_tasks": "title_agent_tasks", "assistant.task_lessons": "title_task_lessons", "assistant.close_ended_terminals": "title_close_ended_terminals", "assistant.inbox_signals": "title_inbox_signals", "assistant.auto_supervision_messages": "title_auto_supervision_messages", "assistant.auto_supervision_mode": "title_auto_supervision_mode", "inbox.todo_forward.enabled": "title_inbox_todo_forward", "assistant.pr_events_route_to_agent": "title_pr_events_route_to_agent", "assistant.main_supervises_own_tasks": "title_main_supervises_own_tasks", "assistant.supervision_batch_minutes": "title_supervision_batch_minutes", modelProfileBindingsKey: "title_model_bindings", "model_profiles.tiers": "title_model_tiers",
	supervisionTriageSettingKey:               "title_supervision_triage",
	"assistant.load_all_tools":                "title_load_all_tools",
	"assistant.meeting_transcription_default": "title_meeting_transcription_default",
	"assistant.meeting_attendance_default":    "title_meeting_attendance_default", "assistant.minutes_days": "title_minutes_days",
	"assistant.kb_topics": "title_kb_topics", "assistant.main_background_folded": "title_main_background_folded",
	"assistant.fold_routine_relay_replies": "title_fold_routine_relay_replies",
	"assistant.main_compact_window":        "title_main_compact_window",
	"assistant.recent_task_chats":          "title_recent_task_chats", admissionSettingsKey: "title_admission", trustSetting: "title_task_trust",
	dashboardsSettingKey: "title_dashboards"}

// dashboardsSettingKey is the Dashboards switch as persona_settings names it (Settings.Dashboards, `dashboards` in
// GET/PUT /api/v1/assistant).
const dashboardsSettingKey = "assistant.dashboards"

const (
	todosV2AutoSeenSettingKey            = "assistant.todos_v2_auto_seen"
	todosV2PendingReviewSLASettingKey    = "assistant.todos_v2_pending_review_sla_hours"
	todosV2CanWaitSLASettingKey          = "assistant.todos_v2_can_wait_sla_hours"
	todosV2FoldChildrenSettingKey        = "assistant.todos_v2_fold_children"
	todosV2QuietHoursSettingKey          = "assistant.todos_v2_quiet_hours"
	todosV2DigestWindowMinutesSettingKey = "assistant.todos_v2_digest_window_minutes"
)

// clipped is a value as the card shows it, in l.
func clipped(l i18n.Lang, s string) string {
	if strings.TrimSpace(s) == "" {
		s = i18n.T(l, "assistant-settings_tool.none")
	}
	return clip(oneLine(s), 120)
}

func (s *Service) setIdleAutoCompact(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var value *bool
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, i18n.Err("assistant-settings_tool.bad_idle_auto_compact")
	}
	return s.setBoolSetting(ctx, raw, s.idleAutoCompact, i18n.T(s.lang(ctx), "assistant-settings_tool.idle_auto_compact_unchanged"), func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_idle_auto_compact", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.IdleAutoCompact },
		"Idle compaction setting saved (%v); applies from the next unattended message.")
}

func (s *Service) setIdleAutoCompactMinutes(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var value int
	if err := json.Unmarshal(raw, &value); err != nil || value < minIdleAutoCompactMinutes || value > maxIdleAutoCompactMinutes {
		return nil, i18n.Err("assistant-settings_tool.bad_idle_auto_compact_minutes")
	}
	before := s.idleAutoCompactMinutes(ctx)
	if value == before {
		return &ownChange{summary: i18n.T(s.lang(ctx), "assistant-settings_tool.idle_auto_compact_unchanged")}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_idle_auto_compact_minutes", i18n.Args{"before": before, "after": value})
	}, apply: func(ctx context.Context) (string, error) {
		if _, err := s.updateSettings(ctx, func(v *Settings) error { v.IdleAutoCompactMinutes = &value; return nil }); err != nil {
			return "", err
		}
		s.settingsChanged(ctx)
		return i18n.T(s.lang(ctx), "assistant-settings_tool.idle_auto_compact_saved"), nil
	}}, nil
}

func (s *Service) setIdleAutoCompactTokens(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var value int
	if err := json.Unmarshal(raw, &value); err != nil || value < minIdleAutoCompactTokens || value > maxIdleAutoCompactTokens {
		return nil, i18n.Err("assistant-settings_tool.bad_idle_auto_compact_tokens")
	}
	before := s.idleAutoCompactTokens(ctx)
	// Saving the default is a change while unset: a saved value is also the main chat's release-time threshold.
	if value == before && s.settings(ctx).IdleAutoCompactTokens != nil {
		return &ownChange{summary: i18n.T(s.lang(ctx), "assistant-settings_tool.idle_auto_compact_unchanged")}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_idle_auto_compact_tokens", i18n.Args{"before": before, "after": value})
	}, apply: func(ctx context.Context) (string, error) {
		if _, err := s.updateSettings(ctx, func(v *Settings) error { v.IdleAutoCompactTokens = &value; return nil }); err != nil {
			return "", err
		}
		s.settingsChanged(ctx)
		return i18n.T(s.lang(ctx), "assistant-settings_tool.idle_auto_compact_saved"), nil
	}}, nil
}

func (s *Service) setAgyIdleAutoCompact(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var value *bool
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, i18n.Err("assistant-settings_tool.bad_agy_idle_auto_compact")
	}
	return s.setBoolSetting(ctx, raw, s.agyIdleAutoCompact, i18n.T(s.lang(ctx), "assistant-settings_tool.agy_idle_auto_compact_unchanged"), func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_agy_idle_auto_compact", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.AgyIdleAutoCompact },
		"Antigravity idle compaction setting saved (%v).")
}

func (s *Service) setAgyIdleAutoCompactMinutes(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var value int
	if err := json.Unmarshal(raw, &value); err != nil || !assistantwire.AgyIdleMinutesValid(value) {
		return nil, i18n.Err("assistant-settings_tool.bad_agy_idle_auto_compact_minutes")
	}
	before := s.agyIdleAutoCompactMinutes(ctx)
	if value == before {
		return &ownChange{summary: i18n.T(s.lang(ctx), "assistant-settings_tool.agy_idle_auto_compact_unchanged")}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_agy_idle_auto_compact_minutes", i18n.Args{"before": before, "after": value})
	}, apply: func(ctx context.Context) (string, error) {
		if _, err := s.updateSettings(ctx, func(v *Settings) error { v.AgyIdleAutoCompactMinutes = &value; return nil }); err != nil {
			return "", err
		}
		s.settingsChanged(ctx)
		return i18n.T(s.lang(ctx), "assistant-settings_tool.agy_idle_auto_compact_saved"), nil
	}}, nil
}

func (s *Service) setAgyIdleAutoCompactTokens(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var value int
	if err := json.Unmarshal(raw, &value); err != nil || !assistantwire.AgyIdleTokensValid(value) {
		return nil, i18n.Err("assistant-settings_tool.bad_agy_idle_auto_compact_tokens")
	}
	before := s.agyIdleAutoCompactTokens(ctx)
	if value == before {
		return &ownChange{summary: i18n.T(s.lang(ctx), "assistant-settings_tool.agy_idle_auto_compact_unchanged")}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_agy_idle_auto_compact_tokens", i18n.Args{"before": before, "after": value})
	}, apply: func(ctx context.Context) (string, error) {
		if _, err := s.updateSettings(ctx, func(v *Settings) error { v.AgyIdleAutoCompactTokens = &value; return nil }); err != nil {
			return "", err
		}
		s.settingsChanged(ctx)
		return i18n.T(s.lang(ctx), "assistant-settings_tool.agy_idle_auto_compact_saved"), nil
	}}, nil
}

func (s *Service) setIdleReleaseMinutes(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var value int
	if err := json.Unmarshal(raw, &value); err != nil || value < minIdleReleaseMinutes || value > maxIdleReleaseMinutes {
		return nil, i18n.Err("assistant-settings_tool.bad_idle_release_minutes")
	}
	before := s.idleReleaseMinutes(ctx)
	if value == before {
		return &ownChange{summary: i18n.T(s.lang(ctx), "assistant-settings_tool.idle_release_unchanged")}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_idle_release_minutes", i18n.Args{"before": before, "after": value})
	}, apply: func(ctx context.Context) (string, error) {
		if _, err := s.updateSettings(ctx, func(v *Settings) error { v.IdleReleaseMinutes = &value; return nil }); err != nil {
			return "", err
		}
		s.settingsChanged(ctx)
		return i18n.T(s.lang(ctx), "assistant-settings_tool.idle_release_saved"), nil
	}}, nil
}

func (s *Service) setNotes(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var text string
	if err := json.Unmarshal(v, &text); err != nil {
		return nil, errors.New("value must be a string")
	}
	text = strings.TrimSpace(text)
	before := s.notes(ctx)
	if text == before {
		return &ownChange{summary: "关于我没有变化"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_notes", i18n.Args{"before": clipped(l, before), "after": clipped(l, text)})
	},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.Notes = &text; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			return "关于我 updated; new conversations read it (this one keeps what it started with).", nil
		}}, nil
}

// setBoolSetting changes one of the assistant's on/off settings: v is the new value, current reads it, unchanged is
// the summary when v is what it already is, card the approval card's text (before → after, each "on" or "off" in the
// card's language), field where Settings keeps it, and applied the tool's answer once it is (a format of the value).
func (s *Service) setBoolSetting(ctx context.Context, v json.RawMessage, current func(context.Context) bool, unchanged string,
	card func(l i18n.Lang, before, after string) string, field func(*Settings) **bool, applied string) (*ownChange, error) {
	var on bool
	if err := json.Unmarshal(v, &on); err != nil {
		return nil, errors.New("value must be true or false")
	}
	before := current(ctx)
	if on == before {
		return &ownChange{summary: unchanged}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	word := map[bool]string{true: "assistant-settings_tool.on", false: "assistant-settings_tool.off"}
	return &ownChange{cardText: func(l i18n.Lang) string { return card(l, i18n.T(l, word[before]), i18n.T(l, word[on])) },
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { *field(v) = &on; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			return fmt.Sprintf(applied, on), nil
		}}, nil
}

// setReversibleBoolSetting is deliberately separate from setBoolSetting: only the two display-only candidates accept
// null as raw unset, and only while the administrator switch is on. Switch-on changes compare raw state so an explicit
// default and unset remain exactly restorable even when their effective values match. Switch-off keeps the legacy
// decoder (including null as false) and effective-value no-op.
func (s *Service) setReversibleBoolSetting(ctx context.Context, v json.RawMessage, defaultValue bool,
	current func(context.Context) bool, unchanged string, card func(l i18n.Lang, before, after string) string,
	field func(*Settings) **bool, applied string) (*ownChange, error) {
	auto := s.hello(ctx).Assistant.AutoApproveReversibleSettings
	var next *bool
	if err := json.Unmarshal(v, &next); err != nil {
		return nil, errors.New("value must be true or false")
	}
	if next == nil && !auto {
		next = new(bool) // encoding/json historically decoded null into a non-pointer false for these two settings.
	}
	before := current(ctx)
	after := defaultValue
	if next != nil {
		after = *next
	}
	saved := s.settings(ctx)
	beforeRaw := reversibleSettingJSON(*field(&saved))
	if auto {
		if bytes.Equal(beforeRaw, reversibleSettingJSON(next)) {
			return &ownChange{summary: unchanged}, nil
		}
	} else if after == before {
		return &ownChange{summary: unchanged}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	word := map[bool]string{true: "assistant-settings_tool.on", false: "assistant-settings_tool.off"}
	return &ownChange{
		cardText: func(l i18n.Lang) string { return card(l, i18n.T(l, word[before]), i18n.T(l, word[after])) },
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { *field(v) = next; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			return fmt.Sprintf(applied, after), nil
		},
		reversible: &reversibleSetting{
			before: beforeRaw,
			after:  reversibleSettingJSON(next),
			current: func(v Settings) json.RawMessage {
				return reversibleSettingJSON(*field(&v))
			},
			assign:  func(v *Settings) { *field(v) = next },
			applied: fmt.Sprintf(applied, after),
		},
	}, nil
}

func (s *Service) setMainTasksQuiet(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, s.mainTasksQuiet, "「主对话派的任务只在主对话里通知」没有变化", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_main_tasks_quiet", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.MainTasksQuiet },
		"assistant.main_tasks_quiet is %v for tasks opened from now on (existing ones keep theirs).")
}

func (s *Service) setApprovalMirrorMain(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, s.approvalMirrorMain, "assistant.approval_mirror_main is unchanged", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_approval_mirror_main", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.ApprovalMirrorMain },
		"assistant.approval_mirror_main is %v for new cards.")
}

func (s *Service) setStartRequestedTasks(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, s.startRequestedTasks, "「我在对话里说过的，直接开始」没有变化", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_start_requested_tasks", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.StartRequestedTasks },
		"assistant.start_requested_tasks is %v for plans from now on.")
}

func (s *Service) setTaskWorkspacePreference(ctx context.Context, raw json.RawMessage) (*ownChange, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || !validWorkspacePreference(value) {
		return nil, fmt.Errorf("value must be prefer-existing or prefer-new")
	}
	before := s.taskWorkspacePreference(ctx)
	if before == value {
		return &ownChange{summary: "assistant.task_workspace_preference is unchanged"}, nil
	}
	if s.store() == nil {
		return nil, fmt.Errorf("no database on this manager")
	}
	return &ownChange{
		cardText: func(l i18n.Lang) string {
			return i18n.T(l, "assistant-settings_tool.change_task_workspace_preference", i18n.Args{"before": before, "after": value})
		},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.TaskWorkspacePreference = value; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			return "assistant.task_workspace_preference is " + value + " for new task plans", nil
		},
	}, nil
}

func (s *Service) setMainBackgroundFolded(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setReversibleBoolSetting(ctx, v, true, s.mainBackgroundFolded, "assistant.main_background_folded is unchanged", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_main_background_folded", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.MainBackgroundFolded },
		"assistant.main_background_folded is %v; the apps redraw the main conversation with it.")
}

func (s *Service) setFoldRoutineRelayReplies(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, func(ctx context.Context) bool { return s.settings(ctx).foldRoutineRelayReplies() }, "assistant.fold_routine_relay_replies is unchanged", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_fold_routine_relay_replies", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.FoldRoutineRelayReplies }, "assistant.fold_routine_relay_replies is %v for routine reports in the main conversation.")
}

func (s *Service) setRecentTaskChats(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setReversibleBoolSetting(ctx, v, false, s.recentTaskChats, "assistant.recent_task_chats is unchanged", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_recent_task_chats", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.RecentTaskChats },
		"assistant.recent_task_chats is %v; the apps' sidebar Recent follows it on every device.")
}

func (s *Service) setKBTopics(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, s.kbTopics, "assistant.kb_topics is unchanged", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_kb_topics", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.KBTopics },
		"assistant.kb_topics is %v from the knowledge base's next round on.")
}

// parseRetentionDays reads a retention value of persona_settings: 0 or "forever", or 1–3650 days.
func parseRetentionDays(v json.RawMessage) (int, bool) {
	var word string
	if json.Unmarshal(v, &word) == nil && strings.EqualFold(strings.TrimSpace(word), "forever") {
		return 0, true
	}
	var days int
	if strings.TrimSpace(string(v)) == "null" || json.Unmarshal(v, &days) != nil || !recordingDaysValid(&days, false) {
		return 0, false
	}
	return days, true
}

// setRecordingAudioDays changes recording_audio_days (also the old recording_days): the phone's window.
func (s *Service) setRecordingAudioDays(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setRecordingRetention(ctx, v, s.recordingAudioDays(ctx), "assistant.recording_audio_days",
		"change_recording_audio_days", func(v *Settings, days *int) { v.RecordingAudioDays = days },
		"the phone deletes its own recordings older than that")
}

// setRecordingTextDays changes recording_text_days: how long the knowledge base keeps a recording's text.
func (s *Service) setRecordingTextDays(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setRecordingRetention(ctx, v, s.recordingTextDays(ctx), "assistant.recording_text_days",
		"change_recording_text_days", func(v *Settings, days *int) { v.RecordingTextDays = days },
		"recording text older than that leaves the knowledge base")
}

func (s *Service) setRecordingRetention(ctx context.Context, v json.RawMessage, before int, key, card string,
	save func(*Settings, *int), effect string) (*ownChange, error) {
	days, ok := parseRetentionDays(v)
	if !ok {
		return nil, i18n.Err("assistant-profile.bad_recording_days")
	}
	if days == before {
		return &ownChange{summary: key + " is unchanged"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool."+card, i18n.Args{"before": minutesDaysLabel(l, before), "after": minutesDaysLabel(l, days)})
	},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { save(v, &days); return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			s.kbWake() // a shorter retention prunes now
			if days == 0 {
				return key + " is forever: nothing is deleted for its age.", nil
			}
			return fmt.Sprintf("%s is %d days: %s.", key, days, effect), nil
		}}, nil
}

// minutesDaysLabel spells out the forever sentinel instead of showing a misleading zero-day retention.
func minutesDaysLabel(l i18n.Lang, days int) string {
	if days == 0 {
		return i18n.T(l, "assistant-settings_tool.minutes_forever")
	}
	return i18n.T(l, "assistant-settings_tool.minutes_days_value", i18n.Args{"days": days})
}

// setMinutesDays compares the saved setting, not its recording_text_days floor. A finite value below that floor is
// saved for when recording_text_days becomes shorter. Unset and 0 both mean forever; the card names the effective before/after.
func (s *Service) setMinutesDays(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var days int
	var word string
	forever := json.Unmarshal(v, &word) == nil && strings.EqualFold(strings.TrimSpace(word), "forever")
	if !forever && (strings.TrimSpace(string(v)) == "null" || json.Unmarshal(v, &days) != nil || days < minMinutesDays || days > maxMinutesDays) {
		return nil, i18n.Err("assistant-profile.bad_minutes_days")
	}
	cur := s.settings(ctx)
	saved, recording := minutesDaysSetting(cur), recordingTextDaysOf(cur)
	before, after := kb.MinutesDaysFor(saved, recording), kb.MinutesDaysFor(days, recording)
	args := func(l i18n.Lang) i18n.Args {
		return i18n.Args{"before": minutesDaysLabel(l, before), "after": minutesDaysLabel(l, after),
			"saved": minutesDaysLabel(l, days), "previous": minutesDaysLabel(l, saved)}
	}
	if days == saved {
		return &ownChange{summary: i18n.T(s.lang(ctx), "assistant-settings_tool.minutes_unchanged", args(s.lang(ctx)))}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		if after == before {
			return i18n.T(l, "assistant-settings_tool.change_minutes_days_saved", args(l))
		}
		return i18n.T(l, "assistant-settings_tool.change_minutes_days", args(l))
	},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.MinutesDays = &days; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			s.kbWake() // a shorter retention prunes now
			return i18n.T(s.lang(ctx), "assistant-settings_tool.minutes_applied", args(s.lang(ctx))), nil
		}}, nil
}

// setMeetingAttendanceDefault changes the role meeting recordings pre-select; the card names the roles in the
// user's language.
func (s *Service) setMeetingAttendanceDefault(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var role string
	if err := json.Unmarshal(v, &role); err != nil || !slices.Contains(meetingRoles, strings.TrimSpace(role)) {
		return nil, errors.New("value must be off, listener or participant")
	}
	role = strings.TrimSpace(role)
	before := s.meetingAttendanceDefault(ctx)
	if role == before {
		return &ownChange{summary: "assistant.meeting_attendance_default is unchanged"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	name := func(l i18n.Lang, role string) string { return i18n.T(l, "assistant-settings_tool.meeting_role_"+role) }
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_meeting_attendance_default", i18n.Args{"before": name(l, before), "after": name(l, role)})
	},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.MeetingAttendanceDefault = &role; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			return fmt.Sprintf("assistant.meeting_attendance_default is %s for meeting recordings started from now on.", role), nil
		}}, nil
}

func (s *Service) setDeliveryGate(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var gate string
	if err := json.Unmarshal(v, &gate); err != nil || !slices.Contains(deliveryGates, gate) {
		return nil, errors.New("value must be off, merged or live")
	}
	before := s.deliveryGate(ctx)
	if gate == before {
		return &ownChange{summary: "assistant.delivery_gate is unchanged"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	name := func(l i18n.Lang, tier string) string { return i18n.T(l, "assistant-settings_tool.delivery_gate_"+tier) }
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_delivery_gate", i18n.Args{"before": name(l, before), "after": name(l, gate)})
	},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error {
				v.DeliveryGate = &gate
				v.DoneNeedsDelivery = nil
				return nil
			}); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			return fmt.Sprintf("assistant.delivery_gate is %s from the next judgment and review on.", gate), nil
		}}, nil
}

func (s *Service) setDoneNeedsDelivery(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var onPtr *bool
	if err := json.Unmarshal(v, &onPtr); err != nil || onPtr == nil {
		return nil, errors.New("value must be true or false")
	}
	on := *onPtr
	target := deliveryGateOff
	if on {
		target = deliveryGateLive
	}
	beforeGate := s.deliveryGate(ctx)
	if beforeGate == target {
		return &ownChange{summary: "「上线并实测才算做完」没有变化"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	word := map[bool]string{true: "assistant-settings_tool.on", false: "assistant-settings_tool.off"}
	beforeBool := beforeGate == deliveryGateLive
	return &ownChange{cardText: func(l i18n.Lang) string {
		if beforeGate == deliveryGateMerged && !on {
			return i18n.T(l, "assistant-settings_tool.change_delivery_gate", i18n.Args{
				"before": i18n.T(l, "assistant-settings_tool.delivery_gate_"+beforeGate),
				"after":  i18n.T(l, "assistant-settings_tool.delivery_gate_"+target),
			})
		}
		return i18n.T(l, "assistant-settings_tool.change_done_needs_delivery", i18n.Args{
			"before": i18n.T(l, word[beforeBool]),
			"after":  i18n.T(l, word[on]),
		})
	},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error {
				v.DoneNeedsDelivery = &on
				v.DeliveryGate = nil
				return nil
			}); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			return fmt.Sprintf("assistant.done_needs_delivery is %v from the next judgment and task on.", on), nil
		}}, nil
}

func (s *Service) setReviewSamplePercent(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var pct int
	if err := json.Unmarshal(v, &pct); err != nil || pct < 0 || pct > 100 {
		return nil, errors.New("value must be a whole number, 0–100")
	}
	before := s.reviewSamplePercent(ctx)
	if pct == before {
		return &ownChange{summary: "assistant.review_sample_percent is unchanged"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_review_sample_percent", i18n.Args{"before": before, "after": pct})
	},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.ReviewSamplePercent = &pct; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			return fmt.Sprintf("assistant.review_sample_percent is %d from the next judgment on.", pct), nil
		}}, nil
}

func (s *Service) setInterjectBackground(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, s.interjectBackground, "assistant.interject_background is unchanged", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_interject_background", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.InterjectBackground },
		"assistant.interject_background is %v from the next message on.")
}

func (s *Service) setClaudeInteractive(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, s.claudeInteractive, "assistant.claude_interactive is unchanged", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_claude_interactive", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.ClaudeInteractive },
		"assistant.claude_interactive is %v from each conversation's next start on.")
}

func (s *Service) setAgentTasks(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, s.agentTasks, "assistant.agent_tasks is unchanged", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_agent_tasks", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.AgentTasks },
		"assistant.agent_tasks is %v from the next start, turn end and report on (to-dos made before keep what they have).")
}

func (s *Service) setTaskLessons(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, s.taskLessons, "assistant.task_lessons is unchanged", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_task_lessons", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.TaskLessons },
		"assistant.task_lessons is %v from the next accepted task and the next new Agent on (lessons already learned stay on the project's page).")
}

func (s *Service) setCloseEndedTerminals(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, s.closeEndedTerminals, "assistant.close_ended_terminals is unchanged", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_close_ended_terminals", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.CloseEndedTerminals },
		"assistant.close_ended_terminals is %v for Agents that end from now on (terminals already left open stay).")
}

func (s *Service) setInboxSignals(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, s.inboxSignalsOn, "assistant.inbox_signals is unchanged", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_inbox_signals", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.InboxSignals },
		"assistant.inbox_signals is %v from the next new Inbox message on (signals already waiting go out only while it is on).")
}

func (s *Service) setSupervisionTriage(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, s.supervisionTriageOn, supervisionTriageSettingKey+" is unchanged",
		func(l i18n.Lang, before, after string) string {
			return i18n.T(l, "assistant-settings_tool.change_supervision_triage", i18n.Args{"before": before, "after": after})
		}, func(v *Settings) **bool { return &v.SupervisionTriage }, supervisionTriageSettingKey+" is %v.")
}

// setAutoSupervisionMessages is the older on/off form: true is the mode all, false off. A value already in effect is a
// no-op, so true leaves outcomes as it is.
func (s *Service) setAutoSupervisionMessages(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, s.autoSupervisionOn, "assistant.auto_supervision_messages is unchanged", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_auto_supervision_messages", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool {
		v.AutoSupervisionMode = "" // the saved switch decides again (supervisionModeOf): true is all, false off
		return &v.AutoSupervisionMessages
	}, "assistant.auto_supervision_messages is %v now (settingsChanged applies the switch at once; turning it off withdraws what waited, turning it on replays nothing).")
}

// setAutoSupervisionMode changes which automatic supervision messages are sent (supervise_switch.go). The older switch
// is kept in step (mode != off) for older readers and downgrades.
func (s *Service) setAutoSupervisionMode(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var mode string
	if err := json.Unmarshal(v, &mode); err != nil || !slices.Contains(supervisionModes, mode) {
		return nil, errors.New("value must be all, outcomes or off")
	}
	before := s.supervisionMode(ctx)
	if mode == before {
		return &ownChange{summary: "assistant.auto_supervision_mode is unchanged"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	name := func(l i18n.Lang, m string) string {
		return i18n.T(l, "assistant-settings_tool.auto_supervision_mode_"+m)
	}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_auto_supervision_mode", i18n.Args{"before": name(l, before), "after": name(l, mode)})
	},
		apply: func(ctx context.Context) (string, error) {
			on := mode != supervisionModeOff
			if _, err := s.updateSettings(ctx, func(v *Settings) error {
				v.AutoSupervisionMode, v.AutoSupervisionMessages = mode, &on
				return nil
			}); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			return fmt.Sprintf("assistant.auto_supervision_mode is %s now (applied at once; what the new mode drops is withdrawn, nothing withdrawn is replayed).", mode), nil
		}}, nil
}

func (s *Service) setInboxTodoForward(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, s.inboxTodoForwardOn, "inbox.todo_forward.enabled is unchanged", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_inbox_todo_forward", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.InboxTodoForward },
		"inbox.todo_forward.enabled is %v from the next Inbox round on.")
}

func (s *Service) setPREventsRouteToAgent(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, s.prEventsRouteToAgentOn, "assistant.pr_events_route_to_agent is unchanged", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_pr_events_route_to_agent", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.PREventsRouteToAgent },
		"assistant.pr_events_route_to_agent is %v from the next PR status sweep on.")
}

func (s *Service) setMainSupervisesOwnTasks(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, s.mainSupervisesOwnTasks, "assistant.main_supervises_own_tasks is unchanged", func(l i18n.Lang, before, after string) string {
		return i18n.T(l, "assistant-settings_tool.change_main_supervises_own_tasks", i18n.Args{"before": before, "after": after})
	}, func(v *Settings) **bool { return &v.MainSupervisesOwnTasks },
		"assistant.main_supervises_own_tasks is %v for the signals raised from now on (open tasks keep running; what the "+
			"background supervisor was already sent it finishes).")
}

func (s *Service) setBackgroundSupervisorKeepsContext(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	return s.setBoolSetting(ctx, v, func(ctx context.Context) bool { return backgroundKeepsContext(s.settings(ctx)) },
		"assistant.background_supervisor_keeps_context is unchanged", func(l i18n.Lang, before, after string) string {
			return i18n.T(l, "assistant-settings_tool.change_background_supervisor_keeps_context", i18n.Args{"before": before, "after": after})
		}, func(v *Settings) **bool { return &v.BackgroundSupervisorKeepsContext },
		"assistant.background_supervisor_keeps_context is %v from the background supervisor's next round (one running now "+
			"finishes as it is).")
}

func (s *Service) setChatReplyRelay(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	ch, err := s.setBoolSetting(ctx, v, func(ctx context.Context) bool { return chatReplyRelayOn(s.settings(ctx)) },
		"assistant.chat_reply_relay is unchanged", func(l i18n.Lang, before, after string) string {
			return i18n.T(l, "assistant-settings_tool.change_chat_reply_relay", i18n.Args{"before": before, "after": after})
		}, func(v *Settings) **bool { return &v.ChatReplyRelay },
		"assistant.chat_reply_relay is %v from now on.")
	if err != nil || ch == nil || ch.apply == nil {
		return ch, err
	}
	apply := ch.apply
	ch.apply = func(ctx context.Context) (string, error) {
		out, err := apply(ctx)
		if err == nil && !chatReplyRelayOn(s.settings(ctx)) {
			s.dropQueuedChatReplies(ctx, "", time.Time{})
		}
		return out, err
	}
	return ch, nil
}

func (s *Service) setTodosV2AutoSeen(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var mode string
	if err := json.Unmarshal(v, &mode); err != nil {
		return nil, errors.New("todos_v2_auto_seen must be off, subtasks or all_autonomous")
	}
	mode = strings.TrimSpace(mode)
	if !slices.Contains(todosV2AutoSeenModes, mode) {
		return nil, errors.New("todos_v2_auto_seen must be off, subtasks or all_autonomous")
	}
	before := s.settings(ctx).todosV2AutoSeenMode()
	if mode == before {
		return &ownChange{summary: todosV2AutoSeenSettingKey + " is unchanged"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{
		cardText: func(l i18n.Lang) string {
			return fmt.Sprintf("Change %s: %s → %s", todosV2AutoSeenSettingKey, before, mode)
		},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.TodosV2AutoSeen = mode; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			s.todosChanged(ctx)
			return fmt.Sprintf("%s is %s (applies when todos_v2 is enabled).", todosV2AutoSeenSettingKey, mode), nil
		},
	}, nil
}

func (s *Service) setTodosV2PendingReviewSLAHours(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var hours *int
	if err := json.Unmarshal(v, &hours); err != nil || hours == nil || !slices.Contains(todosV2PendingReviewSLAOptions, *hours) {
		return nil, errors.New("todos_v2_pending_review_sla_hours must be 0, 12, 24 or 48")
	}
	next := *hours
	before := s.settings(ctx).todosV2PendingReviewSLAHours()
	if next == before {
		return &ownChange{summary: todosV2PendingReviewSLASettingKey + " is unchanged"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{
		cardText: func(l i18n.Lang) string {
			return fmt.Sprintf("Change %s: %d → %d", todosV2PendingReviewSLASettingKey, before, next)
		},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.TodosV2PendingReviewSLAHours = &next; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			s.todosChanged(ctx)
			return fmt.Sprintf("%s is %d (applies when todos_v2 is enabled).", todosV2PendingReviewSLASettingKey, next), nil
		},
	}, nil
}

func (s *Service) setTodosV2CanWaitSLAHours(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var hours *int
	if err := json.Unmarshal(v, &hours); err != nil || hours == nil || !slices.Contains(todosV2CanWaitSLAOptions, *hours) {
		return nil, errors.New("todos_v2_can_wait_sla_hours must be 0, 24, 48 or 72")
	}
	next := *hours
	before := s.settings(ctx).todosV2CanWaitSLAHours()
	if next == before {
		return &ownChange{summary: todosV2CanWaitSLASettingKey + " is unchanged"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{
		cardText: func(l i18n.Lang) string {
			return fmt.Sprintf("Change %s: %d → %d", todosV2CanWaitSLASettingKey, before, next)
		},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.TodosV2CanWaitSLAHours = &next; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			s.todosChanged(ctx)
			return fmt.Sprintf("%s is %d (applies when todos_v2 is enabled).", todosV2CanWaitSLASettingKey, next), nil
		},
	}, nil
}

func (s *Service) setTodosV2FoldChildren(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var onPtr *bool
	if err := json.Unmarshal(v, &onPtr); err != nil || onPtr == nil {
		return nil, errors.New("value must be true or false")
	}
	on := *onPtr
	before := s.settings(ctx).todosV2FoldChildrenOn()
	if on == before {
		return &ownChange{summary: todosV2FoldChildrenSettingKey + " is unchanged"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{
		cardText: func(l i18n.Lang) string {
			return fmt.Sprintf("Change %s: %v → %v", todosV2FoldChildrenSettingKey, before, on)
		},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.TodosV2FoldChildren = &on; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			s.todosChanged(ctx)
			return fmt.Sprintf("%s is %v (applies when todos_v2 is enabled).", todosV2FoldChildrenSettingKey, on), nil
		},
	}, nil
}

func (s *Service) setTodosV2QuietHours(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var rawPtr *string
	if err := json.Unmarshal(v, &rawPtr); err != nil || rawPtr == nil {
		return nil, errors.New("todos_v2_quiet_hours must be empty or HH:MM-HH:MM (24-hour range with distinct start and end)")
	}
	next := strings.TrimSpace(*rawPtr)
	if next != "" {
		if _, ok := parseTodosV2QuietHours(next); !ok {
			return nil, errors.New("todos_v2_quiet_hours must be empty or HH:MM-HH:MM (24-hour range with distinct start and end)")
		}
	}
	before := s.settings(ctx).todosV2QuietHours()
	if next == before {
		return &ownChange{summary: todosV2QuietHoursSettingKey + " is unchanged"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	display := func(val string) string {
		if val == "" {
			return "off"
		}
		return val
	}
	return &ownChange{
		cardText: func(l i18n.Lang) string {
			return fmt.Sprintf("Change %s: %s → %s", todosV2QuietHoursSettingKey, display(before), display(next))
		},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.TodosV2QuietHours = next; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			s.todosChanged(ctx)
			return fmt.Sprintf("%s is %s (applies when todos_v2 is enabled).", todosV2QuietHoursSettingKey, display(next)), nil
		},
	}, nil
}

func (s *Service) setTodosV2DigestWindowMinutes(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var mins *int
	if err := json.Unmarshal(v, &mins); err != nil || mins == nil || !slices.Contains(todosV2DigestWindowOptions, *mins) {
		return nil, errors.New("todos_v2_digest_window_minutes must be 0, 30, 60 or 120")
	}
	next := *mins
	before := s.settings(ctx).todosV2DigestWindowMinutes()
	if next == before {
		return &ownChange{summary: todosV2DigestWindowMinutesSettingKey + " is unchanged"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	return &ownChange{
		cardText: func(l i18n.Lang) string {
			return fmt.Sprintf("Change %s: %d → %d", todosV2DigestWindowMinutesSettingKey, before, next)
		},
		apply: func(ctx context.Context) (string, error) {
			if _, err := s.updateSettings(ctx, func(v *Settings) error { v.TodosV2DigestWindowMinutes = &next; return nil }); err != nil {
				return "", err
			}
			s.settingsChanged(ctx)
			s.todosChanged(ctx)
			return fmt.Sprintf("%s is %d (applies when todos_v2 is enabled).", todosV2DigestWindowMinutesSettingKey, next), nil
		},
	}, nil
}

// launchers are the launchers of the assistant's machine, as the apps list them.
func (s *Service) launchers(ctx context.Context) ([]proto.Launcher, error) {
	machine := s.settings(ctx).MachineID
	if _, ok := s.device(ctx, machine); !ok {
		return nil, errors.New("the assistant's machine is not configured")
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ls, err := s.assistantLaunchers(cctx, machine)
	if err != nil {
		return nil, err
	}
	out := make([]proto.Launcher, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.Launcher)
	}
	return out, nil
}

func (s *Service) getLauncher(ctx context.Context) (string, error) {
	now := s.launcherNow(ctx)
	ls, err := s.launchers(ctx)
	if err != nil {
		return "now: " + now + " (the machine's launchers could not be listed: " + err.Error() + ")", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "now: %s\navailable:\n", now)
	for _, l := range ls {
		fmt.Fprintf(&b, "- %s (model %s)\n", l.Name, orNone(l.Model))
	}
	return b.String(), nil
}

// setLauncher writes the launcher into Model configuration › Main chat, inheriting its own default model and effort.
func (s *Service) setLauncher(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var name string
	if err := json.Unmarshal(v, &name); err != nil || strings.TrimSpace(name) == "" {
		return nil, errors.New("value must be a launcher name")
	}
	name = strings.TrimSpace(name)
	if proto.IsExternalLauncher(name) {
		return nil, i18n.Err("external-adapter.purpose_unsupported")
	}
	before := s.launcherNow(ctx)
	ls, err := s.launchers(ctx)
	if err != nil {
		return nil, err
	}
	known := false
	for _, l := range ls {
		known = known || l.Name == name
	}
	if !known {
		return nil, fmt.Errorf("no launcher %q on the assistant's machine (get lists them): %w", name, i18n.Err("assistant-models.err_provider_unsupported"))
	}
	if name == before {
		return &ownChange{summary: "启动器没有变化"}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	providers := s.modelProviders(ctx)
	p, err := s.modelPurpose(ctx, "assistant", providers)
	if err != nil {
		return nil, err
	}
	provider, _ := json.Marshal("launcher." + name)
	binding, err := patchModel(p.Override, modelPatch{Provider: provider})
	if err != nil {
		return nil, err
	}
	base, _, err := s.modelBaseline(ctx, p)
	if err != nil {
		return nil, err
	}
	if err := s.validateModel(ctx, p, mergeModel(base, binding), providers); err != nil {
		return nil, err
	}
	rec := modelRecord{Revision: p.Revision, Binding: binding}
	return &ownChange{cardText: func(l i18n.Lang) string {
		return i18n.T(l, "assistant-settings_tool.change_launcher", i18n.Args{"before": before, "after": name})
	},
		apply: func(ctx context.Context) (string, error) {
			if err := s.saveModel(ctx, "assistant", rec); err != nil {
				return "", err
			}
			return "New Persona conversations start with " + name + ".", nil
		}}, nil
}
