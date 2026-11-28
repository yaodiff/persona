package persona.platform.shortcuts

import android.app.Application
import android.content.Context
import android.content.Intent
import androidx.test.core.app.ApplicationProvider
import dagger.hilt.internal.GeneratedComponent
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.JsonObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.Implementation
import org.robolectric.annotation.Implements
import org.robolectric.shadows.ShadowTileService
import org.robolectric.util.ReflectionHelpers
import persona.core.contracts.FleetItem
import persona.core.contracts.FleetState
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.PairingRequest
import persona.core.contracts.api.PairingService
import persona.core.contracts.api.SessionsRepository
import persona.platform.notify.FleetFeed
import persona.platform.notify.FleetSource

/** The tile hands navigation to a foreground activity before its service can die (#4055). */
@OptIn(ExperimentalCoroutinesApi::class)
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [33], application = TileRouteApplication::class, shadows = [TestShadowTileService::class])
class TileRouteTest {
    private lateinit var app: TileRouteApplication

    @Before fun setup() {
        app = ApplicationProvider.getApplicationContext()
        app.restoring = DelayedPairing()
        val saved = savedFleet()
        app.getSharedPreferences("fleet", Context.MODE_PRIVATE).edit()
            .putString("pairing", "hub/generation")
            .putString("snapshot", JsonCodec.encodeToString(FleetState.serializer(), saved)).commit()
        app.feed = FleetFeed(app, app.hubConnection, app.restoring, object : FleetSource {
            override fun pairing() = app.restoredScope
            override suspend fun fetch() = saved
        })
    }

    @After fun cleanup() {
        if (::app.isInitialized) {
            ReflectionHelpers.getField<CoroutineScope>(app.feed, "scope").cancel()
            app.getSharedPreferences("fleet", Context.MODE_PRIVATE).edit().clear().commit()
        }
        Dispatchers.resetMain()
    }

    @Test fun tileLaunchesRouterBeforeRestoreAndRoutesAfterServiceDestruction() = runTest {
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val service = Robolectric.buildService(PersonaTileService::class.java).create()
        service.get().onClick()
        val launched = shadowOf(app).nextStartedActivity
        assertNotNull("The tile must launch its foreground router before restore", launched)
        assertEquals(TileRouteActivity::class.java.name, launched.component?.className)

        // Closing the shade destroys the tile service, not the activity the system just launched.
        service.destroy()
        val activityController = Robolectric.buildActivity(TileRouteActivity::class.java, launched).create().start().resume()
        val activity = activityController.get()
        runCurrent()
        withContext(Dispatchers.IO) { withTimeout(5_000) { app.restoring.restoreStarted.await() } }
        assertFalse(activity.isFinishing)
        assertNull("No final route may be launched from the pre-restore fleet", shadowOf(activity).nextStartedActivity)

        app.completeRestore()
        withContext(Dispatchers.IO) { withTimeout(5_000) { app.feed.settled() } }
        runCurrent()
        val routed = shadowOf(activity).nextStartedActivity
        assertNotNull("The foreground router must perform the final navigation", routed)
        assertEquals("s1", routed.data?.getQueryParameter("session"))
        assertEquals("second", routed.data?.getQueryParameter("request"))
        assertTrue(activity.isFinishing)
        activityController.destroy()
    }
}

@Implements(PersonaTileService::class)
class TestShadowTileService : ShadowTileService() {
    @Implementation protected fun onDestroy() {
        // Avoid the framework shadow's ShadowTileService-to-ShadowService cast failure.
    }
}

class TileRouteApplication : Application(), GeneratedComponent, SurfacesDependencies {
    lateinit var restoring: DelayedPairing
    lateinit var feed: FleetFeed
    var restoredScope: String? = null
    val hubConnection = object : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }
    private val sessionRepository = object : SessionsRepository {
        override val machines = MutableStateFlow<List<Machine>>(emptyList())
        override val sessions = MutableStateFlow<List<Session>>(emptyList())
        override suspend fun refresh() = Unit
    }

    private val assistantRepository = object : AssistantRepository {
        override val assistant = MutableStateFlow(AssistantSnapshot())
        override suspend fun refresh() = Unit
    }

    override fun fleet() = feed
    override fun sessions() = sessionRepository
    override fun assistant() = assistantRepository
    override fun interactions(): InteractionCenter = error("Not used by tile routing")
    override fun pairing() = restoring
    override fun surfaces(): SurfacesRuntime = error("Not used by tile routing")

    fun completeRestore() {
        restoredScope = "hub/generation"
        restoring.complete()
    }
}

class DelayedPairing : PairingService {
    val restoreStarted = CompletableDeferred<Unit>()
    private val restored = CompletableDeferred<Unit>()
    override val isPaired = MutableStateFlow(false)
    override suspend fun restore() {
        restoreStarted.complete(Unit)
        restored.await()
    }
    override suspend fun pair(request: PairingRequest) = error("Not used by tile routing")
    override suspend fun unpair() = error("Not used by tile routing")
    fun complete() {
        isPaired.value = true
        restored.complete(Unit)
    }
}

private fun savedFleet() = FleetState(phase = "needsYou", needsYou = 1,
    items = listOf(FleetItem("mac/s1", title = "tests", kind = "permission", interaction = "second")))
