package assistantwire

import "github.com/nwverse/persona/server/internal/devicewire"

// ---- the bus ----

// AssistantID is the bus identity the assistant connects as (a manager's token, hub/bustokens.go).
const AssistantID = "assistant"

// Capabilities a manager names in manager.hello's capabilities (stable names: never renamed or reused).
const (
	// CapabilityConfigOwn: the manager reads its own sections of the configuration from its manager.json (owner,
	// notes, Agents, the knowledge base, the models, a card's wait): the hello does not carry them any more, and a
	// manager without it runs on its built-in defaults (docs/assistant/managers.md "Configuration").
	CapabilityConfigOwn = "config.own"
	// CapabilityAgentSessionArchives: the manager owns recoverable Codex/Claude archives.
	CapabilityAgentSessionArchives = "agent_session_archives"
	// CapabilityAgentNaming: ordinary full-user renames record manual intent before node dispatch.
	CapabilityAgentNaming = "agent_naming.v1"
	// CapabilityAgentAutoname: the manager names one Agent session with the model when the user asks
	// (POST /api/v1/sessions/{mid}/{sid}/autoname; docs/protocol/protocol.md "Naming an Agent on request"). The hub
	// lists the agent_autoname feature only while every manager that carries Agents names it.
	CapabilityAgentAutoname = "agent_autoname"
	// CapabilitySessionsSideFold: the manager folds a Claude desktop app's side sessions (parent_agent_id) under the
	// session they were started from, folded_as "side", with folded_side and the children's sides rollup
	// (docs/protocol/protocol.md "Session hierarchy", #7944). The hub lists the sessions_side_fold feature only while
	// every manager that carries Agents names it; until then clients keep drawing side sessions as rows.
	CapabilitySessionsSideFold = "sessions_side_fold"
	// CapabilityAgentSessionArchiveClose: the manager's archive also takes ?close=1, "Archive and close", which closes
	// the agent on its computer (session.retire) once archived (docs/protocol/protocol.md "Agent session archives").
	CapabilityAgentSessionArchiveClose = "agent_session_archive_close"
	// CapabilityMachinesOwn: the manager keeps its own list of computers in its manager.json and takes the hub's edits
	// of it (devicewire MethodMachinesPut / Remove / Get) once the hub hands it MachinesOwn
	// (docs/assistant/managers.md "Each manager's computers").
	CapabilityMachinesOwn = "machines.own"
	// CapabilityMeetingAttendance: the manager attends recorded meetings (the recordings' attendance fields, …/live,
	// …/ask and meeting_attendance_default; docs/assistant/meeting-minutes.md "Attending a meeting"). The hub lists the
	// meeting_attendance feature only while the default manager names it.
	CapabilityMeetingAttendance = "meeting_attendance"
	// CapabilityMeetingConversation: the manager takes the meeting conversation's phase 1 (the upload's room and
	// private, the room conversation, …/ask answered by it, …/just-me, …/private, …/followup, the meeting_live event;
	// docs/assistant/meeting-minutes.md "Meeting conversation (#3862)"). The hub lists meeting_conversation only while
	// the default manager names it. A manager that names it names CapabilityMeetingAttendance too.
	CapabilityMeetingConversation = "meeting_conversation"
	// CapabilityMeetingPhotos: the manager takes the meeting conversation's phase 3, its photos (…/photos, forwarding
	// them to the room, the room's notes, citations in the minutes; docs/assistant/meeting-minutes.md "Phase 3:
	// photos"). The hub lists meeting_photos only while the default manager names it beside
	// CapabilityMeetingConversation.
	CapabilityMeetingPhotos = "meeting_photos"
	// CapabilityMeetingLiveView: the manager takes the meeting conversation's phase 2, its live view (lines and topics
	// on GET …/live, the room's meeting_topics_set, lines and topics in meeting_live; docs/assistant/meeting-minutes.md
	// "Phase 2: live view"). The hub lists meeting_live_view only while the default manager names it beside
	// CapabilityMeetingConversation.
	CapabilityMeetingLiveView = "meeting_live_view"
	// CapabilityMeetingHighlights: the manager takes the meeting conversation's phase 2b, its highlights (highlights and
	// highlights_at on GET …/live, the room's meeting_highlights_set, highlights in meeting_live and in GET …/live's
	// revs and parts; docs/assistant/meeting-minutes.md "Phase 2b: highlights"). The hub lists meeting_highlights only
	// while the default manager names it beside CapabilityMeetingLiveView.
	CapabilityMeetingHighlights = "meeting_highlights"
	// CapabilityMeetingNotes: the manager takes a room meeting's notes role (#8851: room.attendance and the timeline's
	// roles may be notes, a room that keeps the topics and highlights while Persona answers nothing, …/ask refused
	// with not_attending; docs/assistant/meeting-minutes.md "The notes role (#8851)"). The hub lists meeting_notes
	// only while the default manager names it beside CapabilityMeetingLiveView.
	CapabilityMeetingNotes = "meeting_notes"
	// CapabilityMeetingEcho: the manager takes Persona's echo marked on a meeting's upload (#5905: the segments' echo
	// and the spoken spans, its own copy of the rule, echo lines kept but left out of the minutes, the summary and the
	// follow-up; docs/assistant/meeting-minutes.md "Persona's echo"). The hub lists meeting_echo only while the default
	// manager names it beside CapabilityMeetingAttendance; until then a phone leaves echo lines out of the upload.
	CapabilityMeetingEcho = "meeting_echo"
	// CapabilityMeetingTranscription: the manager transcribes meetings on the hub (#3862 phase 5: the upload's
	// transcription and timeline, the audio socket GET …/audio and the gap uploads PUT …/audio, the hub's transcript of
	// record, the meeting_transcription_* settings; docs/assistant/meeting-minutes.md "Phase 5: hub transcription").
	// The hub lists meeting_transcription only while the default manager names it beside CapabilityMeetingConversation
	// and CapabilityMeetingLiveView.
	CapabilityMeetingTranscription = "meeting_transcription"
	// CapabilityRecordingDay: the manager takes all-day recordings and writes their daily recap (the upload's kind
	// day, POST …/segments, detail.recap, recap_state; #4741). The hub lists the recording_day feature only while the
	// default manager names it.
	CapabilityRecordingDay = "recording_day"
	// CapabilityAssistantSearch: the manager answers GET /api/v1/assistant/search from its index of the chats' messages
	// (#6282, docs/protocol/protocol.md "Assistant chat search"). The hub lists the assistant_search feature only while
	// the default manager names it; the apps keep filtering titles and last messages without it.
	CapabilityAssistantSearch = "assistant_search"
	// CapabilityAssistantPages: the manager keeps the assistant's pages (Dashboards, #15017,
	// docs/assistant/dashboards.md): the reserved extension -pages in GET /api/v1/extensions/-/ui and its pages, the
	// dashboards switch in GET/PUT /api/v1/assistant and the assistant_pages event. Only the built-in manager names
	// it, the switch on or off; the hub lists the assistant_pages feature only while the default manager names it, and
	// the apps show the switch only then.
	CapabilityAssistantPages = "assistant_pages"
	// CapabilityTaskInput: the manager validates and saves structured scheduled prompts.
	CapabilityTaskInput = "task_input"
	// CapabilityAssistantSegments promises persistent logical routing and explicit switches.
	CapabilityAssistantSegments = "assistant_segments"
	// CapabilityDirectionSessions: the manager files sessions under a direction and reads one direction
	// (PUT /api/v1/assistant/sessions/{mid}/{sid}/direction, GET /api/v1/assistant/directions/{id}, and the tree's
	// additive sessions; #11391). The hub lists direction_sessions only while the default manager names it.
	CapabilityDirectionSessions = "direction_sessions"
	// CapabilityAssistantStartServiceTier: the manager forwards a new conversation's native service_tier to the node,
	// which acknowledges it before the first turn (#6240). The hub lists the feature only for the default manager and
	// refuses an explicit tier sent to a manager that does not name this capability.
	CapabilityAssistantStartServiceTier = "assistant_start_service_tier"
	// CapabilityHTTPBodyStream: the manager takes MethodHTTPStreamed, a forwarded request whose body comes on a relay
	// stream of its own instead of inside the bus message (#6574). The hub sends a large body that way only to a
	// manager that names it; any other gets MethodHTTP as before.
	CapabilityHTTPBodyStream = "http_body_stream"
	// CapabilityExtensionInfoAccessScoped: the manager includes its own trust verdict with recognized extension tokens.
	CapabilityExtensionInfoAccessScoped = "extension_info_access_scoped"
	// CapabilityMCPWait: the manager accepts enum-only mcp.wait updates through devices.notify for an explicitly
	// opted-in MCP call. The hub sends them only when this capability and the call's opaque id are both present.
	CapabilityMCPWait = "mcp_wait_v1"
	// CapabilityTodoView: the manager works out each to-do's view (#10226, TodoSummary): `view` on every row of
	// GET /api/v1/todos and on GET /api/v1/todos/{id}, and the list's `counts` (docs/protocol/protocol.md, the to-dos
	// rows). The hub lists the todo_view feature only while every manager whose to-dos it merges names it; until then
	// clients keep their own rules, and draw a row without `view` as before.
	CapabilityTodoView = "todo_view"
	// CapabilityTodoRevision: the manager answers GET /api/v1/todos/revision with a cheap validator for the full list's
	// to-do writes and stable view classes (#13804). The hub lists todo_revision only while every manager whose to-dos it
	// merges names it; older and mixed fleets keep reading the complete list after a push.
	CapabilityTodoRevision = "todo_revision"
	// TodoRevisionHeader carries the manager's or merged hub's lightweight to-do validator on GET /api/v1/todos and
	// GET /api/v1/todos/revision. It is separate from the full body's ETag.
	TodoRevisionHeader = "X-Persona-Todos-Revision"
	// CapabilityTodoPlaces: the manager answers GET /api/v1/todos/places, the To-dos sidebar's places with the list's
	// counts (#10226, TodoPlaces). The hub lists the todo_places feature only while every manager whose to-dos it
	// merges names it.
	CapabilityTodoPlaces = "todo_places"
	// CapabilityTodoAsks: the manager answers POST /api/v1/todos/{id}/asks/{ask}/answer, one route for every answer to a
	// to-do's own asks, named by the ask ids of its view (#10465, TodoAskAnswer). The hub lists the todo_asks feature
	// only while every manager whose to-dos it merges names it; until then clients answer through the accept,
	// decision-dismiss and retry routes. It gates the route alone: the to-do count unit rides on its own payload.
	CapabilityTodoAsks = "todo_asks"
	// CapabilityTodoTimeline: the manager answers GET /api/v1/todos/{id}/timeline, a to-do's conversation grouped into
	// rounds (#10465, TodoTimeline), and GET /api/v1/todos/{id} carries the rounds the paper shows, how many there are,
	// the to-do's results and what changed since the owner looked (TodoItemDetail). The hub lists the todo_timeline
	// feature only while every manager whose to-dos it merges names it; until then clients draw none of them.
	CapabilityTodoTimeline = "todo_timeline"
	// CapabilityTodoImages: the manager creates to-dos with images and serves their private bytes (#11084). The hub
	// lists todo_images only while every manager whose to-dos it merges names it, so an older manager never receives an
	// image create it would silently drop. Existing image metadata and byte reads remain available on capable managers.
	CapabilityTodoImages = "todo_images"
)

