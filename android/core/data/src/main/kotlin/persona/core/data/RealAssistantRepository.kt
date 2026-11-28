package persona.core.data

import persona.core.contracts.CoreText
import persona.core.contracts.CoreText.Key
import persona.core.contracts.Direction
import persona.core.contracts.DirectionAgent
import persona.core.contracts.DirectionCounts
import persona.core.contracts.DirectionTask
import persona.core.contracts.DirectionTaskState
import persona.core.contracts.DirectionTree
import persona.core.contracts.DirectionsEvent
import persona.core.contracts.ExternalDirection
import persona.core.contracts.Hello
import persona.core.contracts.JsonCodec
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.domain.models.keepingDetails
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.json.*
import java.util.logging.Level
import java.util.logging.Logger

class RealAssistantRepository(
    private val connection: InboxAssistantConnection,
    private val sessions: SessionsRepository,
    scope: CoroutineScope,
    private val snapshots: ListSnapshotStore? = null,
    private val preferences: HubPreferences? = null,
    /** The hub's pushes: `{"type":"directions"}` (#4352) and a new hello read the direction tree again. */
    events: Flow<ServerEvent> = emptyFlow(),
) : AssistantConversations {
    private val current = MutableStateFlow(AssistantSnapshot())
    override val assistant = current.asStateFlow()
    private val currentPins = MutableStateFlow<Set<SessionKey>>(emptySet())
    override val pinned = currentPins.asStateFlow()
    private val lock = Mutex()
    private val creationLock = Mutex()
    private var restoredScope: String? = null
    private val cached = mutableMapOf<String, AssistantSnapshot>()
    private var refreshVersion = 0L

    private data class Request(
        val transport: InboxAssistantTransport,
        val choice: AssistantManagerChoice.Choice,
    ) {
        val manager get() = choice.manager
        val owner get() = choice.shown
        // Preserve the builtin assistant's existing disk snapshot; all other owners have explicit identities.
        val key get() = "${transport.scope}:assistant" + if (owner == ManagerRoster.ASSISTANT_MANAGER) "" else "@$owner"
    }

    private fun request(): Request = Request(
        requireNotNull(connection.current.value) { CoreText.of(Key.PairHubFirst).shown },
        AssistantManagerChoice.current.value,
    )

    private fun selected(request: Request): Boolean = connection.current.value === request.transport &&
        AssistantManagerChoice.current.value == request.choice

    private fun decode(data: JsonObject, request: Request): AssistantSnapshot {
        val machine = data.string("machine_id")
        val main = data.string("main")
        val conversations = data.array("conversations").map { JsonCodec.decodeFromJsonElement<Session>(it) }
        (conversations.map { it.sessionId } + main).filter { it.isNotEmpty() }
            .forEach { noteConversation(it, request) }
        return AssistantSnapshot(
            if (machine.isNotEmpty() && main.isNotEmpty()) SessionKey(machine, main) else null,
            conversations, request.manager, request.owner,
            leaders = directionLeaderCacheOf(data) ?: leadersOf(data),
            directionNames = directionNameCacheOf(data),
            externalDirections = directionExternalCacheOf(data),
            backgroundSupervisor = backgroundSupervisorOf(data),
            recentTaskChats = (data["recent_task_chats"] as? JsonPrimitive)?.booleanOrNull,
            conversationOrder = (data["conversation_order"] as? JsonArray)
                ?.mapNotNull { (it as? JsonPrimitive)?.contentOrNull },
            conversationTagFilters = data["conversation_tag_filters"]?.let {
                JsonCodec.decodeFromJsonElement<ConversationTagFilters>(it)
            } ?: ConversationTagFilters.Unsupported,
        )
    }

    /**
     * The direction tree (#4352), the one place the app reads and parses it: the drawer's 常驻 rows, To-dos and Agents
     * all draw this. The hub answers it for the default manager alone; null where it cannot be read (an older hub answers
     * 404), which changes nothing.
     */
    private suspend fun directionTree(request: Request): DirectionTree? {
        if (request.manager != null) return null
        return try {
            directionTreeOf(request.transport.directionsTreeOf(request.manager))
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            null
        }
    }

    /**
     * What the drawer shows: the tree's ids in the hub's order, names and the tree itself where it answers. A hub without
     * the tree (an older one answers 404) keeps [held] and takes the leaders answer's names, which have no counts.
     */
    private suspend fun directionsOf(request: Request, held: LeaderInfo): LeaderInfo {
        directionTree(request)?.let {
            return LeaderInfo(it.ids, it.names, it, it.leaders.mapNotNull { d ->
                d.external?.let { external -> d.id to external }
            }.toMap())
        }
        val leaders = try {
            request.transport.assistantLeadersOf(request.owner)
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            return held
        }
        val names = directionNamesOf(leaders)
        // Null ids select the fresh assistant answer's leader order in refresh; held ids are only for a failed read.
        return LeaderInfo(null, names.ifEmpty { held.names }, held.tree, directionExternalOf(leaders))
    }

    private fun noteConversation(id: String, request: Request) {
        AssistantManagerChoice.noteItem(listOf("assistant", "conversations", id), request.choice.shown, request.choice.hubId)
    }

    private suspend fun restore(request: Request) {
        if (!selected(request) || restoredScope == request.key) return
        // The selected page changes before disk or network work, so old conversations cannot stay actionable.
        restoredScope = request.key
        current.value = cached[request.key]?.copy(manager = request.manager) ?: AssistantSnapshot(manager = request.manager, managerId = request.owner)
        currentPins.value = preferences?.preferences?.value?.pins?.sessions?.mapNotNull(::sessionPath)?.toSet().orEmpty()
        if (request.key !in cached) {
            val data = snapshots.restore<JsonObject>(request.key)
            if (data != null && selected(request)) {
                val snapshot = decode(data, request)
                cached[request.key] = snapshot
                current.value = snapshot
            }
        }
    }

    init {
        if (preferences != null) scope.launch {
            preferences.preferences.collect { state ->
                currentPins.value = state.pins.sessions.mapNotNull(::sessionPath).toSet()
            }
        }
        // The direction tree changed (a direction made, ended or renamed, a to-do or an Agent: `directions`, #4352): the
        // drawer, To-dos and Agents follow at once, whichever device did it, by reading the tree again; also on a new
        // hello (the socket came back without the transport changing, so no refresh ran).
        scope.launch {
            events.filter { it === DirectionsEvent || it is Hello }.collect {
                try {
                    applyDirections()
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Exception) {
                    Logger.getLogger(RealAssistantRepository::class.java.name).log(
                        Level.WARNING, "Directions update failed", e,
                    )
                }
            }
        }
        scope.launch {
            var refreshJob: Job? = null
            combine(connection.current, AssistantManagerChoice.current) { transport, choice ->
                transport?.let { Request(it, choice) }
            }.collect { request ->
                refreshJob?.cancel()
                if (request == null) lock.withLock {
                    restoredScope = null
                    current.value = AssistantSnapshot()
                    currentPins.value = emptySet()
                } else {
                    lock.withLock { restore(request) }
                    refreshJob = launch {
                        // Refresh errors leave the selected manager's cached page available offline.
                        try {
                            refresh(request)
                        } catch (e: CancellationException) {
                            throw e
                        } catch (e: Exception) {
                            Logger.getLogger(RealAssistantRepository::class.java.name).log(
                                Level.WARNING, "Assistant refresh failed; retaining cached conversations", e,
                            )
                        }
                    }
                }
            }
        }
        scope.launch {
            sessions.sessions.collect { updates ->
                val reread = lock.withLock {
                    val transport = connection.current.value ?: return@withLock null
                    val choice = AssistantManagerChoice.current.value
                    val request = Request(transport, choice)
                    if (restoredScope != request.key || current.value.managerId != request.owner) return@withLock null
                    // A space's chat (health or a domain space, docs/assistant/health.md): never on the assistant's own list.
                    val owned = updates.filter { choice.owns(it.manager) && !it.isSpace }
                    val byKey = owned.associateBy { it.key }
                    val before = current.value
                    // A summary row (paged lists) leaves the launcher, the permission mode, the compaction and the
                    // other chat details out: they stay as the conversation had them (#5055); its model_pick is its own.
                    current.update { snapshot ->
                        snapshot.copy(conversations = (snapshot.conversations.map { byKey[it.key]?.keepingDetails(it) ?: it } +
                            owned.filter { it.assistant == true }).distinctBy { it.key })
                    }
                    cached[request.key] = current.value
                    request.takeIf { mayBeNewBackgroundSupervisor(before, owned) }
                }
                // The hub makes the background supervisor on first use (#4927), long after the page was read: which
                // conversation it is comes from `GET /api/v1/assistant` alone, so a new one that may be it is asked about.
                if (reread != null) launch {
                    try {
                        refresh(reread)
                    } catch (e: CancellationException) {
                        throw e
                    } catch (e: Exception) {
                        Logger.getLogger(RealAssistantRepository::class.java.name).log(
                            Level.WARNING, "Assistant refresh for a new conversation failed; the list stands", e,
                        )
                    }
                }
            }
        }
    }

    /**
     * `directions` (#4352): no payload, so the tree is read again. It is the default manager's alone, and not while
     * another manager's page is shown. An id no conversation on the page has (a direction made elsewhere, or one this
     * list has not met) needs the conversations too, so the page refreshes.
     */
    private suspend fun applyDirections() {
        val request = lock.withLock {
            val transport = connection.current.value ?: return
            val choice = AssistantManagerChoice.current.value
            val request = Request(transport, choice)
            if (request.manager != null) return
            if (restoredScope != request.key || current.value.managerId != request.owner) return
            request
        }
        val tree = directionTree(request) ?: return
        val missing = lock.withLock {
            if (!selected(request)) return
            val snapshot = current.value
            val main = snapshot.mainSession?.sessionId
            val ids = tree.ids.filter { it.isNotEmpty() && it != main }
            val known = snapshot.conversations.mapTo(HashSet()) { it.sessionId }
            current.value = snapshot.copy(leaders = ids, directionNames = tree.names, directionTree = tree,
                externalDirections = tree.leaders.mapNotNull { d -> d.external?.let { d.id to it } }.toMap())
            cached[request.key] = current.value
            ids.any { it !in known && tree.direction(it)?.external == null }
        }
        if (missing) refresh(request)
    }

    override suspend fun refresh() = refresh(request())

    override suspend fun updateRecentTaskChats(value: Boolean?) {
        recentTaskChatsUpdater()(value)
    }

    override suspend fun recentTaskChatsUpdater(): suspend (Boolean?) -> Unit {
        val request = request()
        return { value ->
            lock.withLock {
                val selected = selected(request)
                val snapshot = if (selected) current.value else cached[request.key] ?: return@withLock
                if (selected) ++refreshVersion
                val updated = snapshot.copy(recentTaskChats = value)
                cached[request.key] = updated
                if (selected) current.value = updated
            }
        }
    }

    override suspend fun updateConversationOrder(value: List<String>?) {
        conversationOrderUpdater()(value)
    }

    override suspend fun conversationOrderUpdater(): suspend (List<String>?) -> Unit {
        val request = request()
        return { value ->
            lock.withLock {
                val selected = selected(request)
                val snapshot = if (selected) current.value else cached[request.key] ?: return@withLock
                if (selected) ++refreshVersion
                val updated = snapshot.copy(conversationOrder = value)
                cached[request.key] = updated
                if (selected) current.value = updated
            }
        }
    }

    override suspend fun updateConversationTagFilters(value: ConversationTagFilters) {
        conversationTagFiltersUpdater()(value)
    }

    override suspend fun conversationTagFiltersUpdater(): suspend (ConversationTagFilters) -> Unit {
        val request = request()
        return { value ->
            lock.withLock {
                val selected = selected(request)
                val snapshot = if (selected) current.value else cached[request.key] ?: return@withLock
                if (selected) ++refreshVersion
                val updated = snapshot.copy(conversationTagFilters = value)
                cached[request.key] = updated
                if (selected) current.value = updated
            }
        }
    }

    private suspend fun refresh(request: Request) {
        val version = lock.withLock {
            if (!selected(request)) return
            restore(request)
            ++refreshVersion
        }
        // Network calls never hold the page/cache mutex: a slow manager cannot block switching away.
        val data = request.transport.assistantOf(request.owner)
        // The tree is read on every refresh and after a reconnect: a direction's open tasks change without the
        // leaders changing, and the tree is also where the order and names come from (#4352).
        val fresh = decode(data, request)
        val held = current.value.takeIf { it.managerId == request.owner }
        val info = directionsOf(request, held?.let {
            LeaderInfo(it.leaders, it.directionNames, it.directionTree, it.externalDirections)
        } ?: LeaderInfo.EMPTY)
        val pins = if (preferences != null) preferences.preferences.value.pins.sessions.mapNotNull(::sessionPath).toSet()
            else request.transport.pins().array("sessions").mapNotNull { (it as? JsonPrimitive)?.contentOrNull?.let(::sessionPath) }.toSet()
        lock.withLock {
            if (!selected(request) || version != refreshVersion) return
            val snapshot = fresh.copy(leaders = info.ids ?: fresh.leaders, directionNames = info.names,
                directionTree = info.tree, externalDirections = info.external)
            cached[request.key] = snapshot
            current.value = snapshot
            currentPins.value = pins
            snapshots.save(request.key, withDirectionCache(data, snapshot))
        }
    }

    override suspend fun startConversation(): SessionKey = start(null)

    override suspend fun startConversation(launcher: String): SessionKey = start(persona.core.contracts.ModelPick(launcher))

    override suspend fun startConversation(pick: persona.core.contracts.ModelPick): SessionKey = start(pick)

    override suspend fun mainConversationOrNull(manager: String): SessionKey? {
        val held = current.value
        if (held.managerId == manager) return held.mainSession
        val captured = request()
        val named = captured.copy(choice = captured.choice.copy(
            manager = manager.takeUnless { it == captured.choice.defaultId },
        ))
        val config = named.transport.assistantOf(named.manager)
        check(connection.current.value === named.transport) { CoreText.of(Key.ConnectionSwitched).shown }
        return mainSessionOf(config)?.also { noteConversation(it.sessionId, named) }
    }

    override suspend fun mainConversationOf(manager: String): SessionKey {
        val captured = request()
        val request = captured.copy(choice = captured.choice.copy(
            manager = manager.takeUnless { it == captured.choice.defaultId },
        ))
        return creationLock.withLock {
            val config = request.transport.assistantOf(manager)
            check(connection.current.value === request.transport) { CoreText.of(Key.ConnectionSwitched).shown }
            mainSessionOf(config)?.let { main ->
                noteConversation(main.sessionId, request)
                return@withLock main
            }
            create(request, true, null, config)
        }
    }

    override suspend fun spaceConversations(space: String): List<Session> {
        val transport = requireNotNull(connection.current.value) { CoreText.of(Key.PairHubFirst).shown }
        val data = transport.assistantIn(space)
        check(connection.current.value === transport) { CoreText.of(Key.ConnectionSwitched).shown }
        return spaceConversationsOf(data, space)
    }

    override suspend fun startConversationIn(space: String): SessionKey {
        val transport = requireNotNull(connection.current.value) { CoreText.of(Key.PairHubFirst).shown }
        val result = transport.createConversationIn(space)
        val session = JsonCodec.decodeFromJsonElement<Session>(result.getValue("session"))
        check(connection.current.value === transport) { CoreText.of(Key.ConnectionSwitched).shown }
        return session.key
    }

    private suspend fun start(pick: persona.core.contracts.ModelPick?): SessionKey {
        // Capture identity before waiting behind another creation.
        val request = request()
        return creationLock.withLock {
            val config = request.transport.assistantOf(request.owner)
            check(connection.current.value === request.transport) { CoreText.of(Key.ConnectionSwitched).shown }
            create(request, config.string("main").isEmpty(), pick, config)
        }
    }

    private suspend fun create(request: Request, isMain: Boolean, pick: persona.core.contracts.ModelPick?, config: JsonObject): SessionKey {
        // A model or an effort goes with the launcher only (the new-chat page's panel); a launcher alone as before.
        val result = if (pick != null && (pick.model != null || pick.effort != null)) request.transport.createConversationOf(request.owner, isMain, pick)
            else request.transport.createConversationOf(request.owner, isMain, pick?.launcher)
        val session = JsonCodec.decodeFromJsonElement<Session>(result.getValue("session"))
        check(connection.current.value === request.transport) { CoreText.of(Key.ConnectionSwitched).shown }
        noteConversation(session.sessionId, request)
        lock.withLock {
            val previous = cached[request.key]?.copy(manager = request.manager) ?: decode(config, request)
            val snapshot = previous.copy(
                mainSession = if (isMain) session.key else previous.mainSession,
                conversations = (listOf(session) + previous.conversations).distinctBy { it.key },
            )
            cached[request.key] = snapshot
            if (selected(request)) {
                ++refreshVersion
                current.value = snapshot
            }
            snapshots.save(request.key, withDirectionCache(buildJsonObject {
                put("machine_id", snapshot.mainSession?.machineId ?: session.machineId)
                put("main", snapshot.mainSession?.sessionId.orEmpty())
                put("conversations", JsonArray(snapshot.conversations.map { JsonCodec.encodeToJsonElement(it) }))
                snapshot.backgroundSupervisor?.let { put("background_supervisor", it) }
            }, snapshot))
        }
        return session.key
    }

}

