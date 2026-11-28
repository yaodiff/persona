package persona.core.hub

import kotlinx.coroutines.test.runTest
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.Protocol
import okhttp3.Request
import okhttp3.Response
import okhttp3.ResponseBody.Companion.toResponseBody
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ItemsQuery

class ToolViewCapabilityTest {
    @Test fun transcriptRequestsAdvertiseToolViews() = runTest {
        val capabilities = mutableListOf<String?>()
        val transport = HubChatTransport(send = { path, _, _, headers ->
            capabilities += headers["X-Persona-Features"]
            val item = """{"id":"result","kind":"tool_result","tool_views":[{"title":"Backup route"}]}"""
            val json = if (path.endsWith("/full")) item else """{"items":[$item]}"""
            Response.Builder().request(Request.Builder().url("https://example.com$path").build())
                .protocol(Protocol.HTTP_1_1).code(200).message("OK")
                .body(json.toResponseBody("application/json".toMediaType())).build()
        })
        val key = SessionKey("demo", "session")
        assertEquals("Backup route", transport.items(key, ItemsQuery()).items.single().toolViews.single().title)
        assertEquals("Backup route", transport.fullItem(key, "result").toolViews.single().title)
        assertEquals(listOf("tool_views", "tool_views"), capabilities)
    }
}
