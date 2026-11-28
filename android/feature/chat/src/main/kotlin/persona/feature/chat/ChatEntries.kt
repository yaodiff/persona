package persona.feature.chat

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.*
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalFocusManager
import androidx.compose.ui.res.stringResource
import androidx.navigation3.runtime.EntryProviderScope
import androidx.navigation3.runtime.NavKey
import dagger.hilt.EntryPoint
import dagger.hilt.InstallIn
import dagger.hilt.android.EntryPointAccessors
import dagger.hilt.components.SingletonComponent
import persona.core.contracts.SessionKey
import persona.core.contracts.Session
import persona.core.contracts.HubScope
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.emptyFlow
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.runtime.snapshots.SnapshotApplyConflictException
import persona.core.contracts.api.*
import persona.core.navigation.SettingsPageRoute
import persona.core.navigation.SettingsPage
import persona.core.navigation.ChatRoute
import persona.core.navigation.Navigator
import persona.core.navigation.TerminalRoute
import persona.core.navigation.ModelProviderRoute
import persona.core.navigation.ModelPurposeRoute
import persona.feature.files.SessionFilesScreen
import persona.feature.files.insertFilePath

@EntryPoint
@InstallIn(SingletonComponent::class)
interface ChatServices {
    fun chats(): ChatRepository
    fun messages(): MessageSender
    fun interactions(): InteractionCenter
    fun sessions(): SessionsRepository
    fun chatWiring(): ChatWiringSource
    fun inboxTrays(): AssistantInboxTrays
    /** The current assistant's snapshot, whose `leaders` drive the leader-conversation background fold (#6595). */
    fun assistantRepository(): AssistantRepository
}

/** Every service in a binding belongs to the same pairing generation. */
data class ChatAppBindings(
    val scope: HubScope,
    val coroutineScope: CoroutineScope,
    val drafts: DraftRepository,
    val connection: HubConnection,
    val actions: ChatActions,
    val composer: ChatComposerService,
    val recovery: ChatRecoveryService,
    val assistantManagement: AssistantManagement,
    val mainSender: MainConversationSender,
    val draftVersions: (SessionKey) -> Flow<DraftSessionVersion> = { emptyFlow() },
    /** Resolve the main conversation only when a local handoff needs its upload machine; it never sends a message. */
    val mainUploadTarget: (suspend () -> SessionKey?)? = null,
    /** Read-only access to the machines (the photos sent with messages load from them); null where there is none. */
    val files: SessionFiles? = null,
    /** The group's machines' ports on the phone (docs/operations/ports.md); null where there is no hub. */
    val ports: persona.core.contracts.api.PortOpener? = null,
    /** Tasks from the assistant's conversations (docs/assistant/unified-entry.md); null where the hub group has none. */
    val tasks: persona.core.domain.tasks.TasksStore? = null,
    val agentShares: AgentSharesProvider? = null,
    /** Worktree status and removal by this group's own machine ids (#4532); built per call for the current route. */
    val worktrees: (() -> persona.core.contracts.api.AgentCreationService?)? = null,
    /** This group's hub's subscription use (`GET /api/v1/usage/windows`), for ultracode's quota line (#582). */
    val usage: (suspend () -> List<persona.core.contracts.api.UsageAccount>)? = null,
    /** Forward to's Recent for this group (#3901): on the phone, ids only, gone with the pairing; null where there is none. */
    val forwardMemory: ForwardMemory? = null,
)

interface ChatWiringSource {
    val current: StateFlow<ChatAppBindings?>
    /** The services of the hub group [session] belongs to (docs/android/android.md "Multiple hubs"); a single hub has only [current]. */
    fun bindingsFor(session: SessionKey): StateFlow<ChatAppBindings?> = current
}

@Composable
fun chatWiringSource(): ChatWiringSource {
    val context = LocalContext.current.applicationContext
    return remember(context) { EntryPointAccessors.fromApplication(context, ChatServices::class.java).chatWiring() }
}