// ManagerStatus is EventManagerStatus's params: live runtime status a connected manager reports to the hub's roster.
type ManagerStatus struct {
	PendingRestart bool `json:"pending_restart"`
}

// BodyStream is where MethodHTTPStreamed's body comes from: the relay the hub holds with ID (devicewire.RelayPath
// ?id=, only the manager the request is for may take it), which carries exactly Size bytes as a body stream
// (bus.WriteBody) and then closes normally.
type BodyStream struct {
	ID   string `json:"id"`
	Size int64  `json:"size"`
}

// The methods and events, by direction. The assistant's handshake is manager.hello {id, host, machine_uid?, os, arch,
// deploy, version, pending_restart?, capabilities}; its answer is Hello (with the hub's name, role and managers besides).
const (
	// The assistant calls the hub (Hub).
	MethodHello  = "manager.hello"
	MethodDevice = "device.call"
	// MethodDeviceRetire forwards only session.stop_if_idle, with caller cancellation.
	MethodDeviceRetire = "device.retire"
	MethodApp          = "app.call"
	MethodNotify       = "notify.request"
	MethodSnapshot     = "manager.snapshot"
	EventBroadcast     = "ws.broadcast"   // one way
	EventManagerStatus = "manager.status" // one way: ManagerStatus (live roster status, e.g. pending_restart)

	// The hub calls the assistant (Handler).
	MethodHTTP = "http.request" // HTTPRequest → HTTPResponse
	// MethodHTTPStreamed is MethodHTTP for a large body: HTTPRequest without Body, with BodyStream saying where the
	// body comes from. The manager reads it whole before the route runs, so the bus carries no message of its size
	// and other calls are not held up behind it (#6574). A body that does not arrive whole is refused with
	// CodeBodyIncomplete before anything runs.
	MethodHTTPStreamed = "http.request_streamed"
	MethodTools        = "assistant.tools"
	MethodTool         = "assistant.tool"
	MethodContext      = "assistant.context"
	MethodAction       = "manager.action"

	// The hub tells the assistant (one way).
	EventUserSpoke           = "session.user_spoke"
	EventUserStopped         = "session.user_stopped" // the user stopped an assistant conversation's turn in an app
	EventTurnStarted         = "session.turn_started"
	EventTurnEnded           = "session.turn_ended"
	EventSessionsChanged     = "sessions.changed"
	EventInteractionResolved = "interaction.resolved"
	EventInteractionRaised   = "interaction.raised"
	EventUpdated             = "manager.updated" // Hello again: hub.json's assistant section or the machines changed
	EventResync              = "manager.resync"  // {}: report the whole snapshot again

	// EventStartEnded (StartEnded): how an app's start the node ran before answering went (POST /api/v1/sessions).
	EventStartEnded = "session.start_ended"

	// EventConversationSent (ConversationSent): how an app's message to one of the assistant's conversations went, as
	// the hub sent it (the north star's availability, docs/assistant/north-star.md). An older manager ignores it.
	EventConversationSent = "session.assistant_sent"
)

