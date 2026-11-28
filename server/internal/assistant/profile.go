package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/recordingwire"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/supervisionrole"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

// The assistant's own settings (settings `assistant`, docs/assistant/assistant.md "Boundary with hub"): the machine its
// conversations run on, its launcher, the user's time zone, "About me", the main conversation and the ones it replaced,
// and whether tasks from the main conversation are told only there. Only the assistant reads and writes them; hub.json's
// assistant section is their default (the hub's hello). It reports what the hub's own features need of them in every
// snapshot (Profile), and serves GET / PUT /api/v1/assistant, the warm-up and the memory routes.

// settingsKey is the setting the assistant keeps them in (the hub wrote the same JSON until 2026-11-04).
const settingsKey = "assistant"

// assistantRel is this manager's assistant directory on a machine, relative to its home (node assistant.prepare):
// assistant for the built-in manager, assistant-<id> for another (identity.AssistantDirName), in the state directory. It
// is spelled with the former state directory's name (compat.StateDir), which older nodes join to HOME as it is; a
// current node resolves either spelling to the state directory it runs on (node/assistant_guest.go assistantDirOf,
// #1681), so the spelling never decides where the directory is.
func (s *Service) assistantRel() string {
	return path.Join(compat.StateDir, identity.AssistantDirName(s.id))
}

// settingsFresh is how long the settings read from the database are used before they are read again (the assistant
// writes them itself; another hub sharing the database might too).
const settingsFresh = 5 * time.Second

