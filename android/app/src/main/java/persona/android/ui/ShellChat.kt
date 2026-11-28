package persona.android.ui

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Modifier
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.android.navigation.waitingRoute
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.AppFeedback
import persona.core.navigation.*
import persona.feature.chat.*
import persona.feature.files.*
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.flow.flow

/** [terminal]: the session's terminal service (its own hub group's), which the chat's terminal cover reads; null: none. */
@Composable
internal fun ShellChat(route: ChatRoute, session: ShellSession, navigator: Navigator, feedback: AppFeedback, requestId: String? = null,
    onSessionClosed: (SessionKey) -> Unit = {}, onOpenCard: (ResolvedRoute) -> Unit, onGroupGone: (ChatRoute, ChatRoute?) -> Unit,
    terminal: persona.core.contracts.api.TerminalTransport? = session.terminal) {
    // The services of the session's own hub group, which need not be the selected one (docs/android/android.md "Multiple hubs").
    val source = chatWiringSource()
    val activeWiring by remember(source, route.machineId) { source.bindingsFor(SessionKey(route.machineId, route.sessionId)) }
        .collectAsStateWithLifecycle()
    val selectedWiring by source.current.collectAsStateWithLifecycle()
    val wiring = activeWiring?.takeIf { it.scope.path == session.identity || selectedWiring?.scope?.path == session.identity }
    val onClosed by rememberUpdatedState(onSessionClosed)
    var closed by remember(route.machineId, route.sessionId) { mutableStateOf(false) }
    val sourceActions = wiring?.actions ?: session.actions
    val actions = remember(sourceActions) { sourceActions?.let { delegate ->
        object : ChatActions by delegate {
            private fun confirmed(session: persona.core.contracts.Session, action: ChatAction, result: ChatActionResult): ChatActionResult {
                // Closed or deleted: the chat and its routes go (AppNavigator.sessionClosed).
                if ((action == ChatAction.Close || action == ChatAction.Delete) && result == ChatActionResult.Done) {
                    closed = true
                    onClosed(session.key)
                }
                return result
            }
            override suspend fun execute(session: persona.core.contracts.Session, action: ChatAction) =
                confirmed(session, action, delegate.execute(session, action))
            override suspend fun verify(session: persona.core.contracts.Session, action: ChatAction) =
                confirmed(session, action, delegate.verify(session, action))
        }
    } }
    if (session.demo == null && wiring == null) {
        GroupGoneRedirect(route, session, onGroupGone)
        androidx.compose.material3.CircularProgressIndicator()
        return
    }
    val sessions by session.sessions.sessions.collectAsStateWithLifecycle()
    val routingConnection = wiring?.connection ?: session.connection
    val routingState by routingConnection.state.collectAsStateWithLifecycle()
    val routingFeatures = routingConnection.lastHello?.features.orEmpty()
    val home = rememberChildSessionHome(SessionKey(route.machineId, route.sessionId), sessions, actions,
        routingFeatures, routingState == ConnectionState.Online, side = route.side, directSubagent = route.subagentDetail)
    // A link to a side session (#7944: a notification, Needs you, a search hit) opens its parent, then the side over it,
    // once: Back from the side comes to the parent.
    val landedSide = remember(home, sessions, routingFeatures) { home.shownSide(sessions, routingFeatures) }
    var sideShown by rememberSaveable(route.machineId, route.sessionId, requestId) { mutableStateOf(false) }
    LaunchedEffect(landedSide) {
        val side = landedSide ?: return@LaunchedEffect
        if (sideShown) return@LaunchedEffect
        sideShown = true
        val chat = ChatRoute(side.machineId, side.sessionId, ConversationKind.Agent, side = true, decisionSource = route.decisionSource)
        navigator.navigate(requestId?.let { InteractionRoute(chat, it) } ?: chat)
    }
    val key = home.key
    val metadata = (sessions + home.rows).firstOrNull { it.key == key }
    val managerChoice by AssistantManagerChoice.current.collectAsStateWithLifecycle()
    val managerId = metadata?.manager ?: managerChoice.shown
    val assistantManagement = remember(wiring?.assistantManagement, managerId) {
        wiring?.assistantManagement?.forManager(managerId)
    }
    // The hub's leader conversations drive the chat's background-round fold (#6595), scoped to this manager.
    val assistantSnapshot by session.assistant.assistant.collectAsStateWithLifecycle()
    val agentPreferences by remember(session) { session.preferences.snapshots }
        .collectAsStateWithLifecycle(persona.feature.agents.AgentsPreferencesSnapshot())
    val waiting = rememberWaitingRequests(session, sessions)
    val assistant = route.kind == ConversationKind.Assistant || metadata?.assistant == true
    val preferHerdrTabName = !assistant && persona.core.contracts.GroupKeys.namespaceOf(route.machineId) in agentPreferences.herdrTabNameHubs
    val editor = rememberChatDraftEditor(wiring, key)
    val storedComposer by (editor?.composer ?: remember { MutableStateFlow<ChatComposerState?>(null) }).collectAsStateWithLifecycle()
    val draftError by (editor?.error ?: remember { MutableStateFlow<Int?>(null) }).collectAsStateWithLifecycle()
    val composer = storedComposer ?: rememberChatComposerState()
    val focus = androidx.compose.ui.platform.LocalFocusManager.current
    val keyboard = androidx.compose.ui.platform.LocalSoftwareKeyboardController.current
    val pendingChatFocus by persona.feature.chat.ChatComposerFocus.requests.collectAsStateWithLifecycle()
    LaunchedEffect(key, pendingChatFocus, composer) {
        val entry = pendingChatFocus ?: return@LaunchedEffect
        if (!persona.feature.chat.ChatComposerFocus.take(key, entry)) return@LaunchedEffect
        kotlinx.coroutines.delay(300)
        composer.focusRequest++
        kotlinx.coroutines.delay(100)
        keyboard?.show()
    }
    var pane by rememberSaveable { mutableStateOf<FilesPane?>(null) }
    // A path opened from a message or a tool card: the files pane opens straight on it.
    var openedPath by rememberSaveable { mutableStateOf<String?>(null) }
    // Its line (`a.kt:12`): the file opens scrolled to it.
    var openedLine by rememberSaveable { mutableStateOf<Int?>(null) }
    // The terminal comes up over the chat (iOS TerminalCover, #4339 C7), from ⋯, the work line or a request's Open terminal.
    var terminalOpen by rememberSaveable(route.machineId, route.sessionId) { mutableStateOf(false) }
    // Resume in Terminal (#5565): the cover resumes the stopped Agent as it comes up.
    var resumeOnOpen by rememberSaveable(route.machineId, route.sessionId) { mutableStateOf(false) }
    val resumer = remember(session) { SessionTerminalResumer({ session.sessions.sessions.value }, { session.agentsWiring.state.value.creation }, { session.sessions.machines.value }) }
    var insertionFocused by remember { mutableStateOf(false) }
    var git by remember { mutableStateOf<persona.core.contracts.api.SessionGitStatus?>(null) }
    val cwd = metadata?.cwd.orEmpty()
    val connectionState = (wiring?.connection ?: session.connection).state
    LaunchedEffect(key.machineId, cwd, session.files, pane, connectionState) {
        val files = session.files
        if (!assistant && cwd.isNotBlank() && files != null) followGitStatus(connectionState, { files.status(key.machineId, cwd) }) { git = it }
    }
    val filesAvailable = !assistant && cwd.isNotBlank() && session.files != null
    val canOpenFilePath = filesAvailable || (assistant && key.machineId.isNotBlank() && session.files != null)
    fun open(p: FilesPane, path: String? = null, line: Int? = null) {
        insertionFocused = composer.focused; focus.clearFocus(); openedPath = path; openedLine = line; pane = p
    }
    if (editor != null && storedComposer == null) return
    // ultracode's quota line (#582) reads this chat's hub
    androidx.compose.runtime.CompositionLocalProvider(persona.feature.chat.LocalUsageRead provides wiring?.usage) {
        Box(Modifier.fillMaxSize()) {
            // Keep the composer and conversation mounted while browsing the session directory.
            ChatScreen(key, session.chats, session.sender, session.interactions, session.sessions, { if (!closed) navigator.back() },
                onTerminal = if (terminal != null && !assistant) ({ focus.clearFocus(); resumeOnOpen = false; terminalOpen = true }) else null,
                onResumeInTerminal = if (terminal != null && !assistant) ({ focus.clearFocus(); resumeOnOpen = true; terminalOpen = true }) else null,
                chrome = ChatChromeBindings(
                    onFiles = if (filesAvailable) ({ open(FilesPane.Files) }) else null,
                    onGit = if (filesAvailable && git?.isRepo == true) ({ open(FilesPane.Git) }) else null,
                    gitLabel = "Git${git?.files?.size?.takeIf { it > 0 }?.let { " · $it" }.orEmpty()}",
                    actions = actions, connection = wiring?.connection ?: session.connection, requestId = requestId, covered = pane != null || terminalOpen,
                    childSessionId = home.childSessionId.takeIf { landedSide == null },
                    lookupRows = home.rows,
                    worktrees = wiring?.worktrees,
                    assistantManagement = assistantManagement,
                    mainConversation = assistantSnapshot.mainSession?.sessionId,
                    leaderConversations = assistantSnapshot.leaders.toSet(),
                    onOpenSession = { navigator.navigate(ChatRoute(it.machineId, it.sessionId)) },
                    onOpenSubagent = { navigator.navigate(ChatRoute(it.machineId, it.sessionId, ConversationKind.Agent, subagentDetail = true)) },
                    openSubagents = route.openSubagents,
                    onOpenSide = { navigator.navigate(ChatRoute(it.machineId, it.sessionId, ConversationKind.Agent, side = true)) },
                    onFollowSession = { onGroupGone(route, route.copy(machineId = it.machineId, sessionId = it.sessionId)) },
                    waiting = waiting,
                    onOpenRequest = { chat, request ->
                        val target = waitingRoute(chat, request)
                        if (target.card != null) onOpenCard(target) else navigator.navigate(target.key)
                    },
                    decisionSource = route.decisionSource,
                    onOpenDecisionSource = { chat, toolUseId ->
                        navigator.navigate(ChatRoute(chat.machineId, chat.sessionId, ConversationKind.Agent, subagentDetail = true,
                            decisionSource = DecisionSource(toolUseId)))
                    },
                    onOpenAssistant = { navigator.navigate(persona.feature.chat.conversationRoute(it, assistant = true)) },
                    onConnectionDetails = { navigator.navigate(SettingsPageRoute(SettingsPage.Diagnostics)) },
                    files = session.files, hubScope = wiring?.scope?.path ?: session.identity, ports = wiring?.ports,
                    onOpenPath = if (canOpenFilePath) ({ path, line -> open(FilesPane.Files, path, line) }) else null,
                    onOpenTodo = { todoId -> navigator.navigate(persona.core.navigation.TodoDetailRoute(todoId)) },
                    // A model error the hub named on Persona's turn: Fix It opens what the home's readiness card would (MainReadiness.kt).
                    onFixModel = { action, launcher, manager ->
                        navigator.navigate(when (action) {
                            persona.core.designsystem.ReadinessActionId.EditProvider ->
                                launcher?.takeIf(String::isNotBlank)?.let { persona.core.navigation.ModelProviderRoute(it) } ?: SettingsPageRoute(SettingsPage.Providers)
                            persona.core.designsystem.ReadinessActionId.PickModel -> SettingsPageRoute(SettingsPage.ModelConfig)
                            else -> persona.core.navigation.ModelPurposeRoute("assistant", manager)
                        })
                    },
                    // A briefing's recommended setup opens the page its mechanism is turned on at, and changes nothing (#5085).
                    onOpenSetup = { page, manager ->
                        navigator.navigate(when (page) {
                            persona.core.contracts.BriefSetupPage.SessionTriage -> SettingsPageRoute(SettingsPage.Triage)
                            persona.core.contracts.BriefSetupPage.SupervisorModel ->
                                persona.core.navigation.ModelPurposeRoute(persona.core.contracts.api.BackgroundSupervisor.PURPOSE, manager)
                            persona.core.contracts.BriefSetupPage.StandingAuthorizations ->
                                SettingsPageRoute(SettingsPage.StandingAuthorizations)
                        })
                    },
                    preferHerdrTabName = preferHerdrTabName),
                renderStyle = if (assistant) ChatRenderStyle.Assistant else ChatRenderStyle.Agent,
                composerState = composer, composerService = wiring?.composer,
                onDraftChanged = editor?.let { it::changed },
                statusBindings = ChatStatusBindings(recovery = wiring?.recovery, draftSaveError = draftError?.let { androidx.compose.ui.res.stringResource(it) }),
                onContextCleared = { navigator.navigate(ChatRoute(it.machineId, it.sessionId)) })
            if (pane != null && (filesAvailable || (canOpenFilePath && openedPath != null))) {
                BackHandler { pane = null; openedPath = null; openedLine = null }
                CompositionLocalProvider(persona.core.designsystem.LocalSplitDetail provides null) {
                    SessionFilesScreen(route.copy(machineId = key.machineId, sessionId = key.sessionId), cwd.ifBlank { "~" }, requireNotNull(session.files), { pane = null; openedPath = null; openedLine = null }, { path ->
                        composer.value = insertFilePath(composer.value, path, insertionFocused)
                        pane = null
                        openedPath = null
                        openedLine = null
                        composer.focusRequest++
                    }, initialPane = pane!!, initialPath = openedPath, initialLine = openedLine,
                        connection = connectionState)
                }
            }
            if (terminal != null && !assistant) CompositionLocalProvider(persona.core.designsystem.LocalSplitDetail provides null) {
                persona.feature.terminal.TerminalCover(
                    key, terminalOpen, { terminalOpen = false; resumeOnOpen = false }, terminal, resumer, resumeOnOpen = resumeOnOpen)
            }
        }
    }
}

