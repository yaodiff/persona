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
	// CapabilityAssistantStartServiceTier: the manager forwards a new conversation's native service_tier to the node,
	// which acknowledges it before the first turn (#6240). The hub lists the feature only for the default manager and
	// refuses an explicit tier sent to a manager that does not name this capability.
	CapabilityAssistantStartServiceTier = "assistant_start_service_tier"
	// CapabilityHTTPBodyStream: the manager takes MethodHTTPStreamed, a forwarded request whose body comes on a relay
	// stream of its own instead of inside the bus message (#6574). The hub sends a large body that way only to a
	// manager that names it; any other gets MethodHTTP as before.
	CapabilityHTTPBodyStream = "http_body_stream"
	// CapabilityMCPWait: the manager accepts enum-only mcp.wait updates through devices.notify for an explicitly
	// opted-in MCP call. The hub sends them only when this capability and the call's opaque id are both present.
	CapabilityMCPWait = "mcp_wait_v1"
)

// BodyStream is where MethodHTTPStreamed's body comes from: the relay the hub holds with ID (devicewire.RelayPath
// ?id=, only the manager the request is for may take it), which carries exactly Size bytes as a body stream
// (bus.WriteBody) and then closes normally.
type BodyStream struct {
	ID   string `json:"id"`
	Size int64  `json:"size"`
}