private fun mainSessionOf(data: JsonObject): SessionKey? {
    val machine = data.string("machine_id")
    val main = data.string("main")
    return if (machine.isNotEmpty() && main.isNotEmpty()) SessionKey(machine, main) else null
}

/**
 * A space's chats from `GET /api/v1/assistant?space=` (docs/assistant/health.md): each named for [space] and its machine
 * even where the hub leaves them out (the list's `machine_id`, as iOS does).
 */
internal fun spaceConversationsOf(data: JsonObject, space: String): List<Session> {
    val machine = data.string("machine_id")
    return data.array("conversations").map { JsonCodec.decodeFromJsonElement<Session>(it) }.map {
        it.copy(space = it.space ?: space, machineId = it.machineId.ifEmpty { machine })
    }
}

/** `assistant.leaders`: the leader conversations' ids in the hub's order, never the main one. */
internal fun leadersOf(data: JsonObject): List<String> {
    val main = data.string("main")
    return data.array("leaders").mapNotNull { (it as? JsonPrimitive)?.contentOrNull?.takeIf { id -> id.isNotEmpty() && id != main } }
}

/**
 * `background_supervisor` of `GET /api/v1/assistant` (#4927): the built-in background supervisor's conversation id, null
 * until the hub has made it and from a hub without it. Never the main conversation's.
 */
