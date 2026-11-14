package persona.feature.chat

// ChatPage's set-up code, moved here in blocks: each function is a value-returning
// @Composable called once from ChatPage where the block was, so its state reads and effects stay in ChatPage's
// recomposition scope and order. They keep ChatPage under ART's per-method compile limit (10,000 code units).
// Keep each one returning a value: a Unit-returning composable gets its own restart scope, which changes what recomposes.

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
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/** ChatPage's cards and tasks: the taps, the requests shown here, the tasks' state and the bubbles (moved as is). */
@Composable
internal fun rememberChatPageRequests(key: SessionKey, chrome: ChatChromeBindings, renderStyle: ChatRenderStyle,
    composerService: ChatComposerService?, snapshot: persona.core.contracts.api.ChatSnapshot, sessionList: List<Session>,
    machines: List<Machine>, allRequests: List<Interaction>, allMessages: List<OutboxMessage>): ChatPageRequests {
    val machine = machines.firstOrNull { it.id == key.machineId }
    // The cards' taps: the answer in flight, the cards answered here, the unconfirmed ones (persona.core.domain.ChatTaps).
    val tapsState = remember { mutableStateOf(ChatTaps()) }
    var taps by tapsState
    val interactionFeedbackState = remember { mutableStateOf<String?>(null) }
    // Counts the cards this page took away by answering: the next one waiting elsewhere is offered for a while after.
    val answeredHereState = remember { mutableIntStateOf(0) }
    val offerNextState = remember { mutableStateOf(false) }
    // The assistant's tasks (docs/assistant/unified-entry.md): their agents' questions are answered here too, oldest first.
    val tasks = chrome.tasks
    val taskTodosState = (tasks?.todos ?: remember { kotlinx.coroutines.flow.MutableStateFlow(emptyList<persona.core.contracts.Todo>()) })
        .collectAsStateWithLifecycle()
    val taskTodos by taskTodosState
    val taskKeys = remember(taskTodos, key) {
        taskTodos.filter { it.origin == key.sessionId && it.doneAt == null }
            .flatMap { t -> t.activeAgents.map { GroupKeys.key(SessionKey(it.machineId, it.sessionId), GroupKeys.namespaceOf(key.machineId)) } }.toSet()
    }
    val taskAgentSessionsState = (tasks?.agentSessions ?: remember {
        kotlinx.coroutines.flow.MutableStateFlow(emptyMap<SessionKey, Session>())
    }).collectAsStateWithLifecycle()
    val requests = taps.shown(allRequests).filter { it.homeKey == key || it.homeKey in taskKeys }
        .sortedBy { if (it.id == chrome.requestId) 0 else 1 }
    // The cards above the composer, as iOS's footer has them. An Agent's chat: the stack holds every card it waits on.
    // Persona's conversation: the oldest question of its tasks is a card of its own above the Needs you / In progress
    // entries (iOS TaskQuestions), under its task's name with "N more"; the stack below them holds the conversation's own
    // cards alone (#4883), so its "N pending" counts only them (DESIGN_SYSTEM.md 4.8, #4339 C10).
    val assistantChat = renderStyle == ChatRenderStyle.Assistant
    val taskAsk = if (!assistantChat) null else taskQuestion(requests, taskKeys) { card ->
        val raw = GroupKeys.rawKey(card.sessionKey)
        taskTodos.firstOrNull { todo -> todo.activeAgents.any { it.key == raw } }?.title?.takeIf(String::isNotBlank)
            ?: card.taskTitle?.takeIf(String::isNotBlank) ?: card.sessionTitle?.takeIf(String::isNotBlank)
            ?: sessionList.firstOrNull { it.key == card.sessionKey }?.displayTitle
    }
    val stackCards = if (assistantChat) requests.filter { it.homeKey == key } else requests
    val messages = chatBubbles(allMessages, key, snapshot.items)
    val composerDeliveriesState = (composerService?.deliveries ?: remember {
        kotlinx.coroutines.flow.MutableStateFlow(emptyMap<String, ComposerDelivery>())
    }).collectAsStateWithLifecycle()
    return ChatPageRequests(machine, tapsState, interactionFeedbackState, answeredHereState, offerNextState, tasks,
        taskTodosState, taskKeys, taskAgentSessionsState, requests, assistantChat, taskAsk, stackCards, messages,
        composerDeliveriesState)
}

/** What [rememberChatPageRequests] hands back to ChatPage: plain values, and the state objects ChatPage reads and writes. */
internal class ChatPageRequests(
    val machine: Machine?,
    val taps: MutableState<ChatTaps>,
    val interactionFeedback: MutableState<String?>,
    val answeredHere: MutableIntState,
    val offerNext: MutableState<Boolean>,
    val tasks: persona.core.domain.tasks.TasksStore?,
    val taskTodos: State<List<persona.core.contracts.Todo>>,
    val taskKeys: Set<SessionKey>,
    val taskAgentSessions: State<Map<SessionKey, Session>>,
    val requests: List<Interaction>,
    val assistantChat: Boolean,
    val taskAsk: TaskQuestion?,
    val stackCards: List<Interaction>,
    val messages: List<OutboxMessage>,
    val composerDeliveries: State<Map<String, ComposerDelivery>>,
)

