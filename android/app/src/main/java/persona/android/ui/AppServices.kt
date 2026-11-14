package persona.android.ui

import persona.android.AppText
import persona.android.R
import android.app.Application
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.ProcessLifecycleOwner
import androidx.room.Room
import dagger.Module
import dagger.Provides
import dagger.hilt.InstallIn
import dagger.hilt.components.SingletonComponent
import persona.android.PersonaApp
import persona.android.di.ConnectionRuntime
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.data.*
import persona.core.demo.DemoHub
import persona.core.demo.fromAssets
import persona.core.hub.*
import persona.feature.agents.AgentsDependencies
import persona.feature.agents.AgentsPreferences
import persona.platform.notify.HubService
import persona.platform.phonetools.PhoneToolsRuntime
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import java.io.File
import javax.inject.Inject
import javax.inject.Singleton

class ShellSession(
    val identity: String,
    val connection: HubConnection,
    val sessions: SessionsRepository,
    val chats: ChatRepository,
    val interactions: InteractionCenter,
    val sender: MessageSender,
    val assistant: AssistantConversations,
    val inbox: persona.feature.inbox.InboxRepository,
    val preferences: AgentsPreferences,
    val terminal: TerminalTransport? = null,
    val files: SessionFiles? = null,
    val metrics: MachineMetricsApi? = null,
    val knowledge: KnowledgeConnection? = null,
    val actions: ChatActions? = null,
    val phone: PhoneToolsRuntime? = null,
    val demo: DemoHub? = null,
    val agentsWiring: persona.feature.agents.AgentsWiring = persona.feature.agents.AgentsWiring.Empty,
    /** Launchers, "Reconnect", "Redeploy" and the engine of the hub's machines; null in demo mode. */
    val machines: MachineManagement? = null,
    /** The hub's voice extensions (voice chat); null in demo mode. */
    val voice: VoiceExtensions? = null,
    /** The phone's hub groups; a demo has none. */
    val groups: HubGroups = HubGroups.None,
    val tasks: persona.core.domain.tasks.TasksStore? = null,
    val hubUpdates: persona.core.contracts.api.HubUpdates? = null,
    /** The addresses the selected group's hubs go by (their links open in the app, ShellLinks.kt); none in demo mode. */
    val hubAddresses: () -> List<String> = { emptyList() },
    /** "Add computer" and "Confirm computer" (docs/assistant/onboarding.md 4.2); null in demo mode. */
    val computers: ComputerPairing? = null,
) {
    val agents = object : AgentsDependencies {
        override fun sessions() = this@ShellSession.sessions
        override fun interactions() = this@ShellSession.interactions
        override fun connection() = this@ShellSession.connection
        override fun sender() = this@ShellSession.sender
        override fun preferences() = this@ShellSession.preferences
        override fun wiring() = this@ShellSession.agentsWiring
        override fun groups() = this@ShellSession.groups
        override fun tasks() = this@ShellSession.tasks
        override fun hubUpdates() = this@ShellSession.hubUpdates
        override fun machines() = this@ShellSession.machines
    }
}