// DispatchGuardTools are the assistant tools an opt-in dispatch guard may hide and refuse for an explicitly named
// conversation. List filtering and call admission use the same names, so a hidden tool is not left callable.
var DispatchGuardTools = []string{"task_plan", "agent_start", "conversation_new", "conversation_send"}

// Error codes that cross the bus where a Go error would lose its kind (rpc.Error.Code).
const (
	CodeNoDevice         = "no_device"         // ErrNoDevice
	CodeMachineOffline   = "machine_offline"   // the machine's node is not connected (apierr.ErrOffline)
	CodeNodeClosed       = "node_closed"       // the link to the machine's node dropped during the call (rpc.ErrClosed)
	CodeStaleSnapshot    = "stale_snapshot"    // ErrStaleSnapshot
	CodeAssistantOffline = "assistant_offline" // the hub has no connection from the assistant
	// CodeFeishuAPI: Feishu answered the bot's call with an error code (Data: {"code": n}): a refusal or a passing
	// failure the assistant tells apart (the agent messages, internal/modules/lark/cot.go).
	CodeFeishuAPI = "feishu_api"
	// CodeFeishuSave: the image downloaded, but saving its bytes on the selected machine failed.
	CodeFeishuSave = "feishu_save"
	// CodeChatDenied: the hub refused to send into a chat or to reply to a message in one, because the bot may only use
	// the owner's private chat and the group chats the owner opted in. Nothing was sent.
	CodeChatDenied = "chat_denied"
	// CodeMeetingEnded: a message into a meeting's room that ended (docs/assistant/meeting-minutes.md "Messages from
	// elsewhere"): the hub answers 409 meeting_ended, and the apps write in the follow-up instead.
	CodeMeetingEnded = "meeting_ended"
	// CodeLauncherOtherKind: a launcher of another agent kind picked for a message in an existing assistant
	// conversation (Data: conversation_agent, launcher, launcher_agent, delivered "none"): the hub answers 409, nothing
	// was sent, and the conversation keeps its agent (docs/assistant/assistant.md, #4662).
	CodeLauncherOtherKind = "launcher_other_kind"
	// CodeConversationBusy: an assistant conversation is at work (a turn, background tasks) and its task-follow-up role
	// changed, so the message would need a new process with the new role while the old one still works. Nothing was
	// sent. The hub keeps an owner's message with an id (MethodKeepUndelivered); the manager sends it again once the
	// conversation is idle, resolving the role again, so it starts on the new role and the old one never gets it.
	CodeConversationBusy = "busy"
	// CodeTaskDirectoryMissing distinguishes a missing directory from permission and validation failures.
	CodeTaskDirectoryMissing = "task_directory_missing"
	// CodeSupervisionRuntime means the runtime cannot receive separate supervision instructions. Nothing was sent.
	CodeSupervisionRuntime = "assistant_supervision_runtime"
	// CodeSupervisionAccount means the engine supports the channel but this account has not enabled it.
	CodeSupervisionAccount = "assistant_supervision_account"
	// CodeBodyIncomplete: MethodHTTPStreamed's body did not arrive whole (the relay was not there, broke or carried
	// another size). Nothing ran: the hub sends the request again as MethodHTTP.
	CodeBodyIncomplete = "body_incomplete"
)