// Settings are the assistant's settings, as saved.
type Settings struct {
	// WorkspaceRules are used only by explicit backend preparation.
	WorkspaceRules workspacenaming.Rules `json:"workspace_rules,omitempty"`
	MachineID      string                `json:"machine_id"`
	Launcher       string                `json:"launcher"`
	// DefaultPermissionMode applies to new non-guest conversations and stopped Claude Code resumes without an explicit mode.
	// Empty selects the built-in bypassPermissions default; other agents keep legacy resume precedence, and live/explicit modes are unchanged.
	DefaultPermissionMode string `json:"default_permission_mode,omitempty"`
	// Timezone is the phone's (IANA name): routines and the snapshot read times in it, and the hub its own.
	Timezone string `json:"timezone,omitempty"`
	// TimezonePicked: the user picked Timezone by hand (#1855); while true, a device's own sync (a `timezone` written
	// without `timezone_picked`) leaves it alone. false: it follows the devices again. nil: never said (every write
	// lands, as before).
	TimezonePicked *bool `json:"timezone_picked,omitempty"`
	// Notes, once edited in the app, replace manager.json's assistant.notes.
	Notes *string `json:"notes,omitempty"`
	// OwnerAliases are the names the user explicitly adds for kb.owner; an empty list clears them.
	OwnerAliases []string `json:"owner_aliases,omitempty"`
	// Main is the main conversation (the Persona tab opens it): one long-running conversation, the others are side ones.
	Main          string `json:"main,omitempty"`
	MainMachineID string `json:"main_machine_id,omitempty"` // confirmed routing device of Main
	// Former are the main conversations replaced since (newest first, POST /api/v1/assistant/main/new): their machine
	// keeps listing them, archived, however long they are quiet.
	Former []FormerMain `json:"former_main,omitempty"`
	// MainTasksQuiet: tasks the main conversation opens are told only there, not pushed, on the Live Activity or in
	// task badges (#1731). nil is on (the default); the view always says what is in effect.
	MainTasksQuiet *bool `json:"main_tasks_quiet,omitempty"`
	// ApprovalMirrorMain shows eligible non-main assistant permission cards in Main too. Nil is on;
	// changing it applies to new cards, and every existing copy still resolves with its authoritative card.
	ApprovalMirrorMain *bool `json:"approval_mirror_main,omitempty"`
	// ApprovalMirrorTools replaces the write-tool allowlist for new main-conversation and direct-bot
	// copies. Nil uses the default; an empty list disables both routes. Existing copies still finish.
	ApprovalMirrorTools       *[]string `json:"approval_mirror_tools,omitempty"`
	IdleAutoCompact           *bool     `json:"idle_auto_compact,omitempty"`
	IdleAutoCompactMinutes    *int      `json:"idle_auto_compact_minutes,omitempty"`
	IdleAutoCompactTokens     *int      `json:"idle_auto_compact_tokens,omitempty"`
	AgyIdleAutoCompact        *bool     `json:"agy_idle_auto_compact,omitempty"`
	AgyIdleAutoCompactMinutes *int      `json:"agy_idle_auto_compact_minutes,omitempty"`
	AgyIdleAutoCompactTokens  *int      `json:"agy_idle_auto_compact_tokens,omitempty"`
	CacheKeepalive            *bool     `json:"cache_keepalive,omitempty"`
	CacheKeepaliveMinutes     *int      `json:"cache_keepalive_minutes,omitempty"`
	CacheKeepaliveMax         *int      `json:"cache_keepalive_max,omitempty"`
	IdleReleaseMinutes        *int      `json:"idle_release_minutes,omitempty"` // #12008, idle_compact_settings.go
	// StartRequestedTasks: a task_plan task the user asked for in so many words starts without the card's "Start"
	// (#2559). nil is on (the default).
	StartRequestedTasks     *bool  `json:"start_requested_tasks,omitempty"`
	TaskWorkspacePreference string `json:"task_workspace_preference,omitempty"`
	// ReadonlyMode leaves runtime protection unchanged unless the owner opts into prompt-only task constraints.
	ReadonlyMode string `json:"readonly_mode,omitempty"`
	// RecordingDays is the old single retention of recordings (#3235): read as RecordingAudioDays while that is unset,
	// and a write of it sets RecordingAudioDays (#16412).
	RecordingDays *int `json:"recording_days,omitempty"`
	// RecordingAudioDays: how many days a recording's audio is kept (the phone deletes its copies, audio and text, at
	// it); 0 means forever, nil is RecordingDays or 14 (#16412).
	RecordingAudioDays *int `json:"recording_audio_days,omitempty"`
	// RecordingTextDays: how many days a recording's text (transcript, summary, live log, whoever wrote it) stays in the
	// knowledge base; nil or 0 means forever (#16412).
	RecordingTextDays *int `json:"recording_text_days,omitempty"`
	// MinutesDays: how many days a meeting's minutes stay once its transcript went with RecordingTextDays (#3780,
	// kb/recording_trim.go); nil or 0 means forever. Finite windows are never fewer than the text's (kb.MinutesDaysFor).
	MinutesDays *int `json:"minutes_days,omitempty"`
	// KBTopics: the knowledge base organizes knowledge points by topic (#3783, kb/topics.go). nil is on (the default).
	KBTopics *bool `json:"kb_topics,omitempty"`
	// Dashboards: conversations may draft pages the owner keeps in the sidebar (#15017, pages_ui.go). nil is off (the
	// default); only the owner changes it (a device with full access).
	Dashboards *bool `json:"dashboards,omitempty"`
	// MainBackgroundFolded: the apps fold the main conversation's background rounds (another conversation's message,
	// a scheduled task or follow-up) into one line that opens on request (#4314). nil is on (the default).
	MainBackgroundFolded *bool `json:"main_background_folded,omitempty"`
	// FoldRoutineRelayReplies opts the main chat into quiet completed relay reports when its background rounds fold.
	FoldRoutineRelayReplies *bool `json:"fold_routine_relay_replies,omitempty"`
	// MainCompactWindow: the compaction window, in tokens, the main conversation and each leader conversation are held to
	// when their launcher declares a larger one (#7314, #15318, main_compact_window.go); nil is 400000, 0 follows the
	// launcher.
	MainCompactWindow *int `json:"main_compact_window,omitempty"`
	// RecentTaskChats: the apps' sidebar Recent lists the chats a scheduled task or the background supervisor started
	// too, by last activity with the others (#8324; since #6228 they are only in All chats). Display only, one value
	// for every device. nil is off (the default).
	RecentTaskChats *bool `json:"recent_task_chats,omitempty"`
	// ConversationOrder is the user's manual order of ordinary conversation ids (#12017). Empty keeps activity-time
	// ordering. Clients append ids they do not currently hold so a limited read cannot silently discard the rest.
	ConversationOrder []string `json:"conversation_order,omitempty"`
	// ConversationTagFilters is the user's exact filter-entry order above the conversation list (#12018). Nil keeps
	// the automatic all-in-use-tag row; a non-nil empty list is a deliberately customized empty row.
	ConversationTagFilters []string `json:"conversation_tag_filters"`
	WorkGuide              *bool    `json:"work_guide,omitempty"`
	// TodosV2 enables the unified experimental New To-dos & Tasks experience (#14538, #14584). nil is off (the default).
	TodosV2 *bool `json:"todos_v2,omitempty"`
	// TodosV2AutoSeen controls whether autonomously completed to-dos auto-clear unseen badges under todos_v2=true (#16098):
	// "off", "subtasks" (part_of children only), or "all_autonomous" (default when empty).
	TodosV2AutoSeen string `json:"todos_v2_auto_seen,omitempty"`
	// TodosV2PendingReviewSLAHours controls how many hours pending_review stays quiet in working under todos_v2=true
	// before escalating to needs_you (#16098): 0 (off — immediate needs_you), 12, 24 (default when nil), or 48.
	TodosV2PendingReviewSLAHours *int `json:"todos_v2_pending_review_sla_hours,omitempty"`
	// TodosV2CanWaitSLAHours controls how many hours non-critical can_wait decisions wait under todos_v2=true
	// before aging into active needs_you (#16098): 0 (off — never auto-escalate), 24, 48 (default when nil), or 72.
	TodosV2CanWaitSLAHours *int `json:"todos_v2_can_wait_sla_hours,omitempty"`
	// TodosV2FoldChildren controls whether non-urgent child tasks fold under their top-level parent goal in board
	// projection under todos_v2=true (#16098). nil is true (the default).
	TodosV2FoldChildren *bool `json:"todos_v2_fold_children,omitempty"`
	// TodosV2QuietHours is an optional 24-hour local-time quiet window ("HH:MM-HH:MM") under todos_v2=true during
	// which routine non-card, non-P0, non-notify_user pushes downgrade to LevelP2 (#16433). Empty disables it.
	TodosV2QuietHours string `json:"todos_v2_quiet_hours,omitempty"`
	// TodosV2DigestWindowMinutes is an optional burst-coalescing window in minutes under todos_v2=true (#16433):
	// 0 (default, disabled), 30, 60, or 120.
	TodosV2DigestWindowMinutes *int `json:"todos_v2_digest_window_minutes,omitempty"`
	// IntegrationsV2 enables the unified experimental Integrations & Extensions experience (#14802, #14805). nil is off (the default).
	IntegrationsV2 *bool `json:"integrations_v2,omitempty"`
	// LabsSeen is the set of experiment IDs acknowledged on any client of this assistant.
	LabsSeen []string `json:"labs_seen,omitempty"`
	// MeetingAttendanceDefault: the role a meeting recording pre-selects on the phone (off, listener, participant;
	// docs/assistant/meeting-minutes.md "Attending a meeting"); nil is off.
	MeetingAttendanceDefault *string `json:"meeting_attendance_default,omitempty"`
	// Meeting transcription stays on the device until the user chooses the hub. A nil glossary is empty;
	// a nil or empty extension reference selects the built-in transcription contribution.
	MeetingTranscriptionDefault   *string   `json:"meeting_transcription_default,omitempty"`
	MeetingGlossary               *[]string `json:"meeting_glossary,omitempty"`
	MeetingTranscriptionExtension *string   `json:"meeting_transcription_extension,omitempty"`
	// MeetingTranscriptionInstall pins MeetingTranscriptionExtension to the installation the owner named
	// (transcribers.TranscriptionInstall): set with it on every PUT, never by the apps, never shown. Nil for the
	// built-in contribution (or a choice stored before pins were kept).
	MeetingTranscriptionInstall *string `json:"meeting_transcription_install,omitempty"`
	// DeliveryGate: three-tier closeout gate for PR/Issue tasks (off, merged, live; delivery.go). nil falls back to
	// DoneNeedsDelivery (true -> live, explicit false -> off) and otherwise defaults to merged.
	DeliveryGate *string `json:"delivery_gate,omitempty"`
	// DoneNeedsDelivery: legacy boolean toggle for live delivery gating (true -> live, false -> off; #3306,
	// delivery.go). Kept for backward compatibility alongside DeliveryGate.
	DoneNeedsDelivery *bool `json:"done_needs_delivery,omitempty"`
	// AgentTasks: every agent Persona starts is on a quiet, supervised todo (#3617, agent_tasks.go). nil is off (the
	// default).
	AgentTasks *bool `json:"agent_tasks,omitempty"`
	// TaskLessons: a supervised task accepted after friction teaches its project's next Agents what it learned
	// (#12295, task_lessons.go). nil is off (the default): no model call, no lesson written, no prompt changed.
	TaskLessons *bool `json:"task_lessons,omitempty"`
	// CloseEndedTerminals: when an Agent's session ends (its task is done or cancelled, it quit by itself or was told
	// to), its terminal tab is closed too, through the "Archive and close" path (#10355, agent_terminal_close.go).
	// nil is off (the default): the terminal stays, as since #4754.
	CloseEndedTerminals *bool `json:"close_ended_terminals,omitempty"`
	// AgentAutoNaming corrects recognizable placeholder names; nil is on. Manual titles are protected.
	AgentAutoNaming *bool `json:"agent_auto_naming,omitempty"`
	// AgentAutoNamingMax counts successful automatic title changes per session; nil is one.
	AgentAutoNamingMax *int `json:"agent_auto_naming_max,omitempty"`
	// AgentAutoNamingIntervalHours enables refresh after new output; nil or zero is off.
	AgentAutoNamingIntervalHours *int `json:"agent_auto_naming_interval_hours,omitempty"`
	// InboxSignals: new Inbox messages the judge links to a supervised task reach its supervisor as the signal inbox
	// (#4293, supervise_inbox.go). nil is off (the default): each one is a supervisor turn.
	InboxSignals *bool `json:"inbox_signals,omitempty"`
	// InboxTodoForward: deterministically forward PR/Issue Inbox items to the bound open to-do's Agent without LLM
	// calls (#12492, inbox_forward.go). nil is off (the default): zero automatic forwards until opted in.
	InboxTodoForward *bool `json:"inbox_todo_forward,omitempty"`
	// PREventsRouteToAgent: when true, new actionable PR events observed by pr_status on an open to-do (merge conflict,
	// current-head CI failure, or current-head changes_requested / actionable review) route directly to the bound Agent
	// (send, queue, or non-user resume), falling back to supervisor wakes when unavailable (#12607, pr_status_route.go).
	// nil is off (the default), preserving existing pr_status supervision behavior.
	PREventsRouteToAgent *bool `json:"pr_events_route_to_agent,omitempty"`
	// InboxPost: the assistant may put a conversation into the Inbox (inbox_post, a task's "put it in the Inbox when
	// done", inbox_post.go). nil is on: a row only comes from the user's own request or a task they set. The Inbox
	// Sources page's switch for it.
	InboxPost *bool `json:"inbox_post,omitempty"`
	// IdleCloseHours: an agent Persona started that has been idle this many hours with nothing going on is closed
	// (tasks_idle.go); 0 turns it off, nil is 12 (the default).
	IdleCloseHours *int `json:"idle_close_hours,omitempty"`
	// ArchiveClosedDays: a closed session idle this many days, on no to-do or only on done ones, is archived by itself
	// (agent_archive_auto.go, #3618); 0 turns it off, nil is 3 (the default).
	ArchiveClosedDays *int `json:"archive_closed_days,omitempty"`
	// ReviewSamplePercent: the share (0–100) of the finished supervised tasks that passed the judge's own check whose
	// review still goes to the main conversation (#3128, supervise_sample.go); the rest the judge closes. nil is 100:
	// every finished task waits for task_review, as before.
	ReviewSamplePercent *int `json:"review_sample_percent,omitempty"`
	// DispatchPolicy is the optional organization policy restricting which conversation classes may hand out work or
	// relay to the main or a leader conversation (dispatch_policy.go). nil or expired: every conversation behaves as
	// without it.
	DispatchPolicy *DispatchPolicy `json:"dispatch_policy,omitempty"`
	// StartDefaults is the optional policy of what task Agents start with beyond what their caller named, by the kind
	// of work (start_defaults.go). nil or expired: every task starts as without it.
	StartDefaults *StartDefaultsPolicy `json:"start_defaults,omitempty"`
	// Leaders are the side conversations the user made leaders (#3133, supervise_leader.go): a task one hands out with
	// task_plan is supervised by it instead of the main conversation. Empty (the default): only the main one supervises.
	Leaders []string `json:"leaders,omitempty"`
	// LimitFallbacks are the launchers, first choice first, that a conversation the owner relies on goes on with when a
	// subscription's usage limit stops it, until that limit resets (#14435, limit_fallback.go). Empty (the default):
	// it waits for the reset as before. The assistant writes it through persona_settings; PUT /api/v1/assistant does not.
	LimitFallbacks []string `json:"limit_fallbacks,omitempty"`
	// MachineFallbacks are the backup computers, first choice first, that the main conversation, direction leaders, and
	// unbound default routines fail over to when the primary computer stays offline past MachineFallbackMinutes
	// (#15119, machine_fallback.go). Empty (the default) disables automatic cross-machine failover.
	MachineFallbacks []string `json:"machine_fallbacks,omitempty"`
	// MachineFallbackMinutes is how many minutes a computer must stay offline before automatic failover moves
	// conversations or falls back default routines to MachineFallbacks (#15119). nil or 0 uses the 10-minute default.
	MachineFallbackMinutes *int `json:"machine_fallback_minutes,omitempty"`
	// BriefingWaits tunes only the deterministic wait rows in morning/evening facts: the briefing module's setting
	// (internal/modules/briefing, Waits), kept here as stored, also while the module is off. Nil uses the module's
	// defaults (the supervision thresholds); changes go through persona_settings' ordinary owner approval (modules.go),
	// except the administrator's exact reversible-setting switch for this key.
	BriefingWaits moduleValue `json:"briefing_waits,omitempty"`
	// InterjectBackground: a message the user sends while a Claude conversation is still answering moves what runs in
	// the foreground (Bash commands, subagents) to the background, so the message is taken up at once instead of after
	// them (Claude Code 2.1.139 or later, node/assistant_interject.go). nil is off (the default). A round the
	// user did not start is moved to the background by their message either way (owner_preempt.go).
	InterjectBackground *bool `json:"interject_background,omitempty"`
	// ClaudeInteractive: a Claude assistant conversation runs in interactive Claude Code instead of `claude -p`
	// (#10058, node/assistant_tui.go). nil is off (the default). Observed, not documented by Anthropic: a Claude
	// subscription's 5-hour window lasts longer in interactive use; driving the interactive program from Persona may also
	// be read as a different kind of use than it is, so it is the owner's opt-in.
	ClaudeInteractive *bool `json:"claude_interactive,omitempty"`
	// OwnerReplyApproval enables exact, one-shot approval of server-prepared connector batches. It never creates an
	// Always rule. PersistentOwnerActionCards independently keeps only reconstructable proposals pending indefinitely.
	OwnerReplyApproval         *bool `json:"owner_reply_approval,omitempty"`
	PersistentOwnerActionCards *bool `json:"persistent_owner_action_cards,omitempty"`
	// An explicitly empty phrase list disables reply approval. Do not omit it when encoding Settings: an omitted field
	// decodes as nil and would silently restore the default phrases after a write or restart.
	OwnerApprovalPhrases []string `json:"owner_reply_approval_phrases"`
	// LoadAllTools: every Persona tool of an assistant conversation is loaded up front, as before #7317. nil is off
	// (the default): only the frequently used ones are, the rest wait behind ToolSearch (tool_loading.go).
	LoadAllTools *bool `json:"load_all_tools,omitempty"`
	// NextSuggestions: chats that have no suggestion of their own (Persona's conversations, Codex and the other
	// runtimes) get the next message suggested by the suggest.next model after a turn (#4644, suggest_next.go). nil is
	// off (the default).
	NextSuggestions *bool `json:"next_suggestions,omitempty"`
	// MainSupervisesOwnTasks: the main conversation follows up the tasks no leader supervises itself. nil is off (the
	// default, #4927): the background supervisor does (supervise_background.go).
	MainSupervisesOwnTasks *bool `json:"main_supervises_own_tasks,omitempty"`
	// AutoSupervisionMessages is the older on/off form of AutoSupervisionMode, read only while the mode is unset: nil
	// or true is all, false is off. A write of the mode keeps it in step (mode != off) for older readers and downgrades.
	AutoSupervisionMessages *bool `json:"auto_supervision_messages,omitempty"`
	// AutoSupervisionMode: which automatic supervision messages (the follow-up messages that wake the main, leader or
	// background supervisor conversation) the assistant sends (supervise_switch.go). all (the default) sends every one;
	// outcomes only a task's terminal handoff, stop, failure, explicit done/blocked/question report and decisions, not
	// the routine checks made while it runs; off none, and what still waits is withdrawn. "" falls back to
	// AutoSupervisionMessages. The task ledger, the owner's own questions and acceptance, permission cards and approval
	// answers do not depend on it.
	AutoSupervisionMode string `json:"auto_supervision_mode,omitempty"`
	// SupervisionTriage is opt-in: eligible routine signals use the Sorting model before waking a supervisor.
	SupervisionTriage *bool `json:"supervision_triage,omitempty"`
	// SupervisionBatchMinutes independently batches routine supervision per final recipient, anchored at its first
	// routine signal. Nil or 0 preserves the legacy shared gather and spacing. Immediate signals bypass the batch.
	SupervisionBatchMinutes *int `json:"supervision_batch_minutes,omitempty"`
	// BackgroundSupervisorKeepsContext: the background supervisor is one long conversation again from its next round,
	// keeping each later round in context, as before #7315 (rounds already run from the first turn stay on their own
	// branches). nil is off (the default): each round starts from its first turn with only that round's facts and tools
	// (supervise_round.go).
	BackgroundSupervisorKeepsContext *bool `json:"background_supervisor_keeps_context,omitempty"`
	// ChatReplyRelay: after a conversation wrote into a Lark chat as the user, other people's new messages there are
	// told to it (chat_reply.go). nil is off (the default); on while true, and turning it off drops those still queued.
	ChatReplyRelay *bool `json:"chat_reply_relay,omitempty"`
	// ChatReplyHinted: the one-time hint that the feature exists was given to a conversation (chat_reply.go). The
	// assistant writes it itself; PUT /api/v1/assistant does not.
	ChatReplyHinted bool `json:"chat_reply_hinted,omitempty"`
	// ChatReplyMutedChats are Lark chats (by feed ID) where chat_reply relays are stopped even while
	// assistant.chat_reply_relay is on (chat_reply.go). Written through persona_settings; PUT /api/v1/assistant does not.
	ChatReplyMutedChats []chatReplyMutedChat `json:"chat_reply_muted_chats,omitempty"`
	// BackgroundSupervisor is the background supervisor's conversation, on BackgroundSupervisorMachineID, started at
	// BackgroundSupervisorAt: made on first use, "" until then. BackgroundSupervisorAnnounced: the main conversation
	// was told once that it follows up its tasks. The assistant writes these itself; PUT /api/v1/assistant does not.
	BackgroundSupervisor          string     `json:"background_supervisor,omitempty"`
	BackgroundSupervisorMachineID string     `json:"background_supervisor_machine_id,omitempty"`
	BackgroundSupervisorAt        *time.Time `json:"background_supervisor_at,omitempty"`
	BackgroundSupervisorAnnounced bool       `json:"background_supervisor_announced,omitempty"`
}

