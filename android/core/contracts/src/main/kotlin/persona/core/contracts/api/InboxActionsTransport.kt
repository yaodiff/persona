package persona.core.contracts.api

/**
 * The `context` field of `GET /api/v1/inbox/{id}` and the optional `mail_body_truncated` beside it (#15995).
 * [mailBodyTruncated] stays the raw wire tri-state: `true` clipped, `false` whole, null absent or not a JSON boolean;
 * the data layer maps it once to the closed `StoredMailClipping` enum before any page sees it.
 */
data class InboxItemContext(
    val text: String,
    val mailBodyTruncated: Boolean? = null,
)

/** Additional inbox reads and explicit collection, bound to one pairing. */
interface InboxActionsTransport {
    suspend fun collectNow()
    suspend fun context(itemId: String): InboxItemContext

    /**
     * Asks the hub to draft a reply in the user's voice (`POST /api/v1/inbox/{id}/draft`). [instruction] may be
     * empty; the text comes back for the user to edit and is never sent by this call.
     */
    suspend fun draft(itemId: String, instruction: String): String =
        throw UnsupportedOperationException("Reply drafting unavailable")
}