/** ChatPage's links and mentions: port links, the @ menu, the cards host, call jumps and tool names (moved as is). */
@Composable
internal fun rememberChatPageLinks(key: SessionKey, chrome: ChatChromeBindings, renderStyle: ChatRenderStyle,
    scope: kotlinx.coroutines.CoroutineScope, snapshot: persona.core.contracts.api.ChatSnapshot, sessionList: List<Session>,
    machines: List<Machine>, readOnly: Boolean, session: Session?, canSend: Boolean, machine: Machine?,
    tasks: persona.core.domain.tasks.TasksStore?, taskTodos: List<persona.core.contracts.Todo>,
    taskAgentSessions: Map<SessionKey, Session>, glass: Boolean, taskHost: AssistantTaskHost?): ChatPageLinks {
    val portPresence = remember(chrome.ports, key.machineId) {
        chrome.ports?.let { opener -> PortPresence(scope, { port -> opener.probe(key.machineId, port).let { it.listening to it.title } }) }
    }
    val chatPorts = remember(chrome.ports, machine, sessionList, key.machineId, portPresence) {
        chrome.ports?.let { opener ->
            val listed = LinkedHashMap<Int, String?>()
            sessionList.filter { it.machineId == key.machineId }.flatMap { it.ports.orEmpty() }
                .forEach { p -> if (p.port !in listed || listed[p.port] == null) listed[p.port] = p.title?.takeIf(String::isNotBlank) }
            ChatPorts(key.machineId, machine?.displayName ?: key.machineId, listOfNotNull(machine?.hostname?.takeIf(String::isNotBlank)),
                listed, opener, portPresence)
        }
    }
    // The @ menu: files this chat's tools used (newest first) and the other sessions, to quote (iOS PromptItems); in
    // Persona's conversation its open tasks first, to route the next message to one (iOS TaskMentionList, #4339 C15).
    val mentionFiles = remember(snapshot.items) { chatFiles(snapshot.items) }
    val taskMentions = remember(renderStyle, tasks, taskTodos, sessionList, taskAgentSessions, machines) {
        if (renderStyle != ChatRenderStyle.Assistant) null else taskHost?.let { host ->
            mentionTasks(taskTodos, host.conversation) { host.tasks.stateOf(it, host.sessions) }
                .map { (todo, state) -> MentionTask(todo.id, todo.title, todo.place(host, null), state.status()) }
        }
    }
    // Its session's folder and any folder a typed path names are read from the session's computer, as iOS's `MentionSource` does.
    val mentionFolders = remember(chrome.files, key.machineId) {
        chrome.files?.let { files -> MentionFolders { dir, cwd -> files.directory(key.machineId, cwd, dir).entries } }
    }
    val mentions = if (readOnly || !canSend) null
        else ComposerMentions(mentionFiles, session?.cwd.orEmpty(), sessionList, key, taskMentions, mentionFolders)
    // An assistant conversation's cards answer through its hub's store (docs/assistant/assistant-views.md); elsewhere they only draw.
    val cardsHost = remember(tasks, key.sessionId, readOnly, canSend) {
        tasks?.let { persona.core.domain.views.AssistantCardsHost(key.sessionId, it.cards, it.cardActions, interactive = !readOnly && canSend) }
    }
    // A task's notice jumps to the call that started it through the task bar's navigation (ChatTasks).
    val callJumps = remember(key) { ChatCallJumps() }
    // The Macs a computer tool's `machine` is, by name (the list changes with every report, its names seldom); Glass's
    // rows also keep some tools' words as the title with the subject under them (#4339 C1).
    val toolNames = remember(machines.map { it.id to it.name }, key.machineId, glass) { ToolNames(machines, key.machineId, glass = glass) }
    // Whose the conversation is, for what its links open (docs/assistant/managers.md "Clients"): its session's manager, else
    // for an assistant conversation the one whose page listed it.
    val linkManager = session?.manager ?: if (renderStyle == ChatRenderStyle.Assistant)
        persona.core.contracts.api.AssistantManagerChoice.itemHeader(listOf("assistant", "conversations", key.sessionId)) else null
    return ChatPageLinks(chatPorts, mentions, cardsHost, callJumps, toolNames, linkManager)
}

/** What [rememberChatPageLinks] hands back to ChatPage: plain values, and the state objects ChatPage reads and writes. */
internal class ChatPageLinks(
    val chatPorts: ChatPorts?,
    val mentions: ComposerMentions?,
    val cardsHost: persona.core.domain.views.AssistantCardsHost?,
    val callJumps: ChatCallJumps,
    val toolNames: ToolNames,
    val linkManager: String?,
)

/** ChatPage's per-line indexes for the content rows: photos, source labels, work groups, last steps, plans (moved as is). */
@Composable
internal fun rememberChatPageContent(lines: List<ChatLine>): ChatPageContent {
    val contentUserImages = remember(lines) {
        buildMap<String, List<Item>> {
            lines.asReversed().zipWithNext().forEach { (photos, user) ->
                if (photos is ChatLine.UserImages && user is ChatLine.Row && user.row.type == "user") {
                    put(user.id, photos.images.map { it.item })
                }
            }
        }
    }
    val contentHiddenImages = remember(lines, contentUserImages) {
        lines.asReversed().zipWithNext().mapNotNull { (photos, user) ->
            photos.id.takeIf { photos is ChatLine.UserImages && user.id in contentUserImages }
        }.toSet()
    }
    val contentSourceLabels = remember(lines) { sourceLabelIds(lines.asReversed()) }
    val contentWorkGroups = remember(lines) { lines.filterIsInstance<ChatLine.Work>().associateBy { it.id } }
    val contentLastSteps = remember(lines) { lines.asReversed().filterIsInstance<ChatLine.Step>().associate { it.groupId to it.step.id } }
    // Each open work line's plan card (web #1990 draws only the latest plan of an exchange).
    val contentPlans = remember(contentWorkGroups) { contentWorkGroups.filterValues { it.open }.mapValues { latestPlan(it.value.steps) } }
    return ChatPageContent(contentUserImages, contentHiddenImages, contentSourceLabels, contentWorkGroups, contentLastSteps,
        contentPlans)
}

/** What [rememberChatPageContent] hands back to ChatPage: plain values, and the state objects ChatPage reads and writes. */
internal class ChatPageContent(
    val contentUserImages: Map<String, List<Item>>,
    val contentHiddenImages: Set<String>,
    val contentSourceLabels: Set<String>,
    val contentWorkGroups: Map<String, ChatLine.Work>,
    val contentLastSteps: Map<String, String>,
    val contentPlans: Map<String, LinePlan?>,
)

