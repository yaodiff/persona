package persona.feature.assistant

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.layout.positionInRoot
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.contracts.Session
import persona.core.contracts.Machine
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.AssistantRefused
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.AssistantReadinessFeature
import persona.core.contracts.api.ReadinessSuggestion
import persona.core.contracts.api.ReadinessStep
import persona.core.contracts.api.ChannelSources
import persona.core.designsystem.*
import persona.core.navigation.MessageJumps
import persona.core.designsystem.dense
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.icons.PhIcons
import persona.core.contracts.api.MainConversationSender
import persona.core.contracts.api.PendingMainConversation
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.DraftRepository
import persona.core.contracts.api.DraftKey
import persona.core.contracts.api.ChatComposerService
import persona.core.designsystem.row
import persona.core.domain.ConversationTitle
import persona.feature.chat.ChatComposer
import persona.feature.chat.ChatComposerState
import persona.feature.chat.ChatDraftEditor
import persona.feature.chat.MentionFolders
import persona.feature.chat.StarterQuoteCards
import persona.feature.chat.rememberChatComposerState
import persona.feature.chat.rememberStarterMentions
import persona.feature.chat.starterMentionSource
import persona.core.navigation.AppTab
import persona.core.navigation.InboxRoute
import persona.core.navigation.SettingsRoute
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.TodoDetailRoute
import persona.core.navigation.ModelPurposeRoute
import persona.core.navigation.Navigator
import persona.feature.assistant.proactive.AssistantProactiveScreen
import persona.feature.assistant.settings.AssistantSettingsScreen
import persona.feature.assistant.settings.StoredTimezoneChoice
import persona.feature.assistant.settings.followPhoneZone
import persona.feature.assistant.settings.settingsApi
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.first
import java.time.LocalTime
import kotlin.math.roundToInt
import java.util.UUID

/** One assistant's in-flight operation and retry target survive switching away and back. */
private class AssistantSendState {
    val sending = mutableStateOf(false)
    val clientId = mutableStateOf<String?>(null)
    val text = mutableStateOf<String?>(null)
    val launcher = mutableStateOf<String?>(null)
    /** The model and effort picked with it in the model panel (Glass, #5055); null: the launcher's defaults. */
    val model = mutableStateOf<String?>(null)
    val effort = mutableStateOf<String?>(null)
    val started = mutableStateOf<StarterStart?>(null)
    /** A new conversation's first message while it starts, or kept after its start failed (#4339 C18). */
    val first = mutableStateOf<FirstMessage?>(null)
}

/** New chat's blank page (#2804): [from] is the conversation it was opened on, where Back returns; [started] is the
 *  conversation its first message started, kept so a retry after that message failed does not start another;
 *  [space] is the assistant space the chat starts in (`health`, docs/assistant/health.md), null for the assistant's own. */
internal data class NewChatStart(val from: SessionKey?, val started: SessionKey? = null, val space: String? = null)

/** Where Back goes on the assistant page; see [assistantBack]. */
internal sealed interface AssistantBack {
    /** The conversation New chat was opened on. */
    data class Open(val key: SessionKey) : AssistantBack
    /** The main conversation; with none yet, the blank page whose first message starts it. */
    data object Main : AssistantBack
}

/**
 * Where Back goes on the assistant page, as iOS has it: there is no home page (#4339 C11), so Back from any other
 * conversation opens the main one, and Back on New chat's page ([newChat]) returns to the conversation it was opened on.
 * Null on the main conversation ([mainQueue]: its messages still on their way) and on the page that starts it when
 * there is none: Back is the shell's there, and leaves the app.
 */
internal fun assistantBack(active: SessionKey?, main: SessionKey?, newChat: NewChatStart?, mainQueue: Boolean): AssistantBack? = when {
    mainQueue -> null
    active == null -> newChat?.let { page -> page.from?.takeUnless { it == main }?.let(AssistantBack::Open) ?: AssistantBack.Main }
    active == main -> null
    else -> AssistantBack.Main
}

