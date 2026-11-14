package persona.feature.chat

import androidx.compose.runtime.*
import androidx.compose.ui.platform.LocalResources
import persona.core.chatlist.*
import persona.core.contracts.Item
import persona.core.domain.lines.ChatLine
import kotlinx.coroutines.delay

internal val LocalChatSearchItem = staticCompositionLocalOf<String?> { null }

@Stable
internal class ChatSearchState {
    var visible by mutableStateOf(false)
    var query by mutableStateOf("")
    var origin by mutableStateOf<ChatReadingPosition?>(null)
    var selected by mutableStateOf<ChatSearchHit?>(null)
    var pending by mutableStateOf(false)
    var returning by mutableStateOf(false)
    var highlight by mutableStateOf<String?>(null)
    var notice by mutableStateOf<String?>(null)
    /** Steps of folds this search fetched without opening them, plus which fold each belongs to. */
    var foldItems by mutableStateOf<List<Item>>(emptyList())
        private set
    var foldOfItem by mutableStateOf<Map<String, String>>(emptyMap())
        private set
    var foldsLoading by mutableStateOf(false)
    var foldsPartial by mutableStateOf(false)
    var foldCacheKey = ""
        private set
    private val fetchedFolds = mutableMapOf<String, Pair<Int, List<Item>>>()
    private var foldSpent = 0

    /** Drops a previous chat's folds when the session or its history file changes. */
    fun resetFolds(key: String) {
        if (foldCacheKey == key) return
        foldCacheKey = key
        fetchedFolds.clear()
        foldSpent = 0
        foldsPartial = false
        publishFolds()
    }

    fun needsFold(fold: Item): Boolean = fetchedFolds[fold.id]?.first != (fold.fold?.throughSeq ?: -1)

    fun acceptFold(fold: Item, items: List<Item>) {
        fetchedFolds[fold.id]?.second?.let { foldSpent -= foldItemsSize(it) }
        fetchedFolds[fold.id] = (fold.fold?.throughSeq ?: -1) to items
        foldSpent += foldItemsSize(items)
        publishFolds()
    }

    fun foldBudgetLeft(): Int = (FOLD_SEARCH_BUDGET - foldSpent).coerceAtLeast(0)

    private fun publishFolds() {
        foldItems = fetchedFolds.values.flatMap { it.second }
        val owners = HashMap<String, String>()
        for ((id, stored) in fetchedFolds) for (item in stored.second) owners[item.id] = id
        foldOfItem = owners
    }

    fun open(list: ChatListState, freeze: () -> Unit) {
        if (origin == null) origin = list.readingPosition()
        list.holdPosition()
        freeze()
        visible = true
    }

    fun choose(hit: ChatSearchHit) {
        selected = hit
        pending = true
        visible = false
        notice = null
    }

    fun close(list: ChatListState) {
        visible = false
        if (selected == null) returnToOrigin(list)
    }

    fun returnToOrigin(list: ChatListState) {
        list.setExpanded(origin?.expanded ?: list.position.expanded)
        selected = null
        highlight = null
        pending = false
        returning = true
    }
}

/** Reveal background-wake drawers in the same lazy list instead of introducing a second scroller. */
internal fun searchVisibleLines(lines: List<ChatLine>, itemId: String?): List<ChatLine> = lines.flatMap { line ->
    if (line is ChatLine.Wakes && itemId != null && searchTarget(line.drawer, itemId) != null)
        listOf(line) + searchVisibleLines(line.drawer, itemId)
    else listOf(line)
}.distinctBy { it.id }

@Composable
internal fun ChatSearchNavigation(
    search: ChatSearchState, list: ChatListState, lines: List<ChatLine>, foldFailed: Set<String> = emptySet(),
) {
    val resources = LocalResources.current
    LaunchedEffect(search.pending, search.selected, lines, foldFailed) {
        val hit = search.selected ?: return@LaunchedEffect
        if (!search.pending) return@LaunchedEffect
        when (val step = searchGo(lines, hit.itemId, list.position.expanded, search.foldOfItem[hit.itemId], foldFailed)) {
            is SearchGo.Expand -> if (step.groups != list.position.expanded) list.setExpanded(step.groups)
            SearchGo.Waiting -> Unit
            SearchGo.Missing -> {
                search.pending = false
                search.notice = resources.getString(R.string.chat_search_unlocatable)
            }
            is SearchGo.Scroll -> {
                if (lines.none { it.contentKey == step.rowId }) return@LaunchedEffect
                withFrameNanos { }
                if (list.scrollToEntry(ChatListEntry(step.rowId), alignTop = true)) {
                    search.pending = false
                    search.highlight = step.rowId
                }
            }
        }
    }
    LaunchedEffect(search.returning, lines) {
        if (!search.returning) return@LaunchedEffect
        val origin = search.origin ?: return@LaunchedEffect
        val rowId = origin.rowId
        withFrameNanos { }
        if (rowId == null) list.returnToLatest()
        else if (rowId.startsWith("outbox-") || lines.any { it.contentKey == rowId }) {
            if (!list.scrollToEntry(ChatListEntry(rowId, offsetPx = origin.offsetPx))) return@LaunchedEffect
        } else {
            // Expansion changes are projected asynchronously before the saved key becomes available.
            if (lines.any { it is ChatLine.Step && it.groupId !in origin.expanded }) return@LaunchedEffect
            search.notice = resources.getString(R.string.chat_search_origin_changed)
        }
        search.origin = null
        search.returning = false
    }
    LaunchedEffect(search.highlight) {
        if (search.highlight != null) {
            delay(3_000)
            search.highlight = null
        }
    }
}