// chatReplyMutedChat is one Lark chat excluded from chat_reply relays (chat_reply.go,
// persona_settings assistant.chat_reply_relay_muted_chats).
type chatReplyMutedChat struct {
	Chat  string `json:"chat"`
	Title string `json:"title,omitempty"`
}

// FormerMain is a main conversation that was replaced, and until when it was the main one.
type FormerMain struct {
	MachineID string    `json:"machine_id"`
	SessionID string    `json:"session_id"`
	Until     time.Time `json:"until"`
}

// settingsBook keeps the settings read last (with no database: the only copy).
type settingsBook struct {
	change sync.Mutex // one change at a time
	mu     sync.Mutex
	cached *Settings
	at     time.Time
	gen    uint64 // counts the changes saved: a read begun before one does not replace what it saved
}

// savedSettings are the settings as saved (zero when never written); false when they could not be read.
func (s *Service) savedSettings(ctx context.Context) (Settings, bool) {
	b := &s.prefs
	st := s.store()
	b.mu.Lock()
	if b.cached != nil && (st == nil || time.Since(b.at) < settingsFresh) {
		v := *b.cached
		b.mu.Unlock()
		return v, true
	}
	gen := b.gen
	b.mu.Unlock()
	var v Settings
	if st != nil {
		if _, err := st.Setting(ctx, settingsKey, &v); err != nil {
			s.logf("assistant: settings: %v", err)
			return Settings{}, false
		}
	}
	b.mu.Lock()
	if b.gen == gen { // no change was saved meanwhile (else what this read is older than what is cached)
		b.cached, b.at = &v, time.Now()
	} else if b.cached != nil {
		v = *b.cached
	}
	b.mu.Unlock()
	return v, true
}

// updateSettings changes the saved settings with fn (which may refuse) and saves them.
func (s *Service) updateSettings(ctx context.Context, fn func(*Settings) error) (Settings, error) {
	return s.updateSettingsSaveChecked(ctx, fn, nil, nil)
}

