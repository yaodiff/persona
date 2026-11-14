package persona.feature.inbox

import kotlinx.coroutines.CancellationException

/** The three outcomes an item page or link can show without confusing a failed read with a missing item. */
sealed interface InboxItemLookupResult {
    data class Found(val record: InboxRecord) : InboxItemLookupResult
    data object Missing : InboxItemLookupResult
    data class Failed(val cause: Exception) : InboxItemLookupResult
}

/** Read a listed, paged-back or single hub item, preserving cancellation for the page's owner. */
suspend fun readInboxItem(repository: InboxRepository, itemId: String): InboxItemLookupResult = try {
    repository.item(itemId)?.let { InboxItemLookupResult.Found(it) } ?: InboxItemLookupResult.Missing
} catch (cancelled: CancellationException) {
    throw cancelled
} catch (e: Exception) {
    InboxItemLookupResult.Failed(e)
}
