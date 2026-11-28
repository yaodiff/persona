package persona.feature.agents

import persona.core.contracts.clipped
import persona.core.designsystem.dense
import persona.core.designsystem.text
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.coroutines.launch
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.input.nestedscroll.nestedScroll
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.onClick
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.repeatOnLifecycle
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.sort.canAnswerInteraction
import persona.core.domain.sort.*
import persona.core.domain.answer.firstBySession
import persona.core.domain.engines.Engines
import persona.core.domain.answer.rowLine
import persona.core.domain.answer.askingLine
import persona.core.domain.showsStarCard
import persona.core.domain.showsStarPill
import persona.core.navigation.*
import persona.feature.chat.ChatSourceNote
import persona.feature.chat.CodexTakeoverBusyPill
import persona.feature.chat.CodexTakeoverClient
import persona.feature.chat.CodexTakeoverDialogs
import persona.feature.chat.codexTakeoverMenuActions
import persona.feature.chat.rememberCodexTakeoverCoordinator
import kotlinx.coroutines.delay
import java.time.Instant

private const val AGENTS_PREFETCH_ROWS = 6

/** The unread rows a bulk confirmation counts, and every row it releases from this visit's held Seen places. */
internal data class MarkAllSeenBatch(val unread: List<AgentSession>, val release: List<AgentSession>)

internal fun markAllSeenBatch(rows: List<AgentSession>): MarkAllSeenBatch {
    val release = rows.distinctBy(AgentSession::id)
    return MarkAllSeenBatch(release.filter { it.unseen }, release)
}

/** Inputs supplied by the shell's single merged list. The footer is outside its scroll viewport. */
class AgentsSidebarLayout(
    val prefix: LazyListScope.() -> Unit,
    val footer: @Composable () -> Unit,
    val waiting: List<NeedsYouRow>,
    val onSearch: () -> Unit,
    val searchPick: Session?,
    val onSearchPicked: () -> Unit,
)

private const val NeedsYouListKey = "needs-you-title"
private const val AgentsHeadingKey = "sidebar-agents-heading"

@Composable
private fun SidebarNeedsJump(state: LazyListState, motion: ListTapMotion, count: Int, onReveal: () -> Unit) {
    val scope = rememberCoroutineScope()
    var jump by remember { mutableStateOf<kotlinx.coroutines.Job?>(null) }
    val arrow by remember(state, motion) { derivedStateOf {
        val index = motion.composedKeys.indexOf(NeedsYouListKey)
        val visible = state.layoutInfo.visibleItemsInfo
        when {
            index < 0 || visible.isEmpty() -> PhIcons.CaretDown
            index < visible.first().index -> PhIcons.CaretUp
            index > visible.last().index -> PhIcons.CaretDown
            else -> null
        }
    } }
    val haptics = rememberHaptics()
    Box(Modifier.fillMaxWidth().padding(vertical = Tokens.Space.xs), contentAlignment = Alignment.Center) {
        JChip(
            stringResource(persona.core.designsystem.R.string.designsystem_status_needs_you),
            modifier = Modifier.testTag("sidebar-needs-jump"),
            count = count,
            countTone = Status.NeedsYou,
            role = Role.Button,
            trailingIcon = arrow,
            onClick = haptics.tapped {
                jump?.cancel()
                if (NeedsYouListKey !in motion.composedKeys) onReveal()
                jump = scope.launch {
                    val index = withTimeoutOrNull(2_000L) {
                        snapshotFlow { motion.composedKeys.indexOf(NeedsYouListKey) }.first { it >= 0 }
                    } ?: motion.composedKeys.indexOf(AgentsHeadingKey)
                    if (index in 0 until state.layoutInfo.totalItemsCount) {
                        motion.onProgrammaticScroll()
                        state.animateScrollToItem(index)
                    }
                }
            },
        )
    }
}

