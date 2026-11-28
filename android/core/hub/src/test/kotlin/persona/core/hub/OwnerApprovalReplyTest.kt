package persona.core.hub

import java.io.IOException
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.jsonObject
import okhttp3.Protocol
import okhttp3.Request
import okhttp3.Response
import okhttp3.ResponseBody.Companion.toResponseBody
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec

/** Exact owner replies use a narrow authenticated message, never an ordinary resolve or composer send. */
@OptIn(ExperimentalCoroutinesApi::class)
class OwnerApprovalReplyTest {
    private data class Sent(val path: String, val opId: String?, val body: JsonObject)

    private fun response(path: String, body: String) = Response.Builder()
        .request(Request.Builder().url("https://hub.example$path").build())
        .protocol(Protocol.HTTP_1_1).code(200).message("OK").body(body.toResponseBody()).build()

    private fun proposal() = Interaction(
        id = "proposal-1", machineId = "machine-1", sessionId = "session-1", kind = "permission",
        approvalBatchId = "batch-1", ownerProposal = true, ownerApprovalPhrases = listOf("发", "同意"),
        createdAt = "2026-11-21T12:00:00.123456789Z",
    )

    @Test fun exactReplyHasOnlyTheProofEnvelopeAndRetriesUnderOneEventId() = runTest {
        val sent = mutableListOf<Sent>()
        var fail = true
        var ids = 0
        val transport = HubChatTransport({ path, method, body, headers ->
            assertEquals("POST", method)
            val buffer = okio.Buffer().also { body!!.writeTo(it) }
            sent += Sent(path, headers[HubChatTransport.OP_ID_HEADER], JsonCodec.parseToJsonElement(buffer.readUtf8()).jsonObject)
            if (fail) {
                fail = false
                throw HubFailure.from(IOException("connection reset"))
            }
            response(path, """{"ok":true,"approval_reply":true,"interaction_id":"proposal-1"}""")
        }, awaitOnline = { true }, clockMillis = { testScheduler.currentTime }, newOpId = { "event-${++ids}" })

        transport.replyOwnerApproval(proposal(), "发")

        assertEquals(2, sent.size)
        assertEquals(setOf("event-1"), sent.map { it.opId }.toSet())
        assertEquals(1, sent.map { it.body }.toSet().size)
        assertEquals("/api/v1/sessions/machine-1/session-1/messages", sent.first().path)
        assertEquals(JsonObject(mapOf(
            "text" to JsonPrimitive("发"),
            "client_msg_id" to JsonPrimitive("event-1"),
            "approval_reply_to" to JsonPrimitive("proposal-1"),
            "approval_reply_created_at" to JsonPrimitive("2026-11-21T12:00:00.123456789Z"),
        )), sent.first().body)
    }

    @Test fun aSameIdReplacementStartsANewOwnerEvent() = runTest {
        val sent = mutableListOf<Sent>()
        var ids = 0
        val transport = HubChatTransport({ path, _, body, headers ->
            val buffer = okio.Buffer().also { body!!.writeTo(it) }
            sent += Sent(path, headers[HubChatTransport.OP_ID_HEADER], JsonCodec.parseToJsonElement(buffer.readUtf8()).jsonObject)
            response(path, """{"ok":true,"approval_reply":true,"interaction_id":"proposal-1"}""")
        }, newOpId = { "event-${++ids}" })
        val original = proposal()
        transport.replyOwnerApproval(original, "发")
        transport.replyOwnerApproval(original.copy(createdAt = "2026-11-21T12:00:01.123456789Z"), "发")
        assertEquals(listOf("event-1", "event-2"), sent.map { it.opId })
        assertEquals(2, sent.map { it.body["approval_reply_created_at"] }.toSet().size)
    }

    @Test fun aLaterUserRetryKeepsTheSameEventIdUntilSuccess() = runTest {
        val sent = mutableListOf<Sent>()
        var fail = true
        var ids = 0
        val transport = HubChatTransport({ path, _, body, headers ->
            val buffer = okio.Buffer().also { body!!.writeTo(it) }
            sent += Sent(path, headers[HubChatTransport.OP_ID_HEADER], JsonCodec.parseToJsonElement(buffer.readUtf8()).jsonObject)
            val receipt = if (fail) {
                fail = false
                """{"ok":true,"approval_reply":true,"interaction_id":"another-card"}"""
            } else {
                """{"ok":true,"approval_reply":true,"interaction_id":"proposal-1"}"""
            }
            response(path, receipt)
        }, newOpId = { "event-${++ids}" })

        assertTrue(runCatching { transport.replyOwnerApproval(proposal(), "发") }.isFailure)
        transport.replyOwnerApproval(proposal(), "发")

        assertEquals(2, sent.size)
        assertEquals(setOf("event-1"), sent.map { it.opId }.toSet())
        assertEquals(1, ids)
        assertEquals(1, sent.map { it.body }.toSet().size)
    }

    @Test fun unadvertisedPhraseOrIncompleteProposalSendsNothing() = runTest {
        var calls = 0
        val transport = HubChatTransport({ _, _, _, _ -> calls++; error("must not send") }, newOpId = { "event-1" })
        assertTrue(runCatching { transport.replyOwnerApproval(proposal(), "owner said yes") }.exceptionOrNull() is IllegalArgumentException)
        assertTrue(runCatching { transport.replyOwnerApproval(proposal().copy(approvalBatchId = null), "发") }.exceptionOrNull() is IllegalArgumentException)
        assertTrue(runCatching { transport.replyOwnerApproval(proposal().copy(ownerProposal = false), "发") }.exceptionOrNull() is IllegalArgumentException)
        assertEquals(0, calls)
    }

    @Test fun aMismatchedReceiptIsNotSuccessAndDoesNotFallBackToModelDelivery() = runTest {
        var calls = 0
        val transport = HubChatTransport({ path, _, _, _ ->
            calls++
            response(path, """{"ok":true,"approval_reply":true,"interaction_id":"another-card"}""")
        }, newOpId = { "event-1" })
        assertTrue(runCatching { transport.replyOwnerApproval(proposal(), "发") }.exceptionOrNull() is HubFailure)
        assertEquals(1, calls)
    }
}
