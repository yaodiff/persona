package persona.core.data

import persona.core.contracts.*
import persona.core.contracts.api.*
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject

// Fakes the chat repositories' tests share (ChatRepositoriesTest, OutboxMessageSenderTest, the chat sync scenarios).

/** The hub's socket: events pushed by the test, what the app sent. A closed socket ([open] false) sends nothing. */
internal class FakeConnection : HubConnection {
    override val state = MutableStateFlow(ConnectionState.Online)
    override val events = MutableSharedFlow<ServerEvent>()
    override var resumed = false
    override var lastHello: Hello? = null
    override val currentHello = MutableStateFlow<SocketHello?>(null)
    var open = true
    val sent = mutableListOf<JsonObject>()
    override fun reconnect() = Unit
    override fun send(message: JsonObject): Boolean {
        if (!open) return false
        sent += message
        return true
    }
}

internal class FakeSessions : SessionsRepository {
    override val machines = MutableStateFlow<List<Machine>>(emptyList())
    override val sessions = MutableStateFlow<List<Session>>(emptyList())
    override suspend fun refresh() = Unit
}

/** A disk cache holding [cached]; [saved] is what the repository last wrote. */
internal class FakeItemsCache(private val cached: CachedChat) : ChatItemsCache {
    var saved = cached
    override suspend fun load(session: SessionKey) = cached
    override suspend fun save(session: SessionKey, snapshot: CachedChat) { saved = snapshot }
}

/** The hub's HTTP API for chats: each read answered by its lambda. */
internal class FakeChatTransport : ChatTransport {
    var list = emptyList<Session>()
    var machineList = emptyList<Machine>()
    var listFetch: (suspend () -> List<Session>)? = null
    var pending = emptyList<Interaction>()
    var pendingFetch: (suspend () -> List<Interaction>)? = null
    var fetch: suspend (ItemsQuery) -> ItemsResponse = { error("Unexpected items request") }
    var resolved: String? = null
    var handoffCard: Boolean? = null
    var secretRunEnv: Boolean? = null
    var outcome = ResolveOutcome()
    var goneStatus: String? = null
    var fullItemId: String? = null
    override suspend fun machines() = machineList
    override suspend fun sessions() = listFetch?.invoke() ?: list
    override suspend fun items(session: SessionKey, query: ItemsQuery) = fetch(query)
    override suspend fun interactions() = pendingFetch?.invoke() ?: pending
    override suspend fun resolve(id: String, resolution: Resolution) { resolved = id }
    override suspend fun resolveOutcome(id: String, resolution: Resolution): ResolveOutcome {
        resolved = id
        handoffCard = false
        secretRunEnv = false
        goneStatus?.let { goneStatus = null; throw InteractionGoneException(it) }
        return outcome
    }
    override suspend fun resolveOutcome(id: String, resolution: Resolution, handoffCard: Boolean): ResolveOutcome =
        resolveOutcome(id, resolution).also { this.handoffCard = handoffCard }
    override suspend fun resolveOutcome(id: String, resolution: Resolution, handoffCard: Boolean, secretRunEnv: Boolean): ResolveOutcome =
        resolveOutcome(id, resolution, handoffCard).also { this.secretRunEnv = secretRunEnv }
    override suspend fun image(session: SessionKey, itemId: String) = byteArrayOf()
    override suspend fun fullItem(session: SessionKey, itemId: String): Item {
        fullItemId = itemId
        return Item(itemId)
    }
}

/** The outbox's disk, in memory: it outlives the sender, as the app's database outlives a relaunch. */
internal class MemoryOutboxStore : OutboxStore {
    override val entries = MutableStateFlow<List<OutboxEntry>>(emptyList())
    private var main: SessionKey? = null
    private var creationClaimed = false
    override suspend fun mainConversation(): SessionKey? = main
    override suspend fun claimMainCreation(): Boolean {
        if (creationClaimed) return false
        creationClaimed = true
        return true
    }
    override suspend fun bindMainConversation(session: SessionKey) {
        main = session
        entries.value = entries.value.map {
            if (it.mainTarget && it.session == PendingMainConversation) it.copyForDelivery(session = session) else it
        }
    }
    override suspend fun releaseMainCreation() { if (main == null) creationClaimed = false }
    override suspend fun forgetMainConversation(session: SessionKey) {
        if (main != session) return
        main = null
        creationClaimed = false
    }
    val transitions = mutableListOf<Pair<OutboxState, OutboxState>>()
    fun state(id: String) = entries.value.single { it.clientMessageId == id }.state
    override suspend fun enqueue(entry: OutboxEntry) { entries.value += entry }
    override suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?): Boolean {
        val old = entries.value.single { it.clientMessageId == id }
        if (old.state !in from) return false
        transitions += old.state to to
        // As the database: every transition but the one that records a refusal's words clears them.
        entries.value = entries.value.map { if (it == old) it.copyForDelivery(state = to, error = error, reason = null) else it }
        return true
    }
    override suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?, opId: String): Boolean =
        transition(id, from, to, error).also { moved ->
            if (moved) entries.value = entries.value.map { if (it.clientMessageId == id) it.copyForDelivery(opId = opId) else it }
        }
    override suspend fun transition(
        id: String, from: Set<OutboxState>, to: OutboxState, error: String?, opId: String?, reason: String?,
    ): Boolean = (if (opId == null) transition(id, from, to, error) else transition(id, from, to, error, opId)).also { moved ->
        if (moved) entries.value = entries.value.map { if (it.clientMessageId == id) it.copyForDelivery(reason = reason) else it }
    }
    override suspend fun recoverInterrupted() {
        entries.value = entries.value.map { if (it.state == OutboxState.Sending) it.copyForDelivery(state = OutboxState.Unknown) else it }
    }
}
