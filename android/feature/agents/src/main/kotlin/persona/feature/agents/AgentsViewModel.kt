package persona.feature.agents

import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.contracts.api.ResolveOutcome
import persona.core.domain.answer.*
import persona.core.domain.sort.canAnswerInteraction
import persona.core.domain.sort.*
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.*
import persona.core.designsystem.Haptic
import persona.core.designsystem.ListTapMotion
import persona.core.designsystem.ListTapMotionKind
import persona.feature.chat.InteractionReply
import persona.feature.chat.InteractionSubmission
import persona.feature.chat.classifyInteractionFailure
import persona.feature.chat.isCommandInputPermission
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.time.Instant
import java.util.UUID

private data class GroupListCounts(val filter: SessionListFilter, val revision: String, val counts: SessionListCounts)
/** Older hubs omit the origin breakdown; use the visible projection for those pages. */
private fun SessionListCounts.larkGroups(local: Map<AgentQuickFilter, Int>): Int =
    if (paneSource.isEmpty()) local[AgentQuickFilter.Feishu] ?: 0 else paneSource["feishu"] ?: 0

/** The chats the shell held on entry: one that was Unread, one that was Just finished (#9786). */
private data class HeldChats(val unread: SessionKey?, val finished: SessionKey?)
private data class HeldInputs(val settings: AgentListSettings, val group: GroupFilter?, val assistant: persona.core.contracts.api.CurrentAssistant?,
                              val visible: SessionKey?, val held: HeldChats)
private data class ListInputs(val settings: AgentListSettings, val group: GroupFilter?, val assistant: persona.core.contracts.api.CurrentAssistant?,
                              val visible: SessionKey?, val held: HeldChats, val workspaces: List<AgentWorkspace>)
private data class SeenVisitScope(
    val group: GroupFilter?, val assistantHub: String?, val assistant: String?, val machine: String, val quick: AgentQuickFilter,
    val range: AgentRange,
)
private data class SeenVisitRow(val place: AgentSeenPlace, val version: String)
private data class SeenVisit(
    val scope: SeenVisitScope? = null,
    val candidates: Map<String, SeenVisitRow> = emptyMap(),
    /** Mark All as Seen suppresses that version while the hub returns its receipt. */
    val clearedVersions: Map<String, String> = emptyMap(),
    val revision: Int = 0,
)

/** What the hub's paged search matches: the search's words, not its conditions ([AgentQuery]), which it does not read. */
internal fun hubQuery(query: String): String = AgentQuery.parse(query).text.take(256)

internal data class AgentsPageState(
    val sessions: List<Session> = emptyList(),
    val machines: List<Machine> = emptyList(),
    val preferences: AgentsPreferencesSnapshot = AgentsPreferencesSnapshot(),
    val settings: AgentListSettings = AgentListSettings(),
    val list: AgentListResult = AgentListResult(emptyMap(), emptyList(), emptyList()),
    val allCount: Int = 0,
    /** Every computer for the filter strip, with how many sessions the list would show for it. */
    val machineCounts: List<MachineCount> = emptyList(),
    /** What the list would show for all computers (the "All machines" chip). */
    val machineTotal: Int = 0,
    /** Sessions of each hub group by its namespace, whichever group the list shows (AgentGroups.kt). */
    val groupCounts: Map<String?, Int> = emptyMap(),
    /**
     * False only for the placeholder before the first build: the sessions and the local preferences arrive
     * asynchronously, so until then the list shows no empty state (as the space page's `loaded`, #5377).
     */
    val loaded: Boolean = false,
    val pagination: SessionListStatus = SessionListStatus(),
    /** Built with the list on listContext: sorting every session into spaces in composition stalled the screen. */
    val spaces: List<AgentSpace> = emptyList(),
    val spacesById: Map<String, AgentSpace> = emptyMap(),
    /** Whether the hub has the direction tree (#4352): only then does the menu offer "By direction". */
    val directionsAvailable: Boolean = false,
    /** The tap this page was built from. A throttled page from before the tap carries an older one (#9600). */
    val tapEpoch: Int = 0,
    val hiddenSessions: Set<SessionKey> = emptySet(),
)

/**
 * Where an answer to a request stands; the screen says it (AgentRequests.kt). [Unknown] is checked against the hub
 * and then either ends or becomes [Retry], which lets the request be answered again (RequestDeliveries.kt, #3791).
 */
internal enum class RequestDelivery { Sending, Sent, Unknown, Retry }

