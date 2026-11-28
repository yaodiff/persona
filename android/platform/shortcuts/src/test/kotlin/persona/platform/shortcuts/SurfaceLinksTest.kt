package persona.platform.shortcuts

import android.content.Context
import android.content.Intent
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.FleetItem
import persona.core.contracts.FleetState
import persona.core.contracts.Interaction
import persona.core.contracts.SessionKey
import persona.core.navigation.AppTab
import persona.core.navigation.AskPersonaLink
import persona.core.navigation.AssistantRoute
import persona.core.navigation.NotificationRoute
import persona.core.navigation.RequestRef
import persona.core.navigation.RouteInput
import persona.core.navigation.Router

/** A tap on a widget row or the tile lands on the request it shows, as a needs-you notification does (#4149). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class SurfaceLinksTest {
    private val context: Context get() = ApplicationProvider.getApplicationContext()
    private val second = FleetItem("mac/s1", title = "tests", kind = "permission", interaction = "second")
    // The hub sends no interaction on working rows; one here checks that a stray id is still not followed.
    private val working = FleetItem("mac/s2", title = "build", kind = "working", interaction = "stale")

    private fun request(intent: Intent?) = intent?.data?.getQueryParameter("request")

    @Test fun aWaitingRowOpensItsRequest() {
        val intent = SurfaceLinks.row(context, second) { false }
        assertEquals("s1", intent?.data?.getQueryParameter("session"))
        assertEquals("second", request(intent))
        // The tab rides along, so a cold start does not wait for session metadata.
        assertEquals("agents", intent?.data?.getQueryParameter("tab"))
        assertEquals("persona", SurfaceLinks.row(context, second) { true }?.data?.getQueryParameter("tab"))
    }

    @Test fun aWorkingRowOpensTheConversationOnly() {
        assertEquals("s2", SurfaceLinks.row(context, working) { false }?.data?.getQueryParameter("session"))
        assertNull(request(SurfaceLinks.row(context, working) { false }))
        assertNull(request(SurfaceLinks.row(context, second.copy(interaction = null)) { false }))
        assertNull(SurfaceLinks.row(context, second.copy(id = "nokey")) { false })
    }

    @Test fun theWholeWidgetWithOneAgentOpensItsRequest() {
        val one = FleetState(phase = "needsYou", needsYou = 1, items = listOf(second))
        assertEquals("second", request(SurfaceLinks.fleet(context, true, one) { false }))
        val two = FleetState(phase = "needsYou", needsYou = 2, items = listOf(second, second.copy(id = "mac/s3")))
        assertEquals("agents", SurfaceLinks.fleet(context, true, two) { false }.data?.getQueryParameter("tab"))
        val busy = FleetState(phase = "working", working = 1, items = listOf(working))
        assertEquals("s2", SurfaceLinks.fleet(context, true, busy) { false }.data?.getQueryParameter("session"))
        assertNull(request(SurfaceLinks.fleet(context, true, busy) { false }))
        assertNull(SurfaceLinks.fleet(context, false, one) { false }.data)
    }

    @Test fun approveFirstOpensTheRequestItCouldNotSettle() {
        val key = SessionKey("mac", "s1")
        assertEquals("q1", request(SurfaceLinks.conversation(context, key, false, "q1")))
        assertNull(request(SurfaceLinks.conversation(context, key, false)))
    }

    @Test fun approveFirstOpensACardOutsideAnySessionInAgents() {
        // `personad ask` in a plain terminal (#5719): no conversation to open, so Agents with that card, as a notification does.
        val intent = SurfaceLinks.request(context, Interaction("q1", "mac", "", kind = "question")) { null }
        assertEquals(NotificationRoute(AppTab.Agents, card = RequestRef("mac", "q1")), intent.data?.let(NotificationRoute::parse))
        val inSession = SurfaceLinks.request(context, Interaction("q1", "mac", "s1", kind = "question")) { false }
        assertEquals("s1", inSession.data?.getQueryParameter("session"))
        assertEquals("q1", request(inSession))
    }

    @Test fun mainChatLinkOpensExactMainConversationOrPersonaTab() {
        val key = SessionKey("hub", "main-1")
        val direct = SurfaceLinks.mainChat(context, key)
        assertEquals("persona", direct.data?.getQueryParameter("tab"))
        assertEquals("hub", direct.data?.getQueryParameter("machine"))
        assertEquals("main-1", direct.data?.getQueryParameter("session"))

        val fallback = SurfaceLinks.mainChat(context, null)
        assertEquals(NotificationRoute(AppTab.Persona), fallback.data?.let(NotificationRoute::parse))
        assertEquals(persona.core.navigation.AskPersonaLink, SurfaceLinks.ask(context).dataString)
        assertEquals(persona.core.navigation.NewTodoLink, SurfaceLinks.newTodo(context).dataString)
    }

    @Test fun askWidgetOpensMainChatComposerWhenPairedAndPairingPageWhenUnpaired() {
        val paired = SurfaceLinks.askWidget(context, paired = true)
        assertEquals(Intent.ACTION_VIEW, paired.action)
        assertEquals(AskPersonaLink, paired.dataString)
        assertEquals("persona", paired.data?.getQueryParameter("tab"))
        assertEquals("composer", paired.data?.getQueryParameter("focus"))
        val resolved = Router().parse(RouteInput.DeepLink(paired.dataString!!))
        assertNotNull(resolved)
        assertEquals(AppTab.Persona, resolved!!.tab)
        assertEquals(AssistantRoute, resolved.key)
        assertTrue(resolved.focusComposer)

        val unpaired = SurfaceLinks.askWidget(context, paired = false)
        assertNull(unpaired.data)
        assertEquals(context.packageName, unpaired.`package` ?: unpaired.component?.packageName)
    }
}