// updateSettingsChecked also checks the freshly read saved state while holding the settings write lock. The automatic
// reversible-setting path uses it to reject a concurrent edit instead of rebasing its recorded before-image.
func (s *Service) updateSettingsChecked(ctx context.Context, fn func(*Settings) error, check func(Settings) error) (Settings, error) {
	return s.updateSettingsSaveChecked(ctx, fn, nil, check)
}

// updateSettingsSave is updateSettings with an optional atomic writer for state that must change with the settings.
// The ordinary path still writes only the assistant setting; a custom writer receives the store and complete value.
func (s *Service) updateSettingsSave(ctx context.Context, fn func(*Settings) error, save func(context.Context, Store, Settings) error) (Settings, error) {
	return s.updateSettingsSaveChecked(ctx, fn, save, nil)
}

// updateSettingsSaveChecked composes the optional freshly-read-state check and atomic writer under one settings lock.
func (s *Service) updateSettingsSaveChecked(ctx context.Context, fn func(*Settings) error, save func(context.Context, Store, Settings) error, check func(Settings) error) (Settings, error) {
	b := &s.prefs
	b.change.Lock()
	defer b.change.Unlock()
	b.mu.Lock()
	b.at = time.Time{} // read them again
	b.mu.Unlock()
	v, ok := s.savedSettings(ctx)
	if !ok {
		return Settings{}, errors.New("the assistant's settings could not be read")
	}
	if check != nil {
		if err := check(v); err != nil {
			return Settings{}, err
		}
	}
	if err := fn(&v); err != nil {
		return Settings{}, err
	}
	if proto.IsExternalLauncher(v.Launcher) {
		return Settings{}, i18n.Err("external-adapter.purpose_unsupported")
	}
	st := s.store()
	if st == nil && save != nil {
		return Settings{}, errors.New("no database on this hub")
	}
	if st != nil {
		var err error
		if save != nil {
			err = save(ctx, st, v)
		} else {
			err = st.PutSetting(ctx, settingsKey, v)
		}
		if err != nil {
			return Settings{}, err
		}
	}
	b.mu.Lock()
	b.cached, b.at = &v, time.Now()
	b.gen++
	b.mu.Unlock()
	return v, nil
}

const (
	todosV2AutoSeenOff           = "off"
	todosV2AutoSeenSubtasks      = "subtasks"
	todosV2AutoSeenAllAutonomous = "all_autonomous"

	todosV2DefaultPendingReviewSLAHours = 24
	todosV2DefaultCanWaitSLAHours       = 48
	todosV2DefaultDigestWindowMinutes   = 0
)

var (
	todosV2AutoSeenModes           = []string{todosV2AutoSeenOff, todosV2AutoSeenSubtasks, todosV2AutoSeenAllAutonomous}
	todosV2PendingReviewSLAOptions = []int{0, 12, 24, 48}
	todosV2CanWaitSLAOptions       = []int{0, 24, 48, 72}
	todosV2DigestWindowOptions     = []int{0, 30, 60, 120}
)

func (v Settings) workGuideOn() bool      { return v.WorkGuide != nil && *v.WorkGuide }
func (v Settings) todosV2On() bool        { return v.TodosV2 != nil && *v.TodosV2 }
func (v Settings) integrationsV2On() bool { return v.IntegrationsV2 != nil && *v.IntegrationsV2 }

func (v Settings) todosV2AutoSeenMode() string {
	if slices.Contains(todosV2AutoSeenModes, v.TodosV2AutoSeen) {
		return v.TodosV2AutoSeen
	}
	return todosV2AutoSeenAllAutonomous
}

func (v Settings) todosV2PendingReviewSLAHours() int {
	if v.TodosV2PendingReviewSLAHours != nil && slices.Contains(todosV2PendingReviewSLAOptions, *v.TodosV2PendingReviewSLAHours) {
		return *v.TodosV2PendingReviewSLAHours
	}
	return todosV2DefaultPendingReviewSLAHours
}

func (v Settings) todosV2CanWaitSLAHours() int {
	if v.TodosV2CanWaitSLAHours != nil && slices.Contains(todosV2CanWaitSLAOptions, *v.TodosV2CanWaitSLAHours) {
		return *v.TodosV2CanWaitSLAHours
	}
	return todosV2DefaultCanWaitSLAHours
}

func (v Settings) todosV2FoldChildrenOn() bool {
	return v.TodosV2FoldChildren == nil || *v.TodosV2FoldChildren
}

func (v Settings) todosV2QuietHours() string {
	raw := strings.TrimSpace(v.TodosV2QuietHours)
	if raw == "" {
		return ""
	}
	if _, ok := parseTodosV2QuietHours(raw); !ok {
		return ""
	}
	return raw
}

func (v Settings) todosV2DigestWindowMinutes() int {
	if v.TodosV2DigestWindowMinutes != nil && slices.Contains(todosV2DigestWindowOptions, *v.TodosV2DigestWindowMinutes) {
		return *v.TodosV2DigestWindowMinutes
	}
	return todosV2DefaultDigestWindowMinutes
}

// integrationsV2On reports whether assistant.integrations_v2 is enabled (off by default).
// Uses settingsLastGood so hot receive/routing paths never trigger a hub hello RPC.
func (s *Service) integrationsV2On(ctx context.Context) bool {
	v, _ := s.settingsLastGood(ctx)
	return v.integrationsV2On()
}

// prEventsRouteToAgentOn reports whether assistant.pr_events_route_to_agent is enabled (off by default).
func (s *Service) prEventsRouteToAgentOn(ctx context.Context) bool {
	v := s.settings(ctx).PREventsRouteToAgent
	return v != nil && *v
}

// settings are the settings in effect, what the rest of the package reads (the main conversation, the machine, the time
// zone; isMain and mainTasksQuiet for short): the machine one the hub has (else the hub's default), the launcher the
// assistant's own (else hub.json's, else claude; Model configuration › Main chat goes over it: launcherNow). A read that fails once
// they were read serves those (settingsLastGood): the zero value would say there is no main conversation, which
// would let it be archived and title it by its first message for that moment.
func (s *Service) settings(ctx context.Context) Settings {
	v, _ := s.settingsLastGood(ctx)
	return s.effective(ctx, v)
}

func (s *Service) effective(ctx context.Context, v Settings) Settings {
	def := s.hello(ctx).Assistant
	if v.MachineID == "" || !slices.Contains(def.Machines, v.MachineID) {
		v.MachineID = def.MachineID
	}
	if s.isWorkMachine(v.MachineID) {
		v.MachineID = def.MachineID
		if s.isWorkMachine(v.MachineID) {
			v.MachineID = ""
		}
	}
	if v.Launcher == "" {
		v.Launcher = def.Launcher
	}
	if v.Launcher == "" {
		v.Launcher = "claude"
	}
	if def.SupervisionBatchMinutes != nil {
		v.SupervisionBatchMinutes = def.SupervisionBatchMinutes
	}
	return v
}

// notes is what the assistant knows about the user: edited in the app, else manager.json's assistant.notes.
func (s *Service) notes(ctx context.Context) string {
	if n := s.settings(ctx).Notes; n != nil {
		return strings.TrimSpace(*n)
	}
	return strings.TrimSpace(s.hello(ctx).OwnerNotes)
}

func quietOn(v *bool) bool { return v == nil || *v }

// mainTasksQuiet: a task opened from the main conversation now is quiet (main_tasks_quiet, on by default).
func (s *Service) mainTasksQuiet(ctx context.Context) bool {
	return quietOn(s.settings(ctx).MainTasksQuiet)
}

// interjectBackground: a message sent while a conversation answers moves its foreground work to the background
// (interject_background, off by default). The owner's message to a round they did not start does so whatever this says
// (owner_preempt.go).
// claudeInteractive: Claude assistant conversations run in interactive Claude Code (claude_interactive, off by default).
func (s *Service) claudeInteractive(ctx context.Context) bool {
	v := s.settings(ctx).ClaudeInteractive
	return v != nil && *v
}

var defaultOwnerApprovalPhrases = []string{"发", "同意"} // i18n-ignore: exact affirmative owner input, not displayed text

func ownerReplyApprovalOn(v Settings) bool {
	return v.OwnerReplyApproval != nil && *v.OwnerReplyApproval
}

func persistentOwnerActionCardsOn(v Settings) bool {
	return v.PersistentOwnerActionCards != nil && *v.PersistentOwnerActionCards
}

// ownerApprovalPhrases returns the exact affirmative replies in effect. Nil
// means the documented defaults; an explicitly empty list disables chat
// approval without touching ordinary Allow/Deny cards or existing grants.
func ownerApprovalPhrases(v Settings) ([]string, error) {
	phrases := v.OwnerApprovalPhrases
	if phrases == nil {
		phrases = defaultOwnerApprovalPhrases
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(phrases))
	if len(phrases) > 8 {
		return nil, errors.New("owner_reply_approval_phrases accepts at most 8 phrases")
	}
	for _, phrase := range phrases {
		if phrase == "" || strings.TrimSpace(phrase) != phrase || len([]rune(phrase)) > 32 || seen[phrase] {
			return nil, errors.New("owner_reply_approval_phrases must contain unique, trimmed phrases of 1–32 characters")
		}
		seen[phrase] = true
		out = append(out, phrase)
	}
	return out, nil
}

