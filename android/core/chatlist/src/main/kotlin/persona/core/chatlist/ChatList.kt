package persona.core.chatlist

import androidx.compose.foundation.interaction.DragInteraction
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.listSaver
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.currentStateAsState
import kotlinx.coroutines.FlowPreview
import kotlinx.coroutines.flow.debounce
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlin.math.abs

@Stable
class ChatListState internal constructor(
    val list: LazyListState,
    internal val readingKey: ChatReadingKey?,
    internal val store: ChatAnchorStore?,
    initial: ChatReadingPosition,
) {
    /** The reading anchor. Scrolling writes a new offset into it every frame: composition reads [expanded] and [followsEnd]. */
    var position by mutableStateOf(initial)
        internal set
    // Derived, so a reader is invalidated only when the value changes, not by every offset written while scrolling (#4039).
    private val followsEndState = derivedStateOf { position.followsEnd }
    private val expandedState = derivedStateOf { position.expanded }
    val followsEnd: Boolean get() = followsEndState.value
    /** The open work groups. */
    val expanded: Set<String> get() = expandedState.value
    var restored by mutableStateOf(false)
        internal set
    var programmaticScroll by mutableStateOf(false)
        internal set
    internal var dragged by mutableStateOf(false)
    internal var epoch by mutableIntStateOf(0)
    internal var latestRequest by mutableIntStateOf(0)
    internal var rowIds: List<String> = emptyList()
        set(value) { if (value !== field) { field = value; rowIdSet = null } }
    /** [rowIds] as a set, built once per list: [capture] runs every frame of a scroll. */
    private var rowIdSet: Set<String>? = null
    internal val lastLineExtents = mutableMapOf<String, Int>()

    fun setExpanded(ids: Set<String>) { position = position.copy(expanded = ids) }
    fun reportLastLine(rowId: String, extentPx: Int) { lastLineExtents[rowId] = extentPx }

    /** Explicit navigation invalidates any pending entry correction. */
    fun returnToLatest() {
        epoch++
        restored = true
        position = position.copy(rowId = null, offsetPx = 0, followsEnd = true)
        latestRequest++
    }

    internal fun capture() {
        if (!restored || programmaticScroll) return
        val rows = rowIdSet ?: rowIds.toHashSet().also { rowIdSet = it }
        val top = list.layoutInfo.visibleItemsInfo.filter { it.key in rows }.maxByOrNull { it.index }
        position = position.copy(rowId = top?.key as? String ?: position.rowId,
            offsetPx = top?.let { -it.offset } ?: position.offsetPx)
    }

    internal fun persist() {
        if (!restored) return
        capture()
        readingKey?.let { store?.write(it, position) }
    }
}

@Composable
fun rememberChatListState(readingKey: ChatReadingKey? = null, store: ChatAnchorStore? = null): ChatListState {
    val list = rememberLazyListState()
    return rememberSaveable(readingKey, store, saver = listSaver<ChatListState, Any>(
        save = { listOf(it.position.rowId.orEmpty(), it.position.offsetPx, it.followsEnd, ArrayList(it.position.expanded)) },
        restore = { saved ->
            @Suppress("UNCHECKED_CAST")
            ChatListState(list, readingKey, store, ChatReadingPosition((saved[0] as String).ifEmpty { null },
                saved[1] as Int, saved[2] as Boolean, (saved[3] as List<String>).toSet()))
        },
    )) { ChatListState(list, readingKey, store, readingKey?.let { store?.read(it) } ?: ChatReadingPosition()) }
}

