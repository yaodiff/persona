package persona.core.chatlist

/** Items must belong to one verified, continuous window, ordered oldest to newest. */
class ChatListWindow<T>(
    private val id: (T) -> String,
    private val order: Comparator<T>,
    private val countsAsUnread: (T) -> Boolean,
) {
    private class CountCache<T>(
        val live: List<T>,
        val size: Int,
        val last: T?,
        val max: T?,
        val unread: List<T>?,
    )

    private class FrozenState<T>(
        private val initial: List<T>,
    ) {
        private val mergeLock = Any()
        private var snapshot: LinkedHashMap<String, T>? = null
        private var lastLive: List<T>? = null
        private var lastLiveSize = -1
        private var cachedSorted: List<T>? = null

        @Volatile private var boundaryComputed = false
        @Volatile private var boundaryValue: T? = null
        @Volatile var heldCount: Int? = null
        @Volatile private var countCache: CountCache<T>? = null

        fun boundary(order: Comparator<T>): T? {
            if (!boundaryComputed) {
                val computed = initial.maxWithOrNull(order)
                boundaryValue = computed
                boundaryComputed = true
            }
            return boundaryValue
        }

        fun items(live: List<T>, id: (T) -> String, order: Comparator<T>): List<T> = synchronized(mergeLock) {
            val size = live.size
            val existing = cachedSorted
            if (lastLive === live && lastLiveSize == size && existing != null) return existing
            var map = snapshot
            var changed = false
            if (map == null) {
                map = initial.associateByTo(LinkedHashMap(initial.size), id)
                snapshot = map
                changed = true
            }
            val end = boundary(order)
            if (end != null) {
                for (item in live) {
                    if (order.compare(item, end) <= 0 && map.putIfAbsent(id(item), item) == null) {
                        changed = true
                    }
                }
            }
            val sorted = if (changed || existing == null) {
                map.values.sortedWith(order).also { cachedSorted = it }
            } else {
                existing
            }
            lastLive = live
            lastLiveSize = size
            sorted
        }

        fun newMessageCount(
            live: List<T>,
            below: T?,
            lastRead: T?,
            defer: Boolean,
            id: (T) -> String,
            order: Comparator<T>,
            countsAsUnread: (T) -> Boolean,
        ): Int {
            if (defer) {
                val held = heldCount
                if (held != null) return held
            }
            val anchor = below ?: boundary(order)
            val end = when {
                anchor == null -> lastRead
                lastRead == null -> anchor
                order.compare(anchor, lastRead) >= 0 -> anchor
                else -> lastRead
            } ?: return 0
            if (live.isEmpty()) return 0.also { heldCount = it }

            val size = live.size
            val last = live[size - 1]
            var cache = countCache
            if (cache == null || cache.live !== live || cache.size != size || cache.last !== last) {
                val max = live.maxWithOrNull(order)
                cache = CountCache(live, size, last, max, unread = null)
                countCache = cache
            }
            val max = cache.max ?: return 0.also { heldCount = it }
            if (order.compare(max, end) <= 0) {
                return 0.also { heldCount = it }
            }

            var unread = cache.unread
            if (unread == null) {
                val seen = HashSet<String>(size)
                val collected = ArrayList<T>()
                for (item in live) {
                    if (seen.add(id(item)) && countsAsUnread(item)) {
                        collected += item
                    }
                }
                collected.sortWith(order)
                unread = collected
                countCache = CountCache(live, size, last, max, unread)
            }

            var low = 0
            var high = unread.size
            while (low < high) {
                val mid = (low + high) ushr 1
                if (order.compare(unread[mid], end) <= 0) low = mid + 1 else high = mid
            }
            return (unread.size - low).also { heldCount = it }
        }
    }

    private val stateLock = Any()
    @Volatile private var frozenState: FrozenState<T>? = null

    val isFrozen: Boolean get() = frozenState != null

    fun freeze(items: List<T>) {
        if (frozenState != null) return
        synchronized(stateLock) {
            if (frozenState != null) return
            frozenState = FrozenState(items.toList())
        }
    }

    fun follow() {
        if (frozenState == null) return
        synchronized(stateLock) {
            frozenState = null
        }
    }

    /** Preserve frozen values while admitting older pages and holes anywhere before the boundary. */
    fun items(live: List<T>): List<T> {
        val current = frozenState ?: return live
        return current.items(live, id, order)
    }

    /**
     * A measured visible boundary also counts messages already loaded below the current viewport. While [defer] holds,
     * keep the last count: streaming deltas must not rescan the transcript on the main thread. The next settled call,
     * or [follow], restores the exact count under the original rule.
     */
    fun newMessageCount(live: List<T>, below: T? = null, lastRead: T? = null, defer: Boolean = false): Int {
        val current = frozenState ?: return 0
        return current.newMessageCount(live, below, lastRead, defer, id, order, countsAsUnread)
    }
}

