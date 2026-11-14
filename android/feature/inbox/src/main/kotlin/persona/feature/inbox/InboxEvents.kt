package persona.feature.inbox

import androidx.annotation.StringRes
import androidx.navigation3.runtime.NavKey
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import persona.core.contracts.InboxChainEntry
import persona.core.contracts.InboxHandling
import persona.core.contracts.InboxSuggestion
import persona.core.contracts.SessionKey
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.Status
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import persona.core.navigation.ChatRoute
import persona.core.navigation.RouteSource
import java.time.Instant
import java.time.ZoneId
import java.util.Locale

// Inbox phase 1 (docs/assistant/inbox.md"Events, handling chains, and suggestions", docs/android/android.md"Inbox: events, suggestions, and handling chains"): the pure parts of
// how an event reads in the list and on its page. Older items have no event sections; chat and mail still open the detail
// so their originals stay unmounted until an explicit source action.

/** An item's detail page: event sections when present, with "View original" opening the source above it. */
@Serializable @SerialName("persona.feature.inbox.InboxEventRoute")
data class InboxEventRoute(val itemId: String) : NavKey

/**
 * Opening [record] from the list: its detail for event fields or a chat/mail original, so merely opening it cannot
 * acknowledge the source. Other legacy items retain their own pages. Explicit source links still use [pages] directly.
 */
fun inboxRowPage(record: InboxRecord, pages: InboxItemPages): NavKey {
    if (record.event.present) return InboxEventRoute(record.item.id)
    return when (inboxItemPage(record)) {
        is LarkRoute, is MailRoute -> InboxEventRoute(record.item.id)
        else -> pages.open(record)
    }
}

/** The words for a processing conversation that is on it (In progress): AI is reviewing, Agent is working, Waiting for reply. */
@StringRes
internal fun handlingWord(handling: InboxHandling?): Int? = when (handling?.state) {
    "looking" -> R.string.inbox_handling_looking
    "agent" -> R.string.inbox_handling_agent
    "waiting" -> R.string.inbox_handling_waiting
    else -> null
}

/** What an open event's row says under its preview, besides its status mark. */
internal sealed interface EventMark {
    /** In In progress: who is on it ([word]). */
    data class Working(@StringRes val word: Int) : EventMark
    /** Urgent in Needs you while the manager prepares its suggestion. */
    data object Preparing : EventMark
    /** The suggestion's kind and the first line of its text. */
    data class Suggests(@StringRes val kind: Int, val line: String) : EventMark
    /** The manager failed or timed out: back in Needs you without a suggestion. */
    data class Failed(val reason: String?) : EventMark
    /** The shared `now` words; an unknown key never reaches this variant and keeps the old mark. */
    data class Now(val text: String, val status: Status, val problem: Boolean = false, val key: String? = null) : EventMark
}

/** The processing conversation, from the handling record or its handling-chain entry, when the hub gave both ids. */
internal fun inboxProcessingSession(record: InboxRecord): SessionKey? {
    record.event.handling?.let {
        if (!it.machineId.isNullOrBlank() && !it.sessionId.isNullOrBlank()) return SessionKey(it.machineId!!, it.sessionId!!)
    }
    val entry = record.event.chain.lastOrNull {
        it.kind == "handling" && it.link?.let { link ->
            link.type == "session" && !link.machineId.isNullOrBlank() && !link.sessionId.isNullOrBlank()
        } == true
    } ?: return null
    return SessionKey(entry.link!!.machineId!!, entry.link!!.sessionId!!)
}

internal fun eventMark(record: InboxRecord): EventMark? {
    val item = record.item
    if (item.isDone) return null
    val event = record.event
    event.suggestion?.let { return EventMark.Suggests(suggestionKindLabel(it), firstLine(it.text)) }
    if (item.isWorking) return EventMark.Working(handlingWord(event.handling) ?: R.string.inbox_handling_looking)
    val handling = event.handling
    if (handling?.state == "failed") return EventMark.Failed(handling.error?.takeIf { it.isNotBlank() })
    if (item.urgent && handling?.state == "looking") return EventMark.Preparing
    return null
}

internal fun firstLine(text: String): String = text.trim().lineSequence().firstOrNull { it.isNotBlank() }?.trim().orEmpty()

/** The suggestion card's title: what it suggests (a handback one needs the user's judgment, whatever its kind). */
@StringRes
internal fun suggestionKindLabel(suggestion: InboxSuggestion): Int = when {
    suggestion.handback -> R.string.inbox_suggestion_handback
    suggestion.kind == "reply" -> R.string.inbox_suggestion_reply
    suggestion.kind == "agent" -> R.string.inbox_suggestion_agent
    suggestion.kind == "conclusion" -> R.string.inbox_suggestion_conclusion
    else -> R.string.inbox_suggestion_handback
}

/**
 * The card's buttons: [confirm]'s label, and whether it can be changed (Edit opens the editor with the text and
 * "Say something"). reply Send · Edit · Reject, agent Start · Edit · Reject, conclusion Got it · Reject, handback Confirm · Edit · Reject.
 */
