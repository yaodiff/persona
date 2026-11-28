package persona.core.hub

import persona.core.contracts.api.DeviceCredentialsUnavailable
import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import org.junit.Assert.*
import org.junit.Test

class DeviceCredentialsTest {
    @Test fun revokedCredentialsAreListedApartAndUnsupportedHubsSaySo() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpDeviceSettings(server.url("/").toString(), "example-token", "phone/id", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"current":"credential","online":["credential","watch"],"tokens":[
                {"id":"credential","name":"示例手机","kind":"phone","role":"full","created_at":"2026-01-01T00:00:00Z"},
                {"id":"old","name":"旧手机","kind":"phone","role":"full","revoked_at":"2026-01-02T08:00:00+08:00"},
                {"id":"watch","name":"示例手表","kind":"watch","role":"answer","revoked_at":""}]}"""))
            val devices = api.devices()
            assertEquals(listOf("credential", "watch"), (devices.devices.map { it.id }).zhAny())
            assertEquals(setOf("credential", "watch"), devices.onlineIds)
            assertEquals("2026-01-01T00:00:00Z", (devices.devices.first().createdAt).zhAny())
            assertEquals(listOf("old"), (devices.revoked.map { it.id }).zhAny())
            assertEquals("2026-01-02T08:00:00+08:00", (devices.revoked.single().revokedAt).zhAny())
            server.takeRequest()
            server.enqueue(MockResponse().setResponseCode(404).setBody("404 page not found"))
            assertEquals("这个 hub 版本还不支持设备凭据。", ((runCatching { api.devices() }.exceptionOrNull() as DeviceCredentialsUnavailable).text).zhAny())
            server.enqueue(MockResponse().setResponseCode(503).setBody("""{"error":"no database"}"""))
            assertEquals("hub 没有数据库，不支持设备凭据。", ((runCatching { api.devices() }.exceptionOrNull() as DeviceCredentialsUnavailable).text).zhAny())
            server.enqueue(MockResponse().setResponseCode(500).setBody("""{"error":"boom"}"""))
            assertTrue(runCatching { api.devices() }.exceptionOrNull() is HubFailure)
        }
    }
}
