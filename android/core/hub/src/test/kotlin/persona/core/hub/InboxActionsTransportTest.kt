package persona.core.hub

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.runBlocking
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import persona.core.contracts.api.InboxItemContext
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import org.junit.Assert.*
import org.junit.Test

class InboxActionsTransportTest {
    @Test fun draftPostsTheInstructionAndReturnsTheText() = runBlocking {
        MockWebServer().use { server ->
            var current = true
            val transport = HttpInboxActionsTransport(OkHttpClient(), { server.url("/").toString().toHttpUrl() }, "fixture-token", { current })
            server.enqueue(MockResponse().setBody("""{"text":"  能赶上，周五前给你。\n","future":1}"""))
            assertEquals("能赶上，周五前给你。", (transport.draft("lark:f1/10#x", "语气轻松一点\n在这版上改：能赶上")).zhAny())
            val request = server.takeRequest()
            assertEquals("POST", (request.method).zhAny())
            assertEquals("/api/v1/inbox/lark:f1%2F10%23x/draft", (request.path).zhAny())
            assertEquals("Bearer fixture-token", (request.getHeader("Authorization")).zhAny())
            assertEquals("""{"instruction":"语气轻松一点\n在这版上改：能赶上"}""", (request.body.readUtf8()).zhAny())

            // An empty instruction still goes on the wire: the hub drafts from the item alone.
            server.enqueue(MockResponse().setBody("""{"text":"好的"}"""))
            transport.draft("mail:a1:t:7", "")
            assertEquals("""{"instruction":""}""", (server.takeRequest().body.readUtf8()).zhAny())

            server.enqueue(MockResponse().setResponseCode(502).setBody("""{"error":"model unavailable"}"""))
            val failure = runCatching { transport.draft("x", "") }.exceptionOrNull() as HubFailure
            assertEquals("model unavailable", (failure.message).zhAny())
            server.takeRequest()

            server.enqueue(MockResponse().setBody("""{"text":"   "}"""))
            assertEquals(HubError.Protocol, (runCatching { transport.draft("x", "") }.exceptionOrNull() as HubFailure).kind)
            server.takeRequest()

            current = false
            assertTrue(runCatching { transport.draft("x", "") }.exceptionOrNull() is CancellationException)
            assertEquals("Unexpected request: ${if (server.requestCount > 4) server.takeRequest(200, java.util.concurrent.TimeUnit.MILLISECONDS) else null}",
                4, server.requestCount)
        }
    }

    /** A draft or a context asked for another manager's item names it on the request (RequestManager, docs/assistant/managers.md). */
    @Test fun anItemsManagerIsNamedOnTheRequest() = runBlocking {
        MockWebServer().use { server ->
            val transport = HttpInboxActionsTransport(OkHttpClient(), { server.url("/").toString().toHttpUrl() }, "fixture-token", { true })
            server.enqueue(MockResponse().setBody("""{"text":"好的"}"""))
            kotlinx.coroutines.withContext(persona.core.contracts.api.RequestManager("m-3")) { transport.draft("x", "") }
            assertEquals("/api/v1/inbox/x/draft?manager=m-3", server.takeRequest().path)
            server.enqueue(MockResponse().setBody("""{"context":"c"}"""))
            assertEquals("c", transport.context("x").text)
            assertEquals("/api/v1/inbox/x", server.takeRequest().path)
        }
    }

    /** The optional mail_body_truncated beside context is read as the raw tri-state (#15995): true, false, absent. */
    @Test fun contextReadsMailBodyTruncatedTrueFalseAndAbsent() = runBlocking {
        MockWebServer().use { server ->
            val transport = HttpInboxActionsTransport(OkHttpClient(), { server.url("/").toString().toHttpUrl() }, "fixture-token", { true })
            fun answer(body: String) = server.enqueue(MockResponse().setBody(body))

            answer("""{"context":"c","mail_body_truncated":true}""")
            assertEquals(InboxItemContext("c", true), transport.context("mail:1"))

            answer("""{"context":"c","mail_body_truncated":false}""")
            assertEquals(false, transport.context("mail:1").mailBodyTruncated)

            // Absent, null and non-boolean JSON values are all "not known" (null), never false / whole.
            for (body in listOf(
                """{"context":"c"}""",
                """{"context":"c","mail_body_truncated":null}""",
                """{"context":"c","mail_body_truncated":"false"}""",
                """{"context":"c","mail_body_truncated":0}""",
            )) {
                answer(body)
                assertEquals(body, null, transport.context("mail:1").mailBodyTruncated)
            }

            // A manager envelope nests the item under "item"; the flag is read there, with the envelope as fallback.
            answer("""{"item":{"context":"c","mail_body_truncated":true}}""")
            assertEquals(true, transport.context("mail:1").mailBodyTruncated)
            answer("""{"item":{"context":"c"},"mail_body_truncated":false}""")
            assertEquals(false, transport.context("mail:1").mailBodyTruncated)
        }
    }
}