/**
 * The cards agents wait on you with, as the Persona home's "Needs you now" lists them (the same `needsYou`, all of
 * them): a chat that has just answered its last card offers the first one of another chat (#4325).
 */
@Composable
internal fun rememberWaitingRequests(session: ShellSession, sessions: List<persona.core.contracts.Session>): List<WaitingRequest> {
    val pending by session.interactions.pending.collectAsStateWithLifecycle()
    val assistant by session.assistant.assistant.collectAsStateWithLifecycle()
    // `snapshots` builds a new flow on each read: keep one, or the collection restarts on every recomposition.
    val snapshots = remember(session) { session.preferences.snapshots }
    val preferences by snapshots.collectAsStateWithLifecycle(persona.feature.agents.AgentsPreferencesSnapshot())
    return remember(pending, sessions, assistant, preferences.mutes) {
        persona.feature.assistant.needsYou(pending, sessions, preferences.mutes, emptyList(), shown = Int.MAX_VALUE,
            isAssistant = { it == assistant.mainSession || assistant.conversations.any { c -> c.key == it } })
            .lines.filterIsInstance<persona.feature.assistant.NeedsYouLine.Agent>()
            .map { WaitingRequest(it.session, it.requestId, it.who, it.what) }
    }
}

/**
 * The session's hub group went away under the page: it merged into another group (its machines lost their suffix)
 * or was disconnected (docs/android/android.md "Multiple hubs"). The page becomes the same session under its new id, else leaves its stack.
 */
