package persona.feature.chat

import android.content.Context
import android.content.res.Resources
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableState
import androidx.compose.runtime.State
import androidx.compose.runtime.getValue
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.FlowPreview
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.debounce
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import persona.core.chatlist.*
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.AppFeedback
import persona.core.domain.ChatTaps
import persona.core.domain.lines.ChatLine

// ChatPage's side effects, moved out of its body as they were (#17578): ART stops compiling a method past 10,000 code
// units, and every effect's keys, captured values and launch cost the page. Each group keeps its effects in their
// original order and reads the same state objects ChatPage writes.

/** The composer's effects: the draft callback, the wait for a /clear's new session, forwarded quotes and photos, an
 * assist draft, and what a delivered send or an answered request leaves to clean up. */
@Composable
internal fun ChatPageComposerEffects(
    key: SessionKey, composer: ChatComposerState, onDraftChanged: ((ChatComposerDraft) -> Unit)?,
    sessionList: List<Session>, clearingPane: MutableState<String?>, onContextCleared: ((SessionKey) -> Unit)?,
    allMessages: List<OutboxMessage>, allRequests: List<Interaction>, notice: MutableState<String?>, res: Resources,
    readOnly: Boolean, canSend: Boolean, imageLimits: ImageLimits, linkContexts: MutableState<List<ChatLinkContext>>,
    scope: CoroutineScope, context: Context, composerService: ChatComposerService?, feedback: AppFeedback?,
    submittedDraft: MutableState<Pair<String, ChatComposerDraft>?>, submittedContexts: MutableState<List<ChatLinkContext>>,
    taps: MutableState<ChatTaps>,
) {
    var clearing by clearingPane
    var shownNotice by notice
    var contexts by linkContexts
    val currentDraftCallback by rememberUpdatedState(onDraftChanged)
    LaunchedEffect(composer) { snapshotFlow { composer.draft }.collect { currentDraftCallback?.invoke(it) } }
    LaunchedEffect(sessionList, clearing) {
        clearing?.let { pane ->
            sessionList.firstOrNull { it.machineId == key.machineId && it.paneId == pane && it.key != key && it.isLive }?.let {
                clearing = null
                if (onContextCleared != null) onContextCleared(it.key)
                else shownNotice = res.getString(R.string.chat_screen_context_cleared)
            }
        }
    }
    // The new session gets 20 seconds to show up in the same pane; after that the list has to do, and /clear is offered again.
    LaunchedEffect(clearing) {
        if (clearing == null) return@LaunchedEffect
        delay(CLEAR_WAIT_MILLIS)
        clearing = null
        // A /clear still waiting to go out, or refused, says so on its own bubble.
        if (allMessages.none { it.session == key && it.text == SlashCommand.CLEAR && it.state != DeliveryState.Sent })
            shownNotice = res.getString(R.string.chat_screen_clear_sent)
    }
    // Forward placements arrive over the composer; the message action and forward-sheet UI lives in ChatMessageActions.
    val placedForwards = ForwardPlacements.waiting(key)
    LaunchedEffect(placedForwards, readOnly, canSend, imageLimits) {
        if (placedForwards == null || readOnly || !canSend) return@LaunchedEffect
        val fresh = mutableListOf<ForwardPlacement>()
        val duplicates = mutableListOf<RestoredPhotos>()
        ForwardPlacements.take(key).forEach { placement ->
            val next = withQuote(contexts, placement.ref)
            if (next.size == contexts.size) duplicates += placement.photos
            else {
                contexts = next
                fresh += placement
            }
        }
        // `take(key)` clears `placedForwards`, which cancels this LaunchedEffect on recomposition; finish the
        // taken handoff on the page scope so IO measurement and cleanup are not aborted mid-flight.
        scope.launch {
            duplicates.forEach { discardForwardedPhotos(context, it) }
            val rejected = attachForwardedPhotos(context, composer, fresh.map(ForwardPlacement::photos),
                limits = imageLimits, canSpill = composerService != null,
                onNotice = { message -> if (feedback?.notice(message) != true) shownNotice = message })
            val lost = fresh.sumOf { it.photos.lost } + rejected.size
            if (lost > 0) {
                val message = forwardedPhotoNotice(res, lost)
                if (feedback?.notice(message) != true) shownNotice = message
            }
            composer.focusRequest++
        }
    }
    BindPendingAssistDraft(
        key = key,
        context = context,
        composer = composer,
        readOnly = readOnly,
        canSend = canSend,
        existingContextIds = { contexts.mapTo(mutableSetOf()) { it.id } },
        onContext = { ref -> contexts = contexts + ref },
        onPhotoLimit = {
            val limitNotice = res.getString(R.string.chat_composer_photo_limit, imageLimits.perMessage)
            if (feedback?.notice(limitNotice) != true) shownNotice = limitNotice
        },
        limits = imageLimits,
    )
    LaunchedEffect(allMessages, submittedDraft.value) {
        submittedDraft.value?.let { (id, draft) ->
            if (allMessages.any { it.clientMessageId == id }) {
                composer.clearIfUnchanged(draft)
                contexts = contexts.filterNot { ref -> submittedContexts.value.any { it.id == ref.id } }
                submittedContexts.value = emptyList()
                submittedDraft.value = null
            }
        }
    }
    LaunchedEffect(allRequests) {
        taps.value = taps.value.pending(allRequests.map { it.id }.toSet())
    }
}