internal fun backgroundSupervisorOf(data: JsonObject): String? =
    data.string("background_supervisor").takeIf { it.isNotEmpty() && it != data.string("main") }

/**
 * Whether [updates] bring a conversation that may be the hub's background supervisor and that [before] does not name as
 * it: an assistant conversation the hub opened itself with no task behind it (`origin: "task"`, no `origin_ref`,
 * docs/assistant/unified-entry.md "Background supervisor"), either new to the list or just retitled. The hub starts the
 * conversation first, then records it as the background supervisor, then titles it, so the retitling is the update
 * after which `GET /api/v1/assistant` is sure to name it.
 */
internal fun mayBeNewBackgroundSupervisor(before: AssistantSnapshot, updates: List<Session>): Boolean {
    val listed = before.conversations.associateBy { it.key }
    return updates.any {
        it.assistant == true && it.sessionId != before.backgroundSupervisor &&
            it.origin == "task" && it.originRef.isNullOrBlank() &&
            listed[it.key].let { known -> known == null || known.title != it.title }
    }
}

private const val DIRECTION_CACHE = "_persona_direction_cache"

/** The drawer's direction metadata inside the existing assistant snapshot; absent from older cache files and wire answers. */
private fun withDirectionCache(data: JsonObject, snapshot: AssistantSnapshot): JsonObject = buildJsonObject {
    data.forEach { (key, value) -> put(key, value) }
    put(DIRECTION_CACHE, buildJsonObject {
        put("order", JsonArray(snapshot.leaders.map(::JsonPrimitive)))
        put("names", buildJsonObject { snapshot.directionNames.forEach { (id, name) -> put(id, name) } })
        put("external", JsonArray(snapshot.externalDirections.map { (id, external) -> buildJsonObject {
            put("id", id)
            put("machine_id", external.machineId)
            put("session_id", external.sessionId)
        } }))
    })
}

