package persona.core.hub

import persona.core.contracts.Endpoint
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.ConnectionState
import java.io.IOException
import java.net.InetAddress
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.Job
import kotlinx.coroutines.launch
import kotlinx.coroutines.delay
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.TestCoroutineScheduler
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.*
import okhttp3.*
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import okio.ByteString
import org.junit.Assert.*
import org.junit.Test

/** A hub restart and short drops stay invisible; a reconnect resumes the hub's broadcast stream (#1574). */
@OptIn(ExperimentalCoroutinesApi::class)
class HubRestartTest {
    @Test fun theSocketUrlCarriesTheResumePoint() {
        Fixture().use { fixture ->
            val base = "https://hub.example:8443/".toHttpUrl()
            assertEquals("https://hub.example:8443/api/v1/ws?features=sessions_list%2Csessions_hierarchy%2Cextension_ui_stream_v1%2Cpersona_contracts_v1", (fixture.connection.socketUrl(base, null).toString()).zhAny())
            assertEquals("https://hub.example:8443/api/v1/ws?features=sessions_list%2Csessions_hierarchy%2Cextension_ui_stream_v1%2Cpersona_contracts_v1&event_stream=s%201&event_seq=42",
                (fixture.connection.socketUrl(base, "s 1" to 42L).toString()).zhAny())
        }
    }

    @Test fun broadcastsAreCountedAndTheNextSocketResumes() = runBlocking {
        Fixture().use { fixture ->
            fixture.hello = { """{"type":"hello","hub_id":"test-hub","event_stream":"s1","event_seq":5,"resumed":false}""" }
            val first = fixture.start()
            assertEquals("sessions_list,sessions_hierarchy,extension_ui_stream_v1,persona_contracts_v1", first.request.url.queryParameter("features"))
            assertNull(first.request.url.queryParameter("event_stream"))
            assertFalse(fixture.connection.resumed)
            assertEquals("s1" to 5L, (fixture.connection.resumePoint).zhAny())
            first.receive("""{"event_seq":6,"type":"todos"}""")
            first.receive("""{"event_seq":7,"type":"agents"}""")
            first.receive("""{"type":"items","machine_id":"m","session_id":"s","items":[]}""")
            fixture.scheduler.runCurrent()
            assertEquals("s1" to 7L, (fixture.connection.resumePoint).zhAny())

            fixture.hello = { """{"type":"hello","hub_id":"test-hub","event_stream":"s1","event_seq":9,"resumed":true}""" }
            first.fail()
            fixture.advance(1000)
            val second = fixture.awaitSocket(2)
            assertEquals("s1", (second.request.url.queryParameter("event_stream")).zhAny())
            assertEquals("7", (second.request.url.queryParameter("event_seq")).zhAny())
            assertTrue(fixture.connection.resumed)
            // Resumed: the replay, not the hello, moves the point, so a drop mid-replay loses nothing.
            assertEquals("s1" to 7L, (fixture.connection.resumePoint).zhAny())
            second.receive("""{"event_seq":8,"type":"todos"}""")
            second.receive("""{"event_seq":9,"type":"inbox"}""")
            fixture.scheduler.runCurrent()
            assertEquals("s1" to 9L, (fixture.connection.resumePoint).zhAny())

            // A hub that lost the stream starts a new one; the app starts over from its hello.
            fixture.hello = { """{"type":"hello","hub_id":"test-hub","event_stream":"s2","event_seq":3,"resumed":false}""" }
            second.fail()
            fixture.advance(1000)
            val third = fixture.awaitSocket(3)
            assertEquals("s1", (third.request.url.queryParameter("event_stream")).zhAny())
            assertFalse(fixture.connection.resumed)
            assertEquals("s2" to 3L, (fixture.connection.resumePoint).zhAny())
        }
    }

    @Test fun anOlderHubIsNeverAskedToResume() = runBlocking {
        Fixture().use { fixture ->
            val first = fixture.start()
            first.receive("""{"event_seq":6,"type":"todos"}""")
            fixture.scheduler.runCurrent()
            assertNull(fixture.connection.resumePoint)
            first.fail()
            fixture.advance(1000)
            val second = fixture.awaitSocket(2)
            assertNull(second.request.url.queryParameter("event_stream"))
            assertFalse(fixture.connection.resumed)
        }
    }