// HTTPRequest is http.request's params: an app's request on one of Routes, with an escaped URL path (header without
// the hub's credentials and hop-by-hop headers, the asking device's role in RoleHeader; body as base64).
// HandoffApprover is set by the hub on a card's answer from a device that may approve a hand-off card; Human on a new
// conversation a person's own app opens.
type HTTPRequest struct {
	// ModeActor is authenticated display provenance, never supplied by HTTP headers or a request body.
	ModeActor *devicewire.Actor `json:"mode_actor,omitempty"`
	// OwnerAnswer is the hub's verified full-device provenance for a relayed card answer.
	// It never comes from HTTP headers or the resolution body.
	OwnerAnswer     bool                       `json:"owner_answer,omitempty"`
	Caller          *devicewire.Caller         `json:"caller,omitempty"`
	ShareAuthority  *devicewire.ShareAuthority `json:"share_authority,omitempty"`
	HandoffApprover *HandoffApprover           `json:"handoff_approver,omitempty"`
	// ClarificationProof is the hub-issued opaque, one-time proof for this exact
	// authenticated question. Older managers ignore it and keep unmarked delivery.
	ClarificationProof string `json:"clarification_proof,omitempty"`
	// Human is the hub's word that a person's own app opens this conversation (POST /api/v1/assistant/conversations
	// with a full device's credential, decided as for SendParams.Human): its first message counts as theirs
	// (WithHumanStart). Set by the hub on that route only, never from the request; an older hub never sends it.
	Human bool `json:"human,omitempty"`
	// ReplyInput marks a legacy authenticated app start for missing-reply recovery, as in SendParams.
	ReplyInput bool                `json:"reply_input,omitempty"`
	Method     string              `json:"method"`
	Path       string              `json:"path"`
	Query      string              `json:"query,omitempty"`
	Header     map[string][]string `json:"header,omitempty"`
	// BodyStream is set on MethodHTTPStreamed only, in place of Body: where the body comes from.
	BodyStream *BodyStream `json:"body_stream,omitempty"`
	// Body stays the last field: hub/bus_forward.go httpParams appends it to the rest as written.
	Body []byte `json:"body,omitempty"`
}