/** ChatPage's assistant trays and the background-fold setting, with their three effects (moved as is). */
@Composable
internal fun rememberChatPageTrays(key: SessionKey, chrome: ChatChromeBindings, renderStyle: ChatRenderStyle,
    connection: ConnectionState?): ChatPageTrays {
    val assistantTrays = remember(key) { AssistantTrayState() }
    val trayApprovingState = remember(key) { mutableStateOf(false) }
    val traySendingState = remember(key) { mutableStateOf(false) }
    val trayPlanState = remember(key) { mutableStateOf<persona.core.contracts.TaskPlan?>(null) }
    val trayScheduleState = remember(key) { mutableStateOf<ScheduledTask?>(null) }
    val traySendIds = remember(chrome.inboxScope, chrome.inboxTrays) { mutableMapOf<AssistantInboxDraft, String>() }
    val trayDraftSource = remember(chrome.inboxTrays, chrome.inboxScope) {
        chrome.inboxScope?.let { chrome.inboxTrays?.drafts(it) } ?: kotlinx.coroutines.flow.flowOf(emptyList())
    }
    val inboxDraftsState = trayDraftSource.collectAsStateWithLifecycle(emptyList())
    val traySchedulesState = remember(chrome.assistantManagement) { mutableStateOf(emptyList<persona.core.contracts.api.ScheduledTask>()) }
    var traySchedules by traySchedulesState
    LaunchedEffect(chrome.assistantManagement, connection, assistantTrays.expanded) {
        if (renderStyle == ChatRenderStyle.Assistant && assistantTrays.expanded == AssistantTray.Running) {
            try { traySchedules = chrome.assistantManagement?.tasks().orEmpty() }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { /* A failed read retains the last confirmed schedule list. */ }
        }
    }
    // #4314 "Fold background rounds" (#6595 widens it to the leader conversations): the hub's switch (until read, the
    // value last read this run, else on; on for a hub without it), the main conversation and the hub's leader ids.
    var foldSetting by remember(chrome.assistantManagement, key) { mutableStateOf(BackgroundFoldSetting.lastKnown(key.sessionId)) }
    LaunchedEffect(chrome.assistantManagement, connection) {
        if (renderStyle != ChatRenderStyle.Assistant) return@LaunchedEffect
        try {
            chrome.assistantManagement?.config()?.let {
                foldSetting = BackgroundFoldSetting(it.backgroundFolded, it.main)
                BackgroundFoldSetting.store(key.sessionId, foldSetting)
            }
        }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { /* Unread: the default (folded) stands, as on a hub without the setting. */ }
    }
    val mainConversation by (chrome.tasks?.mainConversation ?: remember { kotlinx.coroutines.flow.MutableStateFlow<String?>(null) })
        .collectAsStateWithLifecycle()
    val foldsBackground = renderStyle.compactToolSummary &&
        persona.core.domain.tasks.BackgroundFolding.foldsConversation(
            key.sessionId, mainConversation ?: foldSetting.main, foldSetting.on, chrome.leaderConversations,
        )
    LaunchedEffect(chrome.inboxTrays, chrome.inboxScope, assistantTrays.expanded) {
        if (renderStyle == ChatRenderStyle.Assistant && assistantTrays.expanded == AssistantTray.NeedsYou) {
            try { chrome.inboxScope?.let { chrome.inboxTrays?.refresh(it) } }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { /* Live or cached drafts remain visible. */ }
        }
    }
    return ChatPageTrays(assistantTrays, trayApprovingState, traySendingState, trayPlanState, trayScheduleState, traySendIds,
        inboxDraftsState, traySchedulesState, foldsBackground)
}

/** What [rememberChatPageTrays] hands back to ChatPage: plain values, and the state objects ChatPage reads and writes. */
internal class ChatPageTrays(
    val assistantTrays: AssistantTrayState,
    val trayApproving: MutableState<Boolean>,
    val traySending: MutableState<Boolean>,
    val trayPlan: MutableState<persona.core.contracts.TaskPlan?>,
    val traySchedule: MutableState<ScheduledTask?>,
    val traySendIds: MutableMap<AssistantInboxDraft, String>,
    val inboxDrafts: State<List<AssistantInboxDraft>>,
    val traySchedules: MutableState<List<persona.core.contracts.api.ScheduledTask>>,
    val foldsBackground: Boolean,
)

