package persona.core.hub

import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText
import persona.core.contracts.HeldAgentMessage
import persona.core.contracts.HubScope
import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.withContext
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.*
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.asRequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import java.io.File
import java.util.UUID
import java.util.concurrent.TimeUnit

/** Decorates the existing transport's readiness gate without changing the outbox payload format. */
class HttpChatComposerService internal constructor(
    private val hubScope: HubScope,
    private val transport: MessageTransport,
    private val request: suspend (String, RequestBody, Map<String, String>) -> Response,
    /** This hub's current features; empty before the first hello, when attachment retention is still worth trying. */
    private val features: () -> Set<String> = { emptySet() },
    /** Claim a refusal retry's operation before any request reaches the hub. */
    private val onRetryStart: suspend (String, String) -> Unit = { _, _ -> },
    /** A refusal retry's delivery, operation id, and actual transport result (null until a final response). */
    private val onRetryDelivery: suspend (String, ComposerDelivery, String, MessageTransportResult?) -> Unit = { _, _, _, _ -> },
    private val onPairingExpired: () -> Unit = {},
    private val onHeld: suspend (SessionKey, HeldAgentMessage) -> Unit = { _, _ -> },
) : ChatComposerService, MessageTransport by transport {
    constructor(
        hubScope: HubScope, transport: MessageTransport, connection: OkHttpHubConnection, token: String,
        onRetryDelivery: suspend (String, ComposerDelivery, String, MessageTransportResult?) -> Unit,
        onPairingExpired: () -> Unit,
        onRetryStart: suspend (String, String) -> Unit = { _, _ -> },
        isCurrent: () -> Boolean = { true },
        onHeld: suspend (SessionKey, HeldAgentMessage) -> Unit = { _, _ -> },
    ) :
        this(hubScope, transport, { path, body, headers ->
            if (!isCurrent()) throw CancellationException("Pairing changed")
            val route = connection.route.value ?: throw HubFailure(HubError.NoNetwork)
            val url = route.endpoint.baseUrl().hubUrl(path)
            // A file goes up in one request that the hub answers once the computer has all of it: on a slow link that
            // takes minutes (the old app allowed 10), not the 15 s every other call gets.
            val client = if (path.contains("/files?")) connection.http.client.newBuilder()
                .writeTimeout(10, TimeUnit.MINUTES).readTimeout(10, TimeUnit.MINUTES).build() else connection.http.client
            client.newCall(Request.Builder().url(url).header("Authorization", "Bearer $token")
                .apply { headers.forEach { (name, value) -> header(name, value) } }.post(body).build()).awaitResponse().also {
                    if (!isCurrent()) {
                        it.close()
                        throw CancellationException("Pairing changed")
                    }
                    if (connection.route.value !== route) {
                        it.close()
                        throw HubFailure(HubError.Transport, "Route changed before receipt")
                    }
                }
        }, { connection.serverFeatures.value }, onRetryStart, onRetryDelivery, onPairingExpired, onHeld)

    override val deliveries = MutableStateFlow<Map<String, ComposerDelivery>>(emptyMap())
    private val refused = mutableMapOf<String, MessageRequest>()
    private val sending = Mutex()

    override suspend fun send(request: MessageRequest): MessageTransportResult = sending.withLock {
        sendLocked(request)
    }

    private suspend fun sendLocked(message: MessageRequest): MessageTransportResult {
        require(message.scope == hubScope)
        if (!readyToSend.value) return MessageTransportResult.NotSent
        val body = buildJsonObject {
            put("text", persona.core.contracts.ProtocolCompatibility.messageForHub(message.text))
            put("client_msg_id", message.opId)
            message.clearDraft?.let { put("clear_draft", it) }
            message.launcher?.let { put("launcher", it) }
            message.model?.let { put("model", it) }
            if (message.applySwitch) put("apply_switch", true)
            message.via?.let { put("via", it) }
        }.toString().toRequestBody(jsonType)
        return try {
            request(api("sessions", message.session.machineId, message.session.sessionId, "messages"), body, message.headers).use { response ->
                if (response.isSuccessful) {
                    val fields = runCatching { JsonCodec.parseToJsonElement(response.peekBody(64 * 1024).string()).jsonObject }.getOrNull()
                    if (fields?.get("state")?.jsonPrimitive?.contentOrNull in setOf("pending", "unknown")) {
                        record(message, ComposerDelivery(DeliveryState.Unknown))
                        return@use MessageTransportResult.NoReceipt
                    }
                    if (fields?.get("delivered")?.jsonPrimitive?.contentOrNull == "later") {
                        val kept = (fields["undelivered"] as? JsonObject)?.let {
                            runCatching { JsonCodec.decodeFromJsonElement<HeldAgentMessage>(it) }.getOrNull()
                        }
                        if (kept == null || !kept.computer.isNullOrBlank()) {
                            refused.remove(message.clientMessageId)
                            record(message, ComposerDelivery(DeliveryState.Held))
                            if (kept?.id == message.opId) {
                                try { onHeld(message.session, kept) }
                                catch (cancelled: CancellationException) { throw cancelled }
                                catch (_: Exception) { /* A later session snapshot also carries the queue. */ }
                            }
                            return@use MessageTransportResult.Held
                        }
                        refused.remove(message.clientMessageId)
                        return@use MessageTransportResult.Kept
                    }
                    try { transport.onAccepted(message.session, fields) }
                    catch (cancelled: CancellationException) { throw cancelled }
                    catch (_: Exception) { /* Confirmed delivery survives a metadata presentation failure. */ }
                    refused.remove(message.clientMessageId)
                    record(message, ComposerDelivery(DeliveryState.Sent))
                    MessageTransportResult.Accepted
                } else {
                    val failure = HubFailure.from(response)
                    if (failure.kind == HubError.PairingExpired) onPairingExpired()
                    val fields = runCatching { JsonCodec.parseToJsonElement(response.peekBody(64 * 1024).string()).jsonObject }.getOrNull()
                    val refusal = if (failure.delivered == "none" && failure.code in setOf("input_draft", "dialog_open")) {
                        refused[message.clientMessageId] = message
                        ComposerRefusal(message.session, message.clientMessageId, failure.code!!,
                            fields?.get("draft")?.jsonPrimitive?.contentOrNull,
                            fields?.get("clearable")?.jsonPrimitive?.contentOrNull)
                    } else null
                    val state = when {
                        failure.delivered == "yes" -> DeliveryState.Sent
                        failure.code == "node_unreachable" && failure.delivered == "none" -> DeliveryState.Refused
                        failure.code == "assistant_offline" && (failure.delivered == null || failure.delivered == "none") -> DeliveryState.WaitingForNetwork
                        failure.delivered == "none" && failure.retryable -> DeliveryState.WaitingForNetwork
                        failure.delivered == "none" -> DeliveryState.Refused
                        else -> DeliveryState.Unknown
                    }
                    record(message, ComposerDelivery(state, refusal))
                    MessageTransportResult.HubError(failure.code, failure.delivered, failure.message, failure.retryable)
                }
            }
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (error: Exception) {
            val kind = HubFailure.from(error).kind
            if (kind in setOf(HubError.NoNetwork, HubError.AddressNotFound, HubError.ConnectionRefused, HubError.Unreachable)) {
                MessageTransportResult.NotSent
            } else {
                record(message, ComposerDelivery(DeliveryState.Unknown))
                MessageTransportResult.NoReceipt
            }
        }
    }

    override suspend fun retryRefused(clientMessageId: String, clearDraft: Boolean): ComposerDelivery = sending.withLock {
        val original = refused[clientMessageId] ?: throw ComposerOperationException(CoreText.of(Key.ComposerCheckTerminal))
        val refusal = deliveries.value[clientMessageId]?.refusal
            ?: throw ComposerOperationException(CoreText.of(Key.ComposerUnconfirmed))
        val clearable = if (clearDraft) refusal.clearable?.takeIf { it.isNotEmpty() }
            ?: throw ComposerOperationException(CoreText.of(Key.ComposerDraftInTerminal)) else null
        if (!readyToSend.value) throw ComposerOperationException(CoreText.of(Key.ComposerOffline))
        // The refused id is spent: the hub answers it again from its first try (the 409, or op_id_reused once
        // clear_draft is added), so the retry goes under a new one, as on iOS.
        val retry = original.copy(clearDraft = clearable, opId = UUID.randomUUID().toString())
        onRetryStart(clientMessageId, retry.opId)
        onRetryDelivery(clientMessageId, ComposerDelivery(DeliveryState.Sending), retry.opId, null)
        record(original, ComposerDelivery(DeliveryState.Sending))
        val result = try { sendLocked(retry) }
        catch (cancelled: CancellationException) {
            val unknown = ComposerDelivery(DeliveryState.Unknown)
            record(original, unknown)
            withContext(NonCancellable) { onRetryDelivery(clientMessageId, unknown, retry.opId, null) }
            throw cancelled
        }
        val outcome = when (result) {
            MessageTransportResult.Accepted -> ComposerDelivery(DeliveryState.Sent)
            MessageTransportResult.Kept, MessageTransportResult.Held -> ComposerDelivery(DeliveryState.Held)
            MessageTransportResult.NotSent -> ComposerDelivery(DeliveryState.Refused, refusal)
            is MessageTransportResult.HubError -> when {
                result.delivered == "yes" -> ComposerDelivery(DeliveryState.Sent)
                result.code in setOf("input_draft", "dialog_open") -> ComposerDelivery(DeliveryState.Refused, refusal)
                result.delivered == "none" || (result.code == "assistant_offline" && result.delivered == null) -> ComposerDelivery(DeliveryState.Refused)
                else -> ComposerDelivery(DeliveryState.Unknown)
            }
            else -> ComposerDelivery(DeliveryState.Unknown)
        }
        // Another automatic attempt may update this visible id while the retry is in flight. The actual retry
        // result is authoritative for this callback; do not read a stale Sending overlay back from the map.
        record(original, outcome)
        outcome.also { onRetryDelivery(clientMessageId, it, retry.opId, result) }
    }

    override suspend fun upload(session: SessionKey, name: String, file: File): String =
        uploadToComputer(session, name, file, null)

    override suspend fun upload(session: SessionKey, name: String, file: File, destination: String): String =
        uploadToComputer(session, name, file, destination)

    private suspend fun uploadToComputer(session: SessionKey, name: String, file: File, requestedDestination: String?): String {
        if (!readyToSend.value) throw ComposerOperationException(CoreText.of(Key.ComposerUploadOffline))
        if (file.length() > 200L * 1024 * 1024) throw ComposerOperationException(CoreText.of(Key.ComposerFileTooLarge, 200))
        // An explicit destination such as Health wins. Ordinary chat files use persistent storage where the hub supports
        // it, or before its features arrive; a known older hub keeps the previous temporary destination.
        val destination = if (requestedDestination == "attachment" && features().isNotEmpty() && "upload_attachment" !in features()) null
            else requestedDestination
        return try {
            uploadOnce(session, name, file, destination)
        } catch (_: UnknownUploadDestination) {
            // The reference clients retry only this file, once. A later upload makes its own feature-based choice.
            uploadOnce(session, name, file, null)
        }
    }

    private suspend fun uploadOnce(session: SessionKey, name: String, file: File, destination: String?): String {
        val url = path("machines", session.machineId, "files").addQueryParameter("name", name)
            .apply { destination?.let { addQueryParameter("dest", it) } }.build()
        return request(url.encodedPath + "?" + url.encodedQuery,
            file.asRequestBody("application/octet-stream".toMediaType()), emptyMap()).use { response ->
            if (!response.isSuccessful) {
                val failure = HubFailure.from(response)
                if (failure.kind == HubError.PairingExpired) onPairingExpired()
                if (destination == "attachment" && response.code == 400 &&
                    listOfNotNull(failure.message, failure.detail, failure.body).any { it.contains("unknown dest", ignoreCase = true) }) {
                    throw UnknownUploadDestination
                }
                throw ComposerOperationException(CoreText.of(when {
                    response.code == 404 -> Key.UploadHubUnsupported
                    failure.code == "node_outdated" -> Key.UploadNodeOutdated
                    response.code == 413 -> Key.UploadTooLarge
                    else -> Key.UploadFailed
                }))
            }
            JsonCodec.parseToJsonElement(response.body!!.string()).jsonObject["path"]!!.jsonPrimitive.content.also {
                require(it.startsWith('/') && !it.contains('\n') && !it.contains('\r'))
            }
        }
    }

    private object UnknownUploadDestination : Exception()

    override suspend fun interrupt(session: SessionKey) {
        if (!readyToSend.value) throw ComposerOperationException(CoreText.of(Key.ComposerInterruptOffline))
        request(api("sessions", session.machineId, session.sessionId, "interrupt"), "{}".toRequestBody(jsonType), emptyMap()).use {
            if (!it.isSuccessful) {
                if (HubFailure.from(it).kind == HubError.PairingExpired) onPairingExpired()
                throw ComposerOperationException(CoreText.of(Key.ComposerInterruptUnconfirmed))
            }
        }
    }

    private fun record(request: MessageRequest, delivery: ComposerDelivery) {
        deliveries.update { it + (request.clientMessageId to delivery) }
    }

    private fun path(vararg segments: String) = HttpUrl.Builder().scheme("https").host("example.com")
        .addPathSegments("api/v1").apply { segments.forEach(::addPathSegment) }
    private fun api(vararg segments: String) = path(*segments).build().encodedPath
    private val jsonType = "application/json; charset=utf-8".toMediaType()
}