// HTTPResponse is http.request's answer, written back to the app unchanged.
type HTTPResponse struct {
	ShareAuthority *devicewire.ShareAuthority `json:"share_authority,omitempty"`
	Status         int                        `json:"status"`
	Header         map[string][]string        `json:"header,omitempty"`
	Body           []byte                     `json:"body,omitempty"`
}

const (
	StarredImportPath            = "/api/v1/assistant/starred/import"
	StarredImportPreviewPath     = StarredImportPath + "/preview"
	StarredImportBodyMax         = 8 << 20
	RouteBackendWorkspacePrepare = "POST /api/v1/todos/{id}/backend-workspace/prepare"
)

// Routes are the routes the assistant answers (net/http patterns): the hub relays the apps' requests on them as
// http.request. The assistant registers exactly these (assistant.TestRoutesAreTheWireOnes).
var Routes = []string{
	"POST /api/v1/skills/import",
	"POST /api/v1/assistant/health/workouts",
	"GET /api/v1/sessions/archived",
	"POST /api/v1/sessions/{mid}/{sid}/archive",
	"POST /api/v1/sessions/{mid}/{sid}/unarchive",
	"POST /api/v1/sessions/{mid}/{sid}/autoname",
	"POST /api/v1/assistant/conversations/{id}/archive",
	"PUT /api/v1/assistant/conversations/{id}/tags",
	"DELETE /api/v1/assistant/conversations/{id}",
	"GET /api/v1/assistant/conversations/{id}/always",
	"GET /api/v1/assistant/standing-authorizations",
	"GET /api/v1/assistant/outbound-messages",
	"POST /api/v1/assistant/outbound-messages",
	"GET /api/v1/assistant/north-star",
	"DELETE /api/v1/assistant/standing-authorizations/{id}",
	"POST /api/v1/assistant/rule-suggestions/{id}/always",
	"POST /api/v1/assistant/rule-suggestions/{id}/not-now",
	"DELETE /api/v1/assistant/conversations/{id}/always/{hash}",
	"POST /api/v1/assistant/conversations/{id}/undelivered/{msg}/retry",
	"DELETE /api/v1/assistant/conversations/{id}/undelivered/{msg}",
	"POST /api/v1/assistant/conversations/{id}/unarchive",
	"POST /api/v1/assistant/conversations/{id}/stop",
	"GET /api/v1/assistant/reporters",
	"GET /api/v1/assistant/leaders",
	"POST /api/v1/assistant/directions",
	"POST /api/v1/assistant/directions/{id}/progress",
	"GET /api/v1/assistant/dispatch-policy",
	"PUT /api/v1/assistant/dispatch-policy",
	"GET /api/v1/assistant/dispatch-guard",
	"PUT /api/v1/assistant/dispatch-guard",
	"GET /api/v1/assistant/worker-presets",
	"GET /api/v1/assistant/start-defaults",
	"PUT /api/v1/assistant/start-defaults",
	"POST /api/v1/assistant/start-defaults/preview",
	"GET /api/v1/assistant/availability",
	"GET /api/v1/assistant/directions/tree",
	"GET /api/v1/assistant/directions/offers",
	"GET /api/v1/assistant/directions/{id}",
	"DELETE /api/v1/assistant/directions/offers/{id}",
	"POST /api/v1/assistant/directions/{id}/start",
	"POST /api/v1/assistant/directions/{id}/make",
	"POST /api/v1/assistant/directions/{id}/end",
	"POST /api/v1/assistant/route/agent",
	"POST /api/v1/assistant/agents/{mid}/{sid}/supervisor",
	"PUT /api/v1/assistant/directions/{id}/name",
	"PUT /api/v1/assistant/directions/{id}/notes",
	"PUT /api/v1/assistant/sessions/{mid}/{sid}/direction",
	"GET /api/v1/assistant/conversations/{id}/feed",
	"POST /api/v1/assistant/feed/{id}/dismiss",
	"POST /api/v1/assistant/feed/{id}/undo-dismiss",
	"POST /api/v1/assistant/conversations/{id}/route",
	"POST /api/v1/assistant/conversations/{id}/today",
	"GET /api/v1/assistant/tasks",
	"GET /api/v1/assistant/tasks/cron",
	"POST /api/v1/assistant/tasks",
	"POST /api/v1/assistant/tasks/input-context",
	"PUT /api/v1/assistant/tasks/{id}",
	"DELETE /api/v1/assistant/tasks/{id}",
	"POST /api/v1/assistant/tasks/{id}/run",
	"POST /api/v1/assistant/tasks/{id}/skip-next",
	"DELETE /api/v1/assistant/tasks/{id}/skip-next",
	"POST /api/v1/assistant/tasks/{id}/fresh",
	"DELETE /api/v1/assistant/tasks/{id}/start-grant",
	"GET /api/v1/assistant/places",
	"GET /api/v1/assistant/repositories",
	"GET /api/v1/assistant/repositories/{id}",
	"GET /api/v1/assistant/repositories/providers",
	"POST /api/v1/assistant/repositories/preview",
	"POST /api/v1/assistant/repositories",
	"PUT /api/v1/assistant/repositories/{id}",
	"DELETE /api/v1/assistant/repositories/{id}",
	"POST /api/v1/assistant/repositories/{id}/restore",
	"POST /api/v1/assistant/places",
	"PUT /api/v1/assistant/places/{id}",
	"DELETE /api/v1/assistant/places/{id}",
	"GET /api/v1/assistant/saved-prompts",
	"POST /api/v1/assistant/saved-prompts",
	"PUT /api/v1/assistant/saved-prompts/order",
	"PUT /api/v1/assistant/saved-prompts/{id}",
	"DELETE /api/v1/assistant/saved-prompts/{id}",
	"GET /api/v1/assistant/starred",
	"POST /api/v1/assistant/starred",
	"DELETE /api/v1/assistant/starred/{id}",
	"GET " + StarredImportPath,
	"POST " + StarredImportPreviewPath,
	"POST " + StarredImportPath,
	"GET /api/v1/assistant/runs",
	"GET /api/v1/assistant/routines",
	"PUT /api/v1/assistant/routines",
	"POST /api/v1/assistant/routines/{id}/run",
	"GET /api/v1/assistant/followups",
	"POST /api/v1/assistant/followups/{id}/run",
	"DELETE /api/v1/assistant/followups/{id}",
	"GET /api/v1/task-plans",
	"GET /api/v1/task-plans/{id}",
	"POST /api/v1/task-plans/{id}/start",
	"POST /api/v1/task-plans/{id}/workspace-preview",
	"POST /api/v1/task-plans/{id}/dismiss",
	"DELETE /api/v1/todos/{id}/terminal-cleanup",
	"GET /api/v1/task-trust",
	"DELETE /api/v1/task-trust",
	// The todos (the assistant's todos.go, docs/assistant/todos.md).
	"GET /api/v1/todos",
	"GET /api/v1/todos/places",              // the To-dos places and counts (todo_view_routes.go, #10226); the literal wins over {id}
	"GET /api/v1/todos/{id}",                // one to-do as the list has it, with its view
	"GET /api/v1/todos/{id}/images/{image}", // one attached picture's exact bytes (todo_images.go, #11084)
	"GET /api/v1/todos/{id}/repositories",
	"GET /api/v1/todos/{id}/backend-workspace",
	"GET /api/v1/todos/{id}/backend-workspace/operations/{operation_id}",
	RouteBackendWorkspacePrepare,
	"POST /api/v1/todos/{id}/backend-workspace/start",
	"POST /api/v1/todos/{id}/backend-workspace/cancel",
	"POST /api/v1/todos/{id}/backend-workspace/cleanup",
	"GET /api/v1/todos/{id}/workspace-rules",
	"PUT /api/v1/todos/{id}/workspace-rules",
	"POST /api/v1/todos/{id}/workspace-naming/preview",
	"POST /api/v1/todos/{id}/workspace-naming/plans",
	"GET /api/v1/todos/{id}/workspace-naming/plans/{operation_id}",
	"POST /api/v1/assistant/workspace-naming/preview",
	"PUT /api/v1/todos/{id}/repositories",
	"PUT /api/v1/todos/{id}/repositories/{repo_id}/links",
	"POST /api/v1/todos",
	"PATCH /api/v1/todos/{id}",
	"DELETE /api/v1/todos/{id}",
	"PUT /api/v1/todos/{id}/spaces/{mid}/{space...}",
	"DELETE /api/v1/todos/{id}/spaces/{mid}/{space...}",
	"PUT /api/v1/todos/{id}/agents/{mid}/{sid}",
	"DELETE /api/v1/todos/{id}/agents/{mid}/{sid}",
	"GET /api/v1/todos/{id}/decisions",
	"GET /api/v1/todos/{id}/handoffs",
	"POST /api/v1/todos/{id}/handoff",
	"POST /api/v1/decisions",
	"GET /api/v1/decisions/{id}",
	"POST /api/v1/decisions/{id}/withdraw",
	"GET /api/v1/todos/{id}/timeline", // its conversation's rounds (todo_timeline.go, #10465)
	"POST /api/v1/todos/{id}/decisions",
	"PATCH /api/v1/todos/{id}/decisions/{did}",
	"PUT /api/v1/todos/{id}/thread",
	"DELETE /api/v1/todos/{id}/thread",
	"PUT /api/v1/todos/{id}/seen", // Got it on a Just finished to-do, and its Undo (todo_seen.go, #5707)
	"DELETE /api/v1/todos/{id}/seen",
	"PUT /api/v1/todos/{id}/restored", // Restore on an inactive to-do, and Someday and back (todo_marks.go, #10226)
	"PUT /api/v1/todos/{id}/someday",
	"DELETE /api/v1/todos/{id}/someday",
	"POST /api/v1/todos/{id}/accept",
	"POST /api/v1/todos/{id}/decision-dismiss",
	"POST /api/v1/todos/{id}/decision-undo-dismiss",
	// Any answer to a to-do's own ask, by the id its view lists (todo_asks_routes.go, #10465).
	"POST /api/v1/todos/{id}/asks/{ask}/answer",
	"POST /api/v1/later/{id}/handled", // a question that can wait, marked handled (later.go)
	"POST /api/v1/todos/{id}/done-check",
	"POST /api/v1/todos/{id}/retry",
	"POST /api/v1/todos/{id}/start/answer", // the startup question a task's start stopped on, answered (start_answer.go, #10365)
	"POST /api/v1/todos/retitle",
	"POST /api/v1/todos/import/reminders",
	// Its settings and its main conversation (profile.go, start.go).
	"GET /api/v1/assistant",
	"PUT /api/v1/assistant",
	"POST /api/v1/assistant/warm",
	"GET /api/v1/assistant/readiness",
	"POST /api/v1/assistant/readiness/dismiss",
	"GET /api/v1/assistant/memory",
	"PUT /api/v1/assistant/memory/{name}",
	"DELETE /api/v1/assistant/memory/{name}",
	"GET /api/v1/assistant/capabilities",
	"GET /api/v1/assistant/skills",
	"POST /api/v1/assistant/conversations",
	"POST /api/v1/assistant/conversations/{id}/compact",
	"PUT /api/v1/assistant/conversations/{id}/model", // the model a conversation's next messages run on (model_pick.go, #5033)
	"DELETE /api/v1/assistant/conversations/{id}/model",
	"POST /api/v1/assistant/main/new",
	"POST /api/v1/assistant/main/clear",
	"POST /api/v1/assistant/origins/backfill",
	// The channels (channels.go there, docs/extensions/channels.md "API").
	"GET /api/v1/channels",
	"GET /api/v1/channels/{id}",
	"PUT /api/v1/channels/{id}",
	"DELETE /api/v1/channels/{id}/binding",
	"POST /api/v1/assistant/conversations/{id}/views/{key}/actions",
	"POST /api/v1/assistant/conversations/{id}/views/{key}/html-actions",
	"POST /api/v1/channels/{id}/test",
	// The Lark bot's workbench: companion bots and their notices (feishu_companions.go there, docs/extensions/bots.md
	// "Companion bots").
	"PUT /api/v1/assistant/feishu/companions",
	"GET /api/v1/assistant/feishu/notices",
	"POST /api/v1/assistant/feishu/notices/action",
	"POST /api/v1/assistant/feishu/workbench",
	// The knowledge base (the assistant's kb_routes.go, docs/assistant/knowledge.md "App API").
	"GET /api/v1/kb/{kind}",
	"GET /api/v1/kb/{kind}/{id}",
	"PUT /api/v1/kb/{kind}/{id}",
	"DELETE /api/v1/kb/projects/{id}",
	"GET /api/v1/kb-trash",
	"POST /api/v1/kb-trash/{id}/restore",
	"GET /api/v1/kb/moments/{id}",
	"GET /api/v1/kb/chats/{id}/context",
	"GET /api/v1/kb/chats/{id}/days/{day}",
	"GET /api/v1/kb-search",
	// A project's lessons from accepted tasks (task_lessons_routes.go there, #12295): the owner's apps only.
	"GET /api/v1/kb/projects/{id}/lessons",
	"PUT /api/v1/kb/projects/{id}/lessons/{lesson}",
	"DELETE /api/v1/kb/projects/{id}/lessons/{lesson}",
	// The assistant's chat search (search_routes.go there, docs/protocol/protocol.md "Assistant chat search", #6282).
	"GET /api/v1/assistant/search",
	"GET /api/v1/assistant/history",
	"PUT /api/v1/assistant/history",
	"GET /api/v1/assistant/segments",
	"PUT /api/v1/assistant/segments",
	"GET /api/v1/assistant/conversations/{id}/segments",
	"POST /api/v1/assistant/conversations/{id}/switch",
	"GET /api/v1/assistant/conversations/{id}/switch/{operation}",
	// A batch of those switches (conversations_switch.go there, docs/assistant/assistant.md, #14222).
	"POST /api/v1/assistant/conversation-switches",
	"GET /api/v1/assistant/conversation-switches/{batch}",
	// The selection: what the knowledge base brings in and keeps (kb_selection.go there).
	"GET /api/v1/kb/selection",
	"PUT /api/v1/kb/selection",
	"GET /api/v1/kb/selection/candidates",
	// The phone's recordings (kb_recordings.go there, docs/protocol/protocol.md "Recording sync").
	"PUT /api/v1/recordings/{id}",
	"DELETE /api/v1/recordings/{id}",
	"GET /api/v1/recordings",
	"POST /api/v1/recordings/{id}/minutes",
	"POST /api/v1/recordings/{id}/minutes/todos",
	// The owner's name from the web (#8877).
	"PUT /api/v1/recordings/{id}/title",
	// All-day recordings' appends (kb_day.go there, #4741).
	"POST /api/v1/recordings/{id}/segments",
	// Attending a meeting (meeting_live.go there, docs/assistant/meeting-minutes.md "Attending a meeting").
	"POST /api/v1/recordings/{id}/live",
	"GET /api/v1/recordings/{id}/live",
	"POST /api/v1/recordings/{id}/ask",
	// The meeting conversation (meeting_room.go there, docs/assistant/meeting-minutes.md "Meeting conversation (#3862)").
	"PUT /api/v1/recordings/{id}/private",
	"POST /api/v1/recordings/{id}/just-me",
	"POST /api/v1/recordings/{id}/followup",
	// A meeting handed to an Agent after it (meeting_handoff.go there, #8852).
	"POST /api/v1/recordings/{id}/handoff",
	// Its photo writes (the read-only GETs are the knowledge base's own, kb_recordings.go).
	"POST /api/v1/recordings/{id}/photos",
	"DELETE /api/v1/recordings/{id}/photos/{n}",
	// Hub transcription's gap uploads (meeting_transcribe.go there, "Phase 5: hub transcription"); its audio socket is
	// one of SocketRoutes.
	"PUT /api/v1/recordings/{id}/audio",
}

// SocketRoutes are the assistant's routes that upgrade to a WebSocket. The hub relays them over a relay stream
// (extwire.MethodAccept) to the default manager, as it relays the extensions' routes, never as http.request; the
// manager's extensions layer hands them to the assistant (extmgr.Options.AssistantSocket). The assistant serves exactly
// these on its socket handler (assistant.Service.ServeSocket).
const (
	A2ARPC  = "POST /api/v1/a2a/{mid}/{sid}/rpc"
	A2ACard = "GET /api/v1/a2a/{mid}/{sid}/.well-known/agent-card.json"
)

var SocketRoutes = []string{"GET /api/v1/recordings/{id}/audio", A2ARPC, A2ACard}
