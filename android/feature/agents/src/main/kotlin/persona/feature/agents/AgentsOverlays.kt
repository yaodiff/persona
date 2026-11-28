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

/**
 * The Agents screen's overlay state that is not saved: which sheet or dialog is up and what it was opened for. One
 * remembered holder instead of a remember each (#17579); the values the screen saves across process death stay in its
 * own `rememberSaveable` calls and reach [AgentsOverlaysHost] as their state objects.
 */
@Stable
internal class AgentsOverlayState {
    val archivePreview = mutableStateOf<Session?>(null)
    val archiveTarget = mutableStateOf<Session?>(null)
    val operationDialog = mutableStateOf<AgentOperationDialog?>(null)
    val pickingTodoForSession = mutableStateOf<Session?>(null)
    // "Mark all as read" asks first and names how many it marks (iOS confirmReadAll).
    val confirmRead = mutableStateOf<MarkAllSeenBatch?>(null)
    /** A project being put on a to-do ("Link to to-do…"): its machine and its name as the hub keys it. */
    val pickingTodoForSpace = mutableStateOf<Pair<String, String>?>(null)
    val menu = mutableStateOf(false)
}

/**
 * Everything the Agents screen draws over its list: list settings, archiving and the archives, a read-only history
 * chat, the session operations, the to-do pickers, Mark all as read, New agent and the requests (moved as is, #17579).
 * ART stops compiling a method past 10,000 code units; drawn inline, this layer was a third of AgentsScreen's.
 */
