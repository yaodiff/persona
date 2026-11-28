package persona.platform.notify

import android.app.Application
import android.content.BroadcastReceiver
import android.content.Intent
import androidx.test.core.app.ApplicationProvider
import androidx.work.ListenableWorker
import androidx.work.testing.TestListenableWorkerBuilder
import androidx.work.testing.WorkManagerTestInitHelper
import dagger.hilt.internal.GeneratedComponent
import java.util.concurrent.atomic.AtomicBoolean
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.async
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.JsonObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.util.ReflectionHelpers
import persona.core.contracts.FleetState
import persona.core.contracts.Interaction
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.DeviceNotificationPreferences
import persona.core.contracts.api.ExtensionNotificationActionRecord
import persona.core.contracts.api.ExtensionNotificationActionStore
import persona.core.contracts.api.ExtensionNotificationScope
import persona.core.contracts.api.ExtensionStreamBinding
import persona.core.contracts.api.ExtensionUi
import persona.core.contracts.api.ExtensionUiContext
import persona.core.contracts.api.ExtensionUiPlace
import persona.core.contracts.api.ExtensionUiResult
import persona.core.contracts.api.ExtensionView
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.PairingService
import persona.core.contracts.api.SessionsRepository
import persona.core.contracts.generated.ExtensionNotifyAction

/** #4055 AC2: process entry points keep their original work pending until pairings are restored. */
@OptIn(ExperimentalCoroutinesApi::class)
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34], application = NotifyRestoreApplication::class)
class ReceiverPairingRestoreTest {
    private lateinit var app: NotifyRestoreApplication

    @Before fun setup() {
        app = ApplicationProvider.getApplicationContext()
        app.restoring = RestoringPairingService()
        WorkManagerTestInitHelper.initializeTestWorkManager(app)
        ReflectionHelpers.setStaticField(HubService::class.java, "running", false)
        resetForegroundStartsForTest()
    }

    @After fun cleanup() {
        ReflectionHelpers.setStaticField(HubService::class.java, "running", false)
        resetForegroundStartsForTest()
        Dispatchers.resetMain()
        WorkManagerTestInitHelper.closeWorkDatabase()
    }

    @Test fun bootWaitsForRestoreInsteadOfSkippingTheResidentService() = runTest {
        receive(BootReceiver(), Intent(Intent.ACTION_BOOT_COMPLETED))

        app.restoring.awaitRestore()
        assertNull("Boot must not decide from transient empty pairings", shadowOf(app).nextStartedService)

        app.restoring.completeRestore()
        var started: Intent? = null
        awaitRealCondition("Boot did not continue after restore") {
            started = shadowOf(app).nextStartedService
            started != null
        }
        assertEquals(HubService::class.java.name, started?.component?.className)
    }

    @Test fun resumeKeepsTheBroadcastPendingUntilRestoreCompletes() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val nested = CompletableDeferred<Unit>()
        app.restoring.resumeRestore = nested
        app.getSharedPreferences("notify_recovery", android.content.Context.MODE_PRIVATE)
            .edit().putBoolean("user_stopped", true).commit()
        app.journal().capture()
        val pending = receive(ActionReceiver(), Intent(ActionReceiver.RESUME))
        advanceUntilIdle()

        app.restoring.awaitRestore()
        assertTrue(app.journal().userStopped)
        assertFalse("RESUME must keep its broadcast pending during restore", shadowOf(pending).future.isDone)

        app.restoring.completeRestore()
        advanceUntilIdle()
        assertTrue("The resume action must not run before its own job completes", app.journal().userStopped)
        assertFalse("The receiver must join the resume job", shadowOf(pending).future.isDone)