/** Supply the shared chat content when available, or use the shell's ChatRoute entry. */
@Composable
fun AssistantScreen(
    navigator: Navigator,
    repository: AssistantRepository,
    messageSender: MessageSender,
    startConversation: suspend () -> SessionKey,
    connectionLabel: String? = null,
    hubOnline: Boolean = true,
    chatContent: (@Composable (SessionKey) -> Unit)? = null,
    demo: Boolean = false,
    machines: List<Machine> = LocalAssistantHost.current?.machines.orEmpty(),
    phoneToolsAvailable: Boolean? = LocalAssistantHost.current?.phoneToolsAvailable,
    management: AssistantManagement? = LocalAssistantHost.current?.management,
    assistantChatContent: (@Composable (SessionKey, AssistantChatOptions) -> Unit)? = LocalAssistantHost.current?.chatContent,
    requestedSession: SessionKey? = null,
    mainConversationSender: MainConversationSender? = LocalAssistantHost.current?.mainSender ?: messageSender as? MainConversationSender,
    onKnowledge: (() -> Unit)? = null,
    draftRepository: DraftRepository? = LocalAssistantHost.current?.drafts,
    draftScope: CoroutineScope? = LocalAssistantHost.current?.coroutineScope,
    composerService: ChatComposerService? = assistantComposerService(),
    /** The launcher's "Ask Persona": put the cursor in the composer and bring up the keyboard. */
    composerFocus: ComposerFocusRequests = AssistantComposerFocus,
    /** "Needs you now" over the blank page's suggestions; [onOpenNeed] opens a line where it lives (none: not shown). */
    needsYou: NeedsYou = NeedsYou(),
    /**
     * On Glass, what needs the user over the blank page's composer instead (#4339 C17, [needsYouBySession]): a line per
     * session the Agents tab's badge counts, then the inbox's, all of them; the page shows as many as fit.
     */
    shellNeedsYou: List<NeedsYouLine> = emptyList(),
    onOpenNeed: ((NeedsYouLine) -> Unit)? = null,
    /** "N more in the inbox": opens the inbox as a secondary page. */
    onOpenInbox: (() -> Unit)? = null,
    /** "Voice chat" in the title menu, when the hub runs a voice extension. */
    onVoice: (() -> Unit)? = null,
    onTodos: (() -> Unit)? = null,
    todoCount: Int = 0,
    showTodos: Boolean = true,
    /** A new conversation on a launcher picked for it alone (the blank page composer's "Launcher" chip); null: no chip. */
    startConversationWith: (suspend (String) -> SessionKey)? =
        (repository as? persona.core.contracts.api.AssistantConversations)?.let { conversations -> { launcher -> conversations.startConversation(launcher) } },
    /** A new conversation on the launcher, model and effort picked in the model panel (Glass, #5055, `{launcher, model, effort}`). */
    startConversationPicked: (suspend (persona.core.contracts.ModelPick) -> SessionKey)? =
        (repository as? persona.core.contracts.api.AssistantConversations)?.let { conversations -> { pick -> conversations.startConversation(pick) } },
    /** The hub's link: while it is not up, the title menu offers Connection details (iOS); null: never. */
    connection: persona.core.contracts.api.ConnectionState? = null,
    /** The sessions New chat's @ lists (every computer's, iOS `StarterComposer`); null: no @ on New chat's page. */
    mentionSessions: StateFlow<List<Session>>? = null,
    /** Reads the assistant's computer's folders for a path typed after @ on New chat's page; null: sessions only. */
    mentionFiles: persona.core.contracts.api.SessionFiles? = assistantSessionFiles(),
    /** The selected hub group's name while several are paired, which ends the title menu's heading as on iOS; null with one. */
    hubGroup: String? = null,
) = PersonaTheme {
    val receivedSnapshot by repository.assistant.collectAsStateWithLifecycle()
    val conversationsOf = repository as? persona.core.contracts.api.AssistantConversations
    // The page shows the current assistant (docs/assistant/managers.md "Clients"), picked in the sidebar's switcher.
    val reportedRoster = LocalManagerRoster.current
    val managerRoster = if (hubOnline) reportedRoster else reportedRoster?.copy(
        managers = reportedRoster.managers.map { it.copy(online = false) },
    )
    val managerChoice by persona.core.contracts.api.AssistantManagerChoice.current.collectAsStateWithLifecycle()
    // A late repository emission is never shown under another assistant.
    val snapshot = receivedSnapshot.takeIf { it.managerId == managerChoice.shown }
        ?: persona.core.contracts.api.AssistantSnapshot(manager = managerChoice.manager, managerId = managerChoice.shown)
    val pageManagement = remember(management, managerChoice.hubId, managerChoice.shown) {
        management?.forManager(managerChoice.shown)
    }
    val managers = assistantManagers(managerRoster, managerChoice, androidx.compose.ui.platform.LocalResources.current)
    // One name source with the sidebar's switcher (#6401): the current manager's name. The persona pack's name is the
    // fallback only before the roster is read or on a hub without managers, where the default manager is that Persona.
    val assistantName = managerRoster.assistantDisplayName(managerChoice.shown, LocalPersonaName.current)
    val outbox by (mainConversationSender?.outbox ?: messageSender.outbox).collectAsStateWithLifecycle()
    val receivedSendReceipts by key(mainConversationSender, messageSender, managerChoice.hubId, managerChoice.shown) {
        (mainConversationSender?.sendReceipts ?: messageSender.sendReceipts).collectAsStateWithLifecycle()
    }
    val mainReceipts = receivedSendReceipts.filter { receipt -> receipt.session == snapshot.mainSession ||
        (managerChoice.manager == null && receipt.mainTarget && outbox.any { it.mainTarget &&
            (it.session == receipt.session || (receipt.session == PendingMainConversation && it.clientMessageId == receipt.clientMessageId)) }) }
    val mainMessages = if (managerChoice.manager == null) outbox.filter { (it.mainTarget || it.session == PendingMainConversation) && (it.session == PendingMainConversation || it.session == snapshot.mainSession) } else emptyList()
    val scope = rememberCoroutineScope()
    val sharedSidebar = LocalAssistantSidebarHost.current
    val drawerState: MutableState<Boolean> = sharedSidebar?.opened ?: rememberSaveable { mutableStateOf(false) }
    var drawerOpen by drawerState
    var sidebarVisible by rememberSaveable { mutableStateOf(true) }
    // A fallback for when no app feedback is provided (previews, tests): the page's own toast, as the root's.
    val toast = persona.core.designsystem.rememberToastState()
    val feedback = persona.core.designsystem.LocalAppFeedback.current
    val haptics = persona.core.designsystem.rememberHaptics()
    val archivedWords = stringResource(R.string.assistant_archived)
    var query by rememberSaveable(managerChoice.hubId, managerChoice.shown) { mutableStateOf("") }
    val transientComposer = key(managerChoice.hubId, managerChoice.shown) { rememberChatComposerState() }
    // Keep an in-flight editor alive when its assistant is hidden, so its successful send can clear only that draft.
    val draftEditors = remember(draftRepository, draftScope) { mutableMapOf<Pair<String?, String>, ChatDraftEditor>() }
    val draftEditor = if (draftRepository != null && draftScope != null) {
        draftEditors.getOrPut(managerChoice.hubId to managerChoice.shown) {
            ChatDraftEditor(draftRepository, draftScope, DraftKey.Assistant(managerChoice.shown.takeUnless { it == persona.core.contracts.api.ManagerRoster.ASSISTANT_MANAGER }?.let { "main@$it" } ?: "main"))
        }
    } else null
    DisposableEffect(draftEditors) { onDispose { draftEditors.values.forEach { it.close() } } }
    val savedComposer by key(draftEditor) {
        (draftEditor?.composer ?: remember { MutableStateFlow<ChatComposerState?>(null) }).collectAsStateWithLifecycle()
    }
    val draftError by key(draftEditor) {
        (draftEditor?.error ?: remember { MutableStateFlow<Int?>(null) }).collectAsStateWithLifecycle()
    }
    val composer = savedComposer ?: transientComposer
    val draftRead by key(draftEditor) { (draftEditor?.ready ?: remember { MutableStateFlow(true) }).collectAsStateWithLifecycle() }
    val draftReady = draftEditor == null || draftRead
    LaunchedEffect(draftEditor, savedComposer) {
        savedComposer?.let { state -> snapshotFlow { state.draft }.collect { draftEditor?.changed(it) } }
    }
    val sends = remember(draftRepository, draftScope) { mutableMapOf<Pair<String?, String>, AssistantSendState>() }
    // A failed first message is kept on the device per assistant (FirstMessage.kt), as iOS keeps it across launches.
    val appContext = androidx.compose.ui.platform.LocalContext.current.applicationContext
    val failedStarts = remember(appContext) { FailedStarts(appContext) }
    val firstPage = "${managerChoice.hubId.orEmpty()}/${managerChoice.shown}"
    val sendState = sends.getOrPut(managerChoice.hubId to managerChoice.shown) {
        AssistantSendState().apply { first.value = failedStarts.read(firstPage).takeUnless { demo } }
    }
    var sending by sendState.sending
    var firstMessage by sendState.first
    var activeMachine by rememberSaveable(managerChoice.hubId, managerChoice.shown) { mutableStateOf<String?>(null) }
    var activeSession by rememberSaveable(managerChoice.hubId, managerChoice.shown) { mutableStateOf<String?>(null) }
    var viewingMainQueue by rememberSaveable(managerChoice.hubId, managerChoice.shown) { mutableStateOf(false) }
    var pendingClientId by sendState.clientId
    var pendingText by sendState.text
    var openedMain by rememberSaveable(managerChoice.hubId, managerChoice.shown) { mutableStateOf(false) }
    var proactiveOpen by rememberSaveable(managerChoice.hubId, managerChoice.shown) { mutableStateOf(false) }
    var settingsOpen by rememberSaveable(managerChoice.hubId, managerChoice.shown) { mutableStateOf(false) }
    // Per manager, so switching manager leaves New chat's page.
    var newChat by remember(managerChoice.hubId, managerChoice.shown) { mutableStateOf<NewChatStart?>(null) }
    val context = androidx.compose.ui.platform.LocalContext.current
    var ending by remember { mutableStateOf<Session?>(null) }
    var endingBusy by remember { mutableStateOf(false) }
    // The 设为常驻 / 取消常驻 menu asks before the hub call (#6214).
    var leaderConfirm by remember { mutableStateOf<Session?>(null) }
    var ended by remember(pageManagement) { mutableStateOf(emptySet<SessionKey>()) }
    var archived by remember(pageManagement) { mutableStateOf(emptySet<SessionKey>()) }
    // Rename… and Archived (N) (iOS `AssistantSidebar`): titles given here and the archived conversations.
    val history = rememberConversationHistory(pageManagement?.takeUnless { demo }, repository)
    var assistantMachine by remember(pageManagement) { mutableStateOf("") }
    // The launcher the next new conversation starts with, for it alone (iOS StarterComposer); null: the default.
    // Both belong to this hub: another one has other launchers and conversations.
    var starterLauncher by sendState.launcher
    var starterModel by sendState.model
    var starterEffort by sendState.effort
    // The sessions picked in New chat's @ list, to go in front of its first message (iOS `StarterDraft.quotes`).
    val starterMentions = rememberStarterMentions(managerChoice.hubId, managerChoice.shown)
    // The conversation that launcher started for a message that then failed to send: its retry goes there.
    var starterStarted by sendState.started
    var launchers by remember(pageManagement, assistantMachine) { mutableStateOf<List<persona.core.contracts.api.AgentLauncher>?>(null) }
    var launchersFailed by remember(pageManagement, assistantMachine) { mutableStateOf(false) }
    var defaultLauncher by remember(pageManagement) { mutableStateOf<String?>(null) }
    val hasChat = chatContent != null || assistantChatContent != null
    // Ask Persona's cursor for the main conversation's composer, inside the shared chat page.
    val focusAsk = remember { persona.feature.chat.ComposerFocusAsk() }
    var menu by remember { mutableStateOf(false) }
    var info by rememberSaveable { mutableStateOf<AssistantInfo?>(null) }
    val copy = rememberCopy()
    val resources = androidx.compose.ui.platform.LocalResources.current
    val locale = currentLocale
    val words = conversationWords()
    fun say(message: String) {
        if (feedback?.notice(message) != true) toast.show(persona.core.designsystem.FeedbackNotice(message))
    }
    fun copyId(key: SessionKey) {
        copy(key.sessionId, resources.getString(R.string.assistant_copied_session_id))
    }
    fun copyDiagnostics(session: Session) {
        copy(conversationDiagnostics(session), resources.getString(R.string.assistant_diagnostics_copied))
    }
    val active = activeSession?.let { SessionKey(activeMachine.orEmpty(), it) }
    // The health space (docs/assistant/health.md, #4329): its chats as last read, and every chat known to be one, so New
    // on a health chat opens the health space's new-chat page.
    var healthChats by remember(managerChoice.hubId) { mutableStateOf<List<Session>?>(null) }
    var healthKeys by remember(managerChoice.hubId) { mutableStateOf(emptySet<SessionKey>()) }
    var healthBusy by remember { mutableStateOf(false) }
    val healthPage = active == null && newChat?.space == persona.core.contracts.api.AssistantSpace.HEALTH
    // New chat's page takes @ (iOS: the shell's starter composer; Health's has none). Its picked sessions go with its message.
    val mentionsHere = active == null && !viewingMainQueue && !healthPage && mentionSessions != null
    val starterQuotes = if (mentionsHere) starterMentions.quotes else emptyList()
    val onHealthChat = active != null && active in healthKeys
    // "Compact context" and "New main chat" (#1727): the main conversation's menu, on hubs that offer them.
    val mainActions = if (pageManagement != null && !demo) MainActions.of(LocalHubFeatures.current) else MainActions(false, false)
    // What the main conversation still needs (docs/assistant/onboarding.md 4.4), on hubs that work it out: asked when the tab
    // shows, when a computer comes or goes and after the hub refused to start it.
    val readinessOn = pageManagement != null && !demo && AssistantReadinessFeature in LocalHubFeatures.current
    val mainReadiness = remember(pageManagement) { MainReadiness { probe -> requireNotNull(pageManagement).readiness(probe) } }
    var readinessHelp by remember { mutableStateOf<ReadinessHelp?>(null) }
    val machinesSeen = machines.joinToString(",") { "${it.id}:${it.online}" }
    LaunchedEffect(mainReadiness, readinessOn, machinesSeen) { if (readinessOn) mainReadiness.refresh() }
    androidx.lifecycle.compose.LifecycleEventEffect(androidx.lifecycle.Lifecycle.Event.ON_RESUME) {
        if (readinessOn) scope.launch { mainReadiness.refresh() }
    }
    val refusedMain = mainMessages.filter { it.setupRefusal() != null }.map { it.clientMessageId }
    LaunchedEffect(mainReadiness, readinessOn, refusedMain) { if (readinessOn && refusedMain.isNotEmpty()) mainReadiness.refresh() }
    val readiness = mainReadiness.readiness.takeIf { readinessOn }
    val blocking = mainReadiness.blocking.takeIf { readinessOn }
    fun runReadinessAction(action: ReadinessActionId, launcher: String? = blocking?.launcher ?: readiness?.launcher) {
        action.help?.let { readinessHelp = it; return }
        if (action == ReadinessActionId.TestAssistant) { scope.launch { mainReadiness.refresh(probe = true) }; return }
        val machine = readiness?.steps?.firstOrNull { it.id == ReadinessStep.COMPUTER }?.machine
        action.destination(launcher, machine, managerChoice.shown)?.let(navigator::navigate)
    }
    val onMain = active != null && active == snapshot.mainSession
    val shownCompaction = if (onMain) snapshot.conversations.firstOrNull { it.key == active }?.compaction else null
    var compaction by remember(active) { mutableStateOf(CompactionStatus()) }
    var compacting by remember { mutableStateOf(false) }
    var newMainOpen by remember { mutableStateOf(false) }
    var newMainBusy by remember { mutableStateOf(false) }
    var carrySummary by rememberSaveable { mutableStateOf(true) }
    // Clear context (#5100, the web's #5095): the main conversation it cleared, while none has started since. Then the page
    // stays a conversation page: an empty middle over the composer, where the first message starts the new main
    // conversation, not the centered start page. This page and assistant only; another main conversation forgets it.
    var clearBusy by remember { mutableStateOf(false) }
    var clearedMain by remember(managerChoice.hubId, managerChoice.shown) { mutableStateOf<SessionKey?>(null) }
    LaunchedEffect(snapshot.mainSession, clearedMain) {
        if (snapshot.mainSession != null && snapshot.mainSession != clearedMain) clearedMain = null
    }
    val clearedPage = clearedMain != null && active == null && newChat == null && !viewingMainQueue
    LaunchedEffect(active, shownCompaction) { compaction = compaction.observe(shownCompaction) }
    LaunchedEffect(compaction.line) {
        if (compaction.line == CompactionLine.Done) {
            kotlinx.coroutines.delay(CompactionDoneMillis)
            compaction = compaction.dismissed()
        }
    }
    val groups = remember(snapshot, query, ended, archived, words, history.titles) {
        conversationGroups(snapshot.copy(conversations = history.titled(snapshot.conversations.filter { it.key !in archived }.map {
            if (it.key in ended) it.copy(status = "offline", state = "offline") else it
        })), query, words)
    }
    val searchOn = pageManagement != null && !demo && AssistantSearchFeature in LocalHubFeatures.current
    val knownChats = remember(snapshot.conversations, ended, archived, history.titles, history.archived) {
        val open = history.titled(snapshot.conversations.filter { it.key !in archived }.map {
            if (it.key in ended) it.copy(status = "offline", state = "offline") else it
        })
        (open + history.shownArchived(openKeys(open))).distinctBy { it.sessionId }
    }
    val (foundChats, searchPartial) = rememberFoundChats(
        query, remember(groups) { (groups.directions + groups.others).distinctBy { it.sessionId } },
        knownChats, searchOn, pageManagement, "${managerChoice.hubId.orEmpty()}|${managerChoice.shown}")
    // Persona's face (top bar and the conversation list) shows the conversation on screen, as on iOS: none on the
    // new-conversation page.
    val faceState = personaState(active?.let { key -> snapshot.conversations.firstOrNull { it.key == key } }, hubOnline,
        machines, assistantMachine)
    SideEffect { sharedSidebar?.personaState = faceState; sharedSidebar?.active = active }
    fun openConversation(key: SessionKey) {
        newChat = null
        if (key == PendingMainConversation) {
            viewingMainQueue = true
            return
        }
        openedMain = true
        viewingMainQueue = false
        ended = ended - key
        activeMachine = key.machineId
        activeSession = key.sessionId
        if (!hasChat) {
            navigator.selectTab(AppTab.Persona)
            navigator.navigate(ChatRoute(key.machineId, key.sessionId))
        }
    }
    fun clearConversation() {
        openedMain = true
        viewingMainQueue = false
        activeMachine = null
        activeSession = null
        pendingClientId = null
        pendingText = null
    }
    // The blank page with no New chat on it: with no main conversation yet, the page whose first message starts it.
    fun newConversation() {
        if (sending) return
        newChat = null
        clearConversation()
        scope.launch { drawerOpen = false }
    }
    // There is no home page (#4339 C11, as iOS): Back, "Back to main chat", a closed or put-away conversation and Ask
    // Persona land on the main conversation; with none yet, on the page that starts it (or its messages on their way).
    fun openMain() {
        snapshot.mainSession?.let(::openConversation) ?: run {
            newConversation()
            if (mainMessages.isNotEmpty()) viewingMainQueue = true
        }
    }
    fun refusal(error: Exception): String =
        if (error is AssistantRefused) error.message.orEmpty() else persona.core.designsystem.operationFeedback(error).text.resolve(resources)
    fun compactMain() {
        val key = active ?: return
        val api = pageManagement ?: return
        if (compacting) return
        compacting = true
        compaction = compaction.asked(shownCompaction)
        scope.launch {
            try { api.compactConversation(key) }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                compaction = compaction.refused(snapshot.conversations.firstOrNull { it.key == key }?.compaction)
                say(refusal(error))
            }
            finally { compacting = false }
        }
    }
    fun newMain() {
        val api = pageManagement ?: return
        val previous = snapshot.mainSession ?: return
        if (newMainBusy) return
        newMainBusy = true
        val carry = carrySummary
        scope.launch {
            try {
                val result = api.newMainConversation(carry, previous.sessionId)
                newMainOpen = false
                // The next message to the main conversation starts the new one, here and from every other entry.
                assistantAttempt { mainConversationSender?.mainReplaced(previous) }
                assistantAttempt { repository.refresh() }
                newConversation()
                say(resources.getString(newMainNotice(result.carried, carry)))
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                newMainOpen = false
                if ((error as? AssistantRefused)?.code == "main_changed") {
                    // Another device put it away first: show the main conversation the hub has now.
                    assistantAttempt { mainConversationSender?.mainReplaced(previous) }
                    assistantAttempt { repository.refresh() }
                }
                say(refusal(error))
            } finally {
                newMainBusy = false
            }
        }
    }
    // No confirmation, as on the web and Cindy's /clear: nothing is deleted, the old conversation is archived. [onFailed]: the
    // composer's /clear goes back into the box.
    fun clearMain(onFailed: () -> Unit = {}) {
        val api = pageManagement ?: return
        val previous = snapshot.mainSession?.takeIf { it == active } ?: return
        if (clearBusy) return
        clearBusy = true
        // Before the request: the hub's update without the main conversation may come before its answer.
        clearedMain = previous
        fun shown() = activeSession?.let { SessionKey(activeMachine.orEmpty(), it) }
        scope.launch {
            try {
                api.clearMainConversation(previous.sessionId)
                // The next message to the main conversation starts a new one, here and from every other entry.
                assistantAttempt { mainConversationSender?.mainReplaced(previous) }
                assistantAttempt { repository.refresh() }
                if (shown() == previous) newConversation()
            } catch (cancelled: CancellationException) {
                clearedMain = null
                throw cancelled
            } catch (error: Exception) {
                clearedMain = null
                if ((error as? AssistantRefused)?.code == "main_changed") {
                    // Another device replaced or cleared it first: show the main conversation the hub has now, as New main chat does.
                    assistantAttempt { mainConversationSender?.mainReplaced(previous) }
                    assistantAttempt { repository.refresh() }
                    val now = repository.assistant.value.takeIf { it.managerId == managerChoice.shown }?.mainSession
                    if (shown() == previous) now?.takeUnless { it == previous }?.let(::openConversation) ?: newConversation()
                    say(refusal(error))
                } else {
                    onFailed()
                    val failed = resources.getString(R.string.assistant_clear_context_failed, refusal(error))
                    if (feedback?.notice(failed, persona.core.designsystem.FeedbackStyle.Error) != true)
                        toast.show(persona.core.designsystem.FeedbackNotice(failed, style = persona.core.designsystem.FeedbackStyle.Error))
                }
            } finally {
                clearBusy = false
            }
        }
    }
    // Hidden at once; after the hub's list is refreshed (it leaves archived ones out) the hub's word is the one kept,
    // so a later message that brings the conversation back shows it again.
    fun archive(session: Session) {
        val target = pageManagement ?: return
        archived = archived + session.key
        // As iOS: a tap now, "Archived" once the hub has it (DESIGN_SYSTEM.md 4.3).
        haptics(persona.core.designsystem.Haptic.Tap)
        scope.launch {
            try {
                target.archiveConversation(session.key)
                if (feedback?.notice(archivedWords, persona.core.designsystem.FeedbackStyle.Success) != true)
                    toast.show(persona.core.designsystem.FeedbackNotice(archivedWords, style = persona.core.designsystem.FeedbackStyle.Success))
                // The main conversation in place of the archived one; keep the list open for the next archive.
                if (activeMachine == session.machineId && activeSession == session.sessionId) openMain()
                assistantAttempt { repository.refresh() }.onSuccess { archived = archived - session.key }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                archived = archived - session.key
                if (feedback?.failure(error) != true) toast.show(persona.core.designsystem.FeedbackNotice(persona.core.designsystem.operationFeedback(error).text.resolve(resources), style = persona.core.designsystem.FeedbackStyle.Error))
            }
        }
    }
    // A chat is made a leader (常驻) or stops being one on the hub, for every device (#4331): the list is read again
    // after, and a refusal says why.
    fun setLeader(session: Session) {
        val target = pageManagement ?: return
        val leader = session.sessionId in snapshot.leaders
        scope.launch {
            try {
                if (leader) target.endDirection(session.key) else target.makeDirection(session.key)
                assistantAttempt { repository.refresh() }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) {
                if (feedback?.failure(error) != true) toast.show(persona.core.designsystem.FeedbackNotice(persona.core.designsystem.operationFeedback(error).text.resolve(resources), style = persona.core.designsystem.FeedbackStyle.Error))
            }
        }
    }
    fun startNewConversation() {
        if (sending) return
        if (snapshot.mainSession == null) {
            newConversation()
            return
        }
        // A blank page only (#2804): the chat starts with its first message (submit), so New chat left without one
        // leaves no empty chat on the hub. Back returns to the conversation it was tapped on.
        val from = newChat?.from ?: active
        newConversation()
        newChat = HealthEntry.newChat(from, health = false)
    }
    // The health space's new-chat page: its first message starts a health chat, never the main one.
    fun startHealthConversation() {
        if (sending) return
        val from = newChat?.from ?: active
        newConversation()
        newChat = HealthEntry.newChat(from, health = true)
    }
    suspend fun loadHealth(): List<Session> {
        val chats = requireNotNull(conversationsOf).spaceConversations(persona.core.contracts.api.AssistantSpace.HEALTH)
        healthChats = chats
        healthKeys = healthKeys + chats.map { it.key }
        return chats
    }
    // The sidebar's Health: the newest health chat, else the health space's new-chat page.
    fun openHealth() {
        if (sending || healthBusy || conversationsOf == null) return
        healthBusy = true
        scope.launch {
            try {
                when (val target = HealthEntry.resolve(loadHealth())) {
                    is HealthEntry.Target.Open -> openConversation(target.key)
                    HealthEntry.Target.New -> startHealthConversation()
                }
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                if (feedback?.failure(error) != true) toast.show(persona.core.designsystem.FeedbackNotice(resources.getString(R.string.assistant_health_load_failed), style = persona.core.designsystem.FeedbackStyle.Error))
            } finally {
                healthBusy = false
            }
        }
    }
    /** Why a new conversation did not start: what is missing for a coded refusal, else "Can't start a chat right now". */
    suspend fun newChatRefused(error: Exception): String {
        // A coded refusal (no computer, Claude Code missing…) says what is missing instead of "Can't start a chat right now".
        val coded = persona.core.designsystem.operationFeedback(error).takeIf { it.action.readinessAction() != null }
        if (coded != null && readinessOn) mainReadiness.refresh()
        return coded?.text?.resolve(context) ?: resources.getString(R.string.assistant_new_conversation_failed)
    }
    /** [resend]: the failed first message's Resend, in place of what the composer holds. */
    fun submit(resend: FirstMessage? = null) {
        // Not ready (setup): the step opens instead and the draft stays; nothing is queued (docs/assistant/onboarding.md 4.4).
        if (active == null && blocking != null) { blocking.primaryAction()?.let { runReadinessAction(it) }; return }
        // An offline manager takes nothing: its page is history only until it is back.
        if (managers.offline != null) return
        if (sending || !draftReady) return
        // A failed first message holds the blank page until it is sent again or deleted (iOS).
        if (resend == null && (active == null && firstMessage != null || !composer.hasContent && starterQuotes.isEmpty() || !composer.ready)) return
        val sentDraft = composer.draft.takeIf { resend == null }
        val sentQuotes = starterQuotes.takeIf { resend == null }.orEmpty()
        // The sessions picked with @ go in front of the words as <persona-context> tags, as in a chat (iOS `StarterDraft.message`).
        val text = resend?.text ?: if (sentQuotes.isEmpty()) composer.message() else starterMentions.message(composer.message(), resources)
        // A launcher picked on the new-conversation page: a conversation of its own on it, then the message to it.
        val launcher = if (resend != null) resend.launcher else starterLauncher.takeIf { active == null && newChat?.space == null }
        val startWith = startConversationWith
        // New chat's page: the message starts that chat and never goes to the main one. A resend starts one again
        // (in the space it was sent from), whichever blank page shows it.
        val fresh = if (resend != null) newChat?.takeIf { active == null && it.space == resend.space } ?: NewChatStart(newChat?.from, space = resend.space)
            else newChat.takeIf { active == null }
        // Capture the concrete owner before any lookup; a later default change cannot retarget this message.
        val forManager = managerChoice.manager
        val submittedManager = managerChoice.shown
        val shownSnapshot = snapshot
        sending = true
        val submitted = text.trim()
        openedMain = true
        // A new conversation's first message: its bubble and "Sending…" stand on the page while the conversation starts,
        // and the composer is empty (iOS `FirstMessagePending`).
        val startsOne = (launcher != null && startWith != null || fresh != null) && active == null
        val page = firstPage
        if (startsOne) {
            firstMessage = FirstMessage(submitted, space = fresh?.space, launcher = launcher)
            failedStarts.write(page, null)
            if (sentDraft != null) { composer.clearIfUnchanged(sentDraft); draftEditor?.changed(composer.draft) }
            starterMentions.sent(sentQuotes)
        }
        // A start that failed keeps the message as a failed bubble with why, Resend and Delete (iOS #1368).
        fun failed(reason: String) {
            firstMessage = FirstMessage(submitted, failed = true, reason = reason, space = fresh?.space, launcher = launcher)
                .also { failedStarts.write(page, it) }
        }
        if (pendingClientId == null || pendingText != submitted) {
            pendingClientId = UUID.randomUUID().toString()
            pendingText = submitted
        }
        val operationId = requireNotNull(pendingClientId)
        val submittedChoice = managerChoice
        scope.launch {
            try {
                // Started once: a retry after the message failed sends to the same new conversation, while the
                // launcher is still the one that started it.
                val target = when {
                    launcher != null && startWith != null -> {
                        // a model or an effort picked in the panel goes with the start (#5055); a launcher alone as before
                        val picked = persona.core.contracts.ModelPick(launcher, starterModel, starterEffort)
                        val start: suspend (String) -> SessionKey = { l ->
                            if ((picked.model != null || picked.effort != null) && startConversationPicked != null) startConversationPicked(picked.copy(launcher = l))
                            else startWith(l)
                        }
                        starterTarget(launcher, starterStarted, start).also { starterStarted = it }.session
                    }
                    fresh != null -> fresh.started ?: try {
                        val space = fresh.space
                        if (space != null) requireNotNull(conversationsOf).startConversationIn(space).also {
                            newChat = fresh.copy(started = it)
                            healthKeys = healthKeys + it
                        } else startConversation().also { newChat = fresh.copy(started = it) }
                    } catch (cancelled: CancellationException) {
                        throw cancelled
                    } catch (error: Exception) {
                        failed(newChatRefused(error))
                        return@launch
                    }
                    // Resolve this owner explicitly; legacy repositories retain the default main queue.
                    else -> active ?: if (conversationsOf != null) {
                        managerMainTarget(submittedManager, shownSnapshot, conversationsOf::mainConversationOf)
                    } else forManager?.let { error("managers unavailable") }
                }
                sendAssistantMessage(mainConversationSender, messageSender, target, submitted, operationId)
                // Clear the submitted assistant's editor after commit, even while another assistant is shown.
                if (sentDraft != null) {
                    composer.clearIfUnchanged(sentDraft)
                    draftEditor?.changed(composer.draft)
                }
                starterMentions.sent(sentQuotes)
                // The conversation takes the message over: its outbox bubble and "Persona is getting ready…".
                if (startsOne) firstMessage = null
                pendingClientId = null
                pendingText = null
                if (persona.core.contracts.api.AssistantManagerChoice.current.value != submittedChoice) return@launch
                if (launcher != null && target != null) {
                    starterLauncher = null
                    starterModel = null
                    starterEffort = null
                    starterStarted = null
                    assistantAttempt { repository.refresh() }
                    openConversation(target)
                } else if (target != null) {
                    openConversation(target)
                } else if (active == null) viewingMainQueue = true
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                if (startsOne) failed(persona.core.designsystem.operationFeedback(error).text.resolve(resources))
                else if (feedback?.failure(error) != true)
                    toast.show(persona.core.designsystem.FeedbackNotice(persona.core.designsystem.operationFeedback(error).text.resolve(resources), style = persona.core.designsystem.FeedbackStyle.Error))
            } finally {
                sending = false
            }
        }
    }
    // Delete on a failed first message: gone, with Undo putting it back (as a chat's Delete, #4645).
    fun deleteFirst() {
        val message = firstMessage?.takeIf { it.failed } ?: return
        val page = firstPage
        firstMessage = null
        failedStarts.write(page, null)
        val deleted = resources.getString(persona.feature.chat.R.string.chat_delivery_deleted)
        val undo = persona.core.designsystem.FeedbackAction(resources.getString(persona.feature.chat.R.string.chat_delivery_undo)) {
            if (sendState.first.value == null) { sendState.first.value = message; failedStarts.write(page, message) }
        }
        if (feedback?.notice(deleted, undo) != true) toast.show(persona.core.designsystem.FeedbackNotice(deleted, undo))
    }
    LaunchedEffect(mainMessages, viewingMainQueue, managerChoice) {
        if (managerChoice.manager == null && (viewingMainQueue || (!openedMain && mainMessages.isNotEmpty() && requestedSession == null))) {
            openedMain = true
            viewingMainQueue = true
            mainMessages.firstOrNull { it.session != PendingMainConversation }?.session?.let { target ->
                openConversation(target)
                assistantAttempt { repository.refresh() }
            }
        }
    }
    LaunchedEffect(repository) {
        // Another device may have moved or replaced the main conversation while this one stayed connected.
        assistantAttempt { mainConversationSender?.mainChanged() }
        try { repository.refresh() }
        catch (cancelled: CancellationException) { throw cancelled }
        catch (_: Exception) { /* Cached conversations remain available while offline. */ }
    }
    LaunchedEffect(pageManagement) {
        if (pageManagement != null) {
            assistantAttempt { pageManagement.warm() }
            assistantAttempt { assistantMachine = pageManagement.notes().machineId }
            // Routines and the assistant's "today" follow the phone's time zone unless the user picked one.
            assistantAttempt { followPhoneZone(pageManagement.settingsApi(), StoredTimezoneChoice(context)) }
        }
    }
    // The chip's default and list: read when the page shows and again on each open (the default may have changed in
    // Settings; a list that could not be read is tried again), as the web reads them each time its menu opens.
    suspend fun loadStarterLaunchers() {
        val api = pageManagement ?: return
        if (startConversationWith == null || demo || assistantMachine.isBlank()) return
        val machine = assistantMachine
        assistantAttempt { api.config().launcher }.onSuccess { defaultLauncher = it.takeIf(String::isNotBlank) }
        if (launchers != null) return
        launchersFailed = false
        assistantAttempt { api.assistantLaunchers(machine) }
            .onSuccess { if (machine == assistantMachine) launchers = it }
            .onFailure { if (machine == assistantMachine) launchersFailed = true }
    }
    LaunchedEffect(pageManagement, assistantMachine, startConversationWith) { loadStarterLaunchers() }
    // The health space's new-chat page lists its past chats: read each time it opens.
    LaunchedEffect(healthPage) { if (healthPage && conversationsOf != null) assistantAttempt { loadHealth() } }
    // A chat opened from elsewhere (a notification, a link) that is not on the assistant's own list may be a health one:
    // asked once, so its New opens the health space's page.
    LaunchedEffect(active) {
        val shown = active ?: return@LaunchedEffect
        if (demo || conversationsOf == null || shown in healthKeys || shown == snapshot.mainSession ||
            snapshot.conversations.any { it.key == shown }) return@LaunchedEffect
        assistantAttempt { loadHealth() }
    }
    LaunchedEffect(requestedSession) {
        requestedSession?.let { openedMain = true; proactiveOpen = false; settingsOpen = false; openConversation(it) }
    }
    LaunchedEffect(sharedSidebar?.action) {
        val action = sharedSidebar?.action ?: return@LaunchedEffect
        when (action) {
            AssistantSidebarAction.NewConversation -> startNewConversation()
            AssistantSidebarAction.Health -> openHealth()
            AssistantSidebarAction.MainConversation -> {
                openedMain = true
                snapshot.mainSession?.let(::openConversation) ?: run { newConversation(); viewingMainQueue = true }
            }
            AssistantSidebarAction.ScheduledTasks -> if (pageManagement != null) proactiveOpen = true else info = AssistantInfo.Schedule
            AssistantSidebarAction.SessionInfo -> info = AssistantInfo.Session
            AssistantSidebarAction.Launcher -> if (pageManagement != null) settingsOpen = true else info = AssistantInfo.Launcher
        }
        if (sharedSidebar.action == action) sharedSidebar.action = null
    }
    LaunchedEffect(sharedSidebar?.retired) {
        val retired = sharedSidebar?.retired.orEmpty()
        if (active != null && active in retired) openMain()
        sharedSidebar?.retired = emptySet()
    }
    LaunchedEffect(snapshot.mainSession, snapshot.conversations, managerChoice) {
        if (!demo && !openedMain && requestedSession == null && mainMessages.isEmpty()) {
            val initial = snapshot.mainSession
            initial?.let(::openConversation)
        }
    }
    val keyboard = androidx.compose.ui.platform.LocalSoftwareKeyboardController.current
    val focusTarget by rememberUpdatedState(composer)
    val latestMain by rememberUpdatedState(snapshot.mainSession)
    // The main conversation's composer, inside the shared chat page, takes the cursor ([LocalComposerFocusAsk]); the ask
    // lapses if it takes no messages now (its manager offline), as the request itself does.
    fun askMain(main: SessionKey) {
        openConversation(main)
        focusAsk.session = main
        scope.launch { kotlinx.coroutines.delay(10_000); if (focusAsk.session == main) focusAsk.session = null }
    }
    LaunchedEffect(composerFocus) {
        composerFocus.requests.collect { at ->
            if (at == null || !composerFocus.take(at)) return@collect
            // Ask Persona goes to the main conversation, as iOS's does (#4339 C11: no home page); with none yet, to the
            // composer of the page that starts it, here.
            proactiveOpen = false; settingsOpen = false; drawerOpen = false
            if (hasChat && !demo) {
                latestMain?.let { askMain(it); return@collect }
                // On a cold start the conversations may still be on their way: the main one takes the cursor when it comes.
                if (!openedMain) scope.launch {
                    kotlinx.coroutines.withTimeoutOrNull(3_000) { snapshotFlow { latestMain }.first { it != null } }
                        ?.let { main -> if (!openedMain || main == SessionKey(activeMachine.orEmpty(), activeSession.orEmpty())) askMain(main) }
                }
                if (activeSession != null || newChat != null) newConversation()
            }
            // After the tab's first layout and the saved draft: the text field has to be attached (the old app waited 300 ms).
            kotlinx.coroutines.withTimeoutOrNull(3_000) { snapshotFlow { draftEditor == null || savedComposer != null }.first { it } }
            kotlinx.coroutines.delay(300)
            focusTarget.focusRequest++
            kotlinx.coroutines.delay(100)
            keyboard?.show()
        }
    }
    // Back as on iOS (#4339 C11): to the main conversation, or from New chat's page to the conversation it was tapped on
    // (nothing is started until its first message); on the main conversation Back is the shell's, which leaves the app.
    val back = assistantBack(active, snapshot.mainSession, newChat, viewingMainQueue)
    BackHandler(drawerOpen || back != null) {
        if (drawerOpen) { scope.launch { drawerOpen = false }; return@BackHandler }
        if (sending) return@BackHandler
        when (back) {
            is AssistantBack.Open -> openConversation(back.key)
            AssistantBack.Main -> openMain()
            null -> Unit
        }
    }
    val openKeys = remember(snapshot.conversations) { openKeys(snapshot.conversations) }
    val sidebar: @Composable () -> Unit = {
        AssistantDrawer(query, { query = it }, groups, active, snapshot.mainSession != null,
            onEnd = if (pageManagement != null) ({ ending = it }) else null,
            onMain = {
                openedMain = true
                snapshot.mainSession?.let(::openConversation) ?: run { newConversation(); viewingMainQueue = true }
                scope.launch { drawerOpen = false }
            },
            onOpen = { openConversation(it); scope.launch { drawerOpen = false } },
            onInfo = {
                if (it == AssistantInfo.Knowledge && onKnowledge != null) onKnowledge()
                else if (pageManagement != null && it == AssistantInfo.Launcher) settingsOpen = true
                else if (pageManagement != null && it == AssistantInfo.Schedule) proactiveOpen = true
                else info = it
                scope.launch { drawerOpen = false }
            },
            onNew = ::startNewConversation,
            onHealth = if (conversationsOf != null && !demo) ({ scope.launch { drawerOpen = false }; openHealth() }) else null,
            onSettings = { drawerOpen = false; navigator.navigate(SettingsRoute) },
            onInbox = { drawerOpen = false; onOpenInbox?.invoke() ?: navigator.navigate(InboxRoute) },
            onCopy = { copyId(it.key) },
            onArchive = if (pageManagement != null && !demo) ::archive else null,
            onLeader = if (pageManagement != null && !demo) ({ session ->
                // Open the confirmation first; setLeader runs only on the second choice.
                leaderConfirm = session
            }) else null,
            onRename = if (pageManagement != null && !demo) ({ history.renaming = it }) else null,
            onDiagnostics = ::copyDiagnostics,
            archivedCount = history.shownArchived(openKeys).size, onArchived = { history.archivedOpen = true },
            onTodos = onTodos?.let { action -> { scope.launch { drawerOpen = false }; action() } },
            todoCount = todoCount,
            showTodos = showTodos,
            onOpenTodo = { id -> scope.launch { drawerOpen = false }; navigator.navigate(TodoDetailRoute(id)) },
            onOpenAgent = { key -> scope.launch { drawerOpen = false }; navigator.navigate(ChatRoute(key.machineId, key.sessionId, ConversationKind.Agent)) },
            found = foundChats, searchPartial = searchPartial,
        )
        ConversationHistoryDialogs(history, snapshot.conversations, scope,
            onOpen = { openConversation(it); scope.launch { drawerOpen = false } },
            notice = { message ->
                if (feedback?.notice(message, persona.core.designsystem.FeedbackStyle.Success) != true)
                    toast.show(persona.core.designsystem.FeedbackNotice(message, style = persona.core.designsystem.FeedbackStyle.Success))
            },
            failure = { error ->
                if (feedback?.failure(error) != true) toast.show(persona.core.designsystem.FeedbackNotice(persona.core.designsystem.operationFeedback(error).text.resolve(resources), style = persona.core.designsystem.FeedbackStyle.Error))
            })
    }
    // On Glass what needs the user sits over the blank page's composer, as many lines as fit (#4339 C17, iOS ShellNeedsYou);
    // Brief and Board keep "Needs you now" in the page's scroll.
    val needsRoom = remember { NeedsRoom() }
    var mentionOpen by remember { mutableStateOf(false) }
    val needsFade = with(androidx.compose.ui.platform.LocalDensity.current) { NeedsFade.roundToPx() }
    val body: @Composable (Boolean) -> Unit = { wide ->
        val hasComposer = active == null || !hasChat || demo
        val blankPage = active == null && !viewingMainQueue && !healthPage && !clearedPage
        val needsOverComposer = PersonaTheme.style == AppStyle.Glass
        // They step aside while the @ list is open (`NeedsStepAside`); the dock keeps the page's backdrop meanwhile, as iOS's does.
        val shellLines = if (needsOverComposer && blankPage && onOpenNeed != null) shellNeedsYou else emptyList()
        // On the cleared page /clear is listed and does nothing: there is nothing to clear, and it never starts the new chat.
        CompositionLocalProvider(persona.feature.chat.LocalMainChatClear provides (if (clearedPage) persona.feature.chat.MainChatClear { _ -> } else null)) {
        Box(Modifier.fillMaxSize().then(if (shellLines.isEmpty()) Modifier else Modifier.onSizeChanged { needsRoom.pageSize(it) })) {
        JPage(
            title = assistantName,
            modifier = Modifier.fillMaxSize().imePadding(),
            topBar = {
                CompositionLocalProvider(LocalRootLeading provides if (sharedSidebar == null) ({
                    JPersonaButton(faceState, stringResource(R.string.assistant_conversation_list), {
                        if (wide) sidebarVisible = !sidebarVisible else drawerOpen = true
                    })
                }) else null) {
                    JTopBar(
                        title = assistantName, subtitle = connectionLabel,
                        titleBusy = !connectionLabel.isNullOrBlank(), onTitleClick = { menu = true },
                        actions = {
                            // On a health chat (or the health space's new-chat page) New starts another health chat. Off on
                            // the blank page itself, as iOS's (#4339 C18).
                            val health = onHealthChat || healthPage
                            JIconButton(PhIcons.NotePencil,
                                stringResource(if (health) R.string.assistant_health_new else R.string.assistant_new_conversation),
                                { if (health) startHealthConversation() else startNewConversation() },
                                enabled = !sending && managers.offline == null && (active != null || viewingMainQueue))
                            if (LocalHomeChrome.current == null) JIconButton(PhIcons.UserCircle, stringResource(R.string.assistant_profile), {
                                if (pageManagement != null) settingsOpen = true else info = AssistantInfo.Settings
                            })
                        },
                    )
                }
            },
            bottomBar = if (hasComposer) ({ Box {
                // On Glass with lines over the composer, the dock is on the page's own backdrop (NeedsBackdrop).
                if (shellLines.isNotEmpty()) NeedsBackdrop(NeedsFade, Modifier.matchParentSize())
                Column {
                    if (hasComposer && active == null && !viewingMainQueue && (sending || mainReceipts.isNotEmpty())) {
                        StatusLine(assistantStatus(null, emptyList(),
                            submittingId = pendingClientId.takeIf { sending }, receipts = mainReceipts))
                    }
                    if (hasComposer && active == null && !viewingMainQueue) {
                        val cardModifier = (if (shellLines.isEmpty()) Modifier else Modifier.onSizeChanged { needsRoom.above = it.height })
                            .padding(horizontal = pagePadding).padding(bottom = Tokens.Space.s)
                        if (shellLines.isNotEmpty() && blocking == null && !(mainReadiness.chatOnly && readinessOn)) SideEffect { needsRoom.above = 0 }
                        if (blocking != null) ReadinessStepCard(blocking, { runReadinessAction(it) }, cardModifier,
                            busy = ReadinessActionId.TestAssistant.takeIf { mainReadiness.probing },
                            note = readiness?.probe?.let { probe ->
                                if (probe.ok) stringResource(persona.core.designsystem.R.string.designsystem_readiness_probe_ok, probe.answer.orEmpty())
                                else stringResource(persona.core.designsystem.R.string.designsystem_readiness_probe_failed, probe.error.orEmpty())
                            })
                        else if (mainReadiness.chatOnly && readinessOn) JNotice(
                            stringResource(persona.core.designsystem.R.string.designsystem_readiness_chat_only), cardModifier,
                            actionLabel = stringResource(ReadinessActionId.AddComputer.label),
                            onAction = { runReadinessAction(ReadinessActionId.AddComputer) })
                    }
                    if (shellLines.isNotEmpty()) NeedsStepAside(mentionOpen, Modifier.testTag("shell-needs-aside")) {
                        ShellNeedsYou(shellLines, { needsRoom.height(needsFade) }, requireNotNull(onOpenNeed), onMore = { more ->
                            if (more.opensAgents) navigator.selectTab(AppTab.Agents) else onOpenInbox?.invoke() ?: navigator.navigate(InboxRoute)
                        }, Modifier.padding(horizontal = Tokens.Space.m))
                    }
                    if (hasComposer) {
                        draftError?.let { JNotice(androidx.compose.ui.res.stringResource(it),
                            (if (shellLines.isEmpty()) Modifier else Modifier.onSizeChanged { needsRoom.below = it.height }).padding(horizontal = pagePadding),
                            status = Status.NeedsYou) }
                        if (shellLines.isNotEmpty() && draftError == null) SideEffect { needsRoom.below = 0 }
                        // Where the composer starts with the keyboard down: the floor of the room the lines above may take.
                        if (shellLines.isNotEmpty()) Spacer(Modifier.fillMaxWidth().onGloballyPositioned {
                            needsRoom.composerAt(it.positionInRoot().y.roundToInt())
                        })
                        val uploadTarget = active ?: snapshot.mainSession
                            ?: SessionKey(assistantMachine, PendingMainConversation.sessionId)
                        // New chat's @: the sessions, and a typed path's folder on the assistant's computer (where uploads go).
                        val mentionMachine = persona.core.contracts.GroupKeys.raw(uploadTarget.machineId)
                        val folders = remember(mentionFiles, mentionMachine) {
                            mentionFiles?.takeIf { mentionMachine.isNotBlank() }?.let { files ->
                                MentionFolders { dir, cwd -> files.directory(mentionMachine, cwd, dir).entries }
                            }
                        }
                        val mentions = if (mentionsHere) mentionSessions?.collectAsStateWithLifecycle()?.value
                            ?.let { starterMentionSource(it, folders) } else null
                        ChatComposer(uploadTarget, composer, working = false, sending = sending || !draftReady,
                            // The health page sends no files: an upload goes to the main chat's machine, outside the health space.
                            canClear = false, service = composerService?.takeIf { uploadTarget.machineId.isNotBlank() && !healthPage },
                            onSend = { submit() }, onCommand = { composer.insert(it, composer.focused) },
                            onNotice = { message ->
                                if (feedback?.notice(message) != true) toast.show(persona.core.designsystem.FeedbackNotice(message))
                            }, enabled = draftReady && !sending && managers.offline == null && !(active == null && firstMessage != null),
                            placeholder = managers.offline ?: if (healthPage) stringResource(R.string.assistant_health_placeholder)
                                else stringResource(R.string.assistant_composer_placeholder, assistantName),
                            blockedLabel = blocking?.takeIf { active == null }?.let { stringResource(blockedSendLabel(it)) },
                            onBlocked = { blocking?.primaryAction()?.let { runReadinessAction(it) } },
                            extraContent = starterQuotes.isNotEmpty(), mentions = mentions, onMention = starterMentions::add,
                            onMentionOpen = { mentionOpen = it },
                            // Glass: the model pill and its panel (#5055, the web's new-chat page): the pick is this chat's start.
                            modelPill = if (PersonaTheme.style == AppStyle.Glass && active == null && !healthPage && pageManagement != null &&
                                startConversationWith != null && !demo) ({ width ->
                                persona.feature.chat.PersonaModelPill(null, assistantMachine, pageManagement, width,
                                    pagePick = starterLauncher?.let { persona.core.contracts.ModelPick(it, starterModel, starterEffort) },
                                    onPagePick = { pick ->
                                        // Another setup does not reuse what the last one started.
                                        if (pick?.launcher != starterLauncher || pick?.model != starterModel) starterStarted = null
                                        starterLauncher = pick?.launcher; starterModel = pick?.model; starterEffort = pick?.effort
                                    },
                                    onNotice = { message -> if (feedback?.notice(message) != true) toast.show(persona.core.designsystem.FeedbackNotice(message)) },
                                    enabled = !sending)
                            }) else null,
                            // A new conversation's launcher, for it alone (iOS StarterComposer, web StarterLauncherChip); Glass's is the pill.
                            accessory = if (active == null && !healthPage && pageManagement != null && startConversationWith != null && !demo) ({
                                if (mentionsHere) StarterQuoteCards(starterMentions)
                                if (PersonaTheme.style != AppStyle.Glass) StarterLauncherChip(starterLauncher, defaultLauncher, launchers, launchersFailed, enabled = !sending,
                                    onOpen = { scope.launch { loadStarterLaunchers() } }) { picked ->
                                    // Another launcher (or the default) does not reuse what the last one started.
                                    if (picked != starterLauncher) starterStarted = null
                                    starterLauncher = picked
                                }
                            }) else if (starterQuotes.isNotEmpty()) ({ StarterQuoteCards(starterMentions) }) else null)
                    }
                }
            } }) else null,
            // An embedded conversation is a page of its own and softens the edges under these bars itself.
            edges = !(active != null && hasChat),
            scrollEdgeStyle = ScrollEdgeStyle.Gradient,
        ) { padding ->
            if (active != null && hasChat) {
                // The conversation fills the page and scrolls under the bar above it and the dock below, like any
                // page, instead of being cut off at a line under the bar; the chrome insets give it their room.
                // (The demo notice sits under the bar, so there the conversation starts below it.)
                val host = ChromeInsets(bottom = padding.calculateBottomPadding(), handlesNavigationBar = true)
                Column(Modifier.fillMaxSize().then(if (demo) Modifier.padding(top = padding.calculateTopPadding()) else Modifier)) {
                    if (demo) AssistantDemoNotice()
                    Box(Modifier.weight(1f)) {
                        CompositionLocalProvider(LocalChromeInsets provides if (demo) host else host.copy(top = padding.calculateTopPadding())) {
                            // The refusal fallback card's "New main chat" (#2528): the main conversation's own dialog.
                            CompositionLocalProvider(persona.feature.chat.LocalNewMainChat provides
                                (if (onMain && mainActions.newMain) ({ newMainOpen = true }) else null),
                                persona.feature.chat.LocalMainChatClear provides (if (onMain && mainActions.clear) persona.feature.chat.MainChatClear(::clearMain) else null),
                                persona.feature.chat.LocalComposerFocusAsk provides focusAsk) {
                                if (assistantChatContent != null) assistantChatContent(active, AssistantChatOptions(readOnlyNote = managers.offline))
                                else chatContent?.invoke(active)
                            }
                        }
                        // A compaction of the main conversation: under the bar, over the conversation.
                        if (onMain) compaction.line?.let { line ->
                            CompactionStatusLine(line, onDismiss = { compaction = compaction.dismissed() },
                                modifier = Modifier.align(Alignment.TopCenter)
                                    .then(if (demo) Modifier else Modifier.padding(top = padding.calculateTopPadding())))
                        }
                    }
                }
            } else if (viewingMainQueue || (active != null && !hasChat)) {
                val visible = if (active != null) outbox.filter { it.session == active } else mainMessages
                AssistantPendingConversation(visible, snapshot.conversations.firstOrNull { it.key == active },
                    Modifier.fillMaxSize(), contentPadding = padding, demo = demo,
                    receipts = if (active != null) receivedSendReceipts.filter { it.session == active } else mainReceipts,
                    emptyDetail = managers.offlineEmpty,
                    onRefusal = { action -> runReadinessAction(action) },
                    onRetry = { message -> scope.launch {
                        // Asked again first: a step still missing opens instead of sending into the same refusal.
                        if (readinessOn) mainReadiness.refresh()
                        val missing = mainReadiness.blocking.takeIf { readinessOn }
                        if (missing != null) missing.primaryAction()?.let { runReadinessAction(it) }
                        else assistantAttempt { (mainConversationSender as? MessageSender ?: messageSender).retry(message.clientMessageId) }
                    } })
            } else if (firstMessage != null) {
                // The first message while its conversation starts, or kept after the start failed (#4339 C18, iOS).
                FirstMessagePending(requireNotNull(firstMessage), padding, busy = sending,
                    onResend = { firstMessage?.let { submit(resend = it) } }, onDelete = ::deleteFirst)
            } else if (clearedPage) {
                // The main conversation just cleared: an empty chat, the composer still at the bottom (#5100).
                Box(Modifier.fillMaxSize().padding(padding).testTag("main-cleared"))
            } else if (healthPage) {
                HealthHome(healthChats, contentPadding = padding, onOpen = ::openConversation, onCopy = ::copyId)
            } else {
                NewChatPage(enabled = !sending && draftReady && composer.ready, machines = machines,
                    phoneToolsAvailable = phoneToolsAvailable, contentPadding = padding, demo = demo,
                    setup = blocking != null, startsMain = newChat == null, hubSuggestions = readiness?.suggestions?.takeIf { it.isNotEmpty() },
                    onLocalSuggestion = { readinessHelp = ReadinessHelp.ofSuggestion(it) },
                    onSuggestion = {
                        composer.value = androidx.compose.ui.text.input.TextFieldValue(it, androidx.compose.ui.text.TextRange(it.length))
                        submit()
                    }, needsYou = needsYou, onNeed = onOpenNeed.takeUnless { needsOverComposer },
                    onMore = onOpenInbox ?: { navigator.navigate(InboxRoute) }, room = needsRoom.takeIf { shellLines.isNotEmpty() })
            }
        }
            // The page's own toast (no app feedback), at the top, where the root draws the app's.
            persona.core.designsystem.JToastHost(toast, Modifier.align(Alignment.TopCenter).statusBarsPadding())
        }
        }
    }
    // A run or a follow-up opened from "Proactive" (here or inside the settings page) shows its conversation in place.
    val openFromSchedule: (SessionKey) -> Unit = {
        proactiveOpen = false; settingsOpen = false; openedMain = true; openConversation(it)
        scope.launch { assistantAttempt { repository.refresh() } }
    }
    // A task's model is set on Settings › Models (iOS ModelConfigurationLink); back from it returns here.
    val openModelConfiguration: (String, String) -> Unit = { purpose, owner -> navigator.navigate(ModelPurposeRoute(purpose, owner)) }
    if (settingsOpen && pageManagement != null) {
        AssistantSettingsScreen(pageManagement, machines, initialManager = managerChoice.shown, onBack = { settingsOpen = false }, inPlace = true, onMoved = {
            // The conversations and the upload target now belong to the other machine.
            assistantAttempt { repository.refresh() }
            assistantAttempt { assistantMachine = pageManagement.notes().machineId }
            // The old main conversation is an ordinary one on the old machine now: back here, start the new one.
            newConversation()
        }, onOpenConversation = openFromSchedule, onModelConfiguration = openModelConfiguration)
        return@PersonaTheme
    }
    if (proactiveOpen && pageManagement != null) {
        // Shown in place of the conversation: back on its first page closes it here (its own pages come first).
        BackHandler { proactiveOpen = false }
        AssistantProactiveScreen(onBack = { proactiveOpen = false }, onOpenConversation = openFromSchedule,
            onModelConfiguration = { purpose -> openModelConfiguration(purpose, managerChoice.shown) }, management = pageManagement, machines = machines)
        return@PersonaTheme
    }
    BoxWithConstraints(Modifier.fillMaxSize()) {
        if (sharedSidebar != null) {
            body(false)
        } else if (maxWidth >= Tokens.Android.Size.assistantSplitMin) {
            Row(Modifier.fillMaxSize()) {
                // The list of conversations sits on the same background as the page beside it, not a panel of its own.
                if (sidebarVisible) Box(Modifier.width(Tokens.Size.box300).fillMaxHeight()) {
                    PageBackdrop()
                    Box(Modifier.fillMaxSize().statusBarsPadding().navigationBarsPadding()) { sidebar() }
                }
                Box(Modifier.weight(1f)) { body(true) }
            }
        } else {
            body(false)
            if (drawerOpen) JSheet(onDismiss = { drawerOpen = false }, title = stringResource(R.string.assistant_conversation_list)) {
                Box(Modifier.fillMaxWidth().fillMaxHeight(0.9f)) { sidebar() }
            }
        }
    }
    // The title menu, in iOS's order (#4339 C18): its copies read the settings as the menu opens.
    var menuConfig by remember(pageManagement) { mutableStateOf<persona.core.contracts.api.AssistantConfig?>(null) }
    LaunchedEffect(menu, pageManagement) {
        if (menu && !demo) pageManagement?.let { api -> assistantAttempt { api.config() }.onSuccess { menuConfig = it } }
    }
    val openConnectionDetails = persona.core.designsystem.rememberConnectionDetails(
        connection ?: persona.core.contracts.api.ConnectionState.Online, { navigator.navigate(persona.core.navigation.ConnectionSettingsRoute) })
    if (menu) {
        val shown = active?.let { key -> snapshot.conversations.firstOrNull { it.key == key } }
        val copyText = AssistantCopyText(shown, active, onMain, menuConfig, assistantMachine, machines)
        val interrupt = composerService.takeIf { !demo }
        val sections = assistantMenu(offline = connection != null && connection != persona.core.contracts.api.ConnectionState.Online,
            voice = onVoice != null, main = snapshot.mainSession, active = active, compact = onMain && mainActions.compact,
            newMain = onMain && mainActions.newMain, working = interrupt != null && persona.core.domain.lines.isLiveStatus(shown?.status),
            clear = onMain && mainActions.clear)
        fun rawAction(item: AssistantMenuItem, separator: Boolean): JAction = when (item) {
            AssistantMenuItem.ConnectionDetails -> JAction(resources.getString(item.label), separatorBefore = separator,
                onClick = openConnectionDetails)
            AssistantMenuItem.Knowledge -> JAction(resources.getString(item.label), separatorBefore = separator, onClick = {
                if (onKnowledge != null) onKnowledge() else info = AssistantInfo.Knowledge
            })
            // iOS's Details opens Persona's profile: here the assistant's own page, its settings.
            AssistantMenuItem.Details -> JAction(resources.getString(item.label), separatorBefore = separator, onClick = {
                if (pageManagement != null) settingsOpen = true else info = AssistantInfo.Settings
            })
            AssistantMenuItem.Voice -> JAction(resources.getString(item.label), separatorBefore = separator,
                onClick = { onVoice?.invoke() })
            // In place of the home page's Home (#4339 C11), as iOS's menu has it: off the main conversation only.
            AssistantMenuItem.BackToMain -> JAction(resources.getString(item.label), PhIcons.House,
                separatorBefore = separator, onClick = { snapshot.mainSession?.let(::openConversation) })
            AssistantMenuItem.CopyDirectory -> JAction(resources.getString(item.label), separatorBefore = separator,
                onClick = { copy(copyText.cwd, resources.getString(R.string.assistant_directory_copied)) })
            AssistantMenuItem.CopyInfo -> JAction(resources.getString(item.label), separatorBefore = separator,
                onClick = { copy(copyText.info(resources), resources.getString(R.string.assistant_info_copied)) })
            AssistantMenuItem.CopySessionId -> JAction(resources.getString(item.label), separatorBefore = separator,
                onClick = { active?.let(::copyId) })
            AssistantMenuItem.CopyDiagnostics -> JAction(resources.getString(item.label), separatorBefore = separator,
                onClick = { active?.let { key -> copyDiagnostics(shown ?: Session(machineId = key.machineId, sessionId = key.sessionId)) } })
            AssistantMenuItem.Compact -> JAction(resources.getString(item.label), separatorBefore = separator,
                enabled = !compacting && compaction.line != CompactionLine.Running, onClick = ::compactMain)
            AssistantMenuItem.ClearContext -> JAction(resources.getString(item.label), separatorBefore = separator,
                enabled = !clearBusy, onClick = { clearMain() })
            AssistantMenuItem.NewMain -> JAction(resources.getString(item.label), separatorBefore = separator,
                enabled = !newMainBusy, onClick = { newMainOpen = true })
            AssistantMenuItem.SessionInfo -> JAction(resources.getString(item.label), separatorBefore = separator,
                onClick = { info = AssistantInfo.Session })
            AssistantMenuItem.Interrupt -> JAction(resources.getString(item.label), separatorBefore = separator, onClick = {
                val key = active
                if (interrupt != null && key != null) scope.launch {
                    try { interrupt.interrupt(key); say(resources.getString(persona.feature.chat.R.string.chat_composer_interrupt_requested)) }
                    catch (cancelled: CancellationException) { throw cancelled }
                    catch (_: Exception) { say(resources.getString(persona.feature.chat.R.string.chat_composer_interrupt_unknown)) }
                }
            })
        }
        // iOS's icon on every item: Glass draws them; Brief and Board keep their words (Back to main chat has had its house all along).
        val glassIcons = PersonaTheme.style == AppStyle.Glass
        fun action(item: AssistantMenuItem, separator: Boolean): JAction = rawAction(item, separator)
            .copy(icon = item.icon.takeIf { glassIcons || item == AssistantMenuItem.BackToMain })
        // iOS's heading: which conversation and what it runs on ("Main chat · claude · Opus 4.5").
        JActionSheet(title = copyText.menuTitle(resources, assistantName, hubGroup), onDismiss = { menu = false },
            actions = sections.flatMapIndexed { index, items -> items.mapIndexed { i, item -> action(item, separator = index > 0 && i == 0) } })
    }
    if (newMainOpen) {
        JDialog(onDismiss = { if (!newMainBusy) newMainOpen = false }, title = stringResource(R.string.assistant_new_main_title),
            text = newMainText(resources, java.time.LocalDate.now(), locale), confirmEnabled = !newMainBusy,
            confirm = stringResource(R.string.assistant_create) to ::newMain,
            dismiss = stringResource(R.string.assistant_cancel) to { if (!newMainBusy) newMainOpen = false }) {
            JSwitchRow(stringResource(R.string.assistant_carry_summary), carrySummary, { carrySummary = it },
                subtitle = stringResource(R.string.assistant_carry_summary_hint),
                enabled = !newMainBusy)
        }
    }
    ending?.let { session ->
        JDialog(onDismiss = { if (!endingBusy) ending = null }, title = stringResource(R.string.assistant_end_title),
            text = stringResource(R.string.assistant_end_text, session.conversationTitle(words)),
            destructive = true, confirmEnabled = !endingBusy,
            confirm = stringResource(R.string.assistant_end) to {
                endingBusy = true
                scope.launch {
                    try {
                        requireNotNull(pageManagement).endConversation(session.key)
                        ended = ended + session.key
                        ending = null
                        if (active == session.key) openMain()
                        assistantAttempt { repository.refresh() }
                    } catch (cancelled: CancellationException) { throw cancelled }
                    catch (_: Exception) {
                        val failed = resources.getString(R.string.assistant_end_failed)
                        if (feedback?.notice(failed, persona.core.designsystem.FeedbackStyle.Error) != true) toast.show(persona.core.designsystem.FeedbackNotice(failed, style = persona.core.designsystem.FeedbackStyle.Error))
                    }
                    finally { endingBusy = false }
                }
            }, dismiss = stringResource(R.string.assistant_cancel) to { if (!endingBusy) ending = null })
    }
    leaderConfirm?.let { session ->
        val leader = session.sessionId in snapshot.leaders
        val title = session.conversationTitle(words)
        JDialog(onDismiss = { leaderConfirm = null },
            title = stringResource(if (leader) R.string.assistant_end_leader_title else R.string.assistant_make_leader_title),
            text = if (leader) stringResource(R.string.assistant_end_leader_text, title)
                   else stringResource(R.string.assistant_make_leader_text),
            confirm = stringResource(if (leader) R.string.assistant_end_leader else R.string.assistant_make_leader) to {
                // setLeader owns the hub call, the refresh and the failure notice.
                setLeader(session)
                leaderConfirm = null
            }, dismiss = stringResource(R.string.assistant_cancel) to { leaderConfirm = null })
    }
    readinessHelp?.let { help ->
        ReadinessHelpDialog(help, readiness?.machine?.name, onDismiss = { readinessHelp = null }, onAction = { runReadinessAction(it) })
    }
    // An open conversation's "Session info" also lists what it was always allowed (#890); the rest explain in a dialog.
    val infoSession = active?.takeIf { info == AssistantInfo.Session && !demo }
    if (infoSession != null && pageManagement != null) AssistantSessionInfoSheet(infoSession, pageManagement, onDismiss = { info = null })
    else info?.let { topic ->
        JDialog(onDismiss = { info = null }, title = stringResource(topic.title),
            text = when (topic) {
                AssistantInfo.Schedule -> stringResource(R.string.assistant_info_schedule_text)
                AssistantInfo.Knowledge -> stringResource(R.string.assistant_info_knowledge_text)
                AssistantInfo.Settings -> stringResource(R.string.assistant_info_settings_text)
                AssistantInfo.Session -> if (active == null) stringResource(R.string.assistant_info_session_none)
                    else stringResource(R.string.assistant_info_session_text, active.sessionId)
                AssistantInfo.Launcher -> stringResource(R.string.assistant_info_launcher_text)
            }, confirm = stringResource(R.string.assistant_got_it) to { info = null }, dismiss = null)
    }
}

