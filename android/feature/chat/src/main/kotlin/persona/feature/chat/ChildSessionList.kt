package persona.feature.chat

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.launch
import persona.core.contracts.*
import persona.core.contracts.api.SessionListExpired
import persona.core.contracts.api.SessionListPage
import persona.core.domain.sort.childSessions

/** Held rows count toward coverage; a settled, complete list needs no automatic read on resume. */
internal fun needsChildSessionRead(parent: Session?, known: List<Session>): Boolean {
    if (parent == null) return false
    val children = childSessions(known, parent.key)
    return children.size != (parent.children?.total ?: 0) ||
        (parent.children?.working ?: 0) > 0 || (parent.children?.needsYou ?: 0) > 0 ||
        children.any { it.agentState in setOf(AgentState.Working, AgentState.NeedsYou) }
}

internal data class ChildSessionSnapshot(val rows: List<Session> = emptyList(), val revision: String? = null, val loaded: Boolean = false)

/** Once a baseline commits, its membership wins over stale link metadata and old local rows. */
internal fun childSessionRows(parent: SessionKey, local: List<Session>, held: ChildSessionSnapshot, lookups: List<Session>): List<Session> {
    val byKey = local.associateBy { it.key }
    val rows = if (!held.loaded) local + lookups else held.rows.map { cached ->
        byKey[cached.key]?.takeIf { (it.rev ?: 0L) >= (cached.rev ?: 0L) } ?: cached
    }
    return childSessions(rows.distinctBy { it.key }, parent)
}

/** Page-local baseline/checkpoint. Events invalidate it; no timer downloads finished children again. */
internal class ChildSessionList(private val parent: SessionKey) {
    private val held = MutableStateFlow(ChildSessionSnapshot())
    val state = held.asStateFlow()
    val revision: String? get() = held.value.revision

    suspend fun refresh(read: suspend (String?) -> SessionListPage) {
        var since = revision
        val page = try { read(since) } catch (_: SessionListExpired) { since = null; read(null) }
        currentCoroutineContext().ensureActive()
        val removals = page.remove.map { encoded ->
            val key = JsonCodec.decodeFromString<List<String>>(encoded)
            require(key.size == 2) { "Invalid session removal key" }
            SessionKey(key[0], key[1])
        }.toSet()
        val changes = childSessions(page.sessions, parent).associateBy { it.key }
        val kept = if (since == null) emptyList() else held.value.rows.filter { it.key !in removals && it.key !in changes }
        val next = childSessions(kept + changes.values, parent)
        // read() returns only after every frozen cursor page succeeded. A failure never advances this checkpoint.
        held.value = ChildSessionSnapshot(next, page.revision.takeIf(String::isNotBlank), loaded = true)
    }

    suspend fun watch(events: Flow<String>, needsRead: () -> Boolean, read: suspend (String?) -> SessionListPage) {
        channelFlow<String?> {
            // Subscribe before the initial HTTP read so invalidations during it remain queued.
            launch(start = CoroutineStart.UNDISPATCHED) { events.collect { send(it) } }
            if (needsRead()) send(null)
        }.conflate().collect { hint ->
            if (hint != null && hint == revision) return@collect
            try { refresh(read) }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { /* Preserve the rows and checkpoint until the next invalidation or resume. */ }
        }
    }
}
