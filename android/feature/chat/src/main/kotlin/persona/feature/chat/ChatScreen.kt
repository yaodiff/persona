package persona.feature.chat

import persona.core.designsystem.dense
import persona.core.designsystem.text
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.fadeOut
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.LocalContentColor
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.luminance
import androidx.compose.ui.input.nestedscroll.nestedScroll
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.compose.currentStateAsState
import persona.core.chatlist.*
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import androidx.compose.ui.semantics.clearAndSetSemantics
import persona.core.domain.ChatTaps
import persona.core.domain.chatBubbles
import persona.core.domain.lines.ChatLine
import persona.core.domain.models.AgentModelPanel
import persona.core.domain.lines.isLiveStatus
import persona.core.domain.lines.placeFailed
import persona.core.domain.sort.READ_ONLY
import persona.core.domain.sort.canApprove
import persona.core.domain.sort.canChangeEffortOnly
import persona.core.domain.sort.canChangeModes
import persona.core.domain.sort.canInterrupt
import persona.core.domain.sort.canSend
import persona.core.domain.sort.isStoppedAgent
import persona.core.domain.sort.TerminalResume
import persona.core.domain.sort.terminalResume
import persona.core.domain.sort.canTerminal
import persona.core.domain.sort.canTypeClaudeCommands
import persona.core.domain.sort.shownCloseout
import persona.feature.chat.media.ChatImageViewer
import persona.core.domain.rewind.Rewind
import dagger.hilt.android.EntryPointAccessors
import dagger.hilt.internal.GeneratedComponentManager
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import java.util.UUID
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.FlowPreview
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.debounce
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.drop
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

// Finish the fade in the existing gap below the tab strip, without changing the chat's content padding.
private val TabbedChatScrollEdgeStyle = ScrollEdgeStyle.Gradient.copy(overlap = Tokens.Space.Standard.s)

/** The shell supplies repositories and optional terminal/navigation affordances, never wire clients. */
@OptIn(ExperimentalMaterial3Api::class, FlowPreview::class)
@Composable
fun ChatScreen(
    sessionKey: SessionKey, repository: ChatRepository, sender: MessageSender,
    interactions: InteractionCenter, sessions: SessionsRepository, onBack: () -> Unit,
    onTerminal: ((SessionKey) -> Unit)? = null,
    /** Resume in Terminal (#5565): open the terminal and resume there; null when the shell has no terminal. */
    onResumeInTerminal: ((SessionKey) -> Unit)? = null,
    chrome: ChatChromeBindings = ChatChromeBindings(),
    pendingBadge: @Composable () -> Unit = { PendingBadge(PendingStatus.NeedsReview) },
    readingKey: ChatReadingKey? = null,
    anchorStore: ChatAnchorStore? = null,
    renderStyle: ChatRenderStyle = ChatRenderStyle.Agent,
    textUpdates: Map<String, ChatTextUpdate> = emptyMap(),
    composerService: ChatComposerService? = null,
    composerState: ChatComposerState? = null,
    onDraftChanged: ((ChatComposerDraft) -> Unit)? = null,
    onContextCleared: ((SessionKey) -> Unit)? = null,
    statusBindings: ChatStatusBindings = ChatStatusBindings(),
    embedded: Boolean = false,
    showTopBar: Boolean = true,
    historyReadOnly: Boolean = false,
    historyFooter: (@Composable () -> Unit)? = null,
    /** Display metadata for a history entry that is absent from the live session list. */
    historySession: Session? = null,
) {
    PersonaTheme(darkTheme = MaterialTheme.colorScheme.background.luminance() < 0.5f) {
        val storage = repository as? ChatLocalStorage
        val local by (storage?.localStorage ?: remember { kotlinx.coroutines.flow.MutableStateFlow<ChatStorageSnapshot?>(null) })
            .collectAsStateWithLifecycle()
        val scopedReadingKey = readingKey ?: local?.let { ChatReadingKey(it.scope, sessionKey.path) }
        key(sessionKey, scopedReadingKey) {
            val readOnlyRefusal = androidx.compose.ui.res.stringResource(R.string.chat_read_only_history)
            val pageSender = remember(sender, historyReadOnly) { if (historyReadOnly) ReadOnlyMessageSender(sender, readOnlyRefusal) else sender }
            ChatPage(sessionKey, repository, pageSender, interactions, sessions, onBack, onTerminal, onResumeInTerminal, chrome,
                scopedReadingKey, anchorStore ?: local?.anchors,
                renderStyle, textUpdates,
                composerService, composerState ?: rememberChatComposerState(), onDraftChanged, onContextCleared, statusBindings,
                embedded, showTopBar, historyReadOnly, historyFooter, historySession)
        }
    }
}

