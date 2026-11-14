package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.mockwebserver.MockResponse
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.ServerEvent
import persona.core.contracts.SubscriptionEvent
import persona.core.contracts.api.ExtensionUiContext
import persona.core.contracts.api.SUBSCRIPTIONS_UI
import persona.core.contracts.api.SubscriptionSummaryLine
import persona.core.contracts.api.subscriptionCounts
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer

/** Source subscriptions (#2041): the manager's pages through the extension page client, the entry rows' summary and event. */
class HttpSubscriptionsTest {
    private val device = ExtensionUiContext(tz = "Asia/Shanghai", locale = "zh-Hans-CN", platform = "android", uiVersion = 6)

    private fun <T> withHub(block: suspend (MockWebServer, HttpExtensionManagement) -> T): T = runBlocking {
        MockWebServer().use { server ->
            val http = OkHttpClient.Builder().retryOnConnectionFailure(false).build()
            block(server, HttpExtensionManagement({ path, method, body ->
                http.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute()
            }, device = { device }, legacy = false))
        }
    }

    @Test fun thePagesAreDrawnAndPressedAtTheManagersRoute() = withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"view":{"title":"Subscriptions","sections":[{"items":[{"type":"text","text":"hi"}]}]}}"""))
        val view = api.renderUi(SUBSCRIPTIONS_UI, "pages", null, """{"ext":"apps"}""")
        val drawn = server.takeRequest()
        assertEquals("/api/v1/subscriptions/-/ui", drawn.path)
        assertTrue(drawn.body.readUtf8().contains(""""state":"{\"ext\":\"apps\"}""""))
        assertEquals("Subscriptions", view.title)

        server.enqueue(MockResponse().setBody("""{"toast":"Queued."}"""))
        api.uiAction(SUBSCRIPTIONS_UI, "pages", "run_now", null, """{"p":"source","id":"sub_1"}""", null)
        val pressed = server.takeRequest()
        assertEquals("/api/v1/subscriptions/-/ui", pressed.path)
        assertTrue(pressed.body.readUtf8().contains(""""action":"run_now""""))
    }

    /** #5661: these pages are told `context.assistant`, including "" so the key is present; other calls omit it. */
    @Test fun thePagesAreToldTheAssistantsName() = withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"view":{"title":"Sources","sections":[]}}"""))
        api.renderUi(SUBSCRIPTIONS_UI, "pages", ExtensionUiContext(assistant = ""))
        assertTrue(server.takeRequest().body.readUtf8().contains("\"assistant\":\"\""))
        server.enqueue(MockResponse().setBody("""{"view":{"title":"Sources","sections":[]}}"""))
        api.renderUi(SUBSCRIPTIONS_UI, "pages", ExtensionUiContext(assistant = "Claude"))
        val named = server.takeRequest().body.readUtf8()
        assertTrue(named.contains("\"assistant\":\"Claude\""))
        server.enqueue(MockResponse().setBody("""{"toast":"Queued."}"""))
        api.uiAction(SUBSCRIPTIONS_UI, "pages", "setup", ExtensionUiContext(assistant = ""), null, null)
        assertTrue(server.takeRequest().body.readUtf8().contains("\"assistant\":\"\""))
        server.enqueue(MockResponse().setBody("""{"view":{"title":"Sources","sections":[]}}"""))
        api.renderUi(SUBSCRIPTIONS_UI, "pages", null)
        assertFalse(server.takeRequest().body.readUtf8().contains("assistant"))
        server.enqueue(MockResponse().setBody("""{"view":{"sections":[]}}"""))
        api.renderUi("example-gateway", "home", ExtensionUiContext(assistant = null))
        assertFalse(server.takeRequest().body.readUtf8().contains("assistant"))
    }

    @Test fun theSummaryCountsWhatIsOnAndFailing() = withHub { server, api ->
        server.enqueue(MockResponse().setBody("""{"connectors":[{"connector":"apps","extension":"apps","subscribable":true,"on":2,"failing":1},
            {"connector":"github","extension":"","on":0,"failing":0},{"connector":"x","subscribable":false,"on":0,"failing":0}]}"""))
        val lines = api.subscriptionSummary()
        assertEquals("/api/v1/subscriptions/summary", server.takeRequest().path)
        assertEquals(2 to 1, lines.subscriptionCounts { it.extension == "apps" })
        assertNull("not read yet: hidden", lines.subscriptionCounts { it.connector == "github" })
        assertNull("nothing to subscribe: hidden", lines.subscriptionCounts { it.connector == "x" })
        assertEquals("tools gone, subscriptions kept", 1 to 0, listOf(SubscriptionSummaryLine("y", subscribable = false, on = 1)).subscriptionCounts { true })
    }

    @Test fun aSubscriptionEventIsRead() {
        val event = ServerEvent.decode("""{"type":"subscription","id":"sub_1","connector":"apps","revision":3,"state":"ok","removed":false}""")
        assertEquals(SubscriptionEvent("sub_1", "apps", 3, "ok", false), event)
    }
}