@Composable
private fun GroupGoneRedirect(route: ChatRoute, session: ShellSession, onGroupGone: (ChatRoute, ChatRoute?) -> Unit) {
    val namespace = persona.core.contracts.GroupKeys.namespaceOf(route.machineId) ?: return
    val groups by session.groups.groups.collectAsStateWithLifecycle()
    val sessions by session.sessions.sessions.collectAsStateWithLifecycle()
    if (groups.isEmpty() || groups.any { it.namespace == namespace }) return
    val plain = persona.core.contracts.GroupKeys.raw(route.machineId)
    val moved = sessions.firstOrNull { it.sessionId == route.sessionId && persona.core.contracts.GroupKeys.raw(it.machineId) == plain }
    LaunchedEffect(route, moved?.machineId) {
        // The merged group's list may still be on its way: give the session a moment to show up under its new id.
        if (moved == null) kotlinx.coroutines.delay(2_000)
        onGroupGone(route, moved?.let { ChatRoute(it.machineId, it.sessionId, kind = route.kind) })
    }
}

/**
 * The chat's Git button status: read now, and again each time [states] goes from not-Online back to Online (#4239,
 * as the files pane does since #4211), so a read that failed while the hub was away does not leave the button gone
 * or on an old count. A read still on its way at reconnect is started again. A failed read shows null. Never returns.
 * A StateFlow can fold a quick drop and return into one Online, and nothing is read again then.
 */
internal suspend fun followGitStatus(states: StateFlow<ConnectionState>?, read: suspend () -> SessionGitStatus, show: (SessionGitStatus?) -> Unit) {
    flow {
        emit(Unit)
        states?.let { connection ->
            var was = connection.value
            connection.collect { now ->
                if (now == ConnectionState.Online && was != ConnectionState.Online) emit(Unit)
                was = now
            }
        }
    }.collectLatest {
        show(try { read() } catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) { null })
    }
}