internal fun directionLeaderCacheOf(data: JsonObject): List<String>? =
    data.obj(DIRECTION_CACHE)?.array("order")?.mapNotNull { (it as? JsonPrimitive)?.contentOrNull }
        ?.filter { it.isNotEmpty() }

internal fun directionNameCacheOf(data: JsonObject): Map<String, String> =
    data.obj(DIRECTION_CACHE)?.obj("names")?.mapNotNull { (id, name) ->
        (name as? JsonPrimitive)?.contentOrNull?.let { id to it }
    }?.toMap().orEmpty()

internal fun directionExternalCacheOf(data: JsonObject): Map<String, ExternalDirection> =
    data.obj(DIRECTION_CACHE)?.array("external")?.mapNotNull { item ->
        (item as? JsonObject)?.let { external ->
            external.string("id").takeIf { it.isNotEmpty() }?.let { id ->
                id to ExternalDirection(external.string("machine_id"), external.string("session_id"))
            }
        }
    }?.toMap().orEmpty()

/** `GET /api/v1/assistant/leaders`'s names by leader conversation id: current directions with a name only. */
internal fun directionNamesOf(data: JsonObject): Map<String, String> = data.array("leaders").mapNotNull { it as? JsonObject }
    .filter { (it["ended"] as? JsonPrimitive)?.booleanOrNull != true }
    .mapNotNull { l -> l.string("id").takeIf { it.isNotEmpty() }?.let { id -> l.string("name").trim().takeIf { it.isNotEmpty() }?.let { id to it } } }
    .toMap()

