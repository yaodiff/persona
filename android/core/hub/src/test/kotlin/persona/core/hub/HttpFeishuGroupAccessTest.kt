package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.mockwebserver.MockResponse
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.FeishuGroupAccessMode
import persona.core.contracts.api.FeishuGroupAccessPolicy
import persona.core.contracts.api.FeishuGroupChat
import persona.core.contracts.api.FeishuGroupUser

/** Settings › Lark bot › group access over HTTP (web `lib/feishu-group-access.ts`): the view, an older hub, and the whole policy written back. */
class HttpFeishuGroupAccessTest {
    private fun <T> withHub(block: suspend (MockWebServer, HttpFeishuGroupAccess) -> T): T = runBlocking {
        MockWebServer().use { server ->
            val http = OkHttpClient.Builder().retryOnConnectionFailure(false).build()
            block(server, HttpFeishuGroupAccess { path, method, body ->
                val response = http.newCall(Request.Builder().url(server.url(path)).method(method, body).build()).execute()
                if (!response.isSuccessful) throw response.use { HubFailure.from(it) }
                response
            })
        }
    }

    private val body = """{"revision":3,"paired":true,
        "policy":{"mode":"allowlist",
            "allowed_users":[{"open_id":"ou_example1","name":"Teammate A"}],
            "allowed_chats":[{"chat_id":"oc_example1","name":"Project group"}],
            "blocked_users":[],"blocked_chats":[],
            "guest_tools":["wiki_search"],"per_chat_hourly":20},
        "available_tools":[{"name":"wiki_search","connector":"Wiki"}],
        "recent_requests":[{"open_id":"ou_example2","name":"Teammate B","chat_id":"oc_example1","at":1795000000}],
        "limits":{"list_max":200,"guest_tools_max":8,"per_chat_hourly_min":1,"per_chat_hourly_max":120,"per_chat_hourly_default":20},
        "unknown":[1]}"""

    @Test fun readsThePolicyAndToleratesWhatItDoesNotKnow() = withHub { server, api ->
        server.enqueue(MockResponse().setBody(body))
        val access = api.read()!!
        assertEquals(3L, access.revision)
        assertTrue(access.paired)
        assertEquals(FeishuGroupAccessMode.Allowlist, access.policy.mode)
        assertEquals("ou_example1", access.policy.allowedUsers.single().openId)
        assertEquals("Project group", access.policy.allowedChats.single().name)
        assertTrue(access.policy.blockedUsers.isEmpty())
        assertEquals(listOf("wiki_search"), access.policy.guestTools)
        assertEquals("Wiki", access.availableTools.single().connector)
        assertEquals(1795000000L, access.recentRequests.single().at)
        assertEquals(200, access.limits.listMax)
        val request = server.takeRequest()
        assertEquals("GET", request.method)
        assertEquals("/api/v1/bots/feishu/group-access", request.path)
    }

    @Test fun anOlderHubHidesTheSection() = withHub { server, api ->
        server.enqueue(MockResponse().setResponseCode(404).setBody("""{"error":"not found"}"""))
        assertNull(api.read())
        // A real failure is not hidden.
        server.enqueue(MockResponse().setResponseCode(500).setBody("""{"error":"database down"}"""))
        assertEquals(500, assertThrows(HubFailure::class.java) { runBlocking { api.read() } }.status)
    }

    @Test fun writesTheWholePolicyAtTheRevisionItWasRead() = withHub { server, api ->
        server.enqueue(MockResponse().setBody(body.replace("\"revision\":3", "\"revision\":4")))
        val next = api.update(3, FeishuGroupAccessPolicy(
            mode = FeishuGroupAccessMode.EveryoneInAllowedChats,
            allowedUsers = listOf(FeishuGroupUser("ou_example1", "Teammate A")),
            allowedChats = listOf(FeishuGroupChat("oc_example1")),
            guestTools = listOf("wiki_search"),
            perChatHourly = 40,
        ))
        assertEquals(4L, next.revision)
        val request = server.takeRequest()
        assertEquals("PUT", request.method)
        assertEquals("/api/v1/bots/feishu/group-access", request.path)
        assertEquals("""{"revision":3,"policy":{"mode":"everyone_in_allowed_chats","allowed_users":[{"open_id":"ou_example1","name":"Teammate A"}],"allowed_chats":[{"chat_id":"oc_example1"}],"blocked_users":[],"blocked_chats":[],"guest_tools":["wiki_search"],"per_chat_hourly":40}}""",
            request.body.readUtf8())
    }

    @Test fun aStaleRevisionComesBackAsAConflict() = withHub { server, api ->
        server.enqueue(MockResponse().setResponseCode(409).setBody("""{"code":"group_access_conflict","error":"changed elsewhere"}"""))
        val failure = assertThrows(HubFailure::class.java) { runBlocking { api.update(2, FeishuGroupAccessPolicy()) } }
        assertEquals(409, failure.status)
    }
}
