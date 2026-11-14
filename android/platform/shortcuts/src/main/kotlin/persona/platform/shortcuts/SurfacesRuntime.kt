package persona.platform.shortcuts

import android.content.Context
import dagger.hilt.EntryPoint
import dagger.hilt.InstallIn
import dagger.hilt.android.EntryPointAccessors
import dagger.hilt.android.qualifiers.ApplicationContext
import dagger.hilt.components.SingletonComponent
import persona.core.contracts.SessionKey
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.GroupKeys
import persona.core.contracts.api.PairingService
import persona.core.contracts.api.SessionsRepository
import persona.platform.notify.FleetFeed
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.FlowPreview
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.debounce
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.launch
import javax.inject.Inject
import javax.inject.Singleton

/**
 * Keeps the surfaces outside the app in step with the pairing: the widget and the tile redraw when the fleet changes,
 * the launcher's recent agents follow the session list, and all of it empties when the hub is unpaired.
 * Start it once from Application.onCreate, after the pairing is restored.
 */
@Singleton
@OptIn(FlowPreview::class)
class SurfacesRuntime @Inject constructor(
    @ApplicationContext private val context: Context,
    private val fleet: FleetFeed,
    private val sessions: SessionsRepository,
) {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private var started = false

    @Synchronized fun start() {
        if (started) return
        started = true
        fleet.start()
        ShortcutPublisher.publishFixed(context)
        scope.launch {
            combine(fleet.state, fleet.paired) { state, paired -> state to paired }.distinctUntilChanged().debounce(REDRAW_DEBOUNCE_MILLIS)
                .collect {
                    PersonaWidget.refresh(context)
                    PersonaTileService.refresh(context)
                }
        }
        scope.launch {
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
    private fun label(session: persona.core.contracts.Session) = Triple(session.key, session.displayTitle, session.assistant)

    private companion object { const val REDRAW_DEBOUNCE_MILLIS = 300L }
}

/** For the widget, the tile and the dialog, which the system creates outside Hilt's reach. */
@EntryPoint
@InstallIn(SingletonComponent::class)
internal interface SurfacesDependencies {
    fun fleet(): FleetFeed
    fun sessions(): SessionsRepository
    fun interactions(): InteractionCenter
    fun pairing(): PairingService

    companion object {
        fun of(context: Context): SurfacesDependencies =
            EntryPointAccessors.fromApplication(context.applicationContext, SurfacesDependencies::class.java)
    }
}

/** Whether a session is the assistant's, when the session list knows it (null before it has loaded). */
internal fun SessionsRepository.assistant(key: SessionKey): Boolean? = sessions.value.firstOrNull { it.key == key }?.assistant