data class ChatReadingKey(val scope: String, val conversation: String)

data class ChatReadingPosition(
    val rowId: String? = null,
    val offsetPx: Int = 0,
    val followsEnd: Boolean = true,
    val expanded: Set<String> = emptySet(),
)

/** Supply a scope-owned cache with ordered durable writes; these calls must not block the UI thread. */
interface ChatAnchorStore {
    fun read(key: ChatReadingKey): ChatReadingPosition?
    fun write(key: ChatReadingKey, position: ChatReadingPosition)
}

data class ChatListEntry(val rowId: String, val lastLineAtTop: Boolean = false, val offsetPx: Int = 0, val alignTop: Boolean = false)

internal const val LEAVE_BOTTOM_THRESHOLD_DP = 16f
internal const val RETURN_BOTTOM_TOLERANCE_DP = 4f

/** Whether the reverse-layout list has moved far enough from the newest edge to stop following. */
internal fun hasLeftBottom(
    firstVisibleItemIndex: Int,
    firstVisibleItemScrollOffset: Int,
    canScrollBackward: Boolean,
    leaveThresholdPx: Int,
): Boolean = canScrollBackward && (firstVisibleItemIndex > 0 || firstVisibleItemScrollOffset > leaveThresholdPx)

/** Whether the reverse-layout list has moved further toward older history than [anchorIndex]/[anchorOffset]. */
internal fun movedAwayFromBottom(
    firstVisibleItemIndex: Int,
    firstVisibleItemScrollOffset: Int,
    anchorIndex: Int,
    anchorOffset: Int,
): Boolean = firstVisibleItemIndex > anchorIndex ||
    (firstVisibleItemIndex == anchorIndex && firstVisibleItemScrollOffset > anchorOffset)

/** Whether the reverse-layout list is settled at (or within small tolerance of) the newest edge. */
internal fun isAtBottom(
    firstVisibleItemIndex: Int,
    firstVisibleItemScrollOffset: Int,
    canScrollBackward: Boolean,
    returnTolerancePx: Int,
): Boolean = !canScrollBackward || (firstVisibleItemIndex == 0 && firstVisibleItemScrollOffset <= returnTolerancePx)

internal fun shouldPrefetch(dragged: Boolean, hasOlder: Boolean, loading: Boolean, lastVisible: Int, rows: Int, distance: Int): Boolean =
    dragged && hasOlder && !loading && rows > 0 && lastVisible >= (rows - 1 - distance).coerceAtLeast(0)

/**
 * Reverse layout coordinates grow from the latest end towards history. [topPadding] is the list's content padding at
 * the top (the bar the page floats over it): the last line goes right under it, not behind it.
 */
internal fun lastLineScrollOffset(viewportEnd: Int, lastLineExtent: Int, topPadding: Int = 0): Int =
    -(viewportEnd - topPadding - lastLineExtent).coerceAtLeast(0)
