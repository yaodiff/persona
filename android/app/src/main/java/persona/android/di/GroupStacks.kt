package persona.android.di

import persona.android.AppText
import persona.android.R
import android.app.Application
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringSetPreferencesKey
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.ProcessLifecycleOwner
import persona.android.PersonaApp
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.data.ChatDataRepositories
import persona.core.data.ScopedHubData
import persona.core.hub.*
import persona.feature.chat.ChatAppBindings
import persona.platform.phonetools.AndroidPhoneTools
import javax.inject.Inject
import javax.inject.Singleton
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.serialization.json.JsonObject

/**
 * One group's services, built on its data and connection with the group's plain ids (what ChatDataOwner used to
 * build for the only pairing). [bindings] and [agents] are already in the merged lists' ids (GroupNames).
 */
class GroupStack internal constructor(
    internal val group: GroupRuntime,
    val data: ScopedHubData,
    val connection: OkHttpHubConnection,
    val repositories: ChatDataRepositories,
    /** The chat pages' services, in merged ids. */
    val bindings: ChatAppBindings,
    /** The same services with the group's plain ids. */
    val plainBindings: ChatAppBindings,
) {
    val id: String get() = group.id
    internal val names = GroupNames(group.namespace)
    val namespace: String? get() = names.namespace
    internal val terminal by lazy { HubTerminalTransport(connection) }
    /** [terminal] in the merged lists' ids. */
    internal val mergedTerminal: TerminalTransport by lazy { terminal.inGroup(names) }
    internal val files: SessionFiles by lazy { HubSessionFiles(connection).inGroup(names) }
    internal val actions: ChatActions by lazy { HttpChatActions(connection).inGroup(names) }
    /** Successful manager snapshots follow this pairing across refreshed HTTP routes. */
    internal val metricSnapshots = HttpMachineMetricsApi.Cache()
    /** The single TasksStore shared by shell badges, the To-dos page and chat bindings for this hub group. */
    val tasks: persona.core.domain.tasks.TasksStore? get() = plainBindings.tasks
    val current: Boolean get() = group.data === data && group.active
}

/** Builds and closes a [GroupStack] for every open group (ConnectionRuntime.groups). */
@Singleton
@OptIn(ExperimentalCoroutinesApi::class)
class GroupStacks @Inject constructor(application: Application, private val runtime: ConnectionRuntime) {
    private val app = application as PersonaApp
    private val mutableStacks = MutableStateFlow<List<GroupStack>>(emptyList())
    /** Every open group's services, in the registry's order. */
    val stacks = mutableStacks.asStateFlow()
    /** The selected group's services (hub-level pages). */
    val selected: StateFlow<GroupStack?> = combine(stacks, runtime.scopedData) { list, data -> list.firstOrNull { it.data === data } }
        .stateIn(app.scope, SharingStarted.Eagerly, null)
    private val foreground = ProcessLifecycleOwner.get().lifecycle.currentStateFlow
        .map { it.isAtLeast(Lifecycle.State.STARTED) }
        .stateIn(app.scope, SharingStarted.Eagerly, false)

    init {
        runtime.beforeClose = ::awaitDropped
        app.scope.launch {
            val built = LinkedHashMap<GroupRuntime, GroupStack>()
            runtime.groups.collect { groups ->
                val gone = built.keys.filter { group -> group !in groups || built[group]?.data !== group.data }
                gone.forEach { group -> built.remove(group)?.let { stack -> closeStack(stack) } }
                for (group in groups) {
                    if (group in built) continue
                    val data = group.data ?: continue
                    if (!group.active) continue
                    val stack = try { build(group, data) } catch (cancelled: CancellationException) { throw cancelled }
                        catch (_: Exception) { null } ?: continue
                    built[group] = stack
                }
                mutableStacks.value = groups.mapNotNull { built[it] }
            }
        }
    }