func ownerApprovalPhrase(v Settings, text string) bool {
	if !ownerReplyApprovalOn(v) {
		return false
	}
	phrases, err := ownerApprovalPhrases(v)
	if err != nil {
		return false // invalid persisted configuration fails closed
	}
	text = ownerApprovalFoldLatin(strings.TrimSpace(text))
	for _, phrase := range phrases {
		if text == ownerApprovalFoldLatin(phrase) {
			return true
		}
	}
	return false
}

// ownerApprovalFoldLatin folds ASCII Latin capitals only. Unicode simple
// folding would make distinct non-Latin code points (for example long s and s)
// authorize one another, which is broader than an exact reply.
func ownerApprovalFoldLatin(text string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, text)
}

func ownerApprovalPhrasesForView(v Settings) []string {
	phrases, err := ownerApprovalPhrases(v)
	if err != nil {
		return v.OwnerApprovalPhrases
	}
	return phrases
}

func (s *Service) interjectBackground(ctx context.Context) bool {
	v := s.settings(ctx).InterjectBackground
	return v != nil && *v
}

// startRequestedTasks: task_plan starts the tasks the user asked for without waiting for "Start" (start_requested_tasks,
// on by default).
func (s *Service) startRequestedTasks(ctx context.Context) bool {
	return quietOn(s.settings(ctx).StartRequestedTasks)
}

// Bounds of recording_audio_days and recording_text_days (0: forever) and of the old recording_days (no forever).
const (
	minRecordingDays = 0
	maxRecordingDays = 3650
)

// recordingDaysValid: d is a saved retention within bounds (the old recording_days had no forever).
func recordingDaysValid(d *int, legacy bool) bool {
	return d != nil && *d >= minRecordingDays && *d <= maxRecordingDays && (!legacy || *d > 0)
}

// recordingAudioDays is how many days a recording's audio is kept (recording_audio_days, else the old
// recording_days, else 14); 0 means forever.
func (s *Service) recordingAudioDays(ctx context.Context) int {
	return recordingAudioDaysOf(s.settings(ctx))
}

func recordingAudioDaysOf(v Settings) int {
	if recordingDaysValid(v.RecordingAudioDays, false) {
		return *v.RecordingAudioDays
	}
	if recordingDaysValid(v.RecordingDays, true) {
		return *v.RecordingDays
	}
	return kb.DefaultRecordingDays
}

// recordingTextDays is how many days a recording's text stays in the knowledge base (recording_text_days); 0, the
// default, means forever.
func (s *Service) recordingTextDays(ctx context.Context) int {
	return recordingTextDaysOf(s.settings(ctx))
}

func recordingTextDaysOf(v Settings) int {
	if recordingDaysValid(v.RecordingTextDays, false) {
		return *v.RecordingTextDays
	}
	return kb.DefaultRecordingTextDays
}

// Bounds of minutes_days.
const (
	minMinutesDays = 0
	maxMinutesDays = 3650
)

// minutesDaysSetting is minutes_days as saved, 0 when unset or out of bounds (kb.MinutesDaysFor: the default).
func minutesDaysSetting(v Settings) int {
	if d := v.MinutesDays; d != nil && *d >= minMinutesDays && *d <= maxMinutesDays {
		return *d
	}
	return 0
}

// minutesDays is the effective minutes_days: 0 means forever (the default, and always while the text is kept
// forever); otherwise never fewer than recording_text_days.
func (s *Service) minutesDays(ctx context.Context) int {
	v := s.settings(ctx)
	return kb.MinutesDaysFor(minutesDaysSetting(v), recordingTextDaysOf(v))
}

// meetingRoles are the values of meeting_attendance_default, off (the default) first.
var meetingRoles = []string{recordingwire.AttendanceOff, recordingwire.AttendanceListener, recordingwire.AttendanceParticipant}

// meetingAttendanceDefault is the role a meeting recording pre-selects (meeting_attendance_default, off by default).
func (s *Service) meetingAttendanceDefault(ctx context.Context) string {
	return meetingAttendanceOf(s.settings(ctx))
}

// meetingAttendanceOf is meetingAttendanceDefault of settings already read (the view reads them once).
func meetingAttendanceOf(v Settings) string {
	if r := v.MeetingAttendanceDefault; r != nil && slices.Contains(meetingRoles, *r) {
		return *r
	}
	return recordingwire.AttendanceOff
}

// settingsLastGood is savedSettings, except that a transient read failure serves the settings read last when there
// are any. Callers whose default would be destructive use it: the recording retention falling back to 14 days for one
// locked-database round would make the next KB prune delete recordings the user kept for longer (#4008). Callers for
// which a stale value is risky (kbZone: moving the archive into a possibly old time zone) keep using savedSettings.
func (s *Service) settingsLastGood(ctx context.Context) (Settings, bool) {
	if v, ok := s.savedSettings(ctx); ok {
		return v, true
	}
	s.prefs.mu.Lock()
	cached := s.prefs.cached // a save during the failed read may already have put newer settings here
	var v Settings
	if cached != nil {
		v = *cached
	}
	s.prefs.mu.Unlock()
	return v, cached != nil
}

// recordingTextDaysNow is recordingTextDays for the knowledge base's engine, which has no context of its own. A
// transient settings read failure keeps the value read last (settingsLastGood); settings never successfully read keep
// the text forever, so one locked-database round can never shorten the retention and prune recordings early.
func (s *Service) recordingTextDaysNow() int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, ok := s.settingsLastGood(ctx)
	if !ok {
		return 0
	}
	return recordingTextDaysOf(v)
}

// recordingAudioDaysNow is recordingAudioDays for the knowledge base's engine, read like recordingTextDaysNow; only
// settings never successfully read fall back to the default.
func (s *Service) recordingAudioDaysNow() int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, ok := s.settingsLastGood(ctx)
	if !ok {
		return kb.DefaultRecordingDays
	}
	return recordingAudioDaysOf(v)
}

// minutesDaysNow is minutes_days as saved for the knowledge base's engine (0: forever by default), read like
// recordingTextDaysNow: a transient failure keeps the value read last, so it never deletes minutes the user kept for
// longer. The engine raises only positive values to recording_text_days (kb.Engine.MinutesDays).
func (s *Service) minutesDaysNow() int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, ok := s.settingsLastGood(ctx)
	if !ok {
		return 0
	}
	return minutesDaysSetting(v)
}

// kbTopics: the knowledge base organizes knowledge points by topic (kb_topics, on by default).
func (s *Service) kbTopics(ctx context.Context) bool { return quietOn(s.settings(ctx).KBTopics) }

// mainBackgroundFolded: the apps fold the main conversation's background rounds (main_background_folded, on by default).
func (s *Service) mainBackgroundFolded(ctx context.Context) bool {
	return quietOn(s.settings(ctx).MainBackgroundFolded)
}

func (v Settings) foldRoutineRelayReplies() bool {
	return v.FoldRoutineRelayReplies != nil && *v.FoldRoutineRelayReplies
}

// recentTaskChats: the apps' sidebar Recent lists the scheduled tasks' chats too (recent_task_chats, off by default).
func (s *Service) recentTaskChats(ctx context.Context) bool {
	v := s.settings(ctx).RecentTaskChats
	return v != nil && *v
}

// kbTopicsNow is kbTopics for the knowledge base loop, from the last settings read well: a database that does not
// answer neither starts nor stops the curation.
func (s *Service) kbTopicsNow() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, ok := s.settingsLastGood(ctx)
	return !ok || quietOn(v.KBTopics)
}

// launcherNow is the launcher new conversations start with: Model configuration › Main chat's, else the assistant's own.
func (s *Service) launcherNow(ctx context.Context) string {
	if rec, err := s.modelRecord(ctx, "assistant"); err == nil && rec.Binding.Provider != "" {
		return strings.TrimPrefix(rec.Binding.Provider, "launcher.")
	}
	return s.settings(ctx).Launcher
}

// mainLauncherNow resolves the main-only binding without changing new-chat defaults.
// An unreadable main override must not become permission to probe another launcher.
func (s *Service) mainLauncherNow(ctx context.Context) string {
	binding, err := s.resolveModel(ctx, purposeMain)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(binding.Provider, "launcher.")
}

