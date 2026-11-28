package extension

import (
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// Defined in the contract package extwire (docs/server/architecture.md "Contract packages"); the old names stay.
const (
	ScopeAgentsControl           = extwire.ScopeAgentsControl
	ScopeAgentsArchiveIdle       = extwire.ScopeAgentsArchiveIdle
	ScopeAgentsPermissionMode    = extwire.ScopeAgentsPermissionMode
	ScopeAgentsRead              = extwire.ScopeAgentsRead
	ScopeAssistantAsk            = extwire.ScopeAssistantAsk
	ScopeSpaces                  = extwire.ScopeSpaces
	ScopeCallPrefix              = extwire.ScopeCallPrefix
	ScopeInboxRead               = extwire.ScopeInboxRead
	ScopeInboxWrite              = extwire.ScopeInboxWrite
	ScopeKBImportStatus          = extwire.ScopeKBImportStatus
	ScopeKBRead                  = extwire.ScopeKBRead
	ScopeKBWrite                 = extwire.ScopeKBWrite
	ScopeSentRead                = extwire.ScopeSentRead
	ScopeMachinesRead            = extwire.ScopeMachinesRead
	ScopeMachinesPair            = extwire.ScopeMachinesPair
	ScopeBrowserRun              = extwire.ScopeBrowserRun
	ScopeNotify                  = extwire.ScopeNotify
	ScopePushGrading             = extwire.ScopePushGrading
	ScopeTodosRead               = extwire.ScopeTodosRead
	ScopeTodosWrite              = extwire.ScopeTodosWrite
	ScopeTasksSchedule           = extwire.ScopeTasksSchedule
	ScopeDecisionsRaise          = extwire.ScopeDecisionsRaise
	ScopeBotsFeishuIdentity      = extwire.ScopeBotsFeishuIdentity
	ScopeBotsFeishuHandoff       = extwire.ScopeBotsFeishuHandoff
	ScopeLeadersRead             = extwire.ScopeLeadersRead
	ScopeLeadersWrite            = extwire.ScopeLeadersWrite
	ScopeAssistantDispatchPolicy = extwire.ScopeAssistantDispatchPolicy
	ScopeAssistantStartDefaults  = extwire.ScopeAssistantStartDefaults
	ScopeDispatchGuard           = extwire.ScopeDispatchGuard
	ScopeModels                  = extwire.ScopeModels
	ScopeAssistantRead           = extwire.ScopeAssistantRead
	ScopeAssistantMemoryRead     = extwire.ScopeAssistantMemoryRead
	ScopeAssistantEntriesRead    = extwire.ScopeAssistantEntriesRead
	ScopeTranscription           = extwire.ScopeTranscription
	ScopeMarketRefreshOwn        = extwire.ScopeMarketRefreshOwn
	ScopeAssistantEvents         = extwire.ScopeAssistantEvents
	ScopeLarkMessagesRead        = extwire.ScopeLarkMessagesRead
)

// HubScopes are the scopes, in the order the install screen lists them.
var HubScopes = []string{ScopeKBRead, ScopeKBWrite, ScopeKBImportStatus, ScopeSentRead, ScopeInboxRead, ScopeInboxWrite, ScopeAgentsRead, ScopeAgentsControl,
	ScopeAgentsArchiveIdle, ScopeAgentsPermissionMode, ScopeMachinesRead, ScopeMachinesPair, ScopeBrowserRun, ScopeNotify, ScopePushGrading, ScopeAssistantAsk, ScopeSpaces, ScopeAssistantRead, ScopeAssistantMemoryRead, ScopeAssistantEntriesRead,
	ScopeTodosRead, ScopeTodosWrite, ScopeTasksSchedule, ScopeDecisionsRaise, ScopeBotsFeishuIdentity, ScopeBotsFeishuHandoff, ScopeLeadersRead, ScopeLeadersWrite, ScopeAssistantDispatchPolicy, ScopeAssistantStartDefaults, ScopeDispatchGuard, ScopeModels,
	ScopeTranscription, ScopeMarketRefreshOwn, ScopeAssistantEvents, ScopeLarkMessagesRead}

// InEveryScope says whether a token of every scope (an extension granted the hub's full token the old way, hub_api,
// that gets one of every scope instead) has scope s. Scopes whose authority must be agreed to by name are left out:
// agents.archive_idle (which archives and closes long-idle coding-agent sessions),
// agents.permission_mode, push.grading (which changes how pushes are graded),
// assistant.start_defaults (which can raise what task Agents spend), decisions.raise (which can
// interrupt the owner with a new decision), dispatch.guard (which changes which conversations may hand work out),
// machines.pair (which can issue one-time pairing links for new pending computers),
// browser.run (which runs scripts in a paired computer's browser),
// tasks.schedule (which assigns target computers for pending tasks),
// market.refresh_own (which spends the source marketplace's API quota),
// lark.messages.read (which receives newly synced Lark message metadata events), and
// bots.feishu.handoff (which makes the bot write in a group and can interrupt the owner).
// bots.feishu.identity stays in every scope: it names the bot and holds no secret.
func InEveryScope(s string) bool {
	return s != ScopeKBImportStatus && s != ScopeAgentsArchiveIdle && s != ScopeAgentsPermissionMode && s != ScopePushGrading && s != ScopeAssistantStartDefaults && s != ScopeDecisionsRaise && s != ScopeDispatchGuard && s != ScopeMachinesPair && s != ScopeBrowserRun && s != ScopeMarketRefreshOwn && s != ScopeSpaces && s != ScopeTasksSchedule && s != ScopeLarkMessagesRead && s != ScopeBotsFeishuHandoff
}

// EveryScope is the scopes of a token of every scope (InEveryScope), in HubScopes' order.
func EveryScope() []string {
	return slices.DeleteFunc(slices.Clone(HubScopes), func(s string) bool { return !InEveryScope(s) })
}

var scopeTitles = map[string]string{
	ScopeKBRead:                  "scope_kb_read",
	ScopeKBImportStatus:          "scope_kb_import_status",
	ScopeKBWrite:                 "scope_kb_write",
	ScopeSentRead:                "scope_sent_read",
	ScopeInboxRead:               "scope_inbox_read",
	ScopeInboxWrite:              "scope_inbox_write",
	ScopeAgentsRead:              "scope_agents_read",
	ScopeAgentsControl:           "scope_agents_control",
	ScopeAgentsArchiveIdle:       "scope_agents_archive_idle",
	ScopeAgentsPermissionMode:    "scope_agents_permission_mode",
	ScopeMachinesRead:            "scope_machines_read",
	ScopeMachinesPair:            "scope_machines_pair",
	ScopeBrowserRun:              "scope_browser_run",
	ScopeNotify:                  "scope_notify",
	ScopePushGrading:             "scope_push_grading",
	ScopeAssistantAsk:            "scope_assistant_ask",
	ScopeSpaces:                  "scope_spaces",
	ScopeTodosRead:               "scope_todos_read",
	ScopeTodosWrite:              "scope_todos_write",
	ScopeTasksSchedule:           "scope_tasks_schedule",
	ScopeDecisionsRaise:          "scope_decisions_raise",
	ScopeBotsFeishuIdentity:      "scope_bots_feishu_identity",
	ScopeBotsFeishuHandoff:       "scope_bots_feishu_handoff",
	ScopeLeadersRead:             "scope_leaders_read",
	ScopeLeadersWrite:            "scope_leaders_write",
	ScopeAssistantDispatchPolicy: "scope_assistant_dispatch_policy",
	ScopeAssistantStartDefaults:  "scope_assistant_start_defaults",
	ScopeDispatchGuard:           "scope_dispatch_guard",
	ScopeModels:                  "scope_models",
	ScopeAssistantRead:           "scope_assistant_read",
	ScopeAssistantMemoryRead:     "scope_assistant_memory_read",
	ScopeAssistantEntriesRead:    "scope_assistant_entries_read",
	ScopeTranscription:           "scope_transcription",
	ScopeMarketRefreshOwn:        "scope_market_refresh_own",
	ScopeAssistantEvents:         "scope_assistant_events",
	ScopeLarkMessagesRead:        "scope_lark_messages_read",
}

// ScopeTitle is how the install screen says scope s; ok false for what is not a scope.
func ScopeTitle(s string) (string, bool) { return ScopeTitleIn(i18n.Default, s) }

// ScopeTitleIn names a known scope in the requested language.
func ScopeTitleIn(l i18n.Lang, s string) (string, bool) {
	if id, ok := strings.CutPrefix(s, ScopeCallPrefix); ok {
		if !idPattern.MatchString(id) {
			return "", false
		}
		return permissionText(l, "scope_call", i18n.Args{"id": id}), true
	}
	key, ok := scopeTitles[s]
	if !ok {
		return "", false
	}
	return permissionText(l, key), true
}

// hubLine is the line for scopes (all an extension asks for, or those an upgrade adds), in HubScopes' order.
func hubLine(l i18n.Lang, title string, scopes []string) PermissionLine {
	var what []string
	for _, known := range HubScopes {
		for _, s := range scopes {
			if s == known {
				t, _ := ScopeTitleIn(l, s)
				what = append(what, t)
			}
		}
	}
	for _, s := range scopes {
		if strings.HasPrefix(s, ScopeCallPrefix) {
			t, _ := ScopeTitleIn(l, s)
			what = append(what, t)
		}
	}
	return PermissionLine{Key: "hub", Title: title, Detail: strings.Join(what, permissionText(l, "sentence_separator"))}
}
