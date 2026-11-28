package persona.feature.assistant

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.AssistantConfigUpdate
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.AssistantSnapshot
import persona.core.designsystem.*

/**
 * Which sidebar row stays shaded for the page root beside the column (#9667).
 * iOS `UnifiedSidebar.shownPlace` (UnifiedSidebar.swift:39–44); [None] has no place row.
 */
object AssistantSidebarShade {
    const val Conversation = "conversation"
    const val Starred = "starred"
    const val Inbox = "inbox"
    const val Messages = "messages"
    const val Todos = "todos"
    const val Knowledge = "knowledge"
    const val Machines = "machines"
    const val Schedule = "schedule"
    const val Health = "health"
    const val None = "none"
    /** The new space form (#13500): [space] of [SpacesModel.NEW], a word no space's address may be. */
    const val NewSpace = "space:new"

    fun agent(key: SessionKey) = "agent:${key.path}"
    /** A domain space's row (#13500). */
    fun space(slug: String) = "space:$slug"
}

/** The Agents list supplies the scroll viewport and places the footer below its Needs You jump. */
class MergedSidebarContent(
    val list: @Composable (LazyListState, LazyListScope.() -> Unit, @Composable () -> Unit, () -> Unit) -> Unit,
    val usage: @Composable () -> Unit,
    val agents: List<Session>,
    val agentMachines: Map<String, String>,
    val pinnedAgents: Set<String>,
    val onSearchAgent: (SessionKey) -> Unit,
    val agentsInFront: Boolean,
    val showKnowledge: Boolean,
    val herdrTabNameHubs: Set<String?> = emptySet(),
)

