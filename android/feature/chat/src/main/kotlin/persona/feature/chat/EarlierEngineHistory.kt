package persona.feature.chat

import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.res.stringResource
import coil3.ImageLoader
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.designsystem.AppStyle
import persona.core.designsystem.JLoading
import persona.core.designsystem.JNotice
import persona.core.designsystem.JTextButton
import persona.core.designsystem.PersonaTheme
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.EngineChanges
import persona.core.domain.lines.EngineHistoryWindow
import persona.core.domain.lines.EngineHistoryWindows
import persona.core.domain.lines.LineOptions
import persona.core.domain.lines.ModelChanges
import persona.core.domain.lines.WorkItem
import persona.core.domain.lines.chatLines

internal data class EngineHistorySource(val items: List<Item>, val hasOlder: Boolean, val history: String?, val missing: Boolean = false)

internal data class EngineHistorySection(val window: EngineHistoryWindow, val items: List<Item>)

internal data class EngineHistoryPlan(
    val sections: List<EngineHistorySection> = emptyList(),
    val next: EngineHistoryWindow? = null,
    val missing: Boolean = false,
)

/** The current conversation is always rendered. Older windows appear newest first, only after every window after them is complete. */
internal fun planEarlierEngines(
    windows: List<EngineHistoryWindow>, ready: Boolean, sourceOf: (SessionKey) -> EngineHistorySource?,
): EngineHistoryPlan {
    if (!ready || windows.size < 2) return EngineHistoryPlan()
    return buildList<EngineHistorySection> {
        for (window in windows.dropLast(1).asReversed()) {
            val source = sourceOf(window.session)
            val items = source?.let { EngineHistoryWindows.split(it.items, windows, window.session)[window.index] }.orEmpty()
            if (items.isNotEmpty()) add(EngineHistorySection(window, items))
            if (source == null || !EngineHistoryWindows.complete(window, windows, source.items, source.hasOlder)) {
                return EngineHistoryPlan(toList(), next = window, missing = windows.any { sourceOf(it.session)?.missing == true })
            }
        }
    }.let { EngineHistoryPlan(it, missing = windows.any { window -> sourceOf(window.session)?.missing == true }) }
}

/** Scope the ordinary chat reducer and its outgoing actions to the current run. Older runs use their own session IDs. */
internal fun currentEngineSnapshot(snapshot: ChatSnapshot, windows: List<EngineHistoryWindow>): ChatSnapshot {
    val live = windows.lastOrNull()?.takeIf { it.repeated } ?: return snapshot
    val items = EngineHistoryWindows.split(snapshot.items, windows, live.session)[live.index].orEmpty()
    val complete = snapshot.olderBoundaryKnown && EngineHistoryWindows.complete(live, windows, snapshot.items, snapshot.hasOlder)
    if (items.size == snapshot.items.size && (!complete || !snapshot.hasOlder)) return snapshot
    val ids = items.mapTo(hashSetOf()) { it.id }
    return snapshot.copy(items = items, hasOlder = snapshot.hasOlder && !complete,
        loadingOlder = snapshot.loadingOlder && !complete,
        liveText = snapshot.liveText.filterKeys { it in ids }, streamingItemIds = snapshot.streamingItemIds.intersect(ids))
}

/** Each load reads one page on demand. It neither subscribes to stopped sessions nor marks old messages seen. */
internal class EarlierEngineHistory(private val repository: ChatRepository) {
    private var sources by mutableStateOf<Map<SessionKey, EngineHistorySource>>(emptyMap())
    private var ignoreSeed by mutableStateOf<Set<SessionKey>>(emptySet())
    private var liveGeneration: Pair<SessionKey, String>? = null
    private val generations = mutableMapOf<SessionKey, Long>()
    private var failure by mutableStateOf<SessionKey?>(null)
    var loading by mutableStateOf(false)
        private set
    val failed: Boolean get() = failure != null
    var open by mutableStateOf<Set<EngineLineKey>>(emptySet())
        private set
    var folds by mutableStateOf<Map<EngineLineKey, List<Item>>>(emptyMap())
        private set
    var folding by mutableStateOf<Set<EngineLineKey>>(emptySet())
        private set
    var failedFolds by mutableStateOf<Set<EngineLineKey>>(emptySet())
        private set