/** The leaders fallback's explicit outside directions. Blank names and incomplete targets remain visible. */
internal fun directionExternalOf(data: JsonObject): Map<String, ExternalDirection> =
    data.array("leaders").mapNotNull { it as? JsonObject }
        .filter { (it["ended"] as? JsonPrimitive)?.booleanOrNull != true && it.flag("external") }
        .mapNotNull { leader -> leader.string("id").takeIf { it.isNotEmpty() }?.let { id ->
            id to ExternalDirection(leader.string("machine_id"), leader.string("session_id"))
        } }
        .toMap()

/**
 * `GET /api/v1/assistant/directions/tree` (#4352) as the app keeps it; null without `directions` (an older hub's empty
 * answer). Directions, tasks and agents without an id are left out, except the secretary group: its id is the main
 * conversation's, which is empty on a hub that has none yet, and it must still carry its tasks.
 */
internal fun directionTreeOf(data: JsonObject): DirectionTree? {
    val directions = data["directions"] as? JsonArray ?: return null
    return DirectionTree(directions.mapNotNull { (it as? JsonObject)?.let(::directionOf) })
}

private fun directionOf(data: JsonObject): Direction? {
    val secretary = data.flag("secretary")
    val id = data.string("id")
    if (id.isEmpty() && !secretary) return null
    val counts = data.obj("counts")
    fun count(key: String) = counts?.number(key)?.toInt()?.coerceAtLeast(0) ?: 0
    return Direction(id, data.string("name").trim(), secretary,
        DirectionCounts(count("working"), count("needs_you"), count("stuck"), count("review"), count("planned")),
        data.array("tasks").mapNotNull { task ->
            (task as? JsonObject)?.let { t ->
                t.string("id").takeIf { it.isNotEmpty() }?.let { taskId ->
                    DirectionTask(taskId, t.string("title"), DirectionTaskState.of(t.string("state")),
                        t.array("agents").mapNotNull { (it as? JsonObject)?.let(::directionAgentOf) })
                }
            }
        },
        // #6626: the hub's; a hub without it (it always sends it, false too) spins on a working task, as before
        running = (data["running"] as? JsonPrimitive)?.booleanOrNull ?: (count("working") > 0),
        external = if (!secretary && data.flag("external"))
            ExternalDirection(data.string("machine_id"), data.string("session_id")) else null)
}