/** The real shared history remains available even while a secondary page hides the Assistant root. */
@Composable
fun AssistantSidebarContent(
    snapshot: AssistantSnapshot,
    repository: AssistantRepository,
    active: SessionKey?,
    operationScope: CoroutineScope,
    demo: Boolean,
    onOpen: (SessionKey) -> Unit,
    onAction: (AssistantSidebarAction) -> Unit,
    onSettings: () -> Unit,
    onInbox: () -> Unit,
    onKnowledge: () -> Unit,
    onTodos: (() -> Unit)?,
    todoCount: Int,
    showTodos: Boolean,
    inboxCount: Int,
    settingsCount: Int,
    settingsDot: Boolean,
    settingsBadge: String?,
    pages: List<NavItem>,
    onPage: (String) -> Unit,
    onRetired: (SessionKey) -> Unit,
    /** A to-do's card / an Agent's session, from the Directions tree (#4352); null: those rows only fold. */
    onOpenTodo: ((String) -> Unit)? = null,
    onOpenAgent: ((SessionKey) -> Unit)? = null,
    switcher: AssistantSwitcherSlot? = null,
    /**
     * A regular window (open foldable / tablet): Search opens over the whole window as the open iPhone Duo (#9171).
     * Compact windows keep the drawer's inline search.
     */
    fullWindowSearch: Boolean = false,
    /**
     * Which row stays shaded for the visible root, including while drilled in and for an Agent on either tab
     * (iOS `UnifiedSidebar.shownPlace`, UnifiedSidebar.swift:39–44).
     * Null keeps the compact Persona drawer's conversation shading.
     */
    shadedPlace: String? = null,
    /** When the sidebar is a column beside the page, folds the column from its own header; null in a drawer or overlay. */
    onFoldColumn: (() -> Unit)? = null,
    merged: MergedSidebarContent? = null,
    onSchedule: (() -> Unit)? = null,
    /**
     * The selected hub group's management, scoped to the shown manager here. The shell draws this sidebar outside
     * `AssistantAppHost`, so [LocalAssistantHost] is null there; without this the conversation menu keeps only the two
     * copy actions (#17123). Null: the composition local, as an assistant-hosted page has it.
     */
    management: AssistantManagement? = LocalAssistantHost.current?.management,
) {
    val choice by AssistantManagerChoice.current.collectAsStateWithLifecycle()
    val received = snapshot.takeIf { it.managerId == choice.shown }
        ?: AssistantSnapshot(manager = choice.manager, managerId = choice.shown)
    val base = management ?: LocalAssistantHost.current?.management
    val management = remember(base, choice.hubId, choice.shown) { base?.forManager(choice.shown) }
    // The domain spaces (#13500): the page opens them, so they show only where the page is the real assistant's.
    val sidebarHost = LocalAssistantSidebarHost.current
    val spacesEnabled = LocalSpaceConversationsEnabled.current?.collectAsStateWithLifecycle()?.value == true
    val spaces = rememberSpaces(choice.hubId, base,
        enabled = spacesEnabled && !demo && sidebarHost != null && repository is persona.core.contracts.api.AssistantConversations)
    val conversationArranger = remember(management, repository, demo) {
        management?.takeUnless { demo }?.let { api ->
            ConversationArranger(
                prepare = { repository.conversationOrderUpdater() },
                write = { order ->
                    api.updateConfig(AssistantConfigUpdate(conversationOrder = order)).conversationOrder
                },
            )
        }
    }
    val conversationArrangeUi = conversationArranger?.ui?.collectAsStateWithLifecycle()?.value ?: ConversationArrangeUi()
    val resources = LocalResources.current
    val copy = rememberCopy()
    val feedback = LocalAppFeedback.current
    val toast = rememberToastState()
    val haptics = persona.core.designsystem.rememberHaptics()
    val archivedWords = stringResource(R.string.assistant_archived)
    val words = conversationWords()
    var query by rememberSaveable(choice.hubId, choice.shown) { mutableStateOf("") }
    var fullSearchOpen by rememberSaveable(choice.hubId, choice.shown) { mutableStateOf(false) }
    var searchAgentsFirst by rememberSaveable(choice.hubId, choice.shown) { mutableStateOf(false) }
    fun openSearch(agentsFirst: Boolean) {
        searchAgentsFirst = agentsFirst
        fullSearchOpen = true
    }
    LaunchedEffect(fullWindowSearch) {
        if (fullWindowSearch) query = "" else fullSearchOpen = false
    }
    var ended by remember(management) { mutableStateOf(emptySet<SessionKey>()) }
    var archived by remember(management) { mutableStateOf(emptySet<SessionKey>()) }
    // Rename… and Archived (N) (iOS `AssistantSidebar`): titles given here and the archived conversations.
    val history = rememberConversationHistory(management?.takeUnless { demo }, repository)
    val openKeys = remember(received.conversations) { openKeys(received.conversations) }
    var ending by remember { mutableStateOf<Pair<Session, AssistantManagement>?>(null) }
    var busy by remember { mutableStateOf(false) }
    var leaderBusy by remember { mutableStateOf(false) }
    // The 设为常驻 / 取消常驻 menu asks before the hub call (#6214).
    var leaderConfirm by remember { mutableStateOf<Session?>(null) }
    var failure by remember { mutableStateOf<String?>(null) }
    fun failed(error: Exception) {
        failure = operationFeedback(error).text.resolve(resources)
        feedback?.failure(error)
    }
    // A chat is made a leader (常驻) or stops being one on the hub, for every device (#4331).
    fun setLeader(session: Session) {
        val target = management?.takeUnless { demo } ?: return
        if (leaderBusy) return
        leaderBusy = true
        val leader = session.sessionId in received.leaders
        operationScope.launch {
            try {
                if (leader) target.endDirection(session.key) else target.makeDirection(session.key)
                repository.refresh()
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { failed(error) }
            finally { leaderBusy = false }
        }
    }
    val orderedSnapshot = if (conversationArrangeUi.arranging || conversationArrangeUi.pending > 0) {
        received.copy(conversationOrder = conversationArrangeUi.order)
    } else received
    val groups = remember(orderedSnapshot, query, ended, archived, words, history.titles, history.tagEdits, history.activeTag) {
        conversationGroups(orderedSnapshot.copy(conversations = history.tagged(orderedSnapshot.conversations.filter { it.key !in archived }.map {
            if (it.key in ended) it.copy(status = "offline", state = "offline") else it
        })), query, words, activeTag = history.activeTag)
    }
    val latestArrangeActivityIds = rememberUpdatedState(groups.others.map { it.sessionId })
    val beginArrange: (() -> Unit)? = conversationArranger?.let { controller ->
        {
            query = ""
            fullSearchOpen = false
            history.activeTag?.let(history::selectTag)
            operationScope.launch {
                controller.start(if (conversationArrangeUi.pending > 0) conversationArrangeUi.order else received.conversationOrder)
            }
        }
    }
    LaunchedEffect(conversationArranger, resources, feedback) {
        val controller = conversationArranger ?: return@LaunchedEffect
        controller.events.collect { event ->
            when (event) {
                is ConversationArrangeEvent.Failed -> failed(event.error)
                is ConversationArrangeEvent.Reset -> {
                    val message = resources.getString(R.string.assistant_arrange_reset_done)
                    val undo = FeedbackAction(resources.getString(R.string.assistant_arrange_undo)) {
                        operationScope.launch { controller.undoReset(event.revision, latestArrangeActivityIds.value) }
                    }
                    if (feedback?.notice(message, undo) != true) toast.show(FeedbackNotice(message, undo))
                }
            }
        }
    }
    val searchOn = management != null && !demo && AssistantSearchFeature in LocalHubFeatures.current
    val openChats = remember(received.conversations, ended, archived, history.titles, history.tagEdits) {
        history.tagged(received.conversations.filter { !it.archived && it.key !in archived }.map {
            if (it.key in ended) it.copy(status = "offline", state = "offline") else it
        })
    }
    val knownChats = remember(received.conversations, ended, archived, history.titles, history.tagEdits, history.archived, history.restoring) {
        val listed = received.conversations.filter { it.key !in archived }.map {
            if (it.key in ended) it.copy(status = "offline", state = "offline") else it
        }
        history.tagged((listed + history.shownArchived(openKeys(openChats))).distinctBy { it.sessionId })
    }
    val tagChoices = remember(knownChats) { inUseConversationTags(knownChats) }
    val (foundChats, searchPartial) = rememberFoundChats(
        query, remember(groups) { (groups.directions + groups.others).distinctBy { it.sessionId } },
        knownChats.withConversationTag(history.activeTag), searchOn, management, "${choice.hubId.orEmpty()}|${choice.shown}",
        activeTag = history.activeTag)
    val fullSearchChats = remember(openChats, groups.directions, groups.externalDirections) {
        openChats + groups.directions.filter { it.sessionId in groups.externalDirections }
    }
    Column(Modifier.fillMaxHeight()) {
        failure?.let { JNotice(it, status = Status.NeedsYou) }
        Box(Modifier.weight(1f)) {
            val visibleGroups = if (merged == null) groups else groups.copy(
                main = groups.main ?: openChats.firstOrNull { it.key == received.mainSession },
                others = if (conversationArrangeUi.arranging) groups.others
                    else groups.others.filterNot { chat -> groups.recent.any { it.key == chat.key } },
            )
            AssistantDrawer(query, { query = it }, visibleGroups, active = active, hasMain = received.mainSession != null,
                onEnd = management?.takeUnless { demo }?.let { target -> { session: Session -> ending = session to target } },
                onMain = { received.mainSession?.let(onOpen) ?: onAction(AssistantSidebarAction.MainConversation) },
                onOpen = onOpen,
                onInfo = { topic -> when (topic) {
                    AssistantInfo.Knowledge -> onKnowledge()
                    AssistantInfo.Schedule -> if (onSchedule != null) onSchedule() else onAction(AssistantSidebarAction.ScheduledTasks)
                    AssistantInfo.Session -> onAction(AssistantSidebarAction.SessionInfo)
                    AssistantInfo.Launcher, AssistantInfo.Settings -> onAction(AssistantSidebarAction.Launcher)
                } },
                onNew = { onAction(AssistantSidebarAction.NewConversation) },
                // The health space (docs/assistant/health.md): the Assistant page opens its newest chat or its new-chat page.
                onHealth = if (demo || (merged != null && repository !is persona.core.contracts.api.AssistantConversations)) null
                    else ({ onAction(AssistantSidebarAction.Health) }),
                onSettings = onSettings,
                onInbox = onInbox, inboxCount = inboxCount,
                onCopy = { copy(it.sessionId, resources.getString(R.string.assistant_copied_session_id)) },
                onArchive = management?.takeUnless { demo }?.let { target -> { session: Session ->
                    archived = archived + session.key
                    // As iOS: a tap now, "Archived" once the hub has it (DESIGN_SYSTEM.md 4.3).
                    haptics(persona.core.designsystem.Haptic.Tap)
                    operationScope.launch {
                        try {
                            target.archiveConversation(session.key)
                            feedback?.notice(archivedWords, persona.core.designsystem.FeedbackStyle.Success)
                            // Archiving only hides the row; End below is what retires an open conversation.
                            repository.refresh()
                            archived = archived - session.key
                        } catch (cancelled: CancellationException) { throw cancelled }
                        catch (error: Exception) { archived = archived - session.key; failed(error) }
                    }
                } },
                onLeader = if (management != null && !demo) ({ session: Session ->
                    // Open the confirmation first; setLeader runs only on the second choice.
                    leaderConfirm = session
                }) else null,
                onRename = if (management != null && !demo) ({ session: Session -> history.renaming = session }) else null,
                onEditTags = if (management != null && !demo) ({ session: Session -> history.editingTags = session }) else null,
                onDiagnostics = { copy(conversationDiagnostics(it), resources.getString(R.string.assistant_diagnostics_copied)) },
                archivedCount = history.shownArchived(openKeys).size, onArchived = { history.archivedOpen = true },
                tagChoices = tagChoices, activeTag = history.activeTag, onTag = history::selectTag,
                tagFilterBar = if (management != null && !demo && received.conversationTagFilters.supported) ({
                    ConversationTagFilterBar(
                        entries = conversationTagFilterEntries(tagChoices, received.conversationTagFilters),
                        activeTag = history.activeTag,
                        customized = received.conversationTagFilters.customized,
                        editing = history.editingTagFilters,
                        busy = history.tagFiltersBusy,
                        onSelect = history::selectTag,
                        onChange = { next ->
                            val result = history.saveConversationTagFilters(next)
                            (result.exceptionOrNull() as? Exception)?.let(::failed)
                            result.isSuccess
                        },
                        onEdit = { history.editingTagFilters = true },
                        onDone = { history.editingTagFilters = false },
                    )
                }) else null,
                onTodos = onTodos, todoCount = todoCount, showTodos = showTodos,
                settingsCount = settingsCount, settingsDot = settingsDot, settingsBadge = settingsBadge,
                pages = pages, onPage = onPage, onOpenTodo = onOpenTodo, onOpenAgent = onOpenAgent, switcher = switcher,
                found = foundChats, searchPartial = searchPartial,
                fullWindowSearch = fullWindowSearch,
                onFullSearch = { if (fullWindowSearch) openSearch(merged?.agentsInFront == true) },
                shadedPlace = shadedPlace,
                onFoldColumn = onFoldColumn,
                merged = merged,
                showSchedule = merged == null || (management != null && !demo),
                onAgentSearch = { openSearch(true) },
                arranger = conversationArranger, arrangeScope = operationScope, onArrange = beginArrange,
                spaces = spaces?.sidebar,
                onSpace = { slug -> sidebarHost?.openSpace = slug; onAction(AssistantSidebarAction.Space) },
                onNewSpace = { sidebarHost?.openSpace = SpacesModel.NEW; onAction(AssistantSidebarAction.Space) },
                frontSpace = sidebarHost?.spaceInFront)
            JToastHost(toast, Modifier.align(Alignment.TopCenter))
        }
    }
    if (fullSearchOpen && fullWindowSearch) {
        AssistantFullSearch(
            chats = fullSearchChats,
            known = knownChats,
            searchOn = searchOn,
            management = management,
            hubKey = "${choice.hubId.orEmpty()}|${choice.shown}",
            words = words,
            names = received.directionNames,
            externalDirections = groups.externalDirections,
            backgroundId = received.backgroundSupervisor,
            activeTag = history.activeTag,
            mainKey = received.mainSession,
            onOpen = { key ->
                fullSearchOpen = false
                onOpen(key)
            },
            onDismiss = { fullSearchOpen = false },
            agents = merged?.agents,
            agentMachines = merged?.agentMachines.orEmpty(),
            pinnedAgents = merged?.pinnedAgents.orEmpty(),
            herdrTabNameHubs = merged?.herdrTabNameHubs.orEmpty(),
            agentsFirst = searchAgentsFirst,
            onOpenAgent = if (merged == null && onOpenAgent == null) null else ({ key ->
                fullSearchOpen = false
                if (merged != null) merged.onSearchAgent(key) else onOpenAgent?.invoke(key)
            }),
        )
    }
    ConversationHistoryDialogs(history, received.conversations, operationScope, onOpen = onOpen,
        customizedTagFilters = received.conversationTagFilters.customized,
        notice = { feedback?.notice(it, persona.core.designsystem.FeedbackStyle.Success) }, failure = ::failed)
    ending?.let { (session, target) ->
        JDialog(onDismiss = { if (!busy) ending = null }, title = stringResource(R.string.assistant_end_title),
            text = stringResource(R.string.assistant_end_text, session.conversationTitle(words)),
            destructive = true, confirmEnabled = !busy,
            confirm = stringResource(R.string.assistant_end) to {
                busy = true
                operationScope.launch {
                    try {
                        target.endConversation(session.key)
                        ended = ended + session.key
                        onRetired(session.key)
                        ending = null
                        repository.refresh()
                    } catch (cancelled: CancellationException) { throw cancelled }
                    catch (error: Exception) { failed(error) }
                    finally { busy = false }
                }
            }, dismiss = stringResource(R.string.assistant_cancel) to { if (!busy) ending = null })
    }
    leaderConfirm?.let { session ->
        val leader = session.sessionId in received.leaders
        val title = session.conversationTitle(words)
        JDialog(onDismiss = { if (!leaderBusy) leaderConfirm = null },
            title = stringResource(if (leader) R.string.assistant_end_leader_title else R.string.assistant_make_leader_title),
            text = if (leader) stringResource(R.string.assistant_end_leader_text, title)
                   else stringResource(R.string.assistant_make_leader_text),
            // Reversible (pin again any time), so the confirm is the ordinary primary button, not a destructive one.
            confirmEnabled = !leaderBusy,
            confirm = stringResource(if (leader) R.string.assistant_end_leader else R.string.assistant_make_leader) to {
                // setLeader owns the busy flag, the hub call and the failure notice.
                setLeader(session)
                leaderConfirm = null
            }, dismiss = stringResource(R.string.assistant_cancel) to { if (!leaderBusy) leaderConfirm = null })
    }
}
