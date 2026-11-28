// Package proto holds the wire types shared by hub, node and clients. See docs/protocol/protocol.md.
package proto

import "github.com/nwverse/persona/server/internal/nodeproto"

// Protocol is the app <-> hub protocol's major version: it goes up only for a change an older app cannot live with
// (features cover everything finer). MinAppBuild is the oldest app build (CFBundleVersion) this hub works with; 0:
// any. Both are in /api/v1/health and the WebSocket hello (docs/operations/hub-update.md "Compatibility").
const (
	Protocol    = 1
	MinAppBuild = 0
)

// Features are what this hub supports beyond the first protocol, listed in the WebSocket hello and /health for
// clients that need to know before they try (docs/protocol/protocol.md).
var Features = []string{
	nodeproto.PersonaContracts, // bidirectional pre-cutover runtime identities
	"look_uploads",             // POST /api/v1/looks/uploads, DELETE /api/v1/looks/uploads/{id}: avatars made from an uploaded image
	"locale",                   // Accept-Language, client_hello.locale and a device's locale pick the language of text for people (docs/protocol/i18n.md)
	"assistant_compact",        // POST /api/v1/assistant/conversations/{id}/compact, session.compaction
	"assistant_main_new",       // POST /api/v1/assistant/main/new: archive the main conversation, the next message starts one
	"assistant_main_clear",     // POST /api/v1/assistant/main/clear: clear the main conversation's context in place (closed, archived, nothing carried)
	"assistant_start_ops",      // POST /api/v1/assistant/conversations takes X-Persona-Op-Id: one start per op id (#1349)
	"assistant_readiness",      // GET /api/v1/assistant/readiness, …/readiness/dismiss, warm's readiness (docs/assistant/onboarding.md 5.1)
	"browser_qr_login",         // browser sign-in requests approved by an existing full device
	"browser_qr_match",         // browser sign-in: the approving device shows a number the browser types (POST …/browser-login/confirm)
	"android_update",           // authenticated Android release metadata and APK downloads
	"error_codes",              // stable HTTP error codes and optional delivery information
	"client_hello",             // client platform, version and capabilities
	"heartbeat",                // per-connection heartbeat negotiation
	"app_ping",                 // application ping/pong with a string id
	"ws_notify",                // notification alerts for capable connected clients
	"elicitation",              // MCP forms and URLs for opted-in clients
	"lock_test",                // scheduled device notification tests with durable receipts
	"ultracode",                // session-only Claude Code workflow switch, queued changes and resume-loss notices
	"model_configuration",      // unified per-purpose models and provider selection
	"typed_models",             // a model ID the launcher's list lacks: starts, POST …/modes, the Persona chat's pick; custom rows, DELETE …/models/custom (#1644)
	"assistant_model_pick",     // PUT / DELETE /api/v1/assistant/conversations/{id}/model, model_pick on assistant conversations, unavailable on …/launchers?purpose=assistant (#5033)
	"launcher_switch",          // POST …/modes {launcher, model?, effort?} switches a running Claude Code Agent to another launcher with its next message; Session.pending_switch (#5035)
	"engine_switch",            // POST …/modes with a launcher of the other engine: the next message moves the conversation between Claude Code and Codex with a handoff; pending_switch.agent, engine_history (#5054)
	"client_msg_id",            // session.send dedupes retries
	FeatureMessageSendState,    // informational receipt support; trust only authenticated hello.message_send_state == 1
	FeatureTodoSendState,       // informational: a to-do write's receipts (#10465); trust only authenticated hello.todo_send_state == 1
	"client_ops",               // durable operation receipts and read-only status queries
	FeatureAgentTerminalSend,   // paired terminal sends to exact Agent sessions
	"send_refusals",            // 409 dialog_open / input_draft, clear_draft
	"screen_dialog",            // interaction.dialog, Resolution.option, 409 prompt_changed
	"resumable_interactions",   // hook questions survive hub / node restarts
	"secret_prompts",           // interaction.secret
	"request_id",               // X-Request-Id
	"terminal_stream",          // WebSocket terminal_subscribe: the pane's live frames (docs/integrations/terminal.md)
	"terminal_control",         // terminal_subscribe with control: the phone drives the terminal at its size; terminal_input
	"terminal_scroll",          // terminal_input with scroll: the wheel over a terminal the app drives, to herdr (docs/integrations/terminal.md)
	"terminal_click",           // terminal_input with click: a tap on a terminal the app drives, a left click there (docs/integrations/terminal.md)
	"terminal_mouse",           // terminal_input with mouse: press / release / drag / move, a button and modifiers at a cell (docs/integrations/terminal.md "Mouse")
	"herdr_client",             // WebSocket herdr_open / herdr_input / herdr_resize / herdr_ack / herdr_close: herdr's own client in a PTY for the web, full devices only (docs/integrations/terminal.md "Full herdr UI")
	"herdr_focus",              // WebSocket herdr_focus (browser -> hub) and herdr_focus (hub -> browser): the full herdr view moves herdr's focus and follows it, full devices only (docs/integrations/terminal.md "Focus synchronization")
	"terminal_panes",           // GET/POST /api/v1/machines/{mid}/panes, terminal ids "pane:<id>": any pane as a terminal (docs/integrations/terminal.md "Standalone terminal")
	"file_download",            // GET /api/v1/machines/{mid}/file/download
	"file_stream",              // file?stream=1 (binary described, bytes by Range), file/download?inline=1, sniffed MIME
	"file_thumb",               // GET /api/v1/machines/{mid}/file/thumb (docs/protocol/protocol.md "File preview")
	"file_convert",             // GET /api/v1/machines/{mid}/file/convert: LibreOffice → PDF on the machine
	"git_blob",                 // GET /api/v1/machines/{mid}/git/blob: a file at a version, for comparing
	"git_write",                // the Git panel's writes and the branch's pull request under /api/v1/machines/{mid}/git/ (#6121)
	"git_review",               // GET /api/v1/machines/{mid}/git/review: every file a scope changed, with its hunks (#151)
	"session_review",           // POST/GET /api/v1/sessions/{mid}/{sid}/review: explicit independent read-only reviewer (#1808)
	"preview",                  // GET …/preview/servers, POST …/preview/start, WebSocket …/preview/connect: dev server previews in a headless browser on the computer (docs/web/preview.md)
	"file_upload",              // POST /api/v1/machines/{mid}/files (docs/protocol/protocol.md "Sending files")
	"machine_trust",            // machines' trust and error_code, POST /api/v1/machines/{id}/confirm|reject (docs/assistant/onboarding.md 5.3)
	"machines_add",             // POST /api/v1/auth/pair-codes with add / machine from a signed-in device: it adds a computer and pairs it (docs/assistant/onboarding.md 5.3)
	"todo_threads",             // a to-do's chat thread: PUT/DELETE /api/v1/todos/{id}/thread, and `thread` on the to-dos GET returns (docs/assistant/todos.md "Chat thread")
	"todo_seen",                // Got it on a Just finished to-do: PUT/DELETE /api/v1/todos/{id}/seen, and `seen_at` on the to-dos GET returns (docs/assistant/todo-status-sop.md "Seen")
	"task_retry_launcher",      // POST /api/v1/todos/{id}/retry takes {launcher, model}: a failed start again on another launcher of its computer; a failed plan task's retry_with (docs/assistant/todos.md "Retrying on another launcher")
	"tls",                      // optional TLS listener and SPKI pin (hub-changes.md 1.1)
	"endpoints",                // authenticated address discovery and capability-gated updates
	"plugins",                  // GET /api/v1/plugins and the relay under /api/v1/plugins/{id}/ (docs/extensions/extensions.md)
	"plugin_manager",           // a plugin's detail, form, settings, secrets and switch under /api/v1/plugins/{id}/-/ (docs/extensions/extensions.md)
	"plugin_ui_slots",          // plugins' UI slots (nav_page, overview_card, machine_section), UI version 2, plugin_ui events (docs/extensions/extensions.md "UI slots")
	"extension_operations",     // installs, upgrades and uninstalls as operations with steps: /api/v1/extensions/-/operations, extension_operation events, async: true (docs/protocol/protocol.md "Extension install, upgrade and uninstall progress")
	"extension_auth",           // extensions that sign in (manifest.auth): /api/v1/extensions/{id}/-/auth…, extension_auth events, the tools install step (docs/extensions/extensions.md "Sign-in (auth)")
	"plugin_auto_upgrade",      // market plugins upgrade themselves: /api/v1/plugins/-/upgrades, …/{id}/-/auto-upgrade, plugin_upgrade events (docs/extensions/extensions.md "Auto-upgrade")
	// The same under the new names (docs/extensions/extensions.md "Naming and compatibility", #1844): /api/v1/extensions/… answering with the
	// keys extensions / extension / extension_name, extension_ui and extension_upgrade events. The plugin ones above
	// stay for the apps out there (until at least 2027-05-05).
	"extensions",               // GET /api/v1/extensions and the relay under /api/v1/extensions/{id}/
	"extension_manager",        // an extension's detail, form, settings, secrets and switch under /api/v1/extensions/{id}/-/
	"extension_ui_slots",       // UI slots under /api/v1/extensions/-/ui, extension_ui events
	"extension_auto_upgrade",   // /api/v1/extensions/-/upgrades, …/{id}/-/auto-upgrade, extension_upgrade events
	"extension_market_spec",    // PUT /api/v1/extensions/-/markets/{id} {spec}: a market moves, keeping its id
	"extension_ui_v3",          // UI version 3 (markdown, table, empty, loading, search, filter, more, push / pop) and extension_ui {invalidate} (docs/extensions/extensions.md "UI version 3")
	"extension_widgets",        // widgets outside the app: GET /api/v1/extensions/-/widgets, POST …/{id}/-/widget/{ui}, PUT /api/v1/devices/{id}/widgets, extension_widget pushes (docs/extensions/extensions.md "System-level placements")
	"extension_shortcuts",      // extensions' actions in Shortcuts and Siri: GET /api/v1/extensions/-/shortcuts, POST …/{id}/-/shortcut/{sid} (docs/extensions/extensions.md "System-level placements")
	"extension_notify_actions", // notification buttons: POST /api/v1/notify actions / ui / state, aps.category and extension_notification (docs/extensions/extensions.md "System-level placements")
	"text_lookup",              // a dictionary for a tapped word: GET /api/v1/text-lookup, POST …/lookup, POST and DELETE …/words, text_lookup_changed (docs/extensions/extensions.md "Looking up a word")
	"desktops",                 // register_desktop, the desktops event, NotifyPrefs.mute_while_desktop ("Mute phone")
	"desktops_link",            // ?features=desktops_link (or client_hello): the desktops event lists Persona Link desktops too (docs/protocol/protocol.md "desktops")
	"sessions_delta",           // ?features=sessions_delta on the WebSocket: a snapshot, then sessions_delta (docs/protocol/protocol.md "Session deltas")
	"connectors",               // GET /api/v1/connectors (docs/plans/plan-connectors.md)
	"channels",                 // GET /api/v1/channels and each channel's settings, binding, test (docs/extensions/channels.md)
	"runtime_cli",              // phone_runtime_cli kept out of the phone's tools, POST /api/v1/phone/runtime-cli (docs/operations/runtime-cli.md)
	"conversation_origin",      // Session.origin on assistant conversations (docs/protocol/protocol.md "Origin of assistant conversations")
	"builtin_presets",          // the hub's own provider presets in GET /api/v1/launchers/presets; POST /api/v1/launchers with a preset takes an empty name, display_name and assign (docs/integrations/launchers.md "Built-in presets")
	"launcher_auto_name",       // POST /api/v1/launchers without a preset takes an empty name; GET /api/v1/machines/{mid}/launchers carries display_name (docs/integrations/launchers.md "Display names")
	"launcher_import_parse",    // POST /api/v1/launchers/import/parse reads a pasted configuration into provider drafts; GET / POST /api/v1/launchers/import take alias (#5193)
	"launcher_auth_auto",       // POST /api/v1/launchers/test and /discover send only the credential header auth names, take auth "auto" and answer the auth that worked (docs/integrations/launchers.md "Authentication method")
	"launcher_import_link",     // POST /api/v1/launchers/import/parse reads provider import links: cindy://provider/import and the web's #import= (#5420)
	"upload_dir",               // POST /api/v1/machines/{mid}/files?dest=dir&dir=&conflict=: into a folder of the home (docs/protocol/protocol.md "Changing files")
	"file_edit",                // POST /api/v1/machines/{mid}/files/{rename,move,trash,restore} (docs/protocol/protocol.md "Changing files")
	"file_info",                // GET /api/v1/machines/{mid}/stat?detail=1 and fs entries' link_target / link_broken (docs/protocol/protocol.md "File details and links")
	"file_mkdir",               // POST /api/v1/machines/{mid}/files/mkdir: a new folder (docs/protocol/protocol.md "Changing files")
	"file_write",               // POST /api/v1/machines/{mid}/files/write: a new text file, or one replaced when still the version opened (docs/protocol/protocol.md "Changing files")
	"file_zip",                 // POST /api/v1/machines/{mid}/files/zip: files and folders zipped into a folder or for a download (docs/protocol/protocol.md "Changing files")
	"file_copy",                // POST /api/v1/machines/{mid}/files/copy: files and folders copied into a folder, or duplicated beside themselves (docs/protocol/protocol.md "Changing files")
	"file_chmod",               // POST /api/v1/machines/{mid}/files/chmod: a file's or folder's permission bits (docs/protocol/protocol.md "Changing files")
	"pane_cwd",                 // POST /api/v1/machines/{mid}/panes takes cwd: a new terminal in a folder (docs/integrations/terminal.md "Standalone terminal")
	"pane_new_space",           // POST /api/v1/machines/{mid}/panes takes new_space: a terminal on a computer with no workspace yet, or 409 node_outdated (docs/integrations/terminal.md "Standalone terminal")
	"start_continue",           // POST /api/v1/sessions/{mid}/{sid}/start/continue: answer the startup question a start's new tab stopped on (shell_waiting), or 409 node_outdated (docs/protocol/protocol.md "Answering a start's question")
	"todo_start_answer",        // POST /api/v1/todos/{id}/start/answer: the same for a planned task's failed start (its error_args), then the task again in that tab, or 409 node_outdated (docs/assistant/todos.md "Answering a failed start's question")
	"launcher_scope",           // a Persona-managed provider's share, scope_machines, delivery, own_service and machine_keys (docs/integrations/launchers.md "Device scope and on-demand delivery")
	"file_inbox",               // POST /api/v1/machines/{mid}/files?dest=inbox: into the machine's receive folder, kept for good (docs/protocol/file-transfer.md)
	"upload_attachment",        // POST /api/v1/machines/{mid}/files?dest=attachment: a chat attachment kept for good in the node's uploads/<date>/ (docs/protocol/protocol.md "Sending files", #8361)
	"agent_message_hold",       // a message to an Agent whose computer is away is held and sent when it is back: 202 {"delivered":"later"}, undelivered on its row with computer, …/held/{msg} Retry / DELETE (#10661)
	"file_transfer",            // POST/GET/DELETE /api/v1/files/transfer: computer to computer, run by the manager, file_transfer events (docs/protocol/file-transfer.md)
	"task_push",                // Task.push on /api/v1/assistant/tasks: "Push when done", every run or only what a run sends (docs/assistant/assistant.md "Push when done")
	"task_inbox",               // Task.inbox on /api/v1/assistant/tasks: "When done, put it in the Inbox", off / only if worth a look / every time (docs/assistant/assistant.md "Put it in the Inbox when done")
	"task_execution",           // task execution overrides, working directories and execution history
	"task_cron",                // repeat "cron" with Task.cron on /api/v1/assistant/tasks, and GET /api/v1/assistant/tasks/cron (docs/assistant/assistant.md "Scheduled tasks")

	// A secret may hold a provider's op://… reference: PUT …/{id}/-/secrets/{name} {reference}; secrets[].reference and
	// secret_users in the details (docs/extensions/secret-providers.md).
	"extension_secret_references",
	// A model provider's key (and a computer's key for it) may be such a reference: api_key_reference /
	// machine_key_references on POST / PUT /api/v1/launchers, key_reference / machine_key_references in its view.
	"launcher_secret_references",

	"assistant_conversation_delete", // Delete ordinary and archived former main chats; retain task references and computer transcripts.
}

