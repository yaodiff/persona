package persona.core.domain

import persona.core.contracts.Item

/**
 * iOS continuity rules from spec/fixtures/continuity and ChatStore.apply/merge/replace.
 *
 * [covered] starts at zero (not -1). A push continuing from zero establishes a baseline;
 * a later push without a baseline waits for backlog, then requests reload if [loadedOnce].
 * Cache restoration does not count as backlog. Only backlog sets [loadedOnce]; [replace]
 * deliberately preserves it. A changed history clears items, coverage and pagination before
 * accepting a self-contained page or requesting reload for a continuing batch.
 *
 * A repeated full item replaces the old copy as on iOS; changed folds replace the work they cover.
 * Opened-fold and promotion retention matches `ChatStore.opened` and `ChatStore.retainingPromotion`.
 * Callers serialize access and execute effects outside this reducer (see [ContinuitySync]).
 */
class ContinuityReducer {
    enum class Gap { None, Fill, Reload }

    data class PushResult(
        val reloadLatest: Boolean = false,
        val fillGap: Boolean = false,
        val moreAfter: Boolean = false,
    )

    private val byId = LinkedHashMap<String, Item>()
    private val cachedPromotionIds = mutableSetOf<String>()
    private val opened = mutableMapOf<String, Item>()
    private var ordered: List<Item>? = null
    var history: String? = null
        private set
    var covered: Int = 0
        private set
    /** Null means no accepted pagination information, including after a history reset. */
    var hasMore: Boolean? = null
        private set
    var loadedOnce: Boolean = false
        private set
    val firstSeq: Int? get() = byId.values.minOfOrNull { it.seq }
    val items: List<Item>
        get() = ordered ?: byId.values.sortedWith(compareBy<Item> { it.seq }.thenBy { part(it.id) }).also { ordered = it }

    /** Restores cached state without treating the cache as a received backlog. */
    fun restore(items: List<Item>, history: String?, covered: Int, hasMore: Boolean? = null, loadedOnce: Boolean = false) {
        byId.clear()
        opened.clear()
        ordered = null
        cachedPromotionIds.clear()
        for (item in items) {
            if (item.supervisionUser == true) cachedPromotionIds += item.id
            byId[item.id] = item
        }
        this.history = history
        this.covered = covered.coerceAtLeast(0)
        this.hasMore = hasMore
        this.loadedOnce = loadedOnce
    }

    /**
     * Initial backlog: merges adjacent/overlapping pages, replaces distant cache, marks loaded.
     * Reload callers must use [replace] to discard a hole while retaining overtaking pushes.
     */
    fun latest(items: List<Item>, history: String?, toSeq: Int?, hasMore: Boolean? = null) {
        apply(items, history, toSeq = toSeq, backlog = true, hasMore = hasMore)
    }

    /**
     * Source-compatible push adapter. Pass backlog and nullable hasMore from the envelope.
     * moreAfter is actionable only when the resulting gap is Fill; before the first backlog
     * it cannot bypass the baseline gate, and a legacy capped batch requests reload instead.
     */
    fun push(items: List<Item>, history: String?, fromSeq: Int?, toSeq: Int?, moreAfter: Boolean,
             backlog: Boolean = false, hasMore: Boolean? = null): PushResult {
        val gap = apply(items, history, fromSeq, toSeq, backlog, hasMore, moreAfter)
        return PushResult(gap == Gap.Reload, gap == Gap.Fill, moreAfter && gap == Gap.Fill)
    }

    /**
     * Source-compatible catch-up adapter; omitted fromSeq assumes a request after current covered.
     * False means reload. New callers should use [apply] to retain the full gap decision and
     * pass the response's actual fromSeq, including null for legacy hubs.
     */
    fun newer(items: List<Item>, history: String?, toSeq: Int?, fromSeq: Int? = covered,
              moreAfter: Boolean = false): Boolean =
        apply(items, history, fromSeq, toSeq, moreAfter = moreAfter) != Gap.Reload

    fun apply(items: List<Item>, history: String? = null, fromSeq: Int? = null, toSeq: Int? = null,
              backlog: Boolean = false, hasMore: Boolean? = null, moreAfter: Boolean = false): Gap {
        if (history != null && this.history != null && history != this.history) {
            restore(emptyList(), history, 0)
            if (fromSeq != null && fromSeq > 0) return Gap.Reload
        }
        if (history != null) this.history = history
        if (toSeq == null) {
            merge(items, backlog, hasMore)
            covered = maxOf(covered, byId.values.maxOfOrNull { it.seq } ?: 0)
            return if (moreAfter) Gap.Reload else Gap.None
        }
        if (fromSeq != null) {
            merge(items, backlog, hasMore)
            if (covered == 0 && fromSeq > 0) return if (loadedOnce) Gap.Reload else Gap.None
            if (fromSeq > covered) return Gap.Fill
            covered = maxOf(covered, toSeq)
            return if (moreAfter) Gap.Fill else Gap.None
        }
        if (covered > 0 && items.firstOrNull()?.seq?.let { it.toLong() > covered.toLong() + 1 } == true) {
            replace(items, history, toSeq, hasMore ?: true)
            if (backlog) loadedOnce = true
            return Gap.None
        }
        merge(items, backlog, hasMore)
        covered = maxOf(covered, toSeq)
        return Gap.None
    }