    @Test fun receiptTrustIsCurrentSocketOwnedAndClearsBeforeAnOldHubsHelloDuringOnlineGrace() = runBlocking {
        Fixture().use { fixture ->
            val modern = """{"type":"hello","hub_id":"test-hub","message_send_state":1,"features":["message_send_state"]}"""
            val frame = """{"type":"message_send_state","machine_id":"m","session_id":"s","client_msg_id":"message","state":"processing","at":"2026-11-12T10:00:00Z","socketGeneration":999}"""
            fixture.hello = { modern }
            val first = fixture.start()
            val firstHello = requireNotNull(fixture.connection.currentHello.value)
            assertEquals(1, firstHello.hello.messageSendState)
            val receipts = mutableListOf<persona.core.contracts.MessageSendStateEvent>()
            val watcher = Job()
            CoroutineScope(StandardTestDispatcher(fixture.scheduler) + watcher).launch {
                fixture.connection.events.collect { if (it is persona.core.contracts.MessageSendStateEvent) receipts += it }
            }
            fixture.scheduler.runCurrent()
            try {
                first.receive(frame)
                fixture.scheduler.runCurrent()
                assertEquals(firstHello.generation, receipts.single().socketGeneration)
                // The replacement sends a forged receipt before any hello, while the UI deliberately stays Online.
                fixture.hello = { frame }
                first.close(1001, "hub restarting")
                assertNull(fixture.connection.currentHello.value)
                assertSame(firstHello.hello, fixture.connection.lastHello)
                fixture.advance(1_000)
                val second = fixture.awaitSocket(2)
                assertEquals(ConnectionState.Online, fixture.connection.state.value)
                assertNull(fixture.connection.currentHello.value)
                assertEquals(1, receipts.size)
                second.receive("""{"type":"hello","hub_id":"test-hub","features":["message_send_state"]}""")
                fixture.scheduler.runCurrent()
                second.receive(frame)
                fixture.scheduler.runCurrent()
                assertNull(fixture.connection.currentHello.value?.hello?.messageSendState)
                assertEquals(1, receipts.size)
                fixture.hello = { modern }
                second.close(1001, "hub restarting")
                fixture.advance(1_000)
                val third = fixture.awaitSocket(3)
                val current = requireNotNull(fixture.connection.currentHello.value)
                assertTrue(current.generation > firstHello.generation)
                third.receive(frame)
                fixture.scheduler.runCurrent()
                assertEquals(current.generation, receipts.last().socketGeneration)
                assertEquals(2, receipts.size)
                second.fail() // A retired socket cannot clear the new one's proof.
                assertSame(current, fixture.connection.currentHello.value)
            } finally { watcher.cancel() }
        }
    }

    @Test fun aQueuedHelloCannotReauthorizeReceiptsAfterItsSocketAlreadyFailed() = runBlocking {
        Fixture().use { fixture ->
            fixture.hello = { """{"type":"hello","hub_id":"test-hub","message_send_state":1}""" }
            fixture.afterOpening = { socket ->
                socket.receive("""{"type":"message_send_state","machine_id":"m","session_id":"s","client_msg_id":"message","state":"processing","at":"2026-11-12T10:00:00Z"}""")
                socket.fail() // Both text frames are queued; onFailure runs before the socket loop consumes either.
            }
            val receipts = mutableListOf<persona.core.contracts.MessageSendStateEvent>()
            val watcher = Job()
            CoroutineScope(StandardTestDispatcher(fixture.scheduler) + watcher).launch(start = kotlinx.coroutines.CoroutineStart.UNDISPATCHED) {
                fixture.connection.events.collect { if (it is persona.core.contracts.MessageSendStateEvent) receipts += it }
            }
            try {
                fixture.start()
                fixture.scheduler.runCurrent()
                assertEquals(1, fixture.connection.lastHello?.messageSendState)
                assertNull(fixture.connection.currentHello.value)
                assertTrue("a queued hello cannot revive a socket's already revoked proof", receipts.isEmpty())
            } finally { watcher.cancel() }
        }
    }

