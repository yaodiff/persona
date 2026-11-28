package persona.core.hub

import persona.core.contracts.HubScope
import persona.core.contracts.JsonCodec
import persona.core.contracts.MessageEcho
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ItemsQuery
import persona.core.contracts.api.MessageEvidence
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.MessageRequest
import persona.core.contracts.api.MessageTransport
import persona.core.contracts.api.MessageTransportResult
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.stateIn
import kotlinx.serialization.json.*
import java.util.Base64
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody.Companion.toRequestBody

/** A transport belongs to exactly one pairing generation and its pinned connection. */
class HttpMessageTransport(
    private val hubScope: HubScope,
    private val connection: OkHttpHubConnection,
    scope: CoroutineScope,
    /** A waiting Codex pane's first message returns the thread it started, before the next list push. */
    private val onSessionStarted: suspend (SessionKey, Session) -> Unit = { _, _ -> },
    private val onPairingExpired: () -> Unit = {},
) : MessageTransport {
    /** Credentials always come from the live connection, including in compatibility tests. */
    internal constructor(
        hubScope: HubScope, connection: OkHttpHubConnection, @Suppress("UNUSED_PARAMETER") token: String,
        scope: CoroutineScope, onPairingExpired: () -> Unit = {},
    ) : this(hubScope, connection, scope, onPairingExpired = onPairingExpired)

    private class ReadyRoute(val route: HubRoute?)
    private val caughtUp = MutableStateFlow<ReadyRoute?>(null)
    override val readyToSend = combine(connection.state, connection.route, caughtUp) { state, route, ready ->
        state == ConnectionState.Online && route != null && route === ready?.route
    }
        .stateIn(scope, SharingStarted.Eagerly, false)
    // The authenticated hello is stored before catch-up. Queued event delivery
    // is neither a readiness barrier nor replayed to a later transport.
    override val supportsClientOps: Boolean
        get() = connection.lastHello?.features?.contains("client_ops") == true

    /** Call at the end of the connection's onReady hook, after authoritative catch-up. */
    fun onReady() { caughtUp.value = ReadyRoute(connection.route.value) }

    override suspend fun reconcile(request: MessageRequest, createdAt: Long, allowTextMatch: Boolean): MessageEvidence? {
        require(request.scope == hubScope) { "Message belongs to another pairing" }
        if (supportsClientOps) {
            val path = HttpUrl.Builder().scheme("https").host("example.com")
                .addPathSegments("api/v1/ops").addPathSegment(request.opId).build().encodedPath
            val receipt = try {
                connection.request(path).use { JsonCodec.parseToJsonElement(it.body!!.string()).jsonObject }
            } catch (failure: HubFailure) {
                if (failure.status == 404) null else throw failure
            }
            val opState = receipt?.get("state")?.jsonPrimitive?.contentOrNull
            if (opState in setOf("done", "failed", "unknown")) {
                val saved = receipt?.get("result") as? JsonObject
                val status = saved?.get("status")?.jsonPrimitive?.intOrNull
                val body = saved?.get("body")?.jsonPrimitive?.contentOrNull
                if (opState == "unknown") {
                    val result = if (status != null && body != null) {
                        decodeResult(request.session, status, String(Base64.getDecoder().decode(body), Charsets.UTF_8))
                    } else {
                        MessageTransportResult.NoReceipt
                    }
                    // When the saved error proves nothing reached the node, return it directly without fuzzy transcript matching.
                    if (result is MessageTransportResult.HubError &&
                        (result.delivered == "none" || (result.code == "assistant_offline" && result.delivered == null))) {
                        return MessageEvidence.Receipt(result)
                    }
                    // Otherwise check the transcript first so messages that already landed converge to Shown;
                    // a recovered row with result=null still reports Receipt(NoReceipt) rather than null.
                    val shown = try {
                        transcriptEvidence(request, createdAt, allowTextMatch)
                    } catch (cancelled: CancellationException) {
                        throw cancelled
                    } catch (_: Exception) {
                        null
                    }
                    return shown ?: MessageEvidence.Receipt(result)
                }
                if (status != null && body != null) {
                    val result = decodeResult(request.session, status, String(Base64.getDecoder().decode(body), Charsets.UTF_8))
                    if (result == MessageTransportResult.Held) {
                        // A failed follow-up read does not undo the manager's receipt. Only an exact
                        // transcript echo can promote it to Agent delivery.
                        val shown = try {
                            transcriptEvidence(request, createdAt, false)
                        } catch (cancelled: CancellationException) {
                            throw cancelled
                        } catch (_: Exception) {
                            null
                        }
                        return shown ?: MessageEvidence.Receipt(result)
                    }
                    if (result != MessageTransportResult.Accepted) return MessageEvidence.Receipt(result)
                    // A successful receipt still needs the transcript to replace the local bubble: an item tagged with
                    // this id, or (older hubs; Claude Code keeps its own id for the message) one with its words.
                    val shown = transcriptEvidence(request, createdAt, allowTextMatch)
                    return shown ?: MessageEvidence.Receipt(result)
                }
            }
        }
        return transcriptEvidence(request, createdAt, allowTextMatch && !supportsClientOps)
    }

    private suspend fun transcriptEvidence(request: MessageRequest, createdAt: Long, allowTextMatch: Boolean): MessageEvidence? {
        val items = HubChatTransport(connection).items(request.session, ItemsQuery()).items
        val shown = items.any { item ->
            MessageEcho.carries(item, request.clientMessageId) || MessageEcho.carries(item, request.opId) ||
                allowTextMatch && MessageEcho.arrived(request.text, createdAt, item)
        }
        return MessageEvidence.Shown.takeIf { shown }
    }

    private suspend fun decodeResult(target: SessionKey, status: Int, body: String): MessageTransportResult {
        val reply = runCatching { JsonCodec.parseToJsonElement(body).jsonObject }.getOrNull()
        if (reply?.get("state")?.jsonPrimitive?.contentOrNull in setOf("pending", "unknown"))
            return MessageTransportResult.NoReceipt
        if (status in 200..299) {
            if (reply?.get("delivered")?.jsonPrimitive?.contentOrNull == "later") {
                val kept = reply?.get("undelivered") as? JsonObject
                // An offline Agent names its computer. Assistant conversations do not; a bare replay stays Held as upstream.
                return if (kept == null || kept["computer"] != null) MessageTransportResult.Held
                    else MessageTransportResult.Kept
            }
            onAccepted(target, reply)
            return MessageTransportResult.Accepted
        }
        return MessageTransportResult.HubError(
            reply?.get("code")?.jsonPrimitive?.contentOrNull,
            reply?.get("delivered")?.jsonPrimitive?.contentOrNull,
            reply?.get("error")?.jsonPrimitive?.contentOrNull,
            reply?.get("retryable")?.jsonPrimitive?.booleanOrNull ?: false)
    }

    override suspend fun onAccepted(session: SessionKey, reply: JsonObject?) {
        val started = (reply?.get("session") as? JsonObject)?.let {
            runCatching { JsonCodec.decodeFromJsonElement<Session>(it) }.getOrNull()
        }?.takeIf { it.sessionId.isNotBlank() && it.sessionId != session.sessionId } ?: return
        // Node replies have no machine id; the request owns it (also when the hub is one of several).
        try { onSessionStarted(session, started.copy(machineId = session.machineId)) }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { /* A presentation failure cannot change confirmed delivery. */ }
    }

    override suspend fun send(request: MessageRequest): MessageTransportResult {
        require(request.scope == hubScope) { "Message belongs to another pairing" }
        if (!readyToSend.value) return MessageTransportResult.NotSent
        val route = connection.route.value ?: return MessageTransportResult.NotSent
        if (route !== caughtUp.value?.route) return MessageTransportResult.NotSent
        val base = route.endpoint.baseUrl()
        val url = base.hubUrl("/api/v1/sessions").newBuilder()
            .addPathSegment(request.session.machineId)
            .addPathSegment(request.session.sessionId)
            .addPathSegment("messages").build()
        val body = buildJsonObject {
            put("text", request.text)
            put("client_msg_id", request.opId)
            request.clearDraft?.let { put("clear_draft", it) }
            request.todoId?.let { put("todo_id", it) }
            request.launcher?.let { put("launcher", it) }
            request.model?.let { put("model", it) }
            if (request.applySwitch) put("apply_switch", true)
            request.via?.let { put("via", it) }
        }.toString().toRequestBody("application/json; charset=utf-8".toMediaType())
        return try {
            // Non-2xx replies throw HubFailure (handled below); a 2xx body may still be a pending receipt.
            connection.request(requireNotNull(base.hubPath(url)), "POST", body, request.headers, expectedRoute = route).use { response ->
                decodeResult(request.session, response.code, response.body?.string().orEmpty())
            }
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            val failure = HubFailure.from(error)
            if (failure.kind == HubError.PairingExpired) onPairingExpired()
            if (failure.status != null || failure.kind == HubError.PairingExpired) {
                return MessageTransportResult.HubError(failure.code, failure.delivered, failure.message, failure.retryable)
            }
            when (failure.kind) {
                HubError.AddressNotFound, HubError.ConnectionRefused, HubError.Unreachable,
                HubError.NoNetwork -> MessageTransportResult.NotSent
                HubError.CertificateMismatch, HubError.HubMismatch, HubError.TlsRequired ->
                    MessageTransportResult.HubError(failure.code, "none", failure.message, false)
                else -> MessageTransportResult.NoReceipt
            }
        }
    }
}
