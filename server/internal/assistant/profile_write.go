package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/workspacenaming"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// putSettings is PUT /api/v1/assistant: what the body sets changes ("-" as main clears it); a launcher is checked
// on the assistant's machine and goes into Model configuration › Main chat too.
func (s *Service) putSettings(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change settings"))
		return
	}
	ctx := r.Context()
	var in struct {
		Settings
		ConversationTagFilters json.RawMessage `json:"conversation_tag_filters"`
		ExpectedWorkGuide      *bool           `json:"expected_work_guide"`
		ExpectedTodosV2        *bool           `json:"expected_todos_v2"`
		ExpectedIntegrationsV2 *bool           `json:"expected_integrations_v2"`
		WorkspaceRulesPatch    json.RawMessage `json:"workspace_rules"`
		Permission             json.RawMessage `json:"default_permission_mode"`
		Readonly               json.RawMessage `json:"readonly_mode"`
		MinutesRetention       json.RawMessage `json:"minutes_days"`
		TranscriptionMode      json.RawMessage `json:"meeting_transcription_default"`
		Glossary               json.RawMessage `json:"meeting_glossary"`
		TranscriptionExtension json.RawMessage `json:"meeting_transcription_extension"`
		TodosV2QuietHoursPatch json.RawMessage `json:"todos_v2_quiet_hours"`
	}
	var raw json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	labsSeen, err := normalizeLabsSeen(in.LabsSeen)
	if err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	if in.LabsSeen != nil {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) == nil && len(fields) == 1 {
			s.putLabsSeen(w, ctx, labsSeen)
			return
		}
	}
	if in.Permission != nil {
		if err := json.Unmarshal(in.Permission, &in.DefaultPermissionMode); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
	}
	if in.Permission != nil && !slices.Contains(proto.PermissionModes, in.DefaultPermissionMode) {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-schedule.bad_permission_mode", i18n.Args{"mode": strconv.Quote(in.DefaultPermissionMode)}))
		return
	}
	if (in.Permission != nil || in.WorkGuide != nil || in.TodosV2 != nil ||
		in.TodosV2AutoSeen != "" || in.TodosV2PendingReviewSLAHours != nil ||
		in.TodosV2CanWaitSLAHours != nil || in.TodosV2FoldChildren != nil ||
		in.TodosV2QuietHoursPatch != nil || in.TodosV2DigestWindowMinutes != nil ||
		in.IntegrationsV2 != nil || in.MemoryExtensionStandingBytes != nil) && !s.explicitSettingsTarget(w, r) {
		return
	}
	if in.Readonly != nil {
		if err := json.Unmarshal(in.Readonly, &in.ReadonlyMode); err != nil || !validReadonlyMode(in.ReadonlyMode) {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-profile.bad_readonly_mode"))
			return
		}
		if !s.explicitSettingsTarget(w, r) {
			return
		}
	}
	if in.TaskWorkspacePreference != "" && !validWorkspacePreference(in.TaskWorkspacePreference) {
		apierr.Write(w, http.StatusBadRequest, errors.New("task_workspace_preference must be prefer-existing or prefer-new"))
		return
	}
	if len(in.WorkspaceRulesPatch) > 0 {
		if !s.explicitSettingsTarget(w, r) {
			return
		}
		if _, err := workspacenaming.Patch(workspacenaming.Rules{}, in.WorkspaceRulesPatch); err != nil {
			namingError(w, err)
			return
		}
	}
	if in.DispatchPolicy != nil && len(in.DispatchPolicy.Rules) > 0 {
		if err := in.DispatchPolicy.validate(true, time.Now().UTC()); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
	}
	if in.StartDefaults != nil && len(in.StartDefaults.Rules) > 0 {
		if err := in.StartDefaults.validate(true, time.Now().UTC()); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		in.StartDefaults.By = "owner" // this route is full credentials only
	}
	before := s.settings(ctx)
	if _, err := applyLabsSeen(&before, labsSeen); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	machine := before.MachineID
	if in.MachineID != "" {
		if _, ok := s.device(ctx, in.MachineID); !ok {
			apierr.Write(w, http.StatusBadRequest, errors.New("unknown machine"))
			return
		}
		machine = in.MachineID
	}
	if s.isWorkMachine(machine) {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-start.work_machine"))
		return
	}
	if in.Launcher != "" {
		if err := s.checkLauncher(ctx, machine, in.Launcher); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
	}
	if d := in.RecordingDays; d != nil && !recordingDaysValid(d, true) {
		apierr.Write(w, http.StatusBadRequest, errors.New("recording_days must be 1–3650"))
		return
	}
	for _, d := range []*int{in.RecordingAudioDays, in.RecordingTextDays} {
		if d != nil && !recordingDaysValid(d, false) {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-profile.bad_recording_days"))
			return
		}
	}
	var conversationTagFilters []string
	tagFiltersWritten := len(in.ConversationTagFilters) > 0
	if tagFiltersWritten && !bytes.Equal(bytes.TrimSpace(in.ConversationTagFilters), []byte("null")) {
		if err := json.Unmarshal(in.ConversationTagFilters, &conversationTagFilters); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		var err error
		conversationTagFilters, err = normalizeConversationTagFilters(conversationTagFilters)
		if err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
	}
	conversationOrder, orderErr := normalizeConversationOrder(in.ConversationOrder)
	if orderErr != nil {
		apierr.Write(w, http.StatusBadRequest, orderErr)
		return
	}
	if in.CacheKeepaliveMinutes != nil && (*in.CacheKeepaliveMinutes < 1 || *in.CacheKeepaliveMinutes > 55) || in.CacheKeepaliveMax != nil && (*in.CacheKeepaliveMax < 1 || *in.CacheKeepaliveMax > 144) {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-profile.bad_cache_keepalive"))
		return
	}
	if m := in.IdleAutoCompactMinutes; m != nil && (*m < minIdleAutoCompactMinutes || *m > maxIdleAutoCompactMinutes) {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-profile.bad_idle_auto_compact_minutes"))
		return
	}
	if m := in.SupervisionBatchMinutes; m != nil && (*m < 0 || *m > maxSupervisionBatchMinutes) {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-profile.bad_supervision_batch_minutes"))
		return
	}
	if n := in.IdleAutoCompactTokens; n != nil && (*n < minIdleAutoCompactTokens || *n > maxIdleAutoCompactTokens) {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-profile.bad_idle_auto_compact_tokens"))
		return
	}
	if m := in.AgyIdleAutoCompactMinutes; m != nil && !assistantwire.AgyIdleMinutesValid(*m) {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-profile.bad_agy_idle_auto_compact_minutes"))
		return
	}
	if n := in.AgyIdleAutoCompactTokens; n != nil && !assistantwire.AgyIdleTokensValid(*n) {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-profile.bad_agy_idle_auto_compact_tokens"))
		return
	}
	if m := in.IdleReleaseMinutes; m != nil && (*m < minIdleReleaseMinutes || *m > maxIdleReleaseMinutes) {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-profile.bad_idle_release_minutes"))
		return
	}
	if in.MinutesRetention != nil {
		if err := json.Unmarshal(in.MinutesRetention, &in.MinutesDays); err != nil || in.MinutesDays == nil ||
			*in.MinutesDays < minMinutesDays || *in.MinutesDays > maxMinutesDays {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-profile.bad_minutes_days"))
			return
		}
	}
	if m := in.MeetingAttendanceDefault; m != nil && !slices.Contains(meetingRoles, *m) {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-meeting.bad_default_role"))
		return
	}
	if g := in.DeliveryGate; g != nil && !slices.Contains(deliveryGates, *g) {
		apierr.Write(w, http.StatusBadRequest, errors.New("delivery_gate must be off, merged or live"))
		return
	}
	if in.AutoSupervisionMode != "" && !slices.Contains(supervisionModes, in.AutoSupervisionMode) {
		apierr.Write(w, http.StatusBadRequest, errors.New("auto_supervision_mode must be all, outcomes or off"))
		return
	}
	if in.TodosV2AutoSeen != "" && !slices.Contains(todosV2AutoSeenModes, in.TodosV2AutoSeen) {
		apierr.Write(w, http.StatusBadRequest, errors.New("todos_v2_auto_seen must be off, subtasks or all_autonomous"))
		return
	}
	if h := in.TodosV2PendingReviewSLAHours; h != nil && !slices.Contains(todosV2PendingReviewSLAOptions, *h) {
		apierr.Write(w, http.StatusBadRequest, errors.New("todos_v2_pending_review_sla_hours must be 0, 12, 24 or 48"))
		return
	}
	if h := in.TodosV2CanWaitSLAHours; h != nil && !slices.Contains(todosV2CanWaitSLAOptions, *h) {
		apierr.Write(w, http.StatusBadRequest, errors.New("todos_v2_can_wait_sla_hours must be 0, 24, 48 or 72"))
		return
	}
	if b := in.MemoryExtensionStandingBytes; b != nil && !slices.Contains(memoryExtensionStandingOptions, *b) {
		apierr.Write(w, http.StatusBadRequest, errors.New(memoryExtensionStandingBytesInvalid))
		return
	}
	var todosV2QuietHours string
	if in.TodosV2QuietHoursPatch != nil {
		var qh *string
		if err := json.Unmarshal(in.TodosV2QuietHoursPatch, &qh); err != nil || qh == nil {
			apierr.Write(w, http.StatusBadRequest, errors.New("todos_v2_quiet_hours must be empty or HH:MM-HH:MM (24-hour range with distinct start and end)"))
			return
		}
		todosV2QuietHours = strings.TrimSpace(*qh)
		if todosV2QuietHours != "" {
			if _, ok := parseTodosV2QuietHours(todosV2QuietHours); !ok {
				apierr.Write(w, http.StatusBadRequest, errors.New("todos_v2_quiet_hours must be empty or HH:MM-HH:MM (24-hour range with distinct start and end)"))
				return
			}
		}
	}
	if m := in.TodosV2DigestWindowMinutes; m != nil && !slices.Contains(todosV2DigestWindowOptions, *m) {
		apierr.Write(w, http.StatusBadRequest, errors.New("todos_v2_digest_window_minutes must be 0, 30, 60 or 120"))
		return
	}
	if code := s.readMeetingTranscriptionSettings(&in.Settings, in.TranscriptionMode, in.Glossary, in.TranscriptionExtension); code != "" {
		apierr.WriteCode(w, http.StatusBadRequest, code, i18n.Err("assistant-meeting."+code))
		return
	}
	if p := in.ReviewSamplePercent; p != nil && (*p < 0 || *p > 100) {
		apierr.Write(w, http.StatusBadRequest, errors.New("review_sample_percent must be 0–100"))
		return
	}
	if p := in.MainCompactWindow; p != nil && !validMainCompactWindow(*p) {
		apierr.Write(w, http.StatusBadRequest, errors.New("main_compact_window must be 0 or 100000–10000000"))
		return
	}
	if d := in.IdleCloseHours; d != nil && (*d < 0 || *d > maxIdleCloseHours) {
		apierr.Write(w, http.StatusBadRequest, errors.New("idle_close_hours must be 0–8760"))
		return
	}
	if d := in.ArchiveClosedDays; d != nil && (*d < 0 || *d > maxArchiveClosedDays) {
		apierr.Write(w, http.StatusBadRequest, errors.New("archive_closed_days must be 0–3650"))
		return
	}
	if err := validateAgentNamingSettings(in.Settings); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	if in.OwnerApprovalPhrases != nil {
		if _, err := ownerApprovalPhrases(Settings{OwnerApprovalPhrases: in.OwnerApprovalPhrases}); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
	}
	if (in.AgentAutoNaming != nil || in.AgentAutoNamingMax != nil || in.AgentAutoNamingIntervalHours != nil) && !s.explicitSettingsTarget(w, r) {
		return
	}
	if in.Timezone != "" {
		if _, err := time.LoadLocation(in.Timezone); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
	}
	if in.Launcher != "" {
		rec, err := s.modelRecord(ctx, "assistant")
		if err == nil {
			rec.Binding = mergeModel(rec.Binding, ModelBinding{Provider: "launcher." + in.Launcher})
			err = s.saveModel(ctx, "assistant", rec)
		}
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
	}
	if in.Main != "" && in.Main != "-" {
		// The user's from now on, as a main conversation started as one (startMain): no extension keeps its mode.
		if err := s.originMadeMain(ctx, in.Main); err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
	}
	launcher := s.launcherNow(ctx)
	var mainMachine string
	if in.Main != "" && in.Main != "-" && in.Main != before.Main {
		mainMachine = s.confirmedMainMachine(ctx, Settings{Main: in.Main, MachineID: machine})
	} else if (in.MachineID != "" || in.Main != "") && in.Main != "-" {
		mainMachine = s.confirmedMainMachine(ctx, before)
		if mainMachine == "" && before.Main != "" && in.MachineID != "" {
			// Keep the legacy route while its inventory is missing. This is a
			// routing hint only; the node must still attest actual execution.
			mainMachine = before.MachineID
		}
	}
	guideChanged := false
	errGuideChanged := errors.New("work guide setting changed")
	errTodosV2Changed := errors.New("todos_v2 setting changed")
	errIntegrationsV2Changed := errors.New("integrations_v2 setting changed")
	var labsSeenChanged bool
	var beforeTodoPrefs todoRevisionPrefs
	saved, err := s.updateSettings(ctx, func(v *Settings) error {
		beforeTodoPrefs = todoRevisionPrefsOf(*v)
		var err error
		if labsSeenChanged, err = applyLabsSeen(v, labsSeen); err != nil {
			return err
		}
		if in.WorkGuide != nil && in.ExpectedWorkGuide != nil && *in.ExpectedWorkGuide != v.workGuideOn() {
			return errGuideChanged
		}
		if in.TodosV2 != nil && in.ExpectedTodosV2 != nil && *in.ExpectedTodosV2 != v.todosV2On() {
			return errTodosV2Changed
		}
		if in.IntegrationsV2 != nil && in.ExpectedIntegrationsV2 != nil && *in.ExpectedIntegrationsV2 != v.integrationsV2On() {
			return errIntegrationsV2Changed
		}
		if len(in.WorkspaceRulesPatch) > 0 {
			rules, err := workspacenaming.Patch(v.WorkspaceRules, in.WorkspaceRulesPatch)
			if err != nil {
				return err
			}
			v.WorkspaceRules = rules
		}
		*v = s.effective(ctx, *v) // saved as they are in effect, as the hub saved them
		v.MachineID, v.Launcher = machine, launcher
		if in.Launcher != "" {
			v.Launcher = in.Launcher
		}
		if in.Main == "-" {
			v.Main, v.MainMachineID = "", ""
		} else if in.Main != "" && in.Main != before.Main {
			v.Main, v.MainMachineID = in.Main, mainMachine
		} else if v.Main == before.Main && v.MainMachineID == "" {
			v.MainMachineID = mainMachine
		}
		v.Timezone = zoneWritten(*v, in.Timezone, in.TimezonePicked)
		if in.TimezonePicked != nil {
			v.TimezonePicked = in.TimezonePicked
		}
		if in.Notes != nil {
			v.Notes = in.Notes
		}
		if in.OwnerAliases != nil {
			v.OwnerAliases = ownerAliases(in.OwnerAliases)
		}
		if in.MainTasksQuiet != nil {
			v.MainTasksQuiet = in.MainTasksQuiet
		}
		if in.ApprovalMirrorMain != nil {
			v.ApprovalMirrorMain = in.ApprovalMirrorMain
		}
		if in.ApprovalMirrorTools != nil {
			tools := cleanApprovalMirrorTools(*in.ApprovalMirrorTools)
			v.ApprovalMirrorTools = &tools
		}
		if in.CacheKeepalive != nil {
			v.CacheKeepalive = in.CacheKeepalive
		}
		if in.CacheKeepaliveMinutes != nil {
			v.CacheKeepaliveMinutes = in.CacheKeepaliveMinutes
		}
		if in.CacheKeepaliveMax != nil {
			v.CacheKeepaliveMax = in.CacheKeepaliveMax
		}
		if in.IdleAutoCompact != nil {
			v.IdleAutoCompact = in.IdleAutoCompact
		}
		if in.IdleAutoCompactMinutes != nil {
			v.IdleAutoCompactMinutes = in.IdleAutoCompactMinutes
		}
		if in.IdleAutoCompactTokens != nil {
			v.IdleAutoCompactTokens = in.IdleAutoCompactTokens
		}
		if in.AgyIdleAutoCompact != nil {
			v.AgyIdleAutoCompact = in.AgyIdleAutoCompact
		}
		if in.AgyIdleAutoCompactMinutes != nil {
			v.AgyIdleAutoCompactMinutes = in.AgyIdleAutoCompactMinutes
		}
		if in.AgyIdleAutoCompactTokens != nil {
			v.AgyIdleAutoCompactTokens = in.AgyIdleAutoCompactTokens
		}
		if in.IdleReleaseMinutes != nil {
			v.IdleReleaseMinutes = in.IdleReleaseMinutes
		}
		if in.StartRequestedTasks != nil {
			v.StartRequestedTasks = in.StartRequestedTasks
		}
		if in.TaskWorkspacePreference != "" {
			v.TaskWorkspacePreference = in.TaskWorkspacePreference
		}
		if in.Readonly != nil {
			v.ReadonlyMode = in.ReadonlyMode
		}
		if in.RecordingDays != nil { // the old name: a client from before #16412 sets the audio's days
			v.RecordingAudioDays = in.RecordingDays
		}
		if in.RecordingAudioDays != nil {
			v.RecordingAudioDays = in.RecordingAudioDays
		}
		if in.RecordingTextDays != nil {
			v.RecordingTextDays = in.RecordingTextDays
		}
		if in.MinutesDays != nil {
			v.MinutesDays = in.MinutesDays
		}
		if in.KBTopics != nil {
			v.KBTopics = in.KBTopics
		}
		if in.Dashboards != nil {
			v.Dashboards = in.Dashboards
		}
		if in.MainBackgroundFolded != nil {
			v.MainBackgroundFolded = in.MainBackgroundFolded
		}
		if in.FoldRoutineRelayReplies != nil {
			v.FoldRoutineRelayReplies = in.FoldRoutineRelayReplies
		}
		if in.MainCompactWindow != nil {
			v.MainCompactWindow = in.MainCompactWindow
		}
		if in.WorkGuide != nil {
			guideChanged = v.workGuideOn() != *in.WorkGuide
			v.WorkGuide = in.WorkGuide
		}
		if in.TodosV2 != nil {
			v.TodosV2 = in.TodosV2
		}
		if in.TodosV2AutoSeen != "" {
			v.TodosV2AutoSeen = in.TodosV2AutoSeen
		}
		if in.TodosV2PendingReviewSLAHours != nil {
			v.TodosV2PendingReviewSLAHours = in.TodosV2PendingReviewSLAHours
		}
		if in.TodosV2CanWaitSLAHours != nil {
			v.TodosV2CanWaitSLAHours = in.TodosV2CanWaitSLAHours
		}
		if in.TodosV2FoldChildren != nil {
			v.TodosV2FoldChildren = in.TodosV2FoldChildren
		}
		if in.TodosV2QuietHoursPatch != nil {
			v.TodosV2QuietHours = todosV2QuietHours
		}
		if in.TodosV2DigestWindowMinutes != nil {
			v.TodosV2DigestWindowMinutes = in.TodosV2DigestWindowMinutes
		}
		if in.IntegrationsV2 != nil {
			v.IntegrationsV2 = in.IntegrationsV2
		}
		if in.MemoryExtensionStandingBytes != nil {
			v.MemoryExtensionStandingBytes = in.MemoryExtensionStandingBytes
		}
		if in.RecentTaskChats != nil {
			v.RecentTaskChats = in.RecentTaskChats
		}
		if in.ConversationOrder != nil {
			v.ConversationOrder = conversationOrder
		}
		if tagFiltersWritten {
			v.ConversationTagFilters = conversationTagFilters
		}
		if in.MeetingAttendanceDefault != nil {
			v.MeetingAttendanceDefault = in.MeetingAttendanceDefault
		}
		if in.MeetingTranscriptionDefault != nil {
			v.MeetingTranscriptionDefault = in.MeetingTranscriptionDefault
		}
		if in.MeetingGlossary != nil {
			v.MeetingGlossary = in.MeetingGlossary
		}
		if in.MeetingTranscriptionExtension != nil {
			v.MeetingTranscriptionExtension = in.MeetingTranscriptionExtension
			v.MeetingTranscriptionInstall = in.MeetingTranscriptionInstall // readMeetingTranscriptionSettings's, never the body's
		}
		if in.IdleCloseHours != nil {
			v.IdleCloseHours = in.IdleCloseHours
		}
		if in.ArchiveClosedDays != nil {
			v.ArchiveClosedDays = in.ArchiveClosedDays
		}
		if in.DeliveryGate != nil {
			v.DeliveryGate = in.DeliveryGate
			v.DoneNeedsDelivery = nil
		} else if in.DoneNeedsDelivery != nil {
			v.DoneNeedsDelivery = in.DoneNeedsDelivery
			v.DeliveryGate = nil
		}
		if in.ReviewSamplePercent != nil {
			v.ReviewSamplePercent = in.ReviewSamplePercent
		}
		if in.InterjectBackground != nil {
			v.InterjectBackground = in.InterjectBackground
		}
		if in.ClaudeInteractive != nil {
			v.ClaudeInteractive = in.ClaudeInteractive
		}
		if in.OwnerReplyApproval != nil {
			v.OwnerReplyApproval = in.OwnerReplyApproval
		}
		if in.PersistentOwnerActionCards != nil {
			v.PersistentOwnerActionCards = in.PersistentOwnerActionCards
		}
		if in.OwnerApprovalPhrases != nil {
			v.OwnerApprovalPhrases = in.OwnerApprovalPhrases
		}
		if in.AgentTasks != nil {
			v.AgentTasks = in.AgentTasks
		}
		if in.TaskLessons != nil {
			v.TaskLessons = in.TaskLessons
		}
		if in.CloseEndedTerminals != nil {
			v.CloseEndedTerminals = in.CloseEndedTerminals
		}
		if in.AgentAutoNaming != nil {
			v.AgentAutoNaming = in.AgentAutoNaming
		}
		if in.AgentAutoNamingMax != nil {
			v.AgentAutoNamingMax = in.AgentAutoNamingMax
		}
		if in.AgentAutoNamingIntervalHours != nil {
			v.AgentAutoNamingIntervalHours = in.AgentAutoNamingIntervalHours
		}
		if in.InboxSignals != nil {
			v.InboxSignals = in.InboxSignals
		}
		if in.InboxTodoForward != nil {
			v.InboxTodoForward = in.InboxTodoForward
		}
		if in.PREventsRouteToAgent != nil {
			v.PREventsRouteToAgent = in.PREventsRouteToAgent
		}
		if in.InboxPost != nil {
			v.InboxPost = in.InboxPost
		}
		if in.NextSuggestions != nil {
			v.NextSuggestions = in.NextSuggestions
		}
		if in.MainSupervisesOwnTasks != nil {
			v.MainSupervisesOwnTasks = in.MainSupervisesOwnTasks
		}
		if in.SupervisionBatchMinutes != nil {
			v.SupervisionBatchMinutes = in.SupervisionBatchMinutes
		}
		if in.SupervisionTriage != nil {
			v.SupervisionTriage = in.SupervisionTriage
		}
		if in.AutoSupervisionMode != "" {
			// The mode wins over the older switch in the same body, and keeps it in step for older readers.
			on := in.AutoSupervisionMode != supervisionModeOff
			v.AutoSupervisionMode, v.AutoSupervisionMessages = in.AutoSupervisionMode, &on
		} else if in.AutoSupervisionMessages != nil {
			v.AutoSupervisionMessages = in.AutoSupervisionMessages
			v.AutoSupervisionMode = supervisionModeOff
			if *in.AutoSupervisionMessages {
				v.AutoSupervisionMode = supervisionModeAll
			}
		}
		if in.DefaultPermissionMode != "" {
			v.DefaultPermissionMode = in.DefaultPermissionMode
		}
		if in.DispatchPolicy != nil {
			// A non-empty policy was validated above before any other change; empty rules clear it.
			if len(in.DispatchPolicy.Rules) == 0 {
				v.DispatchPolicy = nil
			} else {
				v.DispatchPolicy = in.DispatchPolicy
			}
		}
		if in.StartDefaults != nil { // the same for the start defaults (start_defaults.go)
			if len(in.StartDefaults.Rules) == 0 {
				v.StartDefaults = nil
			} else {
				v.StartDefaults = in.StartDefaults
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errGuideChanged) || errors.Is(err, errTodosV2Changed) || errors.Is(err, errIntegrationsV2Changed) {
			apierr.WriteCode(w, http.StatusConflict, "configuration_conflict", err)
			return
		}
		if errors.Is(err, errLabsSeenLimit) {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		apierr.Write(w, http.StatusInternalServerError, err)
		return
	}
	if labsSeenChanged {
		s.publishLabsSeen(ctx, saved.LabsSeen)
	}
	if guideChanged {
		s.mcpChanges.notify()
	}
	if todoRevisionPrefsOf(saved) != beforeTodoPrefs {
		s.todoRevisionChanged()
	}
	s.settingsChanged(ctx)
	// Revoke already captured requests as part of a policy edit. Forwarding stays
	// available; only a subsequent genuine request can rearm the snapshot.
	if (in.CacheKeepalive != nil || in.CacheKeepaliveMinutes != nil || in.CacheKeepaliveMax != nil) && cacheKeepaliveOf(before) != cacheKeepaliveOf(s.settings(ctx)) {
		clearCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		for _, ref := range s.cacheKeepaliveRefs(ctx, before) {
			_ = s.call(clearCtx, ref.MachineID, "assistant.cache_clear", map[string]any{"session_id": ref.SessionID}, nil)
		}
	}
	if in.SupervisionBatchMinutes != nil && supervisionBatchMinutesOf(s.settings(ctx)) != supervisionBatchMinutesOf(before) {
		s.rescheduleSupervision()
	}
	if in.MeetingTranscriptionExtension != nil {
		s.transcriptionReadinessChanged() // named again: probed again (the answer stands meanwhile)
	}
	if after := s.settings(ctx).Dashboards; in.Dashboards != nil && (before.Dashboards != nil && *before.Dashboards) != (after != nil && *after) {
		s.pagesListChanged(ctx) // the sidebar's Dashboards come or go (pages_events.go)
		s.mcpChanges.notify()   // and the page tool with them, in every open conversation's list (pages_tool.go)
	}
	if in.Main != "" && in.Main != "-" && in.Main != before.Main {
		if before.Main != "" {
			s.handOverMainTasks(context.WithoutCancel(ctx), before.Main, in.Main)  // its open tasks follow the main conversation (main_handover.go)
			s.handOverMainAlways(context.WithoutCancel(ctx), before.Main, in.Main) // its Always-allow grants follow once (#16158)
		} else {
			s.handOverFormerMainTasks(context.WithoutCancel(ctx)) // after newMain / clearMain, old main sits in Former
			if len(before.Former) > 0 && before.Former[0].SessionID != "" && before.Former[0].SessionID != in.Main {
				s.handOverMainAlways(context.WithoutCancel(ctx), before.Former[0].SessionID, in.Main)
			}
		}
	}
	apierr.WriteJSON(w, s.view(ctx, false))
}

// checkLauncher: the assistant runs Claude Code (claude -p), Codex (codex app-server) or an agent runtime that says it
// can (its launcher has assistant: docs/integrations/runtimes.md "Assistant"); anything else would fail every message. One that can't
// be probed now goes.
func (s *Service) checkLauncher(ctx context.Context, machine, launcher string) error {
	if proto.IsExternalLauncher(launcher) {
		return i18n.Err("external-adapter.purpose_unsupported")
	}
	if launcher == "claude" {
		return nil
	}
	if _, ok := s.device(ctx, machine); !ok || s.runtimeAssistantLauncher(ctx, machine, launcher) {
		return nil
	}
	var res struct {
		Detail proto.LauncherDetail `json:"detail"`
	}
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err := s.call(pctx, machine, DeviceLaunchersProbe, map[string]any{"name": launcher}, &res)
	cancel()
	if err == nil && res.Detail.Agent != "" && res.Detail.Agent != "claude" && res.Detail.Agent != "codex" {
		return i18n.Err("assistant-profile.launcher_agent", i18n.Args{"launcher": launcher, "agent": res.Detail.Agent})
	}
	return nil
}

// runtimeAssistantLauncher: launcher is one an agent runtime on machine offers to run the assistant (the node's
// launchers.list says assistant), not a login-shell launcher to probe.
func (s *Service) runtimeAssistantLauncher(ctx context.Context, machine, launcher string) bool {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var res struct {
		Launchers []proto.Launcher `json:"launchers"`
	}
	if err := s.cachedLauncherList(ctx, machine, launcherListReadOpts{}, &res); err != nil {
		return false
	}
	return slices.ContainsFunc(res.Launchers, func(l proto.Launcher) bool { return l.Name == launcher && l.Assistant })
}

// zoneWritten is the zone a settings write leaves (#1855): one the user picked (`timezone_picked` true) or a device's
// own once the user goes back to following the devices (false); without `timezone_picked` the write is a device syncing
// its zone, as apps did before picking was kept, and it lands only while no zone was picked. An empty zone changes nothing.
func zoneWritten(saved Settings, zone string, picked *bool) string {
	if zone == "" || (picked == nil && saved.TimezonePicked != nil && *saved.TimezonePicked) {
		return saved.Timezone
	}
	return zone
}
