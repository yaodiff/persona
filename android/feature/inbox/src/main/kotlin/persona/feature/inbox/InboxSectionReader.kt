package persona.feature.inbox

import androidx.compose.runtime.*
import androidx.compose.ui.platform.LocalResources
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import persona.core.domain.inbox.InboxGroup
import persona.core.domain.inbox.InboxRowType

internal class InboxSectionReader(
    val busy: Boolean,
    val notice: String?,
    val retry: (() -> Unit)?,
    val hasUnread: (InboxGroup) -> Boolean,
    val read: (InboxGroup) -> Unit,
)

/** Read the actual hub-section items, including folded ones. Read receipts never file pending items. */
@Composable
internal fun rememberInboxSectionReader(
    controller: InboxConversations?,
    conversations: InboxConversationList,
    records: Map<String, InboxRecord>,
): InboxSectionReader {
    val res = LocalResources.current
    val scope = rememberCoroutineScope()
    val rows = remember(conversations) { conversations.rows.associateBy { it.key } }
    var busy by remember(controller) { mutableStateOf(false) }
    var notice by remember(controller) { mutableStateOf<String?>(null) }
    var retry by remember(controller) { mutableStateOf<(() -> Unit)?>(null) }

    fun row(record: InboxRecord): InboxConversationRow? =
        controller?.let { inboxConversationKey(it.scope, record) }?.let(rows::get)

    // A source receipt is conversation-wide, but whether this item still needs one is reconciled independently of its hub section.
    fun unread(group: InboxGroup): List<InboxRecord> = group.rows.filter { it.type == InboxRowType.Item }
        .mapNotNull { records[it.id] }.filter { !it.read && row(it)?.unread != false }

    fun read(keys: Set<InboxConversationKey>, unsupported: Boolean) {
        if (busy) return
        if (controller == null) { notice = res.getString(R.string.inbox_conversations_read_skipped); return }
        busy = true
        retry = null
        scope.launch {
            try {
                // A hub Action section can contain an item whose original tier was FYI, so accept both unread row states.
                val result = controller.readAll(setOf(InboxConversationSection.Action, InboxConversationSection.Fyi), keys)
                val failed = result.skipped.toSet() + result.failed.map { it.key }
                notice = res.getString(when {
                    failed.isNotEmpty() -> R.string.inbox_conversations_read_failed
                    unsupported -> R.string.inbox_conversations_read_skipped
                    else -> R.string.inbox_conversations_read_success
                })
                if (failed.isNotEmpty()) retry = { read(failed, false) }
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) {
                notice = res.getString(R.string.inbox_conversations_read_failed)
                retry = { read(keys, unsupported) }
            } finally { busy = false }
        }
    }

    return InboxSectionReader(busy, notice, retry,
        hasUnread = { group -> unread(group).isNotEmpty() },
        read = { group ->
            val pending = unread(group)
            val keys = pending.mapNotNull { record ->
                controller?.let { inboxConversationKey(it.scope, record) }?.takeIf { rows[it]?.unread == true }
            }.toSet()
            read(keys, pending.any { row(it) == null })
        },
    )
}