private fun directionAgentOf(data: JsonObject): DirectionAgent? {
    val machine = data.string("machine_id")
    val session = data.string("session_id")
    if (machine.isEmpty() || session.isEmpty()) return null
    return DirectionAgent(machine, session, data.string("title"), data.string("agent"), data.string("state"),
        data.array("children").mapNotNull { (it as? JsonObject)?.let(::directionAgentOf) })
}

/**
 * The directions to show: their ids in the hub's order (null: not read, keep the ones we have), their names and the tree
 * they came from (`GET /api/v1/assistant/directions/tree`, or the leaders answer's names alone, which have no tree).
 */
private data class LeaderInfo(
    val ids: List<String>? = null, val names: Map<String, String> = emptyMap(),
    val tree: DirectionTree? = null, val external: Map<String, ExternalDirection> = emptyMap(),
) {
    companion object { val EMPTY = LeaderInfo() }
}

internal fun JsonObject.string(key: String): String = (this[key] as? JsonPrimitive)?.contentOrNull.orEmpty()
internal fun JsonObject.optional(key: String): String? = string(key).takeIf { it.isNotEmpty() }
internal fun JsonObject.array(key: String): JsonArray = this[key] as? JsonArray ?: JsonArray(emptyList())
internal fun JsonObject.obj(key: String): JsonObject? = this[key] as? JsonObject
internal fun JsonObject.flag(key: String): Boolean = (this[key] as? JsonPrimitive)?.booleanOrNull == true
internal fun JsonObject.number(key: String): Long? = (this[key] as? JsonPrimitive)?.longOrNull

private fun sessionPath(path: String): SessionKey? = path.split('/', limit = 2).let {
    if (it.size == 2) SessionKey(it[0], it[1]) else null
}
