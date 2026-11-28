package persona.core.inbox

/**
 * Whether the text the hub stored for a mail is the whole newest message (#15995), from the optional
 * `mail_body_truncated` field of `GET /api/v1/inbox/{id}` (only that route carries it, beside `context`):
 *  - [Clipped]: the hub cut the newest mail's text to the 3,000 characters it stores (`true`);
 *  - [Whole]: the stored text is all of it (`false`);
 *  - [Unknown]: the hub did not say — the field is absent or not a boolean, an older hub, an item stored before the
 *    flag, a newer mail whose text was not read yet, a mail with no plain text, or a row that also keeps merged
 *    members' text.
 *
 * Absent is read as not known, never as [Whole]. Pages take this closed type, never the wire's `Boolean?`.
 */
enum class StoredMailClipping {
    Clipped,
    Whole,
    Unknown,
    ;

    companion object {
        /** The wire's optional boolean as the closed set; `null` (absent or not a boolean) is [Unknown]. */
        fun fromHub(mailBodyTruncated: Boolean?): StoredMailClipping = when (mailBodyTruncated) {
            true -> Clipped
            false -> Whole
            null -> Unknown
        }
    }
}

/**
 * The `context` of `GET /api/v1/inbox/{id}` as the repository hands it to pages: the stored [text] (null while it has
 * not loaded or when nothing was kept) and the hub's [mailClipping] claim about a mail's text. The claim is
 * [StoredMailClipping.Unknown] for non-mail items, cached copies written before the flag and every failure fallback.
 */
data class PersonaContext(
    val text: String?,
    val mailClipping: StoredMailClipping = StoredMailClipping.Unknown,
)
