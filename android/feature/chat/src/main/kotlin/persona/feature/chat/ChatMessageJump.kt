package persona.feature.chat

import androidx.compose.runtime.*
import androidx.compose.ui.platform.LocalResources
import persona.core.chatlist.ChatListEntry
import persona.core.chatlist.ChatListState
import persona.core.chatlist.scrollToEntry
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatRepository
import persona.core.domain.lines.ChatLine
import persona.core.navigation.MessageJumps
import kotlinx.coroutines.CancellationException

/** How many older pages a hub link's message is looked for in before the chat gives up and stays at the newest. */
internal const val MESSAGE_JUMP_PAGE_LIMIT = 20

/** The next thing to do to land on a hub link's message (`?at=`, #2194). */
internal sealed interface MessageJumpStep {
    /** The message's row is drawn: scroll to it and light it up. */
    data class Scroll(val rowId: String) : MessageJumpStep
    /** The message sits in a closed work line: open it first. */
    data class Expand(val groups: Set<String>) : MessageJumpStep
    /** Further up than what is loaded: the next older page. */
    data object LoadOlder : MessageJumpStep
    /** Not in this conversation, or not within [MESSAGE_JUMP_PAGE_LIMIT] pages. */
    data object NotFound : MessageJumpStep
}

private data class MessageJumpSnapshot(
    val lines: List<ChatLine>, val expanded: Set<String>, val hasOlder: Boolean, val ready: Boolean, val pages: Int,
)

/** Where [itemId] is in [lines] (as chat search finds a hit), given what is open and what is left to load. */
internal fun messageJumpStep(lines: List<ChatLine>, itemId: String, expanded: Set<String>, hasOlder: Boolean, pages: Int): MessageJumpStep {
    val target = searchTarget(lines, itemId) ?: return if (hasOlder && pages < MESSAGE_JUMP_PAGE_LIMIT) MessageJumpStep.LoadOlder
        else MessageJumpStep.NotFound
    if (!expanded.containsAll(target.expand)) return MessageJumpStep.Expand(expanded + target.expand)
    return MessageJumpStep.Scroll(target.rowId)
}

/**
 * Lands on the message a hub link named for this conversation ([MessageJumps]): taken when the page opens or, already
 * open, when the link is tapped; brought to the top and lit up like a search hit ([ChatSearchState.highlight]). Not
 * found, the chat stays where it is (the newest) and says so in [onNotice].
 */
@Composable
internal fun ChatMessageJump(
    key: SessionKey, repository: ChatRepository, list: ChatListState, lines: List<ChatLine>,
    hasOlder: Boolean, loading: Boolean, search: ChatSearchState, onItem: (String?) -> Unit, onNotice: (String) -> Unit,
) {
    val res = LocalResources.current
    var item by remember { mutableStateOf<String?>(null) }
    var pages by remember { mutableIntStateOf(0) }
    val requested = MessageJumps.pending
    LaunchedEffect(requested, key) {
        MessageJumps.take(key.machineId, key.sessionId)?.let { item = it; pages = 0; onItem(it) }
    }
    fun done() { item = null; onItem(null) }
    val currentLines by rememberUpdatedState(lines)
    val currentHasOlder by rememberUpdatedState(hasOlder)
    val currentLoading by rememberUpdatedState(loading)
    LaunchedEffect(item, key, repository, list, search) {
        val id = item ?: return@LaunchedEffect
        // Loading and lines change during a jump's own page request. Observe them without restarting (and cancelling)
        // that request; the suspending collector still waits for an unrelated load and prevents overlapping pages.
        snapshotFlow {
            MessageJumpSnapshot(currentLines, list.expanded, currentHasOlder,
                ready = list.restored && !currentLoading, pages = pages)
        }.collect { snapshot ->
            if (!snapshot.ready) return@collect
            when (val step = messageJumpStep(snapshot.lines, id, snapshot.expanded, snapshot.hasOlder, snapshot.pages)) {
                MessageJumpStep.LoadOlder -> {
                    pages++
                    try { repository.loadOlder(key) } catch (e: CancellationException) { throw e }
                    catch (_: Exception) { onNotice(res.getString(R.string.chat_link_message_gone)); done() }
                }
                MessageJumpStep.NotFound -> { onNotice(res.getString(R.string.chat_link_message_gone)); done() }
                is MessageJumpStep.Expand -> list.setExpanded(step.groups)
                is MessageJumpStep.Scroll -> {
                    if (snapshot.lines.none { it.contentKey == step.rowId }) return@collect
                    withFrameNanos { }
                    if (list.scrollToEntry(ChatListEntry(step.rowId), alignTop = true)) {
                        search.highlight = step.rowId
                        done()
                    }
                }
            }
        }
    }
}