// profile is what the hub's features need of the settings (Snapshot.Profile); tasks are the scheduled tasks and todos
// the open todos' agents as reported (either nil: unreadable, and Keep is left out, so the nodes keep what they list;
// Keep is left out too when what places the directions' sessions cannot be read, directionKeep). nil when the
// settings could not be read.
func (s *Service) profile(ctx context.Context, tasks []TaskView, todos *TodoReport) *Profile {
	saved, ok := s.savedSettings(ctx)
	if !ok {
		return nil
	}
	v := s.effective(ctx, saved)
	p := &Profile{MachineID: v.MachineID, Main: v.Main, MainMachineID: v.MainMachineID, Launcher: v.Launcher, Timezone: v.Timezone,
		ExtensionOwners: s.extensionOwners(ctx), Attention: s.attentionProfile(ctx)}
	if tasks != nil && todos != nil {
		if placed, err := s.directionKeep(ctx, v); err != nil {
			s.logf("assistant: keep: %v", err)
		} else {
			p.Keep = keptSessions(v, tasks, todos.Links, s.leadSessions(ctx, v), s.spaceKeep(ctx, v), placed)
		}
	}
	return p
}

// keptSessions are the conversations their machines list however long they have been quiet: the main conversation,
// the main ones it replaced, scheduled tasks' own, the agents of open todos, the current leaders (leads) and those listed
// for their spaces to be told (convs, spaceKeep: the task leaders' agents of open tasks, and a batch at a time of the
// conversations to-dos name that were never noted). Otherwise a busy machine's 80 newest transcripts crowd them out of
// the list and the Persona tab has no main chat (2026-11-01: the main one, quiet for two days, was gone), a weekly
// task's conversation drops out in between, with the app's link to it and the check that its last run still goes, an
// open task whose agent finished and exited shows "Starting Agent" for ever: its todo names a session the list no
// longer has (#4510), a direction the owner leaves alone for a week loses its Pinned row and its tasks' supervision
// messages go to the main chat (#11392), and the space of a to-do's conversation quiet since before the manager noted
// spaces could never be told (conversation_space_backfill.go, #10941). Last come the sessions placed under the current
// directions (placed, directionKeep), or a quiet one reads not_listed in its direction and can no longer be moved
// (#11515). Keeping a row listed does not keep its transcript file.
func keptSessions(v Settings, tasks []TaskView, links []TodoLink, leads, convs, placed []SessionRef) []SessionRef {
	out := []SessionRef{}
	add := func(machine, id string) {
		if machine != "" && id != "" && !slices.Contains(out, SessionRef{MachineID: machine, SessionID: id}) {
			out = append(out, SessionRef{MachineID: machine, SessionID: id})
		}
	}
	add(v.mainRouteMachine(), v.Main)
	for _, f := range v.Former { // archived, still to be found
		add(f.MachineID, f.SessionID)
	}
	for _, t := range tasks {
		add(t.RunMachineID, t.RunSessionID)
	}
	for _, l := range links {
		add(l.MachineID, l.SessionID)
	}
	// The background supervisor: quiet between tasks, still the one their signals go to (supervise_background.go).
	add(v.BackgroundSupervisorMachineID, v.BackgroundSupervisor)
	for _, l := range leads {
		add(l.MachineID, l.SessionID)
	}
	for _, c := range convs {
		add(c.MachineID, c.SessionID)
	}
	for _, p := range placed {
		add(p.MachineID, p.SessionID)
	}
	return out
}

// leadSessions are the current leaders' conversations (assistant.leaders without the main one), each on the computer
// its direction record names, else the assistant's (a leader not recorded yet, or the records unreadable). An ended
// direction is no longer in assistant.leaders, so its conversation leaves the set.
func (s *Service) leadSessions(ctx context.Context, v Settings) []SessionRef {
	if len(v.Leaders) == 0 {
		return nil
	}
	b := &s.directions
	b.mu.Lock()
	defer b.mu.Unlock()
	loaded := s.directionsLocked(ctx)
	out := make([]SessionRef, 0, len(v.Leaders))
	for _, id := range v.Leaders {
		if id == "" || id == v.Main {
			continue
		}
		if d := b.m[id]; loaded && d != nil && d.Outside != nil {
			if d.Outside.MachineID != "" && d.Outside.SessionID != "" {
				out = append(out, SessionRef{MachineID: d.Outside.MachineID, SessionID: d.Outside.SessionID})
			}
			continue // never keep a synthetic direction id as though it were a conversation
		}
		machine := v.MachineID
		if d := b.m[id]; loaded && d != nil && d.MachineID != "" {
			machine = d.MachineID
		}
		out = append(out, SessionRef{MachineID: machine, SessionID: id})
	}
	return out
}

// settingsChanged: the hub learns them (the snapshot's profile) and the next message reads the time in the new zone.
func (s *Service) settingsChanged(ctx context.Context) {
	s.syncAutoSupervision(ctx)          // the automatic-supervision switch takes effect now (supervise_switch.go)
	s.transcriptionSettingsChanged(ctx) // only another contribution chosen drops the readiness answer
	s.ProfileChanged(ctx)
	s.syncAgyIdleCompactionToNodes(ctx)
	s.revalidateFailedStartDeferrals(ctx)
	s.publish(ctx)
}

// ---- GET / PUT /api/v1/assistant, the warm-up, the memory ----

// assistantCwd is this manager's assistant directory on d, or "" while d has not reported its home.
func (s *Service) assistantCwd(d Device) string {
	return stateDirOn(d.Home, d.DataDir, identity.AssistantDirName(s.id))
}

// stateDirOn is the directory name in a machine's state directory: its data directory when it reported one (a custom
// PERSONA_HOME), else ~/.persona; "" while it has not reported its home.
func stateDirOn(home, dataDir, name string) string {
	if home == "" {
		return ""
	}
	if dataDir != "" {
		return path.Join(dataDir, name)
	}
	return path.Join(home, ".persona", name)
}