@Composable
internal fun AgentsScreen(
    model: AgentsViewModel,
    navigator: Navigator,
    metadata: AgentsMetadata,
    creationModel: NewAgentViewModel? = null,
    onMachineSelected: (String) -> Unit = {},
    groups: List<persona.core.contracts.api.HubGroupInfo> = emptyList(),
    /** The hub lists terminal panes (feature terminal_panes): the menu offers "Terminal" under "Machines", as the web's sidebar does. */
    terminalPanes: Boolean = false,
    /** A notification's card to open once it is listed (#4808). */
    cardRequests: CardOpenRequests = AgentsCardOpen,
    sidebar: AgentsSidebarLayout? = null,
    providedListState: androidx.compose.foundation.lazy.LazyListState? = null,
) {
    val latestPage by model.page.collectAsStateWithLifecycle()
    val listState = providedListState ?: rememberLazyListState()
    LaunchedEffect(model, listState, latestPage.pagination.paged) {
        if (!latestPage.pagination.paged) return@LaunchedEffect
        snapshotFlow {
            val layout = listState.layoutInfo
            val last = layout.visibleItemsInfo.lastOrNull()?.index ?: -1
            listState.canScrollBackward && last >= layout.totalItemsCount - AGENTS_PREFETCH_ROWS
        }.distinctUntilChanged().collect { nearEnd -> if (nearEnd) model.loadMore() }
    }
    val connection by model.connection.state.collectAsStateWithLifecycle()
    val group by model.group.collectAsStateWithLifecycle()
    val everyRequest by model.interactions.pending.collectAsStateWithLifecycle()
    // The current assistant's agents (docs/assistant/managers.md "Clients"), and the cards that are theirs.
    val assistant = persona.core.designsystem.currentAssistant()
    // What the list draws: the hub's page, held still for a bounded time while it is scrolled (AgentsFeedHold.kt). The
    // controls and the window size start it over; a rebuilt window is not told apart from a new list.
    val window = androidx.compose.ui.platform.LocalConfiguration.current
    // Tap-driven row motion only (#9600). Hub pushes change [tree] with the flag clear, so they stay put.
    val listTapMotion = remember { ListTapMotion() }
    val tapMotionScope = rememberCoroutineScope()
    val reduceMotion = PersonaTheme.reduceMotion
    // The pill counts what the Needs You section of a page would draw, from the requests the screen shows for that page.
    val feed = rememberAgentsFeed(latestPage, { p ->
        agentsFeedNeedsYou(p, shownRequests(everyRequest, group, assistant, p.sessions, p.hiddenSessions)) { key -> model.operationState.value.sessions[key]?.closed == true }
    }, listOf(group?.namespace, assistant.id, assistant.group, latestPage.settings, latestPage.preferences, PersonaTheme.style,
        window.screenWidthDp, window.screenHeightDp), listState, onProgrammaticScroll = listTapMotion::onProgrammaticScroll)
    val page = feed.page
    LaunchedEffect(model, assistant) { model.show(assistant) }
    // The shell keeps the picked computer for the next launch: it follows the filter however that changes (a chip, or
    // let go by a switch of assistant or hub group).
    val machineSelected by androidx.compose.runtime.rememberUpdatedState(onMachineSelected)
    LaunchedEffect(model) { model.listSettings.map { it.machineFilter }.distinctUntilChanged().collect { machineSelected(it) } }
    val everySession by model.everySession.collectAsStateWithLifecycle()
    val everyMachine by model.everyMachine.collectAsStateWithLifecycle()
    val pending = remember(everyRequest, group, assistant, page.sessions, page.hiddenSessions) {
        shownRequests(everyRequest, group, assistant, page.sessions, page.hiddenSessions)
    }
    val outbox by model.sender.outbox.collectAsStateWithLifecycle()
    val delivery by model.delivery.collectAsStateWithLifecycle()
    val commandInputErrors by model.commandInputErrors.collectAsStateWithLifecycle()
    val requestHistory by model.requestHistory.collectAsStateWithLifecycle()
    val refreshing by model.refreshing.collectAsStateWithLifecycle()
    val notice by model.notice.collectAsStateWithLifecycle()
    val explainSeenRows = !page.preferences.seenRowsNoticed &&
        page.list.groups.any { group -> group.visibleRows.any { it.seenPlace != null } }
    LaunchedEffect(explainSeenRows) { if (explainSeenRows) model.showSeenRowsNotice() }
    val hubNotice by model.hubNotice.collectAsStateWithLifecycle()
    val herdrStates by (model.herdr?.states ?: remember { kotlinx.coroutines.flow.MutableStateFlow(emptyMap<String, HerdrState>()) }).collectAsStateWithLifecycle()
    val startingHerdr by (model.herdr?.starting ?: remember { kotlinx.coroutines.flow.MutableStateFlow(emptySet<String>()) }).collectAsStateWithLifecycle()
    val herdrFailures by (model.herdr?.failures ?: remember { kotlinx.coroutines.flow.MutableStateFlow(emptyMap<String, HerdrFailure>()) }).collectAsStateWithLifecycle()
    val operations by model.operationState.collectAsStateWithLifecycle()
    // Orca's worktree create, rename and delete (#17054), resolved against the catalog's Orca rows.
    val orcaRows by model.orcaWorkspaces.collectAsStateWithLifecycle()
    val worktrees = rememberOrcaWorktreeActions(LocalAgentCreationService.current, page.machines, orcaRows, model::refreshWorkspaceCatalog)
    val archives = model.archives
    val archiveState by (archives?.state ?: remember { kotlinx.coroutines.flow.MutableStateFlow(AgentArchiveSnapshot()) }).collectAsStateWithLifecycle()
    val showArchivesState = rememberSaveable { mutableStateOf(false) }
    var showArchives by showArchivesState
    val overlays = remember { AgentsOverlayState() }
    var archiveTarget by overlays.archiveTarget
    var operationDialog by overlays.operationDialog
    var pickingTodoForSession by overlays.pickingTodoForSession
    var confirmRead by overlays.confirmRead
    var pickingTodoForSpace by overlays.pickingTodoForSpace
    val todos by (model.tasks?.todos ?: remember { kotlinx.coroutines.flow.MutableStateFlow(emptyList()) }).collectAsStateWithLifecycle()
    val sessionsByKey = remember(page.sessions) { page.sessions.associateBy { it.key } }
    // Visible (post-local-seen, pre-foldRows) sessions, so rows can tell whether a parent is Unread only via folded sides (#13524).
    val visibleSessions = remember(page.sessions, page.preferences) { page.sessions.map { it.listSession(page.preferences).session } }
    val visibleSessionsByKey = remember(visibleSessions) { visibleSessions.associateBy { it.key } }
    // By To-do only when the hub has to-dos, as iOS offers it (`DevGrouping.offered(supportsTodos:)`, #4338 A5).
    val todosSupported by (model.tasks?.todosSupported ?: remember { kotlinx.coroutines.flow.MutableStateFlow(false) }).collectAsStateWithLifecycle()
    // "By pinned" only from a hub with the direction tree (#4352), or while it is the one chosen.
    val groupings = offeredGroupings(todosSupported, page.directionsAvailable || page.preferences.grouping == AgentGrouping.Direction)
    // A fallback for when no app feedback is provided (previews, tests): the page's own toast, as the root's.
    val toast = persona.core.designsystem.rememberToastState()
    val feedback = persona.core.designsystem.LocalAppFeedback.current
    // The GitHub star prompts (#5761): the pill under the first-run empty state, the card in the notice slot. None
    // without the app's store (previews, tests).
    val starPrompt = LocalStarPrompt.current
    val starState = starPrompt?.state?.collectAsStateWithLifecycle()?.value
    val context = androidx.compose.ui.platform.LocalContext.current
    var menu by overlays.menu
    val searchVisibleState = rememberSaveable { mutableStateOf(false) }
    var searchVisible by searchVisibleState
    val homeChrome = sidebar != null || LocalHomeChrome.current != null
    val isGlass = PersonaTheme.style == AppStyle.Glass
    val layout = LocalWindowLayout.current
    val optionsInSelectedTab = homeChrome && isGlass && !layout.expanded && !layout.tabletop &&
        connection == ConnectionState.Online
    val firstPageIncomplete = !page.loaded && page.list.groups.isEmpty()
    val newAgentState = rememberSaveable { mutableStateOf(false) }
    var newAgent by newAgentState
    val historyMachineState = rememberSaveable { mutableStateOf<String?>(null) }
    var historyMachine by historyMachineState
    val historySessionState = rememberSaveable { mutableStateOf<String?>(null) }
    var historySession by historySessionState
    val creation = creationModel?.state?.collectAsStateWithLifecycle()?.value
    val allRequestsState = rememberSaveable { mutableStateOf(false) }
    var allRequests by allRequestsState
    // On Glass the requests and their history are a page in ⋯, after Machines and Terminals where iOS keeps its pages
    // (#4338 A1), not a row in the list; Brief and Board keep the row (decision 8).
    val requestsInMenu = PersonaTheme.style == AppStyle.Glass
    val requestDetailState = remember(model) { mutableStateOf<Interaction?>(null) }
    var requestDetail by requestDetailState
    val nowState = remember { mutableStateOf(Instant.now()) }
    val now by nowState
    AgentsScreenEffects(model, navigator, creationModel, creation, newAgentState, historyMachineState, historySessionState,
        cardRequests, everyRequest, allRequestsState, requestDetailState, nowState, page.machines, connection, feedback, toast,
        archives, archiveState, showArchives, notice)
    val nameWithGroup = stringResource(R.string.agents_machine_with_group)
    val names = remember(page.machines, groups, nameWithGroup) { groups.machineNames(page.machines, nameWithGroup) }
    val requests = remember(pending, page.sessions, page.preferences, page.settings, operations.sessions) {
        visibleAgentRequests(pending, page.sessions.map { it.listSession(page.preferences).session }, page.preferences.mutes, page.settings)
            .filter { operations.sessions[it.sessionKey]?.closed != true }
    }
    // Needs You: one row per waiting session, its cards under it (#4338 A1). The section's header, the summary and the
    // Needs You filter count these rows, as iOS's and the tab's badge do; the section shows them under All and Needs You.
    val waitingRows = remember(pending, page.sessions, page.preferences, page.settings.machineFilter, operations.sessions) {
        agentNeedsYouRows(pending, page.sessions.map { it.listSession(page.preferences).session },
            page.settings.copy(quick = AgentQuickFilter.All, query = "")) { operations.sessions[it]?.closed == true }
    }
    val sidebarWaiting = sidebar?.waiting?.let { waiting ->
        remember(waiting, page.settings.machineFilter, group, operations.sessions) {
            waiting.filter {
                operations.sessions[it.key]?.closed != true &&
                    (page.settings.machineFilter.isEmpty() || page.settings.machineFilter == it.key.machineId) &&
                    (group == null || group.admits(it.key.machineId))
            }
        }
    }
    val actionableRows = sidebarWaiting ?: waitingRows
    val needsYou = if (page.settings.query.isEmpty() && page.settings.quick in setOf(AgentQuickFilter.All, AgentQuickFilter.NeedsYou))
        actionableRows else emptyList()
    val quickCounts = page.list.counts + (AgentQuickFilter.NeedsYou to actionableRows.size)
    val messagesBySession = remember(outbox) { outbox.groupBy { it.session } }
    // Each row's own card (the one that came first) and how many it has, as on the space page (#3964).
    val requestCards = remember(pending) { pending.firstBySession() }
    val cardCounts = remember(pending) { pending.distinctBy { it.id }.groupingBy { it.homeKey }.eachCount() }
    val unknownRequests = remember(pending, delivery) {
        pending.filter { delivery[it.id] == RequestDelivery.Unknown }.map { it.sessionKey }.toSet()
    }
    /** Whether a request can be answered now: through its own group's connection when there are several groups. */
    fun reachable(request: Interaction) = groups.stateFor(request.machineId, connection) == ConnectionState.Online
    fun open(session: SessionKey, openSubagents: Boolean = false) {
        navigator.navigate(
            ChatRoute(
                session.machineId,
                session.sessionId,
                kind = if (openSubagents) ConversationKind.Agent else null,
                openSubagents = openSubagents,
            ),
        )
    }
    val takeoverOps = model.operationsApi ?: LocalAgentsOperations.current?.operations
    val takeoverClient = remember(takeoverOps) { takeoverOps?.let(CodexTakeoverClient::of) }
    val takeoverCoordinator = rememberCodexTakeoverCoordinator(
        client = takeoverClient,
        sessions = model.sessions,
        onOpenSession = null,
        fallbackToast = toast,
        onCompleted = { _, adopted ->
            historyMachine = null
            historySession = null
            open(adopted.key)
        },
    )
    fun connectionSettings() { navigator.navigate(ConnectionSettingsRoute) }
    // The title while the hub is out of reach opens why, since when and "Retry now" in place (iOS ConnectionTitle).
    val haptics = persona.core.designsystem.rememberHaptics()
    LaunchedEffect(model) { model.haptics.collect { haptics(it) } }
    val connectionDetails = persona.core.designsystem.rememberConnectionDetails(connection, ::connectionSettings, onRetry = model::refresh)
    fun openRow(row: AgentSession) {
        if (!row.session.isLive) {
            historyMachine = row.session.machineId
            historySession = row.session.sessionId
        } else {
            model.opened(row)
            // A search hit inside a side session (#7944) opens the parent with that side shown.
            open(page.settings.sideHit(row.session, page.sessions, names) ?: row.session.key)
        }
    }
    fun openRowSubagents(row: AgentSession) {
        model.opened(row)
        open(row.session.key, openSubagents = true)
    }
    val legacyNestedKeys = remember(page.list.children) {
        page.list.children.values.flatten().mapTo(HashSet()) { it.session.key }
    }
    val heldChildrenByParent = remember(page.sessions, legacyNestedKeys) {
        val grouped = HashMap<SessionKey, MutableList<Session>>()
        for (candidate in page.sessions) {
            if (candidate.key in legacyNestedKeys) continue
            val target = candidate.foldedUnderKey ?: candidate.takeIf { it.isSubagent }?.parentKey ?: continue
            if (target != candidate.key) grouped.getOrPut(target) { mutableListOf() }.add(candidate)
        }
        grouped
    }
    LaunchedEffect(sidebar?.searchPick) {
        val picked = sidebar?.searchPick ?: return@LaunchedEffect
        sidebar.onSearchPicked()
        openRow(picked.listSession(page.preferences))
    }
    fun newSession(space: AgentSpacePreset? = null, project: AgentProjectFolder? = null) {
        creationModel?.startIn(space, project)
        newAgent = true
    }
    // Built with the page off the main thread (AgentsViewModel).
    val spaces = page.spaces
    // The list rows and the space page share one session menu and one space menu; [nested] rows sit under "Subtasks (N)".
    val resources = agentResources()
    val rowMenu: @Composable (AgentSession, Boolean) -> List<JAction> = { row, nested ->
        val archiveAction = if (archives?.api?.supports(row.session.key) == true && row.session.canArchiveAgent &&
            everyRequest.none { it.sessionKey == row.session.key }) JAction(stringResource(R.string.agents_action_archive),
                icon = PhIcons.Archive, enabled = archiveState.busy == null && !archiveState.loading && !operations.busy) { archiveTarget = row.session } else null
        val forkOnline = groups.stateFor(row.session.machineId, connection) == ConnectionState.Online &&
            page.machines.firstOrNull { it.id == row.session.machineId }?.online != false
        val takeoverActions = if (takeoverClient != null && !row.session.archived) {
            codexTakeoverMenuActions(row.session, takeoverCoordinator, enabled = !operations.busy && forkOnline)
        } else emptyList()
        val forkAction: (() -> Unit)? = if (row.session.canFork && !row.session.archived && model.hasOperations) ({
            model.fork(row.session) { forked -> open(forked) }
        }) else null
        if (!row.session.isLive) buildList {
            add(JAction(stringResource(R.string.agents_action_open_read_only), onClick = { openRow(row) }))
            addAll(takeoverActions)
            forkAction?.let {
                add(JAction(stringResource(persona.feature.chat.R.string.chat_menu_fork), PersonaIcons.forkChat,
                    enabled = !operations.busy && forkOnline, onClick = it))
            }
            archiveAction?.let(::add)
            if (row.session.canDelete) add(JAction(stringResource(R.string.agents_action_delete_chat), destructive = true, enabled = !operations.busy) {
                operationDialog = AgentOperationDialog.Delete(row.session)
            })
        }
        else AgentSessionMenuItems(row, row.id in page.preferences.pins.sessions, page.preferences.mutes.mutes(row), operations.busy,
            onOpen = { openRow(row) }, onPin = { model.pin(row) }, onMute = { model.mute(row) },
            onRead = { model.markRead(listOf(row)) }, onRename = { operationDialog = AgentOperationDialog.Rename(row.session) },
            onStopLoop = { model.stopLoop(row.session) }, onHandoff = {
                model.handoff(row.session, row.session.handoffPrompt(resources), onQueued = { navigator.selectTab(AppTab.Persona) }, onBound = {
                    navigator.navigate(ChatRoute(it.machineId, it.sessionId, kind = ConversationKind.Assistant))
                })
            },
            onClose = { operationDialog = AgentOperationDialog.Close(row.session) },
            // A Codex app-server thread the phone may join live (iOS "Join live").
            onAttach = if (row.session.canAttach) ({ model.attach(row.session) }) else null,
            onFork = forkAction,
            forkOnline = forkOnline,
            takeoverActions = takeoverActions,
            onInterrupt = { model.interrupt(row.session) },
            onLinkTodo = { pickingTodoForSession = row.session },
            onUnlinkTodo = row.session.todo?.let { todo -> { model.unlinkTodo(todo.id, row.session) } },
            onDelete = { operationDialog = AgentOperationDialog.Delete(row.session) }, nested = nested) + listOfNotNull(archiveAction)
    }
    // In the list the space menu also opens the space page; the space page itself puts it in its bar.
    val spaceMenu: @Composable (AgentSpace, Boolean) -> Unit = { space, inList ->
        val session = space.members.first().session
        // Mark all as read: the finished ones nobody has seen, Just finished ones too (#9786)
        val markAll = markAllSeenBatch(space.members)
        // A whole project on a to-do (#1616, iOS SpaceTodoMenu): only a herdr space the hub can name, on a hub that knows projects.
        val todoSpace = session.todoSpaceName?.takeIf { model.tasks?.spacesSupported == true }
        val linked = todoSpace?.let { todos.forSpace(session.machineId, it) }
        // An Orca space is its worktree: New worktree… from it, as the web's space menu (#17054).
        val worktreeAction = worktrees?.let { w -> w.targetOf(space.members.map { it.session })?.let { w.newWorktreeAction(it) } }
        AgentSpaceMenu(session, space.preferenceKey in page.preferences.pins.spaces, space.preferenceKey in page.preferences.mutes.spaces,
            operations.busy, onPin = { model.spacePin(session) }, onMute = { model.spaceMute(session) },
            onRename = { operationDialog = AgentOperationDialog.RenameSpace(session) },
            onMarkRead = markAll.takeIf { it.unread.isNotEmpty() }?.let { batch -> { confirmRead = batch } },
            onOpen = if (inList) ({ navigator.navigate(SpaceRoute(space.id, space.name)) }) else null, compact = inList,
            todo = todoSpace?.let { name ->
                SpaceTodoActions(linked != null, onLink = { pickingTodoForSpace = session.machineId to name },
                    onUnlink = linked?.let { todo -> { model.unlinkSpace(todo.id, session.machineId, name) } })
            }, extra = listOfNotNull(worktreeAction))
    }
    // A repository header's long press on Glass (iOS `repoMenu`): the actions of the one space its agents are in and its
    // to-do; agents in several spaces, Mark all as read.
    val repoActions: @Composable (AgentListGroup) -> List<JAction> = { repo ->
        val space = page.spacesById[repo.id]
        val actions = if (space != null) {
            val session = space.members.first().session
            val markAll = markAllSeenBatch(space.members)
            val todoSpace = session.todoSpaceName?.takeIf { model.tasks?.spacesSupported == true }
            val linked = todoSpace?.let { todos.forSpace(session.machineId, it) }
            agentSpaceActions(session, space.preferenceKey in page.preferences.pins.spaces, space.preferenceKey in page.preferences.mutes.spaces,
                operations.busy, onPin = { model.spacePin(session) }, onMute = { model.spaceMute(session) },
                onRename = { operationDialog = AgentOperationDialog.RenameSpace(session) },
                onMarkRead = markAll.takeIf { it.unread.isNotEmpty() }?.let { batch -> { confirmRead = batch } },
                onOpen = { navigator.navigate(SpaceRoute(space.id, space.name)) },
                todo = todoSpace?.let { name ->
                    SpaceTodoActions(linked != null, onLink = { pickingTodoForSpace = session.machineId to name },
                        onUnlink = linked?.let { todo -> { model.unlinkSpace(todo.id, session.machineId, name) } })
                }) + listOfNotNull(linked?.let { todo ->
                    JAction(stringResource(R.string.agents_flag_space_todo, todo.title), PersonaIcons.todo,
                        onClick = { navigator.navigate(persona.core.navigation.TodoDetailRoute(todo.id)) })
                })
        } else {
            val markAll = markAllSeenBatch(repo.rows + page.list.groups.filter { it.under == repo.id }.flatMap { it.rows })
            listOfNotNull(JAction(stringResource(R.string.agents_mark_all_read), PhIcons.Check, onClick = { confirmRead = markAll })
                .takeIf { markAll.unread.isNotEmpty() })
        }
        // A repository drawn as one Orca worktree carries that worktree's actions (#17054).
        val worktree = worktrees?.let { w -> w.targetOf(repo.rows.map { it.session })?.let { w.menuActions(it, section = true) } }
        actions + listOfNotNull(workspaceCopyPathAction(repo.workspace)) + worktree.orEmpty()
    }
    // Why that worktree's actions are off, under the repository's actions.
    val repoNote: @Composable (AgentListGroup) -> String? = { repo ->
        worktrees?.let { w -> w.targetOf(repo.rows.map { it.session })?.let { w.note(it) } }
    }
    // "By machine" shows a herdr tab's agents as a group (#2320), as the web's sidebar; the other groupings list one agent a row
    val tabs = page.preferences.grouping == AgentGrouping.MachineSpace
    var expandedTasks by remember { mutableStateOf(emptySet<String>()) }
    // By Repository on Glass draws iOS's tree (#4338 A7, A8): each repository a card, its header and working trees' lines in
    // it, the agents' rows under their line and the idle ones behind one quiet line; each computer's idle repositories a row
    // each in its Idle Repositories. Brief and Board keep their headings and "N more idle" (decision 8).
    val repoTree = PersonaTheme.style == AppStyle.Glass && page.preferences.grouping == AgentGrouping.Repo
    // By project on Glass, and By Machine always: fold opened idle agents again with "Hide N Idle" (iOS `collapseRow` /
    // `machineSections`, #4338 A8 / #9319). Brief and Board keep one-way "N more idle" under By project (decision 8).
    val hideIdle = page.preferences.grouping == AgentGrouping.Machine ||
        (PersonaTheme.style == AppStyle.Glass && page.preferences.grouping == AgentGrouping.MachineSpace)
    val tree = remember(page.list, page.settings.machineFilter, tabs, page.preferences.foldedTabs, page.preferences.mutes, expandedTasks, repoTree, hideIdle) {
        agentTreeEntries(page.list, page.settings.machineFilter, tabs, page.preferences.foldedTabs, page.preferences.mutes, expandedTasks,
            showAll = page.settings.expandedAll.takeIf {
                page.settings.query.isEmpty() && page.settings.quick == AgentQuickFilter.All && !page.pagination.paged
            }, idleRepos = repoTree, hideIdle = hideIdle)
    }
    // The motion follows this screen's model. A second model (the shot loop, or a new key) must not
    // leave the flag on the previous one: that list then folds with nothing armed.
    DisposableEffect(model, listTapMotion) {
        model.attachListTap(listTapMotion)
        onDispose { model.detachListTap(listTapMotion) }
    }
    // A page built before the tap carries an older generation and is ignored (the list is throttled).
    LaunchedEffect(page) { listTapMotion.land(page.tapEpoch) }
    BindListTapMotion(listTapMotion)
    // The computer chip only (ChatsView.swift:1646). The menu's machine picker sets the filter with no animation (AgentsMenus.swift:141).
    fun clearMachine() {
        model.machine("", animate = true)
        onMachineSelected("")
    }
    val paginationFooter = page.pagination.paged && !firstPageIncomplete
    val controls = page.settings
    // Use the effective sort and the user's fold choices, which are kept separately from the live list projection.
    // Display includes the Compact/Detailed preference in every style.
    val restoring = RestoreAgentReadingPosition(
        listState, tree, paginationFooter, page.loaded,
        onProgrammaticScroll = listTapMotion::onProgrammaticScroll,
    )
    KeepAgentReadingPosition(listState, tree, paginationFooter,
        scope = listOf(group?.namespace, assistant.id, assistant.group, controls.grouping, controls.sort,
            controls.quick, controls.machineFilter, controls.query, page.preferences.hiddenBotmuxHubs,
            controls.folded, controls.expandedIdle, controls.expandedQuiet, controls.expandedAll, page.preferences.foldedTabs, expandedTasks,
            page.preferences.range, page.preferences.display, PersonaTheme.style),
        restoring = restoring,
        onProgrammaticScroll = listTapMotion::cancelShiftsAndResetBaseline)
    // Rows outside the tree name their computer while the list mixes computers, By To-do's their host while it mixes
    // hosts (iOS `rowMachine`, `mixedHosts`; #4338 A9). By Machine's sections are the computers, so rows omit the name
    // (iOS `rowMachine` skips `.machine`).
    val mixesComputers = remember(page.list, page.settings.machineFilter, page.preferences.grouping) {
        page.preferences.grouping != AgentGrouping.Machine && page.list.mixesComputers(page.settings.machineFilter)
    }
    val showHost = remember(page.list, page.preferences.grouping) { page.preferences.grouping == AgentGrouping.Todo && page.list.mixesHosts() }
    val spacesById = page.spacesById
    val detail = LocalDetailKey.current
    val selectedEntry = if (sidebar == null || detail == null) null else remember(tree, detail) {
        tree.firstOrNull { entry -> when (entry) {
            is TreeRowEntry -> isSessionSelected(detail, entry.row.session.key)
            is TreeTabEntry -> entry.folded && entry.tab.members.any { isSessionSelected(detail, it.session.key) }
            else -> false
        } }?.key
    }
    val repoSlots = remember(tree, repoTree) { if (repoTree) repoCardSlots(tree) else emptyMap() }

    val working = page.list.counts[AgentQuickFilter.Working] ?: 0
    val unread = page.list.counts[AgentQuickFilter.Unread] ?: 0
    val workingText = pluralStringResource(R.plurals.agents_summary_working, working, working)
    val summary = when {
        needsYou.isNotEmpty() -> pluralStringResource(R.plurals.agents_summary_requests, needsYou.size, needsYou.size)
        working > 0 -> workingText
        else -> stringResource(R.string.agents_summary_quiet)
    }
    val parts = buildList {
        if (needsYou.isNotEmpty() && working > 0) add(workingText)
        if (unread > 0) add(pluralStringResource(R.plurals.agents_subtitle_unread, unread, unread))
    }
    val subtitle = when (parts.size) {
        0 -> null
        1 -> stringResource(R.string.agents_subtitle_one, parts[0])
        else -> stringResource(R.string.agents_subtitle_two, parts[0], parts[1])
    }
    val searchPull = remember(listState, homeChrome) {
        object : androidx.compose.ui.input.nestedscroll.NestedScrollConnection {
            override fun onPreScroll(available: androidx.compose.ui.geometry.Offset,
                source: androidx.compose.ui.input.nestedscroll.NestedScrollSource): androidx.compose.ui.geometry.Offset {
                if (homeChrome && available.y > 0 && !listState.canScrollBackward) searchVisible = true
                return androidx.compose.ui.geometry.Offset.Zero
            }
        }
    }
    val listContent: LazyListScope.() -> Unit = {
            val tapScope = ListTapLazyScope(this, listTapMotion, listState, page.tapEpoch)
            with(tapScope) {
            if (sidebar == null) item("search") {
                val settings by model.listSettings.collectAsStateWithLifecycle()
                androidx.compose.animation.AnimatedVisibility(!homeChrome || searchVisible || settings.query.isNotBlank()) {
                    JSearchField(settings.query, model::query, stringResource(R.string.agents_search_placeholder), Modifier.padding(bottom = Tokens.Space.xs))
                }
            } else {
                sidebar.prefix(this)
                item(AgentsHeadingKey) {
                    JSidebarSectionHeading(stringResource(R.string.agents_sidebar_heading), actions = {
                        JIconButton(PersonaIcons.moreMenu, stringResource(R.string.agents_action_list_settings), { menu = true })
                        if (creationModel != null) {
                            JIconButton(PhIcons.Plus, stringResource(R.string.agents_action_new_session), { newSession() })
                        }
                    })
                }
            }
            if (groups.size > 1) item("groups") {
                GroupFilterRow(groups, page.groupCounts, group, model::group)
            }
            if (!homeChrome) item("machines") {
                // With more than one group a computer's chip says its group too (two groups can both have a MacBook).
                val chips = if (groups.size > 1) page.machineCounts.map { it.copy(machine = it.machine.copy(name = names[it.machine.id] ?: it.machine.displayName)) }
                    else page.machineCounts
                MachineFilterRow(chips, page.machineTotal, page.settings.machineFilter, { id -> model.machine(id); onMachineSelected(id) })
            }
            item("quick-filters") {
                // The Lark filter disappears when the visible list has no Lark sessions.
                val shown = AgentQuickFilter.shown(page.settings.quick, quickCounts)
                val bays = shown.map { filter ->
                    Bay(filter.label, if (filter == AgentQuickFilter.All) page.allCount else quickCounts[filter] ?: 0, filter.state?.designStatus,
                        icon = PersonaIcons.backgroundWaiting.takeIf { filter == AgentQuickFilter.BackgroundWaiting },
                        waiting = filter == AgentQuickFilter.BackgroundWaiting)
                }
                if (homeChrome) JAgentFilterCapsule(bays, shown.indexOf(page.settings.quick), { model.filter(shown[it]) },
                    groupLabel = "${stringResource(R.string.agents_group_by)}: ${page.preferences.grouping.label}",
                    groupActions = groupings.map { grouping ->
                        JAction(grouping.label, checked = page.preferences.grouping == grouping, onClick = { model.grouping(grouping) })
                    }, modifier = Modifier.padding(vertical = Tokens.Space.s))
                else HomeAgentFilters(bays, shown.indexOf(page.settings.quick), { model.filter(shown[it]) }, Modifier.padding(vertical = Tokens.Space.s))
            }
            // The computer the list is narrowed to (iOS's "Only <computer>", #1659): with the shared home chrome the computer
            // is chosen in a sheet and its chips don't show, so nothing else on the page says so. A tap shows every computer.
            if (homeChrome && page.settings.machineFilter.isNotEmpty()) item("machine-filter") {
                val name = names[page.settings.machineFilter] ?: page.settings.machineFilter
                val hint = stringResource(R.string.agents_only_machine_hint)
                Row(Modifier.padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.xs)) {
                    JChip(stringResource(R.string.agents_only_machine, name), Modifier.testTag("agents-machine-filter")
                        .semantics { onClick(label = hint) { clearMachine(); true } },
                        selected = true, onClick = { clearMachine() },
                        role = Role.Button, trailingIcon = persona.core.designsystem.icons.PersonaIcons.clear)
                }
            }
            val currentNotice = hubNotice
            if (currentNotice != null && page.settings.query.isEmpty() && page.preferences.range != AgentRange.History) {
                item("hub-notice") {
                    HubNoticeRow(
                        notice = currentNotice,
                        onClick = { navigator.navigate(SettingsPageRoute(SettingsPage.HubUpdate)) },
                        onAutoOff = { model.setAutoUpdate(false) },
                        onNoticeSeen = model::noticeSeen,
                        onUpdate = model::startHubUpdate,
                        onDismiss = model::dismissHubNotice,
                        modifier = Modifier.padding(bottom = Tokens.Space.xs),
                    )
                }
            }
            // The star card waits while a session needs the user or a card waits, so it never pushes a waiting one down (#5761).
            if (starPrompt != null && starState != null && page.loaded && waitingRows.isEmpty() && pending.isEmpty() && page.settings.query.isEmpty() &&
                page.preferences.range != AgentRange.History && showsStarCard(starState, now.toEpochMilli())) {
                item("star-card") {
                    StarPromptCard(onStar = { starPrompt.star(context) }, onLater = starPrompt::later,
                        modifier = Modifier.padding(bottom = Tokens.Space.xs))
                }
            }
            if (connection != ConnectionState.Online) item("connection") { ConnectionNotice(connection, ::connectionSettings) }
            if (model.herdr != null && page.preferences.range != AgentRange.History) {
                items(page.machines.filter { it.herdrStopped(herdrStates[it.id]) && groups.stateFor(it.id, connection) == ConnectionState.Online &&
                    (page.settings.machineFilter.isEmpty() || page.settings.machineFilter == it.id) },
                    key = { "herdr:${it.id}" }) { machine ->
                    HerdrNotice(names[machine.id] ?: machine.displayName, machine.id in startingHerdr, { model.herdr.start(machine.id) },
                        failure = herdrFailures[machine.id])
                }
            }
            if (page.preferences.range != AgentRange.History) {
                items(page.machines.filter { Engines.orcaStopped(it) && groups.stateFor(it.id, connection) == ConnectionState.Online &&
                    (page.settings.machineFilter.isEmpty() || page.settings.machineFilter == it.id) },
                    key = { "orca:${it.id}" }) { machine -> OrcaNotice(names[machine.id] ?: machine.displayName) }
            }
            if (page.preferences.range != AgentRange.History && needsYou.isNotEmpty()) {
                item(NeedsYouListKey) {
                    JSectionHeader(
                        stringResource(persona.core.designsystem.R.string.designsystem_core_state_needs_you),
                        count = needsYou.size,
                        countTone = Status.NeedsYou.takeIf { isGlass },
                    )
                }
                if (isGlass) {
                    val glassItems = flattenGlassNeedsYou(
                        rows = needsYou,
                        ownerOf = { row -> sessionsByKey[row.key] ?: everySession.firstOrNull { it.key == row.key }.takeIf { sidebar != null } },
                        machineOf = { id -> names[id] },
                        includeOwnerlessWaiting = sidebar != null,
                    )
                    itemsIndexed(glassItems, key = { _, entry -> entry.itemKey }) { index, entry ->
                        JGroupItem(GroupPosition.of(index, glassItems.size), grouped = true) {
                            when (entry) {
                                is GlassNeedsYouItem.Waiting -> GlassNeedsYouWaitingRow(
                                    owner = entry.owner,
                                    machine = entry.machine,
                                    sidebar = sidebar != null,
                                    preferHerdrTabName = entry.owner?.let(page.preferences::prefersHerdrTabName) == true,
                                    onClick = {
                                        if (entry.owner != null) openRow(entry.owner.listSession(page.preferences)) else open(entry.row.key)
                                    },
                                )
                                is GlassNeedsYouItem.Card -> GlassNeedsYouCardRow(
                                    request = entry.request,
                                    machine = entry.machine,
                                    now = now,
                                    online = reachable(entry.request),
                                    delivery = delivery[entry.request.id],
                                    showsPlace = entry.showsPlace,
                                    onOpen = { requestDetail = entry.request },
                                    onAnswer = model::answer,
                                    approvable = entry.owner?.canAnswerInteraction(entry.request) ?: true,
                                )
                            }
                        }
                    }
                } else {
                    for (row in needsYou) {
                        val owner = sessionsByKey[row.key] ?: everySession.firstOrNull { it.key == row.key }.takeIf { sidebar != null }
                        if (row.cards.isEmpty()) {
                            // Waiting in its terminal with no card read: a row of its own that opens the session, as iOS's
                            // Terminal Needs You row (its session row stays in the list below).
                            if (owner != null || sidebar != null) item("needs-you:${row.id}") {
                                JRow(stringResource(persona.core.designsystem.R.string.designsystem_decision_screen),
                                    subtitle = owner?.let {
                                        val preferTab = page.preferences.prefersHerdrTabName(it)
                                        if (sidebar == null) it.displayTabName(preferTab) ?: it.title.takeIf(String::isNotBlank) ?: it.sessionId
                                        else it.displayTitle(preferTab)
                                    },
                                    leadingIcon = persona.core.designsystem.icons.PersonaIcons.terminal,
                                    value = names[row.key.machineId], onClick = {
                                        if (owner != null) openRow(owner.listSession(page.preferences)) else open(row.key)
                                    })
                            }
                        } else items(row.cards, key = { "request:${it.id}" }) { request ->
                            RequestCard(request, names[request.machineId], metadata, now, reachable(request),
                                delivery[request.id], { requestDetail = request }, model::answer,
                                space = owner?.spaceName?.takeIf { it.isNotBlank() }, approvable = owner?.canAnswerInteraction(request) ?: true)
                        }
                    }
                }
            }
            if (!requestsInMenu && page.preferences.range != AgentRange.History) item("request-history") {
                JRow(stringResource(R.string.agents_requests_history), onClick = { allRequests = true })
            }
            if (page.settings.quick == AgentQuickFilter.Unread && page.list.unreadShown.isNotEmpty()) item("mark-read") {
                // The same set iOS counts and marks: the list's unread ids, whichever groups (or none) draw them.
                val markAll = remember(page.list, page.sessions) {
                    val rows = page.list.groups.flatMap { it.rows }.associateBy { it.id }
                    val sessions = page.sessions.associateBy { it.key.path }
                    val unread = page.list.unreadShown.distinct().mapNotNull { id -> rows[id] ?: sessions[id]?.let(::AgentSession) }
                    markAllSeenBatch(page.list.groups.flatMap { it.rows } + unread)
                }
                JRow(stringResource(R.string.agents_mark_all_read), leadingIcon = PhIcons.Check,
                    value = pluralStringResource(R.plurals.agents_mark_all_read_count, markAll.unread.size, markAll.unread.size),
                    chevron = false, onClick = { confirmRead = markAll })
            }
            if (firstPageIncomplete) item("initial-state") {
                if (page.pagination.failed) JNotice(
                    text = stringResource(persona.core.designsystem.R.string.designsystem_load_failed),
                    modifier = Modifier.padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.xs),
                    actionLabel = stringResource(persona.core.designsystem.R.string.designsystem_retry),
                    onAction = model::refresh,
                ) else AgentsListPlaceholder()
            }
            // No session yet with a computer to start one on: the empty state's New session (and the star pill under it).
            val firstRun = page.allCount == 0 && page.settings.query.isEmpty() &&
                !(page.machines.isEmpty() && page.settings.machineFilter.isEmpty())
            if (page.loaded && page.list.groups.isEmpty()) item("empty") {
                // Three cases, as iOS: a search with no result (no New session, which read as if the agents were gone), no
                // sessions at all, and a quick filter with nothing left (Show all).
                when {
                    // An offline assistant with nothing the hub still keeps of it (no agent, no computer): out of reach, not a first run;
                    // with a computer online it can start a session now.
                    assistant.offline != null && page.allCount == 0 && page.settings.query.isEmpty() &&
                        page.machines.isEmpty() && page.settings.machineFilter.isEmpty() -> JAssistantOffline(assistant)
                    page.allCount == 0 && page.settings.query.isNotEmpty() -> JEmptyState(
                        stringResource(R.string.agents_empty_search_title, page.settings.query),
                        stringResource(R.string.agents_empty_search_hint), icon = PhIcons.MagnifyingGlass)
                    // No computer at all: no session can start anywhere, so the step is adding one (docs/assistant/onboarding.md 4.5).
                    page.allCount == 0 && page.machines.isEmpty() && page.settings.machineFilter.isEmpty() -> JEmptyState(
                        stringResource(R.string.agents_empty_no_computer_title), stringResource(R.string.agents_empty_no_computer_hint),
                        icon = PhIcons.Desktop,
                        action = { JButton(stringResource(ReadinessActionId.AddComputer.label), { navigator.navigate(persona.core.navigation.AddComputerRoute()) }) })
                    firstRun -> JEmptyState(
                        stringResource(if (page.settings.machineFilter.isEmpty()) R.string.agents_empty_first_title else R.string.agents_empty_machine_title),
                        stringResource(R.string.agents_empty_first_hint), icon = PhIcons.Robot,
                        action = { JButton(stringResource(R.string.agents_action_new_session), { newSession() }) })
                    else -> JEmptyState(
                        stringResource(R.string.agents_empty_filter_title, page.settings.quick.label),
                        stringResource(if (page.settings.machineFilter.isEmpty()) R.string.agents_empty_filter_hint else R.string.agents_empty_filter_machine_hint),
                        icon = PhIcons.CheckCircle,
                        action = { JButton(stringResource(R.string.agents_empty_show_all), { model.filter(AgentQuickFilter.All) }, kind = JButtonKind.Secondary) })
                }
            }
            if (starPrompt != null && starState != null && page.loaded && page.list.groups.isEmpty() && firstRun && showsStarPill(starState)) {
                item("star-pill") { StarPill(onStar = { starPrompt.star(context) }, onHide = starPrompt::hidePill) }
            }
            if (page.pagination.hasPrevious) item("sessions-previous", contentType = "pagination") {
                JTextButton(stringResource(R.string.agents_page_previous), model::loadPrevious)
            }
            tree.forEach { entry ->
                when (entry) {
                    is TreeMachineEntry -> item(entry.key, contentType = "machine") {
                        val machine = entry.machine
                        LaunchedEffect(entry.key, page.pagination.counts.revision, page.settings.query, page.settings.quick) {
                            model.countMachine(machine)
                        }
                        // A computer only the sessions name (no machine snapshot yet) says so (AgentsViewModel.page).
                        val machineTitle = if (page.machines.any { it.id == machine.machine.id }) machine.machine.displayName
                            else stringResource(R.string.agents_machine_pending)
                        AgentTreeHeader(machineTitle, machine.folded, { model.fold(machine) }, first = entry.first,
                            icon = machine.machine.deviceIcon, online = machine.machine.online,
                            detail = stringResource(R.string.agents_count_pair,
                                machine.repoCount?.let { pluralStringResource(R.plurals.agents_count_repos, it, it) }
                                    ?: pluralStringResource(R.plurals.agents_count_spaces, machine.spaceCount, machine.spaceCount),
                                pluralStringResource(R.plurals.agents_count_sessions, machine.sessionCount, machine.sessionCount)),
                            counts = machine.headingCounts, waiting = machine.waitingInBackground,
                            onMonitor = { navigator.navigate(MachineDetailRoute(machine.machine.id)) }, tag = groups.tagFor(machine.machine.id))
                    }
                    is TreeGroupEntry -> item(entry.key, contentType = if (entry.space) "space" else "section") {
                        val group = entry.group
                        LaunchedEffect(entry.key, page.pagination.counts.revision, page.settings.query, page.settings.quick, page.settings.grouping) {
                            model.countGroup(group)
                        }
                        val space = spacesById[group.id]
                        val project = group.projectFolder.takeIf { page.pagination.loaded && !page.pagination.paged }
                        // A history group of a session without a directory has no title of its own (AgentHistory.kt).
                        val groupTitle = group.title.text().ifBlank { stringResource(R.string.agents_history_unknown_folder) }
                        val slot = repoSlots[entry.key]
                        if (slot != null && group.isIdleRepository) {
                            // A row of its computer's Idle Repositories (iOS `QuietSpaceRow`, #4338 A8); the long press keeps
                            // what its card's header offered.
                            val actions = repoActions(group)
                            var sheet by remember { mutableStateOf(false) }
                            val line = idleRepoLine(group)
                            AgentIdleRepoRow(groupTitle, line.agents, line.tabs, relativeTime(agentResources(), line.updatedAt, now),
                                group.folded, { model.fold(group) }, Modifier.repoCard(slot.first, slot.last, spaced = false),
                                onLongClick = if (actions.isEmpty()) null else ({ sheet = true }),
                                actionsLabel = stringResource(R.string.agents_repo_actions))
                            if (sheet) JActionSheet(actions, { sheet = false }, title = groupTitle, note = repoNote(group))
                        } else if (slot != null && group.repo == RepoLevel.Repository) {
                            val actions = repoActions(group)
                            var sheet by remember { mutableStateOf(false) }
                            AgentRepoHeader(groupTitle, group.folded, { model.fold(group) }, Modifier.repoCard(slot.first, slot.last),
                                onLongClick = if (actions.isEmpty()) null else ({ sheet = true }),
                                actionsLabel = stringResource(R.string.agents_repo_actions),
                                main = group.worktree?.main == true,
                                offline = group.worktree?.offline == true,
                                pinned = group.worktree?.pinned == true)
                            if (sheet) JActionSheet(actions, { sheet = false }, title = groupTitle, note = repoNote(group))
                        } else if (slot != null && group.repo != null) {
                            val line = group.worktree
                            // An Orca worktree's long press is its actions (#17054, the web's worktree menu).
                            val worktree = worktrees?.let { w -> w.targetOf(group.rows.map { it.session })?.let { w to it } }
                            var sheet by remember { mutableStateOf(false) }
                            if (sheet && worktree != null) JActionSheet(worktree.first.menuActions(worktree.second), { sheet = false },
                                title = groupTitle, note = worktree.first.note(worktree.second))
                            AgentWorktreeLine(groupTitle, main = group.repo == RepoLevel.MainCheckout, folded = group.folded,
                                onToggle = { model.fold(group) }, modifier = Modifier.repoCard(slot.first, slot.last), level = slot.level,
                                branch = line?.branch, detached = line?.detached == true, tally = line?.tally?.let { tally ->
                                    when (tally) {
                                        WorktreeTally.Idle -> stringResource(persona.core.designsystem.R.string.designsystem_worktree_idle)
                                        is WorktreeTally.Agents -> pluralStringResource(persona.core.designsystem.R.plurals.designsystem_tab_agents, tally.count, tally.count)
                                    }
                                }, quiet = entry.quiet, offline = line?.offline == true, pinned = line?.pinned == true,
                                onLongClick = worktree?.let { { sheet = true } }, actionsLabel = stringResource(R.string.agents_worktree_actions))
                        } else if (entry.space) AgentSpaceHeader(groupTitle, group.folded, { model.fold(group) }, depth = entry.depth, first = entry.first,
                            quiet = entry.quiet, sessions = group.sectionCount, counts = group.headingCounts,
                            waiting = group.waitingInBackground, flags = buildList {
                                if (group.worktree?.pinned == true) add(RowFlag(stringResource(R.string.agents_row_pinned), PersonaIcons.pinned))
                                if (group.repo == RepoLevel.Repository && group.worktree?.main == true) {
                                    add(RowFlag(stringResource(persona.core.designsystem.R.string.designsystem_worktree_primary), PhIcons.House))
                                }
                                if (group.repo == RepoLevel.Repository && group.worktree?.offline == true) {
                                    add(RowFlag(stringResource(persona.core.designsystem.R.string.designsystem_worktree_offline), PersonaIcons.offline))
                                }
                                if (space != null && space.preferenceKey in page.preferences.pins.spaces) add(RowFlag(stringResource(R.string.agents_flag_space_pinned), PhIcons.PushPin))
                                if (space != null && space.preferenceKey in page.preferences.mutes.spaces) add(RowFlag(stringResource(R.string.agents_flag_space_muted), PhIcons.Prohibit))
                                // iOS TodoSpaceTag: the project is on a to-do.
                                space?.members?.first()?.session?.let { s -> s.todoSpaceName?.let { name -> todos.forSpace(s.machineId, name) } }
                                    ?.let { todo -> add(RowFlag(stringResource(R.string.agents_flag_space_todo, todo.title), PhIcons.ListChecks)) }
                            }, menu = {
                                if (project != null && creationModel != null && connection == ConnectionState.Online &&
                                    page.machines.any { it.id == project.machineId && it.online }) {
                                    val projectLabel = listOfNotNull(groupTitle, project.qualifier).joinToString(" · ")
                                    HeadingAction(PersonaIcons.add, stringResource(R.string.agents_new_agent_in_project, projectLabel),
                                        onClick = { newSession(project = project) })
                                }
                                if (group.workspace != null) {
                                    val actions = repoActions(group)
                                    var sheet by remember { mutableStateOf(false) }
                                    if (actions.isNotEmpty()) {
                                        HeadingAction(PersonaIcons.moreMenu, stringResource(R.string.agents_repo_actions), { sheet = true })
                                        if (sheet) JActionSheet(actions, { sheet = false }, title = groupTitle)
                                    }
                                }
                                if (group.repo != null && worktrees != null) worktrees.targetOf(group.rows.map { it.session })?.let { worktree ->
                                    // By repository in Brief and Board: an Orca worktree's heading has its actions (#17054).
                                    var sheet by remember { mutableStateOf(false) }
                                    HeadingAction(PersonaIcons.moreMenu, stringResource(R.string.agents_worktree_actions), { sheet = true })
                                    if (sheet) JActionSheet(worktrees.menuActions(worktree), { sheet = false }, title = groupTitle,
                                        note = worktrees.note(worktree))
                                }
                                if (space != null) spaceMenu(space, true)
                            }, icon = group.repo?.icon, kind = group.repo?.kind ?: stringResource(R.string.agents_kind_space),
                            qualifier = project?.qualifier)
                        else if (group.id.startsWith("todo/") && group.id != TodoNoneGroup && group.id != TodoManualGroup) {
                            // "By to-do" as iOS (#4338 A5): the to-do's title, its agents' count in the to-do's state, and
                            // the arrow to the to-do.
                            val todoId = group.id.removePrefix("todo/")
                            val todoState = todos.firstOrNull { it.id == todoId }?.state { key -> sessionsByKey[key] }
                            AgentTreeHeader(groupTitle, group.folded, { model.fold(group) }, first = entry.first, icon = PersonaIcons.todo,
                                counts = listOf(todoState.countTone to group.rows.count(AgentSession::countsInHeading)),
                                onAction = { navigator.navigate(persona.core.navigation.TodoDetailRoute(todoId)) },
                                actionLabel = stringResource(R.string.agents_view_todo))
                        } else if (group.id.startsWith(DirectionTaskPrefix)) {
                            // "By direction": a to-do of the hub's tree under its direction (#4352); the arrow opens it.
                            val todoId = group.id.removePrefix(DirectionTaskPrefix).substringAfter('/')
                            AgentSpaceHeader(groupTitle, group.folded, { model.fold(group) }, depth = entry.depth, first = entry.first,
                                counts = group.headingCounts, icon = PersonaIcons.todo, kind = stringResource(R.string.agents_kind_todo),
                                menu = { HeadingAction(PersonaIcons.forward, stringResource(R.string.agents_view_todo),
                                    { navigator.navigate(persona.core.navigation.TodoDetailRoute(todoId)) }) })
                        } else if (group.id.startsWith(DirectionGroupPrefix)) {
                            val todoCount = page.list.groups.count { it.under == group.id }
                            AgentTreeHeader(groupTitle, group.folded, { model.fold(group) }, first = entry.first,
                                icon = PersonaIcons.pinned, counts = group.headingCounts,
                                detail = pluralStringResource(R.plurals.agents_count_todos, todoCount, todoCount))
                        } else if (group.under?.startsWith("app/") == true && "/project/" in group.id) {
                            // By App's project fold row (#9199, iOS `AppProjectRow`): under the app heading, not a section of its own.
                            val siblingMachines = page.list.groups.asSequence()
                                .filter { it.under == group.under }
                                .mapNotNull { it.rows.firstOrNull()?.session?.machineId }
                                .toSet()
                            val machineName = if (siblingMachines.size > 1) {
                                names[group.rows.firstOrNull()?.session?.machineId]
                            } else null
                            AgentAppProjectRow(
                                title = groupTitle,
                                count = group.sectionCount,
                                folded = group.folded,
                                onToggle = { model.fold(group) },
                                other = group.id.endsWith("/project/other#"),
                                machine = machineName,
                                // iOS `compact ? CompactList…`; Board uses the denser type too.
                                compact = page.preferences.compact || PersonaTheme.style == AppStyle.Board,
                            )
                        } else if (group.id.startsWith("machine/")) {
                            // By Machine (#9319, iOS `MachineSectionHeader`): the computer's mark, online tint, state counts
                            // from the rows (domain leaves stateCounts null for the shared fixtures).
                            val mid = group.id.removePrefix("machine/")
                            val host = page.machines.firstOrNull { it.id == mid }
                            AgentTreeHeader(groupTitle, group.folded, { model.fold(group) }, first = entry.first,
                                icon = host?.deviceIcon ?: PersonaIcons.machine, online = host?.online,
                                counts = machineHeadingCounts(group), waiting = group.waitingInBackground)
                        } else {
                            val stateCounts = group.stateSectionCounts
                            val seen = if (stateCounts != null) group.heldSeen else emptyList()
                            AgentTreeHeader(groupTitle, group.folded, { model.fold(group) }, first = entry.first,
                                icon = PhIcons.PushPin.takeIf { group.id == "pinned" }
                                    ?: PersonaIcons.backgroundWaiting.takeIf { group.id == "state/background" }
                                    ?: PersonaIcons.recent.takeIf { group.id == RecentGroup }
                                    ?: PersonaIcons.byApp.takeIf { group.id.startsWith("app/") },
                                counts = stateCounts ?: group.todoLooseCounts ?: group.sectionCounts,
                                waitsInBackground = group.id == "state/background",
                                // Pinned counts agents that only wait in the background separately.
                                waiting = if (stateCounts == null && group.todoLooseCounts == null) group.waitingInBackground else 0,
                                onAction = if (seen.isEmpty()) null else ({ model.clearSeen(seen.toSet()) }),
                                actionText = if (seen.isEmpty()) null else {
                                    pluralStringResource(R.plurals.agents_clear_seen, seen.size, seen.size)
                                },
                            )
                        }
                    }
                    is TreeRowEntry -> item(entry.key, contentType = "session") {
                        val row = entry.row
                        val info = metadata.rows[row.session.key] ?: AgentRowMetadata()
                        val operationUncertain = operations.uncertain.any {
                            it.endsWith(":${row.id}") || it == "space:${row.session.machineId}/${row.session.operationWorkspaceId}"
                        }
                        val infoWithDelivery = info.withDelivery(messagesBySession[row.session.key].orEmpty(), row.session.key in unknownRequests)
                            .let { if (operationUncertain) it.copy(needsConfirmation = true) else it }
                        val slot = repoSlots[entry.key]
                        val sideUnread = unreadSideSummary(row.session, visibleSessionsByKey[row.session.key], visibleSessions)
                        // In a repository's card the rows sit under their line at its level, without rules between them (iOS).
                        Box(if (slot != null) Modifier.repoCard(slot.first, slot.last) else Modifier) {
                            CompositionLocalProvider(LocalDetailKey provides detail.takeIf { sidebar == null || entry.key == selectedEntry }) {
                            SlotRow(row, names[row.session.machineId], infoWithDelivery, now, page.preferences.compact,
                                row.id in page.preferences.pins.sessions, page.preferences.mutes.mutes(row),
                                depth = slot?.level ?: entry.depth, position = if (slot != null) GroupPosition(true, entry.last) else GroupPosition(entry.first, entry.last),
                                located = entry.located,
                                onOpen = { openRow(row) }, menuItems = { rowMenu(row, entry.nested) },
                                onClose = closeSwipe(row, operations.busy) { operationDialog = AgentOperationDialog.Close(row.session) },
                                launcher = entry.launcher, branch = entry.branch, hideState = entry.inStateSection,
                                requestCount = cardCounts[row.session.key] ?: 0, card = requestCards[row.session.key],
                                mixesComputers = mixesComputers, showHost = showHost, style = page.preferences.rowStyle,
                                preferHerdrTabName = page.preferences.prefersHerdrTabName(row.session),
                                heldChildren = heldChildrenByParent[row.session.key].orEmpty(),
                                onOpenSubagents = { openRowSubagents(row) },
                                sideUnread = sideUnread, onOpenSide = { side -> model.opened(row); open(side) })
                            }
                        }
                    }
                    is TreeChildrenEntry -> item(entry.key, contentType = "children") {
                        val slot = repoSlots[entry.key]
                        AgentSpaceHeader(stringResource(R.string.agents_child_tasks, entry.count), !entry.expanded,
                            onToggle = {
                                // Codex subtasks (ChatsView.swift:3289). Prepare exit fade one frame ahead without
                                // arming placement yet so a hub push in that prep frame cannot steal the fold's tap,
                                // while idle rows keep null specs and zero GraphicsLayers.
                                val parentId = entry.parent.id
                                val collapse = entry.expanded
                                if (reduceMotion) {
                                    expandedTasks = if (collapse) expandedTasks - parentId else expandedTasks + parentId
                                } else {
                                    listTapMotion.prepareExitFade()
                                    androidx.compose.runtime.snapshots.Snapshot.sendApplyNotifications()
                                    tapMotionScope.launch {
                                        androidx.compose.runtime.withFrameNanos { }
                                        listTapMotion.arm(ListTapMotionKind.State)
                                        expandedTasks = if (collapse) expandedTasks - parentId else expandedTasks + parentId
                                    }
                                }
                            },
                            modifier = if (slot != null) Modifier.repoCard(slot.first, slot.last) else Modifier,
                            depth = slot?.level ?: entry.depth, kind = stringResource(R.string.agents_child_tasks_kind))
                    }
                    is TreeTabEntry -> item(entry.key, contentType = "tab") {
                        CompositionLocalProvider(LocalDetailKey provides detail.takeIf { sidebar == null || entry.key == selectedEntry }) {
                            AgentTabEntryRow(entry, now, page.preferences.compact, onOpen = { openRow(entry.tab.lead) }, onToggle = { model.foldTab(entry.tab) },
                                card = requestCards[entry.tab.lead.session.key], muted = page.preferences.mutes.mutes(entry.tab.lead),
                                preferHerdrTabName = page.preferences.prefersHerdrTabName(entry.tab.lead.session))
                        }
                    }
                    is TreeIdleEntry -> item(entry.key, contentType = "idle") {
                        val slot = repoSlots[entry.key]
                        val fold = entry.fold
                        // By Repository on Glass: iOS's quiet line, which folds them again once open (#4338 A8).
                        if (fold != null && repoTree) AgentRepoIdleLine(fold.rows.size, fold.names, fold.open,
                            onClick = { if (fold.open) model.hideIdle(entry.group) else model.showIdle(entry.group) },
                            modifier = if (slot != null) Modifier.repoCard(slot.first, slot.last) else Modifier, level = slot?.level ?: entry.depth)
                        // By project on Glass: "Hide N Idle" after the opened agents; closed, "N more idle" with the first six's tabs (iOS `IdleFoldRow`).
                        else AgentIdleRow(entry.hidden, onClick = { if (fold != null) model.hideIdle(entry.group) else model.showIdle(entry.group) },
                            modifier = if (slot != null) Modifier.repoCard(slot.first, slot.last) else Modifier, depth = slot?.level ?: entry.depth,
                            open = fold != null, tabs = entry.tabs, compact = page.preferences.compact)
                    }
                    is TreeIdleReposEntry -> item(entry.key, contentType = "section") {
                        JSectionHeader(stringResource(R.string.agents_idle_repositories), count = entry.count, countTone = Status.Idle)
                    }
                    is TreeShowAllEntry -> item(entry.key, contentType = "idle") {
                        val slot = repoSlots[entry.key]
                        AgentShowAllRow(entry.count, entry.expanded, onClick = { model.showAll(entry.group, !entry.expanded) },
                            modifier = if (slot != null) Modifier.repoCard(slot.first, slot.last) else Modifier, depth = slot?.level ?: entry.depth)
                    }
                }
            }
            if (paginationFooter) item("sessions-pagination", contentType = "pagination") {
                Column(Modifier.fillMaxWidth().padding(vertical = Tokens.Space.m), horizontalAlignment = Alignment.CenterHorizontally) {
                    Text(pluralStringResource(R.plurals.agents_page_loaded, page.pagination.total, page.sessions.size, page.pagination.total),
                        style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
                    if (page.pagination.loading) JSpinner()
                    else if (page.pagination.hasMore) JTextButton(stringResource(R.string.agents_page_more), model::loadMore)
                }
            }
            tapScope.commit()
            }
    }
    // State Cards float on the page, each a card of its own: By Repository's cards give way to them, as iOS's do (#4338 A9).
    JAgentHomeSurface(page.preferences.grouping == AgentGrouping.Repo && page.preferences.display != AgentListDisplay.Cards) {
    Box(Modifier.fillMaxSize()) {
        if (sidebar != null) {
            Column(Modifier.fillMaxSize().imePadding()) {
                LazyColumn(
                    Modifier.weight(1f).fillMaxWidth().testTag("merged-sidebar-list").nestedScroll(listTapScroll(listTapMotion)),
                    state = listState,
                    contentPadding = PaddingValues(bottom = Tokens.Space.s),
                    content = listContent,
                )
                if (sidebar.waiting.isNotEmpty()) {
                    SidebarNeedsJump(listState, listTapMotion, sidebar.waiting.size) {
                        if (page.preferences.range == AgentRange.History) model.range(AgentRange.All)
                        model.query("")
                        model.filter(AgentQuickFilter.All)
                        if (actionableRows.size < sidebar.waiting.size) {
                            model.machine("")
                            model.group(null)
                        }
                    }
                }
                sidebar.footer()
            }
        } else JListPage(
            title = if (connection == ConnectionState.Online) names[page.settings.machineFilter] ?: "Agents" else connection.title,
            // The list holds text fields (search, a password card's password, #1581): it ends above the keyboard so the
            // focused one scrolls into view; the root leaves keyboard padding to each page (#867).
            modifier = Modifier.imePadding().then(Modifier.nestedScroll(searchPull)),
            state = listState,
            scrollEdgeStyle = ScrollEdgeStyle.Gradient,
            summary = summary, subtitle = subtitle, greet = true, largeTitle = LocalHomeChrome.current == null,
            titleBusy = connection in setOf(ConnectionState.Connecting, ConnectionState.Updating),
            onTitleClick = if (connection != ConnectionState.Online) connectionDetails else ({ menu = true }), refreshing = refreshing, onRefresh = model::refresh,
            listScroll = listTapScroll(listTapMotion),
            actions = {
                if (LocalHomeChrome.current == null) JIconButton(PhIcons.ListChecks, stringResource(R.string.agents_action_todos), { navigator.navigate(persona.core.navigation.TodosRoute) })
                JIconButton(if (homeChrome) PhIcons.NotePencil else PhIcons.Plus, stringResource(R.string.agents_action_new_session), { newSession() },
                    expandOnHomePress = optionsInSelectedTab)
                if (LocalHomeChrome.current == null) JIconButton(PhIcons.SlidersHorizontal, stringResource(R.string.agents_action_list_settings), { menu = true })
                // On the compact Glass home the selected tab opens list settings. While offline the tab explains the connection.
                else if (!optionsInSelectedTab) JIconButton(PersonaIcons.moreMenu, stringResource(R.string.agents_action_list_settings), { menu = true },
                    expandOnHomePress = false)
            },
            content = listContent,
        )
        Column(
            Modifier.align(Alignment.BottomCenter).navigationBarsPadding()
                .padding(horizontal = Tokens.Space.page, vertical = Tokens.Space.l),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        ) {
            if (takeoverClient != null) CodexTakeoverBusyPill(takeoverCoordinator, page.sessions)
            if (sidebar == null) AgentsFeedNote(feed, latestPage)
        }
        persona.core.designsystem.JToastHost(toast, Modifier.align(Alignment.TopCenter).statusBarsPadding().padding(top = persona.core.designsystem.ToastTopBarClearance))
    }
    }
    AgentsOverlaysHost(overlays, showArchivesState, searchVisibleState, newAgentState, historyMachineState,
        historySessionState, allRequestsState, requestDetailState, model, navigator, metadata, creationModel,
        onMachineSelected, groups, terminalPanes, sidebar, page, connection, assistant, archives, todos, everyRequest,
        everySession, everyMachine, requests, delivery, requestHistory, commandInputErrors, now, names, groupings,
        homeChrome, requestsInMenu, creation, takeoverClient, takeoverCoordinator, feedback, resources,
        open = { open(it) }, reachable = ::reachable)
    OrcaWorktreeDialogs(worktrees)
}


