package persona.platform.notify

import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import android.os.Process
import android.os.SystemClock
import android.util.Log
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import dagger.hilt.android.AndroidEntryPoint
import persona.core.contracts.GroupKeys
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.PairingService
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.SessionsRepository
import persona.core.contracts.api.VerifiedSessionsRepository
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import persona.core.domain.fleet.FleetSummary
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharingStarted
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.flow.stateIn
import kotlinx.coroutines.flow.transformLatest
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import javax.inject.Inject

@AndroidEntryPoint
@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
open class HubService : Service() {
    /** Text in the language of Settings › Language (docs/protocol/i18n.md). */
    override fun attachBaseContext(base: android.content.Context) = super.attachBaseContext(persona.core.designsystem.AppLanguages.wrap(base))

    @Inject lateinit var connection: HubConnection
    @Inject lateinit var pairing: PairingService
    @Inject lateinit var notifier: Notifier
    @Inject lateinit var journal: ExitJournal
    @Inject lateinit var interactions: InteractionCenter
    @Inject lateinit var sessions: SessionsRepository
    @Inject lateinit var notificationPreferences: NotificationPreferencesSource
    @Inject lateinit var fleet: FleetFeed

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private var observing = false

    /**
     * What the ongoing notification shows now. Every start promotes with it (the app coming to the front starts the
     * service again): a summary made afresh would bring back the finished agents the five minutes already put away, and
     * the collector below, seeing nothing new, would leave them there until the fleet changed.
     */
    private var shown: OngoingStatus? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        journal.capture()
        // Promote before deciding anything: stopping first (not paired, stopped by the user, or a stop that came while
        // this start was pending) is what crashed the app (ForegroundStarts). stopSelf(startId) leaves a newer start alone.
        try {
            val status = shown ?: OngoingStatus.from(connection.state.value, fleet.state.value,
                details = notificationPreferences.preferences.value?.lockDetails == true, pending = interactions.pending.value)
            ServiceCompat.startForeground(this, Notifier.ONGOING_ID, notifier.ongoing(status),
                if (Build.VERSION.SDK_INT >= 34) ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE else 0)
            shown = status
        } catch (failure: RuntimeException) {
            Log.w(TAG, "Foreground promotion failed: ${failure.javaClass.simpleName}")
            starts.promoted()
            stopSelf(startId)
            return START_NOT_STICKY
        }
        if (starts.promoted() || journal.userStopped) {
            stopSelf(startId)
            return START_NOT_STICKY
        }
        scope.launch(start = CoroutineStart.UNDISPATCHED) {
            pairing.restore()
            if (!pairing.isPaired.value) {
                stopSelf(startId)
                return@launch
            }
            running = true
            if (!observing) {
                observing = true
                scope.launch {
                    fleet.settled()
                    // Only finished agents left for a while: back to the connection line (the hub's fleet stays as it is).
                    val quiet = fleet.state.map { FleetSummary.phase(it) == FleetSummary.DONE }.distinctUntilChanged()
                        .transformLatest { done ->
                            emit(false)
                            if (done) { delay(FleetSummary.DONE_LINGER_MILLIS); emit(true) }
                        }.stateIn(this, SharingStarted.Eagerly, false)
                    // "Show details on lock screen" decides whether names show at all (OngoingStatus.from); any other preference may change how
                    // the same content is drawn (visibility, promotion), so a change redraws.
                    launch {
                        notificationPreferences.preferences.collect { preferences ->
                            show(OngoingStatus.from(connection.state.value, fleet.state.value, quiet.value,
                                details = preferences?.lockDetails == true, pending = interactions.pending.value))
                        }
                    }
                    // The open cards say whether the first permission may be allowed from here (#4987).
                    combine(connection.state, fleet.state, quiet, notificationPreferences.preferences, interactions.pending) { state, snapshot, done, preferences, pending ->
                        OngoingStatus.from(state, snapshot, done, details = preferences?.lockDetails == true, pending = pending)
                    }.throttleOngoing().collect { status ->
                        if (status != shown) show(status)
                    }
                }
                scope.launch {
                    val delivery = NotificationDelivery(interactions, sessions, ::suppressed,
                        notifier::postInteraction, notifier::postNotify, notifier::retract,
                        notificationPreferences::refreshPreferences, notifier::deliveredRequests,
                        showing = notifier::showingRequests,
                        retractOccurrence = { id, occurrence -> notifier.retract(id, occurrence) },
                        notifiedOccurrences = notifier::deliveredOccurrences)
                    Notify.events.notificationEvents().collect { event ->
                        try {
                            // Bound refresh work without terminating observation on a transient HTTP failure.
                            withTimeout(8_000) { delivery.accept(event) }
                        } catch (failure: Exception) {
                            if (failure is CancellationException && failure !is kotlinx.coroutines.TimeoutCancellationException) throw failure
                            Log.w(TAG, "Notification refresh failed: ${failure.javaClass.simpleName}")
                        }
                    }
                }
                // Requests answered elsewhere while a reconnect did not resume (#3401).
                scope.launch(Dispatchers.Default) {
                    InteractionNotificationReconciler(notifier::showingRequests, notifier::retract)
                        .run(interactions.pending, connection.state.map { it == ConnectionState.Online },
                            sessions.machines.map { list -> list.mapTo(HashSet()) { GroupKeys.namespaceOf(it.id) } })
                }
                // "Finished" notifications left by an earlier process, for sessions seen meanwhile (#4088).
                (sessions as? VerifiedSessionsRepository)?.let { verified ->
                    scope.launch(Dispatchers.Default) {
                        // Process start on the wall clock (postTime's clock); a clock change since then only shifts the cut-off.
                        val since = System.currentTimeMillis() - (SystemClock.elapsedRealtime() - Process.getStartElapsedRealtime())
                        StaleDoneNotifications(notifier::showingDone, { verified.verifiedSessions().values.flatten() }, notifier::retractDone, since)
                            .run(connection.state.map { it == ConnectionState.Online }.distinctUntilChanged())
                    }
                }
                scope.launch {
                    pairing.isPaired.collect { if (!it && !starts.deferStop()) stopSelf() }
                }
                // Network changes are the connection's own business (it reconnects only when the network really changed,
                // #4139); a second callback here tore every change down twice and an online connection down at each start.
                ResidentConnection.resume(connection)
            }
        }
        return START_STICKY
    }

    /**
     * Redraws the ongoing notification, unless the pairing is gone and the service is on its way out. Unpairing changes
     * the connection state and the preferences just before the service stops, and the system may handle a post that
     * late after stopForeground has taken the notification away: it stayed, "Waiting for network", ongoing and impossible to
     * dismiss (2026-11-02 on the emulator).
     */
    private fun show(status: OngoingStatus) {
        if (!pairing.isPaired.value) return
        notifier.updateOngoing(status)
        shown = status
    }

    private fun suppressed(session: SessionKey) =
        NotificationPolicy.suppressed(session, Notify.foreground.value, Notify.visibleSession.value)

    override fun onDestroy() {
        running = false
        scope.cancel()
        ServiceCompat.stopForeground(this, ServiceCompat.STOP_FOREGROUND_REMOVE)
        super.onDestroy()
    }

    companion object {
        private const val TAG = "HubService"
        private val live = MutableStateFlow(false)
        private val starts = ForegroundStarts()
        val runningState = live.asStateFlow()
        @Volatile internal var running = false
            private set(value) { field = value; live.value = value }

        fun start(context: Context): Boolean {
            starts.requested()
            return try {
                ContextCompat.startForegroundService(context, Intent(context, persona.platform.notify.HubService::class.java))
                true
            } catch (failure: RuntimeException) {
                starts.failed()
                Log.w(TAG, "Service launch failed: ${failure.javaClass.simpleName}")
                false
            }
        }

        /**
         * Stops the service only once it runs in the foreground. While one of our starts is pending the stop is left to
         * it (see [ForegroundStarts]); a start we did not send (the system restarting the service in a new process,
         * `am start-foreground-service`) is pending unseen while Application.onCreate runs this, and it checks pairing
         * and the user's stop itself after promoting.
         */
        fun stop(context: Context) {
            if (!starts.deferStop() && running) context.stopService(Intent(context, persona.platform.notify.HubService::class.java))
        }
    }
}