/** ChatPage's lines: the projection as presented, with task cards, subagents and model changes (moved as is). */
@Composable
internal fun rememberChatPageLines(key: SessionKey, renderStyle: ChatRenderStyle,
    snapshot: persona.core.contracts.api.ChatSnapshot, session: Session?, foldsBackground: Boolean,
    tasks: persona.core.domain.tasks.TasksStore?, taskTodos: List<persona.core.contracts.Todo>, search: ChatSearchState,
    openWork: Set<String>, projection: persona.core.domain.lines.ChatSnapshot, feed: List<persona.core.contracts.FeedItem>,
    taskPlans: Map<String, TaskPlan>, taskCardProjection: Map<String, List<AssistantTaskCard>>,
    children: List<Session> = emptyList()): ChatPageLines {
    val jumpItemState = remember { mutableStateOf<String?>(null) }
    var jumpItem by jumpItemState
    // Glass draws the hub's task records among the lines, as iOS does (#4339 C13), and folds a supervision round into one
    // line there rather than moving it to the background record; Brief and Board keep both behind the trays.
    val inlineRecords = PersonaTheme.style == AppStyle.Glass
    val backgroundFolding = if (foldsBackground || (inlineRecords && renderStyle.compactToolSummary))
        persona.core.domain.tasks.BackgroundFolding(openWork, search.selected?.itemId ?: jumpItem, rounds = foldsBackground) else null
    val assistantPresentation = remember(projection, snapshot.items, feed, renderStyle, backgroundFolding, inlineRecords) {
        if (renderStyle.compactToolSummary) {
            val presentation = persona.core.domain.tasks.AssistantConversation.project(projection.lines, snapshot.items, backgroundFolding,
                inline = inlineRecords)
            presentation.copy(background = persona.core.domain.tasks.AssistantLines.lines(presentation.background,
                persona.core.domain.tasks.AssistantTaskCards.backgroundFeed(feed, inlineRecords)))
        } else persona.core.domain.tasks.AssistantConversation(projection.lines, emptyList())
    }
    LaunchedEffect(key, tasks) { tasks?.load(key.sessionId); tasks?.loadTodayIfMain(key.sessionId) }
    // Glass draws the subagents a subagent launched as task cards of their own among the steps, by when they started
    // (#5045, DESIGN_SYSTEM.md 4.7 "Task cards"); Brief and Board do not.
    val subagentCards = PersonaTheme.style == AppStyle.Glass
    val nestedSubagents = if (subagentCards) session?.subagents.orEmpty() else emptyList()
    val viewedStrips = PersonaTheme.style == AppStyle.Glass
    // Where the replies' model changes, a divider (#5055, the web's ModelChangeDivider): Glass's, as its model pill.
    val modelChanges = PersonaTheme.style == AppStyle.Glass
    val childPlaces = remember(key) { persona.core.domain.lines.ChildSessionPlaces() }
    val places = remember(children) { childPlaces.places(children) }
    val lines = remember(assistantPresentation, search.selected, jumpItem, feed, tasks, taskPlans, taskTodos, taskCardProjection, renderStyle, inlineRecords, nestedSubagents, viewedStrips, modelChanges, places, snapshot.hasOlder, openWork) {
        searchVisibleLines(assistantPresentation.foreground, search.selected?.itemId ?: jumpItem)
            .let {
                when {
                    tasks == null -> it
                    renderStyle.compactToolSummary -> persona.core.domain.tasks.AssistantTaskCards.lines(it, feed, taskPlans,
                        taskTodos, taskCardProjection[key.sessionId].orEmpty(), inline = inlineRecords)
                    else -> persona.core.domain.tasks.AssistantLines.lines(it,
                        persona.core.domain.tasks.SentToAgents.shown(feed))
                }
            }.let { persona.core.domain.lines.Subagents.withNested(it, nestedSubagents) }
            .let { persona.core.domain.lines.ChildSessionCards.lines(it, places, !snapshot.hasOlder, openWork) }
            // The strip of what a turn's work looked at is Glass's, as iOS draws it (#4339 C9); Brief and Board keep their lines.
            .let { if (viewedStrips) it else it.filterNot { line -> line is ChatLine.Viewed } }
            .let { if (modelChanges) persona.core.domain.lines.ModelChanges.mark(it) else it }.asReversed()
    }
    return ChatPageLines(jumpItemState, assistantPresentation, subagentCards, lines)
}

/** What [rememberChatPageLines] hands back to ChatPage: plain values, and the state objects ChatPage reads and writes. */
internal class ChatPageLines(
    val jumpItem: MutableState<String?>,
    val assistantPresentation: persona.core.domain.tasks.AssistantConversation,
    val subagentCards: Boolean,
    val lines: List<ChatLine>,
)

/** ChatPage's open recap line and what closes it (moved as is). */
@Composable
internal fun rememberChatPageRecap(key: SessionKey, composer: ChatComposerState, readOnly: Boolean, lines: List<ChatLine>,
    glass: Boolean, agentChat: Boolean): ChatPageRecap {
    val recapContext = LocalContext.current
    val closedRecaps = remember(recapContext) { ClosedRecaps(recapContext) }
    var closedRecap by remember(key) { mutableStateOf(closedRecaps.closed(key)) }
    val recapShown = if (glass && agentChat && !readOnly) openRecapId(lines, closedRecap) else null
    val openRecap = remember(key, recapShown) {
        recapShown?.let { id -> OpenRecap(id) { closedRecaps.close(key, id); closedRecap = id } }
    }
    val currentOpenRecap by rememberUpdatedState(openRecap)
    LaunchedEffect(composer) {
        snapshotFlow { composer.value.text }.drop(1).collect { typed -> if (typed.isNotEmpty()) currentOpenRecap?.close?.invoke() }
    }
    return ChatPageRecap(openRecap)
}

/** What [rememberChatPageRecap] hands back to ChatPage: plain values, and the state objects ChatPage reads and writes. */
internal class ChatPageRecap(
    val openRecap: OpenRecap?,
)

/** ChatPage's subagent links and workflow cards, with what is folded in them (moved as is). */
@Composable
internal fun rememberChatPageSubagents(key: SessionKey, chrome: ChatChromeBindings, readOnly: Boolean, session: Session?): ChatPageSubagents {
    val subagentLinks = remember(session?.subagents, chrome.onOpenSession, readOnly, key.machineId) {
        val listed = session?.subagents.orEmpty()
        if (listed.isEmpty()) null
        else ChatSubagents(listed.filter { !it.toolUseId.isNullOrBlank() && it.sessionId.isNotBlank() }
            .associate { it.toolUseId!! to SessionKey(key.machineId, it.sessionId) },
            chrome.onOpenSession?.takeIf { !readOnly }, listed.associateBy(persona.core.domain.lines.Subagents::callOf))
    }
    val subagentFolds = remember(key) { SubagentFolds() }
    // A Workflow call draws its run's card (#3435): the session's runs by the call that launched them, their agents
    // opening as "View sub-agent" does. What the reader opens or folds in them lasts while this chat is open.
    val workflowFolds = remember(key) { persona.core.designsystem.WorkflowFolds() }
    val workflowCards = remember(session?.workflows, chrome.onOpenSession, readOnly, key.machineId, workflowFolds) {
        val open = chrome.onOpenSession?.takeIf { !readOnly }
        session?.workflows.orEmpty().filter { !it.toolUseId.isNullOrBlank() }.associateBy { it.toolUseId!! }
            .takeIf { it.isNotEmpty() }
            ?.let { runs -> ChatWorkflows(runs, workflowFolds, open?.let { o -> { agent -> o(SessionKey(key.machineId, agent.sessionId)) } }) }
    }
    return ChatPageSubagents(subagentLinks, subagentFolds, workflowCards)
}

/** What [rememberChatPageSubagents] hands back to ChatPage: plain values, and the state objects ChatPage reads and writes. */
internal class ChatPageSubagents(
    val subagentLinks: ChatSubagents?,
    val subagentFolds: SubagentFolds,
    val workflowCards: ChatWorkflows?,
)

