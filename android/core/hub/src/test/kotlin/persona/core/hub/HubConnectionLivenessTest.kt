package persona.core.hub

import persona.core.contracts.Endpoint
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.ConnectionState
import java.net.InetAddress
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.delay
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.TestCoroutineScheduler
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.*
import okhttp3.*
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import okio.ByteString
import org.junit.Assert.*
import org.junit.Test

@OptIn(ExperimentalCoroutinesApi::class)
class HubConnectionLivenessTest {
    @Test fun foregroundCountsAnyDataAndReconnectsAtInactivityDeadline() = runBlocking {
        Fixture().use { fixture ->
            val socket = fixture.start()
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
            fixture.advance(44_999)
            assertFalse(socket.cancelled)
            socket.receive("""{"type":"future_event"}""")
            fixture.scheduler.runCurrent()
            fixture.advance(44_999)
            assertFalse(socket.cancelled)
            fixture.advance(1)
            assertTrue(socket.cancelled)
            assertEquals(ConnectionState.Connecting, fixture.connection.state.value)
            assertNull(fixture.connection.route.value)
            assertEquals("Timeout", (fixture.connection.diagnostics.value.lastDisconnectReason).zhAny())
            fixture.advance(1000)
            fixture.awaitSocket(2)
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
            assertEquals("Timeout", (fixture.connection.diagnostics.value.lastDisconnectReason).zhAny())
        }
    }

    /** client_hello carries the shown language and is sent again when it changes (docs/protocol/i18n.md "Protocol"). */
    @Test fun helloCarriesTheLanguageAndRepeatsWhenItChanges() = runBlocking {
        try {
            persona.core.contracts.ClientLanguage.current = persona.core.contracts.AppLanguage.Ja
            Fixture(appPing = true).use { fixture ->
                val socket = fixture.start()
                fun hellos() = socket.sent.filter { it["type"]?.jsonPrimitive?.content == "client_hello" }
                assertEquals("ja", (hellos().last()["locale"]?.jsonPrimitive?.content).zhAny())
                val before = hellos().size
                persona.core.contracts.ClientLanguage.current = persona.core.contracts.AppLanguage.ZhHans
                fixture.scheduler.runCurrent()
                assertEquals(before + 1, hellos().size)
                assertEquals("zh-Hans", (hellos().last()["locale"]?.jsonPrimitive?.content).zhAny())
                assertEquals("test-device", (hellos().last()["device_id"]?.jsonPrimitive?.content).zhAny())
            }
        } finally { persona.core.contracts.ClientLanguage.current = persona.core.contracts.AppLanguage.En }
    }

    @Test fun wakeCutsTheRetryBackoffShort() = runBlocking {
        Fixture().use { fixture ->
            val socket = fixture.start()
            fixture.advance(45_000)
            assertTrue(socket.cancelled)
            assertEquals(ConnectionState.Connecting, fixture.connection.state.value)
            // Virtual time stands still: only the wake (the capsule unlocked or expanded) can start the next socket.
            fixture.connection.onWake()
            fixture.awaitSocket(2)
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
        }
    }

    @Test fun backgroundWakePingsImmediatelyAndKeepsAnAnsweredSocket() = runBlocking {
        Fixture(appPing = true).use { fixture ->
            val socket = fixture.start()
            fixture.connection.setForeground(false)
            fixture.scheduler.runCurrent()
            socket.receive("""{"type":"client_hello_ack","heartbeat":{"interval":240,"pong_timeout":15}}""")
            fixture.scheduler.runCurrent()
            fixture.advance(60_000)
            fixture.connection.onWake()
            fixture.scheduler.runCurrent()
            assertFalse(socket.cancelled)
            val ping = socket.sent.last()
            assertEquals("ping", (ping["type"]?.jsonPrimitive?.content).zhAny())
            socket.receive(buildJsonObject { put("type", "pong"); put("id", ping.getValue("id")) }.toString())
            fixture.advance(5_000)
            assertFalse(socket.cancelled)
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
        }
    }

