package persona.feature.settings

import androidx.lifecycle.ViewModel
import java.util.UUID
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.*
import kotlinx.serialization.json.*
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.contracts.generated.*
import persona.core.contracts.stream.*

/** One entry-owned, authenticated socket adapter. No separate socket or extension code executes here. */
class ExtensionUIStreamController(
    val binding: ExtensionStreamBinding,
    extension: String,
    contribution: String,
    initialState: String = "",
    // Native streams draw maps. Scene and graph components use the hub's compatible rows.
    context: ExtensionUIContext = ExtensionUIContext(uiVersion = persona.core.hub.HttpExtensionManagement.UI_VERSION, platform = "android",
        locale = ClientLanguage.localeTag(), tz = java.util.TimeZone.getDefault().id, unsupported = listOf("scene", "graph"),
        stream = ExtensionUIStreamCapability(protocol = 1, components = NativeExtensionStreamAdapter.Components.toList())),
    dispatcher: CoroutineDispatcher = Dispatchers.Main.immediate,
    private val responseDeadlineMillis: Long = 30000,
    private val deadlineDispatcher: CoroutineDispatcher = Dispatchers.IO,
) : ViewModel() {
    // This bounded ownership collector is independent of viewModelScope and the cancelled renderer.
    private val job = SupervisorJob()
    private val scope = CoroutineScope(job + dispatcher)
    private data class Sent(val request: ExtensionUIStreamRequest, val tree: NativeStreamTree?, val hello: Hello?)
    private val sent = LinkedHashMap<String, Sent>()
    private val waiting = LinkedHashMap<String, ExtensionUIStreamReceipt>()
    private val settled = linkedSetOf<String>()
    private val presented = linkedSetOf<Triple<String, String, String>>()
    private var allocatedHello: Hello? = null
    private var closing = false
    private var readFailures = 0
    private var permissionRequest: String? = null
    private data class Cleanup(val snapshot: ExtensionUIStreamSnapshot, var hello: Hello?, var requestId: String? = null, var resuming: Boolean = false)
    private val cleanup = linkedMapOf<String, Cleanup>()
    private val effects = Channel<NativeStreamPresentation>(64)
    internal val presentations = effects.receiveAsFlow()
    private val identity = NativeStreamIdentity(binding.owner, binding.group, extension, contribution, UUID.randomUUID().toString())
    val adapter: NativeExtensionStreamAdapter = NativeExtensionStreamAdapter(NativeStreamSession(identity, context, NativeStreamTransport { request ->
        if (closing) return@NativeStreamTransport // Final removal uses the separate cleanup ownership barrier.
        val raw = envelope(request)
        val requestId = raw.getValue("request").jsonObject.getValue("request_id").jsonPrimitive.content
        evictCompleted()
        if (sent.size >= 256 && request !is ExtensionUIStreamCloseRequest) {
            scope.launch { close() }
            throw NativeStreamNotSent()
        }
        sent[requestId] = Sent(request, adapter.session.state.value.tree, binding.connection.lastHello)
        if (!binding.connection.send(raw)) { sent.remove(requestId); throw NativeStreamNotSent() }
        if (request is ExtensionUIStreamOpenRequest || request is ExtensionUIStreamEventRequest ||
            request is ExtensionUIStreamResumeRequest || request is ExtensionUIStreamSnapshotRequest) scope.launch {
            withContext(deadlineDispatcher) { delay(responseDeadlineMillis) }
            if (closing || sent[requestId]?.request !== request || requestId in settled) return@launch
            when (request) {
                is ExtensionUIStreamOpenRequest -> close() // Keep the pending allocation response collector alive.
                is ExtensionUIStreamEventRequest -> {
                    settled.add(requestId)
                    adapter.session.requestSnapshot() // Unknown delivery is recovered read-only, never replayed.
                }
                else -> failedRead(requestId)
            }
        }
    }, initialState), shown = true)

    init {
        require(binding.manager?.isNotBlank() == true)
        require("extension_ui_stream_v1" in binding.connection.lastHello?.features.orEmpty())
        adapter.decidePermission = { permission, allow ->
            val record = permissionRequest?.let { sent[it] }
            if (!closing && adapter.shown && adapter.permission === permission && !adapter.permissionBusy && record != null && permissionMatches(permission, record)) {
                adapter.permissionBusy = true
                scope.launch {
                    try {
                        val resolver = checkNotNull(binding.approvals)
                        withTimeout(responseDeadlineMillis) { resolver.resolveOccurrence(permission, Resolution(allow = allow)) }
                        if (adapter.permission === permission) adapter.permission = null
                    } catch (cancelled: CancellationException) { throw cancelled }
                    catch (_: Exception) { adapter.local.rejected = true }
                    finally { adapter.permissionBusy = false }
                }
            }
        }
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            binding.connection.events.collect { event ->
                when (event) {
                    is ExtensionUiStreamEvent -> receive(event)
                    is InteractionEvent -> receivePermission(event.interaction)
                    is InteractionResolvedEvent -> if (adapter.permission?.id == event.id) adapter.permission = null
                    is Hello -> {
                        if (closing) {
                            cleanup.values.forEach { it.requestId = null }
                            retryOpenCleanup()
                            advanceCleanup()
                        } else {
                            adapter.session.connection(false)
                            if ("extension_ui_stream_v1" !in event.features) close()
                            else adapter.session.connection(binding.connection.state.value == ConnectionState.Online)
                        }
                    }
                    else -> Unit
                }
            }
        }
        scope.launch { binding.connection.state.collect {
            if (closing) {
                if (it != ConnectionState.Online) cleanup.values.forEach { entry -> entry.requestId = null }
                else advanceCleanup()
            } else adapter.session.connection(it == ConnectionState.Online && "extension_ui_stream_v1" in binding.connection.lastHello?.features.orEmpty())
        } }
        scope.launch { adapter.session.state.collect {
            if (it.phase != NativeStreamPhase.Ready) { adapter.permission = null; permissionRequest = null }
            else {
                permissionRequest?.let { id -> if (sent[id]?.let { record -> adapter.permission?.let { permission -> permissionMatches(permission, record) } } != true) adapter.permission = null }
                drain()
            }
        } }
    }

    private fun permissionMatches(permission: Interaction, record: Sent): Boolean {
        val request = record.request as? ExtensionUIStreamEventRequest ?: return false
        val tree = record.tree ?: return false
        val current = adapter.session.state.value
        if (permission.manager != binding.manager || current.phase != NativeStreamPhase.Ready || current.tree !== tree || record.hello !== binding.connection.lastHello ||
            request.interaction.session != permission.sessionId || request.interaction.epoch != tree.snapshot.epoch) return false
        val expires = permission.expiresAt?.let { runCatching { java.time.Instant.parse(it) }.getOrNull() } ?: return false
        if (!expires.isAfter(java.time.Instant.now())) return false
        val input = permission.toolInput ?: return false
        val action = runCatching { JsonCodec.decodeFromJsonElement(persona.core.contracts.api.ExtensionViewAction.serializer(), input) }.getOrNull() ?: return false
        val node = tree.nodes[request.interaction.key] ?: return false
        fun saved(value: JsonElement): Boolean = when (value) {
            is JsonObject -> if (value !== node && value["key"] != null) false else {
                val candidate = if (value["id"] == JsonPrimitive(request.interaction.action) && "confirm" in value)
                    runCatching { JsonCodec.decodeFromJsonElement(persona.core.contracts.api.ExtensionViewAction.serializer(), value) }.getOrNull() else null
                candidate != null && candidate == action && candidate.confirm == permission.summary || value.values.any(::saved)
            }
            is JsonArray -> value.any(::saved)
            else -> false
        }
        return action.id == request.interaction.action && !action.confirm.isNullOrBlank() && saved(node)
    }

    private suspend fun receivePermission(permission: Interaction) {
        if (closing || !adapter.shown || permission.kind != "permission" || permission.toolName != "extension.ui") return
        val pending = sent.entries.firstOrNull { (id, record) -> id !in settled && permissionMatches(permission, record) } ?: return
        if (binding.approvals == null) { close(); return } // Never offer an approval that this owner cannot resolve.
        permissionRequest = pending.key
        adapter.permission = permission
    }

    private fun envelope(request: ExtensionUIStreamRequest): JsonObject {
        val raw = JsonCodec.encodeToJsonElement(ExtensionUIStreamClientEnvelope.serializer(),
            ExtensionUIStreamClientEnvelope(type = "extension_ui_stream", manager = binding.manager, request = request)).jsonObject
        NativeStreamSchema.parse(raw.toString(), "UIStreamClientEnvelope", NativeStreamSchema.RequestBytes)
        return raw
    }

    private suspend fun failedRead(requestId: String) {
        sent.remove(requestId)
        adapter.session.snapshotFailed(requestId)
        if (++readFailures > 2) { close(); return }
        scope.launch {
            withContext(deadlineDispatcher) { delay(responseDeadlineMillis) }
            if (!closing) adapter.session.requestSnapshot()
        }
    }

    private suspend fun receive(event: ExtensionUiStreamEvent) {
        val raw = event.raw
        if ((raw["manager"] as? JsonPrimitive)?.content != binding.manager) return
        val requestId = (raw["request_id"] as? JsonPrimitive)?.content.orEmpty()
        val remembered = sent[requestId]
        if (closing) { receiveCleanup(event, requestId, remembered); return }
        when (val envelope = event.envelope) {
            is ExtensionUIStreamSnapshotEnvelope -> {
                val snapshot = raw.getValue("snapshot").toString()
                when (remembered?.request) {
                    is ExtensionUIStreamOpenRequest, is ExtensionUIStreamResumeRequest, is ExtensionUIStreamSnapshotRequest -> {
                        if (adapter.session.snapshot(identity, requestId, snapshot)) {
                            allocatedHello = binding.connection.lastHello
                            readFailures = 0
                            sent.remove(requestId)
                        } else if (remembered.request !is ExtensionUIStreamOpenRequest) failedRead(requestId)
                        else sent.remove(requestId)
                    }
                    is ExtensionUIStreamEventRequest -> adapter.session.liveSnapshot(identity, snapshot)
                    null -> if (requestId.isEmpty()) adapter.session.liveSnapshot(identity, snapshot)
                    else -> Unit
                }
            }
            is ExtensionUIStreamDeltaEnvelope -> {
                val current = adapter.session.state.value.tree?.snapshot ?: return
                if (envelope.delta.session != current.session || envelope.delta.epoch != current.epoch) return
                adapter.session.delta(identity, raw.getValue("delta").toString())
            }
            is ExtensionUIStreamReceiptEnvelope -> {
                if (requestId in settled) return
                val record = remembered?.request as? ExtensionUIStreamEventRequest ?: return
                val receipt = envelope.receipt
                val interaction = record.interaction
                if (receipt.session != interaction.session || receipt.epoch != interaction.epoch || receipt.eventId != interaction.eventId) return
                if (receipt.status != "rejected" && receipt.version != interaction.version + 1) return
                val result = raw["receipt"]?.jsonObject?.get("result")
                if (result != null && result.toString().toByteArray(Charsets.UTF_8).size > 65536) { adapter.session.requestSnapshot(); return }
                if (receipt.result?.push?.state?.toByteArray(Charsets.UTF_8)?.size?.let { it > 16384 } == true) return
                waiting[requestId] = receipt
            }
            is ExtensionUIStreamErrorEnvelope -> {
                if (remembered == null) return
                when (remembered.request) {
                    is ExtensionUIStreamEventRequest -> { adapter.rejected(identity, requestId); adapter.session.requestSnapshot(); sent.remove(requestId) }
                    is ExtensionUIStreamOpenRequest -> { sent.remove(requestId); close() }
                    is ExtensionUIStreamResumeRequest, is ExtensionUIStreamSnapshotRequest -> {
                        if (envelope.error.code == "expired") { sent.remove(requestId); adapter.session.reopen() }
                        else failedRead(requestId)
                    }
                    else -> sent.remove(requestId)
                }
            }
            is ExtensionUIStreamClosedEnvelope -> {
                val current = adapter.session.state.value.tree?.snapshot ?: return
                if (envelope.session != current.session) return
                if (remembered?.request !is ExtensionUIStreamCloseRequest) adapter.session.reopen()
                sent.remove(requestId)
            }
            is ExtensionUIStreamSuspendedEnvelope -> {
                val current = adapter.session.state.value.tree?.snapshot ?: return
                if (envelope.session != current.session) return
                sent.remove(requestId)
                if (adapter.shown && adapter.session.state.value.phase == NativeStreamPhase.Ready) {
                    adapter.session.connection(false); adapter.session.connection(true)
                }
            }
            else -> Unit
        }
        drain()
    }

    private fun evictCompleted() {
        while (settled.size > 128) {
            val id = settled.first(); settled.remove(id); sent.remove(id); waiting.remove(id)
        }
        while (presented.size > 128) presented.remove(presented.first())
    }

    private suspend fun drain() {
        if (!adapter.shown || adapter.session.state.value.phase != NativeStreamPhase.Ready) return
        val tree = adapter.session.state.value.tree ?: return
        for ((requestId, receipt) in waiting.toMap()) {
            if (receipt.session != tree.snapshot.session || receipt.epoch != tree.snapshot.epoch || tree.snapshot.version < receipt.version) continue
            val remembered = sent[requestId] ?: continue
            val request = remembered.request as? ExtensionUIStreamEventRequest ?: continue
            val eventIdentity = Triple(receipt.session, receipt.epoch, receipt.eventId)
            if (presented.add(eventIdentity)) {
                val result = receipt.result ?: ExtensionUIStreamActionResult()
                if (receipt.status != "rejected" && effects.trySend(NativeStreamPresentation(request.interaction, remembered.tree, result)).isFailure) { close(); return }
            }
            if (receipt.status == "rejected") adapter.rejected(this.identity, requestId)
            else if (receipt.status == "accepted") adapter.session.received(this.identity, requestId, receipt.version)
            else adapter.session.acknowledged(this.identity, requestId)
            waiting.remove(requestId)
            settled.add(requestId)
            sent[requestId] = remembered.copy(tree = null)
            evictCompleted()
        }
    }

    /** Return after detaching UI; the bounded collector retains orphan and offline allocation ownership. */
    suspend fun close() = withContext(NonCancellable) {
        if (closing) return@withContext
        closing = true
        adapter.shown = false
        adapter.session.state.value.tree?.snapshot?.let { cleanup[it.session] = Cleanup(it, allocatedHello) }
        adapter.close()
        effects.close()
        sent.entries.removeAll { it.value.request !is ExtensionUIStreamOpenRequest }
        waiting.clear(); settled.clear(); presented.clear()
        scope.launch {
            withContext(deadlineDispatcher) { delay(responseDeadlineMillis) }
            retryOpenCleanup() // Same original ID and frozen body; the manager's open contract is idempotent.
            withContext(deadlineDispatcher) { delay(responseDeadlineMillis) }
            cleanup.clear(); sent.clear(); job.cancel()
        }
        advanceCleanup()
    }

    private var cleanupOpenRetried = false
    private fun retryOpenCleanup() {
        if (cleanupOpenRetried || binding.connection.state.value != ConnectionState.Online) return
        cleanupOpenRetried = true
        sent.values.filter { it.request is ExtensionUIStreamOpenRequest }.forEach { binding.connection.send(envelope(it.request)) }
    }

    private suspend fun advanceCleanup() {
        if (binding.connection.state.value != ConnectionState.Online) return
        for (entry in cleanup.values.toList()) {
            if (entry.requestId != null) continue
            val snapshot = entry.snapshot
            entry.resuming = entry.hello !== binding.connection.lastHello
            val request = if (entry.resuming) ExtensionUIStreamResumeRequest(protocol = 1, requestId = UUID.randomUUID().toString(),
                session = snapshot.session, epoch = snapshot.epoch, version = snapshot.version)
            else ExtensionUIStreamCloseRequest(protocol = 1, requestId = UUID.randomUUID().toString(), session = snapshot.session, epoch = snapshot.epoch)
            val raw = envelope(request)
            entry.requestId = raw.getValue("request").jsonObject.getValue("request_id").jsonPrimitive.content
            if (!binding.connection.send(raw)) entry.requestId = null
        }
        finishCleanup()
    }

    private suspend fun receiveCleanup(event: ExtensionUiStreamEvent, requestId: String, remembered: Sent?) {
        val entry = cleanup.values.firstOrNull { it.requestId == requestId }
        when (val envelope = event.envelope) {
            is ExtensionUIStreamSnapshotEnvelope -> {
                if (remembered?.request is ExtensionUIStreamOpenRequest) {
                    sent.remove(requestId)
                    cleanup[envelope.snapshot.session] = Cleanup(envelope.snapshot, remembered.hello)
                } else if (entry != null && entry.resuming && envelope.snapshot.session == entry.snapshot.session && envelope.snapshot.epoch == entry.snapshot.epoch) {
                    entry.hello = binding.connection.lastHello; entry.requestId = null
                }
            }
            is ExtensionUIStreamClosedEnvelope -> if (entry != null && envelope.session == entry.snapshot.session) cleanup.remove(entry.snapshot.session)
            is ExtensionUIStreamErrorEnvelope -> {
                // A read error is not proof of disposal. Keep ownership until the bounded deadline.
                if (remembered?.request is ExtensionUIStreamOpenRequest) sent.remove(requestId)
                if (entry != null && envelope.error.code == "expired") cleanup.remove(entry.snapshot.session)
            }
            else -> Unit
        }
        advanceCleanup()
    }

    private fun finishCleanup() {
        if (cleanup.isEmpty() && sent.values.none { it.request is ExtensionUIStreamOpenRequest }) job.cancel()
    }

    override fun onCleared() { scope.launch { close() } }
}

internal data class NativeStreamPresentation(
    val event: ExtensionUIStreamEvent,
    val tree: NativeStreamTree?,
    val result: ExtensionUIStreamActionResult,
)

/** File/context results stay bound to the original saved callback target, not a later selected diff. */
internal fun nativeStreamTargetResult(presentation: NativeStreamPresentation): ExtensionUiResult? {
    val result = presentation.result
    val lines = result.lines ?: return ExtensionUiResult(toast = result.toast)
    val values = presentation.event.value as? JsonObject
    fun value(name: String) = (values?.get(name) as? JsonPrimitive)?.content
    val path = value("path") ?: presentation.tree?.nodes?.get(presentation.event.key)?.get("path")?.jsonPrimitive?.content
    val side = value("side")
    val from = value("from")?.toIntOrNull()
    if (path != lines.path || side != null && lines.side != null && side != lines.side || from != null && from != lines.start) return null
    return ExtensionUiResult(toast = result.toast, lines = persona.core.contracts.api.ExtensionFileLines(lines.path, lines.side ?: side, lines.start, lines.lines, lines.total))
}