/** ChatPage's reader state: the plan sheet, seen tracking, the newest visible item, the image loader (moved as is). */
@Composable
internal fun rememberChatPageReader(key: SessionKey, repository: ChatRepository,
    snapshot: persona.core.contracts.api.ChatSnapshot, chatList: ChatListState, lines: List<ChatLine>): ChatPageReader {
    val planSheetState = remember { mutableStateOf(false) }
    val sheetPlanState = remember { mutableStateOf<LinePlan?>(null) }
    val currentItemsState = rememberUpdatedState(snapshot.items)
    // Rows map to lines by key, never by position: a failed send can sit between lines (#2769).
    val currentLinesByKeyState = rememberUpdatedState(remember(lines) { lines.associateBy { it.contentKey } })
    val currentLinesByKey by currentLinesByKeyState
    val list = chatList.list
    val reportedSeenState = remember { mutableStateOf<Item?>(null) }
    var reportedSeen by reportedSeenState
    val newestVisibleItemState = remember {
        derivedStateOf {
            list.layoutInfo.visibleItemsInfo.mapNotNull { visible ->
                currentLinesByKey[visible.key]?.lastItem()
            }.maxWithOrNull(chatItemOrder)
        }
    }
    val readThrough = listOfNotNull(reportedSeen, snapshot.items.firstOrNull { it.id == snapshot.lastSeenMessageId }).maxWithOrNull(chatItemOrder)
    val context = androidx.compose.ui.platform.LocalContext.current
    val pairingLoader = remember(repository, key) { (repository as? ChatLocalStorage)?.imageLoader(key) }
    val loader = remember(context, repository, pairingLoader) {
        pairingLoader ?: chatImageLoader(context.applicationContext, repository)
    }
    val markdownCache = remember { ChatMarkdownCache() }
    return ChatPageReader(planSheetState, sheetPlanState, currentItemsState, currentLinesByKeyState, list, reportedSeenState,
        newestVisibleItemState, readThrough, context, pairingLoader, loader, markdownCache)
}

/** What [rememberChatPageReader] hands back to ChatPage: plain values, and the state objects ChatPage reads and writes. */
internal class ChatPageReader(
    val planSheet: MutableState<Boolean>,
    val sheetPlan: MutableState<LinePlan?>,
    val currentItems: State<List<Item>>,
    val currentLinesByKey: State<Map<String, ChatLine>>,
    val list: androidx.compose.foundation.lazy.LazyListState,
    val reportedSeen: MutableState<Item?>,
    val newestVisibleItem: State<Item?>,
    val readThrough: Item?,
    val context: android.content.Context,
    val pairingLoader: coil3.ImageLoader?,
    val loader: coil3.ImageLoader,
    val markdownCache: ChatMarkdownCache,
)

/** ChatPage's list state and header fold: the list, search, the fold and its scroll, the open work lines (moved as is). */
@Composable
internal fun rememberChatPageHeaderFold(readingKey: ChatReadingKey?, anchorStore: ChatAnchorStore?,
    renderStyle: ChatRenderStyle, showTopBar: Boolean, scope: kotlinx.coroutines.CoroutineScope): ChatPageHeaderFold {
    val chatList = rememberChatListState(readingKey, anchorStore)
    val search = remember { ChatSearchState() }
    // Glass folds an Agent chat's header into its slim bar (#4339 C4, iOS ChatChrome): on the list's scrolling, and by
    // itself after 3 s without a touch while animations are on.
    val foldsHeader = HeaderFoldRules.folds(PersonaTheme.style, showTopBar, renderStyle)
    val activityInHeader = ChatActivity.inHeader(PersonaTheme.style, showTopBar, renderStyle)
    val reduceMotion = PersonaTheme.reduceMotion
    val headerFold = remember { ChatHeaderFold(scope) }
    val headerMotion = PersonaTheme.motion.layout?.spec<Float>()
    SideEffect { headerFold.moves = !reduceMotion; headerFold.motion = headerMotion }
    val newestEdge = with(LocalDensity.current) { HeaderFoldRules.NEWEST_EDGE_DP.dp.roundToPx() }
    val headerScroll = remember(headerFold, chatList, newestEdge) {
        headerFold.connection { chatList.list.firstVisibleItemIndex == 0 && chatList.list.firstVisibleItemScrollOffset < newestEdge }
    }
    // Not chatList.position: that changes every frame of a scroll, and reading it here re-ran this whole page (#4039).
    val openWork = chatList.expanded
    return ChatPageHeaderFold(chatList, search, foldsHeader, activityInHeader, reduceMotion, headerFold, headerScroll, openWork)
}

/** What [rememberChatPageHeaderFold] hands back to ChatPage: plain values, and the state objects ChatPage reads and writes. */
internal class ChatPageHeaderFold(
    val chatList: ChatListState,
    val search: ChatSearchState,
    val foldsHeader: Boolean,
    val activityInHeader: Boolean,
    val reduceMotion: Boolean,
    val headerFold: ChatHeaderFold,
    val headerScroll: androidx.compose.ui.input.nestedscroll.NestedScrollConnection,
    val openWork: Set<String>,
)

/** ChatPage's timeline and the wait after a send: whether the turn counts as started and live (moved as is). */
@Composable
internal fun rememberChatPageAwait(snapshot: persona.core.contracts.api.ChatSnapshot, sendReceipts: List<MessageSendReceipt>,
    readOnly: Boolean, session: Session?, messages: List<OutboxMessage>): ChatPageAwait {
    val timelineVersionState = remember { mutableIntStateOf(0) }
    val timeline = remember { ChatTimeline() }
    // Sent, the hub not saying working yet (#3909): the turn counts as started for a minute after the message.
    var awaitNow by remember { mutableStateOf(java.time.Instant.now()) }
    val awaited = remember(snapshot.items) { awaitedMessage(snapshot.items) }
    val startFailed = !session?.startError.isNullOrBlank()
    val awaiting = !readOnly && sendReceipts.none { it.processing } && awaitingTurn(awaited, session?.status, startFailed, awaitNow)
    val awaitingStatus = awaiting || (!readOnly && awaitingDelivered(messages, session?.status, startFailed))
    LaunchedEffect(awaited?.id, awaited?.ts, session?.status) {
        // Draw again when the wait runs out, so a message nobody picks up stops looking worked on.
        awaitNow = java.time.Instant.now()
        val at = parseTime(awaited?.ts) ?: return@LaunchedEffect
        val left = AWAIT_TURN_MILLIS - java.time.Duration.between(at, awaitNow).toMillis()
        if (left <= 0) return@LaunchedEffect
        delay(left + 10)
        awaitNow = java.time.Instant.now()
    }
    val live = isLiveStatus(session?.status) || awaiting
    return ChatPageAwait(timelineVersionState, timeline, awaiting, awaitingStatus, live)
}

