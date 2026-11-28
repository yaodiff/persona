package persona.platform.shortcuts

import android.content.Context
import dagger.hilt.EntryPoint
import dagger.hilt.InstallIn
import dagger.hilt.android.EntryPointAccessors
import dagger.hilt.android.qualifiers.ApplicationContext
import dagger.hilt.components.SingletonComponent
import persona.core.contracts.Interaction
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.GroupKeys
import persona.core.contracts.api.PairingService
import persona.core.contracts.api.SessionsRepository
import persona.platform.notify.FleetFeed
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.FlowPreview
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.debounce
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.onEach
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import javax.inject.Inject
import javax.inject.Singleton

/**
 * Keeps the surfaces outside the app in step with the pairing: the widget and the tile redraw when the fleet changes,
 * the launcher's recent agents follow the session list, and all of it empties when the hub is unpaired.
 * Start it once from Application.onCreate; its first reads wait for the pairing restore.
 */
@Singleton
@OptIn(FlowPreview::class)
class SurfacesRuntime internal constructor(
    private val context: Context,
    private val fleet: FleetFeed,
    private val sessions: SessionsRepository,
    private val assistant: AssistantRepository,
    private val interactions: InteractionCenter,
    private val onlineWarmupGraceMillis: Long,
) {
    @Inject constructor(
        @ApplicationContext context: Context,
        fleet: FleetFeed,
        sessions: SessionsRepository,
        assistant: AssistantRepository,
        interactions: InteractionCenter,
    ) : this(context, fleet, sessions, assistant, interactions, ONLINE_WARMUP_GRACE_MILLIS)

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private var started = false
    private val mutableMainChatLivePairing = MutableStateFlow<String?>(null)

    /**
     * Pairing scope whose live main-chat repositories have explicitly settled (either via a live
     * repository emission or after the bounded online warmup window); null while unpaired or warming up.
     */
    internal val mainChatLivePairing: StateFlow<String?> = mutableMainChatLivePairing.asStateFlow()

    @Synchronized fun start() {
        if (started) return
        started = true
        fleet.start()
        ShortcutPublisher.publishFixed(context)
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            fleet.settled()
            combine(fleet.state, fleet.paired) { state, paired -> state to paired }.distinctUntilChanged().debounce(REDRAW_DEBOUNCE_MILLIS)
                .collect {
                    PersonaWidget.refresh(context)
                    PersonaTileService.refresh(context)
                }
        }
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            fleet.settled()
            var warmupJob: Job? = null
            var warmupPairing: String? = null
            var observedPairing: String? = null
            var repoReadiness = MainChatWidgetState.MainChatRepoReadiness()

            val repoFlow = combine(assistant.assistant, sessions.sessions, interactions.pending) { snapshot, list, pending ->
                Triple(snapshot, list, pending)
            }
            combine(
                fleet.paired,
                fleet.connectionState,
                repoFlow,
                mutableMainChatLivePairing,
            ) { paired, connState, (snapshot, list, pending), settledPairing ->
                MainChatObservation(
                    paired = paired,
                    activePairing = if (paired) fleet.activePairing()?.takeIf(String::isNotBlank) else null,
                    online = connState == ConnectionState.Online,
                    assistant = snapshot,
                    sessions = list,
                    interactions = pending,
                    settledPairing = settledPairing,
                )
            }
                .distinctUntilChanged()
                .onEach { obs ->
                    if (!obs.paired) {
                        warmupJob?.cancel()
                        warmupJob = null
                        warmupPairing = null
                        observedPairing = null
                        repoReadiness = MainChatWidgetState.MainChatRepoReadiness()
                        mutableMainChatLivePairing.value = null
                        MainChatWidgetStore.clear(context)
                        return@onEach
                    }

                    val activePairing = obs.activePairing
                    if (activePairing != observedPairing) {
                        warmupJob?.cancel()
                        warmupJob = null
                        warmupPairing = null
                        observedPairing = activePairing
                        repoReadiness = MainChatWidgetState.MainChatRepoReadiness()
                        if (mutableMainChatLivePairing.value != activePairing) {
                            mutableMainChatLivePairing.value = null
                        }
                        // Immediately evict any persisted snapshot belonging to a different or unscoped pairing.
                        MainChatWidgetStore.read(context, activePairing)
                    }

                    repoReadiness = MainChatWidgetState.repositoryReadiness(
                        assistant = obs.assistant,
                        sessions = obs.sessions,
                        interactions = obs.interactions,
                        previous = repoReadiness,
                    )
                    if (activePairing != null && repoReadiness.allReady) {
                        warmupJob?.cancel()
                        warmupJob = null
                        warmupPairing = null
                        if (mutableMainChatLivePairing.value != activePairing) {
                            mutableMainChatLivePairing.value = activePairing
                        }
                    } else if (activePairing != null && mutableMainChatLivePairing.value != activePairing) {
                        if (obs.online) {
                            if (warmupPairing != activePairing || warmupJob?.isActive != true) {
                                warmupJob?.cancel()
                                warmupPairing = activePairing
                                warmupJob = scope.launch {
                                    delay(onlineWarmupGraceMillis)
                                    if (isActive &&
                                        fleet.paired.value &&
                                        fleet.connectionState.value == ConnectionState.Online &&
                                        fleet.activePairing()?.takeIf(String::isNotBlank) == activePairing
                                    ) {
                                        mutableMainChatLivePairing.value = activePairing
                                    }
                                }
                            }
                        } else {
                            warmupJob?.cancel()
                            warmupJob = null
                            warmupPairing = null
                        }
                    }
                }
                .debounce(REDRAW_DEBOUNCE_MILLIS)
                .collect { obs ->
                    if (!obs.paired) {
                        MainChatWidgetStore.clear(context)
                        MainChatWidget.refresh(context)
                        return@collect
                    }

                    val activePairing = obs.activePairing
                    val liveSettled = MainChatWidgetState.isLiveSettled(
                        paired = true,
                        activePairing = activePairing,
                        settledPairing = obs.settledPairing,
                        assistant = obs.assistant,
                        sessions = obs.sessions,
                        interactions = obs.interactions,
                    )
                    val widgetSnapshot = MainChatWidgetState.build(
                        paired = true,
                        assistant = obs.assistant,
                        sessions = obs.sessions,
                        interactions = obs.interactions,
                        resources = context.resources,
                    )
                    if (liveSettled && activePairing != null) {
                        MainChatWidgetStore.save(context, activePairing, widgetSnapshot)
                        ConversationWidgetPrefs.allBindings(context, activePairing).values
                            .filterNot { it.isMain }
                            .distinctBy { it.conversationKey }
                            .forEach { binding ->
                                val boundSnapshot = MainChatWidgetState.build(
                                    paired = true,
                                    assistant = obs.assistant,
                                    sessions = obs.sessions,
                                    interactions = obs.interactions,
                                    resources = context.resources,
                                    targetBinding = binding,
                                )
                                MainChatWidgetStore.save(context, activePairing, boundSnapshot, binding.conversationKey)
                            }
                    }
                    MainChatWidget.refresh(context)
                    AskPersonaWidget.refresh(context)
                }
        }
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            fleet.settled()
            fleet.paired.debounce(REDRAW_DEBOUNCE_MILLIS)
                .collect { AskPersonaWidget.refresh(context) }
        }
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            fleet.settled()
            combine(sessions.sessions, fleet.paired) { list, paired ->
                if (paired) list.groupBy { GroupKeys.namespaceOf(it.machineId) }.values.flatMap { rows ->
                    RecentAgents.pick(rows, sessions.supportsFeature(rows.first().machineId, "sessions_hierarchy"))
                }.let { RecentAgents.pick(it, hierarchy = true) } else emptyList()
            }
                .distinctUntilChanged { old, new -> old.map(::label) == new.map(::label) }
                .collect { ShortcutPublisher.publishRecent(context, it) }
        }
    }

    /** What a recent agent's shortcut shows: republish only when that changes. */
    private fun label(session: Session) = Triple(session.key, session.displayTitle, session.assistant)

    private data class MainChatObservation(
        val paired: Boolean,
        val activePairing: String?,
        val online: Boolean,
        val assistant: AssistantSnapshot,
        val sessions: List<Session>,
        val interactions: List<Interaction>,
        val settledPairing: String?,
    )

    private companion object {
        const val REDRAW_DEBOUNCE_MILLIS = 300L
        const val ONLINE_WARMUP_GRACE_MILLIS = 3_000L
    }
}

/** For the widget, the tile and the dialog, which the system creates outside Hilt's reach. */
@EntryPoint
@InstallIn(SingletonComponent::class)
internal interface SurfacesDependencies {
    fun fleet(): FleetFeed
    fun sessions(): SessionsRepository
    fun assistant(): AssistantRepository
    fun interactions(): InteractionCenter
    fun pairing(): PairingService
    fun surfaces(): SurfacesRuntime

    companion object {
        fun of(context: Context): SurfacesDependencies =
            EntryPointAccessors.fromApplication(context.applicationContext, SurfacesDependencies::class.java)
    }
}

/** Whether a session is the assistant's, when the session list knows it (null before it has loaded). */
internal fun SessionsRepository.assistant(key: SessionKey): Boolean? = sessions.value.firstOrNull { it.key == key }?.assistant