    @Test fun backgroundWakeReconnectsAfterTheExistingPongTimeout() = runBlocking {
        Fixture(appPing = true).use { fixture ->
            val socket = fixture.start()
            fixture.connection.setForeground(false)
            fixture.scheduler.runCurrent()
            socket.receive("""{"type":"client_hello_ack","heartbeat":{"interval":240,"pong_timeout":15}}""")
            fixture.scheduler.runCurrent()
            fixture.advance(60_000)
            fixture.connection.onWake()
            fixture.scheduler.runCurrent()
            assertEquals("ping", (socket.sent.last()["type"]?.jsonPrimitive?.content).zhAny())
            fixture.advance(14_999)
            assertFalse(socket.cancelled)
            fixture.advance(1)
            assertTrue(socket.cancelled)
            assertEquals(ConnectionState.Connecting, fixture.connection.state.value)
        }
    }

    @Test fun backgroundUsesAcknowledgedHeartbeatAndRestartsNegotiationOnForeground() = runBlocking {
        Fixture(appPing = true).use { fixture ->
            val socket = fixture.start()
            val hello = socket.sent.first { it["type"]?.jsonPrimitive?.content == "client_hello" }
            assertEquals("test-device", (hello["device_id"]?.jsonPrimitive?.content).zhAny())
            assertEquals(JsonPrimitive(7), hello["build"])
            assertTrue("approval_mirrors" in hello["features"]!!.jsonArray.map { it.jsonPrimitive.content })
            assertEquals("Android", (hello["os"]?.jsonObject?.get("name")?.jsonPrimitive?.content).zhAny())
            fixture.connection.setForeground(false)
            fixture.scheduler.runCurrent()
            assertEquals(240, socket.sent.last { it["type"]?.jsonPrimitive?.content == "client_hello" }
                ["heartbeat"]!!.jsonObject["interval"]!!.jsonPrimitive.int)
            socket.receive("""{"type":"client_hello_ack","heartbeat":{"interval":80,"pong_timeout":12}}""")
            fixture.scheduler.runCurrent()
            val diagnostics = fixture.connection.diagnostics.value
            assertEquals(80, diagnostics.backgroundHeartbeatSeconds)
            assertEquals(12, diagnostics.pongTimeoutSeconds)
            assertNotNull(diagnostics.lastReceivedAt)
            assertEquals(1, diagnostics.candidates.single().sampleCount)
            assertNotNull(diagnostics.candidates.single().probeDurationMillis)
            assertTrue("fingerprint" in diagnostics.unmeasuredFields)
            assertEquals("未实测", (diagnostics.unmeasuredFields["fingerprint"]).zhAny())
            assertTrue(diagnostics.candidates.single().measurementNote.zh().contains("样本数 1"))

            fixture.advance(80_000)
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
            assertEquals("ping", (socket.sent.last()["type"]?.jsonPrimitive?.content).zhAny())
            socket.receive(buildJsonObject { put("type", "pong"); put("id", socket.sent.last().getValue("id")) }.toString())
            fixture.scheduler.runCurrent()
            fixture.advance(80_000)
            assertFalse(socket.cancelled)
            fixture.advance(11_999)
            assertFalse(socket.cancelled)
            fixture.advance(1)
            assertTrue(socket.cancelled)
            assertEquals(ConnectionState.Connecting, fixture.connection.state.value)

            fixture.advance(1000)
            val replacement = fixture.awaitSocket(2)
            replacement.receive("""{"type":"client_hello_ack","heartbeat":{"interval":90,"pong_timeout":10}}""")
            fixture.scheduler.runCurrent()
            fixture.connection.setForeground(true)
            fixture.scheduler.runCurrent()
            assertEquals(20, replacement.sent.last { it["type"]?.jsonPrimitive?.content == "client_hello" }
                ["heartbeat"]!!.jsonObject["interval"]!!.jsonPrimitive.int)
            replacement.receive("""{"type":"client_hello_ack","heartbeat":{"interval":18,"pong_timeout":8}}""")
            fixture.scheduler.runCurrent()
            assertEquals(18, fixture.connection.diagnostics.value.foregroundHeartbeatSeconds)
            assertEquals(18, fixture.connection.heartbeat.value.interval)
        }
    }