/** The list settings sheet (⋯): search, the computer, archives, range, grouping, sort, display, folding and the pages
 * behind it. Drawn by [AgentsOverlaysHost] while [menuState] is up. */
@Composable
internal fun AgentsListSettingsMenu(
    menuState: MutableState<Boolean>, showArchivesState: MutableState<Boolean>, searchVisibleState: MutableState<Boolean>,
    allRequestsState: MutableState<Boolean>, model: AgentsViewModel, navigator: Navigator,
    onMachineSelected: (String) -> Unit, terminalPanes: Boolean, sidebar: AgentsSidebarLayout?, page: AgentsPageState,
    archives: AgentArchivesState?, groupings: List<AgentGrouping>, homeChrome: Boolean, requestsInMenu: Boolean,
) {
    var menu by menuState
    var showArchives by showArchivesState
    var searchVisible by searchVisibleState
    var allRequests by allRequestsState
    val glassRows = PersonaTheme.style == AppStyle.Glass
    AgentsListSettingsSheet {
        JActionSheet(buildList {
        if (homeChrome) {
            add(JAction(stringResource(R.string.agents_action_search), PhIcons.MagnifyingGlass, onClick = sidebar?.onSearch ?: { searchVisible = true }))
            add(JAction(stringResource(R.string.agents_chip_all_machines), PhIcons.Desktop,
                checked = page.settings.machineFilter.isEmpty(), onClick = { model.machine(""); onMachineSelected("") }))
            page.machineCounts.forEach { chip -> add(JAction(chip.machine.displayName, PhIcons.Desktop,
                checked = page.settings.machineFilter == chip.machine.id,
                onClick = { model.machine(chip.machine.id); onMachineSelected(chip.machine.id) })) }
        }
        if (archives?.api?.supported == true) add(JAction(stringResource(R.string.agents_archives_title), PhIcons.Archive,
            onClick = { showArchives = true }))
        AgentRange.entries.forEach { range -> add(JAction(stringResource(R.string.agents_menu_range, range.label), checked = page.preferences.range == range, onClick = { model.range(range) })) }
        if (!homeChrome) groupings.forEach { grouping -> add(JAction(grouping.label, checked = page.preferences.grouping == grouping, onClick = { model.grouping(grouping) })) }
        // Not under By Status / By App / Recently Used / By pinned: iOS hides Sort there (AgentsMenus.swift).
        val hideSort = page.preferences.grouping in setOf(
            AgentGrouping.State, AgentGrouping.App, AgentGrouping.Recent, AgentGrouping.Direction)
        if (!hideSort) AgentSort.entries.forEachIndexed { index, sort ->
            add(JAction(stringResource(R.string.agents_menu_sort, sort.label), icon = sort.icon,
                checked = page.preferences.sort == sort, separatorBefore = index == 0, onClick = { model.sort(sort) })) }
        if (glassRows) {
            // iOS's ⋯ › Display: the three row styles and Compact List, one choice (#4338 A9).
            val display = stringResource(R.string.agents_menu_display)
            AgentListDisplay.entries.forEachIndexed { index, choice ->
                add(JAction(choice.label, checked = page.preferences.display == choice, section = display.takeIf { index == 0 },
                    onClick = { model.display(choice) }))
            }
        } else {
            add(JAction(stringResource(R.string.agents_menu_compact), checked = page.preferences.compact, onClick = { if (!page.preferences.compact) model.compact() }))
            add(JAction(stringResource(R.string.agents_menu_detailed), checked = !page.preferences.compact, onClick = { if (page.preferences.compact) model.compact() }))
        }
        // Recently Used is a flat list: Collapse / Expand All have nothing to fold (iOS AgentsMenus).
        val recentFlat = page.preferences.grouping == AgentGrouping.Recent
        if (!recentFlat) {
            add(JAction(stringResource(R.string.agents_menu_collapse_all), checked = page.list.allCollapsed, separatorBefore = glassRows, onClick = { model.foldAll(true) }))
            add(JAction(stringResource(R.string.agents_menu_expand_all), checked = page.list.groups.isNotEmpty() && page.list.groups.none { it.folded } && page.list.machines.none { it.folded } &&
                // under "By machine" a tab group folded by hand is not expanded either (#2792)
                (page.preferences.grouping != AgentGrouping.MachineSpace || page.preferences.foldedTabs.isEmpty()), onClick = { model.foldAll(false) }))
        }
        add(JAction(stringResource(R.string.agents_menu_machines), icon = PhIcons.Gauge,
            separatorBefore = recentFlat && glassRows, onClick = { navigator.navigate(MachinesRoute) }))
        if (terminalPanes) add(JAction(stringResource(R.string.agents_menu_terminals), icon = PhIcons.TerminalWindow,
            onClick = { navigator.navigate(persona.core.navigation.TerminalPanesRoute) }))
        if (requestsInMenu) add(JAction(stringResource(R.string.agents_requests_history), icon = PersonaIcons.waitsForYou,
            onClick = { allRequests = true }))
    }, { menu = false }, title = stringResource(R.string.agents_action_list_settings), note = stringResource(R.string.agents_menu_note))
    }
}