    private fun source(session: SessionKey, current: SessionKey, live: ChatSnapshot): EngineHistorySource? =
        sources[session]?.takeUnless { session == current && session !in ignoreSeed && changedHistory(it.history, live.history) }
            ?: if (session == current && session !in ignoreSeed && live.olderBoundaryKnown && !live.loading)
                EngineHistorySource(live.items, live.hasOlder, live.history) else null

    fun observeLiveHistory(current: SessionKey, history: String?) {
        if (history == null) return
        val previous = liveGeneration?.takeIf { it.first == current }?.second
        val cached = sources[current]?.history
        if (cached == history && current in ignoreSeed) ignoreSeed = ignoreSeed - current
        else if (changedHistory(previous, history) || changedHistory(cached, history)) reset(current, ignoreLiveSeed = false)
        liveGeneration = current to history
    }

    private fun reset(session: SessionKey, ignoreLiveSeed: Boolean) {
        generations[session] = generation(session) + 1L
        sources = sources - session
        ignoreSeed = if (ignoreLiveSeed) ignoreSeed + session else ignoreSeed - session
        open = open.filterNot { it.window.session == session }.toSet()
        folds = folds.filterKeys { it.window.session != session }
        folding = folding.filterNot { it.window.session == session }.toSet()
        failedFolds = failedFolds.filterNot { it.window.session == session }.toSet()
        if (failure == session) failure = null
    }

    private fun generation(session: SessionKey): Long = generations[session] ?: 0L

    fun plan(windows: List<EngineHistoryWindow>, current: SessionKey, live: ChatSnapshot, ready: Boolean): EngineHistoryPlan =
        planEarlierEngines(windows, ready) { source(it, current, live) }

