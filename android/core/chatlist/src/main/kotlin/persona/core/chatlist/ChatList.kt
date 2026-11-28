package persona.core.chatlist

import androidx.compose.foundation.gestures.scrollBy
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
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.currentStateAsState
import kotlinx.coroutines.FlowPreview
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.debounce
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.launch
import kotlin.math.abs
import kotlin.math.roundToInt

private data class ScrollEdgeSnapshot(
    val index: Int,
    val offset: Int,
    val firstVisibleKey: Any?,
    val newestKey: String?,
    val canScrollBackward: Boolean,
    val scrolling: Boolean,
)

@Stable
class ChatListState internal constructor(
    val list: LazyListState,
    internal val readingKey: ChatReadingKey?,
    internal val store: ChatAnchorStore?,
    initial: ChatReadingPosition,
) {
    /**
     * A fold opened from the list. [rowId] is the header the user tapped, [offsetPx] its distance from the newest
     * edge (the reversed list's item offset), and [sizePx] its measured height. While it is set, new rows below the
     * header must not move it: [ChatListEffects] puts the item back until the rows that arrive with the fold settle.
     * [riseToTop] is set when the tap happened while following the newest end: there the header may rise to the top
     * edge, showing what opened under it, but no further.
     */
    internal data class ExpansionAnchor(
        val rowId: String,
        val offsetPx: Int,
        val sizePx: Int,
        val epoch: Int,
        val riseToTop: Boolean,
    )

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
    internal var expansionAnchor by mutableStateOf<ExpansionAnchor?>(null)
        private set
    internal var expansionLoading by mutableStateOf(false)
        private set
    /** A tapped fold is still being held in place. */
    val expanding: Boolean get() = expansionAnchor != null
    internal var rowIds: List<String> = emptyList()
        set(value) { if (value !== field) { field = value; rowIdSet = null } }
    /** [rowIds] as a set, built once per list: [capture] runs every frame of a scroll. */
    private var rowIdSet: Set<String>? = null
    internal val lastLineExtents = mutableMapOf<String, Int>()

    fun setExpanded(ids: Set<String>) { position = position.copy(expanded = ids) }
    fun reportLastLine(rowId: String, extentPx: Int) { lastLineExtents[rowId] = extentPx }

    /**
     * Hold [rowId]'s header where it is before opening or closing its fold. The tap stops following the newest end, as
     * a drag does, and bumps [epoch] so an in-flight restore cannot pull the list away. [riseToTop] lets an opening at
     * the newest end use the room to the top edge instead of holding the header near the composer. Returns false when
     * the row is not on screen or the list is still being placed; the fold then changes as before.
     */
    fun beginToggle(rowId: String, riseToTop: Boolean = position.followsEnd): Boolean {
        if (!restored || programmaticScroll) return false
        val info = list.layoutInfo.visibleItemsInfo.firstOrNull { it.key == rowId } ?: return false
        capture()
        epoch++
        dragged = false
        restored = true
        position = position.copy(followsEnd = false)
        expansionAnchor = ExpansionAnchor(rowId, info.offset, info.size, epoch, riseToTop)
        expansionLoading = false
        return true
    }

    /** What the opened fold is still fetching (a hub fold, another row's content): keep watching until it arrives. */
    fun noteExpansionLoading(loading: Boolean) {
        if (expansionAnchor != null) expansionLoading = loading
    }

    /** The tap opened nothing (or another toggle replaced it): stop holding the previous header. */
    fun endExpansion() {
        if (expansionAnchor == null) return
        expansionAnchor = null
        expansionLoading = false
        capture()
    }

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
    val density = LocalDensity.current.density
    val leaveThreshold by rememberUpdatedState((LEAVE_BOTTOM_THRESHOLD_DP * density).roundToInt())
    val returnTolerance by rememberUpdatedState((RETURN_BOTTOM_TOLERANCE_DP * density).roundToInt())
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
        val activeDrags = mutableSetOf<DragInteraction.Start>()
        var userScrollPending = false
        var settleAfterStop: Job? = null
        var handledLatestRequest = state.latestRequest

        fun scrollEdge(): ScrollEdgeSnapshot {
            val index = state.list.firstVisibleItemIndex
            return ScrollEdgeSnapshot(
                index = index,
                offset = state.list.firstVisibleItemScrollOffset,
                firstVisibleKey = state.list.layoutInfo.visibleItemsInfo.firstOrNull { it.index == index }?.key,
                newestKey = latestRows.firstOrNull(),
                canScrollBackward = state.list.canScrollBackward,
                scrolling = state.list.isScrollInProgress,
            )
        }
        var anchor = scrollEdge()

        fun reconcileFollow(edge: ScrollEdgeSnapshot = scrollEdge()) {
            val dragging = activeDrags.isNotEmpty()

            if (state.latestRequest != handledLatestRequest) {
                handledLatestRequest = state.latestRequest
                userScrollPending = false
                anchor = edge
            }
            if (state.programmaticScroll) {
                if (!dragging) userScrollPending = false
                anchor = edge
                return
            }

            val newestChanged = edge.newestKey != anchor.newestKey
            val leftBottom = hasLeftBottom(edge.index, edge.offset, edge.canScrollBackward, leaveThreshold)
            val followingGesture = (dragging || userScrollPending) && state.followsEnd
            val rebasedIndex = if (followingGesture && leftBottom) {
                // LazyColumn retains the old key when new rows are prepended. Its index can grow with no drag.
                state.list.layoutInfo.visibleItemsInfo.firstOrNull { it.key == anchor.firstVisibleKey }?.index
            } else null
            if (followingGesture && leftBottom) {
                val movedAway = when {
                    rebasedIndex != null -> {
                        val offset = if (anchor.index == 0 && rebasedIndex > 0) maxOf(anchor.offset, leaveThreshold) else anchor.offset
                        movedAwayFromBottom(edge.index, edge.offset, rebasedIndex, offset)
                    }
                    !newestChanged -> movedAwayFromBottom(edge.index, edge.offset, anchor.index, anchor.offset)
                    else -> {
                        // A fling can carry the anchor offscreen. Compare the keys' order, since some rendered
                        // rows are absent from rowIds and its indexes then differ from the layout indexes.
                        val rows = latestRows
                        val anchorRank = (anchor.firstVisibleKey as? String)?.let(rows::indexOf) ?: -1
                        val currentRank = (edge.firstVisibleKey as? String)?.let(rows::indexOf) ?: -1
                        anchorRank >= 0 && currentRank > anchorRank
                    }
                }
                if (movedAway) {
                    if (state.position.followsEnd) state.position = state.position.copy(followsEnd = false)
                    freeze()
                }
            }

            if (!dragging && !edge.scrolling && settleAfterStop == null) {
                val settledAtBottom = isAtBottom(edge.index, edge.offset, edge.canScrollBackward, returnTolerance)
                val missedFollow = state.followsEnd && userScrollPending && newestChanged && !settledAtBottom &&
                    rebasedIndex == edge.index && edge.offset == anchor.offset
                if (atLatest && state.dragged && state.restored && (missedFollow || (!state.followsEnd && settledAtBottom))) {
                    state.returnToLatest()
                    handledLatestRequest = state.latestRequest
                }
                userScrollPending = false
                anchor = edge
            }
        }

        launch {
            state.list.interactionSource.interactions.collect { interaction ->
                when (interaction) {
                    is DragInteraction.Start -> {
                        // As on iOS (`ChatView`'s `.interacting` scroll phase): a real drag on the conversation puts the keyboard
                        // away and takes the focus from whatever field has it, a request card's too. Only a finger past the touch
                        // slop starts a DragInteraction: a tap and a scroll the app starts (following the newest message, a jump)
                        // do not, and they leave the keyboard up (#4210).
                        focusManager.clearFocus()
                        keyboard?.hide()
                        settleAfterStop?.cancel()
                        settleAfterStop = null
                        activeDrags += interaction
                        userScrollPending = true
                        if (!state.restored || state.programmaticScroll) state.epoch++
                        if (!state.dragged) state.dragged = true
                        if (!state.restored) state.restored = true
                        reconcileFollow()
                    }
                    is DragInteraction.Stop -> {
                        activeDrags -= interaction.start
                        if (activeDrags.isEmpty() && userScrollPending) {
                            // Compose emits Stop before starting the fling in a separate coroutine.
                            settleAfterStop?.cancel()
                            settleAfterStop = launch {
                                withFrameNanos { }
                                settleAfterStop = null
                                reconcileFollow()
                            }
                        }
                        reconcileFollow()
                    }
                    is DragInteraction.Cancel -> {
                        activeDrags -= interaction.start
                        reconcileFollow()
                    }
                }
            }
        }

        launch {
            snapshotFlow { scrollEdge() }.collect { edge ->
                state.capture()
                reconcileFollow(edge)
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
    LaunchedEffect(state, state.expansionAnchor) {
        val anchor = state.expansionAnchor ?: return@LaunchedEffect
        state.programmaticScroll = true
        var settledFrames = 0
        try {
            while (true) {
                withFrameNanos { }
                if (state.expansionAnchor !== anchor || state.dragged || state.epoch != anchor.epoch) return@LaunchedEffect
                val index = state.rowIds.indexOf(anchor.rowId)
                if (index < 0) return@LaunchedEffect
                val layout = state.list.layoutInfo
                val visible = layout.visibleItemsInfo.firstOrNull { it.key == anchor.rowId }
                // The reversed list measures an item's offset from the newest edge: the same number scrollToItem takes.
                val topOffset = layout.viewportEndOffset - anchor.sizePx
                val target = if (anchor.riseToTop) topOffset else anchor.offsetPx
                val needs = visible == null || expansionNeedsScroll(visible.offset, target, anchor.riseToTop)
                if (needs) {
                    if (visible != null) {
                        val delta = visible.offset - target
                        if (abs(delta) > 1) state.list.scrollBy(delta.toFloat())
                    } else {
                        // It left the composed window (a whole fold laid out in one pass): put it at the newest edge
                        // first, then move it up by the offset it should have from that edge.
                        state.list.scrollToItem(index, 0)
                        withFrameNanos { }
                        val placed = state.list.layoutInfo.visibleItemsInfo.firstOrNull { it.key == anchor.rowId }?.offset ?: 0
                        if (abs(placed - target) > 1) state.list.scrollBy((placed - target).toFloat())
                    }
                    settledFrames = 0
                } else if (state.expansionLoading) {
                    settledFrames = 0
                } else if (++settledFrames >= 3) {
                    return@LaunchedEffect
                }
            }
        } finally {
            state.programmaticScroll = false
            if (state.expansionAnchor === anchor) state.endExpansion()
        }
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

/** Whether a held fold header still has to move. A one-pixel rounding difference is settled, as the ordinary hold
 *  already treats it; a rise-to-top hold only ever moves the header up, never back down. */
internal fun expansionNeedsScroll(visibleOffset: Int, target: Int, riseToTop: Boolean): Boolean =
    if (riseToTop) visibleOffset - target > 1 else abs(visibleOffset - target) > 1

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