internal data class SuggestionButtons(@StringRes val confirm: Int, val editable: Boolean)

internal fun suggestionButtons(suggestion: InboxSuggestion): SuggestionButtons = when {
    suggestion.handback -> SuggestionButtons(R.string.inbox_suggestion_confirm, true)
    suggestion.kind == "reply" -> SuggestionButtons(R.string.inbox_suggestion_send, true)
    suggestion.kind == "agent" -> SuggestionButtons(R.string.inbox_suggestion_start, true)
    suggestion.kind == "conclusion" -> SuggestionButtons(R.string.inbox_suggestion_ack, false)
    else -> SuggestionButtons(R.string.inbox_suggestion_confirm, true)
}

/**
 * The confirm request for [suggestion] with the text the user leaves in the editor: the edited text only when it
 * differs from the suggestion's (the hub records Edited then), else none (run exactly as shown).
 */
internal fun confirmText(suggestion: InboxSuggestion, edited: String?): String? =
    edited?.trim()?.takeIf { it.isNotEmpty() && it != suggestion.text.trim() }

/** The chain as shown: the three newest entries while collapsed, else all of it, oldest of the shown entries first. */
internal fun shownChain(chain: List<InboxChainEntry>, expanded: Boolean): List<InboxChainEntry> =
    if (expanded || chain.size <= 3) chain else chain.takeLast(3)

/** What tapping a chain entry does. */
internal sealed interface ChainTarget {
    data class Page(val key: NavKey) : ChainTarget
    /** The item's own source page (Feishu chat, mail, connector thread, the extension's page): what "View original" opens. */
    data object Source : ChainTarget
    /** A connector's link, opened in the in-app browser. */
    data class Url(val url: String) : ChainTarget
    /** Another inbox item. */
    data class Item(val id: String) : ChainTarget
    /** It opens its [InboxChainEntry.detail] / [InboxChainEntry.messages] in place. */
    data object Inline : ChainTarget
    /** Nothing more to show. */
    data object None : ChainTarget
}

/** A conversation opened from this item; its source lets the shell keep Back pointed at the item. */
internal fun inboxConversationRoute(machineId: String, sessionId: String, itemId: String) =
    ChatRoute(machineId, sessionId, source = RouteSource.InboxItem(itemId))

internal fun chainTarget(entry: InboxChainEntry, record: InboxRecord): ChainTarget {
    val inline = if (!entry.detail.isNullOrBlank() || entry.messages.isNotEmpty()) ChainTarget.Inline else ChainTarget.None
    val link = entry.link ?: return inline
    return when (link.type) {
        "source" -> {
            val position = link.positionValue
            when {
                position != null && record.feedId != null -> ChainTarget.Page(LarkRoute(record.feedId, record.item.id, position))
                !link.url.isNullOrBlank() -> ChainTarget.Url(link.url!!)
                link.uid != null && record.isMail && record.doc == null -> ChainTarget.Page(MailRoute(record.item.id))
                else -> ChainTarget.Source
            }
        }
        "session" -> if (!link.machineId.isNullOrBlank() && !link.sessionId.isNullOrBlank())
            ChainTarget.Page(inboxConversationRoute(link.machineId!!, link.sessionId!!, record.item.id)) else inline
        "item" -> link.id?.takeIf { it.isNotBlank() }?.let { ChainTarget.Item(it) } ?: inline
        else -> inline
    }
}

/** A chain entry's icon by its kind; a kind this app does not know gets a plain one. */
internal fun chainIcon(kind: String, record: InboxRecord): PhIcon = when (kind) {
    "received" -> if (record.isMail) PhIcons.EnvelopeSimple else PhIcons.ChatCircleDots
    "triaged" -> PhIcons.FunnelSimple
    "handling" -> PhIcons.Sparkle
    "sent" -> PhIcons.PaperPlaneTilt
    "agent" -> PhIcons.Robot
    "confirmed" -> PhIcons.CheckCircle
    "rejected" -> PhIcons.XCircle
    "instructed" -> PhIcons.Quotes
    "self_replied" -> PhIcons.ArrowBendUpLeft
    "failed" -> PhIcons.WarningCircle
    "concluded" -> PhIcons.Lightbulb
    // Filed (Done) by the manager or the user; not in the contract table, drawn like iOS does.
    "done" -> PhIcons.Checks
    else -> PhIcons.Info
}

/** A chain entry's time: the 24-hour clock today, the localized short date and 24-hour clock before. */
internal fun chainTime(value: String?, locale: Locale, now: Instant = Instant.now(), zone: ZoneId = ZoneId.systemDefault()): String {
    val at = value?.let { runCatching { Instant.parse(it) }.getOrNull() } ?: return ""
    val today = at.atZone(zone).toLocalDate() == now.atZone(zone).toLocalDate()
    return LocalizedFormats.format24(at, if (today) LocalizedFormats.TIME_24 else LocalizedFormats.MONTH_DAY_TIME_24, locale, zone)
}
