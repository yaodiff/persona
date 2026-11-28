package persona.core.hub

import persona.core.contracts.Endpoint
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.AgentMessageHoldFeature
import persona.core.contracts.api.ConnectionState
import java.io.EOFException
import java.net.InetAddress
import java.net.ServerSocket
import java.net.SocketException
import java.net.SocketTimeoutException
import java.security.cert.CertificateException
import java.security.MessageDigest
import java.util.Base64
import java.util.UUID
import java.util.concurrent.ConcurrentLinkedQueue
import java.util.concurrent.atomic.AtomicBoolean
import javax.net.ssl.SSLHandshakeException
import javax.net.ssl.SSLPeerUnverifiedException
import kotlin.concurrent.thread
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.*
import okhttp3.Dns
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

class HubNetworkingTest {
    @Test fun pinnedSelfSignedConnectionNegotiatesHeartbeatAndStopsAfterRevocation() = runBlocking {
        val certificate = HeldCertificate.Builder().commonName("example.com").build()
        val hellos = Channel<JsonObject>(Channel.UNLIMITED)
        val revoked = AtomicBoolean()
        val requests = ConcurrentLinkedQueue<RecordedRequest>()
        tlsServer(certificate).use { server ->
            server.dispatcher = object : Dispatcher() {
                override fun dispatch(request: RecordedRequest): MockResponse {
                    requests.add(request)
                    if (request.path == "/api/v1/health") return health()
                    if (revoked.get()) return MockResponse().setResponseCode(401)
                        .setBody("""{"error":"expired","code":"invalid_token","retryable":false}""")
                    return MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                        override fun onOpen(webSocket: WebSocket, response: okhttp3.Response) {
                            webSocket.send("""{"type":"hello","hub_id":"test-hub","features":["app_ping","heartbeat","client_hello"]}""")
                        }
                        override fun onMessage(webSocket: WebSocket, text: String) {
                            val message = JsonCodec.parseToJsonElement(text).jsonObject
                            when (message["type"]?.jsonPrimitive?.content) {
                                "client_hello" -> {
                                    hellos.trySend(message)
                                    webSocket.send(buildJsonObject {
                                        put("type", "client_hello_ack")
                                        put("heartbeat", message.getValue("heartbeat"))
                                    }.toString())
                                }
                                "ping" -> webSocket.send(buildJsonObject {
                                    put("type", "pong"); put("id", message.getValue("id"))
                                }.toString())
                            }
                        }
                    })
                }
            }
            val endpoint = endpoint(server, "lan")
            HubHttpClient("test-hub", fingerprint(certificate), "test").use { pinned ->
                assertEquals("test-hub", (pinned.health(endpoint).hubId).zhAny())
            }
            val other = HeldCertificate.Builder().commonName("example.com").build()
            HubHttpClient("test-hub", fingerprint(other), "test").use { wrongPin ->
                assertEquals(HubError.CertificateMismatch, failure { wrongPin.health(endpoint) }.kind)
            }
            val publicDns = object : Dns {
                override fun lookup(hostname: String) = listOf(InetAddress.getByAddress(byteArrayOf(192.toByte(), 0, 2, 1)))
            }
            HubHttpClient("test-hub", null, "test", dns = publicDns).use { plaintext ->
                assertEquals(HubError.TlsRequired,
                    failure { plaintext.health(Endpoint("http://example.com/", "lan")) }.kind)
            }
            val config = HubConnectionConfig("test-hub", fingerprint(certificate), UUID.randomUUID().toString(), listOf(endpoint), "test")
            OkHttpHubConnection(config, HubNetwork("first")).use { connection ->
                val first = withTimeout(5000) { hellos.receive() }
                assertEquals("android", (first.getValue("platform").jsonPrimitive.content).zhAny())
                assertEquals(setOf("endpoints", "ws_notify", "app_ping", "elicitation", "approval_mirrors", AgentMessageHoldFeature, "tool_views", "persona_contracts_v1"),
                    first.getValue("features").jsonArray.map { it.jsonPrimitive.content }.toSet())
                assertEquals(20, first.getValue("heartbeat").jsonObject.getValue("interval").jsonPrimitive.int)
                withTimeout(5000) { connection.state.first { it == ConnectionState.Online } }
                assertEquals(fingerprint(certificate), connection.diagnostics.value.fingerprint)
                assertNotNull(connection.diagnostics.value.currentPath)
                connection.setForeground(false)
                val background = withTimeout(5000) { hellos.receive() }
                assertEquals(240, background.getValue("heartbeat").jsonObject.getValue("interval").jsonPrimitive.int)
                withTimeout(5000) { connection.heartbeat.first { it.interval == 240 } }
                connection.setForeground(true)
                assertEquals(20, withTimeout(5000) { hellos.receive() }.getValue("heartbeat").jsonObject.getValue("interval").jsonPrimitive.int)
                revoked.set(true)
                connection.reconnect()
                withTimeout(5000) { connection.state.first { it == ConnectionState.Revoked } }
                assertFalse(connection.failure.value!!.retryable)
                val count = server.requestCount
                connection.reconnect()
                connection.updateNetwork(HubNetwork("second"))
                assertEquals(count, server.requestCount)
            }
            assertTrue(requests.filter { it.path == "/api/v1/health" }.all { it.getHeader("Authorization") == null })
            assertTrue(requests.filter { it.requestUrl!!.encodedPath == "/api/v1/ws" }.all { it.getHeader("Authorization") != null })
        }
    }

    @Test fun routeRaceRejectsWrongPinAndHubAndTriesStrictlyInTheGivenOrderEveryTime() = runBlocking {
        val certificate = HeldCertificate.Builder().commonName("example.com").build()
        val wrongCertificate = HeldCertificate.Builder().commonName("example.com").build()
        val servers = listOf(tlsServer(wrongCertificate), tlsServer(certificate), tlsServer(certificate), tlsServer(certificate))
        val requests = ConcurrentLinkedQueue<RecordedRequest>()
        // Which server each request reached, in arrival order (the wrong pin fails in the handshake, before any request).
        val arrivals = ConcurrentLinkedQueue<Int>()
        try {
            servers.forEachIndexed { index, server ->
                server.dispatcher = object : Dispatcher() {
                    override fun dispatch(request: RecordedRequest): MockResponse {
                        requests.add(request)
                        arrivals.add(index)
                        return when (index) {
                            1 -> health("other-hub")
                            2 -> MockResponse().setSocketPolicy(SocketPolicy.NO_RESPONSE)
                            else -> health()
                        }
                    }
                }
            }
            val candidates = servers.mapIndexed { index, server -> endpoint(server, listOf("ipv6", "name", "lan", "ipv4")[index]) }
            HubHttpClient("test-hub", fingerprint(certificate), "test").use { http ->
                val selector = HubRouteSelector(http)
                val outcomes = ConcurrentLinkedQueue<Pair<Int, HubError?>>()
                val started = System.nanoTime()
                val winner = withTimeout(5000) {
                    selector.select(candidates) { endpoint, result ->
                        outcomes.add(candidates.indexOf(endpoint) to result.exceptionOrNull()?.let { HubFailure.from(it).kind })
                    }
                }
                // The wrong pin and the wrong hub fail at once, each starting the next; the silent one has its full step
                // alone before the last one starts and wins.
                assertEquals(candidates[3], winner.endpoint)
                assertTrue(System.nanoTime() - started >= HubRouteSelector.STEP_MILLIS * 1_000_000)
                assertEquals(listOf(0 to HubError.CertificateMismatch, 1 to HubError.HubMismatch, 3 to null), outcomes.toList())
                assertEquals(listOf(1, 2, 3), arrivals.toList())

                // Nothing is re-sorted: the order given is the order tried, whatever the kinds say.
                arrivals.clear()
                assertEquals(candidates[3], withTimeout(5000) { selector.select(candidates.reversed()) }.endpoint)
                assertEquals(3, arrivals.first())
                // No winner is remembered: the same candidates are tried from the top again.
                arrivals.clear()
                assertEquals(candidates[3], withTimeout(5000) { selector.select(candidates) }.endpoint)
                assertEquals(listOf(1, 2, 3), arrivals.toList())
                // The pin outranks the hub id among the reasons, whichever failed first.
                assertEquals(HubError.CertificateMismatch,
                    failure { withTimeout(5000) { selector.select(listOf(candidates[1], candidates[0])) } }.kind)
                assertEquals(HubError.HubMismatch, failure { withTimeout(5000) { selector.select(listOf(candidates[1])) } }.kind)
                assertTrue(requests.all { it.path == "/api/v1/health" && it.getHeader("Authorization") == null })
            }
        } finally { servers.forEach { it.close() } }
    }

    /**
     * A restarting hub behind a port proxy (Docker Desktop, adb reverse) takes the TCP connection and drops it in the
     * middle of the handshake: a network error to retry, not "the hub's certificate changed" (#1250). A wrong pin still is.
     */
    @Test fun aHandshakeCutShortIsRetriedNotACertificateChange() = runBlocking {
        val certificate = HeldCertificate.Builder().commonName("example.com").build()
        ServerSocket(0, 50, InetAddress.getLoopbackAddress()).use { proxy ->
            val _ = thread(isDaemon = true) {
                while (!proxy.isClosed) {
                    val _ = runCatching {
                        proxy.accept().use { socket -> socket.getInputStream().read(ByteArray(512)) } // the ClientHello, then gone
                    }
                }
            }
            val endpoint = Endpoint("https://${InetAddress.getLoopbackAddress().hostAddress}:${proxy.localPort}/", "lan")
            HubHttpClient("test-hub", fingerprint(certificate), "test").use { client ->
                val failure = failure { client.health(endpoint) }
                assertNotEquals(HubError.CertificateMismatch, failure.kind)
                assertTrue("a cut handshake is retried", failure.retryable)
            }
        }
        tlsServer(certificate).use { server ->
            server.enqueue(health())
            val other = HeldCertificate.Builder().commonName("example.com").build()
            HubHttpClient("test-hub", fingerprint(other), "test").use { wrongPin ->
                val failure = failure { wrongPin.health(endpoint(server, "lan")) }
                assertEquals(HubError.CertificateMismatch, failure.kind)
                assertFalse(failure.retryable)
            }
        }
    }

    @Test fun onlyARefusedCertificateIsACertificateChange() {
        val refused = SSLHandshakeException("handshake failed").apply { initCause(CertificateException("Hub SPKI mismatch")) }
        assertEquals(HubError.CertificateMismatch, HubFailure.from(refused).kind)
        assertEquals(HubError.CertificateMismatch, HubFailure.from(SSLPeerUnverifiedException("Hostname not verified")).kind)
        for (cut in listOf(
            SSLHandshakeException("Connection closed by peer"),
            SSLHandshakeException("Remote host terminated the handshake").apply { initCause(EOFException("SSL peer shut down incorrectly")) },
            SSLHandshakeException("Read error").apply { initCause(SocketException("Connection reset")) },
        )) {
            val failure = HubFailure.from(cut)
            assertEquals(cut.message, HubError.Transport, failure.kind)
            assertTrue(cut.message, failure.retryable)
        }
        assertEquals(HubError.Timeout, HubFailure.from(SSLHandshakeException("timed out").apply { initCause(SocketTimeoutException()) }).kind)
    }

    private fun health(id: String = "test-hub") = MockResponse().setBody("""{"ok":true,"hub_id":"$id"}""")

    private fun tlsServer(certificate: HeldCertificate) = MockWebServer().apply {
        val tls = HandshakeCertificates.Builder().heldCertificate(certificate).build()
        useHttps(tls.sslSocketFactory(), false)
        start(InetAddress.getLoopbackAddress(), 0)
    }

    private fun endpoint(server: MockWebServer, kind: String) = Endpoint(
        server.url("/").newBuilder().host(InetAddress.getLoopbackAddress().hostAddress!!).build().toString(), kind,
    )

    private fun fingerprint(certificate: HeldCertificate) = Base64.getUrlEncoder().withoutPadding().encodeToString(
        MessageDigest.getInstance("SHA-256").digest(certificate.certificate.publicKey.encoded),
    )

    private suspend fun failure(block: suspend () -> Unit): HubFailure {
        try { block() } catch (error: HubFailure) { return error }
        throw AssertionError("Expected HubFailure")
    }
}