@OptIn(ExperimentalMaterial3Api::class, FlowPreview::class)
@Composable
private fun ChatPage(
    key: SessionKey, repository: ChatRepository, sender: MessageSender,
    interactions: InteractionCenter, sessions: SessionsRepository, onBack: () -> Unit,
    onTerminal: ((SessionKey) -> Unit)?, onResumeInTerminal: ((SessionKey) -> Unit)?, chrome: ChatChromeBindings,
    readingKey: ChatReadingKey?, anchorStore: ChatAnchorStore?,
    renderStyle: ChatRenderStyle, textUpdates: Map<String, ChatTextUpdate>,
    composerService: ChatComposerService?, composer: ChatComposerState,
    onDraftChanged: ((ChatComposerDraft) -> Unit)?, onContextCleared: ((SessionKey) -> Unit)?,
    statusBindings: ChatStatusBindings,
    embedded: Boolean, showTopBar: Boolean,
    historyReadOnly: Boolean, historyFooter: (@Composable () -> Unit)?,
    historySession: Session?,
) {
    val pageSources = rememberChatPageSources(key, repository, sender, interactions, sessions, chrome, textUpdates,
        statusBindings)
    val scope = pageSources.scope
    val haptics = pageSources.haptics
    val res = pageSources.res
    val recovery = pageSources.recovery
    val snapshot by pageSources.snapshot
    val liveTextUpdates = pageSources.liveTextUpdates
    val renamer = pageSources.renamer
    val sessionList by pageSources.sessionList
    val managerChoice by pageSources.managerChoice
    val machines by pageSources.machines
    val allRequests by pageSources.allRequests
    val allMessages by pageSources.allMessages
    val adoptedSessions by pageSources.adoptedSessions
    val startedThreads by pageSources.startedThreads
    val writtenAt by pageSources.writtenAt
    val sendReceipts = pageSources.sendReceipts
    val pageSession = rememberChatPageSession(key, chrome, renderStyle, historyReadOnly, historyFooter, historySession, res,
        sessionList, adoptedSessions)
    val child = pageSession.child
    val workflowAgent = pageSession.workflowAgent
    val historyOnly = pageSession.historyOnly
    val footer = pageSession.footer
    val readOnly = pageSession.readOnly
    val row = pageSession.row
    val session = pageSession.session
    val codexThread = session?.takeIf { it.awaitingFirstMessage && !historyOnly }?.let {
        startedThreads[key] ?: codexThreadOf(sessionList, it)
    }
    val followSession = chrome.onFollowSession ?: onContextCleared ?: chrome.onOpenSession
    LaunchedEffect(codexThread?.key) {
        codexThread?.let { followSession?.invoke(it.key) }
    }
    // A launcher switch this page recorded or dropped (#5055), drawn at once until the row's next push says what the hub has.
    val pageCloseout = rememberChatPageCloseout(key, chrome, renderStyle, child, workflowAgent, row, session)
    var drawnSwitch by pageCloseout.drawnSwitch
    val switchSession = pageCloseout.switchSession
    val closeoutWork = pageCloseout.closeoutWork
    val closeoutShown = pageCloseout.closeoutShown
    // What the session's source lets the phone do (docs/protocol/protocol.md "Codex sources and capabilities"). Older sessions, and one whose
    // metadata has not arrived, can do everything: the hub and the node refuse what is not allowed anyway.
    val canSend = session?.canSend ?: true
    val canApprove = session?.canApprove ?: true
    val canTerminal = session?.canTerminal ?: true
    val connection = chrome.connection?.state?.collectAsStateWithLifecycle()?.value
    val controls = remember(key, chrome.actions, renamer, interactions) {
        ChatChromeState(res, chrome.actions, scope, sessions::refresh, onBack, renamer, interactions)
    }
    var headerOverlay by remember { mutableStateOf(false) }
    var modeOverlay by remember { mutableStateOf(false) }
    var pillOverlay by remember { mutableStateOf(false) }
    var composerOverlay by remember { mutableStateOf(false) }
    val chromeOverlay = headerOverlay || modeOverlay || pillOverlay || composerOverlay
    // Each pending card folds on its own (iOS InteractionPanel's `collapsed`); the composer stays either way.
    val requestFolds = rememberRequestFolds()
    // What the cards' room is read from (PendingStack.cap): the bars' heights, and those of the two places the cards sit in,
    // Persona's task question above its entries and the stack below them.
    var topBarHeight by remember { mutableIntStateOf(0) }
    var footerHeight by remember { mutableIntStateOf(0) }
    var taskHeight by remember { mutableIntStateOf(0) }
    var stackHeight by remember { mutableIntStateOf(0) }
    val pageTrays = rememberChatPageTrays(key, chrome, renderStyle, connection)
    val assistantTrays = pageTrays.assistantTrays
    var trayApproving by pageTrays.trayApproving
    var traySending by pageTrays.traySending
    var trayPlan by pageTrays.trayPlan
    var traySchedule by pageTrays.traySchedule
    val traySendIds = pageTrays.traySendIds
    val inboxDrafts by pageTrays.inboxDrafts
    var traySchedules by pageTrays.traySchedules
    val foldsBackground = pageTrays.foldsBackground
    val pageRequests = rememberChatPageRequests(key, chrome, renderStyle, composerService, snapshot, sessionList, machines,
        allRequests, allMessages)
    val machine = pageRequests.machine
    var taps by pageRequests.taps
    var interactionFeedback by pageRequests.interactionFeedback
    var answeredHere by pageRequests.answeredHere
    var offerNext by pageRequests.offerNext
    val tasks = pageRequests.tasks
    val taskTodos by pageRequests.taskTodos
    val taskKeys = pageRequests.taskKeys
    val taskAgentSessions by pageRequests.taskAgentSessions
    val requests = pageRequests.requests
    val assistantChat = pageRequests.assistantChat
    val taskAsk = pageRequests.taskAsk
    val stackCards = pageRequests.stackCards
    val messages = pageRequests.messages
    val composerDeliveries by pageRequests.composerDeliveries
    var submittedDraft by remember { mutableStateOf<Pair<String, ChatComposerDraft>?>(null) }
    var clearingPane by rememberSaveable { mutableStateOf<String?>(null) }
    var linkContexts by rememberSaveable(stateSaver = ChatLinkContextsSaver) { mutableStateOf(emptyList<ChatLinkContext>()) }
    var conversationLauncher by rememberSaveable(key.path, chrome.assistantManagement) { mutableStateOf<String?>(null) }
    var conversationModel by rememberSaveable(key.path, chrome.assistantManagement) { mutableStateOf<String?>(null) }
    var submittedContexts by remember { mutableStateOf(emptyList<ChatLinkContext>()) }
    var notice by remember { mutableStateOf<String?>(null) }
    // /model in the "/" list asked for the "Mode" sheet: a one-off, cleared once the sheet opened (not replayed when the bar returns).
    var openModes by remember { mutableStateOf(false) }
    // The host's failure notice whose "Retry" went out: hidden until the list catches up; back if that send failed.
    var hostRetried by rememberSaveable(key.path) { mutableStateOf<String?>(null) }
    val deliveryActions = remember(sender) { ChatDeliveryActions(sender) }
    var statusSheet by remember { mutableStateOf(false) }
    // The loop and background work's sheet, from the header's pill (Glass, #4339 C8).
    var activitySheet by remember { mutableStateOf(false) }
    // The shell presents transient notices once; persistent sync errors stay inline.
    val feedback = persona.core.designsystem.LocalAppFeedback.current
    // A message only on this phone taken off (Cancel send / Delete): nothing was sent; Undo puts it back (#4645,
    // docs/design/DESIGN_SYSTEM.md 4.4 principle 3).
    fun removed(message: OutboxMessage) {
        val deleted = res.getString(R.string.chat_delivery_deleted)
        val undo = persona.core.designsystem.FeedbackAction(res.getString(R.string.chat_delivery_undo)) { sender.restore(message) }
        if (feedback?.notice(deleted, undo) != true) notice = deleted
    }
    fun deliveryAction(message: OutboxMessage, action: DeliveryAction) {
        if (readOnly) return
        if (!message.state.allows(action, canSend)) return
        scope.launch {
            try {
                if (!deliveryActions.perform(message.clientMessageId, action, composer::restoreMessage, ::removed))
                    notice = res.getString(R.string.chat_screen_delivery_changed)
                else if (action == DeliveryAction.Discard) statusSheet = false
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { notice = res.getString(R.string.chat_screen_delivery_failed) }
        }
    }
    LaunchedEffect(notice) { notice?.let { if (feedback?.notice(it) == true) notice = null } }
    val currentDraftCallback by rememberUpdatedState(onDraftChanged)
    LaunchedEffect(composer) { snapshotFlow { composer.draft }.collect { currentDraftCallback?.invoke(it) } }
    LaunchedEffect(sessionList, clearingPane) {
        clearingPane?.let { pane ->
            sessionList.firstOrNull { it.machineId == key.machineId && it.paneId == pane && it.key != key && it.isLive }?.let {
                clearingPane = null
                if (onContextCleared != null) onContextCleared(it.key)
                else notice = res.getString(R.string.chat_screen_context_cleared)
            }
        }
    }
    // The new session gets 20 seconds to show up in the same pane; after that the list has to do, and /clear is offered again.
    LaunchedEffect(clearingPane) {
        if (clearingPane == null) return@LaunchedEffect
        delay(CLEAR_WAIT_MILLIS)
        clearingPane = null
        // A /clear still waiting to go out, or refused, says so on its own bubble.
        if (allMessages.none { it.session == key && it.text == SlashCommand.CLEAR && it.state != DeliveryState.Sent })
            notice = res.getString(R.string.chat_screen_clear_sent)
    }
    var sending by remember { mutableStateOf(false) }
    var sendingId by remember { mutableStateOf<String?>(null) }
    // A card open on its full page, in either place of cards.
    var taskOverlay by remember { mutableStateOf(false) }
    var stackOverlay by remember { mutableStateOf(false) }
    val overlay = taskOverlay || stackOverlay
    var terminalHelp by remember { mutableStateOf(false) }
    var taskPageVisible by remember { mutableStateOf(false) }
    var fullText by remember { mutableStateOf<String?>(null) }
    var linkVisible by remember { mutableStateOf(false) }
    val pageHeaderFold = rememberChatPageHeaderFold(readingKey, anchorStore, renderStyle, showTopBar, scope)
    val chatList = pageHeaderFold.chatList
    val search = pageHeaderFold.search
    val foldsHeader = pageHeaderFold.foldsHeader
    val activityInHeader = pageHeaderFold.activityInHeader
    val reduceMotion = pageHeaderFold.reduceMotion
    val headerFold = pageHeaderFold.headerFold
    val headerScroll = pageHeaderFold.headerScroll
    val openWork = pageHeaderFold.openWork
    val pageAwait = rememberChatPageAwait(snapshot, sendReceipts, readOnly, session, messages)
    var timelineVersion by pageAwait.timelineVersion
    val timeline = pageAwait.timeline
    val awaiting = pageAwait.awaiting
    val awaitingStatus = pageAwait.awaitingStatus
    val live = pageAwait.live
    // Waiting on you (the hub's state says needs you, or one of this session's requests is pending): the turn is still the
    // live one (it folds and opens as while it runs), but nothing is at work, so its line reads "Waiting for you" with no
    // clock, as the header does (web goes by the same state).
    val waitingOnYou = turnWaitsOnYou(session, key, requests)
    // Which items the lines were last built from: until the first build finishes, the page is still loading, not empty.
    var projectedFrom by remember { mutableStateOf<List<Item>?>(null) }
    val projection by produceState(persona.core.domain.lines.ChatSnapshot(emptyList(), emptyList(), persona.core.domain.lines.TaskIndex()),
        snapshot.items, live, openWork, timelineVersion) {
        val items = snapshot.items
        value = withContext(Dispatchers.Default) {
            timeline.lines(items, live, openWork.toSet())
        }
        projectedFrom = items
    }
    // Where the header counts "Working 22 min" from: the start of the turn that runs now (iOS `Turn.startedAt`).
    val runningSince = remember(projection) { projection.liveTurnId?.let { id -> projection.turns.firstOrNull { it.id == id }?.start } }
    val liveWorkLines = remember(projection) { liveWorkIds(projection.lines) }
    var previousLiveWork by remember { mutableStateOf(emptySet<String>()) }
    // Fold the completed run, but keep choices made on historical lines and transfer an opened placeholder.
    LaunchedEffect(liveWorkLines) {
        val settled = settleWorkOpen(openWork.toSet(), liveWorkLines, previousLiveWork)
        previousLiveWork = liveWorkLines
        if (settled != openWork.toSet()) chatList.setExpanded(settled)
    }
    val feeds by (tasks?.feeds ?: remember { kotlinx.coroutines.flow.MutableStateFlow(emptyMap<String, List<persona.core.contracts.FeedItem>>()) })
        .collectAsStateWithLifecycle()
    val feed = feeds[key.sessionId].orEmpty()
    val taskPlans by (tasks?.plans ?: remember { kotlinx.coroutines.flow.MutableStateFlow(emptyMap<String, TaskPlan>()) })
        .collectAsStateWithLifecycle()
    val taskCardProjection by (tasks?.taskCards ?: remember {
        kotlinx.coroutines.flow.MutableStateFlow(emptyMap<String, List<AssistantTaskCard>>())
    }).collectAsStateWithLifecycle()
    var backgroundOpen by rememberSaveable(key.path) { mutableStateOf(false) }
    // The message a hub link named (`?at=`, #2194) while it is being landed on: like a search hit, its drawer opens.
    val pageLines = rememberChatPageLines(key, renderStyle, snapshot, session, foldsBackground, tasks, taskTodos, search,
        openWork, projection, feed, taskPlans, taskCardProjection)
    var jumpItem by pageLines.jumpItem
    val assistantPresentation = pageLines.assistantPresentation
    val subagentCards = pageLines.subagentCards
    val lines = pageLines.lines
    val searchableItems by produceState(emptyList<Item>(), snapshot.items, timelineVersion, search.visible) {
        if (search.visible) value = withContext(Dispatchers.Default) { timeline.searchItems(snapshot.items) }
    }
    // Photos sent just before a message go into that message's bubble instead of a row of their own.
    val pageContent = rememberChatPageContent(lines)
    val contentUserImages = pageContent.contentUserImages
    val contentHiddenImages = pageContent.contentHiddenImages
    val contentSourceLabels = pageContent.contentSourceLabels
    val contentWorkGroups = pageContent.contentWorkGroups
    val contentLastSteps = pageContent.contentLastSteps
    val contentPlans = pageContent.contentPlans
    // Rows at the chat's newest end, under your unsent messages, as iOS draws them in an Agent's chat: a load error as a line
    // of the chat (Glass; Brief and Board say it above the composer), then "Claude is getting ready…" while your message
    // waits for its turn.
    val glass = PersonaTheme.style == AppStyle.Glass
    val agentChat = renderStyle != ChatRenderStyle.Assistant
    val errorLine = snapshot.error?.takeIf { glass && agentChat }
    // Persona's conversation says it too, by Persona's name (#4339 C18): a new conversation's first message waits there.
    val personaName = persona.core.designsystem.LocalPersonaName.current
    val readyLine = if (readOnly) null else gettingReady(res, session, messages, liveWork = liveWorkLines.isNotEmpty(),
        name = if (agentChat) chatAgentName(session) else personaName)
    val tailRows = listOfNotNull(ERROR_ROW.takeIf { errorLine != null }, READY_ROW.takeIf { readyLine != null })
    // Rows before the lines in the list: the tail rows and the unsent messages.
    // The newest "while you were away" recap open as the last line until tapped or typing starts (Glass, an Agent's chat).
    val pageRecap = rememberChatPageRecap(key, composer, readOnly, lines, glass, agentChat)
    val openRecap = pageRecap.openRecap
    val pageReader = rememberChatPageReader(key, repository, snapshot, chatList, lines)
    var planSheet by pageReader.planSheet
    var sheetPlan by pageReader.sheetPlan
    val currentItems by pageReader.currentItems
    val currentLinesByKey by pageReader.currentLinesByKey
    val list = pageReader.list
    var reportedSeen by pageReader.reportedSeen
    val newestVisibleItem by pageReader.newestVisibleItem
    val readThrough = pageReader.readThrough
    val context = pageReader.context
    val pairingLoader = pageReader.pairingLoader
    val loader = pageReader.loader
    val markdownCache = pageReader.markdownCache
    // Only fresh Agent replies preload their links (ChatLinks.toPreload): paging old history never does.
    val preloadUrls by produceState(emptyList<String>(), snapshot.items) {
        value = withContext(Dispatchers.Default) { ChatLinks.toPreload(snapshot.items, java.time.Instant.now()) }
    }
    // The items whose latest turn has its Markdown parsed. An unread conversation opens on its last question and waits
    // for this: a reply drawn as plain text and then as Markdown grows after the list was placed, and in the reversed
    // list that pushes the question off the top (the newest reply is the one that keeps its place).
    val pageFolds = rememberChatPageFolds(key, onTerminal, renderStyle, readOnly, canTerminal, pairingLoader, loader,
        onResumeInTerminal)
    var markdownReadyFor by pageFolds.markdownReadyFor
    val markdownSplit = pageFolds.markdownSplit
    var folding by pageFolds.folding
    var foldErrors by pageFolds.foldErrors
    val terminal = pageFolds.terminal
    val hasTerminal = pageFolds.hasTerminal
    val agentTerminal = pageFolds.agentTerminal
    val resumeInTerminal = pageFolds.resumeInTerminal
    val lifecycle by pageFolds.lifecycle

    fun followEnd() {
        chatList.returnToLatest()
    }
    fun expand(item: Item) {
        val fold = item.fold ?: return
        if (item.id in folding) return
        folding = folding + item.id; foldErrors = foldErrors - item.id
        scope.launch {
            try {
                val page = repository.expandFold(key, item.seq, fold.throughSeq)
                require(page.isNotEmpty() && page.none { it.kind == "fold" })
                timeline.unfold(item, page); timelineVersion++
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { foldErrors = foldErrors + (item.id to res.getString(R.string.chat_screen_fold_failed)) }
            finally { folding = folding - item.id }
        }
    }
    fun toggle(id: String) {
        val (next, opened) = toggleWork(WorkOpenState(openWork.toSet()), id)
        chatList.setExpanded(next.open)
        if (opened) (projection.lines.firstOrNull { it.id == id } as? ChatLine.Work)?.steps
            ?.filterIsInstance<persona.core.domain.lines.WorkItem.Folded>()?.forEach { expand(it.item.item) }
    }
    // Restored open lines and folds arriving while expanded load just as a manual tap does.
    LaunchedEffect(projection) {
        openFoldsToFetch(projection.lines, folding, foldErrors.keys).forEach(::expand)
    }
    // The chat's model operating a Mac (#4339 C9 and C18, iOS ComputerActivityPill), on Glass: a pill over an Agent chat's
    // composer, under the entries in Persona's conversation; a tap opens the work line that holds the call.
    val computer = remember(glass, readOnly, snapshot.items, machines, key.machineId) {
        if (glass && !readOnly) persona.core.domain.ComputerActivity.latest(snapshot.items, key.machineId, machines) else null
    }
    val computerLine = computer?.let { a -> computerWorkLine(lines, a.toolUseId) }
    val computerPill: (@Composable (Modifier) -> Unit)? = computer?.let { a -> { modifier -> ChatComputerPill(a, onOpen = computerLine?.let { line -> {
        if (!line.open) toggle(line.id)
        if (!chatList.followsEnd) scope.launch { chatList.scrollToRecord(line.contentKey) }
    } }, modifier) } }
    /** Whether the message was taken (false: nothing went, the box or another send stands); [onFailed] if it then fails. */
    fun send(command: String? = null, onFailed: () -> Unit = {}): Boolean {
        if (readOnly || !canSend) return false
        if (sending || submittedDraft != null) return false
        if (command == null && ((!composer.hasContent && linkContexts.isEmpty()) || !composer.ready)) return false
        val draft = composer.draft
        val refs = if (command == null) linkContexts else emptyList()
        val wireText = command ?: linkMessage(composer.message(), refs, context.resources)
        val id = UUID.randomUUID().toString()
        if (command == null) { submittedDraft = id to draft; submittedContexts = refs }
        if (command == "/clear") clearingPane = session?.paneId
        sendingId = id
        sending = true
        followEnd()
        scope.launch {
            try {
                // Routed to a task ("Say to "xx""): its accepted response, not launching the request, consumes the route
                // and clears the draft below. Until then this has the same pending state as an ordinary send (#5949).
                val routed = command == null && tasks != null && refs.isEmpty() && draft.attachments.isEmpty() &&
                    tasks.sendRouted(key.sessionId, draft.text.trim())
                if (!routed) {
                    // The user's own message carries out a launcher switch waiting on Glass, where the pill shows it (#5055):
                    // `apply_switch`; any other message leaves it waiting.
                    if (command == null && switchesNext(switchSession, glass && agentChat)) {
                        sender.sendApplyingSwitch(key, wireText, id)
                        drawnSwitch = null
                    } else sender.send(key, wireText, id, null, conversationLauncher, conversationModel)
                }
                if (command == null) {
                    composer.clearIfUnchanged(draft)
                    linkContexts = linkContexts.filterNot { ref -> refs.any { it.id == ref.id } }
                    submittedDraft = null; submittedContexts = emptyList()
                }
            }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) {
                submittedDraft = null; submittedContexts = emptyList()
                if (command == SlashCommand.CLEAR) clearingPane = null
                onFailed()
                if (feedback?.failure(e) != true) notice = operationFeedback(e).text.resolve(res)
            }
            finally { sending = false; sendingId = null }
        }
        return true
    }
    // Chat content (group A): the message menu, the image viewer, photos and paths in messages, the API error retry.
    val messageMenu = remember { MessageMenuState() }
    // A swipe to the left quotes (iOS #1058) wherever the long-press menu's "Quote" is offered.
    val quoteTarget: (MessageTarget) -> Unit = { target ->
        // Over the box as a quote that goes with the next message, the draft left as it is (iOS #986).
        linkContexts = withQuote(linkContexts, messageQuote(target.text, target.user, session?.agentLabel ?: "Claude", target.id))
        composer.focusRequest++
    }
    val swipeQuote by rememberUpdatedState(quoteTarget)
    // Quote in the menu of a selected message's words quotes the words picked (iOS Select Text › Quote).
    val quoteWords: (MessageTarget, String) -> Unit = { target, words ->
        linkContexts = withQuote(linkContexts, messageQuote(words, target.user, session?.agentLabel ?: "Claude", target.id, ChatLinkContext.PART_SELECTION))
        composer.focusRequest++
    }
    val pickedQuote by rememberUpdatedState(quoteWords)
    val menuWrites = messageMenuWrites(readOnly, session)
    LaunchedEffect(menuWrites) {
        messageMenu.quote = if (menuWrites) ({ target: MessageTarget -> swipeQuote(target) }) else null
        messageMenu.quoteWords = if (menuWrites) ({ target: MessageTarget, words: String -> pickedQuote(target, words) }) else null
    }
    // "Branch to new chat" (#2137) from a reply, "Rewind to here" (#2436) on your message: the menu's session-wide extras (web ⋯).
    var forking by remember { mutableStateOf(false) }
    var rewindBusy by remember { mutableStateOf(false) }
    val actionsService = chrome.actions
    val openFork = chrome.forkOpener(renderStyle)
    val onFork: ((String) -> Unit)? = if (actionsService != null && openFork != null && session?.canFork == true && !readOnly) { itemId ->
        val from = session
        if (!forking) {
            forking = true
            scope.launch {
                try {
                    val id = actionsService.fork(from, itemId)
                    notice = res.getString(R.string.chat_menu_forked)
                    try { sessions.refresh() } catch (e: CancellationException) { throw e } catch (_: Exception) { }
                    openFork(SessionKey(from.machineId, id))
                } catch (e: CancellationException) { throw e }
                catch (e: Exception) { notice = res.getString(R.string.chat_menu_fork_failed, operationFeedback(e).text.resolve(res)) }
                finally { forking = false }
            }
        }
    } else null
    val rewindBlock = Rewind.block(session, busy = sending || Rewind.outgoing(messages.map { it.state }) || rewindBusy)
    val rewindMenu = if (actionsService != null && canSend && !readOnly && renderStyle != ChatRenderStyle.Assistant) RewindMenu(
        block = rewindBlock?.let { res.getString(when (it) {
            Rewind.Block.ClaudeOnly -> R.string.chat_rewind_block_claude_only
            Rewind.Block.Unsupported -> R.string.chat_rewind_block_unsupported
            Rewind.Block.Offline -> R.string.chat_rewind_block_offline
            Rewind.Block.NeedsYou -> R.string.chat_rewind_block_needs_you
            Rewind.Block.Working -> R.string.chat_rewind_block_working
        }) },
        // "Code and conversation" only where the node says it puts files back too (#6294), or says nothing (a Claude terminal).
        code = Rewind.restoresCode(session),
        run = { target, restore ->
            val at = Rewind.target(snapshot.items, target.id)
            val on = session
            if (at == null) notice = res.getString(R.string.chat_rewind_no_text)
            else if (on != null && !rewindBusy) {
                rewindBusy = true
                notice = res.getString(R.string.chat_rewind_running)
                scope.launch {
                    try {
                        val done = actionsService.rewind(on, at, restore)
                        // The message goes back into the composer, as Claude's own /rewind puts it back in its box.
                        if (done.inputCleared) composer.edit(target.editText)
                        notice = res.getString(when {
                            // Claude's own box on the computer: only Claude's /rewind in its terminal (a session whose node
                            // declares no rewind of its own) has one to say this of; a runtime or Codex goes back itself (#6294).
                            !done.inputCleared && on.capabilities?.rewind != true -> R.string.chat_rewind_input_kept
                            restore == RewindRestore.CodeAndConversation && !done.codeRestored -> R.string.chat_rewind_done_no_code_changes
                            done.codeRestored -> R.string.chat_rewind_done_code_and_conversation
                            else -> R.string.chat_rewind_done_conversation
                        })
                    } catch (e: CancellationException) { throw e }
                    catch (e: RewindFailure) {
                        notice = when (e.reason) {
                            RewindFailure.Reason.HubUnsupported -> res.getString(R.string.chat_rewind_hub_unsupported)
                            RewindFailure.Reason.NeedsFullDevice -> res.getString(R.string.chat_rewind_needs_full_device)
                            RewindFailure.Reason.Refused -> res.getString(R.string.chat_rewind_failed,
                                e.hubMessage ?: operationFeedback(e.cause ?: e).text.resolve(res))
                        }
                    }
                    catch (e: Exception) { notice = res.getString(R.string.chat_rewind_failed, operationFeedback(e).text.resolve(res)) }
                    finally {
                        rewindBusy = false
                        try { sessions.refresh() } catch (e: CancellationException) { throw e } catch (_: Exception) { }
                    }
                }
            }
        }) else null
    // The session's subagents by the call that started them, done ones too: what their task cards say (Glass), and the
    // conversations "View sub-agent" opens under Task / Agent steps in Brief and Board.
    val pageSubagents = rememberChatPageSubagents(key, chrome, readOnly, session)
    val subagentLinks = pageSubagents.subagentLinks
    val subagentFolds = pageSubagents.subagentFolds
    val workflowCards = pageSubagents.workflowCards
    var viewer by remember { mutableStateOf<ViewerRequest?>(null) }
    val photos = remember(chrome.files, key, session?.cwd) { chrome.files?.let { AttachedPhotos(it, key.machineId, session?.cwd.orEmpty()) } }
    // A "Continue" already on its way counts as working: the card offers no second one.
    // A turn that failed on the refusal fallback's missing model (#2528): "Continue" would go to the same model.
    val retryId = retryLine(projection.lines, session?.status == "working" || messages.isNotEmpty(),
        !readOnly && canSend && session?.turnErrorCode != FALLBACK_UNAVAILABLE)
    val currentOpenPath by rememberUpdatedState(chrome.onOpenPath)
    // Paths in messages link only when there on the machine (PathLinks.kt): asked of the session's directory.
    val pathPresence = remember(chrome.files, key, session?.cwd, chrome.onOpenPath != null) {
        val files = chrome.files
        val cwd = session?.cwd.orEmpty()
        if (files == null || cwd.isBlank() || chrome.onOpenPath == null) null
        else PathPresence(scope, { paths -> files.stat(key.machineId, cwd, paths) })
    }
    val currentSend by rememberUpdatedState<(String) -> Unit>({ send(it) })
    // Stays the same object while the conversation streams: rows read it, and a new one would redraw them all.
    val continueText = stringResource(R.string.chat_screen_continue_message)
    val imageDescription = stringResource(R.string.chat_content_image)
    val rowActions = remember(key, repository, photos, retryId, chrome.onOpenPath != null, pathPresence, chrome.files, session?.cwd) {
        ChatRowActions(key, fullText = { item -> repository.fullText(key, item.id) },
            openPath = if (chrome.onOpenPath != null) ({ path, line -> currentOpenPath?.invoke(path, line) }) else null,
            paths = pathPresence,
            photos = photos, pictures = { chatViewerImages(key, currentItems, repository, imageDescription) },
            openImages = { images, index -> viewer = ViewerRequest(images, index) },
            menu = messageMenu, retryLineId = retryId, onRetry = { currentSend(continueText) }, imageDescription = imageDescription,
            thumbnails = chrome.files?.let { files ->
                val cwd = session?.cwd.orEmpty().ifBlank { "~" }
                ({ path -> "${key.machineId}\n$cwd\n$path" to { pixels: Int -> files.thumbnail(key.machineId, cwd, path, pixels) } })
            }, viewerImage = { item, description -> chatViewerImage(key, item, repository, description) })
    }
    LaunchedEffect(snapshot.items) {
        // Replies render each text part under "<item id>/<part index>" (MessageContent), so prewarm those keys, each with
        // the source its row parses there (MarkdownText.source): the same key and source, so the row finds the parse.
        val items = snapshot.items
        val paths = rowActions.paths?.takeIf { rowActions.openPath != null }
        val (latest, earlier) = withContext(Dispatchers.Default) {
            markdownSplit.split(items).let { (latest, earlier) -> latest.map { it.key to it.source(paths) } to earlier.map { it.key to it.source(paths) } }
        }
        markdownCache.keep(latest.map { it.first } + earlier.map { it.first })
        latest.forEach { (id, source) -> markdownCache.parse(id, source) }
        markdownReadyFor = items
        earlier.forEach { (id, source) -> markdownCache.parse(id, source) }
    }
    // A task's detail or the dock's "All", open over the conversation; kept across the host being made again.
    val taskSheet = remember(key) { mutableStateOf<TaskSheet?>(null) }
    LaunchedEffect(tasks, taskKeys, taskTodos, taskSheet.value) {
        val selected = (taskSheet.value as? TaskSheet.Task)?.todoId
        val detailKeys = taskTodos.firstOrNull { it.id == selected }?.activeAgents.orEmpty().map { it.key }
        tasks?.watchAgents(taskKeys.map(GroupKeys::rawKey).toSet() + detailKeys)
    }
    DisposableEffect(tasks, key) { onDispose { tasks?.watchAgents(emptySet()) } }
    // the cards still to answer: one just answered here no longer says what it asked
    val taskCards = remember(allRequests, taps.resolved) { taps.shown(allRequests) }
    val taskHost = tasks?.let { store ->
        val namespace = GroupKeys.namespaceOf(key.machineId)
        val localSessions = sessionList.filter { GroupKeys.namespaceOf(it.machineId) == namespace }
            .map { it.copy(machineId = GroupKeys.raw(it.machineId)) }
            .map { base -> base.copy(plan = listOfNotNull(base.plan, taskAgentSessions[base.key]?.plan)
                .maxByOrNull { parseTime(it.at) ?: java.time.Instant.MIN }) }
        AssistantTaskHost(store, key.sessionId, (localSessions + taskAgentSessions.values).distinctBy { it.key },
            { id -> machines.firstOrNull { it.id == GroupKeys.machine(id, namespace) }?.displayName ?: id },
            onOpen = { k -> chrome.onOpenSession?.invoke(GroupKeys.key(k, namespace)) }, onError = { notice = operationFeedback(it).text.resolve(res) },
            say = { text -> scope.launch { runCatching { sender.send(key, text, UUID.randomUUID().toString()) } } },
            sheet = taskSheet, machines = machines.filter { GroupKeys.namespaceOf(it.id) == namespace }.map { it.copy(id = GroupKeys.raw(it.id)) },
            pending = taskCards.filter { GroupKeys.namespaceOf(it.machineId) == namespace }.map { it.copy(machineId = GroupKeys.raw(it.machineId)) },
            manager = session?.manager,
            backgroundRecords = assistantPresentation.background, onOpenBackground = { backgroundOpen = true },
            onOpenSetup = chrome.onOpenSetup?.let { open -> { page: persona.core.contracts.BriefSetupPage -> open(page, session?.manager) } })
    }
    fun approveTray(captured: List<Pair<Interaction, Resolution>>) {
        if (trayApproving || taps.busy || readOnly) return
        trayApproving = true
        scope.launch {
            try {
                for ((interaction, resolution) in captured) {
                    val current = interactions.pending.value
                    val sourceSession = sessions.sessions.value.firstOrNull { it.key == interaction.sessionKey }
                        ?: taskAgentSessions[GroupKeys.rawKey(interaction.sessionKey)]
                    if (!taps.canTap(interaction, current, java.time.Instant.now()) || trayApproval(interaction, sourceSession) != resolution) continue
                    taps = taps.tapped(interaction.id)
                    try {
                        val result = submitInteraction { interactions.resolveOutcome(interaction.id, resolution) }
                        result.submission.haptic?.let(haptics)
                        taps = taps.answered(interaction.id, result.card)
                        if (!result.withdraw) notice = result.notice(res, interaction, resolution)
                        // An approval from the tray answers a card here too: the next one waiting elsewhere is offered.
                        else if (result.offersNext(resolution)) answeredHere++
                    } finally { taps = taps.idle() }
                }
            } finally { trayApproving = false }
        }
    }
    fun sendTray(draft: TrayDraft) {
        if (traySending || readOnly || !canSend) return
        traySending = true
        scope.launch {
            try {
                when (draft) {
                    is TrayDraft.Inbox -> {
                        val accepted = chrome.inboxScope?.let { scopeId ->
                            chrome.inboxTrays?.send(scopeId, draft.draft, traySendIds.getOrPut(draft.draft) { UUID.randomUUID().toString() })
                        } == true
                        if (!accepted) notice = res.getString(R.string.chat_tray_draft_changed)
                    }
                    is TrayDraft.Feed -> {
                        val latest = tasks?.feeds?.value?.get(key.sessionId)?.firstOrNull { it.id == draft.item.id }
                        if (latest != draft.item || !latest.canSend) notice = res.getString(R.string.chat_tray_draft_changed)
                        else tasks?.sendMessage(draft.item, draft.text) { notice = operationFeedback(it).text.resolve(res) }
                    }
                }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { notice = operationFeedback(error).text.resolve(res) }
            finally { traySending = false }
        }
    }
    trayPlan?.let { plan -> taskHost?.let { host ->
        JSheet(onDismiss = { trayPlan = null }, title = plan.tasks.firstOrNull()?.title, scrollable = true) {
            PlanCard(plan, host)
        }
    } }
    traySchedule?.let { schedule ->
        JSheet(onDismiss = { traySchedule = null }, title = schedule.title, scrollable = true) {
            Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                Text(schedule.prompt, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink)
                schedule.lastStatus?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3) }
                schedule.lastError?.let { JNotice(it, status = Status.NeedsYou) }
                schedule.nextRun?.let { Text(stringResource(R.string.chat_tray_next_run, it), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3) }
                val machineId = schedule.runMachineId ?: schedule.machineId
                val sessionId = schedule.runSessionId ?: schedule.lastSessionId ?: schedule.sessionId
                if (machineId != null && sessionId != null) JButton(stringResource(R.string.chat_task_detail_open_chat), {
                    traySchedule = null
                    chrome.onOpenSession?.invoke(GroupKeys.key(GroupKeys.rawKey(SessionKey(machineId, sessionId)), GroupKeys.namespaceOf(key.machineId)))
                })
            }
        }
    }
    // Drawn whatever the composer area shows: a read-only assistant conversation's task cards open their sheet too.
    taskHost?.let { AssistantTaskSheets(it) }
    if (backgroundOpen) CompositionLocalProvider(LocalAssistantTaskHost provides taskHost) {
        AssistantBackgroundRecords(assistantPresentation.background, key, loader,
            onToggle = ::toggle, onFold = ::expand, folding = folding, foldErrors = foldErrors, hideTaskPrompts = foldsBackground,
            onFullText = { item -> scope.launch {
                try { fullText = repository.fullText(key, item.id) }
                catch (e: CancellationException) { throw e }
                catch (e: Exception) { if (feedback?.failure(e) != true) notice = operationFeedback(e).text.resolve(res) }
            } },
            onDismiss = { backgroundOpen = false })
    }
    LaunchedEffect(allMessages, submittedDraft) {
        submittedDraft?.let { (id, draft) ->
            if (allMessages.any { it.clientMessageId == id }) {
                composer.clearIfUnchanged(draft)
                linkContexts = linkContexts.filterNot { ref -> submittedContexts.any { it.id == ref.id } }
                submittedContexts = emptyList()
                submittedDraft = null
            }
        }
    }
    LaunchedEffect(interactionFeedback) {
        if (interactionFeedback != null) { delay(3_000); interactionFeedback = null }
    }
    // As long as the web shows its "Answered" receipts (20 s); never moves on by itself.
    LaunchedEffect(answeredHere) {
        if (answeredHere > 0) { offerNext = true; delay(NEXT_WAITING_MILLIS); offerNext = false }
    }
    LaunchedEffect(allRequests) {
        taps = taps.pending(allRequests.map { it.id }.toSet())
    }
    val lastUserId = snapshot.items.lastOrNull { it.role == "user" && it.kind == "text" }?.id
    // Persona's last turn ended on a model set-up error the hub named: the fix replaces the error line (#4339 C18, iOS
    // `turnFix`). Persona's conversations only, as iOS and the web.
    val turnFix = if (renderStyle == ChatRenderStyle.Assistant) TurnErrorFix.of(session?.turnErrorCode) else null
    val fixModel = chrome.onFixModel?.let { open -> { action: persona.core.designsystem.ReadinessActionId -> open(action, session?.launcher, session?.manager) } }
    // Nothing is up over the page: the header may fold by itself (iOS waits while a sheet or menu is presented).
    val headerBusy by rememberUpdatedState(chrome.covered || overlay || chromeOverlay || linkVisible || statusSheet || activitySheet ||
        terminalHelp || taskPageVisible || fullText != null || search.visible)
    LaunchedEffect(headerFold, foldsHeader, reduceMotion) {
        if (!foldsHeader || reduceMotion) return@LaunchedEffect
        while (true) {
            delay(HeaderFoldRules.TICK_MILLIS)
            headerFold.tick(HeaderFoldRules.TICK_MILLIS, headerBusy, reduceMotion)
        }
    }
    // A request arriving opens it, so its line says the agent needs you (chats.md "Risks"); and so does coming back
    // from the terminal or the files over the chat (iOS: a pane switch).
    val pageListKeys = rememberChatPageListKeys(chrome, snapshot, writtenAt, session, connection, chromeOverlay, requests,
        messages, statusSheet, overlay, terminalHelp, taskPageVisible, fullText, linkVisible, chatList, search, foldsHeader,
        headerFold, lines, lifecycle, lastUserId)
    val unreadEntry = pageListKeys.unreadEntry
    val placed = pageListKeys.placed
    // Persona's first message, not in the transcript yet: its day stands over it on Glass, as the transcript's will once it
    // is there (iOS `DayLine`, #1911), so the line does not appear above it later. The day it was written, else today.
    val pendingDay = if (glass && !agentChat && lines.isEmpty() && placed.rest.isNotEmpty())
        placed.rest.mapNotNull { it.createdAt }.minOrNull()?.let { java.time.Instant.ofEpochMilli(it).atZone(java.time.ZoneId.systemDefault()).toLocalDate() }
            ?: java.time.LocalDate.now()
        else null
    // The list's keys from the bottom (reverse layout): the tail rows, the outbox's bubbles still on their way, that day,
    // then the lines, each with the failed sends written before it just above it (#2769).
    val listKeys = remember(lines, placed, tailRows, pendingDay != null) {
        buildList {
            addAll(tailRows)
            placed.rest.asReversed().forEach { add("outbox-${it.clientMessageId}") }
            if (pendingDay != null) add(PENDING_DAY_ROW)
            for (line in lines) {
                add(line.contentKey)
                placed.before[line.id].orEmpty().asReversed().forEach { add("outbox-${it.clientMessageId}") }
            }
        }
    }
    ChatListEffects(chatList, listKeys,
        ready = !snapshot.loading && (snapshot.items.isEmpty() || lines.isNotEmpty() ||
            (renderStyle.compactToolSummary && projectedFrom === snapshot.items && assistantPresentation.foreground.isEmpty())) &&
            (unreadEntry == null || markdownReadyFor == snapshot.items), entry = unreadEntry,
        rowAliases = lines.associate { it.id to it.contentKey },
        hasOlder = snapshot.hasOlder, loadingOlder = snapshot.loadingOlder,
        onFreeze = { timeline.freeze(currentItems); timelineVersion++ },
        onFollow = { timeline.follow(); timelineVersion++ },
        loadOlder = {
            try { repository.loadOlder(key) } catch (e: CancellationException) { throw e }
            catch (e: Exception) { if (feedback?.failure(e) != true) notice = operationFeedback(e).text.resolve(res) }
        })
    ChatSearchNavigation(search, chatList, lines, foldErrors.keys)
    ChatMessageJump(key, repository, chatList, lines, snapshot.hasOlder, snapshot.loading || snapshot.loadingOlder, search,
        onItem = { jumpItem = it }, onNotice = { notice = it })
    LaunchedEffect(list, chrome.covered, search.visible, search.pending, search.returning, chatList.restored, chatList.programmaticScroll, overlay, chromeOverlay, fullText, linkVisible, statusSheet, terminalHelp, taskPageVisible, lifecycle, snapshot.verified) {
        if (chrome.covered || search.visible || search.pending || search.returning || !chatList.restored || chatList.programmaticScroll || overlay || chromeOverlay || fullText != null || linkVisible || statusSheet || terminalHelp || taskPageVisible || lifecycle != Lifecycle.State.RESUMED || !snapshot.verified) return@LaunchedEffect
        snapshotFlow {
            if (list.isScrollInProgress) null else list.layoutInfo.visibleItemsInfo.mapNotNull { visible ->
                currentLinesByKey[visible.key]?.seenItem()
            }.maxWithOrNull(chatItemOrder)
        }.debounce(450).distinctUntilChanged().collect { item ->
            if (item != null && (reportedSeen == null || chatItemOrder.compare(item, reportedSeen!!) > 0)) try {
                repository.markSeen(key, item.id)
                reportedSeen = item
            } catch (e: CancellationException) { throw e } catch (_: Exception) { }
        }
    }
    val status = chatStatusState(res, snapshot, session, machine, messages, requests, taps.busy || controls.busy,
        operationUnknown = taps.uncertain.isNotEmpty() || controls.pending != null, awaiting = awaitingStatus,
        submittingId = sendingId.takeIf { sending } ?: submittedDraft?.first, receipts = sendReceipts)
    val replacement = if (snapshot.replaced) sessionList.firstOrNull { candidate ->
        session != null && candidate.key != key && SessionRuntimeState.map(session, listOf(candidate), true).replaced
    } else null
    // The machine this conversation runs on, for its port links (docs/operations/ports.md "Recognizing links in replies").
    val pageLinks = rememberChatPageLinks(key, chrome, renderStyle, scope, snapshot, sessionList, machines, readOnly, session,
        canSend, machine, tasks, taskTodos, taskAgentSessions, glass, taskHost)
    val chatPorts = pageLinks.chatPorts
    val mentions = pageLinks.mentions
    val cardsHost = pageLinks.cardsHost
    val callJumps = pageLinks.callJumps
    val toolNames = pageLinks.toolNames
    val linkManager = pageLinks.linkManager
    CompositionLocalProvider(LocalChatMarkdownCache provides markdownCache, LocalChatRowActions provides rowActions,
        LocalContentColor provides PersonaTheme.palette.ink, LocalChatPorts provides chatPorts,
        persona.core.designsystem.LocalAssistantCards provides cardsHost, LocalChatSubagents provides subagentLinks, LocalSubagentFolds provides subagentFolds, LocalChatWorkflows provides workflowCards,
        LocalChatTaskIndex provides projection.tasks, LocalChatJumpToCall provides callJumps::jump, LocalToolNames provides toolNames) {
        // Under the files pane or the terminal's cover: hidden from screen readers too.
        ChatLinkHost(Modifier.fillMaxSize().background(PersonaTheme.palette.background)
            .then(if (embedded) Modifier else Modifier.imePadding())
            .then(if (chrome.covered) Modifier.clearAndSetSemantics {} else Modifier), preloadUrls, list.isScrollInProgress,
            onContext = { if (readOnly || !canSend) notice = res.getString(R.string.chat_screen_read_only_context) else linkContexts = linkContexts + it },
            onEdit = { selection, instruction ->
                if (readOnly || !canSend) notice = res.getString(R.string.chat_screen_read_only_edit)
                else {
                    sender.send(key, ChatLinkContext.editMessage(selection, instruction, context.resources), selection.id)
                    followEnd()
                }
            }, onVisibilityChanged = { linkVisible = it; if (it) { fullText = null; statusSheet = false } }, manager = linkManager) { linkInset ->
            BoxWithConstraints(Modifier.fillMaxSize().padding(bottom = linkInset)) {
                val pageHeight = maxHeight
                val trayMax = maxHeight * AssistantTrayTokens.maximumHeightFraction
                // The rule a question a supervisor passed up suggests, under its card (#4929); a hub from before sends none.
                val ruleLine: (@Composable (Interaction) -> Unit)? = if (tasks == null) null else { card ->
                    RuleSuggestionLine("card:${card.id}", card.suggestion, tasks, { notice = operationFeedback(it).text.resolve(res) },
                        card.manager, enabled = canApprove)
                }
                // The pending cards above the composer (DESIGN_SYSTEM.md 4.8, iOS PendingCards and TaskQuestions), in two places:
                // [task] Persona's task question above its entries, else the stack. Each takes at most half the page and never so
                // much that the composer leaves it; with the keyboard up, what the conversation has above the rest of the
                // footer; and the two share that room (PendingStack.cap), so together they never take more than it. Derived:
                // the bars' heights move as the header folds, the cap only now and then.
                val pendingCards: @Composable (cards: List<Interaction>, task: Boolean, keyboard: Boolean) -> Unit = { cards, task, keyboard ->
                    val density = LocalDensity.current
                    val above = LocalChromeInsets.current.top
                    val otherShown = if (task) stackCards.isNotEmpty() else taskAsk != null
                    val cap by remember(pageHeight, keyboard, density, above, task, otherShown) {
                        derivedStateOf {
                            with(density) {
                                PendingStack.cap(pageHeight, keyboard, rest = (footerHeight - taskHeight - stackHeight).coerceAtLeast(0).toDp(),
                                    header = above + topBarHeight.toDp(), floor = Tokens.Space.xl,
                                    other = if (!otherShown) null else (if (task) stackHeight else taskHeight).toDp())
                            }
                        }
                    }
                    // In a box of its own: with no cards it is nothing, and its height reads 0 again.
                    Box(Modifier.onSizeChanged { if (task) taskHeight = it.height else stackHeight = it.height }) {
                        InteractionPanel(cards, machine, cap, taps.busyId, taps.uncertain, { interaction, resolution ->
                            if (taps.canTap(interaction, allRequests, java.time.Instant.now())) {
                                taps = taps.tapped(interaction.id)
                                scope.launch {
                                    try {
                                        val result = submitInteraction { interactions.resolveOutcome(interaction.id, resolution) }
                                        result.submission.haptic?.let(haptics)
                                        taps = taps.answered(interaction.id, result.card)
                                        if (result.withdraw) {
                                            interactionFeedback = result.notice(res, interaction, resolution)
                                            if (result.offersNext(resolution)) answeredHere++
                                        }
                                        notice = if (result.withdraw) null else result.notice(res, interaction, resolution)
                                        if (result.submission == InteractionSubmission.PromptChanged || !result.withdraw) {
                                            try { interactions.refresh() } catch (e: CancellationException) { throw e } catch (_: Exception) { }
                                        }
                                    } finally { taps = taps.idle() }
                                }
                            }
                        }, terminal, { if (task) taskOverlay = it else stackOverlay = it }, linkVisible = linkVisible,
                            folds = requestFolds,
                            blockedReason = if (canApprove) null else approvalBlockedReason(res, session),
                            terminalHelp = canTerminal, onDiscardLocal = { interaction ->
                                if (!taps.busy) {
                                    taps = taps.tapped(interaction.id)
                                    scope.launch {
                                        try {
                                            interactions.discardLocal(interaction.id)
                                            taps = taps.discarded(interaction.id)
                                            interactionFeedback = res.getString(R.string.chat_card_discarded_local)
                                            notice = null
                                        } catch (e: CancellationException) { throw e
                                        } catch (_: Exception) {
                                            notice = res.getString(R.string.chat_card_discard_failed)
                                        } finally { taps = taps.idle() }
                                    }
                                }
                            }, keyboard = keyboard,
                            labels = if (task) taskAsk?.let { mapOf(it.card.id to it.label) }.orEmpty() else emptyMap(),
                            first = if (task) null else chrome.requestId, counted = !task, suggestion = ruleLine)
                    }
                }
                val onOpenSibling = chrome.onOpenSession
                val siblingSessions = if (showTopBar && renderStyle != ChatRenderStyle.Assistant && onOpenSibling != null) {
                    remember(session, sessionList) { spaceTabs(session, sessionList) }
                } else emptyList()
                // Glass: a loop and background work are a pill at the end of the tabs' row (iOS ActivityChips, #4339 C8),
                // a row of its own without tabs; the other styles keep their chips above the composer.
                val activityPill: (@Composable () -> Unit)? = session
                    ?.takeIf { activityInHeader && ChatActivity.shown(it) }?.let { s -> { ChatActivityPill(s) { activitySheet = true } } }
                val tabs = session != null && onOpenSibling != null && siblingSessions.size > 1
                JPage(
                    title = session?.displayTitle ?: stringResource(R.string.chat_screen_untitled),
                    scrollEdgeStyle = ScrollEdgeStyle.Gradient,
                    topScrollEdgeStyle = if (tabs || activityPill != null) TabbedChatScrollEdgeStyle else ScrollEdgeStyle.Gradient,
                    topBar = {
                        Column(Modifier.onSizeChanged { topBarHeight = it.height }) {
                            val header: @Composable () -> Unit = {
                                Column {
                                    if (showTopBar) ChatHeader(session, machine, sessionList, controls, connection, readOnly, onBack,
                                        chrome.onOpenSession, chrome.onConnectionDetails, onOverlay = { headerOverlay = it },
                                        onFiles = chrome.onFiles, onGit = chrome.onGit, gitLabel = chrome.gitLabel,
                                        statusState = status, onDetails = { statusSheet = true },
                                        onOpenTodo = chrome.onOpenTodo, agentShares = chrome.agentShares, worktrees = chrome.worktrees,
                                        onSearch = { search.open(chatList) { timeline.freeze(currentItems); timelineVersion++ } },
                                        onTerminal = agentTerminal, onTerminalHelp = if (hasTerminal && terminal == null) ({ terminalHelp = true }) else null,
                                        onResumeInTerminal = resumeInTerminal,
                                        turnStart = runningSince)
                                    // The space's other tabs under the title (iOS SiblingTabs); not for the assistant's conversation.
                                    if (tabs && session != null && onOpenSibling != null) {
                                        ChatSiblingTabs(session, siblingSessions, onOpenSibling, trailing = activityPill)
                                    } else activityPill?.let { ChatActivityRow(it) }
                                    // The ports it listens on (its dev servers, docs/operations/ports.md): a tap opens the page.
                                    if (showTopBar && renderStyle != ChatRenderStyle.Assistant) ChatPortChips(session?.ports)
                                    // The goal it works toward (Claude Code's /goal, a Codex thread goal; docs/integrations/agent-goal.md): a tap opens it.
                                    if (showTopBar) ChatGoalBar(session?.goal)
                                }
                            }
                            if (foldsHeader) JFoldingHeader({ headerFold.fold }, onRange = { headerFold.range = it }, onTouch = headerFold::touched,
                                full = header, compact = { ChatSlimHeader(session, status, connection, onBack, onOpen = headerFold::open) })
                            else header()

                            ChatTasks(projection.tasks, lines, chatList,
                                onFreeze = { timeline.freeze(currentItems); timelineVersion++ },
                                onVisibilityChanged = { taskPageVisible = it },
                                onExpandFold = ::expand, foldErrors = foldErrors,
                                // While the link card peeks, the composer needs the room more (glass laid them over each other).
                                showBar = linkInset.value <= 0f, callJumps = callJumps)
                        }
                    },
                    bottomBar = {
                        Column(Modifier.onSizeChanged { footerHeight = it.height }) {
                            if (renderStyle == ChatRenderStyle.Assistant && (status.submitting || status.processing ||
                                    (status.unconfirmed && sendReceipts.isNotEmpty()))) StatusLine(status)
                            if (renderStyle != ChatRenderStyle.Assistant) ChatSessionStatusLine(status, session, snapshot.turnDurationSeconds, projection.turns.lastOrNull()?.toolCount ?: 0,
                                onDetails = { statusSheet = true }, terminal = terminal, draftSaveError = statusBindings.draftSaveError,
                                activityInHeader = activityInHeader)
                            AnimatedVisibility(interactionFeedback != null, exit = fadeOut()) {
                                // Inset like the status line and the composer around it, not edge to edge.
                                JNotice(interactionFeedback.orEmpty(), Modifier.padding(horizontal = Tokens.Space.m))
                            }
                            // An agent operating a Mac: over the cards and the composer, as iOS's footer has it.
                            if (agentChat) computerPill?.invoke(Modifier.padding(horizontal = Tokens.Space.m))
                            // This agent has nothing left for you, others have (#4325): one tap to the next, no jump.
                            val openRequest = chrome.onOpenRequest
                            val keyboard = WindowInsets.ime.getBottom(LocalDensity.current) > 0
                            if (showsNextWaiting(offerNext, requests.size, readOnly, canOpen = openRequest != null, keyboard = keyboard))
                                nextWaiting(chrome.waiting, key)?.let { next ->
                                    NextWaitingRow(next, { offerNext = false; openRequest?.invoke(next.request.session, next.request.requestId) })
                                }
                            // An Agent's cards sit over the queue, the notes and the composer, as iOS's footer has them.
                            if (!readOnly && !assistantChat) pendingCards(stackCards, false, keyboard)
                            // The latest question the assistant answered here, after the agent's own cards (iOS DelegatedCard, #4339 C9).
                            if (glass && agentChat && !readOnly)
                                DelegatedCard(key, interactions, canSend, Modifier.padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.xs))
                            val composerError = snapshot.error.takeIf { errorLine == null }
                            (if (feedback == null) notice ?: composerError else composerError)?.let {
                                Text(it, Modifier.padding(horizontal = Tokens.Space.m), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                            }
                            if (!historyOnly && !readOnly) ChatStartingNote(session)
                            // The host's own word on it: why it waits for you, or what stopped it, with a retry.
                            // ultracode went off when it resumed (#582, iOS UltracodeLostNote): one line to turn it back on or dismiss.
                            if (!historyOnly && !readOnly && session != null) UltracodeLostNote(session, controls, canSend)
                            if (!historyOnly && !readOnly) ChatHostNote(session, hostRetried, canRetry = canSend,
                                retryEnabled = !sending && submittedDraft == null,
                                onRetry = { reason ->
                                    // Hidden first (a failure can come back before this returns), back if it was not taken.
                                    val before = hostRetried
                                    hostRetried = reason
                                    if (!send(continueText, onFailed = { if (hostRetried == reason) hostRetried = null })) hostRetried = before
                                })
                            // An upstream refusal moved it to a fallback model (#2528): both models and the ways back.
                            if (!historyOnly && !readOnly && session != null) ModelFallbackCard(session, chrome.actions)
                            if (historyOnly) footer?.invoke()
                            else if (readOnly) Text(stringResource(R.string.chat_screen_subagent_read_only), Modifier.padding(Tokens.Space.m), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                            else if (session?.isStoppedAgent == true && machine?.online != false) ChatStoppedNote(agentTerminal,
                                resumeInTerminal?.takeIf { terminalResume(session, machine) == TerminalResume.Available })
                            else {
                                // What waits in the agent's queue sits right above the composer, while the composer shows (Persona's further down).
                                // Not while a "/" quick message is being picked: the list opens above the field, and with the
                                // keyboard up the queue, the list and the status line together climbed over the top bar.
                                if (renderStyle != ChatRenderStyle.Assistant && SlashCommand.suggestions(composer.value.text, clear = true).isEmpty())
                                    ChatQueueStrip(session, chrome.actions, composer, { notice = it }, sessions::refresh)
                                // A source that only lets the phone read says so where the typing would be.
                                if (!canSend && session != null) ChatSourceNote(session)
                                if (assistantChat) taskHost?.let { host ->
                                    val namespace = GroupKeys.namespaceOf(key.machineId)
                                    val owner = session?.manager ?: managerChoice.shown
                                    val trayRequests = taps.shown(allRequests).filter { request ->
                                        GroupKeys.namespaceOf(request.machineId) == namespace &&
                                            (request.sessionKey == key || request.sessionKey in taskKeys ||
                                                host.sessions.any { it.key == GroupKeys.rawKey(request.sessionKey) && !it.assistant && (it.manager ?: managerChoice.defaultId) == owner })
                                    }
                                    // What its tasks ask, at the bottom where the user is: the oldest question above the entries and the
                                    // tray, answered there (iOS taskFooter, TaskQuestions).
                                    taskAsk?.let { pendingCards(listOf(it.card), true, keyboard) }
                                    AssistantTrays(host, assistantTrays, trayRequests,
                                        inboxDrafts.filter { (it.manager ?: managerChoice.defaultId) == owner }, traySchedules,
                                        maximumHeight = trayMax,
                                        busy = trayApproving || traySending || taps.busy, enabled = canSend,
                                        onApprove = ::approveTray, onReview = { interaction ->
                                            assistantTrays.expanded = null
                                            val raw = GroupKeys.rawKey(interaction.sessionKey)
                                            // As iOS's rows do: the conversation's own card is right below the entries, so the row closes
                                            // the tray onto it; a task's row opens that task, an Agent's its own (detailTask, detailAgent),
                                            // where Open chat brings its page and its cards. Never into the stack: that is the conversation's.
                                            if (interaction.sessionKey == key) requestFolds.open(interaction.id)
                                            else taskTodos.firstOrNull { todo -> todo.activeAgents.any { it.key == raw } }
                                                ?.let { todo -> openTask(host, todo) } ?: run { host.sheet.value = TaskSheet.Agent(raw) }
                                        }, onSend = ::sendTray, onSchedule = { traySchedule = it }, onPlan = { trayPlan = it },
                                        backgroundRecords = host.backgroundRecords.isNotEmpty(), onBackground = host.onOpenBackground,
                                        under = computerPill?.let { pill -> { pill(Modifier) } },
                                        groupByOrigin = foldsBackground)
                                    // Where the next message goes, never only inside the In progress tray (iOS RouteChip, #4339 C15).
                                    TaskRouteChip(host)
                                }
                                // The conversation's own cards under its entries and the route chip, over its queue and the composer,
                                // as iOS's footer has them: they alone, so "N pending" counts only them (DESIGN_SYSTEM.md 4.8).
                                if (assistantChat) pendingCards(stackCards, false, keyboard)
                                // Persona's queue: its own messages, then those relayed from elsewhere, under its entries and right
                                // above the composer, as iOS's footer has it (every style: it is behaviour; each draws its own strip, #4339 C6).
                                if (renderStyle == ChatRenderStyle.Assistant && SlashCommand.suggestions(composer.value.text, clear = true).isEmpty())
                                    ChatQueueStrip(session, chrome.actions, composer, { notice = it }, sessions::refresh)
                                val claudeCommands = session?.canTypeClaudeCommands == true
                                ChatComposer(key, composer, session?.status == "working", sending || submittedDraft != null,
                                    // /clear and /model type into Claude's pane: Claude in a terminal engine only (positive check).
                                    canClear = clearingPane == null && session?.canTypeClaudeCommands == true,
                                    service = composerService, onSend = { send() }, onCommand = { send(it) }, onNotice = { notice = it },
                                    extraContent = linkContexts.isNotEmpty(),
                                    enabled = canSend, placeholder = when {
                                        !canSend -> READ_ONLY.text()
                                        // "Ask Persona…", as the assistant's blank page and iOS say (#4339 C18).
                                        !agentChat -> stringResource(R.string.chat_placeholder_ask, persona.core.designsystem.LocalPersonaName.current)
                                        else -> agentPlaceholder(res, session)
                                    },
                                    // Permission mode and effort for Claude's sessions, Persona's included; the model for Claude in a pane;
                                    // the effort alone for a Codex thread on its app-server.
                                    leading = when {
                                        // Glass: the permission mode in the composer (Q11, iOS and the web): its symbol in its tone and
                                        // its menu; the model and the effort are the pill's.
                                        glass -> if (session != null && SessionModes.showsInComposer(session)) ({
                                            HeaderModeButton(session, controls, connection == null || connection == ConnectionState.Online) { modeOverlay = it }
                                        }) else null
                                        session?.canChangeModes == true || session?.canChangeEffortOnly == true -> ({
                                            ChatModeBar(session, controls, connection == null || connection == ConnectionState.Online,
                                                onOverlay = { modeOverlay = it }, onPickModel = { model -> send("/model $model") },
                                                openRequested = openModes, onOpenHandled = { openModes = false })
                                        })
                                        else -> null
                                    },
                                    modeHint = if (glass) null else chatModeHint(session, includeModes = session?.canChangeModes == true || session?.canChangeEffortOnly == true),
                                    // The model pill (#5055, the web's ModelPill): Glass's, in an Agent's chat and a Persona chat.
                                    modelPill = if (!glass || switchSession == null) null else ({ width ->
                                        val online = connection == null || connection == ConnectionState.Online
                                        if (agentChat) AgentModelPill(switchSession, controls, chrome.worktrees?.invoke(), online, width,
                                            onSwitch = { drawnSwitch = it }, onOverlay = { pillOverlay = it },
                                            openRequested = openModes, onOpenHandled = { openModes = false },
                                            onConfigure = chrome.onFixModel?.let { fix -> { fix(persona.core.designsystem.ReadinessActionId.EditProvider, null, switchSession.manager) } })
                                        else PersonaModelPill(switchSession, key.machineId, chrome.assistantManagement, width,
                                            pagePick = conversationLauncher?.let { persona.core.contracts.ModelPick(it, conversationModel) },
                                            onPagePick = { pick -> conversationLauncher = pick?.launcher; conversationModel = pick?.model },
                                            onNotice = { notice = it }, enabled = online, onOverlay = { pillOverlay = it },
                                            onConfigure = chrome.onFixModel?.let { fix -> { fix(persona.core.designsystem.ReadinessActionId.EditProvider, null, switchSession.manager) } },
                                            onEffort = { effort -> controls.run(switchSession, ChatAction.Modes(effort = effort)) })
                                    }),
                                    accessory = {
                                        // Brief and Board keep the launcher chip; Glass's pill does its work (#5055).
                                        if (!glass && renderStyle == ChatRenderStyle.Assistant && !readOnly && canSend && chrome.assistantManagement != null) {
                                            ConversationLauncherChip(chrome.assistantManagement, key.machineId, key.sessionId, session?.launcher,
                                                conversationLauncher, conversationModel) { launcher, model ->
                                                conversationLauncher = launcher
                                                conversationModel = model
                                            }
                                        }
                                        if (linkContexts.isEmpty()) ComposerSuggestion(session, composer, onSend = { text -> send(text) })
                                        // One line each while the link card peeks: the whole cards left no room for the composer.
                                        if (linkContexts.isNotEmpty()) ComposerContextCards(linkContexts, compact = linkInset.value > 0f) { ref ->
                                            linkContexts = linkContexts.filterNot { it.id == ref.id }
                                        }
                                    }, onOverlay = { composerOverlay = it }, agentName = session?.agentLabel ?: "Claude",
                                    canInterrupt = session?.canInterrupt ?: true,
                                    mentions = mentions, onMention = { ref -> linkContexts = linkContexts + ref },
                                    onMentionTask = { id -> tasks?.setRoute(key.sessionId, id) },
                                    // /model opens the "Mode" sheet's model choices; both need Claude in a pane (as the sheet does).
                                    onModel = if (claudeCommands && session?.canChangeModes == true) ({ openModes = true }) else null,
                                    // (on Glass /model opens the pill's panel, where the model is picked)
                                    loop = claudeCommands)
                            }
                        }
                    },
                ) { pagePaddingValues ->
                    val horizontalPadding = pagePadding
                    // The closeout card takes the room under the top bar; the list then starts right under the card.
                    val padding = if (closeoutShown != null) PaddingValues(bottom = pagePaddingValues.calculateBottomPadding()) else pagePaddingValues
                    val closeoutReply = remember(lines, closeoutShown != null) { if (closeoutShown != null) closeoutReplyId(lines) else null }
                    Column(Modifier.fillMaxSize()) {
                        if (search.selected != null) ReturnChip { search.returnToOrigin(chatList) }
                        search.notice?.let { Text(it, Modifier.padding(horizontal = horizontalPadding), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2) }
                        closeoutShown?.let { closeout ->
                            CloseoutCard(closeout, closeoutWork?.artifacts.orEmpty(), key.path, Modifier.padding(
                                start = horizontalPadding, end = horizontalPadding, top = pagePaddingValues.calculateTopPadding() + Tokens.Space.s))
                        }
                        Box(Modifier.weight(1f).then(if (foldsHeader) Modifier.nestedScroll(headerScroll) else Modifier)) {
                            val outboxBubble: @Composable (OutboxMessage) -> Unit = { message ->
                                val delivery = composerDeliveries[message.clientMessageId]
                                val deliveryState = if (message.state in setOf(DeliveryState.Refused, DeliveryState.Rejected))
                                    delivery?.state ?: message.state else message.state
                                val busy = message.clientMessageId in deliveryActions.busyIds
                                val onAction: ((DeliveryAction) -> Unit)? = if (readOnly) null else { action -> deliveryAction(message, action) }
                                val failed = deliveryState == DeliveryState.Rejected || deliveryState == DeliveryState.Refused
                                val receipt = sendReceipts.lastOrNull { it.clientMessageId == message.clientMessageId }
                                Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                                    // Glass: iOS's bubble, its mark and small menu in place of the status line, which still says why
                                    // one did not go (#4339 C9); a delivery to check keeps the status line.
                                    if (glass && drawsAsPendingBubble(deliveryState)) {
                                        OutboxBubble(composerMessagePreview(message.text), message.clientMessageId, deliveryState, busy,
                                            canSend = canSend, onAction = onAction)
                                        if (receipt?.processing == true) Text(stringResource(persona.core.designsystem.R.string.designsystem_message_processing),
                                            Modifier.align(Alignment.End), style = PersonaTheme.type.caption,
                                            color = PersonaTheme.palette.ink2, maxLines = 1)
                                        if (failed) PendingBadge(PendingStatus.Refused, Modifier.align(Alignment.End),
                                            reason = message.copy(error = delivery?.refusal?.code ?: message.error).refusalReason())
                                    } else {
                                        UserBubble(composerMessagePreview(message.text), message.clientMessageId, deliveryState)
                                        OutboxStatus(message.copy(state = deliveryState,
                                            error = delivery?.refusal?.code ?: message.error),
                                            busy = busy, onAction = onAction, receipt = receipt, canSend = canSend, onDetails = { statusSheet = true })
                                    }
                                    if (!readOnly && failed) {
                                        // Edit puts the words back in the box and drops the refused bubble (nothing reached the computer).
                                        ComposerRefusalActions(message, composerService, delivery, terminal, canSend = canSend,
                                            edit = { deliveryAction(message, DeliveryAction.Edit) }, notice = { notice = it })
                                    }
                                }
                            }
                            ChatList(state = chatList, modifier = Modifier.fillMaxSize().endSelectionOnTapOutside(messageMenu), contentPadding = PaddingValues(
                                start = horizontalPadding, end = horizontalPadding,
                                top = padding.calculateTopPadding() + 12.dp.dense,
                                bottom = padding.calculateBottomPadding() + 12.dp.dense,
                            )) {
                                errorLine?.let { error -> item(key = ERROR_ROW, contentType = ERROR_ROW) { ChatSystemLine(error, failed = true) } }
                                readyLine?.let { words -> item(key = READY_ROW, contentType = READY_ROW) { TypingLine(words) } }
                                // Newest at the bottom (reverse layout). A failed send sits above the first line written after it.
                                placed.rest.asReversed().forEach { message ->
                                    item(key = "outbox-${message.clientMessageId}", contentType = "outbox") { outboxBubble(message) }
                                }
                                pendingDay?.let { day -> item(key = PENDING_DAY_ROW, contentType = "day") { DayLine(day, Modifier.testTag("chat-pending-day")) } }
                                for (line in lines) {
                                    item(key = line.contentKey, contentType = line.contentType()) {
                                        ChatSearchHighlight(search, line.contentKey) {
                                            androidx.compose.runtime.CompositionLocalProvider(LocalAssistantTaskHost provides taskHost,
                                                LocalTurnErrorFixAction provides fixModel, LocalOpenRecap provides openRecap) {
                                            ChatLineContent(line, key, loader,
                                            line.recordedDuration(snapshot.recordedTurnDurations),
                                            ::toggle, ::expand, folding, foldErrors,
                                            lastUserId = lastUserId,
                                            turnFix = turnFix.takeIf { line.contentKey == lines.firstOrNull()?.contentKey },
                                            onLastLine = { extent -> chatList.reportLastLine(line.contentKey, extent) }, onFullText = { item ->
                                            scope.launch {
                                                try { fullText = repository.fullText(key, item.id) }
                                                catch (e: CancellationException) { throw e }
                                                catch (e: Exception) { if (feedback?.failure(e) != true) notice = operationFeedback(e).text.resolve(res) }
                                            }
                                        }, style = renderStyle, textUpdates = liveTextUpdates,
                                            userImages = contentUserImages[line.id].orEmpty(),
                                            imagesInUserBubble = line.id in contentHiddenImages,
                                            workRunning = (line as? ChatLine.Step)?.let { contentWorkGroups[it.groupId]?.live } == true,
                                            lastWorkStep = (line as? ChatLine.Step)?.let {
                                                // A nested subagent's card can come after the line's own last step (Glass).
                                                if (subagentCards) contentLastSteps[it.groupId] == it.step.id
                                                else contentWorkGroups[it.groupId]?.steps?.lastOrNull()?.id == it.step.id
                                            } == true,
                                            plan = (line as? ChatLine.Step)?.let { contentPlans[it.groupId] },
                                            showSourceLabel = line.id in contentSourceLabels, waiting = waitingOnYou,
                                            closeoutReply = closeoutReply, hideTaskPrompts = foldsBackground, onTerminal = agentTerminal)
                                            }
                                        }
                                    }
                                    placed.before[line.id].orEmpty().asReversed().forEach { message ->
                                        item(key = "outbox-${message.clientMessageId}", contentType = "outbox") { outboxBubble(message) }
                                    }
                                }
                                if (snapshot.loadingOlder) item(key = "loading-older", contentType = "loading") { JLoading() }
                                if (snapshot.hasOlder && !snapshot.loadingOlder) item(key = "older", contentType = "older") {
                                    JTextButton(stringResource(R.string.chat_screen_load_older), { scope.launch { try { repository.loadOlder(key) } catch (e: CancellationException) { throw e } catch (e: Exception) { if (feedback?.failure(e) != true) notice = operationFeedback(e).text.resolve(res) } } })
                                }
                            }
                            // Under the top bar: in glass the list (and this box) runs under it, up to the status bar.
                            WorkCollapseOverlay(lines, list, listKeys, ::toggle, Modifier.align(Alignment.TopStart)
                                .padding(start = horizontalPadding, top = padding.calculateTopPadding() + Tokens.Space.s))
                            val settling = snapshot.loading || projectedFrom == null || (snapshot.items.isNotEmpty() && projectedFrom !== snapshot.items)
                            if (settling && lines.isEmpty() && messages.isEmpty()) {
                                // An Agent's chat on Glass: iOS's skeleton rows where its newest lines will be; Brief and Board keep the spinner.
                                if (glass && agentChat) ChatSkeleton(Modifier.align(Alignment.BottomCenter).padding(start = horizontalPadding, end = horizontalPadding,
                                    bottom = padding.calculateBottomPadding() + Tokens.Space.m))
                                else JLoading(Modifier.align(Alignment.Center).padding(padding).padding(horizontal = horizontalPadding))
                            }
                            else if (!settling && lines.isEmpty() && messages.isEmpty() && snapshot.error == null)
                                JEmptyState(stringResource(R.string.chat_screen_empty_title),
                                    emptyChatHint(res, session, machines.firstOrNull { it.id == key.machineId }?.name, readOnly),
                                    Modifier.align(Alignment.Center).padding(padding).padding(horizontal = horizontalPadding))
                            // The newest plan card scrolled out of view: "Step N/M" above the composer brings it back (web PlanPill).
                            val anchor = remember(lines, contentPlans, renderStyle) { latestPlanLine(lines, contentPlans) }
                            val planOut by remember(anchor) { derivedStateOf {
                                anchor != null && list.layoutInfo.totalItemsCount > 0 &&
                                    list.layoutInfo.visibleItemsInfo.none { it.key == anchor.lineId }
                            } }
                            // The open sheet follows the newest plan, and keeps the last one if its work line folds away.
                            if (planSheet && anchor != null) SideEffect { sheetPlan = anchor.plan }
                            if (anchor != null && planOut) {
                                val progress = remember(anchor.plan.todos) { planProgress(anchor.plan.todos) }
                                // An Agent's chat on Glass: the whole plan in a sheet (iOS `PlanSheet`); Brief and Board scroll back to its card.
                                val opensSheet = glass && agentChat
                                PlanPill(progress.step, progress.total, progress.done, onClick = {
                                    if (opensSheet) { sheetPlan = anchor.plan; planSheet = true }
                                    else scope.launch {
                                        timeline.freeze(currentItems); timelineVersion++
                                        chatList.scrollToRecord(anchor.lineId)
                                    }
                                }, modifier = Modifier.align(Alignment.BottomCenter).padding(bottom = padding.calculateBottomPadding() + Tokens.Space.l),
                                    spoken = if (opensSheet) planStepText(res, anchor.plan.todos) else null,
                                    onClickLabel = if (opensSheet) stringResource(R.string.chat_plan_pill_opens) else null)
                            }
                            if (!chatList.followsEnd) JumpToLatest(timeline.newMessageCount(snapshot.items, newestVisibleItem, readThrough),
                                onClick = chatList::returnToLatest,
                                modifier = Modifier.align(Alignment.BottomEnd).padding(end = horizontalPadding, bottom = padding.calculateBottomPadding() + 16.dp.dense))
                        }
                    }
                }
            }
            if (search.visible) ChatSearchScreen(search, searchableItems, projection.turns.size, snapshot.hasOlder,
                repository, key, snapshot.history, onClose = { search.close(chatList) })
            if (terminalHelp) TerminalHelpDialog { terminalHelp = false }
            if (activitySheet) session?.takeIf(ChatActivity::shown)?.let { s ->
                ChatActivitySheet(s, onStop = if (readOnly || !canSend) null else ({ send(ChatActivity.STOP_LOOP) }),
                    onOpenOutput = chrome.onOpenPath?.let { open -> { path -> activitySheet = false; open(path, null) } },
                    onDismiss = { activitySheet = false })
            }
            // The loop stopped and the background work ended: nothing left for the sheet.
            val activityShown = ChatActivity.shown(session)
            LaunchedEffect(activityShown) { if (!activityShown) activitySheet = false }
            if (statusSheet) ChatStatusSheet(status, session, messages, deliveryActions.busyIds,
                onAction = if (readOnly) null else ::deliveryAction, onDismiss = { statusSheet = false },
                replacement = replacement,
                onOpenReplacement = if (replacement != null && chrome.onOpenSession != null) {
                    { statusSheet = false; chrome.onOpenSession.invoke(replacement.key) }
                } else null,
                onConnectionDetails = chrome.onConnectionDetails, terminal = terminal,
                recordedDuration = snapshot.turnDurationSeconds, notice = notice ?: snapshot.error,
                recovery = recovery, machine = machine, draftSaveError = statusBindings.draftSaveError, readOnly = readOnly,
                onOpenSession = chrome.onOpenSession?.let { open -> { target -> statusSheet = false; open(target) } })
            fullText?.let { FullTextReader(it, onDismiss = { fullText = null }) }
            sheetPlan?.takeIf { planSheet }?.let { PlanSheet(it, onDismiss = { planSheet = false }) }
            val menuTitle = session?.displayTitle ?: stringResource(R.string.chat_screen_untitled)
            MessageMenuSheet(messageMenu, messageMenuWrites(readOnly, session),
                onQuote = quoteTarget, onEdit = composer::edit,
                onContext = { text -> linkContexts = linkContexts + messageContext(menuTitle, text, context.resources); notice = res.getString(R.string.chat_screen_added_context) },
                onHandToPersona = chrome.onHandToPersona?.takeIf { renderStyle != ChatRenderStyle.Assistant }
                    ?.let { hand -> { text: String -> hand(handoffMessage(menuTitle, text, context.resources)) } },
                onNotice = { notice = it }, onFork = onFork, rewind = rewindMenu,
                onDelete = { target -> messages.firstOrNull { it.clientMessageId == target.id }?.let { message ->
                    deliveryAction(message, if (message.state.allows(DeliveryAction.Cancel)) DeliveryAction.Cancel else DeliveryAction.Delete)
                } }, share = shareCardSource(session, LocalPersonaName.current))
            viewer?.let { request ->
                ChatImageViewer(request.images, request.index, onDismiss = { viewer = null },
                    onEdited = if (readOnly) null else ({ attachment -> composer.beginEdit(); composer.attachments += attachment }),
                    photoLimitReached = composer.attachments.count { it.media } >= MAX_COMPOSER_MEDIA)
            }
        }
    }
}

/** The contexts the next message carries, over the composer (a chat's, and New chat's sessions picked with @); a longer list scrolls. */
@Composable
internal fun ComposerContextCards(contexts: List<ChatLinkContext>, compact: Boolean = false, onRemove: (ChatLinkContext) -> Unit) {
    Column(Modifier.heightIn(max = 140.dp).verticalScroll(rememberScrollState())) {
        contexts.forEach { ref -> ChatContextCard(ref, compact = compact) { onRemove(ref) } }
    }
}

/** The rows at the chat's newest end, under your unsent messages: a load error (Glass), then "Claude is getting ready…". */
private const val ERROR_ROW = "load-error"
private const val READY_ROW = "getting-ready"
/** The day over a first message not in the transcript yet (Glass, Persona's conversation). */
private const val PENDING_DAY_ROW = "pending-day"
