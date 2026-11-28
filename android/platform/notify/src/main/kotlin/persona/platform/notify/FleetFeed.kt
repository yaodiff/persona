package persona.platform.notify

import android.content.Context
import android.util.Log
import dagger.hilt.android.qualifiers.ApplicationContext
import persona.core.contracts.FleetEvent
import persona.core.contracts.FleetState
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.PairingService
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull
import javax.inject.Inject
import javax.inject.Singleton

/** The pairing's fleet over HTTP; supplied by the app, which owns the connection and the pairing scope. */
interface FleetSource {
    /** The pairing the fleet belongs to (its scope path); null when not paired. */
    fun pairing(): String?

    /** GET /api/v1/fleet: the hub pushes `fleet` only when it changes, so a fresh connection asks once. */
    suspend fun fetch(): FleetState
}

/**
 * The hub's fleet summary for this process (`fleet` events, plus one fetch each time the connection comes up), and the
 * last one on disk: the home-screen widget and the Quick Settings tile draw it when the process was not running. It
 * belongs to one pairing: unpairing clears it, a different pairing never shows the previous one's.
 */
@Singleton
class FleetFeed @Inject constructor(
    @ApplicationContext context: Context,
    private val connection: HubConnection,
    private val pairing: PairingService,
    private val source: FleetSource,
) {
    private val preferences = context.getSharedPreferences("fleet", Context.MODE_PRIVATE)
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private val mutable = MutableStateFlow(FleetState())
    private var started = false
    @Volatile private var startup: Job? = null

    /** The latest fleet of the current pairing; empty when there is none. */
    val state: StateFlow<FleetState> = mutable.asStateFlow()

    /** Whether a hub is paired: the widget and the tile say "Not paired" otherwise. */
    val paired: StateFlow<Boolean> get() = pairing.isPaired

    /** The hub connection state observed by this feed. */
    val connectionState: StateFlow<ConnectionState> get() = connection.state

    /** The active pairing's scope path, or null when unpaired (same key that scopes persisted snapshots). */
    fun activePairing(): String? = source.pairing()

    /** Call once at process start; the first snapshot and collectors wait for the pairing restore. */
    @Synchronized fun start() {
        if (started) return
        started = true
        startup = scope.launch(start = CoroutineStart.UNDISPATCHED) {
            pairing.restore()
            mutable.value = restore()
            scope.launch { connection.events.collect { if (it is FleetEvent) publish(it.fleet) } }
            scope.launch {
                connection.state.collect { state ->
                    // Paired with another hub since: the previous one's agents must not show.
                    if (preferences.getString(KEY_PAIRING, null)?.let { it != source.pairing() } == true) clear()
                    if (state != ConnectionState.Online) return@collect
                    try { withTimeoutOrNull(FETCH_TIMEOUT_MILLIS) { source.fetch() }?.let(::publish) }
                    catch (cancelled: CancellationException) { throw cancelled }
                    catch (failure: Exception) { Log.w(TAG, "Fleet fetch failed: ${failure.javaClass.simpleName}") }
                }
            }
            scope.launch { pairing.isPaired.collect { if (!it) clear() } }
        }
    }

    /** Waits until the saved snapshot is visible, not only until the pairing restore has finished. */
    suspend fun settled() {
        start()
        startup?.join()
    }

    @Synchronized internal fun publish(fleet: FleetState) {
        val pairing = source.pairing() ?: return
        if (fleet == mutable.value && preferences.getString(KEY_PAIRING, null) == pairing) return
        mutable.value = fleet
        preferences.edit().putString(KEY_PAIRING, pairing).putString(KEY_SNAPSHOT, JsonCodec.encodeToString(FleetState.serializer(), fleet)).apply()
    }

    @Synchronized private fun clear() {
        mutable.value = FleetState()
        preferences.edit().remove(KEY_PAIRING).remove(KEY_SNAPSHOT).apply()
    }

    /** The saved snapshot, when it is the current pairing's. */
    private fun restore(): FleetState {
        val saved = preferences.getString(KEY_SNAPSHOT, null) ?: return FleetState()
        if (preferences.getString(KEY_PAIRING, null) != source.pairing()) return FleetState()
        return runCatching { JsonCodec.decodeFromString(FleetState.serializer(), saved) }.getOrDefault(FleetState())
    }

    private companion object {
        const val TAG = "FleetFeed"
        const val KEY_PAIRING = "pairing"
        const val KEY_SNAPSHOT = "snapshot"
        const val FETCH_TIMEOUT_MILLIS = 15_000L
    }
}