    @Test fun aRestartingHubIsRetriedQuicklyWhileTheStateStaysOnline() = runBlocking {
        Fixture().use { fixture ->
            val first = fixture.start()
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
            fixture.reachable = false
            first.receive("""{"type":"hub_restarting","back_in_ms":3000}""")
            first.close(1001, "hub restarting")
            fixture.scheduler.runCurrent()
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
            fixture.pump(149)
            assertEquals(1, fixture.sockets.size)
            fixture.pump(251)
            fixture.awaitSocket(2)
            // Steady retries, no exponential backoff (which would make at most four more in ten seconds).
            fixture.pump(10_000)
            assertTrue("tries: ${fixture.sockets.size}", fixture.sockets.size >= 8)
            fixture.pump(29_600 - 10_400)
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
            fixture.pump(400)
            assertEquals(ConnectionState.Connecting, fixture.connection.state.value)

            // Back: Online again, and the next drop is an ordinary one again.
            fixture.reachable = true
            fixture.pump(20_000)
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
        }
    }

    @Test fun aSlowEventSubscriberDoesNotStopTheConnectionFromReconnecting() = runBlocking {
        Fixture().use { fixture ->
            val first = fixture.start()
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
            // A screen whose handler for an event waits a long time (a reload over the network, a lock).
            val stuck = kotlinx.coroutines.CompletableDeferred<Unit>()
            val got = mutableListOf<String>()
            CoroutineScope(StandardTestDispatcher(fixture.scheduler) + Job()).launch {
                fixture.connection.events.collect { got += it::class.simpleName.orEmpty(); stuck.await() }
            }
            fixture.scheduler.runCurrent()
            first.receive("""{"event_seq":2,"type":"todos"}""")
            first.receive("""{"event_seq":3,"type":"todos"}""")
            fixture.pump(100)
            assertEquals(1, got.size)
            // The hub restarts: the socket closes and the next one must still be opened.
            first.close(1001, "hub restarting")
            fixture.pump(2_000)
            fixture.awaitSocket(2)
            fixture.pump(10_000)
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
            // Let go, it gets the second todos (already being handed over when the hub restarted: not taken back) and
            // then the new socket's hello.
            stuck.complete(Unit)
            fixture.pump(100)
            assertEquals(listOf("TodosEvent", "TodosEvent", "Hello"), got)
        }
        Unit
    }

    @Test fun aSocketThatStartsOverDropsWhatTheOldOneStillHadQueued() = runBlocking {
        Fixture().use { fixture ->
            val first = fixture.start()
            val stuck = kotlinx.coroutines.CompletableDeferred<Unit>()
            val got = mutableListOf<String>()
            CoroutineScope(StandardTestDispatcher(fixture.scheduler) + Job()).launch {
                fixture.connection.events.collect { got += it::class.simpleName.orEmpty(); stuck.await() }
            }
            fixture.scheduler.runCurrent()
            first.receive("""{"event_seq":2,"type":"todos"}""")
            first.receive("""{"event_seq":3,"type":"inbox"}""")
            first.receive("""{"event_seq":4,"type":"inbox"}""")
            fixture.pump(100)
            // Not resumed (the restarted hub has a new stream): what came before is refetched on Online, so the old
            // socket's queued inbox event is dropped; the one already being handed over is not.
            first.close(1001, "hub restarting")
            fixture.pump(2_000)
            fixture.awaitSocket(2)
            assertFalse(fixture.connection.resumed)
            stuck.complete(Unit)
            fixture.pump(100)
            assertEquals(listOf("TodosEvent", "InboxEvent", "Hello"), got)
        }
        Unit
    }

    @Test fun aResumedSocketKeepsWhatTheOldOneStillHadQueued() = runBlocking {
        Fixture().use { fixture ->
            fixture.hello = { """{"type":"hello","hub_id":"test-hub","event_stream":"s1","event_seq":1,"resumed":false}""" }
            val first = fixture.start()
            val stuck = kotlinx.coroutines.CompletableDeferred<Unit>()
            val got = mutableListOf<String>()
            CoroutineScope(StandardTestDispatcher(fixture.scheduler) + Job()).launch {
                fixture.connection.events.collect { got += it::class.simpleName.orEmpty(); stuck.await() }
            }
            fixture.scheduler.runCurrent()
            first.receive("""{"event_seq":2,"type":"todos"}""")
            first.receive("""{"event_seq":3,"type":"inbox"}""")
            first.receive("""{"event_seq":4,"type":"inbox"}""")
            fixture.pump(100)
            // Resumed: the hub replays only what came after event 4, so the queued inbox events are not sent again and
            // must still be delivered.
            fixture.hello = { """{"type":"hello","hub_id":"test-hub","event_stream":"s1","event_seq":4,"resumed":true}""" }
            first.close(1001, "hub restarting")
            fixture.pump(2_000)
            fixture.awaitSocket(2)
            assertTrue(fixture.connection.resumed)
            stuck.complete(Unit)
            fixture.pump(100)
            assertEquals(listOf("TodosEvent", "InboxEvent", "InboxEvent", "Hello"), got)
        }
        Unit
    }