/** What [rememberChatPageAwait] hands back to ChatPage: plain values, and the state objects ChatPage reads and writes. */
internal class ChatPageAwait(
    val timelineVersion: MutableIntState,
    val timeline: ChatTimeline,
    val awaiting: Boolean,
    val awaitingStatus: Boolean,
    val live: Boolean,
)

/** ChatPage's session: the row, its detail and closeout, and what the page may do with it (moved as is). */
@Composable
internal fun rememberChatPageSession(key: SessionKey, chrome: ChatChromeBindings, renderStyle: ChatRenderStyle,
    historyReadOnly: Boolean, historyFooter: (@Composable () -> Unit)?, historySession: Session?,
    res: android.content.res.Resources, sessionList: List<Session>, adoptedSessions: List<Session>): ChatPageSession {
    val parent = sessionList.firstOrNull { it.machineId == key.machineId && it.subagents.orEmpty().any { child -> child.sessionId == key.sessionId } }
    val child = parent?.subagents?.firstOrNull { it.sessionId == key.sessionId }
    // A workflow's agent reads like a subagent (`<session>@<agent id>`, #2935).
    val workflowParent = if (child != null) null else sessionList.firstOrNull { s ->
        s.machineId == key.machineId && s.workflows.orEmpty().any { w -> w.agents.orEmpty().any { it.sessionId == key.sessionId } }
    }
    val workflowAgent = workflowParent?.workflows.orEmpty().firstNotNullOfOrNull { w -> w.agents.orEmpty().firstOrNull { it.sessionId == key.sessionId } }
    // Its manager offline (several managers): history only, its 最后同步于 where the composer was, wherever the
    // conversation was opened (the assistant page says so itself through historyReadOnly).
    val ownerOffline = persona.core.designsystem.managerOfflineNote(res, persona.core.designsystem.LocalManagerRoster.current,
        persona.core.designsystem.LocalManagerGroup.current, key.machineId, sessionList.firstOrNull { it.key == key }?.manager)
    val historyOnly = historyReadOnly || ownerOffline != null
    val footer: (@Composable () -> Unit)? = historyFooter
        ?: ownerOffline?.let { note -> @Composable { persona.core.designsystem.JNotice(note, status = persona.core.designsystem.Status.Offline) } }
    val readOnly = historyOnly || child != null || workflowAgent != null || key.sessionId.contains('@')
    var lastSession by remember(key) { mutableStateOf<Session?>(null) }
    var detailInfo by remember(key) { mutableStateOf<SessionInfo?>(null) }
    val adopted = adoptedSessions.lastOrNull { it.key == key }
    val row = sessionList.firstOrNull { it.key == key }?.let {
        // Summary rows from older hubs omit this flag; a waiting pane keeps its id only until it becomes a thread.
        if (adopted?.awaitingFirstMessage == true) it.copy(awaitingFirstMessage = true) else it
    } ?: adopted
    LaunchedEffect(key, row?.waiting != null, row?.rev, row?.model, chrome.actions) {
        if (row?.waiting == null) return@LaunchedEffect
        try { detailInfo = chrome.actions?.info(row) }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { /* Keep held detail; the info sheet still offers an explicit retry. */ }
    }
    // What an Agent's summary row leaves out and `info` doesn't say: the launcher switch waiting for the next message
    // (#5055), read from the session's complete row where the hub looks one up (`sessions_lookup`); Glass's pill shows it.
    var completeRow by remember(key) { mutableStateOf<Session?>(null) }
    val lookup = "sessions_lookup" in LocalHubFeatures.current && renderStyle != ChatRenderStyle.Assistant &&
        PersonaTheme.style == AppStyle.Glass && AgentModelPanel.LAUNCHER_SWITCH in LocalHubFeatures.current
    LaunchedEffect(key, row?.waiting != null, row?.rev, chrome.actions, lookup) {
        if (row?.waiting == null || !lookup) return@LaunchedEffect
        try { completeRow = chrome.actions?.completeRow(row) }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { /* The switch the chat held stays. */ }
    }
    val liveSession = row?.let { sessionWithDetail(it, lastSession, detailInfo, completeRow) } ?: child?.let {
        parent?.copy(sessionId = it.sessionId, title = it.description?.takeIf(String::isNotBlank) ?: it.type,
            status = if (it.running) "working" else "done", state = if (it.running) "working" else "unread", subagents = emptyList(),
            queued = null, loop = null, background = null, pendingInteractions = 0)
    } ?: workflowAgent?.let {
        // Its own model (#3435: a phone's workflow card leaves it out of the agent's row), else the parent's.
        workflowParent?.copy(sessionId = it.sessionId, title = it.label?.takeIf(String::isNotBlank) ?: it.id,
            status = if (it.running) "working" else "done", state = if (it.running) "working" else "unread", subagents = emptyList(),
            queued = null, loop = null, background = null, workflows = null, pendingInteractions = 0,
            model = it.model?.takeIf(String::isNotBlank) ?: workflowParent.model)
    } ?: lastSession
    SideEffect { if (liveSession != null) lastSession = liveSession }
    val session = liveSession ?: readOnlySessionMetadata(key, historyReadOnly, historySession)
    return ChatPageSession(child, workflowAgent, historyOnly, footer, readOnly, row, session)
}