    /**
     * Reload completion keeps same-history pushes strictly past toSeq, but does not claim
     * coverage through them. Unlike [latest], this does not change [loadedOnce].
     */
    fun replace(items: List<Item>, history: String?, toSeq: Int?, hasMore: Boolean) {
        val keep = if (history == null || history == this.history)
            byId.values.filter { toSeq != null && it.seq > toSeq } else emptyList()
        byId.clear()
        opened.clear()
        ordered = null
        this.hasMore = hasMore
        if (history != null) this.history = history
        merge(items)
        merge(keep)
        covered = toSeq ?: (byId.values.maxOfOrNull { it.seq } ?: 0)
    }

    /** Older pages set pagination directly without changing history, coverage or loadedOnce. */
    fun older(items: List<Item>, hasMore: Boolean? = null) {
        if (hasMore != null) this.hasMore = hasMore
        merge(items)
    }

    /** Publish accumulated range pages together, discarding fold placeholders in the response. */
    fun unfold(fold: Item, work: List<Item>) {
        val retained = byId.remove(fold.id) ?: return
        opened[fold.id] = retained
        ordered = null
        merge(work.filter { it.kind != "fold" })
    }

    private fun Item.promoted(): Item {
        val f = fold
        return copy(supervisionUser = true, fold = f?.copy(supervisionUser = true))
    }

    private fun merge(new: List<Item>, backlog: Boolean = false, hasMore: Boolean? = null) {
        if (byId.size != byId.keys.size) ordered = null
        // A cache may repeat an id: the last copy is authoritative, and earlier promotion is retained.
        val cached = LinkedHashMap<String, Item>()
        for (item in byId.values) cached[item.id] = item
        val evidence = (byId.values + opened.values + new).filter { it.supervisionUser == true || it.fold?.supervisionUser == true }
        val promotedIDs = (evidence.filter { it.supervisionUser == true }.map { it.id } + cachedPromotionIds).toMutableSet()
        cachedPromotionIds.clear()
        val promotedFolds = evidence.filter { it.fold != null }
        val promotedWork = evidence.filter { it.isWork() }
        fun retain(item: Item): Item {
            val keep = promotedIDs.contains(item.id) ||
                item.isWork() && promotedFolds.any { it.folds(item) } ||
                item.kind == "fold" && promotedWork.any { item.folds(it) }
            return if (keep) item.promoted() else item
        }
        byId.clear()
        for (item in cached.values) byId[item.id] = retain(item)
        val promoted = LinkedHashMap<String, Item>()
        new.forEach { promoted[it.id] = retain(it) }
        val fresh = promoted.values.toList()
        val batchWork = fresh.filter { it.isWork() && it.kind != "fold" }
        val incoming = fresh.filter { item ->
            val previous = opened[item.id]?.fold
            val current = item.fold
            val stillOpen = item.kind == "fold" && previous != null && current != null &&
                (current.throughSeq < previous.throughSeq ||
                    current.throughSeq == previous.throughSeq && part(current.throughId) <= part(previous.throughId))
            !stillOpen && !(item.kind == "fold" && batchWork.any { item.folds(it) })
        }
        val folds = incoming.filter { it.kind == "fold" && byId[it.id]?.fold != it.fold }
        for (fold in folds) {
            for (old in byId.values.toList()) {
                if (old.kind != "fold" && old.isWork() && fold.folds(old)) {
                    byId.remove(old.id)
                    ordered = null
                }
            }
        }
        for (work in batchWork) {
            for (old in byId.values.toList()) {
                if (old.kind == "fold" && old.folds(work)) {
                    byId.remove(old.id)
                    ordered = null
                }
            }
        }
        for (item in incoming) {
            if (byId[item.id] != item) ordered = null
            byId[item.id] = item
        }
        val existingFirst = cached.values.minOfOrNull { it.seq } ?: incoming.firstOrNull()?.seq
        val reachesStart = incoming.minOfOrNull { it.seq }?.let { it <= (existingFirst ?: it) } ?: false
        if (hasMore != null && backlog && (!loadedOnce || byId.size <= new.size) && (hasMore || reachesStart)) {
            this.hasMore = hasMore
        }
        if (backlog) loadedOnce = true
    }

    private fun Item.isWork(): Boolean = when (kind) {
        "thinking", "tool_use", "tool_result", "fold" -> true
        // A fold range can contain Codex commentary; assistant text outside that range is the final answer.
        "text" -> role == "assistant"
        "image" -> toolUseId != null
        else -> false
    }

    private fun Item.folds(other: Item): Boolean {
        val range = fold ?: return false
        return (other.seq > seq || other.seq == seq && part(other.id) >= part(id)) &&
            (other.seq < range.throughSeq || other.seq == range.throughSeq && part(other.id) <= part(range.throughId))
    }

    private fun part(id: String): Int = id.substringAfterLast('#', "").toIntOrNull() ?: 0
}