    private suspend fun build(group: GroupRuntime, data: ScopedHubData): GroupStack {
        val connection = group.connection
        fun current() = group.data === data && group.active
        val retryKey = stringSetPreferencesKey("held_agent_retry_receipts")
        val receipts = HeldAgentRetryReceipts(data.heldRetryPreferences.data.map { it[retryKey].orEmpty() }) { update ->
            data.heldRetryPreferences.edit { prefs ->
                val before = prefs[retryKey].orEmpty()
                val after = update(before)
                if (after != before) prefs[retryKey] = after
            }
        }
        val repositories = withContext(Dispatchers.IO) {
            ChatDataRepositories(data, HubChatTransport(connection), connection, foreground,
                HttpHubPreferencesTransport(connection) { current() }, reconcileHeldRetries = receipts::reconcile,
                continuityFactory = app.continuityFactory)
        }
        val names = GroupNames(group.namespace)
        group.adoptSession = { previous, session -> if (current()) repositories.sessions.adoptThread(previous, session) }
        group.heldActions = { session ->
            HttpHeldAgentActions(session, connection.serverFeatures, receipts, data.coroutineScope, { path, method, body ->
                if (!current()) throw CancellationException("Pairing changed")
                connection.request(path, method, body)
            }, { id, retry -> if (current()) repositories.sessions.heldActionCompleted(session, id, retry) })
        }
        val view = object : HubConnection {
            override val state = group.state
            override val resumed get() = connection.resumed
            override val events = connection.events
            override fun reconnect() = group.reconnect()
            override fun send(message: JsonObject) = connection.send(message)
        }
        val plain = createChatWiring(data, repositories, connection, view, ::current,
            // The conversation's Undo of the assistant's calendar write (iOS `CalendarTool.remove`): this phone's own
            // delete, so it is not a tool call that would ask the user to confirm again. The current group's phone is the
            // only phone these cards can undo on: a card made on another phone has no event here and says so.
            quickResults = persona.core.contracts.api.PhoneQuickResults { id -> AndroidPhoneTools(app) { true }.removeCalendarEvent(id) })
        return GroupStack(group, data, connection, repositories, plain.inGroup(names), plain)
    }

    private fun closeStack(stack: GroupStack) {
        app.scope.launch { val _ = runCatching { stack.repositories.close() } }
        // Its forwarded ports' listeners, or the next pairing cannot bind the same numbers (#3483).
        (stack.plainBindings.ports as? AutoCloseable)?.let { ports -> runCatching { ports.close() } }
    }

    /** The open group a merged-list machine id belongs to. */
    fun of(machineId: String): GroupStack? {
        val namespace = GroupKeys.namespaceOf(machineId)
        return stacks.value.firstOrNull { it.namespace == namespace }
    }

    fun of(data: ScopedHubData): GroupStack? = stacks.value.firstOrNull { it.data === data }

    /** Runs [block] in the group's lifetime once its services are built; fails when no such group is paired. */
    suspend fun <T> use(namespace: String?, block: suspend (GroupStack) -> T): T {
        val stack = stacks.map { list ->
            if (runtime.groups.value.none { it.namespace == namespace }) throw IllegalStateException(AppText.get(R.string.app_error_pair_first))
            list.firstOrNull { it.namespace == namespace }
        }.filterNotNull().first()
        return try {
            stack.data.coroutineScope.async {
                check(stack.current) { AppText.get(R.string.app_error_switched) }
                block(stack)
            }.await()
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (failure: Exception) {
            // The group closed under the call (it merged into another, or was disconnected): like a pairing change,
            // a cancellation the page drops, not a failure it reports (or crashes on, in a LaunchedEffect).
            if (!stack.current) throw CancellationException("Pairing changed").apply { initCause(failure) }
            throw failure
        }
    }

    /** Waits (briefly) until the stacks no longer include [group], so pages stop using its data before it closes. */
    internal suspend fun awaitDropped(group: GroupRuntime) {
        withTimeoutOrNull(1_000) { stacks.first { list -> list.none { it.group === group } } }
    }

    suspend fun <T> use(key: SessionKey, block: suspend (GroupStack) -> T): T = use(GroupKeys.namespaceOf(key.machineId), block)

    /** Every group in turn: succeeds when at least one does, else throws the first failure. */
    suspend fun forEach(block: suspend (GroupStack) -> Unit) {
        val list = stacks.value
        if (list.isEmpty()) throw IllegalStateException(AppText.get(R.string.app_error_pair_first))
        var first: Exception? = null
        var any = false
        for (stack in list) try {
            use(stack.namespace, block)
            any = true
        } catch (cancelled: CancellationException) { throw cancelled }
        catch (failure: Exception) { if (first == null) first = failure }
        if (!any) throw first!!
    }

    /** A value merged over every group; with a single group it is that group's own flow, untouched. */
    fun <T> merged(initial: T, source: (GroupStack) -> Flow<T>, combineAll: (List<T>) -> T): StateFlow<T> =
        stacks.flatMapLatest { list ->
            when (list.size) {
                0 -> flowOf(initial)
                1 -> source(list.single())
                else -> list.map(source).fold(flowOf(emptyList<T>())) { acc, flow -> acc.combine(flow) { values, value -> values + value } }
                    .map(combineAll)
            }
        }.stateIn(app.scope, SharingStarted.Eagerly, initial)
}