@Composable
private fun AgentsListSettingsSheet(content: @Composable () -> Unit) = content()

@Composable
private fun AgentsListPlaceholder() {
    val loading = stringResource(persona.core.designsystem.R.string.designsystem_loading)
    val fill = PersonaTheme.palette.sunken
    val shape = PersonaTheme.shapes.controlShape
    Column(
        Modifier.fillMaxWidth().repoCard(first = true, last = true)
            .testTag("agents-initial-placeholder").clearAndSetSemantics { contentDescription = loading },
    ) {
        repeat(3) {
            Row(
                Modifier.fillMaxWidth().padding(vertical = Tokens.Space.s),
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Box(Modifier.size(Tokens.Space.Standard.xxl).background(fill, shape))
                Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    Box(Modifier.fillMaxWidth(0.48f).height(Tokens.Space.m).background(fill, shape))
                    Box(Modifier.fillMaxWidth(0.68f).height(Tokens.Space.s).background(fill, shape))
                }
            }
        }
    }
}

/**
 * The session and computer a card's sheet reads, looked up in every manager's lists as the card is: a notification's
 * card may be another assistant's, whose session and computer the page does not list. The page's own session comes
 * first (it carries what is pending on it), and the hub's computer (the page has placeholders for unknown ones).
 */
internal fun cardContext(request: Interaction, pageSessions: List<Session>, pageMachines: List<Machine>,
    everySession: List<Session>, everyMachine: List<Machine>): Pair<Session?, Machine?> =
    (pageSessions.firstOrNull { it.key == request.sessionKey } ?: everySession.firstOrNull { it.key == request.sessionKey }) to
        (everyMachine.firstOrNull { it.id == request.machineId } ?: pageMachines.firstOrNull { it.id == request.machineId })