/** The list's effects: the header's own fold, placing and paging the list, search and message jumps, and reporting
 * what was read. [seenBlocked] is true while something covers the list or it is not settled; the read report starts
 * over when it clears, as it did when any of those values changed. */
@OptIn(FlowPreview::class)
@Composable
internal fun ChatPageListEffects(
    key: SessionKey, repository: ChatRepository, chatList: ChatListState, search: ChatSearchState, lines: List<ChatLine>,
    listKeys: List<String>, ready: Boolean, entry: ChatListEntry?, hasOlder: Boolean, loadingOlder: Boolean,
    loading: Boolean, onFreeze: () -> Unit, onFollow: () -> Unit, feedback: AppFeedback?, notice: MutableState<String?>,
    res: Resources, foldErrors: Set<String>, decisionSource: persona.core.navigation.DecisionSource?, offline: Boolean,
    jumpItem: MutableState<String?>, headerFold: ChatHeaderFold, foldsHeader: Boolean, reduceMotion: Boolean,
    headerBusy: Boolean, list: LazyListState, seenBlocked: Boolean, currentLinesByKey: State<Map<String, ChatLine>>,
    reportedSeen: MutableState<Item?>,
) {
    val busy by rememberUpdatedState(headerBusy)
    LaunchedEffect(headerFold, foldsHeader, reduceMotion) {
        if (!foldsHeader || reduceMotion) return@LaunchedEffect
        while (true) {
            delay(HeaderFoldRules.TICK_MILLIS)
            headerFold.tick(HeaderFoldRules.TICK_MILLIS, busy, reduceMotion)
        }
    }
    val rowAliases = androidx.compose.runtime.remember(lines) { lines.associate { it.id to it.contentKey } }
    ChatListEffects(chatList, listKeys,
        ready = ready, entry = entry,
        rowAliases = rowAliases,
        hasOlder = hasOlder, loadingOlder = loadingOlder,
        onFreeze = onFreeze,
        onFollow = onFollow,
        loadOlder = {
            try { repository.loadOlder(key) } catch (e: CancellationException) { throw e }
            catch (e: Exception) { if (feedback?.failure(e) != true) notice.value = persona.core.designsystem.operationFeedback(e).text.resolve(res) }
        })
    ChatSearchNavigation(search, chatList, lines, foldErrors)
    JumpToDecisionSource(key, decisionSource, repository, offline = offline)
    ChatMessageJump(key, repository, chatList, lines, hasOlder, loading || loadingOlder, search,
        onItem = { jumpItem.value = it }, onNotice = { notice.value = it })
    val linesByKey by currentLinesByKey
    var seen by reportedSeen
    LaunchedEffect(list, seenBlocked) {
        if (seenBlocked) return@LaunchedEffect
        snapshotFlow {
            if (list.isScrollInProgress) null else list.layoutInfo.visibleItemsInfo.mapNotNull { visible ->
                linesByKey[visible.key]?.seenItem()
            }.maxWithOrNull(chatItemOrder)
        }.debounce(450).distinctUntilChanged().collect { item ->
            if (item != null && (seen == null || chatItemOrder.compare(item, seen!!) > 0)) try {
                repository.markSeen(key, item.id)
                seen = item
            } catch (e: CancellationException) { throw e } catch (_: Exception) { }
        }
    }
}

