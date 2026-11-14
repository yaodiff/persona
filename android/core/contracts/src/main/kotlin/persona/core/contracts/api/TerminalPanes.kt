package persona.core.contracts.api

import persona.core.contracts.SessionKey
import kotlinx.coroutines.flow.Flow
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * The terminal browser (docs/integrations/terminal.md "Standalone terminal"): a machine's terminal hosts with their workspaces, tabs and panes,
 * agent or plain shell, and a new shell tab in a workspace. Hubs with the feature `terminal_panes` only; both calls
 * need a full device. Machine ids are the merged lists' (the app routes each call to the machine's hub group).
 */
interface TerminalPanes {
    /** Whether a paired hub lists panes (feature `terminal_panes`): the Terminal entry shows only then. */
    val available: Flow<Boolean>

    /** Whether the hub of [machineId] lists panes; machines of a hub without the feature are not asked. */
    fun lists(machineId: String): Boolean = true

    /** Whether the hub offers `new_space`; an unknown feature list offers it. */
    fun opensNewSpaces(machineId: String): Boolean = true

    /** `GET /api/v1/machines/{mid}/panes`. */
    suspend fun panes(machineId: String): PanesResponse

    /**
     * `POST /api/v1/machines/{mid}/panes {workspace_id, kind?}` opens a shell tab in that workspace. With no workspace
     * yet, `{kind, cwd: "~", new_space: true}` opens one in the home directory instead.
     */
    suspend fun newTab(machineId: String, workspaceId: String, kind: String? = null, cwd: String? = null): NewPaneTab

    /** User-confirmed close of one pane, including a plain terminal. */
    suspend fun close(machineId: String, pane: String): Unit = error("Closing panes is unavailable")

    /** Full devices only. Preview first, then send only the pane ids that the user confirmed. */
    suspend fun closeFinished(machineId: String, preview: Boolean = false, panes: List<String> = emptyList()): FinishedPanes =
        error("Closing finished terminals is unavailable")
}

/** What `GET /api/v1/machines/{mid}/panes` answers (server proto.PanesResponse); ids are the host's own. */
@Serializable
data class PanesResponse(val hosts: List<PaneHost> = emptyList())

/** One terminal host (herdr, herdr-core, Orca…) of the machine and what it can do with a pane. */
@Serializable
data class PaneHost(
    val kind: String = "",
    /** The host streams a pane's terminal; without it a pane is read once a second. */
    val stream: Boolean = false,
    /** A phone can drive a pane at its own size ("Take over"). */
    val control: Boolean = false,
    /** A shell tab can be opened in a workspace ("New tab"). */
    @SerialName("new_tab") val newTab: Boolean = false,
    val workspaces: List<PaneSpace> = emptyList(),
    val tabs: List<PaneTab> = emptyList(),
    val panes: List<PaneItem> = emptyList(),
)

@Serializable
data class PaneSpace(
    val id: String,
    val label: String = "",
    val number: Int? = null,
    val cwd: String? = null,
    val branch: String? = null,
)

@Serializable
data class PaneTab(
    val id: String,
    @SerialName("workspace_id") val workspaceId: String = "",
    val label: String = "",
    val number: Int? = null,
)

/**
 * One pane. [terminal] is what the terminal stream calls it (`pane:<pane>`); [sessionId] is the agent session running
 * in it, which the pane opens by when there is one (it shares the stream of that session's own terminal).
 */
@Serializable
data class PaneItem(
    val pane: String,
    val terminal: String = "",
    @SerialName("tab_id") val tabId: String = "",
    @SerialName("workspace_id") val workspaceId: String = "",
    /** The kind of agent running there ("claude", "codex"…); null for a plain shell. */
    val agent: String? = null,
    /** The host's status for the agent: idle / working / blocked / done. */
    val status: String? = null,
    val title: String? = null,
    val cwd: String? = null,
    @SerialName("session_id") val sessionId: String? = null,
    /** The foreground program of a plain terminal, without reading its screen. */
    val foreground: String? = null,
    /** The stopped Agent whose terminal this was; not an Agent currently running in it. */
    @SerialName("agent_session") val agentSession: String? = null,
)

@Serializable
data class FinishedPanes(val closed: List<FinishedPane> = emptyList(), val kept: List<FinishedPane> = emptyList())

@Serializable
data class FinishedPane(
    val pane: String,
    @SerialName("session_id") val sessionId: String = "",
    @SerialName("tab_id") val tabId: String = "",
    @SerialName("workspace_id") val workspaceId: String = "",
    val why: String = "",
)

/** What `POST …/panes` opened: the new tab's pane and its terminal address. */
@Serializable
data class NewPaneTab(
    val pane: String,
    val terminal: String,
    @SerialName("tab_id") val tabId: String? = null,
)

/**
 * A terminal opened by pane instead of by session (docs/protocol/protocol.md "Terminal stream"): a pane with no agent has no session id,
 * so `terminal_subscribe` / `terminal_input` / `terminal_unsubscribe` and the `/screen` read take `pane:<pane id>` in
 * the place of the session id. Such a key is a [SessionKey] whose session id is the address; nothing else of a
 * session (`/keys`, sending, closing) takes it.
 */
object TerminalAddress {
    const val PREFIX = "pane:"

    fun isPane(sessionId: String): Boolean = sessionId.startsWith(PREFIX)

    /** The key a pane's terminal goes by: [terminal] as `/panes` gives it (`pane:w1:p2`), or the bare pane id. */
    fun key(machineId: String, terminal: String): SessionKey =
        SessionKey(machineId, if (isPane(terminal)) terminal else PREFIX + terminal)
}

val SessionKey.isPaneAddress: Boolean get() = TerminalAddress.isPane(sessionId)
