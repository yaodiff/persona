package persona.core.hub

import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.jsonObject
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.InboxParentRef
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer

class InboxLineageTransportTest {
    @Test fun treeEncodesAnItemAsOneSegmentAndASessionAsItsTwoParts() = runBlocking {
        MockWebServer().use { server ->
            val transport = HttpInboxAssistantTransport("scope", server.url("/").toString(), "test-token", OkHttpClient())
            val response = """{"node":{"kind":"inbox","id":"item"},"ancestors":[],"children":[]}"""
            server.enqueue(MockResponse().setBody(response))
            assertEquals(JsonCodec.parseToJsonElement(response).jsonObject, transport.tree("inbox", "item/with#separator"))
            val item = server.takeRequest()
            assertEquals("GET", item.method)
            assertEquals("/api/v1/inbox/tree/inbox/item%2Fwith%23separator", item.path)
            assertEquals("Bearer test-token", item.getHeader("Authorization"))

            server.enqueue(MockResponse().setBody(response))
            transport.tree("session", "machine 1/session/1")
            val session = server.takeRequest()
            assertEquals("GET", session.method)
            assertEquals("/api/v1/inbox/tree/session/machine%201/session/1", session.path)
        }
    }

    @Test fun clearingAParentSendsPutWithALiteralJsonNull() = runBlocking {
        MockWebServer().use { server ->
            val transport = HttpInboxAssistantTransport("scope", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"id":"child/with#separator"}"""))
            transport.setParent("child/with#separator", null)
            val request = server.takeRequest()
            assertEquals("PUT", request.method)
            assertEquals("/api/v1/inbox/child%2Fwith%23separator/parent", request.path)
            assertEquals("null", request.body.readUtf8())
        }
    }

    @Test fun settingAParentSendsTheReferenceItselfWithoutAWrapper() = runBlocking {
        MockWebServer().use { server ->
            val transport = HttpInboxAssistantTransport("scope", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"id":"child"}"""))
            transport.setParent("child", InboxParentRef("inbox", "parent/one"))
            val request = server.takeRequest()
            assertEquals("PUT", request.method)
            assertEquals("/api/v1/inbox/child/parent", request.path)
            assertEquals("""{"kind":"inbox","id":"parent/one"}""", request.body.readUtf8())
        }
    }
}