    @Test fun theNextHubAnsweringWithinTheGraceIsNeverShown() = runBlocking {
        Fixture().use { fixture ->
            fixture.hello = { """{"type":"hello","hub_id":"test-hub","event_stream":"s1","event_seq":1,"resumed":true}""" }
            val first = fixture.start()
            val states = fixture.record()
            first.receive("""{"type":"hub_restarting","back_in_ms":3000}""")
            first.close(1001, "hub restarting")
            fixture.pump(400)
            fixture.awaitSocket(2)
            fixture.pump(4_000)
            assertEquals(listOf(ConnectionState.Online), states)
            assertTrue(fixture.connection.resumed)
        }
    }

    @Test fun anOrdinaryDropShowsAfterEightSeconds() = runBlocking {
        Fixture().use { fixture ->
            val first = fixture.start()
            fixture.reachable = false
            first.fail()
            fixture.scheduler.runCurrent()
            fixture.pump(7_900)
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
            fixture.pump(100)
            assertEquals(ConnectionState.Connecting, fixture.connection.state.value)
        }
    }

    @Test fun theBackoffSaysWhenTheNextTryIsAndAConnectionClearsTheFailure() = runBlocking {
        Fixture().use { fixture ->
            val first = fixture.start()
            val seen = java.util.Collections.synchronizedList(mutableListOf<persona.core.contracts.api.ConnectionDiagnostics>())
            val watcher = CoroutineScope(StandardTestDispatcher(fixture.scheduler)).launch { fixture.connection.diagnostics.collect { seen += it } }
            fixture.reachable = false
            first.fail()
            fixture.pump(3_000)
            // While it waits between attempts it says when the next is, and why the last one failed (in the user's words).
            val waiting = seen.filter { it.nextRetryAt != null }
            assertTrue("no backoff seen: $seen", waiting.isNotEmpty())
            assertTrue(waiting.all { it.lastAttemptFailure == endpointResultLabel(HubError.Transport) })

            fixture.reachable = true
            fixture.connection.reconnect()
            fixture.pump(2_000)
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
            val now = fixture.connection.diagnostics.value
            assertNull(now.nextRetryAt)
            assertNull(now.lastAttemptFailure)
            // Settings keep the raw reason for diagnosis.
            assertNotNull(now.lastDisconnectReason)
            watcher.cancel()
        }
    }

    @Test fun aFreshStartAfterAVisibleLengthDropSurfacesSoScreensRefetch() = runBlocking {
        Fixture().use { fixture ->
            val first = fixture.start()
            val seen = fixture.record()
            fixture.reachable = false
            first.fail()
            fixture.scheduler.runCurrent()
            fixture.pump(2_000)
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
            fixture.reachable = true
            fixture.pump(6_000)
            // Not resumed (an older hub): Updating then Online, as before the grace window, so refetches run.
            assertEquals(listOf(ConnectionState.Online, ConnectionState.Updating, ConnectionState.Online), seen)
            assertEquals(ConnectionState.Online, fixture.connection.state.value)
        }
    }

    @Test fun equalFeatureReconnectRetriesFailedManagerBindingWithoutChangingCapabilityState() = runBlocking {
        Fixture().use { fixture ->
            fixture.hello = { """{"type":"hello","hub_id":"test-hub","client_id":"first","features":["extension_ui_stream_v1"]}""" }
            val first = fixture.start()
            fixture.rosterFailure = true
            val bindings = java.util.Collections.synchronizedList(mutableListOf<persona.core.contracts.api.ExtensionStreamBinding?>())
            val watcher = CoroutineScope(StandardTestDispatcher(fixture.scheduler)).launch {
                HttpExtensionManagement(fixture.connection, kotlinx.coroutines.flow.flowOf(true)).nativeStream.collect { bindings += it }
            }
            withTimeout(5000) { while (bindings.isEmpty()) fixture.pump(50) }
            assertNull(bindings.single())
            val features = fixture.connection.serverFeatures.value
            fixture.rosterFailure = false
            first.fail(); fixture.advance(1000)
            fixture.hello = { """{"type":"hello","hub_id":"test-hub","client_id":"second","features":["extension_ui_stream_v1"]}""" }
            fixture.awaitSocket(2)
            withTimeout(5000) { while (bindings.none { it != null }) fixture.pump(50) }
            assertEquals(features, fixture.connection.serverFeatures.value)
            assertEquals("manager-a", bindings.last()!!.manager)
            watcher.cancel()
        }
    }

