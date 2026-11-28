package persona.feature.inbox

import androidx.annotation.StringRes
import persona.core.contracts.generated.InboxStoredSources

/** Whether an existing source entry or empty-state mention stays visible (#17399). Unknown means visible. */
data class InboxSourceVisibility(val lark: Boolean, val mail: Boolean)

fun inboxSourceVisibility(larkOffered: Boolean, mailOffered: Boolean, stored: InboxStoredSources?): InboxSourceVisibility =
    InboxSourceVisibility(
        lark = larkOffered || (stored?.lark ?: true),
        mail = mailOffered || (stored?.mail ?: true),
    )

/**
 * Fixed kind-filter values in row order, shared by the Kind filter sheet and the Glass filter strip. Unlike source
 * options these are not built from loaded records, so the Mail entry can show on an empty core-only hub (#17814).
 */
val INBOX_FILTER_KIND_VALUES: List<String> = listOf("mention", "dm", "group", "mail", "agent")

/**
 * The fixed kind filters that stay offered. Only Mail follows the single offered/retained visibility decision; every
 * other kind matches item kinds across sources and always stays.
 */
fun visibleInboxFilterKinds(visibility: InboxSourceVisibility): List<String> =
    INBOX_FILTER_KIND_VALUES.filter { it != "mail" || visibility.mail }

/** The zero-record setup sentence. The original both-sources text is unchanged when both remain visible. */
@StringRes
fun inboxEmptyText(visibility: InboxSourceVisibility): Int = when {
    visibility.lark && visibility.mail -> R.string.inbox_empty_text
    visibility.lark -> R.string.inbox_empty_text_lark
    visibility.mail -> R.string.inbox_empty_text_mail
    else -> R.string.inbox_empty_text_generic
}
