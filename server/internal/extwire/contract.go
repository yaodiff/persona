package extwire

import "strings"

// The scopes of the hub's API an extension may ask for (permissions.hub; docs/extensions/extensions.md"Permissions"). Each route of the
// hub an extension's token may call names one (extmgr/extension_scopes.go); a route naming none is for full tokens
// only.
const (
	ScopeKBImportStatus = "kb.import_status"
	ScopeKBRead         = "kb.read"
	ScopeKBWrite        = "kb.write"
	// ScopeSentRead reveals which archived messages Persona sent as the user (kb.day).
	ScopeSentRead      = "sent.read"
	ScopeInboxRead     = "inbox.read"
	ScopeInboxWrite    = "inbox.write"
	ScopeAgentsRead    = "agents.read"
	ScopeAgentsControl = "agents.control"
	ScopeMachinesRead  = "machines.read"
	// ScopeMachinesPair lets an extension request a one-time active-mode pairing link (machines.pair_link) for a
	// pending link computer and read its connection status (machines.status). Granted by name only; newly added
	// computers still wait for the owner's "This is my computer" confirmation.
	ScopeMachinesPair = "machines.pair"
	// ScopeBrowserRun lets an extension list browser-enabled computers (browser.machines) and run scripts inside an
	// extension-isolated task space (browser.run) via a pluggable BrowserBackend (e.g. ego lite, future CDP). Granted
	// by name only (#12866).
	ScopeBrowserRun = "browser.run"
	ScopeNotify     = "notify"
	// ScopePushGrading lets an extension manage its own push grading layer and preview grading decisions.
	// It is granted by name only and does not include reading held pushes or changing the owner's base settings.
	ScopePushGrading  = "push.grading"
	ScopeAssistantAsk = "assistant.ask"
	ScopeTodosRead    = "todos.read"
	ScopeTodosWrite   = "todos.write"
	// ScopeDecisionsRaise lets an explicitly authorized caller raise, poll and
	// withdraw its own pending owner decisions. It never grants owner answers.
	ScopeDecisionsRaise = "decisions.raise"
	// ScopeLeadersRead: being told of directions (leader.changed) and what they report (leader.report), and listing
	// them (GET /api/v1/assistant/leaders); #3484. A direction need not have a Persona conversation (#8236).
	ScopeLeadersRead = "leaders.read"
	// ScopeLeadersWrite: creating a conversationless direction and reporting any current direction's progress.
	// It does not imply ScopeLeadersRead.
	ScopeLeadersWrite = "leaders.write"
	// ScopeAssistantDispatchPolicy: reading and writing the assistant's dispatch policy
	// (GET/PUT /api/v1/assistant/dispatch-policy), the organization rule for which conversations may hand out work.
	ScopeAssistantDispatchPolicy = "assistant.dispatch_policy"
	// ScopeAssistantStartDefaults: reading the Agent presets a task can start with on a computer and reading and
	// writing the defaults task Agents start with (GET /api/v1/assistant/worker-presets, GET/PUT
	// /api/v1/assistant/start-defaults).
	ScopeAssistantStartDefaults = "assistant.start_defaults"
	// ScopeDispatchGuard: reading and writing the explicit-target dispatch guard and narrowing the affected
	// conversations' tool lists (GET/PUT /api/v1/assistant/dispatch-guard). Granted by name only.
	ScopeDispatchGuard = "dispatch.guard"
	// ScopeAgentsPermissionMode: changing the permission mode of a conversation the extension opened itself, and of no
	// other (the hub checks who opened it: hub/extension_modes.go).
	ScopeAgentsPermissionMode = "agents.permission_mode"
	// ScopeModels: calling a model through the hub (model.complete) on the extension's own purpose in Settings ›
	// Models; never a key (#6102).
	ScopeModels = "models"
	// ScopeAssistantRead: reading the assistant's own-space conversations, the user's and the assistant's words
	// (assistant.conversations, assistant.messages; #1288). assistant.ask does not include it.
	ScopeAssistantRead = "assistant.read"
	// ScopeAssistantMemoryRead: listing the assistant's own-space memory entries (assistant.memory), read-only; the
	// health space's memory is never included and writing it stays with full credentials.
	ScopeAssistantMemoryRead = "assistant.memory.read"
	// ScopeTranscription: handing the extension's own audio to the transcription the owner chose for meetings
	// (transcription.start / audio / finish / cancel), never a key and never anyone else's audio (#8485).
	ScopeTranscription = "transcription"
	// ScopeMarketRefreshOwn: asking for a background read of only the marketplace the calling extension was installed
	// from. It is never inherited through an all-scope token; the manifest must name it and the owner must approve it.
	ScopeMarketRefreshOwn = "market.refresh_own"
	// ScopeAssistantEvents: telling the assistant's existing main conversation about something the extension observed
	// (assistant.event). It starts, restarts and replaces nothing, and reads nothing back: assistant.ask and
	// assistant.read do not include it.
	ScopeAssistantEvents = "assistant.events"
	// ScopeLarkMessagesRead: receiving event.lark.messages metadata notifications when Lark user sync commits new
	// messages to the outbox (binding, chat_id, message_id, sender_open_id, at, type; no message body). Granted by
	// name only, not covered by the legacy full-hub grant.
	ScopeLarkMessagesRead = "lark.messages.read"
	// ScopeCallPrefix and an extension's id: calling that extension's own HTTP (/api/v1/extensions/<id>/…).
	ScopeCallPrefix = "extensions.call:"
)

// ProviderModel is one model of a provider.
type ProviderModel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// APIReceipt is one route of a plugin's API with a send receipt: Method and Path (under the prefix, "*" one segment)
// and the receipt's Kind. The receipt's id comes from the body's client_id, or the caller's X-Persona-Op-Id.
type APIReceipt struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Kind   string `json:"kind"`
}

// Matches says whether a request (its path under the prefix) is the receipt's route.
func (r APIReceipt) Matches(method, path string) bool {
	if !strings.EqualFold(r.Method, method) {
		return false
	}
	want := strings.Split(strings.Trim(r.Path, "/"), "/")
	got := strings.Split(strings.Trim(path, "/"), "/")
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if want[i] != "*" && want[i] != got[i] {
			return false
		}
	}
	return true
}