    /** #4695: silence that was normal in the background is not a failure on return; the ping decides. */
    @Test fun foregroundAfterQuietBackgroundKeepsTheSocketWhenThePingIsAnswered() = runBlocking {
        Fixture(appPing = true).use { fixture ->
            val socket = fixture.start()
            fixture.connection.setForeground(false)
            fixture.scheduler.runCurrent()
            socket.receive("""{"type":"client_hello_ack","heartbeat":{"interval":240,"pong_timeout":15}}""")
            fixture.scheduler.runCurrent()
            fixture.advance(60_000)
            assertFalse(socket.cancelled)
            fixture.connection.setForeground(true)
            fixture.scheduler.runCurrent()
            assertFalse(socket.cancelled)
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
            val ping = socket.sent.last()
            assertEquals("ping", (ping["type"]?.jsonPrimitive?.content).zhAny())
            socket.receive(buildJsonObject { put("type", "pong"); put("id", ping.getValue("id")) }.toString())
            fixture.advance(5_000)
            assertFalse(socket.cancelled)
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
        }
    }

    @Test fun foregroundAfterQuietBackgroundReconnectsWhenThePingGoesUnanswered() = runBlocking {
        Fixture(appPing = true).use { fixture ->
            val socket = fixture.start()
            fixture.connection.setForeground(false)
            fixture.scheduler.runCurrent()
            socket.receive("""{"type":"client_hello_ack","heartbeat":{"interval":240,"pong_timeout":15}}""")
            fixture.scheduler.runCurrent()
            fixture.advance(60_000)
            fixture.connection.setForeground(true)
            fixture.scheduler.runCurrent()
            assertFalse(socket.cancelled)
            fixture.advance(4_999)
            assertFalse(socket.cancelled)
            fixture.advance(1)
            assertTrue(socket.cancelled)
        }
    }

    @Test fun networkSwitchRenegotiatesConfiguredIntervalsAndJitterNeverRaisesThem() = runBlocking {
        val policy = HeartbeatPolicy(backgroundWifiSeconds = 180, backgroundCellularSeconds = 120,
            backgroundJitterFraction = 0.25)
        assertEquals(180, policy.interval(false, HubNetworkTransport.Wifi) { 0.0 })
        assertEquals(135, policy.interval(false, HubNetworkTransport.Wifi) { 1.0 })
        Fixture(appPing = true, policy = policy.copy(backgroundJitterFraction = 0.0)).use { fixture ->
            val socket = fixture.start()
            fixture.connection.setForeground(false)
            fixture.scheduler.runCurrent()
            fun interval(socket: FakeSocket) = socket.sent.last { it["type"]?.jsonPrimitive?.content == "client_hello" }
                .getValue("heartbeat").jsonObject.getValue("interval").jsonPrimitive.int
            assertEquals(180, interval(socket))
            fixture.connection.updateNetwork(HubNetwork("cellular", transport = HubNetworkTransport.Cellular))
            fixture.scheduler.runCurrent()
            val replacement = fixture.awaitSocket(2)
            assertTrue(socket.cancelled)
            assertEquals(120, interval(replacement))
            fixture.connection.setForeground(true)
            fixture.scheduler.runCurrent()
            assertEquals(20, interval(replacement))
        }
    }

    /** A per-request 403 (an expired install preview) fails that request only; the pairing stays (#4692). */
    @Test fun aRefusedRequestDoesNotRevokeThePairing() = runBlocking {
        Fixture().use { fixture ->
            fixture.start()
            val failure = runCatching {
                fixture.connection.request("/api/v1/extensions/-/install", "POST", "{}".toRequestBody())
            }.exceptionOrNull() as HubFailure
            assertEquals("this_machine_only", failure.code)
            fixture.scheduler.runCurrent()
            assertNotEquals(ConnectionState.Revoked, fixture.connection.state.value)
        }
    }