/** Bind row keys in newest-first order, including local pending rows but excluding paging controls. */
@OptIn(FlowPreview::class)
@Composable
fun ChatListEffects(
    state: ChatListState,
    rowIds: List<String>,
    ready: Boolean,
    entry: ChatListEntry? = null,
    hasOlder: Boolean = false,
    loadingOlder: Boolean = false,
    prefetchRows: Int = 5,
    onFreeze: () -> Unit,
    onFollow: () -> Unit,
    loadOlder: suspend () -> Unit,
    canFollowEnd: Boolean = true,
    /** Previously persisted message keys mapped to stable presentation slots. */
    rowAliases: Map<String, String> = emptyMap(),
) {
    val latestRows by rememberUpdatedState(rowIds)
    val freeze by rememberUpdatedState(onFreeze)
    val follow by rememberUpdatedState(onFollow)
    val load by rememberUpdatedState(loadOlder)
    val older by rememberUpdatedState(hasOlder)
    val loading by rememberUpdatedState(loadingOlder)
    val atLatest by rememberUpdatedState(canFollowEnd)
    val focusManager by rememberUpdatedState(LocalFocusManager.current)
    val keyboard by rememberUpdatedState(LocalSoftwareKeyboardController.current)
    SideEffect { state.rowIds = rowIds }
    val owner = LocalLifecycleOwner.current
    val lifecycle by owner.lifecycle.currentStateAsState()
    val foreground = lifecycle == Lifecycle.State.RESUMED
    DisposableEffect(state, owner) {
        val observer = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_PAUSE || event == Lifecycle.Event.ON_STOP) {
                state.epoch++
                if (state.programmaticScroll) state.restored = true
                state.persist()
            }
        }
        owner.lifecycle.addObserver(observer)
        onDispose { owner.lifecycle.removeObserver(observer); state.epoch++; state.persist() }
    }
    LaunchedEffect(state) {
        state.list.interactionSource.interactions.collect { interaction ->
            if (interaction is DragInteraction.Start) {
                // As on iOS (`ChatView`'s `.interacting` scroll phase): a real drag on the conversation puts the keyboard
                // away and takes the focus from whatever field has it, a request card's too. Only a finger past the touch
                // slop starts a DragInteraction: a tap and a scroll the app starts (following the newest message, a jump)
                // do not, and they leave the keyboard up (#4210).
                focusManager.clearFocus()
                keyboard?.hide()
                state.epoch++
                state.dragged = true
                state.restored = true
                state.position = state.position.copy(followsEnd = false)
                freeze()
            }
        }
    }
    LaunchedEffect(state, ready, rowIds, rowAliases, foreground, state.epoch) {
        if (!foreground || !ready || state.restored || state.dragged) return@LaunchedEffect
        if (rowIds.isEmpty()) { state.restored = true; return@LaunchedEffect }
        val saved = state.position
        val target = (entry ?: saved.rowId?.takeUnless { saved.followsEnd }?.let { ChatListEntry(it, offsetPx = saved.offsetPx) })
            ?.let { it.copy(rowId = rowAliases[it.rowId] ?: it.rowId) }
        val targetIndex = target?.let { rowIds.indexOf(it.rowId) } ?: -1
        state.programmaticScroll = true
        try {
            if (targetIndex >= 0 && target != null) {
                state.position = saved.copy(followsEnd = false)
                freeze()
                state.list.scrollToItem(targetIndex, if (target.lastLineAtTop) 0 else target.offsetPx)
                withFrameNanos { }
                // Instantiate at most once more if lazy measurement recycled the target row.
                if (state.list.layoutInfo.visibleItemsInfo.none { it.key == target.rowId }) {
                    state.list.scrollToItem(targetIndex)
                    withFrameNanos { }
                }
                val visible = state.list.layoutInfo.visibleItemsInfo.firstOrNull { it.key == target.rowId }
                if (visible != null && !state.list.isScrollInProgress) {
                    val offset = when {
                        // In the reversed list the content padding after the items is the top one, under the top bar.
                        target.lastLineAtTop -> lastLineScrollOffset(state.list.layoutInfo.viewportEndOffset,
                            state.lastLineExtents[target.rowId] ?: 1, state.list.layoutInfo.afterContentPadding)
                        target.alignTop -> visible.size - state.list.layoutInfo.viewportEndOffset
                        else -> target.offsetPx
                    }
                    if (abs(visible.offset + offset) > 1) state.list.scrollToItem(targetIndex, offset)
                }
            } else {
                state.position = saved.copy(followsEnd = true)
                follow()
                state.list.scrollToItem(0)
            }
            state.restored = true
        } finally { state.programmaticScroll = false }
    }
    LaunchedEffect(state, state.latestRequest) {
        if (state.latestRequest == 0) return@LaunchedEffect
        follow()
    }
    LaunchedEffect(state, rowIds, foreground, state.followsEnd, state.restored, state.latestRequest) {
        if (foreground && state.restored && state.followsEnd && !state.list.isScrollInProgress && rowIds.isNotEmpty()) {
            state.programmaticScroll = true
            try { state.list.scrollToItem(0) } finally { state.programmaticScroll = false }
        }
    }
    LaunchedEffect(state) {
        snapshotFlow { Triple(state.list.firstVisibleItemIndex, state.list.firstVisibleItemScrollOffset, state.list.isScrollInProgress) }
            .distinctUntilChanged().collect { (index, offset, scrolling) ->
                state.capture()
                if (atLatest && state.dragged && state.restored && !state.programmaticScroll && !scrolling && index == 0 && offset == 0 && !state.followsEnd)
                    state.returnToLatest()
            }
    }
    LaunchedEffect(state, hasOlder, loadingOlder, rowIds, foreground) {
        if (!foreground) return@LaunchedEffect
        snapshotFlow { state.dragged to (state.list.layoutInfo.visibleItemsInfo.maxOfOrNull { it.index } ?: -1) }
            .distinctUntilChanged().collect { (dragged, last) ->
                if (shouldPrefetch(dragged, older, loading, last, latestRows.size, prefetchRows)) load()
            }
    }
    LaunchedEffect(state) {
        snapshotFlow { state.position }.debounce(250).distinctUntilChanged().collect { state.persist() }
    }
}

@Composable
fun ChatList(
    state: ChatListState,
    modifier: Modifier = Modifier,
    contentPadding: PaddingValues = PaddingValues(12.dp),
    content: LazyListScope.() -> Unit,
) {
    LazyColumn(state = state.list, reverseLayout = true, modifier = modifier,
        contentPadding = contentPadding, verticalArrangement = Arrangement.spacedBy(8.dp), content = content)
}
