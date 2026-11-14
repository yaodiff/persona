package persona.feature.settings

import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.async
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.yield
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.ExtensionAuthEvent
import persona.core.contracts.api.ExtensionAuth
import persona.core.contracts.api.ExtensionListing
import persona.core.contracts.api.HubExtension
import persona.core.contracts.api.ManagerRoster

/** An auth push received while a listing load is in flight is newer than that load's answer (#7836). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ExtensionAuthReloadTest {
    @Test fun signInPushDuringListingReloadSurvivesTheStaleAnswer() = assertPushSurvivesReload(
        initial = ExtensionAuth(state = ExtensionAuth.SignedOut),
        pushed = ExtensionAuth(state = ExtensionAuth.SignedIn, account = "me"),
    )

    @Test fun signOutPushDuringListingReloadSurvivesTheStaleAnswer() = assertPushSurvivesReload(
        initial = ExtensionAuth(state = ExtensionAuth.SignedIn, account = "me"),
        pushed = ExtensionAuth(state = ExtensionAuth.SignedOut),
    )

    private fun assertPushSurvivesReload(initial: ExtensionAuth, pushed: ExtensionAuth) = runBlocking {
        val entered = CompletableDeferred<Unit>()
        val release = CompletableDeferred<Unit>()
        val pushes = MutableSharedFlow<ExtensionAuthEvent>(extraBufferCapacity = 1)
        val original = HubExtension(
            "example-notes", "Example Notes", status = "running", version = "1.0.0",
            auth = initial, manager = ManagerRoster.ASSISTANT_MANAGER,
        )
        // This response's other fields are new, but its auth was assembled before the push.
        val refreshed = original.copy(name = "Example Notes renamed", status = "stopped", version = "2.0.0")
        val api = object : ExtensionApiStub() {
            override val authUpdates: Flow<ExtensionAuthEvent> get() = pushes
            var reads = 0
            override suspend fun listing(): ExtensionListing {
                reads += 1
                if (reads == 1) return ExtensionListing(listOf(original))
                entered.complete(Unit)
                release.await()
                return ExtensionListing(listOf(refreshed), markets = true)
            }
        }
        val res = ApplicationProvider.getApplicationContext<android.content.Context>().resources
        val model = ExtensionListModel(api, res)
        model.load()
        val watcher = launch(start = CoroutineStart.UNDISPATCHED) { model.watchAuth() }
        val reload = async(start = CoroutineStart.UNDISPATCHED) { model.load() }
        try {
            entered.await()
            pushes.emit(ExtensionAuthEvent(original.id, pushed, manager = original.manager))
            withTimeout(2_000) {
                while (model.state.value.extensions?.single()?.auth != pushed) yield()
            }
            release.complete(Unit)
            reload.await()
            val extension = model.state.value.extensions?.single()
            assertEquals(pushed.state, extension?.auth?.state)
            assertEquals(pushed.account, extension?.auth?.account)
            assertEquals(refreshed.copy(auth = pushed), extension)
            assertTrue(model.state.value.markets)
        } finally {
            release.complete(Unit)
            reload.await()
            watcher.cancelAndJoin()
        }
    }
}
