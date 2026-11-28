package persona.core.hub

import persona.core.contracts.Endpoint
import persona.core.contracts.Hello
import persona.core.contracts.HubScope
import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.contracts.Session
import persona.core.contracts.api.MessageRequest
import persona.core.contracts.api.MessageEvidence
import persona.core.contracts.api.MessageTransportResult
import persona.core.contracts.api.MessageVia
import java.net.InetAddress
import java.security.MessageDigest
import java.util.Base64
import java.util.concurrent.atomic.AtomicBoolean
import java.util.concurrent.ConcurrentLinkedQueue
import kotlin.coroutines.CoroutineContext
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.first
import kotlinx.serialization.json.*
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import okhttp3.mockwebserver.SocketPolicy
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import org.junit.Assert.*
import org.junit.Test

class HttpMessageTransportTest {
    @Test fun negotiatedCapabilitiesDoNotWaitForAnEventConsumer() = runBlocking {
        assertHelloCapabilities(listOf("client_ops"), expected = true, attachAfterHello = false)
    }

    @Test fun transportAttachedAfterHelloRetainsNegotiatedCapabilities() = runBlocking {
        assertHelloCapabilities(listOf("client_ops"), expected = true, attachAfterHello = true)
    }

    @Test fun absentNegotiatedCapabilityIsNotInvented() = runBlocking {
        assertHelloCapabilities(emptyList(), expected = false, attachAfterHello = false)
    }