/** What the drawer's "More" rows and the title menu explain when there is nothing to open (the demo, a missing hub). */
internal enum class AssistantInfo(@androidx.annotation.StringRes val title: Int) {
    Schedule(R.string.assistant_info_schedule),
    Knowledge(R.string.assistant_info_knowledge),
    Settings(R.string.assistant_info_settings),
    Session(R.string.assistant_info_session),
    Launcher(R.string.assistant_info_launcher),
}

/** A suggestion card; [prompt] is sent as the user's own message, so it is in the user's language too. */
private data class Suggestion(val icon: PhIcon, @androidx.annotation.StringRes val title: Int,
    @androidx.annotation.StringRes val detail: Int, @androidx.annotation.StringRes val prompt: Int)
private val suggestions = listOf(
    Suggestion(PhIcons.Tray, R.string.assistant_suggest_inbox, R.string.assistant_suggest_inbox_detail, R.string.assistant_suggest_inbox_prompt),
    Suggestion(PhIcons.Terminal, R.string.assistant_suggest_agents, R.string.assistant_suggest_agents_detail, R.string.assistant_suggest_agents_prompt),
    Suggestion(PhIcons.CalendarBlank, R.string.assistant_suggest_calendar, R.string.assistant_suggest_calendar_detail, R.string.assistant_suggest_calendar_prompt),
    Suggestion(PhIcons.Clock, R.string.assistant_suggest_remind, R.string.assistant_suggest_remind_detail, R.string.assistant_suggest_remind_prompt),
    Suggestion(PhIcons.BookOpen, R.string.assistant_suggest_knowledge, R.string.assistant_suggest_knowledge_detail, R.string.assistant_suggest_knowledge_prompt),
    Suggestion(PhIcons.FileText, R.string.assistant_suggest_doc, R.string.assistant_suggest_doc_detail, R.string.assistant_suggest_doc_prompt),
)

