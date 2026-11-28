package persona.core.hub

import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.contracts.generated.ExtensionHubCapability
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.decodeFromJsonElement
import java.util.UUID
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.withTimeoutOrNull
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response

/** Shares the selected connection's authenticated HTTP client and route. */
class HubChatTransport internal constructor(
    private val send: suspend (path: String, method: String, body: RequestBody?, headers: Map<String, String>) -> Response,
    /** Waits until the connection is Online, at most the given millis; false when it was not. */
    private val awaitOnline: suspend (Long) -> Boolean = { true },
    private val clockMillis: () -> Long = { System.nanoTime() / 1_000_000 },
    private val newOpId: () -> String = { UUID.randomUUID().toString() },
    /** How long an answer cut off by the connection keeps being retried with the same op id. */
    private val resolveRetryMillis: Long = RESOLVE_RETRY_MILLIS,
    private val hierarchy: () -> Boolean = { false },
    private val crossMachineHierarchy: () -> Boolean = { false },
) : ChatTransport {
    constructor(connection: OkHttpHubConnection) : this(
        { path, method, body, headers -> connection.request(path, method, body, headers) },
        { timeout -> withTimeoutOrNull(timeout) { connection.state.first { it == ConnectionState.Online } } != null },
        hierarchy = { "sessions_hierarchy" in connection.serverFeatures.value },
        crossMachineHierarchy = { SESSIONS_CROSS_MACHINE_HIERARCHY in connection.serverFeatures.value },
    )

    /** Fixed origins are only available to tests in this module. */
    internal constructor(http: HubHttpClient, endpoint: Endpoint, token: String) : this({ path, method, body, headers ->
        val url = endpoint.baseUrl().hubUrl(path)
        http.client.newCall(Request.Builder().url(url).apply { headers.forEach { (name, value) -> header(name, value) } }
            .header("Authorization", "Bearer $token")
            .method(method, body).build()).awaitResponse().also { response ->
            if (!response.isSuccessful) throw response.use { HubFailure.from(it) }
        }
    })

    private suspend fun request(path: String, method: String, body: RequestBody?) = send(path, method, body, emptyMap())

    // A cut-off request may already have reached the hub: later user retries keep its event ID until a receipt wins.
    private val ownerApprovalAttemptsLock = Any()
    private val ownerApprovalAttempts = mutableMapOf<String, String>()
    private fun ownerApprovalAttemptKey(interaction: Interaction) = interaction.id + "\u0000" + (interaction.createdAt ?: "")

    override suspend fun machines() = get<MachinesResponse>(api("machines")).machines
    override suspend fun sessions() = get<SessionsResponse>(api("sessions")).sessions

    override suspend fun sessionPage(filter: SessionListFilter, cursor: String?, since: String?): SessionListPage =
        bounded(sessionListPath(filter, cursor, since), setOf("sessions", "has_more", "total"))

    override suspend fun sessionCounts(filter: SessionListFilter): SessionListCounts =
        bounded(sessionListPath(filter, counts = true), setOf("total", "machine", "project", "group", "state"))

    private fun sessionListPath(
        filter: SessionListFilter,
        cursor: String? = null,
        since: String? = null,
        counts: Boolean = false,
    ): String {
        val url = path("sessions").apply {
            // A complete-membership view holds whole rows (`summary=0`), read in the largest pages the hub allows.
            val complete = filter.requiresCompleteMembership && !counts
            addQueryParameter("summary", if (complete) "0" else "1")
            addQueryParameter("limit", if (complete) "200" else "50")
            if (counts) addQueryParameter("counts", "1")
            if (hierarchy()) {
                val cross = crossMachineHierarchy()
                if (filter.parentSessionId != null) {
                    addQueryParameter("parent_session_id", filter.parentSessionId)
                    if (cross && !filter.parentMachineId.isNullOrBlank()) {
                        addQueryParameter("parent_machine_id", filter.parentMachineId)
                    }
                } else if (filter.roots && !filter.requiresCompleteMembership) {
                    addQueryParameter("roots", "1")
                    if (cross) addQueryParameter("cross_machine_hierarchy", "1")
                }
            }
            filter.state?.let { addQueryParameter("state", it) }
            filter.machineId?.let { addQueryParameter("machine_id", it) }
            filter.project?.let { addQueryParameter("project", it) }
            filter.group?.let { addQueryParameter("group", it) }
            filter.source?.let { addQueryParameter("source", it) }
            filter.provider?.let { addQueryParameter("provider", it) }
            if (null in filter.hiddenBotmuxHubs) addQueryParameter("exclude_pane_source", "feishu")
            if (filter.query.isNotEmpty()) addQueryParameter("q", filter.query)
            cursor?.let { addQueryParameter("cursor", it) }
            since?.let { addQueryParameter("since", it) }
        }.build()
        return url.encodedPath + "?" + url.encodedQuery
    }

    /** Only capability failures permit legacy fallback; all other failures remain observable. */
    private suspend inline fun <reified T> bounded(path: String, envelope: Set<String>): T {
        val body = try {
            request(path, "GET", null).use { response ->
                if (!response.isSuccessful) throw HubFailure.from(response)
                response.body!!.string()
            }
        } catch (failure: HubFailure) {
            when {
                failure.status in setOf(404, 405, 501) -> throw SessionListsUnsupported()
                failure.status == 409 && failure.code in setOf("cursor_expired", "revision_expired") ->
                    throw SessionListExpired()
                else -> throw failure
            }
        }
        val element = try { JsonCodec.parseToJsonElement(body) }
        catch (error: Exception) { throw HubFailure(HubError.Protocol, retryable = false, cause = error) }
        val obj = element as? JsonObject ?: throw SessionListsUnsupported()
        val revision = (obj["revision"] as? JsonPrimitive)?.takeIf { it.isString }?.content
        if (revision.isNullOrEmpty() || !obj.keys.containsAll(envelope)) throw SessionListsUnsupported()
        return try { JsonCodec.decodeFromJsonElement<T>(obj) }
        catch (error: Exception) { throw HubFailure(HubError.Protocol, retryable = false, cause = error) }
    }
    override suspend fun interactions() = get<InteractionsResponse>(api("interactions")).interactions

    override suspend fun items(session: SessionKey, query: ItemsQuery): ItemsResponse {
        val url = path("sessions", session.machineId, session.sessionId, "items").apply {
            addQueryParameter("lazy_images", "1")
            when {
                query.afterSeq != null -> addQueryParameter("after_seq", query.afterSeq.toString())
                query.fromSeq != null -> {
                    addQueryParameter("from_seq", query.fromSeq.toString())
                    addQueryParameter("through_seq", query.throughSeq.toString())
                }
                else -> {
                    addQueryParameter("turns", "2")
                    addQueryParameter("fold_work", "1")
                    query.beforeSeq?.let { addQueryParameter("before_seq", it.toString()) }
                }
            }
        }.build()
        return get(url.encodedPath + "?" + url.encodedQuery, ToolViewsHeader)
    }

    override suspend fun modeChanges(session: SessionKey): List<ModeChange> {
        val body = request(api("sessions", session.machineId, session.sessionId, "mode-changes"), "GET", null)
            .use { it.body!!.string() }
        val root = try { JsonCodec.parseToJsonElement(body) as? JsonObject }
        catch (error: Exception) { throw HubFailure(HubError.Protocol, retryable = false, cause = error) }
        val records = root?.get("changes") as? JsonArray ?: return emptyList()
        return records.mapNotNull { element ->
            val change = runCatching { JsonCodec.decodeFromJsonElement<ModeChange>(element) }.getOrNull()
                ?: return@mapNotNull null
            change.takeIf { it.kind in MODE_CHANGE_KINDS && parseTime(it.at) != null }
        }
    }

    /**
     * Sends one answer under one op id (#1574): when the connection cuts it off (a hub restarting, a network change),
     * the same answer goes again with the same id once the connection is back, for up to [resolveRetryMillis]; the
     * hub runs it at most once and answers a repeat with the stored outcome.
     */
    override suspend fun resolve(id: String, resolution: Resolution) { resolveOutcome(id, resolution) }

    /**
     * A card that is over answers 200 `already` with the answer that took effect, or 410 with how it ended
     * (docs/protocol/protocol.md, #1673): the first is an outcome, the second an [InteractionGoneException] carrying `status`.
     */
    override suspend fun resolveOutcome(id: String, resolution: Resolution): ResolveOutcome =
        resolveOutcome(id, resolution, handoffCard = false)

    override suspend fun resolveOutcome(id: String, resolution: Resolution, handoffCard: Boolean): ResolveOutcome =
        resolveOutcome(id, resolution, handoffCard, secretRunEnv = false, createdAt = null)

    override suspend fun resolveOutcome(id: String, resolution: Resolution, handoffCard: Boolean, secretRunEnv: Boolean): ResolveOutcome =
        resolveOutcome(id, resolution, handoffCard, secretRunEnv, createdAt = null)

    override suspend fun resolveOccurrence(interaction: Interaction, resolution: Resolution, handoffCard: Boolean): ResolveOutcome =
        resolveOutcome(interaction.id, resolution, handoffCard,
            secretRunEnv = interaction.secretRun?.delivery == "env", createdAt = interaction.createdAt)

    private suspend fun resolveOutcome(id: String, resolution: Resolution, handoffCard: Boolean, secretRunEnv: Boolean, createdAt: String?): ResolveOutcome {
        val path = api("interactions", id, "resolve")
        val body = JsonCodec.encodeToString(resolution)
        val headers = ProtocolCompatibility.operationHeaders(newOpId()) +
            (if (handoffCard && resolution.allow == true) {
                mapOf("X-Persona-Features" to if (secretRunEnv) "secret_run_env" else "secret_handoff")
            } else emptyMap()) +
            (if (createdAt != null) mapOf("X-Persona-Interaction-Created-At" to createdAt) else emptyMap())
        val deadline = clockMillis() + resolveRetryMillis
        while (true) {
            try {
                val text = send(path, "POST", body.toRequestBody("application/json; charset=utf-8".toMediaType()), headers)
                    .use { response -> runCatching { response.body?.string() }.getOrNull() }
                return resolveOutcome(text)
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                val failure = HubFailure.from(error)
                if (failure.status == 410) throw InteractionGoneException(field(failure.body, "status"))
                if (failure.code == "prompt_changed") throw InteractionPromptChangedException()
                val remaining = deadline - clockMillis()
                if (!cutOff(failure) || remaining <= 0) throw error
                delay(RESOLVE_RETRY_PAUSE_MILLIS.coerceAtMost(remaining))
                if (!awaitOnline((deadline - clockMillis()).coerceAtLeast(0))) throw error
            }
        }
    }

    /** Exact text and exact target, with no composer, quote, via, image, launcher or model fields. */
    override suspend fun replyOwnerApproval(interaction: Interaction, phrase: String) {
        require(interaction.ownerApprovalPhrase == phrase) { "The proposal did not advertise this exact reply" }
        val attemptKey = ownerApprovalAttemptKey(interaction)
        val eventID = synchronized(ownerApprovalAttemptsLock) {
            ownerApprovalAttempts.getOrPut(attemptKey) { newOpId() }
        }
        val path = api("sessions", interaction.machineId, interaction.sessionId, "messages")
        val body = JsonObject(buildMap {
            put("text", JsonPrimitive(phrase))
            put("client_msg_id", JsonPrimitive(eventID))
            put("approval_reply_to", JsonPrimitive(interaction.id))
            interaction.createdAt?.let { put("approval_reply_created_at", JsonPrimitive(it)) }
        }).toString().toRequestBody("application/json; charset=utf-8".toMediaType())
        val headers = ProtocolCompatibility.operationHeaders(eventID)
        val deadline = clockMillis() + resolveRetryMillis
        while (true) {
            try {
                val text = send(path, "POST", body, headers).use { response -> runCatching { response.body?.string() }.getOrNull() }
                val root = text?.let { runCatching { JsonCodec.parseToJsonElement(it) as? JsonObject }.getOrNull() }
                val approved = (root?.get("approval_reply") as? JsonPrimitive)?.booleanOrNull == true
                if (approved && field(text, "interaction_id") == interaction.id) {
                    synchronized(ownerApprovalAttemptsLock) { ownerApprovalAttempts.remove(attemptKey) }
                    return
                }
                throw HubFailure(HubError.Protocol, "The hub did not confirm this exact owner reply", retryable = false)
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                val failure = HubFailure.from(error)
                val remaining = deadline - clockMillis()
                if (!cutOff(failure) || remaining <= 0) throw error
                delay(RESOLVE_RETRY_PAUSE_MILLIS.coerceAtMost(remaining))
                if (!awaitOnline((deadline - clockMillis()).coerceAtLeast(0))) throw error
            }
        }
    }

    override suspend fun reanswer(id: String, answers: Map<String, String>, manager: String?) {
        val url = path("interactions", id, "reanswer").apply { manager?.takeIf(String::isNotEmpty)?.let { addQueryParameter("manager", it) } }.build()
        val body = JsonObject(mapOf("answers" to JsonObject(answers.mapValues { JsonPrimitive(it.value) }))).toString()
            .toRequestBody("application/json; charset=utf-8".toMediaType())
        request(url.encodedPath + (url.encodedQuery?.let { "?$it" } ?: ""), "POST", body).use { if (!it.isSuccessful) throw HubFailure.from(it) }
    }

    override suspend fun image(session: SessionKey, itemId: String): ByteArray =
        request(api("sessions", session.machineId, session.sessionId, "items", itemId, "image"), "GET", null)
            .use { it.body!!.bytes() }

    override suspend fun fullItem(session: SessionKey, itemId: String): Item =
        get(api("sessions", session.machineId, session.sessionId, "items", itemId, "full"), ToolViewsHeader)

    companion object {
        private val ToolViewsHeader = mapOf("X-Persona-Features" to ExtensionHubCapability.TOOL_VIEWS)
        private val MODE_CHANGE_KINDS = setOf("set", "restart_done", "restart_failed", "restart_dropped")
        const val OP_ID_HEADER = "X-Persona-Op-Id"
        const val RESOLVE_RETRY_MILLIS = 60_000L
        /** Between tries: a hub that answers 503 while it stops would otherwise be asked again at once. */
        const val RESOLVE_RETRY_PAUSE_MILLIS = 500L

        /** The hub's 200 body; anything unreadable (an older hub's `{"ok":true}`, none at all) is a plain answer. */
        internal fun resolveOutcome(text: String?): ResolveOutcome {
            val body = text?.let { runCatching { JsonCodec.parseToJsonElement(it) as? JsonObject }.getOrNull() }
                ?: return ResolveOutcome()
            val already = (body["already"] as? JsonPrimitive)?.booleanOrNull == true
            val resolution = body["resolution"]?.takeIf { it is JsonObject }
                ?.let { runCatching { JsonCodec.decodeFromJsonElement<Resolution>(it) }.getOrNull() }
            return ResolveOutcome(already, field(text, "status"), resolution)
        }

        /** A string field of a JSON object body, when there is one. */
        internal fun field(text: String?, name: String): String? = text
            ?.let { runCatching { JsonCodec.parseToJsonElement(it) as? JsonObject }.getOrNull() }
            ?.get(name)?.let { it as? JsonPrimitive }?.takeIf { it.isString }?.content?.takeIf { it.isNotEmpty() }

        /**
         * The connection lost the request (refused, reset, timed out), or something in front of the hub answered
         * 502/503/504 for it. The hub's own errors carry a `code` or say what was `delivered` (503 `assistant_offline`,
         * a node that did not answer): those are answers, not gaps, as the web's `isTransient` (#2768).
         */
        internal fun cutOff(failure: HubFailure) = when (failure.status) {
            null -> failure.kind in setOf(HubError.ConnectionRefused, HubError.Unreachable, HubError.Timeout,
                HubError.Transport, HubError.NoNetwork, HubError.AddressNotFound)
            502, 503, 504 -> failure.code.isNullOrEmpty() && failure.delivered == null
            else -> false
        }
    }

    private suspend inline fun <reified T> get(path: String, headers: Map<String, String> = emptyMap()): T = send(path, "GET", null, headers).use {
        try { JsonCodec.decodeFromString<T>(it.body!!.string()) }
        catch (error: Exception) { throw HubFailure(HubError.Protocol, retryable = false, cause = error) }
    }

    // Only the encoded path/query are used; the authenticated caller chooses the actual origin.
    private fun path(vararg segments: String) = HttpUrl.Builder().scheme("https").host("example.com")
        .addPathSegment("api").addPathSegment("v1").apply { segments.forEach(::addPathSegment) }
    private fun api(vararg segments: String) = path(*segments).build().encodedPath
}