/** What [rememberChatPageSession] hands back to ChatPage: plain values, and the state objects ChatPage reads and writes. */
internal class ChatPageSession(
    val child: persona.core.contracts.Subagent?,
    val workflowAgent: persona.core.contracts.WorkflowAgent?,
    val historyOnly: Boolean,
    val footer: (@Composable () -> Unit)?,
    val readOnly: Boolean,
    val row: Session?,
    val session: Session?,
)

/** ChatPage's launcher switch and closeout: the switch drawn here, the closeout card's detail (moved as is). */
@Composable
internal fun rememberChatPageCloseout(key: SessionKey, chrome: ChatChromeBindings, renderStyle: ChatRenderStyle,
    child: persona.core.contracts.Subagent?, workflowAgent: persona.core.contracts.WorkflowAgent?, row: Session?,
    session: Session?): ChatPageCloseout {
    val drawnSwitchState = remember(key) { mutableStateOf<DrawnSwitch?>(null) }
    var drawnSwitch by drawnSwitchState
    val switchSession = session?.let { drawnSwitch.over(it) }
    // A stopped agent's closeout is a card above the transcript (#3612): its own session's, never an assistant
    // conversation's, a subagent's or a workflow agent's (those pages borrow their parent's row).
    val listedCloseout = session?.takeIf { renderStyle != ChatRenderStyle.Assistant && !it.assistant && child == null && workflowAgent == null }?.shownCloseout
    // A list row is a summary: where the closeout stands. What the agent did and verified, and what it produced, come
    // with the whole row, read once per closeout; until then (and from a hub without the lookup) the card shows the row's part.
    var closeoutDetail by remember(key) { mutableStateOf<Session?>(null) }
    LaunchedEffect(key, listedCloseout, row?.rev, chrome.tasks) {
        if (listedCloseout == null) { closeoutDetail = null; return@LaunchedEffect }
        if (row?.waiting == null) return@LaunchedEffect
        try { chrome.tasks?.sessionDetail(key)?.takeIf { it.sessionId == key.sessionId }?.let { closeoutDetail = it } }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { /* Keep the row's part: the card still says where the work stands. */ }
    }
    val closeoutWork = listedCloseout?.let { closeoutDetail?.takeIf { detail -> detail.shownCloseout != null }?.work ?: session?.work }
    val closeoutShown = closeoutWork?.closeout
    return ChatPageCloseout(drawnSwitchState, switchSession, closeoutWork, closeoutShown)
}

/** What [rememberChatPageCloseout] hands back to ChatPage: plain values, and the state objects ChatPage reads and writes. */
internal class ChatPageCloseout(
    val drawnSwitch: MutableState<DrawnSwitch?>,
    val switchSession: Session?,
    val closeoutWork: persona.core.contracts.WorkState?,
    val closeoutShown: persona.core.contracts.Closeout?,
)

/** ChatPage's sources: the scope, the snapshot and the flows the page collects (moved as is). */
@Composable
internal fun rememberChatPageSources(key: SessionKey, repository: ChatRepository, sender: MessageSender,
    interactions: InteractionCenter, sessions: SessionsRepository, chrome: ChatChromeBindings,
    textUpdates: Map<String, ChatTextUpdate>, statusBindings: ChatStatusBindings): ChatPageSources {
    val scope = rememberCoroutineScope()
    val haptics = persona.core.designsystem.rememberHaptics()
    val res = LocalResources.current
    val recovery = remember(key, statusBindings.recovery) { ChatStatusRecovery(res, statusBindings.recovery, scope, sessions::refresh) }
    val source = remember(repository, key) { repository.observe(key) }
    val snapshotState = source.collectAsStateWithLifecycle(ChatSnapshot())
    val snapshot by snapshotState
    val liveTextUpdates = remember(snapshot.items, snapshot.streamingItemIds, textUpdates) {
        snapshot.items.filter { it.id in snapshot.streamingItemIds }.associate {
            it.id to ChatTextUpdate(it.text.orEmpty(), streaming = true)
        } + textUpdates
    }
    val application = LocalContext.current.applicationContext
    val renamer = chrome.renamer ?: remember(application) {
        if (application is GeneratedComponentManager<*>)
            EntryPointAccessors.fromApplication(application, ChatRenameServices::class.java).sessionRenamer()
        else null
    }
    // Only the page's own row joins metadata. Looked-up children remain fallback cards, so bounded lists still refresh them.
    val lookupRows = chrome.lookupRows.filter { it.key == key }
    val sessionSource = remember(renamer, sessions, lookupRows) {
        val source = renamer?.project(sessions.sessions) ?: sessions.sessions
        if (lookupRows.isEmpty()) source else source.map { rows ->
            val missing = lookupRows.filter { found -> rows.none { it.key == found.key } }
            if (missing.isEmpty()) rows else rows + missing
        }
    }
    // Read once per source, not on every recomposition: only the first value seeds the collected state.
    val initialSessions = remember(sessionSource) {
        if (lookupRows.isEmpty()) sessions.sessions.value else (sessions.sessions.value + lookupRows).distinctBy { it.key }
    }
    val sessionListState = sessionSource.collectAsStateWithLifecycle(initialSessions)
    val managerChoiceState = AssistantManagerChoice.current.collectAsStateWithLifecycle()
    val machinesState = sessions.machines.collectAsStateWithLifecycle()
    val allRequestsState = interactions.pending.collectAsStateWithLifecycle()
    val allMessagesState = sender.outbox.collectAsStateWithLifecycle()
    val allMessages by allMessagesState
    val adoptedSessionsState = sessions.adoptedSessions.collectAsStateWithLifecycle()
    val startedThreadsState = sessions.startedThreads.collectAsStateWithLifecycle()
    val writtenAtState = sender.writtenAt.collectAsStateWithLifecycle()
    val allSendReceipts by key(sender) { sender.sendReceipts.collectAsStateWithLifecycle() }
    val sendReceipts = allSendReceipts.filter { receipt -> receipt.session == key ||
        (receipt.session == PendingMainConversation && receipt.mainTarget &&
            allMessages.any { it.session == key && it.clientMessageId == receipt.clientMessageId }) }
    return ChatPageSources(scope, haptics, res, recovery, snapshotState, liveTextUpdates, renamer, sessionListState,
        managerChoiceState, machinesState, allRequestsState, allMessagesState, adoptedSessionsState, startedThreadsState,
        writtenAtState, sendReceipts)
}