/** Writes drain in the pairing scope even when navigation disposes the editor. */
class ChatDraftEditor(
    repository: DraftRepository, scope: CoroutineScope, key: DraftKey,
    sessionVersions: Flow<DraftSessionVersion>? = null,
) {
    constructor(bindings: ChatAppBindings, session: SessionKey) :
        this(bindings.drafts, bindings.coroutineScope, DraftKey.Chat(session), bindings.draftVersions(session))

    /**
     * There at once, so the chat draws while its draft is read from the database (#4045); the draft fills this same
     * composer when it arrives.
     */
    val composer = MutableStateFlow<ChatComposerState?>(ChatComposerState())
    /** Why the draft could not be read or saved, as a string resource. */
    val error = MutableStateFlow<Int?>(null)
    private val changes = Channel<ChatComposerDraft>(Channel.CONFLATED)
    /** The draft has been read (or could not be): until then what the page reports must never be saved over it. */
    val ready = MutableStateFlow(false)
    /** The composer changed before the draft arrived (typed, or typed and sent): the draft no longer goes into it. */
    @Volatile private var touched = false
    init {
        scope.launch {
            var saved: DraftSnapshot? = null
            val state = checkNotNull(composer.value)
            try {
                val read = repository.read(key)
                saved = read
                // In one snapshot, so it can't land half-way through the page's own edits: something typed in the moment
                // before keeps its place (a write that collides with typing is dropped, and the typing wins).
                try {
                    Snapshot.withMutableSnapshot {
                        if (!touched && !state.hasContent) {
                            if (read.attachments.isEmpty() && !read.hasPickedSavedPrompt) state.editMessage(read.text)
                            else state.load(read.composerDraft())
                            state.sessionVersion = read.sessionVersion
                        }
                    }
                } catch (_: SnapshotApplyConflictException) { }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { error.value = R.string.chat_draft_read_failed }
            ready.value = true
            state.draftLoaded = true
            // What was typed before the draft arrived was not saved then: it is now (it replaces the draft).
            if (touched) changes.trySend(state.draft)
            val versions = sessionVersions?.let { flow -> launch {
                flow.collect { version ->
                    composer.value?.let { state ->
                        state.currentSessionVersion = version
                        if (state.sessionVersion == null) state.sessionVersion = version
                        else if (!state.hasContent) state.sessionVersion = version
                        else if (!state.oldVersion) state.sessionVersion = DraftSessionVersion(
                            state.sessionVersion?.history ?: version.history,
                            state.sessionVersion?.instance ?: version.instance)
                    }
                }
            } }
            try { for (draft in changes) try {
                val previous = saved ?: repository.read(key)
                // Text-only stores keep uploaded attachments as message tags; structured stores keep them as fields.
                val attachments = if (repository.structured) draft.storedAttachments() else emptyList()
                val text = if (repository.structured) draft.text
                    else ChatComposerState(draft).let { if (it.ready) it.message() else draft.text }
                val version = draft.sessionVersion.takeIf { repository.structured && (text.isNotEmpty() || attachments.isNotEmpty()) }
                val picked = repository.structured && draft.hasPickedSavedPrompt
                if (text != previous.text || attachments != previous.attachments || version != previous.sessionVersion ||
                    picked != previous.hasPickedSavedPrompt)
                    saved = repository.save(key, text, previous.version, attachments, version, picked)
                error.value = null
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (_: Exception) { error.value = R.string.chat_draft_save_failed }
            } finally { versions?.cancel() }
        }
    }
    /** A report from before the draft was read, or one already overtaken by the composer, is not saved. */
    fun changed(draft: ChatComposerDraft) {
        if (!ready.value) { if (draft != ChatComposerDraft()) touched = true; return }
        if (draft == composer.value?.draft) changes.trySend(draft)
    }
    fun close() { composer.value?.draft?.let(::changed); changes.close() }
}

@Composable
fun rememberChatDraftEditor(bindings: ChatAppBindings?, session: SessionKey): ChatDraftEditor? {
    val editor = remember(bindings?.scope, session) { bindings?.let { ChatDraftEditor(it, session) } }
    DisposableEffect(editor) { onDispose { editor?.close() } }
    return editor
}

/** Remove the shell's legacy content override to use this destination with existing Hilt bindings. */
fun EntryProviderScope<NavKey>.chatEntries(
    navigator: Navigator,
    content: @Composable (ChatRoute) -> Unit = { route -> ChatDestination(route, navigator) },
) {
    entry<ChatRoute> { route -> content(route) }
}

@Composable
fun ChatDestination(
    route: ChatRoute, navigator: Navigator, onTerminal: ((SessionKey) -> Unit)? = null,
    renderStyle: ChatRenderStyle? = null, embedded: Boolean = false, showTopBar: Boolean = true,
    historyReadOnly: Boolean = false, onHistoryBack: (() -> Unit)? = null,
    historyFooter: (@Composable () -> Unit)? = null,
    historySession: Session? = null,
    /** A Health conversation identified by its host even while its live session row is still loading. */
    healthChat: Boolean = false,
) {
    val context = LocalContext.current.applicationContext
    val services = remember(context) { EntryPointAccessors.fromApplication(context, ChatServices::class.java) }
    val requested = SessionKey(route.machineId, route.sessionId)
    val wiring by remember(services, requested) { services.chatWiring().bindingsFor(requested) }.collectAsStateWithLifecycle()
    if (wiring == null) { androidx.compose.material3.CircularProgressIndicator(); return }
    val metadata by services.sessions().sessions.collectAsStateWithLifecycle()
    val routingState = wiring?.connection?.state?.collectAsStateWithLifecycle()?.value
    val features = wiring?.connection?.lastHello?.features.orEmpty()
    val home = rememberChildSessionHome(requested, metadata, wiring?.actions, features,
        routingState == ConnectionState.Online, side = route.side)
    // A link to a side session (#7944) opens its parent, then the side's own chat over it, once.
    val landedSide = remember(home, metadata, features) { home.shownSide(metadata, features)?.takeUnless { historyReadOnly } }
    var sideShown by androidx.compose.runtime.saveable.rememberSaveable(route.machineId, route.sessionId) { mutableStateOf(false) }
    LaunchedEffect(landedSide) {
        val side = landedSide ?: return@LaunchedEffect
        if (sideShown) return@LaunchedEffect
        sideShown = true
        navigator.navigate(ChatRoute(side.machineId, side.sessionId, persona.core.navigation.ConversationKind.Agent, side = true))
    }
    val session = home.key
    val shownMetadata = metadata + home.rows
    val managerChoice by AssistantManagerChoice.current.collectAsStateWithLifecycle()
    val managerId = shownMetadata.firstOrNull { it.key == session }?.manager ?: managerChoice.shown
    val assistantManagement = remember(wiring?.assistantManagement, managerId) {
        wiring?.assistantManagement?.forManager(managerId)
    }
    // The hub's leader conversations fold their background rounds in the Assistant tab too (#6595): the repository's
    // snapshot follows the selected manager, the same one this assistant surface belongs to.
    val assistantSnapshot by services.assistantRepository().assistant.collectAsStateWithLifecycle()
    val style = renderStyle ?: if (route.kind == persona.core.navigation.ConversationKind.Assistant ||
        shownMetadata.any { it.key == session && it.assistant }) ChatRenderStyle.Assistant else ChatRenderStyle.Agent
    val sharingManager = shownMetadata.firstOrNull { it.key == session }?.manager?.takeIf { it.isNotBlank() }
    val sharing = remember(wiring?.agentShares, sharingManager, session) {
        wiring?.agentShares?.let { provider -> sharingManager?.let { owner -> { provider.forSession(session, owner) } } }
    }?.takeIf { style == ChatRenderStyle.Agent }
    val editor = rememberChatDraftEditor(if (historyReadOnly) null else wiring, session)
    val composer by (editor?.composer ?: remember { MutableStateFlow<ChatComposerState?>(null) }).collectAsStateWithLifecycle()
    val error by (editor?.error ?: remember { MutableStateFlow<Int?>(null) }).collectAsStateWithLifecycle()
    ComposerFocusAskEffect(session, composer)
    if (editor != null && composer == null) return
    val focus = LocalFocusManager.current
    var openedPath by androidx.compose.runtime.saveable.rememberSaveable(route.machineId, route.sessionId) { mutableStateOf<String?>(null) }
    var openedLine by androidx.compose.runtime.saveable.rememberSaveable(route.machineId, route.sessionId) { mutableStateOf<Int?>(null) }
    var insertionFocused by remember(route.machineId, route.sessionId) { mutableStateOf(false) }
    val cwd = shownMetadata.firstOrNull { it.key == session }?.cwd ?: historySession?.takeIf { it.key == session }?.cwd.orEmpty()
    val canOpenFilePath = style == ChatRenderStyle.Assistant && session.machineId.isNotBlank() && wiring?.files != null && composer != null
    fun open(path: String, line: Int?) {
        insertionFocused = composer?.focused == true
        focus.clearFocus()
        openedPath = path
        openedLine = line
    }
    fun close() { openedPath = null; openedLine = null }
    // The next card another chat waits on you with, after answering here (Persona's conversation in the Assistant tab).
    val elsewhere = LocalWaitingElsewhere.current.takeUnless { historyReadOnly }
    androidx.compose.runtime.CompositionLocalProvider(LocalUsageRead provides wiring?.usage) {
        Box(Modifier.fillMaxSize()) {
            ChatScreen(session, services.chats(), services.messages(),
                services.interactions(), services.sessions(), onBack = onHistoryBack ?: navigator::back,
                onTerminal = onTerminal ?: { navigator.navigate(TerminalRoute(it.machineId, it.sessionId)) },
                onResumeInTerminal = { navigator.navigate(TerminalRoute(it.machineId, it.sessionId, resume = true)) },
                chrome = ChatChromeBindings(covered = openedPath != null,
                    actions = wiring?.actions.takeUnless { historyReadOnly },
                    forkActions = wiring?.actions,
                    takeoverActions = wiring?.actions,
                    connection = wiring?.connection,
                    childSessionId = home.childSessionId.takeIf { landedSide == null },
                    lookupRows = home.rows,
                    worktrees = wiring?.worktrees.takeUnless { historyReadOnly },
                    onOpenSession = {
                        if (historyReadOnly) onHistoryBack?.invoke()
                        navigator.navigate(ChatRoute(it.machineId, it.sessionId))
                    },
                    decisionSource = route.decisionSource,
                    onOpenDecisionSource = if (historyReadOnly) null else ({ chat, toolUseId ->
                        navigator.navigate(ChatRoute(chat.machineId, chat.sessionId, persona.core.navigation.ConversationKind.Agent,
                            subagentDetail = true, decisionSource = persona.core.navigation.DecisionSource(toolUseId)))
                    }),
                    onOpenSide = if (historyReadOnly) null else ({ navigator.navigate(ChatRoute(it.machineId, it.sessionId,
                        persona.core.navigation.ConversationKind.Agent, side = true)) }),
                    onOpenAssistant = {
                        if (historyReadOnly) onHistoryBack?.invoke()
                        navigator.navigate(conversationRoute(it, assistant = true))
                    },
                    onFixModel = { action, launcher, manager ->
                        navigator.navigate(when (action) {
                            persona.core.designsystem.ReadinessActionId.EditProvider ->
                                launcher?.takeIf(String::isNotBlank)?.let { ModelProviderRoute(it) } ?: SettingsPageRoute(SettingsPage.Providers)
                            persona.core.designsystem.ReadinessActionId.PickModel -> SettingsPageRoute(SettingsPage.ModelConfig)
                            else -> ModelPurposeRoute("assistant", manager)
                        })
                    },
                    onConnectionDetails = { navigator.navigate(SettingsPageRoute(SettingsPage.Diagnostics)) },
                    files = wiring?.files, hubScope = wiring?.scope?.path.orEmpty(), ports = wiring?.ports,
                    onOpenPath = if (canOpenFilePath) ({ path, line -> open(path, line) }) else null,
                    tasks = wiring?.tasks?.takeIf { style == ChatRenderStyle.Assistant && !historyReadOnly },
                    assistantManagement = assistantManagement,
                    mainConversation = assistantSnapshot.mainSession?.sessionId,
                    leaderConversations = assistantSnapshot.leaders.toSet(),
                    agentShares = sharing,
                    inboxTrays = services.inboxTrays(), inboxScope = wiring?.scope?.path,
                    waiting = elsewhere?.waiting.orEmpty(), onOpenRequest = elsewhere?.open),
                renderStyle = style,
                embedded = embedded, showTopBar = showTopBar,
                historyReadOnly = historyReadOnly, historyFooter = historyFooter, historySession = historySession,
                healthChat = healthChat,
                composerState = composer, onDraftChanged = editor?.let { it::changed },
                composerService = wiring?.composer.takeUnless { historyReadOnly },
                statusBindings = ChatStatusBindings(recovery = wiring?.recovery.takeUnless { historyReadOnly }, draftSaveError = error?.let { stringResource(it) }),
                onContextCleared = { navigator.navigate(ChatRoute(it.machineId, it.sessionId)) })
            if (openedPath != null && canOpenFilePath) {
                BackHandler { close() }
                CompositionLocalProvider(persona.core.designsystem.LocalSplitDetail provides null) {
                    SessionFilesScreen(route.copy(sessionId = session.sessionId), cwd.ifBlank { "~" }, requireNotNull(wiring?.files), ::close, { path ->
                        composer?.let { state ->
                            state.value = insertFilePath(state.value, path, insertionFocused)
                            state.focusRequest++
                        }
                        close()
                    }, initialPath = openedPath, initialLine = openedLine, connection = wiring?.connection?.state)
                }
            }
        }
    }
}