// CodeWorktreeUnsupported: worktree.prepare / worktree.create on an engine that has no checkouts (a runtime such as
// Paseo or Cindy, or a host without checkout support); the message starts with "unsupported".
const CodeWorktreeUnsupported = "worktree_unsupported"

// CodeWorktreeNoCommits: worktree.prepare / worktree.create on a repository nobody has committed to (HEAD names an
// unborn branch): there is nothing to check out, so the folder itself is the only workspace (#5548).
const CodeWorktreeNoCommits = "worktree_no_commits"

// CodeWorktreePathTaken: worktree.create / worktree.add's checkout path already holds something (data: path). The node
// never adopts or overwrites what is there; a task's own leftover checkout is reused through worktree.prepare's
// existing instead (#10365).
const CodeWorktreePathTaken = "worktree_path_taken"

// Codes of worktree.prepare's refusal to hand back the checkout of the branch it was asked about (data: branch, path,
// and owner for one in use): it has uncommitted changes, an Agent works in it, it is no longer that branch of this
// repository, or it could not be checked. The hub tells them from other failures (#10365).
const (
	CodeCheckoutDirty      = "checkout_dirty"
	CodeCheckoutInUse      = "checkout_in_use"
	CodeCheckoutChanged    = "checkout_changed"
	CodeCheckoutUnverified = "checkout_unverified"
)

// Codes of a failed credential.gh_token (hub → node, node/credential.go).
const (
	CodeNoGh        = "no_gh"
	CodeNotSignedIn = "not_signed_in"
)

// Codes of a failed connector.chats / connector.older (hub → node, modules/imessage/node.go): macOS does not let the
// node read the connector's data (Full Disk Access), or no conversation has that id.
const (
	CodeNoPermission = "no_permission"
	CodeNoChat       = "no_chat"
)