    @Test fun reconnectAndCloseReleaseLockBeforeCancellingWebSocket() = runBlocking {
        Fixture().use { fixture ->
            val first = fixture.start()
            first.onCancel = {
                // Acquiring connection.lock from another thread inside cancel() deadlocks if lock is still held.
                val latch = java.util.concurrent.CountDownLatch(1)
                val probe = Thread {
                    try { fixture.connection.updateTunnels(emptyList()) } finally { latch.countDown() }
                }
                probe.start()
                assertTrue("reconnect() held lock across socket.cancel()", latch.await(2, java.util.concurrent.TimeUnit.SECONDS))
            }
            fixture.connection.reconnect()
            assertTrue(first.cancelled)

            val second = fixture.awaitSocket(2)
            second.onCancel = {
                val latch = java.util.concurrent.CountDownLatch(1)
                val probe = Thread {
                    try { fixture.connection.updateTunnels(emptyList()) } finally { latch.countDown() }
                }
                probe.start()
                assertTrue("close() held lock across socket.cancel()", latch.await(2, java.util.concurrent.TimeUnit.SECONDS))
            }
            fixture.connection.close()
            assertTrue(second.cancelled)
        }
    }

    private class Fixture(private val appPing: Boolean = false, policy: HeartbeatPolicy = HeartbeatPolicy()) : AutoCloseable {
        val scheduler = TestCoroutineScheduler()
        private val sockets = mutableListOf<FakeSocket>()
        private val server = MockWebServer().apply {
            dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest) =
                    if (request.path.orEmpty().startsWith("/api/v1/extensions/")) MockResponse().setResponseCode(403)
                        .setBody("""{"error":"preview expired","code":"this_machine_only"}""")
                    else MockResponse().setBody("""{"ok":true,"hub_id":"test-hub"}""")
            }
            start(InetAddress.getLoopbackAddress(), 0)
        }
        val connection = OkHttpHubConnection(
            HubConnectionConfig(
                "test-hub", null, "test-token", listOf(Endpoint(server.url("/").toString(), "lan")), "test",
                deviceId = "test-device", build = JsonPrimitive(7), heartbeatPolicy = policy,
                os = buildJsonObject { put("name", "Android"); put("version", "test"); put("sdk", 36) },
            ),
            HubNetwork("test-network", transport = HubNetworkTransport.Wifi), dispatcher = StandardTestDispatcher(scheduler),
            clockMillis = { scheduler.currentTime },
            webSocketFactory = object : WebSocket.Factory {
                override fun newWebSocket(request: Request, listener: WebSocketListener): WebSocket {
                    val socket = FakeSocket(request, listener)
                    sockets += socket
                    listener.onOpen(socket, Response.Builder().request(request).protocol(Protocol.HTTP_1_1)
                        .code(101).message("Switching Protocols").build())
                    val features = if (appPing) "[\"client_hello\",\"heartbeat\",\"app_ping\"]" else "[]"
                    socket.receive("""{"type":"hello","hub_id":"test-hub","features":$features}""")
                    return socket
                }
            },
        )

        suspend fun start() = awaitSocket(1)

        suspend fun awaitSocket(count: Int): FakeSocket {
            withTimeout(5000) {
                while (sockets.size < count) {
                    scheduler.runCurrent()
                    delay(10)
                }
            }
            scheduler.runCurrent()
            return sockets.last()
        }

        fun advance(millis: Long) { scheduler.advanceTimeBy(millis); scheduler.runCurrent() }
        override fun close() { connection.close(); scheduler.runCurrent(); server.close() }
    }

    private class FakeSocket(private val request: Request, private val listener: WebSocketListener) : WebSocket {
        val sent = mutableListOf<JsonObject>()
        var cancelled = false
        var onCancel: (() -> Unit)? = null
        fun receive(text: String) = listener.onMessage(this, text)
        override fun request() = request
        override fun queueSize() = 0L
        override fun send(text: String): Boolean { sent += JsonCodec.parseToJsonElement(text).jsonObject; return true }
        override fun send(bytes: ByteString) = true
        override fun close(code: Int, reason: String?) = true
        override fun cancel() { cancelled = true; onCancel?.invoke() }
    }
}