/**
 * The words under the greeting on the blank page (iOS `ShellHome` header): "This is your main chat…" on the page that starts the
 * main conversation, "Start a new side chat…" on New chat's; with a step to finish first, the step's own line.
 */
@androidx.annotation.StringRes
internal fun homeLead(setup: Boolean, startsMain: Boolean): Int = when {
    setup -> R.string.assistant_home_setup_lead
    startsMain -> R.string.assistant_home_lead_main
    else -> R.string.assistant_home_lead_side
}

/**
 * The assistant's one blank page (iOS `ShellHome`; #4339 C11: Android has no home page): New chat's, whose first message
 * starts a new conversation, or with no main conversation yet the page whose first message starts that one. A greeting,
 * what needs the user, things to ask and the devices Persona can reach, over the composer.
 */
@Composable
@OptIn(ExperimentalLayoutApi::class)
private fun NewChatPage(
    enabled: Boolean, machines: List<Machine>, phoneToolsAvailable: Boolean?, contentPadding: PaddingValues,
    demo: Boolean, onSuggestion: (String) -> Unit,
    needsYou: NeedsYou = NeedsYou(), onNeed: ((NeedsYouLine) -> Unit)? = null, onMore: () -> Unit = {},
    /** The main conversation still misses a step (docs/assistant/onboarding.md 4.4): the lead says so. */
    setup: Boolean = false,
    /** The first message here starts the main conversation (the page that starts it, not New chat's): the lead says "This is your main chat" (iOS `assistantMain == nil`), else "Start a new side chat". */
    startsMain: Boolean = true,
    /** The hub's example questions (readiness `suggestions`), in place of the app's own; null: the app's. */
    hubSuggestions: List<ReadinessSuggestion>? = null,
    onLocalSuggestion: (String) -> Unit = {},
    /** On Glass, the room Needs you over the composer may take: told where the first two rows of things to ask end. */
    room: NeedsRoom? = null,
) {
    val grid = androidx.compose.foundation.lazy.grid.rememberLazyGridState()
    var gridTop by remember { mutableIntStateOf(0) }
    if (room != null) {
        // The items before the things to ask: the demo's notice, the lead, and "Needs you now" where the scroll has it.
        val first = (if (demo) 1 else 0) + 1 + (if (onNeed != null && !needsYou.isEmpty) 1 else 0)
        val fold = welcomeFold(first, hubSuggestions?.size ?: suggestions.size)
        LaunchedEffect(grid, room, fold) {
            snapshotFlow { if (grid.canScrollBackward) null else foldBottom(grid.layoutInfo, fold)?.let { gridTop + it } }
                .collect { bottom -> if (bottom != null) room.fold = bottom }
        }
    }
    LazyVerticalGrid(
        columns = GridCells.Fixed(2), state = grid,
        modifier = Modifier.fillMaxSize().then(if (room == null) Modifier else Modifier.onGloballyPositioned { gridTop = it.positionInRoot().y.roundToInt() }),
        contentPadding = PaddingValues(start = pagePadding, end = pagePadding,
            top = contentPadding.calculateTopPadding() + 16.dp.dense, bottom = contentPadding.calculateBottomPadding() + 24.dp.dense),
        horizontalArrangement = Arrangement.spacedBy(12.dp.dense), verticalArrangement = Arrangement.spacedBy(12.dp.dense),
    ) {
        if (demo) item(span = { GridItemSpan(2) }) { AssistantDemoNotice() }
        item(span = { GridItemSpan(2) }) {
            Column(Modifier.padding(vertical = 20.dp.dense), verticalArrangement = Arrangement.spacedBy(12.dp.dense)) {
                Text(stringResource(greeting(LocalTime.now().hour)), style = PersonaTheme.type.largeTitle, color = PersonaTheme.palette.ink)
                Text(stringResource(homeLead(setup, startsMain)), style = PersonaTheme.type.voice, color = PersonaTheme.palette.ink2)
            }
        }
        // What waits on the user comes before what Persona could do: the suggestions stay under it.
        if (onNeed != null && !needsYou.isEmpty) item(key = "needs-you", span = { GridItemSpan(2) }) {
            NeedsYouSection(needsYou, onNeed, onMore)
        }
        if (hubSuggestions != null) items(hubSuggestions, key = { "hub-${it.id}" }) { suggestion ->
            HubSuggestionCard(suggestion, enabled, onOpen = {
                val prompt = suggestion.prompt?.takeIf(String::isNotBlank)
                if (suggestion.local || prompt == null) onLocalSuggestion(suggestion.id) else onSuggestion(prompt)
            })
        } else items(suggestions) { suggestion ->
            val prompt = stringResource(suggestion.prompt)
            SuggestionCard(suggestion.icon, stringResource(suggestion.title), stringResource(suggestion.detail),
                onClick = if (enabled) ({ onSuggestion(prompt) }) else null)
        }
        item(span = { GridItemSpan(2) }) {
            Column {
                JSectionHeader(stringResource(R.string.assistant_online_devices))
                FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp.dense), verticalArrangement = Arrangement.spacedBy(8.dp.dense)) {
                    machines.filter { it.online }.distinctBy { it.id }.forEach { machine ->
                        JChip(machine.displayName, icon = PhIcons.Desktop)
                    }
                    JChip(when (phoneToolsAvailable) {
                        true -> stringResource(R.string.assistant_this_phone)
                        false -> stringResource(R.string.assistant_phone_tools_unavailable)
                        null -> stringResource(R.string.assistant_phone_permissions_pending)
                    }, icon = PhIcons.DeviceMobile)
                }
            }
        }
    }
}

