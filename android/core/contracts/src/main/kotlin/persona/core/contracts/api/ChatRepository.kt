package persona.core.contracts.api

import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import kotlinx.coroutines.flow.Flow

data class ChatSnapshot(
    val items: List<Item> = emptyList(),
    val loading: Boolean = true,
    val hasOlder: Boolean = false,
    val loadingOlder: Boolean = false,
    val error: String? = null,
    val ended: Boolean = false,
    val replaced: Boolean = false,
    val stopping: Boolean = false,
    val verified: Boolean = true,
    val lastSeenMessageId: String? = null,
    val turnDurationSeconds: Long? = null,
    val streamingItemIds: Set<String> = emptySet(),
    val history: String? = null,
    val recordedTurnDurations: Map<String, Long> = emptyMap(),
)

interface ChatRepository {
    fun observe(session: SessionKey): Flow<ChatSnapshot>
    suspend fun loadOlder(session: SessionKey)
    suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item>

    /**
     * Items in `[fromSeq, throughSeq]` for search in chat, without opening the fold in the transcript
     * (`GET …/items?from_seq=&through_seq=`, the same pages [expandFold] reads). Empty where a repository
     * cannot read a range on its own.
     */
    suspend fun loadRange(session: SessionKey, fromSeq: Int, throughSeq: Int): List<Item> = emptyList()

    /** Implementations fetch authenticated bytes; presentation owns decoding and caching. */
    suspend fun loadImage(session: SessionKey, itemId: String): ByteArray =
        throw UnsupportedOperationException("Image unavailable")

    suspend fun fullText(session: SessionKey, itemId: String): String =
        throw UnsupportedOperationException("Full text unavailable")

    /** Called only after foreground, visibility and verified-window gates pass. */
    suspend fun markSeen(session: SessionKey, itemId: String) = Unit
}
