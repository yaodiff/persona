package persona.core.hub

import persona.core.contracts.JsonCodec
import persona.core.contracts.api.NewPaneTab
import persona.core.contracts.api.FinishedPanes
import persona.core.contracts.api.PanesResponse
import persona.core.contracts.api.TerminalPanes
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response

/**
 * The terminal browser's calls on one hub (docs/integrations/terminal.md "Standalone terminal"), with that hub's plain machine ids.
 * [request] is the pairing's connection: it waits for the route, adds the token and turns refusals into [HubFailure].
 */
class HttpTerminalPanes internal constructor(
    override val available: Flow<Boolean>,
    private val request: suspend (String, String, RequestBody?, Long?) -> Response,
) : TerminalPanes {
    constructor(connection: OkHttpHubConnection) : this(connection.serverFeatures.map { TERMINAL_PANES in it }, { path, method, body, timeout ->
        connection.request(path, method, body, readTimeoutSeconds = timeout)
    })

    override suspend fun panes(machineId: String): PanesResponse =
        request(panesPath(machineId), "GET", null, null).use { JsonCodec.decodeFromString(body(it)) }

    override suspend fun newTab(machineId: String, workspaceId: String, kind: String?, cwd: String?): NewPaneTab {
        val payload = newTabBody(workspaceId, kind, cwd).toString().toRequestBody("application/json; charset=utf-8".toMediaType())
        // The hub gives the node 30 s to open the tab (without taking the computer's focus).
        return request(panesPath(machineId), "POST", payload, 35).use { JsonCodec.decodeFromString(body(it)) }
    }

    override suspend fun close(machineId: String, pane: String) {
        post(machineId, "close", buildJsonObject { put("pane", pane) }).use { body(it) }
    }

    override suspend fun closeFinished(machineId: String, preview: Boolean, panes: List<String>): FinishedPanes {
        require(preview || panes.isNotEmpty())
        val payload = buildJsonObject {
            if (preview) put("preview", true) else put("panes", panes.joinToString(","))
        }
        // The hub allows 60 s to recheck every candidate's foreground program, as on iOS.
        return post(machineId, "close_finished", payload, timeout = 70).use { JsonCodec.decodeFromString(body(it)) }
    }

    private suspend fun post(machineId: String, action: String, payload: JsonObject, timeout: Long = 35): Response =
        request("${panesPath(machineId)}/$action", "POST",
            payload.toString().toRequestBody("application/json; charset=utf-8".toMediaType()), timeout)

    private fun body(response: Response): String {
        if (!response.isSuccessful) throw HubFailure.from(response)
        return response.body?.string().orEmpty()
    }

    companion object {
        /** The hub's feature for `/machines/{mid}/panes` and the `pane:` terminal address. */
        const val TERMINAL_PANES = "terminal_panes"
        const val NEW_SPACE = "pane_new_space"

        /** An unknown feature list offers the action, as on the other clients. */
        fun offersNewSpace(features: Set<String>) = features.isEmpty() || NEW_SPACE in features
    }
}

/** `/api/v1/machines/{mid}/panes`, the machine id encoded as one segment. */
internal fun panesPath(machineId: String): String = HttpUrl.Builder().scheme("https").host("example.com")
    .addPathSegment("api").addPathSegment("v1").addPathSegment("machines").addPathSegment(machineId).addPathSegment("panes")
    .build().encodedPath

/** A workspace tab, or a home-directory shell when the host has no workspace yet. */
internal fun newTabBody(workspaceId: String, kind: String?, cwd: String?): JsonObject = buildJsonObject {
    require(workspaceId.isNotBlank() || !cwd.isNullOrBlank())
    if (workspaceId.isNotBlank()) put("workspace_id", workspaceId)
    if (!kind.isNullOrBlank()) put("kind", kind)
    if (!cwd.isNullOrBlank()) put("cwd", cwd)
    if (workspaceId.isBlank() && !cwd.isNullOrBlank()) put("new_space", true)
}