/**
 * The health space's new-chat page (docs/assistant/health.md): what it is for, then its past chats, newest first (the
 * history: tap one to open it). The composer under it starts a health chat.
 */
@Composable
private fun HealthHome(
    chats: List<Session>?, contentPadding: PaddingValues, onOpen: (SessionKey) -> Unit, onCopy: (SessionKey) -> Unit,
) {
    val history = remember(chats) { HealthEntry.history(chats.orEmpty()) }
    LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(start = pagePadding, end = pagePadding,
        top = contentPadding.calculateTopPadding() + Tokens.Space.l, bottom = contentPadding.calculateBottomPadding() + Tokens.Space.xl)) {
        item(key = "health-lead") {
            Column(Modifier.padding(vertical = Tokens.Space.l), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                Text(stringResource(R.string.assistant_health_title), style = PersonaTheme.type.largeTitle, color = PersonaTheme.palette.ink)
                Text(stringResource(R.string.assistant_health_lead), style = PersonaTheme.type.voice, color = PersonaTheme.palette.ink2)
            }
        }
        if (history.isNotEmpty()) item(key = "health-history") {
            JSectionHeader(stringResource(R.string.assistant_health_history), count = history.size)
        }
        itemsIndexed(history, key = { _, session -> "health/" + session.key.path }) { index, session ->
            ConversationRow(session, selected = false, position = GroupPosition.of(index, history.size),
                onOpen = { onOpen(session.key) }, onEnd = null, onCopy = { onCopy(session.key) })
        }
    }
}