internal class AgentsViewModel(
    private val repository: SessionsRepository,
    val interactions: InteractionCenter,
    val connection: HubConnection,
    val sender: MessageSender,
    private val preferences: AgentsPreferences,
    private val bindings: AgentsOperationsBindings? = null,
    private val mainConversationSender: MainConversationSender? = null,
    private val pairingScope: String = bindings?.operations?.scope.orEmpty(),
    private val renamer: SessionRenamer? = null,
    val tasks: persona.core.domain.tasks.TasksStore? = null,
    val hubUpdates: persona.core.contracts.api.HubUpdates? = null,
    val appBuild: Long = 1L,
    machineManagement: MachineManagement? = null,
    /** The phone's hub groups: a request is answered through its own group's connection (AgentGroups.kt stateFor). */
    internal val hubGroups: persona.core.contracts.api.HubGroups = persona.core.contracts.api.HubGroups.None,
    /**
     * Where [page] is built: the app passes [kotlinx.coroutines.Dispatchers.Default], so sorting, filtering and counting
     * every session on each push stays off the main thread. Empty (the caller's thread) keeps tests synchronous.
     */
    listContext: kotlin.coroutines.CoroutineContext = kotlin.coroutines.EmptyCoroutineContext,
    archiveService: AgentArchives? = null,
    private val listAdapter: persona.core.data.AgentListAdapter = persona.core.data.AgentListAdapters.create(),
    /** The shell's visible Agent chat, null while the list itself is on screen. */
    private val visibleChat: kotlinx.coroutines.flow.StateFlow<SessionKey?> = kotlinx.coroutines.flow.MutableStateFlow(null),
    /** Captured by the shell on chat entry, before any read receipt can turn that Unread row idle. */
    private val heldChat: kotlinx.coroutines.flow.StateFlow<SessionKey?> = visibleChat,
    /** Captured by the shell on chat entry as Just finished (#9786), before its seen can take the flag off. */
    private val finishedChat: kotlinx.coroutines.flow.StateFlow<SessionKey?> = kotlinx.coroutines.flow.MutableStateFlow(null),
) : ViewModel() {
    val archives = archiveService?.let { api -> AgentArchivesState(api, viewModelScope) { key, archived ->
        if (archived) bindings?.onSessionClosed?.invoke(key)
        else {
            // Restored offline history must be discoverable even when the list was showing only running agents.
            previous = null
            groupFilter.value = null
            settings.update { it.copy(quick = AgentQuickFilter.All, query = "", machineFilter = "", kept = emptySet(),
                folded = if (it.query.isNotEmpty()) stashedFolds ?: it.folded else it.folded).also { stashedFolds = null } }
            preferences.range(pairingScope, AgentRange.All)
        }
        _notice.value = AgentText(if (archived) R.string.agents_notice_archived else R.string.agents_notice_restored)
        _haptics.tryEmit(Haptic.Tap)
        repository.refresh()
    } }
    private val operations = bindings?.operations?.let { AgentOperationsState(it, preferences.hubCache) }
    internal val sessions: SessionsRepository get() = repository
    internal val operationsApi: AgentOperations? get() = bindings?.operations
    val hasOperations: Boolean get() = operations != null
    val operationState = operations?.state ?: MutableStateFlow(AgentOperationsSnapshot())
    /**
     * Every Agents list on screen (#9600). A foldable's overlay and the page underneath can both be composed
     * during a navigation transition (`Root.kt` `sceneStrategies`, `AgentsOverlay`), and they share this model,
     * so each list registers its own motion instead of one field the later screen would replace.
     */
    private val listTaps = mutableListOf<ListTapMotion>()
    /** Bumped on a tap. The throttled page flow stops waiting, and the page built from the tap carries this. */
    private val tapWake = MutableStateFlow(0)
    private val sending = mutableSetOf<String>()
    private val messageIds = mutableMapOf<String, String>()
    private var changing = false
    private val settings = MutableStateFlow(AgentListSettings())
    private val seenVisit = MutableStateFlow(SeenVisit())
    private val workspaceCatalog = AgentsWorkspaceCatalog()
    private val workspaceRefresh = MutableStateFlow(0)

    /** Called by the visible screen's lifecycle: hidden pages cancel reads and have no polling timer. */
    suspend fun watchWorkspaceCatalog(service: AgentCreationService?, repositoryRows: Boolean = true) {
        if (service == null) return
        fun hosts(machine: persona.core.contracts.Machine) = machine.engineHosts ?: listOf(machine.engine)
        // The space page reads only the Orca rows ([repositoryRows] false): it has no repository grouping.
        combine(repository.machines.map { machines ->
            val paseo = if (repositoryRows) machines.filter { "paseo" in hosts(it) } else emptyList()
            // Orca computers too, for their worktrees' actions (#17054); they add no repository rows.
            paseo.map { it.id to it.online } to
                machines.filter { ORCA_KIND in hosts(it) && it !in paseo && service.worktrees != null }.map { it.id to it.online }
        }.distinctUntilChanged(), workspaceRefresh) { machines, _ -> machines }.collectLatest { (machines, orca) ->
            while (true) {
                workspaceCatalog.refresh(service, machines, orca)
                kotlinx.coroutines.delay(60_000)
            }
        }
    }

    /** The computers' Orca worktrees from the catalog (#17054), for the worktree actions; never part of the list's grouping. */
    internal val orcaWorkspaces get() = workspaceCatalog.orcaRows

    /** Reads the workspace catalog again now (a worktree was created, renamed or removed). */
    fun refreshWorkspaceCatalog() { workspaceRefresh.update { it + 1 } }
    // Editors need the latest settings before the background list projection catches up.
    val listSettings = settings.asStateFlow()
    private val groupListCounts = MutableStateFlow<Map<String, GroupListCounts>>(emptyMap())
    private val countingGroups = mutableSetOf<Triple<String, String, SessionListFilter>>()
    private val groupFilter = MutableStateFlow<GroupFilter?>(null)
    /** The current assistant the list is of (docs/assistant/managers.md "Clients"); null until the page says: every manager's. */
    private val assistant = MutableStateFlow<persona.core.contracts.api.CurrentAssistant?>(null)
    /**
     * Lists [value]'s agents and computers: the sessions and computers stamped with it, the cards that go with them.
     * Another assistant lets go of a computer filter on one that is not its own (else the list would be empty, with
     * no chip picked) and of the rows kept open; the search and the quick filter stay.
     */
    fun show(value: persona.core.contracts.api.CurrentAssistant) {
        val before = assistant.value
        if (before == value) return
        // Another assistant is another list; the same one's roster changing (a count, a name) keeps the rows in place.
        if (before?.id != value.id) previous = null
        assistant.value = value
        if (before?.id != value.id) settings.update { current ->
            // An unknown computer (the list not read yet, on a cold start) keeps the filter it was restored with.
            val machine = repository.machines.value.firstOrNull { it.id == current.machineFilter }
            val foreign = machine != null && !value.ownsOn(machine.id, machine.manager)
            current.copy(machineFilter = if (foreign) "" else current.machineFilter, kept = emptySet())
        }
    }

    /** Every manager's sessions and computers as the hub lists them: a card opened from a notification may be another assistant's. */
    val everySession: StateFlow<List<Session>> get() = repository.sessions
    val everyMachine: StateFlow<List<Machine>> get() = repository.machines
    /** The hub group the list shows; null is every group (AgentGroups.kt). */
    val group = groupFilter.asStateFlow()
    // Written while building [page] (on listContext) and reset from the main thread.
    @Volatile private var previous: AgentListResult? = null
    // Only touched while building [page]: one build at a time, so it needs no more.
    private var previousContext: List<Any?>? = null
    private val _delivery = MutableStateFlow<Map<String, RequestDelivery>>(emptyMap())
    val delivery = _delivery.asStateFlow()
    private val _commandInputErrors = MutableStateFlow<Map<String, InteractionReply>>(emptyMap())
    val commandInputErrors = _commandInputErrors.asStateFlow()
    private val _requestHistory = MutableStateFlow<List<Interaction>>(emptyList())
    val requestHistory = _requestHistory.asStateFlow()
    private val _refreshing = MutableStateFlow(false)
    val refreshing = _refreshing.asStateFlow()
    private val _notice = MutableStateFlow<AgentText?>(null)
    val notice = _notice.asStateFlow()
    val herdr = machineManagement?.let { HerdrStarter(it, repository, connection, hubGroups, viewModelScope) { _notice.value = it } }
    private val _haptics = MutableSharedFlow<Haptic>(extraBufferCapacity = 4)
    /**
     * What the screen plays as the answers land (iOS AppModel.resolve): success; a nudge when the terminal's dialog
     * changed or the hub is out of reach; nothing when it was already answered elsewhere; an error otherwise.
     */
    val haptics = _haptics.asSharedFlow()
    private val _failure = MutableStateFlow<Throwable?>(null)
    val failure = _failure.asStateFlow()
    fun clearFailure() { _failure.value = null }
    private val _hubUpdate = MutableStateFlow<HubUpdateStatus?>(null)
    private val _hubHello = MutableStateFlow<Hello?>(null)
    private val _hubNoticeDismissed = MutableStateFlow<Set<String>>(emptySet())
    /** The group the hub-level pages show: this page stays across a switch in Settings › Hub, its notice must not (#5725). */
    private val selectedGroup = hubGroups.groups.map { groups -> groups.firstOrNull { it.selected }?.id }.distinctUntilChanged()
    val hubNotice: StateFlow<HubNotice?> = combine(
        _hubUpdate,
        _hubHello,
        connection.state,
        _hubNoticeDismissed,
        selectedGroup,
    ) { update, hello, connState, dismissed, _ ->
        // The selected hub's hello as its connection kept it: the events carry every paired hub's, the last one is not
        // necessarily the hub this page shows (#2157). Before any hello there is nothing to say (HubCompat.of).
        // A plain getter: switching between two online groups changes no state, so the selection is read again here.
        val compat = HubCompat.of(connection.lastHello ?: hello, appBuild, connected = connState == ConnectionState.Online)
        HubNotice.of(update, compat, dismissed)
    }.stateIn(viewModelScope, SharingStarted.WhileSubscribed(5_000), null)

    fun dismissHubNotice(notice: HubNotice) {
        val key = notice.dismissKey ?: return
        _hubNoticeDismissed.update { (it + key).toList().takeLast(8).toSet() }
    }

    fun setAutoUpdate(on: Boolean) {
        viewModelScope.launch {
            try {
                val updated = hubUpdates?.settings(auto = on)
                if (updated != null) _hubUpdate.value = updated
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (e: Exception) {
                _notice.value = AgentText(R.string.agents_notice_save_failed, persona.core.designsystem.failureReasonText(e))
            }
        }
    }

    fun noticeSeen() {
        viewModelScope.launch {
            try {
                val updated = hubUpdates?.settings(noticeSeen = true)
                if (updated != null) _hubUpdate.value = updated
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (e: Exception) {
                _notice.value = AgentText(R.string.agents_notice_save_failed, persona.core.designsystem.failureReasonText(e))
            }
        }
    }

    fun startHubUpdate() {
        viewModelScope.launch {
            try {
                val updated = hubUpdates?.start(now = false)
                if (updated != null) _hubUpdate.value = updated
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (e: Exception) {
                _notice.value = AgentText(R.string.agents_notice_update_failed, persona.core.designsystem.failureReasonText(e))
            }
        }
    }

    private val todosFlow = tasks?.todos ?: MutableStateFlow(emptyList())
    private val todosSupportedFlow = tasks?.todosSupported ?: MutableStateFlow(false)
    // The hub's direction tree (#4352), for "By pinned": the selected assistant's snapshot holds the one copy the app
    // reads (RealAssistantRepository), already in the list's ids.
    private val directionsFlow: Flow<DirectionTree?> =
        bindings?.assistant?.assistant?.map { it.directionTree }?.distinctUntilChanged() ?: flowOf(null)
    private val listPreferences = channelFlow {
        launch { preferences.watchDisplaySettings(workspaceRefresh) }
        preferences.snapshots(pairingScope).collect { send(it) }
    }.shareIn(viewModelScope, SharingStarted.WhileSubscribed(replayExpirationMillis = 0), replay = 1)
    private val projectedPage = combine(renamer?.project(repository.sessions) ?: repository.sessions, repository.machines,
        listPreferences.combine(repository.listStatus) { prefs, status -> prefs to status },
        combine(settings, groupFilter, assistant, visibleChat, heldChat.combine(finishedChat, ::HeldChats)) { settings, group, assistant, visible, held -> HeldInputs(settings, group, assistant, visible, held) }
            .combine(workspaceCatalog.rows) { inputs, workspaces -> ListInputs(inputs.settings, inputs.group, inputs.assistant, inputs.visible, inputs.held, workspaces) }
            .combine(seenVisit, ::Pair),
        combine(operationState, todosFlow.combine(directionsFlow, ::Pair), todosSupportedFlow, ::Triple)) { hubSessions, hubMachines, (localPrefs, pagination), (inputs, visit), (ops, todosAndTree, todosSupported) ->
        val (settings, group, assistant, visible, held, workspaces) = inputs
        val (todos, directions) = todosAndTree
        val now = Instant.now()
        // The current assistant's (the hub's lists are every manager's, each row with its own).
        val (hiddenSessions, visibleSessions) = if (localPrefs.hiddenBotmuxHubs.isEmpty()) emptyList<Session>() to hubSessions
            else hubSessions.partition(localPrefs::hides)
        val everySession = if (assistant == null) visibleSessions else visibleSessions.filter { assistant.ownsOn(it.machineId, it.manager) }
        val everyMachine = if (assistant == null) hubMachines else hubMachines.filter { assistant.ownsOn(it.id, it.manager) }
        val source = if (group == null) everySession else everySession.filter { group.admits(it.machineId) }
        val machines = if (group == null) everyMachine else everyMachine.filter { group.admits(it.id) }
        val sessions = ops.project(source)
        val grouping = resolvedGrouping(settings.pendingGrouping ?: localPrefs.grouping, todosSupported)
        val visitScope = SeenVisitScope(group, assistant?.group, assistant?.id, settings.machineFilter, settings.quick, localPrefs.range)
        val keepSeen = localPrefs.seenRows == AgentSeenRows.Stay
        val currentVisit = visit.takeIf { keepSeen && it.scope == visitScope }
        val cleared = currentVisit?.clearedVersions.orEmpty()
        val prefs = localPrefs.copy(pins = ops.pins?.domain() ?: localPrefs.pins, mutes = ops.mutes?.domain() ?: localPrefs.mutes,
            grouping = grouping, foldedTabs = settings.pendingFoldedTabs ?: localPrefs.foldedTabs,
            seen = if (cleared.isEmpty()) localPrefs.seen else localPrefs.seen + cleared)
        // The shell records these before its read receipt, so a conflated Idle snapshot cannot erase an Unread entry, nor
        // the hub's list without the flag a Just finished one (#9786): while its chat is open the row keeps that place.
        val heldSession = held.unread?.takeIf { !keepSeen && it == visible }?.path
        val finishedSession = held.finished?.takeIf { !keepSeen && it == visible }?.path
        val rows = sessions.map { it.listSession(prefs).copy(hierarchyKnown = repository.supportsFeature(it.machineId, "sessions_hierarchy"))
            .let { row -> if (finishedSession != null && row.id == finishedSession) row.heldAsFinished() else row } }
        // A partial machine snapshot must not hide sessions in machine grouping.
        val knownMachines = machines + sessions.map { it.machineId }.distinct().filter { id -> machines.none { it.id == id } }
            .map { Machine(it) } // named by the list as not updated yet (AgentsScreen.kt)
        val admittedMachines = machines.mapTo(mutableSetOf()) { it.id }
        val rawGrouping = settings.pendingGrouping ?: localPrefs.grouping
        val initialFolded = if (foldsAuthority == rawGrouping || settings.query.isNotEmpty())
            settings.folded else localPrefs.folds[prefs.grouping].orEmpty()
        val requested = settings.copy(grouping = prefs.grouping, sort = prefs.sort, folded = initialFolded,
            workspaces = workspaces.filter { it.machineId in admittedMachines })
        // A paged search already matched the words on the hub, including matches outside the truncated row summary.
        val projection = if (pagination.paged) requested.copy(query = AgentQuery.parse(requested.query).conditions) else requested
        val all = scopedAgentList(rows, knownMachines, prefs, projection.copy(quick = AgentQuickFilter.All, kept = emptySet()),
            todos, directions = directions, now = now)
        val hidden = group?.let { prefs.hiddenBotmuxHubs.intersect(setOf(it.namespace)) } ?: prefs.hiddenBotmuxHubs
        val larkCount = if (pagination.paged && pagination.filter.hiddenBotmuxHubs == hidden)
            pagination.counts.larkGroups(all.counts)
        else all.counts[AgentQuickFilter.Feishu] ?: 0
        val effective = if (requested.quick == AgentQuickFilter.Feishu && larkCount == 0) {
            if (pagination.loaded && !pagination.loading) this.settings.compareAndSet(settings, settings.copy(quick = AgentQuickFilter.All, kept = emptySet()))
            requested.copy(quick = AgentQuickFilter.All, kept = emptySet())
        } else requested
        val context = listOf(prefs.range, effective.grouping, effective.sort, hidden, effective.quick, effective.query, effective.machineFilter,
            prefs.pins, todos, directions, effective.workspaces)
        if (context != previousContext) previous = null
        previousContext = context
        val effectiveScope = visitScope.copy(quick = effective.quick)
        val candidates = currentVisit?.candidates?.takeIf { effectiveScope == visitScope }.orEmpty().toMutableMap()
        if (candidates.isNotEmpty()) for (row in rows) {
            val candidate = candidates[row.id] ?: continue
            if ((!row.isQuiet && !row.unseen) || row.session.readVersion != candidate.version) candidates.remove(row.id)
        }
        val visitPlaces = candidates.mapValues { it.value.place }
        val projectionSettings = projection.copy(quick = effective.quick, kept = effective.kept, heldSeen = visitPlaces)
        val sorted = scopedAgentList(rows, knownMachines, prefs, projectionSettings, todos, openedRowsOfTheirOwn(previous, effective.kept), directions,
            listAdapter, held = heldSession, now = now)
        val result = retainOpenedRows(sorted, previous, effective.kept).also { previous = it }
        if (keepSeen) {
            for (row in result.groups.asSequence().flatMap { it.visibleRows.asSequence() }
                .filterNot { it.isNestedSubtask || cleared[it.id] == it.session.readVersion }
            ) {
                val place = when {
                    row.state == AgentState.Unread -> AgentSeenPlace.Unread
                    row.showsJustFinished && !prefs.mutes.mutes(row) -> AgentSeenPlace.JustFinished
                    else -> continue
                }
                candidates[row.id] = SeenVisitRow(place, row.session.readVersion)
            }
            val pending = cleared.takeIf { effectiveScope == visitScope }.orEmpty()
            val next = if (candidates.isEmpty() && pending.isEmpty()) SeenVisit(revision = visit.revision)
                else visit.copy(scope = effectiveScope, candidates = candidates, clearedVersions = pending)
            seenVisit.compareAndSet(visit, next)
        } else if (visit.scope != null) {
            seenVisit.compareAndSet(visit, SeenVisit(revision = visit.revision + 1))
        }
        // The chips count what each computer would show under the current range, search and quick filter.
        val everywhere = scopedAgentList(rows, knownMachines, prefs, projectionSettings.copy(machineFilter = "", kept = emptySet()), todos,
            directions = directions, now = now)
            .groups.flatMap { it.rows }.filterNot { it.isNestedSubtask }
        AgentsPageState(sessions, knownMachines, prefs.copy(hiddenBotmuxHubs = hidden), effective, result, all.groups.sumOf { group -> group.rows.count { !it.isNestedSubtask } },
            machineCounts(knownMachines, everywhere), everywhere.size, groupCounts(everySession, everyMachine, prefs, effective, ops),
            // Loaded as of the hub status this page was built from: [page] joins the latest status to a page that may be
            // one throttled build behind, and a page built before the load must not say the list is in.
            loaded = pagination.loaded).let { state ->
            // Computer projects use the same held marker as the main projection; ordinary space members stay as before.
            val spaces = agentSpaces(rows.map {
                it.copy(heldInList = it.id == heldSession).placingSeenRow(visitPlaces[it.id], prefs.mutes)
            })
            state.copy(spaces = spaces, spacesById = spacesByGroup(result, prefs.grouping, spaces), directionsAvailable = directions != null,
                tapEpoch = maxOf(effective.tapEpoch, ops.tapEpoch), hiddenSessions = hiddenSessions.mapTo(mutableSetOf()) { it.key })
        }
    // The app (listContext set) builds only from the latest inputs and redraws at most every PAGE_MILLIS: with an
    // accessibility service on, Compose diffs the whole semantics tree on each recomposition, and one per pushed
    // session event kept the long list janky. Tests (no listContext) stay synchronous. A tap wakes the hold so the
    // page that contains the tap is not stuck behind one built before it.
    }.let { built -> if (listContext == kotlin.coroutines.EmptyCoroutineContext) built.flowOn(listContext)
        else built.conflate().flowOn(listContext).throttleLatest(PAGE_MILLIS, tapWake) }
    val page = combine(projectedPage, repository.listStatus, flow {
        // Apply filters only while the page is observed; hidden/destroyed screens retain no filter worker.
        // A hub hello can cancel the first request and start a replacement. Keep displaying the repository's
        // updates even if that first filter application never completes.
        emit(Unit)
        remoteFilter.collectLatest { (filter, group) ->
            try {
                repository.setListNamespaces(group?.let { setOf(it.namespace) })
                repository.setListFilter(filter)
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { _failure.value = error }
        }
    }, groupListCounts) { projected, status, _, metadata ->
        if (!status.paged || status.filter.hiddenBotmuxHubs != projected.preferences.hiddenBotmuxHubs) projected.copy(loaded = projected.loaded && status.loaded, pagination = status)
        else {
            val counts = status.counts
            val machineCounts = status.machineCounts
            // A parent marked seen locally stays counted Unread while a side is unread; once held sides are seen, drop immediately (#13524).
            val clearedUnread = unreadClearedBySeen(projected.sessions, projected.sessions.map { it.listSession(projected.preferences).session })
            val quickCounts = AgentQuickFilter.entries.associateWith { filter ->
                when (filter) {
                    // The hub counts them among working and apart too (#6045): Working leaves them out, as iOS.
                    AgentQuickFilter.Feishu -> counts.larkGroups(projected.list.counts)
                    AgentQuickFilter.BackgroundWaiting -> counts.backgroundWaiting
                    AgentQuickFilter.Working -> ((counts.state[AgentState.Working.wire] ?: 0) - counts.backgroundWaiting).coerceAtLeast(0)
                    AgentQuickFilter.Unread -> ((counts.state[AgentState.Unread.wire] ?: 0) - clearedUnread).coerceAtLeast(0)
                    else -> filter.state?.let { counts.state[it.wire] ?: 0 }
                        ?: if (filter == AgentQuickFilter.All) counts.total else projected.list.counts[filter] ?: 0
                }
            }
            fun count(id: String): SessionListCounts? = metadata[id]?.takeIf {
                it.revision == status.counts.revision && it.filter.query == hubQuery(projected.settings.query) &&
                    it.filter.state == projected.settings.quick.state?.wire && it.filter.hiddenBotmuxHubs == projected.preferences.hiddenBotmuxHubs
            }?.counts
            fun SessionListCounts.states() = AgentState.entries.associateWith { state -> this.state[state.wire] ?: 0 }
            val countedGroups = projected.list.groups.map { group ->
                count("group:${group.id}")?.let { group.copy(totalCount = it.total, stateCounts = it.states(), waitingInBackground = it.backgroundWaiting) } ?: group
            }
            val countedMachines = projected.list.machines.map { machine ->
                count("machine:${machine.machine.id}")?.let { machine.copy(sessionCount = it.total, stateCounts = it.states(),
                    repoCount = it.project.size, spaceCount = it.group.size, waitingInBackground = it.backgroundWaiting) } ?: machine
            }
            projected.copy(
                list = projected.list.copy(counts = quickCounts, groups = countedGroups, machines = countedMachines), allCount = counts.total,
                machineCounts = projected.machines.sortedBy { !it.online }.map { MachineCount(it, machineCounts.machine[it.id] ?: 0) },
                machineTotal = machineCounts.total,
                groupCounts = machineCounts.machine.entries.groupBy { GroupKeys.namespaceOf(it.key) }
                    .mapValues { (_, rows) -> rows.sumOf { it.value } },
                loaded = projected.loaded && status.loaded, pagination = status,
            )
        }
    }.stateIn(viewModelScope, SharingStarted.WhileSubscribed(0), AgentsPageState())

    /** Network filters reset the frozen cursor; the repository discards responses from an older selection. */
    private val remoteFilter = combine(settings, groupFilter, listPreferences.map { it.hiddenBotmuxHubs to it.seenRows }.distinctUntilChanged()) { settings, group, (hidden, seenRows) ->
        // The Unread view can keep a row after the hub counts it as seen. Its complete membership includes those rows.
        val state = settings.quick.state?.wire.takeUnless { settings.quick == AgentQuickFilter.Unread && seenRows == AgentSeenRows.Stay }
        SessionListFilter(state = state, machineId = settings.machineFilter.takeIf { it.isNotEmpty() },
            // Range, assistant exclusion, host and preference membership are local until #6108 is implemented.
            // A generic server total is not the number of rows this page admits.
            query = hubQuery(settings.query), requiresCompleteMembership = true,
            hiddenBotmuxHubs = group?.let { hidden.intersect(setOf(it.namespace)) } ?: hidden) to group
    }.distinctUntilChanged()

    fun countMachine(machine: AgentMachineGroup) = requestGroupCounts("machine:${machine.machine.id}",
        SessionListFilter(machineId = machine.machine.id))

    fun countGroup(group: AgentListGroup) {
        val page = this.page.value
        val members = group.rows + page.list.groups.filter { it.under == group.id }.flatMap { it.rows }
        val machine = members.map { it.session.machineId }.distinct().singleOrNull() ?: return
        val dimension = when (page.settings.grouping) {
            AgentGrouping.Repo -> {
                if (group.repo != RepoLevel.Repository) return // Worktree recipes need a cwd dimension the server lacks.
                val project = members.map { it.session.repoRoot?.takeIf(String::isNotEmpty) ?: it.session.cwd }
                    .distinct().singleOrNull() ?: return
                SessionListFilter(machineId = machine, project = project)
            }
            AgentGrouping.MachineSpace -> {
                val id = members.map { it.session.spaceId?.takeIf(String::isNotEmpty) ?: it.session.workspaceId.orEmpty() }
                    .distinct().singleOrNull() ?: return
                SessionListFilter(machineId = machine, group = id)
            }
            else -> return // Pin/mute ranks and custom to-do sections are not server groups.
        }
        requestGroupCounts("group:${group.id}", dimension)
    }

    private fun requestGroupCounts(id: String, dimension: SessionListFilter) {
        val page = this.page.value
        val revision = page.pagination.counts.revision
        if (!page.pagination.paged || revision.isEmpty()) return
        val filter = dimension.copy(state = page.settings.quick.state?.wire, query = hubQuery(page.settings.query),
            hiddenBotmuxHubs = page.preferences.hiddenBotmuxHubs)
        val key = Triple(id, revision, filter)
        if (groupListCounts.value[id]?.let { it.revision == revision && it.filter == filter } == true || !countingGroups.add(key)) return
        viewModelScope.launch {
            try {
                val counts = repository.listCounts(filter)
                val current = this@AgentsViewModel.page.value
                if (current.pagination.counts.revision == revision && hubQuery(current.settings.query) == filter.query &&
                    current.settings.quick.state?.wire == filter.state && current.preferences.hiddenBotmuxHubs == filter.hiddenBotmuxHubs) groupListCounts.update { metadata ->
                    (metadata + (id to GroupListCounts(filter, revision, counts))).entries.toList().takeLast(512)
                        .associate { it.key to it.value }
                }
            } catch (cancelled: CancellationException) { throw cancelled }
            catch (_: SessionListsUnsupported) { /* Existing local group recipe; no unbounded fallback. */ }
            catch (error: Exception) { _failure.value = error }
            finally { countingGroups.remove(key) }
        }
    }

    fun loadMore() {
        if (!repository.listStatus.value.hasMore || repository.listStatus.value.loading) return
        viewModelScope.launch {
            try { repository.loadMore() }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { _failure.value = error }
        }
    }

    fun loadPrevious() {
        if (!repository.listStatus.value.hasPrevious || repository.listStatus.value.loading) return
        viewModelScope.launch {
            try { repository.loadPrevious() }
            catch (cancelled: CancellationException) { throw cancelled }
            catch (error: Exception) { _failure.value = error }
        }
    }

    init {
        if (hubUpdates != null) {
            viewModelScope.launch {
                // Read again for each selected group: the other group's status must not stay up meanwhile.
                selectedGroup.collectLatest {
                    _hubUpdate.value = null
                    try {
                        _hubUpdate.value = hubUpdates.status()
                    } catch (cancelled: CancellationException) {
                        throw cancelled
                    } catch (_: HubUpdatesUnsupported) {
                        // Legacy hub without update API
                    } catch (_: Exception) {
                        // Ignored on initial poll
                    }
                }
            }
            viewModelScope.launch {
                hubUpdates.events.collect { update ->
                    _hubUpdate.value = update
                }
            }
        }
        viewModelScope.launch {
            connection.events.collect { event ->
                if (event is Hello) {
                    _hubHello.value = event
                }
            }
        }
        viewModelScope.launch {
            var previous = emptyList<Interaction>()
            interactions.pending.collect { pending ->
                val removed = previous.filter { old -> !old.isDisplayMirror && old.secret != true && pending.none { it.id == old.id && it.sessionKey == old.sessionKey } }
                _requestHistory.update { history ->
                    (removed.asReversed() + history).distinctBy { it.sessionKey to it.id }
                        .filter { old -> pending.none { it.id == old.id && it.sessionKey == old.sessionKey } }.take(50)
                }
                previous = pending
                _delivery.update { RequestDeliveries.prune(it, pending) }
                val pendingIds = pending.mapTo(mutableSetOf()) { it.id }
                _commandInputErrors.update { errors -> errors.filterKeys { it in pendingIds } }
            }
        }
        if (operations != null) {
            localChange { operations.refresh() }
            viewModelScope.launch {
                repository.sessions.collect { sessions ->
                    val status = repository.listStatus.value
                    operations.reconcile(sessions, complete = status.loaded && !status.paged).forEach { bindings?.onSessionClosed?.invoke(it) }
                    messageIds.keys.removeAll { operation ->
                        operation.startsWith("loop:") && sessions.none { "loop:${it.key.path}" == operation && it.loop != null }
                    }
                }
            }
        }
    }

    fun filter(filter: AgentQuickFilter) {
        resetSeenVisit()
        previous = null
        settings.update { it.copy(quick = filter, kept = emptySet()) }
    }
    fun sort(sort: AgentSort) = localChange { previous = null; preferences.sort(sort) }
    fun range(range: AgentRange) = localChange {
        resetSeenVisit()
        previous = null
        settings.update { it.copy(quick = AgentQuickFilter.All, kept = emptySet()) }
        preferences.range(pairingScope, range)
    }
    /**
     * A search opens every fold while it runs (iOS): the list's folds wait in [stashedFolds] and come back as they were when
     * the query clears; what is folded during the search is forgotten with it (#4338 A6).
     */
    fun query(query: String) {
        previous = null
        val was = settings.value.query
        if (was.isEmpty() && query.isNotEmpty()) stashedFolds = settings.value.folded
        settings.update {
            when {
                was.isEmpty() && query.isNotEmpty() -> it.copy(query = query, kept = emptySet(), folded = emptySet())
                was.isNotEmpty() && query.isEmpty() -> it.copy(query = query, kept = emptySet(), folded = stashedFolds ?: emptySet())
                else -> it.copy(query = query, kept = emptySet())
            }
        }
        if (query.isEmpty()) stashedFolds = null
    }
    /** What the list would show for each group (the group chips), like the computer chips count. */
    private fun groupCounts(all: List<Session>, machines: List<Machine>, prefs: AgentsPreferencesSnapshot,
        settings: AgentListSettings, ops: AgentOperationsSnapshot): Map<String?, Int> {
        // One group (no suffixed ids anywhere): no group chips, nothing to count.
        if (machines.none { GroupKeys.namespaceOf(it.id) != null } && all.none { GroupKeys.namespaceOf(it.machineId) != null }) return emptyMap()
        val rows = ops.project(all).map { session ->
            session.listSession(prefs).copy(hierarchyKnown = repository.supportsFeature(session.machineId, "sessions_hierarchy"))
        }
        return scopedAgentList(rows, machines, prefs, settings.copy(machineFilter = "", kept = emptySet()))
            .groups.flatMap { it.rows }.filterNot { it.isNestedSubtask }.distinctBy { it.id }
            .groupingBy { GroupKeys.namespaceOf(it.session.machineId) }.eachCount()
    }

    fun machine(id: String, animate: Boolean = false) {
        resetSeenVisit()
        previous = null
        // The chip clears with the list (ChatsView.swift:1646). The menu's picker does not (AgentsMenus.swift:141).
        if (animate) writeTap(ListTapMotionKind.State) { it.copy(machineFilter = id, kept = emptySet()) }
        else settings.update { it.copy(machineFilter = id, kept = emptySet()) }
    }
    /** Shows one hub group (or every group); a computer chip of another group is let go. */
    fun group(value: GroupFilter?) {
        resetSeenVisit()
        previous = null
        groupFilter.value = value
        settings.update { if (value.admits(it.machineFilter) || it.machineFilter.isEmpty()) it else it.copy(machineFilter = "", kept = emptySet()) }
    }
    fun grouping(group: AgentGrouping) {
        // Theme.Motion.layout, AgentsMenus.swift:405. The value on screen may still be the previous
        // page: a tap's pending grouping is the one already chosen (the page waits on the throttle).
        val shown = settings.value.pendingGrouping ?: page.value.preferences.grouping
        if (shown == group) {
            localChange {
                previous = null
                settings.update { it.copy(kept = emptySet()) }
                preferences.grouping(group)
            }
            return
        }
        // The new grouping and the epoch are one emission. Saving comes after, so a slow write cannot
        // publish the grouping alone (that page would be held, the rows would land, and the real epoch
        // would capture no shift). A failed save is put back only if this tap is still the pending one
        // (PinsStore.swift:131, :139, :163). A later tap, or a grouping saved by someone else, stays.
        val epoch = stamp(ListTapMotionKind.Layout)
        previous = null
        val previousAuthority = foldsAuthority
        val previousFolded = settings.value.folded
        val targetFolds = page.value.preferences.folds[group].orEmpty()
        foldsAuthority = group
        settings.update { current ->
            if (current.query.isEmpty()) current.copy(kept = emptySet(), tapEpoch = epoch, pendingGrouping = group, folded = targetFolds)
            else { stashedFolds = targetFolds; current.copy(kept = emptySet(), tapEpoch = epoch, pendingGrouping = group) }
        }
        localChange {
            try {
                preferences.grouping(group)
                settings.update { current -> if (current.pendingGrouping == group) current.copy(pendingGrouping = null) else current }
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) {
                if (settings.value.pendingGrouping == group) {
                    val back = stamp(ListTapMotionKind.Layout)
                    foldsAuthority = previousAuthority
                    settings.update { current ->
                        if (current.pendingGrouping != group) current
                        else if (current.query.isEmpty()) current.copy(kept = emptySet(), tapEpoch = back, pendingGrouping = null, folded = previousFolded)
                        else { stashedFolds = previousFolded; current.copy(kept = emptySet(), tapEpoch = back, pendingGrouping = null) }
                    }
                }
                throw e
            }
        }
    }
    fun compact() = localChange { preferences.compact(!page.value.preferences.compact) }
    /** Glass's ⋯ › Display: a row style or Compact List (iOS `AgentListDisplay`). */
    fun display(value: AgentListDisplay) = localChange { preferences.setDisplay(value) }
    fun foldTab(tab: AgentTab) {
        // A tab folds through toggleFold (ChatsView.swift:2080), animated at :1168.
        // The folded set and the epoch are written together, before the phone saves them. A page
        // built from the save alone would carry the fold and the old epoch. The base is the pending
        // set when the throttled page has not caught up, so a second fold in that gap keeps the first.
        val base = settings.value.pendingFoldedTabs ?: page.value.preferences.foldedTabs
        val next = if (tab.foldKey in base) base - tab.foldKey else base + tab.foldKey
        val epoch = stamp(ListTapMotionKind.State)
        previous = null
        settings.update { it.copy(tapEpoch = epoch, pendingFoldedTabs = next) }
        localChange {
            try {
                preferences.foldTab(tab.foldKey, tab.foldKey in next)
                settings.update { current -> if (current.pendingFoldedTabs == next) current.copy(pendingFoldedTabs = null) else current }
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) {
                // Only this tap's set is dropped. The saved prefs show again, and the revert is its own tap.
                if (settings.value.pendingFoldedTabs == next) {
                    val back = stamp(ListTapMotionKind.State)
                    settings.update { current ->
                        if (current.pendingFoldedTabs != next) current
                        else current.copy(tapEpoch = back, pendingFoldedTabs = null)
                    }
                }
                throw e
            }
        }
    }
    fun pin(row: AgentSession) = change(row.session.machineId) { it.preference(pin = true, space = false, key = row.id, onWrite = ::stampState) }
    fun mute(row: AgentSession) = change(row.session.machineId) { it.preference(pin = false, space = false, key = row.id, spaceKey = row.spacePreferenceKey, onWrite = ::stampState) }
    fun spacePin(session: Session) = change(session.machineId) { it.preference(true, true, "${session.machineId}/${session.spaceName}", onWrite = ::stampState) }
    fun spaceMute(session: Session) = change(session.machineId) { it.preference(false, true, "${session.machineId}/${session.spaceName}", onWrite = ::stampState) }
    fun rename(session: Session, title: String?, tab: String?) {
        val edit = SessionRename.changes(session, title, tab)
        if (!edit.changed) return
        change(session.machineId, R.string.agents_notice_renamed) {
            if (renamer != null) renamer.rename(session, edit.title, edit.tabName)
            else it.session(session, AgentSessionEdit(title = edit.title, tab = edit.tabName))
        }
    }
    fun renameUnified(session: Session, name: String) {
        val edit = SessionRename.unifiedChanges(session, name)
        if (!edit.changed) return
        change(session.machineId, R.string.agents_notice_renamed) {
            if (renamer != null) renamer.renameUnified(session, name)
            else it.session(session, AgentSessionEdit(title = edit.title, tab = edit.tabName), unified = true)
        }
    }
    fun renameSpace(session: Session, name: String) {
        val trimmed = name.trim()
        if (trimmed.isEmpty() || trimmed == session.spaceName) return
        change(session.machineId, R.string.agents_notice_renamed) { it.renameSpace(session, trimmed) }
    }
    /** [onDone]: after it went through (the worktree cleanup, #4532). */
    fun close(session: Session, onDone: () -> Unit = {}) = change(session.machineId, onDone = onDone) {
        it.session(session, AgentSessionEdit(closed = true))
        bindings?.onSessionClosed?.invoke(session.key)
    }
    /** "Delete chat": out of every app's list (the transcript stays); a chat open on it goes. */
    fun delete(session: Session, onDone: () -> Unit = {}) = change(session.machineId, R.string.agents_notice_deleted, onDone) {
        it.session(session, AgentSessionEdit(deleted = true))
        bindings?.onSessionClosed?.invoke(session.key)
    }
    /** Still not running (not started again elsewhere since the confirmation opened). */
    fun deletable(session: Session): Boolean = page.value.sessions.firstOrNull { it.key == session.key }?.canDelete
        ?: page.value.sessions.isEmpty()
    /** Still running and closable from here, in a terminal pane or a runtime's (not closed elsewhere since a confirmation opened). */
    fun closable(session: Session): Boolean = page.value.sessions.firstOrNull { it.key == session.key }
        ?.let { it.isLive && it.canClose } ?: page.value.sessions.isEmpty()
    /** "Branch to new chat": copies the session into a new one, refreshes the list and opens the branched session (#12996). */
    fun fork(session: Session, onOpened: (SessionKey) -> Unit) = sendOperation("fork:${session.key.path}") {
        val ops = operations ?: return@sendOperation
        if (hubGroups.groups.value.stateFor(session.machineId, connection.state.value) != ConnectionState.Online) {
            _notice.value = AgentText(persona.feature.chat.R.string.chat_menu_fork_failed,
                persona.core.designsystem.UiText.of(persona.core.designsystem.R.string.designsystem_reason_unreachable))
            _haptics.tryEmit(Haptic.Attention)
            return@sendOperation
        }
        try {
            val newId = ops.fork(session)
            try { repository.refresh() } catch (cancelled: CancellationException) { throw cancelled } catch (_: Exception) {}
            _notice.value = AgentText(persona.feature.chat.R.string.chat_menu_forked)
            _haptics.tryEmit(Haptic.Success)
            onOpened(SessionKey(session.machineId, newId))
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (e: Exception) {
            val cause = (e as? AgentOperationFailure)?.cause ?: e
            _notice.value = AgentText(persona.feature.chat.R.string.chat_menu_fork_failed,
                persona.core.designsystem.operationFeedback(cause).text)
        }
    }
    /** "Join live": join a Codex app-server thread live; the list refreshes after it. */
    fun attach(session: Session) = change(session.machineId, R.string.agents_notice_attached) { it.attach(session) }
    /** Stop its turn from the list (iOS's row menu Interrupt): felt once it went through. */
    fun interrupt(session: Session) = change(session.machineId, R.string.agents_notice_interrupted,
        onDone = { _haptics.tryEmit(Haptic.Tap) }) { it.interrupt(session) }
    fun stopLoop(session: Session) = sendOperation("loop:${session.key.path}") {
        if (hubGroups.groups.value.stateFor(session.machineId, connection.state.value) != ConnectionState.Online) { _notice.value = AgentText(R.string.agents_notice_stop_loop_offline); return@sendOperation }
        sendOnce("loop:${session.key.path}", session.key, STOP_LOOP_PROMPT)
        _notice.value = AgentText(R.string.agents_notice_stop_loop_sent)
    }
    /** [prompt] is [handoffPrompt] in the language on screen. */
    fun handoff(session: Session, prompt: String, onQueued: () -> Unit, onBound: (SessionKey) -> Unit) = sendOperation("handoff:${session.key.path}") {
        val main = mainConversationSender ?: run { _notice.value = AgentText(R.string.agents_notice_operations_missing); return@sendOperation }
        val operation = "handoff:${session.key.path}"
        val id = messageIds.getOrPut(operation) { UUID.randomUUID().toString() }
        handoffToMain(main, id, prompt, onQueued, onBound)
        messageIds.remove(operation)
    }
    private suspend fun sendOnce(operation: String, target: SessionKey, text: String) {
        val previous = messageIds[operation]?.let { id -> sender.outbox.value.firstOrNull { it.clientMessageId == id } }
        if (previous != null && previous.state !in setOf(DeliveryState.Rejected, DeliveryState.Refused)) {
            _notice.value = AgentText(R.string.agents_notice_already_sent)
            throw AlreadySubmitted()
        }
        val id = UUID.randomUUID().toString()
        messageIds[operation] = id
        sender.send(target, text, id)
        if (sender.outbox.value.any { it.clientMessageId == id && it.state in setOf(DeliveryState.Rejected, DeliveryState.Refused) }) {
            _notice.value = AgentText(R.string.agents_notice_not_sent)
            throw AlreadySubmitted()
        }
    }
    private class AlreadySubmitted : Exception()
    private fun sendOperation(key: String, action: suspend () -> Unit) {
        if (!sending.add(key)) return
        viewModelScope.launch {
            try { action() }
            catch (_: AlreadySubmitted) { }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { _failure.value = e }
            finally { sending.remove(key) }
        }
    }
    /** [machineId] is the target's: it goes through its own hub group's connection, as answers do (stateFor). */
    private fun change(machineId: String, @androidx.annotation.StringRes success: Int = R.string.agents_notice_saved, onDone: () -> Unit = {},
                       action: suspend (AgentOperationsState) -> Unit) {
        val operations = operations ?: run { _notice.value = AgentText(R.string.agents_notice_operations_missing); return }
        if (hubGroups.groups.value.stateFor(machineId, connection.state.value) != ConnectionState.Online) { _notice.value = AgentText(R.string.agents_notice_offline); return }
        if (changing) { _notice.value = AgentText(R.string.agents_notice_saving); return }
        changing = true
        viewModelScope.launch {
            var succeeded = false
            try {
                action(operations)
                succeeded = true
                _notice.value = AgentText(success)
                onDone()
            } catch (e: CancellationException) { throw e }
            catch (_: OperationsMissing) { _notice.value = AgentText(R.string.agents_notice_operations_missing) }
            catch (_: RenameUnconfirmed) { _notice.value = AgentText(R.string.agents_notice_rename_unconfirmed) }
            catch (e: Exception) {
                _failure.value = e
            } finally { changing = false }
            try {
                // A key this refresh adds is not in the pin's moving set, so it jumps. A row the pin
                // already moved keeps the spring. The 300 ms throttle can fold this refresh into
                // the pin's own emission; those two cannot be told apart.
                repository.refresh()
                operations.reconcile(repository.sessions.value).forEach { bindings?.onSessionClosed?.invoke(it) }
                operations.refresh()
                interactions.refresh()
            } catch (e: CancellationException) { throw e }
            catch (_: Exception) { if (succeeded) _notice.value = AgentText(R.string.agents_notice_saved_stale) }
        }
    }
    fun opened(row: AgentSession) = keepQuickRows(listOf(row))

    fun markRead(rows: List<AgentSession>, clear: Boolean = false, release: List<AgentSession> = rows) = localChange {
        if (clear && page.value.preferences.seenRows == AgentSeenRows.Stay) {
            val versions = rows.associate { it.id to it.session.readVersion }
            clearSeen(release.mapTo(mutableSetOf(), AgentSession::id), versions)
            try {
                preferences.markSeen(rows.map { it.session })
            } finally {
                seenVisit.update { current -> current.copy(clearedVersions =
                    current.clearedVersions.filterNot { (id, version) -> versions[id] == version }) }
            }
        } else {
            keepQuickRows(rows)
            preferences.markSeen(rows.map { it.session })
        }
    }

    private fun keepQuickRows(rows: List<AgentSession>) {
        val current = page.value
        if (current.settings.quick == AgentQuickFilter.All) return
        val kept = rows.filterNot { current.preferences.seenRows == AgentSeenRows.Stay && (it.unseen || it.seenPlace != null) }
            .map(AgentSession::id)
        settings.update { it.copy(kept = it.kept + kept) }
    }

    fun clearSeen(ids: Set<String>, versions: Map<String, String> = emptyMap()) {
        val shown = page.value
        val owner = assistant.value
        val scope = SeenVisitScope(groupFilter.value, owner?.group, owner?.id, shown.settings.machineFilter,
            shown.settings.quick, shown.preferences.range)
        seenVisit.update { current ->
            current.copy(scope = scope, candidates = current.candidates.takeIf { current.scope == scope }.orEmpty() - ids,
                clearedVersions = current.clearedVersions.takeIf { current.scope == scope }.orEmpty() + versions,
                revision = current.revision + 1)
        }
        settings.update { it.copy(kept = it.kept - ids) }
        previous = null
    }

    private fun resetSeenVisit() {
        val previousVisit = seenVisit.getAndUpdate { if (it.scope == null) it else SeenVisit(revision = it.revision + 1) }
        settings.update { it.copy(kept = it.kept - previousVisit.candidates.keys) }
        previous = null
    }

    fun showSeenRowsNotice() = localChange {
        if (preferences.noticeSeenRows()) _notice.value = AgentText(R.string.agents_seen_rows_notice)
    }
    /** The grouping whose stored folds [settings] holds; folds are read again only when it changes (#4338 A6). */
    private var foldsGrouping: AgentGrouping? = null
    /** The raw grouping ([AgentListSettings.pendingGrouping] or stored grouping) that [settings]'s fold set is authoritative for. */
    @Volatile private var foldsAuthority: AgentGrouping? = null
    /** The list's folds while a search opens them ([query]). */
    private var stashedFolds: Set<String>? = null

    init {
        // Folds come back after a relaunch and with each grouping, as iOS keeps them on the phone.
        viewModelScope.launch {
            preferences.snapshots(pairingScope).collect { prefs ->
                if (prefs.grouping == foldsGrouping) return@collect
                foldsGrouping = prefs.grouping
                val pending = settings.value.pendingGrouping
                if (pending != null && pending != prefs.grouping) return@collect
                val stored = prefs.folds[prefs.grouping].orEmpty()
                if (settings.value.query.isEmpty()) settings.update { it.copy(folded = stored) } else stashedFolds = stored
                foldsAuthority = prefs.grouping
            }
        }
    }

    private fun markFoldsAuthoritative() {
        foldsAuthority = settings.value.pendingGrouping ?: foldsGrouping ?: page.value.preferences.grouping
    }

    /** Keeps the current grouping's folds on this phone; a search's are not kept. */
    private fun saveFolds() {
        val grouping = settings.value.pendingGrouping ?: foldsGrouping ?: return
        if (settings.value.query.isNotEmpty()) return
        val folded = settings.value.folded
        localChange { preferences.folds(grouping, folded) }
    }

    fun fold(group: AgentListGroup) {
        markFoldsAuthoritative()
        // Section fold and a quiet space (ChatsView.swift:1168, :2595, :3114, :3214, :3237).
        writeTap(ListTapMotionKind.State) { current ->
            if (group.parent == "quiet") current.copy(
                expandedQuiet = if (group.folded) current.expandedQuiet + group.id else current.expandedQuiet - group.id,
                folded = if (group.folded) current.folded - group.id else current.folded + group.id,
            )
            // "No to-do" is folded by default under to-dos: opening it is remembered, folding it again forgets that.
            else if (group.id == TodoNoneGroup) current.copy(
                folded = current.folded - group.id + if (group.folded) emptySet() else setOf(group.id),
                expandedIdle = if (group.folded) current.expandedIdle + TodoNoneOpened else current.expandedIdle - TodoNoneOpened,
            )
            // By Status's Seen starts folded: opening it is kept ([StateSeenOpened]), folding it again forgets that.
            else if (group.id == StateSeenGroup) current.copy(
                folded = current.folded - group.id - StateSeenOpened + if (group.folded) setOf(StateSeenOpened) else emptySet(),
            )
            else current.copy(folded = if (group.folded) current.folded - group.id else current.folded + group.id)
        }
        saveFolds()
    }
    fun fold(machine: AgentMachineGroup) {
        markFoldsAuthoritative()
        // ChatsView.swift:1168, the computer's section
        writeTap(ListTapMotionKind.State) { it.copy(folded = if (machine.folded) it.folded - machine.id else it.folded + machine.id) }
        saveFolds()
    }
    fun foldAll(fold: Boolean) {
        markFoldsAuthoritative()
        // Collapse All / Expand All (AgentsMenus.swift:125, :128), Theme.Motion.standard.
        previous = null
        writeTap(ListTapMotionKind.State) { it.copy(
            // The sections, on top of what is folded: a computer heading stays as it is, as iOS's Collapse All (#4338 A11).
            folded = if (fold) it.folded + page.value.list.groups.map { group -> group.id }
                else if (page.value.settings.grouping == AgentGrouping.State) setOf(StateSeenOpened) else emptySet(),
            expandedQuiet = if (fold) emptySet() else page.value.list.groups.filter { group -> group.parent == "quiet" }.map { group -> group.id }.toSet(),
        ) }
        saveFolds()
        if (!fold) localChange { preferences.openAllTabs() }
    }
    /** A section's rows past [AgentRowCap]: all of them ([open]), or the first ones again. */
    fun showAll(group: AgentListGroup, open: Boolean) {
        // ChatsView.swift:3010, Show all
        val key = AgentRowCap.key(group.id)
        writeTap(ListTapMotionKind.State) { it.copy(expandedAll = if (open) it.expandedAll + key else it.expandedAll - key) }
    }
    fun showIdle(group: AgentListGroup) {
        // ChatsView.swift:3048, "N more idle"
        writeTap(ListTapMotionKind.State) { it.copy(expandedIdle = it.expandedIdle + group.idleKey) }
    }
    /** Folds [group]'s idle agents behind their line again (By Repository's "Hide N Idle", iOS `collapseRow`, #4338 A8). */
    fun hideIdle(group: AgentListGroup) {
        // ChatsView.swift:3074
        writeTap(ListTapMotionKind.State) { it.copy(expandedIdle = it.expandedIdle - group.idleKey) }
    }
    /** Each composed list (page and overlay) gets the tap. The page is throttled, so the flag waits for that emission. */
    fun attachListTap(motion: ListTapMotion) {
        if (listTaps.none { it === motion }) listTaps += motion
    }
    fun detachListTap(motion: ListTapMotion) { listTaps.removeAll { it === motion } }

    /** Theme.Motion.standard. Returns the generation stamped on the page this tap writes. */
    private fun stampState(): Int = stamp(ListTapMotionKind.State)
    private fun stamp(kind: ListTapMotionKind): Int {
        val epoch = tapWake.value + 1
        tapWake.value = epoch
        armTaps(kind, epoch)
        return epoch
    }
    /** The settings write and the generation are one emission, so a page from before the tap cannot match it. */
    private fun writeTap(kind: ListTapMotionKind, block: (AgentListSettings) -> AgentListSettings) {
        val epoch = stamp(kind)
        settings.update { block(it).copy(tapEpoch = epoch) }
    }
    private fun armTaps(kind: ListTapMotionKind, epoch: Int) {
        listTaps.toList().forEach { it.arm(kind, awaitPage = true, epoch = epoch) }
    }
    private val AgentListGroup.idleKey: String
        get() = if (id.startsWith("machine/")) "machine-${id.removePrefix("machine/")}" else id
    fun clearNotice() { _notice.value = null }

    fun linkTodo(todoId: String, session: Session) {
        viewModelScope.launch {
            try {
                tasks?.linkTodoAgent(todoId, session.machineId, session.sessionId)
                _notice.value = AgentText(R.string.agents_notice_todo_linked)
            } catch (e: Exception) {
                _notice.value = AgentText(R.string.agents_notice_todo_link_failed, e.message.orEmpty())
            }
        }
    }

    /**
     * The assistant a new to-do for a thing on computer [machineId] stamped [manager] is made under: the thing's own
     * (docs/assistant/managers.md "Clients"), so it is never another assistant's to-do linked to this one's agent, whichever
     * is current by the time it is made (`""`: the default's, naming none). Nothing with one assistant, without a roster,
     * or for another group's computer: the request goes as before.
     */
    internal fun todoOwner(machineId: String, manager: String?): kotlin.coroutines.CoroutineContext {
        val current = assistant.value ?: return kotlin.coroutines.EmptyCoroutineContext
        val roster = current.roster?.takeIf { it.isMulti } ?: return kotlin.coroutines.EmptyCoroutineContext
        if (persona.core.contracts.GroupKeys.namespaceOf(machineId) != current.group) return kotlin.coroutines.EmptyCoroutineContext
        val owner = roster.shownUnder(manager)
        return persona.core.contracts.api.RequestManager(if (owner == roster.defaultManagerId) "" else owner)
    }

    /** "Create and link" (iOS TodoLinkAgentSheet): a new to-do named [title] under the agent's assistant, then this agent on it. */
    fun createAndLinkTodo(title: String, session: Session) {
        viewModelScope.launch {
            val store = tasks ?: return@launch
            val owner = todoOwner(session.machineId, session.manager ?: repository.machines.value.firstOrNull { it.id == session.machineId }?.manager)
            val todo = try { withContext(owner) { store.createTodo(title.trim()) } } catch (e: Exception) {
                _notice.value = AgentText(R.string.agents_notice_todo_create_failed, e.message.orEmpty()); return@launch
            }
            try {
                store.linkTodoAgent(todo.id, session.machineId, session.sessionId)
                _notice.value = AgentText(R.string.agents_notice_todo_linked)
            } catch (e: Exception) {
                _notice.value = AgentText(R.string.agents_notice_todo_created_not_linked, e.message.orEmpty())
            }
        }
    }

    /** "Link to to-do" a whole project (#1616): its agents, now and later, are on [todoId]; null [todoId] makes one named [newTitle]. */
    fun linkSpace(todoId: String?, machineId: String, space: String, newTitle: String? = null) {
        viewModelScope.launch {
            val store = tasks ?: return@launch
            val owner = todoOwner(machineId, repository.machines.value.firstOrNull { it.id == machineId }?.manager)
            val id = todoId ?: try { withContext(owner) { store.createTodo(newTitle.orEmpty().trim()) }.id } catch (e: Exception) {
                _notice.value = AgentText(R.string.agents_notice_todo_create_failed, e.message.orEmpty()); return@launch
            }
            try {
                store.setTodoSpace(id, machineId, space, linked = true)
                _notice.value = AgentText(R.string.agents_notice_space_linked)
            } catch (e: Exception) {
                _notice.value = AgentText(if (todoId == null) R.string.agents_notice_todo_created_not_linked else R.string.agents_notice_todo_link_failed,
                    e.message.orEmpty())
            }
        }
    }

    fun unlinkSpace(todoId: String, machineId: String, space: String) {
        viewModelScope.launch {
            try {
                tasks?.setTodoSpace(todoId, machineId, space, linked = false)
                _notice.value = AgentText(R.string.agents_notice_space_unlinked)
            } catch (e: Exception) {
                _notice.value = AgentText(R.string.agents_notice_todo_unlink_failed, e.message.orEmpty())
            }
        }
    }

    fun unlinkTodo(todoId: String, session: Session) {
        viewModelScope.launch {
            try {
                tasks?.unlinkTodoAgent(todoId, session.machineId, session.sessionId)
                _notice.value = AgentText(R.string.agents_notice_todo_unlinked)
            } catch (e: Exception) {
                _notice.value = AgentText(R.string.agents_notice_todo_unlink_failed, e.message.orEmpty())
            }
        }
    }
    fun refresh() {
        if (_refreshing.value) return
        resetSeenVisit()
        workspaceRefresh.update { it + 1 }
        viewModelScope.launch {
            _refreshing.value = true
            try {
                repository.refresh(); interactions.refresh(); herdr?.refresh()
                operations?.reconcile(repository.sessions.value)?.forEach { bindings?.onSessionClosed?.invoke(it) }
                operations?.refresh()
            }
            catch (e: CancellationException) { throw e }
            catch (_: Exception) { /* Background refresh retains the cached list. */ }
            finally { _refreshing.value = false }
        }
    }
    fun answer(interaction: Interaction, action: AnswerAction, draft: AnswerDraft = AnswerDraft(), fullPlanRead: Boolean = false) {
        val latest = interactions.pending.value.firstOrNull { it.id == interaction.id } ?: return
        if (latest != interaction || latest.expired(Instant.now()) || RequestDeliveries.locks(_delivery.value[interaction.id])) return
        if (page.value.sessions.firstOrNull { it.key == latest.sessionKey }?.canAnswerInteraction(latest) == false) {
            _notice.value = AgentText(R.string.agents_notice_approve_blocked); return
        }
        if (hubGroups.groups.value.stateFor(latest.machineId, connection.state.value) != ConnectionState.Online) {
            _notice.value = AgentText(R.string.agents_notice_answer_offline); _haptics.tryEmit(Haptic.Attention); return
        }
        val result = AnswerBuilder.build(latest, action, draft, AnswerContext(fullPlanRead = fullPlanRead))
        val resolution = result.resolution ?: return
        _commandInputErrors.update { it - latest.id }
        _delivery.update { it + (interaction.id to RequestDelivery.Sending) }
        viewModelScope.launch {
            try {
                val outcome = resolution.ownerApprovalPhrase?.let { phrase ->
                    interactions.replyOwnerApproval(latest, phrase)
                    ResolveOutcome()
                } ?: interactions.resolveOccurrence(latest, resolution,
                    handoffCard = (latest.isSecretHandoff || latest.secretRun?.delivery == "env") && resolution.allow == true)
                _delivery.update { it + (interaction.id to RequestDelivery.Sent) }
                val effective = if (outcome.already) outcome.resolution else resolution
                // The notice outlives the card: it keeps the answer without a password card's typed value (#1581).
                _notice.value = if (effective == null) AgentText(persona.feature.chat.R.string.chat_interaction_already)
                else AgentText.receipt(persona.feature.chat.AnswerReceipt(latest, effective.copy(values = null)))
                _haptics.tryEmit(Haptic.Success)
            } catch (e: CancellationException) {
                _delivery.update { it + (interaction.id to RequestDelivery.Unknown) }
                throw e
            } catch (e: Exception) {
                if (latest.isCommandInputPermission) {
                    val problem = classifyInteractionFailure(e, keepUnconfirmedReason = true)
                    if (!problem.withdraw) {
                        _commandInputErrors.update { it + (latest.id to problem) }
                        if (problem.submission == InteractionSubmission.Refused) {
                            _delivery.update { it + (latest.id to RequestDelivery.Retry) }
                            problem.submission.haptic?.let(_haptics::tryEmit)
                            return@launch
                        }
                    }
                }
                // A lost acknowledgement must never cause an automatic duplicate approval.
                _delivery.update { it + (interaction.id to RequestDelivery.Unknown) }
                answerFailureHaptic(e)?.let(_haptics::tryEmit)
                checkUnconfirmed(interaction.id, latest.machineId)
            }
        }
    }
    /**
     * Checks an unconfirmed answer against the hub's pending list once it is reachable. If the request is gone it
     * ends. Otherwise the buttons work again: answering twice is safe because resolve is idempotent (#3791).
     */
    private fun checkUnconfirmed(id: String, machineId: String) {
        // The request's own group's connection, as answer() checks it.
        val reachable = combine(hubGroups.groups, connection.state) { groups, state -> groups.stateFor(machineId, state) }
        viewModelScope.launch {
            val check = checkDelivery(id, reachable) { interactions.refresh(); interactions.pending.value }
            val before = _delivery.value[id]
            _delivery.update { RequestDeliveries.checked(it, id, check) }
            if (before == RequestDelivery.Unknown && check == DeliveryCheck.Gone) _notice.value = AgentText(R.string.agents_request_checked_gone)
        }
    }
    private fun localChange(block: suspend () -> Unit) {
        viewModelScope.launch {
            try { block() }
            catch (e: CancellationException) { throw e }
            catch (e: Exception) { _failure.value = e }
        }
    }
}

/** As iOS AppModel.resolve: a changed dialog nudges, one already answered elsewhere plays nothing, the rest is an error. */
internal fun answerFailureHaptic(error: Exception): Haptic? = when (error) {
    is InteractionPromptChangedException -> Haptic.Attention
    is InteractionGoneException -> null
    else -> Haptic.Error
}

/** How often the app's Agents page redraws at most while sessions stream in. */
private const val PAGE_MILLIS = 300L

/**
 * The first value at once, then at most one per [millis]: the latest of those that came meanwhile.
 * [wake] ending the wait (a tap) sends one extra page without waiting. If a page is already queued,
 * that queued page is the extra: it was built before the tap, and the page after it is the tap.
 * A conflated channel keeps only the latest, so a queued value might already be the tap; the extra
 * is then the next hub page. A hub page after the tap waits another [millis].
 */
internal fun <T> Flow<T>.throttleLatest(millis: Long, wake: kotlinx.coroutines.flow.StateFlow<Int> = kotlinx.coroutines.flow.MutableStateFlow(0)): Flow<T> = channelFlow {
    val latest = kotlinx.coroutines.channels.Channel<T>(kotlinx.coroutines.channels.Channel.CONFLATED)
    launch { collect { latest.send(it) }; latest.close() }
    var burst = 0
    for (value in latest) {
        send(value)
        if (burst > 0) { burst--; continue }
        val stamp = wake.value
        if (kotlinx.coroutines.withTimeoutOrNull(millis) { wake.first { it != stamp } } != null) {
            val queued = latest.tryReceive().getOrNull()
            if (queued != null) send(queued)
            burst = 1
        }
    }
}
