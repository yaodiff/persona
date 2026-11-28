package persona.core.hub

import persona.core.contracts.HubScope
import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.Protocol
import okhttp3.Request
import okhttp3.Response
import okhttp3.ResponseBody.Companion.toResponseBody
import okio.Buffer
import org.junit.Assert.*
import org.junit.Test
import java.io.File

class ChatComposerServiceTest {
    @Test fun heldReceiptDoesNotReportDeliveryOrStartAnotherSession() = runBlocking {
        val scope = HubScope("test-hub", "generation")
        val session = SessionKey("machine", "session")
        var accepted = 0
        var advertised: String? = null
        var held: persona.core.contracts.HeldAgentMessage? = null
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = true
            override suspend fun send(request: MessageRequest): MessageTransportResult = error("Composer owns sending")
            override suspend fun onAccepted(session: SessionKey, reply: JsonObject?) { accepted++ }
        }
        val service = HttpChatComposerService(scope, transport, request = { _, _, headers ->
            advertised = headers["X-Persona-Features"]
            Response.Builder().request(Request.Builder().url("https://example.com/").build())
                .protocol(Protocol.HTTP_1_1).code(202).message("Accepted").body(
                    """{"delivered":"later","undelivered":{"id":"op","text":"Hello","computer":"Laptop","state":"waiting"}}""".toResponseBody()
                ).build()
        }, onHeld = { target, message -> assertEquals(session, target); held = message })
        val result = service.send(MessageRequest(scope, session, "Hello", "op"))
        assertEquals("Manager custody is not Agent delivery", 0, accepted)
        assertEquals(MessageTransportResult.Held, result)
        assertEquals("Laptop", held?.computer)
        assertEquals(DeliveryState.Held, service.deliveries.value["op"]?.state)
        assertTrue(advertised.orEmpty().split(',').contains("agent_message_hold"))
    }

    @Test fun uploadsKnownLengthAndRetriesOnlyWithMatchingRefusalEvidence() = runBlocking {
        val scope = HubScope("test-hub", "generation")
        val session = SessionKey("machine", "session")
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = false
            override suspend fun send(request: MessageRequest): MessageTransportResult = error("Unexpected delegate send")
        }
        var attempts = 0
        val provenance = mutableListOf<String?>()
        val retryResults = mutableListOf<Pair<DeliveryState, MessageTransportResult?>>()
        val retryStarts = mutableListOf<Pair<String, String>>()
        val service = HttpChatComposerService(scope, transport, request = { path, body, headers ->
            val buffer = Buffer()
            body.writeTo(buffer)
            val (code, response) = if (path.contains("/files?")) {
                assertEquals(3L, body.contentLength())
                assertEquals("abc", (buffer.readUtf8()).zhAny())
                assertTrue(path.contains("name="))
                200 to """{"path":"/tmp/test.txt"}"""
            } else {
                val fields = JsonCodec.parseToJsonElement(buffer.readUtf8()).jsonObject
                assertEquals(headers["X-Persona-Op-Id"], fields["client_msg_id"]!!.jsonPrimitive.content)
                assertEquals("消息", (fields["text"]!!.jsonPrimitive.content).zhAny())
                if (headers["X-Persona-Op-Id"] == "overrides") {
                    assertNull(fields["via"])
                    assertEquals("runtime", fields["launcher"]?.jsonPrimitive?.content)
                    assertEquals("model-id", fields["model"]?.jsonPrimitive?.content)
                    return@HttpChatComposerService Response.Builder()
                        .request(Request.Builder().url("https://example.com/").build())
                        .protocol(Protocol.HTTP_1_1).code(200).message("test").body("{}".toResponseBody()).build()
                }
                attempts++
                if (attempts > 1) assertEquals(retryStarts.single().second, headers["X-Persona-Op-Id"])
                provenance += fields["via"]?.jsonPrimitive?.content
                // The retry goes under a new operation id: the hub would answer "operation" from its first try.
                assertEquals(attempts == 1, headers["X-Persona-Op-Id"] == "operation")
                if (attempts == 1) {
                    assertNull(fields["clear_draft"])
                    409 to """{"error":"电脑上有草稿","code":"input_draft","delivered":"none","draft":"电脑草稿","clearable":"电脑草稿"}"""
                } else {
                    assertEquals("电脑草稿", (fields["clear_draft"]!!.jsonPrimitive.content).zhAny())
                    200 to "{}"
                }
            }
            Response.Builder().request(Request.Builder().url("https://example.com/").build())
                .protocol(Protocol.HTTP_1_1).code(code).message("test").body(response.toResponseBody()).build()
        }, onRetryStart = { id, opId -> retryStarts += id to opId },
            onRetryDelivery = { _, delivery, _, result -> retryResults += delivery.state to result })
        val file = File.createTempFile("composer-test-", ".txt")
        try {
            file.writeText("abc")
            assertEquals("/tmp/test.txt", (service.upload(session, "test.txt", file)).zhAny())
        } finally { file.delete() }
        service.send(MessageRequest(scope, session, "消息", "operation", via = MessageVia.Share))
        assertEquals("电脑草稿", (service.deliveries.value["operation"]!!.refusal!!.draft).zhAny())
        assertEquals(DeliveryState.Sent, service.retryRefused("operation", true).state)
        assertEquals(listOf(
            DeliveryState.Sending to null,
            DeliveryState.Sent to MessageTransportResult.Accepted,
        ), retryResults)
        try {
            service.retryRefused("operation", true)
            fail("A delivered message must not be retried")
        } catch (_: ComposerOperationException) { }
        assertEquals(MessageTransportResult.Accepted,
            service.send(MessageRequest(scope, session, "消息", "overrides", launcher = "runtime", model = "model-id")))
        assertEquals(listOf(MessageVia.Share, MessageVia.Share), provenance)
        assertEquals(2, attempts)
    }

    @Test fun keptReplyIsNotAcceptedOrMarkedSent() = runBlocking {
        val scope = HubScope("test-hub", "generation")
        val session = SessionKey("machine", "session")
        var accepted = 0
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = true
            override suspend fun send(request: MessageRequest): MessageTransportResult = error("Unexpected delegate send")
            override suspend fun onAccepted(session: SessionKey, reply: kotlinx.serialization.json.JsonObject?) { accepted++ }
        }
        val service = HttpChatComposerService(scope, transport, request = { _, _, _ ->
            Response.Builder().request(Request.Builder().url("https://example.com/").build())
                .protocol(Protocol.HTTP_1_1).code(202).message("kept")
                .body("""{"delivered":"later","undelivered":{"id":"kept","text":"message","at":"2026-11-17T10:00:00Z","state":"waiting"}}""".toResponseBody()).build()
        })
        assertEquals(MessageTransportResult.Kept, service.send(MessageRequest(scope, session, "message", "kept")))
        assertEquals(0, accepted)
        assertNull(service.deliveries.value["kept"])
    }

    @Test fun clearAndSendGoesUnderANewOperationIdTheHubHasNotAnswered() = runBlocking {
        val scope = HubScope("test-hub", "generation")
        val session = SessionKey("machine", "session")
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = true
            override suspend fun send(request: MessageRequest): MessageTransportResult = error("Unexpected delegate send")
        }
        // Like the hub's client ops: the first answer per operation id, op_id_reused when its parameters change.
        val receipts = mutableMapOf<String, Pair<String, Pair<Int, String>>>()
        val ops = mutableListOf<String>()
        val service = HttpChatComposerService(scope, transport, request = { _, body, headers ->
            val buffer = Buffer()
            body.writeTo(buffer)
            val raw = buffer.readUtf8()
            val op = headers.getValue("X-Persona-Op-Id")
            ops += op
            assertEquals(op, JsonCodec.parseToJsonElement(raw).jsonObject["client_msg_id"]!!.jsonPrimitive.content)
            val params = JsonCodec.parseToJsonElement(raw).jsonObject.filterKeys { it != "client_msg_id" }.toString()
            val (code, response) = receipts[op]?.let { (seen, answer) ->
                if (seen == params) answer else 409 to """{"code":"op_id_reused"}"""
            } ?: (if (receipts.isEmpty()) 409 to """{"error":"电脑上有草稿","code":"input_draft","delivered":"none","draft":"电脑草稿","clearable":"电脑草稿"}"""
                else 200 to "{}").also { receipts[op] = params to it }
            Response.Builder().request(Request.Builder().url("https://example.com/").build())
                .protocol(Protocol.HTTP_1_1).code(code).message("test").body(response.toResponseBody()).build()
        })
        service.send(MessageRequest(scope, session, "消息", "operation"))
        assertEquals(DeliveryState.Refused, service.deliveries.value["operation"]!!.state)
        assertEquals(DeliveryState.Sent, service.retryRefused("operation", true).state)
        assertEquals(2, ops.size)
        assertNotEquals(ops[0], ops[1])
        assertEquals(DeliveryState.Sent, service.deliveries.value["operation"]!!.state)
    }

    @Test fun retryCallbackReportsTheActualDeliveredYesFailure() = runBlocking {
        val scope = HubScope("test-hub", "generation")
        val session = SessionKey("machine", "session")
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = false
            override suspend fun send(request: MessageRequest): MessageTransportResult = error("Unexpected delegate send")
        }
        var attempts = 0
        val callbacks = mutableListOf<Pair<DeliveryState, MessageTransportResult?>>()
        val service = HttpChatComposerService(scope, transport, request = { _, _, _ ->
            val (code, body) = if (attempts++ == 0) {
                409 to """{"error":"draft","code":"input_draft","delivered":"none","draft":"draft","clearable":"draft"}"""
            } else {
                500 to """{"error":"HTTP 500","code":"internal","delivered":"yes"}"""
            }
            Response.Builder().request(Request.Builder().url("https://example.com/").build())
                .protocol(Protocol.HTTP_1_1).code(code).message("test").body(body.toResponseBody()).build()
        }, onRetryDelivery = { _, delivery, _, result -> callbacks += delivery.state to result })
        service.send(MessageRequest(scope, session, "message", "operation"))
        assertEquals(DeliveryState.Sent, service.retryRefused("operation", true).state)
        assertEquals(2, callbacks.size)
        assertEquals(DeliveryState.Sending to null, callbacks[0])
        assertEquals(DeliveryState.Sent, callbacks[1].first)
        val result = callbacks[1].second
        assertTrue(result is MessageTransportResult.HubError)
        result as MessageTransportResult.HubError
        assertEquals("internal", result.code)
        assertEquals("yes", result.delivered)
    }

    @Test fun retryCallbackUsesNullForSendingAndCancellation() = runBlocking {
        val scope = HubScope("test-hub", "generation")
        val session = SessionKey("machine", "session")
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = false
            override suspend fun send(request: MessageRequest): MessageTransportResult = error("Unexpected delegate send")
        }
        var attempts = 0
        val retryStarted = CompletableDeferred<Unit>()
        val responseGate = CompletableDeferred<Unit>()
        val callbacks = mutableListOf<Pair<DeliveryState, MessageTransportResult?>>()
        val service = HttpChatComposerService(scope, transport, request = { _, _, _ ->
            if (attempts++ == 0) {
                Response.Builder().request(Request.Builder().url("https://example.com/").build())
                    .protocol(Protocol.HTTP_1_1).code(409).message("test").body(
                        """{"error":"draft","code":"input_draft","delivered":"none","draft":"draft","clearable":"draft"}"""
                            .toResponseBody()).build()
            } else {
                retryStarted.complete(Unit)
                responseGate.await()
                error("cancelled response must not complete")
            }
        }, onRetryDelivery = { _, delivery, _, result -> callbacks += delivery.state to result })
        service.send(MessageRequest(scope, session, "message", "operation"))
        val retry = async { service.retryRefused("operation", true) }
        try {
            retryStarted.await()
            assertEquals(listOf(DeliveryState.Sending to null), callbacks)
            retry.cancelAndJoin()
            assertTrue(retry.isCancelled)
            assertEquals(listOf(
                DeliveryState.Sending to null,
                DeliveryState.Unknown to null,
            ), callbacks)
        } finally {
            responseGate.complete(Unit)
            retry.cancelAndJoin()
        }
    }

    // An interrupt refused for this request (#4803) keeps the pairing, as every other request does since #4776; only a
    // refused credential ends it.
    @Test fun aRefusedInterruptEndsThePairingOnlyForARefusedCredential() = runBlocking {
        val scope = HubScope("test-hub", "generation")
        val session = SessionKey("machine", "session")
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = false
            override suspend fun send(request: MessageRequest): MessageTransportResult = error("Unexpected delegate send")
        }
        val cases = listOf(
            Triple(403, """{"error":"TLS is required for public requests","code":"tls_required"}""", false),
            Triple(403, """{"error":"only on the hub's machine","code":"this_machine_only"}""", false),
            Triple(401, """{"error":"invalid token","code":"invalid_token"}""", true),
        )
        for ((code, body, endsPairing) in cases) {
            var expired = 0
            val paths = mutableListOf<String>()
            val service = HttpChatComposerService(scope, transport, request = { path, _, _ ->
                paths += path
                Response.Builder().request(Request.Builder().url("https://example.com/").build())
                    .protocol(Protocol.HTTP_1_1).code(code).message("test").body(body.toResponseBody()).build()
            }, onPairingExpired = { expired++ })
            try {
                service.interrupt(session)
                fail("A refused interrupt must not look confirmed")
            } catch (_: ComposerOperationException) { }
            assertEquals(listOf("/api/v1/sessions/machine/session/interrupt"), paths)
            assertEquals("$code $body", if (endsPairing) 1 else 0, expired)
        }
    }

    @Test fun uploadDestinationFollowsFeaturesAndExplicitHealthAlwaysWins() = runBlocking {
        val scope = HubScope("test-hub", "generation")
        val session = SessionKey("machine", "session")
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = false
            override suspend fun send(request: MessageRequest): MessageTransportResult = error("Unexpected delegate send")
        }
        val cases = listOf(
            Triple(setOf("file_upload", "upload_attachment"), "attachment", "attachment"),
            Triple(emptySet(), "attachment", "attachment"),
            Triple(setOf("file_upload"), "attachment", null),
            Triple(setOf("upload_attachment"), "health", "health"),
            Triple(setOf("file_upload"), "health", "health"),
        )
        for ((features, requested, expected) in cases) {
            val paths = mutableListOf<String>()
            val service = HttpChatComposerService(scope, transport, request = { path, _, _ ->
                paths += path
                uploadResponse(200, """{"path":"/up/file.txt"}""")
            }, features = { features })
            val file = File.createTempFile("composer-destination-", ".txt")
            try {
                assertEquals("/up/file.txt", service.upload(session, "file.txt", file, requested))
                val expectedQuery = "name=file.txt" + (expected?.let { "&dest=$it" } ?: "")
                assertEquals("features=$features requested=$requested",
                    listOf("/api/v1/machines/machine/files?$expectedQuery"), paths)
            } finally { file.delete() }
        }
    }

    @Test fun unknownAttachmentDestinationRetriesThatFileOnceWithoutDestAndNeverLoops() = runBlocking {
        val scope = HubScope("test-hub", "generation")
        val session = SessionKey("machine", "session")
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = false
            override suspend fun send(request: MessageRequest): MessageTransportResult = error("Unexpected delegate send")
        }
        val file = File.createTempFile("composer-fallback-", ".txt")
        try {
            val paths = mutableListOf<String>()
            val service = HttpChatComposerService(scope, transport, request = { path, _, _ ->
                paths += path
                if (paths.size == 1) uploadResponse(400, """{"error":"unknown dest"}""")
                else uploadResponse(200, """{"path":"/tmp/file.txt"}""")
            }, features = { setOf("upload_attachment") })
            assertEquals("/tmp/file.txt", service.upload(session, "file.txt", file, "attachment"))
            assertEquals(listOf(
                "/api/v1/machines/machine/files?name=file.txt&dest=attachment",
                "/api/v1/machines/machine/files?name=file.txt",
            ), paths)

            val refused = mutableListOf<String>()
            val noLoop = HttpChatComposerService(scope, transport, request = { path, _, _ ->
                refused += path
                uploadResponse(400, """{"error":"unknown dest"}""")
            }, features = { setOf("upload_attachment") })
            try {
                noLoop.upload(session, "file.txt", file, "attachment")
                fail("The second refusal must remain a failure")
            } catch (_: ComposerOperationException) { }
            assertEquals(2, refused.size)
            assertFalse(refused[1].contains("dest="))

            val health = mutableListOf<String>()
            val noHealthFallback = HttpChatComposerService(scope, transport, request = { path, _, _ ->
                health += path
                uploadResponse(400, """{"error":"unknown dest"}""")
            })
            try {
                noHealthFallback.upload(session, "health.json", file, "health")
                fail("Health must not silently change destination")
            } catch (_: ComposerOperationException) { }
            assertEquals(listOf("/api/v1/machines/machine/files?name=health.json&dest=health"), health)
        } finally { file.delete() }
    }

    @Test fun assistantOfflineDoesNotRecordStaleUnknownOverlay() = runBlocking {
        val scope = HubScope("test-hub", "generation")
        val session = SessionKey("machine", "session")
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = true
            override suspend fun send(request: MessageRequest): MessageTransportResult = error("Unexpected delegate send")
        }
        val answers = ArrayDeque(listOf(
            """{"code":"assistant_offline","error":"assistant offline"}""",
            """{"code":"assistant_offline","delivered":"none","retryable":true,"error":"assistant offline"}""",
        ))
        val service = HttpChatComposerService(scope, transport, request = { _, _, _ ->
            uploadResponse(503, answers.removeFirst())
        })
        service.send(MessageRequest(scope, session, "legacy", "legacy-id"))
        assertEquals(DeliveryState.WaitingForNetwork, service.deliveries.value["legacy-id"]?.state)
        service.send(MessageRequest(scope, session, "current", "current-id"))
        assertEquals(DeliveryState.WaitingForNetwork, service.deliveries.value["current-id"]?.state)
    }

    private fun uploadResponse(code: Int, body: String): Response = Response.Builder()
        .request(Request.Builder().url("https://example.com/").build())
        .protocol(Protocol.HTTP_1_1).code(code).message("test").body(body.toResponseBody()).build()
}