/** One of the hub's example questions: a local one opens help here, never sent to the model. */
@Composable
private fun HubSuggestionCard(suggestion: ReadinessSuggestion, enabled: Boolean, onOpen: () -> Unit) {
    val detail = when {
        suggestion.local -> stringResource(R.string.assistant_suggest_local_detail)
        "phone_calendar" in suggestion.needs -> stringResource(R.string.assistant_suggest_needs_calendar)
        else -> null
    }
    SuggestionCard(if (suggestion.local) PhIcons.Question else PhIcons.Sparkle, suggestion.title, detail,
        onClick = if (enabled || suggestion.local) onOpen else null)
}

/** A suggestion card on the blank page: an icon, a one-line title and a line under it. */
@Composable
private fun SuggestionCard(icon: PhIcon, title: String, detail: String?, onClick: (() -> Unit)?) {
    JCard(Modifier.fillMaxWidth().heightIn(min = 144.dp.row), padding = 16.dp.dense, onClick = onClick) {
        JIcon(icon, null, tint = PersonaTheme.palette.accent)
        Spacer(Modifier.height(12.dp.dense))
        Text(title, maxLines = 1, overflow = TextOverflow.Ellipsis,
            style = PersonaTheme.type.headline, color = PersonaTheme.palette.ink)
        Spacer(Modifier.height(4.dp.dense))
        Text(detail.orEmpty(), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2,
            maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

@Composable
internal fun AssistantDrawer(
    query: String, onQuery: (String) -> Unit, groups: ConversationGroups, active: SessionKey?, hasMain: Boolean,
    onEnd: ((Session) -> Unit)?,
    onMain: () -> Unit, onOpen: (SessionKey) -> Unit, onInfo: (AssistantInfo) -> Unit,
    onNew: () -> Unit, onSettings: () -> Unit,
    /** The health space (docs/assistant/health.md): its newest chat, else its new-chat page; null: no Health row. */
    onHealth: (() -> Unit)? = null,
    onCopy: (Session) -> Unit = {},
    onArchive: ((Session) -> Unit)? = null,
    /** Makes a chat a leader (常驻) or ends it, on the hub (#4331); null: the menu offers neither. */
    onLeader: ((Session) -> Unit)? = null,
    /** Rename… on a chat's menu (not the main one's); null: no Rename. */
    onRename: ((Session) -> Unit)? = null,
    /** Copy diagnostics on a chat's menu; null: none. */
    onDiagnostics: ((Session) -> Unit)? = null,
    /** "Archived (N)" at the end of the list when there are any (iOS `archivedRow`); [onArchived] opens them. */
    archivedCount: Int = 0, onArchived: (() -> Unit)? = null,
    onTodos: (() -> Unit)? = null, todoCount: Int = 0, showTodos: Boolean = true,
    onInbox: (() -> Unit)? = null, inboxCount: Int = 0,
    settingsCount: Int = 0, settingsDot: Boolean = false, settingsBadge: String? = null,
    pages: List<NavItem> = emptyList(), onPage: (String) -> Unit = {},
    /** Opens a to-do's card / an Agent's session from a 常驻 lead's tree (#4352); null: those rows only fold. */
    onOpenTodo: ((String) -> Unit)? = null, onOpenAgent: ((SessionKey) -> Unit)? = null,
    /** The sidebar's head with the assistant switcher (docs/assistant/managers.md "Clients"); none: the head as it was. */
    switcher: AssistantSwitcherSlot? = null,
    /**
     * The results list while a query is non-blank and the hub lists assistant search: local matches at once, the
     * manager's results merged in when they arrive. Null keeps today's groups.
     */
    found: List<FoundChat>? = null,
    /** The manager is still reading older chats, so some matches may be missing. */
    searchPartial: Boolean = false,
) {
    // "All chats" opens the rest of the conversations in place: this app has no all-conversations page.
    var showAllChats by rememberSaveable { mutableStateOf(false) }
    // Recent folds on its own too (#6228): the choice is kept on this device across cold starts (the web's
    // localStorage, iOS's @StoredValue), not just configuration changes.
    val foldContext = androidx.compose.ui.platform.LocalContext.current
    val foldStore = remember { SidebarFoldStore(foldContext) }
    var recentFolded by remember { mutableStateOf(foldStore.recentFolded) }
    // The open conversation's row still shows while folded, so folding never hides where a message lands (iOS holdsCurrent).
    val holdsCurrent = active != null && groups.recent.any { it.key == active }
    val recentOpen = !recentFolded || holdsCurrent
    // The drawer's head and main-conversation row read the same name as the page's top bar and the switcher (#6401):
    // the current manager's name, with the persona pack's name only as the no-roster fallback.
    val drawerRoster = LocalManagerRoster.current
    val drawerChoice by persona.core.contracts.api.AssistantManagerChoice.current.collectAsStateWithLifecycle()
    val assistantName = drawerRoster.assistantDisplayName(drawerChoice.shown, LocalPersonaName.current)
    var searchVisible by rememberSaveable { mutableStateOf(false) }
    val drawer = LocalHomeDrawer.current
    val resources = androidx.compose.ui.platform.LocalResources.current
    Column(Modifier.fillMaxHeight().padding(horizontal = if (drawer) Tokens.Space.s else 12.dp.dense)) {
        val headPadding = Modifier.padding(horizontal = if (drawer) Tokens.Space.l else Tokens.Space.xs).padding(top = Tokens.Space.s)
        val search: @Composable () -> Unit = {
            JIconButton(PhIcons.MagnifyingGlass, stringResource(R.string.assistant_search_chats), { searchVisible = !searchVisible }, raised = true)
        }
        // The head as it was (the persona's name; only the compact drawer has one), where there is no switcher to show.
        val plainHead: @Composable () -> Unit = {
            if (drawer) Row(Modifier.fillMaxWidth().then(headPadding), verticalAlignment = Alignment.CenterVertically) {
                Text(assistantName, style = PersonaTheme.type.largeTitle, color = PersonaTheme.palette.ink, modifier = Modifier.weight(1f))
                search()
            }
        }
        if (switcher != null) switcher(headPadding, search.takeIf { drawer }, plainHead) else plainHead()
        LazyColumn(Modifier.weight(1f), contentPadding = PaddingValues(bottom = 12.dp.dense)) {
            // The first row is the main conversation, as "Persona" with the app's mark, then a line (#2004, the same on
            // every platform): it is listed nowhere else, has no pin of its own, and is on show whatever the search.
            // Its session keeps its id and title; only the row reads differently.
            item(key = "persona") {
                val mainHit = found?.firstOrNull { it.session.key == groups.main?.key }?.hit
                PersonaRow(groups.main, selected = groups.main != null && groups.main.key == active, hasMain = hasMain,
                    name = assistantName,
                    onMain = {
                        val main = groups.main
                        if (main != null && mainHit != null) MessageJumps.request(main.machineId, main.sessionId, mainHit.itemId)
                        onMain()
                    }, onEnd = groups.main?.let { m -> onEnd?.let { action -> { action(m) } } },
                    onCopy = { groups.main?.let(onCopy) },
                    onDiagnostics = groups.main?.let { m -> onDiagnostics?.let { action -> { action(m) } } },
                    said = mainHit?.let(::markedSnippet))
                JDivider(Modifier.padding(vertical = Tokens.Space.s))
            }
            if (!drawer) item { JSectionHeader(stringResource(R.string.assistant_more)) }
            // The compact drawer keeps only iPhone destinations; extra actions remain in the selected Assistant menu.
            val totalMore = (if (drawer) 2 else 4) + (if (showTodos && onTodos != null) 1 else 0) +
                (if (onInbox != null) 1 else 0) + (if (onHealth != null) 1 else 0) + pages.size
            // Each row's place in the group is taken while the list is built: counted inside an item's own content, it
            // was counted again whenever only some rows recomposed, and the group's corners landed on the wrong rows.
            var moreIndex = 0
            fun more(content: @Composable () -> Unit) {
                val position = GroupPosition.of(moreIndex++, totalMore)
                item { JGroupItem(position) { content() } }
            }
            if (onInbox != null) more { DrawerItem(stringResource(R.string.assistant_origin_inbox), PhIcons.Tray, badge = inboxCount, onClick = onInbox) }
            if (showTodos && onTodos != null) more { DrawerItem(stringResource(R.string.assistant_todos), PhIcons.ListChecks, badge = todoCount, onClick = onTodos) }
            more { DrawerItem(stringResource(AssistantInfo.Schedule.title), PhIcons.Clock) { onInfo(AssistantInfo.Schedule) } }
            more { DrawerItem(stringResource(AssistantInfo.Knowledge.title), PhIcons.BookOpen) { onInfo(AssistantInfo.Knowledge) } }
            if (onHealth != null) more { DrawerItem(stringResource(R.string.assistant_health), PhIcons.Heart, onHealth) }
            if (!drawer) {
                more { DrawerItem(stringResource(AssistantInfo.Session.title), PhIcons.Info) { onInfo(AssistantInfo.Session) } }
                more { DrawerItem(stringResource(AssistantInfo.Launcher.title), PhIcons.RocketLaunch) { onInfo(AssistantInfo.Launcher) } }
            }
            pages.forEach { page -> more {
                JRow(page.label, onClick = { onPage(page.key) }, leading = { JIcon(page.icon, null) },
                    chevron = true,
                    trailing = {
                        if (page.badge > 0) JBadge(page.badge)
                        else if (page.dot) JStatusDot(Status.NeedsYou, breathes = false)
                    })
            } }
            if (drawer) item { JDivider(Modifier.padding(vertical = Tokens.Space.s)) }
            if (!drawer || searchVisible || query.isNotBlank()) item { JSearchField(query, onQuery, stringResource(R.string.assistant_search_chats)) }
            if (searchPartial) item {
                Text(stringResource(R.string.assistant_search_partial),
                    Modifier.padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s),
                    style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            }
            if (found != null) {
                val rows = found.filter { groups.main == null || it.session.key != groups.main.key }
                itemsIndexed(rows, key = { _, row -> "found-" + row.session.key.path }) { index, row ->
                    ConversationRow(row.session, selected = row.session.key == active,
                        position = GroupPosition.of(index, rows.size),
                        onOpen = {
                            row.hit?.let { MessageJumps.request(row.session.machineId, row.session.sessionId, it.itemId) }
                            onOpen(row.session.key)
                        },
                        onEnd = onEnd?.let { action -> { action(row.session) } },
                        onCopy = { onCopy(row.session) },
                        onArchive = onArchive?.let { action -> { action(row.session) } },
                        onRename = onRename?.let { action -> { action(row.session) } },
                        onDiagnostics = onDiagnostics?.let { action -> { action(row.session) } },
                        onLeader = onLeader?.let { action -> { action(row.session) } },
                        said = row.hit?.let(::markedSnippet))
                }
                if (rows.isEmpty()) item {
                    JEmptyState(stringResource(R.string.assistant_no_match), stringResource(R.string.assistant_no_match_detail))
                }
            } else {
            // The leader conversations, "常驻" (#4331), under their names, each saying what it has open. Whether a chat
            // is one is the hub's answer for every device: the row's menu makes it one or ends that.
            if (groups.directions.isNotEmpty()) item(key = "directions-header") {
                JSectionHeader(stringResource(R.string.assistant_directions), count = groups.directions.size)
            }
            itemsIndexed(groups.directions, key = { _, session -> "direction-" + session.key.path }) { index, session ->
                // With the hub's direction tree a lead folds open to its to-dos and their Agents (#4352, DirectionRows.kt).
                DirectionLeadRows(groups.tree?.direction(session.sessionId)?.takeUnless { it.secretary },
                    groups.names[session.sessionId] ?: session.conversationTitle(conversationWords()),
                    GroupPosition.of(index, groups.directions.size), onOpenTodo, onOpenAgent) { position, disclosure ->
                    ConversationRow(session, selected = session.key == active,
                        position = position,
                        onOpen = { onOpen(session.key) }, onEnd = onEnd?.let { action -> { action(session) } },
                        onCopy = { onCopy(session) }, onArchive = onArchive?.let { action -> { action(session) } },
                        onRename = onRename?.let { action -> { action(session) } },
                        onDiagnostics = onDiagnostics?.let { action -> { action(session) } },
                        leader = true, onLeader = onLeader?.let { action -> { action(session) } },
                        counts = leaderCountsLabel(groups.counts[session.sessionId], resources),
                        working = groups.counts[session.sessionId]?.running == true,
                        named = groups.names[session.sessionId], disclosure = disclosure)
                }
            }
            // The newest eligible chats (#4922, #5196), the same rows and menu; All chats below still lists everything.
            // The heading folds them, and the choice is kept on this device (#6228).
            if (groups.recent.isNotEmpty()) item(key = "recent-header") {
                JSectionHeader(stringResource(R.string.assistant_recent),
                    onClick = { recentFolded = !recentFolded; foldStore.setRecentFolded(recentFolded) }, open = recentOpen,
                    action = { JIcon(if (recentOpen) PhIcons.CaretUp else PhIcons.CaretDown, null,
                        size = Tokens.Space.l, tint = PersonaTheme.palette.ink3) })
            }
            if (recentOpen) itemsIndexed(groups.recent, key = { _, session -> "recent-" + session.key.path }) { index, session ->
                ConversationRow(session, selected = session.key == active,
                    position = GroupPosition.of(index, groups.recent.size),
                    onOpen = { onOpen(session.key) }, onEnd = onEnd?.let { action -> { action(session) } },
                    onCopy = { onCopy(session) }, onArchive = onArchive?.let { action -> { action(session) } },
                    onRename = onRename?.let { action -> { action(session) } },
                    onDiagnostics = onDiagnostics?.let { action -> { action(session) } },
                    onLeader = onLeader?.let { action -> { action(session) } })
            }
            // Every other chat is under one "All chats" row, which opens the list in place (a search lists what it finds
            // at once instead; the drawer's search finds every conversation either way).
            val searching = query.isNotBlank()
            if (!searching && groups.others.isNotEmpty()) item(key = "all-chats") {
                JGroupItem(GroupPosition.Single) {
                    DrawerItem(pluralStringResource(R.plurals.assistant_show_all_recent, groups.others.size, groups.others.size),
                        if (showAllChats) PhIcons.CaretDown else PhIcons.ChatsCircle, onClick = { showAllChats = !showAllChats })
                }
            }
            if (searching || showAllChats) itemsIndexed(groups.others, key = { _, session -> session.key.path }) { index, session ->
                // The background supervisor (#4927) is built in: listed here with the chats Persona opened itself, under
                // its own name, and its menu only copies (the hub refuses to pin, archive or end it; its row shows the
                // built-in name, so a rename would show nowhere).
                val builtIn = session.sessionId == groups.background
                ConversationRow(session, selected = session.key == active,
                    position = GroupPosition.of(index, groups.others.size),
                    onOpen = { onOpen(session.key) }, onEnd = onEnd?.takeUnless { builtIn }?.let { action -> { action(session) } },
                    onCopy = { onCopy(session) }, onArchive = onArchive?.takeUnless { builtIn }?.let { action -> { action(session) } },
                    onRename = onRename?.takeUnless { builtIn }?.let { action -> { action(session) } },
                    onDiagnostics = onDiagnostics?.let { action -> { action(session) } },
                    onLeader = onLeader?.takeUnless { builtIn }?.let { action -> { action(session) } },
                    named = if (builtIn) stringResource(R.string.assistant_background_supervisor) else null,
                    origin = if (builtIn) AssistantConversationOrigin.Task.id else session.origin)
            }
            // Archived (N) at the end, where the archived conversations are (iOS `archivedRow`); a search lists only what it finds.
            if (!searching && archivedCount > 0 && onArchived != null) item(key = "archived") {
                JGroupItem(GroupPosition.Single) {
                    DrawerItem(stringResource(R.string.assistant_archived_count, archivedCount), PersonaIcons.archive, onClick = onArchived)
                }
            }
            if (groups.directions.isEmpty() && groups.others.isEmpty() && (groups.main == null || query.isNotBlank())) {
                item { JEmptyState(stringResource(if (query.isBlank()) R.string.assistant_empty_title else R.string.assistant_no_match),
                    stringResource(if (query.isBlank()) R.string.assistant_empty_detail else R.string.assistant_no_match_detail)) }
            }
            }

        }
        Row(Modifier.then(if (drawer) Modifier.padding(horizontal = Tokens.Space.l) else Modifier)
            .padding(vertical = if (drawer) Tokens.Space.m else Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
            JSidebarNewChat(stringResource(R.string.assistant_new_conversation), onNew, if (drawer) Modifier else Modifier.weight(1f))
            if (drawer) Spacer(Modifier.weight(1f))
            Box {
                JIconButton(PhIcons.Gear, stringResource(R.string.assistant_open_settings), onSettings, raised = drawer)
                when {
                    settingsBadge != null -> JTag(settingsBadge, Modifier.align(Alignment.TopEnd))
                    settingsCount > 0 -> JBadge(settingsCount, Modifier.align(Alignment.TopEnd))
                    settingsDot -> JStatusDot(Status.NeedsYou, Modifier.align(Alignment.TopEnd), breathes = false) // still, as iOS's `ExtensionSettingsBadge`
                }
            }
        }
    }
}

/**
 * One conversation of the history (iOS `AssistantSidebar.row`). A swipe left archives it ([onArchive]; not the main one).
 * Its menu (⋯, or a long press) shows what it last said and when, over iOS's items: Pin / Unpin ([onLeader]), Rename…
 * ([onRename]), Archive, Copy session ID, Copy diagnostics ([onDiagnostics]) and End… while it runs ([onEnd]). [counts]
 * is what the leader has open (#4331).
 */
@Composable
private fun ConversationRow(
    session: Session, selected: Boolean, onOpen: () -> Unit, onEnd: (() -> Unit)?,
    modifier: Modifier = Modifier, position: GroupPosition = GroupPosition.Single,
    leader: Boolean = false, onLeader: (() -> Unit)? = null, onCopy: () -> Unit = {}, onArchive: (() -> Unit)? = null,
    onRename: (() -> Unit)? = null, onDiagnostics: (() -> Unit)? = null,
    counts: String? = null, named: String? = null, origin: String? = session.origin,
    /** A 常驻 lead's caret that folds its to-dos open (#4352, DirectionRows.kt), before its ⋯; null: none. */
    disclosure: (@Composable () -> Unit)? = null,
    /** Any Agent under this lead is working, even if the conversation's own turn is idle (#6230): the row spins. */
    working: Boolean = false,
    /** The sentence a search found, its match marked. Replaces the last message (and, on the compact drawer, the counts). */
    said: AnnotatedString? = null,
) {
    var menu by remember { mutableStateOf(false) }
    // What opened an automatic chat takes precedence; a user's own chat keeps its latest message's channel mark (#5196).
    val source = automaticConversationOrigin(origin)
    val channels = LocalChannels.current
    val channel = remember(session.channels, channels) { ChannelSources.latest(session.channels, channels) }
    val sourceMark: (@Composable () -> Unit)? = source?.let { s -> { ConversationOriginMark(s) } }
        ?: channel?.let { c -> { JChannelMark(c, size = MarkSize.Inline) } }
    val archivedLabel = if (session.archived) stringResource(R.string.assistant_archived) else null
    val accessory: (@Composable () -> Unit)? = if (sourceMark == null && archivedLabel == null) null else {
        {
            archivedLabel?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3, maxLines = 1) }
            sourceMark?.invoke()
        }
    }
    // [named]: a direction's name, shown instead of its leader conversation's title
    val title = named ?: session.conversationTitle(conversationWords())
    // One activity mark for the whole row (#6230): an own needs-you / unread / working turn still wins; otherwise a lead
    // with anything running under it (the tree's `running`, an Agent at work, #6626) spins.
    val ownStatus = session.conversationStatus()
    val leadActive = working && ownStatus != Status.NeedsYou && ownStatus != Status.Unread && ownStatus != Status.Working
    // A hub title's day and kind ("1101 | Fix | ") in the quiet ink, so the topic is what the eye finds (iOS `titleText`); Glass.
    val dated = if (PersonaTheme.style == AppStyle.Glass) ConversationTitle.parse(title) else null
    val resources = androidx.compose.ui.platform.LocalResources.current
    val swipe = onArchive?.let { SwipeAction(stringResource(R.string.assistant_archive), PersonaIcons.archive, Status.Working, it) }
    if (LocalHomeDrawer.current) JSwipeActions(modifier, end = swipe) {
        JSidebarHistoryRow(dated?.topic ?: title, ownStatus, selected, onOpen, { menu = true },
            stringResource(R.string.assistant_conversation_actions), hint = if (said == null) counts else null, hintMark = said,
            titleAccessory = accessory, titleLead = dated?.head, trailingAccessory = disclosure, working = leadActive)
    } else JSwipeActions(modifier, end = swipe) { JGroupItem(position) {
        // The time goes on the line under the title, with the state: beside the title it left the title two or three
        // characters in the wide window's 300dp list (2026-11-02), and a narrow drawer cut it short too.
        JRow(dated?.topic ?: title, onClick = onOpen, onLongClick = { menu = true },
            titleAccessory = accessory, titleLead = dated?.head,
            subtitle = if (said == null) session.lastText?.takeIf { it.isNotBlank() } else null,
            subtitleMark = said,
            leading = {
                if (leadActive) JSpinner(Modifier.size(Tokens.Size.iconXS), color = PersonaTheme.palette.accent, stroke = SpinnerTokens.compactStroke)
                else JIcon(if (selected) PhIcons.CheckCircle else PhIcons.ChatCircle, null,
                    selected = selected, tint = if (selected) PersonaTheme.palette.accent else PersonaTheme.palette.ink2)
            },
            trailing = {
                disclosure?.invoke()
                JIconButton(PhIcons.DotsThree, stringResource(R.string.assistant_conversation_actions), { menu = true })
            }, chevron = false)
        // Under the title, not the icon: a fixed 16dp put it under the icon in glass and board and past it in brief.
        Row(Modifier.padding(start = rowTitleStart, end = rowPadding, bottom = 10.dp.dense),
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
            StatusLabel(ownStatus)
            counts?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3, maxLines = 1) }
            historyTime(session.updated, resources)?.let { time ->
                Text(time, Modifier.weight(1f), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3,
                    maxLines = 1, overflow = TextOverflow.Ellipsis, textAlign = TextAlign.End)
            }
        }
    } }
    if (menu) JActionSheet(
        title = title, onDismiss = { menu = false },
        actions = conversationActions(androidx.compose.ui.platform.LocalResources.current, leader, session.isLive, onLeader, onCopy, onEnd,
            onArchive, onRename = onRename, onDiagnostics = onDiagnostics, glass = PersonaTheme.style == AppStyle.Glass),
        // The long press's preview (iOS's context menu): what it last said (the row shows only the title) and when.
        preview = session.lastText?.let { persona.core.domain.plainMarkdown(it) }?.takeIf { it.isNotBlank() },
        previewCaption = historyTime(session.updated, resources),
    )
}