    private class Fixture : AutoCloseable {
        val scheduler = TestCoroutineScheduler()
        val sockets = mutableListOf<FakeSocket>()
        @Volatile var hello: () -> String = { """{"type":"hello","hub_id":"test-hub","features":[]}""" }
        @Volatile var afterOpening: (FakeSocket) -> Unit = {}
        /** False: each socket fails as soon as it is opened (a hub not yet answering). */
        @Volatile var reachable = true
        @Volatile var rosterFailure = false
        private val server = MockWebServer().apply {
            dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest) = if (request.path == "/api/v1/managers") {
                    if (rosterFailure) MockResponse().setResponseCode(503).setBody("""{"error":"Temporary failure"}""")
                    else MockResponse().setBody("""{"managers":[],"default":"manager-a"}""")
                } else MockResponse().setBody("""{"ok":true,"hub_id":"test-hub"}""")
            }
            start(InetAddress.getLoopbackAddress(), 0)
        }
        val connection = OkHttpHubConnection(
            HubConnectionConfig("test-hub", null, "test-token", listOf(Endpoint(server.url("/").toString(), "lan")), "test"),
            HubNetwork("test-network", transport = HubNetworkTransport.Wifi), dispatcher = StandardTestDispatcher(scheduler),
            clockMillis = { scheduler.currentTime },
            webSocketFactory = object : WebSocket.Factory {
                override fun newWebSocket(request: Request, listener: WebSocketListener): WebSocket {
                    val socket = FakeSocket(request, listener)
                    synchronized(sockets) { sockets += socket }
                    if (!reachable) {
                        socket.fail()
                        return socket
                    }
                    listener.onOpen(socket, Response.Builder().request(request).protocol(Protocol.HTTP_1_1)
                        .code(101).message("Switching Protocols").build())
                    socket.receive(hello())
                    afterOpening(socket)
                    return socket
                }
            },
        )

        suspend fun start() = awaitSocket(1)

        /** Every state published from now on, in order. */
        fun record(): MutableList<ConnectionState> {
            val states = mutableListOf<ConnectionState>()
            CoroutineScope(StandardTestDispatcher(scheduler) + watchers).launch { connection.state.collect { states += it } }
            scheduler.runCurrent()
            return states
        }
        private val watchers = Job()

        suspend fun awaitSocket(count: Int): FakeSocket {
            withTimeout(5000) {
                while (sockets.size < count) {
                    scheduler.runCurrent()
                    delay(10)
                }
            }
            repeat(5) { scheduler.runCurrent(); delay(5) }
            return sockets.last()
        }

        fun advance(millis: Long) { scheduler.advanceTimeBy(millis); scheduler.runCurrent() }

        /** Moves virtual time in small steps, letting the real health probes in between finish. */
        suspend fun pump(millis: Long) {
            var left = millis
            while (left > 0) {
                val step = minOf(50L, left)
                scheduler.advanceTimeBy(step)
                repeat(4) { scheduler.runCurrent(); delay(3) }
                left -= step
            }
            scheduler.runCurrent()
        }

        override fun close() { watchers.cancel(); connection.close(); scheduler.runCurrent(); server.close() }
    }

    private class FakeSocket(val request: Request, private val listener: WebSocketListener) : WebSocket {
        val sent = mutableListOf<JsonObject>()
        var cancelled = false
        fun receive(text: String) = listener.onMessage(this, text)
        fun fail() = listener.onFailure(this, IOException("connection reset"), null)
        override fun request() = request
        override fun queueSize() = 0L
        override fun send(text: String): Boolean { sent += JsonCodec.parseToJsonElement(text).jsonObject; return true }
        override fun send(bytes: ByteString) = true
        override fun close(code: Int, reason: String?): Boolean {
            if (reason != null) listener.onClosing(this, code, reason)
            return true
        }
        override fun cancel() { cancelled = true }
    }
}