// The methods and events, by direction. The assistant's handshake is manager.hello {id, host, os, arch, deploy,
// version, capabilities}; its answer is Hello (with the hub's name, role and managers besides).
const (
	// The assistant calls the hub (Hub).
	MethodHello    = "manager.hello"
	MethodDevice   = "device.call"
	MethodApp      = "app.call"
	MethodNotify   = "notify.request"
	MethodSnapshot = "manager.snapshot"
	EventBroadcast = "ws.broadcast" // one way

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

// Error codes that cross the bus where a Go error would lose its kind (rpc.Error.Code).
const (
	CodeNoDevice         = "no_device"         // ErrNoDevice
	CodeMachineOffline   = "machine_offline"   // the machine's node is not connected (apierr.ErrOffline)
	CodeNodeClosed       = "node_closed"       // the link to the machine's node dropped during the call (rpc.ErrClosed)
	CodeStaleSnapshot    = "stale_snapshot"    // ErrStaleSnapshot
	CodeAssistantOffline = "assistant_offline" // the hub has no connection from the assistant
	// CodeFeishuAPI: Feishu answered the bot's call with an error code (Data: {"code": n}): a refusal or a passing
	// failure the assistant tells apart (the agent messages, feishu_cot.go).
	CodeFeishuAPI = "feishu_api"
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
	// CodeBodyIncomplete: MethodHTTPStreamed's body did not arrive whole (the relay was not there, broke or carried
	// another size). Nothing ran: the hub sends the request again as MethodHTTP.
	CodeBodyIncomplete = "body_incomplete"
)

// HTTPRequest is http.request's params: an app's request on one of Routes, with an escaped URL path (header without
// the hub's credentials and hop-by-hop headers, the asking device's role in RoleHeader; body as base64).
// HandoffApprover is set by the hub on a card's answer from a device that may approve a hand-off card; Human on a new
// conversation a person's own app opens.
type HTTPRequest struct {
	Caller          *devicewire.Caller         `json:"caller,omitempty"`
	ShareAuthority  *devicewire.ShareAuthority `json:"share_authority,omitempty"`
	HandoffApprover *HandoffApprover           `json:"handoff_approver,omitempty"`
	// Human is the hub's word that a person's own app opens this conversation (POST /api/v1/assistant/conversations
	// with a full device's credential, decided as for SendParams.Human): its first message counts as theirs
	// (WithHumanStart). Set by the hub on that route only, never from the request; an older hub never sends it.
	Human  bool                `json:"human,omitempty"`
	Method string              `json:"method"`
	Path   string              `json:"path"`
	Query  string              `json:"query,omitempty"`
	Header map[string][]string `json:"header,omitempty"`
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
	"GET /api/v1/assistant/conversations/{id}/always",
	"GET /api/v1/assistant/standing-authorizations",
	"GET /api/v1/assistant/north-star",
	"DELETE /api/v1/assistant/standing-authorizations/{id}",
	"POST /api/v1/assistant/rule-suggestions/{id}/always",
	"POST /api/v1/assistant/rule-suggestions/{id}/not-now",
	"DELETE /api/v1/assistant/conversations/{id}/always/{hash}",
	"POST /api/v1/assistant/conversations/{id}/unarchive",
	"POST /api/v1/assistant/conversations/{id}/stop",
	"GET /api/v1/assistant/reporters",
	"GET /api/v1/assistant/leaders",
	"GET /api/v1/assistant/availability",
	"GET /api/v1/assistant/directions/tree",
	"GET /api/v1/assistant/directions/offers",
	"DELETE /api/v1/assistant/directions/offers/{id}",
	"POST /api/v1/assistant/directions/{id}/start",
	"POST /api/v1/assistant/directions/{id}/make",
	"POST /api/v1/assistant/directions/{id}/end",
	"POST /api/v1/assistant/route/agent",
	"POST /api/v1/assistant/agents/{mid}/{sid}/supervisor",
	"PUT /api/v1/assistant/directions/{id}/name",
	"GET /api/v1/assistant/conversations/{id}/feed",
	"POST /api/v1/assistant/feed/{id}/dismiss",
	"POST /api/v1/assistant/feed/{id}/undo-dismiss",
	"POST /api/v1/assistant/conversations/{id}/route",
	"POST /api/v1/assistant/conversations/{id}/today",
	"GET /api/v1/assistant/tasks",
	"GET /api/v1/assistant/tasks/cron",
	"POST /api/v1/assistant/tasks",
	"PUT /api/v1/assistant/tasks/{id}",
	"DELETE /api/v1/assistant/tasks/{id}",
	"POST /api/v1/assistant/tasks/{id}/run",
	"POST /api/v1/assistant/tasks/{id}/fresh",
	"GET /api/v1/assistant/places",
	"GET /api/v1/assistant/repositories",
	"GET /api/v1/assistant/repositories/{id}",
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
	"GET /api/v1/assistant/runs",
	"GET /api/v1/assistant/routines",
	"PUT /api/v1/assistant/routines",
	"POST /api/v1/assistant/routines/{id}/run",
	"GET /api/v1/assistant/followups",
	"POST /api/v1/assistant/followups/{id}/run",
	"DELETE /api/v1/assistant/followups/{id}",
	"GET /api/v1/task-plans/{id}",
	"POST /api/v1/task-plans/{id}/start",
	"POST /api/v1/task-plans/{id}/workspace-preview",
	"POST /api/v1/task-plans/{id}/dismiss",
	"GET /api/v1/task-trust",
	"DELETE /api/v1/task-trust",
	// The todos (the assistant's todos.go, docs/assistant/todos.md).
	"GET /api/v1/todos",
	"POST /api/v1/todos",
	"PATCH /api/v1/todos/{id}",
	"DELETE /api/v1/todos/{id}",
	"PUT /api/v1/todos/{id}/spaces/{mid}/{space...}",
	"DELETE /api/v1/todos/{id}/spaces/{mid}/{space...}",
	"PUT /api/v1/todos/{id}/agents/{mid}/{sid}",
	"DELETE /api/v1/todos/{id}/agents/{mid}/{sid}",
	"GET /api/v1/todos/{id}/decisions",
	"POST /api/v1/todos/{id}/decisions",
	"PATCH /api/v1/todos/{id}/decisions/{did}",
	"PUT /api/v1/todos/{id}/thread",
	"DELETE /api/v1/todos/{id}/thread",
	"PUT /api/v1/todos/{id}/seen", // Got it on a Just finished to-do, and its Undo (todo_seen.go, #5707)
	"DELETE /api/v1/todos/{id}/seen",
	"POST /api/v1/todos/{id}/accept",
	"POST /api/v1/todos/{id}/decision-dismiss",
	"POST /api/v1/todos/{id}/decision-undo-dismiss",
	"POST /api/v1/later/{id}/handled", // a question that can wait, marked handled (later.go)
	"POST /api/v1/todos/{id}/done-check",
	"POST /api/v1/todos/{id}/retry",
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
	// The assistant's chat search (search_routes.go there, docs/protocol/protocol.md "Assistant chat search", #6282).
	"GET /api/v1/assistant/search",
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
	// Its photos (meeting_photos.go there, "Phase 3: photos").
	"POST /api/v1/recordings/{id}/photos",
	"GET /api/v1/recordings/{id}/photos",
	"GET /api/v1/recordings/{id}/photos/{n}",
	"DELETE /api/v1/recordings/{id}/photos/{n}",
	// Hub transcription's gap uploads (meeting_transcribe.go there, "Phase 5: hub transcription"); its audio socket is
	// one of SocketRoutes.
	"PUT /api/v1/recordings/{id}/audio",
}

// SocketRoutes are the assistant's routes that upgrade to a WebSocket. The hub relays them over a relay stream
// (extwire.MethodAccept) to the default manager, as it relays the extensions' routes, never as http.request; the
// manager's extensions layer hands them to the assistant (extmgr.Options.AssistantSocket). The assistant serves exactly
// these on its socket handler (assistant.Service.ServeSocket).
var SocketRoutes = []string{"GET /api/v1/recordings/{id}/audio"}