type assistantView struct {
	WorkspaceNamingVersion  string                 `json:"workspace_naming_version"`
	EffectiveWorkspaceRules workspacenaming.Rules  `json:"effective_workspace_rules"`
	SystemDefaults          *workspacenaming.Rules `json:"system_defaults,omitempty"`
	Settings
	Manager                   string             `json:"manager"`
	PermissionDefaults        permissionDefaults `json:"permission_defaults"`
	Cwd                       string             `json:"cwd"`
	Conversations             []proto.Session    `json:"conversations"`
	NotesEffective            string             `json:"notes_effective"`  // what the assistant is told about the user
	MainTasksQuiet            bool               `json:"main_tasks_quiet"` // in effect (the setting's own is left out while unset)
	ApprovalMirrorMain        bool               `json:"approval_mirror_main"`
	ApprovalMirrorTools       []string           `json:"approval_mirror_tools"`
	IdleAutoCompact           bool               `json:"idle_auto_compact"`
	IdleAutoCompactMinutes    int                `json:"idle_auto_compact_minutes"`
	IdleAutoCompactTokens     int                `json:"idle_auto_compact_tokens"`
	AgyIdleAutoCompact        bool               `json:"agy_idle_auto_compact"`
	AgyIdleAutoCompactMinutes int                `json:"agy_idle_auto_compact_minutes"`
	AgyIdleAutoCompactTokens  int                `json:"agy_idle_auto_compact_tokens"`
	CacheKeepalive            bool               `json:"cache_keepalive"`
	CacheKeepaliveMinutes     int                `json:"cache_keepalive_minutes"`
	CacheKeepaliveMax         int                `json:"cache_keepalive_max"`
	IdleReleaseMinutes        int                `json:"idle_release_minutes"`
	// StartRequestedTasks is in effect, like MainTasksQuiet.
	StartRequestedTasks     bool   `json:"start_requested_tasks"`
	TaskWorkspacePreference string `json:"task_workspace_preference"`
	ReadonlyMode            string `json:"readonly_mode"`
	// RecordingDays, DeliveryGate and DoneNeedsDelivery are in effect, like MainTasksQuiet. RecordingDays is the old
	// name of RecordingAudioDays, for clients from before #16412 (0: forever).
	RecordingDays      int    `json:"recording_days"`
	RecordingAudioDays int    `json:"recording_audio_days"`
	RecordingTextDays  int    `json:"recording_text_days"`
	DeliveryGate       string `json:"delivery_gate"`
	DoneNeedsDelivery  bool   `json:"done_needs_delivery"`
	// MinutesDays is in effect: 0 means forever (the default); otherwise never fewer than RecordingTextDays.
	MinutesDays int `json:"minutes_days"`
	// KBTopics is in effect, like MainTasksQuiet.
	KBTopics bool `json:"kb_topics"`
	// Dashboards is in effect (off until the owner turns it on).
	Dashboards bool `json:"dashboards"`
	// MainBackgroundFolded is in effect, like MainTasksQuiet.
	MainBackgroundFolded    bool `json:"main_background_folded"`
	FoldRoutineRelayReplies bool `json:"fold_routine_relay_replies"`
	// RecentTaskChats is in effect, like DoneNeedsDelivery.
	RecentTaskChats              bool     `json:"recent_task_chats"`
	WorkGuide                    bool     `json:"work_guide"`
	TodosV2                      bool     `json:"todos_v2"`
	TodosV2AutoSeen              string   `json:"todos_v2_auto_seen"`
	TodosV2PendingReviewSLAHours int      `json:"todos_v2_pending_review_sla_hours"`
	TodosV2CanWaitSLAHours       int      `json:"todos_v2_can_wait_sla_hours"`
	TodosV2FoldChildren          bool     `json:"todos_v2_fold_children"`
	TodosV2QuietHours            string   `json:"todos_v2_quiet_hours"`
	TodosV2DigestWindowMinutes   int      `json:"todos_v2_digest_window_minutes"`
	IntegrationsV2               bool     `json:"integrations_v2"`
	LabsSeen                     []string `json:"labs_seen"`
	// MeetingAttendanceDefault is in effect (off until changed).
	MeetingAttendanceDefault      string   `json:"meeting_attendance_default"`
	MeetingTranscriptionDefault   string   `json:"meeting_transcription_default"`
	MeetingGlossary               []string `json:"meeting_glossary"`
	MeetingTranscriptionExtension string   `json:"meeting_transcription_extension"`
	MeetingTranscriptionReady     bool     `json:"meeting_transcription_ready"`
	// IdleCloseHours is in effect (0: off).
	IdleCloseHours int `json:"idle_close_hours"`
	// ArchiveClosedDays is in effect (0: off).
	ArchiveClosedDays int `json:"archive_closed_days"`
	// ReviewSamplePercent is in effect (100: every finished task waits for task_review).
	ReviewSamplePercent int `json:"review_sample_percent"`
	// InterjectBackground is in effect, like DoneNeedsDelivery.
	InterjectBackground bool `json:"interject_background"`
	// ClaudeInteractive is in effect, like DoneNeedsDelivery.
	ClaudeInteractive bool `json:"claude_interactive"`
	// Exact owner-action controls are always visible with their effective values on a hub that supports them.
	OwnerReplyApproval         bool     `json:"owner_reply_approval"`
	PersistentOwnerActionCards bool     `json:"persistent_owner_action_cards"`
	OwnerApprovalPhrases       []string `json:"owner_reply_approval_phrases"`
	// AgentTasks is in effect, like DoneNeedsDelivery.
	AgentTasks bool `json:"agent_tasks"`
	// TaskLessons is in effect, like DoneNeedsDelivery.
	TaskLessons bool `json:"task_lessons"`
	// CloseEndedTerminals is in effect, like DoneNeedsDelivery.
	CloseEndedTerminals          bool `json:"close_ended_terminals"`
	AgentAutoNaming              bool `json:"agent_auto_naming"`
	AgentAutoNamingMax           int  `json:"agent_auto_naming_max"`
	AgentAutoNamingIntervalHours int  `json:"agent_auto_naming_interval_hours"`
	// InboxSignals is in effect, like DoneNeedsDelivery.
	InboxSignals bool `json:"inbox_signals"`
	// InboxTodoForward is in effect, like InboxSignals (#12492).
	InboxTodoForward bool `json:"inbox_todo_forward"`
	// PREventsRouteToAgent is in effect, like InboxSignals (false by default).
	PREventsRouteToAgent bool `json:"pr_events_route_to_agent"`
	// NextSuggestions is in effect, like DoneNeedsDelivery.
	NextSuggestions bool `json:"next_suggestions"`
	// MainSupervisesOwnTasks is in effect, like DoneNeedsDelivery: false means the background supervisor follows up
	// the tasks no leader supervises.
	MainSupervisesOwnTasks  bool `json:"main_supervises_own_tasks"`
	SupervisionBatchMinutes int  `json:"supervision_batch_minutes"`
	// AutoSupervisionMessages is in effect (true unless the mode is off), for clients that know only the switch.
	AutoSupervisionMessages bool `json:"auto_supervision_messages"`
	// AutoSupervisionMode is in effect: all, outcomes or off.
	AutoSupervisionMode string `json:"auto_supervision_mode"`
	SupervisionTriage   bool   `json:"supervision_triage"`
}

// view is GET /api/v1/assistant; withArchived lists archived conversations too (flagged `archived`). It lists the
// assistant's own conversations; a space (space "health") lists that space's instead (docs/assistant/health.md), with
// that space's directory as Cwd.
func (s *Service) view(ctx context.Context, withArchived bool, space ...string) assistantView {
	sp := ""
	if len(space) > 0 {
		sp = space[0]
	}
	v := s.settings(ctx)
	v.MeetingTranscriptionInstall = nil // the hub's own, never shown
	if sp == "" {
		v.MainMachineID = s.confirmedMainMachine(ctx, v)
	}
	v.Launcher = s.launcherNow(ctx)
	effectiveRules, _ := workspacenaming.Effective(v.WorkspaceRules, workspacenaming.Rules{})
	out := assistantView{WorkspaceNamingVersion: workspacenaming.Version, EffectiveWorkspaceRules: effectiveRules, SystemDefaults: new(workspacenaming.Defaults()), Settings: v, Manager: s.managerIdentity(), PermissionDefaults: s.permissionDefaults(), Conversations: []proto.Session{}, NotesEffective: s.notes(ctx), MainTasksQuiet: quietOn(v.MainTasksQuiet),
		ApprovalMirrorMain:  quietOn(v.ApprovalMirrorMain),
		ApprovalMirrorTools: approvalMirrorToolNames(v),
		StartRequestedTasks: quietOn(v.StartRequestedTasks), RecordingDays: recordingAudioDaysOf(v), RecordingAudioDays: recordingAudioDaysOf(v),
		RecordingTextDays: recordingTextDaysOf(v), MinutesDays: kb.MinutesDaysFor(minutesDaysSetting(v), recordingTextDaysOf(v)),
		IdleAutoCompact: s.idleAutoCompact(ctx), IdleAutoCompactMinutes: s.idleAutoCompactMinutes(ctx), IdleAutoCompactTokens: s.idleAutoCompactTokens(ctx),
		AgyIdleAutoCompact: s.agyIdleAutoCompact(ctx), AgyIdleAutoCompactMinutes: s.agyIdleAutoCompactMinutes(ctx), AgyIdleAutoCompactTokens: s.agyIdleAutoCompactTokens(ctx),
		CacheKeepalive: cacheKeepaliveOf(v).Enabled, CacheKeepaliveMinutes: cacheKeepaliveOf(v).IntervalMinutes, CacheKeepaliveMax: cacheKeepaliveOf(v).MaxRequests,
		IdleReleaseMinutes: s.idleReleaseMinutes(ctx), MeetingAttendanceDefault: meetingAttendanceOf(v), KBTopics: quietOn(v.KBTopics),
		Dashboards:                  v.Dashboards != nil && *v.Dashboards,
		MeetingTranscriptionDefault: meetingTranscriptionDefaultOf(v), MeetingGlossary: meetingGlossaryOf(v),
		MeetingTranscriptionExtension: meetingTranscriptionExtensionOf(v), MeetingTranscriptionReady: s.transcriptionReadyNow(),
		TaskWorkspacePreference:      s.taskWorkspacePreference(ctx),
		ReadonlyMode:                 readonlyModeOf(v),
		MainBackgroundFolded:         quietOn(v.MainBackgroundFolded),
		FoldRoutineRelayReplies:      v.foldRoutineRelayReplies(),
		WorkGuide:                    v.workGuideOn(),
		TodosV2:                      v.todosV2On(),
		TodosV2AutoSeen:              v.todosV2AutoSeenMode(),
		TodosV2PendingReviewSLAHours: v.todosV2PendingReviewSLAHours(),
		TodosV2CanWaitSLAHours:       v.todosV2CanWaitSLAHours(),
		TodosV2FoldChildren:          v.todosV2FoldChildrenOn(),
		TodosV2QuietHours:            v.todosV2QuietHours(),
		TodosV2DigestWindowMinutes:   v.todosV2DigestWindowMinutes(),
		IntegrationsV2:               v.integrationsV2On(),
		LabsSeen:                     append([]string{}, v.LabsSeen...),
		RecentTaskChats:              v.RecentTaskChats != nil && *v.RecentTaskChats,
		DeliveryGate:                 deliveryGateOf(v),
		DoneNeedsDelivery:            deliveryGateOf(v) == deliveryGateLive, IdleCloseHours: int(s.idleCloseAfter(ctx) / time.Hour),
		ArchiveClosedDays:            int(s.archiveClosedAfter(ctx) / (24 * time.Hour)),
		ReviewSamplePercent:          s.reviewSamplePercent(ctx),
		InterjectBackground:          v.InterjectBackground != nil && *v.InterjectBackground,
		ClaudeInteractive:            v.ClaudeInteractive != nil && *v.ClaudeInteractive,
		OwnerReplyApproval:           ownerReplyApprovalOn(v),
		PersistentOwnerActionCards:   persistentOwnerActionCardsOn(v),
		OwnerApprovalPhrases:         ownerApprovalPhrasesForView(v),
		AgentTasks:                   v.AgentTasks != nil && *v.AgentTasks,
		TaskLessons:                  v.TaskLessons != nil && *v.TaskLessons,
		CloseEndedTerminals:          v.CloseEndedTerminals != nil && *v.CloseEndedTerminals,
		AgentAutoNaming:              quietOn(v.AgentAutoNaming),
		AgentAutoNamingMax:           agentNamingLimit(v),
		AgentAutoNamingIntervalHours: agentNamingInterval(v),
		InboxSignals:                 v.InboxSignals != nil && *v.InboxSignals,
		InboxTodoForward:             v.InboxTodoForward != nil && *v.InboxTodoForward,
		PREventsRouteToAgent:         v.PREventsRouteToAgent != nil && *v.PREventsRouteToAgent,
		MainSupervisesOwnTasks:       !s.backgroundOn(v),
		SupervisionBatchMinutes:      supervisionBatchMinutesOf(v),
		AutoSupervisionMessages:      autoSupervisionOf(v),
		AutoSupervisionMode:          supervisionModeOf(v),
		SupervisionTriage:            v.supervisionTriageOn(),
		NextSuggestions:              v.NextSuggestions != nil && *v.NextSuggestions}
	if d, ok := s.device(ctx, v.MachineID); ok {
		out.Cwd = s.spaceCwd(ctx, d, sp)
		var list []proto.Session
		if s.fleetOn() {
			// Select the headless conversations before copying, decorating or serializing the inventory: coding
			// sessions can outnumber them by thousands.
			if m, found := s.fleetMachine(d.ID); found {
				list = s.decorateInventory(m.ID, s.assistantSessions(ctx, m), false)
			}
		} else {
			list, _ = s.sessions(ctx, d.ID)
		}
		for _, c := range list {
			if !c.Assistant || c.IsDeleted() || !(withArchived || !c.Archived) {
				continue
			}
			rowSpace, known := s.conversationRowSpace(ctx, c)
			if !known || rowSpace != sp {
				continue
			}
			c.Space = rowSpace
			out.Conversations = append(out.Conversations, c)
		}
	}
	if sp == "" && v.MainMachineID != "" && v.MainMachineID != v.MachineID {
		if c, ok := s.session(ctx, v.MainMachineID, v.Main); ok && c.Assistant {
			if rowSpace, known := s.conversationRowSpace(ctx, c); known && rowSpace == "" {
				c.Space = rowSpace
				out.Conversations = append(out.Conversations, c)
			}
		}
	}
	return out
}