/**
 * The cards the page shows: the current assistant's, of the hub group shown. A card names its manager, else it is its
 * session's (one of a session the list no longer has: its own stamp alone; none: the default's).
 */
/**
 * The to-dos an agent (or a project) on computer [machineId] stamped [manager] can be put on: its own assistant's, the
 * ones a new to-do from the same sheet is made under (AgentsViewModel.todoOwner); every one with one assistant, and for
 * another group's computer.
 */
internal fun persona.core.contracts.api.CurrentAssistant.todosFor(todos: List<persona.core.contracts.Todo>, machineId: String, manager: String?): List<persona.core.contracts.Todo> {
    val roster = roster?.takeIf { it.isMulti } ?: return todos
    if (persona.core.contracts.GroupKeys.namespaceOf(machineId) != group) return todos
    val owner = roster.shownUnder(manager)
    return todos.filter { roster.shownUnder(it.manager) == owner }
}

internal fun shownRequests(requests: List<Interaction>, group: GroupFilter?, assistant: persona.core.contracts.api.CurrentAssistant,
    sessions: List<Session>, hidden: Set<SessionKey> = emptySet()): List<Interaction> {
    val managers = sessions.associate { it.key to it.manager }
    return requests.filter { request ->
        !request.isDisplayMirror && request.sessionKey !in hidden && (group == null || group.admits(request.machineId)) && assistant.ownsOn(request.machineId, request.manager ?: managers[request.sessionKey])
    }
}