    suspend fun load(window: EngineHistoryWindow, current: SessionKey, live: ChatSnapshot) {
        if (loading) return
        val observed = liveGeneration?.takeIf { it.first == window.session }?.second
        if (window.session == current && changedHistory(live.history, observed)) return
        loading = true
        failure = null
        val key = window.session
        val started = generation(key)
        val old = source(key, current, live)
        try {
            val before = old?.items?.minOfOrNull { it.seq }
            val page = repository.earlierEnginePage(key, before)
            if (started != generation(key)) return
            if (page == null) {
                sources = sources + (key to EngineHistorySource(old?.items.orEmpty(), false, old?.history, missing = true))
                return
            }
            if (changedHistory(old?.history, page.history)) {
                // A rewind invalidated the cursor and every fold expanded against it.
                reset(key, ignoreLiveSeed = true)
                failure = key
                return
            }
            val pageStart = page.items.minOfOrNull { it.seq }
            check(!page.hasMore || pageStart != null && (before == null || pageStart < before))
            val items = (page.items + old?.items.orEmpty()).distinctBy(Item::id)
            sources = sources + (key to EngineHistorySource(items, page.hasMore, page.history ?: old?.history))
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (_: Exception) {
            if (started == generation(key)) failure = key
        } finally {
            loading = false
        }
    }

    fun toggle(window: EngineHistoryWindow, id: String): Boolean {
        val key = EngineLineKey(window, id)
        val opening = key !in open
        open = if (opening) open + key else open - key
        return opening
    }

    suspend fun expand(window: EngineHistoryWindow, item: Item) {
        val fold = item.fold ?: return
        val key = EngineLineKey(window, item.id)
        if (key in folding || key in folds) return
        val started = generation(window.session)
        folding = folding + key
        failedFolds = failedFolds - key
        try {
            val items = repository.loadRange(window.session, item.seq, fold.throughSeq)
            if (started != generation(window.session)) return
            check(items.any { it.id == fold.throughId } && items.none { it.kind == "fold" })
            folds = folds + (key to items)
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (_: Exception) {
            if (started == generation(window.session)) failedFolds = failedFolds + key
        } finally {
            if (started == generation(window.session)) folding = folding - key
        }
    }
}

private fun changedHistory(previous: String?, current: String?): Boolean =
    previous != null && current != null && previous != current

internal data class EngineLineKey(val window: EngineHistoryWindow, val item: String)

internal data class EarlierEngineLine(val window: EngineHistoryWindow, val line: ChatLine) {
    val key get() = "history-" + window.index + "-" + line.contentKey
}

/** Build each session's rows on its own sequence axis. A tool result cannot bind to a call in another engine. */
internal fun earlierEngineLines(
    plan: EngineHistoryPlan, open: Set<EngineLineKey>, folds: Map<EngineLineKey, List<Item>>, showModelChanges: Boolean,
): List<EarlierEngineLine> = plan.sections.flatMap { (window, items) ->
    val rows = items.flatMap { item ->
        folds[EngineLineKey(window, item.id)]?.mapIndexed { index, expanded ->
            expanded.asLineItem().copy(exchangeId = item.id.takeIf { index == 0 })
        } ?: listOf(item.asLineItem())
    }
    val openWork = open.filter { it.window == window }.mapTo(mutableSetOf()) { it.item }
    val lines = chatLines(rows, LineOptions(openWork = openWork)).lines
    val marked = EngineChanges.mark(if (showModelChanges) ModelChanges.mark(lines) else lines)
    marked.asReversed().map { EarlierEngineLine(window, it) }
}

internal data class EarlierEnginePresentation(
    val lines: List<EarlierEngineLine>,
    val next: EngineHistoryWindow?,
    val loading: Boolean,
    val failed: Boolean,
    val missing: Boolean,
    val load: () -> Unit,
    val store: EarlierEngineHistory,
) {
    fun keys(): List<String> = buildList {
        addAll(lines.map(EarlierEngineLine::key))
        if (missing) add("history-missing")
        if (next != null) add("history-more")
    }
}

@Composable
internal fun rememberEarlierEngines(
    repository: ChatRepository,
    current: SessionKey,
    windows: List<EngineHistoryWindow>,
    live: ChatSnapshot,
    ready: Boolean,
): EarlierEnginePresentation? {
    if (windows.isEmpty()) return null
    val store = remember(repository, current, windows) { EarlierEngineHistory(repository) }
    val scope = rememberCoroutineScope()
    LaunchedEffect(store, current, live.history) {
        store.observeLiveHistory(current, live.history)
    }
    val plan by remember(store, windows, current, live, ready) {
        derivedStateOf { store.plan(windows, current, live, ready) }
    }
    val open = store.open
    val folds = store.folds
    val glass = PersonaTheme.style == AppStyle.Glass
    val lines by produceState(emptyList<EarlierEngineLine>(), plan.sections, open, folds, glass) {
        value = withContext(Dispatchers.Default) { earlierEngineLines(plan, open, folds, glass) }
    }
    return EarlierEnginePresentation(lines, plan.next, store.loading, store.failed, plan.missing, {
        plan.next?.let { next -> scope.launch { store.load(next, current, live) } }
    }, store)
}

internal fun LazyListScope.earlierEngineHistory(
    history: EarlierEnginePresentation,
    scope: CoroutineScope,
    loader: ImageLoader,
    style: ChatRenderStyle,
    onReadFullText: (SessionKey, Item) -> Unit,
) {
    val workGroups = history.lines.mapNotNull { (window, line) -> (line as? ChatLine.Work)?.let { window to it.id } }.toSet()
    for ((window, line) in history.lines) {
        item(key = EarlierEngineLine(window, line).key, contentType = line.contentType()) {
            CompositionLocalProvider(LocalChatRowActions provides null, LocalChatChildren provides null,
                LocalChatSubagents provides null, LocalChatWorkflows provides null, LocalReplyUsage provides emptyMap()) {
                fun expand(item: Item) { scope.launch { history.store.expand(window, item) } }
                val work = line as? ChatLine.Work
                val folding = history.store.folding.filter { it.window == window }.mapTo(hashSetOf()) { it.item }
                val failed = stringResource(R.string.chat_screen_fold_failed)
                val errors = history.store.failedFolds.filter { it.window == window }.associate { it.item to failed }
                ChatLineContent(line, window.session, loader, null,
                    onToggle = { id ->
                        if (history.store.toggle(window, id)) work?.steps?.filterIsInstance<WorkItem.Folded>()
                            ?.forEach { expand(it.item.item) }
                    },
                    onFold = ::expand, folding = folding, foldErrors = errors, style = style,
                    onFullText = { item -> onReadFullText(window.session, item) },
                    showToolViews = line !is ChatLine.Step || (window to line.groupId) !in workGroups)
            }
        }
    }
    if (history.missing) item(key = "history-missing") { JNotice(stringResource(R.string.chat_engine_history_missing)) }
    if (history.next != null) item(key = "history-more") {
        when {
            history.loading -> JLoading()
            history.failed -> {
                JNotice(stringResource(R.string.chat_engine_history_failed))
                JTextButton(stringResource(R.string.chat_engine_history_retry), onClick = history.load)
            }
            else -> JTextButton(stringResource(R.string.chat_screen_load_older), onClick = history.load)
        }
    }
}