/** What [rememberChatPageSources] hands back to ChatPage: plain values, and the state objects ChatPage reads and writes. */
internal class ChatPageSources(
    val scope: kotlinx.coroutines.CoroutineScope,
    val haptics: (Haptic) -> Unit,
    val res: android.content.res.Resources,
    val recovery: ChatStatusRecovery,
    val snapshot: State<persona.core.contracts.api.ChatSnapshot>,
    val liveTextUpdates: Map<String, ChatTextUpdate>,
    val renamer: persona.core.domain.sort.SessionRenamer?,
    val sessionList: State<List<Session>>,
    val managerChoice: State<AssistantManagerChoice.Choice>,
    val machines: State<List<Machine>>,
    val allRequests: State<List<Interaction>>,
    val allMessages: State<List<OutboxMessage>>,
    val adoptedSessions: State<List<Session>>,
    val startedThreads: State<Map<SessionKey, Session>>,
    val writtenAt: State<Map<String, Long>>,
    val sendReceipts: List<MessageSendReceipt>,
)

/** ChatPage's folds and terminal: Markdown readiness, folds being fetched, the terminal actions (moved as is). */
@Composable
internal fun rememberChatPageFolds(key: SessionKey, onTerminal: ((SessionKey) -> Unit)?, renderStyle: ChatRenderStyle,
    readOnly: Boolean, canTerminal: Boolean, pairingLoader: coil3.ImageLoader?, loader: coil3.ImageLoader,
    onResumeInTerminal: ((SessionKey) -> Unit)?): ChatPageFolds {
    val markdownReadyForState = remember { mutableStateOf<List<Item>?>(null) }
    val markdownSplit = remember { MarkdownPrewarm() }
    DisposableEffect(loader, pairingLoader) { onDispose { if (pairingLoader == null) loader.shutdown() } }
    val foldingState = remember { mutableStateOf(emptySet<String>()) }
    val foldErrorsState = remember { mutableStateOf(emptyMap<String, String>()) }
    val terminal: (() -> Unit)? = if (readOnly || !canTerminal) null else onTerminal?.let { action -> { action(key) } }
    // A session without a terminal (a Codex thread, an agent runtime's) has nothing to open or explain (#4339 C7).
    val hasTerminal = ChatTerminalPlaces.has(readOnly, canTerminal, renderStyle)
    val agentTerminal = terminal?.takeIf { hasTerminal }
    val resumeInTerminal: (() -> Unit)? = onResumeInTerminal?.let { action -> { action(key) } }
    val lifecycleState = LocalLifecycleOwner.current.lifecycle.currentStateAsState()
    return ChatPageFolds(markdownReadyForState, markdownSplit, foldingState, foldErrorsState, terminal, hasTerminal,
        agentTerminal, resumeInTerminal, lifecycleState)
}

/** What [rememberChatPageFolds] hands back to ChatPage: plain values, and the state objects ChatPage reads and writes. */
internal class ChatPageFolds(
    val markdownReadyFor: MutableState<List<Item>?>,
    val markdownSplit: MarkdownPrewarm,
    val folding: MutableState<Set<String>>,
    val foldErrors: MutableState<Map<String, String>>,
    val terminal: (() -> Unit)?,
    val hasTerminal: Boolean,
    val agentTerminal: (() -> Unit)?,
    val resumeInTerminal: (() -> Unit)?,
    val lifecycle: State<Lifecycle.State>,
)

/** ChatPage's list keys: the header's request ids, the completion feedback, the unread entry and the keys (moved as is). */
@Composable
internal fun rememberChatPageListKeys(chrome: ChatChromeBindings, snapshot: persona.core.contracts.api.ChatSnapshot,
    writtenAt: Map<String, Long>, session: Session?, connection: ConnectionState?, chromeOverlay: Boolean,
    requests: List<Interaction>, messages: List<OutboxMessage>, statusSheet: Boolean, overlay: Boolean, terminalHelp: Boolean,
    taskPageVisible: Boolean, fullText: String?, linkVisible: Boolean, chatList: ChatListState, search: ChatSearchState,
    foldsHeader: Boolean, headerFold: ChatHeaderFold, lines: List<ChatLine>, lifecycle: Lifecycle.State, lastUserId: String?): ChatPageListKeys {
    val requestIds = remember(requests) { requests.map { it.id }.toSet() }
    LaunchedEffect(headerFold, requestIds) { if (foldsHeader) headerFold.requests(requestIds) }
    LaunchedEffect(headerFold, chrome.covered) { if (!chrome.covered) headerFold.open() }
    ChatCompletionFeedback(lastUserId, session?.status,
        lifecycle == Lifecycle.State.RESUMED && !chrome.covered && !overlay && !chromeOverlay && !linkVisible &&
            !statusSheet && !terminalHelp && !taskPageVisible && fullText == null && !search.visible &&
            !search.pending && !search.returning && chatList.restored && !chatList.programmaticScroll &&
            !snapshot.loading && snapshot.verified && requests.isEmpty() && snapshot.error == null &&
            !snapshot.ended && !snapshot.replaced && (connection == null || connection == ConnectionState.Online))
    val unreadEntry = if (session?.agentState == AgentState.Unread) lines.firstOrNull { line ->
        line.holds(lastUserId)
    }?.let { ChatListEntry(it.contentKey, lastLineAtTop = true) } else null
    val placed = remember(lines, messages, writtenAt) { placeFailed(lines.asReversed(), messages, writtenAt) }
    return ChatPageListKeys(unreadEntry, placed)
}

/** What [rememberChatPageListKeys] hands back to ChatPage: plain values, and the state objects ChatPage reads and writes. */
internal class ChatPageListKeys(
    val unreadEntry: ChatListEntry?,
    val placed: persona.core.domain.lines.PlacedMessages,
)