@Composable
private fun ConnectionNotice(state: ConnectionState, onSettings: () -> Unit) {
    if (state in listOf(ConnectionState.CertificateMismatch, ConnectionState.Revoked, ConnectionState.NeedsLocalNetworkPermission)) {
        JNotice(title = state.title, text = stringResource(when (state) {
            ConnectionState.CertificateMismatch -> R.string.agents_connection_certificate_text
            ConnectionState.Revoked -> R.string.agents_connection_revoked_text
            else -> R.string.agents_connection_local_network_text
        }), status = Status.NeedsYou,
            actionLabel = stringResource(if (state == ConnectionState.NeedsLocalNetworkPermission) R.string.agents_connection_allow else R.string.agents_connection_diagnose),
            onAction = onSettings)
    } else Text(stringResource(R.string.agents_connection_cached), Modifier.padding(vertical = 8.dp.dense), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
}

@Composable
internal fun SectionTitle(text: String) { JSectionHeader(text) }

/**
 * A session row of the tree and the space page. [located] rows sit outside the computer tree, so they say which
 * computer and space they run in; rows under a space do not repeat it. On Glass the row is iOS's Avatar row (#4338 A9):
 * its line 2 word and line 3 come from core/domain's `rowContent`, the computer named only while the list
 * [mixesComputers], the host while By To-do mixes hosts ([showHost]). Brief and Board keep their facts line.
 */
@Composable
internal fun SlotRow(
    row: AgentSession, machine: String?, metadata: AgentRowMetadata, now: Instant, compact: Boolean, pinned: Boolean, muted: Boolean,
    depth: Int, position: GroupPosition, located: Boolean, onOpen: () -> Unit,
    /** What a long press offers; null: nothing (a computer's projects page lists its agents outside any space as iOS does, with no menu). */
    menuItems: (@Composable () -> List<JAction>)?,
    slotLabel: String? = null, requestCount: Int = 0,
    /** Swiping the row left offers "Close" (iOS); null where the agent cannot be closed from here ([closeSwipe]). */
    onClose: (() -> Unit)? = null,
    /** Under its herdr tab (#2320): the tab's launcher, or a pane it split off. */
    launcher: Boolean = false, branch: Boolean = false,
    /** The card this agent waits on, first of its own ([firstBySession]): what it asks is the row's line while it needs you. */
    card: Interaction? = null,
    /** Its By Status section names its state, so the row does not ([TreeRowEntry.inStateSection]). */
    hideState: Boolean = false,
    /** The list holds more than one computer's agents (`mixesComputers`): a [located] row names its computer. */
    mixesComputers: Boolean = false,
    /** By To-do while the list mixes hosts (`mixesHosts`): the row names its host (botmux, Orca, Tern). */
    showHost: Boolean = false,
    /** Glass's row style while the list is not [compact] (#4338 A9); Brief and Board keep their row. */
    style: AgentRowStyle = AgentRowStyle.Avatar,
    /** Hub setting `use_herdr_tab_name`: promotes the herdr tab name to main title and moves the CLI title to detail. */
    preferHerdrTabName: Boolean = false,
    /** Held folded children under this parent row, for unified [childRollup] computation. */
    heldChildren: List<Session> = emptyList(),
    /** Tapping the subagent rollup subtitle opens this parent session at Session Overview's Subagents section. */
    onOpenSubagents: (() -> Unit)? = null,
    /** Unread side session(s) keeping this parent row Unread (#13524); null leaves the row unchanged. */
    sideUnread: UnreadSideSummary? = null,
    /** Opens the named unread side session directly in one tap (#13524). */
    onOpenSide: ((SessionKey) -> Unit)? = null,
) {
    var menu by remember { mutableStateOf(false) }
    val session = row.session
    val primaryTab = session.displayTabName(preferHerdrTabName)
    val rowTitle = session.displayTitle(preferHerdrTabName)
    val titleDetail = session.displayTitleDetail(preferHerdrTabName)
    val source = session.source
    val glass = PersonaTheme.style == AppStyle.Glass
    val content = row.rowContent(AgentRowContext(located = located, machine = machine.takeIf { mixesComputers }, host = showHost,
        // Under its tab's row the agent is named by its agent and whether it started the tab; the tab's row names the tab.
        pane = if (launcher || branch) AgentPane(session.agentLabel, launcher) else null, slot = slotLabel, stateSection = hideState,
        preferHerdrTabName = preferHerdrTabName), muted,
        display = AgentListDisplay.of(if (glass) style else AgentRowStyle.Avatar, compact))
    val links = androidx.compose.ui.platform.LocalUriHandler.current
    // "Lark groups" / "In a tab" / "Desktop app" under the time (#3132, #3043, #17054); a botmux session's tag opens its Feishu
    // topic when it has one.
    val origin = session.agentSource?.takeIf { !session.assistant }?.let { kind ->
        val link = session.externalLink.takeIf { kind == AgentSource.Feishu }
        RowSource(stringResource(when (kind) {
            AgentSource.Feishu -> R.string.agents_source_feishu
            AgentSource.Terminal -> R.string.agents_filter_terminal
            AgentSource.Desktop -> R.string.agents_source_desktop
        }), when (kind) {
            AgentSource.Feishu -> SourceKind.Feishu
            AgentSource.Terminal -> SourceKind.Terminal
            AgentSource.Desktop -> SourceKind.Desktop
        }, onClick = link?.let { { links.openUri(it) } }, clickLabel = link?.let { stringResource(R.string.agents_source_feishu_open) })
    }
    val rowSideUnread = sideUnread?.toRowSideUnread(onOpenSide)
    JSwipeActions(end = onClose?.let { SwipeAction(stringResource(R.string.agents_close), PhIcons.X, Status.NeedsYou, it, destructive = true) }) {
    AgentRow(
        title = rowTitle, status = row.rowStatus(muted), depth = depth, position = position,
        onClick = onOpen, onLongClick = menuItems?.let { { menu = true } }, compact = compact, muted = muted,
        selected = isSessionSelected(LocalDetailKey.current, session.key),
        // What its card asks while it needs you, else why its host says it waits or stopped, where its work stands, its last words.
        // When only folded side sessions are unread, omit the parent's own already-seen preview so the side line stands in its place (#13524).
        preview = if (sideUnread?.replacesPreview == true) null else session.rowLine(card, now, muted)?.text(), time = relativeTime(agentResources(), row.updatedAt, now),
        // Codex, a compatible CLI or what the hub calls a runtime's agent ("DSH"); a session with a source names it in its facts instead.
        agent = session.agentLabel.takeIf { !session.isClaude && source == null },
        statusText = content.stateWord?.text(),
        facts = buildList {
            // Where its closeout stands on merged and deployed (#3612), first: the facts' line is cut at its end.
            session.closeoutFacts.forEach { add(RowFact(it.text.text(), caution = it.contradicted)) }
            // Glass says where it runs, its source, model and to-do on its third line ([place]); view only stays a fact.
            if (glass && source?.readOnly == true) add(RowFact(stringResource(R.string.agents_row_view_only), caution = true))
            if (!glass) {
                titleDetail?.let { add(RowFact(it)) }
                // Where a Codex thread or a runtime's session comes from, and when the phone can only read it.
                source?.let { add(RowFact(listOfNotNull(it.label, it.agentName).joinToString(" / "))) }
                if (source?.readOnly == true) add(RowFact(stringResource(R.string.agents_row_view_only), caution = true))
                // By Machine's sections are the computers (iOS `rowMachine` skips `.machine`); other groupings name the
                // host while the list mixes computers ([mixesComputers]).
                if (located) listOfNotNull(
                    machine.takeIf { mixesComputers },
                    session.spaceName?.takeIf { it.isNotBlank() } ?: session.folderName.takeIf { it.isNotBlank() },
                ).joinToString(" / ").takeIf { it.isNotBlank() }?.let { add(RowFact(it)) }
                // The model it runs on (#4338 A9): the session's model, else its launcher's, by its short name, never the raw id.
                session.rowModel?.let { add(RowFact(it)) }
                session.todo?.let { add(RowFact(stringResource(R.string.agents_row_todo, it.title))) }
                if (primaryTab == null) slotLabel?.let { add(RowFact(it)) }
            }
            if (requestCount > 0) add(RowFact(pluralStringResource(R.plurals.agents_row_requests, requestCount, requestCount), caution = true))
            session.loop?.let { add(RowFact(it.nextRunLabel(agentResources(), now))) }
            // a running workflow is in background too: counted once (#2935)
            session.backgroundCount.takeIf { it > 0 }?.let { add(RowFact(stringResource(R.string.agents_row_background, it))) }
            if (metadata.needsConfirmation) add(RowFact(if (metadata.verifying) PendingStatus.Verifying.localizedLabel else PendingStatus.Unconfirmed.localizedLabel, caution = true))
            if (metadata.pendingCount > 0) add(RowFact(stringResource(R.string.agents_row_queued, metadata.pendingCount), caution = true))
            if (metadata.sending) add(RowFact(PendingStatus.Sending.localizedLabel))
            if (metadata.hasDraft) {
                val draft = metadata.draft.replace('\n', ' ').clipped(20)
                add(RowFact(when {
                    draft.isBlank() -> stringResource(if (metadata.hasAttachments) R.string.agents_row_draft_attachments else R.string.agents_row_draft)
                    metadata.hasAttachments -> stringResource(R.string.agents_row_draft_attachments_text, draft)
                    else -> stringResource(R.string.agents_row_draft_text, draft)
                }, caution = true))
            }
        },
        flags = buildList {
            // Claude Code's ultracode on (#582, iOS UltracodeMark): the workflow mark right after the title.
            if (ultracodeMark(session)) add(RowFlag(stringResource(R.string.agents_row_ultracode), PersonaIcons.workflow, ai = true))
            if (pinned) add(RowFlag(stringResource(R.string.agents_row_pinned), PhIcons.PushPin))
            // Glass rings the bell at the third line's end instead.
            if (muted && !glass) add(RowFlag(stringResource(R.string.agents_row_muted), PhIcons.Prohibit))
            // Its closeout (#3612): the hub found a claim untrue (the line says which), or the turn is not finished.
            when (session.closeoutMark) {
                CloseoutMark.Contradicted -> add(RowFlag(stringResource(R.string.agents_row_closeout_contradicted), PhIcons.Warning, caution = true))
                CloseoutMark.NotFinished -> add(RowFlag(stringResource(R.string.agents_row_not_finished), PhIcons.CircleDashed))
                null -> Unit
            }
        },
        // Glass says "Started it" on the third line instead of the tag after the title.
        launcher = launcher && !glass, branch = branch, source = origin,
        // Which agent runs there and in which client (#6390): its mark before the state mark (Glass: in its well).
        agentMark = session.agentMark,
        place = if (glass && content.hasPlaceLine)
            RowPlace(CoreText.joined(" · ", content.place).text(), content.model, content.todo, content.muted) else null,
        waiting = row.waitsInBackground && !muted,
        style = style,
        showPreviewWithMetadata = card != null && !card.foldedUnder.isNullOrBlank() && card.homeKey == session.key,
        subagent = childRollup(session, heldChildren)?.let { rollup ->
            RowSubagent(
                text = stringResource(
                    if (rollup.kind == ChildRollupKind.NeedsYou) R.string.agents_row_subagents_need_you
                    else R.string.agents_row_subagents_running,
                    rollup.count,
                ),
                needsYou = rollup.kind == ChildRollupKind.NeedsYou,
                onClick = onOpenSubagents,
            )
        },
        sideUnread = rowSideUnread,
        // Its running Workflows (#2935) stay structured until the row lays them out beside critical facts (#3435).
        note = WorkflowSummary.lineFields(session, now)?.let { RowNote(it, WorkflowIcon) },
    )
    }
    if (menu && menuItems != null) JActionSheet(menuItems(), { menu = false }, title = rowTitle)
}

@Composable
private fun UnreadSideSummary.toRowSideUnread(onOpenSide: ((SessionKey) -> Unit)?): RowSideUnread {
    val side = latest
    if (side != null) {
        val sideTitle = side.displayTitle
        val label = stringResource(R.string.agents_side_unread_kicker, sideTitle)
        val more = extraCount.takeIf { it > 0 }?.let { stringResource(R.string.agents_side_unread_more, it) }
        val openLabel = stringResource(R.string.agents_side_unread_open, sideTitle)
        val spoken = listOfNotNull(
            stringResource(R.string.agents_side_unread_spoken, sideTitle),
            otherTitles.takeIf { it.isNotEmpty() }?.let { stringResource(R.string.agents_side_unread_also, it.joinToString(", ")) },
        ).joinToString(". ")
        return RowSideUnread(
            label = label,
            more = more,
            spoken = spoken,
            clickLabel = openLabel,
            onClick = onOpenSide?.let { cb -> { cb(side.key) } },
        )
    }
    val countText = pluralStringResource(R.plurals.agents_side_unread_count, count, count)
    return RowSideUnread(label = countText, spoken = countText)
}

/**
 * A herdr tab with several agents (#2320), as the web's overview row: its `#N` or name and the task (the launcher's
 * title, else the first pane's), then the most urgent agent's state and latest words, after its name when it is not the
 * one the title is from; tapping it opens that agent, "N agents" folds its agents to this row and opens them again.
 * Time and agent kind stay on the agents' own rows: the tab's row has "N agents" at its end instead.
 */
@Composable
internal fun AgentTabEntryRow(
    entry: TreeTabEntry, now: Instant, compact: Boolean, onOpen: () -> Unit, onToggle: () -> Unit, card: Interaction? = null,
    /** The lead agent is muted: a muted Awaiting Reply reads as Seen. */
    muted: Boolean = false,
    preferHerdrTabName: Boolean = false,
) {
    val lead = entry.tab.lead
    val session = lead.session
    val titled = entry.tab.launcher ?: entry.tab.members.first()
    val primaryTab = titled.session.displayTabName(preferHerdrTabName)
    val tabLabel = if (primaryTab != null) "" else titled.session.slotLabel.orEmpty()
    val tabTitle = titled.session.displayTitle(preferHerdrTabName)
    val asks = session.askingLine(card)?.text()
    val words = asks ?: session.rowLine(card, now, muted)?.text()
    AgentTabRow(
        label = tabLabel, title = tabTitle, status = lead.rowStatus(muted),
        agents = entry.tab.members.size, folded = entry.folded, onToggle = onToggle, onClick = onOpen,
        depth = entry.depth, position = GroupPosition(entry.first, entry.last),
        // "Codex · Needs approval: …": the card's own words already have a colon
        preview = words?.let { if (lead.id == titled.id) it else stringResource(if (asks != null) R.string.agents_tab_lead_asks else R.string.agents_tab_lead_words, session.agentLabel, it) },
        statusText = lead.rowStatusText(muted), compact = compact,
        // folded, the tab's row stands for its agents: it is the one lit while one of them is open
        selected = entry.folded && entry.tab.members.any { isSessionSelected(LocalDetailKey.current, it.session.key) },
    )
}

/**
 * Swiping a row left closes the agent (iOS): only a live agent that closes from here (in a terminal pane, or a runtime's
 * session whose node says so, #6294), while nothing else is being saved.
 */
internal fun closeSwipe(row: AgentSession, busy: Boolean, close: () -> Unit): (() -> Unit)? =
    close.takeIf { row.session.isLive && row.session.canClose && !row.session.assistant && !busy }

/**
 * The session actions are shared by the home list and the space page. A [nested] row, drawn under its parent's
 * "Subtasks (N)", offers no Pin: Pinned lists rows of their own only, so it would not move (#4494); a pin it has can go.
 */
@Suppress("DEPRECATION")
internal fun forkBlockedByTurn(session: Session): Boolean =
    session.isLive && (session.agentState == AgentState.Working || session.agentState == AgentState.NeedsYou)

@Composable
internal fun AgentSessionMenuItems(
    row: AgentSession, pinned: Boolean, muted: Boolean, busy: Boolean,
    onOpen: () -> Unit, onPin: () -> Unit, onMute: () -> Unit, onRead: () -> Unit,
    onRename: () -> Unit, onStopLoop: () -> Unit, onHandoff: () -> Unit, onClose: () -> Unit,
    onAttach: (() -> Unit)? = null,
    onFork: (() -> Unit)? = null,
    forkOnline: Boolean = true,
    /** Explicit Codex takeover / continuation actions, placed as a group right after terminal items. */
    takeoverActions: List<JAction> = emptyList(),
    /** Stops its turn; offered while it works or needs you and can be interrupted (iOS `AgentRowMenu`). */
    onInterrupt: (() -> Unit)? = null,
    onLinkTodo: (() -> Unit)? = null,
    onUnlinkTodo: (() -> Unit)? = null,
    onDelete: (() -> Unit)? = null,
    nested: Boolean = false,
): List<JAction> {
    val copy = rememberCopy()
    val session = row.session
    return buildList {
        if (pinned || !nested) add(JAction(stringResource(if (pinned) R.string.agents_action_unpin else R.string.agents_action_pin), PhIcons.PushPin, enabled = !busy, onClick = onPin))
        if (onInterrupt != null && (row.state == AgentState.Working || row.state == AgentState.NeedsYou) && session.canInterrupt)
            add(JAction(stringResource(R.string.agents_action_interrupt), PersonaIcons.interrupt, enabled = !busy, onClick = onInterrupt))
        add(JAction(stringResource(if (muted) R.string.agents_action_unmute else R.string.agents_action_mute), PhIcons.Prohibit, enabled = !busy, onClick = onMute))
        if (session.todo != null && onUnlinkTodo != null) {
            // The same picker moves it to another todo: the hub releases the old link (#2348).
            onLinkTodo?.let { add(JAction(stringResource(R.string.agents_action_move_todo), PhIcons.ListChecks, enabled = !busy, onClick = it)) }
            add(JAction(stringResource(R.string.agents_action_unlink_todo), PhIcons.ListChecks, enabled = !busy, onClick = onUnlinkTodo))
        } else if (onLinkTodo != null) {
            add(JAction(stringResource(R.string.agents_action_link_todo), PhIcons.ListChecks, enabled = !busy, onClick = onLinkTodo))
        }
        onAttach?.let { add(JAction(stringResource(R.string.agents_action_attach), PhIcons.PlugsConnected, enabled = !busy, onClick = it)) }
        if (onFork != null) {
            val turnRunning = forkBlockedByTurn(session)
            add(JAction(
                stringResource(persona.feature.chat.R.string.chat_menu_fork),
                PersonaIcons.forkChat,
                enabled = !busy && forkOnline && !turnRunning,
                subtitle = if (turnRunning) stringResource(persona.feature.chat.R.string.chat_menu_fork_wait_turn) else null,
                onClick = onFork,
            ))
        }
        // Renaming changes the terminal tab and Claude's /rename; a session without a terminal renames where its node says it
        // can (a runtime's title, #6294).
        if (session.canRename) add(JAction(stringResource(R.string.agents_action_rename), PhIcons.NotePencil, enabled = !busy, onClick = onRename))
        if (session.loop != null && session.canSend) add(JAction(stringResource(R.string.agents_action_stop_loop), enabled = !busy, onClick = onStopLoop))
        add(JAction(stringResource(R.string.agents_action_handoff), onClick = onHandoff))
        if (row.unseen) add(JAction(stringResource(R.string.agents_action_mark_read), PhIcons.Check, onClick = onRead))
        // Only a session in a terminal resumes there; a Codex thread by its own thread id.
        session.resumeCommand()?.let { command -> add(JAction(stringResource(R.string.agents_action_copy_resume), onClick = { copy(command, null) })) }
        if (takeoverActions.isNotEmpty()) addAll(takeoverActions)
        add(JAction(stringResource(R.string.agents_action_copy_directory), enabled = session.cwd.isNotBlank(), onClick = { copy(session.cwd, null) }))
        add(JAction(stringResource(R.string.agents_action_open_session), onClick = onOpen))
        // Its pane quits, or a runtime's session ends where its node says it can (#6294).
        if (session.isLive && session.canClose) add(JAction(stringResource(R.string.agents_action_close_agent), destructive = true, enabled = !busy, onClick = onClose))
        if (session.canDelete && onDelete != null) add(JAction(stringResource(R.string.agents_action_delete_chat), destructive = true, enabled = !busy, onClick = onDelete))
    }
}

private val ConnectionState.title: String @Composable get() = when (this) {
    ConnectionState.Online -> "Agents" // i18n-ignore: the tab's name is Agents in every language (docs/protocol/i18n.md)
    ConnectionState.Connecting -> stringResource(R.string.agents_connection_connecting)
    ConnectionState.Updating -> stringResource(R.string.agents_connection_updating)
    ConnectionState.WaitingForNetwork -> stringResource(R.string.agents_connection_waiting)
    ConnectionState.NeedsLocalNetworkPermission -> stringResource(R.string.agents_connection_local_network)
    ConnectionState.CertificateMismatch -> stringResource(R.string.agents_connection_certificate)
    ConnectionState.Revoked -> stringResource(R.string.agents_connection_revoked)
}
/** What TalkBack calls the level (iOS WorktreeRow: "Main worktree" / "Worktree"). */
internal val RepoLevel.kind: String @Composable get() = stringResource(when (this) {
    RepoLevel.Repository -> R.string.agents_repo_repository
    RepoLevel.MainCheckout -> R.string.agents_repo_main_checkout
    RepoLevel.Worktree -> R.string.agents_repo_worktree
})
/**
 * The level's leading icon. Non-main worktrees carry none: their name and the extra indentation identify them, as on
 * the web sidebar; [AgentSpaceHeader] already treats a null icon as no mark.
 */
internal val RepoLevel.icon: PhIcon? get() = when (this) {
    RepoLevel.Repository -> PhIcons.Folder
    RepoLevel.MainCheckout -> PhIcons.House
    RepoLevel.Worktree -> null
}
/** The grouping's own icon, as iOS's `DevGrouping.symbol`. */
private val AgentGrouping.icon: PhIcon get() = when (this) {
    AgentGrouping.Repo -> PersonaIcons.folder
    AgentGrouping.MachineSpace, AgentGrouping.Space -> PersonaIcons.space
    AgentGrouping.Todo -> PersonaIcons.todo
    AgentGrouping.Machine -> PersonaIcons.machine
    AgentGrouping.App -> PersonaIcons.byApp
    AgentGrouping.Recent -> PersonaIcons.recent
    AgentGrouping.Direction -> PersonaIcons.pinned
    // iOS draws two state dots (circle.grid.2x1), which no generated glyph shows; a board of columns stands in.
    AgentGrouping.State -> PhIcons.Kanban
}

internal val AgentGrouping.label: String @Composable get() = stringResource(when (this) {
    AgentGrouping.Repo -> R.string.agents_grouping_repo
    AgentGrouping.State -> R.string.agents_grouping_state
    AgentGrouping.Todo -> R.string.agents_grouping_todo
    AgentGrouping.Machine -> R.string.agents_grouping_machine
    AgentGrouping.App -> R.string.agents_grouping_app
    AgentGrouping.Recent -> R.string.agents_grouping_recent
    AgentGrouping.Direction -> R.string.agents_grouping_direction
    // iOS calls this grouping (computer, space, tab, agent) By Project (#4338 A5); the stored value stays MachineSpace.
    else -> R.string.agents_grouping_project
})
private val AgentQuickFilter.label: String @Composable get() = stringResource(when (this) {
    AgentQuickFilter.All -> R.string.agents_filter_all
    AgentQuickFilter.NeedsYou -> R.string.agents_filter_needs_you
    AgentQuickFilter.Unread -> R.string.agents_filter_unread
    AgentQuickFilter.Working -> R.string.agents_filter_working
    AgentQuickFilter.BackgroundWaiting -> R.string.agents_filter_background_waiting
    AgentQuickFilter.Feishu -> R.string.agents_filter_feishu
    AgentQuickFilter.Terminal -> R.string.agents_filter_terminal
})
internal val AgentSort.label: String @Composable get() = stringResource(when (this) {
    AgentSort.State -> R.string.agents_sort_state
    AgentSort.Updated -> R.string.agents_sort_updated
})
internal val AgentSort.icon get() = when (this) {
    AgentSort.State -> PhIcons.FunnelSimple
    AgentSort.Updated -> PhIcons.Clock
}
internal val AgentRange.label: String @Composable get() = stringResource(when (this) {
    AgentRange.Running -> R.string.agents_range_running
    AgentRange.All -> R.string.agents_range_all
    AgentRange.History -> R.string.agents_range_history
})
internal val AgentState.designStatus: Status get() = when (this) {
    AgentState.NeedsYou -> Status.NeedsYou
    AgentState.Unread -> Status.Unread
    AgentState.Working -> Status.Working
    AgentState.Idle -> Status.Idle
    AgentState.Unknown -> Status.Unknown
    AgentState.Offline -> Status.Offline
}
internal val AgentState.label: String @Composable get() = designStatus.localizedLabel

/**
 * A row's state colour: Awaiting Reply (#5096) and Just finished (#9786) take Unread's, since they wait on the user as
 * much; a muted one is just Seen.
 */
internal fun AgentSession.rowStatus(muted: Boolean): Status = when {
    seenPlace != null -> Status.Idle
    (asksYou || showsJustFinished) && !muted -> Status.Unread
    else -> state.designStatus
}

/**
 * The row's state in words, as iOS's default row (#4338 A9, `rowStateWord`): only what its dot does not say alone, Needs
 * you (unless muted), Awaiting reply, Just finished and Waiting in the background. Working, Unread and the rest are the
 * dot's; TalkBack still hears the state ([AgentRow]).
 */
@Composable
internal fun AgentSession.rowStatusText(muted: Boolean): String? = rowStateWord(muted)?.text()
@Composable
internal fun StateDot(state: AgentState, breathe: Boolean = false) {
    JStatusMark(state.designStatus, animated = breathe)
}

/** The space shown for each group heading: the herdr space itself, or the one space a repository's sessions are all in. */
internal fun spacesByGroup(list: AgentListResult, grouping: AgentGrouping, spaces: List<AgentSpace>): Map<String, AgentSpace> =
    when (grouping) {
        AgentGrouping.MachineSpace -> spaces.associateBy { it.id }
        // A repository whose sessions are all in one herdr space (the usual case) takes that space's marks and ⋯ (iOS).
        AgentGrouping.Repo -> {
            val byId = spaces.associateBy { it.id }
            list.groups.filter { it.repo == RepoLevel.Repository }.mapNotNull { repo ->
                val members = repo.rows + list.groups.filter { it.under == repo.id }.flatMap { it.rows }
                agentSpaces(members).singleOrNull()?.takeIf { it.members.first().session.spaceId != null }
                    ?.let { byId[it.id] }?.let { repo.id to it }
            }.toMap()
        }
        else -> emptyMap()
    }
