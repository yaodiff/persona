package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.mockwebserver.MockResponse
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.Session
import persona.core.contracts.api.SessionListFilter
import persona.core.contracts.api.SessionListExpired
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer

class HttpHierarchyTest {
    private val page = """{"sessions":[],"has_more":false,"total":0,"revision":"r"}"""
    private val counts = """{"total":0,"machine":{},"project":{},"group":{},"state":{},"revision":"r"}"""

    @Test fun hierarchyIsIndependentlyGatedAndRepeatedWithEveryRootPageCountAndDelta() = runBlocking {
        MockWebServer().use { server ->
            val client = OkHttpClient()
            var hierarchy = false
            val api = HubChatTransport({ path, method, body, _ ->
                client.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute()
            }, hierarchy = { hierarchy })
            server.enqueue(MockResponse().setBody(page))
            api.sessionPage()
            assertNull(server.takeRequest().requestUrl!!.queryParameter("roots"))
            hierarchy = true
            for (count in listOf(false, true)) {
                server.enqueue(MockResponse().setBody(if (count) counts else page))
                if (count) api.sessionCounts(SessionListFilter(machineId = "m"))
                else api.sessionPage(SessionListFilter(machineId = "m"), "opaque/+", "since/+")
                val url = server.takeRequest().requestUrl!!
                assertEquals("1", url.queryParameter("roots"))
                assertEquals("m", url.queryParameter("machine_id"))
                if (!count) {
                    assertEquals("opaque/+", url.queryParameter("cursor"))
                    assertEquals("since/+", url.queryParameter("since"))
                }
            }
            server.enqueue(MockResponse().setBody(page))
            api.sessionPage(SessionListFilter(machineId = "m", parentSessionId = "parent/+"))
            val childUrl = server.takeRequest().requestUrl!!
            assertNull(childUrl.queryParameter("roots"))
            assertEquals("parent/+", childUrl.queryParameter("parent_session_id"))
            server.enqueue(MockResponse().setBody(page))
            api.sessionPage(SessionListFilter(requiresCompleteMembership = true))
            assertNull(server.takeRequest().requestUrl!!.queryParameter("roots"))
        }
    }

    @Test fun parentChatReadsEveryChildPageWithItsOwnStateAndTheSameParentFilter() = runBlocking {
        MockWebServer().use { server ->
            val client = OkHttpClient()
            val api = HttpChatActions { path, method, body ->
                client.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute()
            }
            server.enqueue(MockResponse().setBody("""{"sessions":[{"session_id":"child","folded_under":"p/+","state":"needs_you"}],"has_more":true,"next_cursor":"next/+","total":2,"revision":"r"}"""))
            server.enqueue(MockResponse().setBody("""{"sessions":[{"session_id":"grandchild","folded_under":"p/+","state":"working"}],"has_more":false,"total":2,"revision":"r"}"""))
            val rows = api.childRows(Session("m", "p/+"))
            assertEquals(listOf("child", "grandchild"), rows.map { it.sessionId })
            assertEquals(listOf("needs_you", "working"), rows.map { it.state })
            assertTrue(rows.all { it.machineId == "m" })
            repeat(2) { i ->
                val url = server.takeRequest().requestUrl!!
                assertEquals("p/+", url.queryParameter("parent_session_id"))
                assertEquals("m", url.queryParameter("machine_id"))
                assertNull(url.queryParameter("roots"))
                assertEquals(if (i == 0) null else "next/+", url.queryParameter("cursor"))
            }
        }
    }

    @Test fun childDeltasRepeatTheirCheckpointAndAggregateEveryFrozenPage() = runBlocking {
        MockWebServer().use { server ->
            val client = OkHttpClient()
            val api = HttpChatActions { path, method, body ->
                client.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute()
            }
            server.enqueue(MockResponse().setBody("""{"sessions":[{"session_id":"child","folded_under":"p","state":"idle"}],"remove":["[\"m\",\"old/child\"]"],"has_more":true,"next_cursor":"next/+","total":2,"revision":"r2"}"""))
            server.enqueue(MockResponse().setBody("""{"sessions":[{"session_id":"new","folded_under":"p","state":"working"}],"has_more":false,"total":2,"revision":"r2"}"""))
            val page = api.childPage(Session("m", "p"), "r1/+")
            assertEquals(listOf("child", "new"), page.sessions.map { it.sessionId })
            assertEquals(listOf("""["m","old/child"]"""), page.remove)
            assertEquals("r2", page.revision)
            assertFalse(page.hasMore)
            repeat(2) { index ->
                val url = server.takeRequest().requestUrl!!
                assertEquals("r1/+", url.queryParameter("since"))
                assertEquals("p", url.queryParameter("parent_session_id"))
                assertEquals("m", url.queryParameter("machine_id"))
                assertEquals("1", url.queryParameter("summary"))
                assertEquals(if (index == 0) null else "next/+", url.queryParameter("cursor"))
            }
        }
    }

    @Test fun anExpiredChildCheckpointRebaselinesAndMixedRevisionsCannotCommit() = runBlocking {
        MockWebServer().use { server ->
            val client = OkHttpClient()
            val api = HttpChatActions { path, method, body ->
                client.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute()
            }
            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"Expired","code":"revision_expired"}"""))
            try { api.childPage(Session("m", "p"), "old"); fail("Expired checkpoints need a fresh baseline") }
            catch (_: SessionListExpired) { }
            server.takeRequest()
            server.enqueue(MockResponse().setBody("""{"sessions":[],"has_more":true,"next_cursor":"next","revision":"r1"}"""))
            server.enqueue(MockResponse().setBody("""{"sessions":[],"revision":"r2"}"""))
            try { api.childPage(Session("m", "p")); fail("Frozen pages must share a revision") }
            catch (failure: HubFailure) { assertEquals(HubError.Protocol, failure.kind) }
        }
    }
}