    private suspend fun assertHelloCapabilities(
        features: List<String>, expected: Boolean, attachAfterHello: Boolean,
    ) = coroutineScope {
        val certificate = HeldCertificate.Builder().commonName("example.com").build()
        val fingerprint = Base64.getUrlEncoder().withoutPadding().encodeToString(
            MessageDigest.getInstance("SHA-256").digest(certificate.certificate.publicKey.encoded))
        val enteredReady = CompletableDeferred<Unit>()
        val ready = CompletableDeferred<Unit>()
        val pending = ConcurrentLinkedQueue<Runnable>()
        // Deliberately never schedule the transport's event collector before
        // assertions. Capability visibility must not depend on its dispatcher.
        val dispatcher = object : CoroutineDispatcher() {
            override fun dispatch(context: CoroutineContext, block: Runnable) { pending.add(block) }
        }
        val jobs = CoroutineScope(SupervisorJob() + dispatcher)
        MockWebServer().use { server ->
            server.useHttps(HandshakeCertificates.Builder().heldCertificate(certificate).build().sslSocketFactory(), false)
            server.dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse = when (request.requestUrl!!.encodedPath) {
                    "/api/v1/health" -> MockResponse().setBody("""{"ok":true,"hub_id":"test-hub"}""")
                    "/api/v1/ws" -> MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                        override fun onOpen(webSocket: WebSocket, response: Response) {
                            webSocket.send(buildJsonObject {
                                put("type", "hello")
                                put("hub_id", "test-hub")
                                putJsonArray("features") { features.forEach { add(it) } }
                            }.toString())
                        }
                    })
                    else -> MockResponse().setResponseCode(404)
                }
            }
            server.start(InetAddress.getLoopbackAddress(), 0)
            val origin = server.url("/").newBuilder().host(InetAddress.getLoopbackAddress().hostAddress!!).build().toString()
            lateinit var transport: HttpMessageTransport
            OkHttpHubConnection(HubConnectionConfig("test-hub", fingerprint, "test-token",
                listOf(Endpoint(origin, "lan")), "test"), HubNetwork("initial", available = false),
                onReady = { enteredReady.complete(Unit); ready.await(); transport.onReady() }).use { connection ->
                try {
                    val pairing = HubScope("test-hub", "generation")
                    val observedHello = async(start = CoroutineStart.UNDISPATCHED) {
                        connection.events.first { it is Hello }
                    }
                    if (!attachAfterHello) {
                        transport = HttpMessageTransport(pairing, connection, "test-token", jobs)
                        assertFalse(transport.supportsClientOps)
                    }
                    connection.updateNetwork(HubNetwork("online"))
                    withTimeout(5000) { enteredReady.await(); observedHello.await() }
                    if (attachAfterHello) transport = HttpMessageTransport(pairing, connection, "test-token", jobs)
                    assertEquals(expected, transport.supportsClientOps)
                    assertFalse(transport.readyToSend.value)
                } finally {
                    ready.complete(Unit)
                    jobs.cancel()
                    // Drain canceled continuations, not an active event consumer.
                    while (true) { (pending.poll() ?: break).run() }
                }
            }
        }
    }

    @Test fun waitsForCatchUpPreservesOperationIdentityAndReturnsDeliveryEvidence() = runBlocking {
        val certificate = HeldCertificate.Builder().commonName("example.com").build()
        val fingerprint = Base64.getUrlEncoder().withoutPadding().encodeToString(
            MessageDigest.getInstance("SHA-256").digest(certificate.certificate.publicKey.encoded))
        val ready = CompletableDeferred<Unit>()
        val enteredReady = CompletableDeferred<Unit>()
        val responses = Channel<MockResponse>(Channel.UNLIMITED)
        val requests = Channel<RecordedRequest>(Channel.UNLIMITED)
        val expired = AtomicBoolean()
        val started = mutableListOf<Pair<SessionKey, Session>>()
        val jobs = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        MockWebServer().use { server ->
            server.useHttps(HandshakeCertificates.Builder().heldCertificate(certificate).build().sslSocketFactory(), false)
            server.dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse = when (request.requestUrl!!.encodedPath) {
                    "/api/v1/health" -> MockResponse().setBody("""{"ok":true,"hub_id":"test-hub"}""")
                    "/api/v1/ws" -> MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                        override fun onOpen(webSocket: WebSocket, response: Response) {
                            webSocket.send("""{"type":"hello","hub_id":"test-hub","features":["client_ops"]}""")
                        }
                    })
                    else -> {
                        requests.trySend(request)
                        runBlocking { responses.receive() }
                    }
                }
            }
            server.start(InetAddress.getLoopbackAddress(), 0)
            val origin = server.url("/").newBuilder().host(InetAddress.getLoopbackAddress().hostAddress!!).build().toString()
            lateinit var transport: HttpMessageTransport
            OkHttpHubConnection(HubConnectionConfig("test-hub", fingerprint, "test-token",
                listOf(Endpoint(origin, "lan")), "test"), HubNetwork("initial", available = false),
                onReady = { enteredReady.complete(Unit); ready.await(); transport.onReady() }).use { connection ->
                try {
                    val pairing = HubScope("test-hub", "generation")
                    transport = HttpMessageTransport(pairing, connection, jobs,
                        onSessionStarted = { previous, session -> started += previous to session },
                        onPairingExpired = { expired.set(true) })
                    val message = MessageRequest(pairing, SessionKey("machine", "session/encoded"), "hello", "stable-id", "draft")
                    assertEquals(MessageTransportResult.NotSent, transport.send(message))
                    connection.updateNetwork(HubNetwork("online"))
                    withTimeout(5000) { enteredReady.await() }
                    assertFalse(transport.readyToSend.value)
                    ready.complete(Unit)
                    withTimeout(5000) { transport.readyToSend.first { it } }
                    assertTrue(transport.supportsClientOps)

                    val storedBody = Base64.getEncoder().encodeToString("""{"ok":true}""".toByteArray())
                    responses.send(MockResponse().setBody("""{"state":"done","result":{"status":200,"body":"$storedBody"}}"""))
                    responses.send(MockResponse().setBody("""{"items":[{"id":"stable-id","role":"user","kind":"text","text":"hello"}]}"""))
                    assertEquals(MessageEvidence.Shown, transport.reconcile(message, 0))
                    assertEquals("/api/v1/ops/stable-id", (withTimeout(5000) { requests.receive() }.path).zhAny())
                    assertTrue(withTimeout(5000) { requests.receive() }.path!!.startsWith(
                        "/api/v1/sessions/machine/session%2Fencoded/items?"))
                    // Claude Code keeps its own id for the message: the transcript's copy is found by its words (#895).
                    responses.send(MockResponse().setBody("""{"state":"done","result":{"status":200,"body":"$storedBody"}}"""))
                    responses.send(MockResponse().setBody("""{"items":[{"id":"claude-uuid","role":"user","kind":"text","text":"hello","ts":"2026-11-03T09:00:00Z"}]}"""))
                    assertEquals(MessageEvidence.Shown, transport.reconcile(message, 0))
                    repeat(2) { withTimeout(5000) { requests.receive() } }
                    // Hubs since #968 tag the items a message became: a notice with other words is its copy…
                    responses.send(MockResponse().setBody("""{"state":"done","result":{"status":200,"body":"$storedBody"}}"""))
                    responses.send(MockResponse().setBody("""{"items":[{"id":"notice-1","role":"system","kind":"notice","text":"压缩上下文","client_msg_id":"stable-id"}]}"""))
                    assertEquals(MessageEvidence.Shown, transport.reconcile(message, 0))
                    repeat(2) { withTimeout(5000) { requests.receive() } }
                    // …and an item tagged for another message is not, even with the same words.
                    responses.send(MockResponse().setBody("""{"state":"done","result":{"status":200,"body":"$storedBody"}}"""))
                    responses.send(MockResponse().setBody("""{"items":[{"id":"claude-uuid","role":"user","kind":"text","text":"hello","ts":"2026-11-03T09:00:00Z","client_msg_id":"other-id"}]}"""))
                    assertEquals(MessageEvidence.Receipt(MessageTransportResult.Accepted), transport.reconcile(message, 0))
                    repeat(2) { withTimeout(5000) { requests.receive() } }

                    val heldBody = Base64.getEncoder().encodeToString("""{"delivered":"later"}""".toByteArray())
                    val heldReceipt = """{"state":"done","result":{"status":202,"body":"$heldBody"}}"""
                    val heldEvidence = MessageEvidence.Receipt(MessageTransportResult.Held)
                    for ((items, expectation) in listOf(
                        MockResponse().setResponseCode(503) to heldEvidence,
                        MockResponse().setBody("""{"items":[{"id":"other","role":"user","text":"hello","ts":"2026-11-03T09:00:00Z"}]}""") to heldEvidence,
                        MockResponse().setBody("""{"items":[{"id":"echo","role":"user","client_msg_id":"stable-id"}]}""") to MessageEvidence.Shown,
                    )) {
                        responses.send(MockResponse().setBody(heldReceipt))
                        responses.send(items)
                        assertEquals(expectation, transport.reconcile(message, 0))
                        repeat(2) { withTimeout(5000) { requests.receive() } }
                    }

                    val results = listOf(
                        MockResponse().setBody("""{"ok":true}""") to MessageTransportResult.Accepted,
                        MockResponse().setResponseCode(202).setBody("""{"delivered":"later"}""") to MessageTransportResult.Held,
                        MockResponse().setResponseCode(202).setBody("""{"state":"pending"}""") to MessageTransportResult.NoReceipt,
                        MockResponse().setResponseCode(202).setBody("""{"state":"unknown"}""") to MessageTransportResult.NoReceipt,
                        MockResponse().setResponseCode(409).setBody("""{"error":"draft","code":"input_draft","delivered":"none","retryable":false}""") to
                            MessageTransportResult.HubError("input_draft", "none", "draft", false),
                        MockResponse().setResponseCode(502).setBody("""{"error":"uncertain","code":"send_failed","delivered":"unknown","retryable":false}""") to
                            MessageTransportResult.HubError("send_failed", "unknown", "uncertain", false),
                        MockResponse().setSocketPolicy(SocketPolicy.DISCONNECT_AFTER_REQUEST) to MessageTransportResult.NoReceipt,
                    )
                    for ((response, expected) in results) {
                        responses.send(response)
                        assertEquals(expected, transport.send(message))
                        val sent = withTimeout(5000) { requests.receive() }
                        assertEquals("/api/v1/sessions/machine/session%2Fencoded/messages", (sent.path).zhAny())
                        assertEquals("Bearer test-token", (sent.getHeader("Authorization")).zhAny())
                        assertEquals("stable-id", (sent.getHeader("X-Persona-Op-Id")).zhAny())
                        val body = JsonCodec.parseToJsonElement(sent.body.readUtf8()).jsonObject
                        assertEquals("stable-id", (body["client_msg_id"]?.jsonPrimitive?.content).zhAny())
                        assertEquals("draft", (body["clear_draft"]?.jsonPrimitive?.content).zhAny())
                        assertFalse("launcher" in body)
                        assertFalse("model" in body)
                        assertNull("only the message that carries a launcher switch out says so", body["apply_switch"])
                        assertNull("an ordinary typed message has no provenance mark", body["via"])
                    }
                    responses.send(MockResponse().setResponseCode(202).setBody(
                        """{"delivered":"later","undelivered":{"id":"stable-id","text":"hello","at":"2026-11-17T10:00:00Z","state":"waiting"}}"""))
                    assertTrue("202 delivered=later is kept by the assistant, not accepted as delivered",
                        transport.send(message) is MessageTransportResult.Kept)
                    assertEquals("/api/v1/sessions/machine/session%2Fencoded/messages",
                        (withTimeout(5000) { requests.receive() }.path).zhAny())
                    responses.send(MockResponse().setBody("""{"ok":true}"""))
                    assertEquals(MessageTransportResult.Accepted,
                        transport.send(message.copy(launcher = "runtime", model = "model-id")))
                    val overrideBody = JsonCodec.parseToJsonElement(
                        withTimeout(5000) { requests.receive() }.body.readUtf8()).jsonObject
                    assertEquals("runtime", overrideBody["launcher"]?.jsonPrimitive?.content)
                    assertEquals("model-id", overrideBody["model"]?.jsonPrimitive?.content)
                    // #5055: the user's message from a chat with a switch waiting carries it out first
                    responses.send(MockResponse().setBody("""{"ok":true}"""))
                    assertEquals(MessageTransportResult.Accepted, transport.send(message.copy(applySwitch = true)))
                    val switching = JsonCodec.parseToJsonElement(withTimeout(5000) { requests.receive() }.body.readUtf8()).jsonObject
                    assertEquals(JsonPrimitive(true), switching["apply_switch"])
                    for (via in listOf(MessageVia.Share, MessageVia.Intent, MessageVia.Card)) {
                        responses.send(MockResponse().setBody("""{"ok":true}"""))
                        assertEquals(MessageTransportResult.Accepted, transport.send(message.copy(via = via)))
                        val marked = JsonCodec.parseToJsonElement(withTimeout(5000) { requests.receive() }.body.readUtf8()).jsonObject
                        assertEquals(via, marked["via"]?.jsonPrimitive?.content)
                    }
                    // A first Codex send returns the new thread before it enters the session-list window.
                    val firstReply = """{"ok":true,"session":{"machine_id":"","session_id":"thread","agent":"codex","pane_id":"pane"}}"""
                    responses.send(MockResponse().setBody(firstReply))
                    assertEquals(MessageTransportResult.Accepted, transport.send(message))
                    withTimeout(5000) { requests.receive() }
                    assertEquals(message.session, started.single().first)
                    assertEquals("machine", started.single().second.machineId)
                    assertEquals("thread", started.single().second.sessionId)
                    assertEquals("pane", started.single().second.paneId)
                    // A lost HTTP answer recovered through its operation receipt preserves the same target.
                    val savedFirst = Base64.getEncoder().encodeToString(firstReply.toByteArray())
                    responses.send(MockResponse().setBody("""{"state":"done","result":{"status":200,"body":"$savedFirst"}}"""))
                    responses.send(MockResponse().setBody("""{"items":[]}"""))
                    assertEquals(MessageEvidence.Receipt(MessageTransportResult.Accepted), transport.reconcile(message, 0))
                    repeat(2) { withTimeout(5000) { requests.receive() } }
                    assertEquals(2, started.size)
                    assertEquals(started.first(), started.last())
                    responses.send(MockResponse().setResponseCode(401).setBody("""{"error":"expired","code":"invalid_token"}"""))
                    assertTrue(transport.send(message) is MessageTransportResult.HubError)
                    assertTrue(expired.get())
                    try {
                        transport.send(message.copy(scope = HubScope("test-hub", "other-generation")))
                        fail("Expected scope mismatch")
                    } catch (_: IllegalArgumentException) { }
                } finally { jobs.cancel() }
            }
        }
    }

    @Test fun reconcileChecksTranscriptAndReportsFinalizedUnknownReceipts() = runBlocking {
        val certificate = HeldCertificate.Builder().commonName("example.com").build()
        val fingerprint = Base64.getUrlEncoder().withoutPadding().encodeToString(
            MessageDigest.getInstance("SHA-256").digest(certificate.certificate.publicKey.encoded))
        val ready = CompletableDeferred<Unit>()
        val responses = Channel<MockResponse>(Channel.UNLIMITED)
        val requests = Channel<RecordedRequest>(Channel.UNLIMITED)
        val jobs = CoroutineScope(SupervisorJob() + Dispatchers.Default)
        MockWebServer().use { server ->
            server.useHttps(HandshakeCertificates.Builder().heldCertificate(certificate).build().sslSocketFactory(), false)
            server.dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse = when (request.requestUrl!!.encodedPath) {
                    "/api/v1/health" -> MockResponse().setBody("""{"ok":true,"hub_id":"test-hub"}""")
                    "/api/v1/ws" -> MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                        override fun onOpen(webSocket: WebSocket, response: Response) {
                            webSocket.send("""{"type":"hello","hub_id":"test-hub","features":["client_ops"]}""")
                        }
                    })
                    else -> {
                        requests.trySend(request)
                        runBlocking { responses.receive() }
                    }
                }
            }
            server.start(InetAddress.getLoopbackAddress(), 0)
            val origin = server.url("/").newBuilder().host(InetAddress.getLoopbackAddress().hostAddress!!).build().toString()
            lateinit var transport: HttpMessageTransport
            OkHttpHubConnection(HubConnectionConfig("test-hub", fingerprint, "test-token",
                listOf(Endpoint(origin, "lan")), "test"), HubNetwork("initial", available = false),
                onReady = { transport.onReady(); ready.complete(Unit) }).use { connection ->
                try {
                    val pairing = HubScope("test-hub", "generation")
                    transport = HttpMessageTransport(pairing, connection, jobs)
                    val message = MessageRequest(pairing, SessionKey("machine", "session"), "hello", "stable-id")
                    connection.updateNetwork(HubNetwork("online"))
                    withTimeout(5000) { ready.await(); transport.readyToSend.first { it } }

                    // 1. Saved unknown receipt with transcript match converges to Shown.
                    val unknownBody = Base64.getEncoder().encodeToString(
                        """{"code":"send_failed","delivered":"unknown","error":"uncertain"}""".toByteArray())
                    responses.send(MockResponse().setBody("""{"state":"unknown","result":{"status":502,"body":"$unknownBody"}}"""))
                    responses.send(MockResponse().setBody("""{"items":[{"id":"e1","role":"user","kind":"text","text":"hello","client_msg_id":"stable-id"}]}"""))
                    assertEquals(MessageEvidence.Shown, transport.reconcile(message, 0))
                    repeat(2) { withTimeout(5000) { requests.receive() } }

                    // 2. Saved unknown receipt without transcript match returns the decoded HubError receipt.
                    responses.send(MockResponse().setBody("""{"state":"unknown","result":{"status":502,"body":"$unknownBody"}}"""))
                    responses.send(MockResponse().setBody("""{"items":[]}"""))
                    assertEquals(
                        MessageEvidence.Receipt(MessageTransportResult.HubError("send_failed", "unknown", "uncertain", false)),
                        transport.reconcile(message, 0),
                    )
                    repeat(2) { withTimeout(5000) { requests.receive() } }

                    // 3. Recovered unknown op (result=null) without transcript match reports Receipt(NoReceipt), not null.
                    responses.send(MockResponse().setBody("""{"state":"unknown","result":null}"""))
                    responses.send(MockResponse().setBody("""{"items":[]}"""))
                    assertEquals(MessageEvidence.Receipt(MessageTransportResult.NoReceipt), transport.reconcile(message, 0))
                    repeat(2) { withTimeout(5000) { requests.receive() } }

                    // 4. Recovered unknown op (result=null) with transcript match converges to Shown.
                    responses.send(MockResponse().setBody("""{"state":"unknown","result":null}"""))
                    responses.send(MockResponse().setBody("""{"items":[{"id":"e2","role":"user","kind":"text","text":"hello","ts":"2026-11-03T09:00:00Z"}]}"""))
                    assertEquals(MessageEvidence.Shown, transport.reconcile(message, 0))
                    repeat(2) { withTimeout(5000) { requests.receive() } }

                    // 5. Transcript read error does not drop a finalized unknown receipt.
                    responses.send(MockResponse().setBody("""{"state":"unknown","result":null}"""))
                    responses.send(MockResponse().setResponseCode(503))
                    assertEquals(MessageEvidence.Receipt(MessageTransportResult.NoReceipt), transport.reconcile(message, 0))
                    repeat(2) { withTimeout(5000) { requests.receive() } }

                    // 6. Saved unknown receipt with delivered:"none" returns the receipt directly without hitting /items.
                    val notDeliveredBody = Base64.getEncoder().encodeToString(
                        """{"code":"node_unreachable","delivered":"none","retryable":true,"error":"machine offline"}""".toByteArray())
                    responses.send(MockResponse().setBody("""{"state":"unknown","result":{"status":503,"body":"$notDeliveredBody"}}"""))
                    assertEquals(
                        MessageEvidence.Receipt(MessageTransportResult.HubError("node_unreachable", "none", "machine offline", true)),
                        transport.reconcile(message, 0),
                    )
                    assertEquals("/api/v1/ops/stable-id", withTimeout(5000) { requests.receive() }.path)

                    // 7. Pending op remains null so retries keep the same opId.
                    responses.send(MockResponse().setBody("""{"state":"pending"}"""))
                    responses.send(MockResponse().setBody("""{"items":[]}"""))
                    assertNull(transport.reconcile(message, 0))
                    repeat(2) { withTimeout(5000) { requests.receive() } }
                } finally { jobs.cancel() }
            }
        }
    }
}