@Composable
internal fun AgentsOverlaysHost(
    overlays: AgentsOverlayState, showArchivesState: MutableState<Boolean>, searchVisibleState: MutableState<Boolean>,
    newAgentState: MutableState<Boolean>, historyMachineState: MutableState<String?>,
    historySessionState: MutableState<String?>, allRequestsState: MutableState<Boolean>,
    requestDetailState: MutableState<Interaction?>, model: AgentsViewModel, navigator: Navigator, metadata: AgentsMetadata,
    creationModel: NewAgentViewModel?, onMachineSelected: (String) -> Unit,
    groups: List<persona.core.contracts.api.HubGroupInfo>, terminalPanes: Boolean, sidebar: AgentsSidebarLayout?,
    page: AgentsPageState, connection: ConnectionState, assistant: persona.core.contracts.api.CurrentAssistant,
    archives: AgentArchivesState?, todos: List<persona.core.contracts.Todo>, everyRequest: List<Interaction>,
    everySession: List<Session>, everyMachine: List<Machine>, requests: List<Interaction>,
    delivery: Map<String, RequestDelivery>, requestHistory: List<Interaction>,
    commandInputErrors: Map<String, persona.feature.chat.InteractionReply>, now: Instant, names: Map<String, String>,
    groupings: List<AgentGrouping>, homeChrome: Boolean, requestsInMenu: Boolean, creation: NewAgentState?,
    takeoverClient: CodexTakeoverClient?, takeoverCoordinator: persona.feature.chat.CodexTakeoverCoordinator,
    feedback: AppFeedback?, resources: android.content.res.Resources, open: (SessionKey) -> Unit,
    reachable: (Interaction) -> Boolean,
) {
    var archivePreview by overlays.archivePreview
    var archiveTarget by overlays.archiveTarget
    var operationDialog by overlays.operationDialog
    var pickingTodoForSession by overlays.pickingTodoForSession
    var confirmRead by overlays.confirmRead
    var pickingTodoForSpace by overlays.pickingTodoForSpace
    var menu by overlays.menu
    var showArchives by showArchivesState
    var searchVisible by searchVisibleState
    var newAgent by newAgentState
    var historyMachine by historyMachineState
    var historySession by historySessionState
    var allRequests by allRequestsState
    var requestDetail by requestDetailState
    if (menu) AgentsListSettingsMenu(overlays.menu, showArchivesState, searchVisibleState, allRequestsState, model,
        navigator, onMachineSelected, terminalPanes, sidebar, page, archives, groupings, homeChrome, requestsInMenu)
    archiveTarget?.let { target ->
        JDialog(onDismiss = { archiveTarget = null }, title = stringResource(R.string.agents_action_archive),
            text = stringResource(R.string.agents_archive_confirm, target.displayTitle),
            confirm = stringResource(R.string.agents_action_archive) to {
                val current = page.sessions.firstOrNull { it.key == target.key }
                if (current?.canArchiveAgent == true && everyRequest.none { it.sessionKey == target.key }) archives?.archive(current)
                archiveTarget = null
            })
    }
    if (showArchives && archives != null) JFullScreen({ showArchives = false }) {
        AgentArchivesScreen(archives, names, onBack = { showArchives = false }, onOpen = { archivePreview = it })
        archivePreview?.let { session -> JFullScreen({ archivePreview = null }) {
            persona.feature.chat.ChatDestination(ChatRoute(session.machineId, session.sessionId), navigator,
                historyReadOnly = true, historySession = session, onHistoryBack = { archivePreview = null })
        } }
    }
    if (historyMachine != null && historySession != null) {
        val target = (if (sidebar == null) page.sessions else everySession).firstOrNull {
            it.machineId == historyMachine && it.sessionId == historySession
        }
        JFullScreen({ historyMachine = null; historySession = null }) {
            // Host feedback, toasts and takeover dialogs inside JFullScreen's own window so failure/retry feedback
            // is visible above the preview instead of hidden behind it (Super Rule 5).
            val historyToast = persona.core.designsystem.rememberToastState()
            val historyToastScope = rememberCoroutineScope()
            val historyRes = agentResources()
            val historyFeedback = remember(historyToast, historyToastScope, historyRes) {
                object : AppFeedback {
                    override fun failure(error: Throwable, operation: Boolean): Boolean {
                        if (!operation) return true
                        val text = persona.core.designsystem.operationFeedback(error).text.resolve(historyRes)
                        historyToastScope.launch { historyToast.show(FeedbackNotice(text, style = FeedbackStyle.Error)) }
                        return true
                    }
                    override fun notice(message: String): Boolean = notice(message, FeedbackStyle.Info)
                    override fun notice(message: String, style: FeedbackStyle): Boolean {
                        if (style == FeedbackStyle.Success && feedback?.notice(message, style) == true) return true
                        historyToastScope.launch { historyToast.show(FeedbackNotice(message, style = style)) }
                        return true
                    }
                    override fun notice(message: String, action: FeedbackAction): Boolean = notice(message, action, FeedbackStyle.Info)
                    override fun notice(message: String, action: FeedbackAction, style: FeedbackStyle): Boolean {
                        historyToastScope.launch { historyToast.show(FeedbackNotice(message, action = action, style = style)) }
                        return true
                    }
                }
            }
            CompositionLocalProvider(LocalAppFeedback provides historyFeedback) {
                val historyTakeoverCoordinator = rememberCodexTakeoverCoordinator(
                    client = takeoverClient,
                    sessions = model.sessions,
                    onOpenSession = null,
                    fallbackToast = historyToast,
                    successFeedback = feedback,
                    onCompleted = { _, adopted ->
                        historyMachine = null
                        historySession = null
                        open(adopted.key)
                    },
                )
                Box(Modifier.fillMaxSize()) {
                    persona.feature.chat.ChatDestination(ChatRoute(requireNotNull(historyMachine), requireNotNull(historySession)), navigator,
                        historyReadOnly = true, onHistoryBack = { historyMachine = null; historySession = null },
                        historyFooter = {
                            if (target != null && target.canContinueCodex && takeoverClient != null) {
                                val targetHubOnline = groups.stateFor(target.machineId, connection) == ConnectionState.Online
                                ChatSourceNote(
                                    session = target,
                                    modifier = Modifier.navigationBarsPadding(),
                                    takeover = historyTakeoverCoordinator,
                                    takeoverEnabled = targetHubOnline && page.machines.any { it.id == target.machineId && it.online },
                                )
                            } else Column(Modifier.fillMaxWidth().navigationBarsPadding().padding(Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                                creation?.error?.let { Text(it.resolve(), style = PersonaTheme.type.caption) }
                                // A Codex history thread or a runtime's session is not resumed in a terminal from the phone.
                                val sourced = target?.takeIf { it.hasSource }
                                if (sourced != null) Text(sourced.readOnlyNotice.text(), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                                else JButton(stringResource(if (creation?.pending?.request?.resumeSessionId == historySession) R.string.agents_history_check_resume else R.string.agents_history_resume),
                                    loading = creation?.starting == true,
                                    enabled = creationModel != null && creation?.initialized == true && creation.starting != true &&
                                        target != null && (creation.pending != null ||
                                        (groups.stateFor(target.machineId, connection) == ConnectionState.Online && page.machines.any { it.id == target.machineId && it.online })),
                                    onClick = { target?.let {
                                        if (it.isLive && creation?.pending == null) {
                                            historyMachine = null
                                            historySession = null
                                            open(it.key)
                                        } else creationModel?.resume(it)
                                    } }, modifier = Modifier.fillMaxWidth())
                            }
                        })
                    if (takeoverClient != null) CodexTakeoverDialogs(historyTakeoverCoordinator)
                    persona.core.designsystem.JToastHost(historyToast, Modifier.align(Alignment.TopCenter).statusBarsPadding().padding(top = persona.core.designsystem.ToastTopBarClearance))
                }
            }
        }
    }
    if (takeoverClient != null) CodexTakeoverDialogs(takeoverCoordinator)
    operationDialog?.let { AgentOperationDialog(it, model, names[it.session.machineId], creationModel?.service,
        page.machines.firstOrNull { m -> m.id == it.session.machineId },
        preferHerdrTabName = page.preferences.prefersHerdrTabName(it.session)) { operationDialog = null } }
    pickingTodoForSession?.let { targetSession ->
        TodoPickerSheet(
            todos = assistant.todosFor(todos, targetSession.machineId,
                targetSession.manager ?: page.machines.firstOrNull { it.id == targetSession.machineId }?.manager),
            title = stringResource(if (targetSession.todo != null) R.string.agents_todo_move_title else R.string.agents_todo_link_title),
            currentTodoId = targetSession.todo?.id,
            sessionLookup = { k -> page.sessions.firstOrNull { it.key == k } },
            onDismiss = { pickingTodoForSession = null },
            onPick = { todo ->
                model.linkTodo(todo.id, targetSession)
                pickingTodoForSession = null
            },
            onCreate = { title ->
                model.createAndLinkTodo(title, targetSession)
                pickingTodoForSession = null
            },
        )
    }
    pickingTodoForSpace?.let { (machineId, space) ->
        val current = todos.forSpace(machineId, space)
        TodoPickerSheet(
            todos = assistant.todosFor(todos, machineId, page.machines.firstOrNull { it.id == machineId }?.manager),
            title = stringResource(if (current != null) R.string.agents_space_todo_move else R.string.agents_space_todo_link),
            currentTodoId = current?.id,
            sessionLookup = { k -> page.sessions.firstOrNull { it.key == k } },
            onDismiss = { pickingTodoForSpace = null },
            onPick = { todo ->
                model.linkSpace(todo.id, machineId, space)
                pickingTodoForSpace = null
            },
            onCreate = { title ->
                model.linkSpace(null, machineId, space, newTitle = title)
                pickingTodoForSpace = null
            },
            unlink = current?.let { todo ->
                stringResource(R.string.agents_space_todo_unlink) to {
                    model.unlinkSpace(todo.id, machineId, space)
                    pickingTodoForSpace = null
                }
            },
        )
    }
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
    if (allRequests) RequestsDialog(requests, names, metadata, now, delivery,
        onDismiss = { allRequests = false }, onOpen = { request ->
            allRequests = false
            requestDetail = request
        }, onAnswer = model::answer,
        history = visibleAgentRequests(requestHistory, page.sessions.map { it.listSession(page.preferences).session },
            page.preferences.mutes, page.settings))
    val approveBlocked = stringResource(R.string.agents_approve_blocked)
    requestDetail?.let { selected ->
        // Every group's requests: a notification's card may belong to a group the list is not showing (#4808).
        val current = everyRequest.firstOrNull { it.id == selected.id && it.sessionKey == selected.sessionKey }
        val request = current ?: selected
        // Answered here: the sheet goes once the answer is delivered, and the page's notice says what was answered.
        val openedWith = remember(selected.id, selected.sessionKey) { delivery[selected.id] }
        LaunchedEffect(openedWith, delivery[selected.id]) {
            if (RequestDeliveries.closesSheet(openedWith, delivery[selected.id])) requestDetail = null
        }
        val (session, machine) = cardContext(request, page.sessions, page.machines, everySession, everyMachine)
        RequestDetail(request, metadata, now, reachable(request), delivery[request.id],
            onDismiss = { requestDetail = null }, onOpenChat = {
                requestDetail = null
                open(it.sessionKey)
            }, onAnswer = model::answer, readOnly = current == null || delivery[request.id] == RequestDelivery.Sent,
            onTerminal = if (session?.canTerminal == false) null else ({
                requestDetail = null
                navigator.navigate(TerminalRoute(request.machineId, request.sessionId))
            }), machine = machine,
            blockedReason = session?.takeIf { !it.canAnswerInteraction(request) }?.let { approveBlocked },
            terminalHelp = session?.canTerminal != false, refusal = commandInputErrors[request.id]?.notice(resources, request))
    }
}