/** What opened an automatic chat: Lark's official brand for /agent, otherwise the source's icon (#5196). */
@Composable
private fun ConversationOriginMark(source: AssistantConversationOrigin) {
    val label = source.accessibilityLabel(androidx.compose.ui.platform.LocalResources.current)
    if (source == AssistantConversationOrigin.LarkAgent) {
        JSourceMark(label, brand = Brand.Lark, size = MarkSize.Inline)
    } else {
        val icon = when (source) {
            AssistantConversationOrigin.Inbox -> PersonaIcons.inbox
            AssistantConversationOrigin.Task -> PersonaIcons.schedule
            AssistantConversationOrigin.LarkAgent -> PersonaIcons.mention
            AssistantConversationOrigin.Spawned -> PersonaIcons.forkChat
            AssistantConversationOrigin.Extension -> PersonaIcons.plugins
            AssistantConversationOrigin.Other -> PersonaIcons.ai
        }
        JIcon(icon, label, size = Tokens.Size.markS, tint = PersonaTheme.palette.ink2)
    }
}

/**
 * The history row's menu, in iOS's order (`AssistantSidebar.row`): "Pin" / "Unpin" (making a chat a leader or ending
 * that, when it can), "Rename…", "Archive" (when it can be), "Copy session ID", "Copy diagnostics", then "End…" while it
 * runs. Each is there only with its action. [glass]: iOS's icons for the copy and End… (decision 8: Brief and Board keep
 * theirs).
 */