        nested.complete(Unit)
        advanceUntilIdle()
        awaitRealCondition("RESUME did not finish after restore") { shadowOf(pending).future.isDone }
        assertFalse(app.journal().userStopped)
    }

    @Test fun resolveWaitsForRestoreBeforeReadingOrAnsweringTheHub() = runTest {
        val intent = Intent(ActionReceiver.RESOLVE)
            .putExtra(ActionReceiver.EXTRA_ID, app.center.request.id)
            .putExtra(ActionReceiver.EXTRA_ACTION, "allow")
        receive(ActionReceiver(), intent)

        app.restoring.awaitRestore()
        assertEquals("A notification action must not read the hub before restore", 0, app.center.refreshes)
        assertEquals(0, app.center.resolutions)

        app.restoring.completeRestore()
        app.center.answered.awaitReal()
        assertEquals(1, app.center.refreshes)
        assertEquals(1, app.center.resolutions)
    }

    @Test fun staleActionCannotRetractOrOverwriteItsReplacementNotification() = runTest {
        val original = app.center.request.copy(createdAt = "2030-01-01T00:00:00.000000001Z")
        val replacement = app.center.request.copy(createdAt = "2030-01-01T00:00:00.000000002Z")
        app.center.request = replacement
        app.center.pending.value = listOf(replacement)
        shadowOf(app).grantPermissions(android.Manifest.permission.POST_NOTIFICATIONS)
        app.showForTest(replacement)
        app.restoring.completeRestore()
        val intent = Intent(ActionReceiver.RESOLVE)
            .putExtra(ActionReceiver.EXTRA_ID, original.id)
            .putExtra(ActionReceiver.EXTRA_ACTION, "allow")
            .putExtra(ActionReceiver.EXTRA_CREATED_AT, original.createdAt)
        val pending = receive(ActionReceiver(), intent)
        awaitRealCondition("stale notification action did not finish") { shadowOf(pending).future.isDone }
        assertEquals("the replacement's notification must survive the old action", setOf(replacement.id), app.notifier().showingRequests())
        assertEquals(0, app.center.resolutions)
    }

    @Test fun failedActionCannotOverwriteItsReplacementNotification() = runTest {
        val original = app.center.request.copy(createdAt = "2030-01-01T00:00:00.000000001Z")
        val replacement = app.center.request.copy(createdAt = "2030-01-01T00:00:00.000000002Z")
        app.center.request = replacement
        app.center.pending.value = emptyList() // cold action: the old occurrence is not cached
        app.restoring.failure = RuntimeException("restore failed")
        shadowOf(app).grantPermissions(android.Manifest.permission.POST_NOTIFICATIONS)
        app.showForTest(replacement)
        val intent = Intent(ActionReceiver.RESOLVE)
            .putExtra(ActionReceiver.EXTRA_ID, original.id)
            .putExtra(ActionReceiver.EXTRA_ACTION, "allow")
            .putExtra(ActionReceiver.EXTRA_CREATED_AT, original.createdAt)
        val pending = receive(ActionReceiver(), intent)
        awaitRealCondition("failed notification action did not finish") { shadowOf(pending).future.isDone }
        assertEquals("the exception result must retain the displayed occurrence fence", setOf(replacement.id), app.notifier().showingRequests())
        assertEquals(0, app.center.resolutions)
    }

    @Test fun extensionActionWaitsForRestoreBeforeClaimingOrExecuting() = runTest {
        val scope = ExtensionNotificationScope("hub/generation", null, "assistant")
        val ui = RestoreUi()
        app.extensions = object : ExtensionNotificationEnvironment {
            override fun currentScope() = scope
            override fun extensionUi(record: ExtensionNotificationActionRecord) = ui
        }
        val tag = "restore-extension-action"
        val store = ExtensionNotificationActionStore.shared
        val record = requireNotNull(store.register(tag, "example-gateway", "usage", "state",
            ExtensionNotifyAction("stop", "Stop"), scope) { scope })
        try {
            receive(ExtensionNotificationActionReceiver(), ExtensionNotificationIntents(app).broadcastIntent(record))

            app.restoring.awaitRestore()
            assertEquals("The extension action must not be claimed and executed before restore", 0, ui.calls)

            app.restoring.completeRestore()
            ui.called.awaitReal()
            assertEquals(1, ui.calls)
        } finally {
            store.retire(tag)
        }
    }

    @Test fun watchdogWaitsForRestoreInsteadOfReturningUnpairedSuccess() = runTest {
        ReflectionHelpers.setStaticField(HubService::class.java, "running", true)
        val worker = TestListenableWorkerBuilder.from(app, ConnectionWatchdog::class.java).build()
        val result = async(Dispatchers.IO) { worker.doWork() }

        app.restoring.awaitRestore()
        assertTrue("The watchdog must remain pending during restore", result.isActive)
        assertEquals(0, app.hubConnection.wakes)

        app.restoring.completeRestore()
        val completed = withContext(Dispatchers.IO) { withTimeout(5_000) { result.await() } }
        assertTrue(completed is ListenableWorker.Result.Success)
        assertEquals("The restored watchdog must perform its original wake", 1, app.hubConnection.wakes)
    }
    private fun receive(receiver: BroadcastReceiver, intent: Intent): BroadcastReceiver.PendingResult {
        val pending = ReflectionHelpers.newInstance(BroadcastReceiver.PendingResult::class.java)
        ReflectionHelpers.setField(receiver, "mPendingResult", pending)
        shadowOf(receiver).onReceive(app, intent, AtomicBoolean())
        return pending
    }
}