/** Assembles entry points around the existing pairing; owns no additional socket. */
@Singleton
class AppServices @Inject constructor(
    application: Application,
    private val runtime: ConnectionRuntime,
    private val sessions: SessionsRepository,
    private val chats: ChatRepository,
    private val interactions: InteractionCenter,
    private val sender: MessageSender,
    private val assistant: AssistantConversations,
    private val inbox: persona.feature.inbox.InboxRepository,
    private val preferences: AgentsPreferences,
    private val agentsWiring: persona.feature.agents.AgentsWiring,
    private val machineManagement: MachineManagement,
    private val stacks: persona.android.di.GroupStacks,
    private val hubGroups: HubGroups,
    private val hubUpdates: persona.core.contracts.api.HubUpdates,
    private val computers: ComputerPairing,
) {
    private val app = application as PersonaApp
    /** Terminal, files, chat actions and machine metrics of every hub group (docs/android/android.md "Multiple hubs"). */
    private val shell = persona.android.di.MergedShellServices(stacks)
    /** Terminal's pane lists of every hub group (feature terminal_panes). */
    val terminalPanes: TerminalPanes get() = shell.panes
    /** The terminal of another hub group's machine (`~group` ids, as Terminal lists them); null for this hub's own. */
    fun groupTerminal(key: SessionKey): TerminalTransport? = if (mutableSession.value?.demo != null) null else shell.groupTerminal(key.machineId)
    private val mergedMetrics = shell.metrics { stack ->
        val route = stack.connection.route.value ?: return@metrics null
        HttpMachineMetricsApi(route.endpoint.url, stack.connection.token, stack.connection.http.client,
            isCurrent = { stack.current && stack.connection.route.value == route })
    }
    private val mutableSession = MutableStateFlow<ShellSession?>(null)
    val session = mutableSession.asStateFlow()
    private val mode = app.getSharedPreferences("app-shell-mode", Application.MODE_PRIVATE)
    private var demoJob: Job? = null
    internal val feedback = FeedbackGate(connection = { mutableSession.value?.connection?.state?.value ?: ConnectionState.WaitingForNetwork },
        localize = { it.resolve(app) })
    var automation = false
        set(value) { field = value; if (value) mutableSession.value?.phone?.close() }
    /** Debug captures only: a pretend fold posture (see LaunchInput.posture). */
    val posture = androidx.compose.runtime.mutableStateOf<String?>(null)

    init {
        app.scope.launch {
            app.hub.collectLatest { hub ->
                if (hub == null) {
                    if (demoJob == null) mutableSession.value = null
                    return@collectLatest
                }
                if (demoJob != null) exitDemo()
                val data = runtime.dataStore.restore() ?: return@collectLatest
                if (app.hub.value !== hub) return@collectLatest
                val socket = hub.connection
                val knowledgeState = MutableStateFlow<KnowledgeRepository?>(null)
                val knowledgeConnection = object : KnowledgeConnection { override val current = knowledgeState.asStateFlow() }
                fun current() = app.hub.value === hub
                // Like metrics: each call follows the live route with the pairing's pinned client and token.
                val voice = object : VoiceExtensions {
                    private fun api(): VoiceExtensions {
                        if (!current()) throw CancellationException("Pairing changed")
                        val address = socket.route.value?.endpoint?.url ?: throw HubFailure(HubError.NoNetwork)
                        return HttpVoiceExtensions(address, hub.api.token, socket.http.client, ::current,
                            legacy = { "extensions" !in socket.serverFeatures.value })
                    }
                    override suspend fun voiceExtension() = api().voiceExtension()
                    override suspend fun voices(extensionId: String, model: String?) = api().voices(extensionId, model)
                    override suspend fun models(extensionId: String) = api().models(extensionId)
                    override suspend fun modelChoice(extensionId: String) = api().modelChoice(extensionId)
                    override fun connect(extensionId: String, listener: VoiceSocketListener) = api().connect(extensionId, listener)
                }
                val phoneTransport = data.credentials.deviceId?.let { HubPhoneToolsTransport(socket) }
                val phone = phoneTransport?.let {
                    PhoneToolsRuntime(app, data.scope, requireNotNull(data.credentials.deviceId), socket, it, data.coroutineScope,
                        simulator = android.os.Build.FINGERPRINT.contains("generic"), automation = automation)
                }
                socket.phoneMessageHandler = phoneTransport?.let { it::accept }
                val boundInteractions = object : InteractionCenter by interactions {
                    override suspend fun resolve(id: String, resolution: Resolution) { resolveOutcome(id, resolution) }
                    override suspend fun resolveOutcome(id: String, resolution: Resolution) =
                        try { interactions.resolveOutcome(id, resolution) }
                        catch (cancelled: CancellationException) { throw cancelled }
                        catch (error: Exception) { feedback.failure(error, true); throw error }
                }
                // Its to-dos show from the last read until the hub answers (#4533), as the group's chat bindings' do.
                // A new hub gets a new store, so todosReady starts false and a card does not call a task gone mid-switch.
                val tasksStore = persona.core.domain.tasks.TasksStore(persona.core.hub.HttpTasksApi(socket), runtime, data.coroutineScope,
                    ListSnapshotStore(File(data.noBackupDirectory, "lists")).todoCache())
                // Files, chat actions and machine metrics go to the group of the machine they name (one group: this hub).
                mutableSession.value = ShellSession(data.scope.path, runtime, sessions, chats, boundInteractions, sender,
                    assistant, inbox, preferences, HubTerminalTransport(socket), shell.files, mergedMetrics,
                    knowledgeConnection, shell.actions, phone, agentsWiring = agentsWiring, machines = machineManagement,
                    voice = voice, groups = hubGroups, tasks = tasksStore, hubUpdates = hubUpdates,
                    hubAddresses = { listOf(data.credentials.hubAddress) + data.credentials.candidateAddresses + socket.knownAddresses() },
                    computers = computers)
                try {
                    coroutineScope {
                        launch {
                            // A reconnect drops the route for a few seconds: the page keeps its repository (not the
                            // unpaired empty state) and reloads when the next route comes (#4030). Without a route it
                            // sends nothing and fails as unreachable.
                            socket.route.followingLastRoute { route ->
                                OnlyWithRoute(HttpKnowledgeRepository(data.scope, route.endpoint.url, hub.api.token, socket.http.client,
                                    isCurrent = { current() && socket.route.value == route })) { socket.route.value != null }
                            }.collect { knowledgeState.value = it }
                        }
                        launch {
                            ProcessLifecycleOwner.get().lifecycle.currentStateFlow.collect {
                                phone?.setForeground(it.isAtLeast(Lifecycle.State.STARTED))
                            }
                        }
                        launch { HubService.runningState.collect { phone?.setResidentConnection(it) } }
                        awaitCancellation()
                    }
                } finally {
                    knowledgeState.value = null
                    socket.phoneMessageHandler = null
                    phone?.close()
                    phoneTransport?.close()
                    if (mutableSession.value?.identity == data.scope.path) mutableSession.value = null
                }
            }
        }
    }

    /** Whether the last run was in demo mode; readable before the demo session is rebuilt. */
    fun demoSaved(): Boolean = mode.getBoolean("demo", false)

    suspend fun restoreDemo() {
        app.pairingReady.first { it }
        if (mode.getBoolean("demo", false) && app.hub.value == null) enterDemo()
    }

    @Suppress("DEPRECATION")
    suspend fun enterDemo() {
        if (demoJob != null) return
        check(app.hub.value == null) { "Disconnect pairing before demo" }
        val job = SupervisorJob(app.scope.coroutineContext[Job])
        demoJob = job
        val scope = CoroutineScope(app.scope.coroutineContext + job)
        val demo = persona.core.demo.DemoHub.fromAssets(app, scope)
        val localPreferences = object : HubPreferences {
            override val preferences = MutableStateFlow(HubPreferencesSnapshot())
            override suspend fun refresh() = Unit
            override suspend fun togglePin(path: String, space: Boolean) = Unit
            override suspend fun toggleMute(path: String, space: Boolean) = Unit
            override suspend fun markSeen(session: SessionKey, version: String) = Unit
        }
        val local = withContext(Dispatchers.IO) {
            ChatDataRepositories(app, demo.hubScope, demo, demo, scope, MutableStateFlow(true))
        }
        val database = Room.inMemoryDatabaseBuilder(app, PersistentDatabase::class.java).build()
        val messages = OutboxMessageSender(demo.hubScope, RoomOutboxStore(database, demo.hubScope), demo, scope)
        // "Hand to Persona" and the Persona tab write to the main conversation: without a resolver such a message waits for
        // ever and holds every message queued after it ("send automatically once connected"), the Agents' included.
        messages.connectMainConversation(AssistantMainConversationResolver(demo))
        val directory = File(app.noBackupFilesDir, "demo/${demo.hubScope.path}")
        val snapshots = ListSnapshotStore(File(directory, "lists"))
        val journal = InboxReplyJournal(File(directory, "replies"), demo.scope)
        // Demo list settings live with the demo data and disappear when it is discarded.
        val listPreferences = androidx.datastore.preferences.core.PreferenceDataStoreFactory.create(scope = scope) {
            File(directory, "lists.preferences_pb")
        }
        val localInbox = RealInboxRepository(demo.inboxConnection, journal, local.sessions, local.interactions, scope,
            snapshots = snapshots, hubPreferences = localPreferences, mainConversationSender = messages, messageSender = messages,
            download = { _, _ -> error(AppText.get(R.string.app_error_demo_no_data)) })
        val conversations = RealAssistantRepository(demo.inboxConnection, local.sessions, scope, snapshots, localPreferences)
        mode.edit().putBoolean("demo", true).apply()
        mutableSession.value = ShellSession(demo.hubScope.path, demo, local.sessions, local.chats, local.interactions,
            messages, conversations, localInbox, AgentsPreferences(listPreferences, localPreferences), demo = demo)
        scope.launch { demo.notices.collect { feedback.notice(it) } }
        scope.launch {
            try { awaitCancellation() }
            finally { withContext(NonCancellable + Dispatchers.IO) {
                demo.close(); messages.close(); local.close(discard = true); database.close(); journal.close()
                directory.deleteRecursively()
            } }
        }
        local.sessions.refresh(); local.interactions.refresh(); conversations.refresh(); localInbox.refresh()
        demo.reconnect()
    }

    fun exitDemo() {
        mode.edit().putBoolean("demo", false).apply()
        mutableSession.value = null
        demoJob?.cancel()
        demoJob = null
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    val terminalFacade = object : TerminalTransport {
        private fun active() = requireNotNull(mutableSession.value?.terminal) { AppText.get(R.string.app_error_terminal_unavailable) }
        /** The terminal of the session's group, with the group's plain key; the demo has its own. */
        private fun routed(key: SessionKey): Pair<TerminalTransport, SessionKey> =
            if (mutableSession.value?.demo != null || stacks.of(key.machineId) == null && 
                persona.core.contracts.GroupKeys.namespaceOf(key.machineId) == null) active() to key
            else shell.terminal(key) to persona.core.contracts.GroupKeys.rawKey(key)
        override val connection = runtime.state
        override val features = session.flatMapLatest { it?.terminal?.features ?: flowOf(emptySet()) }
            .stateIn(app.scope, SharingStarted.Eagerly, emptySet())
        override val events = runtime.events
        override fun subscribe(key: SessionKey, epoch: String?, after: Int, size: TerminalSize?, takeover: Boolean) =
            routed(key).let { (terminal, plain) -> terminal.subscribe(plain, epoch, after, size, takeover) }
        override fun unsubscribe(key: SessionKey) {
            runCatching { routed(key) }.getOrNull()?.let { (terminal, plain) -> terminal.unsubscribe(plain) }
        }
        override fun input(key: SessionKey, text: String, size: TerminalSize?, scroll: Int, at: TerminalCellPosition?, click: TerminalCellPosition?) =
            routed(key).let { (terminal, plain) -> terminal.input(plain, text, size, scroll, at, click) }
        override suspend fun screen(key: SessionKey) = routed(key).let { (terminal, plain) -> terminal.screen(plain) }
        override suspend fun keys(key: SessionKey, keys: List<String>, text: String) =
            routed(key).let { (terminal, plain) -> terminal.keys(plain, keys, text) }
    }
}

/** What was built for the last route, while the connection has none (reconnecting); a new route builds anew. */
internal fun <R : Any> Flow<HubRoute?>.followingLastRoute(build: (HubRoute) -> R): Flow<R> = filterNotNull().map(build)

/** [delegate] kept through a reconnect: while there is no route its requests fail as unreachable, nothing is sent. */
internal class OnlyWithRoute(private val delegate: KnowledgeRepository, private val hasRoute: () -> Boolean) : KnowledgeRepository {
    override val scope get() = delegate.scope
    private fun <T> routed(block: () -> T): T = if (hasRoute()) block() else throw HubFailure(HubError.NoNetwork)
    override suspend fun list(kind: KnowledgeKind, tag: String?) = routed { delegate }.list(kind, tag)
    override suspend fun detail(kind: KnowledgeKind, id: String) = routed { delegate }.detail(kind, id)
    override suspend fun search(query: String) = routed { delegate }.search(query)
    override suspend fun entry(kind: KnowledgeKind, id: String) = routed { delegate }.entry(kind, id)
    override suspend fun update(kind: KnowledgeKind, id: String, update: KnowledgeUpdate) = routed { delegate }.update(kind, id, update)
}

@Module
@InstallIn(SingletonComponent::class)
object ShellModule {
    @Provides @Singleton fun terminal(services: AppServices): TerminalTransport = services.terminalFacade
}
