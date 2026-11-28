package persona.core.contracts.api

import persona.core.contracts.CoreTextException
import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText
import persona.core.contracts.*

/** HTTP operations for one paired hub; WebSocket ownership stays with HubConnection. */
interface ChatTransport {
    suspend fun machines(): List<Machine>
    suspend fun sessions(): List<Session>
    suspend fun sessionPage(
        filter: SessionListFilter = SessionListFilter(),
        cursor: String? = null,
        since: String? = null,
    ): SessionListPage = throw SessionListsUnsupported()
    suspend fun sessionCounts(filter: SessionListFilter = SessionListFilter()): SessionListCounts =
        throw SessionListsUnsupported()
    suspend fun items(session: SessionKey, query: ItemsQuery): ItemsResponse
    /** The session's recorded permission mode changes, oldest first; empty where the hub has no such record. */
    suspend fun modeChanges(session: SessionKey): List<ModeChange> = emptyList()
    suspend fun interactions(): List<Interaction>
    suspend fun resolve(id: String, resolution: Resolution)
    /** [resolve], with what the hub said about it (a card answered already carries the answer that took effect). */
    suspend fun resolveOutcome(id: String, resolution: Resolution): ResolveOutcome {
        resolve(id, resolution)
        return ResolveOutcome()
    }
    /** The transport adds the hand-off or env feature header only for this explicit full-card approval. */
    suspend fun resolveOutcome(id: String, resolution: Resolution, handoffCard: Boolean): ResolveOutcome =
        resolveOutcome(id, resolution)
    /** Answers the exact displayed occurrence; older delegated transports retain their ID-only fallback. */
    suspend fun resolveOccurrence(interaction: Interaction, resolution: Resolution, handoffCard: Boolean = false): ResolveOutcome =
        resolveOutcome(interaction.id, resolution, handoffCard)
    /** [secretRunEnv] selects the env feature instead of the hand-off feature, only when [handoffCard] is true. */
    suspend fun resolveOutcome(id: String, resolution: Resolution, handoffCard: Boolean, secretRunEnv: Boolean): ResolveOutcome =
        resolveOutcome(id, resolution, handoffCard)
    /** An authenticated exact reply to a server-owned proposal; never an ordinary model message or card resolution. */
    suspend fun replyOwnerApproval(interaction: Interaction, phrase: String): Unit =
        throw UnsupportedOperationException("Exact owner replies unavailable")
    suspend fun image(session: SessionKey, itemId: String): ByteArray
    suspend fun fullItem(session: SessionKey, itemId: String): Item
    /**
     * Change Answer (POST /api/v1/interactions/{id}/reanswer `{answers}`, with the card's [manager]): the user's answers to a
     * question the assistant answered go to the Agent as a message. Not retried: the hub sends the same answer once.
     */
    suspend fun reanswer(id: String, answers: Map<String, String>, manager: String?): Unit =
        throw UnsupportedOperationException("Change Answer unavailable")
}

data class ItemsQuery(
    val beforeSeq: Int? = null,
    val afterSeq: Int? = null,
    val fromSeq: Int? = null,
    val throughSeq: Int? = null,
) {
    init {
        require(listOfNotNull(beforeSeq, afterSeq, fromSeq, throughSeq).all { it >= 0 })
        require((fromSeq == null) == (throughSeq == null))
        require(fromSeq == null || throughSeq!! >= fromSeq)
        require(listOf(beforeSeq != null, afterSeq != null, fromSeq != null).count { it } <= 1)
    }
}

/**
 * The hub's 200 to an answer (docs/protocol/protocol.md, #1673): sent now, or [already] answered, [resolution] being the answer
 * that took effect then (redacted as the history keeps it; it can differ from the one just sent).
 */
data class ResolveOutcome(val already: Boolean = false, val status: String? = null, val resolution: Resolution? = null)

/**
 * A request that is over (410) must disappear from the pending list. [status] is how it ended — `expired`,
 * `cancelled` or `dismissed` — or null when the hub could not tell (or is too old to say).
 */
class InteractionGoneException(val status: String? = null) : CoreTextException(CoreText.of(when (status) {
    "expired" -> Key.InteractionExpired
    "cancelled" -> Key.InteractionCancelled
    "dismissed" -> Key.InteractionDismissed
    else -> Key.InteractionGone
}))