/** EntryPointAccessors accepts a GeneratedComponent directly; no static mocking or production test hooks are needed. */
internal class NotifyRestoreApplication : Application(), GeneratedComponent, NotifyDependencies {
    internal lateinit var restoring: RestoringPairingService
    internal lateinit var extensions: ExtensionNotificationEnvironment
    internal val hubConnection = RecordingConnection()
    internal val center = RecordingCenter()
    internal val sessionRepository = object : SessionsRepository {
        override val machines = MutableStateFlow<List<Machine>>(emptyList())
        override val sessions = MutableStateFlow<List<Session>>(emptyList())
        override suspend fun refresh() = Unit
    }
    private val preferences = object : NotificationPreferencesSource {
        override val preferences = MutableStateFlow<DeviceNotificationPreferences?>(null)
        override suspend fun refreshPreferences() = preferences.value
    }
    private val notificationManager by lazy { Notifier(this, sessionRepository, preferences) }
    private val exits by lazy { ExitJournal(this) }
    private val claims by lazy { NotificationActionJournal(this) }
    private val notificationRuntime by lazy {
        val feed = FleetFeed(this, hubConnection, restoring.service, object : FleetSource {
            override fun pairing() = "hub/generation"
            override suspend fun fetch() = FleetState()
        })
        NotificationRuntime(this, restoring.service, exits, notificationManager, feed, sessionRepository, ResidentConnectionRequests(this))
    }

    override fun connection(): HubConnection = hubConnection
    override fun interactions(): InteractionCenter = center
    override fun sessions(): SessionsRepository = sessionRepository
    override fun pairing(): PairingService = restoring.service
    override fun notifier(): Notifier = notificationManager
    override fun notificationPreferences(): NotificationPreferencesSource = preferences
    override fun actionJournal(): NotificationActionJournal = claims
    override fun journal(): ExitJournal = exits
    override fun runtime(): NotificationRuntime = notificationRuntime
    override fun extensionNotifications(): ExtensionNotificationEnvironment = extensions

    internal fun showForTest(request: Interaction) {
        preferences.preferences.value = DeviceNotificationPreferences(input = true)
        notificationManager.postInteraction(request)
    }
}

internal class RecordingConnection : HubConnection {
    override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
    override val events = MutableSharedFlow<ServerEvent>()
    var wakes = 0
    override fun reconnect() { wakes++ }
    override fun send(message: JsonObject) = true
}

internal class RecordingCenter : InteractionCenter {
    var request = Interaction("restore-action", machineId = "machine", sessionId = "session", kind = "permission")
    override val pending = MutableStateFlow(listOf(request))
    val answered = CompletableDeferred<Unit>()
    var refreshes = 0
    var resolutions = 0
    override suspend fun refresh() {
        refreshes++
        pending.value = listOf(request)
    }
    override suspend fun resolve(id: String, resolution: Resolution) {
        resolutions++
        answered.complete(Unit)
    }
}

private class RestoreUi : ExtensionUi {
    val called = CompletableDeferred<Unit>()
    var calls = 0
    override val nativeStream: Flow<ExtensionStreamBinding?> = flowOf(null)
    override suspend fun uiPlaces(placement: String, launcher: String?, machine: String?): List<ExtensionUiPlace> = emptyList()
    override suspend fun renderUi(extension: String, ui: String, context: ExtensionUiContext?, state: String?) = ExtensionView()
    override suspend fun uiAction(extension: String, ui: String, action: String, context: ExtensionUiContext?, state: String?,
        values: Map<String, String>?): ExtensionUiResult {
        calls++
        called.complete(Unit)
        return ExtensionUiResult(toast = "done")
    }
}