/** The content's effects: folding a finished run, loading the folds open lines need, the message menu's quote actions,
 * a linked subagent's fold, and parsing the newest replies' Markdown before the list is placed. */
@Composable
internal fun ChatPageContentEffects(
    chatList: ChatListState, openWork: Set<String>, liveWorkLines: Set<String>, previousLiveWork: MutableState<Set<String>>,
    folding: Set<String>, projection: persona.core.domain.lines.ChatSnapshot, foldErrors: Set<String>, expand: (Item) -> Unit,
    menuWrites: Boolean, messageMenu: MessageMenuState, quote: (MessageTarget) -> Unit,
    quoteWords: (MessageTarget, String) -> Unit, childSessionId: String?, subagentFolds: SubagentFolds,
    liveItems: ChatLiveItems, markdownSplit: MarkdownPrewarm, markdownCache: ChatMarkdownCache,
    markdownPaths: PathPresence?, markdownReadyFor: MutableState<List<Item>?>,
) {
    // Fold the completed run, but keep choices made on historical lines and transfer an opened placeholder.
    LaunchedEffect(liveWorkLines) {
        val settled = settleWorkOpen(openWork.toSet(), liveWorkLines, previousLiveWork.value)
        previousLiveWork.value = liveWorkLines
        if (settled != openWork.toSet()) chatList.setExpanded(settled)
    }
    LaunchedEffect(folding) { chatList.noteExpansionLoading(folding.isNotEmpty()) }
    // Restored open lines and folds arriving while expanded load just as a manual tap does.
    LaunchedEffect(projection) {
        openFoldsToFetch(projection.lines, folding, foldErrors).forEach(expand)
    }
    val swipeQuote by rememberUpdatedState(quote)
    val pickedQuote by rememberUpdatedState(quoteWords)
    LaunchedEffect(menuWrites) {
        messageMenu.quote = if (menuWrites) ({ target: MessageTarget -> swipeQuote(target) }) else null
        messageMenu.quoteWords = if (menuWrites) ({ target: MessageTarget, words: String -> pickedQuote(target, words) }) else null
    }
    LaunchedEffect(childSessionId, subagentFolds) {
        childSessionId?.let { subagentFolds.open(persona.core.domain.lines.ChildSessionCards.call(it)) }
    }
    val currentMarkdownPaths by rememberUpdatedState(markdownPaths)
    LaunchedEffect(liveItems) {
        liveItems.revisions.collect { revision ->
            // Replies render each text part under "<item id>/<part index>" (MessageContent), so prewarm those keys, each with
            // the source its row parses there (MarkdownText.source): the same key and source, so the row finds the parse.
            val paths = currentMarkdownPaths
            val (latest, earlier) = withContext(Dispatchers.Default) {
                markdownSplit.split(revision).let { (latest, earlier) ->
                    latest.map { it.key to it.source(paths) } to earlier.map { it.key to it.source(paths) }
                }
            }
            markdownCache.keep(latest.map { it.first } + earlier.map { it.first })
            latest.forEach { (id, source) -> markdownCache.parse(id, source) }
            // Readiness still describes the structural snapshot, never the lazy live overlay's list identity.
            markdownReadyFor.value = revision.base
            earlier.forEach { (id, source) -> markdownCache.parse(id, source) }
        }
    }
}
