package persona.feature.agents

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.repeatOnLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.navigation3.runtime.EntryProviderScope
import androidx.navigation3.runtime.NavKey
import dagger.hilt.android.EntryPointAccessors
import persona.core.contracts.api.ConnectionState
import persona.core.designsystem.JAction
import persona.core.designsystem.JDialog
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.operationFeedback
import persona.core.domain.sort.AgentSession
import persona.core.domain.sort.agentSpaces
import persona.core.navigation.AppTab
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.MachineSpacesRoute
import persona.core.navigation.Navigator
import persona.core.navigation.SpaceRoute
import kotlinx.coroutines.delay
import java.time.Instant
import persona.core.designsystem.FeedbackNotice
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.rememberToastState
import persona.feature.chat.CodexTakeoverBusyPill
import persona.feature.chat.CodexTakeoverClient
import persona.feature.chat.CodexTakeoverDialogs
import persona.feature.chat.codexTakeoverMenuActions
import persona.feature.chat.rememberCodexTakeoverCoordinator

/**
 * The space page as a destination of its own ([SpaceRoute]): in a wide window it opens beside the Agents list (the
 * shell gives it the detail pane), on a phone it is pushed over the list. A conversation opened from it goes on top,
 * and back returns to the page. [dependenciesOverride] is the same one the shell gives [agentsEntries].
 */
fun EntryProviderScope<NavKey>.spaceEntries(navigator: Navigator, dependenciesOverride: AgentsDependencies? = null) {
    entry<SpaceRoute> { route -> SpaceDestination(route, navigator, dependenciesOverride) }
}

/** The pairing's services for the page, set up as the list's own destination sets them up. */
@Composable
private fun SpaceDestination(route: SpaceRoute, navigator: Navigator, dependenciesOverride: AgentsDependencies?) {
    val context = LocalContext.current.applicationContext
    val dependencies = remember(context, dependenciesOverride) {
        dependenciesOverride ?: EntryPointAccessors.fromApplication(context, AgentsDependencies::class.java)
    }
    val wiring by dependencies.wiring().state.collectAsStateWithLifecycle()
    val renamer = remember(context) {
        if (context is dagger.hilt.internal.GeneratedComponentManager<*>)
            EntryPointAccessors.fromApplication(context, AgentsRenameDependencies::class.java).renamer()
        else null
    }
    CompositionLocalProvider(
        LocalAgentsOperations provides (wiring.operations ?: LocalAgentsOperations.current),
        LocalAgentCreationService provides (wiring.creation ?: LocalAgentCreationService.current),
    ) {
        val operations = LocalAgentsOperations.current
        val scope = wiring.scope.ifEmpty { operations?.operations?.scope.orEmpty() }
        val model = viewModel(key = "space:${wiring.scope}:${operations?.operations?.scope.orEmpty()}") {
            AgentsViewModel(dependencies.sessions(), dependencies.interactions(), dependencies.connection(), dependencies.sender(),
                dependencies.preferences(), operations, wiring.mainSender, scope, renamer, hubGroups = dependencies.groups(),
                listContext = kotlinx.coroutines.Dispatchers.Default, listAdapter = dependencies.listAdapter(),
                visibleChat = dependencies.visibleChat(), heldChat = dependencies.heldChat(), finishedChat = dependencies.finishedChat())
        }
        val creationService = LocalAgentCreationService.current
        val starPrompt = persona.core.designsystem.LocalStarPrompt.current
        // As Agents' "New session": the first message goes once the hub answers, with its attachments (#2321).
        val uploads = rememberNewAgentUploads()
        val creationModel = creationService?.let { service ->
            viewModel(key = "space-new-agent:${service.scope}:${System.identityHashCode(service)}") {
                NewAgentViewModel(service, wiring.creationPreferences?.let { AgentCreationPreferences(context, it) }
                    ?: AgentCreationPreferences(context), dependencies.sessions().machines,
                    sender = dependencies.sender(), adopt = dependencies.sessions()::adopt,
                    usage = dependencies.machines()?.let { m -> { id: String -> m.usageWindows(id) } },
                    launched = { starPrompt?.launched() }, uploads = uploads)
            }
        }
        SpacePage(route, model, navigator, creationModel)
    }
}