func (s *Service) profileRoutes() {
	s.handle("GET /api/v1/assistant", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("view") == "main-runtime" {
			apierr.WriteJSON(w, s.mainExecution(r.Context()))
			return
		}
		archived := r.URL.Query().Get("archived")
		space := r.URL.Query().Get("space")
		if !s.trackKnown(r.Context(), space) {
			apierr.Write(w, http.StatusBadRequest, fmt.Errorf("space %q: not a registered space", space))
			return
		}
		out := s.view(r.Context(), archived == "1" || archived == "true", space)
		if tag := strings.TrimSpace(r.URL.Query().Get("tag")); tag != "" {
			out.Conversations = slices.DeleteFunc(out.Conversations, func(c proto.Session) bool { return !hasTag(c, tag) })
		}
		if r.Header.Get(RoleHeader) == roleExtension {
			out.StartDefaults = nil // read with its own scope (GET /api/v1/assistant/start-defaults), not assistant.ask
			out.Conversations = s.extensionViewConversations(r.Context(), out.Conversations)
		}
		apierr.WriteJSON(w, out)
	})
	s.handle("PUT /api/v1/assistant", s.putSettings)
	// warm: the app opened the chat tab; get a process ready so the first message skips claude's startup, and say what
	// the main conversation still needs.
	s.handle("POST /api/v1/assistant/warm", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Launcher string `json:"launcher"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		go s.warm(in.Launcher)
		// What the main conversation still needs, for the first screen's card (docs/assistant/onboarding.md 5.1); no probe, and
		// bounded so that opening the tab stays quick.
		out := map[string]any{"ok": true}
		if rd := s.readinessQuick(r.Context(), readinessWaitWarm); rd != nil {
			out["readiness"] = rd
		}
		apierr.WriteJSON(w, out)
	})
	s.handle("GET /api/v1/assistant/memory", func(w http.ResponseWriter, r *http.Request) {
		var res json.RawMessage
		if err := s.memory(r.Context(), map[string]any{"op": "list", "space": r.URL.Query().Get("space")}, &res); err != nil {
			apierr.Call(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(res)
	})
	s.handle("PUT /api/v1/assistant/memory/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change memory"))
			return
		}
		var in struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if err := s.memory(r.Context(), map[string]any{"op": "write", "name": r.PathValue("name"), "content": in.Content, "space": r.URL.Query().Get("space")}, nil); err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
	s.handle("DELETE /api/v1/assistant/memory/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change memory"))
			return
		}
		if err := s.memory(r.Context(), map[string]any{"op": "delete", "name": r.PathValue("name"), "space": r.URL.Query().Get("space")}, nil); err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
}

// warm gets a process ready (node assistant.warm) for the launcher on the assistant's machine; "" is what the main
// conversation's next message runs on: its pick (model_pick.go) on the main conversation's machine, else the one new
// conversations start with.
func (s *Service) warm(launcher string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cfg := s.settings(ctx)
	machine := cfg.MachineID
	pick, picked := modelPick{}, false
	switch {
	case launcher != "":
	case cfg.Main != "":
		// Kept under the main conversation's machine, where its next message runs: that one is warmed then.
		if pick, picked = s.modelPickOf(ctx, cfg.mainRouteMachine(), cfg.Main); picked {
			machine = cfg.mainRouteMachine()
		}
	default: // after /clear: the next main conversation starts on the assistant's machine with the carried pick
		if h := s.loadHandoff(ctx); h.Pick != nil {
			pick, picked = *h.Pick, true
		}
	}
	if proto.IsExternalLauncher(launcher) || picked && proto.IsExternalLauncher(pick.Launcher) {
		return // no real session exists for a prewarm's external scope
	}
	if launcher == "" && !picked {
		launcher = s.mainLauncherNow(ctx)
	}
	if _, ok := s.device(ctx, machine); !ok {
		return
	}
	cwd, err := s.prepare(ctx, machine)
	if err != nil {
		return
	}
	params := map[string]any{"cwd": cwd, "launcher": launcher, "is_main": true}
	if picked {
		err = s.applyPickFor(ctx, purposeMain, params, pick)
	} else {
		err = s.applyModel(ctx, purposeMain, params)
	}
	if err != nil {
		s.logf("warm model: %v", err)
		return
	}
	// The spare is adopted only by a new conversation in the same profile (node/assistant_spare.go), and a new one is
	// started in the main chat's, on a carried pick too (startMain, startOn).
	profiled := s.withNewConversationProfile(ctx, machine, params)
	ctx = context.WithValue(ctx, supervisionStartup{}, supervisionrole.RoleMain())
	err = s.call(ctx, machine, DeviceAssistantWarm, params, nil)
	if profiled && (nodeOutdated(err) || profileRefused(err)) { // as startOn: an old node, or another agent than Claude Code
		delete(params, "config_dir")
		err = s.call(ctx, machine, DeviceAssistantWarm, params, nil)
	}
	if err != nil {
		s.logf("warm assistant: %v", err)
	}
}

// memory runs one memory operation (node/memory.go) on the assistant's machine.
func (s *Service) memory(ctx context.Context, op map[string]any, out any) error {
	d, ok := s.device(ctx, s.settings(ctx).MachineID)
	if !ok {
		return errNoAssistantMachine
	}
	space, _ := op["space"].(string)
	delete(op, "space")
	if !s.trackKnown(ctx, space) {
		return badRequest(fmt.Errorf("space %q: not a registered space", space))
	}
	cwd := s.spaceCwd(ctx, d, space) // a space keeps a memory of its own (docs/assistant/health.md)
	if cwd == "" {
		return errMemoryMachineOffline
	}
	op["cwd"] = cwd
	return s.call(ctx, d.ID, DeviceAssistantMemory, op, out)
}