internal fun conversationActions(
    resources: android.content.res.Resources, leader: Boolean, live: Boolean, onLeader: (() -> Unit)?, onCopy: () -> Unit,
    onEnd: (() -> Unit)?, onArchive: (() -> Unit)? = null, onRename: (() -> Unit)? = null, onDiagnostics: (() -> Unit)? = null,
    glass: Boolean = false,
): List<JAction> =
    buildList {
        onLeader?.let { add(JAction(resources.getString(if (leader) R.string.assistant_end_leader else R.string.assistant_make_leader), PersonaIcons.pin, onClick = it)) }
        onRename?.let { add(JAction(resources.getString(R.string.assistant_rename_menu), PersonaIcons.edit, onClick = it)) }
        onArchive?.let { add(JAction(resources.getString(R.string.assistant_archive), PersonaIcons.archive, onClick = it)) }
        add(JAction(resources.getString(R.string.assistant_copy_session_id), if (glass) PersonaIcons.id else PhIcons.Copy, onClick = onCopy))
        onDiagnostics?.let { add(JAction(resources.getString(R.string.assistant_copy_diagnostics), PersonaIcons.diagnostics, onClick = it)) }
        if (onEnd != null && live) add(JAction(resources.getString(R.string.assistant_end_menu), if (glass) PersonaIcons.end else PhIcons.Stop,
            destructive = true, separatorBefore = true, onClick = onEnd))
    }

/**
 * The drawer's first row: the main conversation ([main]; null before it has started, or not yet loaded), read as
 * Persona's name after the app's mark. Beside it what it waits on (its pending count, else working / needs you /
 * unread); its menu (⋯ or a long press) copies its session id and its diagnostics, and ends it while it runs (iOS
 * `mainItem`). It cannot be pinned, renamed, archived or swiped.
 */
@Composable
private fun PersonaRow(main: Session?, selected: Boolean, hasMain: Boolean, name: String, onMain: () -> Unit, onEnd: (() -> Unit)?, onCopy: () -> Unit,
    onDiagnostics: (() -> Unit)? = null, said: AnnotatedString? = null) {
    var menu by remember { mutableStateOf(false) }
    val status = main?.conversationStatus()
    val pending = main?.pendingInteractions ?: 0
    JRow(name, onClick = onMain, onLongClick = main?.let { { menu = true } }, selected = selected,
        // "Main chat" under the name keeps the row readable as a conversation, not a brand heading (#6327, as the web, #6293).
        // A search that found this chat shows that sentence instead, and only then.
        subtitle = if (said == null) stringResource(if (main == null && !hasMain) R.string.assistant_start_main else R.string.assistant_main_chat) else null,
        subtitleMark = said,
        leading = { JAppMark(Tokens.Space.xl) }, chevron = false,
        trailing = if (main == null) null else ({
            Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
                if (pending > 0) JBadge(pending)
                else if (status == Status.Working || status == Status.NeedsYou || status == Status.Unread) StatusLabel(status)
                JIconButton(PhIcons.DotsThree, stringResource(R.string.assistant_conversation_actions), { menu = true })
            }
        }))
    if (menu && main != null) JActionSheet(
        title = name, onDismiss = { menu = false },
        actions = conversationActions(androidx.compose.ui.platform.LocalResources.current, leader = false, live = main.isLive,
            onLeader = null, onCopy = onCopy, onEnd = onEnd, onDiagnostics = onDiagnostics, glass = PersonaTheme.style == AppStyle.Glass),
    )
}

@Composable
private fun DrawerItem(title: String, icon: PhIcon, onClick: () -> Unit) {
    JRow(title, leadingIcon = icon, onClick = onClick)
}

@Composable
private fun DrawerItem(title: String, icon: PhIcon, badge: Int, onClick: () -> Unit) {
    JRow(
        title,
        leadingIcon = icon,
        onClick = onClick,
        trailing = if (badge > 0) ({ JBadge(badge) }) else null
    )
}

@Composable
internal fun AssistantDemoNotice() = JNotice(stringResource(R.string.assistant_demo_notice), title = stringResource(R.string.assistant_demo_title), icon = PhIcons.Info)

@Composable
internal fun AssistantDemo(navigator: Navigator) {
    val words = demoWords()
    val repository = remember { DemoAssistantRepository(words) }
    val messages by repository.outbox.collectAsStateWithLifecycle()
    AssistantScreen(navigator, repository, repository, repository::start, demo = true,
        chatContent = { session ->
            LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(16.dp.dense), verticalArrangement = Arrangement.spacedBy(12.dp.dense)) {
                items(messages.filter { it.session == session }, key = { it.clientMessageId }) { message ->
                    UserBubble(message.text)
                }
                item { Text(stringResource(R.string.assistant_demo_reply), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2) }
            }
        })
}