/** The page itself: the space's sessions in tab order with the list's menus, and a new tab in the space. */
@Composable
private fun SpacePage(route: SpaceRoute, model: AgentsViewModel, navigator: Navigator, creationModel: NewAgentViewModel?) {
    val page by model.page.collectAsStateWithLifecycle()
    val connection by model.connection.state.collectAsStateWithLifecycle()
    val groups by model.hubGroups.groups.collectAsStateWithLifecycle()
    val pending by model.interactions.pending.collectAsStateWithLifecycle()
    val operations by model.operationState.collectAsStateWithLifecycle()
    val notice by model.notice.collectAsStateWithLifecycle()
    val failure by model.failure.collectAsStateWithLifecycle()
    // A fallback for when no app feedback is provided (previews, tests): the page's own toast, as the root's.
    val toast = rememberToastState()
    val feedback = LocalAppFeedback.current
    var operationDialog by remember { mutableStateOf<AgentOperationDialog?>(null) }
    var confirmRead by remember { mutableStateOf<MarkAllSeenBatch?>(null) }
    var newAgent by rememberSaveable { mutableStateOf(false) }
    val creation = creationModel?.state?.collectAsStateWithLifecycle()?.value
    var now by remember { mutableStateOf(Instant.now()) }
    val lifecycle = LocalLifecycleOwner.current
    LaunchedEffect(lifecycle) {
        lifecycle.lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            while (true) { now = Instant.now(); delay(1_000) }
        }
    }
    val failureResources = agentResources()
    LaunchedEffect(failure) {
        failure?.let {
            if (feedback?.failure(it) != true) toast.show(FeedbackNotice(operationFeedback(it).text.resolve(failureResources), style = FeedbackStyle.Error))
            model.clearFailure()
        }
    }
    val resources = agentResources()
    LaunchedEffect(notice) {
        notice?.let { val text = it.resolve(resources); if (feedback?.notice(text) != true) toast.show(FeedbackNotice(text)); model.clearNotice() }
    }
    LaunchedEffect(creation?.created) {
        creation?.created?.let { session ->
            newAgent = false
            navigator.navigate(ChatRoute(session.machineId, session.sessionId))
            creationModel.opened()
            model.refresh()
        }
    }
    val spaces = page.spaces
    val space = spaces.firstOrNull { it.id == route.spaceId }
    val workspace = space?.members?.first()?.session?.operationWorkspaceId
    // An Orca space is its worktree: New worktree… from it (#17054), resolved against the catalog's Orca rows.
    val creationService = LocalAgentCreationService.current
    LaunchedEffect(lifecycle, model, creationService) {
        lifecycle.lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) { model.watchWorkspaceCatalog(creationService, repositoryRows = false) }
    }
    val orcaRows by model.orcaWorkspaces.collectAsStateWithLifecycle()
    val worktrees = rememberOrcaWorktreeActions(creationService, page.machines, orcaRows, model::refreshWorkspaceCatalog)
    fun openRow(row: AgentSession) {
        model.opened(row)
        navigator.navigate(ChatRoute(row.session.machineId, row.session.sessionId))
    }
    val takeoverOps = model.operationsApi ?: LocalAgentsOperations.current?.operations
    val takeoverClient = remember(takeoverOps) { takeoverOps?.let(CodexTakeoverClient::of) }
    val takeoverCoordinator = rememberCodexTakeoverCoordinator(
        client = takeoverClient,
        sessions = model.sessions,
        onOpenSession = null,
        fallbackToast = toast,
        onCompleted = { _, adopted -> navigator.navigate(ChatRoute(adopted.machineId, adopted.sessionId)) },
    )
    val rowMenu: @Composable (AgentSession) -> List<JAction> = { row ->
        val onlineForRow = groups.stateFor(row.session.machineId, connection) == ConnectionState.Online &&
            page.machines.firstOrNull { it.id == row.session.machineId }?.online != false
        val takeoverActions = if (takeoverClient != null && !row.session.archived) {
            codexTakeoverMenuActions(row.session, takeoverCoordinator, enabled = !operations.busy && onlineForRow)
        } else emptyList()
        val forkAction: (() -> Unit)? = if (row.session.canFork && !row.session.archived && model.hasOperations) ({
            model.fork(row.session) { forked -> navigator.navigate(ChatRoute(forked.machineId, forked.sessionId)) }
        }) else null
        if (!row.session.isLive) buildList {
            add(JAction(stringResource(R.string.agents_action_open_read_only), onClick = { openRow(row) }))
            addAll(takeoverActions)
            forkAction?.let {
                add(JAction(stringResource(persona.feature.chat.R.string.chat_menu_fork), PersonaIcons.forkChat,
                    enabled = !operations.busy && onlineForRow, onClick = it))
            }
            if (row.session.canDelete) add(JAction(stringResource(R.string.agents_action_delete_chat), destructive = true, enabled = !operations.busy) {
                operationDialog = AgentOperationDialog.Delete(row.session)
            })
        } else AgentSessionMenuItems(row, row.id in page.preferences.pins.sessions, page.preferences.mutes.mutes(row), operations.busy,
            onOpen = { openRow(row) }, onPin = { model.pin(row) }, onMute = { model.mute(row) },
            onRead = { model.markRead(listOf(row)) }, onRename = { operationDialog = AgentOperationDialog.Rename(row.session) },
            onStopLoop = { model.stopLoop(row.session) }, onHandoff = {
                model.handoff(row.session, row.session.handoffPrompt(resources), onQueued = { navigator.selectTab(AppTab.Persona) }, onBound = {
                    navigator.navigate(ChatRoute(it.machineId, it.sessionId, kind = ConversationKind.Assistant))
                })
            },
            onFork = forkAction,
            forkOnline = onlineForRow,
            takeoverActions = takeoverActions,
            onInterrupt = { model.interrupt(row.session) },
            onClose = { operationDialog = AgentOperationDialog.Close(row.session) },
            onDelete = { operationDialog = AgentOperationDialog.Delete(row.session) })
    }
    Box(Modifier.fillMaxSize()) {
        SpaceScreen(space, page.machines, pending, page.preferences, operations.busy || takeoverCoordinator.inFlight,
            canStart = workspace != null && page.machines.any { it.id == space?.machineId && it.online }, toast = toast, now = now,
            onDismiss = navigator::back, onOpen = ::openRow,
            onNewTab = { creationModel?.startIn(AgentSpacePreset(it.machineId, requireNotNull(workspace), it.cwd, it.name)); newAgent = true },
            spaceMenu = { shown ->
                val session = shown.members.first().session
                val markAll = markAllSeenBatch(shown.members)
                val worktreeAction = worktrees?.let { w -> w.targetOf(shown.members.map { it.session })?.let { w.newWorktreeAction(it) } }
                AgentSpaceMenu(session, shown.preferenceKey in page.preferences.pins.spaces, shown.preferenceKey in page.preferences.mutes.spaces,
                    operations.busy, onPin = { model.spacePin(session) }, onMute = { model.spaceMute(session) },
                    onRename = { operationDialog = AgentOperationDialog.RenameSpace(session) },
                    onMarkRead = markAll.takeIf { it.unread.isNotEmpty() }?.let { batch -> { confirmRead = batch } },
                    extra = listOfNotNull(worktreeAction))
            },
            sessionMenu = rowMenu, title = route.title,
            onMachine = { navigator.navigate(MachineSpacesRoute(it)) },
            // The page's list arrives a moment after it opens: no "No running agents" flash before that.
            loaded = page.sessions.isNotEmpty() || page.machines.isNotEmpty(),
            onClose = { row -> closeSwipe(row, operations.busy) { operationDialog = AgentOperationDialog.Close(row.session) } },
            sessions = page.sessions,
            onOpenSide = { row, side ->
                model.opened(row)
                navigator.navigate(ChatRoute(side.machineId, side.sessionId))
            })
        if (takeoverClient != null) CodexTakeoverBusyPill(
            takeoverCoordinator,
            page.sessions,
            Modifier.align(Alignment.BottomCenter).navigationBarsPadding().padding(horizontal = Tokens.Space.page, vertical = Tokens.Space.l),
        )
    }
    operationDialog?.let { dialog ->
        val machine = page.machines.firstOrNull { it.id == dialog.session.machineId }
        AgentOperationDialog(dialog, model, machine?.displayName, creationModel?.service, machine,
            preferHerdrTabName = page.preferences.prefersHerdrTabName(dialog.session)) { operationDialog = null }
    }
    if (takeoverClient != null) CodexTakeoverDialogs(takeoverCoordinator)
    OrcaWorktreeDialogs(worktrees)
    confirmRead?.let { markAll ->
        MarkAllReadDialog(markAll.unread.size, onDismiss = { confirmRead = null }) {
            model.markRead(markAll.unread, clear = true, release = markAll.release)
        }
    }
    if (newAgent) {
        if (creationModel != null) NewAgentDialog(creationModel, page.machines, connection == ConnectionState.Online,
            onAddProvider = { navigator.navigate(persona.core.navigation.SettingsPageRoute(persona.core.navigation.SettingsPage.Providers)) },
            onOpenProvider = { navigator.navigate(persona.core.navigation.ModelProviderRoute(it)) }) { newAgent = false }
        else JDialog(onDismiss = { newAgent = false }, title = stringResource(R.string.agents_action_new_session),
            text = stringResource(R.string.agents_new_session_unavailable), confirm = stringResource(R.string.agents_close) to { newAgent = false }, dismiss = null)
    }
}
