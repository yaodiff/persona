package persona.core.hub

import persona.core.contracts.JsonCodec
import persona.core.contracts.decodeStandingAuthorizations
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import kotlinx.coroutines.CancellationException
import kotlinx.serialization.json.*
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.util.concurrent.TimeUnit

/** Construct per pairing using the shared routed, pinned client; reject stale responses after unpairing. */
class HttpAssistantManagement private constructor(
    baseUrl: String,
    private val token: String,
    client: OkHttpClient,
    private val isCurrent: () -> Boolean,
    private val pinnedManager: String?,
) : AssistantManagement, UsageHistoryApi {
    constructor(
        baseUrl: String, token: String, client: OkHttpClient, isCurrent: () -> Boolean = { true },
    ) : this(baseUrl, token, client, isCurrent, null)

    private val base = baseUrl.toHttpUrl()
    private val http = client.newBuilder().followRedirects(false).followSslRedirects(false)
        .retryOnConnectionFailure(false).build()
    /** "Run now" answers once the hub has started the conversation (it allows itself two minutes), like starting an agent. */
    private val runHttp = http.newBuilder().readTimeout(130, TimeUnit.SECONDS).build()
    /** Compacting may start the conversation's process first, and "New main chat" reads what the old one left: the hub allows a minute. */
    private val compactHttp = http.newBuilder().readTimeout(70, TimeUnit.SECONDS).build()
    /** Switching or clearing a conversation's model allows up to 30 s on the hub (`model_pick.go`). */
    private val modelPickHttp = http.newBuilder().readTimeout(MODEL_PICK_TIMEOUT_SECONDS, TimeUnit.SECONDS).build()

    override fun forManager(manager: String?): AssistantManagement =
        HttpAssistantManagement(base.toString(), token, http, isCurrent, manager.orEmpty())

    private fun checkScope() { if (!isCurrent()) throw CancellationException("Pairing changed") }

    private suspend fun request(
        method: String, segments: List<String>, body: JsonElement? = null, client: OkHttpClient = http,
        query: Map<String, String> = emptyMap(),
    ): JsonObject {
        checkScope()
        val url = base.newBuilder().apply {
            segments.forEach(::addPathSegment)
            query.forEach { (name, value) -> addQueryParameter(name, value) }
            pinnedManager?.let { setQueryParameter(ManagerQuery, it) }
        }.build().withRequestManager()
        val payload = body?.toString()?.toRequestBody("application/json".toMediaType())
        val request = Request.Builder().url(url).header("Authorization", "Bearer $token")
            .apply { pinnedManager?.takeIf(String::isNotBlank)?.let { header(ManagerHeader, it) } }
            .method(method, payload).build()
        return client.newCall(request).awaitResponse().use { response ->
            checkScope()
            if (!response.isSuccessful) throw HubFailure.from(response)
            val text = response.body?.string().orEmpty()
            if (text.isBlank()) buildJsonObject { } else JsonCodec.parseToJsonElement(text).jsonObject
        }
    }

    private suspend fun assistant(method: String = "GET", vararg path: String, body: JsonElement? = null) =
        request(method, listOf("api", "v1", "assistant") + path, body)

    private inline fun <reified T> JsonObject.list(name: String): List<T> =
        (this[name] as? JsonArray).orEmpty().map { JsonCodec.decodeFromJsonElement<T>(it) }

    override suspend fun warm() { assistant("POST", "warm", body = buildJsonObject { }) }
    override suspend fun alwaysGrants(session: SessionKey): List<AlwaysGrant>? = try {
        assistant("GET", "conversations", session.sessionId, "always").list("grants")
    } catch (failure: HubFailure) {
        // A hub before the route, or a device without full access: no list to show, nothing to fix.
        if (failure.status == 404 || failure.status == 403) null else throw failure
    }
    // The hub asks the conversation's computer to reset it first (up to 10 s) before it answers.
    override suspend fun revokeAlwaysGrant(session: SessionKey, keyHash: String): Boolean =
        (request("DELETE", listOf("api", "v1", "assistant", "conversations", session.sessionId, "always", keyHash), client = compactHttp)
            ["restart_required"] as? JsonPrimitive)?.booleanOrNull ?: false

    override suspend fun standingAuthorizations() = decodeStandingAuthorizations(assistant("GET", "standing-authorizations"))

    override suspend fun revokeStandingAuthorization(id: String) {
        require(id.isNotBlank())
        // One path segment, so a slash or a question mark in an id cannot change the route.
        assistant("DELETE", "standing-authorizations", id)
    }

    // Domain spaces (#13500, docs/web/web.md "Domain spaces (#13500)"): a slug or id is one path segment, so a slash in it cannot
    // change the route. A hub before tracks answers 404 / 405, or a list without `tracks`.
    override suspend fun tracks(): List<Track> {
        val answer = try { assistant("GET", "tracks") } catch (failure: HubFailure) {
            if (failure.status == 404 || failure.status == 405) throw TracksUnsupported() else throw failure
        }
        if (answer["tracks"] !is JsonArray) throw TracksUnsupported()
        return answer.list("tracks")
    }
    override suspend fun trackKinds(): List<TrackKind> = assistant("GET", "track-kinds").list("kinds")
    override suspend fun createTrack(track: TrackCreate): Track = JsonCodec.decodeFromJsonElement(
        assistant("POST", "tracks", body = JsonCodec.encodeToJsonElement(TrackCreate.serializer(), track)))
    override suspend fun changeTrack(slug: String, change: TrackChange): Track {
        require(slug.isNotBlank())
        return JsonCodec.decodeFromJsonElement(
            assistant("PUT", "tracks", slug, body = JsonCodec.encodeToJsonElement(TrackChange.serializer(), change)))
    }
    override suspend fun spaceMd(slug: String): SpaceMd {
        require(slug.isNotBlank())
        return JsonCodec.decodeFromJsonElement(assistant("GET", "tracks", slug, "space-md"))
    }
    override suspend fun writeSpaceMd(slug: String, content: String, baseDigest: String): SpaceMd {
        require(slug.isNotBlank())
        return JsonCodec.decodeFromJsonElement(assistant("PUT", "tracks", slug, "space-md", body = buildJsonObject {
            put("content", content)
            put("base_digest", baseDigest)
        }))
    }
    override suspend fun conversationCategories(session: SessionKey): ConversationCategories {
        require(session.sessionId.isNotBlank())
        return JsonCodec.decodeFromJsonElement(assistant("GET", "conversations", session.sessionId, "categories"))
    }
    override suspend fun changeConversationCategories(session: SessionKey, add: List<String>, remove: List<String>): ConversationCategories {
        require(session.sessionId.isNotBlank())
        return JsonCodec.decodeFromJsonElement(assistant("PUT", "conversations", session.sessionId, "categories", body = buildJsonObject {
            if (add.isNotEmpty()) put("add", JsonArray(add.map { JsonPrimitive(it) }))
            if (remove.isNotEmpty()) put("remove", JsonArray(remove.map { JsonPrimitive(it) }))
        }))
    }
    override suspend fun extensionTrust(): List<ExtensionTrust>? {
        val answer = try { assistant("GET", "extension-trust") } catch (failure: HubFailure) {
            if (failure.status == 404 || failure.status == 405) return null else throw failure
        }
        if (answer["items"] !is JsonArray) return null
        return answer.list("items")
    }
    override suspend fun setExtensionTrust(id: String, trust: ExtensionTrust): ExtensionTrust {
        require(id.isNotBlank())
        return JsonCodec.decodeFromJsonElement(assistant("PUT", "extension-trust", id, body = buildJsonObject {
            put("trust", trust.trust)
            put("categories", JsonArray(trust.categories.map { JsonPrimitive(it) }))
        }))
    }
    // The hub waits up to 12 s for the computer's self-check, and a probe runs a real one-word turn on top of it.
    override suspend fun readiness(probe: Boolean): AssistantReadiness = JsonCodec.decodeFromJsonElement(
        request("GET", listOf("api", "v1", "assistant", "readiness"), client = if (probe) runHttp else http,
            query = if (probe) mapOf("probe" to "1") else emptyMap()))
    override suspend fun dismissReadiness(dismissed: Boolean) {
        assistant("POST", "readiness", "dismiss", body = buildJsonObject { put("dismissed", dismissed) })
    }
    override suspend fun config(): AssistantConfig = JsonCodec.decodeFromJsonElement(assistant())
    override suspend fun updateConfig(update: AssistantConfigUpdate): AssistantConfig =
        JsonCodec.decodeFromJsonElement(assistant("PUT", body = JsonCodec.encodeToJsonElement(update)))
    override suspend fun launchers(machineId: String): List<AgentLauncher> {
        require(machineId.isNotBlank())
        return request("GET", listOf("api", "v1", "machines", machineId, "launchers")).list("launchers")
    }
    override suspend fun assistantLaunchers(machineId: String): List<AgentLauncher> {
        require(machineId.isNotBlank())
        return request("GET", listOf("api", "v1", "machines", machineId, "launchers"),
            query = mapOf("purpose" to "assistant")).list("launchers")
    }
    override suspend fun launcherModels(machineId: String, launcher: String): LauncherModels? = try {
        require(machineId.isNotBlank() && launcher.isNotBlank())
        JsonCodec.decodeFromJsonElement(request("GET",
            listOf("api", "v1", "machines", machineId, "launchers", launcher, "models"),
            query = mapOf("purpose" to "assistant")))
    } catch (failure: HubFailure) {
        if (failure.status == 404) null else throw failure
    }
    override suspend fun endConversation(session: SessionKey) {
        request("POST", listOf("api", "v1", "sessions", session.machineId, session.sessionId, "close"), buildJsonObject { })
    }
    override suspend fun compactConversation(session: SessionKey) {
        require(session.sessionId.isNotBlank())
        refusals {
            request("POST", listOf("api", "v1", "assistant", "conversations", session.sessionId, "compact"), buildJsonObject {
                if (session.machineId.isNotBlank()) put("machine_id", session.machineId)
            }, compactHttp)
        }
    }
    override suspend fun assistantLauncherList(machineId: String): AssistantLaunchers = assistantLauncherList(machineId, refresh = false)
    override suspend fun assistantLauncherList(machineId: String, refresh: Boolean): AssistantLaunchers {
        require(machineId.isNotBlank())
        return JsonCodec.decodeFromJsonElement(request("GET", listOf("api", "v1", "machines", machineId, "launchers"),
            query = buildMap {
                put("purpose", "assistant")
                if (refresh) put("refresh", "1")
            }))
    }
    override suspend fun mainAssistantLauncherList(machineId: String, refresh: Boolean): AssistantLaunchers {
        val shared = assistantLauncherList(machineId, refresh)
        val own: AssistantLaunchers = try {
            JsonCodec.decodeFromJsonElement(request("GET", listOf("api", "v1", "machines", machineId, "launchers"),
                query = mapOf("purpose" to "assistant.main")))
        } catch (failure: HubFailure) {
            if (failure.status == 404) return shared else throw failure
        }
        return shared.copy(default = own.default ?: shared.default)
    }
    override suspend fun setModelPick(session: SessionKey, pick: persona.core.contracts.ModelPick): persona.core.contracts.ModelPick {
        require(session.sessionId.isNotBlank() && pick.launcher.isNotBlank())
        val answer = request(
            method = "PUT",
            segments = listOf("api", "v1", "assistant", "conversations", session.sessionId, "model"),
            body = buildJsonObject {
                put("launcher", pick.launcher)
                pick.model?.takeIf(String::isNotBlank)?.let { put("model", it) }
                pick.effort?.takeIf(String::isNotBlank)?.let { put("effort", it) }
                if (session.machineId.isNotBlank()) put("machine_id", session.machineId)
            },
            client = modelPickHttp,
        )
        return (answer["model_pick"] as? JsonObject)?.let { JsonCodec.decodeFromJsonElement<persona.core.contracts.ModelPick>(it) } ?: pick
    }
    override suspend fun clearModelPick(session: SessionKey) {
        require(session.sessionId.isNotBlank())
        val _ = request(
            method = "DELETE",
            segments = listOf("api", "v1", "assistant", "conversations", session.sessionId, "model"),
            client = modelPickHttp,
        )
    }
    override suspend fun conversationSwitchState(session: SessionKey): ConversationSwitchState {
        require(session.machineId.isNotBlank() && session.sessionId.isNotBlank())
        return JsonCodec.decodeFromJsonElement(
            request(
                "GET",
                listOf("api", "v1", "assistant", "conversations", session.sessionId, "segments"),
                query = mapOf("machine_id" to session.machineId),
            ),
        )
    }
    override suspend fun submitConversationSwitch(session: SessionKey, request: ConversationSwitchRequest) {
        require(
            session.sessionId.isNotBlank() &&
                request.operationId.isNotBlank() &&
                request.expectedEpoch >= 1 &&
                request.sourceMachineId.isNotBlank() &&
                request.machineId.isNotBlank() &&
                request.launcher.isNotBlank(),
        )
        request(
            "POST",
            listOf("api", "v1", "assistant", "conversations", session.sessionId, "switch"),
            body = buildJsonObject {
                put("operation_id", request.operationId)
                put("expected_epoch", request.expectedEpoch)
                put("source_machine_id", request.sourceMachineId)
                put("machine_id", request.machineId)
                put("launcher", request.launcher)
                request.model?.takeIf(String::isNotBlank)?.let { put("model", it) }
                request.effort?.takeIf(String::isNotBlank)?.let { put("effort", it) }
                request.permissionMode?.takeIf(String::isNotBlank)?.let { put("permission_mode", it) }
            },
            client = compactHttp,
        )
    }
    override suspend fun archiveConversation(session: SessionKey) {
        require(session.sessionId.isNotBlank())
        assistant("POST", "conversations", session.sessionId, "archive", body = buildJsonObject { })
    }
    override suspend fun unarchiveConversation(session: SessionKey) {
        require(session.sessionId.isNotBlank())
        assistant("POST", "conversations", session.sessionId, "unarchive", body = buildJsonObject { })
    }
    override suspend fun setConversationTags(session: SessionKey, tags: List<String>) {
        require(session.sessionId.isNotBlank())
        assistant("PUT", "conversations", session.sessionId, "tags", body = buildJsonObject {
            put("tags", JsonArray(tags.map(::JsonPrimitive)))
        })
    }
    override suspend fun searchChats(query: String, limit: Int): AssistantSearchAnswer? = try {
        val q = query.trim().take(200)
        if (q.isEmpty()) AssistantSearchAnswer()
        else JsonCodec.decodeFromJsonElement(request("GET", listOf("api", "v1", "assistant", "search"),
            query = mapOf("q" to q, "limit" to limit.coerceIn(1, 50).toString())))
    } catch (failure: HubFailure) {
        if (failure.status == 404) null else throw failure
    }
    override suspend fun archivedConversations(): List<Session> {
        val out = request("GET", listOf("api", "v1", "assistant"), query = mapOf("archived" to "1"))
        val machine = (out["machine_id"] as? JsonPrimitive)?.contentOrNull.orEmpty()
        return out.list<Session>("conversations").filter { it.archived && it.space.isNullOrBlank() }
            .map { it.copy(machineId = it.machineId.ifEmpty { machine }) }
            .sortedByDescending { it.updated }
    }
    override suspend fun renameConversation(session: SessionKey, title: String) {
        require(session.machineId.isNotBlank() && session.sessionId.isNotBlank() && title.isNotBlank())
        request("POST", listOf("api", "v1", "sessions", session.machineId, session.sessionId, "rename"),
            buildJsonObject { put("title", title) })
    }
    override suspend fun makeDirection(session: SessionKey) {
        require(session.sessionId.isNotBlank())
        assistant("POST", "directions", session.sessionId, "make", body = buildJsonObject { })
    }
    override suspend fun endDirection(session: SessionKey) {
        require(session.sessionId.isNotBlank())
        assistant("POST", "directions", session.sessionId, "end", body = buildJsonObject { })
    }
    override suspend fun newMainConversation(carrySummary: Boolean, expect: String?): NewMainResult {
        val body = buildJsonObject {
            put("carry", if (carrySummary) "summary" else "none")
            expect?.takeIf(String::isNotBlank)?.let { put("expect", it) }
        }
        val out = refusals { request("POST", listOf("api", "v1", "assistant", "main", "new"), body, compactHttp) }
        return NewMainResult((out["archived"] as? JsonPrimitive)?.contentOrNull.orEmpty(),
            (out["carried"] as? JsonPrimitive)?.booleanOrNull == true)
    }
    override suspend fun clearMainConversation(expect: String?): String {
        val body = buildJsonObject { expect?.takeIf(String::isNotBlank)?.let { put("expect", it) } }
        val out = refusals { request("POST", listOf("api", "v1", "assistant", "main", "clear"), body, compactHttp) }
        return (out["archived"] as? JsonPrimitive)?.contentOrNull.orEmpty()
    }
    /** A 409 with a code is the hub refusing the action for a reason it words itself ("Answering", "Main chat already changed"…). */
    private inline fun <T> refusals(block: () -> T): T = try { block() } catch (failure: HubFailure) {
        val code = failure.code
        if (failure.status == 409 && !code.isNullOrBlank()) throw AssistantRefused(code, failure.message)
        throw failure
    }
    override suspend fun tasks(): List<ScheduledTask> = assistant("GET", "tasks").list("tasks")
    private fun JsonObject.task(): ScheduledTask = JsonCodec.decodeFromJsonElement(getValue("task"))
    override suspend fun saveTask(task: ScheduledTask): ScheduledTask =
        if (task.id.isBlank()) assistant("POST", "tasks", body = task.editable()).task()
        else assistant("PUT", "tasks", taskId(task.id), body = task.editable()).task()
    private fun taskId(id: String): String {
        require(id.isNotBlank() && '/' !in id && '\\' !in id && id != "." && id != "..")
        return id
    }
    override suspend fun deleteTask(id: String) { assistant("DELETE", "tasks", taskId(id)) }
    private fun JsonObject.session(): SessionKey {
        val machine = getValue("machine_id").jsonPrimitive.content
        val session = getValue("session_id").jsonPrimitive.content
        require(machine.isNotBlank() && session.isNotBlank())
        return SessionKey(machine, session)
    }
    override suspend fun runTask(id: String) =
        request("POST", listOf("api", "v1", "assistant", "tasks", taskId(id), "run"), buildJsonObject { }, runHttp).session()
    override suspend fun freshTask(id: String): ScheduledTask =
        assistant("POST", "tasks", taskId(id), "fresh", body = buildJsonObject { }).task()
    override suspend fun runs(): List<Run> = assistant("GET", "runs").list("runs")
    override suspend fun notes(): AssistantNotes = JsonCodec.decodeFromJsonElement(assistant())
    override suspend fun setNotes(text: String) { assistant("PUT", body = buildJsonObject { put("notes", text) }) }
    override suspend fun memory(): List<MemoryFile> = assistant("GET", "memory").list("files")
    private fun memoryName(name: String): String {
        require(name.isNotBlank() && name != "." && name != ".." && '/' !in name && '\\' !in name)
        return name
    }
    override suspend fun saveMemory(name: String, content: String) {
        assistant("PUT", "memory", memoryName(name), body = buildJsonObject { put("content", content) })
    }
    override suspend fun deleteMemory(name: String) { assistant("DELETE", "memory", memoryName(name)) }
    override suspend fun places(): List<SavedPlace> = assistant("GET", "places").list("places")
    private fun placeId(id: String): String {
        require(id.isNotBlank() && id != "." && id != ".." && '/' !in id && '\\' !in id)
        return id
    }
    private fun SavedPlace.writeBody(): JsonObject = buildJsonObject {
        put("name", name)
        put("address", address)
        put("latitude", latitude)
        put("longitude", longitude)
        put("radius_meters", radiusMeters)
    }
    override suspend fun savePlace(place: SavedPlace): SavedPlace {
        val body = place.writeBody()
        val out = if (place.id.isBlank()) assistant("POST", "places", body = body)
        else assistant("PUT", "places", placeId(place.id), body = body)
        return JsonCodec.decodeFromJsonElement(out.getValue("place"))
    }
    override suspend fun deletePlace(id: String) { assistant("DELETE", "places", placeId(id)) }
    override suspend fun skills(): AssistantSkills = try {
        val out = request("GET", listOf("api", "v1", "skills"))
        AssistantSkills((out["machine_id"] as? JsonPrimitive)?.contentOrNull.orEmpty(), (out["machine"] as? JsonPrimitive)?.contentOrNull,
            out.list<AgentSkill>("skills").filter { it.name.isNotBlank() })
    } catch (failure: HubFailure) {
        // 409 (no machine runs the assistant) and 502 (its node did not answer) carry the hub's reason, fit to show.
        val code = failure.code
        if ((failure.status == 409 || (failure.status ?: 0) >= 500) && !code.isNullOrBlank()) throw AssistantRefused(code, failure.message)
        throw failure
    }

    override suspend fun usageDays(days: Int): UsageDays {
        require(days in 1..371) { "days is 1 to 371" }
        val answer = request("GET", listOf("api", "v1", "usage", "days"), query = mapOf("days" to days.toString()))
        return JsonCodec.decodeFromJsonElement(answer.getValue("usage"))
    }

    override suspend fun usageHours(days: Int): UsageHours {
        require(days in 1..7) { "days is 1 to 7" }
        val answer = request("GET", listOf("api", "v1", "usage", "hours"), query = mapOf("days" to days.toString()))
        return JsonCodec.decodeFromJsonElement(answer.getValue("usage"))
    }

    override suspend fun usageProjects(): UsageProjects? = try {
        val answer = request("GET", listOf("api", "v1", "usage", "projects"))
        JsonCodec.decodeFromJsonElement<UsageProjects>(answer.getValue("usage")).takeIf { it.ranges.isNotEmpty() }
    } catch (failure: HubFailure) {
        if (failure.status == 404) null else throw failure
    }

    override suspend fun usageProjectDetail(key: String): UsageProjectDetail {
        require(key.isNotBlank()) { "project key is required" }
        val answer = request("GET", listOf("api", "v1", "usage", "projects", "detail"), query = mapOf("key" to key))
        return JsonCodec.decodeFromJsonElement(answer.getValue("usage"))
    }

    companion object {
        internal const val MODEL_PICK_TIMEOUT_SECONDS = 40L
    }
}
